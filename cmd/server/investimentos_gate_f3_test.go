package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// B7 F3 gate: record-only registration cancellation (schema 81) through the
// real HTTP surface. The confirmation performs no financial write at all:
// every assertion reads the persisted receipt, the balances and the journal
// instead of trusting a rendered success message.

func b7GateRecordCreationForm(name, principal string) url.Values {
	form := investimentosRouteForm()
	form.Set("name", name)
	form.Set("principal_amount", principal)
	form.Set("funding_mode", "record")
	return form
}

func b7GateCreateRecord(t *testing.T, h *investimentosRoutesHarness, name, principal string) string {
	t.Helper()
	rr := h.request(t, http.MethodPost, "/investimentos", h.admin, b7GateRecordCreationForm(name, principal), true, false)
	requireInvestimentosStatus(t, rr, http.StatusSeeOther)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-rbac' AND name=?`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func b7GateRecordCancellationRows(t *testing.T, h *investimentosRoutesHarness) int {
	t.Helper()
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_record_cancellations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func b7GateRecordPreview(t *testing.T, h *investimentosRoutesHarness, id, session string, form url.Values) string {
	t.Helper()
	rr := h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro/previa", session, form, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	body := rr.Body.String()
	if !strings.Contains(body, "data-investment-cancellation-preview") {
		t.Fatalf("record preview marker missing: %q", body)
	}
	return body
}

func b7GateRecordConfirmForm(t *testing.T, preview string) url.Values {
	t.Helper()
	form := url.Values{}
	for _, name := range []string{"request_id", "reason", "preview_token", "expected_investment_version"} {
		form.Set(name, b7GateCancellationHTMLField(t, preview, name))
	}
	return form
}

// TestB7GateRecordCancellationZeroEffectAndReceipt covers the full happy path:
// record-only creation without transfer, preview and confirmation, byte
// identical balances and journal, durable receipt, CANCELADA projection,
// exclusion from ATIVAS, preservation in TODAS and the corrected re-record.
func TestB7GateRecordCancellationZeroEffectAndReceipt(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateRecord(t, h, "Gate F3 registro", "1.000,00")
	if got := b7GateBalance(t, h, "checking-a"); got != 200000000 {
		t.Fatalf("record lot moved checking: %d", got)
	}
	if got := b7GateBalance(t, h, "inv-a"); got != 0 {
		t.Fatalf("record lot moved investment account: %d", got)
	}
	var transfers int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='workspace-rbac' AND id=?`, services.InvestmentPurchaseTransferPrefix+"initial:"+id).Scan(&transfers); err != nil {
		t.Fatal(err)
	}
	if transfers != 0 {
		t.Fatalf("record lot carries a transfer: %d", transfers)
	}
	before := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases")

	// Route shapes before a receipt exists.
	requireInvestimentosStatus(t, h.request(t, http.MethodGet, "/investimentos/"+id+"/cancelamento-registro", h.admin, nil, false, false), http.StatusMethodNotAllowed)
	requireInvestimentosStatus(t, h.request(t, http.MethodGet, "/investimentos/"+id+"/cancelamento-registro/previa", h.admin, nil, false, false), http.StatusMethodNotAllowed)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro/previa/extra", h.admin, b7GateCancellationPreviewForm("x"), true, true), http.StatusNotFound)

	previewForm := b7GateCancellationPreviewForm("valor correto é R$ 100,00")
	// RBAC: only ADMIN holds investments:write.
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro/previa", session, previewForm, true, true), http.StatusForbidden)
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", session, url.Values{"request_id": {uuid.NewString()}, "reason": {"x"}}, true, true), http.StatusForbidden)
	}
	// CSRF on both POSTs; a missing token must not reach the writer.
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro/previa", h.admin, previewForm, false, true), http.StatusForbidden)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", h.admin, url.Values{"request_id": {uuid.NewString()}, "reason": {"x"}}, false, true), http.StatusForbidden)
	if got := b7GateRecordCancellationRows(t, h); got != 0 {
		t.Fatalf("denied requests wrote receipts: %d", got)
	}

	detail := h.request(t, http.MethodGet, "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	if !strings.Contains(detail, "Cancelar registro") || !strings.Contains(detail, `data-investment-open="investment-cancel-record-panel"`) {
		t.Fatal("record lot did not render the registration cancellation action")
	}
	if strings.Contains(detail, "Cancelar aplicação") {
		t.Fatal("record lot rendered the funded cancellation action")
	}

	// Forged financial fields are ignored: the service resolves everything
	// from the authenticated workspace.
	previewForm.Set("workspace_id", "workspace-b")
	previewForm.Set("amount_cents", "1")
	previewBody := b7GateRecordPreview(t, h, id, h.admin, previewForm)
	for _, want := range []string{
		"Prévia do cancelamento do registro", "Nenhuma movimentação financeira será realizada",
		"Investimento A", "R$ 1.000,00", `name="preview_token" value="`,
	} {
		if !strings.Contains(previewBody, want) {
			t.Fatalf("record preview missing %q", want)
		}
	}
	// The preview itself never writes.
	if after := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases"); after != before {
		t.Fatalf("preview wrote: %s -> %s", before, after)
	}

	confirm := b7GateRecordConfirmForm(t, previewBody)
	// A damaged carrier is a typed conflict and writes nothing.
	damaged := cloneURLValues(confirm)
	damaged.Set("preview_token", "invalido")
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", h.admin, damaged, true, true), http.StatusConflict)
	if got := b7GateRecordCancellationRows(t, h); got != 0 {
		t.Fatalf("damaged preview wrote receipts: %d", got)
	}

	// Native POST fallback re-renders the detail page with the preview inside
	// its drawer instead of a bare fragment.
	fallback := h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro/previa", h.admin, b7GateCancellationPreviewForm("fallback nativo"), true, false)
	requireInvestimentosStatus(t, fallback, http.StatusOK)
	for _, want := range []string{`id="investment-cancel-record-panel"`, `data-investment-reopen`, "Prévia do cancelamento do registro"} {
		if !strings.Contains(fallback.Body.String(), want) {
			t.Fatalf("native record preview fallback missing %q", want)
		}
	}

	rr := h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, rr, http.StatusOK)
	if redirect := rr.Header().Get("HX-Redirect"); redirect != "/investimentos/"+id+"?cancelada=1" {
		t.Fatalf("confirm redirect=%q", redirect)
	}
	if got := b7GateRecordCancellationRows(t, h); got != 1 {
		t.Fatalf("receipts=%d", got)
	}
	// Zero financial effect, byte identical.
	if after := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases"); after != before {
		t.Fatalf("record cancellation moved money: %s -> %s", before, after)
	}
	var beforeCents, afterCents, beforeVersion, afterVersion int64
	if err := h.db.QueryRow(`SELECT investment_before_cents,investment_after_cents,investment_before_version,investment_after_version FROM investment_record_cancellations WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&beforeCents, &afterCents, &beforeVersion, &afterVersion); err != nil {
		t.Fatal(err)
	}
	if beforeCents != afterCents || beforeVersion != afterVersion {
		t.Fatalf("receipt zero-effect witness: before=%d/%d after=%d/%d", beforeCents, beforeVersion, afterCents, afterVersion)
	}

	// CANCELADA projection, receipt view and preserved history.
	cancelled := h.request(t, http.MethodGet, "/investimentos/"+id, h.admin, nil, false, false).Body.String()
	for _, want := range []string{
		"Cancelada", "valor correto é R$ 100,00", "Nenhuma movimentação financeira foi realizada",
		"Cancelamento confirmado", "Histórico original preservado",
	} {
		if !strings.Contains(cancelled, want) {
			t.Fatalf("cancelled detail missing %q", want)
		}
	}
	requireInvestimentosStatus(t, h.request(t, http.MethodGet, "/investimentos/"+id+"/cancelamento", h.admin, nil, false, false), http.StatusOK)

	// Replay of the same confirmation is a redirect without the success flag.
	replay := h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", h.admin, confirm, true, true)
	requireInvestimentosStatus(t, replay, http.StatusOK)
	if redirect := replay.Header().Get("HX-Redirect"); redirect != "/investimentos/"+id {
		t.Fatalf("replay redirect=%q", redirect)
	}
	if got := b7GateRecordCancellationRows(t, h); got != 1 {
		t.Fatalf("replay duplicated receipts: %d", got)
	}

	// Excluded from ATIVAS, preserved in TODAS.
	ativas := h.request(t, http.MethodGet, "/investimentos?status=ATIVAS", h.admin, nil, false, false).Body.String()
	if strings.Contains(ativas, "Gate F3 registro") {
		t.Fatal("cancelled record lot still listed in ATIVAS")
	}
	todas := h.request(t, http.MethodGet, "/investimentos?status=TODAS", h.admin, nil, false, false).Body.String()
	if !strings.Contains(todas, "Gate F3 registro") {
		t.Fatal("cancelled record lot missing from TODAS")
	}

	// Later operations on the cancelled lot are refused.
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/compras", h.admin, b7GatePurchaseForm("100,00"), true, false), http.StatusConflict)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/valuations", h.admin, url.Values{"valuation_date": {"2026-06-01"}, "amount": {"1.100,00"}}, true, false), http.StatusConflict)

	// The corrected re-record works through the same F1 flow.
	corrected := b7GateCreateRecord(t, h, "Gate F3 corrigido", "100,00")
	if corrected == "" || corrected == id {
		t.Fatalf("corrected re-record id=%q", corrected)
	}
	if after := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases"); after == before {
		t.Fatal("corrected re-record missing from snapshot")
	}
}

// TestB7GateRecordCancellationReplayAndBlocking covers the ineligible
// surfaces: a funded lot keeps the schema 79 action, a lot with additional
// purchases shows the objective reason, and the cancelled lot refuses every
// mutation.
func TestB7GateRecordCancellationReplayAndBlocking(t *testing.T) {
	h := b7GateHarness(t)

	t.Run("funded lot keeps schema 79", func(t *testing.T) {
		id := b7GateCreateFunded(t, h, "Gate F3 financiada", "1.000,00")
		body := h.request(t, http.MethodGet, "/investimentos/"+id, h.admin, nil, false, false).Body.String()
		if !strings.Contains(body, "Cancelar aplicação") || !strings.Contains(body, `data-investment-open="investment-cancel-panel"`) {
			t.Fatal("funded lot lost the schema 79 action")
		}
		if strings.Contains(body, "investment-cancel-record-panel") {
			t.Fatal("funded lot rendered the record cancellation drawer")
		}
		preview := b7GateRecordPreview(t, h, id, h.admin, b7GateCancellationPreviewForm("financiada"))
		if strings.Contains(preview, `name="preview_token" value="`) {
			t.Fatal("funded lot eligible for record cancellation")
		}
		if !strings.Contains(preview, "Cancelamento do registro indisponível") {
			t.Fatalf("funded lot preview missing the objective refusal: %q", preview)
		}
	})

	t.Run("lot with additional purchase explains the blocker", func(t *testing.T) {
		id := b7GateCreateRecord(t, h, "Gate F3 com compras", "1.000,00")
		form := investmentI21Form()
		form.Set("principal_amount", "200,00")
		form.Del("source_account_id")
		form.Set("funding_mode", "record")
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/compras", h.admin, form, true, false), http.StatusSeeOther)
		body := h.request(t, http.MethodGet, "/investimentos/"+id, h.admin, nil, false, false).Body.String()
		if !strings.Contains(body, "Cancelamento do registro indisponível") ||
			!strings.Contains(body, "possui compras adicionais") ||
			strings.Contains(body, `data-investment-open="investment-cancel-record-panel"`) {
			t.Fatalf("ineligible record lot did not explain the blocker: %q", body)
		}
	})

	t.Run("cancelled lot refuses mutations and replays once", func(t *testing.T) {
		id := b7GateCreateRecord(t, h, "Gate F3 bloqueios", "500,00")
		previewBody := b7GateRecordPreview(t, h, id, h.admin, b7GateCancellationPreviewForm("bloqueios"))
		confirm := b7GateRecordConfirmForm(t, previewBody)
		path := "/investimentos/" + id + "/cancelamento-registro"
		submit := func() *httptest.ResponseRecorder {
			return h.request(t, http.MethodPost, path, h.admin, confirm, true, true)
		}
		var wg sync.WaitGroup
		responses := make(chan *httptest.ResponseRecorder, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				responses <- submit()
			}()
		}
		wg.Wait()
		close(responses)
		redirects := 0
		for rr := range responses {
			requireInvestimentosStatus(t, rr, http.StatusOK)
			switch got := rr.Header().Get("HX-Redirect"); got {
			case path[:len(path)-len("/cancelamento-registro")] + "?cancelada=1":
				redirects++
			case path[:len(path)-len("/cancelamento-registro")]:
			default:
				t.Fatalf("concurrent redirect=%q", got)
			}
		}
		if redirects < 1 {
			t.Fatal("no confirmation among concurrent replays")
		}
		if got := b7GateRecordCancellationRows(t, h); got != 1 {
			t.Fatalf("concurrent receipts=%d", got)
		}
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/compras", h.admin, b7GatePurchaseForm("100,00"), true, false), http.StatusConflict)
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/valuations", h.admin, url.Values{"valuation_date": {"2026-06-01"}, "amount": {"600,00"}}, true, false), http.StatusConflict)
		withdrawal := investmentI22Form("initial:" + id)
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/retiradas", h.admin, withdrawal, true, false), http.StatusConflict)
		requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/arquivar", h.admin, url.Values{}, true, false), http.StatusConflict)
		// The drawer is gone once cancelled.
		body := h.request(t, http.MethodGet, "/investimentos/"+id, h.admin, nil, false, false).Body.String()
		if strings.Contains(body, "investment-cancel-record-panel") {
			t.Fatal("cancelled lot still renders the record drawer")
		}
	})
}

