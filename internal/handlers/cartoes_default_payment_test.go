package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCreateCardWithDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentScenario(t, db)

	tmpl := template.Must(template.New("test").Parse(`
{{define "configuracoes-cartoes-content"}}ok{{end}}
{{define "configuracoes-cartoes-page"}}ok{{end}}
`))
	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN", Templates: tmpl}

	form := url.Values{}
	form.Set("provider_slug", "nubank")
	form.Set("credit_limit", "500000")
	form.Set("closing_day", "20")
	form.Set("due_day", "10")
	form.Set("default_payment_account_id", "checking-1")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleCartoesCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Trigger-After-Swap"); got != "workspaceColorsChanged" {
		t.Fatalf("HX-Trigger-After-Swap = %q, want workspaceColorsChanged", got)
	}

	var defaultPaymentAccountID sql.NullString
	err := db.QueryRow(`
		SELECT default_payment_account_id FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND a.id != 'card-1'
	`, "ws-1").Scan(&defaultPaymentAccountID)
	if err != nil {
		t.Fatalf("query default_payment_account_id: %v", err)
	}
	if !defaultPaymentAccountID.Valid || defaultPaymentAccountID.String != "checking-1" {
		t.Fatalf("default_payment_account_id = %v, want checking-1", defaultPaymentAccountID)
	}
}

func TestCreateCardWithoutDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentScenario(t, db)

	tmpl := template.Must(template.New("test").Parse(`
{{define "configuracoes-cartoes-content"}}ok{{end}}
{{define "configuracoes-cartoes-page"}}ok{{end}}
`))
	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN", Templates: tmpl}

	form := url.Values{}
	form.Set("provider_slug", "nubank")
	form.Set("credit_limit", "500000")
	form.Set("closing_day", "20")
	form.Set("due_day", "10")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleCartoesCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Trigger-After-Swap"); got != "workspaceColorsChanged" {
		t.Fatalf("HX-Trigger-After-Swap = %q, want workspaceColorsChanged", got)
	}

	var defaultPaymentAccountID sql.NullString
	err := db.QueryRow(`
		SELECT default_payment_account_id FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND a.id != 'card-1'
	`, "ws-1").Scan(&defaultPaymentAccountID)
	if err != nil {
		t.Fatalf("query default_payment_account_id: %v", err)
	}
	if defaultPaymentAccountID.Valid {
		t.Fatalf("default_payment_account_id should be NULL, got %v", defaultPaymentAccountID)
	}
}

func TestCreateCardRejectsDefaultPaymentFromOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentScenario(t, db)

	tmpl := template.Must(template.New("test").Parse(`
{{define "configuracoes-cartoes-content"}}ok{{end}}
{{define "configuracoes-cartoes-page"}}ok{{end}}
`))
	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN", Templates: tmpl}

	form := url.Values{}
	form.Set("provider_slug", "nubank")
	form.Set("credit_limit", "500000")
	form.Set("closing_day", "20")
	form.Set("due_day", "10")
	form.Set("default_payment_account_id", "checking-ws2")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleCartoesCreate(rr, req)

	// Verify the card was NOT created (only the seeded card-1 should exist)
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_cards`).Scan(&count); err != nil {
		t.Fatalf("count credit_cards: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected only 1 credit_card (the seeded one), got %d. No new card should be created with cross-ws payment account", count)
	}
}

func TestEditCardSetsDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentScenario(t, db)

	tmpl := template.Must(template.New("test").Parse(`
{{define "config-cartoes-row"}}ok{{end}}
`))

	form := url.Values{}
	form.Set("name", "Cartao Editado")
	form.Set("credit_limit", "600000")
	form.Set("closing_day", "15")
	form.Set("due_day", "5")
	form.Set("icon", "credit-card")
	form.Set("color", "#6B7280")
	form.Set("default_payment_account_id", "checking-1")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes/card-1/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", Templates: tmpl}
	h.HandleCartoesInlineSave(rr, req, "card-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Trigger-After-Swap"); got != "workspaceColorsChanged" {
		t.Fatalf("HX-Trigger-After-Swap = %q, want workspaceColorsChanged", got)
	}

	var defaultPaymentAccountID sql.NullString
	err := db.QueryRow(`SELECT default_payment_account_id FROM credit_cards WHERE account_id = ?`, "card-1").Scan(&defaultPaymentAccountID)
	if err != nil {
		t.Fatalf("query default_payment_account_id: %v", err)
	}
	if !defaultPaymentAccountID.Valid || defaultPaymentAccountID.String != "checking-1" {
		t.Fatalf("default_payment_account_id = %v, want checking-1", defaultPaymentAccountID)
	}
}

func TestEditCardRemovesDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentScenario(t, db)

	tmpl := template.Must(template.New("test").Parse(`
{{define "config-cartoes-row"}}ok{{end}}
`))

	execTestSQL(t, db, `UPDATE credit_cards SET default_payment_account_id = 'checking-1' WHERE account_id = 'card-1'`)

	form := url.Values{}
	form.Set("name", "Cartao Editado")
	form.Set("credit_limit", "600000")
	form.Set("closing_day", "15")
	form.Set("due_day", "5")
	form.Set("icon", "credit-card")
	form.Set("color", "#6B7280")
	form.Set("default_payment_account_id", "")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes/card-1/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", Templates: tmpl}
	h.HandleCartoesInlineSave(rr, req, "card-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var defaultPaymentAccountID sql.NullString
	err := db.QueryRow(`SELECT default_payment_account_id FROM credit_cards WHERE account_id = ?`, "card-1").Scan(&defaultPaymentAccountID)
	if err != nil {
		t.Fatalf("query default_payment_account_id: %v", err)
	}
	if defaultPaymentAccountID.Valid {
		t.Fatalf("default_payment_account_id should be NULL after removal, got %v", defaultPaymentAccountID)
	}
}

func TestEditCardRejectsDefaultPaymentFromOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentScenario(t, db)

	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN"}

	form := url.Values{}
	form.Set("name", "Cartao Editado")
	form.Set("credit_limit", "600000")
	form.Set("closing_day", "15")
	form.Set("due_day", "5")
	form.Set("icon", "credit-card")
	form.Set("color", "#6B7280")
	form.Set("default_payment_account_id", "checking-ws2")
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/cartoes/card-1/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleCartoesInlineSave(rr, req, "card-1")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 for cross-workspace payment account, got %d, body = %q", rr.Code, rr.Body.String())
	}
}

func TestBuildFaturaDataPreSelectsDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentInvoiceScenario(t, db)

	execTestSQL(t, db, `UPDATE credit_cards SET default_payment_account_id = 'checking-1' WHERE account_id = 'card-1'`)

	data, err := buildFaturaDataForInvoice(db, "ws-1", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.DefaultPaymentAccountID != "checking-1" {
		t.Fatalf("DefaultPaymentAccountID = %q, want checking-1", data.DefaultPaymentAccountID)
	}
}

func TestBuildFaturaDataWithoutDefaultPaymentKeepsEmpty(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentInvoiceScenario(t, db)

	data, err := buildFaturaDataForInvoice(db, "ws-1", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.DefaultPaymentAccountID != "" {
		t.Fatalf("DefaultPaymentAccountID should be empty, got %q", data.DefaultPaymentAccountID)
	}
}

func TestBuildFaturaDataClearsInvalidDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentInvoiceScenario(t, db)

	execTestSQL(t, db, `UPDATE credit_cards SET default_payment_account_id = 'nonexistent-account' WHERE account_id = 'card-1'`)

	data, err := buildFaturaDataForInvoice(db, "ws-1", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.DefaultPaymentAccountID != "" {
		t.Fatalf("DefaultPaymentAccountID should be empty for invalid account, got %q", data.DefaultPaymentAccountID)
	}
}

func TestBuildFaturaDataClearsRemovedDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDefaultPaymentInvoiceScenario(t, db)

	execTestSQL(t, db, `UPDATE credit_cards SET default_payment_account_id = 'checking-2' WHERE account_id = 'card-1'`)
	execTestSQL(t, db, `DELETE FROM accounts WHERE id = 'checking-2'`)

	data, err := buildFaturaDataForInvoice(db, "ws-1", "invoice-2026-08", "desc")
	if err != nil {
		t.Fatalf("buildFaturaDataForInvoice: %v", err)
	}
	if data.DefaultPaymentAccountID != "" {
		t.Fatalf("DefaultPaymentAccountID should be empty after account deleted, got %q", data.DefaultPaymentAccountID)
	}
}

func TestPaymentTotalStillWorksWithDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-2', 'ws-test', 'Conta Secundaria', 'CHECKING', 100000, 100000, ?, ?)`, time.Now().Unix(), time.Now().Unix())
	execTestSQL(t, db, `UPDATE credit_cards SET default_payment_account_id = 'checking-2' WHERE account_id = 'card-test'`)

	templates := testInvoicePaymentTemplates(t)

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "settle")
	form.Set("confirm_settle", "1")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&status); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", status)
	}
}

