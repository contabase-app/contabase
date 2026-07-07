package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

func TestDashboardEmblemsRenderProviderMark(t *testing.T) {
	// 1. Parse all relevant templates
	tpl := template.Must(template.New("dashboard-test").Funcs(template.FuncMap{
		"sub":   func(a, b int) int { return a - b },
		"upper": func(s string) string { return strings.ToUpper(s) },
		"slice": func(s string, start, end int) string {
			if len(s) < end {
				return s
			}
			return s[start:end]
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/pages/dashboard.html"),
		resolveTemplatePath(t, "templates/components/dashboard-financial.html"),
		resolveTemplatePath(t, "templates/components/dashboard-accounts.html"),
		resolveTemplatePath(t, "templates/components/dashboard-cards.html"),
		resolveTemplatePath(t, "templates/components/financial-emblem.html"),
		resolveTemplatePath(t, "templates/components/dashboard-balance.html"),
		resolveTemplatePath(t, "templates/components/dashboard-health.html"),
	))

	// 2. Set up mockup DashboardData
	data := DashboardData{
		CurrentWorkspaceName: "Workspace Teste",
		ActiveWorkspaceName:  "Workspace Teste",
		Title:                "Dashboard",
		Accounts: []AccountCard{
			{
				ID:           "acc-inter",
				Name:         "Banco Inter",
				ProviderMark: "INTER",
				Color:        "#FF7A00",
				Icon:         "wallet",
				Money:        MoneyDisplay{Reais: "1.234", Cents: ",56"},
			},
			{
				ID:           "acc-caixa",
				Name:         "Caixa Econômica",
				ProviderMark: "CAIXA",
				Color:        "#0066AE",
				Icon:         "wallet",
				Money:        MoneyDisplay{Reais: "500", Cents: ",00"},
			},
			{
				ID:           "acc-mp",
				Name:         "Mercado Pago",
				ProviderMark: "MP",
				Color:        "#009EE3",
				Icon:         "wallet",
				Money:        MoneyDisplay{Reais: "3.200", Cents: ",10"},
			},
			{
				ID:           "acc-custom",
				Name:         "Minha Carteira",
				ProviderMark: "",
				Color:        "#6B7280",
				Icon:         "wallet",
				Money:        MoneyDisplay{Reais: "50", Cents: ",00"},
			},
		},
		Cards: []CreditCardCard{
			{
				ID:             "card-nu",
				Name:           "Nubank Violeta",
				ProviderMark:   "NU",
				Color:          "#8A05BE",
				Icon:           "credit-card",
				Money:          MoneyDisplay{Reais: "850", Cents: ",00"},
				LimitMoney:     MoneyDisplay{Reais: "4.150", Cents: ",00"},
				LimitPercent:   83,
				StatusLabel:    "Aberto",
				DueDay:         "10",
				DueDateCompact: "10/abr",
			},
			{
				ID:             "card-xp",
				Name:           "XP Investimentos",
				ProviderMark:   "XP",
				Color:          "#FFE600",
				Icon:           "credit-card",
				Money:          MoneyDisplay{Reais: "1.500", Cents: ",00"},
				LimitMoney:     MoneyDisplay{Reais: "8.500", Cents: ",00"},
				LimitPercent:   85,
				StatusLabel:    "Aberto",
				DueDay:         "25",
				DueDateCompact: "25/abr",
			},
		},
	}

	// 3. Test rendering dashboard-accounts template
	t.Run("dashboard-accounts template rendering", func(t *testing.T) {
		var buf bytes.Buffer
		err := tpl.ExecuteTemplate(&buf, "dashboard-accounts", data)
		if err != nil {
			t.Fatalf("failed to execute dashboard-accounts template: %v", err)
		}

		html := buf.String()

		// Verify ProviderMark texts are present
		assertContains(t, html, "INTER")
		assertContains(t, html, "CAIXA")
		assertContains(t, html, "MP")

		// Custom account shouldn't have ProviderMark text, should fallback to icon
		if strings.Contains(html, "Minha Carteira") && strings.Contains(html, "emblem-len-0") {
			t.Errorf("custom account should not render an emblem-text block with length 0")
		}

		// Verify emblem-text class presence
		assertContains(t, html, "emblem-text")
		assertContains(t, html, "emblem-len-5") // len("INTER") or len("CAIXA")
		assertContains(t, html, "emblem-len-2") // len("MP")
	})

	t.Run("dashboard-financial template rendering", func(t *testing.T) {
		var buf bytes.Buffer
		err := tpl.ExecuteTemplate(&buf, "dashboard-financial", data)
		if err != nil {
			t.Fatalf("failed to execute dashboard-financial template: %v", err)
		}

		html := buf.String()

		assertContains(t, html, "Posição financeira")
		assertContains(t, html, "Saldo total")
		assertContains(t, html, "nos últimos 30 dias")
		assertContains(t, html, "Reservado")
		assertContains(t, html, "Disponível")
	})

	// 4. Test rendering dashboard-cards template
	t.Run("dashboard-cards template rendering", func(t *testing.T) {
		var buf bytes.Buffer
		err := tpl.ExecuteTemplate(&buf, "dashboard-cards", data)
		if err != nil {
			t.Fatalf("failed to execute dashboard-cards template: %v", err)
		}

		html := buf.String()

		// Verify card marks are present
		assertContains(t, html, "NU")
		assertContains(t, html, "XP")

		// Verify emblem classes
		assertContains(t, html, "emblem-text")
		assertContains(t, html, "emblem-len-2")
	})

	// 5. Test rendering full dashboard-content template (initial page load)
	t.Run("dashboard-content template rendering", func(t *testing.T) {
		var buf bytes.Buffer
		err := tpl.ExecuteTemplate(&buf, "dashboard-content", data)
		if err != nil {
			t.Fatalf("failed to execute dashboard-content template: %v", err)
		}

		html := buf.String()

		// Verify marks exist on initial load representation too
		assertContains(t, html, "INTER")
		assertContains(t, html, "CAIXA")
		assertContains(t, html, "MP")
		assertContains(t, html, "NU")
		assertContains(t, html, "XP")
		assertContains(t, html, "Saldo total")
		assertContains(t, html, "Minhas Contas")

		// Verify emblem-text CSS class is outputted
		assertContains(t, html, "emblem-text")
		assertContains(t, html, "emblem-len-5")
		assertContains(t, html, "emblem-len-2")
	})

	t.Run("dashboard financial block empty state", func(t *testing.T) {
		var buf bytes.Buffer
		emptyData := DashboardData{
			CurrentWorkspaceName: "Workspace Novo",
			ActiveWorkspaceName:  "Workspace Novo",
			Title:                "Dashboard",
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-financial", emptyData)
		if err != nil {
			t.Fatalf("failed to execute empty dashboard-financial template: %v", err)
		}

		html := buf.String()
		assertContains(t, html, "Saldo total")
		assertContains(t, html, `data-onboarding-empty="true"`)
	})

	t.Run("dashboard empty onboarding rendering", func(t *testing.T) {
		var buf bytes.Buffer
		emptyData := DashboardData{
			CurrentWorkspaceName: "Workspace Novo",
			ActiveWorkspaceName:  "Workspace Novo",
			Title:                "Dashboard",
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-content", emptyData)
		if err != nil {
			t.Fatalf("failed to execute empty dashboard-content template: %v", err)
		}

		html := buf.String()

		assertContains(t, html, "Nenhum limite definido")
		assertContains(t, html, "Crie objetivos para planejar seus investimentos.")
		assertContains(t, html, "Nenhuma conta a pagar nos proximos 7 dias")
		assertContains(t, html, "Nenhum recebimento nos proximos 7 dias")
		assertContains(t, html, `data-tab-group="dashboard"`)
		if strings.Contains(html, "Conta XP") || strings.Contains(html, "Conta PagBank") || strings.Contains(html, "Cartão exemplo") {
			t.Fatalf("empty dashboard should not render seeded/demo financial names")
		}
		if strings.Contains(html, "Contas representam onde seu dinheiro está") {
			t.Fatalf("empty dashboard should not render the legacy microcopy about accounts representation")
		}
		if strings.Contains(html, "Criar cartão de crédito") {
			t.Fatalf("empty 'Minhas Contas' card should not duplicate the cartão CTA; that lives only in 'Meus Cartões'")
		}
	})

	t.Run("dashboard empty accounts card has a single CTA", func(t *testing.T) {
		var buf bytes.Buffer
		emptyData := DashboardData{
			CurrentWorkspaceName: "Workspace Novo",
			ActiveWorkspaceName:  "Workspace Novo",
			Title:                "Dashboard",
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-accounts", emptyData)
		if err != nil {
			t.Fatalf("failed to execute empty dashboard-accounts template: %v", err)
		}

		html := buf.String()

		if strings.Count(html, "Criar primeira conta") != 2 {
			t.Fatalf("empty 'Minhas Contas' card should expose desktop+mobile 'Criar primeira conta' CTAs, got %d", strings.Count(html, "Criar primeira conta"))
		}
		if strings.Contains(html, `secao=cartoes`) {
			t.Fatalf("empty 'Minhas Contas' card must not link to /configuracoes?secao=cartoes; that CTA belongs to 'Meus Cartões'")
		}
		if strings.Contains(html, "Criar cartão") {
			t.Fatalf("empty 'Minhas Contas' card must not render any cartão CTA; the single cartão CTA lives in 'Meus Cartões'")
		}
		if !strings.Contains(html, `class="mt-4 flex justify-center"`) {
			t.Fatalf("empty 'Minhas Contas' CTA must be wrapped in a centered flex container to align the button")
		}
	})

	t.Run("dashboard empty cards renders two placeholders", func(t *testing.T) {
		var buf bytes.Buffer
		emptyData := DashboardData{
			CurrentWorkspaceName: "Workspace Novo",
			ActiveWorkspaceName:  "Workspace Novo",
			Title:                "Dashboard",
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-cards", emptyData)
		if err != nil {
			t.Fatalf("failed to execute empty dashboard-cards template: %v", err)
		}

		html := buf.String()

		assertContains(t, html, "Cadastre seu primeiro cartão")
		assertContains(t, html, "Acompanhe faturas, limites e vencimentos.")
		assertContains(t, html, "Adicionar cartão")
		if got := strings.Count(html, `data-dashboard-card-placeholder="true"`); got != 2 {
			t.Fatalf("empty 'Meus Cartões' should render 2 card placeholders, got %d", got)
		}
		if got := strings.Count(html, `class="card-cc `); got != 0 {
			t.Fatalf("empty 'Meus Cartões' placeholders must not render as real cards, got %d real card nodes", got)
		}
		if got := strings.Count(html, `data-show-more-item=`); got != 0 {
			t.Fatalf("empty 'Meus Cartões' placeholders must not enter show-more state, got %d show-more items", got)
		}
		if strings.Contains(html, `/cartoes/faturas/`) {
			t.Fatalf("empty 'Meus Cartões' placeholders must not link to invoices")
		}
		if strings.Contains(html, "Criar cartão de crédito") {
			t.Fatalf("empty 'Meus Cartões' card should use the shorter 'Criar cartão' CTA")
		}
		if !strings.Contains(html, `cb-cards-grid`) {
			t.Fatalf("empty 'Meus Cartões' should use grid layout matching pair grid below")
		}
		if strings.Contains(html, `cb-cards-rail`) {
			t.Fatalf("empty 'Meus Cartões' must not use carousel rail when no cards")
		}
		if strings.Contains(html, `hx-get="/configuracoes/cartoes"`) {
			t.Fatalf("placeholders must use plain href navigation, not HTMX")
		}
		if !strings.Contains(html, `href="/configuracoes/cartoes"`) {
			t.Fatalf("placeholders must link to /configuracoes/cartoes")
		}
	})

	t.Run("dashboard with one card renders one placeholder", func(t *testing.T) {
		var buf bytes.Buffer
		oneCard := DashboardData{
			CurrentWorkspaceName: "Workspace Um Cartao",
			ActiveWorkspaceName:  "Workspace Um Cartao",
			Title:                "Dashboard",
			Cards: []CreditCardCard{
				{
					ID:             "card-one",
					InvoiceID:      "invoice-one",
					Name:           "Cartão Principal",
					ProviderMark:   "CP",
					Color:          "#2563EB",
					Icon:           "credit-card",
					Money:          MoneyDisplay{Reais: "120", Cents: ",00"},
					LimitMoney:     MoneyDisplay{Reais: "880", Cents: ",00"},
					LimitPercent:   88,
					DueDateCompact: "10/abr",
				},
			},
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-cards", oneCard)
		if err != nil {
			t.Fatalf("failed to execute one-card dashboard-cards template: %v", err)
		}

		html := buf.String()

		assertContains(t, html, "Cartão Principal")
		assertContains(t, html, "Cadastre outro cartão")
		if got := strings.Count(html, `data-dashboard-card-placeholder="true"`); got != 1 {
			t.Fatalf("one-card 'Meus Cartões' should render 1 card placeholder, got %d", got)
		}
		if got := strings.Count(html, `class="card-cc `); got != 1 {
			t.Fatalf("one-card 'Meus Cartões' should render 1 real card node, got %d", got)
		}
		if strings.Contains(html, "Cadastre seu primeiro cartão") {
			t.Fatalf("one-card 'Meus Cartões' should not render first-card placeholder copy")
		}
		if !strings.Contains(html, `cb-cards-grid`) {
			t.Fatalf("one-card 'Meus Cartões' should use grid layout matching pair grid below")
		}
		if strings.Contains(html, `cb-cards-rail`) {
			t.Fatalf("one-card 'Meus Cartões' must not use carousel rail")
		}
		if strings.Contains(html, `cb-card-rail-item`) {
			t.Fatalf("one-card 'Meus Cartões' real card must not carry cb-card-rail-item in grid mode")
		}
		if strings.Contains(html, `hx-get="/configuracoes/cartoes"`) {
			t.Fatalf("one-card placeholder must use plain href navigation, not HTMX")
		}
	})

	t.Run("dashboard with two cards renders no placeholder", func(t *testing.T) {
		var buf bytes.Buffer
		twoCards := DashboardData{
			CurrentWorkspaceName: "Workspace Dois Cartoes",
			ActiveWorkspaceName:  "Workspace Dois Cartoes",
			Title:                "Dashboard",
			Cards: []CreditCardCard{
				{
					ID:             "card-one",
					InvoiceID:      "invoice-one",
					Name:           "Cartão Principal",
					ProviderMark:   "CP",
					Color:          "#2563EB",
					Icon:           "credit-card",
					Money:          MoneyDisplay{Reais: "120", Cents: ",00"},
					LimitMoney:     MoneyDisplay{Reais: "880", Cents: ",00"},
					LimitPercent:   88,
					DueDateCompact: "10/abr",
				},
				{
					ID:             "card-two",
					InvoiceID:      "invoice-two",
					Name:           "Cartão Reserva",
					ProviderMark:   "CR",
					Color:          "#059669",
					Icon:           "credit-card",
					Money:          MoneyDisplay{Reais: "80", Cents: ",00"},
					LimitMoney:     MoneyDisplay{Reais: "1.920", Cents: ",00"},
					LimitPercent:   96,
					DueDateCompact: "20/abr",
				},
			},
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-cards", twoCards)
		if err != nil {
			t.Fatalf("failed to execute two-card dashboard-cards template: %v", err)
		}

		html := buf.String()

		assertContains(t, html, "Cartão Principal")
		assertContains(t, html, "Cartão Reserva")
		if got := strings.Count(html, `data-dashboard-card-placeholder="true"`); got != 0 {
			t.Fatalf("two-card 'Meus Cartões' should not render placeholders, got %d", got)
		}
		if got := strings.Count(html, `class="card-cc `); got != 2 {
			t.Fatalf("two-card 'Meus Cartões' should render 2 real card nodes, got %d", got)
		}
		if got := strings.Count(html, `data-show-more-item=`); got != 2 {
			t.Fatalf("two-card 'Meus Cartões' should keep only real cards in show-more state, got %d", got)
		}
		if strings.Contains(html, "Cadastre outro cartão") || strings.Contains(html, "Cadastre seu primeiro cartão") {
			t.Fatalf("two-card 'Meus Cartões' should not render placeholder copy")
		}
		if !strings.Contains(html, `cb-cards-rail`) {
			t.Fatalf("two-card 'Meus Cartões' should use carousel rail layout")
		}
		if !strings.Contains(html, `cb-card-rail-item`) {
			t.Fatalf("two-card 'Meus Cartões' cards must carry cb-card-rail-item in carousel mode")
		}
		if strings.Contains(html, `cb-cards-grid`) {
			t.Fatalf("two-card 'Meus Cartões' must not use grid layout; use carousel for 2+ cards")
		}
	})

	t.Run("dashboard populated onboarding flag is absent", func(t *testing.T) {
		var buf bytes.Buffer
		populated := DashboardData{
			CurrentWorkspaceName: "Workspace Com Dados",
			ActiveWorkspaceName:  "Workspace Com Dados",
			Title:                "Dashboard",
			Accounts: []AccountCard{
				{
					ID:    "acc-1",
					Name:  "Conta Corrente",
					Money: MoneyDisplay{Reais: "100", Cents: ",00"},
				},
			},
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-content", populated)
		if err != nil {
			t.Fatalf("failed to execute populated dashboard-content template: %v", err)
		}

		html := buf.String()

		if strings.Contains(html, `data-onboarding-empty="true"`) {
			t.Fatalf("populated dashboard should not be marked as onboarding empty")
		}
		if strings.Contains(html, "Comece criando sua primeira conta.") {
			t.Fatalf("populated dashboard should not render the onboarding empty state")
		}
	})

	t.Run("dashboard account CTAs use plain href navigation", func(t *testing.T) {
		var buf bytes.Buffer
		emptyData := DashboardData{
			CurrentWorkspaceName: "Workspace Novo",
			ActiveWorkspaceName:  "Workspace Novo",
			Title:                "Dashboard",
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-content", emptyData)
		if err != nil {
			t.Fatalf("failed to execute empty dashboard-content template: %v", err)
		}

		html := buf.String()

		if !strings.Contains(html, `href="/configuracoes/contas"`) {
			t.Fatalf("dashboard must link to /configuracoes/contas for adding accounts")
		}
		if strings.Contains(html, `hx-get="/configuracoes/contas"`) {
			t.Fatalf("dashboard account CTAs must use plain href, not HTMX hx-get")
		}
		if strings.Contains(html, `hx-get="/configuracoes/cartoes"`) {
			t.Fatalf("dashboard card CTAs must use plain href, not HTMX hx-get")
		}
	})

	t.Run("dashboard cards placeholders use grid layout matching pair grid", func(t *testing.T) {
		var buf bytes.Buffer
		emptyData := DashboardData{
			CurrentWorkspaceName: "Workspace Novo",
			ActiveWorkspaceName:  "Workspace Novo",
			Title:                "Dashboard",
		}
		err := tpl.ExecuteTemplate(&buf, "dashboard-content", emptyData)
		if err != nil {
			t.Fatalf("failed to execute empty dashboard-content template: %v", err)
		}

		html := buf.String()

		if !strings.Contains(html, `cb-cards-grid`) {
			t.Fatalf("empty dashboard must use grid card layout matching pair grid below")
		}
		if strings.Contains(html, `cb-cards-rail`) && !strings.Contains(html, `data-real-card-count="0"`) {
			t.Fatalf("empty dashboard must not render carousel rail")
		}
		if strings.Contains(html, `cb-card-rail-item`) {
			t.Fatalf("empty dashboard must not have cb-card-rail-item (only in carousel for 2+ cards)")
		}
		if strings.Count(html, `grid grid-cols-1 md:grid-cols-2 gap-4`) < 1 {
			t.Fatalf("empty dashboard must render grid cards matching pair grid column structure")
		}
	})
}
