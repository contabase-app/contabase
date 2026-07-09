package handlers

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestLancamentosResumoStatusDefaultReflectsVisibleItems(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}
	data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	assertLancamentosSummary(t, data, "pago", 16000, 2)
	assertLancamentosSummary(t, data, "pendente", 131000, 3)
	assertLancamentosSummary(t, data, "vencido", 0, 0)
	assertLancamentosSummary(t, data, "agendado", 33000, 1)
}

func TestLancamentosResumoStatusPagoReflectsVisiblePaidItems(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}
	data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	assertLancamentosSummary(t, data, "pago", 16000, 2)
	assertLancamentosSummary(t, data, "pendente", 0, 0)
	assertLancamentosSummary(t, data, "vencido", 0, 0)
	assertLancamentosSummary(t, data, "agendado", 0, 0)
}

func TestLancamentosResumoStatusRespectsCategoryAccountSearchAndType(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}

	byCategory, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Categorias: []string{"cat-food-summary"}})
	if err != nil {
		t.Fatalf("buildLancamentosData category: %v", err)
	}
	assertLancamentosSummary(t, byCategory, "pendente", 22000, 1)
	assertLancamentosSummary(t, byCategory, "agendado", 33000, 1)

	byAccount, err := handler.buildLancamentosData("checking-summary", 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData account: %v", err)
	}
	assertLancamentosSummary(t, byAccount, "pendente", 92000, 2)
	assertLancamentosSummary(t, byAccount, "agendado", 33000, 1)

	bySearch, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Busca: "Card Purchase"})
	if err != nil {
		t.Fatalf("buildLancamentosData search: %v", err)
	}
	assertLancamentosSummary(t, bySearch, "pendente", 39000, 1)
	assertLancamentosSummary(t, bySearch, "agendado", 0, 0)

	byType, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Tipos: []string{"receita"}})
	if err != nil {
		t.Fatalf("buildLancamentosData type: %v", err)
	}
	assertLancamentosSummary(t, byType, "pendente", 70000, 1)
	assertLancamentosSummary(t, byType, "agendado", 0, 0)
}

func TestLancamentosResumoStatusKeepsWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary-other", UserID: "user-summary"}
	data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	assertLancamentosSummary(t, data, "pendente", 99000, 1)
	assertLancamentosSummary(t, data, "pago", 0, 0)
	assertLancamentosSummary(t, data, "agendado", 0, 0)
}

func assertLancamentosSummary(t *testing.T, data LancamentosData, key string, amount int64, count int) {
	t.Helper()
	item, ok := lancamentosSummaryByKey(data, key)
	if !ok {
		t.Fatalf("summary %q not found in %#v", key, data.ResumoStatus)
	}
	if item.Amount != formatCurrencyLabel(amount) {
		t.Fatalf("summary %s amount = %q, want %q", key, item.Amount, formatCurrencyLabel(amount))
	}
	if item.Count != count {
		t.Fatalf("summary %s count = %d, want %d", key, item.Count, count)
	}
}

func lancamentosSummaryByKey(data LancamentosData, key string) (LancamentosStatusSummary, bool) {
	for _, item := range data.ResumoStatus {
		if item.Key == key {
			return item, true
		}
	}
	return LancamentosStatusSummary{}, false
}

func seedLancamentosStatusSummaryScenario(t *testing.T, db *sql.DB, year int) {
	t.Helper()
	now := time.Now().Unix()
	aug := func(day int) int64 {
		return testUnixDate(fmt.Sprintf("%04d-08-%02d", year, day))
	}

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-summary', 'Summary User', 'summary@test.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-summary', 'Summary WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-summary-other', 'Summary Other', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-summary', 'user-summary', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-summary-other', 'user-summary', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-summary', 'ws-summary', 'Conta Summary', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-summary', 'ws-summary', 'Card Summary', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-summary-other', 'ws-summary-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-summary', 'card-summary', 25, 15, 500000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-food-summary', 'ws-summary', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-card-summary', 'ws-summary', 'Cartao', 'credit-card', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-income-summary', 'ws-summary', 'Salario', 'briefcase', '#22c55e', 'INCOME', NULL, ?)`, now)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('invoice-summary', 'card-summary', ?, ?, ?, 'OPEN', ?)`, fmt.Sprintf("%04d-08", year), aug(5), aug(15), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES
		('paid-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-food-summary', 'EXPENSE', 11000, ?, 'Paid Hidden', 'paid', ?, ?),
		('pending-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-food-summary', 'EXPENSE', 22000, ?, 'Pending Lunch', 'pending', ?, ?),
		('income-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-income-summary', 'INCOME', 70000, ?, 'Pending Salary', 'pending', ?, ?),
		('other-summary', 'ws-summary-other', 'user-summary', 'checking-summary-other', NULL, 'EXPENSE', 99000, ?, 'Other Workspace', 'pending', ?, ?)`,
		aug(2), now, now,
		aug(3), now, now,
		aug(3), now, now,
		aug(3), now, now,
	)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, scheduled_payment_at, created_at, updated_at) VALUES ('scheduled-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-food-summary', 'EXPENSE', 33000, ?, 'Scheduled Rent', 'pending', ?, ?, ?)`, aug(4), aug(20), now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, created_at, updated_at) VALUES ('purchase-summary', 'ws-summary', 'user-summary', 'card-summary', 'cat-card-summary', 'invoice-summary', 'EXPENSE', 44000, ?, 'Card Purchase', 'paid', ?, ?)`, aug(6), now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, created_at, updated_at) VALUES ('payment-summary', 'ws-summary', 'user-summary', 'checking-summary', 'EXPENSE', 5000, ?, ?, 'paid', ?, ?)`, aug(7), invoicePaymentDescription("Card Summary"), now, now)
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at) VALUES ('invoice-payment-summary', 'ws-summary', 'invoice-summary', 'checking-summary', 'payment-summary', 5000, ?, 'manual', 'user-summary', ?)`, aug(7), now)
}
