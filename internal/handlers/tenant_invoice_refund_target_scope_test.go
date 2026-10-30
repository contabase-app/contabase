package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

const refundTargetInvoiceOwnership = ` AND EXISTS (
			SELECT 1 FROM accounts invoice_owner
			WHERE invoice_owner.id = i.account_id AND invoice_owner.workspace_id = ?
		)`

// Capture the final SQL and actual binds by calling production itself. Probes
// shadow only read relations; the migrated schema and its guards stay intact.
type refundTargetProbe struct {
	t     *testing.T
	query interface {
		QueryRow(string, ...interface{}) *sql.Row
	}
	prefix string
	parent bool
	sql    string
	args   []interface{}
	calls  int
}

func (p *refundTargetProbe) QueryRow(query string, args ...interface{}) *sql.Row {
	p.t.Helper()
	p.calls++
	if strings.Count(query, refundTargetInvoiceOwnership) != 1 || strings.Count(query, "?") != 5 || len(args) != 5 {
		p.t.Fatalf("unexpected final ownership/placeholder/bind contract: %s %v", query, args)
	}
	if args[0] != args[2] || args[3] != models.TransactionTypeExpense || args[4] != models.AccountTypeCreditCard {
		p.t.Fatalf("production bind order changed: %v", args)
	}
	p.sql, p.args = query, append([]interface{}(nil), args...)
	if p.parent {
		query = strings.Replace(query, refundTargetInvoiceOwnership, "", 1)
		args = args[1:]
	}
	return p.query.QueryRow(p.prefix+query, args...)
}

func refundTargetRead(t *testing.T, db interface {
	QueryRow(string, ...interface{}) *sql.Row
}, workspace, id, prefix string, parent bool) (transactionRefundTarget, error, *refundTargetProbe) {
	t.Helper()
	probe := &refundTargetProbe{t: t, query: db, prefix: prefix, parent: parent}
	target, err := loadTransactionRefundTarget(probe, workspace, id)
	if probe.calls != 1 || !reflect.DeepEqual(probe.args, []interface{}{workspace, id, workspace, models.TransactionTypeExpense, models.AccountTypeCreditCard}) {
		t.Fatalf("expected one production query with exact binds: calls=%d args=%v", probe.calls, probe.args)
	}
	return target, err, probe
}

func refundTargetSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	snapshot := tenantTransactionSnapshot(t, db)
	snapshot["invoice_payments"] = tenantScopeRows(t, db, "SELECT * FROM invoice_payments ORDER BY id")
	snapshot["schema"] = tenantScopeRows(t, db, "SELECT type, name, tbl_name, sql FROM sqlite_schema ORDER BY type, name")
	return snapshot
}

func TestTenantInvoiceRefundTargetN22Ownership(t *testing.T) {
	db := seedTenantInvoiceMove(t)
	for _, own := range []string{"a", "b"} {
		other := "b"
		if own == "b" {
			other = "a"
		}
		for _, tc := range []struct{ name, invoice string }{
			{"same", "inv-" + own}, {"foreign", "inv-" + other}, {"null", ""},
		} {
			t.Run(own+"/"+tc.name, func(t *testing.T) {
				id, workspace := "tx-"+own, "scope-"+own
				execTestSQL(t, db, `UPDATE transactions SET invoice_id = NULLIF(?, '') WHERE id = ?`, tc.invoice, id)
				before := refundTargetSnapshot(t, db)
				got, err, probe := refundTargetRead(t, db, workspace, id, "", false)
				parent, parentErr, _ := refundTargetRead(t, db, workspace, id, "", true)
				switch tc.name {
				case "same":
					want := transactionRefundTarget{TransactionID: id, Description: "Scope purchase", InvoiceID: tc.invoice, InvoiceStatus: "OPEN", Amount: map[string]int64{"a": 1100, "b": 7700}[own]}
					if err != nil || parentErr != nil || got != want || got != parent {
						t.Fatalf("same-tenant full target=%+v/%v parent=%+v/%v", got, err, parent, parentErr)
					}
				case "foreign":
					if !errors.Is(err, sql.ErrNoRows) || got != (transactionRefundTarget{}) || parentErr != nil || parent.InvoiceID != tc.invoice {
						t.Fatalf("cross-tenant contribution=%+v/%v parent=%+v/%v", got, err, parent, parentErr)
					}
				case "null":
					if !errors.Is(err, sql.ErrNoRows) || !errors.Is(parentErr, sql.ErrNoRows) || got != parent || got != (transactionRefundTarget{}) {
						t.Fatalf("NULL inner-join semantics changed: %+v/%v parent=%+v/%v", got, err, parent, parentErr)
					}
				}
				if !reflect.DeepEqual(before, refundTargetSnapshot(t, db)) {
					t.Fatal("target read changed financial state or schema")
				}
				tenantScopeIndexedPlan(t, db, probe.sql, probe.args...)
			})
		}
	}
	for _, tc := range []struct{ workspace, id string }{
		{"scope-a", "tx-b"}, {"scope-b", "tx-a"}, {"", "tx-a"}, {"missing", "tx-a"}, {"scope-a", "missing"},
	} {
		if got, err, _ := refundTargetRead(t, db, tc.workspace, tc.id, "", false); !errors.Is(err, sql.ErrNoRows) || got != (transactionRefundTarget{}) {
			t.Fatalf("foreign/unknown root resolved: %v %+v/%v", tc, got, err)
		}
	}
}

