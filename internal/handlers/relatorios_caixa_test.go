package handlers

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func seedCaixaRealizadoScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-caixa', 'User Caixa', 'u-caixa@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-caixa', 'WS Caixa', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-caixa', 'u-caixa', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-caixa', 'ws-caixa', 'Conta Corrente', 'CHECKING', 1000000, 1000000, ?, ?),
		('sav-caixa', 'ws-caixa', 'Poupanca', 'SAVINGS', 200000, 200000, ?, ?),
		('card-caixa', 'ws-caixa', 'Cartao Visa', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-caixa', 'card-caixa', 20, 10, 500000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-food-caixa', 'ws-caixa', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?),
		('cat-salary-caixa', 'ws-caixa', 'Salario', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-card-caixa', 'ws-caixa', 'Compras Cartao', 'shopping-bag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now, now, now)
}

func insertCaixaTransaction(t *testing.T, db *sql.DB, id, txType, account, category string, amount int64, dateUnix, paidAt int64, status string) {
	t.Helper()
	now := time.Now().Unix()
	var paidAtArg interface{}
	if paidAt > 0 {
		paidAtArg = paidAt
	} else {
		paidAtArg = nil
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES (?, 'ws-caixa', 'u-caixa', ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
	`, id, account, category, txType, amount, dateUnix, id, status, paidAtArg, now, now)
}

func TestCaixaNaoIncluiPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "exp-pending", "EXPENSE", "chk-caixa", "cat-food-caixa", 50000, agostoDate, 0, "pending")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalDespesas.Reais != "0" || data.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa despesas = R$ %s%s, want R$ 0,00 (pending nao entra)", data.TotalDespesas.Reais, data.TotalDespesas.Cents)
	}
	if data.RealizedCashCount != 0 {
		t.Fatalf("RealizedCashCount = %d, want 0", data.RealizedCashCount)
	}
}

func TestCaixaNaoIncluiProximosVencimentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "exp-future", "EXPENSE", "chk-caixa", "cat-food-caixa", 50000, agostoDate, 0, "pending")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if len(data.UpcomingCashItems) != 0 {
		t.Fatalf("UpcomingCashItems deveria estar vazio no caixa realizado: %#v", data.UpcomingCashItems)
	}
}

func TestCaixaNaoUsaDueDateComoBase(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	julhoDate := testUnixDate("2026-07-10")
	agostoDue := testUnixDate("2026-08-15")
	agostoPaid := testUnixDate("2026-08-20")
	insertCaixaTransaction(t, db, "exp-due", "EXPENSE", "chk-caixa", "cat-food-caixa", 50000, julhoDate, agostoPaid, "paid")
	execTestSQL(t, db, `UPDATE transactions SET due_date = ? WHERE id = 'exp-due'`, agostoDue)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}

	julhoData, err := handler.buildRelatoriosData("", "caixa", "movimentos", 7, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData julho: %v", err)
	}
	if julhoData.TotalDespesas.Reais != "0" || julhoData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa julho despesas = R$ %s%s, want R$ 0,00 (base e paid_at, nao due_date)", julhoData.TotalDespesas.Reais, julhoData.TotalDespesas.Cents)
	}

	agostoData, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData agosto: %v", err)
	}
	if agostoData.TotalDespesas.Reais != "500" || agostoData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa agosto despesas = R$ %s%s, want R$ 500,00", agostoData.TotalDespesas.Reais, agostoData.TotalDespesas.Cents)
	}
}

func TestCaixaReceitaPagaEntraEmEntradasRecebidas(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "inc-paid", "INCOME", "chk-caixa", "cat-salary-caixa", 100000, agostoDate, agostoPaid, "paid")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalReceitas.Reais != "1.000" || data.TotalReceitas.Cents != ",00" {
		t.Fatalf("Caixa receitas = R$ %s%s, want R$ 1.000,00", data.TotalReceitas.Reais, data.TotalReceitas.Cents)
	}
	if data.RealizedIncomeTotal.Reais != "1.000" || data.RealizedIncomeTotal.Cents != ",00" {
		t.Fatalf("RealizedIncomeTotal = R$ %s%s, want R$ 1.000,00", data.RealizedIncomeTotal.Reais, data.RealizedIncomeTotal.Cents)
	}
}

func TestCaixaDespesaPagaEntraEmSaidasPagas(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "exp-paid", "EXPENSE", "chk-caixa", "cat-food-caixa", 50000, agostoDate, agostoPaid, "paid")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalDespesas.Reais != "500" || data.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa despesas = R$ %s%s, want R$ 500,00", data.TotalDespesas.Reais, data.TotalDespesas.Cents)
	}
	if data.RealizedExpenseTotal.Reais != "500" || data.RealizedExpenseTotal.Cents != ",00" {
		t.Fatalf("RealizedExpenseTotal = R$ %s%s, want R$ 500,00", data.RealizedExpenseTotal.Reais, data.RealizedExpenseTotal.Cents)
	}
}

func TestCaixaPagamentoFaturaEntraComoSaidaPaga(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-08-25")
	paymentDate := testUnixDate("2026-08-22")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-pay', 'card-caixa', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-pay-tx', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'inv-pay', 'EXPENSE', 30000, ?, 'Compra cartao', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-tx', 'ws-caixa', 'u-caixa', 'chk-caixa', 'EXPENSE', 30000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('ip-caixa', 'ws-caixa', 'inv-pay', 'chk-caixa', 'pay-tx', 30000, ?, 'manual', 'u-caixa', ?)
	`, paymentDate, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalDespesas.Reais != "300" || data.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa despesas = R$ %s%s, want R$ 300,00 (pagamento fatura entra)", data.TotalDespesas.Reais, data.TotalDespesas.Cents)
	}

	var foundCardCategory bool
	for _, c := range data.CategoriasCaixa {
		if c.Nome == "Compras Cartao" {
			foundCardCategory = true
			assertMoneyDisplay(t, "item fatura categoria", c.Valor, "300", ",00")
			break
		}
	}
	if !foundCardCategory {
		t.Fatalf("Categoria da compra no cartão nao encontrada na composição: %#v", data.CategoriasCaixa)
	}
	for _, c := range data.CategoriasCaixa {
		if c.Nome == "Fatura de cartão" {
			t.Fatalf("Composição não deve agrupar pagamento como Fatura de cartão: %#v", data.CategoriasCaixa)
		}
	}
}

func TestCaixaCompraCartaoNaoEntra(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-08-05")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-card', 'card-caixa', '2026-08', ?, ?, 'OPEN', ?)
	`, purchaseDate, purchaseDate, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-tx', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'inv-card', 'EXPENSE', 30000, ?, 'Compra cartao', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalDespesas.Reais != "0" || data.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa despesas = R$ %s%s, want R$ 0,00 (compra cartao nao entra)", data.TotalDespesas.Reais, data.TotalDespesas.Cents)
	}
}

func TestCaixaTransferenciaNaoAlteraResultado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	transferDate := testUnixDate("2026-08-10")
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('transfer-caixa', 'ws-caixa', 'u-caixa', 'chk-caixa', 'sav-caixa', 'TRANSFER', 50000, ?, 'Transferencia interna', 'paid', ?, 1, 1, ?, ?)
	`, transferDate, transferDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalReceitas.Reais != "0" || data.TotalReceitas.Cents != ",00" {
		t.Fatalf("Caixa receitas = R$ %s%s, want R$ 0,00", data.TotalReceitas.Reais, data.TotalReceitas.Cents)
	}
	if data.TotalDespesas.Reais != "0" || data.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa despesas = R$ %s%s, want R$ 0,00", data.TotalDespesas.Reais, data.TotalDespesas.Cents)
	}
}

