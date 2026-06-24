package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestRelatoriosCompetenciaExcluiPagamentoFaturaMesmoMes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentCompetenceScenario(t, db, "2026-08-05", "2026-08-10")

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "250", ",00")
	assertMoneyDisplay(t, "saldo liquido", data.SaldoLiquido, "-250", ",00")
	if len(data.Categorias) != 1 {
		t.Fatalf("categorias = %#v, want only purchase category", data.Categorias)
	}
	if data.Categorias[0].Nome != "Compras Cartao" {
		t.Fatalf("categoria = %q, want Compras Cartao", data.Categorias[0].Nome)
	}
	assertMoneyDisplay(t, "categoria valor", data.Categorias[0].Valor, "250", ",00")

	assertDRETotal(t, handler, 2026, -25000)
	assertNoDREMacroGroup(t, handler, 2026, "Sem grupo macro")
}

func TestRelatoriosCompetenciaExcluiPagamentoFaturaCategorizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentCompetenceScenario(t, db, "2026-08-05", "2026-08-10")
	execTestSQL(t, db, `UPDATE transactions SET category_id = 'cat-card-purchase' WHERE id = 'payment-test'`)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "250", ",00")
	if len(data.Categorias) != 1 || data.Categorias[0].Nome != "Compras Cartao" {
		t.Fatalf("categorias = %#v, want only card purchase category", data.Categorias)
	}
	assertMoneyDisplay(t, "categoria valor", data.Categorias[0].Valor, "250", ",00")
	assertDRETotal(t, handler, 2026, -25000)
}

func TestRelatoriosCompetenciaExcluiPagamentoFaturaDescricaoAlterada(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentCompetenceScenario(t, db, "2026-08-05", "2026-08-10")
	execTestSQL(t, db, `
		UPDATE transactions
		SET description = 'Pagamento manual editado', category_id = 'cat-card-purchase'
		WHERE id = 'payment-test'
	`)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "250", ",00")
	if len(data.Categorias) != 1 || data.Categorias[0].Nome != "Compras Cartao" {
		t.Fatalf("categorias = %#v, want only card purchase category", data.Categorias)
	}
	assertMoneyDisplay(t, "categoria valor", data.Categorias[0].Valor, "250", ",00")
	assertDRETotal(t, handler, 2026, -25000)
}

func TestRelatoriosCompetenciaExcluiPagamentoFaturaMesDiferente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentCompetenceScenario(t, db, "2026-07-05", "2026-08-10")

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	july, err := handler.buildRelatoriosData("", 7, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData july: %v", err)
	}
	august, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData august: %v", err)
	}

	assertMoneyDisplay(t, "july total despesas", july.TotalDespesas, "250", ",00")
	assertMoneyDisplay(t, "august total despesas", august.TotalDespesas, "0", ",00")
	if len(august.Categorias) != 0 {
		t.Fatalf("august categorias = %#v, want no competence expenses", august.Categorias)
	}
	if len(august.Membros) != 0 {
		t.Fatalf("august membros = %#v, want no competence expenses", august.Membros)
	}

	raw, err := handler.queryDRERaw(2026)
	if err != nil {
		t.Fatalf("queryDRERaw: %v", err)
	}
	for _, row := range raw {
		if row.Competencia == "2026-08" {
			t.Fatalf("DRE has august payment row: %#v", row)
		}
	}
	assertDRETotal(t, handler, 2026, -25000)
}

func TestLancamentosPreservaPagamentoFaturaComoMovimentoDeCaixa(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentCompetenceScenario(t, db, "2026-07-05", "2026-08-10")

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	var foundPayment bool
	for _, row := range data.Transactions {
		if row.Description == invoicePaymentDescription("Cartao Teste") {
			foundPayment = true
			assertMoneyDisplay(t, "payment amount", MoneyMinor(row.Amount), "250", ",00")
		}
	}
	if !foundPayment {
		t.Fatalf("payment transaction not found in lancamentos: %#v", data.Transactions)
	}
	if len(data.Invoices) != 1 || data.Invoices[0].Reference != "2026-08" {
		t.Fatalf("invoices = %#v, want consolidated 2026-08 invoice", data.Invoices)
	}
	if data.ResumoSaidas != "R$ 250,00" {
		t.Fatalf("ResumoSaidas = %q, want R$ 250,00", data.ResumoSaidas)
	}
}

func TestRelatoriosNaoIncluiTransacoesDeletadas(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedRelatoriosDeletedTransactionsScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-del", UserID: "user-del"}
	data, err := handler.buildRelatoriosData("", 6, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total receitas", data.TotalReceitas, "0", ",00")
	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "0", ",00")
	assertMoneyDisplay(t, "saldo liquido", data.SaldoLiquido, "0", ",00")
	if len(data.Categorias) != 0 {
		t.Fatalf("categorias = %#v, want empty after deletes", data.Categorias)
	}
	assertMoneyDisplay(t, "donut essential", data.DonutEssentialTotal, "0", ",00")
	assertMoneyDisplay(t, "donut lifestyle", data.DonutLifestyleTotal, "0", ",00")

	raw, err := handler.queryDRERaw(2026)
	if err != nil {
		t.Fatalf("queryDRERaw: %v", err)
	}
	for _, row := range raw {
		if row.Competencia == "2026-06" {
			t.Fatalf("DRE has deleted june row: %#v", row)
		}
	}
}

