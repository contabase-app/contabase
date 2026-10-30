package handlers

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistrarReembolsoReduzFaturaFechadaSemCriarReceita(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE invoices SET status='CLOSED' WHERE id='invoice-2026-08'`)

	h := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	h.HandleRegistrarReembolso(rr, formPost("/transacoes/purchase-test/reembolso", url.Values{
		"valor":     {"50,00"},
		"descricao": {"Estorno parcial da compra"},
	}), "purchase-test")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var refundAmount, incomeCount int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents), 0) FROM transaction_refunds WHERE workspace_id='ws-test' AND transaction_id='purchase-test' AND reversed_at IS NULL`).Scan(&refundAmount); err != nil {
		t.Fatal(err)
	}
	if refundAmount != 5000 {
		t.Fatalf("refund amount = %d, want 5000", refundAmount)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND type='INCOME'`).Scan(&incomeCount); err != nil {
		t.Fatal(err)
	}
	if incomeCount != 0 {
		t.Fatalf("income transactions = %d, want 0", incomeCount)
	}
	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 20000 || snapshot.Status != "CLOSED" {
		t.Fatalf("snapshot total/status = %d/%s, want 20000/CLOSED", snapshot.Total, snapshot.Status)
	}
	limitUsed, err := sumCardOutstandingLimit(db, "ws-test", "card-test")
	if err != nil {
		t.Fatal(err)
	}
	if limitUsed != 20000 {
		t.Fatalf("limit used = %d, want 20000", limitUsed)
	}
	projected, err := QueryInvoiceProjectedCashEvents(db, "ws-test", NewReportPeriodFromMonth(2026, 8))
	if err != nil {
		t.Fatal(err)
	}
	if len(projected) != 1 || projected[0].AmountCents != 20000 {
		t.Fatalf("projected invoice cash = %#v, want one event of 20000", projected)
	}
	reportExpense, err := (&RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-test"}).queryUncategorizedExpenses(context.Background(), 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if reportExpense != 20000 {
		t.Fatalf("report expense = %d, want net 20000", reportExpense)
	}
}

func TestRegistrarReembolsoBloqueiaExcessoEFaturaPaga(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		value  string
	}{
		{name: "exceeds purchase", status: "CLOSED", value: "250,01"},
		{name: "paid invoice", status: "PAID", value: "10,00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			execTestSQL(t, db, `UPDATE invoices SET status=? WHERE id='invoice-2026-08'`, tc.status)
			h := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
			rr := httptest.NewRecorder()
			h.HandleRegistrarReembolso(rr, formPost("/transacoes/purchase-test/reembolso", url.Values{
				"valor":     {tc.value},
				"descricao": {"Teste protegido"},
			}), "purchase-test")
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%q", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_refunds`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("refund count = %d, want 0", count)
			}
		})
	}
}

