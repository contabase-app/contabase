package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/repository"
)

func TestTenantIsolationReadModelsDoNotMixWorkspaces(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTenantIsolationScenario(t, db)

	now := time.Now().UTC()

	dashboard := BuildDashboardData(db, "user-a", "ws-a")
	assertIDs(t, "dashboard accounts", accountCardIDs(dashboard.Accounts), []string{"a-check"})
	assertIDs(t, "dashboard cards", creditCardIDs(dashboard.Cards), []string{"a-card"})
	if len(dashboard.Limits) != 1 || dashboard.Limits[0].CategoryName != "A Expense" {
		t.Fatalf("dashboard limits = %#v, want only A Expense", dashboard.Limits)
	}
	if len(dashboard.Boxes) != 1 || dashboard.Boxes[0].ID != "a-box" {
		t.Fatalf("dashboard boxes = %#v, want only a-box", dashboard.Boxes)
	}

	txHandler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	lancamentos, err := txHandler.buildLancamentosData("", int(now.Month()), now.Year(), LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}
	assertIDs(t, "lancamentos transactions", transactionRowIDs(lancamentos.Transactions), []string{"a-income", "a-expense"})
	assertIDs(t, "lancamentos invoices", invoiceRowIDs(lancamentos.Invoices), []string{"a-invoice"})
	assertIDs(t, "lancamentos filter accounts", formAccountIDs(lancamentos.FilterAccounts), []string{"a-card", "a-check"})
	assertIDs(t, "lancamentos filter categories", formCategoryIDs(lancamentos.FilterCategories), []string{"a-expense-cat", "a-income-cat"})

	contatosHandler := ContatosHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	contatos, err := contatosHandler.queryContatos("")
	if err != nil {
		t.Fatalf("queryContatos: %v", err)
	}
	assertIDs(t, "contacts", contatoRowIDs(contatos), []string{"a-contact"})

	metasHandler := MetasHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	metas, err := metasHandler.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}
	assertIDs(t, "metas limites", limiteCardIDs(metas.Limites), []string{"a-limit"})
	assertIDs(t, "metas boxes", caixinhaCardIDs(metas.Caixinhas), []string{"a-box"})

	relHandler := RelatoriosHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	relatorios, err := relHandler.buildRelatoriosData("", int(now.Month()), now.Year())
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}
	assertIDs(t, "relatorios categorias", categoriaBarIDs(relatorios.Categorias), []string{"a-expense-cat"})

	configHandler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a", ActorRole: "ADMIN"}
	repo := repository.NewConfigRepository(db)
	categorias, _, err := configHandler.queryCategorias(repo)
	if err != nil {
		t.Fatalf("queryCategorias: %v", err)
	}
	assertIDs(t, "config categorias", configCategoryIDs(categorias), []string{"a-expense-cat", "a-income-cat"})
	contas, err := configHandler.queryContas(repo)
	if err != nil {
		t.Fatalf("queryContas: %v", err)
	}
	assertIDs(t, "config contas", configAccountIDs(contas), []string{"a-check"})
	cartoes, err := configHandler.queryCartoes(repo)
	if err != nil {
		t.Fatalf("queryCartoes: %v", err)
	}
	assertIDs(t, "config cartoes", configCardIDs(cartoes), []string{"a-card"})
}

