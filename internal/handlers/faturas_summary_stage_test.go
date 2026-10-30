package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func renderInvoiceSummaryForStageC(t *testing.T, data FaturaData) string {
	t.Helper()
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read faturas template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(raw)))
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "invoice-summary", data); err != nil {
		t.Fatalf("execute invoice-summary: %v", err)
	}
	return out.String()
}

func money(reais, cents string) MoneyDisplay { return MoneyDisplay{Reais: reais, Cents: cents} }

// Locks the Stage C presentation contract: canonical components only, no
// payment/refund boxes, and every financial value/HTMX hook preserved.
func TestInvoiceSummaryUsesCanonicalCompositionWithoutNestedCards(t *testing.T) {
	data := FaturaData{
		InvoiceID:         "invoice-1",
		Reference:         "2026-11",
		CardName:          "Cartão principal",
		ClosingLabel:      "31 Out 2026",
		DueLabel:          "7 Nov 2026",
		IsFaturasIndex:    true,
		HasCycleBadge:     true,
		CycleLabel:        "Aberta",
		HasFinancialBadge: true,
		FinancialLabel:    "Pendente",
		Total:             money("9.029", ",72"),
		TotalPaid:         money("1.000", ",00"),
		PendingAmount:     money("8.029", ",72"),
		HasCreditLimit:    true,
		CreditLimit:       money("64.300", ",00"),
		LimitUsed:         money("34.831", ",30"),
		LimitAvailable:    money("29.468", ",70"),
		LimitPercent:      45,
		HasPayments:       true,
		InvoicePayments: []InvoicePaymentRow{{
			DateLabel:   "10 Out 2026",
			AccountName: "Conta corrente",
			Source:      "Manual",
			Note:        "Pagamento parcial",
			Amount:      money("1.000", ",00"),
		}},
		HasGeneralRefunds: true,
		GeneralRefunds: []InvoiceGeneralRefundRow{{
			ID:          "refund-1",
			DateLabel:   "12 Out 2026",
			Description: "Compra de outra fatura",
			Amount:      money("200", ",00"),
		}},
		CanMutateItems:       true,
		CanAttemptPayment:    true,
		CanSubmitPayment:     true,
		ShowSensitiveWarning: true,
		SensitiveWarningText: "Valores de origem legada exigem conferência.",
	}

	html := renderInvoiceSummaryForStageC(t, data)

	for _, expected := range []string{
		`id="invoice-summary"`,
		`class="ui-panel cb-invoice-main"`,
		`class="ui-panel cb-invoice-actions"`,
		`ui-badge ui-badge--info`,
		`ui-badge ui-badge--warning`,
		`class="ui-row"`,
		`ui-row__amount is-income`,
		`data-open-settle-payment`,
		`data-onclick="openPartialPaymentModal"`,
		`hx-get="/cartoes/faturas/reembolso?invoice_id=invoice-1"`,
		`hx-get="/cartoes/faturas/reembolsos/refund-1/editar?invoice_id=invoice-1"`,
		`hx-post="/cartoes/faturas/reembolsos/refund-1/remover"`,
		`hx-confirm="Remover este reembolso? O saldo da fatura será recalculado."`,
		`data-tooltip-text="Editar reembolso"`,
		`data-tooltip-text="Remover reembolso"`,
		`R$ 9.029,72`,
		`R$ 1.000,00`,
		`R$ 8.029,72`,
		`R$ 64.300,00`,
		`R$ 34.831,30`,
		`R$ 29.468,70`,
		`R$ 200,00`,
		`45% livre`,
		`cb-invoice-limit-progress is-warning`,
		`value="45" max="100"`,
		`Valores de origem legada exigem conferência.`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("summary missing %q", expected)
		}
	}

	// The hub summary header does not repeat the selected card identity
	// (the payment dialog itself may name the card for confirmation context).
	headerScope := html
	if i := strings.Index(html, `id="invoice-settle-payment-modal"`); i > 0 {
		headerScope = html[:i]
	}
	if strings.Contains(headerScope, data.CardName) {
		t.Fatal("hub summary must not repeat card identity")
	}

	// Legacy nested presentation must be gone from the financial composition
	// (the payment modals and the month rail keep their own, out-of-scope markup).
	composition := html
	if i := strings.Index(html, `id="invoice-settle-payment-modal"`); i > 0 {
		composition = html[:i]
	}
	for _, legacy := range []string{
		`cb-invoice-action-label`,
		`cb-section-title`,
		`font-black`,
		`<span>Quitar</span>`,
		`cb-status-chip`,
	} {
		if strings.Contains(composition, legacy) {
			t.Fatalf("summary kept legacy presentation %q", legacy)
		}
	}
}

