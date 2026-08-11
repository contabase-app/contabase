package database

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
)

func openAuditTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/contabase.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=wal")
	if err != nil {
		t.Fatalf("open audit test db: %v", err)
	}
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("execute DDL: %v", err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dbPath
}

func openAuditTestDBNoMigrations(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := dir + "/contabase.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=wal")
	if err != nil {
		t.Fatalf("open audit test db: %v", err)
	}
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("execute DDL: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dbPath
}

func seedUserAndWorkspace(t *testing.T, db *sql.DB, userID, wsID, wsType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO users (id, name, email, password_hash) VALUES (?, 'Fixture', ?, 'hash')`,
		userID, userID+"@test.example"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO workspaces (id, name, type) VALUES (?, 'Fixture', ?)`,
		wsID, wsType); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role) VALUES (?, ?, 'ADMIN')`,
		wsID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO accounts (id, workspace_id, name, type) VALUES (?, ?, 'Fixture', 'CHECKING')`,
		"acc-"+wsID, wsID); err != nil {
		t.Fatalf("insert account: %v", err)
	}
}

func TestQuickCheckHelper_Ok(t *testing.T) {
	db, _ := openAuditTestDB(t)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := quickCheckConn(context.Background(), conn); err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
}

func TestQuickCheckHelper_RejectsNonOk(t *testing.T) {
	tests := []struct {
		name   string
		vals   []string
		wantOk bool
	}{
		{"single_ok", []string{"ok"}, true},
		{"single_bad", []string{"bad"}, false},
		{"empty", []string{}, false},
		{"multiple", []string{"ok", "ok"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := func() error {
				if len(tt.vals) == 0 {
					return errQuickCheckNoRows()
				}
				if len(tt.vals) > 1 {
					return errQuickCheckMultipleRows(tt.vals)
				}
				if tt.vals[0] != "ok" {
					return errQuickCheckFailed(tt.vals[0])
				}
				return nil
			}
			err := check()
			if tt.wantOk && err != nil {
				t.Fatalf("wanted ok, got %v", err)
			}
			if !tt.wantOk && err == nil {
				t.Fatal("wanted error, got nil")
			}
		})
	}
}

func errQuickCheckNoRows() error {
	return errQuickCheckGeneric("quick_check returned no rows")
}
func errQuickCheckMultipleRows(vals []string) error {
	return errQuickCheckGeneric("quick_check returned multiple rows: " + strings.Join(vals, ", "))
}
func errQuickCheckFailed(val string) error {
	return errQuickCheckGeneric("quick_check failed: " + val)
}
func errQuickCheckGeneric(msg string) error {
	return &quickCheckError{msg}
}

type quickCheckError struct{ msg string }

func (e *quickCheckError) Error() string { return e.msg }

func TestCivilDateLegacyAudit_EmptyDB(t *testing.T) {
	db, _ := openAuditTestDB(t)
	report, exitCode, err := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if report.Summary.TransactionsScanned != 0 {
		t.Fatalf("expected 0 scanned, got %d", report.Summary.TransactionsScanned)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("expected 0 findings, got %d", len(report.Findings))
	}
}

func TestCivilDateLegacyAudit_ValidNormalOccurrence(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")
	report, exitCode, err := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if report.Summary.Valid != 1 {
		t.Fatalf("expected 1 valid, got %d", report.Summary.Valid)
	}
}

func TestCivilDateLegacyAudit_DateCivilMissing(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1)`, noon)
	report, exitCode, err := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exitCode != 2 {
		t.Fatalf("expected exit 2, got %d", exitCode)
	}
	if report.Summary.Deterministic != 1 {
		t.Fatalf("expected 1 deterministic, got %d", report.Summary.Deterministic)
	}
	f := report.Findings[0]
	if f.ReasonCode != ReasonDateCivilMissing {
		t.Fatalf("expected DATE_CIVIL_MISSING, got %s", f.ReasonCode)
	}
	if f.ProposedCivilValue != "2026-07-12" {
		t.Fatalf("expected proposed 2026-07-12, got %s", f.ProposedCivilValue)
	}
}

func TestCivilDateLegacyAudit_DateCivilInvalid(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'not-a-date', 'rule-1', 'series-1', 1)`, noon)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	f := report.Findings[0]
	if f.ReasonCode != ReasonDateCivilInvalid {
		t.Fatalf("expected DATE_CIVIL_INVALID, got %s", f.ReasonCode)
	}
}

