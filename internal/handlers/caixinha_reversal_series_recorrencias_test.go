package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSeriesFutureUpdateAmountAdjustsReserveEventsForFutureInstallments(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	rootID, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Valor", "checking-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":            "15,00",
		"descricao":        "Serie Future Valor",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, rootID, "box-direct", -1000, 1, 0)
	assertSeriesReserveState(t, db, secondID, "box-direct", -1500, 2, 1)
	assertSeriesReserveState(t, db, thirdID, "box-direct", -1500, 2, 1)
}

func TestSeriesFutureUpdateToUnlinkedCategoryOnlyCreatesCompensation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	rootID, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Sem Caixinha", "checking-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":            "10,00",
		"descricao":        "Serie Future Sem Caixinha",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-unlinked",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, rootID, "box-direct", -1000, 1, 0)
	assertNoActiveConsumeForSourceTx(t, db, secondID)
	assertNoActiveConsumeForSourceTx(t, db, thirdID)
	if got := countConsumesBySourceTx(t, db, secondID); got != 1 {
		t.Fatalf("second consume history = %d, want 1", got)
	}
	if got := countConsumesBySourceTx(t, db, thirdID); got != 1 {
		t.Fatalf("third consume history = %d, want 1", got)
	}
	if got := countReversalsBySourceTx(t, db, secondID); got != 1 {
		t.Fatalf("second reversal count = %d, want 1", got)
	}
	if got := countReversalsBySourceTx(t, db, thirdID); got != 1 {
		t.Fatalf("third reversal count = %d, want 1", got)
	}
}

func TestSeriesFutureUpdateToDifferentBoxReversesAndConsumesNewBox(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	_, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Troca Caixa", "checking-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":            "13,00",
		"descricao":        "Serie Future Troca Caixa",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-parent",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, secondID, "box-parent", -1300, 2, 1)
	assertSeriesReserveState(t, db, thirdID, "box-parent", -1300, 2, 1)
}

