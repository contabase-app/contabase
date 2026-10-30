package handlers

import (
	"fmt"
	"strings"
	"testing"
)

// M4 — Cards de Limites e Reservas.
//
// Contratos cobertos aqui:
//   - a hierarquia do card usa as classes locais .metas-card/.metas-card__* e
//     os componentes canônicos ui-badge/ui-panel, sem cb-card-dense nem
//     cb-status-chip;
//   - o valor dominante, o alvo, o progresso e o rodapé preservam os valores
//     calculados pelo handler;
//   - a barra de Limites continua exibindo o DISPONÍVEL (não o consumido);
//   - Reservas sem alvo não inventam barra nem percentual;
//   - o menu mantém ações, permissões, estado disabled e aria-controls;
//   - a quantidade de cards não é fixa;
//   - todos os valores monetários continuam [data-sensitive] e fora de
//     aria-label/title;
//   - a geometria Premium e o alvo de toque de 44px são contratos de CSS.

func renderMetasCard(t *testing.T, name string, data any) string {
	t.Helper()
	return renderMetasPanel(t, testMetasPageTemplates(t), name, data)
}

func testLimitCard(id string, consumedPercent int) LimiteCard {
	// Os valores monetários não são recalculados aqui: o contrato em teste é a
	// semântica do percentual disponível, que o handler já entrega pronto.
	return LimiteCard{
		ID:           id,
		CategoryID:   "cat-food",
		CategoryName: "Alimentação",
		CategoryIcon: "utensils",
		MaxAmount:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
		Spent:        MoneyDisplay{Reais: "0", Cents: ",00"},
		Remaining:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
		Percent:      consumedPercent,
	}
}

func testReserveCard(id string, percent int) CaixinhaCard {
	return CaixinhaCard{
		ID:              id,
		CategoryID:      "cat-retirement",
		CategoryName:    "Aposentadoria",
		Name:            "Aposentadoria",
		Icon:            "piggy-bank",
		Target:          MoneyDisplay{Reais: "90.000", Cents: ",00"},
		Balance:         MoneyDisplay{Reais: "68.400", Cents: ",00"},
		Percent:         percent,
		MonthlyRecharge: MoneyDisplay{Reais: "500", Cents: ",00"},
		ForecastLabel:   "Previsão: 12 meses",
		DeadlineLabel:   "mar/2028",
		RequiredState:   requiredStateValue,
	}
}

func TestMetasLimitCardAdoptsPremiumCardContract(t *testing.T) {
	body := renderMetasCard(t, "metas-limit-card", testLimitCard("lim-1", 0))

	for _, expected := range []string{
		`<article data-meta-card class="metas-card metas-card--limit relative flex flex-col">`,
		`class="metas-card__link absolute inset-0 z-0 rounded-[inherit]"></a>`,
		`<p class="metas-card-title">Alimentação</p>`,
		`<p class="metas-card-subtitle">Limite mensal</p>`,
		`class="cb-meta-menu-toggle metas-card__menu-toggle"`,
		`aria-label="Ações do limite" aria-expanded="false" aria-controls="metas-limite-menu-lim-1"`,
		`<div id="metas-limite-menu-lim-1" data-ck-panel class="metas-menu-panel hidden">`,
		`<p class="metas-box-label">Disponível</p>`,
		`<p class="metas-box-value metas-box-value-ok" data-sensitive>R$ 1.800,00</p>`,
		`<div class="metas-card__progress metas-progress-wrap metas-progress-tone-ok" data-metas-p="100">`,
		`<progress class="metas-progress" value="100" max="100" aria-label="Alimentação: 100% do limite disponível">100%</progress>`,
		`<span class="metas-progress-chip" aria-hidden="true">100% disponível</span>`,
		`<span class="metas-card__fact-label">Utilizado</span>`,
		`<span class="metas-card__fact-value" data-sensitive>R$ 0,00</span>`,
		`<span class="metas-card__fact-label">Limite</span>`,
		`<span class="metas-card__fact-value" data-sensitive>R$ 1.800,00</span>`,
	} {
		assertContains(t, body, expected)
	}

	for _, forbidden := range []string{
		"cb-card-dense",
		"cb-status-chip",
		"metas-metrics",
		"metas-value",
		"metas-box-foot-item",
		"metas-summary",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("limit card still contains legacy contract %q", forbidden)
		}
	}

	if got := strings.Count(body, "data-sensitive"); got != 3 {
		t.Fatalf("limit card monetary fields = %d, want 3 (disponível, utilizado, limite)", got)
	}
}

