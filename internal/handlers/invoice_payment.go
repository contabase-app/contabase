package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

const invoicePaymentDescriptionPrefix = "Pagamento de Fatura - "

func invoicePaymentDescription(cardName string) string {
	return invoicePaymentDescriptionPrefix + cardName
}

func excludeInvoicePaymentCompetenceClause(alias string) string {
	prefix := "transactions."
	if alias != "" {
		prefix = alias + "."
	}
	return "NOT EXISTS (" +
		"SELECT 1 FROM invoice_payments ip " +
		"WHERE ip.workspace_id = " + prefix + "workspace_id " +
		"AND (ip.transaction_id = " + prefix + "id " +
		"OR ip.reversal_transaction_id = " + prefix + "id))"
}

var (
	errInvoicePaymentReversalNotFound     = errors.New("invoice payment not found")
	errInvoicePaymentAlreadyReversed      = errors.New("invoice payment already reversed")
	errInvoicePaymentReversalInvalidInput = errors.New("invalid invoice payment reversal input")
	errInvoicePaymentReversalStructural   = errors.New("inconsistent invoice payment financial link")
	errInvoicePaymentReversalLinkConflict = errors.New("invoice payment reversal link conflict")
)

const invoicePaymentReversalDescription = "Reversão de pagamento de fatura"

type invoicePaymentReversalCommand struct {
	WorkspaceID       string
	InvoicePaymentID  string
	ActorID           string
	Reason            string
	ReversedAt        int64
	ReversedDateCivil string
}

type invoicePaymentReversalResult struct {
	InvoicePaymentID      string
	InvoiceID             string
	OriginalTransactionID string
	ReversalTransactionID string
	AccountID             string
	AmountCents           int64
	ActivePaymentSum      int64
	InvoiceStatus         string
	InvoicePaidAt         *int64
}

type activeInvoicePaymentTimeline struct {
	ActiveSum       int64
	ThresholdPaidAt *int64
}

func loadActiveInvoicePaymentTimelineTx(tx *sql.Tx, workspaceID, invoiceID string, invoiceTotal int64) (activeInvoicePaymentTimeline, error) {
	var timeline activeInvoicePaymentTimeline
	rows, err := tx.Query(`
		SELECT amount_cents, paid_at
		FROM invoice_payments
		WHERE workspace_id = ?
		  AND invoice_id = ?
		  AND reversed_at IS NULL
		ORDER BY paid_at ASC, created_at ASC, id ASC
	`, workspaceID, invoiceID)
	if err != nil {
		return timeline, err
	}
	defer rows.Close()
	for rows.Next() {
		var amount, paidAt int64
		if err := rows.Scan(&amount, &paidAt); err != nil {
			return timeline, err
		}
		timeline.ActiveSum += amount
		thresholdReached := (invoiceTotal > 0 && timeline.ActiveSum >= invoiceTotal) || (invoiceTotal <= 0 && timeline.ActiveSum > 0)
		if timeline.ThresholdPaidAt == nil && thresholdReached {
			value := paidAt
			timeline.ThresholdPaidAt = &value
		}
	}
	if err := rows.Err(); err != nil {
		return timeline, err
	}
	return timeline, nil
}

// invoicePaymentReversalTestHooks are per-call and nil in production. They
// allow rollback proofs at each financial write boundary without global state.
type invoicePaymentReversalTestHooks struct {
	afterCAS                func(*sql.Tx) error
	afterCompensationInsert func(*sql.Tx) error
	afterBalanceEffect      func(*sql.Tx) error
	afterReversalLink       func(*sql.Tx) error
	afterInvoiceUpdate      func(*sql.Tx) error
}

func reverseInvoicePayment(db *sql.DB, command invoicePaymentReversalCommand) (invoicePaymentReversalResult, error) {
	return reverseInvoicePaymentWithHooks(db, command, nil)
}

