package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

const invoiceCycleSoonDays = 7

type InvoiceCycleState string

const (
	InvoiceCycleOpen    InvoiceCycleState = "open"
	InvoiceCycleSoon    InvoiceCycleState = "soon"
	InvoiceCycleClosed  InvoiceCycleState = "closed"
	InvoiceCycleUnknown InvoiceCycleState = "unknown"
)

type InvoiceFinancialState string

const (
	InvoiceFinancialPending InvoiceFinancialState = "pending"
	InvoiceFinancialPartial InvoiceFinancialState = "partial"
	InvoiceFinancialPaid    InvoiceFinancialState = "paid"
	InvoiceFinancialEmpty   InvoiceFinancialState = "empty"
)

type InvoiceComputedState struct {
	Total      int64
	PaidActive int64
	Pending    int64
	Cycle      InvoiceCycleState
	Financial  InvoiceFinancialState
	CycleDays  int
	CycleLabel string
	FinLabel   string
}

type invoiceFinancialSnapshot struct {
	ID                      string
	AccountID               string
	Status                  string
	Total                   int64
	ClosingDate             int64
	PaidAt                  sql.NullInt64
	TotalPaymentRecordCount int64
	ActivePaymentCount      int64
	ActivePaymentSum        int64
	LegacyPaidAmount        int64
	EffectivePaid           int64
}

var (
	errInvoiceStateTransitionConflict  = errors.New("invoice state transition conflict")
	errAutomaticInvoiceFrozen          = fmt.Errorf("%w: automatic invoice destination is frozen", errInvoiceStateTransitionConflict)
	errInvoiceWouldOverpay             = fmt.Errorf("%w: effective paid exceeds invoice total", errInvoiceStateTransitionConflict)
	errInvoiceImplicitReopenBlocked    = fmt.Errorf("%w: paid invoice cannot reopen implicitly", errInvoiceStateTransitionConflict)
	errInvoiceStateStructural          = errors.New("invoice state is structurally inconsistent")
	errInvoiceLegacyStateIndeterminate = fmt.Errorf("%w: legacy paid amount cannot start modern lineage safely", errInvoiceStateTransitionConflict)
)

func effectivePaidFromModernAndLegacy(totalPaymentRecordCount, activeSum, legacyPaid int64) int64 {
	if totalPaymentRecordCount > 0 {
		return activeSum
	}
	return legacyPaid
}

// invoiceEffectivePaidSQLExpr is the bulk-query equivalent of
// effectivePaidFromModernAndLegacy. Callers must use the fixed invoice/account
// aliases i and a; aliases never come from request data.
const invoiceEffectivePaidSQLExpr = `CASE
	WHEN EXISTS (
		SELECT 1
		FROM invoice_payments ip_lineage
		WHERE ip_lineage.workspace_id = a.workspace_id
		  AND ip_lineage.invoice_id = i.id
	)
	THEN COALESCE((
		SELECT SUM(ip_active.amount_cents)
		FROM invoice_payments ip_active
		WHERE ip_active.workspace_id = a.workspace_id
		  AND ip_active.invoice_id = i.id
		  AND ip_active.reversed_at IS NULL
	), 0)
	ELSE COALESCE(i.paid_amount, 0)
END`

// transactionNetAmountTSQL is the effective expense amount for queries that
// use the fixed transaction alias t. Refunds are contra-expenses, never income.
const transactionNetAmountTSQL = `CASE WHEN t.id IS NULL THEN NULL ELSE contabase_refund_net(t.amount, (
	SELECT contabase_refund_sum(r.amount_cents)
	FROM transaction_refunds r
	WHERE r.workspace_id = t.workspace_id
	  AND r.transaction_id = t.id
	  AND r.reversed_at IS NULL
)) END`

// transactionNetAmountTxSQL is the equivalent expression for the fixed tx
// alias used by cash-flow allocation queries.
const transactionNetAmountTxSQL = `CASE WHEN tx.id IS NULL THEN NULL ELSE contabase_refund_net(tx.amount, (
	SELECT contabase_refund_sum(r.amount_cents)
	FROM transaction_refunds r
	WHERE r.workspace_id = tx.workspace_id
	  AND r.transaction_id = tx.id
	  AND r.reversed_at IS NULL
)) END`

