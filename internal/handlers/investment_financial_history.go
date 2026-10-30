package handlers

import (
	"database/sql"
	"fmt"
	"math/big"
)

// Investment adjustments participate in balance evolution, never in cash-flow
// income. Economic time keeps backdated corrections out of today's return.
// The existing trend path is preserved when no integration falls in the window.
func investmentAdjustmentTrend(db *sql.DB, workspace, account string, current, movementNet, start, end int64) (string, string, bool) {
	rows, err := db.Query(`SELECT h.delta_cents FROM investment_adjustment_economic_dates f
 JOIN account_balance_adjustments h ON h.workspace_id=f.workspace_id AND h.account_id=f.account_id AND h.id=f.adjustment_id
 JOIN accounts a ON a.workspace_id=f.workspace_id AND a.id=f.account_id
 WHERE f.workspace_id=? AND (?='' OR f.account_id=?) AND a.archived_at IS NULL AND a.type='INVESTMENT'
 AND f.economic_at>=? AND f.economic_at<?`, workspace, account, account, start, end)
	if err != nil {
		return "—", "flat", true
	}
	defer rows.Close()
	delta := big.NewInt(movementNet)
	found := false
	for rows.Next() {
		var amount int64
		if err = rows.Scan(&amount); err != nil {
			return "—", "flat", true
		}
		found = true
		delta.Add(delta, big.NewInt(amount))
	}
	if rows.Err() != nil {
		return "—", "flat", true
	}
	if !found {
		return "", "", false
	}
	previous := new(big.Int).Sub(big.NewInt(current), delta)
	if !previous.IsInt64() {
		return "—", "flat", true
	}
	direction := "flat"
	if delta.Sign() > 0 {
		direction = "up"
	} else if delta.Sign() < 0 {
		direction = "down"
	} else {
		return "0,0%", direction, true
	}
	base := new(big.Int).Abs(previous)
	if base.Cmp(big.NewInt(10)) < 0 {
		return "novo", direction, true
	}
	tenths := new(big.Int).Mul(new(big.Int).Abs(delta), big.NewInt(1000))
	tenths.Add(tenths, new(big.Int).Div(new(big.Int).Set(base), big.NewInt(2))).Div(tenths, base)
	if tenths.Cmp(big.NewInt(9990)) > 0 {
		if direction == "up" {
			return ">+999%", direction, true
		}
		return "<-999%", direction, true
	}
	sign := "+"
	if direction == "down" {
		sign = "-"
	}
	n := tenths.Int64()
	return fmt.Sprintf("%s%d,%d%%", sign, n/10, n%10), direction, true
}
