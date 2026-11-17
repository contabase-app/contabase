package handlers

import (
	"context"
	"testing"
	"time"
)

func TestDashboardCashFlowBucketsByCivilDateAcrossYearBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "Asia/Tokyo")
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-civil', 'Civil', 'civil@example.com', 'hash', ?, ?)`, now.Unix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-civil', 'Civil', 'personal', ?, ?)`, now.Unix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-civil', 'ws-civil', 'Conta', 'CHECKING', 0, 0, ?, ?)`, now.Unix(), now.Unix())
	// 2027-01-01 00:30 in Asia/Tokyo == 2026-12-31 15:30 UTC.
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, date_civil, paid_at, paid_date_civil, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-civil-boundary', 'ws-civil', 'u-civil', 'acc-civil', 'INCOME', 10000, ?, '2027-01-01', ?, '2027-01-01', 'Virada civil', 'paid', 1, 1, ?, ?)`,
		time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 12, 31, 15, 30, 0, 0, time.UTC).Unix(), now.Unix(), now.Unix())

	cash := buildPreviewCashFlow(db, "ws-civil", now, dashboardCanonicalBasePath, "6m", "")
	if !cash.Available || !cash.HasData {
		t.Fatalf("cash flow unavailable: %+v", cash)
	}
	var december, january int64
	for _, month := range cash.Months {
		switch month.Key {
		case "2026-12":
			december = month.Income.rawCents()
		case "2027-01":
			january = month.Income.rawCents()
		}
	}
	if december != 0 {
		t.Fatalf("civil January event leaked into December: %d", december)
	}
	if january != 10000 {
		t.Fatalf("civil January income = %d, want 10000", january)
	}
	if got := cash.TotalIncome.rawCents(); got != 10000 {
		t.Fatalf("cash flow total income = %d, want 10000", got)
	}
}

func TestDashboardProjectionBucketsRealizedByCivilDateAcrossYearBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "Asia/Tokyo")
	db := openTestDB(t)
	defer db.Close()

	now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-proj-civil', 'Projection', 'projection@example.com', 'hash', ?, ?)`, now.Unix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-proj-civil', 'Projection', 'personal', ?, ?)`, now.Unix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-proj-civil', 'ws-proj-civil', 'Conta', 'CHECKING', 0, 0, ?, ?)`, now.Unix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, date_civil, paid_at, paid_date_civil, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-proj-civil', 'ws-proj-civil', 'u-proj-civil', 'acc-proj-civil', 'INCOME', 10000, ?, '2027-01-01', ?, '2027-01-01', 'Virada civil', 'paid', 1, 1, ?, ?)`,
		time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 12, 31, 15, 30, 0, 0, time.UTC).Unix(), now.Unix(), now.Unix())

	projection, err := BuildDashboardFinancialProjection(context.Background(), db, "ws-proj-civil", time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC), now)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if len(projection.Months) != 3 {
		t.Fatalf("projection months = %d, want 3", len(projection.Months))
	}
	if got := projection.Months[0].Realized.OperationalIncome.rawCents(); got != 10000 {
		t.Fatalf("January realized income = %d, want 10000", got)
	}
	if got := projection.Months[0].Realized.Count; got != 1 {
		t.Fatalf("January realized count = %d, want 1", got)
	}
}

// The overdue bucket and the first projected month must be mutually exclusive
// by civil date. With extreme offsets the legacy instant can sit on the other
// side of the UTC month boundary, so an instant comparison alone would either
// duplicate or lose the event.
func TestDashboardProjectionOverdueAndFirstMonthNeverOverlapByCivilDate(t *testing.T) {
	t.Run("civil first day with instant before month start stays in month one", func(t *testing.T) {
		t.Setenv("CONTABASE_TIMEZONE", "Pacific/Kiritimati") // UTC+14
		now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-proj-kiritimati")
		defer db.Close()

		// 2027-01-01 00:30 +14 == 2026-12-31 10:30 UTC.
		addF1Tx(t, db, f1TxSeed{
			ID: "tx-proj-kiritimati", WorkspaceID: base.ws, UserID: base.user,
			AccountID: base.checking, Type: "EXPENSE", Amount: 12000, Status: "pending",
			Description: "Virada civil +14", CreatedAt: now.Unix(),
			DueDate: time.Date(2026, 12, 31, 10, 30, 0, 0, time.UTC).Unix(),
		})
		execTestSQL(t, db,
			`UPDATE transactions SET due_date_civil = '2027-01-01' WHERE id = 'tx-proj-kiritimati' AND workspace_id = ?`,
			base.ws)

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws,
			time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC), now)
		if err != nil {
			t.Fatalf("projection: %v", err)
		}
		if got := projectionCents(projection.Months[0].Pending.Expense); got != 12000 {
			t.Fatalf("first month pending = %d, want 12000", got)
		}
		if got := projectionCents(projection.Overdue.Expense); got != 0 {
			t.Fatalf("overdue expense = %d, want 0 (event is civil January)", got)
		}
	})

	t.Run("civil last day with instant after month start stays overdue", func(t *testing.T) {
		t.Setenv("CONTABASE_TIMEZONE", "Pacific/Midway") // UTC-11
		now := time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-proj-midway")
		defer db.Close()

		// 2026-12-31 23:00 -11 == 2027-01-01 10:00 UTC.
		addF1Tx(t, db, f1TxSeed{
			ID: "tx-proj-midway", WorkspaceID: base.ws, UserID: base.user,
			AccountID: base.checking, Type: "EXPENSE", Amount: 34000, Status: "pending",
			Description: "Virada civil -11", CreatedAt: now.Unix(),
			DueDate: time.Date(2027, 1, 1, 10, 0, 0, 0, time.UTC).Unix(),
		})
		execTestSQL(t, db,
			`UPDATE transactions SET due_date_civil = '2026-12-31' WHERE id = 'tx-proj-midway' AND workspace_id = ?`,
			base.ws)

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws,
			time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC), now)
		if err != nil {
			t.Fatalf("projection: %v", err)
		}
		if got := projectionCents(projection.Overdue.Expense); got != 34000 {
			t.Fatalf("overdue expense = %d, want 34000", got)
		}
		for i, month := range projection.Months {
			if got := projectionCents(month.Pending.Expense); got != 0 {
				t.Fatalf("month %d pending = %d, want 0 (event is civil December)", i, got)
			}
		}
	})
}