func TestCaixaReceitasPorCategoriaApenasRealizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "inc-cat", "INCOME", "chk-caixa", "cat-salary-caixa", 100000, agostoDate, agostoPaid, "paid")
	insertCaixaTransaction(t, db, "inc-pending", "INCOME", "chk-caixa", "cat-salary-caixa", 50000, agostoDate, 0, "pending")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	var found bool
	for _, r := range data.RevenueByCategoryCaixa {
		if r.Name == "Salario" {
			found = true
			assertMoneyDisplay(t, "receita categoria caixa", r.Amount, "1.000", ",00")
			break
		}
	}
	if !found {
		t.Fatalf("Categoria Salario nao encontrada em RevenueByCategoryCaixa: %#v", data.RevenueByCategoryCaixa)
	}
}

func TestCaixaCustosPorMacroApenasRealizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "exp-macro", "EXPENSE", "chk-caixa", "cat-food-caixa", 50000, agostoDate, agostoPaid, "paid")
	insertCaixaTransaction(t, db, "exp-pending", "EXPENSE", "chk-caixa", "cat-food-caixa", 25000, agostoDate, 0, "pending")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	var found bool
	for _, row := range data.BusinessCostsCaixa {
		if row.MacroGroup == "Essencial" {
			found = true
			assertMoneyDisplay(t, "custo macro caixa", row.Amount, "500", ",00")
			break
		}
	}
	if !found {
		t.Fatalf("Macro Essencial nao encontrado em BusinessCostsCaixa: %#v", data.BusinessCostsCaixa)
	}
}

func TestCaixaGastosPorCategoriaApenasRealizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "exp-gasto", "EXPENSE", "chk-caixa", "cat-food-caixa", 50000, agostoDate, agostoPaid, "paid")
	insertCaixaTransaction(t, db, "exp-pending", "EXPENSE", "chk-caixa", "cat-food-caixa", 25000, agostoDate, 0, "pending")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	var found bool
	for _, c := range data.CategoriasCaixa {
		if c.Nome == "Alimentacao" && !c.IsGroup {
			found = true
			assertMoneyDisplay(t, "gasto categoria caixa", c.Valor, "500", ",00")
			break
		}
	}
	if !found {
		t.Fatalf("Categoria Alimentacao nao encontrada em CategoriasCaixa: %#v", data.CategoriasCaixa)
	}
}

func TestCaixaWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-other', 'Other', 'o@t.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-other', 'Other WS', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-other', 'u-other', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('chk-other', 'ws-other', 'Conta Other', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
			('cat-other', 'ws-other', 'Outra', 'x', '#000', 'EXPENSE', 'Essencial', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('exp-other', 'ws-other', 'u-other', 'chk-other', 'cat-other', 'EXPENSE', 99900, ?, 'Outra despesa', 'paid', ?, 1, 1, ?, ?)
	`, agostoDate, agostoPaid, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.TotalDespesas.Reais != "0" || data.TotalDespesas.Cents != ",00" {
		t.Fatalf("Caixa despesas = R$ %s%s, want R$ 0,00 (isolamento workspace)", data.TotalDespesas.Reais, data.TotalDespesas.Cents)
	}
}

func TestCaixaRealizadoCount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	agostoDate := testUnixDate("2026-08-10")
	agostoPaid := testUnixDate("2026-08-10")
	insertCaixaTransaction(t, db, "exp-1", "EXPENSE", "chk-caixa", "cat-food-caixa", 10000, agostoDate, agostoPaid, "paid")
	insertCaixaTransaction(t, db, "exp-2", "EXPENSE", "chk-caixa", "cat-food-caixa", 20000, agostoDate, agostoPaid, "paid")
	insertCaixaTransaction(t, db, "inc-1", "INCOME", "chk-caixa", "cat-salary-caixa", 50000, agostoDate, agostoPaid, "paid")

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	if data.RealizedCashCount != 3 {
		t.Fatalf("RealizedCashCount = %d, want 3", data.RealizedCashCount)
	}
}

func TestCaixaModoAusenteOuInvalidoCaiEmRealizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}

	for _, modo := range []string{"", "errado"} {
		data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", modo, 8, 2026)
		if err != nil {
			t.Fatalf("buildRelatoriosDataWithModo(%q): %v", modo, err)
		}
		if data.CaixaModo != "realizado" || !data.IsCaixaRealizado || data.IsCaixaPrevisao {
			t.Fatalf("modo %q normalizado errado: modo=%q realizado=%v previsao=%v", modo, data.CaixaModo, data.IsCaixaRealizado, data.IsCaixaPrevisao)
		}
	}
}

func TestCaixaPrevisaoIncluiPendentesEFaturaPorVencimentoSemDuplicarCartao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	julhoDate := testUnixDate("2026-07-10")
	agostoDue := testUnixDate("2026-08-15")
	cardPurchaseDate := testUnixDate("2026-08-05")
	invoiceDue := testUnixDate("2026-08-25")

	insertCaixaTransaction(t, db, "inc-prev", "INCOME", "chk-caixa", "cat-salary-caixa", 100000, julhoDate, 0, "pending")
	execTestSQL(t, db, `UPDATE transactions SET due_date = ? WHERE id = 'inc-prev'`, agostoDue)
	insertCaixaTransaction(t, db, "exp-prev", "EXPENSE", "chk-caixa", "cat-food-caixa", 25000, julhoDate, 0, "pending")
	execTestSQL(t, db, `UPDATE transactions SET due_date = ? WHERE id = 'exp-prev'`, agostoDue)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-prev', 'card-caixa', '2026-08', ?, ?, 'OPEN', ?)
	`, cardPurchaseDate, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-prev-tx', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'inv-prev', 'EXPENSE', 30000, ?, 'Compra cartao previsao', 'paid', 1, 1, ?, ?)
	`, cardPurchaseDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if data.CaixaModo != "previsao" || !data.IsCaixaPrevisao {
		t.Fatalf("modo = %q, previsao=%v", data.CaixaModo, data.IsCaixaPrevisao)
	}
	assertMoneyDisplay(t, "entradas previstas", data.ProjectedIncomeTotal, "1.000", ",00")
	assertMoneyDisplay(t, "saidas previstas", data.ProjectedExpenseTotal, "550", ",00")
	assertMoneyDisplay(t, "resultado previsto", data.CashMonthResultTotal, "450", ",00")
	var foundCardCategory bool
	for _, c := range data.CategoriasCaixa {
		if c.Nome == "Compras Cartao" {
			foundCardCategory = true
			assertMoneyDisplay(t, "composicao fatura prevista", c.Valor, "300", ",00")
		}
		if c.Nome == "Fatura de cartão" {
			t.Fatalf("previsao nao deve agrupar fatura inteira na composição: %#v", data.CategoriasCaixa)
		}
	}
	if !foundCardCategory {
		t.Fatalf("previsao deve abrir itens da fatura por categoria: %#v", data.CategoriasCaixa)
	}

	realizado, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("build realizado: %v", err)
	}
	assertMoneyDisplay(t, "realizado nao inclui pendentes/fatura aberta", realizado.RealizedExpenseTotal, "0", ",00")
}

func TestCaixaPrevisaoFaturaParcialUsaSaldoPendenteSemDuplicarPagamento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-08-05")
	invoiceDue := testUnixDate("2026-08-25")
	paymentDate := testUnixDate("2026-08-15")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-prev-partial', 'card-caixa', '2026-08', ?, ?, 'OPEN', ?)
	`, purchaseDate, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-prev-partial-tx', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'inv-prev-partial', 'EXPENSE', 40000, ?, 'Compra cartao parcial', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('pay-prev-partial-paid', 'ws-caixa', 'u-caixa', 'chk-caixa', 'EXPENSE', 25000, ?, ?, ?, 'paid', ?, 1, 1, ?, ?),
			('pay-prev-partial-pending', 'ws-caixa', 'u-caixa', 'chk-caixa', 'EXPENSE', 99999, ?, ?, ?, 'pending', NULL, 1, 1, ?, ?)
	`, paymentDate, paymentDate, invoicePaymentDescription("Cartao Visa"), paymentDate, now, now,
		invoiceDue, invoiceDue, invoicePaymentDescription("Cartao Visa"), now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('ip-prev-partial', 'ws-caixa', 'inv-prev-partial', 'chk-caixa', 'pay-prev-partial-paid', 25000, ?, 'manual', 'u-caixa', ?)
	`, paymentDate, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}

	assertMoneyDisplay(t, "saidas previstas com fatura parcial", data.ProjectedExpenseTotal, "400", ",00")
	assertMoneyDisplay(t, "resultado previsto com fatura parcial", data.CashMonthResultTotal, "-400", ",00")

	var caixaAugustTotal int64
	for _, row := range data.Caixa {
		if row.Vencimento == "2026-08" {
			caixaAugustTotal = row.RawResultado
			break
		}
	}
	if caixaAugustTotal != -40000 {
		t.Fatalf("Caixa 2026-08 = %d, want -40000", caixaAugustTotal)
	}

	var foundCardCategory bool
	for _, c := range data.CategoriasCaixa {
		if c.Nome == "Compras Cartao" {
			foundCardCategory = true
			assertMoneyDisplay(t, "composicao fatura parcial", c.Valor, "400", ",00")
		}
	}
	if !foundCardCategory {
		t.Fatalf("previsao deve abrir saldo pendente da fatura por categoria: %#v", data.CategoriasCaixa)
	}
}

