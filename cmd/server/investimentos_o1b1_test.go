package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

type o1b1Fixture struct {
	*o1aFixture
	withdrawal string
}

func newO1B1(t *testing.T, principal string) *o1b1Fixture {
	t.Helper()
	f := &o1b1Fixture{o1aFixture: newO1A(t)}
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "true")
	form := investmentI22Form("initial:" + f.id)
	form.Set("mode", "TOTAL")
	form.Set("received_amount", "5255,00")
	if principal != "" {
		form.Set("principal_initial:"+f.id, principal)
	}
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos/"+f.id+"/retiradas", f.h.admin, form, true, false), 303)
	if err := f.h.db.QueryRow(`SELECT id FROM investment_withdrawals WHERE workspace_id='workspace-rbac' AND investment_id=? AND request_id=?`, f.id, form.Get("request_id")).Scan(&f.withdrawal); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM investment_settlement_revisions WHERE workspace_id='workspace-rbac'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy withdrawal activated C1: %d %v", count, err)
	}
	return f
}

func (f *o1b1Fixture) path() string {
	return "/investimentos/" + f.id + "/financeiro/retiradas/" + f.withdrawal + "/liquidacao"
}

func (f *o1b1Fixture) form(version int64) url.Values {
	return url.Values{"account_id": {"inv-a"}, "destination_account_id": {"inv-a"}, "economic_date": {"2026-03-15"},
		"expected_version": {strconv.FormatInt(version, 10)}, "request_id": {uuid.NewString()}, "reason": {"Liquidação conferida pelo operador"},
		"gross_cents": {"530000"}, "ir_cents": {"4500"}, "iof_cents": {"0"}, "fees_cents": {"0"}, "net_cents": {"525500"},
		"patrimony_cents": {"530000"}, "patrimony_evidence": {"Extrato identifica o patrimônio retirado"}}
}

type o1b1Result struct {
	Settlement              services.InvestmentSettlement
	ReceiptConfirmationKind string
}

func (f *o1b1Fixture) post(t *testing.T, form url.Values, confirm bool, want int) o1b1Result {
	t.Helper()
	path := f.path()
	if confirm {
		path += "/conciliar"
	}
	rr := f.h.request(t, "POST", path, f.h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, want)
	var result o1b1Result
	if want == 200 {
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Settlement.ID == "" || rr.Header().Get("Idempotency-Key") != form.Get("request_id") || rr.Header().Get("Link") != "<"+f.path()+">; rel=\"status\"" {
			t.Fatalf("missing durable result/headers: %s %v", rr.Body.String(), rr.Header())
		}
		if strings.Contains(strings.ToLower(rr.Body.String()), `"outcome"`) {
			t.Fatal("C1 cannot report invocation outcome")
		}
	}
	return result
}

func (f *o1b1Fixture) read(t *testing.T, want int) services.InvestmentSettlementOperationalState {
	t.Helper()
	rr := f.h.request(t, "GET", f.path(), f.h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, want)
	var s services.InvestmentSettlementOperationalState
	if want == 200 {
		if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("financial response must not be cached")
		}
	}
	return s
}

func (f *o1b1Fixture) stable(t *testing.T) string {
	t.Helper()
	return o1aSnapshot(t, f.h, "accounts", "transactions", "account_balance_adjustments", "box_virtual_ledger",
		"investments", "investment_valuations", "investment_purchases", "investment_withdrawals", "investment_withdrawal_allocations",
		"investment_reference_revisions", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding",
		"investment_settlement_applications", "investment_settlement_prior_effects", "investment_result_classifications")
}

func (f *o1b1Fixture) proof(t *testing.T) string {
	t.Helper()
	return o1aSnapshot(t, f.h, "investment_settlement_revisions", "investment_settlement_receipts")
}