func TestTenantInvoiceRefundTargetN22MissingAndOwnerProbes(t *testing.T) {
	db := seedTenantInvoiceMove(t)
	for _, tc := range []struct {
		name, prefix string
		parentRow    bool
	}{
		{"missing-invoice", `WITH invoices AS (SELECT * FROM main.invoices WHERE id != 'inv-a') `, false},
		{"missing-owner", `WITH invoices AS (SELECT id, 'missing' AS account_id, status FROM main.invoices) `, true},
		{"foreign-owner", `WITH invoices AS (SELECT id, 'card-b' AS account_id, status FROM main.invoices) `, true},
		{"missing-transaction-account", `WITH accounts AS (SELECT * FROM main.accounts WHERE id != 'card-a') `, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := refundTargetSnapshot(t, db)
			got, err, _ := refundTargetRead(t, db, "scope-a", "tx-a", tc.prefix, false)
			parent, parentErr, _ := refundTargetRead(t, db, "scope-a", "tx-a", tc.prefix, true)
			if !errors.Is(err, sql.ErrNoRows) || got != (transactionRefundTarget{}) {
				t.Fatalf("unresolved invoice/owner produced success: %+v/%v", got, err)
			}
			if tc.parentRow {
				if parentErr != nil || parent.InvoiceID != "inv-a" || parent.InvoiceStatus != "OPEN" {
					t.Fatalf("parent negative control failed: %+v/%v", parent, parentErr)
				}
			} else if !errors.Is(parentErr, sql.ErrNoRows) || parent != got {
				t.Fatalf("preexisting structural error changed: %+v/%v", parent, parentErr)
			}
			if !reflect.DeepEqual(before, refundTargetSnapshot(t, db)) {
				t.Fatal("probe changed financial state, constraints, or triggers")
			}
		})
	}
	// N22 proves workspace ownership, without adding a new same-account rule.
	execTestSQL(t, db, `INSERT INTO accounts(id, workspace_id, name, type) VALUES ('other-a', 'scope-a', 'Other card', 'CREDIT_CARD')`)
	prefix := `WITH invoices AS (SELECT id, 'other-a' AS account_id, status FROM main.invoices) `
	got, err, _ := refundTargetRead(t, db, "scope-a", "tx-a", prefix, false)
	parent, parentErr, _ := refundTargetRead(t, db, "scope-a", "tx-a", prefix, true)
	if err != nil || parentErr != nil || got != parent {
		t.Fatalf("same-workspace owner contract changed: %+v/%v parent=%+v/%v", got, err, parent, parentErr)
	}
}

