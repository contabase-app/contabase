package handlers

import (
	"database/sql"
	"testing"
	"time"
)

// Helpers compartilhados pelos testes de busca de lançamentos por contato.

func insertTestContact(t *testing.T, db *sql.DB, id, workspaceID, customClientID, name, document, email, phone string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, custom_client_id, name, document, type, email, phone, created_at)
		VALUES (?, ?, ?, ?, ?, 'client', ?, ?, ?)
	`, id, workspaceID, customClientID, name, document, email, phone, now)
}

func insertTestCheckingTx(t *testing.T, db *sql.DB, id, accountID string, contactID, categoryID interface{}, txType string, amount int64, date int64, description, notes, status string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, contact_id, type, amount, date, description, notes, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
	`, id, accountID, categoryID, contactID, txType, amount, date, description, notes, status, now, now)
}

func aug2026(day int) int64 {
	return time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC).Unix()
}

func findTxByID(rows []TransactionRow, id string) (TransactionRow, bool) {
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return TransactionRow{}, false
}

func newLancamentosTestHandler(db *sql.DB) TransactionHandler {
	return TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
}

// TestLancamentosBuscaContato cobre os cenários principais de busca por dados do
// lançamento e do contato vinculado, sempre com situacao=pago para resultados
// determinísticos independente do relógio.
func TestLancamentosBuscaContato(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-042", "Padaria Estrela", "12.345.678/0001-99", "contato@padaria.com", "(11) 98888-7777")
	insertTestCheckingTx(t, db, "tx-contato", "checking-test", "ct-main", nil, "EXPENSE", 1500, aug2026(15), "Compra mensal loja", "observacao especial pedido", "paid")
	insertTestCheckingTx(t, db, "tx-sem-contato", "checking-test", nil, nil, "EXPENSE", 2000, aug2026(16), "Aluguel da sala comercial", "nota interna unica", "paid")

	handler := newLancamentosTestHandler(db)

	cases := []struct {
		name     string
		busca    string
		wantTx   string
		absentTx string
	}{
		{"descricao", "Aluguel da sala", "tx-sem-contato", "tx-contato"},
		{"notas", "nota interna unica", "tx-sem-contato", "tx-contato"},
		{"nome contato", "Padaria Estrela", "tx-contato", "tx-sem-contato"},
		{"nome contato case-insensitive", "padaria estrela", "tx-contato", "tx-sem-contato"},
		{"cnpj com mascara", "12.345.678/0001-99", "tx-contato", "tx-sem-contato"},
		{"cnpj sem mascara", "12345678000199", "tx-contato", "tx-sem-contato"},
		{"telefone com mascara", "(11) 98888-7777", "tx-contato", "tx-sem-contato"},
		{"telefone sem mascara", "11988887777", "tx-contato", "tx-sem-contato"},
		{"custom client id", "CLI-042", "tx-contato", "tx-sem-contato"},
		{"email", "contato@padaria.com", "tx-contato", "tx-sem-contato"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filters := LancamentosFilters{Situacoes: []string{"pago"}, Busca: tc.busca}
			data, err := handler.buildLancamentosData("", 8, 2026, filters)
			if err != nil {
				t.Fatalf("buildLancamentosData: %v", err)
			}
			if _, ok := findTxByID(data.Transactions, tc.wantTx); !ok {
				t.Errorf("busca %q: esperava encontrar %s", tc.busca, tc.wantTx)
			}
			if _, ok := findTxByID(data.Transactions, tc.absentTx); ok {
				t.Errorf("busca %q: nao deveria encontrar %s", tc.busca, tc.absentTx)
			}
		})
	}
}

