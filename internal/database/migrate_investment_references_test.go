package database

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func referenceLegacy71(t *testing.T) *sql.DB {
	t.Helper()
	db := investmentPurchasesLegacyDB(t)
	mustExecOK(t, db, insertValidInvestment)
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('reference-position','ws-1','inv-1','2026-02-01',110000,'user-1')`)
	if err := runMigrationsThroughVersionForTest(db, 71); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `INSERT INTO investment_withdrawals(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id) VALUES('legacy-withdrawal','ws-1','inv-1','2026-01-20',100,'PARTIAL','user-1','11111111-1111-4111-8111-111111111111')`)
	mustExecOK(t, db, `INSERT INTO investment_withdrawal_allocations(workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase) VALUES('ws-1','inv-1','legacy-withdrawal','initial:inv-1',100,NULL,0)`)
	return db
}

func referenceLegacySnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	result := investmentPurchasesSnapshot(t, db)
	for _, query := range []string{
		`SELECT json_array(id,workspace_id,investment_id,purchase_date,principal_cents,conditions_source,created_by,created_at) FROM investment_purchases ORDER BY workspace_id,id`,
		`SELECT json_array(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id,note,created_at) FROM investment_withdrawals ORDER BY workspace_id,id`,
		`SELECT json_array(workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase) FROM investment_withdrawal_allocations ORDER BY workspace_id,withdrawal_id,purchase_id`,
		`SELECT json_array(id,workspace_id,account_id,delta_cents,created_at) FROM account_balance_adjustments ORDER BY id`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result = append(result, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestMigration72FreshUpgradeAndIdempotency(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		db := investmentsFreshDB(t)
		assertMigrationApplied(t, db, 72)
		if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_reference_revisions`) != 0 || countQueryRows(t, db, `SELECT COUNT(*) FROM investment_reference_coverage`) != 0 || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
			t.Fatal("unexpected preparation or reserved migration")
		}
	})
	t.Run("upgrade 71", func(t *testing.T) {
		db := referenceLegacy71(t)
		before := referenceLegacySnapshot(t, db)
		if schemaObjectExists(t, db, "table", "investment_reference_revisions") {
			t.Fatal("v72 applied early")
		}
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		assertMigrationApplied(t, db, 72)
		if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_reference_revisions`) != 0 {
			t.Fatal("legacy positions automatically reconciled")
		}
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = migrateInvestmentReferences(tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
			t.Fatal("upgrade modified legacy or financial rows")
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=72`) != 1 {
			t.Fatal("duplicate registration")
		}
		if err = assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMigration72FailureRollsBack(t *testing.T) {
	db := referenceLegacy71(t)
	before := referenceLegacySnapshot(t, db)
	// Fail at the final index, after the new tables and triggers were created.
	mustExecOK(t, db, `CREATE TABLE idx_reference_coverage_withdrawal(id TEXT)`)
	if err := runMigrations(db); err == nil || !strings.Contains(err.Error(), "apply migration 72") {
		t.Fatalf("expected migration failure: %v", err)
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=72`) != 0 || countQueryRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE tbl_name IN ('investment_reference_revisions','investment_reference_coverage') OR name IN ('idx_investments_reference_owner','idx_valuations_reference_owner')`) != 0 {
		t.Fatal("partial schema or registration survived")
	}
	if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
		t.Fatal("failed migration modified history")
	}
}

const insertReferenceFixture = `INSERT INTO investment_reference_revisions(workspace_id,investment_id,account_id,id,reference_id,version,expected_version,position_id,position_version,economic_date,represented_cents,confirmed,day_closed,confirmed_at,created_by,created_at,request_id,reason,snapshot_json,coverage_token,payload_json)
 VALUES(?,?,?,'ref','ref',1,0,?,1,?,?,1,1,1,'user-1',1,'22222222-2222-4222-8222-222222222222','Explicit confirmation','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','{}')`

func TestMigration72TenantMoneyDatesAndCoverageConstraints(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('foreign-account','ws-2','Other','INVESTMENT')`)
	mustExecOK(t, db, `INSERT INTO investments(id,workspace_id,account_id,name,kind,principal_cents,tax_regime,applied_date,created_by) VALUES('foreign','ws-2','foreign-account','Other','CDB',100,'MANUAL','2026-01-01','user-2'),('other','ws-1','acc-inv','Other','CDB',100,'MANUAL','2026-01-01','user-1')`)
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('foreign-position','ws-2','foreign','2026-02-01',100,'user-2'),('other-position','ws-1','other','2026-02-01',100,'user-1')`)
	for _, owner := range [][]string{
		{"ws-2", "inv-1", "foreign-account", "reference-position"},
		{"ws-1", "inv-1", "foreign-account", "reference-position"},
		{"ws-1", "inv-1", "acc-checking", "reference-position"},
		{"ws-1", "inv-1", "acc-inv", "foreign-position"},
		{"ws-1", "inv-1", "acc-inv", "other-position"},
	} {
		mustExecError(t, db, insertReferenceFixture, owner[0], owner[1], owner[2], owner[3], "2026-02-01", 100)
	}
	for _, date := range []any{nil, "2026-02-31", "2026-02-29", "1900-02-29", "0000-01-01", "2026-04-31", "2026-02-01\x00", "2026-2-01"} {
		mustExecError(t, db, insertReferenceFixture, "ws-1", "inv-1", "acc-inv", "reference-position", date, 100)
	}
	for _, value := range []any{nil, -1, 1.5, "9223372036854775808"} {
		mustExecError(t, db, insertReferenceFixture, "ws-1", "inv-1", "acc-inv", "reference-position", "2026-02-01", value)
	}
	mustExecOK(t, db, insertReferenceFixture, "ws-1", "inv-1", "acc-inv", "reference-position", "2026-02-01", 0)
	coverage := `INSERT INTO investment_reference_coverage(workspace_id,investment_id,revision_id,kind,operation_id,purchase_id) VALUES(?,?,'ref','PURCHASE',?,?)`
	mustExecError(t, db, coverage, "ws-1", "inv-1", "initial:foreign", "initial:foreign")
	mustExecError(t, db, coverage, "ws-1", "inv-1", "initial:other", "initial:other")
	mustExecError(t, db, coverage, "ws-2", "foreign", "initial:foreign", "initial:foreign")
	mustExecOK(t, db, coverage, "ws-1", "inv-1", "initial:inv-1", "initial:inv-1")
	mustExecOK(t, db, `INSERT INTO investment_reference_coverage(workspace_id,investment_id,revision_id,kind,operation_id,withdrawal_id) VALUES('ws-1','inv-1','ref','WITHDRAWAL','legacy-withdrawal','legacy-withdrawal')`)
	mustExecError(t, db, `UPDATE investment_reference_revisions SET integration_state='APPLIED'`)
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}
