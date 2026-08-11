package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// p1SeedBase creates a user, workspace, account (checking + card), and category with unique IDs.
func p1SeedBase(t *testing.T, db *sql.DB, suffix string) (wsID string) {
	t.Helper()
	wsID = "ws-p1-" + suffix
	userID := "u-p1-" + suffix
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?,?,?, 'hash', ?,?)`, userID, userID, userID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, description, created_at, updated_at) VALUES (?,?,'',?,?)`, wsID, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?,?,'ADMIN',?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-`+suffix+`', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-`+suffix+`', ?, 'Card', 'CREDIT_CARD', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-`+suffix+`', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)
	return
}

// p1SeedRule creates a recurring_series, recurring_rules, and a first transaction.
func p1SeedRule(t *testing.T, db *sql.DB, suffix, wsID, desc, startDate string) (ruleID, seriesID string) {
	t.Helper()
	ruleID = "rule-" + suffix
	seriesID = "series-" + suffix
	now := time.Now().Unix()
	execTestSQL(t, db, fmt.Sprintf(`INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at) VALUES ('%s','%s','u-p1-%s',1,NULL,%d,%d)`, seriesID, wsID, suffix, now, now))
	execTestSQL(t, db, fmt.Sprintf(`INSERT OR IGNORE INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at) VALUES ('%s','%s','u-p1-%s','acc-%s','cat-%s','EXPENSE',15000,'%s',%d,'%s',%d,'%s','MONTHLY','PENDING',1,NULL,'%s',1,%d,%d)`, ruleID, wsID, suffix, suffix, suffix, desc, testUnixDate(startDate), MustCivilWriteDate(startDate).Civil(), testUnixDate(startDate), MustCivilWriteDate(startDate).Civil(), seriesID, now, now))
	execTestSQL(t, db, fmt.Sprintf(`INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-%s-1','%s','u-p1-%s','acc-%s','cat-%s','EXPENSE',15000,%d,'%s','%s','pending',1,1,'%s',1,'%s',%d,%d)`, suffix, wsID, suffix, suffix, suffix, testUnixDate(startDate), MustCivilWriteDate(startDate).Civil(), desc, ruleID, seriesID, now, now))
	return
}

// p1SeedMatState creates a materialization state row.
func p1SeedMatState(t *testing.T, db *sql.DB, wsID string, horizon CivilWriteDate) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR REPLACE INTO recurring_materialization_state (workspace_id, materialized_until, materialized_until_civil, status, created_at, updated_at) VALUES (?,?,?,'idle',?,?)`, wsID, horizon.LegacyUnix(), horizon.Civil(), now, now)
}

// p1SnapRule captures rule-level markers before an operation that should fail.
func p1SnapRule(t *testing.T, db *sql.DB, wsID, ruleID string) (txCount int64, gen sql.NullInt64, genCiv sql.NullString) {
	t.Helper()
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = ?`, wsID).Scan(&txCount)
	db.QueryRow(`SELECT generated_until, generated_until_civil FROM recurring_rules WHERE id = ?`, ruleID).Scan(&gen, &genCiv)
	return
}

func p1AssertRuleNotAdvanced(t *testing.T, db *sql.DB, wsID, ruleID string, beforeTx int64, beforeGen sql.NullInt64, beforeGenCiv sql.NullString) {
	t.Helper()
	var afterTx int64
	var afterGen sql.NullInt64
	var afterGenCiv sql.NullString
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = ?`, wsID).Scan(&afterTx)
	db.QueryRow(`SELECT generated_until, generated_until_civil FROM recurring_rules WHERE id = ?`, ruleID).Scan(&afterGen, &afterGenCiv)
	if afterTx != beforeTx {
		t.Errorf("tx count changed: %d -> %d", beforeTx, afterTx)
	}
	if !nullInt64Eq(beforeGen, afterGen) || !nullStringEq(beforeGenCiv, afterGenCiv) {
		t.Errorf("generated_until changed: (%v,%v) -> (%v,%v)", beforeGen, beforeGenCiv, afterGen, afterGenCiv)
	}
}

func nullInt64Eq(a, b sql.NullInt64) bool {
	if a.Valid != b.Valid {
		return false
	}
	if !a.Valid {
		return true
	}
	return a.Int64 == b.Int64
}

func nullStringEq(a, b sql.NullString) bool {
	if a.Valid != b.Valid {
		return false
	}
	if !a.Valid {
		return true
	}
	return a.String == b.String
}

func formPostMultipart(t *testing.T, path string, values url.Values) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for key, vals := range values {
		for _, v := range vals {
			if err := w.WriteField(key, v); err != nil {
				t.Fatalf("multipart writeField %s: %v", key, err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// p1ToggleTemplates returns minimal templates for HandleTogglePagamento.
func p1ToggleTemplates() *template.Template {
	return template.Must(template.New("test").Parse(`
{{define "lancamento-row"}}<tr id="{{.ID}}"></tr>{{end}}
{{define "lancamentos-resumo-oob"}}{{end}}
`))
}

// ---------- P1: PERSONAL ----------

func TestCivil4BA_PersonalProducerDropsUnexpectedDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "personal")
	h := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: "u-p1-personal"}
	totalOcc := int64(3)
	_, err := h.insertTransaction("EXPENSE", 10000, "p1 Personal Drop", "", "", MustCivilWriteDate("2026-07-12"), "acc-personal", "", "cat-personal", 1, "pending", true, "MONTHLY", "", MustOptionalCivilWriteDate("2026-07-20"), &totalOcc, "", false)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	ruleID, _ := recurringRuleSeriesByDescription(t, db, wsID, "p1 Personal Drop")
	var offset sql.NullInt64
	db.QueryRow(`SELECT due_date_offset_days FROM recurring_rules WHERE id = ?`, ruleID).Scan(&offset)
	if offset.Valid {
		t.Fatalf("due_date_offset_days = %v, want NULL", offset)
	}
	var dueDate sql.NullInt64
	var dueDateCiv sql.NullString
	db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE description = 'p1 Personal Drop' AND recurrence_sequence = 1`).Scan(&dueDate, &dueDateCiv)
	if dueDate.Valid {
		t.Fatalf("first occurrence due_date = %d, want NULL", dueDate.Int64)
	}
	if dueDateCiv.Valid {
		t.Fatalf("first occurrence due_date_civil = %q, want NULL", dueDateCiv.String)
	}
	// Future occurrence (seq 2): must also have NULL due_date for Personal accounts
	db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE description = 'p1 Personal Drop' AND recurrence_sequence = 2`).Scan(&dueDate, &dueDateCiv)
	if dueDate.Valid {
		t.Fatalf("future occurrence seq=2 due_date = %d, want NULL", dueDate.Int64)
	}
	if dueDateCiv.Valid {
		t.Fatalf("future occurrence seq=2 due_date_civil = %q, want NULL", dueDateCiv.String)
	}

	// Run materializer: must preserve NULL due_date and not create divergence
	if err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05")); err != nil {
		t.Fatalf("materializer after personal insert: %v", err)
	}
	db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE description = 'p1 Personal Drop' AND recurrence_sequence = 2`).Scan(&dueDate, &dueDateCiv)
	if dueDate.Valid {
		t.Fatalf("seq=2 due_date after materialization = %d, want NULL", dueDate.Int64)
	}
	if dueDateCiv.Valid {
		t.Fatalf("seq=2 due_date_civil after materialization = %q, want NULL", dueDateCiv.String)
	}
}

// ---------- P1: TYPE MISMATCH ----------

