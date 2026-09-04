package handlers

import (
	"bytes"
	"database/sql"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

type recurringDeleteFixture struct {
	db       *sql.DB
	handler  TransactionHandler
	seriesID string
	ruleID   string
	txType   string
	amount   int64
}

func newRecurringDeleteFixture(t *testing.T, db *sql.DB, workspaceType, transactionType string, finite bool) *recurringDeleteFixture {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, workspaceType)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-delete-dest', 'ws-test', 'Destino Delete', 'CHECKING', 50000, 50000, ?, ?)
	`, now, now)

	seriesID := "series-delete-" + transactionType + "-" + workspaceType
	ruleID := "rule-delete-" + transactionType + "-" + workspaceType
	var total interface{}
	if finite {
		total = int64(5)
	}
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, ?, ?, ?)
	`, seriesID, total, now, now)
	var destination interface{}
	if transactionType == "TRANSFER" {
		destination = "checking-delete-dest"
	}
	var dueOffset interface{}
	if workspaceType == "business" {
		dueOffset = int64(5)
	}
	var category interface{}
	if transactionType == "EXPENSE" {
		category = "cat-expense-rec"
	}
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, destination_account_id, category_id,
			type, amount, description, start_date, start_date_civil,
			starts_at, starts_at_civil, frequency, default_payment_status,
			active, total_occurrences, series_id, effective_start_sequence,
			due_date_offset_days, created_at, updated_at
		) VALUES (
			?, 'ws-test', 'user-test', 'checking-test', ?, ?,
			?, 10000, 'Exclusão durável', ?, '2026-01-15',
			?, '2026-01-15', 'MONTHLY', 'PENDING',
			1, ?, ?, 1, ?, ?, ?
		)
	`, ruleID, destination, category, transactionType, testUnixDate("2026-01-15"), testUnixDate("2026-01-15"), total, seriesID, dueOffset, now, now)
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("materialize fixture: %v", err)
	}
	return &recurringDeleteFixture{
		db:       db,
		handler:  TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)},
		seriesID: seriesID,
		ruleID:   ruleID,
		txType:   transactionType,
		amount:   10000,
	}
}

func (f *recurringDeleteFixture) transactionID(t *testing.T, sequence int64) string {
	t.Helper()
	var id string
	if err := f.db.QueryRow(`
		SELECT id FROM transactions
		WHERE workspace_id = 'ws-test' AND series_id = ? AND recurrence_sequence = ?
	`, f.seriesID, sequence).Scan(&id); err != nil {
		t.Fatalf("query sequence %d: %v", sequence, err)
	}
	return id
}

func recurringDeleteRequest(t *testing.T, handler *TransactionHandler, transactionID, scope string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/transacoes/"+transactionID+"?escopo="+scope, nil)
	req.Header.Set("HX-Current-URL", "http://localhost/lancamentos")
	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, req, transactionID)
	return rr
}

func assertRecurringSkip(t *testing.T, db *sql.DB, workspaceID, seriesID string, sequence int64, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM recurring_occurrence_skips
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
	`, workspaceID, seriesID, sequence).Scan(&count); err != nil {
		t.Fatalf("count skip: %v", err)
	}
	if count != want {
		t.Fatalf("skip count = %d, want %d", count, want)
	}
}

