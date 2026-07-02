package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestSchedulePendingExpenseAndIncome(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-sched-expense", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	insertScheduledPaymentTestTransaction(t, db, "tx-sched-income", models.TransactionTypeIncome, "checking-test", "", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	for _, id := range []string{"tx-sched-expense", "tx-sched-income"} {
		rr := httptest.NewRecorder()
		handler.HandleTogglePagamento(rr, formPost("/transacoes/"+id+"/status-pagamento", url.Values{
			"payment_action":         {"scheduled"},
			"scheduled_payment_date": {"2026-07-18"},
		}), id)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %q", id, rr.Code, rr.Body.String())
		}

		var status string
		var scheduledAt, paidAt sql.NullInt64
		if err := db.QueryRow(`SELECT status, scheduled_payment_at, paid_at FROM transactions WHERE id = ?`, id).Scan(&status, &scheduledAt, &paidAt); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if status != models.PaymentStatusPending {
			t.Fatalf("%s status = %q, want pending", id, status)
		}
		if !scheduledAt.Valid || scheduledAt.Int64 != testAppCivilDate("2026-07-18") {
			t.Fatalf("%s scheduled_payment_at = %v/%d", id, scheduledAt.Valid, scheduledAt.Int64)
		}
		if paidAt.Valid {
			t.Fatalf("%s paid_at = %d, want NULL", id, paidAt.Int64)
		}
	}
}

func TestScheduleRejectsPaidCardAndTransferTransactions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	paidAt := testUnixDate("2026-07-12")
	insertScheduledPaymentTestTransaction(t, db, "tx-sched-paid", models.TransactionTypeExpense, "checking-test", "", "paid", &paidAt)
	insertScheduledPaymentTestTransaction(t, db, "tx-sched-transfer", models.TransactionTypeTransfer, "checking-test", "checking-extra", "pending", nil)
	insertScheduledPaymentTestTransaction(t, db, "tx-sched-card", models.TransactionTypeExpense, "card-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'invoice-2026-08' WHERE id = 'tx-sched-card'`)

	handler := testPaidInvoiceMutationHandler(db)
	for _, id := range []string{"tx-sched-paid", "tx-sched-transfer", "tx-sched-card"} {
		rr := httptest.NewRecorder()
		handler.HandleTogglePagamento(rr, formPost("/transacoes/"+id+"/status-pagamento", url.Values{
			"payment_action":         {"scheduled"},
			"scheduled_payment_date": {"2026-07-18"},
		}), id)
		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s status = %d, want 422, body = %q", id, rr.Code, rr.Body.String())
		}
	}
}

func TestClearAndManualPaymentRemoveSchedule(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-clear-schedule", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	insertScheduledPaymentTestTransaction(t, db, "tx-pay-schedule", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id IN ('tx-clear-schedule', 'tx-pay-schedule')`, testAppCivilDate("2026-07-18"))

	handler := testPaidInvoiceMutationHandler(db)
	clearRR := httptest.NewRecorder()
	handler.HandleTogglePagamento(clearRR, formPost("/transacoes/tx-clear-schedule/status-pagamento", url.Values{
		"payment_action": {"clear_schedule"},
	}), "tx-clear-schedule")
	if clearRR.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %q", clearRR.Code, clearRR.Body.String())
	}

	payRR := httptest.NewRecorder()
	handler.HandleTogglePagamento(payRR, formPost("/transacoes/tx-pay-schedule/status-pagamento", url.Values{
		"payment_date": {"2026-07-14"},
	}), "tx-pay-schedule")
	if payRR.Code != http.StatusOK {
		t.Fatalf("pay status = %d, body = %q", payRR.Code, payRR.Body.String())
	}

	for _, id := range []string{"tx-clear-schedule", "tx-pay-schedule"} {
		var scheduledAt, executedAt sql.NullInt64
		if err := db.QueryRow(`SELECT scheduled_payment_at, scheduled_payment_executed_at FROM transactions WHERE id = ?`, id).Scan(&scheduledAt, &executedAt); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if scheduledAt.Valid || executedAt.Valid {
			t.Fatalf("%s schedule fields = %v/%v, want NULL", id, scheduledAt, executedAt)
		}
	}
}

