package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
)

// RunExperimentalInvestmentAdmission78 is an explicit, offline maintenance
// adapter, intentionally absent from runMigrations and from CLI/startup. The
// caller must own this dedicated raw maintenance pool exclusively, with all
// runtime pools/jobs closed. A disk target requires a new, verified backup.
// Disposable in-memory SQLite fixtures have no disk backup requirement.
func RunExperimentalInvestmentAdmission78(ctx context.Context, maintenance *sql.DB, backupPath string) error {
	return runExperimentalAdmission78(ctx, maintenance, backupPath, nil)
}

func runExperimentalAdmission78(ctx context.Context, db *sql.DB, backupPath string, checkpoint func(string, *sql.Tx) error) (retErr error) {
	if db == nil || db.Stats().MaxOpenConnections != 1 {
		return fmt.Errorf("migration 78 requires exclusive maintenance ownership")
	}
	var path string
	if err := db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return err
	}
	if path != "" {
		if backupPath == "" {
			return fmt.Errorf("migration 78 requires verified backup")
		}
		if err := CreateSQLiteBackupFileContext(ctx, db, path, backupPath); err != nil {
			return err
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Closing sql.Conn alone returns it to the pool. D3 requires the
		// maintenance connection itself to be discarded after read-back.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		if e := conn.Close(); e != nil && !errors.Is(e, sql.ErrConnDone) {
			retErr = errors.Join(retErr, e)
		}
	}()
	if err = conn.Raw(func(raw any) error {
		if _, guarded := raw.(*sqliteGuardConn); guarded {
			return fmt.Errorf("migration 78 requires a separate maintenance connection")
		}
		return nil
	}); err != nil {
		return err
	}
	// This is the only mechanical FK suspension in the candidate. Nothing
	// financial/admission-related runs before commit while FK actions are off.
	defer func() {
		_, e := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON; PRAGMA defer_foreign_keys=OFF`)
		if e == nil {
			e = checkAdmission78Pragmas(context.Background(), conn, 1)
		}
		if e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			retErr = errors.Join(retErr, e)
		}
	}()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF; PRAGMA defer_foreign_keys=OFF`); err != nil {
		return err
	}
	if err = checkAdmission78Pragmas(ctx, conn, 0); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		e := tx.Rollback()
		if e != nil && !errors.Is(e, sql.ErrTxDone) {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			retErr = errors.Join(retErr, fmt.Errorf("migration 78 rollback failed: %w", e))
		}
	}()
	// Take the writer before checking schema/version or enumerating owners.
	if _, err = tx.ExecContext(ctx, `UPDATE schema_migrations SET version=version WHERE version=77`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version != 77 {
		return fmt.Errorf("migration 78 requires schema 77; preserve existing candidate for inspection")
	}
	var partial int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='investment_adoption_admissions' OR name LIKE 'investment_admission_%' OR name LIKE 'trg_admission_%' OR name LIKE 'idx_admission_%'`).Scan(&partial); err != nil {
		return err
	}
	if partial != 0 {
		return fmt.Errorf("migration 78 reserved objects already exist; preserve for inspection")
	}
	if err = checkMigration12ForeignKeys(tx); err != nil {
		return fmt.Errorf("migration 78 invalid source foreign keys: %w", err)
	}
	cols, err := transactionV1PhysicalColumns(ctx, tx)
	if err != nil {
		return err
	}
	// CONTROL-PLANE GLOBAL: owners are discovered only from workspaces.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return err
	}
	var owners []string
	for rows.Next() {
		var w string
		if err = rows.Scan(&w); err != nil {
			break
		}
		owners = append(owners, w)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var sourceDDL string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&sourceDDL); err != nil {
		return err
	}
	create, err := admission78ReplacementDDL(sourceDDL)
	if err != nil {
		return err
	}
	// Resource estimate for a full copy plus WAL, including new key/index
	// records. This is a maintenance reserve, not a financial capacity limit.
	if path != "" {
		var pages uint64
		if err = tx.QueryRowContext(ctx, `SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&pages); err != nil {
			return err
		}
		need := pages
		for _, w := range owners {
			var n uint64
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM transactions WHERE workspace_id=?`, w).Scan(&n); err != nil {
				return err
			}
			need += n * uint64(128+len(w))
		}
		free, err := admission78FreeBytes(path)
		if err != nil {
			return err
		}
		if free < 2*need {
			return fmt.Errorf("migration 78 insufficient copy/WAL space")
		}
	}
	type object struct {
		kind, name, ddl string
		columns         []string
	}
	objects, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema WHERE sql IS NOT NULL AND (type IN ('trigger','view') OR (type='index' AND tbl_name='transactions')) ORDER BY rowid`)
	if err != nil {
		return err
	}
	var saved []object
	for objects.Next() {
		var o object
		if err = objects.Scan(&o.kind, &o.name, &o.ddl); err != nil {
			break
		}
		saved = append(saved, o)
	}
	err = errors.Join(err, objects.Err(), objects.Close())
	if err != nil {
		return err
	}
	for i := range saved {
		if saved[i].kind == "view" {
			saved[i].columns, err = admission78ViewColumns(ctx, tx, saved[i].name)
			if err != nil {
				return err
			}
		}
	}
	step := func(name string) error {
		if checkpoint != nil {
			return checkpoint(name, tx)
		}
		return nil
	}
	if _, err = tx.ExecContext(ctx, create); err != nil {
		return err
	}
	if err = step("create"); err != nil {
		return err
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteAdmissionIdentifier(c)
	}
	projection := strings.Join(quoted, ",")
	for _, w := range owners {
		if _, err = tx.ExecContext(ctx, `INSERT INTO transactions_admission78_new(rowid,`+projection+`) SELECT rowid,`+projection+` FROM transactions WHERE workspace_id=? ORDER BY rowid`, w); err != nil {
			return err
		}
		// Exact bijection, including rowids, type and TEXT/BLOB bytes. Neither
		// counts nor a hash stands in for equality of the historical payload.
		var differences []string
		for _, c := range cols {
			a, b := "s."+quoteAdmissionIdentifier(c), "d."+quoteAdmissionIdentifier(c)
			differences = append(differences, `typeof(`+a+`) IS NOT typeof(`+b+`) OR `+a+` IS NOT `+b+` OR (typeof(`+a+`) IN ('text','blob') AND hex(CAST(`+a+` AS BLOB)) IS NOT hex(CAST(`+b+` AS BLOB)))`)
		}
		var invalid int
		q := `SELECT EXISTS(SELECT 1 FROM transactions s LEFT JOIN transactions_admission78_new d ON d.workspace_id=?1 AND d.rowid=s.rowid WHERE s.workspace_id=?1 AND (d.rowid IS NULL OR ` + strings.Join(differences, " OR ") + `)) OR EXISTS(SELECT 1 FROM transactions_admission78_new d WHERE d.workspace_id=?1 AND NOT EXISTS(SELECT 1 FROM transactions s WHERE s.workspace_id=?1 AND s.rowid=d.rowid))`
		if err = tx.QueryRowContext(ctx, q, w).Scan(&invalid); err != nil {
			return err
		}
		if invalid != 0 {
			return fmt.Errorf("migration 78 copy integrity")
		}
	}
	if err = step("copy"); err != nil {
		return err
	}
	// Remove dependent SQL while swapping only this table. Never rename the
	// old parent: SQLite must not rewrite any child FK to a staging name.
	for _, o := range saved {
		if o.kind == "trigger" || o.kind == "view" {
			if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+quoteAdmissionIdentifier(o.name)); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE transactions; ALTER TABLE transactions_admission78_new RENAME TO transactions`); err != nil {
		return err
	}
	if err = step("swap"); err != nil {
		return err
	}
	for _, o := range saved {
		if o.name == "financial_transactions" && o.kind == "view" {
			o.ddl = `CREATE VIEW financial_transactions AS SELECT ` + projection + ` FROM transactions WHERE effect_mode='STANDARD'`
		}
		if _, err = tx.ExecContext(ctx, o.ddl); err != nil {
			return fmt.Errorf("migration 78 restore dependency: %w", err)
		}
	}
	for _, o := range saved {
		if o.kind == "view" {
			cols, err := admission78ViewColumns(ctx, tx, o.name)
			if err != nil {
				return err
			}
			if !stringSlicesEqual(cols, o.columns) {
				return fmt.Errorf("migration 78 unsupported dependent view projection")
			}
		}
	}
	if err = step("dependencies"); err != nil {
		return err
	}
	if err = migrateInvestmentAdoptionAdmission(tx); err != nil {
		return err
	}
	if err = step("candidate"); err != nil {
		return err
	}
	if err = checkMigration12ForeignKeys(tx); err != nil {
		return err
	}
	var integrity string
	if err = tx.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("migration 78 database integrity")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name) VALUES(78,'investment_adoption_admission')`); err != nil {
		return err
	}
	if err = step("marker"); err != nil {
		return err
	}
	if err = validateAdmission78Tx(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return fmt.Errorf("migration 78 commit uncertain; inspect durable marker/schema before retry: %w", err)
	}
	// Return only after read-back, never resume jobs or restore a DB here.
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON; PRAGMA defer_foreign_keys=OFF`); err != nil {
		return err
	}
	if err = checkAdmission78Pragmas(ctx, conn, 1); err != nil {
		return err
	}
	return validateAdmission78Reopen(ctx, conn)
}

func checkAdmission78Pragmas(ctx context.Context, conn *sql.Conn, fk int) error {
	for _, p := range []struct {
		name string
		want int
	}{{"foreign_keys", fk}, {"defer_foreign_keys", 0}} {
		var v int
		if err := conn.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&v); err != nil || v != p.want {
			return errSQLiteWriteGuard
		}
	}
	return nil
}

func admission78ReplacementDDL(original string) (string, error) {
	// Preserve verbatim every original column/constraint clause. Inserting a
	// column before table constraints preserves the physical order of all 37.
	start, end := strings.Index(original, "("), strings.LastIndex(original, ")")
	if start < 0 || end <= start || strings.Contains(strings.ToUpper(original[end:]), "WITHOUT") {
		return "", fmt.Errorf("unsupported transactions table")
	}
	body := original[start+1 : end]
	// Schema 77's explicit table constraints start with its workspace FK.
	pos := strings.Index(strings.ToUpper(body), "FOREIGN KEY")
	if pos < 0 {
		return "", fmt.Errorf("unsupported transactions constraints")
	}
	key := `admission_occurrence_key TEXT COLLATE BINARY NOT NULL DEFAULT (lower(hex(randomblob(16)))) CHECK(typeof(admission_occurrence_key)='text' AND length(admission_occurrence_key)=32 AND length(CAST(admission_occurrence_key AS BLOB))=32 AND admission_occurrence_key NOT GLOB '*[^0-9a-f]*'), `
	return `CREATE TABLE transactions_admission78_new (` + body[:pos] + key + body[pos:] + `, UNIQUE(workspace_id,admission_occurrence_key))` + original[end+1:], nil
}

// Validate persisted evidence on every reopen before publishing either pool.
// No recapture: mismatches keep the runtime unavailable.
func validateAdmission78Reopen(ctx context.Context, conn *sql.Conn) error {
	var applied int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='investment_adoption_admissions' AND type='table'`).Scan(&applied); err != nil {
		return err
	}
	var registry, marker int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&registry); err != nil {
		return err
	}
	if registry != 0 {
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=78`).Scan(&marker); err != nil {
			return err
		}
	}
	if applied != marker {
		return errSQLiteWriteGuard
	}
	if applied == 0 {
		return nil
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateAdmission78Tx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// The same structural and relational proof runs before the upgrade commit and
// on reopen. A malformed/partial candidate can never commit a version marker.
func validateAdmission78Tx(ctx context.Context, tx *sql.Tx) error {
	var err error
	queries := admission78ConsistencyQueries()
	if len(queries) != 9 {
		return errSQLiteWriteGuard
	}
	for _, ddl := range append(append(append(admissionHeaderDDL(), admissionSealDDL()...), admissionIndexDDL()...), admissionEvidenceSQL()...) {
		w := strings.Fields(ddl)
		i := 2
		if w[1] == "UNIQUE" {
			i = 3
		}
		if w[i] == "IF" {
			i += 3
		}
		name := strings.Split(w[i], "(")[0]
		var stored string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&stored); err != nil {
			return errSQLiteWriteGuard
		}
		normalize := func(s string) string {
			return strings.Join(strings.Fields(strings.Replace(s, "IF NOT EXISTS ", "", 1)), " ")
		}
		if normalize(stored) != normalize(ddl) {
			return errSQLiteWriteGuard
		}
	}
	if err = checkMigration12ForeignKeys(tx); err != nil {
		return errSQLiteWriteGuard
	}
	// CONTROL-PLANE GLOBAL: no business data selects the owners.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return err
	}
	var owners []string
	for rows.Next() {
		var w string
		if err = rows.Scan(&w); err != nil {
			break
		}
		owners = append(owners, w)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, w := range owners {
		var badKey int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM transactions WHERE workspace_id=? AND (admission_occurrence_key IS NULL OR typeof(admission_occurrence_key)!='text' OR length(admission_occurrence_key)!=32 OR length(CAST(admission_occurrence_key AS BLOB))!=32 OR admission_occurrence_key GLOB '*[^0-9a-f]*'))`, w).Scan(&badKey); err != nil || badKey != 0 {
			return errSQLiteWriteGuard
		}
		r, err := tx.QueryContext(ctx, `SELECT account_id FROM investment_adoption_admissions WHERE workspace_id=? ORDER BY account_id`, w)
		if err != nil {
			return err
		}
		var accounts []string
		for r.Next() {
			var a string
			if err = r.Scan(&a); err != nil {
				break
			}
			accounts = append(accounts, a)
		}
		err = errors.Join(err, r.Err(), r.Close())
		if err != nil {
			return err
		}
		for _, a := range accounts {
			for _, q := range queries {
				var invalid int
				if err = tx.QueryRowContext(ctx, q, w, a).Scan(&invalid); err != nil || invalid != 0 {
					return errSQLiteWriteGuard
				}
			}
		}
	}
	return nil
}

func admission78ConsistencyQueries() []string {
	var out []string
	for _, ddl := range admissionEvidenceSQL() {
		if !strings.Contains(ddl, "trg_admission_capture AFTER") {
			continue
		}
		const marker = "\n\tSELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS("
		parts := strings.Split(ddl, marker)
		for _, part := range parts[1:] {
			end := strings.Index(part, ";\n")
			// Split consumes the newline preceding the next SELECT marker.
			if end < 0 {
				end = strings.LastIndex(part, ";")
			}
			if end < 0 {
				return nil
			}
			q := "SELECT EXISTS(" + part[:end]
			q = strings.ReplaceAll(q, "NEW.workspace_id", "?1")
			q = strings.ReplaceAll(q, "NEW.account_id", "?2")
			out = append(out, q)
		}
	}
	return out
}

// Inspect only schema metadata; no unscoped financial SELECT is needed to
// preserve a view's public projection.
func admission78ViewColumns(ctx context.Context, tx *sql.Tx, name string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?) ORDER BY cid`, name)
	if err != nil {
		return nil, err
	}
	var cols []string
	for rows.Next() {
		var c string
		if err = rows.Scan(&c); err != nil {
			break
		}
		cols = append(cols, c)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	return cols, err
}
