package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

// F5 — Consolidated invoice rows inside /lancamentos. The row changes form but
// keeps the invoice-specific fields (cycle reference, due date, pending/total/
// paid, cycle status, link and swipe) and the backend semantics.
func TestLancamentosConsolidatedInvoiceSectionComposition(t *testing.T) {
	data := LancamentosData{
		Invoices: []InvoiceRow{
			{
				ID:             "inv-a",
				AccountName:    "Inter",
				ReferenceLabel: "Out/2026",
				DateLabel:      "07/10/2026",
				ItemCount:      3,
				FaturaURL:      "/cartoes/card-a/faturas?mes=10&ano=2026",
				PendingMoney:   MoneyDisplay{Reais: "4.373", Cents: ",25"},
				BadgeLabel:     "Fechada",
				IsOverdue:      true,
			},
			{
				ID:             "inv-b",
				AccountName:    "Nubank",
				ReferenceLabel: "Out/2026",
				DateLabel:      "05/10/2026",
				ItemCount:      1,
				FaturaURL:      "/cartoes/card-b/faturas?mes=10&ano=2026",
				PendingMoney:   MoneyDisplay{Reais: "0", Cents: ",00"},
				BadgeLabel:     "Paga",
			},
		},
		Transactions: []TransactionRow{
			{ID: "tx-a", Description: "Movimentação", Type: "EXPENSE", AmountDisplay: "- R$ 10,00", DateLabel: "5 OUT", PaymentStatus: "paid"},
		},
	}
	html := renderLancamentosListHTML(t, data)

	for _, want := range []string{
		`id="lancamentos-invoices"`,
		`class="ui-panel ui-list-panel"`,
		`id="lancamentosInvoicesTitle"`,
		`Faturas dos cartões`,
		`class="lancamentos-invoices-list"`,
		`id="invoice-inv-a"`,
		`id="invoice-inv-b"`,
		`id="transactions-list"`,
	} {
		assertContains(t, html, want)
	}
	invoicesIndex := strings.Index(html, `id="lancamentos-invoices"`)
	transactionsIndex := strings.Index(html, `id="transactions-list"`)
	if invoicesIndex < 0 || transactionsIndex < 0 || invoicesIndex > transactionsIndex {
		t.Fatalf("invoice section must be rendered before the transaction list: invoices=%d transactions=%d", invoicesIndex, transactionsIndex)
	}
	if strings.Contains(html, `class="invoice-content`) {
		t.Fatal("legacy invoice card markup must not return to /lancamentos")
	}
}

func TestLancamentosConsolidatedInvoiceSectionAbsentWithoutInvoices(t *testing.T) {
	data := LancamentosData{
		Transactions: []TransactionRow{
			{ID: "tx-only", Description: "Movimentação", Type: "EXPENSE", AmountDisplay: "- R$ 10,00", DateLabel: "5 OUT", PaymentStatus: "paid"},
		},
	}
	html := renderLancamentosListHTML(t, data)
	if strings.Contains(html, `id="lancamentos-invoices"`) {
		t.Fatal("invoice section must not render when the period has no invoices")
	}
	if !strings.Contains(html, `id="transactions-list"`) {
		t.Fatal("transaction section must remain when the period has no invoices")
	}
}

