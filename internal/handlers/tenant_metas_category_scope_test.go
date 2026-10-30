package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"html/template"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Evaluate the real SQL expressions, including N8's financial SQL constant
// and N12/N13's local variables. Also verify each actual call's bind order.
func tenantMetasCategorySQL(t *testing.T, function, marker string, bindings ...string) []string {
	t.Helper()
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "metas.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"transactionNetAmountTSQL": transactionNetAmountTSQL}
	var eval func(ast.Expr) (string, bool)
	eval = func(expr ast.Expr) (string, bool) {
		switch e := expr.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				v, err := strconv.Unquote(e.Value)
				return v, err == nil
			}
		case *ast.Ident:
			v, ok := vars[e.Name]
			return v, ok
		case *ast.BinaryExpr:
			if e.Op == token.ADD {
				a, okA := eval(e.X)
				b, okB := eval(e.Y)
				return a + b, okA && okB
			}
		}
		return "", false
	}
	var queries []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if assign, ok := n.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
				if name, ok := assign.Lhs[0].(*ast.Ident); ok {
					if value, ok := eval(assign.Rhs[0]); ok {
						vars[name.Name] = value
					}
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Query" && selector.Sel.Name != "QueryRow") {
				return true
			}
			query, ok := eval(call.Args[0])
			if !ok || !strings.Contains(query, marker) {
				return true
			}
			var got []string
			for _, arg := range call.Args[1:] {
				var b bytes.Buffer
				if err := printer.Fprint(&b, fs, arg); err != nil {
					t.Fatal(err)
				}
				got = append(got, b.String())
			}
			if !reflect.DeepEqual(got, bindings) || strings.Count(query, "?") != len(bindings) {
				t.Fatalf("%s binds=%v want=%v placeholders=%d", function, got, bindings, strings.Count(query, "?"))
			}
			queries = append(queries, query)
			return true
		})
	}
	want := 1
	if function == "loadBoxFormData" || function == "queryBoxesForMetas" {
		want = 2
	}
	if len(queries) != want {
		t.Fatalf("%s SQL matches=%d want=%d", function, len(queries), want)
	}
	return queries
}

func seedTenantMetasCategories(t *testing.T) *sql.DB {
	t.Helper()
	db := seedTenantRuntimeScope(t)
	execTestSQL(t, db, `UPDATE categories SET name = 'Owned A', icon = 'home', color = '#22c55e' WHERE id = 'cat-a'`)
	execTestSQL(t, db, `UPDATE categories SET name = 'FOREIGN B', icon = 'car', color = '#ef4444' WHERE id = 'cat-b'`)
	execTestSQL(t, db, `UPDATE boxes SET category_id = 'cat-a', monthly_recharge = 1200, monthly_yield_rate = 0.005, target_date = 2200000000 WHERE id = 'box-a'`)
	execTestSQL(t, db, `UPDATE boxes SET category_id = 'cat-b' WHERE id = 'box-b'`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, parent_id) VALUES
		('child-a', 'scope-a', 'Child A', 'home', '#22c55e', 'EXPENSE', 'cat-a'),
		('child-b', 'scope-b', 'FOREIGN child B', 'car', '#ef4444', 'EXPENSE', 'cat-a'),
		('a-child-of-b', 'scope-a', 'Local child of foreign parent', 'home', '#22c55e', 'EXPENSE', 'cat-b')`)
	execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES
		('limit-a', 'scope-a', 'cat-a', 10000), ('limit-foreign', 'scope-a', 'cat-b', 30000), ('limit-b', 'scope-b', 'cat-b', 20000)`)
	return db
}

