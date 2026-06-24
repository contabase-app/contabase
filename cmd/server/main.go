package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/contabase-app/contabase/internal/assets"
	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/httpcookies"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/contabase-app/contabase/internal/version"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	seedWorkspaceID               = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	seedUserID                    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seedDefaultPassword           = "12345678"
	sessionCookieName             = httpcookies.Session
	adminBackupImportMaxBodyBytes = 50 << 20
	bootstrapRestoreMaxBodyBytes  = 32 << 20
	bootstrapSetupTokenEnv        = "CONTABASE_SETUP_TOKEN"
	bootstrapSetupTokenMinLength  = 32
)

var adminBackupRestoreMu sync.Mutex

func main() {
	loadDotEnv(".env")
	configureLogger()

	mime.AddExtensionType(".css", "text/css; charset=utf-8")
	mime.AddExtensionType(".js", "application/javascript; charset=utf-8")

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = paths.DefaultDatabaseURL()
	}
	dbFilePath := sqlitePathFromURL(dbURL)

	db, err := database.Open(dbURL)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		log.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()

	slog.Info("database initialized successfully")

	seedIfEmpty(db)

	debugMode := strings.EqualFold(strings.TrimSpace(os.Getenv("APP_DEBUG")), "true")
	assets.SetDebugMode(debugMode)
	tpl, err := newAppTemplateEngine(debugMode, buildFuncMap())
	if err != nil {
		slog.Error("failed to parse templates", "error", err)
		log.Fatalf("failed to parse templates: %v", err)
	}

	authService := auth.NewService(db)
	csrfSigner, err := newCSRFSigner()
	if err != nil {
		slog.Error("failed to initialize csrf signer", "error", err)
		log.Fatalf("failed to initialize csrf signer: %v", err)
	}
	rateLimiter := newMemoryRateLimiter()
	loginRateLimitPolicy := rateLimitPolicy{Limit: 5, Window: 10 * time.Minute}
	activationRateLimitPolicy := rateLimitPolicy{Limit: 8, Window: 15 * time.Minute}
	activationTokenRateLimitPolicy := rateLimitPolicy{Limit: 5, Window: 30 * time.Minute}
	auth2FAIPRateLimitPolicy := rateLimitPolicy{Limit: 10, Window: 10 * time.Minute}
	auth2FASessionRateLimitPolicy := rateLimitPolicy{Limit: 5, Window: 10 * time.Minute}
	bootstrapSetupRateLimitPolicy := rateLimitPolicy{Limit: 3, Window: 30 * time.Minute}
	bootstrapRestoreRateLimitPolicy := rateLimitPolicy{Limit: 2, Window: time.Hour}
	adminBackupExportRateLimitPolicy := rateLimitPolicy{Limit: 6, Window: 5 * time.Minute}
	adminBackupImportRateLimitPolicy := rateLimitPolicy{Limit: 3, Window: 10 * time.Minute}
	adminIdentityMutationRateLimitPolicy := rateLimitPolicy{Limit: 20, Window: 10 * time.Minute}
	adminHeavyReadRateLimitPolicy := rateLimitPolicy{Limit: 10, Window: 5 * time.Minute}
	adminDebugMutationRateLimitPolicy := rateLimitPolicy{Limit: 3, Window: 30 * time.Minute}
	securitySettingsRateLimitPolicy := rateLimitPolicy{Limit: 5, Window: 10 * time.Minute}
	financialBulkRateLimitPolicy := rateLimitPolicy{Limit: 12, Window: 5 * time.Minute}
	bootstrapSetupGuard := newBootstrapSetupGuard(os.Getenv(bootstrapSetupTokenEnv))
	allowRateLimit := func(w http.ResponseWriter, r *http.Request, key string, policy rateLimitPolicy) bool {
		return allowRateLimitedRequest(w, r, rateLimiter, key, policy, time.Now())
	}
	publicIPRateLimitKey := func(scope string, r *http.Request) string {
		return scope + ":ip:" + clientIP(r)
	}
	tokenRateLimitKey := func(scope, token string) string {
		return scope + ":token:" + rateLimitKeyHash(token)
	}
	userIPRateLimitKey := func(scope string, ctx authContext, r *http.Request) string {
		return scope + ":user:" + ctx.UserID + ":ip:" + clientIP(r)
	}
	workspaceUserRouteRateLimitKey := func(scope string, ctx authContext, route string) string {
		return scope + ":workspace:" + ctx.ActiveWorkspaceID + ":user:" + ctx.UserID + ":route:" + route
	}
	baseURL := strings.TrimSpace(os.Getenv("APP_BASE_URL"))
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	txBase := handlers.TransactionHandler{DB: db, Templates: tpl}
	contatosBase := handlers.ContatosHandler{DB: db, Templates: tpl}
	contasBase := handlers.ContasHandler{DB: db, Templates: tpl}
	metasBase := handlers.MetasHandler{DB: db, Templates: tpl}
	relBase := handlers.RelatoriosHandler{DB: db, Templates: tpl}
	faturasBase := handlers.FaturasHandler{DB: db, Templates: tpl}
	notificacoesBase := handlers.NotificacoesHandler{DB: db, Templates: tpl}
	configBase := handlers.ConfiguracoesHandler{DB: db, Templates: tpl, AuthService: authService, BaseURL: baseURL}
	newConfigHandler := func(ctx authContext) handlers.ConfiguracoesHandler {
		h := configBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.ActorRole = ctx.Role
		h.SessionToken = ctx.SessionToken
		h.CanConfigRead = models.HasPermission(&ctx.Member, string(permConfigRead))
		h.CanConfigWrite = models.HasPermission(&ctx.Member, string(permConfigWrite))
		return h
	}
	refreshDeps := func(newDB *sql.DB) {
		db = newDB
		authService.DB = newDB
		txBase.DB = newDB
		contatosBase.DB = newDB
		contasBase.DB = newDB
		metasBase.DB = newDB
		relBase.DB = newDB
		faturasBase.DB = newDB
		notificacoesBase.DB = newDB
		configBase.DB = newDB
		configBase.AuthService = authService
	}
	securityAudit := newSecurityAuditService(func() *sql.DB { return db })
	logAdminAuditTampering := func(r *http.Request, ctx authContext) {
		logSecurityEventAsync(db, r, ctx.ActiveWorkspaceID, ctx.UserID, "security.tampering", "CRITICAL", map[string]any{
			"path":        r.URL.Path,
			"method":      r.Method,
			"reason":      "forbidden_admin_audit_access",
			"actor_role":  ctx.Role,
			"workspaceID": ctx.ActiveWorkspaceID,
		})
	}

	mux := http.NewServeMux()

	// Servir arquivos estáticos da pasta assets
	assetsFS := http.FileServer(http.Dir("assets"))
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
		assetsFS.ServeHTTP(w, r)
	})))
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("assets", "favicon.ico"))
	})
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("assets", "manifest.json"))
	})

	setupHandler := func(w http.ResponseWriter, r *http.Request) {
		csrfToken := csrfSigner.ensureCookie(w, r)
		bootstrapMode, err := isDatabaseBootstrapMode(db)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !bootstrapMode {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodGet {
			handleSetupPage(w, tpl, csrfToken, "")
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !csrfRequestValid(csrfSigner, r) {
			handleSetupCSRFError(w, tpl, csrfToken)
			return
		}
		if !allowRateLimit(w, r, publicIPRateLimitKey("auth:bootstrap:setup", r), bootstrapSetupRateLimitPolicy) {
			return
		}
		if !requireBootstrapSetupToken(w, r, tpl, csrfToken, bootstrapSetupGuard) {
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")
		workspaceType := normalizeWorkspaceType(r.FormValue("workspace_type"))
		if name == "" || email == "" || len(password) < 8 {
			handleSetupPage(w, tpl, csrfToken, "Preencha nome, e-mail e senha (mínimo 8 caracteres).")
			return
		}
		userID, err := runInitialSetup(db, name, email, password, workspaceType)
		if err != nil {
			handleSetupPage(w, tpl, csrfToken, "Não foi possível concluir o setup inicial.")
			return
		}
		if err := issueFinalSession(w, r, authService, userID, false); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		bootstrapSetupGuard.Consume()
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
	mux.HandleFunc("/setup", setupHandler)
	mux.HandleFunc("/signup", setupHandler)
	mux.HandleFunc("/setup/restaurar", func(w http.ResponseWriter, r *http.Request) {
		csrfToken := csrfSigner.ensureCookie(w, r)
		bootstrapMode, err := isDatabaseBootstrapMode(db)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !bootstrapMode {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := prepareBootstrapRestoreForm(w, r, bootstrapRestoreMaxBodyBytes); err != nil {
			handleSetupPage(w, tpl, csrfToken, "Não foi possível ler o arquivo enviado.")
			return
		}
		if !csrfRequestValid(csrfSigner, r) {
			handleSetupCSRFError(w, tpl, csrfToken)
			return
		}
		if !allowRateLimit(w, r, publicIPRateLimitKey("auth:bootstrap:restore", r), bootstrapRestoreRateLimitPolicy) {
			return
		}
		if !requireBootstrapSetupToken(w, r, tpl, csrfToken, bootstrapSetupGuard) {
			return
		}
		file, header, err := r.FormFile("backup_file")
		if err != nil {
			handleSetupPage(w, tpl, csrfToken, "Selecione um arquivo .db para restaurar.")
			return
		}
		defer file.Close()
		if strings.ToLower(filepath.Ext(header.Filename)) != ".db" {
			handleSetupPage(w, tpl, csrfToken, "Arquivo inválido. Envie um backup com extensão .db.")
			return
		}
		tmpRestorePath, cleanup, err := saveBootstrapBackupRestoreUpload(dbFilePath, file)
		if err != nil {
			log.Printf("bootstrap restore upload save failed: %v", err)
			handleSetupPage(w, tpl, csrfToken, "Não foi possível salvar o backup enviado.")
			return
		}
		defer cleanup()

		result, err := restoreBootstrapBackupFromFile(db, dbURL, dbFilePath, tmpRestorePath, database.Open, refreshDeps)
		if err != nil {
			log.Printf("bootstrap restore failed: %v", err)
			if result.RolledBack {
				handleSetupPage(w, tpl, csrfToken, "Não foi possível restaurar o backup. A restauração foi revertida; verifique os logs do servidor.")
			} else {
				handleSetupPage(w, tpl, csrfToken, "Backup inválido ou incompatível. Verifique os logs do servidor.")
			}
			return
		}
		log.Printf("bootstrap restore completed successfully")
		bootstrapSetupGuard.Consume()
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})

	mux.HandleFunc("/uploads/profile/", func(w http.ResponseWriter, r *http.Request) {
		serveProfileUpload(w, r)
	})

	// Secure workspace logo serve — validates that the requesting user is a member of the workspace.
	mux.HandleFunc("/uploads/workspaces/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		tail := strings.TrimPrefix(r.URL.Path, "/uploads/workspaces/")
		parts := strings.SplitN(tail, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		workspaceID := parts[0]
		fileName := parts[1]
		if !safeUploadPathSegment(workspaceID) || !safeUploadFileName(fileName) {
			http.NotFound(w, r)
			return
		}
		contentType, ok := uploadedImageContentType(fileName)
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Validate that the authenticated user is a member of this workspace.
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND workspace_id = ?`, ctx.UserID, workspaceID).Scan(&count); err != nil || count == 0 {
			http.NotFound(w, r)
			return
		}
		fullPath, err := safeServedUploadPath(paths.WorkspaceUploadsDir(workspaceID), fileName)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		serveUploadedImage(w, r, fullPath, contentType)
	}))

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		bootstrapMode, err := isDatabaseBootstrapMode(db)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if bootstrapMode {
			redirectToSetup(w, r)
			return
		}
		csrfToken := csrfSigner.ensureCookie(w, r)
		if r.Method == http.MethodGet {
			if isAuthenticated(w, r, authService) {
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			handleLoginPage(w, tpl, csrfToken, "")
			return
		}
		if r.Method == http.MethodPost {
			if !csrfRequestValid(csrfSigner, r) {
				http.Error(w, "csrf token inválido", http.StatusForbidden)
				return
			}
			now := time.Now()
			if decision := rateLimiter.Allow(publicIPRateLimitKey("auth:login", r), loginRateLimitPolicy, now); !decision.Allowed {
				writeAuthAuditEvent(db, r, "", "", "LOGIN_RATE_LIMITED", map[string]string{"scope": "ip"})
				writeRateLimitExceeded(w, r, decision)
				return
			}
			if err := r.ParseForm(); err == nil {
				email := normalizeEmail(r.FormValue("email"))
				if email != "" {
					if decision := rateLimiter.Allow("auth:login:email:"+email, loginRateLimitPolicy, now); !decision.Allowed {
						writeAuthAuditEvent(db, r, "", "", "LOGIN_RATE_LIMITED", map[string]string{"scope": "email"})
						writeRateLimitExceeded(w, r, decision)
						return
					}
				}
			}
			handleLoginSubmit(w, r, tpl, authService, db, csrfToken)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/login/2fa", func(w http.ResponseWriter, r *http.Request) {
		csrfToken := csrfSigner.ensureCookie(w, r)
		if r.Method == http.MethodGet {
			handleLogin2FAPage(w, tpl, csrfToken, "")
			return
		}
		if r.Method == http.MethodPost {
			if !csrfRequestValid(csrfSigner, r) {
				http.Error(w, "csrf token inválido", http.StatusForbidden)
				return
			}
			if !allowRateLimit(w, r, publicIPRateLimitKey("auth:2fa", r), auth2FAIPRateLimitPolicy) {
				return
			}
			if preAuthCookie, err := r.Cookie(preAuthCookieName); err == nil && strings.TrimSpace(preAuthCookie.Value) != "" {
				if !allowRateLimit(w, r, tokenRateLimitKey("auth:2fa", preAuthCookie.Value), auth2FASessionRateLimitPolicy) {
					return
				}
			}
			handleLogin2FASubmit(w, r, tpl, authService, db, csrfToken)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/ativar-conta", func(w http.ResponseWriter, r *http.Request) {
		csrfToken := csrfSigner.ensureCookie(w, r)
		if r.Method == http.MethodGet {
			handleActivationPage(w, tpl, csrfToken, strings.TrimSpace(r.URL.Query().Get("token")), "")
			return
		}
		if r.Method == http.MethodPost {
			if !csrfRequestValid(csrfSigner, r) {
				http.Error(w, "csrf token inválido", http.StatusForbidden)
				return
			}
			now := time.Now()
			if decision := rateLimiter.Allow(publicIPRateLimitKey("auth:activation", r), activationRateLimitPolicy, now); !decision.Allowed {
				writeRateLimitExceeded(w, r, decision)
				return
			}
			token := strings.TrimSpace(r.FormValue("token"))
			if token != "" {
				if decision := rateLimiter.Allow(tokenRateLimitKey("auth:activation", token), activationTokenRateLimitPolicy, now); !decision.Allowed {
					writeRateLimitExceeded(w, r, decision)
					return
				}
			}
			password := r.FormValue("password")
			confirm := r.FormValue("password_confirm")
			if token == "" || password == "" || confirm == "" {
				handleActivationPage(w, tpl, csrfToken, token, "Preencha todos os campos.")
				return
			}
			if password != confirm {
				handleActivationPage(w, tpl, csrfToken, token, "As senhas não conferem.")
				return
			}
			if len(password) < 8 {
				handleActivationPage(w, tpl, csrfToken, token, "Use no mínimo 8 caracteres.")
				return
			}
			if err := authService.ActivateAccount(token, password); err != nil {
				handleActivationPage(w, tpl, csrfToken, token, "Token inválido ou expirado.")
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/logout", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleLogout(w, r, authService)
	}))
	mux.HandleFunc("/session/check", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/trocar-senha", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		switch r.Method {
		case http.MethodGet:
			handleRequiredPasswordChangePage(w, tpl, csrfTokenFromRequestCookie(r), "")
		case http.MethodPost:
			handleRequiredPasswordChangeSubmit(w, r, tpl, authService, db, ctx, csrfTokenFromRequestCookie(r))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/workspace/switch", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspaceID := r.FormValue("workspace_id")
		if workspaceID == "" {
			slog.Error("workspace switch failed: empty workspace_id", "user_id", ctx.UserID)
			http.Error(w, "workspace_id required", http.StatusBadRequest)
			return
		}
		slog.Debug("workspace switch requested", "user_id", ctx.UserID, "workspace_id", workspaceID, "active_workspace_id", ctx.ActiveWorkspaceID)
		member, err := authService.ResolveWorkspaceMember(ctx.UserID, workspaceID)
		if err != nil {
			slog.Error("workspace switch failed: membership not found", "user_id", ctx.UserID, "workspace_id", workspaceID, "error", err)
			setSecOpsFailureHeaders(w, "security.tampering", "CRITICAL", "workspace_scope_violation", "")
			w.Header().Set("X-SecOps-Meta-Workspace-Id", strings.TrimSpace(workspaceID))
			http.Error(w, "workspace not found", http.StatusForbidden)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			slog.Error("workspace switch failed: missing session cookie", "user_id", ctx.UserID, "workspace_id", workspaceID)
			http.Error(w, "session not found", http.StatusUnauthorized)
			return
		}
		if err := authService.UpdateSessionWorkspace(cookie.Value, workspaceID); err != nil {
			slog.Error("workspace switch failed: unable to update session", "user_id", ctx.UserID, "workspace_id", workspaceID, "error", err)
			http.Error(w, "unable to switch workspace", http.StatusInternalServerError)
			return
		}
		slog.Debug("workspace switch session updated", "user_id", ctx.UserID, "workspace_id", workspaceID, "role", member.Role)
		http.SetCookie(w, &http.Cookie{
			Name:     "workspace_role",
			Value:    member.Role,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   shouldUseSecureCookie(r),
			MaxAge:   86400 * 30,
		})
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}))
	mux.HandleFunc("/ajuda", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permDashboardRead) {
			respondForbidden(w, r)
			return
		}

		var activeWorkspaceName string
		_ = db.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, ctx.ActiveWorkspaceID).Scan(&activeWorkspaceName)
		wsType := rawWorkspaceType(db, ctx.ActiveWorkspaceID)
		isBusiness := wsType == models.WorkspaceTypeBusiness

		var userName, profilePhotoURL string
		var updatedAt int64
		_ = db.QueryRow(`SELECT name, COALESCE(profile_photo_path, ''), COALESCE(updated_at, unixepoch()) FROM users WHERE id = ?`, ctx.UserID).Scan(&userName, &profilePhotoURL, &updatedAt)

		userInitials := "US"
		if userName != "" {
			parts := strings.Fields(userName)
			if len(parts) == 1 {
				r := []rune(parts[0])
				if len(r) > 0 {
					userInitials = strings.ToUpper(string(r[0]))
				}
			} else if len(parts) > 1 {
				r1 := []rune(parts[0])
				rn := []rune(parts[len(parts)-1])
				if len(r1) > 0 && len(rn) > 0 {
					userInitials = strings.ToUpper(string(r1[0])) + strings.ToUpper(string(rn[0]))
				}
			}
		}

		userFirstName := "Usuário"
		if userName != "" {
			parts := strings.Fields(userName)
			if len(parts) > 0 {
				userFirstName = parts[0]
			}
		}

		fullPhotoURL := ""
		if profilePhotoURL != "" {
			fileName := strings.TrimPrefix(profilePhotoURL, "/uploads/profile/")
			if fileName != profilePhotoURL && fileName != "" {
				fullPath := filepath.Join(paths.ProfileUploadsDir(), fileName)
				if _, err := os.Stat(fullPath); err == nil {
					fullPhotoURL = fmt.Sprintf("%s?v=%d", profilePhotoURL, updatedAt)
				}
			}
		}

		data := struct {
			Title               string
			UserInitials        string
			UserFirstName       string
			ActiveWorkspaceName string
			ProfilePhotoURL     string
			IsBusiness          bool
		}{
			Title:               "Ajuda",
			UserInitials:        userInitials,
			UserFirstName:       userFirstName,
			ActiveWorkspaceName: activeWorkspaceName,
			ProfilePhotoURL:     fullPhotoURL,
			IsBusiness:          isBusiness,
		}

		if err := tpl.ExecuteTemplate(w, "ajuda-page", data); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}))
	mux.HandleFunc("/workspace/context", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var wsType, themeToken string
		if err := db.QueryRow(`SELECT COALESCE(type, 'personal'), COALESCE(theme_token, '') FROM workspaces WHERE id = ?`, ctx.ActiveWorkspaceID).Scan(&wsType, &themeToken); err != nil {
			http.Error(w, "workspace not found", http.StatusNotFound)
			return
		}
		theme := handlers.ResolveWorkspaceTheme(themeToken, wsType)
		payload := struct {
			Type          string `json:"type"`
			ThemeToken    string `json:"theme_token"`
			ThemeName     string `json:"theme_name"`
			AccentRGB     string `json:"accent_rgb"`
			AccentSoftRGB string `json:"accent_soft_rgb"`
			AccentTextRGB string `json:"accent_text_rgb"`
		}{
			Type:          wsType,
			ThemeToken:    theme.Token,
			ThemeName:     theme.Nome,
			AccentRGB:     theme.AccentRGB,
			AccentSoftRGB: theme.AccentSoftRGB,
			AccentTextRGB: theme.AccentTextRGB,
		}
		if wsType != "business" {
			wsType = "personal"
		}
		payload.Type = wsType
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(payload)
	}))
	mux.HandleFunc("/admin/seed-demo-b2b", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.EqualFold(strings.TrimSpace(os.Getenv("APP_DEBUG")), "true") {
			http.Error(w, "seed demo desabilitada fora do modo debug", http.StatusForbidden)
			return
		}
		if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
			respondForbidden(w, r)
			return
		}
		if !allowRateLimit(w, r, workspaceUserRouteRateLimitKey("admin:debug:seed-demo-b2b", ctx, r.URL.Path), adminDebugMutationRateLimitPolicy) {
			return
		}
		if err := seedDemoB2B(db, ctx.ActiveWorkspaceID, ctx.UserID); err != nil {
			http.Error(w, "erro ao gerar seed demo B2B: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		slog.Warn("admin_action", "action", "seed_demo_b2b", "actor_user_id", ctx.UserID, "workspace_id", ctx.ActiveWorkspaceID, "client_ip", clientIP(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Seed demo B2B aplicada com sucesso para Abril, Maio e Junho de 2026."))
	}))
	mux.HandleFunc("/admin/backups/exportar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:backup:export", ctx, r), adminBackupExportRateLimitPolicy) {
			return
		}

		backupPath, cleanup, err := createAdminExportBackup(db, dbFilePath)
		if err != nil {
			log.Printf("backup export prepare error: %v", err)
			http.Error(w, "backup indisponivel", http.StatusInternalServerError)
			return
		}
		defer cleanup()

		f, err := os.Open(backupPath)
		if err != nil {
			log.Printf("backup export open error: %v", err)
			http.Error(w, "backup indisponivel", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		setAdminBackupDownloadHeaders(w, time.Now())
		if _, err := io.Copy(w, f); err != nil {
			log.Printf("backup export error: %v", err)
		}
		slog.Info("admin_action", "action", "backup_export", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
	}))

	mux.HandleFunc("/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !database.HasAnyUser(db) {
			redirectToSetup(w, r)
			return
		}
		if !hasPermission(ctx.Role, permDashboardRead) {
			respondForbidden(w, r)
			return
		}

		t0 := time.Now()
		reqID := handlers.PerfReqID()
		dbB := handlers.DbSnap(db)

		data := handlers.BuildDashboardData(db, ctx.UserID, ctx.ActiveWorkspaceID)
		handlers.PerfStep(reqID, "Dashboard", "BuildDashboardData", time.Since(t0))

		tR := time.Now()
		if err := tpl.ExecuteTemplate(w, "dashboard-page", data); err != nil {
			log.Printf("template error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		handlers.PerfStep(reqID, "Dashboard", "templateRender", time.Since(tR))

		dbA := handlers.DbSnap(db)
		handlers.PerfDBDelta(reqID, "Dashboard", "total", dbB, dbA)
		handlers.PerfRequest(reqID, r, time.Since(t0), 0)
	}))

	mux.HandleFunc("/transacoes/nova", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !hasPermission(ctx.Role, permTransactionsCreate) {
			respondForbidden(w, r)
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleNovaTransacao(w, r)
	}))
	mux.HandleFunc("/transacoes/preditiva", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsCreate) {
			respondForbidden(w, r)
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleTransacaoPreditiva(w, r)
	}))
	mux.HandleFunc("/api/categorias/seletor", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsCreate) {
			respondForbidden(w, r)
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleCategorySelector(w, r)
	}))
	mux.HandleFunc("/api/categorias/novo", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permConfigWrite) {
			respondForbidden(w, r)
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleCategoryCreateForm(w, r)
	}))
	mux.HandleFunc("/api/categorias", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permConfigWrite) {
			respondForbidden(w, r)
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleCategoryCreateAPI(w, r)
	}))
	mux.HandleFunc("/lancamentos", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleListarTransacoes(w, r)
	}))

	mux.HandleFunc("/contatos", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := contatosBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		switch r.Method {
		case http.MethodGet:
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleContatosConceito(w, r)
		case http.MethodPost:
			if !hasPermission(ctx.Role, permTransactionsCreate) {
				respondForbidden(w, r)
				return
			}
			h.HandleCriarContato(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/contatos/options", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		h := contatosBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleOptionsContato(w, r)
	}))
	mux.HandleFunc("/contatos/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := contatosBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		rest := strings.TrimPrefix(r.URL.Path, "/contatos/")
		if rest == "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(rest, "/")
		id := strings.TrimSpace(parts[0])
		if id == "" {
			http.NotFound(w, r)
			return
		}

		if len(parts) == 2 && parts[1] == "salvar" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !hasPermission(ctx.Role, permTransactionsUpdate) {
				respondForbidden(w, r)
				return
			}
			h.HandleAtualizarContato(w, r, id)
			return
		}
		if len(parts) != 1 {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleDetalheContato(w, r, id)
		case http.MethodDelete:
			if !models.HasPermission(&ctx.Member, models.PermissionContactsDelete) {
				respondForbidden(w, r)
				return
			}
			h.HandleExcluirContato(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/transacoes", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		switch r.Method {
		case http.MethodGet:
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleListarTransacoes(w, r)
		case http.MethodPost:
			if !hasPermission(ctx.Role, permTransactionsCreate) {
				respondForbidden(w, r)
				return
			}
			h.HandleSalvarTransacao(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/transacoes/bulk/delete", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsDelete) {
			respondForbidden(w, r)
			return
		}
		if !allowRateLimit(w, r, workspaceUserRouteRateLimitKey("financial:bulk", ctx, r.URL.Path), financialBulkRateLimitPolicy) {
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleBulkDelete(w, r)
	}))
	mux.HandleFunc("/transacoes/bulk/update", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsUpdate) {
			respondForbidden(w, r)
			return
		}
		if !allowRateLimit(w, r, workspaceUserRouteRateLimitKey("financial:bulk", ctx, r.URL.Path), financialBulkRateLimitPolicy) {
			return
		}
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleBulkUpdate(w, r)
	}))
	mux.HandleFunc("/faturas", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		h := faturasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleFaturaConceito(w, r)
	}))

	mux.HandleFunc("/cartoes/faturas/pagar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permInvoicesStatusUpdate) {
			respondForbidden(w, r)
			return
		}
		h := faturasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandlePagarFatura(w, r)
	}))

	mux.HandleFunc("/cartoes/faturas/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		invoiceID := strings.TrimPrefix(r.URL.Path, "/cartoes/faturas/")
		if invoiceID == "" || strings.Contains(invoiceID, "/") {
			http.NotFound(w, r)
			return
		}
		h := faturasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleFaturaConceitoPorID(w, r, invoiceID)
	}))
	mux.HandleFunc("/cartoes/faturas-disponiveis/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		accountID := strings.TrimPrefix(r.URL.Path, "/cartoes/faturas-disponiveis/")
		if accountID == "" || strings.Contains(accountID, "/") {
			http.NotFound(w, r)
			return
		}
		h := faturasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleListarFaturasDisponiveis(w, r, accountID)
	}))
	mux.HandleFunc("/cartoes/fatura-destino/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		accountID := strings.TrimPrefix(r.URL.Path, "/cartoes/fatura-destino/")
		if accountID == "" || strings.Contains(accountID, "/") {
			http.NotFound(w, r)
			return
		}
		h := faturasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleResolverDestinoFatura(w, r, accountID)
	}))
	mux.HandleFunc("/cartoes/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		rest := strings.TrimPrefix(r.URL.Path, "/cartoes/")
		parts := strings.Split(rest, "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] != "faturas" {
			http.NotFound(w, r)
			return
		}
		mesStr := r.URL.Query().Get("mes")
		anoStr := r.URL.Query().Get("ano")
		mes, mesErr := strconv.Atoi(mesStr)
		ano, anoErr := strconv.Atoi(anoStr)
		if mesErr != nil || anoErr != nil {
			http.Error(w, "competência inválida", http.StatusBadRequest)
			return
		}
		h := faturasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		if len(parts) == 2 && r.Method == http.MethodGet {
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleExibirFaturaPorCartaoMes(w, r, parts[0], mes, ano)
			return
		}
		if len(parts) == 3 && parts[2] == "abrir" && r.Method == http.MethodPost {
			if !hasPermission(ctx.Role, permInvoicesStatusUpdate) {
				respondForbidden(w, r)
				return
			}
			h.HandleAbrirFaturaPorCartaoMes(w, r, parts[0], mes, ano)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))

	mux.HandleFunc("/transacoes/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := txBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		rest := strings.TrimPrefix(r.URL.Path, "/transacoes/")
		if rest == "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(rest, "/")
		id := parts[0]

		if len(parts) == 2 && parts[1] == "status-pagamento" {
			if r.Method == http.MethodPost {
				if !hasPermission(ctx.Role, permInvoicesStatusUpdate) {
					respondForbidden(w, r)
					return
				}
				h.HandleTogglePagamento(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "mover-fatura" {
			if r.Method == http.MethodPost {
				if !hasPermission(ctx.Role, permInvoicesStatusUpdate) {
					respondForbidden(w, r)
					return
				}
				h.HandleMoverFatura(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "editar" {
			if r.Method == http.MethodGet {
				h.HandleDetalheTransacao(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "salvar" {
			if r.Method == http.MethodPost || r.Method == http.MethodPut {
				if !hasPermission(ctx.Role, permTransactionsUpdate) {
					respondForbidden(w, r)
					return
				}
				h.HandleAtualizarTransacao(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "recibo" {
			if r.Method == http.MethodGet {
				if !hasPermission(ctx.Role, permTransactionsRead) {
					respondForbidden(w, r)
					return
				}
				h.HandleGerarRecibo(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "anexo" {
			if !hasPermission(ctx.Role, permAttachmentRead) {
				respondForbidden(w, r)
				return
			}
			if ctx.Role == models.RoleUser && !userOwnsTransaction(db, id, ctx.UserID, ctx.ActiveWorkspaceID) {
				respondForbidden(w, r)
				return
			}
			h.HandleDownloadAnexo(w, r, id)
			return
		}

		if len(parts) != 1 {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodDelete:
			if !hasPermission(ctx.Role, permTransactionsDelete) {
				respondForbidden(w, r)
				return
			}
			h.HandleDeletarTransacao(w, r, id)
		case http.MethodGet:
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleDetalheTransacao(w, r, id)
		case http.MethodPut:
			if !hasPermission(ctx.Role, permTransactionsUpdate) {
				respondForbidden(w, r)
				return
			}
			h.HandleAtualizarTransacao(w, r, id)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/metas", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		switch r.Method {
		case http.MethodGet:
			if !hasPermission(ctx.Role, permDashboardRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleMetasConceito(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))

	mux.HandleFunc("/metas/novo", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permGoalsWrite) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleNovaMeta(w, r)
	}))
	mux.HandleFunc("/metas/caixinha", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permGoalsWrite) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleCriarCaixinha(w, r)
	}))
	mux.HandleFunc("/metas/limite", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permGoalsWrite) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleCriarLimite(w, r)
	}))
	mux.HandleFunc("/metas/caixinha/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method == http.MethodDelete {
			if !hasPermission(ctx.Role, permGoalsWrite) {
				respondForbidden(w, r)
				return
			}
			h := metasBase
			h.WorkspaceID = ctx.ActiveWorkspaceID
			h.UserID = ctx.UserID
			h.HandleDeleteCaixinha(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/metas/limite/historico", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permDashboardRead) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleHistoricoLimite(w, r)
	}))
	mux.HandleFunc("/metas/limite/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method == http.MethodDelete {
			if !hasPermission(ctx.Role, permGoalsWrite) {
				respondForbidden(w, r)
				return
			}
			h := metasBase
			h.WorkspaceID = ctx.ActiveWorkspaceID
			h.UserID = ctx.UserID
			h.HandleDeleteLimite(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/metas/caixinha/aporte", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permGoalsWrite) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleAporteCaixinha(w, r)
	}))
	mux.HandleFunc("/metas/caixinha/resgate", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permGoalsWrite) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleResgateCaixinha(w, r)
	}))
	mux.HandleFunc("/metas/caixinha/historico", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permGoalsWrite) {
			respondForbidden(w, r)
			return
		}
		h := metasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleHistoricoCaixinha(w, r)
	}))

	mux.HandleFunc("/relatorios", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := relBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		switch r.Method {
		case http.MethodGet:
			if !models.HasPermission(&ctx.Member, models.PermissionReportsView) {
				respondForbidden(w, r)
				return
			}
			h.HandleRelatoriosConceito(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/relatorios/export.csv", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !models.HasPermission(&ctx.Member, models.PermissionReportsView) {
			respondForbidden(w, r)
			return
		}
		h := relBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleExportarCSV(w, r)
	}))

	mux.HandleFunc("/contas", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		h := contasBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleContasConceito(w, r)
	}))

	mux.HandleFunc("/notificacoes", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := notificacoesBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleExibirNotificacoes(w, r)
	}))
	mux.HandleFunc("/notificacoes/limpar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permTransactionsRead) {
			respondForbidden(w, r)
			return
		}
		h := notificacoesBase
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleLimparTudo(w, r)
	}))
	mux.HandleFunc("/notificacoes/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		rest := strings.TrimPrefix(r.URL.Path, "/notificacoes/")
		parts := strings.Split(rest, "/")

		// Case 1: DELETE /notificacoes/{key}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h := notificacoesBase
			h.WorkspaceID = ctx.ActiveWorkspaceID
			h.UserID = ctx.UserID
			h.HandleApagarNotificacao(w, r, parts[0])
			return
		}

		// Case 2: POST /notificacoes/{key}/apagar
		if len(parts) == 2 && parts[1] == "apagar" {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !hasPermission(ctx.Role, permTransactionsRead) {
				respondForbidden(w, r)
				return
			}
			h := notificacoesBase
			h.WorkspaceID = ctx.ActiveWorkspaceID
			h.UserID = ctx.UserID
			h.HandleApagarNotificacao(w, r, parts[0])
			return
		}

		http.NotFound(w, r)
	}))

	mux.HandleFunc("/configuracoes", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := newConfigHandler(ctx)
		h.HandleConfiguracoesConceito(w, r)
	}))

	mux.HandleFunc("/configuracoes/categorias", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := newConfigHandler(ctx)
		if r.Method == http.MethodGet {
			if !models.HasPermission(&ctx.Member, string(permConfigRead)) {
				respondForbidden(w, r)
				return
			}
			h.HandleConfiguracoesSection(w, r, "categorias")
			return
		}
		if r.Method == http.MethodPost {
			if !hasPermission(ctx.Role, permConfigWrite) {
				respondForbidden(w, r)
				return
			}
			h.HandleCategoriasCreate(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/configuracoes/contas", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := newConfigHandler(ctx)
		if r.Method == http.MethodGet {
			if !models.HasPermission(&ctx.Member, string(permConfigRead)) {
				respondForbidden(w, r)
				return
			}
			h.HandleConfiguracoesSection(w, r, "contas")
			return
		}
		if r.Method == http.MethodPost {
			if !hasPermission(ctx.Role, permConfigWrite) {
				respondForbidden(w, r)
				return
			}
			h.HandleContasCreate(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/configuracoes/cartoes", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := newConfigHandler(ctx)
		if r.Method == http.MethodGet {
			if !models.HasPermission(&ctx.Member, string(permConfigRead)) {
				respondForbidden(w, r)
				return
			}
			h.HandleConfiguracoesSection(w, r, "cartoes")
			return
		}
		if r.Method == http.MethodPost {
			if !hasPermission(ctx.Role, permConfigWrite) {
				respondForbidden(w, r)
				return
			}
			h.HandleCartoesCreate(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/configuracoes/contas/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
			respondForbidden(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/configuracoes/contas/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		h := newConfigHandler(ctx)

		if len(parts) == 1 {
			if r.Method == http.MethodGet {
				h.HandleContasView(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "formulario" {
			if r.Method == http.MethodGet {
				h.HandleContasInlineForm(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "salvar" {
			if r.Method == http.MethodPost {
				h.HandleContasInlineSave(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "arquivar" {
			if r.Method == http.MethodPost {
				h.HandleContaArchive(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "reativar" {
			if r.Method == http.MethodPost {
				h.HandleContaUnarchive(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && (parts[1] == "subir" || parts[1] == "descer") {
			if r.Method == http.MethodPost {
				h.HandleContasReorder(w, r, id, map[string]string{"subir": "up", "descer": "down"}[parts[1]])
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, r)
	}))
	mux.HandleFunc("/configuracoes/cartoes/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
			respondForbidden(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/configuracoes/cartoes/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || strings.TrimSpace(parts[0]) == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		h := newConfigHandler(ctx)

		if len(parts) == 1 {
			if r.Method == http.MethodGet {
				h.HandleCartoesView(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "formulario" {
			if r.Method == http.MethodGet {
				h.HandleCartoesInlineForm(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "salvar" {
			if r.Method == http.MethodPost {
				h.HandleCartoesInlineSave(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "arquivar" {
			if r.Method == http.MethodPost {
				h.HandleCartaoArchive(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "reativar" {
			if r.Method == http.MethodPost {
				h.HandleCartaoUnarchive(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && (parts[1] == "subir" || parts[1] == "descer") {
			if r.Method == http.MethodPost {
				h.HandleCartoesReorder(w, r, id, map[string]string{"subir": "up", "descer": "down"}[parts[1]])
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, r)
	}))
	mux.HandleFunc("/configuracoes/workspace", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := newConfigHandler(ctx)
		if r.Method == http.MethodGet {
			if !models.HasPermission(&ctx.Member, string(permConfigRead)) {
				respondForbidden(w, r)
				return
			}
			if rawWorkspaceType(db, ctx.ActiveWorkspaceID) != models.WorkspaceTypeBusiness {
				h.RenderConfigSectionWithFlashStatus(w, "workspace", "Perfil corporativo disponível apenas para workspaces Business.", "", http.StatusForbidden)
				return
			}
			h.HandleConfiguracoesSection(w, r, "workspace")
			return
		}
		if r.Method == http.MethodPost {
			if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
				respondForbidden(w, r)
				return
			}
			if rawWorkspaceType(db, ctx.ActiveWorkspaceID) != models.WorkspaceTypeBusiness {
				h.RenderConfigSectionWithFlashStatus(w, "workspace", "Perfil corporativo disponível apenas para workspaces Business.", "", http.StatusForbidden)
				return
			}
			h.HandleWorkspaceCorporateProfileUpdate(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/configuracoes/perfil", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		h := newConfigHandler(ctx)
		if r.Method == http.MethodGet {
			if !hasPermission(ctx.Role, permProfileRead) {
				respondForbidden(w, r)
				return
			}
			h.HandleConfiguracoesSection(w, r, "perfil")
			return
		}
		if r.Method == http.MethodPost {
			if !hasPermission(ctx.Role, permProfileWrite) {
				respondForbidden(w, r)
				return
			}
			h.HandlePerfilUpdate(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/configuracoes/perfil/padrao", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permProfileWrite) {
			respondForbidden(w, r)
			return
		}
		h := newConfigHandler(ctx)
		h.HandlePerfilDefaultWorkspaceUpdate(w, r)
	}))
	mux.HandleFunc("/configuracoes/perfil/senha", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permProfileWrite) {
			respondForbidden(w, r)
			return
		}
		h := newConfigHandler(ctx)
		h.HandlePasswordChange(w, r)
	}))
	mux.HandleFunc("/configuracoes/perfil/sessoes/revogar-outras", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permProfileWrite) {
			respondForbidden(w, r)
			return
		}
		h := newConfigHandler(ctx)
		h.HandleRevokeOtherSessions(w, r)
	}))
	mux.HandleFunc("/configuracoes/seguranca", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleSecuritySettingsPage(w, r, tpl, authService, ctx.UserID)
	}))
	mux.HandleFunc("/configuracoes/seguranca/totp/setup", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("security:totp:setup", ctx, r), securitySettingsRateLimitPolicy) {
			return
		}
		handleTOTPSetupStart(w, r, tpl, authService, ctx.UserID)
	}))
	mux.HandleFunc("/configuracoes/seguranca/totp/confirmar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("security:totp:confirm", ctx, r), securitySettingsRateLimitPolicy) {
			return
		}
		handleTOTPSetupConfirm(w, r, tpl, authService, db, ctx.UserID)
	}))
	mux.HandleFunc("/configuracoes/seguranca/totp/cancelar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("security:totp:cancel", ctx, r), securitySettingsRateLimitPolicy) {
			return
		}
		handleTOTPCancel(w, r, tpl)
	}))
	mux.HandleFunc("/configuracoes/seguranca/totp/desativar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("security:totp:disable", ctx, r), securitySettingsRateLimitPolicy) {
			return
		}
		handleTOTPDisable(w, r, tpl, authService, db, ctx.UserID)
	}))
	mux.HandleFunc("/configuracoes/seguranca/backup-codes/regenerar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("security:backup-codes:regenerate", ctx, r), securitySettingsRateLimitPolicy) {
			return
		}
		handleBackupCodesRegenerate(w, r, tpl, authService, db, ctx.UserID)
	}))
	mux.HandleFunc("/configuracoes/membros", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		respondForbidden(w, r)
	}))
	mux.HandleFunc("/configuracoes/categorias/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
			respondForbidden(w, r)
			return
		}
		h := newConfigHandler(ctx)
		rest := strings.TrimPrefix(r.URL.Path, "/configuracoes/categorias/")
		parts := strings.Split(rest, "/")
		if len(parts) < 1 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		id := parts[0]
		if len(parts) == 1 {
			if r.Method == http.MethodGet {
				h.HandleCategoriasView(w, r, id)
				return
			}
			if r.Method == http.MethodDelete {
				h.HandleCategoriasDelete(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "formulario" {
			if r.Method == http.MethodGet {
				h.HandleCategoriasInlineForm(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if len(parts) == 2 && parts[1] == "salvar" {
			if r.Method == http.MethodPost {
				h.HandleCategoriasInlineSave(w, r, id)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.NotFound(w, r)
	}))
	mux.HandleFunc("/admin/usuarios", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		h := newConfigHandler(ctx)
		h.HandleConfiguracoesSection(w, r, "admin-users")
	}))
	mux.HandleFunc("/admin/usuarios/salvar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:users-save", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		h := newConfigHandler(ctx)
		slog.Info("admin_action", "action", "admin_users_save", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		h.HandleAdminUsersSave(w, r)
	}))
	mux.HandleFunc("/admin/usuarios/reset-senha", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:reset-password", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		h := newConfigHandler(ctx)
		slog.Warn("admin_action", "action", "admin_users_reset_password", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		h.HandleAdminUsersResetPassword(w, r)
	}))
	mux.HandleFunc("/admin/usuarios/desativar-2fa", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:disable-2fa", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		h := newConfigHandler(ctx)
		slog.Warn("admin_action", "action", "admin_users_disable_2fa", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		h.HandleAdminUsersDisable2FA(w, r)
	}))
	mux.HandleFunc("/admin/usuarios/revogar-sessoes", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:revoke-sessions", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		h := newConfigHandler(ctx)
		slog.Warn("admin_action", "action", "admin_users_revoke_sessions", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		h.HandleAdminUsersRevokeSessions(w, r)
	}))
	mux.HandleFunc("/admin/workspaces", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:workspaces-save", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		h := newConfigHandler(ctx)
		h.HandleConfiguracoesSection(w, r, "admin-workspaces")
	}))
	mux.HandleFunc("/admin/workspaces/salvar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:workspaces-edit", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		h := newConfigHandler(ctx)
		slog.Info("admin_action", "action", "admin_workspace_create", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		h.HandleAdminWorkspacesCreate(w, r)
	}))
	mux.HandleFunc("/admin/workspaces/editar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		h := newConfigHandler(ctx)
		workspaceID := strings.TrimSpace(r.FormValue("workspace_id"))
		slog.Info("admin_action", "action", "admin_workspace_edit", "actor_user_id", ctx.UserID, "target_workspace_id", workspaceID, "client_ip", clientIP(r))
		h.HandleAdminWorkspaceEdit(w, r, workspaceID)
	}))
	mux.HandleFunc("/workspaces/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:workspace-delete", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		workspaceID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/workspaces/"))
		if workspaceID == "" || strings.Contains(workspaceID, "/") {
			http.NotFound(w, r)
			return
		}
		h := newConfigHandler(ctx)
		slog.Warn("admin_action", "action", "admin_workspace_delete", "actor_user_id", ctx.UserID, "target_workspace_id", workspaceID, "client_ip", clientIP(r))
		h.HandleAdminWorkspaceDelete(w, r, workspaceID)
	}))
	mux.HandleFunc("/users/", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:identity:user-delete", ctx, r), adminIdentityMutationRateLimitPolicy) {
			return
		}
		userID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/users/"))
		if userID == "" || strings.Contains(userID, "/") {
			http.NotFound(w, r)
			return
		}
		h := newConfigHandler(ctx)
		slog.Warn("admin_action", "action", "admin_user_delete", "actor_user_id", ctx.UserID, "target_user_id", userID, "client_ip", clientIP(r))
		h.HandleAdminUserDelete(w, r, userID)
	}))
	mux.HandleFunc("/workspace/list", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		type wsItem struct {
			ID         string
			Name       string
			Type       string
			ThemeToken string
		}
		var workspaces []wsItem
		rows, err := db.Query(`
			SELECT w.id, w.name, COALESCE(w.type, 'personal'), COALESCE(w.theme_token, '')
			FROM workspaces w
			JOIN workspace_members wm ON w.id = wm.workspace_id
			WHERE wm.user_id = ?
			ORDER BY wm.joined_at ASC
		`, ctx.UserID)
		if err != nil {
			slog.Error("workspace list query failed", "user_id", ctx.UserID, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var ws wsItem
			if err := rows.Scan(&ws.ID, &ws.Name, &ws.Type, &ws.ThemeToken); err != nil {
				slog.Error("workspace list scan failed", "user_id", ctx.UserID, "error", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			workspaces = append(workspaces, ws)
		}
		if err := rows.Err(); err != nil {
			slog.Error("workspace list iteration failed", "user_id", ctx.UserID, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		slog.Debug("workspace list loaded", "user_id", ctx.UserID, "workspace_count", len(workspaces))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		for _, ws := range workspaces {
			active := ""
			theme := handlers.ResolveWorkspaceTheme(ws.ThemeToken, ws.Type)
			if ws.ID == ctx.ActiveWorkspaceID {
				active = fmt.Sprintf(` style="border-color:rgb(%s/0.44);background:rgb(%s/0.14)"`, theme.AccentRGB, theme.AccentRGB)
			}
			typeLabel := "Pessoal"
			if ws.Type == "business" {
				typeLabel = "Empresarial"
			}
			fmt.Fprintf(w,
				`<button type="button"`+
					` hx-post="/workspace/switch"`+
					` hx-vals="{&quot;workspace_id&quot;:&quot;%s&quot;}"`+
					` hx-target="body"`+
					` hx-swap="none"`+
					` onclick="closeWorkspaceDrawer()"`+
					` class="w-full flex items-center gap-3 rounded-xl border border-zinc-200/60 dark:border-white/[0.08] bg-zinc-50/60 dark:bg-white/[0.04] px-4 py-3 text-left text-sm font-medium text-zinc-700 dark:text-white/70 hover:bg-zinc-100/80 dark:hover:bg-white/[0.08] active:scale-95 transition-all"`+
					`%s>`+
					`<i data-lucide="layout-grid" class="w-4 h-4 shrink-0" style="color:rgb(%s)"></i><span class="min-w-0 flex-1">`+
					`<span class="block truncate">%s</span>`+
					`<span class="block text-[11px] text-zinc-500">%s · tema %s</span></span>`+
					`</button>`,
				template.HTMLEscapeString(ws.ID), active, theme.AccentRGB, template.HTMLEscapeString(ws.Name), typeLabel, strings.ToLower(template.HTMLEscapeString(theme.Nome)),
			)
		}
		if len(workspaces) == 0 {
			fmt.Fprintf(w, `<p class="text-xs text-zinc-500 px-2">Nenhum workspace disponível</p>`)
		}
	}))
	mux.HandleFunc("/admin/backups", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		h := newConfigHandler(ctx)
		h.HandleConfiguracoesSection(w, r, "admin-backups")
	}))
	mux.HandleFunc("/admin/backups/importar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		h := newConfigHandler(ctx)
		backupImportRateLimitKey := userIPRateLimitKey("admin_backup_import", ctx, r)
		if decision := rateLimiter.Allow(backupImportRateLimitKey, adminBackupImportRateLimitPolicy, time.Now()); !decision.Allowed {
			writeAdminBackupImportRateLimitExceeded(w, r, h, decision)
			slog.Warn("rate_limit_blocked",
				"family", "admin_backup_import",
				"key_hash", rateLimitKeyHash(backupImportRateLimitKey),
				"client_ip", clientIP(r),
				"method", r.Method,
				"path", r.URL.Path,
				"actor_user_id", ctx.UserID,
				"retry_after", retryAfterSeconds(decision.RetryAfter),
			)
			return
		}
		slog.Info("admin_action", "action", "backup_import_attempt", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		r.Body = http.MaxBytesReader(w, r.Body, adminBackupImportMaxBodyBytes)
		if err := r.ParseMultipartForm(adminBackupImportMaxBodyBytes); err != nil {
			slog.Warn("admin_action", "action", "backup_import_validation_failed", "actor_user_id", ctx.UserID, "client_ip", clientIP(r), "reason", "multipart_parse")
			h.HandleConfiguracoesSection(w, r, "admin-backups")
			return
		}
		file, header, err := r.FormFile("backup_file")
		if err != nil {
			slog.Warn("admin_action", "action", "backup_import_validation_failed", "actor_user_id", ctx.UserID, "client_ip", clientIP(r), "reason", "missing_file")
			h.HandleConfiguracoesSection(w, r, "admin-backups")
			return
		}
		defer file.Close()
		if strings.ToLower(filepath.Ext(header.Filename)) != ".db" {
			slog.Warn("admin_action", "action", "backup_import_validation_failed", "actor_user_id", ctx.UserID, "client_ip", clientIP(r), "reason", "invalid_extension")
			h.HandleConfiguracoesSection(w, r, "admin-backups")
			return
		}

		tmpPath, cleanup, err := saveAdminBackupImportUpload(dbFilePath, file)
		if err != nil {
			slog.Warn("admin_action", "action", "backup_import_validation_failed", "actor_user_id", ctx.UserID, "client_ip", clientIP(r), "reason", "upload_save_failed")
			h.HandleConfiguracoesSection(w, r, "admin-backups")
			return
		}
		defer cleanup()

		if err := validateSQLiteBackupCandidate(tmpPath); err != nil {
			slog.Warn("admin_action", "action", "backup_import_validation_failed", "actor_user_id", ctx.UserID, "client_ip", clientIP(r), "reason", "sqlite_validation_failed")
			h.HandleConfiguracoesSection(w, r, "admin-backups")
			return
		}

		adminBackupRestoreMu.Lock()
		_, err = restoreAdminBackupFromFile(db, dbURL, dbFilePath, tmpPath, database.Open, refreshDeps)
		adminBackupRestoreMu.Unlock()
		if err != nil {
			slog.Warn("admin_action", "action", "backup_import_failed", "actor_user_id", ctx.UserID, "client_ip", clientIP(r), "reason", "restore_failed")
			fresh := newConfigHandler(ctx)
			fresh.RenderConfigSectionWithFlashStatus(w, "admin-backups", "Não foi possível importar o backup. O banco anterior foi preservado.", "", http.StatusInternalServerError)
			return
		}
		slog.Warn("admin_action", "action", "backup_import", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		writeAdminBackupImportSuccess(w, r)
	}))
	mux.HandleFunc("/admin/auditoria", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isGlobalAdmin(ctx) {
			logAdminAuditTampering(r, ctx)
			respondForbidden(w, r)
			return
		}
		h := newConfigHandler(ctx)
		h.AuditEventFilter = strings.TrimSpace(r.URL.Query().Get("event_type"))
		h.AuditSeverityFilter = strings.TrimSpace(r.URL.Query().Get("severity"))
		if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "auditoria-table-body" {
			h.HandleAdminAuditoriaRows(w, r)
			return
		}
		h.HandleConfiguracoesSection(w, r, "admin-auditoria")
	}))
	mux.HandleFunc("/admin/auditoria/salvar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isGlobalAdmin(ctx) {
			logAdminAuditTampering(r, ctx)
			respondForbidden(w, r)
			return
		}
		h := newConfigHandler(ctx)
		h.HandleAdminAuditoriaSave(w, r)
	}))
	mux.HandleFunc("/admin/caixinhas/ledger/reconciliar", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if !allowRateLimit(w, r, userIPRateLimitKey("admin:heavy:ledger-reconcile", ctx, r), adminHeavyReadRateLimitPolicy) {
			return
		}
		slog.Info("admin_action", "action", "admin_ledger_reconcile", "actor_user_id", ctx.UserID, "client_ip", clientIP(r))
		handleAdminBoxLedgerReconcile(w, r, db, ctx)
	}))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := db.Ping(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"unhealthy"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	go func() {
		for {
			time.Sleep(6 * time.Hour)
			sessionsDeleted, preAuthDeleted, err := authService.CleanupExpiredSessions(time.Now())
			if err != nil {
				slog.Error("session cleanup failed", "error", err)
				continue
			}
			if sessionsDeleted > 0 || preAuthDeleted > 0 {
				slog.Info("session cleanup completed", "sessions_deleted", sessionsDeleted, "pre_auth_deleted", preAuthDeleted)
			}
		}
	}()
	authService.CleanupExpiredSessions(time.Now())

	slog.Info("ContaBase rodando", "url", fmt.Sprintf("http://localhost:%s", port))
	serverHandler := securityAudit.Middleware(securityHeaders(enforceRemoteHTTPS(mux)))
	if err := http.ListenAndServe(":"+port, serverHandler); err != nil {
		slog.Error("server failed", "error", err)
		log.Fatalf("server failed: %v", err)
	}
}

type authContext struct {
	UserID            string
	ActiveWorkspaceID string
	Role              string
	Member            models.WorkspaceMember
	SessionToken      string
}

func withAuth(authService *auth.Service, csrfSigner *csrfSigner, next func(http.ResponseWriter, *http.Request, authContext)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSpace(r.URL.Path)
		if isBootstrapPublicPath(path) {
			next(w, r, authContext{})
			return
		}
		bootstrapMode, err := isDatabaseBootstrapMode(authService.DB)
		if err != nil {
			slog.Error("bootstrap mode check failed", "error", err, "path", r.URL.Path)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if bootstrapMode {
			redirectToSetup(w, r)
			return
		}
		if isAuthenticationPipelinePath(r.URL.Path) {
			next(w, r, authContext{})
			return
		}
		slog.Debug("withAuth request start", "method", r.Method, "path", r.URL.Path, "hx", r.Header.Get("HX-Request") == "true")
		csrfSigner.ensureCookie(w, r)
		if isMutationMethod(r.Method) && !csrfRequestValid(csrfSigner, r) {
			slog.Error("csrf validation failed", "method", r.Method, "path", r.URL.Path)
			http.Error(w, "csrf token invalido", http.StatusForbidden)
			return
		}
		ctx, ok := resolveAuthContext(w, r, authService)
		if !ok {
			slog.Debug("withAuth unauthorized request", "method", r.Method, "path", r.URL.Path)
			respondUnauthorized(w, r)
			return
		}
		slog.Debug("withAuth context resolved", "user_id", ctx.UserID, "workspace_id", ctx.ActiveWorkspaceID, "role", ctx.Role)

		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")

		r = r.WithContext(context.WithValue(r.Context(), authContextKey, ctx))
		if !temporaryPasswordPathAllowed(r.URL.Path) {
			state, err := authService.TemporaryPasswordState(ctx.UserID, time.Now())
			if err != nil {
				slog.Error("temporary password state check failed", "error", err, "user_id", ctx.UserID)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if state.Required {
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("HX-Redirect", "/trocar-senha")
					w.WriteHeader(http.StatusOK)
					return
				}
				http.Redirect(w, r, "/trocar-senha", http.StatusSeeOther)
				return
			}
		}
		next(w, r, ctx)
	}
}

func temporaryPasswordPathAllowed(path string) bool {
	switch strings.TrimSpace(path) {
	case "/trocar-senha", "/logout":
		return true
	default:
		return false
	}
}

func resolveAuthContext(w http.ResponseWriter, r *http.Request, authService *auth.Service) (authContext, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return authContext{}, false
	}
	userID, activeWorkspaceID, newMaxAge, refreshed, err := authService.ResolveAndRefreshSession(cookie.Value)
	if err != nil {
		return authContext{}, false
	}
	if refreshed {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    cookie.Value,
			Path:     "/",
			MaxAge:   newMaxAge,
			HttpOnly: true,
			Secure:   shouldUseSecureCookie(r),
			SameSite: http.SameSiteLaxMode,
		})
	}
	member, err := authService.ResolveWorkspaceMember(userID, activeWorkspaceID)
	if err != nil {
		return authContext{}, false
	}
	if member.CustomPermissions == nil {
		member.CustomPermissions = make([]string, 0)
	}
	return authContext{
		UserID:            userID,
		ActiveWorkspaceID: activeWorkspaceID,
		Role:              member.Role,
		Member:            member,
		SessionToken:      cookie.Value,
	}, true
}

func isGlobalAdmin(ctx authContext) bool {
	return ctx.Role == models.RoleAdmin
}

func requireGlobalAdmin(w http.ResponseWriter, r *http.Request, ctx authContext) bool {
	if !isGlobalAdmin(ctx) {
		respondForbidden(w, r)
		return false
	}
	return true
}

type adminBoxLedgerIssuePayload struct {
	Code                string `json:"code"`
	BoxID               string `json:"box_id"`
	SourceTransactionID string `json:"source_transaction_id"`
	LedgerID            string `json:"ledger_id"`
	Description         string `json:"description"`
}

type adminBoxLedgerReconcilePayload struct {
	WorkspaceID string                       `json:"workspace_id"`
	IssueCount  int                          `json:"issue_count"`
	IssueCodes  []string                     `json:"issue_codes"`
	Issues      []adminBoxLedgerIssuePayload `json:"issues"`
}

func handleAdminBoxLedgerReconcile(w http.ResponseWriter, r *http.Request, db *sql.DB, ctx authContext) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if workspaceID == "" {
		workspaceID = ctx.ActiveWorkspaceID
	}
	if workspaceID == "" {
		http.Error(w, "workspace_id obrigatório", http.StatusBadRequest)
		return
	}

	var exists int
	if err := db.QueryRow(`SELECT COUNT(1) FROM workspaces WHERE id = ?`, workspaceID).Scan(&exists); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if exists == 0 {
		http.Error(w, "workspace não encontrado", http.StatusNotFound)
		return
	}

	report, err := services.ReconcileWorkspaceBoxLedger(db, workspaceID)
	if err != nil {
		http.Error(w, "erro ao reconciliar ledger", http.StatusInternalServerError)
		return
	}

	issueCodesSet := make(map[string]struct{}, len(report.Issues))
	issues := make([]adminBoxLedgerIssuePayload, 0, len(report.Issues))
	for _, issue := range report.Issues {
		if strings.TrimSpace(issue.Code) != "" {
			issueCodesSet[issue.Code] = struct{}{}
		}
		detail := strings.TrimSpace(issue.Detail)
		if detail == "" {
			detail = issue.Code
		}
		issues = append(issues, adminBoxLedgerIssuePayload{
			Code:                issue.Code,
			BoxID:               issue.BoxID,
			SourceTransactionID: issue.SourceTransaction,
			LedgerID:            issue.LedgerID,
			Description:         detail,
		})
	}

	issueCodes := make([]string, 0, len(issueCodesSet))
	for code := range issueCodesSet {
		issueCodes = append(issueCodes, code)
	}
	sort.Strings(issueCodes)

	payload := adminBoxLedgerReconcilePayload{
		WorkspaceID: workspaceID,
		IssueCount:  len(issues),
		IssueCodes:  issueCodes,
		Issues:      issues,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

func isAuthenticated(w http.ResponseWriter, r *http.Request, authService *auth.Service) bool {
	_, ok := resolveAuthContext(w, r, authService)
	return ok
}

func respondUnauthorized(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func respondForbidden(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("HX-Retarget", "#main-content")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`
<section id="main-content" class="mx-auto max-w-md px-4 pt-8 pb-28">
  <div class="rounded-3xl border border-rose-500/25 bg-rose-500/10 p-5">
    <p class="text-xs font-semibold uppercase tracking-wide text-rose-200/85">Permissão</p>
    <h2 class="mt-2 text-lg font-bold text-white">Acesso negado</h2>
    <p class="mt-2 text-sm text-rose-100/85">Seu perfil não tem permissão para executar esta ação.</p>
  </div>
</section>`))
		return
	}
	http.Error(w, "forbidden", http.StatusForbidden)
}

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func csrfRequestValid(csrfSigner *csrfSigner, r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	token := csrfSigner.tokenFromRequest(r)
	if strings.TrimSpace(token) == "" || token != cookie.Value {
		return false
	}
	return csrfSigner.validate(token, time.Now())
}

func handleLoginPage(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string) {
	handleLoginPageWithStatus(w, tpl, csrfToken, errMsg, http.StatusOK)
}

func handleLoginPageWithStatus(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string, status int) {
	data := struct {
		Error     string
		CSRFToken string
	}{
		Error:     errMsg,
		CSRFToken: csrfToken,
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := tpl.ExecuteTemplate(w, "login-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func handleLogout(w http.ResponseWriter, r *http.Request, authService *auth.Service) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = authService.RevokeSession(cookie.Value)
	}
	if preAuthCookie, err := r.Cookie(preAuthCookieName); err == nil {
		_ = authService.RevokePreAuthSession(preAuthCookie.Value)
	}
	clearAuthCookies(w, r)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Trigger", "contabase:logout")
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	clearSessionCookie(w, r)
	clearPreAuthCookie(w, r)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	expiredAt := time.Now().Add(-24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   shouldUseSecureCookie(r),
		MaxAge:   -1,
		Expires:  expiredAt,
	})
}

func isAuthenticationPipelinePath(path string) bool {
	switch strings.TrimSpace(path) {
	case "/login", "/login/2fa", "/ativar-conta":
		return true
	default:
		return false
	}
}

func handleActivationPage(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, token, errMsg string) {
	data := struct {
		Error     string
		Token     string
		CSRFToken string
	}{
		Error:     errMsg,
		Token:     token,
		CSRFToken: csrfToken,
	}
	if err := tpl.ExecuteTemplate(w, "activation-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func handleSetupPage(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string) {
	handleSetupPageWithStatus(w, tpl, csrfToken, errMsg, http.StatusOK)
}

func handleSetupCSRFError(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken string) {
	handleSetupPageWithStatus(w, tpl, csrfToken, "Sessão de segurança expirada. Recarregue /setup e tente novamente.", http.StatusForbidden)
}

func handleSetupPageWithStatus(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string, status int) {
	data := struct {
		Error     string
		CSRFToken string
	}{
		Error:     errMsg,
		CSRFToken: csrfToken,
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := tpl.ExecuteTemplate(w, "setup-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func prepareBootstrapRestoreForm(w http.ResponseWriter, r *http.Request, maxBodyBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return r.ParseMultipartForm(maxBodyBytes)
	}
	return r.ParseForm()
}

type bootstrapSetupGuard struct {
	mu       sync.Mutex
	token    string
	consumed bool
}

func newBootstrapSetupGuard(token string) *bootstrapSetupGuard {
	return &bootstrapSetupGuard{token: strings.TrimSpace(token)}
}

func (g *bootstrapSetupGuard) Allow(r *http.Request) bool {
	if g == nil {
		return false
	}
	candidate := bootstrapSetupTokenFromRequest(r)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.consumed || !bootstrapSetupTokenUsable(g.token) || candidate == "" {
		return false
	}
	return constantTimeStringEqual(candidate, g.token)
}

func (g *bootstrapSetupGuard) Consume() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.consumed = true
}

func requireBootstrapSetupToken(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, csrfToken string, guard *bootstrapSetupGuard) bool {
	if guard.Allow(r) {
		return true
	}
	handleSetupPageWithStatus(w, tpl, csrfToken, "Token local de setup inválido ou ausente.", http.StatusForbidden)
	return false
}

func bootstrapSetupTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if headerToken := strings.TrimSpace(r.Header.Get("X-ContaBase-Setup-Token")); headerToken != "" {
		return headerToken
	}
	return strings.TrimSpace(r.FormValue("setup_token"))
}

func bootstrapSetupTokenUsable(token string) bool {
	return len(strings.TrimSpace(token)) >= bootstrapSetupTokenMinLength
}

func constantTimeStringEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func setupAllowed(db *sql.DB) (bool, error) {
	bootstrapMode, err := isDatabaseBootstrapMode(db)
	if err != nil {
		return false, err
	}
	return bootstrapMode, nil
}

func redirectToSetup(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/setup")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/setup", http.StatusSeeOther)
}

func serveProfileUpload(w http.ResponseWriter, r *http.Request) {
	fileName := strings.TrimPrefix(r.URL.Path, "/uploads/profile/")
	contentType, ok := uploadedImageContentType(fileName)
	if !ok {
		http.NotFound(w, r)
		return
	}
	fullPath, err := safeServedUploadPath(paths.ProfileUploadsDir(), fileName)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	serveUploadedImage(w, r, fullPath, contentType)
}

func uploadedImageContentType(fileName string) (string, bool) {
	if !safeUploadFileName(fileName) {
		return "", false
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".png":
		return "image/png", true
	case ".webp":
		return "image/webp", true
	default:
		return "", false
	}
}

func safeUploadPathSegment(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" &&
		value != "." &&
		value != ".." &&
		!strings.Contains(value, "/") &&
		!strings.Contains(value, "\\") &&
		!strings.Contains(value, "..")
}

func safeUploadFileName(fileName string) bool {
	fileName = strings.TrimSpace(fileName)
	return safeUploadPathSegment(fileName) && filepath.Base(fileName) == fileName
}

func safeServedUploadPath(baseDir, fileName string) (string, error) {
	if !safeUploadFileName(fileName) {
		return "", fmt.Errorf("invalid upload filename")
	}
	baseAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(filepath.Join(baseDir, fileName))
	if err != nil {
		return "", err
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("upload path outside base")
	}
	return fullAbs, nil
}

func serveUploadedImage(w http.ResponseWriter, r *http.Request, fullPath, contentType string) {
	file, err := os.Open(fullPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, filepath.Base(fullPath), info.ModTime(), file)
}

func createAdminExportBackup(db *sql.DB, dbPath string) (string, func(), error) {
	tmp, err := os.CreateTemp("", "contabase-export-*.db")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, err
	}

	if err := database.CreateSQLiteBackupFile(db, dbPath, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, err
	}

	cleanup := func() {
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			log.Printf("backup export temp cleanup failed: %v", err)
		}
	}
	return tmpPath, cleanup, nil
}

func setAdminBackupDownloadHeaders(w http.ResponseWriter, now time.Time) {
	filename := fmt.Sprintf("contabase-backup-%s.db", now.Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

type openDatabaseFunc func(string) (*sql.DB, error)

type adminRestoreResult struct {
	PreRestoreBackupPath string
	RolledBack           bool
}

func saveAdminBackupImportUpload(dbPath string, src io.Reader) (string, func(), error) {
	return saveSQLiteRestoreUpload(dbPath, "contabase-import-*.db", src)
}

func saveBootstrapBackupRestoreUpload(dbPath string, src io.Reader) (string, func(), error) {
	return saveSQLiteRestoreUpload(dbPath, "contabase-bootstrap-restore-*.db", src)
}

func saveSQLiteRestoreUpload(dbPath, pattern string, src io.Reader) (string, func(), error) {
	if !isSQLiteDiskDatabasePath(dbPath) {
		return "", nil, fmt.Errorf("restore requires disk-backed sqlite database")
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			log.Printf("admin backup import temp cleanup failed: %v", err)
		}
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmpPath, cleanup, nil
}

func validateSQLiteBackupCandidate(path string) error {
	return validateContaBaseBackupCandidate(path)
}

func validateContaBaseBackupCandidate(path string) error {
	db, err := openSQLiteCandidateForValidation(path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := validateSQLiteIntegrity(db); err != nil {
		return err
	}
	for _, table := range []string{"users", "workspaces"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("missing ContaBase backup table: %s", table)
		}
	}
	return nil
}

func validateBootstrapRestoreCandidate(path string) error {
	return validateContaBaseBackupCandidate(path)
}

func openSQLiteCandidateForValidation(path string) (*sql.DB, error) {
	if !isSQLiteFile(path) {
		return nil, fmt.Errorf("invalid sqlite header")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func restoreAdminBackupFromFile(currentDB *sql.DB, dbURL, dbPath, candidatePath string, openFn openDatabaseFunc, refresh func(*sql.DB)) (adminRestoreResult, error) {
	return restoreSQLiteBackupFromFile(currentDB, dbURL, dbPath, candidatePath, openFn, refresh, validateSQLiteBackupCandidate)
}

func restoreBootstrapBackupFromFile(currentDB *sql.DB, dbURL, dbPath, candidatePath string, openFn openDatabaseFunc, refresh func(*sql.DB)) (adminRestoreResult, error) {
	return restoreSQLiteBackupFromFile(currentDB, dbURL, dbPath, candidatePath, openFn, refresh, validateBootstrapRestoreCandidate)
}

func restoreSQLiteBackupFromFile(currentDB *sql.DB, dbURL, dbPath, candidatePath string, openFn openDatabaseFunc, refresh func(*sql.DB), validateCandidate func(string) error) (adminRestoreResult, error) {
	var result adminRestoreResult
	if currentDB == nil {
		return result, fmt.Errorf("nil current database")
	}
	if openFn == nil {
		return result, fmt.Errorf("nil database opener")
	}
	if refresh == nil {
		return result, fmt.Errorf("nil refresh callback")
	}
	if validateCandidate == nil {
		return result, fmt.Errorf("nil backup candidate validator")
	}
	if !isSQLiteDiskDatabasePath(dbPath) {
		return result, fmt.Errorf("restore requires disk-backed sqlite database")
	}
	if err := validateCandidate(candidatePath); err != nil {
		return result, fmt.Errorf("invalid backup candidate: %w", err)
	}
	slog.Info("admin_backup_restore_stage", "stage", "candidate_validated")

	backupPath, err := nextAdminPreRestoreBackupPath(dbPath, time.Now())
	if err != nil {
		return result, err
	}
	if err := database.CreateSQLiteBackupFile(currentDB, dbPath, backupPath); err != nil {
		return result, fmt.Errorf("failed to create pre-restore backup: %w", err)
	}
	result.PreRestoreBackupPath = backupPath
	slog.Info("admin_backup_restore_stage", "stage", "pre_restore_backup_created")

	oldPath, err := nextAdminRestoreWorkingPath(dbPath, time.Now())
	if err != nil {
		return result, err
	}

	if err := currentDB.Close(); err != nil {
		return result, fmt.Errorf("failed to close current database: %w", err)
	}
	slog.Info("admin_backup_restore_stage", "stage", "current_db_closed")

	currentMoved := false
	if err := os.Rename(dbPath, oldPath); err != nil {
		_ = reopenAdminRestoreDatabase(dbURL, openFn, refresh)
		return result, fmt.Errorf("failed to stage current database for restore: %w", err)
	}
	currentMoved = true

	rollback := func() error {
		result.RolledBack = true
		return rollbackAdminRestore(dbURL, dbPath, oldPath, backupPath, openFn, refresh, currentMoved)
	}

	if err := removeSQLiteSidecars(dbPath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("failed to clean sqlite sidecars before restore: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("failed to clean sqlite sidecars before restore: %w", err)
	}
	if err := removeSQLiteSidecars(candidatePath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("failed to clean backup candidate sidecars: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("failed to clean backup candidate sidecars: %w", err)
	}
	if err := os.Rename(candidatePath, dbPath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("failed to install backup candidate: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("failed to install backup candidate: %w", err)
	}
	slog.Info("admin_backup_restore_stage", "stage", "candidate_installed")
	if err := ensureRestoredDatabasePermissions(dbPath); err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("failed to set restored database permissions: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("failed to set restored database permissions: %w", err)
	}

	newDB, err := openFn(dbURL)
	if err != nil {
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("failed to reopen restored database: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("failed to reopen restored database: %w", err)
	}
	slog.Info("admin_backup_restore_stage", "stage", "restored_db_reopened")
	if err := validateRestoredDatabase(newDB); err != nil {
		_ = newDB.Close()
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("restored database validation failed: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("restored database validation failed: %w", err)
	}
	if err := invalidateRestoredAuthState(newDB); err != nil {
		_ = newDB.Close()
		if rollbackErr := rollback(); rollbackErr != nil {
			return result, fmt.Errorf("restored database auth-state cleanup failed: %w; rollback failed: %v", err, rollbackErr)
		}
		return result, fmt.Errorf("restored database auth-state cleanup failed: %w", err)
	}

	refresh(newDB)
	slog.Info("admin_backup_restore_stage", "stage", "runtime_db_refreshed")
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		log.Printf("admin backup import staged database cleanup failed: %v", err)
	}
	return result, nil
}

func rollbackAdminRestore(dbURL, dbPath, oldPath, backupPath string, openFn openDatabaseFunc, refresh func(*sql.DB), currentMoved bool) error {
	slog.Warn("admin_backup_restore_stage", "stage", "rollback_started")
	_ = removeSQLiteSidecars(dbPath)
	_ = os.Remove(dbPath)

	var restoreErr error
	if currentMoved {
		if err := os.Rename(oldPath, dbPath); err != nil {
			restoreErr = err
		}
	}
	if restoreErr != nil {
		if err := copyFile(backupPath, dbPath); err != nil {
			return fmt.Errorf("failed to restore pre-restore backup: %w", err)
		}
	}
	if err := removeSQLiteSidecars(dbPath); err != nil {
		return err
	}
	if err := reopenAdminRestoreDatabase(dbURL, openFn, refresh); err != nil {
		return err
	}
	slog.Warn("admin_backup_restore_stage", "stage", "rollback_completed")
	return nil
}

func reopenAdminRestoreDatabase(dbURL string, openFn openDatabaseFunc, refresh func(*sql.DB)) error {
	db, err := openFn(dbURL)
	if err != nil {
		return err
	}
	refresh(db)
	return nil
}

func nextAdminPreRestoreBackupPath(dbPath string, now time.Time) (string, error) {
	backupDir := filepath.Join(paths.BackupsDir(), "pre-restore")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	return nextTimestampedPath(backupDir, "contabase-pre-restore", now)
}

func nextAdminRestoreWorkingPath(dbPath string, now time.Time) (string, error) {
	return nextTimestampedPath(filepath.Dir(dbPath), filepath.Base(dbPath)+".restore-current", now)
}

func nextTimestampedPath(dir, prefix string, now time.Time) (string, error) {
	timestamp := now.Format("20060102-150405")
	for i := 0; ; i++ {
		name := fmt.Sprintf("%s-%s.db", prefix, timestamp)
		if i > 0 {
			name = fmt.Sprintf("%s-%s-%02d.db", prefix, timestamp, i)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
}

func removeSQLiteSidecars(dbPath string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func ensureRestoredDatabasePermissions(dbPath string) error {
	if !isSQLiteDiskDatabasePath(dbPath) {
		return nil
	}
	return os.Chmod(dbPath, 0o600)
}

func invalidateRestoredAuthState(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE revoked_at IS NULL`); err != nil {
		return fmt.Errorf("revoke restored sessions: %w", err)
	}
	if _, err := db.Exec(`UPDATE pre_auth_sessions SET consumed_at = unixepoch() WHERE consumed_at IS NULL`); err != nil {
		return fmt.Errorf("consume restored pre-auth sessions: %w", err)
	}
	return nil
}

func isSQLiteDiskDatabasePath(dbPath string) bool {
	dbPath = strings.TrimSpace(dbPath)
	return dbPath != "" && dbPath != ":memory:"
}

func sqlitePathFromURL(dbURL string) string {
	if strings.HasPrefix(dbURL, "file:") {
		path := strings.TrimPrefix(dbURL, "file:")
		if idx := strings.Index(path, "?"); idx >= 0 {
			path = path[:idx]
		}
		return path
	}
	return dbURL
}

func normalizeWorkspaceType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case models.WorkspaceTypeBusiness:
		return models.WorkspaceTypeBusiness
	default:
		return models.WorkspaceTypePersonal
	}
}

func rawWorkspaceType(db *sql.DB, workspaceID string) string {
	var wsType string
	if err := db.QueryRow(`SELECT COALESCE(type, 'personal') FROM workspaces WHERE id = ?`, workspaceID).Scan(&wsType); err != nil {
		return models.WorkspaceTypePersonal
	}
	return strings.ToLower(strings.TrimSpace(wsType))
}

func isBootstrapPublicPath(path string) bool {
	if path == "/setup" || path == "/signup" || path == "/login" || strings.HasPrefix(path, "/setup/") || strings.HasPrefix(path, "/signup/") {
		return true
	}
	return strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/assets/")
}

func isDatabaseBootstrapMode(db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

func runInitialSetup(db *sql.DB, name, email, password, workspaceType string) (string, error) {
	now := time.Now().Unix()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	userID := uuid.NewString()
	workspaceID := uuid.NewString()
	if _, err := tx.Exec(`
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)
	`, userID, name, email, string(hash), now, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES (?, ?, 'Workspace inicial', ?, ?, ?)
	`, workspaceID, personalWorkspaceName(name), normalizeWorkspaceType(workspaceType), now, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', ?)
	`, workspaceID, userID, now); err != nil {
		return "", err
	}
	if err := seedWorkspaceFactoryDefaultsTx(tx, workspaceID, normalizeWorkspaceType(workspaceType), now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return userID, nil
}

func seedWorkspaceFactoryDefaultsTx(tx *sql.Tx, workspaceID, workspaceType string, now int64) error {
	// Categorias: delegadas ao seeder centralizado (internal/database/seeder.go)
	if err := database.SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
		return err
	}

	// Contas e cartões reais não são criados automaticamente.
	// O seeder centralizado mantém a chamada como no-op idempotente para
	// proteger instalações novas e bancos existentes.
	if err := database.SeedWorkspaceAccountsTx(tx, workspaceID, workspaceType); err != nil {
		return err
	}

	return nil
}

func userOwnsTransaction(db *sql.DB, transactionID, userID, workspaceID string) bool {
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE id = ? AND workspace_id = ? AND user_id = ?
	`, transactionID, workspaceID, userID).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func isSQLiteFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := make([]byte, 16)
	if _, err := io.ReadFull(f, header); err != nil {
		return false
	}
	return string(header) == "SQLite format 3\x00"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func validateRestoredDatabase(db *sql.DB) error {
	if err := validateSQLiteIntegrity(db); err != nil {
		return err
	}
	if err := validateSQLiteForeignKeys(db); err != nil {
		return err
	}
	for _, table := range []string{"users", "workspaces", "workspace_members", "schema_migrations"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("missing required table: %s", table)
		}
	}
	return nil
}

func validateSQLiteForeignKeys(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return err
		}
		if rowID.Valid {
			return fmt.Errorf("foreign_key_check failed: table=%s rowid=%d parent=%s fk=%d", table, rowID.Int64, parent, fkID)
		}
		return fmt.Errorf("foreign_key_check failed: table=%s rowid=NULL parent=%s fk=%d", table, parent, fkID)
	}
	return rows.Err()
}

func validateSQLiteIntegrity(db *sql.DB) error {
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(integrity)) != "ok" {
		return fmt.Errorf("integrity_check failed: %s", integrity)
	}
	return nil
}

func buildFuncMap() template.FuncMap {
	fieldValue := func(data any, name string) (reflect.Value, bool) {
		v := reflect.ValueOf(data)
		for v.IsValid() && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		if !v.IsValid() || v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		f := v.FieldByName(name)
		if !f.IsValid() {
			return reflect.Value{}, false
		}
		return f, true
	}

	funcMap := template.FuncMap{
		"appVersion": func() string {
			return version.Version
		},
		"assetPath": func(path string) string {
			fullPath := filepath.Join("assets", strings.TrimPrefix(path, "/assets/"))
			return "/" + assets.VersionedPath(fullPath)
		},
		"intRange": func(start, end int) []int {
			var result []int
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
			return result
		},
		"list": func(items ...string) []string {
			return items
		},
		"add": func(a, b int) int {
			return a + b
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"slice": func(s string, start, end int) string {
			if start < 0 || end > len(s) || start > end {
				return ""
			}
			return s[start:end]
		},
		"upper": func(s string) string {
			return strings.ToUpper(s)
		},
		"index": func(items []string, i int) string {
			if i >= 0 && i < len(items) {
				return items[i]
			}
			return ""
		},
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
		"filterCategories": func(cats []handlers.ConfigCategoryRow, typ string) []handlers.ConfigCategoryRow {
			var result []handlers.ConfigCategoryRow
			for _, c := range cats {
				if c.Type == typ {
					result = append(result, c)
				}
			}
			return result
		},
		"dreMacroValue": func(groups []handlers.DREMacroGroupAmount, macroGroup string) handlers.MoneyDisplay {
			for _, item := range groups {
				if item.MacroGroup == macroGroup {
					return item.Amount
				}
			}
			return handlers.MoneyMinor(0)
		},
		"dreMacroClass": func(groups []handlers.DREMacroGroupAmount, macroGroup string) string {
			for _, item := range groups {
				if item.MacroGroup == macroGroup {
					if item.RawAmount < 0 {
						return "text-rose-600 dark:text-rose-300"
					}
					return "text-emerald-600 dark:text-emerald-300"
				}
			}
			return "text-emerald-600 dark:text-emerald-300"
		},
		"fieldString": func(data any, name string) string {
			f, ok := fieldValue(data, name)
			if !ok || f.Kind() != reflect.String {
				return ""
			}
			return f.String()
		},
		"userDisplayName": func(data any) string {
			if f, ok := fieldValue(data, "UserFirstName"); ok && f.Kind() == reflect.String {
				if s := f.String(); s != "" {
					return s
				}
			}
			if f, ok := fieldValue(data, "UserName"); ok && f.Kind() == reflect.String {
				if s := f.String(); s != "" {
					parts := strings.Fields(s)
					if len(parts) > 0 {
						return parts[0]
					}
				}
			}
			if f, ok := fieldValue(data, "UserInitials"); ok && f.Kind() == reflect.String {
				if s := f.String(); s != "" {
					return s
				}
			}
			return "Usuário"
		},
		"fieldBool": func(data any, name string) bool {
			f, ok := fieldValue(data, name)
			if !ok || f.Kind() != reflect.Bool {
				return false
			}
			return f.Bool()
		},
		"fieldInt": func(data any, name string) int {
			f, ok := fieldValue(data, name)
			if !ok {
				return 0
			}
			switch f.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				return int(f.Int())
			default:
				return 0
			}
		},
		"SetupTokenStillConfigured": func(data any) bool {
			return shouldShowSetupTokenNotice(os.Getenv(bootstrapSetupTokenEnv), data)
		},
	}

	return funcMap
}

func writeAdminBackupImportRateLimitExceeded(w http.ResponseWriter, _ *http.Request, h handlers.ConfiguracoesHandler, decision rateLimitDecision) {
	retryAfter := retryAfterSeconds(decision.RetryAfter)
	message := adminBackupImportRateLimitMessage(retryAfter)
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"mostrarAlerta":{"value":%q}}`, message))
	h.RenderConfigSectionWithFlashStatus(w, "admin-backups", message, "", http.StatusTooManyRequests)
}

func writeAdminBackupImportSuccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.Header().Set("HX-Trigger", `{"mostrarSucesso":{"value":"Backup importado com sucesso. Faça login novamente para continuar."}}`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "Backup importado com sucesso. Faça login novamente para continuar.")
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func adminBackupImportRateLimitMessage(retryAfterSeconds int) string {
	if retryAfterSeconds <= 60 {
		return "Muitas tentativas de importação. Aguarde 1 minuto e tente novamente."
	}
	minutes := (retryAfterSeconds + 59) / 60
	return fmt.Sprintf("Muitas tentativas de importação. Aguarde %d minutos e tente novamente.", minutes)
}

func shouldShowSetupTokenNotice(token string, data any) bool {
	return bootstrapSetupTokenConfigured(token) && templateActorIsAdmin(data)
}

func bootstrapSetupTokenConfigured(token string) bool {
	return strings.TrimSpace(token) != ""
}

func templateActorIsAdmin(data any) bool {
	for _, field := range []string{"ActorRole", "UserRole"} {
		role := templateStringField(data, field)
		if setupNoticeRoleIsAdmin(role) {
			return true
		}
	}
	return false
}

func templateStringField(data any, name string) string {
	v := reflect.ValueOf(data)
	for v.IsValid() && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName(name)
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

func setupNoticeRoleIsAdmin(role string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(role))
	switch normalized {
	case models.RoleAdmin, "ADMINISTRADOR", "ADMINISTRATOR", "OWNER", "DONO", "PROPRIETARIO", "PROPRIETÁRIO":
		return true
	default:
		return false
	}
}

func seedIfEmpty(db *sql.DB) {
	repairLegacySeedCredentials(db)
	var userCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&userCount); err != nil {
		slog.Error("seed skipped: failed to count users", "error", err)
		return
	}
	if userCount != 0 {
		slog.Info("seed skipped: users table is not empty", "user_count", userCount)
		return
	}
	slog.Info("seed skipped: bootstrap mode enabled with empty database")
}

func seedDemoB2B(db *sql.DB, workspaceID, userID string) error {
	var wsType string
	if err := db.QueryRow(`SELECT COALESCE(type, 'personal') FROM workspaces WHERE id = ?`, workspaceID).Scan(&wsType); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("workspace ativo não encontrado")
		}
		return err
	}
	if wsType != "business" {
		return fmt.Errorf("a seed demo B2B só pode ser executada em workspace empresarial")
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var accountID string
	if err := tx.QueryRow(`
		SELECT id
		FROM accounts
		WHERE workspace_id = ? AND type IN ('CHECKING', 'SAVINGS', 'INVESTMENT')
		ORDER BY created_at ASC
		LIMIT 1
	`, workspaceID).Scan(&accountID); err != nil {
		if err != sql.ErrNoRows {
			return err
		}
		accountID = seedStableID(workspaceID, "demo-b2b-account-main")
		now := time.Now().Unix()
		if _, err := tx.Exec(`
			INSERT INTO accounts (id, workspace_id, name, type, color, provider_slug, initial_balance, current_balance, created_at, updated_at)
			VALUES (?, ?, ?, 'CHECKING', '#EC7000', 'itau', 0, 0, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				updated_at = excluded.updated_at
		`, accountID, workspaceID, "Conta Operacional B2B", now, now); err != nil {
			return err
		}
	}

	type demoCategory struct {
		Key   string
		Name  string
		Type  string
		Macro string
	}
	categories := []demoCategory{
		{Key: "cat-operating-revenue", Name: "Receita de Prestação de Serviços", Type: "INCOME", Macro: "Receitas Operacionais"},
		{Key: "cat-tax-deductions", Name: "Tributos sobre Receita", Type: "EXPENSE", Macro: "Deduções/Impostos"},
		{Key: "cat-operating-costs", Name: "Custos de Produção", Type: "EXPENSE", Macro: "Custos Operacionais"},
		{Key: "cat-admin-expenses", Name: "Despesas Administrativas", Type: "EXPENSE", Macro: "Despesas Administrativas"},
		{Key: "cat-sales-expenses", Name: "Despesas Comerciais", Type: "EXPENSE", Macro: "Despesas Comerciais"},
		{Key: "cat-investments-other", Name: "Investimentos e Outros", Type: "EXPENSE", Macro: "Investimentos/Outros"},
	}
	categoryIDs := make(map[string]string, len(categories))
	now := time.Now().Unix()
	for _, item := range categories {
		id := seedStableID(workspaceID, item.Key)
		categoryIDs[item.Key] = id
		if _, err := tx.Exec(`
			INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
			VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, NULL, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				type = excluded.type,
				macro_group = excluded.macro_group
		`, id, workspaceID, item.Name, item.Type, item.Macro, now); err != nil {
			return err
		}
	}

	type demoContact struct {
		Key      string
		Name     string
		Document string
		Type     string
		Email    string
		Phone    string
	}
	contacts := []demoContact{
		{Key: "client-alvorada", Name: "Alvorada Construções Ltda", Document: "12.345.678/0001-90", Type: "client", Email: "financeiro@alvorada.com.br", Phone: "(11) 98888-1101"},
		{Key: "client-orion", Name: "Orion Logística S/A", Document: "23.456.789/0001-01", Type: "client", Email: "contas@orionlog.com.br", Phone: "(11) 97777-2202"},
		{Key: "client-solare", Name: "Solare Tecnologia Ltda", Document: "34.567.890/0001-12", Type: "client", Email: "pagamentos@solaretech.com.br", Phone: "(11) 96666-3303"},
		{Key: "vendor-fenix", Name: "Fênix Insumos Industriais", Document: "45.678.901/0001-23", Type: "vendor", Email: "vendas@fenixinsumos.com.br", Phone: "(11) 95555-4404"},
		{Key: "vendor-norte", Name: "Norte Serviços Administrativos", Document: "56.789.012/0001-34", Type: "vendor", Email: "comercial@norteserv.com.br", Phone: "(11) 94444-5505"},
		{Key: "vendor-metro", Name: "Metro Marketing Digital", Document: "67.890.123/0001-45", Type: "vendor", Email: "atendimento@metromkt.com.br", Phone: "(11) 93333-6606"},
	}
	contactIDs := make(map[string]string, len(contacts))
	for _, item := range contacts {
		id := seedStableID(workspaceID, item.Key)
		contactIDs[item.Key] = id
		if _, err := tx.Exec(`
			INSERT INTO contacts (id, workspace_id, name, document, type, email, phone, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				document = excluded.document,
				type = excluded.type,
				email = excluded.email,
				phone = excluded.phone
		`, id, workspaceID, item.Name, item.Document, item.Type, item.Email, item.Phone, now); err != nil {
			return err
		}
	}

	type demoTransaction struct {
		Key         string
		CategoryKey string
		ContactKey  string
		Type        string
		Amount      int64
		Date        string
		DueDate     string
		Status      string
		Payment     string
		Description string
	}
	transactions := []demoTransaction{
		{Key: "tx-2026-04-income-paid-1", CategoryKey: "cat-operating-revenue", ContactKey: "client-alvorada", Type: "INCOME", Amount: 1850000, Date: "2026-04-05", Status: "paid", Payment: "PAID", Description: "NF 2001 - Projeto de consultoria mensal"},
		{Key: "tx-2026-04-income-overdue-1", CategoryKey: "cat-operating-revenue", ContactKey: "client-orion", Type: "INCOME", Amount: 1320000, Date: "2026-04-12", DueDate: "2026-04-20", Status: "pending", Payment: "PENDING", Description: "NF 2004 - Implantação de módulo logístico"},
		{Key: "tx-2026-04-expense-admin", CategoryKey: "cat-admin-expenses", ContactKey: "vendor-norte", Type: "EXPENSE", Amount: 420000, Date: "2026-04-10", Status: "paid", Payment: "PAID", Description: "Serviços administrativos terceirizados"},
		{Key: "tx-2026-04-expense-cost", CategoryKey: "cat-operating-costs", ContactKey: "vendor-fenix", Type: "EXPENSE", Amount: 690000, Date: "2026-04-15", Status: "paid", Payment: "PAID", Description: "Compra de insumos para operação"},
		{Key: "tx-2026-05-income-paid-1", CategoryKey: "cat-operating-revenue", ContactKey: "client-solare", Type: "INCOME", Amount: 2140000, Date: "2026-05-07", Status: "paid", Payment: "PAID", Description: "NF 2044 - Sprint de desenvolvimento"},
		{Key: "tx-2026-05-income-pending-1", CategoryKey: "cat-operating-revenue", ContactKey: "client-alvorada", Type: "INCOME", Amount: 980000, Date: "2026-05-21", DueDate: "2026-05-30", Status: "pending", Payment: "PENDING", Description: "NF 2051 - Ajustes de integração"},
		{Key: "tx-2026-05-expense-sales", CategoryKey: "cat-sales-expenses", ContactKey: "vendor-metro", Type: "EXPENSE", Amount: 310000, Date: "2026-05-11", Status: "paid", Payment: "PAID", Description: "Campanha de geração de leads B2B"},
		{Key: "tx-2026-05-expense-tax", CategoryKey: "cat-tax-deductions", ContactKey: "vendor-norte", Type: "EXPENSE", Amount: 275000, Date: "2026-05-27", Status: "paid", Payment: "PAID", Description: "Recolhimento de impostos sobre faturamento"},
		{Key: "tx-2026-06-income-paid-1", CategoryKey: "cat-operating-revenue", ContactKey: "client-orion", Type: "INCOME", Amount: 2390000, Date: "2026-06-06", Status: "paid", Payment: "PAID", Description: "NF 2103 - Renovação de contrato anual"},
		{Key: "tx-2026-06-income-overdue-1", CategoryKey: "cat-operating-revenue", ContactKey: "client-solare", Type: "INCOME", Amount: 1210000, Date: "2026-06-18", DueDate: "2026-06-25", Status: "pending", Payment: "PENDING", Description: "NF 2112 - Treinamento especializado"},
		{Key: "tx-2026-06-expense-cost", CategoryKey: "cat-operating-costs", ContactKey: "vendor-fenix", Type: "EXPENSE", Amount: 760000, Date: "2026-06-09", Status: "paid", Payment: "PAID", Description: "Aquisição de materiais de produção"},
		{Key: "tx-2026-06-expense-invest", CategoryKey: "cat-investments-other", ContactKey: "vendor-metro", Type: "EXPENSE", Amount: 490000, Date: "2026-06-22", Status: "pending", Payment: "PENDING", Description: "Investimento em infraestrutura de automação"},
	}

	for _, item := range transactions {
		txID := seedStableID(workspaceID, item.Key)
		dateUnix, err := parseSeedDateStrict(item.Date)
		if err != nil {
			return err
		}
		var dueDate any
		if strings.TrimSpace(item.DueDate) != "" {
			dueUnix, err := parseSeedDateStrict(item.DueDate)
			if err != nil {
				return err
			}
			dueDate = dueUnix
		}
		if _, err := tx.Exec(`
			INSERT INTO transactions (
				id, workspace_id, user_id, account_id, destination_account_id, category_id, invoice_id, invoice_override_id,
				type, amount, date, description, notes, attachment_path, installment_number, total_installments, parent_id,
				recurring_rule_id, recurrence_sequence, payment_status, status, due_date, contact_id, created_at, updated_at
			)
			VALUES (?, ?, ?, ?, NULL, ?, NULL, NULL, ?, ?, ?, ?, '', '', 1, 1, NULL, NULL, NULL, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				category_id = excluded.category_id,
				type = excluded.type,
				amount = excluded.amount,
				date = excluded.date,
				description = excluded.description,
				payment_status = excluded.payment_status,
				status = excluded.status,
				due_date = excluded.due_date,
				contact_id = excluded.contact_id,
				updated_at = excluded.updated_at
		`, txID, workspaceID, userID, accountID, categoryIDs[item.CategoryKey], item.Type, item.Amount, dateUnix, item.Description, item.Payment, item.Status, dueDate, contactIDs[item.ContactKey], now, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func parseSeedDateStrict(value string) (int64, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("data inválida na seed demo B2B: %s", value)
	}
	return t.Unix(), nil
}

func seedStableID(workspaceID, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("financeiro:"+workspaceID+":"+key)).String()
}

func configureLogger() {
	level := slog.LevelInfo
	debugEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv("APP_DEBUG")), "true")
	if debugEnabled {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	slog.Info("logger initialized", "app_debug", debugEnabled, "level", level.String())
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
}