func TestMetasReserveCardAdoptsPremiumCardContract(t *testing.T) {
	body := renderMetasCard(t, "metas-caixinha-card", testReserveCard("box-1", 76))

	for _, expected := range []string{
		`<article data-meta-card class="metas-card metas-card--reserve relative flex flex-col">`,
		`<p class="metas-card-title">Aposentadoria</p>`,
		`<span class="ui-badge ui-badge--success metas-card__badge">mar/2028</span>`,
		`aria-label="Ações da reserva" aria-expanded="false" aria-controls="metas-caixinha-menu-box-1"`,
		`<div id="metas-caixinha-menu-box-1" data-ck-panel class="metas-menu-panel hidden">`,
		`<p class="metas-box-label">Saldo</p>`,
		`<p class="metas-box-value metas-box-value-ok" data-sensitive>R$ 68.400,00</p>`,
		`<p class="metas-box-label">Alvo</p>`,
		`<p class="metas-box-target" data-sensitive>R$ 90.000,00</p>`,
		`data-metas-p="76"`,
		`<progress class="metas-progress" value="76" max="100" aria-label="Aposentadoria: Perto do alvo, 76% atingido">76%</progress>`,
		`<span class="metas-progress-chip" aria-hidden="true">76% do alvo</span>`,
		`<span class="metas-card__footer-item">`,
		`Aporte <b class="metas-box-foot-value" data-sensitive>R$ 500,00/mês</b>`,
		`Previsão: 12 meses`,
	} {
		assertContains(t, body, expected)
	}

	for _, forbidden := range []string{
		"cb-card-dense",
		"cb-status-chip",
		"metas-metrics",
		"metas-box-foot-item",
		`<span class="metas-progress-chip" aria-hidden="true">76%</span>`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("reserve card still contains legacy contract %q", forbidden)
		}
	}

	if got := strings.Count(body, "data-sensitive"); got != 3 {
		t.Fatalf("reserve card monetary fields = %d, want 3 (saldo, alvo, aporte)", got)
	}
}

func TestMetasReserveCardWithoutTargetSkipsProgressAndFooter(t *testing.T) {
	card := testReserveCard("box-2", 0)
	card.Target = MoneyDisplay{Reais: "0", Cents: ",00"}
	card.Balance = MoneyDisplay{Reais: "1.200", Cents: ",00"}
	card.MonthlyRecharge = MoneyDisplay{Reais: "0", Cents: ",00"}
	card.ForecastLabel = ""
	card.RequiredState = requiredStateNoDeadline
	card.DeadlineLabel = "Sem prazo"
	card.CategoryName = "Sem categoria"

	body := renderMetasCard(t, "metas-caixinha-card", card)

	for _, expected := range []string{
		`<span class="ui-badge ui-badge--neutral metas-card__badge">Sem prazo</span>`,
		`<p class="metas-box-label">Alvo</p>`,
		`<p class="metas-box-target metas-box-target-empty">Sem alvo</p>`,
		`<p class="metas-box-label">Saldo</p>`,
	} {
		assertContains(t, body, expected)
	}

	for _, forbidden := range []string{
		"data-metas-p=",
		`class="metas-progress"`,
		"metas-progress-chip",
		"metas-card__footer",
		"R$ 0,00/mês",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("reserve without target must not render %q", forbidden)
		}
	}

	if got := strings.Count(body, "data-sensitive"); got != 1 {
		t.Fatalf("reserve without target monetary fields = %d, want 1 (saldo)", got)
	}
}

func TestMetasReserveCardWithoutCategoryDisablesTransactions(t *testing.T) {
	card := testReserveCard("box-3", 30)
	card.CategoryID = ""
	card.CategoryName = "Sem categoria"

	body := renderMetasCard(t, "metas-caixinha-card", card)

	assertContains(t, body, `<span class="ui-badge ui-badge--success metas-card__badge">mar/2028</span>`)
	assertContains(t, body, `<button type="button" disabled class="cb-meta-menu-item">`)
	assertContains(t, body, `Lançamentos`)
	if strings.Contains(body, `href="/lancamentos?categoria=`) {
		t.Fatal("reserve without category must not expose a transaction link")
	}
}

