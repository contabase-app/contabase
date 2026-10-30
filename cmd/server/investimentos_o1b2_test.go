package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

const o1b2Flag = "CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS"

type o1b2Scenario struct {
	net                  int64
	principal, receipt   string
	partial, unconfirmed bool
}

type o1b2Fixture struct {
	*o1b1Fixture
	settlement services.InvestmentSettlement
	applyForm  url.Values
}

func newO1B2(t *testing.T, scenario o1b2Scenario) *o1b2Fixture {
	t.Helper()
	a := newO1A(t)
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "true")
	t.Setenv(o1b2Flag, "true")
	a.activate(t)
	a.valuation(t, "o1b2-value", "2026-03-01", 530000)
	b := a.integrate(t, a.form(t, "o1b2-value", 1), 200)
	f := &o1b2Fixture{o1b1Fixture: &o1b1Fixture{o1aFixture: a}}
	wform := investmentI22Form("initial:" + f.id)
	wform.Set("received_amount", fmt.Sprintf("%d,%02d", scenario.net/100, scenario.net%100))
	if !scenario.partial {
		wform.Set("mode", "TOTAL")
	}
	if scenario.principal != "" {
		wform.Set("principal_initial:"+f.id, scenario.principal)
	}
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos/"+f.id+"/retiradas", f.h.admin, wform, true, false), 303)
	if err := f.h.db.QueryRow(`SELECT id FROM investment_withdrawals WHERE workspace_id='workspace-rbac' AND investment_id=? AND request_id=?`, f.id, wform.Get("request_id")).Scan(&f.withdrawal); err != nil {
		t.Fatal(err)
	}
	f.h.exec(t, `INSERT INTO categories(id,workspace_id,name,type) VALUES('o1b2-gain','workspace-rbac','Lucro realizado','INCOME'),('o1b2-loss','workspace-rbac','Perda realizada','EXPENSE'),('o1b2-foreign','workspace-b','Foreign','INCOME')`)
	c1 := f.form(0)
	patrimony := int64(530000)
	if scenario.partial {
		patrimony = 106000
	}
	c1.Set("gross_cents", strconv.FormatInt(patrimony, 10))
	c1.Set("patrimony_cents", strconv.FormatInt(patrimony, 10))
	c1.Set("net_cents", strconv.FormatInt(scenario.net, 10))
	c1.Set("ir_cents", strconv.FormatInt(patrimony-scenario.net, 10))
	if scenario.receipt != "" {
		source, dest := "inv-a", "checking-a"
		if scenario.receipt == "INCOME" {
			source, dest = "checking-a", ""
		}
		f.receipt(t, "o1b2-receipt", "workspace-rbac", source, dest, scenario.receipt, "paid", scenario.net)
		tx, err := f.h.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = services.ApplyBalanceEffect(tx, "workspace-rbac", scenario.receipt, "INVESTMENT", "paid", scenario.net, source, dest, time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC).Unix()); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		c1.Set("destination_account_id", "checking-a")
		o1b1Claims(c1, services.InvestmentSettlementReceipt{TransactionID: "o1b2-receipt", AmountCents: scenario.net, Evidence: "Comprovante identificado"})
	}
	if !scenario.unconfirmed {
		o1b1Confirm(c1)
	}
	f.settlement = f.post(t, c1, !scenario.unconfirmed, 200).Settlement
	s := f.read(t, 200)
	if s.ApplicationID != "" || s.ClassificationID != "" || s.Application != nil || s.Classification != nil {
		t.Fatal("legacy O1A/I2.2/C1 handlers activated C2")
	}
	f.applyForm = url.Values{"request_id": {uuid.NewString()}, "settlement_revision_id": {f.settlement.ID},
		"reference_revision_id": {f.root}, "financial_revision_id": {b.ID}, "expected_settlement_version": {strconv.FormatInt(f.settlement.Version, 10)},
		"expected_financial_version": {strconv.FormatInt(b.Version, 10)}, "expected_balance_version": {strconv.FormatInt(s.Account.BalanceVersion, 10)},
		"expected_destination_version": {strconv.FormatInt(s.DestinationAccount.BalanceVersion, 10)}, "reason": {"Liquidação comprovada"},
		"reconciliation_evidence": {"Extratos e efeitos equivalentes conferidos; somente os vínculos informados"}}
	if scenario.partial {
		f.valuation(t, "o1b2-remaining", "2026-03-15", 424000)
		c, err := services.ReadInvestmentReferenceCandidate(context.Background(), f.h.db, "workspace-rbac", f.id, "o1b2-remaining")
		if err != nil {
			t.Fatal(err)
		}
		f.applyForm.Set("remaining_position_id", "o1b2-remaining")
		f.applyForm.Set("remaining_coverage_token", c.CoverageToken)
		f.applyForm.Set("day_closed", "true")
	}
	return f
}

