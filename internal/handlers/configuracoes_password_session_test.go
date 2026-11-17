package handlers

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/httpcookies"

	"golang.org/x/crypto/bcrypt"
)

type passwordChangeTestTemplates struct{}

func (passwordChangeTestTemplates) ExecuteTemplate(w io.Writer, name string, data any) error {
	return nil
}

func (passwordChangeTestTemplates) Lookup(name string) *template.Template {
	return template.New(name)
}

type passwordChangeFixture struct {
	db             *sql.DB
	service        *auth.Service
	handler        ConfiguracoesHandler
	currentToken   string
	otherToken     string
	preAuthToken   string
	otherUserToken string
	userID         string
	otherUserID    string
}

func setupPasswordChangeFixture(t *testing.T) passwordChangeFixture {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().Unix()
	userID := "password-user"
	otherUserID := "password-other-user"
	hash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	otherHash, err := bcrypt.GenerateFromPassword([]byte("other-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash other password: %v", err)
	}
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES
			(?, 'Password User', 'password-user@example.com', ?, 'active', ?, ?),
			(?, 'Other User', 'password-other@example.com', ?, 'active', ?, ?)
	`, userID, string(hash), now, now, otherUserID, string(otherHash), now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('password-workspace', 'Password Workspace', '', 'personal', ?, ?),
			('password-other-workspace', 'Other Workspace', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('password-workspace', ?, 'ADMIN', ?),
			('password-other-workspace', ?, 'ADMIN', ?)
	`, userID, now, otherUserID, now)

	service := auth.NewService(db)
	currentToken, _, err := service.CreateSession(userID, "password-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	otherToken, _, err := service.CreateSession(userID, "password-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}
	preAuthToken, _, err := service.CreatePreAuthSession(userID, "TOTP", time.Minute, false)
	if err != nil {
		t.Fatalf("create pre-auth session: %v", err)
	}
	otherUserToken, _, err := service.CreateSession(otherUserID, "password-other-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create other user session: %v", err)
	}

	return passwordChangeFixture{
		db:             db,
		service:        service,
		handler:        ConfiguracoesHandler{DB: db, Templates: passwordChangeTestTemplates{}, AuthService: service, UserID: userID, SessionToken: currentToken, SecureCookie: true},
		currentToken:   currentToken,
		otherToken:     otherToken,
		preAuthToken:   preAuthToken,
		otherUserToken: otherUserToken,
		userID:         userID,
		otherUserID:    otherUserID,
	}
}

func TestHandlePasswordChangeRevokesEverySessionAndClearsCookies(t *testing.T) {
	fixture := setupPasswordChangeFixture(t)

	form := url.Values{}
	form.Set("current_password", "current-password")
	form.Set("new_password", "new-password-123")
	form.Set("confirm_password", "new-password-123")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/perfil/senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: httpcookies.Session, Value: fixture.currentToken})
	rr := httptest.NewRecorder()

	fixture.handler.HandlePasswordChange(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Redirect"); got != "/login" {
		t.Fatalf("HX-Redirect = %q, want /login", got)
	}
	if got := rr.Header().Get("HX-Trigger"); !strings.Contains(got, "contabase:logout") {
		t.Fatalf("HX-Trigger = %q, want contabase:logout", got)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("password change must not return stale authenticated UI: %q", rr.Body.String())
	}
	assertSessionCookieCleared(t, rr, httpcookies.Session)
	assertSessionCookieCleared(t, rr, httpcookies.PreAuth)

	if _, _, _, _, err := fixture.service.ResolveAndRefreshSession(fixture.currentToken); err == nil {
		t.Fatal("current session survived password change")
	}
	if _, _, _, _, err := fixture.service.ResolveAndRefreshSession(fixture.otherToken); err == nil {
		t.Fatal("other session survived password change")
	}
	if _, _, _, err := fixture.service.ResolvePreAuthSession(fixture.preAuthToken); err == nil {
		t.Fatal("pre-auth session survived password change")
	}
	if _, _, _, _, err := fixture.service.ResolveAndRefreshSession(fixture.otherUserToken); err != nil {
		t.Fatalf("another user's session was revoked: %v", err)
	}
	if _, err := fixture.service.Authenticate("password-user@example.com", "new-password-123"); err != nil {
		t.Fatalf("new password did not authenticate: %v", err)
	}
	if _, err := fixture.service.Authenticate("password-user@example.com", "current-password"); err == nil {
		t.Fatal("old password still authenticated")
	}
}

func TestHandlePasswordChangeTraditionalNavigationClearsCookie(t *testing.T) {
	fixture := setupPasswordChangeFixture(t)

	form := url.Values{}
	form.Set("current_password", "current-password")
	form.Set("new_password", "new-password-123")
	form.Set("confirm_password", "new-password-123")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/perfil/senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: httpcookies.Session, Value: fixture.currentToken})
	rr := httptest.NewRecorder()

	fixture.handler.HandlePasswordChange(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want /login", got)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("password change must not return stale authenticated UI: %q", rr.Body.String())
	}
	assertSessionCookieCleared(t, rr, httpcookies.Session)
	assertSessionCookieCleared(t, rr, httpcookies.PreAuth)
	if _, _, _, _, err := fixture.service.ResolveAndRefreshSession(fixture.currentToken); err == nil {
		t.Fatal("current session survived password change")
	}
}

func assertSessionCookieCleared(t *testing.T, rr *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name != name {
			continue
		}
		if cookie.Value != "" || cookie.MaxAge >= 0 || !cookie.HttpOnly {
			t.Fatalf("%s cookie was not cleared correctly: %+v", name, cookie)
		}
		if name == httpcookies.Session && !cookie.Secure {
			t.Fatalf("%s cookie lost the Secure policy", name)
		}
		return
	}
	t.Fatalf("%s cookie was not emitted", name)
}
