package handlers

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func seedActiveRecurringRule(t *testing.T, db *sql.DB, workspaceID, userID, ruleID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, userID, userID+"@example.com", now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, workspaceID, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, workspaceID, userID, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Conta', 'CHECKING', 0, 0, ?, ?)`, "acc-"+workspaceID, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at) VALUES (?, ?, 'Cat', 'tag', '#000', 'EXPENSE', ?)`, "cat-"+workspaceID, workspaceID, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, ?, ?, 1, NULL, ?, ?)
	`, "series-"+ruleID, workspaceID, userID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', 15000, 'Assinatura', ?, 'MONTHLY', 'PENDING', 1, NULL, ?, 1, ?, ?)
	`, ruleID, workspaceID, userID, "acc-"+workspaceID, "cat-"+workspaceID, testUnixDate("2026-06-05"), "series-"+ruleID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', 15000, ?, 'Assinatura', 'pending', 1, 1, ?, 1, ?, ?, ?)
	`, "tx-"+ruleID, workspaceID, userID, "acc-"+workspaceID, "cat-"+workspaceID, testUnixDate("2026-06-05"), ruleID, "series-"+ruleID, now, now)
}

func TestRecurringMaterializationSweepProcessesActiveWorkspaces(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedActiveRecurringRule(t, db, "ws-a", "user-a", "rule-a")
	seedActiveRecurringRule(t, db, "ws-b", "user-b", "rule-b")
	seedMaterializationWorkspace(t, db, "ws-empty", "user-empty")

	var mu sync.Mutex
	var seen []string
	runner := func(ctx context.Context, d *sql.DB, workspaceID string, now int64) error {
		mu.Lock()
		seen = append(seen, workspaceID)
		mu.Unlock()
		return nil
	}
	count, err := runRecurringMaterializationSweep(context.Background(), db, time.Now(), runner)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if count != 2 {
		t.Fatalf("processed = %d, want 2", count)
	}
	if len(seen) != 2 {
		t.Fatalf("runner called for %v, want [ws-a ws-b]", seen)
	}
	for _, id := range seen {
		if id == "ws-empty" {
			t.Fatalf("sweep processed workspace without active rules")
		}
	}
}

func TestRecurringMaterializationSweepContinuesOnWorkspaceError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedActiveRecurringRule(t, db, "ws-a", "user-a", "rule-a")
	seedActiveRecurringRule(t, db, "ws-b", "user-b", "rule-b")

	runner := func(ctx context.Context, d *sql.DB, workspaceID string, now int64) error {
		if workspaceID == "ws-a" {
			return errors.New("boom")
		}
		return nil
	}
	count, err := runRecurringMaterializationSweep(context.Background(), db, time.Now(), runner)
	if err != nil {
		t.Fatalf("sweep should not return fatal error: %v", err)
	}
	if count != 1 {
		t.Fatalf("processed = %d, want 1 (ws-b only, ws-a errored)", count)
	}
}

func TestRecurringMaterializationSweepRealRunnerMaterializes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedActiveRecurringRule(t, db, "ws-a", "user-a", "rule-a")

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	count, err := RunRecurringMaterializationSweep(context.Background(), db, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if count != 1 {
		t.Fatalf("processed = %d, want 1", count)
	}
	var txCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-a' AND series_id = 'series-rule-a'`).Scan(&txCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if txCount <= 1 {
		t.Fatalf("expected materialized occurrences, got %d", txCount)
	}
	var materializedUntil int64
	if err := db.QueryRow(`SELECT materialized_until FROM recurring_materialization_state WHERE workspace_id = 'ws-a'`).Scan(&materializedUntil); err != nil {
		t.Fatalf("state: %v", err)
	}
	if materializedUntil != defaultRecurringHorizon(now.Unix()).Unix() {
		t.Fatalf("materialized_until = %d, want %d", materializedUntil, defaultRecurringHorizon(now.Unix()).Unix())
	}
}

func TestRecurringMaterializationSweepCanceledContext(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedActiveRecurringRule(t, db, "ws-a", "user-a", "rule-a")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var called int
	runner := func(ctx context.Context, d *sql.DB, workspaceID string, now int64) error {
		called++
		return nil
	}
	_, err := runRecurringMaterializationSweep(ctx, db, time.Now(), runner)
	if err == nil {
		t.Fatalf("expected error from canceled context")
	}
	if called != 0 {
		t.Fatalf("runner called %d times, want 0", called)
	}
}

func TestStartRecurringMaterializationJobDoesNotBlock(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedActiveRecurringRule(t, db, "ws-a", "user-a", "rule-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		StartRecurringMaterializationJob(ctx, db, time.Hour)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("StartRecurringMaterializationJob blocked")
	}

	// Aguarda a varredura assíncrona de boot materializar.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_materialization_state WHERE workspace_id = 'ws-a'`).Scan(&count); err == nil && count == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("boot async sweep did not run")
}

func TestFallbackFreshDoesNotDispatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	horizon := defaultRecurringHorizon(now)
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', ?, 'idle', ?, ?)
	`, horizon.Unix(), now, now)

	scheduler := newRecurringMaterializationScheduler(db)
	var dispatched int
	scheduler.dispatch = func(workspaceID string, n int64) { dispatched++ }

	if fired := scheduler.maybeSchedule("ws-mat", now); fired {
		t.Fatalf("fresh state should not dispatch")
	}
	if dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0", dispatched)
	}
}

func TestFallbackStaleDispatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', 0, 'idle', ?, ?)
	`, now, now)

	scheduler := newRecurringMaterializationScheduler(db)
	var dispatched []string
	scheduler.dispatch = func(workspaceID string, n int64) { dispatched = append(dispatched, workspaceID) }

	if fired := scheduler.maybeSchedule("ws-mat", now); !fired {
		t.Fatalf("stale state should dispatch")
	}
	if len(dispatched) != 1 || dispatched[0] != "ws-mat" {
		t.Fatalf("dispatched = %v, want [ws-mat]", dispatched)
	}
}

func TestFallbackNoStateIsStaleAndDispatches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	scheduler := newRecurringMaterializationScheduler(db)
	var dispatched int
	scheduler.dispatch = func(workspaceID string, n int64) { dispatched++ }

	if fired := scheduler.maybeSchedule("ws-mat", time.Now().Unix()); !fired {
		t.Fatalf("missing state should be treated as stale and dispatch")
	}
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", dispatched)
	}
}

func TestFallbackCooldownPreventsRepeatDispatch(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', 0, 'idle', ?, ?)
	`, now, now)

	scheduler := newRecurringMaterializationScheduler(db)
	var dispatched int
	scheduler.dispatch = func(workspaceID string, n int64) { dispatched++ }

	if fired := scheduler.maybeSchedule("ws-mat", now); !fired {
		t.Fatalf("first call should dispatch")
	}
	if fired := scheduler.maybeSchedule("ws-mat", now+10); fired {
		t.Fatalf("second call within cooldown should not dispatch")
	}
	if dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1 (cooldown)", dispatched)
	}
}

func TestFallbackTwoSimultaneousDispatchSingleMotorViaLock(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedActiveRecurringRule(t, db, "ws-a", "user-a", "rule-a")

	now := time.Now().Unix()
	if err := ensureRecurringMaterializationStateRow(db, "ws-a", now); err != nil {
		t.Fatalf("ensure row: %v", err)
	}

	var mu sync.Mutex
	motorRuns := 0
	runner := func(tx *sql.Tx, workspaceID string, horizon time.Time, n int64) error {
		mu.Lock()
		motorRuns++
		mu.Unlock()
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = runRecurringMaterializationForWorkspace(context.Background(), db, "ws-a", now, runner)
		}()
	}
	wg.Wait()

	if motorRuns != 1 {
		t.Fatalf("motor ran %d times, want 1 (lock prevents duplicate)", motorRuns)
	}
}

func TestFallbackDispatchErrorDoesNotPanic(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', 0, 'idle', ?, ?)
	`, now, now)

	scheduler := newRecurringMaterializationScheduler(db)
	done := make(chan struct{})
	scheduler.dispatch = func(workspaceID string, n int64) {
		// Simula um dispatch que roda o motor com erro; não deve propagar.
		go func() {
			defer close(done)
			_ = runRecurringMaterializationForWorkspace(context.Background(), db, workspaceID, n, func(tx *sql.Tx, ws string, h time.Time, nn int64) error {
				return errors.New("motor boom")
			})
		}()
	}

	if fired := scheduler.maybeSchedule("ws-mat", now); !fired {
		t.Fatalf("stale should dispatch")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("dispatch goroutine did not finish")
	}
	state := readMaterializationState(t, db, "ws-mat")
	if state.Status != "error" {
		t.Fatalf("status = %q, want error", state.Status)
	}
}

func TestScheduleRecurringMaterializationFallbackNilSchedulerIsNoop(t *testing.T) {
	packageSchedulerMu.Lock()
	prev := packageRecurringScheduler
	packageRecurringScheduler = nil
	packageSchedulerMu.Unlock()
	defer func() {
		packageSchedulerMu.Lock()
		packageRecurringScheduler = prev
		packageSchedulerMu.Unlock()
	}()

	// Não deve entrar em panic quando o scheduler global não foi inicializado.
	ScheduleRecurringMaterializationFallback("ws-any")
}

func TestFallbackLockedExpiredTakenEvenWhenStatusRunning(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, locked_until, lock_owner, created_at, updated_at)
		VALUES ('ws-mat', 0, 'running', ?, 'dead-owner', ?, ?)
	`, now-10, now, now)

	var called int
	runner := func(tx *sql.Tx, workspaceID string, h time.Time, n int64) error {
		called++
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 1 {
		t.Fatalf("runner called %d times, want 1 (expired lock taken despite status=running)", called)
	}
}
