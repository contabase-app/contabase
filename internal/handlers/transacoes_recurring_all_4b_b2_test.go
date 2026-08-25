package handlers

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/database"
)

type recurringAll4BB2Fixture struct {
	db       *sql.DB
	handler  TransactionHandler
	seriesID string
	ruleID   string
	ids      []string
	business bool
}

func newRecurringAll4BB2Fixture(t *testing.T, business bool, start string, dueOffset *int64) *recurringAll4BB2Fixture {
	t.Helper()
	db := openTestDB(t)
	return newRecurringAll4BB2FixtureWithDB(t, db, business, start, dueOffset)
}

func newRecurringAll4BB2FixtureWithDB(t *testing.T, db *sql.DB, business bool, start string, dueOffset *int64) *recurringAll4BB2Fixture {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	now := time.Now().Unix()
	workspaceKind := "personal"
	if business {
		workspaceKind = "business"
	}
	execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, workspaceKind)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-alt-b2', 'ws-test', 'Conta Alternativa B2', 'CHECKING', 500000, 500000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, name, type, created_at)
		VALUES ('contact-b2', 'ws-test', 'Contato B2', 'client', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES ('series-all-b2', 'ws-test', 'user-test', 1, 3, ?, ?)
	`, now, now)

	startDate := MustCivilWriteDate(start)
	var dueOffsetValue interface{}
	if business && dueOffset != nil {
		dueOffsetValue = *dueOffset
	}
	var contactValue interface{}
	if business {
		contactValue = "contact-b2"
	}
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id,
			effective_start_sequence, due_date_offset_days, contact_id, created_at, updated_at
		) VALUES (
			'rule-all-b2', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec',
			'EXPENSE', 10000, 'Base B2', ?, ?, ?, ?, 'MONTHLY', 'PENDING', 1, 3,
			'series-all-b2', 1, ?, ?, ?, ?
		)
	`, startDate.LegacyUnix(), startDate.Civil(), startDate.LegacyUnix(), startDate.Civil(), dueOffsetValue, contactValue, now, now)

	ids := []string{"tx-all-b2-1", "tx-all-b2-2", "tx-all-b2-3"}
	for index, id := range ids {
		date, err := recurrenceCivilDateFromOffset(startDate, "MONTHLY", int64(index))
		if err != nil {
			t.Fatalf("fixture recurrence date: %v", err)
		}
		var dueUnix, dueCivil interface{}
		if business && dueOffset != nil {
			due, err := date.AddDays(int(*dueOffset))
			if err != nil {
				t.Fatalf("fixture due date: %v", err)
			}
			dueUnix, dueCivil = due.LegacyUnix(), due.Civil()
		}
		execTestSQL(t, db, `
			INSERT INTO transactions (
				id, workspace_id, user_id, account_id, category_id, type, amount,
				date, date_civil, due_date, due_date_civil, contact_id,
				description, notes, status, installment_number, total_installments,
				recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at
			) VALUES (
				?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000,
				?, ?, ?, ?, ?, 'Base B2', 'nota-base', 'pending', 1, 1,
				'rule-all-b2', ?, 'series-all-b2', ?, ?
			)
		`, id, date.LegacyUnix(), date.Civil(), dueUnix, dueCivil, contactValue, index+1, now, now)
	}

	return &recurringAll4BB2Fixture{
		db:       db,
		handler:  TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test", Templates: testOOBTemplatesSeries(t)},
		seriesID: "series-all-b2",
		ruleID:   "rule-all-b2",
		ids:      ids,
		business: business,
	}
}

func newRecurringAll4BB2DiskFixture(t *testing.T, business bool, start string, dueOffset *int64) *recurringAll4BB2Fixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "recurring-all-b2.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap B2 disk database: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close B2 bootstrap database: %v", err)
	}
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("reopen B2 disk database without background jobs: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping B2 disk database: %v", err)
	}
	db.SetMaxOpenConns(2)
	return newRecurringAll4BB2FixtureWithDB(t, db, business, start, dueOffset)
}

func (f *recurringAll4BB2Fixture) close() { _ = f.db.Close() }

func (f *recurringAll4BB2Fixture) request(t *testing.T, selected int, overrides map[string]string) *httptest.ResponseRecorder {
	return f.requestWithHandler(t, &f.handler, selected, overrides)
}

func (f *recurringAll4BB2Fixture) requestWithHandler(t *testing.T, handler *TransactionHandler, selected int, overrides map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var dateCivil string
	if err := f.db.QueryRow(`SELECT COALESCE(date_civil, '') FROM transactions WHERE id = ?`, f.ids[selected-1]).Scan(&dateCivil); err != nil {
		t.Fatalf("load selected civil date: %v", err)
	}
	fields := map[string]string{
		"valor":            "200,00",
		"descricao":        "Atualizada B2",
		"data":             dateCivil,
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"recorrencia":      "mensal",
		"escopo":           "all",
	}
	if f.business {
		fields["contact_id"] = "contact-b2"
	}
	for key, value := range overrides {
		fields[key] = value
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+f.ids[selected-1], fields)
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, f.ids[selected-1])
	return rr
}

func recurringAll4BB2Snapshot(t *testing.T, db *sql.DB, seriesID string) string {
	t.Helper()
	parts := []string{
		recurringAll4BB2RowsSnapshot(t, db, "series", `SELECT * FROM recurring_series WHERE workspace_id='ws-test' AND id=? ORDER BY id`, seriesID),
		recurringAll4BB2RowsSnapshot(t, db, "rules", `SELECT * FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? ORDER BY id`, seriesID),
		recurringAll4BB2RowsSnapshot(t, db, "transactions", `
			SELECT * FROM transactions
			WHERE workspace_id='ws-test' AND (
				series_id=? OR recurring_rule_id IN (
					SELECT id FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?
				)
			)
			ORDER BY id`, seriesID, seriesID),
		recurringAll4BB2RowsSnapshot(t, db, "history", `
			SELECT * FROM transaction_status_history
			WHERE workspace_id='ws-test' AND transaction_id IN (
				SELECT id FROM transactions WHERE workspace_id='ws-test' AND (
					series_id=? OR recurring_rule_id IN (
						SELECT id FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=?
					)
				)
			) ORDER BY id`, seriesID, seriesID),
		recurringAll4BB2RowsSnapshot(t, db, "accounts", `SELECT * FROM accounts WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "invoices", `SELECT i.* FROM invoices i JOIN accounts a ON a.id=i.account_id WHERE a.workspace_id='ws-test' ORDER BY i.id`),
		recurringAll4BB2RowsSnapshot(t, db, "payments", `SELECT * FROM invoice_payments WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "boxes", `SELECT * FROM boxes WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "ledger", `SELECT l.* FROM box_virtual_ledger l JOIN boxes b ON b.id=l.box_id WHERE b.workspace_id='ws-test' ORDER BY l.id`),
		recurringAll4BB2RowsSnapshot(t, db, "contacts", `SELECT * FROM contacts WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "categories", `SELECT * FROM categories WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "credit_cards", `SELECT c.* FROM credit_cards c JOIN accounts a ON a.id=c.account_id WHERE a.workspace_id='ws-test' ORDER BY c.id`),
		recurringAll4BB2RowsSnapshot(t, db, "materialization", `SELECT * FROM recurring_materialization_state WHERE workspace_id='ws-test' ORDER BY workspace_id`),
	}
	return strings.Join(parts, "\n")
}

func recurringAll4BB2RowsSnapshot(t *testing.T, db queryer, label, query string, args ...interface{}) string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("snapshot %s query: %v", label, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("snapshot %s columns: %v", label, err)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s|%s", label, strings.Join(columns, ","))
	for rows.Next() {
		values := make([]interface{}, len(columns))
		pointers := make([]interface{}, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("snapshot %s scan: %v", label, err)
		}
		out.WriteByte('\n')
		for i, value := range values {
			if i > 0 {
				out.WriteByte('|')
			}
			switch typed := value.(type) {
			case nil:
				out.WriteString("NULL")
			case []byte:
				fmt.Fprintf(&out, "bytes:%x", typed)
			default:
				fmt.Fprintf(&out, "%T:%v", value, value)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot %s rows: %v", label, err)
	}
	return out.String()
}

type queryer interface {
	Query(query string, args ...interface{}) (*sql.Rows, error)
}

func recurringAll4BB2AssertStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, want, rr.Body.String())
	}
}

func recurringAll4BB2AssertOccurrence(t *testing.T, db *sql.DB, id, description string, amount int64) {
	t.Helper()
	var gotDescription string
	var gotAmount int64
	if err := db.QueryRow(`SELECT description, amount FROM transactions WHERE id = ? AND workspace_id = 'ws-test'`, id).Scan(&gotDescription, &gotAmount); err != nil {
		t.Fatalf("load occurrence %s: %v", id, err)
	}
	if gotDescription != description || gotAmount != amount {
		t.Fatalf("occurrence %s = (%q,%d), want (%q,%d)", id, gotDescription, gotAmount, description, amount)
	}
}

func TestRecurringAll4BB2SimpleMetadataFromSequenceOneAndTwo(t *testing.T) {
	for _, selected := range []int{1, 2} {
		t.Run(fmt.Sprintf("sequence_%d", selected), func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
			defer f.close()
			rr := f.request(t, selected, nil)
			recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
			for _, id := range f.ids {
				recurringAll4BB2AssertOccurrence(t, f.db, id, "Atualizada B2", 20000)
			}
			var description string
			var amount int64
			if err := f.db.QueryRow(`SELECT description, amount FROM recurring_rules WHERE id = ?`, f.ruleID).Scan(&description, &amount); err != nil {
				t.Fatal(err)
			}
			if description != "Atualizada B2" || amount != 20000 {
				t.Fatalf("active rule = (%q,%d)", description, amount)
			}
		})
	}
}

func recurringAll4BB2MakeChain(t *testing.T, f *recurringAll4BB2Fixture) string {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 1 WHERE id = ?`, f.ruleID)
	execTestSQL(t, f.db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id, parent_rule_id,
			effective_start_sequence, created_at, updated_at
		) VALUES (
			'rule-all-b2-active', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec',
			'EXPENSE', 10000, 'Base B2', ?, '2026-01-10', ?, '2026-02-10', 'MONTHLY',
			'PENDING', 1, 3, ?, ?, 2, ?, ?
		)
	`, testUnixDate("2026-01-10"), testUnixDate("2026-02-10"), f.seriesID, f.ruleID, now, now)
	execTestSQL(t, f.db, `UPDATE transactions SET recurring_rule_id = 'rule-all-b2-active' WHERE series_id = ? AND recurrence_sequence >= 2`, f.seriesID)
	return "rule-all-b2-active"
}

func TestRecurringAll4BB2ChainMetadataFromActiveAndInactiveSelection(t *testing.T) {
	for _, selected := range []int{1, 2} {
		t.Run(fmt.Sprintf("sequence_%d", selected), func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
			defer f.close()
			activeID := recurringAll4BB2MakeChain(t, f)
			rr := f.request(t, selected, nil)
			recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
			for _, id := range f.ids {
				recurringAll4BB2AssertOccurrence(t, f.db, id, "Atualizada B2", 20000)
			}
			var rootDescription, activeDescription string
			if err := f.db.QueryRow(`SELECT description FROM recurring_rules WHERE id = ?`, f.ruleID).Scan(&rootDescription); err != nil {
				t.Fatal(err)
			}
			if err := f.db.QueryRow(`SELECT description FROM recurring_rules WHERE id = ?`, activeID).Scan(&activeDescription); err != nil {
				t.Fatal(err)
			}
			if rootDescription != "Base B2" || activeDescription != "Atualizada B2" {
				t.Fatalf("historical=%q active=%q", rootDescription, activeDescription)
			}
			assertRecurringIdentity(t, f.db, f.ids[0], f.ruleID, f.seriesID, 1)
			assertRecurringIdentity(t, f.db, f.ids[1], activeID, f.seriesID, 2)
		})
	}
}

func TestRecurringAll4BB2CalendarGatesAndSimpleRebase(t *testing.T) {
	t.Run("simple_sequence_one_date", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		rr := f.request(t, 1, map[string]string{"data": "2026-01-20"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		want := []string{"2026-01-20", "2026-02-20", "2026-03-20"}
		for i, id := range f.ids {
			var civil string
			if err := f.db.QueryRow(`SELECT date_civil FROM transactions WHERE id = ?`, id).Scan(&civil); err != nil {
				t.Fatal(err)
			}
			if civil != want[i] {
				t.Fatalf("%s date = %q, want %q", id, civil, want[i])
			}
		}
	})
	t.Run("simple_sequence_one_frequency", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		rr := f.request(t, 1, map[string]string{"recorrencia": "semanal"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		want := []string{"2026-01-10", "2026-01-17", "2026-01-24"}
		for i, id := range f.ids {
			var civil string
			_ = f.db.QueryRow(`SELECT date_civil FROM transactions WHERE id = ?`, id).Scan(&civil)
			if civil != want[i] {
				t.Fatalf("%s date = %q, want %q", id, civil, want[i])
			}
		}
	})
	t.Run("sequence_two_calendar_conflict", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		rr := f.request(t, 2, map[string]string{"data": "2026-02-11"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2Snapshot(t, f.db, f.seriesID); after != before {
			t.Fatalf("sequence two conflict performed writes")
		}
	})
	t.Run("chain_calendar_conflict", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		recurringAll4BB2MakeChain(t, f)
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		rr := f.request(t, 1, map[string]string{"data": "2026-01-11"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2Snapshot(t, f.db, f.seriesID); after != before {
			t.Fatalf("chain conflict performed writes")
		}
	})
}

func TestRecurringAll4BB2EndOfMonthAndLeapYearCalendar(t *testing.T) {
	tests := []struct {
		name  string
		start string
		date  string
		want  []string
	}{
		{"month_end", "2026-01-10", "2026-01-31", []string{"2026-01-31", "2026-02-28", "2026-03-31"}},
		{"leap_year", "2024-01-10", "2024-01-31", []string{"2024-01-31", "2024-02-29", "2024-03-31"}},
		{"year_turn", "2026-11-10", "2026-12-31", []string{"2026-12-31", "2027-01-31", "2027-02-28"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, tc.start, nil)
			defer f.close()
			rr := f.request(t, 1, map[string]string{"data": tc.date, "recorrencia": "mensal"})
			recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
			for i, id := range f.ids {
				var unix int64
				var civil string
				if err := f.db.QueryRow(`SELECT date, date_civil FROM transactions WHERE id = ?`, id).Scan(&unix, &civil); err != nil {
					t.Fatal(err)
				}
				if civil != tc.want[i] || civildate.LegacyUnixToCivilDateUTC(unix).String() != tc.want[i] {
					t.Fatalf("%s date pair = (%d,%q), want %q", id, unix, civil, tc.want[i])
				}
			}
		})
	}
}

func TestRecurringAll4BB2BusinessDueIntent(t *testing.T) {
	offset := int64(10)
	tests := []struct {
		name          string
		initialOffset *int64
		overrides     map[string]string
		wantDue       []string
		wantOff       *int64
	}{
		{"preserve", &offset, nil, []string{"2026-01-20", "2026-02-20", "2026-03-20"}, &offset},
		{"clear", &offset, map[string]string{"due_date": ""}, []string{"", "", ""}, nil},
		{"set_plus_ten", nil, map[string]string{"due_date": "2026-01-20"}, []string{"2026-01-20", "2026-02-20", "2026-03-20"}, &offset},
		{"set_zero", &offset, map[string]string{"due_date": "2026-01-10"}, []string{"2026-01-10", "2026-02-10", "2026-03-10"}, int64Ptr4BB2(0)},
		{"set_negative", &offset, map[string]string{"due_date": "2026-01-05"}, []string{"2026-01-05", "2026-02-05", "2026-03-05"}, int64Ptr4BB2(-5)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", tc.initialOffset)
			defer f.close()
			rr := f.request(t, 1, tc.overrides)
			recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
			var gotOffset sql.NullInt64
			if err := f.db.QueryRow(`SELECT due_date_offset_days FROM recurring_rules WHERE id = ?`, f.ruleID).Scan(&gotOffset); err != nil {
				t.Fatal(err)
			}
			if tc.wantOff == nil {
				if gotOffset.Valid {
					t.Fatalf("offset = %v, want NULL", gotOffset)
				}
			} else if !gotOffset.Valid || gotOffset.Int64 != *tc.wantOff {
				t.Fatalf("offset = %v, want %d", gotOffset, *tc.wantOff)
			}
			for i, id := range f.ids {
				var due sql.NullString
				if err := f.db.QueryRow(`SELECT due_date_civil FROM transactions WHERE id = ?`, id).Scan(&due); err != nil {
					t.Fatal(err)
				}
				if due.String != tc.wantDue[i] || due.Valid != (tc.wantDue[i] != "") {
					t.Fatalf("%s due = %v, want %q", id, due, tc.wantDue[i])
				}
			}
		})
	}
}

func TestRecurringAll4BB2BusinessSetPlusTenMaterializes(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", nil)
	defer f.close()
	rr := f.request(t, 1, map[string]string{"due_date": "2026-01-20"})
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	execTestSQL(t, f.db, `UPDATE recurring_series SET total_occurrences=4 WHERE id=?`, f.seriesID)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET total_occurrences=4 WHERE id=?`, f.ruleID)
	if err := EnsureRecurringMaterialized(f.db, "ws-test", MustCivilWriteDate("2026-04-30")); err != nil {
		t.Fatalf("materialize +10: %v", err)
	}
	var date, due string
	if err := f.db.QueryRow(`SELECT date_civil, due_date_civil FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=4`, f.seriesID).Scan(&date, &due); err != nil {
		t.Fatal(err)
	}
	if date != "2026-04-10" || due != "2026-04-20" {
		t.Fatalf("materialized +10 date=%q due=%q", date, due)
	}
}

