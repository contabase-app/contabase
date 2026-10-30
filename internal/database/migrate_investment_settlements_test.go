package database

import (
	"reflect"
	"testing"
)

func TestMigration74FreshUpgradeIdempotentAndLegacy(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db := investmentsFreshDB(t)
		assertMigrationApplied(t, db, 74)
		for _, table := range []string{"investment_settlement_revisions", "investment_settlement_receipts"} {
			if countQueryRows(t, db, `SELECT COUNT(*) FROM `+table) != 0 {
				t.Fatal("automatic settlement", table)
			}
		}
	})
	t.Run("upgrade73", func(t *testing.T) {
		db := referenceLegacy71(t)
		if err := runMigrationsThroughVersionForTest(db, 73); err != nil {
			t.Fatal(err)
		}
		before := referenceLegacySnapshot(t, db)
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		assertMigrationApplied(t, db, 74)
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = migrateInvestmentSettlements(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
			t.Fatal("legacy mutation")
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=74`) != 1 || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
			t.Fatal("migration registration")
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 0 {
			t.Fatal("legacy activated")
		}
		if err = assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMigration74AtomicFailure(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrationsThroughVersionForTest(db, 73); err != nil {
		t.Fatal(err)
	}
	before := referenceLegacySnapshot(t, db)
	mustExecOK(t, db, `CREATE TABLE idx_settlement_receipt_source(id TEXT)`)
	if err := runMigrations(db); err == nil {
		t.Fatal("expected migration error")
	}
	if schemaObjectExists(t, db, "table", "investment_settlement_revisions") || schemaObjectExists(t, db, "table", "investment_settlement_receipts") || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=74`) != 0 {
		t.Fatal("partial migration survived")
	}
	if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
		t.Fatal("legacy mutation")
	}
}

const settlementSQLFixture = `INSERT INTO investment_settlement_revisions
(workspace_id,investment_id,withdrawal_id,id,version,account_id,destination_account_id,economic_date,gross_cents,ir_cents,iof_cents,fees_cents,net_cents,patrimony_evidence,receipt_evidence,state,request_id,created_by,created_at,reason,payload_json,result_json,withdrawal_json)
VALUES(?,?,?,'settlement',1,?,?,?,100,0,0,0,100,'','','PENDENTE','33333333-3333-4333-8333-333333333333','user-1',1,'Manual evidence','{}','{}','{}')`

func TestMigration74TenantDatesMoneyAndImmutability(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	for _, owner := range [][]string{
		{"ws-2", "inv-1", "legacy-withdrawal", "acc-inv", "acc-checking"},
		{"ws-1", "missing", "legacy-withdrawal", "acc-inv", "acc-checking"},
		{"ws-1", "inv-1", "missing", "acc-inv", "acc-checking"},
		{"ws-1", "inv-1", "legacy-withdrawal", "acc-checking", "acc-checking"},
		{"ws-1", "inv-1", "legacy-withdrawal", "acc-inv", "missing"},
	} {
		mustExecError(t, db, settlementSQLFixture, owner[0], owner[1], owner[2], owner[3], owner[4], "2026-01-20")
	}
	for _, date := range []any{nil, "2026-02-29", "1900-02-29", "0000-01-01", "2026-04-31", "2026-1-20", "2026-01-20\x00"} {
		mustExecError(t, db, settlementSQLFixture, "ws-1", "inv-1", "legacy-withdrawal", "acc-inv", "acc-checking", date)
	}
	mustExecOK(t, db, settlementSQLFixture, "ws-1", "inv-1", "legacy-withdrawal", "acc-inv", "acc-checking", "2026-01-20")
	for _, stmt := range []string{
		`UPDATE investment_settlement_revisions SET state='CONCILIADA'`,
		`DELETE FROM investment_settlement_revisions`,
		`INSERT OR REPLACE INTO investment_settlement_revisions SELECT * FROM investment_settlement_revisions`,
		`INSERT INTO investment_settlement_receipts(workspace_id,investment_id,withdrawal_id,revision_id,transaction_id,destination_account_id,amount_cents,evidence,snapshot_json) VALUES('ws-1','inv-1','legacy-withdrawal','settlement','missing','acc-checking',100,'explicit','{}')`,
	} {
		mustExecError(t, db, stmt)
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}