func TestCivilDateLegacyAudit_DateCivilMismatch(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, '2026-07-13', 'rule-1', 'series-1', 1)`, noon)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	if report.Findings[0].ReasonCode != ReasonDateCivilMismatch {
		t.Fatalf("expected DATE_CIVIL_MISMATCH, got %s", report.Findings[0].ReasonCode)
	}
}

func TestCivilDateLegacyAudit_DueDateDeterministic(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, due_date, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12", noon)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 2 {
		t.Fatalf("expected exit 2, got %d", exitCode)
	}
	hasDue := false
	for _, f := range report.Findings {
		if f.AuditedField == "due_date_civil" && f.ReasonCode == ReasonDueDateCivilMissing {
			hasDue = true
			if f.ProposedCivilValue != "2026-07-12" {
				t.Fatalf("expected proposed due date 2026-07-12, got %s", f.ProposedCivilValue)
			}
		}
	}
	if !hasDue {
		t.Fatalf("expected due_date_civil finding")
	}
}

func TestCivilDateLegacyAudit_DueDateCivilInvalid(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, due_date, due_date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, ?, 'not-a-date', 'rule-1', 'series-1', 1)`, noon, "2026-07-12", noon)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	found := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonDueDateCivilInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected DUE_DATE_CIVIL_INVALID")
	}
}

func TestCivilDateLegacyAudit_ManualExceptionValid(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence, is_recurring_exception, recurring_exception_reason)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1, 1, 'manual edit')`, noon, "2026-07-12")
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if report.Summary.Valid != 1 {
		t.Fatalf("expected 1 valid, got %d", report.Summary.Valid)
	}
}

func TestCivilDateLegacyAudit_HistoricalDivergenceDateCivilNull(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence, is_recurring_exception, recurring_exception_reason)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1, 1, ?)`, noon, legacyDivergenceReason)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 2 {
		t.Fatalf("expected exit 2, got %d", exitCode)
	}
	found := false
	for _, f := range report.Findings {
		if f.AuditedField == "date_civil" && f.SemanticState == SemanticHistoricalDivergence &&
			f.AuditOutcome == AuditLegacyIncompleteDeterministic && f.ReasonCode == ReasonDateCivilMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected HISTORICAL_DIVERGENCE + DETERMINISTIC finding")
	}
}

func TestCivilDateLegacyAudit_Types(t *testing.T) {
	for _, typ := range []string{"INCOME", "EXPENSE", "TRANSFER"} {
		t.Run(typ, func(t *testing.T) {
			db, _ := openAuditTestDB(t)
			seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
			noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
			mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
			dest := "NULL"
			if typ == "TRANSFER" {
				mustExec(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type) VALUES ('acc-dest', 'ws-1', 'Dest', 'CHECKING')`)
				dest = "'acc-dest'"
			}
			mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, destination_account_id, type, amount, start_date, frequency, series_id, active)
					VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', `+dest+`, '`+typ+`', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
			mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
					VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', '`+typ+`', 1000, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")
			_, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
			if exitCode != 0 {
				t.Fatalf("expected exit 0 for %s, got %d", typ, exitCode)
			}
		})
	}
}

func TestCivilDateLegacyAudit_BusinessWorkspace(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-biz", "business")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-biz', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-biz', 'u-1', 'acc-ws-biz', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-biz', 'u-1', 'acc-ws-biz', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")
	_, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_SequenceDuplicate(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES
			('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1),
			('rule-2', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence) VALUES
			('tx-a', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1),
			('tx-b', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 2000, ?, ?, 'rule-2', 'series-1', 1)`, noon, "2026-07-12", noon, "2026-07-12")
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	hasDup := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonSequenceDuplicate {
			hasDup = true
		}
	}
	if !hasDup {
		t.Fatalf("expected SEQUENCE_DUPLICATE")
	}
}

func TestCivilDateLegacyAudit_SequenceOutsideWindow(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active, effective_start_sequence, effective_end_sequence)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1, 2, 5)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	hasWindow := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonSequenceOutsideRuleWindow {
			hasWindow = true
		}
	}
	if !hasWindow {
		t.Fatalf("expected SEQUENCE_OUTSIDE_RULE_WINDOW")
	}
}

