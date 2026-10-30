package database

import (
	"database/sql"
	"fmt"
)

// Phase A is append-only evidence. No backfill, source-table trigger, balance
// write or integration activation. A later phase can add an adjustment-link
// table referencing (workspace_id, investment_id, id), without rewriting history.
func migrateInvestmentReferences(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_withdrawals")
	if err != nil || !exists {
		return err
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_investments_reference_owner ON investments(workspace_id,id,account_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_valuations_reference_owner ON investment_valuations(workspace_id,investment_id,id)`,
		`CREATE TABLE IF NOT EXISTS investment_reference_revisions (
		 workspace_id TEXT NOT NULL,
		 investment_id TEXT NOT NULL,
		 account_id TEXT NOT NULL,
		 id TEXT NOT NULL,
		 reference_id TEXT NOT NULL,
		 previous_revision_id TEXT,
		 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
		 expected_version INTEGER NOT NULL CHECK(typeof(expected_version)='integer' AND expected_version>=0 AND expected_version=version-1),
		 position_id TEXT NOT NULL,
		 position_version INTEGER NOT NULL CHECK(typeof(position_version)='integer' AND position_version>0),
		 economic_date TEXT NOT NULL ` + referenceCivilDateCheck("economic_date") + `,
		 represented_cents INTEGER CHECK(represented_cents IS NULL OR (typeof(represented_cents)='integer' AND represented_cents>=0)),
		 confirmed INTEGER NOT NULL CHECK(confirmed IN (0,1)),
		 day_closed INTEGER NOT NULL CHECK(day_closed IN (0,1)),
		 confirmed_at INTEGER,
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
		 origin TEXT NOT NULL DEFAULT 'MANUAL_REFERENCE' CHECK(origin='MANUAL_REFERENCE'),
		 integration_state TEXT NOT NULL DEFAULT 'PREPARED' CHECK(integration_state='PREPARED'),
		 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
		 coverage_token TEXT NOT NULL CHECK(length(coverage_token)=64),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 PRIMARY KEY(workspace_id,investment_id,id),
		 UNIQUE(workspace_id,investment_id,version),
		 UNIQUE(workspace_id,investment_id,request_id),
		 UNIQUE(workspace_id,investment_id,id,version),
		 UNIQUE(workspace_id,investment_id,position_id,position_version),
		 FOREIGN KEY(investment_id,workspace_id) REFERENCES investments(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
		 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,position_id) REFERENCES investment_valuations(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,reference_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,previous_revision_id,expected_version) REFERENCES investment_reference_revisions(workspace_id,investment_id,id,version) ON DELETE RESTRICT,
		 CHECK((version=1 AND previous_revision_id IS NULL AND reference_id=id) OR (version>1 AND previous_revision_id IS NOT NULL)),
		 CHECK((confirmed=0 AND confirmed_at IS NULL) OR (confirmed=1 AND represented_cents IS NOT NULL AND confirmed_at IS NOT NULL AND confirmed_at=created_at AND typeof(confirmed_at)='integer'))
		)`,
		`CREATE TABLE IF NOT EXISTS investment_reference_coverage (
		 workspace_id TEXT NOT NULL,
		 investment_id TEXT NOT NULL,
		 revision_id TEXT NOT NULL,
		 operation_id TEXT NOT NULL,
		 kind TEXT NOT NULL CHECK(kind IN ('PURCHASE','WITHDRAWAL')),
		 purchase_id TEXT,
		 withdrawal_id TEXT,
		 PRIMARY KEY(workspace_id,investment_id,revision_id,kind,operation_id),
		 FOREIGN KEY(workspace_id,investment_id,revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id) REFERENCES investment_withdrawals(workspace_id,investment_id,id) ON DELETE RESTRICT,
		 CHECK((kind='PURCHASE' AND purchase_id IS NOT NULL AND purchase_id=operation_id AND withdrawal_id IS NULL) OR
		       (kind='WITHDRAWAL' AND withdrawal_id IS NOT NULL AND withdrawal_id=operation_id AND purchase_id IS NULL))
		)`,
		// Guard INSERT OR REPLACE as well as ordinary mutation of audit records.
		`CREATE TRIGGER IF NOT EXISTS trg_reference_revision_insert BEFORE INSERT ON investment_reference_revisions
		 WHEN EXISTS(SELECT 1 FROM investment_reference_revisions r WHERE r.workspace_id=NEW.workspace_id AND r.investment_id=NEW.investment_id AND
		 (r.id=NEW.id OR r.version=NEW.version OR r.request_id=NEW.request_id OR (r.position_id=NEW.position_id AND r.position_version=NEW.position_version)))
		 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_reference_revisions p WHERE p.workspace_id=NEW.workspace_id AND p.investment_id=NEW.investment_id AND p.id=NEW.previous_revision_id AND p.reference_id=NEW.reference_id AND p.version=NEW.expected_version))
		 BEGIN SELECT RAISE(ABORT,'invalid reference revision chain'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_reference_coverage_insert BEFORE INSERT ON investment_reference_coverage
		 WHEN EXISTS(SELECT 1 FROM investment_reference_coverage c WHERE c.workspace_id=NEW.workspace_id AND c.investment_id=NEW.investment_id AND c.revision_id=NEW.revision_id AND c.kind=NEW.kind AND c.operation_id=NEW.operation_id)
		 BEGIN SELECT RAISE(ABORT,'immutable reference coverage'); END`,
	}
	for _, table := range []string{"investment_reference_revisions", "investment_reference_coverage"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'immutable investment reference'); END`, table, action, action, table))
		}
	}
	statements = append(statements, `CREATE INDEX IF NOT EXISTS idx_reference_coverage_purchase ON investment_reference_coverage(workspace_id,investment_id,purchase_id)`)
	statements = append(statements, `CREATE INDEX IF NOT EXISTS idx_reference_coverage_withdrawal ON investment_reference_coverage(workspace_id,investment_id,withdrawal_id)`)
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// Gregorian validation independent of SQLite's date normalization/version.
func referenceCivilDateCheck(column string) string {
	return fmt.Sprintf(`CHECK(COALESCE(typeof(%[1]s)='text' AND length(CAST(%[1]s AS BLOB))=10 AND
	 %[1]s GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]' AND
	 CAST(substr(%[1]s,1,4) AS INTEGER) BETWEEN 1 AND 9999 AND
	 CAST(substr(%[1]s,6,2) AS INTEGER) BETWEEN 1 AND 12 AND
	 CAST(substr(%[1]s,9,2) AS INTEGER) BETWEEN 1 AND CASE CAST(substr(%[1]s,6,2) AS INTEGER)
	 WHEN 2 THEN 28+(CAST(substr(%[1]s,1,4) AS INTEGER)%%4=0 AND (CAST(substr(%[1]s,1,4) AS INTEGER)%%100!=0 OR CAST(substr(%[1]s,1,4) AS INTEGER)%%400=0))
	 WHEN 4 THEN 30 WHEN 6 THEN 30 WHEN 9 THEN 30 WHEN 11 THEN 30 ELSE 31 END,0)=1)`, column)
}
