package handlers

import (
	"context"
	"database/sql"
	"fmt"
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
//
// Real account types: CHECKING, SAVINGS, WALLET, INVESTMENT.
// CREDIT_CARD is excluded.
//
// This is an operational reconstruction, not an auditable accounting close.
func BuildAccountClosingBalances(db *sql.DB, workspaceID string, periodEnd int64) ([]AccountClosingBalance, error) {
	ctx := context.Background()

	realTypes := []interface{}{"CHECKING", "SAVINGS", "WALLET", "INVESTMENT"}

	rows, err := db.QueryContext(ctx, `
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

	accounts := make(map[string]*AccountClosingBalance)
	var ordered []*AccountClosingBalance
	for rows.Next() {
		var acc AccountClosingBalance
		var archived int
		if err := rows.Scan(&acc.ID, &acc.Name, &acc.Type, &acc.Balance, &acc.Color, &acc.Icon, &archived); err != nil {
			return nil, fmt.Errorf("scan account closing: %w", err)
		}
		acc.IsArchived = archived == 1
		accounts[acc.ID] = &acc
		ordered = append(ordered, &acc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows accounts closing: %w", err)
	}

	if len(accounts) == 0 {
		return nil, nil
	}

	txRows, err := db.QueryContext(ctx, `
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
				acc.Balance -= amount
			}
			continue
		}

		// EXPENSE on real account: current was decreased by amount. Reverse: add back.
		if txType == "EXPENSE" {
			if acc, ok := accounts[accountID]; ok {
				acc.Balance += amount
			}
			continue
		}

		// TRANSFER: source account decreased, destination account increased.
		if txType == "TRANSFER" {
			// Reverse source effect: add amount back to source.
			if acc, ok := accounts[accountID]; ok {
				acc.Balance += amount
			}
			// Reverse destination effect: subtract amount from destination.
			if destID.Valid {
				if acc, ok := accounts[destID.String]; ok {
					acc.Balance -= amount
				}
			}
		}
	}
	if err := txRows.Err(); err != nil {
		return nil, fmt.Errorf("rows post-period tx: %w", err)
	}

	var result []AccountClosingBalance
	for _, acc := range ordered {
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

	return result, nil
}
