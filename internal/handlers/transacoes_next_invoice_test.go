package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestInsertTransactionNextInvoiceCreatesInvoiceForCreditCard(t *testing.T) {
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
		"Compra Proxima Fatura",
		"",
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction NEXT_INVOICE: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Proxima Fatura")

	assertTransactionInvoiceReference(t, db, txID, "2026-09")
	assertInvoiceExists(t, db, "card-test", "2026-09", "OPEN")
}

func TestInsertTransactionNextInvoiceReusesExistingNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	seedNextInvoiceForTest(t, db, "card-test", "2026-09")

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Proxima Fatura Existente",
		"",
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction NEXT_INVOICE with existing next: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Proxima Fatura Existente")

	assertTransactionInvoiceReference(t, db, txID, "2026-09")

	assertInvoiceCount(t, db, "card-test", "2026-09", 1)
}

func TestInsertTransactionAutoUsesPaidInvoiceWithinCycle(t *testing.T) {
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
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction auto paid within cycle: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Apos Quitacao Antecipada")
	assertTransactionInvoiceReference(t, db, txID, "2026-08")

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status); err != nil {
		t.Fatalf("query invoice status: %v", err)
	}
	if status == "PAID" {
		t.Fatalf("invoice should have left PAID status after new transaction, got %q", status)
	}
}

func TestInsertTransactionAutoUsesClosedInvoiceWithinCycle(t *testing.T) {
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
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction auto closed within cycle: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Fatura Fechada Mesmo Ciclo")
	assertTransactionInvoiceReference(t, db, txID, "2026-08")
}

func TestInsertTransactionAutoAfterClosingGoesToNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	setInvoiceStatusForTest(t, db, "invoice-2026-08", "PAID")
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "OPEN")

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE",
		15000,
		"Compra Apos Fechamento",
		"",
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction auto after closing: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Apos Fechamento")
	assertTransactionInvoiceReference(t, db, txID, "2026-09")
}

func TestInsertTransactionNextUsesNextReferenceRegardlessOfStatus(t *testing.T) {
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
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"NEXT_INVOICE",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction next paid reference: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Next Fatura Paga")
	assertTransactionInvoiceReference(t, db, txID, "2026-09")
}

func TestHandleAtualizarTransacaoRespeitaFaturaOffsetNext(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedExpenseCategoryForInvoiceResolutionTest(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "OPEN")

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
		"data":             "2026-07-10",
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
	assertTransactionInvoiceReference(t, db, "purchase-test", "2026-09")
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

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	_, err := handler.insertTransaction(
		"EXPENSE",
		45000,
		"Compra Parcelada Proxima Fatura",
		"",
		"",
		date,
		"card-test",
		"",
		"",
		3,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
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

	assertTransactionInvoiceReference(t, db, parentID, "2026-09")

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

	assertInvoiceExists(t, db, "card-test", "2026-09", "OPEN")
	assertInvoiceExists(t, db, "card-test", "2026-10", "OPEN")

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

	if install2Ref != "2026-09" {
		t.Fatalf("installment 2 reference = %q, want 2026-09", install2Ref)
	}
	if install3Ref != "2026-10" {
		t.Fatalf("installment 3 reference = %q, want 2026-10", install3Ref)
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
		"",
		date,
		"checking-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
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
	id := "invoice-" + reference
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'OPEN', ?)
	`, id, accountID, reference, testUnixDate("2026-08-20"), testUnixDate("2026-09-10"), now)
}

func seedInvoiceForStatusTest(t *testing.T, db *sql.DB, accountID, reference, status string) {
	t.Helper()
	now := time.Now().Unix()
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		t.Fatalf("parse reference: %v", err)
	}
	closingUnix, dueUnix := invoiceDatesForReference(year, month, 20, 10)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "invoice-"+reference, accountID, reference, closingUnix, dueUnix, status, nullablePaidAtForStatus(status, now), nullablePaidAmountForStatus(status), now)
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
		"",
		date,
		"card-no-config",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
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
		"",
		date,
		"card-test",
		"",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
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
