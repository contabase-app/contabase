package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

// CFG5 — Hub de Configurações e semântica das tabs.
func TestConfiguracoesHubShowsOnlyAuthorizedSections(t *testing.T) {
	renderHub := func(handler ConfiguracoesHandler) string {
		req := httptest.NewRequest(http.MethodGet, "/configuracoes", nil)
		rr := httptest.NewRecorder()
		handler.HandleConfiguracoesConceito(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("hub status = %d, want %d", rr.Code, http.StatusOK)
		}
		return rr.Body.String()
	}

	admin := newConfiguracoesRenderTestHandler(t)
	adminBody := renderHub(admin)
	for _, want := range []string{
		`cb-v2-hub-grid`,
		`href="/configuracoes/perfil"`,
		`href="/configuracoes/workspace"`,
		`href="/configuracoes/categorias"`,
		`href="/configuracoes/contas"`,
		`href="/configuracoes/cartoes"`,
		`href="/admin/usuarios"`,
		`href="/admin/workspaces"`,
		`href="/admin/backups"`,
		`href="/admin/auditoria"`,
	} {
		assertContains(t, adminBody, want)
	}
	for _, forbidden := range []string{`data-config-legacy-hub-nav`, `configuracoes-sidebar`, `config-sidebar`} {
		if strings.Contains(adminBody, forbidden) {
			t.Fatalf("hub must not render the legacy navigation %q", forbidden)
		}
	}

	manager := newConfiguracoesRenderTestHandler(t)
	manager.ActorRole = models.RoleManager
	managerBody := renderHub(manager)
	for _, want := range []string{`/configuracoes/workspace`, `/configuracoes/categorias`, `/configuracoes/contas`, `/configuracoes/cartoes`} {
		assertContains(t, managerBody, want)
	}
	if strings.Contains(managerBody, `/admin/usuarios`) {
		t.Fatal("manager hub must not expose administrative links")
	}

	user := newConfiguracoesRenderTestHandler(t)
	user.ActorRole = models.RoleUser
	user.CanConfigRead = false
	user.CanConfigWrite = false
	userBody := renderHub(user)
	assertContains(t, userBody, `href="/configuracoes/perfil"`)
	for _, forbidden := range []string{`/configuracoes/workspace`, `/configuracoes/categorias`, `/configuracoes/contas`, `/configuracoes/cartoes`, `/admin/usuarios`} {
		if strings.Contains(userBody, forbidden) {
			t.Fatalf("user hub must not expose %q", forbidden)
		}
	}
}

func TestConfiguracoesCategoriasTabsUseToggleButtonSemantics(t *testing.T) {
	template := readTestFile(t, "templates/pages/configuracoes_categorias.html")
	for _, want := range []string{
		`role="group" aria-label="Tipo de categoria"`,
		`aria-pressed="true"`,
		`aria-pressed="false"`,
		`aria-controls="categorias-panel-expense"`,
		`aria-controls="categorias-panel-income"`,
	} {
		if !strings.Contains(template, want) {
			t.Fatalf("categorias tabs lost %q", want)
		}
	}
	if strings.Contains(template, `role="tab"`) {
		t.Fatal("categorias must not mix toggle buttons with role=tab semantics")
	}
}
