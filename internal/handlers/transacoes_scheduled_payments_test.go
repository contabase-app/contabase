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

	"github.com/contabase-app/contabase/internal/civildate"
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

func TestScheduledPaymentUsesCurrentTransactionVersion(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, tc := range []struct {
			name, account, typ      string
			amount, checking, extra int64
		}{
			{"amount", "checking-test", "EXPENSE", 20000, 80000, 50000},
			{"account", "checking-extra", "EXPENSE", 10000, 100000, 40000},
			{"type", "checking-test", "INCOME", 10000, 110000, 50000},
			{"all", "checking-extra", "INCOME", 20000, 100000, 70000},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				row, now := seedStaleScheduledPayment(t, db, kind)
				execTestSQL(t, db, `UPDATE transactions SET account_id = ?, type = ?, amount = ?, description = 'Edited description' WHERE id = ? AND workspace_id = ?`, tc.account, tc.typ, tc.amount, row.ID, row.WorkspaceID)
				ok, err := executeScheduledPayment(context.Background(), db, row, now)
				if err != nil || !ok {
					t.Fatalf("execute = %t, %v", ok, err)
				}
				var status, account, typ string
				var amount, paidAt int64
				if err := db.QueryRow(`SELECT status, account_id, type, amount, paid_at FROM transactions WHERE id = ? AND workspace_id = ?`, row.ID, row.WorkspaceID).Scan(&status, &account, &typ, &amount, &paidAt); err != nil {
					t.Fatal(err)
				}
				if status != "paid" || account != tc.account || typ != tc.typ || amount != tc.amount || paidAt != row.ScheduledAt {
					t.Fatalf("paid transaction differs: %s %s %s %d %d", status, account, typ, amount, paidAt)
				}
				assertScheduledAccountBalances(t, db, tc.checking, tc.extra)
				var history int
				if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_status_history WHERE transaction_id = ? AND workspace_id = ? AND source = 'automatic'`, row.ID, row.WorkspaceID).Scan(&history); err != nil || history != 1 {
					t.Fatalf("history = %d, %v", history, err)
				}
				var title, message string
				if err := db.QueryRow(`SELECT title, message FROM user_notifications WHERE workspace_id = ? AND type = ?`, row.WorkspaceID, scheduledPaymentNotificationType).Scan(&title, &message); err != nil {
					t.Fatal(err)
				}
				wantTitle := "Pagamento automático executado"
				if tc.typ == "INCOME" {
					wantTitle = "Recebimento automático executado"
				}
				if title != wantTitle || !strings.Contains(message, "Edited description") {
					t.Fatalf("notification uses stale version: %s / %s", title, message)
				}
				if ok, err := executeScheduledPayment(context.Background(), db, row, now); err != nil || ok {
					t.Fatalf("duplicate execution = %t, %v", ok, err)
				}
				assertScheduledAccountBalances(t, db, tc.checking, tc.extra)
			})
		}
	}
}

func TestScheduledPaymentRevalidatesCandidateEligibility(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, tc := range []struct{ name, mutation string }{
			{"cancelled", `UPDATE transactions SET scheduled_payment_at = NULL WHERE id = 'stale-scheduled'`},
			{"rescheduled", `UPDATE transactions SET scheduled_payment_at = scheduled_payment_at + 86400 WHERE id = 'stale-scheduled'`},
			{"card", `UPDATE transactions SET account_id = 'card-test' WHERE id = 'stale-scheduled'`},
			{"deleted", `DELETE FROM transactions WHERE id = 'stale-scheduled'`},
			{"foreign-workspace", ""},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				row, now := seedStaleScheduledPayment(t, db, kind)
				if tc.mutation != "" {
					execTestSQL(t, db, tc.mutation)
				} else {
					row.WorkspaceID = "different-workspace"
				}
				if ok, err := executeScheduledPayment(context.Background(), db, row, now); err != nil || ok {
					t.Fatalf("ineligible execution = %t, %v", ok, err)
				}
				assertScheduledAccountBalances(t, db, 100000, 50000)
				var effects int
				if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM transaction_status_history WHERE transaction_id = 'stale-scheduled') + (SELECT COUNT(*) FROM user_notifications WHERE type = ?)`, scheduledPaymentNotificationType).Scan(&effects); err != nil || effects != 0 {
					t.Fatalf("ineligible side effects = %d, %v", effects, err)
				}
			})
		}
	}
}

