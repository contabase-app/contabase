package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

// Extract both real INSERTs and their bind order, including the existing
// cardinality helper. The direct probes deliberately bypass handler guards.
func tenantMetasInsert(t *testing.T, function, amountBinding string) string {
	t.Helper()
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "metas.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var matches []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "execOneTx" {
				return true
			}
			literal, ok := call.Args[1].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			query, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(query, "INSERT INTO box_virtual_ledger") {
				return true
			}
			var bindings []string
			for _, arg := range call.Args[2:] {
				var b bytes.Buffer
				if err := printer.Fprint(&b, fs, arg); err != nil {
					t.Fatal(err)
				}
				bindings = append(bindings, b.String())
			}
			want := []string{"uuid.NewString()", "now", amountBinding, "note", "now", "boxID", "h.WorkspaceID"}
			if !reflect.DeepEqual(bindings, want) || strings.Count(query, "?") != len(want) {
				t.Fatalf("%s bindings=%v, want=%v", function, bindings, want)
			}
			matches = append(matches, query)
			return true
		})
	}
	if len(matches) != 1 {
		t.Fatalf("%s matched %d INSERTs", function, len(matches))
	}
	return matches[0]
}

func tenantMetasHistory(t *testing.T) string {
	return tenantScopeStatement(t, "metas.go", "HandleHistoricoCaixinha", "FROM box_virtual_ledger l", "h.WorkspaceID", "boxID", "h.WorkspaceID")
}

func TestTenantMetasLedgerN5N6DirectOwnership(t *testing.T) {
	for _, operation := range []struct {
		site, function, binding, kind string
		amount                        int64
	}{
		{"N5", "HandleAporteCaixinha", "amount", "RECHARGE", 1234},
		{"N6", "HandleResgateCaixinha", "-amount", "RELEASE", -1234},
	} {
		t.Run(operation.site, func(t *testing.T) {
			db := seedTenantRuntimeScope(t)
			query := tenantMetasInsert(t, operation.function, operation.binding)
			for _, tc := range []struct {
				name, box, workspace string
				affected             int64
			}{
				{"foreign", "box-b", "scope-a", 0},
				{"missing-box", "missing", "scope-a", 0},
				{"missing-workspace", "box-a", "missing", 0},
				{"own-a", "box-a", "scope-a", 1},
				{"own-b", "box-b", "scope-b", 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := tenantLedgerSnapshot(t, db)
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					id, date, note := operation.site+tc.name, int64(1720000000), "  payload exato ç  "
					args := []any{id, date, operation.amount, note, date, tc.box, tc.workspace}
					result, err := tx.Exec(query, args...)
					if err != nil {
						t.Fatal(err)
					}
					affected, err := result.RowsAffected()
					if err != nil || affected != tc.affected {
						t.Fatalf("RowsAffected=%d err=%v, want=%d", affected, err, tc.affected)
					}
					if affected == 1 {
						var box, kind, desc string
						var amount, ref, created int64
						var source, reversal sql.NullString
						if err := tx.QueryRow(`SELECT box_id, amount, type, description, reference_date, created_at, source_transaction_id, reversal_of_ledger_id FROM box_virtual_ledger WHERE id = ?`, id).Scan(&box, &amount, &kind, &desc, &ref, &created, &source, &reversal); err != nil {
							t.Fatal(err)
						}
						if box != tc.box || amount != operation.amount || kind != operation.kind || desc != note || ref != date || created != date || source.Valid || reversal.Valid {
							t.Fatalf("payload changed: %s %d %s %q %d %d %v %v", box, amount, kind, desc, ref, created, source, reversal)
						}
					} else if err := execOneTx(tx, query, args...); err == nil {
						t.Fatal("zero-row INSERT accepted by cardinality helper")
					}
					if err := tx.Rollback(); err != nil {
						t.Fatal(err)
					}
					if after := tenantLedgerSnapshot(t, db); !reflect.DeepEqual(before, after) {
						t.Fatal("direct probe changed persisted financial snapshot")
					}
					tenantScopeIndexedPlan(t, db, query, args...)
				})
			}
			// Exact former VALUES shape: proves a foreign ID would insert without
			// ownership in this statement, independently of the earlier Go guard.
			legacy := `INSERT INTO box_virtual_ledger (id, box_id, reference_date, amount, type, description, created_at) VALUES (?, ?, ?, ?, '` + operation.kind + `', ?, ?)`
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			result, err := tx.Exec(legacy, "legacy", "box-b", 1720000000, operation.amount, "legacy", 1720000000)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("negative control rows=%d err=%v", n, err)
			}
		})
	}
}

