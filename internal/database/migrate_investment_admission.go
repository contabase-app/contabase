package database

import (
	"database/sql"
	"fmt"
	"strings"
)

// migrateInvestmentAdoptionAdmission installs the B-REL candidate after the
// dedicated 77->78 maintenance adapter has rebuilt transactions. It is never
// registered in the automatic runner. CBTX/CBMS remain the 37-field v1.
func migrateInvestmentAdoptionAdmission(tx *sql.Tx) error {
	exists, err := tableExists(tx, "investment_adoption_confirmations")
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("migration 78 requires valid schema 77 confirmation chain")
	}
	key, err := columnExists(tx, "transactions", "admission_occurrence_key")
	if err != nil {
		return err
	}
	if !key {
		return fmt.Errorf("migration 78 requires dedicated maintenance rebuild")
	}
	statements := admissionHeaderDDL()
	statements = append(statements, admissionSealDDL()...)
	statements = append(statements, admissionIndexDDL()...)
	statements = append(statements, admissionEvidenceSQL()...)
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			preview := statement
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			return fmt.Errorf("migration 78 [%s]: %w", preview, err)
		}
	}
	return nil
}

// admissionHeaderDDL creates the admission header, its journal, the six pin
// tables and the technical scope/witness tables (annex B §5; WITHOUT ROWID
// per §12, with no physical rowid identity to protect or attack).
func admissionHeaderDDL() []string {
	const fkSrc = " ON DELETE RESTRICT ON UPDATE RESTRICT NOT DEFERRABLE INITIALLY IMMEDIATE"
	return []string{
		`CREATE TABLE IF NOT EXISTS investment_adoption_admissions (
		 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
		 confirmation_id TEXT NOT NULL, confirmation_version INTEGER NOT NULL CHECK(typeof(confirmation_version)='integer' AND confirmation_version>0),
		 cut_date TEXT NOT NULL ` + referenceCivilDateCheck("cut_date") + `,
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
		 financial_effect_cents INTEGER NOT NULL DEFAULT 0 CHECK(typeof(financial_effect_cents)='integer' AND financial_effect_cents=0),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 result_json TEXT NOT NULL ` + admissionReceiptJSONCheck() + `,
		 PRIMARY KEY(workspace_id,account_id), UNIQUE(workspace_id,account_id,id), UNIQUE(workspace_id,account_id,request_id),
		 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,account_id,confirmation_id,confirmation_version) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id,version) ON DELETE RESTRICT
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_attempts (
		 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL, id TEXT NOT NULL,
		 request_id TEXT NOT NULL CHECK(length(request_id)=36),
		 created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at INTEGER NOT NULL CHECK(typeof(created_at)='integer' AND created_at>0),
		 reason TEXT NOT NULL CHECK(length(trim(reason)) BETWEEN 1 AND 500),
		 code TEXT NOT NULL CHECK(code IN ('CONFLICT','VALIDATION','NOT_FOUND','INVALID_STATUS','STALE_EVIDENCE','ALREADY_ADMITTED')),
		 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
		 confirmation_id TEXT, conflicting_admission_id TEXT,
		 PRIMARY KEY(workspace_id,account_id,id),
		 FOREIGN KEY(account_id,workspace_id) REFERENCES accounts(id,workspace_id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,account_id,confirmation_id) REFERENCES investment_adoption_confirmations(workspace_id,account_id,id) ON DELETE RESTRICT,
		 FOREIGN KEY(workspace_id,account_id,conflicting_admission_id) REFERENCES investment_adoption_admissions(workspace_id,account_id,id) ON DELETE RESTRICT,
		 CHECK(conflicting_admission_id IS NULL OR code IN ('CONFLICT','ALREADY_ADMITTED'))
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_transactions (
		 workspace_id TEXT NOT NULL, transaction_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,transaction_id,account_id),
		 FOREIGN KEY(workspace_id,transaction_id) REFERENCES transactions(workspace_id,id)` + fkSrc + `,
		 FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_null_transactions (
         workspace_id TEXT NOT NULL, occurrence_key TEXT COLLATE BINARY NOT NULL, account_id TEXT NOT NULL,
         PRIMARY KEY(workspace_id,occurrence_key,account_id),
         FOREIGN KEY(workspace_id,occurrence_key) REFERENCES transactions(workspace_id,admission_occurrence_key)` + fkSrc + `,
         FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
        ) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_valuations (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, valuation_date TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,investment_id,valuation_date,account_id),
		 FOREIGN KEY(workspace_id,investment_id,valuation_date) REFERENCES investment_valuations(workspace_id,investment_id,valuation_date)` + fkSrc + `,
		 FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_purchases (
		 workspace_id TEXT NOT NULL, purchase_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,purchase_id,account_id),
		 FOREIGN KEY(workspace_id,purchase_id) REFERENCES investment_purchases(workspace_id,id)` + fkSrc + `,
		 FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_withdrawals (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,investment_id,withdrawal_id,account_id),
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id) REFERENCES investment_withdrawals(workspace_id,investment_id,id)` + fkSrc + `,
		 FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_allocations (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, withdrawal_id TEXT NOT NULL, purchase_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,investment_id,withdrawal_id,purchase_id,account_id),
		 FOREIGN KEY(workspace_id,investment_id,withdrawal_id,purchase_id) REFERENCES investment_withdrawal_allocations(workspace_id,investment_id,withdrawal_id,purchase_id)` + fkSrc + `,
		 FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_pin_items (
		 workspace_id TEXT NOT NULL, investment_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,investment_id,account_id),
		 FOREIGN KEY(workspace_id,investment_id) REFERENCES investment_adoption_items(workspace_id,investment_id)` + fkSrc + `,
		 FOREIGN KEY(workspace_id,account_id) REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_null_scopes (
		 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 PRIMARY KEY(workspace_id,account_id),
		 FOREIGN KEY(workspace_id,account_id)
		   REFERENCES investment_adoption_admissions(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS investment_admission_null_transaction_witness (
		 workspace_id TEXT NOT NULL, account_id TEXT NOT NULL,
		 encoding_version INTEGER NOT NULL CHECK(typeof(encoding_version)='integer' AND encoding_version=1),
		 group_count INTEGER NOT NULL CHECK(typeof(group_count)='integer' AND group_count>0),
		 occurrence_count INTEGER NOT NULL CHECK(typeof(occurrence_count)='integer' AND occurrence_count>=group_count),
		 canonical_multiset BLOB NOT NULL
		   CHECK(typeof(canonical_multiset)='blob' AND length(canonical_multiset)>0 AND length(canonical_multiset)<=67108864),
		 PRIMARY KEY(workspace_id,account_id),
		 FOREIGN KEY(workspace_id,account_id)
		   REFERENCES investment_admission_null_scopes(workspace_id,account_id)` + fkSrc + `
		) WITHOUT ROWID`,
	}
}

