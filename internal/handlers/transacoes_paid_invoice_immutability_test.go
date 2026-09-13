package handlers

import (
	"bytes"
	"database/sql"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPaidInvoiceTransactionEditIsBlockedWithoutMutation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	var beforeAmount, beforeDate int64
	var beforeDescription, beforeNotes, beforeInvoiceID string
	if err := db.QueryRow(`SELECT amount, date, description, COALESCE(notes, ''), invoice_id FROM transactions WHERE id = 'purchase-test'`).Scan(&beforeAmount, &beforeDate, &beforeDescription, &beforeNotes, &beforeInvoiceID); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             "300,00",
		"descricao":         "Compra Alterada",
		"anotacoes":         "metadata bloqueada",
		"data":              "2026-07-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":  "paid",
		"return_invoice_id": "invoice-2026-08",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "purchase-test")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}

	var amount, date int64
	var description, notes, invoiceID string
	if err := db.QueryRow(`SELECT amount, date, description, COALESCE(notes, ''), invoice_id FROM transactions WHERE id = ?`, "purchase-test").Scan(&amount, &date, &description, &notes, &invoiceID); err != nil {
		t.Fatalf("query tx: %v", err)
	}
	if amount != beforeAmount || date != beforeDate || description != beforeDescription || notes != beforeNotes || invoiceID != beforeInvoiceID {
		t.Fatalf("blocked edit changed transaction: before=%d/%d/%q/%q/%q after=%d/%d/%q/%q/%q", beforeAmount, beforeDate, beforeDescription, beforeNotes, beforeInvoiceID, amount, date, description, notes, invoiceID)
	}
}

func TestPaidInvoiceTransactionDeleteIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	req := httptest.NewRequest(http.MethodDelete, "/transacoes/purchase-test", nil)
	rr := httptest.NewRecorder()

	handler.HandleDeletarTransacao(rr, req, "purchase-test")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, "purchase-test").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("transaction count = %d, want 1", count)
	}
}

func TestPaidInvoiceTransactionBulkDeleteIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	deleteForm := url.Values{}
	deleteForm.Add("ids[]", "purchase-test")
	deleteReq := httptest.NewRequest(http.MethodPost, "/transacoes/bulk-delete", strings.NewReader(deleteForm.Encode()))
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteRR := httptest.NewRecorder()
	handler.HandleBulkDelete(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", deleteRR.Code, deleteRR.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, "purchase-test").Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("transaction count = %d, want 1", count)
	}
}

func TestPaidInvoiceTransactionToggleIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	req := httptest.NewRequest(http.MethodPost, "/transacoes/purchase-test/toggle", nil)
	rr := httptest.NewRecorder()

	handler.HandleTogglePagamento(rr, req, "purchase-test")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}
}

func TestPaidInvoiceTransactionSeriesUpdateIsBlockedWhenScopeTouchesPaidInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
		VALUES
			('series-paid-1', 'ws-test', 'user-test', 'card-test', 'cat-expense', 'invoice-2026-08', 'EXPENSE', 10000, ?, 'Serie Cartao', 'paid', 1, 2, NULL, ?, ?),
			('series-open-2', 'ws-test', 'user-test', 'card-test', 'cat-expense', 'invoice-2026-09', 'EXPENSE', 10000, ?, 'Serie Cartao', 'paid', 2, 2, 'series-paid-1', ?, ?)
	`, time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC).Unix(), now, now, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC).Unix(), now, now)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":            "150,00",
		"descricao":        "Serie Atualizada",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense",
		"status_pagamento": "paid",
		"escopo":           "all",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/series-open-2/salvar?escopo=all", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "series-open-2")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}
	var changed int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id IN ('series-paid-1', 'series-open-2') AND amount != 10000`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("changed rows = %d, want 0", changed)
	}
}

func TestOpenInvoiceTransactionRemainsEditable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             "410,00",
		"descricao":         "Compra Open Atualizada",
		"data":              "2026-08-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":  "paid",
		"return_invoice_id": "invoice-2026-09",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "open-purchase")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var amount int64
	var description string
	if err := db.QueryRow(`SELECT amount, description FROM transactions WHERE id = ?`, "open-purchase").Scan(&amount, &description); err != nil {
		t.Fatalf("query updated transaction: %v", err)
	}
	if amount != 41000 {
		t.Fatalf("amount = %d, want 41000", amount)
	}
	if description != "Compra Open Atualizada" {
		t.Fatalf("description = %q, want %q", description, "Compra Open Atualizada")
	}
}

