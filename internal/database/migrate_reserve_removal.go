package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// Migration 83 removes the reserve domain physically. It is intentionally
// conservative: if a historical reserve link touches an investment purchase,
// receipt, classification transaction or crosses workspaces, it aborts before
// changing anything. Common expense links are deleted with the rest of the
// reserve history because they have no investment-cancellation meaning.
var reserveRemovalTables = []string{
	"box_contribution_lifecycle_events",
	"box_virtual_ledger",
	"boxes",
}

var reserveRemovalIndexes = []string{
	"idx_box_contribution_lifecycle_events_box_occurred",
	"idx_box_virtual_ledger_box",
	"idx_box_virtual_ledger_box_reference_date",
	"idx_box_virtual_ledger_automatic_recharge_month",
	"idx_box_virtual_ledger_reversal_of",
	"idx_box_virtual_ledger_source_transaction",
	"idx_boxes_workspace",
	"idx_boxes_workspace_category",
}

var reserveRemovalReserveTriggers = map[string]bool{
	"trg_c2b_reserve":        true,
	"trg_c2b_reserve_update": true,
	"trg_cancellation79_link_box_virtual_ledger_source_transaction_id_insert": true,
	"trg_cancellation79_link_box_virtual_ledger_source_transaction_id_update": true,
	"trg_reversal80_link_box_virtual_ledger_source_transaction_id_insert":     true,
	"trg_reversal80_link_box_virtual_ledger_source_transaction_id_update":     true,
}

var reserveRemovalInvestmentTriggers = []string{
	"trg_cancellation79_insert",
	"trg_reversal80_insert",
}

const (
	reservePhysicalRemovalSourceVersion = 82
	reservePhysicalRemovalVersion       = 83
	reserveRemovalNotificationPredicate = `type LIKE 'caixinha.%' OR dedupe_key LIKE 'caixinha.%'`
	reserveRemovalDismissalPredicate    = `notification_key LIKE 'box_goal:%' OR notification_key LIKE 'caixinha_notif:%' OR notification_key LIKE 'caixinha.%'`
)

func hasReservePhysicalRemoval(version int) bool {
	return version >= reservePhysicalRemovalVersion
}

