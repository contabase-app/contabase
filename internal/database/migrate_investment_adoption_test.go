package database

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMigration77AdditiveUpgradeFreshAndIdempotency(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade-76", true: "fresh"}[fresh], func(t *testing.T) {
			db := referenceLegacy71(t)
			if err := runMigrationsThroughVersionForTest(db, 76); err != nil {
				t.Fatal(err)
			}
			before := referenceLegacySnapshot(t, db)
			if fresh {
				db = investmentsFreshDB(t)
			} else if err := runMigrations(db); err != nil {
				t.Fatal(err)
			}
			assertMigrationApplied(t, db, 77)
			for _, table := range []string{"investment_adoption_confirmations", "investment_adoption_items", "investment_adoption_attempts"} {
				if countQueryRows(t, db, `SELECT COUNT(*) FROM `+table) != 0 {
					t.Fatal("automatic historical adoption", table)
				}
			}
			if !fresh && !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
				t.Fatal("migration changed financial/source rows")
			}
			if err := runMigrations(db); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = migrateInvestmentAdoption(tx); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=69`) != 0 {
				t.Fatal("reserved migration used")
			}
			if err = assertDatabaseForeignKeyIntegrity(db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigration77FailureAtomic(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrationsThroughVersionForTest(db, 76); err != nil {
		t.Fatal(err)
	}
	before := referenceLegacySnapshot(t, db)
	mustExecOK(t, db, `CREATE TABLE investment_adoption_items(broken TEXT)`)
	if err := runMigrations(db); err == nil {
		t.Fatal("incompatible schema accepted")
	}
	if schemaObjectExists(t, db, "table", "investment_adoption_confirmations") || schemaObjectExists(t, db, "index", "idx_reference_adoption_owner") || countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version=77`) != 0 {
		t.Fatal("partial migration committed")
	}
	if !reflect.DeepEqual(before, referenceLegacySnapshot(t, db)) {
		t.Fatal("failed migration changed sources")
	}
}

const adoptionHeaderFixture = `INSERT INTO investment_adoption_confirmations(workspace_id,account_id,id,version,previous_version,cut_date,created_by,created_at,request_id,reason,evidence_version,declaration_version,evidence_token,source_token,payload_json,evidence_json,result_json)
 VALUES(?,?,'adoption',1,0,?,'user-1',1,'33333333-3333-4333-8333-333333333333','Explicit joint declaration',1,1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','{}','{}',?)`

func adoptionSchemaReceipt(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"ID": "adoption", "WorkspaceID": "ws-1", "AccountID": "acc-inv", "Version": int64(1),
		"CutDate": "2026-02-01", "CreatedBy": "user-1", "CreatedAt": int64(1),
		"RequestID": "33333333-3333-4333-8333-333333333333", "Reason": "Explicit joint declaration",
		"ObservedBalanceCents": int64(110000), "ObservedBalanceVersion": int64(0),
		"Coverage": "PARTIAL", "IntegrationAdmission": "PENDING", "FinancialEffectCents": int64(0),
		"Items":                     []any{map[string]any{"InvestmentID": "inv-1", "PositionID": "reference-position", "ReferenceRevisionID": "ref", "RepresentedCents": int64(110000), "EconomicIdentity": "lot-1", "IdentityBasis": "Individual contract and lot"}},
		"Cash":                      map[string]any{"Cents": nil, "Evidence": map[string]any{"State": "UNKNOWN", "Basis": "No broker cash declared or proven", "IDs": nil}},
		"DeclaredBalanceAtCutCents": nil,
		"Declaration":               map[string]any{"WorkspaceID": "ws-1", "AccountID": "acc-inv", "CutDate": "2026-02-01", "CreatedBy": "user-1", "RequestID": "33333333-3333-4333-8333-333333333333", "Reason": "Explicit joint declaration", "ExpectedAdoptionVersion": 0, "ExpectedBalanceVersion": 0, "EvidenceVersion": 1, "DeclarationVersion": 1, "EvidenceToken": strings.Repeat("a", 64), "Coverage": "PARTIAL"},
		"Diagnostic":                map[string]any{"WorkspaceID": "ws-1", "AccountID": "acc-inv", "CutDate": "2026-02-01", "CurrentBalanceCents": 110000, "BalanceVersion": 0},
	}
}

