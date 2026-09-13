package services

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/contabase-app/contabase/internal/database"
)

func seedAdjustment(t *testing.T, db *sql.DB) AccountBalanceAdjustment {
	t.Helper()
	seedAccounts(t, db)
	exec(t, db, `INSERT INTO users (id,name,email,password_hash) VALUES ('adjust-user','Reconciler','reconcile@example.test','hash')`)
	return AccountBalanceAdjustment{ID: "adjust-1", WorkspaceID: "ws", AccountID: "checking-1", UserID: "adjust-user", Reason: "Conferência", ExpectedVersion: 0, TargetBalance: 15000, CreatedAt: 100}
}

func assertAdjustmentState(t *testing.T, db *sql.DB, balance, version, count int64) {
	t.Helper()
	var gotBalance, gotVersion, gotCount, initial, transactions int64
	if err := db.QueryRow(`SELECT current_balance,balance_version,initial_balance FROM accounts WHERE id='checking-1'`).Scan(&gotBalance, &gotVersion, &initial); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_balance_adjustments`).Scan(&gotCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if gotBalance != balance || gotVersion != version || gotCount != count || initial != 10000 || transactions != 0 {
		t.Fatalf("state balance=%d version=%d history=%d initial=%d transactions=%d; want %d/%d/%d/10000/0", gotBalance, gotVersion, gotCount, initial, transactions, balance, version, count)
	}
}

func TestAccountAdjustmentAtomicHistoryAndIdempotency(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	input := seedAdjustment(t, db)
	if err := AdjustAccountBalance(context.Background(), db, input); err != nil {
		t.Fatal(err)
	}
	assertAdjustmentState(t, db, 15000, 1, 1)
	var before, after, delta int64
	if err := db.QueryRow(`SELECT before_balance,after_balance,delta_cents FROM account_balance_adjustments`).Scan(&before, &after, &delta); err != nil {
		t.Fatal(err)
	}
	if before != 10000 || after != 15000 || delta != 5000 {
		t.Fatalf("incorrect history: %d/%d/%d", before, after, delta)
	}
	// A retry after a later movement must not reapply the old target.
	exec(t, db, `UPDATE accounts SET current_balance=current_balance+123 WHERE id='checking-1'`)
	if err := AdjustAccountBalance(context.Background(), db, input); err != nil {
		t.Fatal(err)
	}
	assertAdjustmentState(t, db, 15123, 2, 1)
	input.TargetBalance++
	if err := AdjustAccountBalance(context.Background(), db, input); !errors.Is(err, ErrBalanceAdjustmentConflict) {
		t.Fatalf("changed retry: %v", err)
	}
	assertAdjustmentState(t, db, 15123, 2, 1)
	input.ID = "adjust-2"
	input.ExpectedVersion = 2
	input.TargetBalance = -1
	if err := AdjustAccountBalance(context.Background(), db, input); err != nil {
		t.Fatal(err)
	}
	assertAdjustmentState(t, db, -1, 3, 2)
}

func TestAccountAdjustmentRejectsStaleBalanceEvenAfterReturnToSameValue(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	input := seedAdjustment(t, db)
	exec(t, db, `UPDATE accounts SET current_balance=current_balance+100 WHERE id='checking-1'`)
	exec(t, db, `UPDATE accounts SET current_balance=current_balance-100 WHERE id='checking-1'`)
	if err := AdjustAccountBalance(context.Background(), db, input); !errors.Is(err, ErrBalanceAdjustmentConflict) {
		t.Fatalf("stale result: %v", err)
	}
	assertAdjustmentState(t, db, 10000, 2, 0)
}

func TestAccountAdjustmentRollsBackOnEitherWriteFailure(t *testing.T) {
	for _, table := range []string{"account_balance_adjustments", "accounts"} {
		t.Run(table, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			input := seedAdjustment(t, db)
			stmt := `CREATE TRIGGER injected_failure BEFORE INSERT ON account_balance_adjustments BEGIN SELECT RAISE(ABORT,'test failure'); END`
			if table == "accounts" {
				stmt = `CREATE TRIGGER injected_failure BEFORE UPDATE OF current_balance ON accounts BEGIN SELECT RAISE(ABORT,'test failure'); END`
			}
			exec(t, db, stmt)
			if err := AdjustAccountBalance(context.Background(), db, input); err == nil {
				t.Fatal("expected injected error")
			}
			assertAdjustmentState(t, db, 10000, 0, 0)
		})
	}
}

func TestAccountAdjustmentRejectsUnauthorizedAndInvalidStates(t *testing.T) {
	for _, name := range []string{"foreign", "card", "archived", "unchanged", "delta overflow", "empty reason", "negative version", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			input := seedAdjustment(t, db)
			ctx := context.Background()
			var expectedError error
			switch name {
			case "foreign":
				input.WorkspaceID = "other"
				expectedError = sql.ErrNoRows
			case "card":
				input.AccountID = "card-1"
				expectedError = sql.ErrNoRows
			case "archived":
				exec(t, db, `UPDATE accounts SET archived_at=1 WHERE id='checking-1'`)
				expectedError = sql.ErrNoRows
			case "unchanged":
				input.TargetBalance = 10000
				expectedError = ErrBalanceAdjustmentUnchanged
			case "delta overflow":
				input.TargetBalance = math.MinInt64
				expectedError = ErrBalanceOutOfRange
			case "empty reason":
				input.Reason = " "
			case "negative version":
				input.ExpectedVersion = -1
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expectedError = context.Canceled
			}
			err := AdjustAccountBalance(ctx, db, input)
			if err == nil || (expectedError != nil && !errors.Is(err, expectedError)) {
				t.Fatalf("unexpected rejection: %v", err)
			}
			assertAdjustmentState(t, db, 10000, 0, 0)
		})
	}
}

func TestAccountAdjustmentConcurrentSubmissionsWAL(t *testing.T) {
	for _, sameRequest := range []bool{false, true} {
		name := "competing"
		if sameRequest {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "adjustments.db")
			runtimeDBs, err := database.OpenRuntimeDBs(path)
			if err != nil {
				t.Fatal(err)
			}
			defer runtimeDBs.Close()
			db, other := runtimeDBs.DB, runtimeDBs.Immediate
			input := seedAdjustment(t, db)
			second := input
			if !sameRequest {
				second.ID = "adjust-2"
				second.TargetBalance = 17000
			}
			start := make(chan struct{})
			done := make(chan error, 2)
			go func() { <-start; done <- AdjustAccountBalance(context.Background(), db, input) }()
			go func() { <-start; done <- AdjustAccountBalance(context.Background(), other, second) }()
			close(start)
			succeeded, conflicted := 0, 0
			for i := 0; i < 2; i++ {
				err := <-done
				if err == nil {
					succeeded++
				} else if errors.Is(err, ErrBalanceAdjustmentConflict) {
					conflicted++
				} else {
					t.Fatalf("unexpected concurrency error: %v", err)
				}
			}
			if sameRequest {
				if succeeded != 2 {
					t.Fatalf("same request successes=%d", succeeded)
				}
			} else if succeeded != 1 || conflicted != 1 {
				t.Fatalf("competing results: %d/%d", succeeded, conflicted)
			}
			var balance int64
			if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-1'`).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if balance != 15000 && balance != 17000 {
				t.Fatalf("unexpected balance=%d", balance)
			}
			assertAdjustmentState(t, db, balance, 1, 1)
		})
	}
}
