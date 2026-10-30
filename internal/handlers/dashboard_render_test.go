package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDashboardRendersCanonicalData(t *testing.T) {
	path := resolveTemplatePath(t, "templates/pages/dashboard.html")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dashboard V2 template: %v", err)
	}

	shellPath := resolveTemplatePath(t, "templates/components/app_shell.html")
	shell, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("read app shell V2 template: %v", err)
	}

	creditCardPath := resolveTemplatePath(t, "templates/components/credit_card.html")
	creditCard, err := os.ReadFile(creditCardPath)
	if err != nil {
		t.Fatalf("read canonical credit-card template: %v", err)
	}

	patrimonyPath := resolveTemplatePath(t, "templates/components/investment_patrimony.html")
	patrimony, err := os.ReadFile(patrimonyPath)
	if err != nil {
		t.Fatalf("read investment patrimony template: %v", err)
	}

	tpl, err := template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
	})).Parse(string(content) + string(shell) + string(creditCard) + string(patrimony))
	if err != nil {
		t.Fatalf("parse dashboard V2 template: %v", err)
	}

	data := DashboardData{
		AppShellAccess: AppShellAccess{
			CanManageOps:    true,
			CanManageGlobal: true,
		},
		Title:               "Dashboard",
		IsBusiness:          true,
		UserName:            "Vitor Ferreira",
		UserFirstName:       "Vitor",
		UserInitials:        "VF",
		UserRole:            "Administrador",
		ActiveWorkspaceName: "Família",
		NotificationCount:   2,
		Balance: BalanceData{
			ReserveBalanceAvailable: true,
			Money:                   MoneyMinor(31628602),
			Reserved:                MoneyMinor(16661138),
			Free:                    MoneyMinor(14967464),
			TrendPercent:            "+189,6%",
			TrendDirection:          "up",
		},
		Accounts: []AccountCard{{
			ID:               "account-1",
			Name:             "Conta principal",
			Money:            MoneyMinor(15007146),
			TrendPercent:     "+7,8%",
			TrendDirection:   "up",
			ProviderMark:     "ITAU",
			ProviderLogoPath: "institutions/itau.png",
			Color:            "#ec7000",
			TypeLabel:        "Investimento",
		}},
		Cards: []CreditCardCard{{
			ID:               "card-1",
			InvoiceID:        "invoice-1",
			Href:             "/faturas?ano=2026&cartao=card-1&mes=9",
			Name:             "Cartão principal",
			Money:            MoneyMinor(666666),
			LimitMoney:       MoneyMinor(5833334),
			DueDateCompact:   "10/set",
			ClosingDaysLabel: "fecha hoje",
			StatusLabel:      "Aberto",
			LimitPercent:     74,
			ProviderMark:     "XP",
			ProviderLogoPath: "institutions/xp.png",
			Color:            "#111111",
		}},
		Limits: []DashboardLimitCard{{
			CategoryName: "Alimentação",
			Spent:        MoneyMinor(192565),
			Limit:        MoneyMinor(180000),
			Percent:      100,
		}},
		Categories: DashboardCategoriesData{
			HasData:        true,
			CurrentLabel:   "01/10 a 24/10",
			PreviousLabel:  "01/09 a 24/09",
			ComparisonNote: "01/10 a 24/10 comparado com 01/09 a 24/09",
			Items: []DashboardCategoryItem{
				{
					ID:                   "cat-food",
					Name:                 "Alimentação",
					Icon:                 "utensils",
					Tone:                 2,
					Amount:               MoneyMinor(245000),
					AmountCents:          245000,
					SharePercent:         70,
					ShareLabel:           "70%",
					VariationLabel:       "+12,5%",
					VariationClass:       "is-up",
					VariationDescription: "Aumento de 12,5% em relação a 01/09 a 24/09.",
					HasBase:              true,
				},
				{
					ID: "cat-home", Name: "Casa", Icon: "house", Tone: 3,
					Amount: MoneyMinor(70000), AmountCents: 70000, SharePercent: 20, ShareLabel: "20%",
					VariationLabel: "−8,2%", VariationClass: "is-down",
					VariationDescription: "Redução de 8,2% em relação a 01/09 a 24/09.", HasBase: true,
				},
				{
					ID: "cat-transport", Name: "Transporte", Icon: "bus", Tone: 4,
					Amount: MoneyMinor(20000), AmountCents: 20000, SharePercent: 6, ShareLabel: "6%",
					VariationLabel: "Novo gasto", VariationClass: "is-new",
					VariationDescription: "Novo gasto: sem despesa comparável em 01/09 a 24/09.",
				},
				{
					ID: "cat-pets", Name: "Pet", Icon: "paw-print", Tone: 5,
					Amount: MoneyMinor(15000), AmountCents: 15000, SharePercent: 4, ShareLabel: "4%",
					VariationLabel: "0,0%", VariationClass: "is-flat",
					VariationDescription: "Sem variação em relação a 01/09 a 24/09.", HasBase: true,
				},
			},
		},
		Boxes: []CaixinhaCard{{
			Name:    "Trocar de carro",
			Balance: MoneyMinor(3600000),
			Target:  MoneyMinor(5000000),
			Percent: 72,
		}},
		PendingPayables: []DashboardPayableItem{{
			Description:  "Energia",
			DueDateLabel: "Vence em 05/09",
			Amount:       MoneyMinor(12918),
		}},
		Payable7dTotal: MoneyMinor(12918),
		Payable7dCount: 1,
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "dashboard-page", data); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}

	html := out.String()
	for _, expected := range []string{
		`data-dashboard`,
		`data-app-shell`,
		`data-page-section="dashboard"`,
		`href="/"`,
		`hx-get="/transacoes/nova?tipo=despesa"`,
		`data-v2-carousel`,
		`href="/faturas?ano=2026&amp;cartao=card-1&amp;mes=9"`,
		`data-v2-carousel-prev`,
		`data-v2-card-group`,
		`data-wallet-commitments`,
		`data-wallet-categories`,
		`Principais categorias`,
		`Maiores despesas do mês`,
		`01/10 a 24/10`,
		`01/09 a 24/09`,
		`Alimentação`,
		`12,5%`,
		`data-wallet-categories-toggle`,
		`data-v2-action-tabs`,
		`data-v2-global-navigation`,
		`data-v2-nav-section="dashboard"`,
		`data-v2-sidebar`,
		`data-v2-sidebar-toggle`,
		`data-v2-nav-group-key="settings"`,
		`href="/configuracoes/categorias"`,
		`data-v2-nav-group-key="administration"`,
		`href="/admin/usuarios"`,
		`href="/contatos"`,
		`Controle de custos`,
		`data-v2-menu-trigger`,
		`data-theme-toggle`,
		`data-v2-theme-toggle`,
		`cb-v2-theme-icon-dark`,
		`cb-v2-theme-icon-light`,
		`cb-v2-appbar-brand`,
		`data-v2-workspace-trigger`,
		`data-lucide="settings-2"`,
		`aria-label="Início"`,
		`aria-label="Transações"`,
		`aria-label="Metas"`,
		`aria-label="Contas"`,
		`/assets/css/app-shell.css`,
		`/assets/css/dashboard.css`,
		`/assets/js/app-shell.js`,
		`/assets/js/event-handlers.js`,
		`/assets/js/show-more.js`,
		`Conta principal`,
		`Trocar de carro`,
		`cb-v2-credit-card--physical`,
		`cb-v2-credit-card--tonal`,
		`data-card-visual="physical"`,
		`cb-v2-credit-card-status`,
		`cb-v2-credit-card-status is-open`,
		`Aberto</strong>`,
		`wallet-card-closing`,
		`fecha hoje`,
		`src="institutions/itau.png?v=test"`,
		`src="institutions/xp.png?v=test"`,
		`cb-v2-provider-mark--image`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("dashboard V2 render missing %q", expected)
		}
	}
	for _, removed := range []string{`cb-v2-credit-card-black`, `cb-v2-credit-card-chip`, `cb-v2-credit-card-contactless`, `data-lucide="wifi"`} {
		if strings.Contains(html, removed) {
			t.Fatalf("dashboard must not render removed element %q", removed)
		}
	}
	if strings.Contains(html, `cb-v2-header-profile`) {
		t.Fatal("dashboard must not render the profile image in the top app bar")
	}
	if strings.Contains(html, `cb-v2-user-card`) {
		t.Fatal("dashboard sidebar must not duplicate the profile card")
	}
	if strings.Contains(html, "Gerenciar cartões") {
		t.Fatalf("dashboard should not render the card management link")
	}

	out.Reset()
	data.OOB = true
	if err := tpl.ExecuteTemplate(&out, "dashboard-canonical-content", data); err != nil {
		t.Fatalf("render dashboard OOB content: %v", err)
	}
	if !strings.Contains(out.String(), `id="dashboard-content"`) || !strings.Contains(out.String(), `hx-swap-oob="outerHTML"`) {
		t.Fatal("dashboard OOB refresh must replace the canonical dashboard content")
	}
}

