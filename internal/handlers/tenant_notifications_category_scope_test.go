package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

const notificationCategoryOwnership = " AND c.workspace_id = cl.workspace_id"

// Evaluate the production concatenation, including its real financial and
// invoice-payment helpers. Verify the actual month expressions and bind order;
// a fixed clock input makes the SQL evidence independent of the test-run date.
func notificationCategorySQL(t *testing.T, workspace string, now time.Time) (string, []any) {
	t.Helper()
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "notificacoes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	printNode := func(n ast.Node) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, fs, n); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(b.String()), "")
	}
	var eval func(ast.Expr) string
	eval = func(expr ast.Expr) string {
		switch e := expr.(type) {
		case *ast.BasicLit:
			if e.Kind == token.STRING {
				v, err := strconv.Unquote(e.Value)
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
		case *ast.Ident:
			if e.Name == "transactionNetAmountTSQL" {
				return transactionNetAmountTSQL
			}
		case *ast.BinaryExpr:
			if e.Op == token.ADD {
				return eval(e.X) + eval(e.Y)
			}
		case *ast.CallExpr:
			if printNode(e) == `excludeInvoicePaymentCompetenceClause("t")` {
				return excludeInvoicePaymentCompetenceClause("t")
			}
		}
		t.Fatal("unsupported production notification SQL expression")
		return ""
	}
	var queries []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "exceededCostLimits" {
			continue
		}
		if len(fn.Body.List) < 5 {
			t.Fatal("production month construction missing")
		}
		for i, want := range []string{
			"monthStartDate:=appMonthStartUTC(time.Now())",
			"monthStart:=monthStartDate.Unix()",
			"nextMonth:=monthStartDate.AddDate(0,1,0).Unix()",
		} {
			if printNode(fn.Body.List[i+1]) != want {
				t.Fatal("production month window changed")
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || printNode(selector.X) != "h.DB" {
				return true
			}
			if selector.Sel.Name != "Query" || len(call.Args) != 5 {
				t.Fatal("production read count or query contract changed")
			}
			var binds []string
			for _, bind := range call.Args[1:] {
				binds = append(binds, printNode(bind))
			}
			if !reflect.DeepEqual(binds, []string{"models.TransactionTypeExpense", "monthStart", "nextMonth", "h.WorkspaceID"}) {
				t.Fatalf("production binds changed: %v", binds)
			}
			queries = append(queries, eval(call.Args[0]))
			return true
		})
	}
	if len(queries) != 1 || strings.Count(queries[0], "?") != 4 || strings.Count(queries[0], notificationCategoryOwnership) != 1 {
		t.Fatal("expected one production query, four placeholders and one category predicate")
	}
	start := appMonthStartUTC(now)
	args := []any{models.TransactionTypeExpense, start.Unix(), start.AddDate(0, 1, 0).Unix(), workspace}
	t.Logf("N34 final SQL=%s\nbinds=%v; QUERY COUNT=1", queries[0], args)
	return queries[0], args
}

func notificationCategoryParentSQL(t *testing.T, query string) string {
	t.Helper()
	if strings.Count(query, notificationCategoryOwnership) != 1 {
		t.Fatal("negative control must remove exactly the L9 predicate")
	}
	return strings.Replace(query, notificationCategoryOwnership, "", 1)
}

