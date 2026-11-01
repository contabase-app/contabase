package database

import (
	"database/sql"
	"fmt"
	"strings"
)

type cancellationSchemaObject struct {
	kind, name, table, ddl string
}

func cancellation79Objects() []cancellationSchemaObject {
	table := `CREATE TABLE investment_cancellations (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, investment_id TEXT NOT NULL, purchase_id TEXT NOT NULL,
 original_transaction_id TEXT NOT NULL, compensation_transaction_id TEXT NOT NULL,
 investment_account_id TEXT NOT NULL, origin_account_id TEXT NOT NULL,
 amount_cents INTEGER NOT NULL CHECK(typeof(amount_cents)='integer' AND amount_cents>0),
 investment_before_cents INTEGER NOT NULL, investment_after_cents INTEGER NOT NULL,
 origin_before_cents INTEGER NOT NULL, origin_after_cents INTEGER NOT NULL,
 investment_before_version INTEGER NOT NULL, investment_after_version INTEGER NOT NULL,
 origin_before_version INTEGER NOT NULL, origin_after_version INTEGER NOT NULL,
 created_by TEXT NOT NULL, confirmed_at INTEGER NOT NULL,
 economic_date TEXT NOT NULL ` + referenceCivilDateCheck("economic_date") + `,
 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500 AND reason=trim(reason)),
 request_id TEXT NOT NULL CHECK(length(request_id)=36),
 preview_id TEXT NOT NULL CHECK(length(preview_id)=64 AND preview_id NOT GLOB '*[^0-9a-f]*'),
 preview_issued_at INTEGER NOT NULL, preview_expires_at INTEGER NOT NULL,
 evidence_hash TEXT NOT NULL CHECK(length(evidence_hash)=64 AND evidence_hash NOT GLOB '*[^0-9a-f]*'),
 payload_version INTEGER NOT NULL CHECK(typeof(payload_version)='integer' AND payload_version=1),
 receipt_version INTEGER NOT NULL CHECK(typeof(receipt_version)='integer' AND receipt_version=1),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json)),
 PRIMARY KEY(workspace_id,id),
 UNIQUE(workspace_id,investment_id), UNIQUE(workspace_id,purchase_id),
 UNIQUE(workspace_id,original_transaction_id), UNIQUE(workspace_id,compensation_transaction_id),
 UNIQUE(workspace_id,request_id), UNIQUE(workspace_id,preview_id),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,investment_account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(investment_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(origin_account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,original_transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,compensation_transaction_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(created_by) REFERENCES users(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 CHECK(length(workspace_id)>0 AND length(id)>0 AND length(investment_id)>0 AND length(purchase_id)>0),
 CHECK(investment_account_id<>origin_account_id AND original_transaction_id<>compensation_transaction_id),
 CHECK(typeof(investment_before_cents)='integer' AND typeof(investment_after_cents)='integer'
   AND typeof(origin_before_cents)='integer' AND typeof(origin_after_cents)='integer'),
 CHECK(typeof(investment_before_cents-amount_cents)='integer' AND investment_after_cents=investment_before_cents-amount_cents),
 CHECK(typeof(origin_before_cents+amount_cents)='integer' AND origin_after_cents=origin_before_cents+amount_cents),
 CHECK(typeof(investment_before_version)='integer' AND investment_before_version BETWEEN 0 AND 9223372036854775806
   AND typeof(investment_after_version)='integer' AND investment_after_version=investment_before_version+1),
 CHECK(typeof(origin_before_version)='integer' AND origin_before_version BETWEEN 0 AND 9223372036854775806
   AND typeof(origin_after_version)='integer' AND origin_after_version=origin_before_version+1),
 CHECK(typeof(confirmed_at)='integer' AND confirmed_at>0 AND typeof(preview_issued_at)='integer' AND preview_issued_at>0),
 CHECK(typeof(preview_expires_at)='integer' AND typeof(preview_issued_at+900)='integer'
   AND preview_expires_at=preview_issued_at+900 AND confirmed_at>=preview_issued_at AND confirmed_at<preview_expires_at)
) WITHOUT ROWID`
	objects := []cancellationSchemaObject{{"table", "investment_cancellations", "investment_cancellations", table},
		{"index", "idx_cancellation79_date", "investment_cancellations", `CREATE INDEX idx_cancellation79_date ON investment_cancellations(workspace_id,economic_date,id)`}}
	addTrigger := func(name, on, action, condition string) {
		q := "CREATE TRIGGER " + name + " BEFORE " + action + " ON " + on
		if condition != "" {
			q += " WHEN " + condition
		}
		q += " BEGIN SELECT RAISE(ABORT,'investment cancellation integrity'); END"
		objects = append(objects, cancellationSchemaObject{"trigger", name, on, q})
	}
	addTrigger("trg_cancellation79_insert", "investment_cancellations", "INSERT", cancellation79Collision("NEW")+" OR NOT ("+cancellation79Relation("NEW", true)+") OR NOT ("+cancellation79JSON("NEW")+")")
	for _, action := range []string{"UPDATE", "DELETE"} {
		addTrigger("trg_cancellation79_receipt_"+strings.ToLower(action), "investment_cancellations", action, "")
		for _, parent := range []struct{ table, match string }{
			{"investments", "c.investment_id=OLD.id"},
			{"investment_purchases", "c.purchase_id=OLD.id"},
			{"transactions", "OLD.id IN(c.original_transaction_id,c.compensation_transaction_id)"},
		} {
			addTrigger("trg_cancellation79_"+parent.table+"_"+strings.ToLower(action), parent.table, action,
				"EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=OLD.workspace_id AND "+parent.match+")")
		}
	}
	for _, child := range cancellation79Children {
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			condition := ""
			for _, ref := range []string{"OLD", "NEW"} {
				if action == "INSERT" && ref == "OLD" || action == "DELETE" && ref == "NEW" {
					continue
				}
				if condition != "" {
					condition += " OR "
				}
				condition += "EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=" + ref + ".workspace_id AND c.investment_id=" + ref + ".investment_id)"
			}
			addTrigger("trg_cancellation79_child_"+child+"_"+strings.ToLower(action), child, action, condition)
		}
	}
	for _, link := range cancellation79TransactionLinks {
		for _, action := range []string{"INSERT", "UPDATE"} {
			condition := ""
			for _, ref := range []string{"OLD", "NEW"} {
				if ref == "OLD" && action == "INSERT" {
					continue
				}
				if condition != "" {
					condition += " OR "
				}
				condition += cancellation79ProtectedLink(link, ref)
			}
			addTrigger("trg_cancellation79_link_"+link.table+"_"+link.column+"_"+strings.ToLower(action), link.table, action, condition)
		}
	}
	// An account remains usable. Only the adoption declaration about the
	// investment account is incompatible with an already cancelled lot.
	for _, action := range []string{"INSERT", "UPDATE"} {
		condition := `EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=NEW.workspace_id AND c.investment_account_id=NEW.account_id)`
		if action == "UPDATE" {
			condition += ` OR EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=OLD.workspace_id AND c.investment_account_id=OLD.account_id)`
		}
		addTrigger("trg_cancellation79_adoption_"+strings.ToLower(action), "investment_adoption_confirmations", action, condition)
	}
	return objects
}

