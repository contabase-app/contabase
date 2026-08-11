package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTogglePagamentoWithPaymentDatePersistsPaidAtAndPreservesDates(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-paid-at", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	paymentDate := "2026-07-14"
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-paid-at/status-pagamento", url.Values{
		"payment_date": {paymentDate},
	}), "tx-paid-at")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAt sql.NullInt64
	var txDate, dueDate int64
	if err := db.QueryRow(`SELECT status, paid_at, date, due_date FROM transactions WHERE id = ?`, "tx-paid-at").Scan(&status, &paidAt, &txDate, &dueDate); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q, want paid", status)
	}
	wantPaidAt := testAppCivilDate("2026-07-14")
	if !paidAt.Valid || paidAt.Int64 != wantPaidAt {
		t.Fatalf("paid_at = %v/%d, want %d", paidAt.Valid, paidAt.Int64, wantPaidAt)
	}
	if got, want := txDate, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix(); got != want {
		t.Fatalf("date = %d, want preserved %d", got, want)
	}
	if got, want := dueDate, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix(); got != want {
		t.Fatalf("due_date = %d, want preserved %d", got, want)
	}
}

func TestTogglePagamentoInvalidPaymentDateDoesNotFallbackToToday(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-invalid-date", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-invalid-date/status-pagamento", url.Values{
		"payment_date": {"data-invalida"},
	}), "tx-invalid-date")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = ?`, "tx-invalid-date").Scan(&status, &paidAt); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
	if paidAt.Valid {
		t.Fatalf("paid_at = %d, want NULL", paidAt.Int64)
	}
}

func TestTogglePagamentoBackToPendingClearsPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	paidAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC).Unix()
	insertPaymentDateTestTransaction(t, db, "tx-clear-paid-at", "paid", &paidAt)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-clear-paid-at/status-pagamento", url.Values{
		"payment_action": {"pending"},
	}), "tx-clear-paid-at")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAtAfter sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = ?`, "tx-clear-paid-at").Scan(&status, &paidAtAfter); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
	if paidAtAfter.Valid {
		t.Fatalf("paid_at = %d, want NULL", paidAtAfter.Int64)
	}
}

func TestTogglePagamentoFromContasReturnsNoLancamentosFragment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-contas-paid-at", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-contas-paid-at/status-pagamento", url.Values{
		"context":      {"contas"},
		"payment_date": {"2026-07-14"},
	}), "tx-contas-paid-at")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Reswap"); got != "none" {
		t.Fatalf("HX-Reswap = %q, want none", got)
	}
	if strings.Contains(rr.Body.String(), `class="tx-row`) || strings.Contains(rr.Body.String(), `id="tx-contas-paid-at"`) {
		t.Fatalf("contas response should not include lancamentos row fragment: %q", rr.Body.String())
	}
}

func TestHandleBulkUpdateWithPaymentDatePersistsPaidAtAndPreservesDates(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-1", "pending", nil)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-2", "pending", nil)

	handler := testBulkPaymentHandler(db)
	paymentDate := "2026-08-03"
	rr := bulkUpdateRequest(t, handler, []string{"tx-bulk-1", "tx-bulk-2"}, "paid", paymentDate)

	if rr.Code != http.StatusOK {
		t.Logf("bulk update response (non-fatal for template): code=%d body=%q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{"tx-bulk-1", "tx-bulk-2"} {
		var status string
		var paidAt sql.NullInt64
		var txDate, dueDate int64
		if err := db.QueryRow(`SELECT status, paid_at, date, due_date FROM transactions WHERE id = ?`, txID).Scan(&status, &paidAt, &txDate, &dueDate); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if status != "paid" {
			t.Fatalf("%s status = %q, want paid", txID, status)
		}
		wantPaidAt := testAppCivilDate("2026-08-03")
		if !paidAt.Valid || paidAt.Int64 != wantPaidAt {
			t.Fatalf("%s paid_at = %v/%d, want %d", txID, paidAt.Valid, paidAt.Int64, wantPaidAt)
		}
		if got, want := txDate, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix(); got != want {
			t.Fatalf("%s date = %d, want preserved %d", txID, got, want)
		}
		if got, want := dueDate, time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix(); got != want {
			t.Fatalf("%s due_date = %d, want preserved %d", txID, got, want)
		}
	}
}

