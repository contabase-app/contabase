package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInsertTransactionNextInvoiceCreatesInvoiceForCreditCard(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := scenario.Occurrence.Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Proxima Fatura",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction NEXT_INVOICE: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Proxima Fatura")

	assertTransactionInvoiceReference(t, db, txID, scenario.NextRef)
	assertInvoiceExists(t, db, "card-test", scenario.NextRef, "OPEN")
}

func TestInsertTransactionNextInvoiceReusesExistingNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)

	seedNextInvoiceForTest(t, db, "card-test", scenario.NextRef)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := scenario.Occurrence.Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Proxima Fatura Existente",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction NEXT_INVOICE with existing next: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Proxima Fatura Existente")

	assertTransactionInvoiceReference(t, db, txID, scenario.NextRef)

	assertInvoiceCount(t, db, "card-test", scenario.NextRef, 1)
}

func TestInsertTransactionAutoRejectsPaidInvoiceWithinCycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	setInvoiceStatusForTest(t, db, "invoice-2026-08", "PAID")

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Apos Quitacao Antecipada",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if !errors.Is(err, errAutomaticInvoiceFrozen) || !errors.Is(err, errInvoiceStateTransitionConflict) {
		t.Fatalf("insertTransaction auto paid within cycle error = %v, want frozen conflict", err)
	}

	var inserted int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Compra Apos Quitacao Antecipada'`).Scan(&inserted); err != nil {
		t.Fatalf("count inserted transactions: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted transactions = %d, want 0", inserted)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status); err != nil {
		t.Fatalf("query invoice status: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID unchanged", status)
	}
}

func TestInsertTransactionAutoAcceptsClosedUnpaidInvoiceWithinCycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	setInvoiceStatusForTest(t, db, "invoice-2026-08", "CLOSED")

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Fatura Fechada Mesmo Ciclo",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction auto closed within cycle error = %v, want nil", err)
	}

	var inserted int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Compra Fatura Fechada Mesmo Ciclo'`).Scan(&inserted); err != nil {
		t.Fatalf("count inserted transactions: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted transactions = %d, want 1", inserted)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status); err != nil {
		t.Fatalf("query invoice status: %v", err)
	}
	if status != "CLOSED" {
		t.Fatalf("invoice status = %q, want CLOSED unchanged", status)
	}
}

func TestHandleSalvarTransacaoAutoFrozenReturns409WithoutTombstone(t *testing.T) {
	for _, status := range []string{"PAID"} {
		t.Run(status, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			seedRecurringExpenseCategory(t, db)
			setInvoiceStatusForTest(t, db, "invoice-2026-08", status)
			handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}

			req := formPostMultipart(t, "/transacoes", url.Values{
				"valor":            {"150,00"},
				"descricao":        {"Auto frozen HTTP " + status},
				"data":             {"2026-07-10"},
				"tipo":             {"despesa"},
				"origem_conta_id":  {"card-test"},
				"categoria_id":     {"cat-expense-rec"},
				"status_pagamento": {"paid"},
			})
			rr := httptest.NewRecorder()
			handler.HandleSalvarTransacao(rr, req)
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body=%q", rr.Code, rr.Body.String())
			}

			var transactions, blocks int
			_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description=?`, "Auto frozen HTTP "+status).Scan(&transactions)
			_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks`).Scan(&blocks)
			if transactions != 0 || blocks != 0 {
				t.Fatalf("sync frozen path wrote state: transactions=%d blocks=%d", transactions, blocks)
			}
		})
	}
}

