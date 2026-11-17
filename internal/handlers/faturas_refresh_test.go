package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func faturasRefreshTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("faturas-refresh").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/faturas_index.html"),
		resolveTemplatePath(t, "templates/pages/faturas.html"),
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
		resolveTemplatePath(t, "templates/components/credit_card.html"),
	))
}

func hubMutationRequest(path string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Current-URL", "http://localhost:8080/faturas?cartao=card-test&mes=8&ano=2026&ordem=asc")
	return r
}

func assertHubSnapshot(t *testing.T, rr *httptest.ResponseRecorder, paid bool) {
	t.Helper()
	body := rr.Body.String()
	if rr.Code != http.StatusOK || rr.Header().Get("HX-Redirect") != "" {
		t.Fatalf("snapshot response %d %v %s", rr.Code, rr.Header(), body)
	}
	if strings.Count(body, `id="faturas-index-state"`) != 1 || !strings.Contains(body, `id="faturas-index-state" hx-swap-oob="outerHTML"`) {
		t.Fatalf("one state region required: %s", body)
	}
	for _, id := range []string{"faturas-index-card-rail", "faturas-index-detail", "invoice-summary", "invoice-transactions"} {
		if strings.Count(body, `id="`+id+`"`) != 1 {
			t.Fatalf("region %s missing or duplicated", id)
		}
		if strings.Contains(body, `id="`+id+`" hx-swap-oob=`) {
			t.Fatalf("competing nested OOB: %s", id)
		}
	}
	if strings.Contains(body, "is-settled") != paid || strings.Contains(body, ">PAGO</strong>") != paid {
		t.Fatalf("paid visual = %v; want %v", strings.Contains(body, "is-settled"), paid)
	}
	if !strings.Contains(body, `data-faturas-card-id="card-test"`) || !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, "mes=8") || !strings.Contains(body, "ordem=asc") {
		t.Fatal("snapshot lost selected card, period or sort")
	}
	if rr.Header().Get("HX-Reswap") != "none" || !strings.Contains(rr.Header().Get("HX-Trigger"), `"invoiceSnapshot":true`) {
		t.Fatal("snapshot must suppress competing default/refresh swaps")
	}
}

func TestFaturasAtomicPaymentAndReversal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	h := FaturasHandler{DB: db, WriterDB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: faturasRefreshTemplates(t)}
	for _, step := range []struct {
		amount   string
		paid     bool
		wantPaid int64
	}{{"100,00", false, 10000}, {"150,00", true, 25000}} {
		rr := httptest.NewRecorder()
		h.HandlePagarFatura(rr, hubMutationRequest("/cartoes/faturas/pagar", url.Values{"invoice_id": {"invoice-2026-08"}, "payment_account_id": {"checking-test"}, "payment_mode": {"partial"}, "payment_amount": {step.amount}, "payment_date": {"2026-08-10"}}))
		assertHubSnapshot(t, rr, step.paid)
		cards, err := queryFaturasIndexCards(db, "ws-test", 2026, 8)
		if err != nil || cards[0].PaymentState.PaidActive != step.wantPaid || cards[0].PaymentState.Pending != 25000-step.wantPaid {
			t.Fatalf("rail settlement state: %+v %v", cards, err)
		}
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM invoice_payments WHERE amount_cents=15000`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.HandleReverseInvoicePayment(rr, hubMutationRequest("/cartoes/faturas/pagamentos/reverter", url.Values{"invoice_payment_id": {id}, "reason": {"Correção sintética"}}))
	assertHubSnapshot(t, rr, false)
	cards, err := queryFaturasIndexCards(db, "ws-test", 2026, 8)
	if err != nil || cards[0].PaymentState.Pending != 15000 || cards[0].PaymentState.Financial != InvoiceFinancialPartial {
		t.Fatalf("reversal projection: %+v %v", cards, err)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balance); err != nil || balance != 90000 {
		t.Fatalf("balance after reversal %d %v", balance, err)
	}
}

func TestFaturasAtomicFailedPaymentAndRenderRecovery(t *testing.T) {
	for _, failRender := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected mutation", true: "committed render failure"}[failRender], func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			tpl := faturasRefreshTemplates(t)
			amount := "999,00"
			if failRender {
				tpl = template.Must(template.New("broken").Parse(`{{define "faturas-index-state"}}<div>partial{{.MissingField}}{{end}}`))
				amount = "100,00"
			}
			h := FaturasHandler{DB: db, WriterDB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: tpl}
			rr := httptest.NewRecorder()
			h.HandlePagarFatura(rr, hubMutationRequest("/cartoes/faturas/pagar", url.Values{"invoice_id": {"invoice-2026-08"}, "payment_account_id": {"checking-test"}, "payment_mode": {"partial"}, "payment_amount": {amount}, "payment_date": {"2026-08-10"}}))
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM invoice_payments`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if failRender {
				if rr.Code != 200 || count != 1 || rr.Header().Get("HX-Redirect") != "/faturas?cartao=card-test&mes=8&ano=2026&ordem=asc" || rr.Body.Len() != 0 {
					t.Fatalf("committed payment must recover with GET: %d %d %v %s", rr.Code, count, rr.Header(), rr.Body.String())
				}
			} else if rr.Code < 400 || count != 0 || strings.Contains(rr.Body.String(), "faturas-index-state") || rr.Header().Get("HX-Trigger") != "" {
				t.Fatalf("failed mutation produced success state: %d %d %s", rr.Code, count, rr.Body.String())
			}
		})
	}
}