func assertRecurringSequenceCount(t *testing.T, db *sql.DB, workspaceID, seriesID string, sequence int64, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM transactions
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
	`, workspaceID, seriesID, sequence).Scan(&count); err != nil {
		t.Fatalf("count sequence: %v", err)
	}
	if count != want {
		t.Fatalf("sequence %d count = %d, want %d", sequence, count, want)
	}
}

func TestRecurringDeleteSinglePersistsSkipAcrossMaterializationTypesAndWorkspaces(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workspaceType string
		transaction   string
		finite        bool
	}{
		{name: "personal_expense_finite", workspaceType: "personal", transaction: "EXPENSE", finite: true},
		{name: "business_expense_endless", workspaceType: "business", transaction: "EXPENSE", finite: false},
		{name: "personal_income", workspaceType: "personal", transaction: "INCOME", finite: true},
		{name: "personal_transfer", workspaceType: "personal", transaction: "TRANSFER", finite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			fixture := newRecurringDeleteFixture(t, db, tc.workspaceType, tc.transaction, tc.finite)
			selectedID := fixture.transactionID(t, 3)

			rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
			if rr.Code != http.StatusOK {
				t.Fatalf("delete status = %d body=%q", rr.Code, rr.Body.String())
			}
			assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 1)
			assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)

			for i := 0; i < 2; i++ {
				if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-08-15")); err != nil {
					t.Fatalf("materializer run %d: %v", i+1, err)
				}
			}
			assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
			for _, sequence := range []int64{1, 2, 4, 5} {
				assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, sequence, 1)
			}
			if tc.transaction == "TRANSFER" {
				var dangling int
				if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND destination_account_id IS NULL`, fixture.seriesID).Scan(&dangling); err != nil {
					t.Fatalf("count transfer sides: %v", err)
				}
				if dangling != 0 {
					t.Fatalf("transfer occurrences without destination = %d", dangling)
				}
			}
			var generatedUntil string
			if err := db.QueryRow(`SELECT generated_until_civil FROM recurring_rules WHERE id=? AND workspace_id='ws-test'`, fixture.ruleID).Scan(&generatedUntil); err != nil {
				t.Fatalf("generated_until_civil: %v", err)
			}
			if generatedUntil != "2026-08-15" {
				t.Fatalf("generated_until_civil = %q, want 2026-08-15", generatedUntil)
			}
		})
	}
}

func TestRecurringDeleteSinglePaidAndExceptionDoNotReappear(t *testing.T) {
	for _, mutation := range []string{"paid", "exception"} {
		t.Run(mutation, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
			selectedID := fixture.transactionID(t, 3)
			if mutation == "paid" {
				execTestSQL(t, db, `UPDATE transactions SET status='paid', paid_at=12345 WHERE id=?`, selectedID)
				execTestSQL(t, db, `UPDATE accounts SET current_balance=90000 WHERE id='checking-test'`)
			} else {
				execTestSQL(t, db, `UPDATE transactions SET is_recurring_exception=1, recurring_exception_reason='manual_edit', description='Editada' WHERE id=?`, selectedID)
			}

			rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
			if rr.Code != http.StatusOK {
				t.Fatalf("delete status = %d body=%q", rr.Code, rr.Body.String())
			}
			if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-05-15")); err != nil {
				t.Fatalf("materializer: %v", err)
			}
			assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
			assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 1)
			if mutation == "paid" {
				var balance int64
				if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balance); err != nil {
					t.Fatalf("balance: %v", err)
				}
				if balance != 100000 {
					t.Fatalf("balance after paid delete = %d, want 100000", balance)
				}
			}
		})
	}
}

func TestRecurringDeleteSingleBlocksPaidInvoices(t *testing.T) {
	for _, status := range []string{"PAID"} {
		t.Run(status, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			ruleID, selectedID := seedRecurringSeriesOnCard(t, db, "Protegida", "2026-06-05", 3)
			seriesID := "series-delete-card-" + status
			attachRecurringSeriesIdentity(t, db, ruleID, seriesID)
			execTestSQL(t, db, `UPDATE invoices SET status=? WHERE id='invoice-2026-08'`, status)
			handler := deleteHandler(t, db)

			rr := recurringDeleteRequest(t, &handler, selectedID, "single")
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 body=%q", rr.Code, rr.Body.String())
			}
			assertRecurringSkip(t, db, "ws-test", seriesID, 1, 0)
			assertRecurringSequenceCount(t, db, "ws-test", seriesID, 1, 1)
			var invoiceState string
			if err := db.QueryRow(`SELECT status FROM invoices WHERE id='invoice-2026-08'`).Scan(&invoiceState); err != nil {
				t.Fatalf("invoice state: %v", err)
			}
			if invoiceState != status {
				t.Fatalf("invoice status = %q, want %q", invoiceState, status)
			}
		})
	}
}

