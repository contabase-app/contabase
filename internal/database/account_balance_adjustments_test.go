package database

import (
	"database/sql"
	"testing"
)

func accountAdjustmentLegacyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version >= 64 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			t.Fatalf("legacy migration %d: %v", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seedAppliedMigrationsBefore(t, db, 64)
	for _, stmt := range []string{
		`INSERT INTO workspaces (id,name) VALUES ('balance-ws','Balance workspace')`,
		`INSERT INTO users (id,name,email,password_hash) VALUES ('balance-user','Author','balance@example.test','hash')`,
		`INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance) VALUES ('balance-account','balance-ws','Existing','CHECKING',12345,-7654)`,
		`INSERT INTO transactions (id,workspace_id,user_id,account_id,type,amount,date,description,status) VALUES ('existing-transaction','balance-ws','balance-user','balance-account','EXPENSE',19999,1,'Existing expense','paid')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestMigration64PreservesExistingFinancialData(t *testing.T) {
	db := accountAdjustmentLegacyDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 64)
	for i := 0; i < 2; i++ {
		var initial, current, version, amount int64
		if err := db.QueryRow(`SELECT a.initial_balance,a.current_balance,a.balance_version,t.amount FROM accounts a JOIN transactions t ON t.account_id=a.id WHERE a.id='balance-account'`).Scan(&initial, &current, &version, &amount); err != nil {
			t.Fatal(err)
		}
		if initial != 12345 || current != -7654 || version != 0 || amount != 19999 {
			t.Fatalf("upgrade changed existing data: %d/%d/%d/%d", initial, current, version, amount)
		}
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}

func TestMigration64RollsBackAdditiveSchemaOnFailure(t *testing.T) {
	db := accountAdjustmentLegacyDB(t)
	if _, err := db.Exec(`CREATE TABLE account_balance_adjustments (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db); err == nil {
		t.Fatal("expected incompatible table failure")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('accounts') WHERE name='balance_version'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partially committed balance_version column")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=64`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration marked applied")
	}
}

func TestMigration64HistoryIntegrityAndVersionTracking(t *testing.T) {
	db := accountAdjustmentLegacyDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET name='Renamed' WHERE id='balance-account'`); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := db.QueryRow(`SELECT balance_version FROM accounts WHERE id='balance-account'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatal("metadata advanced financial version")
	}
	if _, err := db.Exec(`INSERT INTO account_balance_adjustments (id,workspace_id,account_id,created_by,created_at,expected_version,before_balance,after_balance,delta_cents,reason)
		VALUES ('audit','balance-ws','balance-account','balance-user',1,0,-7654,0,7654,'Reconcile')`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE account_balance_adjustments SET reason='Changed' WHERE id='audit'`,
		`DELETE FROM account_balance_adjustments WHERE id='audit'`,
		`INSERT INTO account_balance_adjustments (id,workspace_id,account_id,created_by,created_at,expected_version,before_balance,after_balance,delta_cents,reason) VALUES ('bad','balance-ws','balance-account','balance-user',1,0,-7654,0,1,'Bad delta')`,
	} {
		if _, err := db.Exec(stmt); err == nil {
			t.Fatalf("accepted invalid history: %s", stmt)
		}
	}
	if _, err := db.Exec(`UPDATE accounts SET current_balance=0 WHERE id='balance-account'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT balance_version FROM accounts WHERE id='balance-account'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version=%d, want 1", version)
	}
}
