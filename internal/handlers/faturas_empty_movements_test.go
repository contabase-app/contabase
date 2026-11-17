package handlers

import (
	"testing"
	"time"
)

// A zero total does not prove an untouched invoice. The hub must distinguish
// an invoice with no records from one whose purchases and refunds net to zero.
func TestFaturasIndexDistinguishesNoMovementsFromZeroTotalWithMovements(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const workspaceID = "ws-empty-states"
	seedCreditCardWithLimit(t, db, workspaceID, "card-empty", "cc-empty", 100000)

	now := time.Now().Unix()
	for _, cardID := range []string{"card-zero", "card-zero-general", "card-missing", "card-paid", "card-partial"} {
		execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
			VALUES (?, ?, ?, 'CREDIT_CARD', 0, 0, ?, ?)`, cardID, workspaceID, cardID, now, now)
		execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
			VALUES (?, ?, 20, 10, 100000)`, "cc-"+cardID, cardID)
	}

	period := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	seedPeriodDebt(t, db, "card-empty", period, "CLOSED", 0, 0)
	seedPeriodDebt(t, db, "card-zero", period, "CLOSED", 10000, 0)
	execTestSQL(t, db, `INSERT INTO transaction_refunds (id, workspace_id, transaction_id, amount_cents, description, created_by)
		VALUES ('refund-zero', ?, 'expense-card-zero-2026-08', 10000, 'Estorno integral', 'user-ws-empty-states')`, workspaceID)
	seedPeriodDebt(t, db, "card-zero-general", period, "CLOSED", 10000, 0)
	execTestSQL(t, db, `INSERT INTO invoice_refunds (id, workspace_id, invoice_id, amount_cents, description, created_by)
		VALUES ('refund-zero-general', ?, 'card-zero-general-2026-08', 10000, 'Reembolso geral integral', 'user-ws-empty-states')`, workspaceID)
	seedPeriodDebt(t, db, "card-paid", period, "PAID", 10000, 10000)
	seedPeriodDebt(t, db, "card-partial", period, "CLOSED", 10000, 3000)

	cards, err := queryFaturasIndexCards(db, workspaceID, 2026, time.August)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]CreditCardCard, len(cards))
	for _, card := range cards {
		byID[card.ID] = card
	}

	cases := []struct {
		id   string
		want string
	}{
		{"card-empty", "Sem movimentação"},
		{"card-zero", "Sem valor a pagar"},
		{"card-zero-general", "Sem valor a pagar"},
		{"card-missing", "Sem fatura"},
		{"card-paid", "Pago"},
	}
	for _, tc := range cases {
		card, ok := byID[tc.id]
		if !ok {
			t.Fatalf("card %s missing from hub projection", tc.id)
		}
		if card.StatusLabel != tc.want {
			t.Fatalf("%s label = %q, want %q (financial %q, total %d)", tc.id, card.StatusLabel, tc.want, card.PaymentState.Financial, card.Amount)
		}
	}
	if byID["card-empty"].PaymentState.Financial != InvoiceFinancialEmpty ||
		byID["card-zero"].PaymentState.Financial != InvoiceFinancialEmpty ||
		byID["card-zero-general"].PaymentState.Financial != InvoiceFinancialEmpty {
		t.Fatal("untouched and net-zero invoices must stay financially empty")
	}
	if byID["card-partial"].PaymentState.Financial != InvoiceFinancialPartial {
		t.Fatalf("partial invoice state = %q, want partial", byID["card-partial"].PaymentState.Financial)
	}
	if byID["card-partial"].StatusLabel == "Sem movimentação" || byID["card-partial"].StatusLabel == "Sem valor a pagar" {
		t.Fatalf("partial invoice received an empty-state label: %q", byID["card-partial"].StatusLabel)
	}
}