func TestRecurringAll4BB2BusinessPreserveLegacyDueWithoutCanonicalizing(t *testing.T) {
	offset := int64(10)
	f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", &offset)
	defer f.close()
	execTestSQL(t, f.db, `UPDATE transactions SET due_date_civil=NULL WHERE workspace_id='ws-test' AND series_id=?`, f.seriesID)
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	var canonicalized int
	if err := f.db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND due_date_civil IS NOT NULL`, f.seriesID).Scan(&canonicalized); err != nil {
		t.Fatal(err)
	}
	if canonicalized != 0 {
		t.Fatalf("preserve canonicalized %d legacy due pairs", canonicalized)
	}
}

func int64Ptr4BB2(value int64) *int64 { return &value }

func TestRecurringAll4BB2PersonalAndBusinessContacts(t *testing.T) {
	t.Run("personal_stays_without_due_or_contact", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		rr := f.request(t, 1, map[string]string{"contact_id": "contact-b2"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		for _, id := range f.ids {
			var contact, due sql.NullString
			_ = f.db.QueryRow(`SELECT contact_id, due_date_civil FROM transactions WHERE id = ?`, id).Scan(&contact, &due)
			if contact.Valid || due.Valid {
				t.Fatalf("personal %s contact=%v due=%v", id, contact, due)
			}
		}
	})
	t.Run("business_local_contact", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", nil)
		defer f.close()
		execTestSQL(t, f.db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-b2-new', 'ws-test', 'Novo', 'vendor', ?)`, time.Now().Unix())
		rr := f.request(t, 1, map[string]string{"contact_id": "contact-b2-new"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		for _, id := range f.ids {
			var contact string
			_ = f.db.QueryRow(`SELECT contact_id FROM transactions WHERE id = ?`, id).Scan(&contact)
			if contact != "contact-b2-new" {
				t.Fatalf("%s contact = %q", id, contact)
			}
		}
	})
	t.Run("foreign_contact_conflict", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", nil)
		defer f.close()
		seedOtherWorkspaceForRecurringCreate(t, f.db)
		execTestSQL(t, f.db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-foreign-b2', 'ws-other', 'Fora', 'client', ?)`, time.Now().Unix())
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		rr := f.request(t, 1, map[string]string{"contact_id": "contact-foreign-b2"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("foreign contact conflict performed writes")
		}
	})
}

func TestRecurringAll4BB2ProtectedMaterializerCompatibility(t *testing.T) {
	offset := int64(10)
	tests := []struct {
		name      string
		business  bool
		offset    *int64
		overrides map[string]string
		want      int
	}{
		{"competence_conflict", false, nil, map[string]string{"data": "2026-01-11"}, http.StatusConflict},
		{"due_conflict", true, &offset, map[string]string{"due_date": "2026-01-21"}, http.StatusConflict},
		{"account_conflict", false, nil, map[string]string{"origem_conta_id": "checking-alt-b2"}, http.StatusConflict},
		{"type_conflict", false, nil, map[string]string{"tipo": "receita"}, http.StatusConflict},
		{"metadata_allowed", false, nil, nil, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, tc.business, "2026-01-10", tc.offset)
			defer f.close()
			execTestSQL(t, f.db, `UPDATE transactions SET status = 'paid' WHERE id = ?`, f.ids[1])
			protectedBefore := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1])
			before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
			rr := f.request(t, 1, tc.overrides)
			recurringAll4BB2AssertStatus(t, rr, tc.want)
			if got := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1]); got != protectedBefore {
				t.Fatalf("protected occurrence changed: before=%q after=%q", protectedBefore, got)
			}
			if tc.want == http.StatusConflict && recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
				t.Fatal("protected compatibility conflict performed writes")
			}
			if tc.want == http.StatusOK {
				recurringAll4BB2AssertOccurrence(t, f.db, f.ids[0], "Atualizada B2", 20000)
				recurringAll4BB2AssertOccurrence(t, f.db, f.ids[2], "Atualizada B2", 20000)
			}
		})
	}
}

func recurringAll4BB2OccurrenceSnapshot(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	parts := []string{
		recurringAll4BB2RowsSnapshot(t, db, "transaction", `SELECT * FROM transactions WHERE id=? AND workspace_id='ws-test' ORDER BY id`, id),
		recurringAll4BB2RowsSnapshot(t, db, "history", `SELECT * FROM transaction_status_history WHERE transaction_id=? AND workspace_id='ws-test' ORDER BY id`, id),
		recurringAll4BB2RowsSnapshot(t, db, "invoice", `SELECT i.* FROM invoices i JOIN transactions t ON t.invoice_id=i.id WHERE t.id=? AND t.workspace_id='ws-test' ORDER BY i.id`, id),
		recurringAll4BB2RowsSnapshot(t, db, "payments", `
			SELECT ip.* FROM invoice_payments ip
			WHERE ip.workspace_id='ws-test' AND (
				ip.transaction_id=? OR ip.invoice_id IN (
					SELECT invoice_id FROM transactions WHERE id=? AND workspace_id='ws-test'
				)
			) ORDER BY ip.id`, id, id),
		recurringAll4BB2RowsSnapshot(t, db, "ledger", `
			SELECT l.* FROM box_virtual_ledger l JOIN boxes b ON b.id=l.box_id
			WHERE b.workspace_id='ws-test' AND l.source_transaction_id=? ORDER BY l.id`, id),
	}
	return strings.Join(parts, "\n")
}

func TestRecurringAll4BB2ProtectedEvidenceIsPreserved(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *recurringAll4BB2Fixture)
	}{
		{"paid_status", func(t *testing.T, f *recurringAll4BB2Fixture) {
			execTestSQL(t, f.db, `UPDATE transactions SET status='paid' WHERE id=?`, f.ids[1])
		}},
		{"paid_at", func(t *testing.T, f *recurringAll4BB2Fixture) {
			execTestSQL(t, f.db, `UPDATE transactions SET paid_at=? WHERE id=?`, testUnixDate("2026-02-10"), f.ids[1])
		}},
		{"paid_date_civil", func(t *testing.T, f *recurringAll4BB2Fixture) {
			execTestSQL(t, f.db, `UPDATE transactions SET paid_at=?, paid_date_civil='2026-02-10' WHERE id=?`, testUnixDate("2026-02-10"), f.ids[1])
		}},
		{"status_history", func(t *testing.T, f *recurringAll4BB2Fixture) {
			execTestSQL(t, f.db, `INSERT INTO transaction_status_history (id, transaction_id, workspace_id, user_id, previous_status, new_status, source, created_at) VALUES ('hist-b2', ?, 'ws-test', 'user-test', 'pending', 'paid', 'manual', ?)`, f.ids[1], time.Now().Unix())
		}},
		{"invoice_closed", func(t *testing.T, f *recurringAll4BB2Fixture) { recurringAll4BB2LinkInvoice(t, f, "CLOSED", false) }},
		{"invoice_paid", func(t *testing.T, f *recurringAll4BB2Fixture) { recurringAll4BB2LinkInvoice(t, f, "PAID", false) }},
		{"active_invoice_payment", func(t *testing.T, f *recurringAll4BB2Fixture) { recurringAll4BB2LinkInvoice(t, f, "OPEN", true) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
			defer f.close()
			tc.setup(t, f)
			before := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1])
			seriesBefore := recurringAll4BB2RowsSnapshot(t, f.db, "series", `SELECT * FROM recurring_series WHERE id=? AND workspace_id='ws-test'`, f.seriesID)
			financialBefore := recurringAll4BB2FinancialSnapshot(t, f.db)
			rr := f.request(t, 1, nil)
			recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
			if after := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1]); after != before {
				t.Fatalf("protected evidence changed occurrence: before=%q after=%q", before, after)
			}
			if seriesAfter := recurringAll4BB2RowsSnapshot(t, f.db, "series", `SELECT * FROM recurring_series WHERE id=? AND workspace_id='ws-test'`, f.seriesID); seriesAfter != seriesBefore {
				t.Fatal("protected metadata edit changed recurring series identity")
			}
			if financialAfter := recurringAll4BB2FinancialSnapshot(t, f.db); financialAfter != financialBefore {
				t.Fatal("protected metadata edit changed balances, invoices, payments, or reserves")
			}
			recurringAll4BB2AssertOccurrence(t, f.db, f.ids[0], "Atualizada B2", 20000)
			recurringAll4BB2AssertOccurrence(t, f.db, f.ids[2], "Atualizada B2", 20000)
		})
	}
}

func recurringAll4BB2LinkInvoice(t *testing.T, f *recurringAll4BB2Fixture, status string, payment bool) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, f.db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('invoice-all-b2', 'checking-test', '2026-02', ?, ?, ?, ?)`, testUnixDate("2026-02-20"), testUnixDate("2026-03-10"), status, now)
	execTestSQL(t, f.db, `UPDATE transactions SET invoice_id = 'invoice-all-b2' WHERE id = ?`, f.ids[1])
	if payment {
		execTestSQL(t, f.db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at) VALUES ('invoice-payment-all-b2', 'ws-test', 'invoice-all-b2', 'checking-test', ?, 10000, ?, 'manual', ?)`, f.ids[1], now, now)
	}
}

func TestRecurringAll4BB2ExceptionAllowsCalendarAndStaysIntact(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	execTestSQL(t, f.db, `UPDATE transactions SET is_recurring_exception=1, recurring_exception_reason='manual_edit', date=?, date_civil='2026-02-15', description='Exceção B2' WHERE id=?`, testUnixDate("2026-02-15"), f.ids[1])
	before := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1])
	rr := f.request(t, 1, map[string]string{"data": "2026-01-20"})
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	if after := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1]); after != before {
		t.Fatalf("exception changed: before=%q after=%q", before, after)
	}
	var thirdDate string
	_ = f.db.QueryRow(`SELECT date_civil FROM transactions WHERE id=?`, f.ids[2]).Scan(&thirdDate)
	if thirdDate != "2026-03-20" {
		t.Fatalf("mutable sequence three date = %q, want 2026-03-20", thirdDate)
	}
}

