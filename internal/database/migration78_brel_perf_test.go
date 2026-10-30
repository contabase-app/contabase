package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func brelPerfDisk(t *testing.T, n int, candidate bool) (*sql.DB, string) {
	t.Helper()
	var fixture *sql.DB
	if candidate {
		fixture = admissionCandidateDB(t)
	} else {
		fixture = referenceLegacy71(t)
		if err := runMigrations(fixture); err != nil {
			t.Fatal(err)
		}
		mustExecOK(t, fixture, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('acc-inv2','ws-2','Second','INVESTMENT')`)
	}
	admissionNULLBatch(t, fixture, n, "brel-perf")
	path := filepath.Join(t.TempDir(), "perf.db")
	mustExecOK(t, fixture, `VACUUM INTO ?`, path)
	fixture.Close()
	dsn := sqliteDSNWithTxLock(normalizeSQLiteDSN("file:"+path), "immediate")
	if candidate {
		db, c, err := newSQLiteGuardPool(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if err = publishSQLiteGuard(db, c); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db, path
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func brelPerfSamples(t *testing.T, db *sql.DB, q string, n int) []time.Duration {
	t.Helper()
	stmt, err := db.Prepare(q)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	v := make([]time.Duration, 0, n)
	for i := -2; i < n; i++ {
		start := time.Now()
		if _, err = stmt.Exec(); err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			v = append(v, time.Since(start))
		}
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v
}
func brelPerfLog(t *testing.T, n, rep int, class string, s []time.Duration) {
	t.Logf("MEASURE n=%d rep=%d class=%s samples=%d p50_ns=%d p95_ns=%d p99_ns=%d max_ns=%d", n, rep, class, len(s), percentile(s, .5), percentile(s, .95), percentile(s, .99), s[len(s)-1])
}

func TestBRelPerformance(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_PERF")
	t.Setenv("DATA_DIR", t.TempDir())
	classes := []struct{ name, q string }{
		{"P2", `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-checking','EXPENSE',1,1,'2026-03-06')`},
		{"P3", `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-06')`},
		{"P6", `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-2','user-2','acc-inv2','EXPENSE',1,1,'2026-03-06')`},
	}
	for _, n := range []int{0, 100, 1000, 20000} {
		for rep := 0; rep < 5; rep++ {
			base, _ := brelPerfDisk(t, n, false)
			cand, path := brelPerfDisk(t, n, true)
			if n == 0 {
				brelPerfLog(t, n, rep, "P1_base", brelPerfSamples(t, base, classes[2].q, 10000))
				brelPerfLog(t, n, rep, "P1_dormant", brelPerfSamples(t, cand, classes[2].q, 10000))
			}
			start := time.Now()
			tx, err := cand.Begin()
			if err != nil {
				t.Fatal(err)
			}
			acquired := time.Now()
			if _, err = tx.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			t.Logf("CAPTURE n=%d rep=%d begin_ns=%d hold_ns=%d total_ns=%d", n, rep, acquired.Sub(start), time.Since(acquired), time.Since(start))
			for _, c := range classes {
				brelPerfLog(t, n, rep, c.name+"_base", brelPerfSamples(t, base, c.q, 10000))
				brelPerfLog(t, n, rep, c.name+"_candidate", brelPerfSamples(t, cand, c.q, 10000))
			}
			if n == 20000 && rep == 0 {
				for _, m := range []int{10, 100, 1000} {
					values := make([]string, m)
					for i := range values {
						values[i] = "('ws-2','user-2','acc-inv2','EXPENSE',1,1,'2026-03-06')"
					}
					for label, db := range map[string]*sql.DB{"base": base, "candidate": cand} {
						tx, err := db.Begin()
						if err != nil {
							t.Fatal(err)
						}
						start := time.Now()
						if _, err = tx.Exec(`INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES ` + strings.Join(values, ",")); err != nil {
							t.Fatal(err)
						}
						if err = tx.Commit(); err != nil {
							t.Fatal(err)
						}
						t.Logf("MULTI class=%s rows=%d hold_ns=%d", label, m, time.Since(start))
					}
				}
			}
			if n > 0 {
				gc, oc, blob := witnessRow(t, cand)
				var pages int64
				_ = cand.QueryRow(`SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&pages)
				var wal int64
				if info, err := os.Stat(path + "-wal"); err == nil {
					wal = info.Size()
				}
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				t.Logf("RESOURCE n=%d rep=%d groups=%d occurrences=%d envelope=%d db_pages_bytes=%d wal_observed=%d heap_alloc=%d heap_inuse=%d", n, rep, gc, oc, len(blob), pages, wal, mem.HeapAlloc, mem.HeapInuse)
				rows, err := cand.Query(`SELECT name,sum(pgsize) FROM dbstat GROUP BY name ORDER BY name`)
				if err != nil {
					t.Logf("DBSTAT unavailable: %v", err)
				} else {
					for rows.Next() {
						var name string
						var size int64
						if err = rows.Scan(&name, &size); err != nil {
							t.Fatal(err)
						}
						if name == "transactions" || strings.Contains(name, "admission") || strings.Contains(name, "autoindex_transactions") {
							t.Logf("COMPONENT n=%d rep=%d name=%s bytes=%d", n, rep, name, size)
						}
					}
					if err = rows.Err(); err != nil {
						t.Fatal(err)
					}
					rows.Close()
				}
			}
			base.Close()
			cand.Close()
		}
	}
	// P4 medium: five independent 100k NULL captures, measured separately
	// from creation/backfill and without weakening the original 2s threshold.
	for rep := 0; rep < 5; rep++ {
		cand, _ := brelPerfDisk(t, 100000, true)
		tx, err := cand.Begin()
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err = tx.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		t.Logf("MEDIUM_CAPTURE rep=%d hold_ns=%d limit_ns=2000000000", rep, time.Since(start))
		cand.Close()
	}
}

func TestBRelWALComparison(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_PERF")
	for _, candidate := range []bool{false, true} {
		db, path := brelPerfDisk(t, 20000, candidate)
		if candidate {
			admitDefault(t, db)
		}
		reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		reader.SetMaxOpenConns(1)
		rtx, err := reader.Begin()
		if err != nil {
			t.Fatal(err)
		}
		var before int
		if err = rtx.QueryRow(`SELECT count(*) FROM transactions`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		secondDSN := sqliteDSNWithTxLock(normalizeSQLiteDSN("file:"+path), "immediate")
		var second *sql.DB
		if candidate {
			var c *sqliteGuardConnector
			second, c, err = newSQLiteGuardPool(secondDSN)
			if err == nil {
				err = publishSQLiteGuard(second, c)
			}
		} else {
			second, err = sql.Open("sqlite", secondDSN)
		}
		if err != nil {
			t.Fatal(err)
		}
		second.SetMaxOpenConns(1)
		for _, offeredDelay := range []time.Duration{time.Millisecond, 0} {
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for id, pool := range []*sql.DB{db, second} {
				wg.Add(1)
				go func(id int, pool *sql.DB) {
					defer wg.Done()
					start := time.Now()
					var waits, holds []time.Duration
					busy := 0
					for i := 0; i < 500; i++ {
						begin := time.Now()
						tx, err := pool.Begin()
						if err != nil {
							busy++
							continue
						}
						waits = append(waits, time.Since(begin))
						acquired := time.Now()
						_, err = tx.Exec(`INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-2','user-2','acc-inv2','EXPENSE',1,1,'2026-03-06')`)
						if err != nil {
							tx.Rollback()
							results <- err
							return
						}
						if err = tx.Commit(); err != nil {
							results <- err
							return
						}
						holds = append(holds, time.Since(acquired))
						if offeredDelay > 0 {
							time.Sleep(offeredDelay)
						}
					}
					sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
					sort.Slice(holds, func(i, j int) bool { return holds[i] < holds[j] })
					t.Logf("WAL candidate=%t writer=%d delay_ns=%d successes=%d busy=%d elapsed_ns=%d wait_p95=%d hold_p95=%d", candidate, id, offeredDelay, len(holds), busy, time.Since(start), percentile(waits, .95), percentile(holds, .95))
					results <- nil
				}(id, pool)
			}
			wg.Wait()
			for i := 0; i < 2; i++ {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
		}
		var after int
		if err = rtx.QueryRow(`SELECT count(*) FROM transactions`).Scan(&after); err != nil || before != after {
			t.Fatal("long WAL reader changed snapshot", err)
		}
		rtx.Rollback()
		reader.Close()
		second.Close()
		if err := checkpointWAL(db); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}

func TestBRelPerfHost(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_PERF")
	db := admissionCandidateDB(t)
	for _, q := range []string{`SELECT sqlite_version(),sqlite_source_id()`, `PRAGMA compile_options`} {
		r, e := db.Query(q)
		if e != nil {
			t.Fatal(e)
		}
		cols, _ := r.Columns()
		for r.Next() {
			v := make([]any, len(cols))
			dest := make([]any, len(v))
			for i := range v {
				dest[i] = &v[i]
			}
			if e = r.Scan(dest...); e != nil {
				t.Fatal(e)
			}
			t.Log(fmt.Sprint(v...))
		}
		r.Close()
	}
	t.Logf("GO=%s OS=%s ARCH=%s CPUs=%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}

// A narrow final-code comparison after format/control hardening. The full
// matrix above is retained as diagnostic evidence, never relabelled as a
// measurement of code compiled after that run began.
func TestBRelFinalWriteComparison(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_FINAL_PERF")
	const q = `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-06')`
	for _, n := range []int{0, 100, 1000, 20000} {
		for rep := 0; rep < 5; rep++ {
			base, _ := brelPerfDisk(t, n, false)
			cand, _ := brelPerfDisk(t, n, true)
			if n > 0 {
				admitDefault(t, cand)
			}
			brelPerfLog(t, n, rep, "FINAL_base", brelPerfSamples(t, base, q, 10000))
			brelPerfLog(t, n, rep, "FINAL_candidate", brelPerfSamples(t, cand, q, 10000))
			base.Close()
			cand.Close()
		}
	}
}

func TestBRelSpaceComponents(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_CAPACITY")
	for _, candidate := range []bool{false, true} {
		db, path := brelPerfDisk(t, 20000, candidate)
		measure := func(stage string) {
			var pages int64
			if err := db.QueryRow(`SELECT page_count*page_size FROM pragma_page_count,pragma_page_size`).Scan(&pages); err != nil {
				t.Fatal(err)
			}
			var wal int64
			if f, err := os.Stat(path + "-wal"); err == nil {
				wal = f.Size()
			}
			t.Logf("SPACE candidate=%t stage=%s db_pages_bytes=%d wal_observed=%d peak_rss=%d", candidate, stage, pages, wal, brelPeakRSS(t))
			rows, err := db.Query(`SELECT name,sum(pgsize) FROM dbstat GROUP BY name ORDER BY name`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var name string
				var bytes int64
				if err = rows.Scan(&name, &bytes); err != nil {
					t.Fatal(err)
				}
				if name == "transactions" || strings.Contains(name, "admission") || strings.Contains(name, "autoindex_transactions") {
					t.Logf("SPACE_COMPONENT candidate=%t stage=%s name=%s bytes=%d", candidate, stage, name, bytes)
				}
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		if err := checkpointWAL(db); err != nil {
			t.Fatal(err)
		}
		measure("dormant")
		if candidate {
			admitDefault(t, db)
			measure("admitted")
		}
		var page, cache, temp, syncMode, timeout int
		if err := db.QueryRow(`SELECT page_size,cache_size,temp_store,synchronous,timeout FROM pragma_page_size,pragma_cache_size,pragma_temp_store,pragma_synchronous,pragma_busy_timeout`).Scan(&page, &cache, &temp, &syncMode, &timeout); err != nil {
			t.Fatal(err)
		}
		t.Logf("PRAGMAS candidate=%t page_size=%d cache_size=%d temp_store=%d synchronous=%d busy_timeout=%d", candidate, page, cache, temp, syncMode, timeout)
		db.Close()
	}
}
