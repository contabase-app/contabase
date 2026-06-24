package handlers

import (
	"database/sql"

	"github.com/contabase-app/contabase/internal/models"
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
		"AND ip.transaction_id = " + prefix + "id " +
		"AND ip.reversed_at IS NULL)"
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
