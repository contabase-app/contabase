package main

import (
	"database/sql"
	"github.com/contabase-app/contabase/internal/database"
	"net/url"
	"path/filepath"
	"testing"
)

func openRawAdminFixture(t *testing.T) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "orphan-fixture"), RawQuery: "mode=memory&cache=shared&_pragma=foreign_keys(1)"}).String()
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
	t.Cleanup(func() { db.Close() })
	return db, nil
}