func TestRegistrarReembolsoGeralReduzSaldoAtualSemAlterarDespesaHistorica(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE invoices SET status='CLOSED' WHERE id='invoice-2026-08'`)

	h := FaturasHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	h.HandleRegistrarReembolsoGeral(rr, formPost("/cartoes/faturas/reembolso", url.Values{
		"invoice_id": {"invoice-2026-08"},
		"valor":      {"50,00"},
		"descricao":  {"Estorno de compra em fatura anterior"},
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var refundAmount, incomeCount int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents), 0) FROM invoice_refunds WHERE workspace_id='ws-test' AND invoice_id='invoice-2026-08' AND reversed_at IS NULL`).Scan(&refundAmount); err != nil {
		t.Fatal(err)
	}
	if refundAmount != 5000 {
		t.Fatalf("refund amount = %d, want 5000", refundAmount)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND type='INCOME'`).Scan(&incomeCount); err != nil {
		t.Fatal(err)
	}
	if incomeCount != 0 {
		t.Fatalf("income transactions = %d, want 0", incomeCount)
	}
	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 20000 || snapshot.Status != "CLOSED" {
		t.Fatalf("snapshot total/status = %d/%s, want 20000/CLOSED", snapshot.Total, snapshot.Status)
	}
	limitUsed, err := sumCardOutstandingLimit(db, "ws-test", "card-test")
	if err != nil {
		t.Fatal(err)
	}
	if limitUsed != 20000 {
		t.Fatalf("limit used = %d, want 20000", limitUsed)
	}
	reportExpense, err := (&RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-test"}).queryUncategorizedExpenses(context.Background(), 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	if reportExpense != 25000 {
		t.Fatalf("report expense = %d, want original 25000", reportExpense)
	}
	if !strings.Contains(rr.Body.String(), `id="bottom-sheet-container" hx-swap-oob="innerHTML"`) {
		t.Fatalf("expected response to close refund sheet, got %q", rr.Body.String())
	}
}

func TestRegistrarReembolsoGeralBloqueiaExcessoEInvoicePaga(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       string
		paymentCents int64
		value        string
	}{
		{name: "exceeds pending balance", status: "CLOSED", paymentCents: 20000, value: "60,00"},
		{name: "paid invoice", status: "PAID", paymentCents: 0, value: "10,00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			execTestSQL(t, db, `UPDATE invoices SET status=? WHERE id='invoice-2026-08'`, tc.status)
			if tc.paymentCents > 0 {
				now := int64(1_800_000_000)
				execTestSQL(t, db, `
					INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
					VALUES ('general-refund-payment', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, ?, ?, 'manual', 'user-test', ?)
				`, tc.paymentCents, now, now)
			}
			h := FaturasHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
			rr := httptest.NewRecorder()
			h.HandleRegistrarReembolsoGeral(rr, formPost("/cartoes/faturas/reembolso", url.Values{
				"invoice_id": {"invoice-2026-08"},
				"valor":      {tc.value},
				"descricao":  {"Teste protegido"},
			}))
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%q", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_refunds`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("general refund count = %d, want 0", count)
			}
		})
	}
}

