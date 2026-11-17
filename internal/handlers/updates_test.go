package handlers

import (
	"bytes"
	"context"
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/updates"
)

func updateTemplates(t *testing.T) *template.Template {
	t.Helper()
	tpl := testConfiguracoesRenderTemplates(t)
	body, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/configuracoes_admin_updates.html"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Parse(string(body)); err != nil {
		t.Fatal(err)
	}
	return tpl
}

func updateResult(method, status string) updates.Result {
	r := updates.Result{
		Status:   status,
		Identity: updates.Identity{Version: "v1.0.0-beta.8", Channel: "beta", Method: method},
	}
	if status == updates.Current || status == updates.Newer {
		r.Latest = "v1.0.0-beta.9"
		r.NotesURL = "https://github.com/contabase-app/contabase/releases/tag/v1.0.0-beta.9"
		r.CheckedAt = time.Date(2026, 11, 3, 12, 30, 0, 0, time.UTC)
	}
	return r
}

func renderUpdatePanel(t *testing.T, tpl *template.Template, r updates.Result) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "configuracoes-admin_updates-panel", ConfiguracoesData{
		Updates:               r,
		UpdatesCSRFToken:      "synthetic-csrf",
		UpdatesLastCheck:      FormatAppTime(r.CheckedAt),
		UpdatesHealthcheckURL: updateHealthcheckURL("http://127.0.0.1:8080"),
	}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestAdminUpdatesRejectsRolesWithoutDatabaseOrNetwork(t *testing.T) {
	for _, role := range []string{"USER", "MANAGER", "", "SUPERUSER"} {
		for _, path := range []string{"/admin/atualizacoes", "/admin/atualizacoes/status", "/admin/atualizacoes/verificar"} {
			h := ConfiguracoesHandler{ActorRole: role}
			method := http.MethodGet
			if strings.HasSuffix(path, "verificar") {
				method = http.MethodPost
			}
			rr := httptest.NewRecorder()
			h.HandleAdminUpdates(rr, httptest.NewRequest(method, path, nil))
			if rr.Code != http.StatusForbidden || strings.Contains(rr.Body.String(), "Versão instalada") {
				t.Fatalf("role %s %s: %d %s", role, path, rr.Code, rr.Body.String())
			}
		}
	}
}

func TestAdminUpdatesHTMXWorksWithoutDatabase(t *testing.T) {
	h := ConfiguracoesHandler{ActorRole: "ADMIN", Templates: updateTemplates(t)}
	for _, path := range []string{"/admin/atualizacoes", "/admin/atualizacoes/status", "/admin/atualizacoes/verificar"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "verificar") {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, path+"?channel=stable&url=http://127.0.0.1", nil)
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()
		h.HandleAdminUpdates(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		assertContains(t, rr.Body.String(), "Canal não identificado")
		assertContains(t, rr.Body.String(), "Método não identificado")
		assertContains(t, rr.Body.String(), "adminUpdatesIdentifyCommand")
		if strings.Contains(rr.Body.String(), "127.0.0.1") || strings.Contains(rr.Body.String(), "<html") {
			t.Fatal("fragment leaked request input or document wrapper")
		}
	}
}