func (f *o1b1Fixture) receipt(t *testing.T, id, workspace, source, destination, typ, status string, amount int64) {
	t.Helper()
	var dest any = destination
	if destination == "" {
		dest = nil
	}
	at := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC).Unix()
	f.h.exec(t, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES(?,?,'admin-user',?,?,?,?,?,?,?)`, id, workspace, source, dest, typ, amount, at, at, status)
}

func o1b1Claims(form url.Values, receipts ...services.InvestmentSettlementReceipt) {
	b, err := json.Marshal(receipts)
	if err != nil {
		panic(err)
	}
	form.Set("receipts", string(b))
}

func o1b1Confirm(form url.Values) {
	form.Set("confirm_receipt", "true")
	form.Set("receipt_evidence", "Declaro ter conferido o recebimento no extrato")
}

func TestInvestimentosO1B1RequiredScenarioReadAndLegacy(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before, proof := f.stable(t), f.proof(t)
	s := f.read(t, 200)
	if s.State != "MISSING" || s.Version != 0 || s.Settlement != nil || s.Withdrawal.ID != f.withdrawal || s.PrincipalCents == nil || *s.PrincipalCents != 500000 {
		t.Fatalf("missing state: %+v", s)
	}
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("read wrote data")
	}
	form := f.form(0)
	for key, value := range map[string]string{"workspace_id": "workspace-b", "created_by": "regular-user", "user_id": "regular-user", "investment_id": "foreign", "withdrawal_id": "foreign", "role": "ADMIN"} {
		form.Set(key, value)
	}
	r := f.post(t, form, false, 200).Settlement
	if r.State != services.InvestmentSettlementRecorded || r.Version != 1 || r.CreatedBy != "admin-user" || r.WorkspaceID != "workspace-rbac" || r.WithdrawalID != f.withdrawal || r.PrincipalCents == nil || *r.PrincipalCents != 500000 || r.RealizedResultCents == nil || *r.RealizedResultCents != 25500 || r.Input.NetCents != 525500 || *r.Input.GrossCents != 530000 || *r.Input.IRCents != 4500 || *r.Input.IOFCents != 0 || *r.Input.FeesCents != 0 || r.EffectsState != "NOT_ACTIVATED" {
		t.Fatalf("required result: %+v", r)
	}
	proof = f.proof(t)
	s = f.read(t, 200)
	if s.State != r.State || s.Version != 1 || s.Settlement.ID != r.ID || s.ApplicationID != "" || s.ClassificationID != "" || s.ReceiptConfirmationKind != "" || len(s.Pending) != 1 || s.Pending[0] != "RECEIPT_CONFIRMATION_PENDING" {
		t.Fatalf("read: %+v", s)
	}
	if proof != f.proof(t) || before != f.stable(t) {
		t.Fatal("C1/read changed financial or legacy rows")
	}
}

func TestInvestimentosO1B1Guards(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before, proof := f.stable(t), f.proof(t)
	for _, op := range []struct{ method, suffix string }{{"GET", ""}, {"POST", ""}, {"POST", "/conciliar"}} {
		t.Run(op.method+op.suffix, func(t *testing.T) {
			form := f.form(0)
			if op.suffix != "" {
				o1b1Confirm(form)
			}
			path := f.path() + op.suffix
			requireInvestimentosStatus(t, f.h.request(t, op.method, path, "", form, true, false), 401)
			requireInvestimentosStatus(t, f.h.request(t, op.method, path, "invalid-session", form, true, false), 401)
			for _, user := range []string{"manager-user", "regular-user"} {
				requireInvestimentosStatus(t, f.h.request(t, op.method, path, f.h.session(t, user, "workspace-rbac"), form, true, false), 403)
			}
			if op.method == "POST" {
				requireInvestimentosStatus(t, f.h.request(t, op.method, path, f.h.admin, form, false, false), 403)
				req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.h.admin})
				addAdminUsersRBACCSRF(t, req, f.h.signer)
				req.Header.Set("X-CSRF-Token", "invalid")
				rr := httptest.NewRecorder()
				f.h.mux.ServeHTTP(rr, req)
				requireInvestimentosStatus(t, rr, 403)
			}
			requireInvestimentosStatus(t, f.h.request(t, "DELETE", path, f.h.admin, form, true, false), 405)
			requireInvestimentosStatus(t, f.h.request(t, op.method, path+"/extra", f.h.admin, form, true, false), 404)
		})
	}
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("guards wrote business data")
	}
}

func TestInvestimentosO1B1FeatureOffAndIndependentOfO1A(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before, proof := f.stable(t), f.proof(t)
	for _, flag := range []string{"", "false", "0", "garbage"} {
		t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", flag)
		for _, session := range []string{"", f.h.admin} {
			for _, op := range []struct{ method, suffix string }{{"GET", ""}, {"POST", ""}, {"POST", "/conciliar"}} {
				requireInvestimentosStatus(t, f.h.request(t, op.method, f.path()+op.suffix, session, f.form(0), false, false), 404)
			}
		}
	}
	// Disabling only O1B1 leaves the O1A read operation available.
	requireInvestimentosStatus(t, f.h.request(t, "GET", f.o1aFixture.path("estado"), f.h.admin, nil, false, false), 200)
	for _, parent := range []string{"CONTABASE_FEATURE_INVESTMENTS", "CONTABASE_FEATURE_INVESTMENT_OPERATIONS"} {
		t.Run(parent, func(t *testing.T) {
			t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "true")
			t.Setenv(parent, "false")
			requireInvestimentosStatus(t, f.h.request(t, "GET", f.path(), f.h.admin, nil, false, false), 404)
		})
	}
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("feature checks changed data")
	}
}

func TestInvestimentosO1B1TenantIsolation(t *testing.T) {
	f := newO1B1(t, "5000,00")
	foreign := f.h.session(t, "admin-user", "workspace-b")
	for _, op := range []struct{ method, suffix string }{{"GET", ""}, {"POST", ""}, {"POST", "/conciliar"}} {
		form := f.form(0)
		if op.suffix != "" {
			o1b1Confirm(form)
		}
		requireInvestimentosStatus(t, f.h.request(t, op.method, f.path()+op.suffix, foreign, form, true, false), 404)
	}
	// A real foreign investment and I2.2 withdrawal, using the foreign account.
	create := investimentosRouteForm()
	create.Set("account_id", "inv-b")
	create.Set("name", "Foreign O1B1")
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos", foreign, create, true, false), 303)
	var investment, withdrawal string
	if err := f.h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-b' AND name='Foreign O1B1'`).Scan(&investment); err != nil {
		t.Fatal(err)
	}
	wform := investmentI22Form("initial:" + investment)
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos/"+investment+"/retiradas", foreign, wform, true, false), 303)
	if err := f.h.db.QueryRow(`SELECT id FROM investment_withdrawals WHERE workspace_id='workspace-b' AND investment_id=?`, investment).Scan(&withdrawal); err != nil {
		t.Fatal(err)
	}
	f.receipt(t, "foreign-receipt", "workspace-b", "inv-b", "", "INCOME", "paid", 525500)
	foreignBefore := o1aWorkspaceSnapshot(t, f.h, "workspace-b", "accounts", "transactions", "investment_withdrawals", "investment_settlement_revisions", "investment_settlement_receipts")
	before, proof := f.stable(t), f.proof(t)
	for _, path := range []string{strings.Replace(f.path(), f.id, investment, 1), strings.Replace(f.path(), f.withdrawal, withdrawal, 1)} {
		for _, method := range []string{"GET", "POST"} {
			requireInvestimentosStatus(t, f.h.request(t, method, path, f.h.admin, f.form(0), true, false), 404)
		}
	}
	for _, field := range []string{"account_id", "destination_account_id"} {
		form := f.form(0)
		form.Set(field, "inv-b")
		f.post(t, form, false, 404)
	}
	form := f.form(0)
	o1b1Confirm(form)
	o1b1Claims(form, services.InvestmentSettlementReceipt{TransactionID: "foreign-receipt", AmountCents: 525500, Evidence: "Explicit foreign ID"})
	f.post(t, form, true, 404)
	if before != f.stable(t) || proof != f.proof(t) || foreignBefore != o1aWorkspaceSnapshot(t, f.h, "workspace-b", "accounts", "transactions", "investment_withdrawals", "investment_settlement_revisions", "investment_settlement_receipts") {
		t.Fatal("tenant rejection changed data")
	}
}

