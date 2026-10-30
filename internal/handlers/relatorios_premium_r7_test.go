package handlers

import (
	"strings"
	"testing"
)

// R7 — Mobile, alvos de toque, affordance de rolagem e ARIA dos dropdowns.
//
// Contratos cobertos:
//   - alvos de 44px escopados em Relatórios até 1023px, desktop intacto;
//   - dica de rolagem horizontal presente apenas como contrato de layout,
//     com detecção real delegada ao script local;
//   - dropdowns expõem aria-expanded/aria-controls no botão e painel com id;
//   - foco e reduced motion continuam cobertos por CSS local.

func TestRelatoriosTouchTargetsAreScopedAndAccessible(t *testing.T) {
	css := readTestFile(t, "assets/css/relatorios.css")
	for _, expected := range []string{
		`@media (max-width: 1023px)`,
		`.reports-main-tab {`,
		`min-height: 44px`,
		`.cb-v2-relatorios-main [data-month-selector] .month-btn`,
		`.cb-v2-relatorios-main [data-month-selector] .month-control`,
		`width: 44px;`,
		`height: 44px;`,
		`.reports-export-menu .cb-meta-menu-item`,
		`.cb-v2-relatorios-main [data-month-selector] :is(.month-btn, .month-control):focus-visible`,
		`@media (prefers-reduced-motion: reduce)`,
		`scroll-behavior: auto;`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("relatorios.css missing R7 contract %q", expected)
		}
	}

	// O aumento de alvo do seletor de meses é local; o componente compartilhado
	// não pode ser redefinido globalmente.
	if strings.Contains(css, "\n.month-control {") || strings.Contains(css, "\n.month-btn {") {
		t.Fatal("month selector touch targets must stay scoped to Relatórios")
	}
}

func TestRelatoriosScrollHintMarkupAndScript(t *testing.T) {
	data := relatoriosPremiumTestData(false)
	data.ExtratoView = "movimentos"
	data.ExtratoRows = []StatementExtratoRow{{
		DateLabel:   "25/10",
		Description: "Passagem e Alimentação",
		Tipo:        "INCOME",
		Entrada:     MoneyDisplay{Reais: "200", Cents: ",00"},
	}}
	data.DailySummary = []DailySummaryRow{{
		DateLabel:   "25/10",
		IncomeTotal: MoneyDisplay{Reais: "200", Cents: ",00"},
		HasIncome:   true,
	}}
	body := renderRelatoriosPremiumPage(t, data)

	for _, expected := range []string{
		`/assets/js/relatorios-scroll.js`,
		`id="reports-caixa-scroll-hint" class="reports-scroll-hint" data-scroll-hint hidden`,
		`id="reports-extrato-scroll-hint" class="reports-scroll-hint" data-scroll-hint hidden`,
		`id="reports-diario-scroll-hint" class="reports-scroll-hint" data-scroll-hint hidden`,
		`aria-describedby="reports-caixa-scroll-hint"`,
		`aria-describedby="reports-extrato-scroll-hint"`,
		`aria-describedby="reports-diario-scroll-hint"`,
		`Deslize para ver mais colunas`,
		`role="region"`,
		`tabindex="0"`,
	} {
		assertContains(t, body, expected)
	}
	if strings.Contains(body, `id="reports-dre-scroll-hint"`) {
		t.Fatal("personal page must not render the business-only DRE hint")
	}

	business := relatoriosPremiumTestData(true)
	business.DRE = []DRERow{{Competencia: "2026-10", Resultado: MoneyDisplay{Reais: "1", Cents: ",00"}}}
	business.DREMacroGroups = []string{"Receitas"}
	businessBody := renderRelatoriosPremiumPage(t, business)
	assertContains(t, businessBody, `id="reports-dre-scroll-hint" class="reports-scroll-hint" data-scroll-hint hidden`)
	assertContains(t, businessBody, `aria-describedby="reports-dre-scroll-hint"`)

	js := readTestFile(t, "assets/js/relatorios-scroll.js")
	for _, expected := range []string{
		`region.scrollWidth > region.clientWidth + 1`,
		`region.scrollLeft + region.clientWidth >= region.scrollWidth - 2`,
		`hint.hidden = !scrollable || atEnd`,
		`htmx:afterSwap`,
		`addEventListener('resize', updateAll)`,
	} {
		if !strings.Contains(js, expected) {
			t.Fatalf("relatorios-scroll.js missing contract %q", expected)
		}
	}
}

func TestRelatoriosExportDropdownARIA(t *testing.T) {
	personal := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))
	for _, expected := range []string{
		`aria-expanded="false" aria-controls="reports-extrato-pdf-menu"`,
		`aria-expanded="false" aria-controls="reports-extrato-csv-menu"`,
		`id="reports-extrato-pdf-menu"`,
		`id="reports-extrato-csv-menu"`,
	} {
		assertContains(t, personal, expected)
	}
	if strings.Contains(personal, `data-dropdown aria-expanded=`) {
		t.Fatal("aria-expanded must live on the disclosure button, not the wrapper")
	}

	business := relatoriosPremiumTestData(true)
	businessBody := renderRelatoriosPremiumPage(t, business)
	for _, expected := range []string{
		`aria-expanded="false" aria-controls="reports-contabil-csv-menu"`,
		`id="reports-contabil-csv-menu"`,
	} {
		assertContains(t, businessBody, expected)
	}

	js := readTestFile(t, "assets/js/dropdown.js")
	for _, expected := range []string{
		`toggle.setAttribute('aria-expanded', 'true')`,
		`toggle.setAttribute('aria-expanded', 'false')`,
	} {
		if !strings.Contains(js, expected) {
			t.Fatalf("dropdown.js missing toggle ARIA contract %q", expected)
		}
	}
}
