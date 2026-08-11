package handlers

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecurringProjectionNonCardRespectsDefaultPaymentStatusPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	totalOccurrences := int64(3)
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Pending",
		"",
		"", civilWriteDateFromLegacyUnix(time.Now().UTC().AddDate(0, -1, 0).Unix()), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring pending: %v", err)
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia Pending", []string{"pending", "pending", "pending"})
}

func TestRecurringProjectionNonCardKeepsFuturePendingWhenCurrentIsPaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	totalOccurrences := int64(3)
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Paid",
		"",
		"", civilWriteDateFromLegacyUnix(time.Now().UTC().AddDate(0, -1, 0).Unix()), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"paid",
		true,
		"MONTHLY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring paid: %v", err)
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia Paid", []string{"paid", "pending", "pending"})
	assertRecurringRuleDefaultStatus(t, db, "Recorrencia Paid", "PENDING")
}

func TestRecurringCreateCreatesSeriesAndPropagatesSeriesID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(3)
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Fase3 Series",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-01")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring series: %v", err)
	}

	ruleID, seriesID := recurringRuleSeriesByDescription(t, db, "ws-test", "Recorrencia Fase3 Series")
	assertRecurringSeriesRow(t, db, "ws-test", seriesID, "user-test", totalOccurrences)
	assertRecurringTransactionsCarrySeries(t, db, "ws-test", ruleID, seriesID, []int64{1, 2, 3})
}

func TestRecurringCreatePersonalContactNullAndCommonTransactionNoSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(2)
	_, err := handler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Personal Fase3",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-03")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction personal recurring: %v", err)
	}
	assertRecurringFirstContactNull(t, db, "Recorrencia Personal Fase3")

	_, err = handler.insertTransaction(
		"EXPENSE",
		9000,
		"Transacao Comum Fase3",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-04")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction common: %v", err)
	}
	assertCommonTransactionHasNoRecurringIdentity(t, db, "Transacao Comum Fase3")
}

