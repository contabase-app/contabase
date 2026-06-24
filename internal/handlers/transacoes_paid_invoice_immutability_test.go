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
)

func TestPaidInvoiceTransactionEditIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	form := url.Values{}
	form.Set("valor", "300,00")
	form.Set("descricao", "Compra Alterada")
	form.Set("data", "2026-07-05")
	form.Set("tipo", "despesa")
	form.Set("origem_conta_id", "card-test")
	form.Set("categoria_id", "cat-expense")
	form.Set("status_pagamento", "paid")

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "purchase-test")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	assertInvoicePaidAmountAndTotal(t, db, "invoice-2026-08", 25000)
}

func TestPaidInvoiceTransactionDeleteIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	req := httptest.NewRequest(http.MethodDelete, "/transacoes/purchase-test", nil)
	rr := httptest.NewRecorder()

	handler.HandleDeletarTransacao(rr, req, "purchase-test")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	assertRowExists(t, db, "transactions", "purchase-test")
	assertInvoicePaidAmountAndTotal(t, db, "invoice-2026-08", 25000)
}

func TestPaidInvoiceTransactionBulkDeleteAndBulkUpdateAreBlocked(t *testing.T) {
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
	if deleteRR.Code != http.StatusConflict {
		t.Fatalf("bulk delete status = %d, want %d", deleteRR.Code, http.StatusConflict)
	}

	updateForm := url.Values{}
	updateForm.Add("ids[]", "purchase-test")
	updateForm.Set("status_pagamento", "pending")
	updateReq := httptest.NewRequest(http.MethodPost, "/transacoes/bulk-update", strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRR := httptest.NewRecorder()
	handler.HandleBulkUpdate(updateRR, updateReq)
	if updateRR.Code != http.StatusConflict {
		t.Fatalf("bulk update status = %d, want %d", updateRR.Code, http.StatusConflict)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = ?`, "purchase-test").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q, want paid", status)
	}
	assertRowExists(t, db, "transactions", "purchase-test")
	assertInvoicePaidAmountAndTotal(t, db, "invoice-2026-08", 25000)
}

func TestPaidInvoiceTransactionToggleIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/toggle", nil)
	rr := httptest.NewRecorder()

	handler.HandleTogglePagamento(rr, req, "purchase-test")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	assertInvoicePaidAmountAndTotal(t, db, "invoice-2026-08", 25000)
}

func TestPaidInvoiceTransactionSeriesUpdateIsBlockedWhenScopeTouchesPaidInvoice(t *testing.T) {
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
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 35000 WHERE id = 'invoice-2026-08'`)

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

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}

	var paidAmount int64
	if err := db.QueryRow(`SELECT amount FROM transactions WHERE id = ?`, "series-paid-1").Scan(&paidAmount); err != nil {
		t.Fatalf("query series-paid-1: %v", err)
	}
	var openAmount int64
	if err := db.QueryRow(`SELECT amount FROM transactions WHERE id = ?`, "series-open-2").Scan(&openAmount); err != nil {
		t.Fatalf("query series-open-2: %v", err)
	}
	if paidAmount != 10000 || openAmount != 10000 {
		t.Fatalf("series amounts changed after blocked update: paid=%d open=%d", paidAmount, openAmount)
	}
	assertInvoicePaidAmountAndTotal(t, db, "invoice-2026-08", 35000)
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

func assertInvoicePaidAmountAndTotal(t *testing.T, db *sql.DB, invoiceID string, wantAmount int64) {
	t.Helper()

	var paidAmount int64
	if err := db.QueryRow(`SELECT paid_amount FROM invoices WHERE id = ?`, invoiceID).Scan(&paidAmount); err != nil {
		t.Fatalf("query paid_amount: %v", err)
	}
	if paidAmount != wantAmount {
		t.Fatalf("paid_amount = %d, want %d", paidAmount, wantAmount)
	}

	total, err := sumInvoiceTotal(db, "ws-test", invoiceID)
	if err != nil {
		t.Fatalf("sumInvoiceTotal: %v", err)
	}
	if total != wantAmount {
		t.Fatalf("invoice total = %d, want %d", total, wantAmount)
	}
}
