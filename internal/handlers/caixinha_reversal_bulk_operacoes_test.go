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

func TestBulkDeleteLinkedExpensesCompensatesReserveForAllItems(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txA := createExpenseForReserveTest(t, db, "Bulk Delete Linked A", "checking-test", "cat-expense-direct", 900)
	txB := createExpenseForReserveTest(t, db, "Bulk Delete Linked B", "checking-test", "cat-expense-direct", 1100)
	txC := createExpenseForReserveTest(t, db, "Bulk Delete Linked C", "checking-test", "cat-expense-parent", 1200)

	beforeA := activeConsumesBySourceTx(t, db, txA)
	beforeB := activeConsumesBySourceTx(t, db, txB)
	beforeC := activeConsumesBySourceTx(t, db, txC)
	if len(beforeA) != 1 || len(beforeB) != 1 || len(beforeC) != 1 {
		t.Fatalf("expected one active consume before bulk delete")
	}

	handler := testBulkReserveHandler(db)
	rr := bulkDeleteRequestForReserveTest(t, handler, []string{txA, txB, txC})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertTransactionRemoved(t, db, txA)
	assertTransactionRemoved(t, db, txB)
	assertTransactionRemoved(t, db, txC)
	if got := countReversalsForConsume(t, db, beforeA[0].ID); got != 1 {
		t.Fatalf("reversal for txA = %d, want 1", got)
	}
	if got := countReversalsForConsume(t, db, beforeB[0].ID); got != 1 {
		t.Fatalf("reversal for txB = %d, want 1", got)
	}
	if got := countReversalsForConsume(t, db, beforeC[0].ID); got != 1 {
		t.Fatalf("reversal for txC = %d, want 1", got)
	}
}

func TestBulkDeleteMixedCategoryReserveAdjustsPerItem(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	linkedID := createExpenseForReserveTest(t, db, "Bulk Delete Mixed Linked", "checking-test", "cat-expense-direct", 1000)
	unlinkedID := createExpenseForReserveTest(t, db, "Bulk Delete Mixed Unlinked", "checking-test", "cat-expense-unlinked", 1000)
	incomeID := createTransactionForReserveTest(t, db, "INCOME", "Bulk Delete Mixed Income", "checking-test", "", "cat-income", 1000)

	beforeLinked := activeConsumesBySourceTx(t, db, linkedID)
	if len(beforeLinked) != 1 {
		t.Fatalf("linked active consume count = %d, want 1", len(beforeLinked))
	}
	assertNoConsumeLedgerEvent(t, db, unlinkedID)
	assertNoConsumeLedgerEvent(t, db, incomeID)

	handler := testBulkReserveHandler(db)
	rr := bulkDeleteRequestForReserveTest(t, handler, []string{linkedID, unlinkedID, incomeID})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	if got := countReversalsForConsume(t, db, beforeLinked[0].ID); got != 1 {
		t.Fatalf("linked reversal count = %d, want 1", got)
	}
	if got := countReversalsBySourceTx(t, db, unlinkedID); got != 0 {
		t.Fatalf("unlinked reversal count = %d, want 0", got)
	}
	if got := countReversalsBySourceTx(t, db, incomeID); got != 0 {
		t.Fatalf("income reversal count = %d, want 0", got)
	}
}

func TestBulkDeleteFailsAndRollsBackReserveAdjustmentsWhenAnyItemIsImmutable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	mutableID := createExpenseForReserveTest(t, db, "Bulk Delete Rollback", "checking-test", "cat-expense-direct", 900)
	beforeMutable := activeConsumesBySourceTx(t, db, mutableID)
	if len(beforeMutable) != 1 {
		t.Fatalf("mutable active consume count = %d, want 1", len(beforeMutable))
	}

	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE transactions SET category_id = 'cat-expense-direct' WHERE id = 'purchase-test'`)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ?, paid_amount = 25000 WHERE id = 'invoice-2026-08'`, now)

	handler := testBulkReserveHandler(db)
	rr := bulkDeleteRequestForReserveTest(t, handler, []string{mutableID, "purchase-test"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusConflict, rr.Body.String())
	}

	assertTransactionExists(t, db, mutableID)
	assertTransactionExists(t, db, "purchase-test")
	afterMutable := activeConsumesBySourceTx(t, db, mutableID)
	if len(afterMutable) != 1 || afterMutable[0].ID != beforeMutable[0].ID {
		t.Fatalf("mutable active consume changed after rollback: before=%#v after=%#v", beforeMutable, afterMutable)
	}
	if got := countReversalsBySourceTx(t, db, mutableID); got != 0 {
		t.Fatalf("mutable reversal count after rollback = %d, want 0", got)
	}
}

func TestBulkDeleteCardExpenseKeepsCompetencyReserveContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	cardExpenseID := createExpenseForReserveTest(t, db, "Bulk Delete Card Expense", "card-test", "cat-expense-direct", 1800)
	before := activeConsumesBySourceTx(t, db, cardExpenseID)
	if len(before) != 1 {
		t.Fatalf("card active consume count = %d, want 1", len(before))
	}

	handler := testBulkReserveHandler(db)
	rr := bulkDeleteRequestForReserveTest(t, handler, []string{cardExpenseID})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("card reversal count = %d, want 1", got)
	}
}

