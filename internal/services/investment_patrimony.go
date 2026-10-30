package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
)

const (
	InvestmentPatrimonyPartial     = "PARTIAL"
	InvestmentPatrimonyUnavailable = "UNAVAILABLE"
)

// InvestmentPatrimonyPosition retains the observed value even when it cannot
// enter today's subtotal. A missing position is nil, never original principal.
type InvestmentPatrimonyPosition struct {
	Investment models.Investment
	Position   *models.InvestmentValuation
	Eligible   bool
	Reason     string
}

type InvestmentAccountPatrimony struct {
	AccountID, AccountName, WorkspaceID string
	Archived, HasMaintainedInvestments  bool
	Investments                         []InvestmentPatrimonyPosition
	SubtotalCents                       int64
	EligibleCount, WithoutValidPosition int
	Coverage                            string
}

// Coverage is always partial or unavailable: broker cash is not reconciled by
// this projection, even when every maintained application has a valid position.
type WorkspaceInvestmentPatrimony struct {
	Accounts                            []InvestmentAccountPatrimony
	SubtotalCents                       int64
	EligibleCount, WithoutValidPosition int
	ArchivedMaintainedAccountCount      int
	Coverage                            string
}

func ReadWorkspaceInvestmentPatrimony(ctx context.Context, db *sql.DB, workspaceID string) (WorkspaceInvestmentPatrimony, error) {
	return readInvestmentPatrimony(ctx, db, workspaceID, "")
}

func ReadAccountInvestmentPatrimony(ctx context.Context, db *sql.DB, workspaceID, accountID string) (InvestmentAccountPatrimony, error) {
	if strings.TrimSpace(accountID) == "" {
		return InvestmentAccountPatrimony{}, ErrInvestmentNotFound
	}
	result, err := readInvestmentPatrimony(ctx, db, workspaceID, accountID)
	if err != nil {
		return InvestmentAccountPatrimony{}, err
	}
	if len(result.Accounts) != 1 {
		return InvestmentAccountPatrimony{}, ErrInvestmentNotFound
	}
	return result.Accounts[0], nil
}

