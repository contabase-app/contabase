package main

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/services"
)

// A real loopback HTTP listener over a disposable schema-77 database. Uses the
// actual investment routes, session cookies, CSRF guard and Immediate writer.
func TestInvestimentosBeta7Schema79HTTPSmoke(t *testing.T) {
	h := newInvestimentosI22Harness(t)
	for _, flag := range []string{"CONTABASE_FEATURE_INVESTMENTS", "CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS"} {
		t.Setenv(flag, "")
		if err := os.Unsetenv(flag); err != nil {
			t.Fatal(err)
		}
	}
	h.mux = http.NewServeMux()
	registerInvestimentosRoutes(h.mux, h.service, h.signer, h.base)
	h.exec(t, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='workspace-rbac' AND id='checking-a'`)
	server := httptest.NewServer(h.mux)
	defer server.Close()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, session string, form url.Values, csrf bool) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if session != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		}
		if csrf {
			addAdminUsersRBACCSRF(t, req, h.signer)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, html.UnescapeString(string(body)), response.Header.Get("Location")
	}
	mustStatus := func(want int, method, path, session string, form url.Values, csrf bool) (string, string) {
		t.Helper()
		status, body, location := request(method, path, session, form, csrf)
		if status != want {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, path, status, want, body)
		}
		return body, location
	}
	body, _ := mustStatus(200, "GET", "/investimentos", h.admin, nil, false)
	for _, want := range []string{`name="source_account_id"`, `name="funding_mode"`, `value="checking-a"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("form missing %s", want)
		}
	}
	if strings.Contains(body, `value="inv-b"`) {
		t.Fatal("foreign account leaked")
	}
	form := investimentosRouteForm()
	form.Set("source_account_id", "checking-a")
	form.Set("funding_mode", "transfer")
	form.Set("principal_amount", "5000,00")
	form.Set("name", "Beta6 financeira")
	mustStatus(403, "POST", "/investimentos", h.admin, form, false)
	regular := h.session(t, "regular-user", "workspace-rbac")
	mustStatus(403, "POST", "/investimentos", regular, form, true)
	missing := investimentosRouteForm()
	missing.Set("funding_mode", "transfer")
	mustStatus(400, "POST", "/investimentos", h.admin, missing, true)
	_, location := mustStatus(303, "POST", "/investimentos", h.admin, form, true)
	id := strings.TrimSuffix(strings.TrimPrefix(location, "/investimentos/"), "?created=1")
	if id == "" || strings.Contains(id, "/") {
		t.Fatal("bad redirect", location)
	}
	balances := func(cash, investment int64) {
		t.Helper()
		for account, want := range map[string]int64{"checking-a": cash, "inv-a": investment} {
			var got int64
			if err := h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='workspace-rbac' AND id=?`, account).Scan(&got); err != nil || got != want {
				t.Fatalf("%s=%d want=%d err=%v", account, got, want, err)
			}
		}
	}
	balances(500000, 500000)
	mustStatus(303, "POST", "/investimentos", h.admin, form, true)
	balances(500000, 500000)
	purchase := investmentI21Form()
	purchase.Set("source_account_id", "checking-a")
	purchase.Set("funding_mode", "transfer")
	purchase.Set("purchase_date", form.Get("applied_date"))
	path := "/investimentos/" + id + "/compras"
	mustStatus(303, "POST", path, h.admin, purchase, true)
	balances(300000, 700000)
	before := o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases")
	mustStatus(303, "POST", path, h.admin, purchase, true)
	if before != o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases") {
		t.Fatal("HTTP replay changed journal")
	}
	wrong := investmentI21Form()
	wrong.Set("source_account_id", "inv-b")
	wrong.Set("funding_mode", "transfer")
	mustStatus(400, "POST", path, h.admin, wrong, true)
	foreign := h.session(t, "admin-user", "workspace-b")
	mustStatus(404, "POST", path, foreign, purchase, true)
	mustStatus(403, "POST", path, regular, purchase, true)
	mustStatus(403, "POST", path, h.admin, purchase, false)
	// F1: omitting the origin on a funded inventory records an explicit
	// record-only purchase — history only. Neither money nor patrimonial
	// coverage changes, and unhomologated exits stay refused.
	mustStatus(303, "POST", path, h.admin, investmentI21Form(), true)
	balances(300000, 700000)
	before = o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases")
	mustStatus(409, "POST", "/investimentos/"+id+"/resgatar", h.admin, url.Values{"redeemed_date": {"2026-04-01"}, "final_amount": {"7000,00"}}, true)
	mustStatus(409, "POST", "/investimentos/"+id+"/retiradas", h.admin, investmentI22Form("initial:"+id), true)
	mustStatus(404, "POST", "/investimentos/"+id+"/financeiro/retiradas/nope/liquidacao/aplicar", h.admin, url.Values{}, true)
	body, _ = mustStatus(200, "GET", "/investimentos/"+id, h.admin, nil, false)
	for _, want := range []string{"R$ 9.000,00", "3 compras", "Corrente A", "Origem da aplicação", "Consultar lançamento", "Transferência automática", "Sem movimentação automática"} {
		if !strings.Contains(body, want) {
			t.Fatalf("history missing %s", want)
		}
	}
	if strings.Contains(body, `data-investment-open="investment-withdrawal-panel"`) {
		t.Fatal("unhomologated withdrawal visible")
	}
	if before != o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases") {
		t.Fatal("rejected requests changed money")
	}
	// The same guards used by single and bulk generic journal mutations protect
	// both generated transfer receipts.
	var transfer string
	if err := h.db.QueryRow(`SELECT id FROM transactions WHERE workspace_id='workspace-rbac' AND id=?`, services.InvestmentPurchaseTransferPrefix+"initial:"+id).Scan(&transfer); err != nil {
		t.Fatal(err)
	}
	transaction := &handlers.TransactionHandler{DB: h.db, WriterDB: h.base.WriterDB, WorkspaceID: "workspace-rbac", UserID: "admin-user"}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"delete":      func(w http.ResponseWriter, r *http.Request) { transaction.HandleDeletarTransacao(w, r, transfer) },
		"edit":        func(w http.ResponseWriter, r *http.Request) { transaction.HandleAtualizarTransacao(w, r, transfer) },
		"toggle":      func(w http.ResponseWriter, r *http.Request) { transaction.HandleTogglePagamento(w, r, transfer) },
		"bulk-delete": transaction.HandleBulkDelete,
		"bulk-update": transaction.HandleBulkUpdate,
	} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/test", strings.NewReader(url.Values{"ids": {transfer}, "status_pagamento": {"pending"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		call(rr, req)
		if rr.Code != 409 {
			t.Fatalf("%s status=%d body=%s", name, rr.Code, rr.Body.String())
		}
	}
	if before != o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases") {
		t.Fatal("generic mutations changed funded inventory")
	}
	var version int
	if err := h.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 82 {
		t.Fatal("wrong schema", version, err)
	}
	t.Log("HTTP_SMOKE schema=82 checking=1000000->500000->300000 investment=0->500000->700000 purchases=3 record_only=1 transfers=2 same_day=yes replay=exact RBAC=403 CSRF=403 workspace=404 funded_exits=409 generic_mutations=409")
}