func TestInvestimentosO1B1UnknownZeroAndPendingRevisions(t *testing.T) {
	for _, field := range []string{"gross_cents", "ir_cents", "iof_cents", "fees_cents", "patrimony_cents"} {
		t.Run(field, func(t *testing.T) {
			f := newO1B1(t, "")
			before := f.stable(t)
			form := f.form(0)
			form.Del(field)
			r := f.post(t, form, false, 200).Settlement
			if r.State != services.InvestmentSettlementPending || r.PrincipalCents != nil || r.RealizedResultCents != nil {
				t.Fatalf("unknown became known: %+v", r)
			}
			s := f.read(t, 200)
			if s.PrincipalCents != nil || s.State != "PENDENTE" || len(s.Pending) != 3 {
				t.Fatalf("pending: %+v", s)
			}
			proof := f.proof(t)
			missing := cloneO1B1Form(form)
			o1b1Confirm(missing)
			missing.Set("request_id", uuid.NewString())
			missing.Set("expected_version", "1")
			f.post(t, missing, true, 409)
			if proof != f.proof(t) {
				t.Fatal("incomplete confirmation wrote proof")
			}
			next := f.post(t, f.form(1), false, 200).Settlement
			if next.State != "REGISTRADA" || next.Version != 2 || next.PreviousID != r.ID || next.PrincipalCents != nil || *next.Input.IOFCents != 0 || *next.Input.FeesCents != 0 {
				t.Fatalf("revision: %+v", next)
			}
			if before != f.stable(t) {
				t.Fatal("revision changed finances")
			}
		})
	}
}