func TestMetasCardsKeepMenuContractAndActions(t *testing.T) {
	limit := renderMetasCard(t, "metas-limit-card", testLimitCard("lim-menu", 20))
	reserve := renderMetasCard(t, "metas-caixinha-card", testReserveCard("box-menu", 40))

	for _, expected := range []string{
		`data-caixinha-dropdown data-ck-mode="menu"`,
		`data-ck-toggle`,
		`data-ck-panel`,
		`aria-expanded="false"`,
		`aria-controls="metas-limite-menu-lim-menu"`,
		`id="metas-limite-menu-lim-menu"`,
		`<button type="button" data-ck-close`,
		`hx-get="/metas/novo?aba=limite&id=lim-menu"`,
		`<a href="/lancamentos?categoria=cat-food" data-ck-close class="cb-meta-menu-item">`,
	} {
		assertContains(t, limit, expected)
	}

	for _, expected := range []string{
		`aria-controls="metas-caixinha-menu-box-menu"`,
		`id="metas-caixinha-menu-box-menu"`,
		`Aportar`,
		`Liberar`,
		`Editar`,
		`<a href="/lancamentos?categoria=cat-retirement" data-ck-close class="cb-meta-menu-item">`,
		`hx-get="/metas/caixinha/aporte/sheet?box_id=box-menu"`,
		`hx-get="/metas/caixinha/resgate/sheet?box_id=box-menu"`,
	} {
		assertContains(t, reserve, expected)
	}

	if strings.Contains(limit, `disabled class="cb-meta-menu-item"`) {
		t.Fatal("limit card menu should not render the reserve-only disabled fallback")
	}
}

func TestMetasCardsSupportVariableCardCounts(t *testing.T) {
	for _, count := range []int{1, 2, 3, 6, 8} {
		t.Run(fmt.Sprintf("%d-cards", count), func(t *testing.T) {
			cards := make([]CaixinhaCard, 0, count)
			for i := 0; i < count; i++ {
				card := testReserveCard(fmt.Sprintf("box-%d", i+1), 10+i)
				card.Name = fmt.Sprintf("Reserva %d", i+1)
				cards = append(cards, card)
			}

			body := renderMetasPanel(t, testMetasPageTemplates(t), "metas-caixinhas-panel", MetasData{
				Aba:            "caixinhas",
				Caixinhas:      cards,
				CaixinhasCount: count,
			})

			if got := strings.Count(body, `<article data-meta-card `); got != count {
				t.Fatalf("rendered %d cards, want %d", got, count)
			}
			if got := strings.Count(body, `aria-controls="metas-caixinha-menu-`); got != count {
				t.Fatalf("rendered %d aria-controls, want %d", got, count)
			}
			assertContains(t, body, `class="grid grid-cols-1 md:grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-3 w-full"`)
		})
	}
}

func TestMetasLimitCardProgressKeepsAvailableSemantics(t *testing.T) {
	cases := []struct {
		consumed  int
		available string
		chip      string
		tone      string
		progress  string
		aria      string
	}{
		{consumed: 0, available: "100", chip: "100% disponível", tone: "ok", progress: "100", aria: "100% do limite disponível"},
		{consumed: 10, available: "90", chip: "90% disponível", tone: "ok", progress: "90", aria: "90% do limite disponível"},
		{consumed: 30, available: "70", chip: "70% disponível", tone: "ok", progress: "70", aria: "70% do limite disponível"},
		{consumed: 75, available: "25", chip: "25% disponível", tone: "attention", progress: "25", aria: "25% do limite disponível"},
		{consumed: 100, available: "0", chip: "Excedido", tone: "exceeded", progress: "0", aria: "limite excedido"},
		{consumed: 120, available: "0", chip: "Excedido", tone: "exceeded", progress: "0", aria: "limite excedido"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d-consumed", tc.consumed), func(t *testing.T) {
			body := renderMetasCard(t, "metas-limit-card", testLimitCard("lim-state", tc.consumed))

			assertContains(t, body, fmt.Sprintf(`data-metas-p="%s"`, tc.available))
			assertContains(t, body, fmt.Sprintf(`metas-progress-tone-%s`, tc.tone))
			assertContains(t, body, fmt.Sprintf(`<progress class="metas-progress" value="%s" max="100" aria-label="Alimentação: %s">`, tc.progress, tc.aria))
			assertContains(t, body, fmt.Sprintf(`<span class="metas-progress-chip" aria-hidden="true">%s</span>`, tc.chip))

			// O texto do chip nunca deve inverter a semântica para "utilizado".
			if strings.Contains(body, "% utilizado") {
				t.Fatal("limit progress chip must describe the available budget, not consumption")
			}
		})
	}
}