// admissionSealDDL preserves the financial seals of the approved admission
// contract: header guards, confirmation-chain seal and the temporal seals on
// the admitted account's history. Physical anti-REPLACE protection comes from
// the pins (FK RESTRICT) and exact occurrence pins, so these BEFORE
// triggers carry only NEW/OLD financial predicates — the WIP rowid-conflict
// branches and their rowid=-1 gap are superseded.
func admissionSealDDL() []string {
	var out []string
	add := func(ddl string) { out = append(out, ddl) }
	add(`CREATE TRIGGER IF NOT EXISTS trg_admission_occurrence_insert BEFORE INSERT ON transactions
    WHEN NEW.admission_occurrence_key IS NULL OR typeof(NEW.admission_occurrence_key)!='text'
      OR length(NEW.admission_occurrence_key)!=32 OR length(CAST(NEW.admission_occurrence_key AS BLOB))!=32 OR NEW.admission_occurrence_key GLOB '*[^0-9a-f]*'
      OR EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=NEW.workspace_id AND t.admission_occurrence_key=NEW.admission_occurrence_key)
    BEGIN SELECT RAISE(ABORT,'investment admission integrity'); END`)
	add(`CREATE TRIGGER IF NOT EXISTS trg_admission_occurrence_update BEFORE UPDATE ON transactions
    WHEN NEW.admission_occurrence_key IS NOT OLD.admission_occurrence_key
      OR (NEW.workspace_id IS NOT OLD.workspace_id AND EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=NEW.workspace_id AND t.admission_occurrence_key=NEW.admission_occurrence_key))
    BEGIN SELECT RAISE(ABORT,'investment admission integrity'); END`)

	add(`CREATE TRIGGER IF NOT EXISTS trg_admission_insert BEFORE INSERT ON investment_adoption_admissions
		 WHEN EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=NEW.workspace_id AND a.account_id=NEW.account_id)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_confirmations c WHERE c.workspace_id=NEW.workspace_id AND c.account_id=NEW.account_id AND c.id=NEW.confirmation_id AND c.version=NEW.confirmation_version AND c.cut_date=NEW.cut_date AND c.request_id=json_extract(NEW.result_json,'$.Confirmation.RequestID') AND c.created_by=json_extract(NEW.result_json,'$.Confirmation.CreatedBy'))
		 OR NEW.confirmation_version!=(SELECT COALESCE(MAX(version),0) FROM investment_adoption_confirmations WHERE workspace_id=NEW.workspace_id AND account_id=NEW.account_id)
		 BEGIN SELECT RAISE(ABORT,'invalid financial admission'); END`)
	add(`CREATE TRIGGER IF NOT EXISTS trg_admission_attempt_insert BEFORE INSERT ON investment_admission_attempts
		 WHEN EXISTS(SELECT 1 FROM investment_admission_attempts a WHERE a.workspace_id=NEW.workspace_id AND a.account_id=NEW.account_id AND a.id=NEW.id)
		 BEGIN SELECT RAISE(ABORT,'invalid admission attempt'); END`)
	add(`CREATE TRIGGER IF NOT EXISTS trg_admission_confirmation_seal BEFORE INSERT ON investment_adoption_confirmations
		 WHEN EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=NEW.workspace_id AND a.account_id=NEW.account_id)
		 BEGIN SELECT RAISE(ABORT,'admitted confirmation is sealed'); END`)
	add(`CREATE TRIGGER IF NOT EXISTS trg_admission_confirmation_item_seal BEFORE INSERT ON investment_adoption_items
		 WHEN EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=NEW.workspace_id AND a.account_id=NEW.account_id)
		 BEGIN SELECT RAISE(ABORT,'admitted confirmation is sealed'); END`)

	admittedSource := func(date string) string {
		return fmt.Sprintf(`EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id WHERE a.workspace_id=%%[1]s.workspace_id AND i.id=%%[1]s.investment_id AND %%[1]s.%[1]s<=a.cut_date)`, date)
	}
	_ = admittedSource
	for _, source := range []struct{ short, table, date string }{
		{"valuation", "investment_valuations", "valuation_date"},
		{"purchase", "investment_purchases", "purchase_date"},
		{"withdrawal", "investment_withdrawals", "withdrawal_date"},
	} {
		pred := fmt.Sprintf(`EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id WHERE a.workspace_id=%%[1]s.workspace_id AND i.id=%%[1]s.investment_id AND %%[1]s.%[1]s<=a.cut_date)`, source.date)
		for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
			condition := ""
			for _, row := range []string{"OLD", "NEW"} {
				if (action == "INSERT" && row == "OLD") || (action == "DELETE" && row == "NEW") {
					continue
				}
				if condition != "" {
					condition += " OR "
				}
				condition += fmt.Sprintf(pred, row)
			}
			add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_admission_%s_%s BEFORE %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'admitted %s is sealed'); END`, source.short, strings.ToLower(action), action, source.table, condition, source.short))
		}
	}
	allocationPred := `EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_withdrawals w ON w.workspace_id=i.workspace_id AND w.investment_id=i.id WHERE w.workspace_id=%[1]s.workspace_id AND w.investment_id=%[1]s.investment_id AND w.id=%[1]s.withdrawal_id AND w.withdrawal_date<=a.cut_date)`
	for _, action := range []string{"INSERT", "UPDATE", "DELETE"} {
		condition := ""
		for _, row := range []string{"OLD", "NEW"} {
			if (action == "INSERT" && row == "OLD") || (action == "DELETE" && row == "NEW") {
				continue
			}
			if condition != "" {
				condition += " OR "
			}
			condition += fmt.Sprintf(allocationPred, row)
		}
		add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_admission_allocation_%s BEFORE %s ON investment_withdrawal_allocations WHEN %s BEGIN SELECT RAISE(ABORT,'admitted allocation is sealed'); END`, strings.ToLower(action), action, condition))
	}

	admittedTransaction := func(row string) string {
		return fmt.Sprintf(`EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=%[1]s.workspace_id AND (a.account_id=%[1]s.account_id OR a.account_id=%[1]s.destination_account_id) AND (%[1]s.type IN ('INCOME','EXPENSE') OR (%[1]s.type='TRANSFER' AND (%[1]s.date_civil IS NULL OR %[1]s.date_civil<=a.cut_date))))`, row)
	}
	add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_admission_transaction_insert BEFORE INSERT ON transactions WHEN NEW.effect_mode='STANDARD' AND %[1]s BEGIN SELECT RAISE(ABORT,'admitted transaction is sealed'); END`, admittedTransaction("NEW")))
	add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_admission_transaction_update BEFORE UPDATE ON transactions WHEN (OLD.effect_mode='STANDARD' AND %[1]s) OR (NEW.effect_mode='STANDARD' AND %[2]s) BEGIN SELECT RAISE(ABORT,'admitted transaction is sealed'); END`, admittedTransaction("OLD"), admittedTransaction("NEW")))
	add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_admission_transaction_delete BEFORE DELETE ON transactions WHEN OLD.effect_mode='STANDARD' AND %[1]s BEGIN SELECT RAISE(ABORT,'admitted transaction is sealed'); END`, admittedTransaction("OLD")))

	for _, table := range []string{"investment_adoption_admissions", "investment_admission_attempts",
		"investment_admission_null_scopes", "investment_admission_null_transaction_witness"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_immutable_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'immutable investment admission'); END`, table, strings.ToLower(action), action, table))
		}
	}
	for _, pin := range []string{"transactions", "null_transactions", "valuations", "purchases", "withdrawals", "allocations", "items"} {
		for _, action := range []string{"UPDATE", "DELETE"} {
			add(fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_%s_%s BEFORE %s ON investment_admission_pin_%s BEGIN SELECT RAISE(ABORT,'immutable investment admission pin'); END`, pin, strings.ToLower(action), action, pin))
		}
	}
	return out
}

// admissionIndexDDL adds the supporting indexes of annex B §10, including the
// two partial origin/destination indexes over legacy NULL STANDARD rows (a
// physical index build, not evidence capture and not a financial backfill).
func admissionIndexDDL() []string {
	out := []string{
		`CREATE INDEX IF NOT EXISTS idx_admission_attempts_created ON investment_admission_attempts(workspace_id,account_id,created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_admission_attempts_confirmation ON investment_admission_attempts(workspace_id,account_id,confirmation_id)`,
		`CREATE INDEX IF NOT EXISTS idx_admission_attempts_conflicting ON investment_admission_attempts(workspace_id,account_id,conflicting_admission_id)`,
		`CREATE INDEX IF NOT EXISTS idx_admission_attempts_creator ON investment_admission_attempts(created_by)`,
		`CREATE INDEX IF NOT EXISTS idx_admission_admissions_creator ON investment_adoption_admissions(created_by)`,
		`CREATE INDEX IF NOT EXISTS idx_admission_null_source
		 ON transactions(workspace_id,account_id,type,date_civil)
		 WHERE id IS NULL AND effect_mode='STANDARD'`,
		`CREATE INDEX IF NOT EXISTS idx_admission_null_destination
		 ON transactions(workspace_id,destination_account_id,type,date_civil)
		 WHERE id IS NULL AND effect_mode='STANDARD'`,
	}
	for _, pin := range []string{"transactions", "null_transactions", "valuations", "purchases", "withdrawals", "allocations", "items"} {
		out = append(out, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_admission_pin_%s_admission ON investment_admission_pin_%s(workspace_id,account_id)`, pin, pin))
	}
	return out
}

