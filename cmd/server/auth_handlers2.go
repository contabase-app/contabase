package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type login2FAData struct {
	Error     string
	CSRFToken string
}

type securityPageData struct {
	Title                 string
	CSRFToken             string
	Error                 string
	Success               string
	TOTPEnabled           bool
	TOTPSecret            string
	TOTPURI               string
	TOTPQRCodeData        string
	QRCode                string
	BackupCodes           []string
	SecurityAction        string
	RequiresCurrentFactor bool
	MFASetupToken         string
}

func setSecOpsFailureHeaders(w http.ResponseWriter, eventType, severity, reason, attemptedEmail string) {
	if strings.TrimSpace(eventType) != "" {
		w.Header().Set(secOpsEventHeader, strings.ToLower(strings.TrimSpace(eventType)))
	}
	if strings.TrimSpace(severity) != "" {
		w.Header().Set(secOpsSeverityHeader, strings.ToUpper(strings.TrimSpace(severity)))
	}
	if strings.TrimSpace(reason) != "" {
		w.Header().Set(secOpsMetaReasonHeader, strings.TrimSpace(reason))
	}
	if strings.TrimSpace(attemptedEmail) != "" {
		w.Header().Set(secOpsMetaEmailHeader, strings.ToLower(strings.TrimSpace(attemptedEmail)))
	}
}

