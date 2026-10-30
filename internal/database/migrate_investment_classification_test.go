package database

import (
	"database/sql"
	"reflect"
	"testing"
)

func TestMigration76ClassificationAdditiveAtomic(t *testing.T) {
	t.Run("partial category schema", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		mustExecOK(t, db, `CREATE TABLE categories(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL)`)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = migrateInvestmentClassification(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if !schemaObjectExists(t, db, "index", "idx_c2b_category_tenant") || schemaObjectExists(t, db, "table", "investment_result_classifications") {
			t.Fatal("partial schema migration differs")
		}
	})
	t.Run("upgrade75", func(t *testing.T) {
		db := referenceLegacy71(t)
		if err := runMigrationsThroughVersionForTest(db, 75); err != nil {
			t.Fatal(err)
		}
		before := referenceLegacySnapshot(t, db)
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		assertMigrationApplied(t, db, 76)
		if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
			t.Fatal("legacy finances changed")
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM transactions WHERE effect_mode!='STANDARD'`) != 0 {
			t.Fatal("legacy mode changed")
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_result_classifications`) != 0 {
			t.Fatal("automatic activation")
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = migrateInvestmentClassification(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err = assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
			t.Fatal("reserved migration consumed")
		}
	})
	t.Run("rollback", func(t *testing.T) {
		db := referenceLegacy71(t)
		if err := runMigrationsThroughVersionForTest(db, 75); err != nil {
			t.Fatal(err)
		}
		mustExecOK(t, db, `CREATE TABLE idx_c2b_category_tenant(id TEXT)`)
		if err := runMigrations(db); err == nil {
			t.Fatal("expected migration failure")
		}
		if schemaObjectExists(t, db, "table", "investment_result_classifications") || schemaObjectExists(t, db, "view", "financial_transactions") || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=76`) != 0 {
			t.Fatal("partial migration persisted")
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('transactions') WHERE name='effect_mode'`).Scan(&count); err != nil || count != 0 {
			t.Fatal("column survived rollback", err)
		}
	})
	t.Run("fresh", func(t *testing.T) {
		db := investmentsFreshDB(t)
		assertMigrationApplied(t, db, 76)
		if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
	})
}
