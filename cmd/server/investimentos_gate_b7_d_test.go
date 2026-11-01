package main

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// B7-I2-D gate: cancellation through the real HTTP surface. Preview and
// receipt are reads; only the explicit confirmation reaches the Immediate
// writer. Every assertion reads the persisted receipt and balances instead of
// trusting a rendered success message.

func b7GateCancellationPreviewForm(reason string) url.Values {
	return url.Values{"request_id": {uuid.NewString()}, "reason": {reason}}
}

func b7GateCancellationHTMLField(t *testing.T, body, name string) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<input[^>]*name="` + regexp.QuoteMeta(name) + `"[^>]*value="([^"]*)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing hidden field %q in cancellation preview: %s", name, body)
	}
	return html.UnescapeString(match[1])
}

func b7GateCancellationPreview(t *testing.T, h *investimentosRoutesHarness, id, session string, form url.Values) string {
	t.Helper()
	rr := h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento/previa", session, form, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	body := rr.Body.String()
	if !strings.Contains(body, "data-investment-cancellation-preview") {
		t.Fatalf("preview marker missing: %q", body)
	}
	return body
}

func b7GateCancellationConfirmForm(t *testing.T, preview string) url.Values {
	t.Helper()
	form := url.Values{}
	for _, name := range []string{"request_id", "reason", "preview_token", "expected_source_version", "expected_investment_version"} {
		form.Set(name, b7GateCancellationHTMLField(t, preview, name))
	}
	return form
}

