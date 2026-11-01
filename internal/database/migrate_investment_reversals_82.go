package database

import (
	"database/sql"
	"fmt"
)

// Schema 82: keeps accounts and investments operable while a schema 80
// reversal receipt exists. Schema 80 froze every UPDATE on the receipt's
// accounts and lot, so ordinary movements, renames, redemptions and archiving
// became impossible after an individual reversal. Receipts, purchase and
// transaction identities, deletion guards and the original transfer links stay
// untouched; only the identity-bearing columns of the parent rows remain
// frozen.

func reversal82Drops() []string {
	return []string{"trg_reversal80_accounts_update", "trg_reversal80_investments_update"}
}

func reversal82Objects() []cancellationSchemaObject {
	addTrigger := func(name, on, condition string) cancellationSchemaObject {
		q := "CREATE TRIGGER " + name + " BEFORE UPDATE ON " + on + " WHEN " + condition +
			" BEGIN SELECT RAISE(ABORT,'investment purchase reversal integrity'); END"
		return cancellationSchemaObject{"trigger", name, on, q}
	}
	return []cancellationSchemaObject{
		addTrigger("trg_reversal82_accounts_update", "accounts",
			`EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=OLD.workspace_id AND OLD.id IN(r.investment_account_id,r.origin_account_id))
 AND (OLD.id<>NEW.id OR OLD.workspace_id<>NEW.workspace_id OR OLD.type<>NEW.type)`),
		addTrigger("trg_reversal82_investments_update", "investments",
			`EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=OLD.workspace_id AND r.investment_id=OLD.id)
 AND (OLD.id<>NEW.id OR OLD.workspace_id<>NEW.workspace_id OR OLD.account_id<>NEW.account_id
  OR OLD.principal_cents<>NEW.principal_cents OR OLD.applied_date<>NEW.applied_date
  OR OLD.created_by<>NEW.created_by OR OLD.created_at<>NEW.created_at)`),
	}
}

func migrateInvestmentPurchaseReversals82(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investments")
	if err != nil || !exists {
		return err
	}
	if err := preflightInvestmentPurchaseReversals82(tx); err != nil {
		return err
	}
	var before string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&before); err != nil {
		return err
	}
	for _, name := range reversal82Drops() {
		if _, err := tx.Exec("DROP TRIGGER " + name); err != nil {
			return fmt.Errorf("drop %s: %w", name, err)
		}
	}
	for _, object := range reversal82Objects() {
		if _, err := tx.Exec(object.ddl); err != nil {
			return fmt.Errorf("create %s: %w", object.name, err)
		}
	}
	if err := validateReversal80Structure(tx, true); err != nil {
		return err
	}
	if err := validateReversal80Receipts(tx); err != nil {
		return err
	}
	if err := sqlite79Integrity(tx); err != nil {
		return err
	}
	var after string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&after); err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("migration 82 changed transactions schema")
	}
	return nil
}
