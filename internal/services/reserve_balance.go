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

var ErrBalanceAggregateOutOfRange = errors.New("agregado de saldo fora do intervalo int64")

// AvailableBalanceStatus separates a fully validated composition from an
// indeterminate one. Numeric fields are diagnostic only when the status is
// AvailableBalanceUnavailable.
type AvailableBalanceStatus string

const (
	AvailableBalanceReliable    AvailableBalanceStatus = "RELIABLE"
	AvailableBalanceUnavailable AvailableBalanceStatus = "UNAVAILABLE"
)

type AvailableBalanceReason struct {
	Code    string
	Message string
}

type WorkspaceObligationsF1 struct {
	Amount int64
	Due30  int64
	Due60  int64
	// DueBeyond60 and DueWithoutDate are internal reconciliation amounts.
	// They are not presentation fields and must never be rendered as totals.
	DueBeyond60         int64
	DueWithoutDate      int64
	DueWithoutDateCount int64
	ScheduleAvailable   bool
	Reasons             []AvailableBalanceReason
}

// WorkspaceObligationsProbe runs inside the same read transaction that loaded
// the real balance. It must not write.
type WorkspaceObligationsProbe func(context.Context, *sql.Tx, string) (WorkspaceObligationsF1, error)

// WorkspaceFinancialPositionF1 is the dashboard financial position after
// Reservas removal: only real account balances and obligations remain.
type WorkspaceFinancialPositionF1 struct {
	Status                       AvailableBalanceStatus
	RealBalance                  int64
	Obligations                  int64
	ObligationsDue30             int64
	ObligationsDue60             int64
	ObligationsWithoutDateCount  int64
	ObligationsScheduleAvailable bool
	Reasons                      []AvailableBalanceReason
}

func (b WorkspaceFinancialPositionF1) IsReliable() bool {
	return b.Status == AvailableBalanceReliable
}

func (b WorkspaceFinancialPositionF1) ReasonText() string {
	seen := make(map[string]bool, len(b.Reasons))
	parts := make([]string, 0, len(b.Reasons))
	for _, reason := range b.Reasons {
		text := strings.TrimSpace(reason.Message)
		if text == "" {
			text = strings.TrimSpace(reason.Code)
		}
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ")
}

type f1AvailableReasons struct {
	seen  map[string]bool
	items []AvailableBalanceReason
}

func newF1AvailableReasons() *f1AvailableReasons {
	return &f1AvailableReasons{seen: make(map[string]bool)}
}

func (r *f1AvailableReasons) add(code, message string) {
	if r == nil || r.seen[code] {
		return
	}
	r.seen[code] = true
	r.items = append(r.items, AvailableBalanceReason{Code: code, Message: message})
}

func (r *f1AvailableReasons) len() int {
	if r == nil {
		return 0
	}
	return len(r.items)
}

func (r *f1AvailableReasons) list() []AvailableBalanceReason {
	if r == nil {
		return nil
	}
	return append([]AvailableBalanceReason(nil), r.items...)
}

// CalculateWorkspaceFinancialPositionF1 computes the real balance and the
// independent obligations total from a single read snapshot.
func CalculateWorkspaceFinancialPositionF1(db *sql.DB, workspaceID string, probe WorkspaceObligationsProbe) (WorkspaceFinancialPositionF1, error) {
	var position WorkspaceFinancialPositionF1
	if db == nil {
		return position, fmt.Errorf("db obrigatório")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return position, fmt.Errorf("workspace obrigatório")
	}
	if probe == nil {
		return position, fmt.Errorf("probe de obrigações obrigatório")
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return position, fmt.Errorf("abrir snapshot F1: %w", err)
	}
	defer tx.Rollback()
	return calculateWorkspaceFinancialPositionF1Tx(ctx, tx, workspaceID, probe)
}

// CalculateWorkspaceFinancialPositionF1Tx reuses the caller's read snapshot.
func CalculateWorkspaceFinancialPositionF1Tx(ctx context.Context, tx *sql.Tx, workspaceID string, probe WorkspaceObligationsProbe) (WorkspaceFinancialPositionF1, error) {
	return calculateWorkspaceFinancialPositionF1Tx(ctx, tx, workspaceID, probe)
}

func calculateWorkspaceFinancialPositionF1Tx(ctx context.Context, tx *sql.Tx, workspaceID string, probe WorkspaceObligationsProbe) (WorkspaceFinancialPositionF1, error) {
	result := WorkspaceFinancialPositionF1{Status: AvailableBalanceReliable}
	reasons := newF1AvailableReasons()

	real, err := sumF1RealBalance(ctx, tx, workspaceID)
	if err != nil {
		return result, fmt.Errorf("calcular saldo real F1: %w", err)
	}
	if !real.IsInt64() {
		reasons.add("real_overflow", "o saldo total excede o limite representável")
	} else {
		result.RealBalance = real.Int64()
	}

	obligations, err := probe(ctx, tx, workspaceID)
	if err != nil {
		return result, fmt.Errorf("calcular obrigações F1: %w", err)
	}
	if obligations.Amount < 0 {
		reasons.add("obligations_negative", "as obrigações reconhecidas têm valor inválido")
	} else {
		result.Obligations = obligations.Amount
	}
	for _, reason := range obligations.Reasons {
		reasons.add(reason.Code, reason.Message)
	}
	result.ObligationsDue30 = obligations.Due30
	result.ObligationsDue60 = obligations.Due60
	result.ObligationsWithoutDateCount = obligations.DueWithoutDateCount
	result.ObligationsScheduleAvailable = obligations.ScheduleAvailable

	if reasons.len() > 0 {
		result.Status = AvailableBalanceUnavailable
		result.Reasons = reasons.list()
		return result, nil
	}
	result.Reasons = reasons.list()
	return result, nil
}

func sumF1RealBalance(ctx context.Context, tx *sql.Tx, workspaceID string) (*big.Int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT current_balance
		FROM accounts
		WHERE workspace_id = ?
		  AND type != ?
		  AND archived_at IS NULL
	`, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	total := new(big.Int)
	value := new(big.Int)
	for rows.Next() {
		var amount int64
		if err := rows.Scan(&amount); err != nil {
			return nil, err
		}
		total.Add(total, value.SetInt64(amount))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return total, nil
}