func TestCivil4BA_CommonOccurrenceTypeMismatchReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "type")
	p1SeedRule(t, db, "type", wsID, "p1 Type", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-type-div',?,'u-p1-type','acc-type','cat-type','INCOME',15000,?,?,'p1 Type','pending',1,1,'rule-type',2,'series-type',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-type")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if !strings.Contains(err.Error(), "divergent type") {
		t.Fatalf("error should mention divergent type, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-type", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: PAYMENT_STATUS MUABLE (Contract B) ----------

func TestCivil4BA_CommonOccurrenceMutablePaymentStatusPreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "statusb")
	p1SeedRule(t, db, "statusb", wsID, "p1 StatusB", "2026-06-05")

	// Verify initial state
	var isExc sql.NullInt64
	db.QueryRow(`SELECT is_recurring_exception FROM transactions WHERE id = 'tx-statusb-1'`).Scan(&isExc)
	if isExc.Int64 != 0 {
		t.Fatalf("initial is_recurring_exception = %d, want 0", isExc.Int64)
	}

	// Toggle payment via real public flow
	toggleHandler := TransactionHandler{
		DB:          db,
		Templates:   p1ToggleTemplates(),
		WorkspaceID: wsID,
		UserID:      "u-p1-statusb",
	}
	req := formPost("/transacoes/tx-statusb-1/status-pagamento", url.Values{
		"payment_action": {"paid"},
	})
	rr := httptest.NewRecorder()
	toggleHandler.HandleTogglePagamento(rr, req, "tx-statusb-1")
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle to paid: code=%d body=%q", rr.Code, rr.Body.String())
	}

	// Verify status changed, exception NOT marked
	var status string
	db.QueryRow(`SELECT status, is_recurring_exception FROM transactions WHERE id = 'tx-statusb-1'`).Scan(&status, &isExc)
	if status != "paid" {
		t.Fatalf("status = %q, want paid", status)
	}
	if isExc.Int64 != 0 {
		t.Fatalf("is_recurring_exception = %d, want 0 after user toggle", isExc.Int64)
	}

	// Run materializer: must preserve user toggle and not overwrite
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err != nil {
		t.Fatalf("materializer should accept user-mutable payment_status: %v", err)
	}
	db.QueryRow(`SELECT status, is_recurring_exception FROM transactions WHERE id = 'tx-statusb-1'`).Scan(&status, &isExc)
	if status != "paid" {
		t.Fatalf("status after materialization = %q, want paid", status)
	}
	if isExc.Int64 != 0 {
		t.Fatalf("is_recurring_exception after materialization = %d, want 0", isExc.Int64)
	}

	// Next sequence must be created normally
	var dc2 string
	if err := db.QueryRow(`SELECT date_civil FROM transactions WHERE workspace_id = ? AND series_id = 'series-statusb' AND recurrence_sequence = 2`, wsID).Scan(&dc2); err != nil {
		t.Fatalf("seq 2 not created: %v", err)
	}
	if dc2 != "2026-07-05" {
		t.Fatalf("seq 2 date_civil = %q, want 2026-07-05", dc2)
	}
}

// ---------- P1: DUE_DATE ----------