func TestDashboardCreditCardAvailablePercentPresentation(t *testing.T) {
	tpl := template.Must(template.New("card").Funcs(template.FuncMap{"assetPath": func(p string) string { return p }}).Parse(readTemplateFile(t, "templates/components/credit_card.html")))
	for _, tc := range []struct {
		name            string
		used, available int64
		percent         int
	}{
		{"unused", 0, 100000, 100}, {"half", 50000, 50000, 50}, {"full", 100000, 0, 0}, {"over-limit", 125000, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			now := time.Now().UTC()
			seedCreditCardWithLimit(t, db, "ws-percent", "card-percent", "cc-percent", 100000)
			seedInvoiceForCard(t, db, "ws-percent", "card-percent", "inv-percent", now.Format("2006-01"), "OPEN", now.Year(), int(now.Month()), 20)
			if tc.used > 0 {
				seedExpenseOnInvoice(t, db, "ws-percent", "card-percent", "inv-percent", tc.used, 1, 1, "expense-percent")
			}
			dashboard := BuildDashboardData(db, "user-ws-percent", "ws-percent")
			index, err := queryFaturasIndexCards(db, "ws-percent", now.Year(), now.Month())
			if err != nil {
				t.Fatal(err)
			}
			for source, cards := range map[string][]CreditCardCard{"dashboard": dashboard.Cards, "invoice-hub": index} {
				if len(cards) != 1 {
					t.Fatalf("%s count=%d", source, len(cards))
				}
				card := cards[0]
				if card.LimitPercent != tc.percent || card.LimitMoney != MoneyMinor(tc.available) || card.Money != MoneyMinor(tc.used) {
					t.Fatalf("%s financial values changed: %+v", source, card)
				}
				var out bytes.Buffer
				if err := tpl.ExecuteTemplate(&out, "credit-card-tile", card); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), fmt.Sprintf("%d%% disponível", tc.percent)) || strings.Contains(out.String(), "% utilizado") {
					t.Fatalf("%s incorrect percent label", source)
				}
			}
			var count int
			var amount, balance int64
			if err := db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(amount),0) FROM transactions WHERE workspace_id='ws-percent'`).Scan(&count, &amount); err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tc.used > 0 {
				wantCount = 1
			}
			if count != wantCount || amount != tc.used {
				t.Fatalf("transactions changed: %d/%d", count, amount)
			}
			if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='card-percent' AND workspace_id='ws-percent'`).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if balance != 0 {
				t.Fatalf("account balance changed: %d", balance)
			}
		})
	}
}

