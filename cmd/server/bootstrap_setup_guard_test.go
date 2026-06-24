package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testBootstrapSetupToken = "0123456789abcdef0123456789abcdef"

type setupGuardTemplateEngine struct{}

func (setupGuardTemplateEngine) ExecuteTemplate(w io.Writer, name string, data any) error {
	if name != "setup-page" {
		return fmt.Errorf("unexpected template %q", name)
	}
	v := data.(struct {
		Error     string
		CSRFToken string
	})
	_, err := fmt.Fprintf(w, "error=%s csrf=%s", v.Error, v.CSRFToken)
	return err
}

func (setupGuardTemplateEngine) Lookup(name string) *template.Template {
	return nil
}

func TestBootstrapSetupGuardBlocksMissingAndInvalidToken(t *testing.T) {
	guard := newBootstrapSetupGuard(testBootstrapSetupToken)

	missing := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(url.Values{}.Encode()))
	missing.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if guard.Allow(missing) {
		t.Fatalf("expected missing setup token to be blocked")
	}

	form := url.Values{}
	form.Set("setup_token", "invalid-token")
	invalid := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	invalid.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if guard.Allow(invalid) {
		t.Fatalf("expected invalid setup token to be blocked")
	}
}

func TestBootstrapSetupGuardBlocksWhenServerTokenIsMissingOrWeak(t *testing.T) {
	for _, token := range []string{"", "short-token"} {
		t.Run(token, func(t *testing.T) {
			guard := newBootstrapSetupGuard(token)
			form := url.Values{}
			form.Set("setup_token", testBootstrapSetupToken)
			req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if guard.Allow(req) {
				t.Fatalf("expected unusable server setup token %q to block bootstrap mutation", token)
			}
		})
	}
}

func TestBootstrapSetupGuardAllowsValidFormTokenOnce(t *testing.T) {
	guard := newBootstrapSetupGuard(testBootstrapSetupToken)
	form := url.Values{}
	form.Set("setup_token", testBootstrapSetupToken)
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if !guard.Allow(req) {
		t.Fatalf("expected valid setup token to be allowed")
	}
	guard.Consume()

	reqAfterConsume := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	reqAfterConsume.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if guard.Allow(reqAfterConsume) {
		t.Fatalf("expected consumed setup token to be blocked")
	}
}

func TestBootstrapSetupGuardAllowsValidHeaderToken(t *testing.T) {
	guard := newBootstrapSetupGuard(testBootstrapSetupToken)
	req := httptest.NewRequest(http.MethodPost, "/setup/restaurar", nil)
	req.Header.Set("X-ContaBase-Setup-Token", testBootstrapSetupToken)

	if !guard.Allow(req) {
		t.Fatalf("expected valid setup token header to be allowed")
	}
}

func TestRequireBootstrapSetupTokenReturnsSafeForbidden(t *testing.T) {
	guard := newBootstrapSetupGuard(testBootstrapSetupToken)
	form := url.Values{}
	form.Set("setup_token", "wrong-token-value")
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	if requireBootstrapSetupToken(rr, req, setupGuardTemplateEngine{}, "csrf-token", guard) {
		t.Fatalf("expected invalid token to block request")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
	body := rr.Body.String()
	for _, forbidden := range []string{testBootstrapSetupToken, "wrong-token-value", bootstrapSetupTokenEnv, "/Users/", "panic", "stack"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("forbidden response leaked %q in body %q", forbidden, body)
		}
	}
	if !strings.Contains(body, "Token local de setup inválido ou ausente.") {
		t.Fatalf("expected generic setup token error, got %q", body)
	}
}

func TestSetupTokenStillConfiguredNoticeRequiresTokenAndAdminRole(t *testing.T) {
	adminData := struct {
		ActorRole string
	}{ActorRole: "ADMIN"}
	userData := struct {
		ActorRole string
	}{ActorRole: "USER"}
	localizedAdminData := struct {
		UserRole string
	}{UserRole: "Administrador"}

	if shouldShowSetupTokenNotice("", adminData) {
		t.Fatalf("expected missing setup token env to hide notice")
	}
	if shouldShowSetupTokenNotice("   ", adminData) {
		t.Fatalf("expected blank setup token env to hide notice")
	}
	if shouldShowSetupTokenNotice(testBootstrapSetupToken, userData) {
		t.Fatalf("expected non-admin role to hide notice")
	}
	if !shouldShowSetupTokenNotice(testBootstrapSetupToken, adminData) {
		t.Fatalf("expected admin role with configured setup token to show notice")
	}
	if !shouldShowSetupTokenNotice(testBootstrapSetupToken, localizedAdminData) {
		t.Fatalf("expected localized admin label with configured setup token to show notice")
	}
}

func TestLayoutSetupTokenNoticeDoesNotRenderTokenValue(t *testing.T) {
	t.Setenv(bootstrapSetupTokenEnv, testBootstrapSetupToken)

	tpl := template.Must(template.New("layout-test").Funcs(buildFuncMap()).ParseFiles("../../templates/layout.html"))
	data := struct {
		Title     string
		ActorRole string
	}{
		Title:     "Dashboard",
		ActorRole: "ADMIN",
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "layout-start", data); err != nil {
		t.Fatalf("execute layout-start: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "token local de setup ainda está ativo") {
		t.Fatalf("expected setup token notice to render")
	}
	if !strings.Contains(body, "docker compose up -d --build") {
		t.Fatalf("expected docker restart command to render")
	}
	if !strings.Contains(body, "sudo systemctl restart contabase") {
		t.Fatalf("expected binary/systemd restart command to render")
	}
	if strings.Contains(body, testBootstrapSetupToken) {
		t.Fatalf("setup token value leaked in rendered layout")
	}
}
