package database

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Schema 81 is purely additive over 80: exact object set difference, no
// receipts, untouched transactions journal, immutable receipts, frozen lot
// and no fabricated funding transfer.

func recordCancellation81Snapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type,name,COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	objects := map[string]string{}
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		objects[kind+" "+name] = ddl
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return objects
}

func TestInvestmentRecordCancellationMigration81AdditiveAndRegistered(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	db := openInvestmentsTestDB(t)
	seedAppliedMigrationsBefore(t, db, 1)
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	// Apply schemas 79 and 80 directly so the registry stays an exact prefix
	// while the capped runner would leave excluded markers above them that
	// their own preflights rightly refuse (the precedent of the schema 80
	// test, now needed for 80 as well because 81 and 82 exist above it).
	for _, direct := range []struct {
		version int
		name    string
		up      func(*sql.Tx) error
	}{
		{79, "investment_cancellation", migrateInvestmentCancellations},
		{80, "investment_purchase_reversal", migrateInvestmentPurchaseReversals},
	} {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := direct.up(tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES (?,?)`, direct.version, direct.name); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertMigrationApplied(t, db, direct.version)
	}
	before := recordCancellation81Snapshot(t, db)

	// Schema 81 is applied directly for the same exact-prefix reason (82 now
	// exists above it); the capped runner is later proven to be a no-op.
	applyMigrationDirectForTest(t, db, 81, "investment_record_cancellation", migrateInvestmentRecordCancellations)
	if err := runMigrationsThroughVersionForTest(db, 81); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 81)
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 81 {
		t.Fatalf("migration version=%d, want 81", got)
	}
	if got := countQueryRows(t, db, `SELECT count(*) FROM schema_migrations WHERE version IN(69,78)`); got != 0 {
		t.Fatal("migration 81 introduced reserved markers")
	}
	after := recordCancellation81Snapshot(t, db)
	added := map[string]bool{}
	for object, ddl := range after {
		if before[object] == ddl {
			continue
		}
		if _, existed := before[object]; existed {
			t.Fatalf("migration 81 rewrote existing object: %s", object)
		}
		added[object] = true
	}
	for object := range before {
		if _, exists := after[object]; !exists {
			t.Fatalf("migration 81 dropped object: %s", object)
		}
	}
	expected := map[string]bool{}
	for _, object := range recordCancellation81Objects() {
		expected[object.kind+" "+object.name] = true
	}
	if len(added) != len(expected) {
		t.Fatalf("added objects=%d want=%d: %v", len(added), len(expected), added)
	}
	for object := range expected {
		if !added[object] {
			t.Fatalf("missing schema 81 object: %s", object)
		}
	}
	if got := countQueryRows(t, db, `SELECT count(*) FROM investment_record_cancellations`); got != 0 {
		t.Fatal("migration 81 created financial receipts")
	}

	// Re-running the runner must stay a no-op, including object identity.
	identity := recordCancellation81Snapshot(t, db)
	if err := runMigrationsThroughVersionForTest(db, 81); err != nil {
		t.Fatal(err)
	}
	if got := recordCancellation81Snapshot(t, db); len(got) != len(identity) {
		t.Fatal("re-run duplicated schema objects")
	}

	// The runtime validator accepts the resulting version-81 database and the
	// preflight refuses to run 81 over a pre-80 registry.
	path := filepath.Join(t.TempDir(), "runtime81.db")
	mustExecOK(t, db, `VACUUM INTO ?`, path)
	p, err := OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := countQueryRows(t, p.DB, `SELECT MAX(version) FROM schema_migrations`); got != 82 {
		t.Fatalf("runtime version=%d, want 82", got)
	}
	older := openInvestmentsTestDB(t)
	seedAppliedMigrationsBefore(t, older, 1)
	if err := runMigrationsThroughVersionForTest(older, 77); err != nil {
		t.Fatal(err)
	}
	olderTx, err := older.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := preflightInvestmentRecordCancellations81(olderTx); err == nil {
		olderTx.Rollback()
		t.Fatal("preflight accepted schema 79 for migration 81")
	}
	olderTx.Rollback()
}

// recordCancellation81Fixture builds the migration-77 world, a record-only
// investment (no purchase transfer) and the synthetic receipt row.
func recordCancellation81Fixture(t *testing.T) (*sql.DB, map[string]any) {
	t.Helper()
	db := migration77Fixture(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	// The investment account carries an independent balance; the version is
	// read back because the accounts triggers own every balance write.
	mustExecOK(t, db, `UPDATE accounts SET initial_balance=250000, current_balance=250000 WHERE id='acc-inv'`)
	var balance, version int64
	if err := db.QueryRow(`SELECT current_balance,balance_version FROM accounts WHERE workspace_id='ws-1' AND id='acc-inv'`).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	confirmed := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Unix()
	r := map[string]any{"workspace_id": "ws-1", "id": "record-cancel-1", "investment_id": "inv-1", "purchase_id": "initial:inv-1",
		"investment_account_id": "acc-inv", "amount_cents": int64(100000),
		"investment_before_cents": balance, "investment_after_cents": balance,
		"investment_before_version": version, "investment_after_version": version,
		"created_by": "user-1", "confirmed_at": confirmed, "economic_date": "2026-11-01", "reason": "Registro sintético",
		"request_id": "22222222-2222-4222-8222-222222222222", "preview_id": strings.Repeat("c", 64),
		"preview_issued_at": confirmed - 60, "preview_expires_at": confirmed + 840, "evidence_hash": strings.Repeat("d", 64),
		"payload_version": 1, "receipt_version": 1}
	payload := map[string]any{"version": 1, "operation": "CANCEL_INITIAL_RECORD_INVESTMENT", "workspace_id": r["workspace_id"],
		"acting_user_id": r["created_by"], "investment_id": r["investment_id"], "request_id": r["request_id"],
		"reason": r["reason"], "confirmed": true, "preview_token": "synthetic-unverified-token"}
	evidence := map[string]any{}
	for _, field := range []string{"workspace_id", "investment_id", "purchase_id", "investment_account_id", "amount_cents", "economic_date", "evidence_hash"} {
		evidence[field] = r[field]
	}
	for field, v := range map[string]any{"payload_json": payload, "evidence_json": evidence} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		r[field] = string(b)
	}
	return db, r
}

func recordCancellation81Insert(db *sql.DB, r map[string]any) error {
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
	_, err := db.Exec(`INSERT INTO investment_record_cancellations(`+strings.Join(cols, ",")+`) VALUES(`+strings.Join(slots, ",")+`)`, args...)
	return err
}

func TestInvestmentRecordCancellation81ReceiptTriggersImmutable(t *testing.T) {
	db, r := recordCancellation81Fixture(t)
	if err := recordCancellation81Insert(db, r); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `PRAGMA recursive_triggers=OFF`)
	for _, query := range []string{
		`UPDATE investment_record_cancellations SET reason=reason`,
		`DELETE FROM investment_record_cancellations`,
		`INSERT OR REPLACE INTO investment_record_cancellations SELECT * FROM investment_record_cancellations`,
		`UPDATE investments SET rowid=rowid+100 WHERE id='inv-1'`,
		`DELETE FROM investments WHERE id='inv-1'`,
		`UPDATE investment_purchases SET rowid=rowid+100 WHERE investment_id='inv-1'`,
		`DELETE FROM investment_purchases WHERE investment_id='inv-1'`,
		// A later purchase of the cancelled lot is a child write.
		`INSERT INTO investment_purchases(id,workspace_id,investment_id,purchase_date,principal_cents,conditions_source,is_initial,created_by,created_at) VALUES('p2','ws-1','inv-1','2026-02-01',1,'RECORDED',0,'user-1',1)`,
		`INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('bad','ws-1','inv-1','2026-11-01',0,'user-1')`,
	} {
		mustExecError(t, db, query)
	}
	// The two schema 81 specific guards must refuse with their own signature:
	// the absent funding transfer cannot be fabricated afterwards (closing the
	// only path that could later satisfy the schema 79 relation), and an
	// adoption declaration about the cancelled account is incompatible. Both
	// triggers fire BEFORE the table constraints.
	for _, query := range []string{
		`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,created_at,updated_at) VALUES('investment-purchase:initial:inv-1','ws-1','user-1','acc-checking','acc-inv','TRANSFER',100000,unixepoch('2026-01-01 12:00:00'),'2026-01-01',unixepoch('2026-01-01 12:00:00'),'2026-01-01','paid',1,1)`,
		`INSERT INTO investment_adoption_confirmations(workspace_id,account_id,id,version,previous_id,previous_version,cut_date,created_by,created_at,request_id,reason,evidence_version,declaration_version,evidence_token,source_token,payload_json,evidence_json,result_json) VALUES('ws-1','acc-inv','bad',1,NULL,0,'2026-01-01','user-1',1,'33333333-3333-4333-8333-333333333333','sintético',1,1,'` + strings.Repeat("e", 64) + `','` + strings.Repeat("f", 64) + `','{}','{}','{}')`,
	} {
		if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "investment record cancellation integrity") {
			t.Fatalf("schema 81 guard did not fire for %q: %v", query[:60], err)
		}
	}
	// Independent movements on the investment account are preserved and the
	// historical receipt survives them: the pins are insert-time only.
	mustExecOK(t, db, `UPDATE accounts SET current_balance=current_balance+1,balance_version=balance_version+1 WHERE id='acc-inv'`)
	mustExecOK(t, db, `UPDATE transactions SET description='independent' WHERE id='existing-transaction'`)
	if err := validateRecordCancellation81Receipts(db); err != nil {
		t.Fatal("receipt incorrectly depends on the current balance", err)
	}
}

func TestInvestmentRecordCancellation81ReceiptChecks(t *testing.T) {
	faults := map[string]func(*sql.DB, map[string]any){
		"zero-effect-broken": func(_ *sql.DB, r map[string]any) { r["investment_after_cents"] = int64(249999) },
		"version-broken":     func(_ *sql.DB, r map[string]any) { r["investment_after_version"] = int64(4) },
		"real-amount":        func(_ *sql.DB, r map[string]any) { r["amount_cents"] = 1.5 },
		"wrong-workspace":    func(_ *sql.DB, r map[string]any) { r["workspace_id"] = "ws-2" },
		"wrong-amount":       func(_ *sql.DB, r map[string]any) { r["amount_cents"] = int64(1) },
		"expired":            func(_ *sql.DB, r map[string]any) { r["confirmed_at"] = r["preview_expires_at"] },
		"bad-payload":        func(_ *sql.DB, r map[string]any) { r["payload_json"] = `{}` },
		"bad-evidence":       func(_ *sql.DB, r map[string]any) { r["evidence_json"] = `{}` },
		"wrong-purchase":     func(_ *sql.DB, r map[string]any) { r["purchase_id"] = "initial:inv-2" },
		"funded-transfer": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,created_at,updated_at) VALUES('investment-purchase:initial:inv-1','ws-1','user-1','acc-checking','acc-inv','TRANSFER',100000,unixepoch('2026-01-01 12:00:00'),'2026-01-01',unixepoch('2026-01-01 12:00:00'),'2026-01-01','paid',1,1)`)
		},
		"balance-pin": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `UPDATE accounts SET current_balance=999999 WHERE id='acc-inv'`)
		},
		"adjustment": func(db *sql.DB, r map[string]any) {
			mustExecOK(t, db, `INSERT INTO account_balance_adjustments(id,workspace_id,account_id,before_balance,after_balance,delta_cents,expected_version,reason,created_by,created_at) VALUES('adjust','ws-1','acc-inv',?,?+1,1,?,'synthetic','user-1',1)`,
				r["investment_before_cents"], r["investment_before_cents"], r["investment_before_version"])
		},
		"position": func(db *sql.DB, _ map[string]any) {
			mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('position','ws-1','inv-1','2026-01-01',0,'user-1')`)
		},
	}
	for name, f := range faults {
		t.Run(name, func(t *testing.T) {
			db, r := recordCancellation81Fixture(t)
			f(db, r)
			if err := recordCancellation81Insert(db, r); err == nil {
				t.Fatal("invalid receipt accepted")
			}
			if countQueryRows(t, db, `SELECT count(*) FROM investment_record_cancellations`) != 0 {
				t.Fatal("partial receipt persisted")
			}
		})
	}
}

func TestInvestmentRecordCancellation81ReopenDrift(t *testing.T) {
	for _, fault := range []string{"missing-trigger", "weakened-trigger", "extra-trigger", "missing-index", "weakened-insert-trigger"} {
		t.Run(fault, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			db, r := recordCancellation81Fixture(t)
			if err := recordCancellation81Insert(db, r); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "missing-trigger":
				mustExecOK(t, db, `DROP TRIGGER trg_recordcancel81_receipt_update`)
			case "weakened-trigger":
				mustExecOK(t, db, `DROP TRIGGER trg_recordcancel81_receipt_update;CREATE TRIGGER trg_recordcancel81_receipt_update BEFORE UPDATE ON investment_record_cancellations BEGIN SELECT 1;END`)
			case "extra-trigger":
				mustExecOK(t, db, `CREATE TRIGGER extra_record_receipt BEFORE INSERT ON investment_record_cancellations BEGIN SELECT 1;END`)
			case "missing-index":
				mustExecOK(t, db, `DROP INDEX idx_recordcancel81_date`)
			case "weakened-insert-trigger":
				mustExecOK(t, db, `DROP TRIGGER trg_recordcancel81_insert;CREATE TRIGGER trg_recordcancel81_insert BEFORE INSERT ON investment_record_cancellations BEGIN SELECT 1;END`)
			}
			path := filepath.Join(t.TempDir(), "drift81.db")
			mustExecOK(t, db, `VACUUM INTO ?`, path)
			if err := validateSQLiteRuntimePath(path); err == nil {
				t.Fatal("runtime validator accepted drifted schema 81")
			}
			if _, err := OpenRuntimeDBs(path); err == nil {
				t.Fatal("reopen accepted drifted schema 81")
			}
		})
	}
}
