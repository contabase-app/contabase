package services

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
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

func TestCalculateWorkspaceReserveBalanceRealBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		values     []int64
		want       int64
		outOfRange bool
	}{
		{"normal", []int64{100000, 25000}, 125000, false},
		{"positive-and-negative", []int64{100000, -25000}, 75000, false},
		{"max", []int64{math.MaxInt64}, math.MaxInt64, false},
		{"min", []int64{math.MinInt64}, math.MinInt64, false},
		{"two-halves", []int64{math.MaxInt64 / 2, math.MaxInt64 / 2}, math.MaxInt64 - 1, false},
		{"above-max", []int64{math.MaxInt64, 1}, 0, true},
		{"below-min", []int64{math.MinInt64, -1}, 0, true},
		{"cancel-above-max", []int64{math.MaxInt64, 1, -1}, math.MaxInt64, false},
		{"cancel-below-min", []int64{math.MinInt64, -1, 1}, math.MinInt64, false},
		{"cancel-reordered", []int64{-1, math.MaxInt64, 1}, math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			now := time.Now().Unix()
			seedReserveWorkspace(t, db, "ws-a", now)
			for i, value := range tc.values {
				insertReserveAccount(t, db, string(rune('a'+i)), "ws-a", models.AccountTypeChecking, value, now)
			}
			got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
			if tc.outOfRange {
				assertReserveOutOfRange(t, err, "real")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertReserveBalance(t, got, tc.want, 0, tc.want)
		})
	}
}

func TestCalculateWorkspaceReserveBalanceFiltersAndEmptyWorkspace(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now().Unix()
	seedReserveWorkspace(t, db, "ws-a", now)
	seedReserveWorkspace(t, db, "ws-b", now)
	seedReserveWorkspace(t, db, "ws-c", now)
	insertReserveAccount(t, db, "a-check", "ws-a", models.AccountTypeChecking, -200, now)
	insertReserveAccount(t, db, "a-investment", "ws-a", models.AccountTypeInvestment, 900, now)
	insertReserveAccount(t, db, "a-wallet", "ws-a", models.AccountTypeWallet, 100, now)
	insertReserveAccount(t, db, "a-card", "ws-a", models.AccountTypeCreditCard, math.MaxInt64, now)
	insertReserveAccount(t, db, "a-archived", "ws-a", models.AccountTypeSavings, math.MaxInt64, now)
	exec(t, db, `UPDATE accounts SET archived_at = ? WHERE id = 'a-archived'`, now)
	insertReserveAccount(t, db, "b-check", "ws-b", models.AccountTypeChecking, math.MaxInt64, now)
	got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	assertReserveBalance(t, got, 800, 0, 800)
	got, err = CalculateWorkspaceReserveBalance(db, "ws-c")
	if err != nil {
		t.Fatal(err)
	}
	assertReserveBalance(t, got, 0, 0, 0)
}

func TestCalculateWorkspaceReserveBalanceReservedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		values     []int64
		want       int64
		outOfRange bool
	}{
		{"normal", []int64{3000, 2000}, 5000, false},
		{"positive-and-negative", []int64{5000, -2000}, 3000, false},
		{"max", []int64{math.MaxInt64}, math.MaxInt64, false},
		{"above-max", []int64{math.MaxInt64, 1}, 0, true},
		{"below-min", []int64{math.MinInt64, -1}, 0, true},
		{"cancel-above-max", []int64{math.MaxInt64, 1, -1}, math.MaxInt64, false},
		{"cancel-reordered", []int64{-1, math.MaxInt64, 1}, math.MaxInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			now := time.Now().Unix()
			seedReserveWorkspace(t, db, "ws-a", now)
			seedReserveWorkspace(t, db, "ws-b", now)
			insertReserveBox(t, db, "a-box", "ws-a", "a-cat-1", now)
			insertReserveBox(t, db, "a-empty", "ws-a", "a-cat-2", now)
			insertReserveBox(t, db, "b-box", "ws-b", "b-cat-1", now)
			insertReserveLedger(t, db, "b-ledger", "b-box", math.MaxInt64, now)
			for i, value := range tc.values {
				insertReserveLedger(t, db, string(rune('a'+i)), "a-box", value, now)
			}
			got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
			if tc.outOfRange {
				assertReserveOutOfRange(t, err, "reservado")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertReserveBalance(t, got, 0, tc.want, -tc.want)
		})
	}
}

func TestCalculateWorkspaceReserveBalanceFreeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		real       int64
		reserved   int64
		want       int64
		outOfRange bool
	}{
		{"normal", 100000, 25000, 75000, false},
		{"negative", -100, -200, 100, false},
		{"above-max", math.MaxInt64, -1, 0, true},
		{"below-min", math.MinInt64, 1, 0, true},
		{"exact-max", math.MaxInt64 - 1, -1, math.MaxInt64, false},
		{"exact-min", math.MinInt64 + 1, 1, math.MinInt64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			now := time.Now().Unix()
			seedReserveWorkspace(t, db, "ws-a", now)
			insertReserveAccount(t, db, "a-check", "ws-a", models.AccountTypeChecking, tc.real, now)
			insertReserveBox(t, db, "a-box", "ws-a", "a-cat-1", now)
			insertReserveLedger(t, db, "a-ledger", "a-box", tc.reserved, now)
			got, err := CalculateWorkspaceReserveBalance(db, "ws-a")
			if tc.outOfRange {
				assertReserveOutOfRange(t, err, "disponível")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertReserveBalance(t, got, tc.real, tc.reserved, tc.want)
		})
	}
}

func TestCalculateWorkspaceReserveBalancePropagatesContextError(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := calculateWorkspaceReserveBalance(ctx, db, "ws-a")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestMonthlyPlanningPropagatesReserveAggregateError(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	now := time.Now().UTC()
	seedReserveWorkspace(t, db, "ws-a", now.Unix())
	insertReserveAccount(t, db, "a-max", "ws-a", models.AccountTypeChecking, math.MaxInt64, now.Unix())
	insertReserveAccount(t, db, "a-one", "ws-a", models.AccountTypeSavings, 1, now.Unix())
	_, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-a", now)
	if !errors.Is(err, ErrBalanceAggregateOutOfRange) {
		t.Fatalf("planning error = %v, want reserve aggregate out of range", err)
	}
}

func assertReserveOutOfRange(t *testing.T, err error, component string) {
	t.Helper()
	if !errors.Is(err, ErrBalanceAggregateOutOfRange) || !strings.Contains(err.Error(), component) {
		t.Fatalf("error = %v, want %s out of range", err, component)
	}
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
	ledgerType := "RECHARGE"
	if amount < 0 {
		ledgerType = "RELEASE"
	}
	exec(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES (?, ?, ?, ?, 'teste', ?, ?)
	`, ledgerID, boxID, amount, ledgerType, now, now)
}

func assertReserveBalance(t *testing.T, got WorkspaceReserveBalance, real, reserved, free int64) {
	t.Helper()
	if got.RealBalance != real || got.ReservedBalance != reserved || got.FreeBalance != free {
		t.Fatalf("balance = real:%d reserved:%d free:%d, want real:%d reserved:%d free:%d",
			got.RealBalance, got.ReservedBalance, got.FreeBalance, real, reserved, free)
	}
}