func TestRecurringCreateBusinessPreservesDueDateAndContact(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, name, type, created_at)
		VALUES ('contact-fase3', 'ws-test', 'Fornecedor Fase3', 'vendor', ?)
	`, now)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(2)
	dueDate := MustCivilWriteDate("2026-07-20").LegacyUnix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		13000,
		"Recorrencia Business Fase3",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"contact-fase3", MustOptionalCivilWriteDate("2026-07-20"), &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction business recurring: %v", err)
	}

	var gotDue sql.NullInt64
	var gotContact sql.NullString
	if err := db.QueryRow(`
		SELECT due_date, contact_id
		FROM transactions
		WHERE workspace_id = 'ws-test'
		  AND description = 'Recorrencia Business Fase3'
		  AND recurrence_sequence = 1
	`).Scan(&gotDue, &gotContact); err != nil {
		t.Fatalf("query business recurring first occurrence: %v", err)
	}
	if !gotDue.Valid || gotDue.Int64 != dueDate {
		t.Fatalf("due_date = %d valid=%v, want %d", gotDue.Int64, gotDue.Valid, dueDate)
	}
	if !gotContact.Valid || gotContact.String != "contact-fase3" {
		t.Fatalf("contact_id = %q valid=%v, want contact-fase3", gotContact.String, gotContact.Valid)
	}
}

func TestRecurringCreateWorkspaceIsolationSeriesID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceForRecurringCreate(t, db)

	totalOccurrences := int64(2)
	handlerA := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	if _, err := handlerA.insertTransaction("EXPENSE", 11000, "Recorrencia Isolada Fase3", "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-06")), "checking-test", "", "cat-expense-rec", 1, "pending", true, "MONTHLY", "", OptionalCivilWriteDate{}, &totalOccurrences, "", false); err != nil {
		t.Fatalf("insert workspace A recurring: %v", err)
	}
	handlerB := TransactionHandler{DB: db, WorkspaceID: "ws-other", UserID: "user-other"}
	if _, err := handlerB.insertTransaction("EXPENSE", 11000, "Recorrencia Isolada Fase3", "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-06")), "checking-other", "", "cat-expense-other", 1, "pending", true, "MONTHLY", "", OptionalCivilWriteDate{}, &totalOccurrences, "", false); err != nil {
		t.Fatalf("insert workspace B recurring: %v", err)
	}

	_, seriesA := recurringRuleSeriesByDescription(t, db, "ws-test", "Recorrencia Isolada Fase3")
	_, seriesB := recurringRuleSeriesByDescription(t, db, "ws-other", "Recorrencia Isolada Fase3")
	if seriesA == "" || seriesB == "" || seriesA == seriesB {
		t.Fatalf("series isolation failed: ws-test=%q ws-other=%q", seriesA, seriesB)
	}
	assertRecurringSeriesWorkspace(t, db, seriesA, "ws-test")
	assertRecurringSeriesWorkspace(t, db, seriesB, "ws-other")
}

func TestRecurringCardCreatePropagatesSeriesIDToMaterializedOccurrences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(3)
	if _, err := handler.insertTransaction("EXPENSE", 14000, "Recorrencia Cartao Fase3", "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-07")), "card-test", "", "cat-expense-rec", 1, "paid", true, "MONTHLY", "", OptionalCivilWriteDate{}, &totalOccurrences, "", false); err != nil {
		t.Fatalf("insert card recurring: %v", err)
	}

	ruleID, seriesID := recurringRuleSeriesByDescription(t, db, "ws-test", "Recorrencia Cartao Fase3")
	assertRecurringTransactionsCarrySeries(t, db, "ws-test", ruleID, seriesID, []int64{1, 2, 3})
	assertRecurringTransactionsHaveInvoices(t, db, "ws-test", ruleID, 3)
}

func TestRecurringCreateSingleEditMarksExceptionAndPreservesSeriesID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(3)
	if _, err := handler.insertTransaction("EXPENSE", 15000, "Recorrencia Single Nova Fase3", "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-08")), "checking-test", "", "cat-expense-rec", 1, "pending", true, "MONTHLY", "", OptionalCivilWriteDate{}, &totalOccurrences, "", false); err != nil {
		t.Fatalf("insert recurring for single edit: %v", err)
	}
	ids := recurringIDsByDescription(t, db, "Recorrencia Single Nova Fase3")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}
	ruleID, seriesID := recurringRuleSeriesByDescription(t, db, "ws-test", "Recorrencia Single Nova Fase3")

	updateHandler := TransactionHandler{DB: db, Templates: testOOBTemplatesSeries(t), WorkspaceID: "ws-test", UserID: "user-test"}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[1], map[string]string{
		"valor":            "155,00",
		"descricao":        "Recorrencia Single Nova Fase3 Editada",
		"data":             "2026-08-08",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "single",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, ids[1])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringExceptionFields(t, db, ids[1], 1, "manual_edit")
	assertRecurringIdentity(t, db, ids[1], ruleID, seriesID, 2)
	assertRecurringTxSummary(t, db, ids[0], "Recorrencia Single Nova Fase3", 15000, 1)
	assertRecurringTxSummary(t, db, ids[2], "Recorrencia Single Nova Fase3", 15000, 3)
}

func TestRecurringCreateFromExistingTransactionCreatesSeriesWithoutDuplicateSequence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	if _, err := handler.insertTransaction("EXPENSE", 16000, "Transacao Para Fixo Fase3", "", "", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-09")), "checking-test", "", "cat-expense-rec", 1, "pending", false, "", "", OptionalCivilWriteDate{}, nil, "", false); err != nil {
		t.Fatalf("insert common transaction for conversion: %v", err)
	}
	ids := recurringIDsByDescription(t, db, "Transacao Para Fixo Fase3")
	if len(ids) != 1 {
		t.Fatalf("ids = %v, want one common transaction", ids)
	}

	updateHandler := TransactionHandler{DB: db, Templates: testOOBTemplatesSeries(t), WorkspaceID: "ws-test", UserID: "user-test"}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[0], map[string]string{
		"valor":               "160,00",
		"descricao":           "Transacao Para Fixo Fase3",
		"data":                "2026-07-09",
		"tipo":                "despesa",
		"origem_conta_id":     "checking-test",
		"categoria_id":        "cat-expense-rec",
		"status_pagamento":    "pending",
		"lancamento_fixo":     "on",
		"recorrencia":         "MONTHLY",
		"escopo":              "single",
		"escopo_edicao":       "single",
		"fatura_offset":       "auto",
		"allow_box_overdraft": "",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	ruleID, seriesID := recurringRuleSeriesByDescription(t, db, "ws-test", "Transacao Para Fixo Fase3")
	assertRecurringIdentity(t, db, ids[0], ruleID, seriesID, 1)
	assertNoDuplicateRecurringSequences(t, db, "ws-test", ruleID)
}

func TestRecurringLegacyEnsureSeriesBackfillsRuleAndTransactions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-legacy-fase31", "Recorrencia Legacy Fase31")

	seriesID := ensureRecurringSeriesForRuleInTest(t, db, "ws-test", "rule-legacy-fase31")
	if seriesID != "rule-legacy-fase31" {
		t.Fatalf("seriesID = %q, want rule-legacy-fase31", seriesID)
	}
	assertRecurringSeriesWorkspace(t, db, seriesID, "ws-test")
	assertRecurringTransactionsCarrySeries(t, db, "ws-test", "rule-legacy-fase31", seriesID, []int64{1, 2})
	assertLegacyRecurringFinancialFieldsUnchanged(t, db, "ws-test", "rule-legacy-fase31")
}

func TestRecurringLegacyEnsureSeriesIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-legacy-idempotent", "Recorrencia Legacy Idempotente")

	first := ensureRecurringSeriesForRuleInTest(t, db, "ws-test", "rule-legacy-idempotent")
	second := ensureRecurringSeriesForRuleInTest(t, db, "ws-test", "rule-legacy-idempotent")
	if first != second {
		t.Fatalf("series IDs = %q/%q, want same", first, second)
	}
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM recurring_series
		WHERE workspace_id = 'ws-test' AND id = ?
	`, first).Scan(&count); err != nil {
		t.Fatalf("count recurring series: %v", err)
	}
	if count != 1 {
		t.Fatalf("recurring_series count = %d, want 1", count)
	}
}

