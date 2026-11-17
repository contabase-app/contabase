package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInvoicePageReportsProjectionUnavailable(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	for _, htmx := range []bool{false, true} {
		mode := "html"
		if htmx {
			mode = "htmx"
		}
		t.Run(mode, func(t *testing.T) {
			for _, handlerName := range []string{"by_id", "by_card_month", "open_by_card_month"} {
				t.Run(handlerName, func(t *testing.T) {
					scenario := newVersionedInvoiceScenario(t, "ws-invoice-503-"+mode+"-"+handlerName, 5)
					execTestSQL(t, scenario.db, `ALTER TABLE recurring_occurrence_skips RENAME TO recurring_occurrence_skips_unavailable`)
					handler := FaturasHandler{DB: scenario.db, WorkspaceID: scenario.base.ws, UserID: scenario.base.user}

					req := httptest.NewRequest(http.MethodGet, "/", nil)
					switch handlerName {
					case "by_id":
						req = httptest.NewRequest(http.MethodGet, "/faturas/"+scenario.invoiceID, nil)
					case "by_card_month":
						req = httptest.NewRequest(http.MethodGet, "/cartoes/"+scenario.base.card+"/faturas?mes=11&ano=2026", nil)
					case "open_by_card_month":
						req = httptest.NewRequest(http.MethodGet, "/cartoes/"+scenario.base.card+"/faturas/abrir?mes=11&ano=2026", nil)
					}
					if htmx {
						req.Header.Set("HX-Request", "true")
						if handlerName == "open_by_card_month" {
							req.Header.Set("HX-Current-URL", "/faturas?mes=11&ano=2026&cartao="+scenario.base.card)
						}
					}
					rr := httptest.NewRecorder()
					switch handlerName {
					case "by_id":
						handler.HandleExibirFaturaPorID(rr, req, scenario.invoiceID)
					case "by_card_month":
						handler.HandleExibirFaturaPorCartaoMes(rr, req, scenario.base.card, 11, 2026)
					case "open_by_card_month":
						handler.HandleAbrirFaturaPorCartaoMes(rr, req, scenario.base.card, 11, 2026)
					}
					if rr.Code != http.StatusServiceUnavailable {
						t.Fatalf("%s %s status = %d, want 503; body=%q", mode, handlerName, rr.Code, rr.Body.String())
					}
					body := strings.ToLower(rr.Body.String())
					for _, forbidden := range []string{"no such table", "recurring_occurrence_skips", "sql logic error"} {
						if strings.Contains(body, forbidden) {
							t.Fatalf("%s %s leaked SQL detail: %q", mode, handlerName, rr.Body.String())
						}
					}
					if htmx {
						if got := rr.Header().Get("HX-Reswap"); got != "none" {
							t.Fatalf("%s %s HX-Reswap = %q, want none", mode, handlerName, got)
						}
						if trigger := rr.Header().Get("HX-Trigger"); !strings.Contains(trigger, "mostrarAlerta") {
							t.Fatalf("%s %s HX-Trigger = %q, want mostrarAlerta", mode, handlerName, trigger)
						}
					} else if !strings.Contains(strings.ToLower(rr.Body.String()), "temporariamente indispon") {
						t.Fatalf("%s %s missing controlled message: %q", mode, handlerName, rr.Body.String())
					}
				})
			}
		})
	}
}

func TestInvoicePageMissingReturns404(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	db, baseA := newRecurringInvoiceTestBase(t, "ws-invoice-404-a")
	baseB := seedF1Base(t, db, "ws-invoice-404-b")
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-b', ?, 20, 10, 1000000)
	`, baseB.card)
	now := time.Now().UTC()
	addOpenInvoiceFutureClosing(t, db, "invoice-b", baseB.card, "2026-11", now)
	handler := FaturasHandler{DB: db, WorkspaceID: baseA.ws, UserID: baseA.user}

	req := httptest.NewRequest(http.MethodGet, "/faturas/missing", nil)
	rr := httptest.NewRecorder()
	handler.HandleExibirFaturaPorID(rr, req, "missing")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing invoice status = %d, want 404; body=%q", rr.Code, rr.Body.String())
	}

	crossReq := httptest.NewRequest(http.MethodGet, "/faturas/invoice-b", nil)
	crossRR := httptest.NewRecorder()
	handler.HandleExibirFaturaPorID(crossRR, crossReq, "invoice-b")
	if crossRR.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace invoice status = %d, want 404; body=%q", crossRR.Code, crossRR.Body.String())
	}
	for _, forbidden := range []string{baseB.card, "invoice-b", "no such table"} {
		if strings.Contains(crossRR.Body.String(), forbidden) {
			t.Fatalf("foreign workspace response leaked %q: %q", forbidden, crossRR.Body.String())
		}
	}

	cardReq := httptest.NewRequest(http.MethodGet, "/cartoes/"+baseB.card+"/faturas?mes=11&ano=2026", nil)
	cardRR := httptest.NewRecorder()
	handler.HandleExibirFaturaPorCartaoMes(cardRR, cardReq, baseB.card, 11, 2026)
	if cardRR.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace card status = %d, want 404; body=%q", cardRR.Code, cardRR.Body.String())
	}
}

func TestInvoicePageUnexpectedErrorReturns500(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	db, base := newRecurringInvoiceTestBase(t, "ws-invoice-500")
	now := time.Now().UTC()
	addOpenInvoiceFutureClosing(t, db, "invoice-500", base.card, "2026-11", now)
	handler := FaturasHandler{DB: db, WorkspaceID: base.ws, UserID: base.user}
	execTestSQL(t, db, `ALTER TABLE credit_cards RENAME TO credit_cards_unavailable`)

	byIDReq := httptest.NewRequest(http.MethodGet, "/faturas/invoice-500", nil)
	byIDRR := httptest.NewRecorder()
	handler.HandleExibirFaturaPorID(byIDRR, byIDReq, "invoice-500")
	if byIDRR.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected by-id status = %d, want 500; body=%q", byIDRR.Code, byIDRR.Body.String())
	}

	monthReq := httptest.NewRequest(http.MethodGet, "/cartoes/"+base.card+"/faturas?mes=11&ano=2026", nil)
	monthRR := httptest.NewRecorder()
	handler.HandleExibirFaturaPorCartaoMes(monthRR, monthReq, base.card, 11, 2026)
	if monthRR.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected month status = %d, want 500; body=%q", monthRR.Code, monthRR.Body.String())
	}
}