// admissionCanonicalScalar renders the fail-closed canonical encoding of one
// column (annex B §8.1). REAL values only receive a token when the numeric
// round-trip of their representation is exact; otherwise the identity is NULL
// (invalid) and every consumer aborts.
func admissionCanonicalScalar(col string) string {
	return "CASE typeof(" + col + ") " +
		"WHEN 'null' THEN 'N;' " +
		"WHEN 'integer' THEN 'I'||printf('%lld'," + col + ")||';' " +
		"WHEN 'real' THEN CASE " +
		"WHEN " + col + "=0 THEN 'R0;' " +
		"WHEN " + col + "=9e999 THEN 'R+Inf;' " +
		"WHEN " + col + "=-9e999 THEN 'R-Inf;' " +
		"WHEN CAST(printf('%!.26g'," + col + ") AS REAL) IS " + col + " THEN 'R'||printf('%!.26g'," + col + ")||';' " +
		"ELSE NULL END " +
		"WHEN 'text' THEN 'T'||length(CAST(" + col + " AS BLOB))||':'||hex(CAST(" + col + " AS BLOB))||';' " +
		"WHEN 'blob' THEN 'B'||length(" + col + ")||':'||hex(" + col + ")||';' " +
		"END"
}

// admissionCanonicalRow is the 37-column identity prefix CBTX|1|37| followed
// by every encoded column in the versioned v1 order (transactions-columns-v1).
func admissionCanonicalRow(alias string) string {
	cols := []string{
		"id", "workspace_id", "user_id", "account_id", "destination_account_id",
		"category_id", "invoice_id", "invoice_override_id", "type", "amount", "date",
		"description", "notes", "attachment_path", "installment_number", "total_installments",
		"parent_id", "recurring_rule_id", "recurrence_sequence", "series_id",
		"is_recurring_exception", "recurring_exception_reason", "payment_status", "status",
		"due_date", "paid_at", "scheduled_payment_at", "scheduled_payment_created_by",
		"scheduled_payment_executed_at", "contact_id", "created_at", "updated_at",
		"date_civil", "due_date_civil", "paid_date_civil", "scheduled_payment_date_civil",
		"effect_mode",
	}
	parts := make([]string, 0, len(cols)+1)
	parts = append(parts, "'CBTX|1|37|'")
	for _, c := range cols {
		parts = append(parts, admissionCanonicalScalar(alias+"."+c))
	}
	return strings.Join(parts, " || ")
}