func TestCaixaPendingInvoiceAllReversedUsesCanonicalEffectivePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixaRealizadoScenario(t, db)

	now := time.Now().Unix()
	dueDate := testUnixDate("2026-08-25")
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES ('inv-caixa-reversed', 'card-caixa', '2026-08', ?, ?, 'OPEN', 40000, ?)
	`, testUnixDate("2026-08-20"), dueDate, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-caixa-reversed', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'inv-caixa-reversed', 'EXPENSE', 40000, ?, 'Compra caixa revertida', 'paid', 1, 1, ?, ?)
	`, testUnixDate("2026-08-05"), now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, reversed_at, created_at)
		VALUES ('ip-caixa-reversed', 'ws-caixa', 'inv-caixa-reversed', 'chk-caixa', 40000, ?, 'manual', ?, ?)
	`, testUnixDate("2026-08-10"), testUnixDate("2026-08-11"), now)

	rows, err := buildPendingInvoiceCaixaRows(db, "ws-caixa", testUnixDate("2026-08-01"), testUnixDate("2026-09-01"))
	if err != nil {
		t.Fatalf("buildPendingInvoiceCaixaRows: %v", err)
	}
	if len(rows) != 1 || rows[0].AmountCents != -40000 {
		t.Fatalf("pending invoice caixa rows = %#v, want one row of -40000", rows)
	}

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	targets, err := handler.queryInvoiceAllocationTargets(context.Background(), testUnixDate("2026-08-01"), testUnixDate("2026-08-31")+86399, "pending")
	if err != nil {
		t.Fatalf("queryInvoiceAllocationTargets pending: %v", err)
	}
	if len(targets) != 1 || targets["inv-caixa-reversed"] != 40000 {
		t.Fatalf("pending allocation targets = %#v, want inv-caixa-reversed:40000", targets)
	}
}

func TestCaixaPrevisaoSomaRealizadoEPendenteSemDuplicarCartaoOuTransferencia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	paidDate := testUnixDate("2026-08-05")
	pendingDue := testUnixDate("2026-08-20")
	cardPurchaseDate := testUnixDate("2026-08-06")
	invoiceDue := testUnixDate("2026-08-25")
	previousDue := testUnixDate("2026-07-25")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('mix-paid-income', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-salary-caixa', 'INCOME', 1000000, ?, ?, 'Receita paga', 'paid', ?, 1, 1, ?, ?),
			('mix-pending-income', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-salary-caixa', 'INCOME', 500000, ?, ?, 'Receita pendente', 'pending', NULL, 1, 1, ?, ?),
			('mix-paid-expense', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 200000, ?, ?, 'Despesa paga', 'paid', ?, 1, 1, ?, ?),
			('mix-pending-expense', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 100000, ?, ?, 'Despesa pendente', 'pending', NULL, 1, 1, ?, ?),
			('mix-transfer', 'ws-caixa', 'u-caixa', 'chk-caixa', NULL, 'TRANSFER', 50000, ?, ?, 'Transferencia interna', 'pending', NULL, 1, 1, ?, ?)
	`, paidDate, paidDate, paidDate, now, now,
		paidDate, pendingDue, now, now,
		paidDate, paidDate, paidDate, now, now,
		paidDate, pendingDue, now, now,
		paidDate, pendingDue, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('mix-invoice', 'card-caixa', '2026-08', ?, ?, 'OPEN', ?)
	`, cardPurchaseDate, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('mix-card-purchase', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'mix-invoice', 'EXPENSE', 300000, ?, 'Compra no cartao', 'paid', 1, 1, ?, ?)
	`, cardPurchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('mix-card-payment', 'ws-caixa', 'u-caixa', 'chk-caixa', 'EXPENSE', 100000, ?, ?, ?, 'paid', ?, 1, 1, ?, ?)
	`, previousDue, previousDue, invoicePaymentDescription("Cartao Visa"), paidDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('mix-invoice-payment', 'ws-caixa', 'mix-invoice', 'chk-caixa', 'mix-card-payment', 100000, ?, 'manual', 'u-caixa', ?)
	`, paidDate, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	realizado, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("build realizado: %v", err)
	}
	assertMoneyDisplay(t, "realizado entradas", realizado.CashMonthIncomeTotal, "10.000", ",00")
	assertMoneyDisplay(t, "realizado saidas", realizado.CashMonthExpenseTotal, "3.000", ",00")
	assertMoneyDisplay(t, "realizado resultado", realizado.CashMonthResultTotal, "7.000", ",00")

	previsao, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("build previsao: %v", err)
	}
	assertMoneyDisplay(t, "previsao entradas", previsao.ProjectedIncomeTotal, "15.000", ",00")
	assertMoneyDisplay(t, "previsao saidas", previsao.ProjectedExpenseTotal, "6.000", ",00")
	assertMoneyDisplay(t, "previsao resultado", previsao.CashMonthResultTotal, "9.000", ",00")

	var caixaAugustTotal int64
	for _, row := range previsao.Caixa {
		if row.Vencimento == "2026-08" {
			caixaAugustTotal = row.RawResultado
			break
		}
	}
	if caixaAugustTotal != 900000 {
		t.Fatalf("Caixa 2026-08 = %d, want 900000", caixaAugustTotal)
	}
	if len(previsao.Caixa) == 0 || previsao.Caixa[0].Vencimento != "2026-08" {
		t.Fatalf("primeira linha previsao = %#v, want 2026-08", previsao.Caixa)
	}
	firstIncome, firstExpense, err := caixaRowIncomeExpense(previsao.Caixa[0])
	if err != nil {
		t.Fatal(err)
	}
	if firstIncome != previsao.ProjectedIncomeTotal.rawCents() {
		t.Fatalf("card entradas = %d, want primeira linha receitas %d", previsao.ProjectedIncomeTotal.rawCents(), firstIncome)
	}
	if firstExpense != previsao.ProjectedExpenseTotal.rawCents() {
		t.Fatalf("card saidas = %d, want primeira linha saidas %d", previsao.ProjectedExpenseTotal.rawCents(), firstExpense)
	}
	if previsao.Caixa[0].RawResultado != previsao.CashMonthResultTotal.rawCents() {
		t.Fatalf("card resultado = %d, want primeira linha resultado %d", previsao.CashMonthResultTotal.rawCents(), previsao.Caixa[0].RawResultado)
	}
	var foundCardCategory bool
	for _, c := range previsao.CategoriasCaixa {
		if c.Nome == "Compras Cartao" {
			foundCardCategory = true
			assertMoneyDisplay(t, "previsao categoria cartao", c.Valor, "3.000", ",00")
		}
	}
	if !foundCardCategory {
		t.Fatalf("previsao deve abrir compra de cartao por categoria: %#v", previsao.CategoriasCaixa)
	}
}

