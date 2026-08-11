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

func TestMigration56BoxContributionLifecycleFreshSchema(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open fresh database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 56)
	assertColumnExists(t, db, "boxes", "continue_after_target")
	assertColumnExists(t, db, "boxes", "automatic_contribution_state")
	assertSchemaObjectExists(t, db, "table", "box_contribution_lifecycle_events")
	assertSchemaObjectExists(t, db, "index", "idx_box_contribution_lifecycle_events_box_occurred")

	seedMonthlyRechargeWorkspace(t, db, "ws-fresh-lifecycle")
	insertMonthlyRechargeBox(t, db, "box-fresh-lifecycle", "ws-fresh-lifecycle", 1000, 5000, time.Now().Unix())
	assertBoxContributionLifecycle(t, db, "box-fresh-lifecycle", 0, BoxAutomaticContributionStateActive)

	if _, err := db.Exec(`UPDATE boxes SET continue_after_target = 0 WHERE id = 'box-fresh-lifecycle'`); err != nil {
		t.Fatalf("continue_after_target=false should be accepted: %v", err)
	}
	if _, err := db.Exec(`UPDATE boxes SET continue_after_target = 2 WHERE id = 'box-fresh-lifecycle'`); err == nil {
		t.Fatal("invalid continue_after_target should be rejected")
	}
	if _, err := db.Exec(`UPDATE boxes SET automatic_contribution_state = 'BLOCKED' WHERE id = 'box-fresh-lifecycle'`); err != nil {
		t.Fatalf("BLOCKED state should be accepted: %v", err)
	}
	if _, err := db.Exec(`UPDATE boxes SET automatic_contribution_state = 'UNKNOWN' WHERE id = 'box-fresh-lifecycle'`); err == nil {
		t.Fatal("invalid automatic contribution state should be rejected")
	}

	if _, err := db.Exec(`
		INSERT INTO box_contribution_lifecycle_events (
			id, box_id, event_type, previous_state, new_state, occurred_at
		) VALUES ('goal-event', 'box-fresh-lifecycle', 'GOAL_REACHED', NULL, NULL, 1)
	`); err != nil {
		t.Fatalf("valid GOAL_REACHED event should be accepted: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO box_contribution_lifecycle_events (
			id, box_id, event_type, previous_state, new_state, occurred_at
		) VALUES ('invalid-event', 'box-fresh-lifecycle', 'CONTRIBUTION_BLOCKED', 'BLOCKED', 'ACTIVE', 2)
	`); err == nil {
		t.Fatal("invalid event/state pairing should be rejected")
	}
}

func TestMigration56UpgradePreservesBoxesLedgerAndBehaviorDefaults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration56.db")
	db, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+dbPath))
	if err != nil {
		t.Fatalf("open v55 fixture: %v", err)
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
			monthly_recharge_anchor_month TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE box_virtual_ledger (
			id TEXT PRIMARY KEY,
			box_id TEXT NOT NULL,
			amount INTEGER NOT NULL,
			type TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			automatic_recharge_month TEXT,
			reference_date INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create v55 fixture: %v", err)
		}
	}
	seedAppliedMigrationsBefore(t, db, 56)

	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO boxes (
			id, workspace_id, name, target_amount, monthly_recharge,
			monthly_recharge_anchor_month, created_at, updated_at
		) VALUES
			('box-no-target', 'ws-a', 'No target', 0, 1000, '2026-08', ?, ?),
			('box-below-target', 'ws-a', 'Below', 10000, 1000, '2026-08', ?, ?),
			('box-at-target', 'ws-a', 'At', 10000, 1000, '2026-08', ?, ?),
			('box-above-target', 'ws-b', 'Above', 10000, 1000, '2026-08', ?, ?)
	`, now, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("insert v55 boxes: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (
			id, box_id, amount, type, description, automatic_recharge_month, reference_date, created_at
		) VALUES
			('ledger-below', 'box-below-target', 5000, 'RECHARGE', 'seed', NULL, ?, ?),
			('ledger-at', 'box-at-target', 10000, 'RECHARGE', 'seed', NULL, ?, ?),
			('ledger-above', 'box-above-target', 15000, 'RECHARGE', 'seed', NULL, ?, ?)
	`, now, now, now, now, now, now); err != nil {
		t.Fatalf("insert v55 ledger: %v", err)
	}

	beforeBoxes := countRows(t, db, "boxes")
	beforeLedger := countRows(t, db, "box_virtual_ledger")
	beforeLedgerSum := sumLedgerForAllBoxes(t, db)
	beforeSnapshot := lifecycleBoxFinancialSnapshot(t, db)

	if err := runMigrationsThroughVersionForTest(db, 56); err != nil {
		t.Fatalf("upgrade v55 to v56: %v", err)
	}
	assertMigrationApplied(t, db, 56)
	assertColumnExists(t, db, "boxes", "continue_after_target")
	assertColumnExists(t, db, "boxes", "automatic_contribution_state")
	assertSchemaObjectExists(t, db, "table", "box_contribution_lifecycle_events")
	assertSchemaObjectExists(t, db, "index", "idx_box_contribution_lifecycle_events_box_occurred")

	if got := countRows(t, db, "boxes"); got != beforeBoxes {
		t.Fatalf("box count after migration = %d, want %d", got, beforeBoxes)
	}
	if got := countRows(t, db, "box_virtual_ledger"); got != beforeLedger {
		t.Fatalf("ledger count after migration = %d, want %d", got, beforeLedger)
	}
	if got := sumLedgerForAllBoxes(t, db); got != beforeLedgerSum {
		t.Fatalf("ledger sum after migration = %d, want %d", got, beforeLedgerSum)
	}
	if got := lifecycleBoxFinancialSnapshot(t, db); got != beforeSnapshot {
		t.Fatalf("financial box fields changed: before=%q after=%q", beforeSnapshot, got)
	}
	var compatibleDefaults int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM boxes
		WHERE continue_after_target = 1
		  AND automatic_contribution_state = 'ACTIVE'
	`).Scan(&compatibleDefaults); err != nil {
		t.Fatalf("count compatible lifecycle defaults: %v", err)
	}
	if compatibleDefaults != beforeBoxes {
		t.Fatalf("compatible defaults = %d, want %d", compatibleDefaults, beforeBoxes)
	}
	if events := countRows(t, db, "box_contribution_lifecycle_events"); events != 0 {
		t.Fatalf("migration invented %d lifecycle events", events)
	}

	if err := runMigrationsThroughVersionForTest(db, 56); err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}
	if events := countRows(t, db, "box_contribution_lifecycle_events"); events != 0 {
		t.Fatalf("migration rerun invented %d lifecycle events", events)
	}
}

