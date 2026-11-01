package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

// Remove only the new ownership predicate for the parent negative control.
var invoiceMoveOwnership = regexp.MustCompile(`\s+AND EXISTS \(\s+SELECT 1 FROM accounts invoice_owner\s+WHERE invoice_owner.id = i.account_id AND invoice_owner.workspace_id = t.workspace_id\s+\)`)

func invoiceMoveParentSQL(t *testing.T, query string) string {
	t.Helper()
	if len(invoiceMoveOwnership.FindAllStringIndex(query, -1)) != 1 {
		t.Fatal("expected exactly one invoice ownership predicate")
	}
	return invoiceMoveOwnership.ReplaceAllString(query, "")
}

// Evaluate the final dynamic IN expression from production, including its
// placeholder count. Verify the production bind construction and Query call.
func invoiceMutabilitySQL(t *testing.T, h *TransactionHandler, ids []string) (string, []any) {
	t.Helper()
	ids = dedupeIDs(ids)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "transacoes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	printNode := func(n ast.Node) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, n); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(b.String()), "")
	}
	var evaluate func(ast.Expr) string
	evaluate = func(expr ast.Expr) string {
		switch x := expr.(type) {
		case *ast.BasicLit:
			value, err := strconv.Unquote(x.Value)
			if err != nil {
				t.Fatal(err)
			}
			return value
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				return evaluate(x.X) + evaluate(x.Y)
			}
		case *ast.CallExpr:
			if printNode(x) == "sqlPlaceholders(len(ids))" {
				return sqlPlaceholders(len(ids))
			}
		}
		t.Fatal("unsupported production dynamic SQL expression")
		return ""
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ensureTransactionsMutableByInvoiceStatusTx" {
			continue
		}
		var query string
		var binds []string
		deduped, queried := false, false
		for _, stmt := range fn.Body.List {
			printed := printNode(stmt)
			if printed == "ids=dedupeIDs(ids)" {
				deduped = true
			}
			if assign, ok := stmt.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
				name, ok := assign.Lhs[0].(*ast.Ident)
				if ok && name.Name == "query" {
					if query != "" || assign.Tok != token.DEFINE {
						t.Fatal("unexpected production query reassignment")
					}
					query = evaluate(assign.Rhs[0])
				}
				if ok && name.Name == "args" && assign.Tok == token.ASSIGN {
					binds = append(binds, printed)
				}
			}
			ast.Inspect(stmt, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if ok && printNode(call) == "tx.Query(query,args...)" {
					queried = true
				}
				return true
			})
		}
		if !deduped || !queried || !reflect.DeepEqual(binds, []string{"args=append(args,h.WorkspaceID)", "args=appendStrings(args,ids)"}) {
			t.Fatal("production dedupe/IN bind contract changed")
		}
		args := []any{h.WorkspaceID}
		for _, id := range ids {
			args = append(args, id)
		}
		if strings.Count(query, "?") != len(args) {
			t.Fatal("final SQL placeholder count differs from binds")
		}
		t.Logf("N18 final SQL=%s\nbinds=%v", query, args)
		return query, args
	}
	t.Fatal("N18 production function not found")
	return "", nil
}

func seedTenantInvoiceMove(t *testing.T) *sql.DB {
	t.Helper()
	db := seedTenantRuntimeScope(t)
	for _, suffix := range []string{"a", "b"} {
		execTestSQL(t, db, `UPDATE transactions SET invoice_id = ?, created_at = 10, updated_at = 100 WHERE id = ?`, "inv-"+suffix, "tx-"+suffix)
	}
	return db
}

func invoiceMoveReadOnly(t *testing.T, db *sql.DB, read func(*sql.Tx)) {
	t.Helper()
	snapshot := func() map[string][][]any {
		result := tenantTransactionSnapshot(t, db)
		result["invoice_payments"] = tenantScopeRows(t, db, "SELECT * FROM invoice_payments ORDER BY id")
		return result
	}
	before := snapshot()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	read(tx)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("invoice read changed financial snapshot")
	}
}

