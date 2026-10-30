package handlers

import (
	"strings"
	"testing"
)

// R4 — Navegação harmônica e gráficos Premium de /relatorios.
//
// Contratos cobertos:
//   - os três níveis de navegação usam a mesma linguagem de seleção, com
//     classes distintas para preservar a hierarquia;
//   - URLs/HTMX/estado ativo continuam iguais;
//   - donuts mantêm viewBox, raio, stroke-width, dasharray, ordem e os strokes
//     servidos pelo backend;
//   - cores hardcoded dos gráficos foram substituídas por tokens;
//   - legendas continuam mascaradas por data-sensitive.

func TestRelatoriosNavigationTabsUsePremiumSelection(t *testing.T) {
	data := relatoriosPremiumTestData(false)
	data.ExtratoView = "movimentos"
	personal := renderRelatoriosPremiumPage(t, data)

	for _, expected := range []string{
		`class="reports-main-tabs"`,
		`reports-main-tab--active`,
		`reports-main-tab--inactive`,
		`class="cb-tabs cb-v2-relatorios-modes"`,
		`class="cb-tabs reports-view-tabs"`,
		`/relatorios?tab=caixa&modo=realizado&mes=10&ano=2026&partial=content`,
		`/relatorios?tab=extrato&view=movimentos&mes=10&ano=2026&partial=content`,
		`hx-target="#relatorios-body"`,
		`hx-push-url="true"`,
	} {
		assertContains(t, personal, expected)
	}
	// O modo ativo da navegação principal é Fluxo; Extrato fica inativo.
	mainStart := strings.Index(personal, `class="reports-main-tabs"`)
	mainEnd := mainStart + strings.Index(personal[mainStart:], "</nav>")
	main := personal[mainStart:mainEnd]
	if !strings.Contains(main, `reports-main-tab--active`) || !strings.Contains(main, `reports-main-tab--inactive`) {
		t.Fatal("main navigation must expose active and inactive states")
	}
	if strings.Contains(main, `cb-tab`) {
		t.Fatal("main navigation must keep its own hierarchy, not reuse the segmented class")
	}

	viewStart := strings.Index(personal, `class="cb-tabs reports-view-tabs"`)
	viewEnd := viewStart + strings.Index(personal[viewStart:], "</nav>")
	modeStart := strings.Index(personal, `class="cb-tabs cb-v2-relatorios-modes"`)
	modeEnd := modeStart + strings.Index(personal[modeStart:], "</nav>")
	for _, nav := range []string{main, personal[viewStart:viewEnd], personal[modeStart:modeEnd]} {
		for _, forbidden := range []string{"bg-[var(--cb-accent)]", "text-white", "shadow-sm"} {
			if strings.Contains(nav, forbidden) {
				t.Fatalf("report navigation must not use primary-action styling %q", forbidden)
			}
		}
	}

	// Business mantém as quatro seções, uma ativa por vez.
	business := relatoriosPremiumTestData(true)
	business.ActiveTab = "competencia"
	businessBody := renderRelatoriosPremiumPage(t, business)
	for _, expected := range []string{
		`tab=contabil`,
		`tab=caixa`,
		`tab=competencia`,
		`tab=extrato`,
	} {
		assertContains(t, businessBody, expected)
	}
	if !strings.Contains(businessBody, `reports-main-tab--active`) {
		t.Fatal("business main navigation needs an active tab")
	}
}

