package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"os"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBRelOptimizationMemoryJournalAtomicity(t *testing.T) {
	db, _ := brelPerfDisk(t, 0, true)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.Raw(func(raw any) error {
		g := raw.(*sqliteGuardConn)
		r, e := g.raw.(driver.QueryerContext).QueryContext(t.Context(), `PRAGMA temp_store`, nil)
		if e != nil {
			return e
		}
		v := make([]driver.Value, 1)
		e = r.Next(v)
		ce := r.Close()
		if e != nil || ce != nil || v[0] != int64(2) {
			return fmt.Errorf("temp_store=%v read=%v close=%v", v, e, ce)
		}
		for _, q := range []string{
			`CREATE TABLE rollback_probe(id INTEGER PRIMARY KEY,payload BLOB)`,
			`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<128) INSERT INTO rollback_probe SELECT x,zeroblob(8192) FROM n`,
			`CREATE TRIGGER rollback_probe_fail BEFORE UPDATE ON rollback_probe WHEN NEW.id=128 BEGIN SELECT RAISE(ABORT,'journal injected failure');END`,
		} {
			if _, e = g.rawExec(t.Context(), q, nil); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO rollback_probe VALUES(129,x'01')`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE rollback_probe SET payload=randomblob(8192) WHERE id<=128`); err == nil || !strings.Contains(err.Error(), "journal injected failure") {
		t.Fatal("expected statement ABORT", err)
	}
	var intact, prior int
	if err = tx.QueryRow(`SELECT count(*) FROM rollback_probe WHERE id<=128 AND payload=zeroblob(8192)`).Scan(&intact); err != nil || intact != 128 {
		t.Fatal("statement journal lost prior rows", intact, err)
	}
	if err = tx.QueryRow(`SELECT count(*) FROM rollback_probe WHERE id=129`).Scan(&prior); err != nil || prior != 1 {
		t.Fatal("ABORT lost earlier statement", prior, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRowContext(t.Context(), `SELECT count(*) FROM rollback_probe WHERE id=129`).Scan(&prior); err != nil || prior != 0 {
		t.Fatal("caller rollback failed", prior, err)
	}
}

// Diagnostic decomposition on disposable fixtures only. RAISE checks become
// row-returning assertions outside the trigger; all nested table guards remain.
func brelCaptureParts(q string) []string {
	q = q[strings.Index(q, "\nBEGIN")+6 : strings.LastIndex(q, "END")]
	q = strings.NewReplacer("NEW.workspace_id", "'ws-1'", "NEW.account_id", "'acc-inv'",
		"RAISE(ABORT,'investment admission integrity')", "1").Replace(q)
	var parts []string
	start, quoted := 0, false
	for i := 0; i < len(q); i++ {
		if q[i] == '\'' {
			if quoted && i+1 < len(q) && q[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
		}
		if q[i] == ';' && !quoted {
			parts = append(parts, strings.TrimSpace(q[start:i]))
			start = i + 1
		}
	}
	return parts
}

func TestBRelOptimizationCaptureProfile(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_OPT_PROFILE")
	for _, n := range []int{1000, 5000} {
		db, _ := brelPerfDisk(t, n, true)
		// A full intact capture establishes the actual transaction measurement.
		start := time.Now()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...); err != nil {
			t.Fatal(err)
		}
		t.Logf("CAPTURE n=%d hold_ns=%d (rollback excluded)", n, time.Since(start))
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var ddl string
		if err = db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='trg_admission_capture'`).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		// Only this synthetic connection permits diagnostic DDL.
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = conn.Raw(func(raw any) error {
			_, e := raw.(*sqliteGuardConn).rawExec(t.Context(), `DROP TRIGGER trg_admission_capture`, nil)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		tx, err = db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...); err != nil {
			t.Fatal(err)
		}
		for i, q := range brelCaptureParts(ddl) {
			start = time.Now()
			if strings.HasPrefix(q, "SELECT 1 WHERE") {
				var violation int
				if e := tx.QueryRow(q).Scan(&violation); e != sql.ErrNoRows {
					t.Fatalf("part %d: violation %d: %v", i, violation, e)
				}
			} else if _, err = tx.Exec(q); err != nil {
				t.Fatalf("part %d: %v", i, err)
			}
			t.Logf("PART n=%d i=%d elapsed_ns=%d sql=%.85s", n, i, time.Since(start), strings.ReplaceAll(q, "\n", " "))
		}
		queries := []struct{ name, q string }{
			{"selection", `SELECT count(*) FROM transactions t JOIN investment_adoption_admissions a ON t.workspace_id=a.workspace_id WHERE a.workspace_id='ws-1' AND a.account_id='acc-inv' AND t.id IS NULL AND ` + admissionFinancial("t", "a")},
			{"encoding", `SELECT sum(length(` + admissionCanonicalRow("t") + `)) FROM transactions t WHERE t.workspace_id='ws-1' AND t.account_id='acc-inv' AND t.id IS NULL`},
			{"state", admissionScopeState("'ws-1'", "'acc-inv'") + ` SELECT length(` + admissionEnvelope("st") + `) FROM st`},
		}
		for _, query := range queries {
			start = time.Now()
			var v int64
			if err = tx.QueryRow(query.q).Scan(&v); err != nil {
				t.Fatal(err)
			}
			t.Logf("COMPONENT n=%d name=%s elapsed_ns=%d value=%d", n, query.name, time.Since(start), v)
		}
		if err = tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}

func TestBRelOptimizationWriteProfile(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_OPT_PROFILE")
	const q = `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-06')`
	for _, candidate := range []bool{false, true} {
		db, _ := brelPerfDisk(t, 0, candidate)
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var g *sqliteGuardConn
		if candidate {
			if err = conn.Raw(func(raw any) error { g = raw.(*sqliteGuardConn); return nil }); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			for i := 0; i < 1000; i++ {
				if err = g.check(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("GUARD check_mean_ns=%d", time.Since(start).Nanoseconds()/1000)
		}
		conn.Close()
		for _, guarded := range []bool{false, true} {
			if !candidate && guarded {
				// Reopen the identical schema-77 database with exactly the D5 wrapper.
				var path string
				if err = db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
					t.Fatal(err)
				}
				db.Close()
				var c *sqliteGuardConnector
				db, c, err = newSQLiteGuardPool(sqliteDSNWithTxLock(normalizeSQLiteDSN("file:"+path), "immediate"))
				if err != nil {
					t.Fatal(err)
				}
				c.enabled.Store(true)
			} else if candidate {
				g.owner.enabled.Store(guarded)
			}
			brelPerfLog(t, 0, 0, fmt.Sprintf("DIAG_candidate_%t_D5_%t", candidate, guarded), brelPerfSamples(t, db, q, 2000))
		}
		if candidate {
			conn, err = db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = conn.Raw(func(raw any) error {
				c := raw.(*sqliteGuardConn)
				for _, p := range []string{"trg_admission_occurrence_insert", "trg_admission_transaction_insert"} {
					if _, e := c.rawExec(t.Context(), "DROP TRIGGER "+p, nil); e != nil {
						return e
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			conn.Close()
			brelPerfLog(t, 0, 0, "DIAG_no_two_new_insert_triggers_D5_on", brelPerfSamples(t, db, q, 2000))
			conn, err = db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = conn.Raw(func(raw any) error {
				_, e := raw.(*sqliteGuardConn).rawExec(t.Context(), `PRAGMA temp_store=MEMORY`, nil)
				return e
			}); err != nil {
				t.Fatal(err)
			}
			conn.Close()
			brelPerfLog(t, 0, 0, "DIAG_memory_subjournal_D5_on", brelPerfSamples(t, db, q, 2000))
		}
		db.Close()
	}
	// Isolate K emission and UNIQUE(W,K) without conflating either with D5.
	db := openInvestmentsTestDB(t)
	mustExecOK(t, db, `CREATE TABLE key_probe(w TEXT,k TEXT DEFAULT(lower(hex(randomblob(16)))))`)
	for _, indexed := range []bool{false, true} {
		if indexed {
			mustExecOK(t, db, `CREATE UNIQUE INDEX key_probe_unique ON key_probe(w,k)`)
		}
		brelPerfLog(t, 0, 0, fmt.Sprintf("DIAG_key_index_%t", indexed), brelPerfSamples(t, db, `INSERT INTO key_probe(w) VALUES('w')`, 2000))
	}
}

func TestBRelOptimizationTransactionCosts(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_OPT_PROFILE")
	const q = `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-06')`
	db, path := brelPerfDisk(t, 0, true)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmt, err := conn.PrepareContext(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	samples := map[string][]time.Duration{}
	for i := 0; i < 1000; i++ {
		start := time.Now()
		tx, e := conn.BeginTx(t.Context(), nil)
		if e != nil {
			t.Fatal(e)
		}
		samples["begin"] = append(samples["begin"], time.Since(start))
		bound := tx.Stmt(stmt)
		start = time.Now()
		_, e = bound.Exec()
		if e != nil {
			t.Fatal(e)
		}
		samples["execute"] = append(samples["execute"], time.Since(start))
		bound.Close()
		start = time.Now()
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		samples["commit"] = append(samples["commit"], time.Since(start))
	}
	for i := 0; i < 100; i++ {
		start := time.Now()
		s, e := conn.PrepareContext(t.Context(), q)
		if e != nil {
			t.Fatal(e)
		}
		samples["prepare"] = append(samples["prepare"], time.Since(start))
		s.Close()
	}
	for name, v := range samples {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		brelPerfLog(t, 0, 0, "COST_"+name, v)
	}
	if err = conn.Raw(func(raw any) error {
		g := raw.(*sqliteGuardConn)
		for i := 0; i < 30; i++ {
			start := time.Now()
			physical, e := g.owner.Connect(t.Context())
			if e != nil {
				return e
			}
			samples["physical_open"] = append(samples["physical_open"], time.Since(start))
			physical.Close()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v := samples["physical_open"]
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	brelPerfLog(t, 0, 0, "COST_physical_open", v)
	if st, e := os.Stat(path + "-wal"); e == nil {
		t.Logf("COST_WAL bytes=%d", st.Size())
	}
}

// Final measurements: opt-in, independent disk/WAL fixtures; no SLO relaxation.
func TestBRelOptimizationFinal(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_OPT_FINAL")
	const q = `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-06')`
	for _, n := range []int{0, 100, 1000, 20000} {
		for rep := 0; rep < 5; rep++ {
			base, _ := brelPerfDisk(t, n, false)
			cand, _ := brelPerfDisk(t, n, true)
			if n > 0 {
				admitDefault(t, cand)
			}
			brelPerfLog(t, n, rep, "OPT_base", brelPerfSamples(t, base, q, 10000))
			brelPerfLog(t, n, rep, "OPT_candidate", brelPerfSamples(t, cand, q, 10000))
			base.Close()
			cand.Close()
		}
	}
}

func TestBRelOptimizationCPU(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_OPT_CPU")
	db, _ := brelPerfDisk(t, 0, true)
	f, err := os.Create(os.Getenv("CB_BREL_OPT_CPU_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = pprof.StartCPUProfile(f); err != nil {
		t.Fatal(err)
	}
	defer pprof.StopCPUProfile()
	brelPerfSamples(t, db, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-06')`, 10000)
}

func TestBRelOptimizationCaptureScale(t *testing.T) {
	admissionPerfEnabled(t, "CB_BREL_OPT_CAPTURE")
	n, err := strconv.Atoi(os.Getenv("CB_BREL_OPT_CAPTURE_N"))
	if err != nil {
		t.Fatal(err)
	}
	for rep := 0; rep < 5; rep++ {
		db, path := brelPerfDisk(t, n, true)
		start := time.Now()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		var wal int64
		if st, e := os.Stat(path + "-wal"); e == nil {
			wal = st.Size()
		}
		t.Logf("CAPTURE_SCALE n=%d rep=%d hold_ns=%d rss_peak=%d wal_bytes=%d pages_bytes=%d", n, rep, elapsed, brelPeakRSS(t), wal, int64(countQueryRows(t, db, `PRAGMA page_count`))*4096)
		db.Close()
	}
}
