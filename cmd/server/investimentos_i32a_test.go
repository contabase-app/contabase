package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

func seedInvestimentosI32A(t *testing.T) *investimentosRoutesHarness {
	t.Helper()
	h := newInvestimentosI22Harness(t)
	h.exec(t, `UPDATE accounts SET current_balance=100000 WHERE id='inv-a' AND workspace_id='workspace-rbac'`)
	h.exec(t, `INSERT INTO accounts(id,workspace_id,name,type,current_balance) VALUES('inv-other','workspace-rbac','Outra corretora','INVESTMENT',45000)`)
	h.exec(t, `UPDATE accounts SET archived_at=NULL WHERE id='archived-a' AND workspace_id='workspace-rbac'`)
	for _, item := range []struct {
		name, account, date, operation string
		amount                         int64
	}{
		{"Aplicação conhecida", "inv-a", "2026-02-01", "", 200000},
		{"Outra aplicação", "inv-a", "2026-04-01", "", 90000},
		{"Sem posição", "inv-a", "", "", 0},
		{"Compra posterior", "inv-a", "2026-02-01", "purchase", 120000},
		{"Retirada posterior", "inv-a", "2026-02-01", "withdrawal", 110000},
		{"Data ambígua", "inv-a", "2026-03-15", "withdrawal", 130000},
		{"Aplicação em outra conta", "inv-other", "2026-03-01", "", 50000},
		{"Aplicação mantida em arquivada", "archived-a", "2026-02-15", "", 30000},
	} {
		form := investimentosRouteForm()
		form.Set("name", item.name)
		form.Set("account_id", item.account)
		rr := h.request(t, "POST", "/investimentos", h.admin, form, true, false)
		requireInvestimentosStatus(t, rr, http.StatusSeeOther)
		id := strings.TrimSuffix(strings.TrimPrefix(rr.Header().Get("Location"), "/investimentos/"), "?created=1")
		if item.date != "" {
			h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES(?,'workspace-rbac',?,?,?,'admin-user')`, "position:"+id, id, item.date, item.amount)
		}
		switch item.operation {
		case "purchase":
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, investmentI21Form(), true, false), 303)
		case "withdrawal":
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/retiradas", h.admin, investmentI22Form("initial:"+id), true, false), 303)
		}
	}
	h.exec(t, `UPDATE accounts SET archived_at=100 WHERE id='archived-a' AND workspace_id='workspace-rbac'`)
	foreign, err := services.CreateInvestment(context.Background(), h.base.WriterDB, services.CreateInvestmentInput{WorkspaceID: "workspace-b", AccountID: "inv-b", CreatedBy: "admin-user", Name: "Aplicação sigilosa estrangeira", Kind: "CDB", PrincipalCents: 100000, TaxRegime: models.InvestmentTaxRegimeManual, AppliedDate: "2026-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES('foreign-position','workspace-b',?,'2026-02-01',999999999,'admin-user')`, foreign.ID)
	h.mux.HandleFunc("/", withAuth(h.service, h.signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !hasPermission(ctx.Role, permDashboardRead) {
			respondForbidden(w, r)
			return
		}
		data := handlers.BuildDashboardData(h.db, ctx.UserID, ctx.ActiveWorkspaceID)
		if err := h.base.Templates.ExecuteTemplate(w, "dashboard-page", data); err != nil {
			t.Error(err)
		}
	}))
	bindConfig := func(ctx authContext) handlers.ConfiguracoesHandler {
		return handlers.ConfiguracoesHandler{DB: h.db, Templates: h.base.Templates, UserID: ctx.UserID, WorkspaceID: ctx.ActiveWorkspaceID, ActorRole: ctx.Role, CanConfigRead: hasPermission(ctx.Role, permConfigRead), CanConfigWrite: hasPermission(ctx.Role, permConfigWrite)}
	}
	h.mux.HandleFunc("/configuracoes/contas", withAuth(h.service, h.signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !hasPermission(ctx.Role, permConfigRead) {
			respondForbidden(w, r)
			return
		}
		config := bindConfig(ctx)
		config.HandleConfiguracoesSection(w, r, "contas")
	}))
	h.mux.HandleFunc("GET /configuracoes/contas/{id}", withAuth(h.service, h.signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !hasPermission(ctx.Role, permConfigRead) {
			respondForbidden(w, r)
			return
		}
		config := bindConfig(ctx)
		config.HandleContasView(w, r, r.PathValue("id"))
	}))
	return h
}