func TestBoxContributionLifecycleTransitionsAreAtomicAndIdempotent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	seedMonthlyRechargeWorkspace(t, db, "ws-lifecycle")
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	insertMonthlyRechargeBox(t, db, "box-lifecycle", "ws-lifecycle", 1000, 10000, now.Unix())

	changed, err := TransitionBoxAutomaticContributionState(context.Background(), db, "box-lifecycle", BoxAutomaticContributionStateBlocked, now)
	if err != nil || !changed {
		t.Fatalf("ACTIVE -> BLOCKED changed=%v err=%v", changed, err)
	}
	assertBoxContributionLifecycle(t, db, "box-lifecycle", 1, BoxAutomaticContributionStateBlocked)
	assertLifecycleEvent(t, db, "box-lifecycle", 1, BoxContributionLifecycleEventBlocked, BoxAutomaticContributionStateActive, BoxAutomaticContributionStateBlocked)

	changed, err = TransitionBoxAutomaticContributionState(context.Background(), db, "box-lifecycle", BoxAutomaticContributionStateBlocked, now.Add(time.Minute))
	if err != nil || changed {
		t.Fatalf("repeated BLOCKED changed=%v err=%v, want idempotent no-op", changed, err)
	}
	assertBoxContributionLifecycle(t, db, "box-lifecycle", 1, BoxAutomaticContributionStateBlocked)

	changed, err = TransitionBoxAutomaticContributionState(context.Background(), db, "box-lifecycle", BoxAutomaticContributionStateActive, now.Add(2*time.Minute))
	if err != nil || !changed {
		t.Fatalf("BLOCKED -> ACTIVE changed=%v err=%v", changed, err)
	}
	assertBoxContributionLifecycle(t, db, "box-lifecycle", 2, BoxAutomaticContributionStateActive)
	assertLifecycleEvent(t, db, "box-lifecycle", 2, BoxContributionLifecycleEventReactivated, BoxAutomaticContributionStateBlocked, BoxAutomaticContributionStateActive)
}

