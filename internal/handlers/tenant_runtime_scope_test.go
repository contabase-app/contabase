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
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Read the actual production statements, rather than testing SQL copied into a
// fixture. This also tests reads that earlier Go validation normally masks and
// the dead W10 helper without invoking or reactivating it.
func tenantScopeStatement(t *testing.T, file, function, marker string, bindings ...string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Query" && selector.Sel.Name != "QueryRow") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
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
			for _, arg := range call.Args[1:] {
				var b bytes.Buffer
				if err := printer.Fprint(&b, fset, arg); err != nil {
					t.Fatal(err)
				}
				got = append(got, b.String())
			}
			if !reflect.DeepEqual(got, bindings) || strings.Count(query, "?") != len(bindings) {
				t.Fatalf("%s SQL bindings = %v, want %v; placeholders = %d", function, got, bindings, strings.Count(query, "?"))
			}
			found = append(found, query)
			return true
		})
	}
	if len(found) != 1 {
		t.Fatalf("%s: matched %d production statements for %q", function, len(found), marker)
	}
	return found[0]
}

func seedTenantRuntimeScope(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('scope-user', 'Scope', 'scope@example.com', 'hash')`)
	for _, suffix := range []string{"a", "b"} {
		ws := "scope-" + suffix
		execTestSQL(t, db, `INSERT INTO workspaces (id, name) VALUES (?, ?)`, ws, ws)
		execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES (?, 'scope-user', 'ADMIN')`, ws)
		for _, account := range []struct{ prefix, kind string }{{"card-", "CREDIT_CARD"}, {"bank-", "CHECKING"}} {
			execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES (?, ?, ?, ?)`, account.prefix+suffix, ws, account.prefix+suffix, account.kind)
		}
		execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day) VALUES (?, ?, 20, 10)`, "cc-"+suffix, "card-"+suffix)
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status) VALUES (?, ?, '2099-08', ?, ?, 'OPEN')`, "inv-"+suffix, "card-"+suffix, testUnixDate("2099-07-20"), testUnixDate("2099-08-10"))
		execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type) VALUES (?, ?, ?, 'home', '#22c55e', 'EXPENSE')`, "cat-"+suffix, ws, "Category "+suffix)
		amount := int64(1100)
		if suffix == "b" {
			amount = 7700
		}
		execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status) VALUES (?, ?, 'scope-user', ?, 'EXPENSE', ?, ?, 'Scope purchase', 'paid')`, "tx-"+suffix, ws, "card-"+suffix, amount, testUnixDate("2099-06-25"))
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, start_date, frequency, total_occurrences) VALUES (?, ?, 'scope-user', ?, ?, 'EXPENSE', 2300, ?, 'MONTHLY', 1)`, "rule-"+suffix, ws, "card-"+suffix, "cat-"+suffix, testUnixDate("2099-06-26"))
	}
	return db
}

func tenantScopeRows(t *testing.T, db *sql.DB, query string, args ...any) [][]any {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(cols))
		pointers := make([]any, len(cols))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func tenantScopeIndexedPlan(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	plan := tenantScopeRows(t, db, "EXPLAIN QUERY PLAN "+query, args...)
	for _, row := range plan {
		detail := fmt.Sprint(row[3])
		t.Log(detail)
		if strings.HasPrefix(detail, "SCAN ") && !strings.Contains(detail, "USING") {
			t.Fatalf("avoidable table scan: %s", detail)
		}
	}
}