func TestInsertTransactionAutoUnknownInvoiceStatusIsStructural(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	execTestSQL(t, db, `PRAGMA ignore_check_constraints=ON`)
	execTestSQL(t, db, `UPDATE invoices SET status='CORRUPT' WHERE id='invoice-2026-08'`)
	execTestSQL(t, db, `PRAGMA ignore_check_constraints=OFF`)
	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}

	_, err := handler.insertTransaction(
		"EXPENSE", 15000, "Auto corrupt status", "", "",
		MustCivilWriteDate("2026-07-10"), "card-test", "", "cat-expense-rec",
		1, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "", false,
	)
	if !errors.Is(err, errInvoiceStateStructural) {
		t.Fatalf("error = %v, want structural", err)
	}
	var inserted int
	_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description='Auto corrupt status'`).Scan(&inserted)
	if inserted != 0 {
		t.Fatalf("inserted transactions = %d, want 0", inserted)
	}
}

func TestAutomaticInstallmentsFrozenDestinationRollbackIntegral(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "CLOSED")
	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}

	_, err := handler.insertTransaction(
		"EXPENSE", 30000, "Installments frozen rollback", "", "",
		MustCivilWriteDate("2026-07-10"), "card-test", "", "cat-expense-rec",
		2, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "", false,
	)
	if !errors.Is(err, errAutomaticInvoiceFrozen) {
		t.Fatalf("error = %v, want frozen conflict", err)
	}
	var transactions, blocks int
	_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description='Installments frozen rollback'`).Scan(&transactions)
	_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks`).Scan(&blocks)
	if transactions != 0 || blocks != 0 {
		t.Fatalf("partial installment state persisted: transactions=%d blocks=%d", transactions, blocks)
	}
}

func TestAutomaticRecurringProjectionFrozenDestinationRollbackIntegral(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "CLOSED")
	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	total := int64(2)

	_, err := handler.insertTransaction(
		"EXPENSE", 15000, "Projection frozen rollback", "", "",
		MustCivilWriteDate("2026-07-10"), "card-test", "", "cat-expense-rec",
		1, "paid", true, "MONTHLY", "", OptionalCivilWriteDate{}, &total, "", false,
	)
	if !errors.Is(err, errAutomaticInvoiceFrozen) {
		t.Fatalf("error = %v, want frozen conflict", err)
	}
	var transactions, rules, blocks int
	_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description='Projection frozen rollback'`).Scan(&transactions)
	_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE description='Projection frozen rollback'`).Scan(&rules)
	_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks`).Scan(&blocks)
	if transactions != 0 || rules != 0 || blocks != 0 {
		t.Fatalf("partial projection state persisted: transactions=%d rules=%d blocks=%d", transactions, rules, blocks)
	}
}

func TestManualClosedInvoiceSelectionRemainsExplicitControl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	setInvoiceStatusForTest(t, db, "invoice-2026-08", "CLOSED")
	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}

	_, err := handler.insertTransaction(
		"EXPENSE", 15000, "Manual closed control", "", "",
		MustCivilWriteDate("2026-07-10"), "card-test", "", "cat-expense-rec",
		1, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "invoice-2026-08", false,
	)
	if err != nil {
		t.Fatalf("manual CLOSED selection: %v", err)
	}
	var invoiceID, overrideID string
	if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE description='Manual closed control'`).Scan(&invoiceID, &overrideID); err != nil {
		t.Fatalf("query manual transaction: %v", err)
	}
	if invoiceID != "invoice-2026-08" || overrideID != "invoice-2026-08" {
		t.Fatalf("manual invoice evidence = %q/%q", invoiceID, overrideID)
	}
}

func TestInsertTransactionAutoAfterClosingGoesToNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)
	setInvoiceStatusForTest(t, db, "invoice-2026-08", "PAID")
	seedInvoiceForStatusTest(t, db, "card-test", scenario.NextRef, "OPEN")

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := scenario.Occurrence.AddDate(0, 1, 0).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Apos Fechamento",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction auto after closing: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Apos Fechamento")
	assertTransactionInvoiceReference(t, db, txID, scenario.NextRef)
}

func TestHandleResolverDestinoFaturaOnClosingDayGoesToNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToClosingDayBoundary(t, db)

	handler := FaturasHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(
		"GET",
		"/cartoes/fatura-destino/card-test?data="+scenario.ClosingDate.Format("2006-01-02")+"&fatura_offset=auto",
		nil,
	)
	rr := httptest.NewRecorder()

	handler.HandleResolverDestinoFatura(rr, req, "card-test")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var payload struct {
		Reference string `json:"reference"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Reference != scenario.NextRef {
		t.Fatalf("destination reference = %q, want %q", payload.Reference, scenario.NextRef)
	}
	if payload.Status != "OPEN" {
		t.Fatalf("destination status = %q, want OPEN", payload.Status)
	}
}

