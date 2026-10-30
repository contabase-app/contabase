package handlers

import (
	"strings"
	"testing"
)

// R3 — Indicadores financeiros de /relatorios sobre os componentes Premium e
// alinhamento do seletor Realizado/Previsão às tabs de Metas.
//
// Contratos cobertos aqui:
//   - os quatro grupos reais de KPIs (caixa realizado, caixa previsto,
//     competência e contábil) usam ui-kpi-grid/ui-kpi;
//   - valores, rótulos, metadados e contagem são preservados;
//   - resultado positivo/zero/negativo recebe o tom correto;
//   - a contagem de movimentos não recebe semântica monetária;
//   - o seletor de modo usa a linguagem .cb-tabs/.cb-tab de Metas sem perder
//     URL, HTMX, histórico ou estado ativo;
//   - a composição CSS é responsiva por container e não recria a paleta.

func kpiCardWindow(t *testing.T, body, label string) string {
	t.Helper()
	marker := ">" + label + "</span>"
	idx := strings.Index(body, marker)
	if idx < 0 {
		t.Fatalf("kpi label %q not found", label)
	}
	head := body[:idx]
	start := strings.LastIndex(head, `<div class="ui-kpi"`)
	if alt := strings.LastIndex(head, `<div class="ui-kpi `); alt > start {
		start = alt
	}
	if start < 0 {
		t.Fatalf("kpi card for %q not found", label)
	}
	end := idx + 500
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}

func TestRelatoriosCashKPIsUsePremiumComponents(t *testing.T) {
	personal := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))

	for _, expected := range []string{
		`ui-kpi-grid cb-v2-relatorios-kpis cb-v2-relatorios-kpis--4`,
		`aria-label="Indicadores do caixa realizado"`,
		`ui-kpi__label`,
		`ui-kpi__value`,
		`ui-kpi__meta`,
		`ui-kpi__icon`,
		`ui-kpi ui-kpi--success`,
		`ui-kpi ui-kpi--danger`,
	} {
		assertContains(t, personal, expected)
	}
	for _, label := range []string{"Entradas recebidas", "Saídas pagas", "Resultado realizado", "Movimentos confirmados"} {
		assertContains(t, personal, label)
	}
	if strings.Contains(personal, "min-h-[7rem]") {
		t.Fatal("relatorios KPIs must not keep the legacy dense card height")
	}

	// Previsão mantém os quatro indicadores próprios, sem herdar os realizados.
	previsao := relatoriosPremiumTestData(false)
	previsao.IsCaixaPrevisao = true
	previsao.IsCaixaRealizado = false
	previsaoBody := renderRelatoriosPremiumPage(t, previsao)
	for _, expected := range []string{
		`aria-label="Indicadores do caixa previsto"`,
		"Entradas previstas",
		"Saídas previstas",
		"Resultado previsto",
		"Saldo projetado",
	} {
		assertContains(t, previsaoBody, expected)
	}
	for _, forbidden := range []string{"Entradas recebidas", "Saídas pagas", "Resultado realizado"} {
		if strings.Contains(previsaoBody, forbidden) {
			t.Fatalf("previsao must not render the realized KPI %q", forbidden)
		}
	}

	// Business: competência (3) e contábil (4) também usam os componentes.
	business := relatoriosPremiumTestData(true)
	business.ActiveTab = "competencia"
	businessBody := renderRelatoriosPremiumPage(t, business)
	for _, expected := range []string{
		`ui-kpi-grid cb-v2-relatorios-kpis cb-v2-relatorios-kpis--3`,
		`aria-label="Indicadores de competência"`,
		"Receitas",
		"Despesas",
		"Receitas menos despesas",
		`ui-kpi-grid cb-v2-relatorios-kpis cb-v2-relatorios-kpis--4`,
		`aria-label="Indicadores contábeis"`,
		"Competência",
		"Caixa realizado",
		"Confirmados no mês",
	} {
		assertContains(t, businessBody, expected)
	}
	if strings.Contains(personal, "Indicadores de competência") {
		t.Fatal("personal workspace must not render business-only KPI groups")
	}
}

func TestRelatoriosKPIResultTonesReflectFinancialState(t *testing.T) {
	renderWith := func(reais string, negative bool) string {
		data := relatoriosPremiumTestData(false)
		data.CashMonthResultTotal = MoneyDisplay{Reais: reais, Cents: ",00"}
		data.CashMonthResultNegative = negative
		return renderRelatoriosPremiumPage(t, data)
	}

	positive := kpiCardWindow(t, renderWith("8.024", false), "Resultado realizado")
	assertContains(t, positive, `class="ui-kpi ui-kpi--success"`)
	assertContains(t, positive, `ui-kpi__value" data-sensitive>R$ 8.024,00</strong>`)

	negative := kpiCardWindow(t, renderWith("-8.024", true), "Resultado realizado")
	assertContains(t, negative, `class="ui-kpi ui-kpi--danger"`)
	if strings.Contains(negative, "ui-kpi--success") {
		t.Fatal("negative result must not use the success tone")
	}

	zero := kpiCardWindow(t, renderWith("0", false), "Resultado realizado")
	if strings.Contains(zero, "ui-kpi--success") || strings.Contains(zero, "ui-kpi--danger") {
		t.Fatal("zero result must stay neutral, not green or red")
	}
	assertContains(t, zero, `ui-kpi__value" data-sensitive>R$ 0,00</strong>`)

	// Competência segue a mesma regra de estado.
	data := relatoriosPremiumTestData(true)
	data.ActiveTab = "competencia"
	data.SaldoLiquido = MoneyDisplay{Reais: "-1.500", Cents: ",00"}
	data.SaldoNegativo = true
	competencia := kpiCardWindow(t, renderRelatoriosPremiumPage(t, data), "Resultado")
	assertContains(t, competencia, `class="ui-kpi ui-kpi--danger"`)
	assertContains(t, competencia, `R$ -1.500,00`)
}

