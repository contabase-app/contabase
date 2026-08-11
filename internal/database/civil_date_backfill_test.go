package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCivilDateBackfillDryRunApplyAndIdempotency(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "civil-date-backfill.db"))
	backupDir := filepath.Join(t.TempDir(), "backups")
	seedCivilDateBackfillFixture(t, db)

	dryRun, err := DryRunCivilDateBackfill(context.Background(), db)
	if err != nil {
		t.Fatalf("dry-run civil date backfill: %v", err)
	}
	if dryRun.Applied {
		t.Fatal("dry-run report unexpectedly marked applied")
	}
	if dryRun.DivergenceTimezone != "America/Sao_Paulo" {
		t.Fatalf("divergence timezone = %q", dryRun.DivergenceTimezone)
	}
	assertCivilDateBackfillTableTotal(t, dryRun, "transactions", 3)
	assertCivilDateBackfillTableTotal(t, dryRun, "recurring_rules", 1)
	assertCivilDateBackfillTableTotal(t, dryRun, "recurring_materialization_state", 1)
	assertCivilDateBackfillTableTotal(t, dryRun, "invoice_payments", 1)
	assertCivilDateBackfillTableTotal(t, dryRun, "invoices", 1)

	dateDryRun := civilDateBackfillFieldReport(t, dryRun, "transactions", "date_civil")
	if dateDryRun.EligibleLegacy != 2 || dateDryRun.Candidates != 1 || dateDryRun.Filled != 0 || dateDryRun.AlreadyFilled != 1 || dateDryRun.LegacyNullOrZero != 1 {
		t.Fatalf("unexpected transactions.date_civil dry-run report: %+v", dateDryRun)
	}
	if dateDryRun.DivergentUTCAndSaoPaulo != 1 || dateDryRun.UTCHourDistribution[0] != 1 || dateDryRun.UTCHourDistribution[12] != 1 {
		t.Fatalf("unexpected transactions.date_civil characterization: %+v", dateDryRun)
	}
	if got := dryRun.Integrity.RemainingEligibleCivilNull; got != 11 {
		t.Fatalf("dry-run remaining eligible NULL = %d, want 11", got)
	}
	assertCivilDateBackfillIntegrity(t, dryRun, 11)
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-backfill")
	assertCivilDateValue(t, db, "transactions", "date_civil", "tx-existing", "1999-01-01")

	t.Logf("dry-run agregado: tabelas=%d campos=%d candidatos=%d divergencias_transactions_date=%d integrity=%s",
		len(dryRun.Tables), len(dryRun.Fields), dryRun.Integrity.RemainingEligibleCivilNull,
		dateDryRun.DivergentUTCAndSaoPaulo, dryRun.Integrity.IntegrityCheckAfter)

	applied, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, backupDir)
	if err != nil {
		t.Fatalf("apply civil date backfill: %v", err)
	}
	if !applied.Applied {
		t.Fatal("apply report not marked applied")
	}
	if applied.BackupPath == "" {
		t.Fatal("apply report missing backup path")
	}
	if _, statErr := os.Stat(applied.BackupPath); statErr != nil {
		t.Fatalf("backup file not found at %s: %v", applied.BackupPath, statErr)
	}
	if got := totalCivilDateBackfillFilled(applied); got != 11 {
		t.Fatalf("filled = %d, want 11", got)
	}
	assertCivilDateBackfillIntegrity(t, applied, 0)
	assertCivilDateValue(t, db, "transactions", "date_civil", "tx-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "transactions", "due_date_civil", "tx-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "transactions", "paid_date_civil", "tx-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "transactions", "scheduled_payment_date_civil", "tx-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "transactions", "date_civil", "tx-existing", "1999-01-01")
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-zero")
	assertCivilDateValue(t, db, "recurring_rules", "start_date_civil", "rule-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "recurring_rules", "starts_at_civil", "rule-backfill", "2026-07-12")
	assertNullCivilDate(t, db, "recurring_rules", "end_date_civil", "rule-backfill")
	assertCivilDateValue(t, db, "recurring_rules", "generated_until_civil", "rule-backfill", "2026-07-12")
	assertCivilDateValueByWorkspace(t, db, "recurring_materialization_state", "materialized_until_civil", "ws-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "invoice_payments", "paid_date_civil", "payment-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "invoices", "closing_date_civil", "invoice-backfill", "2026-07-12")
	assertCivilDateValue(t, db, "invoices", "due_date_civil", "invoice-backfill", "2026-07-12")
	assertCivilDateBackfillLegacyAndExcludedData(t, db)

	t.Logf("execucao agregada: preenchidos=%d restantes=%d transactions=%d->%d amount_groups=%d inalteradas=%t integrity=%s",
		totalCivilDateBackfillFilled(applied), applied.Integrity.RemainingEligibleCivilNull,
		applied.Integrity.TransactionCountBefore, applied.Integrity.TransactionCountAfter,
		applied.Integrity.TransactionAmountWorkspaceGroups, applied.Integrity.TransactionAmountsUnchanged,
		applied.Integrity.IntegrityCheckAfter)

	second, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, filepath.Join(t.TempDir(), "backups2"))
	if err != nil {
		t.Fatalf("second civil date backfill: %v", err)
	}
	if got := totalCivilDateBackfillFilled(second); got != 0 {
		t.Fatalf("second run filled = %d, want 0", got)
	}
	if got := second.Integrity.RemainingEligibleCivilNull; got != 0 {
		t.Fatalf("second run remaining = %d, want 0", got)
	}
	assertCivilDateValue(t, db, "transactions", "date_civil", "tx-existing", "1999-01-01")
	assertCivilDateBackfillLegacyAndExcludedData(t, db)
	t.Logf("segunda execucao: preenchidos=%d restantes=%d", totalCivilDateBackfillFilled(second), second.Integrity.RemainingEligibleCivilNull)
}

