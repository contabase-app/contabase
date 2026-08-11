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

func TestMigration55BoxMonthlyRechargeIdentityFreshSchema(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open fresh database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 55)
	assertColumnExists(t, db, "boxes", "monthly_recharge_anchor_month")
	assertColumnExists(t, db, "box_virtual_ledger", "automatic_recharge_month")
	assertSchemaObjectExists(t, db, "index", "idx_box_virtual_ledger_automatic_recharge_month")

	seedMonthlyRechargeWorkspace(t, db, "ws-fresh")
	insertMonthlyRechargeBox(t, db, "box-fresh", "ws-fresh", 1000, 0, time.Now().Unix())
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (
			id, box_id, amount, type, description, automatic_recharge_month, reference_date, created_at
		) VALUES ('invalid-month', 'box-fresh', 1000, 'RECHARGE', 'invalid', '2026-1x', 1, 1)
	`); err == nil {
		t.Fatal("invalid civil month should be rejected by the fresh schema")
	}
}

func TestMigration55UpgradePreservesLedgerAndRecognizesLegacy(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	t.Setenv("TZ", "UTC")
	dbPath := filepath.Join(t.TempDir(), "migration55.db")
	db, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+dbPath))
	if err != nil {
		t.Fatalf("open v54 fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE boxes (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			target_amount INTEGER NOT NULL DEFAULT 0,
			monthly_recharge INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE box_virtual_ledger (
			id TEXT PRIMARY KEY,
			box_id TEXT NOT NULL,
			amount INTEGER NOT NULL,
			type TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			source_transaction_id TEXT,
			reversal_of_ledger_id TEXT,
			reference_date INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create v54 fixture: %v", err)
		}
	}
	seedAppliedMigrationsBefore(t, db, 55)

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	now := time.Now().In(loc)
	legacyMonth := now.Format("2006-01")
	createdAt := now.AddDate(0, -8, 0).Unix()
	if _, err := db.Exec(`
		INSERT INTO boxes (id, workspace_id, name, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('box-legacy', 'ws-a', 'Legacy', 1, 1200, ?, ?),
			('box-zero', 'ws-a', 'Zero', 0, 0, ?, ?)
	`, createdAt, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("insert legacy boxes: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger
			(id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('legacy-auto-a', 'box-legacy', 1200, 'RECHARGE', 'Aporte Automático', ?, ?),
			('legacy-auto-duplicate', 'box-legacy', 1200, 'RECHARGE', 'Aporte Automático', ?, ?),
			('legacy-manual', 'box-legacy', 1200, 'RECHARGE', 'Aporte Manual', ?, ?)
	`, now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Unix(), now.Unix(), now.Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatalf("insert legacy ledger: %v", err)
	}

	beforeCount, beforeSum := monthlyRechargeLedgerCountAndSum(t, db, "box-legacy")
	if err := runMigrationsThroughVersionForTest(db, 55); err != nil {
		t.Fatalf("upgrade v54 to v55: %v", err)
	}
	assertMigrationApplied(t, db, 55)
	assertColumnExists(t, db, "boxes", "monthly_recharge_anchor_month")
	assertColumnExists(t, db, "box_virtual_ledger", "automatic_recharge_month")
	assertSchemaObjectExists(t, db, "index", "idx_box_virtual_ledger_automatic_recharge_month")

	var anchor string
	if err := db.QueryRow(`SELECT monthly_recharge_anchor_month FROM boxes WHERE id = 'box-legacy'`).Scan(&anchor); err != nil {
		t.Fatalf("query rollout anchor: %v", err)
	}
	if anchor != legacyMonth {
		t.Fatalf("rollout anchor = %q, want %q", anchor, legacyMonth)
	}
	var recognized, manualRecognized int
	if err := db.QueryRow(`
		SELECT
			COUNT(automatic_recharge_month),
			COUNT(CASE WHEN id = 'legacy-manual' AND automatic_recharge_month IS NOT NULL THEN 1 END)
		FROM box_virtual_ledger
		WHERE box_id = 'box-legacy'
	`).Scan(&recognized, &manualRecognized); err != nil {
		t.Fatalf("query legacy recognition: %v", err)
	}
	if recognized != 1 {
		t.Fatalf("recognized legacy events = %d, want one deterministic identity", recognized)
	}
	if manualRecognized != 0 {
		t.Fatal("manual recharge was classified as automatic")
	}
	afterCount, afterSum := monthlyRechargeLedgerCountAndSum(t, db, "box-legacy")
	if afterCount != beforeCount || afterSum != beforeSum {
		t.Fatalf("migration changed ledger count/sum: before=(%d,%d) after=(%d,%d)", beforeCount, beforeSum, afterCount, afterSum)
	}

	// The migration-55 contract is asserted above. The current recharge writer
	// reads the lifecycle column introduced by v56, so advance only to that
	// explicit runtime prerequisite before exercising the existing behavior check.
	if err := runMigrationsThroughVersionForTest(db, 56); err != nil {
		t.Fatalf("prepare current recharge writer through v56: %v", err)
	}
	if err := materializeMonthlyBoxRecharges(context.Background(), db, now); err != nil {
		t.Fatalf("materialize recognized legacy month: %v", err)
	}
	finalCount, finalSum := monthlyRechargeLedgerCountAndSum(t, db, "box-legacy")
	if finalCount != beforeCount || finalSum != beforeSum {
		t.Fatalf("recognized legacy month duplicated: before=(%d,%d) final=(%d,%d)", beforeCount, beforeSum, finalCount, finalSum)
	}

	if err := runMigrationsThroughVersionForTest(db, 56); err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}
	repeatedCount, repeatedSum := monthlyRechargeLedgerCountAndSum(t, db, "box-legacy")
	if repeatedCount != finalCount || repeatedSum != finalSum {
		t.Fatalf("migration rerun changed ledger: first=(%d,%d) repeated=(%d,%d)", finalCount, finalSum, repeatedCount, repeatedSum)
	}
}

