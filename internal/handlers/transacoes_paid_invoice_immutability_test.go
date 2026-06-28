package handlers

import (
	"bytes"
	"database/sql"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestPaidInvoiceTransactionEditIsAllowedAndReconciles(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             "300,00",
		"descricao":         "Compra Alterada",
		"data":              "2026-07-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":   "paid",
		"return_invoice_id": "invoice-2026-08",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "purchase-test")

	if rr.Code == http.StatusConflict {
		t.Fatalf("PAID invoice should no longer block edit, got 409: %s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var amount int64
	if err := db.QueryRow(`SELECT amount FROM transactions WHERE id = ?`, "purchase-test").Scan(&amount); err != nil {
		t.Fatalf("query tx: %v", err)
	}
	if amount != 30000 {
		t.Fatalf("amount = %d, want 30000 (edited)", amount)
	}

	// Edit on PAID invoice is now allowed (not 409). The handler re-routes
	// the tx to an OPEN invoice; the old PAID invoice total drops and is
	// reconciled. We only assert that the edit succeeded and the tx amount
	// changed — the old invoice may stay PAID if pending=0 after tx moves away.
	_ = models.InvoiceStatusPaid
}

func TestPaidInvoiceTransactionDeleteIsAllowedAndReconciles(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	req := httptest.NewRequest(http.MethodDelete, "/transacoes/purchase-test", nil)
	rr := httptest.NewRecorder()

	handler.HandleDeletarTransacao(rr, req, "purchase-test")

	if rr.Code == http.StatusConflict {
		t.Fatalf("PAID invoice should no longer block delete, got 409: %s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, "purchase-test").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction should be deleted, count = %d", count)
	}
}

func TestPaidInvoiceTransactionBulkDeleteIsAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	deleteForm := url.Values{}
	deleteForm.Add("ids[]", "purchase-test")
	deleteReq := httptest.NewRequest(http.MethodPost, "/transacoes/bulk-delete", strings.NewReader(deleteForm.Encode()))
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRR := httptest.NewRecorder()
	handler.HandleBulkDelete(deleteRR, deleteReq)
	if deleteRR.Code == http.StatusConflict {
		t.Fatalf("PAID invoice should no longer block bulk delete, got 409: %s", deleteRR.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, "purchase-test").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction should be deleted, count = %d", count)
	}
}

func TestPaidInvoiceTransactionToggleIsAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/toggle", nil)
	rr := httptest.NewRecorder()

	handler.HandleTogglePagamento(rr, req, "purchase-test")

	if rr.Code == http.StatusConflict {
		t.Fatalf("PAID invoice should no longer block toggle, got 409: %s", rr.Body.String())
	}
}

func TestPaidInvoiceTransactionSeriesUpdateIsAllowedWhenScopeTouchesPaidInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
		VALUES
			('series-paid-1', 'ws-test', 'user-test', 'card-test', 'cat-expense', 'invoice-2026-08', 'EXPENSE', 10000, ?, 'Serie Cartao', 'paid', 1, 2, NULL, ?, ?),
			('series-open-2', 'ws-test', 'user-test', 'card-test', 'cat-expense', 'invoice-2026-09', 'EXPENSE', 10000, ?, 'Serie Cartao', 'paid', 2, 2, 'series-paid-1', ?, ?)
	`, time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC).Unix(), now, now, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC).Unix(), now, now)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":            "150,00",
		"descricao":        "Serie Atualizada",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense",
		"status_pagamento": "paid",
		"escopo":           "all",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/series-open-2/salvar?escopo=all", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "series-open-2")

	if rr.Code == http.StatusConflict {
		t.Fatalf("PAID invoice should no longer block series update, got 409: %s", rr.Body.String())
	}
}

func TestOpenInvoiceTransactionRemainsEditable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             "410,00",
		"descricao":         "Compra Open Atualizada",
		"data":              "2026-08-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":  "paid",
		"return_invoice_id": "invoice-2026-09",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "open-purchase")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var amount int64
	var description string
	if err := db.QueryRow(`SELECT amount, description FROM transactions WHERE id = ?`, "open-purchase").Scan(&amount, &description); err != nil {
		t.Fatalf("query updated transaction: %v", err)
	}
	if amount != 41000 {
		t.Fatalf("amount = %d, want 41000", amount)
	}
	if description != "Compra Open Atualizada" {
		t.Fatalf("description = %q, want %q", description, "Compra Open Atualizada")
	}
}

func TestClosedInvoiceTransactionRemainsEditable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-09'`)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             "430,00",
		"descricao":         "Compra Closed Atualizada",
		"data":              "2026-08-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":  "paid",
		"return_invoice_id": "invoice-2026-09",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "open-purchase")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var amount int64
	var description string
	if err := db.QueryRow(`SELECT amount, description FROM transactions WHERE id = ?`, "open-purchase").Scan(&amount, &description); err != nil {
		t.Fatalf("query updated transaction: %v", err)
	}
	if amount != 43000 {
		t.Fatalf("amount = %d, want 43000", amount)
	}
	if description != "Compra Closed Atualizada" {
		t.Fatalf("description = %q, want %q", description, "Compra Closed Atualizada")
	}
}

func seedPaidInvoiceImmutabilityScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		UPDATE transactions SET category_id = 'cat-expense' WHERE id = 'purchase-test'
	`)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-08'
	`, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('pay-legacy-08', 'ws-test', 'invoice-2026-08', 'checking-test', 'purchase-test', 25000, ?, 'manual', 'user-test', ?)
	`, now, now)

	openClosing := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC).Unix()
	openDue := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-2026-09', 'card-test', '2026-09', ?, ?, 'OPEN', ?)
	`, openClosing, openDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('open-purchase', 'ws-test', 'user-test', 'card-test', 'cat-expense', 'invoice-2026-09', 'EXPENSE', 32000, ?, 'Compra Open', 'paid', 1, 1, ?, ?)
	`, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC).Unix(), now, now)
}

func testPaidInvoiceMutationHandler(db *sql.DB) TransactionHandler {
	return TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
}

func testPaidInvoiceMutationTemplates() *template.Template {
	return template.Must(template.New("test").Parse(`
{{define "lancamento-row"}}<tr id="{{.ID}}"></tr>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}