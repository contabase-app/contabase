package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
)

func TestHandlePagarFaturaLiquidaFaturaDebitaContaECriaProxima(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `hx-swap-oob="outerHTML"`) {
		t.Fatalf("expected OOB dashboard fragments, got %q", rr.Body.String())
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?
	`, "invoice-2026-08").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid amount = %d, want 25000", paidAmount)
	}
	if !paidAt.Valid || paidAt.Int64 <= 0 {
		t.Fatalf("paid_at = %v, want unix timestamp", paidAt)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 75000 {
		t.Fatalf("balance = %d, want 75000", balance)
	}

	var paymentDescription string
	var paymentAmount int64
	if err := db.QueryRow(`
		SELECT description, amount
		FROM transactions
		WHERE account_id = ? AND invoice_id IS NULL AND description = ?
	`, "checking-test", "Pagamento de Fatura - Cartao Teste").Scan(&paymentDescription, &paymentAmount); err != nil {
		t.Fatalf("payment transaction query: %v", err)
	}
	if paymentAmount != 25000 {
		t.Fatalf("payment transaction amount = %d, want 25000", paymentAmount)
	}

	var nextStatus string
	if err := db.QueryRow(`
		SELECT status FROM invoices WHERE account_id = ? AND reference = ?
	`, "card-test", "2026-09").Scan(&nextStatus); err != nil {
		t.Fatalf("next invoice query: %v", err)
	}
	if nextStatus != "OPEN" {
		t.Fatalf("next invoice status = %q, want OPEN", nextStatus)
	}

	var ipCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count = %d, want 1", ipCount)
	}

	var ip models.InvoicePayment
	if err := db.QueryRow(`
		SELECT id, workspace_id, invoice_id, account_id, transaction_id,
		       amount_cents, paid_at, note, source, reversed_at, created_by, created_at
		FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(
		&ip.ID, &ip.WorkspaceID, &ip.InvoiceID, &ip.AccountID, &ip.TransactionID,
		&ip.AmountCents, &ip.PaidAt, &ip.Note, &ip.Source, &ip.ReversedAt, &ip.CreatedBy, &ip.CreatedAt,
	); err != nil {
		t.Fatalf("invoice_payments scan: %v", err)
	}
	if ip.WorkspaceID != "ws-test" {
		t.Fatalf("ip workspace_id = %q, want ws-test", ip.WorkspaceID)
	}
	if ip.InvoiceID != "invoice-2026-08" {
		t.Fatalf("ip invoice_id = %q, want invoice-2026-08", ip.InvoiceID)
	}
	if ip.AccountID != "checking-test" {
		t.Fatalf("ip account_id = %q, want checking-test (payment account)", ip.AccountID)
	}
	if ip.TransactionID == nil || *ip.TransactionID == "" {
		t.Fatalf("ip transaction_id should be populated")
	}
	if ip.AmountCents != 25000 {
		t.Fatalf("ip amount_cents = %d, want 25000", ip.AmountCents)
	}
	if ip.PaidAt <= 0 {
		t.Fatalf("ip paid_at = %d, want > 0", ip.PaidAt)
	}
	if ip.Note != nil {
		t.Fatalf("ip note should be nil")
	}
	if ip.Source != "manual" {
		t.Fatalf("ip source = %q, want manual", ip.Source)
	}
	if ip.ReversedAt != nil {
		t.Fatalf("ip reversed_at should be nil")
	}
	if ip.CreatedBy == nil || *ip.CreatedBy != "user-test" {
		t.Fatalf("ip created_by = %v, want user-test", ip.CreatedBy)
	}
	if ip.CreatedAt <= 0 {
		t.Fatalf("ip created_at = %d, want > 0", ip.CreatedAt)
	}
}

func TestHandlePagarFaturaQuitarSemConfirmacaoNaoCriaPagamento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}

	var ipCount, txCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 0 {
		t.Fatalf("invoice_payments count = %d, want 0", ipCount)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE account_id = ? AND description = ?`, "checking-test", invoicePaymentDescription("Cartao Teste")).Scan(&txCount); err != nil {
		t.Fatalf("payment tx count query: %v", err)
	}
	if txCount != 0 {
		t.Fatalf("payment tx count = %d, want 0", txCount)
	}

	var status string
	var paidAmount int64
	if err := db.QueryRow(`SELECT status, COALESCE(paid_amount, 0) FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "OPEN" || paidAmount != 0 {
		t.Fatalf("invoice status/paid_amount = %q/%d, want OPEN/0", status, paidAmount)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 100000 {
		t.Fatalf("balance = %d, want 100000", balance)
	}
}

func TestHandlePagarFaturaContaVaziaBloqueiaSemPagamento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %q", rr.Code, rr.Body.String())
	}

	var ipCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 0 {
		t.Fatalf("invoice_payments count = %d, want 0", ipCount)
	}
}