func TestRecurringLegacyEnsureSeriesDoesNotCrossWorkspaces(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceForRecurringCreate(t, db)
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-legacy-local", "Recorrencia Legacy Parecida")
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-other", "user-other", "checking-other", "cat-expense-other", "rule-legacy-other", "Recorrencia Legacy Parecida")

	seriesID := ensureRecurringSeriesForRuleInTest(t, db, "ws-test", "rule-legacy-local")
	assertRecurringSeriesWorkspace(t, db, seriesID, "ws-test")

	var otherRuleSeries sql.NullString
	if err := db.QueryRow(`
		SELECT series_id
		FROM recurring_rules
		WHERE workspace_id = 'ws-other' AND id = 'rule-legacy-other'
	`).Scan(&otherRuleSeries); err != nil {
		t.Fatalf("query other workspace legacy rule: %v", err)
	}
	if otherRuleSeries.Valid {
		t.Fatalf("other workspace rule series_id = %q, want NULL", otherRuleSeries.String)
	}
	assertLegacyTransactionsHaveNoSeries(t, db, "ws-other", "rule-legacy-other")
}

func TestRecurringLegacyEnsureSeriesKeepsExistingSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES ('series-existing-fase31', 'ws-test', 'user-test', 1, 2, ?, ?)
	`, now, now)
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-existing-series-fase31", "Recorrencia Legacy Series Existente")
	execTestSQL(t, db, `
		UPDATE recurring_rules
		SET series_id = 'series-existing-fase31'
		WHERE workspace_id = 'ws-test' AND id = 'rule-existing-series-fase31'
	`)

	seriesID := ensureRecurringSeriesForRuleInTest(t, db, "ws-test", "rule-existing-series-fase31")
	if seriesID != "series-existing-fase31" {
		t.Fatalf("seriesID = %q, want series-existing-fase31", seriesID)
	}
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM recurring_series
		WHERE workspace_id = 'ws-test'
	`).Scan(&count); err != nil {
		t.Fatalf("count recurring series: %v", err)
	}
	if count != 1 {
		t.Fatalf("recurring_series count = %d, want 1", count)
	}
	assertRecurringTransactionsCarrySeries(t, db, "ws-test", "rule-existing-series-fase31", seriesID, []int64{1, 2})
}

