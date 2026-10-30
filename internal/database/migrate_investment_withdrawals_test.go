package database

import (
	"reflect"
	"strings"
	"testing"
)

func TestMigration71AdditiveUpgradeAndTenantConstraints(t *testing.T) {
	t.Run("migration failure rolls back schema and registration", func(t *testing.T) {
		db := investmentPurchasesLegacyDB(t)
		mustExecOK(t, db, insertValidInvestment)
		if err := runMigrationsThroughVersionForTest(db, 70); err != nil {
			t.Fatal(err)
		}
		before := investmentPurchasesSnapshot(t, db)
		// A name collision fails the final index creation after both tables.
		mustExecOK(t, db, `CREATE TABLE idx_investment_withdrawals_purchase(id TEXT)`)
		if err := runMigrations(db); err == nil || !strings.Contains(err.Error(), "apply migration 71") {
			t.Fatalf("expected migration 71 failure, got %v", err)
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('investment_withdrawals','investment_withdrawal_allocations','idx_investment_purchases_owner','idx_investment_withdrawals_date')`) != 0 || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=71`) != 0 {
			t.Fatal("failed migration left partial schema or registration")
		}
		if !reflect.DeepEqual(before, investmentPurchasesSnapshot(t, db)) || countQueryRows(t, db, `SELECT COUNT(*) FROM investment_purchases`) != 1 {
			t.Fatal("failed migration changed existing data")
		}
	})
	db := investmentPurchasesLegacyDB(t)
	mustExecOK(t, db, insertValidInvestment)
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('position','ws-1','inv-1','2026-02-01',123456,'user-1')`)
	if err := runMigrationsThroughVersionForTest(db, 70); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `UPDATE investments SET status='RESGATADA',redeemed_date='2026-03-01',redeemed_by='user-1' WHERE workspace_id='ws-1' AND id='inv-1'`)
	before := investmentPurchasesSnapshot(t, db)
	if schemaObjectExists(t, db, "table", "investment_withdrawals") {
		t.Fatal("v71 applied early")
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 71)
	if !schemaObjectExists(t, db, "table", "investment_withdrawals") || !schemaObjectExists(t, db, "table", "investment_withdrawal_allocations") {
		t.Fatal("migration 71 tables missing")
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
		t.Fatal("reserved migration claimed")
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_withdrawals`) != 0 || !reflect.DeepEqual(before, investmentPurchasesSnapshot(t, db)) {
		t.Fatal("upgrade inferred withdrawals or mutated history")
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('foreign-acc','ws-2','Other','INVESTMENT')`)
	mustExecOK(t, db, `INSERT INTO investments(id,workspace_id,account_id,name,kind,principal_cents,tax_regime,applied_date,created_by) VALUES('foreign','ws-2','foreign-acc','Other','CDB',100,'MANUAL','2026-01-01','user-2'),('same-tenant','ws-1','acc-inv','Other','CDB',100,'MANUAL','2026-01-01','user-1')`)
	event := `INSERT INTO investment_withdrawals(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id) VALUES('w','ws-1','inv-1',?,?,'PARTIAL','user-1','11111111-1111-4111-8111-111111111111')`
	for _, date := range []any{nil, "2026-02-31", "2026-02-29", "1900-02-29", "2026-04-31", "2026-13-01", "0000-01-01", "2026-00-01", "2026-01-00", "2026-2-28", "2026-10-15extra", "2026-10-15\x00"} {
		mustExecError(t, db, event, date, 100)
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_withdrawals`) != 0 {
		t.Fatal("invalid dates left withdrawal rows")
	}
	for _, date := range []string{"2026-02-28", "2024-02-29", "2000-02-29", "2026-10-15", "0001-01-01", "9999-12-31"} {
		mustExecOK(t, db, event, date, 100)
		mustExecOK(t, db, `DELETE FROM investment_withdrawals WHERE workspace_id='ws-1' AND investment_id='inv-1' AND id='w'`)
	}
	mustExecError(t, db, `INSERT INTO investment_withdrawals(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id) VALUES('foreign-event','ws-2','inv-1','2026-03-01',100,'PARTIAL','user-2','22222222-2222-4222-8222-222222222222')`)
	mustExecError(t, db, event, "2026-03-01", -1)
	mustExecOK(t, db, event, "2026-03-01", 100)
	mustExecError(t, db, event, "2026-03-01", 100)
	allocation := `INSERT INTO investment_withdrawal_allocations(workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase) VALUES(?,?,?, ?,100,?,0)`
	mustExecError(t, db, allocation, "ws-1", "inv-1", "w", "initial:foreign", nil)
	mustExecError(t, db, allocation, "ws-1", "inv-1", "w", "initial:same-tenant", nil)
	mustExecError(t, db, allocation, "ws-2", "foreign", "w", "initial:foreign", nil)
	mustExecError(t, db, allocation, "ws-1", "inv-1", "missing", "initial:inv-1", nil)
	mustExecError(t, db, allocation, "ws-1", "inv-1", "w", "initial:inv-1", -1)
	mustExecOK(t, db, allocation, "ws-1", "inv-1", "w", "initial:inv-1", nil)
	mustExecError(t, db, allocation, "ws-1", "inv-1", "w", "initial:inv-1", nil)
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}