func TestHandlePagarFaturaJaPagaNaoCriaNovoInvoicePayment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandlePagarFatura(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first payment status = %d, want 200", rr.Code)
	}

	var ipCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count after first payment = %d, want 1", ipCount)
	}

	var paidAmount int64
	if err := db.QueryRow(`SELECT paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&paidAmount); err != nil {
		t.Fatalf("paid_amount query: %v", err)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid_amount after first payment = %d, want 25000", paidAmount)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 75000 {
		t.Fatalf("balance after first payment = %d, want 75000", balance)
	}

	var txCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM transactions
		WHERE account_id = ? AND description = ?
	`, "checking-test", "Pagamento de Fatura - Cartao Teste").Scan(&txCount); err != nil {
		t.Fatalf("payment tx count query: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("payment tx count after first payment = %d, want 1", txCount)
	}

	rr2 := httptest.NewRecorder()
	handler.HandlePagarFatura(rr2, req)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("second payment status = %d, want 409", rr2.Code)
	}

	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count query after second attempt: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count after second attempt = %d, want 1", ipCount)
	}

	if err := db.QueryRow(`SELECT paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&paidAmount); err != nil {
		t.Fatalf("paid_amount query after second attempt: %v", err)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid_amount after second attempt = %d, want 25000", paidAmount)
	}

	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query after second attempt: %v", err)
	}
	if balance != 75000 {
		t.Fatalf("balance after second attempt = %d, want 75000", balance)
	}

	if err := db.QueryRow(`
		SELECT COUNT(1) FROM transactions
		WHERE account_id = ? AND description = ?
	`, "checking-test", "Pagamento de Fatura - Cartao Teste").Scan(&txCount); err != nil {
		t.Fatalf("payment tx count query after second attempt: %v", err)
	}
	if txCount != 1 {
		t.Fatalf("payment tx count after second attempt = %d, want 1", txCount)
	}
}

func TestHandlePagarFaturaComPagamentoParcialQuitaApenasSaldoPendente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-extra-partial', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 10000, ?, 'Compra Extra Parcial', 'paid', 1, 1, ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		UPDATE accounts SET current_balance = 90000 WHERE id = 'checking-test' AND workspace_id = 'ws-test'
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('payment-partial-active-tx', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 10000, ?, ?, 'paid', 1, 1, ?, ?)
	`, now-200, invoicePaymentDescription("Cartao Teste"), now-200, now-200)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-partial-active', 'ws-test', 'invoice-2026-08', 'checking-test', 'payment-partial-active-tx', 10000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now-200, now-200)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-partial-reversed', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 8000, ?, NULL, 'manual', ?, 'user-test', ?)
	`, now-100, now-50, now-100)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAmount int64
	if err := db.QueryRow(`SELECT status, paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
	if paidAmount != 35000 {
		t.Fatalf("paid_amount = %d, want 35000", paidAmount)
	}

	var newPaymentAmount int64
	if err := db.QueryRow(`
		SELECT amount_cents
		FROM invoice_payments
		WHERE invoice_id = ? AND id != 'ip-partial-active' AND reversed_at IS NULL
	`, "invoice-2026-08").Scan(&newPaymentAmount); err != nil {
		t.Fatalf("new invoice_payment query: %v", err)
	}
	if newPaymentAmount != 25000 {
		t.Fatalf("new invoice_payment amount = %d, want 25000", newPaymentAmount)
	}

	var paymentTxAmount int64
	if err := db.QueryRow(`
		SELECT amount
		FROM transactions
		WHERE account_id = ?
		  AND description = ?
		  AND id != 'payment-partial-active-tx'
	`, "checking-test", invoicePaymentDescription("Cartao Teste")).Scan(&paymentTxAmount); err != nil {
		t.Fatalf("payment tx query: %v", err)
	}
	if paymentTxAmount != 25000 {
		t.Fatalf("payment transaction amount = %d, want 25000", paymentTxAmount)
	}

	var wrongFullPaymentCount int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM invoice_payments
		WHERE invoice_id = ? AND amount_cents = 35000 AND reversed_at IS NULL
	`, "invoice-2026-08").Scan(&wrongFullPaymentCount); err != nil {
		t.Fatalf("wrong full payment count query: %v", err)
	}
	if wrongFullPaymentCount != 0 {
		t.Fatalf("unexpected full amount payment count = %d, want 0", wrongFullPaymentCount)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE account_id = ?
		  AND description = ?
		  AND amount = 35000
	`, "checking-test", invoicePaymentDescription("Cartao Teste")).Scan(&wrongFullPaymentCount); err != nil {
		t.Fatalf("wrong full payment tx count query: %v", err)
	}
	if wrongFullPaymentCount != 0 {
		t.Fatalf("unexpected full amount payment transaction count = %d, want 0", wrongFullPaymentCount)
	}

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.Total.Reais != "350" || data.Total.Cents != ",00" {
		t.Fatalf("Total = R$ %s%s, want R$ 350,00", data.Total.Reais, data.Total.Cents)
	}
	if data.TotalPaid.Reais != "350" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 350,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "0" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 0,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 2 {
		t.Fatalf("active invoice payments = %d, want 2", len(data.InvoicePayments))
	}
}

