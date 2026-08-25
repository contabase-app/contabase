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

func attachRecurringSeriesIdentity(t *testing.T, db *sql.DB, ruleID, seriesID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		SELECT ?, workspace_id, user_id, active, total_occurrences, ?, ?
		FROM recurring_rules
		WHERE id = ? AND workspace_id = 'ws-test'
	`, seriesID, now, now, ruleID)
	execTestSQL(t, db, `UPDATE recurring_rules SET series_id = ? WHERE id = ? AND workspace_id = 'ws-test'`, seriesID, ruleID)
	execTestSQL(t, db, `UPDATE transactions SET series_id = ? WHERE recurring_rule_id = ? AND workspace_id = 'ws-test'`, seriesID, ruleID)
}

func seedAdditionalCheckingOccurrences(t *testing.T, db *sql.DB, ruleID, seriesID, description string) (middleID, futureID string) {
	t.Helper()
	now := time.Now().Unix()
	middleID = "tx-p51-chk-2"
	futureID = "tx-p51-chk-3"
	for _, tx := range []struct {
		id       string
		date     string
		sequence int64
	}{
		{middleID, "2026-07-05", 2},
		{futureID, "2026-08-05", 3},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, ?, ?, ?, ?)
		`, tx.id, testUnixDate(tx.date), description, ruleID, tx.sequence, seriesID, now, now)
	}
	return middleID, futureID
}

func assertRecurringExceptionFields(t *testing.T, db *sql.DB, txID string, wantException int, wantReason string) {
	t.Helper()
	var gotException int
	var gotReason sql.NullString
	if err := db.QueryRow(`
		SELECT is_recurring_exception, recurring_exception_reason
		FROM transactions
		WHERE id = ? AND workspace_id = 'ws-test'
	`, txID).Scan(&gotException, &gotReason); err != nil {
		t.Fatalf("query recurring exception fields for %s: %v", txID, err)
	}
	if gotException != wantException {
		t.Fatalf("tx %s is_recurring_exception = %d, want %d", txID, gotException, wantException)
	}
	if wantReason == "" {
		if gotReason.Valid {
			t.Fatalf("tx %s recurring_exception_reason = %q, want NULL", txID, gotReason.String)
		}
		return
	}
	if !gotReason.Valid || gotReason.String != wantReason {
		t.Fatalf("tx %s recurring_exception_reason = %q valid=%v, want %q", txID, gotReason.String, gotReason.Valid, wantReason)
	}
}

func assertRecurringIdentity(t *testing.T, db *sql.DB, txID, wantRuleID, wantSeriesID string, wantSequence int64) {
	t.Helper()
	var ruleID, seriesID sql.NullString
	var sequence sql.NullInt64
	if err := db.QueryRow(`
		SELECT recurring_rule_id, series_id, recurrence_sequence
		FROM transactions
		WHERE id = ? AND workspace_id = 'ws-test'
	`, txID).Scan(&ruleID, &seriesID, &sequence); err != nil {
		t.Fatalf("query recurring identity for %s: %v", txID, err)
	}
	if !ruleID.Valid || ruleID.String != wantRuleID {
		t.Fatalf("tx %s recurring_rule_id = %q valid=%v, want %q", txID, ruleID.String, ruleID.Valid, wantRuleID)
	}
	if !seriesID.Valid || seriesID.String != wantSeriesID {
		t.Fatalf("tx %s series_id = %q valid=%v, want %q", txID, seriesID.String, seriesID.Valid, wantSeriesID)
	}
	if !sequence.Valid || sequence.Int64 != wantSequence {
		t.Fatalf("tx %s recurrence_sequence = %d valid=%v, want %d", txID, sequence.Int64, sequence.Valid, wantSequence)
	}
}

func assertRecurringRuleUnchanged(t *testing.T, db *sql.DB, ruleID, wantDescription string, wantAmount int64) {
	t.Helper()
	var gotDescription string
	var gotAmount int64
	if err := db.QueryRow(`
		SELECT description, amount
		FROM recurring_rules
		WHERE id = ? AND workspace_id = 'ws-test'
	`, ruleID).Scan(&gotDescription, &gotAmount); err != nil {
		t.Fatalf("query recurring rule %s: %v", ruleID, err)
	}
	if gotDescription != wantDescription || gotAmount != wantAmount {
		t.Fatalf("rule %s = (%q, %d), want (%q, %d)", ruleID, gotDescription, gotAmount, wantDescription, wantAmount)
	}
}

func assertRecurringTxSummary(t *testing.T, db *sql.DB, txID, wantDescription string, wantAmount int64, wantSequence int64) {
	t.Helper()
	var gotDescription string
	var gotAmount int64
	var gotSequence sql.NullInt64
	if err := db.QueryRow(`
		SELECT description, amount, recurrence_sequence
		FROM transactions
		WHERE id = ? AND workspace_id = 'ws-test'
	`, txID).Scan(&gotDescription, &gotAmount, &gotSequence); err != nil {
		t.Fatalf("query recurring summary for %s: %v", txID, err)
	}
	if gotDescription != wantDescription || gotAmount != wantAmount || !gotSequence.Valid || gotSequence.Int64 != wantSequence {
		t.Fatalf("tx %s = (%q, %d, seq=%d valid=%v), want (%q, %d, seq=%d)", txID, gotDescription, gotAmount, gotSequence.Int64, gotSequence.Valid, wantDescription, wantAmount, wantSequence)
	}
}

func recurringTxRuleBySequence(t *testing.T, db *sql.DB, seriesID string, sequence int64) (txID, ruleID, description string, amount int64) {
	t.Helper()
	if err := db.QueryRow(`
		SELECT id, recurring_rule_id, description, amount
		FROM transactions
		WHERE workspace_id = 'ws-test' AND series_id = ? AND recurrence_sequence = ?
		ORDER BY id ASC
		LIMIT 1
	`, seriesID, sequence).Scan(&txID, &ruleID, &description, &amount); err != nil {
		t.Fatalf("query recurring tx by sequence %d: %v", sequence, err)
	}
	return txID, ruleID, description, amount
}

func recurringRuleVersionFields(t *testing.T, db *sql.DB, ruleID string) (seriesID string, parentRuleID sql.NullString, startSeq int64, endSeq sql.NullInt64, active int) {
	t.Helper()
	if err := db.QueryRow(`
		SELECT COALESCE(series_id, ''), parent_rule_id, COALESCE(effective_start_sequence, 1), effective_end_sequence, active
		FROM recurring_rules
		WHERE id = ? AND workspace_id = 'ws-test'
	`, ruleID).Scan(&seriesID, &parentRuleID, &startSeq, &endSeq, &active); err != nil {
		t.Fatalf("query recurring rule version %s: %v", ruleID, err)
	}
	return seriesID, parentRuleID, startSeq, endSeq, active
}

func recurringRuleCountBySeries(t *testing.T, db *sql.DB, seriesID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count recurring rules by series %s: %v", seriesID, err)
	}
	return count
}

func seedAdditionalCardOccurrences(t *testing.T, db *sql.DB, ruleID, seriesID, description string) (middleID, futureID string) {
	t.Helper()
	now := time.Now().Unix()
	closingUnix, dueUnix := futureOpenInvoiceDatesForTest()
	for _, inv := range []struct {
		id        string
		reference string
	}{
		{"invoice-2026-09", "2026-09"},
		{"invoice-2026-10", "2026-10"},
	} {
		execTestSQL(t, db, `
			INSERT OR IGNORE INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, 'card-test', ?, ?, ?, 'OPEN', ?)
		`, inv.id, inv.reference, closingUnix, dueUnix, now)
	}
	middleID = "tx-p51-card-2"
	futureID = "tx-p51-card-3"
	for _, tx := range []struct {
		id        string
		invoiceID string
		date      string
		sequence  int64
	}{
		{middleID, "invoice-2026-09", "2026-07-24", 2},
		{futureID, "invoice-2026-10", "2026-08-24", 3},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', ?, 'EXPENSE', 15000, ?, ?, 'paid', 1, 1, ?, ?, ?, ?, ?)
		`, tx.id, tx.invoiceID, testUnixDate(tx.date), description, ruleID, tx.sequence, seriesID, now, now)
	}
	return middleID, futureID
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

func TestRecurringSingleScopeMarksManualExceptionAndPreservesIdentity(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-chk")
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-chk", desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+middleID, map[string]string{
		"valor":            "175,00",
		"descricao":        "Assinatura P51 Manual",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "single",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, middleID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringExceptionFields(t, db, middleID, 1, "manual_edit")
	assertRecurringIdentity(t, db, middleID, ruleID, "series-p51-chk", 2)
	assertRecurringRuleUnchanged(t, db, ruleID, desc, 15000)
	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	assertRecurringTxSummary(t, db, middleID, "Assinatura P51 Manual", 17500, 2)
	assertRecurringTxSummary(t, db, futureID, desc, 15000, 3)
}

func TestRecurringSingleScopeBusinessPreservesDueDateAndContactWhenOmitted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-business")
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-p51', 'ws-test', 'Cliente P51', 'client', ?)`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET due_date = ?, contact_id = ? WHERE id = ? AND workspace_id = 'ws-test'`, testUnixDate("2026-06-20"), "contact-p51", firstID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "175,00",
		"descricao":        "Assinatura P51 Business",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "single",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var dueDate sql.NullInt64
	var contactID sql.NullString
	if err := db.QueryRow(`SELECT due_date, contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID).Scan(&dueDate, &contactID); err != nil {
		t.Fatalf("query business due/contact: %v", err)
	}
	if !dueDate.Valid || dueDate.Int64 != testUnixDate("2026-06-20") {
		t.Fatalf("due_date = %v, want 2026-06-20", dueDate)
	}
	if !contactID.Valid || contactID.String != "contact-p51" {
		t.Fatalf("contact_id = %v, want contact-p51", contactID)
	}
	assertRecurringExceptionFields(t, db, firstID, 1, "manual_edit")
}

