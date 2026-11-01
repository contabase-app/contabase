package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

// F2 HTTP contract: individual purchase reversal over the full cycle —
// create, reapply financed, reapply record-only, preview, confirm, receipt —
// with replay, refused dependencies and workspace isolation.

func f2FinancedPurchaseID(t *testing.T, h *investimentosRoutesHarness, investment string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(`SELECT p.id FROM investment_purchases p
	 WHERE p.workspace_id='workspace-rbac' AND p.investment_id=? AND p.is_initial=0
	 AND EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=p.workspace_id AND t.id=?||p.id)`,
		investment, services.InvestmentPurchaseTransferPrefix).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func f2HiddenValue(t *testing.T, body, name string) string {
	t.Helper()
	re := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`)
	match := re.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("fragment missing hidden field %q", name)
	}
	return match[1]
}

func TestF2FullCycleFinancedReversalOverHTTP(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "F2 estorno", "1.000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, b7GatePurchaseForm("500,00"), true, false), 303)
	recordless := b7GatePurchaseForm("250,00")
	recordless.Del("source_account_id")
	recordless.Set("funding_mode", "record")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, recordless, true, false), 303)
	if got := b7GateBalance(t, h, "inv-a"); got != 150000 {
		t.Fatalf("inv balance before reversal=%d", got)
	}
	purchase := f2FinancedPurchaseID(t, h, id)

	// The preview is a read: it explains the compensation and writes nothing.
	before := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases")
	previa := url.Values{"purchase_id": {purchase}, "reason": {"valor digitado errado"}}
	rr := h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", h.admin, previa, true, true)
	requireInvestimentosStatus(t, rr, 200)
	fragment := rr.Body.String()
	for _, want := range []string{"Prévia do estorno", "Transferência automática", "R$ 500,00", "Confirmar estorno"} {
		if !strings.Contains(fragment, want) {
			t.Fatalf("preview fragment missing %q", want)
		}
	}
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") != before {
		t.Fatal("preview wrote state")
	}
	requestID := f2HiddenValue(t, fragment, "request_id")
	token := f2HiddenValue(t, fragment, "preview_token")
	sourceVersion := f2HiddenValue(t, fragment, "expected_source_version")
	investmentVersion := f2HiddenValue(t, fragment, "expected_investment_version")
	if requestID == "" || token == "" || sourceVersion == "" || investmentVersion == "" {
		t.Fatal("preview fragment lost confirmation carriers")
	}

	// Confirmation compensates exactly once and redirects to the receipt.
	confirm := url.Values{"purchase_id": {purchase}, "reason": {"valor digitado errado"},
		"request_id": {requestID}, "preview_token": {token},
		"expected_source_version": {sourceVersion}, "expected_investment_version": {investmentVersion}}
	rr = h.request(t, "POST", "/investimentos/"+id+"/compras/estorno", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Redirect") != "/investimentos/"+id+"?estornada=1" {
		t.Fatal("confirm lacks feedback redirect")
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 100000 {
		t.Fatalf("inv balance after reversal=%d", got)
	}
	if got := b7GateBalance(t, h, "checking-a"); got != 200000000-100000 {
		t.Fatalf("checking balance after reversal=%d", got)
	}
	var compensations int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac' AND id LIKE 'investment-reversal:%'`).Scan(&compensations); err != nil || compensations != 1 {
		t.Fatalf("compensations=%d err=%v", compensations, err)
	}

	// The history keeps the purchase flagged Estornada, out of the active
	// total, and the receipt section explains what happened.
	detail := h.request(t, "GET", "/investimentos/"+id+"?estornada=1", h.admin, nil, false, false).Body.String()
	for _, want := range []string{
		"Estorno de compra confirmado.",
		"Estornada",
		"fora do principal ativo",
		"R$ 1.250,00",
		"2 compras",
		"Consultar transferência compensatória",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail after reversal missing %q", want)
		}
	}
	if got := strings.Count(detail, "Estornar compra"); got != 2 {
		t.Fatalf("reversal candidates=%d want=2 (initial excluded, reversed excluded)", got)
	}

	// P1 over HTTP: the reversed lot and its accounts must keep accepting a new
	// financed application instead of aborting on the legacy UPDATE trigger.
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, b7GatePurchaseForm("150,00"), true, false), 303)
	if got := b7GateBalance(t, h, "inv-a"); got != 115000 {
		t.Fatalf("inv balance after post-reversal purchase=%d", got)
	}

	// Replay of the same confirmed form is idempotent over HTTP too.
	after := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_purchase_reversals")
	rr = h.request(t, "POST", "/investimentos/"+id+"/compras/estorno", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Redirect") != "/investimentos/"+id {
		t.Fatalf("replay redirect=%q", rr.Header().Get("HX-Redirect"))
	}
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_purchase_reversals") != after {
		t.Fatal("replay wrote state")
	}

	// A second fresh identity for the same purchase is refused with the typed
	// conflict, and the state stays untouched.
	stale := url.Values{"purchase_id": {purchase}, "reason": {"segunda tentativa"},
		"request_id": {"0f1d3e55-1111-2222-3333-444455556666"}, "preview_token": {"stale"},
		"expected_source_version": {sourceVersion}, "expected_investment_version": {investmentVersion}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno", h.admin, stale, true, false), 409)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_purchase_reversals") != after {
		t.Fatal("conflicting reversal wrote state")
	}
}

