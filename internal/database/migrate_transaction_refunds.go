package database

import (
	"database/sql"
	"fmt"
)

// Migration 67 audits existing money before installing the database barriers.
// The migration runner executes this and its registration in one transaction.
func migrateTransactionRefundInvariants(tx *sql.Tx) error {
	if err := auditTransactionRefunds(tx); err != nil {
		return fmt.Errorf("audit transaction refunds: %w", err)
	}
	statements := []string{
		`DROP TRIGGER trg_transaction_refunds_validate_insert`,
		`CREATE TRIGGER trg_transaction_refunds_validate_insert
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
			WHERE t.id = NEW.transaction_id
			  AND t.workspace_id = NEW.workspace_id
			  AND t.type = 'EXPENSE'
		  )
		  OR NEW.amount_cents > COALESCE((
			SELECT t.amount - COALESCE((
				SELECT SUM(r.amount_cents) FROM transaction_refunds r
				WHERE r.transaction_id = t.id
				  AND r.workspace_id = t.workspace_id
				  AND r.reversed_at IS NULL
			), 0)
			FROM transactions t
			WHERE t.id = NEW.transaction_id AND t.workspace_id = NEW.workspace_id
		  ), 0)
		BEGIN
			SELECT RAISE(ABORT, 'invalid transaction refund');
		END`,
		`CREATE TRIGGER trg_transaction_refunds_immutable_identity
		BEFORE UPDATE OF amount_cents, workspace_id, transaction_id ON transaction_refunds
		FOR EACH ROW
		BEGIN
			SELECT RAISE(ABORT, 'transaction refund identity and amount are immutable');
		END`,
		`CREATE TRIGGER trg_transaction_refunds_no_reactivation
		BEFORE UPDATE OF reversed_at, reversed_by ON transaction_refunds
		FOR EACH ROW WHEN OLD.reversed_at IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'reversed transaction refund is immutable');
		END`,
		// In SQLite's FK cascade, the parent row is already absent when this
		// child trigger runs. A direct child DELETE still sees its parent.
		`CREATE TRIGGER trg_transaction_refunds_no_direct_delete
		BEFORE DELETE ON transaction_refunds
		FOR EACH ROW
		WHEN EXISTS (
			SELECT 1 FROM transactions t
			WHERE t.id = OLD.transaction_id AND t.workspace_id = OLD.workspace_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'direct transaction refund delete not allowed');
		END`,
		`CREATE TRIGGER trg_transactions_refund_parent_identity
		BEFORE UPDATE OF workspace_id, type, invoice_id, account_id ON transactions
		FOR EACH ROW
		WHEN EXISTS (SELECT 1 FROM transaction_refunds r WHERE r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id)
		  AND (
			NEW.workspace_id != OLD.workspace_id
			OR NEW.type != 'EXPENSE'
			OR NEW.invoice_id IS NULL
			OR NOT EXISTS (
				SELECT 1 FROM invoices i
				JOIN accounts a ON a.id = i.account_id
				WHERE i.id = NEW.invoice_id
				  AND i.account_id = NEW.account_id
				  AND a.workspace_id = NEW.workspace_id
			)
		  )
		BEGIN
			SELECT RAISE(ABORT, 'transaction refund parent identity would become invalid');
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func auditTransactionRefunds(tx *sql.Tx) error {
	// CONTROL-PLANE GLOBAL: enumerate workspace IDs only. No financial row
	// participates in choosing which workspaces receive the preflight. A refund
	// whose workspace does not exist is excluded here; migration 68's foreign
	// key check blocks that orphan before the final runtime can open.
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
		if err := auditWorkspaceTransactionRefundsBefore67(tx, workspaceID); err != nil {
			return err
		}
	}
	return nil
}

func auditWorkspaceTransactionRefundsBefore67(tx *sql.Tx, workspaceID string) error {
	rows, err := tx.Query(`
		SELECT r.id, r.workspace_id, r.transaction_id, r.amount_cents,
		       r.reversed_at, r.reversed_by,
		       t.workspace_id, t.type, t.amount, typeof(t.amount), t.invoice_id,
		       i.account_id, a.workspace_id
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
		var id, workspaceID, transactionID string
		var amount int64
		var parentAmount sql.NullInt64
		var reversedAt sql.NullInt64
		var reversedBy, parentWorkspace, parentType, parentAmountType, invoiceID, invoiceAccount, accountWorkspace sql.NullString
		if err := rows.Scan(&id, &workspaceID, &transactionID, &amount,
			&reversedAt, &reversedBy, &parentWorkspace, &parentType,
			&parentAmount, &parentAmountType, &invoiceID, &invoiceAccount, &accountWorkspace); err != nil {
			return fmt.Errorf("read transaction refund audit row: %w", err)
		}
		if transactionID != lastTransactionID {
			lastTransactionID, activeTotal = transactionID, 0
		}
		if !parentWorkspace.Valid || parentWorkspace.String != workspaceID ||
			!parentType.Valid || parentType.String != "EXPENSE" ||
			!invoiceID.Valid || !invoiceAccount.Valid ||
			!accountWorkspace.Valid || accountWorkspace.String != workspaceID ||
			amount <= 0 || reversedAt.Valid != reversedBy.Valid {
			return fmt.Errorf("refund %q has inconsistent ownership or state", id)
		}
		if !parentAmount.Valid || !parentAmountType.Valid || parentAmountType.String != "integer" || parentAmount.Int64 <= 0 {
			return fmt.Errorf("refund %q has invalid parent amount", id)
		}
		if !reversedAt.Valid {
			if amount > parentAmount.Int64-activeTotal {
				return fmt.Errorf("transaction %q has active refunds above its amount", transactionID)
			}
			activeTotal += amount
		}
	}
	return rows.Err()
}
