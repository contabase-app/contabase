package database

import (
	"reflect"
	"testing"
)

func TestMigration75AdditiveIdempotentAndAtomic(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		db := investmentsFreshDB(t)
		assertMigrationApplied(t, db, 75)
		for _, table := range []string{"investment_settlement_applications", "investment_settlement_prior_effects"} {
			if countQueryRows(t, db, `SELECT COUNT(*) FROM `+table) != 0 {
				t.Fatal("automatic financial activation")
			}
		}
		if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("upgrade74", func(t *testing.T) {
		db := referenceLegacy71(t)
		if err := runMigrationsThroughVersionForTest(db, 74); err != nil {
			t.Fatal(err)
		}
		before := referenceLegacySnapshot(t, db)
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		assertMigrationApplied(t, db, 75)
		if err := runMigrations(db); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = migrateInvestmentSettlementFinancial(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
			t.Fatal("legacy balances changed")
		}
		if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
			t.Fatal("reserved migration used")
		}
		if err = assertDatabaseForeignKeyIntegrity(db); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rollback", func(t *testing.T) {
		db := referenceLegacy71(t)
		if err := runMigrationsThroughVersionForTest(db, 74); err != nil {
			t.Fatal(err)
		}
		before := referenceLegacySnapshot(t, db)
		mustExecOK(t, db, `CREATE TABLE idx_settlement_application_date(id TEXT)`)
		if err := runMigrations(db); err == nil {
			t.Fatal("expected migration error")
		}
		if schemaObjectExists(t, db, "table", "investment_settlement_applications") || schemaObjectExists(t, db, "view", "investment_adjustment_economic_dates") || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=75`) != 0 {
			t.Fatal("partial migration survived")
		}
		if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
			t.Fatal("legacy mutation")
		}
	})
}