func TestCivilDateBackfillRejectsInvalidExistingCivilValueWithoutWriting(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "civil-date-invalid-existing.db"))
	seedCivilDateBackfillFixture(t, db)
	if _, err := db.Exec(`UPDATE transactions SET due_date_civil = '2026-7-12' WHERE id = 'tx-existing'`); err != nil {
		t.Fatalf("seed invalid civil value: %v", err)
	}

	_, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, filepath.Join(t.TempDir(), "backups"))
	if err == nil || !strings.Contains(err.Error(), "not strict YYYY-MM-DD") {
		t.Fatalf("apply error = %v, want strict civil date failure", err)
	}
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-backfill")
}

func TestCivilDateBackfillRejectsLegacyTimestampOutsideISOYearRangeWithoutWriting(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "civil-date-invalid-legacy.db"))
	seedCivilDateBackfillFixture(t, db)
	if _, err := db.Exec(`UPDATE transactions SET date = ? WHERE id = 'tx-backfill'`, int64(^uint64(0)>>1)); err != nil {
		t.Fatalf("seed out-of-range legacy timestamp: %v", err)
	}

	_, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, filepath.Join(t.TempDir(), "backups"))
	if err == nil || !strings.Contains(err.Error(), "outside strict ISO year range") {
		t.Fatalf("apply error = %v, want legacy timestamp range failure", err)
	}
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-backfill")
}

func TestCivilDateBackfillMaintenanceOpenDoesNotRunMigrations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "maintenance-open.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw fixture: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE marker (id INTEGER PRIMARY KEY)`); err != nil {
		raw.Close()
		t.Fatalf("create marker: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw fixture: %v", err)
	}

	maintenance, err := OpenExistingForMaintenance("file:" + dbPath)
	if err != nil {
		t.Fatalf("open existing for maintenance: %v", err)
	}
	defer maintenance.Close()
	var migrations int
	if err := maintenance.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&migrations); err != nil {
		t.Fatalf("inspect maintenance schema: %v", err)
	}
	if migrations != 0 {
		t.Fatalf("maintenance open created schema_migrations")
	}
}

func TestCivilDateBackfillReadOnlyOpenRejectsNonexistentFile(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "nonexistent.db")
	_, _, err := OpenExistingForMaintenanceReadOnly("file:" + nonexistent)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v, want 'does not exist'", err)
	}

	// Confirm no file was created.
	if _, statErr := os.Stat(nonexistent); statErr == nil {
		t.Fatal("nonexistent file was created")
	}
}

func TestCivilDateBackfillReadWriteOpenRejectsNonExistentFile(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "nonexistent-rw.db")
	_, _, err := OpenExistingForMaintenanceReadWrite("file:" + nonexistent)
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	if _, statErr := os.Stat(nonexistent); statErr == nil {
		t.Fatal("nonexistent file was created by rw opener")
	}
}

func TestCivilDateBackfillOpenRejectsModeRWC(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reject-rwc.db")
	raw, _ := sql.Open("sqlite", "file:"+dbPath)
	raw.Exec(`CREATE TABLE t(x)`)
	raw.Close()

	_, _, err := OpenExistingForMaintenanceReadOnly("file:" + dbPath + "?mode=rwc")
	if err == nil || !strings.Contains(err.Error(), "mode=rwc") {
		t.Fatalf("error = %v, want mode=rwc rejection", err)
	}

	_, _, err = OpenExistingForMaintenanceReadWrite("file:" + dbPath + "?mode=rwc")
	if err == nil || !strings.Contains(err.Error(), "mode=rwc") {
		t.Fatalf("error = %v, want mode=rwc rejection", err)
	}
}

func TestCivilDateBackfillOpenRejectsMemory(t *testing.T) {
	_, _, err := OpenExistingForMaintenanceReadOnly(":memory:")
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("error = %v, want :memory: rejection", err)
	}
	_, _, err = OpenExistingForMaintenanceReadWrite(":memory:")
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("error = %v, want :memory: rejection", err)
	}
}

