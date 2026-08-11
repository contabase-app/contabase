package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func execTestDB(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func dateMidnight(t *testing.T, date string) int64 {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC).Unix()
}

func dateNoon(t *testing.T, date string) int64 {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatalf("parse date: %v", err)
	}
	return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 12, 0, 0, 0, time.UTC).Unix()
}

func openWriteThenReadOnly(t *testing.T, dbPath string, seed func(*sql.DB)) (*sql.DB, string) {
	t.Helper()
	wDB, absPath := openCivilDateBackfillTestDB(t, dbPath)
	seedCivilDateBackfillFixture(t, wDB)

	execTestDB(t, wDB, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat-c1i2', 'ws-backfill', 'Exp', 'EXPENSE', '#000', 'tag')`)

	seed(wDB)
	wDB.Close()

	rDB, _, err := OpenExistingForMaintenanceReadOnly("file:" + absPath)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	return rDB, absPath
}

func openCleanWriteThenReadOnly(t *testing.T, dbPath string, seed func(*sql.DB)) (*sql.DB, string) {
	t.Helper()
	wDB, absPath := openCivilDateBackfillTestDB(t, dbPath)
	seed(wDB)
	wDB.Close()

	rDB, _, err := OpenExistingForMaintenanceReadOnly("file:" + absPath)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	return rDB, absPath
}

func TestCivilDateRepairAuditNilDB(t *testing.T) {
	_, err := RunCivilDateRepairAudit(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil db")
	}
}

func TestCivilDateRepairAuditReadOnly(t *testing.T) {
	db, absPath := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-ro.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-ro', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-ro', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-ro', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			})
	defer db.Close()

	snapBefore := sha256Hash(t, absPath)

	report, err := RunCivilDateRepairAudit(context.Background(), db)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if report == nil {
		t.Fatal("report is nil")
	}

	if sha256Hash(t, absPath) != snapBefore {
		t.Fatal("database modified during read-only audit")
	}
}

func TestCivilDateRepairAuditZeroWrites(t *testing.T) {
	db, absPath := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-zwrite.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-zw', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-zw', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-zw', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
	defer db.Close()

	snap1 := sha256Hash(t, absPath)

	_, _ = RunCivilDateRepairAudit(context.Background(), db)
	if sha256Hash(t, absPath) != snap1 {
		t.Fatal("db changed after first audit")
	}
	_, _ = RunCivilDateRepairAudit(context.Background(), db)
	if sha256Hash(t, absPath) != snap1 {
		t.Fatal("db changed after second audit")
	}
}

func TestCivilDateRepairAuditAutoRepairDetection(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-auto.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-auto', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			// Regra com start_date definido, sem start_date_civil → AUTO_REPAIR
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-auto', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-auto', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			// Transação com date definido, sem date_civil → AUTO_REPAIR
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-auto', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-auto', 1, 's-auto', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
	defer db.Close()

	report, err := RunCivilDateRepairAudit(context.Background(), db)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	foundDate := false
	foundStart := false
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonDateCivilMissing && f.RepairOutcome == CivilRepairOutcomeAutoRepair {
			foundDate = true
		}
		if f.ReasonCode == ReasonStartDateCivilMissing && f.RepairOutcome == CivilRepairOutcomeAutoRepair {
			foundStart = true
		}
	}
	if !foundDate {
		t.Fatal("date_civil missing → AUTO_REPAIR not found")
	}
	if !foundStart {
		t.Fatal("start_date_civil missing → AUTO_REPAIR not found")
	}
}

func TestCivilDateRepairAuditScansRules(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-rules.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-rules', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-rules', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, '2026-06-01', 'MONTHLY', 'PENDING', 1, 's-rules', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
	defer db.Close()

	report, err := RunCivilDateRepairAudit(context.Background(), db)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if report.Summary.RulesScanned == 0 {
		t.Fatal("expected at least one recurring_rule scanned")
	}
}