func TestHandlePagarFaturaPagamentoParcialMenorQueSaldoMantemAberta(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	paymentDateInput := time.Now().UTC().Format("2006-01-02")
	paymentDateUnix, err := parseDate(paymentDateInput)
	if err != nil {
		t.Fatalf("parse payment date: %v", err)
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "partial")
	form.Set("payment_amount", "100,00")
	form.Set("payment_date", paymentDateInput)
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	for _, fragment := range []string{`id="faturas-content"`, `id="dashboard-balance" hx-swap-oob="outerHTML"`, `id="dashboard-accounts" hx-swap-oob="outerHTML"`, `id="dashboard-cards" hx-swap-oob="outerHTML"`} {
		if !strings.Contains(rr.Body.String(), fragment) {
			t.Fatalf("missing HTMX/OOB fragment %q in %q", fragment, rr.Body.String())
		}
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "OPEN" {
		t.Fatalf("invoice status = %q, want OPEN", status)
	}
	if paidAmount != 10000 {
		t.Fatalf("paid_amount = %d, want 10000", paidAmount)
	}
	if paidAt.Valid {
		t.Fatalf("paid_at = %v, want NULL while invoice remains open", paidAt)
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, "checking-test").Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 90000 {
		t.Fatalf("balance = %d, want 90000", balance)
	}

	var ipAmount, ipPaidAt, txAmount, txDate int64
	var transactionID, txStatus string
	var txInvoiceID sql.NullString
	if err := db.QueryRow(`
		SELECT ip.amount_cents, ip.paid_at, ip.transaction_id, t.amount, t.date, t.status, t.invoice_id
		FROM invoice_payments ip
		JOIN transactions t ON t.id = ip.transaction_id AND t.workspace_id = ip.workspace_id
		WHERE ip.invoice_id = ? AND ip.reversed_at IS NULL
	`, "invoice-2026-08").Scan(&ipAmount, &ipPaidAt, &transactionID, &txAmount, &txDate, &txStatus, &txInvoiceID); err != nil {
		t.Fatalf("invoice payment join query: %v", err)
	}
	if transactionID == "" {
		t.Fatalf("transaction_id should be populated")
	}
	if ipAmount != 10000 || txAmount != 10000 {
		t.Fatalf("payment amounts = ip %d tx %d, want 10000", ipAmount, txAmount)
	}
	if ipPaidAt != paymentDateUnix || txDate != paymentDateUnix {
		t.Fatalf("payment dates = ip %d tx %d, want %d", ipPaidAt, txDate, paymentDateUnix)
	}
	if txStatus != "paid" {
		t.Fatalf("transaction status = %q, want paid", txStatus)
	}
	if txInvoiceID.Valid {
		t.Fatalf("payment transaction invoice_id = %q, want NULL", txInvoiceID.String)
	}

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	assertMoneyDisplay(t, "TotalPaid", data.TotalPaid, "100", ",00")
	assertMoneyDisplay(t, "PendingAmount", data.PendingAmount, "150", ",00")
	if !data.HasPayments || len(data.InvoicePayments) != 1 {
		t.Fatalf("invoice payments = has %v len %d, want one active payment", data.HasPayments, len(data.InvoicePayments))
	}

	_, monthlyExpense := queryDashboardMonthlySummary(db, "ws-test", time.Now().UTC())
	if monthlyExpense != 10000 {
		t.Fatalf("dashboard monthly expense = %d, want 10000", monthlyExpense)
	}
}

func TestHandlePagarFaturaPagamentoParcialIgualSaldoQuitaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := FaturasHandler{
		DB:          db,
		Templates:   testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "partial")
	form.Set("payment_amount", "250,00")
	form.Set("payment_date", time.Now().UTC().Format("2006-01-02"))
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
	if paidAmount != 25000 {
		t.Fatalf("paid_amount = %d, want 25000", paidAmount)
	}
	if !paidAt.Valid || paidAt.Int64 <= 0 {
		t.Fatalf("paid_at = %v, want timestamp", paidAt)
	}
}

func TestHandlePagarFaturaPagamentoParcialValorInvalidoRejeita(t *testing.T) {
	cases := []struct {
		name   string
		amount string
	}{
		{name: "above pending", amount: "300,00"},
		{name: "zero", amount: "0"},
		{name: "negative", amount: "-10,00"},
		{name: "empty partial", amount: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()

			seedInvoicePaymentScenario(t, db)

			handler := FaturasHandler{
				DB:          db,
				Templates:   testInvoicePaymentTemplates(t),
				WorkspaceID: "ws-test",
				UserID:      "user-test",
			}

			form := url.Values{}
			form.Set("invoice_id", "invoice-2026-08")
			form.Set("payment_account_id", "checking-test")
			form.Set("payment_mode", "partial")
			form.Set("payment_amount", tc.amount)
			req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()

			handler.HandlePagarFatura(rr, req)

			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422, body = %q", rr.Code, rr.Body.String())
			}

			var ipCount int
			if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "invoice-2026-08").Scan(&ipCount); err != nil {
				t.Fatalf("invoice_payments count query: %v", err)
			}
			if ipCount != 0 {
				t.Fatalf("invoice_payments count = %d, want 0", ipCount)
			}

			var status string
			var paidAmount int64
			if err := db.QueryRow(`SELECT status, COALESCE(paid_amount, 0) FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status, &paidAmount); err != nil {
				t.Fatalf("invoice query: %v", err)
			}
			if status != "OPEN" || paidAmount != 0 {
				t.Fatalf("invoice status/paid_amount = %q/%d, want OPEN/0", status, paidAmount)
			}
		})
	}
}

func TestHandleMoverFaturaAvulsaMoveParaProximaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		Templates:   testOOBTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/mover-fatura", nil)
	rr := httptest.NewRecorder()

	handler.HandleMoverFatura(rr, req, "purchase-test")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertTransactionInvoiceReference(t, db, "purchase-test", "2026-09")
	assertInvoiceExists(t, db, "card-test", "2026-09", "OPEN")
	if !strings.Contains(rr.Body.String(), `id="invoice-summary"`) || !strings.Contains(rr.Body.String(), `id="dashboard-cards"`) {
		t.Fatalf("expected invoice and dashboard OOB fragments, got %q", rr.Body.String())
	}
}

func TestHandleMoverFaturaParceladaMoveCadeiaFuturaEmCascata(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedInstallmentMoveScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		Templates:   testOOBTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/installment-2/mover-fatura", nil)
	rr := httptest.NewRecorder()

	handler.HandleMoverFatura(rr, req, "installment-2")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertTransactionInvoiceReference(t, db, "installment-1", "2026-08")
	assertTransactionInvoiceReference(t, db, "installment-2", "2026-10")
	assertTransactionInvoiceReference(t, db, "installment-3", "2026-11")
	assertInvoiceExists(t, db, "card-test", "2026-11", "OPEN")
}

func TestInsertTransactionCriaNovaFaturaQuandoCompetenciaAtualJaFoiPaga(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = ?
		WHERE id = 'invoice-2026-08'
	`, time.Now().Unix(), int64(25000))

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	txDate := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Compra pos pagamento",
		"",
		"",
		txDate,
		"card-test",
		"",
		"cat-expense",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insert transaction: %v", err)
	}

	assertInvoiceExists(t, db, "card-test", "2026-09", "OPEN")
	assertTransactionInvoiceReference(t, db, findTransactionByDescription(t, db, "Compra pos pagamento"), "2026-09")
}

func TestBuildLancamentosDataExibeFaturaNoMesDaReferencia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if len(data.Invoices) != 1 {
		t.Fatalf("invoice count = %d, want 1", len(data.Invoices))
	}
	if data.Invoices[0].Reference != "2026-08" {
		t.Fatalf("invoice reference = %q, want 2026-08", data.Invoices[0].Reference)
	}
}

