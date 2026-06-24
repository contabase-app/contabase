package handlers

import (
	"net/http"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

func TestLedgerReconciliationAfterCombinedFlows(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	updateHandler := testPaidInvoiceMutationHandler(db)
	bulkHandler := testBulkReserveHandler(db)

	// 1) Criação + ajuste individual
	singleTxID := createExpenseForReserveTest(t, db, "Reconcile Single", "checking-test", "cat-expense-direct", 1000)
	rr := updateTransactionForReserveTest(t, updateHandler, singleTxID, map[string]string{
		"valor":            "13,00",
		"descricao":        "Reconcile Single",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("single update status = %d body=%q", rr.Code, rr.Body.String())
	}

	// 2) Série (future): troca de categoria/valor para forçar REVERSAL + novo CONSUME
	_, seriesSecondID, _ := createInstallmentSeriesForReserveTest(t, db, "Reconcile Series", "checking-test", "cat-expense-direct", 3000, 3)
	rr = updateTransactionForReserveScopeTest(t, updateHandler, seriesSecondID, "future", map[string]string{
		"valor":            "14,00",
		"descricao":        "Reconcile Series",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-parent",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("series future update status = %d body=%q", rr.Code, rr.Body.String())
	}

	// 3) Bulk delete: item com consume ativo + item sem consume
	bulkLinkedID := createExpenseForReserveTest(t, db, "Reconcile Bulk Linked", "checking-test", "cat-expense-direct", 900)
	bulkUnlinkedID := createExpenseForReserveTest(t, db, "Reconcile Bulk Unlinked", "checking-test", "cat-expense-unlinked", 700)
	rr = bulkDeleteRequestForReserveTest(t, bulkHandler, []string{bulkLinkedID, bulkUnlinkedID})
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk delete status = %d body=%q", rr.Code, rr.Body.String())
	}

	// 4) Cartão por competência (sem quebra de reconciliação)
	cardTxID := createExpenseForReserveTest(t, db, "Reconcile Card", "card-test", "cat-expense-direct", 1600)
	rr = updateTransactionForReserveTest(t, updateHandler, cardTxID, map[string]string{
		"valor":            "17,00",
		"descricao":        "Reconcile Card",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "card-test",
		"categoria_id":     "cat-expense-direct",
		"status_pagamento": "paid",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("card update status = %d body=%q", rr.Code, rr.Body.String())
	}

	// 5) Reconciliação consolidada por workspace
	reportA, err := services.ReconcileWorkspaceBoxLedger(db, "ws-test")
	if err != nil {
		t.Fatalf("reconcile ws-test: %v", err)
	}
	if reportA.HasIssues() {
		t.Fatalf("reconcile ws-test found issues: %+v", reportA.Issues)
	}

	// Workspace B foi semeado no cenário e não pode ser afetado pelos fluxos de ws-test.
	reportB, err := services.ReconcileWorkspaceBoxLedger(db, "ws-b")
	if err != nil {
		t.Fatalf("reconcile ws-b: %v", err)
	}
	if reportB.HasIssues() {
		t.Fatalf("reconcile ws-b found issues: %+v", reportB.Issues)
	}
	if got := reportB.BoxTotals["b-box-1"]; got != 9999 {
		t.Fatalf("ws-b reserved total changed: got=%d want=9999", got)
	}

	// 6) Consulta por source_transaction_id deve refletir histórico append-only da transação individual.
	bySource, err := services.ListBoxLedgerBySourceTransaction(db, "ws-test", singleTxID)
	if err != nil {
		t.Fatalf("list by source singleTx: %v", err)
	}
	if len(bySource) < 2 {
		t.Fatalf("source events count = %d, want >= 2", len(bySource))
	}
	activeCount := 0
	for _, evt := range bySource {
		if evt.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("active source events count = %d, want 1", activeCount)
	}
}

func TestLedgerReconciliationAllowsConfirmedOverdraftWithoutInconsistency(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		600,
		"Reconcile Overdraft Confirmed",
		"",
		"",
		testUnixDate("2026-08-05"),
		"checking-test",
		"",
		"cat-expense-low",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		true,
	)
	if err != nil {
		t.Fatalf("insert overdraft confirmed expense: %v", err)
	}

	report, err := services.ReconcileWorkspaceBoxLedger(db, "ws-test")
	if err != nil {
		t.Fatalf("reconcile ws-test: %v", err)
	}
	if report.HasIssues() {
		t.Fatalf("reconcile ws-test found issues for confirmed overdraft: %+v", report.Issues)
	}
}
