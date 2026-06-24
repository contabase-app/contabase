package services

import (
	"database/sql"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestCalculateWorkspaceReserveBalancePositiveFreeBalance(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now().Unix()
	seedReserveWorkspace(t, db, "ws-a", now)

	insertReserveAccount(t, db, "a-checking", "ws-a", models.AccountTypeChecking, 10000, now)
	insertReserveAccount(t, db, "a-savings", "ws-a", models.AccountTypeSavings, 5000, now)
	insertReserveAccount(t, db, "a-card", "ws-a", models.AccountTypeCreditCard, 999999, now)
	insertReserveBox(t, db, "a-box-1", "ws-a", "a-cat-1", now)
	insertReserveBox(t, db, "a-box-2", "ws-a", "a-cat-2", now)
	insertReserveBox(t, db, "a-box-empty", "ws-a", "a-cat-3", now)
	insertReserveLedger(t, db, "a-ledger-1", "a-box-1", 3000, now)
	insertReserveLedger(t, db, "a-ledger-2", "a-box-2", 2000, now)
	insertReserveLedger(t, db, "a-ledger-3", "a-box-2", 500, now)

	got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate reserve balance: %v", err)
	}

	assertReserveBalance(t, got, 15000, 5500, 9500)
}

func TestCalculateWorkspaceReserveBalanceZeroFreeBalance(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now().Unix()
	seedReserveWorkspace(t, db, "ws-a", now)

	insertReserveAccount(t, db, "a-checking", "ws-a", models.AccountTypeChecking, 5000, now)
	insertReserveBox(t, db, "a-box-1", "ws-a", "a-cat-1", now)
	insertReserveLedger(t, db, "a-ledger-1", "a-box-1", 5000, now)

	got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate reserve balance: %v", err)
	}

	assertReserveBalance(t, got, 5000, 5000, 0)
}

func TestCalculateWorkspaceReserveBalanceNegativeFreeBalance(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now().Unix()
	seedReserveWorkspace(t, db, "ws-a", now)

	insertReserveAccount(t, db, "a-checking", "ws-a", models.AccountTypeChecking, 4000, now)
	insertReserveBox(t, db, "a-box-1", "ws-a", "a-cat-1", now)
	insertReserveLedger(t, db, "a-ledger-1", "a-box-1", 6000, now)

	got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate reserve balance: %v", err)
	}

	assertReserveBalance(t, got, 4000, 6000, -2000)
}

func TestCalculateWorkspaceReserveBalanceIsolatesWorkspace(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now().Unix()
	seedReserveWorkspace(t, db, "ws-a", now)
	seedReserveWorkspace(t, db, "ws-b", now)

	insertReserveAccount(t, db, "a-checking", "ws-a", models.AccountTypeChecking, 10000, now)
	insertReserveAccount(t, db, "a-card", "ws-a", models.AccountTypeCreditCard, 30000, now)
	insertReserveBox(t, db, "a-box-1", "ws-a", "a-cat-1", now)
	insertReserveLedger(t, db, "a-ledger-1", "a-box-1", 2000, now)

	insertReserveAccount(t, db, "b-checking", "ws-b", models.AccountTypeChecking, 90000, now)
	insertReserveAccount(t, db, "b-savings", "ws-b", models.AccountTypeSavings, 9000, now)
	insertReserveBox(t, db, "b-box-1", "ws-b", "b-cat-1", now)
	insertReserveLedger(t, db, "b-ledger-1", "b-box-1", 77000, now)

	got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate reserve balance: %v", err)
	}

	assertReserveBalance(t, got, 10000, 2000, 8000)
}

func seedReserveWorkspace(t *testing.T, db *sql.DB, workspaceID string, now int64) {
	t.Helper()
	exec(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, workspaceID, workspaceID, now, now)
	for _, categoryID := range []string{workspaceID[3:] + "-cat-1", workspaceID[3:] + "-cat-2", workspaceID[3:] + "-cat-3"} {
		exec(t, db, `
			INSERT INTO categories (id, workspace_id, name, type, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, categoryID, workspaceID, categoryID, models.TransactionTypeExpense, now)
	}
}

func insertReserveAccount(t *testing.T, db *sql.DB, accountID, workspaceID, accountType string, balance int64, now int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, accountID, workspaceID, accountID, accountType, balance, balance, now, now)
}

func insertReserveBox(t *testing.T, db *sql.DB, boxID, workspaceID, categoryID string, now int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, 0, ?, ?)
	`, boxID, workspaceID, boxID, categoryID, now, now)
}

func insertReserveLedger(t *testing.T, db *sql.DB, ledgerID, boxID string, amount int64, now int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES (?, ?, ?, 'RECHARGE', 'teste', ?, ?)
	`, ledgerID, boxID, amount, now, now)
}

func assertReserveBalance(t *testing.T, got WorkspaceReserveBalance, real, reserved, free int64) {
	t.Helper()
	if got.RealBalance != real || got.ReservedBalance != reserved || got.FreeBalance != free {
		t.Fatalf("balance = real:%d reserved:%d free:%d, want real:%d reserved:%d free:%d",
			got.RealBalance, got.ReservedBalance, got.FreeBalance, real, reserved, free)
	}
}