func TestCivilDateRepairAuditNoSensitiveData(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-sensitive.db",
		func(db *sql.DB) {})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	for _, f := range report.Findings {
		if strings.Contains(f.ProposedCivilValue, "@") ||
			strings.Contains(f.CachedUTC, "@") ||
			strings.Contains(f.WorkspaceID, "@") {
			t.Fatalf("sensitive data in finding: %+v", f)
		}
	}
}

func TestCivilDateRepairAuditDeterministicOrdering(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-order.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-ord', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-ord', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-ord', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-ord', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-ord', 1, 's-ord', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
	defer db.Close()

	r1, _ := RunCivilDateRepairAudit(context.Background(), db)
	r2, _ := RunCivilDateRepairAudit(context.Background(), db)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("finding counts differ: %d vs %d", len(r1.Findings), len(r2.Findings))
	}
	for i := range r1.Findings {
		a, b := r1.Findings[i], r2.Findings[i]
		if a.AuditedField+a.SourceRowID != b.AuditedField+b.SourceRowID {
			t.Fatalf("ordering differs at %d: %q/%q vs %q/%q", i, a.AuditedField, a.SourceRowID, b.AuditedField, b.SourceRowID)
		}
	}
}

func TestCivilDateRepairAuditJSONOutput(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-json.db",
		func(db *sql.DB) {})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	var buf strings.Builder
	PrintCivilDateRepairReportJSON(report, &buf)
	output := buf.String()
	if !strings.Contains(output, `"schema_version"`) {
		t.Fatal("JSON missing schema_version")
	}
	if !strings.Contains(output, `"timezone"`) {
		t.Fatal("JSON missing timezone")
	}
	if !strings.Contains(output, `"findings"`) {
		t.Fatal("JSON missing findings")
	}
}

func TestCivilDateRepairAuditTextOutput(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-text.db",
		func(db *sql.DB) {})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	var buf strings.Builder
	PrintCivilDateRepairReportText(report, &buf)
	output := buf.String()
	if !strings.Contains(output, "AUDITORIA DE DATAS CIVIS") {
		t.Fatal("text output missing header")
	}
	if !strings.Contains(output, "Timezone:") {
		t.Fatal("text output missing timezone")
	}
}

func TestCivilDateRepairAuditPaidDateHumanReviewOnly(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-paid.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-paid', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-paid', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, '2026-06-01', 'MONTHLY', 'PENDING', 1, 's-paid', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			// paid com paid_at mas sem paid_date_civil
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, paid_at, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-paid', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, ?, '2026-06-01', 'test', 'paid', ?, 'r-paid', 1, 's-paid', ?, ?)`, dateNoon(t, "2026-06-01"), dateNoon(t, "2026-06-01"), now, now)
		})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	for _, f := range report.Findings {
		if f.AuditedField == "paid_date_civil" {
			if f.RepairOutcome != CivilRepairOutcomeHumanReview {
				t.Fatalf("paid_date_civil must be HUMAN_REVIEW, got %q", f.RepairOutcome)
			}
			if f.ReasonCode != ReasonPaidDateUnknown {
				t.Fatalf("reason = %q, want HUMAN_REVIEW_PAID_DATE", f.ReasonCode)
			}
			return
		}
	}
	t.Fatal("expected paid_date_civil finding")
}