// TestRecurringLegacyEnsureSeriesRejectsForeignWorkspaceSeries garante o isolamento
// multi-tenant do helper: uma recurring_rule do workspace A que aponte para uma
// recurring_series do workspace B deve falhar com erro genérico, sem vazar o workspace
// estrangeiro, sem preencher series_id estrangeiro em transactions do workspace A e sem
// alterar o workspace B.
func TestRecurringLegacyEnsureSeriesRejectsForeignWorkspaceSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceForRecurringCreate(t, db)

	now := time.Now().Unix()
	// Série pertencente ao workspace B (ws-other).
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES ('series-foreign-b', 'ws-other', 'user-other', 1, 2, ?, ?)
	`, now, now)
	// Regra do workspace A (ws-test) apontando para série estrangeira.
	seedLegacyRecurringRuleWithoutSeries(t, db, "ws-test", "user-test", "checking-test", "cat-expense-rec", "rule-a-points-foreign", "Recorrencia Cross Tenant")
	execTestSQL(t, db, `
		UPDATE recurring_rules
		SET series_id = 'series-foreign-b'
		WHERE workspace_id = 'ws-test' AND id = 'rule-a-points-foreign'
	`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	_, ensureErr := EnsureRecurringSeriesForRuleTx(tx, "ws-test", "rule-a-points-foreign", now)
	if ensureErr == nil {
		tx.Rollback()
		t.Fatalf("expected error for foreign workspace series, got nil")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback tx: %v", err)
	}

	// A mensagem de erro não deve vazar o workspace estrangeiro nem o id da série.
	if strings.Contains(ensureErr.Error(), "ws-other") {
		t.Fatalf("error message leaks foreign workspace: %q", ensureErr.Error())
	}
	if strings.Contains(ensureErr.Error(), "series-foreign-b") {
		t.Fatalf("error message leaks foreign series id: %q", ensureErr.Error())
	}

	// Nenhuma transaction do workspace A foi preenchida com series_id estrangeiro.
	assertLegacyTransactionsHaveNoSeries(t, db, "ws-test", "rule-a-points-foreign")

	// Workspace B não foi alterado: série continua no ws-other.
	assertRecurringSeriesWorkspace(t, db, "series-foreign-b", "ws-other")
	var otherSeriesCount int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM recurring_series
		WHERE id = 'series-foreign-b' AND workspace_id = 'ws-other'
	`).Scan(&otherSeriesCount); err != nil {
		t.Fatalf("count foreign series: %v", err)
	}
	if otherSeriesCount != 1 {
		t.Fatalf("foreign series count = %d, want 1", otherSeriesCount)
	}
	// Nenhuma série órfã foi criada no workspace A.
	var localSeriesCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id = 'ws-test'`).Scan(&localSeriesCount); err != nil {
		t.Fatalf("count local series: %v", err)
	}
	if localSeriesCount != 0 {
		t.Fatalf("ws-test recurring_series count = %d, want 0", localSeriesCount)
	}
}

func TestRecurringDailyTogglePaymentOnlySelectedOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Diaria Toggle",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-01")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring daily: %v", err)
	}

	ids := recurringIDsByDescription(t, db, "Recorrencia Diaria Toggle")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}

	toggleHandler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := formPost("/transacoes/"+ids[0]+"/status-pagamento", url.Values{
		"payment_action": {"paid"},
	})
	rr := httptest.NewRecorder()
	toggleHandler.HandleTogglePagamento(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle paid status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertRecurringStatusesByDescription(t, db, "Recorrencia Diaria Toggle", []string{"paid", "pending", "pending"})

	req = formPost("/transacoes/"+ids[0]+"/status-pagamento", url.Values{
		"payment_action": {"pending"},
	})
	rr = httptest.NewRecorder()
	toggleHandler.HandleTogglePagamento(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle pending status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertRecurringStatusesByDescription(t, db, "Recorrencia Diaria Toggle", []string{"pending", "pending", "pending"})
}

func TestInstallmentSeriesUpdateUsesAmountPerInstallment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		30000,
		"Serie Parcelada Base",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
		"",
		"cat-expense-rec",
		3,
		"pending",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction installment series: %v", err)
	}

	var rootID string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Serie Parcelada Base' AND installment_number = 1
	`).Scan(&rootID); err != nil {
		t.Fatalf("query root installment: %v", err)
	}
	var secondID string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND parent_id = ? AND installment_number = 2
	`, rootID).Scan(&secondID); err != nil {
		t.Fatalf("query second installment: %v", err)
	}

	updateHandler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+secondID+"/salvar?escopo=future", map[string]string{
		"valor":            "150,00",
		"descricao":        "Serie Parcelada Atualizada",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, secondID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertInstallmentAmount(t, db, rootID, 1, 10000)
	assertInstallmentAmount(t, db, rootID, 2, 15000)
	assertInstallmentAmount(t, db, rootID, 3, 15000)
}

func TestInsertTransactionInstallmentsDistributeRemainderToFirstInstallment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		txType     string
		categoryID string
	}{
		{name: "expense", txType: "EXPENSE", categoryID: "cat-expense-direct"},
		{name: "income", txType: "INCOME", categoryID: "cat-income"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			now := time.Now().Unix()
			execTestSQL(t, db, `
				INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
				VALUES
					('cat-expense-direct', 'ws-test', 'Despesa parcelada', 'receipt', '#6b7280', 'EXPENSE', ?),
					('cat-income', 'ws-test', 'Receita parcelada', 'wallet', '#22c55e', 'INCOME', ?)
			`, now, now)

			handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
			description := "Parcelamento 20000 em 3 " + tc.name
			_, err := handler.insertTransaction(
				tc.txType,
				2000000,
				description,
				"",
				"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
				"",
				tc.categoryID,
				3,
				"pending",
				false,
				"",
				"", OptionalCivilWriteDate{}, nil,
				"",
				false,
			)
			if err != nil {
				t.Fatalf("insertTransaction: %v", err)
			}

			rows, err := db.Query(`
				SELECT amount
				FROM transactions
				WHERE workspace_id = 'ws-test' AND description = ?
				ORDER BY installment_number
			`, description)
			if err != nil {
				t.Fatalf("query installments: %v", err)
			}
			defer rows.Close()

			var got []int64
			var sum int64
			for rows.Next() {
				var amount int64
				if err := rows.Scan(&amount); err != nil {
					t.Fatalf("scan installment: %v", err)
				}
				got = append(got, amount)
				sum += amount
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate installments: %v", err)
			}
			want := []int64{666668, 666666, 666666}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("installment amounts = %v, want %v", got, want)
			}
			if sum != 2000000 {
				t.Fatalf("installment sum = %d, want 2000000", sum)
			}
		})
	}
}

func TestRecurringEditBroadScopeRespondsWithListRefreshHeaders(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Mensal Refresh",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	var firstID string
	if err := db.QueryRow(`
		SELECT id FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Recorrencia Mensal Refresh'
		ORDER BY date ASC LIMIT 1
	`).Scan(&firstID); err != nil {
		t.Fatalf("query first recurring tx: %v", err)
	}

	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Recorrencia Mensal Refresh Editada",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if rr.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("HX-Reswap = %q, want %q", rr.Header().Get("HX-Reswap"), "none")
	}
	trigger := rr.Header().Get("HX-Trigger")
	if trigger != "refreshLancamentosList" {
		t.Fatalf("HX-Trigger = %q, want %q", trigger, "refreshLancamentosList")
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="bottom-sheet-container"`) {
		t.Fatalf("body missing bottom-sheet-container OOB")
	}
	if !strings.Contains(body, `id="lancamento-form-error"`) {
		t.Fatalf("body missing lancamento-form-error OOB")
	}
}

