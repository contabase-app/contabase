package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/contabase-app/contabase/internal/civildate"
)

const civilDateBackfillDivergenceTimezone = "America/Sao_Paulo"

type civilDateBackfillField struct {
	table        string
	keyColumn    string
	legacyColumn string
	civilColumn  string
}

var civilDateBackfillFields = []civilDateBackfillField{
	{table: "transactions", keyColumn: "id", legacyColumn: "date", civilColumn: "date_civil"},
	{table: "transactions", keyColumn: "id", legacyColumn: "due_date", civilColumn: "due_date_civil"},
	{table: "transactions", keyColumn: "id", legacyColumn: "paid_at", civilColumn: "paid_date_civil"},
	{table: "transactions", keyColumn: "id", legacyColumn: "scheduled_payment_at", civilColumn: "scheduled_payment_date_civil"},
	{table: "recurring_rules", keyColumn: "id", legacyColumn: "start_date", civilColumn: "start_date_civil"},
	{table: "recurring_rules", keyColumn: "id", legacyColumn: "starts_at", civilColumn: "starts_at_civil"},
	{table: "recurring_rules", keyColumn: "id", legacyColumn: "end_date", civilColumn: "end_date_civil"},
	{table: "recurring_rules", keyColumn: "id", legacyColumn: "generated_until", civilColumn: "generated_until_civil"},
	{table: "recurring_materialization_state", keyColumn: "workspace_id", legacyColumn: "materialized_until", civilColumn: "materialized_until_civil"},
	{table: "invoice_payments", keyColumn: "id", legacyColumn: "paid_at", civilColumn: "paid_date_civil"},
	{table: "invoices", keyColumn: "id", legacyColumn: "closing_date", civilColumn: "closing_date_civil"},
	{table: "invoices", keyColumn: "id", legacyColumn: "due_date", civilColumn: "due_date_civil"},
}

// CivilDateBackfillTableReport contains only aggregate counts. It intentionally
// carries no row identifiers or financial/user data.
type CivilDateBackfillTableReport struct {
	Table     string
	TotalRows int64
}

// CivilDateBackfillFieldReport characterizes one legacy/civil column pair.
// UTCHourDistribution uses indexes 0..23 for the legacy instant's UTC hour.
type CivilDateBackfillFieldReport struct {
	Table                      string
	LegacyColumn               string
	CivilColumn                string
	EligibleLegacy             int64
	Candidates                 int64
	Filled                     int64
	AlreadyFilled              int64
	LegacyNullOrZero           int64
	DivergentUTCAndSaoPaulo    int64
	UTCHourDistribution        [24]int64
	RemainingEligibleCivilNull int64
}

// CivilDateBackfillIntegrityReport records invariant counts before and after
// the transaction without exposing workspace identifiers or amounts.
type CivilDateBackfillIntegrityReport struct {
	IntegrityCheckBefore             string
	IntegrityCheckAfter              string
	TransactionCountBefore           int64
	TransactionCountAfter            int64
	TransactionAmountWorkspaceGroups int
	TransactionAmountsUnchanged      bool
	TransactionInvoiceLinksBefore    int64
	TransactionInvoiceLinksAfter     int64
	InvoicePaymentInvoiceLinksBefore int64
	InvoicePaymentInvoiceLinksAfter  int64
	TransactionRuleLinksBefore       int64
	TransactionRuleLinksAfter        int64
	TransactionSeriesLinksBefore     int64
	TransactionSeriesLinksAfter      int64
	InvoicePaymentCountBefore        int64
	InvoicePaymentCountAfter         int64
	RemainingEligibleCivilNull       int64
}

// CivilDateBackfillReport is safe to print: it contains schema names, modes,
// time-bucket counts and integrity booleans/counts only.
type CivilDateBackfillReport struct {
	Applied            bool
	DivergenceTimezone string
	BackupPath         string
	Tables             []CivilDateBackfillTableReport
	Fields             []CivilDateBackfillFieldReport
	Integrity          CivilDateBackfillIntegrityReport
}

// DryRunCivilDateBackfill analyzes the v52 columns without writing anything.
func DryRunCivilDateBackfill(ctx context.Context, db *sql.DB) (CivilDateBackfillReport, error) {
	return runCivilDateBackfill(ctx, db, "", false, "")
}

