package handlers

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

func TestRecurringOccurrencesCountByStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-rec-fin-status")
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, frequency, active, created_at, updated_at
		) VALUES ('rule-rec-fin-status', ?, ?, ?, 'EXPENSE', 1000, 'recorrente', ?, 'MONTHLY', 1, ?, ?)
	`, base.ws, base.user, base.checking, base.now, base.now, base.now)

	addF1Tx(t, db, f1TxSeed{ID: "rec-fin-future", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now + 86400, RecurringRuleID: "rule-rec-fin-status", RecurrenceSequence: 1})
	addF1Tx(t, db, f1TxSeed{ID: "rec-fin-past", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 2000, Status: "pending", CreatedAt: base.now - 2*86400, RecurringRuleID: "rule-rec-fin-status", RecurrenceSequence: 2})
	addF1Tx(t, db, f1TxSeed{ID: "rec-fin-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 4000, Status: "paid", CreatedAt: base.now - 3*86400, RecurringRuleID: "rule-rec-fin-status", RecurrenceSequence: 3})

	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 3000 {
		t.Fatalf("recurring obligations = %d, want 3000 (future 1000 + overdue 2000)", got.Obligations)
	}
	requireF1ReasonAbsent(t, got, "recurring_indeterminate")
	if availability := financialIndicatorAvailability(got, nil); !availability.ObligationsAvailable || !availability.RealBalanceAvailable {
		t.Fatalf("recurring availability = %+v, want both indicators available", availability)
	}
}

func TestRecurringCardOccurrenceCountsOnlyThroughInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-rec-fin-card")
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, frequency, active, created_at, updated_at
		) VALUES ('rule-rec-fin-card', ?, ?, ?, 'EXPENSE', 5000, 'recorrente cartão', ?, 'MONTHLY', 1, ?, ?)
	`, base.ws, base.user, base.card, base.now, base.now, base.now)
	addF1Invoice(t, db, "inv-rec-fin-card", base.card, "2026-01", "OPEN", 0, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "rec-fin-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-rec-fin-card", Type: "EXPENSE", Amount: 5000, Status: "pending", CreatedAt: base.now, RecurringRuleID: "rule-rec-fin-card", RecurrenceSequence: 1})

	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.Obligations != 5000 {
		t.Fatalf("recurring card obligations = %d, want 5000 counted once through invoice", got.Obligations)
	}
	requireF1ReasonAbsent(t, got, "recurring_indeterminate")
}

func TestDuplicateAndOrphanRecurringLineageBlockObligations(t *testing.T) {
	t.Run("duplicate occurrence", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-rec-fin-duplicate")
		execTestSQL(t, db, `
			INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at)
			VALUES ('series-rec-fin-duplicate', ?, ?, 1, ?, ?)
		`, base.ws, base.user, base.now, base.now)
		for _, id := range []string{"rec-fin-dup-1", "rec-fin-dup-2"} {
			addF1Tx(t, db, f1TxSeed{ID: id, WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now, SeriesID: "series-rec-fin-duplicate", RecurrenceSequence: 1})
		}

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "recurring_indeterminate")
		if availability := financialIndicatorAvailability(got, nil); availability.ObligationsAvailable {
			t.Fatalf("duplicate recurrence must block obligations: %+v", availability)
		}
	})

	t.Run("orphan exception", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-rec-fin-orphan")
		addF1Tx(t, db, f1TxSeed{ID: "rec-fin-orphan", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now, RecurrenceSequence: 1, IsRecurringException: true, RecurringExceptionReason: "sem linhagem"})

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "recurring_indeterminate")
	})
}