func TestCivilDateBackfillMaintenanceReadOnlyBlocksWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "readonly-blocks.db")
	raw, _ := sql.Open("sqlite", "file:"+dbPath)
	raw.Exec(`CREATE TABLE t(x)`)
	raw.Close()

	db, _, err := OpenExistingForMaintenanceReadOnly("file:" + dbPath)
	if err != nil {
		t.Fatalf("open readonly: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`INSERT INTO t(x) VALUES (1)`)
	if err == nil {
		t.Fatal("write in readonly connection succeeded")
	}
	if !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("error = %v, want readonly error", err)
	}
}

func TestCivilDateBackfillConcurrentWriterBlocksApply(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concurrent.db")
	db, _ := openCivilDateBackfillTestDB(t, dbPath)
	seedCivilDateBackfillFixture(t, db)

	writer, err := sql.Open("sqlite", "file:"+dbPath+"?mode=rw")
	if err != nil {
		t.Fatalf("open concurrent writer: %v", err)
	}
	writer.SetMaxOpenConns(1)
	defer writer.Close()

	writerTx, err := writer.Begin()
	if err != nil {
		t.Fatalf("begin writer tx: %v", err)
	}
	if _, err := writerTx.Exec(`UPDATE schema_migrations SET name = name WHERE version = 52`); err != nil {
		writerTx.Rollback()
		t.Fatalf("hold write lock: %v", err)
	}

	dbForApply, applyPath, err := OpenExistingForMaintenanceReadWrite("file:" + dbPath)
	if err != nil {
		writerTx.Rollback()
		t.Fatalf("open for apply: %v", err)
	}
	defer dbForApply.Close()

	backupDir := filepath.Join(t.TempDir(), "backups-concurrent")
	_, err = ApplyCivilDateBackfill(context.Background(), dbForApply, applyPath, backupDir)
	if err == nil {
		writerTx.Rollback()
		t.Fatal("apply succeeded despite concurrent writer")
	}
	t.Logf("apply blocked by concurrent writer: %v", err)

	// No backup should exist after a failed attempt.
	entries, _ := os.ReadDir(backupDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			t.Errorf("backup .db file %s exists after blocked apply", e.Name())
		}
	}

	// Zero columns altered.
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-backfill")

	writerTx.Rollback()

	applied, err := ApplyCivilDateBackfill(context.Background(), dbForApply, applyPath, filepath.Join(t.TempDir(), "backups-ok"))
	if err != nil {
		t.Fatalf("apply after writer released: %v", err)
	}
	if !applied.Applied {
		t.Fatal("apply not marked applied after writer released")
	}
}

func TestCivilDateBackfillForcedRollbackOnLastFieldPreservesAllData(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "forced-rollback.db"))
	seedCivilDateBackfillFixture(t, db)

	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()

	// TRIGGER RAISE(ABORT) on the LAST field (invoices.due_date_civil).
	// This fires AFTER the first 11 fields have been UPDATEd, forcing a
	// transaction rollback that must undo all prior updates.
	if _, err := db.Exec(`
		CREATE TEMP TRIGGER civil_backfill_rollback_test
		BEFORE UPDATE OF due_date_civil ON invoices
		BEGIN
			SELECT RAISE(ABORT, 'injected rollback: invoices.due_date_civil');
		END
	`); err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}

	report, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, filepath.Join(t.TempDir(), "backups-rollback"))
	if err == nil {
		t.Fatal("expected rollback error from trigger, got nil")
	}
	if !strings.Contains(err.Error(), "injected rollback") && !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("error = %v, want injected rollback", err)
	}
	if report.Applied {
		t.Fatal("report.Applied is true after failed commit")
	}

	// All 12 legacy INTEGER values must be preserved byte-for-byte.
	type legacyCheck struct {
		table, keyColumn, keyValue, column string
		want                               int64
	}
	checks := []legacyCheck{
		{"transactions", "id", "tx-backfill", "date", midnight},
		{"transactions", "id", "tx-backfill", "due_date", noon},
		{"transactions", "id", "tx-backfill", "paid_at", midnight},
		{"transactions", "id", "tx-backfill", "scheduled_payment_at", noon},
		{"recurring_rules", "id", "rule-backfill", "start_date", midnight},
		{"recurring_rules", "id", "rule-backfill", "starts_at", noon},
		{"recurring_rules", "id", "rule-backfill", "end_date", 0},
		{"recurring_rules", "id", "rule-backfill", "generated_until", midnight},
		{"recurring_materialization_state", "workspace_id", "ws-backfill", "materialized_until", midnight},
		{"invoice_payments", "id", "payment-backfill", "paid_at", midnight},
		{"invoices", "id", "invoice-backfill", "closing_date", midnight},
		{"invoices", "id", "invoice-backfill", "due_date", noon},
	}
	for _, c := range checks {
		var val sql.NullInt64
		q := `SELECT ` + c.column + ` FROM ` + c.table + ` WHERE ` + c.keyColumn + ` = ?`
		if err := db.QueryRow(q, c.keyValue).Scan(&val); err != nil {
			t.Fatalf("read %s.%s: %v", c.table, c.column, err)
		}
		if !val.Valid || val.Int64 != c.want {
			t.Fatalf("%s.%s = valid=%v value=%d, want %d", c.table, c.column, val.Valid, val.Int64, c.want)
		}
	}

	// All civil columns remain NULL.
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "transactions", "due_date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "transactions", "paid_date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "transactions", "scheduled_payment_date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "start_date_civil", "rule-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "starts_at_civil", "rule-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "end_date_civil", "rule-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "generated_until_civil", "rule-backfill")
	assertNullCivilDateByWorkspace(t, db, "recurring_materialization_state", "materialized_until_civil", "ws-backfill")
	assertNullCivilDate(t, db, "invoice_payments", "paid_date_civil", "payment-backfill")
	assertNullCivilDate(t, db, "invoices", "closing_date_civil", "invoice-backfill")
	assertNullCivilDate(t, db, "invoices", "due_date_civil", "invoice-backfill")

	t.Logf("forced rollback on 12th field: %v | report.Applied=%t | all legacy INTs and civil NULLs preserved", err, report.Applied)
}

