//go:build contabase_legacy_fixtures

package main

import (
	"database/sql"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/services"
)

func newBetaRC78Harness(t *testing.T) *investimentosRoutesHarness {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "true")
	seedPath := filepath.Join(t.TempDir(), "seed77.db")
	if err := database.CreateLegacyInvestmentFixture77(seedPath); err != nil {
		t.Fatal(err)
	}
	seedPools, err := database.OpenLegacyInvestmentFixtureDBs(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seedPools.Close() })
	if _, err := serverFixtureExec(t, seedPools.DB, `INSERT INTO users(id,name,email,password_hash,status) VALUES
 ('admin-user','Admin','admin@example.test','hash','active'),('manager-user','Manager','manager@example.test','hash','active'),('regular-user','Regular','regular@example.test','hash','active');
 INSERT INTO workspaces(id,name) VALUES('workspace-rbac','Synthetic RBAC');
 INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('workspace-rbac','admin-user','ADMIN'),('workspace-rbac','manager-user','MANAGER'),('workspace-rbac','regular-user','USER')`); err != nil {
		t.Fatal(err)
	}
	h := newInvestimentosRoutesHarnessWithDB(t, "true", seedPools.DB)
	path := filepath.Join(t.TempDir(), "beta-rc.db")
	h.exec(t, `VACUUM INTO ?`, path)
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	maintenance.SetMaxOpenConns(1)
	err = database.RunExperimentalInvestmentAdmission78(t.Context(), maintenance, filepath.Join(t.TempDir(), "pre78.db"))
	closeErr := maintenance.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if err := database.RegisterLegacyInvestmentFixture(path); err != nil {
		t.Fatal(err)
	}
	pools, err := database.OpenLegacyInvestmentFixtureDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	h.db = pools.DB
	h.service = auth.NewService(pools.DB)
	h.base.DB, h.base.WriterDB = pools.DB, pools.Immediate
	h.mux = http.NewServeMux()
	registerInvestimentosRoutes(h.mux, h.service, h.signer, h.base)
	h.admin = h.session(t, "admin-user", "workspace-rbac")
	return h
}

// HTTP creation/purchase/position routes after the real maintenance adapter and
// runtime opener. Transfers are explicit synthetic setup through the existing
// atomic balance helper, not a claim of browser/transfer-handler validation.
func TestInvestimentosBetaRCFinancialFlowAfter78(t *testing.T) {
	h := newBetaRC78Harness(t)
	h.exec(t, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='workspace-rbac' AND id='checking-a'`)
	balances := func(cash, investment int64) {
		t.Helper()
		for id, want := range map[string]int64{"checking-a": cash, "inv-a": investment} {
			var got int64
			if err := h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='workspace-rbac' AND id=?`, id).Scan(&got); err != nil || got != want {
				t.Fatalf("%s balance=%d want=%d err=%v", id, got, want, err)
			}
		}
	}
	transfer := func(id, date string, amount int64) {
		t.Helper()
		at, err := time.Parse("2006-01-02", date)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := h.base.WriterDB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		_, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status) VALUES(?,'workspace-rbac','admin-user','checking-a','inv-a','TRANSFER',?,?,?,?,?,'paid')`, id, amount, at.Unix(), date, at.Unix(), date)
		if err != nil {
			t.Fatal(err)
		}
		if err = services.ApplyBalanceEffect(tx, "workspace-rbac", "TRANSFER", "CHECKING", "paid", amount, "checking-a", "inv-a", at.Unix()); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	form := investimentosRouteForm()
	form.Set("name", "Beta RC 78")
	form.Set("principal_amount", "5000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, false), 303)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-rbac' AND name='Beta RC 78'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	balances(1000000, 0) // Registering a purchase cannot invent cash movement.
	transfer("rc-root-transfer", "2026-01-15", 500000)
	balances(500000, 500000)
	f := &o1aFixture{h: h, id: id}
	valuation := func(date string, cents int64) string {
		t.Helper()
		v := url.Values{"valuation_date": {date}, "amount_cents": {strconv.FormatInt(cents, 10)}}
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, v, true, false), 303)
		var position string
		if err := h.db.QueryRow(`SELECT id FROM investment_valuations WHERE workspace_id='workspace-rbac' AND investment_id=? AND valuation_date=?`, id, date).Scan(&position); err != nil {
			t.Fatal(err)
		}
		return position
	}
	f.position = valuation("2026-02-01", 500000)
	f.activate(t)
	transfer("rc-new-transfer", "2026-02-02", 200000)
	balances(300000, 700000)
	purchase := investmentI21Form()
	purchase.Set("purchase_date", "2026-02-03")
	path := "/investimentos/" + id + "/compras"
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, purchase, true, false), 303)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, purchase, true, false), 303)
	var pid string
	if err := h.db.QueryRow(`SELECT id FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id=? AND is_initial=0`, id).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	position := valuation("2026-02-04", 730000)
	input := f.form(t, position, 1)
	before := o1aSnapshot(t, h, "accounts", "transactions", "investment_financial_revisions", "investment_purchase_funding")
	f.integrate(t, input, 409) // Missing funding proof is blocking and atomic.
	if before != o1aSnapshot(t, h, "accounts", "transactions", "investment_financial_revisions", "investment_purchase_funding") {
		t.Fatal("missing proof had partial effects")
	}
	input.Set("funding", `[{"PurchaseID":"`+pid+`","TransferID":"rc-new-transfer","AmountCents":200000}]`)
	result := f.integrate(t, input, 200)
	if result.NetAdjustmentCents != 30000 || result.BalanceCents != 730000 {
		t.Fatalf("wrong delta: %+v", result)
	}
	balances(300000, 730000)
	before = o1aSnapshot(t, h, "accounts", "transactions", "investment_financial_revisions", "investment_purchase_funding")
	f.integrate(t, input, 200)
	if before != o1aSnapshot(t, h, "accounts", "transactions", "investment_financial_revisions", "investment_purchase_funding") {
		t.Fatal("replay duplicated financial effect")
	}
	for _, q := range []string{
		`SELECT count(*) FROM transactions WHERE workspace_id='workspace-rbac' AND type='TRANSFER'`,
		`SELECT count(*) FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id='` + id + `'`,
	} {
		var n int
		if err := h.db.QueryRow(q).Scan(&n); err != nil || n != 2 {
			t.Fatal("duplicate/missing operations", n, err)
		}
	}
	foreign := h.session(t, "admin-user", "workspace-b")
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id, foreign, nil, false, false), 404)
	requireInvestimentosStatus(t, h.request(t, "POST", path, foreign, investmentI21Form(), true, false), 404)
	regular := h.session(t, "regular-user", "workspace-rbac")
	requireInvestimentosStatus(t, h.request(t, "POST", path, regular, investmentI21Form(), true, false), 403)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI21Form(), false, false), 403)
	detail := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, detail, 200)
	if !strings.Contains(detail.Body.String(), "Beta RC 78") {
		t.Fatal("missing investment detail")
	}
	t.Log("RC_HTTP schema=78 checking:1000000->500000->300000 investment:0->500000->700000->730000 purchases=2 transfers=2 replay=exact missing_funding=rollback W/A=isolated RBAC=403 CSRF=403")
}