func TestTenantRuntimeScopeInvoiceBlockStatementsR3R4R6R8(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	for _, tc := range []struct {
		id, file, function, marker, entity, predicate string
		bindings                                      []string
	}{
		{"R3", "transacoes.go", "validateSequenceExistingRow", "SELECT i.status", "inv-", " AND a.workspace_id = ?", []string{"existing.invoiceID.String", "workspaceID"}},
		{"R4a", "transacoes.go", "validateInvoiceBelongsToAccount", "SELECT type, workspace_id", "card-", " AND workspace_id = ?", []string{"accountID.String", "workspaceID"}},
		{"R4b", "transacoes.go", "validateInvoiceBelongsToAccount", "SELECT i.account_id", "inv-", " AND a.workspace_id = ?", []string{"invoiceID", "workspaceID"}},
		{"R4c", "transacoes.go", "validateInvoiceBelongsToAccount", "SELECT workspace_id FROM accounts", "card-", " AND workspace_id = ?", []string{"invAccountID", "workspaceID"}},
		{"R6", "recurring_occurrence_blocks.go", "ensureAutomaticInvoiceAcceptsTransactionTx", "SELECT i.id, i.reference", "inv-", " AND a.workspace_id = ?", []string{"invoiceID", "workspaceID"}},
		{"R8", "recurring_occurrence_blocks.go", "validateRecurringOccurrenceBlockRuleCoverageTx", "SELECT workspace_id, series_id", "rule-", " AND workspace_id = ?", []string{"block.RecurringRuleID", "block.WorkspaceID"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			query := tenantScopeStatement(t, tc.file, tc.function, tc.marker, tc.bindings...)
			for _, suffix := range []string{"a", "b"} {
				args := []any{tc.entity + suffix, "scope-" + suffix}
				got := tenantScopeRows(t, db, query, args...)
				if len(got) != 1 {
					t.Fatalf("same tenant %v returned %v", args, got)
				}
				switch tc.id {
				case "R3":
					if got[0][0] != "OPEN" {
						t.Fatalf("invoice status changed: %v", got)
					}
				case "R4a":
					if !reflect.DeepEqual(got[0], []any{"CREDIT_CARD", "scope-" + suffix}) {
						t.Fatalf("account metadata changed: %v", got)
					}
				case "R4b":
					if got[0][0] != "card-"+suffix {
						t.Fatalf("invoice owner changed: %v", got)
					}
				case "R4c", "R8":
					if got[0][0] != "scope-"+suffix {
						t.Fatalf("workspace changed: %v", got)
					}
				case "R6":
					if !reflect.DeepEqual(got[0], []any{"inv-" + suffix, "2099-08", "OPEN", "card-" + suffix, "scope-" + suffix, "CREDIT_CARD"}) {
						t.Fatalf("automatic invoice evidence changed: %v", got)
					}
				}
				tenantScopeIndexedPlan(t, db, query, args...)
			}
			for _, args := range [][]any{
				{tc.entity + "b", "scope-a"}, {tc.entity + "a", "scope-b"},
				{tc.entity + "b", ""}, {tc.entity + "b", "missing"}, {"missing", "scope-a"},
			} {
				if got := tenantScopeRows(t, db, query, args...); len(got) != 0 {
					t.Fatalf("foreign/unknown %v returned %v", args, got)
				}
			}
			// Negative control removes only the new ownership predicate from the
			// extracted production statement. The formerly unscoped read sees B.
			old := strings.Replace(query, tc.predicate, "", 1)
			if old == query {
				t.Fatal("ownership predicate not found")
			}
			if got := tenantScopeRows(t, db, old, tc.entity+"b"); len(got) != 1 {
				t.Fatalf("unscoped negative control did not resolve B: %v", got)
			}
		})
	}
}

