package handlers

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
	"modernc.org/sqlite"
)

// Only test preparation can hold the raw administrative handle. Requests
// continue to run against the guarded pools; no production escape is added.
type handlerSQLiteFixture struct {
	dsn                   string
	mu                    sync.Mutex
	admin                 *sql.DB
	anchor                *sql.DB
	foreignOff, checksOff bool
}

var handlerSQLiteFixtures sync.Map

func openGuardedHandlerFixture(t *testing.T) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "fixture"), RawQuery: "mode=memory&cache=shared"}).String()
	db, err := database.Open(dsn)
	if err != nil {
		return nil, err
	}
	f := registerHandlerFixture(t, db, dsn)
	f.anchor, err = sql.Open("sqlite", dsn)
	if err != nil {
		db.Close()
		return nil, err
	}
	if err = f.anchor.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	t.Cleanup(func() { db.Close() })
	return db, nil
}
func registerHandlerFixture(t *testing.T, db *sql.DB, dsn string) *handlerSQLiteFixture {
	f := &handlerSQLiteFixture{dsn: dsn}
	actual, loaded := handlerSQLiteFixtures.LoadOrStore(db, f)
	if loaded {
		return actual.(*handlerSQLiteFixture)
	}
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.admin != nil {
			f.admin.Close()
		}
		if f.anchor != nil {
			f.anchor.Close()
		}
		handlerSQLiteFixtures.Delete(db)
	})
	return f
}
func handlerFixtureSQL(t *testing.T, db *sql.DB, q string) (*sql.DB, func()) {
	t.Helper()
	if _, raw := db.Driver().(*sqlite.Driver); raw {
		return db, func() {}
	}
	upper := strings.ToUpper(strings.TrimSpace(q))
	if strings.HasPrefix(upper, "PRAGMA QUERY_ONLY") || strings.HasPrefix(upper, "PRAGMA BUSY_TIMEOUT") {
		return db, func() {}
	}
	v, ok := handlerSQLiteFixtures.Load(db)
	var f *handlerSQLiteFixture
	if ok {
		f = v.(*handlerSQLiteFixture)
	} else {
		var path string
		if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path == "" {
			t.Fatal("fixture maintenance requires a registered named memory or disk database")
		}
		f = registerHandlerFixture(t, db, (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}).String())
	}
	f.mu.Lock()
	handedOff := false
	defer func() {
		if !handedOff {
			f.mu.Unlock()
		}
	}()
	if f.admin == nil {
		var err error
		f.admin, err = sql.Open("sqlite", f.dsn)
		if err != nil {
			t.Fatal(err)
		}
		f.admin.SetMaxOpenConns(1)
		if _, err = f.admin.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			t.Fatal(fmt.Errorf("fixture FK setup: %w", err))
		}
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(upper)
	if strings.HasPrefix(compact, "PRAGMAFOREIGN_KEYS=") {
		f.foreignOff = strings.HasSuffix(compact, "=OFF") || strings.HasSuffix(compact, "=0")
	}
	if strings.HasPrefix(compact, "PRAGMAIGNORE_CHECK_CONSTRAINTS=") {
		f.checksOff = strings.HasSuffix(compact, "=ON") || strings.HasSuffix(compact, "=1")
	}
	handedOff = true
	return f.admin, func() {
		defer f.mu.Unlock()
		if !f.foreignOff && !f.checksOff {
			f.admin.Close()
			f.admin = nil
		}
	}
}

// Persistent fault triggers survive the short maintenance lease and affect
// the guarded request connection. They exist only in a disposable fixture.
func fixtureHandlerExec(t *testing.T, db *sql.DB, q string, args ...any) (sql.Result, error) {
	t.Helper()
	q = strings.Replace(q, "CREATE TEMP TRIGGER", "CREATE TRIGGER", 1)
	admin, release := handlerFixtureSQL(t, db, q)
	defer release()
	return admin.Exec(q, args...)
}
