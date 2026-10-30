package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Extract the real mutation and assert its bind order and cardinality helper.
// Direct SQLite execution below bypasses every handler pre-validation.
func tenantConfigSQL(t *testing.T, function, marker, executor string, bindings ...string) string {
	t.Helper()
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "configuracoes.go", nil, 0)
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
			if !ok {
				return true
			}
			index, name := 0, ""
			switch f := call.Fun.(type) {
			case *ast.Ident:
				name, index = f.Name, 1
			case *ast.SelectorExpr:
				name = f.Sel.Name
			}
			if name != executor || len(call.Args) <= index {
				return true
			}
			literal, ok := call.Args[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			query, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(query, marker) {
				return true
			}
			var got []string
			for _, arg := range call.Args[index+1:] {
				var b bytes.Buffer
				if err := printer.Fprint(&b, fs, arg); err != nil {
					t.Fatal(err)
				}
				got = append(got, b.String())
			}
			// Reorders have two similar statements, distinguished by their target bind.
			if strings.Contains(marker, "UPDATE accounts SET sort_order") && len(got) > 1 && got[1] != bindings[1] {
				return true
			}
			if !reflect.DeepEqual(got, bindings) || strings.Count(query, "?") != len(bindings) {
				t.Fatalf("%s bindings=%v, want=%v", function, got, bindings)
			}
			matches = append(matches, query)
			return true
		})
	}
	if len(matches) != 1 {
		t.Fatalf("%s: matched %d mutations for %s", function, len(matches), marker)
	}
	return matches[0]
}

func tenantConfigInlineSQL(t *testing.T) string {
	return tenantConfigSQL(t, "HandleCartoesInlineSave", "UPDATE credit_cards", "Exec", "closingDay", "dueDay", "creditLimit", "defaultPaymentAccountID", "accountID", "h.WorkspaceID")
}
func tenantConfigInsertSQL(t *testing.T) string {
	return tenantConfigSQL(t, "HandleCartoesCreate", "INSERT INTO credit_cards", "execOneTx", "uuid.NewString()", "closingDay", "dueDay", "creditLimit", "defaultPaymentAccountID", "accountID", "h.WorkspaceID")
}
func tenantConfigSwapSQL(t *testing.T, function string) [2]string {
	return [2]string{
		tenantConfigSQL(t, function, "UPDATE accounts SET sort_order", "execOneTx", "adjacentSort", "accountID", "h.WorkspaceID"),
		tenantConfigSQL(t, function, "UPDATE accounts SET sort_order", "execOneTx", "sortOrder", "adjacentID", "h.WorkspaceID"),
	}
}