func TestCaixaPrevisaoRateioFaturaFechaCentavoResidual(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-07-05")
	invoiceDue := testUnixDate("2026-08-25")
	paymentDate := testUnixDate("2026-07-20")

	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
			('cat-card-serv-caixa', 'ws-caixa', 'Servicos Cartao', 'wrench', '#0ea5e9', 'EXPENSE', 'Operacional', ?),
			('cat-card-mkt-caixa', 'ws-caixa', 'Marketing Cartao', 'megaphone', '#a855f7', 'EXPENSE', 'Marketing', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-centavo', 'card-caixa', '2026-08-centavo', ?, ?, 'OPEN', ?)
	`, purchaseDate, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('centavo-card-a', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'inv-centavo', 'EXPENSE', 33, ?, 'Item A', 'paid', 1, 1, ?, ?),
			('centavo-card-b', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-serv-caixa', 'inv-centavo', 'EXPENSE', 33, ?, 'Item B', 'paid', 1, 1, ?, ?),
			('centavo-card-c', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-mkt-caixa', 'inv-centavo', 'EXPENSE', 34, ?, 'Item C', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now, purchaseDate, now, now, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('centavo-pay', 'ws-caixa', 'u-caixa', 'chk-caixa', 'EXPENSE', 99, ?, ?, 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, invoicePaymentDescription("Cartao Visa"), paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('centavo-ip', 'ws-caixa', 'inv-centavo', 'chk-caixa', 'centavo-pay', 99, ?, 'manual', 'u-caixa', ?)
	`, paymentDate, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}

	assertMoneyDisplay(t, "saidas previstas centavo residual", data.ProjectedExpenseTotal, "0", ",01")
	var categoryTotal int64
	for _, c := range data.CategoriasCaixa {
		if c.IsGroup {
			continue
		}
		categoryTotal += c.Valor.rawCents()
	}
	if categoryTotal != 1 {
		t.Fatalf("soma categorias = %d, want 1 centavo; categorias=%#v", categoryTotal, data.CategoriasCaixa)
	}
	var macroTotal int64
	for _, row := range data.BusinessCostsCaixa {
		macroTotal += row.RawAmount
	}
	if macroTotal != 1 {
		t.Fatalf("soma macros = %d, want 1 centavo; macros=%#v", macroTotal, data.BusinessCostsCaixa)
	}
	var caixaAugustTotal int64
	for _, row := range data.Caixa {
		if row.Vencimento == "2026-08" {
			caixaAugustTotal = row.RawResultado
			break
		}
	}
	if caixaAugustTotal != -1 {
		t.Fatalf("Caixa 2026-08 = %d, want -1", caixaAugustTotal)
	}
}

