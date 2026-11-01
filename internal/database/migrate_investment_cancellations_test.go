package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func cancellation79Snapshot(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN('schema_migrations','investment_cancellations','investment_purchase_reversals','investment_record_cancellations') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = []string{strings.Join(cols, ",")}
		for rows.Next() {
			v := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range v {
				dest[i] = &v[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(v))
			for i, x := range v {
				parts[i] = fmt.Sprintf("%T:%#v", x, x)
			}
			out[table] = append(out[table], strings.Join(parts, "|"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(out[table][1:])
	}
	return out
}

func cancellation79Disk77(t *testing.T) (string, *sql.DB) {
	t.Helper()
	db := migration77Fixture(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)
	// NULL ids and extreme explicit rowids remain legal in schema 77.
	mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,notes) VALUES(-9223372036854775808,'ws-1','user-1','acc-checking','EXPENSE',1,1,x'00ff')`)
	path := filepath.Join(t.TempDir(), "source77.db")
	mustExecOK(t, db, `VACUUM INTO ?`, path)
	raw, err := sql.Open("sqlite", sqliteBackupURI(path, "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"))
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	t.Cleanup(func() { raw.Close() })
	return path, raw
}

func cancellation79TableIdentity(t *testing.T, db *sql.DB) string {
	t.Helper()
	var ddl string
	var page int
	if err := db.QueryRow(`SELECT sql,rootpage FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&ddl, &page); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT json_array(rowid,id,workspace_id,typeof(notes),hex(notes)) FROM transactions ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := fmt.Sprintf("%d|%s", page, ddl)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		result += "\n" + s
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMigration79AdditiveUpgradeFreshAndReopen(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fresh.db")
			var before map[string][]string
			var identity string
			if existing {
				var raw *sql.DB
				path, raw = cancellation79Disk77(t)
				before = cancellation79Snapshot(t, raw)
				identity = cancellation79TableIdentity(t, raw)
				raw.Close()
			}
			p, err := OpenRuntimeDBs(path)
			if err != nil {
				t.Fatal(err)
			}
			if countQueryRows(t, p.DB, `SELECT MAX(version) FROM schema_migrations`) != 82 || countQueryRows(t, p.DB, `SELECT count(*) FROM schema_migrations WHERE version IN(69,78)`) != 0 {
				t.Fatal("wrong startup registry")
			}
			if countQueryRows(t, p.DB, `SELECT count(*) FROM investment_cancellations`) != 0 {
				t.Fatal("migration created a cancellation")
			}
			if schemaObjectExists(t, p.DB, "table", "investment_adoption_admissions") || countQueryRows(t, p.DB, `SELECT count(*) FROM pragma_table_info('transactions') WHERE name='admission_occurrence_key'`) != 0 {
				t.Fatal("startup incorporated experimental admission")
			}
			if existing && (!reflect.DeepEqual(before, cancellation79Snapshot(t, p.DB)) || identity != cancellation79TableIdentity(t, p.DB)) {
				t.Fatal("migration changed existing rows or rebuilt transactions")
			}
			if err := sqlite79Integrity(p.DB); err != nil {
				t.Fatal(err)
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := ValidateSQLiteRuntimeSchema(path); err != nil {
				t.Fatal(err)
			}
			p, err = OpenRuntimeDBs(path)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if existing && !reflect.DeepEqual(before, cancellation79Snapshot(t, p.DB)) {
				t.Fatal("reopen changed financial data")
			}
		})
	}
}