func lookupUserIdentityByEmail(db *sql.DB, email string) (string, bool, error) {
	var userID string
	err := db.QueryRow(`SELECT id FROM users WHERE LOWER(TRIM(email)) = LOWER(TRIM(?)) LIMIT 1`, strings.TrimSpace(email)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(userID), true, nil
}

func lookupUserEmailByID(db *sql.DB, userID string) string {
	var email string
	if err := db.QueryRow(`SELECT LOWER(TRIM(email)) FROM users WHERE id = ?`, strings.TrimSpace(userID)).Scan(&email); err != nil {
		return ""
	}
	return strings.TrimSpace(email)
}

func lookupUserPrimaryWorkspace(db *sql.DB, userID string) string {
	var workspaceID string
	err := db.QueryRow(`
		SELECT wm.workspace_id
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE u.id = ?
		ORDER BY wm.joined_at ASC
		LIMIT 1
	`, strings.TrimSpace(userID)).Scan(&workspaceID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(workspaceID)
}

func insertUserNotification(db *sql.DB, userID, title, message, notificationType string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	if _, err := db.Exec(`
		INSERT INTO user_notifications (id, user_id, title, message, type, is_read, created_at)
		VALUES (?, ?, ?, ?, ?, 0, ?)
	`, uuid.NewString(), userID, strings.TrimSpace(title), strings.TrimSpace(message), strings.TrimSpace(notificationType), time.Now().Unix()); err != nil {
		log.Printf("insert user notification failed: user=%s type=%s err=%v", userID, notificationType, err)
	}
}

func enqueueLoginSecurityNotifications(db *sql.DB, userID string, r *http.Request) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	currentIP := strings.TrimSpace(clientIP(r))
	currentUA := strings.TrimSpace(r.UserAgent())

	var lastSuccessIP, lastSuccessUA string
	var lastSuccessAt int64
	err := db.QueryRow(`
		SELECT COALESCE(ip, ''), COALESCE(user_agent, ''), created_at
		FROM auth_audit_events
		WHERE user_id = ? AND event_type = 'LOGIN_SUCCESS'
		ORDER BY created_at DESC
		LIMIT 1
	`, userID).Scan(&lastSuccessIP, &lastSuccessUA, &lastSuccessAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("load last login success failed: user=%s err=%v", userID, err)
	}

	if lastSuccessAt > 0 && (strings.TrimSpace(lastSuccessIP) != currentIP || strings.TrimSpace(lastSuccessUA) != currentUA) {
		insertUserNotification(
			db,
			userID,
			"Novo login detectado",
			fmt.Sprintf("Novo login detectado do IP %s usando o navegador %s.", currentIP, currentUA),
			"security.new_login",
		)
	}

	var lastInvalidPasswordAttemptAt int64
	err = db.QueryRow(`
		SELECT created_at
		FROM auth_audit_events
		WHERE user_id = ?
		  AND event_type = 'LOGIN_FAIL'
		  AND metadata_json LIKE '%"reason":"invalid_password"%'
		  AND created_at > ?
		ORDER BY created_at DESC
		LIMIT 1
	`, userID, lastSuccessAt).Scan(&lastInvalidPasswordAttemptAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("load last invalid password attempt failed: user=%s err=%v", userID, err)
		return
	}
	if lastInvalidPasswordAttemptAt > 0 {
		insertUserNotification(
			db,
			userID,
			"Tentativa incorreta de senha detectada",
			fmt.Sprintf("Atenção: Detectamos 1 tentativa incorreta de senha no seu perfil em %s.", time.Unix(lastInvalidPasswordAttemptAt, 0).Format("02/01/2006 15:04")),
			"security.password_alert",
		)
	}
}

func handleLogin2FAPage(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string) {
	handleLogin2FAPageWithStatus(w, tpl, csrfToken, errMsg, http.StatusOK)
}

func handleLogin2FAPageWithStatus(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string, status int) {
	data := login2FAData{Error: errMsg, CSRFToken: csrfToken}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if err := tpl.ExecuteTemplate(w, "login-2fa-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func handleLogin2FASubmit(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, db *sql.DB, csrfToken string) {
	if err := r.ParseForm(); err != nil {
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "malformed_request", "")
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Dados inválidos.", http.StatusUnauthorized)
		return
	}
	cookie, err := r.Cookie(preAuthCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		clearPreAuthCookie(w, r)
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "expired_challenge", "")
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Sessão de autenticação expirada. Faça login novamente.", http.StatusUnauthorized)
		return
	}
	preAuthToken := strings.TrimSpace(cookie.Value)
	_, userID, rememberMe, err := authService.ResolvePreAuthSession(preAuthToken)
	if err != nil {
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "expired_challenge", "")
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Sessão de autenticação expirada. Faça login novamente.", http.StatusUnauthorized)
		return
	}
	locked, err := isAuthLocked(db, userID, time.Now())
	if err != nil {
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		log.Printf("auth lockout check failed: user=%s err=%v", userID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if locked {
		writeAuthAuditEvent(db, r, userID, "", "LOGIN_FAIL", map[string]string{"stage": "totp", "reason": "lockout"})
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "lockout", "")
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Código inválido.", http.StatusUnauthorized)
		return
	}
	userEmail := lookupUserEmailByID(db, userID)
	if wsID := lookupUserPrimaryWorkspace(db, userID); wsID != "" {
		w.Header().Set("X-SecOps-Meta-Workspace-Id", wsID)
	}
	code := strings.TrimSpace(r.FormValue("code"))
	backupCode := strings.TrimSpace(r.FormValue("backup_code"))

	secretEnc, err := authService.GetEncryptedTOTPSecret(userID)
	if err != nil || secretEnc == "" {
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "totp_not_configured", userEmail)
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "2FA não está configurado para este usuário.", http.StatusUnauthorized)
		return
	}
	secret, err := decryptTextForAuth(secretEnc)
	if err != nil {
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "totp_secret_error", userEmail)
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Falha ao validar 2FA.", http.StatusUnauthorized)
		return
	}
	valid := false
	usedTOTPCode := false
	var totpTimeStep int64
	if code != "" {
		totpTimeStep, valid = matchingTOTPTimeStep(secret, code, time.Now())
		usedTOTPCode = valid
	}
	if !valid && backupCode != "" {
		payload, err := authService.GetBackupCodeHashes(userID)
		if err == nil {
			hashes, decErr := unmarshalBackupCodeHashes(payload)
			if decErr == nil {
				used, next := consumeBackupCode(hashes, backupCode)
				if used {
					nextJSON, marshalErr := marshalBackupCodeHashes(next)
					if marshalErr != nil {
						_ = authService.RevokePreAuthSession(preAuthToken)
						clearPreAuthCookie(w, r)
						http.Error(w, "internal server error", http.StatusInternalServerError)
						return
					}
					replaced, replaceErr := authService.ReplaceBackupCodeHashesIfCurrent(userID, payload, nextJSON)
					if replaceErr != nil {
						_ = authService.RevokePreAuthSession(preAuthToken)
						clearPreAuthCookie(w, r)
						http.Error(w, "internal server error", http.StatusInternalServerError)
						return
					}
					if replaced {
						writeAuthAuditEvent(db, r, userID, "", "BACKUP_CODE_USED", map[string]string{"remaining": fmt.Sprintf("%d", len(next))})
						valid = true
					}
				}
			}
		}
	}
	if !valid {
		_, lockoutErr := recordAuthFailure(db, userID, authLockoutStageTOTP, time.Now())
		writeAuthAuditEvent(db, r, userID, "", "LOGIN_FAIL", map[string]string{"stage": "totp"})
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		if lockoutErr != nil {
			log.Printf("auth lockout record failed: user=%s stage=totp err=%v", userID, lockoutErr)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "invalid_totp", userEmail)
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Código inválido.", http.StatusUnauthorized)
		return
	}
	var consumeErr error
	if usedTOTPCode {
		consumeErr = authService.ConsumePreAuthSessionWithTOTP(preAuthToken, userID, totpTimeStep)
	} else {
		consumeErr = authService.ConsumePreAuthSession(preAuthToken)
	}
	if consumeErr != nil {
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		if errors.Is(consumeErr, auth.ErrTOTPReplay) {
			writeAuthAuditEvent(db, r, userID, "", "LOGIN_FAIL", map[string]string{"stage": "totp", "reason": "replayed_code"})
			setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "replayed_totp", userEmail)
			handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Código já utilizado. Aguarde o próximo código.", http.StatusUnauthorized)
			return
		}
		setSecOpsFailureHeaders(w, "auth.2fa_failed", "HIGH", "expired_challenge", userEmail)
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Sessão de autenticação expirada. Faça login novamente.", http.StatusUnauthorized)
		return
	}
	clearPreAuthCookie(w, r)
	if err := clearAuthLockout(db, userID); err != nil {
		log.Printf("auth lockout clear failed: user=%s err=%v", userID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	temporaryPasswordState, err := authService.TemporaryPasswordState(userID, time.Now())
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if temporaryPasswordState.Expired {
		writeAuthAuditEvent(db, r, userID, "", "LOGIN_FAIL", map[string]string{"stage": "totp", "reason": "temporary_password_expired"})
		setSecOpsFailureHeaders(w, "auth.failed", "HIGH", "temporary_password_expired", userEmail)
		handleLogin2FAPageWithStatus(w, tpl, csrfToken, "Senha temporária expirada. Solicite um novo reset de acesso.", http.StatusUnauthorized)
		return
	}
	if err := issueFinalSession(w, r, authService, userID, rememberMe); err != nil {
		_ = authService.RevokePreAuthSession(preAuthToken)
		clearPreAuthCookie(w, r)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	enqueueLoginSecurityNotifications(db, userID, r)
	if temporaryPasswordState.Required {
		writeAuthAuditEvent(db, r, userID, "", "LOGIN_SUCCESS", map[string]string{"method": "temporary_password+totp"})
		http.Redirect(w, r, "/trocar-senha", http.StatusSeeOther)
		return
	}
	writeAuthAuditEvent(db, r, userID, "", "LOGIN_SUCCESS", map[string]string{"method": "password+totp"})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleLoginSubmit(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, db *sql.DB, csrfToken string) {
	if err := r.ParseForm(); err != nil {
		handleLoginPageWithStatus(w, tpl, csrfToken, "Dados de login inválidos.", http.StatusUnauthorized)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	normalizedEmail := normalizeEmail(email)
	password := r.FormValue("password")
	isRemember := r.FormValue("remember_me") == "on" || r.FormValue("remember_me") == "true"
	if email == "" || password == "" {
		setSecOpsFailureHeaders(w, "auth.failed", "HIGH", "missing_credentials", normalizedEmail)
		handleLoginPageWithStatus(w, tpl, csrfToken, "Preencha e-mail e senha.", http.StatusUnauthorized)
		return
	}
	loginUserID, exists, err := lookupUserIdentityByEmail(db, normalizedEmail)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	userID, authErr := authService.Authenticate(email, password)
	if authErr != nil && !errors.Is(authErr, auth.ErrInactiveAccount) && !errors.Is(authErr, auth.ErrInvalidCredentials) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	lockoutUserID := loginUserID
	if !exists {
		// Keep the lockout lookup on the same path without persisting attacker-
		// controlled identities in auth_lockouts.
		lockoutUserID = "00000000-0000-0000-0000-000000000000"
	}
	locked, err := isAuthLocked(db, lockoutUserID, time.Now())
	if err != nil {
		log.Printf("auth lockout check failed: user=%s err=%v", loginUserID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		writeAuthAuditEvent(db, r, "", "", "LOGIN_FAIL", map[string]string{"email": normalizedEmail, "reason": "unknown_identity"})
		logSecurityEvent(db, r, "", "", "auth.invalid_user", "WARNING", map[string]any{
			"email_tentado": normalizedEmail,
			"reason":        "invalid_credentials",
		})
		w.WriteHeader(http.StatusUnauthorized)
		handleLoginPage(w, tpl, csrfToken, "E-mail ou senha inválidos.")
		return
	}
	if locked {
		writeAuthAuditEvent(db, r, loginUserID, "", "LOGIN_FAIL", map[string]string{"email": normalizedEmail, "reason": "lockout"})
		logSecurityEvent(db, r, "", "", "auth.failed", "HIGH", map[string]any{
			"email_tentado": normalizedEmail,
			"reason":        "invalid_credentials",
		})
		w.WriteHeader(http.StatusUnauthorized)
		handleLoginPage(w, tpl, csrfToken, "E-mail ou senha inválidos.")
		return
	}
	if authErr != nil {
		if errors.Is(authErr, auth.ErrInactiveAccount) || errors.Is(authErr, auth.ErrInvalidCredentials) {
			reason := "invalid_password"
			if errors.Is(authErr, auth.ErrInactiveAccount) {
				reason = "inactive_account"
			}
			writeAuthAuditEvent(db, r, loginUserID, "", "LOGIN_FAIL", map[string]string{"email": normalizedEmail, "reason": reason})
			logSecurityEvent(db, r, "", "", "auth.failed", "HIGH", map[string]any{
				"email_tentado": normalizedEmail,
				"reason":        "invalid_credentials",
			})
			if _, lockoutErr := recordAuthFailure(db, loginUserID, authLockoutStagePassword, time.Now()); lockoutErr != nil {
				log.Printf("auth lockout record failed: user=%s stage=password err=%v", loginUserID, lockoutErr)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			handleLoginPage(w, tpl, csrfToken, "E-mail ou senha inválidos.")
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	temporaryPasswordState, err := authService.TemporaryPasswordState(userID, time.Now())
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if temporaryPasswordState.Expired {
		writeAuthAuditEvent(db, r, userID, "", "LOGIN_FAIL", map[string]string{"email": normalizedEmail, "reason": "temporary_password_expired"})
		w.WriteHeader(http.StatusUnauthorized)
		handleLoginPage(w, tpl, csrfToken, "Senha temporária expirada. Solicite um novo reset de acesso.")
		return
	}
	totpEnabled, err := authService.IsTOTPEnabled(userID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if totpEnabled {
		if err := clearAuthFailureStage(db, userID, authLockoutStagePassword); err != nil {
			log.Printf("auth lockout password clear failed: user=%s err=%v", userID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		preToken, preExpiresAt, err := authService.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, isRemember)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		setPreAuthCookie(w, r, preToken, preExpiresAt)
		writeAuthAuditEvent(db, r, userID, "", "TOTP_CHALLENGE", map[string]string{"method": "password"})
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login/2fa")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
		return
	}
	if err := clearAuthLockout(db, userID); err != nil {
		log.Printf("auth lockout clear failed: user=%s err=%v", userID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := issueFinalSession(w, r, authService, userID, isRemember); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	enqueueLoginSecurityNotifications(db, userID, r)
	if temporaryPasswordState.Required {
		writeAuthAuditEvent(db, r, userID, "", "LOGIN_SUCCESS", map[string]string{"method": "temporary_password"})
		http.Redirect(w, r, "/trocar-senha", http.StatusSeeOther)
		return
	}
	writeAuthAuditEvent(db, r, userID, "", "LOGIN_SUCCESS", map[string]string{"method": "password"})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleSecuritySettingsPage(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, userID string) {
	_ = tpl
	_ = authService
	_ = userID
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/configuracoes?secao=perfil")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/configuracoes?secao=perfil", http.StatusSeeOther)
}

func handleTOTPSetupStart(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, userID string) {
	enabled, err := authService.IsTOTPEnabled(userID)
	if err != nil {
		http.Error(w, "falha ao verificar 2FA", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		renderSensitiveMFAAction(w, tpl, r, "/configuracoes/seguranca/totp/setup", enabled, "Confirme sua identidade para configurar 2FA.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	if err := verifySensitiveMFAAction(authService, userID, r.FormValue("current_password"), r.FormValue("current_totp_code")); err != nil {
		renderSensitiveMFAActionWithError(w, tpl, r, "/configuracoes/seguranca/totp/setup", enabled, "Não foi possível confirmar sua identidade.")
		return
	}
	var email string
	_ = authService.DB.QueryRow(`SELECT email FROM users WHERE id = ?`, userID).Scan(&email)
	key, qrData, err := generateTOTPSetup(email)
	if err != nil {
		http.Error(w, "falha ao iniciar TOTP", http.StatusInternalServerError)
		return
	}
	secretEnc, err := encryptTextForAuth(key.Secret())
	if err != nil {
		http.Error(w, "falha ao preparar TOTP", http.StatusInternalServerError)
		return
	}
	challengeToken, _, err := authService.CreateMFASetupChallenge(userID, secretEnc, 10*time.Minute)
	if err != nil {
		http.Error(w, "falha ao iniciar TOTP", http.StatusInternalServerError)
		return
	}
	qrData = strings.NewReplacer("\n", "", "\r", "", "\t", "", " ", "").Replace(qrData)
	data := securityPageData{
		CSRFToken:      csrfTokenFromRequestCookie(r),
		TOTPSecret:     key.Secret(),
		TOTPURI:        key.URL(),
		TOTPQRCodeData: qrData,
		QRCode:         qrData,
		MFASetupToken:  challengeToken,
	}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-setup", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func handleTOTPSetupConfirm(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, db *sql.DB, userID, currentSessionToken string) {
	if err := r.ParseForm(); err != nil {
		log.Printf("totp confirm parse form error: user=%s err=%v", userID, err)
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	challengeToken := strings.TrimSpace(r.FormValue("mfa_setup_token"))
	code := strings.TrimSpace(r.FormValue("code"))
	if challengeToken == "" || code == "" {
		log.Printf("totp confirm missing fields: user=%s challenge_empty=%t code_empty=%t", userID, challengeToken == "", code == "")
		http.Error(w, "desafio e código são obrigatórios", http.StatusBadRequest)
		return
	}
	enc, err := authService.ResolveMFASetupChallenge(challengeToken, userID)
	if err != nil {
		http.Error(w, "configuração expirada. Confirme sua identidade e tente novamente.", http.StatusUnauthorized)
		return
	}
	secret, err := decryptTextForAuth(enc)
	if err != nil {
		log.Printf("totp confirm challenge decrypt error: user=%s err=%v", userID, err)
		http.Error(w, "falha ao validar configuração", http.StatusInternalServerError)
		return
	}
	if !validateTOTPCode(secret, code, time.Now()) {
		log.Printf("totp confirm invalid code: user=%s code_len=%d", userID, len(code))
		http.Error(w, "código TOTP inválido", http.StatusBadRequest)
		return
	}
	codes, err := generateBackupCodes()
	if err != nil {
		http.Error(w, "falha ao gerar backup codes", http.StatusInternalServerError)
		return
	}
	hashes, err := hashBackupCodes(codes)
	if err != nil {
		http.Error(w, "falha ao gerar backup codes", http.StatusInternalServerError)
		return
	}
	hashesJSON, err := marshalBackupCodeHashes(hashes)
	if err != nil {
		http.Error(w, "falha ao gerar backup codes", http.StatusInternalServerError)
		return
	}
	if err := authService.CompleteMFASetupChallenge(challengeToken, userID, enc, hashesJSON, currentSessionToken); err != nil {
		log.Printf("totp confirm persist error: user=%s backup_codes=%d err=%v", userID, len(codes), err)
		http.Error(w, "falha ao salvar TOTP", http.StatusInternalServerError)
		return
	}
	writeAuthAuditEvent(db, r, userID, "", "TOTP_ACTIVATED", map[string]string{"backup_codes": fmt.Sprintf("%d", len(codes))})
	data := securityPageData{
		Success:     "TOTP ativado com sucesso. Guarde os códigos de recuperação em local offline seguro.",
		TOTPEnabled: true,
		BackupCodes: codes,
	}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-enabled", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func handleTOTPDisable(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, db *sql.DB, userID, currentSessionToken string) {
	if r.Method == http.MethodGet {
		renderSensitiveMFAAction(w, tpl, r, "/configuracoes/seguranca/totp/desativar", true, "Confirme sua identidade para desativar 2FA.")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	if err := verifySensitiveMFAAction(authService, userID, r.FormValue("current_password"), r.FormValue("current_totp_code")); err != nil {
		renderSensitiveMFAActionWithError(w, tpl, r, "/configuracoes/seguranca/totp/desativar", true, "Não foi possível confirmar sua identidade.")
		return
	}
	if err := authService.DisableTOTPAndRevokeOtherSessions(userID, currentSessionToken); err != nil {
		http.Error(w, "falha ao desativar TOTP", http.StatusInternalServerError)
		return
	}
	writeAuthAuditEvent(db, r, userID, "", "TOTP_DISABLED", nil)
	data := securityPageData{Success: "TOTP desativado.", TOTPEnabled: false}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-disabled", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func verifySensitiveMFAAction(authService *auth.Service, userID, currentPassword, currentTOTPCode string) error {
	var storedHash string
	if err := authService.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ? AND lower(trim(status)) = 'active' AND deleted_at = 0`, userID).Scan(&storedHash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(currentPassword)) != nil {
		return auth.ErrInvalidCredentials
	}
	enabled, err := authService.IsTOTPEnabled(userID)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	secretEnc, err := authService.GetEncryptedTOTPSecret(userID)
	if err != nil {
		return err
	}
	secret, err := decryptTextForAuth(secretEnc)
	if err != nil {
		return err
	}
	step, valid := matchingTOTPTimeStep(secret, currentTOTPCode, time.Now())
	if !valid {
		return auth.ErrInvalidCredentials
	}
	return authService.ConsumeTOTPTimeStep(userID, step)
}

func renderSensitiveMFAAction(w http.ResponseWriter, tpl handlers.TemplateEngine, r *http.Request, action string, requiresCurrentFactor bool, message string) {
	data := securityPageData{
		CSRFToken:             csrfTokenFromRequestCookie(r),
		SecurityAction:        action,
		RequiresCurrentFactor: requiresCurrentFactor,
		Title:                 message,
	}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-reauth", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func renderSensitiveMFAActionWithError(w http.ResponseWriter, tpl handlers.TemplateEngine, r *http.Request, action string, requiresCurrentFactor bool, message string) {
	data := securityPageData{
		CSRFToken:             csrfTokenFromRequestCookie(r),
		SecurityAction:        action,
		RequiresCurrentFactor: requiresCurrentFactor,
		Title:                 "Confirme sua identidade para continuar.",
		Error:                 message,
	}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-reauth", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func handleBackupCodesRegenerate(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, db *sql.DB, userID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	enabled, err := authService.IsTOTPEnabled(userID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !enabled {
		http.Error(w, "TOTP não está habilitado", http.StatusBadRequest)
		return
	}
	if err := verifySensitiveMFAAction(authService, userID, r.FormValue("current_password"), r.FormValue("current_totp_code")); err != nil {
		http.Error(w, "não foi possível confirmar sua identidade", http.StatusUnauthorized)
		return
	}
	codes, err := generateBackupCodes()
	if err != nil {
		http.Error(w, "falha ao gerar backup codes", http.StatusInternalServerError)
		return
	}
	hashes, err := hashBackupCodes(codes)
	if err != nil {
		http.Error(w, "falha ao gerar backup codes", http.StatusInternalServerError)
		return
	}
	hashesJSON, err := marshalBackupCodeHashes(hashes)
	if err != nil {
		http.Error(w, "falha ao gerar backup codes", http.StatusInternalServerError)
		return
	}
	if err := authService.ReplaceBackupCodeHashes(userID, hashesJSON); err != nil {
		http.Error(w, "falha ao salvar backup codes", http.StatusInternalServerError)
		return
	}
	writeAuthAuditEvent(db, r, userID, "", "BACKUP_CODES_REGENERATED", map[string]string{"count": fmt.Sprintf("%d", len(codes))})
	data := securityPageData{Success: "Códigos regenerados. Salve-os offline.", TOTPEnabled: true, BackupCodes: codes}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-enabled", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func handleTOTPCancel(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine) {
	data := securityPageData{}
	if err := tpl.ExecuteTemplate(w, "perfil-2fa-disabled", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func csrfTokenFromRequestCookie(r *http.Request) string {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func isAdminRole(role string) bool {
	return strings.TrimSpace(role) == models.RoleAdmin
}