func TestRecurringEditAllScopeUpdatesAllOccurrenceStatuses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia All Status",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	firstID := recurringIDsByDescription(t, db, "Recorrencia All Status")[0]
	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Recorrencia All Status",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia All Status", []string{"paid", "paid", "paid"})
	assertRecurringRuleDefaultStatus(t, db, "Recorrencia All Status", "PAID")
}

func TestRecurringEditFutureScopeDoesNotChangePastOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Future Scope",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-01")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	ids := recurringIDsByDescription(t, db, "Recorrencia Future Scope")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}
	var sourceRuleID, seriesID string
	var currentSequence int64
	if err := db.QueryRow(`
		SELECT recurring_rule_id, series_id, recurrence_sequence
		FROM transactions
		WHERE id = ? AND workspace_id = 'ws-test'
	`, ids[1]).Scan(&sourceRuleID, &seriesID, &currentSequence); err != nil {
		t.Fatalf("query source future occurrence: %v", err)
	}
	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[1], map[string]string{
		"valor":            "110,00",
		"descricao":        "Recorrencia Future Scope Atualizada",
		"data":             "2026-07-02",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, ids[1])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringDescriptionsAndStatuses(t, db, []struct {
		id          string
		description string
		status      string
	}{
		{ids[0], "Recorrencia Future Scope", "pending"},
		{ids[1], "Recorrencia Future Scope Atualizada", "paid"},
		{ids[2], "Recorrencia Future Scope Atualizada", "paid"},
	})

	var sourceActive int
	var sourceEnd sql.NullInt64
	if err := db.QueryRow(`
		SELECT active, effective_end_sequence
		FROM recurring_rules
		WHERE id = ? AND workspace_id = 'ws-test'
	`, sourceRuleID).Scan(&sourceActive, &sourceEnd); err != nil {
		t.Fatalf("query closed source rule: %v", err)
	}
	if sourceActive != 0 || !sourceEnd.Valid || sourceEnd.Int64 != currentSequence-1 {
		t.Fatalf("source active=%d end=%v, want inactive ending at %d", sourceActive, sourceEnd, currentSequence-1)
	}

	var childRuleID, childWorkspaceID string
	var childParent sql.NullString
	var childStart int64
	var childOffset sql.NullInt64
	var childPolicy sql.NullString
	if err := db.QueryRow(`
		SELECT id, workspace_id, parent_rule_id, effective_start_sequence,
		       due_date_offset_days, due_date_policy
		FROM recurring_rules
		WHERE workspace_id = 'ws-test' AND series_id = ? AND active = 1
	`, seriesID).Scan(&childRuleID, &childWorkspaceID, &childParent, &childStart, &childOffset, &childPolicy); err != nil {
		t.Fatalf("query active child rule: %v", err)
	}
	if childRuleID == sourceRuleID || childWorkspaceID != "ws-test" || !childParent.Valid || childParent.String != sourceRuleID || childStart != currentSequence {
		t.Fatalf("child id=%q workspace=%q parent=%v start=%d; source=%q current=%d", childRuleID, childWorkspaceID, childParent, childStart, sourceRuleID, currentSequence)
	}
	if childOffset.Valid || childPolicy.Valid {
		t.Fatalf("paid child due metadata offset=%v policy=%v, want NULL/NULL", childOffset, childPolicy)
	}

	var activeRules, futureRowsWithDue, duplicateSequences int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recurring_rules WHERE workspace_id = 'ws-test' AND series_id = ? AND active = 1`, seriesID).Scan(&activeRules); err != nil {
		t.Fatalf("count active child rules: %v", err)
	}
	if activeRules != 1 {
		t.Fatalf("active rules = %d, want 1", activeRules)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM transactions
		WHERE workspace_id = 'ws-test' AND series_id = ? AND recurrence_sequence >= ?
		  AND (due_date IS NOT NULL OR due_date_civil IS NOT NULL)
	`, seriesID, currentSequence).Scan(&futureRowsWithDue); err != nil {
		t.Fatalf("count future rows with due date: %v", err)
	}
	if futureRowsWithDue != 0 {
		t.Fatalf("future rows with due date = %d, want 0", futureRowsWithDue)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM (
			SELECT recurrence_sequence
			FROM transactions
			WHERE workspace_id = 'ws-test' AND series_id = ?
			GROUP BY recurrence_sequence
			HAVING COUNT(*) > 1
		)
	`, seriesID).Scan(&duplicateSequences); err != nil {
		t.Fatalf("count duplicate future sequences: %v", err)
	}
	if duplicateSequences != 0 {
		t.Fatalf("duplicate sequences = %d, want 0", duplicateSequences)
	}
}

func TestRecurringEditAllScopeDoesNotChangeOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceRecurringSeries(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Workspace Local",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	firstID := recurringIDsByDescription(t, db, "Recorrencia Workspace Local")[0]
	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "130,00",
		"descricao":        "Recorrencia Workspace Local",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia Workspace Local", []string{"paid", "paid", "paid"})
	assertOtherWorkspaceRecurringUnchanged(t, db)
}

func TestRecurringEditSingleScopeKeepsRowSwap(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Single Scope",
		"",
		"", civilWriteDateFromLegacyUnix(testUnixDate("2026-07-05")), "checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"", OptionalCivilWriteDate{}, &totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	var firstID string
	if err := db.QueryRow(`
		SELECT id FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Recorrencia Single Scope'
		ORDER BY date ASC LIMIT 1
	`).Scan(&firstID); err != nil {
		t.Fatalf("query first recurring tx: %v", err)
	}
	ids := recurringIDsByDescription(t, db, "Recorrencia Single Scope")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}

	updateHandler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Recorrencia Single Scope Editada",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if rr.Header().Get("HX-Reswap") == "none" {
		t.Fatalf("single scope should NOT have HX-Reswap: none; row swap must be preserved")
	}
	if rr.Header().Get("HX-Trigger") == "refreshLancamentosList" {
		t.Fatalf("single scope should NOT trigger refreshLancamentosList")
	}

	body := rr.Body.String()
	if !strings.Contains(body, firstID) {
		t.Fatalf("single scope response body missing row ID %q in: %q", firstID, body)
	}
	assertRecurringDescriptionsAndStatuses(t, db, []struct {
		id          string
		description string
		status      string
	}{
		{ids[0], "Recorrencia Single Scope Editada", "pending"},
		{ids[1], "Recorrencia Single Scope", "pending"},
		{ids[2], "Recorrencia Single Scope", "pending"},
	})
	assertRecurringRuleDefaultStatus(t, db, "Recorrencia Single Scope", "PENDING")
}

func seedRecurringExpenseCategory(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense-rec', 'ws-test', 'Assinaturas', 'repeat', '#6b7280', 'EXPENSE', ?)
	`, now)
}