func TestMaterializeMonthlyBoxRechargesDeterministicAndScoped(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	august := time.Date(2026, time.August, 17, 14, 30, 0, 0, loc)
	createdAt := time.Date(2026, time.August, 2, 9, 0, 0, 0, loc).Unix()
	seedMonthlyRechargeWorkspace(t, db, "ws-a")
	seedMonthlyRechargeWorkspace(t, db, "ws-b")
	insertMonthlyRechargeBox(t, db, "box-a", "ws-a", 1000, 500, createdAt)
	insertMonthlyRechargeBox(t, db, "box-b", "ws-a", 2000, 0, createdAt)
	insertMonthlyRechargeBox(t, db, "box-zero", "ws-a", 0, 0, createdAt)
	insertMonthlyRechargeBox(t, db, "box-c", "ws-b", 3000, 0, createdAt)
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger
			(id, box_id, amount, type, description, reference_date, created_at)
		VALUES ('manual-same-value', 'box-a', 1000, 'RECHARGE', 'Aporte Manual', ?, ?)
	`, august.Unix(), august.Unix()); err != nil {
		t.Fatalf("insert manual recharge: %v", err)
	}

	if err := materializeMonthlyBoxRecharges(context.Background(), db, august); err != nil {
		t.Fatalf("materialize after day one: %v", err)
	}
	assertAutomaticRechargeCount(t, db, "box-a", "2026-08", 1)
	assertAutomaticRechargeCount(t, db, "box-b", "2026-08", 1)
	assertAutomaticRechargeCount(t, db, "box-c", "2026-08", 1)
	assertAutomaticRechargeCount(t, db, "box-zero", "2026-08", 0)
	assertMonthlyRechargeSum(t, db, "box-a", 2000) // manual + automatic despite target already reached
	assertMonthlyRechargeSum(t, db, "box-b", 2000)
	assertMonthlyRechargeSum(t, db, "box-c", 3000)

	for i := 0; i < 10; i++ {
		if err := materializeMonthlyBoxRecharges(context.Background(), db, august.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("repeat %d: %v", i+1, err)
		}
	}
	assertAutomaticRechargeCount(t, db, "box-a", "2026-08", 1)
	assertMonthlyRechargeSum(t, db, "box-a", 2000)

	september := time.Date(2026, time.September, 9, 8, 0, 0, 0, loc)
	if err := materializeMonthlyBoxRecharges(context.Background(), db, september); err != nil {
		t.Fatalf("materialize next competence: %v", err)
	}
	assertAutomaticRechargeCount(t, db, "box-a", "2026-09", 1)
	assertMonthlyRechargeSum(t, db, "box-a", 3000)

	july := time.Date(2026, time.July, 20, 8, 0, 0, 0, loc)
	if err := materializeMonthlyBoxRecharges(context.Background(), db, july); err != nil {
		t.Fatalf("run before anchor: %v", err)
	}
	assertAutomaticRechargeCount(t, db, "box-a", "2026-07", 0)
}

func TestMaterializeMonthlyBoxRechargesConcurrentConnectionsAndRestart(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	dbPath := filepath.Join(t.TempDir(), "monthly-concurrent.db")
	bootstrap := openMonthlyRechargeFileDB(t, dbPath)
	seedMonthlyRechargeWorkspace(t, bootstrap, "ws-a")
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, loc)
	insertMonthlyRechargeBox(t, bootstrap, "box-race", "ws-a", 4500, 0, now.AddDate(0, 0, -2).Unix())
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	dbA := openMonthlyRechargeFileDB(t, dbPath)
	defer dbA.Close()
	dbB := openMonthlyRechargeFileDB(t, dbPath)
	defer dbB.Close()

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{dbA, dbB} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			errs <- materializeMonthlyBoxRecharges(context.Background(), db, now)
		}(db)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent materialization returned error: %v", err)
		}
	}
	assertAutomaticRechargeCount(t, dbA, "box-race", "2026-08", 1)
	assertMonthlyRechargeSum(t, dbA, "box-race", 4500)

	if err := dbA.Close(); err != nil {
		t.Fatalf("close first handle before restart: %v", err)
	}
	restarted := openMonthlyRechargeFileDB(t, dbPath)
	defer restarted.Close()
	if err := materializeMonthlyBoxRecharges(context.Background(), restarted, now.Add(48*time.Hour)); err != nil {
		t.Fatalf("materialize after logical restart: %v", err)
	}
	assertAutomaticRechargeCount(t, restarted, "box-race", "2026-08", 1)
	assertMonthlyRechargeSum(t, restarted, "box-race", 4500)
}

func TestMaterializeMonthlyBoxRechargesRollsBackOnIntermediateError(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	seedMonthlyRechargeWorkspace(t, db, "ws-a")
	insertMonthlyRechargeBox(t, db, "box-a-ok", "ws-a", 1000, 0, now.Unix())
	insertMonthlyRechargeBox(t, db, "box-z-fail", "ws-a", 2000, 0, now.Unix())
	if _, err := db.Exec(`
		CREATE TRIGGER fail_monthly_recharge
		BEFORE INSERT ON box_virtual_ledger
		WHEN NEW.box_id = 'box-z-fail' AND NEW.automatic_recharge_month IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'forced monthly recharge failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	err = materializeMonthlyBoxRecharges(context.Background(), db, now)
	if err == nil || !strings.Contains(err.Error(), "forced monthly recharge failure") {
		t.Fatalf("materialization error = %v, want propagated trigger error", err)
	}
	assertAutomaticRechargeCount(t, db, "box-a-ok", "2026-08", 0)
	assertAutomaticRechargeCount(t, db, "box-z-fail", "2026-08", 0)
	assertMonthlyRechargeSum(t, db, "box-a-ok", 0)
	assertMonthlyRechargeSum(t, db, "box-z-fail", 0)
	for _, boxID := range []string{"box-a-ok", "box-z-fail"} {
		var anchor sql.NullString
		if err := db.QueryRow(`SELECT monthly_recharge_anchor_month FROM boxes WHERE id = ?`, boxID).Scan(&anchor); err != nil {
			t.Fatalf("query rolled-back anchor for %s: %v", boxID, err)
		}
		if anchor.Valid {
			t.Fatalf("anchor for %s survived rollback: %q", boxID, anchor.String)
		}
	}
}