func TestPaymentPartialStillWorksWithDefaultPaymentAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-2', 'ws-test', 'Conta Secundaria', 'CHECKING', 100000, 100000, ?, ?)`, time.Now().Unix(), time.Now().Unix())
	execTestSQL(t, db, `UPDATE credit_cards SET default_payment_account_id = 'checking-2' WHERE account_id = 'card-test'`)

	templates := testInvoicePaymentTemplates(t)

	handler := FaturasHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   templates,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	form := url.Values{}
	form.Set("invoice_id", "invoice-2026-08")
	form.Set("payment_account_id", "checking-test")
	form.Set("payment_mode", "partial")
	form.Set("payment_amount", "100,00")
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandlePagarFatura(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var paidAmount sql.NullInt64
	if err := db.QueryRow(`SELECT paid_amount FROM invoices WHERE id = ?`, "invoice-2026-08").Scan(&paidAmount); err != nil {
		t.Fatalf("invoice query: %v", err)
	}
	if !paidAmount.Valid || paidAmount.Int64 != 10000 {
		t.Fatalf("paid_amount = %v, want 10000", paidAmount)
	}
}

func seedDefaultPaymentScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'User', 'u@e.com', 'h', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-1', 'WS1', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-2', 'WS2', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-1', 'user-1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-2', 'user-1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-1', 'ws-1', 'Conta Corrente', 'CHECKING', 100000, 100000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-ws2', 'ws-2', 'Conta WS2', 'CHECKING', 50000, 50000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-1', 'ws-1', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-1', 'card-1', 25, 5, 500000)`)
}

func seedDefaultPaymentInvoiceScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	futureClosing := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC).Unix()
	futureDue := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'User', 'u@e.com', 'h', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-1', 'WS1', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-1', 'user-1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-1', 'ws-1', 'Conta Corrente', 'CHECKING', 100000, 100000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-2', 'ws-1', 'Conta Secundaria', 'CHECKING', 50000, 50000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-1', 'ws-1', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-1', 'card-1', 20, 10, 500000)`)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('invoice-2026-08', 'card-1', '2026-08', ?, ?, 'OPEN', ?)`, futureClosing, futureDue, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at) VALUES ('cat-1', 'ws-1', 'Categoria', 'EXPENSE', 'Estilo de Vida', ?)`, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at) VALUES ('purchase-1', 'ws-1', 'user-1', 'card-1', 'invoice-2026-08', 'EXPENSE', 25000, ?, 'Compra Teste', 'paid', 1, 1, ?, ?)`, now, now, now)
}
