package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func stageEModalData() FaturaData {
	return FaturaData{
		InvoiceID:               "invoice-1",
		AccountID:               "acc-1",
		CardName:                "Cartão principal",
		Reference:               "2026-11",
		PendingAmount:           money("750", ",00"),
		PendingAmountInput:      "75000",
		PaymentDateInput:        "2026-10-25",
		Total:                   money("750", ",00"),
		TotalPaid:               money("0", ",00"),
		HasCreditLimit:          false,
		CanAttemptPayment:       true,
		CanSubmitPayment:        true,
		DefaultPaymentAccountID: "acc-1",
		PaymentAccounts: []InvoicePaymentAccountOption{
			{ID: "acc-1", Name: "Conta corrente", Money: money("1.000", ",00"), BalanceAfterPayment: money("250", ",00")},
			{ID: "acc-2", Name: "Conta poupança", Money: money("500", ",00"), BalanceAfterPayment: money("-250", ",00")},
		},
	}
}

func TestInvoicePaymentModalsKeepContractsAndUseCanonicalFields(t *testing.T) {
	html := renderInvoiceSummaryForStageC(t, stageEModalData())

	modalScope := html
	if i := strings.Index(html, `id="invoice-settle-payment-modal"`); i > 0 {
		modalScope = html[i:]
	}
	if j := strings.Index(modalScope, `class="cb-card-dense`); j > 0 {
		modalScope = modalScope[:j]
	}

	for _, expected := range []string{
		`id="invoice-settle-payment-modal"`,
		`data-invoice-settle-modal`,
		`data-contabase-overlay`,
		`data-overlay-priority="120"`,
		`data-overlay-close="closeSettlePaymentModal"`,
		`role="dialog" aria-modal="true" aria-labelledby="invoice-settle-title" aria-describedby="invoice-settle-description"`,
		`id="invoice-settle-title"`,
		`id="invoice-settle-description"`,
		`class="cb-invoice-modal"`,
		`hx-post="/cartoes/faturas/pagar" hx-target="#faturas-content" hx-swap="outerHTML"`,
		`name="invoice_id" value="invoice-1"`,
		`name="account_id" value="acc-1"`,
		`name="payment_mode" value="settle"`,
		`name="confirm_settle" value="1"`,
		`name="payment_account_id" required data-settle-payment-account class="ui-select"`,
		`data-balance-after="R$ 250,00"`,
		`name="payment_date" value="2026-10-25" class="ui-input"`,
		`data-settle-balance-after`,
		`data-onclick="closeSettlePaymentModal"`,
		`class="ui-btn ui-btn--ghost cb-invoice-modal-btn"`,
		`class="ui-btn ui-btn--primary cb-invoice-modal-btn"`,
		// Partial payment dialog.
		`id="invoice-partial-payment-modal"`,
		`data-invoice-payment-modal data-pending-cents="75000"`,
		`data-overlay-close="closePartialPaymentModal"`,
		`aria-labelledby="invoice-partial-title" aria-describedby="invoice-partial-description"`,
		`name="payment_mode" value="partial"`,
		`name="payment_amount" inputmode="numeric"`,
		`data-partial-payment-amount data-money-mask`,
		`aria-describedby="invoice-partial-error"`,
		`id="invoice-partial-error"`,
		`data-partial-payment-error aria-live="polite" hidden`,
		`data-partial-payment-remaining`,
		`data-onclick="closePartialPaymentModal"`,
		// Original labels and values stay intact.
		"Competência", "Valor a quitar", "Conta selecionada", "Data de pagamento",
		"Saldo estimado da conta após pagamento", "Saldo pendente",
		"Saldo restante após este pagamento", "R$ 750,00", "R$ 250,00",
	} {
		if !strings.Contains(modalScope, expected) {
			t.Fatalf("payment modals missing %q", expected)
		}
	}

	// Legacy modal classes/inputs are gone from the dialog region.
	for _, legacy := range []string{
		`cb-card-dense`, `cb-button`, `cb-text-micro`,
		`bg-[var(--cb-surface-2)]`, `font-black`, `rounded-xl border`,
	} {
		if strings.Contains(modalScope, legacy) {
			t.Fatalf("payment modals kept legacy presentation %q", legacy)
		}
	}
}

func TestInvoicePaymentModalsRespectDisabledSubmission(t *testing.T) {
	data := stageEModalData()
	data.CanSubmitPayment = false
	data.PaymentDisabledReason = "Sem valor para pagar"
	html := renderInvoiceSummaryForStageC(t, data)
	if !strings.Contains(html, `type="submit" disabled`) {
		t.Fatal("settle/partial submits must stay disabled when the backend disallows payment")
	}
}

func TestGeneralRefundSheetKeepsContractsAndUsesCanonicalSurface(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/components/form_reembolso_fatura.html"))
	if err != nil {
		t.Fatalf("read form: %v", err)
	}
	tpl := template.Must(template.New("refund").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(raw)))

	data := InvoiceGeneralRefundFormData{
		InvoiceID:          "invoice-1",
		CardName:           "Cartão principal",
		Reference:          "2026-11",
		PendingAmount:      money("750", ",00"),
		HasAlreadyRefunded: true,
		AlreadyRefunded:    money("100", ",00"),
		CurrentAmountInput: "100,00",
		Description:        "Estorno de compra",
		FormAction:         "/cartoes/faturas/reembolso",
		SubmitLabel:        "Salvar reembolso",
		IsEditing:          true,
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "form-reembolso-fatura", data); err != nil {
		t.Fatalf("execute refund form: %v", err)
	}
	html := out.String()

	for _, expected := range []string{
		`role="dialog" aria-modal="true" aria-labelledby="invoice-refund-title" aria-describedby="invoice-refund-description"`,
		`id="invoice-refund-title"`,
		`class="cb-invoice-sheet-title"`,
		`class="cb-invoice-sheet-icon"`,
		`class="cb-invoice-sheet-summary"`,
		`id="invoice-refund-description"`,
		`hx-post="/cartoes/faturas/reembolso" hx-swap="none" hx-indicator="#htmx-indicator"`,
		`name="invoice_id" value="invoice-1"`,
		`name="valor" type="text" inputmode="numeric"`,
		`data-money-mask`,
		`name="descricao" type="text" maxlength="240"`,
		`class="cb-invoice-sheet-actions"`,
		`class="ui-btn ui-btn--ghost cb-invoice-modal-btn"`,
		`class="ui-btn ui-btn--primary cb-invoice-modal-btn"`,
		`R$ 750,00`, `R$ 100,00`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("refund sheet missing %q", expected)
		}
	}

	for _, legacy := range []string{`bg-emerald-500/10`, `cb-button`, `rounded-xl border border-[var(--cb-border-subtle)] bg-[var(--cb-surface-2)] p-3`} {
		if strings.Contains(html, legacy) {
			t.Fatalf("refund sheet kept legacy presentation %q", legacy)
		}
	}
}