func TestInsertTransactionAutoOnClosingDayGoesToNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToClosingDayBoundary(t, db)
	seedExpenseCategoryForInvoiceResolutionTest(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	_, err := handler.insertTransaction(
		"EXPENSE",
		23188,
		"Compra no dia do fechamento",
		"",
		"", civilWriteDateFromLegacyUnix(scenario.ClosingDate.Unix()), "card-test",
		"",
		"cat-expense",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction auto on closing day: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra no dia do fechamento")
	assertTransactionInvoiceReference(t, db, txID, scenario.NextRef)
}

func TestInsertTransactionNextPaidReferencePreservesStateMachineBlock(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "PAID")

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Next Fatura Paga",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"NEXT_INVOICE",
		false,
	)
	if !errors.Is(err, errInvoiceImplicitReopenBlocked) {
		t.Fatalf("insertTransaction next paid reference error = %v, want state-machine block", err)
	}

	var inserted int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Compra Next Fatura Paga'`).Scan(&inserted); err != nil {
		t.Fatalf("count inserted transactions: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted transactions = %d, want 0", inserted)
	}
}

func TestHandleAtualizarTransacaoRespeitaFaturaOffsetNext(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)
	seedExpenseCategoryForInvoiceResolutionTest(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", scenario.NextRef, "OPEN")

	handler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":            "150,00",
		"descricao":        "Compra Editada Proxima",
		"data":             scenario.Occurrence.Format("2006-01-02"),
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense",
		"status_pagamento": "paid",
		"fatura_offset":    "next",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/transacoes/purchase-test/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "purchase-test")

	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertTransactionInvoiceReference(t, db, "purchase-test", scenario.NextRef)
}

func TestHandleResolverDestinoFaturaUsaFaturaPagaNoCiclo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	setInvoiceStatusForTest(t, db, "invoice-2026-08", "PAID")

	handler := FaturasHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest("GET", "/cartoes/fatura-destino/card-test?data=2026-07-10&fatura_offset=auto", nil)
	rr := httptest.NewRecorder()

	handler.HandleResolverDestinoFatura(rr, req, "card-test")

	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var payload struct {
		Reference string `json:"reference"`
		Status    string `json:"status"`
		Notice    string `json:"notice"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Reference != "2026-08" {
		t.Fatalf("destination reference = %s, want 2026-08", payload.Reference)
	}
}

func TestInsertTransactionNextInvoiceSkipsForNonFirstInstallment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	scenario := moveSeedInvoiceScenarioToFuture(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := scenario.Occurrence.Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		45000,
		"Compra Parcelada Proxima Fatura",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		3,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction NEXT_INVOICE installment: %v", err)
	}

	var parentID string
	if err := db.QueryRow(`
		SELECT id FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Compra Parcelada Proxima Fatura' AND installment_number = 1
	`).Scan(&parentID); err != nil {
		t.Fatalf("find first installment: %v", err)
	}

	assertTransactionInvoiceReference(t, db, parentID, scenario.NextRef)
	nextNextRef, err := nextInvoiceReference(scenario.NextRef)
	if err != nil {
		t.Fatalf("second future invoice reference: %v", err)
	}

	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM transactions
		WHERE parent_id = ? AND workspace_id = 'ws-test'
	`, parentID).Scan(&count); err != nil {
		t.Fatalf("installment count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 installment children, got %d", count)
	}

	assertInvoiceExists(t, db, "card-test", scenario.NextRef, "OPEN")
	assertInvoiceExists(t, db, "card-test", nextNextRef, "OPEN")

	var install2Ref, install3Ref string
	_ = db.QueryRow(`
		SELECT i.reference FROM transactions t
		JOIN invoices i ON i.id = t.invoice_id
		WHERE t.parent_id = ? AND t.installment_number = 2 AND t.workspace_id = 'ws-test'
	`, parentID).Scan(&install2Ref)
	_ = db.QueryRow(`
		SELECT i.reference FROM transactions t
		JOIN invoices i ON i.id = t.invoice_id
		WHERE t.parent_id = ? AND t.installment_number = 3 AND t.workspace_id = 'ws-test'
	`, parentID).Scan(&install3Ref)

	if install2Ref != scenario.NextRef {
		t.Fatalf("installment 2 reference = %q, want %s", install2Ref, scenario.NextRef)
	}
	if install3Ref != nextNextRef {
		t.Fatalf("installment 3 reference = %q, want %s", install3Ref, nextNextRef)
	}
}