func TestScheduledPaymentCurrentVersionRollsBackOnHistoryFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	row, now := seedStaleScheduledPayment(t, db, "business")
	execTestSQL(t, db, `UPDATE transactions SET amount = 20000 WHERE id = 'stale-scheduled'`)
	execTestSQL(t, db, `CREATE TRIGGER fail_scheduled_history BEFORE INSERT ON transaction_status_history BEGIN SELECT RAISE(ABORT, 'injected history failure'); END`)
	if ok, err := executeScheduledPayment(context.Background(), db, row, now); err == nil || ok {
		t.Fatalf("expected rollback, got %t, %v", ok, err)
	}
	assertScheduledAccountBalances(t, db, 100000, 50000)
	var status string
	var executed sql.NullInt64
	if err := db.QueryRow(`SELECT status, scheduled_payment_executed_at FROM transactions WHERE id = ?`, row.ID).Scan(&status, &executed); err != nil || status != "pending" || executed.Valid {
		t.Fatalf("failed execution persisted: %s %v %v", status, executed, err)
	}
}

func seedStaleScheduledPayment(t *testing.T, db *sql.DB, kind string) (scheduledPaymentDueRow, time.Time) {
	t.Helper()
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, kind)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('checking-extra', 'ws-test', 'Extra', 'CHECKING', 50000, 50000)`)
	insertScheduledPaymentTestTransaction(t, db, "stale-scheduled", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET amount = 10000, scheduled_payment_at = ?, scheduled_payment_date_civil = '2026-07-18', scheduled_payment_created_by = 'user-test' WHERE id = 'stale-scheduled'`, testAppCivilDate("2026-07-18"))
	now := time.Date(2026, 7, 18, 18, 0, 0, 0, time.UTC)
	rows, err := queryDueScheduledPayments(context.Background(), db, now)
	if err != nil || len(rows) != 1 {
		t.Fatalf("candidates=%d, err=%v", len(rows), err)
	}
	return rows[0], now
}

