package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func openLockingRuntimeDBsAt(t *testing.T, path, dsnSuffix string) *database.RuntimeDBs {
	t.Helper()
	dbs, err := database.OpenRuntimeDBs("file:" + path + dsnSuffix)
	if err != nil {
		t.Fatalf("open locking runtime DBs: %v", err)
	}
	t.Cleanup(func() {
		if err := dbs.Close(); err != nil {
			t.Fatalf("close locking runtime DBs: %v", err)
		}
	})
	return dbs
}

func openLockingToggleHandler(t *testing.T, dbs *database.RuntimeDBs) TransactionHandler {
	t.Helper()
	seedInvoicePaymentScenario(t, dbs.DB)
	return TransactionHandler{
		DB:          dbs.DB,
		WriterDB:    dbs.Immediate,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
}

func TestSQLiteDeferredReadThenWriteReturnsBusySnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deferred-busy-snapshot.db")
	dbs := openLockingRuntimeDBsAt(t, path, "")

	if _, err := dbs.DB.Exec(`
		CREATE TABLE snapshot_probe (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO snapshot_probe (id, value) VALUES (1, 'initial')
	`); err != nil {
		t.Fatalf("seed snapshot probe: %v", err)
	}

	ctx := context.Background()
	aTx, err := dbs.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("A deferred begin: %v", err)
	}
	defer aTx.Rollback()

	var value string
	if err := aTx.QueryRow(`SELECT value FROM snapshot_probe WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("A read: %v", err)
	}

	bTx, err := dbs.Immediate.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("B immediate begin: %v", err)
	}
	if _, err := bTx.Exec(`UPDATE snapshot_probe SET value = 'b' WHERE id = 1`); err != nil {
		t.Fatalf("B write: %v", err)
	}
	if err := bTx.Commit(); err != nil {
		t.Fatalf("B commit: %v", err)
	}

	_, writeErr := aTx.Exec(`UPDATE snapshot_probe SET value = 'a' WHERE id = 1`)
	if writeErr == nil {
		t.Fatal("expected SQLITE_BUSY_SNAPSHOT when deferred reader promotes to writer after concurrent commit")
	}
	var sqliteErr *sqliteDriver.Error
	if !errors.As(writeErr, &sqliteErr) {
		t.Fatalf("write error = %v, want wrapped *sqlite.Error", writeErr)
	}
	if got := sqliteErr.Code(); got != sqlite3.SQLITE_BUSY_SNAPSHOT {
		t.Fatalf("sqlite code = %d, want %d (%v)", got, sqlite3.SQLITE_BUSY_SNAPSHOT, writeErr)
	}
	if !sqliteContention(writeErr) {
		t.Fatalf("sqliteContention(%v) = false, want true for extended BUSY snapshot", writeErr)
	}
}

func TestHandleTogglePagamentoImmediateSerializesDoublePaidIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "double-paid.db")
	dbs := openLockingRuntimeDBsAt(t, path, "")
	h := openLockingToggleHandler(t, dbs)
	insertPaymentDateTestTransaction(t, dbs.DB, "tx-double-paid", "pending", nil)

	aAcquired := make(chan struct{})
	releaseA := make(chan struct{})
	var once sync.Once
	h.testHooks = &recurringEditTestHooks{afterWriterBegin: func() {
		once.Do(func() {
			close(aAcquired)
			<-releaseA
		})
	}}

	type toggleResult struct {
		label string
		rr    *httptest.ResponseRecorder
	}
	run := func(label string, values url.Values) <-chan toggleResult {
		done := make(chan toggleResult, 1)
		go func() {
			rr := httptest.NewRecorder()
			h.HandleTogglePagamento(rr, formPost("/transacoes/tx-double-paid/status-pagamento", values), "tx-double-paid")
			done <- toggleResult{label: label, rr: rr}
		}()
		return done
	}

	aDone := run("A", url.Values{"payment_action": {"paid"}, "payment_date": {"2026-07-14"}})
	select {
	case <-aAcquired:
	case <-time.After(10 * time.Second):
		t.Fatal("A never acquired the immediate writer")
	}

	baselineWaits := dbs.Immediate.Stats().WaitCount
	bDone := run("B", url.Values{"payment_action": {"paid"}})

	waitDeadline := time.Now().Add(10 * time.Second)
	for dbs.Immediate.Stats().WaitCount <= baselineWaits {
		if time.Now().After(waitDeadline) {
			t.Fatal("B did not enter the immediate writer pool wait queue")
		}
		runtime.Gosched()
	}
	select {
	case got := <-bDone:
		t.Fatalf("B completed before A committed: code=%d body=%q", got.rr.Code, got.rr.Body.String())
	default:
	}

	close(releaseA)

	for _, done := range []<-chan toggleResult{aDone, bDone} {
		select {
		case got := <-done:
			if got.rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body = %q", got.label, got.rr.Code, got.rr.Body.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("toggle request did not finish after writer release")
		}
	}

	var status string
	var paidAt sql.NullInt64
	var invoiceID sql.NullString
	if err := dbs.DB.QueryRow(`SELECT status, paid_at, invoice_id FROM transactions WHERE id = 'tx-double-paid'`).Scan(&status, &paidAt, &invoiceID); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q, want paid", status)
	}
	if !paidAt.Valid || paidAt.Int64 != testAppCivilDate("2026-07-14") {
		t.Fatalf("paid_at = %v/%d, want %d from first confirmation", paidAt.Valid, paidAt.Int64, testAppCivilDate("2026-07-14"))
	}
	if invoiceID.Valid {
		t.Fatalf("invoice_id = %q, want NULL", invoiceID.String)
	}

	var balance int64
	if err := dbs.DB.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balance); err != nil {
		t.Fatalf("query balance: %v", err)
	}
	if balance != 87655 {
		t.Fatalf("balance = %d, want 87655 (single balance effect)", balance)
	}

	var historyCount int
	if err := dbs.DB.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-double-paid'`).Scan(&historyCount); err != nil {
		t.Fatalf("query status history: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("status history count = %d, want exactly 1", historyCount)
	}
}

