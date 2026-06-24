package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestDashboardPendingWindowIncludesOverdueAndNext7Days(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	seedDashboardPendingWindowScenario(t, db)

	payables := QueryDashboardPendingPayables7d(db, "ws-dashboard", now)
	assertDashboardPendingDescriptions(t, "payables", payables, []string{
		"Despesa vencida",
		"Fatura Cartao Dashboard - 2026-08",
		"Despesa fallback date",
		"Despesa proximos 7 dias",
		"Fatura Cartao Dashboard - 2026-09",
	})
	assertDashboardPendingNotContains(t, "payables", payables,
		"Despesa fora janela",
		"Despesa paga",
		"Transferencia pendente",
		"Despesa outro workspace",
		"Compra fatura vencida",
		"Compra fatura proximos 7 dias",
		"Compra fatura paga",
		"Compra fatura fora janela",
		"Pagamento de Fatura - Cartao Dashboard",
	)
	if !payables[0].IsOverdue {
		t.Fatalf("first payable should be overdue: %#v", payables[0])
	}

	payableTotal, payableCount := queryDashboardPayable7dTotal(db, "ws-dashboard", now)
	assertMoneyDisplay(t, "payable total", payableTotal, "1.500", ",00")
	if payableCount != 5 {
		t.Fatalf("payable count = %d, want 5", payableCount)
	}

	receivables := QueryDashboardPendingReceivables7d(db, "ws-dashboard", now)
	assertDashboardPendingDescriptions(t, "receivables", receivables, []string{
		"Receita vencida",
		"Receita fallback date",
		"Receita proximos 7 dias",
	})
	assertDashboardPendingNotContains(t, "receivables", receivables,
		"Receita fora janela",
		"Receita recebida",
		"Receita outro workspace",
	)
	if !receivables[0].IsOverdue {
		t.Fatalf("first receivable should be overdue: %#v", receivables[0])
	}

	receivableTotal, receivableCount := queryDashboardReceivable7dTotal(db, "ws-dashboard", now)
	assertMoneyDisplay(t, "receivable total", receivableTotal, "950", ",00")
	if receivableCount != 3 {
		t.Fatalf("receivable count = %d, want 3", receivableCount)
	}
}

func seedDashboardPendingWindowScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-dashboard', 'User Dashboard', 'dash@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-other', 'User Other', 'other@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-dashboard', 'Dashboard WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-other', 'Other WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-dashboard', 'user-dashboard', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-other', 'user-other', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-dashboard', 'ws-dashboard', 'Conta Dashboard', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-dashboard-dest', 'ws-dashboard', 'Conta Destino', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-dashboard', 'ws-dashboard', 'Cartao Dashboard', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-other', 'ws-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-other', 'ws-other', 'Cartao Other', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-dashboard', 'card-dashboard', 20, 10, 500000)`)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-other', 'card-other', 20, 10, 500000)`)

	insertDashboardPendingTx(t, db, "pay-overdue", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "EXPENSE", 10000, "2026-08-01", "2026-08-08", "Despesa vencida", "pending")
	insertDashboardPendingTx(t, db, "pay-window", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "EXPENSE", 20000, "2026-08-10", "2026-08-15", "Despesa proximos 7 dias", "pending")
	insertDashboardPendingTx(t, db, "pay-outside", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "EXPENSE", 30000, "2026-08-10", "2026-08-18", "Despesa fora janela", "pending")
	insertDashboardPendingTx(t, db, "pay-paid", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "EXPENSE", 40000, "2026-08-10", "2026-08-12", "Despesa paga", "paid")
	insertDashboardPendingTx(t, db, "pay-fallback", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "EXPENSE", 50000, "2026-08-13", "", "Despesa fallback date", "pending")
	insertDashboardPendingTx(t, db, "transfer-window", "ws-dashboard", "user-dashboard", "acc-dashboard", "acc-dashboard-dest", "TRANSFER", 60000, "2026-08-10", "2026-08-12", "Transferencia pendente", "pending")
	insertDashboardPendingTx(t, db, "pay-other", "ws-other", "user-other", "acc-other", "", "EXPENSE", 70000, "2026-08-10", "2026-08-12", "Despesa outro workspace", "pending")
	insertDashboardPendingTx(t, db, "invoice-payment-pending", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "EXPENSE", 90000, "2026-08-10", "2026-08-12", invoicePaymentDescription("Cartao Dashboard"), "pending")

	insertDashboardPendingTx(t, db, "rec-overdue", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "INCOME", 15000, "2026-08-01", "2026-08-08", "Receita vencida", "pending")
	insertDashboardPendingTx(t, db, "rec-window", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "INCOME", 25000, "2026-08-10", "2026-08-16", "Receita proximos 7 dias", "pending")
	insertDashboardPendingTx(t, db, "rec-outside", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "INCOME", 35000, "2026-08-10", "2026-08-18", "Receita fora janela", "pending")
	insertDashboardPendingTx(t, db, "rec-paid", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "INCOME", 45000, "2026-08-10", "2026-08-12", "Receita recebida", "paid")
	insertDashboardPendingTx(t, db, "rec-fallback", "ws-dashboard", "user-dashboard", "acc-dashboard", "", "INCOME", 55000, "2026-08-13", "", "Receita fallback date", "pending")
	insertDashboardPendingTx(t, db, "rec-other", "ws-other", "user-other", "acc-other", "", "INCOME", 65000, "2026-08-10", "2026-08-12", "Receita outro workspace", "pending")

	insertDashboardInvoice(t, db, "invoice-overdue", "card-dashboard", "2026-08", "2026-07-20", "2026-08-09", "CLOSED", 0)
	insertDashboardInvoice(t, db, "invoice-window", "card-dashboard", "2026-09", "2026-08-20", "2026-08-16", "OPEN", 0)
	insertDashboardInvoice(t, db, "invoice-paid", "card-dashboard", "2026-10", "2026-09-20", "2026-08-12", "PAID", 60000)
	insertDashboardInvoice(t, db, "invoice-outside", "card-dashboard", "2026-11", "2026-10-20", "2026-08-18", "OPEN", 0)
	insertDashboardInvoice(t, db, "invoice-other", "card-other", "2026-08", "2026-07-20", "2026-08-12", "OPEN", 0)

	insertDashboardCardPurchase(t, db, "card-purchase-overdue", "ws-dashboard", "user-dashboard", "card-dashboard", "invoice-overdue", 30000, "2026-08-01", "Compra fatura vencida")
	insertDashboardCardPurchase(t, db, "card-purchase-window", "ws-dashboard", "user-dashboard", "card-dashboard", "invoice-window", 40000, "2026-08-10", "Compra fatura proximos 7 dias")
	insertDashboardCardPurchase(t, db, "card-purchase-paid", "ws-dashboard", "user-dashboard", "card-dashboard", "invoice-paid", 60000, "2026-08-10", "Compra fatura paga")
	insertDashboardCardPurchase(t, db, "card-purchase-outside", "ws-dashboard", "user-dashboard", "card-dashboard", "invoice-outside", 70000, "2026-08-10", "Compra fatura fora janela")
	insertDashboardCardPurchase(t, db, "card-purchase-other", "ws-other", "user-other", "card-other", "invoice-other", 80000, "2026-08-10", "Compra fatura outro workspace")
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES ('invoice-payment-pending-row', 'ws-dashboard', 'invoice-paid', 'acc-dashboard', 'invoice-payment-pending', 90000, ?, 'manual', ?)
	`, now, now)
}

func insertDashboardPendingTx(t *testing.T, db *sql.DB, id, workspaceID, userID, accountID, destAccountID, txType string, amount int64, date, dueDate, description, status string) {
	t.Helper()

	now := time.Now().Unix()
	var destination interface{}
	if destAccountID != "" {
		destination = destAccountID
	}
	var due interface{}
	if dueDate != "" {
		due = testUnixDate(dueDate)
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, due_date, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, workspaceID, userID, accountID, destination, txType, amount, testUnixDate(date), description, status, due, now, now)
}

