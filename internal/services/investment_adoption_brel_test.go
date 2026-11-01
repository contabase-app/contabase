//go:build contabase_legacy_fixtures

package services

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
)

func legacyAdoptionConfirmationFixture(t *testing.T) (*database.RuntimeDBs, string, []models.Investment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adoption77.db")
	if err := database.CreateLegacyInvestmentFixture77(path); err != nil {
		t.Fatal(err)
	}
	p, err := database.OpenLegacyInvestmentFixtureDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	seedInvestmentServiceFixtures(t, p.DB)
	exec(t, p.Immediate, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-a','user-a','ADMIN'),('ws-b','user-b','ADMIN')`)
	var investments []models.Investment
	for n, cost := range []int64{900000, 700000} {
		input := investmentCreateInput("ws-a", "acc-inv-a")
		input.PrincipalCents = cost
		i := mustCreateInvestment(t, p.Immediate, input)
		investments = append(investments, i)
		insertValuationRow(t, p.Immediate, "adoption-position-"+i.ID, "ws-a", i.ID, adoptionTestCut, []int64{1000000, 800000}[n])
	}
	exec(t, p.Immediate, `UPDATE accounts SET current_balance=2000000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	return p, path, investments
}

func TestBRelConfirmationTokensSurviveMigration78(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	p, path, inv := legacyAdoptionConfirmationFixture(t)
	// Duplicate NULL ids, negative rowid and ORDER BY id ties are deliberate.
	for _, rowid := range []int64{-1, 100, 101} {
		exec(t, p.DB, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date,date_civil,description,notes) VALUES(?,'ws-a','user-a','acc-inv-a','TRANSFER',1,1,'2026-03-05','duplicata','á; CBTX|1|37|')`, rowid)
	}
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
	confirmation, err := ConfirmInvestmentAdoption(t.Context(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	before, err := ReadInvestmentAdoptionConfirmationCandidate(t.Context(), p.DB, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadInvestmentAdoptionConfirmationValidity(t.Context(), p.DB, "ws-a", "acc-inv-a", confirmation.ID, "user-a")
	if err != nil || state.State != "CURRENT" {
		t.Fatalf("before validity: %+v %v", state, err)
	}
	var source, evidence, receipt string
	if err = p.DB.QueryRow(`SELECT source_token,evidence_json,result_json FROM investment_adoption_confirmations WHERE workspace_id='ws-a' AND account_id='acc-inv-a' AND id=?`, confirmation.ID).Scan(&source, &evidence, &receipt); err != nil {
		t.Fatal(err)
	}
	// Confirm the explicit projection still equals the historical SELECT *,
	// including the column names/physical order and tie iteration order.
	tx, err := p.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := adoptionSourceEvidenceTx(t.Context(), tx, "ws-a", "acc-inv-a")
	if err != nil {
		t.Fatal(err)
	}
	r, err := tx.Query(`SELECT t.* FROM transactions t WHERE t.workspace_id=? AND (t.account_id=? OR t.destination_account_id=?) ORDER BY t.id`, "ws-a", "acc-inv-a", "acc-inv-a")
	if err != nil {
		t.Fatal(err)
	}
	legacy := adoptionEvidenceRows{Kind: "transactions"}
	legacy.Columns, err = r.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		v := make([]any, len(legacy.Columns))
		dest := make([]any, len(v))
		for i := range v {
			dest[i] = &v[i]
		}
		if err = r.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		for i, x := range v {
			if b, ok := x.([]byte); ok {
				v[i] = string(b)
			}
		}
		legacy.Rows = append(legacy.Rows, v)
	}
	if err = r.Err(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	tx.Rollback()
	var old []byte
	for _, s := range sources {
		if s.Kind == "transactions" {
			a, _ := json.Marshal(s)
			b, _ := json.Marshal(legacy)
			if !bytes.Equal(a, b) {
				t.Fatal("projection changed v77 evidence")
			}
			old = a
		}
	}
	view := func(db *sql.DB) []string {
		r, e := db.Query(`SELECT * FROM financial_transactions LIMIT 0`)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		cols, e := r.Columns()
		if e != nil {
			t.Fatal(e)
		}
		return cols
	}
	viewBefore := view(p.DB)
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	maintenance.SetMaxOpenConns(1)
	if err = database.RunExperimentalInvestmentAdmission78(t.Context(), maintenance, filepath.Join(t.TempDir(), "pre78.db")); err != nil {
		t.Fatal(err)
	}
	if maintenance.Stats().OpenConnections != 0 {
		t.Fatal("maintenance connection returned to pool")
	}
	maintenance.Close()
	post, err := database.OpenLegacyInvestmentFixtureDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer post.Close()
	after, err := ReadInvestmentAdoptionConfirmationCandidate(t.Context(), post.DB, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.EvidenceSnapshot, after.EvidenceSnapshot) || before.EvidenceToken != after.EvidenceToken {
		t.Fatal("migration changed evidence bytes/token")
	}
	var sourceAfter, evidenceAfter, receiptAfter string
	if err = post.DB.QueryRow(`SELECT source_token,evidence_json,result_json FROM investment_adoption_confirmations WHERE workspace_id='ws-a' AND account_id='acc-inv-a' AND id=?`, confirmation.ID).Scan(&sourceAfter, &evidenceAfter, &receiptAfter); err != nil {
		t.Fatal(err)
	}
	if source != sourceAfter || evidence != evidenceAfter || receipt != receiptAfter || len(old) == 0 {
		t.Fatal("stored tokens/receipt rewritten")
	}
	state, err = ReadInvestmentAdoptionConfirmationValidity(t.Context(), post.DB, "ws-a", "acc-inv-a", confirmation.ID, "user-a")
	if err != nil || state.State != "CURRENT" {
		t.Fatalf("after validity: %+v %v", state, err)
	}
	if !reflect.DeepEqual(viewBefore, view(post.DB)) {
		t.Fatal("financial_transactions projection changed")
	}
}
