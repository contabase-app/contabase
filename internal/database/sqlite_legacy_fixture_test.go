package database

import (
	"database/sql"
	"testing"
)

// Historical SQL/DDL corruption probes use the existing raw SQLite fixture.
// Runtime/opening/78 tests continue to exercise the guarded production opener.
func openLegacySQLiteFixture(t *testing.T) (*sql.DB, error) {
	db := openInvestmentsTestDB(t)
	if err := runMigrations(db); err != nil {
		return nil, err
	}
	return db, nil
}