func insertDashboardInvoice(t *testing.T, db *sql.DB, id, accountID, reference, closingDate, dueDate, status string, paidAmount int64) {
	t.Helper()

	now := time.Now().Unix()
	var paidAt interface{}
	var paidValue interface{}
	if status == "PAID" {
		paidAt = now
		paidValue = paidAmount
	}
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, reference, testUnixDate(closingDate), testUnixDate(dueDate), status, paidAt, paidValue, now)
}

func insertDashboardCardPurchase(t *testing.T, db *sql.DB, id, workspaceID, userID, accountID, invoiceID string, amount int64, date, description string) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', ?, ?, ?, 'paid', 1, 1, ?, ?)
	`, id, workspaceID, userID, accountID, invoiceID, amount, testUnixDate(date), description, now, now)
}

func assertDashboardPendingDescriptions(t *testing.T, label string, items []DashboardPayableItem, want []string) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("%s len = %d, want %d; items = %#v", label, len(items), len(want), items)
	}
	for i, wantDescription := range want {
		if items[i].Description != wantDescription {
			t.Fatalf("%s[%d] = %q, want %q; items = %#v", label, i, items[i].Description, wantDescription, items)
		}
	}
}

func assertDashboardPendingNotContains(t *testing.T, label string, items []DashboardPayableItem, descriptions ...string) {
	t.Helper()
	blocked := make(map[string]struct{}, len(descriptions))
	for _, description := range descriptions {
		blocked[description] = struct{}{}
	}
	for _, item := range items {
		if _, ok := blocked[item.Description]; ok {
			t.Fatalf("%s unexpectedly contains %q; items = %#v", label, item.Description, items)
		}
	}
}

func TestDashboardPendingPartiallyPaidInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	seedDashboardPartialInvoicePaymentScenario(t, db)

	payables := QueryDashboardPendingPayables7d(db, "ws-partial", now)
	if len(payables) != 1 {
		t.Fatalf("payables len = %d, want 1; items = %#v", len(payables), payables)
	}
	if payables[0].Description != "Fatura Cartao Parcial - 2026-08" {
		t.Fatalf("description = %q, want %q", payables[0].Description, "Fatura Cartao Parcial - 2026-08")
	}
	if payables[0].IsOverdue {
		t.Fatalf("expected non-overdue invoice (due Aug 15, today Aug 10); item = %#v", payables[0])
	}

	assertMoneyDisplay(t, "partial invoice amount", payables[0].Amount, "250", ",00")

	payableTotal, payableCount := queryDashboardPayable7dTotal(db, "ws-partial", now)
	assertMoneyDisplay(t, "payable total", payableTotal, "250", ",00")
	if payableCount != 1 {
		t.Fatalf("payable count = %d, want 1", payableCount)
	}
}

func seedDashboardPartialInvoicePaymentScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-partial', 'User Partial', 'partial@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-partial', 'Partial WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-partial', 'user-partial', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-partial-checking', 'ws-partial', 'Conta Corrente', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-partial', 'ws-partial', 'Cartao Parcial', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-partial', 'card-partial', 20, 10, 500000)`)

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-partial', 'card-partial', '2026-08', ?, ?, 'OPEN', ?)
	`, testUnixDate("2026-07-20"), testUnixDate("2026-08-15"), now)

	insertDashboardCardPurchase(t, db, "purchase-partial-1", "ws-partial", "user-partial", "card-partial", "invoice-partial", 20000, "2026-07-10", "Compra 1")
	insertDashboardCardPurchase(t, db, "purchase-partial-2", "ws-partial", "user-partial", "card-partial", "invoice-partial", 15000, "2026-07-15", "Compra 2")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-partial-tx', 'ws-partial', 'user-partial', 'acc-partial-checking', NULL, 'EXPENSE', 10000, ?, 'Pagamento parcial renomeado', 'paid', 1, 1, ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES ('pay-partial', 'ws-partial', 'invoice-partial', 'acc-partial-checking', 'pay-partial-tx', 10000, ?, 'manual', ?)
	`, now, now)
}