// admissionFinancial renders the annex B §6 financial predicate shared by the
// pin selection (with non-NULL id) and the NULL witness selection: explicit
// workspace, STANDARD mode, origin/destination branches without double
// counting, and the type/cut-date rule of the admission.
func admissionFinancial(t, a string) string {
	return t + ".workspace_id=" + a + ".workspace_id AND " + t + ".effect_mode='STANDARD' " +
		"AND (" + t + ".account_id=" + a + ".account_id OR (" + t + ".destination_account_id=" + a + ".account_id " +
		"AND " + t + ".account_id IS NOT " + a + ".account_id)) " +
		"AND (" + t + ".type IN ('INCOME','EXPENSE') OR (" + t + ".type='TRANSFER' AND (" + t + ".date_civil IS NULL OR " + t + ".date_civil<=" + a + ".cut_date)))"
}

// admissionScopeState renders a WITH clause computing the grouped canonical
// NULL state of one scope: adm1 is the admission row selected by the given
// workspace/account SQL expressions; st exposes gc, oc, invalid and env.
// Consumers append " SELECT ... FROM st WHERE ...".
func admissionScopeState(w, a string) string {
	return "WITH adm1 AS MATERIALIZED (SELECT workspace_id, account_id, cut_date FROM investment_adoption_admissions WHERE workspace_id=" + w + " AND account_id=" + a + "), " +
		"elig AS MATERIALIZED (SELECT " + admissionCanonicalRow("t") + " AS cr FROM transactions t JOIN adm1 a ON t.workspace_id=a.workspace_id " +
		"WHERE t.id IS NULL AND " + admissionFinancial("t", "a") + "), " +
		"groups AS MATERIALIZED (SELECT cr, COUNT(*) m FROM elig GROUP BY cr), " +
		"st AS (SELECT COUNT(*) gc, COALESCE(SUM(m),0) oc, COALESCE(SUM(CASE WHEN cr IS NULL THEN 1 ELSE 0 END),0) invalid, " +
		"group_concat(CAST(length(cr) AS TEXT)||':'||CAST(cr AS TEXT)||printf('%lld;',m), '' ORDER BY cr) FILTER (WHERE cr IS NOT NULL) env " +
		"FROM groups)"
}