func TestLancamentosInvoiceRowPremiumContracts(t *testing.T) {
	row := InvoiceRow{
		ID:                   "inv-premium",
		AccountID:            "card-1",
		AccountName:          "Inter",
		CardColor:            "#FF7A00",
		CardProviderMark:     "INTER",
		CardProviderLogoPath: "institutions/inter.png",
		Reference:            "2026-10",
		ReferenceLabel:       "Out/2026",
		Status:               "CLOSED",
		DateLabel:            "07/10/2026",
		FaturaURL:            "/cartoes/card-1/faturas?mes=10&ano=2026",
		ItemCount:            3,
		PendingMoney:         MoneyDisplay{Reais: "4.373", Cents: ",25"},
		TotalMoney:           MoneyDisplay{Reais: "4.373", Cents: ",25"},
		PaidMoney:            MoneyDisplay{Reais: "0", Cents: ",00"},
		BadgeLabel:           "Fechada",
	}
	html := renderInvoiceRowHTML(t, row)

	for _, want := range []string{
		`id="invoice-inv-premium"`,
		`data-invoice-id="inv-premium"`,
		`data-swipe-row="true"`,
		`data-swipe-right-url="/cartoes/card-1/faturas?mes=10&ano=2026"`,
		`data-swipe-right-method="GET"`,
		`data-swipe-right-target="#main-content"`,
		`data-swipe-right-swap="innerHTML"`,
		`href="/cartoes/card-1/faturas?mes=10&ano=2026"`,
		`data-swipe-content`,
		`hx-get="/cartoes/card-1/faturas?mes=10&ano=2026"`,
		`hx-target="#main-content"`,
		`hx-select="#main-content"`,
		`hx-push-url="true"`,
		`class="ui-row ui-row--invoice-summary ui-row--link"`,
		`class="ui-avatar" data-cb-color="#FF7A00" aria-hidden="true"`,
		`institutions/inter.png`,
		`data-avatar-fallback`,
		`data-avatar-text`,
		`Fatura Inter — Out/2026`,
		`Vence em 07/10/2026`,
		`3 itens`,
		`class="ui-row__amount" data-sensitive>R$ 4.373,25</span>`,
		`class="ui-badge ui-badge--neutral">Fechada</span>`,
	} {
		assertContains(t, html, want)
	}
	if strings.Contains(html, "data-swipe-left-") || strings.Contains(html, "data-swipe-bg-left") {
		t.Fatal("consolidated invoice rows must not advertise a bulk transaction delete swipe")
	}
	if strings.Contains(html, "Vencida") {
		t.Fatal("a closed invoice without overdue must not be labelled Vencida")
	}
	if strings.Contains(html, `class="invoice-content`) || strings.Contains(html, `class="invoice-copy`) || strings.Contains(html, `class="invoice-summary`) {
		t.Fatal("legacy invoice classes must not return")
	}
}

func TestLancamentosInvoiceRowStatusAndValueSemantics(t *testing.T) {
	cases := []struct {
		name       string
		row        InvoiceRow
		wantBadge  string
		wantAmount string
		wantClass  string
		wantVenc   bool
	}{
		{
			name:       "paid zero stays paid by status",
			row:        InvoiceRow{ID: "inv-paid", AccountName: "Nubank", BadgeLabel: "Paga", PendingMoney: MoneyDisplay{Reais: "0", Cents: ",00"}, ItemCount: 1},
			wantBadge:  `ui-badge--success">Paga`,
			wantAmount: `>R$ 0,00</span>`,
			wantClass:  `class="ui-row__amount"`,
		},
		{
			name:       "partial is info",
			row:        InvoiceRow{ID: "inv-partial", AccountName: "Inter", BadgeLabel: "Parcial", PendingMoney: MoneyDisplay{Reais: "5", Cents: ",00"}, TotalMoney: MoneyDisplay{Reais: "10", Cents: ",00"}, PaidMoney: MoneyDisplay{Reais: "5", Cents: ",00"}, HasPartialPayment: true},
			wantBadge:  `ui-badge--info">Parcial`,
			wantAmount: `>R$ 5,00</span>`,
			wantClass:  `class="ui-row__amount"`,
		},
		{
			name:       "closed is neutral",
			row:        InvoiceRow{ID: "inv-closed", AccountName: "Itaú", BadgeLabel: "Fechada", PendingMoney: MoneyDisplay{Reais: "9", Cents: ",00"}},
			wantBadge:  `ui-badge--neutral">Fechada`,
			wantAmount: `>R$ 9,00</span>`,
			wantClass:  `class="ui-row__amount"`,
		},
		{
			name:       "open is accent",
			row:        InvoiceRow{ID: "inv-open", AccountName: "Cartão Viagem", BadgeLabel: "Aberta", PendingMoney: MoneyDisplay{Reais: "2.500", Cents: ",00"}},
			wantBadge:  `ui-badge--accent">Aberta`,
			wantAmount: `>R$ 2.500,00</span>`,
			wantClass:  `class="ui-row__amount"`,
		},
		{
			name:       "overdue adds danger and value tone",
			row:        InvoiceRow{ID: "inv-over", AccountName: "Inter", BadgeLabel: "Fechada", IsOverdue: true, PendingMoney: MoneyDisplay{Reais: "4.373", Cents: ",25"}},
			wantBadge:  `ui-badge--neutral">Fechada`,
			wantAmount: `>R$ 4.373,25</span>`,
			wantClass:  `class="ui-row__amount is-overdue"`,
			wantVenc:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := renderInvoiceRowHTML(t, tc.row)
			assertContains(t, html, tc.wantBadge)
			assertContains(t, html, tc.wantAmount)
			assertContains(t, html, tc.wantClass)
			if got := strings.Contains(html, `ui-badge--danger">Vencida`); got != tc.wantVenc {
				t.Fatalf("Vencida badge = %v, want %v\n%s", got, tc.wantVenc, html)
			}
			if strings.Contains(html, "is-expense") || strings.Contains(html, "is-income") {
				t.Fatal("invoice value must not inherit transaction income/expense classes")
			}
		})
	}
}

