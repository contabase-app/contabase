package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// Schema 81: record-only initial registration cancellation receipts. The
// migration is purely additive — the initial purchase stays immutable, schemas
// 79 and 80 keep their exact contracts and the transactions journal is never
// rebuilt or even read for writes. A record-only initial application has no
// automatic funding transfer, so cancelling its registration performs no
// financial compensation at all: no transaction, no balance effect, no balance
// version bump. The receipt is the immutable, durable idempotency record and
// the only authority for the CANCELADA projection. Account balance pins exist
// only in the insert-time arm (mirroring schema 79), so later independent
// movements on the investment account never invalidate a historical receipt.

func recordCancellation81Objects() []cancellationSchemaObject {
	table := `CREATE TABLE investment_record_cancellations (
 workspace_id TEXT NOT NULL, id TEXT NOT NULL, investment_id TEXT NOT NULL, purchase_id TEXT NOT NULL,
 investment_account_id TEXT NOT NULL,
 amount_cents INTEGER NOT NULL CHECK(typeof(amount_cents)='integer' AND amount_cents>0),
 investment_before_cents INTEGER NOT NULL, investment_after_cents INTEGER NOT NULL,
 investment_before_version INTEGER NOT NULL, investment_after_version INTEGER NOT NULL,
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
 UNIQUE(workspace_id,request_id), UNIQUE(workspace_id,preview_id),
 FOREIGN KEY(workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,investment_account_id) REFERENCES investments(workspace_id,id,account_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(workspace_id,investment_id,purchase_id) REFERENCES investment_purchases(workspace_id,investment_id,id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 FOREIGN KEY(created_by) REFERENCES users(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
 CHECK(length(workspace_id)>0 AND length(id)>0 AND length(investment_id)>0 AND length(purchase_id)>0),
 CHECK(length(investment_account_id)>0 AND purchase_id='initial:'||investment_id),
 CHECK(typeof(investment_before_cents)='integer' AND typeof(investment_after_cents)='integer'
   AND investment_after_cents=investment_before_cents),
 CHECK(typeof(investment_before_version)='integer' AND investment_before_version BETWEEN 0 AND 9223372036854775806
   AND typeof(investment_after_version)='integer' AND investment_after_version=investment_before_version),
 CHECK(typeof(confirmed_at)='integer' AND confirmed_at>0 AND typeof(preview_issued_at)='integer' AND preview_issued_at>0),
 CHECK(typeof(preview_expires_at)='integer' AND typeof(preview_issued_at+900)='integer'
   AND preview_expires_at=preview_issued_at+900 AND confirmed_at>=preview_issued_at AND confirmed_at<preview_expires_at)
) WITHOUT ROWID`
	objects := []cancellationSchemaObject{{"table", "investment_record_cancellations", "investment_record_cancellations", table},
		{"index", "idx_recordcancel81_date", "investment_record_cancellations", `CREATE INDEX idx_recordcancel81_date ON investment_record_cancellations(workspace_id,economic_date,id)`}}
	addTrigger := func(name, on, action, condition string) {
		q := "CREATE TRIGGER " + name + " BEFORE " + action + " ON " + on
		if condition != "" {
			q += " WHEN " + condition
		}
		q += " BEGIN SELECT RAISE(ABORT,'investment record cancellation integrity'); END"
		objects = append(objects, cancellationSchemaObject{"trigger", name, on, q})
	}
	addTrigger("trg_recordcancel81_insert", "investment_record_cancellations", "INSERT", recordCancellation81Collision("NEW")+" OR NOT ("+recordCancellation81Relation("NEW", true)+") OR NOT ("+recordCancellation81JSON("NEW")+")")
	for _, action := range []string{"UPDATE", "DELETE"} {
		addTrigger("trg_recordcancel81_receipt_"+strings.ToLower(action), "investment_record_cancellations", action, "")
		for _, parent := range []struct{ table, match string }{
			{"investments", "c.investment_id=OLD.id"},
			{"investment_purchases", "c.purchase_id=OLD.id"},
		} {
			addTrigger("trg_recordcancel81_"+parent.table+"_"+strings.ToLower(action), parent.table, action,
				"EXISTS(SELECT 1 FROM investment_record_cancellations c WHERE c.workspace_id=OLD.workspace_id AND "+parent.match+")")
		}
	}
	// The investment account itself stays fully usable: independent movements
	// and later lots on the same account are preserved, and the receipt foreign
	// key already refuses the account's deletion. No account trigger exists.
	// The cancelled record-only lot accepts no further operation of its own:
	// no purchase, position, withdrawal, settlement or integration may ever be
	// attached to it (same dependent family as schema 79).
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
				condition += "EXISTS(SELECT 1 FROM investment_record_cancellations c WHERE c.workspace_id=" + ref + ".workspace_id AND c.investment_id=" + ref + ".investment_id)"
			}
			addTrigger("trg_recordcancel81_child_"+child+"_"+strings.ToLower(action), child, action, condition)
		}
	}
	// An account remains usable. Only the adoption declaration about the
	// investment account is incompatible with an already cancelled lot.
	for _, action := range []string{"INSERT", "UPDATE"} {
		condition := `EXISTS(SELECT 1 FROM investment_record_cancellations c WHERE c.workspace_id=NEW.workspace_id AND c.investment_account_id=NEW.account_id)`
		if action == "UPDATE" {
			condition += ` OR EXISTS(SELECT 1 FROM investment_record_cancellations c WHERE c.workspace_id=OLD.workspace_id AND c.investment_account_id=OLD.account_id)`
		}
		addTrigger("trg_recordcancel81_adoption_"+strings.ToLower(action), "investment_adoption_confirmations", action, condition)
	}
	// The absent automatic funding transfer cannot be fabricated afterwards:
	// this closes the only path that could later satisfy the schema 79 funded
	// cancellation relation for an already record-cancelled lot.
	for _, action := range []string{"INSERT", "UPDATE"} {
		condition := `EXISTS(SELECT 1 FROM investment_record_cancellations c WHERE c.workspace_id=NEW.workspace_id AND NEW.id='investment-purchase:'||c.purchase_id)`
		if action == "UPDATE" {
			condition += ` OR EXISTS(SELECT 1 FROM investment_record_cancellations c WHERE c.workspace_id=OLD.workspace_id AND OLD.id='investment-purchase:'||c.purchase_id)`
		}
		addTrigger("trg_recordcancel81_funding_"+strings.ToLower(action), "transactions", action, condition)
	}
	return objects
}

