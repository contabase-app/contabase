package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"html/template"
	"math"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"errors"
	"github.com/contabase-app/contabase/internal/database"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

func TestInvestimentosI1ConsolidationHistoryAndReadOnly(t *testing.T) {
	h, other := seedInvestmentHandlerDB(t)
	h.Templates = template.Must(template.New("investimentos-i1").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"upper":     strings.ToUpper,
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/investimentos.html"),
		resolveTemplatePath(t, "templates/pages/investimento_detalhe.html"),
		resolveTemplatePath(t, "templates/components/investimento_form.html"),
		resolveTemplatePath(t, "templates/components/investimento_valuation_form.html"),
		resolveTemplatePath(t, "templates/components/investimento_retirada_form.html"),
		resolveTemplatePath(t, "templates/components/investimento_cancelamento.html"),
		resolveTemplatePath(t, "templates/components/investimento_estorno.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
	))
	other.Templates = h.Templates
	h.CSRFToken = "i1-csrf"
	ctx := context.Background()
	create := func(handler *InvestimentosHandler, name string) models.Investment {
		values := investmentFormValues()
		values["name"] = []string{name}
		if handler == other {
			values["account_id"] = []string{"acc-inv-b"}
		}
		return mustHandlerCreate(t, handler, values)
	}
	position := func(handler *InvestimentosHandler, inv models.Investment, date string, cents int64) {
		_, err := services.UpsertInvestmentValuation(ctx, h.WriterDB, services.UpsertInvestmentValuationInput{
			WorkspaceID: handler.WorkspaceID, InvestmentID: inv.ID, ActingUserID: handler.UserID,
			ValuationDate: date, AmountCents: cents,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	first := create(h, "I1 primeira")
	position(h, first, "2026-03-01", 110000)
	position(h, first, "2026-02-01", 800000) // inserted later, but an older civil date
	second := create(h, "I1 segunda")
	position(h, second, "2026-05-01", 50000)
	zero := create(h, "I1 zero conhecido")
	position(h, zero, "2026-04-01", 0)
	missing := create(h, "I1 sem posição")
	closed := create(h, "I1 encerrada")
	archived := create(h, "I1 arquivada")
	for _, inv := range []models.Investment{closed, archived} {
		if err := services.RedeemInvestment(ctx, h.WriterDB, services.RedeemInvestmentInput{
			WorkspaceID: h.WorkspaceID, InvestmentID: inv.ID, ActingUserID: h.UserID,
			RedeemedDate: "2026-06-01", FinalAmountCents: 900000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := services.ArchiveInvestment(ctx, h.WriterDB, services.ArchiveInvestmentInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: archived.ID, ActingUserID: h.UserID,
	}); err != nil {
		t.Fatal(err)
	}
	foreign := create(other, "I1 exclusivo B")
	position(other, foreign, "2026-08-01", 990000)
	execTestSQL(t, h.DB, `UPDATE accounts SET current_balance=72345 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	execTestSQL(t, h.DB, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status)
		VALUES ('i1-ledger', 'ws-a', 'user-a', 'acc-checking-a', 'EXPENSE', 125, 1768478400, 'I1 fixture', 'paid')`)
	before := queryScalarInvestments(t, h, `SELECT (SELECT COUNT(*) FROM transactions) || ':' ||
		(SELECT SUM(amount) FROM transactions) || ':' || group_concat(id || ':' || current_balance)
		FROM (SELECT id,current_balance FROM accounts ORDER BY id)`)
	beforePositions, err := services.GetInvestmentValuations(ctx, h.DB, h.WorkspaceID, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Every GET below must also succeed when SQLite rejects all writes.
	h.DB.SetMaxOpenConns(1)
	execTestSQL(t, h.DB, `PRAGMA query_only=ON`)
	positions, err := h.latestInvestmentPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := positions[foreign.ID]; leaked {
		t.Fatal("foreign workspace position leaked")
	}
	all, err := h.listInvestments(ctx, services.InvestmentListAll)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := summarizeInvestmentPositions(all, positions)
	if err != nil || summary.ActiveCount != 4 || summary.WithoutPosition != 1 || summary.KnownTotal != "R$ 1.600,00" || summary.LatestDate != "01/05/2026" {
		t.Fatalf("consolidation: %+v, %v", summary, err)
	}
	for _, filter := range []string{"ATIVAS", "TODAS", "RESGATADAS", "ARQUIVADAS"} {
		rr := httptest.NewRecorder()
		h.HandleInvestimentos(rr, httptest.NewRequest("GET", "/investimentos?status="+filter, nil))
		body := html.UnescapeString(rr.Body.String())
		for _, want := range []string{"Resumo das aplicações ativas", "R$ 1.600,00", `datetime="2026-05-01">01/05/2026</time>`, "Total parcial"} {
			if rr.Code != 200 || !strings.Contains(body, want) {
				t.Fatalf("filter %s missing %q: %d %s", filter, want, rr.Code, body)
			}
		}
		if strings.Contains(body, foreign.Name) || strings.Contains(body, "R$ 9.900,00") {
			t.Fatal("foreign data leaked in list")
		}
		if filter == "ATIVAS" && (!strings.Contains(body, `Em <time datetime="2026-03-01">01/03/2026</time>`) || !strings.Contains(body, "Sem posição registrada") || !strings.Contains(body, "R$ 0,00") || strings.Contains(body, closed.Name)) {
			t.Fatal("active positions, dates or closed exclusion incorrect")
		}
		if filter == "RESGATADAS" && !strings.Contains(body, "Encerrada") {
			t.Fatal("closed application not identified separately")
		}
	}
	rr := httptest.NewRecorder()
	h.HandleDetalheInvestimento(rr, httptest.NewRequest("GET", "/investimentos/"+first.ID, nil), first.ID)
	body := html.UnescapeString(rr.Body.String())
	if rr.Code != 200 || !strings.Contains(body, first.Name) || !strings.Contains(body, `Posição em <time datetime="2026-03-01">01/03/2026</time>`) || !strings.Contains(body, "R$ 1.100,00") {
		t.Fatalf("latest detail: %d %s", rr.Code, body)
	}
	historyHeading := regexp.MustCompile(`(?is)<h2\b[^>]*\sid\s*=\s*["']investment-history-title["'][^>]*>`).FindStringIndex(body)
	if historyHeading == nil {
		t.Fatal("investment history missing")
	}
	historyStart := strings.LastIndex(body[:historyHeading[0]], "<section")
	historyCloseOffset := strings.Index(body[historyHeading[1]:], "</section>")
	if historyStart < 0 || historyCloseOffset < 0 {
		t.Fatal("investment history section boundaries missing")
	}
	historyEnd := historyHeading[1] + historyCloseOffset + len("</section>")
	history := body[historyStart:historyEnd]
	if !strings.Contains(history, "Histórico e evolução das posições") {
		t.Fatal("investment history heading missing from its section")
	}
	olderPosition := `datetime="2026-02-01">01/02/2026</time>`
	newerPosition := `datetime="2026-03-01">01/03/2026</time>`
	if strings.Index(history, olderPosition) < 0 || strings.Index(history, olderPosition) >= strings.Index(history, newerPosition) || !strings.Contains(history, "R$ 8.000,00") || !strings.Contains(history, "R$ 1.100,00") {
		t.Fatal("recorded history is incomplete or out of order")
	}
	rr = httptest.NewRecorder()
	h.HandleDetalheInvestimento(rr, httptest.NewRequest("GET", "/investimentos/"+missing.ID, nil), missing.ID)
	body = html.UnescapeString(rr.Body.String())
	if rr.Code != 200 || !strings.Contains(body, "Sem posição registrada") || !strings.Contains(body, "Indisponível") || strings.Contains(body, "0,00%") || strings.Contains(body, "R$ 0,00") {
		t.Fatalf("missing position shown as known: %d %s", rr.Code, body)
	}
	rr = httptest.NewRecorder()
	other.HandleDetalheInvestimento(rr, httptest.NewRequest("GET", "/investimentos/"+first.ID, nil), first.ID)
	if rr.Code != 404 {
		t.Fatalf("foreign detail: %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	other.HandleInvestimentos(rr, httptest.NewRequest("GET", "/investimentos", nil))
	body = html.UnescapeString(rr.Body.String())
	if rr.Code != 200 || !strings.Contains(body, "R$ 9.900,00") || strings.Contains(body, first.Name) || strings.Contains(body, "R$ 1.600,00") {
		t.Fatalf("workspace B consolidation: %d %s", rr.Code, body)
	}
	afterPositions, err := services.GetInvestmentValuations(ctx, h.DB, h.WorkspaceID, first.ID)
	if err != nil || !reflect.DeepEqual(beforePositions, afterPositions) {
		t.Fatalf("GET changed recorded positions: %v", err)
	}
	after := queryScalarInvestments(t, h, `SELECT (SELECT COUNT(*) FROM transactions) || ':' ||
		(SELECT SUM(amount) FROM transactions) || ':' || group_concat(id || ':' || current_balance)
		FROM (SELECT id,current_balance FROM accounts ORDER BY id)`)
	if before != after {
		t.Fatalf("GET changed ledger or balances: %s -> %s", before, after)
	}
}

func TestInvestimentosI1MaturityCivilDates(t *testing.T) {
	// Same instant falls on different civil dates: use the app's configured
	// date, then compare calendar days, including a month boundary.
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	today := civildate.CivilDateAt(time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC), appTimeLocation())
	if today.String() != "2026-10-12" {
		t.Fatal("today shifted away from the application calendar")
	}
	for _, tc := range []struct{ date, status, want string }{
		{"2026-10-11", "ATIVA", "Vencida"},
		{"2026-10-12", "ATIVA", "Vencendo nos próximos 30 dias · Vence hoje"},
		{"2026-11-11", "ATIVA", "Vencendo nos próximos 30 dias"},
		{"2026-11-12", "ATIVA", "Sem vencimento próximo"},
		{"", "ATIVA", "Sem data de vencimento"},
		{"2026-10-11", "RESGATADA", "Encerrada"},
	} {
		inv := models.Investment{Status: tc.status}
		if tc.date != "" {
			inv.MaturityDate = &tc.date
		}
		got, err := investmentMaturityLabel(inv, today)
		if err != nil || got != tc.want {
			t.Fatalf("%s/%s: %q, %v", tc.date, tc.status, got, err)
		}
	}
}

func TestInvestimentosI1UnknownTotalAndOverflow(t *testing.T) {
	active := []models.Investment{{ID: "a", Status: "ATIVA"}, {ID: "b", Status: "ATIVA"}}
	summary, err := summarizeInvestmentPositions(active, nil)
	if err != nil || summary.WithoutPosition != 2 || summary.KnownTotal != "Sem posição registrada" || summary.LatestDate != "Indisponível" {
		t.Fatalf("unknown positions: %+v %v", summary, err)
	}
	_, err = summarizeInvestmentPositions(active, map[string]models.InvestmentValuation{
		"a": {AmountCents: math.MaxInt64, ValuationDate: "2026-02-01"},
		"b": {AmountCents: 1, ValuationDate: "2026-02-01"},
	})
	if err == nil {
		t.Fatal("position total overflow must not wrap into a false financial value")
	}
}

// --- fixtures -----------------------------------------------------------------

func seedInvestmentHandlerDB(t *testing.T) (*InvestimentosHandler, *InvestimentosHandler) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('user-a', 'Owner A', 'a@example.test', 'hash')`)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('user-b', 'Owner B', 'b@example.test', 'hash')`)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name) VALUES ('ws-a', 'WS A')`)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name) VALUES ('ws-b', 'WS B')`)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-inv-a', 'ws-a', 'Corretora A', 'INVESTMENT')`)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-checking-a', 'ws-a', 'Banco A', 'CHECKING')`)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-inv-b', 'ws-b', 'Corretora B', 'INVESTMENT')`)
	handlerA := &InvestimentosHandler{DB: db, WriterDB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	handlerB := &InvestimentosHandler{DB: db, WriterDB: db, WorkspaceID: "ws-b", UserID: "user-b"}
	return handlerA, handlerB
}

func investmentFormValues() map[string][]string {
	return map[string][]string{
		"account_id":       {"acc-inv-a"},
		"name":             {"CDB Banco X"},
		"institution":      {"Banco X"},
		"kind":             {"CDB"},
		"principal_amount": {"1000,00"},
		"request_id":       {uuid.NewString()},
		"tax_regime":       {"IR_REGRESSIVO"},
		"applied_date":     {"2026-01-15"},
		"yield_note":       {"100% CDI"},
	}
}

// --- parse / create -------------------------------------------------------------

func TestInvestimentosParseCreateFormUsesInternalContext(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	values := investmentFormValues()
	// Payload nunca é fonte de autoridade para workspace/autor.
	values["workspace_id"] = []string{"ws-b"}
	values["created_by"] = []string{"user-b"}
	values["status"] = []string{"RESGATADA"}

	req := httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(values)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	input, err := h.parseInvestmentCreateForm(req)
	if err != nil {
		t.Fatal(err)
	}
	if input.WorkspaceID != "ws-a" || input.CreatedBy != "user-a" {
		t.Fatalf("workspace/autor devem vir do contexto interno: %+v", input)
	}
	if input.PrincipalCents != 100000 || input.AppliedDate != "2026-01-15" {
		t.Fatalf("input divergente: %+v", input)
	}
	if input.MaturityDate != nil {
		t.Fatalf("maturity ausente deve ser nil: %v", *input.MaturityDate)
	}

	values["maturity_date"] = []string{"2027-06-30"}
	values["principal_amount"] = []string{"1,23"}
	req = httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(values)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	input, err = h.parseInvestmentCreateForm(req)
	if err != nil {
		t.Fatal(err)
	}
	if input.PrincipalCents != 123 || input.MaturityDate == nil || *input.MaturityDate != "2027-06-30" {
		t.Fatalf("maturity/principal não parseados: %+v", input)
	}

	values["principal_amount"] = []string{"300,001"}
	req = httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(values)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := h.parseInvestmentCreateForm(req); !errors.Is(err, services.ErrInvestmentValidation) {
		t.Fatalf("frações além de centavos devem falhar como validação, got %v", err)
	}
}

func TestInvestimentosCreateFlowAndAccountRules(t *testing.T) {
	h, handlerB := seedInvestmentHandlerDB(t)

	req := httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(investmentFormValues())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created, err := h.createInvestment(req)
	if err != nil {
		t.Fatal(err)
	}
	var createdBy, workspace, status string
	if err := h.DB.QueryRow(`SELECT created_by, workspace_id, status FROM investments WHERE id = ?`, created.ID).
		Scan(&createdBy, &workspace, &status); err != nil {
		t.Fatal(err)
	}
	if createdBy != "user-a" || workspace != "ws-a" || status != "ATIVA" {
		t.Fatalf("lote persistido divergente: %s/%s/%s", createdBy, workspace, status)
	}

	// Conta CHECKING do próprio workspace: rejeitada.
	values := investmentFormValues()
	values["account_id"] = []string{"acc-checking-a"}
	req = httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(values)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := h.createInvestment(req); !errors.Is(err, services.ErrInvestmentInvalidAccount) {
		t.Fatalf("conta CHECKING: err = %v", err)
	}

	// Conta INVESTMENT de outro workspace: rejeitada mesmo com conta válida existindo.
	values = investmentFormValues()
	values["account_id"] = []string{"acc-inv-b"}
	req = httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(values)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := h.createInvestment(req); !errors.Is(err, services.ErrInvestmentInvalidAccount) {
		t.Fatalf("conta cross-tenant: err = %v", err)
	}

	// Workspace B cria o próprio lote sem interferir em ws-a.
	valuesB := investmentFormValues()
	valuesB["account_id"] = []string{"acc-inv-b"}
	reqB := httptest.NewRequest(http.MethodPost, "/investimentos", strings.NewReader(formValuesEncode(valuesB)))
	reqB.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := handlerB.createInvestment(reqB); err != nil {
		t.Fatal(err)
	}
	var countA int
	if err := h.DB.QueryRow(`SELECT COUNT(*) FROM investments WHERE workspace_id = 'ws-a'`).Scan(&countA); err != nil {
		t.Fatal(err)
	}
	if countA != 1 {
		t.Fatalf("criação em ws-b vazou em ws-a: %d lotes", countA)
	}
}

// --- update ---------------------------------------------------------------------

func TestInvestimentosUpdateFlow(t *testing.T) {
	h, handlerB := seedInvestmentHandlerDB(t)
	created := mustHandlerCreate(t, h, investmentFormValues())

	updateValues := func() map[string][]string {
		return map[string][]string{
			"name":          {"CDB Banco Y"},
			"institution":   {"Banco Y"},
			"kind":          {"CDB pós-fixado"},
			"tax_regime":    {"ISENTO_PF"},
			"maturity_date": {"2027-06-30"},
			"yield_note":    {"105% CDI"},
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/investimentos/"+created.ID, strings.NewReader(formValuesEncode(updateValues())))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := h.updateInvestment(req, created.ID); err != nil {
		t.Fatal(err)
	}
	var name, regime string
	if err := h.DB.QueryRow(`SELECT name, tax_regime FROM investments WHERE id = ?`, created.ID).Scan(&name, &regime); err != nil {
		t.Fatal(err)
	}
	if name != "CDB Banco Y" || regime != "ISENTO_PF" {
		t.Fatalf("update não aplicado: %s/%s", name, regime)
	}

	// Handler de outro workspace não altera (NotFound sem vazamento).
	reqB := httptest.NewRequest(http.MethodPost, "/investimentos/"+created.ID, strings.NewReader(formValuesEncode(updateValues())))
	reqB.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := handlerB.updateInvestment(reqB, created.ID); !errors.Is(err, services.ErrInvestmentNotFound) {
		t.Fatalf("cross-tenant: err = %v", err)
	}
	if name := queryScalarInvestments(t, h, `SELECT name FROM investments WHERE id = '`+created.ID+`'`); name != "CDB Banco Y" {
		t.Fatalf("tentativa cross-tenant alterou estado: %s", name)
	}
}

// --- detail / list / error mapping ----------------------------------------------

func TestInvestmentReturnFormattingLargeValues(t *testing.T) {
	if got := formatInvestmentBasisPoints(10000); got != "+100,00%" {
		t.Fatalf("percentual grande: %q", got)
	}
	if got := formatInvestmentBasisPoints(-50); got != "-0,50%" {
		t.Fatalf("percentual negativo pequeno: %q", got)
	}
	if got := formatCurrencyLabel(8_000_000_000_000_000_000); got != "R$ 80.000.000.000.000.000,00" {
		t.Fatalf("moeda grande: %q", got)
	}
}

func TestInvestimentosDetailAndListIsolation(t *testing.T) {
	h, handlerB := seedInvestmentHandlerDB(t)
	created := mustHandlerCreate(t, h, investmentFormValues())
	ctx := context.Background()

	detail, valuations, err := h.investmentDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Investment.ID != created.ID || detail.TotalPurchasesCents != 100000 || detail.CurrentCents != 0 || !detail.CurrentIsPrincipal || detail.DifferenceAvailable {
		t.Fatalf("detail sem valuation: %+v", detail)
	}
	if len(valuations) != 0 {
		t.Fatalf("histórico deveria estar vazio: %+v", valuations)
	}

	_, _, err = handlerB.investmentDetail(ctx, created.ID)
	if !errors.Is(err, services.ErrInvestmentNotFound) {
		t.Fatalf("detail cross-tenant: err = %v", err)
	}

	// Uma valuation muda o current e o flag de fallback.
	_, err = h.DB.Exec(`INSERT INTO investment_valuations (id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at)
		VALUES ('hval-1', 'ws-a', ?, '2026-03-01', 110000, 'user-a', 200)`, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, valuations, err = h.investmentDetail(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CurrentCents != 110000 || detail.CurrentIsPrincipal || len(valuations) != 1 {
		t.Fatalf("detail com valuation: %+v/%+v", detail, valuations)
	}

	// Listagem só enxerga o próprio workspace.
	if _, err := handlerB.createInvestment(formRequest("/investimentos", map[string][]string{
		"account_id":       {"acc-inv-b"},
		"name":             {"LCI Banco B"},
		"kind":             {"LCI"},
		"principal_amount": {"500,00"},
		"request_id":       {uuid.NewString()},
		"tax_regime":       {"ISENTO_PF"},
		"applied_date":     {"2026-02-01"},
	})); err != nil {
		t.Fatal(err)
	}
	listA, err := h.listInvestments(ctx, services.InvestmentListActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].ID != created.ID {
		t.Fatalf("lista de ws-a: %+v", listA)
	}
	listB, err := handlerB.listInvestments(ctx, services.InvestmentListActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || listB[0].WorkspaceID != "ws-b" {
		t.Fatalf("lista de ws-b: %+v", listB)
	}
}

func TestMapInvestmentErrorStatuses(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	assertStatus := func(err error, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/investimentos", nil)
		mapInvestmentError(rec, req, err)
		if rec.Code != want {
			t.Fatalf("err %v → status %d, want %d", err, rec.Code, want)
		}
	}
	assertStatus(services.ErrInvestmentNotFound, http.StatusNotFound)
	assertStatus(services.ErrInvestmentInvalidAccount, http.StatusBadRequest)
	assertStatus(services.ErrInvestmentInvalidStatus, http.StatusConflict)
	assertStatus(services.ErrInvestmentConflict, http.StatusConflict)
	validation := func() error {
		req := formRequest("/investimentos", map[string][]string{"principal_amount": {"abc"}})
		_, err := h.parseInvestmentCreateForm(req)
		return err
	}()
	assertStatus(validation, http.StatusBadRequest)
	// Erro genérico (ex.: trigger RAISE em corrida) falha fechado sem vazar SQL.
	assertStatus(errors.New("SQL logic error: invalid investment account"), http.StatusInternalServerError)
}

// --- helpers ---------------------------------------------------------------------

func mustHandlerCreate(t *testing.T, h *InvestimentosHandler, values map[string][]string) models.Investment {
	t.Helper()
	created, err := h.createInvestment(formRequest("/investimentos", values))
	if err != nil {
		t.Fatalf("create via handler: %v", err)
	}
	return created
}

func formRequest(path string, values map[string][]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(formValuesEncode(values)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func formValuesEncode(values map[string][]string) string {
	params := url.Values(values)
	return params.Encode()
}

func queryScalarInvestments(t *testing.T, h *InvestimentosHandler, query string) string {
	t.Helper()
	var value string
	if err := h.DB.QueryRow(query).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestInvestimentosHTTPNilWriterAndCancelledContext(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	created := mustHandlerCreate(t, h, investmentFormValues())
	for _, update := range []bool{false, true} {
		invoke := func(rr *httptest.ResponseRecorder, req *http.Request) {
			if update {
				h.HandleEditarInvestimento(rr, req, created.ID)
			} else {
				h.HandleCriarInvestimento(rr, req)
			}
		}
		writer := h.WriterDB
		h.WriterDB = nil
		rr := httptest.NewRecorder()
		// Invalid payload would yield 400 if parsing/service were reached.
		invoke(rr, formRequest("/investimentos", map[string][]string{"principal_amount": {"invalid"}}))
		if rr.Code != 500 {
			t.Fatalf("nil writer status=%d", rr.Code)
		}
		h.WriterDB = writer
		for _, deadline := range []bool{false, true} {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			req := formRequest("/investimentos", investmentFormValues()).WithContext(ctx)
			rr = httptest.NewRecorder()
			invoke(rr, req)
			cancel()
			if rr.Code != 408 {
				t.Fatalf("cancelled status=%d body=%q", rr.Code, rr.Body.String())
			}
		}
	}
	var count int
	if err := h.DB.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed writes changed rows: %d %v", count, err)
	}
}

func TestInvestimentosHTTPImmediateAcrossDiskPools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "investments.db")
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pools.Close()
	pools.Immediate.SetMaxOpenConns(1)
	pools.Immediate.SetMaxIdleConns(1)
	if _, err := pools.Immediate.Exec(`PRAGMA busy_timeout=25`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO users (id,name,email,password_hash) VALUES ('user-a','A','a@example.test','hash')`,
		`INSERT INTO workspaces (id,name) VALUES ('ws-a','A')`,
		`INSERT INTO accounts (id,workspace_id,name,type) VALUES ('acc-inv-a','ws-a','Investment','INVESTMENT')`,
	} {
		if _, err := pools.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	h := &InvestimentosHandler{DB: pools.DB, WriterDB: pools.Immediate, WorkspaceID: "ws-a", UserID: "user-a"}
	created := mustHandlerCreate(t, h, investmentFormValues())

	// A separate connection owns the writer reservation. WAL reads still work.
	competitor, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(25)")
	if err != nil {
		t.Fatal(err)
	}
	defer competitor.Close()
	blocker, err := competitor.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.Exec(`UPDATE investments SET name=name WHERE id=?`, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.investmentDetail(context.Background(), created.ID); err != nil {
		t.Fatalf("read acquired writer lock: %v", err)
	}
	if _, err := h.listInvestments(context.Background(), services.InvestmentListActive); err != nil {
		t.Fatalf("list acquired writer lock: %v", err)
	}

	// Invalid account / missing investment discriminate BEGIN IMMEDIATE from
	// deferred: a deferred transaction would read first and return 400/404.
	// The Immediate pool must fail on BEGIN with 503, before any such read.
	for _, tc := range []struct {
		name            string
		update, invalid bool
	}{
		{"create-valid", false, false}, {"create-invalid-account", false, true},
		{"update-valid", true, false}, {"update-missing-id", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := investmentFormValues()
			id := created.ID
			if tc.invalid {
				values["account_id"] = []string{"missing-account"}
				id = "missing-id"
			}
			req := formRequest("/investimentos", values)
			rr := httptest.NewRecorder()
			if tc.update {
				h.HandleEditarInvestimento(rr, req, id)
			} else {
				h.HandleCriarInvestimento(rr, req)
			}
			if rr.Code != 503 || rr.Header().Get("Retry-After") != "1" {
				t.Fatalf("writer must reserve before reads: %d %q headers=%v", rr.Code, rr.Body.String(), rr.Header())
			}
			if strings.Contains(rr.Body.String(), "SQLITE") || strings.Contains(rr.Body.String(), "database") {
				t.Fatal("raw database error leaked")
			}
		})
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}

	// A stale deferred read snapshot remains open while HTTP writes succeed on
	// the independent Immediate pool. They cannot borrow/upgrade this reader.
	snapshot, err := competitor.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Rollback()
	var oldName string
	if err := snapshot.QueryRow(`SELECT name FROM investments WHERE id=?`, created.ID).Scan(&oldName); err != nil {
		t.Fatal(err)
	}
	h.DB = nil // mutations must depend only on WriterDB
	values := investmentFormValues()
	values["name"] = []string{"Immediate edit"}
	rr := httptest.NewRecorder()
	h.HandleEditarInvestimento(rr, formRequest("/investimentos", values), created.ID)
	if rr.Code != 303 {
		t.Fatalf("edit failed: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.HandleCriarInvestimento(rr, formRequest("/investimentos", investmentFormValues()))
	if rr.Code != 303 {
		t.Fatalf("create failed: %d %s", rr.Code, rr.Body.String())
	}
	var staleName string
	if err := snapshot.QueryRow(`SELECT name FROM investments WHERE id=?`, created.ID).Scan(&staleName); err != nil || staleName != oldName {
		t.Fatalf("reader snapshot changed: %s %v", staleName, err)
	}
	snapshot.Rollback()
	fresh, err := services.GetInvestment(context.Background(), pools.DB, "ws-a", created.ID)
	if err != nil || fresh.Name != "Immediate edit" {
		t.Fatalf("write not committed: %+v %v", fresh, err)
	}
	var count int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("contention mutated state or write missing: %d %v", count, err)
	}
	// Reopen independently to prove persistence, not just in-memory visibility.
	reopened, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := services.GetInvestment(context.Background(), reopened, "ws-a", created.ID)
	if err != nil || persisted.Name != "Immediate edit" {
		t.Fatalf("persisted state: %+v %v", persisted, err)
	}
}

func TestInvestimentosHTTPUnexpectedWriteErrorIsGeneric(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	created := mustHandlerCreate(t, h, investmentFormValues())
	// Force a database error after the service's validation and reads.
	execTestSQL(t, h.DB, `CREATE TRIGGER investment_test_insert BEFORE INSERT ON investments BEGIN SELECT RAISE(ABORT, 'private SQL detail'); END`)
	execTestSQL(t, h.DB, `CREATE TRIGGER investment_test_update BEFORE UPDATE ON investments BEGIN SELECT RAISE(ABORT, 'private SQL detail'); END`)
	for _, update := range []bool{false, true} {
		rr := httptest.NewRecorder()
		req := formRequest("/investimentos", investmentFormValues())
		if update {
			h.HandleEditarInvestimento(rr, req, created.ID)
		} else {
			h.HandleCriarInvestimento(rr, req)
		}
		if rr.Code != 500 || strings.Contains(rr.Body.String(), "private SQL detail") {
			t.Fatalf("unexpected error response: %d %q", rr.Code, rr.Body.String())
		}
	}
}

func valuationFormValues() map[string][]string {
	return map[string][]string{"valuation_date": {"2026-02-01"}, "amount_cents": {"110000"}}
}

func TestInvestimentosValuationInputAndAuthority(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	inv := mustHandlerCreate(t, h, investmentFormValues())
	for _, tc := range []struct {
		name, amount, date string
		status             int
	}{
		{"zero", "0", "2026-02-01", 303}, {"integer", "12345", "2026-02-01", 303},
		{"negative", "-1", "2026-02-01", 400}, {"invalid", "x", "2026-02-01", 400},
		{"float", "1.5", "2026-02-01", 400}, {"overflow", "9223372036854775808", "2026-02-01", 400},
		{"empty", "", "2026-02-01", 400}, {"date-format", "1", "01/02/2026", 400},
		{"date-impossible", "1", "2026-02-30", 400}, {"date-before", "1", "2026-01-14", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rowsBefore, err := services.GetInvestmentValuations(context.Background(), h.WriterDB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			values := valuationFormValues()
			values["amount_cents"] = []string{tc.amount}
			values["valuation_date"] = []string{tc.date}
			for _, key := range []string{"workspace_id", "investment_id", "created_by", "updated_by", "status", "archived_at", "redeemed_by"} {
				values[key] = []string{"forged"}
			}
			rr := httptest.NewRecorder()
			h.HandleValuation(rr, formRequest("/investimentos/"+inv.ID+"/valuations", values), inv.ID)
			if rr.Code != tc.status {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			rows, err := services.GetInvestmentValuations(context.Background(), h.WriterDB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == 303 {
				amount, _ := strconv.ParseInt(tc.amount, 10, 64)
				if len(rows) != 1 || rows[0].AmountCents != amount || rows[0].ValuationDate != tc.date || rows[0].CreatedBy != "user-a" || rows[0].WorkspaceID != "ws-a" || (rows[0].UpdatedBy != nil && *rows[0].UpdatedBy != "user-a") {
					t.Fatalf("persisted: %+v", rows)
				}
				if rr.Header().Get("Location") != "/investimentos/"+inv.ID {
					t.Fatal("wrong detail redirect")
				}
			} else if !reflect.DeepEqual(rowsBefore, rows) {
				t.Fatal("invalid input changed valuation")
			}
		})
	}
	// A malformed form is rejected; query parameters cannot supply the body DTO.
	for _, body := range []string{"amount_cents=%zz", ""} {
		req := httptest.NewRequest("POST", "/investimentos/"+inv.ID+"/valuations?amount_cents=1&valuation_date=2026-02-01", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		h.HandleValuation(rr, req, inv.ID)
		if rr.Code != 400 {
			t.Fatalf("malformed/bodyless form status=%d", rr.Code)
		}
	}
}

func TestInvestimentosValuationWriterAndErrors(t *testing.T) {
	h, foreign := seedInvestmentHandlerDB(t)
	inv := mustHandlerCreate(t, h, investmentFormValues())
	writer := h.WriterDB
	h.WriterDB = nil
	rr := httptest.NewRecorder()
	h.HandleValuation(rr, formRequest("/", nil), inv.ID)
	if rr.Code != 500 {
		t.Fatalf("nil writer status=%d", rr.Code)
	}
	h.WriterDB = writer
	// The read pool is deliberately absent: every valuation operation uses WriterDB.
	h.DB = nil
	rr = httptest.NewRecorder()
	req := formRequest("/", valuationFormValues())
	req.Header.Set("HX-Request", "true")
	h.HandleValuation(rr, req, inv.ID)
	if rr.Code != 200 || rr.Header().Get("HX-Refresh") != "true" {
		t.Fatalf("writer-only HTMX: %d %v", rr.Code, rr.Header())
	}
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}
		rr = httptest.NewRecorder()
		h.HandleValuation(rr, formRequest("/", valuationFormValues()).WithContext(ctx), inv.ID)
		cancel()
		if rr.Code != 408 {
			t.Fatalf("context status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
	for _, tc := range []struct {
		handler *InvestimentosHandler
		id      string
	}{{h, "missing"}, {foreign, inv.ID}} {
		rr = httptest.NewRecorder()
		tc.handler.HandleValuation(rr, formRequest("/", valuationFormValues()), tc.id)
		if rr.Code != 404 {
			t.Fatalf("isolation: %d %s", rr.Code, rr.Body.String())
		}
	}
	execTestSQL(t, writer, `UPDATE investments SET status='RESGATADA', redeemed_date='2026-02-01', redeemed_by='user-a' WHERE id=?`, inv.ID)
	rr = httptest.NewRecorder()
	h.HandleValuation(rr, formRequest("/", valuationFormValues()), inv.ID)
	if rr.Code != 409 {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestInvestimentosValuationUnexpectedTriggerRollback(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	inv := mustHandlerCreate(t, h, investmentFormValues())
	rr := httptest.NewRecorder()
	h.HandleValuation(rr, formRequest("/", valuationFormValues()), inv.ID)
	if rr.Code != 303 {
		t.Fatal(rr.Body.String())
	}
	before, err := services.GetInvestmentValuations(context.Background(), h.DB, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, h.DB, `CREATE TRIGGER valuation_test_abort AFTER UPDATE ON investment_valuations BEGIN SELECT RAISE(ABORT, 'private SQL detail'); END`)
	values := valuationFormValues()
	values["amount_cents"] = []string{"222"}
	rr = httptest.NewRecorder()
	h.HandleValuation(rr, formRequest("/", values), inv.ID)
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), "SQL") {
		t.Fatalf("error leaked: %d %s", rr.Code, rr.Body.String())
	}
	after, err := services.GetInvestmentValuations(context.Background(), h.DB, "ws-a", inv.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("abort did not roll back: %+v %v", after, err)
	}
}

func newValuationDiskHandler(t *testing.T) (*InvestimentosHandler, *database.RuntimeDBs, string, models.Investment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "valuation.db")
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	execTestSQL(t, pools.DB, `INSERT INTO users (id,name,email,password_hash) VALUES ('user-a','A','a@example.test','hash')`)
	execTestSQL(t, pools.DB, `INSERT INTO workspaces (id,name) VALUES ('ws-a','A')`)
	execTestSQL(t, pools.DB, `INSERT INTO accounts (id,workspace_id,name,type,current_balance) VALUES ('acc-inv-a','ws-a','Investment','INVESTMENT',725)`)
	pools.Immediate.SetMaxOpenConns(1)
	pools.Immediate.SetMaxIdleConns(1)
	execTestSQL(t, pools.Immediate, `PRAGMA busy_timeout=25`)
	h := &InvestimentosHandler{DB: pools.DB, WriterDB: pools.Immediate, WorkspaceID: "ws-a", UserID: "user-a"}
	inv := mustHandlerCreate(t, h, investmentFormValues())
	return h, pools, path, inv
}

func TestInvestimentosValuationImmediateContention(t *testing.T) {
	h, pools, path, inv := newValuationDiskHandler(t)
	other, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(25)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	blocker, err := other.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	// Missing ID proves the writer reserves before the lookup (otherwise 404).
	for _, id := range []string{inv.ID, "missing"} {
		rr := httptest.NewRecorder()
		h.HandleValuation(rr, formRequest("/", valuationFormValues()), id)
		if rr.Code != 503 || rr.Header().Get("Retry-After") != "1" || strings.Contains(rr.Body.String(), "SQLITE") {
			t.Fatalf("pre-commit contention: %d %v %s", rr.Code, rr.Header(), rr.Body.String())
		}
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	// A distinct reader keeps an old WAL snapshot while the write commits.
	reader, err := pools.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var before int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM investment_valuations`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("before=%d %v", before, err)
	}
	h.DB = nil
	rr := httptest.NewRecorder()
	h.HandleValuation(rr, formRequest("/", valuationFormValues()), inv.ID)
	if rr.Code != 303 {
		t.Fatalf("independent writer: %d %s", rr.Code, rr.Body.String())
	}
	var stale int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM investment_valuations`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("snapshot=%d %v", stale, err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if v := queryScalarInvestments(t, &InvestimentosHandler{DB: pools.DB}, `SELECT COUNT(*) FROM investment_valuations`); v != "1" {
		t.Fatalf("rows=%s", v)
	}
	if v := queryScalarInvestments(t, &InvestimentosHandler{DB: pools.DB}, `SELECT current_balance FROM accounts WHERE id='acc-inv-a'`); v != "725" {
		t.Fatalf("balance=%s", v)
	}
}

func TestInvestimentosValuationCommitBusyIsGeneric500(t *testing.T) {
	h, pools, path, inv := newValuationDiskHandler(t)
	// Rollback-journal mode permits BEGIN IMMEDIATE and the write while an
	// independent reader holds a shared lock, but forces COMMIT to return BUSY.
	// This is a real commit failure, not a synthetic pre-commit error mapping.
	if err := pools.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(25)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	execTestSQL(t, writer, `PRAGMA journal_mode=DELETE`)
	readerDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(25)")
	if err != nil {
		t.Fatal(err)
	}
	defer readerDB.Close()
	reader, err := readerDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var n int
	if err := reader.QueryRow(`SELECT COUNT(*) FROM investment_valuations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	h.DB = nil
	h.WriterDB = writer
	rr := httptest.NewRecorder()
	h.HandleValuation(rr, formRequest("/", valuationFormValues()), inv.ID)
	if rr.Code != 500 || rr.Header().Get("Retry-After") != "" || rr.Body.String() != "erro interno ao processar investimento\n" {
		t.Fatalf("commit failure: %d %v %q", rr.Code, rr.Header(), rr.Body.String())
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	rows, err := services.GetInvestmentValuations(context.Background(), readerDB, "ws-a", inv.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed commit persisted: %+v %v", rows, err)
	}
}

func investmentLifecycleForm() url.Values {
	return url.Values{"redeemed_date": {"2026-02-01"}, "final_amount_cents": {"120000"}}
}

func TestInvestimentosLifecycleRedeemInput(t *testing.T) {
	h, _ := seedInvestmentHandlerDB(t)
	inv := mustHandlerCreate(t, h, investmentFormValues())
	for _, tc := range []struct{ field, value string }{
		{"final_amount_cents", ""}, {"final_amount_cents", "-1"}, {"final_amount_cents", "1.1"}, {"final_amount_cents", "9223372036854775808"},
		{"redeemed_date", "invalid"}, {"redeemed_date", "2026-02-30"}, {"redeemed_date", "2026-01-14"},
	} {
		t.Run(tc.field+tc.value, func(t *testing.T) {
			form := investmentLifecycleForm()
			form.Set(tc.field, tc.value)
			rr := httptest.NewRecorder()
			h.HandleRedeemInvestment(rr, formRequest("/", form), inv.ID)
			if rr.Code != 400 {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
	// Query values cannot supply either external field.
	rr := httptest.NewRecorder()
	h.HandleRedeemInvestment(rr, formRequest("/?redeemed_date=2026-02-01&final_amount_cents=1", nil), inv.ID)
	if rr.Code != 400 {
		t.Fatalf("query fallback: %d", rr.Code)
	}
	form := investmentLifecycleForm()
	form.Set("final_amount_cents", "0")
	for _, key := range []string{"workspace_id", "investment_id", "user_id", "redeemed_by", "created_by", "updated_by", "status", "archived_at"} {
		form.Set(key, "forged")
	}
	rr = httptest.NewRecorder()
	h.HandleRedeemInvestment(rr, formRequest("/", form), inv.ID)
	if rr.Code != 303 || rr.Header().Get("Location") != "/investimentos/"+inv.ID {
		t.Fatalf("zero: %d %s", rr.Code, rr.Body.String())
	}
	got, err := services.GetInvestment(context.Background(), h.DB, "ws-a", inv.ID)
	if err != nil || got.RedeemedBy == nil || *got.RedeemedBy != "user-a" || got.ArchivedAt != nil {
		t.Fatalf("authority=%+v %v", got, err)
	}
	rows, err := services.GetInvestmentValuations(context.Background(), h.DB, "ws-a", inv.ID)
	if err != nil || len(rows) != 1 || rows[0].AmountCents != 0 {
		t.Fatalf("zero=%+v %v", rows, err)
	}
}

func TestInvestimentosLifecycleWriterAndErrors(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "redeem", true: "archive"}[archive], func(t *testing.T) {
			h, foreign := seedInvestmentHandlerDB(t)
			inv := mustHandlerCreate(t, h, investmentFormValues())
			call := func(handler *InvestimentosHandler, rr http.ResponseWriter, r *http.Request, id string) {
				if archive {
					handler.HandleArchiveInvestment(rr, r, id)
				} else {
					handler.HandleRedeemInvestment(rr, r, id)
				}
			}
			if archive {
				rr := httptest.NewRecorder()
				call(h, rr, formRequest("/", nil), inv.ID)
				if rr.Code != 409 {
					t.Fatalf("active archive=%d", rr.Code)
				}
				if err := services.RedeemInvestment(context.Background(), h.WriterDB, services.RedeemInvestmentInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", RedeemedDate: "2026-02-01", FinalAmountCents: 12}); err != nil {
					t.Fatal(err)
				}
			}
			writer := h.WriterDB
			h.WriterDB = nil
			rr := httptest.NewRecorder()
			call(h, rr, formRequest("/", investmentLifecycleForm()), inv.ID)
			if rr.Code != 500 {
				t.Fatalf("nil writer=%d", rr.Code)
			}
			h.WriterDB = writer
			h.DB = nil
			for _, deadline := range []bool{false, true} {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if deadline {
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				}
				rr = httptest.NewRecorder()
				call(h, rr, formRequest("/", investmentLifecycleForm()).WithContext(ctx), inv.ID)
				cancel()
				if rr.Code != 408 {
					t.Fatalf("context=%d", rr.Code)
				}
			}
			for _, tc := range []struct {
				handler *InvestimentosHandler
				id      string
			}{{h, "missing"}, {foreign, inv.ID}} {
				rr = httptest.NewRecorder()
				call(tc.handler, rr, formRequest("/", investmentLifecycleForm()), tc.id)
				if rr.Code != 404 {
					t.Fatalf("not found=%d", rr.Code)
				}
			}
			rr = httptest.NewRecorder()
			req := formRequest("/", investmentLifecycleForm())
			req.Header.Set("HX-Request", "true")
			call(h, rr, req, inv.ID)
			if rr.Code != 200 || rr.Header().Get("HX-Refresh") != "true" {
				t.Fatalf("writer-only=%d %v", rr.Code, rr.Header())
			}
			rr = httptest.NewRecorder()
			call(h, rr, formRequest("/", investmentLifecycleForm()), inv.ID)
			if rr.Code != 409 {
				t.Fatalf("repeat=%d", rr.Code)
			}
		})
	}
}

func TestInvestimentosLifecycleImmediateContention(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "redeem", true: "archive"}[archive], func(t *testing.T) {
			h, pools, path, inv := newValuationDiskHandler(t)
			if archive {
				if err := services.RedeemInvestment(context.Background(), pools.Immediate, services.RedeemInvestmentInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", RedeemedDate: "2026-02-01", FinalAmountCents: 5}); err != nil {
					t.Fatal(err)
				}
			}
			other, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(25)")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			blocker, err := other.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			h.DB = nil
			call := func(rr http.ResponseWriter, id string) {
				if archive {
					h.HandleArchiveInvestment(rr, formRequest("/", nil), id)
				} else {
					h.HandleRedeemInvestment(rr, formRequest("/", investmentLifecycleForm()), id)
				}
			}
			for _, id := range []string{inv.ID, "missing"} {
				rr := httptest.NewRecorder()
				call(rr, id)
				if rr.Code != 503 || rr.Header().Get("Retry-After") != "1" || strings.Contains(rr.Body.String(), "SQLITE") {
					t.Fatalf("contention: %d %v %s", rr.Code, rr.Header(), rr.Body.String())
				}
			}
			if err := blocker.Rollback(); err != nil {
				t.Fatal(err)
			}
			rr := httptest.NewRecorder()
			call(rr, inv.ID)
			if rr.Code != 303 {
				t.Fatalf("writer success=%d %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestInvestimentosLifecycleUnexpectedTriggerRollback(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(map[bool]string{false: "redeem", true: "archive"}[archive], func(t *testing.T) {
			h, _, _, inv := newValuationDiskHandler(t)
			if archive {
				if err := services.RedeemInvestment(context.Background(), h.WriterDB, services.RedeemInvestmentInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", RedeemedDate: "2026-02-01", FinalAmountCents: 5}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := services.GetInvestment(context.Background(), h.DB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			vals, err := services.GetInvestmentValuations(context.Background(), h.DB, "ws-a", inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			execTestSQL(t, h.WriterDB, `CREATE TRIGGER i4_test_abort BEFORE UPDATE ON investments BEGIN SELECT RAISE(ABORT,'private SQL detail'); END`)
			rr := httptest.NewRecorder()
			if archive {
				h.HandleArchiveInvestment(rr, formRequest("/", nil), inv.ID)
			} else {
				h.HandleRedeemInvestment(rr, formRequest("/", investmentLifecycleForm()), inv.ID)
			}
			if rr.Code != 500 || rr.Body.String() != "erro interno ao processar investimento\n" {
				t.Fatalf("trigger: %d %s", rr.Code, rr.Body.String())
			}
			after, err := services.GetInvestment(context.Background(), h.DB, "ws-a", inv.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("partial lifecycle=%+v %v", after, err)
			}
			afterVals, err := services.GetInvestmentValuations(context.Background(), h.DB, "ws-a", inv.ID)
			if err != nil || !reflect.DeepEqual(vals, afterVals) {
				t.Fatalf("partial history=%+v %v", afterVals, err)
			}
		})
	}
}

func TestInvestimentosLifecycleCommitFailureIndeterminate(t *testing.T) {
	for _, archive := range []bool{false, true} {
		for _, hx := range []bool{false, true} {
			t.Run(fmt.Sprintf("archive=%v/htmx=%v", archive, hx), func(t *testing.T) {
				h, pools, path, inv := newValuationDiskHandler(t)
				if archive {
					if err := services.RedeemInvestment(context.Background(), pools.Immediate, services.RedeemInvestmentInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", RedeemedDate: "2026-02-01", FinalAmountCents: 5}); err != nil {
						t.Fatal(err)
					}
				}
				before, err := services.GetInvestment(context.Background(), pools.DB, "ws-a", inv.ID)
				if err != nil {
					t.Fatal(err)
				}
				beforeVals, err := services.GetInvestmentValuations(context.Background(), pools.DB, "ws-a", inv.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err := pools.Close(); err != nil {
					t.Fatal(err)
				}
				writer, err := sql.Open("sqlite", path+"?_txlock=immediate&_pragma=busy_timeout(25)&_pragma=foreign_keys(1)")
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				writer.SetMaxOpenConns(1)
				execTestSQL(t, writer, `PRAGMA journal_mode=DELETE`)
				readerDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(25)")
				if err != nil {
					t.Fatal(err)
				}
				defer readerDB.Close()
				reader, err := readerDB.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Rollback()
				var n int
				if err := reader.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				h.DB = nil
				h.WriterDB = writer
				rr := httptest.NewRecorder()
				req := formRequest("/", investmentLifecycleForm())
				if hx {
					req.Header.Set("HX-Request", "true")
				}
				if archive {
					h.HandleArchiveInvestment(rr, req, inv.ID)
				} else {
					h.HandleRedeemInvestment(rr, req, inv.ID)
				}
				// Compare the real commit failure to the actual payment precedent.
				want := httptest.NewRecorder()
				respondTogglePagamentoIndeterminateOutcome(want, req)
				if rr.Code != want.Code || rr.Body.String() != want.Body.String() || !reflect.DeepEqual(rr.Header(), want.Header()) || rr.Header().Get("Retry-After") != "" {
					t.Fatalf("indeterminate=%d %v %s want=%d %v", rr.Code, rr.Header(), rr.Body.String(), want.Code, want.Header())
				}
				if err := reader.Rollback(); err != nil {
					t.Fatal(err)
				}
				after, err := services.GetInvestment(context.Background(), readerDB, "ws-a", inv.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("failed commit persisted: %+v %v", after, err)
				}
				afterVals, err := services.GetInvestmentValuations(context.Background(), readerDB, "ws-a", inv.ID)
				if err != nil || !reflect.DeepEqual(beforeVals, afterVals) {
					t.Fatalf("failed commit history: %+v %v", afterVals, err)
				}
			})
		}
	}
}