func TestTenantInvoiceMoveN18Mutability(t *testing.T) {
	db := seedTenantInvoiceMove(t)
	h := TransactionHandler{DB: db, WorkspaceID: "scope-a"}
	for _, invoice := range []string{"inv-a", "inv-b", ""} {
		for _, status := range []string{"OPEN", "CLOSED", "PAID"} {
			t.Run(invoice+"/"+status, func(t *testing.T) {
				execTestSQL(t, db, `UPDATE invoices SET status = ? WHERE id IN ('inv-a', 'inv-b')`, status)
				execTestSQL(t, db, `UPDATE transactions SET invoice_id = NULLIF(?, '') WHERE id = 'tx-a'`, invoice)
				query, args := invoiceMutabilitySQL(t, &h, []string{"tx-a"})
				got := tenantScopeRows(t, db, query, args...)
				parent := tenantScopeRows(t, db, invoiceMoveParentSQL(t, query), args...)
				if len(got) != 1 || len(parent) != 1 {
					t.Fatal("LEFT JOIN lost or duplicated own transaction")
				}
				var wantErr error
				if invoice == "inv-b" {
					if parent[0][5] != status || parent[0][6] != "scope-b" {
						t.Fatal("parent negative control did not expose B")
					}
					for i := 3; i <= 6; i++ {
						if got[0][i] != nil {
							t.Fatalf("foreign invoice column %d contributed", i)
						}
						parent[0][i] = nil
					}
					wantErr = errTransactionInvoiceLinkInvalid
				} else if invoice == "inv-a" && status == "PAID" {
					wantErr = errPaidInvoiceMutationBlocked
				}
				if !reflect.DeepEqual(got, parent) {
					t.Fatal("same-tenant/null or non-metadata columns changed")
				}
				invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
					if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-a"}); !errors.Is(err, wantErr) {
						t.Fatalf("guard=%v want=%v", err, wantErr)
					}
				})
				tenantScopeIndexedPlan(t, db, query, args...)
			})
		}
	}
	execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN'`)
	execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'inv-a' WHERE id = 'tx-a'`)
	for _, item := range []struct{ id, invoice string }{{"tx-null", ""}, {"tx-foreign", "inv-b"}} {
		execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, invoice_id) VALUES (?, 'scope-a', 'scope-user', 'card-a', 'EXPENSE', 2200, ?, 'Batch', 'paid', NULLIF(?, ''))`, item.id, testUnixDate("2099-06-26"), item.invoice)
	}
	ids := []string{"tx-foreign", "tx-null", "tx-a", " tx-a "}
	query, args := invoiceMutabilitySQL(t, &h, ids)
	if !reflect.DeepEqual(args, []any{"scope-a", "tx-foreign", "tx-null", "tx-a"}) {
		t.Fatal("batch transaction ID bind order changed")
	}
	for _, status := range []string{"OPEN", "CLOSED", "PAID"} {
		execTestSQL(t, db, `UPDATE invoices SET status = ? WHERE id = 'inv-b'`, status)
		got := tenantScopeRows(t, db, query, args...)
		parent := tenantScopeRows(t, db, invoiceMoveParentSQL(t, query), args...)
		if len(got) != 3 || len(parent) != 3 || got[0][0] != "tx-a" || got[1][0] != "tx-foreign" || got[2][0] != "tx-null" || parent[1][5] != status {
			t.Fatal("batch cardinality/order or negative control changed")
		}
		for i := 3; i <= 6; i++ {
			if got[1][i] != nil {
				t.Fatal("batch foreign invoice contributed")
			}
			parent[1][i] = nil
		}
		if !reflect.DeepEqual(got, parent) {
			t.Fatal("batch valid elements changed")
		}
		invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
			if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); !errors.Is(err, errTransactionInvoiceLinkInvalid) {
				t.Fatalf("batch structural guard=%v", err)
			}
		})
	}
	invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
		for _, ids := range [][]string{nil, {"tx-a", "tx-null"}} {
			if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
				t.Fatalf("valid/null batch=%v", err)
			}
		}
		for _, id := range []string{"missing", "tx-b"} {
			if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-a", id}); !errors.Is(err, errTransactionMutationTargetInvalid) {
				t.Fatalf("missing/foreign target=%v", err)
			}
		}
	})
	missing := tenantScopeRows(t, db, `WITH invoices AS (SELECT * FROM main.invoices WHERE id != 'inv-a') `+query, args...)
	if len(missing) != 3 || missing[0][2] != "inv-a" || missing[0][3] != nil || missing[0][5] != nil {
		t.Fatal("missing invoice did not null-extend")
	}
	tenantScopeIndexedPlan(t, db, query, args...)
	h.WorkspaceID = "scope-b"
	execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN' WHERE id = 'inv-b'`)
	invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
		if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-b"}); err != nil {
			t.Fatalf("same-tenant B=%v", err)
		}
	})
}

