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

func TestInvoicePaymentTransactionEditIsBlockedWithoutSideEffects(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "purchase-test")

	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, invoicePaymentEditRequest(t, "payment-paid-invoice-08"), "payment-paid-invoice-08")

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "purchase-test", before)
}

func TestInvoicePaymentTransactionDeleteIsBlockedBeforeBalanceMutation(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "purchase-test")

	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/payment-paid-invoice-08", nil), "payment-paid-invoice-08")

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "purchase-test", before)
}

func TestInvoicePaymentTransactionTogglePaidToPendingIsBlocked(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "purchase-test")

	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/payment-paid-invoice-08/status-pagamento", url.Values{
		"payment_action": {"pending"},
	}), "payment-paid-invoice-08")

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "purchase-test", before)
}

func TestInvoicePaymentTransactionMixedBulkUpdateRejectsAll(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	seedOrdinaryProtectionTransaction(t, db, "ordinary-bulk-update", "paid")
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "ordinary-bulk-update")

	rr := httptest.NewRecorder()
	handler.HandleBulkUpdate(rr, formPost("/transacoes/bulk/update", url.Values{
		"ids[]":            {"ordinary-bulk-update", "payment-paid-invoice-08"},
		"status_pagamento": {"pending"},
	}))

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "ordinary-bulk-update", before)
}

func TestInvoicePaymentTransactionMixedBulkDeleteRejectsAll(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	seedOrdinaryProtectionTransaction(t, db, "ordinary-bulk-delete", "paid")
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "ordinary-bulk-delete")

	rr := httptest.NewRecorder()
	handler.HandleBulkDelete(rr, formPost("/transacoes/bulk-delete", url.Values{
		"ids[]": {"ordinary-bulk-delete", "payment-paid-invoice-08"},
	}))

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "ordinary-bulk-delete", before)
}

func TestOrdinaryTransactionToggleBehaviorIsUnchanged(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	seedOrdinaryProtectionTransaction(t, db, "ordinary-toggle", "pending")

	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/ordinary-toggle/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-08-08"},
	}), "ordinary-toggle")

	if rr.Code != http.StatusOK {
		t.Fatalf("ordinary toggle status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var status string
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = 'ordinary-toggle'`).Scan(&status, &paidAt); err != nil {
		t.Fatalf("ordinary transaction query: %v", err)
	}
	if status != "paid" || !paidAt.Valid {
		t.Fatalf("ordinary transaction status = %q, paid_at = %v", status, paidAt)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balance); err != nil {
		t.Fatalf("ordinary balance query: %v", err)
	}
	if balance != 90000 {
		t.Fatalf("ordinary balance = %d, want 90000", balance)
	}
}

func TestReversedInvoicePaymentTransactionRemainsProtected(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = ? WHERE id = 'pay-legacy-08'`, time.Now().Unix())
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "purchase-test")

	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/payment-paid-invoice-08", nil), "payment-paid-invoice-08")

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "purchase-test", before)
}

func TestInvoicePaymentTransactionProtectionIsWorkspaceIsolated(t *testing.T) {
	db, handlerA := newInvoicePaymentProtectionScenario(t)
	seedForeignInvoicePaymentProtectionScenario(t, db)

	foreignBefore := invoicePaymentProtectionSnapshotFor(t, db, "foreign-payment", "foreign-payment")
	rrA := httptest.NewRecorder()
	handlerA.HandleDeletarTransacao(rrA, httptest.NewRequest(http.MethodDelete, "/transacoes/foreign-payment", nil), "foreign-payment")
	if rrA.Code != http.StatusNotFound {
		t.Fatalf("workspace A foreign delete status = %d, want 404; body = %q", rrA.Code, rrA.Body.String())
	}
	assertInvoicePaymentProtectionSnapshot(t, db, "foreign-payment", "foreign-payment", foreignBefore)

	handlerB := testPaidInvoiceMutationHandler(db)
	handlerB.WorkspaceID = "ws-foreign"
	handlerB.UserID = "user-foreign"
	rrB := httptest.NewRecorder()
	handlerB.HandleDeletarTransacao(rrB, httptest.NewRequest(http.MethodDelete, "/transacoes/foreign-payment", nil), "foreign-payment")
	assertInvoicePaymentProtectionConflict(t, rrB)
	assertInvoicePaymentProtectionSnapshot(t, db, "foreign-payment", "foreign-payment", foreignBefore)
}

