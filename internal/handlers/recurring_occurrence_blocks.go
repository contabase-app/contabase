package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
)

const recurringOccurrenceBlockReasonFrozenInvoice = "frozen_invoice"

type recurringOccurrenceBlockExpectation struct {
	WorkspaceID        string
	SeriesID           string
	RecurringRuleID    string
	RecurrenceSequence int64
	OccurrenceDate     CivilWriteDate
	AccountID          string
}

type recurringOccurrenceFrozenInvoice struct {
	InvoiceID        string
	InvoiceReference string
	InvoiceStatus    string
}

type recurringOccurrenceBlockRow struct {
	WorkspaceID         string
	SeriesID            string
	RecurringRuleID     string
	RecurrenceSequence  int64
	OccurrenceDateCivil string
	InvoiceID           string
	InvoiceReference    string
	InvoiceStatus       string
	Reason              string
}

func validateRecurringOccurrenceBlockExpectation(expected recurringOccurrenceBlockExpectation) error {
	if strings.TrimSpace(expected.WorkspaceID) == "" ||
		strings.TrimSpace(expected.SeriesID) == "" ||
		strings.TrimSpace(expected.RecurringRuleID) == "" ||
		strings.TrimSpace(expected.AccountID) == "" ||
		expected.RecurrenceSequence <= 0 {
		return fmt.Errorf("%w: invalid recurring occurrence block expectation", errInvoiceStateStructural)
	}
	if err := expected.OccurrenceDate.validate(); err != nil {
		return fmt.Errorf("%w: invalid recurring occurrence civil date: %v", errInvoiceStateStructural, err)
	}
	return nil
}

func invoiceStatusIsFrozen(status string) bool {
	return status == models.InvoiceStatusClosed || status == models.InvoiceStatusPaid
}

func ensureAutomaticInvoiceAcceptsTransactionTx(tx *sql.Tx, workspaceID, accountID, invoiceID string) (recurringOccurrenceFrozenInvoice, error) {
	if tx == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(accountID) == "" || strings.TrimSpace(invoiceID) == "" {
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: invalid automatic invoice validation", errInvoiceStateStructural)
	}

	var evidence recurringOccurrenceFrozenInvoice
	var invoiceAccountID, invoiceWorkspaceID, accountType string
	err := tx.QueryRow(`
		SELECT i.id, i.reference, i.status, i.account_id, a.workspace_id, a.type
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ?
	`, invoiceID, workspaceID).Scan(
		&evidence.InvoiceID,
		&evidence.InvoiceReference,
		&evidence.InvoiceStatus,
		&invoiceAccountID,
		&invoiceWorkspaceID,
		&accountType,
	)
	if err != nil {
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: automatic invoice lookup: %v", errInvoiceStateStructural, err)
	}
	if invoiceWorkspaceID != workspaceID || invoiceAccountID != accountID || accountType != models.AccountTypeCreditCard {
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: automatic invoice workspace/account mismatch", errInvoiceStateStructural)
	}
	switch evidence.InvoiceStatus {
	case models.InvoiceStatusOpen:
		return evidence, nil
	case models.InvoiceStatusClosed, models.InvoiceStatusPaid:
		return evidence, errAutomaticInvoiceFrozen
	default:
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: unsupported invoice status %q", errInvoiceStateStructural, evidence.InvoiceStatus)
	}
}

// ensureInvoiceAcceptsMutableCompositionTx is used by explicit transaction
// composition. A closed invoice is still unpaid and remains editable; recurring
// materialization keeps its stronger frozen-cycle contract through
// ensureAutomaticInvoiceAcceptsTransactionTx.
func ensureInvoiceAcceptsMutableCompositionTx(tx *sql.Tx, workspaceID, accountID, invoiceID string) (recurringOccurrenceFrozenInvoice, error) {
	evidence, err := ensureAutomaticInvoiceAcceptsTransactionTx(tx, workspaceID, accountID, invoiceID)
	if errors.Is(err, errAutomaticInvoiceFrozen) && evidence.InvoiceStatus == models.InvoiceStatusClosed {
		return evidence, nil
	}
	return evidence, err
}

