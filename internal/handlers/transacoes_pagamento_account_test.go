package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestTogglePagamentoChangesPaymentAccountAndAdjustsBalance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-change-payment-account", "pending", nil)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-payment-target', 'ws-test', 'Conta destino', 'CHECKING', 50000, 50000, ?, ?)
	`, now, now)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-change-payment-account/status-pagamento", url.Values{
		"payment_action":     {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"checking-payment-target"},
	}), "tx-change-payment-account")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var accountID, status string
	if err := db.QueryRow(`SELECT account_id, status FROM transactions WHERE id = 'tx-change-payment-account'`).Scan(&accountID, &status); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if accountID != "checking-payment-target" || status != "paid" {
		t.Fatalf("transaction = account %q / status %q, want checking-payment-target / paid", accountID, status)
	}
	assertPaymentAccountBalance(t, db, "checking-test", 100000)
	assertPaymentAccountBalance(t, db, "checking-payment-target", 37655)
}

func TestTogglePagamentoAllowsSimpleTransactionWithSameAccountDestinationPlaceholder(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-same-account-destination", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions SET destination_account_id = account_id WHERE id = 'tx-same-account-destination'`)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-same-destination-target', 'ws-test', 'Conta destino válida', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-same-account-destination/status-pagamento", url.Values{
		"payment_action":     {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"checking-same-destination-target"},
	}), "tx-same-account-destination")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var accountID, status string
	if err := db.QueryRow(`SELECT account_id, status FROM transactions WHERE id = 'tx-same-account-destination'`).Scan(&accountID, &status); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if accountID != "checking-same-destination-target" || status != "paid" {
		t.Fatalf("transaction = account %q / status %q, want checking-same-destination-target / paid", accountID, status)
	}
}

func TestTogglePagamentoPaidIsIdempotentAcrossDuplicatePosts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-idempotent-payment", "pending", nil)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-idempotent-target', 'ws-test', 'Conta destino', 'CHECKING', 50000, 50000, ?, ?)
	`, now, now)

	handler := testPaidInvoiceMutationHandler(db)
	post := func(accountID string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-idempotent-payment/status-pagamento", url.Values{
			"payment_action":     {"paid"},
			"payment_date":       {"2026-07-14"},
			"payment_account_id": {accountID},
		}), "tx-idempotent-payment")
		return rr
	}

	if rr := post("checking-idempotent-target"); rr.Code != http.StatusOK {
		t.Fatalf("first POST status = %d, body = %q", rr.Code, rr.Body.String())
	}
	// A stale duplicate carries the old account. It must not move the paid
	// transaction back or silently rewrite the account selected by the first POST.
	if rr := post("checking-test"); rr.Code != http.StatusOK {
		t.Fatalf("second POST status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var accountID, status string
	if err := db.QueryRow(`SELECT account_id, status FROM transactions WHERE id = 'tx-idempotent-payment'`).Scan(&accountID, &status); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if accountID != "checking-idempotent-target" || status != "paid" {
		t.Fatalf("transaction = account %q / status %q, want checking-idempotent-target / paid", accountID, status)
	}
	assertPaymentAccountBalance(t, db, "checking-test", 100000)
	assertPaymentAccountBalance(t, db, "checking-idempotent-target", 37655)

	var historyCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-idempotent-payment'`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 1 {
		t.Fatalf("history count = %d, want 1", historyCount)
	}
}

func TestTogglePagamentoAlreadyPaidWithExplicitPaidIsNoOp(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	paidAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).Unix()
	insertPaymentDateTestTransaction(t, db, "tx-already-paid-noop", "paid", &paidAt)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-already-paid-noop/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}), "tx-already-paid-noop")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var gotPaidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = 'tx-already-paid-noop'`).Scan(&status, &gotPaidAt); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "paid" || !gotPaidAt.Valid || gotPaidAt.Int64 != paidAt {
		t.Fatalf("status/paid_at = %q / %v, want paid / %d", status, gotPaidAt, paidAt)
	}
	var historyCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-already-paid-noop'`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 0 {
		t.Fatalf("history count = %d, want 0", historyCount)
	}
}