func seedOtherWorkspaceForRecurringCreate(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-other', 'User Other', 'user-other@example.com', 'hash', ?, ?)
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
		VALUES ('checking-other', 'ws-other', 'Conta Other', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense-other', 'ws-other', 'Assinaturas Other', 'repeat', '#6b7280', 'EXPENSE', ?)
	`, now)
}

func seedOtherWorkspaceRecurringSeries(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-other', 'User Other', 'user-other@example.com', 'hash', ?, ?)
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
		VALUES ('checking-other', 'ws-other', 'Conta Other', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense-other', 'ws-other', 'Assinaturas Other', 'repeat', '#6b7280', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
		VALUES ('rule-other', 'ws-other', 'user-other', 'checking-other', 'cat-expense-other', 'EXPENSE', 9900, 'Recorrencia Workspace Other', ?, 'DAILY', 'PENDING', 1, 3, ?, ?)
	`, testUnixDate("2026-07-05"), now, now)
	for i, id := range []string{"other-rec-1", "other-rec-2", "other-rec-3"} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
			VALUES (?, 'ws-other', 'user-other', 'checking-other', 'cat-expense-other', 'EXPENSE', 9900, ?, 'Recorrencia Workspace Other', 'pending', 1, 1, 'rule-other', ?, ?, ?)
		`, id, testUnixDate("2026-07-05")+int64(i*86400), int64(i+1), now, now)
	}
}

func seedLegacyRecurringRuleWithoutSeries(t *testing.T, db *sql.DB, workspaceID, userID, accountID, categoryID, ruleID, description string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', 9900, ?, ?, 'MONTHLY', 'PENDING', 1, 2, ?, ?)
	`, ruleID, workspaceID, userID, accountID, categoryID, description, testUnixDate("2026-07-05"), now, now)
	for seq, id := range []string{ruleID + "-tx-1", ruleID + "-tx-2"} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'EXPENSE', 9900, ?, ?, 'pending', 1, 1, ?, ?, ?, ?)
		`, id, workspaceID, userID, accountID, categoryID, testUnixDate("2026-07-05")+int64(seq)*30*86400, description, ruleID, int64(seq+1), now, now)
	}
}

func ensureRecurringSeriesForRuleInTest(t *testing.T, db *sql.DB, workspaceID, ruleID string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	seriesID, err := EnsureRecurringSeriesForRuleTx(tx, workspaceID, ruleID, time.Now().Unix())
	if err != nil {
		t.Fatalf("EnsureRecurringSeriesForRuleTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	return seriesID
}

func recurringRuleSeriesByDescription(t *testing.T, db *sql.DB, workspaceID, description string) (ruleID, seriesID string) {
	t.Helper()
	if err := db.QueryRow(`
		SELECT id, COALESCE(series_id, '')
		FROM recurring_rules
		WHERE workspace_id = ? AND description = ?
	`, workspaceID, description).Scan(&ruleID, &seriesID); err != nil {
		t.Fatalf("query recurring rule series for %s/%s: %v", workspaceID, description, err)
	}
	if ruleID == "" || seriesID == "" {
		t.Fatalf("recurring rule identity incomplete: ruleID=%q seriesID=%q", ruleID, seriesID)
	}
	return ruleID, seriesID
}

func assertRecurringSeriesRow(t *testing.T, db *sql.DB, workspaceID, seriesID, wantUserID string, wantTotal int64) {
	t.Helper()
	var gotWorkspaceID, gotUserID string
	var active int
	var total sql.NullInt64
	if err := db.QueryRow(`
		SELECT workspace_id, COALESCE(user_id, ''), active, total_occurrences
		FROM recurring_series
		WHERE id = ? AND workspace_id = ?
	`, seriesID, workspaceID).Scan(&gotWorkspaceID, &gotUserID, &active, &total); err != nil {
		t.Fatalf("query recurring series row: %v", err)
	}
	if gotWorkspaceID != workspaceID || gotUserID != wantUserID || active != 1 || !total.Valid || total.Int64 != wantTotal {
		t.Fatalf("series row = workspace:%q user:%q active:%d total:%d valid=%v; want workspace:%q user:%q active:1 total:%d", gotWorkspaceID, gotUserID, active, total.Int64, total.Valid, workspaceID, wantUserID, wantTotal)
	}
}

func assertLegacyRecurringFinancialFieldsUnchanged(t *testing.T, db *sql.DB, workspaceID, ruleID string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT amount, status, invoice_id, recurrence_sequence
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ?
		ORDER BY recurrence_sequence ASC
	`, workspaceID, ruleID)
	if err != nil {
		t.Fatalf("query legacy recurring financial fields: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var amount int64
		var status string
		var invoiceID sql.NullString
		var sequence int64
		if err := rows.Scan(&amount, &status, &invoiceID, &sequence); err != nil {
			t.Fatalf("scan legacy recurring financial fields: %v", err)
		}
		if amount != 9900 || status != "pending" || invoiceID.Valid || (sequence != 1 && sequence != 2) {
			t.Fatalf("legacy recurring changed: amount=%d status=%q invoice=%q valid=%v sequence=%d", amount, status, invoiceID.String, invoiceID.Valid, sequence)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate legacy recurring financial fields: %v", err)
	}
}

func assertLegacyTransactionsHaveNoSeries(t *testing.T, db *sql.DB, workspaceID, ruleID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ? AND series_id IS NOT NULL
	`, workspaceID, ruleID).Scan(&count); err != nil {
		t.Fatalf("count legacy transactions with series: %v", err)
	}
	if count != 0 {
		t.Fatalf("legacy transactions with series_id = %d, want 0", count)
	}
}

func assertRecurringTransactionsCarrySeries(t *testing.T, db *sql.DB, workspaceID, ruleID, seriesID string, wantSequences []int64) {
	t.Helper()
	rows, err := db.Query(`
		SELECT recurrence_sequence, COALESCE(series_id, '')
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ?
		ORDER BY recurrence_sequence ASC
	`, workspaceID, ruleID)
	if err != nil {
		t.Fatalf("query recurring transactions series: %v", err)
	}
	defer rows.Close()

	var gotSequences []int64
	for rows.Next() {
		var seq sql.NullInt64
		var gotSeriesID string
		if err := rows.Scan(&seq, &gotSeriesID); err != nil {
			t.Fatalf("scan recurring transaction series: %v", err)
		}
		if !seq.Valid {
			t.Fatalf("recurring transaction without recurrence_sequence")
		}
		if gotSeriesID != seriesID {
			t.Fatalf("transaction seq %d series_id = %q, want %q", seq.Int64, gotSeriesID, seriesID)
		}
		gotSequences = append(gotSequences, seq.Int64)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recurring transaction series: %v", err)
	}
	if !reflect.DeepEqual(gotSequences, wantSequences) {
		t.Fatalf("recurrence sequences = %v, want %v", gotSequences, wantSequences)
	}
}

func assertRecurringFirstContactNull(t *testing.T, db *sql.DB, description string) {
	t.Helper()
	var contactID sql.NullString
	if err := db.QueryRow(`
		SELECT contact_id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ? AND recurrence_sequence = 1
	`, description).Scan(&contactID); err != nil {
		t.Fatalf("query recurring first contact: %v", err)
	}
	if contactID.Valid {
		t.Fatalf("contact_id = %q, want NULL", contactID.String)
	}
}

func assertCommonTransactionHasNoRecurringIdentity(t *testing.T, db *sql.DB, description string) {
	t.Helper()
	var ruleID, seriesID sql.NullString
	var seq sql.NullInt64
	var isException int
	if err := db.QueryRow(`
		SELECT recurring_rule_id, series_id, recurrence_sequence, is_recurring_exception
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
	`, description).Scan(&ruleID, &seriesID, &seq, &isException); err != nil {
		t.Fatalf("query common transaction identity: %v", err)
	}
	if ruleID.Valid || seriesID.Valid || seq.Valid || isException != 0 {
		t.Fatalf("common transaction identity = rule(%q,%v) series(%q,%v) seq(%d,%v) exception=%d; want empty", ruleID.String, ruleID.Valid, seriesID.String, seriesID.Valid, seq.Int64, seq.Valid, isException)
	}
}

func assertRecurringSeriesWorkspace(t *testing.T, db *sql.DB, seriesID, wantWorkspaceID string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT workspace_id FROM recurring_series WHERE id = ?`, seriesID).Scan(&got); err != nil {
		t.Fatalf("query recurring series workspace: %v", err)
	}
	if got != wantWorkspaceID {
		t.Fatalf("series %s workspace_id = %q, want %q", seriesID, got, wantWorkspaceID)
	}
}