func TestMigration79RejectsExperimental78AndPartialObjects(t *testing.T) {
	faults := map[string]string{
		"78":                     `INSERT INTO schema_migrations VALUES(78,'experimental',1)`,
		"78+79":                  `INSERT INTO schema_migrations VALUES(78,'experimental',1),(79,'investment_cancellation',1)`,
		"future":                 `INSERT INTO schema_migrations VALUES(800,'future',1)`,
		"reserved69":             `INSERT INTO schema_migrations VALUES(69,'reserved',1)`,
		"gap":                    `DELETE FROM schema_migrations WHERE version=60`,
		"marker-name":            `UPDATE schema_migrations SET name='wrong' WHERE version=77`,
		"experimental-table":     `CREATE TABLE investment_adoption_admissions(id TEXT)`,
		"experimental-column":    `ALTER TABLE transactions ADD COLUMN admission_occurrence_key TEXT`,
		"experimental-trigger":   `CREATE TRIGGER trg_admission_bad BEFORE INSERT ON transactions BEGIN SELECT 1;END`,
		"rebuild-residue":        `CREATE TABLE transactions_admission78_new(id TEXT)`,
		"partial79":              `CREATE TABLE investment_cancellations(id TEXT)`,
		"marker-only79":          `INSERT INTO schema_migrations VALUES(79,'investment_cancellation',1)`,
		"missing-parent-index":   `DROP INDEX idx_transactions_funding_owner`,
		"changed-parent-trigger": `DROP TRIGGER trg_investments_initial_purchase;CREATE TRIGGER trg_investments_initial_purchase AFTER INSERT ON investments BEGIN SELECT 1;END`,
		"fk-orphan":              `PRAGMA foreign_keys=OFF;DELETE FROM users WHERE id='user-1';PRAGMA foreign_keys=ON`,
	}
	for name, query := range faults {
		t.Run(name, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("DATA_DIR", data)
			path, raw := cancellation79Disk77(t)
			mustExecOK(t, raw, query)
			raw.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, single := range []bool{false, true} {
				if single {
					db, err := Open(path + "?_pragma=journal_mode(WAL)")
					if err == nil {
						db.Close()
						t.Fatal("Open accepted incompatible schema")
					}
				} else {
					p, err := OpenRuntimeDBs(path + "?_pragma=journal_mode(WAL)")
					if err == nil {
						p.Close()
						t.Fatal("runtime accepted incompatible schema")
					}
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("read-only refusal modified source bytes")
			}
			if _, err := os.Stat(filepath.Join(data, "backups")); !os.IsNotExist(err) {
				t.Fatal("rejected source reached backup")
			}
			for _, suffix := range []string{"-wal", "-shm"} {
				if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Fatalf("rejected source created %s", suffix)
				}
			}
		})
	}
}

func TestMigration79PreflightRevalidatesBeforeFirstWrite(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	path, raw := cancellation79Disk77(t)
	p, err := openRuntimeDBs(path, &sqliteRuntimeOpenTestHooks{beforeInitialize: func() error {
		_, err := raw.Exec(`INSERT INTO schema_migrations VALUES(78,'experimental',1)`)
		return err
	}})
	if err == nil {
		p.Close()
		t.Fatal("schema changed after read-only inspection was accepted")
	}
	if schemaObjectExists(t, raw, "table", "investment_cancellations") {
		t.Fatal("DDL preceded revalidation")
	}
	if countQueryRows(t, raw, `SELECT count(*) FROM schema_migrations WHERE version=79`) != 0 {
		t.Fatal("79 marker written")
	}
}