func TestBulkDeleteWorkspaceIsolationDoesNotTouchForeignWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	localID := createExpenseForReserveTest(t, db, "Bulk Delete Isolation Local", "checking-test", "cat-expense-direct", 800)
	localBefore := activeConsumesBySourceTx(t, db, localID)
	if len(localBefore) != 1 {
		t.Fatalf("local active consume count = %d, want 1", len(localBefore))
	}

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('b-checking', 'ws-b', 'Conta B', 'CHECKING', 1000, 1000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('b-expense-1', 'ws-b', 'user-b', 'b-checking', 'b-cat-expense', 'EXPENSE', 500, ?, 'Despesa B', 'paid', 1, 1, ?, ?)
	`, now, now, now)

	handler := testBulkReserveHandler(db)
	rr := bulkDeleteRequestForReserveTest(t, handler, []string{localID, "b-expense-1"})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusForbidden, rr.Body.String())
	}

	assertTransactionExists(t, db, localID)
	assertTransactionExists(t, db, "b-expense-1")
	localAfter := activeConsumesBySourceTx(t, db, localID)
	if len(localAfter) != 1 || localAfter[0].ID != localBefore[0].ID {
		t.Fatalf("local reserve changed on forbidden bulk delete: before=%#v after=%#v", localBefore, localAfter)
	}
}

func TestBulkDeleteIncomeAndTransferDoNotCreateReserveEvents(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	incomeID := createTransactionForReserveTest(t, db, "INCOME", "Bulk Delete Income", "checking-test", "", "cat-income", 1000)
	transferID := createTransactionForReserveTest(t, db, "TRANSFER", "Bulk Delete Transfer", "checking-test", "checking-extra", "", 1000)

	handler := testBulkReserveHandler(db)
	rr := bulkDeleteRequestForReserveTest(t, handler, []string{incomeID, transferID})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	if got := countConsumesBySourceTx(t, db, incomeID); got != 0 {
		t.Fatalf("income consume count = %d, want 0", got)
	}
	if got := countConsumesBySourceTx(t, db, transferID); got != 0 {
		t.Fatalf("transfer consume count = %d, want 0", got)
	}
	if got := countReversalsBySourceTx(t, db, incomeID); got != 0 {
		t.Fatalf("income reversal count = %d, want 0", got)
	}
	if got := countReversalsBySourceTx(t, db, transferID); got != 0 {
		t.Fatalf("transfer reversal count = %d, want 0", got)
	}
}

func TestBulkUpdateStatusDoesNotCreateExtraReserveEventsOnResend(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	expenseID := createExpenseForReserveTest(t, db, "Bulk Update Resend", "checking-test", "cat-expense-direct", 1000)
	beforeActive := activeConsumesBySourceTx(t, db, expenseID)
	if len(beforeActive) != 1 {
		t.Fatalf("active consume count before bulk update = %d, want 1", len(beforeActive))
	}

	handler := testBulkReserveHandler(db)
	rr := bulkUpdateStatusRequestForReserveTest(t, handler, []string{expenseID}, "pending")
	if rr.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%q", rr.Code, rr.Body.String())
	}
	rr = bulkUpdateStatusRequestForReserveTest(t, handler, []string{expenseID}, "pending")
	if rr.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%q", rr.Code, rr.Body.String())
	}

	afterActive := activeConsumesBySourceTx(t, db, expenseID)
	if len(afterActive) != 1 || afterActive[0].ID != beforeActive[0].ID {
		t.Fatalf("active consume changed after bulk status resend: before=%#v after=%#v", beforeActive, afterActive)
	}
	if got := countReversalsBySourceTx(t, db, expenseID); got != 0 {
		t.Fatalf("reversal count after bulk status resend = %d, want 0", got)
	}
}

func bulkDeleteRequestForReserveTest(t *testing.T, handler TransactionHandler, ids []string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	for _, id := range ids {
		values.Add("ids[]", id)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/bulk/delete", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandleBulkDelete(rr, req)
	return rr
}

func bulkUpdateStatusRequestForReserveTest(t *testing.T, handler TransactionHandler, ids []string, status string) *httptest.ResponseRecorder {
	t.Helper()
	values := url.Values{}
	values.Set("status_pagamento", status)
	for _, id := range ids {
		values.Add("ids[]", id)
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/bulk/update", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandleBulkUpdate(rr, req)
	return rr
}

func assertTransactionRemoved(t *testing.T, db *sql.DB, txID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, txID).Scan(&count); err != nil {
		t.Fatalf("count removed transaction: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction %s still exists", txID)
	}
}

func assertTransactionExists(t *testing.T, db *sql.DB, txID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, txID).Scan(&count); err != nil {
		t.Fatalf("count existing transaction: %v", err)
	}
	if count != 1 {
		t.Fatalf("transaction %s count = %d, want 1", txID, count)
	}
}

func testBulkReserveHandler(db *sql.DB) TransactionHandler {
	return TransactionHandler{
		DB:          db,
		Templates:   testBulkReserveTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
}

func testBulkReserveTemplates() *template.Template {
	return template.Must(template.New("bulk").Parse(`
{{define "lancamentos-table-body"}}<tbody id="transactions-body"></tbody>{{end}}
`))
}
