package main

import (
	"database/sql"
	"encoding/base64"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/admincli"
	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

type dummyTemplateEngine struct{}

func (d dummyTemplateEngine) ExecuteTemplate(w io.Writer, name string, data any) error {
	return nil
}

func (d dummyTemplateEngine) Lookup(name string) *template.Template {
	return nil
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func setupTestDB(t *testing.T) (*sql.DB, *auth.Service, string) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	userID := uuid.NewString()
	workspaceID := uuid.NewString()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, "User Test", "user@test.local", string(hash), now, now)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		workspaceID, "WS", now, now)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`,
		workspaceID, userID, now)
	if err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	return db, auth.NewService(db), userID
}

func TestLoginRememberMe(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	tpl := dummyTemplateEngine{}

	t.Run("login_sem_remember", func(t *testing.T) {
		db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
		form := url.Values{}
		form.Add("email", "user@test.local")
		form.Add("password", "secret123")

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		handleLoginSubmit(rr, req, tpl, authService, db, "csrf")

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect, got %d", rr.Code)
		}

		cookies := rr.Result().Cookies()
		var sessionCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == sessionCookieName {
				sessionCookie = c
				break
			}
		}
		if sessionCookie == nil {
			t.Fatalf("expected session cookie")
		}

		// Max-Age deve ser ~24h (86400 segundos)
		if sessionCookie.MaxAge > 86400 || sessionCookie.MaxAge < 86300 {
			t.Errorf("expected max-age ~86400, got %d", sessionCookie.MaxAge)
		}

		var isRemember int
		err := db.QueryRow("SELECT is_remember FROM sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT 1", userID).Scan(&isRemember)
		if err != nil {
			t.Fatalf("query session: %v", err)
		}
		if isRemember != 0 {
			t.Errorf("expected is_remember=0, got %d", isRemember)
		}
	})

	t.Run("login_com_remember", func(t *testing.T) {
		db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
		form := url.Values{}
		form.Add("email", "user@test.local")
		form.Add("password", "secret123")
		form.Add("remember_me", "on")

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		handleLoginSubmit(rr, req, tpl, authService, db, "csrf")

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect, got %d", rr.Code)
		}

		cookies := rr.Result().Cookies()
		var sessionCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == sessionCookieName {
				sessionCookie = c
				break
			}
		}
		if sessionCookie == nil {
			t.Fatalf("expected session cookie")
		}

		// Max-Age deve ser ~30 dias (2592000 segundos)
		if sessionCookie.MaxAge > 2592000 || sessionCookie.MaxAge < 2591000 {
			t.Errorf("expected max-age ~2592000, got %d", sessionCookie.MaxAge)
		}

		var isRemember int
		err := db.QueryRow("SELECT is_remember FROM sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT 1", userID).Scan(&isRemember)
		if err != nil {
			t.Fatalf("query session: %v", err)
		}
		if isRemember != 1 {
			t.Errorf("expected is_remember=1, got %d", isRemember)
		}
	})
}

func TestLoginWithTemporaryPasswordRequiresChange(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	tempPassword := "temporary-password-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(tempPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash temporary password: %v", err)
	}
	expiresAt := time.Now().Add(time.Hour).Unix()
	if _, err := db.Exec(`
		UPDATE users
		SET password_hash = ?,
		    must_change_password = 1,
		    temporary_password_expires_at = ?
		WHERE id = ?
	`, string(hash), expiresAt, userID); err != nil {
		t.Fatalf("set temporary password: %v", err)
	}

	form := url.Values{}
	form.Add("email", "user@test.local")
	form.Add("password", tempPassword)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handleLoginSubmit(rr, req, dummyTemplateEngine{}, authService, db, "csrf")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != "/trocar-senha" {
		t.Fatalf("redirect = %q, want /trocar-senha", got)
	}
	if sessionCookie := findCookie(rr.Result().Cookies(), sessionCookieName); sessionCookie == nil {
		t.Fatalf("expected session cookie")
	}
}

func TestLoginWithExpiredTemporaryPasswordIsDenied(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	tempPassword := "temporary-password-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(tempPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash temporary password: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE users
		SET password_hash = ?,
		    must_change_password = 1,
		    temporary_password_expires_at = ?
		WHERE id = ?
	`, string(hash), time.Now().Add(-time.Minute).Unix(), userID); err != nil {
		t.Fatalf("set expired temporary password: %v", err)
	}

	form := url.Values{}
	form.Add("email", "user@test.local")
	form.Add("password", tempPassword)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handleLoginSubmit(rr, req, dummyTemplateEngine{}, authService, db, "csrf")

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if sessionCookie := findCookie(rr.Result().Cookies(), sessionCookieName); sessionCookie != nil {
		t.Fatalf("did not expect session cookie")
	}
}