func TestF2RecordOnlyReversalOverHTTPWithoutBalanceEffect(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "F2 sem movimentação", "1.000,00")
	recordless := b7GatePurchaseForm("400,00")
	recordless.Del("source_account_id")
	recordless.Set("funding_mode", "record")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, recordless, true, false), 303)
	var purchase string
	if err := h.db.QueryRow(`SELECT p.id FROM investment_purchases p WHERE p.workspace_id='workspace-rbac' AND p.investment_id=? AND p.is_initial=0
	 AND NOT EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=p.workspace_id AND t.id=?||p.id)`, id, services.InvestmentPurchaseTransferPrefix).Scan(&purchase); err != nil {
		t.Fatal(err)
	}
	before := o1aSnapshot(t, h, "accounts", "transactions")
	previa := url.Values{"purchase_id": {purchase}, "reason": {"reaplicação duplicada"}}
	rr := h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", h.admin, previa, true, true)
	requireInvestimentosStatus(t, rr, 200)
	fragment := rr.Body.String()
	for _, want := range []string{"Sem movimentação automática", "Nenhuma. Compras sem movimentação automática"} {
		if !strings.Contains(fragment, want) {
			t.Fatalf("record preview missing %q", want)
		}
	}
	if strings.Contains(fragment, "Saldo do investimento") {
		t.Fatal("record preview promises balance movement")
	}
	confirm := url.Values{"purchase_id": {purchase}, "reason": {"reaplicação duplicada"},
		"request_id":                  {f2HiddenValue(t, fragment, "request_id")},
		"preview_token":               {f2HiddenValue(t, fragment, "preview_token")},
		"expected_source_version":     {f2HiddenValue(t, fragment, "expected_source_version")},
		"expected_investment_version": {f2HiddenValue(t, fragment, "expected_investment_version")}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno", h.admin, confirm, true, false), 303)
	if o1aSnapshot(t, h, "accounts", "transactions") != before {
		t.Fatal("record reversal moved money")
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 100000 {
		t.Fatalf("inv balance=%d", got)
	}
	detail := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	for _, want := range []string{"Estornada", "R$ 1.000,00", "1 compra"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("record detail missing %q", want)
		}
	}
	var movement string
	if err := h.db.QueryRow(`SELECT movement FROM investment_purchase_reversals WHERE workspace_id='workspace-rbac' AND purchase_id=?`, purchase).Scan(&movement); err != nil || movement != "RECORD" {
		t.Fatalf("record receipt movement=%s err=%v", movement, err)
	}
}