func TestInsertTransactionNextInvoiceFailsForNonCreditCardAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Checking Proxima Fatura",
		"",
		"", civilWriteDateFromLegacyUnix(date), "checking-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("unexpected error for non-credit card NEXT_INVOICE: %v", err)
	}

	var invoiceID sql.NullString
	if err := db.QueryRow(`
		SELECT invoice_id FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Compra Checking Proxima Fatura'
	`).Scan(&invoiceID); err != nil {
		t.Fatalf("query: %v", err)
	}
	if invoiceID.Valid {
		t.Fatalf("expected no invoice_id for non-credit card, got %s", invoiceID.String)
	}
}

func seedNextInvoiceForTest(t *testing.T, db *sql.DB, accountID, reference string) {
	t.Helper()
	now := time.Now().Unix()
	closingUnix, dueUnix := futureOpenInvoiceDatesForTest()
	id := "invoice-" + reference
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'OPEN', ?)
	`, id, accountID, reference, closingUnix, dueUnix, now)
}

func seedInvoiceForStatusTest(t *testing.T, db *sql.DB, accountID, reference, status string) {
	t.Helper()
	now := time.Now().Unix()
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	closingUnix, dueUnix := invoiceDatesForReference(year, month, 20, 10)
	if status == "OPEN" {
		closingUnix, dueUnix = futureOpenInvoiceDatesForTest()
	}
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "invoice-"+reference, accountID, reference, closingUnix, dueUnix, status, nullablePaidAtForStatus(status, now), nullablePaidAmountForStatus(status), now)
}

type closingDayBoundaryScenarioForTest struct {
	ClosingDate time.Time
	CurrentRef  string
	NextRef     string
}

func moveSeedInvoiceScenarioToClosingDayBoundary(t *testing.T, db *sql.DB) closingDayBoundaryScenarioForTest {
	t.Helper()

	base := time.Now().UTC().AddDate(2, 0, 0)
	closingDate := time.Date(base.Year(), base.Month(), 1, 12, 0, 0, 0, time.UTC)
	currentRef := closingDate.Format("2006-01")
	nextRef, err := nextInvoiceReference(currentRef)
	if err != nil {
		t.Fatalf("next closing-day invoice reference: %v", err)
	}

	currentClosingUnix, currentDueUnix := invoiceDatesForReference(closingDate.Year(), closingDate.Month(), 1, 7)
	nextYear, nextMonth, err := parseInvoiceReference(nextRef)
	if err != nil {
		t.Fatalf("parse next closing-day invoice reference: %v", err)
	}
	nextClosingUnix, nextDueUnix := invoiceDatesForReference(nextYear, nextMonth, 1, 7)
	now := time.Now().Unix()

	execTestSQL(t, db, `
		UPDATE credit_cards
		SET closing_day = 1, due_day = 7
		WHERE account_id = 'card-test'
	`)
	execTestSQL(t, db, `
		UPDATE invoices
		SET reference = ?, closing_date = ?, due_date = ?, status = 'CLOSED', paid_at = NULL, paid_amount = 0
		WHERE id = 'invoice-2026-08' AND account_id = 'card-test'
	`, currentRef, currentClosingUnix, currentDueUnix)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at)
		VALUES ('invoice-closing-day-next', 'card-test', ?, ?, ?, 'OPEN', NULL, 0, ?)
	`, nextRef, nextClosingUnix, nextDueUnix, now)

	return closingDayBoundaryScenarioForTest{
		ClosingDate: closingDate,
		CurrentRef:  currentRef,
		NextRef:     nextRef,
	}
}

