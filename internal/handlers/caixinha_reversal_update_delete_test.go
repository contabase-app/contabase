package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

type consumeLedgerState struct {
	ID     string
	BoxID  string
	Amount int64
}

func TestUpdateExpenseAmountCreatesReversalAndNewConsume(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Update Valor", "checking-test", "cat-expense-direct", 1200)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before update = %d, want 1", len(before))
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":            "18,00",
		"descricao":        "Despesa Update Valor",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 1 {
		t.Fatalf("active consumes after update = %d, want 1", len(after))
	}
	if after[0].BoxID != "box-direct" || after[0].Amount != -1800 {
		t.Fatalf("active consume after update = %#v, want box-direct/-1800", after[0])
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("reversals for old consume = %d, want 1", got)
	}
	if got := countConsumesBySourceTx(t, db, txID); got != 2 {
		t.Fatalf("consume history count = %d, want 2", got)
	}
}

func TestUpdateExpenseCategoryToUnlinkedOnlyReverses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Troca Sem Caixinha", "checking-test", "cat-expense-direct", 1000)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before update = %d, want 1", len(before))
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":            "10,00",
		"descricao":        "Despesa Troca Sem Caixinha",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-unlinked",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 0 {
		t.Fatalf("active consumes after unlinked category = %d, want 0", len(after))
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("reversals for old consume = %d, want 1", got)
	}
}

func TestUpdateExpenseCategoryToDifferentBoxReversesAndConsumes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Troca Caixinha", "checking-test", "cat-expense-direct", 1100)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before update = %d, want 1", len(before))
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":            "11,00",
		"descricao":        "Despesa Troca Caixinha",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-parent",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 1 {
		t.Fatalf("active consumes after update = %d, want 1", len(after))
	}
	if after[0].BoxID != "box-parent" || after[0].Amount != -1100 {
		t.Fatalf("active consume after update = %#v, want box-parent/-1100", after[0])
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("reversals for old consume = %d, want 1", got)
	}
}

