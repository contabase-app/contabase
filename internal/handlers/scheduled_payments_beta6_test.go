package handlers

import (
	"context"
	"database/sql"
	"testing"

	sqlite3 "modernc.org/sqlite/lib"
)

// Reproduce the exact plain BUSY branch that the snapshot-only retry cannot
// handle. Both pools have the production D5 connector enabled on schema 77.
func TestScheduledPaymentBeta6DeferredUpgradeWithActiveWriter(t *testing.T) {
	reader, writer := openInvoicePaymentReversalFileDBs(t)
	row, now := seedStaleScheduledPayment(t, reader, "personal")
	var other *sql.Tx
	defer func() {
		if other != nil {
			other.Rollback()
		}
	}()
	ok, err := executeScheduledPaymentAttempt(t.Context(), reader, row, now, func() error {
		var e error
		other, e = writer.BeginTx(t.Context(), nil)
		if e != nil {
			return e
		}
		_, e = other.ExecContext(t.Context(), `UPDATE transactions SET description='competing job' WHERE workspace_id=? AND id=?`, row.WorkspaceID, row.ID)
		return e
	})
	if ok {
		t.Fatal("deferred upgrade committed under active competing writer")
	}
	requireInvoicePaymentReversalSQLiteCode(t, err, sqlite3.SQLITE_BUSY)
	if err := other.Rollback(); err != nil {
		t.Fatal(err)
	}
	other = nil
	assertScheduledAccountBalances(t, reader, 100000, 50000)
	assertScheduledAttemptEffects(t, reader, row.ID, false)
	// The production fix supplies the Immediate pool. Reload, debit, history and
	// notification still use one transaction; a second run has no effect.
	if n, err := RunDueScheduledPayments(context.Background(), writer, now); err != nil || n != 1 {
		t.Fatalf("execution=%d err=%v", n, err)
	}
	assertScheduledAccountBalances(t, reader, 90000, 50000)
	assertScheduledAttemptEffects(t, reader, row.ID, true)
	if n, err := RunDueScheduledPayments(context.Background(), writer, now); err != nil || n != 0 {
		t.Fatalf("replay=%d err=%v", n, err)
	}
	assertScheduledAccountBalances(t, reader, 90000, 50000)
	assertScheduledAttemptEffects(t, reader, row.ID, true)
}
