package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

var ErrBalanceAdjustmentConflict = errors.New("saldo alterado desde a abertura do formulário")
var ErrBalanceAdjustmentUnchanged = errors.New("o saldo informado já é o saldo atual")

type AccountBalanceAdjustment struct {
	ID, WorkspaceID, AccountID, UserID, Reason string
	ExpectedVersion, TargetBalance, CreatedAt  int64
}

// AdjustAccountBalance records a reconciliation, not an income or expense.
// The first statement acquires SQLite's writer lock before reading the balance
// or checking idempotency, avoiding a read snapshot upgrade race.
func AdjustAccountBalance(ctx context.Context, db *sql.DB, input AccountBalanceAdjustment) error {
	input.Reason = strings.TrimSpace(input.Reason)
	if input.ID == "" || input.WorkspaceID == "" || input.AccountID == "" || input.UserID == "" || input.ExpectedVersion < 0 || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 500 {
		return fmt.Errorf("ajuste inválido")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE accounts SET balance_version = balance_version
		WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, input.AccountID, input.WorkspaceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}

	var accountID, userID, reason string
	var target, expected int64
	err = tx.QueryRowContext(ctx, `SELECT account_id, created_by, reason, after_balance, expected_version
		FROM account_balance_adjustments WHERE id = ? AND workspace_id = ?`, input.ID, input.WorkspaceID).Scan(&accountID, &userID, &reason, &target, &expected)
	if err == nil {
		if accountID != input.AccountID || userID != input.UserID || reason != input.Reason || target != input.TargetBalance || expected != input.ExpectedVersion {
			return ErrBalanceAdjustmentConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var current, version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_balance, balance_version FROM accounts WHERE id = ? AND workspace_id = ?`, input.AccountID, input.WorkspaceID).Scan(&current, &version); err != nil {
		return err
	}
	if version != input.ExpectedVersion {
		return ErrBalanceAdjustmentConflict
	}
	if current == input.TargetBalance {
		return ErrBalanceAdjustmentUnchanged
	}
	if (current < 0 && input.TargetBalance > math.MaxInt64+current) || (current > 0 && input.TargetBalance < math.MinInt64+current) {
		return ErrBalanceOutOfRange
	}
	delta := input.TargetBalance - current
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_balance_adjustments
		(id, workspace_id, account_id, created_by, created_at, expected_version, before_balance, after_balance, delta_cents, reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, input.ID, input.WorkspaceID, input.AccountID, input.UserID, input.CreatedAt, version, current, input.TargetBalance, delta, input.Reason); err != nil {
		return err
	}
	res, err = tx.ExecContext(ctx, `UPDATE accounts SET current_balance = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND balance_version = ?`, input.TargetBalance, input.CreatedAt, input.AccountID, input.WorkspaceID, version)
	if err != nil {
		return err
	}
	n, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBalanceAdjustmentConflict
	}
	return tx.Commit()
}
