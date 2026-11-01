package handlers

import (
	"context"
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

func newInvestmentB3B4Templates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("investimentos-b3-b4").Funcs(withAppShellTestFuncs(template.FuncMap{
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
}

func investmentB3B4Get(t *testing.T, h *InvestimentosHandler, path string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	h.HandleInvestimentos(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rr.Code, rr.Body.String())
	}
	return html.UnescapeString(rr.Body.String())
}

func investmentB3B4Detail(t *testing.T, h *InvestimentosHandler, path, id string) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.HandleDetalheInvestimento(rr, httptest.NewRequest(http.MethodGet, path, nil), id)
	return rr.Code, html.UnescapeString(rr.Body.String())
}

func investmentIndicatorCard(body, name string) string {
	pattern := regexp.MustCompile(`(?s)<div data-investment-indicator="` + regexp.QuoteMeta(name) + `">.*?</div>`)
	return pattern.FindString(body)
}

func requireInvestmentContains(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Fatalf("missing %q", value)
		}
	}
}

func requireInvestmentAbsent(t *testing.T, body string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(body, value) {
			t.Fatalf("unexpected %q", value)
		}
	}
}

func TestInvestimentosB3B4AccountIdentityFilterAndIndicators(t *testing.T) {
	h, other := seedInvestmentHandlerDB(t)
	h.Templates = newInvestmentB3B4Templates(t)
	other.Templates = h.Templates
	h.CSRFToken, other.CSRFToken = "b3-csrf", "b3-csrf"
	ctx := context.Background()

	execTestSQL(t, h.DB, `INSERT INTO accounts (id, workspace_id, name, type, current_balance) VALUES ('acc-inv-a2', 'ws-a', 'Corretora Alfa 2', 'INVESTMENT', 250000)`)
	execTestSQL(t, h.DB, `INSERT INTO accounts (id, workspace_id, name, type, current_balance) VALUES ('acc-inv-old', 'ws-a', 'Corretora Antiga', 'INVESTMENT', 100000)`)
	execTestSQL(t, h.DB, `UPDATE accounts SET current_balance=500000 WHERE id='acc-inv-a'`)
	execTestSQL(t, h.DB, `UPDATE accounts SET current_balance=990000 WHERE id='acc-inv-b'`)

	create := func(handler *InvestimentosHandler, account, name, principal, date string) models.Investment {
		values := investmentFormValues()
		values["account_id"] = []string{account}
		values["name"] = []string{name}
		values["principal_amount"] = []string{principal}
		values["applied_date"] = []string{date}
		return mustHandlerCreate(t, handler, values)
	}
	position := func(inv models.Investment, date string, cents int64) {
		if _, err := services.UpsertInvestmentValuation(ctx, h.WriterDB, services.UpsertInvestmentValuationInput{
			WorkspaceID: h.WorkspaceID, InvestmentID: inv.ID, ActingUserID: h.UserID,
			ValuationDate: date, AmountCents: cents,
		}); err != nil {
			t.Fatal(err)
		}
	}

	alfa := create(h, "acc-inv-a", "Aplicação Alfa", "1.000,00", "2026-01-10")
	alfaWithoutPosition := create(h, "acc-inv-a", "Aplicação Alfa sem posição", "300,00", "2026-01-11")
	beta := create(h, "acc-inv-a2", "Aplicação Beta", "500,00", "2026-01-12")
	antiga := create(h, "acc-inv-old", "Aplicação Antiga", "200,00", "2026-01-13")
	encerrada := create(h, "acc-inv-a", "Aplicação Encerrada", "400,00", "2026-01-14")
	arquivada := create(h, "acc-inv-a2", "Aplicação Arquivada", "100,00", "2026-01-15")
	foreign := create(other, "acc-inv-b", "Aplicação Estrangeira", "900,00", "2026-01-16")

	position(alfa, "2026-02-01", 110000)
	position(beta, "2026-02-01", 60000)
	position(antiga, "2026-02-01", 25000)
	execTestSQL(t, h.DB, `UPDATE accounts SET archived_at=123 WHERE id='acc-inv-old'`)

	if err := services.RedeemInvestment(ctx, h.WriterDB, services.RedeemInvestmentInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: encerrada.ID, ActingUserID: h.UserID,
		RedeemedDate: "2026-02-01", FinalAmountCents: 45000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := services.RedeemInvestment(ctx, h.WriterDB, services.RedeemInvestmentInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: arquivada.ID, ActingUserID: h.UserID,
		RedeemedDate: "2026-02-01", FinalAmountCents: 11000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := services.ArchiveInvestment(ctx, h.WriterDB, services.ArchiveInvestmentInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: arquivada.ID, ActingUserID: h.UserID,
	}); err != nil {
		t.Fatal(err)
	}

	body := investmentB3B4Get(t, h, "/investimentos?status=ATIVAS&conta=acc-inv-a")
	requireInvestmentContains(t, body,
		alfa.Name, alfaWithoutPosition.Name, "Conta de investimento: Corretora A",
		"Corretora Antiga (arquivada)", `value="acc-inv-a" selected`,
		"R$ 5.000,00", "R$ 1.300,00", "R$ 1.100,00",
	)
	requireInvestmentAbsent(t, body, beta.Name, foreign.Name, "Corretora B")
	nominal := investmentIndicatorCard(body, "nominal-yield")
	requireInvestmentContains(t, nominal, "Não disponível", "sem posição registrada")
	requireInvestmentContains(t, body, "Rentabilidade percentual:</strong> Não disponível")

	body = investmentB3B4Get(t, h, "/investimentos?status=ATIVAS&conta=acc-inv-a2")
	requireInvestmentContains(t, body, beta.Name, "R$ 2.500,00", "R$ 500,00", "R$ 600,00")
	requireInvestmentAbsent(t, body, alfa.Name, alfaWithoutPosition.Name, arquivada.Name)
	nominal = investmentIndicatorCard(body, "nominal-yield")
	requireInvestmentContains(t, nominal, "+ R$ 100,00")
	requireInvestmentAbsent(t, nominal, "Não disponível")

	body = investmentB3B4Get(t, h, "/investimentos?status=RESGATADAS&conta=acc-inv-a")
	requireInvestmentContains(t, body, encerrada.Name, `value="RESGATADAS" selected`, `value="acc-inv-a" selected`)
	requireInvestmentAbsent(t, body, alfa.Name, beta.Name)

	body = investmentB3B4Get(t, h, "/investimentos?status=ARQUIVADAS&conta=acc-inv-a2")
	requireInvestmentContains(t, body, arquivada.Name, `value="ARQUIVADAS" selected`)
	requireInvestmentAbsent(t, body, beta.Name, alfa.Name)

	body = investmentB3B4Get(t, h, "/investimentos?status=TODAS&conta=acc-inv-a")
	requireInvestmentContains(t, body, alfa.Name, encerrada.Name,
		`href="/investimentos/`+alfa.ID+`?conta=acc-inv-a&status=TODAS"`)
	requireInvestmentAbsent(t, body, beta.Name, foreign.Name)

	// A foreign account id is indistinguishable from an unknown one: the
	// workspace list stays intact and no foreign account or lot is exposed.
	body = investmentB3B4Get(t, h, "/investimentos?status=TODAS&conta=acc-inv-b")
	requireInvestmentContains(t, body, alfa.Name)
	requireInvestmentAbsent(t, body, "Corretora B", foreign.Name, `value="acc-inv-b"`)

	code, detail := investmentB3B4Detail(t, h, "/investimentos/"+antiga.ID+"?status=TODAS&conta=acc-inv-old", antiga.ID)
	if code != http.StatusOK {
		t.Fatalf("archived account detail: %d", code)
	}
	requireInvestmentContains(t, detail, "Conta de investimento:", "Corretora Antiga", "arquivada",
		`href="/investimentos?conta=acc-inv-old&status=TODAS"`)
	requireInvestmentAbsent(t, detail, "Corretora B")

	code, detail = investmentB3B4Detail(t, h, "/investimentos/"+alfa.ID+"?status=ATIVAS&conta=acc-inv-a", alfa.ID)
	if code != http.StatusOK {
		t.Fatalf("detail: %d", code)
	}
	requireInvestmentContains(t, detail, "Conta de investimento:", "Corretora A")
	requireInvestmentAbsent(t, detail, "Corretora Alfa 2", "Corretora B")

	code, _ = investmentB3B4Detail(t, other, "/investimentos/"+alfa.ID, alfa.ID)
	if code != http.StatusNotFound {
		t.Fatalf("cross-workspace detail: %d", code)
	}
}

func TestInvestimentosB4IndicatorsFailClosed(t *testing.T) {
	account := investmentAccountOption{ID: "acc-inv-a", Name: "Corretora A", BalanceCents: 12345}
	active := []models.Investment{{ID: "inv-a", WorkspaceID: "ws-a", AccountID: "acc-inv-a", Status: models.InvestmentStatusAtiva}}
	purchases := map[string]services.InvestmentPurchaseTotal{
		"inv-a": {TotalCents: 100000, Count: 1, LatestDate: "2026-01-10"},
	}

	summary := investmentSummary{ActiveCount: 1, KnownCount: 1, KnownTotalCents: 110000}
	if err := applyInvestmentAccountIndicators(&summary, account, active, purchases, nil); err != nil {
		t.Fatal(err)
	}
	if !summary.NominalYieldAvailable || summary.NominalYield != "+ R$ 100,00" || summary.AccountBalance != "R$ 123,45" || summary.PrincipalTotal != "R$ 1.000,00" {
		t.Fatalf("valid indicators: %+v", summary)
	}

	summary = investmentSummary{ActiveCount: 1, WithoutPosition: 1, KnownCount: 0}
	if err := applyInvestmentAccountIndicators(&summary, account, active, purchases, nil); err != nil {
		t.Fatal(err)
	}
	if summary.NominalYieldAvailable || summary.NominalYield != "Não disponível" || !strings.Contains(summary.NominalYieldReason, "sem posição registrada") {
		t.Fatalf("missing position: %+v", summary)
	}

	summary = investmentSummary{ActiveCount: 1, WithoutPosition: 1, StalePositions: 1, KnownCount: 0}
	if err := applyInvestmentAccountIndicators(&summary, account, active, purchases, nil); err != nil {
		t.Fatal(err)
	}
	if summary.NominalYieldAvailable || !strings.Contains(summary.NominalYieldReason, "desatualizada") {
		t.Fatalf("stale position: %+v", summary)
	}

	summary = investmentSummary{ActiveCount: 1, KnownCount: 1, KnownTotalCents: 110000}
	if err := applyInvestmentAccountIndicators(&summary, account, active, purchases, map[string]bool{"inv-a": true}); err != nil {
		t.Fatal(err)
	}
	if summary.NominalYieldAvailable || !strings.Contains(summary.NominalYieldReason, "retiradas") {
		t.Fatalf("withdrawal must block nominal yield: %+v", summary)
	}
}

func TestInvestimentosB5CancellationBlockerMessages(t *testing.T) {
	messages, note := investmentCancellationBlockerMessages(
		[]string{"aplicação possui operações ou dependências posteriores"},
		services.InvestmentDetail{PurchaseCount: 2},
	)
	if len(messages) != 1 || !strings.Contains(messages[0], "compras adicionais") || !strings.Contains(note, "estorno de compra individual ainda não está disponível") {
		t.Fatalf("additional purchases: %v / %q", messages, note)
	}

	messages, note = investmentCancellationBlockerMessages(
		[]string{"aplicação possui operações ou dependências posteriores"},
		services.InvestmentDetail{PurchaseCount: 1, Valuations: []models.InvestmentValuation{{ID: "v"}}},
	)
	if len(messages) != 1 || !strings.Contains(messages[0], "posições registradas") || note != "" {
		t.Fatalf("positions: %v / %q", messages, note)
	}

	messages, note = investmentCancellationBlockerMessages(
		[]string{"transferência original ausente"},
		services.InvestmentDetail{PurchaseCount: 1},
	)
	if len(messages) != 1 || !strings.Contains(messages[0], "não possui transferência inicial vinculada") || note != "" {
		t.Fatalf("missing transfer: %v / %q", messages, note)
	}
}