func TestOpenInvoiceCompositionSettlementAndUploadLifecycle(t *testing.T) {
	setup := func(t *testing.T) (*sql.DB, TransactionHandler, string) {
		t.Helper()
		db := openTestDB(t)
		seedPaidInvoiceImmutabilityScenario(t, db)
		uploadRoot := t.TempDir()
		t.Setenv("UPLOADS_DIR", uploadRoot)
		execTestSQL(t, db, `UPDATE transactions SET amount = 10000 WHERE id = 'open-purchase'`)
		execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN', paid_amount = 8000, paid_at = NULL WHERE id = 'invoice-2026-09'`)
		execTestSQL(t, db, `
			INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_by, created_at)
			VALUES ('open-partial', 'ws-test', 'invoice-2026-09', 'checking-test', 8000, 1700000000, 'manual', 'user-test', 1700000000)
		`)
		return db, testPaidInvoiceMutationHandler(db), uploadRoot
	}

	t.Run("valid exact settlement keeps new upload", func(t *testing.T) {
		db, handler, uploadRoot := setup(t)
		defer db.Close()
		req := openInvoiceEditUploadRequest(t, "80,00", "new.pdf")
		rr := httptest.NewRecorder()
		start := time.Now().Unix()
		handler.HandleAtualizarTransacao(rr, req, "open-purchase")
		end := time.Now().Unix()
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var status, attachment string
		var paidAmount int64
		var paidAt sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-09'`).Scan(&status, &paidAmount, &paidAt); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT attachment_path FROM transactions WHERE id = 'open-purchase'`).Scan(&attachment); err != nil {
			t.Fatal(err)
		}
		if status != "PAID" || paidAmount != 8000 || !paidAt.Valid || paidAt.Int64 < start || paidAt.Int64 > end || paidAt.Int64 == 1700000000 {
			t.Fatalf("invoice = %q/%d/%v, want composition settlement during request", status, paidAmount, paidAt)
		}
		if _, err := os.Stat(filepath.Join(uploadRoot, "ws-test", filepath.Base(attachment))); err != nil {
			t.Fatalf("committed upload missing: %v", err)
		}
	})

	t.Run("overpay removes only new upload", func(t *testing.T) {
		db, handler, uploadRoot := setup(t)
		defer db.Close()
		workspaceDir := filepath.Join(uploadRoot, "ws-test")
		if err := os.MkdirAll(workspaceDir, 0700); err != nil {
			t.Fatal(err)
		}
		oldFile := filepath.Join(workspaceDir, "existing.pdf")
		if err := os.WriteFile(oldFile, []byte("%PDF-1.4\nexisting"), 0600); err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, db, `UPDATE transactions SET attachment_path = 'uploads/ws-test/existing.pdf' WHERE id = 'open-purchase'`)

		rr := httptest.NewRecorder()
		handler.HandleAtualizarTransacao(rr, openInvoiceEditUploadRequest(t, "70,00", "replacement.pdf"), "open-purchase")
		if rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
		}
		entries, err := os.ReadDir(workspaceDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "existing.pdf" {
			t.Fatalf("upload files = %v, want only existing.pdf", entries)
		}
		var amount int64
		var attachment string
		_ = db.QueryRow(`SELECT amount, attachment_path FROM transactions WHERE id = 'open-purchase'`).Scan(&amount, &attachment)
		if amount != 10000 || attachment != "uploads/ws-test/existing.pdf" {
			t.Fatalf("rolled back transaction = %d/%q", amount, attachment)
		}
	})
}

