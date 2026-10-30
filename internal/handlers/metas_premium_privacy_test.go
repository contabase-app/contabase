package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// M2 — Cabeçalho Premium e privacidade financeira de /metas.
//
// Contratos cobertos aqui:
//   - markup canônico do header (ui-page-header/eyebrow/title/description) e
//     o controle de privacidade global (data-privacy-toggle + aria + ícone);
//   - o botão de ação do header mantém id/hx-* e adota ui-btn--primary;
//   - rótulos acessíveis de progresso não expõem valores monetários;
//   - sheets de /metas são escopados por [data-metas-sheet] e marcam valores
//     exibidos com data-sensitive;
//   - metas.css cobre o modo privacidade no padrão de Faturas/Lançamentos.

func TestMetasPageAdoptsPremiumHeaderAndPrivacyToggle(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	data := MetasData{
		Title:               "Metas",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		Aba:                 "limites",
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-page", data); err != nil {
		t.Fatalf("render metas page: %v", err)
	}
	body := out.String()

	for _, expected := range []string{
		`data-ui="premium"`,
		`/assets/css/premium.css?v=test`,
		`cb-v2-metas-header ui-page-header`,
		`class="ui-eyebrow">Planejamento`,
		`class="ui-page-title">Metas e Reservas`,
		`class="ui-page-description">Defina limites de gastos`,
		`data-privacy-toggle`,
		`aria-pressed="false"`,
		`aria-label="Ocultar valores financeiros"`,
		`data-tooltip-hover data-tooltip-text="Ocultar valores financeiros"`,
		`data-lucide="eye" data-privacy-icon`,
		`id="metas-primary-action"`,
		`cb-v2-metas-new ui-btn ui-btn--primary`,
	} {
		assertContains(t, body, expected)
	}

	// O eyebrow antigo do shell não pode mais ser o rótulo do cabeçalho.
	if strings.Contains(body, `cb-v2-eyebrow">Planejamento`) {
		t.Fatal("metas header must use ui-eyebrow, not the legacy shell eyebrow")
	}
	// premium.css precisa carregar antes do CSS da própria página.
	if strings.Index(body, "/assets/css/premium.css?v=test") > strings.Index(body, "/assets/css/metas.css?v=test") {
		t.Fatal("premium.css must load before metas.css")
	}
}

func TestMetasHeaderActionKeepsHTMXContractWithPremiumButton(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	cases := []struct {
		aba        string
		isBusiness bool
		label      string
		queryAba   string
	}{
		{aba: "limites", label: "Novo limite", queryAba: "limite"},
		{aba: "caixinhas", label: "Nova reserva", queryAba: "reserva"},
		{aba: "limites", isBusiness: true, label: "Nova meta", queryAba: "limite"},
	}

	for _, tc := range cases {
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "metas-header-action", MetasData{Aba: tc.aba, IsBusiness: tc.isBusiness}); err != nil {
			t.Fatalf("render header action (%s): %v", tc.aba, err)
		}
		body := out.String()
		for _, expected := range []string{
			`id="metas-primary-action"`,
			`cb-v2-metas-new ui-btn ui-btn--primary`,
			`hx-get="/metas/novo?aba=` + tc.queryAba,
			`hx-target="#bottom-sheet-container"`,
			`hx-swap="innerHTML"`,
			`<span>` + tc.label + `</span>`,
		} {
			assertContains(t, body, expected)
		}
		if strings.Contains(body, `hx-swap-oob`) {
			t.Fatalf("header action without OOB must not carry hx-swap-oob (aba=%s)", tc.aba)
		}
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-header-action", MetasData{Aba: "caixinhas", HeaderActionOOB: true}); err != nil {
		t.Fatalf("render header action OOB: %v", err)
	}
	assertContains(t, out.String(), `hx-swap-oob="outerHTML"`)
}