func TestTenantIsolationTransactionWritesRejectForeignWorkspaceReferences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTenantIsolationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	now := time.Now().UTC().Unix()

	if _, err := handler.insertTransaction("EXPENSE", 1000, "foreign account", "", "", now, "b-check", "", "a-expense-cat", 1, "paid", false, "", "", 0, false, nil, "", false); err == nil || !strings.Contains(err.Error(), "conta") {
		t.Fatalf("foreign account insert error = %v, want account authorization error", err)
	}
	if _, err := handler.insertTransaction("EXPENSE", 1000, "foreign category", "", "", now, "a-check", "", "b-expense-cat", 1, "paid", false, "", "", 0, false, nil, "", false); err == nil || !strings.Contains(err.Error(), "categoria") {
		t.Fatalf("foreign category insert error = %v, want category authorization error", err)
	}
	if _, err := handler.insertTransaction("EXPENSE", 1000, "foreign contact", "", "", now, "a-check", "", "a-expense-cat", 1, "paid", false, "", "b-contact", 0, false, nil, "", false); err == nil || !strings.Contains(err.Error(), "contato") {
		t.Fatalf("foreign contact insert error = %v, want contact authorization error", err)
	}
	if _, err := handler.insertTransaction("EXPENSE", 1000, "foreign invoice", "", "", now, "a-card", "", "a-expense-cat", 1, "paid", false, "", "", 0, false, nil, "b-invoice", false); err == nil || !strings.Contains(err.Error(), "fatura") {
		t.Fatalf("foreign invoice insert error = %v, want invoice authorization error", err)
	}

	assertTransactionDescriptionCount(t, db, "foreign account", 0)
	assertTransactionDescriptionCount(t, db, "foreign category", 0)
	assertTransactionDescriptionCount(t, db, "foreign contact", 0)
	assertTransactionDescriptionCount(t, db, "foreign invoice", 0)
}

func TestTenantIsolationForeignIDsCannotMutateOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTenantIsolationScenario(t, db)

	txHandler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-a", UserID: "user-a"}
	txReq := httptest.NewRequest(http.MethodDelete, "/transacoes/b-expense", nil)
	txRR := httptest.NewRecorder()
	txHandler.HandleDeletarTransacao(txRR, txReq, "b-expense")
	if txRR.Code != http.StatusNotFound {
		t.Fatalf("foreign transaction delete status = %d, want %d", txRR.Code, http.StatusNotFound)
	}
	assertRowExists(t, db, "transactions", "b-expense")

	faturaHandler := FaturasHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-a", UserID: "user-a"}
	form := url.Values{"invoice_id": {"b-invoice"}, "payment_account_id": {"a-check"}}
	faturaReq := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	faturaReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	faturaRR := httptest.NewRecorder()
	faturaHandler.HandlePagarFatura(faturaRR, faturaReq)
	if faturaRR.Code != http.StatusNotFound {
		t.Fatalf("foreign invoice payment status = %d, want %d", faturaRR.Code, http.StatusNotFound)
	}
	assertInvoiceStatus(t, db, "b-invoice", "OPEN")

	var foreignIPCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, "b-invoice").Scan(&foreignIPCount); err != nil {
		t.Fatalf("invoice_payments count query: %v", err)
	}
	if foreignIPCount != 0 {
		t.Fatalf("invoice_payments cross-workspace count = %d, want 0", foreignIPCount)
	}

	contatosHandler := ContatosHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	contactForm := url.Values{"name": {"Mutated"}, "type": {"client"}}
	contactReq := httptest.NewRequest(http.MethodPost, "/contatos/b-contact/salvar", strings.NewReader(contactForm.Encode()))
	contactReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	contactRR := httptest.NewRecorder()
	contatosHandler.HandleAtualizarContato(contactRR, contactReq, "b-contact")
	if contactRR.Code == http.StatusOK {
		t.Fatalf("foreign contact update unexpectedly succeeded")
	}
	assertContactName(t, db, "b-contact", "B Contact")

	configHandler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a", ActorRole: "ADMIN"}
	categoryForm := url.Values{"name": {"Mutated"}, "type": {"EXPENSE"}}
	categoryReq := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/b-expense-cat/salvar", strings.NewReader(categoryForm.Encode()))
	categoryReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	categoryRR := httptest.NewRecorder()
	configHandler.HandleCategoriasInlineSave(categoryRR, categoryReq, "b-expense-cat")
	if categoryRR.Code != http.StatusNotFound {
		t.Fatalf("foreign category update status = %d, want %d", categoryRR.Code, http.StatusNotFound)
	}
	assertCategoryName(t, db, "b-expense-cat", "B Expense")

	accountForm := url.Values{"name": {"Mutated"}, "type": {"CHECKING"}, "balance": {"1,00"}}
	accountReq := httptest.NewRequest(http.MethodPost, "/configuracoes/contas/b-check/salvar", strings.NewReader(accountForm.Encode()))
	accountReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	accountRR := httptest.NewRecorder()
	configHandler.HandleContasInlineSave(accountRR, accountReq, "b-check")
	if accountRR.Code != http.StatusNotFound {
		t.Fatalf("foreign account update status = %d, want %d", accountRR.Code, http.StatusNotFound)
	}
	assertAccountName(t, db, "b-check", "B Checking")

	cardForm := url.Values{"name": {"Mutated"}, "credit_limit": {"1,00"}, "closing_day": {"10"}, "due_day": {"20"}}
	cardReq := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes/b-card/salvar", strings.NewReader(cardForm.Encode()))
	cardReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cardRR := httptest.NewRecorder()
	configHandler.HandleCartoesInlineSave(cardRR, cardReq, "b-card")
	if cardRR.Code != http.StatusNotFound {
		t.Fatalf("foreign card update status = %d, want %d", cardRR.Code, http.StatusNotFound)
	}
	assertAccountName(t, db, "b-card", "B Card")

	metasHandler := MetasHandler{DB: db, Templates: testTenantMetasTemplates(t), WorkspaceID: "ws-a", UserID: "user-a"}
	aporteForm := url.Values{"box_id": {"b-box"}, "amount": {"10,00"}}
	aporteReq := httptest.NewRequest(http.MethodPost, "/metas/caixinha/aporte", strings.NewReader(aporteForm.Encode()))
	aporteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	aporteRR := httptest.NewRecorder()
	metasHandler.HandleAporteCaixinha(aporteRR, aporteReq)
	assertBoxLedgerCount(t, db, "b-box", 0)

	deleteBoxReq := httptest.NewRequest(http.MethodDelete, "/metas/caixinha/b-box", nil)
	deleteBoxRR := httptest.NewRecorder()
	metasHandler.HandleDeleteCaixinha(deleteBoxRR, deleteBoxReq)
	assertRowExists(t, db, "boxes", "b-box")

	deleteLimitReq := httptest.NewRequest(http.MethodDelete, "/metas/limite/b-limit", nil)
	deleteLimitRR := httptest.NewRecorder()
	metasHandler.HandleDeleteLimite(deleteLimitRR, deleteLimitReq)
	assertRowExists(t, db, "cost_limits", "b-limit")
}

