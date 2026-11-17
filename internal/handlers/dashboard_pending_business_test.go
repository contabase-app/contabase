package handlers

import (
	"testing"
	"time"
)

func TestDashboardPendingBusinessRequiresDueDateAndPersonalKeepsFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	nowUnix := now.Unix()
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-biz', 'Biz', 'biz@example.com', 'hash', ?, ?)`, []any{nowUnix, nowUnix}},
		{`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-biz', 'Business', 'business', ?, ?)`, []any{nowUnix, nowUnix}},
		{`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-pending-biz-personal', 'Personal', 'personal', ?, ?)`, []any{nowUnix, nowUnix}},
		{`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-other', 'Other', 'personal', ?, ?)`, []any{nowUnix, nowUnix}},
		{`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-biz', 'ws-biz', 'Business account', 'CHECKING', 0, 0, ?, ?)`, []any{nowUnix, nowUnix}},
		{`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-biz-personal', 'ws-pending-biz-personal', 'Personal account', 'CHECKING', 0, 0, ?, ?)`, []any{nowUnix, nowUnix}},
		{`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-biz-other', 'ws-other', 'Other account', 'CHECKING', 0, 0, ?, ?)`, []any{nowUnix, nowUnix}},
	} {
		execTestSQL(t, db, stmt.query, stmt.args...)
	}

	insertDashboardPendingTx(t, db, "biz-overdue", "ws-biz", "user-biz", "acc-biz", "", "EXPENSE", 10000, "2026-07-01", "2026-08-05", "Biz overdue", "pending")
	insertDashboardPendingTx(t, db, "biz-due", "ws-biz", "user-biz", "acc-biz", "", "EXPENSE", 30000, "2026-08-01", "2026-08-12", "Biz due", "pending")
	insertDashboardPendingTx(t, db, "biz-no-due", "ws-biz", "user-biz", "acc-biz", "", "EXPENSE", 20000, "2026-08-10", "", "Biz no due", "pending")
	insertDashboardPendingTx(t, db, "biz-outside", "ws-biz", "user-biz", "acc-biz", "", "EXPENSE", 40000, "2026-08-10", "2026-08-18", "Biz outside", "pending")
	insertDashboardPendingTx(t, db, "biz-income-due", "ws-biz", "user-biz", "acc-biz", "", "INCOME", 40000, "2026-08-10", "2026-08-13", "Biz income due", "pending")
	insertDashboardPendingTx(t, db, "biz-income-no-due", "ws-biz", "user-biz", "acc-biz", "", "INCOME", 50000, "2026-08-10", "", "Biz income no due", "pending")
	insertDashboardPendingTx(t, db, "personal-no-due", "ws-pending-biz-personal", "user-biz", "acc-biz-personal", "", "EXPENSE", 60000, "2026-08-12", "", "Personal no due", "pending")
	insertDashboardPendingTx(t, db, "other-workspace", "ws-other", "user-biz", "acc-biz-other", "", "EXPENSE", 70000, "2026-08-10", "2026-08-12", "Other workspace", "pending")

	payables := QueryDashboardPendingPayables7d(db, "ws-biz", now)
	if len(payables) != 2 {
		t.Fatalf("business payables len = %d, want 2; items = %#v", len(payables), payables)
	}
	if payables[0].Description != "Biz overdue" || payables[1].Description != "Biz due" {
		t.Fatalf("business payables = %#v, want overdue then due", payables)
	}
	if !payables[0].IsOverdue {
		t.Fatalf("first business payable must be overdue: %#v", payables[0])
	}
	for _, item := range payables {
		if item.Description == "Biz no due" || item.Description == "Other workspace" {
			t.Fatalf("business payables leaked invalid item: %#v", item)
		}
	}
	payableTotal, payableCount := queryDashboardPayable7dTotal(db, "ws-biz", now)
	assertMoneyDisplay(t, "business payable total", payableTotal, "400", ",00")
	if payableCount != 2 {
		t.Fatalf("business payable count = %d, want 2", payableCount)
	}
	if raw := queryDashboardPendingWindowRawTotal(db, "ws-biz", "EXPENSE", now); raw != 40000 {
		t.Fatalf("business raw payable total = %d, want 40000", raw)
	}

	receivables := QueryDashboardPendingReceivables7d(db, "ws-biz", now)
	if len(receivables) != 1 || receivables[0].Description != "Biz income due" {
		t.Fatalf("business receivables = %#v, want only Biz income due", receivables)
	}
	receivableTotal, receivableCount := queryDashboardReceivable7dTotal(db, "ws-biz", now)
	assertMoneyDisplay(t, "business receivable total", receivableTotal, "400", ",00")
	if receivableCount != 1 {
		t.Fatalf("business receivable count = %d, want 1", receivableCount)
	}

	personal := QueryDashboardPendingPayables7d(db, "ws-pending-biz-personal", now)
	if len(personal) != 1 || personal[0].Description != "Personal no due" {
		t.Fatalf("personal fallback = %#v, want Personal no due", personal)
	}
	personalTotal, personalCount := queryDashboardPayable7dTotal(db, "ws-pending-biz-personal", now)
	assertMoneyDisplay(t, "personal fallback total", personalTotal, "600", ",00")
	if personalCount != 1 {
		t.Fatalf("personal fallback count = %d, want 1", personalCount)
	}
}