func (f *o1b2Fixture) classificationForm(application string) url.Values {
	category := "o1b2-gain"
	if f.settlement.RealizedResultCents == nil || *f.settlement.RealizedResultCents == 0 {
		category = ""
	} else if *f.settlement.RealizedResultCents < 0 {
		category = "o1b2-loss"
	}
	return url.Values{"request_id": {uuid.NewString()}, "settlement_revision_id": {f.settlement.ID}, "application_id": {application},
		"expected_settlement_version": {strconv.FormatInt(f.settlement.Version, 10)}, "expected_version": {"0"}, "category_id": {category},
		"legacy_reconciliation_evidence": {"Extratos e lançamentos conferidos; sem reconhecimento gerencial anterior do resultado, IR, IOF ou taxas"}}
}

func (f *o1b2Fixture) effect(t *testing.T, operation string, form url.Values, want int, dest any) *httptest.ResponseRecorder {
	t.Helper()
	rr := f.h.request(t, "POST", f.path()+"/"+operation, f.h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, want)
	if want == 200 {
		if err := json.Unmarshal(rr.Body.Bytes(), dest); err != nil {
			t.Fatal(err)
		}
		if rr.Header().Get("Idempotency-Key") != form.Get("request_id") || rr.Header().Get("Link") != "<"+f.path()+">; rel=\"status\"" || rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing recovery headers", rr.Header())
		}
		if strings.Contains(strings.ToLower(rr.Body.String()), `"outcome"`) {
			t.Fatal("invented execution outcome")
		}
	}
	return rr
}

func (f *o1b2Fixture) apply(t *testing.T) services.InvestmentSettlementApplication {
	t.Helper()
	var a services.InvestmentSettlementApplication
	f.effect(t, "aplicar", f.applyForm, 200, &a)
	return a
}

func (f *o1b2Fixture) financial(t *testing.T) string {
	t.Helper()
	return o1aSnapshot(t, f.h, "accounts", "financial_transactions", "account_balance_adjustments", "box_virtual_ledger",
		"investments", "investment_valuations", "investment_purchases", "investment_withdrawals", "investment_withdrawal_allocations",
		"investment_reference_revisions", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding",
		"investment_settlement_revisions", "investment_settlement_receipts", "investment_settlement_applications", "investment_settlement_prior_effects")
}

