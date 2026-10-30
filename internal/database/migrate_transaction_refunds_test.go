package database

import (
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func refundInvariantLegacyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrationsThroughVersionForTest(db, 66); err != nil {
		t.Fatal(err)
	}
	return db
}

func refundInvariantSeed(t *testing.T, db *sql.DB, suffix string, amount int64) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO users(id,name,email,password_hash) VALUES('u-` + suffix + `','User','u-` + suffix + `@example.invalid','x')`,
		`INSERT INTO workspaces(id,name) VALUES('w-` + suffix + `','Workspace')`,
		`INSERT INTO accounts(id,workspace_id,name,type) VALUES('a-` + suffix + `','w-` + suffix + `','Card','CREDIT_CARD')`,
		`INSERT INTO invoices(id,account_id,reference,closing_date,due_date) VALUES('i-` + suffix + `','a-` + suffix + `','2026-09',1,2)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,invoice_id,type,amount,date) VALUES(?, ?, ?, ?, ?, 'EXPENSE', ?, 1)`, "t-"+suffix, "w-"+suffix, "u-"+suffix, "a-"+suffix, "i-"+suffix, amount); err != nil {
		t.Fatal(err)
	}
}

func refundInvariantInsert(t *testing.T, db *sql.DB, id, suffix string, amount int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES(?, ?, ?, ?, 'refund', ?)`, id, "w-"+suffix, "t-"+suffix, amount, "u-"+suffix); err != nil {
		t.Fatal(err)
	}
}

func TestMigration67SQLiteCascadeProbe(t *testing.T) {
	db := refundInvariantLegacyDB(t)
	refundInvariantSeed(t, db, "a", 100)
	if _, err := db.Exec(`CREATE TEMP TABLE probe_events(id TEXT, parent_exists INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TEMP TRIGGER probe_refund_delete BEFORE DELETE ON main.transaction_refunds BEGIN INSERT INTO probe_events VALUES(OLD.id, EXISTS(SELECT 1 FROM main.transactions WHERE id=OLD.transaction_id AND workspace_id=OLD.workspace_id)); END`); err != nil {
		t.Fatal(err)
	}
	refundInvariantInsert(t, db, "direct", "a", 10)
	if _, err := db.Exec(`DELETE FROM transaction_refunds WHERE id='direct'`); err != nil {
		t.Fatal(err)
	}
	refundInvariantInsert(t, db, "cascade", "a", 10)
	if _, err := db.Exec(`DELETE FROM transactions WHERE id='t-a'`); err != nil {
		t.Fatal(err)
	}
	var fk, direct, cascade int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT parent_exists FROM probe_events WHERE id='direct'`).Scan(&direct); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT parent_exists FROM probe_events WHERE id='cascade'`).Scan(&cascade); err != nil {
		t.Fatal(err)
	}
	if fk != 1 || direct != 1 || cascade != 0 {
		t.Fatalf("foreign_keys=%d direct=%d cascade=%d", fk, direct, cascade)
	}
}