func recordCancellation81Collision(r string) string {
	return `EXISTS(SELECT 1 FROM investment_record_cancellations x WHERE x.workspace_id=` + r + `.workspace_id AND
 (x.id=` + r + `.id OR x.investment_id=` + r + `.investment_id OR x.purchase_id=` + r + `.purchase_id OR x.request_id=` + r + `.request_id OR x.preview_id=` + r + `.preview_id))`
}

func recordCancellation81JSON(r string) string {
	checks := []string{
		"json_type(" + r + ".payload_json) IS 'object'", "json_type(" + r + ".evidence_json) IS 'object'",
		"json_extract(" + r + ".payload_json,'$.version') IS 1",
		"json_extract(" + r + ".payload_json,'$.operation') IS 'CANCEL_INITIAL_RECORD_INVESTMENT'",
		"json_type(" + r + ".payload_json,'$.confirmed') IS 'true'",
		"json_type(" + r + ".payload_json,'$.preview_token') IS 'text'",
		"length(json_extract(" + r + ".payload_json,'$.preview_token'))>0",
	}
	for _, field := range []struct{ json, column string }{
		{"workspace_id", "workspace_id"}, {"acting_user_id", "created_by"}, {"investment_id", "investment_id"},
		{"request_id", "request_id"}, {"reason", "reason"},
	} {
		checks = append(checks, "json_extract("+r+".payload_json,'$."+field.json+"') IS "+r+"."+field.column)
	}
	for _, field := range []string{"workspace_id", "investment_id", "purchase_id", "investment_account_id", "amount_cents", "economic_date", "evidence_hash"} {
		checks = append(checks, "json_extract("+r+".evidence_json,'$."+field+"') IS "+r+"."+field)
	}
	return "(" + strings.Join(checks, " AND ") + ")"
}

