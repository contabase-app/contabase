package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMonthlyRechargeTargetPolicy(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	tests := []struct {
		name           string
		balance        int64
		target         int64
		recharge       int64
		continueAfter  int64
		wantAutomatic  int64
		wantBalance    int64
		wantState      BoxAutomaticContributionState
		wantEventTypes []BoxContributionLifecycleEventType
	}{
		{name: "continue true below target", balance: 5000, target: 10000, recharge: 1000, continueAfter: 1, wantAutomatic: 1000, wantBalance: 6000, wantState: BoxAutomaticContributionStateActive},
		{name: "continue true permits overshoot", balance: 9500, target: 10000, recharge: 1000, continueAfter: 1, wantAutomatic: 1000, wantBalance: 10500, wantState: BoxAutomaticContributionStateActive},
		{name: "remaining above recharge", balance: 5000, target: 10000, recharge: 1000, continueAfter: 0, wantAutomatic: 1000, wantBalance: 6000, wantState: BoxAutomaticContributionStateActive},
		{name: "remaining equals recharge", balance: 9000, target: 10000, recharge: 1000, continueAfter: 0, wantAutomatic: 1000, wantBalance: 10000, wantState: BoxAutomaticContributionStateBlocked, wantEventTypes: []BoxContributionLifecycleEventType{BoxContributionLifecycleEventGoalReached, BoxContributionLifecycleEventBlocked}},
		{name: "95 100 10 partial recharge", balance: 9500, target: 10000, recharge: 1000, continueAfter: 0, wantAutomatic: 500, wantBalance: 10000, wantState: BoxAutomaticContributionStateBlocked, wantEventTypes: []BoxContributionLifecycleEventType{BoxContributionLifecycleEventGoalReached, BoxContributionLifecycleEventBlocked}},
		{name: "target already reached", balance: 10000, target: 10000, recharge: 1000, continueAfter: 0, wantAutomatic: 0, wantBalance: 10000, wantState: BoxAutomaticContributionStateBlocked, wantEventTypes: []BoxContributionLifecycleEventType{BoxContributionLifecycleEventGoalReached, BoxContributionLifecycleEventBlocked}},
		{name: "target already exceeded", balance: 11000, target: 10000, recharge: 1000, continueAfter: 0, wantAutomatic: 0, wantBalance: 11000, wantState: BoxAutomaticContributionStateBlocked, wantEventTypes: []BoxContributionLifecycleEventType{BoxContributionLifecycleEventGoalReached, BoxContributionLifecycleEventBlocked}},
		{name: "no target remains revolving", balance: 500, target: 0, recharge: 1000, continueAfter: 0, wantAutomatic: 1000, wantBalance: 1500, wantState: BoxAutomaticContributionStateActive},
		{name: "zero recharge stays untouched", balance: 10000, target: 10000, recharge: 0, continueAfter: 0, wantAutomatic: 0, wantBalance: 10000, wantState: BoxAutomaticContributionStateActive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openMonthlyLifecycleMemoryDB(t)
			defer db.Close()
			seedMonthlyLifecycleBox(t, db, "box-policy", tt.recharge, tt.target, tt.continueAfter, BoxAutomaticContributionStateActive, now)
			insertMonthlyLifecycleLedger(t, db, "box-policy", "seed-policy", tt.balance, now)

			if err := materializeMonthlyBoxRecharges(context.Background(), db, now); err != nil {
				t.Fatalf("materialize: %v", err)
			}
			assertMonthlyLifecycleOutcome(t, db, "box-policy", now.Format("2006-01"), tt.wantAutomatic, tt.wantBalance, tt.wantState, tt.wantEventTypes)
		})
	}
}

func TestMonthlyRechargeBlockedReactivationRules(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	tests := []struct {
		name           string
		balance        int64
		target         int64
		wantAutomatic  int64
		wantBalance    int64
		wantState      BoxAutomaticContributionState
		wantEventTypes []BoxContributionLifecycleEventType
	}{
		{name: "balance above recharge remains blocked", balance: 1500, target: 5000, wantBalance: 1500, wantState: BoxAutomaticContributionStateBlocked},
		{name: "balance equal recharge remains blocked", balance: 1000, target: 5000, wantBalance: 1000, wantState: BoxAutomaticContributionStateBlocked},
		{name: "balance below recharge reactivates and contributes", balance: 900, target: 5000, wantAutomatic: 1000, wantBalance: 1900, wantState: BoxAutomaticContributionStateActive, wantEventTypes: []BoxContributionLifecycleEventType{BoxContributionLifecycleEventReactivated}},
		{name: "negative balance reactivates", balance: -500, target: 0, wantAutomatic: 1000, wantBalance: 500, wantState: BoxAutomaticContributionStateActive, wantEventTypes: []BoxContributionLifecycleEventType{BoxContributionLifecycleEventReactivated}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openMonthlyLifecycleMemoryDB(t)
			defer db.Close()
			seedMonthlyLifecycleBox(t, db, "box-blocked", 1000, tt.target, 0, BoxAutomaticContributionStateBlocked, now)
			insertMonthlyLifecycleLedger(t, db, "box-blocked", "seed-blocked", tt.balance, now)

			if err := materializeMonthlyBoxRecharges(context.Background(), db, now); err != nil {
				t.Fatalf("materialize: %v", err)
			}
			assertMonthlyLifecycleOutcome(t, db, "box-blocked", now.Format("2006-01"), tt.wantAutomatic, tt.wantBalance, tt.wantState, tt.wantEventTypes)
		})
	}
}