func TestMigration79BackupPrecedesDDLAndIncludesWAL(t *testing.T) {
	data := t.TempDir()
	t.Setenv("DATA_DIR", data)
	path, raw := cancellation79Disk77(t)
	mustExecOK(t, raw, `PRAGMA journal_mode=WAL;PRAGMA wal_autocheckpoint=0`)
	mustExecOK(t, raw, `UPDATE accounts SET current_balance=1234567,balance_version=42 WHERE id='acc-checking' AND workspace_id='ws-1'`)
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatal("fixture has no WAL frames", err)
	}
	before := cancellation79Snapshot(t, raw)
	p, err := OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	files, err := filepath.Glob(filepath.Join(data, "backups", "pre-migration", "*.db"))
	if err != nil || len(files) != 1 {
		t.Fatal("backup missing", files, err)
	}
	backup, err := sql.Open("sqlite", sqliteBackupURI(files[0], "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	backup.SetMaxOpenConns(1)
	if countQueryRows(t, backup, `SELECT MAX(version) FROM schema_migrations`) != 77 || schemaObjectExists(t, backup, "table", "investment_cancellations") {
		t.Fatal("backup captured post-migration schema")
	}
	if !reflect.DeepEqual(before, cancellation79Snapshot(t, backup)) || !reflect.DeepEqual(before, cancellation79Snapshot(t, p.DB)) {
		t.Fatal("WAL snapshot or migration changed financial data")
	}
	if err := sqlite79Integrity(backup); err != nil {
		t.Fatal(err)
	}
}

func TestMigration79BackupFailureStopsBeforeDDL(t *testing.T) {
	data := t.TempDir()
	t.Setenv("DATA_DIR", data)
	path, raw := cancellation79Disk77(t)
	if err := os.WriteFile(filepath.Join(data, "backups"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	before := cancellation79Snapshot(t, raw)
	identity := cancellation79TableIdentity(t, raw)
	if p, err := OpenRuntimeDBs(path); err == nil {
		p.Close()
		t.Fatal("backup failure ignored")
	}
	if !reflect.DeepEqual(before, cancellation79Snapshot(t, raw)) || identity != cancellation79TableIdentity(t, raw) || schemaObjectExists(t, raw, "table", "investment_cancellations") {
		t.Fatal("backup failure wrote migration")
	}
}

func cancellation79ReceiptFixture(t *testing.T) (*sql.DB, map[string]any) {
	t.Helper()
	db := migration77Fixture(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	confirmed := time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC).Unix()
	mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,created_at,updated_at)
 VALUES('investment-purchase:initial:inv-1','ws-1','user-1','acc-checking','acc-inv','TRANSFER',100000,unixepoch('2026-01-01 12:00:00'),'2026-01-01',unixepoch('2026-01-01 12:00:00'),'2026-01-01','paid',1,1)`)
	mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,created_at,updated_at)
 VALUES('investment-cancellation:cancel-1','ws-1','user-1','acc-inv','acc-checking','TRANSFER',100000,unixepoch('2026-10-31 12:00:00'),'2026-10-31',unixepoch('2026-10-31 12:00:00'),'2026-10-31','paid',?,?)`, confirmed, confirmed)
	mustExecOK(t, db, `UPDATE accounts SET balance_version=4 WHERE id='acc-inv';UPDATE accounts SET current_balance=0 WHERE id='acc-inv';UPDATE accounts SET balance_version=7 WHERE id='acc-checking';UPDATE accounts SET current_balance=110000 WHERE id='acc-checking'`)
	r := map[string]any{"workspace_id": "ws-1", "id": "cancel-1", "investment_id": "inv-1", "purchase_id": "initial:inv-1", "original_transaction_id": "investment-purchase:initial:inv-1", "compensation_transaction_id": "investment-cancellation:cancel-1", "investment_account_id": "acc-inv", "origin_account_id": "acc-checking", "amount_cents": int64(100000), "investment_before_cents": int64(100000), "investment_after_cents": int64(0), "origin_before_cents": int64(10000), "origin_after_cents": int64(110000), "investment_before_version": int64(4), "investment_after_version": int64(5), "origin_before_version": int64(7), "origin_after_version": int64(8), "created_by": "user-1", "confirmed_at": confirmed, "economic_date": "2026-10-31", "reason": "Correção sintética", "request_id": "11111111-1111-4111-8111-111111111111", "preview_id": strings.Repeat("a", 64), "preview_issued_at": confirmed - 60, "preview_expires_at": confirmed + 840, "evidence_hash": strings.Repeat("b", 64), "payload_version": 1, "receipt_version": 1}
	cancellation79FixtureJSON(t, r)
	return db, r
}

func cancellation79FixtureJSON(t *testing.T, r map[string]any) {
	t.Helper()
	payload := map[string]any{"version": 1, "operation": "CANCEL_INITIAL_FUNDED_INVESTMENT", "workspace_id": r["workspace_id"], "acting_user_id": r["created_by"], "investment_id": r["investment_id"], "request_id": r["request_id"], "reason": r["reason"], "confirmed": true, "preview_token": "synthetic-unverified-token"}
	evidence := map[string]any{}
	for _, field := range []string{"workspace_id", "investment_id", "purchase_id", "original_transaction_id", "investment_account_id", "origin_account_id", "amount_cents", "economic_date", "evidence_hash"} {
		evidence[field] = r[field]
	}
	for field, v := range map[string]any{"payload_json": payload, "evidence_json": evidence} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		r[field] = string(b)
	}
}