func TestTogglePagamentoMissingActionCannotUndoPaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	paidAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC).Unix()
	insertPaymentDateTestTransaction(t, db, "tx-paid-missing-action", "paid", &paidAt)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-paid-missing-action/status-pagamento", nil), "tx-paid-missing-action")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var gotPaidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = 'tx-paid-missing-action'`).Scan(&status, &gotPaidAt); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "paid" || !gotPaidAt.Valid || gotPaidAt.Int64 != paidAt {
		t.Fatalf("status/paid_at = %q / %v, want paid / %d", status, gotPaidAt, paidAt)
	}
	var historyCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-paid-missing-action'`).Scan(&historyCount); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if historyCount != 0 {
		t.Fatalf("history count = %d, want 0", historyCount)
	}
}

func TestTogglePagamentoWithoutChangingAccountPreservesCurrentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-keep-payment-account", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-keep-payment-account/status-pagamento", url.Values{
		"payment_action": {"paid"},
		"payment_date":   {"2026-07-14"},
	}), "tx-keep-payment-account")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var accountID string
	if err := db.QueryRow(`SELECT account_id FROM transactions WHERE id = 'tx-keep-payment-account'`).Scan(&accountID); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if accountID != "checking-test" {
		t.Fatalf("account_id = %q, want checking-test", accountID)
	}
	assertPaymentAccountBalance(t, db, "checking-test", 87655)
}

func TestTogglePagamentoRejectsForeignOrIncompatiblePaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-reject-payment-account", "pending", nil)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-payment-other', 'Other', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-payment-other', 'ws-payment-other', 'Outra conta', 'CHECKING', 0, 0, ?, ?)`, now, now)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-reject-payment-account/status-pagamento", url.Values{
		"payment_action":     {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"checking-payment-other"},
	}), "tx-reject-payment-account")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("foreign account status = %d, want 400; body=%q", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/purchase-test/status-pagamento", url.Values{
		"payment_action":     {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"checking-test"},
	}), "purchase-test")
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("card transaction status = %d, want 422; body=%q", rr.Code, rr.Body.String())
	}

	var accountID, status string
	if err := db.QueryRow(`SELECT account_id, status FROM transactions WHERE id = 'tx-reject-payment-account'`).Scan(&accountID, &status); err != nil {
		t.Fatalf("query rejected transaction: %v", err)
	}
	if accountID != "checking-test" || status != "pending" {
		t.Fatalf("rejected transaction = account %q / status %q, want checking-test / pending", accountID, status)
	}
}

func TestTogglePagamentoRejectsArchivedPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-reject-archived-account", "pending", nil)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, archived_at, created_at, updated_at)
		VALUES ('checking-payment-archived', 'ws-test', 'Conta arquivada', 'CHECKING', 0, 0, ?, ?, ?)
	`, now, now, now)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-reject-archived-account/status-pagamento", url.Values{
		"payment_action":     {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"checking-payment-archived"},
	}), "tx-reject-archived-account")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", rr.Code, rr.Body.String())
	}

	var accountID, status string
	if err := db.QueryRow(`SELECT account_id, status FROM transactions WHERE id = 'tx-reject-archived-account'`).Scan(&accountID, &status); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if accountID != "checking-test" || status != "pending" {
		t.Fatalf("transaction = account %q / status %q, want checking-test / pending", accountID, status)
	}
}

func TestTogglePagamentoBlocksAccountChangeForSpecialTransactions(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(*testing.T, *sql.DB, int64)
		wantAccountID string
	}{
		{
			name:          "transfer",
			wantAccountID: "checking-test",
			setup: func(t *testing.T, db *sql.DB, now int64) {
				execTestSQL(t, db, `UPDATE transactions SET type = 'TRANSFER', destination_account_id = 'checking-special-destination' WHERE id = 'tx-special-account-change'`)
			},
		},
		{
			name:          "recurrence",
			wantAccountID: "checking-test",
			setup: func(t *testing.T, db *sql.DB, now int64) {
				execTestSQL(t, db, `
					INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
					VALUES ('rule-special-payment', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 12345, 'Recorrência', ?, 'MONTHLY', 'PENDING', 1, ?, ?)
				`, now, now, now)
				execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = 'rule-special-payment' WHERE id = 'tx-special-account-change'`)
			},
		},
		{
			name:          "invoice",
			wantAccountID: "card-test",
			setup: func(t *testing.T, db *sql.DB, now int64) {
				execTestSQL(t, db, `UPDATE transactions SET account_id = 'card-test', invoice_id = 'invoice-2026-08' WHERE id = 'tx-special-account-change'`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			insertPaymentDateTestTransaction(t, db, "tx-special-account-change", "pending", nil)
			now := time.Now().Unix()
			execTestSQL(t, db, `
				INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
				VALUES
					('checking-special-target', 'ws-test', 'Conta alvo', 'CHECKING', 0, 0, ?, ?),
					('checking-special-destination', 'ws-test', 'Conta transferência', 'CHECKING', 0, 0, ?, ?)
			`, now, now, now, now)
			tt.setup(t, db, now)

			handler := testPaidInvoiceMutationHandler(db)
			rr := httptest.NewRecorder()
			handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-special-account-change/status-pagamento", url.Values{
				"payment_action":     {"paid"},
				"payment_date":       {"2026-07-14"},
				"payment_account_id": {"checking-special-target"},
			}), "tx-special-account-change")
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%q", rr.Code, rr.Body.String())
			}

			var accountID, status string
			if err := db.QueryRow(`SELECT account_id, status FROM transactions WHERE id = 'tx-special-account-change'`).Scan(&accountID, &status); err != nil {
				t.Fatalf("query transaction: %v", err)
			}
			if accountID != tt.wantAccountID || status != "pending" {
				t.Fatalf("transaction = account %q / status %q, want %q / pending", accountID, status, tt.wantAccountID)
			}
		})
	}
}

func TestBulkUpdateRejectsPaymentAccountID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-account-rejected", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleBulkUpdate(rr, formPost("/transacoes/bulk/update", url.Values{
		"ids[]":              {"tx-bulk-account-rejected"},
		"status_pagamento":   {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"checking-test"},
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = 'tx-bulk-account-rejected'`).Scan(&status); err != nil {
		t.Fatalf("query transaction: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %q, want pending", status)
	}
}

func TestPaymentAccountOptionsAreWorkspaceScopedAndExcludeCards(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-payment-options-other', 'Other', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-payment-options-other', 'ws-payment-options-other', 'Outra conta', 'CHECKING', 0, 0, ?, ?)`, now, now)

	options, err := queryPaymentAccountOptions(db, "ws-test")
	if err != nil {
		t.Fatalf("queryPaymentAccountOptions: %v", err)
	}
	if len(options) != 1 || options[0].ID != "checking-test" {
		t.Fatalf("options = %#v, want only checking-test", options)
	}
}

func TestTogglePagamentoChangesAccountInBusinessWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('business-user', 'Business User', 'business@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-payment-business', 'Empresa', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('business-origin', 'ws-payment-business', 'Conta operacional', 'CHECKING', 10000, 10000, ?, ?),
			('business-target', 'ws-payment-business', 'Conta recebimento', 'CHECKING', 20000, 20000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('business-payment', 'ws-payment-business', 'business-user', 'business-origin', 'INCOME', 1234, ?, 'Recebimento', 'pending', 1, 1, ?, ?)
	`, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix(), now, now)

	handler := TransactionHandler{DB: db, WriterDB: db, Templates: testPaidInvoiceMutationTemplates(), WorkspaceID: "ws-payment-business", UserID: "business-user"}
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/business-payment/status-pagamento", url.Values{
		"payment_action":     {"paid"},
		"payment_date":       {"2026-07-14"},
		"payment_account_id": {"business-target"},
	}), "business-payment")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertPaymentAccountBalance(t, db, "business-origin", 10000)
	assertPaymentAccountBalance(t, db, "business-target", 21234)
}

func assertPaymentAccountBalance(t *testing.T, db *sql.DB, accountID string, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, accountID).Scan(&got); err != nil {
		t.Fatalf("query balance for %s: %v", accountID, err)
	}
	if got != want {
		t.Fatalf("balance for %s = %d, want %d", accountID, got, want)
	}
}
