//go:build contabase_legacy_fixtures

package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Only disposable fixtures. Neither test registers 78 in the startup runner.
func TestBetaRCMigrationResourceFailureRollback(t *testing.T) {
	for _, fault := range []string{"cancel-after-copy", "sqlite-full"} {
		t.Run(fault, func(t *testing.T) {
			fixture := brelLegacy(t)
			before := brelData(t, fixture)
			path := filepath.Join(t.TempDir(), "source.db")
			mustExecOK(t, fixture, `VACUUM INTO ?`, path)
			fixture.Close()
			db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			if fault == "sqlite-full" {
				pages := countQueryRows(t, db, `PRAGMA page_count`)
				mustExecOK(t, db, "PRAGMA max_page_count="+strconv.Itoa(pages))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backup := filepath.Join(t.TempDir(), "pre78.db")
			err = runExperimentalAdmission78(ctx, db, backup, func(stage string, _ *sql.Tx) error {
				if fault == "cancel-after-copy" && stage == "copy" {
					cancel()
					return ctx.Err()
				}
				return nil
			})
			if err == nil {
				t.Fatal("resource failure admitted migration")
			}
			if fault == "cancel-after-copy" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if fault == "sqlite-full" && !strings.Contains(err.Error(), "full") {
				t.Fatal(err)
			}
			if countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`) != 77 || !reflect.DeepEqual(before, brelData(t, db)) {
				t.Fatal("failed migration changed historical data/marker")
			}
			if schemaObjectExists(t, db, "table", "investment_adoption_admissions") {
				t.Fatal("partial candidate")
			}
			if err = validateSQLiteSnapshot(t.Context(), backup); err != nil {
				t.Fatal("backup invalid", err)
			}
			saved, err := sql.Open("sqlite", "file:"+backup+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			if !reflect.DeepEqual(before, brelData(t, saved)) {
				t.Fatal("backup differs from source")
			}
			t.Logf("MIGRATION_FAILURE fault=%s schema=77 backup=exact rollback=exact", fault)
		})
	}
}

// One reference capture, not the historical benchmark matrix. The 2s P4
// criterion is logged unchanged; exceeding it is evidence for human review.
func TestBetaRCReferenceCaptureResources(t *testing.T) {
	admissionPerfEnabled(t, "CB_BETA_RC_RESOURCES")
	const n = 100000
	db, path := brelPerfDisk(t, n, true)
	second, guard, err := newSQLiteGuardPool(sqliteDSNWithTxLock(normalizeSQLiteDSN("file:"+path), "immediate"))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err = publishSQLiteGuard(second, guard); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	reader.SetMaxOpenConns(1)
	defer reader.Close()
	readTx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var before int
	if err = readTx.QueryRow(`SELECT count(*) FROM transactions WHERE workspace_id='ws-1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	args := admissionArgsMut(t, nil)
	// Interrupt an actual capture, then verify every technical family is empty.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	start := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, captureErr := tx.ExecContext(ctx, admissionInsertStmt(), args...)
	_ = tx.Rollback()
	cancel()
	if captureErr == nil {
		t.Fatal("capture unexpectedly completed before cancellation")
	}
	for _, table := range []string{"investment_adoption_admissions", "investment_admission_attempts", "investment_admission_pin_transactions", "investment_admission_pin_null_transactions", "investment_admission_pin_valuations", "investment_admission_pin_purchases", "investment_admission_pin_withdrawals", "investment_admission_pin_allocations", "investment_admission_pin_items", "investment_admission_null_scopes", "investment_admission_null_transaction_witness"} {
		if countQueryRows(t, db, "SELECT count(*) FROM "+table+" WHERE workspace_id='ws-1'") != 0 {
			t.Fatal("cancellation left evidence", table)
		}
	}
	t.Logf("CANCEL_CAPTURE n=%d elapsed_ns=%d err=%v no_partial=true", n, time.Since(start), captureErr)
	start = time.Now()
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	acquired := time.Now()
	type writeResult struct {
		elapsed time.Duration
		err     error
	}
	done := make(chan writeResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		s := time.Now()
		_, err := second.ExecContext(ctx, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('rc-contender','ws-2','user-2','acc-inv2','EXPENSE',1,1,'2026-03-06')`)
		done <- writeResult{time.Since(s), err}
	}()
	if _, err = tx.ExecContext(t.Context(), admissionInsertStmt(), args...); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	other := <-done
	if other.err == nil {
		t.Fatal("timed-out competing writer committed")
	}
	if countQueryRows(t, db, `SELECT count(*) FROM transactions WHERE workspace_id='ws-2' AND id='rc-contender'`) != 0 {
		t.Fatal("competing writer left partial row")
	}
	var held int
	if err = readTx.QueryRow(`SELECT count(*) FROM transactions WHERE workspace_id='ws-1'`).Scan(&held); err != nil || held != before {
		t.Fatal("WAL reader lost snapshot", held, err)
	}
	if countQueryRows(t, db, `SELECT count(*) FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1' AND account_id='acc-inv'`) != n {
		t.Fatal("incomplete capture")
	}
	if countQueryRows(t, db, `SELECT count(*) FROM transactions WHERE workspace_id='ws-1'`) != before {
		t.Fatal("capture mutated source count")
	}
	var size, occurrences int64
	if err = db.QueryRow(`SELECT length(canonical_multiset),occurrence_count FROM investment_admission_null_transaction_witness WHERE workspace_id='ws-1' AND account_id='acc-inv'`).Scan(&size, &occurrences); err != nil || occurrences != n {
		t.Fatal("witness count", err)
	}
	wal := int64(0)
	if st, err := os.Stat(path + "-wal"); err == nil {
		wal = st.Size()
	}
	var pages int64
	if err = db.QueryRow(`SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	t.Logf("RC_CAPTURE n=%d total_ns=%d begin_ns=%d hold_ns=%d P4_limit_ns=2000000000 P4_within=%t envelope=%d db_pages_bytes=%d wal_bytes=%d rss_peak=%d temp_store=%d", n, elapsed, acquired.Sub(start), elapsed-acquired.Sub(start), elapsed <= 2*time.Second, size, pages, wal, brelPeakRSS(t), countQueryRows(t, db, `SELECT temp_store FROM pragma_temp_store`))
	t.Logf("RC_CONCURRENCY writer_elapsed_ns=%d writer_err=%v writer_partial=false wal_reader_snapshot=true", other.elapsed, other.err)
}

// Unlike the historical boundary fixture, this exercises the actual guarded
// disk pool, including its temp_store=MEMORY and durable WAL configuration.
func TestBetaRCLMemoryResources(t *testing.T) {
	admissionPerfEnabled(t, "CB_BETA_RC_L")
	const limit = 67108864
	db, path := brelPerfDisk(t, 0, true)
	mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	mustExecOK(t, db, `UPDATE transactions SET description='',amount=10 WHERE workspace_id='ws-1' AND rowid=201`)
	var cr0 int
	if err := db.QueryRow(`SELECT length(` + admissionCanonicalRow("t") + `) FROM transactions t WHERE t.workspace_id='ws-1' AND t.rowid=201`).Scan(&cr0); err != nil {
		t.Fatal(err)
	}
	size := func(n int) int {
		cr := cr0 + 2*n + len(strconv.Itoa(n)) - 1
		return len("CBMS|1|1|1|") + len(strconv.Itoa(cr)) + 1 + cr + len("1;")
	}
	lo, hi := 0, limit
	for lo < hi {
		mid := (lo + hi) / 2
		if size(mid) < limit {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if size(lo) != limit {
		t.Fatal("boundary calibration not exact")
	}
	mustExecOK(t, db, `UPDATE transactions SET description=? WHERE workspace_id='ws-1' AND rowid=201`, strings.Repeat("a", lo))
	start := time.Now()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), admissionInsertStmt(), admissionArgsMut(t, nil)...); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	var witness, sourceBytes int
	if err = db.QueryRow(`SELECT length(canonical_multiset) FROM investment_admission_null_transaction_witness WHERE workspace_id='ws-1' AND account_id='acc-inv'`).Scan(&witness); err != nil || witness != limit {
		t.Fatal("L capture size", witness, err)
	}
	if err = db.QueryRow(`SELECT length(description) FROM transactions WHERE workspace_id='ws-1' AND rowid=201 AND amount=10`).Scan(&sourceBytes); err != nil || sourceBytes != lo {
		t.Fatal("source changed", err)
	}
	if countQueryRows(t, db, `SELECT count(*) FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1' AND account_id='acc-inv'`) != 1 {
		t.Fatal("missing L pin")
	}
	var pages int64
	if err = db.QueryRow(`SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	wal := int64(0)
	if st, err := os.Stat(path + "-wal"); err == nil {
		wal = st.Size()
	}
	t.Logf("RC_L envelope=%d capture_ns=%d rss_peak=%d db_pages_bytes=%d wal_bytes=%d temp_store=%d", witness, elapsed, brelPeakRSS(t), pages, wal, countQueryRows(t, db, `SELECT temp_store FROM pragma_temp_store`))
}

func TestBetaRCMigrationResources(t *testing.T) {
	admissionPerfEnabled(t, "CB_BETA_RC_MIGRATION")
	db, path := brelPerfDisk(t, 100000, false)
	beforeCount := countQueryRows(t, db, `SELECT count(*) FROM transactions WHERE workspace_id='ws-1'`)
	var beforeBytes int64
	if err := db.QueryRow(`SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&beforeBytes); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	maintenance.SetMaxOpenConns(1)
	backup := filepath.Join(t.TempDir(), "pre78.db")
	start := time.Now()
	if err = RunExperimentalInvestmentAdmission78(t.Context(), maintenance, backup); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if err = maintenance.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RegisterLegacyInvestmentFixture(path); err != nil {
		t.Fatal(err)
	}
	pools, err := OpenLegacyInvestmentFixtureDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	var n, keys int
	if err = pools.DB.QueryRow(`SELECT count(*),count(DISTINCT admission_occurrence_key) FROM transactions WHERE workspace_id='ws-1'`).Scan(&n, &keys); err != nil || n != beforeCount || keys != n {
		t.Fatal("backfill", n, keys, err)
	}
	var afterBytes int64
	if err = pools.DB.QueryRow(`SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&afterBytes); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("RC_MIGRATION n=%d backup_and_migration_ns=%d db_before=%d db_after=%d backup_bytes=%d rss_peak=%d reopened_both_pools=true", n, elapsed, beforeBytes, afterBytes, st.Size(), brelPeakRSS(t))
}