func TestCivilDateBackfillPreflightRejectsLegacyColumnWrongType(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-wrong-type.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(ddl); err != nil {
		raw.Close()
		t.Fatalf("ddl: %v", err)
	}
	if err := runMigrations(raw); err != nil {
		raw.Close()
		t.Fatalf("migrations: %v", err)
	}
	// Make transactions.date TEXT instead of INTEGER.
	if _, err := raw.Exec(`ALTER TABLE transactions RENAME COLUMN date TO date_old`); err != nil {
		raw.Close()
		t.Fatalf("rename date: %v", err)
	}
	if _, err := raw.Exec(`ALTER TABLE transactions ADD COLUMN date TEXT NOT NULL DEFAULT ''`); err != nil {
		raw.Close()
		t.Fatalf("add text date: %v", err)
	}
	raw.Close()

	dbForTest := openMigratedRawDB(t, dbPath)
	defer dbForTest.Close()

	_, err = DryRunCivilDateBackfill(context.Background(), dbForTest)
	if err == nil || !strings.Contains(err.Error(), "INTEGER") {
		t.Fatalf("error = %v, want INTEGER type enforcement for legacy column", err)
	}
}

func TestCivilDateBackfillBackupSameDBAccepted(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "backup-same.db"))
	seedCivilDateBackfillFixture(t, db)
	backupDir := filepath.Join(t.TempDir(), "backups-same")

	report, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, backupDir)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.BackupPath == "" {
		t.Fatal("no backup path in report")
	}

	// Verify backup exists, is readable, and passes checks.
	backupDB, err := sql.Open("sqlite", "file:"+report.BackupPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	backupDB.SetMaxOpenConns(1)

	rows, err := backupDB.Query(`PRAGMA integrity_check`)
	if err != nil {
		t.Fatalf("backup integrity: %v", err)
	}
	var result string
	for rows.Next() {
		rows.Scan(&result)
	}
	rows.Close()
	if result != "ok" {
		t.Fatalf("backup integrity_check = %q", result)
	}

	fkRows, err := backupDB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("backup fk: %v", err)
	}
	if fkRows.Next() {
		t.Fatal("backup has foreign key violations")
	}
	fkRows.Close()

	// Manifest exists.
	manifestPath := report.BackupPath + ".manifest"
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest not found: %v", err)
	}
}

func TestCivilDateBackfillApplyRequiresBackupDir(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "no-backup.db"))
	seedCivilDateBackfillFixture(t, db)

	_, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, "")
	if err == nil || !strings.Contains(err.Error(), "backup-dir") {
		t.Fatalf("error = %v, want backup-dir required", err)
	}
}

func TestCivilDateBackfillPreflightRejectsMissingTable(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "missing-table.db"))
	seedCivilDateBackfillFixture(t, db)
	if _, err := db.Exec(`DROP TABLE transactions`); err != nil {
		t.Fatalf("drop transactions: %v", err)
	}

	_, err := DryRunCivilDateBackfill(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "transactions") {
		t.Fatalf("error = %v, want missing table error", err)
	}
	_ = dbPath
}

