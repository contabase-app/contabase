package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"

	"github.com/pquerna/otp/totp"
)

type authLockoutTemplateEngine struct{}

func (authLockoutTemplateEngine) ExecuteTemplate(w io.Writer, _ string, data any) error {
	var errMsg string
	switch v := data.(type) {
	case struct {
		Error     string
		CSRFToken string
	}:
		errMsg = v.Error
	case login2FAData:
		errMsg = v.Error
	default:
		return fmt.Errorf("unexpected template data: %T", data)
	}
	_, err := io.WriteString(w, errMsg)
	return err
}

func (authLockoutTemplateEngine) Lookup(name string) *template.Template {
	return nil
}

func TestPasswordFailuresCreateTemporaryLockout(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	for i := 0; i < authLockoutPasswordLimit; i++ {
		rr := submitLoginForLockoutTest(t, db, authService, tpl, "user@test.local", "wrong-password")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected unauthorized, got %d", i+1, rr.Code)
		}
	}

	state := authLockoutStateForTest(t, db, userID)
	if state.FailedPasswordCount != authLockoutPasswordLimit {
		t.Fatalf("failed_password_count = %d, want %d", state.FailedPasswordCount, authLockoutPasswordLimit)
	}
	if state.LockedUntil <= time.Now().Unix() {
		t.Fatalf("expected future locked_until, got %d", state.LockedUntil)
	}
	if state.LockReason != string(authLockoutStagePassword) {
		t.Fatalf("lock_reason = %q, want password", state.LockReason)
	}
}

func TestUnknownUserDoesNotCreatePersistentLockout(t *testing.T) {
	db, authService, _ := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	for i := 0; i < authLockoutPasswordLimit+2; i++ {
		rr := submitLoginForLockoutTest(t, db, authService, tpl, "missing@test.local", "wrong-password")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected unauthorized, got %d", i+1, rr.Code)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM auth_lockouts`).Scan(&count); err != nil {
		t.Fatalf("count auth_lockouts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no persistent lockout for unknown user, got %d", count)
	}
}

func TestLockedPasswordAccountDoesNotCreateSession(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	for i := 0; i < authLockoutPasswordLimit; i++ {
		if _, err := recordAuthFailure(db, userID, authLockoutStagePassword, time.Now()); err != nil {
			t.Fatalf("record auth failure: %v", err)
		}
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}

	rr := submitLoginForLockoutTest(t, db, authService, tpl, "user@test.local", "secret123")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected locked login to be unauthorized, got %d", rr.Code)
	}
	if got := sessionCountForTest(t, db, userID); got != 0 {
		t.Fatalf("expected no session during lockout, got %d", got)
	}
}

func TestPasswordLoginWorksAfterLockoutExpires(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	for i := 0; i < authLockoutPasswordLimit; i++ {
		if _, err := recordAuthFailure(db, userID, authLockoutStagePassword, time.Now()); err != nil {
			t.Fatalf("record auth failure: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE auth_lockouts SET locked_until = unixepoch() - 1 WHERE user_id = ?`, userID); err != nil {
		t.Fatalf("expire lockout: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}

	rr := submitLoginForLockoutTest(t, db, authService, tpl, "user@test.local", "secret123")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected login after lockout expiry, got %d", rr.Code)
	}
	if got := sessionCountForTest(t, db, userID); got != 1 {
		t.Fatalf("expected one session after expired lockout, got %d", got)
	}
}

func TestSuccessfulPasswordLoginClearsLockoutCounters(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	for i := 0; i < 2; i++ {
		if _, err := recordAuthFailure(db, userID, authLockoutStagePassword, time.Now()); err != nil {
			t.Fatalf("record auth failure: %v", err)
		}
	}
	rr := submitLoginForLockoutTest(t, db, authService, tpl, "user@test.local", "secret123")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected successful login, got %d", rr.Code)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, userID).Scan(&count); err != nil {
		t.Fatalf("count auth_lockouts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected successful login to clear auth lockout row, got %d", count)
	}
}

func TestPasswordSuccessWith2FAPendingOnlyClearsPasswordFailures(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	encSecret, err := encryptTextForAuth("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt totp secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, "[]", true); err != nil {
		t.Fatalf("enable totp: %v", err)
	}
	if _, err := recordAuthFailure(db, userID, authLockoutStagePassword, time.Now()); err != nil {
		t.Fatalf("record password failure: %v", err)
	}
	if _, err := recordAuthFailure(db, userID, authLockoutStageTOTP, time.Now()); err != nil {
		t.Fatalf("record totp failure: %v", err)
	}

	rr := submitLoginForLockoutTest(t, db, authService, tpl, "user@test.local", "secret123")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to 2FA, got %d", rr.Code)
	}
	state := authLockoutStateForTest(t, db, userID)
	if state.FailedPasswordCount != 0 {
		t.Fatalf("expected password failures cleared, got %d", state.FailedPasswordCount)
	}
	if state.Failed2FACount != 1 {
		t.Fatalf("expected 2FA failures preserved, got %d", state.Failed2FACount)
	}
}

func Test2FAFailuresCreateTemporaryLockout(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}
	enableTOTPForLockoutTest(t, authService, userID)

	for i := 0; i < authLockoutTOTPEventLimit; i++ {
		preAuthCookie := createPreAuthCookieForTest(t, authService, userID)
		rr := submit2FAForLockoutTest(t, db, authService, tpl, preAuthCookie, "111111")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("2FA attempt %d: expected unauthorized, got %d", i+1, rr.Code)
		}
	}

	state := authLockoutStateForTest(t, db, userID)
	if state.Failed2FACount != authLockoutTOTPEventLimit {
		t.Fatalf("failed_2fa_count = %d, want %d", state.Failed2FACount, authLockoutTOTPEventLimit)
	}
	if state.LockedUntil <= time.Now().Unix() {
		t.Fatalf("expected future locked_until, got %d", state.LockedUntil)
	}
	if state.LockReason != string(authLockoutStageTOTP) {
		t.Fatalf("lock_reason = %q, want totp", state.LockReason)
	}
}

