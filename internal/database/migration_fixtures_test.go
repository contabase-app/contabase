package database

import (
	"database/sql"
	"strings"
	"testing"
)

func migration77Fixture(t *testing.T) *sql.DB {
	t.Helper()
	db := openInvestmentsTestDB(t)
	seedAppliedMigrationsBefore(t, db, 1)
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	return db
}

func completeHistoricalBaselineForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	// Retain each historical table verbatim. Supply only missing baseline
	// tables so opening tests remain distinct from partial-schema refusal.
	for _, statement := range strings.Split(ddl, ";") {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TABLE IF NOT EXISTS") {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
}