func TestCivilDateBackfillPreflightRejectsMissingCivilColumn(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "missing-column.db"))
	seedCivilDateBackfillFixture(t, db)

	// Manually simulate missing civil column by corrupting migration record.
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 52`); err != nil {
		t.Fatalf("delete migration 52: %v", err)
	}

	_, err := DryRunCivilDateBackfill(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "not applied") {
		t.Fatalf("error = %v, want migration not applied error", err)
	}
	_ = dbPath
}

func TestCivilDateBackfillPreflightRejectsCivilColumnWrongType(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wrong-type.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(ddl); err != nil {
		raw.Close()
		t.Fatalf("ddl: %v", err)
	}
	if err := runMigrations(raw); err != nil {
		raw.Close()
		t.Fatalf("migrations: %v", err)
	}
	// Corrupt: make date_civil INTEGER instead of TEXT.
	if _, err := raw.Exec(`CREATE TABLE transactions_new AS SELECT * FROM transactions`); err != nil {
		raw.Close()
		t.Fatalf("copy transactions: %v", err)
	}
	if _, err := raw.Exec(`DROP TABLE transactions`); err != nil {
		raw.Close()
		t.Fatalf("drop transactions: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE transactions (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		account_id TEXT NOT NULL,
		type TEXT NOT NULL,
		amount INTEGER NOT NULL,
		date INTEGER NOT NULL,
		date_civil INTEGER
	)`); err != nil {
		raw.Close()
		t.Fatalf("create corrupted transactions: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date) SELECT id, workspace_id, user_id, account_id, type, amount, date FROM transactions_new`); err != nil {
		raw.Close()
		t.Fatalf("insert back: %v", err)
	}
	if _, err := raw.Exec(`DROP TABLE transactions_new`); err != nil {
		raw.Close()
		t.Fatalf("drop new: %v", err)
	}
	raw.Close()

	dbForTest := openMigratedRawDB(t, dbPath)
	defer dbForTest.Close()

	_, err = DryRunCivilDateBackfill(context.Background(), dbForTest)
	if err == nil || !strings.Contains(err.Error(), "TEXT") {
		t.Fatalf("error = %v, want TEXT type enforcement error", err)
	}
}

func TestCivilDateBackfillForeignKeyCheckRejectsBrokenDB(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "fk-check.db"))
	seedCivilDateBackfillFixture(t, db)

	// Disable foreign keys temporarily to insert a broken reference.
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	// Insert an invoice payment referencing a non-existent invoice.
	if _, err := db.Exec(`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, created_at) VALUES ('fk-broken', 'ws-backfill', 'nonexistent-inv', 'account-backfill', 500, 1, 1)`); err != nil {
		t.Fatalf("insert broken fk: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fk: %v", err)
	}

	_, err := DryRunCivilDateBackfill(context.Background(), db)
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("error = %v, want foreign_key_check failure", err)
	}
	_ = dbPath
}

func TestCivilDateBackfillRollbackPreservesAllElevenFields(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "rollback.db"))
	seedCivilDateBackfillFixture(t, db)

	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()

	// Capture all 12 legacy INTEGER values before.
	type legacyCheck struct {
		table, keyColumn, keyValue, column string
		want                               int64
	}
	checks := []legacyCheck{
		{"transactions", "id", "tx-backfill", "date", midnight},
		{"transactions", "id", "tx-backfill", "due_date", noon},
		{"transactions", "id", "tx-backfill", "paid_at", midnight},
		{"transactions", "id", "tx-backfill", "scheduled_payment_at", noon},
		{"recurring_rules", "id", "rule-backfill", "start_date", midnight},
		{"recurring_rules", "id", "rule-backfill", "starts_at", noon},
		{"recurring_rules", "id", "rule-backfill", "end_date", 0},
		{"recurring_rules", "id", "rule-backfill", "generated_until", midnight},
		{"recurring_materialization_state", "workspace_id", "ws-backfill", "materialized_until", midnight},
		{"invoice_payments", "id", "payment-backfill", "paid_at", midnight},
		{"invoices", "id", "invoice-backfill", "closing_date", midnight},
		{"invoices", "id", "invoice-backfill", "due_date", noon},
	}

	// Corrupt the last field (invoices.due_date_civil) so the UPDATE fails.
	if _, err := db.Exec(`ALTER TABLE invoices RENAME COLUMN due_date_civil TO due_date_civil_old`); err != nil {
		t.Fatalf("rename due_date_civil: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE invoices ADD COLUMN due_date_civil INTEGER`); err != nil {
		t.Fatalf("add integer due_date_civil: %v", err)
	}

	_, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, filepath.Join(t.TempDir(), "backups-rollback"))
	if err == nil {
		t.Fatal("expected rollback error, got nil")
	}
	t.Logf("rollback error: %v", err)

	// Verify all 12 legacy INTEGER values are intact.
	for _, c := range checks {
		var val sql.NullInt64
		q := `SELECT ` + c.column + ` FROM ` + c.table + ` WHERE ` + c.keyColumn + ` = ?`
		if err := db.QueryRow(q, c.keyValue).Scan(&val); err != nil {
			t.Fatalf("read %s.%s: %v", c.table, c.column, err)
		}
		if !val.Valid || val.Int64 != c.want {
			t.Fatalf("%s.%s = valid=%v value=%d, want %d", c.table, c.column, val.Valid, val.Int64, c.want)
		}
	}

	// All civil columns must remain NULL.
	assertNullCivilDate(t, db, "transactions", "date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "transactions", "due_date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "transactions", "paid_date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "transactions", "scheduled_payment_date_civil", "tx-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "start_date_civil", "rule-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "starts_at_civil", "rule-backfill")
	assertNullCivilDate(t, db, "recurring_rules", "generated_until_civil", "rule-backfill")
}