var cancellation79Children = []string{
	"investment_purchases", "investment_valuations", "investment_withdrawals", "investment_withdrawal_allocations",
	"investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions",
	"investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions",
	"investment_settlement_receipts", "investment_settlement_applications", "investment_result_classifications", "investment_adoption_items",
}

type cancellationTransactionLink struct{ table, column, scope string }

var cancellation79TransactionLinks = []cancellationTransactionLink{
	{"investment_purchase_funding", "transaction_id", "workspace_id"},
	{"investment_settlement_receipts", "transaction_id", "workspace_id"},
	{"investment_settlement_prior_effects", "transaction_id", "workspace_id"},
	{"investment_result_classifications", "transaction_id", "workspace_id"},
	{"transaction_refunds", "transaction_id", "workspace_id"},
	{"transaction_status_history", "transaction_id", "workspace_id"},
	{"transactions", "parent_id", "workspace_id"},
	{"invoice_payments", "transaction_id", "workspace_id"},
	{"invoice_payments", "reversal_transaction_id", "workspace_id"},
	{"box_virtual_ledger", "source_transaction_id", ""},
}

func cancellation79ProtectedLink(link cancellationTransactionLink, row string) string {
	scope := "c.workspace_id=" + row + "." + link.scope
	if link.scope == "" {
		scope = "EXISTS(SELECT 1 FROM boxes b WHERE b.id=" + row + ".box_id AND b.workspace_id=c.workspace_id)"
	}
	return "EXISTS(SELECT 1 FROM investment_cancellations c WHERE " + scope + " AND " + row + "." + link.column + " IN(c.original_transaction_id,c.compensation_transaction_id))"
}