func TestCivil4BA_DueDateZeroReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "due0")
	p1SeedRule(t, db, "due0", wsID, "p1 Due0", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-due0-div',?,'u-p1-due0','acc-due0','cat-due0','EXPENSE',15000,?,?,0,'p1 Due0','pending',1,1,'rule-due0',2,'series-due0',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-due0")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for due_date=0")
	}
	if !strings.Contains(err.Error(), "invalid due_date") {
		t.Fatalf("error should mention invalid due_date, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-due0", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_DueDateNegativeReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "duneg")
	p1SeedRule(t, db, "duneg", wsID, "p1 DueNeg", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-duneg-div',?,'u-p1-duneg','acc-duneg','cat-duneg','EXPENSE',15000,?,?,-1,'p1 DueNeg','pending',1,1,'rule-duneg',2,'series-duneg',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-duneg")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for due_date=-1")
	}
	if !strings.Contains(err.Error(), "invalid due_date") {
		t.Fatalf("error should mention invalid due_date, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-duneg", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_DueDatePairMismatchReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "dupair")
	p1SeedRule(t, db, "dupair", wsID, "p1 DuePair", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, due_date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-dupair-div',?,'u-p1-dupair','acc-dupair','cat-dupair','EXPENSE',15000,?,?,?,?,'p1 DuePair','pending',1,1,'rule-dupair',2,'series-dupair',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-20").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-dupair")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for due_date pair mismatch")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("error should mention mismatch, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-dupair", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_DueDateNullWithCivilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "dunull")
	p1SeedRule(t, db, "dunull", wsID, "p1 DueNull", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-dunull-div',?,'u-p1-dunull','acc-dunull','cat-dunull','EXPENSE',15000,?,?,?,'p1 DueNull','pending',1,1,'rule-dunull',2,'series-dunull',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), MustCivilWriteDate("2026-07-15").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-dunull")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for due_date NULL with due_date_civil SET")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-dunull", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: ENDDATECIVIL ----------

func TestCivil4BA_InvalidEndDateCivilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "endciv")
	p1SeedRule(t, db, "endciv", wsID, "p1 EndCiv", "2026-06-05")
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-endciv")
	execTestSQL(t, db, `UPDATE recurring_rules SET end_date_civil='2026-02-30' WHERE id='rule-endciv'`)
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2027-09-05"))
	if err == nil {
		t.Fatal("expected error for invalid end_date_civil")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-endciv", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: EFFECTIVE START ----------

func TestCivil4BA_ProjectionEffectiveStartSequenceFour(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "eff4")
	p1SeedRule(t, db, "eff4", wsID, "p1 Eff4", "2026-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET effective_start_sequence=4 WHERE id='rule-eff4'`)
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2027-09-05"))
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	var dc4 string
	if err := db.QueryRow(`SELECT date_civil FROM transactions WHERE workspace_id=? AND series_id='series-eff4' AND recurrence_sequence=4`, wsID).Scan(&dc4); err != nil {
		t.Fatalf("seq 4: %v", err)
	}
	if dc4 != "2026-06-05" {
		t.Fatalf("seq 4 date_civil = %q, want 2026-06-05", dc4)
	}
	var dc5 string
	db.QueryRow(`SELECT date_civil FROM transactions WHERE workspace_id=? AND series_id='series-eff4' AND recurrence_sequence=5`, wsID).Scan(&dc5)
	if dc5 != "2026-07-05" {
		t.Fatalf("seq 5 date_civil = %q, want 2026-07-05", dc5)
	}
	for s := int64(2); s <= 3; s++ {
		var n int
		db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id='series-eff4' AND recurrence_sequence=?`, wsID, s).Scan(&n)
		if n > 0 {
			t.Fatalf("seq %d should not exist", s)
		}
	}
}

// ---------- P1: EFFECTIVE END ----------

func TestCivil4BA_ProjectionEffectiveEndSequenceStopsVersion(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "effend")
	p1SeedRule(t, db, "effend", wsID, "p1 EffEnd", "2026-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET effective_start_sequence=2, effective_end_sequence=3 WHERE id='rule-effend'`)
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2027-09-05"))
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// seq 2 must exist with correct rule, anchor date, and coherent civil pair
	var dc2, rule2 string
	var date2 int64
	if err := db.QueryRow(`SELECT date_civil, recurring_rule_id, date FROM transactions WHERE workspace_id=? AND series_id='series-effend' AND recurrence_sequence=2`, wsID).Scan(&dc2, &rule2, &date2); err != nil {
		t.Fatalf("seq 2: %v", err)
	}
	if dc2 != "2026-06-05" {
		t.Fatalf("seq 2 date_civil = %q, want 2026-06-05", dc2)
	}
	if rule2 != "rule-effend" {
		t.Fatalf("seq 2 recurring_rule_id = %q, want rule-effend", rule2)
	}
	if LegacyUnixToCivilDateUTC(date2).String() != dc2 {
		t.Fatalf("seq 2 date/date_civil mismatch: date→%q, date_civil=%q", LegacyUnixToCivilDateUTC(date2).String(), dc2)
	}
	// seq 3 must exist (inclusive) with same rule and offset 1
	var dc3, rule3 string
	var date3 int64
	if err := db.QueryRow(`SELECT date_civil, recurring_rule_id, date FROM transactions WHERE workspace_id=? AND series_id='series-effend' AND recurrence_sequence=3`, wsID).Scan(&dc3, &rule3, &date3); err != nil {
		t.Fatalf("seq 3: %v", err)
	}
	if dc3 != "2026-07-05" {
		t.Fatalf("seq 3 date_civil = %q, want 2026-07-05", dc3)
	}
	if rule3 != "rule-effend" {
		t.Fatalf("seq 3 recurring_rule_id = %q, want rule-effend", rule3)
	}
	if LegacyUnixToCivilDateUTC(date3).String() != dc3 {
		t.Fatalf("seq 3 date/date_civil mismatch: date→%q, date_civil=%q", LegacyUnixToCivilDateUTC(date3).String(), dc3)
	}
	// seq 4 must NOT exist from this version (only seq 1 from seed)
	var n int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id='series-effend' AND recurrence_sequence=4`, wsID).Scan(&n)
	if n > 0 {
		t.Fatalf("seq 4 should not exist (beyond effective end 3)")
	}
}

// ---------- P1: ADDDAYS PROJECTION ----------

func TestCivil4BA_AddDaysProjectionRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "addproj")
	now := time.Now().Unix()
	// Seed a series and rule with huge due_date_offset_days to force AddDays overflow
	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-addproj', ?, 'u-p1-addproj', 1, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, due_date_offset_days, created_at, updated_at) VALUES ('rule-addproj', ?, 'u-p1-addproj', 'acc-addproj', 'cat-addproj', 'EXPENSE', 15000, 'p1 AddProj', ?, '2026-06-05', ?, '2026-06-05', 'MONTHLY', 'PENDING', 1, 'series-addproj', 1, 4000000, ?, ?)`, wsID, MustCivilWriteDate("2026-06-05").LegacyUnix(), MustCivilWriteDate("2026-06-05").LegacyUnix(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-addproj")
	h := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: "u-p1-addproj"}
	tx, _ := db.Begin()
	err := h.generateRecurrenceProjection(tx, recurrenceProjectionRule{
		ID:                   "rule-addproj",
		AccountID:            "acc-addproj",
		CategoryID:           "cat-addproj",
		Type:                 "EXPENSE",
		Amount:               15000,
		Description:          "p1 AddProj",
		StartDate:            MustCivilWriteDate("2026-06-05").LegacyUnix(),
		StartDateCivil:       "2026-06-05",
		Frequency:            "MONTHLY",
		DefaultPaymentStatus: "PENDING",
		HasDueDate:           true,
		DueDateOffsetDays:    sql.NullInt64{Int64: 4000000, Valid: true},
		DueDateUnix:          MustCivilWriteDate("2026-06-05").LegacyUnix(),
		DueDateCivil:         "2026-06-05",
	}, now)
	if err == nil {
		tx.Rollback()
		t.Fatal("expected AddDays overflow error in projection")
	}
	tx.Rollback()
	if !strings.Contains(err.Error(), "add civil days") {
		t.Fatalf("error should mention add civil days, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-addproj", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: ADDDAYS MATERIALIZER ----------

func TestCivil4BA_AddDaysMaterializerRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "addmat")
	// Make workspace business type so isBusiness=true in materializer
	execTestSQL(t, db, `UPDATE workspaces SET type='business' WHERE id = ?`, wsID)
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO contacts (id, name, workspace_id, type, created_at) VALUES ('ct-addmat', 'Contact', ?, 'vendor', ?)`, wsID, now)
	execTestSQL(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-addmat', ?, 'u-p1-addmat', 1, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, due_date_offset_days, contact_id, created_at, updated_at) VALUES ('rule-addmat', ?, 'u-p1-addmat', 'acc-addmat', 'cat-addmat', 'EXPENSE', 15000, 'p1 AddMat', ?, '2026-06-05', ?, '2026-06-05', 'MONTHLY', 'PENDING', 1, 'series-addmat', 1, 4000000, 'ct-addmat', ?, ?)`, wsID, MustCivilWriteDate("2026-06-05").LegacyUnix(), MustCivilWriteDate("2026-06-05").LegacyUnix(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-addmat")
	// Also snap materialized_until
	var beforeMat sql.NullInt64
	var beforeMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id = ?`, wsID).Scan(&beforeMat, &beforeMatCiv)

	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2027-09-05"))
	if err == nil {
		t.Fatal("expected AddDays overflow error in materializer")
	}
	if !strings.Contains(err.Error(), "add civil days") {
		t.Fatalf("error should mention add civil days, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-addmat", beforeTx, beforeGen, beforeGenCiv)
	var afterMat sql.NullInt64
	var afterMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id = ?`, wsID).Scan(&afterMat, &afterMatCiv)
	if !nullInt64Eq(beforeMat, afterMat) || !nullStringEq(beforeMatCiv, afterMatCiv) {
		t.Fatalf("materialized_until changed: (%v,%v) -> (%v,%v)", beforeMat, beforeMatCiv, afterMat, afterMatCiv)
	}
}

// ---------- P1: DUPLICITY & RULE ID ----------

func TestCivil4BA_CommonOccurrenceNullRuleIDReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "nulrule")
	p1SeedRule(t, db, "nulrule", wsID, "p1 NullR", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-nulrule-2',?,'u-p1-nulrule','acc-nulrule','cat-nulrule','EXPENSE',15000,?,?,'p1 NullR','pending',1,1,NULL,2,'series-nulrule',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-nulrule")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for null recurring_rule_id")
	}
	if !strings.Contains(err.Error(), "null recurring_rule_id") {
		t.Fatalf("error should mention null recurring_rule_id, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-nulrule", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_DuplicateSeriesSequenceReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "dups")
	p1SeedRule(t, db, "dups", wsID, "p1 DupS", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-dups-b',?,'u-p1-dups','acc-dups','cat-dups','EXPENSE',15000,?,?,'p1 DupS','pending',1,1,'rule-dups',2,'series-dups',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-dups-c',?,'u-p1-dups','acc-dups','cat-dups','EXPENSE',99999,?,?,'p1 DupS Alt','pending',1,1,NULL,2,'series-dups',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-dups")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected duplicate series sequence error")
	}
	if !strings.Contains(err.Error(), "duplicate series sequence") {
		t.Fatalf("error should mention duplicate series sequence, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-dups", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: GENERATED_UNTIL ----------

func TestCivil4BA_MissingOccurrenceBelowGeneratedUntilIsRecreated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "gaprec")
	p1SeedRule(t, db, "gaprec", wsID, "p1 GapRec", "2026-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET generated_until=?, generated_until_civil=? WHERE id='rule-gaprec'`, MustCivilWriteDate("2026-09-05").LegacyUnix(), MustCivilWriteDate("2026-09-05").Civil())
	execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id=? AND series_id='series-gaprec' AND recurrence_sequence=2`, wsID)
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err != nil {
		t.Fatalf("materialize gap: %v", err)
	}
	var dc string
	if err := db.QueryRow(`SELECT date_civil FROM transactions WHERE workspace_id=? AND series_id='series-gaprec' AND recurrence_sequence=2`, wsID).Scan(&dc); err != nil {
		t.Fatalf("seq 2 not recreated: %v", err)
	}
	if dc != "2026-07-05" {
		t.Fatalf("seq 2 date_civil = %q, want 2026-07-05", dc)
	}
}

func TestCivil4BA_DivergenceBelowGeneratedUntilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "gapdiv")
	p1SeedRule(t, db, "gapdiv", wsID, "p1 GapDiv", "2026-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET generated_until=?, generated_until_civil=? WHERE id='rule-gapdiv'`, MustCivilWriteDate("2027-09-05").LegacyUnix(), MustCivilWriteDate("2027-09-05").Civil())
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-gapdiv-2',?,'u-p1-gapdiv','acc-gapdiv','cat-gapdiv','EXPENSE',99999,?,?,'p1 GapDiv WRONG','pending',1,1,'rule-gapdiv',2,'series-gapdiv',?,?)`, wsID, MustCivilWriteDate("2026-08-05").LegacyUnix(), MustCivilWriteDate("2026-08-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-gapdiv")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2027-09-05"))
	if err == nil {
		t.Fatal("expected divergence error below generated_until")
	}
	if !strings.Contains(err.Error(), "divergent date") {
		t.Fatalf("error should mention divergent date, got: %v", err)
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-gapdiv", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: INVOICES ----------

func TestCivil4BA_InvoiceCrossWorkspaceReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "invxws")
	p1SeedRule(t, db, "invxws", wsID, "p1 InvXWS", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, created_at, updated_at) VALUES ('ws-other', 'Other', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-other', 'ws-other', 'OCard', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-other','card-other','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-invxws-2',?,'u-p1-invxws','card-invxws','inv-other','EXPENSE',15000,?,?,'p1 InvXWS','paid',1,1,'rule-invxws',2,'series-invxws',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-invxws")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for cross-workspace invoice")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-invxws", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvoiceDifferentAccountReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "invdiff")
	p1SeedRule(t, db, "invdiff", wsID, "p1 InvDiff", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card2-invdiff', ?, 'Card2', 'CREDIT_CARD', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-diff','card2-invdiff','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-invdiff-2',?,'u-p1-invdiff','card-invdiff','inv-diff','EXPENSE',15000,?,?,'p1 InvDiff','paid',1,1,'rule-invdiff',2,'series-invdiff',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-invdiff")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for invoice on different account")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-invdiff", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvoiceNonCardAccountReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "invnocard")
	p1SeedRule(t, db, "invnocard", wsID, "p1 InvNCard", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-nocard','acc-invnocard','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-invnocard-2',?,'u-p1-invnocard','acc-invnocard','inv-nocard','EXPENSE',15000,?,?,'p1 InvNCard','paid',1,1,'rule-invnocard',2,'series-invnocard',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-invnocard")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for invoice on non-credit-card account")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-invnocard", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvoiceNonExpenseReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "invnexp")
	p1SeedRule(t, db, "invnexp", wsID, "p1 InvNExp", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-nexp','card-invnexp','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-invnexp-2',?,'u-p1-invnexp','card-invnexp','inv-nexp','INCOME',15000,?,?,'p1 InvNExp','paid',1,1,'rule-invnexp',2,'series-invnexp',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-invnexp")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for invoice on non-expense")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-invnexp", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvoicePaymentStatusMismatchReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "invpstat")
	p1SeedRule(t, db, "invpstat", wsID, "p1 InvPStat", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-pstat','card-invpstat','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-invpstat-2',?,'u-p1-invpstat','card-invpstat','inv-pstat','EXPENSE',15000,?,?,'p1 InvPStat','pending',1,1,'rule-invpstat',2,'series-invpstat',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-invpstat")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for invoice-linked with non-paid status")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-invpstat", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: EXCEPTION WITH INVOICE ----------

func TestCivil4BA_ManualExceptionWithValidInvoicePreserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "excinv")
	p1SeedRule(t, db, "excinv", wsID, "p1 ExcInv", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-exc','card-excinv','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, is_recurring_exception, recurring_exception_reason, created_at, updated_at) VALUES ('tx-excinv-2',?,'u-p1-excinv','card-excinv','inv-exc','EXPENSE',99999,?,?,'p1 ExcInv Edit','paid',1,1,'rule-excinv',2,'series-excinv',1,'manual_edit',?,?)`, wsID, MustCivilWriteDate("2026-08-05").LegacyUnix(), MustCivilWriteDate("2026-08-05").Civil(), now, now)
	if err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05")); err != nil {
		t.Fatalf("should accept manual exception with valid invoice: %v", err)
	}
	var invID sql.NullString
	var isExc sql.NullInt64
	db.QueryRow(`SELECT invoice_id, is_recurring_exception FROM transactions WHERE id='tx-excinv-2'`).Scan(&invID, &isExc)
	if !invID.Valid || invID.String != "inv-exc" {
		t.Fatalf("invoice_id = %v", invID)
	}
	if !isExc.Valid || isExc.Int64 != 1 {
		t.Fatalf("is_recurring_exception = %v, want 1", isExc)
	}
}

func TestCivil4BA_ManualExceptionWithInvalidInvoiceReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "excinvb")
	p1SeedRule(t, db, "excinvb", wsID, "p1 ExcInvB", "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-excb','card-excinvb','2026-07',?,?,'OPEN',?)`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, is_recurring_exception, recurring_exception_reason, created_at, updated_at) VALUES ('tx-excinvb-2',?,'u-p1-excinvb','acc-excinvb','inv-excb','EXPENSE',99999,?,?,'p1 ExcInvB','paid',1,1,'rule-excinvb',2,'series-excinvb',1,'manual_edit',?,?)`, wsID, MustCivilWriteDate("2026-08-05").LegacyUnix(), MustCivilWriteDate("2026-08-05").Civil(), now, now)
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-excinvb")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for exception with invalid invoice (wrong account)")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-excinvb", beforeTx, beforeGen, beforeGenCiv)
}

// ---------- P1: CIVIS INVALID ----------

func TestCivil4BA_InvalidStartDateCivilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "startciv")
	p1SeedRule(t, db, "startciv", wsID, "p1 StartCiv", "2026-06-05")
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-startciv")
	execTestSQL(t, db, `UPDATE recurring_rules SET start_date_civil='2026-02-30' WHERE id='rule-startciv'`)
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for invalid start_date_civil")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-startciv", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvalidStartsAtCivilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "startsciv")
	p1SeedRule(t, db, "startsciv", wsID, "p1 StartsCiv", "2026-06-05")
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-startsciv")
	execTestSQL(t, db, `UPDATE recurring_rules SET starts_at_civil='2026-02-30' WHERE id='rule-startsciv'`)
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-09-05"))
	if err == nil {
		t.Fatal("expected error for invalid starts_at_civil")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-startsciv", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvalidGeneratedUntilCivilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "genciv")
	p1SeedRule(t, db, "genciv", wsID, "p1 GenCiv", "2026-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET generated_until=?, generated_until_civil='2026-02-30' WHERE id='rule-genciv'`, MustCivilWriteDate("2026-09-05").LegacyUnix())
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-genciv")
	err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2027-09-05"))
	if err == nil {
		t.Fatal("expected error for invalid generated_until_civil")
	}
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-genciv", beforeTx, beforeGen, beforeGenCiv)
}

func TestCivil4BA_InvalidMaterializedUntilCivilReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "matciv")
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO recurring_materialization_state (workspace_id, materialized_until, materialized_until_civil, status, created_at, updated_at) VALUES (?,?,'2026-02-30','idle',?,?)`, wsID, MustCivilWriteDate("2027-09-05").LegacyUnix(), now, now)
	err := RunRecurringMaterializationForWorkspace(context.Background(), db, wsID, now)
	if err == nil {
		t.Fatal("expected error for invalid materialized_until_civil")
	}
}

// ---------- P1: LOCK LOSS ----------

func TestCivil4BA_MaterializationLockLossRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "lockreal")
	p1SeedRule(t, db, "lockreal", wsID, "p1 LockReal", "2026-06-05")
	horizon := MustCivilWriteDate("2027-09-05")
	p1SeedMatState(t, db, wsID, horizon)
	now := time.Now().Unix()
	ownerA := fmt.Sprintf("%s-%d", wsID, now)

	// Capture state before
	beforeTx, beforeGen, beforeGenCiv := p1SnapRule(t, db, wsID, "rule-lockreal")
	var beforeMat sql.NullInt64
	var beforeMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&beforeMat, &beforeMatCiv)

	// Acquire lock as owner A
	txAcquire, _ := db.Begin()
	acquired, err := acquireRecurringMaterializationLockTx(txAcquire, wsID, ownerA, now, now+600)
	if err != nil {
		txAcquire.Rollback()
		t.Fatalf("acquire: %v", err)
	}
	txAcquire.Commit()
	if !acquired {
		t.Fatal("failed to acquire lock")
	}

	// Simulate owner B stealing the lock (separate tx, using later "now" so lock appears expired)
	txSteal, _ := db.Begin()
	ownerB := fmt.Sprintf("%s-%d", wsID, now+1)
	stolen, err := acquireRecurringMaterializationLockTx(txSteal, wsID, ownerB, now+601, now+601+600)
	if err != nil || !stolen {
		txSteal.Rollback()
		t.Fatalf("steal lock: err=%v stolen=%v", err, stolen)
	}
	txSteal.Commit()

	// Owner A tries to release — must fail
	txRelease, _ := db.Begin()
	materializedOccurrencesLocally(t, txRelease, wsID, horizon, now)
	relErr := releaseRecurringMaterializationLockSuccessTx(txRelease, wsID, ownerA, horizon.LegacyUnix(), now, horizon.Civil())
	if relErr == nil {
		txRelease.Rollback()
		t.Fatal("expected lock loss error")
	}
	txRelease.Rollback()
	if !strings.Contains(relErr.Error(), "lock lost") {
		t.Fatalf("error should mention lock lost, got: %v", relErr)
	}

	// Owner B must still hold lock
	var lockOwner sql.NullString
	db.QueryRow(`SELECT lock_owner FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&lockOwner)
	if !lockOwner.Valid || lockOwner.String != ownerB {
		t.Fatalf("lock_owner = %q, want %q", lockOwner.String, ownerB)
	}

	// Rolled-back occurrences must not exist
	var n int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id='series-lockreal' AND recurrence_sequence=2`, wsID).Scan(&n)
	if n != 0 {
		t.Fatal("seq 2 should be rolled back after lock loss")
	}

	// Markers unchanged
	p1AssertRuleNotAdvanced(t, db, wsID, "rule-lockreal", beforeTx, beforeGen, beforeGenCiv)
	var afterMat sql.NullInt64
	var afterMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&afterMat, &afterMatCiv)
	if !nullInt64Eq(beforeMat, afterMat) || !nullStringEq(beforeMatCiv, afterMatCiv) {
		t.Fatalf("materialized_until changed: (%v,%v) -> (%v,%v)", beforeMat, beforeMatCiv, afterMat, afterMatCiv)
	}
}

// materializedOccurrencesLocally inserts a transaction inside tx to simulate
// runner materialization work before a lock loss.
func materializedOccurrencesLocally(t *testing.T, tx *sql.Tx, wsID string, horizon CivilWriteDate, now int64) {
	t.Helper()
	_ = wsID
	_ = horizon
	execOneTx(tx, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-lockreal-2',?,'u-p1-lockreal','acc-lockreal','cat-lockreal','EXPENSE',15000,?,?,'p1 LockReal','pending',1,1,'rule-lockreal',2,'series-lockreal',?,?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
}

// ---------- P1: RUNNER ROLLBACK ----------

func TestCivil4BA_RunnerRollbackPreservesAllMarkers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "runnerrb")
	p1SeedRule(t, db, "runnerrb", wsID, "p1 RunnerRb", "2026-06-05")
	horizon := MustCivilWriteDate("2027-09-05")
	p1SeedMatState(t, db, wsID, horizon)
	// Pre-populate all four markers with non-NULL coherent values
	beforeGen := MustCivilWriteDate("2027-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET generated_until=?, generated_until_civil=? WHERE id='rule-runnerrb'`, beforeGen.LegacyUnix(), beforeGen.Civil())
	// materialized_until is already set to horizon by p1SeedMatState
	execTestSQL(t, db, `CREATE TEMP TRIGGER IF NOT EXISTS rbr_atomic BEFORE INSERT ON transactions WHEN NEW.description='p1 RunnerRb' AND NEW.recurrence_sequence=4 BEGIN SELECT RAISE(ABORT,'runner rollback'); END`)
	beforeTx, _, _ := p1SnapRule(t, db, wsID, "rule-runnerrb")
	var beforeMat sql.NullInt64
	var beforeMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&beforeMat, &beforeMatCiv)
	now := time.Now().Unix()
	err := RunRecurringMaterializationForWorkspace(context.Background(), db, wsID, now)
	if err == nil {
		t.Fatal("expected runner error with trigger injection")
	}
	// All four markers must match exact prior non-NULL values
	var afterTx int64
	var afterGen sql.NullInt64
	var afterGenCiv sql.NullString
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id='series-runnerrb'`, wsID).Scan(&afterTx)
	db.QueryRow(`SELECT generated_until, generated_until_civil FROM recurring_rules WHERE id='rule-runnerrb'`).Scan(&afterGen, &afterGenCiv)
	if afterTx != beforeTx {
		t.Fatalf("tx count = %d, want %d", afterTx, beforeTx)
	}
	if !nullInt64Eq(sql.NullInt64{Int64: beforeGen.LegacyUnix(), Valid: true}, afterGen) {
		t.Fatalf("generated_until changed: %v -> %v", beforeGen.LegacyUnix(), afterGen)
	}
	if !nullStringEq(sql.NullString{String: beforeGen.Civil(), Valid: true}, afterGenCiv) {
		t.Fatalf("generated_until_civil changed: %q -> %v", beforeGen.Civil(), afterGenCiv)
	}
	var afterMat sql.NullInt64
	var afterMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&afterMat, &afterMatCiv)
	if !nullInt64Eq(beforeMat, afterMat) {
		t.Fatalf("materialized_until changed: %v -> %v", beforeMat, afterMat)
	}
	if !nullStringEq(beforeMatCiv, afterMatCiv) {
		t.Fatalf("materialized_until_civil changed: %v -> %v", beforeMatCiv, afterMatCiv)
	}
}

func TestCivil4BA_ManualExceptionDueDateZeroReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "exczero")
	p1SeedRule(t, db, "exczero", wsID, "p1 ExcZero", "2026-06-05")
	horizon := MustCivilWriteDate("2027-09-05")
	p1SeedMatState(t, db, wsID, horizon)
	// Pre-populate markers non-NULL
	beforeGen := MustCivilWriteDate("2027-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET generated_until=?, generated_until_civil=? WHERE id='rule-exczero'`, beforeGen.LegacyUnix(), beforeGen.Civil())
	now := time.Now().Unix()
	// Insert manual exception with due_date=0, due_date_civil=NULL
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, is_recurring_exception, due_date, created_at, updated_at) VALUES ('tx-exczero-exc', ?, 'u-p1-exczero', 'acc-exczero', 'cat-exczero', 'EXPENSE', 15000, ?, ?, 'p1 ExcZero', 'pending', 1, 1, 'rule-exczero', 2, 'series-exczero', 1, 0, ?, ?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, _, _ := p1SnapRule(t, db, wsID, "rule-exczero")
	var beforeMat sql.NullInt64
	var beforeMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&beforeMat, &beforeMatCiv)
	err := RunRecurringMaterializationForWorkspace(context.Background(), db, wsID, now)
	if err == nil {
		t.Fatal("expected error for due_date zero")
	}
	if !strings.Contains(err.Error(), "invalid due_date") && !strings.Contains(err.Error(), "due_date") {
		t.Fatalf("error should mention due_date: %v", err)
	}
	// All four markers preserved
	var afterTx int64
	var afterGen sql.NullInt64
	var afterGenCiv sql.NullString
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id='series-exczero'`, wsID).Scan(&afterTx)
	db.QueryRow(`SELECT generated_until, generated_until_civil FROM recurring_rules WHERE id='rule-exczero'`).Scan(&afterGen, &afterGenCiv)
	if afterTx != beforeTx {
		t.Fatalf("tx count = %d, want %d", afterTx, beforeTx)
	}
	if !nullInt64Eq(sql.NullInt64{Int64: beforeGen.LegacyUnix(), Valid: true}, afterGen) {
		t.Fatalf("generated_until changed: %v -> %v", beforeGen.LegacyUnix(), afterGen)
	}
	if !nullStringEq(sql.NullString{String: beforeGen.Civil(), Valid: true}, afterGenCiv) {
		t.Fatalf("generated_until_civil changed: %q -> %v", beforeGen.Civil(), afterGenCiv)
	}
	var afterMat sql.NullInt64
	var afterMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&afterMat, &afterMatCiv)
	if !nullInt64Eq(beforeMat, afterMat) {
		t.Fatalf("materialized_until changed: %v -> %v", beforeMat, afterMat)
	}
	if !nullStringEq(beforeMatCiv, afterMatCiv) {
		t.Fatalf("materialized_until_civil changed: %v -> %v", beforeMatCiv, afterMatCiv)
	}
}

func TestCivil4BA_ManualExceptionDueDateNegativeReturnsError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := p1SeedBase(t, db, "excneg")
	p1SeedRule(t, db, "excneg", wsID, "p1 ExcNeg", "2026-06-05")
	horizon := MustCivilWriteDate("2027-09-05")
	p1SeedMatState(t, db, wsID, horizon)
	beforeGen := MustCivilWriteDate("2027-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET generated_until=?, generated_until_civil=? WHERE id='rule-excneg'`, beforeGen.LegacyUnix(), beforeGen.Civil())
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, is_recurring_exception, due_date, created_at, updated_at) VALUES ('tx-excneg-exc', ?, 'u-p1-excneg', 'acc-excneg', 'cat-excneg', 'EXPENSE', 15000, ?, ?, 'p1 ExcNeg', 'pending', 1, 1, 'rule-excneg', 2, 'series-excneg', 1, -1, ?, ?)`, wsID, MustCivilWriteDate("2026-07-05").LegacyUnix(), MustCivilWriteDate("2026-07-05").Civil(), now, now)
	beforeTx, _, _ := p1SnapRule(t, db, wsID, "rule-excneg")
	var beforeMat sql.NullInt64
	var beforeMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&beforeMat, &beforeMatCiv)
	err := RunRecurringMaterializationForWorkspace(context.Background(), db, wsID, now)
	if err == nil {
		t.Fatal("expected error for due_date negative")
	}
	if !strings.Contains(err.Error(), "invalid due_date") && !strings.Contains(err.Error(), "due_date") {
		t.Fatalf("error should mention due_date: %v", err)
	}
	var afterTx int64
	var afterGen sql.NullInt64
	var afterGenCiv sql.NullString
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id='series-excneg'`, wsID).Scan(&afterTx)
	db.QueryRow(`SELECT generated_until, generated_until_civil FROM recurring_rules WHERE id='rule-excneg'`).Scan(&afterGen, &afterGenCiv)
	if afterTx != beforeTx {
		t.Fatalf("tx count = %d, want %d", afterTx, beforeTx)
	}
	if !nullInt64Eq(sql.NullInt64{Int64: beforeGen.LegacyUnix(), Valid: true}, afterGen) {
		t.Fatalf("generated_until changed: %v -> %v", beforeGen.LegacyUnix(), afterGen)
	}
	if !nullStringEq(sql.NullString{String: beforeGen.Civil(), Valid: true}, afterGenCiv) {
		t.Fatalf("generated_until_civil changed: %q -> %v", beforeGen.Civil(), afterGenCiv)
	}
	var afterMat sql.NullInt64
	var afterMatCiv sql.NullString
	db.QueryRow(`SELECT materialized_until, materialized_until_civil FROM recurring_materialization_state WHERE workspace_id=?`, wsID).Scan(&afterMat, &afterMatCiv)
	if !nullInt64Eq(beforeMat, afterMat) {
		t.Fatalf("materialized_until changed: %v -> %v", beforeMat, afterMat)
	}
	if !nullStringEq(beforeMatCiv, afterMatCiv) {
		t.Fatalf("materialized_until_civil changed: %v -> %v", beforeMatCiv, afterMatCiv)
	}
}