// invoiceGeneralRefundTotalIASQL is the active adjustment total for callers
// with the fixed invoice/account aliases i and a. General refunds settle a
// later invoice without fabricating an income or changing the original expense.
const invoiceGeneralRefundTotalIASQL = `COALESCE((
	SELECT SUM(ir.amount_cents)
	FROM invoice_refunds ir
	WHERE ir.workspace_id = a.workspace_id
	  AND ir.invoice_id = i.id
	  AND ir.reversed_at IS NULL
), 0)`

func loadInvoiceFinancialSnapshotTx(tx *sql.Tx, workspaceID, invoiceID string) (invoiceFinancialSnapshot, error) {
	var snapshot invoiceFinancialSnapshot
	err := tx.QueryRow(`
		SELECT
			i.id,
			i.account_id,
			i.status,
			i.closing_date,
			i.paid_at,
			COALESCE((
				SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
				FROM transactions t
				WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
			), 0) - `+invoiceGeneralRefundTotalIASQL+`,
			COALESCE((
				SELECT COUNT(ip.id)
				FROM invoice_payments ip
				WHERE ip.workspace_id = a.workspace_id
				  AND ip.invoice_id = i.id
			), 0),
			COALESCE((
				SELECT COUNT(ip.id)
				FROM invoice_payments ip
				WHERE ip.workspace_id = a.workspace_id
				  AND ip.invoice_id = i.id
				  AND ip.reversed_at IS NULL
			), 0),
			COALESCE((
				SELECT SUM(ip.amount_cents)
				FROM invoice_payments ip
				WHERE ip.workspace_id = a.workspace_id
				  AND ip.invoice_id = i.id
				  AND ip.reversed_at IS NULL
			), 0),
			COALESCE(i.paid_amount, 0)
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(
		&snapshot.ID,
		&snapshot.AccountID,
		&snapshot.Status,
		&snapshot.ClosingDate,
		&snapshot.PaidAt,
		&snapshot.Total,
		&snapshot.TotalPaymentRecordCount,
		&snapshot.ActivePaymentCount,
		&snapshot.ActivePaymentSum,
		&snapshot.LegacyPaidAmount,
	)
	if err != nil {
		return snapshot, err
	}
	snapshot.EffectivePaid = effectivePaidFromModernAndLegacy(snapshot.TotalPaymentRecordCount, snapshot.ActivePaymentSum, snapshot.LegacyPaidAmount)
	return snapshot, nil
}

func loadInvoiceFinancialSnapshotDB(db *sql.DB, workspaceID, invoiceID string) (invoiceFinancialSnapshot, error) {
	var snapshot invoiceFinancialSnapshot
	err := db.QueryRow(`
		SELECT
			i.id,
			i.account_id,
			i.status,
			i.closing_date,
			i.paid_at,
			COALESCE((
				SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
				FROM transactions t
				WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
			), 0) - `+invoiceGeneralRefundTotalIASQL+`,
			COALESCE((
				SELECT COUNT(ip.id)
				FROM invoice_payments ip
				WHERE ip.workspace_id = a.workspace_id
				  AND ip.invoice_id = i.id
			), 0),
			COALESCE((
				SELECT COUNT(ip.id)
				FROM invoice_payments ip
				WHERE ip.workspace_id = a.workspace_id
				  AND ip.invoice_id = i.id
				  AND ip.reversed_at IS NULL
			), 0),
			COALESCE((
				SELECT SUM(ip.amount_cents)
				FROM invoice_payments ip
				WHERE ip.workspace_id = a.workspace_id
				  AND ip.invoice_id = i.id
				  AND ip.reversed_at IS NULL
			), 0),
			COALESCE(i.paid_amount, 0)
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(
		&snapshot.ID,
		&snapshot.AccountID,
		&snapshot.Status,
		&snapshot.ClosingDate,
		&snapshot.PaidAt,
		&snapshot.Total,
		&snapshot.TotalPaymentRecordCount,
		&snapshot.ActivePaymentCount,
		&snapshot.ActivePaymentSum,
		&snapshot.LegacyPaidAmount,
	)
	if err != nil {
		return snapshot, err
	}
	snapshot.EffectivePaid = effectivePaidFromModernAndLegacy(snapshot.TotalPaymentRecordCount, snapshot.ActivePaymentSum, snapshot.LegacyPaidAmount)
	return snapshot, nil
}

func (snapshot invoiceFinancialSnapshot) blocksIncomingTransaction() bool {
	if snapshot.Status != models.InvoiceStatusOpen && snapshot.Status != models.InvoiceStatusClosed {
		return true
	}
	if snapshot.EffectivePaid > snapshot.Total {
		return true
	}
	return snapshot.Total > 0 && snapshot.EffectivePaid >= snapshot.Total
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

// invoiceClosingCivilDays returns the signed number of civil calendar days
// between the current application civil date and the invoice closing date.
// Both sides are reduced to civil dates before the comparison, so the result
// never depends on the clock time stored in closingUnix or on duration
// arithmetic across DST boundaries.
func invoiceClosingCivilDays(closingUnix int64, now time.Time) int {
	closingDate := civildate.LegacyUnixToCivilDateUTC(closingUnix)
	today := civildate.CivilDateAt(AppCivilDateUTC(now), time.UTC)
	return int(civildate.CivilDateDaysBetween(today, closingDate))
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
		days := invoiceClosingCivilDays(closingUnix, now)
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
	days := invoiceClosingCivilDays(closingUnix, now)
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
	total, err := sumInvoiceTotalTx(tx, workspaceID, invoiceID)
	if err != nil {
		return InvoiceComputedState{}, err
	}
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

func reconcileInvoicesForTransactionsTx(tx *sql.Tx, workspaceID string, txIDs []string, transitionAt int64) error {
	if workspaceID == "" || len(txIDs) == 0 {
		return nil
	}
	if transitionAt <= 0 {
		return fmt.Errorf("%w: invalid composition transition timestamp", errInvoiceStateStructural)
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
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, workspaceID, invID, transitionAt); err != nil {
			return err
		}
	}
	return nil
}

func reconcileInvoiceAfterCompositionMutationTx(tx *sql.Tx, workspaceID, invoiceID string, transitionAt int64) error {
	if workspaceID == "" || invoiceID == "" || transitionAt <= 0 {
		return fmt.Errorf("%w: invalid composition reconcile input", errInvoiceStateStructural)
	}

	snapshot, err := loadInvoiceFinancialSnapshotTx(tx, workspaceID, invoiceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: invoice not found in workspace", errInvoiceStateStructural)
		}
		return err
	}

	if snapshot.Status != models.InvoiceStatusOpen && snapshot.Status != models.InvoiceStatusClosed && snapshot.Status != models.InvoiceStatusPaid {
		return fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
	}
	if snapshot.EffectivePaid > snapshot.Total {
		return errInvoiceWouldOverpay
	}
	if snapshot.Status == models.InvoiceStatusPaid {
		if snapshot.EffectivePaid < snapshot.Total {
			return errInvoiceImplicitReopenBlocked
		}
		if snapshot.Total > 0 && !snapshot.PaidAt.Valid {
			if snapshot.TotalPaymentRecordCount == 0 {
				return errInvoiceLegacyStateIndeterminate
			}
			return fmt.Errorf("%w: paid invoice is missing paid_at", errInvoiceStateStructural)
		}
		return updateInvoiceAfterStateReconcileTx(tx, workspaceID, snapshot, models.InvoiceStatusPaid, snapshot.PaidAt)
	}

	desiredStatus := snapshot.Status
	desiredPaidAt := sql.NullInt64{}
	switch {
	case snapshot.Total > 0 && snapshot.EffectivePaid == snapshot.Total:
		desiredStatus = models.InvoiceStatusPaid
		desiredPaidAt = sql.NullInt64{Int64: transitionAt, Valid: true}
	case snapshot.Status == models.InvoiceStatusClosed:
		desiredStatus = models.InvoiceStatusClosed
	case snapshot.Status == models.InvoiceStatusOpen && snapshot.ClosingDate > 0 && snapshot.ClosingDate <= transitionAt:
		desiredStatus = models.InvoiceStatusClosed
	default:
		desiredStatus = models.InvoiceStatusOpen
	}
	return updateInvoiceAfterStateReconcileTx(tx, workspaceID, snapshot, desiredStatus, desiredPaidAt)
}

func updateInvoiceAfterStateReconcileTx(tx *sql.Tx, workspaceID string, snapshot invoiceFinancialSnapshot, desiredStatus string, desiredPaidAt sql.NullInt64) error {
	paidAmount := snapshot.LegacyPaidAmount
	if snapshot.TotalPaymentRecordCount > 0 {
		paidAmount = snapshot.ActivePaymentSum
	}
	var paidAt interface{}
	if desiredPaidAt.Valid {
		paidAt = desiredPaidAt.Int64
	}
	result, err := tx.Exec(`
		UPDATE invoices
		SET status = ?, paid_amount = ?, paid_at = ?
		WHERE id = ?
		  AND account_id = ?
		  AND status = ?
		  AND EXISTS (
		      SELECT 1
		      FROM accounts a
		      WHERE a.id = invoices.account_id
		        AND a.workspace_id = ?
		        AND a.type = ?
		  )
	`, desiredStatus, paidAmount, paidAt, snapshot.ID, snapshot.AccountID, snapshot.Status, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("%w: invoice reconcile affected %d rows", errInvoiceStateStructural, updated)
	}
	return nil
}

func validateInvoiceForNewPayment(snapshot invoiceFinancialSnapshot) error {
	if snapshot.Status != models.InvoiceStatusOpen && snapshot.Status != models.InvoiceStatusClosed && snapshot.Status != models.InvoiceStatusPaid {
		return fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
	}
	if snapshot.Status == models.InvoiceStatusPaid {
		return errInvoiceStateTransitionConflict
	}
	if snapshot.EffectivePaid > snapshot.Total {
		return errInvoiceWouldOverpay
	}
	if snapshot.TotalPaymentRecordCount == 0 && snapshot.LegacyPaidAmount > 0 {
		return errInvoiceLegacyStateIndeterminate
	}
	return nil
}

func reconcileInvoiceAfterPaymentMutationTx(tx *sql.Tx, workspaceID, invoiceID string, transitionAt int64) error {
	if workspaceID == "" || invoiceID == "" || transitionAt <= 0 {
		return fmt.Errorf("%w: invalid payment reconcile input", errInvoiceStateStructural)
	}
	snapshot, err := loadInvoiceFinancialSnapshotTx(tx, workspaceID, invoiceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: invoice not found in workspace", errInvoiceStateStructural)
		}
		return err
	}
	if snapshot.Status != models.InvoiceStatusOpen && snapshot.Status != models.InvoiceStatusClosed {
		if snapshot.Status == models.InvoiceStatusPaid {
			return errInvoiceStateTransitionConflict
		}
		return fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
	}
	if snapshot.Total <= 0 {
		return fmt.Errorf("%w: non-positive invoice total cannot receive payment", errInvoiceStateTransitionConflict)
	}
	timeline, err := loadActiveInvoicePaymentTimelineTx(tx, workspaceID, invoiceID, snapshot.Total)
	if err != nil {
		return err
	}
	if timeline.ActiveSum > snapshot.Total {
		return errInvoiceWouldOverpay
	}

	desiredStatus := snapshot.Status
	desiredPaidAt := sql.NullInt64{}
	if timeline.ActiveSum == snapshot.Total {
		if timeline.ThresholdPaidAt == nil {
			return fmt.Errorf("%w: paid threshold is absent", errInvoiceStateStructural)
		}
		desiredStatus = models.InvoiceStatusPaid
		desiredPaidAt = sql.NullInt64{Int64: *timeline.ThresholdPaidAt, Valid: true}
	} else if snapshot.Status == models.InvoiceStatusOpen && snapshot.ClosingDate > 0 && snapshot.ClosingDate <= transitionAt {
		desiredStatus = models.InvoiceStatusClosed
	}
	snapshot.ActivePaymentSum = timeline.ActiveSum
	snapshot.EffectivePaid = timeline.ActiveSum
	return updateInvoiceAfterStateReconcileTx(tx, workspaceID, snapshot, desiredStatus, desiredPaidAt)
}