func TestSeriesAllScopeAdjustsReserveForAllInstallments(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	rootID, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie All Troca Caixa", "checking-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "all", map[string]string{
		"valor":            "12,00",
		"descricao":        "Serie All Troca Caixa",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-parent",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, rootID, "box-parent", -1200, 2, 1)
	assertSeriesReserveState(t, db, secondID, "box-parent", -1200, 2, 1)
	assertSeriesReserveState(t, db, thirdID, "box-parent", -1200, 2, 1)
}

func TestSeriesFutureInsufficientReserveRollsBackAllChanges(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	rootID, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Sem Reserva", "checking-test", "cat-expense-direct", 3000, 3)
	beforeSecond := activeConsumesBySourceTx(t, db, secondID)
	beforeThird := activeConsumesBySourceTx(t, db, thirdID)
	if len(beforeSecond) != 1 || len(beforeThird) != 1 {
		t.Fatalf("expected one active consume before failed update")
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":            "7,00",
		"descricao":        "Serie Future Sem Reserva",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-low",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusUnprocessableEntity, rr.Body.String())
	}

	assertSeriesReserveState(t, db, rootID, "box-direct", -1000, 1, 0)
	assertSeriesReserveState(t, db, secondID, "box-direct", -1000, 1, 0)
	assertSeriesReserveState(t, db, thirdID, "box-direct", -1000, 1, 0)
	if got := countReversalsBySourceTx(t, db, secondID); got != 0 {
		t.Fatalf("second reversal count after rollback = %d, want 0", got)
	}
	if got := countReversalsBySourceTx(t, db, thirdID); got != 0 {
		t.Fatalf("third reversal count after rollback = %d, want 0", got)
	}
}

func TestSeriesFutureInsufficientReserveAllowsOverdraftWhenConfirmed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	rootID, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Excedente Confirmado", "checking-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":                       "7,00",
		"descricao":                   "Serie Future Excedente Confirmado",
		"data":                        "2026-08-05",
		"tipo":                        "despesa",
		"origem_conta_id":             "checking-test",
		"categoria_id":                "cat-expense-low",
		"status_pagamento":            "pending",
		"escopo":                      "future",
		"permitir_excedente_caixinha": "1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, rootID, "box-direct", -1000, 1, 0)
	assertSeriesReserveState(t, db, secondID, "box-low", -700, 2, 1)
	assertSeriesReserveState(t, db, thirdID, "box-low", -700, 2, 1)

	var lowReserved int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'box-low'`).Scan(&lowReserved); err != nil {
		t.Fatalf("query low reserved after future confirmed overdraft: %v", err)
	}
	if lowReserved >= 0 {
		t.Fatalf("expected negative reserved after future confirmed overdraft, got %d", lowReserved)
	}
}

func TestSeriesFutureCardExpenseKeepsCompetencyReserveConsumption(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	_, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Cartao", "card-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":            "16,00",
		"descricao":        "Serie Future Cartao",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, secondID, "box-direct", -1600, 2, 1)
	assertSeriesReserveState(t, db, thirdID, "box-direct", -1600, 2, 1)
}

func TestSeriesFutureWorkspaceIsolationDoesNotTouchForeignLedger(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	_, secondID, _ := createInstallmentSeriesForReserveTest(t, db, "Serie Future Isolamento", "checking-test", "cat-expense-direct", 3000, 3)

	var beforeForeign int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'b-box-1'`).Scan(&beforeForeign); err != nil {
		t.Fatalf("query foreign before: %v", err)
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", map[string]string{
		"valor":            "16,00",
		"descricao":        "Serie Future Isolamento",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	var afterForeign int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'b-box-1'`).Scan(&afterForeign); err != nil {
		t.Fatalf("query foreign after: %v", err)
	}
	if afterForeign != beforeForeign {
		t.Fatalf("foreign reserve changed: got=%d want=%d", afterForeign, beforeForeign)
	}
}

func TestSeriesFutureIncomeAndTransferRemainWithoutReserveConsume(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	incomeRoot, incomeSecond, _ := createInstallmentSeriesForReserveTest(t, db, "Serie Future Receita", "checking-test", "cat-income", 3000, 3, "INCOME")
	transferRoot, transferSecond, _ := createTransferSeriesForReserveTest(t, db, "Serie Future Transferencia", "checking-test", "checking-extra", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, incomeSecond, "future", map[string]string{
		"valor":            "20,00",
		"descricao":        "Serie Future Receita",
		"data":             "2026-08-05",
		"tipo":             "receita",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-income",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("income status = %d body=%q", rr.Code, rr.Body.String())
	}

	rr = updateTransactionForReserveScopeTest(t, handler, transferSecond, "future", map[string]string{
		"valor":            "20,00",
		"descricao":        "Serie Future Transferencia",
		"data":             "2026-08-05",
		"tipo":             "transferencia",
		"origem_conta_id":  "checking-test",
		"destino_conta_id": "checking-extra",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("transfer status = %d body=%q", rr.Code, rr.Body.String())
	}

	if got := countConsumesBySourceTx(t, db, incomeRoot); got != 0 {
		t.Fatalf("income root consume count = %d, want 0", got)
	}
	if got := countConsumesBySourceTx(t, db, incomeSecond); got != 0 {
		t.Fatalf("income future consume count = %d, want 0", got)
	}
	if got := countConsumesBySourceTx(t, db, transferRoot); got != 0 {
		t.Fatalf("transfer root consume count = %d, want 0", got)
	}
	if got := countConsumesBySourceTx(t, db, transferSecond); got != 0 {
		t.Fatalf("transfer future consume count = %d, want 0", got)
	}
	if got := countReversalsBySourceTx(t, db, incomeSecond); got != 0 {
		t.Fatalf("income future reversal count = %d, want 0", got)
	}
	if got := countReversalsBySourceTx(t, db, transferSecond); got != 0 {
		t.Fatalf("transfer future reversal count = %d, want 0", got)
	}
}

func TestSeriesFutureResendSameChangeDoesNotCreateExtraEvents(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	_, secondID, thirdID := createInstallmentSeriesForReserveTest(t, db, "Serie Future Reenvio", "checking-test", "cat-expense-direct", 3000, 3)

	handler := testPaidInvoiceMutationHandler(db)
	fields := map[string]string{
		"valor":            "15,00",
		"descricao":        "Serie Future Reenvio",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "pending",
		"escopo":           "future",
	}

	rr := updateTransactionForReserveScopeTest(t, handler, secondID, "future", fields)
	if rr.Code != http.StatusOK {
		t.Fatalf("first status = %d body=%q", rr.Code, rr.Body.String())
	}
	rr = updateTransactionForReserveScopeTest(t, handler, secondID, "future", fields)
	if rr.Code != http.StatusOK {
		t.Fatalf("second status = %d body=%q", rr.Code, rr.Body.String())
	}

	assertSeriesReserveState(t, db, secondID, "box-direct", -1500, 2, 1)
	assertSeriesReserveState(t, db, thirdID, "box-direct", -1500, 2, 1)
}

func TestRecurringAllUpdateAdjustsReserveAcrossRuleTransactions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handlerInsert := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(3)
	_, err := handlerInsert.insertTransaction(
		"EXPENSE",
		3000,
		"Recorrencia All Caixinha",
		"",
		"",
		time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC).Unix(),
		"checking-test",
		"",
		"cat-expense-direct",
		1,
		"pending",
		true,
		"MONTHLY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insert recurring transaction: %v", err)
	}

	ids := recurringTransactionIDsByDescription(t, db, "Recorrencia All Caixinha")
	if len(ids) != 3 {
		t.Fatalf("recurring tx count = %d, want 3", len(ids))
	}
	beforeConsumeCount := make(map[string]int, len(ids))
	for _, txID := range ids {
		beforeConsumeCount[txID] = countConsumesBySourceTx(t, db, txID)
	}

	handler := testPaidInvoiceMutationHandler(db)
	rr := updateTransactionForReserveScopeTest(t, handler, ids[0], "all", map[string]string{
		"valor":            "12,00",
		"descricao":        "Recorrencia All Caixinha",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-parent",
		"status_pagamento": "pending",
		"recorrencia":      "MONTHLY",
		"escopo":           "all",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}

	for _, txID := range ids {
		active := activeConsumesBySourceTx(t, db, txID)
		if len(active) != 1 {
			t.Fatalf("active consumes for %s = %d, want 1", txID, len(active))
		}
		if active[0].BoxID != "box-parent" || active[0].Amount != -1200 {
			t.Fatalf("active consume for %s = %#v, want box-parent/-1200", txID, active[0])
		}
		switch beforeConsumeCount[txID] {
		case 0:
			if got := countConsumesBySourceTx(t, db, txID); got != 1 {
				t.Fatalf("consume history for %s = %d, want 1", txID, got)
			}
			if got := countReversalsBySourceTx(t, db, txID); got != 0 {
				t.Fatalf("reversal count for %s = %d, want 0", txID, got)
			}
		default:
			if got := countConsumesBySourceTx(t, db, txID); got != beforeConsumeCount[txID]+1 {
				t.Fatalf("consume history for %s = %d, want %d", txID, got, beforeConsumeCount[txID]+1)
			}
			if got := countReversalsBySourceTx(t, db, txID); got != 1 {
				t.Fatalf("reversal count for %s = %d, want 1", txID, got)
			}
		}
	}
}

func createInstallmentSeriesForReserveTest(t *testing.T, db *sql.DB, description, accountID, categoryID string, totalAmount, totalInstallments int64, overrideType ...string) (string, string, string) {
	t.Helper()
	txType := "EXPENSE"
	if len(overrideType) > 0 && overrideType[0] != "" {
		txType = overrideType[0]
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		txType,
		totalAmount,
		description,
		"",
		"",
		time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC).Unix(),
		accountID,
		"",
		categoryID,
		totalInstallments,
		"pending",
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
		t.Fatalf("create installment series: %v", err)
	}

	rootID := installmentIDByDescriptionAndNumber(t, db, description, 1)
	secondID := installmentIDByRootAndNumber(t, db, rootID, 2)
	thirdID := installmentIDByRootAndNumber(t, db, rootID, 3)
	return rootID, secondID, thirdID
}

func createTransferSeriesForReserveTest(t *testing.T, db *sql.DB, description, sourceAccountID, destinationAccountID string, totalAmount, totalInstallments int64) (string, string, string) {
	t.Helper()

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"TRANSFER",
		totalAmount,
		description,
		"",
		"",
		time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC).Unix(),
		sourceAccountID,
		destinationAccountID,
		"",
		totalInstallments,
		"pending",
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
		t.Fatalf("create transfer series: %v", err)
	}

	rootID := installmentIDByDescriptionAndNumber(t, db, description, 1)
	secondID := installmentIDByRootAndNumber(t, db, rootID, 2)
	thirdID := installmentIDByRootAndNumber(t, db, rootID, 3)
	return rootID, secondID, thirdID
}

func installmentIDByDescriptionAndNumber(t *testing.T, db *sql.DB, description string, installmentNumber int64) string {
	t.Helper()

	var id string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test'
		  AND description = ?
		  AND installment_number = ?
		LIMIT 1
	`, description, installmentNumber).Scan(&id); err != nil {
		t.Fatalf("query installment by description/number: %v", err)
	}
	return id
}