func TestRecurringAll4BB2PaidIsOccurrenceLocalAndIdempotent(t *testing.T) {
	offset := int64(10)
	f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", &offset)
	defer f.close()
	var balanceBefore int64
	_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceBefore)
	rr := f.request(t, 1, map[string]string{"status_pagamento": "paid"})
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	var balanceAfter int64
	_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceAfter)
	if balanceBefore-balanceAfter != 20000 {
		t.Fatalf("paid balance delta = %d, want 20000", balanceBefore-balanceAfter)
	}
	for index, id := range f.ids {
		var status string
		var due, paidAt sql.NullInt64
		var paidCivil sql.NullString
		_ = f.db.QueryRow(`SELECT status, due_date, paid_at, paid_date_civil FROM transactions WHERE id=?`, id).Scan(&status, &due, &paidAt, &paidCivil)
		wantStatus := "pending"
		if index == 0 {
			wantStatus = "paid"
		}
		if status != wantStatus || !due.Valid || paidAt.Valid || paidCivil.Valid {
			t.Fatalf("%s status=%q due=%v paid_at=%v paid_civil=%v", id, status, due, paidAt, paidCivil)
		}
	}
	var ruleStatus string
	var ruleOffset, rulePolicy sql.NullString
	_ = f.db.QueryRow(`SELECT default_payment_status, CAST(due_date_offset_days AS TEXT), due_date_policy FROM recurring_rules WHERE id=?`, f.ruleID).Scan(&ruleStatus, &ruleOffset, &rulePolicy)
	if ruleStatus != "PENDING" || !ruleOffset.Valid || ruleOffset.String != "10" || rulePolicy.Valid {
		t.Fatalf("rule recurring state = %q offset=%v policy=%v", ruleStatus, ruleOffset, rulePolicy)
	}

	rr = f.request(t, 1, map[string]string{"status_pagamento": "paid"})
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	var balanceRepeated int64
	_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceRepeated)
	if balanceRepeated != balanceAfter {
		t.Fatalf("repeated paid changed balance: first=%d repeated=%d", balanceAfter, balanceRepeated)
	}

	execTestSQL(t, f.db, `UPDATE recurring_series SET total_occurrences=4 WHERE id=?`, f.seriesID)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET total_occurrences=4 WHERE id=?`, f.ruleID)
	if err := EnsureRecurringMaterialized(f.db, "ws-test", MustCivilWriteDate("2026-04-30")); err != nil {
		t.Fatalf("materialize after paid: %v", err)
	}
	var status string
	var due, paidAt sql.NullInt64
	if err := f.db.QueryRow(`SELECT status, due_date, paid_at FROM transactions WHERE series_id=? AND recurrence_sequence=4`, f.seriesID).Scan(&status, &due, &paidAt); err != nil {
		t.Fatalf("load materialized paid occurrence: %v", err)
	}
	if status != "pending" || !due.Valid || paidAt.Valid {
		t.Fatalf("materialized status=%q due=%v paid_at=%v", status, due, paidAt)
	}
}

func TestRecurringAll4BB2NotesAndUploadRequireSingle(t *testing.T) {
	t.Run("identical_note", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		rr := f.request(t, 1, map[string]string{"anotacoes": "nota-base"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		for _, id := range f.ids {
			var note string
			_ = f.db.QueryRow(`SELECT notes FROM transactions WHERE id=?`, id).Scan(&note)
			if note != "nota-base" {
				t.Fatalf("%s note = %q", id, note)
			}
		}
	})
	t.Run("different_note", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		rr := f.request(t, 1, map[string]string{"anotacoes": "outra"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("different note performed writes")
		}
	})
	t.Run("whitespace_only_difference", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		rr := f.request(t, 1, map[string]string{"anotacoes": " nota-base"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("whitespace-only note difference performed writes")
		}
	})
	t.Run("upload", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		uploadRoot := t.TempDir()
		t.Setenv("UPLOADS_DIR", uploadRoot)
		workspaceDir := filepath.Join(uploadRoot, "ws-test")
		if err := os.MkdirAll(workspaceDir, 0700); err != nil {
			t.Fatal(err)
		}
		oldAttachment := filepath.Join(workspaceDir, "existing.pdf")
		if err := os.WriteFile(oldAttachment, recurringAll4BB2PDFBytes("existing"), 0600); err != nil {
			t.Fatal(err)
		}
		execTestSQL(t, f.db, `UPDATE transactions SET attachment_path='uploads/ws-test/existing.pdf' WHERE id=?`, f.ids[0])
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		filesBefore := recurringAll4BB2DirectorySnapshot(t, uploadRoot)
		rr := recurringAll4BB2UploadRequest(t, f)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("upload conflict performed writes")
		}
		if filesAfter := recurringAll4BB2DirectorySnapshot(t, uploadRoot); filesAfter != filesBefore {
			t.Fatalf("upload conflict changed directory: before=%q after=%q", filesBefore, filesAfter)
		}
	})
}

func recurringAll4BB2DirectorySnapshot(t *testing.T, root string) string {
	t.Helper()
	var entries []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		kind := "file"
		digest := "-"
		target := "-"
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
			target, err = os.Readlink(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(target))
			digest = fmt.Sprintf("%x", sum[:])
		case info.IsDir():
			kind = "dir"
		case info.Mode().IsRegular():
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(content)
			digest = fmt.Sprintf("%x", sum[:])
		default:
			kind = info.Mode().Type().String()
		}
		entries = append(entries, fmt.Sprintf("%s|%s|size=%d|mode=%#o|sha256=%s|target=%s",
			filepath.ToSlash(relative), kind, info.Size(), info.Mode().Perm(), digest, filepath.ToSlash(target)))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

func recurringAll4BB2UploadRequest(t *testing.T, f *recurringAll4BB2Fixture) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"valor": "200,00", "descricao": "Atualizada B2", "data": "2026-01-10",
		"tipo": "despesa", "origem_conta_id": "checking-test", "categoria_id": "cat-expense-rec",
		"status_pagamento": "pending", "recorrencia": "mensal", "escopo": "all",
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_ = writer.WriteField(key, fields[key])
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="anexo"; filename="b2.pdf"`)
	header.Set("Content-Type", "application/pdf")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(recurringAll4BB2PDFBytes("blocked-before-persistence"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+f.ids[0], &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	f.handler.HandleAtualizarTransacao(rr, req, f.ids[0])
	return rr
}

func recurringAll4BB2PDFBytes(label string) []byte {
	objects := []string{
		"",
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 1 1] /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\nendstream",
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n% ContaBase B2 " + label + "\n")
	offsets := make([]int, len(objects))
	for index := 1; index < len(objects); index++ {
		offsets[index] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index, objects[index])
	}
	xrefOffset := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n", len(objects))
	pdf.WriteString("0000000000 65535 f \n")
	for index := 1; index < len(objects); index++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects), xrefOffset)
	return pdf.Bytes()
}

func recurringAll4BB2SeedCard(t *testing.T, db *sql.DB, accountID string, closingDay, dueDay int64) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, 'ws-test', ?, 'CREDIT_CARD', 0, 0, ?, ?)
	`, accountID, accountID, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES (?, ?, ?, ?, 500000)`, "cc-"+accountID, accountID, closingDay, dueDay)
}

func recurringAll4BB2MakeMutableCardSeries(t *testing.T, f *recurringAll4BB2Fixture, accountID string) map[string]string {
	t.Helper()
	execTestSQL(t, f.db, `UPDATE recurring_rules SET account_id=?, default_payment_status='PENDING' WHERE id=?`, accountID, f.ruleID)
	oldInvoices := make(map[string]string, len(f.ids))
	for _, id := range f.ids {
		var date int64
		if err := f.db.QueryRow(`SELECT date FROM transactions WHERE id=?`, id).Scan(&date); err != nil {
			t.Fatal(err)
		}
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		invoiceID, _, _, _, _, err := ensureOpenInvoiceTx(tx, "ws-test", accountID, date)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE transactions SET account_id=?, invoice_id=?, status='pending' WHERE id=? AND workspace_id='ws-test'`, accountID, invoiceID, id); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		oldInvoices[id] = invoiceID
	}
	return oldInvoices
}

func recurringAll4BB2AssertInvoicesMoved(t *testing.T, f *recurringAll4BB2Fixture, oldInvoices map[string]string, accountID string, protectedID string) {
	t.Helper()
	oldSet := make(map[string]bool)
	expectedNewTotal := int64(0)
	for _, id := range f.ids {
		oldSet[oldInvoices[id]] = true
		var gotAccount, status string
		var invoiceID sql.NullString
		if err := f.db.QueryRow(`SELECT account_id, status, invoice_id FROM transactions WHERE id=? AND workspace_id='ws-test'`, id).Scan(&gotAccount, &status, &invoiceID); err != nil {
			t.Fatal(err)
		}
		if id == protectedID {
			if gotAccount == accountID || !invoiceID.Valid || invoiceID.String != oldInvoices[id] {
				t.Fatalf("protected %s account=%q invoice=%v", id, gotAccount, invoiceID)
			}
			continue
		}
		if gotAccount != accountID || status != "paid" || !invoiceID.Valid || invoiceID.String == "" || invoiceID.String == oldInvoices[id] {
			t.Fatalf("moved %s account=%q status=%q old_invoice=%q new_invoice=%v", id, gotAccount, status, oldInvoices[id], invoiceID)
		}
		expectedNewTotal += 20000
		var invoiceStatus string
		if err := f.db.QueryRow(`SELECT status FROM invoices WHERE id=?`, invoiceID.String).Scan(&invoiceStatus); err != nil {
			t.Fatal(err)
		}
		if invoiceStatus != "OPEN" && invoiceStatus != "CLOSED" && invoiceStatus != "PAID" {
			t.Fatalf("new invoice %s status=%q", invoiceID.String, invoiceStatus)
		}
	}
	var newTotal int64
	if err := f.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE workspace_id='ws-test' AND series_id=? AND account_id=?`, f.seriesID, accountID).Scan(&newTotal); err != nil {
		t.Fatal(err)
	}
	if newTotal != expectedNewTotal {
		t.Fatalf("new invoice aggregate=%d want=%d", newTotal, expectedNewTotal)
	}
	var oldTotal int64
	oldAccountID := ""
	for invoiceID := range oldSet {
		var total int64
		if oldAccountID == "" {
			if err := f.db.QueryRow(`SELECT account_id FROM invoices WHERE id=?`, invoiceID).Scan(&oldAccountID); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM transactions WHERE workspace_id='ws-test' AND invoice_id=?`, invoiceID).Scan(&total); err != nil {
			t.Fatal(err)
		}
		oldTotal += total
	}
	expectedOldTotal := int64(0)
	if protectedID != "" {
		expectedOldTotal = 10000
	}
	if oldAccountID != accountID && oldTotal != expectedOldTotal {
		t.Fatalf("old invoice aggregate=%d want=%d", oldTotal, expectedOldTotal)
	}
}

func TestRecurringAll4BB2InvoiceMovesAndProtectedRows(t *testing.T) {
	t.Run("checking_to_card", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2027-01-10", nil)
		defer f.close()
		recurringAll4BB2SeedCard(t, f.db, "card-all-b2-A", 20, 10)
		paymentsBefore := recurringAll4BB2RowsSnapshot(t, f.db, "payments", `SELECT * FROM invoice_payments WHERE workspace_id='ws-test' ORDER BY id`)
		rr := f.request(t, 1, map[string]string{"origem_conta_id": "card-all-b2-A"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		for _, id := range f.ids {
			var account, status string
			var invoice sql.NullString
			if err := f.db.QueryRow(`SELECT account_id, status, invoice_id FROM transactions WHERE id=?`, id).Scan(&account, &status, &invoice); err != nil {
				t.Fatal(err)
			}
			if account != "card-all-b2-A" || status != "paid" || !invoice.Valid {
				t.Fatalf("%s account=%q status=%q invoice=%v", id, account, status, invoice)
			}
		}
		if paymentsAfter := recurringAll4BB2RowsSnapshot(t, f.db, "payments", `SELECT * FROM invoice_payments WHERE workspace_id='ws-test' ORDER BY id`); paymentsAfter != paymentsBefore {
			t.Fatal("checking-to-card changed invoice payments")
		}
	})

	t.Run("card_A_to_card_B", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2027-01-10", nil)
		defer f.close()
		recurringAll4BB2SeedCard(t, f.db, "card-all-b2-A", 20, 10)
		recurringAll4BB2SeedCard(t, f.db, "card-all-b2-B", 20, 10)
		seedOtherWorkspaceForRecurringCreate(t, f.db)
		now := time.Now().Unix()
		execTestSQL(t, f.db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-all-b2-foreign', 'ws-other', 'Foreign Card', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
		execTestSQL(t, f.db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-all-b2-foreign', 'card-all-b2-foreign', 20, 10, 500000)`)
		execTestSQL(t, f.db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('invoice-all-b2-foreign', 'card-all-b2-foreign', '2027-01', ?, ?, 'OPEN', ?)`, testUnixDate("2027-01-20"), testUnixDate("2027-02-10"), now)
		foreignBefore := recurringAll4BB2ForeignFinancialSnapshot(t, f.db)
		oldInvoices := recurringAll4BB2MakeMutableCardSeries(t, f, "card-all-b2-A")
		rr := f.request(t, 1, map[string]string{"origem_conta_id": "card-all-b2-B"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		recurringAll4BB2AssertInvoicesMoved(t, f, oldInvoices, "card-all-b2-B", "")
		if foreignAfter := recurringAll4BB2ForeignFinancialSnapshot(t, f.db); foreignAfter != foreignBefore {
			t.Fatal("card move changed foreign workspace financial state")
		}
	})

	t.Run("same_card_new_dates", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2027-01-10", nil)
		defer f.close()
		recurringAll4BB2SeedCard(t, f.db, "card-all-b2-A", 20, 10)
		oldInvoices := recurringAll4BB2MakeMutableCardSeries(t, f, "card-all-b2-A")
		rr := f.request(t, 1, map[string]string{"origem_conta_id": "card-all-b2-A", "data": "2027-02-25"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		recurringAll4BB2AssertInvoicesMoved(t, f, oldInvoices, "card-all-b2-A", "")
	})

	for _, invoiceStatus := range []string{"CLOSED", "PAID"} {
		t.Run("protected_"+strings.ToLower(invoiceStatus), func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2027-01-10", nil)
			defer f.close()
			recurringAll4BB2SeedCard(t, f.db, "card-all-b2-A", 20, 10)
			recurringAll4BB2SeedCard(t, f.db, "card-all-b2-B", 20, 10)
			oldInvoices := recurringAll4BB2MakeMutableCardSeries(t, f, "card-all-b2-A")
			execTestSQL(t, f.db, `UPDATE transactions SET is_recurring_exception=1, recurring_exception_reason='manual_edit' WHERE id=?`, f.ids[1])
			execTestSQL(t, f.db, `UPDATE invoices SET status=? WHERE id=?`, invoiceStatus, oldInvoices[f.ids[1]])
			protectedBefore := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1])
			rr := f.request(t, 1, map[string]string{"origem_conta_id": "card-all-b2-B"})
			recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
			if protectedAfter := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1]); protectedAfter != protectedBefore {
				t.Fatalf("protected invoice row changed: before=%q after=%q", protectedBefore, protectedAfter)
			}
			recurringAll4BB2AssertInvoicesMoved(t, f, oldInvoices, "card-all-b2-B", f.ids[1])
		})
	}
}

func recurringAll4BB2ForeignFinancialSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return strings.Join([]string{
		recurringAll4BB2RowsSnapshot(t, db, "accounts", `SELECT * FROM accounts WHERE workspace_id='ws-other' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "invoices", `SELECT i.* FROM invoices i JOIN accounts a ON a.id=i.account_id WHERE a.workspace_id='ws-other' ORDER BY i.id`),
		recurringAll4BB2RowsSnapshot(t, db, "payments", `SELECT * FROM invoice_payments WHERE workspace_id='ws-other' ORDER BY id`),
	}, "\n")
}

