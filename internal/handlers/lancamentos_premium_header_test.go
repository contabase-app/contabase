package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

// F2 — Premium header, toolbar and privacy. These tests freeze the visual
// adoption without allowing the existing search/filter/month contracts to be
// renamed or removed.
func TestLancamentosPremiumHeaderAndToolbarContract(t *testing.T) {
	tpl := template.Must(template.New("lancamentos-premium").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
		resolveTemplatePath(t, "templates/components/seletor_meses.html"),
	))

	data := LancamentosData{
		Title:               "Lançamentos",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		MonthLabel:          "Outubro de 2026",
		Filters:             LancamentosFilters{Busca: "mercado"},
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "lancamentos-page", data); err != nil {
		t.Fatalf("render lancamentos page: %v", err)
	}
	body := out.String()

	for _, expected := range []string{
		// Premium foundation and canonical header.
		`data-ui="premium"`,
		`/assets/css/premium.css?v=test`,
		`class="cb-v2-lancamentos-header ui-page-header"`,
		`class="ui-eyebrow"`,
		`class="ui-page-title"`,
		`class="ui-page-description"`,
		`class="lancamentos-header-actions"`,
		// Privacy control reuses the global state/script.
		`data-privacy-toggle`,
		`data-privacy-icon`,
		`aria-pressed="false"`,
		`aria-label="Ocultar valores financeiros"`,
		`/assets/js/privacy.js?v=test`,
		// Search keeps its name, value, debounce, include and target contract.
		`name="q" value="mercado"`,
		`class="ui-input ui-input--search"`,
		`aria-label="Buscar lançamentos"`,
		`hx-trigger="keyup delay:400ms, search"`,
		`hx-include="#lancamentosFilters"`,
		`hx-target="#lancamentos-list-wrapper"`,
		`hx-select="#lancamentos-list-wrapper"`,
		// Filter trigger keeps the delegated handler and exposes state.
		`id="lancamentosFilterTrigger"`,
		`data-onclick="toggleLancamentosFilters"`,
		`aria-controls="lancamentosFilterPanel"`,
		`aria-expanded="false"`,
		`aria-label="Abrir filtros"`,
		// Existing filter conditions remain present.
		`name="situacao" value="pendente"`,
		`name="situacao" value="vencido"`,
		`name="situacao" value="agendado"`,
		`name="situacao" value="pago"`,
		`name="tipo" value="receita"`,
		`name="tipo" value="despesa"`,
		`name="tipo" value="transferencia"`,
		`name="ordem" value="asc"`,
		`name="ordem" value="desc"`,
		`id="lancamentos-list-wrapper"`,
		`id="lancamentosResumo"`,
	} {
		assertContains(t, body, expected)
	}

	if got := strings.Count(body, `data-privacy-toggle`); got != 1 {
		t.Fatalf("privacy toggle count = %d, want exactly 1 on /lancamentos", got)
	}
	if strings.Contains(body, `class="search-input"`) {
		t.Fatal("legacy 40px search-input class must not return to the Premium toolbar")
	}
	for _, keep := range []string{
		`hx-get="/lancamentos"`,
		`hx-swap="outerHTML"`,
		`hx-replace-url="true"`,
		`name="partial" value="lista"`,
	} {
		assertContains(t, body, keep)
	}
}

func TestLancamentosPrivacyAndFilterStateAssets(t *testing.T) {
	privacyJS, err := os.ReadFile(resolveTemplatePath(t, "assets/js/privacy.js"))
	if err != nil {
		t.Fatalf("read privacy.js: %v", err)
	}
	privacy := string(privacyJS)
	for _, expected := range []string{
		`sessionStorage.getItem('contabasePrivacy')`,
		`sessionStorage.setItem('contabasePrivacy'`,
		`document.body.classList.toggle('privacy-enabled'`,
		`window.__contabasePrivacyDelegated`,
		`aria-pressed`,
		`aria-label`,
		`data-tooltip-text`,
	} {
		assertContains(t, privacy, expected)
	}
	if got := strings.Count(privacy, "document.addEventListener('click'"); got != 1 {
		t.Fatalf("privacy delegated click listeners = %d, want 1", got)
	}

	filtersJS, err := os.ReadFile(resolveTemplatePath(t, "assets/js/lancamentos-filters.js"))
	if err != nil {
		t.Fatalf("read lancamentos-filters.js: %v", err)
	}
	filters := string(filtersJS)
	for _, expected := range []string{
		`function syncLancamentosFilterTrigger()`,
		`syncLancamentosFilterTrigger();`,
		`MutationObserver`,
		`aria-expanded`,
	} {
		assertContains(t, filters, expected)
	}

	css, err := os.ReadFile(resolveTemplatePath(t, "assets/css/lancamentos.css"))
	if err != nil {
		t.Fatalf("read lancamentos.css: %v", err)
	}
	lanCSS := string(css)
	for _, expected := range []string{
		`.privacy-enabled .cb-v2-transactions-main [data-sensitive]`,
		`visibility: hidden`,
		`::after`,
		`#lancamentosFilterPanel [data-filter-chip]:has(input:checked)`,
		`#lancamentosFilterPanel label:has(> input[name="ordem"]:checked)`,
	} {
		assertContains(t, lanCSS, expected)
	}
	if strings.Contains(lanCSS, ".search-input {") {
		t.Fatal("legacy .search-input styles must not remain after the toolbar migration")
	}
}
