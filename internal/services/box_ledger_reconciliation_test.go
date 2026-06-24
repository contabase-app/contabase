package services

import (
	"database/sql"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestReconcileWorkspaceBoxLedgerHealthyScenario(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedLedgerAuditWorkspace(t, db, "ws-a", now)

	insertLedgerAuditAccount(t, db, "acc-a", "ws-a", models.AccountTypeChecking, now)
	insertLedgerAuditCategory(t, db, "cat-a", "ws-a", "", now)
	insertLedgerAuditCategory(t, db, "cat-b", "ws-a", "", now)
	insertLedgerAuditBox(t, db, "box-a", "ws-a", "cat-a", now)
	insertLedgerAuditBox(t, db, "box-b", "ws-a", "cat-b", now)

	insertLedgerAuditExpenseTx(t, db, "tx-active", "ws-a", "acc-a", "cat-a", 1200, now)
	insertLedgerAuditExpenseTx(t, db, "tx-reversed", "ws-a", "acc-a", "cat-b", 800, now)

	insertLedgerAuditEvent(t, db, "ev-recharge-a", "box-a", 5000, "RECHARGE", "", "", now)
	insertLedgerAuditEvent(t, db, "ev-consume-active", "box-a", -1200, "CONSUME", "tx-active", "", now)
	insertLedgerAuditEvent(t, db, "ev-consume-reversed", "box-b", -800, "CONSUME", "tx-reversed", "", now)
	insertLedgerAuditEvent(t, db, "ev-reversal", "box-b", 800, "REVERSAL", "tx-reversed", "ev-consume-reversed", now)

	report, err := ReconcileWorkspaceBoxLedger(db, "ws-a")
	if err != nil {
		t.Fatalf("reconcile ledger: %v", err)
	}
	if report.HasIssues() {
		t.Fatalf("expected no issues, got %+v", report.Issues)
	}
	if got := report.BoxTotals["box-a"]; got != 3800 {
		t.Fatalf("box-a total = %d, want 3800", got)
	}
	if got := report.BoxTotals["box-b"]; got != 0 {
		t.Fatalf("box-b total = %d, want 0", got)
	}
	summary := findSourceSummary(report, "tx-active")
	if summary == nil {
		t.Fatalf("missing source summary for tx-active")
	}
	if summary.ActiveConsumes != 1 || summary.ConsumeEvents != 1 || summary.ReversalEvents != 0 {
		t.Fatalf("tx-active summary = %+v", *summary)
	}
}

func TestReconcileWorkspaceBoxLedgerDetectsInconsistencies(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedLedgerAuditWorkspace(t, db, "ws-a", now)

	insertLedgerAuditAccount(t, db, "acc-a", "ws-a", models.AccountTypeChecking, now)
	insertLedgerAuditCategory(t, db, "cat-a", "ws-a", "", now)
	insertLedgerAuditCategory(t, db, "cat-b", "ws-a", "", now)
	insertLedgerAuditBox(t, db, "box-a", "ws-a", "cat-a", now)
	insertLedgerAuditBox(t, db, "box-b", "ws-a", "cat-b", now)

	insertLedgerAuditExpenseTx(t, db, "tx-ok", "ws-a", "acc-a", "cat-a", 1000, now)
	insertLedgerAuditExpenseTx(t, db, "tx-wrong-box", "ws-a", "acc-a", "cat-a", 700, now)

	insertLedgerAuditEvent(t, db, "ev-consume-ok", "box-a", -1000, "CONSUME", "tx-ok", "", now)
	insertLedgerAuditEvent(t, db, "ev-consume-missing-tx", "box-a", -500, "CONSUME", "tx-missing", "", now)
	insertLedgerAuditEvent(t, db, "ev-consume-wrong-box", "box-b", -700, "CONSUME", "tx-wrong-box", "", now)
	insertLedgerAuditEvent(t, db, "ev-reversal-mismatch", "box-a", 400, "REVERSAL", "tx-ok", "ev-consume-ok", now)
	insertLedgerAuditEvent(t, db, "ev-reversal-missing-target", "box-a", 300, "REVERSAL", "tx-ok", "", now)

	report, err := ReconcileWorkspaceBoxLedger(db, "ws-a")
	if err != nil {
		t.Fatalf("reconcile ledger: %v", err)
	}
	if !report.HasIssues() {
		t.Fatalf("expected reconciliation issues")
	}

	expectIssueCode(t, report, "active_consume_missing_transaction")
	expectIssueCode(t, report, "active_consume_wrong_box")
	expectIssueCode(t, report, "reversal_amount_mismatch")
	expectIssueCode(t, report, "reversal_missing_target")
}

func TestListBoxLedgerBySourceTransactionWorkspaceScopedAndActiveFlag(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedLedgerAuditWorkspace(t, db, "ws-a", now)
	seedLedgerAuditWorkspace(t, db, "ws-b", now)

	insertLedgerAuditCategory(t, db, "cat-a", "ws-a", "", now)
	insertLedgerAuditCategory(t, db, "cat-b", "ws-b", "", now)
	insertLedgerAuditBox(t, db, "box-a", "ws-a", "cat-a", now)
	insertLedgerAuditBox(t, db, "box-b", "ws-b", "cat-b", now)

	insertLedgerAuditEvent(t, db, "ev-a1", "box-a", -1000, "CONSUME", "tx-1", "", now)
	insertLedgerAuditEvent(t, db, "ev-a2", "box-a", 1000, "REVERSAL", "tx-1", "ev-a1", now)
	insertLedgerAuditEvent(t, db, "ev-a3", "box-a", -500, "CONSUME", "tx-1", "", now)
	insertLedgerAuditEvent(t, db, "ev-b1", "box-b", -900, "CONSUME", "tx-1", "", now)

	events, err := ListBoxLedgerBySourceTransaction(db, "ws-a", "tx-1")
	if err != nil {
		t.Fatalf("list by source: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events count = %d, want 3", len(events))
	}

	activeCount := 0
	for _, e := range events {
		if e.BoxID != "box-a" {
			t.Fatalf("unexpected box in workspace-scoped list: %s", e.BoxID)
		}
		if e.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("active events count = %d, want 1", activeCount)
	}
}

func seedLedgerAuditWorkspace(t *testing.T, db *sql.DB, workspaceID string, now int64) {
	t.Helper()
	exec(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, workspaceID, workspaceID, now, now)
	exec(t, db, `INSERT OR IGNORE INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user', 'User', 'user@example.com', 'hash', ?, ?)`, now, now)
}

func insertLedgerAuditAccount(t *testing.T, db *sql.DB, accountID, workspaceID, accountType string, now int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, 0, ?, ?)
	`, accountID, workspaceID, accountID, accountType, now, now)
}

func insertLedgerAuditCategory(t *testing.T, db *sql.DB, categoryID, workspaceID, parentID string, now int64) {
	t.Helper()
	var parent interface{}
	if parentID != "" {
		parent = parentID
	}
	exec(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, parent_id, created_at)
		VALUES (?, ?, ?, 'EXPENSE', ?, ?)
	`, categoryID, workspaceID, categoryID, parent, now)
}