func TestTenantRuntimeScopeInvoiceAccountHelperR4(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('card-a-other', 'scope-a', 'Other', 'CREDIT_CARD')`)
	for _, tc := range []struct {
		name, workspace, invoice, account, errorText string
	}{
		{"own A", "scope-a", "inv-a", "card-a", ""},
		{"own B", "scope-b", "inv-b", "card-b", ""},
		{"foreign account", "scope-a", "inv-b", "card-b", "account card-b not found"},
		{"foreign invoice", "scope-a", "inv-b", "card-a", "invoice inv-b not found"},
		{"reverse foreign", "scope-b", "inv-a", "card-b", "invoice inv-a not found"},
		{"missing invoice", "scope-a", "missing", "card-a", "invoice missing not found"},
		{"missing account", "scope-a", "inv-a", "missing", "account missing not found"},
		{"non-card", "scope-a", "inv-a", "bank-a", "non-credit-card"},
		{"wrong own card", "scope-a", "inv-a", "card-a-other", "not transaction account"},
		{"null account", "scope-a", "inv-a", "", "on null account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = validateInvoiceBelongsToAccount(tx, tc.invoice, sql.NullString{String: tc.account, Valid: tc.account != ""}, tc.workspace)
			if tc.errorText == "" && err != nil || tc.errorText != "" && (err == nil || !strings.Contains(err.Error(), tc.errorText)) {
				t.Fatalf("validation error = %v, want %q", err, tc.errorText)
			}
			if strings.Contains(tc.errorText, "not found") && !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("not-found contract changed: %v", err)
			}
		})
	}
}

func TestTenantRuntimeScopeSequenceAndAutomaticInvoiceR3R6(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	for _, suffix := range []string{"a", "b"} {
		ws, series := "scope-"+suffix, "series-"+suffix
		execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES (?, ?, 'scope-user')`, series, ws)
		execTestSQL(t, db, `UPDATE recurring_rules SET series_id = ? WHERE id = ?`, series, "rule-"+suffix)
		execTestSQL(t, db, `UPDATE transactions SET invoice_id = ?, recurring_rule_id = ?, series_id = ?, recurrence_sequence = 2, date = ?, date_civil = '2099-07-26' WHERE id = ?`, "inv-"+suffix, "rule-"+suffix, series, testUnixDate("2099-07-26"), "tx-"+suffix)
	}
	for _, status := range []string{"OPEN", "CLOSED", "PAID"} {
		for _, suffix := range []string{"a", "b"} {
			t.Run(status+"/"+suffix, func(t *testing.T) {
				execTestSQL(t, db, `UPDATE invoices SET status = ? WHERE id = ?`, status, "inv-"+suffix)
				tx, err := db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				h := &TransactionHandler{DB: db, WorkspaceID: "scope-" + suffix}
				expected := recurringOccurrenceExpectation{RuleID: "rule-" + suffix, AccountID: "card-" + suffix, Type: "EXPENSE", Date: MustCivilWriteDate("2099-07-26")}
				exists, err := h.seriesSequenceValidateTx(tx, h.WorkspaceID, "series-"+suffix, 2, expected)
				if err != nil || !exists {
					t.Fatalf("same-tenant sequence: exists=%v err=%v", exists, err)
				}
				evidence, err := ensureAutomaticInvoiceAcceptsTransactionTx(tx, h.WorkspaceID, expected.AccountID, "inv-"+suffix)
				if status == "OPEN" && err != nil || status != "OPEN" && !errors.Is(err, errAutomaticInvoiceFrozen) {
					t.Fatalf("same-tenant automatic routing %s: %v", status, err)
				}
				if evidence != (recurringOccurrenceFrozenInvoice{InvoiceID: "inv-" + suffix, InvoiceReference: "2099-08", InvoiceStatus: status}) {
					t.Fatalf("automatic evidence changed: %+v", evidence)
				}
				mutable, mutableErr := ensureInvoiceAcceptsMutableCompositionTx(tx, h.WorkspaceID, expected.AccountID, "inv-"+suffix)
				if mutable != evidence || status != "PAID" && mutableErr != nil || status == "PAID" && !errors.Is(mutableErr, errAutomaticInvoiceFrozen) {
					t.Fatalf("mutable composition status contract changed: %+v %v", mutable, mutableErr)
				}
				other := "b"
				if suffix == "b" {
					other = "a"
				}
				for _, ref := range []struct{ invoice, account string }{{"inv-" + suffix, "card-" + suffix}, {"inv-" + suffix, "card-" + other}} {
					foreign, err := ensureAutomaticInvoiceAcceptsTransactionTx(tx, "scope-"+other, ref.account, ref.invoice)
					if !errors.Is(err, errInvoiceStateStructural) || foreign != (recurringOccurrenceFrozenInvoice{}) || errors.Is(err, errAutomaticInvoiceFrozen) {
						t.Fatalf("foreign invoice contributed routing evidence: %+v %v", foreign, err)
					}
				}
				missing, err := ensureAutomaticInvoiceAcceptsTransactionTx(tx, h.WorkspaceID, expected.AccountID, "missing")
				if !errors.Is(err, errInvoiceStateStructural) || missing != (recurringOccurrenceFrozenInvoice{}) {
					t.Fatalf("missing invoice contract changed: %+v %v", missing, err)
				}
			})
		}
	}
	// A real A transaction can hold a foreign invoice under the current schema.
	// Sequence validation must fail without changing either tenant's state.
	execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'inv-b' WHERE id = 'tx-a'`)
	before := tenantLedgerSnapshot(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	h := &TransactionHandler{DB: db, WorkspaceID: "scope-a"}
	exists, err := h.seriesSequenceValidateTx(tx, "scope-a", "series-a", 2, recurringOccurrenceExpectation{RuleID: "rule-a", AccountID: "card-a", Type: "EXPENSE", Date: MustCivilWriteDate("2099-07-26")})
	if exists || !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign sequence validation: exists=%v err=%v", exists, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, tenantLedgerSnapshot(t, db)) {
		t.Fatal("sequence validation mutated state")
	}
}

func TestTenantRuntimeScopeBlockRuleCoverageR8(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	for _, suffix := range []string{"a", "b"} {
		execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES (?, ?, 'scope-user')`, "series-"+suffix, "scope-"+suffix)
		execTestSQL(t, db, `UPDATE recurring_rules SET series_id = ?, starts_at_civil = '2099-06-26', effective_start_sequence = 1, effective_end_sequence = 1, active = 0 WHERE id = ?`, "series-"+suffix, "rule-"+suffix)
		execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED' WHERE id = ?`, "inv-"+suffix)
	}
	for _, suffix := range []string{"a", "b"} {
		expected := recurringOccurrenceBlockExpectation{WorkspaceID: "scope-" + suffix, SeriesID: "series-" + suffix, RecurringRuleID: "rule-" + suffix, RecurrenceSequence: 1, OccurrenceDate: MustCivilWriteDate("2099-06-26"), AccountID: "card-" + suffix}
		block := recurringOccurrenceBlockRow{WorkspaceID: expected.WorkspaceID, SeriesID: expected.SeriesID, RecurringRuleID: expected.RecurringRuleID, RecurrenceSequence: 1, OccurrenceDateCivil: expected.OccurrenceDate.Civil()}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRecurringOccurrenceBlockRuleCoverageTx(tx, block); err != nil {
			t.Fatalf("own historical rule %s: %v", suffix, err)
		}
		for _, mutate := range []func(*recurringOccurrenceBlockRow){
			func(b *recurringOccurrenceBlockRow) { b.RecurringRuleID = "rule-a"; b.WorkspaceID = "scope-b" },
			func(b *recurringOccurrenceBlockRow) { b.RecurringRuleID = "rule-b"; b.WorkspaceID = "scope-a" },
			func(b *recurringOccurrenceBlockRow) { b.RecurringRuleID = "missing" },
			func(b *recurringOccurrenceBlockRow) { b.SeriesID = "wrong" },
			func(b *recurringOccurrenceBlockRow) { b.RecurrenceSequence = 2 },
			func(b *recurringOccurrenceBlockRow) { b.OccurrenceDateCivil = "2099-06-27" },
		} {
			bad := block
			mutate(&bad)
			if err := validateRecurringOccurrenceBlockRuleCoverageTx(tx, bad); !errors.Is(err, errInvoiceStateStructural) {
				t.Fatalf("invalid block %+v accepted: %v", bad, err)
			}
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "inv-"+suffix, "2099-08")
		if resolved, err := resolveBlockFixture(t, db, expected); !resolved || err != nil {
			t.Fatalf("own historical block %s: resolved=%v err=%v", suffix, resolved, err)
		}
	}
	// Exercise the complete resolver with a persisted A block pointing at B's
	// rule. Do not weaken any production trigger to construct the fixture.
	bad := recurringOccurrenceBlockExpectation{WorkspaceID: "scope-a", SeriesID: "series-a", RecurringRuleID: "rule-b", RecurrenceSequence: 2, OccurrenceDate: MustCivilWriteDate("2099-07-26"), AccountID: "card-a"}
	insertBlockFixture(t, db, bad, bad.RecurringRuleID, bad.OccurrenceDate.Civil(), "inv-a", "2099-08")
	before := tenantScopeRows(t, db, `SELECT * FROM recurring_occurrence_blocks ORDER BY workspace_id, recurrence_sequence`)
	if resolved, err := resolveBlockFixture(t, db, bad); resolved || !errors.Is(err, errInvoiceStateStructural) || !strings.Contains(err.Error(), "rule lookup") {
		t.Fatalf("foreign-rule block: resolved=%v err=%v", resolved, err)
	}
	if after := tenantScopeRows(t, db, `SELECT * FROM recurring_occurrence_blocks ORDER BY workspace_id, recurrence_sequence`); !reflect.DeepEqual(before, after) {
		t.Fatal("block validation mutated historical evidence")
	}
	if violations := tenantScopeRows(t, db, `PRAGMA foreign_key_check`); len(violations) != 0 {
		t.Fatalf("invalid foreign-key fixture: %v", violations)
	}
}

