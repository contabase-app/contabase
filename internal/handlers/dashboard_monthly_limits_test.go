package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestDashboardMonthlyCashSummaryAndCompetenceLimits(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	seedDashboardMonthlyLimitsInvoicePaymentScenario(t, db, now)
	execTestSQL(t, db, `
		UPDATE transactions
		SET description = 'Pagamento renomeado pelo usuario'
		WHERE id = 'dash-month-invoice-payment'
	`)

	income, expense := queryDashboardMonthlySummary(db, "ws-dashboard-monthly", now)
	if income != 12000 {
		t.Fatalf("monthly income = %d, want 12000", income)
	}
	if expense != 27000 {
		t.Fatalf("monthly expense = %d, want 27000", expense)
	}

	dashboard := BuildDashboardData(db, "user-dashboard-monthly", "ws-dashboard-monthly")
	assertMoneyDisplay(t, "ResumoEntradas", dashboard.ResumoEntradas, "120", ",00")
	assertMoneyDisplay(t, "ResumoSaidas", dashboard.ResumoSaidas, "270", ",00")
	assertMoneyDisplay(t, "ResumoSaldo", dashboard.ResumoSaldo, "-150", ",00")
	if dashboard.ResumoMesLabel != dashboardCashSummaryMonthLabel(now) {
		t.Fatalf("ResumoMesLabel = %q, want %q", dashboard.ResumoMesLabel, dashboardCashSummaryMonthLabel(now))
	}
	if !dashboard.ResumoNegativo {
		t.Fatalf("ResumoNegativo = false, want true")
	}

	spent := limitSpentFromDashboardByCategory(t, dashboard.Limits, "Compras Cartao")
	if spent != 20000 {
		t.Fatalf("dashboard limit spent = %d, want 20000", spent)
	}
}

