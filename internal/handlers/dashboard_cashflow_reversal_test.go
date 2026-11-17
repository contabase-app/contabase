package handlers

import (
	"strings"
	"testing"
	"time"
)

func TestDashboardCashFlowSeparatesInvoicePaymentReversalFromOperationalIncome(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for _, stmt := range []string{
		`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-cash', 'Cash', 'cash@example.com', 'hash', 1700000000, 1700000000)`,
		`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-cash', 'Cash', 'personal', 1700000000, 1700000000)`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-cash', 'ws-cash', 'Conta', 'CHECKING', 0, 0, 1700000000, 1700000000)`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-cash', 'ws-cash', 'Cartão', 'CREDIT_CARD', 0, 0, 1700000000, 1700000000)`,
		`INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-cash', 'card-cash', 20, 10, 500000)`,
		`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-cash', 'card-cash', '2026-11', 1760000000, 1762000000, 'CLOSED', 1700000000)`,
	} {
		execTestSQL(t, db, stmt)
	}

	paidAt := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()
	reversedAt := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, date_civil, paid_at, paid_date_civil, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-cash-payment', 'ws-cash', 'u-cash', 'acc-cash', 'EXPENSE', 50000, ?, '2026-11-05', ?, '2026-11-05', ?, 'paid', 1, 1, ?, ?)`,
		paidAt, paidAt, invoicePaymentDescription("Cartão"), paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, date_civil, paid_at, paid_date_civil, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-cash-reversal', 'ws-cash', 'u-cash', 'acc-cash', 'INCOME', 50000, ?, '2026-11-06', ?, '2026-11-06', 'Estorno de pagamento', 'paid', 1, 1, ?, ?)`,
		reversedAt, reversedAt, reversedAt, reversedAt)
	execTestSQL(t, db, `INSERT INTO invoice_payments
		(id, workspace_id, invoice_id, account_id, transaction_id, reversal_transaction_id, amount_cents, paid_at, paid_date_civil, source, reversed_at, created_by, created_at)
		VALUES ('ip-cash', 'ws-cash', 'inv-cash', 'acc-cash', 'tx-cash-payment', 'tx-cash-reversal', 50000, ?, '2026-11-05', 'manual', ?, 'u-cash', ?)`,
		paidAt, reversedAt, paidAt)

	cash := buildPreviewCashFlow(db, "ws-cash", now, dashboardCanonicalBasePath, "6m", "")
	if !cash.Available {
		t.Fatalf("cash flow unavailable")
	}
	if !cash.HasAdjustments {
		t.Fatalf("cash flow must expose adjustments: %+v", cash)
	}
	if got := cash.OperationalIncome.rawCents(); got != 0 {
		t.Fatalf("operational income = %d, want 0", got)
	}
	if got := cash.AdjustmentIncome.rawCents(); got != 50000 {
		t.Fatalf("adjustment income = %d, want 50000", got)
	}
	if got := cash.TotalIncome.rawCents(); got != 50000 {
		t.Fatalf("total income = %d, want 50000", got)
	}
	if got := cash.TotalExpense.rawCents(); got != 50000 {
		t.Fatalf("total expense = %d, want 50000", got)
	}
	if got := cash.Net.rawCents(); got != 0 {
		t.Fatalf("net = %d, want 0", got)
	}

	template := readTestFile(t, "templates/components/dashboard_preview_cards.html")
	if !strings.Contains(template, "Ajustes de caixa") || !strings.Contains(template, ".CashFlow.AdjustmentIncome") {
		t.Fatal("cash flow template must separate adjustment income")
	}
}
