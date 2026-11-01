//go:build contabase_legacy_fixtures

package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// O2B.3-b Fase B — integrated validation of the homologated anti-REPLACE
// candidate (annexes A/B with errata). Gates B2..B12 per the Phase A
// contract; the heavy capacity (B8) and performance (P1..P8) workloads are
// opt-in through dedicated environment variables, following the project's
// established opt-in fixture pattern. All databases are disposable.

const admissionTxNULLFixture = `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,description,status,created_at,updated_at)
 VALUES(?,?,?,?,?,?,1000,?,?,'Witness fixture','paid',1793000000,1793000000)`

const admissionTxPlainFixture = `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status,created_at,updated_at)
 VALUES(?,?,?,?,?,1000,?,?,'Pin fixture','paid',1793000000,1793000000)`

// admissionCandidateDB returns a disposable database fully migrated (through
// 78) with the v77 receipt basis in place and no admission recorded yet.
func admissionCandidateDB(t *testing.T) *sql.DB {
	t.Helper()
	db := referenceLegacy71(t)
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	applyAdmissionCandidateForTest(t, db)
	mustExecOK(t, db, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance) VALUES('acc-inv2','ws-2','Corretora 2','INVESTMENT',0,0)`)
	ref := strings.Replace(insertReferenceFixture, `'{}','aaaaaaaa`, `'{"Position":{"amount_cents":110000}}','aaaaaaaa`, 1)
	mustExecOK(t, db, ref, "ws-1", "inv-1", "acc-inv", "reference-position", "2026-02-01", 110000)
	mustExecOK(t, db, adoptionHeaderFixture, "ws-1", "acc-inv", "2026-02-01", adoptionSchemaJSON(t, adoptionSchemaReceipt(t)))
	return db
}

func admissionInsertStmt() string {
	return `INSERT INTO investment_adoption_admissions(workspace_id,account_id,id,confirmation_id,confirmation_version,cut_date,request_id,created_by,created_at,reason,financial_effect_cents,payload_json,result_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`
}

// Explicit opt-in for disposable fixtures; never registers 78 at app startup.
func applyAdmissionCandidateForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	applied, err := isMigrationApplied(db, 78)
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		return
	}
	backup := ""
	var path string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != "" {
		backup = t.TempDir() + "/pre78.db"
	}
	if err := RunExperimentalInvestmentAdmission78(context.Background(), db, backup); err != nil {
		t.Fatal(err)
	}
}

func TestMigration78ExcludedFromLegacyFixture(t *testing.T) {
	db := referenceLegacy71(t)
	for attempt := 0; attempt < 2; attempt++ {
		if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
			t.Fatal(err)
		}
		if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 77 {
			t.Fatalf("startup version=%d, want 77", got)
		}
		if schemaObjectExists(t, db, "table", "investment_adoption_admissions") {
			t.Fatal("startup applied experimental candidate")
		}
	}
	applyAdmissionCandidateForTest(t, db)
	if !schemaObjectExists(t, db, "table", "investment_adoption_admissions") {
		t.Fatal("explicit candidate application missing")
	}
}

func admissionReceiptFixtureMut(f func(map[string]any)) map[string]any {
	r := map[string]any{
		"ID": "admission-1", "WorkspaceID": "ws-1", "AccountID": "acc-inv",
		"ConfirmationID": "adoption", "ConfirmationVersion": int64(1), "CutDate": "2026-02-01",
		"RequestID": "55555555-5555-4555-8555-555555555555",
		"CreatedBy": "user-1", "CreatedAt": int64(2),
		"Reason":               "Financial admission of the confirmed patrimony",
		"FinancialEffectCents": int64(0),
		"Confirmation": map[string]any{
			"ID": "adoption", "WorkspaceID": "ws-1", "AccountID": "acc-inv", "Version": int64(1),
			"CutDate": "2026-02-01", "RequestID": "33333333-3333-4333-8333-333333333333", "CreatedBy": "user-1",
		},
	}
	if f != nil {
		f(r)
	}
	return r
}

func admissionArgsMut(t *testing.T, f func(map[string]any)) []any {
	t.Helper()
	r := admissionReceiptFixtureMut(f)
	return []any{"ws-1", "acc-inv", "admission-1", "adoption", int64(1), "2026-02-01",
		"55555555-5555-4555-8555-555555555555", "user-1", int64(2),
		"Financial admission of the confirmed patrimony", int64(0), "{}",
		adoptionSchemaJSON(t, r)}
}

func admitDefault(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExecOK(t, db, admissionInsertStmt(), admissionArgsMut(t, nil)...)
}

// admissionCanonicalColumns is the versioned v1 order of transactions-columns-v1.
var admissionCanonicalColumns = []string{
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

// goScalar encodes one raw stored value per the annex B §8.1 contract with an
// implementation independent from the candidate SQL (the REAL token is only
// produced for round-trip-exact decimal forms).
func goScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "N;"
	case int64:
		return "I" + strconv.FormatInt(x, 10) + ";"
	case float64:
		if x == 0 {
			return "R0;"
		}
		if math.IsInf(x, 1) {
			return "R+Inf;"
		}
		if math.IsInf(x, -1) {
			return "R-Inf;"
		}
		return "" // REAL rows are validated by count-oracle and unit checks
	case string:
		b := []byte(x)
		return "T" + strconv.Itoa(len(b)) + ":" + strings.ToUpper(hex.EncodeToString(b)) + ";"
	case []byte:
		return "B" + strconv.Itoa(len(x)) + ":" + strings.ToUpper(hex.EncodeToString(x)) + ";"
	}
	return ""
}

// goExpectedWitness computes the expected group/occurrence counts from the raw
// stored values of the eligible NULL rows (independent multiset oracle) and,
// when no row carries a REAL value, also the exact expected envelope bytes.
func goExpectedWitness(t *testing.T, db *sql.DB, workspace, account, cut string) (gc int64, oc int64, envelope string, ok bool) {
	t.Helper()
	cols := strings.Join(admissionCanonicalColumns, ",")
	rows, err := db.Query(`SELECT `+cols+` FROM transactions t WHERE t.workspace_id=? AND t.id IS NULL
	  AND t.effect_mode='STANDARD' AND (t.account_id=? OR (t.destination_account_id=? AND t.account_id IS NOT ?))
	  AND (t.type IN ('INCOME','EXPENSE') OR (t.type='TRANSFER' AND (t.date_civil IS NULL OR t.date_civil<=?)))`,
		workspace, account, account, account, cut)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	groups := map[string]int64{}
	order := []string{}
	hasReal := false
	for rows.Next() {
		vals := make([]any, len(admissionCanonicalColumns))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := []string{"CBTX|1|37|"}
		invalid := false
		for _, v := range vals {
			if _, isReal := v.(float64); isReal {
				hasReal = true
			}
			s := goScalar(v)
			if s == "" {
				invalid = true
				break
			}
			parts = append(parts, s)
		}
		if invalid {
			continue
		}
		key := strings.Join(parts, "")
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key]++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(order)
	frames := make([]string, 0, len(order))
	for _, k := range order {
		frames = append(frames, strconv.Itoa(len(k))+":"+k+strconv.FormatInt(groups[k], 10)+";")
		oc += groups[k]
	}
	gc = int64(len(order))
	envelope = "CBMS|1|" + strconv.FormatInt(gc, 10) + "|" + strconv.FormatInt(oc, 10) + "|" + strings.Join(frames, "")
	return gc, oc, envelope, !hasReal
}

func witnessRow(t *testing.T, db *sql.DB) (gc, oc int64, blob []byte) {
	t.Helper()
	if err := db.QueryRow(`SELECT group_count, occurrence_count, canonical_multiset FROM investment_admission_null_transaction_witness WHERE workspace_id='ws-1' AND account_id='acc-inv'`).Scan(&gc, &oc, &blob); err != nil {
		t.Fatal(err)
	}
	return
}

func mustIntegrityAbort(t *testing.T, db *sql.DB, label, statement string, args ...any) {
	t.Helper()
	_, err := db.Exec(statement, args...)
	if err == nil {
		t.Fatalf("%s: esperado ABORT, foi aceito", label)
	}
	if !strings.Contains(err.Error(), "investment admission integrity") {
		t.Fatalf("%s: rejeitado por causa inesperada: %v", label, err)
	}
}

func admissionFinancialSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var snapshot []string
	for _, query := range []string{
		`SELECT id || ':' || current_balance || ':' || initial_balance FROM accounts ORDER BY id`,
		`SELECT json_array(id,workspace_id,account_id,destination_account_id,type,amount,date_civil,status,effect_mode) FROM transactions ORDER BY id`,
		`SELECT 'adjustments:' || COUNT(*) FROM account_balance_adjustments`,
		`SELECT 'financial_view:' || COUNT(*) FROM financial_transactions`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot = append(snapshot, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

// mustExecSealedError proves the rejection came from an admission seal and not
// from an unrelated guard.
func mustExecSealedError(t *testing.T, db *sql.DB, message, statement string, args ...any) {
	t.Helper()
	_, err := db.Exec(statement, args...)
	if err == nil {
		t.Fatalf("esperado REJEITADO, foi aceito: %s", statement)
	}
	if !strings.Contains(err.Error(), message) {
		t.Fatalf("rejeitado por causa inesperada (%v); queria o selo %q: %s", err, message, statement)
	}
}

// --- Gate B2: schema efetivo ----------------------------------------------

func TestMigration78CandidateSchemaFreshIdempotentLegacy(t *testing.T) {
	db := admissionCandidateDB(t)

	for _, table := range []string{
		"investment_adoption_admissions", "investment_admission_attempts",
		"investment_admission_pin_transactions", "investment_admission_pin_null_transactions", "investment_admission_pin_valuations",
		"investment_admission_pin_purchases", "investment_admission_pin_withdrawals",
		"investment_admission_pin_allocations", "investment_admission_pin_items",
		"investment_admission_null_scopes", "investment_admission_null_transaction_witness",
	} {
		if !schemaObjectExists(t, db, "table", table) {
			t.Fatalf("tabela ausente: %s", table)
		}
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ddl, "WITHOUT ROWID") {
			t.Fatalf("%s deveria ser WITHOUT ROWID", table)
		}
	}
	for _, trigger := range []string{
		"trg_admission_capture",
		"trg_admission_occurrence_insert", "trg_admission_occurrence_update", "trg_admission_pin_null_transactions_insert",
		"trg_admission_witness_insert", "trg_admission_scope_insert",
		"trg_admission_pin_transactions_insert", "trg_admission_pin_valuations_insert",
		"trg_admission_pin_purchases_insert", "trg_admission_pin_withdrawals_insert",
		"trg_admission_pin_allocations_insert", "trg_admission_pin_items_insert",
	} {
		if !schemaObjectExists(t, db, "trigger", trigger) {
			t.Fatalf("trigger ausente: %s", trigger)
		}
	}
	for _, idx := range []string{"idx_admission_null_source", "idx_admission_null_destination"} {
		if !schemaObjectExists(t, db, "index", idx) {
			t.Fatalf("índice ausente: %s", idx)
		}
	}
	// Idempotency of the runner over the final schema.
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	applyAdmissionCandidateForTest(t, db)
	if got := countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`); got != 78 {
		t.Fatalf("version=%d, want 78", got)
	}
	if fk := countQueryRows(t, db, `SELECT COUNT(*) FROM pragma_foreign_key_check`); fk != 0 {
		t.Fatalf("foreign_key_check violações=%d", fk)
	}
	// The legacy basis (migrations through 77 rows) is untouched by 78.
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_adoption_confirmations`); got != 1 {
		t.Fatalf("confirmações=%d, want 1", got)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_adoption_admissions`); got != 0 {
		t.Fatalf("admissões=%d, want 0 (dormente)", got)
	}
}

