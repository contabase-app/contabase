package handlers

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

func civilWriteDateFromLegacyUnix(unix int64) CivilWriteDate {
	return MustCivilWriteDate(civildate.LegacyUnixToCivilDateUTC(unix).String())
}

func TestCivilDualWritePaymentWritesPair(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	insertPaymentDateTestTransaction(t, db, "tx-dual-pay", "pending", nil)

	rr := httptest.NewRecorder()
	h.HandleTogglePagamento(rr, formPost("/transacoes/tx-dual-pay/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}), "tx-dual-pay")
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}

	var status string
	var paidAt sql.NullInt64
	var paidCivil, schedCivil sql.NullString
	var schedAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at, paid_date_civil, scheduled_payment_at, scheduled_payment_date_civil FROM transactions WHERE id = 'tx-dual-pay'`).Scan(&status, &paidAt, &paidCivil, &schedAt, &schedCivil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q", status)
	}
	if !paidCivil.Valid || paidCivil.String != "2026-07-14" {
		t.Fatalf("paid_date_civil = %q, want 2026-07-14", paidCivil.String)
	}
	wantPaid := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC).Unix()
	if !paidAt.Valid || paidAt.Int64 != wantPaid {
		t.Fatalf("paid_at = %v/%d, want %d", paidAt.Valid, paidAt.Int64, wantPaid)
	}
	if schedAt.Valid || schedCivil.Valid {
		t.Fatalf("schedule not cleared: at=%v civil=%v", schedAt.Valid, schedCivil.Valid)
	}
}

func TestCivilDualWriteDuplicatePaymentNoop(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	insertPaymentDateTestTransaction(t, db, "tx-dual-noop", "pending", nil)

	first := httptest.NewRecorder()
	h.HandleTogglePagamento(first, formPost("/transacoes/tx-dual-noop/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}), "tx-dual-noop")
	if first.Code != 200 {
		t.Fatalf("first payment status = %d", first.Code)
	}
	var balanceAfterFirst int64
	var historyAfterFirst int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfterFirst); err != nil {
		t.Fatalf("balance after first payment: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-dual-noop'`).Scan(&historyAfterFirst); err != nil {
		t.Fatalf("history after first payment: %v", err)
	}

	second := httptest.NewRecorder()
	h.HandleTogglePagamento(second, formPost("/transacoes/tx-dual-noop/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-20"},
	}), "tx-dual-noop")
	if second.Code != 200 {
		t.Fatalf("duplicate payment status = %d", second.Code)
	}

	var paidAt sql.NullInt64
	var paidCivil sql.NullString
	var accountID string
	if err := db.QueryRow(`SELECT paid_at, paid_date_civil, account_id FROM transactions WHERE id = 'tx-dual-noop'`).Scan(&paidAt, &paidCivil, &accountID); err != nil {
		t.Fatalf("query: %v", err)
	}
	wantPaid := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC).Unix()
	if !paidAt.Valid || paidAt.Int64 != wantPaid {
		t.Fatalf("paid_at overwritten: %v/%d, want %d", paidAt.Valid, paidAt.Int64, wantPaid)
	}
	if !paidCivil.Valid || paidCivil.String != "2026-07-14" {
		t.Fatalf("paid_date_civil overwritten: %q, want 2026-07-14", paidCivil.String)
	}
	if accountID != "checking-test" {
		t.Fatalf("account_id = %q, want checking-test", accountID)
	}
	var balanceAfterSecond, historyAfterSecond int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfterSecond); err != nil {
		t.Fatalf("balance after duplicate payment: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-dual-noop'`).Scan(&historyAfterSecond); err != nil {
		t.Fatalf("history after duplicate payment: %v", err)
	}
	if balanceAfterSecond != balanceAfterFirst || historyAfterSecond != historyAfterFirst || historyAfterSecond != 1 {
		t.Fatalf("duplicate payment mutated balance/history: balance %d -> %d, history %d -> %d", balanceAfterFirst, balanceAfterSecond, historyAfterFirst, historyAfterSecond)
	}
}