func TestTenantIsolationForeignInvoiceAndConfigReadsReturnNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTenantIsolationScenario(t, db)

	if _, err := buildFaturaDataForInvoice(db, "ws-a", "b-invoice", "desc"); err == nil {
		t.Fatalf("expected foreign invoice lookup to fail")
	}
	if _, err := buildFaturaDataForInvoice(db, "ws-a", "a-invoice", "desc"); err != nil {
		t.Fatalf("expected own invoice lookup to pass: %v", err)
	}

	configHandler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a", ActorRole: "ADMIN"}
	for _, tc := range []struct {
		name string
		run  func(http.ResponseWriter)
	}{
		{"foreign category form", func(w http.ResponseWriter) {
			configHandler.HandleCategoriasInlineForm(w, httptest.NewRequest(http.MethodGet, "/configuracoes/categorias/b-expense-cat/formulario", nil), "b-expense-cat")
		}},
		{"foreign account form", func(w http.ResponseWriter) {
			configHandler.HandleContasInlineForm(w, httptest.NewRequest(http.MethodGet, "/configuracoes/contas/b-check/formulario", nil), "b-check")
		}},
		{"foreign card form", func(w http.ResponseWriter) {
			configHandler.HandleCartoesInlineForm(w, httptest.NewRequest(http.MethodGet, "/configuracoes/cartoes/b-card/formulario", nil), "b-card")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			tc.run(rr)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
			}
		})
	}
}

func seedTenantIsolationScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC()
	nowUnix := now.Unix()
	monthStart := time.Date(now.Year(), now.Month(), 5, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(now.Year(), now.Month(), 20, 12, 0, 0, 0, time.UTC).Unix()
	reference := now.Format("2006-01")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@example.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@example.com', 'hash', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-check', 'ws-a', 'A Checking', 'CHECKING', 50000, 50000, ?, ?),
			('a-card', 'ws-a', 'A Card', 'CREDIT_CARD', 0, 0, ?, ?),
			('b-check', 'ws-b', 'B Checking', 'CHECKING', 900000, 900000, ?, ?),
			('b-card', 'ws-b', 'B Card', 'CREDIT_CARD', 0, 0, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES
			('a-card-row', 'a-card', 10, 20, 100000),
			('b-card-row', 'b-card', 10, 20, 900000)
	`)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('a-expense-cat', 'ws-a', 'A Expense', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('a-income-cat', 'ws-a', 'A Income', 'tag', '#6b7280', 'INCOME', 'INCOME', ?),
			('b-expense-cat', 'ws-b', 'B Expense', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('b-income-cat', 'ws-b', 'B Income', 'tag', '#6b7280', 'INCOME', 'INCOME', ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, custom_client_id, name, type, created_at)
		VALUES
			('a-contact', 'ws-a', 'A-001', 'A Contact', 'client', ?),
			('b-contact', 'ws-b', 'B-001', 'B Contact', 'client', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES
			('a-invoice', 'a-card', ?, ?, ?, 'OPEN', ?),
			('b-invoice', 'b-card', ?, ?, ?, 'OPEN', ?)
	`, reference, monthStart, dueDate, nowUnix, reference, monthStart, dueDate, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, contact_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('a-expense', 'ws-a', 'user-a', 'a-check', 'a-expense-cat', NULL, 'a-contact', 'EXPENSE', 1000, ?, 'A Expense Tx', 'paid', 1, 1, ?, ?),
			('a-income', 'ws-a', 'user-a', 'a-check', 'a-income-cat', NULL, NULL, 'INCOME', 5000, ?, 'A Income Tx', 'paid', 1, 1, ?, ?),
			('a-card-expense', 'ws-a', 'user-a', 'a-card', 'a-expense-cat', 'a-invoice', NULL, 'EXPENSE', 7000, ?, 'A Card Tx', 'paid', 1, 1, ?, ?),
			('b-expense', 'ws-b', 'user-b', 'b-check', 'b-expense-cat', NULL, 'b-contact', 'EXPENSE', 9000, ?, 'B Expense Tx', 'paid', 1, 1, ?, ?),
			('b-income', 'ws-b', 'user-b', 'b-check', 'b-income-cat', NULL, NULL, 'INCOME', 99000, ?, 'B Income Tx', 'paid', 1, 1, ?, ?),
			('b-card-expense', 'ws-b', 'user-b', 'b-card', 'b-expense-cat', 'b-invoice', NULL, 'EXPENSE', 99000, ?, 'B Card Tx', 'paid', 1, 1, ?, ?)
	`, monthStart, nowUnix, nowUnix, monthStart, nowUnix, nowUnix, monthStart, nowUnix, nowUnix, monthStart, nowUnix, nowUnix, monthStart, nowUnix, nowUnix, monthStart, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES
			('a-limit', 'ws-a', 'a-expense-cat', 10000),
			('b-limit', 'ws-b', 'b-expense-cat', 90000)
	`)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('a-box', 'ws-a', 'A Box', 'a-expense-cat', 20000, 1000, ?, ?),
			('b-box', 'ws-b', 'B Box', 'b-expense-cat', 90000, 9000, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
}

