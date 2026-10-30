package database

import (
	"database/sql"
	"fmt"
)

// Only evidence and links to the existing balance adjustment journal. No
// backfill, activation, source triggers, or second balance ledger.
func migrateInvestmentFinancialIntegration(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_reference_revisions")
	if err != nil || !exists {
		return err
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_transactions_funding_owner ON transactions(workspace_id,id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_adjustments_financial_owner ON account_balance_adjustments(workspace_id,account_id,id)`,
		`CREATE TABLE IF NOT EXISTS investment_financial_revisions (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 id TEXT NOT NULL, reference_revision_id TEXT NOT NULL, previous_id TEXT,
		 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
		 chain_json TEXT NOT NULL CHECK(json_valid(chain_json)),
		 PRIMARY KEY(workspace_id,investment_id,id),
		 UNIQUE(workspace_id,investment_id,version), UNIQUE(workspace_id,investment_id,request_id),
		 UNIQUE(workspace_id,investment_id,account_id,id),
		 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,previous_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 CHECK((version=1 AND previous_id IS NULL) OR (version>1 AND previous_id IS NOT NULL))
		)`,
		`CREATE TABLE IF NOT EXISTS investment_financial_adjustments (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 revision_id TEXT NOT NULL, adjustment_id TEXT NOT NULL, position_id TEXT NOT NULL,
		 reverses_id TEXT, economic_date TEXT NOT NULL ` + referenceCivilDateCheck("economic_date") + `,
		 economic_at INTEGER NOT NULL CHECK(typeof(economic_at)='integer' AND economic_at=unixepoch(economic_date||' 23:59:59')),
		 PRIMARY KEY(workspace_id,account_id,adjustment_id), UNIQUE(adjustment_id), UNIQUE(workspace_id,reverses_id),
		 UNIQUE(workspace_id,investment_id,account_id,adjustment_id),
		 FOREIGN KEY(workspace_id,investment_id,account_id,revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,account_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,account_id,adjustment_id) REFERENCES account_balance_adjustments(workspace_id,account_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,account_id,reverses_id) REFERENCES investment_financial_adjustments(workspace_id,investment_id,account_id,adjustment_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,position_id) REFERENCES investment_valuations(workspace_id,investment_id,id) ON DELETE RESTRICT
		)`,
		// Amount allocations can share a transfer or combine transfers. The
		// writer validates aggregate capacity under the account writer lock.
		`CREATE TABLE IF NOT EXISTS investment_purchase_funding (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, purchase_id TEXT NOT NULL,
		 transaction_id TEXT NOT NULL, revision_id TEXT NOT NULL,
		 amount_cents INTEGER NOT NULL CHECK(typeof(amount_cents)='integer' AND amount_cents>0), snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
		 PRIMARY KEY(workspace_id,investment_id,purchase_id,transaction_id),
		 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_purchase_funding_source ON investment_purchase_funding(workspace_id,transaction_id)`,
		`CREATE INDEX IF NOT EXISTS idx_investment_adjustments_date ON investment_financial_adjustments(workspace_id,account_id,economic_at)`,
		`CREATE TRIGGER IF NOT EXISTS trg_financial_revision_chain BEFORE INSERT ON investment_financial_revisions
		 WHEN EXISTS(SELECT 1 FROM investment_financial_revisions WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id AND (id=NEW.id OR request_id=NEW.request_id))
		 OR NEW.version != (SELECT COALESCE(MAX(version),0)+1 FROM investment_financial_revisions WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id)
		 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_financial_revisions p WHERE p.workspace_id=NEW.workspace_id AND p.investment_id=NEW.investment_id AND p.id=NEW.previous_id AND p.version=NEW.version-1 AND p.reference_revision_id=NEW.reference_revision_id))
		 BEGIN SELECT RAISE(ABORT,'invalid financial revision chain'); END`,
	}
	for _, table := range []string{"investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'immutable investment integration'); END`, table, action, action, table))
		}
	}
	// Explicit REPLACE guards, independent of recursive_triggers settings.
	statements = append(statements,
		`CREATE TRIGGER IF NOT EXISTS trg_financial_adjustment_insert BEFORE INSERT ON investment_financial_adjustments WHEN EXISTS(SELECT 1 FROM investment_financial_adjustments WHERE adjustment_id=NEW.adjustment_id OR (workspace_id=NEW.workspace_id AND reverses_id=NEW.reverses_id)) BEGIN SELECT RAISE(ABORT,'immutable integration link'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_purchase_funding_insert BEFORE INSERT ON investment_purchase_funding WHEN EXISTS(SELECT 1 FROM investment_purchase_funding WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id AND investment_id=NEW.investment_id AND purchase_id=NEW.purchase_id) BEGIN SELECT RAISE(ABORT,'immutable purchase funding'); END`)
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