func TestCivilDualWritePendingClearsPaymentPair(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	insertPaymentDateTestTransaction(t, db, "tx-dual-pending", "pending", nil)
	paidResponse := httptest.NewRecorder()
	h.HandleTogglePagamento(paidResponse, formPost("/transacoes/tx-dual-pending/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}), "tx-dual-pending")
	if paidResponse.Code != 200 {
		t.Fatalf("paid status = %d", paidResponse.Code)
	}

	firstPending := httptest.NewRecorder()
	h.HandleTogglePagamento(firstPending, formPost("/transacoes/tx-dual-pending/status-pagamento", url.Values{
		"payment_action": {"pending"},
	}), "tx-dual-pending")
	if firstPending.Code != 200 {
		t.Fatalf("first pending status = %d", firstPending.Code)
	}
	var balanceAfterFirstPending, historyAfterFirstPending int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfterFirstPending); err != nil {
		t.Fatalf("balance after first pending: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-dual-pending'`).Scan(&historyAfterFirstPending); err != nil {
		t.Fatalf("history after first pending: %v", err)
	}

	secondPending := httptest.NewRecorder()
	h.HandleTogglePagamento(secondPending, formPost("/transacoes/tx-dual-pending/status-pagamento", url.Values{
		"payment_action": {"pending"},
	}), "tx-dual-pending")
	if secondPending.Code != 200 {
		t.Fatalf("second pending status = %d", secondPending.Code)
	}

	var paidAt sql.NullInt64
	var paidCivil sql.NullString
	if err := db.QueryRow(`SELECT paid_at, paid_date_civil FROM transactions WHERE id = 'tx-dual-pending'`).Scan(&paidAt, &paidCivil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if paidAt.Valid {
		t.Fatalf("paid_at = %d, want NULL after pending", paidAt.Int64)
	}
	if paidCivil.Valid {
		t.Fatalf("paid_date_civil = %q, want NULL after pending", paidCivil.String)
	}
	var balanceAfterSecondPending, historyAfterSecondPending int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfterSecondPending); err != nil {
		t.Fatalf("balance after second pending: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-dual-pending'`).Scan(&historyAfterSecondPending); err != nil {
		t.Fatalf("history after second pending: %v", err)
	}
	if balanceAfterFirstPending != 100000 || balanceAfterSecondPending != balanceAfterFirstPending {
		t.Fatalf("pending repeated balance = %d -> %d, want one reversal to 100000", balanceAfterFirstPending, balanceAfterSecondPending)
	}
	if historyAfterFirstPending != 2 || historyAfterSecondPending != historyAfterFirstPending {
		t.Fatalf("pending repeated history = %d -> %d, want 2", historyAfterFirstPending, historyAfterSecondPending)
	}
}

func TestCivilDualWriteScheduleRequiresDate(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	insertPaymentDateTestTransaction(t, db, "tx-sched-no-date", "pending", nil)

	rr := httptest.NewRecorder()
	h.HandleTogglePagamento(rr, formPost("/transacoes/tx-sched-no-date/status-pagamento", url.Values{
		"payment_action": {"scheduled"},
	}), "tx-sched-no-date")
	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400 for missing date", rr.Code)
	}
}

func TestCivilDualWriteScheduleWritesPair(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	insertPaymentDateTestTransaction(t, db, "tx-sched-set", "pending", nil)

	rr := httptest.NewRecorder()
	h.HandleTogglePagamento(rr, formPost("/transacoes/tx-sched-set/status-pagamento", url.Values{
		"payment_action":         {"scheduled"},
		"scheduled_payment_date": {"2026-08-01"},
	}), "tx-sched-set")
	if rr.Code != 200 {
		t.Fatalf("status = %d", rr.Code)
	}

	var schedAt sql.NullInt64
	var schedCivil sql.NullString
	if err := db.QueryRow(`SELECT scheduled_payment_at, scheduled_payment_date_civil FROM transactions WHERE id = 'tx-sched-set'`).Scan(&schedAt, &schedCivil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !schedCivil.Valid || schedCivil.String != "2026-08-01" {
		t.Fatalf("scheduled_payment_date_civil = %q, want 2026-08-01", schedCivil.String)
	}
	want := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	if !schedAt.Valid || schedAt.Int64 != want {
		t.Fatalf("scheduled_payment_at = %v/%d, want %d", schedAt.Valid, schedAt.Int64, want)
	}
}

func TestCivilDualWriteClearScheduleClearsPair(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	insertPaymentDateTestTransaction(t, db, "tx-clear-sched", "pending", nil)

	h.HandleTogglePagamento(httptest.NewRecorder(), formPost("/transacoes/tx-clear-sched/status-pagamento", url.Values{
		"payment_action":         {"scheduled"},
		"scheduled_payment_date": {"2026-08-01"},
	}), "tx-clear-sched")
	h.HandleTogglePagamento(httptest.NewRecorder(), formPost("/transacoes/tx-clear-sched/status-pagamento", url.Values{
		"payment_action": {"clear_schedule"},
	}), "tx-clear-sched")

	var schedAt sql.NullInt64
	var schedCivil sql.NullString
	if err := db.QueryRow(`SELECT scheduled_payment_at, scheduled_payment_date_civil FROM transactions WHERE id = 'tx-clear-sched'`).Scan(&schedAt, &schedCivil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if schedAt.Valid {
		t.Fatalf("scheduled_payment_at = %d, want NULL", schedAt.Int64)
	}
	if schedCivil.Valid {
		t.Fatalf("scheduled_payment_date_civil = %q, want NULL", schedCivil.String)
	}
}

func TestCivilDualWriteBulkRollbackOnLastID(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, h := setupDualWriteTest(t)

	ids := []string{"tx-bulk-rb-1", "tx-bulk-rb-2", "tx-bulk-rb-3"}
	for _, id := range ids {
		insertPaymentDateTestTransaction(t, db, id, "pending", nil)
		execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_date_civil = '2026-07-18', scheduled_payment_executed_at = ? WHERE id = ?`, time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC).Unix(), int64(123456789), id)
	}
	var initialBalance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&initialBalance); err != nil {
		t.Fatalf("initial balance: %v", err)
	}

	execTestSQL(t, db, `CREATE TEMP TRIGGER dualwrite_bulk_rollback BEFORE UPDATE OF status ON transactions WHEN NEW.id = 'tx-bulk-rb-3' BEGIN SELECT RAISE(ABORT, 'injected rollback'); END`)

	params := strings.Join([]string{"ids=" + ids[0], "ids=" + ids[1], "ids=" + ids[2], "status_pagamento=paid", "payment_date=2026-07-14"}, "&")
	req := formPost("/transactions/bulk", url.Values{})
	req.Body = io.NopCloser(strings.NewReader(params))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	h.HandleBulkUpdate(rr, req)
	if rr.Code == 200 {
		t.Fatal("bulk succeeded despite injected rollback")
	}

	for _, id := range ids {
		var status string
		var paidAt, scheduledAt, executedAt sql.NullInt64
		var paidCivil, scheduledCivil sql.NullString
		var accountID string
		if err := db.QueryRow(`SELECT status, paid_at, paid_date_civil, scheduled_payment_at, scheduled_payment_date_civil, scheduled_payment_executed_at, account_id FROM transactions WHERE id = ?`, id).Scan(&status, &paidAt, &paidCivil, &scheduledAt, &scheduledCivil, &executedAt, &accountID); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if status != "pending" {
			t.Fatalf("%s status = %q", id, status)
		}
		if paidAt.Valid {
			t.Fatalf("%s paid_at = %d", id, paidAt.Int64)
		}
		if paidCivil.Valid {
			t.Fatalf("%s paid_date_civil = %q", id, paidCivil.String)
		}
		if !scheduledAt.Valid || scheduledAt.Int64 != time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC).Unix() || !scheduledCivil.Valid || scheduledCivil.String != "2026-07-18" || !executedAt.Valid || executedAt.Int64 != 123456789 {
			t.Fatalf("%s schedule changed after rollback: at=%v/%d civil=%v/%q executed=%v/%d", id, scheduledAt.Valid, scheduledAt.Int64, scheduledCivil.Valid, scheduledCivil.String, executedAt.Valid, executedAt.Int64)
		}
		if accountID != "checking-test" {
			t.Fatalf("%s account_id = %q", id, accountID)
		}
	}
	var finalBalance, historyCount int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&finalBalance); err != nil {
		t.Fatalf("final balance: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id IN (?, ?, ?)`, ids[0], ids[1], ids[2]).Scan(&historyCount); err != nil {
		t.Fatalf("history after rollback: %v", err)
	}
	if finalBalance != initialBalance || historyCount != 0 {
		t.Fatalf("bulk rollback mutated balance/history: balance %d -> %d, history=%d", initialBalance, finalBalance, historyCount)
	}
}

func TestCivilDualWriteScheduleNewExecutesAndPreservesSource(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertScheduledPaymentTestTransaction(t, db, "tx-sched-new", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	schedDate := testAppCivilDate("2026-07-12")
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_date_civil = '2026-07-12', scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-sched-new'`, schedDate)

	execNow := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	processed, err := RunDueScheduledPayments(context.Background(), db, execNow)
	if err != nil {
		t.Fatalf("run scheduler: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	var status string
	var paidAt sql.NullInt64
	var paidCivil, schedCivil sql.NullString
	var schedAt, executedAt sql.NullInt64
	var txDate, dueDate int64
	if err := db.QueryRow(`SELECT status, paid_at, paid_date_civil, scheduled_payment_at, scheduled_payment_date_civil, scheduled_payment_executed_at, date, due_date FROM transactions WHERE id = 'tx-sched-new'`).Scan(&status, &paidAt, &paidCivil, &schedAt, &schedCivil, &executedAt, &txDate, &dueDate); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q", status)
	}
	if !paidCivil.Valid || paidCivil.String != "2026-07-12" {
		t.Fatalf("paid_date_civil = %q", paidCivil.String)
	}
	wantPaid := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	if !paidAt.Valid || paidAt.Int64 != wantPaid {
		t.Fatalf("paid_at = %v/%d, want %d", paidAt.Valid, paidAt.Int64, wantPaid)
	}
	if !schedAt.Valid || schedAt.Int64 != schedDate {
		t.Fatalf("scheduled_payment_at = %v/%d, want preserved %d", schedAt.Valid, schedAt.Int64, schedDate)
	}
	if !schedCivil.Valid || schedCivil.String != "2026-07-12" {
		t.Fatalf("scheduled_payment_date_civil = %q, want preserved 2026-07-12", schedCivil.String)
	}
	if !executedAt.Valid || executedAt.Int64 != execNow.Unix() {
		t.Fatalf("scheduled_payment_executed_at = %v/%d, want %d", executedAt.Valid, executedAt.Int64, execNow.Unix())
	}
	if txDate != testUnixDate("2026-07-10") || dueDate != testUnixDate("2026-07-20") {
		t.Fatalf("scheduler changed source dates: date=%d due_date=%d", txDate, dueDate)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 87655 {
		t.Fatalf("balance = %d, want 87655", balance)
	}

	var historyNewPaidAt sql.NullInt64
	var historySource string
	if err := db.QueryRow(`SELECT new_paid_at, source FROM transaction_status_history WHERE transaction_id = 'tx-sched-new'`).Scan(&historyNewPaidAt, &historySource); err != nil {
		t.Fatalf("history query: %v", err)
	}
	if !historyNewPaidAt.Valid || historyNewPaidAt.Int64 != paidAt.Int64 || historySource != "automatic" {
		t.Fatalf("history = paid_at %v/%d source=%q, want %d/automatic", historyNewPaidAt.Valid, historyNewPaidAt.Int64, historySource, paidAt.Int64)
	}
}

func TestCivilDualWriteScheduleLegacyConvertsAndPreservesSource(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertScheduledPaymentTestTransaction(t, db, "tx-sched-legacy", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	legacySchedAt := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-sched-legacy'`, legacySchedAt)

	execNow := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	processed, err := RunDueScheduledPayments(context.Background(), db, execNow)
	if err != nil {
		t.Fatalf("run scheduler: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	var status string
	var paidAt sql.NullInt64
	var paidCivil, schedCivil sql.NullString
	var schedAt, executedAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at, paid_date_civil, scheduled_payment_at, scheduled_payment_date_civil, scheduled_payment_executed_at FROM transactions WHERE id = 'tx-sched-legacy'`).Scan(&status, &paidAt, &paidCivil, &schedAt, &schedCivil, &executedAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q", status)
	}
	// Legacy 03:00 UTC July 12 = midnight SP July 12 → civil date = 2026-07-12
	if !paidCivil.Valid || paidCivil.String != "2026-07-12" {
		t.Fatalf("paid_date_civil = %q, want 2026-07-12", paidCivil.String)
	}
	wantPaid := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	if !paidAt.Valid || paidAt.Int64 != wantPaid {
		t.Fatalf("paid_at = %v/%d, want %d (noon UTC)", paidAt.Valid, paidAt.Int64, wantPaid)
	}
	// Source preserved.
	if !schedAt.Valid || schedAt.Int64 != legacySchedAt {
		t.Fatalf("scheduled_payment_at = %v/%d, want preserved %d", schedAt.Valid, schedAt.Int64, legacySchedAt)
	}
	if schedCivil.Valid {
		t.Fatalf("scheduled_payment_date_civil = %q, want NULL (legacy)", schedCivil.String)
	}
	if !executedAt.Valid || executedAt.Int64 != execNow.Unix() {
		t.Fatalf("scheduled_payment_executed_at = %v/%d, want %d", executedAt.Valid, executedAt.Int64, execNow.Unix())
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 87655 {
		t.Fatalf("balance = %d, want 87655", balance)
	}

	var historyNewPaidAt sql.NullInt64
	var historySource string
	if err := db.QueryRow(`SELECT new_paid_at, source FROM transaction_status_history WHERE transaction_id = 'tx-sched-legacy'`).Scan(&historyNewPaidAt, &historySource); err != nil {
		t.Fatalf("history query: %v", err)
	}
	if !historyNewPaidAt.Valid || historyNewPaidAt.Int64 != paidAt.Int64 || historySource != "automatic" {
		t.Fatalf("history = paid_at %v/%d source=%q, want %d/automatic", historyNewPaidAt.Valid, historyNewPaidAt.Int64, historySource, paidAt.Int64)
	}
}

func TestCivilDualWriteScheduleIdempotentDoubleExecution(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertScheduledPaymentTestTransaction(t, db, "tx-sched-idem", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	schedDate := testAppCivilDate("2026-07-12")
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_date_civil = '2026-07-12', scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-sched-idem'`, schedDate)

	execNow := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)

	processed, err := RunDueScheduledPayments(context.Background(), db, execNow)
	if err != nil {
		t.Fatalf("first scheduler run: %v", err)
	}
	if processed != 1 {
		t.Fatalf("first processed = %d, want 1", processed)
	}

	var firstPaidAt sql.NullInt64
	var firstPaidCivil sql.NullString
	var firstExecutedAt sql.NullInt64
	var firstScheduledAt sql.NullInt64
	var firstScheduledCivil sql.NullString
	if err := db.QueryRow(`SELECT paid_at, paid_date_civil, scheduled_payment_executed_at, scheduled_payment_at, scheduled_payment_date_civil FROM transactions WHERE id = 'tx-sched-idem'`).Scan(&firstPaidAt, &firstPaidCivil, &firstExecutedAt, &firstScheduledAt, &firstScheduledCivil); err != nil {
		t.Fatalf("first state query: %v", err)
	}
	var historyCount int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-sched-idem'`).Scan(&historyCount); err != nil {
		t.Fatalf("first history query: %v", err)
	}
	var firstBalance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&firstBalance); err != nil {
		t.Fatalf("first balance query: %v", err)
	}

	processed, err = RunDueScheduledPayments(context.Background(), db, execNow)
	if err != nil {
		t.Fatalf("second scheduler run: %v", err)
	}
	if processed != 0 {
		t.Fatalf("second processed = %d, want 0", processed)
	}

	var paidAt sql.NullInt64
	var paidCivil sql.NullString
	var executedAt sql.NullInt64
	var scheduledAt sql.NullInt64
	var scheduledCivil sql.NullString
	if err := db.QueryRow(`SELECT paid_at, paid_date_civil, scheduled_payment_executed_at, scheduled_payment_at, scheduled_payment_date_civil FROM transactions WHERE id = 'tx-sched-idem'`).Scan(&paidAt, &paidCivil, &executedAt, &scheduledAt, &scheduledCivil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if paidAt.Int64 != firstPaidAt.Int64 {
		t.Fatalf("paid_at changed: %d → %d", firstPaidAt.Int64, paidAt.Int64)
	}
	if paidCivil.String != firstPaidCivil.String {
		t.Fatalf("paid_date_civil changed: %q → %q", firstPaidCivil.String, paidCivil.String)
	}
	if executedAt.Int64 != firstExecutedAt.Int64 {
		t.Fatalf("executed_at changed: %d → %d", firstExecutedAt.Int64, executedAt.Int64)
	}
	if scheduledAt != firstScheduledAt || scheduledCivil != firstScheduledCivil {
		t.Fatalf("schedule changed: %v/%d %v/%q -> %v/%d %v/%q", firstScheduledAt.Valid, firstScheduledAt.Int64, firstScheduledCivil.Valid, firstScheduledCivil.String, scheduledAt.Valid, scheduledAt.Int64, scheduledCivil.Valid, scheduledCivil.String)
	}
	var historyCountAfter int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-sched-idem'`).Scan(&historyCountAfter); err != nil {
		t.Fatalf("second history query: %v", err)
	}
	if historyCountAfter != historyCount {
		t.Fatalf("history count changed: %d → %d", historyCount, historyCountAfter)
	}
	var secondBalance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&secondBalance); err != nil {
		t.Fatalf("second balance query: %v", err)
	}
	if secondBalance != firstBalance || historyCountAfter != 1 {
		t.Fatalf("second run mutated balance/history: balance %d -> %d, history=%d", firstBalance, secondBalance, historyCountAfter)
	}
}

func TestCivilDualWriteMidnightFrontier(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	// New schedule with noon-UTC timestamp.
	insertScheduledPaymentTestTransaction(t, db, "tx-front-new", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	noonUTC := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_date_civil = '2026-07-12', scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-front-new'`, noonUTC)

	// Legacy schedule with 03:00 UTC (midnight SP).
	insertScheduledPaymentTestTransaction(t, db, "tx-front-legacy", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	legacy0300 := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-front-legacy'`, legacy0300)

	// Before midnight SP: 02:59:59 UTC July 12.
	beforeMidnight := time.Date(2026, 7, 12, 2, 59, 59, 0, time.UTC)
	count, err := RunDueScheduledPayments(context.Background(), db, beforeMidnight)
	if err != nil {
		t.Fatalf("run before midnight: %v", err)
	}
	if count != 0 {
		t.Fatalf("before midnight processed = %d, want 0", count)
	}

	// At midnight SP: 03:00:00 UTC July 12.
	atMidnight := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	count, err = RunDueScheduledPayments(context.Background(), db, atMidnight)
	if err != nil {
		t.Fatalf("run at midnight: %v", err)
	}
	if count != 2 {
		t.Fatalf("at midnight processed = %d, want 2 (both new and legacy eligible)", count)
	}

	// Both must be paid with civil dates.
	for _, id := range []string{"tx-front-new", "tx-front-legacy"} {
		var status, paidCivil sql.NullString
		if err := db.QueryRow(`SELECT status, paid_date_civil FROM transactions WHERE id = ?`, id).Scan(&status, &paidCivil); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if !status.Valid || status.String != "paid" {
			t.Fatalf("%s status = %q", id, status.String)
		}
		if !paidCivil.Valid || paidCivil.String != "2026-07-12" {
			t.Fatalf("%s paid_date_civil = %q", id, paidCivil.String)
		}
	}
}

func TestCivilDualWriteCreatePersonalBusinessAndNoDue(t *testing.T) {
	for _, tc := range []struct {
		name        string
		business    bool
		dueInput    string
		wantDue     string
		description string
	}{
		{name: "personal", dueInput: "2026-07-20", description: "Civil create personal"},
		{name: "business", business: true, dueInput: "2026-07-20", wantDue: "2026-07-20", description: "Civil create business"},
		{name: "business without due", business: true, description: "Civil create business no due"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, h := setupDualWriteTest(t)
			seedRecurringExpenseCategory(t, db)
			if tc.business {
				seedBusinessWorkspace(t, db)
			}
			fields := map[string]string{
				"tipo":             "despesa",
				"valor":            "100,00",
				"descricao":        tc.description,
				"data":             "2026-07-12",
				"origem_conta_id":  "checking-test",
				"categoria_id":     "cat-expense-rec",
				"status_pagamento": "pending",
			}
			if tc.dueInput != "" {
				fields["due_date"] = tc.dueInput
			}
			rr := httptest.NewRecorder()
			h.HandleSalvarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes", fields))
			if rr.Code != 200 {
				t.Fatalf("create status = %d body=%q", rr.Code, rr.Body.String())
			}

			var date int64
			var dateCivil string
			var dueDate sql.NullInt64
			var dueCivil sql.NullString
			if err := db.QueryRow(`SELECT date, date_civil, due_date, due_date_civil FROM transactions WHERE description = ?`, tc.description).Scan(&date, &dateCivil, &dueDate, &dueCivil); err != nil {
				t.Fatalf("query: %v; response=%q", err, rr.Body.String())
			}
			assertCivilPair(t, "date", date, dateCivil, "2026-07-12")
			if tc.wantDue == "" {
				if dueDate.Valid || dueCivil.Valid {
					t.Fatalf("due pair = %v/%d %v/%q, want NULL/NULL", dueDate.Valid, dueDate.Int64, dueCivil.Valid, dueCivil.String)
				}
			} else {
				if !dueDate.Valid || !dueCivil.Valid {
					t.Fatalf("due pair is incomplete: %v/%d %v/%q", dueDate.Valid, dueDate.Int64, dueCivil.Valid, dueCivil.String)
				}
				assertCivilPair(t, "due_date", dueDate.Int64, dueCivil.String, tc.wantDue)
			}
		})
	}
}

func TestCivilDualWriteCreateInstallmentsUseIndependentCivilMonthArithmetic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start string
		want  []string
	}{
		{name: "normal February", start: "2026-01-31", want: []string{"2026-01-31", "2026-02-28", "2026-03-31"}},
		{name: "leap February", start: "2024-01-31", want: []string{"2024-01-31", "2024-02-29", "2024-03-31"}},
		{name: "thirty day month", start: "2026-03-31", want: []string{"2026-03-31", "2026-04-30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, h := setupDualWriteTest(t)
			seedRecurringExpenseCategory(t, db)
			seedBusinessWorkspace(t, db)
			description := "Civil installments " + tc.name
			if _, err := h.insertTransaction("EXPENSE", int64(len(tc.want))*10000, description, "", "", MustCivilWriteDate(tc.start), "checking-test", "", "cat-expense-rec", int64(len(tc.want)), "pending", false, "", "", MustOptionalCivilWriteDate(tc.start), nil, "", false); err != nil {
				t.Fatalf("insert installments: %v", err)
			}

			rows, err := db.Query(`SELECT date, date_civil, due_date, due_date_civil FROM transactions WHERE description = ? ORDER BY installment_number`, description)
			if err != nil {
				t.Fatalf("query installments: %v", err)
			}
			defer rows.Close()
			index := 0
			for rows.Next() {
				var date, dueDate int64
				var dateCivil, dueCivil string
				if err := rows.Scan(&date, &dateCivil, &dueDate, &dueCivil); err != nil {
					t.Fatalf("scan installment %d: %v", index+1, err)
				}
				if index >= len(tc.want) {
					t.Fatalf("unexpected installment %d", index+1)
				}
				assertCivilPair(t, "date", date, dateCivil, tc.want[index])
				assertCivilPair(t, "due_date", dueDate, dueCivil, tc.want[index])
				index++
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate installments: %v", err)
			}
			if index != len(tc.want) {
				t.Fatalf("installments = %d, want %d", index, len(tc.want))
			}
		})
	}
}

func TestCivilDualWriteCreateInstallmentFailureRollsBackAllPairs(t *testing.T) {
	db, h := setupDualWriteTest(t)
	seedRecurringExpenseCategory(t, db)
	seedBusinessWorkspace(t, db)
	execTestSQL(t, db, `CREATE TEMP TRIGGER civil_create_rollback BEFORE INSERT ON transactions WHEN NEW.description = 'Civil create rollback' AND NEW.installment_number = 3 BEGIN SELECT RAISE(ABORT, 'injected create rollback'); END`)
	var balanceBefore int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceBefore); err != nil {
		t.Fatalf("balance before: %v", err)
	}
	if _, err := h.insertTransaction("EXPENSE", 30000, "Civil create rollback", "", "", MustCivilWriteDate("2026-01-31"), "checking-test", "", "cat-expense-rec", 3, "paid", false, "", "", MustOptionalCivilWriteDate("2026-01-31"), nil, "", false); err == nil {
		t.Fatal("insert succeeded despite injected third-installment failure")
	}
	var count, balanceAfter int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Civil create rollback'`).Scan(&count); err != nil {
		t.Fatalf("count after rollback: %v", err)
	}
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfter); err != nil {
		t.Fatalf("balance after: %v", err)
	}
	if count != 0 || balanceAfter != balanceBefore {
		t.Fatalf("creation rollback incomplete: rows=%d balance %d -> %d", count, balanceBefore, balanceAfter)
	}
}

func TestCivilDualWriteEditRejectsInvalidPersistedCivilBeforeWrite(t *testing.T) {
	for _, field := range []string{"date_civil", "due_date_civil"} {
		t.Run(field, func(t *testing.T) {
			db, h, id := setupCivilEditTransaction(t, false)
			execTestSQL(t, db, `UPDATE transactions SET `+field+` = '2026-02-30' WHERE id = ?`, id)

			var beforeDescription string
			var beforeDate int64
			var beforeDateCivil sql.NullString
			var beforeDue sql.NullInt64
			var beforeDueCivil sql.NullString
			var beforeUpdated int64
			if err := db.QueryRow(`SELECT description, date, date_civil, due_date, due_date_civil, updated_at FROM transactions WHERE id = ?`, id).Scan(&beforeDescription, &beforeDate, &beforeDateCivil, &beforeDue, &beforeDueCivil, &beforeUpdated); err != nil {
				t.Fatalf("before query: %v", err)
			}

			rr := httptest.NewRecorder()
			h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Changed despite invalid civil", stringPtr("2026-07-25"), "single"), id)
			if rr.Code != 409 {
				t.Fatalf("status = %d, want 409", rr.Code)
			}

			var afterDescription string
			var afterDate int64
			var afterDateCivil sql.NullString
			var afterDue sql.NullInt64
			var afterDueCivil sql.NullString
			var afterUpdated int64
			if err := db.QueryRow(`SELECT description, date, date_civil, due_date, due_date_civil, updated_at FROM transactions WHERE id = ?`, id).Scan(&afterDescription, &afterDate, &afterDateCivil, &afterDue, &afterDueCivil, &afterUpdated); err != nil {
				t.Fatalf("after query: %v", err)
			}
			if afterDescription != beforeDescription || afterDate != beforeDate || afterDateCivil != beforeDateCivil || afterDue != beforeDue || afterDueCivil != beforeDueCivil || afterUpdated != beforeUpdated {
				t.Fatalf("invalid persisted civil allowed a partial write")
			}
		})
	}
}

func TestCivilDualWriteEditDueDateSetClearPreserveInvalid(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		db, h, id := setupCivilEditTransaction(t, false)
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Civil edit", stringPtr("2026-07-25"), "single"), id)
		if rr.Code != 200 {
			t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
		}
		assertStoredDuePair(t, db, id, "2026-07-25")
	})

	t.Run("clear", func(t *testing.T) {
		db, h, id := setupCivilEditTransaction(t, false)
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Civil edit", stringPtr(""), "single"), id)
		if rr.Code != 200 {
			t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
		}
		var due sql.NullInt64
		var civil sql.NullString
		if err := db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE id = ?`, id).Scan(&due, &civil); err != nil {
			t.Fatalf("query: %v", err)
		}
		if due.Valid || civil.Valid {
			t.Fatalf("clear left due pair: %v/%d %v/%q", due.Valid, due.Int64, civil.Valid, civil.String)
		}
	})

	for _, tc := range []struct {
		name      string
		recurring bool
		legacy    bool
	}{
		{name: "non recurring"},
		{name: "recurring", recurring: true},
		{name: "legacy due", legacy: true},
	} {
		t.Run("preserve "+tc.name, func(t *testing.T) {
			db, h, id := setupCivilEditTransaction(t, tc.recurring)
			wantLegacy := MustCivilWriteDate("2026-07-20").LegacyUnix()
			wantCivil := sql.NullString{String: "2026-07-20", Valid: true}
			if tc.legacy {
				wantLegacy = time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC).Unix()
				wantCivil = sql.NullString{}
				execTestSQL(t, db, `UPDATE transactions SET due_date = ?, due_date_civil = NULL WHERE id = ?`, wantLegacy, id)
			}
			rr := httptest.NewRecorder()
			h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Civil edit preserved", nil, "single"), id)
			if rr.Code != 200 {
				t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
			}
			var due sql.NullInt64
			var civil sql.NullString
			if err := db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE id = ?`, id).Scan(&due, &civil); err != nil {
				t.Fatalf("query: %v", err)
			}
			if !due.Valid || due.Int64 != wantLegacy || civil != wantCivil {
				t.Fatalf("preserve changed due pair: %v/%d %v/%q", due.Valid, due.Int64, civil.Valid, civil.String)
			}
		})
	}

	t.Run("invalid input", func(t *testing.T) {
		db, h, id := setupCivilEditTransaction(t, false)
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Civil edit", stringPtr("2026-02-30"), "single"), id)
		if rr.Code != 400 {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
		assertStoredDuePair(t, db, id, "2026-07-20")
	})

	t.Run("same legacy civil day canonicalizes without broad refresh", func(t *testing.T) {
		db, h, id := setupCivilEditTransaction(t, false)
		legacyMidnight := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC).Unix()
		execTestSQL(t, db, `UPDATE transactions SET due_date = ?, due_date_civil = NULL WHERE id = ?`, legacyMidnight, id)
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Civil edit canonical due", stringPtr("2026-07-20"), "single"), id)
		if rr.Code != 200 {
			t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
		}
		if rr.Header().Get("HX-Reswap") == "none" {
			t.Fatalf("same due civil day was classified as broad change")
		}
		assertStoredDuePair(t, db, id, "2026-07-20")
	})
}