// ApplyCivilDateBackfill atomically fills only NULL v52 civil columns whose
// legacy counterpart is non-NULL and non-zero. Existing civil values are never
// overwritten. This function is not called by migrations or application boot.
//
// dbPath must be the canonical absolute path of the database file backing db.
// backupDir, if non-empty, triggers a WAL-safe backup creation before the apply
// transaction. The backup is validated with integrity + foreign-key checks and
// bound to dbPath via a manifest. If backupDir is empty, the call fails; an
// explicit backup directory is required for apply.
func ApplyCivilDateBackfill(ctx context.Context, db *sql.DB, dbPath, backupDir string) (CivilDateBackfillReport, error) {
	if strings.TrimSpace(backupDir) == "" {
		return CivilDateBackfillReport{}, fmt.Errorf("civil date backfill: --backup-dir is required for apply")
	}
	return runCivilDateBackfill(ctx, db, dbPath, true, backupDir)
}

func runCivilDateBackfill(ctx context.Context, db *sql.DB, dbPath string, apply bool, backupDir string) (report CivilDateBackfillReport, err error) {
	if db == nil {
		return report, fmt.Errorf("civil date backfill: nil database")
	}
	location, err := time.LoadLocation(civilDateBackfillDivergenceTimezone)
	if err != nil {
		return report, fmt.Errorf("civil date backfill: load divergence timezone: %w", err)
	}

	report.DivergenceTimezone = civilDateBackfillDivergenceTimezone

	// BeginTx on a _txlock=immediate connection acquires the write lock
	// BEFORE any snapshot or backup, eliminating the gap between backup
	// and apply. All subsequent work (backup, preflight, backfill, commit)
	// runs inside this single transaction.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("civil date backfill: begin transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if apply {
		backupPath, backupErr := createCivilDateBackfillBackupLocked(tx, dbPath, backupDir)
		if backupErr != nil {
			return report, fmt.Errorf("civil date backfill: backup: %w", backupErr)
		}
		report.BackupPath = backupPath
	}

	if err = validateCivilDateBackfillSchema(ctx, tx); err != nil {
		return report, err
	}
	if err = assertSQLiteIntegrity(ctx, tx); err != nil {
		return report, fmt.Errorf("civil date backfill preflight: %w", err)
	}

	before, err := captureCivilDateBackfillSnapshot(ctx, tx)
	if err != nil {
		return report, fmt.Errorf("civil date backfill: capture before snapshot: %w", err)
	}

	report.Tables, err = civilDateBackfillTableReports(ctx, tx)
	if err != nil {
		return report, err
	}

	for _, field := range civilDateBackfillFields {
		fieldReport, fieldErr := backfillCivilDateField(ctx, tx, field, location, apply)
		if fieldErr != nil {
			return report, fieldErr
		}
		report.Fields = append(report.Fields, fieldReport)
	}

	if err = assertSQLiteIntegrity(ctx, tx); err != nil {
		return report, fmt.Errorf("civil date backfill postflight: %w", err)
	}
	after, err := captureCivilDateBackfillSnapshot(ctx, tx)
	if err != nil {
		return report, fmt.Errorf("civil date backfill: capture after snapshot: %w", err)
	}
	if err = compareCivilDateBackfillSnapshots(before, after); err != nil {
		return report, err
	}

	remaining, err := countRemainingCivilDateBackfillCandidates(ctx, tx)
	if err != nil {
		return report, err
	}
	for i := range report.Fields {
		fieldRemaining, countErr := countRemainingCivilDateFieldCandidates(ctx, tx, civilDateBackfillFields[i])
		if countErr != nil {
			return report, countErr
		}
		report.Fields[i].RemainingEligibleCivilNull = fieldRemaining
	}
	if apply && remaining != 0 {
		return report, fmt.Errorf("civil date backfill: %d eligible civil value(s) remain NULL", remaining)
	}

	if err = tx.Commit(); err != nil {
		return report, fmt.Errorf("civil date backfill: commit: %w", err)
	}

	report.Applied = apply
	report.Integrity = civilDateBackfillIntegrityReport(before, after, remaining)
	return report, nil
}