func seedInvoicePaymentCompetenceScenario(t *testing.T, db *sql.DB, purchaseDate, paymentDate string) {
	t.Helper()

	now := time.Now().Unix()
	purchaseUnix := testUnixDate(purchaseDate)
	paymentUnix := testUnixDate(paymentDate)
	paymentTime := time.Unix(paymentUnix, 0).UTC()
	reference := paymentTime.Format("2006-01")
	closingUnix := time.Date(paymentTime.Year(), paymentTime.Month()-1, 20, 12, 0, 0, 0, time.UTC).Unix()
	dueUnix := time.Date(paymentTime.Year(), paymentTime.Month(), 10, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-test', 'User Test', 'user-test@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-test', 'Workspace Test', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-test', 'user-test', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('checking-test', 'ws-test', 'Conta Teste', 'CHECKING', 100000, 75000, ?, ?),
			('card-test', 'ws-test', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-test', 'card-test', 20, 10, 500000)
	`)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('cat-card-purchase', 'ws-test', 'Compras Cartao', 'shopping-bag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at)
		VALUES ('invoice-test', 'card-test', ?, ?, ?, 'PAID', ?, 25000, ?)
	`, reference, closingUnix, dueUnix, paymentUnix, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-test', 'ws-test', 'user-test', 'card-test', 'cat-card-purchase', 'invoice-test', 'EXPENSE', 25000, ?, 'Compra Cartao', 'paid', 1, 1, ?, ?)
	`, purchaseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('payment-test', 'ws-test', 'user-test', 'checking-test', NULL, NULL, 'EXPENSE', 25000, ?, ?, 'paid', 1, 1, ?, ?)
	`, paymentUnix, invoicePaymentDescription("Cartao Teste"), now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('payment-test-row', 'ws-test', 'invoice-test', 'checking-test', 'payment-test', 25000, ?, 'manual', 'user-test', ?)
	`, paymentUnix, now)
}

func seedRelatoriosDeletedTransactionsScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	june := testUnixDate("2026-06-10")
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-del', 'User Deleted', 'user-del@example.com', 'hash', ?, ?),
			('user-del-other', 'User Deleted Other', 'user-del-other@example.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-del', 'Workspace Deleted', '', 'personal', ?, ?),
			('ws-del-other', 'Workspace Deleted Other', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-del', 'user-del', 'ADMIN', ?),
			('ws-del-other', 'user-del-other', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('checking-del', 'ws-del', 'Conta Deleted', 'CHECKING', 0, 0, ?, ?),
			('card-del', 'ws-del', 'Cartao Deleted', 'CREDIT_CARD', 0, 0, ?, ?),
			('checking-del-other', 'ws-del-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-del', 'card-del', 20, 10, 500000)
	`)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cat-del', 'ws-del', 'Deleted Junho', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?),
			('cat-del-other', 'ws-del-other', 'Other Junho', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-del', 'card-del', '2026-06', ?, ?, 'OPEN', ?)
	`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('deleted-expense', 'ws-del', 'user-del', 'checking-del', 'cat-del', NULL, 'EXPENSE', 10000, ?, 'Despesa deletada', 'paid', 1, 1, ?, ?),
			('deleted-card-expense', 'ws-del', 'user-del', 'card-del', 'cat-del', 'invoice-del', 'EXPENSE', 20000, ?, 'Compra cartao deletada', 'paid', 1, 1, ?, ?),
			('foreign-expense', 'ws-del-other', 'user-del-other', 'checking-del-other', 'cat-del-other', NULL, 'EXPENSE', 90000, ?, 'Despesa outro workspace', 'paid', 1, 1, ?, ?)
	`, june, now, now, june, now, now, june, now, now)
	execTestSQL(t, db, `DELETE FROM transactions WHERE id IN ('deleted-expense', 'deleted-card-expense') AND workspace_id = 'ws-del'`)
}

func assertMoneyDisplay(t *testing.T, label string, got MoneyDisplay, reais, cents string) {
	t.Helper()
	if got.Reais != reais || got.Cents != cents {
		t.Fatalf("%s = %s%s, want %s%s", label, got.Reais, got.Cents, reais, cents)
	}
}

func assertDRETotal(t *testing.T, handler RelatoriosHandler, year int, want int64) {
	t.Helper()
	raw, err := handler.queryDRERaw(year)
	if err != nil {
		t.Fatalf("queryDRERaw: %v", err)
	}
	var total int64
	for _, row := range raw {
		total += row.Amount
	}
	if total != want {
		t.Fatalf("DRE total = %d, want %d; rows = %#v", total, want, raw)
	}
}

func assertNoDREMacroGroup(t *testing.T, handler RelatoriosHandler, year int, macroGroup string) {
	t.Helper()
	raw, err := handler.queryDRERaw(year)
	if err != nil {
		t.Fatalf("queryDRERaw: %v", err)
	}
	for _, row := range raw {
		if row.MacroGroup == macroGroup {
			t.Fatalf("unexpected DRE macro group %q in row %#v", macroGroup, row)
		}
	}
}