func TestCivilDualWriteEditLegacyMidnightSameCivilKeepsRowSwap(t *testing.T) {
	db, h, id := setupCivilEditTransaction(t, false)
	legacyMidnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE transactions SET date = ?, date_civil = NULL WHERE id = ?`, legacyMidnight, id)

	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, civilEditRequest(t, id, "Civil edit midnight", nil, "single"), id)
	if rr.Code != 200 {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("HX-Reswap") == "none" {
		t.Fatalf("same civil day was classified as broad date change")
	}
	var date int64
	var civil string
	if err := db.QueryRow(`SELECT date, date_civil FROM transactions WHERE id = ?`, id).Scan(&date, &civil); err != nil {
		t.Fatalf("query: %v", err)
	}
	assertCivilPair(t, "date", date, civil, "2026-07-12")
}

func TestCivilDualWriteFutureAndAllInstallmentProducer(t *testing.T) {
	for _, scope := range []string{"future", "all"} {
		t.Run(scope, func(t *testing.T) {
			db, h := setupDualWriteTest(t)
			seedRecurringExpenseCategory(t, db)
			seedBusinessWorkspace(t, db)
			description := "Civil producer seven " + scope
			if _, err := h.insertTransaction("EXPENSE", 30003, description, "", "", MustCivilWriteDate("2026-01-31"), "checking-test", "", "cat-expense-rec", 3, "pending", false, "", "", MustOptionalCivilWriteDate("2026-01-31"), nil, "", false); err != nil {
				t.Fatalf("insert: %v", err)
			}
			var rootID string
			if err := db.QueryRow(`SELECT id FROM transactions WHERE description = ? AND installment_number = 1`, description).Scan(&rootID); err != nil {
				t.Fatalf("root query: %v", err)
			}
			midnight := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC).Unix()
			execTestSQL(t, db, `UPDATE transactions SET date = ?, date_civil = NULL, due_date = ?, due_date_civil = NULL WHERE id = ?`, midnight, midnight, rootID)

			rr := httptest.NewRecorder()
			h.HandleAtualizarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes/"+rootID, map[string]string{
				"valor":            "100,01",
				"descricao":        description + " updated",
				"data":             "2026-01-31",
				"due_date":         "2026-01-31",
				"tipo":             "despesa",
				"origem_conta_id":  "checking-test",
				"categoria_id":     "cat-expense-rec",
				"status_pagamento": "pending",
				"escopo":           scope,
			}), rootID)
			if rr.Code != 200 {
				t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
			}

			rows, err := db.Query(`SELECT id, parent_id, installment_number, amount, account_id, date, date_civil, due_date, due_date_civil, series_id, invoice_id FROM transactions WHERE id = ? OR parent_id = ? ORDER BY installment_number`, rootID, rootID)
			if err != nil {
				t.Fatalf("query series: %v", err)
			}
			defer rows.Close()
			want := []string{"2026-01-31", "2026-02-28", "2026-03-31"}
			index := 0
			for rows.Next() {
				var id, accountID, dateCivil, dueCivil string
				var parent, seriesID, invoiceID sql.NullString
				var installment, amount, date, dueDate int64
				if err := rows.Scan(&id, &parent, &installment, &amount, &accountID, &date, &dateCivil, &dueDate, &dueCivil, &seriesID, &invoiceID); err != nil {
					t.Fatalf("scan %d: %v", index+1, err)
				}
				assertCivilPair(t, "date", date, dateCivil, want[index])
				assertCivilPair(t, "due_date", dueDate, dueCivil, want[index])
				if installment != int64(index+1) || amount != 10001 || accountID != "checking-test" || seriesID.Valid || invoiceID.Valid {
					t.Fatalf("installment %d metadata changed: id=%s installment=%d amount=%d account=%s series=%v invoice=%v", index+1, id, installment, amount, accountID, seriesID.Valid, invoiceID.Valid)
				}
				if index == 0 && parent.Valid {
					t.Fatalf("root parent_id = %q, want NULL", parent.String)
				}
				if index > 0 && (!parent.Valid || parent.String != rootID) {
					t.Fatalf("installment %d parent_id = %v/%q, want %q", index+1, parent.Valid, parent.String, rootID)
				}
				index++
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate series: %v", err)
			}
			if index != len(want) {
				t.Fatalf("rows = %d, want %d", index, len(want))
			}
		})
	}
}

func setupCivilEditTransaction(t *testing.T, recurring bool) (*sql.DB, TransactionHandler, string) {
	t.Helper()
	db, h := setupDualWriteTest(t)
	seedRecurringExpenseCategory(t, db)
	seedBusinessWorkspace(t, db)
	description := "Civil edit"
	var occurrences *int64
	if recurring {
		count := int64(2)
		occurrences = &count
		description = "Civil edit recurring"
	}
	if _, err := h.insertTransaction("EXPENSE", 10000, description, "", "", MustCivilWriteDate("2026-07-12"), "checking-test", "", "cat-expense-rec", 1, "pending", recurring, "MONTHLY", "", MustOptionalCivilWriteDate("2026-07-20"), occurrences, "", false); err != nil {
		t.Fatalf("insert edit transaction: %v", err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE description = ? ORDER BY date LIMIT 1`, description).Scan(&id); err != nil {
		t.Fatalf("edit transaction query: %v", err)
	}
	return db, h, id
}