func TestCivil4BA_ConversionBusinessPreservesPersistedDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := "ws-p1-preserve"
	userID := "u-p1-preserve"
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?,?,?, 'hash', ?,?)`, userID, userID, userID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?,?,'business','',?,?)`, wsID, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?,?,'ADMIN',?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-preserve', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-preserve', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)

	handler := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: p1ToggleTemplates()}
	// Create non-recurring transaction with due_date 2026-07-20
	_, err := handler.insertTransaction("EXPENSE", 15000, "p1 Preserve Conv", "", "", MustCivilWriteDate("2026-07-10"), "acc-preserve", "", "cat-preserve", 1, "pending", false, "", "", MustOptionalCivilWriteDate("2026-07-20"), nil, "", false)
	if err != nil {
		t.Fatalf("insert non-recurring: %v", err)
	}
	// Get the transaction ID by description (insertTransaction returns assignedInvoiceID, not tx ID)
	var txID string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id=? AND description='p1 Preserve Conv'`, wsID).Scan(&txID); err != nil {
		t.Fatalf("tx not found: err=%v", err)
	}
	t.Logf("txID=%s wsID=%s", txID, wsID)

	// Convert to recurring via HandleAtualizarTransacao with PRESERVE (no due_date in form)
	req := formPostMultipart(t, "/transacoes/"+txID+"/atualizar", url.Values{
		"lancamento_fixo":  {"on"},
		"data":             {"2026-07-10"},
		"valor":            {"150.00"},
		"origem_conta_id":  {"acc-preserve"},
		"categoria_id":     {"cat-preserve"},
		"tipo":             {"EXPENSE"},
		"recorrencia":      {"MONTHLY"},
		"status_pagamento": {"pending"},
		"descricao":        {"p1 Preserve Conv"},
		"escopo":           {"single"},
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, txID)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%q", rr.Code, rr.Body.String())
	}

	// Original transaction preserved
	var origDue int64
	var origDueCiv, origRCID, origSID string
	var origSeq int64
	db.QueryRow(`SELECT due_date, due_date_civil, recurring_rule_id, series_id, recurrence_sequence FROM transactions WHERE id=?`, txID).Scan(&origDue, &origDueCiv, &origRCID, &origSID, &origSeq)
	if origDue != MustCivilWriteDate("2026-07-20").LegacyUnix() {
		t.Fatalf("original due_date = %d, want %d", origDue, MustCivilWriteDate("2026-07-20").LegacyUnix())
	}
	if origDueCiv != "2026-07-20" {
		t.Fatalf("original due_date_civil = %q, want 2026-07-20", origDueCiv)
	}
	if origRCID == "" {
		t.Fatal("recurring_rule_id not set")
	}
	if origSID == "" {
		t.Fatal("series_id not set")
	}
	if origSeq != 1 {
		t.Fatalf("recurrence_sequence = %d, want 1", origSeq)
	}
	// No duplicate seq 1
	var nSeq1 int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=1`, wsID, origSID).Scan(&nSeq1)
	if nSeq1 != 1 {
		t.Fatalf("seq 1 count = %d, want 1", nSeq1)
	}

	ruleID := origRCID
	// Rule: due_date_offset_days = 10
	var offset sql.NullInt64
	var startDCiv, startsDCiv string
	var ruleStartDate, ruleStartsAt int64
	db.QueryRow(`SELECT due_date_offset_days, start_date, start_date_civil, starts_at, starts_at_civil FROM recurring_rules WHERE id=?`, ruleID).Scan(&offset, &ruleStartDate, &startDCiv, &ruleStartsAt, &startsDCiv)
	if !offset.Valid || offset.Int64 != 10 {
		t.Fatalf("due_date_offset_days = %v, want 10", offset)
	}
	if startDCiv != "2026-07-10" {
		t.Fatalf("start_date_civil = %q, want 2026-07-10", startDCiv)
	}
	if startsDCiv != "2026-07-10" {
		t.Fatalf("starts_at_civil = %q, want 2026-07-10", startsDCiv)
	}
	if mustLegacyDate("2026-07-10") != ruleStartDate {
		t.Fatalf("start_date = %d, want %d", ruleStartDate, mustLegacyDate("2026-07-10"))
	}
	if mustLegacyDate("2026-07-10") != ruleStartsAt {
		t.Fatalf("starts_at = %d, want %d", ruleStartsAt, mustLegacyDate("2026-07-10"))
	}
	// generated_until/generated_until_civil coherent
	var gen int64
	var genCiv string
	db.QueryRow(`SELECT generated_until, generated_until_civil FROM recurring_rules WHERE id=?`, ruleID).Scan(&gen, &genCiv)
	if gen == 0 {
		t.Fatal("generated_until not set")
	}
	if genCiv == "" {
		t.Fatal("generated_until_civil not set")
	}
	if LegacyUnixToCivilDateUTC(gen).String() != genCiv {
		t.Fatalf("generated_until/date_civil mismatch: %d->%q, civil=%q", gen, LegacyUnixToCivilDateUTC(gen).String(), genCiv)
	}

	// Seq 2: date_civil=2026-08-10, due_date_civil=2026-08-20
	var seq2DC, seq2DDC, seq2RuleID, seq2SID string
	var seq2DueDate int64
	var seq2 int64
	if err := db.QueryRow(`SELECT date_civil, due_date, due_date_civil, recurring_rule_id, series_id, recurrence_sequence FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=2`, wsID, origSID).Scan(&seq2DC, &seq2DueDate, &seq2DDC, &seq2RuleID, &seq2SID, &seq2); err != nil {
		t.Fatalf("seq 2: %v", err)
	}
	if seq2DC != "2026-08-10" {
		t.Fatalf("seq 2 date_civil = %q, want 2026-08-10", seq2DC)
	}
	if seq2DDC != "2026-08-20" {
		t.Fatalf("seq 2 due_date_civil = %q, want 2026-08-20", seq2DDC)
	}
	if seq2DueDate != mustLegacyDate("2026-08-20") {
		t.Fatalf("seq 2 due_date = %d, want %d", seq2DueDate, mustLegacyDate("2026-08-20"))
	}
	if LegacyUnixToCivilDateUTC(seq2DueDate).String() != seq2DDC {
		t.Fatalf("seq 2 due_date/date_civil mismatch: %d->%q, civil=%q", seq2DueDate, LegacyUnixToCivilDateUTC(seq2DueDate).String(), seq2DDC)
	}
	if seq2RuleID != ruleID {
		t.Fatalf("seq 2 recurring_rule_id = %q, want %q", seq2RuleID, ruleID)
	}
	if seq2SID != origSID {
		t.Fatalf("seq 2 series_id = %q, want %q", seq2SID, origSID)
	}
	if seq2 != 2 {
		t.Fatalf("recurrence_sequence = %d, want 2", seq2)
	}
	var nSeq2 int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=2`, wsID, origSID).Scan(&nSeq2)
	if nSeq2 != 1 {
		t.Fatalf("seq 2 count = %d, want 1", nSeq2)
	}

	// Rematerialize: no divergence, preserves due_date
	if err := EnsureRecurringMaterialized(db, wsID, MustCivilWriteDate("2026-10-05")); err != nil {
		t.Fatalf("rematerialize after conversion: %v", err)
	}
	var seq2DDCRem string
	db.QueryRow(`SELECT due_date_civil FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=2`, wsID, origSID).Scan(&seq2DDCRem)
	if seq2DDCRem != "2026-08-20" {
		t.Fatalf("seq 2 due_date_civil after remat = %q, want 2026-08-20", seq2DDCRem)
	}
}

func TestCivil4BA_RecurringProducerPreservesZeroDueDateOffset(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID := "ws-p1-zerooff"
	userID := "u-p1-zerooff"
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?,?,?, 'hash', ?,?)`, userID, userID, userID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?,?,'business','',?,?)`, wsID, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?,?,'ADMIN',?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-zerooff', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-zerooff', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)

	handler := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID}
	totalOcc := int64(3)
	// Same date for competence and due_date → offset zero
	_, err := handler.insertTransaction("EXPENSE", 10000, "p1 Zero Off", "", "", MustCivilWriteDate("2026-07-10"), "acc-zerooff", "", "cat-zerooff", 1, "pending", true, "MONTHLY", "", MustOptionalCivilWriteDate("2026-07-10"), &totalOcc, "", false)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	ruleID, _ := recurringRuleSeriesByDescription(t, db, wsID, "p1 Zero Off")
	var offset sql.NullInt64
	db.QueryRow(`SELECT due_date_offset_days FROM recurring_rules WHERE id=?`, ruleID).Scan(&offset)
	if !offset.Valid {
		t.Fatal("due_date_offset_days is NULL, want 0")
	}
	if offset.Int64 != 0 {
		t.Fatalf("due_date_offset_days = %d, want 0", offset.Int64)
	}

	// First occurrence has due_date = same day as date
	var dueDate int64
	var dueDateCiv, dateCiv string
	db.QueryRow(`SELECT due_date, due_date_civil, date_civil FROM transactions WHERE description='p1 Zero Off' AND recurrence_sequence=1`).Scan(&dueDate, &dueDateCiv, &dateCiv)
	if dueDate != mustLegacyDate("2026-07-10") {
		t.Fatalf("seq 1 due_date = %d, want %d", dueDate, mustLegacyDate("2026-07-10"))
	}
	if dueDateCiv != "2026-07-10" {
		t.Fatalf("seq 1 due_date_civil = %q, want 2026-07-10", dueDateCiv)
	}
	if LegacyUnixToCivilDateUTC(dueDate).String() != dueDateCiv {
		t.Fatalf("seq 1 due_date/date_civil mismatch: %d->%q, civil=%q", dueDate, LegacyUnixToCivilDateUTC(dueDate).String(), dueDateCiv)
	}

	// Seq 2: due_date = 2026-08-10 (same day offset zero)
	var seq2DueDate int64
	var seq2DueDateCiv, seq2DateCiv string
	db.QueryRow(`SELECT due_date, due_date_civil, date_civil FROM transactions WHERE description='p1 Zero Off' AND recurrence_sequence=2`).Scan(&seq2DueDate, &seq2DueDateCiv, &seq2DateCiv)
	if seq2DueDate != mustLegacyDate("2026-08-10") {
		t.Fatalf("seq 2 due_date = %d, want %d", seq2DueDate, mustLegacyDate("2026-08-10"))
	}
	if seq2DueDateCiv != "2026-08-10" {
		t.Fatalf("seq 2 due_date_civil = %q, want 2026-08-10", seq2DueDateCiv)
	}
	if seq2DateCiv != "2026-08-10" {
		t.Fatalf("seq 2 date_civil = %q, want 2026-08-10", seq2DateCiv)
	}
	if LegacyUnixToCivilDateUTC(seq2DueDate).String() != seq2DueDateCiv {
		t.Fatalf("seq 2 due_date/date_civil mismatch: %d->%q, civil=%q", seq2DueDate, LegacyUnixToCivilDateUTC(seq2DueDate).String(), seq2DueDateCiv)
	}
}

func mustLegacyDate(civil string) int64 {
	return MustCivilWriteDate(civil).LegacyUnix()
}

// p1SeedBusinessWorkspace creates a business workspace with a checking account and category.
func p1SeedBusinessWorkspace(t *testing.T, db *sql.DB, suffix string) (wsID, userID string) {
	t.Helper()
	wsID = "ws-p1-" + suffix
	userID = "u-p1-" + suffix
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?,?,?, 'hash', ?,?)`, userID, userID, userID+"@e.com", now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspaces (id, name, type, description, created_at, updated_at) VALUES (?,?,'business','',?,?)`, wsID, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?,?,'ADMIN',?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-`+suffix+`', ?, 'Checking', 'CHECKING', 0, 0, ?, ?)`, wsID, now, now)
	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-`+suffix+`', ?, 'ExpCat', 'tag', '#ef4444', 'EXPENSE', '', ?)`, wsID, now)
	return
}

