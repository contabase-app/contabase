package handlers

import (
	"strings"
	"testing"
)

func TestDashboardPreviewCardSeparatesInvoiceTotalAndPending(t *testing.T) {
	cards := buildPreviewCards([]CreditCardCard{{
		ID:        "card-contract",
		InvoiceID: "invoice-contract",
		Name:      "Cartão contrato",
		Money:     MoneyMinor(50000),
		PaymentState: InvoiceComputedState{
			Total:   50000,
			Pending: 30000,
		},
	}})
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	card := cards[0]
	if got := card.Money.rawCents(); got != 50000 {
		t.Fatalf("total = %d, want 50000", got)
	}
	if got := card.Pending.rawCents(); got != 30000 {
		t.Fatalf("pending = %d, want 30000", got)
	}
	if !card.HasPending {
		t.Fatal("partial invoice must expose pending")
	}

	open := buildPreviewCards([]CreditCardCard{{
		ID:        "card-open",
		InvoiceID: "invoice-open",
		Name:      "Cartão aberto",
		Money:     MoneyMinor(50000),
		PaymentState: InvoiceComputedState{
			Total:   50000,
			Pending: 50000,
		},
	}})
	if open[0].HasPending {
		t.Fatal("open invoice with total equal to pending must not add a redundant pending line")
	}

	paid := buildPreviewCards([]CreditCardCard{{
		ID:        "card-paid",
		InvoiceID: "invoice-paid",
		Name:      "Cartão pago",
		Money:     MoneyMinor(50000),
		PaymentState: InvoiceComputedState{
			Total:   50000,
			Pending: 0,
		},
	}})
	if got := paid[0].Pending.rawCents(); got != 0 {
		t.Fatalf("paid pending = %d, want 0", got)
	}
	if paid[0].HasPending {
		t.Fatal("paid invoice must not expose pending")
	}

	template := readTestFile(t, "templates/components/credit_card.html")
	if !strings.Contains(template, "Total da fatura") ||
		!strings.Contains(template, "Pendente R$") ||
		strings.Contains(template, "Fatura atual</span>") {
		t.Fatal("card contract must distinguish total from pending")
	}
}