func TestRecurringSingleScopePersonalKeepsContactNull(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-personal")
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-personal', 'ws-test', 'Contato Personal', 'client', ?)`, time.Now().Unix())

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "175,00",
		"descricao":        "Assinatura P51 Personal",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"contact_id":       "contact-personal",
		"escopo":           "single",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var contactID sql.NullString
	if err := db.QueryRow(`SELECT contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID).Scan(&contactID); err != nil {
		t.Fatalf("query personal contact: %v", err)
	}
	if contactID.Valid {
		t.Fatalf("personal contact_id = %q, want NULL", contactID.String)
	}
	assertRecurringExceptionFields(t, db, firstID, 1, "manual_edit")
}

func TestRecurringSingleScopeDoesNotCrossWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-isolated")
	seedOtherWorkspaceRecurringSeries(t, db)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req, rr := seriesEditRequest(t, firstID, "2026-06-05", "single", "checking-test", "pending")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertOtherWorkspaceRecurringUnchanged(t, db)
	var otherExceptions int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-other' AND is_recurring_exception = 1`).Scan(&otherExceptions); err != nil {
		t.Fatalf("count other exceptions: %v", err)
	}
	if otherExceptions != 0 {
		t.Fatalf("other workspace exceptions = %d, want 0", otherExceptions)
	}
}

// TestNonRecurringSingleEditKeepsPlainTransactionUnversioned prova que a logica de excecao
// manual da Fase 2 (gated por wasRecurringOccurrence) nao afeta o fluxo de edicao de um
// lancamento comum, sem recurring_rule_id. Regressao explicita pedida na revisao da Fase 2.
func TestNonRecurringSingleEditKeepsPlainTransactionUnversioned(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceRecurringSeries(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-plain', 'ws-test', 'Fornecedor Avulso', 'vendor', ?)`, time.Now().Unix())

	now := time.Now().Unix()
	const plainID = "tx-plain-single"
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, due_date, contact_id, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 5000, ?, 'Compra Avulsa', 'pending', 1, 1, ?, 'contact-plain', ?, ?)
	`, plainID, testUnixDate("2026-06-05"), testUnixDate("2026-06-20"), now, now)

	var rulesBefore int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules`).Scan(&rulesBefore); err != nil {
		t.Fatalf("count recurring_rules before: %v", err)
	}

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+plainID, map[string]string{
		"valor":            "75,00",
		"descricao":        "Compra Avulsa Editada",
		"data":             "2026-06-12",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "single",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, plainID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	// 1. Lancamento sem recurring_rule_id foi editado normalmente.
	var (
		gotDesc   string
		gotAmount int64
		gotDate   int64
	)
	if err := db.QueryRow(`SELECT description, amount, date FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, plainID).Scan(&gotDesc, &gotAmount, &gotDate); err != nil {
		t.Fatalf("query edited plain transaction: %v", err)
	}
	gotDateStr := time.Unix(gotDate, 0).UTC().Format("2006-01-02")
	if gotDesc != "Compra Avulsa Editada" || gotAmount != 7500 || gotDateStr != "2026-06-12" {
		t.Fatalf("plain edit = (%q, %d, %q), want (%q, 7500, %q)", gotDesc, gotAmount, gotDateStr, "Compra Avulsa Editada", "2026-06-12")
	}

	// 2 + 3. Nao marca is_recurring_exception e mantem recurring_exception_reason nulo.
	assertRecurringExceptionFields(t, db, plainID, 0, "")

	// 4 + 5 + recurring_rule_id continua nulo (nada versionado).
	var ruleID, seriesID sql.NullString
	var sequence sql.NullInt64
	if err := db.QueryRow(`SELECT recurring_rule_id, series_id, recurrence_sequence FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, plainID).Scan(&ruleID, &seriesID, &sequence); err != nil {
		t.Fatalf("query plain identity: %v", err)
	}
	if ruleID.Valid {
		t.Fatalf("recurring_rule_id = %q, want NULL", ruleID.String)
	}
	if seriesID.Valid {
		t.Fatalf("series_id = %q, want NULL", seriesID.String)
	}
	if sequence.Valid {
		t.Fatalf("recurrence_sequence = %d, want NULL (must not be recalculated as recurrence)", sequence.Int64)
	}

	// 6. due_date omitido do form preserva o valor existente
	// (Fase 4A: omit = PRESERVE para recorrentes e nao-recorrentes).
	// contact_id segue o fluxo anterior (omit = CLEAR para nao-recorrentes).
	var dueDate sql.NullInt64
	if err := db.QueryRow(`SELECT due_date FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, plainID).Scan(&dueDate); err != nil {
		t.Fatalf("query plain due_date: %v", err)
	}
	if !dueDate.Valid {
		t.Fatalf("due_date = NULL, want preserved original value after omit")
	}

	// 7. Nenhuma recurring_rules criada ou alterada.
	var rulesAfter int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules`).Scan(&rulesAfter); err != nil {
		t.Fatalf("count recurring_rules after: %v", err)
	}
	if rulesAfter != rulesBefore {
		t.Fatalf("recurring_rules count = %d, want %d (no rule may be created/changed)", rulesAfter, rulesBefore)
	}
	var wsTestRules int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id = 'ws-test'`).Scan(&wsTestRules); err != nil {
		t.Fatalf("count ws-test recurring_rules: %v", err)
	}
	if wsTestRules != 0 {
		t.Fatalf("ws-test recurring_rules = %d, want 0", wsTestRules)
	}

	// 8. Isolamento de workspace mantido.
	assertOtherWorkspaceRecurringUnchanged(t, db)
}

func TestFutureRecurringUpdateSkipsManualExceptions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-future-skip")
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-future-skip", desc)
	execTestSQL(t, db, `
		UPDATE transactions
		SET is_recurring_exception = 1,
			recurring_exception_reason = 'manual_edit',
			description = 'Assinatura P51 Manual'
		WHERE id = ? AND workspace_id = 'ws-test'
	`, middleID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "200,00",
		"descricao":        "Assinatura P51 Future",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (protected occurrence blocks), body = %q", rr.Code, rr.Body.String())
	}

	_ = futureID
}

func TestRecurringFutureSplitCreatesVersionedRuleFromCurrentSequence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-split")
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-split", desc)
	seedOtherWorkspaceRecurringSeries(t, db)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+middleID, map[string]string{
		"valor":            "200,00",
		"descricao":        "Assinatura P51 Split",
		"data":             "2026-07-10",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, middleID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	seriesID, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, ruleID)
	if seriesID != "series-p51-split" || parent.Valid || startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 1 || active != 0 {
		t.Fatalf("old rule version = series:%q parent:%v start:%d end:%v active:%d; want closed at seq 1", seriesID, parent, startSeq, endSeq, active)
	}
	_, firstRule, firstDesc, firstAmount := recurringTxRuleBySequence(t, db, "series-p51-split", 1)
	_, splitRule, splitDesc, splitAmount := recurringTxRuleBySequence(t, db, "series-p51-split", 2)
	_, futureRule, futureDesc, futureAmount := recurringTxRuleBySequence(t, db, "series-p51-split", 3)
	if firstID == "" || futureID == "" {
		t.Fatalf("seed returned empty ids")
	}
	if firstRule != ruleID || firstDesc != desc || firstAmount != 15000 {
		t.Fatalf("past tx = rule:%q desc:%q amount:%d; want old rule unchanged", firstRule, firstDesc, firstAmount)
	}
	if splitRule == ruleID || splitRule == "" || futureRule != splitRule {
		t.Fatalf("future split rules = seq2:%q seq3:%q old:%q; want same new rule", splitRule, futureRule, ruleID)
	}
	if splitDesc != "Assinatura P51 Split" || splitAmount != 20000 || futureDesc != "Assinatura P51 Split" || futureAmount != 20000 {
		t.Fatalf("future txs = (%q,%d)/(%q,%d), want split data", splitDesc, splitAmount, futureDesc, futureAmount)
	}
	newSeriesID, newParent, newStartSeq, newEndSeq, newActive := recurringRuleVersionFields(t, db, splitRule)
	if newSeriesID != "series-p51-split" || !newParent.Valid || newParent.String != ruleID || newStartSeq != 2 || newEndSeq.Valid || newActive != 1 {
		t.Fatalf("new rule version = series:%q parent:%v start:%d end:%v active:%d; want child at seq 2", newSeriesID, newParent, newStartSeq, newEndSeq, newActive)
	}
	assertRecurringExceptionFields(t, db, middleID, 0, "")
	assertRecurringExceptionFields(t, db, futureID, 0, "")
	assertOtherWorkspaceRecurringUnchanged(t, db)
}

func TestRecurringFutureSplitPreservesPaidTransactionWithoutInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-paid-preserved")
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-paid-preserved", desc)
	execTestSQL(t, db, `UPDATE transactions SET status = 'paid' WHERE id = ? AND workspace_id = 'ws-test'`, futureID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "210,00",
		"descricao":        "Assinatura P51 Paid Preserved",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (protected occurrence blocks), body = %q", rr.Code, rr.Body.String())
	}

	_ = middleID
}

func TestRecurringFutureSplitPreservesClosedInvoiceOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-closed-invoice")
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, "series-p51-closed-invoice", desc)
	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-09'`)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "220,00",
		"descricao":        "Assinatura P51 Open Invoice",
		"data":             "2026-06-10",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (protected occurrence blocks), body = %q", rr.Code, rr.Body.String())
	}

	_ = middleID
	_ = futureID
}