func setInvoiceStatusForTest(t *testing.T, db *sql.DB, invoiceID, status string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = ?, paid_at = ?, paid_amount = ?
		WHERE id = ?
	`, status, nullablePaidAtForStatus(status, now), nullablePaidAmountForStatus(status), invoiceID)
}

func nullablePaidAtForStatus(status string, now int64) interface{} {
	if status == "PAID" {
		return now
	}
	return nil
}

func nullablePaidAmountForStatus(status string) interface{} {
	if status == "PAID" {
		return int64(0)
	}
	return nil
}

func seedExpenseCategoryForInvoiceResolutionTest(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)
}

func assertInvoiceCount(t *testing.T, db *sql.DB, accountID, reference string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM invoices WHERE account_id = ? AND reference = ?
	`, accountID, reference).Scan(&count); err != nil {
		t.Fatalf("invoice count query: %v", err)
	}
	if count != want {
		t.Fatalf("invoice %s count = %d, want %d", reference, count, want)
	}
}

func TestInsertTransactionNextInvoiceMissingCreditCardConfig(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	seedCreditCardAccountWithoutConfig(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Cartao Sem Configuracao",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-no-config",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"NEXT_INVOICE",
		false,
	)
	if err == nil {
		t.Fatal("expected error for credit card without config, got nil")
	}
	if !strings.Contains(err.Error(), "cartão de crédito sem configuração") {
		t.Fatalf("expected 'cartão de crédito sem configuração' error, got: %v", err)
	}
}

func TestInsertTransactionNextInvoiceNormalCreditCardAlsoWorks(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Normal Cartao",
		"",
		"", civilWriteDateFromLegacyUnix(date), "card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"", OptionalCivilWriteDate{}, nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction normal credit card: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Normal Cartao")

	assertTransactionInvoiceReference(t, db, txID, "2026-08")
}

func seedCreditCardAccountWithoutConfig(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-no-config', 'ws-test', 'Cartao Sem Config', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
}

func TestOrphanCreditCardDoesNotAppearInFormAccounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedCreditCardAccountWithoutConfig(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}

	for _, acc := range accounts {
		if acc.ID == "card-no-config" {
			t.Fatal("orphan credit card should not appear in queryFormAccounts")
		}
	}

	foundCard := false
	for _, acc := range accounts {
		if acc.ID == "card-test" {
			foundCard = true
			break
		}
	}
	if !foundCard {
		t.Fatal("valid credit card card-test should appear in queryFormAccounts")
	}

	foundChecking := false
	for _, acc := range accounts {
		if acc.ID == "checking-test" {
			foundChecking = true
			break
		}
	}
	if !foundChecking {
		t.Fatal("checking account should appear in queryFormAccounts")
	}
}

func TestOrphanCreditCardStillAppearsInFilterAccounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedCreditCardAccountWithoutConfig(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	accounts, err := handler.queryFilterAccounts()
	if err != nil {
		t.Fatalf("queryFilterAccounts: %v", err)
	}

	found := false
	for _, acc := range accounts {
		if acc.ID == "card-no-config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("orphan credit card should still appear in queryFilterAccounts for historical filtering")
	}
}