func TestRecurringDeleteSingleOpenInvoiceCreatesSkipAndDoesNotReappear(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ruleID, selectedID := seedRecurringSeriesOnCard(t, db, "Fatura aberta", "2026-06-05", 3)
	seriesID := "series-delete-card-open"
	attachRecurringSeriesIdentity(t, db, ruleID, seriesID)
	handler := deleteHandler(t, db)

	rr := recurringDeleteRequest(t, &handler, selectedID, "single")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertRecurringSkip(t, db, "ws-test", seriesID, 1, 1)
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-08-05")); err != nil {
		t.Fatalf("materializer: %v", err)
	}
	assertRecurringSequenceCount(t, db, "ws-test", seriesID, 1, 0)
	var invoiceStatus string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id='invoice-2026-08'`).Scan(&invoiceStatus); err != nil {
		t.Fatalf("invoice status: %v", err)
	}
	if invoiceStatus != "OPEN" {
		t.Fatalf("invoice status = %q, want OPEN", invoiceStatus)
	}
}

func TestRecurringDeleteSingleLegacySeriesCreatesDurableSkip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	selectedID := fixture.transactionID(t, 3)
	execTestSQL(t, db, `DELETE FROM recurring_series WHERE workspace_id='ws-test' AND id=?`, fixture.seriesID)

	rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy delete status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertRecurringSkip(t, db, "ws-test", fixture.ruleID, 3, 1)
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("legacy materializer: %v", err)
	}
	assertRecurringSequenceCount(t, db, "ws-test", fixture.ruleID, 3, 0)
}

func TestRecurringDeleteSingleSkipIsIsolatedByWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-delete-b', 'User B', 'delete-b@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-delete-b', 'Workspace B', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-delete-b', 'user-delete-b', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-delete-b', 'ws-delete-b', 'Conta B', 'CHECKING', 100000, 100000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at) VALUES ('series-delete-b', 'ws-delete-b', 'user-delete-b', 1, 5, ?, ?)`, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id,
			effective_start_sequence, due_date_offset_days, created_at, updated_at
		) VALUES (
			'rule-delete-b', 'ws-delete-b', 'user-delete-b', 'acc-delete-b', 'EXPENSE', 10000, 'B',
			?, '2026-01-15', ?, '2026-01-15', 'MONTHLY', 'PENDING', 1, 5,
			'series-delete-b', 1, 5, ?, ?
		)
	`, testUnixDate("2026-01-15"), testUnixDate("2026-01-15"), now, now)
	if err := EnsureRecurringMaterialized(db, "ws-delete-b", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("materialize workspace B: %v", err)
	}
	var workspaceBID string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id='ws-delete-b' AND series_id='series-delete-b' AND recurrence_sequence=3`).Scan(&workspaceBID); err != nil {
		t.Fatalf("workspace B sequence: %v", err)
	}

	selectedID := fixture.transactionID(t, 3)
	rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
	if rr.Code != http.StatusOK {
		t.Fatalf("workspace A delete status = %d", rr.Code)
	}
	if err := EnsureRecurringMaterialized(db, "ws-delete-b", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("rematerialize workspace B: %v", err)
	}
	var workspaceBAfter string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id='ws-delete-b' AND series_id='series-delete-b' AND recurrence_sequence=3`).Scan(&workspaceBAfter); err != nil {
		t.Fatalf("workspace B sequence after A delete: %v", err)
	}
	if workspaceBAfter != workspaceBID {
		t.Fatalf("workspace B transaction changed: before=%s after=%s", workspaceBID, workspaceBAfter)
	}
	assertRecurringSkip(t, db, "ws-delete-b", "series-delete-b", 3, 0)
}

func TestRecurringDeleteSingleRollbackOnDeleteBalanceReconcileAndCommitFailures(t *testing.T) {
	for _, failure := range []string{"delete", "balance", "reconcile", "commit"} {
		t.Run(failure, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			var fixtureSeries, selectedID string
			var handler TransactionHandler
			if failure == "reconcile" {
				ruleID, firstID := seedRecurringSeriesOnCard(t, db, "Rollback invoice", "2026-06-05", 3)
				fixtureSeries = "series-delete-rollback-invoice"
				attachRecurringSeriesIdentity(t, db, ruleID, fixtureSeries)
				selectedID = firstID
				handler = deleteHandler(t, db)
				execTestSQL(t, db, `DELETE FROM transactions WHERE id='purchase-test'`)
				execTestSQL(t, db, `UPDATE invoices SET closing_date=1 WHERE id='invoice-2026-08'`)
				execTestSQL(t, db, `CREATE TRIGGER fail_recurring_delete_reconcile BEFORE UPDATE ON invoices BEGIN SELECT RAISE(ABORT, 'forced reconcile failure'); END`)
			} else {
				fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
				fixtureSeries = fixture.seriesID
				selectedID = fixture.transactionID(t, 3)
				handler = fixture.handler
				switch failure {
				case "delete":
					execTestSQL(t, db, `CREATE TRIGGER fail_recurring_delete BEFORE DELETE ON transactions BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END`)
				case "balance":
					execTestSQL(t, db, `UPDATE transactions SET status='paid', paid_at=123 WHERE id=?`, selectedID)
					execTestSQL(t, db, `UPDATE accounts SET current_balance=90000 WHERE id='checking-test'`)
					execTestSQL(t, db, `CREATE TRIGGER fail_recurring_delete_balance BEFORE UPDATE OF current_balance ON accounts BEGIN SELECT RAISE(ABORT, 'forced balance failure'); END`)
				case "commit":
					execTestSQL(t, db, `CREATE TABLE recurring_delete_commit_parent (id INTEGER PRIMARY KEY)`)
					execTestSQL(t, db, `CREATE TABLE recurring_delete_commit_child (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES recurring_delete_commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
					execTestSQL(t, db, `CREATE TRIGGER fail_recurring_delete_commit AFTER DELETE ON transactions BEGIN INSERT INTO recurring_delete_commit_child(parent_id) VALUES (999); END`)
				}
			}

			rr := recurringDeleteRequest(t, &handler, selectedID, "single")
			if rr.Code == http.StatusOK {
				t.Fatalf("forced %s failure returned 200", failure)
			}
			sequence := int64(3)
			if failure == "reconcile" {
				sequence = 1
			}
			assertRecurringSkip(t, db, "ws-test", fixtureSeries, sequence, 0)
			var count int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id=? AND workspace_id='ws-test'`, selectedID).Scan(&count); err != nil {
				t.Fatalf("count selected after rollback: %v", err)
			}
			if count != 1 {
				t.Fatalf("transaction count after %s failure = %d, want 1", failure, count)
			}
		})
	}
}

func TestRecurringDeleteAllVersionedClosesSeriesAndPreservesProtectedHistory(t *testing.T) {
	fixture := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer fixture.close()
	now := time.Now().Unix()
	execTestSQL(t, fixture.db, `UPDATE recurring_rules SET active=0, effective_end_sequence=1 WHERE id=?`, fixture.ruleID)
	execTestSQL(t, fixture.db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id, parent_rule_id,
			effective_start_sequence, effective_end_sequence, created_at, updated_at
		) SELECT
			'rule-delete-all-b', workspace_id, user_id, account_id, category_id, type, amount, 'B',
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, 0, total_occurrences, series_id, id, 2, 2, ?, ?
		FROM recurring_rules WHERE id=?
	`, now, now, fixture.ruleID)
	execTestSQL(t, fixture.db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id, parent_rule_id,
			effective_start_sequence, created_at, updated_at
		) SELECT
			'rule-delete-all-c', workspace_id, user_id, account_id, category_id, type, amount, 'C',
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, 1, total_occurrences, series_id, 'rule-delete-all-b', 3, ?, ?
		FROM recurring_rules WHERE id=?
	`, now, now, fixture.ruleID)
	execTestSQL(t, fixture.db, `UPDATE transactions SET recurring_rule_id='rule-delete-all-b' WHERE id=?`, fixture.ids[1])
	execTestSQL(t, fixture.db, `UPDATE transactions SET recurring_rule_id='rule-delete-all-c', is_recurring_exception=1, recurring_exception_reason='manual_edit' WHERE id=?`, fixture.ids[2])
	execTestSQL(t, fixture.db, `UPDATE transactions SET status='paid', paid_at=123 WHERE id=?`, fixture.ids[0])

	rr := recurringDeleteRequest(t, &fixture.handler, fixture.ids[1], "all")
	if rr.Code != http.StatusOK {
		t.Fatalf("delete all status = %d body=%q", rr.Code, rr.Body.String())
	}
	var seriesActive, activeRules, ruleCount int
	if err := fixture.db.QueryRow(`SELECT active FROM recurring_series WHERE workspace_id='ws-test' AND id=?`, fixture.seriesID).Scan(&seriesActive); err != nil {
		t.Fatalf("series state: %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT COUNT(1), SUM(CASE WHEN active=1 THEN 1 ELSE 0 END) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?`, fixture.seriesID).Scan(&ruleCount, &activeRules); err != nil {
		t.Fatalf("rule state: %v", err)
	}
	if seriesActive != 0 || activeRules != 0 || ruleCount != 3 {
		t.Fatalf("series/rules after all = active:%d activeRules:%d ruleCount:%d", seriesActive, activeRules, ruleCount)
	}
	assertRecurringSequenceCount(t, fixture.db, "ws-test", fixture.seriesID, 1, 1)
	assertRecurringSequenceCount(t, fixture.db, "ws-test", fixture.seriesID, 2, 0)
	assertRecurringSequenceCount(t, fixture.db, "ws-test", fixture.seriesID, 3, 1)
	for i := 0; i < 2; i++ {
		if err := EnsureRecurringMaterialized(fixture.db, "ws-test", MustCivilWriteDate("2026-12-10")); err != nil {
			t.Fatalf("materializer after all %d: %v", i+1, err)
		}
	}
	assertRecurringSequenceCount(t, fixture.db, "ws-test", fixture.seriesID, 2, 0)
	if _, _, err := fixture.handler.deleteAllRecurringSeriesVersionedTx(mustBeginTx(t, fixture.db), fixture.ids[0], fixture.ruleID, now+1); err != nil {
		t.Fatalf("idempotent all helper: %v", err)
	}
}

func mustBeginTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestRecurringDeleteSingleSurvivesReopenAndConcurrentMaterializer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recurring-delete.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap disk db: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open disk db: %v", err)
	}
	db.SetMaxOpenConns(2)
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	selectedID := fixture.transactionID(t, 3)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_ = recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
	}()
	go func() {
		defer wg.Done()
		<-start
		_ = EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-05-15"))
	}()
	close(start)
	wg.Wait()
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 1)
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
	if err := db.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}

	reopened, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen disk db: %v", err)
	}
	defer reopened.Close()
	reopened.SetMaxOpenConns(2)
	if err := EnsureRecurringMaterialized(reopened, "ws-test", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("materialize after reopen: %v", err)
	}
	assertRecurringSkip(t, reopened, "ws-test", fixture.seriesID, 3, 1)
	assertRecurringSequenceCount(t, reopened, "ws-test", fixture.seriesID, 3, 0)
}

func TestRecurringDeleteSingleConcurrentRetriesLeaveOneSkip(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recurring-delete-retries.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap disk db: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open disk db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	selectedID := fixture.transactionID(t, 3)

	start := make(chan struct{})
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- recurringDeleteRequest(t, &fixture.handler, selectedID, "single").Code
		}()
	}
	close(start)
	statusA, statusB := <-results, <-results
	if statusA != http.StatusOK && statusB != http.StatusOK {
		t.Fatalf("neither concurrent delete succeeded: %d, %d", statusA, statusB)
	}
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 1)
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("materializer after retries: %v", err)
	}
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
}