func TestInvestimentosI32AAccountsDashboardReadOnlyAndCoverage(t *testing.T) {
	h := seedInvestimentosI32A(t)
	before := investmentI11FinancialState(t, h)
	for _, path := range []string{"/", "/configuracoes/contas", "/configuracoes/contas/inv-a"} {
		for _, htmx := range []bool{false, true} {
			rr := h.request(t, "GET", path, h.admin, nil, false, htmx)
			requireInvestimentosStatus(t, rr, 200)
			body := html.UnescapeString(rr.Body.String())
			if strings.Contains(body, "Aplicação sigilosa estrangeira") || strings.Contains(body, "9.999.999,99") {
				t.Fatal("foreign workspace leaked")
			}
			if path == "/" {
				for _, unwanted := range []string{"Posição aplicada conhecida", "Patrimônio aplicado conhecido", "data-investment-patrimony", "R$ 2.900,00", "R$ 3.700,00", "Parcial — caixa livre da corretora ainda não conciliado", "4 aplicação(ões) sem posição válida", "Não representam patrimônio total nem dinheiro livre comprovado", "Arquivada A: conta arquivada com aplicações mantidas"} {
					if strings.Contains(body, unwanted) {
						t.Fatalf("dashboard still exposes patrimonial content %q", unwanted)
					}
				}
				for _, want := range []string{"Saldo financeiro", "Investimento A", "Outra corretora", "R$ 1.000,00", "R$ 450,00", "Saldo total", "Reservado", "Disponível"} {
					if !strings.Contains(body, want) {
						t.Fatalf("dashboard missing %q", want)
					}
				}
				if got := strings.Count(body, "Saldo financeiro"); got != 2 {
					t.Fatalf("dashboard financial balance identifier rendered %d times, want 2 investment accounts", got)
				}
				continue
			}
			for _, want := range []string{"Posição aplicada conhecida", "Saldo financeiro", "R$ 2.900,00", "Parcial — caixa livre da corretora ainda não conciliado", "4 aplicação(ões) sem posição válida", "Não representam patrimônio total nem dinheiro livre comprovado"} {
				if !strings.Contains(body, want) {
					t.Fatalf("%s missing %q", path, want)
				}
			}
			for _, date := range []string{`datetime="2026-02-01"`, `datetime="2026-04-01"`} {
				if !strings.Contains(body, date) {
					t.Fatalf("%s lost independent position date %s", path, date)
				}
			}
			if path == "/configuracoes/contas" && !strings.Contains(body, "conta(s) arquivada(s) com aplicações mantidas") {
				t.Fatal("archived warning hidden inside collapsed details")
			}
		}
	}
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("UI reads changed finance or positions")
	}
}

func TestInvestimentosI32AExistingDashboardAndAccessPreserved(t *testing.T) {
	h := seedInvestimentosI32A(t)
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "false")
	before := handlers.BuildDashboardData(h.db, "admin-user", "workspace-rbac")
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "true")
	after := handlers.BuildDashboardData(h.db, "admin-user", "workspace-rbac")
	if after.InvestmentPatrimony == nil {
		t.Fatal("authorized projection missing")
	}
	after.InvestmentPatrimony = nil
	after.Features = before.Features
	for i := range after.Accounts {
		after.Accounts[i].InvestmentPatrimony = nil
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("existing dashboard indicators changed: before=%+v after=%+v", before, after)
	}
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		paths := []string{"/"}
		if user == "manager-user" {
			paths = append(paths, "/configuracoes/contas", "/configuracoes/contas/inv-a")
		}
		for _, path := range paths {
			rr := h.request(t, "GET", path, session, nil, false, false)
			requireInvestimentosStatus(t, rr, 200)
			if strings.Contains(rr.Body.String(), "data-investment-patrimony") || strings.Contains(rr.Body.String(), "Aplicação conhecida") {
				t.Fatalf("%s gained investment visibility on %s", user, path)
			}
		}
	}
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "false")
	for _, path := range []string{"/", "/configuracoes/contas"} {
		rr := h.request(t, "GET", path, h.admin, nil, false, false)
		requireInvestimentosStatus(t, rr, 200)
		if strings.Contains(rr.Body.String(), "data-investment-patrimony") {
			t.Fatal("disabled feature leaked projection")
		}
	}
}

func TestInvestimentosI32ABrowserFixture(t *testing.T) {
	if os.Getenv("CONTABASE_I32A_BROWSER_FIXTURE") != "1" {
		t.Skip("interactive I3.2A disposable browser fixture")
	}
	h := seedInvestimentosI32A(t)
	before := investmentI11FinancialState(t, h)
	finished := make(chan struct{})
	var once sync.Once
	h.mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("../../assets"))))
	h.mux.HandleFunc("/__i32a/start", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: h.admin, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/", 303)
	})
	h.mux.HandleFunc("/__i32a/finish", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(finished) })
		w.WriteHeader(200)
	})
	server := httptest.NewServer(securityHeaders(h.mux))
	defer server.Close()
	fmt.Println("I32A_BROWSER_URL=" + server.URL + "/__i32a/start")
	select {
	case <-finished:
	case <-time.After(10 * time.Minute):
		t.Fatal("browser fixture timed out")
	}
	if before != investmentI11FinancialState(t, h) {
		t.Fatal("browser changed financial state")
	}
}
