package handlers

import (
	"bytes"
	"database/sql"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedBusinessWorkspace(t *testing.T, db *sql.DB) {
	t.Helper()
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
}

func newCompVencMultipartUpdateRequest(t *testing.T, path string, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestBusinessListagemAgrupaPorDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, name, type, created_at)
		VALUES ('contact-1', 'ws-test', 'Cliente A', 'client', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, contact_id, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-due-15', 'ws-test', 'user-test', 'checking-test', NULL, 'INCOME', 50000, ?, ?, 'Receita com vencimento', 'pending', 'contact-1', 1, 1, ?, ?)
	`, date, dueDate, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	var found bool
	for _, tx := range data.Transactions {
		if tx.ID == "tx-due-15" {
			found = true
			if tx.Date != dueDate {
				t.Fatalf("Date (display) = %d, want due_date %d", tx.Date, dueDate)
			}
			if tx.CompetenceDate != date {
				t.Fatalf("CompetenceDate = %d, want date %d", tx.CompetenceDate, date)
			}
			if !tx.HasDueDate {
				t.Fatalf("HasDueDate = false, want true")
			}
			if !tx.UsesDueDate {
				t.Fatalf("UsesDueDate = false, want true")
			}
			if tx.DateLabel != "Hoje" && tx.DateLabel != "Ontem" && !strings.HasPrefix(tx.DateLabel, "15 Ago") {
				t.Fatalf("DateLabel = %q, want label derived from due_date 15/08/2026", tx.DateLabel)
			}
			if tx.CompetenciaDateLabel != "01/08/2026" && tx.CompetenciaDateLabel != "01/08" {
				t.Fatalf("CompetenciaDateLabel = %q, want competence label", tx.CompetenciaDateLabel)
			}
		}
	}
	if !found {
		t.Fatalf("tx-due-15 not found in transactions")
	}
}

func TestBusinessListagemAgrupaPorDateSemDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-no-due', 'ws-test', 'user-test', 'checking-test', NULL, 'EXPENSE', 25000, ?, 'Despesa sem vencimento', 'pending', 1, 1, ?, ?)
	`, date, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	var found bool
	for _, tx := range data.Transactions {
		if tx.ID == "tx-no-due" {
			found = true
			if tx.Date != date {
				t.Fatalf("Date (display) = %d, want date %d", tx.Date, date)
			}
			if tx.HasDueDate {
				t.Fatalf("HasDueDate = true, want false")
			}
			if tx.UsesDueDate {
				t.Fatalf("UsesDueDate = true, want false")
			}
			if tx.DateLabel != "1 Ago" {
				t.Fatalf("DateLabel = %q, want %q", tx.DateLabel, "1 Ago")
			}
		}
	}
	if !found {
		t.Fatalf("tx-no-due not found in transactions")
	}
}

func TestBusinessListagemCompIndicator(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-diff', 'ws-test', 'user-test', 'checking-test', NULL, 'EXPENSE', 10000, ?, ?, 'Diff', 'pending', 1, 1, ?, ?)
	`, date, dueDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-equal', 'ws-test', 'user-test', 'checking-test', NULL, 'EXPENSE', 10000, ?, ?, 'Equal', 'pending', 1, 1, ?, ?)
	`, date, date, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	tpl := template.Must(template.New("row-test").Funcs(template.FuncMap{
		"appVersion": func() string { return "test" },
		"assetPath":  func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
	))

	for _, tx := range data.Transactions {
		if tx.ID != "tx-diff" && tx.ID != "tx-equal" {
			continue
		}
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "lancamento-row", tx); err != nil {
			t.Fatalf("execute row for %s: %v", tx.ID, err)
		}
		html := buf.String()
		if tx.ID == "tx-diff" {
			if !strings.Contains(html, "Comp.") {
				t.Fatalf("tx-diff should show Comp. indicator, html:\n%s", html)
			}
			if strings.Contains(html, "Venc.") {
				t.Fatalf("tx-diff should not show Venc. badge, html:\n%s", html)
			}
		}
		if tx.ID == "tx-equal" {
			if strings.Contains(html, "Comp.") {
				t.Fatalf("tx-equal should not show Comp. indicator, html:\n%s", html)
			}
		}
	}
}

func TestBusinessFormEditCarregaCompetenciaEVencimento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-edit-dates', 'ws-test', 'user-test', 'checking-test', NULL, 'EXPENSE', 10000, ?, ?, 'Edição datas', 'pending', 1, 1, ?, ?)
	`, date, dueDate, now, now)

	tpl := template.Must(template.New("form-test").Funcs(template.FuncMap{
		"appVersion": func() string { return "test" },
		"assetPath":  func(path string) string { return path + "?v=test" },
		"intRange": func(start, end int) []int {
			var res []int
			for i := start; i <= end; i++ {
				res = append(res, i)
			}
			return res
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/components/form_lancamento.html"),
		resolveTemplatePath(t, "templates/components/financial-emblem.html"),
	))

	handler := TransactionHandler{
		DB:          db,
		Templates:   tpl,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/transacoes/tx-edit-dates", nil)
	handler.HandleDetalheTransacao(rr, req, "tx-edit-dates")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	html := rr.Body.String()

	wantData := `name="data" value="2026-07-29"`
	if !strings.Contains(html, wantData) {
		t.Fatalf("competencia input missing %q, html:\n%s", wantData, html)
	}
	wantDue := `name="due_date" value="2026-07-31"`
	if !strings.Contains(html, wantDue) {
		t.Fatalf("due_date input missing %q, html:\n%s", wantDue, html)
	}
	if !strings.Contains(html, "Competência") {
		t.Fatalf("resumo textual 'Competência' missing, html:\n%s", html)
	}
	if !strings.Contains(html, "Vencimento") {
		t.Fatalf("resumo textual 'Vencimento' missing, html:\n%s", html)
	}
}

func TestBusinessUpdatePersisteDueDateSemMudarDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-form-expense', 'ws-test', 'Alimentação', 'utensils', '#f97316', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-update-due', 'ws-test', 'user-test', 'checking-test', 'cat-form-expense', 'EXPENSE', 10000, ?, ?, 'Update due', 'pending', 1, 1, ?, ?)
	`, date, dueDate, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newCompVencMultipartUpdateRequest(t, "/transacoes/tx-update-due", map[string]string{
		"valor":            "R$ 100,00",
		"descricao":        "Update due",
		"data":             "2026-08-01",
		"due_date":         "2026-08-20",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "pending",
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, "tx-update-due")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	var newDate int64
	var newDueDate sql.NullInt64
	if err := db.QueryRow(`SELECT date, due_date FROM transactions WHERE id = 'tx-update-due' AND workspace_id = 'ws-test'`).Scan(&newDate, &newDueDate); err != nil {
		t.Fatalf("query updated transaction: %v", err)
	}
	if newDate != date {
		t.Fatalf("date changed = %d, want %d", newDate, date)
	}
	if !newDueDate.Valid || newDueDate.Int64 == dueDate {
		t.Fatalf("due_date should have changed: valid=%v value=%d", newDueDate.Valid, newDueDate.Int64)
	}
}

func TestBusinessUpdateDueDateDisparaRefreshDaLista(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-form-expense', 'ws-test', 'Alimentação', 'utensils', '#f97316', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-refresh', 'ws-test', 'user-test', 'checking-test', 'cat-form-expense', 'EXPENSE', 10000, ?, ?, 'Refresh', 'pending', 1, 1, ?, ?)
	`, date, dueDate, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newCompVencMultipartUpdateRequest(t, "/transacoes/tx-refresh", map[string]string{
		"valor":            "R$ 100,00",
		"descricao":        "Refresh",
		"data":             "2026-08-01",
		"due_date":         "2026-08-25",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "pending",
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, "tx-refresh")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	trigger := rr.Header().Get("HX-Trigger")
	if trigger != "refreshLancamentosList" {
		t.Fatalf("HX-Trigger = %q, want refreshLancamentosList", trigger)
	}
	reswap := rr.Header().Get("HX-Reswap")
	if reswap != "none" {
		t.Fatalf("HX-Reswap = %q, want none", reswap)
	}
}

func TestPersonalNaoMostraCompIndicator(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-personal-due', 'ws-test', 'user-test', 'checking-test', NULL, 'EXPENSE', 10000, ?, ?, 'Personal', 'pending', 1, 1, ?, ?)
	`, date, dueDate, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	for _, tx := range data.Transactions {
		if tx.ID == "tx-personal-due" {
			if tx.UsesDueDate {
				t.Fatalf("Personal UsesDueDate = true, want false")
			}
		}
	}
}

func TestBusinessUpdatePersisteDateSemMudarDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedBusinessWorkspace(t, db)

	now := time.Now().Unix()
	date := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dueDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-form-expense', 'ws-test', 'Alimentação', 'utensils', '#f97316', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-update-date', 'ws-test', 'user-test', 'checking-test', 'cat-form-expense', 'EXPENSE', 10000, ?, ?, 'Update date', 'pending', 1, 1, ?, ?)
	`, date, dueDate, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newCompVencMultipartUpdateRequest(t, "/transacoes/tx-update-date", map[string]string{
		"valor":            "R$ 100,00",
		"descricao":        "Update date",
		"data":             "2026-08-05",
		"due_date":         "2026-08-15",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "pending",
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, "tx-update-date")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	var newDate int64
	var newDueDate sql.NullInt64
	if err := db.QueryRow(`SELECT date, due_date FROM transactions WHERE id = 'tx-update-date' AND workspace_id = 'ws-test'`).Scan(&newDate, &newDueDate); err != nil {
		t.Fatalf("query updated transaction: %v", err)
	}
	wantDate := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC).Unix()
	if newDate != wantDate {
		t.Fatalf("date = %d, want %d", newDate, wantDate)
	}
	if !newDueDate.Valid || newDueDate.Int64 != dueDate {
		t.Fatalf("due_date changed or invalid: valid=%v value=%d, want %d", newDueDate.Valid, newDueDate.Int64, dueDate)
	}
}