func reverseInvoicePaymentWithHooks(db *sql.DB, command invoicePaymentReversalCommand, hooks *invoicePaymentReversalTestHooks) (invoicePaymentReversalResult, error) {
	var result invoicePaymentReversalResult
	command.WorkspaceID = strings.TrimSpace(command.WorkspaceID)
	command.InvoicePaymentID = strings.TrimSpace(command.InvoicePaymentID)
	command.ActorID = strings.TrimSpace(command.ActorID)
	command.Reason = strings.TrimSpace(command.Reason)
	command.ReversedDateCivil = strings.TrimSpace(command.ReversedDateCivil)
	if command.WorkspaceID == "" || command.InvoicePaymentID == "" || command.ActorID == "" || command.Reason == "" || command.ReversedAt <= 0 {
		return result, errInvoicePaymentReversalInvalidInput
	}
	reversalDate, err := ParseCivilDateInput(command.ReversedDateCivil)
	if err != nil {
		return result, fmt.Errorf("%w: reversed_date_civil: %v", errInvoicePaymentReversalInvalidInput, err)
	}
	command.ReversedDateCivil = FormatCivilDate(reversalDate)

	tx, err := db.Begin()
	if err != nil {
		return result, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var originalTransactionID sql.NullString
	var originalPaidAt int64
	err = tx.QueryRow(`
		UPDATE invoice_payments
		SET reversed_at = ?,
		    reversed_date_civil = ?,
		    reversed_by = ?,
		    reversal_reason = ?
		WHERE id = ?
		  AND workspace_id = ?
		  AND reversed_at IS NULL
		RETURNING invoice_id, account_id, transaction_id, amount_cents, paid_at
	`, command.ReversedAt, command.ReversedDateCivil, command.ActorID, command.Reason, command.InvoicePaymentID, command.WorkspaceID).Scan(
		&result.InvoiceID,
		&result.AccountID,
		&originalTransactionID,
		&result.AmountCents,
		&originalPaidAt,
	)
	if err == sql.ErrNoRows {
		var reversedAt sql.NullInt64
		lookupErr := tx.QueryRow(`
			SELECT reversed_at
			FROM invoice_payments
			WHERE id = ? AND workspace_id = ?
		`, command.InvoicePaymentID, command.WorkspaceID).Scan(&reversedAt)
		switch {
		case lookupErr == sql.ErrNoRows:
			return result, errInvoicePaymentReversalNotFound
		case lookupErr != nil:
			return result, lookupErr
		case reversedAt.Valid:
			return result, errInvoicePaymentAlreadyReversed
		default:
			return result, fmt.Errorf("%w: active payment was not claimed by CAS", errInvoicePaymentReversalStructural)
		}
	}
	if err != nil {
		return result, err
	}
	result.InvoicePaymentID = command.InvoicePaymentID
	if hooks != nil && hooks.afterCAS != nil {
		if err := hooks.afterCAS(tx); err != nil {
			return result, err
		}
	}

	if !originalTransactionID.Valid || strings.TrimSpace(originalTransactionID.String) == "" {
		return result, fmt.Errorf("%w: original transaction_id is absent", errInvoicePaymentReversalStructural)
	}
	result.OriginalTransactionID = originalTransactionID.String

	var (
		cardAccountID string
		closingUnix   int64
		closingCivil  sql.NullString
		cardWorkspace string
		cardType      string
		creditCardID  string
	)
	if err := tx.QueryRow(`
		SELECT i.account_id, i.closing_date, i.closing_date_civil,
		       a.workspace_id, a.type, cc.id
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		JOIN credit_cards cc ON cc.account_id = a.id
		WHERE i.id = ?
	`, result.InvoiceID).Scan(&cardAccountID, &closingUnix, &closingCivil, &cardWorkspace, &cardType, &creditCardID); err != nil {
		if err == sql.ErrNoRows {
			return result, fmt.Errorf("%w: invoice/card not found", errInvoicePaymentReversalStructural)
		}
		return result, err
	}
	if cardWorkspace != command.WorkspaceID || cardType != models.AccountTypeCreditCard || creditCardID == "" {
		return result, fmt.Errorf("%w: invoice/card workspace or type mismatch", errInvoicePaymentReversalStructural)
	}

	var paymentAccountWorkspace, paymentAccountType string
	if err := tx.QueryRow(`
		SELECT workspace_id, type
		FROM accounts
		WHERE id = ?
	`, result.AccountID).Scan(&paymentAccountWorkspace, &paymentAccountType); err != nil {
		if err == sql.ErrNoRows {
			return result, fmt.Errorf("%w: payment account not found", errInvoicePaymentReversalStructural)
		}
		return result, err
	}
	if paymentAccountWorkspace != command.WorkspaceID || (paymentAccountType != models.AccountTypeChecking && paymentAccountType != models.AccountTypeSavings) {
		return result, fmt.Errorf("%w: payment account workspace or type mismatch", errInvoicePaymentReversalStructural)
	}

	var (
		originalWorkspace string
		originalAccountID string
		originalType      string
		originalAmount    int64
		originalStatus    string
		originalDest      sql.NullString
	)
	if err := tx.QueryRow(`
		SELECT workspace_id, account_id, type, amount, status, destination_account_id
		FROM transactions
		WHERE id = ?
	`, result.OriginalTransactionID).Scan(
		&originalWorkspace,
		&originalAccountID,
		&originalType,
		&originalAmount,
		&originalStatus,
		&originalDest,
	); err != nil {
		if err == sql.ErrNoRows {
			return result, fmt.Errorf("%w: original transaction not found", errInvoicePaymentReversalStructural)
		}
		return result, err
	}
	if originalWorkspace != command.WorkspaceID || originalAccountID != result.AccountID || originalAmount != result.AmountCents ||
		originalType != models.TransactionTypeExpense || originalStatus != models.PaymentStatusPaid ||
		(originalDest.Valid && strings.TrimSpace(originalDest.String) != "") {
		return result, fmt.Errorf("%w: original transaction fields mismatch", errInvoicePaymentReversalStructural)
	}

	var actorExists int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM users WHERE id = ?`, command.ActorID).Scan(&actorExists); err != nil {
		return result, err
	}
	if actorExists != 1 {
		return result, fmt.Errorf("%w: actor not found", errInvoicePaymentReversalStructural)
	}

	result.ReversalTransactionID = uuid.NewString()
	transactionDate := CivilDateToLegacyUnixNoonUTC(reversalDate)
	if _, err := tx.Exec(`
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id,
			type, amount, date, date_civil, description, status,
			paid_at, paid_date_civil, installment_number, total_installments,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, 'INCOME', ?, ?, ?, ?, 'paid', ?, ?, 1, 1, ?, ?)
	`, result.ReversalTransactionID, command.WorkspaceID, command.ActorID, result.AccountID,
		result.AmountCents, transactionDate, command.ReversedDateCivil, invoicePaymentReversalDescription,
		command.ReversedAt, command.ReversedDateCivil, command.ReversedAt, command.ReversedAt); err != nil {
		return result, err
	}
	if hooks != nil && hooks.afterCompensationInsert != nil {
		if err := hooks.afterCompensationInsert(tx); err != nil {
			return result, err
		}
	}

	if err := services.ApplyBalanceEffect(
		tx,
		command.WorkspaceID,
		models.TransactionTypeIncome,
		paymentAccountType,
		models.PaymentStatusPaid,
		result.AmountCents,
		result.AccountID,
		"",
		command.ReversedAt,
	); err != nil {
		return result, err
	}
	if hooks != nil && hooks.afterBalanceEffect != nil {
		if err := hooks.afterBalanceEffect(tx); err != nil {
			return result, err
		}
	}

	linkResult, err := tx.Exec(`
		UPDATE invoice_payments
		SET reversal_transaction_id = ?
		WHERE id = ?
		  AND workspace_id = ?
		  AND reversed_at IS NOT NULL
		  AND reversal_transaction_id IS NULL
	`, result.ReversalTransactionID, command.InvoicePaymentID, command.WorkspaceID)
	if err != nil {
		return result, err
	}
	linked, err := linkResult.RowsAffected()
	if err != nil {
		return result, err
	}
	if linked != 1 {
		return result, fmt.Errorf("%w: affected %d rows", errInvoicePaymentReversalLinkConflict, linked)
	}
	if hooks != nil && hooks.afterReversalLink != nil {
		if err := hooks.afterReversalLink(tx); err != nil {
			return result, err
		}
	}

	activePaymentSum, invoiceStatus, invoicePaidAt, err := reconcileInvoiceAfterPaymentReversalTx(
		tx,
		command.WorkspaceID,
		result.InvoiceID,
		cardAccountID,
		closingUnix,
		closingCivil,
		reversalDate,
	)
	if err != nil {
		return result, err
	}
	result.ActivePaymentSum = activePaymentSum
	result.InvoiceStatus = invoiceStatus
	result.InvoicePaidAt = invoicePaidAt
	if hooks != nil && hooks.afterInvoiceUpdate != nil {
		if err := hooks.afterInvoiceUpdate(tx); err != nil {
			return result, err
		}
	}

	if err := tx.Commit(); err != nil {
		return result, err
	}
	committed = true
	return result, nil
}

func reconcileInvoiceAfterPaymentReversalTx(
	tx *sql.Tx,
	workspaceID, invoiceID, cardAccountID string,
	closingUnix int64,
	closingCivil sql.NullString,
	reversalDate CivilDate,
) (int64, string, *int64, error) {
	var invoiceTotal int64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(t.amount), 0)
		FROM transactions t
		WHERE t.workspace_id = ?
		  AND t.invoice_id = ?
		  AND t.type = 'EXPENSE'
	`, workspaceID, invoiceID).Scan(&invoiceTotal); err != nil {
		return 0, "", nil, err
	}

	timeline, err := loadActiveInvoicePaymentTimelineTx(tx, workspaceID, invoiceID, invoiceTotal)
	if err != nil {
		return 0, "", nil, err
	}
	activePaymentSum := timeline.ActiveSum
	thresholdPaidAt := timeline.ThresholdPaidAt

	paid := (invoiceTotal > 0 && activePaymentSum >= invoiceTotal) || (invoiceTotal <= 0 && activePaymentSum > 0)
	status := models.InvoiceStatusOpen
	if paid {
		status = models.InvoiceStatusPaid
	} else {
		closingReached, err := invoiceClosingReachedAtReversal(closingUnix, closingCivil, reversalDate)
		if err != nil {
			return 0, "", nil, err
		}
		if closingReached {
			status = models.InvoiceStatusClosed
		}
		thresholdPaidAt = nil
	}

	var paidAtValue interface{}
	if thresholdPaidAt != nil {
		paidAtValue = *thresholdPaidAt
	}
	updateResult, err := tx.Exec(`
		UPDATE invoices
		SET paid_amount = ?, status = ?, paid_at = ?
		WHERE id = ?
		  AND account_id = ?
		  AND EXISTS (
		      SELECT 1
		      FROM accounts a
		      WHERE a.id = invoices.account_id
		        AND a.workspace_id = ?
		        AND a.type = 'CREDIT_CARD'
		  )
	`, activePaymentSum, status, paidAtValue, invoiceID, cardAccountID, workspaceID)
	if err != nil {
		return 0, "", nil, err
	}
	updated, err := updateResult.RowsAffected()
	if err != nil {
		return 0, "", nil, err
	}
	if updated != 1 {
		return 0, "", nil, fmt.Errorf("%w: invoice update affected %d rows", errInvoicePaymentReversalStructural, updated)
	}
	return activePaymentSum, status, thresholdPaidAt, nil
}