// admissionEnvelope renders the full envelope BLOB from a state row.
func admissionEnvelope(z string) string {
	return "CAST('CBMS|1|'||" + z + ".gc||'|'||" + z + ".oc||'|'||COALESCE(" + z + ".env,'') AS BLOB)"
}

// admissionEvidenceSQL generates the pin INSERT guards, the scope guard, the
// witness authentication, the admission capture trigger (annex B §7.1) and
// exact occurrence retention.
func admissionEvidenceSQL() []string {
	var out []string
	const W, A = "NEW.workspace_id", "NEW.account_id"
	state := admissionScopeState(W, A)
	// Existence and invalid-encoding checks do not consume a multiset. Keep
	// their exact selection/encoding predicates, without sorting or building
	// an envelope that their callers discard. Authentication still recomputes
	// and compares the complete canonical bytes in both mandatory checks.
	nullSource := "SELECT 1 FROM investment_adoption_admissions a JOIN transactions t ON t.workspace_id=a.workspace_id WHERE a.workspace_id=" + W + " AND a.account_id=" + A + " AND t.id IS NULL AND " + admissionFinancial("t", "a")

	// Pin INSERT guards: duplicate rejection, admission ownership and exact
	// source membership in the §6 selection.
	out = append(out,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_transactions_insert BEFORE INSERT ON investment_admission_pin_transactions
		 WHEN EXISTS(SELECT 1 FROM investment_admission_pin_transactions p WHERE p.workspace_id=`+W+` AND p.transaction_id=NEW.transaction_id AND p.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN transactions t ON t.workspace_id=a.workspace_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND t.id=NEW.transaction_id AND t.id IS NOT NULL AND `+admissionFinancial("t", "a")+`)
		 BEGIN SELECT RAISE(ABORT,'invalid admission pin'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_valuations_insert BEFORE INSERT ON investment_admission_pin_valuations
		 WHEN EXISTS(SELECT 1 FROM investment_admission_pin_valuations p WHERE p.workspace_id=`+W+` AND p.investment_id=NEW.investment_id AND p.valuation_date=NEW.valuation_date AND p.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.id=NEW.investment_id AND i.account_id=a.account_id JOIN investment_valuations v ON v.workspace_id=a.workspace_id AND v.investment_id=NEW.investment_id AND v.valuation_date=NEW.valuation_date WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND v.valuation_date<=a.cut_date)
		 BEGIN SELECT RAISE(ABORT,'invalid admission pin'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_purchases_insert BEFORE INSERT ON investment_admission_pin_purchases
		 WHEN EXISTS(SELECT 1 FROM investment_admission_pin_purchases p WHERE p.workspace_id=`+W+` AND p.purchase_id=NEW.purchase_id AND p.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND p.id=NEW.purchase_id AND p.purchase_date<=a.cut_date)
		 BEGIN SELECT RAISE(ABORT,'invalid admission pin'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_withdrawals_insert BEFORE INSERT ON investment_admission_pin_withdrawals
		 WHEN EXISTS(SELECT 1 FROM investment_admission_pin_withdrawals p WHERE p.workspace_id=`+W+` AND p.investment_id=NEW.investment_id AND p.withdrawal_id=NEW.withdrawal_id AND p.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.id=NEW.investment_id AND i.account_id=a.account_id JOIN investment_withdrawals w ON w.workspace_id=a.workspace_id AND w.investment_id=NEW.investment_id AND w.id=NEW.withdrawal_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND w.withdrawal_date<=a.cut_date)
		 BEGIN SELECT RAISE(ABORT,'invalid admission pin'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_allocations_insert BEFORE INSERT ON investment_admission_pin_allocations
		 WHEN EXISTS(SELECT 1 FROM investment_admission_pin_allocations p WHERE p.workspace_id=`+W+` AND p.investment_id=NEW.investment_id AND p.withdrawal_id=NEW.withdrawal_id AND p.purchase_id=NEW.purchase_id AND p.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.id=NEW.investment_id AND i.account_id=a.account_id JOIN investment_withdrawal_allocations x ON x.workspace_id=a.workspace_id AND x.investment_id=NEW.investment_id AND x.withdrawal_id=NEW.withdrawal_id AND x.purchase_id=NEW.purchase_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND EXISTS(SELECT 1 FROM investment_withdrawals w WHERE w.workspace_id=x.workspace_id AND w.investment_id=x.investment_id AND w.id=x.withdrawal_id AND w.withdrawal_date<=a.cut_date))
		 BEGIN SELECT RAISE(ABORT,'invalid admission pin'); END`,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_pin_items_insert BEFORE INSERT ON investment_admission_pin_items
		 WHEN EXISTS(SELECT 1 FROM investment_admission_pin_items p WHERE p.workspace_id=`+W+` AND p.investment_id=NEW.investment_id AND p.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.id=NEW.investment_id AND i.account_id=a.account_id JOIN investment_adoption_items it ON it.workspace_id=a.workspace_id AND it.investment_id=NEW.investment_id AND it.account_id=a.account_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 BEGIN SELECT RAISE(ABORT,'invalid admission pin'); END`)

	nullPinGuard := strings.NewReplacer(
		"trg_admission_pin_transactions_insert", "trg_admission_pin_null_transactions_insert",
		"investment_admission_pin_transactions", "investment_admission_pin_null_transactions",
		"transaction_id", "occurrence_key",
		"t.id=NEW.transaction_id", "t.admission_occurrence_key=NEW.occurrence_key",
		"t.id IS NOT NULL", "t.id IS NULL",
	).Replace(out[0])
	// NewReplacer does not recursively replace its output; replace the source
	// identity after renaming the pin's field as well.
	nullPinGuard = strings.ReplaceAll(nullPinGuard, "t.id=NEW.occurrence_key", "t.admission_occurrence_key=NEW.occurrence_key")
	out = append(out, nullPinGuard)

	// Scope guard: duplicates rejected, admission required and at least one
	// eligible NULL source row for the exact (W,A) and its cut date.
	out = append(out,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_scope_insert BEFORE INSERT ON investment_admission_null_scopes
		 WHEN EXISTS(SELECT 1 FROM investment_admission_null_scopes s WHERE s.workspace_id=`+W+` AND s.account_id=`+A+`)
		 OR NOT EXISTS(SELECT 1 FROM investment_adoption_admissions a WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`)
		 OR NOT EXISTS(`+nullSource+`)
		 BEGIN SELECT RAISE(ABORT,'invalid admission scope'); END`)

	// Witness authentication (annex B §7.2): the only accepted row is the
	// exact recomputed integral state of the scope.
	out = append(out,
		`CREATE TRIGGER IF NOT EXISTS trg_admission_witness_insert BEFORE INSERT ON investment_admission_null_transaction_witness
		 WHEN EXISTS(SELECT 1 FROM investment_admission_null_transaction_witness w WHERE w.workspace_id=`+W+` AND w.account_id=`+A+`)
		 OR NEW.encoding_version IS NOT 1
		 OR typeof(NEW.canonical_multiset) IS NOT 'blob'
		 OR NOT EXISTS(SELECT 1 FROM investment_admission_null_scopes s JOIN investment_adoption_admissions a ON a.workspace_id=s.workspace_id AND a.account_id=s.account_id WHERE s.workspace_id=`+W+` AND s.account_id=`+A+`)
		 OR NOT EXISTS(`+state+` SELECT 1 FROM st WHERE st.invalid=0 AND st.gc=NEW.group_count AND st.oc=NEW.occurrence_count AND `+admissionEnvelope("st")+` = NEW.canonical_multiset)
		 BEGIN SELECT RAISE(ABORT,'investment admission integrity'); END`)

	// Capture: one AFTER INSERT on the admission performs the invalidity
	// guard, the six pin captures, scope+witness publication and the
	// two-directional completeness checks of annex B §7.1.
	out = append(out, `CREATE TRIGGER IF NOT EXISTS trg_admission_capture AFTER INSERT ON investment_adoption_admissions
BEGIN
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(`+nullSource+` AND (`+admissionCanonicalRow("t")+`) IS NULL);
	INSERT INTO investment_admission_pin_transactions(workspace_id,transaction_id,account_id)
	  SELECT t.workspace_id, t.id, `+A+` FROM investment_adoption_admissions a JOIN transactions t ON t.workspace_id=a.workspace_id
	  WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND t.id IS NOT NULL AND `+admissionFinancial("t", "a")+`;
    INSERT INTO investment_admission_pin_null_transactions(workspace_id,occurrence_key,account_id)
      SELECT t.workspace_id,t.admission_occurrence_key,`+A+` FROM investment_adoption_admissions a JOIN transactions t ON t.workspace_id=a.workspace_id
      WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND t.id IS NULL AND `+admissionFinancial("t", "a")+`;
	INSERT INTO investment_admission_pin_valuations(workspace_id,investment_id,valuation_date,account_id)
	  SELECT v.workspace_id, v.investment_id, v.valuation_date, `+A+` FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_valuations v ON v.workspace_id=i.workspace_id AND v.investment_id=i.id
	  WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND v.valuation_date<=a.cut_date;
	INSERT INTO investment_admission_pin_purchases(workspace_id,purchase_id,account_id)
	  SELECT p.workspace_id, p.id, `+A+` FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id
	  WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND p.purchase_date<=a.cut_date;
	INSERT INTO investment_admission_pin_withdrawals(workspace_id,investment_id,withdrawal_id,account_id)
	  SELECT w.workspace_id, w.investment_id, w.id, `+A+` FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_withdrawals w ON w.workspace_id=i.workspace_id AND w.investment_id=i.id
	  WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND w.withdrawal_date<=a.cut_date;
	INSERT INTO investment_admission_pin_allocations(workspace_id,investment_id,withdrawal_id,purchase_id,account_id)
	  SELECT x.workspace_id, x.investment_id, x.withdrawal_id, x.purchase_id, `+A+` FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_withdrawal_allocations x ON x.workspace_id=i.workspace_id AND x.investment_id=i.id
	  WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND EXISTS(SELECT 1 FROM investment_withdrawals w WHERE w.workspace_id=x.workspace_id AND w.investment_id=x.investment_id AND w.id=x.withdrawal_id AND w.withdrawal_date<=a.cut_date);
	INSERT INTO investment_admission_pin_items(workspace_id,investment_id,account_id)
	  SELECT it.workspace_id, it.investment_id, `+A+` FROM investment_adoption_admissions a JOIN investments i ON i.workspace_id=a.workspace_id AND i.account_id=a.account_id JOIN investment_adoption_items it ON it.workspace_id=i.workspace_id AND it.investment_id=i.id AND it.account_id=a.account_id
	  WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`;
	INSERT INTO investment_admission_null_scopes(workspace_id,account_id)
	  SELECT `+W+`, `+A+` WHERE EXISTS(`+nullSource+`);
	INSERT INTO investment_admission_null_transaction_witness(workspace_id,account_id,encoding_version,group_count,occurrence_count,canonical_multiset)
	  SELECT `+W+`, `+A+`, 1, q.gc, q.oc, q.blob FROM (`+state+` SELECT st.gc gc, st.oc oc, st.invalid invalid, `+admissionEnvelope("st")+` blob FROM st) q WHERE q.oc>0 AND q.invalid=0;
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  WITH expected AS (SELECT t.id v FROM investment_adoption_admissions a JOIN transactions t ON t.workspace_id=a.workspace_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND t.id IS NOT NULL AND `+admissionFinancial("t", "a")+`),
	       actual AS (SELECT p.transaction_id v FROM investment_admission_pin_transactions p WHERE p.workspace_id=`+W+` AND p.account_id=`+A+`)
	  SELECT 1 FROM (SELECT v FROM expected EXCEPT SELECT v FROM actual) UNION ALL SELECT 1 FROM (SELECT v FROM actual EXCEPT SELECT v FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
      WITH expected AS (SELECT t.workspace_id w,t.admission_occurrence_key k,a.account_id a FROM investment_adoption_admissions a JOIN transactions t ON t.workspace_id=a.workspace_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND t.id IS NULL AND `+admissionFinancial("t", "a")+`),
           actual AS (SELECT p.workspace_id w,p.occurrence_key k,p.account_id a FROM investment_admission_pin_null_transactions p WHERE p.workspace_id=`+W+` AND p.account_id=`+A+`)
      SELECT 1 FROM (SELECT w,k,a FROM expected EXCEPT SELECT w,k,a FROM actual) UNION ALL SELECT 1 FROM (SELECT w,k,a FROM actual EXCEPT SELECT w,k,a FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  WITH expected AS (SELECT v.investment_id i1, v.valuation_date d1 FROM investment_adoption_admissions a JOIN investments inv ON inv.workspace_id=a.workspace_id AND inv.account_id=a.account_id JOIN investment_valuations v ON v.workspace_id=inv.workspace_id AND v.investment_id=inv.id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND v.valuation_date<=a.cut_date),
	       actual AS (SELECT p.investment_id i1, p.valuation_date d1 FROM investment_admission_pin_valuations p WHERE p.workspace_id=`+W+` AND p.account_id=`+A+`)
	  SELECT 1 FROM (SELECT i1,d1 FROM expected EXCEPT SELECT i1,d1 FROM actual) UNION ALL SELECT 1 FROM (SELECT i1,d1 FROM actual EXCEPT SELECT i1,d1 FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  WITH expected AS (SELECT p.id v FROM investment_adoption_admissions a JOIN investments inv ON inv.workspace_id=a.workspace_id AND inv.account_id=a.account_id JOIN investment_purchases p ON p.workspace_id=inv.workspace_id AND p.investment_id=inv.id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND p.purchase_date<=a.cut_date),
	       actual AS (SELECT pin.purchase_id v FROM investment_admission_pin_purchases pin WHERE pin.workspace_id=`+W+` AND pin.account_id=`+A+`)
	  SELECT 1 FROM (SELECT v FROM expected EXCEPT SELECT v FROM actual) UNION ALL SELECT 1 FROM (SELECT v FROM actual EXCEPT SELECT v FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  WITH expected AS (SELECT w.investment_id i1, w.id v FROM investment_adoption_admissions a JOIN investments inv ON inv.workspace_id=a.workspace_id AND inv.account_id=a.account_id JOIN investment_withdrawals w ON w.workspace_id=inv.workspace_id AND w.investment_id=inv.id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND w.withdrawal_date<=a.cut_date),
	       actual AS (SELECT p.investment_id i1, p.withdrawal_id v FROM investment_admission_pin_withdrawals p WHERE p.workspace_id=`+W+` AND p.account_id=`+A+`)
	  SELECT 1 FROM (SELECT i1,v FROM expected EXCEPT SELECT i1,v FROM actual) UNION ALL SELECT 1 FROM (SELECT i1,v FROM actual EXCEPT SELECT i1,v FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  WITH expected AS (SELECT x.investment_id i1, x.withdrawal_id v, x.purchase_id v2 FROM investment_adoption_admissions a JOIN investments inv ON inv.workspace_id=a.workspace_id AND inv.account_id=a.account_id JOIN investment_withdrawal_allocations x ON x.workspace_id=inv.workspace_id AND x.investment_id=inv.id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+` AND EXISTS(SELECT 1 FROM investment_withdrawals w WHERE w.workspace_id=x.workspace_id AND w.investment_id=x.investment_id AND w.id=x.withdrawal_id AND w.withdrawal_date<=a.cut_date)),
	       actual AS (SELECT p.investment_id i1, p.withdrawal_id v, p.purchase_id v2 FROM investment_admission_pin_allocations p WHERE p.workspace_id=`+W+` AND p.account_id=`+A+`)
	  SELECT 1 FROM (SELECT i1,v,v2 FROM expected EXCEPT SELECT i1,v,v2 FROM actual) UNION ALL SELECT 1 FROM (SELECT i1,v,v2 FROM actual EXCEPT SELECT i1,v,v2 FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  WITH expected AS (SELECT it.investment_id v FROM investment_adoption_admissions a JOIN investments inv ON inv.workspace_id=a.workspace_id AND inv.account_id=a.account_id JOIN investment_adoption_items it ON it.workspace_id=inv.workspace_id AND it.investment_id=inv.id AND it.account_id=a.account_id WHERE a.workspace_id=`+W+` AND a.account_id=`+A+`),
	       actual AS (SELECT p.investment_id v FROM investment_admission_pin_items p WHERE p.workspace_id=`+W+` AND p.account_id=`+A+`)
	  SELECT 1 FROM (SELECT v FROM expected EXCEPT SELECT v FROM actual) UNION ALL SELECT 1 FROM (SELECT v FROM actual EXCEPT SELECT v FROM expected));
	SELECT RAISE(ABORT,'investment admission integrity') WHERE EXISTS(
	  `+state+` SELECT 1 FROM st WHERE
	    (st.oc>0 AND (NOT EXISTS(SELECT 1 FROM investment_admission_null_scopes s WHERE s.workspace_id=`+W+` AND s.account_id=`+A+`)
	                  OR NOT EXISTS(SELECT 1 FROM investment_admission_null_transaction_witness w WHERE w.workspace_id=`+W+` AND w.account_id=`+A+`
	                         AND w.group_count=st.gc AND w.occurrence_count=st.oc AND w.canonical_multiset=`+admissionEnvelope("st")+`)))
	    OR (st.oc=0 AND (EXISTS(SELECT 1 FROM investment_admission_null_scopes s WHERE s.workspace_id=`+W+` AND s.account_id=`+A+`)
	                     OR EXISTS(SELECT 1 FROM investment_admission_null_transaction_witness w WHERE w.workspace_id=`+W+` AND w.account_id=`+A+`))));
END`)

	// B-REL: integrated retention proof replaces per-write recomputation.
	// The three A2 AFTER verifiers are intentionally no longer installed.
	return out
}

// admissionReceiptJSONCheck is the row-local identity contract of the
// admission receipt: the header echo and the embedded confirmation identity.
// Cross-row receipt truth (the confirmation's request id and author) is
// enforced by trg_admission_insert against the referenced confirmation row.
func admissionReceiptJSONCheck() string {
	checks := []string{`json_type(result_json)='object'`}
	for _, field := range []struct{ name, column, kind string }{
		{"ID", "id", "text"}, {"WorkspaceID", "workspace_id", "text"}, {"AccountID", "account_id", "text"},
		{"ConfirmationID", "confirmation_id", "text"}, {"ConfirmationVersion", "confirmation_version", "integer"},
		{"CutDate", "cut_date", "text"}, {"RequestID", "request_id", "text"},
		{"CreatedBy", "created_by", "text"}, {"CreatedAt", "created_at", "integer"},
		{"Reason", "reason", "text"}, {"FinancialEffectCents", "financial_effect_cents", "integer"},
	} {
		checks = append(checks, fmt.Sprintf(`json_type(result_json,'$.%s')='%s' AND json_extract(result_json,'$.%s')=%s`, field.name, field.kind, field.name, field.column))
	}
	for _, field := range []struct{ name, column, kind string }{
		{"ID", "confirmation_id", "text"}, {"WorkspaceID", "workspace_id", "text"}, {"AccountID", "account_id", "text"},
		{"Version", "confirmation_version", "integer"}, {"CutDate", "cut_date", "text"},
	} {
		checks = append(checks, fmt.Sprintf(`json_type(result_json,'$.Confirmation.%s')='%s' AND json_extract(result_json,'$.Confirmation.%s')=%s`, field.name, field.kind, field.name, field.column))
	}
	checks = append(checks, `json_type(result_json,'$.Confirmation')='object'`)
	// Missing JSON paths evaluate to SQL NULL; NULL must reject, not pass.
	return `CHECK(CASE WHEN json_valid(result_json) THEN COALESCE((` + strings.Join(checks, ` AND `) + `),0) ELSE 0 END=1)`
}
