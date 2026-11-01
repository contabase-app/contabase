//go:build contabase_legacy_fixtures

package database

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBRelExactCapacityBoundary(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_CAPACITY")
	const L = 67108864
	for _, target := range []int{L - 1, L, L + 1} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			db := admissionCandidateDB(t)
			mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
			mustExecOK(t, db, `UPDATE transactions SET description='',amount=1 WHERE workspace_id='ws-1' AND rowid=201`)
			var cr0 int
			if err := db.QueryRow(`SELECT length(` + admissionCanonicalRow("t") + `) FROM transactions t WHERE t.workspace_id='ws-1' AND t.rowid=201`).Scan(&cr0); err != nil {
				t.Fatal(err)
			}
			size := func(n, amount int) int {
				cr := cr0 + 2*n + len(strconv.Itoa(n)) - 1
				if amount == 10 {
					cr++
				}
				return len("CBMS|1|1|1|") + len(strconv.Itoa(cr)) + 1 + cr + len("1;")
			}
			n, amount := -1, 1
			for _, a := range []int{1, 10} {
				lo, hi := 0, L
				for lo < hi {
					mid := (lo + hi) / 2
					if size(mid, a) < target {
						lo = mid + 1
					} else {
						hi = mid
					}
				}
				if size(lo, a) == target {
					n, amount = lo, a
					break
				}
			}
			if n < 0 {
				t.Fatal("boundary calibration not exact")
			}
			mustExecOK(t, db, `UPDATE transactions SET description=?,amount=? WHERE workspace_id='ws-1' AND rowid=201`, strings.Repeat("a", n), amount)
			financialBefore := admissionFinancialSnapshot(t, db)
			runtime.GC()
			var mem0, mem1 runtime.MemStats
			runtime.ReadMemStats(&mem0)
			start := time.Now()
			_, err := db.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...)
			elapsed := time.Since(start)
			runtime.ReadMemStats(&mem1)
			t.Logf("PROCESS_RSS_PEAK target=%d bytes=%d", target, brelPeakRSS(t))
			if target <= L {
				if err != nil {
					t.Fatal(err)
				}
				if got := countQueryRows(t, db, `SELECT length(canonical_multiset) FROM investment_admission_null_transaction_witness WHERE workspace_id='ws-1' AND account_id='acc-inv'`); got != target {
					t.Fatalf("size=%d target=%d", got, target)
				}
				if countQueryRows(t, db, `SELECT count(*) FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1' AND account_id='acc-inv'`) != 1 {
					t.Fatal("boundary omitted source pin")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
					t.Fatalf("L+1 did not fail capacity CHECK: %v", err)
				}
				for _, table := range []string{"investment_adoption_admissions", "investment_admission_attempts", "investment_admission_pin_transactions", "investment_admission_pin_null_transactions", "investment_admission_pin_valuations", "investment_admission_pin_purchases", "investment_admission_pin_withdrawals", "investment_admission_pin_allocations", "investment_admission_pin_items", "investment_admission_null_scopes", "investment_admission_null_transaction_witness"} {
					if countQueryRows(t, db, "SELECT count(*) FROM "+table) != 0 {
						t.Fatal("capacity failure left partial evidence", table)
					}
				}
			}
			if fmt.Sprint(financialBefore) != fmt.Sprint(admissionFinancialSnapshot(t, db)) {
				t.Fatal("capacity capture changed finances")
			}
			t.Logf("EXACT_CAPACITY target=%d text_bytes=%d amount=%d elapsed_ns=%d heap_before=%d heap_after=%d heap_inuse_after=%d err=%v", target, n, amount, elapsed, mem0.HeapAlloc, mem1.HeapAlloc, mem1.HeapInuse, err)
			db.Close()
		})
	}
}