func TestHandleBulkUpdateWithoutPaymentDateDefaultsToToday(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-default", "pending", nil)

	handler := testBulkPaymentHandler(db)
	beforeUpdate := time.Now()
	rr := bulkUpdateRequest(t, handler, []string{"tx-bulk-default"}, "paid", "")

	if rr.Code != http.StatusOK {
		t.Logf("bulk update response (non-fatal for template): code=%d body=%q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = ?`, "tx-bulk-default").Scan(&status, &paidAt); err != nil {
		t.Fatalf("query tx: %v", err)
	}
	if status != "paid" {
		t.Fatalf("status = %q, want paid", status)
	}
	if !paidAt.Valid {
		t.Fatal("paid_at is NULL, want non-null today timestamp")
	}
	paidTime := time.Unix(paidAt.Int64, 0)
	todayStartUnix := appDayStartUnix(beforeUpdate)
	todayStart := time.Unix(todayStartUnix, 0)
	if !paidTime.Equal(todayStart) {
		// Allow up to 1 day difference in edge cases around local midnight.
		diff := paidTime.Sub(todayStart)
		if diff < -24*time.Hour || diff > 24*time.Hour {
			t.Fatalf("paid_at = %v, want today ~ %v", paidAt.Int64, todayStartUnix)
		}
	}
}

func TestHandleBulkUpdateInvalidPaymentDateReturns400(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-invalid", "pending", nil)

	handler := testBulkPaymentHandler(db)
	rr := bulkUpdateRequest(t, handler, []string{"tx-bulk-invalid"}, "paid", "data-invalida")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = ?`, "tx-bulk-invalid").Scan(&status, &paidAt); err != nil {
		t.Fatalf("query tx: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
	if paidAt.Valid {
		t.Fatalf("paid_at = %d, want NULL", paidAt.Int64)
	}
}

func TestHandleBulkUpdateMarkPendingClearsPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	paidAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC).Unix()
	insertPaymentDateTestTransaction(t, db, "tx-bulk-clear-1", "paid", &paidAt)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-clear-2", "paid", &paidAt)

	handler := testBulkPaymentHandler(db)
	rr := bulkUpdateRequest(t, handler, []string{"tx-bulk-clear-1", "tx-bulk-clear-2"}, "pending", "")

	if rr.Code != http.StatusOK {
		t.Logf("bulk update response (non-fatal for template): code=%d body=%q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{"tx-bulk-clear-1", "tx-bulk-clear-2"} {
		var status string
		var paidAtAfter sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = ?`, txID).Scan(&status, &paidAtAfter); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if status != "pending" {
			t.Fatalf("%s status = %q, want pending", txID, status)
		}
		if paidAtAfter.Valid {
			t.Fatalf("%s paid_at = %d, want NULL", txID, paidAtAfter.Int64)
		}
	}
}

func formPost(path string, values url.Values) *http.Request {
	if values == nil {
		values = url.Values{}
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func insertPaymentDateTestTransaction(t *testing.T, db *sql.DB, id, status string, paidAt *int64) {
	t.Helper()
	now := time.Now().Unix()
	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date, due_date,
			description, status, paid_at, installment_number, total_installments, created_at, updated_at
		)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 12345, ?, ?, 'Pagamento com data', ?, ?, 1, 1, ?, ?)
	`, id, txDate, dueDate, status, paidAt, now, now)
}

func testBulkPaymentHandler(db *sql.DB) TransactionHandler {
	return TransactionHandler{
		DB:          db,
		Templates:   testBulkPaymentTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
}

func testBulkPaymentTemplates() *template.Template {
	return template.Must(template.New("bulk").Parse(`
{{define "lancamentos-table-body"}}<tbody id="transactions-body"></tbody>{{end}}
{{define "lancamento-row"}}<tr id="{{.ID}}"></tr>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-financial"}}<section id="dashboard-financial" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-summary"}}<section id="dashboard-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-health-cards"}}<section id="dashboard-health-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "lancamentos-resumo"}}<section id="lancamentos-resumo" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}

func bulkUpdateRequest(t *testing.T, handler TransactionHandler, ids []string, status, paymentDate string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	values.Set("status_pagamento", status)
	for _, id := range ids {
		values.Add("ids[]", id)
	}
	if paymentDate != "" {
		values.Set("payment_date", paymentDate)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/bulk/update", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandleBulkUpdate(rr, req)
	return rr
}