func TestInvestimentosO1B2FinancialAndClassificationScenarios(t *testing.T) {
	for _, tc := range []struct {
		name             string
		scenario         o1b2Scenario
		residual, result int64
		nature           string
	}{
		{"required 255", o1b2Scenario{net: 525500, principal: "5000,00"}, -4500, 25500, "REALIZED_GAIN"},
		{"existing transfer", o1b2Scenario{net: 525500, principal: "5000,00", receipt: "TRANSFER"}, -4500, 25500, "REALIZED_GAIN"},
		{"partial 51", o1b2Scenario{net: 105100, principal: "1000,00", partial: true}, -900, 5100, "REALIZED_GAIN"},
		{"loss 100", o1b2Scenario{net: 490000, principal: "5000,00"}, -40000, -10000, "REALIZED_LOSS"},
		{"zero", o1b2Scenario{net: 500000, principal: "5000,00"}, -30000, 0, "ZERO"},
		{"unknown principal", o1b2Scenario{net: 525500}, -4500, 0, "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newO1B2(t, tc.scenario)
			stable := o1aSnapshot(t, f.h, "transactions", "box_virtual_ledger", "investments", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_financial_revisions", "investment_settlement_revisions", "investment_settlement_receipts")
			before := f.stable(t)
			state := f.read(t, 200)
			if before != f.stable(t) || state.State != services.InvestmentSettlementReconciled {
				t.Fatal("read mutated financial state")
			}
			a := f.apply(t)
			wantBalance := state.Account.BalanceCents + tc.residual
			if a.ResidualCents != tc.residual || a.BalanceCents != wantBalance || a.State != services.InvestmentSettlementFinancialApplied || a.ID == "" {
				t.Fatalf("financial result: %+v", a)
			}
			if stable != o1aSnapshot(t, f.h, "transactions", "box_virtual_ledger", "investments", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_financial_revisions", "investment_settlement_revisions", "investment_settlement_receipts") {
				t.Fatal("C2A modified legacy facts or created income/transfer")
			}
			state = f.read(t, 200)
			if state.Application == nil || !reflect.DeepEqual(*state.Application, a) || state.Classification != nil || state.Account.BalanceCents != wantBalance {
				t.Fatalf("financial pending state: %+v", state)
			}
			if tc.scenario.receipt == "TRANSFER" && (state.Account.BalanceCents != 0 || state.DestinationAccount.BalanceCents != 525500) {
				t.Fatalf("transfer applied twice: %+v", state)
			}
			before = f.stable(t)
			if replay := f.apply(t); !reflect.DeepEqual(a, replay) || before != f.stable(t) {
				t.Fatal("C2A replay duplicated effects")
			}
			form := f.classificationForm(a.ID)
			financial := f.financial(t)
			var c services.InvestmentResultClassification
			f.effect(t, "classificar", form, 200, &c)
			if c.ID == "" || c.Nature != tc.nature || c.Version != 1 || financial != f.financial(t) {
				t.Fatalf("classification changed cash or returned wrong nature: %+v", c)
			}
			if tc.nature == "UNKNOWN" {
				if c.ResultCents != nil || c.State != services.InvestmentClassificationUnavailable {
					t.Fatal("unknown principal became zero", c)
				}
			} else if c.ResultCents == nil || *c.ResultCents != tc.result || c.State != services.InvestmentClassificationCompleted {
				t.Fatal("incorrect realized result", c)
			}
			if tc.result == 0 {
				if c.TransactionID != "" {
					t.Fatal("fictitious monetary transaction", c)
				}
			} else {
				var typ, mode string
				var amount int64
				if err := f.h.db.QueryRow(`SELECT type,effect_mode,amount FROM transactions WHERE workspace_id='workspace-rbac' AND id=?`, c.TransactionID).Scan(&typ, &mode, &amount); err != nil {
					t.Fatal(err)
				}
				want, kind := tc.result, "INCOME"
				if want < 0 {
					want, kind = -want, "EXPENSE"
				}
				if amount != want || typ != kind || mode != "CLASSIFICATION_ONLY" {
					t.Fatal("incorrect managerial row", typ, mode, amount)
				}
			}
			before = f.stable(t)
			var replay services.InvestmentResultClassification
			f.effect(t, "classificar", form, 200, &replay)
			state = f.read(t, 200)
			if !reflect.DeepEqual(c, replay) || state.Classification == nil || !reflect.DeepEqual(c, *state.Classification) || state.ClassificationID != c.ID || before != f.stable(t) {
				t.Fatal("classification replay/read mutated durable state")
			}
			// Session authority, including audit author, is persisted by the writers.
			for _, table := range []string{"investment_settlement_applications", "investment_result_classifications"} {
				var author, workspace string
				if err := f.h.db.QueryRow(`SELECT created_by,workspace_id FROM `+table+` WHERE workspace_id='workspace-rbac' AND investment_id=?`, f.id).Scan(&author, &workspace); err != nil || author != "admin-user" || workspace != "workspace-rbac" {
					t.Fatal("authority escaped session", author, workspace, err)
				}
			}
		})
	}
}

