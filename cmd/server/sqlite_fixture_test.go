package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
	"modernc.org/sqlite"
)

// Legacy unit fixtures may prepare corrupt data or SQL batches. Their raw
// administrative handle is test-only; actual runtime owners remain guarded.
func openRawServerFixture(t *testing.T) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "legacy-fixture"), RawQuery: "mode=memory&cache=shared&_pragma=foreign_keys(1)"}).String()
	anchor, err := database.Open(dsn)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { anchor.Close() })
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	t.Cleanup(func() { db.Close() })
	return db, nil
}
func serverFixtureExec(t *testing.T, db *sql.DB, q string, args ...any) (sql.Result, error) {
	t.Helper()
	if _, raw := db.Driver().(*sqlite.Driver); raw {
		return db.Exec(q, args...)
	}
	var path string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("fixture requires raw named memory or disk maintenance")
	}
	admin, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}).String())
	if err != nil {
		return nil, err
	}
	defer admin.Close()
	admin.SetMaxOpenConns(1)
	return admin.Exec(q, args...)
}