func assertIDs(t *testing.T, label string, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func accountCardIDs(rows []AccountCard) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func creditCardIDs(rows []CreditCardCard) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func transactionRowIDs(rows []TransactionRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func invoiceRowIDs(rows []InvoiceRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func formAccountIDs(rows []FormAccount) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func formCategoryIDs(rows []FormCategory) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func contatoRowIDs(rows []ContatoRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func limiteCardIDs(rows []LimiteCard) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func caixinhaCardIDs(rows []CaixinhaCard) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func categoriaBarIDs(rows []CategoriaBar) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func configCategoryIDs(rows []ConfigCategoryRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func configAccountIDs(rows []ConfigAccountRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func configCardIDs(rows []ConfigCardRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.AccountID)
	}
	return out
}

func assertTransactionDescriptionCount(t *testing.T, db *sql.DB, description string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = ?`, description).Scan(&got); err != nil {
		t.Fatalf("query transaction description count: %v", err)
	}
	if got != want {
		t.Fatalf("transaction description %q count = %d, want %d", description, got, want)
	}
}

func assertRowExists(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM `+table+` WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("query %s row %s: %v", table, id, err)
	}
	if count != 1 {
		t.Fatalf("%s row %s count = %d, want 1", table, id, count)
	}
}

func assertInvoiceStatus(t *testing.T, db *sql.DB, invoiceID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, invoiceID).Scan(&got); err != nil {
		t.Fatalf("query invoice status: %v", err)
	}
	if got != want {
		t.Fatalf("invoice %s status = %q, want %q", invoiceID, got, want)
	}
}

func assertContactName(t *testing.T, db *sql.DB, contactID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT name FROM contacts WHERE id = ?`, contactID).Scan(&got); err != nil {
		t.Fatalf("query contact name: %v", err)
	}
	if got != want {
		t.Fatalf("contact %s name = %q, want %q", contactID, got, want)
	}
}

func assertCategoryName(t *testing.T, db *sql.DB, categoryID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT name FROM categories WHERE id = ?`, categoryID).Scan(&got); err != nil {
		t.Fatalf("query category name: %v", err)
	}
	if got != want {
		t.Fatalf("category %s name = %q, want %q", categoryID, got, want)
	}
}

func assertAccountName(t *testing.T, db *sql.DB, accountID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT name FROM accounts WHERE id = ?`, accountID).Scan(&got); err != nil {
		t.Fatalf("query account name: %v", err)
	}
	if got != want {
		t.Fatalf("account %s name = %q, want %q", accountID, got, want)
	}
}

func assertBoxLedgerCount(t *testing.T, db *sql.DB, boxID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE box_id = ?`, boxID).Scan(&got); err != nil {
		t.Fatalf("query box ledger count: %v", err)
	}
	if got != want {
		t.Fatalf("box ledger count for %s = %d, want %d", boxID, got, want)
	}
}

func testTenantMetasTemplates(t *testing.T) TemplateEngine {
	t.Helper()
	return testOOBTemplates(t)
}

func TestCreditCardAvailableLimitDisplay(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedTenantIsolationScenario(t, db)

	dashboard := BuildDashboardData(db, "user-a", "ws-a")

	// ws-a has card 'a-card' with credit_limit=100000, invoice expense total=7000
	// expected: limitAvailable = 100000 - 7000 = 93000 (R$ 930,00)
	if len(dashboard.Cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(dashboard.Cards))
	}

	card := dashboard.Cards[0]
	if card.ID != "a-card" {
		t.Fatalf("expected card a-card, got %s", card.ID)
	}
	if card.Amount != 7000 {
		t.Fatalf("card Amount = %d, want 7000", card.Amount)
	}

	expLimitMoney := MoneyMinor(93000)
	if card.LimitMoney.Reais != expLimitMoney.Reais || card.LimitMoney.Cents != expLimitMoney.Cents {
		t.Fatalf("card LimitMoney = R$ %s,%s, want R$ %s,%s",
			card.LimitMoney.Reais, card.LimitMoney.Cents, expLimitMoney.Reais, expLimitMoney.Cents)
	}
	// 93000 / 100000 * 100 = 93%
	if card.LimitPercent != 93 {
		t.Fatalf("card LimitPercent = %d, want 93", card.LimitPercent)
	}

	// Also verify ws-b: card 'b-card' with credit_limit=900000, invoice expense=99000
	// expected: limitAvailable = 900000 - 99000 = 801000
	dashboardB := BuildDashboardData(db, "user-b", "ws-b")
	if len(dashboardB.Cards) != 1 {
		t.Fatalf("ws-b expected 1 card, got %d", len(dashboardB.Cards))
	}

	cardB := dashboardB.Cards[0]
	if cardB.ID != "b-card" {
		t.Fatalf("expected card b-card, got %s", cardB.ID)
	}

	// 801000 / 900000 = 89% (truncated)
	expBLimitMoney := MoneyMinor(801000)
	if cardB.LimitMoney.Reais != expBLimitMoney.Reais || cardB.LimitMoney.Cents != expBLimitMoney.Cents {
		t.Fatalf("card B LimitMoney = R$ %s,%s, want R$ %s,%s",
			cardB.LimitMoney.Reais, cardB.LimitMoney.Cents, expBLimitMoney.Reais, expBLimitMoney.Cents)
	}
	if cardB.LimitPercent != 89 {
		t.Fatalf("card B LimitPercent = %d, want 89", cardB.LimitPercent)
	}
}