func TestInvestimentosO1B2Guards(t *testing.T) {
	f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
	a := f.apply(t)
	before := f.stable(t)
	for _, operation := range []string{"aplicar", "classificar"} {
		t.Run(operation, func(t *testing.T) {
			form := f.applyForm
			if operation == "classificar" {
				form = f.classificationForm(a.ID)
			}
			path := f.path() + "/" + operation
			for _, session := range []string{"", "invalid-session"} {
				requireInvestimentosStatus(t, f.h.request(t, "POST", path, session, form, true, false), 401)
			}
			for _, user := range []string{"manager-user", "regular-user"} {
				requireInvestimentosStatus(t, f.h.request(t, "POST", path, f.h.session(t, user, "workspace-rbac"), form, true, false), 403)
			}
			requireInvestimentosStatus(t, f.h.request(t, "POST", path, f.h.admin, form, false, false), 403)
			req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.h.admin})
			addAdminUsersRBACCSRF(t, req, f.h.signer)
			req.Header.Set("X-CSRF-Token", "invalid")
			rr := httptest.NewRecorder()
			f.h.mux.ServeHTTP(rr, req)
			requireInvestimentosStatus(t, rr, 403)
			for _, method := range []string{"GET", "PUT", "DELETE"} {
				requireInvestimentosStatus(t, f.h.request(t, method, path, f.h.admin, form, true, false), 405)
			}
			requireInvestimentosStatus(t, f.h.request(t, "POST", path+"/extra", f.h.admin, form, true, false), 404)
		})
	}
	if before != f.stable(t) {
		t.Fatal("guards mutated financial state")
	}
}

func TestInvestimentosO1B2FeatureOffDependenciesAndNoActivation(t *testing.T) {
	f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
	before := f.stable(t)
	if err := os.Unsetenv(o1b2Flag); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "false", "0", "yes", "garbage"} {
		t.Setenv(o1b2Flag, value)
		for _, operation := range []string{"aplicar", "classificar"} {
			for _, session := range []string{"", f.h.admin} {
				requireInvestimentosStatus(t, f.h.request(t, "POST", f.path()+"/"+operation, session, f.applyForm, false, false), 404)
			}
		}
	}
	// O1B1 and O1A remain available independently of the new opt-in.
	f.read(t, 200)
	requireInvestimentosStatus(t, f.h.request(t, "GET", f.o1aFixture.path("estado"), f.h.admin, nil, false, false), 200)
	for _, parent := range []string{"CONTABASE_FEATURE_INVESTMENTS", "CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS"} {
		t.Run(parent, func(t *testing.T) {
			t.Setenv(o1b2Flag, "true")
			t.Setenv(parent, "false")
			for _, operation := range []string{"aplicar", "classificar"} {
				requireInvestimentosStatus(t, f.h.request(t, "POST", f.path()+"/"+operation, f.h.admin, f.applyForm, true, false), 404)
			}
		})
	}
	t.Setenv(o1b2Flag, "true")
	f.read(t, 200)
	if before != f.stable(t) {
		t.Fatal("flags or read activated C2 effects")
	}
}

