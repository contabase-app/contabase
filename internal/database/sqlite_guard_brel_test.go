//go:build contabase_legacy_fixtures

package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
)

func TestBRelBothPoolsConnectionIntegrity(t *testing.T) {
	for _, mode := range []string{"deferred", "immediate"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			path := filepath.Join(t.TempDir(), "guard.db")
			fixture := admissionCandidateDB(t)
			mustExecOK(t, fixture, admissionTxNULLFixture, -1, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
			admitDefault(t, fixture)
			mustExecOK(t, fixture, `VACUUM INTO ?`, path)
			if err := RegisterLegacyInvestmentFixture(path); err != nil {
				t.Fatal(err)
			}
			p, err := OpenLegacyInvestmentFixtureDBs(path + "?_pragma=foreign_keys(0)&_pragma=defer_foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			db := p.DB
			if mode == "immediate" {
				db = p.Immediate
			}
			for _, pragma := range []string{"foreign_keys", "defer_foreign_keys"} {
				want := 1
				if pragma == "defer_foreign_keys" {
					want = 0
				}
				if countQueryRows(t, db, "PRAGMA "+pragma) != want {
					t.Fatal("hostile DSN accepted")
				}
			}
			for _, q := range []string{
				`PRAGMA foreign_keys=OFF`, `PRAGMA defer_foreign_keys=ON`, `PRAGMA main."defer_foreign_keys"(ON)`,
				`/* hostile */ PRAGMA defer_foreign_keys(ON)`,
				`PRAGMA defer_foreign_keys=ON; DELETE FROM transactions WHERE rowid=-1; PRAGMA defer_foreign_keys=OFF`,
				`SELECT 1; PRAGMA defer_foreign_keys=ON`, `ATTACH ':memory:' AS escape`, `DROP TRIGGER trg_admission_occurrence_insert`,
			} {
				if _, err := db.Exec(q); !errors.Is(err, errSQLiteWriteGuard) {
					t.Fatalf("unsafe SQL not rejected: %s: %v", q, err)
				}
			}
			if _, err := db.Prepare(`PRAGMA defer_foreign_keys=ON`); !errors.Is(err, errSQLiteWriteGuard) {
				t.Fatal("prepared toggle accepted")
			}
			// A caller-owned transaction and its bound prepared statement must
			// observe the very same physical connection at every DML and commit.
			for _, at := range []string{"before-dml", "prepared", "before-commit", "foreign-keys"} {
				t.Run(at, func(t *testing.T) {
					conn, err := db.Conn(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					var physical *sqliteGuardConn
					if err = conn.Raw(func(raw any) error { physical = raw.(*sqliteGuardConn); return nil }); err != nil {
						t.Fatal(err)
					}
					if at == "foreign-keys" {
						_, err = physical.rawExec(t.Context(), `PRAGMA foreign_keys=OFF`, nil)
						if err != nil {
							t.Fatal(err)
						}
						if _, err = conn.BeginTx(t.Context(), nil); !errors.Is(err, errSQLiteWriteGuard) {
							t.Fatal("bad FK connection began transaction")
						}
					} else {
						tx, err := conn.BeginTx(t.Context(), nil)
						if err != nil {
							t.Fatal(err)
						}
						q := `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('guard-prior','ws-1','user-1','acc-checking','EXPENSE',1,1,'2026-03-05')`
						if _, err = tx.Exec(q); err != nil {
							t.Fatal(err)
						}
						stmt, err := tx.Prepare(`UPDATE transactions SET description='second' WHERE workspace_id='ws-1' AND id='guard-prior'`)
						if err != nil {
							t.Fatal(err)
						}
						// Fault injection is internal to this test, not an exposed escape.
						_, err = physical.rawExec(t.Context(), `PRAGMA defer_foreign_keys=ON`, nil)
						if err != nil {
							t.Fatal(err)
						}
						switch at {
						case "before-dml":
							_, err = tx.Exec(`DELETE FROM transactions WHERE workspace_id='ws-1' AND rowid=-1`)
						case "prepared":
							_, err = stmt.Exec()
						case "before-commit":
							err = tx.Commit()
						}
						if !errors.Is(err, errSQLiteWriteGuard) {
							t.Fatalf("drift not rejected: %v", err)
						}
						stmt.Close()
						_ = tx.Rollback()
					}
					if !physical.bad {
						t.Fatal("connection not quarantined")
					}
					conn.Close()
					if countQueryRows(t, db, `SELECT count(*) FROM transactions WHERE workspace_id='ws-1' AND id='guard-prior'`) != 0 {
						t.Fatal("guard failure committed prior statement")
					}
					if countQueryRows(t, db, `SELECT count(*) FROM transactions WHERE workspace_id='ws-1' AND rowid=-1`) != 1 {
						t.Fatal("guard lost protected victim")
					}
				})
			}
			db.SetMaxIdleConns(0)
			for i := 0; i < 2; i++ {
				if countQueryRows(t, db, `PRAGMA foreign_keys`) != 1 || countQueryRows(t, db, `PRAGMA defer_foreign_keys`) != 0 {
					t.Fatal("recreated connection unsafe")
				}
			}
			if _, err = db.Exec(`INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,notes) VALUES('ws-1','user-1','acc-checking','EXPENSE',1,1,'2026-03-05','literal ; PRAGMA defer_foreign_keys=ON')`); err != nil {
				t.Fatal("quoted text misclassified", err)
			}
		})
	}
}

// Reject rather than retry after a commit error, even if the engine committed.
type brelUncertainTx struct{ driver.Tx }

func (t brelUncertainTx) Commit() error {
	if err := t.Tx.Commit(); err != nil {
		return err
	}
	return errors.New("injected uncertain commit")
}
func TestBRelGuardCommitUncertain(t *testing.T) {
	db, c, err := newSQLiteGuardPool(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExecOK(t, db, `CREATE TABLE test_effects(id INTEGER PRIMARY KEY)`)
	c.enabled.Store(true)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var g *sqliteGuardConn
	if err = conn.Raw(func(raw any) error { g = raw.(*sqliteGuardConn); return nil }); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO test_effects VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	wrapped := &sqliteGuardTx{c: g, raw: brelUncertainTx{g.active}, ctx: t.Context()}
	if err = wrapped.Commit(); err == nil || !g.bad {
		t.Fatal("uncertain commit treated as retryable/reusable")
	}
	_ = tx.Rollback()
}

func TestBRelGuardTransactionControls(t *testing.T) {
	db, c, err := newSQLiteGuardPool(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExecOK(t, db, `CREATE TABLE test_effects(id INTEGER PRIMARY KEY)`)
	c.enabled.Store(true)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, end := range []string{"COMMIT /* comment */;", "ROLLBACK -- comment"} {
		if _, err = conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.ExecContext(t.Context(), end); err != nil {
			t.Fatal(err)
		}
		if err = conn.Raw(func(raw any) error {
			if raw.(*sqliteGuardConn).manual {
				t.Fatal("manual transaction flag survived end")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO test_effects VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`SAVEPOINT request_scope`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`ROLLBACK TO request_scope`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`ROLLBACK`); !errors.Is(err, errSQLiteWriteGuard) {
		t.Fatal("raw transaction end escaped caller-owned transaction", err)
	}
	_ = tx.Rollback()
}