// Próximos compromissos: seção permanente, compacta (até 4 itens por aba),
// com abas a pagar/receber e destaque de vencidos; sem sticky.
func TestDashboardCommitmentsSection(t *testing.T) {
	parse := func() *template.Template {
		t.Helper()
		tpl, err := template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
			"assetPath": func(path string) string { return path + "?v=test" },
		})).Parse(
			readTemplateFile(t, "templates/pages/dashboard.html") +
				readTemplateFile(t, "templates/components/app_shell.html") +
				readTemplateFile(t, "templates/components/credit_card.html") +
				readTemplateFile(t, "templates/components/investment_patrimony.html"),
		)
		if err != nil {
			t.Fatalf("parse dashboard V2 template: %v", err)
		}
		return tpl
	}

	render := func(data DashboardData) string {
		t.Helper()
		var out bytes.Buffer
		if err := parse().ExecuteTemplate(&out, "dashboard-page", data); err != nil {
			t.Fatalf("render dashboard: %v", err)
		}
		return out.String()
	}

	empty := render(DashboardData{Title: "Dashboard", UserFirstName: "Vitor"})
	if !strings.Contains(empty, `data-v2-action-tabs`) || !strings.Contains(empty, `Nenhuma conta a pagar no curto prazo.`) {
		t.Fatal("dashboard commitments must render tabs with a discrete empty state")
	}

	full := render(DashboardData{
		Title:              "Dashboard",
		UserFirstName:      "Vitor",
		Payable7dCount:     2,
		Payable7dTotal:     MoneyMinor(243000),
		Receivable7dCount:  1,
		Receivable7dTotal:  MoneyMinor(50000),
		PendingPayables:    []DashboardPayableItem{{Description: "Energia", IsOverdue: true, Amount: MoneyMinor(120000), DueDateLabel: "Venceu em 02/10", StatusLabel: "Vencido"}},
		PendingReceivables: []DashboardPayableItem{{Description: "Freela", Amount: MoneyMinor(50000), DueDateLabel: "Vence em 05/10"}},
	})
	for _, expected := range []string{
		`data-wallet-commitments`,
		`data-v2-action-tab="payables"`,
		`data-v2-action-tab="receivables"`,
		`2 itens`,
		`is-overdue`,
		`cb-v2-action-receivable`,
		`href="/contas"`,
	} {
		if !strings.Contains(full, expected) {
			t.Fatalf("dashboard commitments missing %q", expected)
		}
	}
}

