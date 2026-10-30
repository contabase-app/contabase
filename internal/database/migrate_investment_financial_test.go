package database

import (
	"reflect"
	"testing"
)

func TestMigration73AdditiveAndNoActivation(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrationsThroughVersionForTest(db, 72); err != nil {
		t.Fatal(err)
	}
	before := referenceLegacySnapshot(t, db)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 73)
	for _, table := range []string{"investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding"} {
		if countQueryRows(t, db, `SELECT COUNT(*) FROM `+table) != 0 {
			t.Fatal("legacy activation", table)
		}
	}
	if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
		t.Fatal("migration changed existing financial rows")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = migrateInvestmentFinancialIntegration(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
		t.Fatal("reserved migration occupied")
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}

func TestMigration73FailureAtomic(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrationsThroughVersionForTest(db, 72); err != nil {
		t.Fatal(err)
	}
	before := referenceLegacySnapshot(t, db)
	mustExecOK(t, db, `CREATE TABLE investment_financial_adjustments(broken TEXT)`)
	if err := runMigrations(db); err == nil {
		t.Fatal("broken target schema accepted")
	}
	if schemaObjectExists(t, db, "table", "investment_financial_revisions") || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=73`) != 0 {
		t.Fatal("partial migration committed")
	}
	if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
		t.Fatal("migration failure mutated finances")
	}
}
