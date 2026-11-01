package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// M3 — Indicadores financeiros Premium de /metas.
//
// Contratos cobertos aqui:
//   - o resumo de cada aba é um único .ui-panel com .ui-kpi (não cards soltos);
//   - os rótulos/valores existentes são preservados (sem indicadores novos);
//   - valores zero permanecem neutros e o saldo livre negativo usa danger;
//   - todos os valores monetários dos indicadores continuam [data-sensitive];
//   - o swap OOB de #metas-tabs e os IDs/erros OOB continuam intactos;
//   - a composição responsiva é por container query (1/2/3 métricas).

func renderMetasPanel(t *testing.T, tpl *template.Template, name string, data any) string {
	t.Helper()

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return out.String()
}

func TestMetasOverviewLimitsAdoptsPremiumIndicators(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	body := renderMetasPanel(t, tpl, "metas-limites-panel", MetasData{
		Aba:          "limites",
		LimitesCount: 2,
		TotalLimits:  MoneyDisplay{Reais: "1.234", Cents: ",56"},
	})

	for _, expected := range []string{
		`<section class="ui-panel metas-overview" aria-labelledby="metas-limites-overview-title">`,
		`<header class="ui-section-heading metas-overview__head">`,
		`<span class="ui-section-icon" aria-hidden="true"><i data-lucide="gauge"></i></span>`,
		`<h2 id="metas-limites-overview-title">Limites</h2>`,
		`<span class="ui-pill">2 limites ativos</span>`,
		`<div class="ui-kpi-grid metas-overview__grid metas-overview__grid--1">`,
		`<div class="ui-kpi metas-overview__metric metas-overview__metric--primary">`,
		`<span class="ui-kpi__label">Gastos este mês</span>`,
		`<strong class="ui-kpi__value" data-sensitive>R$ 1.234,56</strong>`,
	} {
		assertContains(t, body, expected)
	}

	if got := strings.Count(body, `<div class="ui-kpi `); got != 1 {
		t.Fatalf("limites overview must keep exactly one KPI, got %d", got)
	}
	for _, forbidden := range []string{
		"metas-summary",
		"ui-kpi--success",
		"ui-kpi--danger",
		"text-[var(--cb-success)]",
		"text-[var(--cb-danger)]",
		"Limite configurado",
		"Valor restante",
		"Percentual utilizado",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("limites overview must not contain %q", forbidden)
		}
	}
}

func TestMetasOverviewLimitsCountAndZeroStates(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	cases := []struct {
		count int
		label string
	}{
		{count: 0, label: "0 limites ativos"},
		{count: 1, label: "1 limite ativo"},
		{count: 2, label: "2 limites ativos"},
	}

	for _, tc := range cases {
		body := renderMetasPanel(t, tpl, "metas-limites-panel", MetasData{
			Aba:          "limites",
			LimitesCount: tc.count,
			TotalLimits:  MoneyDisplay{Reais: "0", Cents: ",00"},
		})

		assertContains(t, body, `<span class="ui-pill">`+tc.label+`</span>`)
		assertContains(t, body, `<strong class="ui-kpi__value" data-sensitive>R$ 0,00</strong>`)
		if strings.Contains(body, "ui-kpi--danger") || strings.Contains(body, "ui-kpi--success") {
			t.Fatalf("zero limit spend must stay neutral (count=%d)", tc.count)
		}
	}
}