func TestCaixaRealizadoDemonstrativoUsaAnoSelecionadoInteiro(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if len(data.Caixa) != 12 {
		t.Fatalf("linhas do demonstrativo realizado = %d, want 12 meses", len(data.Caixa))
	}
	if data.Caixa[0].Vencimento != "2026-01" || data.Caixa[11].Vencimento != "2026-12" {
		t.Fatalf("janela realizado = %s..%s, want 2026-01..2026-12", data.Caixa[0].Vencimento, data.Caixa[11].Vencimento)
	}
}

func TestCaixaPrevisaoDemonstrativoUsaMesSelecionadoMaisDozeMeses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if len(data.Caixa) != 13 {
		t.Fatalf("linhas do demonstrativo previsao = %d, want 13 meses", len(data.Caixa))
	}
	if data.Caixa[0].Vencimento != "2026-08" || data.Caixa[12].Vencimento != "2027-08" {
		t.Fatalf("janela previsao = %s..%s, want 2026-08..2027-08", data.Caixa[0].Vencimento, data.Caixa[12].Vencimento)
	}
}

func TestCaixaPrevisaoDemonstrativoCruzaAnoNoMesDezembro(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 12, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if len(data.Caixa) != 13 {
		t.Fatalf("linhas do demonstrativo previsao dezembro = %d, want 13 meses", len(data.Caixa))
	}
	if data.Caixa[0].Vencimento != "2026-12" || data.Caixa[12].Vencimento != "2027-12" {
		t.Fatalf("janela previsao dezembro = %s..%s, want 2026-12..2027-12", data.Caixa[0].Vencimento, data.Caixa[12].Vencimento)
	}
}

func TestCaixaPercentualPequenoNaoRenderizaZero(t *testing.T) {
	got := percentLabel(1, 20000)
	if got != "<0,01%" {
		t.Fatalf("percentLabel pequeno = %q, want <0,01%%", got)
	}
	got = percentLabel(32, 10000)
	if got != "0,32%" {
		t.Fatalf("percentLabel menor que 1 = %q, want 0,32%%", got)
	}
	got = percentLabel(296, 10000)
	if got != "2,96%" {
		t.Fatalf("percentLabel 2,96 = %q, want 2,96%%", got)
	}
}