func TestHandleTogglePagamentoPreCommitBusyReturns503(t *testing.T) {
	path := filepath.Join(t.TempDir(), "precommit-busy.db")
	dbs := openLockingRuntimeDBsAt(t, path, "?_pragma=busy_timeout(50)")
	h := openLockingToggleHandler(t, dbs)
	insertPaymentDateTestTransaction(t, dbs.DB, "tx-busy-precommit", "pending", nil)

	extDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open external writer: %v", err)
	}
	t.Cleanup(func() { _ = extDB.Close() })
	extTx, err := extDB.Begin()
	if err != nil {
		t.Fatalf("external writer begin: %v", err)
	}
	defer extTx.Rollback()
	if _, err := extTx.Exec(`UPDATE accounts SET updated_at = updated_at WHERE id = 'checking-test'`); err != nil {
		t.Fatalf("external writer lock: %v", err)
	}

	probeTx, probeErr := dbs.Immediate.BeginTx(context.Background(), nil)
	if probeErr == nil {
		probeTx.Rollback()
		t.Fatal("expected immediate begin to observe the external writer lock")
	}
	var sqliteErr *sqliteDriver.Error
	if !errors.As(probeErr, &sqliteErr) {
		t.Fatalf("probe error = %v, want *sqlite.Error", probeErr)
	}
	if got := sqliteErr.Code(); got != sqlite3.SQLITE_BUSY {
		t.Fatalf("probe sqlite code = %d, want %d (%v)", got, sqlite3.SQLITE_BUSY, probeErr)
	}
	if !sqliteContention(probeErr) {
		t.Fatalf("sqliteContention(%v) = false, want true", probeErr)
	}

	before := balanceMutationSnapshot(t, dbs.DB)

	rr := httptest.NewRecorder()
	h.HandleTogglePagamento(rr, formPost("/transacoes/tx-busy-precommit/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}), "tx-busy-precommit")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if after := balanceMutationSnapshot(t, dbs.DB); before != after {
		t.Fatal("pre-commit BUSY response persisted a financial mutation")
	}
}

