package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenRuntimeDBsConfiguresSharedDeferredAndImmediatePools(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	dbPath := filepath.Join(dataDir, "runtime.db")
	runtimeDBs, err := OpenRuntimeDBs("file:" + dbPath)
	if err != nil {
		t.Fatalf("open runtime dbs: %v", err)
	}
	t.Cleanup(func() { _ = runtimeDBs.Close() })

	if got := pragmaString(t, runtimeDBs.DB, "journal_mode"); !strings.EqualFold(got, "wal") {
		t.Fatalf("normal journal_mode = %q, want wal", got)
	}
	if got := pragmaString(t, runtimeDBs.Immediate, "journal_mode"); !strings.EqualFold(got, "wal") {
		t.Fatalf("immediate journal_mode = %q, want wal", got)
	}
	for name, db := range map[string]*sql.DB{"normal": runtimeDBs.DB, "immediate": runtimeDBs.Immediate} {
		if got := pragmaInt(t, db, "foreign_keys"); got != 1 {
			t.Fatalf("%s foreign_keys = %d, want 1", name, got)
		}
		if got := pragmaInt(t, db, "busy_timeout"); got != 5000 {
			t.Fatalf("%s busy_timeout = %d, want 5000", name, got)
		}
	}

	if _, err := runtimeDBs.DB.Exec(`INSERT INTO workspaces (id, name) VALUES ('runtime-shared', 'Runtime Shared')`); err != nil {
		t.Fatalf("insert through normal: %v", err)
	}
	var name string
	if err := runtimeDBs.Immediate.QueryRow(`SELECT name FROM workspaces WHERE id = 'runtime-shared'`).Scan(&name); err != nil {
		t.Fatalf("read normal write through immediate: %v", err)
	}
	if name != "Runtime Shared" {
		t.Fatalf("shared row name = %q", name)
	}

	if _, err := runtimeDBs.Immediate.Exec(`INSERT INTO workspace_members (workspace_id, user_id) VALUES ('missing', 'missing')`); err == nil {
		t.Fatal("immediate pool accepted invalid foreign keys")
	}
}

func TestRuntimeDBsNormalIsDeferredAndImmediateReservesWriter(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	dbPath := filepath.Join(dataDir, "tx-mode.db")
	runtimeDBs, err := OpenRuntimeDBs("file:" + dbPath)
	if err != nil {
		t.Fatalf("open runtime dbs: %v", err)
	}
	t.Cleanup(func() { _ = runtimeDBs.Close() })

	normalTx, err := runtimeDBs.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin normal: %v", err)
	}
	if err := normalTx.QueryRow(`SELECT COUNT(1) FROM workspaces`).Scan(new(int)); err != nil {
		_ = normalTx.Rollback()
		t.Fatalf("normal read: %v", err)
	}
	immediateWhileNormalReads, err := runtimeDBs.Immediate.BeginTx(context.Background(), nil)
	if err != nil {
		_ = normalTx.Rollback()
		t.Fatalf("immediate could not begin while deferred reader was active: %v", err)
	}
	_ = immediateWhileNormalReads.Rollback()
	_ = normalTx.Rollback()

	immediateTx, err := runtimeDBs.Immediate.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin immediate: %v", err)
	}
	contender, err := sql.Open("sqlite", sqliteDSNWithTxLock(normalizeSQLiteDSN("file:"+dbPath+"?_pragma=busy_timeout(20)"), "immediate"))
	if err != nil {
		_ = immediateTx.Rollback()
		t.Fatalf("open immediate contender: %v", err)
	}
	contender.SetMaxOpenConns(1)
	defer contender.Close()
	if _, err := contender.BeginTx(context.Background(), nil); err == nil {
		_ = immediateTx.Rollback()
		t.Fatal("second immediate transaction began while the first held the writer reservation")
	} else if upper := strings.ToUpper(err.Error()); !strings.Contains(upper, "BUSY") && !strings.Contains(upper, "LOCKED") {
		_ = immediateTx.Rollback()
		t.Fatalf("contender error = %v, want BUSY/LOCKED", err)
	}
	if err := immediateTx.Rollback(); err != nil {
		t.Fatalf("rollback immediate: %v", err)
	}
	secondTx, err := contender.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("contender did not begin after writer release: %v", err)
	}
	_ = secondTx.Rollback()
}