func TestTenantRuntimeScopeStatementsW2W3W4W6W7W8W9W10(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	for _, tc := range []struct {
		id, file, function, marker, entity string
		bindings                           []string
	}{
		{"W2", "faturas.go", "buildFaturaDataForInvoice", "SELECT i.status FROM invoices", "inv-", []string{"invoiceID", "workspaceID"}},
		{"W3", "faturas.go", "projectedRecurringInvoiceRows", "SELECT cc.closing_day", "card-", []string{"accountID", "workspaceID"}},
		{"W4", "faturas.go", "CalculateVirtualInvoiceTotal", "SELECT cc.closing_day", "card-", []string{"accountID", "workspaceID"}},
		{"W6", "invoice_payment.go", "reverseInvoicePaymentWithHooks", "SELECT i.account_id, i.closing_date", "inv-", []string{"result.InvoiceID", "command.WorkspaceID"}},
		{"W7", "invoice_payment.go", "reverseInvoicePaymentWithHooks", "SELECT workspace_id, type", "bank-", []string{"result.AccountID", "command.WorkspaceID"}},
		{"W8", "invoice_payment.go", "reverseInvoicePaymentWithHooks", "SELECT workspace_id, account_id, type, amount", "tx-", []string{"result.OriginalTransactionID", "command.WorkspaceID"}},
		{"W9", "transacoes.go", "lancamentosVirtualInvoiceHasMatchingItems", "SELECT cc.closing_day", "card-", []string{"inv.AccountID", "h.WorkspaceID"}},
		{"W10", "transacoes.go", "queryInvoiceOptionsForAccount", "SELECT i.id, i.reference", "card-", []string{"accountID", "h.WorkspaceID"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			query := tenantScopeStatement(t, tc.file, tc.function, tc.marker, tc.bindings...)
			for _, suffix := range []string{"a", "b"} {
				args := []any{tc.entity + suffix, "scope-" + suffix}
				if got := tenantScopeRows(t, db, query, args...); len(got) != 1 {
					t.Fatalf("same workspace %s: rows = %v", suffix, got)
				}
				tenantScopeIndexedPlan(t, db, query, args...)
			}
			for _, args := range [][]any{{tc.entity + "b", "scope-a"}, {tc.entity + "a", "scope-b"}, {tc.entity + "b", ""}, {tc.entity + "b", "missing"}} {
				if got := tenantScopeRows(t, db, query, args...); len(got) != 0 {
					t.Fatalf("foreign/unknown workspace %v returned %v", args, got)
				}
			}
		})
	}
}

