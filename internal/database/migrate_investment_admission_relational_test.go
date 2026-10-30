package database

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// B-INT probes only: this miniature schema never enters a migration or the
// candidate. It uses the existing SQLite test fixture and driver. The named,
// non-NULL occurrence key is independent of the source's mutable raw rowid.
func admissionRelationalProbeSetup(t *testing.T, db *sql.DB, action, deferral string) {
	t.Helper()
	mustExecOK(t, db, `PRAGMA foreign_keys=ON`)
	mustExecOK(t, db, `PRAGMA defer_foreign_keys=OFF`)
	if got := countQueryRows(t, db, `PRAGMA foreign_keys`); got != 1 {
		t.Fatalf("foreign_keys=%d", got)
	}
	statements := []string{
		`CREATE TABLE bint_occurrences(occurrence_key TEXT NOT NULL UNIQUE,workspace_id TEXT NOT NULL,account_id TEXT NOT NULL,destination_account_id TEXT,legacy_id TEXT,payload BLOB NOT NULL,UNIQUE(workspace_id,occurrence_key))`,
		fmt.Sprintf(`CREATE TABLE bint_pins(workspace_id TEXT NOT NULL,occurrence_key TEXT NOT NULL,account_id TEXT NOT NULL,PRIMARY KEY(workspace_id,occurrence_key,account_id),FOREIGN KEY(workspace_id,occurrence_key) REFERENCES bint_occurrences(workspace_id,occurrence_key) ON DELETE %s ON UPDATE %s %s) WITHOUT ROWID`, action, action, deferral),
		`CREATE TABLE bint_delete_audit(occurrence_key TEXT NOT NULL)`,
		`CREATE TRIGGER bint_delete_observer AFTER DELETE ON bint_occurrences BEGIN INSERT INTO bint_delete_audit VALUES(OLD.occurrence_key); END`,
		`INSERT INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,destination_account_id,legacy_id,payload) VALUES(201,'occ-1','w1','a','b',NULL,x'0102'),(202,'occ-2','w1','a','b',NULL,x'0102'),(301,'occ-foreign','w2','c',NULL,NULL,x'0102'),(501,'unprotected','w2','free',NULL,NULL,x'0102')`,
		`INSERT INTO bint_pins VALUES('w1','occ-1','a'),('w1','occ-1','b'),('w1','occ-2','a'),('w1','occ-2','b'),('w2','occ-foreign','c')`,
	}
	for _, q := range statements {
		mustExecOK(t, db, q)
	}
}