func civilEditRequest(t *testing.T, id, description string, dueDate *string, scope string) *http.Request {
	t.Helper()
	fields := map[string]string{
		"valor":            "100,00",
		"descricao":        description,
		"data":             "2026-07-12",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           scope,
	}
	if dueDate != nil {
		fields["due_date"] = *dueDate
	}
	return newMultipartUpdateRequest(t, "/transacoes/"+id, fields)
}

func assertStoredDuePair(t *testing.T, db *sql.DB, id, want string) {
	t.Helper()
	var dueDate int64
	var dueCivil string
	if err := db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE id = ?`, id).Scan(&dueDate, &dueCivil); err != nil {
		t.Fatalf("query due pair: %v", err)
	}
	assertCivilPair(t, "due_date", dueDate, dueCivil, want)
}

func assertCivilPair(t *testing.T, field string, legacy int64, civil, want string) {
	t.Helper()
	if civil != want {
		t.Fatalf("%s_civil = %q, want %q", field, civil, want)
	}
	wantLegacy := MustCivilWriteDate(want).LegacyUnix()
	if legacy != wantLegacy {
		t.Fatalf("%s = %d, want noon UTC %d for %s", field, legacy, wantLegacy, want)
	}
}

func stringPtr(value string) *string {
	return &value
}

func TestCivilDualWriteBulkRollbackWithSchedules(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	ids := []string{"tx-bulk-sched-1", "tx-bulk-sched-2", "tx-bulk-sched-3"}
	for _, id := range ids {
		insertPaymentDateTestTransaction(t, db, id, "pending", nil)
		execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_date_civil = '2026-07-20', scheduled_payment_created_by = 'user-test' WHERE id = ?`, testAppCivilDate("2026-07-20"), id)
	}

	execTestSQL(t, db, `CREATE TEMP TRIGGER dualwrite_bulk_sched_rb BEFORE UPDATE OF status ON transactions WHEN NEW.id = 'tx-bulk-sched-3' BEGIN SELECT RAISE(ABORT, 'injected rollback'); END`)

	params := strings.Join([]string{"ids=" + ids[0], "ids=" + ids[1], "ids=" + ids[2], "status_pagamento=paid", "payment_date=2026-07-14"}, "&")
	req := formPost("/transactions/bulk", url.Values{})
	req.Body = io.NopCloser(strings.NewReader(params))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleBulkUpdate(rr, req)
	if rr.Code == 200 {
		t.Fatal("bulk with schedules succeeded despite injected rollback")
	}

	for _, id := range ids {
		var status string
		var paidAt, schedAt, executedAt sql.NullInt64
		var paidCivil, schedCivil sql.NullString
		if err := db.QueryRow(`SELECT status, paid_at, paid_date_civil, scheduled_payment_at, scheduled_payment_date_civil, scheduled_payment_executed_at FROM transactions WHERE id = ?`, id).Scan(&status, &paidAt, &paidCivil, &schedAt, &schedCivil, &executedAt); err != nil {
			t.Fatalf("query %s: %v", id, err)
		}
		if status != "pending" {
			t.Fatalf("%s status = %q, want pending", id, status)
		}
		if paidAt.Valid {
			t.Fatalf("%s paid_at = %d, want NULL", id, paidAt.Int64)
		}
		if paidCivil.Valid {
			t.Fatalf("%s paid_date_civil = %q, want NULL", id, paidCivil.String)
		}
		if !schedAt.Valid {
			t.Fatalf("%s scheduled_payment_at = NULL, want preserved", id)
		}
		if !schedCivil.Valid || schedCivil.String != "2026-07-20" {
			t.Fatalf("%s scheduled_payment_date_civil = %q, want 2026-07-20", id, schedCivil.String)
		}
		_ = executedAt
	}
}