// p1PreserveConvertTx creates a non-recurring tx, then converts via HandleAtualizarTransacao with PRESERVE.
func p1PreserveConvertTx(t *testing.T, db *sql.DB, wsID, userID, desc string) (txID string) {
	t.Helper()
	handler := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: p1ToggleTemplates()}
	_, err := handler.insertTransaction("EXPENSE", 15000, desc, "", "", MustCivilWriteDate("2026-07-10"), "acc-"+wsID[6:], "", "cat-"+wsID[6:], 1, "pending", false, "", "", MustOptionalCivilWriteDate("2026-07-20"), nil, "", false)
	if err != nil {
		t.Fatalf("insert non-recurring: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id=? AND description=?`, wsID, desc).Scan(&txID); err != nil {
		t.Fatalf("tx not found: err=%v", err)
	}
	req := formPostMultipart(t, "/transacoes/"+txID+"/atualizar", url.Values{
		"lancamento_fixo":  {"on"},
		"data":             {"2026-07-10"},
		"valor":            {"150.00"},
		"origem_conta_id":  {"acc-" + wsID[6:]},
		"categoria_id":     {"cat-" + wsID[6:]},
		"tipo":             {"EXPENSE"},
		"recorrencia":      {"MONTHLY"},
		"status_pagamento": {"pending"},
		"descricao":        {desc},
		"escopo":           {"single"},
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, txID)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%q", rr.Code, rr.Body.String())
	}
	return
}