func Test2FASucceedsAfterLockoutExpires(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}
	enableTOTPForLockoutTest(t, authService, userID)

	for i := 0; i < authLockoutTOTPEventLimit; i++ {
		if _, err := recordAuthFailure(db, userID, authLockoutStageTOTP, time.Now()); err != nil {
			t.Fatalf("record totp failure: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE auth_lockouts SET locked_until = unixepoch() - 1 WHERE user_id = ?`, userID); err != nil {
		t.Fatalf("expire lockout: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		t.Fatalf("delete sessions: %v", err)
	}
	preAuthCookie := createPreAuthCookieForTest(t, authService, userID)
	code, err := totp.GenerateCode("JBSWY3DPEHPK3PXP", time.Now())
	if err != nil {
		t.Fatalf("generate totp code: %v", err)
	}
	rr := submit2FAForLockoutTest(t, db, authService, tpl, preAuthCookie, code)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 2FA success after lockout expiry, got %d", rr.Code)
	}
	if got := sessionCountForTest(t, db, userID); got != 1 {
		t.Fatalf("expected final session after 2FA, got %d", got)
	}
}

func TestLoginLockoutMessageDoesNotEnumerateUser(t *testing.T) {
	db, authService, userID := setupTestDB(t)
	defer db.Close()
	tpl := authLockoutTemplateEngine{}

	for i := 0; i < authLockoutPasswordLimit; i++ {
		if _, err := recordAuthFailure(db, userID, authLockoutStagePassword, time.Now()); err != nil {
			t.Fatalf("record auth failure: %v", err)
		}
	}

	knownLocked := submitLoginForLockoutTest(t, db, authService, tpl, "user@test.local", "secret123")
	unknown := submitLoginForLockoutTest(t, db, authService, tpl, "missing@test.local", "secret123")
	if knownLocked.Code != http.StatusUnauthorized || unknown.Code != http.StatusUnauthorized {
		t.Fatalf("expected both responses to be unauthorized, got known=%d unknown=%d", knownLocked.Code, unknown.Code)
	}
	if knownLocked.Body.String() != unknown.Body.String() {
		t.Fatalf("expected same public error for locked known user and unknown user, got %q vs %q", knownLocked.Body.String(), unknown.Body.String())
	}
}

func TestAuthLockoutDoesNotAffectBootstrapSetup(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	defer db.Close()

	if _, err := runInitialSetup(db, "Admin", "admin@test.local", "secret123", "personal"); err != nil {
		t.Fatalf("run initial setup should not depend on auth_lockouts: %v", err)
	}
}

func submitLoginForLockoutTest(t *testing.T, db *sql.DB, authService *auth.Service, tpl authLockoutTemplateEngine, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("email", email)
	form.Set("password", password)
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handleLoginSubmit(rr, req, tpl, authService, db, "csrf")
	return rr
}

func submit2FAForLockoutTest(t *testing.T, db *sql.DB, authService *auth.Service, tpl authLockoutTemplateEngine, preAuthCookie *http.Cookie, code string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("code", code)
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(preAuthCookie)
	rr := httptest.NewRecorder()
	handleLogin2FASubmit(rr, req, tpl, authService, db, "csrf")
	return rr
}

func enableTOTPForLockoutTest(t *testing.T, authService *auth.Service, userID string) {
	t.Helper()
	encSecret, err := encryptTextForAuth("JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt totp secret: %v", err)
	}
	if err := authService.UpdateTOTPSetup(userID, encSecret, "[]", true); err != nil {
		t.Fatalf("enable totp: %v", err)
	}
}

func createPreAuthCookieForTest(t *testing.T, authService *auth.Service, userID string) *http.Cookie {
	t.Helper()
	token, _, err := authService.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth session: %v", err)
	}
	return &http.Cookie{Name: preAuthCookieName, Value: token}
}

func authLockoutStateForTest(t *testing.T, db *sql.DB, userID string) authLockoutState {
	t.Helper()
	var state authLockoutState
	if err := db.QueryRow(`
		SELECT failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason
		FROM auth_lockouts
		WHERE user_id = ?
	`, userID).Scan(&state.FailedPasswordCount, &state.Failed2FACount, &state.FirstFailedAt, &state.LastFailedAt, &state.LockedUntil, &state.LockReason); err != nil {
		t.Fatalf("load auth lockout state: %v", err)
	}
	return state
}

func sessionCountForTest(t *testing.T, db *sql.DB, userID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, userID).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return count
}