func TestMigration67UpgradeAndRefundBarriers(t *testing.T) {
	db := refundInvariantLegacyDB(t)
	refundInvariantSeed(t, db, "a", math.MaxInt64)
	refundInvariantSeed(t, db, "b", 100)
	refundInvariantInsert(t, db, "r1", "a", math.MaxInt64-1)
	refundInvariantInsert(t, db, "rb", "b", 10)
	var beforeTable, beforeTransactions string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transaction_refunds'`).Scan(&beforeTable); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&beforeTransactions); err != nil {
		t.Fatal(err)
	}
	if err := runMigrationsThroughVersionForTest(db, 67); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 67)
	var afterTable, afterTransactions string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transaction_refunds'`).Scan(&afterTable); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='transactions'`).Scan(&afterTransactions); err != nil {
		t.Fatal(err)
	}
	if beforeTable != afterTable || beforeTransactions != afterTransactions {
		t.Fatal("table schema changed")
	}
	if _, err := db.Exec(`DELETE FROM transaction_refunds WHERE id='r1'`); err == nil {
		t.Fatal("direct refund delete succeeded")
	}
	if _, err := db.Exec(`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('too-large','w-a','t-a',2,'x','u-a')`); err == nil {
		t.Fatal("over-refund insert succeeded")
	}
	if _, err := db.Exec(`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('real','w-a','t-a',1e20,'x','u-a')`); err == nil {
		t.Fatal("REAL amount insert succeeded")
	}
	refundInvariantInsert(t, db, "r2", "a", 1)
	for _, statement := range []string{
		`UPDATE transaction_refunds SET amount_cents=1 WHERE id='r1'`,
		`UPDATE transaction_refunds SET workspace_id='w-b' WHERE id='r1'`,
		`UPDATE transaction_refunds SET transaction_id='t-b' WHERE id='r1'`,
		`UPDATE transactions SET amount=1 WHERE id='t-a'`,
		`UPDATE transactions SET workspace_id='w-b' WHERE id='t-a'`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("accepted %s", statement)
		}
	}
	if _, err := db.Exec(`UPDATE transaction_refunds SET reversed_at=100,reversed_by='u-a' WHERE id='r2'`); err != nil {
		t.Fatalf("legitimate reversal: %v", err)
	}
	if _, err := db.Exec(`UPDATE transaction_refunds SET reversed_at=NULL,reversed_by=NULL WHERE id='r2'`); err == nil {
		t.Fatal("reactivation succeeded")
	}
	if _, err := db.Exec(`UPDATE transactions SET amount=1 WHERE id='t-a'`); err == nil {
		t.Fatal("floor accepted amount below active refund")
	}
	if _, err := db.Exec(`DELETE FROM transactions WHERE id='t-a'`); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	var aRows, bRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE workspace_id='w-a'`).Scan(&aRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE workspace_id='w-b'`).Scan(&bRows); err != nil {
		t.Fatal(err)
	}
	if aRows != 0 || bRows != 1 {
		t.Fatalf("cascade isolation: A=%d B=%d", aRows, bRows)
	}
	if _, err := db.Exec(`DELETE FROM transactions WHERE id='t-b'`); err != nil {
		t.Fatalf("single refund cascade: %v", err)
	}
}