func seedTenantConfigScope(t *testing.T) *sql.DB {
	t.Helper()
	db := seedTenantRuntimeScope(t)
	for _, suffix := range []string{"a", "b"} {
		for _, kind := range []struct{ prefix, typ string }{{"bank-", "CHECKING"}, {"card-", "CREDIT_CARD"}} {
			execTestSQL(t, db, `UPDATE accounts SET sort_order = 10 WHERE id = ?`, kind.prefix+suffix)
			execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, sort_order) VALUES (?, ?, ?, ?, 20)`, kind.prefix+suffix+"2", "scope-"+suffix, kind.prefix+suffix+"2", kind.typ)
		}
		execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES (?, ?, 20, 10, 123456)`, "cc-"+suffix+"2", "card-"+suffix+"2")
		execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, sort_order) VALUES (?, ?, 'Bare', 'CREDIT_CARD', 99)`, "bare-"+suffix, "scope-"+suffix)
	}
	return db
}

func tenantConfigSnapshot(t *testing.T, db *sql.DB) [2][][]any {
	t.Helper()
	return [2][][]any{tenantScopeRows(t, db, `SELECT * FROM accounts ORDER BY id`), tenantScopeRows(t, db, `SELECT * FROM credit_cards ORDER BY id`)}
}
func tenantConfigUnchanged(t *testing.T, db *sql.DB, before [2][][]any) {
	t.Helper()
	if after := tenantConfigSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("accounts/cards changed:\nbefore=%v\nafter=%v", before, after)
	}
}
func tenantConfigAffected(t *testing.T, result sql.Result, err error, want int64) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	got, err := result.RowsAffected()
	if err != nil || got != want {
		t.Fatalf("RowsAffected=%d err=%v, want=%d", got, err, want)
	}
}

func TestTenantConfigScopeS6Direct(t *testing.T) {
	db := seedTenantConfigScope(t)
	query := tenantConfigInlineSQL(t)
	tenantScopeIndexedPlan(t, db, query, 15, 5, 90000, "", "card-a", "scope-a")
	for _, tc := range []struct{ account, workspace string }{{"card-b", "scope-a"}, {"card-a", "scope-b"}, {"card-b", ""}, {"card-b", "missing"}, {"missing", "scope-a"}, {"bare-a", "scope-a"}} {
		t.Run(tc.account+"/"+tc.workspace, func(t *testing.T) {
			before := tenantConfigSnapshot(t, db)
			res, err := db.Exec(query, 15, 5, 90000, "", tc.account, tc.workspace)
			tenantConfigAffected(t, res, err, 0)
			tenantConfigUnchanged(t, db, before)
		})
	}
	// An orphan owner must not authorize a card update either.
	execTestSQL(t, db, `PRAGMA foreign_keys = OFF`)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day) VALUES ('orphan', 'missing-owner', 20, 10)`)
	execTestSQL(t, db, `PRAGMA foreign_keys = ON`)
	before := tenantConfigSnapshot(t, db)
	res, err := db.Exec(query, 15, 5, 90000, "", "missing-owner", "scope-a")
	tenantConfigAffected(t, res, err, 0)
	tenantConfigUnchanged(t, db, before)
	for _, suffix := range []string{"a", "b"} {
		foreignBefore := tenantScopeRows(t, db, `SELECT * FROM credit_cards WHERE account_id = ?`, "card-"+map[string]string{"a": "b", "b": "a"}[suffix])
		res, err := db.Exec(query, 15, 5, 90000, "bank-"+suffix, "card-"+suffix, "scope-"+suffix)
		tenantConfigAffected(t, res, err, 1)
		got := tenantScopeRows(t, db, `SELECT closing_day, due_day, credit_limit, default_payment_account_id FROM credit_cards WHERE account_id = ?`, "card-"+suffix)
		want := [][]any{{int64(15), int64(5), int64(90000), "bank-" + suffix}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("same tenant=%v want=%v", got, want)
		}
		foreignAfter := tenantScopeRows(t, db, `SELECT * FROM credit_cards WHERE account_id = ?`, "card-"+map[string]string{"a": "b", "b": "a"}[suffix])
		if !reflect.DeepEqual(foreignBefore, foreignAfter) {
			t.Fatal("foreign card changed")
		}
	}
}