func TestMonthlyRechargeBlockedReactivationWithExistingCompetenceDoesNotDuplicate(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	db := openMonthlyLifecycleMemoryDB(t)
	defer db.Close()
	seedMonthlyLifecycleBox(t, db, "box-existing", 1000, 5000, 0, BoxAutomaticContributionStateBlocked, now)
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (
			id, box_id, amount, type, description, automatic_recharge_month, reference_date, created_at
		) VALUES
			('existing-auto', 'box-existing', 1000, 'RECHARGE', 'Aporte Automático', '2026-08', ?, ?),
			('existing-release', 'box-existing', -500, 'RELEASE', 'Manual release', NULL, ?, ?)
	`, now.Unix(), now.Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatalf("seed existing competence: %v", err)
	}

	if err := materializeMonthlyBoxRecharges(context.Background(), db, now); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	assertMonthlyLifecycleOutcome(t, db, "box-existing", "2026-08", 1000, 500, BoxAutomaticContributionStateActive, []BoxContributionLifecycleEventType{BoxContributionLifecycleEventReactivated})
	assertAutomaticRechargeCount(t, db, "box-existing", "2026-08", 1)
}

func TestMonthlyRechargeLifecycleReactivationAndReblocking(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	august := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	september := time.Date(2026, time.September, 17, 12, 0, 0, 0, loc)
	db := openMonthlyLifecycleMemoryDB(t)
	defer db.Close()
	seedMonthlyLifecycleBox(t, db, "box-cycle", 1000, 1000, 0, BoxAutomaticContributionStateActive, august)
	insertMonthlyLifecycleLedger(t, db, "box-cycle", "cycle-initial", 1000, august)

	if err := materializeMonthlyBoxRecharges(context.Background(), db, august); err != nil {
		t.Fatalf("initial completion: %v", err)
	}
	assertMonthlyLifecycleOutcome(t, db, "box-cycle", "2026-08", 0, 1000, BoxAutomaticContributionStateBlocked, []BoxContributionLifecycleEventType{BoxContributionLifecycleEventGoalReached, BoxContributionLifecycleEventBlocked})

	insertMonthlyLifecycleLedger(t, db, "box-cycle", "cycle-manual-release", -100, august.Add(time.Hour))
	if err := materializeMonthlyBoxRecharges(context.Background(), db, september); err != nil {
		t.Fatalf("reactivate and reblock: %v", err)
	}
	assertMonthlyLifecycleOutcome(t, db, "box-cycle", "2026-09", 100, 1000, BoxAutomaticContributionStateBlocked, []BoxContributionLifecycleEventType{
		BoxContributionLifecycleEventGoalReached,
		BoxContributionLifecycleEventBlocked,
		BoxContributionLifecycleEventReactivated,
		BoxContributionLifecycleEventGoalReached,
		BoxContributionLifecycleEventBlocked,
	})

	for i := 0; i < 10; i++ {
		if err := materializeMonthlyBoxRecharges(context.Background(), db, september.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatalf("repeat %d: %v", i+1, err)
		}
	}
	assertAutomaticRechargeCount(t, db, "box-cycle", "2026-09", 1)
	assertLifecycleEventTypes(t, db, "box-cycle", []BoxContributionLifecycleEventType{
		BoxContributionLifecycleEventGoalReached,
		BoxContributionLifecycleEventBlocked,
		BoxContributionLifecycleEventReactivated,
		BoxContributionLifecycleEventGoalReached,
		BoxContributionLifecycleEventBlocked,
	})
}

func TestMonthlyRechargeLifecycleIdempotencyRestartAndNextCompetence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monthly-lifecycle-restart.db")
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	august := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	september := time.Date(2026, time.September, 17, 12, 0, 0, 0, loc)
	db := openMonthlyRechargeFileDB(t, dbPath)
	seedMonthlyRechargeWorkspace(t, db, "ws-monthly-lifecycle")
	seedMonthlyLifecycleBox(t, db, "box-restart", 1000, 5000, 0, BoxAutomaticContributionStateActive, august)

	for i := 0; i < 10; i++ {
		if err := materializeMonthlyBoxRecharges(context.Background(), db, august.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("august run %d: %v", i+1, err)
		}
	}
	assertAutomaticRechargeCount(t, db, "box-restart", "2026-08", 1)
	if err := db.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}

	restarted := openMonthlyRechargeFileDB(t, dbPath)
	defer restarted.Close()
	if err := materializeMonthlyBoxRecharges(context.Background(), restarted, august.Add(48*time.Hour)); err != nil {
		t.Fatalf("same competence after restart: %v", err)
	}
	if err := materializeMonthlyBoxRecharges(context.Background(), restarted, september); err != nil {
		t.Fatalf("next competence after restart: %v", err)
	}
	assertAutomaticRechargeCount(t, restarted, "box-restart", "2026-08", 1)
	assertAutomaticRechargeCount(t, restarted, "box-restart", "2026-09", 1)
	assertMonthlyRechargeSum(t, restarted, "box-restart", 2000)
	assertBoxContributionLifecycle(t, restarted, "box-restart", 0, BoxAutomaticContributionStateActive)
}

func TestMonthlyRechargeTargetLifecycleConcurrentPartial(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monthly-lifecycle-concurrent.db")
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	bootstrap := openMonthlyRechargeFileDB(t, dbPath)
	seedMonthlyRechargeWorkspace(t, bootstrap, "ws-monthly-lifecycle")
	seedMonthlyLifecycleBox(t, bootstrap, "box-partial-race", 1000, 10000, 0, BoxAutomaticContributionStateActive, now)
	insertMonthlyLifecycleLedger(t, bootstrap, "box-partial-race", "race-initial", 9500, now)
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
			t.Fatalf("concurrent materialization: %v", err)
		}
	}
	assertMonthlyLifecycleOutcome(t, dbA, "box-partial-race", "2026-08", 500, 10000, BoxAutomaticContributionStateBlocked, []BoxContributionLifecycleEventType{BoxContributionLifecycleEventGoalReached, BoxContributionLifecycleEventBlocked})
	assertAutomaticRechargeCount(t, dbA, "box-partial-race", "2026-08", 1)
}

func TestMonthlyRechargePartialLifecycleRollback(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	db := openMonthlyLifecycleMemoryDB(t)
	defer db.Close()
	seedMonthlyLifecycleBox(t, db, "box-partial-rollback", 1000, 10000, 0, BoxAutomaticContributionStateActive, now)
	insertMonthlyLifecycleLedger(t, db, "box-partial-rollback", "rollback-initial", 9500, now)
	if _, err := db.Exec(`
		CREATE TRIGGER fail_blocked_event
		BEFORE INSERT ON box_contribution_lifecycle_events
		WHEN NEW.event_type = 'CONTRIBUTION_BLOCKED'
		BEGIN
			SELECT RAISE(ABORT, 'forced blocked event failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	err := materializeMonthlyBoxRecharges(context.Background(), db, now)
	if err == nil || !strings.Contains(err.Error(), "forced blocked event failure") {
		t.Fatalf("materialization error = %v", err)
	}
	assertMonthlyLifecycleOutcome(t, db, "box-partial-rollback", "2026-08", 0, 9500, BoxAutomaticContributionStateActive, nil)
}

func TestMonthlyRechargeReactivationRollback(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	db := openMonthlyLifecycleMemoryDB(t)
	defer db.Close()
	seedMonthlyLifecycleBox(t, db, "box-reactivation-rollback", 1000, 5000, 0, BoxAutomaticContributionStateBlocked, now)
	insertMonthlyLifecycleLedger(t, db, "box-reactivation-rollback", "reactivation-initial", 900, now)
	if _, err := db.Exec(`
		CREATE TRIGGER fail_reactivation_recharge
		BEFORE INSERT ON box_virtual_ledger
		WHEN NEW.box_id = 'box-reactivation-rollback' AND NEW.automatic_recharge_month IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'forced recharge after reactivation failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	err := materializeMonthlyBoxRecharges(context.Background(), db, now)
	if err == nil || !strings.Contains(err.Error(), "forced recharge after reactivation failure") {
		t.Fatalf("materialization error = %v", err)
	}
	assertMonthlyLifecycleOutcome(t, db, "box-reactivation-rollback", "2026-08", 0, 900, BoxAutomaticContributionStateBlocked, nil)
}

func TestMonthlyRechargeBlockedDoesNotRestrictManualLedger(t *testing.T) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, loc)
	db := openMonthlyLifecycleMemoryDB(t)
	defer db.Close()
	seedMonthlyLifecycleBox(t, db, "box-manual", 1000, 1000, 0, BoxAutomaticContributionStateBlocked, now)
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('manual-recharge', 'box-manual', 2000, 'RECHARGE', 'Aporte Manual', ?, ?),
			('manual-release', 'box-manual', -500, 'RELEASE', 'Liberação manual', ?, ?)
	`, now.Unix(), now.Unix(), now.Unix(), now.Unix()); err != nil {
		t.Fatalf("manual ledger while blocked: %v", err)
	}
	assertMonthlyLifecycleOutcome(t, db, "box-manual", "2026-08", 0, 1500, BoxAutomaticContributionStateBlocked, nil)
}

func openMonthlyLifecycleMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db, err := openLegacySQLiteFixture(t)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	seedMonthlyRechargeWorkspace(t, db, "ws-monthly-lifecycle")
	return db
}