func TestBoxContributionLifecycleTransitionRollbackLeavesNoPartialState(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	seedMonthlyRechargeWorkspace(t, db, "ws-lifecycle-rollback")
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	insertMonthlyRechargeBox(t, db, "box-lifecycle-rollback", "ws-lifecycle-rollback", 1000, 10000, now.Unix())
	if _, err := db.Exec(`
		CREATE TRIGGER fail_lifecycle_event
		BEFORE INSERT ON box_contribution_lifecycle_events
		BEGIN
			SELECT RAISE(ABORT, 'forced lifecycle event failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	changed, err := TransitionBoxAutomaticContributionState(context.Background(), db, "box-lifecycle-rollback", BoxAutomaticContributionStateBlocked, now)
	if err == nil || changed || !strings.Contains(err.Error(), "forced lifecycle event failure") {
		t.Fatalf("rollback transition changed=%v err=%v", changed, err)
	}
	assertBoxContributionLifecycle(t, db, "box-lifecycle-rollback", 0, BoxAutomaticContributionStateActive)
	if events := countRows(t, db, "box_contribution_lifecycle_events"); events != 0 {
		t.Fatalf("rollback left %d lifecycle events", events)
	}
}

func TestBoxContributionLifecycleConcurrentSameTransition(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lifecycle-concurrent.db")
	bootstrap := openMonthlyRechargeFileDB(t, dbPath)
	seedMonthlyRechargeWorkspace(t, bootstrap, "ws-lifecycle-race")
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	insertMonthlyRechargeBox(t, bootstrap, "box-lifecycle-race", "ws-lifecycle-race", 1000, 10000, now.Unix())
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	dbA := openMonthlyRechargeFileDB(t, dbPath)
	defer dbA.Close()
	dbB := openMonthlyRechargeFileDB(t, dbPath)
	defer dbB.Close()
	start := make(chan struct{})
	type result struct {
		changed bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{dbA, dbB} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			changed, err := TransitionBoxAutomaticContributionState(context.Background(), db, "box-lifecycle-race", BoxAutomaticContributionStateBlocked, now)
			results <- result{changed: changed, err: err}
		}(db)
	}
	close(start)
	wg.Wait()
	close(results)
	changedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent transition error: %v", result.err)
		}
		if result.changed {
			changedCount++
		}
	}
	if changedCount != 1 {
		t.Fatalf("concurrent changed count = %d, want 1", changedCount)
	}
	assertBoxContributionLifecycle(t, dbA, "box-lifecycle-race", 1, BoxAutomaticContributionStateBlocked)
	assertLifecycleEvent(t, dbA, "box-lifecycle-race", 1, BoxContributionLifecycleEventBlocked, BoxAutomaticContributionStateActive, BoxAutomaticContributionStateBlocked)
}

func TestBoxContributionLifecycleTransitionsDoNotMutateLedger(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	seedMonthlyRechargeWorkspace(t, db, "ws-lifecycle-ledger")
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	insertMonthlyRechargeBox(t, db, "box-lifecycle-ledger", "ws-lifecycle-ledger", 1000, 10000, now.Unix())
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (
			id, box_id, amount, type, description, reference_date, created_at
		) VALUES ('box-lifecycle-ledger-seed', 'box-lifecycle-ledger', 9500, 'RECHARGE', 'Seed', ?, ?)
	`, now.Unix(), now.Unix()); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	for i, state := range []BoxAutomaticContributionState{
		BoxAutomaticContributionStateBlocked,
		BoxAutomaticContributionStateActive,
	} {
		changed, err := TransitionBoxAutomaticContributionState(context.Background(), db, "box-lifecycle-ledger", state, now.Add(time.Duration(i)*time.Minute))
		if err != nil || !changed {
			t.Fatalf("transition %s changed=%v err=%v", state, changed, err)
		}
	}
	assertMonthlyRechargeSum(t, db, "box-lifecycle-ledger", 9500)
	assertAutomaticRechargeCount(t, db, "box-lifecycle-ledger", "2026-08", 0)
	assertBoxContributionLifecycle(t, db, "box-lifecycle-ledger", 2, BoxAutomaticContributionStateActive)
}