func TestCivilDualWriteSchedulerRealFlowNewAndLegacyIdempotent(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertScheduledPaymentTestTransaction(t, db, "tx-real-new", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	noonUTC := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_date_civil = '2026-07-12', scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-real-new'`, noonUTC)

	insertScheduledPaymentTestTransaction(t, db, "tx-real-legacy", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	legacy0300 := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test' WHERE id = 'tx-real-legacy'`, legacy0300)

	execNow := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	count, err := RunDueScheduledPayments(context.Background(), db, execNow)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if count != 2 {
		t.Fatalf("first run processed = %d, want 2", count)
	}

	// Verify new schedule.
	var paidAt sql.NullInt64
	var paidCivil sql.NullString
	db.QueryRow(`SELECT paid_at, paid_date_civil FROM transactions WHERE id = 'tx-real-new'`).Scan(&paidAt, &paidCivil)
	wantPaid := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	if !paidAt.Valid || paidAt.Int64 != wantPaid {
		t.Fatalf("new: paid_at = %v/%d, want %d", paidAt.Valid, paidAt.Int64, wantPaid)
	}
	if !paidCivil.Valid || paidCivil.String != "2026-07-12" {
		t.Fatalf("new: paid_date_civil = %q", paidCivil.String)
	}

	// Verify legacy schedule.
	db.QueryRow(`SELECT paid_at, paid_date_civil FROM transactions WHERE id = 'tx-real-legacy'`).Scan(&paidAt, &paidCivil)
	if !paidAt.Valid || paidAt.Int64 != wantPaid {
		t.Fatalf("legacy: paid_at = %v/%d, want %d", paidAt.Valid, paidAt.Int64, wantPaid)
	}
	if !paidCivil.Valid || paidCivil.String != "2026-07-12" {
		t.Fatalf("legacy: paid_date_civil = %q", paidCivil.String)
	}

	// Idempotency: second run processes zero.
	count2, err := RunDueScheduledPayments(context.Background(), db, execNow)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if count2 != 0 {
		t.Fatalf("second run processed = %d, want 0", count2)
	}
}

func setupDualWriteTest(t *testing.T) (*sql.DB, TransactionHandler) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	h := testPaidInvoiceMutationHandler(db)
	return db, h
}