func TestHandleTogglePagamentoCancellationWhileWaitingForPoolHasZeroEffect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel-waiting-pool.db")
	dbs := openLockingRuntimeDBsAt(t, path, "")
	h := openLockingToggleHandler(t, dbs)
	insertPaymentDateTestTransaction(t, dbs.DB, "tx-cancel-pool", "pending", nil)

	heldTx, err := dbs.Immediate.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("hold immediate writer connection: %v", err)
	}
	defer heldTx.Rollback()

	baselineWaits := dbs.Immediate.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	req := formPost("/transacoes/tx-cancel-pool/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}).WithContext(ctx)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.HandleTogglePagamento(rr, req, "tx-cancel-pool")
		close(done)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for dbs.Immediate.Stats().WaitCount <= baselineWaits {
		if time.Now().After(deadline) {
			t.Fatal("handler did not wait for the occupied immediate writer pool")
		}
		runtime.Gosched()
	}
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled handler did not return")
	}

	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAt sql.NullInt64
	if err := dbs.DB.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = 'tx-cancel-pool'`).Scan(&status, &paidAt); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "pending" || paidAt.Valid {
		t.Fatalf("transaction = %q/%v, want pending with NULL paid_at", status, paidAt.Valid)
	}
	var historyCount int
	if err := dbs.DB.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-cancel-pool'`).Scan(&historyCount); err != nil {
		t.Fatalf("query status history: %v", err)
	}
	if historyCount != 0 {
		t.Fatalf("status history count = %d, want 0", historyCount)
	}
}

func TestHandleTogglePagamentoCancellationAfterBeginReturns408(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel-after-begin.db")
	dbs := openLockingRuntimeDBsAt(t, path, "")
	h := openLockingToggleHandler(t, dbs)
	insertPaymentDateTestTransaction(t, dbs.DB, "tx-cancel-after-begin", "pending", nil)

	beginDone := make(chan struct{})
	proceed := make(chan struct{})
	h.testHooks = &recurringEditTestHooks{afterWriterBegin: func() {
		close(beginDone)
		<-proceed
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := formPost("/transacoes/tx-cancel-after-begin/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}).WithContext(ctx)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.HandleTogglePagamento(rr, req, "tx-cancel-after-begin")
		close(done)
	}()

	select {
	case <-beginDone:
	case <-time.After(10 * time.Second):
		t.Fatal("BeginTx did not complete before cancellation window")
	}
	cancel()
	close(proceed)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return after post-begin cancellation")
	}

	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want empty for cancellation", got)
	}

	var status string
	var paidAt sql.NullInt64
	if err := dbs.DB.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = 'tx-cancel-after-begin'`).Scan(&status, &paidAt); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "pending" || paidAt.Valid {
		t.Fatalf("transaction = %q/%v, want pending with NULL paid_at", status, paidAt.Valid)
	}

	var balance int64
	if err := dbs.DB.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balance); err != nil {
		t.Fatalf("query balance: %v", err)
	}
	if balance != 100000 {
		t.Fatalf("balance = %d, want unchanged 100000", balance)
	}

	var historyCount int
	if err := dbs.DB.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-cancel-after-begin'`).Scan(&historyCount); err != nil {
		t.Fatalf("query status history: %v", err)
	}
	if historyCount != 0 {
		t.Fatalf("status history count = %d, want 0", historyCount)
	}

	var invoiceCount int
	var invoiceStatus string
	var invoicePaidAmount int64
	if err := dbs.DB.QueryRow(`SELECT COUNT(1) FROM invoices`).Scan(&invoiceCount); err != nil {
		t.Fatalf("query invoice count: %v", err)
	}
	if err := dbs.DB.QueryRow(`SELECT status, COALESCE(paid_amount, 0) FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&invoiceStatus, &invoicePaidAmount); err != nil {
		t.Fatalf("query invoice state: %v", err)
	}
	if invoiceCount != 1 || invoiceStatus != "OPEN" || invoicePaidAmount != 0 {
		t.Fatalf("invoice state = count %d / %q / %d, want unchanged 1 / OPEN / 0", invoiceCount, invoiceStatus, invoicePaidAmount)
	}
}

func TestHandleTogglePagamentoPostCommitDoesNotRunFinancialWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-post-commit-writer.db")
	dbs := openLockingRuntimeDBsAt(t, path, "")
	h := openLockingToggleHandler(t, dbs)
	insertPaymentDateTestTransaction(t, dbs.DB, "tx-no-oob", "pending", nil)

	now := time.Now().Unix()
	execTestSQL(t, dbs.DB, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-adversarial', 'ws-test', 'Cartao Adversarial', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, dbs.DB, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES ('invoice-adversarial', 'card-adversarial', '2020-01', ?, ?, 'OPEN', 0, ?)
	`, now-2*86400, now-86400, now)

	req := formPost("/transacoes/tx-no-oob/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	})
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "/lancamentos")
	rr := httptest.NewRecorder()
	h.HandleTogglePagamento(rr, req, "tx-no-oob")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Refresh"); got != "true" {
		t.Fatalf("HX-Refresh = %q, want true", got)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rr.Body.String())
	}

	var status string
	if err := dbs.DB.QueryRow(`SELECT status FROM transactions WHERE id = 'tx-no-oob'`).Scan(&status); err != nil {
		t.Fatalf("query toggled transaction: %v", err)
	}
	if status != "paid" {
		t.Fatalf("toggle status = %q, want paid", status)
	}

	var invoiceStatus string
	if err := dbs.DB.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-adversarial'`).Scan(&invoiceStatus); err != nil {
		t.Fatalf("query adversarial invoice: %v", err)
	}
	if invoiceStatus != "OPEN" {
		t.Fatalf("adversarial invoice status = %q, want OPEN (post-commit financial writer ran)", invoiceStatus)
	}
}

