package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ADM2 — os scripts administrativos carregam apenas nas páginas que os consomem
// (documento completo via assetPath), nunca no shell global nem no fragmento
// HTMX da seção, e nunca nas páginas de Configurações Premium.
func TestAdminScriptsLoadOnlyOnTheirAdminPages(t *testing.T) {
	renderSection := func(t *testing.T, section string, hx bool) string {
		t.Helper()
		handler := newConfiguracoesRenderTestHandler(t)
		req := httptest.NewRequest(http.MethodGet, "/"+section, nil)
		if hx {
			req.Header.Set("HX-Request", "true")
		}
		rr := httptest.NewRecorder()
		handler.HandleConfiguracoesSection(rr, req, section)
		if rr.Code != http.StatusOK {
			t.Fatalf("section %s (hx=%v): status = %d, body = %q", section, hx, rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	adminA11yScript := `src="/assets/js/admin-accessibility.js?v=test"`
	usersScripts := []string{
		`src="/assets/js/admin-user-card.js?v=test"`,
		`src="/assets/js/admin-recovery.js?v=test"`,
		`src="/assets/js/admin-user-workspace-roles.js?v=test"`,
		adminA11yScript,
	}
	workspacesScripts := []string{
		`src="/assets/js/admin-workspace.js?v=test"`,
		`src="/assets/js/config-masks.js?v=test"`,
		adminA11yScript,
	}
	allAdminScripts := append(append([]string{}, usersScripts...), workspacesScripts...)
	adminComponentScripts := []string{
		"admin-user-card.js", "admin-recovery.js", "admin-user-workspace-roles.js",
		"admin-workspace.js", "admin-accessibility.js",
	}

	t.Run("admin users carrega seus tres scripts e a camada de acessibilidade", func(t *testing.T) {
		body := renderSection(t, "admin-users", false)
		for _, want := range usersScripts {
			if !strings.Contains(body, want) {
				t.Fatalf("admin-users deve carregar %s", want)
			}
		}
		for _, forbidden := range workspacesScripts {
			if forbidden == adminA11yScript {
				continue
			}
			if strings.Contains(body, forbidden) {
				t.Fatalf("admin-users não deve carregar %s", forbidden)
			}
		}
	})

	t.Run("admin workspaces carrega seu script, as mascaras e a acessibilidade", func(t *testing.T) {
		body := renderSection(t, "admin-workspaces", false)
		for _, want := range workspacesScripts {
			if !strings.Contains(body, want) {
				t.Fatalf("admin-workspaces deve carregar %s", want)
			}
		}
		for _, forbidden := range usersScripts {
			if forbidden == adminA11yScript {
				continue
			}
			if strings.Contains(body, forbidden) {
				t.Fatalf("admin-workspaces não deve carregar %s", forbidden)
			}
		}
	})

	t.Run("backups e auditoria carregam apenas a acessibilidade", func(t *testing.T) {
		for _, section := range []string{"admin-backups", "admin-auditoria"} {
			body := renderSection(t, section, false)
			if !strings.Contains(body, adminA11yScript) {
				t.Fatalf("%s deve carregar a camada de acessibilidade", section)
			}
			for _, forbidden := range []string{
				"admin-user-card.js", "admin-recovery.js", "admin-user-workspace-roles.js", "admin-workspace.js",
			} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s não deve carregar %s", section, forbidden)
				}
			}
		}
	})

	t.Run("fragmento htmx nao recarrega scripts", func(t *testing.T) {
		for _, section := range []string{"admin-users", "admin-workspaces", "admin-backups", "admin-auditoria"} {
			body := renderSection(t, section, true)
			for _, forbidden := range allAdminScripts {
				if strings.Contains(body, forbidden) {
					t.Fatalf("fragmento de %s não deve recarregar %s", section, forbidden)
				}
			}
		}
	})

	t.Run("nenhuma pagina de Configuracoes carrega scripts administrativos", func(t *testing.T) {
		for _, section := range []string{
			"perfil", "workspace", "categorias", "contas", "cartoes",
		} {
			body := renderSection(t, section, false)
			for _, forbidden := range adminComponentScripts {
				if strings.Contains(body, forbidden) {
					t.Fatalf("%s não deve carregar %s", section, forbidden)
				}
			}
		}
	})
}

// ADM2 — os botões de alternância nascem com estado ARIA coerente para o JS manter.
func TestAdminTemplatesExposeDisclosureARIA(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	usersRR := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(usersRR, httptest.NewRequest(http.MethodGet, "/admin/usuarios", nil), "admin-users")
	usersBody := usersRR.Body.String()
	if !strings.Contains(usersBody, `data-admin-user-edit aria-expanded="false"`) {
		t.Fatal("botão Editar de usuário deve nascer com aria-expanded=false")
	}
	if strings.Count(usersBody, `data-admin-recovery-action="password" aria-expanded="false"`) != 1 {
		t.Fatal("ação de recuperação password deve nascer com aria-expanded=false")
	}

	wsRR := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(wsRR, httptest.NewRequest(http.MethodGet, "/admin/workspaces", nil), "admin-workspaces")
	if !strings.Contains(wsRR.Body.String(), `data-admin-ws-toggle aria-expanded="false"`) {
		t.Fatal("botão Editar de workspace deve nascer com aria-expanded=false")
	}
}