func TestBoxContributionLifecycleTransitionInputValidation(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	tests := []struct {
		name  string
		db    *sql.DB
		boxID string
		state BoxAutomaticContributionState
		when  time.Time
	}{
		{name: "nil database", db: nil, boxID: "box", state: BoxAutomaticContributionStateActive, when: time.Now()},
		{name: "empty box", db: db, boxID: " ", state: BoxAutomaticContributionStateActive, when: time.Now()},
		{name: "invalid state", db: db, boxID: "box", state: "UNKNOWN", when: time.Now()},
		{name: "zero time", db: db, boxID: "box", state: BoxAutomaticContributionStateActive, when: time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if changed, err := TransitionBoxAutomaticContributionState(context.Background(), tt.db, tt.boxID, tt.state, tt.when); err == nil || changed {
				t.Fatalf("changed=%v err=%v, want validation error", changed, err)
			}
		})
	}
}

func assertBoxContributionLifecycle(t *testing.T, db *sql.DB, boxID string, wantEvents int, wantState BoxAutomaticContributionState) {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT automatic_contribution_state FROM boxes WHERE id = ?`, boxID).Scan(&state); err != nil {
		t.Fatalf("query lifecycle state for %s: %v", boxID, err)
	}
	if state != string(wantState) {
		t.Fatalf("lifecycle state for %s = %q, want %q", boxID, state, wantState)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_contribution_lifecycle_events WHERE box_id = ?`, boxID).Scan(&events); err != nil {
		t.Fatalf("count lifecycle events for %s: %v", boxID, err)
	}
	if events != wantEvents {
		t.Fatalf("lifecycle events for %s = %d, want %d", boxID, events, wantEvents)
	}
}

func assertLifecycleEvent(t *testing.T, db *sql.DB, boxID string, wantCount int, wantType BoxContributionLifecycleEventType, wantPrevious, wantNew BoxAutomaticContributionState) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_contribution_lifecycle_events WHERE box_id = ?`, boxID).Scan(&count); err != nil {
		t.Fatalf("count lifecycle events: %v", err)
	}
	if count != wantCount {
		t.Fatalf("lifecycle event count = %d, want %d", count, wantCount)
	}
	var eventType, previousState, newState string
	if err := db.QueryRow(`
		SELECT event_type, previous_state, new_state
		FROM box_contribution_lifecycle_events
		WHERE box_id = ?
		ORDER BY occurred_at DESC, id DESC
		LIMIT 1
	`, boxID).Scan(&eventType, &previousState, &newState); err != nil {
		t.Fatalf("query latest lifecycle event: %v", err)
	}
	if eventType != string(wantType) || previousState != string(wantPrevious) || newState != string(wantNew) {
		t.Fatalf("latest lifecycle event = (%q,%q,%q), want (%q,%q,%q)", eventType, previousState, newState, wantType, wantPrevious, wantNew)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func sumLedgerForAllBoxes(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var sum int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger`).Scan(&sum); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	return sum
}

func lifecycleBoxFinancialSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`
		SELECT id, target_amount, monthly_recharge
		FROM boxes
		ORDER BY id
	`)
	if err != nil {
		t.Fatalf("query box financial snapshot: %v", err)
	}
	defer rows.Close()
	var snapshot strings.Builder
	for rows.Next() {
		var id string
		var target, recharge int64
		if err := rows.Scan(&id, &target, &recharge); err != nil {
			t.Fatalf("scan box financial snapshot: %v", err)
		}
		fmt.Fprintf(&snapshot, "%s:%d:%d|", id, target, recharge)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("box financial snapshot rows: %v", err)
	}
	return snapshot.String()
}