func TestTenantRuntimeScopeW5RecurringCategory(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	valid, err := projectedRecurringInvoiceRows(db, "scope-a", "card-a", "2099-08")
	if err != nil {
		t.Fatalf("projection A: %v", err)
	}
	if len(valid) != 1 || valid[0].CategoryName != "Category a" || valid[0].Amount != 2300 {
		t.Fatalf("same workspace projection = %+v", valid)
	}
	// This state is legal with the actual schema and foreign keys enabled.
	execTestSQL(t, db, `UPDATE recurring_rules SET category_id = 'cat-b' WHERE id = 'rule-a'`)
	if got := tenantScopeRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("fixture violates foreign keys: %v", got)
	}
	query := tenantScopeStatement(t, "faturas.go", "projectedRecurringInvoiceRows", "SELECT rr.id, COALESCE(rr.series_id", "workspaceID", "accountID", "models.TransactionTypeExpense", "cycleEnd.Unix()")
	_, end := invoiceCycleBoundsForReference(2099, 8, 20, 10)
	args := []any{"scope-a", "card-a", "EXPENSE", end}
	// Negative control reproduces the old SQL leaking B's category metadata.
	old := strings.Replace(query, " AND c.workspace_id = rr.workspace_id", "", 1)
	if got := tenantScopeRows(t, db, old, args...); len(got) != 1 || got[0][12] != "Category b" {
		t.Fatalf("old cross-workspace leak not reproduced: %v", got)
	}
	got, err := projectedRecurringInvoiceRows(db, "scope-a", "card-a", "2099-08")
	if err != nil {
		t.Fatalf("projection A after corruption: %v", err)
	}
	if len(got) != 1 || got[0].CategoryName != "Sem categoria" || got[0].CategoryIcon != "repeat" || got[0].CategoryColor != "slate" || got[0].Amount != valid[0].Amount {
		t.Fatalf("foreign category contributed to A or own rule changed: %+v", got)
	}
	if ownB, err := projectedRecurringInvoiceRows(db, "scope-b", "card-b", "2099-08"); err != nil {
		t.Fatalf("projection B: %v", err)
	} else if len(ownB) != 1 || ownB[0].CategoryName != "Category b" {
		t.Fatalf("B same workspace projection = %+v", ownB)
	}
}