func TestRecurringAll4BB2InvoiceRollbackFailures(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{"after_new_invoice_resolution", `CREATE TRIGGER fail_b2_invoice_insert AFTER INSERT ON invoices WHEN NEW.account_id='card-all-b2-B' BEGIN SELECT RAISE(ABORT, 'fail new invoice'); END`},
		{"after_first_item", `CREATE TRIGGER fail_b2_invoice_first AFTER UPDATE OF invoice_id ON transactions WHEN OLD.id='tx-all-b2-1' BEGIN SELECT RAISE(ABORT, 'fail first item'); END`},
		{"after_nth_item", `CREATE TRIGGER fail_b2_invoice_nth AFTER UPDATE OF invoice_id ON transactions WHEN OLD.id='tx-all-b2-2' BEGIN SELECT RAISE(ABORT, 'fail nth item'); END`},
		{"after_old_invoice_reconcile", `CREATE TRIGGER fail_b2_old_reconcile AFTER UPDATE OF status ON invoices WHEN OLD.account_id='card-all-b2-A' BEGIN SELECT RAISE(ABORT, 'fail old reconcile'); END`},
		{"after_new_invoice_reconcile", `CREATE TRIGGER fail_b2_new_reconcile AFTER UPDATE OF status ON invoices WHEN OLD.account_id='card-all-b2-B' BEGIN SELECT RAISE(ABORT, 'fail new reconcile'); END`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
			defer f.close()
			recurringAll4BB2SeedCard(t, f.db, "card-all-b2-A", 20, 10)
			recurringAll4BB2SeedCard(t, f.db, "card-all-b2-B", 20, 10)
			recurringAll4BB2MakeMutableCardSeries(t, f, "card-all-b2-A")
			before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
			execTestSQL(t, f.db, tc.trigger)
			rr := f.request(t, 1, map[string]string{"origem_conta_id": "card-all-b2-B"})
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d want=500 body=%q", rr.Code, rr.Body.String())
			}
			if after := recurringAll4BB2Snapshot(t, f.db, f.seriesID); after != before {
				t.Fatalf("%s did not rollback exact state", tc.name)
			}
		})
	}
}

func newRecurringAll4BB2ReserveFixture(t *testing.T) *recurringAll4BB2Fixture {
	t.Helper()
	db := openTestDB(t)
	seedBoxConsumeCreationScenario(t, db)
	handlerInsert := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	totalOccurrences := int64(3)
	_, err := handlerInsert.insertTransaction(
		"EXPENSE", 3000, "Recurring All B2 Reserve", "", "",
		MustCivilWriteDate("2026-07-05"), "checking-test", "", "cat-expense-direct",
		1, "pending", true, "MONTHLY", "", OptionalCivilWriteDate{}, &totalOccurrences, "", false,
	)
	if err != nil {
		t.Fatalf("insert B2 reserve recurrence: %v", err)
	}
	ids := recurringTransactionIDsByDescription(t, db, "Recurring All B2 Reserve")
	if len(ids) != 3 {
		t.Fatalf("B2 reserve ids=%d", len(ids))
	}
	var ruleID, seriesID string
	if err := db.QueryRow(`SELECT recurring_rule_id, series_id FROM transactions WHERE id=?`, ids[0]).Scan(&ruleID, &seriesID); err != nil {
		t.Fatal(err)
	}
	return &recurringAll4BB2Fixture{
		db:       db,
		handler:  testPaidInvoiceMutationHandler(db),
		seriesID: seriesID,
		ruleID:   ruleID,
		ids:      ids,
	}
}

func recurringAll4BB2ReserveRequest(t *testing.T, f *recurringAll4BB2Fixture) *httptest.ResponseRecorder {
	t.Helper()
	return updateTransactionForReserveScopeTest(t, f.handler, f.ids[0], "all", map[string]string{
		"valor":            "12,00",
		"descricao":        "Recurring All B2 Reserve Updated",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-parent",
		"status_pagamento": "pending",
		"recorrencia":      "MONTHLY",
		"escopo":           "all",
	})
}

func TestRecurringAll4BB2ReserveRollbackAndProtectedIdempotency(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
	}{
		{"compensation", `CREATE TRIGGER fail_b2_reserve_reversal AFTER INSERT ON box_virtual_ledger WHEN NEW.type='REVERSAL' BEGIN SELECT RAISE(ABORT, 'fail reserve reversal'); END`},
		{"consume", `CREATE TRIGGER fail_b2_reserve_consume AFTER INSERT ON box_virtual_ledger WHEN NEW.type='CONSUME' AND NEW.amount=-1200 BEGIN SELECT RAISE(ABORT, 'fail reserve consume'); END`},
	} {
		t.Run("rollback_"+tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2ReserveFixture(t)
			defer f.close()
			before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
			execTestSQL(t, f.db, tc.trigger)
			rr := recurringAll4BB2ReserveRequest(t, f)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d want=500 body=%q", rr.Code, rr.Body.String())
			}
			if after := recurringAll4BB2Snapshot(t, f.db, f.seriesID); after != before {
				t.Fatalf("reserve %s failure did not rollback exact state", tc.name)
			}
		})
	}

	t.Run("rollback_after_consume_before_transaction_write", func(t *testing.T) {
		f := newRecurringAll4BB2ReserveFixture(t)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		execTestSQL(t, f.db, fmt.Sprintf(`
			CREATE TRIGGER fail_b2_after_reserve_consume
			BEFORE UPDATE OF description ON transactions
			WHEN OLD.id='%s'
			BEGIN SELECT RAISE(ABORT, 'fail after reserve consume'); END
		`, f.ids[0]))
		rr := recurringAll4BB2ReserveRequest(t, f)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want=500 body=%q", rr.Code, rr.Body.String())
		}
		if after := recurringAll4BB2Snapshot(t, f.db, f.seriesID); after != before {
			t.Fatal("failure after reserve consume did not rollback exact state")
		}
	})

	t.Run("protected_has_no_reserve_effect", func(t *testing.T) {
		f := newRecurringAll4BB2ReserveFixture(t)
		defer f.close()
		execTestSQL(t, f.db, `UPDATE transactions SET status='paid' WHERE id=?`, f.ids[1])
		protectedBefore := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1])
		rr := recurringAll4BB2ReserveRequest(t, f)
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		if protectedAfter := recurringAll4BB2OccurrenceSnapshot(t, f.db, f.ids[1]); protectedAfter != protectedBefore {
			t.Fatal("protected occurrence produced reserve or transaction effects")
		}
	})

	t.Run("repeat_is_financially_idempotent", func(t *testing.T) {
		f := newRecurringAll4BB2ReserveFixture(t)
		defer f.close()
		rr := recurringAll4BB2ReserveRequest(t, f)
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		financialAfterFirst := recurringAll4BB2FinancialSnapshot(t, f.db)
		rr = recurringAll4BB2ReserveRequest(t, f)
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		if financialAfterRepeat := recurringAll4BB2FinancialSnapshot(t, f.db); financialAfterRepeat != financialAfterFirst {
			t.Fatal("repeated reserve update duplicated financial effects")
		}
	})
}

func TestRecurringAll4BB2TransferBalanceAtomicAndIdempotent(t *testing.T) {
	t.Run("rollback_after_second_account_effect", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		execTestSQL(t, f.db, `CREATE TRIGGER fail_b2_transfer_destination AFTER UPDATE OF current_balance ON accounts WHEN OLD.id='checking-alt-b2' BEGIN SELECT RAISE(ABORT, 'fail transfer destination'); END`)
		rr := f.request(t, 1, map[string]string{
			"tipo":             "transferencia",
			"destino_conta_id": "checking-alt-b2",
			"status_pagamento": "paid",
			"categoria_id":     "",
			"origem_conta_id":  "checking-test",
			"recorrencia":      "mensal",
			"escopo":           "all",
		})
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want=500 body=%q", rr.Code, rr.Body.String())
		}
		if after := recurringAll4BB2Snapshot(t, f.db, f.seriesID); after != before {
			t.Fatal("transfer failure did not rollback first and second account effects")
		}
	})

	t.Run("known_effect_and_repeat", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		var originBefore, destinationBefore int64
		if err := f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&originBefore); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-alt-b2'`).Scan(&destinationBefore); err != nil {
			t.Fatal(err)
		}
		fields := map[string]string{
			"tipo":             "transferencia",
			"destino_conta_id": "checking-alt-b2",
			"status_pagamento": "paid",
			"categoria_id":     "",
		}
		rr := f.request(t, 1, fields)
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		var originAfter, destinationAfter int64
		_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&originAfter)
		_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-alt-b2'`).Scan(&destinationAfter)
		if originBefore-originAfter != 20000 || destinationAfter-destinationBefore != 20000 {
			t.Fatalf("transfer effect origin=%d destination=%d", originBefore-originAfter, destinationAfter-destinationBefore)
		}
		rr = f.request(t, 1, fields)
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		var originRepeat, destinationRepeat int64
		_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&originRepeat)
		_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-alt-b2'`).Scan(&destinationRepeat)
		if originRepeat != originAfter || destinationRepeat != destinationAfter {
			t.Fatal("repeated paid transfer duplicated balance effects")
		}
	})
}

func TestRecurringAll4BB2LegacyBackfillAndPartialConflict(t *testing.T) {
	t.Run("coherent_legacy_backfill", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		execTestSQL(t, f.db, `UPDATE transactions SET series_id=NULL WHERE series_id=?`, f.seriesID)
		execTestSQL(t, f.db, `UPDATE recurring_rules SET series_id=NULL WHERE id=?`, f.ruleID)
		execTestSQL(t, f.db, `DELETE FROM recurring_series WHERE id=?`, f.seriesID)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		var seriesCount int
		if err := f.db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE id=? AND workspace_id='ws-test'`, f.ruleID).Scan(&seriesCount); err != nil {
			t.Fatal(err)
		}
		if seriesCount != 1 {
			t.Fatalf("legacy series count = %d, want 1", seriesCount)
		}
		for index, id := range f.ids {
			assertRecurringIdentity(t, f.db, id, f.ruleID, f.ruleID, int64(index+1))
		}
	})

	t.Run("partial_association_conflict", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		execTestSQL(t, f.db, `UPDATE transactions SET series_id=NULL WHERE id=?`, f.ids[1])
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("partial association conflict performed writes")
		}
	})
}

func TestRecurringAll4BB2StructuralPreflightConflicts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *recurringAll4BB2Fixture)
	}{
		{"zero_active", func(t *testing.T, f *recurringAll4BB2Fixture) {
			execTestSQL(t, f.db, `UPDATE recurring_rules SET active=0 WHERE id=?`, f.ruleID)
		}},
		{"two_active", func(t *testing.T, f *recurringAll4BB2Fixture) {
			now := time.Now().Unix()
			execTestSQL(t, f.db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-all-b2-second', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Second', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 3, ?, 2, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.seriesID, now, now)
		}},
		{"active_root_with_inactive_child", func(t *testing.T, f *recurringAll4BB2Fixture) {
			recurringAll4BB2DropInvariantTriggers(t, f.db)
			defer recurringAll4BB2CreateInvariantTriggers(t, f.db)
			recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-inactive-child", f.ruleID, false, 2, nil)
		}},
		{"inactive_active_inactive", func(t *testing.T, f *recurringAll4BB2Fixture) {
			activeID := recurringAll4BB2MakeChain(t, f)
			recurringAll4BB2DropInvariantTriggers(t, f.db)
			defer recurringAll4BB2CreateInvariantTriggers(t, f.db)
			recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-inactive-grandchild", activeID, false, 3, nil)
		}},
		{"invalid_parent", func(t *testing.T, f *recurringAll4BB2Fixture) {
			recurringAll4BB2MakeChain(t, f)
			execTestSQL(t, f.db, `PRAGMA foreign_keys=OFF`)
			execTestSQL(t, f.db, `UPDATE recurring_rules SET parent_rule_id='missing-b2' WHERE id='rule-all-b2-active'`)
			execTestSQL(t, f.db, `PRAGMA foreign_keys=ON`)
		}},
		{"incoherent_civil_pair", func(t *testing.T, f *recurringAll4BB2Fixture) {
			execTestSQL(t, f.db, `UPDATE transactions SET date_civil='2026-01-11' WHERE id=?`, f.ids[1])
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
			defer f.close()
			tc.setup(t, f)
			before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
			rr := f.request(t, 1, nil)
			recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
			if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
				t.Fatal("structural conflict performed writes")
			}
		})
	}
}

func recurringAll4BB2GlobalFunctionalSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return strings.Join([]string{
		recurringAll4BB2RowsSnapshot(t, db, "workspaces", `SELECT * FROM workspaces ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "series", `SELECT * FROM recurring_series ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "rules", `SELECT * FROM recurring_rules ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "transactions", `SELECT * FROM transactions ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "history", `SELECT * FROM transaction_status_history ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "accounts", `SELECT * FROM accounts ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "invoices", `SELECT i.* FROM invoices i JOIN accounts a ON a.id=i.account_id ORDER BY a.workspace_id, i.id`),
		recurringAll4BB2RowsSnapshot(t, db, "payments", `SELECT * FROM invoice_payments ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "boxes", `SELECT * FROM boxes ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "ledger", `SELECT l.* FROM box_virtual_ledger l JOIN boxes b ON b.id=l.box_id ORDER BY b.workspace_id, l.id`),
		recurringAll4BB2RowsSnapshot(t, db, "contacts", `SELECT * FROM contacts ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "categories", `SELECT * FROM categories ORDER BY workspace_id, id`),
		recurringAll4BB2RowsSnapshot(t, db, "credit_cards", `SELECT c.* FROM credit_cards c JOIN accounts a ON a.id=c.account_id ORDER BY a.workspace_id, c.id`),
		recurringAll4BB2RowsSnapshot(t, db, "materialization", `SELECT * FROM recurring_materialization_state ORDER BY workspace_id`),
	}, "\n")
}