func personalWorkspaceName(userName string) string {
	firstName := strings.TrimSpace(userName)
	if fields := strings.Fields(firstName); len(fields) > 0 {
		firstName = fields[0]
	}
	if firstName == "" {
		firstName = "Usuário"
	}
	return "Espaço " + firstName
}

type seedCategoryTreeNode struct {
	Name     string
	Type     string
	Macro    string
	Children []string
}

func seedWorkspaceDefaultCategoryTree(db *sql.DB, workspaceID, workspaceType string, now int64) error {
	personal := []seedCategoryTreeNode{
		{Name: "Moradia", Type: models.CategoryTypeExpense, Macro: "Moradia", Children: []string{"Aluguel/Financiamento", "Condomínio", "Energia Elétrica", "Internet/TV"}},
		{Name: "Alimentação", Type: models.CategoryTypeExpense, Macro: "Alimentação", Children: []string{"Supermercado", "Restaurantes/Delivery", "Feira/Padaria"}},
		{Name: "Transporte", Type: models.CategoryTypeExpense, Macro: "Transporte", Children: []string{"Combustível", "Uber/99", "Manutenção/Seguro"}},
		{Name: "Lazer & Estilo de Vida", Type: models.CategoryTypeExpense, Macro: "Lazer & Estilo de Vida", Children: []string{"Cinema/Streaming", "Viagens", "Academias/Esportes"}},
	}
	business := []seedCategoryTreeNode{
		{Name: "Receitas Operacionais", Type: models.CategoryTypeIncome, Macro: "Receitas Operacionais", Children: []string{"Venda de Produtos", "Prestação de Serviços", "Receita de Licenças/SaaS"}},
		{Name: "Custos Operacionais (CMV/CPV)", Type: models.CategoryTypeExpense, Macro: "Custos Operacionais (CMV/CPV)", Children: []string{"Matéria-Prima/Insumos", "Fretes e Logística", "Ferramentas de Produção"}},
		{Name: "Despesas Administrativas", Type: models.CategoryTypeExpense, Macro: "Despesas Administrativas", Children: []string{"Aluguel do Escritório", "Softwares e Assinaturas (SaaS)", "Material de Escritório/Copa"}},
		{Name: "Pessoal & Recursos Humanos", Type: models.CategoryTypeExpense, Macro: "Pessoal & Recursos Humanos", Children: []string{"Salários/Folha", "Pró-labore dos Sócios", "Benefícios (VR/VA)", "Encargos Sociais"}},
	}

	tree := personal
	if workspaceType == models.WorkspaceTypeBusiness {
		tree = business
	}

	for _, parent := range tree {
		parentID := uuid.NewString()
		if _, err := db.Exec(`
			INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
			VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, NULL, ?)
		`, parentID, workspaceID, parent.Name, parent.Type, parent.Macro, now); err != nil {
			return err
		}
		for _, child := range parent.Children {
			if _, err := db.Exec(`
				INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
				VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, ?, ?)
			`, uuid.NewString(), workspaceID, child, parent.Type, parent.Macro, parentID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func repairLegacySeedCredentials(db *sql.DB) {
	hash, err := bcrypt.GenerateFromPassword([]byte(seedDefaultPassword), bcrypt.DefaultCost)
	if err != nil {
		log.Printf("legacy seed repair skipped: %v", err)
		return
	}
	now := time.Now().Unix()
	_, err = db.Exec(`
		UPDATE users
		SET password_hash = ?, status = 'active', activation_token_hash = NULL, activation_expires_at = NULL, updated_at = ?
		WHERE id = ?
		  AND (
			trim(password_hash) = ''
			OR lower(password_hash) = 'pending'
			OR lower(password_hash) = '$2a$10$placeholder'
			OR status = 'pending'
		  )
	`, string(hash), now, seedUserID)
	if err != nil {
		log.Printf("legacy seed repair failed: %v", err)
	}
}

func seedCostLimits(db *sql.DB, workspaceID string, catIDs map[string]string, now int64) {
	type limit struct {
		catName string
		amount  int64
	}
	for _, l := range []limit{
		{"Mercado", 150000},
		{"iFood", 80000},
	} {
		id := uuid.NewString()
		db.Exec(`INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES (?, ?, ?, ?)`,
			id, workspaceID, catIDs[l.catName], l.amount)
	}
}

func seedBoxes(db *sql.DB, workspaceID string, catIDs map[string]string, now int64) {
	type box struct {
		name, desc      string
		catName         string
		target, monthly int64
	}
	boxes := []box{
		{"Carro", "Trocar de carro até o fim do ano", "Carro", 4500000, 200000},
		{"Viagem", "Viagem para Europa em 2027", "Viagem", 1200000, 100000},
		{"Primeiro Milhão", "Independência financeira", "Primeiro Milhão", 100000000, 1000000},
	}

	for _, b := range boxes {
		boxID := uuid.NewString()
		db.Exec(`INSERT INTO boxes (id, workspace_id, name, description, category_id, target_amount, monthly_recharge, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			boxID, workspaceID, b.name, b.desc, catIDs[b.catName], b.target, b.monthly, now, now)

		seedBoxLedger(db, boxID, b.name, b.monthly, now)
	}
}

func seedBoxLedger(db *sql.DB, boxID, boxName string, monthly, now int64) {
	type ledger struct {
		amount      int64
		kind        string
		description string
		dateStr     string
	}

	entriesByBox := map[string][]ledger{
		"Carro": {
			{monthly, "RECHARGE", "Recarga mensal de janeiro", "2026-01-10"},
			{monthly, "RECHARGE", "Recarga mensal de fevereiro", "2026-02-10"},
			{monthly, "RECHARGE", "Recarga mensal de março", "2026-03-10"},
			{500000, "BONUS", "Venda de acessório antigo", "2026-04-08"},
			{monthly, "RECHARGE", "Recarga mensal de maio", "2026-05-10"},
		},
		"Viagem": {
			{monthly, "RECHARGE", "Recarga mensal de janeiro", "2026-01-05"},
			{monthly, "RECHARGE", "Recarga mensal de fevereiro", "2026-02-05"},
			{300000, "BONUS", "Aporte de férias", "2026-03-15"},
			{monthly, "RECHARGE", "Recarga mensal de abril", "2026-04-05"},
			{monthly, "RECHARGE", "Recarga mensal de maio", "2026-05-05"},
		},
		"Primeiro Milhão": {
			{1000000, "RECHARGE", "Aporte mensal de janeiro", "2026-01-20"},
			{1000000, "RECHARGE", "Aporte mensal de fevereiro", "2026-02-20"},
			{1000000, "RECHARGE", "Aporte mensal de março", "2026-03-20"},
			{5000000, "BONUS", "Bônus anual investido", "2026-04-20"},
			{1000000, "RECHARGE", "Aporte mensal de maio", "2026-05-20"},
		},
	}

	for _, entry := range entriesByBox[boxName] {
		db.Exec(`INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), boxID, entry.amount, entry.kind, entry.description, parseSeedDate(entry.dateStr), now)
	}
}

func seedCreditCardScenarios(db *sql.DB, workspaceID, userID string, accountIDs, catIDs map[string]string, now int64) {
	seedInvoice(db, "Cartão Nubank", accountIDs["Cartão Nubank"], "2026-05", "OPEN", "2026-05-28", "2026-06-05", 0, 0, now)
	seedInvoiceTransaction(db, workspaceID, userID, accountIDs["Cartão Nubank"], catIDs["iFood"], "2026-05", "2026-05-03", "iFood - Almoço de domingo", 12000, now)
	seedInvoiceTransaction(db, workspaceID, userID, accountIDs["Cartão Nubank"], catIDs["Transporte"], "2026-05", "2026-05-08", "Posto Shell - Gasolina", 25000, now)
	seedInvoiceTransaction(db, workspaceID, userID, accountIDs["Cartão Nubank"], catIDs["Assinaturas"], "2026-05", "2026-05-16", "Streaming e aplicativos", 8990, now)

	seedInvoice(db, "Cartão Itaú", accountIDs["Cartão Itaú"], "2026-04", "CLOSED", "2026-04-05", "2026-04-12", 0, 0, now)
	seedInvoiceTransaction(db, workspaceID, userID, accountIDs["Cartão Itaú"], catIDs["Lazer"], "2026-04", "2026-04-01", "Hotel fim de semana", 180000, now)
	seedInvoiceTransaction(db, workspaceID, userID, accountIDs["Cartão Itaú"], catIDs["Mercado"], "2026-04", "2026-04-03", "Mercado mensal", 95000, now)
	seedInvoiceTransaction(db, workspaceID, userID, accountIDs["Cartão Itaú"], catIDs["Educação"], "2026-04", "2026-04-04", "Curso online", 70000, now)
}

func seedInvoice(db *sql.DB, cardName, accountID, reference, status, closingDate, dueDate string, paidAt, paidAmount, now int64) {
	if accountID == "" {
		log.Printf("seed invoice skipped: missing account for %s", cardName)
		return
	}

	var paidAtValue interface{}
	var paidAmountValue interface{}
	if status == "PAID" {
		paidAtValue = paidAt
		paidAmountValue = paidAmount
	}

	db.Exec(`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), accountID, reference, parseSeedDate(closingDate), parseSeedDate(dueDate), status, paidAtValue, paidAmountValue, now)
}

func seedInvoiceTransaction(db *sql.DB, workspaceID, userID, accountID, categoryID, reference, dateStr, description string, amount, now int64) {
	if accountID == "" {
		log.Printf("seed invoice transaction skipped: missing account for %s", description)
		return
	}

	var invoiceID string
	if err := db.QueryRow(`SELECT id FROM invoices WHERE account_id = ? AND reference = ?`, accountID, reference).Scan(&invoiceID); err != nil {
		log.Printf("seed invoice transaction skipped: invoice %s not found for %s: %v", reference, description, err)
		return
	}

	db.Exec(`INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'EXPENSE', ?, ?, ?, 'paid', 1, 1, ?, ?)`,
		uuid.NewString(), workspaceID, userID, accountID, categoryID, invoiceID, amount, parseSeedDate(dateStr), description, now, now)
}

func seedTestTransactions(db *sql.DB, workspaceID, userID string, accountIDs, catIDs map[string]string, now int64) {
	acctID := accountIDs["Conta Corrente Principal"]
	if acctID == "" {
		for _, v := range accountIDs {
			acctID = v
			break
		}
	}

	type testTx struct {
		catName string
		amount  int64
		dateStr string
		txType  string
	}
	for _, tx := range []testTx{
		{"Salário", 850000, "2026-04-05", "INCOME"},
		{"Aluguel", 220000, "2026-04-07", "EXPENSE"},
		{"Mercado", 86000, "2026-04-12", "EXPENSE"},
		{"Mercado", 63000, "2026-04-26", "EXPENSE"},
		{"Lazer", 78000, "2026-04-19", "EXPENSE"},
		{"Salário", 850000, "2026-05-05", "INCOME"},
		{"Aluguel", 220000, "2026-05-07", "EXPENSE"},
		{"Mercado", 91000, "2026-05-12", "EXPENSE"},
		{"Mercado", 54000, "2026-05-26", "EXPENSE"},
		{"Lazer", 69000, "2026-05-18", "EXPENSE"},
		{"Salário", 850000, "2026-06-05", "INCOME"},
		{"Aluguel", 220000, "2026-06-07", "EXPENSE"},
		{"Mercado", 93000, "2026-06-11", "EXPENSE"},
		{"Mercado", 58000, "2026-06-24", "EXPENSE"},
		{"Lazer", 74000, "2026-06-21", "EXPENSE"},
	} {
		id := uuid.NewString()
		txDate := parseSeedDate(tx.dateStr)
		catID := catIDs[tx.catName]
		var catIDVal interface{}
		if tx.catName != "" && catID != "" {
			catIDVal = catID
		}
		db.Exec(`INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, workspaceID, userID, acctID, catIDVal, tx.txType, tx.amount, txDate, fmt.Sprintf("%s: %s", tx.txType, tx.catName), 1, 1, now, now)
	}
}

func parseSeedDate(s string) int64 {
	t, _ := time.Parse("2006-01-02", s)
	return t.Unix()
}
