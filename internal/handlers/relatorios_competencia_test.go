package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func seedCompetenciaScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-comp', 'User Comp', 'u-comp@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-comp', 'WS Comp', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-comp', 'u-comp', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-comp', 'ws-comp', 'Conta Corrente', 'CHECKING', 1000000, 1000000, ?, ?),
		('sav-comp', 'ws-comp', 'Poupanca', 'SAVINGS', 200000, 200000, ?, ?),
		('card-comp', 'ws-comp', 'Cartao Visa', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-comp', 'card-comp', 20, 10, 500000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-food-comp', 'ws-comp', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?),
		('cat-salary-comp', 'ws-comp', 'Salario', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-card-comp', 'ws-comp', 'Compras Cartao', 'shopping-bag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now, now, now)
}

func TestCompetenciaReceitaDateJulhoDueDateAgostoEntraEmJulho(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCompetenciaScenario(t, db)

	julhoDate := testUnixDate("2026-07-10")
	agostoDue := testUnixDate("2026-08-15")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('inc-comp', 'ws-comp', 'u-comp', 'chk-comp', 'cat-salary-comp', 'INCOME', 100000, ?, ?, 'Receita julho', 'pending', 1, 1, ?, ?)
	`, julhoDate, agostoDue, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-comp", UserID: "u-comp"}

	julhoData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 7, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData julho: %v", err)
	}
	if julhoData.TotalReceitas.Reais != "1.000" || julhoData.TotalReceitas.Cents != ",00" {
		t.Fatalf("Competencia julho receitas = R$ %s%s, want R$ 1.000,00", julhoData.TotalReceitas.Reais, julhoData.TotalReceitas.Cents)
	}

	agostoData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData agosto: %v", err)
	}
	if agostoData.TotalReceitas.Reais != "0" || agostoData.TotalReceitas.Cents != ",00" {
		t.Fatalf("Competencia agosto receitas = R$ %s%s, want R$ 0,00", agostoData.TotalReceitas.Reais, agostoData.TotalReceitas.Cents)
	}
}

func TestCompetenciaDespesaDateJulhoDueDateAgostoEntraEmJulho(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCompetenciaScenario(t, db)

	julhoDate := testUnixDate("2026-07-10")
	agostoDue := testUnixDate("2026-08-15")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('exp-comp', 'ws-comp', 'u-comp', 'chk-comp', 'cat-food-comp', 'EXPENSE', 50000, ?, ?, 'Despesa julho', 'pending', 1, 1, ?, ?)
	`, julhoDate, agostoDue, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-comp", UserID: "u-comp"}

	julhoData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 7, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData julho: %v", err)
	}
	if julhoData.TotalDespesas.Reais != "500" || julhoData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Competencia julho despesas = R$ %s%s, want R$ 500,00", julhoData.TotalDespesas.Reais, julhoData.TotalDespesas.Cents)
	}

	agostoData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData agosto: %v", err)
	}
	if agostoData.TotalDespesas.Reais != "0" || agostoData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Competencia agosto despesas = R$ %s%s, want R$ 0,00", agostoData.TotalDespesas.Reais, agostoData.TotalDespesas.Cents)
	}
}

func TestCompetenciaCompraCartaoEntraPorTransactionDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCompetenciaScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-comp', 'card-comp', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, time.Now().Unix())
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-comp-tx', 'ws-comp', 'u-comp', 'card-comp', 'cat-card-comp', 'inv-comp', 'EXPENSE', 30000, ?, 'Compra cartao', 'paid', 1, 1, ?, ?)
	`, purchaseDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-comp", UserID: "u-comp"}

	agostoData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData agosto: %v", err)
	}
	if agostoData.TotalDespesas.Reais != "300" || agostoData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Competencia agosto despesas = R$ %s%s, want R$ 300,00", agostoData.TotalDespesas.Reais, agostoData.TotalDespesas.Cents)
	}

	setembroData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 9, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData setembro: %v", err)
	}
	if setembroData.TotalDespesas.Reais != "0" || setembroData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Competencia setembro despesas = R$ %s%s, want R$ 0,00", setembroData.TotalDespesas.Reais, setembroData.TotalDespesas.Cents)
	}
}

func TestCompetenciaPagamentoFaturaNaoEntra(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCompetenciaScenario(t, db)

	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-pay-comp', 'card-comp', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-pay-tx', 'ws-comp', 'u-comp', 'card-comp', 'cat-card-comp', 'inv-pay-comp', 'EXPENSE', 30000, ?, 'Compra cartao', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-comp-tx', 'ws-comp', 'u-comp', 'chk-comp', 'EXPENSE', 30000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('ip-comp', 'ws-comp', 'inv-pay-comp', 'chk-comp', 'pay-comp-tx', 30000, ?, 'manual', 'u-comp', ?)
	`, paymentDate, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-comp", UserID: "u-comp"}

	setembroData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 9, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData setembro: %v", err)
	}
	if setembroData.TotalDespesas.Reais != "0" || setembroData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Competencia setembro despesas = R$ %s%s, want R$ 0,00 (pagamento fatura nao entra)", setembroData.TotalDespesas.Reais, setembroData.TotalDespesas.Cents)
	}
}

func TestCompetenciaTransferenciaNaoEntra(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCompetenciaScenario(t, db)

	transferDate := testUnixDate("2026-08-10")
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('transfer-comp', 'ws-comp', 'u-comp', 'chk-comp', 'sav-comp', 'TRANSFER', 50000, ?, 'Transferencia interna', 'paid', ?, 1, 1, ?, ?)
	`, transferDate, transferDate, now, now)

	handler := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-comp", UserID: "u-comp"}

	agostoData, err := handler.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData agosto: %v", err)
	}
	if agostoData.TotalReceitas.Reais != "0" || agostoData.TotalReceitas.Cents != ",00" {
		t.Fatalf("Competencia agosto receitas = R$ %s%s, want R$ 0,00", agostoData.TotalReceitas.Reais, agostoData.TotalReceitas.Cents)
	}
	if agostoData.TotalDespesas.Reais != "0" || agostoData.TotalDespesas.Cents != ",00" {
		t.Fatalf("Competencia agosto despesas = R$ %s%s, want R$ 0,00", agostoData.TotalDespesas.Reais, agostoData.TotalDespesas.Cents)
	}
}