func TestCivilDateLegacyAudit_SequenceAtBoundaries(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active, effective_start_sequence, effective_end_sequence)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1, 2, 5)`, noon)
	for _, s := range []struct {
		id  string
		seq int64
	}{
		{"tx-start", 2},
		{"tx-end", 5},
		{"tx-mid", 3},
	} {
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
				VALUES (?, 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', ?)`, s.id, noon, "2026-07-12", s.seq)
	}
	_, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for boundaries, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_ParentRule(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		db, _ := openAuditTestDB(t)
		seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
		noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
				VALUES ('parent-rule', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
		mustExec(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = 'parent-rule'`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active, parent_rule_id)
				VALUES ('child-rule', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1, 'parent-rule')`, noon)
		mustExec(t, db, `UPDATE recurring_rules SET active = 1 WHERE id = 'child-rule'`)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'child-rule', 'series-1', 1)`, noon, "2026-07-12")
		_, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
		if exitCode != 0 {
			t.Fatalf("expected exit 0, got %d", exitCode)
		}
	})
	t.Run("parent_not_found", func(t *testing.T) {
		db, _ := openAuditTestDB(t)
		seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
		noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
		mustExec(t, db, `PRAGMA foreign_keys = OFF`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active, parent_rule_id)
				VALUES ('child-rule', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1, 'fake-parent')`, noon)
		mustExec(t, db, `PRAGMA foreign_keys = ON`)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'child-rule', 'series-1', 1)`, noon, "2026-07-12")
		report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
		if exitCode != 3 {
			t.Fatalf("expected exit 3, got %d", exitCode)
		}
		found := false
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonParentRuleNotFound {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected PARENT_RULE_NOT_FOUND")
		}
	})
	t.Run("self_parent", func(t *testing.T) {
		db, _ := openAuditTestDB(t)
		seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
		noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active, parent_rule_id)
				VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1, 'rule-1')`, noon)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")
		report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
		if exitCode != 3 {
			t.Fatalf("expected exit 3, got %d", exitCode)
		}
		found := false
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonParentRuleStructureInvalid {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected PARENT_RULE_STRUCTURE_INVALID")
		}
	})
	t.Run("parent_cross_workspace", func(t *testing.T) {
		db, _ := openAuditTestDB(t)
		seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
		seedUserAndWorkspace(t, db, "u-2", "ws-2", "personal")
		noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-2', 'ws-2', 'u-2')`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
				VALUES ('parent-rule', 'ws-2', 'u-2', 'acc-ws-2', 'INCOME', 1000, ?, 'MONTHLY', 'series-2', 1)`, noon)
		mustExec(t, db, `UPDATE recurring_rules SET active = 0 WHERE id = 'parent-rule'`)
		mustExec(t, db, `INSERT OR IGNORE INTO accounts (id, workspace_id, name, type) VALUES ('acc-ws-1', 'ws-1', 'Fix', 'CHECKING')`)
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active, parent_rule_id)
				VALUES ('child-rule', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1, 'parent-rule')`, noon)
		mustExec(t, db, `UPDATE recurring_rules SET active = 1 WHERE id = 'child-rule'`)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'child-rule', 'series-1', 1)`, noon, "2026-07-12")
		report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
		if exitCode != 3 {
			t.Fatalf("expected exit 3, got %d", exitCode)
		}
		found := false
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonCrossWorkspaceReference && f.AuditedField == "parent_rule" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected CROSS_WORKSPACE_REFERENCE for parent rule")
		}
	})
}

func TestCivilDateLegacyAudit_Invoice(t *testing.T) {
	cases := []struct {
		name       string
		invoiceID  string
		invStatus  string
		wantReason string
		wantExit   int
	}{
		{"no_invoice", "", "", "", 0},
		{"open", "inv-1", "OPEN", "", 0},
		{"closed", "inv-1", "CLOSED", "", 0},
		{"paid", "inv-1", "PAID", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := openAuditTestDB(t)
			seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
			noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
			mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
			mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
					VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'EXPENSE', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
			if tc.invoiceID != "" {
				mustExec(t, db, `INSERT OR IGNORE INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at)
						VALUES (?, 'acc-ws-1', '2026-07', ?, ?, ?, ?)`, tc.invoiceID, noon, noon, tc.invStatus, noon)
			}
			invID := "NULL"
			if tc.invoiceID != "" {
				invID = "'" + tc.invoiceID + "'"
			}
			mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, invoice_id, recurring_rule_id, series_id, recurrence_sequence)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'EXPENSE', 1000, ?, ?, `+invID+`, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")
			report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
			if exitCode != tc.wantExit {
				t.Fatalf("expected exit %d, got %d", tc.wantExit, exitCode)
			}
			if tc.wantReason != "" {
				found := false
				for _, f := range report.Findings {
					if f.ReasonCode == tc.wantReason {
						found = true
					}
				}
				if !found {
					t.Fatalf("expected %s finding", tc.wantReason)
				}
			}
		})
	}
}

func TestCivilDateLegacyAudit_ExceptionStructure(t *testing.T) {
	t.Run("exception_null_reason", func(t *testing.T) {
		db, _ := openAuditTestDB(t)
		seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
		noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
				VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence, is_recurring_exception)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1, 1)`, noon, "2026-07-12")
		report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
		if exitCode != 3 {
			t.Fatalf("expected exit 3, got %d", exitCode)
		}
		found := false
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonExceptionStructureInvalid {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected EXCEPTION_STRUCTURE_INVALID")
		}
	})
	t.Run("non_exception_with_reason", func(t *testing.T) {
		db, _ := openAuditTestDB(t)
		seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
		noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
				VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence, is_recurring_exception, recurring_exception_reason)
				VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1, 0, 'orphan reason')`, noon, "2026-07-12")
		report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
		if exitCode != 3 {
			t.Fatalf("expected exit 3, got %d", exitCode)
		}
		found := false
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonExceptionStructureInvalid {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected EXCEPTION_STRUCTURE_INVALID")
		}
	})
}

func TestCivilDateLegacyAudit_SeriesNotFound(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 1)`, noon)
	mustExec(t, db, `PRAGMA foreign_keys = OFF`)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'fake-series', 1)`, noon, "2026-07-12")
	mustExec(t, db, `PRAGMA foreign_keys = ON`)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	found := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonSeriesNotFound {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected SERIES_NOT_FOUND")
	}
}

func TestCivilDateLegacyAudit_SchemaIncompatible(t *testing.T) {
	db, dbPath := openAuditTestDBNoMigrations(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1')`, noon)
	_, exitCode, err := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if err == nil {
		t.Fatal("expected error for schema without civil columns")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit 1, got %d", exitCode)
	}
	// confirm DB still exists and wasn't modified
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("db should still exist: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("db should not be empty")
	}
}

