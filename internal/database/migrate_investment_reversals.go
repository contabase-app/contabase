package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// Schema 80: individual purchase reversal receipts. The migration is purely
// additive — purchases stay immutable, schema 79 keeps its exact contract and
// the transactions journal is never rebuilt. A reversal is either TRANSFER
// (the purchase had an automatic funding transfer; one compensating transfer
// inverts it between the same accounts) or RECORD (no automatic transfer, no
// financial effect at all). History is preserved: the purchase row stays and
// the receipt is the immutable, durable idempotency record.

func reversal80Objects() []cancellationSchemaObject {
	table := `CREATE TABLE investment_purchase_reversals (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, investment_id TEXT NOT NULL, purchase_id TEXT NOT NULL,
 movement TEXT NOT NULL CHECK(movement IN ('TRANSFER','RECORD')),
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
 UNIQUE(workspace_id,purchase_id), UNIQUE(workspace_id,request_id), UNIQUE(workspace_id,preview_id),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,investment_account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(created_by) REFERENCES users(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 CHECK(length(workspace_id)>0 AND length(id)>0 AND length(investment_id)>0 AND length(purchase_id)>0),
 CHECK(length(investment_account_id)>0 AND purchase_id<>'initial:'||investment_id),
 CHECK(typeof(investment_before_cents)='integer' AND typeof(investment_after_cents)='integer'
   AND typeof(origin_before_cents)='integer' AND typeof(origin_after_cents)='integer'),
 CHECK(typeof(investment_before_version)='integer' AND investment_before_version BETWEEN 0 AND 9223372036854775806
   AND typeof(investment_after_version)='integer' AND investment_after_version BETWEEN 0 AND 9223372036854775806
   AND typeof(origin_before_version)='integer' AND origin_before_version BETWEEN 0 AND 9223372036854775806
   AND typeof(origin_after_version)='integer' AND origin_after_version BETWEEN 0 AND 9223372036854775806),
 CHECK((movement='RECORD' AND original_transaction_id='' AND compensation_transaction_id='' AND origin_account_id=''
   AND investment_before_cents=investment_after_cents AND origin_before_cents=origin_after_cents
   AND investment_before_version=investment_after_version AND origin_before_version=origin_after_version)
  OR (movement='TRANSFER' AND length(original_transaction_id)>0 AND length(compensation_transaction_id)>0
   AND investment_account_id<>origin_account_id AND original_transaction_id<>compensation_transaction_id
   AND typeof(investment_before_cents-amount_cents)='integer' AND investment_after_cents=investment_before_cents-amount_cents
   AND typeof(origin_before_cents+amount_cents)='integer' AND origin_after_cents=origin_before_cents+amount_cents
   AND investment_after_version=investment_before_version+1 AND origin_after_version=origin_before_version+1)),
 CHECK(typeof(confirmed_at)='integer' AND confirmed_at>0 AND typeof(preview_issued_at)='integer' AND preview_issued_at>0),
 CHECK(typeof(preview_expires_at)='integer' AND typeof(preview_issued_at+900)='integer'
   AND preview_expires_at=preview_issued_at+900 AND confirmed_at>=preview_issued_at AND confirmed_at<preview_expires_at)
) WITHOUT ROWID`
	objects := []cancellationSchemaObject{{"table", "investment_purchase_reversals", "investment_purchase_reversals", table},
		{"index", "idx_reversal80_date", "investment_purchase_reversals", `CREATE INDEX idx_reversal80_date ON investment_purchase_reversals(workspace_id,economic_date,id)`}}
	addTrigger := func(name, on, action, condition string) {
		q := "CREATE TRIGGER " + name + " BEFORE " + action + " ON " + on
		if condition != "" {
			q += " WHEN " + condition
		}
		q += " BEGIN SELECT RAISE(ABORT,'investment purchase reversal integrity'); END"
		objects = append(objects, cancellationSchemaObject{"trigger", name, on, q})
	}
	addTrigger("trg_reversal80_insert", "investment_purchase_reversals", "INSERT", reversal80Collision("NEW")+" OR NOT ("+reversal80Relation("NEW", true)+") OR NOT ("+reversal80JSON("NEW")+")")
	for _, action := range []string{"UPDATE", "DELETE"} {
		addTrigger("trg_reversal80_receipt_"+strings.ToLower(action), "investment_purchase_reversals", action, "")
		for _, parent := range []struct{ table, match string }{
			{"investments", "r.investment_id=OLD.id"},
			{"investment_purchases", "r.purchase_id=OLD.id"},
			{"accounts", "OLD.id IN(r.investment_account_id,r.origin_account_id)"},
			{"transactions", "OLD.id IN(r.original_transaction_id,r.compensation_transaction_id)"},
		} {
			addTrigger("trg_reversal80_"+parent.table+"_"+strings.ToLower(action), parent.table, action,
				"EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=OLD.workspace_id AND "+parent.match+")")
		}
	}
	// A reversed purchase no longer carries active principal: no withdrawal
	// may ever be allocated to it. Independent purchases stay operable.
	for _, action := range []string{"INSERT", "UPDATE"} {
		condition := `EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=NEW.workspace_id AND r.purchase_id=NEW.purchase_id)`
		if action == "UPDATE" {
			condition += ` OR EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=OLD.workspace_id AND r.purchase_id=OLD.purchase_id)`
		}
		addTrigger("trg_reversal80_allocation_"+strings.ToLower(action), "investment_withdrawal_allocations", action, condition)
	}
	// The original and compensating transfers keep the same protection family
	// as schema 79: no reconciliation, refund, invoice or parent link may be
	// attached to them once the receipt exists.
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
				condition += reversal80ProtectedLink(link, ref)
			}
			addTrigger("trg_reversal80_link_"+link.table+"_"+link.column+"_"+strings.ToLower(action), link.table, action, condition)
		}
	}
	return objects
}

