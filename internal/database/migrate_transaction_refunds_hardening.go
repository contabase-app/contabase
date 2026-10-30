package database

import (
	"database/sql"
	"fmt"
)

// Migration 68 re-audits databases that may have been modified after 67, then
// replaces only the affected guards. The runner commits guards and version
// registration together; it never changes financial rows.
func migrateTransactionRefundParentHardening(tx *sql.Tx) error {
	// An orphan refund workspace would be missed by per-workspace enumeration.
	// Check it before installing guards so registration remains atomic.
	if err := assertNoForeignKeyViolations(tx); err != nil {
		return fmt.Errorf("audit transaction refunds for migration 68: foreign keys: %w", err)
	}
	if err := auditTransactionRefundsAfter67(tx); err != nil {
		return fmt.Errorf("audit transaction refunds for migration 68: %w", err)
	}
	statements := []string{
		`DROP TRIGGER trg_transaction_refunds_validate_insert`,
		fmt.Sprintf(`CREATE TRIGGER trg_transaction_refunds_validate_insert
		BEFORE INSERT ON transaction_refunds
		FOR EACH ROW
		WHEN typeof(NEW.amount_cents) != 'integer'
		  OR NEW.amount_cents <= 0
		  OR NEW.reversed_at IS NOT NULL
		  OR NEW.reversed_by IS NOT NULL
		  OR NOT EXISTS (
			SELECT 1 FROM transactions t
			JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
			JOIN accounts a ON a.id = i.account_id AND a.workspace_id = t.workspace_id
			WHERE t.id = NEW.transaction_id AND t.workspace_id = NEW.workspace_id
			  AND t.type = 'EXPENSE'
			  AND typeof(t.amount) = 'integer' AND t.amount > 0
		  )
		  OR NOT (%s)
		BEGIN
			SELECT RAISE(ABORT, 'invalid transaction refund');
		END`, boundedRefundsSQL("(SELECT t.amount FROM transactions t WHERE t.id = NEW.transaction_id AND t.workspace_id = NEW.workspace_id)", "NEW.transaction_id", "NEW.workspace_id", true)),
		`DROP TRIGGER trg_transactions_refund_floor`,
		fmt.Sprintf(`CREATE TRIGGER trg_transactions_refund_floor
		BEFORE UPDATE OF amount ON transactions
		FOR EACH ROW
		WHEN EXISTS (
			SELECT 1 FROM transaction_refunds r
			WHERE r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id
		)
		AND (typeof(NEW.amount) != 'integer' OR NEW.amount <= 0 OR NOT (%s))
		BEGIN
			SELECT RAISE(ABORT, 'transaction amount below active refunds or invalid type');
		END`, boundedRefundsSQL("NEW.amount", "OLD.id", "OLD.workspace_id", false)),
		`DROP TRIGGER trg_transactions_refund_parent_identity`,
		`CREATE TRIGGER trg_transactions_refund_parent_identity
		BEFORE UPDATE OF workspace_id, type, invoice_id, account_id ON transactions
		FOR EACH ROW
		WHEN EXISTS (
			SELECT 1 FROM transaction_refunds r
			WHERE r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id
		)
		AND (
			NEW.workspace_id != OLD.workspace_id
			OR NEW.type != 'EXPENSE'
			OR NEW.invoice_id IS NULL
			OR NOT EXISTS (
				SELECT 1 FROM invoices i
				JOIN accounts a ON a.id = i.account_id AND a.workspace_id = NEW.workspace_id
				WHERE i.id = NEW.invoice_id AND i.account_id = NEW.account_id
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'transaction refund parent identity would become invalid');
		END`,
		// No product path moves an account to another workspace. Invoices
		// inherit its workspace, so any such move can invalidate their rows.
		`CREATE TRIGGER trg_accounts_refund_workspace_identity
		BEFORE UPDATE OF workspace_id ON accounts
		FOR EACH ROW WHEN NEW.workspace_id IS NOT OLD.workspace_id
		BEGIN
			SELECT RAISE(ABORT, 'account workspace is immutable');
		END`,
		// No product path reparents an invoice. A transaction may legitimately
		// change invoice_id, which the transaction guard above still permits
		// when its refund ownership remains valid.
		`CREATE TRIGGER trg_invoices_refund_account_identity
		BEFORE UPDATE OF account_id ON invoices
		FOR EACH ROW WHEN NEW.account_id IS NOT OLD.account_id
		BEGIN
			SELECT RAISE(ABORT, 'invoice account is immutable');
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// boundedRefundsSQL consumes positive INTEGER refunds one at a time from an
// INTEGER parent. A negative remainder stops recursion and fails the count
// check, without ever summing beyond the signed 64-bit monetary domain.
func boundedRefundsSQL(parentAmount, transactionID, workspaceID string, includeNew bool) string {
	source := fmt.Sprintf(`SELECT 1 AS source_order, r.id, r.amount_cents
		FROM transaction_refunds r
		WHERE r.transaction_id = %s AND r.workspace_id = %s
		  AND r.reversed_at IS NULL`, transactionID, workspaceID)
	if includeNew {
		source = "SELECT 0 AS source_order, '' AS id, NEW.amount_cents AS amount_cents UNION ALL " + source
	}
	return fmt.Sprintf(`COALESCE((
		WITH RECURSIVE
		ordered AS (
			SELECT ROW_NUMBER() OVER (ORDER BY source_order, id) AS n, amount_cents
			FROM (%s)
		),
		remaining(n, cents) AS (
			SELECT 0, %s
			UNION ALL
			SELECT o.n, CASE
				WHEN typeof(o.amount_cents) = 'integer'
				 AND o.amount_cents > 0 AND o.amount_cents <= remaining.cents
				THEN remaining.cents - o.amount_cents ELSE -1 END
			FROM remaining JOIN ordered o ON o.n = remaining.n + 1
			WHERE remaining.cents >= 0
		)
		SELECT (SELECT MAX(n) FROM remaining) = (SELECT COUNT(*) FROM ordered)
		  AND NOT EXISTS (SELECT 1 FROM remaining WHERE cents < 0)
	), 0)`, source, parentAmount)
}

func auditTransactionRefundsAfter67(tx *sql.Tx) error {
	// CONTROL-PLANE GLOBAL: enumerate workspace IDs only. All financial
	// reads below are scoped to one of these IDs.
	workspaceRows, err := tx.Query(`SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return err
	}
	var workspaceIDs []string
	for workspaceRows.Next() {
		var id string
		if err := workspaceRows.Scan(&id); err != nil {
			workspaceRows.Close()
			return err
		}
		workspaceIDs = append(workspaceIDs, id)
	}
	if err := workspaceRows.Err(); err != nil {
		workspaceRows.Close()
		return err
	}
	if err := workspaceRows.Close(); err != nil {
		return err
	}
	for _, workspaceID := range workspaceIDs {
		if err := auditWorkspaceTransactionRefunds(tx, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func auditWorkspaceTransactionRefunds(tx *sql.Tx, workspaceID string) error {
	rows, err := tx.Query(`
		SELECT r.id, r.transaction_id, typeof(r.amount_cents),
		       CASE WHEN typeof(r.amount_cents) = 'integer' THEN r.amount_cents END,
		       r.reversed_at, r.reversed_by,
		       t.id, t.type, typeof(t.amount),
		       CASE WHEN typeof(t.amount) = 'integer' THEN t.amount END,
		       i.id, a.id
		FROM transaction_refunds r
		LEFT JOIN transactions t ON t.id = r.transaction_id AND t.workspace_id = r.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		LEFT JOIN accounts a ON a.id = i.account_id AND a.workspace_id = r.workspace_id
		WHERE r.workspace_id = ?
		ORDER BY r.transaction_id, r.id
	`, workspaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var lastTransactionID string
	var activeTotal int64
	for rows.Next() {
		var id, transactionID, amountType string
		var amount, parentAmount sql.NullInt64
		var reversedAt sql.NullInt64
		var reversedBy, parentID, parentType, parentAmountType, invoiceID, accountID sql.NullString
		if err := rows.Scan(&id, &transactionID, &amountType, &amount,
			&reversedAt, &reversedBy, &parentID, &parentType, &parentAmountType,
			&parentAmount, &invoiceID, &accountID); err != nil {
			return fmt.Errorf("read refund %q in workspace %q: %w", id, workspaceID, err)
		}
		if transactionID != lastTransactionID {
			lastTransactionID, activeTotal = transactionID, 0
		}
		if amountType != "integer" || !amount.Valid || amount.Int64 <= 0 ||
			reversedAt.Valid != reversedBy.Valid || !parentID.Valid ||
			!parentType.Valid || parentType.String != "EXPENSE" ||
			!parentAmountType.Valid || parentAmountType.String != "integer" ||
			!parentAmount.Valid || parentAmount.Int64 <= 0 ||
			!invoiceID.Valid || !accountID.Valid {
			return fmt.Errorf("refund %q in workspace %q has invalid type, ownership, or reversal state", id, workspaceID)
		}
		if !reversedAt.Valid {
			if amount.Int64 > parentAmount.Int64-activeTotal {
				return fmt.Errorf("transaction %q in workspace %q has active refunds above its amount", transactionID, workspaceID)
			}
			activeTotal += amount.Int64
		}
	}
	return rows.Err()
}