func TestF2PreviewRefusesDependenciesAndWorkspaceIsolation(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "F2 bloqueios", "1.000,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras", h.admin, b7GatePurchaseForm("500,00"), true, false), 303)
	purchase := f2FinancedPurchaseID(t, h, id)

	// The initial purchase is reserved for the full cancellation.
	initial := url.Values{"purchase_id": {"initial:" + id}, "reason": {"engano"}}
	rr := h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", h.admin, initial, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if body := rr.Body.String(); !strings.Contains(body, "compra inicial não pode ser estornada individualmente") {
		t.Fatalf("initial preview missing blocker: %s", body)
	}

	// A position at or after the purchase refuses with the explicit reason.
	h.exec(t, `INSERT INTO investment_valuations (id,workspace_id,investment_id,valuation_date,amount_cents,created_by,created_at)
		VALUES ('f2-position','workspace-rbac',?,'2026-03-01',150000,'admin-user',100)`, id)
	positioned := url.Values{"purchase_id": {purchase}, "reason": {"engano"}}
	rr = h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", h.admin, positioned, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if body := rr.Body.String(); !strings.Contains(body, "posição igual ou posterior à compra") {
		t.Fatalf("positioned preview missing blocker: %s", body)
	}
	if strings.Contains(rr.Body.String(), "preview_token") {
		t.Fatal("ineligible preview issued a token")
	}

	// A missing reason is a validation error, not a preview.
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", h.admin, url.Values{"purchase_id": {purchase}}, true, true), 400)
	// A missing purchase selection never reaches the service.
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", h.admin, url.Values{"reason": {"x"}}, true, true), 400)

	// Foreign workspace: every layer answers 404 and writes nothing. A
	// malformed token is uniformly refused for known and foreign identities
	// alike; a properly signed token of another workspace stays unknown.
	foreign := h.session(t, "admin-user", "workspace-b")
	previa := url.Values{"purchase_id": {purchase}, "reason": {"isolamento"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno/previa", foreign, previa, true, true), 404)
	foreignConfirm := url.Values{"purchase_id": {purchase}, "reason": {"x"},
		"request_id": {"0f1d3e55-1111-2222-3333-444455556666"}, "preview_token": {"x"},
		"expected_source_version": {"0"}, "expected_investment_version": {"0"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno", foreign, foreignConfirm, true, false), 409)
	h.exec(t, `INSERT INTO accounts (id,workspace_id,name,type) VALUES ('inv-b','workspace-b','Corretora B','INVESTMENT') ON CONFLICT DO NOTHING`)
	ownForm := investimentosRouteForm()
	ownForm.Set("account_id", "inv-b")
	ownForm.Set("name", "F2 isolamento b")
	ownForm.Set("principal_amount", "1.000,00")
	ownForm.Set("funding_mode", "record")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", foreign, ownForm, true, false), 303)
	var foreignID string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-b' AND name='F2 isolamento b'`).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	foreignPurchase := investmentI21Form()
	foreignPurchase.Set("principal_amount", "400,00")
	foreignPurchase.Set("funding_mode", "record")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+foreignID+"/compras", foreign, foreignPurchase, true, false), 303)
	var foreignPurchaseID string
	if err := h.db.QueryRow(`SELECT p.id FROM investment_purchases p WHERE p.workspace_id='workspace-b' AND p.investment_id=? AND p.is_initial=0`, foreignID).Scan(&foreignPurchaseID); err != nil {
		t.Fatal(err)
	}
	signed := h.request(t, "POST", "/investimentos/"+foreignID+"/compras/estorno/previa", foreign, url.Values{"purchase_id": {foreignPurchaseID}, "reason": {"própria"}}, true, true)
	requireInvestimentosStatus(t, signed, 200)
	hijacked := url.Values{"purchase_id": {purchase}, "reason": {"própria"},
		"request_id":                  {f2HiddenValue(t, signed.Body.String(), "request_id")},
		"preview_token":               {f2HiddenValue(t, signed.Body.String(), "preview_token")},
		"expected_source_version":     {f2HiddenValue(t, signed.Body.String(), "expected_source_version")},
		"expected_investment_version": {f2HiddenValue(t, signed.Body.String(), "expected_investment_version")}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/compras/estorno", h.admin, hijacked, true, false), 404)
	var receipts int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_purchase_reversals`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("reversal receipts=%d err=%v", receipts, err)
	}
	// GET methods are not part of the contract.
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/compras/estorno/previa", h.admin, nil, false, false), 405)
}