func TestTenantRuntimeScopeW3W4W9RuntimeCardConfig(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	for _, suffix := range []string{"a", "b"} {
		ws, card := "scope-"+suffix, "card-"+suffix
		if got, err := projectedRecurringInvoiceRows(db, ws, card, "2099-08"); err != nil {
			t.Fatalf("same workspace recurring %s: %v", ws, err)
		} else if len(got) != 1 {
			t.Fatalf("same workspace recurring %s = %+v", ws, got)
		}
		amount, count, err := CalculateVirtualInvoiceTotal(db, ws, card, "2099-08")
		if err != nil {
			t.Fatalf("same workspace virtual %s: %v", ws, err)
		}
		want := int64(3400)
		if suffix == "b" {
			want = 10000
		}
		if amount != want || count != 2 {
			t.Fatalf("same workspace virtual %s = %d/%d", ws, amount, count)
		}
		h := &TransactionHandler{DB: db, WorkspaceID: ws}
		if !h.lancamentosVirtualInvoiceHasMatchingItems(InvoiceRow{AccountID: card, Reference: "2099-08"}, LancamentosFilters{}) {
			t.Fatalf("same workspace virtual filter %s rejected", ws)
		}
	}
	// Legal relational corruption gives A business rows referring to B's
	// account. A must not read B's card configuration to project these rows.
	execTestSQL(t, db, `UPDATE recurring_rules SET account_id = 'card-b' WHERE id = 'rule-a'`)
	execTestSQL(t, db, `UPDATE transactions SET account_id = 'card-b' WHERE id = 'tx-a'`)
	if got, err := projectedRecurringInvoiceRows(db, "scope-a", "card-b", "2099-08"); err != nil {
		t.Fatalf("W3 projection: %v", err)
	} else if len(got) != 0 {
		t.Fatalf("W3 foreign card config used: %+v", got)
	}
	if amount, count, err := CalculateVirtualInvoiceTotal(db, "scope-a", "card-b", "2099-08"); err != nil {
		t.Fatalf("W4 virtual: %v", err)
	} else if amount != 0 || count != 0 {
		t.Fatalf("W4 foreign card config used: %d/%d", amount, count)
	}
	h := &TransactionHandler{DB: db, WorkspaceID: "scope-a"}
	if h.lancamentosVirtualInvoiceHasMatchingItems(InvoiceRow{AccountID: "card-b", Reference: "2099-08"}, LancamentosFilters{}) {
		t.Fatal("W9 foreign card config used")
	}
}

