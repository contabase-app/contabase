package handlers

import (
	"database/sql"
	"testing"
	"time"
)

// previewCardForFixture rebuilds the preview and returns the card belonging to
// the fixture account. It never reads another workspace card.
func previewCardForFixture(t *testing.T, db *sql.DB, base f1Base, now time.Time) *PreviewCreditCard {
	t.Helper()
	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	for i := range data.PreviewCards {
		if data.PreviewCards[i].ID == base.card {
			return &data.PreviewCards[i]
		}
	}
	t.Fatalf("card %q not found in %#v", base.card, data.PreviewCards)
	return nil
}

func TestDashboardPreviewPartialInvoiceExposesTotalAndPending(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	addF1Tx(t, db, f1TxSeed{
		ID:          "tx-preview-invoice-payment",
		WorkspaceID: base.ws,
		UserID:      base.user,
		AccountID:   base.checking,
		Type:        "EXPENSE",
		Amount:      20000,
		Status:      "paid",
		Description: invoicePaymentDescription("Cartão XP"),
		CreatedAt:   now.Unix(),
	})
	execTestSQL(t, db, `UPDATE transactions SET paid_at = ?, paid_date_civil = '2026-11-09' WHERE id = 'tx-preview-invoice-payment' AND workspace_id = ?`, now.Unix(), base.ws)
	addF1InvoicePayment(t, db, "ip-preview-partial", base.ws, "inv-preview-card", base.checking, "tx-preview-invoice-payment", "", 20000, 0, base.user, now.Unix())

	found := previewCardForFixture(t, db, base, now)
	if got := found.Money.rawCents(); got != 50000 {
		t.Fatalf("invoice total = %d, want 50000", got)
	}
	if got := found.Pending.rawCents(); got != 30000 {
		t.Fatalf("invoice pending = %d, want 30000", got)
	}
	if !found.HasPending {
		t.Fatal("partial invoice must expose pending")
	}
	if found.PaymentState.Financial != InvoiceFinancialPartial {
		t.Fatalf("financial state = %s, want partial", found.PaymentState.Financial)
	}
}

// A general invoice refund reduces both the displayed total and the residual
// pending amount; it must never be presented as a payment or as extra income.
func TestDashboardPreviewGeneralRefundKeepsTotalAndPendingConsistent(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	addF1InvoiceRefund(t, db, "ir-preview-general", base.ws, "inv-preview-card", 10000, 0, base.user, now.Unix())

	found := previewCardForFixture(t, db, base, now)
	if got := found.Money.rawCents(); got != 40000 {
		t.Fatalf("total after general refund = %d, want 40000", got)
	}
	if got := found.Pending.rawCents(); got != 40000 {
		t.Fatalf("pending after general refund = %d, want 40000", got)
	}
	if found.HasPending {
		t.Fatal("fully open invoice must not duplicate total as pending")
	}

	addF1Tx(t, db, f1TxSeed{
		ID:          "tx-preview-refund-payment",
		WorkspaceID: base.ws,
		UserID:      base.user,
		AccountID:   base.checking,
		Type:        "EXPENSE",
		Amount:      15000,
		Status:      "paid",
		Description: invoicePaymentDescription("Cartão XP"),
		CreatedAt:   now.Unix(),
	})
	execTestSQL(t, db, `UPDATE transactions SET paid_at = ?, paid_date_civil = '2026-11-09' WHERE id = 'tx-preview-refund-payment' AND workspace_id = ?`, now.Unix(), base.ws)
	addF1InvoicePayment(t, db, "ip-preview-refund-payment", base.ws, "inv-preview-card", base.checking, "tx-preview-refund-payment", "", 15000, 0, base.user, now.Unix())

	found = previewCardForFixture(t, db, base, now)
	if got := found.Money.rawCents(); got != 40000 {
		t.Fatalf("total after refund and payment = %d, want 40000", got)
	}
	if got := found.Pending.rawCents(); got != 25000 {
		t.Fatalf("pending after refund and payment = %d, want 25000", got)
	}
	if !found.HasPending {
		t.Fatal("partially paid refunded invoice must expose pending")
	}
}

// An over-payment is a state inconsistency: the card must stay readable and
// must never expose a negative pending amount or a pending line.
func TestDashboardPreviewOverpaidInvoiceNeverShowsNegativePending(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	addF1Tx(t, db, f1TxSeed{
		ID:          "tx-preview-overpaid",
		WorkspaceID: base.ws,
		UserID:      base.user,
		AccountID:   base.checking,
		Type:        "EXPENSE",
		Amount:      70000,
		Status:      "paid",
		Description: invoicePaymentDescription("Cartão XP"),
		CreatedAt:   now.Unix(),
	})
	execTestSQL(t, db, `UPDATE transactions SET paid_at = ?, paid_date_civil = '2026-11-09' WHERE id = 'tx-preview-overpaid' AND workspace_id = ?`, now.Unix(), base.ws)
	addF1InvoicePayment(t, db, "ip-preview-overpaid", base.ws, "inv-preview-card", base.checking, "tx-preview-overpaid", "", 70000, 0, base.user, now.Unix())

	found := previewCardForFixture(t, db, base, now)
	if got := found.Pending.rawCents(); got != 0 {
		t.Fatalf("overpaid pending = %d, want 0", got)
	}
	if found.HasPending {
		t.Fatal("overpaid invoice must not expose a pending line")
	}
}
