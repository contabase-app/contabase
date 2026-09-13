package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"

	"github.com/contabase-app/contabase/internal/services"
)

// AccountClosingBalance holds the reconstructed balance of a real account at a given period end.
type AccountClosingBalance struct {
	ID         string
	Name       string
	Type       string
	Balance    int64
	Color      string
	Icon       string
	IsArchived bool
}

// BuildAccountClosingBalances reconstructs the operational balance of real accounts
// at periodEnd by starting from current_balance and reversing paid movements
// whose effective date (COALESCE(paid_at, date)) falls after periodEnd.
// Reconciliations are reversed by their persisted delta when created_at > periodEnd.
// periodEnd is an inclusive Unix second; monthly callers use 23:59:59 UTC.
//
// Real account types: CHECKING, SAVINGS, WALLET, INVESTMENT.
// CREDIT_CARD is excluded.
//
// This is an operational reconstruction, not an auditable accounting close.
func BuildAccountClosingBalances(db *sql.DB, workspaceID string, periodEnd int64) ([]AccountClosingBalance, error) {
	ctx := context.Background()
	// Balance and both histories must come from the same committed snapshot.
	readTx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin account closing snapshot: %w", err)
	}
	defer readTx.Rollback()

	realTypes := []interface{}{"CHECKING", "SAVINGS", "WALLET", "INVESTMENT"}

	rows, err := readTx.QueryContext(ctx, `
		SELECT id, COALESCE(name, ''), COALESCE(type, ''),
		       COALESCE(current_balance, 0),
		       COALESCE(color, ''), COALESCE(icon, ''),
		       CASE WHEN archived_at IS NOT NULL THEN 1 ELSE 0 END
		FROM accounts
		WHERE workspace_id = ? AND type IN (?, ?, ?, ?)
		ORDER BY type, name
	`, append([]interface{}{workspaceID}, realTypes...)...)
	if err != nil {
		return nil, fmt.Errorf("query accounts for closing: %w", err)
	}
	defer rows.Close()

	// Exact intermediate sums avoid order-dependent overflow when movements and
	// adjustments cancel. Persisted inputs and the returned balances remain int64.
	accounts := make(map[string]*big.Int)
	var ordered []*AccountClosingBalance
	for rows.Next() {
		var acc AccountClosingBalance
		var archived int
		if err := rows.Scan(&acc.ID, &acc.Name, &acc.Type, &acc.Balance, &acc.Color, &acc.Icon, &archived); err != nil {
			return nil, fmt.Errorf("scan account closing: %w", err)
		}
		acc.IsArchived = archived == 1
		accounts[acc.ID] = big.NewInt(acc.Balance)
		ordered = append(ordered, &acc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows accounts closing: %w", err)
	}

	if len(accounts) == 0 {
		return nil, readTx.Commit()
	}

	txRows, err := readTx.QueryContext(ctx, `
		SELECT t.account_id, t.destination_account_id, t.type, t.amount
		FROM transactions t
		WHERE t.workspace_id = ?
		  AND t.status = 'paid'
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) > ?
	`, workspaceID, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("query post-period transactions: %w", err)
	}
	defer txRows.Close()

	for txRows.Next() {
		var accountID string
		var destID sql.NullString
		var txType string
		var amount int64
		if err := txRows.Scan(&accountID, &destID, &txType, &amount); err != nil {
			return nil, fmt.Errorf("scan post-period tx: %w", err)
		}

		// INCOME on real account: current was increased by amount. Reverse: subtract.
		if txType == "INCOME" {
			if acc, ok := accounts[accountID]; ok {
				acc.Sub(acc, big.NewInt(amount))
			}
			continue
		}

		// EXPENSE on real account: current was decreased by amount. Reverse: add back.
		if txType == "EXPENSE" {
			if acc, ok := accounts[accountID]; ok {
				acc.Add(acc, big.NewInt(amount))
			}
			continue
		}

		// TRANSFER: source account decreased, destination account increased.
		if txType == "TRANSFER" {
			// Reverse source effect: add amount back to source.
			if acc, ok := accounts[accountID]; ok {
				acc.Add(acc, big.NewInt(amount))
			}
			// Reverse destination effect: subtract amount from destination.
			if destID.Valid {
				if acc, ok := accounts[destID.String]; ok {
					acc.Sub(acc, big.NewInt(amount))
				}
			}
		}
	}
	if err := txRows.Err(); err != nil {
		return nil, fmt.Errorf("rows post-period tx: %w", err)
	}
	adjustments, err := readTx.QueryContext(ctx, `
		SELECT h.account_id, h.delta_cents
		FROM account_balance_adjustments h
		JOIN accounts a ON a.id = h.account_id AND a.workspace_id = h.workspace_id
		WHERE h.workspace_id = ? AND h.created_at > ?
	`, workspaceID, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("query post-period reconciliations: %w", err)
	}
	defer adjustments.Close()
	for adjustments.Next() {
		var accountID string
		var delta int64
		if err := adjustments.Scan(&accountID, &delta); err != nil {
			return nil, fmt.Errorf("scan post-period reconciliation: %w", err)
		}
		if acc, ok := accounts[accountID]; ok {
			acc.Sub(acc, big.NewInt(delta))
		}
	}
	if err := adjustments.Err(); err != nil {
		return nil, fmt.Errorf("rows post-period reconciliations: %w", err)
	}

	var result []AccountClosingBalance
	for _, acc := range ordered {
		balance := accounts[acc.ID]
		if !balance.IsInt64() {
			return nil, fmt.Errorf("reconstructed account balance: %w", services.ErrBalanceOutOfRange)
		}
		acc.Balance = balance.Int64()
		if !acc.IsArchived {
			result = append(result, *acc)
			continue
		}
		// Include archived account only if it has non-zero reconstructed balance
		// (current_balance was already adjusted above, so acc.Balance is the reconstructed value).
		if acc.Balance != 0 {
			result = append(result, *acc)
		}
	}

	if err := readTx.Commit(); err != nil {
		return nil, fmt.Errorf("finish account closing snapshot: %w", err)
	}
	return result, nil
}