func admissionRelationalProbeSnapshot(t *testing.T, db *sql.DB, physical bool) []string {
	t.Helper()
	var snapshot []string
	prefix := ""
	if physical {
		prefix = "rowid,"
	}
	for _, workspace := range []string{"w1", "w2"} {
		for _, q := range []string{
			`SELECT json_array(` + prefix + `occurrence_key,workspace_id,account_id,destination_account_id,legacy_id,hex(payload)) FROM bint_occurrences WHERE workspace_id=? ORDER BY occurrence_key`,
			`SELECT json_array(occurrence_key,account_id) FROM bint_pins WHERE workspace_id=? ORDER BY occurrence_key,account_id`,
		} {
			rows, err := db.Query(q, workspace)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				snapshot = append(snapshot, s)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot = append(snapshot, fmt.Sprintf("delete-observer=%d", countQueryRows(t, db, `SELECT COUNT(*) FROM bint_delete_audit`)))
	return snapshot
}

func admissionRelationalProbeReject(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	before := admissionRelationalProbeSnapshot(t, db, true)
	_, err := db.Exec(q, args...)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("expected native FK RESTRICT, got %v", err)
	}
	if !reflect.DeepEqual(before, admissionRelationalProbeSnapshot(t, db, true)) {
		t.Fatal("rejected statement changed physical snapshot")
	}
}

func TestMigration78RelationalProbeRestrict(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		for _, deferral := range []string{"NOT DEFERRABLE INITIALLY IMMEDIATE", "DEFERRABLE INITIALLY DEFERRED"} {
			t.Run(fmt.Sprintf("recursive=%d/%s", recursive, deferral), func(t *testing.T) {
				db := openInvestmentsTestDB(t)
				admissionRelationalProbeSetup(t, db, "RESTRICT", deferral)
				mustExecOK(t, db, fmt.Sprintf(`PRAGMA recursive_triggers=%d`, recursive))
				// Two exact, separate source occurrences each have two legitimate pins.
				if got := countQueryRows(t, db, `SELECT COUNT(*) FROM bint_occurrences WHERE workspace_id='w1' AND account_id='a' AND destination_account_id='b' AND legacy_id IS NULL AND payload=x'0102'`); got != 2 {
					t.Fatalf("duplicate occurrences=%d", got)
				}
				for _, account := range []string{"a", "b"} {
					if got := countQueryRows(t, db, `SELECT COUNT(*) FROM bint_pins WHERE workspace_id='w1' AND account_id=?`, account); got != 2 {
						t.Fatalf("dual-scope pins=%d", got)
					}
				}
				// An unprotected REPLACE proves the DELETE observer is absent in OFF.
				mustExecOK(t, db, `INSERT OR REPLACE INTO bint_occurrences(occurrence_key,workspace_id,account_id,payload) VALUES('unprotected','w2','free',x'0304')`)
				if got := countQueryRows(t, db, `SELECT COUNT(*) FROM bint_delete_audit`); got != recursive {
					t.Fatalf("implicit DELETE observer=%d, mode=%d", got, recursive)
				}
				mustExecOK(t, db, `DELETE FROM bint_delete_audit`)
				t.Logf("control recursive=%d implicit DELETE observer=%d; protected retention tested below", recursive, recursive)
				for _, target := range []struct{ w, a string }{{"w1", "a"}, {"w1", "other"}, {"w2", "free"}} {
					// Final key may even be identical: RESTRICT must reject the deletion,
					// not merely accept a reinserted parent at the end of the statement.
					admissionRelationalProbeReject(t, db, `INSERT OR REPLACE INTO bint_occurrences(occurrence_key,workspace_id,account_id,payload) VALUES('occ-1',?,?,x'FFFF')`, target.w, target.a)
					admissionRelationalProbeReject(t, db, `INSERT OR REPLACE INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,payload) VALUES(201,'new-key',?,?,x'FFFF')`, target.w, target.a)
				}
				admissionRelationalProbeReject(t, db, `INSERT OR REPLACE INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,payload) VALUES(301,'new-key','w1','other',x'FFFF')`)
				admissionRelationalProbeReject(t, db, `UPDATE OR REPLACE bint_occurrences SET occurrence_key='occ-1' WHERE workspace_id='w2' AND occurrence_key='unprotected'`)
				admissionRelationalProbeReject(t, db, `UPDATE bint_occurrences SET occurrence_key='changed-key' WHERE workspace_id='w1' AND occurrence_key='occ-1'`)
				admissionRelationalProbeReject(t, db, `DELETE FROM bint_occurrences WHERE workspace_id='w1' AND occurrence_key='occ-1'`)
				admissionRelationalProbeReject(t, db, `INSERT INTO bint_pins VALUES('w2','occ-1','c')`)
				admissionRelationalProbeReject(t, db, `INSERT OR REPLACE INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,payload) VALUES(910,'good','w2','free',x'00'),(201,'bad','w2','free',x'FF')`)
				// RESTRICT fires during the statement even for a deferred FK.
				before := admissionRelationalProbeSnapshot(t, db, true)
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`INSERT INTO bint_occurrences(occurrence_key,workspace_id,account_id,payload) VALUES('prior','w2','free',x'00')`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`DELETE FROM bint_occurrences WHERE workspace_id='w1' AND occurrence_key='occ-1'`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
					t.Fatalf("RESTRICT not immediate in explicit transaction: %v", err)
				}
				var prior int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM bint_occurrences WHERE workspace_id='w2' AND occurrence_key='prior'`).Scan(&prior); err != nil || prior != 1 {
					t.Fatalf("ABORT lost prior statement: %d %v", prior, err)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, admissionRelationalProbeSnapshot(t, db, true)) {
					t.Fatal("rollback changed snapshot")
				}
				// Controls establish the limits: FKs alone do not seal content or pins.
				tx, err = db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`UPDATE bint_occurrences SET payload=x'FFFF',account_id='other' WHERE workspace_id='w1' AND occurrence_key='occ-1'`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`DELETE FROM bint_pins WHERE workspace_id='w1' AND occurrence_key='occ-1'`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`DELETE FROM bint_occurrences WHERE workspace_id='w1' AND occurrence_key='occ-1'`); err != nil {
					t.Fatal(err)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, admissionRelationalProbeSnapshot(t, db, true)) {
					t.Fatal("control rollback changed snapshot")
				}
				t.Log("controls: mutable payload/ownership and removable pins bypass retention without seals/immutable guards")
				if fk := countQueryRows(t, db, `SELECT COUNT(*) FROM pragma_foreign_key_check`); fk != 0 {
					t.Fatalf("foreign_key_check=%d", fk)
				}
			})
		}
	}
}

func TestMigration78RelationalProbeDeferral(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		for _, deferral := range []string{"NOT DEFERRABLE INITIALLY IMMEDIATE", "DEFERRABLE INITIALLY DEFERRED"} {
			for _, action := range []string{"RESTRICT", "NO ACTION"} {
				for _, override := range []int{0, 1} {
					t.Run(fmt.Sprintf("recursive=%d/%s/action=%s/pragma_defer=%d", recursive, deferral, action, override), func(t *testing.T) {
						db := openInvestmentsTestDB(t)
						admissionRelationalProbeSetup(t, db, action, deferral)
						mustExecOK(t, db, fmt.Sprintf(`PRAGMA recursive_triggers=%d`, recursive))
						// Manual SQL transactions keep the failed COMMIT on the same sole
						// connection until explicit ROLLBACK, rather than hiding driver cleanup.
						mustExecOK(t, db, `BEGIN`)
						mustExecOK(t, db, fmt.Sprintf(`PRAGMA defer_foreign_keys=%d`, override))
						_, replaceErr := db.Exec(`INSERT OR REPLACE INTO bint_occurrences(occurrence_key,workspace_id,account_id,payload) VALUES('occ-1','w1','a',x'FFFF')`)
						_, commitErr := db.Exec(`COMMIT`)
						if commitErr != nil {
							mustExecOK(t, db, `ROLLBACK`)
						}
						t.Logf("same-key REPLACE: statement=%v commit=%v", replaceErr, commitErr)
						if action == "RESTRICT" && override == 0 && (replaceErr == nil || !strings.Contains(replaceErr.Error(), "FOREIGN KEY constraint failed")) {
							t.Fatal("contractual RESTRICT failed to retain parent")
						}
						if replaceErr == nil && commitErr == nil && countQueryRows(t, db, `SELECT COUNT(*) FROM bint_occurrences WHERE workspace_id='w1' AND occurrence_key='occ-1' AND payload=x'FFFF'`) != 1 {
							t.Fatal("accepted control did not replace content")
						}
						before := admissionRelationalProbeSnapshot(t, db, true)
						mustExecOK(t, db, `BEGIN`)
						mustExecOK(t, db, fmt.Sprintf(`PRAGMA defer_foreign_keys=%d`, override))
						_, deleteErr := db.Exec(`DELETE FROM bint_occurrences WHERE workspace_id='w1' AND occurrence_key='occ-1'`)
						_, commitErr = db.Exec(`COMMIT`)
						if commitErr != nil {
							mustExecOK(t, db, `ROLLBACK`)
						}
						t.Logf("DELETE without reinsert: statement=%v commit=%v", deleteErr, commitErr)
						if deleteErr == nil && commitErr == nil {
							t.Fatal("dangling protected pin committed")
						}
						if !reflect.DeepEqual(before, admissionRelationalProbeSnapshot(t, db, true)) {
							t.Fatal("deferred rollback changed snapshot")
						}
					})
				}
			}
		}
	}
}

func TestMigration78RelationalProbeStableKeyWAL(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		t.Run(fmt.Sprintf("recursive=%d", recursive), func(t *testing.T) {
			dsn := "file:" + filepath.Join(t.TempDir(), "retention.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
			open := func() *sql.DB {
				db, err := sql.Open("sqlite", dsn)
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				t.Cleanup(func() { db.Close() })
				mustExecOK(t, db, fmt.Sprintf(`PRAGMA recursive_triggers=%d`, recursive))
				return db
			}
			db := open()
			admissionRelationalProbeSetup(t, db, "RESTRICT", "NOT DEFERRABLE INITIALLY IMMEDIATE")
			var journal string
			if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
				t.Fatalf("WAL=%s err=%v", journal, err)
			}
			reader := open()
			rtx, err := reader.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer rtx.Rollback()
			var initial int
			if err := rtx.QueryRow(`SELECT COUNT(*) FROM bint_occurrences WHERE workspace_id='w1'`).Scan(&initial); err != nil {
				t.Fatal(err)
			}
			mustExecOK(t, db, `INSERT INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,payload) VALUES(800,'retired','w1','free',x'00')`)
			mustExecOK(t, db, `DELETE FROM bint_occurrences WHERE workspace_id='w1' AND occurrence_key='retired'`)
			mustExecOK(t, db, `INSERT INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,payload) VALUES(800,'fresh-key','w1','free',x'00')`)
			var still int
			if err := rtx.QueryRow(`SELECT COUNT(*) FROM bint_occurrences WHERE workspace_id='w1'`).Scan(&still); err != nil || still != initial {
				t.Fatalf("WAL reader changed: %d -> %d err=%v", initial, still, err)
			}
			admissionRelationalProbeReject(t, db, `INSERT OR REPLACE INTO bint_occurrences(rowid,occurrence_key,workspace_id,account_id,payload) VALUES(201,'new-key','w2','free',x'FF')`)
			if err := rtx.Rollback(); err != nil {
				t.Fatal(err)
			}
			reader.Close()
			before := admissionRelationalProbeSnapshot(t, db, false)
			mustExecOK(t, db, `VACUUM`)
			if !reflect.DeepEqual(before, admissionRelationalProbeSnapshot(t, db, false)) {
				t.Fatal("VACUUM changed named keys, multiplicities or references")
			}
			db.Close()
			db = open()
			if countQueryRows(t, db, `PRAGMA foreign_keys`) != 1 || countQueryRows(t, db, `PRAGMA defer_foreign_keys`) != 0 {
				t.Fatal("reopen lost contractual FK pragmas")
			}
			if !reflect.DeepEqual(before, admissionRelationalProbeSnapshot(t, db, false)) {
				t.Fatal("reopen changed durable key/pins")
			}
			admissionRelationalProbeReject(t, db, `INSERT OR REPLACE INTO bint_occurrences(occurrence_key,workspace_id,account_id,payload) VALUES('occ-1','w2','free',x'FF')`)
			if countQueryRows(t, db, `SELECT COUNT(*) FROM pragma_foreign_key_check`) != 0 {
				t.Fatal("FK violation after reopen")
			}
			t.Log("named occurrence keys and dual-scope pins survive raw-rowid reuse, VACUUM, WAL and reopen; key generation/backfill not implemented")
		})
	}
}

func TestMigration78RelationalProbeCurrentSourceKey(t *testing.T) {
	t.Run("raw-rowid-is-not-an-FK-parent", func(t *testing.T) {
		db := admissionCandidateDB(t)
		mustExecOK(t, db, `PRAGMA foreign_keys=ON`)
		mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
		mustExecOK(t, db, `CREATE TABLE bint_raw_rowid_pin(source_rowid INTEGER NOT NULL REFERENCES transactions(rowid) ON DELETE RESTRICT)`)
		_, err := db.Exec(`INSERT INTO bint_raw_rowid_pin VALUES(201)`)
		if err == nil || !strings.Contains(err.Error(), "foreign key mismatch") {
			t.Fatalf("expected invalid FK parent rowid, got %v", err)
		}
		t.Logf("current raw-rowid FK unavailable: %v", err)
	})
	t.Run("nullable-id-does-not-retain-NULL-occurrence", func(t *testing.T) {
		db := admissionCandidateDB(t)
		mustExecOK(t, db, `PRAGMA foreign_keys=ON`)
		mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
		mustExecOK(t, db, `CREATE TABLE bint_nullable_pin(workspace_id TEXT NOT NULL,source_id TEXT,FOREIGN KEY(workspace_id,source_id) REFERENCES transactions(workspace_id,id) ON DELETE RESTRICT)`)
		mustExecOK(t, db, `INSERT INTO bint_nullable_pin VALUES('ws-1',NULL)`)
		mustExecOK(t, db, `DELETE FROM transactions WHERE workspace_id='ws-1' AND rowid=201`)
		if countQueryRows(t, db, `SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-1' AND rowid=201`) != 0 || countQueryRows(t, db, `SELECT COUNT(*) FROM bint_nullable_pin WHERE workspace_id='ws-1'`) != 1 {
			t.Fatal("nullable-FK control not exercised")
		}
		t.Log("current NULL id: FK does not bind the NULL source occurrence; new named non-NULL source identity required")
	})
}
