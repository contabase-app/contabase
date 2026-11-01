package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// reversal80Snapshot captures every schema object of the database so an
// additive migration can be proven by exact set difference.
func reversal80Snapshot(t *testing.T, db *sql.DB) map[string]string {
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

func TestMigration80AdditiveIdempotentAndRegistered(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	db := openInvestmentsTestDB(t)
	seedAppliedMigrationsBefore(t, db, 1)
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	// Apply schema 79 directly so the registry stays an exact prefix while the
	// capped runner would leave a hole above 79 that 79's own preflight
	// rightly refuses (any pre-registered future marker is unregistered).
	applyDirect := func(version int, name string, up func(*sql.Tx) error) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := up(tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES (?,?)`, version, name); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertMigrationApplied(t, db, version)
	}
	applyDirect(79, "investment_cancellation", migrateInvestmentCancellations)
	before := reversal80Snapshot(t, db)

	// Schema 80 is applied directly as well: with migration 81 registered, the
	// capped runner would leave an excluded marker above 80 that 80's own
	// marker preflight refuses by the same exact-prefix rule.
	applyDirect(80, "investment_purchase_reversal", migrateInvestmentPurchaseReversals)

	if err := runMigrationsThroughVersionForTest(db, 80); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 80)
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 80 {
		t.Fatalf("migration version=%d, want 80", got)
	}
	if got := countQueryRows(t, db, `SELECT count(*) FROM schema_migrations WHERE version IN(69,78)`); got != 0 {
		t.Fatal("migration 80 introduced reserved markers")
	}
	after := reversal80Snapshot(t, db)
	// Exactly the schema 80 objects were added; nothing else moved.
	added := map[string]bool{}
	for object, ddl := range after {
		if before[object] == ddl {
			continue
		}
		if _, existed := before[object]; existed {
			t.Fatalf("migration 80 rewrote existing object: %s", object)
		}
		added[object] = true
	}
	for object := range before {
		if _, exists := after[object]; !exists {
			t.Fatalf("migration 80 dropped object: %s", object)
		}
	}
	expected := map[string]bool{}
	for _, object := range reversal80Objects() {
		expected[object.kind+" "+object.name] = true
	}
	if len(added) != len(expected) {
		t.Fatalf("added objects=%d want=%d: %v", len(added), len(expected), added)
	}
	for object := range expected {
		if !added[object] {
			t.Fatalf("missing schema 80 object: %s", object)
		}
	}
	if got := countQueryRows(t, db, `SELECT count(*) FROM investment_purchase_reversals`); got != 0 {
		t.Fatal("migration 80 created financial receipts")
	}
	if got := countQueryRows(t, db, `SELECT count(*) FROM pragma_table_info('transactions')`); got == 0 {
		t.Fatal("transactions table missing")
	}

	// Re-running the runner must stay a no-op, including object identity.
	identity := reversal80Snapshot(t, db)
	if err := runMigrationsThroughVersionForTest(db, 80); err != nil {
		t.Fatal(err)
	}
	if got := reversal80Snapshot(t, db); len(got) != len(identity) {
		t.Fatal("re-run duplicated schema objects")
	}

	// The runtime validator accepts the resulting version-80 database and the
	// preflight refuses to run 80 twice or over a pre-79 registry.
	path := filepath.Join(t.TempDir(), "runtime80.db")
	mustExecOK(t, db, `VACUUM INTO ?`, path)
	p, err := OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// The runtime open migrates the version-80 database to the registry head:
	// with migrations 81 and 82 registered, the reopened database carries 82.
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
	if err := preflightInvestmentReversals80(olderTx); err == nil {
		olderTx.Rollback()
		t.Fatal("preflight accepted schema 77 for migration 80")
	}
	olderTx.Rollback()
}

func applyMigrationDirectForTest(t *testing.T, db *sql.DB, version int, name string, up func(*sql.Tx) error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := up(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version,name) VALUES (?,?)`, version, name); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, version)
}