// TestLancamentosBuscaPreservaPeriodo garante que a busca por contato não
// ignora o filtro de mês/ano.
func TestLancamentosBuscaPreservaPeriodo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-100", "Cliente Mensal", "", "", "")
	insertTestCheckingTx(t, db, "tx-agosto", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(10), "Servico agosto", "", "paid")
	insertTestCheckingTx(t, db, "tx-julho", "checking-test", "ct-main", nil, "EXPENSE", 1000, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix(), "Servico julho", "", "paid")

	handler := newLancamentosTestHandler(db)

	filters := LancamentosFilters{Situacoes: []string{"pago"}, Busca: "Cliente Mensal"}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if _, ok := findTxByID(data.Transactions, "tx-agosto"); !ok {
		t.Errorf("esperava tx-agosto dentro do periodo de agosto")
	}
	if _, ok := findTxByID(data.Transactions, "tx-julho"); ok {
		t.Errorf("tx-julho nao deveria aparecer ao filtrar agosto")
	}
}

// TestLancamentosBuscaPreservaSituacaoPago garante que busca + situacao=pago não
// retorna lançamentos pendentes/vencidos.
func TestLancamentosBuscaPreservaSituacaoPago(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-200", "Fornecedor X", "", "", "")
	insertTestCheckingTx(t, db, "tx-pago", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(5), "Pedido Fornecedor X pago", "", "paid")
	insertTestCheckingTx(t, db, "tx-pendente", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(6), "Pedido Fornecedor X pendente", "", "pending")

	handler := newLancamentosTestHandler(db)

	filters := LancamentosFilters{Situacoes: []string{"pago"}, Busca: "Fornecedor X"}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if _, ok := findTxByID(data.Transactions, "tx-pago"); !ok {
		t.Errorf("esperava tx-pago com filtro situacao=pago")
	}
	if _, ok := findTxByID(data.Transactions, "tx-pendente"); ok {
		t.Errorf("tx-pendente nao deveria aparecer com filtro situacao=pago")
	}
}