func seedNotificationCategoryScope(t *testing.T, now time.Time) *sql.DB {
	t.Helper()
	db := seedTenantRuntimeScope(t)
	start := appMonthStartUTC(now)
	date := start.AddDate(0, 0, 14).Unix()
	end := start.AddDate(0, 1, 0).Unix()
	if !(start.Unix() <= date && date < end) {
		t.Fatal("financial fixture transaction outside production month window")
	}
	t.Logf("MONTH_START=%s (%d); MONTH_END=%s (%d); TRANSACTION_DATE=%s (%d); INSIDE=true",
		start.Format(time.RFC3339), start.Unix(), time.Unix(end, 0).UTC().Format(time.RFC3339), end, time.Unix(date, 0).UTC().Format(time.RFC3339), date)
	execTestSQL(t, db, `UPDATE categories SET name='OWNED A', color='emerald', icon='house' WHERE id='cat-a'`)
	execTestSQL(t, db, `UPDATE categories SET name='FOREIGN B', color='violet', icon='car' WHERE id='cat-b'`)
	execTestSQL(t, db, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES
		('limit-a','scope-a','cat-a',10000),('limit-b','scope-b','cat-b',1000)`)
	execTestSQL(t, db, `UPDATE transactions SET category_id='cat-a',invoice_id='inv-a',amount=25000,date=?,updated_at=111 WHERE id='tx-a'`, date)
	execTestSQL(t, db, `UPDATE transactions SET category_id='cat-b',date=?,updated_at=333 WHERE id='tx-b'`, date)
	for _, r := range []struct {
		id     string
		amount int64
	}{{"active-one", 5000}, {"active-two", 2500}, {"reversed", 1000}} {
		execTestSQL(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES(?,'scope-a','tx-a',?,'Refund','scope-user')`, r.id, r.amount)
	}
	execTestSQL(t, db, `UPDATE transaction_refunds SET reversed_at=1,reversed_by='scope-user' WHERE id='reversed'`)
	for _, tx := range []struct {
		id, workspace, account, kind, status string
		amount, date, updated                int64
	}{
		{"extra-a", "scope-a", "bank-a", "EXPENSE", "paid", 2000, start.Unix(), 222},
		{"before", "scope-a", "bank-a", "EXPENSE", "paid", 100000, start.Unix() - 1, 999},
		{"end", "scope-a", "bank-a", "EXPENSE", "paid", 100000, end, 999},
		{"pending", "scope-a", "bank-a", "EXPENSE", "pending", 100000, date, 999},
		{"income", "scope-a", "bank-a", "INCOME", "paid", 100000, date, 999},
		{"foreign-workspace", "scope-b", "bank-b", "EXPENSE", "paid", 100000, date, 999},
		{"payment-a", "scope-a", "bank-a", "EXPENSE", "paid", 100000, date, 999},
		{"reversal-a", "scope-a", "bank-a", "EXPENSE", "paid", 100000, date, 999},
	} {
		execTestSQL(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,category_id,type,status,amount,date,updated_at,description) VALUES(?,?,'scope-user',?,'cat-a',?,?,?,?,?,'Notification scope')`, tx.id, tx.workspace, tx.account, tx.kind, tx.status, tx.amount, tx.date, tx.updated)
	}
	execTestSQL(t, db, `INSERT INTO invoice_payments(id,workspace_id,invoice_id,account_id,transaction_id,reversal_transaction_id,amount_cents,paid_at,created_at) VALUES
		('payment-link-a','scope-a','inv-a','bank-a','payment-a','reversal-a',100000,?,1),
		('foreign-link-b','scope-b','inv-b','bank-b','extra-a',NULL,2000,?,1)`, date, date)
	return db
}

func notificationCategorySnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	snapshot := refundTargetSnapshot(t, db)
	for _, table := range []string{"categories", "cost_limits"} {
		snapshot[table] = tenantScopeRows(t, db, "SELECT * FROM "+table+" ORDER BY id")
	}
	return snapshot
}

func TestTenantNotificationsCategoryN34OwnershipAndFinancialContract(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	db := seedNotificationCategoryScope(t, now)
	query, args := notificationCategorySQL(t, "scope-a", now)
	parent := notificationCategoryParentSQL(t, query)
	before := notificationCategorySnapshot(t, db)
	want := [][]any{{"limit-a", "OWNED A", int64(10000), int64(19500), int64(222)}}
	got, old := tenantScopeRows(t, db, query, args...), tenantScopeRows(t, db, parent, args...)
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, old) {
		t.Fatalf("same-tenant fields/cardinality/financial values changed: after=%v before=%v want=%v", got, old, want)
	}
	t.Log("SPENT BEFORE=19500; SPENT AFTER=19500; LIMIT BEFORE=10000; LIMIT AFTER=10000; EXCEEDED BEFORE=9500; EXCEEDED AFTER=9500")
	for _, clause := range []string{
		"HAVING spent > cl.max_amount_monthly", "ORDER BY spent - cl.max_amount_monthly DESC", "LIMIT 8",
		"AND t.type = ?", "AND t.status = 'paid'", "AND t.date >= ? AND t.date < ?",
		excludeInvoicePaymentCompetenceClause("t"), transactionNetAmountTSQL,
	} {
		if strings.Count(query, clause) != 1 {
			t.Fatalf("production contract missing or duplicated: %s", clause)
		}
	}
	// Each financial exclusion has an independent sensitivity control.
	for _, control := range []struct {
		clause string
		spent  int64
	}{
		{"AND t.date >= ? AND t.date < ?", 219500},
		{"AND t.status = 'paid'", 119500},
		{"AND t.type = ?", 119500},
		{"AND " + excludeInvoicePaymentCompetenceClause("t"), 219500},
		{"ip.workspace_id = t.workspace_id AND ", 17500},
	} {
		binds := append([]any(nil), args...)
		if control.clause == "AND t.date >= ? AND t.date < ?" {
			binds = []any{args[0], args[3]}
		} else if control.clause == "AND t.type = ?" {
			binds = args[1:]
		}
		rows := tenantScopeRows(t, db, strings.Replace(query, control.clause, "", 1), binds...)
		if len(rows) != 1 || rows[0][3] != control.spent {
			t.Fatalf("independent exclusion control %s: %v want spent=%d", control.clause, rows, control.spent)
		}
	}
	if !reflect.DeepEqual(before, notificationCategorySnapshot(t, db)) {
		t.Fatal("notification reads changed financial state or schema")
	}
	// Keep paid A transactions in the queried month so only ownership can deny
	// the adversarial limit. No constraint, trigger or schema is disabled.
	execTestSQL(t, db, `UPDATE cost_limits SET category_id='cat-b' WHERE id='limit-a'`)
	execTestSQL(t, db, `UPDATE transactions SET category_id='cat-b' WHERE id IN ('tx-a','extra-a')`)
	before = notificationCategorySnapshot(t, db)
	old = tenantScopeRows(t, db, parent, args...)
	if !reflect.DeepEqual(old, [][]any{{"limit-a", "FOREIGN B", int64(10000), int64(19500), int64(222)}}) {
		t.Fatalf("independent parent control did not resolve B: %v", old)
	}
	if got := tenantScopeRows(t, db, query, args...); len(got) != 0 {
		t.Fatalf("cross-tenant category contribution: %v", got)
	}
	t.Log("PARENT CATEGORY B RESOLVED=true; CROSS-TENANT CATEGORY CONTRIBUTION=0")
	bArgs := append([]any(nil), args...)
	bArgs[3] = "scope-b"
	bWant := [][]any{{"limit-b", "FOREIGN B", int64(1000), int64(7700), int64(333)}}
	if got := tenantScopeRows(t, db, query, bArgs...); !reflect.DeepEqual(got, bWant) || !reflect.DeepEqual(got, tenantScopeRows(t, db, parent, bArgs...)) {
		t.Fatalf("B/B metadata/financial result changed: %v", got)
	}
	if !reflect.DeepEqual(before, notificationCategorySnapshot(t, db)) {
		t.Fatal("adversarial reads changed financial state or schema")
	}
}

func TestTenantNotificationsCategoryN34HavingOrderLimit(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	db := seedNotificationCategoryScope(t, now)
	date := appMonthStartUTC(now).AddDate(0, 0, 14).Unix()
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("order-%d", i)
		execTestSQL(t, db, `INSERT INTO categories(id,workspace_id,name,type) VALUES(?,'scope-a',?,'EXPENSE')`, id, id)
		execTestSQL(t, db, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES(?,'scope-a',?,10000)`, id, id)
		execTestSQL(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,category_id,type,status,amount,date,updated_at,description) VALUES(?,'scope-a','scope-user','bank-a',?,'EXPENSE','paid',?,?,444,'Order')`, id, id, 10000+i*2000, date)
	}
	query, args := notificationCategorySQL(t, "scope-a", now)
	got := tenantScopeRows(t, db, query, args...)
	if len(got) != 8 || !reflect.DeepEqual(got, tenantScopeRows(t, db, notificationCategoryParentSQL(t, query), args...)) {
		t.Fatalf("same-tenant LIMIT/cardinality changed: %v", got)
	}
	wantIDs := []string{"order-9", "order-8", "order-7", "order-6", "order-5", "limit-a", "order-4", "order-3"}
	for i, row := range got {
		if row[0] != wantIDs[i] {
			t.Fatalf("exceeded-amount ordering changed at %d: %v", i, got)
		}
	}
	// Remove truncation in a read-only probe: HAVING denies below/equal limits.
	for _, amount := range []int64{9999, 10000, 10001} {
		execTestSQL(t, db, `UPDATE transactions SET amount=? WHERE id='order-0'`, amount)
		probe := strings.Replace(query, "WHERE cl.workspace_id = ?", "WHERE cl.workspace_id = ? AND cl.id = 'order-0'", 1)
		rows := tenantScopeRows(t, db, probe, args...)
		if amount <= 10000 && len(rows) != 0 || amount == 10001 && (len(rows) != 1 || rows[0][3] != amount) {
			t.Fatalf("HAVING boundary spent=%d rows=%v", amount, rows)
		}
	}
}

func TestTenantNotificationsCategoryN34RealFunction(t *testing.T) {
	// Production owns its clock. Deliberately align this smoke fixture with its
	// current month; deterministic financial evidence is in the fixed-date test.
	now := time.Now()
	db := seedNotificationCategoryScope(t, now)
	h := NotificacoesHandler{DB: db, WorkspaceID: "scope-a", UserID: "scope-user"}
	before := notificationCategorySnapshot(t, db)
	query, args := notificationCategorySQL(t, h.WorkspaceID, now)
	if rows := tenantScopeRows(t, db, query, args...); len(rows) != 1 || rows[0][3] != int64(19500) {
		t.Fatalf("live-month SQL financial fixture=%v", rows)
	}
	want := []NotificationItem{{Key: "limit:limit-a", Title: "Limite de categoria estourado", Description: "OWNED A · limite " + formatCurrencyLabel(10000), Money: MoneyMinor(9500), Icon: "gauge", Color: "red", CreatedAt: 222}}
	for _, business := range []bool{false, true} {
		if got := h.exceededCostLimits(business); !reflect.DeepEqual(got, want) {
			t.Fatalf("real same-tenant notifications=%+v want=%+v", got, want)
		}
	}
	if !reflect.DeepEqual(before, notificationCategorySnapshot(t, db)) {
		t.Fatal("real notification read changed financial state or schema")
	}
	execTestSQL(t, db, `UPDATE cost_limits SET category_id='cat-b' WHERE id='limit-a'`)
	execTestSQL(t, db, `UPDATE transactions SET category_id='cat-b' WHERE id IN ('tx-a','extra-a')`)
	if rows := tenantScopeRows(t, db, notificationCategoryParentSQL(t, query), args...); len(rows) != 1 || rows[0][1] != "FOREIGN B" {
		t.Fatal("real-function foreign control masked by another filter")
	}
	for _, business := range []bool{false, true} {
		if got := h.exceededCostLimits(business); len(got) != 0 {
			t.Fatalf("real notification exposed foreign metadata: %+v", got)
		}
	}
}

// Run separately, only after the behavioral focused gate passes.
func TestTenantNotificationsCategoryN34QueryPlan(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	db := seedNotificationCategoryScope(t, now)
	query, args := notificationCategorySQL(t, "scope-a", now)
	parent := notificationCategoryParentSQL(t, query)
	before := tenantScopeRows(t, db, "EXPLAIN QUERY PLAN "+parent, args...)
	after := tenantScopeRows(t, db, "EXPLAIN QUERY PLAN "+query, args...)
	// C2B adds a tenant composite category key. The planner may now start at
	// categories and use the cost-limit composite key instead of the reverse.
	// Require the same indexed access structure and correlated reads, allowing
	// only those two parent index choices and their order to differ.
	details := func(plan [][]any) []string {
		result := make([]string, 0, len(plan))
		for _, row := range plan {
			detail := fmt.Sprint(row[3])
			for _, alias := range []string{"cl", "c"} {
				if strings.HasPrefix(detail, "SEARCH "+alias+" USING INDEX ") || strings.HasPrefix(detail, "SEARCH "+alias+" USING COVERING INDEX ") {
					detail = "SEARCH " + alias + " USING INDEX"
					break
				}
			}
			result = append(result, detail)
		}
		sort.Strings(result)
		return result
	}
	beforeDetails, afterDetails := details(before), details(after)
	if len(beforeDetails) != 17 || len(afterDetails) != 17 || !reflect.DeepEqual(beforeDetails, afterDetails) {
		t.Fatalf("query plan details changed: parent=%v scoped=%v", beforeDetails, afterDetails)
	}
	t.Logf("DETAIL ROWS PARENT=%d; DETAIL ROWS L9=%d; EQUIVALENT INDEXED ACCESS=true", len(beforeDetails), len(afterDetails))
	for _, alias := range []string{"cl", "c", "t", "ip"} {
		indexed := false
		for _, row := range after {
			detail := fmt.Sprint(row[3])
			if strings.Contains(detail, "SEARCH "+alias+" USING") {
				indexed = true
			}
		}
		if !indexed {
			t.Fatalf("missing indexed search for %s: %v", alias, after)
		}
	}
	tenantScopeIndexedPlan(t, db, query, args...)
}