func TestDeleteSingleExpenseCreatesReversalForActiveConsume(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Cancelar", "checking-test", "cat-expense-direct", 900)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before delete = %d, want 1", len(before))
	}

	handler := testPaidInvoiceMutationHandler(db)
	req := httptest.NewRequest(http.MethodDelete, "/transacoes/"+txID, nil)
	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, req, txID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = ?`, txID, "ws-test").Scan(&count); err != nil {
		t.Fatalf("count deleted transaction: %v", err)
	}
	if count != 0 {
		t.Fatalf("transaction still exists after delete: %d", count)
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("reversals for consumed event on delete = %d, want 1", got)
	}
}

func TestResubmitSameUpdateDoesNotDuplicateReversal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Reenvio", "checking-test", "cat-expense-direct", 1000)
	handler := testPaidInvoiceMutationHandler(db)

	fields := map[string]string{
		"valor":            "15,00",
		"descricao":        "Despesa Reenvio",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
	}

	rr := updateTransactionForReserveTest(t, handler, txID, fields)
	if rr.Code != http.StatusOK {
		t.Fatalf("first update status = %d body=%q", rr.Code, rr.Body.String())
	}
	rr = updateTransactionForReserveTest(t, handler, txID, fields)
	if rr.Code != http.StatusOK {
		t.Fatalf("second update status = %d body=%q", rr.Code, rr.Body.String())
	}

	if got := countReversalsBySourceTx(t, db, txID); got != 1 {
		t.Fatalf("reversal count after resend = %d, want 1", got)
	}
	if got := countConsumesBySourceTx(t, db, txID); got != 2 {
		t.Fatalf("consume history count after resend = %d, want 2", got)
	}
	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 1 || after[0].Amount != -1500 {
		t.Fatalf("active consume after resend = %#v, want one consume -1500", after)
	}
}

func TestUpdateToBoxWithoutReserveFailsAndRollsBackLedgerAdjustments(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Sem Reserva Na Nova Caixa", "checking-test", "cat-expense-direct", 300)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before update = %d, want 1", len(before))
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":            "7,00",
		"descricao":        "Despesa Sem Reserva Na Nova Caixa",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-low",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusUnprocessableEntity, rr.Body.String())
	}

	var categoryID sql.NullString
	if err := db.QueryRow(`SELECT category_id FROM transactions WHERE id = ? AND workspace_id = ?`, txID, "ws-test").Scan(&categoryID); err != nil {
		t.Fatalf("query category after failed update: %v", err)
	}
	if !categoryID.Valid || categoryID.String != "cat-expense-direct" {
		t.Fatalf("category changed after failed update: %#v", categoryID)
	}
	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("active consume changed after failed update: before=%#v after=%#v", before, after)
	}
	if got := countReversalsBySourceTx(t, db, txID); got != 0 {
		t.Fatalf("reversal count after failed update = %d, want 0", got)
	}
}

func TestUpdateToBoxWithoutReserveAllowsOverdraftWhenConfirmed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Excedente Update", "checking-test", "cat-expense-direct", 300)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before confirmed overdraft update = %d, want 1", len(before))
	}

	beforeBalance, err := services.CalculateWorkspaceReserveBalance(db, "ws-test")
	if err != nil {
		t.Fatalf("calculate reserve before update: %v", err)
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":                       "7,00",
		"descricao":                   "Despesa Excedente Update",
		"data":                        "2026-07-05",
		"tipo":                        "despesa",
		"origem_conta_id":             "checking-test",
		"categoria_id":                "cat-expense-low",
		"status_pagamento":            "paid",
		"permitir_excedente_caixinha": "1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 1 {
		t.Fatalf("active consumes after confirmed overdraft update = %d, want 1", len(after))
	}
	if after[0].BoxID != "box-low" || after[0].Amount != -700 {
		t.Fatalf("active consume after confirmed overdraft update = %#v, want box-low/-700", after[0])
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("reversals for previous consume = %d, want 1", got)
	}

	var lowReserved int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'box-low'`).Scan(&lowReserved); err != nil {
		t.Fatalf("query low reserved after update: %v", err)
	}
	if lowReserved >= 0 {
		t.Fatalf("expected negative reserved after confirmed overdraft update, got %d", lowReserved)
	}

	afterBalance, err := services.CalculateWorkspaceReserveBalance(db, "ws-test")
	if err != nil {
		t.Fatalf("calculate reserve after update: %v", err)
	}
	if afterBalance.ReservedBalance != beforeBalance.ReservedBalance-400 {
		t.Fatalf("reserved balance after confirmed overdraft update = %d, want %d", afterBalance.ReservedBalance, beforeBalance.ReservedBalance-400)
	}
	if afterBalance.FreeBalance != beforeBalance.FreeBalance {
		t.Fatalf("free balance after confirmed overdraft update = %d, want %d", afterBalance.FreeBalance, beforeBalance.FreeBalance)
	}
	if afterBalance.FreeBalance != afterBalance.RealBalance-afterBalance.ReservedBalance {
		t.Fatalf("free balance formula mismatch: got=%d real=%d reserved=%d", afterBalance.FreeBalance, afterBalance.RealBalance, afterBalance.ReservedBalance)
	}
}

func TestUpdateWorkspaceIsolationDoesNotTouchForeignLedger(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Despesa Isolamento Update", "checking-test", "cat-expense-direct", 1000)
	var beforeForeign int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'b-box-1'`).Scan(&beforeForeign); err != nil {
		t.Fatalf("query foreign reserve before update: %v", err)
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":            "12,00",
		"descricao":        "Despesa Isolamento Update",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	var afterForeign int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'b-box-1'`).Scan(&afterForeign); err != nil {
		t.Fatalf("query foreign reserve after update: %v", err)
	}
	if afterForeign != beforeForeign {
		t.Fatalf("foreign reserve changed: got=%d want=%d", afterForeign, beforeForeign)
	}
}