func recurringAll4BB2SeedExternalChild(t *testing.T, f *recurringAll4BB2Fixture, otherWorkspace, active, withDescendant bool) string {
	t.Helper()
	workspaceID := "ws-test"
	userID := "user-test"
	accountID := "checking-test"
	categoryID := "cat-expense-rec"
	if otherWorkspace {
		seedOtherWorkspaceForRecurringCreate(t, f.db)
		workspaceID = "ws-other"
		userID = "user-other"
		accountID = "checking-other"
		categoryID = "cat-expense-other"
	}
	now := time.Now().Unix()
	seriesID := "series-external-leaf-b2"
	childID := "rule-external-leaf-b2"
	activeValue := 0
	if active {
		activeValue = 1
	}

	recurringAll4BB2DropInvariantTriggers(t, f.db)
	defer recurringAll4BB2CreateInvariantTriggers(t, f.db)

	execTestSQL(t, f.db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, ?, ?, 1, 1, ?, ?)
	`, seriesID, workspaceID, userID, now, now)
	execTestSQL(t, f.db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id, parent_rule_id,
			effective_start_sequence, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, 'EXPENSE', 10000, 'External leaf sentinel',
			?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', ?, 1, ?, ?, 2, ?, ?)
	`, childID, workspaceID, userID, accountID, categoryID,
		testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), activeValue, seriesID, f.ruleID, now, now)
	if withDescendant {
		execTestSQL(t, f.db, `UPDATE recurring_rules SET active=0, effective_end_sequence=2 WHERE id=?`, childID)
		execTestSQL(t, f.db, `
			INSERT INTO recurring_rules (
				id, workspace_id, user_id, account_id, category_id, type, amount, description,
				start_date, start_date_civil, starts_at, starts_at_civil, frequency,
				default_payment_status, active, total_occurrences, series_id, parent_rule_id,
				effective_start_sequence, created_at, updated_at
			) VALUES ('rule-external-grandchild-b2', ?, ?, ?, ?, 'EXPENSE', 10000, 'External grandchild sentinel',
				?, '2026-03-10', ?, '2026-03-10', 'MONTHLY', 'PENDING', 1, 1, ?, ?, 3, ?, ?)
		`, workspaceID, userID, accountID, categoryID,
			testUnixDate("2026-03-10"), testUnixDate("2026-03-10"), seriesID, childID, now, now)
	}

	recurringAll4BB2CreateInvariantTriggers(t, f.db)

	return childID
}

func TestRecurringAll4BB2GlobalActiveLeafRejectsExternalChildren(t *testing.T) {
	for _, tc := range []struct {
		name           string
		otherWorkspace bool
		active         bool
		descendant     bool
	}{
		{"same_workspace_inactive", false, false, false},
		{"same_workspace_active", false, true, false},
		{"same_workspace_with_descendant", false, false, true},
		{"other_workspace_inactive", true, false, false},
		{"other_workspace_active", true, true, false},
		{"other_workspace_with_descendant", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
			defer f.close()
			childID := recurringAll4BB2SeedExternalChild(t, f, tc.otherWorkspace, tc.active, tc.descendant)
			before := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
			rr := f.request(t, 1, nil)
			recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
			if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != before {
				t.Fatal("external child conflict changed functional state")
			}
			body := rr.Body.String()
			for _, secret := range []string{childID, "ws-other", "Workspace Other", "series-external-leaf-b2"} {
				if strings.Contains(body, secret) {
					t.Fatalf("generic conflict leaked external identity %q in %q", secret, body)
				}
			}
		})
	}
}

func TestRecurringAll4BB2GlobalActiveLeafIgnoresUnrelatedOrphanParent(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	now := time.Now().Unix()
	execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at) VALUES ('series-orphan-b2', 'ws-test', 'user-test', 1, 1, ?, ?)`, now, now)
	execTestSQL(t, f.db, `PRAGMA foreign_keys=OFF`)
	execTestSQL(t, f.db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id, parent_rule_id,
			effective_start_sequence, created_at, updated_at
		) VALUES ('rule-unrelated-orphan-b2', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec',
			'EXPENSE', 10000, 'Unrelated orphan', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY',
			'PENDING', 1, 1, 'series-orphan-b2', 'missing-parent-b2', 1, ?, ?)
	`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)
	execTestSQL(t, f.db, `PRAGMA foreign_keys=ON`)
	orphanBefore := recurringAll4BB2RowsSnapshot(t, f.db, "orphan", `SELECT * FROM recurring_rules WHERE id='rule-unrelated-orphan-b2'`)
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	if orphanAfter := recurringAll4BB2RowsSnapshot(t, f.db, "orphan", `SELECT * FROM recurring_rules WHERE id='rule-unrelated-orphan-b2'`); orphanAfter != orphanBefore {
		t.Fatal("unrelated orphan was changed")
	}
}

func recurringAll4BB2InsertRuleVersion(t *testing.T, f *recurringAll4BB2Fixture, id, parentID string, active bool, start int64, end *int64) {
	t.Helper()
	now := time.Now().Unix()
	activeValue := 0
	if active {
		activeValue = 1
	}
	var endValue interface{}
	if end != nil {
		endValue = *end
	}
	startDate := MustCivilWriteDate("2026-01-10")
	execTestSQL(t, f.db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount, description,
			start_date, start_date_civil, starts_at, starts_at_civil, frequency,
			default_payment_status, active, total_occurrences, series_id, parent_rule_id,
			effective_start_sequence, effective_end_sequence, created_at, updated_at
		) VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec',
			'EXPENSE', 10000, ?, ?, ?, ?, ?, 'MONTHLY', 'PENDING', ?, 3, ?, ?, ?, ?, ?, ?)
	`, id, id, startDate.LegacyUnix(), startDate.Civil(), startDate.LegacyUnix(), startDate.Civil(), activeValue, f.seriesID, parentID, start, endValue, now, now)
}

func TestRecurringAll4BB2ValidABCDKeepsHistoricalRules(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	zero := int64(0)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET active=0, effective_end_sequence=0 WHERE id=?`, f.ruleID)
	recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-B", f.ruleID, false, 1, &zero)
	recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-C", "rule-all-b2-B", false, 1, &zero)
	recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-D", "rule-all-b2-C", true, 1, nil)
	execTestSQL(t, f.db, `UPDATE transactions SET recurring_rule_id='rule-all-b2-D' WHERE workspace_id='ws-test' AND series_id=?`, f.seriesID)

	historicalBefore := recurringAll4BB2RowsSnapshot(t, f.db, "historical", `
		SELECT * FROM recurring_rules
		WHERE workspace_id='ws-test' AND id IN (?, ?, ?)
		ORDER BY id`, f.ruleID, "rule-all-b2-B", "rule-all-b2-C")
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	historicalAfter := recurringAll4BB2RowsSnapshot(t, f.db, "historical", `
		SELECT * FROM recurring_rules
		WHERE workspace_id='ws-test' AND id IN (?, ?, ?)
		ORDER BY id`, f.ruleID, "rule-all-b2-B", "rule-all-b2-C")
	if historicalAfter != historicalBefore {
		t.Fatal("valid A-B-C-D edit changed historical rules")
	}
	var activeCount, activeChildren int
	if err := f.db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? AND active=1`, f.seriesID).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? AND parent_rule_id='rule-all-b2-D'`, f.seriesID).Scan(&activeChildren); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || activeChildren != 0 {
		t.Fatalf("valid chain active=%d active_children=%d", activeCount, activeChildren)
	}
}

func TestRecurringAll4BB2CrossWorkspaceInputsConflict(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", nil)
	defer f.close()
	seedOtherWorkspaceForRecurringCreate(t, f.db)
	before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
	for _, tc := range []struct {
		name      string
		overrides map[string]string
	}{
		{"account", map[string]string{"origem_conta_id": "checking-other"}},
		{"category", map[string]string{"categoria_id": "cat-expense-other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := f.request(t, 1, tc.overrides)
			recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
			if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
				t.Fatal("cross-workspace conflict performed writes")
			}
		})
	}
}

func TestRecurringAll4BB2HistoricalCrossWorkspaceReferenceConflicts(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	seedOtherWorkspaceForRecurringCreate(t, f.db)
	execTestSQL(t, f.db, `UPDATE transactions SET account_id='checking-other' WHERE id=?`, f.ids[1])
	before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
	if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
		t.Fatal("historical cross-workspace reference conflict performed writes")
	}
}

func TestRecurringAll4BB2RollbackAfterEnsureRuleAndMutableWrites(t *testing.T) {
	t.Run("after_legacy_ensure", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		execTestSQL(t, f.db, `UPDATE transactions SET series_id=NULL WHERE series_id=?`, f.seriesID)
		execTestSQL(t, f.db, `UPDATE recurring_rules SET series_id=NULL WHERE id=?`, f.ruleID)
		execTestSQL(t, f.db, `DELETE FROM recurring_series WHERE id=?`, f.seriesID)
		execTestSQL(t, f.db, `CREATE TRIGGER fail_all_b2_rule BEFORE UPDATE OF description ON recurring_rules WHEN NEW.description='Atualizada B2' BEGIN SELECT RAISE(ABORT, 'simulated B2 rule failure'); END`)
		rr := f.request(t, 1, nil)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%q", rr.Code, rr.Body.String())
		}
		var seriesCount int
		_ = f.db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE id=?`, f.ruleID).Scan(&seriesCount)
		if seriesCount != 0 {
			t.Fatalf("Ensure backfill escaped rollback: series count=%d", seriesCount)
		}
		var ruleSeries sql.NullString
		_ = f.db.QueryRow(`SELECT series_id FROM recurring_rules WHERE id=?`, f.ruleID).Scan(&ruleSeries)
		if ruleSeries.Valid {
			t.Fatalf("Ensure rule backfill escaped rollback: %v", ruleSeries)
		}
	})

	t.Run("during_mutable_updates", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		execTestSQL(t, f.db, `CREATE TRIGGER fail_all_b2_item BEFORE UPDATE OF description ON transactions WHEN OLD.id='tx-all-b2-2' AND NEW.description='Atualizada B2' BEGIN SELECT RAISE(ABORT, 'simulated B2 item failure'); END`)
		rr := f.request(t, 1, nil)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%q", rr.Code, rr.Body.String())
		}
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("mutable failure did not rollback rule and earlier occurrence")
		}
	})

	t.Run("during_balance", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
		var balanceBefore int64
		_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceBefore)
		execTestSQL(t, f.db, `CREATE TRIGGER fail_all_b2_balance BEFORE UPDATE OF current_balance ON accounts WHEN OLD.id='checking-test' BEGIN SELECT RAISE(ABORT, 'simulated B2 balance failure'); END`)
		rr := f.request(t, 1, map[string]string{"status_pagamento": "paid"})
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%q", rr.Code, rr.Body.String())
		}
		if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
			t.Fatal("balance failure did not rollback series")
		}
		var balanceAfter int64
		_ = f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test'`).Scan(&balanceAfter)
		if balanceAfter != balanceBefore {
			t.Fatalf("balance failure changed balance: before=%d after=%d", balanceBefore, balanceAfter)
		}
	})
}

func TestRecurringAll4BB2OccurrenceConcurrencyGuardRollsBack(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
	// Simula uma atualização concorrente que acontece depois do snapshot global e
	// antes das guardas de cada ocorrência.
	execTestSQL(t, f.db, `CREATE TRIGGER race_all_b2_item AFTER UPDATE OF description ON recurring_rules WHEN NEW.id='rule-all-b2' BEGIN UPDATE transactions SET updated_at=updated_at+1 WHERE id='tx-all-b2-2'; END`)
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
	if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
		t.Fatal("lost occurrence guard did not rollback the complete operation")
	}
}

func TestRecurringAll4BB2ActiveRuleRowsAffectedGuardRollsBack(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	before := recurringAll4BB2Snapshot(t, f.db, f.seriesID)
	execTestSQL(t, f.db, `
		CREATE TRIGGER lose_all_b2_active_rule_guard
		BEFORE UPDATE OF description ON recurring_rules
		WHEN OLD.id='rule-all-b2'
		BEGIN
			SELECT RAISE(IGNORE);
		END
	`)
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
	if recurringAll4BB2Snapshot(t, f.db, f.seriesID) != before {
		t.Fatal("lost active-rule RowsAffected guard did not rollback the complete operation")
	}
}

func recurringAll4BB2ExerciseConcurrentRuleGuard(t *testing.T, field string) {
	t.Helper()
	t.Run("second_connection", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()
		uploadRoot := t.TempDir()
		t.Setenv("UPLOADS_DIR", uploadRoot)
		beforeFiles := recurringAll4BB2DirectorySnapshot(t, uploadRoot)

		ready := make(chan struct{}, 1)
		release := make(chan struct{})
		handler := f.handler
		handler.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
			if scope != "all" {
				return
			}
			ready <- struct{}{}
			<-release
		}}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			result <- f.requestWithHandler(t, &handler, 1, nil)
		}()
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("handler did not establish recurring-all snapshot")
		}
		var err error
		switch field {
		case "updated_at":
			_, err = f.db.Exec(`UPDATE recurring_rules SET updated_at=updated_at+100 WHERE id=?`, f.ruleID)
		case "active":
			_, err = f.db.Exec(`UPDATE recurring_rules SET active=0 WHERE id=?`, f.ruleID)
		default:
			t.Fatalf("unsupported guard field %q", field)
		}
		if err != nil {
			close(release)
			t.Fatalf("second connection update %s: %v", field, err)
		}
		expected := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		close(release)
		var rr *httptest.ResponseRecorder
		select {
		case rr = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("guarded handler did not finish")
		}
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != expected {
			t.Fatalf("handler added partial writes after concurrent %s change", field)
		}
		if afterFiles := recurringAll4BB2DirectorySnapshot(t, uploadRoot); afterFiles != beforeFiles {
			t.Fatalf("concurrent %s conflict changed filesystem", field)
		}
	})

	t.Run("predicate_rows_affected_zero", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()
		uploadRoot := t.TempDir()
		t.Setenv("UPLOADS_DIR", uploadRoot)
		before := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		beforeFiles := recurringAll4BB2DirectorySnapshot(t, uploadRoot)
		var hookRows int64
		handler := f.handler
		handler.testHooks = &recurringEditTestHooks{beforeAllRuleWrite: func(tx *sql.Tx) error {
			var result sql.Result
			var err error
			switch field {
			case "updated_at":
				result, err = tx.Exec(`UPDATE recurring_rules SET updated_at=updated_at+100 WHERE id=?`, f.ruleID)
			case "active":
				result, err = tx.Exec(`UPDATE recurring_rules SET active=0 WHERE id=?`, f.ruleID)
			}
			if err != nil {
				return err
			}
			hookRows, err = result.RowsAffected()
			return err
		}}
		rr := f.requestWithHandler(t, &handler, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if hookRows != 1 {
			t.Fatalf("test mutation rows=%d want=1", hookRows)
		}
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != before {
			t.Fatalf("lost %s guard did not rollback complete transaction", field)
		}
		if afterFiles := recurringAll4BB2DirectorySnapshot(t, uploadRoot); afterFiles != beforeFiles {
			t.Fatalf("lost %s guard changed filesystem", field)
		}
	})
}