func TestRelatoriosMovementCountStaysNonMonetary(t *testing.T) {
	personal := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))
	assertContains(t, personal, `<strong class="ui-kpi__value">7</strong>`)
	if strings.Contains(personal, `<strong class="ui-kpi__value" data-sensitive>7</strong>`) {
		t.Fatal("movement count must not be masked as a monetary value")
	}

	business := relatoriosPremiumTestData(true)
	business.ActiveTab = "contabil"
	business.ContabilExtratoMovementCount = 12
	contabil := renderRelatoriosPremiumPage(t, business)
	assertContains(t, contabil, `<strong class="ui-kpi__value">12</strong>`)
	if strings.Contains(contabil, `<strong class="ui-kpi__value" data-sensitive>12</strong>`) {
		t.Fatal("contabil movement count must stay a plain count")
	}
}

func TestRelatoriosModeSelectorUsesMetasTabLanguage(t *testing.T) {
	realizado := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))

	assertContains(t, realizado, `class="cb-tabs cb-v2-relatorios-modes"`)
	assertContains(t, realizado, `aria-label="Modos do fluxo de caixa"`)
	assertContains(t, realizado, `class="cb-tab cb-tab-active"`)
	assertContains(t, realizado, `class="cb-tab cb-tab-inactive"`)
	for _, expected := range []string{
		`hx-get="/relatorios?tab=caixa&modo=realizado&mes=10&ano=2026&partial=content"`,
		`hx-get="/relatorios?tab=caixa&modo=previsao&mes=10&ano=2026&partial=content"`,
		`hx-target="#relatorios-body"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="true"`,
	} {
		assertContains(t, realizado, expected)
	}

	// O modo ativo é o único com a seleção suave; o inativo não vira CTA.
	activeIdx := strings.Index(realizado, `class="cb-tab cb-tab-active"`)
	inactiveIdx := strings.Index(realizado, `class="cb-tab cb-tab-inactive"`)
	if activeIdx < 0 || inactiveIdx < 0 || activeIdx > inactiveIdx {
		t.Fatal("realizado must be the active tab and previsao the inactive one")
	}
	modesWindow := realizado[strings.Index(realizado, `cb-v2-relatorios-modes`):]
	modesWindow = modesWindow[:strings.Index(modesWindow, "</nav>")]
	for _, forbidden := range []string{"cb-button", "shadow-sm", "text-white", "bg-[var(--cb-accent)]"} {
		if strings.Contains(modesWindow, forbidden) {
			t.Fatalf("mode selector must not use primary-action styling %q", forbidden)
		}
	}

	previsao := relatoriosPremiumTestData(false)
	previsao.IsCaixaPrevisao = true
	previsao.IsCaixaRealizado = false
	previsaoBody := renderRelatoriosPremiumPage(t, previsao)
	if strings.Index(previsaoBody, `class="cb-tab cb-tab-inactive"`) > strings.Index(previsaoBody, `class="cb-tab cb-tab-active"`) {
		t.Fatal("previsao must be the active tab when the forecast mode is selected")
	}
	assertContains(t, previsaoBody, "Realizado")
	assertContains(t, previsaoBody, "Previsão")
}

func TestRelatoriosCSSKPICompositionIsResponsiveAndTokenBased(t *testing.T) {
	css := readTestFile(t, "assets/css/relatorios.css")
	for _, expected := range []string{
		`.cb-v2-relatorios-modes`,
		`width: 100%;`,
		`.cb-v2-relatorios-modes .cb-tab.cb-tab-active`,
		`color: var(--ui-tab-active-fg);`,
		`.cb-v2-relatorios-kpis {`,
		`grid-template-columns: repeat(2, minmax(0, 1fr));`,
		`@media (min-width: 768px)`,
		`.cb-v2-relatorios-kpis--3`,
		`@media (min-width: 1280px)`,
		`.cb-v2-relatorios-kpis--4`,
		`.cb-v2-relatorios-kpis > .ui-kpi:last-child`,
		`var(--ui-info)`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("relatorios.css missing KPI composition rule %q", expected)
		}
	}

	// A linguagem de seleção é a de Metas (design-system.css); a página só
	// restaura a cor para âncoras e não redeclara a geometria/paleta das tabs.
	for _, forbidden := range []string{`.cb-tabs {`, `!important`} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("relatorios.css must not redeclare the shared tab language: %q", forbidden)
		}
	}
}