// TestB7GateRecordCancellationWorkspaceIsolationAndSchema covers cross-tenant
// indistinguishability, the schema 81 marker and the reopen validation.
func TestB7GateRecordCancellationWorkspaceIsolationAndSchema(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateRecord(t, h, "Gate F3 isolamento", "1.000,00")
	foreign := h.session(t, "admin-user", "workspace-b")

	// A foreign workspace never learns the lot exists. The confirm carries the
	// real signed preview: the workspace mismatch itself must read as unknown.
	previewBody := b7GateRecordPreview(t, h, id, h.admin, b7GateCancellationPreviewForm("isolamento"))
	confirm := b7GateRecordConfirmForm(t, previewBody)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro/previa", foreign, b7GateCancellationPreviewForm("invasão"), true, true), http.StatusNotFound)
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", foreign, confirm, true, true), http.StatusNotFound)
	requireInvestimentosStatus(t, h.request(t, http.MethodGet, "/investimentos/"+id+"/cancelamento", foreign, nil, false, false), http.StatusNotFound)
	if leaked := b7GateRecordCancellationRows(t, h); leaked != 0 {
		t.Fatalf("foreign requests wrote receipts: %d", leaked)
	}

	// The harness database carries the full registry including schema 82.
	var version int
	if err := h.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 82 {
		t.Fatalf("schema version=%d want=82", version)
	}

	// Confirm, then revalidate the database through the runtime read-only
	// path (the same validation the reopen and the backup restore run).
	requireInvestimentosStatus(t, h.request(t, http.MethodPost, "/investimentos/"+id+"/cancelamento-registro", h.admin, confirm, true, true), http.StatusOK)
	if got := b7GateRecordCancellationRows(t, h); got != 1 {
		t.Fatalf("receipts=%d", got)
	}
	path := filepath.Join(t.TempDir(), "runtime-f3.db")
	h.exec(t, `VACUUM INTO ?`, path)
	if err := database.ValidateSQLiteRuntimeSchema(path); err != nil {
		t.Fatalf("runtime validation after record cancellation: %v", err)
	}
	reopened, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatalf("reopen after record cancellation: %v", err)
	}
	defer reopened.Close()
	var confirmed int
	if err := reopened.DB.QueryRow(`SELECT COUNT(*) FROM investment_record_cancellations WHERE workspace_id='workspace-rbac' AND investment_id=?`, id).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != 1 {
		t.Fatalf("reopened receipts=%d", confirmed)
	}
}