func TestHandlePagarFaturaStillCreatesProtectedTransactionAndGenericDeleteCannotBreakSettlement(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedInvoicePaymentScenario(t, db)

	faturas := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   invoicePaymentProtectionTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	form := url.Values{
		"invoice_id":         {"invoice-2026-08"},
		"payment_account_id": {"checking-test"},
		"payment_mode":       {"settle"},
		"confirm_settle":     {"1"},
	}
	payRR := httptest.NewRecorder()
	faturas.HandlePagarFatura(payRR, formPost("/cartoes/faturas/pagar", form))
	if payRR.Code != http.StatusOK {
		t.Fatalf("invoice payment status = %d, body = %q", payRR.Code, payRR.Body.String())
	}

	var paymentTransactionID string
	if err := db.QueryRow(`
		SELECT transaction_id
		FROM invoice_payments
		WHERE workspace_id = 'ws-test' AND invoice_id = 'invoice-2026-08'
	`).Scan(&paymentTransactionID); err != nil {
		t.Fatalf("created invoice payment transaction query: %v", err)
	}
	if paymentTransactionID == "" {
		t.Fatal("HandlePagarFatura created an empty transaction_id")
	}

	before := invoicePaymentProtectionSnapshotFor(t, db, paymentTransactionID, "purchase-test")
	transacoes := TransactionHandler{
		DB:          db,
		Templates:   invoicePaymentProtectionTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	deleteRR := httptest.NewRecorder()
	transacoes.HandleDeletarTransacao(deleteRR, httptest.NewRequest(http.MethodDelete, "/transacoes/"+paymentTransactionID, nil), paymentTransactionID)

	assertInvoicePaymentProtectionConflict(t, deleteRR)
	assertInvoicePaymentProtectionSnapshot(t, db, paymentTransactionID, "purchase-test", before)
	if before.balance != 75000 || before.invoiceStatus != "PAID" || before.invoicePaidAmount != 25000 || before.invoicePaymentCount != 1 {
		t.Fatalf("settlement baseline inconsistent: %+v", before)
	}
}

func TestInvoicePaymentTransactionProtectionIsDeterministicAndSideEffectFree(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "purchase-test")

	for attempt := 1; attempt <= 5; attempt++ {
		rr := httptest.NewRecorder()
		handler.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/payment-paid-invoice-08", nil), "payment-paid-invoice-08")
		assertInvoicePaymentProtectionConflict(t, rr)
		assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "purchase-test", before)
	}
}

func TestInvoicePaymentTransactionManualInvoiceAssignmentIsBlocked(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	before := invoicePaymentProtectionSnapshotFor(t, db, "payment-paid-invoice-08", "purchase-test")

	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, formPost("/transacoes/payment-paid-invoice-08/salvar", url.Values{
		"invoice_assignment_mode": {"manual"},
		"invoice_id":              {"invoice-2026-08"},
	}), "payment-paid-invoice-08")

	assertInvoicePaymentProtectionConflict(t, rr)
	assertInvoicePaymentProtectionSnapshot(t, db, "payment-paid-invoice-08", "purchase-test", before)
}