func TestCivilDateLegacyAudit_CrossWorkspace(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	seedUserAndWorkspace(t, db, "u-2", "ws-2", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-2', 'ws-2', 'u-2')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-2', 'ws-2', 'u-2', 'acc-ws-2', 'INCOME', 1000, ?, 'MONTHLY', 'series-2', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-2', 'series-2', 1)`, noon, "2026-07-12")
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3 for cross-workspace, got %d", exitCode)
	}
	found := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonCrossWorkspaceReference {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CROSS_WORKSPACE_REFERENCE")
	}
}

func TestCivilDateLegacyAudit_PaidAndPending(t *testing.T) {
	for _, status := range []string{"paid", "pending"} {
		t.Run(status, func(t *testing.T) {
			db, _ := openAuditTestDB(t)
			seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
			noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
			mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
			mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
					VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
			mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, status, recurring_rule_id, series_id, recurrence_sequence)
					VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12", status)
			report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
			if exitCode != 0 {
				t.Fatalf("expected exit 0 for %s, got %d", status, exitCode)
			}
			if report.Summary.Valid < 1 {
				t.Fatalf("expected >=1 valid for %s", status)
			}
		})
	}
}

func TestCivilDateLegacyAudit_RuleNotFound(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `PRAGMA foreign_keys = OFF`)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'nonexistent-rule', 1)`, noon)
	mustExec(t, db, `PRAGMA foreign_keys = ON`)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	found := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonRuleNotFound {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected RULE_NOT_FOUND")
	}
}

func TestCivilDateLegacyAudit_SequenceInvalid(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 0)`, noon)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 3 {
		t.Fatalf("expected exit 3, got %d", exitCode)
	}
	found := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonSequenceInvalid {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected SEQUENCE_INVALID")
	}
}