func TestTenantInvoiceMoveN19Candidate(t *testing.T) {
	db := seedTenantInvoiceMove(t)
	h := TransactionHandler{DB: db, WorkspaceID: "scope-a"}
	query := tenantScopeStatement(t, "transacoes.go", "fetchInvoiceMoveCandidateTx", "i.reference, i.status", "id", "h.WorkspaceID", "h.WorkspaceID", "models.AccountTypeCreditCard", "models.TransactionTypeExpense")
	for _, invoice := range []string{"inv-a", "inv-b", ""} {
		t.Run(invoice, func(t *testing.T) {
			execTestSQL(t, db, `UPDATE transactions SET invoice_id = NULLIF(?, '') WHERE id = 'tx-a'`, invoice)
			args := []any{"tx-a", "scope-a", "scope-a", models.AccountTypeCreditCard, models.TransactionTypeExpense}
			got := tenantScopeRows(t, db, query, args...)
			parent := tenantScopeRows(t, db, invoiceMoveParentSQL(t, query), args...)
			if invoice == "inv-a" && (len(got) != 1 || !reflect.DeepEqual(got, parent)) {
				t.Fatal("same-tenant candidate changed")
			}
			if invoice != "inv-a" && len(got) != 0 {
				t.Fatal("foreign/null candidate resolved")
			}
			if invoice == "inv-b" && (len(parent) != 1 || parent[0][2] != "inv-b") {
				t.Fatal("candidate negative control did not resolve B")
			}
			invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
				item, err := h.fetchInvoiceMoveCandidateTx(tx, "tx-a")
				if invoice == "inv-a" {
					if err != nil || item.id != "tx-a" || item.invoiceID != "inv-a" || item.reference != "2099-08" || item.updatedAt != 100 {
						t.Fatalf("real candidate=%+v err=%v", item, err)
					}
				} else if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("candidate absence=%v", err)
				}
			})
			tenantScopeIndexedPlan(t, db, query, args...)
		})
	}
	invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
		for _, id := range []string{"missing", "tx-b"} {
			if _, err := h.fetchInvoiceMoveCandidateTx(tx, id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("missing/foreign candidate=%v", err)
			}
		}
		h.WorkspaceID = "scope-b"
		if item, err := h.fetchInvoiceMoveCandidateTx(tx, "tx-b"); err != nil || item.invoiceID != "inv-b" {
			t.Fatalf("same-tenant B candidate=%+v err=%v", item, err)
		}
	})
	args := []any{"tx-a", "scope-a", "scope-a", models.AccountTypeCreditCard, models.TransactionTypeExpense}
	execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'inv-a' WHERE id = 'tx-a'`)
	if got := tenantScopeRows(t, db, `WITH invoices AS (SELECT * FROM main.invoices WHERE id != 'inv-a') `+query, args...); len(got) != 0 {
		t.Fatal("missing invoice candidate resolved")
	}
}

func TestTenantInvoiceMoveN20N21Queues(t *testing.T) {
	for _, recurring := range []bool{true, false} {
		name := "N21-installments"
		if recurring {
			name = "N20-recurring"
		}
		t.Run(name, func(t *testing.T) {
			db := seedTenantInvoiceMove(t)
			h := TransactionHandler{DB: db, WorkspaceID: "scope-a"}
			execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status) SELECT 'inv-a-closed', account_id, '2099-09', closing_date, due_date, 'CLOSED' FROM invoices WHERE id = 'inv-a'`)
			execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status) SELECT 'inv-a-paid', account_id, '2099-10', closing_date, due_date, 'PAID' FROM invoices WHERE id = 'inv-a'`)
			rule := ""
			if recurring {
				rule = "rule-a"
			}
			execTestSQL(t, db, `UPDATE transactions SET recurring_rule_id = NULLIF(?, ''), total_installments = 5, installment_number = 1, recurrence_sequence = 1 WHERE id = 'tx-a'`, rule)
			for i, invoice := range []string{"inv-a-closed", "inv-b", "", "inv-a-paid"} {
				parent := ""
				if !recurring {
					parent = "tx-a"
				}
				execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, created_at, updated_at, description, status, invoice_id, recurring_rule_id, parent_id, total_installments, installment_number, recurrence_sequence) VALUES (?, 'scope-a', 'scope-user', 'card-a', 'EXPENSE', 2200, ?, ?, 100, 'Queue', 'paid', NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), 5, ?, ?)`, "tx-a-"+strconv.Itoa(i+2), testUnixDate("2099-06-"+strconv.Itoa(i+26)), i+11, invoice, rule, parent, i+2, i+2)
			}
			current := invoiceMoveCandidate{id: "tx-a", accountID: "card-a", invoiceID: "inv-a", reference: "2099-08", totalInstallments: 5, date: testUnixDate("2099-06-25"), updatedAt: 100, recurringRuleID: sql.NullString{String: rule, Valid: recurring}}
			var query string
			args := []any{"scope-a", "scope-a", models.AccountTypeCreditCard, models.TransactionTypeExpense}
			if recurring {
				query = tenantScopeStatement(t, "transacoes.go", "invoiceMoveQueueLegacyTx", "AND t.recurring_rule_id = ?", "h.WorkspaceID", "h.WorkspaceID", "models.AccountTypeCreditCard", "models.TransactionTypeExpense", "current.recurringRuleID.String", "current.date")
				args = append(args, rule, current.date)
			} else {
				query = tenantScopeStatement(t, "transacoes.go", "invoiceMoveQueueLegacyTx", "AND (t.id = ? OR t.parent_id = ?)", "h.WorkspaceID", "h.WorkspaceID", "models.AccountTypeCreditCard", "models.TransactionTypeExpense", "rootID", "rootID", "current.date")
				args = append(args, "tx-a", "tx-a", current.date)
			}
			for _, invoice := range []string{"inv-a-closed", "inv-b"} {
				for _, status := range []string{"OPEN", "CLOSED", "PAID"} {
					t.Run(invoice+"/"+status, func(t *testing.T) {
						execTestSQL(t, db, `UPDATE transactions SET invoice_id = ? WHERE id = 'tx-a-3'`, invoice)
						execTestSQL(t, db, `UPDATE invoices SET status = ? WHERE id = 'inv-b'`, status)
						got := tenantScopeRows(t, db, query, args...)
						parent := tenantScopeRows(t, db, invoiceMoveParentSQL(t, query), args...)
						want := []string{"tx-a", "tx-a-2"}
						if invoice == "inv-a-closed" {
							want = append(want, "tx-a-3")
							if !reflect.DeepEqual(got, parent) {
								t.Fatal("same-tenant queue fields/order changed")
							}
						} else if status != "PAID" {
							if len(parent) != 3 || parent[2][2] != "inv-b" {
								t.Fatal("queue negative control did not expose B")
							}
							parent = parent[:2]
						}
						if len(got) != len(want) || !reflect.DeepEqual(got, parent) {
							t.Fatal("own queue/cardinality changed")
						}
						for i, row := range got {
							if row[0] != want[i] || row[2] == "inv-b" {
								t.Fatal("queue ordering or invoice isolation changed")
							}
						}
						invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
							items, err := h.invoiceMoveQueueTx(tx, current, "")
							if err != nil || len(items) != len(got) {
								t.Fatalf("real queue=%+v err=%v", items, err)
							}
							for i, item := range items {
								if !reflect.DeepEqual([]any{item.id, item.accountID, item.invoiceID, item.reference, item.updatedAt}, got[i]) {
									t.Fatal("real queue differs from production SQL")
								}
							}
						})
						tenantScopeIndexedPlan(t, db, query, args...)
					})
				}
			}
			missing := tenantScopeRows(t, db, `WITH invoices AS (SELECT * FROM main.invoices WHERE id != 'inv-a-closed') `+query, args...)
			if len(missing) != 1 || missing[0][0] != "tx-a" {
				t.Fatal("missing/null/paid/foreign queue invoice contributed")
			}
			// Later starts preserve the preexisting date filter and installment root.
			current.date = testUnixDate("2099-06-26")
			if !recurring {
				current.id = "tx-a-2"
				current.parentID = sql.NullString{String: "tx-a", Valid: true}
			}
			invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
				items, err := h.invoiceMoveQueueTx(tx, current, "")
				if err != nil || len(items) != 1 || items[0].id != "tx-a-2" {
					t.Fatalf("future queue/root filter=%+v err=%v", items, err)
				}
			})
			// Symmetric B/B and B/A controls also exercise the SQL branches.
			bRule := ""
			if recurring {
				bRule = "rule-b"
			}
			execTestSQL(t, db, `UPDATE transactions SET total_installments = 5, recurring_rule_id = NULLIF(?, '') WHERE id = 'tx-b'`, bRule)
			execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN' WHERE id = 'inv-b'`)
			h.WorkspaceID = "scope-b"
			bCurrent := invoiceMoveCandidate{id: "tx-b", accountID: "card-b", invoiceID: "inv-b", reference: "2099-08", totalInstallments: 5, date: testUnixDate("2099-06-25"), recurringRuleID: sql.NullString{String: bRule, Valid: recurring}}
			invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
				items, err := h.invoiceMoveQueueTx(tx, bCurrent, "")
				if err != nil || len(items) != 1 || items[0].id != "tx-b" || items[0].invoiceID != "inv-b" {
					t.Fatalf("same-tenant B queue=%+v err=%v", items, err)
				}
			})
			execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'inv-a' WHERE id = 'tx-b'`)
			invoiceMoveReadOnly(t, db, func(tx *sql.Tx) {
				items, err := h.invoiceMoveQueueTx(tx, bCurrent, "")
				if err != nil || len(items) != 0 {
					t.Fatalf("B/A queue contribution=%+v err=%v", items, err)
				}
			})
		})
	}
}
