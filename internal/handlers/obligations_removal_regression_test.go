package handlers

import (
	"testing"
	"time"
)

func TestObligationsKeepInvoiceResidualAndPendingNonCardExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-obligations-removal")
	now := time.Now().UTC().Unix()

	addF1Invoice(t, db, "invoice-obligations", base.card, "2099-08", "OPEN", 0, now)
	addF1Tx(t, db, f1TxSeed{
		ID: "obligation-card-item", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, InvoiceID: "invoice-obligations",
		Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: now,
	})
	addF1Tx(t, db, f1TxSeed{
		ID: "obligation-pending", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.checking, CategoryID: base.category,
		Type: "EXPENSE", Amount: 3000, Status: "pending", DueDate: now + 5*86400, CreatedAt: now,
	})

	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 8000 {
		t.Fatalf("obligations = %d, want 8000", got.Obligations)
	}
	if !got.ObligationsScheduleAvailable || got.ObligationsDue30 != 8000 {
		t.Fatalf("30-day obligations = %d available=%v, want 8000/true", got.ObligationsDue30, got.ObligationsScheduleAvailable)
	}
}

func TestObligationsDoNotDoubleCountInvoicePaymentOrPaidExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-obligations-payment")
	now := time.Now().UTC().Unix()

	addF1Invoice(t, db, "invoice-payment-regression", base.card, "2099-08", "OPEN", 0, now)
	addF1Tx(t, db, f1TxSeed{
		ID: "invoice-item-regression", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, InvoiceID: "invoice-payment-regression",
		Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: now,
	})
	addF1Tx(t, db, f1TxSeed{
		ID: "invoice-payment-tx", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.checking, Type: "EXPENSE", Amount: 2000,
		Status: "paid", Description: "Pagamento de Fatura - Cartão", CreatedAt: now,
	})
	addF1InvoicePayment(t, db, "invoice-payment-regression-row", base.ws, "invoice-payment-regression", base.checking, "invoice-payment-tx", "", 2000, 0, base.user, now)
	addF1Tx(t, db, f1TxSeed{
		ID: "paid-expense-regression", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.checking, CategoryID: base.category,
		Type: "EXPENSE", Amount: 700, Status: "paid", CreatedAt: now,
	})

	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 3000 {
		t.Fatalf("obligations = %d, want invoice residual 3000 only", got.Obligations)
	}
}