func TestExpenseDestinationMetadataDoesNotBlockOrDoubleCount(t *testing.T) {
	t.Run("pending non-card is one obligation", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-dest-fin-pending")
		addF1Account(t, db, "checking-dest-fin-pending", base.ws, "CHECKING", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "dest-fin-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: "checking-dest-fin-pending", Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		if got.Obligations != 1000 {
			t.Fatalf("pending destination expense = %d, want 1000", got.Obligations)
		}
		requireF1ReasonAbsent(t, got, "expense_composition_invalid")
		availability := financialIndicatorAvailability(got, nil)
		if !availability.ObligationsAvailable || !availability.RealBalanceAvailable {
			t.Fatalf("destination availability = %+v, want both indicators available", availability)
		}
	})

	t.Run("paid non-card has no current obligation", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-dest-fin-paid")
		addF1Account(t, db, "checking-dest-fin-paid", base.ws, "CHECKING", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "dest-fin-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: "checking-dest-fin-paid", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		if got.Obligations != 0 {
			t.Fatalf("paid destination expense = %d, want 0", got.Obligations)
		}
		requireF1ReasonAbsent(t, got, "expense_composition_invalid")
	})

	t.Run("card expense with destination remains invoice residual", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-dest-fin-card")
		addF1Account(t, db, "checking-dest-fin-card", base.ws, "CHECKING", 0, base.now)
		addF1Invoice(t, db, "inv-dest-fin-card", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "dest-fin-card-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, DestinationAccountID: "checking-dest-fin-card", InvoiceID: "inv-dest-fin-card", Type: "EXPENSE", Amount: 5000, Status: "pending", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		if got.Obligations != 5000 {
			t.Fatalf("card destination expense = %d, want 5000 once through invoice", got.Obligations)
		}
		requireF1ReasonAbsent(t, got, "expense_composition_invalid")
	})

	t.Run("destination credit card blocks to avoid invoice double count", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-dest-fin-credit-card")
		addF1Tx(t, db, f1TxSeed{ID: "dest-fin-credit-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: base.card, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "expense_composition_invalid")
		if availability := financialIndicatorAvailability(got, nil); availability.ObligationsAvailable {
			t.Fatalf("card destination left obligations available: %+v", availability)
		}
	})
}