func TestAtualizarERemoverReembolsoDeItemMantemHistoricoAuditavel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	h := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}

	create := httptest.NewRecorder()
	h.HandleRegistrarReembolso(create, formPost("/transacoes/purchase-test/reembolso", url.Values{
		"valor": {"50,00"}, "descricao": {"Estorno inicial"},
	}), "purchase-test")
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%q", create.Code, create.Body.String())
	}
	var refundID string
	if err := db.QueryRow(`SELECT id FROM transaction_refunds WHERE transaction_id='purchase-test' AND reversed_at IS NULL`).Scan(&refundID); err != nil {
		t.Fatal(err)
	}

	update := httptest.NewRecorder()
	h.HandleAtualizarReembolso(update, formPost("/transacoes/purchase-test/reembolsos/"+refundID+"/editar", url.Values{
		"valor": {"75,00"}, "descricao": {"Estorno corrigido"},
	}), "purchase-test", refundID)
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%q", update.Code, update.Body.String())
	}
	var activeAmount, activeCount, reversedCount int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents), 0), COUNT(1) FROM transaction_refunds WHERE transaction_id='purchase-test' AND reversed_at IS NULL`).Scan(&activeAmount, &activeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_refunds WHERE transaction_id='purchase-test' AND reversed_at IS NOT NULL`).Scan(&reversedCount); err != nil {
		t.Fatal(err)
	}
	if activeAmount != 7500 || activeCount != 1 || reversedCount != 1 {
		t.Fatalf("transaction refunds active=%d/%d reversed=%d, want 7500/1/1", activeAmount, activeCount, reversedCount)
	}
	var replacementID string
	if err := db.QueryRow(`SELECT id FROM transaction_refunds WHERE transaction_id='purchase-test' AND reversed_at IS NULL`).Scan(&replacementID); err != nil {
		t.Fatal(err)
	}

	remove := httptest.NewRecorder()
	h.HandleRemoverReembolso(remove, formPost("/transacoes/purchase-test/reembolsos/"+replacementID+"/remover", nil), "purchase-test", replacementID)
	if remove.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body=%q", remove.Code, remove.Body.String())
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_refunds WHERE transaction_id='purchase-test' AND reversed_at IS NULL`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 0 {
		t.Fatalf("active transaction refunds = %d, want 0", activeCount)
	}
	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 25000 {
		t.Fatalf("invoice total after refund removal = %d, want 25000", snapshot.Total)
	}
}

func TestAtualizarERemoverReembolsoGeralMantemHistoricoAuditavel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	h := FaturasHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}

	create := httptest.NewRecorder()
	h.HandleRegistrarReembolsoGeral(create, formPost("/cartoes/faturas/reembolso", url.Values{
		"invoice_id": {"invoice-2026-08"}, "valor": {"50,00"}, "descricao": {"Crédito inicial"},
	}))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d, body=%q", create.Code, create.Body.String())
	}
	var refundID string
	if err := db.QueryRow(`SELECT id FROM invoice_refunds WHERE invoice_id='invoice-2026-08' AND reversed_at IS NULL`).Scan(&refundID); err != nil {
		t.Fatal(err)
	}

	update := httptest.NewRecorder()
	h.HandleAtualizarReembolsoGeral(update, formPost("/cartoes/faturas/reembolsos/"+refundID+"/editar", url.Values{
		"invoice_id": {"invoice-2026-08"}, "valor": {"70,00"}, "descricao": {"Crédito corrigido"},
	}), refundID)
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%q", update.Code, update.Body.String())
	}
	var activeAmount, activeCount, reversedCount int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents), 0), COUNT(1) FROM invoice_refunds WHERE invoice_id='invoice-2026-08' AND reversed_at IS NULL`).Scan(&activeAmount, &activeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_refunds WHERE invoice_id='invoice-2026-08' AND reversed_at IS NOT NULL`).Scan(&reversedCount); err != nil {
		t.Fatal(err)
	}
	if activeAmount != 7000 || activeCount != 1 || reversedCount != 1 {
		t.Fatalf("general refunds active=%d/%d reversed=%d, want 7000/1/1", activeAmount, activeCount, reversedCount)
	}
	var replacementID string
	if err := db.QueryRow(`SELECT id FROM invoice_refunds WHERE invoice_id='invoice-2026-08' AND reversed_at IS NULL`).Scan(&replacementID); err != nil {
		t.Fatal(err)
	}

	remove := httptest.NewRecorder()
	h.HandleRemoverReembolsoGeral(remove, formPost("/cartoes/faturas/reembolsos/"+replacementID+"/remover", url.Values{
		"invoice_id": {"invoice-2026-08"},
	}), replacementID)
	if remove.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body=%q", remove.Code, remove.Body.String())
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_refunds WHERE invoice_id='invoice-2026-08' AND reversed_at IS NULL`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 0 {
		t.Fatalf("active general refunds = %d, want 0", activeCount)
	}
	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Total != 25000 {
		t.Fatalf("invoice total after general refund removal = %d, want 25000", snapshot.Total)
	}
}

func TestRefundFormsPrefillOnlyItemAvailableAmount(t *testing.T) {
	for _, tc := range []struct {
		name        string
		path        string
		template    string
		data        any
		wantPrefill bool
	}{
		{
			name:        "transaction refund",
			path:        "templates/components/form_reembolso.html",
			template:    "form-reembolso",
			wantPrefill: true,
			data: TransactionRefundFormData{
				TransactionID: "tx", RemainingAmountInput: "39,99",
			},
		},
		{
			name:        "general invoice refund",
			path:        "templates/components/form_reembolso_fatura.html",
			template:    "form-reembolso-fatura",
			wantPrefill: false,
			data: InvoiceGeneralRefundFormData{
				InvoiceID: "invoice",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(projectRoot(), tc.path))
			if err != nil {
				t.Fatal(err)
			}
			tpl := template.Must(template.New("refund-form").Funcs(template.FuncMap{
				"assetPath": func(path string) string { return path },
			}).Parse(string(content)))
			var out strings.Builder
			if err := tpl.ExecuteTemplate(&out, tc.template, tc.data); err != nil {
				t.Fatal(err)
			}
			prefilled := strings.Contains(out.String(), `value="R$ 39,99"`)
			if prefilled != tc.wantPrefill {
				t.Fatalf("refund form prefill = %v, want %v: %s", prefilled, tc.wantPrefill, out.String())
			}
		})
	}
}
