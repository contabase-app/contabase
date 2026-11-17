package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R1.3-D3-B1: o Dashboard V2 tem um contrato de atualização próprio. Nenhuma
// mutação financeira pode devolver markup de Dashboard (V1) e o grid do V2
// precisa escutar `refreshFinancials` com a URL da própria página.

func TestDashboardPreviewCanonicalRefreshContract(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	historical := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "12m", "2026-10")
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", historical); err != nil {
		t.Fatalf("render preview: %v", err)
	}
	body := out.String()

	if got := strings.Count(body, `id="dashboard-content"`); got != 1 {
		t.Fatalf("dashboard grid count = %d, want exactly 1", got)
	}
	for _, want := range []string{
		`data-cbp-refresh-root`,
		`hx-trigger="refreshFinancials from:body"`,
		`hx-select="#dashboard-content"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="false"`,
		`hx-sync="this:replace"`,
		`hx-indicator="#htmx-indicator"`,
		`hx-get="/?fluxo=12m&amp;mes=2026-10"`,
		`/assets/js/dashboard-preview-refresh.js`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview refresh contract missing %q", want)
		}
	}
	// O V1 não pode ser injetado por um refresh do V2.
	for _, forbidden := range []string{`class="wallet-heading"`, `id="dashboard-balance"`, `id="dashboard-cards"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("preview render leaked V1 markup %q", forbidden)
		}
	}

	current := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	if got := current.DashboardRefreshURL; got != "/" {
		t.Fatalf("current month refresh URL = %q, want canonical current URL", got)
	}
}

func TestFinancialMutationsEmitRefreshEventWithoutDashboardMarkup(t *testing.T) {
	t.Run("invoice payment", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedInvoicePaymentScenario(t, db)

		handler := FaturasHandler{
			DB:          db,
			WriterDB:    db,
			Templates:   testInvoicePaymentTemplates(t),
			WorkspaceID: "ws-test",
			UserID:      "user-test",
		}
		req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", nil)
		rr := httptest.NewRecorder()
		if err := handler.renderInvoiceAndDashboardOOB(rr, req, "invoice-2026-08"); err != nil {
			t.Fatalf("render payment response: %v", err)
		}
		assertCanonicalRefreshOnly(t, rr)
	})

	t.Run("invoice detail composition", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedInvoicePaymentScenario(t, db)

		handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
		req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/salvar", nil)
		rr := httptest.NewRecorder()
		if err := handler.renderInvoiceContextOOB(rr, req, "invoice-2026-08", true); err != nil {
			t.Fatalf("render invoice context: %v", err)
		}
		if strings.Contains(rr.Body.String(), `id="dashboard-`) {
			t.Fatalf("invoice context leaked Dashboard markup: %q", rr.Body.String())
		}
	})
}

func TestTransactionMutationsEmitRefreshFinancials(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
	}{
		{
			name: "expense",
			values: map[string]string{
				"tipo": "despesa", "valor": "100,00", "descricao": "Despesa refresh",
				"data": "2026-10-20", "origem_conta_id": "checking-test", "categoria_id": "cat-refresh",
				"status_pagamento": "paid",
			},
		},
		{
			name: "income",
			values: map[string]string{
				"tipo": "receita", "valor": "100,00", "descricao": "Receita refresh",
				"data": "2026-10-20", "origem_conta_id": "checking-test", "categoria_id": "cat-refresh",
				"status_pagamento": "paid",
			},
		},
		{
			name: "transfer",
			values: map[string]string{
				"tipo": "transferencia", "valor": "100,00", "descricao": "Transferencia refresh",
				"data": "2026-10-20", "origem_conta_id": "checking-test", "destino_conta_id": "checking-test-2",
				"status_pagamento": "paid",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			now := time.Now().Unix()
			execTestSQL(t, db, `
				INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
				VALUES ('checking-test-2', 'ws-test', 'Conta 2', 'CHECKING', 0, 0, ?, ?)
			`, now, now)
			execTestSQL(t, db, `
				INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
				VALUES ('cat-refresh', 'ws-test', 'Categoria refresh', 'tag', '#f97316', ?, ?)
			`, "EXPENSE", now)

			handler := testPaidInvoiceMutationHandler(db)
			values := make(map[string]string, len(tc.values))
			for key, value := range tc.values {
				values[key] = value
			}
			req := newCompVencMultipartUpdateRequest(t, "/transacoes", values)
			rr := httptest.NewRecorder()
			handler.HandleSalvarTransacao(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
			}
			if trigger := rr.Header().Get("HX-Trigger"); !strings.Contains(trigger, "refreshFinancials") {
				t.Fatalf("HX-Trigger = %q, want refreshFinancials", trigger)
			}
			if strings.Contains(rr.Body.String(), `id="dashboard-`) {
				t.Fatalf("mutation leaked Dashboard markup: %q", rr.Body.String())
			}
		})
	}
}

