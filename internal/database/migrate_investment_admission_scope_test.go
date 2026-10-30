package database

import (
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type admissionTestScope struct{ workspace, account string }

func admissionAdditionalBasis(t *testing.T, db *sql.DB, workspace, account, investment, position, user string, newAccount bool) []any {
	t.Helper()
	if newAccount {
		mustExecOK(t, db, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance) VALUES(?,?,'Additional','INVESTMENT',0,0)`, account, workspace)
	}
	replace := strings.NewReplacer("ws-1", workspace, "acc-inv", account, "inv-1", investment, "reference-position", position, "user-1", user)
	mustExecOK(t, db, replace.Replace(insertValidInvestment))
	mustExecOK(t, db, `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by) VALUES(?,?,?,'2026-02-01',110000,?)`, position, workspace, investment, user)
	ref := strings.Replace(insertReferenceFixture, `'{}','aaaaaaaa`, `'{"Position":{"amount_cents":110000}}','aaaaaaaa`, 1)
	mustExecOK(t, db, replace.Replace(ref), workspace, investment, account, position, "2026-02-01", 110000)
	mustExecOK(t, db, replace.Replace(adoptionHeaderFixture), workspace, account, "2026-02-01", replace.Replace(adoptionSchemaJSON(t, adoptionSchemaReceipt(t))))
	args := admissionArgsMut(t, func(r map[string]any) {
		r["ID"] = "admission-" + account
		r["WorkspaceID"] = workspace
		r["AccountID"] = account
		r["CreatedBy"] = user
		c := r["Confirmation"].(map[string]any)
		c["WorkspaceID"] = workspace
		c["AccountID"] = account
		c["CreatedBy"] = user
	})
	args[0] = workspace
	args[1] = account
	args[2] = "admission-" + account
	args[7] = user
	return args
}

func admissionAssertScopedWitnesses(t *testing.T, db *sql.DB, scopes []admissionTestScope, occurrences int64) {
	t.Helper()
	for _, s := range scopes {
		gc, oc, env, ok := goExpectedWitness(t, db, s.workspace, s.account, "2026-02-01")
		var gotGC, gotOC int64
		var blob []byte
		if err := db.QueryRow(`SELECT group_count,occurrence_count,canonical_multiset FROM investment_admission_null_transaction_witness WHERE workspace_id=? AND account_id=?`, s.workspace, s.account).Scan(&gotGC, &gotOC, &blob); err != nil {
			t.Fatal(err)
		}
		if !ok || gc != 1 || oc != occurrences || gotGC != gc || gotOC != oc || string(blob) != env {
			t.Fatalf("scope %+v: oracle=(%d,%d,%v) witness=(%d,%d) exact=%v", s, gc, oc, ok, gotGC, gotOC, string(blob) == env)
		}
	}
}

// Include physical identities and all 37 financial columns, not only counts.
func admissionScopeSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	snapshot := admissionFinancialSnapshot(t, db)
	for _, workspace := range []string{"ws-1", "ws-2"} {
		queries := []string{
			`SELECT json_array(rowid,` + strings.Join(admissionCanonicalColumns, ",") + `) FROM transactions WHERE workspace_id=? ORDER BY rowid`,
			`SELECT json_array(account_id,group_count,occurrence_count,hex(canonical_multiset)) FROM investment_admission_null_transaction_witness WHERE workspace_id=? ORDER BY account_id`,
		}
		for _, q := range queries {
			rows, err := db.Query(q, workspace)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var value string
				if err := rows.Scan(&value); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				snapshot = append(snapshot, value)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return snapshot
}

func TestMigration78ScopeIsolation(t *testing.T) {
	for _, recursive := range []int{0, 1} {
		for _, duplicates := range []int{2, 5} {
			t.Run(fmt.Sprintf("recursive=%d/duplicates=%d", recursive, duplicates), func(t *testing.T) {
				db := admissionCandidateDB(t)
				mustExecOK(t, db, fmt.Sprintf(`PRAGMA recursive_triggers=%d`, recursive))
				second := admissionAdditionalBasis(t, db, "ws-1", "acc-extra", "inv-extra", "position-extra", "user-1", true)
				third := admissionAdditionalBasis(t, db, "ws-2", "acc-inv2", "inv-foreign", "position-foreign", "user-2", false)
				mustExecOK(t, db, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('acc-free','ws-2','Free','CHECKING')`)
				for i := 0; i < duplicates; i++ {
					mustExecOK(t, db, admissionTxNULLFixture, 201+i, "ws-1", "user-1", "acc-inv", "acc-extra", "TRANSFER", "2026-01-05", "2026-01-05")
					mustExecOK(t, db, admissionTxNULLFixture, 301+i, "ws-2", "user-2", "acc-inv2", nil, "EXPENSE", "2026-01-05", "2026-01-05")
				}
				admitDefault(t, db)
				mustExecOK(t, db, admissionInsertStmt(), second...)
				mustExecOK(t, db, admissionInsertStmt(), third...)
				scopes := []admissionTestScope{{"ws-1", "acc-inv"}, {"ws-1", "acc-extra"}, {"ws-2", "acc-inv2"}}
				check := func() { admissionAssertScopedWitnesses(t, db, scopes, int64(duplicates)) }
				check()
				const freeRow = `INSERT INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES(901,'allowed','ws-2','user-2','acc-free','EXPENSE',1,'2026-03-06','2026-03-06','Allowed','paid')`
				mustExecOK(t, db, freeRow)
				mustExecOK(t, db, `UPDATE transactions SET description='Allowed update' WHERE workspace_id='ws-2' AND id='allowed'`)
				mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES('allowed-delete','ws-1','user-1','acc-checking','EXPENSE',1,'2026-03-06','2026-03-06','Allowed delete','paid')`)
				mustExecOK(t, db, `DELETE FROM transactions WHERE workspace_id='ws-1' AND id='allowed-delete'`)
				check()
				reject := func(label, q string, args ...any) {
					t.Helper()
					before := admissionScopeSnapshot(t, db)
					_, err := db.Exec(q, args...)
					if err == nil {
						t.Fatalf("%s: protected occurrence lost without ABORT", label)
					}
					expected := "FOREIGN KEY"
					if recursive == 1 {
						expected = "admitted transaction is sealed"
					}
					if !strings.Contains(err.Error(), expected) {
						t.Fatalf("%s unexpected rejection: %v", label, err)
					}
					if after := admissionScopeSnapshot(t, db); !reflect.DeepEqual(before, after) {
						t.Fatalf("%s changed snapshot after ABORT", label)
					}
					check()
				}
				replaceSQL := `INSERT OR REPLACE INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES(?,'attack',?,?,?,'EXPENSE',1,'2026-03-06','2026-03-06','Replace','paid')`
				reject("cross-workspace", replaceSQL, 201, "ws-2", "user-2", "acc-free")
				reject("other account in workspace", replaceSQL, 201, "ws-1", "user-1", "acc-checking")
				reject("other protected workspace", replaceSQL, 301, "ws-1", "user-1", "acc-checking")
				reject("update physical collision", `UPDATE OR REPLACE transactions SET rowid=201 WHERE workspace_id='ws-2' AND id='allowed'`)
				reject("multirow cannot compensate across workspaces", `INSERT OR REPLACE INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES
    (910,NULL,'ws-2','user-2','acc-free','EXPENSE',1000,'2026-01-05','2026-01-05','Witness fixture','paid'),
    (201,'attack','ws-2','user-2','acc-free','EXPENSE',1,'2026-03-06','2026-03-06','Replace','paid')`)
				reject("multirow cannot compensate across accounts", `INSERT OR REPLACE INTO transactions(rowid,id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES
    (911,NULL,'ws-1','user-1','acc-checking','TRANSFER',1000,'2026-01-05','2026-01-05','Witness fixture','paid'),
    (201,'attack','ws-1','user-1','acc-checking','EXPENSE',1,'2026-03-06','2026-03-06','Replace','paid')`)
				before := admissionScopeSnapshot(t, db)
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err := tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES('prior','ws-2','user-2','acc-free','EXPENSE',1,'2026-03-06','2026-03-06','Prior','paid')`); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(replaceSQL, 201, "ws-2", "user-2", "acc-free"); err == nil {
					t.Fatal("transaction accepted protected deletion")
				}
				var prior int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-2' AND id='prior'`).Scan(&prior); err != nil || prior != 1 {
					t.Fatalf("ABORT lost prior statement: count=%d err=%v", prior, err)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, admissionScopeSnapshot(t, db)) {
					t.Fatal("caller rollback changed snapshot")
				}
				check()
			})
		}
	}
}

