package database

import (
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func refundInvariantVersion67DB(t *testing.T) *sql.DB {
	t.Helper()
	db := refundInvariantLegacyDB(t)
	if err := runMigrationsThroughVersionForTest(db, 67); err != nil {
		t.Fatal(err)
	}
	return db
}

func refundMustReject(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err == nil {
		t.Fatalf("accepted invalid SQL: %s", statement)
	}
}

func TestMigration68FreshInstallParentTypeAndExtremeInteger(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertMigrationApplied(t, db, 67)
	assertMigrationApplied(t, db, 68)
	refundInvariantSeed(t, db, "a", 100)
	refundInvariantInsert(t, db, "regular", "a", 60)
	refundMustReject(t, db, `UPDATE transactions SET amount=1e20 WHERE id='t-a' AND workspace_id='w-a'`)
	// A transaction without refunds may still contain legacy SQLite REAL;
	// the INSERT guard must reject it before any financial aggregation.
	refundInvariantSeed(t, db, "b", 100)
	if _, err := db.Exec(`UPDATE transactions SET amount=1e20 WHERE id='t-b' AND workspace_id='w-b'`); err != nil {
		t.Fatal(err)
	}
	refundMustReject(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('overflow-a','w-b','t-b',9223372036854775807,'x','u-b')`)
	refundMustReject(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('overflow-b','w-b','t-b',1,'x','u-b')`)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE workspace_id='w-b' AND transaction_id='t-b'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("REAL parent persisted refund: count=%d err=%v", count, err)
	}
	var activeTotal int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM transaction_refunds WHERE workspace_id='w-b' AND transaction_id='t-b' AND reversed_at IS NULL`).Scan(&activeTotal); err != nil || activeTotal != 0 {
		t.Fatalf("impossible overflow state became readable: total=%d err=%v", activeTotal, err)
	}
	if _, err := db.Exec(`UPDATE transactions SET amount='not-a-number' WHERE id='t-b' AND workspace_id='w-b'`); err == nil {
		refundMustReject(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('text-parent','w-b','t-b',1,'x','u-b')`)
	}

	refundInvariantSeed(t, db, "max", math.MaxInt64)
	refundInvariantInsert(t, db, "maximum", "max", math.MaxInt64)
	refundMustReject(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('above-max','w-max','t-max',1,'x','u-max')`)
	refundMustReject(t, db, `UPDATE transactions SET amount=1e20 WHERE id='t-max' AND workspace_id='w-max'`)
	refundMustReject(t, db, `UPDATE transactions SET amount=9223372036854775806 WHERE id='t-max' AND workspace_id='w-max'`)
	if _, err := db.Exec(`UPDATE transactions SET amount=9223372036854775807 WHERE id='t-max' AND workspace_id='w-max'`); err != nil {
		t.Fatalf("valid INTEGER update: %v", err)
	}
}

func TestMigration68UpgradeRejectsInvalidVersion67Atomically(t *testing.T) {
	for _, scenario := range []string{"parent-real", "account-moved", "invoice-reparented", "refund-workspace", "orphan-refund-workspace", "over-refund", "reversal-state"} {
		t.Run(scenario, func(t *testing.T) {
			db := refundInvariantVersion67DB(t)
			refundInvariantSeed(t, db, "a", 100)
			refundInvariantSeed(t, db, "b", 100)
			refundInvariantInsert(t, db, "r1", "a", 60)
			switch scenario {
			case "parent-real":
				_, err := db.Exec(`UPDATE transactions SET amount=1e20 WHERE id='t-a' AND workspace_id='w-a'`)
				if err != nil {
					t.Fatal(err)
				}
			case "account-moved":
				_, err := db.Exec(`UPDATE accounts SET workspace_id='w-b' WHERE id='a-a' AND workspace_id='w-a'`)
				if err != nil {
					t.Fatal(err)
				}
			case "invoice-reparented":
				if _, err := db.Exec(`UPDATE invoices SET reference='2026-10' WHERE id='i-b'`); err != nil {
					t.Fatal(err)
				}
				_, err := db.Exec(`UPDATE invoices SET account_id='a-b' WHERE id='i-a'`)
				if err != nil {
					t.Fatal(err)
				}
			case "refund-workspace":
				if _, err := db.Exec(`DROP TRIGGER trg_transaction_refunds_immutable_identity`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE transaction_refunds SET workspace_id='w-b' WHERE id='r1' AND workspace_id='w-a'`); err != nil {
					t.Fatal(err)
				}
			case "orphan-refund-workspace":
				if _, err := db.Exec(`DROP TRIGGER trg_transaction_refunds_immutable_identity`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE transaction_refunds SET workspace_id='missing' WHERE id='r1' AND workspace_id='w-a'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			case "over-refund":
				_, err := db.Exec(`UPDATE transactions SET amount=50 WHERE id='t-a' AND workspace_id='w-a'`)
				if err == nil {
					t.Fatal("version 67 unexpectedly accepted over-refund")
				}
				// A version-67 database can still contain a legacy state if its
				// trigger was absent or disabled during a restore.
				if _, err := db.Exec(`DROP TRIGGER trg_transactions_refund_floor`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE transactions SET amount=50 WHERE id='t-a' AND workspace_id='w-a'`); err != nil {
					t.Fatal(err)
				}
			case "reversal-state":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE transaction_refunds SET reversed_by='u-a' WHERE id='r1' AND workspace_id='w-a'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
					t.Fatal(err)
				}
			}
			err := runMigrations(db)
			if err == nil || !strings.Contains(err.Error(), "audit transaction refunds for migration 68") {
				t.Fatalf("invalid v67 accepted or wrong error: %v", err)
			}
			var applied int
			if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=68`).Scan(&applied); err != nil || applied != 0 {
				t.Fatalf("failed migration registered: count=%d err=%v", applied, err)
			}
			assertSchemaObjectMissing(t, db, "trigger", "trg_accounts_refund_workspace_identity")
			// A second attempt must find the same bad row; migration 68 never repairs it.
			if second := runMigrations(db); second == nil {
				t.Fatal("invalid data auto-corrected after failed migration")
			}
		})
	}
}

