package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type BoxAutomaticContributionState string

const (
	BoxAutomaticContributionStateActive  BoxAutomaticContributionState = "ACTIVE"
	BoxAutomaticContributionStateBlocked BoxAutomaticContributionState = "BLOCKED"
)

type BoxContributionLifecycleEventType string

const (
	BoxContributionLifecycleEventGoalReached BoxContributionLifecycleEventType = "GOAL_REACHED"
	BoxContributionLifecycleEventBlocked     BoxContributionLifecycleEventType = "CONTRIBUTION_BLOCKED"
	BoxContributionLifecycleEventReactivated BoxContributionLifecycleEventType = "CONTRIBUTION_REACTIVATED"
)

func (state BoxAutomaticContributionState) valid() bool {
	return state == BoxAutomaticContributionStateActive || state == BoxAutomaticContributionStateBlocked
}

type boxContributionLifecycleStore interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// TransitionBoxAutomaticContributionState persists an explicit lifecycle
// transition and its corresponding history event atomically. It does not
// inspect balances, targets or contribution amounts; R3 owns those policies.
// Repeating a transition to the current state is an idempotent no-op.
func TransitionBoxAutomaticContributionState(
	ctx context.Context,
	db *sql.DB,
	workspaceID string,
	boxID string,
	targetState BoxAutomaticContributionState,
	occurredAt time.Time,
) (changed bool, err error) {
	if db == nil {
		return false, fmt.Errorf("box contribution lifecycle: nil database")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return false, fmt.Errorf("box contribution lifecycle: workspace id is required")
	}
	boxID = strings.TrimSpace(boxID)
	if boxID == "" {
		return false, fmt.Errorf("box contribution lifecycle: box id is required")
	}
	if !targetState.valid() {
		return false, fmt.Errorf("box contribution lifecycle: invalid target state %q", targetState)
	}
	if occurredAt.IsZero() {
		return false, fmt.Errorf("box contribution lifecycle: zero transition time")
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return false, fmt.Errorf("box contribution lifecycle acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return false, fmt.Errorf("box contribution lifecycle begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	changed, err = transitionBoxAutomaticContributionState(
		ctx,
		conn,
		workspaceID,
		boxID,
		targetState,
		occurredAt.Unix(),
	)
	if err != nil {
		return false, err
	}

	if _, err = conn.ExecContext(ctx, `COMMIT`); err != nil {
		return false, fmt.Errorf("box contribution lifecycle commit box %q: %w", boxID, err)
	}
	committed = true
	return changed, nil
}

func transitionBoxAutomaticContributionState(
	ctx context.Context,
	store boxContributionLifecycleStore,
	workspaceID string,
	boxID string,
	targetState BoxAutomaticContributionState,
	occurredAt int64,
) (bool, error) {
	var currentRaw string
	if err := store.QueryRowContext(ctx, `
		SELECT automatic_contribution_state
		FROM boxes
		WHERE id = ? AND workspace_id = ?
	`, boxID, workspaceID).Scan(&currentRaw); err != nil {
		return false, fmt.Errorf("box contribution lifecycle read box %q: %w", boxID, err)
	}
	currentState := BoxAutomaticContributionState(currentRaw)
	if !currentState.valid() {
		return false, fmt.Errorf("box contribution lifecycle box %q has invalid current state %q", boxID, currentRaw)
	}
	if currentState == targetState {
		return false, nil
	}

	var eventType BoxContributionLifecycleEventType
	switch {
	case currentState == BoxAutomaticContributionStateActive && targetState == BoxAutomaticContributionStateBlocked:
		eventType = BoxContributionLifecycleEventBlocked
	case currentState == BoxAutomaticContributionStateBlocked && targetState == BoxAutomaticContributionStateActive:
		eventType = BoxContributionLifecycleEventReactivated
	default:
		return false, fmt.Errorf("box contribution lifecycle unsupported transition %q -> %q", currentState, targetState)
	}

	result, err := store.ExecContext(ctx, `
		UPDATE boxes
		SET automatic_contribution_state = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND automatic_contribution_state = ?
	`, string(targetState), occurredAt, boxID, workspaceID, string(currentState))
	if err != nil {
		return false, fmt.Errorf("box contribution lifecycle update box %q: %w", boxID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("box contribution lifecycle update result box %q: %w", boxID, err)
	}
	if rowsAffected != 1 {
		return false, fmt.Errorf("box contribution lifecycle update box %q affected %d rows", boxID, rowsAffected)
	}

	eventResult, err := store.ExecContext(ctx, `
		INSERT INTO box_contribution_lifecycle_events (
			id, box_id, event_type, previous_state, new_state, occurred_at
		)
		SELECT ?, b.id, ?, ?, ?, ?
		FROM boxes b
		WHERE b.id = ? AND b.workspace_id = ?
	`, uuid.NewString(), string(eventType), string(currentState), string(targetState), occurredAt, boxID, workspaceID)
	if err != nil {
		return false, fmt.Errorf("box contribution lifecycle insert event for box %q: %w", boxID, err)
	}
	if affected, err := eventResult.RowsAffected(); err != nil || affected != 1 {
		return false, fmt.Errorf("box contribution lifecycle insert event for box %q affected %d rows: %v", boxID, affected, err)
	}
	return true, nil
}

func recordBoxContributionGoalReached(
	ctx context.Context,
	store boxContributionLifecycleStore,
	workspaceID string,
	boxID string,
	occurredAt int64,
) error {
	result, err := store.ExecContext(ctx, `
		INSERT INTO box_contribution_lifecycle_events (
			id, box_id, event_type, previous_state, new_state, occurred_at
		)
		SELECT ?, b.id, ?, NULL, NULL, ?
		FROM boxes b
		WHERE b.id = ? AND b.workspace_id = ?
	`, uuid.NewString(), string(BoxContributionLifecycleEventGoalReached), occurredAt, boxID, workspaceID)
	if err != nil {
		return fmt.Errorf("box contribution lifecycle insert goal reached event for box %q: %w", boxID, err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return fmt.Errorf("box contribution lifecycle insert goal reached event for box %q affected %d rows: %v", boxID, affected, err)
	}
	return nil
}
