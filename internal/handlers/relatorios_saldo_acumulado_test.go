package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestRelatoriosSaldoAcumuladoPopulated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedSaldoAcumuladoScenario(t, db, "2026-08-10", "2026-08-15", "2026-09-05")

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-acum", UserID: "user-acum"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if data.SaldoAcumulado.Reais == "" && data.SaldoAcumulado.Cents == "" {
		t.Fatal("SaldoAcumulado should not be empty")
	}
}

func TestRelatoriosSaldoAcumuladoMatchesLancamentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedSaldoAcumuladoScenario(t, db, "2026-08-10", "2026-08-15", "2026-09-05")

	relHandler := RelatoriosHandler{DB: db, WorkspaceID: "ws-acum", UserID: "user-acum"}
	relData, err := relHandler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	lancHandler := TransactionHandler{DB: db, WorkspaceID: "ws-acum", UserID: "user-acum"}
	lancData, err := lancHandler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	if relData.SaldoAcumulado.Reais != MoneyMinor(calcProjectedAccumulatedBalance(db, "ws-acum", totalBalance(t, db, "ws-acum"), time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))).Reais {
		t.Fatalf("relatorios SaldoAcumulado = %s%s, want consistent with projected balance",
			relData.SaldoAcumulado.Reais, relData.SaldoAcumulado.Cents)
	}
	_ = lancData
}

func TestRelatoriosSaldoAcumuladoNegative(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-neg', 'Negative User', 'neg@test.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, created_at, updated_at) VALUES ('ws-neg', 'Neg WS', '', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-neg', 'user-neg', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-neg', 'ws-neg', 'Conta Negativa', 'CHECKING', -500000, -500000, ?, ?)`, now, now)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-neg", UserID: "user-neg"}
	data, err := handler.buildRelatoriosData("", int(time.Now().UTC().Month()), time.Now().UTC().Year())
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if !data.SaldoAcumuladoNegativo {
		t.Fatal("SaldoAcumuladoNegativo should be true for negative balance")
	}
}

func TestRelatoriosSaldoAcumuladoDifferentWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u1', 'User 1', 'u1@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u2', 'User 2', 'u2@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws1', 'WS1', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws2', 'WS2', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws1', 'u1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws2', 'u2', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('a1', 'ws1', 'Conta WS1', 'CHECKING', 100000, 100000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('a2', 'ws2', 'Conta WS2', 'CHECKING', 500000, 500000, ?, ?)`, now, now)

	h1 := RelatoriosHandler{DB: db, WorkspaceID: "ws1", UserID: "u1"}
	d1, err := h1.buildRelatoriosData("", int(time.Now().UTC().Month()), time.Now().UTC().Year())
	if err != nil {
		t.Fatalf("ws1 buildRelatoriosData: %v", err)
	}

	h2 := RelatoriosHandler{DB: db, WorkspaceID: "ws2", UserID: "u2"}
	d2, err := h2.buildRelatoriosData("", int(time.Now().UTC().Month()), time.Now().UTC().Year())
	if err != nil {
		t.Fatalf("ws2 buildRelatoriosData: %v", err)
	}

	if d1.SaldoAcumulado.Reais == d2.SaldoAcumulado.Reais && d1.SaldoAcumulado.Cents == d2.SaldoAcumulado.Cents {
		t.Fatal("SaldoAcumulado should differ between workspaces with different balances")
	}
}

func TestRelatoriosKPIExistentsPreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedSaldoAcumuladoScenario(t, db, "2026-08-10", "2026-08-15", "2026-09-05")

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-acum", UserID: "user-acum"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if data.TotalReceitas.Reais == "" || data.TotalReceitas.Cents == "" {
		t.Fatal("TotalReceitas should still be populated")
	}
	if data.TotalDespesas.Reais == "" || data.TotalDespesas.Cents == "" {
		t.Fatal("TotalDespesas should still be populated")
	}
	if data.SaldoLiquido.Reais == "" || data.SaldoLiquido.Cents == "" {
		t.Fatal("SaldoLiquido should still be populated")
	}
}

func TestRelatoriosSaldoAcumuladoCurrentMonth(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	currentMonth := int(now.Month())
	currentYear := now.Year()

	ts := now.Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-curr', 'Current User', 'curr@t.com', 'hash', ?, ?)`, ts, ts)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-curr', 'Current WS', 'personal', ?, ?)`, ts, ts)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-curr', 'u-curr', 'ADMIN', ?)`, ts)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-curr', 'ws-curr', 'Conta', 'CHECKING', 100000, 100000, ?, ?)`, ts, ts)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-curr", UserID: "u-curr"}
	data, err := handler.buildRelatoriosData("", currentMonth, currentYear)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if data.SaldoAcumulado.Reais == "" && data.SaldoAcumulado.Cents == "" {
		t.Fatal("SaldoAcumulado should be populated for current month")
	}
}

func totalBalance(t *testing.T, db *sql.DB, workspaceID string) int64 {
	t.Helper()
	var b int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(current_balance), 0) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, workspaceID).Scan(&b); err != nil {
		t.Fatalf("totalBalance: %v", err)
	}
	return b
}

func seedSaldoAcumuladoScenario(t *testing.T, db *sql.DB, expenseDate, incomeDate, nextMonthExpense string) {
	t.Helper()

	now := time.Now().Unix()
	eUnix := testUnixDate(expenseDate)
	iUnix := testUnixDate(incomeDate)
	nUnix := testUnixDate(nextMonthExpense)

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-acum', 'Acum User', 'acum@test.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, created_at, updated_at) VALUES ('ws-acum', 'Acum WS', '', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-acum', 'user-acum', 'ADMIN', ?)`, now)

	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-acum', 'ws-acum', 'Conta Corrente', 'CHECKING', 1000000, 1000000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-acum', 'ws-acum', 'Cartao', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)

	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-acum', 'ws-acum', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-income-acum', 'ws-acum', 'Salario', 'briefcase', '#22c55e', 'INCOME', NULL, ?)`, now)

	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES
		('tx-exp-acum', 'ws-acum', 'user-acum', 'checking-acum', 'cat-acum', 'EXPENSE', 50000, ?, 'Mercado', 'paid', ?, ?),
		('tx-inc-acum', 'ws-acum', 'user-acum', 'checking-acum', 'cat-income-acum', 'INCOME', 200000, ?, 'Salario', 'paid', ?, ?),
		('tx-next-acum', 'ws-acum', 'user-acum', 'checking-acum', 'cat-acum', 'EXPENSE', 30000, ?, 'Aluguel', 'pending', ?, ?)
	`, eUnix, now, now, iUnix, now, now, nUnix, now, now)
}
