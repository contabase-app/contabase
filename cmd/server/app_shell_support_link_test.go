package main

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/support"
)

func renderSupportFooter(t *testing.T) string {
	t.Helper()
	tpl := template.Must(template.New("app-shell-footer-test").Funcs(buildFuncMap()).
		ParseFiles("../../templates/components/app_shell.html"))

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "app-v2-footer", nil); err != nil {
		t.Fatalf("execute app shell footer: %v", err)
	}
	return out.String()
}

func TestAppShellFooterRendersSupportLinkByDefault(t *testing.T) {
	t.Setenv(support.EnvURL, "")
	body := renderSupportFooter(t)

	if !strings.Contains(body, "Apoiar o Projeto") {
		t.Fatalf("footer must render the support link without configuration:\n%s", body)
	}
	if !strings.Contains(body, `href="https://contabase.pages.dev/apoiar/"`) {
		t.Fatalf("support link must point at the default destination:\n%s", body)
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Fatalf("support link must open outside the application:\n%s", body)
	}
	if !strings.Contains(body, `rel="noopener noreferrer"`) {
		t.Fatalf("support link must isolate the opened document:\n%s", body)
	}
	if !strings.Contains(body, `href="https://github.com/contabase-app/contabase"`) {
		t.Fatalf("footer must keep the existing GitHub link:\n%s", body)
	}
	if !strings.Contains(body, "Versão") {
		t.Fatalf("footer must keep the version line:\n%s", body)
	}
}

func TestAppShellFooterRendersConfiguredSupportLink(t *testing.T) {
	t.Setenv(support.EnvURL, "https://apoio.example.org/apoiar/")
	body := renderSupportFooter(t)

	if !strings.Contains(body, "Apoiar o Projeto") {
		t.Fatalf("footer must render the support link:\n%s", body)
	}
	if !strings.Contains(body, `href="https://apoio.example.org/apoiar/"`) {
		t.Fatalf("support link must point at the configured destination:\n%s", body)
	}
	if strings.Contains(body, `href="https://contabase.pages.dev/apoiar/"`) {
		t.Fatalf("configured destination must replace the default:\n%s", body)
	}
}

func TestAppShellFooterIgnoresInvalidSupportDestination(t *testing.T) {
	for _, raw := range []string{
		"javascript:alert(1)",
		"https://contabase.pages.dev/setup/",
		"http://contabase.pages.dev/apoiar/",
		"https://user:secret@contabase.pages.dev/apoiar/",
	} {
		t.Setenv(support.EnvURL, raw)
		body := renderSupportFooter(t)
		if !strings.Contains(body, `href="https://contabase.pages.dev/apoiar/"`) {
			t.Errorf("%q must be ignored in favor of the default destination:\n%s", raw, body)
		}
		if strings.Contains(body, raw) {
			t.Errorf("%q must never reach the footer:\n%s", raw, body)
		}
	}
}
