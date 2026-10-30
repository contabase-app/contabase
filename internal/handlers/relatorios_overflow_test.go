package handlers

import (
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"
)

// seedOverflowReportWorkspace cria user/workspace/membro/conta para os cenários
// extremos da F11 High (agregados de relatório).
func seedOverflowReportWorkspace(t *testing.T, db *sql.DB, workspaceID, userID, accountID, workspaceType string, balance int64) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'hash', ?, ?)
	`, userID, userID, userID+"@test.com", now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES (?, ?, '', ?, ?, ?)
	`, workspaceID, workspaceID, workspaceType, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', ?)
	`, workspaceID, userID, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Conta Extrema', 'CHECKING', 0, ?, ?, ?)
	`, accountID, workspaceID, balance, now, now)
}

// seedOverflowReportTransaction insere lançamento com valor no limite de int64.
// amounts extremos são aceitos pelo schema (CHECK amount > 0).
func seedOverflowReportTransaction(t *testing.T, db *sql.DB, id, workspaceID, userID, accountID, txType string, amount, date int64, status string) {
	t.Helper()

	var paidAt interface{}
	if status == "paid" {
		paidAt = date
	}
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, paid_at, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'valor extremo', ?, 1, 1, ?, ?)
	`, id, workspaceID, userID, accountID, txType, amount, date, paidAt, status, now, now)
}

// TestBuildRelatoriosPersonalExtremeError prova que o caminho de relatório
// Personal falha explicitamente quando a soma de eventos de caixa estoura
// int64, em vez de exibir/exportar um total wrapped.
func TestBuildRelatoriosPersonalExtremeError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	const (
		wsID  = "ws-ovf-personal"
		usrID = "user-ovf-personal"
		accID = "acc-ovf-personal"
	)
	seedOverflowReportWorkspace(t, db, wsID, usrID, accID, "personal", 0)

	now := appTime(time.Now())
	midMonth := time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 0, time.UTC).Unix()
	seedOverflowReportTransaction(t, db, "tx-ovf-p1", wsID, usrID, accID, "INCOME", math.MaxInt64, midMonth, "paid")
	seedOverflowReportTransaction(t, db, "tx-ovf-p2", wsID, usrID, accID, "INCOME", math.MaxInt64, midMonth, "paid")

	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: wsID, UserID: usrID}
	data, err := h.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", int(now.Month()), now.Year())
	if err == nil {
		t.Fatalf("expected overflow error on personal extreme report, got totals receitas=%+v", data.TotalReceitas)
	}
	if !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("expected errReportAggregateOverflow, got %v", err)
	}
	if data.TotalReceitas.Reais != "" || data.TotalDespesas.Reais != "" || data.SaldoAcumulado.Reais != "" {
		t.Fatalf("overflow must not populate report totals, got receitas=%+v despesas=%+v acumulado=%+v",
			data.TotalReceitas, data.TotalDespesas, data.SaldoAcumulado)
	}
}

// TestBuildRelatoriosBusinessExtremeError cobre o mesmo contrato para o
// workspace Business em dois caminhos:
//
//  1. tab exclusivo de business (competência): o agregado SQL do período
//     estoura int64 no próprio SQLite e o builder retorna erro;
//  2. tab compartilhado caixa/previsão: a soma de eventos em Go é a primeira a
//     estourar e retorna errReportAggregateOverflow.
//
// Em ambos os casos nenhum total pode ser populado.
func TestBuildRelatoriosBusinessExtremeError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	const (
		wsID  = "ws-ovf-business"
		usrID = "user-ovf-business"
		accID = "acc-ovf-business"
	)
	seedOverflowReportWorkspace(t, db, wsID, usrID, accID, "business", 0)

	now := appTime(time.Now())
	midMonth := time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 0, time.UTC).Unix()
	seedOverflowReportTransaction(t, db, "tx-ovf-b1", wsID, usrID, accID, "INCOME", math.MaxInt64, midMonth, "paid")
	seedOverflowReportTransaction(t, db, "tx-ovf-b2", wsID, usrID, accID, "INCOME", math.MaxInt64, midMonth, "paid")

	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: wsID, UserID: usrID}

	// Caminho 1: agregação SQL do tab de competência.
	data, err := h.buildRelatoriosDataWithModo("", "competencia", "movimentos", "realizado", int(now.Month()), now.Year())
	if err == nil {
		t.Fatalf("expected overflow error on business competencia report, got totals receitas=%+v", data.TotalReceitas)
	}
	t.Logf("business competencia extreme error: %v", err)
	if data.TotalReceitas.Reais != "" || data.SaldoLiquido.Reais != "" || data.SaldoAcumulado.Reais != "" {
		t.Fatalf("overflow must not populate report totals, got receitas=%+v liquido=%+v acumulado=%+v",
			data.TotalReceitas, data.SaldoLiquido, data.SaldoAcumulado)
	}

	// Caminho 2: soma de eventos em Go no caixa/previsão.
	data, err = h.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", int(now.Month()), now.Year())
	if err == nil {
		t.Fatalf("expected checked-add overflow error on business previsao report, got totals receitas=%+v", data.TotalReceitas)
	}
	if !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("expected errReportAggregateOverflow on business previsao, got %v", err)
	}
	if data.TotalReceitas.Reais != "" || data.SaldoAcumulado.Reais != "" {
		t.Fatalf("overflow must not populate report totals, got receitas=%+v acumulado=%+v", data.TotalReceitas, data.SaldoAcumulado)
	}
}

// TestBuildRelatoriosCurrentBalanceSumOverflow prova que erro de
// SUM(current_balance) no SQLite é propagado pelo builder e nunca convertido
// em saldo acumulado zero.
func TestBuildRelatoriosCurrentBalanceSumOverflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	const (
		wsID  = "ws-ovf-balance"
		usrID = "user-ovf-balance"
	)
	now := time.Now().Unix()
	seedOverflowReportWorkspace(t, db, wsID, usrID, "acc-ovf-balance-1", "personal", math.MaxInt64)
	// A soma das duas contas ultrapassa int64 no próprio SQLite.
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-ovf-balance-2', ?, 'Conta Extrema 2', 'CHECKING', 0, ?, ?, ?)
	`, wsID, math.MaxInt64, now, now)

	appNow := appTime(time.Now())
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: wsID, UserID: usrID}
	data, err := h.buildRelatoriosData("", "caixa", "movimentos", int(appNow.Month()), appNow.Year())
	if err == nil {
		t.Fatalf("expected SQL sum overflow error, got saldo acumulado=%+v", data.SaldoAcumulado)
	}
	t.Logf("current balance sum error: %v", err)
	if data.SaldoAcumulado.Reais != "" || data.SaldoAcumulado.Cents != "" {
		t.Fatalf("SQL error must not populate saldo acumulado, got %+v", data.SaldoAcumulado)
	}
}