func adoptionSchemaJSON(t *testing.T, receipt map[string]any) string {
	t.Helper()
	b, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMigration77ReceiptIntegrity(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"empty", func(r map[string]any) {
			for k := range r {
				delete(r, k)
			}
		}},
		{"missing-id", func(r map[string]any) { delete(r, "ID") }},
		{"id", func(r map[string]any) { r["ID"] = "other" }},
		{"workspace", func(r map[string]any) { r["WorkspaceID"] = "ws-2" }},
		{"account", func(r map[string]any) { r["AccountID"] = "acc-checking" }},
		{"version", func(r map[string]any) { r["Version"] = 2 }},
		{"version-type", func(r map[string]any) { r["Version"] = "1" }},
		{"date", func(r map[string]any) { r["CutDate"] = "2026-02-02" }},
		{"author", func(r map[string]any) { r["CreatedBy"] = "user-2" }},
		{"timestamp", func(r map[string]any) { r["CreatedAt"] = 2 }},
		{"request", func(r map[string]any) { r["RequestID"] = "44444444-4444-4444-8444-444444444444" }},
		{"reason", func(r map[string]any) { r["Reason"] = "Other reason" }},
		{"effect", func(r map[string]any) { r["FinancialEffectCents"] = 1 }},
		{"effect-null", func(r map[string]any) { r["FinancialEffectCents"] = nil }},
		{"effect-missing", func(r map[string]any) { delete(r, "FinancialEffectCents") }},
		{"effect-real", func(r map[string]any) { r["FinancialEffectCents"] = json.RawMessage("0.0") }},
		{"admission", func(r map[string]any) { r["IntegrationAdmission"] = "ACTIVE" }},
		{"coverage", func(r map[string]any) { r["Coverage"] = "COMPLETE" }},
		{"empty-items", func(r map[string]any) { r["Items"] = []any{} }},
		{"items-type", func(r map[string]any) { r["Items"] = map[string]any{} }},
		{"balance-null", func(r map[string]any) { r["ObservedBalanceCents"] = nil }},
		{"balance-fraction", func(r map[string]any) { r["ObservedBalanceCents"] = 1.5 }},
		{"balance-version-missing", func(r map[string]any) { delete(r, "ObservedBalanceVersion") }},
		{"declaration-null", func(r map[string]any) { r["Declaration"] = nil }},
		{"diagnostic-missing", func(r map[string]any) { delete(r, "Diagnostic") }},
		{"cash-proven", func(r map[string]any) { r["Cash"].(map[string]any)["Evidence"].(map[string]any)["State"] = "PROVEN" }},
		{"unknown-cash-zero", func(r map[string]any) { r["Cash"].(map[string]any)["Cents"] = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := adoptionSchemaReceipt(t)
			tc.change(r)
			mustExecError(t, db, adoptionHeaderFixture, "ws-1", "acc-inv", "2026-02-01", adoptionSchemaJSON(t, r))
			if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_adoption_confirmations WHERE workspace_id='ws-1' AND account_id='acc-inv'`) != 0 {
				t.Fatal("invalid receipt persisted")
			}
		})
	}
	for _, declared := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial-unknown-cash", true: "partial-declared-zero-cash"}[declared], func(t *testing.T) {
			r := adoptionSchemaReceipt(t)
			if declared {
				cash := r["Cash"].(map[string]any)
				cash["Cents"] = 0
				cash["Evidence"].(map[string]any)["State"] = "DECLARED"
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(adoptionHeaderFixture, "ws-1", "acc-inv", "2026-02-01", adoptionSchemaJSON(t, r)); err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMigration77OwnershipDatesItemsAndImmutability(t *testing.T) {
	db := referenceLegacy71(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	receipt := adoptionSchemaJSON(t, adoptionSchemaReceipt(t))
	for _, date := range []any{nil, "2026-02-29", "2026-02-31", "1900-02-29", "0000-01-01", "2026-2-01"} {
		r := adoptionSchemaReceipt(t)
		r["CutDate"] = date
		r["Declaration"].(map[string]any)["CutDate"] = date
		r["Diagnostic"].(map[string]any)["CutDate"] = date
		mustExecError(t, db, adoptionHeaderFixture, "ws-1", "acc-inv", date, adoptionSchemaJSON(t, r))
	}
	foreign := adoptionSchemaReceipt(t)
	foreign["WorkspaceID"] = "ws-2"
	foreign["Declaration"].(map[string]any)["WorkspaceID"] = "ws-2"
	foreign["Diagnostic"].(map[string]any)["WorkspaceID"] = "ws-2"
	mustExecError(t, db, adoptionHeaderFixture, "ws-2", "acc-inv", "2026-02-01", adoptionSchemaJSON(t, foreign))
	ref := strings.Replace(insertReferenceFixture, "'{}','aaaaaaaa", `'{"Position":{"amount_cents":110000}}','aaaaaaaa`, 1)
	mustExecOK(t, db, ref, "ws-1", "inv-1", "acc-inv", "reference-position", "2026-02-01", 110000)
	mustExecOK(t, db, adoptionHeaderFixture, "ws-1", "acc-inv", "2026-02-01", receipt)
	item := `INSERT INTO investment_adoption_items(workspace_id,account_id,confirmation_id,investment_id,position_id,reference_revision_id,represented_cents,economic_identity,identity_basis) VALUES(?,?,'adoption',?,'reference-position','ref',?,'lot-1','Individual contract and lot')`
	mustExecError(t, db, item, "ws-2", "acc-inv", "inv-1", 110000)
	mustExecError(t, db, item, "ws-1", "acc-checking", "inv-1", 110000)
	for _, value := range []any{nil, -1, 1.5, "9223372036854775808", 109999} {
		mustExecError(t, db, item, "ws-1", "acc-inv", "inv-1", value)
	}
	mustExecOK(t, db, item, "ws-1", "acc-inv", "inv-1", 110000)
	mustExecOK(t, db, `INSERT INTO investment_adoption_attempts(workspace_id,account_id,id,request_id,created_by,created_at,reason,code,payload_json,conflicting_confirmation_id) VALUES('ws-1','acc-inv','attempt','33333333-3333-4333-8333-333333333333','user-1',1,'Conflicting payload','CONFLICT','{}','adoption')`)
	for _, table := range []string{"investment_adoption_confirmations", "investment_adoption_items", "investment_adoption_attempts"} {
		mustExecError(t, db, `UPDATE `+table+` SET account_id=account_id`)
		mustExecError(t, db, `DELETE FROM `+table)
		mustExecError(t, db, `INSERT OR REPLACE INTO `+table+` SELECT * FROM `+table)
	}
	mustExecError(t, db, `INSERT INTO investment_adoption_confirmations SELECT workspace_id,account_id,'new',version+1,id,version,'2026-02-02',created_by,created_at,'44444444-4444-4444-8444-444444444444',reason,evidence_version,declaration_version,evidence_token,source_token,payload_json,evidence_json,
 json_set(result_json,'$.ID','new','$.Version',version+1,'$.CutDate','2026-02-02','$.RequestID','44444444-4444-4444-8444-444444444444','$.Declaration.ExpectedAdoptionVersion',version,'$.Declaration.CutDate','2026-02-02','$.Declaration.RequestID','44444444-4444-4444-8444-444444444444','$.Diagnostic.CutDate','2026-02-02'),integration_admission,financial_effect_cents FROM investment_adoption_confirmations`)
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}