func seedMonthlyLifecycleBox(
	t *testing.T,
	db *sql.DB,
	boxID string,
	recharge int64,
	target int64,
	continueAfter int64,
	state BoxAutomaticContributionState,
	createdAt time.Time,
) {
	t.Helper()
	insertMonthlyRechargeBox(t, db, boxID, "ws-monthly-lifecycle", recharge, target, createdAt.Unix())
	if _, err := db.Exec(`
		UPDATE boxes
		SET continue_after_target = ?, automatic_contribution_state = ?
		WHERE id = ?
	`, continueAfter, string(state), boxID); err != nil {
		t.Fatalf("configure lifecycle box %s: %v", boxID, err)
	}
}

func insertMonthlyLifecycleLedger(t *testing.T, db *sql.DB, boxID, ledgerID string, amount int64, when time.Time) {
	t.Helper()
	if amount == 0 {
		return
	}
	entryType := "RECHARGE"
	if amount < 0 {
		entryType = "RELEASE"
	}
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (
			id, box_id, amount, type, description, reference_date, created_at
		) VALUES (?, ?, ?, ?, 'Lifecycle test fixture', ?, ?)
	`, ledgerID, boxID, amount, entryType, when.Unix(), when.Unix()); err != nil {
		t.Fatalf("insert lifecycle ledger %s: %v", ledgerID, err)
	}
}

func assertMonthlyLifecycleOutcome(
	t *testing.T,
	db *sql.DB,
	boxID string,
	month string,
	wantAutomatic int64,
	wantBalance int64,
	wantState BoxAutomaticContributionState,
	wantEventTypes []BoxContributionLifecycleEventType,
) {
	t.Helper()
	var automatic, balance int64
	if err := db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN automatic_recharge_month = ? THEN amount ELSE 0 END), 0),
			COALESCE(SUM(amount), 0)
		FROM box_virtual_ledger
		WHERE box_id = ?
	`, month, boxID).Scan(&automatic, &balance); err != nil {
		t.Fatalf("query ledger outcome for %s: %v", boxID, err)
	}
	if automatic != wantAutomatic || balance != wantBalance {
		t.Fatalf("ledger outcome for %s = automatic:%d balance:%d, want automatic:%d balance:%d", boxID, automatic, balance, wantAutomatic, wantBalance)
	}
	var state string
	if err := db.QueryRow(`SELECT automatic_contribution_state FROM boxes WHERE id = ?`, boxID).Scan(&state); err != nil {
		t.Fatalf("query state for %s: %v", boxID, err)
	}
	if state != string(wantState) {
		t.Fatalf("state for %s = %s, want %s", boxID, state, wantState)
	}
	assertLifecycleEventTypes(t, db, boxID, wantEventTypes)
}

func assertLifecycleEventTypes(t *testing.T, db *sql.DB, boxID string, want []BoxContributionLifecycleEventType) {
	t.Helper()
	rows, err := db.Query(`
		SELECT event_type
		FROM box_contribution_lifecycle_events
		WHERE box_id = ?
		ORDER BY rowid
	`, boxID)
	if err != nil {
		t.Fatalf("query lifecycle events for %s: %v", boxID, err)
	}
	defer rows.Close()
	var got []BoxContributionLifecycleEventType
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan lifecycle event for %s: %v", boxID, err)
		}
		got = append(got, BoxContributionLifecycleEventType(eventType))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("lifecycle events for %s: %v", boxID, err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("lifecycle events for %s = %v, want %v", boxID, got, want)
	}
}