func assertRecurringTransactionsHaveInvoices(t *testing.T, db *sql.DB, workspaceID, ruleID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ? AND invoice_id IS NOT NULL
	`, workspaceID, ruleID).Scan(&got); err != nil {
		t.Fatalf("query recurring invoice count: %v", err)
	}
	if got != want {
		t.Fatalf("recurring invoice count = %d, want %d", got, want)
	}
}

func assertNoDuplicateRecurringSequences(t *testing.T, db *sql.DB, workspaceID, ruleID string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT recurrence_sequence, COUNT(1)
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ?
		GROUP BY recurrence_sequence
		HAVING COUNT(1) > 1
	`, workspaceID, ruleID)
	if err != nil {
		t.Fatalf("query duplicate recurring sequences: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq sql.NullInt64
		var count int
		if err := rows.Scan(&seq, &count); err != nil {
			t.Fatalf("scan duplicate recurring sequence: %v", err)
		}
		t.Fatalf("recurring rule %s has duplicate sequence %d valid=%v count=%d", ruleID, seq.Int64, seq.Valid, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate duplicate recurring sequences: %v", err)
	}
}

func assertRecurringStatusesByDescription(t *testing.T, db *sql.DB, description string, want []string) {
	t.Helper()

	rows, err := db.Query(`
		SELECT status
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
		ORDER BY COALESCE(recurrence_sequence, 0) ASC, date ASC
	`, description)
	if err != nil {
		t.Fatalf("query recurring statuses: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan recurring status: %v", err)
		}
		got = append(got, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recurring statuses: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("status count = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func recurringIDsByDescription(t *testing.T, db *sql.DB, description string) []string {
	t.Helper()

	rows, err := db.Query(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
		ORDER BY COALESCE(recurrence_sequence, 0) ASC, date ASC
	`, description)
	if err != nil {
		t.Fatalf("query recurring ids: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan recurring id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recurring ids: %v", err)
	}
	return ids
}

func assertRecurringRuleDefaultStatus(t *testing.T, db *sql.DB, description, want string) {
	t.Helper()

	var got string
	if err := db.QueryRow(`
		SELECT default_payment_status
		FROM recurring_rules
		WHERE workspace_id = 'ws-test' AND description = ?
	`, description).Scan(&got); err != nil {
		t.Fatalf("query recurring rule status: %v", err)
	}
	if got != want {
		t.Fatalf("default_payment_status = %q, want %q", got, want)
	}
}

func assertOtherWorkspaceRecurringUnchanged(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`
		SELECT amount, status
		FROM transactions
		WHERE workspace_id = 'ws-other' AND recurring_rule_id = 'rule-other'
		ORDER BY recurrence_sequence ASC
	`)
	if err != nil {
		t.Fatalf("query other workspace recurring: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var amount int64
		var status string
		if err := rows.Scan(&amount, &status); err != nil {
			t.Fatalf("scan other workspace recurring: %v", err)
		}
		count++
		if amount != 9900 || status != "pending" {
			t.Fatalf("other workspace occurrence changed: amount=%d status=%q", amount, status)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate other workspace recurring: %v", err)
	}
	if count != 3 {
		t.Fatalf("other workspace occurrence count = %d, want 3", count)
	}
}

func assertRecurringDescriptionsAndStatuses(t *testing.T, db *sql.DB, want []struct {
	id          string
	description string
	status      string
}) {
	t.Helper()

	for _, item := range want {
		var gotDescription, gotStatus string
		if err := db.QueryRow(`
			SELECT description, status
			FROM transactions
			WHERE workspace_id = 'ws-test' AND id = ?
		`, item.id).Scan(&gotDescription, &gotStatus); err != nil {
			t.Fatalf("query recurring item %s: %v", item.id, err)
		}
		if gotDescription != item.description || gotStatus != item.status {
			t.Fatalf("tx %s = (%q, %q), want (%q, %q)", item.id, gotDescription, gotStatus, item.description, item.status)
		}
	}
}

func newMultipartUpdateRequest(t *testing.T, path string, fields map[string]string) *http.Request {
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

func assertInstallmentAmount(t *testing.T, db *sql.DB, rootID string, installmentNumber int64, want int64) {
	t.Helper()

	var amount int64
	if err := db.QueryRow(`
		SELECT amount
		FROM transactions
		WHERE workspace_id = 'ws-test'
		  AND (id = ? OR parent_id = ?)
		  AND installment_number = ?
	`, rootID, rootID, installmentNumber).Scan(&amount); err != nil {
		t.Fatalf("query installment %d: %v", installmentNumber, err)
	}
	if amount != want {
		t.Fatalf("installment %d amount = %d, want %d", installmentNumber, amount, want)
	}
}