func TestAdminUpdatesFullPageAndNativeRefresh(t *testing.T) {
	h := newConfiguracoesRenderTestHandler(t)
	h.Templates = updateTemplates(t)
	h.UpdatesCSRFToken = "synthetic-csrf"
	rr := httptest.NewRecorder()
	h.HandleAdminUpdates(rr, httptest.NewRequest(http.MethodGet, "/admin/atualizacoes", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	for _, expected := range []string{
		`id="main-content"`, `cb-v2-admin-main`, `admin-premium.css`, `Atualizações`,
		`value="synthetic-csrf"`, `aria-busy="false"`, `admin-updates.js`,
		`Como atualizar`, `Faça backup antes de atualizar`, `Abrir Backups`,
		`adminUpdatesIdentifyCommand`, `Docker Compose manual (GHCR)`,
	} {
		assertContains(t, rr.Body.String(), expected)
	}
	rr = httptest.NewRecorder()
	h.HandleAdminUpdates(rr, httptest.NewRequest(http.MethodPost, "/admin/atualizacoes/verificar", nil))
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/admin/atualizacoes" {
		t.Fatal("native refresh did not redirect")
	}
	for _, path := range []string{"/admin/atualizacoes", "/admin/atualizacoes/status", "/admin/atualizacoes/verificar"} {
		rr = httptest.NewRecorder()
		h.HandleAdminUpdates(rr, httptest.NewRequest(http.MethodDelete, path, nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatal("unexpected method accepted")
		}
	}
}

func TestAdminUpdatesRenderMethodMatrix(t *testing.T) {
	tpl := updateTemplates(t)
	for _, tc := range []struct {
		name        string
		method      string
		status      string
		wantCommand string
		wantCopy    bool
		want        []string
		notWant     []string
	}{
		{
			name: "release-newer", method: "release", status: updates.Newer,
			wantCommand: "sudo contabase-update", wantCopy: true,
			want: []string{"Release / systemd", "Atualização suportada", "Nova versão disponível", "Execute no host", "systemctl status contabase", "curl 'http://127.0.0.1:8080/health'"},
		},
		{
			name: "release-current", method: "release", status: updates.Current,
			wantCommand: "sudo contabase-update", wantCopy: true,
			want: []string{"Você está na versão publicada", "não fará downgrade", "Release / systemd"},
		},
		{
			name: "release-failed", method: "release", status: updates.Failed,
			want:    []string{"Não foi possível verificar", "Tente novamente antes de copiar", "Release / systemd"},
			notWant: []string{"adminUpdatesPrimaryCommand", "data-copy-target"},
		},
		{
			name: "release-empty", method: "release", status: updates.Empty,
			want:    []string{"Nenhuma versão publicada no canal", "Nenhuma versão foi publicada no canal"},
			notWant: []string{"adminUpdatesPrimaryCommand", "data-copy-target"},
		},
		{
			name: "release-checking", method: "release", status: updates.Checking,
			want:    []string{"Verificando versões", "consulta de versões ainda está em andamento", `hx-get="/admin/atualizacoes/status"`},
			notWant: []string{"adminUpdatesPrimaryCommand", "data-copy-target"},
		},
		{
			name: "source-newer", method: "source", status: updates.Newer,
			wantCommand: "sudo contabase-update", wantCopy: true,
			want: []string{"Source legado", "checkout Git persistente", "branch com upstream", "git status -sb", "Não use este procedimento em instalações Source Bootstrap", "curl 'http://127.0.0.1:8080/health'"},
		},
		{
			name: "docker-newer", method: "docker", status: updates.Newer,
			wantCommand: "contabase-update", wantCopy: true,
			want:    []string{"Docker Compose do instalador", "não existe dentro do contêiner", "docker compose ps", "docker compose logs --tail 100 contabase", "curl 'http://127.0.0.1:8080/health'"},
			notWant: []string{"Docker Compose manual (GHCR)", "docker compose pull"},
		},
		{
			name: "docker-image-newer", method: "docker-image", status: updates.Newer,
			want:    []string{"Docker por imagem/digest", "Em validação", "não está declarado pronto para produção", "Não há comando recomendado", "pode falhar fechado"},
			notWant: []string{"adminUpdatesPrimaryCommand", "data-copy-target"},
		},
		{
			name: "source-bootstrap-newer", method: "source-bootstrap", status: updates.Newer,
			want:    []string{"Source Bootstrap", "Não suportada", "Não execute", "não possui atualização suportada"},
			notWant: []string{"adminUpdatesPrimaryCommand", "data-copy-target"},
		},
		{
			name: "unknown-newer", method: "", status: updates.Newer,
			want:    []string{"Método não identificado", "adminUpdatesIdentifyCommand", "Docker Compose manual (GHCR)", "imagem com tag ou digest fixo", "docker compose pull", "docker compose up -d"},
			notWant: []string{"adminUpdatesPrimaryCommand"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := updateResult(tc.method, tc.status)
			body := renderUpdatePanel(t, tpl, r)
			for _, expected := range tc.want {
				assertContains(t, body, expected)
			}
			for _, unexpected := range tc.notWant {
				if strings.Contains(body, unexpected) {
					t.Fatalf("body unexpectedly contains %q", unexpected)
				}
			}
			if tc.wantCopy {
				assertContains(t, body, `id="adminUpdatesPrimaryCommand">`+tc.wantCommand+`</code>`)
				assertContains(t, body, `data-copy-target="adminUpdatesPrimaryCommand"`)
				assertContains(t, body, `data-copy-status`)
			} else if strings.Contains(body, `id="adminUpdatesPrimaryCommand"`) {
				t.Fatal("primary update command rendered without a completed supported consultation")
			}
			assertContains(t, body, "Esta página apenas consulta publicações. Nenhuma atualização é executada aqui.")
			if strings.Contains(body, `hx-get="/admin/atualizacoes/status"`) != (tc.status == updates.Checking) {
				t.Fatal("polling must be limited to the checking state")
			}
		})
	}
}

func TestAdminUpdatesReleaseLinkIntegrity(t *testing.T) {
	tpl := updateTemplates(t)
	r := updateResult("release", updates.Newer)
	body := renderUpdatePanel(t, tpl, r)
	for _, expected := range []string{
		`href="https://github.com/contabase-app/contabase/releases/tag/v1.0.0-beta.9"`,
		`target="_blank"`,
		`rel="noopener noreferrer"`,
		`referrerpolicy="no-referrer"`,
		`(abre em nova aba)</span>`,
	} {
		assertContains(t, body, expected)
	}
	r.NotesURL = ""
	body = renderUpdatePanel(t, tpl, r)
	if strings.Contains(body, "Ver novidades") {
		t.Fatal("release link visible without a validated URL")
	}
}

func TestAdminUpdatesExternalCopyScriptAndNoInlineHandlers(t *testing.T) {
	body, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/configuracoes_admin_updates.html"))
	if err != nil {
		t.Fatal(err)
	}
	templateSource := string(body)
	assertContains(t, templateSource, `assetPath "/assets/js/admin-updates.js"`)
	for _, forbidden := range []string{"onclick=", "onchange=", "javascript:", "<script>"} {
		if strings.Contains(templateSource, forbidden) {
			t.Fatalf("template contains forbidden inline handler or script: %q", forbidden)
		}
	}
}

func TestAdminUpdatesHealthcheckUsesConfiguredBaseURL(t *testing.T) {
	tpl := updateTemplates(t)
	const fallback = "http://127.0.0.1:8080/health"
	for _, tc := range []struct {
		name string
		base string
		want string
	}{
		{"localhost", "http://localhost:8080", "http://localhost:8080/health"},
		{"loopback", "http://127.0.0.1:8080", fallback},
		{"root-path", "http://127.0.0.1:8080/", fallback},
		{"lan-ip", "http://192.168.1.50:8080", "http://192.168.1.50:8080/health"},
		{"lan-host", "http://contabase.lan:8080", "http://contabase.lan:8080/health"},
		{"single-label", "http://contabase", "http://contabase/health"},
		{"hyphen-digit-host", "http://contabase-2.lan", "http://contabase-2.lan/health"},
		{"absolute-dns-name", "https://financeiro.example.com.", "https://financeiro.example.com./health"},
		{"proxy", "https://financeiro.example.com", "https://financeiro.example.com/health"},
		{"proxy-root", "https://financeiro.example.com/", "https://financeiro.example.com/health"},
		{"ipv6", "http://[::1]", "http://[::1]/health"},
		{"ipv6-lan-port", "http://[fd00::50]:8080", "http://[fd00::50]:8080/health"},
		{"ipv6-mapped", "http://[::ffff:192.168.1.50]:8080/", "http://[::ffff:192.168.1.50]:8080/health"},
		{"minimum-port", "https://example.com:1", "https://example.com:1/health"},
		{"maximum-port", "https://example.com:65535", "https://example.com:65535/health"},
		{"empty", "", fallback},
		{"command-substitution", "http://$(true).example", fallback},
		{"probe-substitution", "http://$(healthcheck_probe).example", fallback},
		{"backticks", "http://`healthcheck_probe`.example", fallback},
		{"single-quote", "http://example'host", fallback},
		{"double-quote", "http://example\"host", fallback},
		{"semicolon", "http://example.com;healthcheck_probe", fallback},
		{"ampersand", "http://example.com&healthcheck_probe", fallback},
		{"pipe", "http://example.com|healthcheck_probe", fallback},
		{"redirect", "http://example.com>probe", fallback},
		{"glob", "http://*.example.com", fallback},
		{"backslash", "http://example.com\\probe", fallback},
		{"empty-host", "http://", fallback},
		{"port-only", "http://:8080", fallback},
		{"empty-port", "http://example.com:", fallback},
		{"empty-ipv6-port", "http://[::1]:", fallback},
		{"zero-port", "http://example.com:0", fallback},
		{"negative-port", "http://example.com:-1", fallback},
		{"nonnumeric-port", "http://example.com:abc", fallback},
		{"port-out-of-range", "http://example.com:65536", fallback},
		{"large-port", "http://example.com:99999", fallback},
		{"overflow-port", "http://example.com:9999999999999999999999", fallback},
		{"empty-query", "https://example.com?", fallback},
		{"empty-fragment", "https://example.com#", fallback},
		{"query", "https://example.com?token=synthetic", fallback},
		{"fragment", "https://example.com#probe", fallback},
		{"credentials", "https://user:synthetic@example.com", fallback},
		{"empty-userinfo", "https://@example.com", fallback},
		{"path", "https://example.com/admin", fallback},
		{"encoded-root", "https://example.com/%2f", fallback},
		{"scheme", "javascript:alert(1)", fallback},
		{"underscore", "http://invalid_host.lan", fallback},
		{"empty-label", "http://invalid..lan", fallback},
		{"leading-hyphen", "http://-invalid.lan", fallback},
		{"trailing-hyphen", "http://invalid-.lan", fallback},
		{"long-label", "http://" + strings.Repeat("a", 64) + ".lan", fallback},
		{"long-host", "http://" + strings.Repeat(strings.Repeat("a", 63)+".", 4) + "lan", fallback},
		{"invalid-ipv4", "http://999.168.1.50", fallback},
		{"leading-zero-ipv4", "http://192.168.001.50", fallback},
		{"bracketed-ipv4", "http://[127.0.0.1]", fallback},
		{"unbracketed-ipv6", "http://fd00::50", fallback},
		{"invalid-ipv6", "http://[not-ip]", fallback},
		{"ipv6-zone", "http://[fe80::1%25eth0]", fallback},
		{"newline", "http://example.com\n", fallback},
		{"carriage-return", "http://example.com\r", fallback},
		{"tab", "\thttp://example.com", fallback},
		{"nul", "http://example.com\x00", fallback},
		{"delete", "http://example.com\x7f", fallback},
		{"unicode-control", "http://example.com\u0085", fallback},
		{"space", "http://example.com ", fallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateHealthcheckURL(tc.base); got != tc.want {
				t.Fatalf("healthcheck URL = %q, want %q", got, tc.want)
			}
			for _, method := range []string{"release", "source", "docker"} {
				t.Run(method, func(t *testing.T) {
					var buf bytes.Buffer
					r := updateResult(method, updates.Newer)
					if err := tpl.ExecuteTemplate(&buf, "configuracoes-admin_updates-panel", ConfiguracoesData{
						Updates: r, UpdatesHealthcheckURL: updateHealthcheckURL(tc.base),
						UpdatesLastCheck: FormatAppTime(r.CheckedAt),
					}); err != nil {
						t.Fatal(err)
					}
					_, rest, found := strings.Cut(buf.String(), "<code>curl ")
					text, _, closed := strings.Cut(rest, "</code>")
					if !found || !closed {
						t.Fatal("rendered healthcheck command missing")
					}
					command := html.UnescapeString("curl " + text)
					if want := "curl '" + tc.want + "'"; command != want {
						t.Fatalf("copied command = %q, want %q", command, want)
					}
					assertUpdateHealthcheckShellArgument(t, command, tc.want)
				})
			}
		})
	}
}

func assertUpdateHealthcheckShellArgument(t *testing.T, command, wantURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Intercept curl locally: no network, and a probe detects shell expansion.
	script := "healthcheck_probe() { printf UNEXPECTED_EXPANSION >&2; }\n" +
		"curl() { printf '%s\\n' \"$#\" \"$@\"; }\n" + command
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.Env = []string{"PATH=/nonexistent", "LC_ALL=C"}
	cmd.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("shell fixture: %v: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "1\n"+wantURL+"\n"; got != want || stderr.Len() != 0 {
		t.Fatalf("shell argument changed: stdout=%q stderr=%q, want=%q", got, stderr.String(), want)
	}
}

func TestAdminUpdatesUsesSystemTimezone(t *testing.T) {
	for _, tc := range []struct{ appTZ, tz, want string }{
		{"America/Sao_Paulo", "UTC", "America/Sao_Paulo"},
		{"UTC", "America/Sao_Paulo", "UTC"},
		{"", "America/New_York", "America/New_York"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Setenv("CONTABASE_TIMEZONE", tc.appTZ)
			t.Setenv("TZ", tc.tz)
			h := ConfiguracoesHandler{ActorRole: "ADMIN", Templates: updateTemplates(t)}
			rr := httptest.NewRecorder()
			h.HandleAdminUpdates(rr, httptest.NewRequest(http.MethodGet, "/admin/atualizacoes/status", nil))
			if rr.Code != http.StatusOK {
				t.Fatal(rr.Code)
			}
			assertContains(t, rr.Body.String(), "Última consulta ("+tc.want+")")
		})
	}
}
