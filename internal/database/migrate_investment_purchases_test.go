package database

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func investmentPurchasesLegacyDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openInvestmentsTestDB(t)
	// Initialize the registry without claiming any migration was applied.
	// The canonical historical helper uses the production runner through v68;
	// v69 is reserved and v70 must not run before the legacy rows are seeded.
	seedAppliedMigrationsBefore(t, db, 1)
	if err := runMigrationsThroughVersionForTest(db, 68); err != nil {
		t.Fatal(err)
	}
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 68 {
		t.Fatalf("legacy migration version=%d, want 68", got)
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version IN (69,70)`) != 0 || schemaObjectExists(t, db, "table", "investment_purchases") {
		t.Fatal("legacy fixture prematurely applied investment purchases or reserved v69")
	}
	seedInvestmentFixtures(t, db)
	return db
}

func investmentPurchasesSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var snapshot []string
	for _, query := range []string{
		`SELECT json_array(id, workspace_id, account_id, name, institution, kind, principal_cents, tax_regime, applied_date, maturity_date, yield_note, status, redeemed_date, redeemed_by, archived_at, created_by, created_at, updated_at) FROM investments ORDER BY workspace_id, id`,
		`SELECT json_array(id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at, updated_by, updated_at) FROM investment_valuations ORDER BY workspace_id, id`,
		`SELECT id || ':' || current_balance || ':' || initial_balance FROM accounts ORDER BY id`,
		`SELECT id || ':' || amount || ':' || description FROM transactions ORDER BY id`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			snapshot = append(snapshot, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return snapshot
}

func TestMigration70LegacyConversionAtomicAndIdempotent(t *testing.T) {
	db := investmentPurchasesLegacyDB(t)
	mustExecOK(t, db, insertValidInvestment)
	mustExecOK(t, db, `INSERT INTO investment_valuations (id, workspace_id, investment_id, valuation_date, amount_cents, created_by) VALUES ('old-position','ws-1','inv-1','2026-02-01',110000,'user-1')`)
	// Simulate editable metadata changes and old administrative closures.
	mustExecOK(t, db, `UPDATE investments SET kind='LCI', tax_regime='ISENTO_PF', maturity_date='2032-01-01' WHERE workspace_id='ws-1' AND id='inv-1'`)
	for _, id := range []string{"closed", "archived"} {
		mustExecOK(t, db, `INSERT INTO investments (id,workspace_id,account_id,name,kind,principal_cents,tax_regime,applied_date,status,redeemed_date,redeemed_by,archived_at,created_by)
		VALUES (?, 'ws-1','acc-inv','Closed','CDB',22222,'MANUAL','2025-01-01','RESGATADA','2026-01-01','user-1',?,'user-1')`, id, map[string]any{"closed": nil, "archived": int64(42)}[id])
	}
	mustExecOK(t, db, `INSERT INTO accounts (id,workspace_id,name,type) VALUES ('acc-other','ws-2','Other','INVESTMENT')`)
	mustExecOK(t, db, `INSERT INTO investments (id,workspace_id,account_id,name,kind,principal_cents,tax_regime,applied_date,created_by) VALUES ('foreign','ws-2','acc-other','Other','CDB',1,'MANUAL','2026-01-01','user-2')`)
	before := investmentPurchasesSnapshot(t, db)
	if err := runMigrationsThroughVersionForTest(db, 70); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 70)
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 70 || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
		t.Fatalf("migration runner did not preserve the reserved v69 gap: max=%d", got)
	}
	if !reflect.DeepEqual(before, investmentPurchasesSnapshot(t, db)) {
		t.Fatal("conversion changed existing financial data")
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_purchases WHERE is_initial=1`); got != 4 {
		t.Fatalf("initial count=%d", got)
	}
	for _, workspace := range []string{"ws-1", "ws-2"} {
		if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investments i WHERE i.workspace_id=? AND
			(SELECT COUNT(*) FROM investment_purchases p WHERE p.workspace_id=? AND p.investment_id=i.id AND p.is_initial=1) != 1`, workspace, workspace); got != 0 {
			t.Fatalf("workspace %s has %d legacy investments without exactly one initial purchase", workspace, got)
		}
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_purchases p JOIN investments i ON p.investment_id=i.id AND p.workspace_id=i.workspace_id
		WHERE p.principal_cents=i.principal_cents AND p.purchase_date=i.applied_date AND p.created_by=i.created_by AND p.created_at=i.created_at
		AND p.conditions_source='LEGACY_UNKNOWN' AND p.kind IS NULL AND p.tax_regime IS NULL AND p.maturity_date IS NULL`); got != 4 {
		t.Fatal("legacy history inferred or principal/date/authorship changed")
	}
	// Re-run both runner and the additive conversion function.
	if err := runMigrationsThroughVersionForTest(db, 70); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateInvestmentPurchases(tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_purchases`); got != 4 {
		t.Fatal("conversion duplicated initial purchases")
	}
	assertMigrationApplied(t, db, 70)
	if !reflect.DeepEqual(before, investmentPurchasesSnapshot(t, db)) {
		t.Fatal("reinitialization changed legacy investments, positions or financial data")
	}
	if schemaObjectExists(t, db, "table", "investment_withdrawals") || schemaObjectExists(t, db, "table", "r7_invoice_provenance") {
		t.Fatal("unrequested phase created")
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}

func TestMigration70ConversionFailureRollsBackSchemaAndRegistration(t *testing.T) {
	db := investmentPurchasesLegacyDB(t)
	mustExecOK(t, db, insertValidInvestment)
	mustExecOK(t, db, `INSERT INTO investments (id,workspace_id,account_id,name,kind,principal_cents,tax_regime,applied_date,created_by) VALUES ('invalid-date','ws-1','acc-inv','Bad date','CDB',1,'MANUAL','2026-02-31','user-1')`)
	before := investmentPurchasesSnapshot(t, db)
	if err := runMigrations(db); err == nil || !strings.Contains(err.Error(), "apply migration 70") || !strings.Contains(err.Error(), "invalid legacy civil date") {
		t.Fatalf("expected migration 70 to reject the legacy calendar date, got %v", err)
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE tbl_name='investment_purchases' OR name='trg_investments_initial_purchase'`) != 0 || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=70`) != 0 {
		t.Fatal("failed conversion committed schema or version")
	}
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 68 {
		t.Fatalf("failed conversion changed the legacy migration version to %d", got)
	}
	if !reflect.DeepEqual(before, investmentPurchasesSnapshot(t, db)) {
		t.Fatal("failed conversion changed legacy rows")
	}
}

func TestMigration70PurchaseConstraintsAndInitialCreation(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_purchases WHERE workspace_id='ws-1' AND investment_id='inv-1' AND is_initial=1 AND conditions_source='RECORDED'`); got != 1 {
		t.Fatal("new investment has no atomic initial purchase")
	}
	statement := `INSERT INTO investment_purchases (id,workspace_id,investment_id,purchase_date,principal_cents,kind,institution,tax_regime,conditions_note,conditions_source,is_initial,created_by)
		VALUES (?, ?, ?, ?, ?, 'CDB','','MANUAL','','RECORDED',0,'user-1')`
	for i, tc := range []struct {
		ws, inv, date string
		cents         any
	}{
		{"ws-2", "inv-1", "2026-02-01", 100}, {"ws-1", "missing", "2026-02-01", 100},
		{"ws-1", "inv-1", "2025-12-31", 100}, {"ws-1", "inv-1", "2026-02-01", 0},
		{"ws-1", "inv-1", "2026-02-01", -1}, {"ws-1", "inv-1", "2026-02-01", 1.5},
		{"ws-1", "inv-1", "2026-02-01", int64(9223372036854775807)},
	} {
		mustExecError(t, db, statement, fmt.Sprint(i), tc.ws, tc.inv, tc.date, tc.cents)
	}
	mustExecOK(t, db, statement, "reapply", "ws-1", "inv-1", "2026-02-01", 200000)
	mustExecError(t, db, `UPDATE investment_purchases SET principal_cents=1 WHERE workspace_id='ws-1' AND id='reapply'`)
	mustExecError(t, db, `INSERT OR REPLACE INTO investment_purchases SELECT * FROM investment_purchases WHERE workspace_id='ws-1' AND id='reapply'`)
	mustExecError(t, db, `INSERT OR REPLACE INTO investment_purchases SELECT * FROM investment_purchases WHERE workspace_id='ws-1' AND is_initial=1`)
	mustExecOK(t, db, `UPDATE investments SET status='RESGATADA',redeemed_date='2026-03-01',redeemed_by='user-1' WHERE workspace_id='ws-1' AND id='inv-1'`)
	mustExecError(t, db, statement, "closed", "ws-1", "inv-1", "2026-04-01", 100)
	mustExecOK(t, db, `UPDATE investments SET archived_at=1 WHERE workspace_id='ws-1' AND id='inv-1'`)
	mustExecError(t, db, statement, "archived", "ws-1", "inv-1", "2026-04-01", 100)
}
