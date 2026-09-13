package services

import (
	"database/sql"
	"errors"
	"math"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

func TestBalanceInt64Boundaries(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, tc := range []struct {
			name                  string
			subtract              bool
			balance, amount, want int64
			reject                bool
		}{
			{"add-exact-max", false, math.MaxInt64 - 1, 1, math.MaxInt64, false},
			{"add-overflow", false, math.MaxInt64, 1, math.MaxInt64, true},
			{"add-large-operand", false, 1, math.MaxInt64, 1, true},
			{"subtract-exact-min", true, math.MinInt64 + 1, 1, math.MinInt64, false},
			{"subtract-underflow", true, math.MinInt64, 1, math.MinInt64, true},
			{"subtract-large-operand", true, -2, math.MaxInt64, -2, true},
			{"add-min-operand-valid", false, 0, math.MinInt64, math.MinInt64, false},
			{"add-min-operand-invalid", false, -1, math.MinInt64, -1, true},
			{"subtract-min-operand-valid", true, -1, math.MinInt64, math.MaxInt64, false},
			{"subtract-min-operand-invalid", true, 0, math.MinInt64, 0, true},
			{"add-negative-valid", false, math.MinInt64 + 1, -1, math.MinInt64, false},
			{"add-negative-invalid", false, math.MinInt64, -1, math.MinInt64, true},
			{"subtract-negative-valid", true, math.MaxInt64 - 1, -1, math.MaxInt64, false},
			{"subtract-negative-invalid", true, math.MaxInt64, -1, math.MaxInt64, true},
			{"zero-at-max", false, math.MaxInt64, 0, math.MaxInt64, false},
			{"zero-at-min", true, math.MinInt64, 0, math.MinInt64, false},
		} {
			prefix := "apply/"
			if reverse {
				prefix = "reverse/"
			}
			t.Run(prefix+tc.name, func(t *testing.T) {
				db := openDB(t)
				defer db.Close()
				seedAccounts(t, db)
				exec(t, db, `UPDATE accounts SET current_balance = ?, updated_at = 17 WHERE id = 'checking-1'`, tc.balance)
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				typ := models.TransactionTypeIncome
				if tc.subtract != reverse {
					typ = models.TransactionTypeExpense
				}
				if reverse {
					err = ReverseBalanceEffect(tx, "ws", typ, models.AccountTypeChecking, tc.amount, "checking-1", "", 23)
				} else {
					err = ApplyBalanceEffect(tx, "ws", typ, models.AccountTypeChecking, models.PaymentStatusPaid, tc.amount, "checking-1", "", 23)
				}
				if (err != nil) != tc.reject {
					t.Fatalf("reject=%t, error=%v", tc.reject, err)
				}
				if tc.reject && !errors.Is(err, ErrBalanceOutOfRange) {
					t.Fatalf("expected explicit overflow error: %v", err)
				}
				// Even committing after a rejected single-account write must leave
				// its value and timestamp untouched; no transient REAL is allowed.
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				wantUpdated := int64(23)
				if tc.reject {
					wantUpdated = 17
				}
				assertIntegerBalance(t, db, "checking-1", tc.want, wantUpdated)
			})
		}
	}
}

func TestBalanceTransferOverflowRollsBackBothSides(t *testing.T) {
	for _, tc := range []struct {
		name                string
		reverse             bool
		source, destination int64
	}{
		{"apply-source", false, math.MinInt64, 0},
		{"apply-destination", false, 0, math.MaxInt64},
		{"reverse-source", true, math.MaxInt64, 0},
		{"reverse-destination", true, 0, math.MinInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			seedAccounts(t, db)
			exec(t, db, `UPDATE accounts SET current_balance = ?, updated_at = 17 WHERE id = 'checking-1'`, tc.source)
			exec(t, db, `UPDATE accounts SET current_balance = ?, updated_at = 17 WHERE id = 'checking-2'`, tc.destination)
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if tc.reverse {
				err = ReverseBalanceEffect(tx, "ws", models.TransactionTypeTransfer, models.AccountTypeChecking, 1, "checking-1", "checking-2", 23)
			} else {
				err = ApplyBalanceEffect(tx, "ws", models.TransactionTypeTransfer, models.AccountTypeChecking, models.PaymentStatusPaid, 1, "checking-1", "checking-2", 23)
			}
			if !errors.Is(err, ErrBalanceOutOfRange) {
				t.Fatalf("expected overflow error: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			assertIntegerBalance(t, db, "checking-1", tc.source, 17)
			assertIntegerBalance(t, db, "checking-2", tc.destination, 17)
		})
	}
}

func assertIntegerBalance(t *testing.T, db *sql.DB, account string, want, wantUpdated int64) {
	t.Helper()
	var value, updated int64
	var storage string
	if err := db.QueryRow(`SELECT current_balance, typeof(current_balance), updated_at FROM accounts WHERE id = ?`, account).Scan(&value, &storage, &updated); err != nil {
		t.Fatal(err)
	}
	if value != want || storage != "integer" || updated != wantUpdated {
		t.Fatalf("%s = %d/%s/%d; want %d/integer/%d", account, value, storage, updated, want, wantUpdated)
	}
}

func TestBalanceGuardRejectsForeignMissingAndNonInteger(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedAccounts(t, db)
	exec(t, db, `INSERT INTO workspaces (id, name) VALUES ('other', 'Other')`)
	for _, tc := range []struct{ name, workspace, account string }{
		{"foreign", "other", "checking-1"},
		{"missing", "ws", "missing-account"},
		{"noninteger", "ws", "checking-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "noninteger" {
				exec(t, db, `UPDATE accounts SET current_balance = 1.5, updated_at = 17 WHERE id = 'checking-2'`)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = ApplyBalanceEffect(tx, tc.workspace, models.TransactionTypeIncome, models.AccountTypeChecking, models.PaymentStatusPaid, 1, tc.account, "", 23)
			if err == nil || errors.Is(err, ErrBalanceOutOfRange) {
				t.Fatalf("expected scope/storage error, got %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			assertBalance(t, db, "checking-1", 10000)
			if tc.name == "noninteger" {
				var value float64
				var storage string
				var updated int64
				if err := db.QueryRow(`SELECT current_balance, typeof(current_balance), updated_at FROM accounts WHERE id = 'checking-2'`).Scan(&value, &storage, &updated); err != nil || value != 1.5 || storage != "real" || updated != 17 {
					t.Fatalf("invalid stored balance changed: %v %s %d %v", value, storage, updated, err)
				}
			}
		})
	}
}
