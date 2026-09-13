package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminRecentAuthenticationGateRedirectsStaleSession(t *testing.T) {
	db, authService, _, _, token := newAdminReauthTestFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET reauthenticated_at = 0 WHERE user_id = 'admin-user'`); err != nil {
		t.Fatalf("expire recent authentication: %v", err)
	}
	ctx := authContext{UserID: "admin-user", ActiveWorkspaceID: "workspace-rbac", Role: "ADMIN", SessionToken: token}

	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/salvar", nil)
	rr := httptest.NewRecorder()
	if requireRecentAuthentication(rr, req, authService, ctx, "/admin/usuarios") {
		t.Fatal("stale session passed the recent-authentication gate")
	}
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != "/admin/reautenticar?return_to=%2Fadmin%2Fusuarios" {
		t.Fatalf("location = %q", got)
	}

	hxReq := httptest.NewRequest(http.MethodDelete, "/users/target-user", nil)
	hxReq.Header.Set("HX-Request", "true")
	hxRR := httptest.NewRecorder()
	if requireRecentAuthentication(hxRR, hxReq, authService, ctx, "/admin/usuarios") {
		t.Fatal("stale HTMX session passed the recent-authentication gate")
	}
	if hxRR.Code != http.StatusOK {
		t.Fatalf("HTMX status = %d, want %d", hxRR.Code, http.StatusOK)
	}
	if got := hxRR.Header().Get("HX-Redirect"); got != "/admin/reautenticar?return_to=%2Fadmin%2Fusuarios" {
		t.Fatalf("HX-Redirect = %q", got)
	}
}

func TestAdminReauthenticationRenewsSessionAndAuditsSuccess(t *testing.T) {
	db, authService, _, handler, token := newAdminReauthTestFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET reauthenticated_at = 0 WHERE user_id = 'admin-user'`); err != nil {
		t.Fatalf("expire recent authentication: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/admin/reautenticar?return_to=%2Fadmin%2Fbackups", nil)
	getReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	getRR := httptest.NewRecorder()
	handler.ServeHTTP(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d; body=%q", getRR.Code, http.StatusOK, getRR.Body.String())
	}
	csrfToken := extractAdminReauthCSRFToken(t, getRR.Body.String())
	csrfCookie := responseCookie(t, getRR.Result().Cookies(), csrfCookieName)

	form := url.Values{
		"csrf_token":       {csrfToken},
		"return_to":        {"/admin/backups"},
		"current_password": {"correct-password"},
	}
	postReq := httptest.NewRequest(http.MethodPost, "/admin/reautenticar", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	postReq.AddCookie(csrfCookie)
	postRR := httptest.NewRecorder()
	handler.ServeHTTP(postRR, postReq)

	if postRR.Code != http.StatusSeeOther {
		t.Fatalf("POST status = %d, want %d; body=%q", postRR.Code, http.StatusSeeOther, postRR.Body.String())
	}
	if got := postRR.Header().Get("Location"); got != "/admin/backups" {
		t.Fatalf("POST location = %q, want /admin/backups", got)
	}
	recent, err := authService.SessionRecentlyAuthenticated(token, time.Now(), recentAdminAuthenticationWindow)
	if err != nil || !recent {
		t.Fatalf("session recent = %v, err = %v; want true", recent, err)
	}
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM auth_audit_events WHERE user_id = 'admin-user' AND event_type = 'ADMIN_REAUTH_SUCCESS'`).Scan(&auditCount); err != nil {
		t.Fatalf("query reauthentication audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("success audit count = %d, want 1", auditCount)
	}
}

func TestAdminReauthenticationFailureIsGenericAndReturnPathIsClosed(t *testing.T) {
	db, authService, signer, handler, token := newAdminReauthTestFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE sessions SET reauthenticated_at = 0 WHERE user_id = 'admin-user'`); err != nil {
		t.Fatalf("expire recent authentication: %v", err)
	}
	csrfToken := signer.issue(time.Now())
	form := url.Values{
		"csrf_token":       {csrfToken},
		"return_to":        {"https://attacker.example/collect"},
		"current_password": {"wrong-password"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/reautenticar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Não foi possível confirmar sua identidade.") {
		t.Fatalf("generic error missing: %q", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `name="return_to" value="/configuracoes"`) {
		t.Fatalf("unsafe return path was not replaced: %q", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "attacker.example") {
		t.Fatal("unsafe return URL reflected in response")
	}
	if recent, err := authService.SessionRecentlyAuthenticated(token, time.Now(), recentAdminAuthenticationWindow); err != nil || recent {
		t.Fatalf("failed confirmation session recent = %v, err = %v; want false", recent, err)
	}
}

func TestAdminReauthenticationPageRequestsTOTPWhenEnabled(t *testing.T) {
	db, _, _, handler, token := newAdminReauthTestFixture(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE users SET totp_enabled = 1 WHERE id = 'admin-user'`); err != nil {
		t.Fatalf("enable TOTP fixture: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/reautenticar?return_to=%2Fadmin%2Fusuarios", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), `name="current_totp_code"`) {
		t.Fatal("TOTP-enabled reauthentication page omitted the current factor field")
	}
}

func newAdminReauthTestFixture(t *testing.T) (*sql.DB, *auth.Service, *csrfSigner, http.Handler, string) {
	t.Helper()
	db := openAdminUsersRBACTestDB(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		db.Close()
		t.Fatalf("hash password: %v", err)
	}
	if _, err := db.Exec(`UPDATE users SET password_hash = ? WHERE id = 'admin-user'`, string(hash)); err != nil {
		db.Close()
		t.Fatalf("update password hash: %v", err)
	}
	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{11}, 32), ttl: time.Hour}
	tpl, err := newAppTemplateEngine(false, buildFuncMap())
	if err != nil {
		db.Close()
		t.Fatalf("parse templates: %v", err)
	}
	handler := adminReauthRoute(authService, signer, tpl, newMemoryRateLimiter(), rateLimitPolicy{Limit: 5, Window: 10 * time.Minute})
	token, _, err := authService.CreateSession("admin-user", "workspace-rbac", time.Hour, false)
	if err != nil {
		db.Close()
		t.Fatalf("create admin session: %v", err)
	}
	return db, authService, signer, handler, token
}

func extractAdminReauthCSRFToken(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 || strings.TrimSpace(match[1]) == "" {
		t.Fatalf("CSRF token missing from reauthentication page: %q", body)
	}
	return match[1]
}

func responseCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response cookie %q not found", name)
	return nil
}