func TestCaixaPrevisaoTransferenciaNaoAlteraResultado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	transferDate := testUnixDate("2026-08-10")
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('transfer-prev', 'ws-caixa', 'u-caixa', 'chk-caixa', 'sav-caixa', 'TRANSFER', 50000, ?, ?, 'Transferencia previsao', 'pending', NULL, 1, 1, ?, ?)
	`, transferDate, transferDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	assertMoneyDisplay(t, "resultado previsto com transferencia", data.CashMonthResultTotal, "0", ",00")
}

func TestProjectedEndingBalanceUsaSaldoAtualEPendenciasFuturasSemDuplicarFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	nowUnix := time.Now().Unix()
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC).Unix()
	dueIncome := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	dueExpense := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC).Unix()
	beforeToday := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Unix()
	afterPeriod := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `
		UPDATE accounts
		SET current_balance = CASE id
			WHEN 'chk-caixa' THEN 100000
			WHEN 'sav-caixa' THEN 50000
			WHEN 'card-caixa' THEN 999999
			ELSE current_balance
		END
		WHERE workspace_id = 'ws-caixa'
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('proj-inc', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-salary-caixa', 'INCOME', 20000, ?, ?, 'Receita futura', 'pending', NULL, 1, 1, ?, ?),
			('proj-exp', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 30000, ?, ?, 'Despesa futura', 'pending', NULL, 1, 1, ?, ?),
			('proj-paid-exp', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 40000, ?, ?, 'Despesa ja paga', 'paid', ?, 1, 1, ?, ?),
			('proj-transfer', 'ws-caixa', 'u-caixa', 'chk-caixa', NULL, 'TRANSFER', 60000, ?, ?, 'Transferencia futura', 'pending', NULL, 1, 1, ?, ?),
			('proj-before', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 70000, ?, ?, 'Antes de hoje', 'pending', NULL, 1, 1, ?, ?),
			('proj-after', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 90000, ?, ?, 'Depois do periodo', 'pending', NULL, 1, 1, ?, ?),
			('proj-invoice-payment-pending', 'ws-caixa', 'u-caixa', 'chk-caixa', NULL, 'EXPENSE', 65000, ?, ?, ?, 'pending', NULL, 1, 1, ?, ?)
	`, dueIncome, dueIncome, nowUnix, nowUnix,
		dueExpense, dueExpense, nowUnix, nowUnix,
		dueExpense, dueExpense, dueExpense, nowUnix, nowUnix,
		dueExpense, dueExpense, nowUnix, nowUnix,
		beforeToday, beforeToday, nowUnix, nowUnix,
		afterPeriod, afterPeriod, nowUnix, nowUnix,
		dueExpense, dueExpense, invoicePaymentDescription("Cartao Visa"), nowUnix, nowUnix)

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES
			('proj-inv-open', 'card-caixa', '2026-08', ?, ?, 'OPEN', ?),
			('proj-inv-closed', 'card-caixa', '2026-08-extra', ?, ?, 'CLOSED', ?)
	`, dueExpense, dueExpense, nowUnix, dueExpense, dueExpense, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('proj-card-open', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'proj-inv-open', 'EXPENSE', 80000, ?, 'Compra cartao aberta', 'paid', 1, 1, ?, ?),
			('proj-card-closed', 'ws-caixa', 'u-caixa', 'card-caixa', 'cat-card-caixa', 'proj-inv-closed', 'EXPENSE', 10000, ?, 'Compra cartao fechada', 'paid', 1, 1, ?, ?)
	`, dueExpense, nowUnix, nowUnix, dueExpense, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('proj-pay-open', 'ws-caixa', 'u-caixa', 'chk-caixa', 'EXPENSE', 25000, ?, ?, 'paid', ?, 1, 1, ?, ?)
	`, dueExpense, invoicePaymentDescription("Cartao Visa"), dueExpense, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('proj-ip-open', 'ws-caixa', 'proj-inv-open', 'chk-caixa', 'proj-pay-open', 25000, ?, 'manual', 'u-caixa', ?)
	`, dueExpense, nowUnix)

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('u-proj-other', 'Other', 'proj-other@t.com', 'hash', ?, ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('ws-proj-other', 'Other', 'personal', ?, ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-proj-other', 'u-proj-other', 'ADMIN', ?)`, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('chk-proj-other', 'ws-proj-other', 'Conta Other', 'CHECKING', 0, 999999, ?, ?),
			('card-proj-other', 'ws-proj-other', 'Card Other', 'CREDIT_CARD', 0, 0, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('proj-other-income', 'ws-proj-other', 'u-proj-other', 'chk-proj-other', 'INCOME', 999999, ?, ?, 'Outro workspace', 'pending', 1, 1, ?, ?)
	`, dueExpense, dueExpense, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('proj-inv-other', 'card-proj-other', '2026-08', ?, ?, 'OPEN', ?)
	`, dueExpense, dueExpense, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('proj-card-other', 'ws-proj-other', 'u-proj-other', 'card-proj-other', 'proj-inv-other', 'EXPENSE', 999999, ?, 'Compra outro workspace', 'paid', 1, 1, ?, ?)
	`, dueExpense, nowUnix, nowUnix)

	got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, "ws-caixa", periodEnd, now)
	if err != nil {
		t.Fatalf("BuildProjectedBalanceUntilPeriodEnd: %v", err)
	}
	if !show {
		t.Fatal("saldo projetado deveria ser exibido para periodo futuro/atual")
	}
	if got != 75000 {
		t.Fatalf("saldo projetado = %d, want 75000", got)
	}
}

func TestProjectedEndingBalanceMesFuturoAcumulaDeHojeAteFimDoMesFuturo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	nowUnix := time.Now().Unix()
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	augDue := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC).Unix()
	sepDue := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC).Unix()
	octDue := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).Unix()
	periodEnd := time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC).Unix()

	execTestSQL(t, db, `UPDATE accounts SET current_balance = CASE WHEN id = 'chk-caixa' THEN 100000 WHEN id = 'sav-caixa' THEN 0 ELSE current_balance END WHERE workspace_id = 'ws-caixa'`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('proj-future-aug', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-salary-caixa', 'INCOME', 10000, ?, ?, 'Receita agosto', 'pending', 1, 1, ?, ?),
			('proj-future-sep', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 15000, ?, ?, 'Despesa setembro', 'pending', 1, 1, ?, ?),
			('proj-future-oct', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 90000, ?, ?, 'Despesa outubro', 'pending', 1, 1, ?, ?)
	`, augDue, augDue, nowUnix, nowUnix,
		sepDue, sepDue, nowUnix, nowUnix,
		octDue, octDue, nowUnix, nowUnix)

	got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, "ws-caixa", periodEnd, now)
	if err != nil {
		t.Fatalf("BuildProjectedBalanceUntilPeriodEnd: %v", err)
	}
	if !show {
		t.Fatal("saldo projetado deveria ser exibido para mes futuro")
	}
	if got != 95000 {
		t.Fatalf("saldo projetado futuro = %d, want 95000", got)
	}
}

func TestProjectedEndingBalanceMesPassadoNaoExibeHistoricoInventado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC).Unix()

	got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, "ws-caixa", periodEnd, now)
	if err != nil {
		t.Fatalf("BuildProjectedBalanceUntilPeriodEnd: %v", err)
	}
	if show {
		t.Fatal("saldo projetado nao deve ser exibido para mes passado")
	}
	if got != 0 {
		t.Fatalf("saldo projetado oculto = %d, want 0", got)
	}
}

