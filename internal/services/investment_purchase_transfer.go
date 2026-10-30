package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

// The schema-77 transaction journal is the receipt. Its primary key gives a
// purchase exactly one transfer, without a second balance ledger or migration.
// The prefix is reserved for this writer; generic transaction mutations reject
// these IDs. Historical record-only purchases never acquire this receipt.
const InvestmentPurchaseTransferPrefix = "investment-purchase:"

func createInvestmentPurchaseTransferTx(ctx context.Context, tx *sql.Tx, workspace, purchase, source, destination, user, date string, amount int64) error {
	if source == "" || source == destination || amount <= 0 {
		return ErrInvestmentInvalidAccount
	}
	var sourceType string
	err := tx.QueryRowContext(ctx, `SELECT type FROM accounts WHERE workspace_id=? AND id=? AND archived_at IS NULL AND type IN ('CHECKING','SAVINGS','WALLET')`, workspace, source).Scan(&sourceType)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentInvalidAccount
	}
	if err != nil {
		return err
	}
	var hits int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE workspace_id=? AND id=? AND archived_at IS NULL AND type='INVESTMENT'`, workspace, destination).Scan(&hits); err != nil {
		return err
	}
	if hits != 1 {
		return ErrInvestmentInvalidAccount
	}
	civil, err := civildate.ParseCivilDateInput(date)
	if err != nil {
		return ErrInvestmentValidation
	}
	at := civildate.CivilDateToLegacyUnixNoonUTC(civil)
	now := time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
 VALUES(?,?,?,?,?,'TRANSFER',?,?,?,?,?,'paid','Aplicação de investimento',?,?)`, InvestmentPurchaseTransferPrefix+purchase, workspace, user, source, destination, amount, at, date, at, date, now, now)
	if err != nil {
		return err
	}
	// Preserve the same policy as ordinary paid transfers (including permitted
	// negative account balances), overflow protection and both balance effects.
	return ApplyBalanceEffect(tx, workspace, models.TransactionTypeTransfer, sourceType, models.PaymentStatusPaid, amount, source, destination, now)
}

func verifyInvestmentPurchaseTransferTx(ctx context.Context, tx *sql.Tx, workspace, purchase, source, destination, user, date string, amount int64) error {
	var savedSource, savedDestination, savedUser, savedDate, typ, status, effect string
	var savedAmount int64
	err := tx.QueryRowContext(ctx, `SELECT account_id,COALESCE(destination_account_id,''),user_id,COALESCE(date_civil,''),amount,type,status,effect_mode FROM transactions WHERE workspace_id=? AND id=?`, workspace, InvestmentPurchaseTransferPrefix+purchase).Scan(&savedSource, &savedDestination, &savedUser, &savedDate, &savedAmount, &typ, &status, &effect)
	if errors.Is(err, sql.ErrNoRows) && source == "" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil || savedSource != source || destination != "" && savedDestination != destination || savedUser != user || savedDate != date || savedAmount != amount || typ != "TRANSFER" || status != "paid" || effect != models.TransactionEffectStandard {
		return ErrInvestmentConflict
	}
	return nil
}

func requireFundedInvestmentTx(ctx context.Context, tx *sql.Tx, workspace, investment string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investments i JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id AND p.is_initial=1
 JOIN transactions t ON t.workspace_id=p.workspace_id AND t.id=?||p.id AND t.destination_account_id=i.account_id AND t.amount=p.principal_cents AND t.date_civil=p.purchase_date AND t.type='TRANSFER' AND t.status='paid' AND t.effect_mode='STANDARD'
 WHERE i.workspace_id=? AND i.id=?`, InvestmentPurchaseTransferPrefix, workspace, investment).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: investimento histórico depende de homologação própria", ErrInvestmentConflict)
	}
	return rejectPendingInvestmentAdoptionTx(ctx, tx, workspace, investment)
}

// Funded inventory cannot use record-only withdrawals, lifecycle closure or
// position integration until those operations have their own homologation.
func rejectFundedInvestmentTx(ctx context.Context, tx *sql.Tx, workspace, investment string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_purchases p JOIN transactions t ON t.workspace_id=p.workspace_id AND t.id=?||p.id WHERE p.workspace_id=? AND p.investment_id=?`, InvestmentPurchaseTransferPrefix, workspace, investment).Scan(&n)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%w: operação pendente de homologação para aplicações financeiras", ErrInvestmentConflict)
	}
	return nil
}

// Read the journal in the same snapshot as purchases and manual positions.
type InvestmentPurchaseTransfer struct{ TransactionID, SourceAccountID, SourceAccountName string }

func readInvestmentPurchaseTransfersTx(ctx context.Context, tx *sql.Tx, workspace, investment, destination string) (map[string]InvestmentPurchaseTransfer, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,t.id,t.account_id,a.name,t.destination_account_id,t.amount,t.date_civil,t.type,t.status,t.effect_mode,p.principal_cents,p.purchase_date
 FROM investment_purchases p JOIN transactions t ON t.workspace_id=p.workspace_id AND t.id=?||p.id
 JOIN accounts a ON a.workspace_id=t.workspace_id AND a.id=t.account_id
 WHERE p.workspace_id=? AND p.investment_id=? ORDER BY p.purchase_date,p.id`, InvestmentPurchaseTransferPrefix, workspace, investment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result map[string]InvestmentPurchaseTransfer
	for rows.Next() {
		var id, dest, date, typ, status, effect, purchaseDate string
		var amount, principal int64
		var receipt InvestmentPurchaseTransfer
		if err := rows.Scan(&id, &receipt.TransactionID, &receipt.SourceAccountID, &receipt.SourceAccountName, &dest, &amount, &date, &typ, &status, &effect, &principal, &purchaseDate); err != nil {
			return nil, err
		}
		if dest != destination || amount != principal || date != purchaseDate || typ != "TRANSFER" || status != "paid" || effect != models.TransactionEffectStandard {
			return nil, ErrInvestmentConflict
		}
		if result == nil {
			result = make(map[string]InvestmentPurchaseTransfer)
		}
		result[id] = receipt
	}
	return result, rows.Err()
}