func TestBuildLancamentosDataExibeFaturaAoFiltrarCartao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("card-test", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData with card filter: %v", err)
	}
	if len(data.Invoices) != 1 {
		t.Fatalf("invoice count with card filter = %d, want 1", len(data.Invoices))
	}
	if data.Invoices[0].AccountID != "card-test" {
		t.Fatalf("invoice account = %q, want card-test", data.Invoices[0].AccountID)
	}
}

func TestBuildFaturaDataIncluiTransacaoPendenteNoTotal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-pending-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 5000, ?, 'Compra Pendente', 'pending', 1, 1, ?, ?)
	`, now, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	total, err := sumInvoiceTotal(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatalf("sumInvoiceTotal: %v", err)
	}
	if total != 30000 {
		t.Fatalf("invoice total = %d, want 30000", total)
	}
	if len(data.Transactions) != 2 {
		t.Fatalf("transactions = %d, want 2", len(data.Transactions))
	}
}

func TestBuildFaturaDataSortAndGroupByDay(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('purchase-aug-18-a', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 1500, ?, 'Compra 18A', 'paid', 1, 1, ?, ?),
			('purchase-aug-18-b', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 2500, ?, 'Compra 18B', 'paid', 1, 1, ?, ?),
			('purchase-aug-20', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 3500, ?, 'Compra 20', 'paid', 1, 1, ?, ?)
	`, testUnixDate("2026-08-18"), now+1, now+1, testUnixDate("2026-08-18")+3600, now+2, now+2, testUnixDate("2026-08-20"), now+3, now+3)

	descData, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice desc: %v", err)
	}
	if len(descData.TransactionGroups) != 3 {
		t.Fatalf("group count desc = %d, want 3", len(descData.TransactionGroups))
	}
	if descData.TransactionGroups[0].DateLabel != "20 Ago" || descData.TransactionGroups[1].DateLabel != "18 Ago" {
		t.Fatalf("unexpected desc leading groups: %#v", descData.TransactionGroups)
	}
	if descData.TransactionGroups[2].Transactions[0].Description != "Compra Teste" {
		t.Fatalf("unexpected desc trailing group: %#v", descData.TransactionGroups[2])
	}
	if descData.Transactions[0].Description != "Compra 20" {
		t.Fatalf("first desc tx = %q, want Compra 20", descData.Transactions[0].Description)
	}

	ascData, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "asc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice asc: %v", err)
	}
	if len(ascData.TransactionGroups) != 3 {
		t.Fatalf("group count asc = %d, want 3", len(ascData.TransactionGroups))
	}
	if ascData.TransactionGroups[0].Transactions[0].Description != "Compra Teste" || ascData.TransactionGroups[1].DateLabel != "18 Ago" || ascData.TransactionGroups[2].DateLabel != "20 Ago" {
		t.Fatalf("unexpected asc group order: %#v", ascData.TransactionGroups)
	}
	if ascData.Transactions[0].Description != "Compra Teste" {
		t.Fatalf("first asc tx = %q, want Compra Teste", ascData.Transactions[0].Description)
	}
}

func TestInvoiceTransactionsTemplateHighlightsLoadMoreAndKeepsMetadataWithoutTagIcon(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	for i := 0; i < 36; i++ {
		installmentNumber := 1
		totalInstallments := 1
		description := fmt.Sprintf("Compra extra %02d", i+1)
		if i == 0 {
			description = "Compra recorrente"
			installmentNumber = 1
			totalInstallments = 12
		}
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 1000, ?, ?, 'paid', ?, ?, ?, ?)
		`, fmt.Sprintf("purchase-bulk-%02d", i+1), testUnixDate("2026-08-20")+int64(i*60), description, installmentNumber, totalInstallments, now+int64(i), now+int64(i))
	}

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if len(data.Transactions) != 37 {
		t.Fatalf("transactions = %d, want 37", len(data.Transactions))
	}
	if data.InitialVisibleItems != 30 {
		t.Fatalf("initial visible items = %d, want 30", data.InitialVisibleItems)
	}
	if data.VisibleItemCountLabel != "Você está vendo 30 de 37 lançamentos." {
		t.Fatalf("visible label = %q", data.VisibleItemCountLabel)
	}
	if data.HiddenItemCountLabel != "Ainda há 7 lançamentos ocultos nesta fatura." {
		t.Fatalf("hidden label = %q", data.HiddenItemCountLabel)
	}
	if data.LoadMoreButtonLabel != "Carregar mais 7 lançamentos" {
		t.Fatalf("load more label = %q", data.LoadMoreButtonLabel)
	}

	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Parse(string(content)))

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "invoice-transactions", data); err != nil {
		t.Fatalf("execute invoice-transactions: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "Você está vendo 30 de 37 lançamentos.") {
		t.Fatalf("missing visible count copy: %s", html)
	}
	if !strings.Contains(html, "Ainda há 7 lançamentos ocultos nesta fatura.") {
		t.Fatalf("missing hidden count copy: %s", html)
	}
	if !strings.Contains(html, "Carregar mais 7 lançamentos") {
		t.Fatalf("missing load more button copy: %s", html)
	}
	if strings.Contains(html, `data-lucide="tag"`) {
		t.Fatalf("decorative tag icon should be absent from invoice rows: %s", html)
	}
	if !strings.Contains(html, "Sem categoria") {
		t.Fatalf("category text should remain visible: %s", html)
	}
	if !strings.Contains(html, "1/12") {
		t.Fatalf("installment badge should remain visible: %s", html)
	}
}

func TestAutoCloseInvoicesKeepsFutureZeroInvoiceOpen(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	futureClosing := time.Now().UTC().AddDate(0, 1, 0).Unix()
	futureDue := time.Now().UTC().AddDate(0, 2, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, futureClosing, futureDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "OPEN", false)
}

func TestAutoCloseInvoicesPaysZeroInvoiceAtClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	pastClosing := time.Now().UTC().AddDate(0, -1, 0).Unix()
	futureDue := time.Now().UTC().AddDate(0, 1, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, pastClosing, futureDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "PAID", true)
}

func TestAutoCloseInvoicesPaysZeroInvoiceAtDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	futureClosing := time.Now().UTC().AddDate(0, 1, 0).Unix()
	pastDue := time.Now().UTC().AddDate(0, -1, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, futureClosing, pastDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "PAID", true)
}

func TestAutoCloseInvoicesDoesNotPayPositiveInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	pastClosing := time.Now().UTC().AddDate(0, -2, 0).Unix()
	pastDue := time.Now().UTC().AddDate(0, -1, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, pastClosing, pastDue)

	if err := autoCloseInvoices(db, "ws-test", "card-test"); err != nil {
		t.Fatalf("autoCloseInvoices: %v", err)
	}

	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "CLOSED", false)
}

func TestResolveDashboardInvoiceSkipsAutoPaidZeroInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	pastClosing := time.Now().UTC().AddDate(0, -2, 0).Unix()
	pastDue := time.Now().UTC().AddDate(0, -1, 0).Unix()
	nextClosing := time.Now().UTC().AddDate(0, 1, 0).Unix()
	nextDue := time.Now().UTC().AddDate(0, 2, 0).Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, pastClosing, pastDue)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-next-zero-skip', 'card-test', '2099-01', ?, ?, 'OPEN', ?)
	`, nextClosing, nextDue, time.Now().Unix())

	invoiceID, _, status, _, _, err := resolveDashboardInvoice(db, "ws-test", "card-test", time.Now().Unix())
	if err != nil {
		t.Fatalf("resolveDashboardInvoice: %v", err)
	}
	if invoiceID != "invoice-next-zero-skip" || status != "OPEN" {
		t.Fatalf("dashboard invoice = %q/%q, want invoice-next-zero-skip/OPEN", invoiceID, status)
	}
	assertInvoiceStatusAndZeroPayment(t, db, "invoice-2026-08", "PAID", true)
}

