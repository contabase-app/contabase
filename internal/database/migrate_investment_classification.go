package database

import (
	"database/sql"
	"fmt"
)

// C2B stores economic classifications in transactions, never in a second
// financial ledger. Old executables MUST NOT open a database upgraded to v76:
// their cash queries do not understand CLASSIFICATION_ONLY.
func migrateInvestmentClassification(tx *sql.Tx) error {
	// Keep the category schema consistent in partial legacy fixtures too. This
	// parent key is required by the tenant-scoped classification category FK.
	exists, err := tableExists(tx, "categories")
	if err != nil {
		return err
	}
	if exists {
		if _, err = tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_c2b_category_tenant ON categories(id,workspace_id)`); err != nil {
			return err
		}
	}
	exists, err = tableExists(tx, "investment_settlement_applications")
	if err != nil || !exists {
		return err
	}
	if err = addColumnIfMissing(tx, "transactions", "effect_mode", "TEXT NOT NULL DEFAULT 'STANDARD' CHECK(effect_mode IN ('STANDARD','CLASSIFICATION_ONLY'))"); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS investment_result_classifications (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL,
 settlement_revision_id TEXT NOT NULL, application_id TEXT NOT NULL,
 settlement_version INTEGER NOT NULL CHECK(typeof(settlement_version)='integer' AND settlement_version>0),
 version INTEGER NOT NULL CHECK(version=1),
 transaction_id TEXT, category_id TEXT,
 principal_cents INTEGER CHECK(principal_cents IS NULL OR (typeof(principal_cents)='integer' AND principal_cents>=0)),
 result_cents INTEGER CHECK(result_cents IS NULL OR typeof(result_cents)='integer'),
 nature TEXT NOT NULL CHECK(nature IN ('REALIZED_GAIN','REALIZED_LOSS','ZERO','UNKNOWN')),
 state TEXT NOT NULL CHECK(state IN ('COMPLETED','UNAVAILABLE')),
 economic_date TEXT NOT NULL ` + referenceCivilDateCheck("economic_date") + `,
 economic_at INTEGER NOT NULL CHECK(typeof(economic_at)='integer' AND economic_at=unixepoch(economic_date||' 12:00:00')),
 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 request_id TEXT NOT NULL CHECK(length(request_id)=36),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 PRIMARY KEY(workspace_id,id), UNIQUE(workspace_id,request_id),
 UNIQUE(workspace_id,investment_id,withdrawal_id), UNIQUE(workspace_id,settlement_revision_id),
 UNIQUE(workspace_id,application_id), UNIQUE(workspace_id,transaction_id),
 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,settlement_revision_id) REFERENCES investment_settlement_revisions(workspace_id,investment_id,withdrawal_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,application_id) REFERENCES investment_settlement_applications(workspace_id,id) ON DELETE RESTRICT,
 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(category_id,workspace_id) REFERENCES categories(id,workspace_id) ON DELETE RESTRICT,
 CHECK((nature='UNKNOWN' AND state='UNAVAILABLE' AND principal_cents IS NULL AND result_cents IS NULL AND transaction_id IS NULL AND category_id IS NULL)
 OR (state='COMPLETED' AND principal_cents IS NOT NULL AND result_cents IS NOT NULL AND
 ((nature='ZERO' AND result_cents=0 AND transaction_id IS NULL AND category_id IS NULL)
 OR (nature='REALIZED_GAIN' AND result_cents>0 AND transaction_id IS NOT NULL AND category_id IS NOT NULL)
 OR (nature='REALIZED_LOSS' AND result_cents<0 AND result_cents>=-9223372036854775807 AND transaction_id IS NOT NULL AND category_id IS NOT NULL))))
 )`,
		`CREATE VIEW IF NOT EXISTS financial_transactions AS SELECT * FROM transactions WHERE effect_mode='STANDARD'`,
		`CREATE TRIGGER IF NOT EXISTS trg_c2b_origin BEFORE INSERT ON investment_result_classifications
 WHEN EXISTS(SELECT 1 FROM investment_result_classifications WHERE workspace_id=NEW.workspace_id AND
 (id=NEW.id OR request_id=NEW.request_id OR settlement_revision_id=NEW.settlement_revision_id OR application_id=NEW.application_id OR transaction_id=NEW.transaction_id))
 OR EXISTS(SELECT 1 FROM transactions WHERE workspace_id=NEW.workspace_id AND id=NEW.transaction_id)
 OR NOT EXISTS(SELECT 1 FROM investment_settlement_applications a JOIN investment_settlement_revisions s
 ON s.workspace_id=a.workspace_id AND s.investment_id=a.investment_id AND s.withdrawal_id=a.withdrawal_id AND s.id=a.settlement_revision_id
 WHERE a.workspace_id=NEW.workspace_id AND a.id=NEW.application_id AND a.investment_id=NEW.investment_id AND a.withdrawal_id=NEW.withdrawal_id
 AND a.settlement_revision_id=NEW.settlement_revision_id AND a.settlement_version=NEW.settlement_version AND s.version=NEW.settlement_version
 AND a.state='FINANCIAL_APPLIED_CLASSIFICATION_PENDING' AND s.state='CONCILIADA' AND s.economic_date=NEW.economic_date
 AND s.principal_cents IS NEW.principal_cents AND s.result_cents IS NEW.result_cents)
 BEGIN SELECT RAISE(ABORT,'invalid or duplicate investment classification'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_c2b_transaction_insert BEFORE INSERT ON transactions
 WHEN (NEW.effect_mode='STANDARD' AND EXISTS(SELECT 1 FROM investment_result_classifications c WHERE c.workspace_id=NEW.workspace_id AND c.transaction_id=NEW.id))
 OR (NEW.effect_mode='CLASSIFICATION_ONLY' AND NOT EXISTS(
 SELECT 1 FROM investment_result_classifications c JOIN investment_settlement_revisions s
 ON s.workspace_id=c.workspace_id AND s.investment_id=c.investment_id AND s.withdrawal_id=c.withdrawal_id AND s.id=c.settlement_revision_id
 JOIN categories cat ON cat.workspace_id=c.workspace_id AND cat.id=c.category_id
 WHERE c.workspace_id=NEW.workspace_id AND c.transaction_id=NEW.id AND c.state='COMPLETED'
 AND NEW.user_id=c.created_by AND NEW.category_id=c.category_id AND NEW.account_id=s.account_id
 AND NEW.type=cat.type AND ((c.nature='REALIZED_GAIN' AND NEW.type='INCOME' AND NEW.amount=c.result_cents)
 OR (c.nature='REALIZED_LOSS' AND NEW.type='EXPENSE' AND NEW.amount=-c.result_cents))
 AND typeof(NEW.amount)='integer' AND NEW.date=c.economic_at AND NEW.date_civil=c.economic_date
 AND NEW.created_at=c.created_at AND NEW.status='paid' AND NEW.paid_at IS NULL AND NEW.paid_date_civil IS NULL
 AND NEW.destination_account_id IS NULL AND NEW.invoice_id IS NULL AND NEW.invoice_override_id IS NULL
 AND NEW.parent_id IS NULL AND NEW.recurring_rule_id IS NULL AND NEW.series_id IS NULL AND NEW.recurrence_sequence IS NULL
 AND NEW.installment_number=1 AND NEW.total_installments=1 AND NEW.is_recurring_exception=0
 AND NEW.scheduled_payment_at IS NULL AND NEW.scheduled_payment_created_by IS NULL AND NEW.scheduled_payment_executed_at IS NULL
 AND NEW.due_date IS NULL AND NEW.due_date_civil IS NULL AND NEW.contact_id IS NULL))
 OR EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=NEW.workspace_id AND t.id=NEW.id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'classification requires immutable settlement origin'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_c2b_transaction_update BEFORE UPDATE ON transactions
 WHEN OLD.effect_mode='CLASSIFICATION_ONLY' OR NEW.effect_mode!=OLD.effect_mode
 BEGIN SELECT RAISE(ABORT,'investment classification is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_c2b_transaction_delete BEFORE DELETE ON transactions
 WHEN OLD.effect_mode='CLASSIFICATION_ONLY'
 BEGIN SELECT RAISE(ABORT,'investment classification is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_c2b_refund BEFORE INSERT ON transaction_refunds
 WHEN EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=NEW.workspace_id AND t.id=NEW.transaction_id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'investment classification cannot be refunded'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_c2b_reserve BEFORE INSERT ON box_virtual_ledger
 WHEN EXISTS(SELECT 1 FROM boxes b JOIN transactions t ON t.workspace_id=b.workspace_id AND t.id=NEW.source_transaction_id
 WHERE b.id=NEW.box_id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'investment classification cannot move reserves'); END`,
	}
	for _, action := range []string{"UPDATE", "DELETE"} {
		statements = append(statements, fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_c2b_classification_%s BEFORE %s ON investment_result_classifications BEGIN SELECT RAISE(ABORT,'immutable investment classification'); END`, action, action))
	}
	statements = append(statements, `CREATE TRIGGER IF NOT EXISTS trg_c2b_reserve_update BEFORE UPDATE ON box_virtual_ledger
 WHEN EXISTS(SELECT 1 FROM boxes b JOIN transactions t ON t.workspace_id=b.workspace_id AND t.id=NEW.source_transaction_id
 WHERE b.id=NEW.box_id AND t.effect_mode='CLASSIFICATION_ONLY')
 BEGIN SELECT RAISE(ABORT,'investment classification cannot move reserves'); END`)
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}