func TestCivilDateLegacyAudit_MidnightUTC(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, midnight)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1)`, midnight, "2026-07-12")
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for midnight UTC, got %d", exitCode)
	}
	if report.Summary.Valid != 1 {
		t.Fatalf("expected 1 valid, got %d", report.Summary.Valid)
	}
}

func TestCivilDateLegacyAudit_NoonSameDay(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, '2026-07-12', 'rule-1', 'series-1', 1)`, midnight)
	_, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for midnight/noon same day, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_NearDayBoundary(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	justBefore := time.Date(2026, 7, 12, 23, 59, 59, 0, time.UTC).Unix()
	justAfter := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, justBefore)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, '2026-07-12', 'rule-1', 'series-1', 1)`+" ,"+`('tx-2', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 2000, ?, '2026-07-13', 'rule-1', 'series-1', 2)`, justBefore, justAfter)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if report.Summary.Valid != 2 {
		t.Fatalf("expected 2 valid, got %d", report.Summary.Valid)
	}
}

func TestCivilDateLegacyAudit_TwoSeriesSameWorkspace(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	for _, s := range []string{"series-1", "series-2"} {
		mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES (?, 'ws-1', 'u-1')`, s)
	}
	for _, r := range []string{"rule-1", "rule-2"} {
		mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
				VALUES (?, 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', ?, 1)`, r, noon, "series-"+r[5:])
	}
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, '2026-07-12', 'rule-1', 'series-1', 1)`+" ,"+`('tx-2', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 2000, ?, '2026-07-12', 'rule-2', 'series-2', 1)`, noon, noon)
	report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if report.Summary.TransactionsScanned != 2 {
		t.Fatalf("expected 2 scanned, got %d", report.Summary.TransactionsScanned)
	}
	if report.Summary.SeriesScanned != 2 {
		t.Fatalf("expected 2 series, got %d", report.Summary.SeriesScanned)
	}
}

func TestCivilDateLegacyAudit_FullIDsInText(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-long-id-12345', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1)`, noon)
	report, _, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	var buf bytes.Buffer
	PrintAuditReportText(report, &buf)
	out := buf.String()
	if !strings.Contains(out, "tx-long-id-12345") {
		t.Fatalf("expected full transaction ID in output, got truncated: %s", out)
	}
}