func TestTenantMetasCategoryN8DynamicOwnership(t *testing.T) {
	db := seedTenantMetasCategories(t)
	query := tenantMetasCategorySQL(t, "buildMetasData", "FROM cost_limits cl", "monthStart", "monthEnd", "h.WorkspaceID")[0]
	now := appTime(time.Now())
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	for _, tc := range []struct {
		id, workspace, category, kind, status string
		amount, date                          int64
	}{
		{"own", "scope-a", "cat-a", "EXPENSE", "paid", 100, start.AddDate(0, 0, 10).Unix()},
		{"own-child", "scope-a", "child-a", "EXPENSE", "paid", 200, start.AddDate(0, 0, 10).Unix()},
		{"own-pending", "scope-a", "cat-a", "EXPENSE", "pending", 50, start.AddDate(0, 0, 10).Unix()},
		{"foreign-child", "scope-a", "child-b", "EXPENSE", "paid", 700, start.AddDate(0, 0, 10).Unix()},
		{"foreign-category", "scope-a", "cat-b", "EXPENSE", "paid", 600, start.AddDate(0, 0, 10).Unix()},
		{"local-child-foreign-parent", "scope-a", "a-child-of-b", "EXPENSE", "paid", 450, start.AddDate(0, 0, 10).Unix()},
		{"foreign-workspace", "scope-b", "cat-a", "EXPENSE", "paid", 900, start.AddDate(0, 0, 10).Unix()},
		{"income", "scope-a", "cat-a", "INCOME", "paid", 333, start.AddDate(0, 0, 10).Unix()},
		{"before", "scope-a", "cat-a", "EXPENSE", "paid", 333, start.AddDate(0, 0, -1).Unix()},
		{"after", "scope-a", "cat-a", "EXPENSE", "paid", 333, end.AddDate(0, 0, 1).Unix()},
	} {
		account := "bank-a"
		if tc.workspace == "scope-b" {
			account = "bank-b"
		}
		execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status) VALUES (?, ?, 'scope-user', ?, ?, ?, ?, ?, ?, ?)`, tc.id, tc.workspace, account, tc.category, tc.kind, tc.amount, tc.date, tc.id, tc.status)
	}
	args := []any{start.Format("2006-01-02"), end.Format("2006-01-02"), "scope-a"}
	rows := tenantScopeRows(t, db, query, args...)
	if len(rows) != 1 || rows[0][0] != "limit-a" || rows[0][3] != "Owned A" || rows[0][4] != "home" || rows[0][5] != "#22c55e" || rows[0][7] != int64(350) {
		t.Fatalf("N8 scoped result=%v", rows)
	}
	tenantScopeIndexedPlan(t, db, query, args...)
	// Two independent negative controls: each must expose its original defect.
	outerOnly := strings.Replace(query, " AND c.workspace_id = cl.workspace_id", "", 1)
	if got := tenantScopeRows(t, db, outerOnly, args...); len(got) != 2 {
		t.Fatalf("N8 principal negative control=%v", got)
	}
	childrenOnly := strings.Replace(query, "workspace_id = cl.workspace_id AND (id = cl.category_id OR parent_id = cl.category_id)", "id = cl.category_id OR parent_id = cl.category_id", 1)
	if got := tenantScopeRows(t, db, childrenOnly, args...); len(got) != 1 || got[0][7] != int64(1050) {
		t.Fatalf("N8 child negative control=%v", got)
	}
	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "scope-a", UserID: "scope-user"}
	before := tenantLedgerSnapshot(t, db)
	data, err := h.buildMetasData("", "limites")
	if err != nil || len(data.Limites) != 1 {
		t.Fatalf("N8 real helper error=%v limits=%v", err, data.Limites)
	}
	card := data.Limites[0]
	if card.ID != "limit-a" || card.CategoryName != "Owned A" || card.CategoryIcon != "home" || card.CategoryColor != "#22c55e" || card.Spent != MoneyMinor(350) || card.MaxAmount != MoneyMinor(10000) || card.Remaining != MoneyMinor(9650) || card.Percent != 3 || data.TotalLimits != MoneyMinor(350) {
		t.Fatalf("N8 real read-model=%+v", card)
	}
	if !reflect.DeepEqual(before, tenantLedgerSnapshot(t, db)) {
		t.Fatal("N8 read mutated financial tables")
	}
	// With only valid relations, the full former SQL gives identical results.
	execTestSQL(t, db, `DELETE FROM cost_limits WHERE id = 'limit-foreign'`)
	execTestSQL(t, db, `DELETE FROM transactions WHERE id IN ('foreign-child', 'foreign-category', 'local-child-foreign-parent')`)
	legacy := strings.Replace(childrenOnly, " AND c.workspace_id = cl.workspace_id", "", 1)
	if got, want := tenantScopeRows(t, db, query, args...), tenantScopeRows(t, db, legacy, args...); !reflect.DeepEqual(got, want) {
		t.Fatalf("N8 valid semantics changed: got=%v want=%v", got, want)
	}
}

func TestTenantMetasCategoryN9LimitForm(t *testing.T) {
	db := seedTenantMetasCategories(t)
	query := tenantMetasCategorySQL(t, "loadLimitFormData", "FROM cost_limits cl", "id", "h.WorkspaceID")[0]
	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "scope-a"}
	var own MetaFormData
	if err := h.loadLimitFormData(&own, "limit-a"); err != nil || own.CategoryID != "cat-a" || own.CategoryName != "Owned A" || own.CategoryIcon != "home" || own.CategoryColor != "slate" || own.MaxAmountInput != formatCurrencyCentsBase(10000) {
		t.Fatalf("N9 own form=%+v err=%v", own, err)
	}
	for _, id := range []string{"limit-foreign", "limit-b", "missing"} {
		var form MetaFormData
		if err := h.loadLimitFormData(&form, id); !errors.Is(err, sql.ErrNoRows) || form.CategoryName != "" || form.CategoryIcon != "" {
			t.Fatalf("N9 foreign/missing form=%+v err=%v", form, err)
		}
	}
	legacy := strings.Replace(query, " AND c.workspace_id = cl.workspace_id", "", 1)
	if got := tenantScopeRows(t, db, legacy, "limit-foreign", "scope-a"); len(got) != 1 || got[0][3] != "FOREIGN B" {
		t.Fatalf("N9 negative control=%v", got)
	}
	if got, want := tenantScopeRows(t, db, query, "limit-a", "scope-a"), tenantScopeRows(t, db, legacy, "limit-a", "scope-a"); !reflect.DeepEqual(got, want) {
		t.Fatalf("N9 same-tenant changed: got=%v want=%v", got, want)
	}
	categories, err := h.queryMetaCategories()
	if err != nil || len(categories) != 3 {
		t.Fatalf("N9 options=%v err=%v", categories, err)
	}
	for i, c := range categories {
		if c.ID == "cat-b" || c.ID == "child-b" || (i > 0 && categories[i-1].Name > c.Name) {
			t.Fatalf("N9 foreign/unsorted options=%v", categories)
		}
	}
	tenantScopeIndexedPlan(t, db, query, "limit-a", "scope-a")
}

// Legacy schemas exist only in this in-memory fixture. Exercise the real
// primary error and real fallback without changing production migrations.
func tenantMetasCategoriesLegacyBoxes(t *testing.T, db *sql.DB) {
	t.Helper()
	restoreClassificationReserveGuards := suspendClassificationReserveTriggersForFixture(t, db)
	execTestSQL(t, db, `PRAGMA foreign_keys = OFF`)
	execTestSQL(t, db, `CREATE TABLE boxes_legacy (
		id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '', category_id TEXT,
		target_amount INTEGER NOT NULL DEFAULT 0, monthly_recharge INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()), updated_at INTEGER NOT NULL DEFAULT (unixepoch()))`)
	execTestSQL(t, db, `INSERT INTO boxes_legacy SELECT id, workspace_id, name, description, category_id, target_amount, monthly_recharge, created_at, updated_at FROM boxes`)
	execTestSQL(t, db, `DROP TABLE boxes`)
	execTestSQL(t, db, `ALTER TABLE boxes_legacy RENAME TO boxes`)
	execTestSQL(t, db, `CREATE INDEX idx_boxes_workspace ON boxes(workspace_id)`)
	execTestSQL(t, db, `CREATE INDEX idx_boxes_workspace_category ON boxes(workspace_id, category_id)`)
	restoreClassificationReserveGuards()
	execTestSQL(t, db, `PRAGMA foreign_keys = ON`)
}

func TestTenantMetasCategoryN10N11N12N13BoxFormsAndLists(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			db := seedTenantMetasCategories(t)
			forms := tenantMetasCategorySQL(t, "loadBoxFormData", "FROM boxes b", "id", "h.WorkspaceID")
			lists := tenantMetasCategorySQL(t, "queryBoxesForMetas", "FROM boxes b", "h.WorkspaceID")
			index := 0
			if legacy {
				tenantMetasCategoriesLegacyBoxes(t, db)
				index = 1
				for i, query := range []string{forms[0], lists[0]} {
					args := []any{"scope-a"}
					if i == 0 {
						args = []any{"box-a", "scope-a"}
					}
					rows, err := db.Query(query, args...)
					if rows != nil {
						rows.Close()
					}
					if !isMissingColumnError(err, "target_date") {
						t.Fatalf("primary must fail on target_date: %v", err)
					}
				}
			}
			h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "scope-a"}
			var absent MetaFormData
			var absentList [][]any
			for _, category := range []any{nil, "cat-b", "cat-a"} {
				execTestSQL(t, db, `UPDATE boxes SET category_id = ? WHERE id = 'box-a'`, category)
				before := tenantLedgerSnapshot(t, db)
				var form MetaFormData
				if err := h.loadBoxFormData(&form, "box-a"); err != nil {
					t.Fatal(err)
				}
				if form.BoxID != "box-a" || form.Name != "Box a" || form.TargetAmountInput != formatCurrencyCentsBase(100000) || form.MonthlyRechargeInput != formatCurrencyCentsBase(1200) || form.BalanceLabel != formatCurrencyLabel(1100) {
					t.Fatalf("box/financial form changed=%+v", form)
				}
				if legacy {
					if form.TargetMonthInput != "" || form.MonthlyYieldRateInput != "" || form.BoxIconKey != "" {
						t.Fatalf("legacy defaults changed=%+v", form)
					}
				} else if form.TargetMonthInput != formatMonthInputFromUnix(sql.NullInt64{Int64: 2200000000, Valid: true}) || form.MonthlyYieldRateInput != formatMonthlyYieldRateForInput(0.005) {
					t.Fatalf("primary date/yield changed=%+v", form)
				}
				rows, err := h.queryBoxesForMetas()
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				for rows.Next() {
					var id string
					var c sql.NullString
					var name, icon, color, categoryName, iconKey string
					var target, monthly, balance int64
					var date sql.NullInt64
					var yield float64
					if err := rows.Scan(&id, &c, &name, &target, &monthly, &date, &icon, &color, &categoryName, &yield, &balance, &iconKey); err != nil {
						t.Fatal(err)
					}
					ids = append(ids, id)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if !reflect.DeepEqual(ids, []string{"box-a"}) {
					t.Fatalf("own box dropped/foreign box exposed: %v", ids)
				}
				list := tenantScopeRows(t, db, lists[index], "scope-a")
				if len(list) != 1 || list[0][10] != int64(1100) {
					t.Fatalf("list balance/cardinality changed=%v", list)
				}
				if category == "cat-a" {
					if form.CategoryName != "Owned A" || form.CategoryIcon != "home" || form.CategoryColor != "slate" || list[0][6] != "home" || list[0][7] != "#22c55e" || list[0][8] != "Owned A" {
						t.Fatalf("same-tenant metadata changed: form=%+v list=%v", form, list)
					}
				} else {
					if form.CategoryName != "" || form.CategoryIcon != "" || form.CategoryColor != "slate" || list[0][6] != "" || list[0][7] != "#6b7280" || list[0][8] != "" {
						t.Fatalf("foreign/null metadata: form=%+v list=%v", form, list)
					}
					if category == nil {
						absent, absentList = form, list
					} else {
						form.CategoryID = ""
						list[0][1] = nil
						if !reflect.DeepEqual(form, absent) || !reflect.DeepEqual(list, absentList) {
							t.Fatalf("foreign does not match absent category fallback: form=%+v list=%v", form, list)
						}
					}
				}
				if !reflect.DeepEqual(before, tenantLedgerSnapshot(t, db)) {
					t.Fatal("box reads mutated financial tables")
				}
				for i, query := range []string{forms[index], lists[index]} {
					args := []any{"scope-a"}
					if i == 0 {
						args = []any{"box-a", "scope-a"}
					}
					old := strings.Replace(query, " AND c.workspace_id = b.workspace_id", "", 1)
					if category == "cat-b" {
						if got := tenantScopeRows(t, db, old, args...); !strings.Contains(fmt.Sprint(got), "FOREIGN B") {
							t.Fatalf("negative control missed foreign metadata: %v", got)
						}
					} else if got, want := tenantScopeRows(t, db, query, args...), tenantScopeRows(t, db, old, args...); !reflect.DeepEqual(got, want) {
						t.Fatalf("same-tenant/null SQL changed: got=%v want=%v", got, want)
					}
					tenantScopeIndexedPlan(t, db, query, args...)
				}
			}
		})
	}
}

func TestTenantMetasCategoryN14Sheets(t *testing.T) {
	db := seedTenantMetasCategories(t)
	query := tenantMetasCategorySQL(t, "buildBoxSheetData", "FROM boxes b", "boxID", "h.WorkspaceID")[0]
	templates := template.Must(template.New("sheets").Parse(`{{define "caixinha-aporte-sheet"}}aporte|{{.BoxID}}|{{.BoxName}}|{{.CategoryName}}|{{.CategoryIcon}}|{{.Color}}|{{.IconOrDefault}}|{{.Balance.Reais}}{{end}}{{define "caixinha-resgate-sheet"}}resgate|{{.BoxID}}|{{.BoxName}}|{{.CategoryName}}|{{.CategoryIcon}}|{{.Color}}|{{.IconOrDefault}}|{{.Balance.Reais}}{{end}}`))
	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "scope-a", Templates: templates}
	var absent BoxSheetData
	for _, category := range []any{nil, "cat-b", "cat-a"} {
		execTestSQL(t, db, `UPDATE boxes SET category_id = ? WHERE id = 'box-a'`, category)
		before := tenantLedgerSnapshot(t, db)
		d, err := h.buildBoxSheetData("box-a")
		if err != nil || d.BoxID != "box-a" || d.BoxName != "Box a" || d.Balance != MoneyMinor(1100) || d.Target != MoneyMinor(100000) || d.AvailableForRelease != MoneyMinor(1100) {
			t.Fatalf("N14 financial/box fields=%+v err=%v", d, err)
		}
		if category == "cat-a" {
			if d.CategoryName != "Owned A" || d.CategoryIcon != "home" || d.Color != "#22c55e" || d.IconOrDefault() != "house" {
				t.Fatalf("N14 own metadata=%+v", d)
			}
		} else if d.CategoryName != "" || d.CategoryIcon != "" || d.Color != "#6b7280" {
			t.Fatalf("N14 foreign/null metadata=%+v", d)
		} else if category == nil {
			absent = d
		} else if !reflect.DeepEqual(d, absent) {
			t.Fatalf("N14 absent fallback changed: got=%+v want=%+v", d, absent)
		}
		for _, handle := range []func(http.ResponseWriter, *http.Request){h.HandleAporteCaixinhaSheet, h.HandleResgateCaixinhaSheet} {
			rr := httptest.NewRecorder()
			handle(rr, httptest.NewRequest(http.MethodGet, "/sheet?box_id=box-a", nil))
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Box a") || strings.Contains(rr.Body.String(), "FOREIGN") || strings.Contains(rr.Body.String(), "#ef4444") {
				t.Fatalf("N14 sheet=%d %s", rr.Code, rr.Body.String())
			}
		}
		if !reflect.DeepEqual(before, tenantLedgerSnapshot(t, db)) {
			t.Fatal("N14 sheet mutated financial tables")
		}
		old := strings.Replace(query, " AND c.workspace_id = b.workspace_id", "", 1)
		if category == "cat-b" {
			if got := tenantScopeRows(t, db, old, "box-a", "scope-a"); !strings.Contains(fmt.Sprint(got), "FOREIGN B") {
				t.Fatalf("N14 negative control=%v", got)
			}
		} else if got, want := tenantScopeRows(t, db, query, "box-a", "scope-a"), tenantScopeRows(t, db, old, "box-a", "scope-a"); !reflect.DeepEqual(got, want) {
			t.Fatalf("N14 same-tenant/null changed: got=%v want=%v", got, want)
		}
		tenantScopeIndexedPlan(t, db, query, "box-a", "scope-a")
	}
	if _, err := h.buildBoxSheetData("box-b"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("N14 foreign box allowed: %v", err)
	}
}