func TestInvoicePaymentHistoricalAccountEvidence(t *testing.T) {
	t.Run("investment payment with matching paid outflow", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-pay-fin-investment")
		addF1Account(t, db, "investment-fin-pay", base.ws, "INVESTMENT", 5000, base.now)
		addF1Invoice(t, db, "inv-fin-pay-investment", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-investment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-investment", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
		addF1Tx(t, db, f1TxSeed{ID: "payment-fin-pay-investment", WorkspaceID: base.ws, UserID: base.user, AccountID: "investment-fin-pay", Type: "EXPENSE", Amount: 2000, Status: "paid", Description: invoicePaymentDescription("Cartão Investimento"), CreatedAt: base.now})
		addF1InvoicePayment(t, db, "ip-fin-pay-investment", base.ws, "inv-fin-pay-investment", "investment-fin-pay", "payment-fin-pay-investment", "", 2000, 0, base.user, base.now)

		got := mustF1(t, db, base.ws)
		if got.Obligations != 3000 {
			t.Fatalf("investment payment = %d, want residual 3000", got.Obligations)
		}
		requireF1ReasonAbsent(t, got, "invoice_payment_invalid")
		requireF1ReasonAbsent(t, got, "invoice_payment_description_ambiguous")
		if availability := financialIndicatorAvailability(got, nil); !availability.ObligationsAvailable {
			t.Fatalf("investment payment availability = %+v, want obligations available", availability)
		}
	})

	t.Run("payment attribution divergence is a non-blocking alert", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-pay-fin-attribution")
		addF1Account(t, db, "savings-fin-pay", base.ws, "SAVINGS", 5000, base.now)
		addF1Invoice(t, db, "inv-fin-pay-attribution", base.card, "2026-01", "OPEN", 0, base.now)
		addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-attribution", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-attribution", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
		addF1Tx(t, db, f1TxSeed{ID: "payment-fin-pay-attribution", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 2000, Status: "paid", Description: invoicePaymentDescription("Cartão Atribuição"), CreatedAt: base.now})
		addF1InvoicePayment(t, db, "ip-fin-pay-attribution", base.ws, "inv-fin-pay-attribution", "savings-fin-pay", "payment-fin-pay-attribution", "", 2000, 0, base.user, base.now)

		got := mustF1(t, db, base.ws)
		if got.Obligations != 3000 {
			t.Fatalf("attribution divergence = %d, want residual 3000", got.Obligations)
		}
		requireF1ReasonAbsent(t, got, "invoice_payment_invalid")
		requireF1ReasonAbsent(t, got, "invoice_payment_description_ambiguous")
		if availability := financialIndicatorAvailability(got, nil); !availability.ObligationsAvailable {
			t.Fatalf("attribution availability = %+v, want obligations available", availability)
		}
	})

	for i, tc := range []struct {
		name   string
		mutate func(t *testing.T, db *sql.DB, base f1Base)
		code   string
	}{
		{
			name: "credit card payment account",
			mutate: func(t *testing.T, db *sql.DB, base f1Base) {
				addF1Invoice(t, db, "inv-fin-pay-card-account", base.card, "2026-01", "OPEN", 0, base.now)
				addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-card-account", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-card-account", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
				addF1InvoicePayment(t, db, "ip-fin-pay-card-account", base.ws, "inv-fin-pay-card-account", base.card, "", "", 2000, 0, base.user, base.now)
			},
			code: "invoice_payment_invalid",
		},
		{
			name: "payment amount differs from linked outflow",
			mutate: func(t *testing.T, db *sql.DB, base f1Base) {
				addF1Invoice(t, db, "inv-fin-pay-amount", base.card, "2026-01", "OPEN", 0, base.now)
				addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-amount", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-amount", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
				addF1Tx(t, db, f1TxSeed{ID: "payment-fin-pay-amount", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "paid", Description: invoicePaymentDescription("Cartão Valor"), CreatedAt: base.now})
				addF1InvoicePayment(t, db, "ip-fin-pay-amount", base.ws, "inv-fin-pay-amount", base.checking, "payment-fin-pay-amount", "", 2000, 0, base.user, base.now)
			},
			code: "invoice_payment_invalid",
		},
		{
			name: "duplicate payment reference",
			mutate: func(t *testing.T, db *sql.DB, base f1Base) {
				execTestSQL(t, db, `DROP INDEX IF EXISTS uq_invoice_payments_workspace_transaction`)
				addF1Invoice(t, db, "inv-fin-pay-duplicate", base.card, "2026-01", "OPEN", 0, base.now)
				addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-duplicate", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-duplicate", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
				addF1Tx(t, db, f1TxSeed{ID: "payment-fin-pay-duplicate", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "paid", Description: invoicePaymentDescription("Cartão Duplicado"), CreatedAt: base.now})
				addF1InvoicePayment(t, db, "ip-fin-pay-duplicate-a", base.ws, "inv-fin-pay-duplicate", base.checking, "payment-fin-pay-duplicate", "", 1000, 0, base.user, base.now)
				addF1InvoicePayment(t, db, "ip-fin-pay-duplicate-b", base.ws, "inv-fin-pay-duplicate", base.checking, "payment-fin-pay-duplicate", "", 1000, 0, base.user, base.now)
			},
			code: "payment_reference_duplicate",
		},
		{
			name: "reversal without reversed timestamp",
			mutate: func(t *testing.T, db *sql.DB, base f1Base) {
				addF1Invoice(t, db, "inv-fin-pay-reversal", base.card, "2026-01", "OPEN", 0, base.now)
				addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-reversal", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
				addF1Tx(t, db, f1TxSeed{ID: "payment-fin-pay-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "paid", Description: invoicePaymentDescription("Cartão Reversão"), CreatedAt: base.now})
				addF1Tx(t, db, f1TxSeed{ID: "reversal-fin-pay-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 1000, Status: "paid", Description: invoicePaymentReversalDescription, CreatedAt: base.now})
				addF1InvoicePayment(t, db, "ip-fin-pay-reversal", base.ws, "inv-fin-pay-reversal", base.checking, "payment-fin-pay-reversal", "reversal-fin-pay-reversal", 1000, 0, base.user, base.now)
			},
			code: "invoice_payment_invalid",
		},
		{
			name: "reversal account diverges from payment account",
			mutate: func(t *testing.T, db *sql.DB, base f1Base) {
				addF1Account(t, db, "savings-fin-pay-reversal-mismatch", base.ws, "SAVINGS", 5000, base.now)
				addF1Invoice(t, db, "inv-fin-pay-reversal-mismatch", base.card, "2026-01", "OPEN", 0, base.now)
				addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-reversal-mismatch", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-reversal-mismatch", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
				addF1Tx(t, db, f1TxSeed{ID: "payment-fin-pay-reversal-mismatch", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "paid", Description: invoicePaymentDescription("Cartão Reversão Divergente"), CreatedAt: base.now})
				addF1Tx(t, db, f1TxSeed{ID: "reversal-fin-pay-reversal-mismatch", WorkspaceID: base.ws, UserID: base.user, AccountID: "savings-fin-pay-reversal-mismatch", Type: "INCOME", Amount: 1000, Status: "paid", Description: invoicePaymentReversalDescription, CreatedAt: base.now})
				addF1InvoicePayment(t, db, "ip-fin-pay-reversal-mismatch", base.ws, "inv-fin-pay-reversal-mismatch", base.checking, "payment-fin-pay-reversal-mismatch", "reversal-fin-pay-reversal-mismatch", 1000, base.now, base.user, base.now)
			},
			code: "invoice_payment_invalid",
		},
		{
			name: "negative legacy paid amount",
			mutate: func(t *testing.T, db *sql.DB, base f1Base) {
				addF1Invoice(t, db, "inv-fin-pay-negative", base.card, "2026-01", "OPEN", -100, base.now)
				addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-pay-negative", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-pay-negative", Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: base.now})
			},
			code: "invoice_payment_invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			base := seedF1Base(t, db, fmt.Sprintf("ws-fin-pay-%d", i))
			tc.mutate(t, db, base)
			got := mustF1(t, db, base.ws)
			requireF1Unavailable(t, got, tc.code)
			if availability := financialIndicatorAvailability(got, nil); availability.ObligationsAvailable {
				t.Fatalf("blocking payment defect %q left obligations available: %+v", tc.code, availability)
			}
		})
	}
}

