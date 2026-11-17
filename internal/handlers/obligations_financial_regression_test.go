package handlers

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

// Regressão financeira preservada após a remoção operacional de Reservas.
// Estes cenários não dependem de caixinhas: eles protegem faturas, pagamentos,
// reembolsos, recorrências, cartões arquivados, overflow e leitura sem escrita.

func requireF1ReasonAbsent(t *testing.T, got services.WorkspaceFinancialPositionF1, code string) {
	t.Helper()
	for _, reason := range got.Reasons {
		if reason.Code == code {
			t.Fatalf("unexpected F1 reason %q: %+v", code, got.Reasons)
		}
	}
}

func TestObligationsCardPurchaseAndPaymentsRemainNeutral(t *testing.T) {
	t.Run("card purchase is an invoice obligation", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-card-purchase")
		execTestSQL(t, db, `UPDATE accounts SET current_balance = 5000 WHERE id = ?`, base.card)
		addF1Invoice(t, db, "inv-fin-card", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-card", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.RealBalance != 10000 || got.Obligations != 5000 {
			t.Fatalf("card purchase = %+v, want real 10000 obligations 5000", got)
		}
	})

	for _, tc := range []struct {
		name     string
		payment  int64
		checking int64
		residual int64
	}{
		{name: "partial", payment: 2000, checking: 8000, residual: 3000},
		{name: "full", payment: 5000, checking: 5000, residual: 0},
		{name: "overpaid", payment: 6000, checking: 4000, residual: 0},
	} {
		t.Run("payment-"+tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			base := seedF1Base(t, db, "ws-fin-payment-"+tc.name)
			addF1Invoice(t, db, "inv-fin-payment", base.card, "2026-01", "OPEN", 0, base.now)
			addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-payment", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
			addF1Tx(t, db, f1TxSeed{ID: "payment-fin-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: tc.payment, Status: "paid", Description: invoicePaymentDescription("Cartão Financeiro"), CreatedAt: base.now})
			execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = ?`, tc.checking, base.checking)
			addF1InvoicePayment(t, db, "ip-fin-payment", base.ws, "inv-fin-payment", base.checking, "payment-fin-payment", "", tc.payment, 0, base.user, base.now)

			got := mustF1(t, db, base.ws)
			requireF1Reliable(t, got)
			if got.RealBalance != tc.checking || got.Obligations != tc.residual {
				t.Fatalf("payment %s = %+v, want real %d obligations %d", tc.name, got, tc.checking, tc.residual)
			}
			requireF1ReasonAbsent(t, got, "invoice_payment_invalid")
			requireF1ReasonAbsent(t, got, "invoice_payment_description_ambiguous")
		})
	}
}

func TestObligationsInvoiceReversalReopensResidual(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-fin-reversal")
	addF1Invoice(t, db, "inv-fin-reversal", base.card, "2026-01", "OPEN", 0, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-reversal", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "payment-fin-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 2000, Status: "paid", Description: invoicePaymentDescription("Cartão Reversão"), CreatedAt: base.now})
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 8000 WHERE id = ?`, base.checking)
	addF1InvoicePayment(t, db, "ip-fin-reversal", base.ws, "inv-fin-reversal", base.checking, "payment-fin-reversal", "", 2000, 0, base.user, base.now)

	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 3000 {
		t.Fatalf("before reversal obligations = %d, want 3000", got.Obligations)
	}

	addF1Tx(t, db, f1TxSeed{ID: "reversal-fin-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 2000, Status: "paid", Description: invoicePaymentReversalDescription, CreatedAt: base.now})
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 10000 WHERE id = ?`, base.checking)
	execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = ?, reversal_transaction_id = 'reversal-fin-reversal' WHERE id = 'ip-fin-reversal'`, base.now)

	got = mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 5000 || got.RealBalance != 10000 {
		t.Fatalf("after reversal = %+v, want real 10000 obligations 5000", got)
	}
}

func TestObligationsInvoiceItemAndTechnicalPaymentCountOnce(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-fin-technical")
	addF1Invoice(t, db, "inv-fin-technical", base.card, "2026-01", "OPEN", 0, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-technical", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-technical", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "payment-fin-technical", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 2000, Status: "paid", Description: invoicePaymentDescription("Cartão Técnico"), CreatedAt: base.now})
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 8000 WHERE id = ?`, base.checking)
	addF1InvoicePayment(t, db, "ip-fin-technical", base.ws, "inv-fin-technical", base.checking, "payment-fin-technical", "", 2000, 0, base.user, base.now)

	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 3000 {
		t.Fatalf("technical payment obligations = %d, want residual 3000", got.Obligations)
	}
	requireF1ReasonAbsent(t, got, "invoice_payment_invalid")
	requireF1ReasonAbsent(t, got, "invoice_payment_description_ambiguous")
}

func TestObligationsRefundsAndReversalsRemainBounded(t *testing.T) {
	t.Run("item refund limited to residual", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-refund-item")
		addF1Invoice(t, db, "inv-fin-refund", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-refund", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-refund", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
		addF1Tx(t, db, f1TxSeed{ID: "payment-fin-refund", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 900, Status: "paid", Description: invoicePaymentDescription("Cartão Refund"), CreatedAt: base.now})
		execTestSQL(t, db, `UPDATE accounts SET current_balance = 9100 WHERE id = ?`, base.checking)
		addF1InvoicePayment(t, db, "ip-fin-refund", base.ws, "inv-fin-refund", base.checking, "payment-fin-refund", "", 900, 0, base.user, base.now)
		addF1TransactionRefund(t, db, "refund-fin-item", base.ws, "purchase-fin-refund", 200, 0, base.user, base.now)

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 0 {
			t.Fatalf("item refund obligations = %d, want 0", got.Obligations)
		}
		execTestSQL(t, db, `UPDATE transaction_refunds SET reversed_at = ?, reversed_by = ? WHERE id = 'refund-fin-item'`, base.now, base.user)
		got = mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 100 {
			t.Fatalf("item refund reversal obligations = %d, want 100", got.Obligations)
		}
	})

	t.Run("item and invoice refunds coexist", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-refund-coexist")
		addF1Invoice(t, db, "inv-fin-coexist", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-coexist", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-coexist", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
		addF1TransactionRefund(t, db, "refund-fin-coexist-item", base.ws, "purchase-fin-coexist", 200, 0, base.user, base.now)
		addF1InvoiceRefund(t, db, "refund-fin-coexist-invoice", base.ws, "inv-fin-coexist", 100, 0, base.user, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "payment-fin-coexist", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 300, Status: "paid", Description: invoicePaymentDescription("Cartão Coexist"), CreatedAt: base.now})
		execTestSQL(t, db, `UPDATE accounts SET current_balance = 9700 WHERE id = ?`, base.checking)
		addF1InvoicePayment(t, db, "ip-fin-coexist", base.ws, "inv-fin-coexist", base.checking, "payment-fin-coexist", "", 300, 0, base.user, base.now)

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 400 {
			t.Fatalf("combined refunds obligations = %d, want 400", got.Obligations)
		}
	})
}

func TestObligationsOverpaymentAndCreditClamp(t *testing.T) {
	t.Run("overpaid invoice and reversal sequence", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-overpaid")
		addF1Invoice(t, db, "inv-fin-overpaid", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-overpaid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-overpaid", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
		for _, payment := range []struct {
			id     string
			amount int64
		}{{"payment-fin-overpaid-1", 1000}, {"payment-fin-overpaid-2", 500}, {"payment-fin-overpaid-3", 300}} {
			addF1Tx(t, db, f1TxSeed{ID: payment.id, WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: payment.amount, Status: "paid", Description: invoicePaymentDescription("Cartão Overpaid"), CreatedAt: base.now})
			addF1InvoicePayment(t, db, "ip-"+payment.id, base.ws, "inv-fin-overpaid", base.checking, payment.id, "", payment.amount, 0, base.user, base.now)
		}

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 0 {
			t.Fatalf("overpaid obligations = %d, want 0", got.Obligations)
		}

		for _, reversal := range []struct {
			paymentID string
			amount    int64
		}{{"payment-fin-overpaid-3", 300}, {"payment-fin-overpaid-2", 500}} {
			reversalID := "reversal-" + reversal.paymentID
			addF1Tx(t, db, f1TxSeed{ID: reversalID, WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: reversal.amount, Status: "paid", Description: invoicePaymentReversalDescription, CreatedAt: base.now})
			execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = ?, reversal_transaction_id = ? WHERE id = ?`, base.now, reversalID, "ip-"+reversal.paymentID)
			got = mustF1(t, db, base.ws)
			requireF1Reliable(t, got)
			if got.Obligations != 0 {
				t.Fatalf("still overpaid after reversing %s = %d, want 0", reversal.paymentID, got.Obligations)
			}
		}

		addF1Tx(t, db, f1TxSeed{ID: "reversal-fin-overpaid-1", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 1000, Status: "paid", Description: invoicePaymentReversalDescription, CreatedAt: base.now})
		execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = ?, reversal_transaction_id = 'reversal-fin-overpaid-1' WHERE id = 'ip-payment-fin-overpaid-1'`, base.now)
		got = mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 1000 {
			t.Fatalf("fully reversed obligations = %d, want 1000", got.Obligations)
		}
	})

	t.Run("credit in one invoice does not pay another", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-credit")
		addF1Invoice(t, db, "inv-fin-credit", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-credit", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-credit", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
		addF1InvoiceRefund(t, db, "refund-fin-credit", base.ws, "inv-fin-credit", 500, 0, base.user, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "payment-fin-credit", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "paid", Description: invoicePaymentDescription("Cartão Credit"), CreatedAt: base.now})
		addF1InvoicePayment(t, db, "ip-fin-credit", base.ws, "inv-fin-credit", base.checking, "payment-fin-credit", "", 1000, 0, base.user, base.now)
		addF1Invoice(t, db, "inv-fin-credit-debt", base.card, "2026-02", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-credit-debt", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-credit-debt", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 1000 {
			t.Fatalf("credit isolation obligations = %d, want 1000", got.Obligations)
		}
	})
}

func TestObligationsArchivedCardAndReadPath(t *testing.T) {
	t.Run("archived card debt remains", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-archived-card")
		execTestSQL(t, db, `UPDATE accounts SET archived_at = ? WHERE id = ?`, base.now, base.card)
		addF1Invoice(t, db, "inv-fin-archived", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-archived", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-archived", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.Obligations != 1000 || got.RealBalance != 10000 {
			t.Fatalf("archived card = %+v, want real 10000 obligations 1000", got)
		}
	})

	t.Run("paid card item without invoice blocks obligations", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-card-no-invoice")
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-no-invoice", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "card_expense_without_invoice")
	})

	t.Run("archived checking pending expense remains", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-archived-checking")
		execTestSQL(t, db, `UPDATE accounts SET archived_at = ? WHERE id = ?`, base.now, base.checking)
		addF1Tx(t, db, f1TxSeed{ID: "pending-fin-archived", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 500, Status: "pending", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Reliable(t, got)
		if got.RealBalance != 0 || got.Obligations != 500 {
			t.Fatalf("archived checking = %+v, want real 0 obligations 500", got)
		}
	})

	t.Run("read path does not write", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-readonly")
		var before int64
		if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		_ = mustF1(t, db, base.ws)
		var after int64
		if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("F1 read path wrote %d changes", after-before)
		}
	})
}

func TestObligationsWorkspaceIsolationAndInvalidReferences(t *testing.T) {
	t.Run("personal and business", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		personal := seedF1Base(t, db, "ws-fin-personal")
		business := seedF1Base(t, db, "ws-fin-business")
		execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = ?`, business.ws)
		for _, base := range []f1Base{personal, business} {
			addF1Invoice(t, db, "inv-"+base.ws, base.card, "2026-01", "OPEN", 0, base.now)
			addF1Tx(t, db, f1TxSeed{ID: "purchase-" + base.ws, WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-" + base.ws, Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
			addF1Tx(t, db, f1TxSeed{ID: "pending-" + base.ws, WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 500, Status: "pending", CreatedAt: base.now})
		}
		for _, base := range []f1Base{personal, business} {
			got := mustF1(t, db, base.ws)
			requireF1Reliable(t, got)
			if got.Obligations != 1500 {
				t.Fatalf("%s obligations = %d, want 1500", base.ws, got.Obligations)
			}
		}
	})

	t.Run("cross-workspace account", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-account-a")
		other := seedF1Base(t, db, "ws-fin-account-b")
		addF1Tx(t, db, f1TxSeed{ID: "expense-fin-account", WorkspaceID: base.ws, UserID: base.user, AccountID: other.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now})
		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "expense_account_invalid")
	})

	t.Run("cross-workspace invoice link", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-invoice-a")
		other := seedF1Base(t, db, "ws-fin-invoice-b")
		addF1Invoice(t, db, "inv-fin-foreign", other.card, "2026-01", "OPEN", 0, other.now)
		addF1Tx(t, db, f1TxSeed{ID: "expense-fin-foreign", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-foreign", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "invoice_link_invalid")
	})

	t.Run("cross-workspace payment account", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-pay-a")
		other := seedF1Base(t, db, "ws-fin-pay-b")
		addF1Invoice(t, db, "inv-fin-pay", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
		addF1InvoicePayment(t, db, "ip-fin-pay", base.ws, "inv-fin-pay", other.checking, "", "", 1000, 0, base.user, base.now)
		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "invoice_payment_invalid")
	})

	t.Run("cross-workspace transaction refund", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-refund-a")
		other := seedF1Base(t, db, "ws-fin-refund-b")
		addF1Invoice(t, db, "inv-fin-refund-other", other.card, "2026-01", "OPEN", 0, other.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-refund-other", WorkspaceID: other.ws, UserID: other.user, AccountID: other.card, InvoiceID: "inv-fin-refund-other", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: other.now})
		execTestSQL(t, db, `DROP TRIGGER IF EXISTS trg_transaction_refunds_validate_insert`)
		addF1TransactionRefund(t, db, "refund-fin-foreign", base.ws, "purchase-fin-refund-other", 100, 0, base.user, base.now)
		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "transaction_refund_invalid")
	})
}