func TestTenantInvoiceRefundTargetN22RefundStateAndPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		active []int64
	}{
		{"none", nil}, {"partial", []int64{100}}, {"multiple", []int64{100, 200}}, {"full", []int64{1100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := seedTenantInvoiceMove(t)
			execTestSQL(t, db, `INSERT INTO transaction_refunds(id, workspace_id, transaction_id, amount_cents, description, created_by) VALUES ('reversed', 'scope-a', 'tx-a', 50, 'Reversed', 'scope-user')`)
			execTestSQL(t, db, `UPDATE transaction_refunds SET reversed_at = 1, reversed_by = 'scope-user' WHERE id = 'reversed'`)
			var refunded int64
			for n, amount := range tc.active {
				execTestSQL(t, db, `INSERT INTO transaction_refunds(id, workspace_id, transaction_id, amount_cents, description, created_by) VALUES (?, 'scope-a', 'tx-a', ?, 'Active', 'scope-user')`, fmt.Sprintf("active-%d", n), amount)
				refunded += amount
			}
			for _, status := range []string{"OPEN", "CLOSED", "PAID"} {
				execTestSQL(t, db, `UPDATE invoices SET status = ? WHERE id = 'inv-a'`, status)
				got, err, probe := refundTargetRead(t, db, "scope-a", "tx-a", "", false)
				parent, parentErr, _ := refundTargetRead(t, db, "scope-a", "tx-a", "", true)
				want := transactionRefundTarget{"tx-a", "Scope purchase", "inv-a", status, 1100, refunded}
				if err != nil || parentErr != nil || got != want || got != parent || transactionRefundFormData(got, activeTransactionRefund{}) != transactionRefundFormData(parent, activeTransactionRefund{}) {
					t.Fatalf("refund calculation/target/form changed: %+v/%v parent=%+v/%v", got, err, parent, parentErr)
				}
				plan := tenantScopeRows(t, db, "EXPLAIN QUERY PLAN "+probe.sql, probe.args...)
				for _, alias := range []string{"t", "i", "a", "invoice_owner", "r"} {
					indexed := false
					for _, row := range plan {
						detail := fmt.Sprint(row[3])
						if strings.Contains(detail, "SEARCH "+alias+" USING") {
							indexed = true
							t.Log(detail)
						}
					}
					if !indexed {
						t.Fatalf("no indexed search for %s: %v", alias, plan)
					}
				}
				tenantScopeIndexedPlan(t, db, probe.sql, probe.args...)
				if status == "OPEN" {
					t.Logf("N22 final SQL=%s\nbinds=%v", probe.sql, probe.args)
				}
			}
		})
	}
}

func TestTenantInvoiceRefundTargetN22Errors(t *testing.T) {
	db := seedTenantInvoiceMove(t)
	for _, tc := range []struct{ name, prefix, message string }{
		{"query", `WITH invoices AS (SELECT nonexistent_column FROM main.invoices) `, "no such column"},
		{"scan", `WITH invoices AS (SELECT id, account_id, NULL AS status FROM main.invoices) `, "converting NULL to string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err, _ := refundTargetRead(t, db, "scope-a", "tx-a", tc.prefix, false)
			parent, parentErr, _ := refundTargetRead(t, db, "scope-a", "tx-a", tc.prefix, true)
			if err == nil || parentErr == nil || !strings.Contains(err.Error(), tc.message) || err.Error() != parentErr.Error() || got != parent {
				t.Fatalf("Query/Scan error contract changed: %+v/%v parent=%+v/%v", got, err, parent, parentErr)
			}
		})
	}
}

