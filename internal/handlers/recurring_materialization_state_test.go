package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func seedMaterializationWorkspace(t *testing.T, db *sql.DB, workspaceID, userID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, userID, userID+"@example.com", now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, workspaceID, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, workspaceID, userID, now)
}

func readMaterializationState(t *testing.T, db *sql.DB, workspaceID string) *RecurringMaterializationState {
	t.Helper()
	var s RecurringMaterializationState
	err := db.QueryRow(`
		SELECT workspace_id, materialized_until, materialized_until_civil, last_attempt_at, last_success_at,
		       last_error, status, locked_until, lock_owner
		FROM recurring_materialization_state
		WHERE workspace_id = ?
	`, workspaceID).Scan(
		&s.WorkspaceID, &s.MaterializedUntil, &s.MaterializedUntilCivil, &s.LastAttemptAt, &s.LastSuccessAt,
		&s.LastError, &s.Status, &s.LockedUntil, &s.LockOwner,
	)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		t.Fatalf("read materialization state: %v", err)
	}
	return &s
}

func TestDefaultRecurringHorizonIs15Months(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	now := time.Date(2026, 6, 5, 9, 30, 0, 0, time.UTC).Unix()
	horizon, err := defaultRecurringHorizon(now)
	if err != nil {
		t.Fatalf("defaultRecurringHorizon: %v", err)
	}
	if horizon.Civil() != "2027-09-05" {
		t.Fatalf("horizon.Civil() = %q, want 2027-09-05", horizon.Civil())
	}
	wantLegacy := time.Date(2027, 9, 5, 12, 0, 0, 0, time.UTC).Unix()
	if horizon.LegacyUnix() != wantLegacy {
		t.Fatalf("horizon.LegacyUnix() = %d, want %d (noon UTC)", horizon.LegacyUnix(), wantLegacy)
	}
}

func TestRunRecurringMaterializationNoStateIsStaleAndExecutes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	var called int
	runner := func(tx *sql.Tx, workspaceID string, horizon CivilWriteDate, n int64) error {
		called++
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 1 {
		t.Fatalf("runner called %d times, want 1 (no state = stale)", called)
	}
	state := readMaterializationState(t, db, "ws-mat")
	if state == nil {
		t.Fatalf("state row not created")
	}
	if state.Status != "idle" {
		t.Fatalf("status = %q, want idle", state.Status)
	}
}

func TestRunRecurringMaterializationFreshSkipsRunner(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	horizon, _ := defaultRecurringHorizon(now)
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', ?, 'idle', ?, ?)
	`, horizon.LegacyUnix(), now, now)

	var called int
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		called++
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 0 {
		t.Fatalf("runner called %d times, want 0 (fresh)", called)
	}
}

func TestRunRecurringMaterializationStaleExecutesRunner(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', 0, 'idle', ?, ?)
	`, now, now)

	var called int
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		called++
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 1 {
		t.Fatalf("runner called %d times, want 1 (stale)", called)
	}
}

func TestRunRecurringMaterializationUpdatesLastAttemptAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, last_attempt_at, status, created_at, updated_at)
		VALUES ('ws-mat', 0, 0, 'idle', ?, ?)
	`, now, now)

	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	state := readMaterializationState(t, db, "ws-mat")
	if state.LastAttemptAt != now {
		t.Fatalf("last_attempt_at = %d, want %d (updated on lock acquire)", state.LastAttemptAt, now)
	}
}

func TestRunRecurringMaterializationCanceledContextDoesNotExecuteRunner(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var called int
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		called++
		return nil
	}
	err := runRecurringMaterializationForWorkspace(ctx, db, "ws-mat", now, runner)
	if err == nil {
		t.Fatalf("expected error from canceled context")
	}
	if called != 0 {
		t.Fatalf("runner called %d times, want 0 (context canceled)", called)
	}
	if state := readMaterializationState(t, db, "ws-mat"); state != nil {
		t.Fatalf("state should not be created when context canceled, got %+v", state)
	}
}

func TestRunRecurringMaterializationLockActivePreventsExecution(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, locked_until, lock_owner, created_at, updated_at)
		VALUES ('ws-mat', 0, 'running', ?, 'other-owner', ?, ?)
	`, now+600, now, now)

	var called int
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		called++
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run should not be fatal when lock is held: %v", err)
	}
	if called != 0 {
		t.Fatalf("runner called %d times, want 0 (lock active)", called)
	}
	state := readMaterializationState(t, db, "ws-mat")
	if state.LockOwner.String != "other-owner" {
		t.Fatalf("lock_owner = %q, want other-owner (untouched)", state.LockOwner.String)
	}
}