var errForcedCommitFailure = errors.New("forced commit failure")

type commitFailDriver struct{ base driver.Driver }

func (d commitFailDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &commitFailConn{Conn: conn}, nil
}

type commitFailConn struct{ driver.Conn }

func (c *commitFailConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	beginner, ok := c.Conn.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("driver does not support BeginTx")
	}
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &commitFailTx{Tx: tx}, nil
}

type commitFailTx struct{ driver.Tx }

func (t *commitFailTx) Commit() error { return errForcedCommitFailure }

var registerCommitFailDriverOnce sync.Once

func openCommitFailWriterDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	registerCommitFailDriverOnce.Do(func() {
		sql.Register("mf-w1a-commit-fail", commitFailDriver{base: &sqliteDriver.Driver{}})
	})
	db, err := sql.Open("mf-w1a-commit-fail", "file:"+path+"?_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open commit-fail writer DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHandleTogglePagamentoCommitErrorReturnsIndeterminateOutcome(t *testing.T) {
	cases := []struct {
		name   string
		action url.Values
		status string
	}{
		{
			name:   "scheduled",
			action: url.Values{"payment_action": {"scheduled"}, "scheduled_payment_date": {"2026-07-18"}},
			status: "pending",
		},
		{
			name:   "idempotent_noop",
			action: url.Values{"payment_action": {"paid"}},
			status: "paid",
		},
		{
			name:   "paid",
			action: url.Values{"payment_action": {"paid"}, "payment_date": {"2026-07-14"}},
			status: "pending",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commit-fail.db")
			dbs := openLockingRuntimeDBsAt(t, path, "")
			seedInvoicePaymentScenario(t, dbs.DB)
			txID := "tx-commit-fail-" + tc.name
			var paidAt *int64
			if tc.status == "paid" {
				v := testAppCivilDate("2026-07-10")
				paidAt = &v
			}
			insertPaymentDateTestTransaction(t, dbs.DB, txID, tc.status, paidAt)

			h := TransactionHandler{
				DB:          dbs.DB,
				WriterDB:    openCommitFailWriterDB(t, path),
				Templates:   testPaidInvoiceMutationTemplates(),
				WorkspaceID: "ws-test",
				UserID:      "user-test",
			}
			req := formPost("/transacoes/"+txID+"/status-pagamento", tc.action)
			req.Header.Set("HX-Request", "true")
			rr := httptest.NewRecorder()
			h.HandleTogglePagamento(rr, req, txID)

			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429, body = %q", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("HX-Reswap"); got != "none" {
				t.Fatalf("HX-Reswap = %q, want none", got)
			}
			trigger := rr.Header().Get("HX-Trigger")
			if !strings.Contains(trigger, "mostrarAlerta") || !strings.Contains(trigger, "refreshFinancials") {
				t.Fatalf("HX-Trigger = %q, want mostrarAlerta and refreshFinancials", trigger)
			}
			if !strings.Contains(trigger, togglePagamentoIndeterminateMessage) {
				t.Fatalf("HX-Trigger = %q, want indeterminate message", trigger)
			}
			if strings.Contains(trigger, "mostrarSucesso") || strings.Contains(trigger, "closePaymentConfirmModal") {
				t.Fatalf("HX-Trigger = %q, must not carry success callbacks", trigger)
			}
			if got := rr.Header().Get("HX-Refresh"); got != "" {
				t.Fatalf("HX-Refresh = %q, want empty", got)
			}
			if got := rr.Header().Get("Retry-After"); got != "" {
				t.Fatalf("Retry-After = %q, want empty for indeterminate commit", got)
			}
			if rr.Body.Len() != 0 {
				t.Fatalf("body = %q, want empty", rr.Body.String())
			}
		})
	}
}
