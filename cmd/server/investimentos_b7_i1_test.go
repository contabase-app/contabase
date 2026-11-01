package main

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestInvestimentosB7I1DisplayedPostPersistsExactCents(t *testing.T) {
	data, err := os.ReadFile("../../assets/js/testdata/investment-money-b7.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Typed, Display string
		Cents          int64
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples {
		t.Run(sample.Typed, func(t *testing.T) {
			h := newInvestimentosRoutesHarness(t)
			form := investimentosRouteForm()
			form.Set("principal_amount", sample.Display)
			form.Set("source_account_id", "checking-a")
			form.Set("funding_mode", "transfer")
			rr := h.request(t, "POST", "/investimentos", h.admin, form, true, true)
			requireInvestimentosStatus(t, rr, 200)
			id := strings.TrimSuffix(strings.TrimPrefix(rr.Header().Get("HX-Redirect"), "/investimentos/"), "?created=1")
			var principal, initial, transferred int64
			if err := h.db.QueryRow(`SELECT i.principal_cents,p.principal_cents,t.amount FROM investments i
			 JOIN investment_purchases p ON p.workspace_id=i.workspace_id AND p.investment_id=i.id AND p.is_initial=1
			 JOIN transactions t ON t.workspace_id=p.workspace_id AND t.id='investment-purchase:'||p.id
			 WHERE i.workspace_id=? AND i.id=?`, "workspace-rbac", id).Scan(&principal, &initial, &transferred); err != nil || principal != sample.Cents || initial != sample.Cents || transferred != sample.Cents {
				t.Fatalf("create %q: principal=%d purchase=%d transfer=%d want=%d err=%v", sample.Display, principal, initial, transferred, sample.Cents, err)
			}
			purchase := investmentI21Form()
			purchase.Set("principal_amount", sample.Display)
			purchase.Set("source_account_id", "checking-a")
			purchase.Set("funding_mode", "transfer")
			rr = h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, purchase, true, true)
			requireInvestimentosStatus(t, rr, 200)
			var purchaseTotal, transferTotal, cash, investment int64
			if err := h.db.QueryRow(`SELECT SUM(principal_cents) FROM investment_purchases WHERE workspace_id=? AND investment_id=?`, "workspace-rbac", id).Scan(&purchaseTotal); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT SUM(amount) FROM transactions WHERE workspace_id=? AND type='TRANSFER'`, "workspace-rbac").Scan(&transferTotal); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?`, "workspace-rbac", "checking-a").Scan(&cash); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?`, "workspace-rbac", "inv-a").Scan(&investment); err != nil {
				t.Fatal(err)
			}
			if purchaseTotal != 2*sample.Cents || transferTotal != purchaseTotal || cash != -purchaseTotal || investment != purchaseTotal {
				t.Fatalf("purchase/transfer/balances: %d/%d/%d/%d", purchaseTotal, transferTotal, cash, investment)
			}
			position := url.Values{"amount": {sample.Display}, "valuation_date": {"2026-03-02"}}
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, position, true, true), 200)
			var cents int64
			if err := h.db.QueryRow(`SELECT amount_cents FROM investment_valuations WHERE workspace_id=? AND investment_id=?`, "workspace-rbac", id).Scan(&cents); err != nil || cents != sample.Cents {
				t.Fatalf("position %q: cents=%d want=%d err=%v", sample.Display, cents, sample.Cents, err)
			}
			var afterCash, afterInvestment int64
			if err := h.db.QueryRow(`SELECT (SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?),(SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?)`, "workspace-rbac", "checking-a", "workspace-rbac", "inv-a").Scan(&afterCash, &afterInvestment); err != nil || afterCash != cash || afterInvestment != investment {
				t.Fatalf("position changed balances: %d/%d err=%v", afterCash, afterInvestment, err)
			}
			position.Set("amount", "R$ 0,00")
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, position, true, false), 303)
			if err := h.db.QueryRow(`SELECT amount_cents FROM investment_valuations WHERE workspace_id=? AND investment_id=?`, "workspace-rbac", id).Scan(&cents); err != nil || cents != 0 {
				t.Fatalf("zero position: %d %v", cents, err)
			}
		})
	}
}

func TestInvestimentosB7I1NativeFormsKeepReaisContract(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	page := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, page, 200)
	if !strings.Contains(page.Body.String(), "Digite em reais") || !strings.Contains(page.Body.String(), "data-investment-money-dial") {
		t.Fatal("native guidance or visual opt-in missing")
	}
	form := investimentosRouteForm()
	form.Set("principal_amount", "1")
	form.Set("source_account_id", "checking-a")
	form.Set("funding_mode", "transfer")
	rr := h.request(t, "POST", "/investimentos", h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 303)
	id := strings.TrimSuffix(strings.TrimPrefix(rr.Header().Get("Location"), "/investimentos/"), "?created=1")
	var cents int64
	if err := h.db.QueryRow(`SELECT principal_cents FROM investments WHERE workspace_id=? AND id=?`, "workspace-rbac", id).Scan(&cents); err != nil || cents != 100 {
		t.Fatalf("native integer must remain reais: %d %v", cents, err)
	}
	purchase := investmentI21Form()
	purchase.Set("principal_amount", "1")
	purchase.Set("source_account_id", "checking-a")
	purchase.Set("funding_mode", "transfer")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, purchase, true, false), 303)
	if err := h.db.QueryRow(`SELECT principal_cents FROM investment_purchases WHERE workspace_id=? AND investment_id=? AND is_initial=0`, "workspace-rbac", id).Scan(&cents); err != nil || cents != 100 {
		t.Fatalf("native purchase integer: %d %v", cents, err)
	}
	position := url.Values{"amount": {"1"}, "valuation_date": {"2026-03-02"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, position, true, false), 303)
	if err := h.db.QueryRow(`SELECT amount_cents FROM investment_valuations WHERE workspace_id=? AND investment_id=?`, "workspace-rbac", id).Scan(&cents); err != nil || cents != 100 {
		t.Fatalf("native position integer: %d %v", cents, err)
	}
}
