package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

type o1aFixture struct {
	h                  *investimentosRoutesHarness
	id, root, position string
}

func newO1A(t *testing.T) *o1aFixture {
	t.Helper()
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "true")
	h := newInvestimentosI22Harness(t)
	form := investimentosRouteForm()
	form.Set("principal_amount", "5000,00")
	form.Set("name", "O1A")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, false), 303)
	f := &o1aFixture{h: h, position: "o1a-root"}
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-rbac' AND name='O1A'`).Scan(&f.id); err != nil {
		t.Fatal(err)
	}
	h.exec(t, `UPDATE accounts SET initial_balance=500000,current_balance=500000 WHERE workspace_id='workspace-rbac' AND id='inv-a'`)
	f.valuation(t, f.position, "2026-02-01", 500000)
	return f
}
func (f *o1aFixture) path(operation string) string {
	return "/investimentos/" + f.id + "/financeiro/" + operation
}
func (f *o1aFixture) valuation(t *testing.T, id, date string, amount int64) {
	t.Helper()
	f.h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES(?,'workspace-rbac',?,?,?,'admin-user')`, id, f.id, date, amount)
}
func (f *o1aFixture) form(t *testing.T, position string, version int64) url.Values {
	t.Helper()
	c, err := services.ReadInvestmentReferenceCandidate(context.Background(), f.h.db, "workspace-rbac", f.id, position)
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{"account_id": {"inv-a"}, "position_id": {position}, "coverage_token": {c.CoverageToken}, "expected_version": {strconv.FormatInt(version, 10)}, "request_id": {uuid.NewString()}, "reason": {"Extrato conferido"}, "day_closed": {"true"}, "confirm": {"true"}, "represented_cents": {strconv.FormatInt(c.Snapshot.Position.AmountCents, 10)}, "economic_date": {c.Snapshot.Position.ValuationDate}, "reference_revision_id": {f.root}}
}
func (f *o1aFixture) confirm(t *testing.T) url.Values {
	t.Helper()
	form := f.form(t, f.position, 0)
	before := o1aSnapshot(t, f.h, "accounts", "transactions", "box_virtual_ledger", "investment_valuations", "account_balance_adjustments")
	rr := f.h.request(t, "POST", f.path("referencia"), f.h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 200)
	var r services.InvestmentReferenceOperationResult
	if err := json.Unmarshal(rr.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.State != "VALID" || r.Version != 1 || r.Outcome != services.InvestmentOperationCreated {
		t.Fatalf("reference %+v", r)
	}
	f.root = r.ID
	if before != o1aSnapshot(t, f.h, "accounts", "transactions", "box_virtual_ledger", "investment_valuations", "account_balance_adjustments") {
		t.Fatal("initial reference moved finances")
	}
	return form
}
func (f *o1aFixture) integrate(t *testing.T, form url.Values, want int) services.InvestmentIntegrationOperationResult {
	t.Helper()
	rr := f.h.request(t, "POST", f.path("posicao"), f.h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, want)
	var r services.InvestmentIntegrationOperationResult
	if want == 200 {
		if err := json.Unmarshal(rr.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Outcome != services.InvestmentOperationCreated && r.Outcome != services.InvestmentOperationReplayed {
			t.Fatalf("missing transaction outcome: %+v", r)
		}
	} else if strings.Contains(rr.Body.String(), `"outcome"`) {
		t.Fatal("failed transaction reported a successful outcome")
	}
	return r
}
func (f *o1aFixture) activate(t *testing.T) {
	t.Helper()
	f.confirm(t)
	r := f.integrate(t, f.form(t, f.position, 0), 200)
	if r.NetAdjustmentCents != 0 || r.BalanceCents != 500000 || len(r.AdjustmentIDs) != 0 {
		t.Fatalf("activation %+v", r)
	}
}
func (f *o1aFixture) state(t *testing.T) services.InvestmentOperationalState {
	t.Helper()
	rr := f.h.request(t, "GET", f.path("estado"), f.h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	var s services.InvestmentOperationalState
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Exact logical snapshots of disposable tenant data. Auth sessions are outside
// the compared surface because withAuth legitimately refreshes session activity.
func o1aSnapshot(t *testing.T, h *investimentosRoutesHarness, tables ...string) string {
	t.Helper()
	return o1aWorkspaceSnapshot(t, h, "workspace-rbac", tables...)
}

func o1aWorkspaceSnapshot(t *testing.T, h *investimentosRoutesHarness, workspace string, tables ...string) string {
	t.Helper()
	var out strings.Builder
	for _, table := range tables {
		query := `SELECT * FROM ` + table + ` WHERE workspace_id=? ORDER BY rowid`
		if table == "financial_transactions" {
			query = `SELECT * FROM financial_transactions WHERE workspace_id=? ORDER BY id`
		}
		if table == "box_virtual_ledger" {
			query = `SELECT l.* FROM box_virtual_ledger l JOIN boxes b ON b.id=l.box_id WHERE b.workspace_id=? ORDER BY l.rowid`
		}
		rows, err := h.db.Query(query, workspace)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%s:%#v\n", table, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

func TestInvestimentosO1AGuards(t *testing.T) {
	f := newO1A(t)
	h := f.h
	f.confirm(t)
	form := f.form(t, f.position, 0)
	for _, op := range []string{"estado", "referencia", "posicao"} {
		method := "POST"
		if op == "estado" {
			method = "GET"
		}
		requireInvestimentosStatus(t, h.request(t, method, f.path(op), "", form, true, false), 303)
		for _, user := range []string{"manager-user", "regular-user"} {
			requireInvestimentosStatus(t, h.request(t, method, f.path(op), h.session(t, user, "workspace-rbac"), form, true, false), 403)
		}
		if method == "POST" {
			requireInvestimentosStatus(t, h.request(t, method, f.path(op), h.admin, form, false, false), 403)
			req := httptest.NewRequest(method, f.path(op), strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.admin})
			addAdminUsersRBACCSRF(t, req, h.signer)
			req.Header.Set("X-CSRF-Token", "invalid")
			rr := httptest.NewRecorder()
			h.mux.ServeHTTP(rr, req)
			requireInvestimentosStatus(t, rr, 403)
		}
		requireInvestimentosStatus(t, h.request(t, "DELETE", f.path(op), h.admin, form, true, false), 405)
		requireInvestimentosStatus(t, h.request(t, method, f.path(op)+"/extra", h.admin, form, true, false), 404)
		requireInvestimentosStatus(t, h.request(t, method, f.path(op), h.session(t, "admin-user", "workspace-b"), form, true, false), 404)
	}
	for _, flag := range []string{"", "false", "garbage"} {
		t.Setenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS", flag)
		for _, op := range []string{"estado", "referencia", "posicao"} {
			method := "POST"
			if op == "estado" {
				method = "GET"
			}
			requireInvestimentosStatus(t, h.request(t, method, f.path(op), h.admin, form, true, false), 404)
		}
	}
}

func TestInvestimentosO1AReferenceAndReadOnlyState(t *testing.T) {
	f := newO1A(t)
	h := f.h
	if s := f.state(t); s.ReferenceState != "MISSING" || s.FinancialVersion != 0 || s.Candidate == nil {
		t.Fatalf("missing %+v", s)
	}
	form := f.form(t, f.position, 0)
	form.Set("represented_cents", "499000")
	requireInvestimentosStatus(t, h.request(t, "POST", f.path("referencia"), h.admin, form, true, false), 409)
	for _, field := range []string{"request_id", "expected_version", "confirm", "represented_cents"} {
		bad := f.form(t, f.position, 0)
		bad.Set(field, "")
		requireInvestimentosStatus(t, h.request(t, "POST", f.path("referencia"), h.admin, bad, true, false), 400)
	}
	bad := f.form(t, f.position, 0)
	bad.Set("economic_date", "2026-02-31")
	requireInvestimentosStatus(t, h.request(t, "POST", f.path("referencia"), h.admin, bad, true, false), 409)
	bad = f.form(t, f.position, 0)
	bad.Set("account_id", "inv-b")
	requireInvestimentosStatus(t, h.request(t, "POST", f.path("referencia"), h.admin, bad, true, false), 404)
	form = f.confirm(t)
	rr := h.request(t, "POST", f.path("referencia"), h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 200)
	var replay services.InvestmentReferenceOperationResult
	if err := json.Unmarshal(rr.Body.Bytes(), &replay); err != nil || replay.Outcome != services.InvestmentOperationReplayed || replay.ID != f.root {
		t.Fatalf("reference replay: %+v %v", replay, err)
	}
	form.Set("reason", "Outro payload")
	requireInvestimentosStatus(t, h.request(t, "POST", f.path("referencia"), h.admin, form, true, false), 409)
	tables := []string{"accounts", "transactions", "box_virtual_ledger", "investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions", "investment_purchase_funding", "investment_valuations", "account_balance_adjustments"}
	before := o1aSnapshot(t, h, tables...)
	s := f.state(t)
	if s.ReferenceState != "VALID" || s.BlockReason != "ROOT_ACTIVATION_REQUIRED" {
		t.Fatalf("confirmed %+v", s)
	}
	if before != o1aSnapshot(t, h, tables...) {
		t.Fatal("state mutated financial data")
	}
	h.exec(t, `UPDATE investment_valuations SET amount_cents=500100,updated_at=updated_at+1 WHERE workspace_id='workspace-rbac' AND id=?`, f.position)
	if s = f.state(t); s.ReferenceState != "INVALIDATED" {
		t.Fatalf("stale %+v", s)
	}
	// Saved request remains recoverable even after the source changes.
	form.Set("reason", "Extrato conferido")
	requireInvestimentosStatus(t, h.request(t, "POST", f.path("referencia"), h.admin, form, true, false), 200)
	f.integrate(t, f.form(t, f.position, 0), 409)
}

func TestInvestimentosO1AChangesReplayAndAtomicCorrection(t *testing.T) {
	for _, tc := range []struct {
		name          string
		amount, delta int64
	}{{"gain", 530000, 30000}, {"loss", 480000, -20000}, {"zero", 500000, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newO1A(t)
			f.activate(t)
			h := f.h
			legacy := o1aSnapshot(t, h, "transactions", "box_virtual_ledger")
			f.valuation(t, "next", "2026-03-01", tc.amount)
			form := f.form(t, "next", 1)
			// Payload authority is ignored in favor of the real authenticated context.
			form.Set("workspace_id", "workspace-b")
			form.Set("created_by", "regular-user")
			r := f.integrate(t, form, 200)
			if r.NetAdjustmentCents != tc.delta || r.BalanceCents != tc.amount || r.Version != 2 {
				t.Fatalf("integration %+v", r)
			}
			if tc.delta == 0 && len(r.AdjustmentIDs) != 0 {
				t.Fatal("zero adjustment created money")
			}
			replay := f.integrate(t, form, 200)
			if r.Outcome != services.InvestmentOperationCreated || replay.Outcome != services.InvestmentOperationReplayed || !reflect.DeepEqual(r.InvestmentIntegrationResult, replay.InvestmentIntegrationResult) {
				t.Fatalf("replay %+v != %+v", r, replay)
			}
			form.Set("reason", "Divergent")
			f.integrate(t, form, 409)
			s := f.state(t)
			if s.FinancialVersion != 2 || s.LastIntegratedPosition == nil || s.LastIntegratedPosition.ID != "next" {
				t.Fatalf("state %+v", s)
			}
			var author string
			if err := h.db.QueryRow(`SELECT created_by FROM investment_financial_revisions WHERE workspace_id='workspace-rbac' AND id=?`, r.ID).Scan(&author); err != nil || author != "admin-user" {
				t.Fatalf("author %q %v", author, err)
			}
			if legacy != o1aSnapshot(t, h, "transactions", "box_virtual_ledger") {
				t.Fatal("integration wrote revenue/transfer/ledger")
			}
			form = f.form(t, "next", 2)
			form.Set("correction_cents", "510000")
			corrected := f.integrate(t, form, 200)
			if corrected.BalanceCents != 510000 {
				t.Fatalf("correction %+v", corrected)
			}
			if replay = f.integrate(t, form, 200); corrected.Outcome != services.InvestmentOperationCreated || replay.Outcome != services.InvestmentOperationReplayed || !reflect.DeepEqual(replay.InvestmentIntegrationResult, corrected.InvestmentIntegrationResult) {
				t.Fatal("atomic correction replay diverged")
			}
		})
	}
}

func TestInvestimentosO1APurchaseFundingAndRequiredExample(t *testing.T) {
	f := newO1A(t)
	f.activate(t)
	h := f.h
	at, _ := time.Parse("2006-01-02", "2026-02-02")
	tx, err := h.base.WriterDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES('o1a-transfer','workspace-rbac','admin-user','checking-a','inv-a','TRANSFER',200000,?,?,'paid')`, at.Unix(), at.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err = services.ApplyBalanceEffect(tx, "workspace-rbac", "TRANSFER", "CHECKING", "paid", 200000, "checking-a", "inv-a", at.Unix()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	purchase := investmentI21Form()
	purchase.Set("principal_amount", "2000,00")
	purchase.Set("purchase_date", "2026-02-03")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+f.id+"/compras", h.admin, purchase, true, false), 303)
	var pid string
	if err = h.db.QueryRow(`SELECT id FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id=? AND is_initial=0`, f.id).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	f.valuation(t, "bought", "2026-02-04", 700000)
	form := f.form(t, "bought", 1)
	f.integrate(t, form, 409)
	if s := f.state(t); s.BlockReason != "PURCHASE_FUNDING_PENDING" || len(s.PendingPurchases) != 1 {
		t.Fatalf("pending %+v", s)
	}
	form.Set("funding", `[{"PurchaseID":"`+pid+`","TransferID":"o1a-transfer","AmountCents":200000}]`)
	legacy := o1aSnapshot(t, h, "transactions", "box_virtual_ledger")
	r := f.integrate(t, form, 200)
	if r.NetAdjustmentCents != 0 || r.BalanceCents != 700000 {
		t.Fatalf("fictitious purchase return %+v", r)
	}
	f.valuation(t, "gained", "2026-02-05", 730000)
	r = f.integrate(t, f.form(t, "gained", 2), 200)
	if r.NetAdjustmentCents != 30000 || r.BalanceCents != 730000 {
		t.Fatalf("required example %+v", r)
	}
	if legacy != o1aSnapshot(t, h, "transactions", "box_virtual_ledger") {
		t.Fatal("created income or transfer")
	}
}