func TestMigration67RejectsInvalidLegacyDataAtomically(t *testing.T) {
	for _, scenario := range []string{"over-refund", "wrong-workspace", "reactivation-state"} {
		t.Run(scenario, func(t *testing.T) {
			db := refundInvariantLegacyDB(t)
			refundInvariantSeed(t, db, "a", 100)
			refundInvariantSeed(t, db, "b", 100)
			refundInvariantInsert(t, db, "r1", "a", 90)
			switch scenario {
			case "over-refund":
				if _, err := db.Exec(`UPDATE transaction_refunds SET amount_cents=101 WHERE id='r1'`); err != nil {
					t.Fatal(err)
				}
			case "wrong-workspace":
				if _, err := db.Exec(`UPDATE transaction_refunds SET workspace_id='w-b' WHERE id='r1'`); err != nil {
					t.Fatal(err)
				}
			case "reactivation-state":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE transaction_refunds SET reversed_by='u-a' WHERE id='r1'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
					t.Fatal(err)
				}
			}
			err := runMigrations(db)
			if err == nil {
				t.Fatal("invalid historical data accepted")
			}
			if !strings.Contains(err.Error(), "audit transaction refunds") {
				t.Fatalf("unexpected error: %v", err)
			}
			var applied int
			if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=67`).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied != 0 {
				t.Fatal("failed migration registered")
			}
			assertSchemaObjectMissing(t, db, "trigger", "trg_transaction_refunds_no_direct_delete")
		})
	}
}

func TestMigration67AuditsEachWorkspaceIndependently(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		invalidWS string
		wantError string
	}{
		{name: "both-valid"},
		{name: "a-valid-b-over-refunded", invalidWS: "b", wantError: `transaction "t-b" has active refunds above its amount`},
		{name: "a-over-refunded-b-valid", invalidWS: "a", wantError: `transaction "t-a" has active refunds above its amount`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := refundInvariantLegacyDB(t)
			for _, suffix := range []string{"a", "b"} {
				refundInvariantSeed(t, db, suffix, 100)
				refundInvariantInsert(t, db, "r-"+suffix, suffix, 60)
			}
			if scenario.invalidWS != "" {
				if _, err := db.Exec(`UPDATE transaction_refunds SET amount_cents=101 WHERE id=? AND workspace_id=?`, "r-"+scenario.invalidWS, "w-"+scenario.invalidWS); err != nil {
					t.Fatal(err)
				}
			}
			err := runMigrationsThroughVersionForTest(db, 67)
			if scenario.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				assertMigrationApplied(t, db, 67)
				var guardSQL string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='trg_transactions_refund_parent_identity'`).Scan(&guardSQL); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(guardSQL, "r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id") {
					t.Fatalf("migration 67 parent guard lacks workspace scope: %s", guardSQL)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), scenario.wantError) {
				t.Fatalf("migration 67 error = %v, want %q", err, scenario.wantError)
			}
			var applied int
			if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=67`).Scan(&applied); err != nil || applied != 0 {
				t.Fatalf("invalid migration 67 registered: count=%d err=%v", applied, err)
			}
		})
	}
}

func TestMigration67RejectsCrossWorkspaceOwnership(t *testing.T) {
	for _, scenario := range []string{"refund-workspace", "account-workspace", "transaction-invoice"} {
		t.Run(scenario, func(t *testing.T) {
			db := refundInvariantLegacyDB(t)
			refundInvariantSeed(t, db, "a", 100)
			refundInvariantSeed(t, db, "b", 100)
			refundInvariantInsert(t, db, "r-a", "a", 60)
			switch scenario {
			case "refund-workspace":
				_, err := db.Exec(`UPDATE transaction_refunds SET workspace_id='w-b' WHERE id='r-a' AND workspace_id='w-a'`)
				if err != nil {
					t.Fatal(err)
				}
			case "account-workspace":
				_, err := db.Exec(`UPDATE accounts SET workspace_id='w-b' WHERE id='a-a' AND workspace_id='w-a'`)
				if err != nil {
					t.Fatal(err)
				}
			case "transaction-invoice":
				_, err := db.Exec(`UPDATE transactions SET account_id='a-b', invoice_id='i-b' WHERE id='t-a' AND workspace_id='w-a'`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := runMigrationsThroughVersionForTest(db, 67); err == nil || !strings.Contains(err.Error(), "inconsistent ownership or state") {
				t.Fatalf("cross-workspace ownership accepted or wrong error: %v", err)
			}
		})
	}
}

func TestMigration68BlocksOrphanRefundWorkspaceFromVersion66(t *testing.T) {
	db := refundInvariantLegacyDB(t)
	refundInvariantSeed(t, db, "a", 100)
	refundInvariantInsert(t, db, "r-a", "a", 60)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transaction_refunds SET workspace_id='missing' WHERE id='r-a' AND workspace_id='w-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	err := runMigrations(db)
	if err == nil || !strings.Contains(err.Error(), "migration 68") || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("orphan workspace reached final schema or wrong error: %v", err)
	}
	assertMigrationApplied(t, db, 67)
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=68`).Scan(&applied); err != nil || applied != 0 {
		t.Fatalf("orphan registered migration 68: count=%d err=%v", applied, err)
	}
}

func TestMigration67DeleteRollbackRestoresParentAndRefunds(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	refundInvariantSeed(t, db, "a", 100)
	refundInvariantInsert(t, db, "r1", "a", 10)
	refundInvariantInsert(t, db, "r2", "a", 20)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM transactions WHERE id='t-a'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var parents, refunds int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE id='t-a'`).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE transaction_id='t-a'`).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if parents != 1 || refunds != 2 {
		t.Fatalf("rollback parent=%d refunds=%d", parents, refunds)
	}
}