func reversal80ProtectedLink(link cancellationTransactionLink, row string) string {
	scope := "r.workspace_id=" + row + "." + link.scope
	if link.scope == "" {
		scope = "EXISTS(SELECT 1 FROM boxes b WHERE b.id=" + row + ".box_id AND b.workspace_id=r.workspace_id)"
	}
	return "EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE " + scope + " AND " + row + "." + link.column + " IN(r.original_transaction_id,r.compensation_transaction_id))"
}

func reversal80Collision(r string) string {
	// RECORD receipts carry empty transaction ids; the overlap arms must only
	// fire on a real (non-empty) transaction identity.
	return `EXISTS(SELECT 1 FROM investment_purchase_reversals x WHERE x.workspace_id=` + r + `.workspace_id AND
 (x.id=` + r + `.id OR x.purchase_id=` + r + `.purchase_id OR x.request_id=` + r + `.request_id OR x.preview_id=` + r + `.preview_id
 OR (length(` + r + `.original_transaction_id)>0 AND ` + r + `.original_transaction_id IN(x.original_transaction_id,x.compensation_transaction_id))
 OR (length(` + r + `.compensation_transaction_id)>0 AND ` + r + `.compensation_transaction_id IN(x.original_transaction_id,x.compensation_transaction_id))))`
}

func reversal80JSON(r string) string {
	checks := []string{
		"json_type(" + r + ".payload_json) IS 'object'", "json_type(" + r + ".evidence_json) IS 'object'",
		"json_extract(" + r + ".payload_json,'$.version') IS 1",
		"json_extract(" + r + ".payload_json,'$.operation') IS 'REVERSE_INVESTMENT_PURCHASE'",
		"json_type(" + r + ".payload_json,'$.confirmed') IS 'true'",
		"json_type(" + r + ".payload_json,'$.preview_token') IS 'text'",
		"length(json_extract(" + r + ".payload_json,'$.preview_token'))>0",
	}
	for _, field := range []struct{ json, column string }{
		{"workspace_id", "workspace_id"}, {"acting_user_id", "created_by"}, {"investment_id", "investment_id"},
		{"purchase_id", "purchase_id"}, {"request_id", "request_id"}, {"reason", "reason"},
	} {
		checks = append(checks, "json_extract("+r+".payload_json,'$."+field.json+"') IS "+r+"."+field.column)
	}
	for _, field := range []string{"workspace_id", "investment_id", "purchase_id", "movement", "original_transaction_id", "investment_account_id", "origin_account_id", "amount_cents", "economic_date", "evidence_hash"} {
		checks = append(checks, "json_extract("+r+".evidence_json,'$."+field+"') IS "+r+"."+field)
	}
	return "(" + strings.Join(checks, " AND ") + ")"
}

// reversal80Dependents mirrors the schema 79 dependent family minus the
// objects handled by dedicated rules: later purchases stay allowed, valuations
// only block from the purchase date on, withdrawals only through allocations
// to this purchase.
var reversal80Dependents = []string{
	"investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions",
	"investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions",
	"investment_settlement_receipts", "investment_settlement_applications", "investment_result_classifications",
	"investment_adoption_items",
}