func TestCivilDateBackfillPreservesScopeExcludedData(t *testing.T) {
	db, dbPath := openCivilDateBackfillTestDB(t, filepath.Join(t.TempDir(), "preserve-scope.db"))
	seedCivilDateBackfillFixture(t, db)
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()

	report, err := ApplyCivilDateBackfill(context.Background(), db, dbPath, filepath.Join(t.TempDir(), "backups-scope"))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !report.Applied {
		t.Fatal("apply not marked applied")
	}

	// invoices.paid_at untouched.
	var invoicePaidAt int64
	if err := db.QueryRow(`SELECT paid_at FROM invoices WHERE id = 'invoice-backfill'`).Scan(&invoicePaidAt); err != nil {
		t.Fatalf("read invoices.paid_at: %v", err)
	}
	if invoicePaidAt != midnight {
		t.Fatalf("invoices.paid_at = %d, want %d", invoicePaidAt, midnight)
	}

	// transaction_status_history untouched.
	var tshNewPaidAt int64
	if err := db.QueryRow(`SELECT new_paid_at FROM transaction_status_history WHERE id = 'history-backfill'`).Scan(&tshNewPaidAt); err != nil {
		t.Fatalf("read tsh.new_paid_at: %v", err)
	}
	if tshNewPaidAt != midnight {
		t.Fatalf("tsh.new_paid_at = %d, want %d", tshNewPaidAt, midnight)
	}
	var tshPrevPaidAt sql.NullInt64
	if err := db.QueryRow(`SELECT previous_paid_at FROM transaction_status_history WHERE id = 'history-backfill'`).Scan(&tshPrevPaidAt); err != nil {
		t.Fatalf("read tsh.previous_paid_at: %v", err)
	}
	if tshPrevPaidAt.Valid && tshPrevPaidAt.Int64 != 0 {
		t.Fatalf("tsh.previous_paid_at = %d, want 0", tshPrevPaidAt.Int64)
	}

	// boxes.target_date untouched.
	var boxTargetDate int64
	if err := db.QueryRow(`SELECT target_date FROM boxes WHERE id = 'box-backfill'`).Scan(&boxTargetDate); err != nil {
		t.Fatalf("read boxes.target_date: %v", err)
	}
	if boxTargetDate != midnight {
		t.Fatalf("boxes.target_date = %d, want %d", boxTargetDate, midnight)
	}

	// Transaction amount preserved.
	var txAmount int64
	if err := db.QueryRow(`SELECT amount FROM transactions WHERE id = 'tx-backfill'`).Scan(&txAmount); err != nil {
		t.Fatalf("read tx amount: %v", err)
	}
	if txAmount != 1000 {
		t.Fatalf("tx amount = %d, want 1000", txAmount)
	}

	// Invoice links preserved.
	var txInvID sql.NullString
	if err := db.QueryRow(`SELECT invoice_id FROM transactions WHERE id = 'tx-backfill'`).Scan(&txInvID); err != nil {
		t.Fatalf("read tx invoice_id: %v", err)
	}
	if !txInvID.Valid || txInvID.String != "invoice-backfill" {
		t.Fatalf("tx invoice_id = %q", txInvID.String)
	}

	// Recurrence links preserved.
	var txRuleID sql.NullString
	if err := db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id = 'tx-backfill'`).Scan(&txRuleID); err != nil {
		t.Fatalf("read tx rule_id: %v", err)
	}
	if !txRuleID.Valid || txRuleID.String != "rule-backfill" {
		t.Fatalf("tx rule_id = %q", txRuleID.String)
	}

	var txSeriesID sql.NullString
	if err := db.QueryRow(`SELECT series_id FROM transactions WHERE id = 'tx-backfill'`).Scan(&txSeriesID); err != nil {
		t.Fatalf("read tx series_id: %v", err)
	}
	if !txSeriesID.Valid || txSeriesID.String != "series-backfill" {
		t.Fatalf("tx series_id = %q", txSeriesID.String)
	}

	// Workspace link preserved.
	var txWsID string
	if err := db.QueryRow(`SELECT workspace_id FROM transactions WHERE id = 'tx-backfill'`).Scan(&txWsID); err != nil {
		t.Fatalf("read tx workspace_id: %v", err)
	}
	if txWsID != "ws-backfill" {
		t.Fatalf("tx workspace_id = %q", txWsID)
	}

	// No invoice_payments were dropped.
	var ipCount int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments`).Scan(&ipCount); err != nil {
		t.Fatalf("count invoice_payments: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count = %d, want 1", ipCount)
	}
}