func TestRuntimeDBsOwnsEachJobExactlyOnceAndCloseIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	dbPath := filepath.Join(dataDir, "lifecycle.db")

	var mu sync.Mutex
	starts := map[string]int{}
	stops := map[string]int{}
	started := make(chan string, 3)
	stopped := make(chan string, 3)
	owner, err := openRuntimeDBs("file:"+dbPath, &sqliteRuntimeOpenTestHooks{
		jobs: &sqliteRuntimeJobHooks{
			onStart: func(name string) {
				mu.Lock()
				starts[name]++
				mu.Unlock()
				started <- name
			},
			onStop: func(name string) {
				mu.Lock()
				stops[name]++
				mu.Unlock()
				stopped <- name
			},
		},
	})
	if err != nil {
		t.Fatalf("open runtime dbs: %v", err)
	}
	seedMonthlyRechargeWorkspace(t, owner.DB, "ws-runtime-activation")
	insertMonthlyRechargeBox(t, owner.DB, "box-runtime-activation", "ws-runtime-activation", 1000, 0, time.Now().AddDate(0, -1, 0).Unix())

	assertNoJobEvent(t, started, "dormant owner")
	activationErrors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { activationErrors <- owner.Activate() }()
	}
	for i := 0; i < 2; i++ {
		if err := <-activationErrors; err != nil {
			t.Fatalf("concurrent activate runtime dbs: %v", err)
		}
	}
	waitForJobEvents(t, started, 3, "start")
	waitForAutomaticRecharge(t, owner.DB, "box-runtime-activation", time.Now().UTC().Format("2006-01"), 1)
	if err := owner.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	waitForJobEvents(t, stopped, 3, "stop")
	if err := owner.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"auto_backup", "security_log_pruner", "monthly_engine"} {
		if starts[name] != 1 || stops[name] != 1 {
			t.Fatalf("job %s lifecycle starts=%d stops=%d, want 1/1", name, starts[name], stops[name])
		}
	}
	if err := owner.DB.Ping(); err == nil {
		t.Fatal("normal pool remained operational after Close")
	}
	if err := owner.Immediate.Ping(); err == nil {
		t.Fatal("immediate pool remained operational after Close")
	}
	if err := owner.Activate(); !errors.Is(err, errRuntimeDBsClosed) {
		t.Fatalf("activate after Close error = %v, want %v", err, errRuntimeDBsClosed)
	}
}

func TestOpenRuntimeDBsDormantAbandonmentPersistsNoMonthlyEffect(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	dbPath := filepath.Join(dataDir, "dormant-abandonment.db")
	started := make(chan string, 1)
	owner, err := openRuntimeDBs("file:"+dbPath, &sqliteRuntimeOpenTestHooks{
		jobs: &sqliteRuntimeJobHooks{onStart: func(name string) { started <- name }},
	})
	if err != nil {
		t.Fatalf("open dormant runtime dbs: %v", err)
	}

	seedMonthlyRechargeWorkspace(t, owner.DB, "ws-dormant")
	insertMonthlyRechargeBox(t, owner.DB, "box-dormant", "ws-dormant", 2500, 0, time.Now().AddDate(0, -1, 0).Unix())
	beforeCount, beforeSum := monthlyRechargeLedgerCountAndSum(t, owner.DB, "box-dormant")
	var beforeAnchor sql.NullString
	if err := owner.DB.QueryRow(`SELECT monthly_recharge_anchor_month FROM boxes WHERE id = 'box-dormant'`).Scan(&beforeAnchor); err != nil {
		t.Fatalf("query dormant anchor before abandonment: %v", err)
	}
	assertNoJobEvent(t, started, "OpenRuntimeDBs")

	if err := owner.Close(); err != nil {
		t.Fatalf("close dormant runtime dbs: %v", err)
	}
	if err := owner.Activate(); !errors.Is(err, errRuntimeDBsClosed) {
		t.Fatalf("activate abandoned owner error = %v, want %v", err, errRuntimeDBsClosed)
	}

	reopened, err := OpenRuntimeDBs("file:" + dbPath)
	if err != nil {
		t.Fatalf("reopen abandoned database: %v", err)
	}
	defer reopened.Close()
	afterCount, afterSum := monthlyRechargeLedgerCountAndSum(t, reopened.DB, "box-dormant")
	if afterCount != beforeCount || afterSum != beforeSum {
		t.Fatalf("dormant abandonment changed ledger: before=(%d,%d) after=(%d,%d)", beforeCount, beforeSum, afterCount, afterSum)
	}
	var afterAnchor sql.NullString
	if err := reopened.DB.QueryRow(`SELECT monthly_recharge_anchor_month FROM boxes WHERE id = 'box-dormant'`).Scan(&afterAnchor); err != nil {
		t.Fatalf("query dormant anchor after abandonment: %v", err)
	}
	if afterAnchor != beforeAnchor {
		t.Fatalf("dormant abandonment changed anchor: before=%+v after=%+v", beforeAnchor, afterAnchor)
	}
}

