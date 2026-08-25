package database

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureDailyAutomaticBackupIsImmutableWithinCivilDay(t *testing.T) {
	db, dbPath := openDailyBackupTestDB(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	loc := mustDailyBackupLocation(t, "America/Sao_Paulo")
	morning := time.Date(2026, 8, 25, 7, 30, 0, 0, loc)

	insertDailyBackupMarker(t, db, "before-import")
	if err := ensureDailyAutomaticBackup(db, dbPath, backupDir, morning, 7); err != nil {
		t.Fatalf("create morning daily backup: %v", err)
	}
	dailyPath := filepath.Join(backupDir, "contabase-daily-20260825.db")
	before := readDailyBackupBytes(t, dailyPath)
	assertDailyBackupReadOnly(t, dailyPath)

	insertDailyBackupMarker(t, db, "after-import")
	if err := ensureDailyAutomaticBackup(db, dbPath, backupDir, morning.Add(8*time.Hour), 7); err != nil {
		t.Fatalf("repeat same-day daily backup: %v", err)
	}
	after := readDailyBackupBytes(t, dailyPath)
	if !bytes.Equal(before, after) {
		t.Fatal("same-day execution replaced the protected daily backup")
	}
	if got := dailyBackupMarkerCount(t, dailyPath); got != 1 {
		t.Fatalf("same-day backup marker count = %d, want 1", got)
	}
	items, err := ListDailyAutomaticBackups(backupDir, 7)
	if err != nil {
		t.Fatalf("list daily backups: %v", err)
	}
	if len(items) != 1 || items[0].Name != "contabase-daily-20260825.db" {
		t.Fatalf("daily backups = %+v, want one immutable day", items)
	}
}

func TestEnsureDailyAutomaticBackupRetainsSevenCivilDays(t *testing.T) {
	db, dbPath := openDailyBackupTestDB(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	loc := mustDailyBackupLocation(t, "UTC")
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, loc)
	preRestoreDir := filepath.Join(backupDir, "pre-restore")
	if err := os.MkdirAll(preRestoreDir, 0o755); err != nil {
		t.Fatalf("create pre-restore fixture dir: %v", err)
	}
	preRestorePath := filepath.Join(preRestoreDir, "contabase-pre-restore-20260817-090000.db")
	if err := os.WriteFile(preRestorePath, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("write pre-restore fixture: %v", err)
	}

	for day := 0; day < 9; day++ {
		insertDailyBackupMarker(t, db, time.Date(2026, 8, 17+day, 0, 0, 0, 0, loc).Format("20060102"))
		if err := ensureDailyAutomaticBackup(db, dbPath, backupDir, start.AddDate(0, 0, day), 7); err != nil {
			t.Fatalf("create daily backup %d: %v", day, err)
		}
	}

	items, err := ListDailyAutomaticBackups(backupDir, 10)
	if err != nil {
		t.Fatalf("list retained daily backups: %v", err)
	}
	if len(items) != 7 {
		t.Fatalf("retained backups = %d, want 7: %+v", len(items), items)
	}
	if items[0].Name != "contabase-daily-20260825.db" || items[6].Name != "contabase-daily-20260819.db" {
		t.Fatalf("retention window = %+v, want 20260819 through 20260825", items)
	}
	if _, err := os.Stat(filepath.Join(backupDir, "contabase-daily-20260818.db")); !os.IsNotExist(err) {
		t.Fatalf("expired daily backup still exists: %v", err)
	}
	if got := string(readDailyBackupBytes(t, preRestorePath)); got != "preserve" {
		t.Fatalf("pre-restore backup changed: %q", got)
	}
}

func TestEnsureDailyAutomaticBackupAdoptsEarliestLegacySnapshotAndRemovesDuplicates(t *testing.T) {
	db, dbPath := openDailyBackupTestDB(t)
	backupDir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("create backup dir: %v", err)
	}

	insertDailyBackupMarker(t, db, "first-state")
	firstLegacy := filepath.Join(backupDir, "contabase-20260825-075345.db")
	if err := CreateSQLiteBackupFile(db, dbPath, firstLegacy); err != nil {
		t.Fatalf("create first legacy backup: %v", err)
	}
	insertDailyBackupMarker(t, db, "later-state")
	for _, name := range []string{"contabase-20260825-132258.db", "contabase-20260825-151450.db"} {
		if err := CreateSQLiteBackupFile(db, dbPath, filepath.Join(backupDir, name)); err != nil {
			t.Fatalf("create legacy backup %s: %v", name, err)
		}
	}

	now := time.Date(2026, 8, 25, 16, 0, 0, 0, mustDailyBackupLocation(t, "America/Sao_Paulo"))
	if err := ensureDailyAutomaticBackup(db, dbPath, backupDir, now, 7); err != nil {
		t.Fatalf("adopt legacy daily backup: %v", err)
	}
	dailyPath := filepath.Join(backupDir, "contabase-daily-20260825.db")
	if got := dailyBackupMarkerCount(t, dailyPath); got != 1 {
		t.Fatalf("adopted daily marker count = %d, want earliest state with 1 marker", got)
	}
	assertDailyBackupReadOnly(t, dailyPath)
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("read migrated backup dir: %v", err)
	}
	for _, entry := range entries {
		if _, ok := parseLegacyAutomaticBackupName(entry.Name()); ok {
			t.Fatalf("legacy duplicate remained after migration: %s", entry.Name())
		}
	}
}

