package database

import "database/sql"

func migrateAccountBalanceAdjustments(tx *sql.Tx) error {
	// Some legacy compatibility fixtures contain only auth tables.
	exists, err := tableExists(tx, "accounts")
	if err != nil || !exists {
		return err
	}
	if err := addColumnIfMissing(tx, "accounts", "balance_version", "INTEGER NOT NULL DEFAULT 0 CHECK (typeof(balance_version) = 'integer' AND balance_version >= 0)"); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_id_workspace ON accounts(id, workspace_id)`,
		`CREATE TABLE IF NOT EXISTS account_balance_adjustments (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
			account_id TEXT NOT NULL,
			created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
			created_at INTEGER NOT NULL,
			expected_version INTEGER NOT NULL CHECK (typeof(expected_version) = 'integer' AND expected_version >= 0),
			before_balance INTEGER NOT NULL CHECK (typeof(before_balance) = 'integer'),
			after_balance INTEGER NOT NULL CHECK (typeof(after_balance) = 'integer'),
			delta_cents INTEGER NOT NULL CHECK (typeof(delta_cents) = 'integer' AND delta_cents != 0),
			reason TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 500),
			UNIQUE (workspace_id, account_id, expected_version),
			FOREIGN KEY (account_id, workspace_id) REFERENCES accounts(id, workspace_id) ON DELETE CASCADE,
			CHECK (typeof(after_balance - before_balance) = 'integer' AND delta_cents = after_balance - before_balance)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_balance_adjustments_history ON account_balance_adjustments(workspace_id, account_id, created_at DESC)`,
		`CREATE TRIGGER IF NOT EXISTS trg_accounts_balance_version_limit
		BEFORE UPDATE OF current_balance ON accounts
		WHEN OLD.balance_version >= 9223372036854775807
		BEGIN SELECT RAISE(ABORT, 'account balance version exhausted'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_accounts_balance_version
		AFTER UPDATE OF current_balance ON accounts
		BEGIN
			UPDATE accounts SET balance_version = OLD.balance_version + 1 WHERE id = NEW.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS trg_balance_adjustments_validate
		BEFORE INSERT ON account_balance_adjustments
		WHEN NOT EXISTS (
			SELECT 1 FROM accounts a WHERE a.id = NEW.account_id AND a.workspace_id = NEW.workspace_id
			AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
			AND a.balance_version = NEW.expected_version AND a.current_balance = NEW.before_balance
		)
		BEGIN SELECT RAISE(ABORT, 'stale account balance adjustment'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_balance_adjustments_immutable
		BEFORE UPDATE ON account_balance_adjustments
		BEGIN SELECT RAISE(ABORT, 'account balance adjustment is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_balance_adjustments_no_delete
		BEFORE DELETE ON account_balance_adjustments
		WHEN EXISTS (SELECT 1 FROM accounts WHERE id = OLD.account_id)
		AND EXISTS (SELECT 1 FROM workspaces WHERE id = OLD.workspace_id)
		BEGIN SELECT RAISE(ABORT, 'account balance adjustment is immutable'); END`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