func TestCaixaPrevisaoViewModelSaldoProjetadoFuturoEPassado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	nowUnix := time.Now().Unix()
	future := time.Now().UTC().AddDate(0, 1, 0)
	futureDue := time.Date(future.Year(), future.Month(), 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `UPDATE accounts SET current_balance = CASE WHEN id = 'chk-caixa' THEN 100000 WHEN id = 'sav-caixa' THEN 0 ELSE current_balance END WHERE workspace_id = 'ws-caixa'`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('proj-view-income', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-salary-caixa', 'INCOME', 25000, ?, ?, 'Receita viewmodel', 'pending', 1, 1, ?, ?)
	`, futureDue, futureDue, nowUnix, nowUnix)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	futureData, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", int(future.Month()), future.Year())
	if err != nil {
		t.Fatalf("build future previsao: %v", err)
	}
	if !futureData.ShowProjectedEndingBalance {
		t.Fatal("viewmodel deveria exibir saldo projetado para mes futuro")
	}
	assertMoneyDisplay(t, "saldo projetado futuro", futureData.ProjectedEndingBalance, "1.250", ",00")
	if len(futureData.Caixa) == 0 || !futureData.Caixa[0].HasProjectedEndingBalance {
		t.Fatalf("primeira linha futura deveria exibir saldo projetado: %#v", futureData.Caixa)
	}
	if futureData.Caixa[0].RawProjectedBalance != futureData.ProjectedEndingBalance.rawCents() {
		t.Fatalf("card saldo projetado = %d, want primeira linha %d", futureData.ProjectedEndingBalance.rawCents(), futureData.Caixa[0].RawProjectedBalance)
	}

	pastRef := time.Now().UTC()
	past := time.Date(pastRef.Year(), pastRef.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	pastData, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", int(past.Month()), past.Year())
	if err != nil {
		t.Fatalf("build past previsao: %v", err)
	}
	if pastData.ShowProjectedEndingBalance {
		t.Fatal("viewmodel nao deve exibir saldo projetado para mes passado")
	}
	if len(pastData.Caixa) == 0 || pastData.Caixa[0].HasProjectedEndingBalance {
		t.Fatalf("primeira linha passada nao deve inventar saldo historico: %#v", pastData.Caixa)
	}
}

func TestCaixaPrevisaoSaldoProjetadoPorLinhaAcumulaMesAMes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	nowUnix := time.Now().Unix()
	current := time.Now().UTC()
	selected := current.AddDate(0, 1, 0)
	next := selected.AddDate(0, 1, 0)
	selectedDue := time.Date(selected.Year(), selected.Month(), 15, 12, 0, 0, 0, time.UTC).Unix()
	nextDue := time.Date(next.Year(), next.Month(), 15, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `UPDATE accounts SET current_balance = CASE WHEN id = 'chk-caixa' THEN 100000 WHEN id = 'sav-caixa' THEN 0 ELSE current_balance END WHERE workspace_id = 'ws-caixa'`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('proj-row-inc', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-salary-caixa', 'INCOME', 20000, ?, ?, 'Receita linha', 'pending', 1, 1, ?, ?),
			('proj-row-exp', 'ws-caixa', 'u-caixa', 'chk-caixa', 'cat-food-caixa', 'EXPENSE', 15000, ?, ?, 'Despesa linha', 'pending', 1, 1, ?, ?)
	`, selectedDue, selectedDue, nowUnix, nowUnix, nextDue, nextDue, nowUnix, nowUnix)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", int(selected.Month()), selected.Year())
	if err != nil {
		t.Fatalf("build previsao: %v", err)
	}
	if len(data.Caixa) < 2 {
		t.Fatalf("linhas previsao insuficientes: %#v", data.Caixa)
	}
	if data.Caixa[0].RawProjectedBalance != 120000 {
		t.Fatalf("saldo projetado primeira linha = %d, want 120000", data.Caixa[0].RawProjectedBalance)
	}
	if data.Caixa[1].RawProjectedBalance != 105000 {
		t.Fatalf("saldo projetado segunda linha = %d, want 105000", data.Caixa[1].RawProjectedBalance)
	}
}

func TestCaixaSubmodosDisponiveisBusinessEPersonal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	handlerBusiness := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	for _, modo := range []string{"realizado", "previsao"} {
		data, err := handlerBusiness.buildRelatoriosDataWithModo("", "caixa", "movimentos", modo, 8, 2026)
		if err != nil {
			t.Fatalf("business modo %s: %v", modo, err)
		}
		if !data.IsBusiness || data.CaixaModo != modo {
			t.Fatalf("business modo %s: IsBusiness=%v CaixaModo=%q", modo, data.IsBusiness, data.CaixaModo)
		}
	}

	execTestSQL(t, db, `UPDATE workspaces SET type = 'personal' WHERE id = 'ws-caixa'`)
	handlerPersonal := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	for _, modo := range []string{"realizado", "previsao"} {
		data, err := handlerPersonal.buildRelatoriosDataWithModo("", "caixa", "movimentos", modo, 8, 2026)
		if err != nil {
			t.Fatalf("personal modo %s: %v", modo, err)
		}
		if data.IsBusiness || data.CaixaModo != modo {
			t.Fatalf("personal modo %s: IsBusiness=%v CaixaModo=%q", modo, data.IsBusiness, data.CaixaModo)
		}
	}
}

func TestRelatoriosPersonalCompetenciaFallbackParaCaixa(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCaixaRealizadoScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'personal' WHERE id = 'ws-caixa'`)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
	data, err := handler.buildRelatoriosDataWithModo("", "competencia", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if data.IsBusiness {
		t.Fatal("workspace deveria ser Personal")
	}
	if data.ActiveTab != "caixa" {
		t.Fatalf("ActiveTab = %q, want caixa", data.ActiveTab)
	}
	if data.CaixaModo != "previsao" {
		t.Fatalf("CaixaModo = %q, want previsao", data.CaixaModo)
	}
	for _, q := range []string{data.MonthSelectorPrevQuery, data.MonthSelectorNextQuery, data.MonthSelectorCurrentQuery} {
		if !strings.Contains(q, "tab=caixa") || !strings.Contains(q, "modo=previsao") {
			t.Fatalf("query %q deveria preservar fallback tab=caixa e modo=previsao", q)
		}
		if strings.Contains(q, "tab=competencia") {
			t.Fatalf("query %q nao deveria manter tab=competencia no Personal", q)
		}
	}
}