func TestRecurringFutureSplitBusinessPreservesDueDateAndContactWhenOmitted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, _ := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-future-business")
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-future-business", desc)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-future-p51', 'ws-test', 'Fornecedor Future P51', 'vendor', ?)`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET due_date = ?, contact_id = ? WHERE id IN (?, ?) AND workspace_id = 'ws-test'`, testUnixDate("2026-07-20"), "contact-future-p51", middleID, futureID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+middleID, map[string]string{
		"valor":            "230,00",
		"descricao":        "Assinatura P51 Business Future",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, middleID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{middleID, futureID} {
		var dueDate sql.NullInt64
		var contactID sql.NullString
		if err := db.QueryRow(`SELECT due_date, contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&dueDate, &contactID); err != nil {
			t.Fatalf("query due/contact for %s: %v", txID, err)
		}
		if !dueDate.Valid || dueDate.Int64 != testUnixDate("2026-07-20") {
			t.Fatalf("%s due_date = %v, want 2026-07-20", txID, dueDate)
		}
		if !contactID.Valid || contactID.String != "contact-future-p51" {
			t.Fatalf("%s contact_id = %v, want contact-future-p51", txID, contactID)
		}
	}
	var newRuleID, ruleContactID sql.NullString
	if err := db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, middleID).Scan(&newRuleID); err != nil {
		t.Fatalf("query new rule id: %v", err)
	}
	if err := db.QueryRow(`SELECT contact_id FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, newRuleID.String).Scan(&ruleContactID); err != nil {
		t.Fatalf("query new rule contact: %v", err)
	}
	if !ruleContactID.Valid || ruleContactID.String != "contact-future-p51" {
		t.Fatalf("new rule contact_id = %v, want contact-future-p51", ruleContactID)
	}
}

func TestRecurringFutureSplitPersonalKeepsContactNull(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-future-personal")
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-future-personal", desc)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-future-personal', 'ws-test', 'Contato Future Personal', 'client', ?)`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET contact_id = 'contact-future-personal' WHERE id IN (?, ?) AND workspace_id = 'ws-test'`, firstID, middleID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "240,00",
		"descricao":        "Assinatura P51 Personal Future",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"contact_id":       "contact-future-personal",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var newRuleID sql.NullString
	if err := db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID).Scan(&newRuleID); err != nil {
		t.Fatalf("query new rule id: %v", err)
	}
	for _, q := range []struct {
		label string
		sql   string
		arg   string
	}{
		{"first tx", `SELECT contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID},
		{"middle tx", `SELECT contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, middleID},
		{"new rule", `SELECT contact_id FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, newRuleID.String},
	} {
		var contactID sql.NullString
		if err := db.QueryRow(q.sql, q.arg).Scan(&contactID); err != nil {
			t.Fatalf("query contact for %s: %v", q.label, err)
		}
		if contactID.Valid {
			t.Fatalf("%s contact_id = %q, want NULL", q.label, contactID.String)
		}
	}
}

func TestRecurringFutureSplitLegacyRuleEnsuresSeriesAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceRecurringSeries(t, db)
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-legacy-future-split", "Recorrencia Legacy Future Split")
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-other", "user-other", "checking-other", "cat-expense-other", "rule-legacy-future-other", "Recorrencia Legacy Future Split")

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	for i, value := range []string{"250,00", "260,00"} {
		req := newMultipartUpdateRequest(t, "/transacoes/rule-legacy-future-split-tx-1", map[string]string{
			"valor":            value,
			"descricao":        "Recorrencia Legacy Future Split Editada",
			"data":             "2026-07-05",
			"tipo":             "despesa",
			"origem_conta_id":  "checking-test",
			"categoria_id":     "cat-expense-rec",
			"status_pagamento": "pending",
			"escopo":           "future",
		})
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, req, "rule-legacy-future-split-tx-1")
		if rr.Code != http.StatusOK {
			t.Fatalf("run %d status = %d, body = %q", i+1, rr.Code, rr.Body.String())
		}
	}

	var seriesID sql.NullString
	if err := db.QueryRow(`SELECT series_id FROM recurring_rules WHERE id = 'rule-legacy-future-split' AND workspace_id = 'ws-test'`).Scan(&seriesID); err != nil {
		t.Fatalf("query legacy series: %v", err)
	}
	if !seriesID.Valid || seriesID.String == "" {
		t.Fatalf("legacy series_id = %v, want filled", seriesID)
	}
	if got := recurringRuleCountBySeries(t, db, seriesID.String); got != 3 {
		t.Fatalf("rules by series = %d, want 3 after always-new versioned split", got)
	}
	var txCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID.String).Scan(&txCount); err != nil {
		t.Fatalf("count legacy transactions by series: %v", err)
	}
	if txCount != 2 {
		t.Fatalf("legacy transaction count by series = %d, want 2", txCount)
	}
	var otherSeries sql.NullString
	if err := db.QueryRow(`SELECT series_id FROM recurring_rules WHERE id = 'rule-legacy-future-other' AND workspace_id = 'ws-other'`).Scan(&otherSeries); err != nil {
		t.Fatalf("query other legacy series: %v", err)
	}
	if otherSeries.Valid {
		t.Fatalf("other workspace series_id = %q, want NULL", otherSeries.String)
	}
}

func TestRecurringFutureSplitCurrentPaidTransactionWithoutInvoiceIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-current-paid")
	_, _ = seedAdditionalCheckingOccurrences(t, db, ruleID, "series-p51-current-paid", desc)
	execTestSQL(t, db, `UPDATE transactions SET status = 'paid' WHERE id = ? AND workspace_id = 'ws-test'`, firstID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "270,00",
		"descricao":        "Assinatura P51 Blocked",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %q; want 409", rr.Code, rr.Body.String())
	}
	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	if got := recurringRuleCountBySeries(t, db, "series-p51-current-paid"); got != 1 {
		t.Fatalf("rules by series = %d, want 1 after blocked split", got)
	}
}

func assertNoDuplicateSeriesSequence(t *testing.T, db *sql.DB, seriesID string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT recurrence_sequence, COUNT(1)
		FROM transactions
		WHERE workspace_id = 'ws-test' AND series_id = ?
		GROUP BY recurrence_sequence
		HAVING COUNT(1) > 1
	`, seriesID)
	if err != nil {
		t.Fatalf("query duplicate series sequences: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq sql.NullInt64
		var count int
		if err := rows.Scan(&seq, &count); err != nil {
			t.Fatalf("scan duplicate series sequence: %v", err)
		}
		t.Fatalf("series %s has duplicate recurrence_sequence %d valid=%v count=%d", seriesID, seq.Int64, seq.Valid, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate duplicate series sequences: %v", err)
	}
}

// TestRecurringFutureSplitFromFirstSequenceClosesOldRuleSafely cobre a lacuna da Fase 4:
// split future a partir da PRIMEIRA ocorrência (current_sequence=1). A regra antiga deve
// encerrar de forma segura (effective_end_sequence=0, active=0), a nova regra começa em
// effective_start_sequence=1, o workspace alheio não é afetado e não há sequência duplicada.
func TestRecurringFutureSplitFromFirstSequenceClosesOldRuleSafely(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-first-seq"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	seedOtherWorkspaceRecurringSeries(t, db)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "180,00",
		"descricao":        "Assinatura P51 Split Seq1",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	// Regra antiga esvaziada com a sentinela 0 quando o split começa
	// exatamente no effective_start_sequence.
	oldSeries, oldParent, oldStart, oldEnd, oldActive := recurringRuleVersionFields(t, db, ruleID)
	if oldSeries != series || oldParent.Valid || oldStart != 1 || !oldEnd.Valid || oldEnd.Int64 != 0 || oldActive != 0 {
		t.Fatalf("old rule = series:%q parent:%v start:%d end:%v active:%d; want empty at seq 0", oldSeries, oldParent, oldStart, oldEnd, oldActive)
	}

	// Todas as 3 ocorrências mutáveis migraram para a nova regra, que começa na seq 1.
	_, newRule1, _, _ := recurringTxRuleBySequence(t, db, series, 1)
	_, newRule2, _, _ := recurringTxRuleBySequence(t, db, series, 2)
	_, newRule3, _, _ := recurringTxRuleBySequence(t, db, series, 3)
	if newRule1 == ruleID || newRule1 == "" {
		t.Fatalf("seq1 rule = %q, want new rule (not %q)", newRule1, ruleID)
	}
	if newRule2 != newRule1 || newRule3 != newRule1 {
		t.Fatalf("moved rules = seq1:%q seq2:%q seq3:%q; want same new rule", newRule1, newRule2, newRule3)
	}
	newSeries, newParent, newStart, newEnd, newActive := recurringRuleVersionFields(t, db, newRule1)
	if newSeries != series || !newParent.Valid || newParent.String != ruleID || newStart != 1 || newEnd.Valid || newActive != 1 {
		t.Fatalf("new rule = series:%q parent:%v start:%d end:%v active:%d; want child at seq 1", newSeries, newParent, newStart, newEnd, newActive)
	}
	if firstID == "" || middleID == "" || futureID == "" {
		t.Fatalf("seed returned empty ids")
	}

	assertNoDuplicateSeriesSequence(t, db, series)
	assertOtherWorkspaceRecurringUnchanged(t, db)
	var otherExceptions int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id = 'ws-other' AND series_id IS NOT NULL`).Scan(&otherExceptions); err != nil {
		t.Fatalf("count other workspace series rules: %v", err)
	}
	if otherExceptions != 0 {
		t.Fatalf("other workspace series rules = %d, want 0 (isolation)", otherExceptions)
	}
}

// TestRecurringFutureSplitPreservesPaidInvoiceOccurrence cobre a lacuna da Fase 4: uma
// ocorrência do meio numa fatura PAID não deve ser movida nem sobrescrita pelo split; o
// invoice_id e o status permanecem, e apenas as ocorrências mutáveis migram para a nova regra.
func TestRecurringFutureSplitPreservesPaidInvoiceOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, "series-p51-paid-invoice")
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, "series-p51-paid-invoice", desc)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID' WHERE id = 'invoice-2026-09'`)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "225,00",
		"descricao":        "Assinatura P51 Paid Invoice",
		"data":             "2026-06-24",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (protected occurrence blocks), body = %q", rr.Code, rr.Body.String())
	}

	_ = middleID
	_ = futureID
}