func TestInvestimentosO1APendingWithdrawalAndLegacyPreservation(t *testing.T) {
	f := newO1A(t)
	f.activate(t)
	h := f.h
	before := o1aSnapshot(t, h, "accounts", "transactions", "box_virtual_ledger", "account_balance_adjustments", "investment_financial_revisions")
	form := investmentI22Form("initial:" + f.id)
	form.Set("withdrawal_date", "2026-02-15")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+f.id+"/retiradas", h.admin, form, true, false), 303)
	// Existing valuation HTTP flow stays manual, even with the new feature ON.
	legacy := url.Values{"valuation_date": {"2026-03-01"}, "amount_cents": {"490000"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+f.id+"/valuations", h.admin, legacy, true, false), 303)
	if before != o1aSnapshot(t, h, "accounts", "transactions", "box_virtual_ledger", "account_balance_adjustments", "investment_financial_revisions") {
		t.Fatal("legacy flow auto-activated finances")
	}
	var position string
	if err := h.db.QueryRow(`SELECT id FROM investment_valuations WHERE workspace_id='workspace-rbac' AND investment_id=? AND valuation_date='2026-03-01'`, f.id).Scan(&position); err != nil {
		t.Fatal(err)
	}
	f.integrate(t, f.form(t, position, 1), 409)
	if s := f.state(t); len(s.PendingWithdrawals) != 1 || s.BlockReason != "WITHDRAWAL_RECONCILIATION_PENDING" {
		t.Fatalf("withdrawal %+v", s)
	}
}