func TestListDailyAutomaticBackupsRejectsLegacySymlinkAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "contabase-daily-20260825.db")
	if err := os.WriteFile(validPath, []byte("daily"), 0o444); err != nil {
		t.Fatalf("write valid daily fixture: %v", err)
	}
	for _, name := range []string{
		"contabase-20260825-151450.db",
		"contabase-daily-20260825.db",
		"contabase-daily-20260230.db",
		"contabase-daily-20260824.txt",
	} {
		if name == filepath.Base(validPath) {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ignored"), 0o600); err != nil {
			t.Fatalf("write ignored fixture %s: %v", name, err)
		}
	}
	if err := os.Symlink(validPath, filepath.Join(dir, "contabase-daily-20260826.db")); err != nil {
		t.Logf("symlink fixture unavailable: %v", err)
	}

	items, err := ListDailyAutomaticBackups(dir, 7)
	if err != nil {
		t.Fatalf("list safe daily backups: %v", err)
	}
	if len(items) != 1 || items[0].Name != filepath.Base(validPath) {
		t.Fatalf("daily backup filter returned %+v", items)
	}
}

func openDailyBackupTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "daily-source.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open daily source db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("enable daily source WAL: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE backup_markers (value TEXT NOT NULL)`); err != nil {
		t.Fatalf("create daily source fixture: %v", err)
	}
	return db, dbPath
}

func insertDailyBackupMarker(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO backup_markers (value) VALUES (?)`, value); err != nil {
		t.Fatalf("insert daily backup marker: %v", err)
	}
}

func dailyBackupMarkerCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open daily backup read-only: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_markers`).Scan(&count); err != nil {
		t.Fatalf("query daily backup markers: %v", err)
	}
	return count
}

func assertDailyBackupReadOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat daily backup: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("daily backup mode = %04o, want no write bits", info.Mode().Perm())
	}
}

func readDailyBackupBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read daily backup fixture: %v", err)
	}
	return data
}

func mustDailyBackupLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestParseLegacyAutomaticBackupName(t *testing.T) {
	for _, name := range []string{
		"contabase-20260825-151450.db",
		"contabase-20260825-151450-01.db",
	} {
		if _, ok := parseLegacyAutomaticBackupName(name); !ok {
			t.Fatalf("expected legacy name %q to parse", name)
		}
	}
	for _, name := range []string{
		"contabase-backup-20260825-151450.db",
		"contabase-daily-20260825.db",
		"contabase-20260825-151450-extra.db",
		"contabase-20260825-15145.db",
		"other-20260825-151450.db",
	} {
		if _, ok := parseLegacyAutomaticBackupName(name); ok {
			t.Fatalf("unexpected legacy name accepted: %q", name)
		}
	}
}

func TestDailyBackupNamesDoNotContainPaths(t *testing.T) {
	day := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	name := dailyAutomaticBackupName(day)
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		t.Fatalf("unsafe daily backup name: %q", name)
	}
}