func TestInvestimentosO1B2TenantIsolation(t *testing.T) {
	f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
	foreign := f.h.session(t, "admin-user", "workspace-b")
	create := investimentosRouteForm()
	create.Set("account_id", "inv-b")
	create.Set("name", "Foreign O1B2")
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos", foreign, create, true, false), 303)
	var investment, withdrawal string
	if err := f.h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-b' AND name='Foreign O1B2'`).Scan(&investment); err != nil {
		t.Fatal(err)
	}
	wform := investmentI22Form("initial:" + investment)
	requireInvestimentosStatus(t, f.h.request(t, "POST", "/investimentos/"+investment+"/retiradas", foreign, wform, true, false), 303)
	if err := f.h.db.QueryRow(`SELECT id FROM investment_withdrawals WHERE workspace_id='workspace-b' AND investment_id=?`, investment).Scan(&withdrawal); err != nil {
		t.Fatal(err)
	}
	f.receipt(t, "foreign-receipt", "workspace-b", "inv-b", "", "INCOME", "paid", 1000)
	before, foreignBefore := f.stable(t), o1aWorkspaceSnapshot(t, f.h, "workspace-b", "accounts", "transactions", "investment_withdrawals", "investment_settlement_applications", "investment_result_classifications")
	for _, operation := range []string{"aplicar", "classificar"} {
		form := f.applyForm
		if operation == "classificar" {
			form = f.classificationForm(uuid.NewString())
		}
		requireInvestimentosStatus(t, f.h.request(t, "POST", f.path()+"/"+operation, foreign, form, true, false), 404)
		for _, path := range []string{strings.Replace(f.path(), f.id, investment, 1), strings.Replace(f.path(), f.withdrawal, withdrawal, 1)} {
			requireInvestimentosStatus(t, f.h.request(t, "POST", path+"/"+operation, f.h.admin, form, true, false), 404)
		}
	}
	bad := cloneO1B1Form(f.applyForm)
	bad.Set("prior_effects", `[{"TransactionID":"foreign-receipt","DeltaCents":-1000,"Evidence":"foreign"}]`)
	f.effect(t, "aplicar", bad, 404, nil)
	if before != f.stable(t) {
		t.Fatal("foreign mutation touched current workspace")
	}
	a := f.apply(t)
	before = f.stable(t)
	bad = f.classificationForm(a.ID)
	bad.Set("category_id", "o1b2-foreign")
	f.effect(t, "classificar", bad, 404, nil)
	bad = f.classificationForm(a.ID)
	bad.Set("legacy_transaction_ids", `["foreign-receipt"]`)
	f.effect(t, "classificar", bad, 404, nil)
	if before != f.stable(t) || foreignBefore != o1aWorkspaceSnapshot(t, f.h, "workspace-b", "accounts", "transactions", "investment_withdrawals", "investment_settlement_applications", "investment_result_classifications") {
		t.Fatal("tenant rejection wrote data")
	}
}

