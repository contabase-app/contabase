package handlers

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestDashboardMonthlyForecastRules(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	seedDashboardMonthlyForecastScenario(t, db, now)

	income, expense := queryDashboardMonthlyForecast(db, "ws-forecast", now)
	if income != 10000 {
		t.Fatalf("forecast income = %d, want 10000", income)
	}
	if expense != 80000 {
		t.Fatalf("forecast expense = %d, want 80000", expense)
	}

	payableTotal, payableCount := queryDashboardPayable7dTotal(db, "ws-forecast", now)
	if payableCount != 1 {
		t.Fatalf("7d payable count = %d, want 1", payableCount)
	}
	assertMoneyDisplay(t, "7d payable total unchanged", payableTotal, "200", ",00")
}

func seedDashboardMonthlyForecastScenario(t *testing.T, db *sql.DB, now time.Time) {
	t.Helper()

	nowUnix := now.Unix()
	monthStart := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, time.UTC)
	currentRef := monthStart.Format("2006-01")
	nextRef := monthStart.AddDate(0, 1, 0).Format("2006-01")
	nextMonth := monthStart.AddDate(0, 1, 0).Unix()
	currentDay := func(day int) int64 {
		return time.Date(now.Year(), now.Month(), day, 12, 0, 0, 0, time.UTC).Unix()
	}

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-forecast', 'User Forecast', 'forecast@example.com', 'hash', ?, ?),
			('user-forecast-other', 'User Forecast Other', 'forecast-other@example.com', 'hash', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES
			('ws-forecast', 'Forecast WS', 'personal', ?, ?),
			('ws-forecast-other', 'Forecast Other WS', 'personal', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-forecast', 'user-forecast', 'ADMIN', ?),
			('ws-forecast-other', 'user-forecast-other', 'ADMIN', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('acc-forecast', 'ws-forecast', 'Conta Forecast', 'CHECKING', 0, 0, ?, ?),
			('acc-forecast-dest', 'ws-forecast', 'Conta Destino', 'CHECKING', 0, 0, ?, ?),
			('card-forecast', 'ws-forecast', 'Cartao Forecast', 'CREDIT_CARD', 0, 0, ?, ?),
			('acc-forecast-other', 'ws-forecast-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?),
			('card-forecast-other', 'ws-forecast-other', 'Cartao Other', 'CREDIT_CARD', 0, 0, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES
			('cc-forecast', 'card-forecast', 20, 10, 500000),
			('cc-forecast-other', 'card-forecast-other', 20, 10, 500000)
	`)

	insertDashboardPendingTx(t, db, "forecast-income-pending", "ws-forecast", "user-forecast", "acc-forecast", "", "INCOME", 10000, dashboardForecastDate(now, 4), dashboardForecastDate(now, 20), "Receita pendente mes", "pending")
	insertDashboardPendingTx(t, db, "forecast-income-paid", "ws-forecast", "user-forecast", "acc-forecast", "", "INCOME", 11000, dashboardForecastDate(now, 4), dashboardForecastDate(now, 21), "Receita paga mes", "paid")
	insertDashboardPendingTx(t, db, "forecast-income-outside", "ws-forecast", "user-forecast", "acc-forecast", "", "INCOME", 12000, time.Unix(nextMonth, 0).UTC().Format("2006-01-02"), "", "Receita fora mes", "pending")
	insertDashboardPendingTx(t, db, "forecast-income-other", "ws-forecast-other", "user-forecast-other", "acc-forecast-other", "", "INCOME", 13000, dashboardForecastDate(now, 4), dashboardForecastDate(now, 20), "Receita outro workspace", "pending")

	insertDashboardPendingTx(t, db, "forecast-expense-pending", "ws-forecast", "user-forecast", "acc-forecast", "", "EXPENSE", 20000, dashboardForecastDate(now, 5), dashboardForecastDate(now, 6), "Despesa pendente mes", "pending")
	insertDashboardPendingTx(t, db, "forecast-expense-paid", "ws-forecast", "user-forecast", "acc-forecast", "", "EXPENSE", 21000, dashboardForecastDate(now, 5), dashboardForecastDate(now, 22), "Despesa paga mes", "paid")
	insertDashboardPendingTx(t, db, "forecast-expense-outside", "ws-forecast", "user-forecast", "acc-forecast", "", "EXPENSE", 22000, time.Unix(nextMonth, 0).UTC().Format("2006-01-02"), "", "Despesa fora mes", "pending")
	insertDashboardPendingTx(t, db, "forecast-transfer", "ws-forecast", "user-forecast", "acc-forecast", "acc-forecast-dest", "TRANSFER", 23000, dashboardForecastDate(now, 5), dashboardForecastDate(now, 23), "Transferencia pendente", "pending")
	insertDashboardPendingTx(t, db, "forecast-invoice-payment", "ws-forecast", "user-forecast", "acc-forecast", "", "EXPENSE", 24000, dashboardForecastDate(now, 5), dashboardForecastDate(now, 24), invoicePaymentDescription("Cartao Forecast"), "pending")

	insertDashboardInvoice(t, db, "forecast-invoice-open", "card-forecast", currentRef, dashboardForecastDate(now, 3), dashboardForecastDate(now, 25), "OPEN", 0)
	insertDashboardInvoice(t, db, "forecast-invoice-closed", "card-forecast", nextRef, dashboardForecastDate(now, 8), dashboardForecastDate(now, 26), "CLOSED", 0)
	insertDashboardInvoice(t, db, "forecast-invoice-paid", "card-forecast", currentRef+"-paid", dashboardForecastDate(now, 9), dashboardForecastDate(now, 27), "PAID", 50000)
	insertDashboardInvoice(t, db, "forecast-invoice-outside", "card-forecast", nextRef+"-outside", time.Unix(nextMonth, 0).UTC().Format("2006-01-02"), time.Unix(nextMonth, 0).UTC().Format("2006-01-02"), "OPEN", 0)
	insertDashboardInvoice(t, db, "forecast-invoice-other", "card-forecast-other", currentRef, dashboardForecastDate(now, 3), dashboardForecastDate(now, 25), "OPEN", 0)

	insertDashboardCardPurchase(t, db, "forecast-card-purchase-open", "ws-forecast", "user-forecast", "card-forecast", "forecast-invoice-open", 30000, dashboardForecastDate(now, 10), "Compra fatura aberta")
	insertDashboardCardPurchase(t, db, "forecast-card-purchase-closed", "ws-forecast", "user-forecast", "card-forecast", "forecast-invoice-closed", 40000, dashboardForecastDate(now, 11), "Compra fatura fechada")
	insertDashboardCardPurchase(t, db, "forecast-card-purchase-paid", "ws-forecast", "user-forecast", "card-forecast", "forecast-invoice-paid", 50000, dashboardForecastDate(now, 12), "Compra fatura paga")
	insertDashboardCardPurchase(t, db, "forecast-card-purchase-outside", "ws-forecast", "user-forecast", "card-forecast", "forecast-invoice-outside", 60000, dashboardForecastDate(now, 13), "Compra fatura fora mes")
	insertDashboardCardPurchase(t, db, "forecast-card-purchase-other", "ws-forecast-other", "user-forecast-other", "card-forecast-other", "forecast-invoice-other", 70000, dashboardForecastDate(now, 14), "Compra outro workspace")

	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES ('forecast-partial-payment', 'ws-forecast', 'forecast-invoice-closed', 'acc-forecast', 10000, ?, 'manual', ?)
	`, currentDay(15), nowUnix)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES ('forecast-invoice-payment-row', 'ws-forecast', 'forecast-invoice-paid', 'acc-forecast', 'forecast-invoice-payment', 24000, ?, 'manual', ?)
	`, currentDay(16), nowUnix)
}

func dashboardForecastDate(now time.Time, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", now.Year(), now.Month(), day)
}
