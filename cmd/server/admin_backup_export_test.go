package main

import (
	"database/sql"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminExportBackupUsesProvidedDatabasePath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "custom-contabase.db")
	db := openExportTestDB(t, dbPath)
	defer db.Close()
	insertExportMarker(t, db, "custom-db")

	backupPath, cleanup, err := createAdminExportBackup(db, dbPath)
	if err != nil {
		t.Fatalf("create admin export backup: %v", err)
	}
	defer cleanup()

	if !isSQLiteFile(backupPath) {
		t.Fatalf("expected exported backup to be a sqlite file")
	}
	assertExportMarker(t, backupPath, "custom-db")
}

func TestAdminExportBackupCheckpointsWALBeforeCopy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wal-contabase.db")
	db := openExportTestDB(t, dbPath)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatalf("disable wal autocheckpoint: %v", err)
	}
	insertExportMarker(t, db, "wal-visible")

	backupPath, cleanup, err := createAdminExportBackup(db, dbPath)
	if err != nil {
		t.Fatalf("create admin export backup: %v", err)
	}
	defer cleanup()

	assertExportMarker(t, backupPath, "wal-visible")
}

func TestAdminBackupDownloadHeadersAreSafe(t *testing.T) {
	rr := httptest.NewRecorder()
	now := time.Date(2026, 5, 31, 12, 34, 56, 0, time.UTC)

	setAdminBackupDownloadHeaders(rr, now)

	assertHeader(t, rr, "Content-Type", "application/vnd.sqlite3")
	assertHeader(t, rr, "Content-Disposition", "attachment; filename=\"contabase-backup-20260531-123456.db\"")
	assertHeader(t, rr, "Cache-Control", "no-store")
	assertHeader(t, rr, "X-Content-Type-Options", "nosniff")
	if cd := rr.Header().Get("Content-Disposition"); strings.ContainsAny(cd, `/\`) {
		t.Fatalf("content disposition leaks path separators: %q", cd)
	}
}

func TestAdminBackupDownloadBodyIsSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "download-contabase.db")
	db := openExportTestDB(t, dbPath)
	defer db.Close()
	insertExportMarker(t, db, "download")

	backupPath, cleanup, err := createAdminExportBackup(db, dbPath)
	if err != nil {
		t.Fatalf("create admin export backup: %v", err)
	}
	defer cleanup()

	f, err := os.Open(backupPath)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer f.Close()

	rr := httptest.NewRecorder()
	setAdminBackupDownloadHeaders(rr, time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC))
	if _, err := io.Copy(rr, f); err != nil {
		t.Fatalf("copy backup response: %v", err)
	}

	downloadPath := filepath.Join(t.TempDir(), "downloaded.db")
	if err := os.WriteFile(downloadPath, rr.Body.Bytes(), 0o600); err != nil {
		t.Fatalf("write downloaded backup: %v", err)
	}
	if !isSQLiteFile(downloadPath) {
		t.Fatalf("expected response body to be a sqlite file")
	}
	assertExportMarker(t, downloadPath, "download")
}

func openExportTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		t.Fatalf("enable wal: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE export_marker (value TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create marker table: %v", err)
	}
	return db
}

func insertExportMarker(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO export_marker (value) VALUES (?)`, value); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
}

func assertExportMarker(t *testing.T, dbPath, want string) {
	t.Helper()
	backupDB, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open backup sqlite: %v", err)
	}
	defer backupDB.Close()

	var got string
	if err := backupDB.QueryRow(`SELECT value FROM export_marker LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("query marker from backup: %v", err)
	}
	if got != want {
		t.Fatalf("marker = %q, want %q", got, want)
	}
}