// recordCancellation81Relation validates the whole contract of the receipt.
// The common arm requires the eligible record-only initial registration: one
// initial purchase mirroring the investment, no automatic funding transfer, no
// funded cancellation (79), no individual reversal (80) and none of the schema
// 79 dependent family. The insert arm additionally pins the observed account
// balance and version, proving the zero-effect receipt recorded reality; the
// revalidation arm drops those pins so independent account movements never
// invalidate a historical receipt (mirroring schema 79).
func recordCancellation81Relation(r string, inserting bool) string {
	q := `EXISTS(SELECT 1 FROM investments i
 JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id AND p.id=` + r + `.purchase_id
 JOIN accounts a ON a.workspace_id=i.workspace_id AND a.id=i.account_id AND a.id=` + r + `.investment_account_id
 WHERE i.workspace_id=` + r + `.workspace_id AND i.id=` + r + `.investment_id AND i.account_id=a.id
 AND i.status='ATIVA' AND i.archived_at IS NULL AND p.is_initial=1 AND p.id='initial:'||i.id
 AND p.principal_cents=i.principal_cents AND p.principal_cents=` + r + `.amount_cents
 AND p.purchase_date=i.applied_date AND p.created_by=i.created_by AND p.created_at=i.created_at
 AND p.purchase_date<=` + r + `.economic_date
 AND (SELECT count(*) FROM investment_purchases x WHERE x.workspace_id=i.workspace_id AND x.investment_id=i.id)=1
 AND NOT EXISTS(SELECT 1 FROM transactions o WHERE o.workspace_id=i.workspace_id AND o.id='investment-purchase:'||p.id)
 AND NOT EXISTS(SELECT 1 FROM investment_cancellations c WHERE c.workspace_id=i.workspace_id AND c.investment_id=i.id)
 AND NOT EXISTS(SELECT 1 FROM investment_purchase_reversals v WHERE v.workspace_id=i.workspace_id AND v.investment_id=i.id)
 AND NOT EXISTS(SELECT 1 FROM account_balance_adjustments x WHERE x.workspace_id=i.workspace_id AND x.account_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM investment_adoption_confirmations x WHERE x.workspace_id=i.workspace_id AND x.account_id=a.id)`
	if inserting {
		q += ` AND a.current_balance=` + r + `.investment_before_cents AND a.balance_version=` + r + `.investment_before_version`
	}
	for _, child := range cancellation79Children {
		if child == "investment_purchases" {
			continue
		}
		q += " AND NOT EXISTS(SELECT 1 FROM " + child + " x WHERE x.workspace_id=i.workspace_id AND x.investment_id=i.id)"
	}
	return q + ")"
}

func migrateInvestmentRecordCancellations(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investments")
	if err != nil || !exists {
		return err
	} // Historical partial-schema migration probes.
	if err := preflightInvestmentRecordCancellations81(tx); err != nil {
		return err
	}
	var before string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&before); err != nil {
		return err
	}
	for _, object := range recordCancellation81Objects() {
		if _, err := tx.Exec(object.ddl); err != nil {
			return fmt.Errorf("create %s: %w", object.name, err)
		}
	}
	if err := validateRecordCancellation81Structure(tx); err != nil {
		return err
	}
	if err := sqlite79Integrity(tx); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM investment_record_cancellations`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("migration 81 created financial receipts")
	}
	var after string
	if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&after); err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("migration 81 changed transactions schema")
	}
	return nil
}