func TestTenantMetasLedgerN5N6RollbackAfterPriorMutations(t *testing.T) {
	for _, tc := range []struct {
		function, binding string
		amount            int64
	}{
		{"HandleAporteCaixinha", "amount", 1234}, {"HandleResgateCaixinha", "-amount", -1234},
	} {
		t.Run(tc.function, func(t *testing.T) {
			db := seedTenantRuntimeScope(t)
			query := tenantMetasInsert(t, tc.function, tc.binding)
			before := tenantLedgerSnapshot(t, db)
			h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "scope-a"}
			err := h.execMetaMutation(func(tx *sql.Tx) error {
				// Simulate earlier writes followed by loss of box ownership in
				// the same transaction; all four financial tables must roll back.
				for _, query := range []string{
					`UPDATE boxes SET workspace_id = 'scope-b', name = 'pending' WHERE id = 'box-a'`,
					`UPDATE accounts SET current_balance = 42 WHERE id = 'bank-a'`,
					`UPDATE transactions SET description = 'pending' WHERE id = 'tx-a'`,
					`UPDATE box_virtual_ledger SET description = 'pending' WHERE id = 'ledger-a'`,
				} {
					if err := execOneTx(tx, query); err != nil {
						return err
					}
				}
				return execOneTx(tx, query, "blocked", 1720000000, tc.amount, "blocked", 1720000000, "box-a", h.WorkspaceID)
			})
			if err == nil || !strings.Contains(err.Error(), "não autorizada") {
				t.Fatalf("ownership failure not propagated: %v", err)
			}
			if after := tenantLedgerSnapshot(t, db); !reflect.DeepEqual(before, after) {
				t.Fatal("partial mutation survived rollback")
			}
		})
	}
}

