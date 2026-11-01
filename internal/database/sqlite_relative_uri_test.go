package database

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A relative path must never gain an URI authority. file://data/db makes
// SQLite read "data" as the authority and refuse the read-only preflight.
func TestSQLiteBackupURIRelativePathOmitsAuthority(t *testing.T) {
	relative := filepath.Join("data", "contabase.db")
	uri := sqliteBackupURI(relative, "mode=ro")
	if strings.HasPrefix(uri, "file://") {
		t.Fatalf("relative URI carries an authority: %s", uri)
	}
	if want := "file:" + filepath.ToSlash(relative) + "?mode=ro"; uri != want {
		t.Fatalf("uri=%q want=%q", uri, want)
	}
}

func TestSQLiteRuntimeSchemaAcceptsRelativeAbsoluteAndSpacedPaths(t *testing.T) {
	base := t.TempDir()
	spacedDir := filepath.Join(base, "dados com espaço")
	if err := os.MkdirAll(spacedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(spacedDir, "contabase.db")

	owner, err := OpenRuntimeDBs("file:" + filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("open disposable database: %v", err)
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		_ = owner.Close()
		t.Fatalf("WAL file missing while database is open: %v", err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close disposable database: %v", err)
		}
	})

	relative := filepath.Join("dados com espaço", "contabase.db")
	t.Chdir(base)

	for name, dsn := range map[string]string{
		"relative path":     relative,
		"relative file URI": "file:" + filepath.ToSlash(relative),
		"absolute path":     dbPath,
		"absolute file URI": "file:" + filepath.ToSlash(dbPath),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSQLiteRuntimeSchema(dsn); err != nil {
				t.Fatalf("ValidateSQLiteRuntimeSchema(%q): %v", dsn, err)
			}
		})
	}
}

func TestSQLiteRuntimeSchemaReopensMigration79FromRelativePath(t *testing.T) {
	base := t.TempDir()
	relative := filepath.Join("data", "contabase.db")
	absolute := filepath.Join(base, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}

	owner, err := OpenRuntimeDBs(absolute)
	if err != nil {
		t.Fatalf("open disposable database: %v", err)
	}
	var version int
	if err := owner.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		_ = owner.Close()
		t.Fatal(err)
	}
	if version != 82 {
		_ = owner.Close()
		t.Fatalf("schema version=%d want=82", version)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close after migration: %v", err)
	}

	t.Chdir(base)
	dsn := "file:" + filepath.ToSlash(relative)
	if err := ValidateSQLiteRuntimeSchema(dsn); err != nil {
		t.Fatalf("preflight after close: %v", err)
	}
	reopened, err := OpenRuntimeDBs(dsn)
	if err != nil {
		t.Fatalf("reopen through relative URI: %v", err)
	}
	defer reopened.Close()
	if err := reopened.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 82 {
		t.Fatalf("reopened schema version=%d want=82", version)
	}
	if err := ValidateSQLiteRuntimeSchema(relative); err != nil {
		t.Fatalf("preflight after reopen: %v", err)
	}
}

func TestSQLiteRuntimeSchemaRejectsMalformedAuthorityWithoutCreatingFiles(t *testing.T) {
	for _, dsn := range []string{"file://data/contabase.db", "file://../contabase.db"} {
		if err := ValidateSQLiteRuntimeSchema(dsn); err == nil {
			t.Fatalf("malformed URI accepted: %s", dsn)
		} else if !strings.Contains(err.Error(), "invalid SQLite file URI") {
			t.Fatalf("malformed URI error=%v", err)
		}
	}

	base := t.TempDir()
	t.Chdir(base)
	missing := filepath.Join("data", "missing.db")
	if err := ValidateSQLiteRuntimeSchema("file:" + filepath.ToSlash(missing)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing database error=%v want=%v", err, fs.ErrNotExist)
	}
	if _, err := os.Stat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("preflight created the missing database: %v", err)
	}
}