// createCivilDateBackfillBackupLocked creates a WAL-safe backup while the
// caller's transaction holds a write lock (BEGIN IMMEDIATE). The lock
// guarantees no other writer can modify data between backup creation and
// the transaction's commit, so the backup represents the exact pre-backfill
// state.
func createCivilDateBackfillBackupLocked(tx *sql.Tx, dbPath, backupDir string) (string, error) {
	canonicalPath, err := filepath.Abs(backupDir)
	if err != nil {
		return "", fmt.Errorf("resolve backup dir: %w", err)
	}
	if err := os.MkdirAll(canonicalPath, 0755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	backupPath := filepath.Join(canonicalPath, fmt.Sprintf("civil-backfill-%s.db", time.Now().Format("20060102-150405")))
	if _, err := os.Stat(backupPath); err == nil {
		return "", fmt.Errorf("backup path %s already exists", backupPath)
	}

	// The caller's transaction holds a write lock (BEGIN IMMEDIATE via
	// _txlock=immediate). In WAL mode this does not block concurrent
	// readers. We use a second read-only connection to run VACUUM INTO,
	// which reads a consistent snapshot of all committed data (including
	// any WAL frames not yet checkpointed to the main file).
	vacuumDB, err := sql.Open("sqlite", "file:"+dbPath+"?mode=rw")
	if err != nil {
		return "", fmt.Errorf("open vacuum connection: %w", err)
	}
	vacuumDB.SetMaxOpenConns(1)
	if _, err := vacuumDB.Exec(fmt.Sprintf(`VACUUM INTO '%s'`, strings.ReplaceAll(backupPath, "'", "''"))); err != nil {
		vacuumDB.Close()
		return "", fmt.Errorf("vacuum into backup: %w", err)
	}
	vacuumDB.Close()

	// Validate backup from a separate read-only connection.
	backupDB, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("open backup for validation: %w", err)
	}
	backupDB.SetMaxOpenConns(1)
	defer backupDB.Close()

	if _, err := backupDB.Exec(`PRAGMA query_only = ON`); err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("set backup readonly: %w", err)
	}

	rows, err := backupDB.Query(`PRAGMA integrity_check`)
	if err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("backup integrity_check query: %w", err)
	}
	okCount := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			rows.Close()
			os.Remove(backupPath)
			return "", fmt.Errorf("backup integrity_check scan: %w", err)
		}
		if okCount == 0 && result == "ok" {
			okCount++
		}
	}
	if err := rows.Err(); err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("backup integrity_check rows: %w", err)
	}
	rows.Close()
	if okCount != 1 {
		os.Remove(backupPath)
		return "", fmt.Errorf("backup failed integrity_check")
	}

	fkRows, err := backupDB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("backup foreign_key_check query: %w", err)
	}
	if fkRows.Next() {
		fkRows.Close()
		os.Remove(backupPath)
		return "", fmt.Errorf("backup foreign_key_check found violations")
	}
	fkRows.Close()

	var schemaVersion int
	if err := backupDB.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		schemaVersion = 0
	}

	manifestPath := backupPath + ".manifest"
	manifest := fmt.Sprintf("source_db=%s\nbackup_path=%s\ncreated_at=%s\nmethod=sqlite_wal_checkpointed_copy\nschema_version=%d\nintegrity_check=ok\nforeign_key_check=ok\n",
		dbPath,
		backupPath,
		time.Now().Format(time.RFC3339),
		schemaVersion)
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		os.Remove(backupPath)
		return "", fmt.Errorf("write backup manifest: %w", err)
	}

	return backupPath, nil
}

type civilDateBackfillCandidate struct {
	key       string
	timestamp int64
}