// Only SELECTs run here, on the ordinary reader pool. All related reads share
// one transaction; no dashboard/runtime helper or writer service is invoked.
func readInvestmentPatrimony(ctx context.Context, db *sql.DB, workspaceID, accountID string) (WorkspaceInvestmentPatrimony, error) {
	result := WorkspaceInvestmentPatrimony{Coverage: InvestmentPatrimonyUnavailable}
	if db == nil || strings.TrimSpace(workspaceID) == "" {
		return result, fmt.Errorf("workspace e banco obrigatórios")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, name, archived_at IS NOT NULL FROM accounts
		WHERE workspace_id = ? AND type = 'INVESTMENT' AND (? = '' OR id = ?) ORDER BY name, id`, workspaceID, accountID, accountID)
	if err != nil {
		return result, err
	}
	indexes := make(map[string]int)
	for rows.Next() {
		account := InvestmentAccountPatrimony{WorkspaceID: workspaceID, Coverage: InvestmentPatrimonyUnavailable}
		if err := rows.Scan(&account.AccountID, &account.AccountName, &account.Archived); err != nil {
			rows.Close()
			return result, err
		}
		indexes[account.AccountID] = len(result.Accounts)
		result.Accounts = append(result.Accounts, account)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT `+investmentColumns+` FROM investments
		WHERE workspace_id = ? AND account_id IN (
			SELECT id FROM accounts WHERE workspace_id = ? AND type = 'INVESTMENT' AND (? = '' OR id = ?)
		) ORDER BY account_id, name, id`, workspaceID, workspaceID, accountID, accountID)
	if err != nil {
		return result, err
	}
	var investments []models.Investment
	for rows.Next() {
		inv, err := scanInvestment(rows.Scan)
		if err != nil {
			rows.Close()
			return result, err
		}
		investments = append(investments, inv)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for _, inv := range investments {
		index, ok := indexes[inv.AccountID]
		if !ok {
			return result, ErrInvestmentNotFound
		}
		position, maintained, err := readInvestmentPatrimonyPosition(ctx, tx, inv)
		if err != nil {
			return WorkspaceInvestmentPatrimony{}, err
		}
		account := &result.Accounts[index]
		account.HasMaintainedInvestments = account.HasMaintainedInvestments || maintained
		account.Investments = append(account.Investments, position)
		if !position.Eligible {
			// Administrative closures remain visible with an explicit reason,
			// but are not applications maintained in the current portfolio.
			if inv.Status == models.InvestmentStatusAtiva && inv.ArchivedAt == nil {
				account.WithoutValidPosition++
				result.WithoutValidPosition++
			}
			continue
		}
		amount := position.Position.AmountCents
		if amount < 0 || account.SubtotalCents > math.MaxInt64-amount || result.SubtotalCents > math.MaxInt64-amount {
			return WorkspaceInvestmentPatrimony{}, ErrBalanceAggregateOutOfRange
		}
		account.SubtotalCents += amount
		result.SubtotalCents += amount
		account.EligibleCount++
		result.EligibleCount++
		account.Coverage = InvestmentPatrimonyPartial
		result.Coverage = InvestmentPatrimonyPartial
	}
	for _, account := range result.Accounts {
		if account.Archived && account.HasMaintainedInvestments {
			result.ArchivedMaintainedAccountCount++
		}
	}
	if err := tx.Commit(); err != nil {
		return WorkspaceInvestmentPatrimony{}, err
	}
	return result, nil
}

func readInvestmentPatrimonyPosition(ctx context.Context, tx *sql.Tx, inv models.Investment) (InvestmentPatrimonyPosition, bool, error) {
	item := InvestmentPatrimonyPosition{Investment: inv}
	valuation, err := scanValuation(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT id, workspace_id, investment_id, valuation_date, amount_cents,
			created_by, created_at, updated_by, updated_at FROM investment_valuations
			WHERE workspace_id = ? AND investment_id = ? ORDER BY valuation_date DESC, id DESC LIMIT 1`, inv.WorkspaceID, inv.ID).Scan(dest...)
	})
	var valuations []models.InvestmentValuation
	if err == nil {
		item.Position = &valuation
		valuations = append(valuations, valuation)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return item, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+purchaseColumns+` FROM investment_purchases
		WHERE workspace_id = ? AND investment_id = ? ORDER BY purchase_date, is_initial DESC, id`, inv.WorkspaceID, inv.ID)
	if err != nil {
		return item, false, err
	}
	var purchases []models.InvestmentPurchase
	for rows.Next() {
		purchase, err := scanInvestmentPurchase(rows.Scan)
		if err != nil {
			rows.Close()
			return item, false, err
		}
		purchases = append(purchases, purchase)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return item, false, err
	}
	detail, err := BuildInvestmentPurchaseDetail(inv, valuations, purchases)
	if err != nil {
		return item, false, err
	}
	history, err := readInvestmentWithdrawals(ctx, tx, inv.WorkspaceID, inv.ID)
	if err != nil {
		return item, false, err
	}
	if err := enrichInvestmentWithdrawals(&detail, history); err != nil {
		return item, false, err
	}
	maintained := inv.Status == models.InvestmentStatusAtiva && inv.ArchivedAt == nil && detail.MaintainedPurchaseCount > 0
	switch {
	case inv.Status != models.InvestmentStatusAtiva || inv.ArchivedAt != nil:
		item.Reason = "Acompanhamento encerrado; recebimento financeiro não comprovado"
	case item.Position == nil:
		item.Reason = "Sem posição registrada"
	case detail.PositionStale:
		item.Reason = "Posição desatualizada: há compra ou retirada posterior"
	case !maintained:
		item.Reason = "Compras integralmente retiradas; posição remanescente não comprovada"
	default:
		// Civil dates cannot establish the order of an operation and a position
		// on the same day. Preserve I2.3's intraday ambiguity without changing it.
		for _, purchase := range purchases {
			if purchase.PurchaseDate == item.Position.ValuationDate {
				item.Reason = "Existem operações na mesma data de uma posição"
			}
		}
		for _, withdrawal := range history {
			if withdrawal.WithdrawalDate == item.Position.ValuationDate {
				item.Reason = "Existem operações na mesma data de uma posição"
			}
		}
		item.Eligible = item.Reason == ""
	}
	return item, maintained, nil
}