func TestInvestimentosO1B2InputValidationAndNoClientAuthority(t *testing.T) {
	f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
	before := f.stable(t)
	for _, tc := range []struct{ field, value string }{
		{"request_id", ""}, {"expected_settlement_version", ""}, {"expected_financial_version", ""}, {"expected_balance_version", ""}, {"expected_destination_version", ""},
		{"expected_settlement_version", "-1"}, {"expected_balance_version", "-1"}, {"expected_financial_version", "9223372036854775808"}, {"reason", ""},
		{"reconciliation_evidence", ""}, {"day_closed", "yes"}, {"prior_effects", `[{"Unknown":1}]`}, {"prior_effects", `[] []`},
		{"reason", strings.Repeat("x", 65<<10)},
	} {
		t.Run("apply/"+tc.field+"/"+strconv.Itoa(len(tc.value)), func(t *testing.T) {
			bad := cloneO1B1Form(f.applyForm)
			bad.Set(tc.field, tc.value)
			f.effect(t, "aplicar", bad, 400, nil)
		})
	}
	for _, operation := range []string{"aplicar", "classificar"} {
		for _, field := range []string{"workspace_id", "created_by", "user_id", "permissions", "account_id", "residual_cents", "realized_result_cents", "state"} {
			bad := cloneO1B1Form(f.applyForm)
			if operation == "classificar" {
				bad = f.classificationForm(uuid.NewString())
			}
			bad.Set(field, "forged")
			f.effect(t, operation, bad, 400, nil)
		}
	}
	bad := cloneO1B1Form(f.applyForm)
	bad.Add("request_id", uuid.NewString())
	f.effect(t, "aplicar", bad, 400, nil)
	if before != f.stable(t) {
		t.Fatal("invalid input wrote data")
	}
	a := f.apply(t)
	before = f.stable(t)
	for _, tc := range []struct{ field, value string }{
		{"request_id", ""}, {"expected_settlement_version", ""}, {"expected_version", ""}, {"expected_version", "1"},
		{"legacy_reconciliation_evidence", ""}, {"category_id", "o1b2-loss"}, {"application_id", ""},
		{"legacy_transaction_ids", `[""]`}, {"legacy_transaction_ids", `[] []`},
	} {
		bad := f.classificationForm(a.ID)
		bad.Set(tc.field, tc.value)
		f.effect(t, "classificar", bad, 400, nil)
	}
	if before != f.stable(t) {
		t.Fatal("invalid classification wrote data")
	}
}

func TestInvestimentosO1B2NotReconciledAndLegacyIncomeBlocked(t *testing.T) {
	for _, tc := range []o1b2Scenario{{net: 525500, principal: "5000,00", unconfirmed: true}, {net: 525500, principal: "5000,00", receipt: "INCOME"}} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			f := newO1B2(t, tc)
			before := f.stable(t)
			f.effect(t, "aplicar", f.applyForm, 409, nil)
			f.effect(t, "classificar", f.classificationForm(uuid.NewString()), 409, nil)
			if before != f.stable(t) {
				t.Fatal("unsafe C1/legacy income rejection changed state")
			}
		})
	}
}

func TestInvestimentosO1B2StaleEvidenceAndFinancialConflicts(t *testing.T) {
	f := newO1B2(t, o1b2Scenario{net: 105100, principal: "1000,00", partial: true})
	before := f.stable(t)
	for _, tc := range []struct{ field, value string }{
		{"expected_balance_version", "999"}, {"expected_destination_version", "999"}, {"expected_settlement_version", "999"},
		{"expected_financial_version", "999"}, {"financial_revision_id", uuid.NewString()}, {"reference_revision_id", uuid.NewString()},
		{"settlement_revision_id", uuid.NewString()}, {"remaining_position_id", ""}, {"remaining_coverage_token", strings.Repeat("a", 64)}, {"day_closed", "false"},
	} {
		bad := cloneO1B1Form(f.applyForm)
		bad.Set(tc.field, tc.value)
		f.effect(t, "aplicar", bad, 409, nil)
	}
	if before != f.stable(t) {
		t.Fatal("conflict changed state")
	}
	// Existing HTTP valuation mutation invalidates B's coverage, which C2A must
	// revalidate inside its transaction rather than trusting the submitted IDs.
	f.h.exec(t, `UPDATE investment_valuations SET amount_cents=530001 WHERE workspace_id='workspace-rbac' AND id='o1b2-value'`)
	before = f.stable(t)
	f.effect(t, "aplicar", f.applyForm, 409, nil)
	if before != f.stable(t) {
		t.Fatal("stale patrimony proof changed state")
	}
}