func TestRecurringAll4BB2ConcurrentUpdatedAtGuard(t *testing.T) {
	recurringAll4BB2ExerciseConcurrentRuleGuard(t, "updated_at")
}

func TestRecurringAll4BB2ConcurrentActiveGuard(t *testing.T) {
	recurringAll4BB2ExerciseConcurrentRuleGuard(t, "active")
}

type recurringAll4BB2HTTPResult struct {
	scope string
	rr    *httptest.ResponseRecorder
}

func recurringAll4BB2RunConcurrentHandlers(t *testing.T, f *recurringAll4BB2Fixture, secondScope string) map[string]*httptest.ResponseRecorder {
	t.Helper()
	readyA := make(chan string, 1)
	readyB := make(chan string, 1)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	handlerA := f.handler
	handlerB := f.handler
	handlerA.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
		readyA <- scope
		<-releaseA
	}}
	handlerB.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
		readyB <- scope
		<-releaseB
	}}
	resultA := make(chan recurringAll4BB2HTTPResult, 1)
	resultB := make(chan recurringAll4BB2HTTPResult, 1)
	start := make(chan struct{})
	go func() {
		<-start
		rr := f.requestWithHandler(t, &handlerA, 1, map[string]string{"descricao": "Concurrent All A", "valor": "210,00", "escopo": "all"})
		resultA <- recurringAll4BB2HTTPResult{scope: "all-a", rr: rr}
	}()
	go func() {
		<-start
		selected := 1
		date := "2026-01-10"
		description := "Concurrent All B"
		amount := "220,00"
		if secondScope == "future" {
			selected = 2
			date = "2026-02-10"
			description = "Concurrent Future B"
		}
		rr := f.requestWithHandler(t, &handlerB, selected, map[string]string{
			"descricao": description, "valor": amount, "data": date, "escopo": secondScope,
		})
		label := secondScope
		if secondScope == "all" {
			label = "all-b"
		}
		resultB <- recurringAll4BB2HTTPResult{scope: label, rr: rr}
	}()
	close(start)
	seen := make([]string, 0, 2)
	for _, ready := range []<-chan string{readyA, readyB} {
		select {
		case scope := <-ready:
			seen = append(seen, scope)
		case <-time.After(3 * time.Second):
			close(releaseA)
			close(releaseB)
			t.Fatalf("handlers did not both cross preflight: seen=%v stats=%+v", seen, f.db.Stats())
		}
	}
	stats := f.db.Stats()
	if stats.MaxOpenConnections != 2 || stats.OpenConnections != 2 || stats.InUse != 2 {
		close(releaseA)
		close(releaseB)
		t.Fatalf("two preflight snapshots did not hold two real connections: %+v", stats)
	}
	out := make(map[string]*httptest.ResponseRecorder, 2)
	winnerRelease, winnerResult := releaseA, resultA
	loserRelease, loserResult := releaseB, resultB
	if secondScope == "future" {
		winnerRelease, winnerResult = releaseB, resultB
		loserRelease, loserResult = releaseA, resultA
	}
	close(winnerRelease)
	select {
	case result := <-winnerResult:
		out[result.scope] = result.rr
		if result.rr.Code != http.StatusOK {
			close(loserRelease)
			t.Fatalf("planned concurrent winner scope=%s status=%d body=%q", result.scope, result.rr.Code, result.rr.Body.String())
		}
	case <-time.After(5 * time.Second):
		close(loserRelease)
		t.Fatal("planned concurrent winner did not finish")
	}
	close(loserRelease)
	select {
	case result := <-loserResult:
		out[result.scope] = result.rr
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent loser did not finish")
	}
	return out
}

func recurringAll4BB2AssertOneHTTPWinner(t *testing.T, results map[string]*httptest.ResponseRecorder) string {
	t.Helper()
	winner := ""
	for scope, rr := range results {
		switch rr.Code {
		case http.StatusOK:
			if winner != "" {
				t.Fatalf("two concurrent winners: %s and %s", winner, scope)
			}
			winner = scope
		case http.StatusConflict:
			if !strings.Contains(rr.Body.String(), "não é possível alterar") {
				t.Fatalf("concurrent conflict body=%q", rr.Body.String())
			}
		default:
			t.Fatalf("generic concurrent error scope=%s status=%d body=%q", scope, rr.Code, rr.Body.String())
		}
	}
	if winner == "" {
		t.Fatal("no concurrent request succeeded")
	}
	return winner
}

func TestRecurringAll4BB2ConcurrentHandlersRemainCoherent(t *testing.T) {
	t.Run("all_all", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()
		financialBefore := recurringAll4BB2FinancialSnapshot(t, f.db)
		results := recurringAll4BB2RunConcurrentHandlers(t, f, "all")
		winner := recurringAll4BB2AssertOneHTTPWinner(t, results)
		wantDescription, wantAmount := "Concurrent All A", int64(21000)
		if winner == "all-b" {
			wantDescription, wantAmount = "Concurrent All B", 22000
		}
		for index, id := range f.ids {
			recurringAll4BB2AssertOccurrence(t, f.db, id, wantDescription, wantAmount)
			assertRecurringIdentity(t, f.db, id, f.ruleID, f.seriesID, int64(index+1))
		}
		recurringAll4BB2AssertActiveLeaf(t, f.db, f.seriesID)
		assertNoDuplicateSeriesSequence(t, f.db, f.seriesID)
		if after := recurringAll4BB2FinancialSnapshot(t, f.db); after != financialBefore {
			t.Fatal("pending all/all changed financial state")
		}
	})

	t.Run("all_future", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()
		financialBefore := recurringAll4BB2FinancialSnapshot(t, f.db)
		results := recurringAll4BB2RunConcurrentHandlers(t, f, "future")
		winner := recurringAll4BB2AssertOneHTTPWinner(t, results)
		activeID := recurringAll4BB2AssertActiveLeaf(t, f.db, f.seriesID)
		if winner == "all-a" {
			if activeID != f.ruleID {
				t.Fatalf("all winner active rule=%q want=%q", activeID, f.ruleID)
			}
			for index, id := range f.ids {
				recurringAll4BB2AssertOccurrence(t, f.db, id, "Concurrent All A", 21000)
				assertRecurringIdentity(t, f.db, id, f.ruleID, f.seriesID, int64(index+1))
			}
		} else {
			if activeID == f.ruleID {
				t.Fatal("future winner did not create a fresh active child")
			}
			recurringAll4BB2AssertOccurrence(t, f.db, f.ids[0], "Base B2", 10000)
			assertRecurringIdentity(t, f.db, f.ids[0], f.ruleID, f.seriesID, 1)
			for index, id := range f.ids[1:] {
				recurringAll4BB2AssertOccurrence(t, f.db, id, "Concurrent Future B", 22000)
				assertRecurringIdentity(t, f.db, id, activeID, f.seriesID, int64(index+2))
			}
		}
		assertNoDuplicateSeriesSequence(t, f.db, f.seriesID)
		if after := recurringAll4BB2FinancialSnapshot(t, f.db); after != financialBefore {
			t.Fatal("pending all/future changed financial state")
		}
	})
}

func recurringAll4BB2ParamsForTest(f *recurringAll4BB2Fixture, description string, amount int64) recurringAll4BB2Params {
	return recurringAll4BB2Params{
		CurrentID:       f.ids[0],
		Type:            "EXPENSE",
		Amount:          amount,
		Date:            MustCivilWriteDate("2026-01-10"),
		Description:     description,
		AccountID:       "checking-test",
		CategoryID:      "cat-expense-rec",
		Status:          "pending",
		Frequency:       "MONTHLY",
		IsBusiness:      false,
		Now:             time.Now().Unix(),
		SelectedNotes:   "nota-base",
		NotesProvided:   false,
		ContactProvided: false,
	}
}

func recurringAll4BB2AssertActiveLeaf(t *testing.T, db *sql.DB, seriesID string) string {
	t.Helper()
	var activeID string
	var activeCount int
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(MAX(id), '') FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? AND active=1`, seriesID).Scan(&activeCount, &activeID); err != nil {
		t.Fatal(err)
	}
	var children int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, activeID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || activeID == "" || children != 0 {
		t.Fatalf("active leaf invariant active_count=%d active_id=%q children=%d", activeCount, activeID, children)
	}
	return activeID
}

func recurringAll4BB2FinancialSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return strings.Join([]string{
		recurringAll4BB2RowsSnapshot(t, db, "accounts", `SELECT * FROM accounts WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "invoices", `SELECT i.* FROM invoices i JOIN accounts a ON a.id=i.account_id WHERE a.workspace_id='ws-test' ORDER BY i.id`),
		recurringAll4BB2RowsSnapshot(t, db, "payments", `SELECT * FROM invoice_payments WHERE workspace_id='ws-test' ORDER BY id`),
		recurringAll4BB2RowsSnapshot(t, db, "ledger", `SELECT l.* FROM box_virtual_ledger l JOIN boxes b ON b.id=l.box_id WHERE b.workspace_id='ws-test' ORDER BY l.id`),
	}, "\n")
}

func recurringAll4BB2RequestFields(description, amount, date, scope string) map[string]string {
	return map[string]string{
		"valor":            amount,
		"descricao":        description,
		"data":             date,
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"recorrencia":      "mensal",
		"escopo":           scope,
	}
}

func TestRecurringAll4BB2MaterializationAfterMetadataUsesActiveRule(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	rr := f.request(t, 2, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	execTestSQL(t, f.db, `UPDATE recurring_series SET total_occurrences=4 WHERE id=?`, f.seriesID)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET total_occurrences=4 WHERE id=?`, f.ruleID)
	if err := EnsureRecurringMaterialized(f.db, "ws-test", MustCivilWriteDate("2026-04-30")); err != nil {
		t.Fatalf("materialize after metadata: %v", err)
	}
	var description string
	var amount, sequence int64
	var ruleID, seriesID string
	if err := f.db.QueryRow(`SELECT description, amount, recurrence_sequence, recurring_rule_id, series_id FROM transactions WHERE series_id=? AND recurrence_sequence=4`, f.seriesID).Scan(&description, &amount, &sequence, &ruleID, &seriesID); err != nil {
		t.Fatalf("load materialized metadata occurrence: %v", err)
	}
	if description != "Atualizada B2" || amount != 20000 || sequence != 4 || ruleID != f.ruleID || seriesID != f.seriesID {
		t.Fatalf("materialized occurrence = (%q,%d,seq=%d,rule=%q,series=%q)", description, amount, sequence, ruleID, seriesID)
	}
}

func TestRecurringAll4BB2PaidInChainPreservesExistingDue(t *testing.T) {
	t.Run("already_without_due_allowed", func(t *testing.T) {
		f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
		defer f.close()
		recurringAll4BB2MakeChain(t, f)
		rr := f.request(t, 2, map[string]string{"status_pagamento": "paid"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	})
	t.Run("existing_due_preserved", func(t *testing.T) {
		offset := int64(10)
		f := newRecurringAll4BB2Fixture(t, true, "2026-01-10", &offset)
		defer f.close()
		recurringAll4BB2MakeChain(t, f)
		execTestSQL(t, f.db, `UPDATE recurring_rules SET due_date_offset_days=10, contact_id='contact-b2' WHERE id='rule-all-b2-active'`)
		rr := f.request(t, 2, map[string]string{"status_pagamento": "paid"})
		recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
		var selectedStatus string
		var selectedDue sql.NullInt64
		if err := f.db.QueryRow(`SELECT status, due_date FROM transactions WHERE id=?`, f.ids[1]).Scan(&selectedStatus, &selectedDue); err != nil {
			t.Fatal(err)
		}
		if selectedStatus != "paid" || !selectedDue.Valid {
			t.Fatalf("selected status=%q due=%v, want paid with preserved due", selectedStatus, selectedDue)
		}
	})
}

// ---------------------------------------------------------------------------
// 4B-B2: active-leaf invariant – schema trigger enforcement (fresh database)
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2InvariantInsertChildForActiveParentFails(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvariantWorkspaceAndAccounts(t, db)
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-inv-parent', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-parent', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Parent', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 1, 'series-inv-parent', 1, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)

	_, err := db.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Child', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-inv-child', 'rule-inv-parent', 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)
	if err == nil {
		t.Fatal("insert child for active parent should be rejected by trigger")
	}
	if !invariantTriggerFired(err) {
		t.Fatalf("expected invariant trigger error, got: %v", err)
	}
}

func TestRecurringAll4BB2InvariantUpdateParentRefToActiveFails(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvariantWorkspaceAndAccounts(t, db)
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-inv-a', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-active', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Active', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 1, 'series-inv-a', 1, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-orphan', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Orphan', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-inv-a', 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)

	_, err := db.Exec(`UPDATE recurring_rules SET parent_rule_id = 'rule-inv-active' WHERE id = 'rule-inv-orphan'`)
	if err == nil {
		t.Fatal("update parent_rule_id to active rule should be rejected by trigger")
	}
	if !invariantTriggerFired(err) {
		t.Fatalf("expected invariant trigger error, got: %v", err)
	}
}

func TestRecurringAll4BB2InvariantReactivateParentWithChildrenFails(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvariantWorkspaceAndAccounts(t, db)
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-inv-rp', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-rp-parent', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'ParentRP', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 0, 'series-inv-rp', 1, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-rp-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'ChildRP', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-inv-rp', 'rule-inv-rp-parent', 2, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)

	_, err := db.Exec(`UPDATE recurring_rules SET active = 1 WHERE id = 'rule-inv-rp-parent'`)
	if err == nil {
		t.Fatal("reactivate parent with children should be rejected by trigger")
	}
	if !invariantTriggerFired(err) {
		t.Fatalf("expected invariant trigger error, got: %v", err)
	}
}

func TestRecurringAll4BB2InvariantParentDeactivateThenInsertChildSucceeds(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvariantWorkspaceAndAccounts(t, db)
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-inv-legit', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-legit-parent', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'LegitParent', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 1, 'series-inv-legit', 1, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)

	execTestSQL(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = 'rule-inv-legit-parent'`)
	_, err := db.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-legit-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'LegitChild', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-inv-legit', 'rule-inv-legit-parent', 2, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)
	if err != nil {
		t.Fatalf("legit split flow (deactivate then insert child) failed: %v", err)
	}
}