func TestLancamentosInvoiceRowLongNameAndLargeValue(t *testing.T) {
	row := InvoiceRow{
		ID:                "inv-long",
		AccountName:       "Cartão Corporativo Viagens Globais e Despesas de Campo",
		ReferenceLabel:    "Outubro de 2026",
		DateLabel:         "20/10/2026",
		ItemCount:         28,
		FaturaURL:         "/cartoes/card-long/faturas?mes=10&ano=2026",
		PendingMoney:      MoneyDisplay{Reais: "734.567", Cents: ",89"},
		TotalMoney:        MoneyDisplay{Reais: "1.234.567", Cents: ",89"},
		PaidMoney:         MoneyDisplay{Reais: "500.000", Cents: ",00"},
		HasPartialPayment: true,
		IsOverdue:         true,
		CardColor:         "#0EA5E9",
		CardProviderMark:  "CCVGD",
		BadgeLabel:        "Parcial",
	}
	html := renderInvoiceRowHTML(t, row)
	for _, want := range []string{
		`is-long-name`,
		`title="Fatura Cartão Corporativo Viagens Globais e Despesas de Campo — Outubro de 2026"`,
		`Cartão Corporativo Viagens Globais e Despesas de Campo`,
		`— Outubro de 2026`,
		`Total R$ 1.234.567,89`,
		`Pago R$ 500.000,00`,
		`R$ 734.567,89`,
		`28 itens`,
		`lancamentos-invoice-mark">CCVGD</span>`,
		`ui-badge--info">Parcial</span>`,
		`ui-badge--danger">Vencida</span>`,
	} {
		assertContains(t, html, want)
	}
	if got := strings.Count(html, `data-sensitive`); got != 2 {
		t.Fatalf("data-sensitive values = %d, want 2", got)
	}
}

func TestPaymentConfirmPrivacyMaskContract(t *testing.T) {
	jsRaw, err := os.ReadFile(resolveTemplatePath(t, "assets/js/payment-confirm.js"))
	if err != nil {
		t.Fatalf("read payment-confirm.js: %v", err)
	}
	js := string(jsRaw)
	for _, want := range []string{
		`function setSinglePaymentDescription(parts, description, amount)`,
		`value.setAttribute('data-sensitive', '')`,
		`setSinglePaymentDescription(parts, desc, amount);`,
	} {
		assertContains(t, js, want)
	}
	if strings.Contains(js, `parts.description.textContent = amount ?`) {
		t.Fatal("single payment modal must not concatenate the amount as plain text")
	}

	cssRaw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/app-shell.css"))
	if err != nil {
		t.Fatalf("read app-shell.css: %v", err)
	}
	css := string(cssRaw)
	for _, want := range []string{
		`.privacy-enabled #payment-confirm-description [data-sensitive]`,
		`visibility: hidden`,
		`content: "••••"`,
	} {
		assertContains(t, css, want)
	}
}

func TestLancamentosInvoicePremiumCSSContract(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/lancamentos.css"))
	if err != nil {
		t.Fatalf("read lancamentos.css: %v", err)
	}
	css := string(raw)
	for _, want := range []string{
		`.lancamentos-invoices`,
		`.lancamentos-invoices-title`,
		`.ui-row--invoice-summary`,
		`.ui-row--invoice-summary .ui-row__title`,
		`-webkit-line-clamp: 2`,
		`.lancamentos-invoice-mark`,
		`color-mix(in srgb, #000 55%`,
		`.ui-row--invoice-summary .ui-row__amount.is-overdue`,
		`@media (min-width: 360px) and (max-width: 430px)`,
		`@media (max-width: 359px)`,
		`.ui-row--invoice-summary.is-long-name`,
		`.ui-row--invoice-summary.is-long-name .lancamentos-invoice-summary`,
		`font-variant-numeric: tabular-nums`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("CSS da fatura não contém %q", want)
		}
	}
}

func renderInvoiceRowHTML(t *testing.T, row InvoiceRow) string {
	t.Helper()
	tpl := template.Must(template.New("invoice-row-f5").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(resolveTemplatePath(t, "templates/pages/lancamentos.html")))
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "invoice-row", row); err != nil {
		t.Fatalf("execute invoice-row: %v", err)
	}
	return strings.ReplaceAll(strings.ReplaceAll(buf.String(), "&#43;", "+"), "&amp;", "&")
}