func TestRelatoriosChartsUseTokensAndKeepGeometry(t *testing.T) {
	src := readTestFile(t, "templates/pages/relatorios.html")

	for _, expected := range []string{
		`class="reports-donut"`,
		`class="reports-donut-track"`,
		`class="donut-segment reports-donut-essential"`,
		`class="donut-segment reports-donut-lifestyle donut-delay-1"`,
		`class="reports-chart-body"`,
		`class="reports-chart-legend"`,
		`class="reports-chart-title"`,
		`class="reports-chart-caption"`,
		`viewBox="0 0 42 42"`,
		`r="15.915"`,
		`stroke-width="4.5"`,
		`stroke-linecap="round"`,
	} {
		assertContains(t, src, expected)
	}
	for _, forbidden := range []string{
		`rgb(228 228 231)`,
		`rgb(0, 212, 146)`,
		`rgb(103, 68, 241)`,
		`dark:stroke-zinc-700`,
	} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("chart template still carries hardcoded color %q", forbidden)
		}
	}
	if got := strings.Count(src, `class="reports-donut"`); got != 8 {
		t.Fatalf("expected 8 donuts to adopt reports-donut, got %d", got)
	}
	if got := strings.Count(src, `class="reports-chart-body"`); got != 8 {
		t.Fatalf("expected 8 chart bodies, got %d", got)
	}
	if got := strings.Count(src, `class="reports-chart-legend"`); got != 8 {
		t.Fatalf("expected 8 chart legends, got %d", got)
	}

	css := readTestFile(t, "assets/css/relatorios.css")
	for _, expected := range []string{
		`.reports-donut-track { stroke: var(--ui-progress-track); }`,
		`.reports-donut-essential { stroke: var(--ui-success); }`,
		`.reports-donut-lifestyle { stroke: var(--ui-accent); }`,
		`.reports-chart-title`,
		`.reports-chart-caption`,
		`.reports-chart-legend`,
		`:has(.reports-chart-title)`,
		`font-variant-numeric: tabular-nums`,
		`white-space: nowrap`,
		`.reports-chart-legend-value {`,
		`color: var(--ui-text);`,
	} {
		assertContains(t, css, expected)
	}
	for _, forbidden := range []string{`rgb(0, 212, 146)`, `rgb(103, 68, 241)`, `rgb(228 228 231)`} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("chart CSS still carries hardcoded color %q", forbidden)
		}
	}
	// A paleta fechada servida pelo backend permanece intacta (17 strokes).
	if got := strings.Count(css, "[data-report-stroke="); got != 17 {
		t.Fatalf("backend stroke palette must keep 17 mappings, got %d", got)
	}
}

func TestRelatoriosChartsRenderDataAndPrivacy(t *testing.T) {
	personal := relatoriosPremiumTestData(false)
	personal.DonutCaixaEssentialPercent = 40
	personal.DonutCaixaEssentialPercentLabel = "40%"
	personal.DonutCaixaEssentialTotal = MoneyDisplay{Reais: "120", Cents: ",00"}
	personal.DonutCaixaLifestylePercent = 60
	personal.DonutCaixaLifestylePercentLabel = "60%"
	personal.DonutCaixaLifestyleTotal = MoneyDisplay{Reais: "180", Cents: ",00"}
	body := renderRelatoriosPremiumPage(t, personal)

	for _, expected := range []string{
		`class="donut-segment reports-donut-essential"`,
		`class="donut-segment reports-donut-lifestyle donut-delay-1"`,
		`class="reports-chart-legend-value" data-sensitive>40% · R$ 120,00`,
		`class="reports-chart-legend-value" data-sensitive>60% · R$ 180,00`,
		`<svg viewBox="0 0 42 42" class="reports-donut" aria-hidden="true">`,
	} {
		assertContains(t, body, expected)
	}

	business := relatoriosPremiumTestData(true)
	business.RevenueByCategoryCaixa = []RevenueCategorySlice{{
		Name:       "Serviços",
		Amount:     MoneyDisplay{Reais: "2.500", Cents: ",00"},
		Percent:    65,
		Stroke:     "#10B981",
		DashOffset: 25,
	}}
	business.BusinessCostsCaixa = []BusinessCostMacroRow{{
		MacroGroup: "Operacional",
		Amount:     MoneyDisplay{Reais: "900", Cents: ",00"},
		Percent:    35,
		Stroke:     "rgb(99 102 241)",
		DashOffset: 60,
	}}
	businessBody := renderRelatoriosPremiumPage(t, business)

	for _, expected := range []string{
		`data-report-stroke="#10B981"`,
		`data-report-stroke="rgb(99 102 241)"`,
		`65% · R$ 2.500,00`,
		`35% · R$ 900,00`,
		`class="reports-chart-legend-value" data-report-stroke="#10B981" data-sensitive`,
		`class="reports-chart-legend-value" data-report-stroke="rgb(99 102 241)" data-sensitive`,
		`class="reports-chart-legend"`,
	} {
		assertContains(t, businessBody, expected)
	}

	// Os donuts são decorativos; os valores acessíveis ficam nas legendas e
	// nenhum deles vira texto ou título dentro do SVG.
	if strings.Contains(body, "<text") || strings.Contains(businessBody, "<text") {
		t.Fatal("donuts must stay label-free inside the SVG")
	}
}