func backfillCivilDateField(ctx context.Context, tx *sql.Tx, field civilDateBackfillField, location *time.Location, apply bool) (CivilDateBackfillFieldReport, error) {
	report := CivilDateBackfillFieldReport{
		Table:        field.table,
		LegacyColumn: field.legacyColumn,
		CivilColumn:  field.civilColumn,
	}
	query := fmt.Sprintf(`SELECT %s, %s, %s FROM %s`, field.keyColumn, field.legacyColumn, field.civilColumn, field.table)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return report, fmt.Errorf("civil date backfill: inspect %s.%s: %w", field.table, field.civilColumn, err)
	}

	var candidates []civilDateBackfillCandidate
	for rows.Next() {
		var key string
		var legacy sql.NullInt64
		var civil sql.NullString
		if err := rows.Scan(&key, &legacy, &civil); err != nil {
			rows.Close()
			return report, fmt.Errorf("civil date backfill: scan %s.%s: %w", field.table, field.civilColumn, err)
		}
		if civil.Valid {
			report.AlreadyFilled++
			if _, err := civildate.ParseCivilDateInput(civil.String); err != nil {
				rows.Close()
				return report, fmt.Errorf("civil date backfill: existing %s.%s is not strict YYYY-MM-DD", field.table, field.civilColumn)
			}
		}
		if !legacy.Valid || legacy.Int64 == 0 {
			report.LegacyNullOrZero++
			continue
		}

		report.EligibleLegacy++
		legacyInstant := time.Unix(legacy.Int64, 0).UTC()
		if legacyInstant.Year() < 1 || legacyInstant.Year() > 9999 {
			rows.Close()
			return report, fmt.Errorf("civil date backfill: %s.%s contains a timestamp outside strict ISO year range", field.table, field.legacyColumn)
		}
		report.UTCHourDistribution[legacyInstant.Hour()]++
		utcDate := civildate.LegacyUnixToCivilDateUTC(legacy.Int64)
		localDate, err := civildate.LegacyUnixToCivilDateInLocation(legacy.Int64, location)
		if err != nil {
			rows.Close()
			return report, fmt.Errorf("civil date backfill: characterize %s.%s: %w", field.table, field.legacyColumn, err)
		}
		if civildate.CompareCivilDate(utcDate, localDate) != 0 {
			report.DivergentUTCAndSaoPaulo++
		}
		if !civil.Valid {
			report.Candidates++
			candidates = append(candidates, civilDateBackfillCandidate{key: key, timestamp: legacy.Int64})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, fmt.Errorf("civil date backfill: iterate %s.%s: %w", field.table, field.civilColumn, err)
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("civil date backfill: close %s.%s rows: %w", field.table, field.civilColumn, err)
	}

	if !apply {
		return report, nil
	}
	update := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ? AND %s IS NULL AND %s IS NOT NULL AND %s <> 0`,
		field.table, field.civilColumn, field.keyColumn, field.civilColumn, field.legacyColumn, field.legacyColumn)
	for _, candidate := range candidates {
		value := civildate.FormatCivilDate(civildate.LegacyUnixToCivilDateUTC(candidate.timestamp))
		result, err := tx.ExecContext(ctx, update, value, candidate.key)
		if err != nil {
			return report, fmt.Errorf("civil date backfill: fill %s.%s: %w", field.table, field.civilColumn, err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return report, fmt.Errorf("civil date backfill: count %s.%s update: %w", field.table, field.civilColumn, err)
		}
		if rowsAffected != 1 {
			return report, fmt.Errorf("civil date backfill: expected one %s.%s update, got %d", field.table, field.civilColumn, rowsAffected)
		}
		report.Filled++
	}
	return report, nil
}

func validateCivilDateBackfillSchema(ctx context.Context, tx *sql.Tx) error {
	var applied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = 52 AND name = 'civil_date_columns'`).Scan(&applied); err != nil {
		return fmt.Errorf("civil date backfill: verify migration 52: %w", err)
	}
	if applied != 1 {
		return fmt.Errorf("civil date backfill: migration 52_civil_date_columns is not applied")
	}

	civilColumns := make(map[string]struct{})
	legacyColumns := make(map[string]struct{})
	for _, field := range civilDateBackfillFields {
		civilColumns[field.table+"."+field.civilColumn] = struct{}{}
		legacyColumns[field.table+"."+field.legacyColumn] = struct{}{}
	}

	required := make(map[string]map[string]struct{})
	for _, field := range civilDateBackfillFields {
		if required[field.table] == nil {
			required[field.table] = make(map[string]struct{})
		}
		required[field.table][field.keyColumn] = struct{}{}
		required[field.table][field.legacyColumn] = struct{}{}
		required[field.table][field.civilColumn] = struct{}{}
	}
	for table, columns := range required {
		exists, err := tableExists(tx, table)
		if err != nil {
			return fmt.Errorf("civil date backfill: check %s existence: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("civil date backfill: required table %s is missing", table)
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
		if err != nil {
			return fmt.Errorf("civil date backfill: inspect %s schema: %w", table, err)
		}
		found := make(map[string]struct{})
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("civil date backfill: scan %s schema: %w", table, err)
			}
			found[name] = struct{}{}

			fullName := table + "." + name
			if _, isCivil := civilColumns[fullName]; isCivil {
				if strings.ToUpper(columnType) != "TEXT" {
					rows.Close()
					return fmt.Errorf("civil date backfill: %s.%s type is %q, must be TEXT", table, name, columnType)
				}
				if notNull != 0 {
					rows.Close()
					return fmt.Errorf("civil date backfill: %s.%s is NOT NULL, must be nullable", table, name)
				}
				if defaultValue != nil {
					rows.Close()
					return fmt.Errorf("civil date backfill: %s.%s has DEFAULT value, must have no DEFAULT", table, name)
				}
			}
			if _, isLegacy := legacyColumns[fullName]; isLegacy {
				upper := strings.ToUpper(columnType)
				if upper != "INTEGER" && upper != "INT" {
					rows.Close()
					return fmt.Errorf("civil date backfill: %s.%s type is %q, must be INTEGER", table, name, columnType)
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("civil date backfill: iterate %s schema: %w", table, err)
		}
		rows.Close()
		for column := range columns {
			if _, ok := found[column]; !ok {
				return fmt.Errorf("civil date backfill: required column %s.%s is missing", table, column)
			}
		}
	}
	return nil
}

func assertSQLiteIntegrity(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return fmt.Errorf("integrity_check query: %w", err)
	}
	defer rows.Close()
	count := 0
	ok := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("integrity_check scan: %w", err)
		}
		count++
		ok = count == 1 && result == "ok"
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("integrity_check iterate: %w", err)
	}
	if count != 1 || !ok {
		return fmt.Errorf("integrity_check reported %d issue row(s)", count)
	}

	fkRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check query: %w", err)
	}
	defer fkRows.Close()
	if fkRows.Next() {
		return fmt.Errorf("foreign_key_check found violations")
	}
	if err := fkRows.Err(); err != nil {
		return fmt.Errorf("foreign_key_check iterate: %w", err)
	}
	return nil
}