func insertLedgerAuditBox(t *testing.T, db *sql.DB, boxID, workspaceID, categoryID string, now int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, 0, ?, ?)
	`, boxID, workspaceID, boxID, categoryID, now, now)
}

func insertLedgerAuditExpenseTx(t *testing.T, db *sql.DB, txID, workspaceID, accountID, categoryID string, amount int64, now int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, 'user', ?, ?, 'EXPENSE', ?, ?, ?, 'pending', 1, 1, ?, ?)
	`, txID, workspaceID, accountID, categoryID, amount, now, txID, now, now)
}

func insertLedgerAuditEvent(t *testing.T, db *sql.DB, ledgerID, boxID string, amount int64, eventType, sourceTx, reversalOf string, now int64) {
	t.Helper()
	var source interface{}
	var reversal interface{}
	if sourceTx != "" {
		source = sourceTx
	}
	if reversalOf != "" {
		reversal = reversalOf
	}
	exec(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, source_transaction_id, reversal_of_ledger_id, reference_date, created_at)
		VALUES (?, ?, ?, ?, 'audit test', ?, ?, ?, ?)
	`, ledgerID, boxID, amount, eventType, source, reversal, now, now)
}

func findSourceSummary(report BoxLedgerReconciliationReport, sourceTx string) *BoxLedgerSourceSummary {
	for i := range report.SourceSummaries {
		if report.SourceSummaries[i].SourceTransactionID == sourceTx {
			return &report.SourceSummaries[i]
		}
	}
	return nil
}

func expectIssueCode(t *testing.T, report BoxLedgerReconciliationReport, code string) {
	t.Helper()
	for _, issue := range report.Issues {
		if issue.Code == code {
			return
		}
	}
	t.Fatalf("expected issue code %q in %+v", code, report.Issues)
}
