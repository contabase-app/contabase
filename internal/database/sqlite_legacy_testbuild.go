//go:build contabase_legacy_fixtures

package database

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

//go:embed testdata/investment_schema77.sql
var legacyInvestmentSchema77 string

var legacyInvestmentFixtures = struct {
	sync.Mutex
	paths map[string]bool
}{paths: map[string]bool{}}

func legacyInvestmentFixturePath(path string) (string, error) {
	if !testing.Testing() {
		return "", fmt.Errorf("legacy investment fixtures require a test binary")
	}
	p, err := sqlite79Path(path)
	if err != nil {
		return "", err
	}
	p, err = filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(p)
	if os.IsNotExist(err) {
		parent, e := filepath.EvalSymlinks(filepath.Dir(p))
		if e != nil {
			return "", e
		}
		p = filepath.Join(parent, filepath.Base(p))
		err = nil
	} else if err == nil {
		p = resolved
	}
	if err != nil {
		return "", err
	}
	for _, root := range []string{os.TempDir(), "/private/tmp", "/tmp"} {
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, e := filepath.Rel(root, p)
		if e == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p, nil
		}
	}
	return "", fmt.Errorf("legacy fixture must be a disposable temporary SQLite file")
}

// CreateLegacyInvestmentFixture77 is excluded from normal builds. It installs
// the frozen synthetic schema, without jobs or current migrations.
func CreateLegacyInvestmentFixture77(path string) error {
	p, err := legacyInvestmentFixturePath(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteBackupURI(p, "mode=rw&_pragma=foreign_keys(1)"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(legacyInvestmentSchema77); err != nil {
		return err
	}
	return RegisterLegacyInvestmentFixture(p)
}

func RegisterLegacyInvestmentFixture(path string) error {
	p, err := legacyInvestmentFixturePath(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", sqliteBackupURI(p, "mode=ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = validateLegacyInvestmentFixture(db); err != nil {
		return err
	}
	legacyInvestmentFixtures.Lock()
	legacyInvestmentFixtures.paths[p] = true
	legacyInvestmentFixtures.Unlock()
	return nil
}

func validateLegacyInvestmentFixture(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version != 77 && version != 78 {
		return fmt.Errorf("legacy fixture requires schema 77 or 78")
	}
	if version == 77 {
		_, err := sqlite79Inspect(db)
		return err
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version>=79 OR version=69`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("legacy fixture has incompatible markers")
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = validateAdmission78Reopen(context.Background(), conn); err != nil {
		return err
	}
	return sqlite79Integrity(conn)
}

func OpenLegacyInvestmentFixtureDBs(path string) (*RuntimeDBs, error) {
	p, err := legacyInvestmentFixturePath(path)
	if err != nil {
		return nil, err
	}
	legacyInvestmentFixtures.Lock()
	registered := legacyInvestmentFixtures.paths[p]
	legacyInvestmentFixtures.Unlock()
	if !registered {
		return nil, fmt.Errorf("legacy fixture was not explicitly registered")
	}
	dsn := sqliteBackupURI(p, "mode=rw&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	db, guard, err := newSQLiteGuardPool(sqliteDSNWithTxLock(dsn, "deferred"))
	if err != nil {
		return nil, err
	}
	if err = validateLegacyInvestmentFixture(db); err != nil {
		db.Close()
		return nil, err
	}
	writer, wguard, err := newSQLiteGuardPool(sqliteDSNWithTxLock(dsn, "immediate"))
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = publishSQLiteGuard(db, guard); err == nil {
		err = publishSQLiteGuard(writer, wguard)
	}
	if err != nil {
		db.Close()
		writer.Close()
		return nil, err
	}
	return &RuntimeDBs{DB: db, Immediate: writer, dbPath: p}, nil
}