func TestCivilDateRepairAuditStartDateAutoRepair(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-start.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-start', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			// Regra com start_date mas sem start_date_civil
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-start', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-start', 1, ?, ?)`, dateNoon(t, "2026-01-01"), now, now)
		})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	for _, f := range report.Findings {
		if f.AuditedField == "start_date_civil" && f.ReasonCode == ReasonStartDateCivilMissing {
			if f.RepairOutcome != CivilRepairOutcomeAutoRepair {
				t.Fatalf("start_date_civil missing should be AUTO_REPAIR, got %q", f.RepairOutcome)
			}
			return
		}
	}
	t.Fatal("expected START_DATE_CIVIL_MISSING finding")
}

func TestCivilDateRepairAuditCLI(t *testing.T) {
	t.Run("audit_help", func(t *testing.T) {
		var buf, errBuf strings.Builder
		exit := RunCivilDateRepairAuditWithArgs([]string{"--help"}, &buf, &errBuf)
		if exit != 0 {
			t.Fatalf("exit = %d, want 0", exit)
		}
		if !strings.Contains(buf.String(), "civil-date-repair audit") {
			t.Fatal("help should mention subcommand syntax")
		}
	})
	t.Run("missing_database_url", func(t *testing.T) {
		var buf, errBuf strings.Builder
		exit := RunCivilDateRepairAuditWithArgs([]string{}, &buf, &errBuf)
		if exit == 0 {
			t.Fatal("expected non-zero exit for missing --database-url")
		}
	})
	t.Run("invalid_format", func(t *testing.T) {
		var buf, errBuf strings.Builder
		exit := RunCivilDateRepairAuditWithArgs([]string{"--format", "xml", "--database-url", "file:/tmp/nonexistent.db"}, &buf, &errBuf)
		if exit == 0 {
			t.Fatal("expected non-zero exit for invalid format")
		}
	})
	t.Run("unknown_flag", func(t *testing.T) {
		var buf, errBuf strings.Builder
		exit := RunCivilDateRepairAuditWithArgs([]string{"--unknown-flag", "--database-url", "file:/tmp/x.db"}, &buf, &errBuf)
		if exit == 0 {
			t.Fatal("expected non-zero exit for unknown flag")
		}
	})
	t.Run("extra_positional_token", func(t *testing.T) {
		var buf, errBuf strings.Builder
		exit := RunCivilDateRepairAuditWithArgs([]string{"extra-token", "--database-url", "file:/tmp/x.db"}, &buf, &errBuf)
		if exit == 0 {
			t.Fatal("expected non-zero exit for unknown argument")
		}
	})
	t.Run("valid_args_after_audit", func(t *testing.T) {
		var buf, errBuf strings.Builder
		exit := RunCivilDateRepairAuditWithArgs([]string{"--format", "json", "--database-url", "file:/tmp/x.db"}, &buf, &errBuf)
		// Should fail with "nonexistent db" but not with parse error
		if exit == 0 {
			t.Fatal("expected non-zero exit (nonexistent db)")
		}
	})
}

func TestCivilDateRepairAuditTimezoneAmbiguousOutcome(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-tz-ambiguous.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-tz', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-tz', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, '2026-06-05', 'MONTHLY', 'PENDING', 1, 's-tz', 1, ?, ?)`, dateNoon(t, "2026-06-05"), now, now)
			// Midnight UTC → America/Sao_Paulo diverges (day before)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-tz', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, ?, NULL, 'test', 'pending', 'r-tz', 1, 's-tz', ?, ?)`, dateMidnight(t, "2026-06-05"), now, now)
		})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	for _, f := range report.Findings {
		if f.AuditedField == "date_civil" {
			if f.ReasonCode != ReasonTimezoneAmbiguous {
				t.Fatalf("reason = %q, want HUMAN_REVIEW_TIMEZONE_AMBIGUOUS", f.ReasonCode)
			}
			if f.RepairOutcome != CivilRepairOutcomeHumanReview {
				t.Fatalf("repair_outcome = %q, want HUMAN_REVIEW", f.RepairOutcome)
			}
			if f.ProposedCivilValue != "" {
				t.Fatalf("proposed = %q, want empty (no AUTO_REPAIR proposal)", f.ProposedCivilValue)
			}
			return
		}
	}
	t.Fatal("expected finding for ambiguous timezone")
}