func cloneO1B1Form(form url.Values) url.Values {
	copy := url.Values{}
	for k, v := range form {
		copy[k] = append([]string(nil), v...)
	}
	return copy
}

func TestInvestimentosO1B1ConfirmedZeroPrincipalAndCosts(t *testing.T) {
	f := newO1B1(t, "0,00")
	before := f.stable(t)
	form := f.form(0)
	form.Set("gross_cents", "530300")
	form.Set("iof_cents", "200")
	form.Set("fees_cents", "100")
	r := f.post(t, form, false, 200).Settlement
	if r.PrincipalCents == nil || *r.PrincipalCents != 0 || r.RealizedResultCents == nil || *r.RealizedResultCents != 525500 || *r.Input.IRCents != 4500 || *r.Input.IOFCents != 200 || *r.Input.FeesCents != 100 {
		t.Fatalf("zero/costs: %+v", r)
	}
	if before != f.stable(t) {
		t.Fatal("costs created expenses or financial effects")
	}
}

func TestInvestimentosO1B1SameAccountConfirmationAndSeal(t *testing.T) {
	f := newO1B1(t, "5000,00")
	first := f.post(t, f.form(0), false, 200).Settlement
	before := f.stable(t)
	form := f.form(1)
	o1b1Confirm(form)
	confirmed := f.post(t, form, true, 200)
	if confirmed.Settlement.State != "CONCILIADA" || confirmed.Settlement.Version != 2 || confirmed.Settlement.PreviousID != first.ID || confirmed.ReceiptConfirmationKind != services.InvestmentReceiptUserDeclaration || confirmed.Settlement.EffectsState != "NOT_ACTIVATED" {
		t.Fatalf("confirmation: %+v", confirmed)
	}
	proof := f.proof(t)
	s := f.read(t, 200)
	if s.ReceiptConfirmationKind != services.InvestmentReceiptUserDeclaration || s.ApplicationID != "" || s.ClassificationID != "" || len(s.Pending) != 0 {
		t.Fatalf("C1 confused with C2: %+v", s)
	}
	f.post(t, f.form(2), false, 409)
	if replay := f.post(t, form, true, 200); replay.Settlement.ID != confirmed.Settlement.ID {
		t.Fatal("confirmation replay changed revision")
	}
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("confirmed/replayed read moved finances or proof")
	}
}