func TestMetasOverviewReservationsAdoptsPremiumIndicators(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	body := renderMetasPanel(t, tpl, "metas-caixinhas-panel", MetasData{
		Aba:                     "caixinhas",
		CaixinhasCount:          6,
		ReservedBalance:         MoneyDisplay{Reais: "226.489", Cents: ",49"},
		FreeBalance:             MoneyDisplay{Reais: "98.140", Cents: ",38"},
		HasMonthlyPlanning:      true,
		ProgrammedContributions: MoneyDisplay{Reais: "20.820", Cents: ",00"},
		HasCoverage:             true,
		CoverageText:            "Cobre cerca de 4,8 meses",
		CoverageSourceText:      "Baseado na média dos custos essenciais dos últimos 5 meses completos.",
		HasMargin:               true,
		Margin:                  MoneyDisplay{Reais: "1.885", Cents: ",00"},
	})

	for _, expected := range []string{
		`<section class="ui-panel metas-overview" aria-labelledby="metas-reservas-overview-title">`,
		`<h2 id="metas-reservas-overview-title">Reservas</h2>`,
		`<span class="ui-pill">6 reservas</span>`,
		`<div class="ui-kpi-grid metas-overview__grid metas-overview__grid--3">`,
		`<span class="ui-kpi__label">Reservado</span>`,
		`<strong class="ui-kpi__value" data-sensitive>R$ 226.489,49</strong>`,
		`<span class="ui-kpi__label">Disponível</span>`,
		`<strong class="ui-kpi__value" data-sensitive>R$ 98.140,38</strong>`,
		`<span class="ui-kpi__meta">Cobre cerca de 4,8 meses</span>`,
		`<span class="ui-kpi__meta metas-overview__source">Baseado na média dos custos essenciais dos últimos 5 meses completos.</span>`,
		`<span class="ui-kpi__label">Aportes programados</span>`,
		`<strong class="ui-kpi__value" data-sensitive>R$ 20.820,00/mês</strong>`,
		`Margem mensal estimada: <span data-sensitive>R$ 1.885,00</span>`,
	} {
		assertContains(t, body, expected)
	}

	reserved := strings.Index(body, "Reservado")
	free := strings.Index(body, "Disponível")
	programmed := strings.Index(body, "Aportes programados")
	if reserved < 0 || free < 0 || programmed < 0 || !(reserved < free && free < programmed) {
		t.Fatalf("reservation indicators must keep the reserved -> free -> programmed order")
	}
	if got := strings.Count(body, "data-sensitive"); got != 4 {
		t.Fatalf("reservation overview must mark exactly four monetary values as sensitive, got %d", got)
	}
	for _, forbidden := range []string{
		"metas-summary",
		"ui-kpi--success",
		"text-[var(--cb-success)]",
		"text-[var(--cb-danger)]",
		"Receita",
		"Despesa",
		"Saldo total",
		"Patrimônio",
		"Total acumulado",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("reservation overview must not contain %q", forbidden)
		}
	}
}

func TestMetasOverviewReservationsConditionalAndNegativeStates(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	t.Run("negative free balance uses danger only", func(t *testing.T) {
		body := renderMetasPanel(t, tpl, "metas-caixinhas-panel", MetasData{
			Aba:                 "caixinhas",
			CaixinhasCount:      1,
			ReservedBalance:     MoneyDisplay{Reais: "2.000", Cents: ",00"},
			FreeBalance:         MoneyDisplay{Reais: "-50", Cents: ",00"},
			FreeBalanceNegative: true,
		})

		assertContains(t, body, `class="ui-kpi metas-overview__metric ui-kpi--danger"`)
		assertContains(t, body, `<strong class="ui-kpi__value" data-sensitive>R$ -50,00</strong>`)
		if strings.Contains(body, "ui-kpi--success") {
			t.Fatal("negative free balance must not use success tone")
		}
	})

	t.Run("zero free balance stays neutral", func(t *testing.T) {
		body := renderMetasPanel(t, tpl, "metas-caixinhas-panel", MetasData{
			Aba:                 "caixinhas",
			CaixinhasCount:      1,
			ReservedBalance:     MoneyDisplay{Reais: "0", Cents: ",00"},
			FreeBalance:         MoneyDisplay{Reais: "0", Cents: ",00"},
			FreeBalanceNegative: false,
		})

		assertContains(t, body, `<strong class="ui-kpi__value" data-sensitive>R$ 0,00</strong>`)
		if strings.Contains(body, "ui-kpi--danger") || strings.Contains(body, "ui-kpi--success") {
			t.Fatal("zero free balance must stay neutral")
		}
	})

	t.Run("no monthly planning drops the third indicator", func(t *testing.T) {
		body := renderMetasPanel(t, tpl, "metas-caixinhas-panel", MetasData{
			Aba:                     "caixinhas",
			CaixinhasCount:          2,
			ReservedBalance:         MoneyDisplay{Reais: "10", Cents: ",00"},
			FreeBalance:             MoneyDisplay{Reais: "90", Cents: ",00"},
			HasMonthlyPlanning:      false,
			HasCoverage:             false,
			ProgrammedContributions: MoneyDisplay{Reais: "0", Cents: ",00"},
		})

		assertContains(t, body, `metas-overview__grid metas-overview__grid--2`)
		if strings.Contains(body, "Aportes programados") {
			t.Fatal("programmed contributions must not render without planning data")
		}
		if strings.Contains(body, "Cobre cerca de") || strings.Contains(body, "Margem mensal") {
			t.Fatal("coverage/margin must not render without their source data")
		}
		if got := strings.Count(body, `<div class="ui-kpi `); got != 2 {
			t.Fatalf("reservation overview without planning must render two indicators, got %d", got)
		}
	})
}

