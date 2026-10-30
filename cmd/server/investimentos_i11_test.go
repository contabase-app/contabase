package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func investmentI11Form(t *testing.T, h *investimentosRoutesHarness) url.Values {
	t.Helper()
	rr := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	key := regexp.MustCompile(`name="request_id" value="([^"]+)"`).FindStringSubmatch(rr.Body.String())
	if len(key) != 2 {
		t.Fatal("creation form missing retry key")
	}
	form := investimentosRouteForm()
	form.Set("request_id", key[1])
	form.Set("principal_amount", "300,00")
	return form
}

func investmentI11FinancialState(t *testing.T, h *investimentosRoutesHarness) string {
	t.Helper()
	var state string
	err := h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM transactions) || ':' ||
		(SELECT COUNT(*) FROM box_virtual_ledger) || ':' ||
		(SELECT COUNT(*) FROM investment_valuations) || ':' ||
		group_concat(id || ':' || current_balance) FROM (SELECT id,current_balance FROM accounts ORDER BY id)`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestInvestimentosI11CreateReaisSuccessAndReplay(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	before := investmentI11FinancialState(t, h)
	form := investmentI11Form(t, h)
	form.Set("workspace_id", "workspace-b")
	form.Set("created_by", "regular-user")
	form.Set("principal_cents", "999999") // cannot multiply/override the visible reais
	rr := h.request(t, "POST", "/investimentos", h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	location := rr.Header().Get("HX-Redirect")
	var id, workspace, author string
	var principal int64
	if err := h.db.QueryRow(`SELECT id,workspace_id,created_by,principal_cents FROM investments`).Scan(&id, &workspace, &author, &principal); err != nil {
		t.Fatal(err)
	}
	if principal != 30000 || workspace != "workspace-rbac" || author != "admin-user" || location != "/investimentos/"+id+"?created=1" {
		t.Fatalf("creation contract: amount=%d identity=%s/%s redirect=%s", principal, workspace, author, location)
	}
	rr = h.request(t, "GET", location, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	for _, want := range []string{`role="status"`, "Investimento cadastrado com sucesso.", "R$ 300,00"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("success page missing %q", want)
		}
	}
	for _, hx := range []bool{false, true} {
		rr = h.request(t, "POST", "/investimentos", h.admin, form, true, hx)
		if hx {
			requireInvestimentosStatus(t, rr, 200)
			if rr.Header().Get("HX-Redirect") != location {
				t.Fatal("replay changed destination")
			}
		} else {
			requireInvestimentosStatus(t, rr, 303)
			if rr.Header().Get("Location") != location {
				t.Fatal("native replay changed destination")
			}
		}
	}
	form.Set("principal_amount", "301,00")
	rr = h.request(t, "POST", "/investimentos", h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 409)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replays created duplicates: %d %v", count, err)
	}
	if after := investmentI11FinancialState(t, h); before != after {
		t.Fatalf("creation changed balances, ledger or positions: %s -> %s", before, after)
	}
}

func TestInvestimentosI11InvalidAndPreservedForm(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	before := investmentI11FinancialState(t, h)
	for _, amount := range []string{"", "abc", "-300,00", "0", "0,00", "300,001", "300.00", "1,234", "12,3456", "1e3", "NaN", "30,0,0", "1.23,45", "1,234.56", "300,", ",01", "92233720368547758,08", "92.233.720.368.547.758,08", "92233720368547759", "18446744073709551616,00"} {
		t.Run(amount, func(t *testing.T) {
			form := investmentI11Form(t, h)
			form.Set("principal_amount", amount)
			rr := h.request(t, "POST", "/investimentos", h.admin, form, true, true)
			requireInvestimentosStatus(t, rr, 400)
			if !strings.Contains(rr.Body.String(), "valor aplicado") || rr.Header().Get("HX-Redirect") != "" || rr.Header().Get("HX-Refresh") != "" {
				t.Fatal("HTMX validation must report a specific error without navigation")
			}
			var count int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid amount %q persisted a record: %d %v", amount, count, err)
			}
		})
	}
	form := investmentI11Form(t, h)
	form.Set("principal_amount", "300,001")
	form.Set("name", `CDB <script>alert(1)</script>`)
	form.Set("institution", "Instituição preservada")
	form.Set("tax_regime", "MANUAL")
	form.Set("maturity_date", "2027-01-15")
	form.Set("yield_note", "Nota preservada")
	rr := h.request(t, "POST", "/investimentos", h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 400)
	for _, want := range []string{`role="alert"`, `value="300,001"`, `value="inv-a" selected`, `value="Instituição preservada"`, `value="MANUAL" selected`, `value="2026-01-15"`, `value="2027-01-15"`, "Nota preservada", `value="` + form.Get("request_id") + `"`, "CDB &lt;script&gt;"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("native validation lost %q", want)
		}
	}
	if strings.Contains(rr.Body.String(), `<script>alert(1)</script>`) {
		t.Fatal("unescaped input")
	}
	form.Set("principal_amount", "300,00")
	form.Set("request_id", "")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, true, true), 400)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid form wrote a lot: %d %v", count, err)
	}
	if after := investmentI11FinancialState(t, h); before != after {
		t.Fatalf("invalid input changed balances, ledger or positions: %s -> %s", before, after)
	}
}

func TestInvestimentosI11ConcurrentReplayAndGuards(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	form := investmentI11Form(t, h)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- h.request(t, "POST", "/investimentos", h.admin, form, true, true)
		}()
	}
	wg.Wait()
	close(responses)
	var location string
	for rr := range responses {
		requireInvestimentosStatus(t, rr, 200)
		if location != "" && location != rr.Header().Get("HX-Redirect") {
			t.Fatal("concurrent replay returned another lot")
		}
		location = rr.Header().Get("HX-Redirect")
	}
	for _, account := range []string{"inv-b", "checking-a", "archived-a", "missing"} {
		invalid := investmentI11Form(t, h)
		invalid.Set("account_id", account)
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, invalid, true, true), 400)
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, form, false, true), 403)
	regular := h.session(t, "regular-user", "workspace-rbac")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", regular, form, true, true), 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrency/guards changed row count: %d %v", count, err)
	}
	// The same retry key remains scoped to authenticated workspace + author.
	b := h.session(t, "admin-user", "workspace-b")
	form.Set("account_id", "inv-b")
	rr := h.request(t, "POST", "/investimentos", b, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Redirect") == location {
		t.Fatal("retry key reused another workspace's lot")
	}
}

// Optional browser fixture: real routes, CSRF and embedded templates on an
// isolated in-memory database, with no server runtime or financial jobs.
func TestInvestimentosI11BrowserFixture(t *testing.T) {
	if os.Getenv("CONTABASE_I11_BROWSER_FIXTURE") != "1" {
		t.Skip("interactive browser fixture")
	}
	h := newInvestimentosRoutesHarness(t)
	before := investmentI11FinancialState(t, h)
	finished := make(chan struct{})
	var once sync.Once
	var posts atomic.Int64
	h.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("../../assets"))))
	h.mux.HandleFunc("/__i11/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: h.admin, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/investimentos", 303)
	})
	h.mux.HandleFunc("/__i11/finish", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(finished) })
		w.WriteHeader(200)
	})
	server := httptest.NewServer(securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/investimentos" {
			posts.Add(1)
			time.Sleep(1500 * time.Millisecond) // observe disabled submit while awaiting the response
		}
		h.mux.ServeHTTP(w, r)
	})))
	defer server.Close()
	fmt.Println("I11_BROWSER_URL=" + server.URL + "/__i11/start")
	select {
	case <-finished:
	case <-time.After(8 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	var count, principal int64
	if err := h.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(principal_cents),0) FROM investments`).Scan(&count, &principal); err != nil || count != 1 || principal != 30000 {
		t.Fatalf("browser creation: rows=%d principal=%d err=%v", count, principal, err)
	}
	if posts.Load() != 2 {
		t.Fatalf("expected one validation error and one successful POST, got %d", posts.Load())
	}
	if after := investmentI11FinancialState(t, h); before != after {
		t.Fatal("browser fixture altered balances, ledger or positions")
	}
}