func TestCivilDateLegacyAudit_JSONOutput(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1)`, noon)
	var buf bytes.Buffer
	report, _, _ := RunCivilDateLegacyAudit(context.Background(), db, "json")
	PrintAuditReportJSON(report, &buf)
	var decoded AuditReport
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json decode failed: %v", err)
	}
	if decoded.SchemaVersion != auditSchemaVersion {
		t.Fatalf("expected schema_version %d, got %d", auditSchemaVersion, decoded.SchemaVersion)
	}
	if decoded.Summary.Deterministic != 1 {
		t.Fatalf("expected 1 deterministic, got %d", decoded.Summary.Deterministic)
	}
}

func TestCivilDateLegacyAudit_Idempotent(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1)`, noon)
	r1, c1, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	r2, c2, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	if c1 != c2 {
		t.Fatalf("exit code diverged: %d vs %d", c1, c2)
	}
	if r1.Summary != r2.Summary {
		t.Fatalf("summaries diverged")
	}
}

func TestCivilDateLegacyAudit_NilDB(t *testing.T) {
	_, exitCode, err := RunCivilDateLegacyAudit(context.Background(), nil, "text")
	if err == nil {
		t.Fatal("expected error for nil db")
	}
	if exitCode != 1 {
		t.Fatalf("expected exit 1, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_ZeroWriteComprehensive(t *testing.T) {
	db, dirPath := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, ?, 'rule-1', 'series-1', 1)`, noon, "2026-07-12")

	beforeFiles := listFiles(dirPath)
	var beforeUserVersion int
	db.QueryRow("PRAGMA user_version").Scan(&beforeUserVersion)

	var beforeSchema []struct {
		Type    string
		Name    string
		TblName string
		SQL     string
	}
	rows, _ := db.Query("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY name")
	for rows.Next() {
		var s struct{ Type, Name, TblName, SQL string }
		rows.Scan(&s.Type, &s.Name, &s.TblName, &s.SQL)
		beforeSchema = append(beforeSchema, s)
	}
	rows.Close()

	var beforeRowCounts map[string]int64
	rows, _ = db.Query("SELECT tbl_name, count FROM (SELECT 'transactions' as tbl_name, COUNT(*) as count FROM transactions UNION ALL SELECT 'recurring_rules', COUNT(*) FROM recurring_rules UNION ALL SELECT 'recurring_series', COUNT(*) FROM recurring_series UNION ALL SELECT 'workspaces', COUNT(*) FROM workspaces)")
	beforeRowCounts = make(map[string]int64)
	for rows.Next() {
		var tbl string
		var cnt int64
		rows.Scan(&tbl, &cnt)
		beforeRowCounts[tbl] = cnt
	}
	rows.Close()

	RunCivilDateLegacyAudit(context.Background(), db, "text")

	var afterUserVersion int
	db.QueryRow("PRAGMA user_version").Scan(&afterUserVersion)
	if afterUserVersion != beforeUserVersion {
		t.Fatalf("user_version changed: %d -> %d", beforeUserVersion, afterUserVersion)
	}

	var afterSchema []struct{ Type, Name, TblName, SQL string }
	rows, _ = db.Query("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY name")
	for rows.Next() {
		var s struct{ Type, Name, TblName, SQL string }
		rows.Scan(&s.Type, &s.Name, &s.TblName, &s.SQL)
		afterSchema = append(afterSchema, s)
	}
	rows.Close()
	if len(beforeSchema) != len(afterSchema) {
		t.Fatalf("schema objects changed: %d -> %d", len(beforeSchema), len(afterSchema))
	}
	for i := range beforeSchema {
		if beforeSchema[i] != afterSchema[i] {
			t.Fatalf("schema changed at %s: %v vs %v", beforeSchema[i].Name, beforeSchema[i], afterSchema[i])
		}
	}

	var afterRowCounts map[string]int64
	rows, _ = db.Query("SELECT tbl_name, count FROM (SELECT 'transactions' as tbl_name, COUNT(*) as count FROM transactions UNION ALL SELECT 'recurring_rules', COUNT(*) FROM recurring_rules UNION ALL SELECT 'recurring_series', COUNT(*) FROM recurring_series UNION ALL SELECT 'workspaces', COUNT(*) FROM workspaces)")
	afterRowCounts = make(map[string]int64)
	for rows.Next() {
		var tbl string
		var cnt int64
		rows.Scan(&tbl, &cnt)
		afterRowCounts[tbl] = cnt
	}
	rows.Close()
	for tbl, cnt := range beforeRowCounts {
		if afterRowCounts[tbl] != cnt {
			t.Fatalf("row count changed for %s: %d -> %d", tbl, cnt, afterRowCounts[tbl])
		}
	}

	afterFiles := listFiles(dirPath)
	for _, f := range afterFiles {
		if !containsFile(beforeFiles, f) {
			t.Fatalf("new file created: %s", f)
		}
	}
}

func listFiles(dir string) []string {
	var files []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			relPath, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(relPath))
		}
		return nil
	})
	return files
}

func containsFile(files []string, target string) bool {
	for _, f := range files {
		if f == target {
			return true
		}
	}
	return false
}

func TestCivilDateLegacyAudit_NoSensitiveDataInOutput(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence, description, notes)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1, 'secret desc', 'secret notes')`, noon)
	var buf bytes.Buffer
	report, _, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	PrintAuditReportJSON(report, &buf)
	output := strings.ToLower(buf.String())
	for _, word := range []string{"description", "notes", "amount", "user", "email", "password", "secret"} {
		if strings.Contains(output, word) {
			t.Fatalf("output contains forbidden word: %s", word)
		}
	}
}

