package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// Adoption records declarations, never financial entries. No backfill or source
// mutation is performed. 69 remains reserved; migrations 72-76 remain untouched.
func migrateInvestmentAdoption(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_reference_revisions")
	if err != nil || !exists {
		return err
	}
	statements := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_reference_adoption_owner ON investment_reference_revisions(workspace_id,investment_id,account_id,id)`,
		`CREATE TABLE IF NOT EXISTS investment_adoption_confirmations (
 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(typeof(version)='integer' AND version>0),
 previous_id TEXT, previous_version INTEGER NOT NULL CHECK(previous_version=version-1),
 cut_date TEXT NOT NULL ` + referenceCivilDateCheck("cut_date") + `,
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 request_id TEXT NOT NULL CHECK(length(request_id)=36), reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
 evidence_version INTEGER NOT NULL CHECK(evidence_version=1), declaration_version INTEGER NOT NULL CHECK(declaration_version=1),
 evidence_token TEXT NOT NULL CHECK(length(evidence_token)=64), source_token TEXT NOT NULL CHECK(length(source_token)=64),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json)),
 result_json TEXT NOT NULL ` + adoptionReceiptJSONCheck() + `,
 integration_admission TEXT NOT NULL DEFAULT 'PENDING' CHECK(integration_admission='PENDING'),
 financial_effect_cents INTEGER NOT NULL DEFAULT 0 CHECK(typeof(financial_effect_cents)='integer' AND financial_effect_cents=0),
 PRIMARY KEY(workspace_id,account_id,id), UNIQUE(workspace_id,account_id,version), UNIQUE(workspace_id,account_id,request_id),
 UNIQUE(workspace_id,account_id,id,version),
 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,previous_id,previous_version) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id,version) ON DELETE RESTRICT,
 CHECK((version=1 AND previous_id IS NULL) OR (version>1 AND previous_id IS NOT NULL))
 )`,
		`CREATE TABLE IF NOT EXISTS investment_adoption_items (
 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, confirmation_id TEXT NOT NULL,
 investment_id TEXT NOT NULL, position_id TEXT NOT NULL, reference_revision_id TEXT NOT NULL,
 represented_cents INTEGER NOT NULL CHECK(typeof(represented_cents)='integer' AND represented_cents>=0),
 economic_identity TEXT NOT NULL CHECK(length(trim(economic_identity)) BETWEEN 1 AND 200),
 identity_basis TEXT NOT NULL CHECK(length(trim(identity_basis)) BETWEEN 1 AND 500),
 PRIMARY KEY(workspace_id,investment_id), UNIQUE(workspace_id,account_id,economic_identity),
 FOREIGN KEY(workspace_id,account_id,confirmation_id) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,position_id) REFERENCES investment_valuations(workspace_id,investment_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,account_id,reference_revision_id) REFERENCES investment_reference_revisions(workspace_id,investment_id,account_id,id) ON DELETE RESTRICT
 )`,
		`CREATE INDEX IF NOT EXISTS idx_adoption_items_confirmation ON investment_adoption_items(workspace_id,account_id,confirmation_id)`,
		// Only authorized, well-formed requests that finish with a domain rejection
		// enter this journal. Infrastructure/commit failures roll back everything.
		`CREATE TABLE IF NOT EXISTS investment_adoption_attempts (
 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL, request_id TEXT NOT NULL CHECK(length(request_id)=36),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
 code TEXT NOT NULL CHECK(code IN ('CONFLICT','VALIDATION','NOT_FOUND','INVALID_STATUS','ADMISSION_PENDING')),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), conflicting_confirmation_id TEXT,
 PRIMARY KEY(workspace_id,account_id,id),
 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,account_id,conflicting_confirmation_id) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id) ON DELETE RESTRICT
 )`,
		`CREATE TRIGGER IF NOT EXISTS trg_adoption_confirmation_insert BEFORE INSERT ON investment_adoption_confirmations
 WHEN EXISTS(SELECT 1 FROM investment_adoption_confirmations c WHERE c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND (c.id=NEW.id OR c.version=NEW.version OR c.request_id=NEW.request_id))
 OR NEW.version!=(SELECT COALESCE(MAX(version),0)+1 FROM investment_adoption_confirmations WHERE workspace_id=NEW.workspace_id AND account_id=NEW.account_id)
 OR (NEW.version>1 AND NOT EXISTS(SELECT 1 FROM investment_adoption_confirmations c WHERE c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND c.id=NEW.previous_id AND c.version=NEW.previous_version AND c.cut_date=NEW.cut_date))
 BEGIN SELECT RAISE(ABORT,'invalid adoption chain'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_adoption_item_insert BEFORE INSERT ON investment_adoption_items
 WHEN EXISTS(SELECT 1 FROM investment_adoption_items i WHERE i.workspace_id=NEW.workspace_id AND (i.investment_id=NEW.investment_id OR (i.account_id=NEW.account_id AND i.economic_identity=NEW.economic_identity)))
 OR NOT EXISTS(SELECT 1 FROM investment_reference_revisions r JOIN investment_adoption_confirmations c ON c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND c.id=NEW.confirmation_id
 WHERE r.workspace_id=NEW.workspace_id AND r.investment_id=NEW.investment_id AND r.account_id=NEW.account_id AND r.id=NEW.reference_revision_id AND r.position_id=NEW.position_id AND r.economic_date=c.cut_date AND r.confirmed=1 AND r.represented_cents=NEW.represented_cents AND r.represented_cents=json_extract(r.snapshot_json,'$.Position.amount_cents'))
 BEGIN SELECT RAISE(ABORT,'invalid adoption item'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_adoption_attempt_insert BEFORE INSERT ON investment_adoption_attempts
 WHEN EXISTS(SELECT 1 FROM investment_adoption_attempts a WHERE a.workspace_id=NEW.workspace_id AND a.account_id=NEW.account_id AND a.id=NEW.id)
 BEGIN SELECT RAISE(ABORT,'immutable adoption attempt'); END`,
		// Even an identical new A revision would supersede the exact adopted revision.
		// Replays do not INSERT and remain supported by the public service wrapper.
		`CREATE TRIGGER IF NOT EXISTS trg_adoption_reference_seal BEFORE INSERT ON investment_reference_revisions
 WHEN EXISTS(SELECT 1 FROM investment_adoption_items i WHERE i.workspace_id=NEW.workspace_id AND i.investment_id=NEW.investment_id)
 BEGIN SELECT RAISE(ABORT,'adopted reference sealed'); END`,
	}
	for _, table := range []string{"investment_adoption_confirmations", "investment_adoption_items", "investment_adoption_attempts"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'immutable investment adoption'); END`, table, action, action, table))
		}
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