// Limites de valor zero não inventam percentual: exibem "Sem limite definido".
func TestDashboardZeroLimitPresentation(t *testing.T) {
	tpl := template.Must(template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	})).Parse(
		readTemplateFile(t, "templates/pages/dashboard.html") +
			readTemplateFile(t, "templates/components/app_shell.html") +
			readTemplateFile(t, "templates/components/credit_card.html") +
			readTemplateFile(t, "templates/components/investment_patrimony.html"),
	))
	var out bytes.Buffer
	data := DashboardData{
		Title:         "Dashboard",
		UserFirstName: "Vitor",
		Limits: []DashboardLimitCard{{
			CategoryName: "Sem teto",
			Spent:        MoneyMinor(50000),
			Limit:        MoneyMinor(0),
		}},
	}
	if err := tpl.ExecuteTemplate(&out, "dashboard-page", data); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	html := out.String()
	if !strings.Contains(html, `Sem limite definido`) || !strings.Contains(html, `is-no-limit`) {
		t.Fatal("zero limit must render the discrete undefined-limit state")
	}
	if strings.Contains(html, `0% de R$ 0,00`) || strings.Contains(html, `R$ 500,00 <span class="cb-v2-progress-sep">de</span> R$ 0,00`) {
		t.Fatal("zero limit must not invent a percentage against R$ 0,00")
	}
}