func TestFaturasPaymentPriorityEffectiveStateAndStableTies(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Unix()
	cards := []CreditCardCard{
		{ID: "empty", PaymentState: computeInvoiceState(0, 0, 0, now)},
		{ID: "paid", DueDateUnix: due, PaymentState: computeInvoiceState(1000, 1000, 0, now)},
		{ID: "open", DueDateUnix: due, PaymentState: computeInvoiceState(1000, 0, due, now)},
		{ID: "partial", DueDateUnix: due, Status: "CLOSED", PaymentState: computeInvoiceState(1000, 400, 1, now)},
		{ID: "closed-first", DueDateUnix: due, Status: "CLOSED", PaymentState: computeInvoiceState(1000, 0, 1, now)},
		{ID: "closed-second", DueDateUnix: due, Status: "CLOSED", PaymentState: computeInvoiceState(90000, 0, 1, now)},
		{ID: "overdue", DueDateUnix: now.AddDate(0, 0, -2).Unix(), PaymentState: computeInvoiceState(1000, 0, 1, now)},
	}
	SortDashboardCardsByPaymentPriority(cards, now)
	var ids []string
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	if got := strings.Join(ids, ","); got != "overdue,closed-first,closed-second,partial,open,paid,empty" {
		t.Fatal(got)
	}
	// Reversal must reposition a previously paid tile from its effective debt.
	for i := range cards {
		if cards[i].ID == "paid" {
			cards[i].DueDateUnix = now.AddDate(0, 0, -5).Unix()
			cards[i].PaymentState = computeInvoiceState(1000, 0, 1, now)
		}
	}
	SortDashboardCardsByPaymentPriority(cards, now)
	if cards[0].ID != "paid" {
		t.Fatal("reopened debt remained behind settled cards")
	}
}

func TestFaturasAtomicRefundLifecycle(t *testing.T) {
	for _, general := range []bool{false, true} {
		t.Run(map[bool]string{false: "purchase refund", true: "general refund"}[general], func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			h := FaturasHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: faturasRefreshTemplates(t)}
			txh := TransactionHandler{DB: db, WorkspaceID: h.WorkspaceID, UserID: h.UserID, Templates: h.Templates}
			for step, want := range []int64{20000, 22000, 25000} {
				form := url.Values{"invoice_id": {"invoice-2026-08"}, "valor": {"50,00"}, "descricao": {"Reembolso sintético"}}
				if step == 1 {
					form.Set("valor", "30,00")
				}
				var refundID string
				if step > 0 {
					table := "transaction_refunds"
					if general {
						table = "invoice_refunds"
					}
					if err := db.QueryRow(`SELECT id FROM ` + table + ` WHERE reversed_at IS NULL`).Scan(&refundID); err != nil {
						t.Fatal(err)
					}
				}
				rr := httptest.NewRecorder()
				req := hubMutationRequest("/cartoes/faturas/reembolso", form)
				if general {
					switch step {
					case 0:
						h.HandleRegistrarReembolsoGeral(rr, req)
					case 1:
						h.HandleAtualizarReembolsoGeral(rr, req, refundID)
					case 2:
						h.HandleRemoverReembolsoGeral(rr, req, refundID)
					}
				} else {
					switch step {
					case 0:
						txh.HandleRegistrarReembolso(rr, req, "purchase-test")
					case 1:
						txh.HandleAtualizarReembolso(rr, req, "purchase-test", refundID)
					case 2:
						txh.HandleRemoverReembolso(rr, req, "purchase-test", refundID)
					}
				}
				assertHubSnapshot(t, rr, false)
				cards, err := queryFaturasIndexCards(db, h.WorkspaceID, 2026, 8)
				if err != nil || cards[0].Amount != want || cards[0].PaymentState.Pending != want || cards[0].LimitMoney != MoneyMinor(500000-want) {
					t.Fatalf("step %d snapshot diverged: %+v %v", step, cards, err)
				}
			}
		})
	}
}

func TestFaturasAtomicDeletionDoesNotMarkEmptyInvoicePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: faturasRefreshTemplates(t)}
	rr := httptest.NewRecorder()
	h.HandleDeletarTransacao(rr, hubMutationRequest("/transacoes/purchase-test?invoice_id=invoice-2026-08", nil), "purchase-test")
	assertHubSnapshot(t, rr, false)
	cards, err := queryFaturasIndexCards(db, h.WorkspaceID, 2026, 8)
	if err != nil || cards[0].PaymentState.Financial != InvoiceFinancialEmpty || cards[0].Amount != 0 {
		t.Fatalf("deleted composition: %+v %v", cards, err)
	}
}