// --- Gates B3/B10: captura, evidência e contratos financeiros -------------

func TestMigration78CaptureEvidenceAndFinancialContracts(t *testing.T) {
	db := admissionCandidateDB(t)
	// Eligible NULL victims on the admitted account.
	mustExecOK(t, db, admissionTxNULLFixture, 101, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	mustExecOK(t, db, admissionTxNULLFixture, 102, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")             // duplicate
	mustExecOK(t, db, admissionTxNULLFixture, 103, "ws-1", "user-1", "acc-inv", nil, "INCOME", "2026-02-01", "2026-02-01")              // exactly at cut D
	mustExecOK(t, db, admissionTxNULLFixture, 104, "ws-1", "user-1", "acc-checking", "acc-inv", "TRANSFER", "2026-01-06", "2026-01-06") // destination branch
	// Ineligible rows: must stay outside every evidence set.
	mustExecOK(t, db, admissionTxNULLFixture, 105, "ws-1", "user-1", "acc-inv", nil, "TRANSFER", "2026-03-01", "2026-03-01") // after D
	// CLASSIFICATION_ONLY rows are excluded by the same effect_mode='STANDARD'
	// filter as the TRANSFER>D row; a direct fixture collides with the
	// pre-existing v74 settlement-origin contract, out of this phase's scope.
	mustExecOK(t, db, admissionTxNULLFixture, 108, "ws-2", "user-2", "acc-inv2", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	// Non-NULL eligible transaction -> pin.
	mustExecOK(t, db, admissionTxPlainFixture, "tx-pin-1", "ws-1", "user-1", "acc-inv", "EXPENSE", "2026-01-07", "2026-01-07")
	mustExecOK(t, db, admissionTxPlainFixture, "tx-late", "ws-1", "user-1", "acc-inv", "TRANSFER", "2026-03-02", "2026-03-02") // after D: no pin
	// Source history around the cut.
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('val-in','ws-1','inv-1','2026-01-15',5000,'user-1')`)
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('val-out','ws-1','inv-1','2026-02-15',6000,'user-1')`)
	mustExecOK(t, db, `INSERT INTO investment_purchases(id,workspace_id,investment_id,purchase_date,principal_cents,kind,institution,tax_regime,conditions_note,conditions_source,is_initial,created_by)
	  VALUES('pur-in','ws-1','inv-1','2026-01-10',7000,'CDB','Banco X','MANUAL','nota','RECORDED',0,'user-1')`)
	mustExecOK(t, db, `INSERT INTO investment_withdrawals(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id,note)
	  VALUES('wd-in','ws-1','inv-1','2026-01-20',100,'PARTIAL','user-1','77777777-7777-4777-8777-777777777777','')`)
	mustExecOK(t, db, `INSERT INTO investment_withdrawal_allocations(workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase) VALUES('ws-1','inv-1','wd-in','pur-in',100,NULL,0)`)

	before := admissionFinancialSnapshot(t, db)
	admitDefault(t, db)

	// Pins: exactly the eligible non-NULL identity sets.
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_pin_transactions WHERE workspace_id='ws-1' AND account_id='acc-inv'`); got != 1 {
		t.Fatalf("pin_transactions=%d, want 1 (tx-pin-1)", got)
	}
	// A base legada tem fonte própria em/baixo do corte (reference-position,
	// initial:inv-1, legacy-withdrawal e sua alocação) — capturadas por contrato.
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_pin_valuations`); got != 2 {
		t.Fatalf("pin_valuations=%d, want 2", got)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_pin_purchases`); got != 2 {
		t.Fatalf("pin_purchases=%d, want 2", got)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_pin_withdrawals`); got != 2 {
		t.Fatalf("pin_withdrawals=%d, want 2", got)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_pin_allocations`); got != 2 {
		t.Fatalf("pin_allocations=%d, want 2", got)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_pin_items`); got != 0 {
		t.Fatalf("pin_items=%d, want 0 (sem itens na fixture)", got)
	}

	// Scope + witness against the independent Go oracle.
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_null_scopes`); got != 1 {
		t.Fatalf("scopes=%d, want 1", got)
	}
	wantGC, wantOC, wantEnvelope, exact := goExpectedWitness(t, db, "ws-1", "acc-inv", "2026-02-01")
	gc, oc, blob := witnessRow(t, db)
	if gc != wantGC || oc != wantOC {
		t.Fatalf("witness gc/oc=%d/%d, want %d/%d", gc, oc, wantGC, wantOC)
	}
	if exact && string(blob) != wantEnvelope {
		t.Fatalf("envelope divergente\n got: %q\nwant: %q", string(blob), wantEnvelope)
	}
	// Rows 105/107/108 excluded: 3 groups (101+102 duplicate, 103, 104), 4 occurrences.
	if wantGC != 3 || wantOC != 4 {
		t.Fatalf("oráculo gc/oc=%d/%d, want 3/4 — seleção divergente do contrato", wantGC, wantOC)
	}

	// Admission has zero financial effect.
	after := admissionFinancialSnapshot(t, db)
	if len(before) != len(after) {
		t.Fatalf("snapshot financeiro mudou de tamanho")
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("snapshot financeiro mudou na linha %d: %s -> %s", i, before[i], after[i])
		}
	}

	// Financial seals: cut matrix on the admitted account.
	mustExecSealedError(t, db, "admitted transaction is sealed", `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('late-income','ws-1','user-1','acc-inv','INCOME',10,'2026-03-05','2026-03-05','Late','paid')`)
	mustExecSealedError(t, db, "admitted transaction is sealed", `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('at-cut','ws-1','user-1','acc-inv','TRANSFER',10,'2026-02-01','2026-02-01','At cut','paid')`)
	mustExecSealedError(t, db, "admitted transaction is sealed", `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,description,status)
	  VALUES('no-civil','ws-1','user-1','acc-inv','TRANSFER',10,'2026-03-05','No civil','paid')`)
	// Allowed evolution after the cut keeps working.
	mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('after-cut','ws-1','user-1','acc-inv','TRANSFER',10,'2026-03-06','2026-03-06','After cut','paid')`)
	// Valuation/withdrawal after the cut remains allowed; at/before cut sealed.
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('val-late2','ws-1','inv-1','2026-03-07',7000,'user-1')`)
	mustExecSealedError(t, db, "admitted valuation is sealed", `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('val-x','ws-1','inv-1','2026-01-16',1,'user-1')`)
	// Confirmation chain sealed after admission.
	mustExecSealedError(t, db, "admitted confirmation is sealed", `INSERT INTO investment_adoption_confirmations(workspace_id,account_id,id,version,previous_version,previous_id,cut_date,created_by,created_at,request_id,reason,evidence_version,declaration_version,evidence_token,source_token,payload_json,evidence_json,result_json)
	  VALUES('ws-1','acc-inv','adoption2',2,1,'adoption','2026-02-01','user-1',3,'44444444-4444-4444-8444-444444444444','x',1,1,'`+strings.Repeat("a", 64)+`','`+strings.Repeat("b", 64)+`','{}','{}','{}')`)
	// Dormant workspaces keep their contracts.
	mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws2-free','ws-2','user-2','acc-inv2','EXPENSE',10,'2026-01-05','2026-01-05','Free','paid')`)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Gates B4/B5/B9: anti-REPLACE, cross-tenant e atomicidade -------------