func TestBuildLancamentosDataZeroPaidInvoiceDoesNotBecomePendingExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	due := time.Now().UTC().AddDate(0, -1, 0)
	dueDate := time.Date(due.Year(), due.Month(), 10, 12, 0, 0, 0, time.UTC)
	closingDate := dueDate.AddDate(0, -1, 0)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'OPEN', due_date = ?, closing_date = ?, paid_at = NULL, paid_amount = NULL
		WHERE id = 'invoice-2026-08'
	`, dueDate.Unix(), closingDate.Unix())

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	data, err := handler.buildLancamentosData("", int(dueDate.Month()), dueDate.Year(), LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if len(data.Invoices) != 1 {
		t.Fatalf("invoice count = %d, want 1", len(data.Invoices))
	}
	if data.Invoices[0].Status != "PAID" || data.Invoices[0].Total != 0 || data.Invoices[0].IsOverdue {
		t.Fatalf("invoice row = status %q total %d overdue %v, want PAID/0/false", data.Invoices[0].Status, data.Invoices[0].Total, data.Invoices[0].IsOverdue)
	}
	for _, item := range data.UnifiedItems {
		if item.IsInvoice && item.Invoice.ID == "invoice-2026-08" {
			t.Fatal("zero paid invoice should not be rendered as a pending unified expense")
		}
	}
}

func TestBuildFaturaDataMaterializaRecorrenciaAntigaNaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-2026-12', 'card-test', '2026-12', ?, ?, 'OPEN', ?)
	`, testUnixDate("2026-11-20"), testUnixDate("2026-12-10"), now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES ('rule-card-monthly', 'ws-test', 'user-test', 'card-test', 'EXPENSE', 7000, 'Assinatura Cartao', ?, 'MONTHLY', 'PAID', 1, ?, ?)
	`, testUnixDate("2026-01-05"), now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-12", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if len(data.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1", len(data.Transactions))
	}
	total, err := sumInvoiceTotal(db, "ws-test", "invoice-2026-12")
	if err != nil {
		t.Fatalf("sumInvoiceTotal: %v", err)
	}
	if total != 7000 {
		t.Fatalf("invoice total = %d, want 7000", total)
	}
	var invoiceID, status string
	occurrence := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()
	if err := db.QueryRow(`
		SELECT invoice_id, status
		FROM transactions
		WHERE recurring_rule_id = 'rule-card-monthly' AND date = ?
	`, occurrence).Scan(&invoiceID, &status); err != nil {
		t.Fatalf("materialized recurrence query: %v", err)
	}
	if invoiceID != "invoice-2026-12" || status != "paid" {
		t.Fatalf("materialized invoice/status = %q/%q, want invoice-2026-12/paid", invoiceID, status)
	}
}

func TestInsertTransactionParceladaComFaturaManualDistribuiParcelas(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-installment', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		30000,
		"Compra Parcelada Override",
		"",
		"",
		testUnixDate("2026-07-05"),
		"card-test",
		"",
		"cat-installment",
		3,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"invoice-2026-08",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction: %v", err)
	}

	for installment, wantReference := range map[int]string{1: "2026-08", 2: "2026-09", 3: "2026-10"} {
		var got string
		if err := db.QueryRow(`
			SELECT i.reference
			FROM transactions t
			JOIN invoices i ON i.id = t.invoice_id
			WHERE t.description = 'Compra Parcelada Override' AND t.installment_number = ?
		`, installment).Scan(&got); err != nil {
			t.Fatalf("installment %d query: %v", installment, err)
		}
		if got != wantReference {
			t.Fatalf("installment %d reference = %q, want %q", installment, got, wantReference)
		}
	}
}

func TestInvoiceDatesForReferenceClampDays293031(t *testing.T) {
	cases := []struct {
		name            string
		year            int
		month           time.Month
		closingDay      int64
		dueDay          int64
		wantClosingDate string
		wantDueDate     string
	}{
		{
			name:            "fevereiro com 31/31 faz clamp para 28",
			year:            2026,
			month:           time.February,
			closingDay:      31,
			dueDay:          31,
			wantClosingDate: "2026-02-28",
			wantDueDate:     "2026-02-28",
		},
		{
			name:            "mes de 30 dias com 31/31 faz clamp para 30",
			year:            2026,
			month:           time.April,
			closingDay:      31,
			dueDay:          31,
			wantClosingDate: "2026-04-30",
			wantDueDate:     "2026-04-30",
		},
		{
			name:            "mes de 31 dias preserva 31",
			year:            2026,
			month:           time.May,
			closingDay:      31,
			dueDay:          31,
			wantClosingDate: "2026-05-31",
			wantDueDate:     "2026-05-31",
		},
		{
			name:            "dueDay menor que closingDay fecha no mes anterior",
			year:            2026,
			month:           time.August,
			closingDay:      31,
			dueDay:          10,
			wantClosingDate: "2026-07-31",
			wantDueDate:     "2026-08-10",
		},
		{
			name:            "dueDay maior que closingDay fecha no mesmo mes",
			year:            2026,
			month:           time.August,
			closingDay:      10,
			dueDay:          25,
			wantClosingDate: "2026-08-10",
			wantDueDate:     "2026-08-25",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closingUnix, dueUnix := invoiceDatesForReference(tc.year, tc.month, tc.closingDay, tc.dueDay)
			closing := time.Unix(closingUnix, 0).UTC().Format("2006-01-02")
			due := time.Unix(dueUnix, 0).UTC().Format("2006-01-02")
			if closing != tc.wantClosingDate {
				t.Fatalf("closing date = %s, want %s", closing, tc.wantClosingDate)
			}
			if due != tc.wantDueDate {
				t.Fatalf("due date = %s, want %s", due, tc.wantDueDate)
			}
		})
	}
}

func TestEnsureInvoiceForReferenceTxUsesSafeClampedDates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-31-10', 'ws-test', 'Card 31-10', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-31-10', 'card-31-10', 31, 10, 100000)
	`)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-10-31', 'ws-test', 'Card 10-31', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-10-31', 'card-10-31', 10, 31, 100000)
	`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	idA, refA, _, closeA, dueA, err := ensureInvoiceForReferenceTx(tx, "ws-test", "card-31-10", 2026, time.February)
	if err != nil {
		t.Fatalf("ensure invoice case A: %v", err)
	}
	if idA == "" || refA != "2026-02" {
		t.Fatalf("case A invoice id/ref = %q/%q", idA, refA)
	}
	if got := time.Unix(closeA, 0).UTC().Format("2006-01-02"); got != "2026-01-31" {
		t.Fatalf("case A closing = %s, want 2026-01-31", got)
	}
	if got := time.Unix(dueA, 0).UTC().Format("2006-01-02"); got != "2026-02-10" {
		t.Fatalf("case A due = %s, want 2026-02-10", got)
	}

	idB, refB, _, closeB, dueB, err := ensureInvoiceForReferenceTx(tx, "ws-test", "card-10-31", 2026, time.February)
	if err != nil {
		t.Fatalf("ensure invoice case B: %v", err)
	}
	if idB == "" || refB != "2026-02" {
		t.Fatalf("case B invoice id/ref = %q/%q", idB, refB)
	}
	if got := time.Unix(closeB, 0).UTC().Format("2006-01-02"); got != "2026-02-10" {
		t.Fatalf("case B closing = %s, want 2026-02-10", got)
	}
	if got := time.Unix(dueB, 0).UTC().Format("2006-01-02"); got != "2026-02-28" {
		t.Fatalf("case B due = %s, want 2026-02-28", got)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func testOOBTemplates(t *testing.T) *template.Template {
	t.Helper()

	return template.Must(template.New("test").Parse(`
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}