func TestRunRecurringMaterializationLockExpiredAllowsExecution(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, locked_until, lock_owner, created_at, updated_at)
		VALUES ('ws-mat', 0, 'running', ?, 'stale-owner', ?, ?)
	`, now-10, now, now)

	var called int
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		called++
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 1 {
		t.Fatalf("runner called %d times, want 1 (lock expired)", called)
	}
}

func TestRunRecurringMaterializationSuccessUpdatesState(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	horizon, _ := defaultRecurringHorizon(now)
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, last_error, created_at, updated_at)
		VALUES ('ws-mat', 0, 'error', 'previous failure', ?, ?)
	`, now, now)

	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner); err != nil {
		t.Fatalf("run: %v", err)
	}
	state := readMaterializationState(t, db, "ws-mat")
	if state.Status != "idle" {
		t.Fatalf("status = %q, want idle", state.Status)
	}
	if state.MaterializedUntil != horizon.LegacyUnix() {
		t.Fatalf("materialized_until = %d, want %d", state.MaterializedUntil, horizon.LegacyUnix())
	}
	if state.LastSuccessAt != now {
		t.Fatalf("last_success_at = %d, want %d", state.LastSuccessAt, now)
	}
	if state.LastError.Valid {
		t.Fatalf("last_error = %q, want NULL", state.LastError.String)
	}
	if state.LockedUntil != 0 || state.LockOwner.Valid {
		t.Fatalf("lock not released: locked_until=%d owner=%v", state.LockedUntil, state.LockOwner)
	}
}

func TestRunRecurringMaterializationErrorRecordsAndReleasesLock(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_materialization_state (workspace_id, materialized_until, status, created_at, updated_at)
		VALUES ('ws-mat', 0, 'idle', ?, ?)
	`, now, now)

	runErr := errors.New("boom no motor")
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		return runErr
	}
	err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat", now, runner)
	if err == nil {
		t.Fatalf("expected error from runner")
	}
	state := readMaterializationState(t, db, "ws-mat")
	if state.Status != "error" {
		t.Fatalf("status = %q, want error", state.Status)
	}
	if !state.LastError.Valid || state.LastError.String != "boom no motor" {
		t.Fatalf("last_error = %v, want 'boom no motor'", state.LastError)
	}
	if state.LockedUntil != 0 || state.LockOwner.Valid {
		t.Fatalf("lock not released after error: locked_until=%d owner=%v", state.LockedUntil, state.LockOwner)
	}
}

func TestRunRecurringMaterializationWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat-a", "user-a")
	seedMaterializationWorkspace(t, db, "ws-mat-b", "user-b")

	now := time.Now().Unix()
	var calledWorkspaces []string
	runner := func(tx *sql.Tx, workspaceID string, h CivilWriteDate, n int64) error {
		calledWorkspaces = append(calledWorkspaces, workspaceID)
		return nil
	}
	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-mat-a", now, runner); err != nil {
		t.Fatalf("run a: %v", err)
	}
	if len(calledWorkspaces) != 1 || calledWorkspaces[0] != "ws-mat-a" {
		t.Fatalf("runner workspaces = %v, want [ws-mat-a]", calledWorkspaces)
	}
	if stateB := readMaterializationState(t, db, "ws-mat-b"); stateB != nil {
		t.Fatalf("ws-mat-b state should not exist, got %+v", stateB)
	}
}

func TestRunRecurringMaterializationAtomicLockSingleWinner(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMaterializationWorkspace(t, db, "ws-mat", "user-mat")

	now := time.Now().Unix()
	if err := ensureRecurringMaterializationStateRow(db, "ws-mat", now); err != nil {
		t.Fatalf("ensure row: %v", err)
	}

	tx1, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	acquired1, err := acquireRecurringMaterializationLockTx(tx1, "ws-mat", "owner-1", now, now+600)
	if err != nil {
		t.Fatalf("acquire tx1: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("commit tx1: %v", err)
	}

	tx2, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	acquired2, err := acquireRecurringMaterializationLockTx(tx2, "ws-mat", "owner-2", now, now+600)
	if err != nil {
		t.Fatalf("acquire tx2: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}

	if !acquired1 || acquired2 {
		t.Fatalf("expected only first acquirer to win: acquired1=%v acquired2=%v", acquired1, acquired2)
	}
}

func TestRunRecurringMaterializationTwiceDoesNotDuplicate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	_, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC).Unix()
	if err := RunRecurringMaterializationForWorkspace(context.Background(), db, "ws-test", now); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	var count1 int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count1); err != nil {
		t.Fatalf("count 1: %v", err)
	}

	if err := RunRecurringMaterializationForWorkspace(context.Background(), db, "ws-test", now); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	var count2 int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count2); err != nil {
		t.Fatalf("count 2: %v", err)
	}

	if count1 != count2 {
		t.Fatalf("materialization not idempotent: count1=%d count2=%d", count1, count2)
	}
	if count1 == 0 {
		t.Fatalf("expected occurrences to be materialized, got 0")
	}
}