func TestMigration78ScopePerformanceSmoke(t *testing.T) {
	admissionPerfEnabled(t, "CB_ADMISSION_BINT_PERF")
	db := admissionCandidateDB(t)
	admissionNULLBatch(t, db, 20000, "bperf")
	admitDefault(t, db)
	stmt, err := db.Prepare(`INSERT INTO transactions(workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES('ws-1','user-1','acc-inv','TRANSFER',10,'2026-03-06','2026-03-06','measure','paid')`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	var samples []time.Duration
	for i := -1; i < 5; i++ {
		start := time.Now()
		if _, err := stmt.Exec(); err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			samples = append(samples, time.Since(start))
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("B-INT prepared N_s=20000 samples=5 min=%v median=%v max=%v (diagnostic only; P2-P6 remain FAIL)", samples[0], samples[2], samples[4])
}

func TestMigration78ScopeSelfTransfer(t *testing.T) {
	db := admissionCandidateDB(t)
	mustExecOK(t, db, admissionTxNULLFixture, 201, "ws-1", "user-1", "acc-inv", "acc-inv", "TRANSFER", "2026-01-05", "2026-01-05")
	admitDefault(t, db)
	admissionAssertScopedWitnesses(t, db, []admissionTestScope{{"ws-1", "acc-inv"}}, 1)
	mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,status) VALUES('outside','ws-2','user-2','acc-inv2','EXPENSE',1,'2026-03-06','2026-03-06','Allowed','paid')`)
	admissionAssertScopedWitnesses(t, db, []admissionTestScope{{"ws-1", "acc-inv"}}, 1)
}
