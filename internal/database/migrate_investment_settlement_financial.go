package database

import (
	"database/sql"
	"fmt"
)

// C2A adds immutable origin evidence to the existing adjustment journal. It
// neither backfills settlements nor changes the immutable C1 state.
func migrateInvestmentSettlementFinancial(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_settlement_revisions")
	if err != nil || !exists {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS investment_settlement_applications (
 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL,
 id TEXT NOT NULL, settlement_revision_id TEXT NOT NULL, account_id TEXT NOT NULL,
 destination_account_id TEXT NOT NULL, reference_revision_id TEXT NOT NULL, financial_revision_id TEXT NOT NULL,
 settlement_version INTEGER NOT NULL CHECK(typeof(settlement_version)='integer' AND settlement_version>0),
 financial_version INTEGER NOT NULL CHECK(typeof(financial_version)='integer' AND financial_version>0),
 net_cents INTEGER NOT NULL CHECK(typeof(net_cents)='integer' AND net_cents>=0),
 patrimony_cents INTEGER NOT NULL CHECK(typeof(patrimony_cents)='integer' AND patrimony_cents>=0),
 prior_effect_cents INTEGER NOT NULL CHECK(typeof(prior_effect_cents)='integer'),
 residual_cents INTEGER NOT NULL CHECK(typeof(residual_cents)='integer'),
 adjustment_id TEXT,
 economic_date TEXT NOT NULL ` + referenceCivilDateCheck("economic_date") + `,
 economic_at INTEGER NOT NULL CHECK(typeof(economic_at)='integer' AND economic_at=unixepoch(economic_date||' 23:59:59')),
 coverage_date TEXT NOT NULL ` + referenceCivilDateCheck("coverage_date") + `,
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
 request_id TEXT NOT NULL CHECK(length(request_id)=36),
 expected_balance_version INTEGER NOT NULL CHECK(typeof(expected_balance_version)='integer' AND expected_balance_version>=0),
 expected_destination_version INTEGER NOT NULL CHECK(typeof(expected_destination_version)='integer' AND expected_destination_version>=0),
 state TEXT NOT NULL CHECK(state='FINANCIAL_APPLIED_CLASSIFICATION_PENDING'),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 proof_json TEXT NOT NULL CHECK(json_valid(proof_json)),
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,request_id),
 UNIQUE(workspace_id,investment_id,withdrawal_id),
 UNIQUE(workspace_id,investment_id,withdrawal_id,settlement_revision_id),
 UNIQUE(adjustment_id),
 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,settlement_revision_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,account_id,financial_revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,adjustment_id) REFERENCES account_balance_adjustments(workspace_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(destination_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
 CHECK((residual_cents=0 AND adjustment_id IS NULL) OR (residual_cents!=0 AND adjustment_id IS NOT NULL))
 )`,
		`CREATE TABLE IF NOT EXISTS investment_settlement_prior_effects (
 workspace_id TEXT NOT NULL, application_id TEXT NOT NULL, account_id TEXT NOT NULL,
 adjustment_id TEXT, transaction_id TEXT,
 delta_cents INTEGER NOT NULL CHECK(typeof(delta_cents)='integer' AND delta_cents!=0),
 evidence TEXT NOT NULL CHECK(length(trim(evidence)) BETWEEN 1 AND 2000),
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
 CHECK((adjustment_id IS NULL)!=(transaction_id IS NULL)),
 UNIQUE(workspace_id,adjustment_id), UNIQUE(workspace_id,transaction_id),
 FOREIGN KEY(workspace_id,application_id) REFERENCES investment_settlement_applications(workspace_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,adjustment_id) REFERENCES account_balance_adjustments(workspace_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT
 )`,
		`CREATE INDEX IF NOT EXISTS idx_settlement_application_date ON investment_settlement_applications(workspace_id,account_id,economic_at)`,
		// One row per adjustment, shared by reconstruction and trends. Prior effects
		// retain their original dates; only the newly applied residual gets this date.
		`CREATE VIEW IF NOT EXISTS investment_adjustment_economic_dates AS
 SELECT workspace_id,account_id,adjustment_id,economic_at FROM investment_financial_adjustments
 UNION ALL SELECT workspace_id,account_id,adjustment_id,economic_at FROM investment_settlement_applications WHERE adjustment_id IS NOT NULL`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_application_insert BEFORE INSERT ON investment_settlement_applications
 WHEN EXISTS(SELECT 1 FROM investment_settlement_applications WHERE workspace_id=NEW.workspace_id AND (id=NEW.id OR request_id=NEW.request_id OR (investment_id=NEW.investment_id AND withdrawal_id=NEW.withdrawal_id)))
 OR EXISTS(SELECT 1 FROM investment_financial_adjustments WHERE adjustment_id=NEW.adjustment_id)
 OR EXISTS(SELECT 1 FROM investment_settlement_prior_effects WHERE adjustment_id=NEW.adjustment_id)
 BEGIN SELECT RAISE(ABORT,'settlement already applied'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_effect_insert BEFORE INSERT ON investment_settlement_prior_effects
 WHEN EXISTS(SELECT 1 FROM investment_settlement_prior_effects WHERE workspace_id=NEW.workspace_id AND (adjustment_id=NEW.adjustment_id OR transaction_id=NEW.transaction_id))
 OR EXISTS(SELECT 1 FROM investment_financial_adjustments WHERE adjustment_id=NEW.adjustment_id)
 OR EXISTS(SELECT 1 FROM investment_settlement_applications WHERE adjustment_id=NEW.adjustment_id)
 BEGIN SELECT RAISE(ABORT,'effect already attributed'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_protect_financial_chain BEFORE INSERT ON investment_financial_revisions
 WHEN EXISTS(SELECT 1 FROM investment_settlement_applications WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id)
 BEGIN SELECT RAISE(ABORT,'applied settlement requires coordinated compensation'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_protect_reference BEFORE INSERT ON investment_reference_revisions
 WHEN EXISTS(SELECT 1 FROM investment_settlement_applications WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id)
 BEGIN SELECT RAISE(ABORT,'applied settlement reference is sealed'); END`,
	}
	for _, table := range []string{"investment_settlement_applications", "investment_settlement_prior_effects"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'immutable settlement application'); END`, table, action, action, table))
		}
	}
	// Seal only investments with an applied C2A event. Future operations remain
	// possible; retroactive economic edits need a later compensation workflow.
	for _, source := range []struct{ table, date string }{
		{"investment_purchases", "purchase_date"}, {"investment_withdrawals", "withdrawal_date"}, {"investment_valuations", "valuation_date"},
	} {
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			condition := ""
			for _, row := range []string{"OLD", "NEW"} {
				if action == "INSERT" && row == "OLD" || action == "DELETE" && row == "NEW" {
					continue
				}
				if condition != "" {
					condition += " OR "
				}
				condition += fmt.Sprintf(`EXISTS(SELECT 1 FROM investment_settlement_applications a WHERE a.workspace_id=%s.workspace_id AND a.investment_id=%s.investment_id AND %s.%s<=a.coverage_date)`, row, row, row, source.date)
			}
			statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_c2a_%s_%s BEFORE %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'settlement source is sealed'); END`, source.table, action, action, source.table, condition))
		}
	}
	// Allocation rows are mutable in the legacy I2.2 schema; seal the exact
	// economic coverage once C2A depends on them, including REPLACE attempts.
	for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
		condition := ""
		for _, row := range []string{"OLD", "NEW"} {
			if action == "INSERT" && row == "OLD" || action == "DELETE" && row == "NEW" {
				continue
			}
			if condition != "" {
				condition += " OR "
			}
			condition += fmt.Sprintf(`EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_withdrawals w ON w.workspace_id=a.workspace_id AND w.investment_id=a.investment_id WHERE w.workspace_id=%s.workspace_id AND w.investment_id=%s.investment_id AND w.id=%s.withdrawal_id AND w.withdrawal_date<=a.coverage_date)`, row, row, row)
		}
		statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_c2a_allocations_%s BEFORE %s ON investment_withdrawal_allocations WHEN %s BEGIN SELECT RAISE(ABORT,'settlement allocation is sealed'); END`, action, action, condition))
	}
	// Receipts and funding can otherwise be changed by generic transaction flows.
	protected := func(row string) string {
		return fmt.Sprintf(`
 EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_settlement_receipts r ON r.workspace_id=a.workspace_id AND r.investment_id=a.investment_id AND r.withdrawal_id=a.withdrawal_id WHERE r.workspace_id=%[1]s.workspace_id AND r.transaction_id=%[1]s.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_purchase_funding f ON f.workspace_id=a.workspace_id AND f.investment_id=a.investment_id WHERE f.workspace_id=%[1]s.workspace_id AND f.transaction_id=%[1]s.id)
 OR EXISTS(SELECT 1 FROM investment_settlement_prior_effects e WHERE e.workspace_id=%[1]s.workspace_id AND e.transaction_id=%[1]s.id)`, row)
	}
	for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
		row := "OLD"
		if action == "INSERT" {
			row = "NEW"
		}
		event := action
		if action == "UPDATE" {
			event = "UPDATE OF id,workspace_id,account_id,destination_account_id,type,status,amount,date,date_civil,paid_at,paid_date_civil"
		}
		statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_c2a_transaction_%s BEFORE %s ON transactions WHEN %s BEGIN SELECT RAISE(ABORT,'applied settlement transaction is sealed'); END`, action, event, protected(row)))
	}
	statements = append(statements, `CREATE TRIGGER IF NOT EXISTS trg_c2a_effect_refund BEFORE INSERT ON transaction_refunds
 WHEN EXISTS(SELECT 1 FROM investment_settlement_prior_effects WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id)
 BEGIN SELECT RAISE(ABORT,'applied settlement effect is sealed'); END`)
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
