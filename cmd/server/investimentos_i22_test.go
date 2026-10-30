package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/google/uuid"
)

// Reuse the authenticated fixture's seeds; clone only its disposable memory DB
// into a dormant runtime owner. Routes then use the real Immediate writer.
func newInvestimentosI22Harness(t *testing.T) *investimentosRoutesHarness {
	t.Helper()
	h := newInvestimentosRoutesHarness(t)
	path := filepath.Join(t.TempDir(), "withdrawals.db")
	h.exec(t, `VACUUM INTO ?`, path)
	pools, err := database.OpenRuntimeDBs(path)
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

func investmentI22Form(ids ...string) url.Values {
	form := url.Values{"request_id": {uuid.NewString()}, "withdrawal_date": {"2026-03-15"}, "mode": {"PARTIAL"}, "received_amount": {"100,00"}, "purchase_id": ids}
	if len(ids) > 1 {
		form.Set("received_amount", "200,00")
		for _, id := range ids {
			form.Set("received_"+id, "100,00")
		}
	}
	return form
}

func TestInvestimentosI22RoutesSecurityValidationAndReplay(t *testing.T) {
	h := newInvestimentosI22Harness(t)
	id := h.create(t, "Withdrawal security")
	initial := "initial:" + id
	path := "/investimentos/" + id + "/retiradas"
	before := investmentI11FinancialState(t, h)
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		for _, hx := range []bool{false, true} {
			requireInvestimentosStatus(t, h.request(t, "POST", path, session, investmentI22Form(initial), true, hx), 403)
		}
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, "", investmentI22Form(initial), true, false), 303)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI22Form(initial), false, true), 403)
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		requireInvestimentosStatus(t, h.request(t, method, path, h.admin, investmentI22Form(initial), true, false), 405)
	}
	for _, suffix := range []string{"/", "/extra"} {
		requireInvestimentosStatus(t, h.request(t, "POST", path+suffix, h.admin, investmentI22Form(initial), true, false), 404)
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.session(t, "admin-user", "workspace-b"), investmentI22Form(initial), true, false), 404)
	other := h.create(t, "Other investment")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI22Form("initial:"+other), true, true), 404)
	for _, field := range []string{"received_amount", "received_" + initial, "principal_" + initial} {
		for _, amount := range []string{"-1", "300.00", "300,001", "92233720368547758,08"} {
			form := investmentI22Form(initial)
			form.Set(field, amount)
			requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, true), 400)
		}
	}
	for _, fields := range []url.Values{{"withdrawal_date": {"2026-02-31"}}, {"withdrawal_date": {"2025-01-01"}}, {"request_id": {""}}, {"mode": {"UNKNOWN"}}, {"purchase_id": {}}, {"received_amount": {"0"}}} {
		form := investmentI22Form(initial)
		for key, value := range fields {
			form[key] = value
		}
		requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, true), 400)
	}
	form := investmentI22Form(initial)
	form.Set("principal_"+initial, "300,001")
	rr := h.request(t, "POST", path, h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 400)
	for _, want := range []string{"data-investment-reopen", `value="300,001"`, `value="` + form.Get("request_id") + `"`, "principal baixado"} {
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("native retry lost %q", want)
		}
	}
	form = investmentI22Form(initial)
	form.Set("workspace_id", "workspace-b")
	form.Set("investment_id", other)
	form.Set("created_by", "regular-user")
	rr = h.request(t, "POST", path, h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Redirect") != "/investimentos/"+id+"?withdrawn=PARTIAL" {
		t.Fatal("missing partial feedback")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, false), 303)
	var count int
	var ws, author string
	if err := h.db.QueryRow(`SELECT COUNT(*),workspace_id,created_by FROM investment_withdrawals`).Scan(&count, &ws, &author); err != nil || count != 1 || ws != "workspace-rbac" || author != "admin-user" {
		t.Fatalf("authority/replay %d %s %s %v", count, ws, author, err)
	}
	form.Set("received_amount", "200,00")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, true), 409)
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("withdrawals changed positions or financial state")
	}
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "")
	h.mux = http.NewServeMux()
	registerInvestimentosRoutes(h.mux, h.service, h.signer, nil)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI22Form(initial), true, true), 404)
}