func TestOpenInvoiceCompositionCreateWithUpload(t *testing.T) {
	setup := func(t *testing.T) (*sql.DB, TransactionHandler, string) {
		t.Helper()
		db := openTestDB(t)
		seedPaidInvoiceImmutabilityScenario(t, db)
		uploadRoot := t.TempDir()
		t.Setenv("UPLOADS_DIR", uploadRoot)
		execTestSQL(t, db, `UPDATE transactions SET amount = 10000 WHERE id = 'open-purchase'`)
		execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN', paid_amount = 8000, paid_at = NULL WHERE id = 'invoice-2026-09'`)
		execTestSQL(t, db, `
			INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_by, created_at)
			VALUES ('open-partial', 'ws-test', 'invoice-2026-09', 'checking-test', 8000, 1700000000, 'manual', 'user-test', 1700000000)
		`)
		return db, testPaidInvoiceMutationHandler(db), uploadRoot
	}

	t.Run("create with upload keeps file on success", func(t *testing.T) {
		db, handler, uploadRoot := setup(t)
		defer db.Close()
		req := createInvoiceUploadRequest(t, "150,00", "create-success.pdf", "invoice-2026-09")
		rr := httptest.NewRecorder()
		handler.HandleSalvarTransacao(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var txID, attachment string
		var amount int64
		if err := db.QueryRow(`SELECT id, amount, attachment_path FROM transactions WHERE description = 'Compra Nova C0-D' ORDER BY created_at DESC LIMIT 1`).Scan(&txID, &amount, &attachment); err != nil {
			t.Fatalf("query new transaction: %v", err)
		}
		if amount != 15000 {
			t.Fatalf("amount = %d, want 15000", amount)
		}
		if attachment == "" {
			t.Fatal("attachment_path empty after successful create")
		}
		if _, err := os.Stat(filepath.Join(uploadRoot, "ws-test", filepath.Base(attachment))); err != nil {
			t.Fatalf("committed upload missing: %v", err)
		}
		var invStatus string
		var invPaidAmount int64
		if err := db.QueryRow(`SELECT status, paid_amount FROM invoices WHERE id = 'invoice-2026-09'`).Scan(&invStatus, &invPaidAmount); err != nil {
			t.Fatal(err)
		}
		if invStatus != "OPEN" || invPaidAmount != 8000 {
			t.Fatalf("invoice state = %q/%d, want OPEN/8000 after successful create", invStatus, invPaidAmount)
		}
	})

	t.Run("create with upload removes file on implicit reopen conflict", func(t *testing.T) {
		db, handler, uploadRoot := setup(t)
		defer db.Close()

		var balanceBefore int64
		if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceBefore); err != nil {
			t.Fatal(err)
		}

		var invStatusBefore string
		var invPaidBefore int64
		var invPaidAtBefore sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&invStatusBefore, &invPaidBefore, &invPaidAtBefore); err != nil {
			t.Fatal(err)
		}

		var txCountBefore int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test'`).Scan(&txCountBefore); err != nil {
			t.Fatal(err)
		}

		req := createInvoiceUploadRequest(t, "100,00", "create-conflict.pdf", "invoice-2026-08")
		rr := httptest.NewRecorder()
		handler.HandleSalvarTransacao(rr, req)
		if rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
		}

		workspaceDir := filepath.Join(uploadRoot, "ws-test")
		if _, err := os.Stat(workspaceDir); os.IsNotExist(err) {
			t.Fatalf("workspace upload dir should exist: %v", err)
		}
		entries, err := os.ReadDir(workspaceDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("upload directory contains orphan files after conflict: %v", entries)
		}

		var txCountAfter int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test'`).Scan(&txCountAfter); err != nil {
			t.Fatal(err)
		}
		if txCountAfter != txCountBefore {
			t.Fatalf("transaction count changed: %d → %d", txCountBefore, txCountAfter)
		}

		var countNew int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Compra Nova C0-D'`).Scan(&countNew); err != nil {
			t.Fatal(err)
		}
		if countNew != 0 {
			t.Fatal("new transaction should not exist after conflict rollback")
		}

		var balanceAfter int64
		if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfter); err != nil {
			t.Fatal(err)
		}
		if balanceAfter != balanceBefore {
			t.Fatalf("balance changed: %d → %d", balanceBefore, balanceAfter)
		}

		var invStatusAfter string
		var invPaidAfter int64
		var invPaidAtAfter sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&invStatusAfter, &invPaidAfter, &invPaidAtAfter); err != nil {
			t.Fatal(err)
		}
		if invStatusAfter != invStatusBefore || invPaidAfter != invPaidBefore {
			t.Fatalf("invoice changed: status %s→%s, paid %d→%d", invStatusBefore, invStatusAfter, invPaidBefore, invPaidAfter)
		}
		if invPaidAtBefore.Valid != invPaidAtAfter.Valid || invPaidAtBefore.Int64 != invPaidAtAfter.Int64 {
			t.Fatalf("invoice paid_at changed after conflict")
		}
	})

	t.Run("create with upload returns generic error for unknown invoice status", func(t *testing.T) {
		db, handler, uploadRoot := setup(t)
		defer db.Close()
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = ON`)
		execTestSQL(t, db, `UPDATE invoices SET status = 'BROKEN' WHERE id = 'invoice-2026-08'`)
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = OFF`)

		var txCountBefore int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test'`).Scan(&txCountBefore); err != nil {
			t.Fatal(err)
		}
		var balanceBefore int64
		if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceBefore); err != nil {
			t.Fatal(err)
		}
		var invStatusBefore string
		var invPaidBefore int64
		var invPaidAtBefore sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&invStatusBefore, &invPaidBefore, &invPaidAtBefore); err != nil {
			t.Fatal(err)
		}

		rr := httptest.NewRecorder()
		handler.HandleSalvarTransacao(rr, createInvoiceUploadRequest(t, "100,00", "create-structural.pdf", "invoice-2026-08"))
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body = %q", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "BROKEN") {
			t.Fatalf("response leaked structural status: %q", rr.Body.String())
		}

		workspaceDir := filepath.Join(uploadRoot, "ws-test")
		entries, err := os.ReadDir(workspaceDir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("upload directory contains orphan files after structural error: %v", entries)
		}

		var txCountAfter int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test'`).Scan(&txCountAfter); err != nil {
			t.Fatal(err)
		}
		if txCountAfter != txCountBefore {
			t.Fatalf("transaction count changed: %d → %d", txCountBefore, txCountAfter)
		}
		var balanceAfter int64
		if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test'`).Scan(&balanceAfter); err != nil {
			t.Fatal(err)
		}
		if balanceAfter != balanceBefore {
			t.Fatalf("balance changed: %d → %d", balanceBefore, balanceAfter)
		}
		var invStatusAfter string
		var invPaidAfter int64
		var invPaidAtAfter sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&invStatusAfter, &invPaidAfter, &invPaidAtAfter); err != nil {
			t.Fatal(err)
		}
		if invStatusAfter != invStatusBefore || invPaidAfter != invPaidBefore || invPaidAtAfter != invPaidAtBefore {
			t.Fatalf("invoice changed after structural error: before=%q/%d/%v after=%q/%d/%v", invStatusBefore, invPaidBefore, invPaidAtBefore, invStatusAfter, invPaidAfter, invPaidAtAfter)
		}
	})
}