func testInvoicePaymentTemplates(t *testing.T) *template.Template {
	t.Helper()

	return template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}>{{if .HasPayments}}has-payments{{end}}</section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}

func seedInvoicePaymentScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	futureClosing := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix()
	futureDue := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-test', 'User Test', 'user-test@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-test', 'Workspace Test', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-test', 'user-test', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-test', 'ws-test', 'Conta Teste', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-test', 'ws-test', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-test', 'card-test', 20, 10, 500000)
	`)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-2026-08', 'card-test', '2026-08', ?, ?, 'OPEN', ?)
	`, futureClosing, futureDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 25000, ?, 'Compra Teste', 'paid', 1, 1, ?, ?)
	`, now, now, now)
}

func seedInstallmentMoveScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	for _, inv := range []struct {
		id        string
		reference string
		closeDate string
		dueDate   string
	}{
		{"invoice-2026-09", "2026-09", "2026-08-20", "2026-09-10"},
		{"invoice-2026-10", "2026-10", "2026-09-20", "2026-10-10"},
	} {
		execTestSQL(t, db, `
			INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, 'card-test', ?, ?, ?, 'OPEN', ?)
		`, inv.id, inv.reference, testUnixDate(inv.closeDate), testUnixDate(inv.dueDate), now)
	}

	for _, tx := range []struct {
		id          string
		invoiceID   string
		parentID    interface{}
		installment int
		dateStr     string
	}{
		{"installment-1", "invoice-2026-08", nil, 1, "2026-07-05"},
		{"installment-2", "invoice-2026-09", "installment-1", 2, "2026-08-05"},
		{"installment-3", "invoice-2026-10", "installment-1", 3, "2026-09-05"},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'card-test', ?, 'EXPENSE', 10000, ?, 'Compra Parcelada', 'paid', ?, 3, ?, ?, ?)
		`, tx.id, tx.invoiceID, testUnixDate(tx.dateStr), tx.installment, tx.parentID, now, now)
	}
}