func TestTenantMetasLedgerN5N6Handlers(t *testing.T) {
	for _, tc := range []struct {
		name, message, kind string
		delta               int64
		handle              func(*MetasHandler, http.ResponseWriter, *http.Request)
	}{
		{"aporte", "erro ao registrar aporte", "RECHARGE", 1234, (*MetasHandler).HandleAporteCaixinha},
		{"resgate", "erro ao registrar liberação", "RELEASE", -1234, (*MetasHandler).HandleResgateCaixinha},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedCaixinhaResgateScenario(t, db)
			h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, Templates: testMetasMutationTemplates(t), WorkspaceID: "ws-a", UserID: "user-a"}
			before := tenantLedgerSnapshot(t, db)
			for _, target := range []string{"b-box", "missing"} {
				rr := httptest.NewRecorder()
				tc.handle(&h, rr, newFormRequest(t, http.MethodPost, "/metas/caixinha/"+tc.name, url.Values{"box_id": {target}, "amount": {"12,34"}}))
				if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), tc.message) || strings.Contains(rr.Body.String(), "metas-tabs") {
					t.Fatalf("failure response=%d %s", rr.Code, rr.Body.String())
				}
				if after := tenantLedgerSnapshot(t, db); !reflect.DeepEqual(before, after) {
					t.Fatal("foreign/missing request changed financial snapshot")
				}
			}
			balanceBefore, err := services.CalculateWorkspaceReserveBalance(db, "ws-a")
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now().Unix()
			rr := httptest.NewRecorder()
			tc.handle(&h, rr, newFormRequest(t, http.MethodPost, "/metas/caixinha/"+tc.name, url.Values{"box_id": {"a-box"}, "amount": {"12,34"}, "note": {"  nota preservada ç  "}}))
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "metas-tabs") {
				t.Fatalf("valid response=%d %s", rr.Code, rr.Body.String())
			}
			balanceAfter, err := services.CalculateWorkspaceReserveBalance(db, "ws-a")
			if err != nil {
				t.Fatal(err)
			}
			if balanceAfter.RealBalance != balanceBefore.RealBalance || balanceAfter.ReservedBalance != balanceBefore.ReservedBalance+tc.delta || balanceAfter.FreeBalance != balanceBefore.FreeBalance-tc.delta {
				t.Fatalf("balance semantics changed: before=%+v after=%+v", balanceBefore, balanceAfter)
			}
			var amount, ref, created int64
			var note string
			var source, reversal sql.NullString
			if err := db.QueryRow(`SELECT amount, description, reference_date, created_at, source_transaction_id, reversal_of_ledger_id FROM box_virtual_ledger WHERE box_id = 'a-box' AND type = ? AND id != 'a-ledger-1'`, tc.kind).Scan(&amount, &note, &ref, &created, &source, &reversal); err != nil {
				t.Fatal(err)
			}
			if amount != tc.delta || note != "nota preservada ç" || ref < start || ref > time.Now().Unix() || ref != created || source.Valid || reversal.Valid {
				t.Fatalf("handler payload changed: %d %q %d %d %v %v", amount, note, ref, created, source, reversal)
			}
			after := tenantLedgerSnapshot(t, db)
			for _, table := range []string{"boxes", "transactions", "accounts"} {
				if !reflect.DeepEqual(before[table], after[table]) {
					t.Fatalf("unexpected %s mutation", table)
				}
			}
			foreign := tenantScopeRows(t, db, `SELECT * FROM box_virtual_ledger WHERE box_id = 'b-box' ORDER BY id`)
			var expectedForeign [][]any
			for _, row := range before["box_virtual_ledger"] {
				if row[1] == "b-box" {
					expectedForeign = append(expectedForeign, row)
				}
			}
			if !reflect.DeepEqual(foreign, expectedForeign) {
				t.Fatal("valid request changed foreign ledger")
			}
		})
	}
}

func TestTenantMetasLedgerN5N6HandlersRejectZeroRows(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		handle        func(*MetasHandler, http.ResponseWriter, *http.Request)
	}{
		{"aporte", "erro ao registrar aporte", (*MetasHandler).HandleAporteCaixinha},
		{"resgate", "erro ao registrar liberação", (*MetasHandler).HandleResgateCaixinha},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedCaixinhaResgateScenario(t, db)
			// A test-only trigger makes the INSERT return zero after both valid
			// handler guards. It must never render success or notify the user.
			execTestSQL(t, db, `CREATE TRIGGER ignore_metas_insert BEFORE INSERT ON box_virtual_ledger BEGIN SELECT RAISE(IGNORE); END`)
			before := tenantLedgerSnapshot(t, db)
			notifications := tenantScopeRows(t, db, `SELECT * FROM user_notifications ORDER BY id`)
			h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, Templates: testMetasMutationTemplates(t), WorkspaceID: "ws-a", UserID: "user-a"}
			rr := httptest.NewRecorder()
			tc.handle(&h, rr, newFormRequest(t, http.MethodPost, "/metas/caixinha/"+tc.name, url.Values{"box_id": {"a-box"}, "amount": {"12,34"}}))
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), tc.message) || strings.Contains(rr.Body.String(), "metas-tabs") {
				t.Fatalf("zero-row response=%d %s", rr.Code, rr.Body.String())
			}
			if after := tenantLedgerSnapshot(t, db); !reflect.DeepEqual(before, after) {
				t.Fatal("zero-row request changed financial snapshot")
			}
			if after := tenantScopeRows(t, db, `SELECT * FROM user_notifications ORDER BY id`); !reflect.DeepEqual(notifications, after) {
				t.Fatal("zero-row request emitted notification")
			}
		})
	}
}