// TestLancamentosBuscaPreservaConta garante que busca + conta preserva o filtro
// de conta.
func TestLancamentosBuscaPreservaConta(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-2', 'ws-test', 'Conta Dois', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-300", "Cliente Conta", "", "", "")
	insertTestCheckingTx(t, db, "tx-conta-1", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(5), "Servico Cliente Conta", "", "paid")
	insertTestCheckingTx(t, db, "tx-conta-2", "checking-2", "ct-main", nil, "EXPENSE", 1000, aug2026(6), "Servico Cliente Conta", "", "paid")

	handler := newLancamentosTestHandler(db)

	filters := LancamentosFilters{Situacoes: []string{"pago"}, Busca: "Cliente Conta"}
	data, err := handler.buildLancamentosData("checking-test", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if _, ok := findTxByID(data.Transactions, "tx-conta-1"); !ok {
		t.Errorf("esperava tx-conta-1 na conta filtrada")
	}
	if _, ok := findTxByID(data.Transactions, "tx-conta-2"); ok {
		t.Errorf("tx-conta-2 (outra conta) nao deveria aparecer")
	}
}

// TestLancamentosBuscaPreservaCategoria garante que busca + categoria preserva o
// filtro de categoria.
func TestLancamentosBuscaPreservaCategoria(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cat-a', 'ws-test', 'Cat A', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('cat-b', 'ws-test', 'Cat B', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)
	`, now, now)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-400", "Cliente Categoria", "", "", "")
	insertTestCheckingTx(t, db, "tx-cat-a", "checking-test", "ct-main", "cat-a", "EXPENSE", 1000, aug2026(5), "Despesa Cliente Categoria", "", "paid")
	insertTestCheckingTx(t, db, "tx-cat-b", "checking-test", "ct-main", "cat-b", "EXPENSE", 1000, aug2026(6), "Despesa Cliente Categoria", "", "paid")

	handler := newLancamentosTestHandler(db)

	filters := LancamentosFilters{Situacoes: []string{"pago"}, Categorias: []string{"cat-a"}, Busca: "Cliente Categoria"}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if _, ok := findTxByID(data.Transactions, "tx-cat-a"); !ok {
		t.Errorf("esperava tx-cat-a com filtro categoria=cat-a")
	}
	if _, ok := findTxByID(data.Transactions, "tx-cat-b"); ok {
		t.Errorf("tx-cat-b (cat-b) nao deveria aparecer")
	}
}

// TestLancamentosBuscaPreservaTipo garante que busca + tipo preserva o filtro de
// tipo.
func TestLancamentosBuscaPreservaTipo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-500", "Cliente Tipo", "", "", "")
	insertTestCheckingTx(t, db, "tx-receita", "checking-test", "ct-main", nil, "INCOME", 1000, aug2026(5), "Movimento Cliente Tipo", "", "paid")
	insertTestCheckingTx(t, db, "tx-despesa", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(6), "Movimento Cliente Tipo", "", "paid")

	handler := newLancamentosTestHandler(db)

	filters := LancamentosFilters{Situacoes: []string{"pago"}, Tipos: []string{"receita"}, Busca: "Cliente Tipo"}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	if _, ok := findTxByID(data.Transactions, "tx-receita"); !ok {
		t.Errorf("esperava tx-receita com filtro tipo=receita")
	}
	if _, ok := findTxByID(data.Transactions, "tx-despesa"); ok {
		t.Errorf("tx-despesa nao deveria aparecer com filtro tipo=receita")
	}
}

// TestLancamentosBuscaContatoIsolamentoWorkspace garante que o mesmo documento em
// outro workspace não vaza resultado.
func TestLancamentosBuscaContatoIsolamentoWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-other', 'Other', 'other@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-other', 'Workspace Other', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-other', 'user-other', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('other-check', 'ws-other', 'Other Checking', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	insertTestContact(t, db, "ct-other", "ws-other", "CLI-999", "Outro Cliente", "12.345.678/0001-99", "outro@cliente.com", "(11) 98888-7777")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, contact_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-other', 'ws-other', 'user-other', 'other-check', 'ct-other', 'EXPENSE', 1000, ?, 'Despesa Outro Cliente', 'paid', 1, 1, ?, ?)
	`, aug2026(10), now, now)

	handler := newLancamentosTestHandler(db)

	for _, busca := range []string{"12.345.678/0001-99", "12345678000199", "Outro Cliente", "CLI-999", "outro@cliente.com"} {
		filters := LancamentosFilters{Situacoes: []string{"pago"}, Busca: busca}
		data, err := handler.buildLancamentosData("", 8, 2026, filters)
		if err != nil {
			t.Fatalf("buildLancamentosData (%q): %v", busca, err)
		}
		if _, ok := findTxByID(data.Transactions, "tx-other"); ok {
			t.Errorf("busca %q vazou lançamento de outro workspace", busca)
		}
	}
}

// TestLancamentosBuscaPreservaOrdenacao garante que a ordenação cronológica
// crescente continua valendo com busca ativa.
func TestLancamentosBuscaPreservaOrdenacao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	insertTestContact(t, db, "ct-main", "ws-test", "CLI-600", "Cliente Ordem", "", "", "")
	insertTestCheckingTx(t, db, "tx-dia20", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(20), "Compra Cliente Ordem", "", "paid")
	insertTestCheckingTx(t, db, "tx-dia05", "checking-test", "ct-main", nil, "EXPENSE", 1000, aug2026(5), "Compra Cliente Ordem", "", "paid")

	handler := newLancamentosTestHandler(db)

	filters := LancamentosFilters{Situacoes: []string{"pago"}, Busca: "Cliente Ordem"}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	var matched []TransactionRow
	for _, tx := range data.Transactions {
		if tx.ID == "tx-dia05" || tx.ID == "tx-dia20" {
			matched = append(matched, tx)
		}
	}
	if len(matched) != 2 {
		t.Fatalf("esperava 2 lançamentos da busca, obteve %d", len(matched))
	}
	if matched[0].ID != "tx-dia05" || matched[1].ID != "tx-dia20" {
		t.Errorf("ordem crescente esperada [tx-dia05 tx-dia20], obteve [%s %s]", matched[0].ID, matched[1].ID)
	}
}