func seedDashboardMonthlyLimitsInvoicePaymentScenario(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()

	nowUnix := now.Unix()
	currentMonth := time.Date(now.Year(), now.Month(), 10, 12, 0, 0, 0, time.UTC).Unix()
	outsideMonth := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Unix()
	reference := time.Unix(currentMonth, 0).UTC().Format("2006-01")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-dashboard-monthly', 'Dashboard Monthly', 'dashboard-monthly@example.com', 'hash', ?, ?),
			('user-dashboard-monthly-other', 'Dashboard Monthly Other', 'dashboard-monthly-other@example.com', 'hash', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES
			('ws-dashboard-monthly', 'Dashboard Monthly WS', 'personal', ?, ?),
			('ws-dashboard-monthly-other', 'Dashboard Monthly Other WS', 'personal', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-dashboard-monthly', 'user-dashboard-monthly', 'ADMIN', ?),
			('ws-dashboard-monthly-other', 'user-dashboard-monthly-other', 'ADMIN', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('acc-dashboard-monthly', 'ws-dashboard-monthly', 'Conta Dashboard', 'CHECKING', 0, 0, ?, ?),
			('acc-dashboard-monthly-dest', 'ws-dashboard-monthly', 'Conta Destino', 'CHECKING', 0, 0, ?, ?),
			('card-dashboard-monthly', 'ws-dashboard-monthly', 'Cartao Dashboard', 'CREDIT_CARD', 0, 0, ?, ?),
			('acc-dashboard-monthly-other', 'ws-dashboard-monthly-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?),
			('card-dashboard-monthly-other', 'ws-dashboard-monthly-other', 'Cartao Other', 'CREDIT_CARD', 0, 0, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES
			('cc-dashboard-monthly', 'card-dashboard-monthly', 20, 10, 500000),
			('cc-dashboard-monthly-other', 'card-dashboard-monthly-other', 20, 10, 500000)
	`)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cat-dashboard-card', 'ws-dashboard-monthly', 'Compras Cartao', 'shopping-bag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('cat-dashboard-other', 'ws-dashboard-monthly-other', 'Compras Other', 'shopping-bag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-dashboard-card', 'ws-dashboard-monthly', 'cat-dashboard-card', 50000)
	`)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at)
		VALUES
			('invoice-dashboard-monthly', 'card-dashboard-monthly', ?, ?, ?, 'PAID', ?, 20000, ?),
			('invoice-dashboard-monthly-other', 'card-dashboard-monthly-other', ?, ?, ?, 'PAID', ?, 90000, ?)
	`, reference, currentMonth, currentMonth, currentMonth, nowUnix, reference, currentMonth, currentMonth, currentMonth, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('dash-month-income-paid', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', NULL, NULL, NULL, 'INCOME', 12000, ?, 'Receita paga', 'paid', 1, 1, ?, ?),
			('dash-month-income-pending', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', NULL, NULL, NULL, 'INCOME', 13000, ?, 'Receita pendente', 'pending', 1, 1, ?, ?),
			('dash-month-expense-paid', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', NULL, NULL, NULL, 'EXPENSE', 7000, ?, 'Despesa paga', 'paid', 1, 1, ?, ?),
			('dash-month-expense-pending', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', NULL, NULL, NULL, 'EXPENSE', 6000, ?, 'Despesa pendente', 'pending', 1, 1, ?, ?),
			('dash-month-card-purchase', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'card-dashboard-monthly', NULL, 'cat-dashboard-card', 'invoice-dashboard-monthly', 'EXPENSE', 20000, ?, 'Compra cartao', 'paid', 1, 1, ?, ?),
			('dash-month-invoice-payment', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', NULL, 'cat-dashboard-card', NULL, 'EXPENSE', 20000, ?, ?, 'paid', 1, 1, ?, ?),
			('dash-month-transfer', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', 'acc-dashboard-monthly-dest', NULL, NULL, 'TRANSFER', 40000, ?, 'Transferencia', 'paid', 1, 1, ?, ?),
			('dash-month-expense-outside', 'ws-dashboard-monthly', 'user-dashboard-monthly', 'acc-dashboard-monthly', NULL, NULL, NULL, 'EXPENSE', 8000, ?, 'Despesa mes anterior', 'paid', 1, 1, ?, ?),
			('dash-month-other-workspace-purchase', 'ws-dashboard-monthly-other', 'user-dashboard-monthly-other', 'card-dashboard-monthly-other', NULL, 'cat-dashboard-other', 'invoice-dashboard-monthly-other', 'EXPENSE', 90000, ?, 'Compra outro workspace', 'paid', 1, 1, ?, ?),
			('dash-month-other-workspace-payment', 'ws-dashboard-monthly-other', 'user-dashboard-monthly-other', 'acc-dashboard-monthly-other', NULL, 'cat-dashboard-other', NULL, 'EXPENSE', 90000, ?, ?, 'paid', 1, 1, ?, ?)
	`, currentMonth, nowUnix, nowUnix,
		currentMonth, nowUnix, nowUnix,
		currentMonth, nowUnix, nowUnix,
		currentMonth, nowUnix, nowUnix,
		currentMonth, nowUnix, nowUnix,
		currentMonth, invoicePaymentDescription("Cartao Dashboard"), nowUnix, nowUnix,
		currentMonth, nowUnix, nowUnix,
		outsideMonth, nowUnix, nowUnix,
		currentMonth, nowUnix, nowUnix,
		currentMonth, invoicePaymentDescription("Cartao Other"), nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES
			('dash-month-invoice-payment-row', 'ws-dashboard-monthly', 'invoice-dashboard-monthly', 'acc-dashboard-monthly', 'dash-month-invoice-payment', 20000, ?, 'manual', ?),
			('dash-month-other-payment-row', 'ws-dashboard-monthly-other', 'invoice-dashboard-monthly-other', 'acc-dashboard-monthly-other', 'dash-month-other-workspace-payment', 90000, ?, 'manual', ?)
	`, currentMonth, nowUnix, currentMonth, nowUnix)
}