func TestCivilDateLegacyAudit_AllReasonCodes(t *testing.T) {
	tests := []struct {
		name       string
		setupSQL   []string
		wantReason string
		wantExit   int
	}{
		{
			"date_civil_missing",
			[]string{`INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('s-1', 'ws-1', 'u-1')`,
				`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES ('r-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 's-1', 1)`,
				`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'r-1', 's-1', 1)`},
			ReasonDateCivilMissing, 2,
		},
		{
			"date_civil_invalid",
			[]string{`INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('s-1', 'ws-1', 'u-1')`,
				`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES ('r-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 's-1', 1)`,
				`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'bad', 'r-1', 's-1', 1)`},
			ReasonDateCivilInvalid, 3,
		},
		{
			"sequence_duplicate",
			[]string{`INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('s-1', 'ws-1', 'u-1')`,
				`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES ('r-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, 1783252800, 'MONTHLY', 's-1', 1)`,
				`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES ('r-2', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, 1783252800, 'MONTHLY', 's-1', 1)`,
				`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-a', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, 1783252800, '2026-07-12', 'r-1', 's-1', 1)`,
				`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-b', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 2000, 1783252800, '2026-07-12', 'r-2', 's-1', 1)`},
			ReasonSequenceDuplicate, 3,
		},
		{
			"rule_not_found",
			[]string{`PRAGMA foreign_keys = OFF`,
				`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, recurrence_sequence) VALUES ('tx-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'fake-rule', 1)`,
				`PRAGMA foreign_keys = ON`},
			ReasonRuleNotFound, 3,
		},
	}
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := openAuditTestDB(t)
			seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
			for _, sql := range tc.setupSQL {
				mustExec(t, db, sql, noon)
			}
			report, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
			if exitCode != tc.wantExit {
				t.Fatalf("expected exit %d, got %d", tc.wantExit, exitCode)
			}
			found := false
			for _, f := range report.Findings {
				if f.ReasonCode == tc.wantReason {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s finding, got none", tc.wantReason)
			}
		})
	}
}

