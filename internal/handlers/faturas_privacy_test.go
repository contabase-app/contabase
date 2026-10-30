package handlers

import (
	"bytes"
	"html"
	"html/template"
	"strings"
	"testing"
)

// Faturas privacy is CSS-only and reuses body.privacy-enabled / data-sensitive.
// These tests pin the scope and the template hooks so a future restyle cannot
// silently expose invoice values.

func TestFaturasPrivacyCSSMasksBothInvoiceEntrypoints(t *testing.T) {
	css := readTestFile(t, "assets/css/faturas.css")
	blockStart := strings.Index(css, "/* === PRIVACY ===")
	if blockStart < 0 {
		t.Fatal("faturas.css must keep the privacy block")
	}
	block := css[blockStart:]
	for _, expected := range []string{
		".privacy-enabled .cb-v2-faturas-main [data-sensitive]",
		"visibility: hidden;",
		`content: "••••";`,
		".privacy-enabled .cb-v2-faturas-main :is(strong, span, p, small, div, dd)[data-sensitive]::after",
		".privacy-enabled .cb-v2-faturas-main .ui-row__amount[data-sensitive]::after",
		".privacy-enabled .cb-v2-faturas-main .cb-invoice-refund-side [data-sensitive]::after",
		".privacy-enabled .cb-v2-faturas-main .cb-v2-credit-card [data-sensitive]::after",
		".privacy-enabled .cb-v2-faturas-main input[data-partial-payment-amount]::placeholder",
		"pointer-events: none;",
	} {
		if !strings.Contains(block, expected) {
			t.Fatalf("faturas privacy block missing %q", expected)
		}
	}
	if strings.Contains(block, "display: none") {
		t.Fatal("faturas privacy must preserve the value box instead of removing it from layout")
	}
}

func TestSheetPrivacyCSSCoversEveryFinancialSheet(t *testing.T) {
	css := readTestFile(t, "assets/css/sheet-form.css")
	for _, expected := range []string{
		".privacy-enabled .sheet-shell [data-sensitive]:not(option)",
		".privacy-enabled .sheet-shell :is(span, strong, p, small, div, dd)[data-sensitive]:not(option)::after",
		`content: "••••";`,
		"pointer-events: none;",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("sheet-form privacy missing %q", expected)
		}
	}

	for _, path := range []string{
		"templates/components/form_reembolso_fatura.html",
		"templates/components/form_reembolso.html",
		"templates/components/reembolsos_lancamento.html",
	} {
		markup := readTestFile(t, path)
		if !strings.Contains(markup, "sheet-shell") {
			t.Fatalf("%s must keep the shared sheet-shell scope", path)
		}
		if !strings.Contains(markup, "data-sensitive") {
			t.Fatalf("%s must keep monetary data-sensitive hooks", path)
		}
	}
}

func TestInvoiceEntrypointsShareThePrivacyScope(t *testing.T) {
	direct := readTestFile(t, "templates/pages/faturas.html")
	for _, expected := range []string{
		`class="cb-v2-main cb-v2-faturas-main"`,
		`id="faturas-content"`,
		`id="invoice-summary"`,
		`id="invoice-transactions"`,
	} {
		if !strings.Contains(direct, expected) {
			t.Fatalf("direct invoice page missing %q", expected)
		}
	}

	hub := readTestFile(t, "templates/pages/faturas_index.html")
	for _, expected := range []string{
		`class="cb-v2-main cb-v2-faturas-main cb-v2-faturas-index-main"`,
		`id="faturas-index-detail"`,
		`{{template "faturas-content" .SelectedFatura}}`,
	} {
		if !strings.Contains(hub, expected) {
			t.Fatalf("invoice hub missing %q", expected)
		}
	}
}

func renderInvoiceSummaryForPrivacy(t *testing.T, data FaturaData) string {
	t.Helper()
	content := readTestFile(t, "templates/pages/faturas.html")
	tpl := template.Must(template.New("faturas-privacy").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(content))
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "invoice-summary", data); err != nil {
		t.Fatalf("execute invoice-summary: %v", err)
	}
	return out.String()
}