func TestMetasOverviewPrivacyContract(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	limits := renderMetasPanel(t, tpl, "metas-limites-panel", MetasData{
		Aba:          "limites",
		LimitesCount: 1,
		TotalLimits:  MoneyDisplay{Reais: "1.234", Cents: ",56"},
	})
	reserves := renderMetasPanel(t, tpl, "metas-caixinhas-panel", MetasData{
		Aba:                     "caixinhas",
		CaixinhasCount:          1,
		ReservedBalance:         MoneyDisplay{Reais: "226.489", Cents: ",49"},
		FreeBalance:             MoneyDisplay{Reais: "98.140", Cents: ",38"},
		HasMonthlyPlanning:      true,
		ProgrammedContributions: MoneyDisplay{Reais: "20.820", Cents: ",00"},
		HasMargin:               true,
		Margin:                  MoneyDisplay{Reais: "1.885", Cents: ",00"},
	})

	for _, expected := range []string{
		`<strong class="ui-kpi__value" data-sensitive>R$ 1.234,56</strong>`,
	} {
		assertContains(t, limits, expected)
	}
	for _, expected := range []string{
		`<strong class="ui-kpi__value" data-sensitive>R$ 226.489,49</strong>`,
		`<strong class="ui-kpi__value" data-sensitive>R$ 98.140,38</strong>`,
		`<strong class="ui-kpi__value" data-sensitive>R$ 20.820,00/mês</strong>`,
		`Margem mensal estimada: <span data-sensitive>R$ 1.885,00</span>`,
	} {
		assertContains(t, reserves, expected)
	}

	// Todo valor monetário do resumo precisa continuar dentro de um nó
	// [data-sensitive]; a contagem pega qualquer valor novo que escape.
	for name, body := range map[string]string{"limites": limits, "reservas": reserves} {
		money := strings.Count(body, "R$")
		sensitive := strings.Count(body, "data-sensitive")
		if money == 0 {
			t.Fatalf("%s overview must render its monetary values", name)
		}
		if sensitive != money {
			t.Fatalf("%s overview has %d monetary values but %d data-sensitive markers", name, money, sensitive)
		}
	}

	for _, body := range []string{limits, reserves} {
		if strings.Contains(body, `aria-label="R$`) || strings.Contains(body, `title="R$`) {
			t.Fatal("overview indicators must not expose monetary values in attributes")
		}
	}

	css := readTestFile(t, "assets/css/metas.css")
	for _, expected := range []string{
		`.privacy-enabled .cb-v2-metas-main [data-sensitive]`,
		`content: "••••"`,
		`.metas-overview__metric .ui-kpi__value[data-sensitive]::after`,
		`.metas-overview__grid--3 .metas-overview__metric.ui-kpi:nth-child(3) .ui-kpi__value[data-sensitive]::after`,
		`justify-content: flex-end`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("metas.css missing M3 privacy contract %q", expected)
		}
	}
}