func TestInvestimentosO1B1ExplicitExistingReceiptAndDrift(t *testing.T) {
	for _, typ := range []string{"TRANSFER", "INCOME"} {
		t.Run(typ, func(t *testing.T) {
			f := newO1B1(t, "5000,00")
			source, destination := "inv-a", "checking-a"
			if typ == "INCOME" {
				source, destination = "checking-a", ""
			}
			f.receipt(t, "receipt", "workspace-rbac", source, destination, typ, "paid", 525500)
			before := f.stable(t)
			form := f.form(0)
			form.Set("destination_account_id", "checking-a")
			o1b1Confirm(form)
			o1b1Claims(form, services.InvestmentSettlementReceipt{TransactionID: "receipt", AmountCents: 525500, Evidence: "Identificador do extrato confirmado"})
			r := f.post(t, form, true, 200)
			if r.Settlement.State != "CONCILIADA" || r.ReceiptConfirmationKind != services.InvestmentReceiptUserDeclaration {
				t.Fatal(r)
			}
			proof := f.proof(t)
			f.read(t, 200)
			if before != f.stable(t) || proof != f.proof(t) {
				t.Fatal("confirmation created income/transfer or mutated financial rows")
			}
			f.h.exec(t, `UPDATE transactions SET amount=525501 WHERE workspace_id='workspace-rbac' AND id='receipt'`)
			before = f.stable(t)
			f.read(t, 409)
			if before != f.stable(t) || proof != f.proof(t) {
				t.Fatal("drift read wrote data")
			}
		})
	}
}