func TestRequiredPasswordChangeClearsTemporaryFlags(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`
		UPDATE users
		SET must_change_password = 1,
		    temporary_password_expires_at = ?
		WHERE id = ?
	`, time.Now().Add(time.Hour).Unix(), userID); err != nil {
		t.Fatalf("set temporary flags: %v", err)
	}
	workspaceID, _, err := authService.ResolveWorkspaceMembership(userID)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	sessionToken, _, err := authService.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	form := url.Values{}
	form.Add("new_password", "new-password-123")
	form.Add("confirm_password", "new-password-123")
	req := httptest.NewRequest(http.MethodPost, "/trocar-senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()

	handleRequiredPasswordChangeSubmit(rr, req, dummyTemplateEngine{}, authService, db, authContext{
		UserID:            userID,
		ActiveWorkspaceID: workspaceID,
		Role:              "ADMIN",
		SessionToken:      sessionToken,
	}, "csrf")

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != "/" {
		t.Fatalf("redirect = %q, want /", got)
	}
	var mustChange int
	var expiresAt sql.NullInt64
	var hash string
	if err := db.QueryRow(`SELECT must_change_password, temporary_password_expires_at, password_hash FROM users WHERE id = ?`, userID).Scan(&mustChange, &expiresAt, &hash); err != nil {
		t.Fatalf("query password flags: %v", err)
	}
	if mustChange != 0 || expiresAt.Valid {
		t.Fatalf("temporary flags not cleared: must_change=%d expires_valid=%v", mustChange, expiresAt.Valid)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("new-password-123")); err != nil {
		t.Fatalf("new password hash mismatch: %v", err)
	}
}

func TestWithAuthRedirectsTemporaryPasswordUserBeforeAppAccess(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`
		UPDATE users
		SET must_change_password = 1,
		    temporary_password_expires_at = ?
		WHERE id = ?
	`, time.Now().Add(time.Hour).Unix(), userID); err != nil {
		t.Fatalf("set temporary flags: %v", err)
	}
	workspaceID, _, err := authService.ResolveWorkspaceMembership(userID)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	sessionToken, _, err := authService.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("csrf signer: %v", err)
	}
	called := false
	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if called {
		t.Fatalf("next handler should not be called before required password change")
	}
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != "/trocar-senha" {
		t.Fatalf("redirect = %q, want /trocar-senha", got)
	}
}

func TestLogin2FARememberMe(t *testing.T) {
	t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("correct"))
	db, authService, userID := setupTestDB(t)
	defer db.Close()

	// Ativa 2FA
	secret := "JBSWY3DPEHPK3PXP"
	encSecret, _ := encryptTextForAuth(secret)
	authService.UpdateTOTPSetup(userID, encSecret, "[]", true)

	tpl := dummyTemplateEngine{}

	t.Run("login_2fa_com_remember_preserva_estado", func(t *testing.T) {
		form := url.Values{}
		form.Add("email", "user@test.local")
		form.Add("password", "secret123")
		form.Add("remember_me", "on")

		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()

		handleLoginSubmit(rr, req, tpl, authService, db, "csrf")

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect to 2fa, got %d", rr.Code)
		}

		cookies := rr.Result().Cookies()
		var preAuthCookie *http.Cookie
		for _, c := range cookies {
			if c.Name == preAuthCookieName {
				preAuthCookie = c
				break
			}
		}
		if preAuthCookie == nil {
			t.Fatalf("expected pre-auth cookie")
		}

		// Verifica no banco se pre_auth_session tem remember_me = 1
		var rememberMe int
		err := db.QueryRow("SELECT remember_me FROM pre_auth_sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT 1", userID).Scan(&rememberMe)
		if err != nil {
			t.Fatalf("query pre auth session: %v", err)
		}
		if rememberMe != 1 {
			t.Errorf("expected remember_me=1 in pre_auth, got %d", rememberMe)
		}

		code, err := totp.GenerateCode(secret, time.Now())
		if err != nil {
			t.Fatalf("generate totp code: %v", err)
		}
		form2fa := url.Values{}
		form2fa.Add("code", code)

		req2 := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form2fa.Encode()))
		req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req2.AddCookie(preAuthCookie)
		rr2 := httptest.NewRecorder()

		handleLogin2FASubmit(rr2, req2, tpl, authService, db, "csrf")

		if rr2.Code != http.StatusSeeOther {
			t.Fatalf("expected redirect after 2FA, got %d", rr2.Code)
		}

		cookies2 := rr2.Result().Cookies()
		var sessionCookie *http.Cookie
		for _, c := range cookies2 {
			if c.Name == sessionCookieName {
				sessionCookie = c
				break
			}
		}
		if sessionCookie == nil {
			t.Fatalf("expected final session cookie")
		}

		// Max-Age deve ser ~30 dias (2592000 segundos) pois remember_me=1 foi preservado
		if sessionCookie.MaxAge > 2592000 || sessionCookie.MaxAge < 2591000 {
			t.Errorf("expected max-age ~2592000, got %d", sessionCookie.MaxAge)
		}

		var isRememberFinal int
		err = db.QueryRow("SELECT is_remember FROM sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT 1", userID).Scan(&isRememberFinal)
		if err != nil {
			t.Fatalf("query final session: %v", err)
		}
		if isRememberFinal != 1 {
			t.Errorf("expected is_remember=1 in final session, got %d", isRememberFinal)
		}
	})
}