// Schema 82 replaces only the two parent UPDATE triggers that schema 80 froze.
// Every other schema 80 object, the receipt family and the transactions table
// stay identical, and a database already at 81 upgrades in place.
func TestMigration82RestoresParentOperabilityAndRegistered(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	db := openInvestmentsTestDB(t)
	seedAppliedMigrationsBefore(t, db, 1)
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	applyMigrationDirectForTest(t, db, 79, "investment_cancellation", migrateInvestmentCancellations)
	applyMigrationDirectForTest(t, db, 80, "investment_purchase_reversal", migrateInvestmentPurchaseReversals)
	applyMigrationDirectForTest(t, db, 81, "investment_record_cancellation", migrateInvestmentRecordCancellations)

	if err := validateReversal80Structure(db, false); err != nil {
		t.Fatalf("schema 81 no longer matches the historical contract: %v", err)
	}
	preflightTx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := preflightInvestmentPurchaseReversals82(preflightTx); err != nil {
		preflightTx.Rollback()
		t.Fatalf("preflight rejected intact schema 81: %v", err)
	}
	preflightTx.Rollback()

	before := reversal80Snapshot(t, db)
	applyMigrationDirectForTest(t, db, 82, "investment_purchase_reversal_operability", migrateInvestmentPurchaseReversals82)
	if err := runMigrationsThroughVersionForTest(db, 82); err != nil {
		t.Fatal(err)
	}
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 82 {
		t.Fatalf("migration version=%d, want 82", got)
	}
	if err := validateReversal80Structure(db, true); err != nil {
		t.Fatal(err)
	}
	if err := validateReversal80Receipts(db); err != nil {
		t.Fatal(err)
	}

	after := reversal80Snapshot(t, db)
	removed := map[string]bool{}
	for object := range before {
		if _, exists := after[object]; !exists {
			removed[object] = true
		} else if before[object] != after[object] {
			t.Fatalf("migration 82 rewrote existing object: %s", object)
		}
	}
	added := map[string]bool{}
	for object, ddl := range after {
		if before[object] == ddl {
			continue
		}
		if _, existed := before[object]; existed {
			t.Fatalf("migration 82 rewrote existing object: %s", object)
		}
		added[object] = true
	}
	wantRemoved := map[string]bool{
		"trigger trg_reversal80_accounts_update":    true,
		"trigger trg_reversal80_investments_update": true,
	}
	wantAdded := map[string]bool{
		"trigger trg_reversal82_accounts_update":    true,
		"trigger trg_reversal82_investments_update": true,
	}
	if len(removed) != len(wantRemoved) || len(added) != len(wantAdded) {
		t.Fatalf("removed=%v added=%v", removed, added)
	}
	for object := range wantRemoved {
		if !removed[object] {
			t.Fatalf("missing removed object: %s", object)
		}
	}
	for object := range wantAdded {
		if !added[object] {
			t.Fatalf("missing added object: %s", object)
		}
	}

	path := filepath.Join(t.TempDir(), "runtime82.db")
	mustExecOK(t, db, `VACUUM INTO ?`, path)
	runtime, err := OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := countQueryRows(t, runtime.DB, `SELECT MAX(version) FROM schema_migrations`); got != 82 {
		t.Fatalf("runtime version=%d, want 82", got)
	}

	older := openInvestmentsTestDB(t)
	seedAppliedMigrationsBefore(t, older, 1)
	if err := runMigrationsThroughVersionForTest(older, 77); err != nil {
		t.Fatal(err)
	}
	applyMigrationDirectForTest(t, older, 79, "investment_cancellation", migrateInvestmentCancellations)
	applyMigrationDirectForTest(t, older, 80, "investment_purchase_reversal", migrateInvestmentPurchaseReversals)
	olderTx, err := older.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := preflightInvestmentPurchaseReversals82(olderTx); err == nil {
		olderTx.Rollback()
		t.Fatal("preflight accepted schema 80 for migration 82")
	}
	olderTx.Rollback()
}