func resolveAutomaticInvoiceForOccurrenceTx(tx *sql.Tx, workspaceID, accountID string, occurrenceDate CivilWriteDate) (recurringOccurrenceFrozenInvoice, error) {
	if err := occurrenceDate.validate(); err != nil {
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: invalid automatic invoice occurrence date", errInvoiceStateStructural)
	}
	invoiceID, reference, status, _, _, err := ensureOpenInvoiceTx(tx, workspaceID, accountID, occurrenceDate.LegacyUnix())
	if err != nil {
		return recurringOccurrenceFrozenInvoice{}, err
	}
	evidence, guardErr := ensureAutomaticInvoiceAcceptsTransactionTx(tx, workspaceID, accountID, invoiceID)
	if evidence.InvoiceReference != reference || evidence.InvoiceStatus != status {
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: automatic invoice routing changed inside transaction", errInvoiceStateStructural)
	}
	return evidence, guardErr
}

func resolveAutomaticInvoiceForUnixTx(tx *sql.Tx, workspaceID, accountID string, occurrenceUnix int64) (recurringOccurrenceFrozenInvoice, error) {
	invoiceID, reference, status, _, _, err := ensureOpenInvoiceTx(tx, workspaceID, accountID, occurrenceUnix)
	if err != nil {
		return recurringOccurrenceFrozenInvoice{}, err
	}
	evidence, guardErr := ensureAutomaticInvoiceAcceptsTransactionTx(tx, workspaceID, accountID, invoiceID)
	if evidence.InvoiceReference != reference || evidence.InvoiceStatus != status {
		return recurringOccurrenceFrozenInvoice{}, fmt.Errorf("%w: automatic invoice routing changed inside transaction", errInvoiceStateStructural)
	}
	return evidence, guardErr
}

// resolveAutomaticCardTransactionInvoiceTx is the write-side guard around the
// general invoice resolver. The unguarded resolver is also used by explicit
// user selection and read-only destination previews; only automatic writes
// must reject a CLOSED/PAID destination.
func resolveAutomaticCardTransactionInvoiceTx(tx *sql.Tx, workspaceID, accountID string, occurrenceUnix int64, faturaOffset string) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	invoiceID, reference, status, closingUnix, dueUnix, err = resolveCardTransactionInvoiceTx(tx, workspaceID, accountID, occurrenceUnix, faturaOffset)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	if _, err = ensureInvoiceAcceptsMutableCompositionTx(tx, workspaceID, accountID, invoiceID); err != nil {
		return "", "", "", 0, 0, err
	}
	return invoiceID, reference, status, closingUnix, dueUnix, nil
}

func recurringOccurrenceTerminalConflictsTx(tx *sql.Tx, workspaceID, seriesID string, sequence int64) (transactionExists, skipExists, blockExists bool, err error) {
	var transaction, skip, block int
	err = tx.QueryRow(`
		SELECT
			EXISTS(SELECT 1 FROM transactions
			       WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?),
			EXISTS(SELECT 1 FROM recurring_occurrence_skips
			       WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?),
			EXISTS(SELECT 1 FROM recurring_occurrence_blocks
			       WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?)
	`, workspaceID, seriesID, sequence, workspaceID, seriesID, sequence, workspaceID, seriesID, sequence).Scan(&transaction, &skip, &block)
	if err != nil {
		return false, false, false, err
	}
	return transaction != 0, skip != 0, block != 0, nil
}