func TestOpenRuntimeDBsPartialFailuresClosePoolsAndStartNoJobs(t *testing.T) {
	fault := errors.New("injected runtime db failure")
	t.Run("normal open", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("DATA_DIR", dataDir)
		starts := 0
		owner, err := openRuntimeDBs("file:"+filepath.Join(dataDir, "normal.db"), &sqliteRuntimeOpenTestHooks{
			beforeNormalOpen: func() error { return fault },
			jobs:             &sqliteRuntimeJobHooks{onStart: func(string) { starts++ }},
		})
		if !errors.Is(err, fault) || owner != nil || starts != 0 {
			t.Fatalf("normal failure owner=%v err=%v starts=%d", owner, err, starts)
		}
	})

	t.Run("immediate open", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("DATA_DIR", dataDir)
		var normal *sql.DB
		starts := 0
		owner, err := openRuntimeDBs("file:"+filepath.Join(dataDir, "immediate.db"), &sqliteRuntimeOpenTestHooks{
			afterNormalOpen:     func(db *sql.DB) { normal = db },
			beforeImmediateOpen: func() error { return fault },
			jobs:                &sqliteRuntimeJobHooks{onStart: func(string) { starts++ }},
		})
		if !errors.Is(err, fault) || owner != nil || starts != 0 {
			t.Fatalf("immediate failure owner=%v err=%v starts=%d", owner, err, starts)
		}
		if normal == nil || normal.Ping() == nil {
			t.Fatal("normal pool was not closed after immediate open failure")
		}
	})

	t.Run("initialization", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("DATA_DIR", dataDir)
		var normal, immediate *sql.DB
		starts := 0
		owner, err := openRuntimeDBs("file:"+filepath.Join(dataDir, "init.db"), &sqliteRuntimeOpenTestHooks{
			afterNormalOpen:    func(db *sql.DB) { normal = db },
			afterImmediateOpen: func(db *sql.DB) { immediate = db },
			beforeInitialize:   func() error { return fault },
			jobs:               &sqliteRuntimeJobHooks{onStart: func(string) { starts++ }},
		})
		if !errors.Is(err, fault) || owner != nil || starts != 0 {
			t.Fatalf("init failure owner=%v err=%v starts=%d", owner, err, starts)
		}
		if normal == nil || normal.Ping() == nil || immediate == nil || immediate.Ping() == nil {
			t.Fatal("both pools were not closed after initialization failure")
		}
	})

	t.Run("migration execution", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("DATA_DIR", dataDir)
		dbPath := filepath.Join(dataDir, "migration.db")
		malformed, err := sql.Open("sqlite", "file:"+dbPath)
		if err != nil {
			t.Fatalf("open malformed database: %v", err)
		}
		if _, err := malformed.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
			_ = malformed.Close()
			t.Fatalf("create malformed migration table: %v", err)
		}
		if err := malformed.Close(); err != nil {
			t.Fatalf("close malformed database: %v", err)
		}

		var normal, immediate *sql.DB
		starts := 0
		owner, err := openRuntimeDBs("file:"+dbPath, &sqliteRuntimeOpenTestHooks{
			afterNormalOpen:    func(db *sql.DB) { normal = db },
			afterImmediateOpen: func(db *sql.DB) { immediate = db },
			jobs:               &sqliteRuntimeJobHooks{onStart: func(string) { starts++ }},
		})
		if err == nil || owner != nil || starts != 0 {
			t.Fatalf("migration failure owner=%v err=%v starts=%d", owner, err, starts)
		}
		if normal == nil || normal.Ping() == nil || immediate == nil || immediate.Ping() == nil {
			t.Fatal("both pools were not closed after migration failure")
		}
	})

	t.Run("activation after initialization", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("DATA_DIR", dataDir)
		var normal, immediate *sql.DB
		starts := 0
		owner, err := openRuntimeDBs("file:"+filepath.Join(dataDir, "activation.db"), &sqliteRuntimeOpenTestHooks{
			afterNormalOpen:    func(db *sql.DB) { normal = db },
			afterImmediateOpen: func(db *sql.DB) { immediate = db },
			beforeJobsStart:    func() error { return fault },
			jobs:               &sqliteRuntimeJobHooks{onStart: func(string) { starts++ }},
		})
		if err != nil || owner == nil || starts != 0 {
			t.Fatalf("dormant open owner=%v err=%v starts=%d", owner, err, starts)
		}
		if err := owner.Activate(); !errors.Is(err, fault) || starts != 0 {
			t.Fatalf("activation failure err=%v starts=%d", err, starts)
		}
		if err := owner.Close(); err != nil {
			t.Fatalf("close after activation failure: %v", err)
		}
		if normal == nil || normal.Ping() == nil || immediate == nil || immediate.Ping() == nil {
			t.Fatal("both pools were not closed after activation failure")
		}
	})
}

