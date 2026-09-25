package database

import (
	"database/sql"
	"fmt"
)

// migrateInvestmentsAndValuations creates the patrimony tables frozen for
// beta.5. The ledger stays untouched: no statement here reads or writes
// accounts or transactions, and no future investment flow may write
// accounts.current_balance. Additive only; safe to re-run via IF NOT EXISTS.
func migrateInvestmentsAndValuations(tx *sql.Tx) error {
	// Historical migration tests intentionally use partial schemas. Normal
	// application startup executes ddl first, so a real ContaBase database
	// always has these tables before v65 runs.
	for _, table := range []string{"accounts", "workspaces", "users"} {
		exists, err := tableExists(tx, table)
		if err != nil {
			return fmt.Errorf("inspect %s table: %w", table, err)
		}
		if !exists {
			return nil
		}
	}

	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS investments (
		id              TEXT    PRIMARY KEY,
		workspace_id    TEXT    NOT NULL,
		account_id      TEXT    NOT NULL,
		name            TEXT    NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 120),
		institution     TEXT    NOT NULL DEFAULT '',
		kind            TEXT    NOT NULL CHECK (typeof(kind) = 'text' AND length(trim(kind)) BETWEEN 1 AND 40),
		principal_cents INTEGER NOT NULL CHECK (typeof(principal_cents) = 'integer' AND principal_cents > 0),
		tax_regime      TEXT    NOT NULL CHECK (tax_regime IN ('IR_REGRESSIVO', 'ISENTO_PF', 'MANUAL')),
		applied_date    TEXT    NOT NULL ` + civilDateCheck("applied_date", false) + `,
		maturity_date   TEXT    NULL ` + civilDateCheck("maturity_date", true) + `,
		yield_note      TEXT    NOT NULL DEFAULT '',
		status          TEXT    NOT NULL DEFAULT 'ATIVA' CHECK (status IN ('ATIVA', 'RESGATADA')),
		redeemed_date   TEXT    NULL ` + civilDateCheck("redeemed_date", true) + `,
		redeemed_by     TEXT    NULL,
		archived_at     INTEGER NULL,
		created_by      TEXT    NOT NULL,
		created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
		updated_at      INTEGER NOT NULL DEFAULT (unixepoch()),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (account_id)   REFERENCES accounts(id)   ON DELETE RESTRICT,
		FOREIGN KEY (created_by)   REFERENCES users(id)      ON DELETE RESTRICT,
		FOREIGN KEY (redeemed_by)  REFERENCES users(id)      ON DELETE RESTRICT,
		CHECK (
			(status = 'ATIVA'     AND redeemed_date IS NULL     AND redeemed_by IS NULL)
			OR
			(status = 'RESGATADA' AND redeemed_date IS NOT NULL AND redeemed_by IS NOT NULL)
		),
		CHECK (archived_at IS NULL OR status = 'RESGATADA')
	)`); err != nil {
		return fmt.Errorf("create investments table: %w", err)
	}

	// Must exist before investment_valuations is created: its composite
	// foreign key (investment_id, workspace_id) resolves against this unique
	// index (same pattern as idx_accounts_id_workspace in migration 64).
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_investments_id_workspace ON investments(id, workspace_id)`); err != nil {
		return fmt.Errorf("create investments identity index: %w", err)
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_investments_workspace_status ON investments(workspace_id, status, archived_at)`); err != nil {
		return fmt.Errorf("create investments status index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_investments_workspace_account ON investments(workspace_id, account_id)`); err != nil {
		return fmt.Errorf("create investments account index: %w", err)
	}

	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS investment_valuations (
		id             TEXT    PRIMARY KEY,
		workspace_id   TEXT    NOT NULL,
		investment_id  TEXT    NOT NULL,
		valuation_date TEXT    NOT NULL ` + civilDateCheck("valuation_date", false) + `,
		amount_cents   INTEGER NOT NULL CHECK (typeof(amount_cents) = 'integer' AND amount_cents >= 0),
		created_by     TEXT    NOT NULL,
		created_at     INTEGER NOT NULL DEFAULT (unixepoch()),
		updated_by     TEXT    NULL,
		updated_at     INTEGER NULL,
		UNIQUE (workspace_id, investment_id, valuation_date),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (investment_id, workspace_id) REFERENCES investments(id, workspace_id) ON DELETE CASCADE,
		FOREIGN KEY (created_by)   REFERENCES users(id) ON DELETE RESTRICT,
		FOREIGN KEY (updated_by)   REFERENCES users(id) ON DELETE RESTRICT,
		CHECK (
			(updated_by IS NULL     AND updated_at IS NULL)
			OR
			(updated_by IS NOT NULL AND updated_at IS NOT NULL)
		)
	)`); err != nil {
		return fmt.Errorf("create investment_valuations table: %w", err)
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_investment_valuations_workspace_date ON investment_valuations(workspace_id, valuation_date)`); err != nil {
		return fmt.Errorf("create investment valuations date index: %w", err)
	}

	statements := []string{
		`CREATE TRIGGER IF NOT EXISTS trg_investments_validate_insert
		BEFORE INSERT ON investments
		FOR EACH ROW
		WHEN NOT EXISTS (
			SELECT 1
			FROM accounts a
			WHERE a.id = NEW.account_id
			  AND a.workspace_id = NEW.workspace_id
			  AND a.type = 'INVESTMENT'
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid investment account');
		END`,
		// One investment is one lot: the historical principal, origin account,
		// application date and authorship can never be rewritten. Everything
		// not listed here (name, institution, kind, tax_regime, maturity_date,
		// yield_note, status transitions, archived_at, updated_at) stays
		// editable through its own domain flow.
		`CREATE TRIGGER IF NOT EXISTS trg_investments_immutable_keys
		BEFORE UPDATE ON investments
		FOR EACH ROW
		WHEN NEW.id              IS NOT OLD.id
		  OR NEW.workspace_id    IS NOT OLD.workspace_id
		  OR NEW.account_id      IS NOT OLD.account_id
		  OR NEW.principal_cents IS NOT OLD.principal_cents
		  OR NEW.applied_date    IS NOT OLD.applied_date
		  OR NEW.created_by      IS NOT OLD.created_by
		  OR NEW.created_at      IS NOT OLD.created_at
		BEGIN
			SELECT RAISE(ABORT, 'investment identity is immutable');
		END`,
		// A valuation requires a live investment in the same workspace, on or
		// after the application date. Redeemed investments are frozen.
		`CREATE TRIGGER IF NOT EXISTS trg_investment_valuations_validate_insert
		BEFORE INSERT ON investment_valuations
		FOR EACH ROW
		WHEN NOT EXISTS (
			SELECT 1
			FROM investments i
			WHERE i.id = NEW.investment_id
			  AND i.workspace_id = NEW.workspace_id
			  AND i.status = 'ATIVA'
			  AND NEW.valuation_date >= i.applied_date
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid investment valuation');
		END`,
		// Same predicate for the UPSERT correction path (DO UPDATE fires the
		// UPDATE trigger, not the INSERT one).
		`CREATE TRIGGER IF NOT EXISTS trg_investment_valuations_validate_update
		BEFORE UPDATE ON investment_valuations
		FOR EACH ROW
		WHEN NOT EXISTS (
			SELECT 1
			FROM investments i
			WHERE i.id = NEW.investment_id
			  AND i.workspace_id = NEW.workspace_id
			  AND i.status = 'ATIVA'
			  AND NEW.valuation_date >= i.applied_date
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid investment valuation');
		END`,
		// Only the correction triplet may change: amount_cents, updated_by,
		// updated_at. Identity and first authorship stay frozen. amount_cents
		// is deliberately mutable — same-day corrections rewrite it.
		`CREATE TRIGGER IF NOT EXISTS trg_investment_valuations_immutable_keys
		BEFORE UPDATE ON investment_valuations
		FOR EACH ROW
		WHEN NEW.id             IS NOT OLD.id
		  OR NEW.workspace_id   IS NOT OLD.workspace_id
		  OR NEW.investment_id  IS NOT OLD.investment_id
		  OR NEW.valuation_date IS NOT OLD.valuation_date
		  OR NEW.created_by     IS NOT OLD.created_by
		  OR NEW.created_at     IS NOT OLD.created_at
		BEGIN
			SELECT RAISE(ABORT, 'investment valuation identity is immutable');
		END`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("create investments trigger: %w", err)
		}
	}
	return nil
}

// civilDateCheck returns the structural YYYY-MM-DD CHECK used by the project
// for civil date columns (same GLOB discipline as the civil-month checks).
// Semantic calendar validation (real month lengths, leap years) lives in
// internal/civildate and is enforced by the application layer.
func civilDateCheck(column string, nullable bool) string {
	structural := fmt.Sprintf(`(
		length(%[1]s) = 10 AND
		substr(%[1]s, 5, 1) = '-' AND
		substr(%[1]s, 8, 1) = '-' AND
		substr(%[1]s, 1, 4) NOT GLOB '*[^0-9]*' AND
		substr(%[1]s, 6, 2) NOT GLOB '*[^0-9]*' AND
		substr(%[1]s, 9, 2) NOT GLOB '*[^0-9]*' AND
		CAST(substr(%[1]s, 6, 2) AS INTEGER) BETWEEN 1 AND 12 AND
		CAST(substr(%[1]s, 9, 2) AS INTEGER) BETWEEN 1 AND 31
	)`, column)
	if nullable {
		return fmt.Sprintf("CHECK (%s IS NULL OR (%s))", column, structural)
	}
	return fmt.Sprintf("CHECK (%s)", structural)
}