func b7GateCancellationTransferRows(t *testing.T, h *investimentosRoutesHarness) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac' AND id LIKE ?`, services.InvestmentCancellationTransferPrefix+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// investment_cancellations is WITHOUT ROWID, so the shared snapshot helper
// cannot order it. The receipt projection is added explicitly instead.
func b7GateCancellationState(t *testing.T, h *investimentosRoutesHarness) string {
	t.Helper()
	var receipts string
	if err := h.db.QueryRow(`SELECT COALESCE(group_concat(id||':'||amount_cents||':'||compensation_transaction_id||':'||request_id, '|'), '')
		FROM (SELECT id,amount_cents,compensation_transaction_id,request_id FROM investment_cancellations WHERE workspace_id='workspace-rbac' ORDER BY id)`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	return o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") + "|receipts:" + receipts
}

func TestB7GateCancellationCompensatesIntegrally(t *testing.T) {
	h := b7GateHarness(t)
	const (
		principal int64 = 123456
		initial   int64 = 200000000
	)
	id := b7GateCreateFunded(t, h, "Gate D integral", "1.234,56")
	if got := b7GateBalance(t, h, "checking-a"); got != initial-principal {
		t.Fatalf("checking after purchase=%d want=%d", got, initial-principal)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != principal {
		t.Fatalf("investment after purchase=%d want=%d", got, principal)
	}

	// Route shapes before a receipt exists.
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.admin, nil, false, false), http.StatusNotFound)
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/cancelamento/previa", h.admin, nil, false, false), http.StatusMethodNotAllowed)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento/previa/extra", h.admin, b7GateCancellationPreviewForm("x"), true, true), http.StatusNotFound)

	previewForm := b7GateCancellationPreviewForm("Gate D integral motivo")
	// RBAC: only ADMIN holds investments:write.
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento/previa", session, previewForm, true, true), http.StatusForbidden)
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento", session, url.Values{"request_id": {uuid.NewString()}, "reason": {"x"}}, true, true), http.StatusForbidden)
	}
	// CSRF on both POSTs; a missing token must not reach the writer.
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento/previa", h.admin, previewForm, false, true), http.StatusForbidden)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, url.Values{"request_id": {uuid.NewString()}, "reason": {"x"}}, false, true), http.StatusForbidden)
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("denied requests wrote receipts: %d", got)
	}

	body := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	if !strings.Contains(body, "Cancelar aplicação") || !strings.Contains(body, `data-investment-open="investment-cancel-panel"`) {
		t.Fatal("eligible lot did not render the cancellation action")
	}

	// The client cannot choose workspace, accounts, values or dates.
	previewForm.Set("workspace_id", "workspace-b")
	previewForm.Set("account_id", "checking-b")
	previewForm.Set("amount_cents", "1")
	previewForm.Set("economic_date", "2000-01-01")
	// Native POST fallback re-renders the existing detail page with the same
	// preview inside its drawer instead of returning a bare fragment.
	fallback := h.request(t, "POST", "/investimentos/"+id+"/cancelamento/previa", h.admin, previewForm, true, false)
	requireInvestimentosStatus(t, fallback, http.StatusOK)
	for _, want := range []string{`id="investment-cancel-panel"`, `data-investment-reopen`, "Prévia do cancelamento", "Gate D integral"} {
		if !strings.Contains(fallback.Body.String(), want) {
			t.Fatalf("native preview fallback missing %q", want)
		}
	}
	previewBody := b7GateCancellationPreview(t, h, id, h.admin, previewForm)
	for _, want := range []string{
		"Gate D integral", "Conta de investimento (origem da compensação)", "Conta de origem (destino da compensação)",
		"Investimento A", "Corrente A", "R$ 1.234,56", "R$ 1.998.765,44", "R$ 2.000.000,00", "R$ 0,00",
		`name="preview_token" value="`,
	} {
		if !strings.Contains(previewBody, want) {
			t.Fatalf("preview missing %q", want)
		}
	}

	confirm := b7GateCancellationConfirmForm(t, previewBody)
	// A damaged carrier is a typed conflict and writes nothing.
	damaged := cloneURLValues(confirm)
	damaged.Set("preview_token", "invalido")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, damaged, true, true), http.StatusConflict)
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("damaged preview wrote receipts: %d", got)
	}
	if got := b7GateCancellationTransferRows(t, h); got != 0 {
		t.Fatalf("damaged preview wrote compensation: %d", got)
	}
	if got := b7GateBalance(t, h, "checking-a"); got != initial-principal {
		t.Fatalf("damaged preview moved checking: %d", got)
	}

	// Forged financial fields are ignored on the confirmation as well.
	confirm.Set("workspace_id", "workspace-b")
	confirm.Set("account_id", "checking-b")
	confirm.Set("amount_cents", "1")
	confirm.Set("economic_date", "2000-01-01")
	rr := h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	if got := rr.Header().Get("HX-Redirect"); got != "/investimentos/"+id+"?cancelada=1" {
		t.Fatalf("success redirect=%q", got)
	}
	if strings.Contains(rr.Header().Get("HX-Redirect"), "preview_token") || strings.Contains(rr.Header().Get("HX-Redirect"), "token=") {
		t.Fatal("preview token leaked into the URL")
	}

	if got := b7GateBalance(t, h, "checking-a"); got != initial {
		t.Fatalf("checking after cancellation=%d want=%d", got, initial)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 0 {
		t.Fatalf("investment after cancellation=%d want=0", got)
	}
	var transactionCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac'`).Scan(&transactionCount); err != nil || transactionCount != 2 {
		t.Fatalf("transactions=%d err=%v", transactionCount, err)
	}
	if got := b7GateCancellationTransferRows(t, h); got != 1 {
		t.Fatalf("compensation transfers=%d", got)
	}
	var compensationID, compensationType, compensationStatus, compensationDescription, source, destination string
	var compensationAmount int64
	if err := h.db.QueryRow(`SELECT id,amount,account_id,destination_account_id,type,status,description
		FROM transactions WHERE workspace_id='workspace-rbac' AND id LIKE ?`, services.InvestmentCancellationTransferPrefix+"%").
		Scan(&compensationID, &compensationAmount, &source, &destination, &compensationType, &compensationStatus, &compensationDescription); err != nil {
		t.Fatal(err)
	}
	if compensationAmount != principal || source != "inv-a" || destination != "checking-a" || compensationType != "TRANSFER" ||
		compensationStatus != "paid" || compensationDescription != "Cancelamento de investimento" {
		t.Fatalf("compensation amount=%d source=%s destination=%s type=%s status=%s description=%s",
			compensationAmount, source, destination, compensationType, compensationStatus, compensationDescription)
	}

	var receiptID, receiptWorkspace, receiptInvestment, receiptRequest, receiptReason, economicDate string
	var amount, investmentBefore, investmentAfter, originBefore, originAfter int64
	if err := h.db.QueryRow(`SELECT id,workspace_id,investment_id,request_id,reason,economic_date,amount_cents,
		investment_before_cents,investment_after_cents,origin_before_cents,origin_after_cents
		FROM investment_cancellations WHERE workspace_id='workspace-rbac'`).
		Scan(&receiptID, &receiptWorkspace, &receiptInvestment, &receiptRequest, &receiptReason, &economicDate,
			&amount, &investmentBefore, &investmentAfter, &originBefore, &originAfter); err != nil {
		t.Fatal(err)
	}
	if receiptWorkspace != "workspace-rbac" || receiptInvestment != id || receiptRequest != confirm.Get("request_id") ||
		receiptReason != "Gate D integral motivo" || economicDate == "" {
		t.Fatalf("receipt identity workspace=%s investment=%s request=%s reason=%s date=%s",
			receiptWorkspace, receiptInvestment, receiptRequest, receiptReason, economicDate)
	}
	if amount != principal || investmentBefore != principal || investmentAfter != 0 ||
		originBefore != initial-principal || originAfter != initial {
		t.Fatalf("receipt amounts amount=%d investment=%d/%d origin=%d/%d",
			amount, investmentBefore, investmentAfter, originBefore, originAfter)
	}
	if compensationID != services.InvestmentCancellationTransferPrefix+receiptID {
		t.Fatalf("compensation=%s receipt=%s", compensationID, receiptID)
	}

	// CANCELADA is derived from the receipt; the investment row stays ATIVA.
	var status string
	if err := h.db.QueryRow(`SELECT status FROM investments WHERE workspace_id='workspace-rbac' AND id=?`, id).Scan(&status); err != nil || status != "ATIVA" {
		t.Fatalf("investment status=%s err=%v", status, err)
	}
	var originalTransfer int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac' AND id=?`,
		services.InvestmentPurchaseTransferPrefix+"initial:"+id).Scan(&originalTransfer); err != nil || originalTransfer != 1 {
		t.Fatalf("original transfer=%d err=%v", originalTransfer, err)
	}
	var purchaseCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_purchases WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&purchaseCount); err != nil || purchaseCount != 1 {
		t.Fatalf("purchase history=%d err=%v", purchaseCount, err)
	}

	body = h.request(t, "GET", "/investimentos/"+id+"?cancelada=1", h.admin, nil, false, false).Body.String()
	for _, want := range []string{
		"Cancelada", "Cancelamento confirmado", "Gate D integral motivo", "Investimento A", "Corrente A",
		"R$ 1.234,56", "R$ 1.998.765,44", "R$ 2.000.000,00", "R$ 0,00",
		"Consultar transferência compensatória", `href="/transacoes/` + compensationID + `"`,
		"Compras e reaplicações", `href="/transacoes/` + services.InvestmentPurchaseTransferPrefix + "initial:" + id + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("cancelled detail missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`data-investment-open="investment-cancel-panel"`, "Cancelar aplicação",
		`hx-post="/investimentos/` + id + `/compras"`, `hx-post="/investimentos/` + id + `/editar"`,
		`hx-post="/investimentos/` + id + `/valuations"`, `hx-post="/investimentos/` + id + `/retiradas"`,
		`hx-post="/investimentos/` + id + `/resgatar"`, `hx-post="/investimentos/` + id + `/arquivar"`,
		`action="/investimentos/` + id + `/cancelamento/previa"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("cancelled detail exposed incompatible action %q", forbidden)
		}
	}

	receiptRoute := h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.admin, nil, false, false)
	requireInvestimentosStatus(t, receiptRoute, http.StatusOK)
	if !strings.Contains(receiptRoute.Body.String(), "Cancelamento confirmado") || !strings.Contains(receiptRoute.Body.String(), compensationID) {
		t.Fatal("receipt route did not render the persisted receipt")
	}
	for _, user := range []string{"manager-user", "regular-user"} {
		requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.session(t, user, "workspace-rbac"), nil, false, false), http.StatusForbidden)
	}
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.session(t, "admin-user", "workspace-b"), nil, false, false), http.StatusNotFound)
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/missing/cancelamento", h.admin, nil, false, false), http.StatusNotFound)

	// The cancelled lot keeps status ATIVA and its receipt, but the Ativas
	// filter lists applications, not receipts: it must not appear there.
	activeList := h.request(t, "GET", "/investimentos", h.admin, nil, false, false).Body.String()
	if strings.Contains(activeList, `href="/investimentos/`+id+`"`) {
		t.Fatal("cancelled investment leaked into the Ativas filter")
	}
	allList := h.request(t, "GET", "/investimentos?status=TODAS", h.admin, nil, false, false).Body.String()
	if !strings.Contains(allList, `href="/investimentos/`+id+`?status=TODAS"`) || !strings.Contains(allList, "Cancelada") {
		t.Fatal("cancelled investment missing from the Todas filter")
	}

	// A lot with a later purchase is not eligible: the action stays visible as
	// disabled and explains the reason, but no executable cancellation exists.
	other := b7GateCreateFunded(t, h, "Gate D inelegível", "500,00")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+other+"/compras", h.admin, b7GatePurchaseForm("100,00"), true, false), http.StatusSeeOther)
	otherBody := h.request(t, "GET", "/investimentos/"+other, h.admin, nil, false, false).Body.String()
	for _, want := range []string{
		"Cancelar aplicação", "Cancelamento indisponível", "compras adicionais",
		`disabled aria-describedby="investment-cancel-blockers"`,
		"estorno de compra individual ainda não está disponível",
	} {
		if !strings.Contains(otherBody, want) {
			t.Fatalf("ineligible detail missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`data-investment-open="investment-cancel-panel"`, `id="investment-cancel-panel"`,
		`action="/investimentos/` + other + `/cancelamento/previa"`,
	} {
		if strings.Contains(otherBody, forbidden) {
			t.Fatalf("ineligible detail exposed executable cancellation %q", forbidden)
		}
	}
	ineligible := b7GateCancellationPreview(t, h, other, h.admin, b7GateCancellationPreviewForm("Gate D inelegível motivo"))
	for _, want := range []string{"Cancelamento indisponível", "aplicação possui operações ou dependências posteriores"} {
		if !strings.Contains(ineligible, want) {
			t.Fatalf("ineligible preview missing %q: %s", want, ineligible)
		}
	}
	if strings.Contains(ineligible, `name="preview_token"`) {
		t.Fatal("ineligible preview issued a confirmation token")
	}

	t.Logf("INTEGRAL checking=%d investment=%d compensated=%d receipt=%s compensation=%s cancelled_state=derived",
		b7GateBalance(t, h, "checking-a"), b7GateBalance(t, h, "inv-a"), amount, receiptID, compensationID)
}

func cloneURLValues(values url.Values) url.Values {
	copyValues := make(url.Values, len(values))
	for key, items := range values {
		copyValues[key] = append([]string(nil), items...)
	}
	return copyValues
}

func TestB7GateCancellationReceiptIdempotencyAndReplay(t *testing.T) {
	h := b7GateHarness(t)
	const (
		principal int64 = 100000
		initial   int64 = 200000000
	)
	id := b7GateCreateFunded(t, h, "Gate D replay", "1.000,00")
	previewForm := b7GateCancellationPreviewForm("Gate D replay motivo")
	previewBody := b7GateCancellationPreview(t, h, id, h.admin, previewForm)
	confirm := b7GateCancellationConfirmForm(t, previewBody)

	// An independent movement after the preview invalidates its evidence and
	// requires a fresh preview; the approved service must refuse without writes.
	h.exec(t, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
 VALUES('d-independent','workspace-rbac','admin-user','checking-a','INCOME',500,1765000000,'2026-02-01',1765000000,'2026-02-01','paid','Rendimento independente',400,400)`)
	h.exec(t, `UPDATE accounts SET current_balance=current_balance+500 WHERE workspace_id='workspace-rbac' AND id='checking-a'`)
	stale := h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, stale, http.StatusConflict)
	if body := stale.Body.String(); !strings.Contains(body, "solicite outra") {
		t.Fatalf("stale preview must ask for a new one: %q", body)
	}
	if got := b7GateCancellationRows(t, h); got != 0 {
		t.Fatalf("stale preview wrote receipts: %d", got)
	}
	if got := b7GateCancellationTransferRows(t, h); got != 0 {
		t.Fatalf("stale preview wrote compensation: %d", got)
	}

	// Same request_id, new preview and confirmation: the retry key is stable.
	retryPreview := b7GateCancellationPreview(t, h, id, h.admin, previewForm)
	retryConfirm := b7GateCancellationConfirmForm(t, retryPreview)
	if retryConfirm.Get("request_id") != confirm.Get("request_id") {
		t.Fatal("retry changed the request_id")
	}
	rr := h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, retryConfirm, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	if got := rr.Header().Get("HX-Redirect"); got != "/investimentos/"+id+"?cancelada=1" {
		t.Fatalf("redirect=%q", got)
	}
	if got := b7GateBalance(t, h, "checking-a"); got != initial+500 {
		t.Fatalf("checking=%d want=%d", got, initial+500)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 0 {
		t.Fatalf("investment=%d want=0", got)
	}
	if got := b7GateCancellationRows(t, h); got != 1 {
		t.Fatalf("receipts=%d", got)
	}
	if got := b7GateCancellationTransferRows(t, h); got != 1 {
		t.Fatalf("compensations=%d", got)
	}
	state := b7GateCancellationState(t, h)

	// Double submission of the exact same form replays the durable receipt.
	rr = h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, retryConfirm, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	if b7GateCancellationState(t, h) != state {
		t.Fatal("double submission duplicated financial effects")
	}

	// Same request_id with other payload is a conflict, never a second effect.
	changed := cloneURLValues(retryConfirm)
	changed.Set("reason", "Gate D outro motivo")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, changed, true, true), http.StatusConflict)
	if b7GateCancellationState(t, h) != state {
		t.Fatal("altered replay changed persisted state")
	}
	// A new key cannot start a second cancellation for the same lot.
	freshKey := cloneURLValues(retryConfirm)
	freshKey.Set("request_id", uuid.NewString())
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, freshKey, true, true), http.StatusConflict)
	if b7GateCancellationState(t, h) != state {
		t.Fatal("new key after confirmation changed persisted state")
	}

	// The persisted receipt is readable without the original token and stays
	// invisible across workspaces.
	receiptRoute := h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.admin, nil, false, false)
	requireInvestimentosStatus(t, receiptRoute, http.StatusOK)
	for _, want := range []string{"Cancelamento confirmado", "Gate D replay motivo", "Consultar transferência compensatória"} {
		if !strings.Contains(receiptRoute.Body.String(), want) {
			t.Fatalf("receipt view missing %q", want)
		}
	}
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.session(t, "admin-user", "workspace-b"), nil, false, false), http.StatusNotFound)

	// A new preview is refused after the receipt exists.
	after := h.request(t, "POST", "/investimentos/"+id+"/cancelamento/previa", h.admin, b7GateCancellationPreviewForm("outra tentativa"), true, true)
	requireInvestimentosStatus(t, after, http.StatusConflict)
	if body := after.Body.String(); !strings.Contains(body, "já cancelada") {
		t.Fatalf("preview after cancellation: %q", body)
	}
	if b7GateCancellationState(t, h) != state {
		t.Fatal("refused preview after cancellation changed persisted state")
	}

	detail := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	if !strings.Contains(detail, "Cancelada") || strings.Contains(detail, `data-investment-open="investment-cancel-panel"`) {
		t.Fatal("cancelled detail did not derive CANCELADA and hide the action")
	}

	t.Logf("REPLAY receipts=1 transfers=%d stale_preview=409 double_submit=replayed altered_payload=409 new_key=409 tokenless_receipt=200",
		b7GateCancellationTransferRows(t, h))
}

