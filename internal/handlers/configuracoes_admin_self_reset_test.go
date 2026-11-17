package handlers

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/httpcookies"
	"github.com/contabase-app/contabase/internal/models"
)

type adminSelfResetTestTemplates struct{}

func (adminSelfResetTestTemplates) ExecuteTemplate(w io.Writer, name string, data any) error {
	payload, ok := data.(adminSelfPasswordResetData)
	if !ok {
		return fmt.Errorf("unexpected self reset template data %T", data)
	}
	_, err := fmt.Fprintf(w, "%s|%s|%s", name, payload.Email, payload.TemporaryPassword)
	return err
}

func (adminSelfResetTestTemplates) Lookup(name string) *template.Template {
	return template.New(name)
}

func TestAdminSelfPasswordResetTemplateIsMinimalAndOneTime(t *testing.T) {
	body := readTestFile(t, "templates/pages/configuracoes_admin_self_reset.html")
	for _, want := range []string{
		`{{define "configuracoes-admin-self-reset-content"}}`,
		`{{define "configuracoes-admin-self-reset-page"}}`,
		`id="settings-dynamic-payload"`,
		`{{.TemporaryPassword}}`,
		`href="/login"`,
		`Expira em 1 hora.`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("self reset template missing %q", want)
		}
	}
	for _, forbidden := range []string{"AdminUsers", "admin-user-card", "admin-recovery"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("self reset template must not render the authenticated admin UI: %q", forbidden)
		}
	}
}

func TestHandleAdminUsersResetPasswordSelfShowsOneTimeCredentialAndLogsOut(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)
	seedTargetAuthState(t, db, "admin-actor")
	seedTargetAuthState(t, db, "target-user")

	handler := ConfiguracoesHandler{
		DB:           db,
		Templates:    adminSelfResetTestTemplates{},
		WorkspaceID:  "workspace-a",
		UserID:       "admin-actor",
		ActorRole:    models.RoleAdmin,
		SecureCookie: true,
	}
	form := url.Values{}
	form.Set("user_id", "admin-actor")
	form.Set("confirm_email", "admin-actor@example.com")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/reset-senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersResetPassword(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	parts := strings.SplitN(rr.Body.String(), "|", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected self reset body %q", rr.Body.String())
	}
	name, email, temporaryPassword := parts[0], parts[1], parts[2]
	if name != "configuracoes-admin-self-reset-content" {
		t.Fatalf("template = %q, want fragment", name)
	}
	if email != "admin-actor@example.com" {
		t.Fatalf("email = %q", email)
	}
	if strings.TrimSpace(temporaryPassword) == "" {
		t.Fatal("temporary password was not returned once")
	}
	assertSessionCookieCleared(t, rr, httpcookies.Session)
	assertSessionCookieCleared(t, rr, httpcookies.PreAuth)

	assertAdminUserAuthStateRevoked(t, db, "admin-actor")
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, "target-user", 1)
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`, "target-user", 1)
	assertAdminUserTemporaryPasswordGenerated(t, db, "admin-actor")
	assertAdminUserPasswordHash(t, db, "target-user", "old-hash")
	assertAdminUserAuditEvent(t, db, "admin-actor", "ADMIN_USER_TEMPORARY_PASSWORD_RESET")
}

func TestHandleAdminUsersResetPasswordSelfTraditionalNavigationRendersPage(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)

	handler := ConfiguracoesHandler{
		DB:           db,
		Templates:    adminSelfResetTestTemplates{},
		WorkspaceID:  "workspace-a",
		UserID:       "admin-actor",
		ActorRole:    models.RoleAdmin,
		SecureCookie: false,
	}
	form := url.Values{}
	form.Set("user_id", "admin-actor")
	form.Set("confirm_email", "admin-actor@example.com")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/reset-senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersResetPassword(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	parts := strings.SplitN(rr.Body.String(), "|", 3)
	if len(parts) != 3 || parts[0] != "configuracoes-admin-self-reset-page" || strings.TrimSpace(parts[2]) == "" {
		t.Fatalf("unexpected traditional self reset body %q", rr.Body.String())
	}
	assertSessionCookieClearedWithoutSecureCheck(t, rr, httpcookies.Session)
	assertSessionCookieClearedWithoutSecureCheck(t, rr, httpcookies.PreAuth)
}

func TestHandleAdminUsersResetPasswordOtherUserPreservesActorSession(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)
	seedTargetAuthState(t, db, "admin-actor")
	seedTargetAuthState(t, db, "target-user")

	handler := ConfiguracoesHandler{
		DB:          db,
		Templates:   adminUsersRecoveryTestTemplates{},
		WorkspaceID: "workspace-a",
		UserID:      "admin-actor",
		ActorRole:   models.RoleAdmin,
	}
	form := url.Values{}
	form.Set("user_id", "target-user")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/reset-senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersResetPassword(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Body.String(); got != "ok" {
		t.Fatalf("body = %q, want normal admin section", got)
	}
	assertAdminUserAuthStateRevoked(t, db, "target-user")
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, "admin-actor", 1)
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`, "admin-actor", 1)
	assertAdminUserPasswordHash(t, db, "admin-actor", "old-hash")
	assertAdminUserTemporaryPasswordGenerated(t, db, "target-user")
	assertAdminUserAuditEvent(t, db, "target-user", "ADMIN_USER_TEMPORARY_PASSWORD_RESET")
}

func assertSessionCookieClearedWithoutSecureCheck(t *testing.T, rr *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name != name {
			continue
		}
		if cookie.Value != "" || cookie.MaxAge >= 0 || !cookie.HttpOnly {
			t.Fatalf("%s cookie was not cleared correctly: %+v", name, cookie)
		}
		return
	}
	t.Fatalf("%s cookie was not emitted", name)
}