func TestInvoiceSummaryPreservesFinancialStates(t *testing.T) {
	base := FaturaData{
		InvoiceID:      "invoice-1",
		Reference:      "2026-11",
		CardName:       "Cartão",
		ClosingLabel:   "31 Out 2026",
		DueLabel:       "7 Nov 2026",
		IsFaturasIndex: true,
		Total:          money("750", ",00"),
		TotalPaid:      money("0", ",00"),
		PendingAmount:  money("750", ",00"),
		HasCreditLimit: true,
		CreditLimit:    money("1.000", ",00"),
		LimitUsed:      money("750", ",00"),
		LimitAvailable: money("250", ",00"),
		LimitPercent:   25,
	}

	t.Run("open invoice with low availability", func(t *testing.T) {
		data := base
		data.HasCycleBadge = true
		data.CycleLabel = "Aberta"
		data.HasFinancialBadge = true
		data.FinancialLabel = "Pendente"
		data.CanAttemptPayment = true
		data.CanSubmitPayment = true
		html := renderInvoiceSummaryForStageC(t, data)
		for _, expected := range []string{
			`ui-badge--info`, `ui-badge--warning`,
			`R$ 750,00`, `R$ 250,00`, `25% livre`,
			`cb-invoice-limit-progress is-danger`,
			`Disponibilidade baixa.`,
			`Quitar fatura`, `Pagamento parcial`, `Reembolso geral`,
			`Nenhum pagamento registrado`,
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("open state missing %q", expected)
			}
		}
	})

	t.Run("partially paid invoice", func(t *testing.T) {
		data := base
		data.HasFinancialBadge = true
		data.FinancialLabel = "Parcial"
		data.TotalPaid = money("250", ",00")
		data.PendingAmount = money("500", ",00")
		data.HasPayments = true
		data.InvoicePayments = []InvoicePaymentRow{{DateLabel: "10 Out 2026", AccountName: "Conta", Source: "Manual", Amount: money("250", ",00")}}
		html := renderInvoiceSummaryForStageC(t, data)
		for _, expected := range []string{
			`ui-badge--warning`, `Parcial`,
			`R$ 250,00`, `R$ 500,00`,
			`ui-row__title`, `ui-row__meta`, `R$ 250,00`,
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("partial state missing %q", expected)
			}
		}
		if strings.Contains(html, "Nenhum pagamento registrado") {
			t.Fatal("partial state must list its payments")
		}
	})

	t.Run("paid invoice shows the settled state", func(t *testing.T) {
		data := base
		data.HasFinancialBadge = true
		data.FinancialLabel = "Paga"
		data.TotalPaid = money("750", ",00")
		data.PendingAmount = money("0", ",00")
		data.CanAttemptPayment = false
		data.HasPayments = true
		data.InvoicePayments = []InvoicePaymentRow{{DateLabel: "10 Out 2026", AccountName: "Conta", Source: "Manual", Amount: money("750", ",00")}}
		html := renderInvoiceSummaryForStageC(t, data)
		for _, expected := range []string{`ui-badge--success`, `Paga`, `Fatura quitada`, `Nenhuma pendência financeira para este período.`} {
			if !strings.Contains(html, expected) {
				t.Fatalf("paid state missing %q", expected)
			}
		}
		if strings.Contains(html, `data-open-settle-payment`) {
			t.Fatal("paid invoice must not offer the settle action")
		}
	})

	t.Run("closed invoice keeps the neutral cycle badge", func(t *testing.T) {
		data := base
		data.HasCycleBadge = true
		data.CycleLabel = "Fechada"
		data.HasFinancialBadge = true
		data.FinancialLabel = "Pendente"
		html := renderInvoiceSummaryForStageC(t, data)
		if !strings.Contains(html, `ui-badge--neutral`) || !strings.Contains(html, "Fechada") {
			t.Fatal("closed invoice must keep its neutral cycle badge")
		}
	})

	t.Run("zero invoice without movement", func(t *testing.T) {
		data := base
		data.HasCycleBadge = true
		data.CycleLabel = "Aberta"
		data.HasFinancialBadge = true
		data.FinancialLabel = "Vazia"
		data.Total = money("0", ",00")
		data.TotalPaid = money("0", ",00")
		data.PendingAmount = money("0", ",00")
		data.LimitUsed = money("0", ",00")
		data.LimitAvailable = money("1.000", ",00")
		data.LimitPercent = 100
		data.CanAttemptPayment = false
		html := renderInvoiceSummaryForStageC(t, data)
		for _, expected := range []string{
			`ui-badge--neutral`, `Vazia`, `R$ 0,00`, `100% livre`,
			`cb-invoice-limit-progress is-success`, `Fatura quitada`,
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("zero invoice missing %q", expected)
			}
		}
	})

	t.Run("invoice without credit limit hides the limit block", func(t *testing.T) {
		data := base
		data.HasCreditLimit = false
		html := renderInvoiceSummaryForStageC(t, data)
		if strings.Contains(html, "cb-invoice-limit-progress") || strings.Contains(html, "Limite do cartão") {
			t.Fatal("invoice without credit limit must not render the limit block")
		}
	})

	t.Run("direct invoice link renders the identity panel and filter", func(t *testing.T) {
		data := base
		data.IsFaturasIndex = false
		data.CardProviderMark = "IT"
		data.FilterLabel = "Categoria: Saúde"
		data.ClearFiltersURL = "/faturas?cartao=card&limpar=1"
		html := renderInvoiceSummaryForStageC(t, data)
		for _, expected := range []string{
			`class="ui-panel cb-invoice-identity"`,
			`Cartão`, `Competência 2026-11`, `ui-badge--accent`, `Categoria: Saúde`,
			`href="/faturas?cartao=card&amp;limpar=1"`,
		} {
			if !strings.Contains(html, expected) {
				t.Fatalf("identity panel missing %q", expected)
			}
		}
	})

	t.Run("legacy payment summary is preserved", func(t *testing.T) {
		data := base
		data.HasLegacyPaymentSummary = true
		data.LegacyPaymentNotice = "Pagamentos anteriores ao histórico detalhado."
		html := renderInvoiceSummaryForStageC(t, data)
		if !strings.Contains(html, "Pagamentos anteriores ao histórico detalhado.") {
			t.Fatal("legacy payment notice must be preserved")
		}
	})
}