// reversal80Relation validates the whole financial contract of the receipt.
// The TRANSFER arm mirrors schema 79 (exact original transfer shape,
// compensating transfer shape, both balance effects and versions). The RECORD
// arm requires the explicit absence of an automatic transfer and pins the
// recorded state without touching any balance. The insert arm additionally
// proves reality at confirmation time (current balances, versions, account
// types, no adjustments/adoption, no valuation or dependent rows yet); the
// revalidation arm drops those reality pins so later independent movements on
// the accounts never invalidate a historical receipt (mirroring schemas 79
// and 81).
func reversal80Relation(r string, inserting bool) string {
	from := ` FROM investments i
 JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id AND p.id=` + r + `.purchase_id
 JOIN accounts a ON a.workspace_id=i.workspace_id AND a.id=i.account_id AND a.id=` + r + `.investment_account_id`
	where := ` WHERE i.workspace_id=` + r + `.workspace_id AND i.id=` + r + `.investment_id AND i.account_id=a.id`
	if inserting {
		// The lot must be alive when the reversal is confirmed, but it stays
		// operable afterwards: redemption and archiving are independent
		// lifecycle transitions that never invalidate a historical receipt.
		where += `
 AND i.status='ATIVA' AND i.archived_at IS NULL`
	}
	where += `
 AND p.is_initial=0 AND p.principal_cents=` + r + `.amount_cents AND p.purchase_date<=` + r + `.economic_date
 AND NOT EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=i.workspace_id AND c.investment_id=i.id)
 AND NOT EXISTS(SELECT 1 FROM investment_purchase_reversals x WHERE x.workspace_id=i.workspace_id AND x.purchase_id=p.id AND x.id<>` + r + `.id)
 AND NOT EXISTS(SELECT 1 FROM investment_withdrawal_allocations w WHERE w.workspace_id=i.workspace_id AND w.investment_id=i.id AND w.purchase_id=p.id)`
	if inserting {
		// Eligibility witnesses observed at confirmation time: a valuation at
		// or after the purchase date (or any dependent row for the investment)
		// rules the reversal out, but the investment stays alive afterwards and
		// may legitimately gain such rows later.
		where += ` AND NOT EXISTS(SELECT 1 FROM investment_valuations v WHERE v.workspace_id=i.workspace_id AND v.investment_id=i.id AND v.valuation_date>=p.purchase_date)`
		for _, table := range reversal80Dependents {
			where += ` AND NOT EXISTS(SELECT 1 FROM ` + table + ` x WHERE x.workspace_id=i.workspace_id AND x.investment_id=i.id)`
		}
	}
	transfer := `EXISTS(SELECT 1` + from + `
 JOIN transactions o ON o.workspace_id=i.workspace_id AND o.id='investment-purchase:'||p.id
 JOIN transactions t ON t.workspace_id=i.workspace_id AND t.id='investment-reversal:'||` + r + `.id
 JOIN accounts s ON s.workspace_id=i.workspace_id AND s.id=` + r + `.origin_account_id` + where + `
 AND o.type='TRANSFER' AND o.status='paid' AND o.effect_mode='STANDARD'
 AND o.user_id=p.created_by AND o.account_id=s.id AND o.destination_account_id=a.id AND o.amount=p.principal_cents
 AND o.date_civil=p.purchase_date AND o.paid_date_civil=p.purchase_date
 AND o.date=unixepoch(p.purchase_date||' 12:00:00') AND o.paid_at=o.date
 AND t.type='TRANSFER' AND t.status='paid' AND t.effect_mode='STANDARD'
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
		transfer += ` AND a.type='INVESTMENT' AND a.archived_at IS NULL AND s.type IN('CHECKING','SAVINGS','WALLET') AND s.archived_at IS NULL
 AND a.current_balance=` + r + `.investment_after_cents AND a.balance_version=` + r + `.investment_after_version
 AND s.current_balance=` + r + `.origin_after_cents AND s.balance_version=` + r + `.origin_after_version
 AND NOT EXISTS(SELECT 1 FROM account_balance_adjustments x WHERE x.workspace_id=i.workspace_id AND x.account_id IN(a.id,s.id))
 AND NOT EXISTS(SELECT 1 FROM investment_adoption_confirmations x WHERE x.workspace_id=i.workspace_id AND x.account_id=a.id)`
	}
	for _, link := range cancellation79TransactionLinks {
		scope := "x." + link.scope + "=i.workspace_id"
		if link.scope == "" {
			scope = "EXISTS(SELECT 1 FROM boxes b WHERE b.id=x.box_id AND b.workspace_id=i.workspace_id)"
		}
		transfer += ` AND NOT EXISTS(SELECT 1 FROM ` + link.table + ` x WHERE ` + scope + ` AND x.` + link.column + ` IN(o.id,t.id))`
	}
	transfer += `)`
	record := `EXISTS(SELECT 1` + from + where + `
 AND ` + r + `.origin_account_id='' AND ` + r + `.original_transaction_id='' AND ` + r + `.compensation_transaction_id=''
 AND NOT EXISTS(SELECT 1 FROM transactions o WHERE o.workspace_id=i.workspace_id AND o.id='investment-purchase:'||p.id)`
	if inserting {
		record += ` AND a.current_balance=` + r + `.investment_before_cents AND a.balance_version=` + r + `.investment_before_version`
	}
	record += `)`
	return "(" + transfer + ` OR ` + record + ")"
}

func migrateInvestmentPurchaseReversals(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investments")
	if err != nil || !exists {
		return err
	} // Historical partial-schema migration probes.
	if err := preflightInvestmentReversals80(tx); err != nil {
		return err
	}
	var before string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&before); err != nil {
		return err
	}
	for _, object := range reversal80Objects() {
		if _, err := tx.Exec(object.ddl); err != nil {
			return fmt.Errorf("create %s: %w", object.name, err)
		}
	}
	if err := validateReversal80Structure(tx, false); err != nil {
		return err
	}
	if err := sqlite79Integrity(tx); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM investment_purchase_reversals`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("migration 80 created financial receipts")
	}
	var after string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&after); err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("migration 80 changed transactions schema")
	}
	return nil
}
