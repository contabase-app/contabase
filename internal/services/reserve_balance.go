package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
)

type WorkspaceReserveBalance struct {
	RealBalance     int64
	ReservedBalance int64
	FreeBalance     int64
}

var ErrBalanceAggregateOutOfRange = errors.New("agregado de saldo fora do intervalo int64")

func CalculateWorkspaceReserveBalance(db *sql.DB, workspaceID string) (WorkspaceReserveBalance, error) {
	var balance WorkspaceReserveBalance
	if db == nil {
		return balance, fmt.Errorf("db obrigatório")
	}
	return calculateWorkspaceReserveBalance(context.Background(), db, workspaceID)
}

type reserveBalanceQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func calculateWorkspaceReserveBalance(ctx context.Context, db reserveBalanceQuerier, workspaceID string) (WorkspaceReserveBalance, error) {
	var balance WorkspaceReserveBalance
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return balance, fmt.Errorf("workspace obrigatório")
	}

	real, err := sumReserveBalanceRows(ctx, db, `
		SELECT current_balance
		FROM accounts
		WHERE workspace_id = ?
		  AND type != ?
		  AND archived_at IS NULL
	`, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return balance, fmt.Errorf("calcular saldo real: %w", err)
	}

	reserved, err := sumReserveBalanceRows(ctx, db, `
		SELECT COALESCE(l.amount, 0)
		FROM boxes b
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE b.workspace_id = ?
	`, workspaceID)
	if err != nil {
		return balance, fmt.Errorf("calcular saldo reservado: %w", err)
	}

	free := new(big.Int).Sub(real, reserved)
	for _, aggregate := range []struct {
		name  string
		value *big.Int
	}{
		{"real", real},
		{"reservado", reserved},
		{"disponível", free},
	} {
		if !aggregate.value.IsInt64() {
			return balance, fmt.Errorf("calcular saldo %s: %w", aggregate.name, ErrBalanceAggregateOutOfRange)
		}
	}
	balance.RealBalance = real.Int64()
	balance.ReservedBalance = reserved.Int64()
	balance.FreeBalance = free.Int64()
	return balance, nil
}

func sumReserveBalanceRows(ctx context.Context, db reserveBalanceQuerier, query string, args ...any) (*big.Int, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	total := new(big.Int)
	valueBig := new(big.Int)
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		total.Add(total, valueBig.SetInt64(value))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return total, nil
}