func TestMetasReserveProgressKeepsTruePercentageAboveHundred(t *testing.T) {
	cases := []struct {
		percent int
		tone    string
		label   string
	}{
		{percent: 0, tone: "initial", label: "Início da reserva"},
		{percent: 25, tone: "info", label: "Em andamento"},
		{percent: 70, tone: "ok", label: "Perto do alvo"},
		{percent: 90, tone: "ok", label: "Perto do alvo"},
		{percent: 100, tone: "complete", label: "Meta atingida"},
		{percent: 120, tone: "complete", label: "Meta atingida"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d-percent", tc.percent), func(t *testing.T) {
			body := renderMetasCard(t, "metas-caixinha-card", testReserveCard("box-state", tc.percent))

			assertContains(t, body, fmt.Sprintf(`data-metas-p="%d"`, tc.percent))
			assertContains(t, body, fmt.Sprintf(`metas-progress-tone-%s`, tc.tone))
			assertContains(t, body, fmt.Sprintf(`<progress class="metas-progress" value="%d" max="100" aria-label="Aposentadoria: %s, %d%% atingido">%d%%</progress>`, tc.percent, tc.label, tc.percent, tc.percent))
			assertContains(t, body, fmt.Sprintf(`<span class="metas-progress-chip" aria-hidden="true">%d%% do alvo</span>`, tc.percent))
		})
	}
}

func TestMetasNegativeReserveUsesDangerBadgeAndExceededTone(t *testing.T) {
	card := testReserveCard("box-negative", 0)
	card.IsNegative = true

	body := renderMetasCard(t, "metas-caixinha-card", card)

	assertContains(t, body, `<span class="ui-badge ui-badge--danger metas-card__badge">excedente</span>`)
	assertContains(t, body, `metas-progress-tone-exceeded`)
	assertContains(t, body, `aria-label="Aposentadoria: Reserva negativa, 0% atingido"`)
}

func TestMetasCardsMaskAllMonetaryValuesButKeepLabelsAndDates(t *testing.T) {
	limit := renderMetasCard(t, "metas-limit-card", testLimitCard("lim-privacy", 30))
	reserve := renderMetasCard(t, "metas-caixinha-card", testReserveCard("box-privacy", 76))

	for _, expected := range []string{
		`<p class="metas-box-value metas-box-value-ok" data-sensitive>`,
		`<span class="metas-card__fact-value" data-sensitive>`,
	} {
		assertContains(t, limit, expected)
	}
	for _, expected := range []string{
		`<p class="metas-box-value metas-box-value-ok" data-sensitive>`,
		`<p class="metas-box-target" data-sensitive>`,
		`<b class="metas-box-foot-value" data-sensitive>`,
	} {
		assertContains(t, reserve, expected)
	}

	for name, body := range map[string]string{"limit": limit, "reserve": reserve} {
		if strings.Contains(body, `aria-label="R$`) || strings.Contains(body, `title="R$`) {
			t.Fatalf("%s card exposes a monetary value in aria-label/title", name)
		}
		if strings.Contains(body, `aria-label="Abrir histórico do limite R$`) ||
			strings.Contains(body, `aria-label="Abrir histórico da reserva R$`) {
			t.Fatalf("%s card exposes a monetary value in the history link", name)
		}
	}

	assertContains(t, reserve, `Aposentadoria`)
	assertContains(t, reserve, `mar/2028`)
	assertContains(t, reserve, `Alvo`)
	assertContains(t, limit, `Utilizado`)
}

func TestMetasCardsCSSKeepsPremiumGeometryAndTouchTarget(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")

	for _, expected := range []string{
		".metas-card {",
		"border-radius: var(--ui-radius-card);",
		"background: var(--ui-surface);",
		"padding: 1rem;",
		"gap: 0.75rem;",
		".metas-card__menu-toggle {",
		"width: 2rem;",
		"height: 2rem;",
		"@media (max-width: 1023px) {",
		"width: 2.75rem;",
		"height: 2.75rem;",
		".metas-progress {",
		"height: 0.375rem;",
		".metas-progress-chip {",
		"font-weight: 650;",
		"@media (prefers-reduced-motion: reduce) {",
		".metas-card__footer",
		"border-top: 1px solid var(--ui-border);",
		"var(--ui-surface-raised)",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("metas.css missing M4 card contract %q", expected)
		}
	}

	for _, forbidden := range []string{
		"cb-card-dense",
		"cb-status-chip",
		"metas-metrics",
		"metas-box-foot-item",
		"metas-box-balance",
		"metas-summary",
		"!important",
	} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("metas.css still contains legacy/forbidden card contract %q", forbidden)
		}
	}
}

func TestMetasCardsTemplateRemovesLegacyCardClasses(t *testing.T) {
	body := readTestFile(t, "templates/pages/metas.html")

	for _, forbidden := range []string{
		"cb-card-dense",
		"cb-status-chip",
		"metas-metrics",
		"metas-box-foot-item",
		"metas-value",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("metas template still contains legacy card class %q", forbidden)
		}
	}
}