func TestInvestimentosO1B2ReceiptDrift(t *testing.T) {
	for _, afterApply := range []bool{false, true} {
		t.Run(fmt.Sprint(afterApply), func(t *testing.T) {
			f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00", receipt: "TRANSFER"})
			if afterApply {
				a := f.apply(t)
				state := f.read(t, 200)
				before, proof := f.stable(t), f.proof(t)
				financial := f.financial(t)
				// Exec uses an autocommit statement, so the expected ABORT cannot
				// leave an open transaction affecting subsequent reads or C2B.
				_, err := f.h.db.Exec(`UPDATE transactions SET amount=525501 WHERE workspace_id='workspace-rbac' AND id='o1b2-receipt'`)
				constraint, ok := err.(interface{ Code() int })
				if !ok || constraint.Code() != 1811 { // SQLITE_CONSTRAINT_TRIGGER
					t.Fatalf("sealed receipt update: want SQLITE_CONSTRAINT_TRIGGER, got %v", err)
				}
				if current := f.read(t, 200); !reflect.DeepEqual(state, current) || before != f.stable(t) || proof != f.proof(t) || financial != f.financial(t) {
					t.Fatal("rejected sealed receipt update changed persisted state")
				}
				var c services.InvestmentResultClassification
				f.effect(t, "classificar", f.classificationForm(a.ID), 200, &c)
				if c.ID == "" || c.State != services.InvestmentClassificationCompleted || c.Nature != "REALIZED_GAIN" || c.ResultCents == nil || *c.ResultCents != 25500 || c.TransactionID == "" {
					t.Fatalf("classification with preserved receipt: %+v", c)
				}
				current := f.read(t, 200)
				if current.Application == nil || !reflect.DeepEqual(a, *current.Application) || current.Classification == nil || !reflect.DeepEqual(c, *current.Classification) || financial != f.financial(t) || proof != f.proof(t) {
					t.Fatal("classification changed financial state or receipt evidence")
				}
				return
			}
			f.h.exec(t, `UPDATE transactions SET amount=525501 WHERE workspace_id='workspace-rbac' AND id='o1b2-receipt'`)
			before := f.stable(t)
			f.read(t, 409)
			f.effect(t, "aplicar", f.applyForm, 409, nil)
			if before != f.stable(t) {
				t.Fatal("drift mutated financial state")
			}
		})
	}
}

func TestInvestimentosO1B2RecoveryAndDivergentReplay(t *testing.T) {
	f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
	before := f.stable(t)
	f.effect(t, "classificar", f.classificationForm(uuid.NewString()), 409, nil)
	if before != f.stable(t) {
		t.Fatal("C2B before C2A wrote data")
	}
	a := f.apply(t)
	before = f.stable(t)
	bad := cloneO1B1Form(f.applyForm)
	bad.Set("reason", "Divergent payload")
	f.effect(t, "aplicar", bad, 409, nil)
	bad = cloneO1B1Form(f.applyForm)
	bad.Set("request_id", uuid.NewString())
	f.effect(t, "aplicar", bad, 409, nil)
	form := f.classificationForm(a.ID)
	bad = cloneO1B1Form(form)
	bad.Set("category_id", "o1b2-loss")
	f.effect(t, "classificar", bad, 400, nil)
	s := f.read(t, 200)
	if before != f.stable(t) || s.ApplicationID != a.ID || s.ClassificationID != "" || s.Application.State != services.InvestmentSettlementFinancialApplied {
		t.Fatal("C2B failure reverted or reapplied C2A")
	}
	// Re-register the production routes to simulate a new HTTP owner after C2A.
	f.h.mux = http.NewServeMux()
	registerInvestimentosRoutes(f.h.mux, f.h.service, f.h.signer, f.h.base)
	if replay := f.apply(t); !reflect.DeepEqual(a, replay) {
		t.Fatal("lost durable application")
	}
	financial := f.financial(t)
	var c services.InvestmentResultClassification
	f.effect(t, "classificar", form, 200, &c)
	before = f.stable(t)
	bad = cloneO1B1Form(form)
	bad.Set("legacy_reconciliation_evidence", "Divergent payload")
	f.effect(t, "classificar", bad, 409, nil)
	bad = cloneO1B1Form(form)
	bad.Set("request_id", uuid.NewString())
	f.effect(t, "classificar", bad, 409, nil)
	if financial != f.financial(t) || before != f.stable(t) {
		t.Fatal("retry/conflict reapplied cash or classification")
	}
}

