package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func investmentI21Form() url.Values {
	return url.Values{"request_id": {uuid.NewString()}, "purchase_date": {"2026-03-01"}, "principal_amount": {"R$ 2.000,00"}, "note_reference": {"NC-123"}, "kind": {"Tesouro IPCA+"}, "institution": {"Corretora B"}, "maturity_date": {"2032-01-01"}, "tax_regime": {"MANUAL"}, "conditions_note": {"IPCA + 6%"}}
}

func TestInvestimentosI21RoutesSecurityValidationAndReplay(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Purchase security")
	path := "/investimentos/" + id + "/compras"
	before := investmentI11FinancialState(t, h)
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		for _, hx := range []bool{false, true} {
			requireInvestimentosStatus(t, h.request(t, "POST", path, session, investmentI21Form(), true, hx), 403)
		}
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, "", investmentI21Form(), true, false), 303)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI21Form(), false, true), 403)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		requireInvestimentosStatus(t, h.request(t, method, path, h.admin, investmentI21Form(), true, false), 405)
	}
	for _, suffix := range []string{"/", "/extra"} {
		requireInvestimentosStatus(t, h.request(t, "POST", path+suffix, h.admin, investmentI21Form(), true, false), 404)
	}
	foreignSession := h.session(t, "admin-user", "workspace-b")
	requireInvestimentosStatus(t, h.request(t, "POST", path, foreignSession, investmentI21Form(), true, false), 404)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/missing/compras", h.admin, investmentI21Form(), true, false), 404)
	for _, amount := range []string{"0", "-1", "300.00", "1,234.56", "1.23,45", "300,001", "92233720368547758,08"} {
		form := investmentI21Form()
		form.Set("principal_amount", amount)
		rr := h.request(t, "POST", path, h.admin, form, true, true)
		requireInvestimentosStatus(t, rr, 400)
		if rr.Header().Get("HX-Redirect") != "" {
			t.Fatal("invalid amount redirected")
		}
	}
	for _, fields := range []url.Values{{"purchase_date": {"2026-02-31"}}, {"purchase_date": {"2025-01-01"}}, {"maturity_date": {"2026-02-01"}}, {"request_id": {""}}} {
		form := investmentI21Form()
		for k, v := range fields {
			form[k] = v
		}
		requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, true), 400)
	}
	// Native failure preserves values and the retry identity in the real page.
	form := investmentI21Form()
	form.Set("principal_amount", "300.00")
	rr := h.request(t, "POST", path, h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 400)
	for _, want := range []string{"data-investment-reopen", `value="300.00"`, `value="` + form.Get("request_id") + `"`, "valor aplicado"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("native error missing %q", want)
		}
	}
	form = investmentI21Form()
	form.Set("workspace_id", "workspace-b")
	form.Set("investment_id", "missing")
	form.Set("created_by", "regular-user")
	form.Set("is_initial", "1")
	form.Set("account_id", "inv-b")
	rr = h.request(t, "POST", path, h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Redirect") != "/investimentos/"+id+"?purchased=1" {
		t.Fatal("success lacks feedback redirect")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, false), 303)
	var count, total int64
	var ws, author string
	if err := h.db.QueryRow(`SELECT COUNT(*),SUM(principal_cents) FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&count, &total); err != nil || count != 2 || total != 300000 {
		t.Fatalf("repeat total=%d/%d err=%v", count, total, err)
	}
	if err := h.db.QueryRow(`SELECT workspace_id,created_by FROM investment_purchases WHERE investment_id=? AND is_initial=0`, id).Scan(&ws, &author); err != nil || ws != "workspace-rbac" || author != "admin-user" {
		t.Fatal("payload forged purchase authority")
	}
	form.Set("principal_amount", "1,00")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, true), 409)
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("HTTP purchases changed positions, balances or ledger")
	}
	// Feature OFF uses the existing fail-closed tombstones for the new route.
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "")
	h.mux = http.NewServeMux()
	registerInvestimentosRoutes(h.mux, h.service, h.signer, nil)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI21Form(), true, true), 404)
}

func TestInvestimentosI21DetailHistoryStaleConsolidationAndClosure(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Purchase history")
	other := h.create(t, "Fresh comparison")
	h.exec(t, `INSERT INTO investment_valuations (id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES ('prior','workspace-rbac',?,'2026-02-01',110000,'admin-user'),('fresh','workspace-rbac',?,'2026-02-01',120000,'admin-user')`, id, other)
	form := investmentI21Form()
	form.Set("note_reference", "<script>NC-123</script>")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, form, true, false), 303)
	rr := h.request(t, "GET", "/investimentos/"+id+"?purchased=1", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	body := html.UnescapeString(rr.Body.String())
	for _, want := range []string{"Reaplicação registrada com sucesso.", "Total de compras realizadas", "R$ 3.000,00", "2 compras", "Última posição conhecida", "R$ 1.100,00", "Desatualizada em relação às compras", "A posição é anterior à última compra", "Compras e reaplicações", "Inicial", "Reaplicação", "Corretora B", "NC-123", "01/03/2026", "Histórico e evolução das posições"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing %q", want)
		}
	}
	if strings.Contains(rr.Body.String(), "<script>NC-123</script>") || strings.Contains(body, "- R$ 1.900,00") {
		t.Fatal("unsafe notes or stale position compared to later purchases")
	}
	formHTML := regexp.MustCompile(`(?s)<form[^>]*action="/investimentos/` + id + `/compras".*?</form>`).FindString(body)
	for _, want := range []string{`data-investment-money="positive"`, `hx-sync="this:drop"`, `name="csrf_token"`, `name="request_id"`, `name="purchase_date"`, `name="note_reference"`, `name="conditions_note"`, `value="Banco A"`, `value="CDB"`} {
		if !strings.Contains(formHTML, want) {
			t.Fatalf("purchase form missing %q", want)
		}
	}
	list := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, list, 200)
	if !strings.Contains(list.Body.String(), "R$ 1.200,00") || !strings.Contains(list.Body.String(), "fora da soma") || strings.Contains(list.Body.String(), "R$ 2.300,00") {
		t.Fatal("stale position included in current consolidation")
	}
	// A fresh manual position restores an interpretable difference, never a return percentage.
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, url.Values{"valuation_date": {"2026-03-02"}, "amount": {"3100,00"}}, true, false), 303)
	rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	body = html.UnescapeString(rr.Body.String())
	if !strings.Contains(body, "+ R$ 100,00") || strings.Contains(body, "Desatualizada em relação às compras") || strings.Contains(body, "3,33%") {
		t.Fatal("fresh difference incorrect after decoding HTML entities")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/resgatar", h.admin, url.Values{"redeemed_date": {"2026-04-01"}, "final_amount": {"3100,00"}}, true, false), 303)
	for _, archived := range []bool{false, true} {
		if archived {
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/arquivar", h.admin, nil, true, false), 303)
		}
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, investmentI21Form(), true, true), 409)
		rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
		requireInvestimentosStatus(t, rr, 200)
		if strings.Contains(rr.Body.String(), `data-investment-open="investment-purchase-panel"`) || !strings.Contains(rr.Body.String(), "NC-123") {
			t.Fatal("closed purchases history or actions incorrect")
		}
	}
}

// Real authenticated routes, CSRF, embedded templates and local assets. This
// disposable fixture never opens the maintainer's database or starts jobs.
func TestInvestimentosI21BrowserFixture(t *testing.T) {
	if os.Getenv("CONTABASE_I21_BROWSER_FIXTURE") != "1" {
		t.Skip("interactive I2.1 browser fixture")
	}
	h := newInvestimentosRoutesHarness(t)
	form := investimentosRouteForm()
	form.Set("name", "IPCA+ 2032 Vitor")
	form.Set("principal_amount", "3000,00")
	form.Set("institution", "Corretora")
	form.Set("kind", "Tesouro IPCA+")
	form.Set("maturity_date", "2032-10-12")
	form.Set("applied_date", "2026-10-10")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, false), 303)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE name='IPCA+ 2032 Vitor'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, url.Values{"valuation_date": {"2026-10-11"}, "amount": {"3050,00"}}, true, false), 303)
	before := investmentI11FinancialState(t, h)
	finished := make(chan struct{})
	var once sync.Once
	h.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("../../assets"))))
	h.mux.HandleFunc("/__i21/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: h.admin, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/investimentos/"+id, 303)
	})
	h.mux.HandleFunc("/__i21/finish", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(finished) }); w.WriteHeader(200) })
	server := httptest.NewServer(securityHeaders(h.mux))
	defer server.Close()
	fmt.Println("I21_BROWSER_URL=" + server.URL + "/__i21/start")
	select {
	case <-finished:
	case <-time.After(12 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("browser altered financial state or positions")
	}
	var count, total int64
	if err := h.db.QueryRow(`SELECT COUNT(*),SUM(principal_cents) FROM investment_purchases WHERE investment_id=?`, id).Scan(&count, &total); err != nil || count != 2 || total != 500000 {
		t.Fatalf("browser purchases=%d total=%d err=%v", count, total, err)
	}
}