// OOB is the mechanism that keeps the summary fresh after a payment/reversal.
func TestInvoiceSummaryKeepsOOBContracts(t *testing.T) {
	data := FaturaData{
		InvoiceID:         "invoice-1",
		Reference:         "2026-11",
		IsFaturasIndex:    true,
		OOB:               true,
		CanAttemptPayment: true,
		Total:             money("10", ",00"),
		PendingAmount:     money("10", ",00"),
		HasCreditLimit:    false,
	}
	html := renderInvoiceSummaryForStageC(t, data)
	if !strings.Contains(html, `id="invoice-summary" hx-swap-oob="outerHTML"`) {
		t.Fatal("summary must keep its OOB swap contract")
	}
	if !strings.Contains(html, `id="invoice-settle-payment-modal"`) {
		t.Fatal("summary OOB must keep the payment modals inside the swapped region")
	}
}

// The summary must consume canonical tokens and never introduce a parallel
// palette or page-specific hex colors.
func TestInvoiceSummaryStylesUseCanonicalTokens(t *testing.T) {
	css := readTestFile(t, "assets/css/faturas.css")
	idx := strings.Index(css, "Invoice financial summary (Etapa C)")
	if idx < 0 {
		t.Fatal("summary stylesheet section not found")
	}
	section := css[idx:]
	if strings.Contains(section, "#") {
		t.Fatal("summary styles must not introduce hex colors")
	}
	for _, token := range []string{
		"var(--ui-text)", "var(--ui-text-muted)", "var(--ui-text-subtle)",
		"var(--ui-surface", "var(--ui-border)", "var(--ui-radius-",
		"var(--ui-success)", "var(--ui-warning)", "var(--ui-danger)",
	} {
		if !strings.Contains(section, token) {
			t.Fatalf("summary styles must consume canonical token %q", token)
		}
	}
	if strings.Contains(section, "var(--cb-") {
		t.Fatal("summary styles must consume --ui-* tokens, not the legacy bridge")
	}
}