// O dataset completo permanece no rail desktop. No mobile o CSS mostra até
// seis cartões reais e o CTA /faturas só existe quando há um sétimo cartão.
func TestDashboardCardRailMobileSixPlusCTA(t *testing.T) {
	parse := func() *template.Template {
		t.Helper()
		tpl, err := template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
			"assetPath": func(path string) string { return path + "?v=test" },
		})).Parse(
			readTemplateFile(t, "templates/pages/dashboard.html") +
				readTemplateFile(t, "templates/components/app_shell.html") +
				readTemplateFile(t, "templates/components/credit_card.html") +
				readTemplateFile(t, "templates/components/investment_patrimony.html"),
		)
		if err != nil {
			t.Fatalf("parse dashboard V2 template: %v", err)
		}
		return tpl
	}

	render := func(cards int) string {
		t.Helper()
		data := DashboardData{Title: "Dashboard"}
		for i := 0; i < cards; i++ {
			data.Cards = append(data.Cards, CreditCardCard{
				ID:           fmt.Sprintf("card-%d", i+1),
				Name:         fmt.Sprintf("Cartão %d", i+1),
				LimitPercent: 100,
			})
		}
		var out bytes.Buffer
		if err := parse().ExecuteTemplate(&out, "dashboard-page", data); err != nil {
			t.Fatalf("render dashboard: %v", err)
		}
		return out.String()
	}

	for _, tc := range []struct{ count, cta int }{{1, 0}, {6, 0}, {7, 1}, {10, 1}} {
		html := render(tc.count)
		if got := strings.Count(html, "data-v2-card-item"); got != tc.count {
			t.Errorf("%d cartões: rail desktop tem %d cartões reais", tc.count, got)
		}
		if got := strings.Count(html, "data-v2-all-invoices-slide"); got != tc.cta {
			t.Errorf("%d cartões: renderizou %d CTAs, esperado %d", tc.count, got, tc.cta)
		}
		if tc.cta == 1 && !strings.Contains(html, `href="/faturas" class="cb-v2-all-invoices-slide"`) {
			t.Errorf("%d cartões: CTA deve apontar para /faturas", tc.count)
		}
	}
}

// A Home apresenta todas as contas do workspace: nenhum recorte, nenhuma
// contagem de "ocultas" e nenhum caminho alternativo de paginação.
func TestDashboardRendersEveryAccount(t *testing.T) {
	parse := func() *template.Template {
		t.Helper()
		tpl, err := template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
			"assetPath": func(path string) string { return path + "?v=test" },
		})).Parse(
			readTemplateFile(t, "templates/pages/dashboard.html") +
				readTemplateFile(t, "templates/components/app_shell.html") +
				readTemplateFile(t, "templates/components/credit_card.html") +
				readTemplateFile(t, "templates/components/investment_patrimony.html"),
		)
		if err != nil {
			t.Fatalf("parse dashboard V2 template: %v", err)
		}
		return tpl
	}

	render := func(accounts int) string {
		t.Helper()
		data := DashboardData{Title: "Dashboard", UserFirstName: "Vitor"}
		for i := 0; i < accounts; i++ {
			data.Accounts = append(data.Accounts, AccountCard{
				ID:        fmt.Sprintf("account-%d", i+1),
				Name:      fmt.Sprintf("Conta %d", i+1),
				Money:     MoneyMinor(int64(100000 + i)),
				TypeLabel: "Conta Corrente",
			})
		}
		var out bytes.Buffer
		if err := parse().ExecuteTemplate(&out, "dashboard-page", data); err != nil {
			t.Fatalf("render dashboard: %v", err)
		}
		return out.String()
	}

	for _, count := range []int{0, 1, 4, 10, 11, 100} {
		html := render(count)
		if got := strings.Count(html, `class="cb-v2-account-row"`); got != count {
			t.Errorf("%d contas: renderizou %d linhas de conta, esperado %d", count, got, count)
		}
		if got := strings.Count(html, `href="/lancamentos?conta=`); got != count {
			t.Errorf("%d contas: renderizou %d links de conta, esperado %d", count, got, count)
		}
		if count == 0 && !strings.Contains(html, `Nenhuma conta cadastrada.`) {
			t.Error("0 contas: esperava estado vazio discreto")
		}
		for _, forbidden := range []string{"oculta", "ocultas"} {
			if strings.Contains(html, forbidden) {
				t.Errorf("%d contas: Home não pode indicar contas escondidas (%q)", count, forbidden)
			}
		}
		// Desktop mostra todas; a expansão existe apenas acima de 10 (mobile/tablet).
		hasToggle := strings.Contains(html, `data-wallet-accounts-toggle`)
		if count > 10 && !hasToggle {
			t.Errorf("%d contas: esperava o controle 'Mostrar mais contas'", count)
		}
		if count <= 10 && hasToggle {
			t.Errorf("%d contas: não deve renderizar controle de expansão", count)
		}
	}
}