// TestCalcProjectedAccumulatedBalanceOverflow prova, no fluxo real de
// projeção, que o limite exato de int64 é válido e que um centavo além dele
// falha explicitamente em vez de wrapar.
func TestCalcProjectedAccumulatedBalanceOverflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	const (
		wsID            = "ws-ovf-proj"
		usrID           = "user-ovf-proj"
		accID           = "acc-ovf-proj"
		projectedIncome = int64(100000)
	)
	seedOverflowReportWorkspace(t, db, wsID, usrID, accID, "personal", 0)

	civilToday := AppCivilDateUTC(time.Now()).Unix()
	seedOverflowReportTransaction(t, db, "tx-ovf-proj", wsID, usrID, accID, "INCOME", projectedIncome, civilToday, "pending")

	selectedMonth := appMonthStartUTC(time.Now())

	got, err := calcProjectedAccumulatedBalance(db, wsID, math.MaxInt64-projectedIncome, selectedMonth)
	if err != nil {
		t.Fatalf("exact int64 limit must succeed, got error: %v", err)
	}
	if got != math.MaxInt64 {
		t.Fatalf("exact int64 limit = %d, want MaxInt64", got)
	}

	if _, err := calcProjectedAccumulatedBalance(db, wsID, math.MaxInt64-projectedIncome+1, selectedMonth); err == nil {
		t.Fatal("expected overflow error one cent above the int64 limit")
	} else if !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("expected errReportAggregateOverflow, got %v", err)
	}
}

// TestCalcProjectedAccumulatedBalanceSQLError prova que erro SQL em qualquer
// ponto da cadeia de projeção é propagado e nunca vira zero silencioso.
func TestCalcProjectedAccumulatedBalanceSQLError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	const (
		wsID  = "ws-ovf-projsql"
		usrID = "user-ovf-projsql"
		accID = "acc-ovf-projsql"
	)
	seedOverflowReportWorkspace(t, db, wsID, usrID, accID, "personal", 1000)

	selectedMonth := appMonthStartUTC(time.Now())

	// Controle: sem erro a projeção devolve o saldo base.
	if got, err := calcProjectedAccumulatedBalance(db, wsID, 1000, selectedMonth); err != nil || got != 1000 {
		t.Fatalf("control call = %d, err = %v; want 1000, nil", got, err)
	}

	// Erro em uma query da cadeia (regras de recorrência ausentes).
	execTestSQL(t, db, `DROP TABLE recurring_rules`)
	if _, err := calcProjectedAccumulatedBalance(db, wsID, 1000, selectedMonth); err == nil {
		t.Fatal("expected SQL error from recurring rules query to propagate")
	}

	// Erro de conexão: nenhum braço da cadeia pode devolver zero silencioso.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if _, err := calcProjectedAccumulatedBalance(db, wsID, 1000, selectedMonth); err == nil {
		t.Fatal("expected closed-database error to propagate in the projected branch")
	}
	pastMonth := selectedMonth.AddDate(0, -1, 0)
	if _, err := calcProjectedAccumulatedBalance(db, wsID, 1000, pastMonth); err == nil {
		t.Fatal("expected closed-database error to propagate in the paid branch")
	}
}