// p1PreserveConvertExpectError performs PRESERVE conversion that should fail.
func p1PreserveConvertExpectError(t *testing.T, db *sql.DB, wsID, userID, desc, wantErrContains string) {
	t.Helper()
	handler := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: p1ToggleTemplates()}
	_, err := handler.insertTransaction("EXPENSE", 15000, desc, "", "", MustCivilWriteDate("2026-07-10"), "acc-"+wsID[6:], "", "cat-"+wsID[6:], 1, "pending", false, "", "", MustOptionalCivilWriteDate("2026-07-20"), nil, "", false)
	if err != nil {
		t.Fatalf("insert non-recurring: %v", err)
	}
	var txID string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE workspace_id=? AND description=?`, wsID, desc).Scan(&txID); err != nil {
		t.Fatalf("tx not found: err=%v", err)
	}
	req := formPostMultipart(t, "/transacoes/"+txID+"/atualizar", url.Values{
		"lancamento_fixo":  {"on"},
		"data":             {"2026-07-10"},
		"valor":            {"150.00"},
		"origem_conta_id":  {"acc-" + wsID[6:]},
		"categoria_id":     {"cat-" + wsID[6:]},
		"tipo":             {"EXPENSE"},
		"recorrencia":      {"MONTHLY"},
		"status_pagamento": {"pending"},
		"descricao":        {desc},
		"escopo":           {"single"},
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, txID)
	if rr.Code == http.StatusOK {
		t.Fatal("expected error, got 200")
	}
	if wantErrContains != "" && !strings.Contains(rr.Body.String(), wantErrContains) {
		t.Fatalf("error body %q, want containing %q", rr.Body.String(), wantErrContains)
	}
	if err := db.QueryRow(`SELECT id FROM recurring_rules WHERE workspace_id=?`, wsID).Scan(new(string)); err == nil {
		t.Fatal("no rule should be created on error")
	}
	var n int
	db.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE workspace_id=?`, wsID).Scan(&n)
	if n > 0 {
		t.Fatalf("%d series created, want 0", n)
	}
}