// resolveRecurringOccurrenceBlockTx validates a committed tombstone as
// historical evidence. It intentionally does not read credit-card cycle
// configuration or recompute invoice routing: configuration drift after commit
// cannot reinterpret a terminal occurrence.
func resolveRecurringOccurrenceBlockTx(tx *sql.Tx, expected recurringOccurrenceBlockExpectation) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("%w: nil recurring occurrence block transaction", errInvoiceStateStructural)
	}
	if err := validateRecurringOccurrenceBlockExpectation(expected); err != nil {
		return false, err
	}

	rows, err := tx.Query(`
		SELECT workspace_id, series_id, recurring_rule_id, recurrence_sequence,
		       occurrence_date_civil, invoice_id, invoice_reference,
		       invoice_status, reason
		FROM recurring_occurrence_blocks
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
	`, expected.WorkspaceID, expected.SeriesID, expected.RecurrenceSequence)
	if err != nil {
		return false, fmt.Errorf("resolve recurring occurrence block: %w", err)
	}
	defer rows.Close()

	var blocks []recurringOccurrenceBlockRow
	for rows.Next() {
		var block recurringOccurrenceBlockRow
		if err := rows.Scan(
			&block.WorkspaceID,
			&block.SeriesID,
			&block.RecurringRuleID,
			&block.RecurrenceSequence,
			&block.OccurrenceDateCivil,
			&block.InvoiceID,
			&block.InvoiceReference,
			&block.InvoiceStatus,
			&block.Reason,
		); err != nil {
			return false, fmt.Errorf("resolve recurring occurrence block: %w", err)
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("resolve recurring occurrence block: %w", err)
	}
	if len(blocks) == 0 {
		return false, nil
	}
	if len(blocks) != 1 {
		return false, fmt.Errorf("%w: duplicate recurring occurrence blocks", errInvoiceStateStructural)
	}

	block := blocks[0]
	if block.WorkspaceID != expected.WorkspaceID ||
		block.SeriesID != expected.SeriesID ||
		block.RecurringRuleID != expected.RecurringRuleID ||
		block.RecurrenceSequence != expected.RecurrenceSequence ||
		block.OccurrenceDateCivil != expected.OccurrenceDate.Civil() {
		return false, fmt.Errorf("%w: recurring occurrence block identity mismatch", errInvoiceStateStructural)
	}
	if block.Reason != recurringOccurrenceBlockReasonFrozenInvoice || !invoiceStatusIsFrozen(block.InvoiceStatus) {
		return false, fmt.Errorf("%w: recurring occurrence block reason/status mismatch", errInvoiceStateStructural)
	}
	if strings.TrimSpace(block.InvoiceID) == "" || strings.TrimSpace(block.InvoiceReference) == "" {
		return false, fmt.Errorf("%w: recurring occurrence block invoice evidence is incomplete", errInvoiceStateStructural)
	}
	if _, _, err := parseInvoiceReference(block.InvoiceReference); err != nil {
		return false, fmt.Errorf("%w: invalid recurring occurrence block invoice reference", errInvoiceStateStructural)
	}
	if err := validateRecurringOccurrenceBlockRuleCoverageTx(tx, block); err != nil {
		return false, err
	}

	var transactionExists, skipExists int
	if err := tx.QueryRow(`
		SELECT
			EXISTS(SELECT 1 FROM transactions
			       WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?),
			EXISTS(SELECT 1 FROM recurring_occurrence_skips
			       WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?)
	`, expected.WorkspaceID, expected.SeriesID, expected.RecurrenceSequence,
		expected.WorkspaceID, expected.SeriesID, expected.RecurrenceSequence,
	).Scan(&transactionExists, &skipExists); err != nil {
		return false, fmt.Errorf("resolve recurring occurrence terminal conflicts: %w", err)
	}
	if transactionExists != 0 || skipExists != 0 {
		return false, fmt.Errorf("%w: recurring occurrence block contradicts another terminal outcome", errInvoiceStateStructural)
	}

	var liveReference, liveStatus, liveAccountID, liveWorkspaceID, liveAccountType string
	err = tx.QueryRow(`
		SELECT i.reference, i.status, i.account_id, a.workspace_id, a.type
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ?
	`, block.InvoiceID, expected.WorkspaceID).Scan(&liveReference, &liveStatus, &liveAccountID, &liveWorkspaceID, &liveAccountType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: recurring occurrence block invoice unavailable", errInvoiceStateStructural)
	}
	if err != nil {
		return false, fmt.Errorf("resolve recurring occurrence block invoice: %w", err)
	}
	if liveWorkspaceID != expected.WorkspaceID ||
		liveAccountID != expected.AccountID ||
		liveAccountType != models.AccountTypeCreditCard ||
		liveReference != block.InvoiceReference ||
		!invoiceStatusIsFrozen(liveStatus) {
		return false, fmt.Errorf("%w: recurring occurrence block live invoice mismatch", errInvoiceStateStructural)
	}

	return true, nil
}