func cancellation79Collision(r string) string {
	return `EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=` + r + `.workspace_id AND
 (c.id=` + r + `.id OR c.investment_id=` + r + `.investment_id OR c.purchase_id=` + r + `.purchase_id OR c.request_id=` + r + `.request_id OR c.preview_id=` + r + `.preview_id
 OR c.original_transaction_id IN(` + r + `.original_transaction_id,` + r + `.compensation_transaction_id)
 OR c.compensation_transaction_id IN(` + r + `.original_transaction_id,` + r + `.compensation_transaction_id)))`
}

func cancellation79JSON(r string) string {
	checks := []string{
		"json_type(" + r + ".payload_json) IS 'object'", "json_type(" + r + ".evidence_json) IS 'object'",
		"json_extract(" + r + ".payload_json,'$.version') IS 1",
		"json_extract(" + r + ".payload_json,'$.operation') IS 'CANCEL_INITIAL_FUNDED_INVESTMENT'",
		"json_type(" + r + ".payload_json,'$.confirmed') IS 'true'",
		"json_type(" + r + ".payload_json,'$.preview_token') IS 'text'",
		"length(json_extract(" + r + ".payload_json,'$.preview_token'))>0",
	}
	for _, field := range []struct{ json, column string }{
		{"workspace_id", "workspace_id"}, {"acting_user_id", "created_by"}, {"investment_id", "investment_id"}, {"request_id", "request_id"}, {"reason", "reason"},
	} {
		checks = append(checks, "json_extract("+r+".payload_json,'$."+field.json+"') IS "+r+"."+field.column)
	}
	// Persist the versioned preview evidence beside the canonical request. Its
	// signature is checked by the future command, never by a schema trigger.
	for _, field := range []string{"workspace_id", "investment_id", "purchase_id", "original_transaction_id", "investment_account_id", "origin_account_id", "amount_cents", "economic_date", "evidence_hash"} {
		checks = append(checks, "json_extract("+r+".evidence_json,'$."+field+"') IS "+r+"."+field)
	}
	return "(" + strings.Join(checks, " AND ") + ")"
}