func TestObligationsCompositionWithoutDoubleCounting(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-fin-composition")
	addF1Account(t, db, "savings-fin-composition", base.ws, "SAVINGS", 20000, base.now)
	addF1Account(t, db, "investment-fin-composition", base.ws, "INVESTMENT", 30000, base.now)
	addF1Account(t, db, "wallet-fin-composition", base.ws, "WALLET", 40000, base.now)
	addF1Account(t, db, "checking-fin-destination", base.ws, "CHECKING", 0, base.now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, frequency, active, created_at, updated_at
		) VALUES ('rule-fin-composition', ?, ?, ?, 'EXPENSE', 1000, 'recorrente composição', ?, 'MONTHLY', 1, ?, ?)
	`, base.ws, base.user, base.checking, base.now, base.now, base.now)

	addF1Invoice(t, db, "inv-fin-composition-open", base.card, "2026-01", "OPEN", 0, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-open", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-composition-open", Type: "EXPENSE", Amount: 6000, Status: "paid", CreatedAt: base.now})

	addF1Invoice(t, db, "inv-fin-composition-closed", base.card, "2026-02", "CLOSED", 0, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-closed", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-composition-closed", Type: "EXPENSE", Amount: 8000, Status: "paid", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "payment-fin-closed", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 3000, Status: "paid", Description: invoicePaymentDescription("Cartão Fechado"), CreatedAt: base.now})
	addF1InvoicePayment(t, db, "ip-fin-composition-closed", base.ws, "inv-fin-composition-closed", base.checking, "payment-fin-closed", "", 3000, 0, base.user, base.now)

	addF1Invoice(t, db, "inv-fin-composition-paid", base.card, "2026-03", "PAID", 6000, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-composition-paid", Type: "EXPENSE", Amount: 7000, Status: "paid", CreatedAt: base.now})

	addF1Invoice(t, db, "inv-fin-composition-recurring", base.card, "2026-04", "OPEN", 0, base.now)
	addF1Tx(t, db, f1TxSeed{ID: "purchase-fin-recurring-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-fin-composition-recurring", Type: "EXPENSE", Amount: 1500, Status: "pending", CreatedAt: base.now, RecurringRuleID: "rule-fin-composition", RecurrenceSequence: 10})

	addF1Tx(t, db, f1TxSeed{ID: "pending-fin-checking", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "pending-fin-savings", WorkspaceID: base.ws, UserID: base.user, AccountID: "savings-fin-composition", Type: "EXPENSE", Amount: 2000, Status: "pending", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "pending-fin-investment", WorkspaceID: base.ws, UserID: base.user, AccountID: "investment-fin-composition", Type: "EXPENSE", Amount: 3000, Status: "pending", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "pending-fin-wallet", WorkspaceID: base.ws, UserID: base.user, AccountID: "wallet-fin-composition", Type: "EXPENSE", Amount: 4000, Status: "pending", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "pending-fin-destination", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: "checking-fin-destination", Type: "EXPENSE", Amount: 1600, Status: "pending", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "paid-fin-checking", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 500, Status: "paid", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "recurring-fin-future", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1200, Status: "pending", CreatedAt: base.now, RecurringRuleID: "rule-fin-composition", RecurrenceSequence: 1})
	addF1Tx(t, db, f1TxSeed{ID: "recurring-fin-overdue", WorkspaceID: base.ws, UserID: base.user, AccountID: "investment-fin-composition", Type: "EXPENSE", Amount: 1300, Status: "pending", CreatedAt: base.now - 2*86400, RecurringRuleID: "rule-fin-composition", RecurrenceSequence: 2})
	addF1Tx(t, db, f1TxSeed{ID: "recurring-fin-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1400, Status: "paid", CreatedAt: base.now - 3*86400, RecurringRuleID: "rule-fin-composition", RecurrenceSequence: 3})
	addF1Tx(t, db, f1TxSeed{ID: "paid-fin-destination", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: "checking-fin-destination", Type: "EXPENSE", Amount: 1700, Status: "paid", CreatedAt: base.now})

	var before int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	got := mustF1(t, db, base.ws)
	var after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("obligation composition wrote data")
	}

	const wantObligations = int64(27600)
	if got.Obligations != wantObligations {
		t.Fatalf("composed obligations = %d, want %d", got.Obligations, wantObligations)
	}
	for _, code := range []string{"recurring_indeterminate", "expense_composition_invalid", "invoice_payment_invalid", "invoice_payment_description_ambiguous", "payment_reference_duplicate"} {
		requireF1ReasonAbsent(t, got, code)
	}
	availability := financialIndicatorAvailability(got, nil)
	if !availability.ObligationsAvailable || !availability.RealBalanceAvailable {
		t.Fatalf("composition availability = %+v, want both indicators available", availability)
	}
}

func TestHistoricalAlertsStayInsideWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	affected := seedF1Base(t, db, "ws-fin-alert-a")
	clean := seedF1Base(t, db, "ws-fin-alert-b")

	addF1Account(t, db, "checking-fin-alert", affected.ws, "CHECKING", 0, affected.now)
	addF1Tx(t, db, f1TxSeed{ID: "pending-fin-alert", WorkspaceID: affected.ws, UserID: affected.user, AccountID: affected.checking, DestinationAccountID: "checking-fin-alert", Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: affected.now})

	affectedGot := mustF1(t, db, affected.ws)
	requireF1Unavailable(t, affectedGot, "expense_destination_historical")
	if affectedGot.Obligations != 1000 {
		t.Fatalf("affected workspace obligations = %d, want 1000", affectedGot.Obligations)
	}
	affectedAvailability := financialIndicatorAvailability(affectedGot, nil)
	if !affectedAvailability.ObligationsAvailable || !affectedAvailability.RealBalanceAvailable {
		t.Fatalf("affected workspace availability = %+v, want both indicators available", affectedAvailability)
	}

	cleanGot := mustF1(t, db, clean.ws)
	requireF1Reliable(t, cleanGot)
	if cleanGot.Obligations != 0 {
		t.Fatalf("clean workspace inherited obligations: %+v", cleanGot)
	}
	for _, code := range []string{"expense_destination_historical", "invoice_payment_account_historical", "invoice_payment_attribution_historical"} {
		requireF1ReasonAbsent(t, cleanGot, code)
	}
	affectedDashboard := BuildDashboardData(db, affected.user, affected.ws)
	cleanDashboard := BuildDashboardData(db, clean.user, clean.ws)
	if !affectedDashboard.Balance.ObligationsAvailable || !affectedDashboard.Balance.RealBalanceAvailable {
		t.Fatal("affected dashboard lost validated indicators")
	}
	if !cleanDashboard.Balance.ObligationsAvailable || !cleanDashboard.Balance.RealBalanceAvailable {
		t.Fatal("clean dashboard lost validated indicators")
	}
}

func TestCrossWorkspaceExpenseReferencesBlockObligations(t *testing.T) {
	t.Run("destination account from another workspace", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-xws-dest-a")
		other := seedF1Base(t, db, "ws-fin-xws-dest-b")
		addF1Tx(t, db, f1TxSeed{ID: "xws-fin-dest", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: other.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now})

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "expense_account_invalid")
		if availability := financialIndicatorAvailability(got, nil); availability.ObligationsAvailable {
			t.Fatalf("foreign destination left obligations available: %+v", availability)
		}
	})

	t.Run("recurring rule from another workspace", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-xws-rule-a")
		other := seedF1Base(t, db, "ws-fin-xws-rule-b")
		execTestSQL(t, db, `
			INSERT INTO recurring_rules (
				id, workspace_id, user_id, account_id, type, amount, description,
				start_date, frequency, active, created_at, updated_at
			) VALUES ('xws-fin-rule', ?, ?, ?, 'EXPENSE', 1000, 'estrangeira', ?, 'MONTHLY', 1, ?, ?)
		`, other.ws, other.user, other.checking, other.now, other.now, other.now)
		addF1Tx(t, db, f1TxSeed{ID: "xws-fin-rule-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now, RecurringRuleID: "xws-fin-rule", RecurrenceSequence: 1})

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "recurring_indeterminate")
		if availability := financialIndicatorAvailability(got, nil); availability.ObligationsAvailable {
			t.Fatalf("foreign recurring rule left obligations available: %+v", availability)
		}
	})

	t.Run("recurring series from another workspace", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-fin-xws-series-a")
		other := seedF1Base(t, db, "ws-fin-xws-series-b")
		execTestSQL(t, db, `
			INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at)
			VALUES ('xws-fin-series', ?, ?, 1, ?, ?)
		`, other.ws, other.user, other.now, other.now)
		addF1Tx(t, db, f1TxSeed{ID: "xws-fin-series-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: base.now, SeriesID: "xws-fin-series", RecurrenceSequence: 1})

		got := mustF1(t, db, base.ws)
		requireF1Unavailable(t, got, "recurring_indeterminate")
		if availability := financialIndicatorAvailability(got, nil); availability.ObligationsAvailable {
			t.Fatalf("foreign recurring series left obligations available: %+v", availability)
		}
	})
}

func TestUnknownReasonStillBlocksEveryIndicator(t *testing.T) {
	position := services.WorkspaceFinancialPositionF1{
		Status:  services.AvailableBalanceUnavailable,
		Reasons: []services.AvailableBalanceReason{{Code: "future_unclassified_reason"}},
	}
	if got := financialIndicatorAvailability(position, nil); got != (FinancialIndicatorAvailability{}) {
		t.Fatalf("unknown reason availability = %+v, want all indicators blocked", got)
	}
}