func TestTenantMetasLedgerN7DirectOwnership(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	query := tenantMetasHistory(t)
	// Foreign box points at an A transaction, and owned box at a B transaction.
	// source_transaction_id has no tenant FK; both inconsistencies are legal.
	tenantLedgerSeedEvent(t, db, "foreign-a-source", "box-b", "CONSUME", "tx-a", "", -333, 1720000000)
	tenantLedgerSeedEvent(t, db, "owned-b-source", "box-a", "CONSUME", "tx-b", "", -444, 1720000000)
	execTestSQL(t, db, `UPDATE box_virtual_ledger SET description = id WHERE id IN ('foreign-a-source', 'owned-b-source')`)
	for _, tc := range []struct {
		workspace, box string
		count          int
	}{
		{"scope-a", "box-a", 2}, {"scope-a", "box-b", 0}, {"scope-a", "missing", 0},
		{"scope-b", "box-b", 2}, {"scope-b", "box-a", 0}, {"missing", "box-a", 0},
	} {
		t.Run(tc.workspace+"/"+tc.box, func(t *testing.T) {
			rows := tenantScopeRows(t, db, query, tc.workspace, tc.box, tc.workspace)
			if len(rows) != tc.count {
				t.Fatalf("history rows=%v, want %d", rows, tc.count)
			}
			for _, row := range rows {
				if row[1] == "CONSUME" && (row[2] != map[string]string{"box-a": "owned-b-source", "box-b": "foreign-a-source"}[tc.box] || row[5] != "" || row[6] != "") {
					t.Fatalf("foreign transaction metadata leaked: %v", row)
				}
			}
			tenantScopeIndexedPlan(t, db, query, tc.workspace, tc.box, tc.workspace)
		})
	}
	// Keep the transaction workspace guard, remove only box ownership.
	unscoped := strings.Replace(query, " AND owner.workspace_id = ?", "", 1)
	if rows := tenantScopeRows(t, db, unscoped, "scope-a", "box-b"); len(rows) != 2 {
		t.Fatalf("negative control did not expose foreign box: %v", rows)
	}
	if rows := tenantScopeRows(t, db, unscoped, "scope-a", "box-b"); rows[1][2] != "Scope purchase" {
		t.Fatalf("adversarial transaction relation not exercised: %v", rows)
	}
	for _, suffix := range []string{"a", "b"} {
		want := tenantScopeRows(t, db, unscoped, "scope-"+suffix, "box-"+suffix)
		got := tenantScopeRows(t, db, query, "scope-"+suffix, "box-"+suffix, "scope-"+suffix)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("same-tenant history semantics changed: want=%v got=%v", want, got)
		}
	}
}

func TestTenantMetasLedgerN7HistoryLimitOrderAndHandlers(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	query := tenantMetasHistory(t)
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("history-%03d", i)
		tenantLedgerSeedEvent(t, db, id, "box-a", "BONUS", "", "", int64(i+1), 2200000000)
	}
	rows := tenantScopeRows(t, db, query, "scope-a", "box-a", "scope-a")
	if len(rows) != 100 {
		t.Fatalf("history limit=%d, want=100", len(rows))
	}
	for i, row := range rows {
		if row[0] != int64(105-i) {
			t.Fatalf("history order at %d: %v", i, row)
		}
	}
	for _, tc := range []struct {
		workspace, box, name string
		status               int
	}{
		{"scope-a", "box-a", "Box a", 200}, {"scope-b", "box-b", "Box b", 200},
		{"scope-a", "box-b", "", 404}, {"scope-b", "box-a", "", 404},
	} {
		h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, Templates: testHistoryTemplates(t), WorkspaceID: tc.workspace, UserID: "scope-user"}
		rr := httptest.NewRecorder()
		h.HandleHistoricoCaixinha(rr, httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id="+tc.box, nil))
		if rr.Code != tc.status || (tc.status == 200 && !strings.Contains(rr.Body.String(), tc.name)) {
			t.Fatalf("history response=%d %s", rr.Code, rr.Body.String())
		}
	}
}