// openCivilDateBackfillTestDB opens a fully migrated test database and returns (db, dbPath).
func openCivilDateBackfillTestDB(t *testing.T, dbPath string) (*sql.DB, string) {
	t.Helper()
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+absPath)
	if err != nil {
		t.Fatalf("open civil date backfill database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("execute civil date backfill DDL: %v", err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		t.Fatalf("run civil date backfill migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, absPath
}

func seedCivilDateBackfillFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO users (id, name, email, password_hash) VALUES ('user-backfill', 'Fixture', 'fixture-backfill@example.test', 'hash'), ('user-backfill-2', 'Fixture 2', 'fixture-backfill-2@example.test', 'hash')`},
		{query: `INSERT INTO workspaces (id, name, type) VALUES ('ws-backfill', 'Fixture', 'personal'), ('ws-backfill-2', 'Fixture 2', 'business')`},
		{query: `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-backfill', 'user-backfill', 'ADMIN'), ('ws-backfill-2', 'user-backfill-2', 'ADMIN')`},
		{query: `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('account-backfill', 'ws-backfill', 'Fixture', 'CHECKING'), ('account-backfill-2', 'ws-backfill-2', 'Fixture 2', 'CHECKING')`},
		{query: `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-backfill', 'ws-backfill', 'user-backfill')`},
		{query: `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, starts_at, end_date, generated_until)
			VALUES ('rule-backfill', 'ws-backfill', 'user-backfill', 'account-backfill', 'EXPENSE', 1000, ?, 'MONTHLY', 'series-backfill', ?, 0, ?)`, args: []any{midnight, noon, midnight}},
		{query: `INSERT INTO recurring_materialization_state (workspace_id, materialized_until) VALUES ('ws-backfill', ?)`, args: []any{midnight}},
		{query: `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at)
			VALUES ('invoice-backfill', 'account-backfill', '2026-07', ?, ?, 'PAID', ?)`, args: []any{midnight, noon, midnight}},
		{query: `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, due_date, paid_at, scheduled_payment_at, recurring_rule_id, series_id)
			VALUES ('tx-backfill', 'ws-backfill', 'user-backfill', 'account-backfill', 'invoice-backfill', 'EXPENSE', 1000, ?, ?, ?, ?, 'rule-backfill', 'series-backfill')`, args: []any{midnight, noon, midnight, noon}},
		{query: `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil)
			VALUES ('tx-existing', 'ws-backfill-2', 'user-backfill-2', 'account-backfill-2', 'INCOME', 2500, ?, '1999-01-01')`, args: []any{noon}},
		{query: `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date)
			VALUES ('tx-zero', 'ws-backfill-2', 'user-backfill-2', 'account-backfill-2', 'INCOME', 500, 0)`},
		{query: `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
			VALUES ('payment-backfill', 'ws-backfill', 'invoice-backfill', 'account-backfill', 'tx-backfill', 1000, ?, 'manual', ?)`, args: []any{midnight, midnight}},
		{query: `INSERT INTO transaction_status_history (id, transaction_id, workspace_id, user_id, previous_status, new_status, previous_paid_at, new_paid_at, created_at)
			VALUES ('history-backfill', 'tx-backfill', 'ws-backfill', 'user-backfill', 'pending', 'paid', 0, ?, ?)`, args: []any{midnight, midnight}},
		{query: `INSERT INTO boxes (id, workspace_id, name, target_amount, target_date) VALUES ('box-backfill', 'ws-backfill', 'Fixture', 1000, ?)`, args: []any{midnight}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed civil date backfill fixture: %v", err)
		}
	}
}

func assertCivilDateBackfillIntegrity(t *testing.T, report CivilDateBackfillReport, remaining int64) {
	t.Helper()
	i := report.Integrity
	if i.IntegrityCheckBefore != "ok" || i.IntegrityCheckAfter != "ok" {
		t.Fatalf("integrity checks = %q/%q", i.IntegrityCheckBefore, i.IntegrityCheckAfter)
	}
	if i.TransactionCountBefore != 3 || i.TransactionCountAfter != 3 {
		t.Fatalf("transaction counts = %d/%d", i.TransactionCountBefore, i.TransactionCountAfter)
	}
	if i.TransactionAmountWorkspaceGroups != 2 || !i.TransactionAmountsUnchanged {
		t.Fatalf("transaction amount integrity = groups %d unchanged %t", i.TransactionAmountWorkspaceGroups, i.TransactionAmountsUnchanged)
	}
	if i.TransactionInvoiceLinksBefore != 1 || i.TransactionInvoiceLinksAfter != 1 || i.InvoicePaymentInvoiceLinksBefore != 1 || i.InvoicePaymentInvoiceLinksAfter != 1 {
		t.Fatalf("invoice link counts changed: %+v", i)
	}
	if i.TransactionRuleLinksBefore != 1 || i.TransactionRuleLinksAfter != 1 || i.TransactionSeriesLinksBefore != 1 || i.TransactionSeriesLinksAfter != 1 {
		t.Fatalf("recurrence link counts changed: %+v", i)
	}
	if i.InvoicePaymentCountBefore != 1 || i.InvoicePaymentCountAfter != 1 {
		t.Fatalf("invoice payment counts = %d/%d", i.InvoicePaymentCountBefore, i.InvoicePaymentCountAfter)
	}
	if i.RemainingEligibleCivilNull != remaining {
		t.Fatalf("remaining eligible civil NULL = %d, want %d", i.RemainingEligibleCivilNull, remaining)
	}
}