func TestScheduledFilterAndCardRendering(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-filter-scheduled", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	insertScheduledPaymentTestTransaction(t, db, "tx-filter-pending", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-filter-scheduled'`, testAppCivilDate("2026-07-18"))

	handler := testPaidInvoiceMutationHandler(db)
	scheduledData, err := handler.buildLancamentosData("", 7, 2026, LancamentosFilters{Situacoes: []string{"agendado"}})
	if err != nil {
		t.Fatalf("build scheduled data: %v", err)
	}
	if got := transactionRowIDs(scheduledData.Transactions); len(got) != 1 || got[0] != "tx-filter-scheduled" {
		t.Fatalf("scheduled rows = %#v", got)
	}
	if !scheduledData.Transactions[0].IsScheduled || scheduledData.Transactions[0].ScheduledPaymentAtDisplay != "18/07/2026" {
		t.Fatalf("scheduled row viewmodel = %#v", scheduledData.Transactions[0])
	}

	pendingData, err := handler.buildLancamentosData("", 7, 2026, LancamentosFilters{Situacoes: []string{"pendente"}})
	if err != nil {
		t.Fatalf("build pending data: %v", err)
	}
	if strings.Contains(strings.Join(transactionRowIDs(pendingData.Transactions), ","), "tx-filter-scheduled") {
		t.Fatalf("pending filter included scheduled row: %#v", transactionRowIDs(pendingData.Transactions))
	}
}