func assertScheduledAccountBalances(t *testing.T, db *sql.DB, checking, extra int64) {
	t.Helper()
	for account, want := range map[string]int64{"checking-test": checking, "checking-extra": extra} {
		var got int64
		if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ? AND workspace_id = 'ws-test'`, account).Scan(&got); err != nil || got != want {
			t.Fatalf("%s balance=%d, err=%v; want %d", account, got, err, want)
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

func TestScheduledPaymentExecutorRejectsHistoricalInvoicePaymentTransactions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reversed bool
	}{
		{name: "active"},
		{name: "reversed", reversed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
			db := openTestDB(t)
			t.Cleanup(func() { _ = db.Close() })

			paymentTransactionID, dueRow := seedHistoricalScheduledInvoicePayment(t, db, tc.reversed)
			before := scheduledInvoicePaymentProtectionSnapshotFor(t, db, paymentTransactionID)
			executorNow := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)

			dueRows, err := queryDueScheduledPayments(context.Background(), db, executorNow)
			if err != nil {
				t.Fatalf("query due scheduled payments: %v", err)
			}
			for _, got := range dueRows {
				if got.ID == paymentTransactionID {
					t.Fatalf("invoice payment transaction %s appeared in due selection", paymentTransactionID)
				}
			}

			executed, err := executeScheduledPayment(context.Background(), db, dueRow, executorNow)
			if err != nil {
				t.Fatalf("execute protected scheduled payment: %v", err)
			}
			if executed {
				t.Fatal("conditional UPDATE executed an invoice payment transaction")
			}
			assertScheduledInvoicePaymentProtectionSnapshot(t, db, paymentTransactionID, before)

			processed, err := RunDueScheduledPayments(context.Background(), db, executorNow)
			if err != nil {
				t.Fatalf("run due scheduled payments: %v", err)
			}
			if processed != 0 {
				t.Fatalf("processed = %d, want 0", processed)
			}
			assertScheduledInvoicePaymentProtectionSnapshot(t, db, paymentTransactionID, before)
		})
	}
}

func TestScheduledPaymentInvoiceProtectionPreservesWorkspaceIsolationAndOrdinaryExecution(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedInvoicePaymentScenario(t, db)
	seedForeignInvoicePaymentProtectionScenario(t, db)

	insertScheduledPaymentTestTransaction(t, db, "tx-workspace-common", models.TransactionTypeExpense, "checking-test", "", "pending", nil)
	scheduledAt := testAppCivilDate("2026-08-01")
	execTestSQL(t, db, `UPDATE transactions SET scheduled_payment_at = ?, scheduled_payment_date_civil = '2026-08-01', scheduled_payment_created_by = 'user-test' WHERE id = 'tx-workspace-common'`, scheduledAt)
	// invoice_payments.transaction_id is not a foreign key. A malformed relation
	// from another tenant must not hide a legitimate transaction in ws-test.
	execTestSQL(t, db, `UPDATE invoice_payments SET transaction_id = 'tx-workspace-common' WHERE id = 'foreign-ip'`)

	executorNow := time.Date(2026, 8, 2, 8, 0, 0, 0, time.UTC)
	dueRows, err := queryDueScheduledPayments(context.Background(), db, executorNow)
	if err != nil {
		t.Fatalf("query due scheduled payments: %v", err)
	}
	if len(dueRows) != 1 || dueRows[0].ID != "tx-workspace-common" {
		t.Fatalf("due rows = %#v, want only tx-workspace-common", scheduledPaymentRowIDs(dueRows))
	}

	processed, err := RunDueScheduledPayments(context.Background(), db, executorNow)
	if err != nil {
		t.Fatalf("run due scheduled payments: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}

	var status string
	var balance int64
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = 'tx-workspace-common'`).Scan(&status); err != nil {
		t.Fatalf("query ordinary transaction: %v", err)
	}
	if status != models.PaymentStatusPaid {
		t.Fatalf("ordinary status = %q, want paid", status)
	}
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test' AND workspace_id = 'ws-test'`).Scan(&balance); err != nil {
		t.Fatalf("query ordinary account balance: %v", err)
	}
	if balance != 87655 {
		t.Fatalf("ordinary account balance = %d, want 87655", balance)
	}

	var foreignPaymentCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE id = 'foreign-ip' AND workspace_id = 'ws-foreign' AND transaction_id = 'tx-workspace-common'`).Scan(&foreignPaymentCount); err != nil {
		t.Fatalf("query foreign invoice payment: %v", err)
	}
	if foreignPaymentCount != 1 {
		t.Fatalf("foreign invoice payment count = %d, want 1", foreignPaymentCount)
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

type scheduledInvoicePaymentProtectionState struct {
	status              string
	paidAt              sql.NullInt64
	paidDateCivil       sql.NullString
	scheduledAt         sql.NullInt64
	scheduledDateCivil  sql.NullString
	executedAt          sql.NullInt64
	updatedAt           int64
	balance             int64
	invoicePaymentCount int
	invoicePaymentTotal int64
	reversedAt          sql.NullInt64
	invoiceStatus       string
	invoicePaidAmount   int64
	invoicePaidAt       sql.NullInt64
	historyCount        int
	notificationCount   int
}

func seedHistoricalScheduledInvoicePayment(t *testing.T, db *sql.DB, reversed bool) (string, scheduledPaymentDueRow) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)

	faturas := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   invoicePaymentProtectionTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	payRR := httptest.NewRecorder()
	faturas.HandlePagarFatura(payRR, formPost("/cartoes/faturas/pagar", url.Values{
		"invoice_id":         {"invoice-2026-08"},
		"payment_account_id": {"checking-test"},
		"payment_mode":       {"settle"},
		"confirm_settle":     {"1"},
		"payment_date":       {"2026-08-08"},
	}))
	if payRR.Code != http.StatusOK {
		t.Fatalf("invoice payment status = %d, body = %q", payRR.Code, payRR.Body.String())
	}

	var paymentTransactionID string
	if err := db.QueryRow(`SELECT transaction_id FROM invoice_payments WHERE workspace_id = 'ws-test' AND invoice_id = 'invoice-2026-08'`).Scan(&paymentTransactionID); err != nil {
		t.Fatalf("query payment transaction: %v", err)
	}
	if paymentTransactionID == "" {
		t.Fatal("HandlePagarFatura created an empty transaction_id")
	}

	scheduledAt := testAppCivilDate("2026-08-01")
	execTestSQL(t, db, `
		UPDATE transactions
		SET status = 'pending', paid_at = NULL, paid_date_civil = NULL,
		    scheduled_payment_at = ?, scheduled_payment_date_civil = '2026-08-01',
		    scheduled_payment_created_by = 'user-test', scheduled_payment_executed_at = NULL,
		    updated_at = ?
		WHERE id = ? AND workspace_id = 'ws-test'
	`, scheduledAt, scheduledAt, paymentTransactionID)
	// This is the balance left by the formerly reachable paid -> pending action.
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 100000 WHERE id = 'checking-test' AND workspace_id = 'ws-test'`)
	if reversed {
		execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = ? WHERE workspace_id = 'ws-test' AND transaction_id = ?`, scheduledAt+1, paymentTransactionID)
		execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN', paid_amount = 0, paid_at = NULL WHERE id = 'invoice-2026-08'`)
	}

	var row scheduledPaymentDueRow
	if err := db.QueryRow(`
		SELECT t.id, t.workspace_id, t.user_id, t.scheduled_payment_created_by, t.type, t.amount,
		       t.account_id, a.type, t.description, t.scheduled_payment_at,
		       t.scheduled_payment_date_civil, t.paid_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = 'ws-test'
	`, paymentTransactionID).Scan(
		&row.ID, &row.WorkspaceID, &row.UserID, &row.CreatedBy, &row.Type, &row.Amount,
		&row.AccountID, &row.AccountType, &row.Description, &row.ScheduledAt,
		&row.ScheduledDateCivil, &row.PaidAt,
	); err != nil {
		t.Fatalf("query historical scheduled payment row: %v", err)
	}
	return paymentTransactionID, row
}