func privacyMoney(cents int64) MoneyDisplay {
	return MoneyMinor(cents)
}

func TestInvoiceSummaryMarksEveryDisplayedAmountSensitive(t *testing.T) {
	data := FaturaData{
		InvoiceID:               "invoice-privacy",
		CardName:                "Cartão principal",
		AccountID:               "account-privacy",
		Reference:               "2026-10",
		IsFaturasIndex:          false,
		HideMonthNavigation:     true,
		Total:                   privacyMoney(100000),
		TotalPaid:               privacyMoney(25000),
		PendingAmount:           privacyMoney(75000),
		PendingAmountInput:      "75000",
		PaymentDateInput:        "2026-10-10",
		HasCreditLimit:          true,
		LimitAvailable:          privacyMoney(50000),
		LimitUsed:               privacyMoney(50000),
		CreditLimit:             privacyMoney(100000),
		LimitPercent:            50,
		HasPayments:             true,
		HasGeneralRefunds:       true,
		CanAttemptPayment:       true,
		CanSubmitPayment:        true,
		CanMutateItems:          true,
		InvoicePayments:         []InvoicePaymentRow{{DateLabel: "10 Out", AccountName: "Conta principal", Amount: privacyMoney(25000)}},
		GeneralRefunds:          []InvoiceGeneralRefundRow{{ID: "refund-privacy", DateLabel: "11 Out", Description: "Estorno", Amount: privacyMoney(5000)}},
		PaymentAccounts:         []InvoicePaymentAccountOption{{ID: "account-privacy", Name: "Conta principal", Money: privacyMoney(200000), BalanceAfterPayment: privacyMoney(125000)}},
		DefaultPaymentAccountID: "account-privacy",
	}

	rendered := renderInvoiceSummaryForPrivacy(t, data)
	for _, expected := range []string{
		`<p class="cb-invoice-hero-value" data-sensitive>R$ 1.000,00</p>`,
		`<strong class="cb-invoice-figure-value is-success" data-sensitive>R$ 250,00</strong>`,
		`<strong class="cb-invoice-figure-value is-warning" data-sensitive>R$ 750,00</strong>`,
		`<strong data-sensitive>R$ 500,00</strong>`,
		`<strong data-sensitive>R$ 1.000,00</strong>`,
		`<span class="ui-row__amount is-income" data-sensitive>R$ 250,00</span>`,
		`<span class="ui-row__amount is-income" data-sensitive>- R$ 50,00</span>`,
		`<strong class="cb-invoice-modal-value" data-sensitive>R$ 750,00</strong>`,
		`<strong data-settle-balance-after data-sensitive>R$ 1.250,00</strong>`,
		`<strong data-partial-payment-remaining data-sensitive>R$ 750,00</strong>`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("invoice summary privacy contract missing %q\nrendered:\n%s", expected, rendered)
		}
	}

	// Editing and validation data must stay outside the visual mask.
	for _, editable := range []string{
		`name="payment_amount"`,
		`name="payment_account_id"`,
		`name="payment_date"`,
		`data-balance-after="R$ 1.250,00"`,
	} {
		if !strings.Contains(rendered, editable) {
			t.Fatalf("invoice summary editable contract missing %q", editable)
		}
	}
}

func TestInvoiceTransactionsMaskRefundBadgeAndNetAmountWithoutHidingLabels(t *testing.T) {
	rendered := html.UnescapeString(renderInvoiceTransactionsForStageD(t, stageDInvoiceData()))
	for _, expected := range []string{
		`Reembolso <span data-sensitive>+ R$ 10,00</span>`,
		`Líquido <span data-sensitive>R$ 29,99</span>`,
		`class="ui-row__amount is-income" data-sensitive>+ R$ 50,00</span>`,
		`class="ui-row__amount is-projected" data-sensitive>- R$ 12,90</span>`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("invoice transactions privacy contract missing %q\nrendered:\n%s", expected, rendered)
		}
	}
}