func installmentIDByRootAndNumber(t *testing.T, db *sql.DB, rootID string, installmentNumber int64) string {
	t.Helper()

	var id string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test'
		  AND (id = ? OR parent_id = ?)
		  AND installment_number = ?
		LIMIT 1
	`, rootID, rootID, installmentNumber).Scan(&id); err != nil {
		t.Fatalf("query installment by root/number: %v", err)
	}
	return id
}

func recurringTransactionIDsByDescription(t *testing.T, db *sql.DB, description string) []string {
	t.Helper()

	rows, err := db.Query(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test'
		  AND description = ?
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

func updateTransactionForReserveScopeTest(t *testing.T, handler TransactionHandler, transactionID, scope string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := newMultipartUpdateRequest(t, "/transacoes/"+transactionID+"/salvar?escopo="+scope, fields)
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, transactionID)
	return rr
}

func assertSeriesReserveState(t *testing.T, db *sql.DB, txID, wantBoxID string, wantActiveAmount int64, wantConsumeCount, wantReversalCount int) {
	t.Helper()

	active := activeConsumesBySourceTx(t, db, txID)
	if len(active) != 1 {
		t.Fatalf("active consumes for %s = %d, want 1", txID, len(active))
	}
	if active[0].BoxID != wantBoxID || active[0].Amount != wantActiveAmount {
		t.Fatalf("active consume for %s = %#v, want box=%s amount=%d", txID, active[0], wantBoxID, wantActiveAmount)
	}
	if got := countConsumesBySourceTx(t, db, txID); got != wantConsumeCount {
		t.Fatalf("consume history for %s = %d, want %d", txID, got, wantConsumeCount)
	}
	if got := countReversalsBySourceTx(t, db, txID); got != wantReversalCount {
		t.Fatalf("reversal count for %s = %d, want %d", txID, got, wantReversalCount)
	}
}

func assertNoActiveConsumeForSourceTx(t *testing.T, db *sql.DB, txID string) {
	t.Helper()
	active := activeConsumesBySourceTx(t, db, txID)
	if len(active) != 0 {
		t.Fatalf("active consumes for %s = %d, want 0", txID, len(active))
	}
}