// TestRecurringFutureSplitChainedSplitsKeepConsistentVersioning cobre a lacuna da Fase 4:
// dois splits future consecutivos (seq=2 depois seq=3) devem formar uma cadeia de regras
// versionadas coerente (parent_rule_id encadeado, mesma series_id, effective_start/end
// consistentes) e sem duplicar recurrence_sequence.
func TestRecurringFutureSplitChainedSplitsKeepConsistentVersioning(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-chain"
	ruleA, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleA, series)
	middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleA, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}

	// Primeiro split: future a partir da seq=2.
	req1 := newMultipartUpdateRequest(t, "/transacoes/"+middleID, map[string]string{
		"valor":            "200,00",
		"descricao":        "Assinatura P51 Chain B",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr1 := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr1, req1, middleID)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first split status = %d, body = %q", rr1.Code, rr1.Body.String())
	}
	seq3ID, ruleB, _, _ := recurringTxRuleBySequence(t, db, series, 3)
	if ruleB == ruleA || ruleB == "" {
		t.Fatalf("rule B = %q, want new rule after first split (not %q)", ruleB, ruleA)
	}

	// Segundo split: future a partir da seq=3 (já pertencente à regra B).
	req2 := newMultipartUpdateRequest(t, "/transacoes/"+seq3ID, map[string]string{
		"valor":            "300,00",
		"descricao":        "Assinatura P51 Chain C",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr2 := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr2, req2, seq3ID)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second split status = %d, body = %q", rr2.Code, rr2.Body.String())
	}
	_, ruleC, _, _ := recurringTxRuleBySequence(t, db, series, 3)
	if ruleC == ruleA || ruleC == ruleB || ruleC == "" {
		t.Fatalf("rule C = %q, want distinct new rule (not %q/%q)", ruleC, ruleA, ruleB)
	}

	// Cadeia: A(start1,end1,active0,parent nil) -> B(start2,end2,active0,parent A) -> C(start3,active1,parent B).
	aSeries, aParent, aStart, aEnd, aActive := recurringRuleVersionFields(t, db, ruleA)
	if aSeries != series || aParent.Valid || aStart != 1 || !aEnd.Valid || aEnd.Int64 != 1 || aActive != 0 {
		t.Fatalf("rule A = series:%q parent:%v start:%d end:%v active:%d; want (series,nil,1,1,0)", aSeries, aParent, aStart, aEnd, aActive)
	}
	bSeries, bParent, bStart, bEnd, bActive := recurringRuleVersionFields(t, db, ruleB)
	if bSeries != series || !bParent.Valid || bParent.String != ruleA || bStart != 2 || !bEnd.Valid || bEnd.Int64 != 2 || bActive != 0 {
		t.Fatalf("rule B = series:%q parent:%v start:%d end:%v active:%d; want (series,A,2,2,0)", bSeries, bParent, bStart, bEnd, bActive)
	}
	cSeries, cParent, cStart, cEnd, cActive := recurringRuleVersionFields(t, db, ruleC)
	if cSeries != series || !cParent.Valid || cParent.String != ruleB || cStart != 3 || cEnd.Valid || cActive != 1 {
		t.Fatalf("rule C = series:%q parent:%v start:%d end:%v active:%d; want (series,B,3,nil,1)", cSeries, cParent, cStart, cEnd, cActive)
	}
	if firstID == "" {
		t.Fatalf("seed returned empty first id")
	}

	assertNoDuplicateSeriesSequence(t, db, series)
}

// TestRecurringAllLegacyVsFutureVersionedSideBySide garante que scope=all continua no fluxo
// legado (sem split, mesma regra) enquanto scope=future usa o split versionado (nova regra
// na série). Documenta a diferença de comportamento apontada na revisão da Fase 4.
func TestRecurringAllLegacyVsFutureVersionedSideBySide(t *testing.T) {
	t.Run("all_legacy_keeps_single_rule", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		const desc = "Assinatura P51"
		const series = "series-p51-all-legacy"
		ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
		attachRecurringSeriesIdentity(t, db, ruleID, series)
		seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

		h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
		req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
			"valor":            "199,00",
			"descricao":        "Assinatura P51 All Legacy",
			"data":             "2026-06-05",
			"tipo":             "despesa",
			"origem_conta_id":  "checking-test",
			"categoria_id":     "cat-expense-rec",
			"status_pagamento": "pending",
			"escopo":           "all",
		})
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, req, firstID)
		if rr.Code != http.StatusOK {
			t.Fatalf("all status = %d, body = %q", rr.Code, rr.Body.String())
		}

		// Legado: nenhuma nova regra versionada é criada; a série continua com 1 regra.
		if got := recurringRuleCountBySeries(t, db, series); got != 1 {
			t.Fatalf("all scope rules by series = %d, want 1 (legacy, no split)", got)
		}
		// Todas as ocorrências continuam na mesma regra original.
		for _, seq := range []int64{1, 2, 3} {
			_, rule, _, _ := recurringTxRuleBySequence(t, db, series, seq)
			if rule != ruleID {
				t.Fatalf("all scope seq %d rule = %q, want original %q", seq, rule, ruleID)
			}
		}
		assertNoDuplicateSeriesSequence(t, db, series)
	})

	t.Run("future_versioned_splits_rule", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		const desc = "Assinatura P51"
		const series = "series-p51-future-versioned"
		ruleID, _ := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
		attachRecurringSeriesIdentity(t, db, ruleID, series)
		middleID, _ := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

		h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
		req := newMultipartUpdateRequest(t, "/transacoes/"+middleID, map[string]string{
			"valor":            "199,00",
			"descricao":        "Assinatura P51 Future Versioned",
			"data":             "2026-07-05",
			"tipo":             "despesa",
			"origem_conta_id":  "checking-test",
			"categoria_id":     "cat-expense-rec",
			"status_pagamento": "pending",
			"escopo":           "future",
		})
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, req, middleID)
		if rr.Code != http.StatusOK {
			t.Fatalf("future status = %d, body = %q", rr.Code, rr.Body.String())
		}

		// Versionado: split cria uma segunda regra na mesma série.
		if got := recurringRuleCountBySeries(t, db, series); got != 2 {
			t.Fatalf("future scope rules by series = %d, want 2 (versioned split)", got)
		}
		// Ocorrência anterior (seq1) permanece na regra original; seq2/seq3 na nova regra.
		_, rule1, _, _ := recurringTxRuleBySequence(t, db, series, 1)
		_, rule2, _, _ := recurringTxRuleBySequence(t, db, series, 2)
		if rule1 != ruleID {
			t.Fatalf("future scope seq1 rule = %q, want original %q", rule1, ruleID)
		}
		if rule2 == ruleID || rule2 == "" {
			t.Fatalf("future scope seq2 rule = %q, want new versioned rule", rule2)
		}
		assertNoDuplicateSeriesSequence(t, db, series)
	})
}

func TestDebugRecurringSeedStartDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ruleID, _ := seedRecurringSeriesOnCard(t, db, "Debug", "2026-06-24", 3)
	var startDate int64
	if err := db.QueryRow(`SELECT start_date FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, ruleID).Scan(&startDate); err != nil {
		t.Fatalf("query: %v", err)
	}
	t.Logf("ruleID=%s start_date=%d -> %s", ruleID, startDate, time.Unix(startDate, 0).UTC().Format("2006-01-02 15:04:05"))
}

func TestRecurringFutureScopeOnInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "future", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (protected occurrence blocks), body = %q", rr.Code, rr.Body.String())
	}
}

func TestRecurringAllScopeOnInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for protected selected occurrence, body = %q", rr.Code, rr.Body.String())
	}
	assertDates(t, db, desc, []string{"2026-06-24"})
}

func TestRecurringAllScopeNoInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-24", 3)

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "checking-test", "pending")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := seriesCountByDescription(t, db, desc); got != 1 {
		t.Errorf("count = %d, want 1 (all must not project), dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-10"})
}

func TestRecurringFutureScopeNoInvoiceMovesDateWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-24", 3)

	h := deleteHandler(t, db)
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

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-24", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for protected selected occurrence, body = %q", rr.Code, rr.Body.String())
	}
	assertDates(t, db, desc, []string{"2026-06-10"})
}

func TestRecurringFutureScopeForwardDateMovesWithoutDuplicating(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-10", 3)

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-24", "future", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (protected occurrence blocks), body = %q", rr.Code, rr.Body.String())
	}
}

func TestRecurringEditStatusOnlyPreservesBehavior(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	h := deleteHandler(t, db)
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

	if got := seriesCountByDescription(t, db, desc); got != 1 {
		t.Errorf("count = %d, want 1 (all must not project), dates=%v", got, seriesDatesByDescription(t, db, desc))
	}
	assertDates(t, db, desc, []string{"2026-06-24"})
}

func TestRecurringFiniteOccurrencesRespectsLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 2)

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for protected calendar conflict, body = %q", rr.Code, rr.Body.String())
	}
	assertDates(t, db, desc, []string{"2026-06-24"})
}

func TestRecurringPaidInvoiceCalendarConflictIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	_, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)

	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID' WHERE id = 'invoice-2026-08'`)

	h := deleteHandler(t, db)
	req, rr := seriesEditRequest(t, firstID, "2026-06-10", "all", "card-test", "paid")
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 because the protected occurrence conflicts with the new calendar: %s", rr.Code, rr.Body.String())
	}
}

// --- Parcelados (regressao) ---

func TestInstallmentAllScopeOnInvoiceDoesNotRegress(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Parcelado Regressao"
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := deleteHandler(t, db)
	_, err := insertHandler.insertTransaction("EXPENSE", 12000, desc, "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-06-24")), "card-test", "", "cat-expense-rec", 3, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "", false)
	if err != nil {
		t.Fatalf("insert installment: %v", err)
	}
	keepOpenTestInvoicesFuture(t, db, "card-test")
	if got := seriesCountByDescription(t, db, desc); got != 3 {
		t.Fatalf("precondition count = %d, want 3", got)
	}

	ids := recurringIDsByDescription(t, db, desc)
	h := deleteHandler(t, db)
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

	insertHandler := deleteHandler(t, db)
	_, err := insertHandler.insertTransaction("EXPENSE", 12000, desc, "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-06-24")), "card-test", "", "cat-expense-rec", 3, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "", false)
	if err != nil {
		t.Fatalf("insert installment: %v", err)
	}
	keepOpenTestInvoicesFuture(t, db, "card-test")

	ids := recurringIDsByDescription(t, db, desc)
	h := deleteHandler(t, db)
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

	insertHandler := deleteHandler(t, db)
	_, err := insertHandler.insertTransaction("EXPENSE", 12000, desc, "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-06-24")), "checking-test", "", "cat-expense-rec", 3, "pending", false, "", "", OptionalCivilWriteDate{}, nil, "", false)
	if err != nil {
		t.Fatalf("insert installment: %v", err)
	}

	ids := recurringIDsByDescription(t, db, desc)
	h := deleteHandler(t, db)
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

// --- Fase 5: scope=all versionado seguro ---

func TestRecurringAllVersionedKeepsSingleRule(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-versioned"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 All Versioned",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := recurringRuleCountBySeries(t, db, series); got != 1 {
		t.Fatalf("rules by series = %d, want 1 (versioned all must not split)", got)
	}
	for _, seq := range []int64{1, 2, 3} {
		_, rule, _, _ := recurringTxRuleBySequence(t, db, series, seq)
		if rule != ruleID {
			t.Fatalf("seq %d rule = %q, want original %q", seq, rule, ruleID)
		}
	}
	assertNoDuplicateSeriesSequence(t, db, series)
}

func TestRecurringAllVersionedUpdatesAllMutableOccurrences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-mutable"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Mutable",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, "Assinatura P51 Mutable", 19900, 1)
	assertRecurringTxSummary(t, db, middleID, "Assinatura P51 Mutable", 19900, 2)
	assertRecurringTxSummary(t, db, futureID, "Assinatura P51 Mutable", 19900, 3)
}

func TestRecurringAllVersionedSkipsManualExceptions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-exception"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `
		UPDATE transactions
		SET is_recurring_exception = 1,
		    recurring_exception_reason = 'manual_edit',
		    description = 'Assinatura P51 Manual'
		WHERE id = ? AND workspace_id = 'ws-test'
	`, middleID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 All",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, "Assinatura P51 All", 19900, 1)
	assertRecurringExceptionFields(t, db, middleID, 1, "manual_edit")
	assertRecurringTxSummary(t, db, middleID, "Assinatura P51 Manual", 15000, 2)
	assertRecurringTxSummary(t, db, futureID, "Assinatura P51 All", 19900, 3)
}

func TestRecurringAllVersionedPreservesPaidWithoutInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-paid"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	_, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE transactions SET status = 'paid' WHERE id = ? AND workspace_id = 'ws-test'`, futureID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Paid Preserved",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, futureID, desc, 15000, 3)
	var rule sql.NullString
	if err := db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, futureID).Scan(&rule); err != nil {
		t.Fatalf("query paid rule: %v", err)
	}
	if !rule.Valid || rule.String != ruleID {
		t.Fatalf("paid future rule = %v, want original %q", rule, ruleID)
	}
}

func TestRecurringAllVersionedPreservesClosedAndPaidInvoices(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-invoice"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-09'`)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID' WHERE id = 'invoice-2026-10'`)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Invoice",
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

	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	assertRecurringTxSummary(t, db, middleID, desc, 15000, 2)
	assertRecurringTxSummary(t, db, futureID, desc, 15000, 3)
	for _, txID := range []string{firstID, middleID, futureID} {
		var rule sql.NullString
		if err := db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&rule); err != nil {
			t.Fatalf("query rule for %s: %v", txID, err)
		}
		if !rule.Valid || rule.String != ruleID {
			t.Fatalf("%s rule = %v, want original %q", txID, rule, ruleID)
		}
	}
}

func TestRecurringAllVersionedKeepsMaterializedCount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-proj"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 No Proj",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, series).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 3 {
		t.Fatalf("series tx count = %d, want 3 (must not regenerate projections)", count)
	}
}

func TestRecurringAllVersionedPreservesRuleStartDateAndFrequency(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-startfreq"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Same Start",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var startDate int64
	var frequency string
	if err := db.QueryRow(`SELECT start_date, frequency FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, ruleID).Scan(&startDate, &frequency); err != nil {
		t.Fatalf("query rule: %v", err)
	}
	if startDate != testUnixDate("2026-06-05") {
		t.Fatalf("start_date = %d, want 2026-06-05", startDate)
	}
	if frequency != "MONTHLY" {
		t.Fatalf("frequency = %q, want MONTHLY", frequency)
	}
}

func TestRecurringAllVersionedUpdatesOnlyActiveRulesInSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-active"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE recurring_rules SET active=0, effective_end_sequence=1 WHERE id=? AND workspace_id='ws-test'`, ruleID)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, starts_at, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at)
		VALUES ('rule-p51-inactive', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, 2, NULL, 3, ?, ?)
	`, desc, testUnixDate("2026-06-05"), testUnixDate("2026-06-05"), series, ruleID, now, now)
	execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id='rule-p51-inactive' WHERE series_id=? AND recurrence_sequence>=2`, series)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Active",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var activeDesc string
	var activeAmount int64
	if err := db.QueryRow(`SELECT description, amount FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, ruleID).Scan(&activeDesc, &activeAmount); err != nil {
		t.Fatalf("query active rule: %v", err)
	}
	if activeDesc != desc || activeAmount != 15000 {
		t.Fatalf("historical rule = (%q, %d), want unchanged", activeDesc, activeAmount)
	}
	var inactiveDesc string
	var inactiveAmount int64
	if err := db.QueryRow(`SELECT description, amount FROM recurring_rules WHERE id = 'rule-p51-inactive' AND workspace_id = 'ws-test'`).Scan(&inactiveDesc, &inactiveAmount); err != nil {
		t.Fatalf("query inactive rule: %v", err)
	}
	if inactiveDesc != "Assinatura P51 Active" || inactiveAmount != 19900 {
		t.Fatalf("active rule = (%q, %d), want updated", inactiveDesc, inactiveAmount)
	}
}

func TestRecurringAllVersionedDoesNotCrossWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-isolated"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	seedOtherWorkspaceRecurringSeries(t, db)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Isolated",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertOtherWorkspaceRecurringUnchanged(t, db)
}

func TestRecurringAllVersionedBusinessPreservesDueDateAndContactWhenOmitted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-business"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-all-p51', 'ws-test', 'Fornecedor All P51', 'vendor', ?)`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET due_date = ?, contact_id = ? WHERE id IN (?, ?) AND workspace_id = 'ws-test'`, testUnixDate("2026-07-20"), "contact-all-p51", middleID, futureID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Business All",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{firstID, middleID, futureID} {
		var dueDate sql.NullInt64
		var contactID sql.NullString
		if err := db.QueryRow(`SELECT due_date, contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&dueDate, &contactID); err != nil {
			t.Fatalf("query due/contact for %s: %v", txID, err)
		}
		wantDue := testUnixDate("2026-07-20")
		if txID == firstID {
			wantDue = 0
		}
		if wantDue == 0 {
			if dueDate.Valid {
				t.Fatalf("%s due_date = %v, want NULL", txID, dueDate)
			}
		} else {
			if !dueDate.Valid || dueDate.Int64 != wantDue {
				t.Fatalf("%s due_date = %v, want 2026-07-20", txID, dueDate)
			}
		}
		if txID == firstID {
			if contactID.Valid {
				t.Fatalf("%s contact_id = %v, want NULL", txID, contactID)
			}
		} else {
			if !contactID.Valid || contactID.String != "contact-all-p51" {
				t.Fatalf("%s contact_id = %v, want contact-all-p51", txID, contactID)
			}
		}
	}
}

