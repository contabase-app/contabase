package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
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
		`aria-label="Lançamentos"`,
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