func TestRecurringDeleteSingleConcurrentEditCannotResurrectSequence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recurring-delete-edit.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap disk db: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open disk db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	selectedID := fixture.transactionID(t, 3)
	editRequest := newMultipartUpdateRequest(t, "/transacoes/"+selectedID+"/salvar?escopo=single", map[string]string{
		"valor":            "125,00",
		"descricao":        "Edição concorrente",
		"data":             "2026-03-15",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "single",
	})
	editRequest.Header.Set("HX-Current-URL", "http://localhost/lancamentos")

	start := make(chan struct{})
	deleteResult := make(chan int, 1)
	editResult := make(chan int, 1)
	go func() {
		<-start
		deleteResult <- recurringDeleteRequest(t, &fixture.handler, selectedID, "single").Code
	}()
	go func() {
		<-start
		rr := httptest.NewRecorder()
		fixture.handler.HandleAtualizarTransacao(rr, editRequest, selectedID)
		editResult <- rr.Code
	}()
	close(start)
	deleteStatus, editStatus := <-deleteResult, <-editResult
	if deleteStatus != http.StatusOK {
		t.Fatalf("concurrent delete status = %d; edit status = %d", deleteStatus, editStatus)
	}
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 1)
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-05-15")); err != nil {
		t.Fatalf("materializer after concurrent edit/delete: %v", err)
	}
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 0)
}

