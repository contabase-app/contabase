package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExpenseCreationHasNoReserveDependency(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-reserve-write")
	var reserveObjects int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE name IN ('boxes','box_virtual_ledger','box_contribution_lifecycle_events')`).Scan(&reserveObjects); err != nil {
		t.Fatal(err)
	}
	if reserveObjects != 0 {
		t.Fatalf("reserve schema objects = %d, want 0", reserveObjects)
	}

	h := TransactionHandler{DB: db, WorkspaceID: base.ws, UserID: base.user}
	_, err := h.insertTransaction(
		"EXPENSE", 1500, "Despesa sem reserva", "", "",
		MustCivilWriteDate("2099-07-10"), base.checking, "", base.category,
		1, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "",
	)
	if err != nil {
		t.Fatalf("expense creation must not be blocked by reserve: %v", err)
	}
	var id string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id = ? AND description = ?`, base.ws, "Despesa sem reserva").Scan(&id); err != nil {
		t.Fatalf("locate created expense: %v", err)
	}
	var current int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ? AND workspace_id = ?`, base.checking, base.ws).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 8500 {
		t.Fatalf("checking balance = %d, want 8500", current)
	}
	// Updating a real expense must not create reserve events either.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.updateSingleTransactionInTx(tx, id, "EXPENSE", 1200, MustCivilWriteDate("2099-07-11").LegacyUnix(), "Despesa sem reserva atualizada", "", "", base.checking, "", base.category, "paid", time.Now().Unix()); err != nil {
		tx.Rollback()
		t.Fatalf("expense update: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Deleting a real expense must remove its financial effect without
	// creating virtual CONSUME/REVERSAL events.
	del := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: base.ws, UserID: base.user}
	rr := httptest.NewRecorder()
	del.HandleDeletarTransacao(rr, httptest.NewRequest(http.MethodDelete, "/transacoes/"+id, nil), id)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ? AND workspace_id = ?`, id, base.ws).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 0 {
		t.Fatalf("transaction %s was not deleted", id)
	}
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ? AND workspace_id = ?`, base.checking, base.ws).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current != 10000 {
		t.Fatalf("checking balance after delete = %d, want 10000", current)
	}
}