func TestRunDueScheduledPaymentsExecutesAndIsIdempotent(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-auto-expense", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	scheduledAt := testAppCivilDate("2026-07-01")
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-auto-expense'`, scheduledAt)

	now := time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC)
	count, err := RunDueScheduledPayments(context.Background(), db, now)
	if err != nil {
		t.Fatalf("run due scheduled payments: %v", err)
	}
	if count != 1 {
		t.Fatalf("processed = %d, want 1", count)
	}
	count, err = RunDueScheduledPayments(context.Background(), db, now)
	if err != nil {
		t.Fatalf("rerun due scheduled payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("rerun processed = %d, want 0", count)
	}

	var status string
	var paidAt, executedAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at, scheduled_payment_executed_at FROM transactions WHERE id = 'tx-auto-expense'`).Scan(&status, &paidAt, &executedAt); err != nil {
		t.Fatalf("query tx: %v", err)
	}
	if status != models.PaymentStatusPaid {
		t.Fatalf("status = %q, want paid", status)
	}
	if !paidAt.Valid || paidAt.Int64 != scheduledAt {
		t.Fatalf("paid_at = %v/%d, want %d", paidAt.Valid, paidAt.Int64, scheduledAt)
	}
	if !executedAt.Valid {
		t.Fatal("scheduled_payment_executed_at is NULL")
	}

	var historyCount, notificationCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-auto-expense' AND source = 'automatic'`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("history count = %d, want 1", historyCount)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM user_notifications WHERE workspace_id = 'ws-test' AND user_id = 'user-test' AND type = ?`, scheduledPaymentNotificationType).Scan(&notificationCount); err != nil {
		t.Fatalf("count notification: %v", err)
	}
	if notificationCount != 1 {
		t.Fatalf("notification count = %d, want 1", notificationCount)
	}
}

func TestRunDueScheduledPaymentsSkipsFuturePaidAndOtherWorkspace(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-auto-future", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	insertScheduledPaymentTestTransaction(t, db, "tx-auto-paid", models.TransactionTypeExpense, "checking-test", "", "paid", ptrInt64(testAppCivilDate("2026-07-01")))
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ? WHERE id = 'tx-auto-future'`, testAppCivilDate("2026-07-20"))
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ? WHERE id = 'tx-auto-paid'`, testAppCivilDate("2026-07-01"))

	count, err := RunDueScheduledPayments(context.Background(), db, time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("run due scheduled payments: %v", err)
	}
	if count != 0 {
		t.Fatalf("processed = %d, want 0", count)
	}
}

func TestScheduledPaymentTimezoneUsesConfiguredCivilDay(t *testing.T) {
	t.Setenv("TZ", "UTC")
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-tz-schedule", models.TransactionTypeExpense, "checking-test", "", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-tz-schedule/status-pagamento", url.Values{
		"payment_action":         {"scheduled"},
		"scheduled_payment_date": {"2026-07-10"},
	}), "tx-tz-schedule")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	wantScheduledAt := testAppCivilDate("2026-07-10")
	var scheduledAt sql.NullInt64
	if err := db.QueryRow(`SELECT scheduled_payment_at FROM transactions WHERE id = 'tx-tz-schedule'`).Scan(&scheduledAt); err != nil {
		t.Fatalf("query scheduled_at: %v", err)
	}
	if !scheduledAt.Valid || scheduledAt.Int64 != wantScheduledAt {
		t.Fatalf("scheduled_payment_at = %v/%d, want %d", scheduledAt.Valid, scheduledAt.Int64, wantScheduledAt)
	}

	beforeLocalMidnight := time.Date(2026, 7, 10, 2, 59, 59, 0, time.UTC)
	count, err := RunDueScheduledPayments(context.Background(), db, beforeLocalMidnight)
	if err != nil {
		t.Fatalf("run before local midnight: %v", err)
	}
	if count != 0 {
		t.Fatalf("before local midnight processed = %d, want 0", count)
	}

	atLocalMidnight := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	count, err = RunDueScheduledPayments(context.Background(), db, atLocalMidnight)
	if err != nil {
		t.Fatalf("run at local midnight: %v", err)
	}
	if count != 1 {
		t.Fatalf("at local midnight processed = %d, want 1", count)
	}

	var paidAt, executedAt sql.NullInt64
	if err := db.QueryRow(`SELECT paid_at, scheduled_payment_executed_at FROM transactions WHERE id = 'tx-tz-schedule'`).Scan(&paidAt, &executedAt); err != nil {
		t.Fatalf("query payment fields: %v", err)
	}
	if !paidAt.Valid || paidAt.Int64 != wantScheduledAt {
		t.Fatalf("paid_at = %v/%d, want scheduled date %d", paidAt.Valid, paidAt.Int64, wantScheduledAt)
	}
	if !executedAt.Valid || executedAt.Int64 != atLocalMidnight.Unix() {
		t.Fatalf("executed_at = %v/%d, want worker now %d", executedAt.Valid, executedAt.Int64, atLocalMidnight.Unix())
	}
}

func TestAppTimezonePriorityAndFallback(t *testing.T) {
	t.Setenv("TZ", "UTC")
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	if got, want := testAppCivilDate("2026-07-10"), time.Date(2026, 7, 10, 0, 0, 0, 0, mustLocation(t, "America/Sao_Paulo")).Unix(); got != want {
		t.Fatalf("CONTABASE_TIMEZONE priority got %d, want %d", got, want)
	}

	t.Setenv("CONTABASE_TIMEZONE", "")
	if got, want := testAppCivilDate("2026-07-10"), time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC).Unix(); got != want {
		t.Fatalf("TZ fallback got %d, want %d", got, want)
	}

	t.Setenv("CONTABASE_TIMEZONE", "Invalid/Zone")
	t.Setenv("TZ", "Invalid/Zone")
	if got, want := testAppCivilDate("2026-07-10"), time.Date(2026, 7, 10, 0, 0, 0, 0, mustLocation(t, "America/Sao_Paulo")).Unix(); got != want {
		t.Fatalf("default fallback got %d, want %d", got, want)
	}
}

func insertScheduledPaymentTestTransaction(t *testing.T, db *sql.DB, id, trType, accountID, destinationID, status string, paidAt *int64) {
	t.Helper()
	now := time.Now().Unix()
	txDate := testUnixDate("2026-07-10")
	dueDate := testUnixDate("2026-07-20")
	var dest interface{}
	if destinationID != "" {
		dest = destinationID
	}
	if destinationID == "checking-extra" {
		execTestSQL(t, db, `
			INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
			VALUES ('checking-extra', 'ws-test', 'Conta Extra', 'CHECKING', 0, 0, ?, ?)
		`, now, now)
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, due_date,
			description, status, paid_at, installment_number, total_installments, created_at, updated_at
		)
		VALUES (?, 'ws-test', 'user-test', ?, ?, ?, 12345, ?, ?, ?, ?, ?, 1, 1, ?, ?)
	`, id, accountID, dest, trType, txDate, dueDate, id, status, paidAt, now, now)
}

func ptrInt64(v int64) *int64 {
	return &v
}

func testAppCivilDate(date string) int64 {
	v, err := parseCivilDateStartUnix(date)
	if err != nil {
		panic(err)
	}
	return v
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}