func TestTenantRuntimeScopeW6W7W8ReversalRollback(t *testing.T) {
	for _, tc := range []struct{ id, corrupt, notFound string }{
		{"same-workspace", "", ""},
		{"W6", `UPDATE invoice_payments SET invoice_id = 'inv-b' WHERE id = 'ip-scope'`, "invoice/card not found"},
		{"W7", `UPDATE invoice_payments SET account_id = 'bank-b' WHERE id = 'ip-scope'`, "payment account not found"},
		{"W8", `UPDATE transactions SET workspace_id = 'scope-foreign' WHERE id = 'payment-scope'`, "original transaction not found"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			db := openTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			paidAt := reversalUnix("2026-08-05", 10)
			seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
				InvoiceTotal: 100000, InitialBalance: 200000,
				Payments: []reversalPaymentSeed{{ID: "ip-scope", TxID: "payment-scope", Amount: 50000, PaidAt: paidAt, CreatedAt: paidAt}},
			})
			execTestSQL(t, db, `INSERT INTO workspaces (id, name) VALUES ('scope-foreign', 'Foreign')`)
			execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('card-b', 'scope-foreign', 'Foreign card', 'CREDIT_CARD'), ('bank-b', 'scope-foreign', 'Foreign bank', 'CHECKING')`)
			execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day) VALUES ('cc-b', 'card-b', 20, 10)`)
			execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status) VALUES ('inv-b', 'card-b', '2099-08', ?, ?, 'OPEN')`, testUnixDate("2099-07-20"), testUnixDate("2099-08-10"))
			if tc.corrupt != "" {
				execTestSQL(t, db, tc.corrupt)
			}
			before := reversalSnapshot(t, db, "ip-scope", "payment-scope")
			_, err := reverseInvoicePayment(db, reversalCommand("ip-scope"))
			if tc.corrupt == "" {
				if err != nil {
					t.Fatalf("valid reversal: %v", err)
				}
				if after := reversalSnapshot(t, db, "ip-scope", "payment-scope"); after.Balance != before.Balance+50000 || !after.PaymentReversedAt.Valid || after.CompensationCount != 1 {
					t.Fatalf("valid reversal state = %+v", after)
				}
				return
			}
			if !errors.Is(err, errInvoicePaymentReversalStructural) || !strings.Contains(err.Error(), tc.notFound) {
				t.Fatalf("foreign entity must be excluded in SQL: %v", err)
			}
			if after := reversalSnapshot(t, db, "ip-scope", "payment-scope"); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed reversal did not roll back: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestTenantRuntimeScopeW10DeadHelperHasNoCallers(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "queryInvoiceOptionsForAccount" {
				t.Errorf("dead method referenced in %s", entry.Name())
			}
			return true
		})
	}
}

func tenantLedgerSnapshot(t *testing.T, db *sql.DB) map[string][][]any {
	t.Helper()
	result := make(map[string][][]any)
	for _, table := range []string{"transactions", "accounts"} {
		result[table] = tenantScopeRows(t, db, "SELECT * FROM "+table+" ORDER BY id")
	}
	return result
}
