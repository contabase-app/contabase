package database

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

func TestTransactionRefundExactAccumulator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gross   int64
		refunds []int64
		want    int64
		invalid bool
	}{
		{"none", 100, nil, 100, false},
		{"one", 100, []int64{20}, 80, false},
		{"multiple", 100, []int64{20, 30}, 50, false},
		{"full", 100, []int64{60, 40}, 0, false},
		{"maximum-none", math.MaxInt64, nil, math.MaxInt64, false},
		{"maximum-full", math.MaxInt64, []int64{math.MaxInt64}, 0, false},
		{"maximum-multiple", math.MaxInt64, []int64{math.MaxInt64 - 1, 1}, 0, false},
		{"maximum-reordered", math.MaxInt64, []int64{1, math.MaxInt64 - 1}, 0, false},
		{"over-gross", 100, []int64{60, 41}, 0, true},
		{"over-int64", math.MaxInt64, []int64{math.MaxInt64, 1}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a exactTransactionRefunds
			for _, amount := range tc.refunds {
				if err := a.Step(nil, []driver.Value{amount}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := transactionRefundNet(tc.gross, &a.total)
			if tc.invalid {
				if got != 0 || !errors.Is(err, ErrTransactionRefundInvariant) {
					t.Fatalf("invalid result %d/%v", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("net %d/%v, want %d", got, err, tc.want)
			}
		})
	}
	for _, value := range []driver.Value{nil, int64(0), int64(-1), 1.5, "1", []byte("1")} {
		var a exactTransactionRefunds
		if err := a.Step(nil, []driver.Value{value}); !errors.Is(err, ErrTransactionRefundInvariant) {
			t.Fatalf("accepted storage %T", value)
		}
		if value, err := a.WindowValue(nil); value != nil || !errors.Is(err, ErrTransactionRefundInvariant) {
			t.Fatal("invalid aggregate exposed a partial value")
		}
	}
	negative := big.NewInt(-1)
	if _, err := transactionRefundNet(100, negative); !errors.Is(err, ErrTransactionRefundInvariant) {
		t.Fatal("negative aggregate accepted")
	}
}

func TestTransactionRefundExactSQLStorageAndReversal(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE refund_inputs(amount_cents, reversed_at)`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		gross    driver.Value
		active   []driver.Value
		reversed driver.Value
		want     int64
		invalid  bool
	}{
		{"none", int64(100), nil, nil, 100, false},
		{"active-and-reversed", int64(100), []driver.Value{int64(20), int64(30)}, int64(math.MaxInt64), 50, false},
		{"maximum-reversed", int64(math.MaxInt64), nil, int64(math.MaxInt64), math.MaxInt64, false},
		{"maximum-split", int64(math.MaxInt64), []driver.Value{int64(math.MaxInt64 - 1), int64(1)}, nil, 0, false},
		{"over-gross", int64(math.MaxInt64), []driver.Value{int64(math.MaxInt64), int64(1)}, nil, 0, true},
		{"real-refund", int64(100), []driver.Value{1.5}, nil, 0, true},
		{"text-refund", int64(100), []driver.Value{"20"}, nil, 0, true},
		{"null-refund", int64(100), []driver.Value{nil}, nil, 0, true},
		{"zero-refund", int64(100), []driver.Value{int64(0)}, nil, 0, true},
		{"negative-refund", int64(100), []driver.Value{int64(-1)}, nil, 0, true},
		{"real-parent", 100.5, []driver.Value{int64(1)}, nil, 0, true},
		{"text-parent", "100", nil, nil, 0, true},
		{"null-parent", nil, nil, nil, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM refund_inputs`); err != nil {
				t.Fatal(err)
			}
			for _, value := range tc.active {
				if _, err := db.Exec(`INSERT INTO refund_inputs VALUES(?,NULL)`, value); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reversed != nil {
				if _, err := db.Exec(`INSERT INTO refund_inputs VALUES(?,1)`, tc.reversed); err != nil {
					t.Fatal(err)
				}
			}
			var got int64
			err := db.QueryRow(`SELECT contabase_refund_net(?,(SELECT contabase_refund_sum(amount_cents) FROM refund_inputs WHERE reversed_at IS NULL))`, tc.gross).Scan(&got)
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), ErrTransactionRefundInvariant.Error()) || strings.Contains(err.Error(), "integer overflow") || strings.Contains(err.Error(), "9223372036854775807") {
					t.Fatalf("wrong invariant failure: %v", err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("net %d/%v, want %d", got, err, tc.want)
			}
		})
	}
}