func TestInvestimentosO1B1ReceiptValidationCapacityAndNoGuessing(t *testing.T) {
	f := newO1B1(t, "5000,00")
	f.h.exec(t, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('checking-other','workspace-rbac','Other checking','CHECKING')`)
	f.receipt(t, "valid", "workspace-rbac", "inv-a", "checking-a", "TRANSFER", "paid", 525500)
	f.receipt(t, "short", "workspace-rbac", "inv-a", "checking-a", "TRANSFER", "paid", 100)
	f.receipt(t, "unpaid", "workspace-rbac", "inv-a", "checking-a", "TRANSFER", "pending", 525500)
	f.receipt(t, "wrong-source", "workspace-rbac", "checking-other", "checking-a", "TRANSFER", "paid", 525500)
	f.receipt(t, "wrong-destination", "workspace-rbac", "inv-a", "checking-other", "TRANSFER", "paid", 525500)
	f.receipt(t, "old", "workspace-rbac", "inv-a", "checking-a", "TRANSFER", "paid", 525500)
	at := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC).Unix()
	f.h.exec(t, `UPDATE transactions SET date=?,paid_at=? WHERE workspace_id='workspace-rbac' AND id='old'`, at, at)
	before, proof := f.stable(t), f.proof(t)
	for _, tc := range []struct {
		id     string
		amount int64
		status int
	}{
		{"", 525500, 400}, {"missing", 525500, 404}, {"short", 525500, 409}, {"unpaid", 525500, 400},
		{"wrong-source", 525500, 400}, {"wrong-destination", 525500, 400}, {"old", 525500, 400}, {"valid", 100, 409},
	} {
		t.Run(tc.id+fmt.Sprint(tc.amount), func(t *testing.T) {
			form := f.form(0)
			form.Set("destination_account_id", "checking-a")
			o1b1Confirm(form)
			if tc.id != "" {
				o1b1Claims(form, services.InvestmentSettlementReceipt{TransactionID: tc.id, AmountCents: tc.amount, Evidence: "Explicit receipt"})
			}
			want := tc.status
			if tc.id == "" {
				want = 409
			} // no date/value matching
			f.post(t, form, true, want)
		})
	}
	form := f.form(0)
	form.Set("destination_account_id", "checking-a")
	o1b1Confirm(form)
	r := services.InvestmentSettlementReceipt{TransactionID: "valid", AmountCents: 100, Evidence: "Explicit receipt"}
	o1b1Claims(form, r, r)
	f.post(t, form, true, 400)
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("invalid receipts created links or effects")
	}
	// Reserving a receipt in one withdrawal prevents over-allocation to another.
	form = f.form(0)
	form.Set("destination_account_id", "checking-a")
	o1b1Claims(form, services.InvestmentSettlementReceipt{TransactionID: "valid", AmountCents: 525500, Evidence: "Reserved receipt"})
	f.post(t, form, false, 200)
	other := f.h.create(t, "Other withdrawal")
	wform := investmentI22Form("initial:" + other)
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos/"+other+"/retiradas", f.h.admin, wform, true, false), 303)
	var otherWithdrawal string
	if err := f.h.db.QueryRow(`SELECT id FROM investment_withdrawals WHERE workspace_id='workspace-rbac' AND investment_id=?`, other).Scan(&otherWithdrawal); err != nil {
		t.Fatal(err)
	}
	form = f.form(0)
	form.Set("gross_cents", "10000")
	form.Set("ir_cents", "0")
	form.Set("net_cents", "10000")
	form.Set("patrimony_cents", "10000")
	form.Set("destination_account_id", "checking-a")
	o1b1Confirm(form)
	o1b1Claims(form, services.InvestmentSettlementReceipt{TransactionID: "valid", AmountCents: 10000, Evidence: "Duplicate capacity"})
	proof = f.proof(t)
	before = f.stable(t)
	path := "/investimentos/" + other + "/financeiro/retiradas/" + otherWithdrawal + "/liquidacao/conciliar"
	requireInvestimentosStatus(t, f.h.request(t, "POST", path, f.h.admin, form, true, false), 409)
	if proof != f.proof(t) || before != f.stable(t) {
		t.Fatal("capacity conflict leaked writes")
	}
}

func TestInvestimentosO1B1InputValidation(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before, proof := f.stable(t), f.proof(t)
	for _, tc := range []struct{ field, value string }{
		{"request_id", ""}, {"expected_version", ""}, {"expected_version", "-1"}, {"net_cents", ""}, {"net_cents", "525499"},
		{"economic_date", "2026-02-31"}, {"economic_date", "2026-03-14"}, {"reason", ""}, {"gross_cents", "5300.00"},
		{"gross_cents", "-1"}, {"ir_cents", "9223372036854775808"}, {"iof_cents", "-1"}, {"patrimony_evidence", ""},
		{"receipts", `[{"Unknown":1}]`}, {"receipts", `[] []`}, {"confirm_receipt", "true"},
	} {
		t.Run(tc.field+tc.value, func(t *testing.T) { form := f.form(0); form.Set(tc.field, tc.value); f.post(t, form, false, 400) })
	}
	form := f.form(0)
	form.Add("net_cents", "525500")
	f.post(t, form, false, 400)
	form = f.form(0)
	f.post(t, form, true, 400)
	form = f.form(0)
	o1b1Confirm(form)
	form.Del("receipt_evidence")
	f.post(t, form, true, 409)
	form = f.form(0)
	form.Set("reason", strings.Repeat("x", 65<<10))
	f.post(t, form, false, 400)
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("invalid input mutated data")
	}
}

func TestInvestimentosO1B1IdempotencyAndStaleVersion(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before := f.stable(t)
	form := f.form(0)
	first := f.post(t, form, false, 200).Settlement
	proof := f.proof(t)
	if replay := f.post(t, form, false, 200).Settlement; replay.ID != first.ID || replay.Version != 1 {
		t.Fatal("idempotency lost")
	}
	bad := cloneO1B1Form(form)
	bad.Set("reason", "Divergent payload")
	f.post(t, bad, false, 409)
	f.post(t, f.form(0), false, 409)
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("replay/conflict mutated data")
	}
	second := f.post(t, f.form(1), false, 200).Settlement
	proof = f.proof(t)
	if replay := f.post(t, form, false, 200).Settlement; replay.ID != first.ID || replay.Version != 1 || second.Version != 2 {
		t.Fatal("historical replay returned latest revision")
	}
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("historical replay mutated data")
	}
}

func TestInvestimentosO1B1ConcurrentRequests(t *testing.T) {
	f := newO1B1(t, "5000,00")
	before := f.stable(t)
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			form := f.form(0)
			if !sameKey {
				form.Set("expected_version", "1")
			}
			const n = 6
			responses := make([]*httptest.ResponseRecorder, n)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				input := cloneO1B1Form(form)
				if !sameKey {
					input.Set("request_id", uuid.NewString())
				}
				wg.Add(1)
				go func(i int, input url.Values) {
					defer wg.Done()
					<-start
					responses[i] = f.h.request(t, "POST", f.path(), f.h.admin, input, true, false)
				}(i, input)
			}
			close(start)
			wg.Wait()
			created, id := 0, ""
			for _, rr := range responses {
				if rr.Code == 200 {
					created++
					var result o1b1Result
					if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if id != "" && id != result.Settlement.ID {
						t.Fatal("concurrency created multiple effective revisions")
					}
					id = result.Settlement.ID
				} else if sameKey || rr.Code != 409 {
					t.Fatalf("unexpected concurrency result: %d %s", rr.Code, rr.Body.String())
				}
			}
			if (sameKey && created != n) || (!sameKey && created != 1) {
				t.Fatalf("successful responses: %d", created)
			}
			want := 1
			if !sameKey {
				want = 2
			}
			var count int
			if err := f.h.db.QueryRow(`SELECT COUNT(*) FROM investment_settlement_revisions WHERE workspace_id='workspace-rbac' AND investment_id=? AND withdrawal_id=?`, f.id, f.withdrawal).Scan(&count); err != nil || count != want {
				t.Fatalf("effective revisions: %d %v", count, err)
			}
		})
	}
	if before != f.stable(t) {
		t.Fatal("concurrency moved finances")
	}
}

func TestInvestimentosO1B1AtomicRollback(t *testing.T) {
	f := newO1B1(t, "5000,00")
	f.receipt(t, "a", "workspace-rbac", "inv-a", "checking-a", "TRANSFER", "paid", 200000)
	f.receipt(t, "b", "workspace-rbac", "inv-a", "checking-a", "TRANSFER", "paid", 325500)
	f.h.exec(t, `CREATE TRIGGER o1b1_injected BEFORE INSERT ON investment_settlement_receipts WHEN NEW.workspace_id='workspace-rbac' AND NEW.transaction_id='b' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	before, proof := f.stable(t), f.proof(t)
	form := f.form(0)
	form.Set("destination_account_id", "checking-a")
	o1b1Confirm(form)
	o1b1Claims(form, services.InvestmentSettlementReceipt{TransactionID: "a", AmountCents: 200000, Evidence: "First part"}, services.InvestmentSettlementReceipt{TransactionID: "b", AmountCents: 325500, Evidence: "Second part"})
	f.post(t, form, true, 500)
	if before != f.stable(t) || proof != f.proof(t) {
		t.Fatal("partial revision or receipt survived rollback")
	}
	f.h.exec(t, `DROP TRIGGER o1b1_injected`)
	if r := f.post(t, form, true, 200); r.Settlement.State != "CONCILIADA" {
		t.Fatal(r)
	}
	if before != f.stable(t) {
		t.Fatal("retry created financial effects")
	}
}
