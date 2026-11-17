package handlers

import (
	"math"
	"testing"
	"time"
)

func TestFinancialPositionKeepsRealBalanceAndObligationsWithoutReserves(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-position")
	now := time.Now().UTC().Unix()

	addF1Invoice(t, db, "inv-position", base.card, "2099-08", "OPEN", 0, now)
	addF1Tx(t, db, f1TxSeed{
		ID: "position-card-expense", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, InvoiceID: "inv-position",
		Type: "EXPENSE", Amount: 5000, Status: "paid", CreatedAt: now,
	})
	addF1Tx(t, db, f1TxSeed{
		ID: "position-pending-expense", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.checking, CategoryID: base.category,
		Type: "EXPENSE", Amount: 3000, Status: "pending", CreatedAt: now,
	})
	got := mustF1(t, db, base.ws)
	requireF1Reliable(t, got)
	if got.RealBalance != 10000 {
		t.Fatalf("RealBalance = %d, want 10000", got.RealBalance)
	}
	if got.Obligations != 8000 {
		t.Fatalf("Obligations = %d, want 8000", got.Obligations)
	}
	if !got.ObligationsScheduleAvailable {
		t.Fatal("obligations schedule should be available")
	}

	// Removed Reservas must leave no schema residue and no effect on position.
	var reserveObjects int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE name IN ('boxes','box_virtual_ledger','box_contribution_lifecycle_events')`).Scan(&reserveObjects); err != nil {
		t.Fatal(err)
	}
	if reserveObjects != 0 {
		t.Fatalf("reserve schema objects = %d, want 0", reserveObjects)
	}
	again := mustF1(t, db, base.ws)
	if again.RealBalance != got.RealBalance || again.Obligations != got.Obligations {
		t.Fatalf("financial position changed without reserves: before=%+v after=%+v", got, again)
	}
}

func TestFinancialPositionIsolatesWorkspaces(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	a := seedF1Base(t, db, "ws-position-a")
	b := seedF1Base(t, db, "ws-position-b")
	now := time.Now().UTC().Unix()

	addF1Tx(t, db, f1TxSeed{
		ID: "position-a-expense", WorkspaceID: a.ws, UserID: a.user,
		AccountID: a.checking, CategoryID: a.category,
		Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: now,
	})
	addF1Tx(t, db, f1TxSeed{
		ID: "position-b-expense", WorkspaceID: b.ws, UserID: b.user,
		AccountID: b.checking, CategoryID: b.category,
		Type: "EXPENSE", Amount: 7000, Status: "pending", CreatedAt: now,
	})

	gotA := mustF1(t, db, a.ws)
	gotB := mustF1(t, db, b.ws)
	requireF1Reliable(t, gotA)
	requireF1Reliable(t, gotB)
	if gotA.Obligations != 1000 || gotB.Obligations != 7000 {
		t.Fatalf("workspace isolation failed: a=%d b=%d", gotA.Obligations, gotB.Obligations)
	}
}

func TestFinancialPositionDetectsRealOverflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-position-overflow")
	execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = ? AND workspace_id = ?`, int64(math.MaxInt64), base.checking, base.ws)
	addF1Account(t, db, "overflow-extra", base.ws, "SAVINGS", 1, base.now)

	got := mustF1(t, db, base.ws)
	requireF1Unavailable(t, got, "real_overflow")
	availability := financialIndicatorAvailability(got, nil)
	if availability.RealBalanceAvailable || !availability.ObligationsAvailable {
		t.Fatalf("overflow availability = %+v, want real blocked and obligations available", availability)
	}
}