func TestCivil4BA_ConversionBusinessPreservesLegacyDueDateWithoutBackfill(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID := p1SeedBusinessWorkspace(t, db, "legacy")
	now := time.Now().Unix()
	desc := "p1 Legacy Preserve"
	// Manually insert non-recurring tx with positive due_date but NULL due_date_civil
	txID := "tx-legacy-preserve"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, 'acc-legacy', 'cat-legacy', 'EXPENSE', 15000, ?, ?, ?, ?, 'pending', 1, 1, ?, ?)`, txID, wsID, userID, MustCivilWriteDate("2026-07-10").LegacyUnix(), MustCivilWriteDate("2026-07-10").Civil(), MustCivilWriteDate("2026-07-20").LegacyUnix(), desc, now, now)

	handler := TransactionHandler{DB: db, WorkspaceID: wsID, UserID: userID, Templates: p1ToggleTemplates()}
	req := formPostMultipart(t, "/transacoes/"+txID+"/atualizar", url.Values{
		"lancamento_fixo":  {"on"},
		"data":             {"2026-07-10"},
		"valor":            {"150.00"},
		"origem_conta_id":  {"acc-legacy"},
		"categoria_id":     {"cat-legacy"},
		"tipo":             {"EXPENSE"},
		"recorrencia":      {"MONTHLY"},
		"status_pagamento": {"pending"},
		"descricao":        {desc},
		"escopo":           {"single"},
	})
	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, req, txID)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%q", rr.Code, rr.Body.String())
	}

	// Original: due_date preserved, due_date_civil still NULL (no backfill)
	var origDue int64
	var origDueCiv sql.NullString
	db.QueryRow(`SELECT due_date, due_date_civil FROM transactions WHERE id=?`, txID).Scan(&origDue, &origDueCiv)
	if origDue != MustCivilWriteDate("2026-07-20").LegacyUnix() {
		t.Fatalf("original due_date = %d, want %d", origDue, MustCivilWriteDate("2026-07-20").LegacyUnix())
	}
	if origDueCiv.Valid {
		t.Fatalf("due_date_civil backfilled to %q, want NULL", origDueCiv.String)
	}

	// Rule: offset correct via legacy fallback
	ruleID := getRuleIDForTx(t, db, txID)
	seriesID := getSeriesIDForTx(t, db, txID)
	var offset sql.NullInt64
	db.QueryRow(`SELECT due_date_offset_days FROM recurring_rules WHERE id=?`, ruleID).Scan(&offset)
	if !offset.Valid || offset.Int64 != 10 {
		t.Fatalf("due_date_offset_days = %v, want 10", offset)
	}

	// Seq 2: due_date and due_date_civil both set
	var seq2Due int64
	var seq2DueCiv, seq2DC string
	db.QueryRow(`SELECT due_date, due_date_civil, date_civil FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=2`, wsID, seriesID).Scan(&seq2Due, &seq2DueCiv, &seq2DC)
	if seq2DC != "2026-08-10" {
		t.Fatalf("seq 2 date_civil = %q, want 2026-08-10", seq2DC)
	}
	if seq2DueCiv != "2026-08-20" {
		t.Fatalf("seq 2 due_date_civil = %q, want 2026-08-20", seq2DueCiv)
	}
	if seq2Due != mustLegacyDate("2026-08-20") {
		t.Fatalf("seq 2 due_date = %d, want %d", seq2Due, mustLegacyDate("2026-08-20"))
	}
	var nSeq2 int
	db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id=? AND series_id=? AND recurrence_sequence=2`, wsID, seriesID).Scan(&nSeq2)
	if nSeq2 != 1 {
		t.Fatalf("seq 2 count = %d, want 1", nSeq2)
	}
}

