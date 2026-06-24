package services

import (
	"database/sql"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
)

func TestApplyBalanceEffectScenarios(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedAccounts(t, db)

	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	if err := ApplyBalanceEffect(tx, "ws", models.TransactionTypeExpense, models.AccountTypeChecking, models.PaymentStatusPaid, 1000, "checking-1", "", now); err != nil {
		t.Fatalf("apply expense paid: %v", err)
	}
	if err := ApplyBalanceEffect(tx, "ws", models.TransactionTypeIncome, models.AccountTypeChecking, models.PaymentStatusPaid, 2000, "checking-1", "", now); err != nil {
		t.Fatalf("apply income paid: %v", err)
	}
	if err := ApplyBalanceEffect(tx, "ws", models.TransactionTypeExpense, models.AccountTypeChecking, models.PaymentStatusPending, 500, "checking-1", "", now); err != nil {
		t.Fatalf("apply expense pending: %v", err)
	}
	if err := ApplyBalanceEffect(tx, "ws", models.TransactionTypeExpense, models.AccountTypeCreditCard, models.PaymentStatusPaid, 700, "card-1", "", now); err != nil {
		t.Fatalf("apply expense credit card: %v", err)
	}
	if err := ApplyBalanceEffect(tx, "ws", models.TransactionTypeTransfer, models.AccountTypeChecking, models.PaymentStatusPaid, 3000, "checking-1", "checking-2", now); err != nil {
		t.Fatalf("apply transfer: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	assertBalance(t, db, "checking-1", 8000) // 10000-1000+2000-3000
	assertBalance(t, db, "checking-2", 8000) // 5000+3000
	assertBalance(t, db, "card-1", 0)        // unchanged
}

func TestReverseBalanceEffectScenarios(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedAccounts(t, db)

	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	if err := ReverseBalanceEffect(tx, "ws", models.TransactionTypeExpense, models.AccountTypeChecking, 1000, "checking-1", "", now); err != nil {
		t.Fatalf("reverse expense: %v", err)
	}
	if err := ReverseBalanceEffect(tx, "ws", models.TransactionTypeIncome, models.AccountTypeChecking, 2000, "checking-1", "", now); err != nil {
		t.Fatalf("reverse income: %v", err)
	}
	if err := ReverseBalanceEffect(tx, "ws", models.TransactionTypeExpense, models.AccountTypeCreditCard, 700, "card-1", "", now); err != nil {
		t.Fatalf("reverse expense card: %v", err)
	}
	if err := ReverseBalanceEffect(tx, "ws", models.TransactionTypeTransfer, models.AccountTypeChecking, 3000, "checking-1", "checking-2", now); err != nil {
		t.Fatalf("reverse transfer: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	assertBalance(t, db, "checking-1", 12000) // 10000+1000-2000+3000
	assertBalance(t, db, "checking-2", 2000)  // 5000-3000
	assertBalance(t, db, "card-1", 0)         // unchanged
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

func seedAccounts(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	exec(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES ('ws', 'WS', '', ?, ?)`, now, now)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-1', 'ws', 'Conta 1', 'CHECKING', 10000, 10000, ?, ?)`, now, now)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-2', 'ws', 'Conta 2', 'CHECKING', 5000, 5000, ?, ?)`, now, now)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-1', 'ws', 'Cartão', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
}

func exec(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func assertBalance(t *testing.T, db *sql.DB, accountID string, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, accountID).Scan(&got); err != nil {
		t.Fatalf("query balance: %v", err)
	}
	if got != want {
		t.Fatalf("balance %s = %d, want %d", accountID, got, want)
	}
}