// Only row-local facts are checked here. Items are inserted after the header;
// the service validates their exact historical set in the same read snapshot.
func adoptionReceiptJSONCheck() string {
	checks := []string{`json_type(result_json)='object'`}
	for _, field := range []struct{ name, column, kind string }{
		{"ID", "id", "text"}, {"WorkspaceID", "workspace_id", "text"}, {"AccountID", "account_id", "text"},
		{"Version", "version", "integer"}, {"CutDate", "cut_date", "text"}, {"CreatedBy", "created_by", "text"},
		{"CreatedAt", "created_at", "integer"}, {"RequestID", "request_id", "text"}, {"Reason", "reason", "text"},
		{"IntegrationAdmission", "integration_admission", "text"}, {"FinancialEffectCents", "financial_effect_cents", "integer"},
		{"Declaration.WorkspaceID", "workspace_id", "text"}, {"Declaration.AccountID", "account_id", "text"},
		{"Declaration.CutDate", "cut_date", "text"}, {"Declaration.CreatedBy", "created_by", "text"},
		{"Declaration.RequestID", "request_id", "text"}, {"Declaration.Reason", "reason", "text"},
		{"Declaration.ExpectedAdoptionVersion", "previous_version", "integer"},
		{"Declaration.EvidenceVersion", "evidence_version", "integer"}, {"Declaration.DeclarationVersion", "declaration_version", "integer"},
		{"Declaration.EvidenceToken", "evidence_token", "text"},
		{"Diagnostic.WorkspaceID", "workspace_id", "text"}, {"Diagnostic.AccountID", "account_id", "text"}, {"Diagnostic.CutDate", "cut_date", "text"},
	} {
		checks = append(checks, fmt.Sprintf(`json_type(result_json,'$.%s')='%s' AND json_extract(result_json,'$.%s')=%s`, field.name, field.kind, field.name, field.column))
	}
	checks = append(checks,
		`json_type(result_json,'$.ObservedBalanceCents')='integer' AND json_extract(result_json,'$.ObservedBalanceCents')>=0`,
		`json_type(result_json,'$.ObservedBalanceVersion')='integer' AND json_extract(result_json,'$.ObservedBalanceVersion')>=0`,
		`json_type(result_json,'$.Coverage')='text' AND json_extract(result_json,'$.Coverage') IN ('PARTIAL','ALL_IDENTIFIED')`,
		`json_type(result_json,'$.Items')='array' AND json_array_length(result_json,'$.Items')>0`,
		`json_type(result_json,'$.Declaration')='object' AND json_type(result_json,'$.Diagnostic')='object'`,
		`json_type(result_json,'$.Declaration.ExpectedBalanceVersion')='integer' AND json_extract(result_json,'$.Declaration.ExpectedBalanceVersion')=json_extract(result_json,'$.ObservedBalanceVersion')`,
		`json_type(result_json,'$.Diagnostic.CurrentBalanceCents')='integer' AND json_extract(result_json,'$.Diagnostic.CurrentBalanceCents')=json_extract(result_json,'$.ObservedBalanceCents')`,
		`json_type(result_json,'$.Diagnostic.BalanceVersion')='integer' AND json_extract(result_json,'$.Diagnostic.BalanceVersion')=json_extract(result_json,'$.ObservedBalanceVersion')`,
		`json_type(result_json,'$.Declaration.Coverage')='text' AND json_extract(result_json,'$.Declaration.Coverage')=json_extract(result_json,'$.Coverage')`,
		`json_type(result_json,'$.Cash')='object' AND json_type(result_json,'$.Cash.Evidence')='object'`,
		`((json_extract(result_json,'$.Cash.Evidence.State')='UNKNOWN' AND json_type(result_json,'$.Cash.Cents')='null') OR
 (json_extract(result_json,'$.Cash.Evidence.State')='DECLARED' AND json_type(result_json,'$.Cash.Cents')='integer' AND json_extract(result_json,'$.Cash.Cents')>=0))`,
		`(json_type(result_json,'$.DeclaredBalanceAtCutCents') IS NULL OR json_type(result_json,'$.DeclaredBalanceAtCutCents')='null' OR (json_type(result_json,'$.DeclaredBalanceAtCutCents')='integer' AND json_extract(result_json,'$.DeclaredBalanceAtCutCents')>=0))`,
	)
	// Missing JSON paths evaluate to SQL NULL; NULL must reject, not pass CHECK.
	return `CHECK(CASE WHEN json_valid(result_json) THEN COALESCE((` + strings.Join(checks, ` AND `) + `),0) ELSE 0 END=1)`
}