func TestB7GateCancellationConcurrentReplay(t *testing.T) {
	h := b7GateHarness(t)
	const (
		principal int64 = 200000
		initial   int64 = 200000000
	)
	id := b7GateCreateFunded(t, h, "Gate D concorrente", "2.000,00")
	previewBody := b7GateCancellationPreview(t, h, id, h.admin, b7GateCancellationPreviewForm("Gate D concorrente motivo"))
	confirm := b7GateCancellationConfirmForm(t, previewBody)
	path := "/investimentos/" + id + "/cancelamento"

	submit := func() *httptest.ResponseRecorder {
		return h.request(t, "POST", path, h.admin, confirm, true, true)
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- submit()
		}()
	}
	wg.Wait()
	close(responses)
	for rr := range responses {
		requireInvestimentosStatus(t, rr, http.StatusOK)
		if got := rr.Header().Get("HX-Redirect"); got != "/investimentos/"+id+"?cancelada=1" {
			t.Fatalf("concurrent replay redirect=%q", got)
		}
	}

	if got := b7GateCancellationRows(t, h); got != 1 {
		t.Fatalf("receipts=%d", got)
	}
	if got := b7GateCancellationTransferRows(t, h); got != 1 {
		t.Fatalf("compensation transfers=%d", got)
	}
	if got := b7GateBalance(t, h, "checking-a"); got != initial {
		t.Fatalf("checking=%d want=%d", got, initial)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 0 {
		t.Fatalf("investment=%d want=0", got)
	}
	state := b7GateCancellationState(t, h)

	// A second concurrent wave only replays the durable receipt.
	responses = make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- submit()
		}()
	}
	wg.Wait()
	close(responses)
	for rr := range responses {
		requireInvestimentosStatus(t, rr, http.StatusOK)
	}
	if b7GateCancellationState(t, h) != state {
		t.Fatal("concurrent replay duplicated financial effects")
	}

	t.Logf("CONCURRENT receipts=1 transfers=%d checking=%d investment=%d replays=4 created=1",
		b7GateCancellationTransferRows(t, h), b7GateBalance(t, h, "checking-a"), b7GateBalance(t, h, "inv-a"))
}