func TestEditAndBulkMutationsEmitRefreshFinancials(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.respondBroadEditAndCloseSheet(rr, httptest.NewRequest(http.MethodPost, "/transacoes/x/salvar", nil))
	trigger := rr.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "refreshLancamentosList") || !strings.Contains(trigger, "refreshFinancials") {
		t.Fatalf("broad edit HX-Trigger = %q, want both refresh events", trigger)
	}
	if strings.Contains(rr.Body.String(), `id="dashboard-`) {
		t.Fatalf("broad edit leaked Dashboard markup: %q", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	handler.respondRowAndCloseSheet(rr, httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/salvar", nil), "purchase-test")
	if got := rr.Header().Get("HX-Trigger"); !strings.Contains(got, "refreshFinancials") {
		t.Fatalf("row edit HX-Trigger = %q, want refreshFinancials", got)
	}
}

func assertCanonicalRefreshOnly(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `id="dashboard-`) {
		t.Fatalf("response leaked Dashboard markup: %q", rr.Body.String())
	}
	if trigger := rr.Header().Get("HX-Trigger"); !strings.Contains(trigger, "refreshFinancials") {
		t.Fatalf("HX-Trigger = %q, want refreshFinancials", trigger)
	}
}

// ServeHTTP: o gate anti-flash existe apenas no primeiro paint. Um refresh HTMX
// não pode esconder o grid atrás da animação de 1,5s.
func TestDashboardPreviewServeHTTPRefreshNeverHidesGrid(t *testing.T) {
	now := time.Now()
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	handler := DashboardV2Handler{
		DB:          db,
		Templates:   previewTestTemplate(t),
		UserID:      base.user,
		WorkspaceID: base.ws,
	}

	full := httptest.NewRecorder()
	handler.ServeHTTP(full, httptest.NewRequest(http.MethodGet, "/", nil))
	if full.Code != http.StatusOK {
		t.Fatalf("full page status = %d", full.Code)
	}
	if !strings.Contains(full.Body.String(), `is-layout-pending`) {
		t.Fatal("first paint must keep the anti-flash gate")
	}
	if !strings.Contains(full.Body.String(), `hx-trigger="refreshFinancials from:body"`) {
		t.Fatal("first paint must expose the canonical refresh listener")
	}

	refreshed := httptest.NewRecorder()
	refreshRequest := httptest.NewRequest(http.MethodGet, "/?fluxo=12m&mes=2026-10", nil)
	refreshRequest.Header.Set("HX-Request", "true")
	handler.ServeHTTP(refreshed, refreshRequest)
	if refreshed.Code != http.StatusOK {
		t.Fatalf("refresh status = %d", refreshed.Code)
	}
	body := refreshed.Body.String()
	if strings.Contains(body, `is-layout-pending`) {
		t.Fatal("HTMX refresh must not hide the grid with the first-paint gate")
	}
	for _, want := range []string{
		`id="dashboard-content"`,
		`data-cbp-refresh-root`,
		`hx-select="#dashboard-content"`,
		`hx-get="/?fluxo=12m&amp;mes=2026-10"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("refresh render missing %q", want)
		}
	}
	if got := strings.Count(body, `id="dashboard-content"`); got != 1 {
		t.Fatalf("refresh grid count = %d, want 1", got)
	}
	// O payload do refresh continua limitado ao workspace ativo do handler.
	for _, foreign := range []string{"9.999,00", "inv-preview-other", "ws-preview-other"} {
		if strings.Contains(body, foreign) {
			t.Fatalf("refresh payload leaked another workspace: %q", foreign)
		}
	}
}