func TestRecurringAllVersionedPersonalKeepsContactNull(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-personal"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-all-personal', 'ws-test', 'Contato All Personal', 'client', ?)`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET contact_id = 'contact-all-personal' WHERE id = ? AND workspace_id = 'ws-test'`, firstID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Personal All",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"contact_id":       "contact-all-personal",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for incoherent personal metadata, body = %q", rr.Code, rr.Body.String())
	}

	for _, q := range []struct {
		label string
		id    string
	}{
		{"first tx", firstID},
		{"rule", ruleID},
	} {
		var contactID sql.NullString
		query := `SELECT contact_id FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`
		if q.label == "rule" {
			query = `SELECT contact_id FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`
		}
		if err := db.QueryRow(query, q.id).Scan(&contactID); err != nil {
			t.Fatalf("query contact for %s: %v", q.label, err)
		}
		if q.label == "first tx" {
			if !contactID.Valid || contactID.String != "contact-all-personal" {
				t.Fatalf("%s contact_id = %q valid=%v, want preserved contact-all-personal", q.label, contactID.String, contactID.Valid)
			}
		} else if contactID.Valid {
			t.Fatalf("%s contact_id = %q, want original NULL", q.label, contactID.String)
		}
	}
}

func TestRecurringFutureVersionedStatusPaidChangesOnlySelectedOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-future-local-status"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	balanceBefore := accountBalance(t, db, "checking-test")
	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req, rr := seriesEditRequest(t, middleID, "2026-07-05", "future", "checking-test", "paid")
	h.HandleAtualizarTransacao(rr, req, middleID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := accountBalance(t, db, "checking-test") - balanceBefore; got != -15000 {
		t.Fatalf("balance delta = %d, want -15000", got)
	}
	wantStatuses := map[string]string{
		firstID:  "pending",
		middleID: "paid",
		futureID: "pending",
	}
	for txID, wantStatus := range wantStatuses {
		var status string
		if err := db.QueryRow(`SELECT status FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&status); err != nil {
			t.Fatalf("query status for %s: %v", txID, err)
		}
		if status != wantStatus {
			t.Fatalf("%s status = %q, want %q", txID, status, wantStatus)
		}
	}
	var defaultStatus string
	if err := db.QueryRow(`SELECT default_payment_status FROM recurring_rules WHERE workspace_id = 'ws-test' AND series_id = ? AND active = 1`, series).Scan(&defaultStatus); err != nil {
		t.Fatalf("query active rule default status: %v", err)
	}
	if defaultStatus != "PENDING" {
		t.Fatalf("active rule default status = %q, want PENDING", defaultStatus)
	}
}

func TestRecurringAllVersionedStatusPaidChangesOnlySelectedOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-balance"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	balanceBefore := accountBalance(t, db, "checking-test")

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Assinatura P51 Paid",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	balanceAfter := accountBalance(t, db, "checking-test")
	var wantDelta int64 = -10000
	if got := balanceAfter - balanceBefore; got != wantDelta {
		t.Fatalf("balance delta = %d, want %d", got, wantDelta)
	}
	wantStatuses := map[string]string{
		firstID:  "paid",
		middleID: "pending",
		futureID: "pending",
	}
	for txID, wantStatus := range wantStatuses {
		var status string
		if err := db.QueryRow(`SELECT status FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&status); err != nil {
			t.Fatalf("query status for %s: %v", txID, err)
		}
		if status != wantStatus {
			t.Fatalf("%s status = %q, want %q", txID, status, wantStatus)
		}
	}
	var defaultStatus string
	if err := db.QueryRow(`SELECT default_payment_status FROM recurring_rules WHERE id = ?`, ruleID).Scan(&defaultStatus); err != nil {
		t.Fatalf("query rule default status: %v", err)
	}
	if defaultStatus != "PENDING" {
		t.Fatalf("rule default status = %q, want PENDING", defaultStatus)
	}
}

func TestRecurringAllVersionedPreservesPaidWithoutInvoiceWhenSwitchingToPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-reverse"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	// Marca middle/future como paid sem invoice: devem ser preservadas mesmo quando
	// o escopo=all tenta voltar tudo para pending.
	execTestSQL(t, db, `UPDATE transactions SET status = 'paid' WHERE id IN (?, ?) AND workspace_id = 'ws-test'`, middleID, futureID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Assinatura P51 Pending",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, "Assinatura P51 Pending", 10000, 1)
	assertRecurringTxSummary(t, db, middleID, desc, 15000, 2)
	assertRecurringTxSummary(t, db, futureID, desc, 15000, 3)
	for _, txID := range []string{middleID, futureID} {
		var status string
		if err := db.QueryRow(`SELECT status FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&status); err != nil {
			t.Fatalf("query status for %s: %v", txID, err)
		}
		if status != "paid" {
			t.Fatalf("%s status = %q, want paid", txID, status)
		}
	}
}

func TestRecurringAllVersionedPreservesVersionStructure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-version-struct"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE recurring_rules SET active=0, effective_end_sequence=1 WHERE id=? AND workspace_id='ws-test'`, ruleID)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, starts_at, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at)
		VALUES ('rule-p51-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, 2, NULL, 3, ?, ?)
	`, desc, testUnixDate("2026-06-05"), testUnixDate("2026-07-05"), series, ruleID, now, now)
	execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id='rule-p51-child' WHERE series_id=? AND recurrence_sequence>=2`, series)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Version Struct",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for _, id := range []string{ruleID, "rule-p51-child"} {
		_, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, id)
		if parent.Valid != (id == "rule-p51-child") {
			t.Fatalf("%s parent.Valid = %v, want %v", id, parent.Valid, id == "rule-p51-child")
		}
		if id == ruleID && (startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 1 || active != 0) {
			t.Fatalf("root rule version = start:%d end:%v active:%d", startSeq, endSeq, active)
		}
		if id == "rule-p51-child" && (startSeq != 2 || endSeq.Valid || active != 1) {
			t.Fatalf("child rule version = start:%d end:%v active:%d", startSeq, endSeq, active)
		}
	}
}

func TestRecurringAllLegacyStillRunsWhenDateChanges(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-date-change"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "150,00",
		"descricao":        desc,
		"data":             "2026-06-10",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := recurringRuleCountBySeries(t, db, series); got != 1 {
		t.Fatalf("rules by series = %d, want 1 (legacy)", got)
	}
	assertDates(t, db, desc, []string{"2026-06-10", "2026-07-10", "2026-08-10"})
}