func TestRecurringDeleteSingleFailureInSkipInsertPreservesTransaction(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	selectedID := fixture.transactionID(t, 3)

	execTestSQL(t, db, `CREATE TRIGGER fail_recurring_skip_insert BEFORE INSERT ON recurring_occurrence_skips
		BEGIN SELECT RAISE(ABORT, 'test_skip_insert_failpoint'); END`)

	var balanceBefore int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceBefore); err != nil {
		t.Fatalf("balance before: %v", err)
	}
	var ruleActive int
	if err := db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, fixture.ruleID).Scan(&ruleActive); err != nil {
		t.Fatalf("rule active: %v", err)
	}

	lc := captureLog(t)

	rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
	if rr.Code == http.StatusOK {
		lc.require("unreachable")
		t.Fatal("forced skip insert failure returned 200")
	}
	lc.require("test_skip_insert_failpoint")

	assertRecurringDeletePreserved(t, db, "ws-test", fixture.seriesID, fixture.ruleID, 3, selectedID, balanceBefore, ruleActive)
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 0)

	var seriesActive int
	if err := db.QueryRow(`SELECT active FROM recurring_series WHERE workspace_id='ws-test' AND id=?`, fixture.seriesID).Scan(&seriesActive); err != nil {
		t.Fatalf("series active: %v", err)
	}
	if seriesActive != 1 {
		t.Errorf("series active = %d, want 1", seriesActive)
	}

	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-08-15")); err != nil {
		t.Fatalf("materializer after skip failure: %v", err)
	}
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 1)
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 0)
}

func assertRecurringDeletePreserved(t *testing.T, db *sql.DB, workspaceID, seriesID, ruleID string, sequence int64, selectedID string, balanceBefore int64, ruleActiveBefore int) bool {
	t.Helper()
	ok := true
	var balanceAfter int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceAfter); err != nil {
		t.Fatalf("balance after: %v", err)
	}
	if balanceAfter != balanceBefore {
		t.Errorf("balance changed: before=%d after=%d", balanceBefore, balanceAfter)
		ok = false
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id=? AND workspace_id='ws-test'`, selectedID).Scan(&count); err != nil {
		t.Fatalf("count selected: %v", err)
	}
	if count != 1 {
		t.Errorf("transaction missing after rollback: count=%d", count)
		ok = false
	}
	var seqCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=?`, workspaceID, seriesID, sequence).Scan(&seqCount); err != nil {
		t.Fatalf("count sequence: %v", err)
	}
	if seqCount != 1 {
		t.Errorf("sequence count = %d, want 1", seqCount)
		ok = false
	}
	var ruleActive int
	if err := db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, ruleID).Scan(&ruleActive); err != nil {
		t.Fatalf("rule active after: %v", err)
	}
	if ruleActive != ruleActiveBefore {
		t.Errorf("rule active changed: before=%d after=%d", ruleActiveBefore, ruleActive)
		ok = false
	}
	return ok
}

