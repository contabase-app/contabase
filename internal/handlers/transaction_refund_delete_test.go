package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransactionRefundCascadeThroughProductionDelete(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	for _, id := range []string{"refund-a", "refund-b"} {
		execTestSQL(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES(?, 'ws-test', 'purchase-test', 1000, 'refund', 'user-test')`, id)
	}
	if _, err := db.Exec(`DELETE FROM transaction_refunds WHERE id='refund-a'`); err == nil {
		t.Fatal("direct refund DELETE succeeded")
	}
	h := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	h.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/purchase-test", nil), "purchase-test")
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%q", rr.Code, rr.Body.String())
	}
	var transactions, refunds int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE id='purchase-test'`).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE transaction_id='purchase-test'`).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if transactions != 0 || refunds != 0 {
		t.Fatalf("after handler DELETE transactions=%d refunds=%d", transactions, refunds)
	}
}
