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

	tpl, err := template.New("dashboard").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
	})).Parse(string(content) + string(shell) + string(creditCard))
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
			Money:          MoneyMinor(31628602),
			Reserved:       MoneyMinor(16661138),
			Free:           MoneyMinor(14967464),
			TrendPercent:   "+189,6%",
			TrendDirection: "up",
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
		`Conta principal`,
		`Trocar de carro`,
		`cb-v2-credit-card--physical`,
		`cb-v2-credit-card--tonal`,
		`data-card-visual="physical"`,
		`cb-v2-credit-card-status`,
		`cb-v2-credit-card-status is-open`,
		`Aberto</strong><small>fecha hoje`,
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
			t.Fatalf("dashboard physical card must not render removed decoration %q", removed)
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
				readTemplateFile(t, "templates/components/credit_card.html"),
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
				readTemplateFile(t, "templates/components/credit_card.html"),
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

	for _, count := range []int{1, 4, 10, 100} {
		html := render(count)
		if got := strings.Count(html, `class="cb-v2-account-row"`); got != count {
			t.Errorf("%d contas: renderizou %d linhas de conta, esperado %d", count, got, count)
		}
		if got := strings.Count(html, `href="/lancamentos?conta=`); got != count {
			t.Errorf("%d contas: renderizou %d links de conta, esperado %d", count, got, count)
		}
		for _, forbidden := range []string{"oculta", "ocultas", "Mostrar mais contas", "Ver mais contas"} {
			if strings.Contains(html, forbidden) {
				t.Errorf("%d contas: Home não pode indicar contas escondidas (%q)", count, forbidden)
			}
		}
	}
}