func TestCivilDateLegacyAudit_ExitCodes(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	mustExec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
	mustExec(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active)
			VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
	tests := []struct {
		name     string
		setup    string
		wantCode int
	}{
		{"valid", `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-v', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, '2026-07-12', 'rule-1', 'series-1', 1)`, 0},
		{"deterministic", `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-d', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'rule-1', 'series-1', 1)`, 2},
		{"human_review", `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, recurring_rule_id, series_id, recurrence_sequence) VALUES ('tx-h', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'not-a-date', 'rule-1', 'series-1', 1)`, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db2, _ := openAuditTestDB(t)
			seedUserAndWorkspace(t, db2, "u-1", "ws-1", "personal")
			mustExec(t, db2, `INSERT INTO recurring_series (id, workspace_id, user_id) VALUES ('series-1', 'ws-1', 'u-1')`)
			mustExec(t, db2, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES ('rule-1', 'ws-1', 'u-1', 'acc-ws-1', 'INCOME', 1000, ?, 'MONTHLY', 'series-1', 1)`, noon)
			mustExec(t, db2, tt.setup, noon)
			_, exitCode, _ := RunCivilDateLegacyAudit(context.Background(), db2, "text")
			if exitCode != tt.wantCode {
				t.Fatalf("expected exit %d, got %d", tt.wantCode, exitCode)
			}
		})
	}
}

func TestCivilDateLegacyAudit_InvalidFormat(t *testing.T) {
	exitCode := RunCivilDateLegacyAuditWithArgs(
		[]string{"--database-url", "file:/x/does/not/exist.db", "--format", "xml"},
		&bytes.Buffer{}, &bytes.Buffer{},
	)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for invalid format, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_NonexistentDB(t *testing.T) {
	exitCode := RunCivilDateLegacyAuditWithArgs(
		[]string{"--database-url", "file:/nonexistent/path/contabase.db"},
		&bytes.Buffer{}, &bytes.Buffer{},
	)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for nonexistent db, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_MissingDatabaseURL(t *testing.T) {
	exitCode := RunCivilDateLegacyAuditWithArgs(
		[]string{},
		&bytes.Buffer{}, &bytes.Buffer{},
	)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for missing url, got %d", exitCode)
	}
}

func TestCivilDateLegacyAudit_Help(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := RunCivilDateLegacyAuditWithArgs(
		[]string{"--help"},
		&stdout, &stderr,
	)
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for help, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "Exit codes:") {
		t.Fatalf("help missing exit codes section")
	}
}

func TestCivilDateLegacyAudit_DeterministicSorting(t *testing.T) {
	db, _ := openAuditTestDB(t)
	seedUserAndWorkspace(t, db, "u-1", "ws-1", "personal")
	seedUserAndWorkspace(t, db, "u-2", "ws-2", "personal")
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	for _, s := range []struct {
		ws, series, rule, tx, acc string
		seq                       int64
	}{
		{"ws-1", "series-a", "rule-a", "tx-a", "acc-ws-1", 1},
		{"ws-1", "series-a", "rule-a", "tx-b", "acc-ws-1", 2},
		{"ws-2", "series-z", "rule-z", "tx-z", "acc-ws-2", 1},
	} {
		mustExec(t, db, `INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id) VALUES (?, ?, 'u-1')`, s.series, s.ws)
		mustExec(t, db, `INSERT OR IGNORE INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, series_id, active) VALUES (?, ?, 'u-1', ?, 'INCOME', 1000, ?, 'MONTHLY', ?, 1)`, s.rule, s.ws, s.acc, noon, s.series)
		mustExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, recurring_rule_id, series_id, recurrence_sequence) VALUES (?, ?, 'u-1', ?, 'INCOME', 1000, ?, ?, ?, ?)`, s.tx, s.ws, s.acc, noon, s.rule, s.series, s.seq)
	}
	var buf1, buf2 bytes.Buffer
	r1, _, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	PrintAuditReportJSON(r1, &buf1)
	r2, _, _ := RunCivilDateLegacyAudit(context.Background(), db, "text")
	PrintAuditReportJSON(r2, &buf2)
	if buf1.String() != buf2.String() {
		t.Fatalf("deterministic sorting failed: outputs differ")
	}
}

func TestLegacyUnixToCivilDateUTC_Consistency(t *testing.T) {
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	civil := civildate.LegacyUnixToCivilDateUTC(noon)
	if civil.String() != "2026-07-12" {
		t.Fatalf("expected 2026-07-12, got %s", civil.String())
	}
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	civil2 := civildate.LegacyUnixToCivilDateUTC(midnight)
	if civil2.String() != "2026-07-12" {
		t.Fatalf("expected 2026-07-12, got %s", civil2.String())
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec failed: %v\nquery: %s", err, query)
	}
}
