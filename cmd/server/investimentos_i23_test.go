package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reuse I2.2's dormant runtime and real routes; all seeds are disposable.
func seedInvestimentosI23(t *testing.T) (*investimentosRoutesHarness, map[string]string) {
	t.Helper()
	h := newInvestimentosI22Harness(t)
	ids := make(map[string]string)
	for _, scenario := range []struct {
		name   string
		amount int64
	}{
		{"Positiva", 135000}, {"Negativa", 125000}, {"Indisponível", 0}, {"Data ambígua", 135000},
	} {
		id := h.create(t, "I2.3 · "+scenario.name)
		ids[scenario.name] = id
		h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES(?, 'workspace-rbac',?,'2026-02-01',100000,'admin-user')`, "start:"+id, id)
		if scenario.name == "Indisponível" {
			continue
		}
		form := investmentI21Form()
		form.Set("principal_amount", "500,00")
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, form, true, false), 303)
		withdrawal := investmentI22Form("initial:" + id)
		withdrawal.Set("received_amount", "200,00")
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/retiradas", h.admin, withdrawal, true, false), 303)
		date := "2026-04-01"
		if scenario.name == "Data ambígua" {
			date = "2026-03-15"
		}
		h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES(?,'workspace-rbac',?,?,?,'admin-user')`, "end:"+id, id, date, scenario.amount)
	}
	return h, ids
}

func TestInvestimentosI23AdjustedVariationRenderingReadOnly(t *testing.T) {
	h, ids := seedInvestimentosI23(t)
	before := investmentI11FinancialState(t, h)
	for _, tc := range []struct {
		name, value string
	}{
		{"Positiva", "+ R$ 50,00"}, {"Negativa", "- R$ 50,00"},
		{"Indisponível", "Registre duas posições para comparar"}, {"Data ambígua", "Existem operações na mesma data de uma posição"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := h.request(t, "GET", "/investimentos/"+ids[tc.name], h.admin, nil, false, false)
			requireInvestimentosStatus(t, rr, 200)
			body := html.UnescapeString(rr.Body.String())
			start := strings.Index(body, `<section id="investment-adjusted-variation"`)
			if start < 0 {
				t.Fatal("adjusted variation section missing")
			}
			end := strings.Index(body[start:], "</section>")
			if end < 0 {
				t.Fatal("adjusted variation section not closed")
			}
			section := body[start : start+end]
			for _, want := range []string{tc.value, "Variação ajustada pelas operações registradas", "Considera as posições informadas, compras e retiradas registradas no período. Não representa rentabilidade líquida."} {
				if !strings.Contains(section, want) {
					t.Fatalf("variation missing %q: %s", want, section)
				}
			}
			if tc.name == "Positiva" || tc.name == "Negativa" {
				for _, date := range []string{`datetime="2026-02-01">01/02/2026</time>`, `datetime="2026-04-01">01/04/2026</time>`} {
					if !strings.Contains(section, date) {
						t.Fatalf("actual position date missing: %s", section)
					}
				}
			} else if !strings.Contains(section, "Variação indisponível") || strings.Contains(section, "R$") {
				t.Fatalf("unavailable variation exposed a numeric value: %s", section)
			}
			if strings.Contains(section, "%") {
				t.Fatal("adjusted variation exposed percentage")
			}
		})
	}
	foreign := h.session(t, "admin-user", "workspace-b")
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+ids["Positiva"], foreign, nil, false, false), 404)
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("detail reads changed positions or financial state")
	}
}

func TestInvestimentosI23BrowserFixture(t *testing.T) {
	if os.Getenv("CONTABASE_I23_BROWSER_FIXTURE") != "1" {
		t.Skip("interactive I2.3 disposable browser fixture")
	}
	h, ids := seedInvestimentosI23(t)
	before := investmentI11FinancialState(t, h)
	finished := make(chan struct{})
	var once sync.Once
	h.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("../../assets"))))
	h.mux.HandleFunc("/__i23/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: h.admin, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/investimentos/"+ids["Positiva"], 303)
	})
	h.mux.HandleFunc("/__i23/finish", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(finished) })
		w.WriteHeader(200)
	})
	server := httptest.NewServer(securityHeaders(h.mux))
	defer server.Close()
	fmt.Println("I23_BROWSER_URL=" + server.URL + "/__i23/start")
	select {
	case <-finished:
	case <-time.After(10 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("browser changed positions or financial state")
	}
}