func TestLogin2FAWithWrongAuthEncryptionKeyFails(t *testing.T) {
	oldKey := testAuthEncryptionKey("old")
	newKey := testAuthEncryptionKey("new")
	secret := "JBSWY3DPEHPK3PXP"
	t.Setenv("AUTH_ENCRYPTION_KEY", oldKey)
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	encSecret, err := encryptTextForAuth(secret)
	if err != nil {
		t.Fatalf("encrypt totp secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, "[]", true); err != nil {
		t.Fatalf("enable totp: %v", err)
	}

	tpl := dummyTemplateEngine{}
	form := url.Values{}
	form.Add("email", "user@test.local")
	form.Add("password", "secret123")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handleLoginSubmit(rr, req, tpl, authService, db, "csrf")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to 2fa, got %d", rr.Code)
	}
	var preAuthCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == preAuthCookieName {
			preAuthCookie = c
			break
		}
	}
	if preAuthCookie == nil {
		t.Fatalf("expected pre-auth cookie")
	}

	t.Setenv("AUTH_ENCRYPTION_KEY", newKey)
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate totp code: %v", err)
	}
	form2fa := url.Values{}
	form2fa.Add("code", code)
	req2 := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form2fa.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(preAuthCookie)
	rr2 := httptest.NewRecorder()
	handleLogin2FASubmit(rr2, req2, tpl, authService, db, "csrf")
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 2FA failure with wrong AUTH_ENCRYPTION_KEY, got %d", rr2.Code)
	}
	var activeSessions int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&activeSessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if activeSessions != 0 {
		t.Fatalf("expected no final session, got %d", activeSessions)
	}
	var pendingPreAuth int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`, userID).Scan(&pendingPreAuth); err != nil {
		t.Fatalf("count pre-auth: %v", err)
	}
	if pendingPreAuth != 0 {
		t.Fatalf("expected pre-auth revoked after decrypt failure, got %d", pendingPreAuth)
	}
}

func TestDisableAdmin2FAOverrideEnvDoesNotBypassLogin2FA(t *testing.T) {
	t.Setenv("DISABLE_ADMIN_2FA_OVERRIDE", "true")
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	encSecret, err := encryptTextForAuth("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt totp secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, "[]", true); err != nil {
		t.Fatalf("enable totp: %v", err)
	}
	preToken, _, err := authService.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader("code=000000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: preAuthCookieName, Value: preToken})
	rr := httptest.NewRecorder()
	handleLogin2FASubmit(rr, req, dummyTemplateEngine{}, authService, db, "csrf")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected override env not to bypass 2FA, got %d", rr.Code)
	}
}

func TestLoginAfterLocalDisable2FARecoverySkips2FAChallenge(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	encSecret, err := encryptTextForAuth("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt totp secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, "[]", true); err != nil {
		t.Fatalf("enable totp: %v", err)
	}
	if _, err := admincli.DisableAdmin2FA(db, "user@test.local"); err != nil {
		t.Fatalf("disable 2fa recovery: %v", err)
	}

	form := url.Values{}
	form.Add("email", "user@test.local")
	form.Add("password", "secret123")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handleLoginSubmit(rr, req, dummyTemplateEngine{}, authService, db, "csrf")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected successful login after 2FA recovery, got %d", rr.Code)
	}
	if got := rr.Header().Get("Location"); got == "/login/2fa" {
		t.Fatalf("expected login not to redirect to 2FA after recovery")
	}
	var pendingPreAuth int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`, userID).Scan(&pendingPreAuth); err != nil {
		t.Fatalf("count pre-auth: %v", err)
	}
	if pendingPreAuth != 0 {
		t.Fatalf("expected no active pre-auth after local 2FA recovery login, got %d", pendingPreAuth)
	}
}

func testAuthEncryptionKey(label string) string {
	raw := []byte("contabase-test-auth-key-" + label)
	for len(raw) < 32 {
		raw = append(raw, 'x')
	}
	return base64.StdEncoding.EncodeToString(raw[:32])
}