func investmentI12LedgerState(t *testing.T, h *investimentosRoutesHarness) string {
	t.Helper()
	var state string
	if err := h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM transactions) || ':' ||
        (SELECT COUNT(*) FROM box_virtual_ledger) || ':' ||
        group_concat(id || ':' || current_balance) FROM (SELECT id,current_balance FROM accounts ORDER BY id)`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestInvestimentosI12DrawersAndReaisOperations(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	before := investmentI12LedgerState(t, h)
	rr := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	for _, want := range []string{`<dialog id="investment-create-panel"`, `data-investment-open="investment-create-panel"`, `data-investment-money="positive"`, "Nenhum investimento neste filtro"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("empty/create missing %s", want)
		}
	}
	form := investmentI11Form(t, h)
	form.Set("principal_amount", "R$ 3.000,00")
	rr = h.request(t, "POST", "/investimentos", h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	location := rr.Header().Get("HX-Redirect")
	id := strings.TrimSuffix(strings.TrimPrefix(location, "/investimentos/"), "?created=1")
	rr = h.request(t, "GET", location, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	for _, want := range []string{`<dialog id="investment-position-panel"`, `<dialog id="investment-edit-panel"`, `<dialog id="investment-redeem-panel"`, `name="amount"`, `name="final_amount"`, "Você ainda não informou o valor atual desta aplicação.", "R$ 3.000,00", "Encerra o acompanhamento no ContaBase e preserva o histórico. Não registra retirada nem movimenta dinheiro."} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("detail missing %s", want)
		}
	}
	b := h.session(t, "admin-user", "workspace-b")
	for _, path := range []string{"/valuations", "/resgatar"} {
		field, dateField := "amount", "valuation_date"
		if path == "/resgatar" {
			field, dateField = "final_amount", "redeemed_date"
		}
		values := url.Values{field: {"R$ 3.420,00"}, dateField: {"2026-03-01"}}
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+path, h.admin, values, false, true), 403)
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+path, b, values, true, true), 404)
		for _, invalid := range []string{"300,001", "300.00", "1,234.56", "", "-1"} {
			values.Set(field, invalid)
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+path, h.admin, values, true, true), 400)
		}
	}
	values := url.Values{"amount": {"R$ 3.420,00"}, "valuation_date": {"2026-03-01"}, "amount_cents": {"999"}}
	rr = h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, values, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Refresh") != "true" {
		t.Fatal("missing position refresh")
	}
	var amount int64
	if err := h.db.QueryRow(`SELECT amount_cents FROM investment_valuations WHERE investment_id=?`, id).Scan(&amount); err != nil || amount != 342000 {
		t.Fatalf("position %d %v", amount, err)
	}
	edit := url.Values{"name": {"Novo nome"}, "institution": {"Banco"}, "kind": {"CDB"}, "tax_regime": {"MANUAL"}, "yield_note": {"Nota"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/editar", h.admin, edit, true, true), 200)
	rr = h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	for _, want := range []string{"Novo nome", "R$ 3.000,00", "R$ 3.420,00", `datetime="2026-03-01"`, `data-investment-search`} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("list missing %s", want)
		}
	}
	values = url.Values{"final_amount": {"R$ 3.500,01"}, "redeemed_date": {"2026-04-01"}, "final_amount_cents": {"999"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/resgatar", h.admin, values, true, true), 200)
	if err := h.db.QueryRow(`SELECT amount_cents FROM investment_valuations WHERE investment_id=? ORDER BY valuation_date DESC LIMIT 1`, id).Scan(&amount); err != nil || amount != 350001 {
		t.Fatalf("final %d %v", amount, err)
	}
	rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if strings.Contains(rr.Body.String(), `data-investment-open=`) || !strings.Contains(rr.Body.String(), "Encerrada") {
		t.Fatal("closed investment exposes active actions")
	}
	if after := investmentI12LedgerState(t, h); before != after {
		t.Fatalf("ledger/balances changed %s -> %s", before, after)
	}
}

// Browser homologation uses only temporary in-memory fixture data. All browser
// POSTs are rejected; the HTTP tests above cover successful mutations.
func TestInvestimentosI12BrowserFixture(t *testing.T) {
	if os.Getenv("CONTABASE_I12_BROWSER_FIXTURE") != "1" {
		t.Skip("interactive I1.2 browser fixture")
	}
	h := newInvestimentosRoutesHarness(t)
	form := investmentI11Form(t, h)
	form.Set("name", "IPCA+ 2032")
	form.Set("principal_amount", "3000")
	form.Set("institution", "Itaú")
	form.Set("kind", "Tesouro Direto")
	form.Set("maturity_date", "2032-10-12")
	rr := h.request(t, "POST", "/investimentos", h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 303)
	id := strings.TrimSuffix(strings.TrimPrefix(rr.Header().Get("Location"), "/investimentos/"), "?created=1")
	for _, p := range []struct{ date, amount string }{{"2026-05-10", "300000"}, {"2026-06-10", "304000"}, {"2026-07-10", "312000"}, {"2026-08-10", "320000"}, {"2026-09-10", "330000"}, {"2026-10-10", "342000"}} {
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/valuations", h.admin, url.Values{"valuation_date": {p.date}, "amount_cents": {p.amount}}, true, false), 303)
	}
	h.create(t, "CDB sem posição")
	before := investmentI11FinancialState(t, h)
	finished := make(chan struct{})
	var once sync.Once
	h.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("../../assets"))))
	h.mux.HandleFunc("/__i12/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: h.admin, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/investimentos", 303)
	})
	h.mux.HandleFunc("/__i12/finish", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(finished) }); w.WriteHeader(200) })
	server := httptest.NewServer(securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, "Homologação somente leitura: nenhuma operação foi executada.", 400)
			return
		}
		h.mux.ServeHTTP(w, r)
	})))
	defer server.Close()
	fmt.Println("I12_BROWSER_URL=" + server.URL + "/__i12/start")
	select {
	case <-finished:
	case <-time.After(12 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	if after := investmentI11FinancialState(t, h); before != after {
		t.Fatal("browser changed fixture financial state")
	}
}
