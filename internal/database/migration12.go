package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// runCategoryMigration12 follows SQLite's generalized table-rebuild procedure:
// suspend FK actions BEFORE BEGIN on one reserved connection, validate before
// commit, then restore the connection's FK setting. Deferring FK checks alone
// does not suppress SET NULL/CASCADE when the historical SQL drops categories.
// No runtime/jobs may run on this migration connection. Other pool connections
// keep their own FK setting. No subsequent migration repairs historical losses.
func runCategoryMigration12(db *sql.DB, m migration) (retErr error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, conn.Close()) }()
	var original int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&original); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys = %d", original))
		var actual int
		if restoreErr == nil {
			restoreErr = conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&actual)
			if restoreErr == nil && actual != original {
				restoreErr = fmt.Errorf("foreign_keys setting was not restored")
			}
		}
		if restoreErr != nil {
			// Never return a connection with an unknown FK setting to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			retErr = errors.Join(retErr, fmt.Errorf("restore migration 12 connection: %w", restoreErr))
		}
	}()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	var disabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&disabled); err != nil {
		return err
	}
	if disabled != 0 {
		return fmt.Errorf("cannot suspend migration 12 FK actions")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			retErr = errors.Join(retErr, err)
		}
	}()
	// Acquire the write reservation before reading invariants; this also makes
	// the version recheck safe if another migrator finished while we waited.
	if _, err := tx.Exec(`UPDATE schema_migrations SET version=version WHERE version=12`); err != nil {
		return err
	}
	var applied int
	if err := tx.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version=12`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	var scratch int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE lower(name)='categories_new'`).Scan(&scratch); err != nil {
		return err
	}
	if scratch != 0 {
		return fmt.Errorf("migration 12 staging name already exists; preserve for inspection")
	}
	if err := checkMigration12ForeignKeys(tx); err != nil {
		return err
	}
	before, err := captureMigration12Data(tx)
	if err != nil {
		return err
	}
	objects, err := tx.Query(`SELECT name,sql FROM sqlite_schema WHERE tbl_name='categories' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		return err
	}
	type schemaObject struct{ name, sql string }
	var saved []schemaObject
	for objects.Next() {
		var obj schemaObject
		if err := objects.Scan(&obj.name, &obj.sql); err != nil {
			objects.Close()
			return err
		}
		saved = append(saved, obj)
	}
	if err := errors.Join(objects.Err(), objects.Close()); err != nil {
		return err
	}
	if err := m.up(tx); err != nil {
		return err
	}
	for _, obj := range saved {
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, obj.name).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			if _, err := tx.Exec(obj.sql); err != nil {
				return fmt.Errorf("restore category schema object: %w", err)
			}
		}
	}
	for _, snap := range before {
		after, err := migration12Digest(tx, snap.query)
		if err != nil {
			return err
		}
		if after != snap.digest {
			return fmt.Errorf("migration 12 changed historical content in %s", snap.table)
		}
	}
	if err := checkMigration12ForeignKeys(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,unixepoch())`, m.version, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

func checkMigration12ForeignKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("migration 12 foreign key validation failed; no changes committed")
	}
	return rows.Err()
}

type migration12Data struct {
	table, query string
	digest       [32]byte
}

// Compare full row contents (including legitimate NULLs), not merely FK checks
// or counts. These are v12's direct/transitive dependents and financial owners.
// Only SQL metadata supplies identifiers, and every identifier is quoted.
func captureMigration12Data(tx *sql.Tx) ([]migration12Data, error) {
	var snapshots []migration12Data
	for _, table := range []string{"categories", "transactions", "cost_limits", "recurring_rules", "boxes", "box_virtual_ledger", "accounts", "workspaces"} {
		rows, err := tx.Query(`SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
		if err != nil {
			return nil, err
		}
		var columns []string
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				rows.Close()
				return nil, err
			}
			columns = append(columns, `"`+table+`"."`+strings.ReplaceAll(col, `"`, `""`)+`"`)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if len(columns) == 0 {
			continue
		}
		projection := strings.Join(columns, ",")
		query := `SELECT ` + projection + ` FROM "` + table + `" ORDER BY ` + projection
		digest, err := migration12Digest(tx, query)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, migration12Data{table, query, digest})
	}
	return snapshots, nil
}

func migration12Digest(tx *sql.Tx, query string) ([32]byte, error) {
	var result [32]byte
	rows, err := tx.Query(query)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return result, err
	}
	h := sha256.New()
	encoder := json.NewEncoder(h)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return result, err
		}
		if err := encoder.Encode(values); err != nil {
			return result, err
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	copy(result[:], h.Sum(nil))
	return result, nil
}
