package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// CFG2 — Categorias.
//
// Estes testes cobrem os contratos que sustentam a correção:
//   - a página de Categorias carrega o controller interativo uma única vez;
//   - o payload HTMX continua sendo o alvo dos swaps, sem duplicar o script;
//   - o formulário só oferece grupos macro válidos para o domínio;
//   - a criação rejeita o literal "custom" e aceita um grupo válido;
//   - as categorias de despesa e receita continuam nos painéis corretos.

func TestConfiguracoesCategoriasLoadsInteractiveControllerOnce(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	fullReq := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias", nil)
	fullRR := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(fullRR, fullReq, "categorias")

	if fullRR.Code != http.StatusOK {
		t.Fatalf("full status = %d, want %d", fullRR.Code, http.StatusOK)
	}
	fullBody := fullRR.Body.String()
	for _, want := range []string{
		`/assets/js/categorias-ui.js?v=test`,
		`/assets/css/premium.css?v=test`,
		`data-ui="premium"`,
		`id="settings-dynamic-payload"`,
		`data-cat-root`,
		`ui-page-header`,
	} {
		assertContains(t, fullBody, want)
	}
	if count := strings.Count(fullBody, `/assets/js/categorias-ui.js?v=test`); count != 1 {
		t.Fatalf("categorias-ui.js loaded %d times, want exactly 1", count)
	}

	partialReq := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias", nil)
	partialReq.Header.Set("HX-Request", "true")
	partialRR := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(partialRR, partialReq, "categorias")
	if partialRR.Code != http.StatusOK {
		t.Fatalf("partial status = %d, want %d", partialRR.Code, http.StatusOK)
	}
	if strings.Contains(partialRR.Body.String(), "categorias-ui.js") {
		t.Fatal("HTMX partial must not re-include the controller; htmx:afterSwap initializes the new payload")
	}

	script := readTestFile(t, "assets/js/categorias-ui.js")
	for _, contract := range []string{
		`__contabaseCategoriasUIInitialized`,
		`[data-cat-tab]`,
		`[data-cat-tab-panel]`,
		`[data-cat-combobox-toggle]`,
		`[data-cat-combobox-option]`,
		`[data-cat-search]`,
		`htmx:afterSwap`,
		`data-cat-ready`,
	} {
		if !strings.Contains(script, contract) {
			t.Fatalf("categorias-ui.js lost the contract %q", contract)
		}
	}
}

func TestConfiguracoesCategoriasTemplateKeepsInteractiveMarkup(t *testing.T) {
	categorias := readTestFile(t, "templates/pages/configuracoes_categorias.html")
	components := readTestFile(t, "templates/pages/configuracoes_components.html")

	for _, want := range []string{
		`data-cat-tab="EXPENSE"`,
		`data-cat-tab="INCOME"`,
		`aria-pressed="true"`,
		`id="categorias-panel-expense"`,
		`id="categorias-panel-income"`,
		`data-cat-tab-panel="EXPENSE"`,
		`data-cat-tab-panel="INCOME"`,
		`data-cat-search`,
		`data-cat-combobox-toggle`,
		`aria-haspopup="listbox"`,
		`data-cat-combobox-search`,
		`data-cat-combobox-hidden`,
		`role="option"`,
		`data-cat-type-select`,
		`data-cat-macro-select`,
		`data-cat-macro-for-type`,
	} {
		if !strings.Contains(categorias, want) {
			t.Fatalf("categorias template lost %q", want)
		}
	}

	for _, want := range []string{
		`data-cat-toggle`,
		`data-cat-parent-block`,
		`data-cat-children`,
		`data-cat-name`,
	} {
		if !strings.Contains(components, want) {
			t.Fatalf("category tree lost %q", want)
		}
	}

	// Grupos macro são um contrato fechado do domínio: o formulário não pode
	// oferecer um valor "custom" nem enviar macro_group_custom.
	for name, template := range map[string]string{
		"categorias": categorias,
		"components": components,
	} {
		for _, forbidden := range []string{`Outro grupo`, `macro_group_custom`} {
			if strings.Contains(template, forbidden) {
				t.Fatalf("%s template still exposes %q", name, forbidden)
			}
		}
	}
	if strings.Contains(categorias, `value="custom"`) {
		t.Fatal("categorias template still offers the invalid custom macro group")
	}
	if !strings.Contains(categorias, `Os grupos macro são definidos pelo ContaBase`) {
		t.Fatal("categorias must explain that macro groups are defined by the domain")
	}
}

