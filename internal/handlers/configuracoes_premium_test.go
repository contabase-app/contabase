package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CFG2/ADM3 — Fundação Premium compartilhada, envelopes separados.
//
// Configurações pessoais/corporativas e Administração compartilham premium.css,
// mas cada uma mantém seu próprio envelope. A camada administrativa
// (admin-premium.css) não pode vazar para Configurações, Perfil ou App Shell.

func TestConfiguracoesPremiumFoundationIsScopedFromAdministration(t *testing.T) {
	settingsSections := []string{"perfil", "workspace", "categorias", "contas", "cartoes"}
	for _, section := range settingsSections {
		t.Run(section, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)
			req := httptest.NewRequest(http.MethodGet, "/configuracoes/"+section, nil)
			rr := httptest.NewRecorder()
			handler.HandleConfiguracoesSection(rr, req, section)
			body := rr.Body.String()

			for _, want := range []string{
				`/assets/css/premium.css?v=test`,
				`data-ui="premium"`,
				`ui-page-header`,
				`ui-page-title`,
				`ui-page-description`,
			} {
				assertContains(t, body, want)
			}
			if strings.Index(body, "premium.css") > strings.Index(body, "configuracoes.css") {
				t.Fatal("premium.css must load before configuracoes.css")
			}
			if strings.Contains(body, "admin-premium.css") {
				t.Fatalf("%s must not load the administration layer", section)
			}
		})
	}

	hubReq := httptest.NewRequest(http.MethodGet, "/configuracoes", nil)
	hubRR := httptest.NewRecorder()
	handler := newConfiguracoesRenderTestHandler(t)
	handler.HandleConfiguracoesConceito(hubRR, hubReq)
	assertContains(t, hubRR.Body.String(), `/assets/css/premium.css?v=test`)
	assertContains(t, hubRR.Body.String(), `data-ui="premium"`)
	assertContains(t, hubRR.Body.String(), `ui-page-title`)

	for _, section := range []string{"admin-users", "admin-workspaces", "admin-backups", "admin-auditoria"} {
		t.Run(section, func(t *testing.T) {
			h := newConfiguracoesRenderTestHandler(t)
			req := httptest.NewRequest(http.MethodGet, "/admin/"+strings.TrimPrefix(section, "admin-"), nil)
			rr := httptest.NewRecorder()
			h.HandleConfiguracoesSection(rr, req, section)
			body := rr.Body.String()

			for _, want := range []string{
				`/assets/css/premium.css?v=test`,
				`/assets/css/configuracoes.css?v=test`,
				`/assets/css/admin-premium.css?v=test`,
				`data-ui="premium"`,
				`cb-v2-admin-main`,
				`ui-page-header`,
				`ui-page-title`,
				`ui-page-description`,
				`id="settings-dynamic-payload"`,
			} {
				assertContains(t, body, want)
			}
			if strings.Index(body, "premium.css") > strings.Index(body, "configuracoes.css") {
				t.Fatal("premium.css must load before configuracoes.css")
			}
			if strings.Index(body, "configuracoes.css") > strings.Index(body, "admin-premium.css") {
				t.Fatal("admin-premium.css must load after configuracoes.css")
			}
			for _, forbidden := range []string{`sticky top-14`, `>admin users<`, `>admin workspaces<`, `>admin backups<`, `>admin auditoria<`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s must not keep the legacy administrative header %q", section, forbidden)
				}
			}
		})
	}
}