func TestCivilDateRepairAuditCrossWorkspaceRule(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-xws-rule.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-xws', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			// Rule from another workspace (ws-backfill-2)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-xws', 'ws-backfill-2', 'user-backfill-2', 'account-backfill-2', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, '2026-06-05', 'MONTHLY', 'PENDING', 1, 's-xws', 1, ?, ?)`, dateNoon(t, "2026-06-05"), now, now)
			// Transaction references cross-workspace rule
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-xws', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-xws', 1, 's-xws', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
	defer db.Close()

	report, _ := RunCivilDateRepairAudit(context.Background(), db)
	for _, f := range report.Findings {
		if f.ReasonCode == ReasonCrossWorkspaceReference {
			if f.RepairOutcome != CivilRepairOutcomeHumanReview {
				t.Fatalf("cross-workspace should be HUMAN_REVIEW, got %q", f.RepairOutcome)
			}
			return
		}
	}
	t.Fatal("expected CROSS_WORKSPACE_REFERENCE finding")
}

func TestCivilDateRepairAuditPaginationMoreThanOnePage(t *testing.T) {
	db, _ := openWriteThenReadOnly(t,
		t.TempDir()+"/repair-paginate.db",
		func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-pag', 'ws-backfill', 'user-backfill', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-pag', 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, 'test', ?, '2026-06-01', 'MONTHLY', 'PENDING', 1, 's-pag', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			for i := 0; i < int(repairPageSize+10); i++ {
				id := fmt.Sprintf("tx-pag-%05d", i)
				execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES (?, 'ws-backfill', 'user-backfill', 'account-backfill', 'cat-c1i2', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-pag', ?, 's-pag', ?, ?)`, id, dateNoon(t, "2026-06-01"), int64(i+1), now, now)
			}
		})
	defer db.Close()

	r1, _ := RunCivilDateRepairAudit(context.Background(), db)
	r2, _ := RunCivilDateRepairAudit(context.Background(), db)

	if r1.Summary.TransactionsScanned != r2.Summary.TransactionsScanned {
		t.Fatalf("transaction count differs: %d vs %d", r1.Summary.TransactionsScanned, r2.Summary.TransactionsScanned)
	}
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("finding count differs: %d vs %d", len(r1.Findings), len(r2.Findings))
	}
	for i := range r1.Findings {
		a, b := r1.Findings[i], r2.Findings[i]
		if a.SourceRowID != b.SourceRowID || a.AuditedField != b.AuditedField {
			t.Fatalf("ordering differs at %d: %q/%q vs %q/%q", i, a.SourceRowID, a.AuditedField, b.SourceRowID, b.AuditedField)
		}
	}
}