func TestMetasOverviewKeepsHTMXAndOOBContract(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	partial := renderMetasPanel(t, tpl, "metas-tabs", MetasData{
		Aba:             "limites",
		ContentOOB:      true,
		HeaderActionOOB: true,
		LimitesCount:    0,
		TotalLimits:     MoneyDisplay{Reais: "0", Cents: ",00"},
	})

	for _, expected := range []string{
		`id="metas-tabs"`,
		`data-active-tab="limites"`,
		`hx-swap-oob="outerHTML"`,
		`hx-get="/metas?aba=limites&partial=conteudo"`,
		`hx-target="#metas-tabs" hx-swap="outerHTML"`,
		`hx-push-url="/metas?aba=limites"`,
		`hx-get="/metas?aba=reservas&partial=conteudo"`,
		`id="aporte-form-error"`,
		`id="resgate-form-error"`,
		`id="metas-primary-action"`,
		`<section class="ui-panel metas-overview"`,
	} {
		assertContains(t, partial, expected)
	}
	if got := strings.Count(partial, `id="metas-tabs"`); got != 1 {
		t.Fatalf("metas-tabs must be rendered exactly once, got %d", got)
	}
	if got := strings.Count(partial, `<section class="ui-panel metas-overview"`); got != 1 {
		t.Fatalf("overview panel must be rendered exactly once after an HTMX swap, got %d", got)
	}

	full := renderMetasPanel(t, tpl, "metas-page", MetasData{
		Title:               "Metas",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		Aba:                 "limites",
		LimitesCount:        2,
		TotalLimits:         MoneyDisplay{Reais: "0", Cents: ",00"},
	})
	for _, expected := range []string{
		`data-ui="premium"`,
		`data-page-section="goals"`,
		`id="metas-tabs"`,
		`id="metas-primary-action"`,
		`id="bottom-sheet-container"`,
		`data-privacy-toggle`,
		`id="metas-limites-overview-title"`,
	} {
		assertContains(t, full, expected)
	}
}

func TestMetasOverviewResponsiveContract(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	for _, expected := range []string{
		`container: metas-overview / inline-size;`,
		`@container metas-overview (max-width: 559px)`,
		`@container metas-overview (min-width: 560px)`,
		`@container metas-overview (min-width: 940px)`,
		`grid-template-columns: minmax(0, 1fr);`,
		`grid-template-columns: repeat(2, minmax(0, 1fr));`,
		`grid-template-columns: repeat(3, minmax(0, 1fr));`,
		`metas-overview__grid--2`,
		`metas-overview__grid--3`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("metas.css missing responsive contract %q", expected)
		}
	}

	page := readTestFile(t, "templates/pages/metas.html")
	assertContains(t, page, `metas-overview__grid--{{if .HasMonthlyPlanning}}3{{else}}2{{end}}`)
	assertContains(t, page, `metas-overview__grid--1`)
}

func TestMetasOverviewRemovesLegacySummaryMarkup(t *testing.T) {
	page := readTestFile(t, "templates/pages/metas.html")
	css := readTestFile(t, "assets/css/metas.css")

	for _, legacy := range []string{
		"metas-summary-card",
		"metas-summary-head",
		"metas-summary-title",
		"metas-summary-icon",
		"metas-summary-chip",
		"metas-summary-metrics",
		"metas-summary-metric",
		"metas-summary-label",
		"metas-summary-value",
		"metas-summary-aux",
		"metas-summary-source",
	} {
		if strings.Contains(page, legacy) {
			t.Fatalf("metas.html still consumes legacy summary class %q", legacy)
		}
		if strings.Contains(css, legacy) {
			t.Fatalf("metas.css still defines legacy summary class %q", legacy)
		}
	}

	assertContains(t, page, "metas-overview")
	assertContains(t, css, ".metas-overview__metric.ui-kpi")
}