func TestRecurringAll4BB2InvariantABCDChainPreservesActiveLeaf(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	zero := int64(0)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET active=0, effective_end_sequence=0 WHERE id=?`, f.ruleID)
	recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-B", f.ruleID, false, 1, &zero)
	recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-C", "rule-all-b2-B", false, 1, &zero)
	recurringAll4BB2InsertRuleVersion(t, f, "rule-all-b2-D", "rule-all-b2-C", true, 1, nil)
	execTestSQL(t, f.db, `UPDATE transactions SET recurring_rule_id='rule-all-b2-D' WHERE workspace_id='ws-test' AND series_id=?`, f.seriesID)

	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)

	var activeID string
	var children int
	if err := f.db.QueryRow(`SELECT COALESCE(MAX(CASE WHEN active=1 THEN id ELSE '' END),''), COUNT(1) FROM recurring_rules WHERE workspace_id='ws-test' AND series_id=? AND active=1`, f.seriesID).Scan(&activeID, &children); err != nil {
		t.Fatal(err)
	}
	if activeID == "" {
		t.Fatal("no active rule in chain after edit")
	}
	_ = f.db.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id=?`, activeID).Scan(&children)
	if children != 0 {
		t.Fatalf("active leaf %s has %d children", activeID, children)
	}
}

// ---------------------------------------------------------------------------
// 4B-B2: invariant cross-workspace child blocking
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2InvariantChildFromOtherWorkspaceBlocked(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	seedOtherWorkspaceForRecurringCreate(t, f.db)
	now := time.Now().Unix()

	execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-inv-xws', 'ws-other', 'user-other', 1, ?, ?)`, now, now)
	_, err := f.db.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-inv-xws-child', 'ws-other', 'user-other', 'checking-other', 'cat-expense-other', 'EXPENSE', 10000, 'XWS Child', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-inv-xws', ?, 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, now, now)
	if err == nil {
		t.Fatal("cross-workspace child for active parent should be rejected")
	}
	if !invariantTriggerFired(err) {
		t.Fatalf("expected invariant trigger error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 4B-B2: migration v53 – triggers on fresh database
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2InvariantTriggersExistOnFreshDatabase(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for _, name := range []string{
		"trg_recurring_rules_no_active_parent_insert",
		"trg_recurring_rules_no_active_parent_update",
		"trg_recurring_rules_no_reactivation_with_children",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil {
			t.Fatalf("check trigger %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("trigger %s not found in fresh database", name)
		}
	}
}

func TestRecurringAll4BB2InvariantParentRuleIDIndexExistsOnFresh(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_recurring_rules_parent_rule_id'`).Scan(&count); err != nil {
		t.Fatalf("check parent_rule_id index: %v", err)
	}
	if count != 1 {
		t.Fatal("idx_recurring_rules_parent_rule_id not found")
	}
}

func TestRecurringAll4BB2InvariantParentRuleIDIndexQueryPlan(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvariantWorkspaceAndAccounts(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-plan-idx', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-plan-a', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'A', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 0, 'series-plan-idx', 1, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-plan-b', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'B', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 0, 'series-plan-idx', 'rule-plan-a', 2, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-plan-c', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'C', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 1, 'series-plan-idx', 'rule-plan-b', 3, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)

	t.Run("direct_parent_query", func(t *testing.T) {
		row := db.QueryRow(`EXPLAIN QUERY PLAN SELECT id FROM recurring_rules WHERE parent_rule_id='rule-plan-a'`)
		var detail string
		if err := row.Scan(&detail, &detail, &detail, &detail); err != nil {
			row.Scan(&detail, &detail, &detail)
		}
		var plan string
		rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT id FROM recurring_rules WHERE parent_rule_id='rule-plan-a'`)
		if err != nil {
			t.Fatalf("explain query plan: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, parent, notused int
			if err := rows.Scan(&id, &parent, &notused, &plan); err != nil {
				rows.Scan(&id, &parent, &notused)
			}
		}
		if !strings.Contains(plan, "idx_recurring_rules_parent_rule_id") && !strings.Contains(plan, "COVERING INDEX") && !strings.Contains(plan, "SEARCH") {
			t.Fatalf("query plan %q does not reference expected index", plan)
		}
	})

	t.Run("exists_query", func(t *testing.T) {
		var planBuilder strings.Builder
		rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT EXISTS(SELECT 1 FROM recurring_rules WHERE parent_rule_id='rule-plan-b')`)
		if err != nil {
			t.Fatalf("explain exists query plan: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Scan(&id, &parent, &notused)
			}
			planBuilder.WriteString(detail)
		}
		plan := planBuilder.String()
		if !strings.Contains(plan, "idx_recurring_rules_parent_rule_id") && !strings.Contains(strings.ToUpper(plan), "SEARCH") && !strings.Contains(strings.ToUpper(plan), "INDEX") {
			t.Fatalf("exists query plan %q does not reference expected index usage", plan)
		}
	})

	t.Run("migration_validation_query", func(t *testing.T) {
		var planBuilder strings.Builder
		rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT p.id, p.workspace_id FROM recurring_rules p WHERE p.active=1 AND EXISTS (SELECT 1 FROM recurring_rules c WHERE c.parent_rule_id=p.id) ORDER BY p.id`)
		if err != nil {
			t.Fatalf("explain migration validation plan: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Scan(&id, &parent, &notused)
			}
			planBuilder.WriteString(" ")
			planBuilder.WriteString(detail)
		}
		plan := planBuilder.String()
		if !strings.Contains(plan, "idx_recurring_rules_parent_rule_id") && !strings.Contains(strings.ToUpper(plan), "SEARCH") && !strings.Contains(strings.ToUpper(plan), "INDEX") {
			t.Fatalf("migration validation query plan %q does not reference expected index usage", plan)
		}
	})
}

// ---------------------------------------------------------------------------
// 4B-B2: migration v53 – triggers on migrated database
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2InvariantMigratedDatabaseGainsTriggers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "invariant-migrated.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap migrated database: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	rawDB, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	rawDB.SetMaxOpenConns(1)
	defer rawDB.Close()

	var migrationCount int
	if err := rawDB.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=53`).Scan(&migrationCount); err != nil {
		t.Fatalf("check migration 53: %v", err)
	}
	if migrationCount != 1 {
		t.Fatal("migration 53 not applied to migrated database")
	}

	for _, name := range []string{
		"trg_recurring_rules_no_active_parent_insert",
		"trg_recurring_rules_no_active_parent_update",
		"trg_recurring_rules_no_reactivation_with_children",
	} {
		var count int
		if err := rawDB.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count); err != nil {
			t.Fatalf("check trigger %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("trigger %s not found in migrated database", name)
		}
	}

	seedInvariantWorkspaceAndAccounts(t, rawDB)
	now := time.Now().Unix()
	execTestSQL(t, rawDB, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-mig-inv', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, rawDB, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig-active', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'MigActive', ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', 1, 'series-mig-inv', 1, ?, ?)`, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), now, now)

	_, err = rawDB.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'MigChild', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-mig-child', 'rule-mig-active', 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)
	if err == nil {
		t.Fatal("insert child for active parent should be rejected in migrated database")
	}
}

// ---------------------------------------------------------------------------
// 4B-B2: TOCTOU concurrency tests (real WAL, 2 connections, channel barriers)
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2TOCTOUScenarioAHandlerFirstInsertRejected(t *testing.T) {
	f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
	defer f.close()

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	insertResult := make(chan error, 1)

	handler := f.handler
	handler.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
		if scope != "all" {
			return
		}
		ready <- struct{}{}
		<-release
	}}

	handlerResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		handlerResult <- f.requestWithHandler(t, &handler, 1, nil)
	}()

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("handler did not reach preflight")
	}

	go func() {
		_, err := f.db.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-tctou-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'TOCTOU Child', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-child', ?, 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, time.Now().Unix(), time.Now().Unix())
		insertResult <- err
	}()

	close(release)

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-handlerResult:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)

	select {
	case err := <-insertResult:
		if err == nil {
			t.Fatal("concurrent insert for active parent should be rejected by invariant trigger")
		}
		if !invariantTriggerFired(err) {
			t.Fatalf("expected invariant trigger error from concurrent insert, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent insert did not complete")
	}

	var activeID, childID string
	_ = f.db.QueryRow(`SELECT id FROM recurring_rules WHERE id=? AND active=1`, f.ruleID).Scan(&activeID)
	_ = f.db.QueryRow(`SELECT id FROM recurring_rules WHERE id='rule-tctou-child'`).Scan(&childID)
	if activeID != f.ruleID {
		t.Fatal("unexpected winner: handler should have succeeded")
	}
	if childID != "" {
		t.Fatal("child should not exist after rejected concurrent insert")
	}
}

func TestRecurringAll4BB2TOCTOUScenarioBChildFirstHandlerConflict(t *testing.T) {
	f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
	defer f.close()

	now := time.Now().Unix()
	_, err := f.db.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-tctou-child-b', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'TOCTOU Child B', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-child-b', ?, 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, now, now)
	if err == nil {
		t.Fatal("insert child for active parent should be rejected by invariant trigger - scenario B broken from start")
	}
	if !invariantTriggerFired(err) {
		t.Fatalf("expected invariant trigger error in scenario B setup, got: %v", err)
	}
}

func TestRecurringAll4BB2TOCTOUConcurrentChildFirstHandlerAllDetectsConflict(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "recurring-all-concurrent-child.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap concurrent child db: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	dbA, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open dbA: %v", err)
	}
	defer dbA.Close()
	dbA.SetMaxOpenConns(1)

	dbB, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open dbB: %v", err)
	}
	defer dbB.Close()
	dbB.SetMaxOpenConns(1)

	f := newRecurringAll4BB2FixtureWithDB(t, dbA, false, "2026-01-10", nil)
	uploadRoot := dir + "/uploads"
	t.Setenv("UPLOADS_DIR", uploadRoot)
	if err := os.MkdirAll(uploadRoot+"/ws-test", 0700); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	childDone := make(chan error, 1)

	handler := f.handler
	handler.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
		if scope != "all" {
			return
		}
		ready <- struct{}{}
		<-release
	}}

	handlerResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		handlerResult <- f.requestWithHandler(t, &handler, 1, nil)
	}()

	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("handler did not reach preflight")
	}

	go func() {
		tx, terr := dbB.Begin()
		if terr != nil {
			childDone <- terr
			return
		}
		if _, terr := tx.Exec(`UPDATE recurring_rules SET active=0 WHERE id=? AND workspace_id='ws-test'`, f.ruleID); terr != nil {
			_ = tx.Rollback()
			childDone <- terr
			return
		}
		now := time.Now().Unix()
		if _, terr := tx.Exec(`
			INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at)
			VALUES ('rule-all-child-concurrent', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Concurrent Child', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, ?, ?, 2, ?, ?)
		`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.seriesID, f.ruleID, now, now); terr != nil {
			_ = tx.Rollback()
			childDone <- terr
			return
		}
		childDone <- tx.Commit()
	}()

	select {
	case err := <-childDone:
		if err != nil {
			close(release)
			t.Fatalf("legitimate child creation failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("child creation did not complete")
	}

	snapshotAfterChild := recurringAll4BB2GlobalFunctionalSnapshot(t, dbB)

	close(release)

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-handlerResult:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
	if afterHandler := recurringAll4BB2GlobalFunctionalSnapshot(t, dbB); afterHandler != snapshotAfterChild {
		t.Fatal("handler performed partial writes after concurrent child creation")
	}

	body := rr.Body.String()
	t.Logf("child-first concurrent handler body = %q status=%d", body, rr.Code)
	if rr.Code == http.StatusConflict {
		if body != "não é possível alterar toda a recorrência com os dados atuais\n" && !strings.Contains(body, "não") {
			t.Fatalf("conflict body unexpected: %q", body)
		}
	}

	activeID := recurringAll4BB2AssertActiveLeaf(t, dbB, f.seriesID)
	if activeID == f.ruleID {
		t.Fatal("active rule should be the concurrent child, not the original root")
	}
	if activeID != "rule-all-child-concurrent" {
		t.Fatalf("active rule id=%q, want rule-all-child-concurrent", activeID)
	}
}

func TestRecurringAll4BB2TOCTOUScenarioBChildFirstLegitimate(t *testing.T) {
	f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
	defer f.close()

	now := time.Now().Unix()
	execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-child-legit', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET active=0 WHERE id=?`, f.ruleID)
	_, err := f.db.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-tctou-child-legit', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'TOCTOU Legit Child', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-child-legit', ?, 2, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, now, now)
	if err != nil {
		t.Fatalf("insert child for inactive parent should succeed: %v", err)
	}

	before := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
	if recurringAll4BB2GlobalFunctionalSnapshot(t, f.db) != before {
		t.Fatal("handler modified state despite legitimate descendant existence")
	}
}

func TestRecurringAll4BB2TOCTOUUpdateParentRefToActiveFails(t *testing.T) {
	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
	defer f.close()
	now := time.Now().Unix()

	execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-orphan-ct', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, f.db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-tctou-orphan', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Orphan', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-orphan-ct', 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), now, now)

	handler := f.handler
	handler.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
		if scope != "all" {
			return
		}
		ready <- struct{}{}
		<-release
	}}

	updateResult := make(chan error, 1)
	go func() {
		_, err := f.db.Exec(`UPDATE recurring_rules SET parent_rule_id = ? WHERE id = 'rule-tctou-orphan'`, f.ruleID)
		updateResult <- err
	}()
	select {
	case err := <-updateResult:
		close(ready)
		if err == nil {
			t.Fatal("parent_rule_id update to active rule should be rejected by trigger")
		}
		if !invariantTriggerFired(err) {
			t.Fatalf("expected invariant trigger error from parent_rule_id update, got: %v", err)
		}
		close(release)
	case <-time.After(3 * time.Second):
		close(ready)
		close(release)
		t.Fatal("parent_rule_id update did not complete")
	}
}

