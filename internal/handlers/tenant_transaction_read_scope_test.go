package handlers

import (
	"database/sql"
	"go/ast"
	"go/parser"
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

const transactionRuleScope = " AND rr.workspace_id = t.workspace_id"
const transactionInvoiceScope = ` AND EXISTS (
			SELECT 1 FROM accounts invoice_owner
			WHERE invoice_owner.id = i.account_id AND invoice_owner.workspace_id = t.workspace_id
		)`

func seedTenantTransactionRead(t *testing.T) *sql.DB {
	t.Helper()
	db := seedTenantRuntimeScope(t)
	for _, suffix := range []string{"a", "b"} {
		execTestSQL(t, db, `UPDATE transactions SET account_id = ?, category_id = ?, recurring_rule_id = ?, invoice_id = ?, total_installments = 3, recurrence_sequence = 2, notes = 'Scope 123', due_date = ?, created_at = 100, updated_at = 200 WHERE id = ?`, "bank-"+suffix, "cat-"+suffix, "rule-"+suffix, "inv-"+suffix, testUnixDate("2099-06-27"), "tx-"+suffix)
		execTestSQL(t, db, `UPDATE recurring_rules SET total_occurrences = ?, frequency = ? WHERE id = ?`, map[string]int{"a": 7, "b": 987}[suffix], map[string]string{"a": "MONTHLY", "b": "ANNUAL"}[suffix], "rule-"+suffix)
	}
	execTestSQL(t, db, `UPDATE invoices SET reference = '2099-10' WHERE id = 'inv-b'`)
	return db
}

// The final N17 statement is evaluated from the actual straight-line AST up
// to DB.Query(query, args...). The WHERE and binds come from the real helper;
// neither the base SELECT nor the ordering suffix is copied into the test.
func tenantTransactionListSQL(t *testing.T, h *TransactionHandler, account string, filters LancamentosFilters) (string, []any) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "transacoes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
	var query string
	var args []any
	whereAdded := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "buildLancamentosData" {
			continue
		}
		for _, stmt := range fn.Body.List {
			if assign, ok := stmt.(*ast.AssignStmt); ok {
				if len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
					name, ok := assign.Lhs[0].(*ast.Ident)
					if !ok || name.Name != "query" {
						continue
					}
					value := ""
					switch rhs := assign.Rhs[0].(type) {
					case *ast.BasicLit:
						value, err = strconv.Unquote(rhs.Value)
						if err != nil {
							t.Fatal(err)
						}
					case *ast.Ident:
						if rhs.Name != "where" || args == nil {
							t.Fatal("unexpected dynamic SQL expression")
						}
						value, _ = h.lancamentosWhereClause(start.Unix(), start.AddDate(0, 1, 0).Add(-time.Second).Unix(), account, filters)
						whereAdded = true
					default:
						t.Fatal("unsupported query expression")
					}
					if assign.Tok == token.DEFINE {
						query = value
					} else if assign.Tok == token.ADD_ASSIGN {
						query += value
					} else {
						t.Fatal("unexpected query assignment")
					}
				}
				if len(assign.Lhs) == 2 && len(assign.Rhs) == 1 && query != "" {
					lhs, ok := assign.Lhs[0].(*ast.Ident)
					if ok && lhs.Name == "where" {
						call, ok := assign.Rhs[0].(*ast.CallExpr)
						if !ok || len(call.Args) != 4 {
							t.Fatal("unexpected WHERE builder")
						}
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok || sel.Sel.Name != "lancamentosWhereClause" {
							t.Fatal("unexpected WHERE helper")
						}
						for i, want := range []string{"monthStart", "monthEnd", "accountFilter", "filters"} {
							arg, ok := call.Args[i].(*ast.Ident)
							if !ok || arg.Name != want {
								t.Fatal("WHERE argument order changed")
							}
						}
						_, args = h.lancamentosWhereClause(start.Unix(), start.AddDate(0, 1, 0).Add(-time.Second).Unix(), account, filters)
					}
				}
			}
			found := false
			ast.Inspect(stmt, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Query" {
					return true
				}
				name, ok := call.Args[0].(*ast.Ident)
				if !ok || name.Name != "query" {
					return true
				}
				bind, ok := call.Args[1].(*ast.Ident)
				if !ok || bind.Name != "args" || !call.Ellipsis.IsValid() || !whereAdded || strings.Count(query, "?") != len(args) {
					t.Fatal("final query/bind contract changed")
				}
				found = true
				return false
			})
			if found {
				t.Logf("N17 final SQL=%s\nbinds=%v", query, args)
				return query, args
			}
		}
	}
	t.Fatal("N17 final query not found")
	return "", nil
}

func tenantTransactionSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	result := tenantLedgerSnapshot(t, db)
	for _, table := range []string{"recurring_rules", "invoices", "transaction_refunds", "invoice_refunds", "recurring_occurrence_blocks"} {
		orderBy := "id"
		if table == "recurring_occurrence_blocks" {
			orderBy = "workspace_id, series_id, recurrence_sequence"
		}
		result[table] = tenantScopeRows(t, db, "SELECT * FROM "+table+" ORDER BY "+orderBy)
	}
	return result
}

func TestTenantTransactionReadN15Detail(t *testing.T) {
	db := seedTenantTransactionRead(t)
	query := tenantScopeStatement(t, "transacoes.go", "HandleDetalheTransacao", "rr.frequency", "id", "h.WorkspaceID")
	legacy := strings.Replace(strings.Replace(query, transactionRuleScope, "", 1), transactionInvoiceScope, "", 1)
	for _, tc := range []struct {
		name, rule, invoice, frequency, reference string
	}{
		{"same", "rule-a", "inv-a", "MONTHLY", "2099-08"},
		{"foreign-rule", "rule-b", "inv-a", "", "2099-08"},
		{"foreign-invoice", "rule-a", "inv-b", "MONTHLY", ""},
		{"both-foreign", "rule-b", "inv-b", "", ""},
		{"null", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = NULLIF(?, ''), invoice_id = NULLIF(?, '') WHERE id = 'tx-a'`, tc.rule, tc.invoice)
			before := tenantTransactionSnapshot(t, db)
			got := tenantScopeRows(t, db, query, "tx-a", "scope-a")
			parent := tenantScopeRows(t, db, legacy, "tx-a", "scope-a")
			if len(got) != 1 || len(parent) != 1 {
				t.Fatalf("own transaction lost or duplicated: %v", got)
			}
			for index, want := range map[int]string{5: tc.frequency, 7: tc.reference} {
				var expected any
				if want != "" {
					expected = want
				}
				if got[0][index] != expected {
					t.Fatalf("metadata column %d=%v want=%v", index, got[0][index], expected)
				}
				if tc.name == "same" || tc.name == "null" {
					if !reflect.DeepEqual(got, parent) {
						t.Fatal("valid/null detail changed from parent")
					}
				}
				if strings.Contains(tc.name, "foreign") && got[0][index] == nil && parent[0][index] == nil {
					t.Fatal("negative control did not expose foreign metadata")
				}
				parent[0][index] = got[0][index]
			}
			if !reflect.DeepEqual(got, parent) {
				t.Fatal("non-metadata detail fields changed")
			}
			var form FormTransacaoData
			h := TransactionHandler{DB: db, WorkspaceID: "scope-a", UserID: "scope-user"}
			h.Templates = template.Must(template.New("test").Funcs(template.FuncMap{"capture": func(data FormTransacaoData) string { form = data; return "detail" }}).Parse(`{{define "form-lancamento"}}{{capture .}}{{end}}`))
			rr := httptest.NewRecorder()
			h.HandleDetalheTransacao(rr, httptest.NewRequest(http.MethodGet, "/transacoes/tx-a", nil), "tx-a")
			wantNext := ""
			if tc.reference != "" {
				wantNext, _ = nextInvoiceReference(tc.reference)
			}
			if rr.Code != http.StatusOK || rr.Body.String() != "detail" || form.EditID != "tx-a" || form.RecorrenciaInicial != tc.frequency || form.QuickMoveDestinationRef != wantNext || form.FixoInicial != (tc.rule != "") || form.AnotacoesPreenchidas != "Scope 123" {
				t.Fatalf("real detail status=%d form=%+v", rr.Code, form)
			}
			if !reflect.DeepEqual(before, tenantTransactionSnapshot(t, db)) {
				t.Fatal("detail mutated financial state")
			}
			tenantScopeIndexedPlan(t, db, query, "tx-a", "scope-a")
		})
	}
	// A real same-tenant B detail and workspace-denial control.
	if got, parent := tenantScopeRows(t, db, query, "tx-b", "scope-b"), tenantScopeRows(t, db, legacy, "tx-b", "scope-b"); len(got) != 1 || !reflect.DeepEqual(got, parent) {
		t.Fatal("same-tenant B detail changed")
	}
	if got := tenantScopeRows(t, db, query, "tx-b", "scope-a"); len(got) != 0 {
		t.Fatal("foreign transaction returned")
	}
	// CTEs shadow only optional fixture relations to model legacy missing rows.
	// No FK/trigger is disabled, removed, or changed in the migrated database.
	execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = 'rule-a', invoice_id = 'inv-a' WHERE id = 'tx-a'`)
	for _, prefix := range []string{
		`WITH recurring_rules AS (SELECT * FROM main.recurring_rules WHERE id != 'rule-a'), invoices AS (SELECT * FROM main.invoices WHERE id != 'inv-a') `,
		`WITH recurring_rules AS (SELECT * FROM main.recurring_rules WHERE id != 'rule-a'), accounts AS (SELECT * FROM main.accounts WHERE id != 'card-a') `,
	} {
		got := tenantScopeRows(t, db, prefix+query, "tx-a", "scope-a")
		if len(got) != 1 || got[0][5] != nil || got[0][7] != nil {
			t.Fatalf("missing/orphan optional metadata contract=%v", got)
		}
	}
}

func TestTenantTransactionReadN16Fetch(t *testing.T) {
	db := seedTenantTransactionRead(t)
	query := tenantScopeStatement(t, "transacoes.go", "fetchTransactionRow", "COALESCE(rr.total_occurrences", "id", "h.WorkspaceID")
	legacy := strings.Replace(query, transactionRuleScope, "", 1)
	h := TransactionHandler{DB: db, WorkspaceID: "scope-a"}
	for _, tc := range []struct {
		name, rule string
		total      int64
	}{{"same", "rule-a", 7}, {"foreign", "rule-b", 3}, {"null", "", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = NULLIF(?, '') WHERE id = 'tx-a'`, tc.rule)
			before := tenantTransactionSnapshot(t, db)
			got := tenantScopeRows(t, db, query, "tx-a", "scope-a")
			parent := tenantScopeRows(t, db, legacy, "tx-a", "scope-a")
			if len(got) != 1 || got[0][12] != tc.total || len(parent) != 1 {
				t.Fatalf("row metadata/fallback=%v", got)
			}
			if tc.name == "foreign" {
				if parent[0][12] != int64(987) {
					t.Fatal("negative control did not leak B occurrence count")
				}
				parent[0][12] = int64(3)
			}
			if !reflect.DeepEqual(got, parent) {
				t.Fatal("non-metadata fetch fields changed")
			}
			row, err := h.fetchTransactionRow("tx-a")
			if err != nil || row.ID != "tx-a" || row.TotalInstallments != tc.total || row.InstallmentNumber != 2 || row.Amount != 1100 || row.PaymentStatus != "paid" || row.IsSeries != (tc.rule != "" || row.TotalInstallments > 1) {
				t.Fatalf("real fetch error=%v row=%+v", err, row)
			}
			if !reflect.DeepEqual(before, tenantTransactionSnapshot(t, db)) {
				t.Fatal("fetch mutated financial state")
			}
			tenantScopeIndexedPlan(t, db, query, "tx-a", "scope-a")
		})
	}
	execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = 'rule-a' WHERE id = 'tx-a'`)
	missing := tenantScopeRows(t, db, `WITH recurring_rules AS (SELECT * FROM main.recurring_rules WHERE id != 'rule-a') `+query, "tx-a", "scope-a")
	if len(missing) != 1 || missing[0][12] != int64(3) {
		t.Fatalf("missing rule fallback=%v", missing)
	}
	if _, err := h.fetchTransactionRow("tx-b"); err == nil {
		t.Fatal("foreign transaction resolved")
	}
	h.WorkspaceID = "scope-b"
	if row, err := h.fetchTransactionRow("tx-b"); err != nil || row.TotalInstallments != 987 {
		t.Fatalf("same-tenant B fetch error=%v row=%+v", err, row)
	}
}

func TestTenantTransactionReadN17DynamicList(t *testing.T) {
	db := seedTenantTransactionRead(t)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, created_at, description, notes, status, recurring_rule_id, total_installments) VALUES ('tx-later', 'scope-a', 'scope-user', 'bank-a', 'cat-a', 'EXPENSE', 2200, ?, ?, 101, 'Scope purchase', 'Scope 123', 'paid', 'rule-a', 3)`, testUnixDate("2099-06-28"), testUnixDate("2099-06-29"))
	h := TransactionHandler{DB: db, WorkspaceID: "scope-a", UserID: "scope-user"}
	for _, rule := range []string{"rule-a", "rule-b", ""} {
		for _, tc := range []struct {
			name, account string
			filters       LancamentosFilters
			want          int
		}{
			{"default", "", LancamentosFilters{}, 2},
			{"combined", "bank-a", LancamentosFilters{Tipos: []string{"despesa"}, Situacoes: []string{"pago"}, OrigemIDs: []string{"bank-a", "bank-b"}, Categorias: []string{"cat-a", "cat-b"}, Busca: "Scope 123", Order: "desc"}, 2},
			{"foreign-account", "bank-b", LancamentosFilters{Situacoes: []string{"pago"}}, 0},
			{"destination", "", LancamentosFilters{Situacoes: []string{"pago"}, DestinoIDs: []string{"bank-a"}}, 0},
			{"fixed", "", LancamentosFilters{Tipos: []string{"fixo"}, Situacoes: []string{"pago"}}, map[bool]int{true: 2, false: 1}[rule != ""]},
		} {
			t.Run(rule+"/"+tc.name, func(t *testing.T) {
				execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = NULLIF(?, '') WHERE id = 'tx-a'`, rule)
				query, args := tenantTransactionListSQL(t, &h, tc.account, tc.filters)
				if tc.name == "combined" {
					start := time.Date(2099, 6, 1, 0, 0, 0, 0, time.UTC)
					want := []any{"scope-a", start.Unix(), start.AddDate(0, 1, 0).Add(-time.Second).Unix(), "bank-a", "bank-a", "bank-a", "bank-b", "bank-a", "bank-b", "cat-a", "cat-b"}
					for i := 0; i < 7; i++ {
						want = append(want, "%Scope 123%")
					}
					want = append(want, "%123%", "%123%")
					if !reflect.DeepEqual(args, want) {
						t.Fatalf("combined bind order=%v want=%v", args, want)
					}
				}
				before := tenantTransactionSnapshot(t, db)
				got := tenantScopeRows(t, db, query, args...)
				parent := tenantScopeRows(t, db, strings.Replace(query, transactionRuleScope, "", 1), args...)
				if len(got) != tc.want || len(parent) != tc.want {
					t.Fatalf("list rows/fan-out=%v want=%d", got, tc.want)
				}
				for i := range got {
					if got[i][0] == "tx-b" {
						t.Fatal("foreign transaction listed")
					}
					if got[i][0] == "tx-a" && rule == "rule-b" {
						if got[i][12] != int64(3) || parent[i][12] != int64(987) {
							t.Fatal("foreign metadata/fallback or negative control incorrect")
						}
						parent[i][12] = int64(3)
					}
				}
				if !reflect.DeepEqual(got, parent) {
					t.Fatal("parent list fields/order changed")
				}
				data, err := h.buildLancamentosData(tc.account, 6, 2099, tc.filters)
				if err != nil || len(data.Transactions) != tc.want {
					t.Fatalf("real list error=%v rows=%+v", err, data.Transactions)
				}
				for i, row := range data.Transactions {
					if row.ID == "tx-b" || (row.ID == "tx-a" && row.TotalInstallments != map[bool]int64{true: 7, false: 3}[rule == "rule-a"]) {
						t.Fatalf("real list metadata=%+v", row)
					}
					index := i
					if tc.filters.Order == "desc" {
						index = len(got) - 1 - i
					}
					if row.ID != got[index][0] {
						t.Fatal("real list sort contract changed")
					}
				}
				if !reflect.DeepEqual(before, tenantTransactionSnapshot(t, db)) {
					t.Fatal("list changed fixture financial state")
				}
				tenantScopeIndexedPlan(t, db, query, args...)
			})
		}
	}
	execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = 'rule-a' WHERE id = 'tx-a'`)
	query, args := tenantTransactionListSQL(t, &h, "", LancamentosFilters{Situacoes: []string{"pago"}})
	missing := tenantScopeRows(t, db, `WITH recurring_rules AS (SELECT * FROM main.recurring_rules WHERE id != 'rule-a') `+query, args...)
	if len(missing) != 2 || missing[0][12] != int64(3) || missing[1][12] != int64(3) {
		t.Fatalf("list missing-rule fallback=%v", missing)
	}
	h.WorkspaceID = "scope-b"
	data, err := h.buildLancamentosData("", 6, 2099, LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil || len(data.Transactions) != 1 || data.Transactions[0].ID != "tx-b" || data.Transactions[0].TotalInstallments != 987 {
		t.Fatalf("same-tenant B list error=%v data=%+v", err, data.Transactions)
	}
}