func TestMetasProgressAriaLabelsHideAmounts(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	renderLimit := func(percent int) string {
		card := LimiteCard{
			ID:           "lim-1",
			CategoryID:   "cat-1",
			CategoryName: "Alimentação",
			MaxAmount:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
			Spent:        MoneyDisplay{Reais: "720", Cents: ",00"},
			Remaining:    MoneyDisplay{Reais: "1.080", Cents: ",00"},
			Percent:      percent,
		}
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "metas-limit-card", card); err != nil {
			t.Fatalf("render limit card: %v", err)
		}
		return out.String()
	}

	ongoing := renderLimit(40)
	if !strings.Contains(ongoing, `aria-label="Alimentação: 60% do limite disponível"`) {
		t.Fatalf("ongoing limit aria-label must describe available percentage, got:\n%s", ariaOf(t, ongoing))
	}
	if strings.Contains(ongoing, `aria-label="Alimentação: R$`) {
		t.Fatal("limit aria-label must not expose monetary amounts")
	}

	exceeded := renderLimit(120)
	if !strings.Contains(exceeded, `aria-label="Alimentação: limite excedido"`) {
		t.Fatalf("exceeded limit aria-label must describe the state, got:\n%s", ariaOf(t, exceeded))
	}
	if strings.Contains(exceeded, `aria-label="Alimentação: R$`) {
		t.Fatal("exceeded limit aria-label must not expose monetary amounts")
	}

	box := CaixinhaCard{
		ID:            "box-1",
		Name:          "Trocar de Carro",
		Target:        MoneyDisplay{Reais: "90.000", Cents: ",00"},
		Balance:       MoneyDisplay{Reais: "68.400", Cents: ",00"},
		Percent:       76,
		DeadlineLabel: "mar/2028",
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-caixinha-card", box); err != nil {
		t.Fatalf("render caixinha card: %v", err)
	}
	body := out.String()
	assertContains(t, body, `76%`)
	assertContains(t, body, `Perto do alvo, 76% atingido`)
	if strings.Contains(body, `aria-label="Trocar de Carro: R$`) {
		t.Fatal("caixinha aria-label must not expose monetary amounts")
	}
}

func TestMetasCaixinhasPanelMarksMarginAsSensitive(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	data := MetasData{
		Aba:                     "caixinhas",
		CaixinhasCount:          2,
		ReservedBalance:         MoneyDisplay{Reais: "226.489", Cents: ",49"},
		FreeBalance:             MoneyDisplay{Reais: "98.140", Cents: ",38"},
		HasMonthlyPlanning:      true,
		ProgrammedContributions: MoneyDisplay{Reais: "20.820", Cents: ",00"},
		HasMargin:               true,
		Margin:                  MoneyDisplay{Reais: "4.500", Cents: ",00"},
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-caixinhas-panel", data); err != nil {
		t.Fatalf("render caixinhas panel: %v", err)
	}
	body := out.String()
	assertContains(t, body, `Margem mensal estimada: <span data-sensitive>R$ 4.500,00</span>`)
}