func migrateReservePhysicalRemoval(tx *sql.Tx) error {
	version, err := sqlite79Inspect(tx)
	if err != nil {
		return fmt.Errorf("migration 83 requires intact schema 82: %w", err)
	}
	if version != reservePhysicalRemovalSourceVersion {
		return fmt.Errorf("migration %d requires schema %d, found %d", reservePhysicalRemovalVersion, reservePhysicalRemovalSourceVersion, version)
	}

	if err := preflightReservePhysicalRemoval(tx); err != nil {
		return err
	}

	before, err := reserveRemovalFinancialSnapshot(tx)
	if err != nil {
		return err
	}

	if err := replaceReserveInvestmentInsertTriggers(tx); err != nil {
		return err
	}
	for _, index := range reserveRemovalIndexes {
		if _, err := tx.Exec(`DROP INDEX IF EXISTS ` + index); err != nil {
			return fmt.Errorf("migration 83 drop index %s: %w", index, err)
		}
	}
	for _, table := range reserveRemovalTables {
		if _, err := tx.Exec(`DROP TABLE ` + table); err != nil {
			return fmt.Errorf("migration 83 drop table %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM user_notifications WHERE ` + reserveRemovalNotificationPredicate); err != nil {
		return fmt.Errorf("migration 83 delete reserve notifications: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM user_notification_dismissals WHERE ` + reserveRemovalDismissalPredicate); err != nil {
		return fmt.Errorf("migration 83 delete reserve notification dismissals: %w", err)
	}

	if err := postflightReservePhysicalRemoval(tx, before); err != nil {
		return err
	}
	return nil
}

func preflightReservePhysicalRemoval(tx *sql.Tx) error {
	for _, table := range reserveRemovalTables {
		exists, err := tableExists(tx, table)
		if err != nil {
			return fmt.Errorf("migration 83 inspect %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("migration 83 expected existing table %s", table)
		}
	}

	if err := checkReserveRemovalObjects(tx); err != nil {
		return err
	}
	if err := checkReserveRemovalForeignKeys(tx); err != nil {
		return err
	}
	if err := checkReserveRemovalInvestmentLinks(tx); err != nil {
		return err
	}
	if err := sqlite79Integrity(tx); err != nil {
		return fmt.Errorf("migration 83 preflight integrity: %w", err)
	}
	return nil
}

func checkReserveRemovalObjects(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT type, name, COALESCE(tbl_name, ''), COALESCE(sql, '') FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return fmt.Errorf("migration 83 inspect schema objects: %w", err)
	}
	defer rows.Close()

	allowedReserveTables := map[string]bool{}
	for _, table := range reserveRemovalTables {
		allowedReserveTables[table] = true
	}
	for rows.Next() {
		var kind, name, table, ddl string
		if err := rows.Scan(&kind, &name, &table, &ddl); err != nil {
			return fmt.Errorf("migration 83 scan schema object: %w", err)
		}
		lowerDDL := strings.ToLower(ddl)
		referencesReserve := strings.Contains(lowerDDL, "boxes") ||
			strings.Contains(lowerDDL, "box_virtual_ledger") ||
			strings.Contains(lowerDDL, "box_contribution_lifecycle_events")
		if !referencesReserve {
			continue
		}
		switch {
		case kind == "table" && allowedReserveTables[name]:
			// The table itself is expected to mention its own name.
		case kind == "index" && allowedReserveTables[table]:
			// Indexes are part of the reserve tables being removed.
		case kind == "trigger" && reserveRemovalReserveTriggers[name]:
			// Known trigger from schema 76/79/80 attached to the ledger.
		case kind == "trigger" && (name == "trg_cancellation79_insert" || name == "trg_reversal80_insert"):
			// These triggers are validated and rewritten below.
		default:
			return fmt.Errorf("migration 83 unexpected schema object %s %s referencing reserves", kind, name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migration 83 iterate schema objects: %w", err)
	}
	return nil
}

func checkReserveRemovalForeignKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("migration 83 list tables for foreign keys: %w", err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return fmt.Errorf("migration 83 scan table for foreign keys: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migration 83 iterate tables for foreign keys: %w", err)
	}
	rows.Close()

	for _, table := range tables {
		fkRows, err := tx.Query(`PRAGMA foreign_key_list(` + table + `)`)
		if err != nil {
			return fmt.Errorf("migration 83 inspect foreign keys of %s: %w", table, err)
		}
		for fkRows.Next() {
			var id, seq int
			var parent, from, to, onUpdate, onDelete, match string
			if err := fkRows.Scan(&id, &seq, &parent, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				fkRows.Close()
				return fmt.Errorf("migration 83 scan foreign key of %s: %w", table, err)
			}
			if parent != "boxes" && parent != "box_virtual_ledger" && parent != "box_contribution_lifecycle_events" {
				continue
			}
			expected := (table == "box_virtual_ledger" && (parent == "boxes" || parent == "box_virtual_ledger")) ||
				(table == "box_contribution_lifecycle_events" && parent == "boxes")
			if !expected {
				fkRows.Close()
				return fmt.Errorf("migration 83 unexpected foreign key %s.%s -> %s", table, from, parent)
			}
		}
		if err := fkRows.Err(); err != nil {
			fkRows.Close()
			return fmt.Errorf("migration 83 iterate foreign keys of %s: %w", table, err)
		}
		fkRows.Close()
	}
	return nil
}

type reserveRemovalQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

const reserveRemovalRelevantLinksSQL = `
	WITH relevant(workspace_id, transaction_id) AS (
		SELECT workspace_id, 'investment-purchase:' || id FROM investment_purchases
		UNION SELECT workspace_id, transaction_id FROM investment_purchase_funding
		UNION SELECT workspace_id, original_transaction_id FROM investment_cancellations WHERE original_transaction_id <> ''
		UNION SELECT workspace_id, compensation_transaction_id FROM investment_cancellations WHERE compensation_transaction_id <> ''
		UNION SELECT workspace_id, original_transaction_id FROM investment_purchase_reversals WHERE original_transaction_id <> ''
		UNION SELECT workspace_id, compensation_transaction_id FROM investment_purchase_reversals WHERE compensation_transaction_id <> ''
		UNION SELECT workspace_id, id FROM transactions
			WHERE id LIKE 'investment-purchase:%'
			   OR id LIKE 'investment-cancellation:%'
			   OR id LIKE 'investment-reversal:%'
			   OR effect_mode = 'CLASSIFICATION_ONLY'
	)
	SELECT COUNT(1)
	FROM box_virtual_ledger l
	JOIN boxes b ON b.id = l.box_id
	JOIN relevant r ON r.workspace_id = b.workspace_id AND r.transaction_id = l.source_transaction_id`

func countReserveRemovalRelevantLinks(q reserveRemovalQueryer) (int64, error) {
	var count int64
	if err := q.QueryRow(reserveRemovalRelevantLinksSQL).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func checkReserveRemovalInvestmentLinks(tx *sql.Tx) error {
	var crossWorkspace int64
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		JOIN transactions t ON t.id = l.source_transaction_id
		WHERE t.workspace_id <> b.workspace_id
	`).Scan(&crossWorkspace); err != nil {
		return fmt.Errorf("migration 83 cross-workspace reserve links: %w", err)
	}
	if crossWorkspace != 0 {
		return fmt.Errorf("migration 83 blocked: %d reserve link(s) cross workspace boundaries", crossWorkspace)
	}

	var classificationLinks int64
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		JOIN transactions t ON t.id = l.source_transaction_id AND t.workspace_id = b.workspace_id
		WHERE t.effect_mode = 'CLASSIFICATION_ONLY'
	`).Scan(&classificationLinks); err != nil {
		return fmt.Errorf("migration 83 classification reserve links: %w", err)
	}
	if classificationLinks != 0 {
		return fmt.Errorf("migration 83 blocked: %d reserve link(s) reference classification-only investment transactions", classificationLinks)
	}

	// Account type alone is not a protected relationship. Ordinary expenses
	// and transfers on INVESTMENT accounts were never blockers for investment
	// cancellation/reversal; only the exact purchase/funding transfers,
	// receipt transfers and classification-only transactions are. The query
	// lives in a constant so its branches can be exercised on a minimal
	// synthetic schema without duplicating the production predicate.
	relevantLinks, err := countReserveRemovalRelevantLinks(tx)
	if err != nil {
		return fmt.Errorf("migration 83 relevant investment links: %w", err)
	}
	if relevantLinks != 0 {
		return fmt.Errorf("migration 83 blocked: %d reserve link(s) reference investment purchase, funding, receipt or classification transactions", relevantLinks)
	}

	var suspiciousOrphans int64
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		LEFT JOIN transactions t ON t.id = l.source_transaction_id
		WHERE t.id IS NULL
		  AND TRIM(COALESCE(l.source_transaction_id, '')) <> ''
	`).Scan(&suspiciousOrphans); err != nil {
		return fmt.Errorf("migration 83 orphan reserve links: %w", err)
	}
	if suspiciousOrphans != 0 {
		return fmt.Errorf("migration 83 blocked: %d reserve link(s) reference missing transactions", suspiciousOrphans)
	}
	return nil
}

func replaceReserveInvestmentInsertTriggers(tx *sql.Tx) error {
	replacements := map[string]string{}
	for _, object := range cancellation79ObjectsForSchema(reservePhysicalRemovalVersion) {
		if object.name == "trg_cancellation79_insert" {
			replacements[object.name] = object.ddl
		}
	}
	for _, object := range reversal80ObjectsForSchema(reservePhysicalRemovalVersion) {
		if object.name == "trg_reversal80_insert" {
			replacements[object.name] = object.ddl
		}
	}
	for _, name := range reserveRemovalInvestmentTriggers {
		ddl, ok := replacements[name]
		if !ok {
			return fmt.Errorf("migration 83 missing replacement trigger %s", name)
		}
		if _, err := tx.Exec(`DROP TRIGGER IF EXISTS ` + name); err != nil {
			return fmt.Errorf("migration 83 drop trigger %s: %w", name, err)
		}
		if _, err := tx.Exec(ddl); err != nil {
			return fmt.Errorf("migration 83 create trigger %s: %w", name, err)
		}
	}
	return nil
}

type reserveRemovalSnapshot struct {
	counts map[string]int64
	sums   map[string]int64
}

func reserveRemovalFinancialSnapshot(tx *sql.Tx) (reserveRemovalSnapshot, error) {
	snapshot := reserveRemovalSnapshot{counts: map[string]int64{}, sums: map[string]int64{}}
	for _, table := range []string{
		"accounts", "transactions", "invoices", "invoice_payments", "transaction_refunds",
		"invoice_refunds", "recurring_rules", "recurring_series", "cost_limits", "categories",
		"workspaces", "investments", "investment_purchases", "investment_cancellations",
		"investment_purchase_reversals", "investment_record_cancellations",
	} {
		var count int64
		if err := tx.QueryRow(`SELECT COUNT(1) FROM ` + table).Scan(&count); err != nil {
			return snapshot, fmt.Errorf("migration 83 snapshot count %s: %w", table, err)
		}
		snapshot.counts[table] = count
	}
	for table, column := range map[string]string{
		"accounts":     "current_balance",
		"transactions": "amount",
	} {
		var sum int64
		if err := tx.QueryRow(`SELECT COALESCE(SUM(` + column + `), 0) FROM ` + table).Scan(&sum); err != nil {
			return snapshot, fmt.Errorf("migration 83 snapshot sum %s.%s: %w", table, column, err)
		}
		snapshot.sums[table+"."+column] = sum
	}
	return snapshot, nil
}

func postflightReservePhysicalRemoval(tx *sql.Tx, before reserveRemovalSnapshot) error {
	for _, table := range reserveRemovalTables {
		exists, err := tableExists(tx, table)
		if err != nil {
			return fmt.Errorf("migration 83 postflight inspect %s: %w", table, err)
		}
		if exists {
			return fmt.Errorf("migration 83 postflight table %s still exists", table)
		}
	}
	rows, err := tx.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("migration 83 postflight inspect objects: %w", err)
	}
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			rows.Close()
			return fmt.Errorf("migration 83 postflight scan object: %w", err)
		}
		lower := strings.ToLower(ddl)
		if strings.Contains(lower, "box_virtual_ledger") || strings.Contains(lower, "box_contribution_lifecycle_events") ||
			strings.Contains(lower, "trg_c2b_reserve") || strings.Contains(lower, "boxes") {
			rows.Close()
			return fmt.Errorf("migration 83 postflight object %s %s still references reserves", kind, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("migration 83 postflight iterate objects: %w", err)
	}
	rows.Close()

	for _, name := range reserveRemovalInvestmentTriggers {
		var ddl string
		if err := tx.QueryRow(`SELECT COALESCE(sql, '') FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&ddl); err != nil {
			return fmt.Errorf("migration 83 postflight trigger %s: %w", name, err)
		}
		lower := strings.ToLower(ddl)
		if strings.Contains(lower, "boxes") || strings.Contains(lower, "box_virtual_ledger") {
			return fmt.Errorf("migration 83 postflight trigger %s still references reserves", name)
		}
	}

	var reserveNotifications int64
	if err := tx.QueryRow(`SELECT COUNT(1) FROM user_notifications WHERE ` + reserveRemovalNotificationPredicate).Scan(&reserveNotifications); err != nil {
		return fmt.Errorf("migration 83 postflight notifications: %w", err)
	}
	if reserveNotifications != 0 {
		return fmt.Errorf("migration 83 postflight retained %d reserve notifications", reserveNotifications)
	}
	var reserveDismissals int64
	if err := tx.QueryRow(`SELECT COUNT(1) FROM user_notification_dismissals WHERE ` + reserveRemovalDismissalPredicate).Scan(&reserveDismissals); err != nil {
		return fmt.Errorf("migration 83 postflight notification dismissals: %w", err)
	}
	if reserveDismissals != 0 {
		return fmt.Errorf("migration 83 postflight retained %d reserve notification dismissals", reserveDismissals)
	}

	after, err := reserveRemovalFinancialSnapshot(tx)
	if err != nil {
		return err
	}
	for table, count := range before.counts {
		if after.counts[table] != count {
			return fmt.Errorf("migration 83 changed financial row count of %s: before=%d after=%d", table, count, after.counts[table])
		}
	}
	for key, sum := range before.sums {
		if after.sums[key] != sum {
			return fmt.Errorf("migration 83 changed financial aggregate %s: before=%d after=%d", key, sum, after.sums[key])
		}
	}
	if err := sqlite79Integrity(tx); err != nil {
		return fmt.Errorf("migration 83 postflight integrity: %w", err)
	}
	return nil
}