func TestTenantInvoiceRefundTargetN22RealFlow(t *testing.T) {
	for _, invoice := range []string{"inv-a", "inv-b", ""} {
		t.Run("invoice="+invoice, func(t *testing.T) {
			db := seedTenantInvoiceMove(t)
			execTestSQL(t, db, `UPDATE transactions SET invoice_id = NULLIF(?, '') WHERE id = 'tx-a'`, invoice)
			before := refundTargetSnapshot(t, db)
			h := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "scope-a", UserID: "scope-user"}
			rr := httptest.NewRecorder()
			h.HandleRegistrarReembolso(rr, formPost("/transacoes/tx-a/reembolso", url.Values{"valor": {"3,00"}, "descricao": {"Partial refund"}}), "tx-a")
			if invoice != "inv-a" {
				if rr.Code != http.StatusConflict || rr.Header().Get("HX-Trigger") != "" || !reflect.DeepEqual(before, refundTargetSnapshot(t, db)) {
					t.Fatalf("foreign/NULL flow mutated state: status=%d body=%q", rr.Code, rr.Body.String())
				}
				if invoice == "" && !strings.Contains(rr.Body.String(), "apenas despesas de cartão") {
					t.Fatal("NULL did not reach N22 structural rejection")
				}
				// The caller must release its transaction on target/pre-guard error.
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("same-tenant create status=%d body=%q", rr.Code, rr.Body.String())
			}
			got, err := loadTransactionRefundTarget(db, "scope-a", "tx-a")
			if err != nil || got != (transactionRefundTarget{"tx-a", "Scope purchase", "inv-a", "OPEN", 1100, 300}) {
				t.Fatalf("post-refund target=%+v/%v", got, err)
			}
			rows := tenantScopeRows(t, db, `SELECT workspace_id, transaction_id, amount_cents, description, reversed_at FROM transaction_refunds ORDER BY id`)
			if !reflect.DeepEqual(rows, [][]any{{"scope-a", "tx-a", int64(300), "Partial refund", nil}}) {
				t.Fatalf("refund mutation payload=%v", rows)
			}
			snapshot, err := loadInvoiceFinancialSnapshotDB(db, "scope-a", "inv-a")
			// Parent and L8 both materialize rule-a/sequence 1 during the OPEN
			// invoice OOB read: purchase net 800 + recurring expense 2300.
			baseAmount := got.Amount - got.Refunded
			const recurringMaterializedAmount int64 = 2300
			expectedTotal := baseAmount + recurringMaterializedAmount
			if err != nil || snapshot.Total != expectedTotal {
				t.Fatalf("invoice net=%d/%v want=%d + %d = %d", snapshot.Total, err, baseAmount, recurringMaterializedAmount, expectedTotal)
			}
			materialized := tenantScopeRows(t, db, `
				SELECT workspace_id, user_id, account_id, category_id, invoice_id,
				       type, amount, date, recurring_rule_id, recurrence_sequence,
				       series_id, status, payment_status, installment_number,
				       total_installments, parent_id, destination_account_id
				FROM transactions WHERE id NOT IN ('tx-a', 'tx-b') ORDER BY id
			`)
			wantMaterialized := [][]any{{
				"scope-a", "scope-user", "card-a", "cat-a", "inv-a",
				"EXPENSE", recurringMaterializedAmount, testUnixDate("2099-06-26"), "rule-a", int64(1),
				"rule-a", "paid", "PAID", int64(1), int64(1), nil, nil,
			}}
			if !reflect.DeepEqual(materialized, wantMaterialized) {
				t.Fatalf("expected exactly the parent recurring occurrence: %v want=%v", materialized, wantMaterialized)
			}
			// Preserve every original transaction column while accounting for
			// the one additional row explicitly checked above, never dropping it.
			originals := tenantScopeRows(t, db, `SELECT * FROM transactions WHERE id IN ('tx-a', 'tx-b') ORDER BY id`)
			if !reflect.DeepEqual(before["transactions"], originals) {
				t.Fatal("refund flow changed an original transaction")
			}
			after := refundTargetSnapshot(t, db)
			if len(after["transactions"]) != len(before["transactions"])+1 {
				t.Fatal("refund flow materialized an unexpected transaction count")
			}
			for _, key := range []string{"invoice_payments", "schema", "accounts", "boxes", "box_virtual_ledger"} {
				if !reflect.DeepEqual(before[key], after[key]) {
					t.Fatalf("refund flow changed %s", key)
				}
			}
			if b, err := loadTransactionRefundTarget(db, "scope-b", "tx-b"); err != nil || b.Refunded != 0 || b.Amount != 7700 || b.InvoiceID != "inv-b" {
				t.Fatalf("same-tenant refund affected B: %+v/%v", b, err)
			}
		})
	}
}
