package services

import (
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/contabase-app/contabase/internal/models"
)

var ErrBalanceOutOfRange = errors.New("saldo resultante fora do intervalo int64")

func ApplyBalanceEffect(tx *sql.Tx, workspaceID, trType, accType, paymentStatus string, amount int64, accountID, destAccountID string, now int64, effectMode ...string) error {
	if cash, err := transactionHasFinancialEffect(effectMode); err != nil || !cash {
		return err
	}
	if paymentStatus != models.PaymentStatusPaid {
		return nil
	}

	switch trType {
	case models.TransactionTypeExpense:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return changeAccountBalanceTx(tx, workspaceID, accountID, amount, true, now)
	case models.TransactionTypeIncome:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return changeAccountBalanceTx(tx, workspaceID, accountID, amount, false, now)
	case models.TransactionTypeTransfer:
		if err := changeAccountBalanceTx(tx, workspaceID, accountID, amount, true, now); err != nil {
			return err
		}
		if destAccountID != "" {
			return changeAccountBalanceTx(tx, workspaceID, destAccountID, amount, false, now)
		}
	}
	return nil
}

func ReverseBalanceEffect(tx *sql.Tx, workspaceID, trType, accType string, amount int64, accountID, destAccountID string, now int64, effectMode ...string) error {
	if cash, err := transactionHasFinancialEffect(effectMode); err != nil || !cash {
		return err
	}
	switch trType {
	case models.TransactionTypeExpense:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return changeAccountBalanceTx(tx, workspaceID, accountID, amount, false, now)
	case models.TransactionTypeIncome:
		if accType == models.AccountTypeCreditCard {
			return nil
		}
		return changeAccountBalanceTx(tx, workspaceID, accountID, amount, true, now)
	case models.TransactionTypeTransfer:
		if err := changeAccountBalanceTx(tx, workspaceID, accountID, amount, false, now); err != nil {
			return err
		}
		if destAccountID != "" {
			return changeAccountBalanceTx(tx, workspaceID, destAccountID, amount, true, now)
		}
	}
	return nil
}

// Omitted mode preserves existing STANDARD callers. Readers of persisted
// transactions must pass their mode or reject classification mutations first.
func transactionHasFinancialEffect(mode []string) (bool, error) {
	if len(mode) == 0 || len(mode) == 1 && mode[0] == models.TransactionEffectStandard {
		return true, nil
	}
	if len(mode) == 1 && mode[0] == models.TransactionEffectClassificationOnly {
		return false, nil
	}
	return false, ErrInvestmentValidation
}

// The caller owns the transaction and must roll it back on any error, including
// failure on the second side of a transfer. The predicate guards the arithmetic
// in the same write, so SQLite cannot promote an overflowing integer to REAL.
func changeAccountBalanceTx(tx *sql.Tx, workspaceID, accountID string, amount int64, subtract bool, now int64) error {
	operator, comparison := "+", "<="
	bound := int64(math.MaxInt64)
	if subtract {
		operator = "-"
		if amount > 0 {
			comparison, bound = ">=", math.MinInt64+amount
		} else if amount < 0 {
			bound = math.MaxInt64 + amount
		}
	} else if amount > 0 {
		bound = math.MaxInt64 - amount
	} else if amount < 0 {
		comparison, bound = ">=", math.MinInt64-amount
	}
	// Only fixed operators are interpolated; values and scope stay parameterized.
	// None of the bound calculations negate amount (which can be MinInt64).
	query := `UPDATE accounts SET current_balance = current_balance ` + operator + ` ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND typeof(current_balance) = 'integer' AND current_balance ` + comparison + ` ?`
	res, err := tx.Exec(query, amount, now, accountID, workspaceID, bound)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	if affected != 0 {
		return fmt.Errorf("atualização de saldo afetou %d contas", affected)
	}
	var storage string
	if err := tx.QueryRow(`SELECT typeof(current_balance) FROM accounts WHERE id = ? AND workspace_id = ?`, accountID, workspaceID).Scan(&storage); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("operação não autorizada ou registro não encontrado")
		}
		return err
	}
	if storage != "integer" {
		return fmt.Errorf("saldo persistido não é INTEGER")
	}
	return ErrBalanceOutOfRange
}