func cancellation79Relation(r string, inserting bool) string {
	q := `EXISTS(SELECT 1 FROM investments i
 JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id AND p.id=` + r + `.purchase_id
 JOIN transactions o ON o.workspace_id=i.workspace_id AND o.id=` + r + `.original_transaction_id
 JOIN transactions t ON t.workspace_id=i.workspace_id AND t.id=` + r + `.compensation_transaction_id
 JOIN accounts a ON a.workspace_id=i.workspace_id AND a.id=` + r + `.investment_account_id
 JOIN accounts s ON s.workspace_id=i.workspace_id AND s.id=` + r + `.origin_account_id
 WHERE i.workspace_id=` + r + `.workspace_id AND i.id=` + r + `.investment_id AND i.account_id=a.id
 AND i.status='ATIVA' AND i.archived_at IS NULL AND p.is_initial=1 AND p.id='initial:'||i.id
 AND p.principal_cents=i.principal_cents AND p.principal_cents=` + r + `.amount_cents
 AND p.purchase_date=i.applied_date AND p.created_by=i.created_by AND p.created_at=i.created_at
 AND p.purchase_date<=` + r + `.economic_date
 AND (SELECT count(*) FROM investment_purchases x WHERE x.workspace_id=i.workspace_id AND x.investment_id=i.id)=1
 AND o.id='investment-purchase:'||p.id AND o.type='TRANSFER' AND o.status='paid' AND o.effect_mode='STANDARD'
 AND o.user_id=p.created_by AND o.account_id=s.id AND o.destination_account_id=a.id AND o.amount=p.principal_cents
 AND o.date_civil=p.purchase_date AND o.paid_date_civil=p.purchase_date
 AND o.date=unixepoch(p.purchase_date||' 12:00:00') AND o.paid_at=o.date
 AND t.id='investment-cancellation:'||` + r + `.id AND t.type='TRANSFER' AND t.status='paid' AND t.effect_mode='STANDARD'
 AND t.user_id=` + r + `.created_by AND t.account_id=a.id AND t.destination_account_id=s.id AND t.amount=` + r + `.amount_cents
 AND t.date_civil=` + r + `.economic_date AND t.paid_date_civil=` + r + `.economic_date
 AND t.date=unixepoch(` + r + `.economic_date||' 12:00:00') AND t.paid_at=t.date
 AND t.created_at=` + r + `.confirmed_at AND t.updated_at=t.created_at
 AND t.category_id IS NULL AND t.invoice_id IS NULL AND t.invoice_override_id IS NULL
 AND t.parent_id IS NULL AND t.installment_number=1 AND t.total_installments=1
 AND t.recurring_rule_id IS NULL AND t.recurrence_sequence IS NULL AND t.series_id IS NULL AND t.is_recurring_exception=0
 AND t.scheduled_payment_at IS NULL AND t.scheduled_payment_created_by IS NULL AND t.scheduled_payment_executed_at IS NULL
 AND t.due_date IS NULL AND t.due_date_civil IS NULL AND t.scheduled_payment_date_civil IS NULL AND t.contact_id IS NULL`
	if inserting {
		q += ` AND a.type='INVESTMENT' AND a.archived_at IS NULL AND s.type IN('CHECKING','SAVINGS','WALLET') AND s.archived_at IS NULL
 AND a.current_balance=` + r + `.investment_after_cents AND a.balance_version=` + r + `.investment_after_version
 AND s.current_balance=` + r + `.origin_after_cents AND s.balance_version=` + r + `.origin_after_version
 AND NOT EXISTS(SELECT 1 FROM account_balance_adjustments x WHERE x.workspace_id=i.workspace_id AND x.account_id IN(a.id,s.id))
 AND NOT EXISTS(SELECT 1 FROM investment_adoption_confirmations x WHERE x.workspace_id=i.workspace_id AND x.account_id=a.id)`
	}
	for _, child := range cancellation79Children {
		if child == "investment_purchases" {
			continue
		}
		q += " AND NOT EXISTS(SELECT 1 FROM " + child + " x WHERE x.workspace_id=i.workspace_id AND x.investment_id=i.id)"
	}
	for _, link := range cancellation79TransactionLinks {
		scope := "x." + link.scope + "=i.workspace_id"
		if link.scope == "" {
			scope = "EXISTS(SELECT 1 FROM boxes b WHERE b.id=x.box_id AND b.workspace_id=i.workspace_id)"
		}
		q += " AND NOT EXISTS(SELECT 1 FROM " + link.table + " x WHERE " + scope + " AND x." + link.column + " IN(o.id,t.id))"
	}
	return q + ")"
}

func migrateInvestmentCancellations(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investments")
	if err != nil || !exists {
		return err
	} // Historical partial-schema migration probes.
	if err := preflightInvestmentCancellations79(tx); err != nil {
		return err
	}
	var before string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&before); err != nil {
		return err
	}
	for _, object := range cancellation79Objects() {
		if _, err := tx.Exec(object.ddl); err != nil {
			return fmt.Errorf("create %s: %w", object.name, err)
		}
	}
	if err := validateCancellation79Structure(tx); err != nil {
		return err
	}
	if err := sqlite79Integrity(tx); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM investment_cancellations`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("migration 79 created financial receipts")
	}
	var after string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&after); err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("migration 79 changed transactions schema")
	}
	return nil
}