func TestRecurringAll4BB2TOCTOUReactivateFails(t *testing.T) {
	f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
	defer f.close()
	now := time.Now().Unix()

	execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-rtv-child', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
	execTestSQL(t, f.db, `UPDATE recurring_rules SET active=0 WHERE id=?`, f.ruleID)
	execTestSQL(t, f.db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-rtv-child', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'RTV Child', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-rtv-child', ?, 2, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, now, now)

	_, err := f.db.Exec(`UPDATE recurring_rules SET active=1 WHERE id=?`, f.ruleID)
	if err == nil {
		t.Fatal("reactivate parent with children should be rejected by trigger")
	}
	if !invariantTriggerFired(err) {
		t.Fatalf("expected invariant trigger error from reactivation, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 4B-B2: workspace snapshot divergence
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2WorkspaceSnapshotDivergesOnForeignChange(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()
	seedOtherWorkspaceForRecurringCreate(t, f.db)

	before := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
	execTestSQL(t, f.db, `UPDATE workspaces SET name='Nome Alterado' WHERE id='ws-other'`)
	after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
	if after == before {
		t.Fatal("workspace snapshot did not diverge after foreign workspace name change")
	}
}

// ---------------------------------------------------------------------------
// 4B-B2: generic public response protection (no internal leak)
// ---------------------------------------------------------------------------

func TestRecurringAll4BB2ConflictResponseDoesNotLeakInternals(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	defer f.close()

	recurringAll4BB2SeedExternalChild(t, f, true, false, false)

	rr := f.request(t, 1, nil)
	recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)

	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}

	const wantBody = "não é possível alterar toda a recorrência com os dados atuais\n"
	if rr.Body.String() != wantBody {
		t.Fatalf("body = %q, want exact %q", rr.Body.String(), wantBody)
	}

	body := rr.Body.String()
	forbidden := []string{
		"rule-unrelated-orphan-b2",
		"ws-other",
		"Workspace Other",
		"series-external-leaf-b2",
		"rule-external-leaf-b2",
		"rule-inv",
		"errRecurringAllConflict.Error()",
		"SQLITE",
		"TRIGGER",
		"trigger",
		"ABORT",
		"active rule cannot have children",
	}
	for _, secret := range forbidden {
		if strings.Contains(strings.ToUpper(body), strings.ToUpper(secret)) {
			t.Fatalf("generic conflict response leaked internal detail %q in body: %q", secret, body)
		}
	}

	t.Run("body_must_differ_from_internal_message", func(t *testing.T) {
		if body == errRecurringAllConflict.Error() {
			t.Fatal("conflict response must not use the internal error message directly")
		}
	})
}

// helpers for invariant trigger tests

func seedInvariantWorkspaceAndAccounts(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type) VALUES ('ws-test', 'Workspace Test', 'personal')`)
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash) VALUES ('user-test', 'Test', 'test@test.test', 'hash')`)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-test', 'ws-test', 'Conta Teste', 'CHECKING', 100000, 100000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, type, created_at) VALUES ('cat-expense-rec', 'ws-test', 'Recorrencia', 'EXPENSE', ?)`, now)
}

func invariantTriggerFired(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "ACTIVE RULE CANNOT HAVE CHILDREN")
}

func recurringAll4BB2DropInvariantTriggers(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, name := range []string{
		"trg_recurring_rules_no_active_parent_insert",
		"trg_recurring_rules_no_active_parent_update",
		"trg_recurring_rules_no_reactivation_with_children",
	} {
		execTestSQL(t, db, `DROP TRIGGER IF EXISTS `+name)
	}
}

func recurringAll4BB2CreateInvariantTriggers(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TRIGGER IF NOT EXISTS trg_recurring_rules_no_active_parent_insert
BEFORE INSERT ON recurring_rules
WHEN NEW.parent_rule_id IS NOT NULL AND (
    SELECT active FROM recurring_rules WHERE id = NEW.parent_rule_id
) = 1
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END`,
		`CREATE TRIGGER IF NOT EXISTS trg_recurring_rules_no_active_parent_update
BEFORE UPDATE OF parent_rule_id ON recurring_rules
WHEN NEW.parent_rule_id IS NOT NULL
 AND NEW.parent_rule_id != COALESCE(OLD.parent_rule_id, '')
 AND (
    SELECT active FROM recurring_rules WHERE id = NEW.parent_rule_id
) = 1
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END`,
		`CREATE TRIGGER IF NOT EXISTS trg_recurring_rules_no_reactivation_with_children
BEFORE UPDATE OF active ON recurring_rules
WHEN NEW.active = 1 AND OLD.active = 0 AND (
    SELECT COUNT(1) FROM recurring_rules WHERE parent_rule_id = OLD.id
) > 0
BEGIN
    SELECT RAISE(ABORT, 'active rule cannot have children');
END`,
	}
	for _, stmt := range stmts {
		execTestSQL(t, db, stmt)
	}
}

func TestRecurringAll4BB2InvariantTriggerHelperRestorationOnSetupFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for _, name := range []string{
		"trg_recurring_rules_no_active_parent_insert",
		"trg_recurring_rules_no_active_parent_update",
		"trg_recurring_rules_no_reactivation_with_children",
	} {
		var before int
		db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&before)
		if before != 1 {
			t.Fatalf("trigger %s should exist on fresh database before helper test", name)
		}
	}

	func() {
		recurringAll4BB2DropInvariantTriggers(t, db)
		defer recurringAll4BB2CreateInvariantTriggers(t, db)

		for _, name := range []string{
			"trg_recurring_rules_no_active_parent_insert",
			"trg_recurring_rules_no_active_parent_update",
			"trg_recurring_rules_no_reactivation_with_children",
		} {
			var afterDrop int
			db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&afterDrop)
			if afterDrop != 0 {
				t.Errorf("trigger %s still exists after DropInvariantTriggers", name)
			}
		}
	}()

	for _, name := range []string{
		"trg_recurring_rules_no_active_parent_insert",
		"trg_recurring_rules_no_active_parent_update",
		"trg_recurring_rules_no_reactivation_with_children",
	} {
		var after int
		db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&after)
		if after != 1 {
			t.Fatalf("trigger %s not restored after deferred restoration", name)
		}
	}
}

func TestRecurringAll4BB2InvariantTriggerHelperNotInProductionPath(t *testing.T) {
	funcs := []string{
		"recurringAll4BB2DropInvariantTriggers",
		"recurringAll4BB2CreateInvariantTriggers",
	}
	for _, name := range funcs {
		if !strings.HasPrefix(name, "recurringAll4BB2") {
			t.Fatalf("helper %q prefix confirms test-only scope", name)
		}
	}

	for _, name := range funcs {
		matches, _ := filepath.Glob(filepath.Join("..", "..", "internal", "*.go"))
		for _, match := range matches {
			if strings.HasSuffix(match, "_test.go") {
				continue
			}
			data, err := os.ReadFile(match)
			if err != nil {
				continue
			}
			if strings.Contains(string(data), name) {
				t.Fatalf("helper %q found in non-test file %s", name, filepath.Base(match))
			}
		}
	}
}

func TestRecurringAll4BB2DeleteFutureStructuralValidation(t *testing.T) {
	type chainRule struct {
		id     string
		parent string
		active int
		start  int64
		end    *int64
	}

	makeChain := func(t *testing.T, db *sql.DB, seriesID string, rules []chainRule) {
		t.Helper()
		now := time.Now().Unix()
		for _, r := range rules {
			var parentVal interface{}
			if r.parent != "" {
				parentVal = r.parent
			}
			var endVal interface{}
			if r.end != nil {
				endVal = *r.end
			}
			execTestSQL(t, db, `
				INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, created_at, updated_at)
				VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, ?, ?, '2026-01-10', ?, '2026-01-10', 'MONTHLY', 'PENDING', ?, 1, ?, ?, ?, ?, ?, ?)
			`, r.id, r.id, testUnixDate("2026-01-10"), testUnixDate("2026-01-10"), r.active, seriesID, parentVal, r.start, endVal, now, now)
		}
	}

	t.Run("linear_delete_future_from_inactive_owner", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-str-B", parent: f.ruleID, active: 0, start: 3, end: int64Ptr4BB2(4)},
			{id: "rule-del-str-C", parent: "rule-del-str-B", active: 1, start: 5, end: nil},
		})

		execTestSQL(t, f.db, `UPDATE transactions SET recurring_rule_id = 'rule-del-str-B', recurrence_sequence = 3 WHERE id = ?`, f.ids[1])
		execTestSQL(t, f.db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-del-str-4', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, ?, '2026-04-10', 'Base B2', 'pending', 1, 1, 'rule-del-str-B', 4, ?, ?, ?)`, testUnixDate("2026-04-10"), f.seriesID, time.Now().Unix(), time.Now().Unix())
		execTestSQL(t, f.db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-del-str-5', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, ?, '2026-05-10', 'Base B2', 'pending', 1, 1, 'rule-del-str-C', 5, ?, ?, ?)`, testUnixDate("2026-05-10"), f.seriesID, time.Now().Unix(), time.Now().Unix())

		h := deleteHandler(t, f.db)
		req, rr := newDeleteRequest(t, f.ids[1], "future")
		h.HandleDeletarTransacao(rr, req, f.ids[1])
		if rr.Code != http.StatusOK {
			t.Fatalf("delete future from inactive B: status=%d body=%q", rr.Code, rr.Body.String())
		}
	})

	t.Run("delete_future_from_stale_ancestor_owner", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 1 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-stale-B", parent: f.ruleID, active: 0, start: 2, end: int64Ptr4BB2(3)},
			{id: "rule-del-stale-C", parent: "rule-del-stale-B", active: 1, start: 4, end: nil},
		})

		// Simula um vínculo legado/stale: a ocorrência está na série e na cadeia
		// correta, mas ainda aponta para o ancestral cuja faixa já terminou.

		h := deleteHandler(t, f.db)
		req, rr := newDeleteRequest(t, f.ids[1], "future")
		h.HandleDeletarTransacao(rr, req, f.ids[1])
		if rr.Code != http.StatusOK {
			t.Fatalf("delete future from stale ancestor: status=%d body=%q", rr.Code, rr.Body.String())
		}
	})

	t.Run("two_children_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-str-B1", parent: f.ruleID, active: 0, start: 3, end: int64Ptr4BB2(4)},
			{id: "rule-del-str-B2", parent: f.ruleID, active: 0, start: 3, end: int64Ptr4BB2(4)},
		})

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("branch rejected but performed writes")
		}
	})

	t.Run("two_active_leaves_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-str-B", parent: f.ruleID, active: 1, start: 3, end: nil},
			{id: "rule-del-str-C", parent: f.ruleID, active: 1, start: 3, end: nil},
		})

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("two active leaves rejected but performed writes")
		}
	})

	t.Run("no_active_leaf_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-str-B", parent: f.ruleID, active: 0, start: 3, end: int64Ptr4BB2(4)},
			{id: "rule-del-str-C", parent: "rule-del-str-B", active: 0, start: 5, end: nil},
		})

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("no active leaf rejected but performed writes")
		}
	})

	t.Run("cycle_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-str-cycle", parent: f.ruleID, active: 1, start: 3, end: nil},
		})
		recurringAll4BB2DropInvariantTriggers(t, f.db)
		defer recurringAll4BB2CreateInvariantTriggers(t, f.db)
		execTestSQL(t, f.db, `PRAGMA foreign_keys=OFF`)
		execTestSQL(t, f.db, `UPDATE recurring_rules SET parent_rule_id = 'rule-del-str-cycle' WHERE id = ?`, f.ruleID)
		execTestSQL(t, f.db, `PRAGMA foreign_keys=ON`)
		recurringAll4BB2CreateInvariantTriggers(t, f.db)

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("cycle rejected but performed writes")
		}
	})

	t.Run("active_leaf_not_descendant_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		makeChain(t, f.db, f.seriesID, []chainRule{
			{id: "rule-del-str-separate", parent: "", active: 1, start: 1, end: nil},
		})

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("disconnected active leaf rejected but performed writes")
		}
	})

	t.Run("cross_series_child_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		now := time.Now().Unix()
		execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-other-del-str', 'ws-test', 'user-test', 1, ?, ?)`, now, now)
		recurringAll4BB2DropInvariantTriggers(t, f.db)
		defer recurringAll4BB2CreateInvariantTriggers(t, f.db)
		execTestSQL(t, f.db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-del-str-cross-series', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'CrossSeries', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-other-del-str', ?, 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, now, now)
		recurringAll4BB2CreateInvariantTriggers(t, f.db)

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		body := rr.Body.String()
		for _, secret := range []string{"rule-del-str-cross-series", "series-other-del-str"} {
			if strings.Contains(body, secret) {
				t.Fatalf("cross-series conflict leaked %q in body: %q", secret, body)
			}
		}
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("cross-series child rejected but performed writes")
		}
	})

	t.Run("cross_workspace_child_rejected", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		seedOtherWorkspaceForRecurringCreate(t, f.db)
		now := time.Now().Unix()
		execTestSQL(t, f.db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-other-ws-del-str', 'ws-other', 'user-other', 1, ?, ?)`, now, now)
		recurringAll4BB2DropInvariantTriggers(t, f.db)
		defer recurringAll4BB2CreateInvariantTriggers(t, f.db)
		execTestSQL(t, f.db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-del-str-cross-ws', 'ws-other', 'user-other', 'checking-other', 'cat-expense-other', 'EXPENSE', 10000, 'CrossWS', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 'series-other-ws-del-str', ?, 1, ?, ?)`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.ruleID, now, now)
		recurringAll4BB2CreateInvariantTriggers(t, f.db)

		snapshotBefore := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db)
		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
		body := rr.Body.String()
		for _, secret := range []string{"rule-del-str-cross-ws", "ws-other", "Workspace Other"} {
			if strings.Contains(body, secret) {
				t.Fatalf("cross-workspace conflict leaked %q in body: %q", secret, body)
			}
		}
		if after := recurringAll4BB2GlobalFunctionalSnapshot(t, f.db); after != snapshotBefore {
			t.Fatal("cross-workspace child rejected but performed writes")
		}
	})

	t.Run("row_order_independent", func(t *testing.T) {
		f := newRecurringAll4BB2DiskFixture(t, false, "2026-01-10", nil)
		defer f.close()

		execTestSQL(t, f.db, `UPDATE recurring_rules SET active = 0, effective_end_sequence = 2 WHERE id = ?`, f.ruleID)
		execTestSQL(t, f.db, `
			INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at)
			VALUES ('rule-del-str-last', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'Last', ?, '2026-03-10', ?, '2026-03-10', 'MONTHLY', 'PENDING', 0, 1, ?, ?, 3, ?, ?)
		`, testUnixDate("2026-03-10"), testUnixDate("2026-03-10"), f.seriesID, f.ruleID, time.Now().Unix(), time.Now().Unix())
		execTestSQL(t, f.db, `
			INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at)
			VALUES ('rule-del-str-first', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, 'First', ?, '2026-02-10', ?, '2026-02-10', 'MONTHLY', 'PENDING', 1, 1, ?, ?, 4, ?, ?)
		`, testUnixDate("2026-02-10"), testUnixDate("2026-02-10"), f.seriesID, "rule-del-str-last", time.Now().Unix(), time.Now().Unix())

		rr := f.request(t, 1, nil)
		recurringAll4BB2AssertStatus(t, rr, http.StatusConflict)
	})
}