func TestCivilDateRepairAuditOutcomePrecedence(t *testing.T) {
	t.Run("none_clean", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/prec-clean.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-cl', 'u-cl', 'u-cl@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws-cl', 'ws-cl', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-cl', 'u-cl', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-cl', 'ws-cl', 'Checking', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat-cl', 'ws-cl', 'Exp', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-cl', 'ws-cl', 'u-cl', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-cl', 'ws-cl', 'u-cl', 'acc-cl', 'cat-cl', 'EXPENSE', 1000, 'test', ?, '2026-06-01', ?, '2026-06-01', 'MONTHLY', 'PENDING', 1, 's-cl', 1, ?, ?)`,
				dateNoon(t, "2026-06-01"), dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		if report.Summary.Findings > 0 {
			t.Fatalf("expected 0 findings, got %d (auto=%d human=%d)", report.Summary.Findings, report.Summary.AutoRepair, report.Summary.HumanReview)
		}
	})

	t.Run("auto_repair_only_exit_2", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/prec-auto.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-ar', 'u-ar', 'u-ar@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws-ar', 'ws-ar', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-ar', 'u-ar', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-ar', 'ws-ar', 'Checking', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat-ar', 'ws-ar', 'Exp', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-ar', 'ws-ar', 'u-ar', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-ar', 'ws-ar', 'u-ar', 'acc-ar', 'cat-ar', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-ar', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		if report.Summary.AutoRepair == 0 {
			t.Fatalf("expected AUTO_REPAIR, got auto=%d human=%d", report.Summary.AutoRepair, report.Summary.HumanReview)
		}
		if report.Summary.HumanReview != 0 {
			t.Fatalf("expected 0 HUMAN_REVIEW, got %d", report.Summary.HumanReview)
		}
	})

	t.Run("human_review_only_exit_3", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/prec-hr.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-hr', 'u-hr', 'u-hr@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws-hr', 'ws-hr', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-hr', 'u-hr', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-hr', 'ws-hr', 'Checking', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat-hr', 'ws-hr', 'Exp', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-hr', 'ws-hr', 'u-hr', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-hr', 'ws-hr', 'u-hr', 'acc-hr', 'cat-hr', 'EXPENSE', 1000, 'test', ?, '2026-06-01', 'MONTHLY', 'PENDING', 1, 's-hr', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-hr', 'ws-hr', 'u-hr', 'acc-hr', 'cat-hr', 'EXPENSE', 1000, ?, '2026-02-30', 'test', 'pending', 'r-hr', 1, 's-hr', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		if report.Summary.AutoRepair != 0 {
			t.Fatalf("expected 0 AUTO_REPAIR, got %d", report.Summary.AutoRepair)
		}
		if report.Summary.HumanReview == 0 {
			t.Fatalf("expected HUMAN_REVIEW, got auto=%d human=%d", report.Summary.AutoRepair, report.Summary.HumanReview)
		}
	})

	t.Run("mixed_exit_3", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/prec-mix.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-mx', 'u-mx', 'u-mx@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws-mx', 'ws-mx', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-mx', 'u-mx', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-mx', 'ws-mx', 'Checking', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat-mx', 'ws-mx', 'Exp', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-mx', 'ws-mx', 'u-mx', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-mx', 'ws-mx', 'u-mx', 'acc-mx', 'cat-mx', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-mx', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-mx', 'ws-mx', 'u-mx', 'acc-mx', 'cat-mx', 'EXPENSE', 1000, ?, '2026-02-30', 'test', 'pending', 'r-mx', 1, 's-mx', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		if report.Summary.AutoRepair == 0 {
			t.Fatalf("expected AUTO_REPAIR, got auto=%d human=%d", report.Summary.AutoRepair, report.Summary.HumanReview)
		}
		if report.Summary.HumanReview == 0 {
			t.Fatalf("expected HUMAN_REVIEW, got auto=%d human=%d", report.Summary.AutoRepair, report.Summary.HumanReview)
		}
	})
}

func TestCivilDateRepairAuditRollbackRobustness(t *testing.T) {
	t.Run("normal_close", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/rollback-normal.db", func(db *sql.DB) {})
		defer db.Close()
		report, err := RunCivilDateRepairAudit(context.Background(), db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if report == nil {
			t.Fatal("report is nil")
		}
	})

	t.Run("primary_error_preserved", func(t *testing.T) {
		_, err := RunCivilDateRepairAudit(context.Background(), nil)
		if err == nil {
			t.Fatal("expected primary error for nil db")
		}
		if !strings.Contains(err.Error(), "database connection is nil") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("err_tx_done_ignored_after_success", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/rollback-errtxdone.db", func(db *sql.DB) {})
		defer db.Close()

		var txAfterRollback *sql.Tx
		origHook := testRepairRollbackHook
		testRepairRollbackHook = func(tx *sql.Tx) error {
			err := tx.Rollback()
			txAfterRollback = tx
			return err
		}
		defer func() { testRepairRollbackHook = origHook }()

		report, err := RunCivilDateRepairAudit(context.Background(), db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if report == nil {
			t.Fatal("report is nil")
		}
		if txAfterRollback == nil {
			t.Fatal("hook was not called")
		}
		secondErr := txAfterRollback.Rollback()
		if !errors.Is(secondErr, sql.ErrTxDone) {
			t.Fatalf("second rollback should be ErrTxDone, got %v", secondErr)
		}
	})

	t.Run("rollback_error_without_primary", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/rollback-err.db", func(db *sql.DB) {})
		defer db.Close()

		origHook := testRepairRollbackHook
		testRepairRollbackHook = func(tx *sql.Tx) error {
			_ = tx.Rollback()
			return fmt.Errorf("forced rollback failure")
		}
		defer func() { testRepairRollbackHook = origHook }()

		_, err := RunCivilDateRepairAudit(context.Background(), db)
		if err == nil {
			t.Fatal("expected rollback error")
		}
		if !strings.Contains(err.Error(), "forced rollback failure") {
			t.Fatalf("unexpected error: %v", err)
		}

	t.Run("primary_and_rollback_both_fail", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/rollback-both.db", func(db *sql.DB) {})
		defer db.Close()

		origPrimary := testRepairPrimaryErrHook
		testRepairPrimaryErrHook = func() error {
			return fmt.Errorf("forced primary scan error")
		}
		defer func() { testRepairPrimaryErrHook = origPrimary }()

		origRollback := testRepairRollbackHook
		testRepairRollbackHook = func(tx *sql.Tx) error {
			_ = tx.Rollback()
			return fmt.Errorf("forced rollback error simultaneous")
		}
		defer func() { testRepairRollbackHook = origRollback }()

		_, err := RunCivilDateRepairAudit(context.Background(), db)
		if err == nil {
			t.Fatal("expected combined error")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "forced primary scan error") {
			t.Fatalf("error missing primary: %v", err)
		}
		if !strings.Contains(errStr, "forced rollback error simultaneous") {
			t.Fatalf("error missing rollback: %v", err)
		}
	})
	})
}

func TestCivilDateRepairAuditConcurrentSnapshotIsolation(t *testing.T) {
	dbPath := t.TempDir() + "/snapshot-iso.db"
	wDB, absPath := openCivilDateBackfillTestDB(t, dbPath)
	now := time.Now().Unix()
	execTestDB(t, wDB, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-snap', 'u-snap', 'u@e.com', 'hash')`)
	execTestDB(t, wDB, `INSERT INTO workspaces (id, name, type) VALUES ('ws-snap', 'WS', 'personal')`)
	execTestDB(t, wDB, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-snap', 'u-snap', 'ADMIN')`)
	execTestDB(t, wDB, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-snap', 'ws-snap', 'C', 'CHECKING')`)
	execTestDB(t, wDB, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat-snap', 'ws-snap', 'Exp', 'EXPENSE', '#000', 'tag')`)
	execTestDB(t, wDB, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-snap', 'ws-snap', 'u-snap', 1, ?, ?)`, now, now)
	execTestDB(t, wDB, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, start_date_civil, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-snap', 'ws-snap', 'u-snap', 'acc-snap', 'cat-snap', 'EXPENSE', 1000, 'test', ?, '2026-06-01', 'MONTHLY', 'PENDING', 1, 's-snap', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
	for i := 0; i < int(repairPageSize+50); i++ {
		id := fmt.Sprintf("tx-snap-%05d", i)
		execTestDB(t, wDB, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES (?, 'ws-snap', 'u-snap', 'acc-snap', 'cat-snap', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-snap', ?, 's-snap', ?, ?)`, id, dateNoon(t, "2026-06-01"), int64(i+1), now, now)
	}
	wDB.Close()

	rDB, _, err := OpenExistingForMaintenanceReadOnly("file:" + absPath)
	if err != nil {
		t.Fatalf("open ro: %v", err)
	}
	defer rDB.Close()

	page1Done := make(chan struct{})
	externalModDone := make(chan struct{})
	hookFired := false
	auditDone := make(chan struct{})
	auditCtx, auditCancel := context.WithCancel(context.Background())
	defer auditCancel()

	origHook := testRepairPaginationHook
	testRepairPaginationHook = func() {
		if !hookFired {
			hookFired = true
			close(page1Done)
			select {
			case <-externalModDone:
			case <-time.After(10 * time.Second):
			}
		}
	}
	defer func() { testRepairPaginationHook = origHook }()

	var snapReport *CivilDateRepairReport
	var snapErr error
	go func() {
		defer close(auditDone)
		snapReport, snapErr = RunCivilDateRepairAudit(auditCtx, rDB)
	}()

	select {
	case <-page1Done:
	case <-time.After(10 * time.Second):
		auditCancel()
		select {
		case <-externalModDone:
		default:
			close(externalModDone)
		}
		select {
		case <-auditDone:
		case <-time.After(5 * time.Second):
		}
		t.Fatal("timeout waiting for first page")
	}

	wDB2, _, err := OpenExistingForMaintenanceReadWrite("file:" + absPath)
	if err != nil {
		auditCancel()
		select {
		case <-externalModDone:
		default:
			close(externalModDone)
		}
		select {
		case <-auditDone:
		case <-time.After(5 * time.Second):
		}
		t.Fatalf("open rw for external write: %v", err)
	}
	execTestDB(t, wDB2, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-snap-external', 'ws-snap', 'u-snap', 'acc-snap', 'cat-snap', 'EXPENSE', 2000, ?, 'external', 'pending', 'r-snap', ?, 's-snap', ?, ?)`, dateNoon(t, "2026-06-01"), int64(repairPageSize+100), now, now)
	wDB2.Close()
	close(externalModDone)

	select {
	case <-auditDone:
	case <-time.After(10 * time.Second):
		auditCancel()
		select {
		case <-auditDone:
		case <-time.After(5 * time.Second):
		}
		t.Fatal("timeout waiting for audit to complete")
	}

	if snapErr != nil {
		t.Fatalf("snapshot audit: %v", snapErr)
	}
	if snapReport == nil {
		t.Fatal("report is nil")
	}

	for _, f := range snapReport.Findings {
		if f.SourceRowID == "tx-snap-external" {
			t.Fatal("snapshot audit saw external insert (snapshot isolation broken)")
		}
	}

	secondReport, err := RunCivilDateRepairAudit(context.Background(), rDB)
	if err != nil {
		t.Fatalf("second audit: %v", err)
	}
	foundExternal := false
	for _, f := range secondReport.Findings {
		if f.SourceRowID == "tx-snap-external" {
			foundExternal = true
			break
		}
	}
	if !foundExternal {
		t.Fatal("second audit did not see external insert")
	}
}

func TestCivilDateRepairAuditStructuralRegression(t *testing.T) {
	t.Run("series_inexistente", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/struct-no-series.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-ns', 'u-ns', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-ns', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, effective_start_sequence, created_at, updated_at) VALUES ('r-ns', 'ws1', 'u-ns', 'acc1', 'cat1', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `PRAGMA foreign_keys = OFF`)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-noseries', 'ws1', 'u-ns', 'acc1', 'cat1', 'EXPENSE', 1000, ?, '2026-06-01', 'test', 'pending', 'r-ns', 1, 'nonexistent-series', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `PRAGMA foreign_keys = ON`)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonSeriesNotFound && f.RepairOutcome == CivilRepairOutcomeHumanReview {
				return
			}
		}
		t.Fatal("expected SERIES_NOT_FOUND with HUMAN_REVIEW")
	})

	t.Run("rule_series_id_divergent", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/struct-divergent.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-dv', 'u-dv', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-dv', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-dv-rule', 'ws1', 'u-dv', 1, ?, ?), ('s-dv-tx', 'ws1', 'u-dv', 1, ?, ?)`, now, now, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-dv', 'ws1', 'u-dv', 'acc1', 'cat1', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-dv-rule', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-dv', 'ws1', 'u-dv', 'acc1', 'cat1', 'EXPENSE', 1000, ?, '2026-06-01', 'test', 'pending', 'r-dv', 1, 's-dv-tx', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonSeriesNotFound && f.RepairOutcome == CivilRepairOutcomeHumanReview {
				return
			}
		}
		t.Fatal("expected SERIES_NOT_FOUND for divergent rule.series_id vs tx.series_id")
	})

	t.Run("transaction_nao_recorrente", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/struct-nonrecur.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-nr', 'u-nr', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-nr', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, created_at, updated_at) VALUES ('tx-nonrecur', 'ws1', 'u-nr', 'acc1', 'cat1', 'EXPENSE', 1000, ?, '2026-06-01', 'test', 'pending', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonRuleNotFound || f.ReasonCode == ReasonSeriesNotFound {
				t.Fatalf("non-recurring transaction should not have structural findings: %s", f.ReasonCode)
			}
		}
	})
}