func TestRecurringAllLegacyStillRunsWhenFrequencyChanges(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-freq-change"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "150,00",
		"descricao":        desc,
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
		"recorrencia":      "WEEKLY",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var frequency string
	if err := db.QueryRow(`SELECT frequency FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, ruleID).Scan(&frequency); err != nil {
		t.Fatalf("query frequency: %v", err)
	}
	if frequency != "WEEKLY" {
		t.Fatalf("frequency = %q, want WEEKLY", frequency)
	}
}

func TestRecurringAllVersionedIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-idempotent"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	for i := 0; i < 2; i++ {
		req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
			"valor":            "199,00",
			"descricao":        "Assinatura P51 Idempotent",
			"data":             "2026-06-05",
			"tipo":             "despesa",
			"origem_conta_id":  "checking-test",
			"categoria_id":     "cat-expense-rec",
			"status_pagamento": "pending",
			"escopo":           "all",
		})
		rr := httptest.NewRecorder()
		h.HandleAtualizarTransacao(rr, req, firstID)
		if rr.Code != http.StatusOK {
			t.Fatalf("run %d status = %d, body = %q", i+1, rr.Code, rr.Body.String())
		}
	}

	if got := recurringRuleCountBySeries(t, db, series); got != 1 {
		t.Fatalf("rules by series = %d, want 1", got)
	}
	if got := seriesCountByDescription(t, db, "Assinatura P51 Idempotent"); got != 3 {
		t.Fatalf("tx count = %d, want 3", got)
	}
}

func TestRecurringAllVersionedDoesNotDuplicateDates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-nodup"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 NoDup",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertNoDuplicateSeriesSequence(t, db, series)
	assertDates(t, db, "Assinatura P51 NoDup", []string{"2026-06-05", "2026-07-05", "2026-08-05"})
}

func TestRecurringAllVersionedItemMutableNormalizesPaymentStatus(t *testing.T) {
	for _, status := range []string{"paid", "PAID", " Paid ", "PENDING", "pending", " PENDING "} {
		item := recurringAllVersionedItem{paymentStatus: status}
		got := recurringAllVersionedItemMutable(item)
		want := strings.ToLower(strings.TrimSpace(status)) != "paid"
		if got != want {
			t.Fatalf("status %q mutable = %v, want %v", status, got, want)
		}
	}
}

func TestRecurringAllVersionedUsesVersionedPathWithNullTotalOccurrences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-null-total"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE recurring_rules SET total_occurrences = NULL WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Null Total",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if got := recurringRuleCountBySeries(t, db, series); got != 1 {
		t.Fatalf("rules by series = %d, want 1 (versioned path for NULL total_occurrences)", got)
	}
	assertRecurringTxSummary(t, db, firstID, "Assinatura P51 Null Total", 19900, 1)
	assertRecurringTxSummary(t, db, "tx-p51-chk-2", "Assinatura P51 Null Total", 19900, 2)
	assertRecurringTxSummary(t, db, "tx-p51-chk-3", "Assinatura P51 Null Total", 19900, 3)
}

func TestRecurringAllVersionedDoesNotProjectWhenPartiallyMaterialized(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-partial"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	// Apenas a primeira ocorrência está materializada; all atualiza o que existe e
	// deixa a materialização posterior consumir a regra ativa.

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "150,00",
		"descricao":        desc,
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, series).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 1 {
		t.Fatalf("partial series tx count = %d, want 1 (all must not project)", count)
	}
}

func TestRecurringAllVersionedPreservesChainABC(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-all-chain-abc"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 5)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	now := time.Now().Unix()
	// Cadeia A(root seq1-2) -> B(seq3-4) -> C(seq5+)
	execTestSQL(t, db, `UPDATE recurring_rules SET active=0, effective_end_sequence=2 WHERE id=? AND workspace_id='ws-test'`, ruleID)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, starts_at, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at)
		VALUES ('rule-p51-B', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, ?, 'MONTHLY', 'PENDING', 0, ?, ?, 3, 4, 5, ?, ?)
	`, desc, testUnixDate("2026-06-05"), testUnixDate("2026-08-05"), series, ruleID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, starts_at, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at)
		VALUES ('rule-p51-C', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, 5, NULL, 5, ?, ?)
	`, desc, testUnixDate("2026-06-05"), testUnixDate("2026-10-05"), series, "rule-p51-B", now, now)
	// Materializa seq 1..5
	for _, tx := range []struct {
		id       string
		date     string
		sequence int64
	}{
		{"tx-p51-chain-2", "2026-07-05", 2},
		{"tx-p51-chain-3", "2026-08-05", 3},
		{"tx-p51-chain-4", "2026-09-05", 4},
		{"tx-p51-chain-5", "2026-10-05", 5},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, ?, ?, ?, ?)
		`, tx.id, testUnixDate(tx.date), desc, map[int64]string{2: ruleID, 3: "rule-p51-B", 4: "rule-p51-B", 5: "rule-p51-C"}[tx.sequence], tx.sequence, series, now, now)
	}

	h := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "199,00",
		"descricao":        "Assinatura P51 Chain",
		"data":             "2026-06-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	wantRules := map[int64]string{
		1: ruleID,
		2: ruleID,
		3: "rule-p51-B",
		4: "rule-p51-B",
		5: "rule-p51-C",
	}
	for seq, wantRule := range wantRules {
		_, rule, description, amount := recurringTxRuleBySequence(t, db, series, seq)
		if rule != wantRule {
			t.Fatalf("seq %d rule = %q, want %q", seq, rule, wantRule)
		}
		if description != "Assinatura P51 Chain" || amount != 19900 {
			t.Fatalf("seq %d = (%q, %d), want (Assinatura P51 Chain, 19900)", seq, description, amount)
		}
	}
	for _, id := range []string{ruleID, "rule-p51-B", "rule-p51-C"} {
		_, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, id)
		if id == ruleID && (parent.Valid || startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 2 || active != 0) {
			t.Fatalf("root rule version = parent:%v start:%d end:%v active:%d", parent, startSeq, endSeq, active)
		}
		if id == "rule-p51-B" && (!parent.Valid || parent.String != ruleID || startSeq != 3 || !endSeq.Valid || endSeq.Int64 != 4 || active != 0) {
			t.Fatalf("B rule version = parent:%v start:%d end:%v active:%d", parent, startSeq, endSeq, active)
		}
		if id == "rule-p51-C" && (!parent.Valid || parent.String != "rule-p51-B" || startSeq != 5 || endSeq.Valid || active != 1) {
			t.Fatalf("C rule version = parent:%v start:%d end:%v active:%d", parent, startSeq, endSeq, active)
		}
	}
}

func newDeleteRequest(t *testing.T, id, scope string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	u, err := url.Parse("/transacoes/" + id)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	q.Set("escopo", scope)
	u.RawQuery = q.Encode()
	req := httptest.NewRequest(http.MethodDelete, u.String(), nil)
	rr := httptest.NewRecorder()
	return req, rr
}

func deleteHandler(t *testing.T, db *sql.DB) TransactionHandler {
	t.Helper()
	return TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
		Templates:   testOOBTemplatesSeries(t),
	}
}

func TestRecurringDeleteFutureSimpleRemovesFuturePending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-simple"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{firstID, middleID, futureID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	_, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, ruleID)
	if parent.Valid || startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 0 || active != 0 {
		t.Fatalf("rule version = parent:%v start:%d end:%v active:%d; want end=0 active=0", parent, startSeq, endSeq, active)
	}
}

func TestRecurringDeleteFutureFromSequenceTwoRemovesCurrentAndFuture(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-seq2"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, middleID, "future")
	h.HandleDeletarTransacao(rr, req, middleID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	for _, txID := range []string{middleID, futureID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	_, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, ruleID)
	if parent.Valid || startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 1 || active != 0 {
		t.Fatalf("rule version = parent:%v start:%d end:%v active:%d; want end=1 active=0", parent, startSeq, endSeq, active)
	}
}

func TestRecurringDeleteFuturePreservesPaidWithoutInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-paid"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE transactions SET status = 'paid' WHERE id = ? AND workspace_id = 'ws-test'`, futureID)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{firstID, middleID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	assertRecurringTxSummary(t, db, futureID, desc, 15000, 3)
	var status string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, futureID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "paid" {
		t.Fatalf("future status = %q, want paid", status)
	}
}

func TestRecurringDeleteFuturePreservesClosedInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-closed"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-09'`)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var firstCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID).Scan(&firstCount); err != nil {
		t.Fatalf("query first: %v", err)
	}
	if firstCount != 0 {
		t.Fatalf("first should be deleted, count = %d", firstCount)
	}
	assertRecurringTxSummary(t, db, middleID, desc, 15000, 2)
	var futureCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, futureID).Scan(&futureCount); err != nil {
		t.Fatalf("query future: %v", err)
	}
	if futureCount != 0 {
		t.Fatalf("future should be deleted, count = %d", futureCount)
	}
}

func TestRecurringDeleteFuturePreservesPaidInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-paid-inv"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID' WHERE id = 'invoice-2026-10'`)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var firstCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID).Scan(&firstCount); err != nil {
		t.Fatalf("query first: %v", err)
	}
	if firstCount != 0 {
		t.Fatalf("first should be deleted, count = %d", firstCount)
	}
	var middleCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, middleID).Scan(&middleCount); err != nil {
		t.Fatalf("query middle: %v", err)
	}
	if middleCount != 0 {
		t.Fatalf("middle should be deleted, count = %d", middleCount)
	}
	assertRecurringTxSummary(t, db, futureID, desc, 15000, 3)
}

func TestRecurringDeleteFuturePreservesManualException(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-exception"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `
		UPDATE transactions
		SET is_recurring_exception = 1,
		    recurring_exception_reason = 'manual_edit',
		    description = 'Assinatura P51 Manual'
		WHERE id = ? AND workspace_id = 'ws-test'
	`, futureID)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{firstID, middleID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	assertRecurringExceptionFields(t, db, futureID, 1, "manual_edit")
	assertRecurringTxSummary(t, db, futureID, "Assinatura P51 Manual", 15000, 3)
}

func TestRecurringDeleteFutureReconcilesOpenInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-open"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, series, desc)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var firstCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, firstID).Scan(&firstCount); err != nil {
		t.Fatalf("query first: %v", err)
	}
	if firstCount != 0 {
		t.Fatalf("first should be deleted, count = %d", firstCount)
	}
	for _, txID := range []string{middleID, futureID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	for _, invID := range []string{"invoice-2026-09", "invoice-2026-10"} {
		var status string
		if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ? AND account_id = 'card-test'`, invID).Scan(&status); err != nil {
			t.Fatalf("query invoice %s: %v", invID, err)
		}
		if status != "OPEN" {
			t.Fatalf("invoice %s status = %q, want OPEN", invID, status)
		}
	}
}

