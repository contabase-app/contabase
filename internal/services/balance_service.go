package services

import (
	"database/sql"
	"fmt"

	"github.com/contabase-app/contabase/internal/models"
)

func ApplyBalanceEffect(tx *sql.Tx, workspaceID, trType, accType, paymentStatus string, amount int64, accountID, destAccountID string, now int64) error {
	if paymentStatus != models.PaymentStatusPaid {
		return nil
	}

	switch trType {
	case models.TransactionTypeExpense:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return execOneTx(tx, `UPDATE accounts SET current_balance = current_balance - ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, accountID, workspaceID)
	case models.TransactionTypeIncome:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return execOneTx(tx, `UPDATE accounts SET current_balance = current_balance + ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, accountID, workspaceID)
	case models.TransactionTypeTransfer:
		if err := execOneTx(tx, `UPDATE accounts SET current_balance = current_balance - ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, accountID, workspaceID); err != nil {
			return err
		}
		if destAccountID != "" {
			return execOneTx(tx, `UPDATE accounts SET current_balance = current_balance + ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, destAccountID, workspaceID)
		}
	}
	return nil
}

func ReverseBalanceEffect(tx *sql.Tx, workspaceID, trType, accType string, amount int64, accountID, destAccountID string, now int64) error {
	switch trType {
	case models.TransactionTypeExpense:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return execOneTx(tx, `UPDATE accounts SET current_balance = current_balance + ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, accountID, workspaceID)
	case models.TransactionTypeIncome:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return execOneTx(tx, `UPDATE accounts SET current_balance = current_balance - ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, accountID, workspaceID)
	case models.TransactionTypeTransfer:
		if err := execOneTx(tx, `UPDATE accounts SET current_balance = current_balance + ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, accountID, workspaceID); err != nil {
			return err
		}
		if destAccountID != "" {
			return execOneTx(tx, `UPDATE accounts SET current_balance = current_balance - ?, updated_at = ? WHERE id = ? AND workspace_id = ?`, amount, now, destAccountID, workspaceID)
		}
	}
	return nil
}

func execOneTx(tx *sql.Tx, query string, args ...interface{}) error {
	res, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("operação não autorizada ou registro não encontrado")
	}
	return nil
}