func cancellation79Insert(db *sql.DB, r map[string]any) error {
	var cols []string
	for c := range r {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	args := make([]any, len(cols))
	slots := make([]string, len(cols))
	for i, c := range cols {
		args[i] = r[c]
		slots[i] = "?"
	}
	_, err := db.Exec(`INSERT INTO investment_cancellations(`+strings.Join(cols, ",")+`) VALUES(`+strings.Join(slots, ",")+`)`, args...)
	return err
}

func TestMigration79ReceiptAndTransactionTriggersImmutable(t *testing.T) {
	db, r := cancellation79ReceiptFixture(t)
	if err := cancellation79Insert(db, r); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `PRAGMA recursive_triggers=OFF`)
	for _, query := range []string{
		`UPDATE investment_cancellations SET reason=reason`, `DELETE FROM investment_cancellations`,
		`INSERT OR REPLACE INTO investment_cancellations SELECT * FROM investment_cancellations`,
		`INSERT INTO investment_cancellations SELECT * FROM investment_cancellations WHERE true ON CONFLICT(workspace_id,id) DO UPDATE SET reason=excluded.reason`,
		`UPDATE investments SET rowid=rowid+100 WHERE id='inv-1'`, `DELETE FROM investments WHERE id='inv-1'`,
		`INSERT OR REPLACE INTO investments SELECT * FROM investments WHERE id='inv-1'`,
		`UPDATE investment_purchases SET rowid=rowid+100 WHERE investment_id='inv-1'`,
		`DELETE FROM investment_purchases WHERE investment_id='inv-1'`,
		`UPDATE transactions SET amount=amount WHERE id='investment-purchase:initial:inv-1'`,
		`UPDATE transactions SET rowid=rowid+100 WHERE id='investment-cancellation:cancel-1'`,
		`DELETE FROM transactions WHERE id='investment-purchase:initial:inv-1'`,
		`DELETE FROM transactions WHERE id='investment-cancellation:cancel-1'`,
		`INSERT OR REPLACE INTO transactions SELECT * FROM transactions WHERE id='investment-cancellation:cancel-1'`,
		`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('bad','ws-1','investment-cancellation:cancel-1',1,'bad','user-1')`,
		`INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('bad','ws-1','inv-1','2026-11-01',0,'user-1')`,
		`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,parent_id) VALUES('reuse','ws-1','user-1','acc-checking','EXPENSE',1,1,'investment-purchase:initial:inv-1')`,
	} {
		mustExecError(t, db, query)
	}
	// Rowid REPLACE must be stopped by immediate RESTRICT, even when the id
	// changes and recursive delete triggers do not run.
	for _, table := range []string{"investments", "transactions"} {
		cols := []string{}
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, name)
		}
		rows.Close()
		selects := append([]string{}, cols...)
		for i, c := range cols {
			if c == "id" {
				selects[i] = "'replacement'"
			}
		}
		where := "id='inv-1'"
		if table == "transactions" {
			where = "id='investment-purchase:initial:inv-1'"
		}
		mustExecError(t, db, `INSERT OR REPLACE INTO `+table+`(rowid,`+strings.Join(cols, ",")+`) SELECT rowid,`+strings.Join(selects, ",")+` FROM `+table+` WHERE `+where)
	}
	// Independent balances and transactions are not frozen by a receipt.
	mustExecOK(t, db, `UPDATE accounts SET current_balance=current_balance+1,balance_version=balance_version+1 WHERE id='acc-checking'`)
	mustExecOK(t, db, `UPDATE transactions SET description='independent' WHERE id='existing-transaction'`)
	if err := validateCancellation79Receipts(db); err != nil {
		t.Fatal("receipt incorrectly depends on current balance", err)
	}
}