var logCaptureMu sync.Mutex

type logCapture struct {
	buf bytes.Buffer
	t   *testing.T
}

func captureLog(t *testing.T) *logCapture {
	t.Helper()
	logCaptureMu.Lock()

	origWriter := log.Writer()
	origPrefix := log.Prefix()
	origFlags := log.Flags()

	lc := &logCapture{t: t}
	log.SetOutput(&lc.buf)
	log.SetPrefix("")
	log.SetFlags(0)

	var cleanedUp bool
	t.Cleanup(func() {
		if cleanedUp {
			return
		}
		cleanedUp = true
		log.SetOutput(origWriter)
		log.SetPrefix(origPrefix)
		log.SetFlags(origFlags)
		logCaptureMu.Unlock()
	})
	return lc
}

func (lc *logCapture) require(marker string) {
	lc.t.Helper()
	if !strings.Contains(lc.buf.String(), marker) {
		lc.t.Fatalf("failpoint marker %q not found in log output: %q", marker, lc.buf.String())
	}
}

func TestRecurringDeleteSingleFailureInReserveReversalPreservesAll(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	fixture := newRecurringDeleteFixture(t, db, "personal", "EXPENSE", true)
	selectedID := fixture.transactionID(t, 3)
	execTestSQL(t, db, `UPDATE transactions SET status='paid', paid_at=12345 WHERE id=?`, selectedID)
	execTestSQL(t, db, `UPDATE accounts SET current_balance=90000 WHERE id='checking-test'`)

	now := time.Now().Unix()
	boxID := "box-reserve-reversal-test"
	execTestSQL(t, db, `INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES (?, 'ws-test', 'Caixinha Rollback', 'cat-expense-rec', 500000, 0, ?, ?)`, boxID, now, now)
	execTestSQL(t, db, `INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, source_transaction_id, reference_date, created_at)
		VALUES (?, ?, -10000, 'CONSUME', 'Consumo para teste de rollback', ?, ?, ?)`,
		"ledger-reserve-rollback", boxID, selectedID, testUnixDate("2026-03-15"), now)

	execTestSQL(t, db, `CREATE TRIGGER fail_recurring_reserve_reversal BEFORE INSERT ON box_virtual_ledger FOR EACH ROW
		WHEN NEW.type = 'REVERSAL'
		BEGIN SELECT RAISE(ABORT, 'test_reserve_reversal_failpoint'); END`)

	var balanceBefore int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceBefore); err != nil {
		t.Fatalf("balance before: %v", err)
	}
	var ruleActive int
	if err := db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, fixture.ruleID).Scan(&ruleActive); err != nil {
		t.Fatalf("rule active: %v", err)
	}

	var ledgerIDsBefore []string
	ldRows, _ := db.Query(`SELECT id FROM box_virtual_ledger WHERE box_id=? ORDER BY id`, boxID)
	if ldRows != nil {
		defer ldRows.Close()
		for ldRows.Next() {
			var id string
			ldRows.Scan(&id)
			ledgerIDsBefore = append(ledgerIDsBefore, id)
		}
	}

	lc := captureLog(t)

	rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
	if rr.Code == http.StatusOK {
		lc.require("unreachable")
		t.Fatal("forced reserve reversal failure returned 200")
	}
	lc.require("test_reserve_reversal_failpoint")
	assertRecurringDeletePreserved(t, db, "ws-test", fixture.seriesID, fixture.ruleID, 3, selectedID, balanceBefore, ruleActive)
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 0)

	var ledgerIDsAfter []string
	ldRows2, _ := db.Query(`SELECT id FROM box_virtual_ledger WHERE box_id=? ORDER BY id`, boxID)
	if ldRows2 != nil {
		defer ldRows2.Close()
		for ldRows2.Next() {
			var id string
			ldRows2.Scan(&id)
			ledgerIDsAfter = append(ledgerIDsAfter, id)
		}
	}
	if len(ledgerIDsAfter) != len(ledgerIDsBefore) {
		t.Fatalf("ledger row count changed: before=%d after=%d", len(ledgerIDsBefore), len(ledgerIDsAfter))
	}
	for i := range ledgerIDsBefore {
		if ledgerIDsAfter[i] != ledgerIDsBefore[i] {
			t.Fatalf("ledger row diverged at %d: before=%s after=%s", i, ledgerIDsBefore[i], ledgerIDsAfter[i])
		}
	}

	var ledgerReversalCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE type='REVERSAL' AND source_transaction_id=?`, selectedID).Scan(&ledgerReversalCount); err != nil {
		t.Fatalf("count reversals: %v", err)
	}
	if ledgerReversalCount != 0 {
		t.Errorf("partial reversal persisted: %d reversal rows", ledgerReversalCount)
	}
}

func TestRecurringDeleteSingleFailureInTransferSidePreservesBothBalances(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	fixture := newRecurringDeleteFixture(t, db, "personal", "TRANSFER", true)
	selectedID := fixture.transactionID(t, 3)
	execTestSQL(t, db, `UPDATE transactions SET status='paid', paid_at=12345 WHERE id=?`, selectedID)
	execTestSQL(t, db, `UPDATE accounts SET current_balance=90000 WHERE id='checking-test'`)
	execTestSQL(t, db, `UPDATE accounts SET current_balance=65000 WHERE id='checking-delete-dest'`)

	execTestSQL(t, db, `CREATE TRIGGER fail_recurring_transfer_side BEFORE UPDATE ON accounts FOR EACH ROW
		WHEN NEW.id = 'checking-delete-dest'
		 AND (SELECT current_balance FROM accounts WHERE id = 'checking-test') = 100000
		BEGIN SELECT RAISE(ABORT, 'test_transfer_second_side_failpoint'); END`)

	var balanceOrigin, balanceDest int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceOrigin); err != nil {
		t.Fatalf("origin balance: %v", err)
	}
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-delete-dest'`).Scan(&balanceDest); err != nil {
		t.Fatalf("dest balance: %v", err)
	}
	var ruleActive int
	if err := db.QueryRow(`SELECT active FROM recurring_rules WHERE id=?`, fixture.ruleID).Scan(&ruleActive); err != nil {
		t.Fatalf("rule active: %v", err)
	}

	lc := captureLog(t)

	rr := recurringDeleteRequest(t, &fixture.handler, selectedID, "single")
	if rr.Code == http.StatusOK {
		lc.require("unreachable")
		t.Fatal("forced transfer side failure returned 200")
	}
	lc.require("test_transfer_second_side_failpoint")

	var balanceOriginAfter, balanceDestAfter int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceOriginAfter); err != nil {
		t.Fatalf("origin balance after: %v", err)
	}
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-delete-dest'`).Scan(&balanceDestAfter); err != nil {
		t.Fatalf("dest balance after: %v", err)
	}
	if balanceOriginAfter != balanceOrigin {
		t.Errorf("origin balance changed: before=%d after=%d", balanceOrigin, balanceOriginAfter)
	}
	if balanceDestAfter != balanceDest {
		t.Errorf("dest balance changed: before=%d after=%d", balanceDest, balanceDestAfter)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id=? AND workspace_id='ws-test'`, selectedID).Scan(&count); err != nil {
		t.Fatalf("count selected: %v", err)
	}
	if count != 1 {
		t.Errorf("transaction missing: count=%d", count)
	}
	assertRecurringSkip(t, db, "ws-test", fixture.seriesID, 3, 0)
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 1)

	var seriesActive int
	if err := db.QueryRow(`SELECT active FROM recurring_series WHERE workspace_id='ws-test' AND id=?`, fixture.seriesID).Scan(&seriesActive); err != nil {
		t.Fatalf("series active: %v", err)
	}
	if seriesActive != 1 {
		t.Errorf("series active = %d, want 1", seriesActive)
	}

	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-08-15")); err != nil {
		t.Fatalf("materializer after transfer side failure: %v", err)
	}
	assertRecurringSequenceCount(t, db, "ws-test", fixture.seriesID, 3, 1)
}