func TestMigration68OwnershipGuardsAndCascade(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	refundInvariantSeed(t, db, "a", 100)
	refundInvariantSeed(t, db, "b", 100)
	refundInvariantInsert(t, db, "r1", "a", 60)
	refundMustReject(t, db, `UPDATE accounts SET workspace_id='w-b' WHERE id='a-a' AND workspace_id='w-a'`)
	if _, err := db.Exec(`UPDATE invoices SET reference='2026-10' WHERE id='i-b'`); err != nil {
		t.Fatal(err)
	}
	refundMustReject(t, db, `UPDATE invoices SET account_id='a-b' WHERE id='i-a'`)
	refundMustReject(t, db, `UPDATE transactions SET workspace_id='w-b' WHERE id='t-a' AND workspace_id='w-a'`)
	refundMustReject(t, db, `UPDATE transactions SET invoice_id='i-b' WHERE id='t-a' AND workspace_id='w-a'`)
	refundMustReject(t, db, `UPDATE transactions SET account_id='a-b' WHERE id='t-a' AND workspace_id='w-a'`)
	refundMustReject(t, db, `UPDATE transaction_refunds SET workspace_id='w-b' WHERE id='r1' AND workspace_id='w-a'`)
	refundMustReject(t, db, `UPDATE transaction_refunds SET transaction_id='t-b' WHERE id='r1' AND workspace_id='w-a'`)
	refundMustReject(t, db, `DELETE FROM transaction_refunds WHERE id='r1' AND workspace_id='w-a'`)
	var accountWS, transactionWS, refundWS, invoiceAccount string
	if err := db.QueryRow(`SELECT workspace_id FROM accounts WHERE id='a-a' AND workspace_id='w-a'`).Scan(&accountWS); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT account_id FROM invoices WHERE id='i-a'`).Scan(&invoiceAccount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT workspace_id FROM transactions WHERE id='t-a' AND workspace_id='w-a'`).Scan(&transactionWS); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT workspace_id FROM transaction_refunds WHERE id='r1' AND workspace_id='w-a'`).Scan(&refundWS); err != nil {
		t.Fatal(err)
	}
	if accountWS != "w-a" || invoiceAccount != "a-a" || transactionWS != "w-a" || refundWS != "w-a" {
		t.Fatalf("ownership drift: %q %q %q %q", accountWS, invoiceAccount, transactionWS, refundWS)
	}
	if _, err := db.Exec(`INSERT INTO invoices(id,account_id,reference,closing_date,due_date) VALUES('i-a-2','a-a','2026-10',1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transactions SET invoice_id='i-a-2' WHERE id='t-a' AND workspace_id='w-a'`); err != nil {
		t.Fatalf("valid transaction invoice edit: %v", err)
	}
	if _, err := db.Exec(`UPDATE transaction_refunds SET reversed_at=100,reversed_by='u-a' WHERE id='r1' AND workspace_id='w-a'`); err != nil {
		t.Fatalf("valid reversal: %v", err)
	}
	refundMustReject(t, db, `UPDATE transaction_refunds SET reversed_at=NULL,reversed_by=NULL WHERE id='r1' AND workspace_id='w-a'`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM transactions WHERE id='t-a' AND workspace_id='w-a'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE workspace_id='w-a' AND transaction_id='t-a'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`DELETE FROM transactions WHERE id='t-a' AND workspace_id='w-a'`); err != nil {
		t.Fatalf("parent cascade: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transaction_refunds WHERE workspace_id='w-a' AND transaction_id='t-a'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade count=%d err=%v", count, err)
	}
}

type refundFinancialSnapshot struct {
	Accounts     []string
	Invoices     []string
	Transactions []string
	Refunds      []string
}

func refundSnapshot(t *testing.T, db *sql.DB, suffixes []string) refundFinancialSnapshot {
	t.Helper()
	var snapshot refundFinancialSnapshot
	for _, suffix := range suffixes {
		ws := "w-" + suffix
		rows, err := db.Query(`SELECT id, workspace_id FROM accounts WHERE workspace_id=? ORDER BY id`, ws)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, workspace string
			if err := rows.Scan(&id, &workspace); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot.Accounts = append(snapshot.Accounts, id+"|"+workspace)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		rows, err = db.Query(`SELECT i.id, i.account_id FROM invoices i JOIN accounts a ON a.id=i.account_id AND a.workspace_id=? ORDER BY i.id`, ws)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, account string
			if err := rows.Scan(&id, &account); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot.Invoices = append(snapshot.Invoices, id+"|"+account)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		rows, err = db.Query(`SELECT id, workspace_id, account_id, invoice_id, quote(amount), typeof(amount) FROM transactions WHERE workspace_id=? ORDER BY id`, ws)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, workspace, account, invoice, amount, amountType string
			if err := rows.Scan(&id, &workspace, &account, &invoice, &amount, &amountType); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot.Transactions = append(snapshot.Transactions, fmt.Sprintf("%s|%s|%s|%s|%s|%s", id, workspace, account, invoice, amount, amountType))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		rows, err = db.Query(`SELECT id, workspace_id, transaction_id, amount_cents, typeof(amount_cents), reversed_at, reversed_by FROM transaction_refunds WHERE workspace_id=? ORDER BY id`, ws)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, workspace, transaction, amountType string
			var amount int64
			var reversedAt sql.NullInt64
			var reversedBy sql.NullString
			if err := rows.Scan(&id, &workspace, &transaction, &amount, &amountType, &reversedAt, &reversedBy); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			snapshot.Refunds = append(snapshot.Refunds, fmt.Sprintf("%s|%s|%s|%d|%s|%v|%v", id, workspace, transaction, amount, amountType, reversedAt, reversedBy))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func TestMigration68UpgradePreservesFiveFinancialStates(t *testing.T) {
	for _, initialVersion := range []int{66, 67} {
		t.Run(fmt.Sprintf("from-%d", initialVersion), func(t *testing.T) {
			db := refundInvariantLegacyDB(t)
			suffixes := []string{"u1", "u2", "u3", "u4", "u5"}
			for n, suffix := range suffixes {
				refundInvariantSeed(t, db, suffix, int64((n+1)*100))
			}
			refundInvariantInsert(t, db, "partial", "u2", 40)
			refundInvariantInsert(t, db, "multiple-a", "u3", 70)
			refundInvariantInsert(t, db, "multiple-b", "u3", 80)
			refundInvariantInsert(t, db, "full", "u4", 400)
			refundInvariantInsert(t, db, "reversed", "u5", 90)
			if _, err := db.Exec(`UPDATE transaction_refunds SET reversed_at=123,reversed_by='u-u5' WHERE id='reversed' AND workspace_id='w-u5'`); err != nil {
				t.Fatal(err)
			}
			if initialVersion == 67 {
				if err := runMigrationsThroughVersionForTest(db, 67); err != nil {
					t.Fatal(err)
				}
				// Model a database that already ran the historical migration 67:
				// its parent guard looked up refunds by transaction ID alone.
				var guardSQL string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='trg_transactions_refund_parent_identity'`).Scan(&guardSQL); err != nil {
					t.Fatal(err)
				}
				oldGuardSQL := strings.Replace(guardSQL,
					"r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id",
					"r.transaction_id = OLD.id", 1)
				if oldGuardSQL == guardSQL {
					t.Fatal("migration 67 guard shape changed")
				}
				if _, err := db.Exec(`DROP TRIGGER trg_transactions_refund_parent_identity`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(oldGuardSQL); err != nil {
					t.Fatal(err)
				}
			}
			before := refundSnapshot(t, db, suffixes)
			if len(before.Transactions) != 5 || len(before.Refunds) != 5 {
				t.Fatalf("incomplete fixture: %+v", before)
			}
			var beforeTables []string
			for _, table := range []string{"accounts", "invoices", "transactions", "transaction_refunds"} {
				var ddl string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
					t.Fatal(err)
				}
				beforeTables = append(beforeTables, ddl)
			}
			if err := runMigrationsThroughVersionForTest(db, 68); err != nil {
				t.Fatal(err)
			}
			assertMigrationApplied(t, db, 67)
			assertMigrationApplied(t, db, 68)
			if initialVersion == 67 {
				var guardSQL string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='trg_transactions_refund_parent_identity'`).Scan(&guardSQL); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(guardSQL, "r.transaction_id = OLD.id AND r.workspace_id = OLD.workspace_id") {
					t.Fatalf("migration 68 did not replace historical parent guard: %s", guardSQL)
				}
			}
			if after := refundSnapshot(t, db, suffixes); !reflect.DeepEqual(before, after) {
				t.Fatalf("financial data changed: before=%+v after=%+v", before, after)
			}
			for n, table := range []string{"accounts", "invoices", "transactions", "transaction_refunds"} {
				var ddl string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
					t.Fatal(err)
				}
				if ddl != beforeTables[n] {
					t.Fatalf("%s table rebuilt", table)
				}
			}
		})
	}
}

func TestMigration68RestoreVersion67ValidAndInvalid(t *testing.T) {
	for _, scenario := range []string{"valid", "parent-real", "ownership"} {
		t.Run(scenario, func(t *testing.T) {
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
			if err := runMigrationsThroughVersionForTest(legacy, 67); err != nil {
				t.Fatal(err)
			}
			refundInvariantSeed(t, legacy, "a", 100)
			refundInvariantSeed(t, legacy, "b", 100)
			refundInvariantInsert(t, legacy, "r1", "a", 60)
			if scenario == "parent-real" {
				if _, err := legacy.Exec(`UPDATE transactions SET amount=1e20 WHERE id='t-a' AND workspace_id='w-a'`); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "ownership" {
				if _, err := legacy.Exec(`UPDATE accounts SET workspace_id='w-b' WHERE id='a-a' AND workspace_id='w-a'`); err != nil {
					t.Fatal(err)
				}
			}
			before := refundSnapshot(t, legacy, []string{"a", "b"})
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenRuntimeDBs("file:" + path)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				assertMigrationApplied(t, reopened.DB, 68)
				if after := refundSnapshot(t, reopened.DB, []string{"a", "b"}); !reflect.DeepEqual(before, after) {
					t.Fatalf("valid restore changed financial data: before=%+v after=%+v", before, after)
				}
				return
			}
			if reopened != nil {
				reopened.Close()
				t.Fatal("invalid restore opened")
			}
			if err == nil || !strings.Contains(err.Error(), "audit transaction refunds for migration 68") {
				t.Fatalf("invalid restore error: %v", err)
			}
			inspect, err := sql.Open("sqlite", "file:"+path)
			if err != nil {
				t.Fatal(err)
			}
			defer inspect.Close()
			if after := refundSnapshot(t, inspect, []string{"a", "b"}); !reflect.DeepEqual(before, after) {
				t.Fatalf("restore changed financial data: before=%+v after=%+v", before, after)
			}
			var applied int
			if err := inspect.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=68`).Scan(&applied); err != nil || applied != 0 {
				t.Fatalf("invalid restore registered 68: %d %v", applied, err)
			}
		})
	}
}