func TestMigration79ReceiptChecksAndRelationalIntegrity(t *testing.T) {
	faults := map[string]func(*sql.DB, map[string]any){
		"overflow-credit": func(_ *sql.DB, r map[string]any) {
			r["origin_before_cents"] = int64(9223372036854775807)
			r["origin_after_cents"] = int64(0)
		},
		"overflow-debit": func(_ *sql.DB, r map[string]any) {
			r["investment_before_cents"] = int64(-9223372036854775808)
			r["investment_after_cents"] = int64(0)
		},
		"version-exhausted": func(_ *sql.DB, r map[string]any) { r["origin_before_version"] = int64(9223372036854775807) },
		"real-amount":       func(_ *sql.DB, r map[string]any) { r["amount_cents"] = 1.5 },
		"wrong-workspace":   func(_ *sql.DB, r map[string]any) { r["workspace_id"] = "ws-2" },
		"wrong-amount":      func(_ *sql.DB, r map[string]any) { r["amount_cents"] = int64(1) },
		"expired":           func(_ *sql.DB, r map[string]any) { r["confirmed_at"] = r["preview_expires_at"] },
		"bad-payload":       func(_ *sql.DB, r map[string]any) { r["payload_json"] = `{}` },
		"bad-evidence":      func(_ *sql.DB, r map[string]any) { r["evidence_json"] = `{}` },
		"source-adjustment": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `INSERT INTO account_balance_adjustments(id,workspace_id,account_id,before_balance,after_balance,delta_cents,expected_version,reason,created_by,created_at) VALUES('adjust','ws-1','acc-checking',110000,110001,1,8,'synthetic','user-1',1)`)
		},
		"position": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('position','ws-1','inv-1','2026-01-01',0,'user-1')`)
		},
		"compensation-invoice": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `UPDATE transactions SET scheduled_payment_at=1 WHERE id='investment-cancellation:cancel-1'`)
		},
		"original-direction": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `UPDATE transactions SET account_id='acc-inv' WHERE id='investment-purchase:initial:inv-1'`)
		},
	}
	for name, f := range faults {
		t.Run(name, func(t *testing.T) {
			db, r := cancellation79ReceiptFixture(t)
			f(db, r)
			if err := cancellation79Insert(db, r); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			if countQueryRows(t, db, `SELECT count(*) FROM investment_cancellations`) != 0 {
				t.Fatal("partial receipt persisted")
			}
		})
	}
}

func TestMigration79ReopenRejectsStructuralAndReceiptDrift(t *testing.T) {
	for _, fault := range []string{"missing-trigger", "weakened-trigger", "extra-trigger", "missing-index", "receipt-evidence"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			db, r := cancellation79ReceiptFixture(t)
			if err := cancellation79Insert(db, r); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "missing-trigger":
				mustExecOK(t, db, `DROP TRIGGER trg_cancellation79_receipt_update`)
			case "weakened-trigger":
				mustExecOK(t, db, `DROP TRIGGER trg_cancellation79_receipt_update;CREATE TRIGGER trg_cancellation79_receipt_update BEFORE UPDATE ON investment_cancellations BEGIN SELECT 1;END`)
			case "extra-trigger":
				mustExecOK(t, db, `CREATE TRIGGER extra_receipt BEFORE INSERT ON investment_cancellations BEGIN SELECT 1;END`)
			case "missing-index":
				mustExecOK(t, db, `DROP INDEX idx_cancellation79_date`)
			case "receipt-evidence":
				var trigger string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='trg_cancellation79_receipt_update'`).Scan(&trigger); err != nil {
					t.Fatal(err)
				}
				mustExecOK(t, db, `DROP TRIGGER trg_cancellation79_receipt_update;UPDATE investment_cancellations SET evidence_json='{}'`)
				mustExecOK(t, db, trigger)
			}
			path := filepath.Join(t.TempDir(), "drift.db")
			mustExecOK(t, db, `VACUUM INTO ?`, path)
			if err := ValidateSQLiteRuntimeSchema(path); err == nil {
				t.Fatal("structural/evidence drift accepted")
			}
			if p, err := OpenRuntimeDBs(path); err == nil {
				p.Close()
				t.Fatal("corrupt schema published")
			}
		})
	}
}