func assertCivilDateBackfillLegacyAndExcludedData(t *testing.T, db *sql.DB) {
	t.Helper()
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	var date, dueDate, paidAt, scheduledAt int64
	if err := db.QueryRow(`SELECT date, due_date, paid_at, scheduled_payment_at FROM transactions WHERE id = 'tx-backfill'`).Scan(&date, &dueDate, &paidAt, &scheduledAt); err != nil {
		t.Fatalf("read transaction legacy dates: %v", err)
	}
	if date != midnight || dueDate != noon || paidAt != midnight || scheduledAt != noon {
		t.Fatalf("legacy transaction dates changed: %d %d %d %d", date, dueDate, paidAt, scheduledAt)
	}
	var invoicePaidAt int64
	if err := db.QueryRow(`SELECT paid_at FROM invoices WHERE id = 'invoice-backfill'`).Scan(&invoicePaidAt); err != nil || invoicePaidAt != midnight {
		t.Fatalf("invoices.paid_at changed: value=%d err=%v", invoicePaidAt, err)
	}
	var historyPaidAt, boxTargetDate int64
	if err := db.QueryRow(`SELECT new_paid_at FROM transaction_status_history WHERE id = 'history-backfill'`).Scan(&historyPaidAt); err != nil {
		t.Fatalf("read transaction_status_history: %v", err)
	}
	if err := db.QueryRow(`SELECT target_date FROM boxes WHERE id = 'box-backfill'`).Scan(&boxTargetDate); err != nil {
		t.Fatalf("read box target date: %v", err)
	}
	if historyPaidAt != midnight || boxTargetDate != midnight {
		t.Fatalf("excluded dates changed: history=%d box=%d", historyPaidAt, boxTargetDate)
	}
}

func civilDateBackfillFieldReport(t *testing.T, report CivilDateBackfillReport, table, civilColumn string) CivilDateBackfillFieldReport {
	t.Helper()
	for _, field := range report.Fields {
		if field.Table == table && field.CivilColumn == civilColumn {
			return field
		}
	}
	t.Fatalf("missing report for %s.%s", table, civilColumn)
	return CivilDateBackfillFieldReport{}
}

func assertCivilDateBackfillTableTotal(t *testing.T, report CivilDateBackfillReport, table string, want int64) {
	t.Helper()
	for _, item := range report.Tables {
		if item.Table == table {
			if item.TotalRows != want {
				t.Fatalf("%s rows = %d, want %d", table, item.TotalRows, want)
			}
			return
		}
	}
	t.Fatalf("missing table report for %s", table)
}

func totalCivilDateBackfillFilled(report CivilDateBackfillReport) int64 {
	var total int64
	for _, field := range report.Fields {
		total += field.Filled
	}
	return total
}

func assertCivilDateValue(t *testing.T, db *sql.DB, table, column, id, want string) {
	t.Helper()
	var got sql.NullString
	query := `SELECT ` + column + ` FROM ` + table + ` WHERE id = ?`
	if err := db.QueryRow(query, id).Scan(&got); err != nil {
		t.Fatalf("read %s.%s: %v", table, column, err)
	}
	if !got.Valid || got.String != want {
		t.Fatalf("%s.%s = %q valid=%t, want %q", table, column, got.String, got.Valid, want)
	}
}

func assertCivilDateValueByWorkspace(t *testing.T, db *sql.DB, table, column, workspaceID, want string) {
	t.Helper()
	var got sql.NullString
	query := `SELECT ` + column + ` FROM ` + table + ` WHERE workspace_id = ?`
	if err := db.QueryRow(query, workspaceID).Scan(&got); err != nil {
		t.Fatalf("read %s.%s: %v", table, column, err)
	}
	if !got.Valid || got.String != want {
		t.Fatalf("%s.%s = %q valid=%t, want %q", table, column, got.String, got.Valid, want)
	}
}

func assertNullCivilDate(t *testing.T, db *sql.DB, table, column, id string) {
	t.Helper()
	var got sql.NullString
	query := `SELECT ` + column + ` FROM ` + table + ` WHERE id = ?`
	if err := db.QueryRow(query, id).Scan(&got); err != nil {
		t.Fatalf("read %s.%s: %v", table, column, err)
	}
	if got.Valid {
		t.Fatalf("%s.%s = %q, want NULL", table, column, got.String)
	}
}

func assertNullCivilDateByWorkspace(t *testing.T, db *sql.DB, table, column, workspaceID string) {
	t.Helper()
	var got sql.NullString
	query := `SELECT ` + column + ` FROM ` + table + ` WHERE workspace_id = ?`
	if err := db.QueryRow(query, workspaceID).Scan(&got); err != nil {
		t.Fatalf("read %s.%s: %v", table, column, err)
	}
	if got.Valid {
		t.Fatalf("%s.%s = %q, want NULL", table, column, got.String)
	}
}