func TestTenantConfigScopeN1Direct(t *testing.T) {
	db := seedTenantConfigScope(t)
	query := tenantConfigInsertSQL(t)
	tenantScopeIndexedPlan(t, db, query, "new-card", 15, 5, 90000, "", "bare-a", "scope-a")
	for _, tc := range []struct{ account, workspace string }{{"bare-b", "scope-a"}, {"bare-a", "scope-b"}, {"bare-b", ""}, {"bare-b", "missing"}, {"missing", "scope-a"}} {
		t.Run(tc.account+"/"+tc.workspace, func(t *testing.T) {
			before := tenantConfigSnapshot(t, db)
			res, err := db.Exec(query, "new-card", 15, 5, 90000, "", tc.account, tc.workspace)
			tenantConfigAffected(t, res, err, 0)
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := execOneTx(tx, query, "new-card", 15, 5, 90000, "", tc.account, tc.workspace); err == nil {
				t.Error("zero-row INSERT accepted")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			tenantConfigUnchanged(t, db, before)
		})
	}
	for _, suffix := range []string{"a", "b"} {
		res, err := db.Exec(query, "new-card-"+suffix, 15, 5, 90000, "", "bare-"+suffix, "scope-"+suffix)
		tenantConfigAffected(t, res, err, 1)
		got := tenantScopeRows(t, db, `SELECT account_id, closing_day, due_day, credit_limit, default_payment_account_id FROM credit_cards WHERE id = ?`, "new-card-"+suffix)
		if !reflect.DeepEqual(got, [][]any{{"bare-" + suffix, int64(15), int64(5), int64(90000), nil}}) {
			t.Fatalf("same-tenant INSERT=%v", got)
		}
	}
}

func TestTenantConfigScopeS7S8DirectAndRollback(t *testing.T) {
	for _, tc := range []struct{ function, prefix, site string }{{"HandleContasReorder", "bank-", "S7"}, {"HandleCartoesReorder", "card-", "S8"}} {
		t.Run(tc.site, func(t *testing.T) {
			db := seedTenantConfigScope(t)
			queries := tenantConfigSwapSQL(t, tc.function)
			// Audit each side independently, including first-side foreign ownership.
			for side, query := range queries {
				for _, id := range []string{tc.prefix + "b", "missing"} {
					before := tenantConfigSnapshot(t, db)
					res, err := db.Exec(query, 20, id, "scope-a")
					tenantConfigAffected(t, res, err, 0)
					tenantConfigUnchanged(t, db, before)
				}
				res, err := db.Exec(query, 10, tc.prefix+"a", "scope-a")
				tenantConfigAffected(t, res, err, 1)
				t.Logf("%s side=%d RowsAffected audited", tc.site, side+1)
			}
			for _, ids := range [][2]string{{tc.prefix + "a", tc.prefix + "b"}, {tc.prefix + "b", tc.prefix + "a"}, {tc.prefix + "a", "missing"}, {"missing", tc.prefix + "a"}} {
				before := tenantConfigSnapshot(t, db)
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				firstErr := execOneTx(tx, queries[0], 20, ids[0], "scope-a")
				if firstErr == nil {
					if err := execOneTx(tx, queries[1], 10, ids[1], "scope-a"); err == nil {
						t.Error("foreign/missing second side accepted")
					}
					var sort int
					if err := tx.QueryRow(`SELECT sort_order FROM accounts WHERE id = ?`, ids[0]).Scan(&sort); err != nil || sort != 20 {
						t.Fatalf("first UPDATE did not execute: sort=%d err=%v", sort, err)
					}
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				tenantConfigUnchanged(t, db, before)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := execOneTx(tx, queries[0], 20, tc.prefix+"a", "scope-a"); err != nil {
				t.Fatal(err)
			}
			if err := execOneTx(tx, queries[1], 10, tc.prefix+"a2", "scope-a"); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			got := tenantScopeRows(t, db, `SELECT id, sort_order FROM accounts WHERE id IN (?, ?) ORDER BY id`, tc.prefix+"a", tc.prefix+"a2")
			if !reflect.DeepEqual(got, [][]any{{tc.prefix + "a", int64(20)}, {tc.prefix + "a2", int64(10)}}) {
				t.Fatalf("same-tenant swap=%v", got)
			}
		})
	}
}

func tenantConfigHandler(t *testing.T, db *sql.DB) ConfiguracoesHandler {
	t.Helper()
	tmpl := template.Must(template.New("test").Parse(`
 {{define "configuracoes-contas-content"}}accounts{{end}}
 {{define "configuracoes-cartoes-content"}}{{.FlashError}}{{.FlashSuccess}}{{end}}
 {{define "config-cartoes-row"}}{{.Name}}{{end}}`))
	return ConfiguracoesHandler{DB: db, WorkspaceID: "scope-a", UserID: "scope-user", ActorRole: "ADMIN", Templates: tmpl}
}
func tenantConfigRequest(form url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/configuracoes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	return req
}
func tenantConfigCardForm() url.Values {
	return url.Values{"name": {"Edited"}, "provider_slug": {"nubank"}, "credit_limit": {"900,00"}, "closing_day": {"15"}, "due_day": {"5"}, "color": {"#123456"}, "icon": {"credit-card"}}
}

func TestTenantConfigScopeS6Handler(t *testing.T) {
	for _, tc := range []struct {
		id     string
		status int
		ignore bool
	}{{"card-a", 200, false}, {"card-b", 404, false}, {"missing", 404, false}, {"bare-a", 422, false}, {"card-a", 422, true}} {
		t.Run(fmt.Sprintf("%s/%t", tc.id, tc.ignore), func(t *testing.T) {
			db := seedTenantConfigScope(t)
			h := tenantConfigHandler(t, db)
			if tc.ignore {
				execTestSQL(t, db, `CREATE TRIGGER ignore_inline BEFORE UPDATE ON credit_cards BEGIN SELECT RAISE(IGNORE); END`)
			}
			before := tenantConfigSnapshot(t, db)
			foreignBefore := tenantScopeRows(t, db, `SELECT * FROM accounts WHERE workspace_id = 'scope-b' ORDER BY id`)
			rr := httptest.NewRecorder()
			h.HandleCartoesInlineSave(rr, tenantConfigRequest(tenantConfigCardForm()), tc.id)
			if rr.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.status, rr.Body.String())
			}
			if tc.status != 200 {
				tenantConfigUnchanged(t, db, before)
				return
			}
			got := tenantScopeRows(t, db, `SELECT a.name, cc.closing_day, cc.due_day, cc.credit_limit FROM accounts a JOIN credit_cards cc ON cc.account_id = a.id WHERE a.id = 'card-a'`)
			if !reflect.DeepEqual(got, [][]any{{"Edited", int64(15), int64(5), int64(90000)}}) {
				t.Fatalf("same-tenant inline=%v", got)
			}
			if !reflect.DeepEqual(foreignBefore, tenantScopeRows(t, db, `SELECT * FROM accounts WHERE workspace_id = 'scope-b' ORDER BY id`)) {
				t.Fatal("foreign accounts changed")
			}
			if !reflect.DeepEqual(before[1][2:], tenantConfigSnapshot(t, db)[1][2:]) {
				t.Fatal("foreign cards changed")
			}
		})
	}
}

func TestTenantConfigScopeS7S8Handlers(t *testing.T) {
	for _, prefix := range []string{"bank-", "card-"} {
		for _, mode := range []string{"success", "foreign-target", "missing-target", "foreign-second", "missing-second", "zero-second", "error-second"} {
			t.Run(prefix+mode, func(t *testing.T) {
				db := seedTenantConfigScope(t)
				h := tenantConfigHandler(t, db)
				first, second := prefix+"a", prefix+"a2"
				switch mode {
				case "foreign-second":
					kind := "CHECKING"
					if prefix == "card-" {
						kind = "CREDIT_CARD"
					}
					// workspace_id is immutable. Replace the selected owner inside
					// the test transaction, without disabling that production guard.
					execTestSQL(t, db, fmt.Sprintf(`CREATE TRIGGER replace_owner AFTER UPDATE OF sort_order ON accounts WHEN NEW.id = '%s' BEGIN DELETE FROM accounts WHERE id = '%s'; INSERT INTO accounts (id, workspace_id, name, type, sort_order) VALUES ('%s', 'scope-b', 'Foreign replacement', '%s', 20); END`, first, second, second, kind))
					// Prove the first UPDATE succeeds and the next target really
					// belongs to B; an earlier trigger error must not mask the test.
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					query := tenantConfigSwapSQL(t, map[string]string{"bank-": "HandleContasReorder", "card-": "HandleCartoesReorder"}[prefix])[0]
					if err := execOneTx(tx, query, 20, first, "scope-a"); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
					var workspace string
					if err := tx.QueryRow(`SELECT workspace_id FROM accounts WHERE id = ?`, second).Scan(&workspace); err != nil || workspace != "scope-b" {
						_ = tx.Rollback()
						t.Fatalf("foreign-second fixture: workspace=%s err=%v", workspace, err)
					}
					if err := tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				case "missing-second":
					execTestSQL(t, db, fmt.Sprintf(`CREATE TRIGGER remove_second AFTER UPDATE OF sort_order ON accounts WHEN NEW.id = '%s' BEGIN DELETE FROM accounts WHERE id = '%s'; END`, first, second))
				case "zero-second":
					execTestSQL(t, db, fmt.Sprintf(`CREATE TRIGGER skip_second BEFORE UPDATE OF sort_order ON accounts WHEN NEW.id = '%s' BEGIN SELECT RAISE(IGNORE); END`, second))
				case "error-second":
					execTestSQL(t, db, fmt.Sprintf(`CREATE TRIGGER fail_second BEFORE UPDATE OF sort_order ON accounts WHEN NEW.id = '%s' BEGIN SELECT RAISE(ABORT, 'injected second update failure'); END`, second))
				}
				before := tenantConfigSnapshot(t, db)
				call := h.HandleContasReorder
				if prefix == "card-" {
					call = h.HandleCartoesReorder
				}
				id := first
				if mode == "foreign-target" {
					id = prefix + "b"
				}
				if mode == "missing-target" {
					id = "missing"
				}
				rr := httptest.NewRecorder()
				call(rr, tenantConfigRequest(nil), id, "down")
				if rr.Code != http.StatusOK {
					t.Fatalf("render changed: status=%d body=%s", rr.Code, rr.Body.String())
				}
				if mode != "success" {
					tenantConfigUnchanged(t, db, before)
					return
				}
				wantAccounts := make([][]any, len(before[0]))
				for i, row := range before[0] {
					wantAccounts[i] = append([]any(nil), row...)
					// Find sort_order by schema column name instead of relying on migration order.
					cols, err := db.Query(`SELECT * FROM accounts LIMIT 0`)
					if err != nil {
						t.Fatal(err)
					}
					names, err := cols.Columns()
					cols.Close()
					if err != nil {
						t.Fatal(err)
					}
					for j, name := range names {
						if name == "sort_order" && row[0] == first {
							wantAccounts[i][j] = int64(20)
						}
						if name == "sort_order" && row[0] == second {
							wantAccounts[i][j] = int64(10)
						}
					}
				}
				after := tenantConfigSnapshot(t, db)
				if !reflect.DeepEqual(after[0], wantAccounts) || !reflect.DeepEqual(after[1], before[1]) {
					t.Fatalf("same-tenant down swap=%v", after)
				}
				rr = httptest.NewRecorder()
				call(rr, tenantConfigRequest(nil), first, "up")
				if rr.Code != http.StatusOK {
					t.Fatalf("up status=%d", rr.Code)
				}
				tenantConfigUnchanged(t, db, before)
			})
		}
	}
}

func TestTenantConfigScopeN1HandlerRollback(t *testing.T) {
	for _, mode := range []string{"success", "missing-owner", "zero-insert"} {
		t.Run(mode, func(t *testing.T) {
			db := seedTenantConfigScope(t)
			h := tenantConfigHandler(t, db)
			switch mode {
			case "missing-owner":
				execTestSQL(t, db, `CREATE TRIGGER remove_new_owner AFTER INSERT ON accounts WHEN NEW.workspace_id = 'scope-a' BEGIN DELETE FROM accounts WHERE id = NEW.id; END`)
			case "zero-insert":
				execTestSQL(t, db, `CREATE TRIGGER ignore_new_card BEFORE INSERT ON credit_cards BEGIN SELECT RAISE(IGNORE); END`)
			}
			before := tenantConfigSnapshot(t, db)
			rr := httptest.NewRecorder()
			h.HandleCartoesCreate(rr, tenantConfigRequest(tenantConfigCardForm()))
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if mode != "success" {
				if !strings.Contains(rr.Body.String(), "Não foi possível criar dados do cartão") {
					t.Fatalf("zero-row INSERT silent success: %s", rr.Body.String())
				}
				tenantConfigUnchanged(t, db, before)
				return
			}
			if !strings.Contains(rr.Body.String(), "Cartão criado com sucesso") {
				t.Fatalf("creation failed: %s", rr.Body.String())
			}
			after := tenantConfigSnapshot(t, db)
			if len(after[0]) != len(before[0])+1 || len(after[1]) != len(before[1])+1 {
				t.Fatal("card/account creation count changed")
			}
			for table, rows := range before {
				for _, row := range rows {
					found := false
					for _, got := range after[table] {
						if reflect.DeepEqual(row, got) {
							found = true
							break
						}
					}
					if !found {
						t.Fatalf("existing row changed: %v", row)
					}
				}
			}
		})
	}
}

func TestTenantConfigScopeS5HasNoProductionCallers(t *testing.T) {
	err := filepath.WalkDir("../..", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != "../.." && path != "../../internal" && path != "../../cmd" && filepath.Dir(path) == "../.." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandleCartoesEdit" {
				t.Errorf("S5 production reference: %s", path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