func testUnixDate(date string) int64 {
	t, _ := time.Parse("2006-01-02", date)
	return t.Unix()
}

func seedInvoiceLimitScenario(t *testing.T, db *sql.DB, creditLimit, expenseAmount int64) {
	t.Helper()

	now := time.Now().Unix()
	futureClosing := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix()
	futureDue := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-limit', 'User Limit', 'user-limit@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-limit', 'Workspace Limit', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-limit', 'user-limit', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-limit', 'ws-limit', 'Cartao Limit', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-limit', 'card-limit', 20, 10, ?)
	`, creditLimit)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-limit-2026-08', 'card-limit', '2026-08', ?, ?, 'OPEN', ?)
	`, futureClosing, futureDue, now)
	if expenseAmount > 0 {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
			VALUES ('purchase-limit', 'ws-limit', 'user-limit', 'card-limit', 'invoice-limit-2026-08', 'EXPENSE', ?, ?, 'Compra Limit', 'paid', 1, 1, ?, ?)
		`, expenseAmount, now, now, now)
	}
}

func TestFaturaExibeLimiteDoCartaoDisponivelCalculado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoiceLimitScenario(t, db, 100000, 64000)

	data, err := buildFaturaDataForInvoice(db, "ws-limit", "invoice-limit-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data error: %v", err)
	}

	if !data.HasCreditLimit {
		t.Fatalf("expected HasCreditLimit true")
	}
	if data.CreditLimit.Reais != "1.000" || data.CreditLimit.Cents != ",00" {
		t.Fatalf("CreditLimit = R$ %s%s, want R$ 1.000,00", data.CreditLimit.Reais, data.CreditLimit.Cents)
	}
	if data.LimitUsed.Reais != "640" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 640,00", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "360" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 360,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
	if data.LimitPercent != 36 {
		t.Fatalf("LimitPercent = %d, want 36", data.LimitPercent)
	}
}

func TestFaturaLimiteDisponivelZeradoQuandoTotalMaiorQueLimite(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoiceLimitScenario(t, db, 50000, 64000)

	data, err := buildFaturaDataForInvoice(db, "ws-limit", "invoice-limit-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data error: %v", err)
	}

	if !data.HasCreditLimit {
		t.Fatalf("expected HasCreditLimit true")
	}
	if data.LimitAvailable.Reais != "0" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 0,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
	if data.LimitPercent != 0 {
		t.Fatalf("LimitPercent = %d, want 0", data.LimitPercent)
	}
}

func TestFaturaSemLimiteDeCreditoNaoExibeIndicador(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoiceLimitScenario(t, db, 0, 25000)

	data, err := buildFaturaDataForInvoice(db, "ws-limit", "invoice-limit-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data error: %v", err)
	}

	if data.HasCreditLimit {
		t.Fatalf("expected HasCreditLimit false when credit_limit is 0")
	}
	if data.Total.Reais != "250" || data.Total.Cents != ",00" {
		t.Fatalf("Total = R$ %s%s, want R$ 250,00", data.Total.Reais, data.Total.Cents)
	}
}

func assertTransactionInvoiceReference(t *testing.T, db *sql.DB, transactionID, wantReference string) {
	t.Helper()

	var got string
	if err := db.QueryRow(`
		SELECT i.reference
		FROM transactions t
		JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ?
	`, transactionID).Scan(&got); err != nil {
		t.Fatalf("transaction invoice reference query: %v", err)
	}
	if got != wantReference {
		t.Fatalf("transaction %s invoice reference = %q, want %q", transactionID, got, wantReference)
	}
}

func assertInvoiceExists(t *testing.T, db *sql.DB, accountID, reference, wantStatus string) {
	t.Helper()

	var gotStatus string
	if err := db.QueryRow(`
		SELECT status FROM invoices WHERE account_id = ? AND reference = ?
	`, accountID, reference).Scan(&gotStatus); err != nil {
		t.Fatalf("invoice %s query: %v", reference, err)
	}
	if gotStatus != wantStatus {
		t.Fatalf("invoice %s status = %q, want %q", reference, gotStatus, wantStatus)
	}
}

func assertInvoiceStatusAndZeroPayment(t *testing.T, db *sql.DB, invoiceID, wantStatus string, wantPaid bool) {
	t.Helper()

	var status string
	var paidAt sql.NullInt64
	var paidAmount sql.NullInt64
	if err := db.QueryRow(`
		SELECT status, paid_at, paid_amount FROM invoices WHERE id = ?
	`, invoiceID).Scan(&status, &paidAt, &paidAmount); err != nil {
		t.Fatalf("invoice status query: %v", err)
	}
	if status != wantStatus {
		t.Fatalf("invoice status = %q, want %q", status, wantStatus)
	}
	if wantPaid {
		if !paidAt.Valid || paidAt.Int64 <= 0 {
			t.Fatalf("paid_at = %v, want unix timestamp", paidAt)
		}
		if !paidAmount.Valid || paidAmount.Int64 != 0 {
			t.Fatalf("paid_amount = %v, want 0", paidAmount)
		}
		return
	}
	if paidAt.Valid || paidAmount.Valid {
		t.Fatalf("payment fields = paid_at %v paid_amount %v, want NULL/NULL", paidAt, paidAmount)
	}
}

func findTransactionByDescription(t *testing.T, db *sql.DB, description string) string {
	t.Helper()

	var id string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, description).Scan(&id); err != nil {
		t.Fatalf("find transaction by description: %v", err)
	}
	return id
}

func TestBuildFaturaDataSemPagamentosMostraTotalPendente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false for invoice without payments")
	}
	if data.TotalPaid.Reais != "0" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 0,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "250" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 250,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 0 {
		t.Fatalf("InvoicePayments len = %d, want 0", len(data.InvoicePayments))
	}
}

func TestFaturaTemplateExpoeFluxoPagamentoParcial(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Parse(string(content)))

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "invoice-summary", data); err != nil {
		t.Fatalf("execute invoice-summary: %v", err)
	}
	html := buf.String()
	for _, expected := range []string{
		`type="button" data-open-settle-payment`,
		"Quitar fatura",
		"Confirmar quitação",
		`name="payment_mode" value="settle"`,
		`name="confirm_settle" value="1"`,
		"Competência",
		"Valor a quitar",
		"Conta selecionada",
		"Data de pagamento",
		"Saldo estimado da conta após pagamento",
		"R$ 750,00",
		"Pagamento parcial",
		`name="payment_mode" value="partial"`,
		`name="payment_amount"`,
		`name="payment_date"`,
		"Saldo restante após este pagamento",
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("missing %q in invoice summary html: %s", expected, html)
		}
	}
}

func TestFaturaTemplateSemContaCorrenteBloqueiaQuitacao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM accounts WHERE id = 'checking-test'`)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.CanSubmitPayment {
		t.Fatalf("CanSubmitPayment should be false without payment accounts")
	}
	if data.PaymentDisabledReason != "Nenhuma conta corrente disponível" {
		t.Fatalf("PaymentDisabledReason = %q", data.PaymentDisabledReason)
	}

	content, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Parse(string(content)))

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "invoice-summary", data); err != nil {
		t.Fatalf("execute invoice-summary: %v", err)
	}
	html := buf.String()
	for _, expected := range []string{
		"Nenhuma conta corrente disponível",
		`data-open-settle-payment disabled`,
		`name="payment_account_id" required`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("missing %q in invoice summary html: %s", expected, html)
		}
	}
}