func createInvoiceUploadRequest(t *testing.T, amount, fileName, invoiceID string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":            amount,
		"descricao":        "Compra Nova C0-D",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense",
		"status_pagamento": "paid",
		"invoice_id":       invoiceID,
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("anexo", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("%PDF-1.4\nC0-D upload create")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func openInvoiceEditUploadRequest(t *testing.T, amount, fileName string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             amount,
		"descricao":         "Compra Open Atualizada",
		"data":              "2026-08-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":  "paid",
		"return_invoice_id": "invoice-2026-09",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("anexo", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("%PDF-1.4\nC0-D upload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestOpenInvoiceTransactionDeleteAndToggleRemainAllowed(t *testing.T) {
	for _, operation := range []string{"delete", "toggle"} {
		t.Run(operation, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedPaidInvoiceImmutabilityScenario(t, db)
			handler := testPaidInvoiceMutationHandler(db)
			rr := httptest.NewRecorder()
			if operation == "delete" {
				handler.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/open-purchase", nil), "open-purchase")
			} else {
				handler.HandleTogglePagamento(rr, httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/toggle", nil), "open-purchase")
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = 'open-purchase'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if operation == "delete" {
				wantCount = 0
			}
			if count != wantCount {
				t.Fatalf("transaction count = %d, want %d", count, wantCount)
			}
		})
	}
}

func TestClosedInvoiceTransactionEditIsAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-09'`)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"valor":             "430,00",
		"descricao":         "Compra Closed Atualizada",
		"data":              "2026-08-05",
		"tipo":              "despesa",
		"origem_conta_id":   "card-test",
		"categoria_id":      "cat-expense",
		"status_pagamento":  "paid",
		"return_invoice_id": "invoice-2026-09",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()

	handler.HandleAtualizarTransacao(rr, req, "open-purchase")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}

	var amount int64
	var description string
	if err := db.QueryRow(`SELECT amount, description FROM transactions WHERE id = ?`, "open-purchase").Scan(&amount, &description); err != nil {
		t.Fatalf("query updated transaction: %v", err)
	}
	if amount != 43000 {
		t.Fatalf("amount = %d, want 43000", amount)
	}
	if description != "Compra Closed Atualizada" {
		t.Fatalf("description = %q, want updated", description)
	}
}

func TestClosedInvoiceTransactionDeleteIsAllowed(t *testing.T) {
	for _, operation := range []string{"delete"} {
		t.Run(operation, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedPaidInvoiceImmutabilityScenario(t, db)
			execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = 'invoice-2026-09'`)
			handler := testPaidInvoiceMutationHandler(db)

			var rr *httptest.ResponseRecorder
			switch operation {
			case "delete":
				rr = httptest.NewRecorder()
				handler.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/open-purchase", nil), "open-purchase")
			case "toggle":
				rr = httptest.NewRecorder()
				handler.HandleTogglePagamento(rr, httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/toggle", nil), "open-purchase")
			case "bulk-delete":
				values := url.Values{"ids[]": {"open-purchase"}}
				req := httptest.NewRequest(http.MethodPost, "/transacoes/bulk-delete", strings.NewReader(values.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rr = httptest.NewRecorder()
				handler.HandleBulkDelete(rr, req)
			case "bulk-status":
				values := url.Values{"ids[]": {"open-purchase"}, "status_pagamento": {"pending"}}
				req := httptest.NewRequest(http.MethodPost, "/transacoes/bulk-update", strings.NewReader(values.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rr = httptest.NewRecorder()
				handler.HandleBulkUpdate(rr, req)
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
			}
			var count, amount int64
			var status, invoiceID string
			if err := db.QueryRow(`SELECT COUNT(1), COALESCE(MAX(amount), 0), COALESCE(MAX(status), ''), COALESCE(MAX(invoice_id), '') FROM transactions WHERE id = 'open-purchase'`).Scan(&count, &amount, &status, &invoiceID); err != nil {
				t.Fatal(err)
			}
			if operation == "delete" || operation == "bulk-delete" {
				if count != 0 {
					t.Fatalf("%s transaction count=%d, want 0", operation, count)
				}
			} else if count != 1 || amount != 32000 || status != "pending" || invoiceID != "invoice-2026-09" {
				t.Fatalf("%s result: count=%d amount=%d status=%q invoice=%q", operation, count, amount, status, invoiceID)
			}
		})
	}
}

func TestPaidInvoiceTransactionBulkStatusIsBlocked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPaidInvoiceImmutabilityScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	values := url.Values{"ids[]": {"purchase-test"}, "status_pagamento": {"pending"}}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/bulk-update", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandleBulkUpdate(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = 'purchase-test'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "paid" {
		t.Fatalf("status changed to %q, want paid", status)
	}
}

func TestInvoiceHistoryDanglingLinkReturnsGeneric500WithoutMutation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPaidInvoiceImmutabilityScenario(t, db)
	execTestSQL(t, db, `PRAGMA foreign_keys = OFF`)
	execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'foreign-secret-invoice' WHERE id = 'open-purchase'`)
	execTestSQL(t, db, `PRAGMA foreign_keys = ON`)

	rr := httptest.NewRecorder()
	handler := testPaidInvoiceMutationHandler(db)
	handler.HandleTogglePagamento(rr, httptest.NewRequest(http.MethodPost, "/transacoes/open-purchase/toggle", nil), "open-purchase")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %q", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "foreign-secret-invoice") {
		t.Fatalf("response leaked invoice identifier: %q", rr.Body.String())
	}
	var status, invoiceID string
	if err := db.QueryRow(`SELECT status, invoice_id FROM transactions WHERE id = 'open-purchase'`).Scan(&status, &invoiceID); err != nil {
		t.Fatal(err)
	}
	if status != "paid" || invoiceID != "foreign-secret-invoice" {
		t.Fatalf("structural block changed transaction: status=%q invoice=%q", status, invoiceID)
	}
}

func seedPaidInvoiceImmutabilityScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense', 'ws-test', 'Compras', 'shopping-bag', '#6b7280', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		UPDATE transactions SET category_id = 'cat-expense' WHERE id = 'purchase-test'
	`)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-08'
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('payment-paid-invoice-08', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 25000, ?, 'Pagamento de Fatura - Cartao Teste', 'paid', ?, 1, 1, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('pay-legacy-08', 'ws-test', 'invoice-2026-08', 'checking-test', 'payment-paid-invoice-08', 25000, ?, 'manual', 'user-test', ?)
	`, now, now)

	openClosing, openDue := futureOpenInvoiceDatesForTest()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-2026-09', 'card-test', '2026-09', ?, ?, 'OPEN', ?)
	`, openClosing, openDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('open-purchase', 'ws-test', 'user-test', 'card-test', 'cat-expense', 'invoice-2026-09', 'EXPENSE', 32000, ?, 'Compra Open', 'paid', 1, 1, ?, ?)
	`, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC).Unix(), now, now)
}

func testPaidInvoiceMutationHandler(db *sql.DB) TransactionHandler {
	return TransactionHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
}

func testPaidInvoiceMutationTemplates() *template.Template {
	return template.Must(template.New("test").Parse(`
{{define "lancamento-row"}}<tr id="{{.ID}}"></tr>{{end}}
{{define "transactions-body"}}<tbody id="transactions-body"></tbody>{{end}}
{{define "dashboard-cards"}}<section id="dashboard-cards" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-summary"}}<section id="invoice-summary" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
{{define "invoice-transactions"}}<section id="invoice-transactions" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}></section>{{end}}
`))
}
