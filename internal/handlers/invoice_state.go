package handlers

import (
	"database/sql"
	"strconv"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

const invoiceCycleSoonDays = 7

type InvoiceCycleState string

const (
	InvoiceCycleOpen      InvoiceCycleState = "open"
	InvoiceCycleSoon     InvoiceCycleState = "soon"
	InvoiceCycleClosed   InvoiceCycleState = "closed"
	InvoiceCycleUnknown  InvoiceCycleState = "unknown"
)

type InvoiceFinancialState string

const (
	InvoiceFinancialPending InvoiceFinancialState = "pending"
	InvoiceFinancialPartial  InvoiceFinancialState = "partial"
	InvoiceFinancialPaid     InvoiceFinancialState = "paid"
	InvoiceFinancialEmpty   InvoiceFinancialState = "empty"
)

type InvoiceComputedState struct {
	Total       int64
	PaidActive  int64
	Pending     int64
	Cycle       InvoiceCycleState
	Financial   InvoiceFinancialState
	CycleDays   int
	CycleLabel  string
	FinLabel    string
}

func sumActiveInvoicePaymentsDB(db *sql.DB, workspaceID, invoiceID string) (int64, error) {
	var total int64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM invoice_payments
		WHERE workspace_id = ? AND invoice_id = ? AND reversed_at IS NULL
	`, workspaceID, invoiceID).Scan(&total)
	return total, err
}

func computeInvoiceState(total, paidActive int64, closingUnix int64, now time.Time) InvoiceComputedState {
	pending := total - paidActive
	if pending < 0 {
		pending = 0
	}

	st := InvoiceComputedState{
		Total:      total,
		PaidActive: paidActive,
		Pending:    pending,
	}

	switch {
	case total <= 0:
		st.Financial = InvoiceFinancialEmpty
		st.FinLabel = "Vazia"
	case paidActive <= 0 && pending > 0:
		st.Financial = InvoiceFinancialPending
		st.FinLabel = "Pendente"
	case paidActive > 0 && pending > 0:
		st.Financial = InvoiceFinancialPartial
		st.FinLabel = "Parcial"
	default:
		st.Financial = InvoiceFinancialPaid
		st.FinLabel = "Paga"
	}

	if closingUnix > 0 {
		closeAt := time.Unix(closingUnix, 0).UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		closeDay := time.Date(closeAt.Year(), closeAt.Month(), closeAt.Day(), 0, 0, 0, 0, time.UTC)
		days := int(closeDay.Sub(today).Hours() / 24)
		switch {
		case days > invoiceCycleSoonDays:
			st.Cycle = InvoiceCycleOpen
			st.CycleDays = days
			st.CycleLabel = "Aberta"
		case days > 0:
			st.Cycle = InvoiceCycleSoon
			st.CycleDays = days
			st.CycleLabel = "Fecha em " + strconv.Itoa(days) + " dia" + daySuffixPT(days)
		default:
			st.Cycle = InvoiceCycleClosed
			st.CycleDays = 0
			st.CycleLabel = "Fechada"
		}
	} else {
		st.Cycle = InvoiceCycleUnknown
		st.CycleLabel = ""
	}

	return st
}

func daySuffixPT(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func computeCycleBadge(closingUnix int64, now time.Time) string {
	if closingUnix <= 0 {
		return ""
	}
	closeAt := time.Unix(closingUnix, 0).UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	closeDay := time.Date(closeAt.Year(), closeAt.Month(), closeAt.Day(), 0, 0, 0, 0, time.UTC)
	days := int(closeDay.Sub(today).Hours() / 24)
	switch {
	case days > invoiceCycleSoonDays:
		return "Aberta"
	case days > 0:
		return "Fecha em " + strconv.Itoa(days) + " dia" + daySuffixPT(days)
	default:
		return "Fechada"
	}
}

func computeFinancialBadge(pendingCents int64, paidActive int64) string {
	if pendingCents < 0 {
		pendingCents = 0
	}
	switch {
	case paidActive <= 0 && pendingCents <= 0:
		return "Vazia"
	case paidActive <= 0 && pendingCents > 0:
		return "Pendente"
	case paidActive > 0 && pendingCents > 0:
		return "Parcial"
	default:
		return "Paga"
	}
}

func computeInvoiceStateTx(tx *sql.Tx, workspaceID, invoiceID string) (InvoiceComputedState, error) {
	var closingUnix int64
	var status string
	if err := tx.QueryRow(`
		SELECT i.closing_date, i.status
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id AND a.workspace_id = ?
		WHERE i.id = ? AND a.workspace_id = ?
	`, workspaceID, invoiceID, workspaceID).Scan(&closingUnix, &status); err != nil {
		return InvoiceComputedState{}, err
	}
	total := sumInvoiceTotalTx(tx, workspaceID, invoiceID)
	paidActive, err := sumActiveInvoicePaymentsTx(tx, workspaceID, invoiceID)
	if err != nil {
		return InvoiceComputedState{}, err
	}
	return computeInvoiceState(total, paidActive, closingUnix, time.Now()), nil
}

func computeInvoiceStateDB(db *sql.DB, workspaceID, invoiceID string) (InvoiceComputedState, error) {
	var closingUnix int64
	var status string
	if err := db.QueryRow(`
		SELECT i.closing_date, i.status
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id AND a.workspace_id = ?
		WHERE i.id = ? AND a.workspace_id = ?
	`, workspaceID, invoiceID, workspaceID).Scan(&closingUnix, &status); err != nil {
		return InvoiceComputedState{}, err
	}
	total, err := sumInvoiceTotal(db, workspaceID, invoiceID)
	if err != nil {
		return InvoiceComputedState{}, err
	}
	paidActive, err := sumActiveInvoicePaymentsDB(db, workspaceID, invoiceID)
	if err != nil {
		return InvoiceComputedState{}, err
	}
	return computeInvoiceState(total, paidActive, closingUnix, time.Now()), nil
}

func reconcileInvoicesForTransactionsTx(tx *sql.Tx, workspaceID string, txIDs []string) error {
	if workspaceID == "" || len(txIDs) == 0 {
		return nil
	}
	placeholders := sqlPlaceholders(len(txIDs))
	args := make([]interface{}, 0, len(txIDs)+1)
	args = append(args, workspaceID)
	for _, id := range txIDs {
		args = append(args, id)
	}
	rows, err := tx.Query(`
		SELECT DISTINCT invoice_id
		FROM transactions
		WHERE workspace_id = ?
		  AND invoice_id IS NOT NULL
		  AND id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var invoiceIDs []string
	for rows.Next() {
		var invID string
		if err := rows.Scan(&invID); err != nil {
			return err
		}
		if invID != "" {
			invoiceIDs = append(invoiceIDs, invID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, invID := range invoiceIDs {
		if err := reconcileInvoiceStatusTx(tx, workspaceID, invID); err != nil {
			return err
		}
	}
	return nil
}

func reconcileInvoiceStatusTx(tx *sql.Tx, workspaceID, invoiceID string) error {
	if workspaceID == "" || invoiceID == "" {
		return nil
	}

	var closingUnix int64
	var currentStatus string
	if err := tx.QueryRow(`
		SELECT i.closing_date, i.status
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id AND a.workspace_id = ?
		WHERE i.id = ? AND a.workspace_id = ?
	`, workspaceID, invoiceID, workspaceID).Scan(&closingUnix, &currentStatus); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	total := sumInvoiceTotalTx(tx, workspaceID, invoiceID)
	paidActive, err := sumActiveInvoicePaymentsTx(tx, workspaceID, invoiceID)
	if err != nil {
		return err
	}
	pending := total - paidActive
	if pending < 0 {
		pending = 0
	}

	now := time.Now().Unix()

	desiredStatus := currentStatus

	switch {
	case total <= 0 && paidActive <= 0:
		if closingUnix > 0 && closingUnix < now {
			desiredStatus = models.InvoiceStatusClosed
		} else {
			desiredStatus = models.InvoiceStatusOpen
		}
	case pending <= 0:
		desiredStatus = models.InvoiceStatusPaid
	default:
		if closingUnix > 0 && closingUnix <= now {
			desiredStatus = models.InvoiceStatusClosed
		} else {
			desiredStatus = models.InvoiceStatusOpen
		}
	}

	if desiredStatus == currentStatus {
		return nil
	}

	switch desiredStatus {
	case models.InvoiceStatusPaid:
		if _, err := tx.Exec(`
			UPDATE invoices
			SET status = 'PAID', paid_at = COALESCE(paid_at, ?)
			WHERE id = ? AND EXISTS (
				SELECT 1 FROM accounts a WHERE a.id = invoices.account_id AND a.workspace_id = ?
			)
		`, now, invoiceID, workspaceID); err != nil {
			return err
		}
	case models.InvoiceStatusClosed:
		if _, err := tx.Exec(`
			UPDATE invoices
			SET status = 'CLOSED', paid_at = NULL
			WHERE id = ? AND EXISTS (
				SELECT 1 FROM accounts a WHERE a.id = invoices.account_id AND a.workspace_id = ?
			)
		`, invoiceID, workspaceID); err != nil {
			return err
		}
	case models.InvoiceStatusOpen:
		if _, err := tx.Exec(`
			UPDATE invoices
			SET status = 'OPEN', paid_at = NULL
			WHERE id = ? AND EXISTS (
				SELECT 1 FROM accounts a WHERE a.id = invoices.account_id AND a.workspace_id = ?
			)
		`, invoiceID, workspaceID); err != nil {
			return err
		}
	}

	return nil
}