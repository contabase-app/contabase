package database

import (
	"database/sql"
	"fmt"

	"github.com/contabase-app/contabase/internal/civildate"
)

// Additive conversion: investments and positions are never rewritten. Editable
// legacy conditions are deliberately NOT claimed as original purchase terms.
func migrateInvestmentPurchases(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investments")
	if err != nil || !exists {
		return err // historical partial-schema fixtures
	}
	if err := assertNoForeignKeyViolations(tx); err != nil {
		return fmt.Errorf("investment purchases conversion: foreign keys: %w", err)
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS investment_purchases (
		id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		investment_id TEXT NOT NULL,
		purchase_date TEXT NOT NULL ` + civilDateCheck("purchase_date", false) + `,
		principal_cents INTEGER NOT NULL CHECK (typeof(principal_cents) = 'integer' AND principal_cents > 0),
		note_reference TEXT NOT NULL DEFAULT '' CHECK (length(note_reference) <= 120),
		kind TEXT,
		institution TEXT,
		maturity_date TEXT ` + civilDateCheck("maturity_date", true) + `,
		tax_regime TEXT CHECK (tax_regime IN ('IR_REGRESSIVO', 'ISENTO_PF', 'MANUAL')),
		conditions_note TEXT,
		conditions_source TEXT NOT NULL CHECK (conditions_source IN ('LEGACY_UNKNOWN', 'RECORDED')),
		is_initial INTEGER NOT NULL CHECK (is_initial IN (0, 1)),
		created_by TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()),
		PRIMARY KEY (workspace_id, id),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (investment_id, workspace_id) REFERENCES investments(id, workspace_id) ON DELETE CASCADE,
		FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE RESTRICT,
		CHECK (maturity_date IS NULL OR maturity_date >= purchase_date),
		CHECK ((conditions_source = 'LEGACY_UNKNOWN' AND is_initial = 1 AND kind IS NULL AND institution IS NULL AND maturity_date IS NULL AND tax_regime IS NULL AND conditions_note IS NULL)
			OR (conditions_source = 'RECORDED' AND kind IS NOT NULL AND length(trim(kind)) BETWEEN 1 AND 40 AND institution IS NOT NULL AND tax_regime IS NOT NULL AND conditions_note IS NOT NULL))
	);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_investment_purchases_initial ON investment_purchases(workspace_id, investment_id) WHERE is_initial = 1;
	CREATE INDEX IF NOT EXISTS idx_investment_purchases_date ON investment_purchases(workspace_id, investment_id, purchase_date, id);`)
	if err != nil {
		return fmt.Errorf("create investment purchases: %w", err)
	}
	// CONTROL-PLANE GLOBAL: enumerate only workspace identities, then convert
	// each tenant under an explicit workspace predicate in this same transaction.
	rows, err := tx.Query(`SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return err
	}
	var workspaces []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		workspaces = append(workspaces, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, workspace := range workspaces {
		rows, err := tx.Query(`SELECT applied_date FROM investments WHERE workspace_id = ?`, workspace)
		if err != nil {
			return err
		}
		for rows.Next() {
			var date string
			if err := rows.Scan(&date); err != nil {
				rows.Close()
				return err
			}
			if _, err := civildate.ParseCivilDateInput(date); err != nil {
				rows.Close()
				return fmt.Errorf("investment purchases conversion: invalid legacy civil date")
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO investment_purchases
			(id, workspace_id, investment_id, purchase_date, principal_cents, conditions_source, is_initial, created_by, created_at)
			SELECT 'initial:' || i.id, i.workspace_id, i.id, i.applied_date, i.principal_cents, 'LEGACY_UNKNOWN', 1, i.created_by, i.created_at
			FROM investments i WHERE i.workspace_id = ? AND NOT EXISTS (
				SELECT 1 FROM investment_purchases p WHERE p.workspace_id = ? AND p.investment_id = i.id AND p.is_initial = 1
			)`, workspace, workspace); err != nil {
			return fmt.Errorf("convert initial investment purchases: %w", err)
		}
	}
	// Three local mechanisms: validate admission (including replacement and
	// checked total), freeze historical purchases, and atomically create the
	// initial purchase for every new investment, including direct SQL inserts.
	_, err = tx.Exec(`CREATE TRIGGER IF NOT EXISTS trg_investment_purchases_validate_insert
	BEFORE INSERT ON investment_purchases FOR EACH ROW
	WHEN EXISTS (SELECT 1 FROM investment_purchases p WHERE p.workspace_id = NEW.workspace_id AND (p.id = NEW.id OR (NEW.is_initial = 1 AND p.investment_id = NEW.investment_id AND p.is_initial = 1)))
	OR NOT EXISTS (SELECT 1 FROM investments i WHERE i.workspace_id = NEW.workspace_id AND i.id = NEW.investment_id
		AND ((NEW.is_initial = 1 AND NEW.purchase_date = i.applied_date AND NEW.principal_cents = i.principal_cents AND NEW.created_by = i.created_by AND NEW.created_at = i.created_at)
		OR (NEW.is_initial = 0 AND NEW.conditions_source = 'RECORDED' AND i.status = 'ATIVA' AND i.archived_at IS NULL AND NEW.purchase_date >= i.applied_date)))
	OR COALESCE((SELECT SUM(p.principal_cents) FROM investment_purchases p WHERE p.workspace_id = NEW.workspace_id AND p.investment_id = NEW.investment_id), 0) > 9223372036854775807 - NEW.principal_cents
	BEGIN SELECT RAISE(ABORT, 'invalid investment purchase'); END;
	CREATE TRIGGER IF NOT EXISTS trg_investment_purchases_immutable
	BEFORE UPDATE ON investment_purchases FOR EACH ROW
	BEGIN SELECT RAISE(ABORT, 'investment purchase is immutable'); END;
	CREATE TRIGGER IF NOT EXISTS trg_investments_initial_purchase
	AFTER INSERT ON investments FOR EACH ROW
	BEGIN
		INSERT INTO investment_purchases (id, workspace_id, investment_id, purchase_date, principal_cents, kind, institution, maturity_date, tax_regime, conditions_note, conditions_source, is_initial, created_by, created_at)
		VALUES ('initial:' || NEW.id, NEW.workspace_id, NEW.id, NEW.applied_date, NEW.principal_cents, NEW.kind, NEW.institution, NEW.maturity_date, NEW.tax_regime, NEW.yield_note, 'RECORDED', 1, NEW.created_by, NEW.created_at);
	END;`)
	return err
}