func TestConfiguracoesCategoriasRenderShowsIncomeAndExpense(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cfg2-expense', 'config-workspace', 'Despesa CFG2', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', ?),
			('cfg2-income', 'config-workspace', 'Receita CFG2', 'tag', '#6b7280', 'INCOME', 'Receitas Operacionais', ?)
	`, now, now)

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, "categorias")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	expensePanel := body[strings.Index(body, `data-cat-tab-panel="EXPENSE"`):strings.Index(body, `data-cat-tab-panel="INCOME"`)]
	incomePanel := body[strings.Index(body, `data-cat-tab-panel="INCOME"`):]
	if !strings.Contains(expensePanel, "Despesa CFG2") {
		t.Fatal("expense category must render inside the EXPENSE panel")
	}
	if strings.Contains(expensePanel, "Receita CFG2") {
		t.Fatal("income category must not render inside the EXPENSE panel")
	}
	if !strings.Contains(incomePanel, "Receita CFG2") {
		t.Fatal("income category must render inside the INCOME panel")
	}
	if strings.Contains(incomePanel, "Despesa CFG2") {
		t.Fatal("expense category must not render inside the INCOME panel")
	}
	if !strings.Contains(body, `(1)`) {
		t.Fatal("tab counters must reflect the existing categories")
	}
}

func TestConfiguracoesCategoriasRejectLiteralCustomMacro(t *testing.T) {
	handler := newConfiguracoesPersonalRenderTestHandler(t)

	rr := postCategoriaCreate(t, handler, url.Values{
		"name":        {"Categoria Custom"},
		"type":        {"EXPENSE"},
		"macro_group": {"custom"},
	})

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	assertContains(t, rr.Body.String(), `não é válido`)
	if got := categoryCountByName(t, handler, "Categoria Custom"); got != 0 {
		t.Fatalf("literal custom macro created %d categories, want 0", got)
	}
}

func TestConfiguracoesCategoriasAcceptOnlyValidMacroGroups(t *testing.T) {
	handler := newConfiguracoesPersonalRenderTestHandler(t)

	valid := postCategoriaCreate(t, handler, url.Values{
		"name":               {"Categoria Válida"},
		"type":               {"EXPENSE"},
		"macro_group":        {"custom"},
		"macro_group_custom": {"Estilo de Vida"},
	})
	assertContains(t, valid.Body.String(), `Categoria criada com sucesso.`)
	var macro string
	if err := handler.DB.QueryRow(`
		SELECT macro_group FROM categories
		WHERE workspace_id = 'config-workspace' AND name = 'Categoria Válida'
	`).Scan(&macro); err != nil {
		t.Fatalf("query created category: %v", err)
	}
	if macro != "Estilo de Vida" {
		t.Fatalf("macro_group = %q, want %q", macro, "Estilo de Vida")
	}

	invalid := postCategoriaCreate(t, handler, url.Values{
		"name":               {"Categoria Inválida"},
		"type":               {"EXPENSE"},
		"macro_group_custom": {"Meu Grupo"},
	})
	assertContains(t, invalid.Body.String(), `não é válido`)
	if got := categoryCountByName(t, handler, "Categoria Inválida"); got != 0 {
		t.Fatalf("invalid custom macro created %d categories, want 0", got)
	}
}

func TestConfiguracoesCategoriasBusinessAcceptsIncomeMacro(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	rr := postCategoriaCreate(t, handler, url.Values{
		"name":        {"Receita Financeira CFG2"},
		"type":        {"INCOME"},
		"macro_group": {"Receitas Financeiras"},
	})
	assertContains(t, rr.Body.String(), `Categoria criada com sucesso.`)

	var macro string
	if err := handler.DB.QueryRow(`
		SELECT macro_group FROM categories
		WHERE workspace_id = 'config-workspace' AND name = 'Receita Financeira CFG2'
	`).Scan(&macro); err != nil {
		t.Fatalf("query created category: %v", err)
	}
	if macro != "Receitas Financeiras" {
		t.Fatalf("macro_group = %q, want %q", macro, "Receitas Financeiras")
	}
}

func postCategoriaCreate(t *testing.T, handler ConfiguracoesHandler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandleCategoriasCreate(rr, req)
	return rr
}

func categoryCountByName(t *testing.T, handler ConfiguracoesHandler, name string) int {
	t.Helper()
	var count int
	if err := handler.DB.QueryRow(`
		SELECT COUNT(*) FROM categories WHERE workspace_id = 'config-workspace' AND name = ?
	`, name).Scan(&count); err != nil {
		t.Fatalf("count categories: %v", err)
	}
	return count
}
