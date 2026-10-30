package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/security"
)

// ADM2 — cadeia de rota de /admin/auditoria/salvar replicando o registro real
// (cmd/server/main.go): withAuth (CSRF) → método → isGlobalAdmin →
// requireRecentAuthentication → rate limit (20/10min) → handler.
func newAdminAuditoriaSaveRouteTestHandler(t *testing.T, db *sql.DB, authService *auth.Service, signer *csrfSigner) http.Handler {
	t.Helper()
	tpl, err := newAppTemplateEngine(false, buildFuncMap())
	if err != nil {
		t.Fatalf("load templates: %v", err)
	}
	limiter := newMemoryRateLimiter()
	// Mesma política de adminIdentityMutationRateLimitPolicy (main.go:114).
	policy := rateLimitPolicy{Limit: 20, Window: 10 * time.Minute}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/auditoria/salvar", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isGlobalAdmin(ctx) {
			respondForbidden(w, r)
			return
		}
		if !requireRecentAuthentication(w, r, authService, ctx, "/admin/auditoria") {
			return
		}
		key := "admin:identity:auditoria-save:user:" + ctx.UserID + ":ip:" + clientIP(r)
		if !allowRateLimitedRequest(w, r, limiter, key, policy, time.Now()) {
			return
		}
		h := handlers.ConfiguracoesHandler{
			DB:                  db,
			Templates:           tpl,
			AutomaticBackupsDir: t.TempDir(),
			WorkspaceID:         ctx.ActiveWorkspaceID,
			UserID:              ctx.UserID,
			ActorRole:           ctx.Role,
			SessionToken:        ctx.SessionToken,
		}
		h.HandleAdminAuditoriaSave(w, r)
	}))
	return mux
}

func adm2RoutePost(handler http.Handler, token string, form url.Values, withCSRF bool, signer *csrfSigner) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/auditoria/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	if withCSRF {
		token := signer.issue(time.Now())
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
		req.Header.Set(csrfHeaderName, token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestAdminAuditoriaSaveRouteRoleMatrix(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	authService := auth.NewService(db)
	signer := newAdm2TestSigner()
	handler := newAdminAuditoriaSaveRouteTestHandler(t, db, authService, signer)

	sessions := map[string]string{
		models.RoleAdmin:   createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin),
		models.RoleManager: createAdminUsersRBACSession(t, authService, "manager-user", models.RoleManager),
		models.RoleUser:    createAdminUsersRBACSession(t, authService, "regular-user", models.RoleUser),
	}
	cases := []struct {
		role string
		want int
	}{
		{models.RoleAdmin, http.StatusOK},
		{models.RoleManager, http.StatusForbidden},
		{models.RoleUser, http.StatusForbidden},
	}
	form := url.Values{"smtp_host": {"smtp.rota.exemplo"}, "smtp_port": {"587"}}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			rr := adm2RoutePost(handler, sessions[tc.role], form, true, signer)
			if rr.Code != tc.want {
				t.Fatalf("role %s: status = %d, want %d, body = %q", tc.role, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestAdminAuditoriaSaveRouteRejectsInvalidCSRF(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	authService := auth.NewService(db)
	signer := newAdm2TestSigner()
	handler := newAdminAuditoriaSaveRouteTestHandler(t, db, authService, signer)
	token := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)

	rr := adm2RoutePost(handler, token, url.Values{"smtp_host": {"smtp.csrf.exemplo"}}, false, signer)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST sem CSRF deve receber 403, recebeu %d (body=%q)", rr.Code, rr.Body.String())
	}
	var host string
	if err := db.QueryRow(`SELECT COALESCE(smtp_host, '') FROM workspaces WHERE id = 'workspace-rbac'`).Scan(&host); err != nil || host != "" {
		t.Fatalf("POST sem CSRF não pode gravar: host=%q err=%v", host, err)
	}
}

func TestAdminAuditoriaSaveRouteRedirectsStaleReauthentication(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	authService := auth.NewService(db)
	signer := newAdm2TestSigner()
	handler := newAdminAuditoriaSaveRouteTestHandler(t, db, authService, signer)
	token := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)
	if _, err := db.Exec(`UPDATE sessions SET reauthenticated_at = 0 WHERE user_id = 'admin-user'`); err != nil {
		t.Fatalf("expire recent authentication: %v", err)
	}

	rr := adm2RoutePost(handler, token, url.Values{"smtp_host": {"smtp.stale.exemplo"}}, true, signer)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/admin/reautenticar?return_to=%2Fadmin%2Fauditoria" {
		t.Fatalf("location = %q", got)
	}
	var host string
	if err := db.QueryRow(`SELECT COALESCE(smtp_host, '') FROM workspaces WHERE id = 'workspace-rbac'`).Scan(&host); err != nil || host != "" {
		t.Fatalf("sessão sem reautenticação recente não pode gravar: host=%q err=%v", host, err)
	}
}

func TestAdminAuditoriaSaveRouteRateLimitsAfterTwentyPosts(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	authService := auth.NewService(db)
	signer := newAdm2TestSigner()
	handler := newAdminAuditoriaSaveRouteTestHandler(t, db, authService, signer)
	token := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)
	form := url.Values{"smtp_host": {"smtp.limite.exemplo"}, "smtp_port": {"587"}}

	for i := 1; i <= 20; i++ {
		rr := adm2RoutePost(handler, token, form, true, signer)
		if rr.Code != http.StatusOK {
			t.Fatalf("post %d: status = %d, want 200 (body=%q)", i, rr.Code, rr.Body.String())
		}
	}
	blocked := adm2RoutePost(handler, token, form, true, signer)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("21º post deve receber 429, recebeu %d (body=%q)", blocked.Code, blocked.Body.String())
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("resposta 429 deve incluir Retry-After")
	}
}

func TestAdminAuditoriaSaveRouteReplacesCredentialWithoutEcho(t *testing.T) {
	t.Setenv("SECURITY_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	authService := auth.NewService(db)
	signer := newAdm2TestSigner()
	handler := newAdminAuditoriaSaveRouteTestHandler(t, db, authService, signer)
	token := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)

	cipher, err := security.Encrypt("sintetico-rota-armazenado-3312")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := db.Exec(`UPDATE workspaces SET smtp_pass = ? WHERE id = 'workspace-rbac'`, cipher); err != nil {
		t.Fatalf("seed smtp_pass: %v", err)
	}

	rr := adm2RoutePost(handler, token, url.Values{
		"smtp_host": {"smtp.echo.exemplo"},
		"smtp_port": {"587"},
		"smtp_user": {"eco@exemplo.com"},
		"smtp_pass": {"sintetico-rota-substituto-9902"},
	}, true, signer)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, needle := range []string{"sintetico-rota-substituto-9902", "sintetico-rota-armazenado-3312", "sec:v1:"} {
		if strings.Contains(body, needle) {
			t.Fatalf("resposta da rota não deve conter %q", needle)
		}
	}

	var stored sql.NullString
	if err := db.QueryRow(`SELECT smtp_pass FROM workspaces WHERE id = 'workspace-rbac'`).Scan(&stored); err != nil || !stored.Valid {
		t.Fatalf("credencial substituída deve existir: %v", err)
	}
	decrypted, err := security.Decrypt(stored.String)
	if err != nil || decrypted != "sintetico-rota-substituto-9902" {
		t.Fatalf("credencial deve decriptar para a senha sintética enviada: %v", err)
	}
}

func newAdm2TestSigner() *csrfSigner {
	return &csrfSigner{secret: bytes.Repeat([]byte{13}, 32), ttl: time.Hour}
}
