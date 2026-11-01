package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// B7-I2-C gate: after a confirmed cancellation the lot keeps reading ATIVA,
// so only the receipt guard can refuse later operations. The HTTP contract
// must answer 409 on every investment mutation, every generic mutation of
// both protected transfers, and stay silent (404) across workspaces, without
// a single balance effect on refused operations.

// b7GateCancelLot cancels one funded lot through the approved B service; no
// cancellation route exists before microetapa D by contract.
func b7GateCancelLot(t *testing.T, h *investimentosRoutesHarness, investment string) models.InvestmentCancellation {
	t.Helper()
	previewIn := services.InvestmentCancellationPreviewInput{
		WorkspaceID: "workspace-rbac", InvestmentID: investment, ActingUserID: "admin-user",
		RequestID: uuid.NewString(), Reason: "gate C proteção", Location: time.UTC,
	}
	preview, err := services.PreviewInvestmentCancellation(context.Background(), h.db, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible {
		t.Fatalf("preview blockers: %+v", preview.Blockers)
	}
	result, err := services.CancelInvestment(context.Background(), h.db, services.CancelInvestmentInput{
		WorkspaceID: previewIn.WorkspaceID, InvestmentID: previewIn.InvestmentID, CreatedBy: previewIn.ActingUserID,
		RequestID: previewIn.RequestID, Reason: previewIn.Reason, PreviewToken: preview.PreviewToken,
		ExpectedSourceVersion: preview.OriginBalanceVersion, ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != services.InvestmentOperationCreated {
		t.Fatalf("outcome=%s", result.Outcome)
	}
	return result.Cancel
}

func b7GateTransactionMux(t *testing.T, h *investimentosRoutesHarness) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	txBase := handlers.TransactionHandler{DB: h.db, Templates: transacoesEditarStubTemplates{}}
	registerTransacoesRoutes(mux, h.db, h.service, h.signer, &txBase)
	return mux
}

func TestB7GateCancellationBlocksNewOperations(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "Gate C bloqueios", "1.000,00")
	receipt := b7GateCancelLot(t, h, id)
	if receipt.InvestmentID != id {
		t.Fatalf("receipt investment=%s", receipt.InvestmentID)
	}
	state := o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_valuations")

	attempts := []struct {
		name string
		path string
		form url.Values
	}{
		{"compra com aporte", "/investimentos/" + id + "/compras", b7GatePurchaseForm("500,00")},
		{"compra registrada", "/investimentos/" + id + "/compras", func() url.Values {
			form := b7GatePurchaseForm("500,00")
			form.Del("source_account_id")
			form.Del("funding_mode")
			return form
		}()},
		{"edição", "/investimentos/" + id + "/editar", url.Values{"name": {"Outro nome"}, "institution": {"Banco"}, "kind": {"CDB"}, "tax_regime": {"MANUAL"}}},
		{"posição", "/investimentos/" + id + "/valuations", url.Values{"valuation_date": {"2026-04-01"}, "amount_cents": {"150000"}}},
		{"retirada", "/investimentos/" + id + "/retiradas", investmentI22Form("initial:" + id)},
		{"resgate", "/investimentos/" + id + "/resgatar", url.Values{"redeemed_date": {"2026-04-01"}, "final_amount": {"1.000,00"}}},
		{"arquivamento", "/investimentos/" + id + "/arquivar", url.Values{}},
	}
	for _, attempt := range attempts {
		rr := h.request(t, "POST", attempt.path, h.admin, attempt.form, true, false)
		if rr.Code != http.StatusConflict {
			t.Fatalf("%s: status=%d want=409 body=%q", attempt.name, rr.Code, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, "cancelamento confirmado") {
			t.Fatalf("%s: body must state the cancellation refusal: %q", attempt.name, body)
		}
	}
	if o1aSnapshot(t, h, "accounts", "transactions", "investment_purchases", "investment_valuations") != state {
		t.Fatal("refused later operations changed persisted state")
	}
	if got := b7GateCancellationRows(t, h); got != 1 {
		t.Fatalf("receipts=%d", got)
	}
	t.Logf("BLOCKS purchase=409 record_only=409 edit=409 valuation=409 withdrawal=409 redeem=409 archive=409 state_unchanged=true")
}

func TestB7GateCancellationRefusesReconciliationAndLaterOperations(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "Gate C conciliação", "2.000,00")
	receipt := b7GateCancelLot(t, h, id)
	original := services.InvestmentPurchaseTransferPrefix + "initial:" + id
	compensation := receipt.CompensationTransactionID
	state := o1aSnapshot(t, h, "accounts", "transactions", "transaction_refunds", "invoice_payments", "transaction_status_history")

	txMux := b7GateTransactionMux(t, h)
	txRequest := func(method, path string, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.admin})
		addAdminUsersRBACCSRF(t, req, h.signer)
		rr := httptest.NewRecorder()
		txMux.ServeHTTP(rr, req)
		return rr
	}

	// Generic mutations of both protected transfers: edit, delete, payment
	// toggle, invoice move and refund are refused before any balance reversal.
	for _, protected := range []string{original, compensation} {
		if rr := txRequest(http.MethodDelete, "/transacoes/"+protected, url.Values{}); rr.Code != http.StatusConflict {
			t.Fatalf("delete %s: status=%d body=%q", protected, rr.Code, rr.Body.String())
		}
		if rr := txRequest(http.MethodPost, "/transacoes/"+protected+"/salvar", url.Values{}); rr.Code != http.StatusConflict {
			t.Fatalf("update %s: status=%d body=%q", protected, rr.Code, rr.Body.String())
		}
		if rr := txRequest(http.MethodPost, "/transacoes/"+protected+"/status-pagamento", url.Values{"payment_action": {"paid"}}); rr.Code != http.StatusConflict {
			t.Fatalf("toggle %s: status=%d body=%q", protected, rr.Code, rr.Body.String())
		}
		if rr := txRequest(http.MethodPost, "/transacoes/"+protected+"/mover-fatura", url.Values{"expected_invoice_id": {"x"}}); rr.Code != http.StatusConflict {
			t.Fatalf("move %s: status=%d body=%q", protected, rr.Code, rr.Body.String())
		}
		if rr := txRequest(http.MethodPost, "/transacoes/"+protected+"/reembolso", url.Values{"valor": {"10,00"}, "descricao": {"reembolso"}}); rr.Code != http.StatusConflict {
			t.Fatalf("refund %s: status=%d body=%q", protected, rr.Code, rr.Body.String())
		}
	}
	// The bulk paths share the writer guard: a batch containing the
	// compensation transfer is refused as a whole. HandleBulkDelete is invoked
	// directly because the route wrapper only adds routing and rate limiting.
	bulkBase := handlers.TransactionHandler{DB: h.db, Templates: transacoesEditarStubTemplates{}, WorkspaceID: "workspace-rbac", UserID: "admin-user"}
	bulkReq := httptest.NewRequest(http.MethodPost, "/transacoes/bulk/delete", strings.NewReader(url.Values{"ids": {compensation, original}}.Encode()))
	bulkReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bulkRR := httptest.NewRecorder()
	bulkBase.HandleBulkDelete(bulkRR, bulkReq)
	if bulkRR.Code != http.StatusConflict {
		t.Fatalf("bulk delete: status=%d body=%q", bulkRR.Code, bulkRR.Body.String())
	}

	// Settlement routes need a withdrawal that a cancelled lot can never have:
	// the reconciliation surface itself refuses to resolve.
	if rr := h.request(t, "POST", "/investimentos/"+id+"/financeiro/retiradas/inexistente/liquidacao/conciliar", h.admin, url.Values{}, true, false); rr.Code != http.StatusNotFound {
		t.Fatalf("settlement route: status=%d body=%q", rr.Code, rr.Body.String())
	}

	if o1aSnapshot(t, h, "accounts", "transactions", "transaction_refunds", "invoice_payments", "transaction_status_history") != state {
		t.Fatal("refused reconciliation mutations changed persisted state or balances")
	}
	// Independent movement on the origin account still works after refusals.
	h.exec(t, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
 VALUES('c-independent-income','workspace-rbac','admin-user','checking-a','INCOME',700,1765000000,'2026-02-01',1765000000,'2026-02-01','paid','Rendimento externo',400,400)`)
	h.exec(t, `UPDATE accounts SET current_balance=current_balance+700 WHERE workspace_id='workspace-rbac' AND id='checking-a'`)
	var balance int64
	if err := h.db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='workspace-rbac' AND id='checking-a'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	// The purchase moved -200000 and the cancellation compensated +200000:
	// only the independent +700 may remain on top of the fixture balance.
	if want := int64(200000000) + 700; balance != want {
		t.Fatalf("independent movement balance=%d want=%d", balance, want)
	}
	t.Logf("RECONCILIATION delete=409 update=409 toggle=409 move=409 refund=409 bulk=409 settlement=404 balances_unchanged=true independent_movement=applied")
}

func TestB7GateCancellationWorkspaceIsolation(t *testing.T) {
	h := b7GateHarness(t)
	id := b7GateCreateFunded(t, h, "Gate C isolamento", "1.000,00")
	b7GateCancelLot(t, h, id)
	foreign := h.session(t, "admin-user", "workspace-b")

	forms := map[string]url.Values{
		"/compras":  b7GatePurchaseForm("1,00"),
		"/editar":   {"name": {"Invasão"}, "institution": {"Banco"}, "kind": {"CDB"}, "tax_regime": {"MANUAL"}},
		"/valuations": {"valuation_date": {"2026-04-01"}, "amount_cents": {"100"}},
		"/retiradas": investmentI22Form("initial:" + id),
		"/resgatar": {"redeemed_date": {"2026-04-01"}, "final_amount": {"1.000,00"}},
		"/arquivar": {},
	}
	if rr := h.request(t, "GET", "/investimentos/"+id, foreign, nil, false, false); rr.Code != http.StatusNotFound {
		t.Fatalf("foreign read: status=%d body=%q", rr.Code, rr.Body.String())
	}
	for suffix, form := range forms {
		if rr := h.request(t, "POST", "/investimentos/"+id+suffix, foreign, form, true, false); rr.Code != http.StatusNotFound {
			t.Fatalf("foreign POST %s: status=%d want=404 body=%q", suffix, rr.Code, rr.Body.String())
		}
	}
	var leaked int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='workspace-b'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("workspace-b receipts=%d err=%v", leaked, err)
	}

	// The receipt of one workspace never reaches the other workspace's lots.
	h.exec(t, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('checking-b', 'workspace-b', 'Corrente B', 'CHECKING')`)
	other, err := services.CreateInvestment(context.Background(), h.db, services.CreateInvestmentInput{
		WorkspaceID: "workspace-b", AccountID: "inv-b", SourceAccountID: "checking-b", CreatedBy: "admin-user",
		RequestID: uuid.NewString(), Name: "Lote B ativo", Institution: "Banco B", Kind: "CDB",
		PrincipalCents: 50000, TaxRegime: "MANUAL", AppliedDate: "2026-01-15",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := services.UpsertInvestmentValuation(context.Background(), h.db, services.UpsertInvestmentValuationInput{
		WorkspaceID: "workspace-b", InvestmentID: other.ID, ActingUserID: "admin-user", ValuationDate: "2026-03-01", AmountCents: 51000,
	}); err != nil {
		t.Fatalf("foreign active lot valuation: %v", err)
	}
	if err := services.UpdateInvestment(context.Background(), h.db, services.UpdateInvestmentInput{
		WorkspaceID: "workspace-b", InvestmentID: other.ID, UpdatedAt: time.Now().Unix(),
		Name: "Lote B editado", Institution: "Banco B", Kind: "CDB", TaxRegime: "MANUAL",
	}); err != nil {
		t.Fatalf("foreign active lot edit: %v", err)
	}
	t.Logf("ISOLATION foreign_read=404 foreign_writes=404 leak=0 foreign_active_lot=editable")
}