func TestMigration67RestoredDatabaseReopen(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "valid"
		if invalid {
			name = "over-refund"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", root)
			path := filepath.Join(root, "restored.db")
			legacy, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			legacy.SetMaxOpenConns(1)
			if _, err := legacy.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := legacy.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`); err != nil {
				t.Fatal(err)
			}
			if err := runMigrationsThroughVersionForTest(legacy, 66); err != nil {
				t.Fatal(err)
			}
			refundInvariantSeed(t, legacy, "a", 100)
			refundInvariantInsert(t, legacy, "r1", "a", 90)
			if invalid {
				if _, err := legacy.Exec(`UPDATE transaction_refunds SET amount_cents=101 WHERE id='r1'`); err != nil {
					t.Fatal(err)
				}
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}

			// The server's canonical restore calls OpenRuntimeDBs when it
			// reopens the installed candidate, before activating the runtime.
			reopened, err := OpenRuntimeDBs("file:" + path)
			if invalid {
				if reopened != nil {
					reopened.Close()
					t.Fatal("invalid restored database opened")
				}
				if err == nil || !strings.Contains(err.Error(), "active refunds above its amount") {
					t.Fatalf("invalid restore error = %v", err)
				}
				inspect, err := sql.Open("sqlite", "file:"+path)
				if err != nil {
					t.Fatal(err)
				}
				defer inspect.Close()
				var amount int64
				var applied int
				if err := inspect.QueryRow(`SELECT amount_cents FROM transaction_refunds WHERE id='r1'`).Scan(&amount); err != nil {
					t.Fatal(err)
				}
				if err := inspect.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=67`).Scan(&applied); err != nil {
					t.Fatal(err)
				}
				if amount != 101 || applied != 0 {
					t.Fatalf("invalid restore changed money/registration: amount=%d applied=%d", amount, applied)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertMigrationApplied(t, reopened.DB, 67)
			assertSchemaObjectExists(t, reopened.DB, "trigger", "trg_transaction_refunds_no_direct_delete")
			var amount int64
			if err := reopened.DB.QueryRow(`SELECT amount_cents FROM transaction_refunds WHERE id='r1'`).Scan(&amount); err != nil {
				t.Fatal(err)
			}
			if amount != 90 {
				t.Fatalf("valid restore rewrote refund: %d", amount)
			}
		})
	}
}

func TestMigration67ConcurrentRefundInsertsCannotOverRefund(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	path := filepath.Join(root, "concurrent.db")
	owner, err := OpenRuntimeDBs("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	refundInvariantSeed(t, owner.DB, "a", 100)
	// Deferred writers can both hold a read snapshot before either attempts
	// an INSERT. The runtime's immediate pool would serialize Begin itself.
	writers := make([]*sql.DB, 2)
	for n := range writers {
		writers[n], err = sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(1000)&_txlock=deferred")
		if err != nil {
			t.Fatal(err)
		}
		writers[n].SetMaxOpenConns(1)
		defer writers[n].Close()
	}
	ready := make(chan error, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for n, db := range writers {
		workers.Add(1)
		go func(n int, db *sql.DB) {
			defer workers.Done()
			tx, err := db.Begin()
			if err != nil {
				ready <- err
				results <- err
				return
			}
			defer tx.Rollback()
			var initialTotal int64
			err = tx.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM transaction_refunds WHERE workspace_id='w-a' AND transaction_id='t-a' AND reversed_at IS NULL`).Scan(&initialTotal)
			if err == nil && initialTotal != 0 {
				err = fmt.Errorf("writer %d read nonempty initial state: %d", n, initialTotal)
			}
			ready <- err
			if err != nil {
				results <- err
				return
			}
			<-release
			_, err = tx.Exec(`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES(?, 'w-a', 't-a', 60, 'refund', 'u-a')`, n)
			if err == nil {
				err = tx.Commit()
			}
			results <- err
		}(n, db)
	}
	for range writers {
		if err := <-ready; err != nil {
			close(release)
			workers.Wait()
			t.Fatalf("overlap setup: %v", err)
		}
	}
	close(release)
	workers.Wait()
	close(results)
	var succeeded int
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	var total, count int64
	if err := owner.DB.QueryRow(`SELECT COALESCE(SUM(amount_cents),0), COUNT(*) FROM transaction_refunds WHERE transaction_id='t-a' AND reversed_at IS NULL`).Scan(&total, &count); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || total != 60 || count != 1 {
		t.Fatalf("concurrent inserts succeeded=%d total=%d count=%d", succeeded, total, count)
	}
}

func TestMigration67AcceptsExactMaximumInt64Refund(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	refundInvariantSeed(t, db, "a", math.MaxInt64)
	refundInvariantInsert(t, db, "maximum", "a", math.MaxInt64)
	if _, err := db.Exec(`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('extra','w-a','t-a',1,'refund','u-a')`); err == nil {
		t.Fatal("insert beyond maximum accepted")
	}
}
