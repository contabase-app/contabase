package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// seedRecurringSeriesOnCard cria uma recorrencia MONTHLY finita (3 ocorrencias)
// em cartao de credito, com a primeira ocorrencia em startDate e invoice aberta.
func seedRecurringSeriesOnCard(t *testing.T, db *sql.DB, description, startDate string, total int64) (ruleID, firstID string) {
	t.Helper()
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	ruleID = "rule-p51-card"
	firstID = "tx-p51-card"
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PAID', 1, ?, ?, ?)
	`, ruleID, description, testUnixDate(startDate), total, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'invoice-2026-08', 'EXPENSE', 15000, ?, ?, 'paid', 1, 1, ?, 1, ?, ?)
	`, firstID, testUnixDate(startDate), description, ruleID, now, now)
	return ruleID, firstID
}

func seedRecurringSeriesOnChecking(t *testing.T, db *sql.DB, description, startDate string, total int64) (ruleID, firstID string) {
	t.Helper()
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	ruleID = "rule-p51-chk"
	firstID = "tx-p51-chk"
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, ?)
	`, ruleID, description, testUnixDate(startDate), total, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 1, ?, ?)
	`, firstID, testUnixDate(startDate), description, ruleID, now, now)
	return ruleID, firstID
}

func seriesDatesByDescription(t *testing.T, db *sql.DB, description string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT date FROM transactions WHERE workspace_id = 'ws-test' AND description = ? ORDER BY date ASC`, description)
	if err != nil {
		t.Fatalf("query p51 dates: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d int64
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("scan p51 date: %v", err)
		}
		out = append(out, time.Unix(d, 0).UTC().Format("2006-01-02"))
	}
	return out
}

func seriesCountByDescription(t *testing.T, db *sql.DB, description string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND description = ?`, description).Scan(&n); err != nil {
		t.Fatalf("count p51: %v", err)
	}
	return n
}

func assertDates(t *testing.T, db *sql.DB, description string, want []string) {
	t.Helper()
	got := seriesDatesByDescription(t, db, description)
	if len(got) != len(want) {
		t.Fatalf("dates count = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dates[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func seriesEditRequest(t *testing.T, id, data, scope, accountID, status string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := newMultipartUpdateRequest(t, "/transacoes/"+id, map[string]string{
		"valor":            "150,00",
		"descricao":        "Assinatura P51",
		"data":             data,
		"tipo":             "despesa",
		"origem_conta_id":  accountID,
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": status,
		"escopo":           scope,
	})
	return req, httptest.NewRecorder()
}

func TestRecurringSingleScopeOnInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "single", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 1 {
		t.Errorf("count = %d, want 1; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10"})
}

func TestRecurringFutureScopeOnInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "future", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestRecurringAllScopeOnInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestRecurringAllScopeNoInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-24", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "checking-test", "pending")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestRecurringFutureScopeNoInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-24", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "future", "checking-test", "pending")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestRecurringAllScopeForwardDateMovesWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-10", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-24", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-24", "2026-07-24", "2026-08-24"})
}

func TestRecurringFutureScopeForwardDateMovesWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-10", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-24", "future", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-24", "2026-07-24", "2026-08-24"})
}

func TestRecurringEditStatusOnlyPreservesBehavior(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "150,00",
		"descricao":        desc,
		"data":             "2026-06-24",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-24", "2026-07-24", "2026-08-24"})
}

func TestRecurringFiniteOccurrencesRespectsLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 2)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 2 {
		t.Errorf("count = %d, want 2 (limit); dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10"})
}

func TestRecurringPaidInvoiceEditIsAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID' WHERE id = 'invoice-2026-08'`)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code == http.StatusConflict {
		t.Fatalf("PAID invoice should no longer block recurring series update, got 409: %s", rr.Body.String())
	}
}

// --- Parcelados (regressao) ---

func TestInstallmentAllScopeOnInvoiceDoesNotRegress(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Parcelado Regressao"
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := insertHandler.insertTransaction("EXPENSE", 12000, desc, "", "", testUnixDate("2026-06-24"), "card-test", "", "cat-expense-rec", 3, "paid", false, "", "", 0, false, nil, "", false)
	if err != nil {
		t.Fatalf("insert installment: %v", err)
	}
	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Fatalf("precondition count = %d, want 3", got)
	}

	ids := recurringIDsByDescription(t, db, desc)
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[0], map[string]string{
		"valor":            "120,00",
		"descricao":        desc,
		"data":             "2026-06-10",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestInstallmentFutureScopeOnInvoiceDoesNotRegress(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Parcelado Regressao Fut"
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := insertHandler.insertTransaction("EXPENSE", 12000, desc, "", "", testUnixDate("2026-06-24"), "card-test", "", "cat-expense-rec", 3, "paid", false, "", "", 0, false, nil, "", false)
	if err != nil {
		t.Fatalf("insert installment: %v", err)
	}

	ids := recurringIDsByDescription(t, db, desc)
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[0], map[string]string{
		"valor":            "120,00",
		"descricao":        desc,
		"data":             "2026-06-10",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestInstallmentAllScopeNoInvoiceDoesNotRegress(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Parcelado Regressao Chk"
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := insertHandler.insertTransaction("EXPENSE", 12000, desc, "", "", testUnixDate("2026-06-24"), "checking-test", "", "cat-expense-rec", 3, "pending", false, "", "", 0, false, nil, "", false)
	if err != nil {
		t.Fatalf("insert installment: %v", err)
	}

	ids := recurringIDsByDescription(t, db, desc)
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[0], map[string]string{
		"valor":            "120,00",
		"descricao":        desc,
		"data":             "2026-06-10",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Errorf("count = %d, want 3; dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func testOOBTemplatesSeries(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("series-test-tmpl").Parse(`
{{define "lancamento-row"}}<tr id="row-p51"></tr>{{end}}
{{define "lancamentos-resumo"}}<div id="lancamentos-resumo"></div>{{end}}
`))
}
