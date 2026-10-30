package database

import (
	"database/sql"
	"fmt"
)

// C1 stores evidence only. Historical withdrawals are never backfilled or
// reclassified. Receipt claims are immutable reservations, not a balance ledger.
func migrateInvestmentSettlements(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_financial_revisions")
	if err != nil || !exists {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS investment_settlement_revisions (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL,
		 id TEXT NOT NULL, previous_id TEXT, version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
		 account_id TEXT NOT NULL, destination_account_id TEXT NOT NULL,
		 economic_date TEXT NOT NULL ` + referenceCivilDateCheck("economic_date") + `,
		 gross_cents INTEGER CHECK(gross_cents IS NULL OR (typeof(gross_cents)='integer' AND gross_cents>=0)),
		 ir_cents INTEGER CHECK(ir_cents IS NULL OR (typeof(ir_cents)='integer' AND ir_cents>=0)),
		 iof_cents INTEGER CHECK(iof_cents IS NULL OR (typeof(iof_cents)='integer' AND iof_cents>=0)),
		 fees_cents INTEGER CHECK(fees_cents IS NULL OR (typeof(fees_cents)='integer' AND fees_cents>=0)),
		 net_cents INTEGER NOT NULL CHECK(typeof(net_cents)='integer' AND net_cents>=0),
		 principal_cents INTEGER CHECK(principal_cents IS NULL OR (typeof(principal_cents)='integer' AND principal_cents>=0)),
		 result_cents INTEGER CHECK(result_cents IS NULL OR typeof(result_cents)='integer'),
		 patrimony_cents INTEGER CHECK(patrimony_cents IS NULL OR (typeof(patrimony_cents)='integer' AND patrimony_cents>=0)),
		 patrimony_evidence TEXT NOT NULL, receipt_evidence TEXT NOT NULL,
		 state TEXT NOT NULL CHECK(state IN ('PENDENTE','REGISTRADA','CONCILIADA')),
		 effects_state TEXT NOT NULL DEFAULT 'NOT_ACTIVATED' CHECK(effects_state='NOT_ACTIVATED'),
		 reference_revision_id TEXT, financial_revision_id TEXT,
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
		 withdrawal_json TEXT NOT NULL CHECK(json_valid(withdrawal_json)),
		 PRIMARY KEY(workspace_id,investment_id,withdrawal_id,id),
		 UNIQUE(workspace_id,investment_id,withdrawal_id,version), UNIQUE(workspace_id,request_id),
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id) REFERENCES investment_withdrawals(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
		 FOREIGN KEY(destination_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,previous_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,financial_revision_id) REFERENCES investment_financial_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 CHECK((version=1 AND previous_id IS NULL) OR (version>1 AND previous_id IS NOT NULL)),
		 CHECK((principal_cents IS NULL AND result_cents IS NULL) OR (principal_cents IS NOT NULL AND result_cents IS NOT NULL AND result_cents=net_cents-principal_cents)),
		 CHECK(patrimony_cents IS NULL OR length(trim(patrimony_evidence))>0),
		 CHECK(state='PENDENTE' OR (gross_cents IS NOT NULL AND ir_cents IS NOT NULL AND iof_cents IS NOT NULL AND fees_cents IS NOT NULL AND patrimony_cents IS NOT NULL)),
		 CHECK(state!='CONCILIADA' OR length(trim(receipt_evidence))>0),
		 CHECK(gross_cents IS NULL OR ir_cents IS NULL OR iof_cents IS NULL OR fees_cents IS NULL OR
		  (ir_cents<=gross_cents AND iof_cents<=gross_cents-ir_cents AND fees_cents<=gross_cents-ir_cents-iof_cents AND net_cents=gross_cents-ir_cents-iof_cents-fees_cents))
		)`,
		`CREATE TABLE IF NOT EXISTS investment_settlement_receipts (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL, revision_id TEXT NOT NULL,
		 transaction_id TEXT NOT NULL, source_account_id TEXT, destination_account_id TEXT NOT NULL,
		 amount_cents INTEGER NOT NULL CHECK(typeof(amount_cents)='integer' AND amount_cents>0),
		 evidence TEXT NOT NULL CHECK(length(trim(evidence)) BETWEEN 1 AND 2000),
		 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
		 PRIMARY KEY(workspace_id,investment_id,withdrawal_id,transaction_id),
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,revision_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(source_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(destination_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_settlement_receipt_source ON investment_settlement_receipts(workspace_id,transaction_id)`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_chain BEFORE INSERT ON investment_settlement_revisions
		 WHEN EXISTS(SELECT 1 FROM investment_settlement_revisions WHERE workspace_id=NEW.workspace_id AND (request_id=NEW.request_id OR id=NEW.id))
		 OR NEW.version!=(SELECT COALESCE(MAX(version),0)+1 FROM investment_settlement_revisions WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id AND withdrawal_id=NEW.withdrawal_id)
		 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_settlement_revisions p WHERE p.workspace_id=NEW.workspace_id AND p.investment_id=NEW.investment_id AND p.withdrawal_id=NEW.withdrawal_id AND p.id=NEW.previous_id AND p.version=NEW.version-1 AND p.state!='CONCILIADA'))
		 BEGIN SELECT RAISE(ABORT,'invalid settlement revision chain'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_receipt_insert BEFORE INSERT ON investment_settlement_receipts
		 WHEN EXISTS(SELECT 1 FROM investment_settlement_receipts WHERE workspace_id=NEW.workspace_id AND investment_id=NEW.investment_id AND withdrawal_id=NEW.withdrawal_id AND transaction_id=NEW.transaction_id)
		 BEGIN SELECT RAISE(ABORT,'immutable settlement receipt'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_settlement_receipt_capacity BEFORE INSERT ON investment_settlement_receipts
		 WHEN NEW.amount_cents > (SELECT amount FROM transactions WHERE workspace_id=NEW.workspace_id AND id=NEW.transaction_id) -
		 COALESCE((SELECT SUM(amount_cents) FROM investment_settlement_receipts WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id),0)
		 OR EXISTS(SELECT 1 FROM investment_settlement_receipts WHERE workspace_id=NEW.workspace_id AND transaction_id=NEW.transaction_id AND snapshot_json!=NEW.snapshot_json)
		 BEGIN SELECT RAISE(ABORT,'settlement receipt capacity or evidence conflict'); END`,
	}
	for _, table := range []string{"investment_settlement_revisions", "investment_settlement_receipts"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'immutable settlement evidence'); END`, table, action, action, table))
		}
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