// B7 sequential smoke: one disposable lot walks inclusion, detail, metadata
// edition, preview, confirmation, compensation, receipt, listing, idempotent
// replay and workspace isolation through the real HTTP surface. Every expected
// and observed amount is asserted in cents.
func TestB7InvestmentSequentialSmoke(t *testing.T) {
	h := b7GateHarness(t)
	const (
		principal int64 = 123456
		initial   int64 = 200000000
	)
	if got := b7GateBalance(t, h, "checking-a"); got != initial {
		t.Fatalf("fixture checking=%d want=%d", got, initial)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 0 {
		t.Fatalf("fixture investment=%d want=0", got)
	}

	id := b7GateCreateFunded(t, h, "Smoke sequencial", "1.234,56")
	checkingAfterPurchase := b7GateBalance(t, h, "checking-a")
	investmentAfterPurchase := b7GateBalance(t, h, "inv-a")
	if checkingAfterPurchase != initial-principal || investmentAfterPurchase != principal {
		t.Fatalf("inclusion checking=%d investment=%d want=%d/%d",
			checkingAfterPurchase, investmentAfterPurchase, initial-principal, principal)
	}
	detail := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, detail, http.StatusOK)
	for _, want := range []string{"Smoke sequencial", "R$ 1.234,56", "1 compra"} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Fatalf("detail missing %q", want)
		}
	}

	beforeEdit := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases")
	edit := url.Values{"name": {"Smoke sequencial editado"}, "institution": {"Banco Smoke"}, "kind": {"LCI"},
		"tax_regime": {"MANUAL"}, "yield_note": {"95% CDI"}, "maturity_date": {"2030-05-01"}}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/editar", h.admin, edit, true, false), http.StatusSeeOther)
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases") != beforeEdit {
		t.Fatal("metadata edition changed balances, journal or purchase principal")
	}
	var name, status string
	var storedPrincipal int64
	if err := h.db.QueryRow(`SELECT name, status, principal_cents FROM investments
		WHERE workspace_id='workspace-rbac' AND id=?`, id).Scan(&name, &status, &storedPrincipal); err != nil {
		t.Fatal(err)
	}
	if name != "Smoke sequencial editado" || status != "ATIVA" || storedPrincipal != principal {
		t.Fatalf("edited name=%s status=%s principal=%d want=%s/ATIVA/%d", name, status, storedPrincipal, "Smoke sequencial editado", principal)
	}

	preview := b7GateCancellationPreview(t, h, id, h.admin, b7GateCancellationPreviewForm("Smoke sequencial motivo"))
	for _, want := range []string{"Smoke sequencial editado", "R$ 1.234,56", "R$ 1.998.765,44", "R$ 2.000.000,00", "R$ 0,00"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q", want)
		}
	}
	confirm := b7GateCancellationConfirmForm(t, preview)
	rr := h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	if got := rr.Header().Get("HX-Redirect"); got != "/investimentos/"+id+"?cancelada=1" {
		t.Fatalf("confirmation redirect=%q", got)
	}

	var compensationID, source, destination, compensationType, compensationStatus string
	var compensationAmount int64
	if err := h.db.QueryRow(`SELECT id, amount, account_id, destination_account_id, type, status
		FROM transactions WHERE workspace_id='workspace-rbac' AND id LIKE ?`,
		services.InvestmentCancellationTransferPrefix+"%").Scan(&compensationID, &compensationAmount, &source, &destination, &compensationType, &compensationStatus); err != nil {
		t.Fatal(err)
	}
	if compensationAmount != principal || source != "inv-a" || destination != "checking-a" || compensationType != "TRANSFER" || compensationStatus != "paid" {
		t.Fatalf("compensation amount=%d source=%s destination=%s type=%s status=%s",
			compensationAmount, source, destination, compensationType, compensationStatus)
	}
	if got := b7GateBalance(t, h, "checking-a"); got != initial {
		t.Fatalf("checking after compensation=%d want=%d", got, initial)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 0 {
		t.Fatalf("investment after compensation=%d want=0", got)
	}

	var purchaseCount, originalTransfers, compensationTransfers, totalTransactions int
	var purchaseTotal int64
	if err := h.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(principal_cents),0) FROM investment_purchases
		WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&purchaseCount, &purchaseTotal); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac' AND id=?`,
		services.InvestmentPurchaseTransferPrefix+"initial:"+id).Scan(&originalTransfers); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac' AND id LIKE ?`,
		services.InvestmentCancellationTransferPrefix+"%").Scan(&compensationTransfers); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac'`).Scan(&totalTransactions); err != nil {
		t.Fatal(err)
	}
	if purchaseCount != 1 || purchaseTotal != principal || originalTransfers != 1 || compensationTransfers != 1 || totalTransactions != 2 {
		t.Fatalf("history purchases=%d total=%d original_transfers=%d compensation_transfers=%d transactions=%d",
			purchaseCount, purchaseTotal, originalTransfers, compensationTransfers, totalTransactions)
	}

	receipt := h.request(t, "GET", "/investimentos/"+id+"/cancelamento", h.admin, nil, false, false)
	requireInvestimentosStatus(t, receipt, http.StatusOK)
	for _, want := range []string{"Cancelamento confirmado", "Smoke sequencial motivo", "Consultar transferência compensatória", compensationID} {
		if !strings.Contains(receipt.Body.String(), want) {
			t.Fatalf("receipt missing %q", want)
		}
	}

	activeList := h.request(t, "GET", "/investimentos", h.admin, nil, false, false).Body.String()
	if strings.Contains(activeList, `href="/investimentos/`+id+`"`) {
		t.Fatal("cancelled lot leaked into the Ativas filter")
	}
	allList := h.request(t, "GET", "/investimentos?status=TODAS", h.admin, nil, false, false).Body.String()
	if !strings.Contains(allList, `href="/investimentos/`+id+`?status=TODAS"`) || !strings.Contains(allList, "Cancelada") {
		t.Fatal("cancelled lot missing from the Todas filter")
	}
	if err := h.db.QueryRow(`SELECT status FROM investments WHERE workspace_id='workspace-rbac' AND id=?`, id).Scan(&status); err != nil || status != "ATIVA" {
		t.Fatalf("stored status=%s err=%v want=ATIVA", status, err)
	}

	state := b7GateCancellationState(t, h)
	replay := h.request(t, "POST", "/investimentos/"+id+"/cancelamento", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, replay, http.StatusOK)
	if b7GateCancellationState(t, h) != state {
		t.Fatal("idempotent replay duplicated financial effects")
	}
	if got := b7GateCancellationRows(t, h); got != 1 {
		t.Fatalf("receipts after replay=%d want=1", got)
	}
	if got := b7GateCancellationTransferRows(t, h); got != 1 {
		t.Fatalf("compensations after replay=%d want=1", got)
	}
	if got := b7GateBalance(t, h, "checking-a"); got != initial {
		t.Fatalf("checking after replay=%d want=%d", got, initial)
	}

	foreign := h.session(t, "admin-user", "workspace-b")
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id, foreign, nil, false, false), http.StatusNotFound)
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos/"+id+"/cancelamento", foreign, nil, false, false), http.StatusNotFound)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/cancelamento/previa", foreign, b7GateCancellationPreviewForm("foreign"), true, true), http.StatusNotFound)
	foreignList := h.request(t, "GET", "/investimentos?status=TODAS", foreign, nil, false, false)
	requireInvestimentosStatus(t, foreignList, http.StatusOK)
	if strings.Contains(foreignList.Body.String(), id) || strings.Contains(foreignList.Body.String(), "Smoke sequencial") {
		t.Fatal("cancelled lot leaked across workspaces")
	}

	t.Logf("SMOKE principal=%d checking=%d->%d->%d investment=%d->%d->%d compensation=%d transfers=%d purchases=%d transactions=%d replay_duplicates=0 foreign=404",
		principal, initial, checkingAfterPurchase, b7GateBalance(t, h, "checking-a"),
		0, investmentAfterPurchase, b7GateBalance(t, h, "inv-a"),
		compensationAmount, compensationTransfers, purchaseCount, totalTransactions)
}