func TestInvoicePaymentTransactionProtectionDoesNotMaskSQLiteErrors(t *testing.T) {
	db, handler := newInvoicePaymentProtectionScenario(t)
	if _, err := fixtureHandlerExec(t, db, `DROP TABLE invoice_payments`); err != nil {
		t.Fatalf("drop invoice_payments in isolated test database: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	err = handler.ensureTransactionsNotInvoicePaymentsTx(tx, []string{"payment-paid-invoice-08"})
	if err == nil {
		t.Fatal("expected SQLite error, got nil")
	}
	if err == errInvoicePaymentTransactionProtected {
		t.Fatalf("SQLite error was masked as functional conflict: %v", err)
	}
}

type invoicePaymentProtectionState struct {
	protectedCount         int
	protectedAmount        int64
	protectedDescription   string
	protectedStatus        string
	protectedAccountID     string
	protectedPaidAt        int64
	protectedUpdatedAt     int64
	ordinaryCount          int
	ordinaryAmount         int64
	ordinaryDescription    string
	ordinaryStatus         string
	ordinaryUpdatedAt      int64
	totalTransactionCount  int
	balance                int64
	invoicePaymentCount    int
	invoicePaymentAmount   int64
	invoicePaymentReversed int64
	invoiceStatus          string
	invoicePaidAmount      int64
	invoicePaidAt          int64
	statusHistoryCount     int
}

func newInvoicePaymentProtectionScenario(t *testing.T) (*sql.DB, TransactionHandler) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedPaidInvoiceImmutabilityScenario(t, db)
	return db, testPaidInvoiceMutationHandler(db)
}

func invoicePaymentEditRequest(t *testing.T, id string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":            "275,00",
		"descricao":        "Pagamento adulterado",
		"data":             "2026-08-08",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense",
		"status_pagamento": "paid",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write edit field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close edit multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+id+"/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func seedOrdinaryProtectionTransaction(t *testing.T, db *sql.DB, id, status string) {
	t.Helper()
	now := time.Now().Unix()
	var paidAt interface{}
	if status == "paid" {
		paidAt = now
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense', 'EXPENSE', 10000, ?, 'Lançamento comum', ?, ?, 1, 1, ?, ?)
	`, id, now, status, paidAt, now, now)
}

func seedForeignInvoicePaymentProtectionScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-foreign', 'Foreign', 'foreign@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES ('ws-foreign', 'Foreign', '', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-foreign', 'user-foreign', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('foreign-checking', 'ws-foreign', 'Foreign Checking', 'CHECKING', 50000, 50000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('foreign-card', 'ws-foreign', 'Foreign Card', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('foreign-credit-card', 'foreign-card', 20, 10, 100000)`)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, paid_at, created_at) VALUES ('foreign-invoice', 'foreign-card', '2026-08', ?, ?, 'PAID', 10000, ?, ?)`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at) VALUES ('foreign-payment', 'ws-foreign', 'user-foreign', 'foreign-checking', 'EXPENSE', 10000, ?, 'Foreign payment', 'paid', ?, 1, 1, ?, ?)`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at) VALUES ('foreign-ip', 'ws-foreign', 'foreign-invoice', 'foreign-checking', 'foreign-payment', 10000, ?, 'manual', 'user-foreign', ?)`, now, now)
}

func invoicePaymentProtectionSnapshotFor(t *testing.T, db *sql.DB, protectedID, ordinaryID string) invoicePaymentProtectionState {
	t.Helper()
	var state invoicePaymentProtectionState
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(MAX(amount), 0), COALESCE(MAX(description), ''), COALESCE(MAX(status), ''),
		       COALESCE(MAX(account_id), ''), COALESCE(MAX(paid_at), 0), COALESCE(MAX(updated_at), 0)
		FROM transactions WHERE id = ?
	`, protectedID).Scan(&state.protectedCount, &state.protectedAmount, &state.protectedDescription, &state.protectedStatus, &state.protectedAccountID, &state.protectedPaidAt, &state.protectedUpdatedAt); err != nil {
		t.Fatalf("protected transaction snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(MAX(amount), 0), COALESCE(MAX(description), ''), COALESCE(MAX(status), ''), COALESCE(MAX(updated_at), 0)
		FROM transactions WHERE id = ?
	`, ordinaryID).Scan(&state.ordinaryCount, &state.ordinaryAmount, &state.ordinaryDescription, &state.ordinaryStatus, &state.ordinaryUpdatedAt); err != nil {
		t.Fatalf("ordinary transaction snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions`).Scan(&state.totalTransactionCount); err != nil {
		t.Fatalf("transaction count snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COALESCE(current_balance, 0) FROM accounts WHERE id = (SELECT account_id FROM transactions WHERE id = ?)`, protectedID).Scan(&state.balance); err != nil {
		t.Fatalf("balance snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(amount_cents), 0), COALESCE(MAX(reversed_at), 0)
		FROM invoice_payments WHERE transaction_id = ?
	`, protectedID).Scan(&state.invoicePaymentCount, &state.invoicePaymentAmount, &state.invoicePaymentReversed); err != nil {
		t.Fatalf("invoice payment snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COALESCE(MAX(i.status), ''), COALESCE(MAX(i.paid_amount), 0), COALESCE(MAX(i.paid_at), 0)
		FROM invoices i JOIN invoice_payments ip ON ip.invoice_id = i.id
		WHERE ip.transaction_id = ?
	`, protectedID).Scan(&state.invoiceStatus, &state.invoicePaidAmount, &state.invoicePaidAt); err != nil {
		t.Fatalf("invoice snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id IN (?, ?)`, protectedID, ordinaryID).Scan(&state.statusHistoryCount); err != nil {
		t.Fatalf("history snapshot: %v", err)
	}
	return state
}

func assertInvoicePaymentProtectionSnapshot(t *testing.T, db *sql.DB, protectedID, ordinaryID string, want invoicePaymentProtectionState) {
	t.Helper()
	if got := invoicePaymentProtectionSnapshotFor(t, db, protectedID, ordinaryID); got != want {
		t.Fatalf("state changed after blocked mutation\n got: %+v\nwant: %+v", got, want)
	}
}

func assertInvoicePaymentProtectionConflict(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "fluxo próprio da fatura") {
		t.Fatalf("conflict body is not understandable: %q", rr.Body.String())
	}
}

func invoicePaymentProtectionTemplates() *template.Template {
	return template.Must(template.New("invoice-payment-protection").Parse(`
{{define "faturas-content"}}<main id="faturas-content">{{template "invoice-summary" .}}{{template "invoice-transactions" .}}</main>{{end}}
{{define "lancamento-row"}}<tr id="{{.ID}}"></tr>{{end}}
{{define "lancamentos-table-body"}}<tbody></tbody>{{end}}
{{define "dashboard-balance"}}<section id="dashboard-balance" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-accounts"}}<section id="dashboard-accounts" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}