func TestInvestimentosO1B2ConcurrentRequests(t *testing.T) {
	for _, operation := range []string{"aplicar", "classificar"} {
		for _, sameKey := range []bool{true, false} {
			t.Run(operation+fmt.Sprint(sameKey), func(t *testing.T) {
				f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
				form := f.applyForm
				if operation == "classificar" {
					a := f.apply(t)
					form = f.classificationForm(a.ID)
				}
				financial := f.financial(t)
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
						responses[i] = f.h.request(t, "POST", f.path()+"/"+operation, f.h.admin, input, true, false)
					}(i, input)
				}
				close(start)
				wg.Wait()
				ok, id := 0, ""
				for _, rr := range responses {
					if rr.Code == 200 {
						ok++
						var r struct{ ID string }
						if err := json.Unmarshal(rr.Body.Bytes(), &r); err != nil {
							t.Fatal(err)
						}
						if r.ID == "" || id != "" && id != r.ID {
							t.Fatal("multiple effective results")
						}
						id = r.ID
					} else if sameKey || rr.Code != 409 {
						t.Fatalf("concurrency: %d %s", rr.Code, rr.Body.String())
					}
				}
				if sameKey && ok != n || !sameKey && ok != 1 {
					t.Fatalf("successes: %d", ok)
				}
				var apps, classes, adjustments int
				if err := f.h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM investment_settlement_applications WHERE workspace_id='workspace-rbac'),(SELECT COUNT(*) FROM investment_result_classifications WHERE workspace_id='workspace-rbac'),(SELECT COUNT(*) FROM account_balance_adjustments WHERE workspace_id='workspace-rbac')`).Scan(&apps, &classes, &adjustments); err != nil {
					t.Fatal(err)
				}
				if apps != 1 || adjustments != 2 || operation == "classificar" && classes != 1 || operation == "aplicar" && classes != 0 {
					t.Fatal("duplicate effects", apps, classes, adjustments)
				}
				if operation == "classificar" && financial != f.financial(t) {
					t.Fatal("concurrent C2B moved cash")
				}
				if s := f.read(t, 200); s.Account.BalanceCents != 525500 {
					t.Fatal("balance duplicated", s)
				}
			})
		}
	}
}

func TestInvestimentosO1B2AtomicRollbackAndRecovery(t *testing.T) {
	for _, table := range []string{"account_balance_adjustments", "investment_settlement_applications", "investment_result_classifications", "transactions"} {
		t.Run(table, func(t *testing.T) {
			f := newO1B2(t, o1b2Scenario{net: 525500, principal: "5000,00"})
			operation, form := "aplicar", f.applyForm
			classification := table == "investment_result_classifications" || table == "transactions"
			if classification {
				a := f.apply(t)
				operation, form = "classificar", f.classificationForm(a.ID)
			}
			f.h.exec(t, `CREATE TRIGGER o1b2_injected BEFORE INSERT ON `+table+` WHEN NEW.workspace_id='workspace-rbac' BEGIN SELECT RAISE(ABORT,'injected'); END`)
			before := f.stable(t)
			rr := f.effect(t, operation, form, 500, nil)
			if strings.Contains(rr.Body.String(), "injected") || before != f.stable(t) {
				t.Fatal("internal error leaked details or partial writes")
			}
			s := f.read(t, 200)
			if classification && (s.Application == nil || s.Classification != nil || s.Account.BalanceCents != 525500) {
				t.Fatal("C2B rollback lost confirmed C2A")
			}
			if !classification && (s.Application != nil || s.Account.BalanceCents != 530000) {
				t.Fatal("C2A rollback incomplete")
			}
			f.h.exec(t, `DROP TRIGGER o1b2_injected`)
			var result any
			f.effect(t, operation, form, 200, &result) // ORIGINAL request ID and payload.
		})
	}
}