func TestBuildFaturaDataComPagamentoMostraTotalPagoEHistorico(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-test', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if !data.HasPayments {
		t.Fatalf("HasPayments should be true")
	}
	if data.TotalPaid.Reais != "250" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 250,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "0" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 0,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 1 {
		t.Fatalf("InvoicePayments len = %d, want 1", len(data.InvoicePayments))
	}
	row := data.InvoicePayments[0]
	if row.Source != "manual" {
		t.Fatalf("payment source = %q, want manual", row.Source)
	}
	if row.AccountName != "Conta Teste" {
		t.Fatalf("payment account name = %q, want Conta Teste", row.AccountName)
	}
	if row.Amount.Reais != "250" || row.Amount.Cents != ",00" {
		t.Fatalf("payment amount = R$ %s%s", row.Amount.Reais, row.Amount.Cents)
	}
	if row.DateLabel == "" || row.DateLabel == "-" {
		t.Fatalf("payment date label = %q, want non-empty", row.DateLabel)
	}
}

func TestBuildFaturaDataPagamentoRevertidoNaoAparece(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-reversed', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', ?, 'user-test', ?)
	`, now, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false when all payments are reversed")
	}
	if len(data.InvoicePayments) != 0 {
		t.Fatalf("InvoicePayments len = %d, want 0", len(data.InvoicePayments))
	}
}

func TestBuildFaturaDataPagamentoOutroWorkspaceNaoAparece(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-other', 'Other', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-other', 'ws-other', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false for cross-workspace payments")
	}
}

func TestHandlePagarFaturaRenderContemSecaoPagamentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	templates := template.Must(template.New("test").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{if .HasLegacyPaymentSummary}}legacy{{else if .HasPayments}}has-payments{{else}}empty{{end}}</main>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))

	handler := FaturasHandler{
		DB:          db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `hx-swap-oob="outerHTML"`) {
		t.Fatalf("expected OOB dashboard fragments, got %q", rr.Body.String())
	}

	var ipCount int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?
	`, "invoice-2026-08").Scan(&ipCount); err != nil {
		t.Fatalf("invoice_payments count: %v", err)
	}
	if ipCount != 1 {
		t.Fatalf("invoice_payments count = %d, want 1", ipCount)
	}
}

func TestBuildFaturaDataPaidLegacySemInvoicePaymentsUsaFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-08'
	`, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasPayments {
		t.Fatalf("HasPayments should be false (no invoice_payments)")
	}
	if !data.HasLegacyPaymentSummary {
		t.Fatalf("HasLegacyPaymentSummary should be true")
	}
	if data.LegacyPaymentNotice == "" {
		t.Fatalf("LegacyPaymentNotice should not be empty")
	}
	if data.TotalPaid.Reais != "250" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 250,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
	if data.PendingAmount.Reais != "0" || data.PendingAmount.Cents != ",00" {
		t.Fatalf("PendingAmount = R$ %s%s, want R$ 0,00", data.PendingAmount.Reais, data.PendingAmount.Cents)
	}
	if len(data.InvoicePayments) != 0 {
		t.Fatalf("InvoicePayments len = %d, want 0", len(data.InvoicePayments))
	}
}

func TestBuildFaturaDataPaidComInvoicePaymentsNaoUsaFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-08'
	`, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES ('ip-test', 'ws-test', 'invoice-2026-08', 'checking-test', NULL, 25000, ?, NULL, 'manual', NULL, 'user-test', ?)
	`, now, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if !data.HasPayments {
		t.Fatalf("HasPayments should be true (has real invoice_payment)")
	}
	if data.HasLegacyPaymentSummary {
		t.Fatalf("HasLegacyPaymentSummary should be false when real invoice_payments exist")
	}
}

func TestBuildFaturaDataPaidZeroSemPaymentsMostraFallbackZero(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DELETE FROM transactions WHERE invoice_id = 'invoice-2026-08'`)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 0
		WHERE id = 'invoice-2026-08'
	`, now)

	data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.HasLegacyPaymentSummary {
		t.Fatalf("HasLegacyPaymentSummary should be false when paid_amount = 0")
	}
	if data.TotalPaid.Reais != "0" || data.TotalPaid.Cents != ",00" {
		t.Fatalf("TotalPaid = R$ %s%s, want R$ 0,00", data.TotalPaid.Reais, data.TotalPaid.Cents)
	}
}

func execTestSQL(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()

	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec test sql: %v\nquery: %s", err, query)
	}
}