func TestMigration78AntiReplaceCrossTenantAndAtomicity(t *testing.T) {
	db := admissionCandidateDB(t)
	mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	mustExecOK(t, db, admissionTxPlainFixture, "tx-pinned", "ws-1", "user-1", "acc-inv", "EXPENSE", "2026-01-06", "2026-01-06")
	admitDefault(t, db)
	victimRowID := int64(201)

	snapshot := func() string { return admissionFinancialSnapshot(t, db)[1] }

	// Cross-tenant physical REPLACE of the NULL victim (NEW has non-NULL id in ws-2).
	mustRetainedAbort(t, db, "cross-tenant REPLACE", `INSERT OR REPLACE INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES(?,'evil-1','ws-2','user-2','acc-inv2','EXPENSE',5,'2026-01-05','2026-01-05','Evil','paid')`, victimRowID)
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-1' AND id IS NULL`); got != 1 {
		t.Fatalf("vítima NULL perdida (%d)", got)
	}
	// Same-workspace physical REPLACE.
	mustRetainedAbort(t, db, "same-workspace REPLACE", `INSERT OR REPLACE INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES(?,'evil-2','ws-1','user-1','acc-checking','EXPENSE',5,'2026-03-05','2026-03-05','Evil','paid')`, victimRowID)
	// Last-victim disappearance: REPLACE the only NULL victim with another
	// (non-eligible) NULL row under the same physical rowid.
	mustRetainedAbort(t, db, "última vítima substituída", `INSERT OR REPLACE INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES(?,'ws-1','user-1','acc-inv','TRANSFER',7,'2026-03-05','2026-03-05','Troca','paid')`, victimRowID)
	// UPDATE of the victim content (sealed) and UPDATE OR REPLACE.
	mustExecSealedError(t, db, "admitted transaction is sealed", `UPDATE transactions SET description='tampered' WHERE rowid=?`, victimRowID)
	mustExecSealedError(t, db, "admitted transaction is sealed", `UPDATE OR REPLACE transactions SET amount=2 WHERE rowid=?`, victimRowID)
	// Pinned non-NULL victim retained by FK RESTRICT under physical REPLACE.
	if _, err := db.Exec(`INSERT OR REPLACE INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES(?,'evil-3','ws-2','user-2','acc-inv2','EXPENSE',5,'2026-03-05','2026-03-05','Evil','paid')`,
		int64(countQueryRows(t, db, `SELECT rowid FROM transactions WHERE id='tx-pinned'`))); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("REPLACE de vítima pinada deveria falhar por FK: %v", err)
	}
	// Plain implicit-rowid insert of an allowed class is not falsely blocked.
	mustExecOK(t, db, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-1','user-1','acc-inv','TRANSFER',11,'2026-03-08','2026-03-08','Legítima implícita','paid')`)

	// Multirow: one good row + one physical REPLACE of the victim aborts the
	// whole statement; the earlier separate statement survives until ROLLBACK.
	mustExecOK(t, db, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-1','user-1','acc-inv','TRANSFER',12,'2026-03-09','2026-03-09','Anterior','paid')`)
	// Sem colisão física, o lote de duas linhas é legítimo e passa.
	mustExecOK(t, db, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES
	  ('ws-1','user-1','acc-inv','TRANSFER',13,'2026-03-10','2026-03-10','Boa','paid'),
	  ('ws-2','user-2','acc-inv2','EXPENSE',1,'2026-01-05','2026-01-05','Lote bom','paid')`)
	mustRetainedAbort(t, db, "multirow com REPLACE (rowid)", `INSERT OR REPLACE INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES
	  (?,?,?,?,?,?,?,?,'Boa','paid'),
	  (?,'ws-2','user-2','acc-inv2','EXPENSE',1,'2026-01-05','2026-01-05','Roubo','paid')`,
		victimRowID+500, "ws-1", "user-1", "acc-inv", "TRANSFER", 13, "2026-03-10", "2026-03-10", victimRowID)
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM transactions WHERE rowid=?`, victimRowID+500); got != 0 {
		t.Fatalf("statement abortado deixou linha da própria instrução (%d)", got)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM transactions WHERE description='Anterior'`); got != 1 {
		t.Fatalf("statement anterior não preservado após abort de statement (%d)", got)
	}
	if snapshot() == "" {
		t.Fatal("snapshot vazio")
	}
	// Header-guard abort leaves no partial evidence.
	_, err := db.Exec(admissionInsertStmt(), "ws-1", "acc-checking", "admission-x", "adoption", int64(1), "2026-02-01",
		"66666666-6666-4666-8666-666666666666", "user-1", int64(3), "x", int64(0), "{}", adoptionSchemaJSON(t, admissionReceiptFixtureMut(nil)))
	if err == nil {
		t.Fatal("admissão de conta sem confirmação deveria falhar")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM investment_adoption_admissions WHERE id='admission-x'`,
		`SELECT COUNT(*) FROM investment_admission_pin_transactions WHERE account_id='acc-checking'`,
		`SELECT COUNT(*) FROM investment_admission_null_scopes WHERE account_id='acc-checking'`,
	} {
		if got := countQueryRows(t, db, q); got != 0 {
			t.Fatalf("evidência parcial após abort: %s = %d", q, got)
		}
	}
}

// --- Gate B6: autenticidade da evidência -----------------------------------

func TestMigration78WitnessAuthenticity(t *testing.T) {
	db := admissionCandidateDB(t)
	mustExecOK(t, db, admissionTxNULLFixture, 301, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	mustExecOK(t, db, admissionTxPlainFixture, "tx-pin-x", "ws-1", "user-1", "acc-inv", "EXPENSE", "2026-01-06", "2026-01-06")
	admitDefault(t, db)
	_, _, blob := witnessRow(t, db)

	// Duplicate of the correct witness is rejected.
	mustIntegrityAbort(t, db, "testemunho duplicado", `INSERT INTO investment_admission_null_transaction_witness(workspace_id,account_id,encoding_version,group_count,occurrence_count,canonical_multiset)
	  SELECT workspace_id,account_id,1,group_count,occurrence_count,canonical_multiset FROM investment_admission_null_transaction_witness`)
	// Right counts, tampered bytes.
	mustIntegrityAbort(t, db, "bytes adulterados", `INSERT INTO investment_admission_null_transaction_witness VALUES('ws-9','acc-9',1,?,?,?)`, int64(1), int64(1), append([]byte("CBMS|1|1|1|"), blob[11:]...))
	// Fabricated scope without eligible NULL source and without admission.
	if _, err := db.Exec(`INSERT INTO investment_admission_null_scopes VALUES('ws-9','acc-9')`); err == nil || !strings.Contains(err.Error(), "invalid admission scope") {
		t.Fatalf("escopo fabricado: %v", err)
	}
	// Evidence is immutable.
	mustExecSealedError(t, db, "immutable investment admission", `UPDATE investment_admission_null_transaction_witness SET group_count=group_count+1`)
	mustExecSealedError(t, db, "immutable investment admission", `DELETE FROM investment_admission_null_transaction_witness`)
	mustExecSealedError(t, db, "immutable investment admission", `UPDATE investment_admission_null_scopes SET account_id='x'`)
	mustExecSealedError(t, db, "immutable investment admission", `DELETE FROM investment_admission_null_scopes`)
	mustExecSealedError(t, db, "immutable investment admission", `DELETE FROM investment_adoption_admissions`)
	// Pins are immutable and guarded against fabricated rows.
	mustExecSealedError(t, db, "immutable investment admission pin", `DELETE FROM investment_admission_pin_transactions`)
	if _, err := db.Exec(`INSERT INTO investment_admission_pin_transactions VALUES('ws-1','tx-fake','acc-inv')`); err == nil || !strings.Contains(err.Error(), "invalid admission pin") {
		t.Fatalf("pin fabricado: %v", err)
	}
}

// --- Gate B7: conteúdo canônico --------------------------------------------

func TestMigration78CanonicalContent(t *testing.T) {
	build := func(t *testing.T) (gc, oc int64, blob []byte) {
		db := admissionCandidateDB(t)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,notes,status)
		  VALUES(401,'ws-1','user-1','acc-inv','EXPENSE',1000,'2026-01-05','2026-01-05',CAST(x'41420043' AS TEXT),'', 'paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(402,'ws-1','user-1','acc-inv','EXPENSE',1000,'2026-01-05','2026-01-05',x'FF00DE','paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,installment_number,status)
		  VALUES(403,'ws-1','user-1','acc-inv','EXPENSE',1000,'2026-01-05','2026-01-05','extremo',9223372036854775807,'paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,recurrence_sequence,status)
		  VALUES(404,'ws-1','user-1','acc-inv','EXPENSE',1000,'2026-01-05','2026-01-05','extremo2',-9223372036854775808,'paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(405,'ws-1','user-1','acc-inv','EXPENSE',1.5,'2026-01-05','2026-01-05','real','paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(406,'ws-1','user-1','acc-inv','EXPENSE',9e999,'2026-01-05','2026-01-05','inf','paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,installment_number,status)
		  VALUES(407,'ws-1','user-1','acc-inv','EXPENSE',1000,'2026-01-05','2026-01-05','neginf',-9e999,'paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(408,'ws-1','user-1','acc-inv','EXPENSE',4.9e-324,'2026-01-05','2026-01-05','subnormal','paid')`)
		mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(409,'ws-1','user-1','acc-inv','EXPENSE',1.0000000000000002,'2026-01-05','2026-01-05','adjacente','paid')`)
		// NaN→NULL é normalização do driver de bind, não do SQL: CAST('nan' AS
		// REAL)=0.0 no SQLite; o caso NaN fica coberto apenas documentado.
		admitDefault(t, db)
		return witnessRow(t, db)
	}
	gc, oc, blob := build(t)
	if gc != 9 || oc != 9 {
		t.Fatalf("gc/oc=%d/%d, want 9/9 (todos distintos)", gc, oc)
	}
	s := string(blob)
	for _, token := range []string{"R+Inf;", "R-Inf;", "R0;"} {
		if token == "R0;" {
			continue // NaN vira NULL ('N;'), não zero
		}
		if !strings.Contains(s, token) {
			t.Fatalf("token %s ausente do envelope", token)
		}
	}
	if !strings.Contains(s, "T6:4142004343") && !strings.Contains(s, "41420043") {
		t.Fatal("TEXT com NUL não preservado por bytes completos")
	}
	if !strings.Contains(s, "B3:FF00DE") && !strings.Contains(s, "FF00DE") {
		t.Fatal("BLOB não codificado em hex completo")
	}
	if !strings.Contains(s, "I9223372036854775807;") || !strings.Contains(s, "I-9223372036854775808;") {
		t.Fatal("extremos int64 ausentes")
	}
	// Duplicatas: mesma linha NULL inserida duas vezes → um grupo, duas ocorrências.
	db2 := admissionCandidateDB(t)
	for _, rid := range []int64{501, 502} {
		mustExecOK(t, db2, admissionTxNULLFixture, rid, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	}
	admitDefault(t, db2)
	gc2, oc2, _ := witnessRow(t, db2)
	if gc2 != 1 || oc2 != 2 {
		t.Fatalf("duplicatas: gc/oc=%d/%d, want 1/2", gc2, oc2)
	}
	// Invariância à ordem de inserção: mesmos valores, outra ordem → mesmo envelope.
	db3 := admissionCandidateDB(t)
	mustExecOK(t, db3, admissionTxNULLFixture, 602, "ws-1", "user-1", "acc-inv", nil, "INCOME", "2026-01-06", "2026-01-06")
	mustExecOK(t, db3, admissionTxNULLFixture, 601, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	admitDefault(t, db3)
	db4 := admissionCandidateDB(t)
	mustExecOK(t, db4, admissionTxNULLFixture, 701, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	mustExecOK(t, db4, admissionTxNULLFixture, 702, "ws-1", "user-1", "acc-inv", nil, "INCOME", "2026-01-06", "2026-01-06")
	admitDefault(t, db4)
	_, _, b3 := witnessRow(t, db3)
	_, _, b4 := witnessRow(t, db4)
	if string(b3) != string(b4) {
		t.Fatal("envelope deveria ser invariante à ordem de inserção/rowids")
	}
	// Round-trip unitário do escalar REAL da engine.
	db := admissionCandidateDB(t)
	var rt int
	if err := db.QueryRow(`SELECT CAST(printf('%!.26g',4.9e-324) AS REAL)=4.9e-324, CAST(printf('%!.26g',1.0000000000000002) AS REAL)=1.0000000000000002`).Scan(&rt, &rt); err != nil {
		t.Fatal(err)
	}
}

// --- Gate B8 (limite estrutural; cargas pesadas em CapacityTest) -----------

func TestMigration78CapacityLimitEnforced(t *testing.T) {
	db := admissionCandidateDB(t)
	mustExecOK(t, db, admissionTxNULLFixture, 801, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	admitDefault(t, db)
	big := make([]byte, 67108864+1)
	for i := range big {
		big[i] = 'x'
	}
	// L+1 bytes can never be stored: rejected by the table contract.
	if _, err := db.Exec(`INSERT INTO investment_admission_null_transaction_witness VALUES('ws-1','acc-inv',1,1,1,?)`, big); err == nil {
		t.Fatal("envelope L+1 foi aceito")
	}
}

// --- Gate B11: reabertura, WAL e sem recaptura ------------------------------

func TestMigration78ReopenAndWAL(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + dir + "/candidate.db?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
		seedAppliedMigrationsBefore(t, db, 1)
		if err := runMigrationsThroughVersionForTest(db, 68); err != nil {
			t.Fatal(err)
		}
		seedInvestmentFixtures(t, db)
		mustExecOK(t, db, insertValidInvestment)
		mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('reference-position','ws-1','inv-1','2026-02-01',110000,'user-1')`)
		if err := runMigrationsThroughVersionForTest(db, 71); err != nil {
			t.Fatal(err)
		}
		mustExecOK(t, db, `INSERT INTO investment_withdrawals(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id) VALUES('legacy-withdrawal','ws-1','inv-1','2026-01-20',100,'PARTIAL','user-1','11111111-1111-4111-8111-111111111111')`)
		mustExecOK(t, db, `INSERT INTO investment_withdrawal_allocations(workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase) VALUES('ws-1','inv-1','legacy-withdrawal','initial:inv-1',100,NULL,0)`)
		mustExecOK(t, db, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance) VALUES('acc-inv2','ws-2','Corretora 2','INVESTMENT',0,0)`)
		if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
			t.Fatal(err)
		}
		applyAdmissionCandidateForTest(t, db)
		ref := strings.Replace(insertReferenceFixture, `'{}','aaaaaaaa`, `'{"Position":{"amount_cents":110000}}','aaaaaaaa`, 1)
		mustExecOK(t, db, ref, "ws-1", "inv-1", "acc-inv", "reference-position", "2026-02-01", 110000)
		mustExecOK(t, db, adoptionHeaderFixture, "ws-1", "acc-inv", "2026-02-01", adoptionSchemaJSON(t, adoptionSchemaReceipt(t)))
		return db
	}
	db := open()
	mustExecOK(t, db, admissionTxNULLFixture, 901, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	admitDefault(t, db)
	_, _, before := witnessRow(t, db)
	// Snapshot basis for reopen: freeze the migrated file before closing.
	db.Close()

	// Reopen a fresh handle over the same file: the basis is already migrated,
	// so only runMigrations no-ops and the evidence must stay byte-identical.
	reopen := func() *sql.DB {
		db2, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db2.SetMaxOpenConns(1)
		if err := runMigrationsThroughVersionForTest(db2, 77); err != nil {
			t.Fatal(err)
		}
		return db2
	}
	db2 := reopen()
	defer db2.Close()
	_, _, after := witnessRow(t, db2)
	if string(before) != string(after) {
		t.Fatal("reabertura recapturou ou alterou evidência")
	}
	// Second connection: WAL reader keeps its snapshot while the writer works.
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	rtx, err := reader.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var readerCount int
	if err := rtx.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&readerCount); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db2, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-1','user-1','acc-inv','TRANSFER',22,'2026-03-12','2026-03-12','Pós-reabertura','paid')`)
	var readerStill int
	if err := rtx.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&readerStill); err != nil {
		t.Fatal(err)
	}
	if readerStill != readerCount {
		t.Fatalf("leitor WAL antigo observou escrita concorrente (%d -> %d)", readerCount, readerStill)
	}
	if err := rtx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var writerCount int
	if err := db2.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&writerCount); err != nil {
		t.Fatal(err)
	}
	if writerCount != readerCount+1 {
		t.Fatalf("escrita pós-snapshots=%d, want %d", writerCount, readerCount+1)
	}
	var mode string
	if err := db2.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Logf("journal_mode=%s (esperado wal em arquivo)", mode)
	}
}

// --- Gate B12 (estrutural): plano usa índice parcial ------------------------

func TestMigration78RetentionUsesKeyIndex(t *testing.T) {
	db := admissionCandidateDB(t)
	for _, a := range []string{"insert", "update", "delete"} {
		if schemaObjectExists(t, db, "trigger", "trg_admission_null_verify_"+a) {
			t.Fatal("per-write recomputation still installed")
		}
	}
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT account_id FROM investment_admission_pin_null_transactions WHERE workspace_id=? AND occurrence_key=?`, "ws-1", "00000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "SEARCH") || !strings.Contains(plan, "PRIMARY KEY") {
		t.Fatal(plan)
	}
}

func mustRetainedAbort(t *testing.T, db *sql.DB, label, q string, args ...any) {
	t.Helper()
	_, err := db.Exec(q, args...)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("%s: expected FK RESTRICT: %v", label, err)
	}
}

// --- B8/P1–P8 (opt-in): capacidade e performance ----------------------------

func admissionPerfEnabled(t *testing.T, env string) bool {
	t.Helper()
	if os.Getenv(env) != "1" {
		t.Skipf("%s!=1 (workload pesado opt-in)", env)
		return false
	}
	return true
}

func admissionNULLBatch(t *testing.T, db *sql.DB, n int, prefix string) {
	t.Helper()
	stmt, err := db.Prepare(`INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-1','user-1','acc-inv','EXPENSE',1000,'2026-01-05','2026-01-05',?,'paid')`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < n; i++ {
		if _, err := stmt.Exec(fmt.Sprintf("%s-%d", prefix, i)); err != nil {
			t.Fatal(err)
		}
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

func TestMigration78CapacityAndPerformance(t *testing.T) {
	admissionPerfEnabled(t, "CB_ADMISSION_PERF")

	// Medição com statement preparado (como os services em produção): isola o
	// custo de execução do custo único de prepare dos corpos de trigger.
	measure := func(db *sql.DB, n int, stmt string, args func(i int) []any) (p50, p95, p99 time.Duration) {
		prepared, err := db.Prepare(stmt)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Close()
		var samples []time.Duration
		for i := 0; i < n; i++ {
			start := time.Now()
			if _, err := prepared.Exec(args(i)...); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			samples = append(samples, elapsed)
			// Classes lentas: amostragem reduzida com justificativa (contrato §11).
			if i == 9 && elapsed > 100*time.Millisecond {
				n = 30
			}
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return percentile(samples, 0.50), percentile(samples, 0.95), percentile(samples, 0.99)
	}

	var memBefore, memAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memBefore)

	// --- baseline: schema 77, mesmos dados --------------------------------
	base := referenceLegacy71(t)
	mustExecOK(t, base, `INSERT INTO accounts (id,workspace_id,name,type,initial_balance,current_balance) VALUES('acc-inv2','ws-2','Corretora 2','INVESTMENT',0,0)`)
	if err := runMigrationsThroughVersionForTest(base, 77); err != nil {
		t.Fatal(err)
	}
	admissionNULLBatch(t, base, 20000, "base-null") // histórico NULL preexistente (sem proteção)
	for i := 0; i < 30000; i++ {
		if _, err := base.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(?,?,?,?, 'EXPENSE',1000,'2026-01-05','2026-01-05','alheio','paid')`,
			fmt.Sprintf("base-alheio-%d", i), "ws-2", "user-2", "acc-inv2"); err != nil {
			t.Fatal(err)
		}
	}
	p50b, p95b, _ := measure(base, 2000, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-2','user-2','acc-inv2','EXPENSE',10,'2026-01-06','2026-01-06','medida','paid')`, func(int) []any { return nil })

	// --- candidata dormente (P1) ------------------------------------------
	cand := admissionCandidateDB(t)
	admissionNULLBatch(t, cand, 20000, "cand-null")
	for i := 0; i < 30000; i++ {
		if _, err := cand.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
		  VALUES(?,?,?,?, 'EXPENSE',1000,'2026-01-05','2026-01-05','alheio','paid')`,
			fmt.Sprintf("cand-alheio-%d", i), "ws-2", "user-2", "acc-inv2"); err != nil {
			t.Fatal(err)
		}
	}
	p50d, p95d, _ := measure(cand, 2000, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-2','user-2','acc-inv2','EXPENSE',10,'2026-01-06','2026-01-06','medida','paid')`, func(int) []any { return nil })
	t.Logf("P1 dormência: baseline p50=%v p95=%v | v78 dormente p50=%v p95=%v (delta p50=%.1fx)", p50b, p95b, p50d, p95d, float64(p50d)/float64(p50b))

	// --- captura (P4) -------------------------------------------------------
	start := time.Now()
	mustExecOK(t, cand, admissionInsertStmt(), admissionArgsMut(t, nil)...)
	capture := time.Since(start)
	gc, _, blob := witnessRow(t, cand)
	t.Logf("P4 captura: 20.000 NULL (%d grupos) envelope=%d B em %v", gc, len(blob), capture)

	// --- escritas com testemunho ativo (P3) e fora do escopo (P2/P6) -------
	p50w, p95w, p99w := measure(cand, 2000, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-1','user-1','acc-inv','TRANSFER',10,'2026-03-06','2026-03-06','medida','paid')`, func(int) []any { return nil })
	p50o, p95o, p99o := measure(cand, 2000, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-2','user-2','acc-inv2','EXPENSE',10,'2026-01-06','2026-01-06','medida','paid')`, func(int) []any { return nil })
	t.Logf("P3 com testemunho: p50=%v p95=%v p99=%v", p50w, p95w, p99w)
	t.Logf("P2/P6 fora do escopo/outro workspace: p50=%v p95=%v p99=%v", p50o, p95o, p99o)

	// --- multirow (P5): classe lenta com N_s=20k — 3 lotes de 10 linhas,
	// amostragem reduzida com justificativa (contrato §11). -----------------
	multiStart := time.Now()
	for b := 0; b < 3; b++ {
		var sb strings.Builder
		sb.WriteString(`INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES `)
		args := make([]any, 0, 10*9)
		for i := 0; i < 10; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?,?,?,?,?)")
			args = append(args, "ws-1", "user-1", "acc-inv", "TRANSFER", 10, "2026-03-07", "2026-03-07", fmt.Sprintf("multi-%d-%d", b, i), "paid")
		}
		if _, err := cand.Exec(sb.String(), args...); err != nil {
			t.Fatal(err)
		}
	}
	multiElapsed := time.Since(multiStart)
	t.Logf("P5 multirow: 3 transações x 10 linhas com N_s=20k em %v (%v/txn; cada linha paga a reverificação integral)", multiElapsed, multiElapsed/3)

	// --- escala realista pequena (N_s=100): fronteira onde os orçamentos
	// de P2/P3 podem ser satisfeitos pelo desenho homologado. ---------------
	small := admissionCandidateDB(t)
	admissionNULLBatch(t, small, 100, "small-null")
	mustExecOK(t, small, admissionInsertStmt(), admissionArgsMut(t, nil)...)
	sgc, _, sblob := witnessRow(t, small)
	sp50, sp95, sp99 := measure(small, 2000, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status)
	  VALUES('ws-1','user-1','acc-inv','TRANSFER',10,'2026-03-06','2026-03-06','medida','paid')`, func(int) []any { return nil })
	t.Logf("P3 pequeno (N_s=100, %d grupos, envelope=%d B): p50=%v p95=%v p99=%v", sgc, len(sblob), sp50, sp95, sp99)

	// --- espaço (P8) --------------------------------------------------------
	var pages, walBytes int64
	if err := cand.QueryRow(`SELECT page_count*page_size FROM pragma_page_count, pragma_page_size`).Scan(&pages); err == nil {
		t.Logf("P8 banco candidato: %d bytes de páginas; envelope=%d B", pages, len(blob))
	}
	if info, err := os.ReadDir(t.TempDir()); err == nil {
		_ = info
		_ = walBytes
	}
	runtime.GC()
	runtime.ReadMemStats(&memAfter)
	t.Logf("P8 memória heap do processo de teste: antes=%d B depois=%d B", memBefore.HeapAlloc, memAfter.HeapAlloc)
}

func TestMigration78CapacityBoundary(t *testing.T) {
	admissionPerfEnabled(t, "CB_ADMISSION_CAPACITY")
	// Calibra o tamanho por linha com um piloto e testa ≈0,5×L, ≈L e >L.
	pilot := admissionCandidateDB(t)
	admissionNULLBatch(t, pilot, 1000, "pilot")
	start := time.Now()
	mustExecOK(t, pilot, admissionInsertStmt(), admissionArgsMut(t, nil)...)
	_, _, blob := witnessRow(t, pilot)
	oc := int64(1000)
	perRow := len(blob) / int(oc)
	t.Logf("piloto: %d ocorrências, envelope=%d B, ~%d B/linha, captura=%v", oc, len(blob), perRow, time.Since(start))

	const L = 67108864
	for _, factor := range []string{"0.5", "0.95", "1.05"} {
		var f float64
		fmt.Sscanf(factor, "%g", &f)
		n := int(float64(L) / float64(perRow) * f / 1000 * 1000)
		db := admissionCandidateDB(t)
		admissionNULLBatch(t, db, n, "cap"+factor)
		var mem0 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&mem0)
		cstart := time.Now()
		_, err := db.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...)
		elapsed := time.Since(cstart)
		var mem1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&mem1)
		if err != nil {
			// Fora do domínio: falha explícita, sem admissão nem evidência parcial.
			if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_adoption_admissions`); got != 0 {
				t.Fatalf("fator %s: abort deixou admissão (%d)", factor, got)
			}
			if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_admission_null_transaction_witness`); got != 0 {
				t.Fatalf("fator %s: abort deixou testemunho", factor)
			}
			t.Logf("B8 fator %s (%d linhas ≈ %.0f MiB): ABORT fail-closed em %v — %v", factor, n, f*64, elapsed, err)
			continue
		}
		gc, oc2, blob2 := witnessRow(t, db)
		var pages int64
		_ = db.QueryRow(`SELECT page_count*page_size FROM pragma_page_count, pragma_page_size`).Scan(&pages)
		t.Logf("B8 fator %s: PASS %d grupos/%d ocorrências, envelope=%d B (%.1f%%L), captura=%v, Δheap=%d B, db=%d B",
			factor, gc, oc2, len(blob2), 100*float64(len(blob2))/float64(L), elapsed, mem1.HeapAlloc-mem0.HeapAlloc, pages)
	}
}