func TestMigration79PartialHistoricalFixtureNoOp(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExecOK(t, db, `CREATE TABLE historical_probe(id TEXT)`)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateInvestmentCancellations(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if schemaObjectExists(t, db, "table", "investment_cancellations") {
		t.Fatal("partial migration probe created cancellation schema")
	}
	path := filepath.Join(t.TempDir(), "partial.db")
	mustExecOK(t, db, `VACUUM INTO ?`, path)
	if p, err := OpenRuntimeDBs(path); err == nil {
		p.Close()
		t.Fatal("runtime accepted historical partial probe")
	}
}

func TestMigration79DDLAndMarkerRollbackTogether(t *testing.T) {
	db := migration77Fixture(t)
	before := cancellation79Snapshot(t, db)
	identity := cancellation79TableIdentity(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateInvestmentCancellations(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations VALUES(79,'investment_cancellation',1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if schemaObjectExists(t, db, "table", "investment_cancellations") || countQueryRows(t, db, `SELECT count(*) FROM schema_migrations WHERE version=79`) != 0 {
		t.Fatal("partial migration survived rollback")
	}
	if !reflect.DeepEqual(before, cancellation79Snapshot(t, db)) || identity != cancellation79TableIdentity(t, db) {
		t.Fatal("rollback changed prior records")
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
}

func TestMigration79ChildrenAndTransferReuseBlocked(t *testing.T) {
	db, r := cancellation79ReceiptFixture(t)
	if err := cancellation79Insert(db, r); err != nil {
		t.Fatal(err)
	}
	for _, child := range cancellation79Children {
		t.Run(child, func(t *testing.T) {
			_, err := db.Exec(`INSERT INTO ` + child + `(workspace_id,investment_id) VALUES('ws-1','inv-1')`)
			if err == nil || !strings.Contains(err.Error(), "investment cancellation integrity") {
				t.Fatal("cancellation child guard was not reached", err)
			}
		})
	}
	mustExecOK(t, db, `INSERT INTO boxes(id,workspace_id,name) VALUES('box79','ws-1','synthetic')`)
	for _, link := range cancellation79TransactionLinks {
		for _, id := range []string{"investment-purchase:initial:inv-1", "investment-cancellation:cancel-1"} {
			t.Run(link.table+"/"+link.column+"/"+id, func(t *testing.T) {
				column, value := "workspace_id", "'ws-1'"
				if link.scope == "" {
					column, value = "box_id", "'box79'"
				}
				_, err := db.Exec(`INSERT INTO `+link.table+`(`+column+`,`+link.column+`) VALUES(`+value+`,?)`, id)
				if err == nil || !strings.Contains(err.Error(), "investment cancellation integrity") {
					t.Fatal("cancellation transfer guard was not reached", err)
				}
			})
		}
	}
}

func TestMigration79IntegerStorageAndOverflowChecks(t *testing.T) {
	for _, fault := range []string{"credit", "debit", "version", "real", "date"} {
		t.Run(fault, func(t *testing.T) {
			db, r := cancellation79ReceiptFixture(t)
			// Isolate table CHECKs from the stronger relational admission trigger.
			mustExecOK(t, db, `DROP TRIGGER trg_cancellation79_insert`)
			switch fault {
			case "credit":
				r["origin_before_cents"] = int64(9223372036854775807)
			case "debit":
				r["investment_before_cents"] = int64(-9223372036854775808)
			case "version":
				r["origin_before_version"] = int64(9223372036854775807)
			case "real":
				r["investment_after_cents"] = 1.5
			case "date":
				r["economic_date"] = "2026-13-01"
			}
			if err := cancellation79Insert(db, r); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Fatal("table storage/arithmetic check failed", err)
			}
		})
	}
}

func TestMigration79SupportedHistoricalSchemaAndPartialRefusal(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	for _, version := range []int{0, 64, 68, 76} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db := openInvestmentsTestDB(t)
			if version > 0 {
				seedAppliedMigrationsBefore(t, db, 1)
				if err := runMigrationsThroughVersionForTest(db, version); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "historical.db")
			mustExecOK(t, db, `VACUUM INTO ?`, path)
			p, err := OpenRuntimeDBs(path)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if countQueryRows(t, p.DB, `SELECT MAX(version) FROM schema_migrations`) != 82 {
				t.Fatal("historical upgrade did not reach the latest registry")
			}
		})
	}
}

func TestMigration79ReopenReceiptAndIndependentMovements(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	db, r := cancellation79ReceiptFixture(t)
	if err := cancellation79Insert(db, r); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "receipt79.db")
	mustExecOK(t, db, `VACUUM INTO ?`, path)
	p, err := OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Immediate.Exec(`UPDATE accounts SET current_balance=current_balance+100 WHERE workspace_id='ws-1' AND id='acc-checking'`); err != nil {
		t.Fatal(err)
	}
	p.Close()
	p, err = OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if countQueryRows(t, p.DB, `SELECT count(*) FROM investment_cancellations`) != 1 {
		t.Fatal("receipt lost on reopen")
	}
}
