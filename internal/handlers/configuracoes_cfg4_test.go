package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// CFG4 — Categorias, Contas e Cartões no padrão Premium.
//
// Os testes verificam estrutura, contratos HTMX e preservação de privacidade,
// sem recontar seletores CSS decorativos. O comportamento funcional de
// Categorias continua coberto pelos testes da CFG2 (JS e Go).

func TestConfiguracoesCategoriasPremiumStructure(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES
			('cfg4-expense-root', 'config-workspace', 'Despesa Raiz', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', NULL, ?),
			('cfg4-expense-child', 'config-workspace', 'Despesa Filha', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', 'cfg4-expense-root', ?),
			('cfg4-income-root', 'config-workspace', 'Receita Raiz', 'tag', '#6b7280', 'INCOME', 'Receitas Operacionais', NULL, ?)
	`, now, now, now)

	body := renderSectionPartial(t, handler, "categorias")
	for _, want := range []string{
		`cb-tabs cb-v2-cat-tabs`,
		`data-cat-tab="EXPENSE"`,
		`data-cat-tab="INCOME"`,
		`data-cat-tab-active="cb-tab-active"`,
		`data-cat-tab-inactive="cb-tab-inactive"`,
		`id="categorias-panel-expense"`,
		`id="categorias-panel-income"`,
		`ui-search`,
		`ui-input ui-input--search`,
		`data-cat-search`,
		`data-cat-combobox`,
		`data-cat-combobox-toggle`,
		`data-cat-combobox-hidden`,
		`data-cat-type-select`,
		`data-cat-macro-select`,
		`data-cat-parent-block`,
		`data-cat-parent-header`,
		`data-cat-toggle`,
		`data-cat-children`,
		`data-cat-macro`,
		`data-cat-child-count`,
		`cb-v2-cat-group`,
		`cb-v2-cat-child`,
		`Despesa Raiz`,
		`Despesa Filha`,
		`Receita Raiz`,
		`data-cat-macro-help`,
		`data-is-business`,
		`ui-label`,
		`ui-input`,
		`ui-select`,
		`hx-disabled-elt="find button[type='submit']"`,
		`hx-sync="this:drop"`,
		`hx-post="/configuracoes/categorias"`,
	} {
		assertContains(t, body, want)
	}
	if strings.Contains(body, `Outro grupo`) || strings.Contains(body, `macro_group_custom`) {
		t.Fatal("categorias must keep the closed macro-group contract")
	}
	if !strings.Contains(readTestFile(t, "templates/pages/configuracoes_categorias.html"), `assetPath "/assets/js/categorias-ui.js"`) {
		t.Fatal("categorias must keep loading the interactive controller")
	}
}

func TestConfiguracoesContasPremiumStructure(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, current_balance, created_at, updated_at)
		VALUES
			('cfg4-account', 'config-workspace', 'Conta Premium', 'CHECKING', '#EC7000', '', 'custom', 503639, ?, ?),
			('cfg4-account-archived', 'config-workspace', 'Conta Arquivada', 'SAVINGS', '#820AD1', '', 'custom', 120000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, handler.DB, `UPDATE accounts SET archived_at = ? WHERE id = 'cfg4-account-archived'`, now)

	body := renderSectionPartial(t, handler, "contas")
	for _, want := range []string{
		`cb-v2-config-form`,
		`cb-v2-config-row`,
		`cb-v2-config-icon`,
		`cb-v2-config-actions`,
		`cb-v2-config-value`,
		`cb-v2-archived-summary`,
		`is-archived`,
		`data-sensitive`,
		`ui-badge ui-badge--neutral`,
		`Conta Premium`,
		`Conta Arquivada`,
		`hx-get="/configuracoes/contas/cfg4-account/conciliar"`,
		`hx-post="/configuracoes/contas/cfg4-account/arquivar"`,
		`hx-post="/configuracoes/contas/cfg4-account-archived/reativar"`,
		`aria-label="Conciliar saldo de Conta Premium"`,
		`aria-label="Arquivar Conta Premium"`,
		`ui-select`,
		`ui-input`,
		`hx-disabled-elt="find button[type='submit']"`,
		`hx-sync="this:drop"`,
		`data-provider-select`,
	} {
		assertContains(t, body, want)
	}
}

func TestConfiguracoesCartoesPremiumStructure(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, created_at, updated_at)
		VALUES
			('cfg4-card', 'config-workspace', 'Cartão Premium', 'CREDIT_CARD', '#820AD1', '', 'custom', ?, ?),
			('cfg4-card-archived', 'config-workspace', 'Cartão Arquivado', 'CREDIT_CARD', '#EC7000', '', 'custom', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, handler.DB, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES
			('cfg4-cc', 'cfg4-card', 25, 5, 500000),
			('cfg4-cc-archived', 'cfg4-card-archived', 10, 17, 250000)
	`)
	execTestSQL(t, handler.DB, `UPDATE accounts SET archived_at = ? WHERE id = 'cfg4-card-archived'`, now)

	body := renderSectionPartial(t, handler, "cartoes")
	for _, want := range []string{
		`cb-v2-config-row`,
		`cb-v2-config-form`,
		`Cartão Premium`,
		`Cartão Arquivado`,
		`>Limite<`,
		`data-sensitive`,
		`Fecha dia 25`,
		`Vence dia 5`,
		`ui-badge ui-badge--neutral`,
		`hx-post="/configuracoes/cartoes/cfg4-card/arquivar"`,
		`hx-get="/configuracoes/cartoes/cfg4-card/formulario"`,
		`hx-post="/configuracoes/cartoes/cfg4-card-archived/reativar"`,
		`aria-label="Editar Cartão Premium"`,
		`ui-input`,
		`ui-select`,
		`hx-disabled-elt="find button[type='submit']"`,
		`hx-sync="this:drop"`,
	} {
		assertContains(t, body, want)
	}
}

func TestConfiguracoesCfg4KeepsHTMXAndFormContracts(t *testing.T) {
	components := readTestFile(t, "templates/pages/configuracoes_components.html")
	pages := readTestFile(t, "templates/pages/configuracoes_contas.html") +
		readTestFile(t, "templates/pages/configuracoes_cartoes.html")

	for _, want := range []string{
		`hx-get="/configuracoes/contas/{{.ID}}/conciliar" hx-target="closest article" hx-swap="outerHTML"`,
		`hx-get="/configuracoes/contas/{{.ID}}/formulario"`,
		`hx-target="closest article" hx-swap="outerHTML"`,
		`data-money-mask="signed"`,
		`name="balance_version"`,
		`name="request_id"`,
		`hx-post="/configuracoes/cartoes/{{.AccountID}}/salvar"`,
		`name="credit_limit"`,
		`name="closing_day"`,
		`name="due_day"`,
		`name="default_payment_account_id"`,
	} {
		assertContains(t, components, want)
	}
	for _, want := range []string{
		`hx-target="#settings-dynamic-payload" hx-swap="outerHTML"`,
		`data-provider-select`,
		`data-provider-custom`,
		`data-provider-custom-input`,
		`cb-v2-config-form`,
	} {
		assertContains(t, pages, want)
	}
}

func TestConfiguracoesCfg4DoesNotLeakIntoAdministrationOrWorkspace(t *testing.T) {
	for _, admin := range []string{
		"templates/pages/configuracoes_admin_users.html",
		"templates/pages/configuracoes_admin_workspaces.html",
		"templates/pages/configuracoes_admin_backups.html",
		"templates/pages/configuracoes_admin_auditoria.html",
	} {
		content := readTestFile(t, admin)
		for _, forbidden := range []string{`cb-v2-config-row`, `cb-v2-cat-`, `cb-v2-config-form`} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s must not consume CFG4 scoped markup %q", admin, forbidden)
			}
		}
	}

	// A Administração tem envelope premium próprio (ADM3), mas continua sem
	// consumir o vocabulário de linhas/formulários criado na CFG4.
	for _, section := range []string{"admin-users", "admin-auditoria"} {
		handler := newConfiguracoesRenderTestHandler(t)
		req := httptest.NewRequest(http.MethodGet, "/admin/"+strings.TrimPrefix(section, "admin-"), nil)
		rr := httptest.NewRecorder()
		handler.HandleConfiguracoesSection(rr, req, section)
		body := rr.Body.String()
		for _, forbidden := range []string{`cb-v2-config-row`, `cb-v2-cat-`, `cb-v2-config-form`} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s must not consume CFG4 scoped markup %q", section, forbidden)
			}
		}
	}
}
