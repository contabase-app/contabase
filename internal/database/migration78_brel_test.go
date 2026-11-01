//go:build contabase_legacy_fixtures

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func brelLegacy(t *testing.T) *sql.DB {
	t.Helper()
	db := referenceLegacy71(t)
	if err := runMigrationsThroughVersionForTest(db, 77); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{math.MinInt64, -1, 0, 99, math.MaxInt64} {
		mustExecOK(t, db, admissionTxNULLFixture, id, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	}
	mustExecOK(t, db, `UPDATE transactions SET notes=x'0001ff',description='duplicata' WHERE workspace_id='ws-1'`)
	mustExecOK(t, db, `CREATE VIEW brel_dependency AS SELECT id,description FROM transactions WHERE workspace_id='ws-1'`)
	mustExecOK(t, db, `CREATE INDEX brel_payload ON transactions(workspace_id,description)`)
	return db
}

// An independent snapshot of every original table, including typed values,
// dependent receipts and balances. It excludes only the added technical key.
func brelData(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	r, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE 'investment_admission_%' AND name NOT IN ('investment_adoption_admissions','schema_migrations') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for r.Next() {
		var s string
		if err = r.Scan(&s); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, s)
	}
	if err = errors.Join(r.Err(), r.Close()); err != nil {
		t.Fatal(err)
	}
	out := map[string][][]any{}
	for _, table := range tables {
		rows, err := db.Query("SELECT * FROM " + quoteAdmissionIdentifier(table))
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			v := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range v {
				dest[i] = &v[i]
			}
			if err = rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			var filtered []any
			for i, c := range cols {
				if c != "admission_occurrence_key" {
					if b, ok := v[i].([]byte); ok {
						v[i] = append([]byte{}, b...)
					}
					filtered = append(filtered, v[i])
				}
			}
			out[table] = append(out[table], filtered)
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestBRelMigrationExactAndRollback(t *testing.T) {
	db := brelLegacy(t)
	before := brelData(t, db)
	canonical := func() []string {
		r, err := db.Query(`SELECT ` + admissionCanonicalRow("t") + ` FROM transactions t WHERE t.workspace_id='ws-1' ORDER BY t.rowid`)
		if err != nil {
			t.Fatal(err)
		}
		var v []string
		for r.Next() {
			var s string
			if err = r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			v = append(v, s)
		}
		if err = errors.Join(r.Err(), r.Close()); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v1 := canonical()
	_, _, multisetBefore, ok := goExpectedWitness(t, db, "ws-1", "acc-inv", "2026-02-01")
	if !ok {
		t.Fatal("unexpected non-oracle fixture")
	}
	for _, stage := range []string{"create", "copy", "swap", "dependencies", "candidate", "marker"} {
		t.Run(stage, func(t *testing.T) {
			var oldDDL string
			if err := db.QueryRow(`SELECT group_concat(sql,'|') FROM sqlite_schema ORDER BY rowid`).Scan(&oldDDL); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected maintenance failure")
			err := runExperimentalAdmission78(t.Context(), db, "", func(s string, tx *sql.Tx) error {
				if s == stage {
					return injected
				}
				return nil
			})
			if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			var ddl string
			if err := db.QueryRow(`SELECT group_concat(sql,'|') FROM sqlite_schema ORDER BY rowid`).Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if ddl != oldDDL || !reflect.DeepEqual(before, brelData(t, db)) {
				t.Fatal("rollback changed historical schema/data")
			}
			if countQueryRows(t, db, `PRAGMA foreign_keys`) != 1 || countQueryRows(t, db, `PRAGMA defer_foreign_keys`) != 0 {
				t.Fatal("maintenance connection contaminated")
			}
		})
	}
	applyAdmissionCandidateForTest(t, db)
	if !reflect.DeepEqual(v1, canonical()) {
		t.Fatal("CBTX v1 bytes changed")
	}
	_, _, multisetAfter, ok := goExpectedWitness(t, db, "ws-1", "acc-inv", "2026-02-01")
	if !ok || multisetBefore != multisetAfter {
		t.Fatal("CBMS v1 bytes changed")
	}
	if !reflect.DeepEqual(before, brelData(t, db)) {
		t.Fatal("upgrade changed original typed values")
	}
	for _, id := range []int64{math.MinInt64, -1, 0, 99, math.MaxInt64} {
		if countQueryRows(t, db, `SELECT count(*) FROM transactions WHERE workspace_id='ws-1' AND rowid=? AND id IS NULL`, id) != 1 {
			t.Fatal("lost rowid/NULL")
		}
	}
	if countQueryRows(t, db, `SELECT count(DISTINCT admission_occurrence_key) FROM transactions WHERE workspace_id='ws-1' AND rowid IN (-9223372036854775808,-1,0,99,9223372036854775807)`) != 5 {
		t.Fatal("duplicate content lost occurrence identity")
	}
	if countQueryRows(t, db, `SELECT count(*) FROM investment_admission_pin_null_transactions`) != 0 {
		t.Fatal("upgrade captured admission")
	}
	// A materialized 78 is never silently rebuilt, even on a disposable DB.
	if err := RunExperimentalInvestmentAdmission78(t.Context(), db, ""); err == nil {
		t.Fatal("78 rebuilt twice")
	}
}

func TestBRelBackfillCollisionRollsBack(t *testing.T) {
	db := brelLegacy(t)
	before := brelData(t, db)
	err := runExperimentalAdmission78(t.Context(), db, "", func(stage string, tx *sql.Tx) error {
		if stage != "create" {
			return nil
		}
		var ddl string
		if err := tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='transactions_admission78_new'`).Scan(&ddl); err != nil {
			return err
		}
		ddl = strings.Replace(ddl, "lower(hex(randomblob(16)))", "'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'", 1)
		if _, err := tx.Exec(`DROP TABLE transactions_admission78_new`); err != nil {
			return err
		}
		_, err := tx.Exec(ddl)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("backfill collision not rejected: %v", err)
	}
	if !reflect.DeepEqual(before, brelData(t, db)) || countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`) != 77 {
		t.Fatal("collision left partial upgrade")
	}
}

func TestBRelCaptureCompletenessAndReopen(t *testing.T) {
	if len(admission78ConsistencyQueries()) != 9 {
		t.Fatalf("missing consistency projection: %d", len(admission78ConsistencyQueries()))
	}
	for _, fault := range []string{"omit", "extra", "reopen"} {
		t.Run(fault, func(t *testing.T) {
			db := admissionCandidateDB(t)
			for _, id := range []int{201, 202} {
				mustExecOK(t, db, admissionTxNULLFixture, id, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
			}
			if fault == "reopen" {
				admitDefault(t, db)
				var guard string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='trg_admission_pin_null_transactions_delete'`).Scan(&guard); err != nil {
					t.Fatal(err)
				}
				mustExecOK(t, db, `DROP TRIGGER trg_admission_pin_null_transactions_delete`)
				mustExecOK(t, db, `DELETE FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1' AND occurrence_key=(SELECT admission_occurrence_key FROM transactions WHERE workspace_id='ws-1' AND rowid=201)`)
				mustExecOK(t, db, guard)
				conn, err := db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err := validateAdmission78Reopen(t.Context(), conn); err == nil {
					t.Fatal("reopen published incomplete retention")
				}
				return
			}
			var capture string
			if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='trg_admission_capture'`).Scan(&capture); err != nil {
				t.Fatal(err)
			}
			needle := `AND t.id IS NULL AND ` + admissionFinancial("t", "a") + `;`
			if fault == "omit" {
				capture = strings.Replace(capture, needle, `AND t.id IS NULL AND t.rowid!=201 AND `+admissionFinancial("t", "a")+`;`, 1)
			} else {
				// Authenticated membership must reject a source outside W/A even
				// before the closing two-directional set comparison runs.
				mustExecOK(t, db, `INSERT INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date) VALUES(999,'ws-1','user-1','acc-checking','EXPENSE',1,1)`)
				capture = strings.Replace(capture, needle, `AND t.id IS NULL;`, 1)
			}
			mustExecOK(t, db, `DROP TRIGGER trg_admission_capture`)
			mustExecOK(t, db, capture)
			if _, err := db.Exec(admissionInsertStmt(), admissionArgsMut(t, nil)...); err == nil {
				t.Fatal("incomplete/extra pin capture accepted")
			}
			for _, table := range []string{"investment_adoption_admissions", "investment_admission_pin_null_transactions", "investment_admission_null_scopes", "investment_admission_null_transaction_witness"} {
				if countQueryRows(t, db, "SELECT count(*) FROM "+table) != 0 {
					t.Fatal("partial evidence survived capture abort")
				}
			}
		})
	}
}

func TestBRelIdentityAndRetentionWithoutVerifiers(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		t.Run(fmt.Sprint(recursive), func(t *testing.T) {
			db := admissionCandidateDB(t)
			mustExecOK(t, db, fmt.Sprintf(`PRAGMA recursive_triggers=%d`, recursive))
			// This first proof deliberately removes only the old AFTER verifiers.
			for _, a := range []string{"insert", "update", "delete"} {
				mustExecOK(t, db, `DROP TRIGGER IF EXISTS trg_admission_null_verify_`+a)
			}
			mustExecOK(t, db, admissionTxNULLFixture, -1, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
			mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
			var k string
			if err := db.QueryRow(`SELECT admission_occurrence_key FROM transactions WHERE workspace_id='ws-1' AND rowid=-1`).Scan(&k); err != nil {
				t.Fatal(err)
			}
			// Equal K in another W is legal; collisions within W abort even IGNORE.
			q := `INSERT INTO transactions(admission_occurrence_key,workspace_id,user_id,account_id,type,amount,date) VALUES(?,?,?,?, 'EXPENSE',1,1)`
			mustExecOK(t, db, q, k, "ws-2", "user-2", "acc-inv2")
			for _, conflict := range []string{"", " OR IGNORE", " OR REPLACE"} {
				for _, key := range []any{k, k + "\x00extra", "INVALID", nil, strings.Repeat("a", 31), strings.Repeat("F", 32)} {
					if _, err := db.Exec(strings.Replace(q, "INSERT", "INSERT"+conflict, 1), key, "ws-1", "user-1", "acc-checking"); err == nil || !strings.Contains(err.Error(), "investment admission integrity") {
						t.Fatalf("identity guard %v", err)
					}
				}
			}
			admitDefault(t, db)
			if countQueryRows(t, db, `SELECT count(*) FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1' AND account_id='acc-inv'`) != 2 {
				t.Fatal("capture omitted duplicate")
			}
			before := brelData(t, db)
			_, _, witness := witnessRow(t, db)
			for _, alias := range []string{"rowid", "oid", "_rowid_"} {
				_, err := db.Exec(`INSERT OR REPLACE INTO transactions(` + alias + `,workspace_id,user_id,account_id,type,amount,date) VALUES(-1,'ws-2','user-2','acc-inv2','EXPENSE',1,1)`)
				if err == nil || (!strings.Contains(err.Error(), "FOREIGN KEY") && !strings.Contains(err.Error(), "admitted transaction is sealed")) {
					t.Fatalf("retention %s: %v", alias, err)
				}
			}
			attacks := []string{
				`DELETE FROM transactions WHERE workspace_id='ws-1' AND rowid=201`,
				`UPDATE transactions SET amount=2 WHERE workspace_id='ws-1' AND rowid=201`,
				`UPDATE transactions SET admission_occurrence_key=lower(hex(randomblob(16))) WHERE workspace_id='ws-1' AND rowid=201`,
				`UPDATE OR REPLACE transactions SET workspace_id='ws-1' WHERE workspace_id='ws-2'`,
				`DELETE FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1'`,
				`UPDATE investment_admission_pin_null_transactions SET occurrence_key='00000000000000000000000000000000' WHERE workspace_id='ws-1'`,
				`INSERT OR REPLACE INTO investment_admission_pin_null_transactions SELECT * FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1'`,
				`INSERT INTO investment_admission_pin_null_transactions SELECT * FROM investment_admission_pin_null_transactions WHERE workspace_id='ws-1' ON CONFLICT DO UPDATE SET occurrence_key=excluded.occurrence_key`,
				`INSERT OR REPLACE INTO transactions(rowid,workspace_id,user_id,account_id,type,amount,date) VALUES(900,'ws-2','user-2','acc-inv2','EXPENSE',1,1),(-1,'ws-2','user-2','acc-inv2','EXPENSE',1,1)`,
			}
			for _, q := range attacks {
				if _, err := db.Exec(q); err == nil {
					t.Fatalf("attack accepted: %s", q)
				}
				if !reflect.DeepEqual(before, brelData(t, db)) {
					t.Fatal("attack changed original data")
				}
				_, _, after := witnessRow(t, db)
				if string(after) != string(witness) {
					t.Fatal("witness changed")
				}
			}
			// The historical -1 witness must not reject a legitimate implicit rowid.
			mustExecOK(t, db, `INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil) VALUES('ws-1','user-1','acc-inv','TRANSFER',1,1,'2026-03-05')`)
		})
	}
}

func TestBRelMigrationRejectsOrphan(t *testing.T) {
	db := brelLegacy(t)
	mustExecOK(t, db, `PRAGMA foreign_keys=OFF; UPDATE transactions SET workspace_id='orphan' WHERE rowid=-1; PRAGMA foreign_keys=ON`)
	before := brelData(t, db)
	if err := RunExperimentalInvestmentAdmission78(context.Background(), db, ""); err == nil {
		t.Fatal("orphan source accepted")
	}
	if !reflect.DeepEqual(before, brelData(t, db)) {
		t.Fatal("failed preflight changed data")
	}
}

func TestBRelVacuumPreservesPersistedIdentity(t *testing.T) {
	db := admissionCandidateDB(t)
	for _, rowid := range []int{-1, 201} {
		mustExecOK(t, db, admissionTxNULLFixture, rowid, "ws-1", "user-1", "acc-inv", nil, "EXPENSE", "2026-01-05", "2026-01-05")
	}
	admitDefault(t, db)
	_, _, before := witnessRow(t, db)
	keys := func() string {
		var v string
		if err := db.QueryRow(`SELECT group_concat(admission_occurrence_key,'|' ORDER BY admission_occurrence_key) FROM transactions WHERE workspace_id='ws-1'`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	k := keys()
	mustExecOK(t, db, `VACUUM`)
	_, _, after := witnessRow(t, db)
	if k != keys() || string(before) != string(after) {
		t.Fatal("VACUUM regenerated keys or witness")
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = validateAdmission78Reopen(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
}

func TestBRelUpgradeRefusesPartialAndInvalidCandidate(t *testing.T) {
	for _, fault := range []string{"partial", "precommit"} {
		t.Run(fault, func(t *testing.T) {
			db := brelLegacy(t)
			if fault == "partial" {
				mustExecOK(t, db, `CREATE TABLE investment_admission_unknown(x TEXT)`)
			}
			before := brelData(t, db)
			err := runExperimentalAdmission78(t.Context(), db, "", func(stage string, tx *sql.Tx) error {
				if fault == "precommit" && stage == "marker" {
					_, err := tx.Exec(`DROP TRIGGER trg_admission_occurrence_insert`)
					return err
				}
				return nil
			})
			if err == nil {
				t.Fatal("partial/malformed upgrade committed")
			}
			if countQueryRows(t, db, `SELECT MAX(version) FROM schema_migrations`) != 77 || !reflect.DeepEqual(before, brelData(t, db)) {
				t.Fatal("invalid upgrade changed historical state")
			}
		})
	}
}