func validateRecurringOccurrenceBlockRuleCoverageTx(tx *sql.Tx, block recurringOccurrenceBlockRow) error {
	var workspaceID, seriesID, frequency string
	var effectiveStart int64
	var effectiveEnd, totalOccurrences sql.NullInt64
	var startsAtCivil sql.NullString
	if err := tx.QueryRow(`
		SELECT workspace_id, series_id, COALESCE(effective_start_sequence, 1),
		       effective_end_sequence, total_occurrences,
		       COALESCE(NULLIF(starts_at_civil, ''), NULLIF(start_date_civil, '')),
		       frequency
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, block.RecurringRuleID, block.WorkspaceID).Scan(
		&workspaceID, &seriesID, &effectiveStart, &effectiveEnd,
		&totalOccurrences, &startsAtCivil, &frequency,
	); err != nil {
		return fmt.Errorf("%w: recurring occurrence block rule lookup: %v", errInvoiceStateStructural, err)
	}
	if workspaceID != block.WorkspaceID || seriesID != block.SeriesID || effectiveStart <= 0 ||
		block.RecurrenceSequence < effectiveStart ||
		(effectiveEnd.Valid && block.RecurrenceSequence > effectiveEnd.Int64) ||
		(totalOccurrences.Valid && totalOccurrences.Int64 > 0 && block.RecurrenceSequence > totalOccurrences.Int64) ||
		!startsAtCivil.Valid {
		return fmt.Errorf("%w: recurring occurrence block rule coverage mismatch", errInvoiceStateStructural)
	}

	var supersedingRule int
	if err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM recurring_rules rr
			WHERE rr.workspace_id = ?
			  AND rr.series_id = ?
			  AND rr.id != ?
			  AND COALESCE(rr.effective_start_sequence, 1) > ?
			  AND COALESCE(rr.effective_start_sequence, 1) <= ?
		)
	`, block.WorkspaceID, block.SeriesID, block.RecurringRuleID, effectiveStart, block.RecurrenceSequence).Scan(&supersedingRule); err != nil {
		return fmt.Errorf("%w: recurring occurrence block rule lineage lookup: %v", errInvoiceStateStructural, err)
	}
	if supersedingRule != 0 {
		return fmt.Errorf("%w: recurring occurrence block rule does not cover sequence", errInvoiceStateStructural)
	}

	anchor, err := NewCivilWriteDate(startsAtCivil.String)
	if err != nil {
		return fmt.Errorf("%w: recurring occurrence block rule civil anchor", errInvoiceStateStructural)
	}
	occurrence, err := recurrenceCivilDateFromOffset(anchor, frequency, block.RecurrenceSequence-effectiveStart)
	if err != nil || occurrence.Civil() != block.OccurrenceDateCivil {
		return fmt.Errorf("%w: recurring occurrence block rule civil date mismatch", errInvoiceStateStructural)
	}
	return nil
}

func insertRecurringOccurrenceFrozenBlockTx(tx *sql.Tx, expected recurringOccurrenceBlockExpectation, evidence recurringOccurrenceFrozenInvoice, now int64) (bool, error) {
	if tx == nil || now <= 0 {
		return false, fmt.Errorf("%w: invalid recurring occurrence block write", errInvoiceStateStructural)
	}
	if err := validateRecurringOccurrenceBlockExpectation(expected); err != nil {
		return false, err
	}
	if strings.TrimSpace(evidence.InvoiceID) == "" || strings.TrimSpace(evidence.InvoiceReference) == "" || !invoiceStatusIsFrozen(evidence.InvoiceStatus) {
		return false, fmt.Errorf("%w: invalid frozen invoice evidence", errInvoiceStateStructural)
	}

	_, err := tx.Exec(`
		INSERT INTO recurring_occurrence_blocks (
			workspace_id, series_id, recurring_rule_id, recurrence_sequence,
			occurrence_date_civil, invoice_id, invoice_reference,
			invoice_status, reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'frozen_invoice', ?)
		ON CONFLICT(workspace_id, series_id, recurrence_sequence) DO NOTHING
	`, expected.WorkspaceID, expected.SeriesID, expected.RecurringRuleID,
		expected.RecurrenceSequence, expected.OccurrenceDate.Civil(), evidence.InvoiceID,
		evidence.InvoiceReference, evidence.InvoiceStatus, now)
	if err != nil {
		return false, fmt.Errorf("insert recurring occurrence frozen block: %w", err)
	}

	resolved, err := resolveRecurringOccurrenceBlockTx(tx, expected)
	if err != nil {
		return false, err
	}
	if !resolved {
		return false, fmt.Errorf("%w: recurring occurrence block insert was not persisted", errInvoiceStateStructural)
	}
	return true, nil
}