func TestOpenRuntimeDBsInitializesOnceAndRejectsPrivateMemory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	initializations := 0
	owner, err := openRuntimeDBs("file:"+filepath.Join(dataDir, "once.db"), &sqliteRuntimeOpenTestHooks{
		afterInitialize: func() { initializations++ },
	})
	if err != nil {
		t.Fatalf("open runtime dbs: %v", err)
	}
	if initializations != 1 {
		_ = owner.Close()
		t.Fatalf("initializations = %d, want 1", initializations)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close runtime dbs: %v", err)
	}

	for _, dsn := range []string{":memory:", "file::memory:", "file:shared?mode=memory&cache=shared"} {
		if dbs, err := OpenRuntimeDBs(dsn); err == nil {
			_ = dbs.Close()
			t.Fatalf("OpenRuntimeDBs(%q) succeeded with memory-backed pools", dsn)
		}
	}
}

func TestDatabaseOpenPreservesSinglePoolContract(t *testing.T) {
	var openSignature func(string) (*sql.DB, error) = Open
	_ = openSignature
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	started := make(chan string, 3)
	stopped := make(chan string, 3)
	var mu sync.Mutex
	starts := map[string]int{}
	stops := map[string]int{}
	db, jobs, err := openSinglePool("file:"+filepath.Join(dataDir, "legacy-open.db"), &sqliteRuntimeJobHooks{
		onStart: func(name string) {
			mu.Lock()
			starts[name]++
			mu.Unlock()
			started <- name
		},
		onStop: func(name string) {
			mu.Lock()
			stops[name]++
			mu.Unlock()
			stopped <- name
		},
	})
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	waitForJobEvents(t, started, 3, "legacy start")
	jobs.cancel()
	jobs.wait.Wait()
	waitForJobEvents(t, stopped, 3, "legacy stop")
	defer db.Close()
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", db.Stats().MaxOpenConnections)
	}
	var migrations int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("schema migrations query: %v", err)
	}
	if migrations == 0 {
		t.Fatal("database.Open did not run migrations")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"auto_backup", "security_log_pruner", "monthly_engine"} {
		if starts[name] != 1 || stops[name] != 1 {
			t.Fatalf("legacy database.Open job %s starts=%d stops=%d, want 1/1", name, starts[name], stops[name])
		}
	}
}

func pragmaString(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var value string
	if err := db.QueryRow(`PRAGMA ` + name).Scan(&value); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return value
}

func pragmaInt(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var value int
	if err := db.QueryRow(`PRAGMA ` + name).Scan(&value); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return value
}

func waitForJobEvents(t *testing.T, events <-chan string, count int, phase string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for i := 0; i < count; i++ {
		select {
		case <-events:
		case <-timer.C:
			t.Fatalf("timeout waiting for job %s events: got %d/%d", phase, i, count)
		}
	}
}

func assertNoJobEvent(t *testing.T, events <-chan string, phase string) {
	t.Helper()
	select {
	case name := <-events:
		t.Fatalf("unexpected job %s started during %s", name, phase)
	case <-time.After(100 * time.Millisecond):
	}
}

func waitForAutomaticRecharge(t *testing.T, db *sql.DB, boxID, month string, want int) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var got int
		if err := db.QueryRow(`
			SELECT COUNT(1)
			FROM box_virtual_ledger
			WHERE box_id = ? AND automatic_recharge_month = ?
		`, boxID, month).Scan(&got); err != nil {
			t.Fatalf("count activated automatic recharge: %v", err)
		}
		if got == want {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("automatic recharge count for %s/%s = %d, want %d", boxID, month, got, want)
		case <-ticker.C:
		}
	}
}