func scheduledInvoicePaymentProtectionSnapshotFor(t *testing.T, db *sql.DB, transactionID string) scheduledInvoicePaymentProtectionState {
	t.Helper()
	var state scheduledInvoicePaymentProtectionState
	if err := db.QueryRow(`
		SELECT t.status, t.paid_at, t.paid_date_civil, t.scheduled_payment_at,
		       t.scheduled_payment_date_civil, t.scheduled_payment_executed_at,
		       t.updated_at, a.current_balance
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = 'ws-test'
	`, transactionID).Scan(
		&state.status, &state.paidAt, &state.paidDateCivil, &state.scheduledAt,
		&state.scheduledDateCivil, &state.executedAt, &state.updatedAt, &state.balance,
	); err != nil {
		t.Fatalf("query protected scheduled transaction snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(amount_cents), 0), MAX(reversed_at)
		FROM invoice_payments
		WHERE workspace_id = 'ws-test' AND transaction_id = ?
	`, transactionID).Scan(&state.invoicePaymentCount, &state.invoicePaymentTotal, &state.reversedAt); err != nil {
		t.Fatalf("query invoice payment snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT i.status, i.paid_amount, i.paid_at
		FROM invoices i
		JOIN invoice_payments ip ON ip.invoice_id = i.id
		WHERE ip.workspace_id = 'ws-test' AND ip.transaction_id = ?
	`, transactionID).Scan(&state.invoiceStatus, &state.invoicePaidAmount, &state.invoicePaidAt); err != nil {
		t.Fatalf("query invoice snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = ? AND workspace_id = 'ws-test'`, transactionID).Scan(&state.historyCount); err != nil {
		t.Fatalf("query automatic history snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM user_notifications WHERE workspace_id = 'ws-test' AND type = ?`, scheduledPaymentNotificationType).Scan(&state.notificationCount); err != nil {
		t.Fatalf("query scheduled notification snapshot: %v", err)
	}
	return state
}

func assertScheduledInvoicePaymentProtectionSnapshot(t *testing.T, db *sql.DB, transactionID string, want scheduledInvoicePaymentProtectionState) {
	t.Helper()
	if got := scheduledInvoicePaymentProtectionSnapshotFor(t, db, transactionID); got != want {
		t.Fatalf("protected scheduled payment state changed\n got: %+v\nwant: %+v", got, want)
	}
}

func scheduledPaymentRowIDs(rows []scheduledPaymentDueRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
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
	wantNoonUTC := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	if got := testAppCivilDate("2026-07-10"); got != wantNoonUTC {
		t.Fatalf("CONTABASE_TIMEZONE priority got %d, want noon-UTC %d", got, wantNoonUTC)
	}
	t.Setenv("CONTABASE_TIMEZONE", "")
	if got := testAppCivilDate("2026-07-10"); got != wantNoonUTC {
		t.Fatalf("UTC fallback got %d, want noon-UTC %d", got, wantNoonUTC)
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
	return civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate(date))
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}