func TestMetasSheetsArePrivacyScopedAndMarkSensitive(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	formTpl := testMetasFormTemplates(t)

	render := func(tpl *template.Template, name string, data any) string {
		t.Helper()
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, name, data); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return out.String()
	}

	history := render(tpl, "box-history", struct {
		BoxID      string
		BoxName    string
		Events     []BoxHistoryEvent
		HasEvents  bool
		IsBusiness bool
	}{
		BoxName: "Aposentadoria",
		Events: []BoxHistoryEvent{{
			Description: "Aporte manual",
			TypeLabel:   "Aporte",
			DateLabel:   "12 de outubro",
			IsCredit:    true,
			Money:       MoneyDisplay{Reais: "18.000", Cents: ",00"},
		}},
		HasEvents: true,
	})
	assertContains(t, history, `data-metas-sheet`)
	assertContains(t, history, `role="dialog"`)
	assertContains(t, history, `aria-modal="true"`)
	assertContains(t, history, `class="ui-row__amount metas-history__amount metas-history__amount--credit" data-sensitive>`)
	assertContains(t, history, `<span class="sr-only">Valor do evento: </span>+R$ 18.000,00`)

	aporte := render(tpl, "caixinha-aporte-sheet", BoxSheetData{
		BoxID:        "box-1",
		BoxName:      "Aposentadoria",
		Balance:      MoneyDisplay{Reais: "184.912", Cents: ",09"},
		Target:       MoneyDisplay{Reais: "5.000.000", Cents: ",00"},
		CategoryName: "Aposentadoria",
	})
	assertContains(t, aporte, `data-metas-sheet`)
	assertContains(t, aporte, `metas-form__summary-value metas-form__summary-value--money" data-sensitive>R$ 184.912,09`)

	limitHistory := render(tpl, "limit-history", struct {
		LimitID       string
		LimitName     string
		CategoryID    string
		CategoryName  string
		CategoryIcon  string
		CategoryColor string
		MaxAmount     MoneyDisplay
		Spent         MoneyDisplay
		Remaining     MoneyDisplay
		Percent       int
		Transactions  []struct {
			Description  string
			DateLabel    string
			Amount       MoneyDisplay
			ID           string
			AccountName  string
			AccountLabel string
		}
		HasTransactions bool
		IsBusiness      bool
	}{
		LimitName:    "Alimentação",
		CategoryName: "Alimentação",
		MaxAmount:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
		Spent:        MoneyDisplay{Reais: "0", Cents: ",00"},
		Remaining:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
		Percent:      0,
		Transactions: []struct {
			Description  string
			DateLabel    string
			Amount       MoneyDisplay
			ID           string
			AccountName  string
			AccountLabel string
		}{{
			Description: "Supermercado",
			DateLabel:   "12 de outubro",
			Amount:      MoneyDisplay{Reais: "320", Cents: ",00"},
		}},
		HasTransactions: true,
	})
	assertContains(t, limitHistory, `data-metas-sheet`)
	assertContains(t, limitHistory, `role="dialog"`)
	assertContains(t, limitHistory, `class="ui-row__amount metas-history__amount metas-history__amount--expense" data-sensitive>`)
	assertContains(t, limitHistory, `<span class="sr-only">Valor do gasto: </span>R$ 320,00`)

	form := render(formTpl, "metas-form", MetaFormData{
		Mode:         "edit",
		OpenTab:      "caixinha",
		BoxID:        "box-1",
		Name:         "Aposentadoria",
		BalanceLabel: "R$ 184.912,09",
	})
	assertContains(t, form, `data-metas-sheet`)
	assertContains(t, form, `class="metas-form__summary-value" data-sensitive>R$ 184.912,09`)
}

func TestMetasCSSPrivacyCoverageFollowsCanonicalPattern(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	for _, expected := range []string{
		`.privacy-enabled .cb-v2-metas-main [data-sensitive]`,
		`.privacy-enabled [data-metas-sheet] [data-sensitive]`,
		`.privacy-enabled [data-metas-sheet] .metas-history__amount[data-sensitive]::after`,
		`content: "••••"`,
		`visibility: hidden`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("metas.css missing privacy coverage %q", expected)
		}
	}
	// O mascaramento não pode alcançar inputs editáveis.
	if strings.Contains(css, `[data-metas-sheet] input`) && strings.Contains(css, `visibility: hidden`) &&
		strings.Index(css, `[data-metas-sheet] input`) < strings.Index(css, `content: "••••"`) {
		t.Fatal("privacy rules must not mask editable inputs")
	}
}

func ariaOf(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `aria-label="`)
	if start < 0 {
		return "(no aria-label)"
	}
	rest := body[start+len(`aria-label="`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "(unterminated)"
	}
	return body[start : start+len(`aria-label="`)+end+1]
}