func TestMaterializeMonthlyBoxRechargesReturnsBusy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monthly-busy.db")
	bootstrap := openMonthlyRechargeFileDB(t, dbPath)
	seedMonthlyRechargeWorkspace(t, bootstrap, "ws-a")
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	insertMonthlyRechargeBox(t, bootstrap, "box-busy", "ws-a", 1000, 0, now.Unix())
	bootstrap.Close()

	locker, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(20)")
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	locker.SetMaxOpenConns(1)
	defer locker.Close()
	contender, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(20)")
	if err != nil {
		t.Fatalf("open contender: %v", err)
	}
	contender.SetMaxOpenConns(1)
	defer contender.Close()
	if _, err := locker.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("hold immediate transaction: %v", err)
	}
	defer locker.Exec(`ROLLBACK`)

	err = materializeMonthlyBoxRecharges(context.Background(), contender, now)
	if err == nil {
		t.Fatal("busy writer contention was incorrectly treated as idempotent success")
	}
	upper := strings.ToUpper(err.Error())
	if !strings.Contains(upper, "BUSY") && !strings.Contains(upper, "LOCKED") {
		t.Fatalf("contention error = %v, want SQLITE_BUSY/LOCKED", err)
	}
	assertAutomaticRechargeCount(t, locker, "box-busy", "2026-08", 0)
}

func openMonthlyRechargeFileDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+dbPath))
	if err != nil {
		t.Fatalf("open file database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("execute ddl: %v", err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		t.Fatalf("run migrations: %v", err)
	}
	return db
}

func seedMonthlyRechargeWorkspace(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO workspaces (id, name, type) VALUES (?, ?, 'personal')`, workspaceID, workspaceID); err != nil {
		t.Fatalf("insert workspace %s: %v", workspaceID, err)
	}
}

func insertMonthlyRechargeBox(t *testing.T, db *sql.DB, boxID, workspaceID string, recharge, target, createdAt int64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO boxes (
			id, workspace_id, name, target_amount, monthly_recharge, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, boxID, workspaceID, boxID, target, recharge, createdAt, createdAt); err != nil {
		t.Fatalf("insert box %s: %v", boxID, err)
	}
}

func assertAutomaticRechargeCount(t *testing.T, db *sql.DB, boxID, month string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger
		WHERE box_id = ? AND automatic_recharge_month = ?
	`, boxID, month).Scan(&got); err != nil {
		t.Fatalf("count automatic recharges for %s/%s: %v", boxID, month, err)
	}
	if got != want {
		t.Fatalf("automatic recharges for %s/%s = %d, want %d", boxID, month, got, want)
	}
}

func assertMonthlyRechargeSum(t *testing.T, db *sql.DB, boxID string, want int64) {
	t.Helper()
	_, got := monthlyRechargeLedgerCountAndSum(t, db, boxID)
	if got != want {
		t.Fatalf("ledger sum for %s = %d, want %d", boxID, got, want)
	}
}

func monthlyRechargeLedgerCountAndSum(t *testing.T, db *sql.DB, boxID string) (int, int64) {
	t.Helper()
	var count int
	var sum int64
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(amount), 0)
		FROM box_virtual_ledger
		WHERE box_id = ?
	`, boxID).Scan(&count, &sum); err != nil {
		t.Fatalf("ledger count/sum for %s: %v", boxID, err)
	}
	return count, sum
}

func TestMaterializeMonthlyBoxRechargesInputErrors(t *testing.T) {
	if err := materializeMonthlyBoxRecharges(context.Background(), nil, time.Now()); err == nil {
		t.Fatal("nil database should fail")
	}
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := materializeMonthlyBoxRecharges(context.Background(), db, time.Time{}); err == nil {
		t.Fatal("zero civil time should fail")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := materializeMonthlyBoxRecharges(canceled, db, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error = %v, want context.Canceled", err)
	}
}