func sha256Hash(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func TestCivilDateRepairAuditCrossWorkspaceAllScenarios(t *testing.T) {
	t.Run("rule_other_workspace", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/xws-rule-other.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-rwx', 'u-rwx', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal'), ('ws2', 'WS2', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-rwx', 'ADMIN'), ('ws2', 'u-rwx', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING'), ('acc2', 'ws2', 'C2', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag'), ('cat2', 'ws2', 'Cat2', 'EXPENSE', '#111', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s1', 'ws1', 'u-rwx', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-ws2', 'ws2', 'u-rwx', 'acc2', 'cat2', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's1', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-rws2', 'ws1', 'u-rwx', 'acc1', 'cat1', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-ws2', 1, 's1', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonCrossWorkspaceReference {
				return
			}
		}
		t.Fatal("expected CROSS_WORKSPACE_REFERENCE for rule from other workspace")
	})

	t.Run("series_other_workspace", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/xws-series-other.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-sxw', 'u-sxw', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal'), ('ws2', 'WS2', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-sxw', 'ADMIN'), ('ws2', 'u-sxw', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING'), ('acc2', 'ws2', 'C2', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag'), ('cat2', 'ws2', 'Cat2', 'EXPENSE', '#111', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-ws2', 'ws2', 'u-sxw', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-in-ws1', 'ws1', 'u-sxw', 'acc1', 'cat1', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-ws2', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-sws2', 'ws1', 'u-sxw', 'acc1', 'cat1', 'EXPENSE', 1000, ?, 'test', 'pending', 'r-in-ws1', 1, 's-ws2', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonCrossWorkspaceReference || f.ReasonCode == ReasonSeriesNotFound {
				return
			}
		}
		t.Fatal("expected CROSS_WORKSPACE_REFERENCE or SERIES_NOT_FOUND for series from other workspace")
	})

	t.Run("rule_not_found", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/xws-no-rule.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-nr', 'u-nr', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-nr', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-nr', 'ws1', 'u-nr', 1, ?, ?)`, now, now)
			execTestDB(t, db, `PRAGMA foreign_keys = OFF`)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-norule', 'ws1', 'u-nr', 'acc1', 'cat1', 'EXPENSE', 1000, ?, '2026-06-01', 'test', 'pending', 'nonexistent-rule', 1, 's-nr', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `PRAGMA foreign_keys = ON`)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonRuleNotFound {
				if f.RepairOutcome != CivilRepairOutcomeHumanReview {
					t.Fatal("RULE_NOT_FOUND must be HUMAN_REVIEW")
				}
				return
			}
		}
		t.Fatal("expected RULE_NOT_FOUND")
	})

	t.Run("valid_same_workspace", func(t *testing.T) {
		db, _ := openCleanWriteThenReadOnly(t, t.TempDir()+"/xws-valid.db", func(db *sql.DB) {
			now := time.Now().Unix()
			execTestDB(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES ('u-vld', 'u-vld', 'u@e.com', 'hash')`)
			execTestDB(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('ws1', 'WS1', 'personal')`)
			execTestDB(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'u-vld', 'ADMIN')`)
			execTestDB(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc1', 'ws1', 'C1', 'CHECKING')`)
			execTestDB(t, db, `INSERT INTO categories (id, workspace_id, name, type, color, icon) VALUES ('cat1', 'ws1', 'Cat1', 'EXPENSE', '#000', 'tag')`)
			execTestDB(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('s-vld', 'ws1', 'u-vld', 1, ?, ?)`, now, now)
			execTestDB(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, effective_start_sequence, created_at, updated_at) VALUES ('r-vld', 'ws1', 'u-vld', 'acc1', 'cat1', 'EXPENSE', 1000, 'test', ?, 'MONTHLY', 'PENDING', 1, 's-vld', 1, ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
			execTestDB(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, date_civil, description, status, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at) VALUES ('tx-vld', 'ws1', 'u-vld', 'acc1', 'cat1', 'EXPENSE', 1000, ?, '2026-06-01', 'test', 'pending', 'r-vld', 1, 's-vld', ?, ?)`, dateNoon(t, "2026-06-01"), now, now)
		})
		defer db.Close()
		report, _ := RunCivilDateRepairAudit(context.Background(), db)
		for _, f := range report.Findings {
			if f.ReasonCode == ReasonCrossWorkspaceReference || f.ReasonCode == ReasonRuleNotFound || f.ReasonCode == ReasonSeriesNotFound {
				t.Fatalf("unexpected structural finding: %s", f.ReasonCode)
			}
		}
	})
}