func TestInvestimentosO1ATenantAndRollback(t *testing.T) {
	f := newO1A(t)
	f.activate(t)
	h := f.h
	f.valuation(t, "next", "2026-03-01", 530000)
	tables := []string{"accounts", "transactions", "investment_financial_revisions", "investment_financial_adjustments", "account_balance_adjustments", "investment_valuations"}
	before := o1aSnapshot(t, h, tables...)
	for _, reference := range []string{"", "not-a-uuid"} {
		form := f.form(t, "next", 1)
		form.Set("reference_revision_id", reference)
		f.integrate(t, form, 400)
	}
	for _, field := range []string{"account_id", "reference_revision_id", "position_id"} {
		form := f.form(t, "next", 1)
		missing := "foreign"
		if field == "reference_revision_id" {
			missing = uuid.NewString()
		}
		form.Set(field, missing)
		f.integrate(t, form, 404)
	}
	// A real foreign reference exists, rather than testing only a missing UUID.
	other, err := services.CreateInvestment(context.Background(), h.base.WriterDB, services.CreateInvestmentInput{WorkspaceID: "workspace-b", AccountID: "inv-b", CreatedBy: "admin-user", RequestID: uuid.NewString(), Name: "B", Institution: "B", Kind: "CDB", TaxRegime: "IR_REGRESSIVO", PrincipalCents: 10000, AppliedDate: "2026-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('foreign-position','workspace-b',?,'2026-02-01',10000,'admin-user')`, other.ID)
	c, err := services.ReadInvestmentReferenceCandidate(context.Background(), h.db, "workspace-b", other.ID, "foreign-position")
	if err != nil {
		t.Fatal(err)
	}
	value := int64(10000)
	ref, err := services.RecordInvestmentReference(context.Background(), h.base.WriterDB, services.RecordInvestmentReferenceInput{WorkspaceID: "workspace-b", InvestmentID: other.ID, AccountID: "inv-b", PositionID: "foreign-position", CreatedBy: "admin-user", RequestID: uuid.NewString(), Reason: "B", CoverageToken: c.CoverageToken, RepresentedCents: &value, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	foreignBefore := o1aWorkspaceSnapshot(t, h, "workspace-b", tables...)
	for field, value := range map[string]string{"account_id": "inv-b", "reference_revision_id": ref.ID, "position_id": "foreign-position"} {
		form := f.form(t, "next", 1)
		form.Set(field, value)
		rr := h.request(t, "POST", f.path("posicao"), h.admin, form, true, false)
		requireInvestimentosStatus(t, rr, 404)
		if strings.Contains(rr.Body.String(), ref.ID) || strings.Contains(rr.Body.String(), other.ID) || strings.Contains(rr.Body.String(), "foreign-position") {
			t.Fatal("foreign data disclosed")
		}
		_, err := services.IntegrateInvestmentPositionWithOutcome(context.Background(), h.base.WriterDB, services.IntegrateInvestmentPositionInput{WorkspaceID: "workspace-rbac", InvestmentID: f.id, AccountID: form.Get("account_id"), ReferenceRevisionID: form.Get("reference_revision_id"), PositionID: form.Get("position_id"), CoverageToken: form.Get("coverage_token"), ExpectedVersion: 1, CreatedBy: "admin-user", RequestID: form.Get("request_id"), Reason: form.Get("reason"), DayClosed: true})
		if !errors.Is(err, services.ErrInvestmentNotFound) {
			t.Fatalf("service tenant isolation: %v", err)
		}
	}
	if _, err := services.ReadInvestmentReferenceRevision(context.Background(), h.db, "workspace-rbac", other.ID, ref.ID); !errors.Is(err, services.ErrInvestmentNotFound) {
		t.Fatalf("foreign reference readable: %v", err)
	}
	if foreignBefore != o1aWorkspaceSnapshot(t, h, "workspace-b", tables...) {
		t.Fatal("tenant failure changed foreign finances")
	}
	requireInvestimentosStatus(t, h.request(t, "GET", f.path("estado")+"?position_id=foreign-position", h.admin, nil, false, false), 404)
	if before != o1aSnapshot(t, h, tables...) {
		t.Fatal("tenant failure changed local finances")
	}
	h.exec(t, `CREATE TRIGGER o1a_fail_revision BEFORE INSERT ON investment_financial_revisions BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	f.integrate(t, f.form(t, "next", 1), 500)
	if before != o1aSnapshot(t, h, tables...) {
		t.Fatal("service error leaked financial writes")
	}
	h.exec(t, `DROP TRIGGER o1a_fail_revision`)
	f.integrate(t, f.form(t, "next", 1), 200)
	// Correction and reversal both roll back when the final proof cannot persist.
	h.exec(t, `CREATE TRIGGER o1a_fail_revision BEFORE INSERT ON investment_financial_revisions BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	before = o1aSnapshot(t, h, tables...)
	form := f.form(t, "next", 2)
	form.Set("correction_cents", "540000")
	f.integrate(t, form, 500)
	if before != o1aSnapshot(t, h, tables...) {
		t.Fatal("correction saved outside financial transaction")
	}
}

func TestInvestimentosO1AConcurrentVersionsAndReplay(t *testing.T) {
	for _, operation := range []string{"referencia", "posicao"} {
		for _, mode := range []string{"same payload", "divergent payload", "competing version"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				f := newO1A(t)
				position, version := f.position, int64(0)
				if operation == "posicao" {
					f.activate(t)
					f.valuation(t, "next", "2026-03-01", 530000)
					position, version = "next", 1
				}
				a, b := f.form(t, position, version), f.form(t, position, version)
				if mode != "competing version" {
					b.Set("request_id", a.Get("request_id"))
				}
				if mode == "divergent payload" {
					b.Set("reason", "Divergent")
				}
				start := make(chan struct{})
				responses := make(chan *httptest.ResponseRecorder, 2)
				var wg sync.WaitGroup
				for _, form := range []url.Values{a, b} {
					wg.Add(1)
					go func(form url.Values) {
						defer wg.Done()
						<-start
						responses <- f.h.request(t, "POST", f.path(operation), f.h.admin, form, true, false)
					}(form)
				}
				close(start)
				wg.Wait()
				close(responses)
				success, conflict, created, replayed := 0, 0, 0, 0
				var id string
				for rr := range responses {
					switch rr.Code {
					case 200:
						success++
						var result struct {
							ID      string
							Outcome services.InvestmentOperationOutcome `json:"outcome"`
						}
						if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						if id != "" && id != result.ID {
							t.Fatal("concurrent replay created two results")
						}
						id = result.ID
						switch result.Outcome {
						case services.InvestmentOperationCreated:
							created++
						case services.InvestmentOperationReplayed:
							replayed++
						default:
							t.Fatalf("outcome %q", result.Outcome)
						}
					case 409:
						conflict++
					default:
						t.Fatalf("concurrent status=%d %s", rr.Code, rr.Body.String())
					}
				}
				if created != 1 || (mode == "same payload" && (success != 2 || replayed != 1 || conflict != 0)) || (mode != "same payload" && (success != 1 || replayed != 0 || conflict != 1)) {
					t.Fatalf("success=%d conflict=%d created=%d replayed=%d", success, conflict, created, replayed)
				}
				var count int
				want := 0
				if operation == "posicao" {
					want = 1
				}
				if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM account_balance_adjustments WHERE workspace_id='workspace-rbac'`).Scan(&count); err != nil || count != want {
					t.Fatalf("adjustment count %d want %d: %v", count, want, err)
				}
				if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM investment_reference_revisions WHERE workspace_id='workspace-rbac'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("reference count %d: %v", count, err)
				}
				if operation == "posicao" {
					if err := f.h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='workspace-rbac' AND id='inv-a'`).Scan(&count); err != nil || count != 530000 {
						t.Fatalf("concurrent balance %d: %v", count, err)
					}
				}
			})
		}
	}
}

func TestInvestimentosO1ASnapshotLedgerScopeAndSensitivity(t *testing.T) {
	f := newO1A(t)
	h := f.h
	for _, workspace := range []string{"workspace-rbac", "workspace-b"} {
		h.exec(t, `INSERT INTO categories(id,workspace_id,name,type) VALUES(?,?,?,'EXPENSE')`, "cat:"+workspace, workspace, "Snapshot")
		h.exec(t, `INSERT INTO boxes(id,workspace_id,name,category_id) VALUES(?,?,?,?)`, "box:"+workspace, workspace, "Snapshot", "cat:"+workspace)
		h.exec(t, `INSERT INTO box_virtual_ledger(id,box_id,amount,type,reference_date) VALUES(?,?,100,'RECHARGE',1)`, "ledger:"+workspace, "box:"+workspace)
	}
	before := o1aSnapshot(t, h, "box_virtual_ledger")
	h.exec(t, `UPDATE box_virtual_ledger SET amount=200 WHERE id='ledger:workspace-b'`)
	if before != o1aSnapshot(t, h, "box_virtual_ledger") {
		t.Fatal("snapshot included foreign ledger")
	}
	h.exec(t, `UPDATE box_virtual_ledger SET amount=300 WHERE id='ledger:workspace-rbac'`)
	if before == o1aSnapshot(t, h, "box_virtual_ledger") {
		t.Fatal("snapshot missed local financial mutation")
	}
}