func TestCardExpenseUpdateKeepsCompetencyConsume(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	txID := createExpenseForReserveTest(t, db, "Compra Cartao Editavel", "card-test", "cat-expense-direct", 2000)
	before := activeConsumesBySourceTx(t, db, txID)
	if len(before) != 1 {
		t.Fatalf("active consumes before card update = %d, want 1", len(before))
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveTest(t, handler, txID, map[string]string{
		"valor":            "25,00",
		"descricao":        "Compra Cartao Editavel",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	after := activeConsumesBySourceTx(t, db, txID)
	if len(after) != 1 || after[0].Amount != -2500 {
		t.Fatalf("active consume after card update = %#v, want one consume -2500", after)
	}
	if got := countReversalsForConsume(t, db, before[0].ID); got != 1 {
		t.Fatalf("reversals for old card consume = %d, want 1", got)
	}
}

func TestIncomeAndTransferUpdatesRemainWithoutReserveConsume(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	incomeID := createTransactionForReserveTest(t, db, "INCOME", "Receita Sem Consumo Update", "checking-test", "", "cat-income", 1000)
	transferID := createTransactionForReserveTest(t, db, "TRANSFER", "Transferencia Sem Consumo Update", "checking-test", "checking-extra", "", 900)
	handler := testPaidInvoiceMutationHandler(db)

	rr := updateTransactionForReserveTest(t, handler, incomeID, map[string]string{
		"valor":            "15,00",
		"descricao":        "Receita Sem Consumo Update",
		"data":             "2026-07-05",
		"tipo":             "receita",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-income",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("income update status = %d body=%q", rr.Code, rr.Body.String())
	}
	rr = updateTransactionForReserveTest(t, handler, transferID, map[string]string{
		"valor":            "11,00",
		"descricao":        "Transferencia Sem Consumo Update",
		"data":             "2026-07-05",
		"tipo":             "transferencia",
		"origem_conta_id":  "checking-test",
		"destino_conta_id": "checking-extra",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("transfer update status = %d body=%q", rr.Code, rr.Body.String())
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

func createExpenseForReserveTest(t *testing.T, db *sql.DB, description, accountID, categoryID string, amount int64) string {
	t.Helper()
	return createTransactionForReserveTest(t, db, "EXPENSE", description, accountID, "", categoryID, amount)
}

func createTransactionForReserveTest(t *testing.T, db *sql.DB, transactionType, description, accountID, destinationAccountID, categoryID string, amount int64) string {
	t.Helper()
	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		transactionType,
		amount,
		description,
		"",
		"",
		time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC).Unix(),
		accountID,
		destinationAccountID,
		categoryID,
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
		t.Fatalf("create transaction for reserve test: %v", err)
	}
	return findTransactionByDescription(t, db, description)
}

func updateTransactionForReserveTest(t *testing.T, handler TransactionHandler, transactionID string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := newMultipartUpdateRequest(t, "/transacoes/"+transactionID+"/salvar", fields)
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, transactionID)
	return rr
}

func activeConsumesBySourceTx(t *testing.T, db *sql.DB, sourceTransactionID string) []consumeLedgerState {
	t.Helper()
	rows, err := db.Query(`
		SELECT l.id, l.box_id, l.amount
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		WHERE b.workspace_id = 'ws-test'
		  AND l.source_transaction_id = ?
		  AND l.type = 'CONSUME'
		  AND NOT EXISTS (
			SELECT 1
			FROM box_virtual_ledger r
			WHERE r.reversal_of_ledger_id = l.id
			  AND r.type = 'REVERSAL'
		  )
		ORDER BY l.created_at ASC
	`, sourceTransactionID)
	if err != nil {
		t.Fatalf("query active consumes by source tx: %v", err)
	}
	defer rows.Close()

	var events []consumeLedgerState
	for rows.Next() {
		var event consumeLedgerState
		if err := rows.Scan(&event.ID, &event.BoxID, &event.Amount); err != nil {
			t.Fatalf("scan active consume: %v", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate active consumes: %v", err)
	}
	return events
}

func countConsumesBySourceTx(t *testing.T, db *sql.DB, sourceTransactionID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE source_transaction_id = ? AND type = 'CONSUME'`, sourceTransactionID).Scan(&count); err != nil {
		t.Fatalf("count consumes by source tx: %v", err)
	}
	return count
}

func countReversalsBySourceTx(t *testing.T, db *sql.DB, sourceTransactionID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE source_transaction_id = ? AND type = 'REVERSAL'`, sourceTransactionID).Scan(&count); err != nil {
		t.Fatalf("count reversals by source tx: %v", err)
	}
	return count
}

func countReversalsForConsume(t *testing.T, db *sql.DB, consumeLedgerID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE reversal_of_ledger_id = ? AND type = 'REVERSAL'`, consumeLedgerID).Scan(&count); err != nil {
		t.Fatalf("count reversals for consume: %v", err)
	}
	return count
}