func TestObligationsOverflowAndAmbiguousTechnicalDescriptions(t *testing.T) {
	t.Run("real overflow", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-overflow-real")
		execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = ?`, int64(math.MaxInt64), base.checking)
		addF1Account(t, db, "overflow-fin-extra", base.ws, "SAVINGS", 1, base.now)
		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "real_overflow")
	})

	t.Run("obligations overflow", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-overflow-obligations")
		for i := 1; i <= 2; i++ {
			addF1Tx(t, db, f1TxSeed{ID: fmt.Sprintf("expense-fin-overflow-%d", i), WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: int64(math.MaxInt64), Status: "pending", CreatedAt: base.now})
		}
		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "obligations_overflow")
	})

	t.Run("unlinked technical descriptions stay ambiguous", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			txType      string
			description string
		}{
			{name: "paid expense with payment description", txType: "EXPENSE", description: invoicePaymentDescription("Cartão Ambíguo")},
			{name: "income with reversal description", txType: "INCOME", description: invoicePaymentReversalDescription},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				base := seedF1Base(t, db, "ws-fin-ambiguous-"+strings.ReplaceAll(tc.name, " ", "-"))
				addF1Tx(t, db, f1TxSeed{ID: "tx-fin-ambiguous", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: tc.txType, Amount: 1000, Status: "paid", Description: tc.description, CreatedAt: base.now})
				got := mustF1(t, db, base.ws)
				requireF1Unavailable(t, got, "invoice_payment_description_ambiguous")
			})
		}
	})
}
