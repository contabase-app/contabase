package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
)

type WorkspaceReserveBalance struct {
	RealBalance     int64
	ReservedBalance int64
	FreeBalance     int64
}

func CalculateWorkspaceReserveBalance(db *sql.DB, workspaceID string) (WorkspaceReserveBalance, error) {
	var balance WorkspaceReserveBalance
	if db == nil {
		return balance, fmt.Errorf("db obrigatório")
	}
	return calculateWorkspaceReserveBalance(context.Background(), db, workspaceID)
}

type reserveBalanceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func calculateWorkspaceReserveBalance(ctx context.Context, db reserveBalanceQuerier, workspaceID string) (WorkspaceReserveBalance, error) {
	var balance WorkspaceReserveBalance
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return balance, fmt.Errorf("workspace obrigatório")
	}

	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(current_balance), 0)
		FROM accounts
		WHERE workspace_id = ?
		  AND type != ?
		  AND archived_at IS NULL
	`, workspaceID, models.AccountTypeCreditCard).Scan(&balance.RealBalance); err != nil {
		return balance, fmt.Errorf("calcular saldo real: %w", err)
	}

	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(l.amount), 0)
		FROM boxes b
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE b.workspace_id = ?
	`, workspaceID).Scan(&balance.ReservedBalance); err != nil {
		return balance, fmt.Errorf("calcular saldo reservado: %w", err)
	}

	freeBalance, err := checkedSubtract(balance.RealBalance, balance.ReservedBalance)
	if err != nil {
		return balance, fmt.Errorf("calcular saldo disponível: %w", err)
	}
	balance.FreeBalance = freeBalance
	return balance, nil
}