type civilDateBackfillSnapshot struct {
	transactionCount           int64
	transactionAmounts         map[string]int64
	transactionInvoiceLinks    int64
	invoicePaymentInvoiceLinks int64
	transactionRuleLinks       int64
	transactionSeriesLinks     int64
	invoicePaymentCount        int64
}

func captureCivilDateBackfillSnapshot(ctx context.Context, tx *sql.Tx) (civilDateBackfillSnapshot, error) {
	var snapshot civilDateBackfillSnapshot
	counts := []struct {
		query string
		dest  *int64
	}{
		{query: `SELECT COUNT(1) FROM transactions`, dest: &snapshot.transactionCount},
		{query: `SELECT COUNT(1) FROM transactions WHERE invoice_id IS NOT NULL AND invoice_id <> ''`, dest: &snapshot.transactionInvoiceLinks},
		{query: `SELECT COUNT(1) FROM invoice_payments WHERE invoice_id IS NOT NULL AND invoice_id <> ''`, dest: &snapshot.invoicePaymentInvoiceLinks},
		{query: `SELECT COUNT(1) FROM transactions WHERE recurring_rule_id IS NOT NULL AND recurring_rule_id <> ''`, dest: &snapshot.transactionRuleLinks},
		{query: `SELECT COUNT(1) FROM transactions WHERE series_id IS NOT NULL AND series_id <> ''`, dest: &snapshot.transactionSeriesLinks},
		{query: `SELECT COUNT(1) FROM invoice_payments`, dest: &snapshot.invoicePaymentCount},
	}
	for _, count := range counts {
		if err := tx.QueryRowContext(ctx, count.query).Scan(count.dest); err != nil {
			return snapshot, err
		}
	}

	snapshot.transactionAmounts = make(map[string]int64)
	rows, err := tx.QueryContext(ctx, `SELECT workspace_id, COALESCE(SUM(amount), 0) FROM transactions GROUP BY workspace_id`)
	if err != nil {
		return snapshot, err
	}
	defer rows.Close()
	for rows.Next() {
		var workspaceID string
		var amount int64
		if err := rows.Scan(&workspaceID, &amount); err != nil {
			return snapshot, err
		}
		snapshot.transactionAmounts[workspaceID] = amount
	}
	if err := rows.Err(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func compareCivilDateBackfillSnapshots(before, after civilDateBackfillSnapshot) error {
	if before.transactionCount != after.transactionCount {
		return fmt.Errorf("civil date backfill: transaction count changed")
	}
	if !reflect.DeepEqual(before.transactionAmounts, after.transactionAmounts) {
		return fmt.Errorf("civil date backfill: transaction amount sums by workspace changed")
	}
	if before.transactionInvoiceLinks != after.transactionInvoiceLinks || before.invoicePaymentInvoiceLinks != after.invoicePaymentInvoiceLinks {
		return fmt.Errorf("civil date backfill: invoice link counts changed")
	}
	if before.transactionRuleLinks != after.transactionRuleLinks || before.transactionSeriesLinks != after.transactionSeriesLinks {
		return fmt.Errorf("civil date backfill: recurrence link counts changed")
	}
	if before.invoicePaymentCount != after.invoicePaymentCount {
		return fmt.Errorf("civil date backfill: invoice payment count changed")
	}
	return nil
}

func civilDateBackfillIntegrityReport(before, after civilDateBackfillSnapshot, remaining int64) CivilDateBackfillIntegrityReport {
	return CivilDateBackfillIntegrityReport{
		IntegrityCheckBefore:             "ok",
		IntegrityCheckAfter:              "ok",
		TransactionCountBefore:           before.transactionCount,
		TransactionCountAfter:            after.transactionCount,
		TransactionAmountWorkspaceGroups: len(before.transactionAmounts),
		TransactionAmountsUnchanged:      reflect.DeepEqual(before.transactionAmounts, after.transactionAmounts),
		TransactionInvoiceLinksBefore:    before.transactionInvoiceLinks,
		TransactionInvoiceLinksAfter:     after.transactionInvoiceLinks,
		InvoicePaymentInvoiceLinksBefore: before.invoicePaymentInvoiceLinks,
		InvoicePaymentInvoiceLinksAfter:  after.invoicePaymentInvoiceLinks,
		TransactionRuleLinksBefore:       before.transactionRuleLinks,
		TransactionRuleLinksAfter:        after.transactionRuleLinks,
		TransactionSeriesLinksBefore:     before.transactionSeriesLinks,
		TransactionSeriesLinksAfter:      after.transactionSeriesLinks,
		InvoicePaymentCountBefore:        before.invoicePaymentCount,
		InvoicePaymentCountAfter:         after.invoicePaymentCount,
		RemainingEligibleCivilNull:       remaining,
	}
}

func civilDateBackfillTableReports(ctx context.Context, tx *sql.Tx) ([]CivilDateBackfillTableReport, error) {
	seen := make(map[string]struct{})
	var reports []CivilDateBackfillTableReport
	for _, field := range civilDateBackfillFields {
		if _, ok := seen[field.table]; ok {
			continue
		}
		seen[field.table] = struct{}{}
		var total int64
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(1) FROM %s`, field.table)).Scan(&total); err != nil {
			return nil, fmt.Errorf("civil date backfill: count %s: %w", field.table, err)
		}
		reports = append(reports, CivilDateBackfillTableReport{Table: field.table, TotalRows: total})
	}
	return reports, nil
}

func countRemainingCivilDateBackfillCandidates(ctx context.Context, tx *sql.Tx) (int64, error) {
	var total int64
	for _, field := range civilDateBackfillFields {
		remaining, err := countRemainingCivilDateFieldCandidates(ctx, tx, field)
		if err != nil {
			return 0, err
		}
		total += remaining
	}
	return total, nil
}

func countRemainingCivilDateFieldCandidates(ctx context.Context, tx *sql.Tx, field civilDateBackfillField) (int64, error) {
	query := fmt.Sprintf(`SELECT COUNT(1) FROM %s WHERE %s IS NULL AND %s IS NOT NULL AND %s <> 0`,
		field.table, field.civilColumn, field.legacyColumn, field.legacyColumn)
	var count int64
	if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("civil date backfill: count remaining %s.%s: %w", field.table, field.civilColumn, err)
	}
	return count, nil
}