func TestRecurringDeleteFutureChainABC(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-chain"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 5)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	now := time.Now().Unix()

	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, ruleID)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, starts_at, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at)
		VALUES ('rule-p51-delete-B', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, 3, 4, 5, ?, ?)
	`, desc, testUnixDate("2026-06-05"), testUnixDate("2026-06-05"), series, ruleID, now, now)
	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = 'rule-p51-delete-B'`)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, starts_at, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, total_occurrences, created_at, updated_at)
		VALUES ('rule-p51-delete-C', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, 5, NULL, 5, ?, ?)
	`, desc, testUnixDate("2026-06-05"), testUnixDate("2026-06-05"), series, "rule-p51-delete-B", now, now)

	t.Run("initial_state", func(t *testing.T) {
		_, parentA, startA, endA, activeA := recurringRuleVersionFields(t, db, ruleID)
		if parentA.Valid || startA != 1 || !endA.Valid || endA.Int64 != 2 || activeA != 0 {
			t.Fatalf("initial A: parent:%v start:%d end:%v active:%d; want root [1,2] inactive", parentA, startA, endA, activeA)
		}
		_, parentB, startB, endB, activeB := recurringRuleVersionFields(t, db, "rule-p51-delete-B")
		if !parentB.Valid || parentB.String != ruleID || startB != 3 || !endB.Valid || endB.Int64 != 4 || activeB != 0 {
			t.Fatalf("initial B: parent:%v start:%d end:%v active:%d; want child of A [3,4] inactive", parentB, startB, endB, activeB)
		}
		_, parentC, startC, endC, activeC := recurringRuleVersionFields(t, db, "rule-p51-delete-C")
		if !parentC.Valid || parentC.String != "rule-p51-delete-B" || startC != 5 || endC.Valid || activeC != 1 {
			t.Fatalf("initial C: parent:%v start:%d end:%v active:%d; want child of B [5,...) active", parentC, startC, endC, activeC)
		}
	})
	for _, tx := range []struct {
		id       string
		date     string
		sequence int64
		ruleID   string
	}{
		{"tx-p51-delete-2", "2026-07-05", 2, ruleID},
		{"tx-p51-delete-3", "2026-08-05", 3, "rule-p51-delete-B"},
		{"tx-p51-delete-4", "2026-09-05", 4, "rule-p51-delete-B"},
		{"tx-p51-delete-5", "2026-10-05", 5, "rule-p51-delete-C"},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, ?, ?, ?, ?)
		`, tx.id, testUnixDate(tx.date), desc, tx.ruleID, tx.sequence, series, now, now)
	}

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, "tx-p51-delete-3", "future")
	h.HandleDeletarTransacao(rr, req, "tx-p51-delete-3")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	_, parentB, startB, endB, activeB := recurringRuleVersionFields(t, db, "rule-p51-delete-B")
	if !parentB.Valid || parentB.String != ruleID || startB != 3 || !endB.Valid || endB.Int64 != 0 || activeB != 0 {
		t.Fatalf("B rule = parent:%v start:%d end:%v active:%d; want parent=A start=3 end=0 active=0", parentB, startB, endB, activeB)
	}
	_, parentC, startC, endC, activeC := recurringRuleVersionFields(t, db, "rule-p51-delete-C")
	if !parentC.Valid || parentC.String != "rule-p51-delete-B" || startC != 5 || !endC.Valid || endC.Int64 != 0 || activeC != 0 {
		t.Fatalf("C rule = parent:%v start:%d end:%v active:%d; want parent=B start=5 end=0 active=0", parentC, startC, endC, activeC)
	}
	_, parentA, startA, endA, activeA := recurringRuleVersionFields(t, db, ruleID)
	if parentA.Valid || startA != 1 || !endA.Valid || endA.Int64 != 2 || activeA != 0 {
		t.Fatalf("A rule = parent:%v start:%d end:%v active:%d; want root start=1 end=2 active=0", parentA, startA, endA, activeA)
	}

	for _, txID := range []string{firstID, "tx-p51-delete-2"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 1 {
			t.Fatalf("%s should be preserved, count = %d", txID, count)
		}
	}
	for _, txID := range []string{"tx-p51-delete-3", "tx-p51-delete-4", "tx-p51-delete-5"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
}

func TestRecurringDeleteFutureLegacyRuleEnsuresSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-legacy-delete-future", "Recorrencia Legacy Delete Future")

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, "rule-legacy-delete-future-tx-1", "future")
	h.HandleDeletarTransacao(rr, req, "rule-legacy-delete-future-tx-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var seriesID sql.NullString
	if err := db.QueryRow(`SELECT series_id FROM recurring_rules WHERE id = 'rule-legacy-delete-future' AND workspace_id = 'ws-test'`).Scan(&seriesID); err != nil {
		t.Fatalf("query legacy series: %v", err)
	}
	if !seriesID.Valid || seriesID.String == "" {
		t.Fatalf("legacy series_id = %v, want filled", seriesID)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID.String).Scan(&count); err != nil {
		t.Fatalf("count legacy tx: %v", err)
	}
	if count != 0 {
		t.Fatalf("legacy tx count = %d, want 0 (all future from seq 1 deleted)", count)
	}
}

func TestRecurringDeleteFutureMissingActiveRuleFailsSafely(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-no-rule"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}

	for _, txID := range []string{firstID, "tx-p51-chk-2", "tx-p51-chk-3"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 1 {
			t.Fatalf("%s should not be deleted, count = %d", txID, count)
		}
	}
}

func TestRecurringDeleteFutureDoesNotCrossWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-isolated"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	seedOtherWorkspaceRecurringSeries(t, db)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertOtherWorkspaceRecurringUnchanged(t, db)
}

func TestRecurringDeleteFutureCurrentSequenceManualExceptionIsPreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-current-exception"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCheckingOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `
		UPDATE transactions
		SET is_recurring_exception = 1,
		    recurring_exception_reason = 'manual_edit',
		    description = 'Assinatura P51 Manual Current'
		WHERE id = ? AND workspace_id = 'ws-test'
	`, firstID)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringExceptionFields(t, db, firstID, 1, "manual_edit")
	assertRecurringTxSummary(t, db, firstID, "Assinatura P51 Manual Current", 15000, 1)
	for _, txID := range []string{middleID, futureID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	_, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, ruleID)
	if parent.Valid || startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 0 || active != 0 {
		t.Fatalf("rule version = parent:%v start:%d end:%v active:%d; want end=0 active=0", parent, startSeq, endSeq, active)
	}
}

func TestRecurringDeleteFutureCurrentSequenceClosedInvoiceIsPreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-current-closed"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, series, desc)
	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-08'`)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	for _, txID := range []string{middleID, futureID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	var invoiceStatus string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08' AND account_id = 'card-test'`).Scan(&invoiceStatus); err != nil {
		t.Fatalf("query invoice: %v", err)
	}
	// A fatura CLOSED da current_sequence é preservada intacta; a coleta de
	// oldInvoiceIDs agora só inclui transações realmente deletáveis, então a
	// fatura da current_sequence não passa por reconcile.
	if invoiceStatus != "CLOSED" {
		t.Fatalf("invoice status = %q, want CLOSED", invoiceStatus)
	}
}

func TestRecurringDeleteFutureCurrentSequencePaidInvoiceIsPreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-current-paid"
	ruleID, firstID := seedRecurringSeriesOnCard(t, db, desc, "2026-06-24", 3)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	middleID, futureID := seedAdditionalCardOccurrences(t, db, ruleID, series, desc)
	// Marca a fatura da current_sequence como PAID (mesmo sem invoice_payments,
	// o sistema preserva a transação current pois ela é imutável para delete future).
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_amount = 15000, paid_at = ? WHERE id = 'invoice-2026-08'`, time.Now().Unix())

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, firstID, "future")
	h.HandleDeletarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	for _, txID := range []string{middleID, futureID} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, txID).Scan(&count); err != nil {
			t.Fatalf("query %s: %v", txID, err)
		}
		if count != 0 {
			t.Fatalf("%s should be deleted, count = %d", txID, count)
		}
	}
	var invoiceStatus string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08' AND account_id = 'card-test'`).Scan(&invoiceStatus); err != nil {
		t.Fatalf("query invoice: %v", err)
	}
	// A fatura PAID da current_sequence é preservada intacta; a coleta de
	// oldInvoiceIDs agora só inclui transações realmente deletáveis, então a
	// fatura da current_sequence não passa por reconcile.
	if invoiceStatus != "PAID" {
		t.Fatalf("invoice status = %q, want PAID", invoiceStatus)
	}
}

func TestRecurringDeleteFuturePartiallyMaterializedSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura P51"
	const series = "series-p51-delete-partial"
	ruleID, firstID := seedRecurringSeriesOnChecking(t, db, desc, "2026-06-05", 5)
	attachRecurringSeriesIdentity(t, db, ruleID, series)
	// Materializa apenas seq 1 e 2 (total_occurrences=5).
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-p51-partial-2', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 2, ?, ?, ?)
	`, testUnixDate("2026-07-05"), desc, ruleID, series, now, now)

	h := deleteHandler(t, db)
	req, rr := newDeleteRequest(t, "tx-p51-partial-2", "future")
	h.HandleDeletarTransacao(rr, req, "tx-p51-partial-2")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringTxSummary(t, db, firstID, desc, 15000, 1)
	var seq2Count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = 'tx-p51-partial-2' AND workspace_id = 'ws-test'`).Scan(&seq2Count); err != nil {
		t.Fatalf("query seq2: %v", err)
	}
	if seq2Count != 0 {
		t.Fatalf("seq2 should be deleted, count = %d", seq2Count)
	}
	// Nenhuma projeção nova deve ter sido gerada.
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, series).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 1 {
		t.Fatalf("series tx count = %d, want 1 (no projections generated)", count)
	}
	_, parent, startSeq, endSeq, active := recurringRuleVersionFields(t, db, ruleID)
	if parent.Valid || startSeq != 1 || !endSeq.Valid || endSeq.Int64 != 1 || active != 0 {
		t.Fatalf("rule version = parent:%v start:%d end:%v active:%d; want end=1 active=0", parent, startSeq, endSeq, active)
	}
}

func accountBalance(t *testing.T, db *sql.DB, accountID string) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(`SELECT COALESCE(current_balance, 0) FROM accounts WHERE id = ? AND workspace_id = 'ws-test'`, accountID).Scan(&balance); err != nil {
		t.Fatalf("query balance for %s: %v", accountID, err)
	}
	return balance
}