func TestInvestimentosI22HistoryAllocationStalePositionsAndClosure(t *testing.T) {
	h := newInvestimentosI22Harness(t)
	id := h.create(t, "Withdrawal history")
	initial := "initial:" + id
	h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('position','workspace-rbac',?,'2026-03-02',310000,'admin-user')`, id)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, investmentI21Form(), true, false), 303)
	var second string
	if err := h.db.QueryRow(`SELECT id FROM investment_purchases WHERE investment_id=? AND is_initial=0`, id).Scan(&second); err != nil {
		t.Fatal(err)
	}
	before := investmentI11FinancialState(t, h)
	path := "/investimentos/" + id + "/retiradas"
	form := investmentI22Form(initial, second)
	form.Set("received_"+second, "99,99")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, true), 400)
	form.Set("received_"+second, "100,00")
	form.Set("principal_"+initial, "80,00")
	form.Set("note", "<script>receipt</script>")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, false), 303)
	rr := h.request(t, "GET", "/investimentos/"+id+"?withdrawn=PARTIAL", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	body := html.UnescapeString(rr.Body.String())
	for _, want := range []string{"Retirada parcial registrada", "Retiradas", "Total recebido em retiradas", "R$ 200,00", "2 em carteira", "Desatualizada em relação às operações", "Principal baixado: ", ">R$ 80,00<", ">Desconhecido<", "Principal remanescente: ", "Não registra retirada nem movimenta dinheiro.", `name="csrf_token"`, `data-investment-money="zero"`, `hx-sync="this:drop"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(rr.Body.String(), "<script>receipt</script>") || strings.Contains(body, "+ R$ 100,00") {
		t.Fatal("unsafe note or fictitious gain")
	}
	list := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, list, 200)
	if !strings.Contains(list.Body.String(), "fora da soma") {
		t.Fatal("withdrawal missing from stale consolidation")
	}
	form = investmentI22Form(initial, second)
	form.Set("mode", "TOTAL")
	form.Set("withdrawal_date", "2026-03-16")
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, form, true, false), 303)
	rr = h.request(t, "GET", "/investimentos/"+id+"?withdrawn=TOTAL", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	body = html.UnescapeString(rr.Body.String())
	for _, want := range []string{"Retirada total registrada", "Nenhuma compra atualmente em carteira", "Integralmente retirada", "Finalizar acompanhamento", "Reaplicar", "R$ 3.100,00"} {
		if !strings.Contains(body, want) {
			t.Fatalf("total missing %q", want)
		}
	}
	if strings.Contains(body, `data-investment-open="investment-withdrawal-panel"`) {
		t.Fatal("no eligible purchase still offers withdrawal")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investmentI22Form(initial), true, true), 409)
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("withdrawal rewrote positions or finances")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, investmentI21Form(), true, false), 303)
	rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if !strings.Contains(rr.Body.String(), "1 em carteira") || !strings.Contains(rr.Body.String(), `data-investment-open="investment-withdrawal-panel"`) {
		t.Fatal("purchase did not reopen composition")
	}
}

func TestInvestimentosI22BrowserFixture(t *testing.T) {
	if os.Getenv("CONTABASE_I22_BROWSER_FIXTURE") != "1" {
		t.Skip("interactive I2.2 disposable browser fixture")
	}
	h := newInvestimentosI22Harness(t)
	id := h.create(t, "Tesouro IPCA+ 2032 · Homologação I2.2")
	form := investmentI21Form()
	form.Set("note_reference", "NC-002")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, form, true, false), 303)
	h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('browser-position','workspace-rbac',?,'2026-03-02',310000,'admin-user')`, id)
	before := investmentI11FinancialState(t, h)
	finished := make(chan struct{})
	var once sync.Once
	h.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("../../assets"))))
	h.mux.HandleFunc("/__i22/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: h.admin, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/investimentos/"+id, 303)
	})
	h.mux.HandleFunc("/__i22/finish", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(finished) }); w.WriteHeader(200) })
	server := httptest.NewServer(securityHeaders(h.mux))
	defer server.Close()
	fmt.Println("I22_BROWSER_URL=" + server.URL + "/__i22/start")
	select {
	case <-finished:
	case <-time.After(18 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("browser changed financial state or positions")
	}
	var events, allocations, closures int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_withdrawals WHERE investment_id=?`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(closes_purchase),0) FROM investment_withdrawal_allocations WHERE investment_id=?`, id).Scan(&allocations, &closures); err != nil {
		t.Fatal(err)
	}
	if events != 2 || allocations != 4 || closures != 2 {
		t.Fatalf("browser events=%d allocations=%d closures=%d", events, allocations, closures)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM investments WHERE id=?`, id).Scan(&status); err != nil || status != "ATIVA" {
		t.Fatalf("browser finalized followup: %s %v", status, err)
	}
}