func invoiceClosingReachedAtReversal(closingUnix int64, closingCivil sql.NullString, reversalDate CivilDate) (bool, error) {
	if closingCivil.Valid && strings.TrimSpace(closingCivil.String) != "" {
		date, err := ParseCivilDateInput(closingCivil.String)
		if err != nil {
			return false, fmt.Errorf("%w: invalid invoice closing_date_civil", errInvoicePaymentReversalStructural)
		}
		return CompareCivilDate(date, reversalDate) <= 0, nil
	}
	if closingUnix <= 0 {
		return false, nil
	}
	date, err := LegacyUnixToCivilDateInLocation(closingUnix, appTimeLocation())
	if err != nil {
		return false, fmt.Errorf("%w: invalid legacy invoice closing_date", errInvoicePaymentReversalStructural)
	}
	return CompareCivilDate(date, reversalDate) <= 0, nil
}

func QueryActiveInvoicePayments(db *sql.DB, workspaceID, invoiceID string) ([]models.InvoicePayment, error) {
	rows, err := db.Query(`
		SELECT id, workspace_id, invoice_id, account_id, transaction_id,
		       amount_cents, paid_at, note, source, reversed_at, created_by, created_at
		FROM invoice_payments
		WHERE workspace_id = ? AND invoice_id = ? AND reversed_at IS NULL
		ORDER BY paid_at DESC, created_at DESC
	`, workspaceID, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var payments []models.InvoicePayment
	for rows.Next() {
		var p models.InvoicePayment
		if err := rows.Scan(
			&p.ID, &p.WorkspaceID, &p.InvoiceID, &p.AccountID, &p.TransactionID,
			&p.AmountCents, &p.PaidAt, &p.Note, &p.Source, &p.ReversedAt, &p.CreatedBy, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		payments = append(payments, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if payments == nil {
		payments = []models.InvoicePayment{}
	}
	return payments, nil
}

func SumActiveInvoicePayments(db *sql.DB, workspaceID, invoiceID string) (int64, error) {
	var total int64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM invoice_payments
		WHERE workspace_id = ? AND invoice_id = ? AND reversed_at IS NULL
	`, workspaceID, invoiceID).Scan(&total)
	return total, err
}

func CalculateInvoicePendingAmount(invoiceTotalCents, totalPaidCents int64) int64 {
	pending := invoiceTotalCents - totalPaidCents
	if pending < 0 {
		return 0
	}
	return pending
}