func TestDashboardCategoriesEmptyStateDoesNotInventRows(t *testing.T) {
	tpl := template.Must(template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	})).Parse(
		readTemplateFile(t, "templates/pages/dashboard.html") +
			readTemplateFile(t, "templates/components/app_shell.html") +
			readTemplateFile(t, "templates/components/credit_card.html") +
			readTemplateFile(t, "templates/components/investment_patrimony.html"),
	))
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "dashboard-page", DashboardData{Title: "Dashboard", UserFirstName: "Vitor"}); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	html := out.String()
	if !strings.Contains(html, "Nenhuma despesa registrada neste período.") {
		t.Fatal("empty categories must render a discrete empty state")
	}
	if strings.Contains(html, "wallet-category-row") || strings.Contains(html, "wallet-category-meter") {
		t.Fatal("empty categories must not invent category rows or bars")
	}
}

func TestDashboardAccountsExpandControlOnlyAboveTen(t *testing.T) {
	tpl := template.Must(template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	})).Parse(
		readTemplateFile(t, "templates/pages/dashboard.html") +
			readTemplateFile(t, "templates/components/app_shell.html") +
			readTemplateFile(t, "templates/components/credit_card.html") +
			readTemplateFile(t, "templates/components/investment_patrimony.html"),
	))
	for _, count := range []int{1, 2, 7, 10, 15} {
		data := DashboardData{Title: "Dashboard", UserFirstName: "Vitor"}
		for i := 0; i < count; i++ {
			data.Accounts = append(data.Accounts, AccountCard{
				ID:    fmt.Sprintf("acc-%d", i+1),
				Name:  fmt.Sprintf("Conta %d", i+1),
				Money: MoneyMinor(int64(10000 + i)),
			})
		}
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "dashboard-page", data); err != nil {
			t.Fatalf("render %d accounts: %v", count, err)
		}
		html := out.String()
		hasToggle := strings.Contains(html, `data-wallet-accounts-toggle`)
		if count > 10 && !hasToggle {
			t.Fatalf("%d accounts must render the expand control", count)
		}
		if count <= 10 && hasToggle {
			t.Fatalf("%d accounts must not render the expand control", count)
		}
	}
}

func renderDashboardCards(t *testing.T, count int) string {
	t.Helper()
	tpl := template.Must(template.New("dashboard-cards").Funcs(withAppShellTestFuncs(nil)).Parse(
		readTemplateFile(t, "templates/components/dashboard-cards.html") +
			readTemplateFile(t, "templates/components/financial-emblem.html"),
	))
	cards := make([]CreditCardCard, count)
	for i := range cards {
		cards[i] = CreditCardCard{
			InvoiceID:      fmt.Sprintf("invoice-%d", i+1),
			Name:           fmt.Sprintf("Cartão %d", i+1),
			DueDateCompact: "Dia 10",
			Color:          "#123456",
		}
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "dashboard-cards", DashboardData{Cards: cards}); err != nil {
		t.Fatalf("render dashboard cards with %d items: %v", count, err)
	}
	return out.String()
}

func TestDashboardCardsShowMoreContractAboveTheLimit(t *testing.T) {
	html := renderDashboardCards(t, 7)
	for _, expected := range []string{
		`data-show-more-group="cards"`,
		`data-show-more-initial="6"`,
		`data-show-more-item="6"`,
		`data-show-more-toggle-container`,
		`data-show-more-toggle`,
		`data-onclick-ev="toggleShowMore"`,
		`focus-visible:ring-[var(--cb-border-focus)]`,
		`Mostrar mais (mais 1)`,
		`Mostrar menos`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("dashboard cards show-more markup missing %q", expected)
		}
	}
	if got := strings.Count(html, `data-show-more-item=`); got != 7 {
		t.Fatalf("dashboard cards rendered %d show-more items, want 7", got)
	}
}

func TestDashboardCardsHideShowMoreControlAtTheLimit(t *testing.T) {
	html := renderDashboardCards(t, 6)
	if strings.Contains(html, `data-show-more-toggle`) {
		t.Fatal("dashboard cards must not render the show-more control at the 6-card limit")
	}
	if got := strings.Count(html, `data-show-more-item=`); got != 6 {
		t.Fatalf("dashboard cards rendered %d show-more items, want 6", got)
	}
}