func TestCivil4BA_ConversionBusinessPreserveRejectsInvalidDueDateCivil(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID := p1SeedBusinessWorkspace(t, db, "invciv")
	now := time.Now().Unix()
	desc := "p1 InvCiv"
	txID := "tx-invciv"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, due_date_civil, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, 'acc-invciv', 'cat-invciv', 'EXPENSE', 15000, ?, ?, ?, '2026-02-30', ?, 'pending', 1, 1, ?, ?)`, txID, wsID, userID, MustCivilWriteDate("2026-07-10").LegacyUnix(), MustCivilWriteDate("2026-07-10").Civil(), MustCivilWriteDate("2026-07-20").LegacyUnix(), desc, now, now)

	p1PreserveConvertExpectError(t, db, wsID, userID, desc, "")
}

func TestCivil4BA_ConversionBusinessPreserveRejectsDivergentDueDatePair(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID := p1SeedBusinessWorkspace(t, db, "divpair")
	now := time.Now().Unix()
	desc := "p1 DivPair"
	txID := "tx-divpair"
	// due_date → 2026-07-20, due_date_civil = 2026-07-21 (diverge)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, due_date_civil, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, 'acc-divpair', 'cat-divpair', 'EXPENSE', 15000, ?, ?, ?, '2026-07-21', ?, 'pending', 1, 1, ?, ?)`, txID, wsID, userID, MustCivilWriteDate("2026-07-10").LegacyUnix(), MustCivilWriteDate("2026-07-10").Civil(), MustCivilWriteDate("2026-07-20").LegacyUnix(), desc, now, now)

	p1PreserveConvertExpectError(t, db, wsID, userID, desc, "par divergente")
}

func TestCivil4BA_ConversionBusinessPreserveRejectsCivilWithoutInteger(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID := p1SeedBusinessWorkspace(t, db, "civnoint")
	now := time.Now().Unix()
	desc := "p1 CivNoInt"
	txID := "tx-civnoint"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date_civil, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, 'acc-civnoint', 'cat-civnoint', 'EXPENSE', 15000, ?, ?, '2026-07-20', ?, 'pending', 1, 1, ?, ?)`, txID, wsID, userID, MustCivilWriteDate("2026-07-10").LegacyUnix(), MustCivilWriteDate("2026-07-10").Civil(), desc, now, now)

	p1PreserveConvertExpectError(t, db, wsID, userID, desc, "par inconsistente")
}

func TestCivil4BA_ConversionBusinessPreserveRejectsZeroDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID := p1SeedBusinessWorkspace(t, db, "zerodue")
	now := time.Now().Unix()
	desc := "p1 ZeroDue"
	txID := "tx-zerodue"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, 'acc-zerodue', 'cat-zerodue', 'EXPENSE', 15000, ?, ?, 0, ?, 'pending', 1, 1, ?, ?)`, txID, wsID, userID, MustCivilWriteDate("2026-07-10").LegacyUnix(), MustCivilWriteDate("2026-07-10").Civil(), desc, now, now)

	p1PreserveConvertExpectError(t, db, wsID, userID, desc, "inválido")
}

func TestCivil4BA_ConversionBusinessPreserveRejectsNegativeDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	wsID, userID := p1SeedBusinessWorkspace(t, db, "negdue")
	now := time.Now().Unix()
	desc := "p1 NegDue"
	txID := "tx-negdue"
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, due_date, description, status, installment_number, total_installments, created_at, updated_at) VALUES (?, ?, ?, 'acc-negdue', 'cat-negdue', 'EXPENSE', 15000, ?, ?, -1, ?, 'pending', 1, 1, ?, ?)`, txID, wsID, userID, MustCivilWriteDate("2026-07-10").LegacyUnix(), MustCivilWriteDate("2026-07-10").Civil(), desc, now, now)

	p1PreserveConvertExpectError(t, db, wsID, userID, desc, "inválido")
}

func getRuleIDForTx(t *testing.T, db *sql.DB, txID string) string {
	var ruleID string
	if err := db.QueryRow(`SELECT recurring_rule_id FROM transactions WHERE id=?`, txID).Scan(&ruleID); err != nil || ruleID == "" {
		t.Fatalf("recurring_rule_id for %s: err=%v val=%q", txID, err, ruleID)
	}
	return ruleID
}

func getSeriesIDForTx(t *testing.T, db *sql.DB, txID string) string {
	var seriesID string
	if err := db.QueryRow(`SELECT series_id FROM transactions WHERE id=?`, txID).Scan(&seriesID); err != nil || seriesID == "" {
		t.Fatalf("series_id for %s: err=%v val=%q", txID, err, seriesID)
	}
	return seriesID
}
