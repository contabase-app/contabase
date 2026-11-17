package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func projectionTestNow() time.Time {
	return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
}

func projectionTestSelection(year int, month time.Month) time.Time {
	return time.Date(year, month, 1, 12, 0, 0, 0, time.UTC)
}

func newProjectionFixture(t *testing.T, workspaceID string) (*sql.DB, f1Base) {
	t.Helper()
	db := openTestDB(t)
	base := seedF1Base(t, db, workspaceID)
	return db, base
}

func addProjectionPaidTx(t *testing.T, db *sql.DB, seed f1TxSeed, paidAt int64) {
	t.Helper()
	addF1Tx(t, db, seed)
	if paidAt != 0 {
		execTestSQL(t, db, `UPDATE transactions SET paid_at = ? WHERE workspace_id = ? AND id = ?`, paidAt, seed.WorkspaceID, seed.ID)
	}
}

func projectionMonth(t *testing.T, projection DashboardFinancialProjection, index int) FinancialProjectionMonth {
	t.Helper()
	if len(projection.Months) <= index {
		t.Fatalf("months = %d, want index %d", len(projection.Months), index)
	}
	return projection.Months[index]
}

func projectionCents(money MoneyDisplay) int64 {
	return money.rawCents()
}

func TestDashboardFinancialProjectionRealizedPendingAndEmptyStates(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("realized and pending", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-mixed")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 100000, Status: "paid", Description: "Receita", CreatedAt: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, CategoryID: base.category, Type: "EXPENSE", Amount: 40000, Status: "paid", Description: "Despesa", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-pending-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 30000, Status: "pending", Description: "Receber", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-pending-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, CategoryID: base.category, Type: "EXPENSE", Amount: 20000, Status: "pending", Description: "Pagar", DueDate: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if month.State != FinancialProjectionComplete {
			t.Fatalf("month state = %s, want complete: %+v", month.State, month.Reasons)
		}
		if got := projectionCents(month.Realized.OperationalIncome); got != 100000 {
			t.Fatalf("realized operational income = %d, want 100000", got)
		}
		if got := projectionCents(month.Realized.Expense); got != 40000 {
			t.Fatalf("realized expense = %d, want 40000", got)
		}
		if got := projectionCents(month.Realized.Net); got != 60000 {
			t.Fatalf("realized net = %d, want 60000", got)
		}
		if got := projectionCents(month.Pending.Income); got != 30000 {
			t.Fatalf("pending income = %d, want 30000", got)
		}
		if got := projectionCents(month.Pending.Expense); got != 20000 {
			t.Fatalf("pending expense = %d, want 20000", got)
		}
		if got := projectionCents(month.Projected.Net); got != 70000 {
			t.Fatalf("projected net = %d, want 70000", got)
		}
		if !month.Projected.Available {
			t.Fatal("projected result must be available")
		}
		if !projection.SnapshotConsistent || !projection.Recurrence.Covered {
			t.Fatalf("snapshot/recurrence = %v/%+v", projection.SnapshotConsistent, projection.Recurrence)
		}
		if got := projectionCents(projection.Undated.Expense); got != 0 || projection.Undated.HasRegistered {
			t.Fatalf("undated = %+v, want empty", projection.Undated)
		}
		if projection.Overdue.HasRegistered {
			t.Fatalf("overdue = %+v, want empty", projection.Overdue)
		}

		before := projectionTableCounts(t, db)
		if _, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now); err != nil {
			t.Fatalf("second build: %v", err)
		}
		after := projectionTableCounts(t, db)
		if before != after {
			t.Fatalf("projection changed persisted rows: before=%v after=%v", before, after)
		}
	})

	t.Run("partial receipt is represented by separate rows", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-partial-receipt")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-receipt-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 40000, Status: "paid", Description: "Recebido", CreatedAt: now.Unix()}, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-receipt-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 60000, Status: "pending", Description: "A receber", DueDate: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if projectionCents(month.Realized.OperationalIncome) != 40000 || projectionCents(month.Pending.Income) != 60000 {
			t.Fatalf("partial receipt rows = %+v/%+v", month.Realized, month.Pending)
		}
	})

	t.Run("only pending", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-pending")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "only-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 25000, Status: "pending", Description: "Pendente", DueDate: time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 1)
		if month.Realized.Count != 0 || projectionCents(month.Pending.Expense) != 25000 || !month.Projected.Available {
			t.Fatalf("December = %+v, want pending-only projection", month)
		}
	})

	t.Run("no movements", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-empty")
		defer db.Close()
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if month.Realized.Count != 0 || month.Pending.HasRegistered || month.Projected.Available {
			t.Fatalf("empty month = %+v, want empty without projected zero", month)
		}
		if month.State != FinancialProjectionPartial {
			t.Fatalf("empty month state = %s, want partial", month.State)
		}
	})
}

func TestDashboardFinancialProjectionInvoicePartialAndRefunds(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("partial invoice payment", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-invoice-partial")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-inv', ?, '2026-11', ?, ?, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "paid", Description: invoicePaymentDescription("Cartão"), CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix())
		addF1InvoicePayment(t, db, "proj-ip", base.ws, "proj-inv", base.checking, "proj-payment", "", 20000, 0, base.user, now.Unix())

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.ObligationsAvailable {
			t.Fatalf("obligations unavailable: %+v", projection.Reasons)
		}
		month := projectionMonth(t, projection, 0)
		if got := projectionCents(month.Realized.Expense); got != 20000 {
			t.Fatalf("paid invoice = %d, want 20000 cash only", got)
		}
		if got := projectionCents(month.Pending.Expense); got != 30000 {
			t.Fatalf("remaining invoice = %d, want 30000", got)
		}
		if month.Pending.Count != 1 || !month.Projected.Available {
			t.Fatalf("partial invoice projection = %+v", month)
		}
	})

	t.Run("full invoice payment", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-invoice-full")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-full-inv', ?, '2026-11', ?, ?, 'CLOSED', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-full-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-full-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-full-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 50000, Status: "paid", Description: invoicePaymentDescription("Cartão"), CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix())
		addF1InvoicePayment(t, db, "proj-full-ip", base.ws, "proj-full-inv", base.checking, "proj-full-payment", "", 50000, 0, base.user, now.Unix())
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if projectionCents(month.Realized.Expense) != 50000 || projectionCents(month.Pending.Expense) != 0 {
			t.Fatalf("full payment = %+v, want 50000 realized and no pending", month)
		}
	})

	t.Run("item and general refunds", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-refund")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-refund-inv', ?, '2026-11', ?, ?, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-refund-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-refund-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})
		addF1TransactionRefund(t, db, "proj-refund-item", base.ws, "proj-refund-purchase", 10000, 0, base.user, now.Unix())
		addF1InvoiceRefund(t, db, "proj-refund-general", base.ws, "proj-refund-inv", 5000, 0, base.user, now.Unix())

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if got := projectionCents(month.Pending.Expense); got != 35000 {
			t.Fatalf("refunded invoice = %d, want 35000", got)
		}
		if projectionCents(month.Realized.Expense) != 0 {
			t.Fatal("refunds must not become realized cash")
		}
	})
}

func TestDashboardFinancialProjectionReversalIsAdjustment(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)
	db, base := newProjectionFixture(t, "ws-proj-reversal")
	defer db.Close()
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-rev-inv', ?, '2026-11', ?, ?, 'CLOSED', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
	addF1Tx(t, db, f1TxSeed{ID: "proj-rev-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-rev-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})
	addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-rev-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 50000, Status: "paid", Description: invoicePaymentDescription("Cartão"), CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix())
	// A descrição não segue o padrão técnico de reversão. O vínculo
	// reversal_transaction_id + reversed_at é a única prova usada.
	addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-rev-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 50000, Status: "paid", Description: "Ajuste atípico de caixa", CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix())
	addF1InvoicePayment(t, db, "proj-rev-ip", base.ws, "proj-rev-inv", base.checking, "proj-rev-payment", "proj-rev-reversal", 50000, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix(), base.user, now.Unix())

	projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	month := projectionMonth(t, projection, 0)
	if got := projectionCents(month.Realized.OperationalIncome); got != 0 {
		t.Fatalf("operational income = %d, want 0", got)
	}
	if got := projectionCents(month.Realized.AdjustmentIncome); got != 50000 {
		t.Fatalf("adjustment income = %d, want 50000", got)
	}
	if got := projectionCents(month.Realized.Expense); got != 50000 {
		t.Fatalf("realized expense = %d, want 50000", got)
	}
	if got := projectionCents(month.Realized.Net); got != 0 {
		t.Fatalf("realized net = %d, want 0", got)
	}
	if got := projectionCents(month.Pending.Expense); got != 50000 {
		t.Fatalf("pending invoice after reversal = %d, want 50000", got)
	}
}

func TestDashboardFinancialProjectionOverdueUndatedAndPast(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("overdue is separate", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-overdue")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-overdue-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "pending", Description: "Atrasada", DueDate: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-overdue-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "pending", Description: "Atrasada", DueDate: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-overdue-inv', ?, '2026-10', ?, ?, 'OPEN', 0, ?)`, base.card, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-overdue-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-overdue-inv", Type: "EXPENSE", Amount: 15000, Status: "paid", Description: "Compra atrasada", CreatedAt: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC).Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.Overdue.Available || projection.Overdue.Count != 3 || projectionCents(projection.Overdue.Net) != -25000 {
			t.Fatalf("overdue = %+v, want separate income/expense/invoice net -25000", projection.Overdue)
		}
		month := projectionMonth(t, projection, 0)
		if projectionCents(month.Pending.Income) != 0 || projectionCents(month.Pending.Expense) != 0 {
			t.Fatalf("overdue leaked into November: %+v", month)
		}
	})

	t.Run("business date without due date remains undated", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-undated")
		defer db.Close()
		execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = ?`, base.ws)
		addF1Tx(t, db, f1TxSeed{ID: "proj-undated", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 7000, Status: "pending", Description: "Sem vencimento", CreatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.Undated.HasRegistered || projection.Undated.Count != 1 || projectionCents(projection.Undated.Expense) != 7000 {
			t.Fatalf("undated = %+v", projection.Undated)
		}
		month := projectionMonth(t, projection, 0)
		if month.Pending.Count != 0 || month.Pending.WithoutDueDateCount != 1 || month.Projected.Available {
			t.Fatalf("undated month = %+v, want no allocated projection", month)
		}
		if month.State != FinancialProjectionPartial {
			t.Fatalf("undated state = %s, want partial", month.State)
		}
	})

	t.Run("negative due date is undated", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-negative-due")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-negative-due", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 7000, Status: "pending", Description: "Vencimento inválido", DueDate: -100, CreatedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.Undated.HasRegistered || projection.Undated.Count != 1 || projectionCents(projection.Undated.Expense) != 7000 {
			t.Fatalf("negative due date undated = %+v, want count 1 and expense 7000", projection.Undated)
		}
		month := projectionMonth(t, projection, 0)
		if month.Pending.Count != 0 || month.Projected.Available || month.State != FinancialProjectionPartial {
			t.Fatalf("negative due date month = %+v, want no allocated projection and partial", month)
		}
	})

	t.Run("past month is realized only", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-past")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-past-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 12000, Status: "paid", Description: "Receita", CreatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix())
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, projectionTestSelection(2026, time.October), now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if !month.IsPast || month.Projected.Available || month.Pending.Available {
			t.Fatalf("past month = %+v, want realized only", month)
		}
		if projectionCents(month.Realized.OperationalIncome) != 12000 || projection.Overdue.Available {
			t.Fatalf("past realized/overdue = %+v/%+v", month.Realized, projection.Overdue)
		}
	})
}

func TestReliablePendingDate(t *testing.T) {
	validDate := time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC).Unix()
	cases := []struct {
		name          string
		workspaceType string
		dueDate       sql.NullInt64
		dateUnix      int64
		dateCivil     sql.NullString
		want          bool
	}{
		{name: "due date wins", workspaceType: "business", dueDate: sql.NullInt64{Int64: 123, Valid: true}, dateUnix: validDate, want: true},
		{name: "personal null due uses valid date", workspaceType: "personal", dateUnix: validDate, want: true},
		{name: "personal zero due uses valid date", workspaceType: "personal", dueDate: sql.NullInt64{Int64: 0, Valid: true}, dateUnix: validDate, want: true},
		{name: "personal negative due is invalid", workspaceType: "personal", dueDate: sql.NullInt64{Int64: -1, Valid: true}, dateUnix: validDate, want: false},
		{name: "business null due does not use date", workspaceType: "business", dateUnix: validDate, want: false},
		{name: "business zero due does not use date", workspaceType: "business", dueDate: sql.NullInt64{Int64: 0, Valid: true}, dateUnix: validDate, want: false},
		{name: "personal valid civil date", workspaceType: "personal", dateUnix: validDate, dateCivil: sql.NullString{String: "2026-12-05", Valid: true}, want: true},
		{name: "personal invalid civil date does not fall back", workspaceType: "personal", dateUnix: validDate, dateCivil: sql.NullString{String: "2026-02-30", Valid: true}, want: false},
		{name: "personal zero date is invalid", workspaceType: "personal", dateUnix: 0, want: false},
		{name: "personal valid civil with zero unix is invalid", workspaceType: "personal", dateUnix: 0, dateCivil: sql.NullString{String: "2026-12-05", Valid: true}, want: false},
		{name: "personal empty civil uses legacy date", workspaceType: "personal", dateUnix: validDate, dateCivil: sql.NullString{String: "", Valid: true}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reliablePendingDate(tc.workspaceType, tc.dueDate, tc.dateUnix, tc.dateCivil); got != tc.want {
				t.Fatalf("reliablePendingDate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDashboardFinancialProjectionPersonalTemporalEligibility(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("personal date-only income and expense are pending", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-date")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "pending", Description: "Receita prevista", CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 4000, Status: "pending", Description: "Despesa prevista", CreatedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC).Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if month.Pending.Count != 2 || projectionCents(month.Pending.Income) != 10000 || projectionCents(month.Pending.Expense) != 4000 {
			t.Fatalf("personal pending = %+v, want income 10000 and expense 4000", month.Pending)
		}
		if month.Pending.WithoutDueDateCount != 0 || projection.Undated.HasRegistered {
			t.Fatalf("personal dated items leaked into undated: pending=%+v undated=%+v", month.Pending, projection.Undated)
		}
		if !month.Projected.Available || projectionCents(month.Projected.Net) != 6000 || month.State != FinancialProjectionComplete {
			t.Fatalf("personal projection = %+v state=%s", month.Projected, month.State)
		}
	})

	t.Run("personal large income in December", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-large")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-large-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 2800000, Status: "pending", Description: "Retirada de Lucro", CreatedAt: time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC).Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, projectionTestSelection(2026, time.December), now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if projectionCents(month.Pending.Income) != 2800000 || month.Pending.WithoutDueDateCount != 0 || projection.Undated.HasRegistered {
			t.Fatalf("December personal income = %+v undated=%+v", month.Pending, projection.Undated)
		}
		if !month.Projected.Available || projectionCents(month.Projected.Net) != 2800000 {
			t.Fatalf("December projected = %+v", month.Projected)
		}
	})

	t.Run("personal recurring date-only occurrences cover December and January", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-recurring")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, active, created_at, updated_at) VALUES ('proj-personal-rule', ?, ?, ?, 'INCOME', 153499, 'Salário', ?, 'MONTHLY', 1, ?, ?)`, base.ws, base.user, base.checking, now.Unix(), now.Unix(), now.Unix())
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, active, created_at, updated_at) VALUES ('proj-personal-rule-expense', ?, ?, ?, 'EXPENSE', 45990, 'Aluguel', ?, 'MONTHLY', 1, ?, ?)`, base.ws, base.user, base.checking, now.Unix(), now.Unix(), now.Unix())
		if err := ensureRecurringMaterializationStateRow(db, base.ws, now.Unix()); err != nil {
			t.Fatalf("state row: %v", err)
		}
		execTestSQL(t, db, `UPDATE recurring_materialization_state SET materialized_until_civil='2027-03-01' WHERE workspace_id=?`, base.ws)
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-rec-dec", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 153499, Status: "pending", Description: "Salário dezembro", RecurringRuleID: "proj-personal-rule", RecurrenceSequence: 1, CreatedAt: time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC).Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-rec-jan", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 153499, Status: "pending", Description: "Salário janeiro", RecurringRuleID: "proj-personal-rule", RecurrenceSequence: 2, CreatedAt: time.Date(2027, 1, 5, 12, 0, 0, 0, time.UTC).Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-rec-exp-dec", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 45990, Status: "pending", Description: "Aluguel dezembro", RecurringRuleID: "proj-personal-rule-expense", RecurrenceSequence: 1, CreatedAt: time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC).Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-rec-exp-jan", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 45990, Status: "pending", Description: "Aluguel janeiro", RecurringRuleID: "proj-personal-rule-expense", RecurrenceSequence: 2, CreatedAt: time.Date(2027, 1, 10, 12, 0, 0, 0, time.UTC).Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, projectionTestSelection(2026, time.December), now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projectionCents(projection.Months[0].Pending.Income) != 153499 || projectionCents(projection.Months[1].Pending.Income) != 153499 {
			t.Fatalf("recurring pending = %+v/%+v", projection.Months[0].Pending, projection.Months[1].Pending)
		}
		if projectionCents(projection.Months[0].Pending.Expense) != 45990 || projectionCents(projection.Months[1].Pending.Expense) != 45990 {
			t.Fatalf("recurring expense = %+v/%+v", projection.Months[0].Pending, projection.Months[1].Pending)
		}
		if projection.Months[0].Pending.Count != 2 || projection.Months[1].Pending.Count != 2 {
			t.Fatalf("recurring count = %+v/%+v", projection.Months[0].Pending, projection.Months[1].Pending)
		}
		if !projection.Recurrence.Covered || projection.Recurrence.State != FinancialProjectionComplete {
			t.Fatalf("recurrence = %+v", projection.Recurrence)
		}
		if projection.Undated.HasRegistered {
			t.Fatalf("recurring dated items undated = %+v", projection.Undated)
		}
	})

	t.Run("undated residual hides projected numeric result", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-mixed-undated")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-valid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "pending", Description: "Receita válida", CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-no-date", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 5000, Status: "pending", Description: "Sem data", CreatedAt: 0})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		month := projectionMonth(t, projection, 0)
		if projectionCents(month.Pending.Income) != 10000 || !projection.Undated.HasRegistered {
			t.Fatalf("mixed pending/undated = %+v/%+v", month.Pending, projection.Undated)
		}
		if month.State != FinancialProjectionPartial || month.Projected.Available {
			t.Fatalf("mixed month must be partial without projected number: %+v", month)
		}
	})

	t.Run("personal invalid civil date remains undated", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-invalid-date")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-invalid-civil", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 5000, Status: "pending", Description: "Data inválida", CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()})
		execTestSQL(t, db, `UPDATE transactions SET date_civil='2026-02-30' WHERE id='proj-personal-invalid-civil'`)

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.Undated.HasRegistered || projection.Undated.Count != 1 || projectionCents(projection.Undated.Income) != 5000 {
			t.Fatalf("invalid civil date undated = %+v", projection.Undated)
		}
		month := projectionMonth(t, projection, 0)
		if month.Pending.Count != 0 || month.Projected.Available || month.State != FinancialProjectionPartial {
			t.Fatalf("invalid civil date month = %+v", month)
		}
	})

	t.Run("personal zero date remains undated", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-zero-date")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-zero-date", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 5000, Status: "pending", Description: "Data zero", CreatedAt: 0})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.Undated.HasRegistered || projection.Undated.Count != 1 {
			t.Fatalf("zero date undated = %+v", projection.Undated)
		}
	})

	t.Run("business uses due date not competence", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-business-due")
		defer db.Close()
		execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = ?`, base.ws)
		addF1Tx(t, db, f1TxSeed{ID: "proj-business-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "pending", Description: "Receita empresarial", DueDate: time.Date(2026, 12, 15, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projectionCents(projection.Months[0].Pending.Income) != 0 || projectionCents(projection.Months[1].Pending.Income) != 10000 {
			t.Fatalf("business pending = %+v/%+v", projection.Months[0].Pending, projection.Months[1].Pending)
		}
		if projection.Undated.HasRegistered {
			t.Fatalf("business due date undated = %+v", projection.Undated)
		}
	})

	t.Run("personal pending transfer is excluded", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-personal-transfer")
		defer db.Close()
		addF1Account(t, db, "proj-personal-transfer-dest", base.ws, "SAVINGS", 0, now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-personal-transfer", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: "proj-personal-transfer-dest", Type: "TRANSFER", Amount: 3000, Status: "pending", Description: "Transferência", CreatedAt: now.Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projection.Undated.HasRegistered || projection.Months[0].Pending.Count != 0 {
			t.Fatalf("transfer leaked: month=%+v undated=%+v", projection.Months[0].Pending, projection.Undated)
		}
	})
}

func TestDashboardFinancialProjectionYearBoundaryAndInstallments(t *testing.T) {
	now := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
	selected := projectionTestSelection(2026, time.December)
	db, base := newProjectionFixture(t, "ws-proj-year")
	defer db.Close()

	addF1Tx(t, db, f1TxSeed{ID: "proj-dec-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "pending", Description: "Dezembro", DueDate: time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "proj-jan-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "pending", Description: "Janeiro", DueDate: time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-inst-inv', ?, '2027-02', ?, ?, 'OPEN', 0, ?)`, base.card, time.Date(2027, 1, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2027, 2, 10, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
	addF1Tx(t, db, f1TxSeed{ID: "proj-installment-1", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-inst-inv", Type: "EXPENSE", Amount: 10000, Status: "paid", Description: "Parcela 1/3", InstallmentNumber: 1, TotalInstallments: 3, CreatedAt: now.Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "proj-installment-2", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-inst-inv", Type: "EXPENSE", Amount: 30000, Status: "paid", Description: "Parcela 2/3", InstallmentNumber: 2, TotalInstallments: 3, ParentID: "proj-installment-1", CreatedAt: now.Unix()})

	projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if projection.Months[0].Label != "Dezembro/2026" || projection.Months[1].Label != "Janeiro/2027" || projection.Months[2].Label != "Fevereiro/2027" {
		t.Fatalf("month labels = %q/%q/%q", projection.Months[0].Label, projection.Months[1].Label, projection.Months[2].Label)
	}
	if projectionCents(projection.Months[0].Pending.Income) != 10000 {
		t.Fatalf("December pending = %+v", projection.Months[0].Pending)
	}
	if projectionCents(projection.Months[1].Pending.Expense) != 20000 {
		t.Fatalf("January pending = %+v", projection.Months[1].Pending)
	}
	if projectionCents(projection.Months[2].Pending.Expense) != 40000 {
		t.Fatalf("February installment = %+v", projection.Months[2].Pending)
	}
}

func TestDashboardFinancialProjectionCoversRecurrenceAndIsolation(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("stale recurrence is partial", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-recurrence")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, active, created_at, updated_at) VALUES ('proj-rule', ?, ?, ?, 'EXPENSE', 1000, 'Recorrente', ?, 'MONTHLY', 1, ?, ?)`, base.ws, base.user, base.checking, now.Unix(), now.Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-recurrence-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 10000, Status: "pending", Description: "Pendente", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projection.Recurrence.State != FinancialProjectionPartial || projection.Recurrence.Covered {
			t.Fatalf("recurrence = %+v, want partial/stale", projection.Recurrence)
		}
		if projection.Months[0].State != FinancialProjectionPartial || !projection.Months[0].Projected.Available {
			t.Fatalf("month with stale recurrence = %+v", projection.Months[0])
		}
	})

	t.Run("covered recurrence is complete", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-recurrence-covered")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, active, created_at, updated_at) VALUES ('proj-rule-covered', ?, ?, ?, 'EXPENSE', 1000, 'Recorrente', ?, 'MONTHLY', 1, ?, ?)`, base.ws, base.user, base.checking, now.Unix(), now.Unix(), now.Unix())
		if err := ensureRecurringMaterializationStateRow(db, base.ws, now.Unix()); err != nil {
			t.Fatalf("state row: %v", err)
		}
		execTestSQL(t, db, `UPDATE recurring_materialization_state SET materialized_until_civil='2027-03-01', last_success_at=? WHERE workspace_id=?`, now.Unix(), base.ws)
		addF1Tx(t, db, f1TxSeed{ID: "proj-covered-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 10000, Status: "pending", Description: "Pendente", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.Recurrence.Covered || projection.Recurrence.State != FinancialProjectionComplete {
			t.Fatalf("recurrence = %+v, want covered/complete", projection.Recurrence)
		}
	})

	t.Run("workspace isolation", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		baseA := seedF1Base(t, db, "ws-proj-a")
		baseB := seedF1Base(t, db, "ws-proj-b")
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-a-income", WorkspaceID: baseA.ws, UserID: baseA.user, AccountID: baseA.checking, Type: "INCOME", Amount: 100000, Status: "paid", Description: "A", CreatedAt: now.Unix()}, now.Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-b-income", WorkspaceID: baseB.ws, UserID: baseB.user, AccountID: baseB.checking, Type: "INCOME", Amount: 900000, Status: "paid", Description: "B", CreatedAt: now.Unix()}, now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-a-pending", WorkspaceID: baseA.ws, UserID: baseA.user, AccountID: baseA.checking, Type: "EXPENSE", Amount: 10000, Status: "pending", Description: "A", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		addF1Tx(t, db, f1TxSeed{ID: "proj-b-pending", WorkspaceID: baseB.ws, UserID: baseB.user, AccountID: baseB.checking, Type: "EXPENSE", Amount: 90000, Status: "pending", Description: "B", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-b-inv', ?, '2026-11', ?, ?, 'OPEN', 0, ?)`, baseB.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-b-purchase", WorkspaceID: baseB.ws, UserID: baseB.user, AccountID: baseB.card, InvoiceID: "proj-b-inv", Type: "EXPENSE", Amount: 500000, Status: "paid", Description: "B", CreatedAt: now.Unix()})
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-b-payment", WorkspaceID: baseB.ws, UserID: baseB.user, AccountID: baseB.checking, Type: "EXPENSE", Amount: 100000, Status: "paid", Description: invoicePaymentDescription("Cartão B"), CreatedAt: now.Unix()}, now.Unix())
		addF1InvoicePayment(t, db, "proj-b-ip", baseB.ws, "proj-b-inv", baseB.checking, "proj-b-payment", "", 100000, 0, baseB.user, now.Unix())
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, active, created_at, updated_at) VALUES ('proj-b-rule', ?, ?, ?, 'EXPENSE', 1000, 'B', ?, 'MONTHLY', 1, ?, ?)`, baseB.ws, baseB.user, baseB.checking, now.Unix(), now.Unix(), now.Unix())
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, baseA.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projectionCents(projection.Months[0].Realized.OperationalIncome) != 100000 || projectionCents(projection.Months[0].Pending.Expense) != 10000 {
			t.Fatalf("workspace leak: %+v", projection.Months[0])
		}
		if projection.Recurrence.ActiveRecurring || !projection.Recurrence.Covered {
			t.Fatalf("workspace recurrence leak: %+v", projection.Recurrence)
		}
	})
}

func TestDashboardFinancialProjectionInvalidCompositionAndExcludedRows(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("card expense without invoice blocks projection", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-invalid")
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "proj-card-no-invoice", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, Type: "EXPENSE", Amount: 5000, Status: "pending", Description: "Compra sem fatura", CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projection.ObligationsAvailable {
			t.Fatal("invalid composition must mark obligations unavailable")
		}
		month := projectionMonth(t, projection, 0)
		if month.State != FinancialProjectionUnavailable || month.Pending.Available || month.Projected.Available {
			t.Fatalf("invalid month = %+v", month)
		}
	})

	t.Run("invalid invoice composition blocks projection", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-invalid-invoice")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-invalid-inv', ?, '2026-11', ?, ?, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-invalid-item", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-invalid-inv", Type: "INCOME", Amount: 5000, Status: "paid", Description: "Item inválido", CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projection.ObligationsAvailable || projection.Months[0].State != FinancialProjectionUnavailable {
			t.Fatalf("invalid invoice = %+v/%+v", projection.ObligationsAvailable, projection.Months[0])
		}
	})

	t.Run("classification only is excluded", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-classification")
		defer db.Close()
		execTestSQL(t, db, `DROP TRIGGER IF EXISTS trg_c2b_transaction_insert`)
		addF1Tx(t, db, f1TxSeed{ID: "proj-classification", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 50000, Status: "paid", Description: "Resultado de investimento", EffectMode: "CLASSIFICATION_ONLY", CreatedAt: now.Unix()})
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projectionCents(projection.Months[0].Realized.OperationalIncome) != 0 {
			t.Fatalf("classification-only leaked into operational income: %+v", projection.Months[0].Realized)
		}
	})

	t.Run("transfer is excluded", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-transfer")
		defer db.Close()
		addF1Account(t, db, "proj-savings", base.ws, "SAVINGS", 0, now.Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-transfer", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, DestinationAccountID: "proj-savings", Type: "TRANSFER", Amount: 30000, Status: "paid", Description: "Transferência", CreatedAt: now.Unix()}, now.Unix())
		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if projectionCents(projection.Months[0].Realized.Net) != 0 || projection.Months[0].Realized.Count != 0 {
			t.Fatalf("transfer leaked into result: %+v", projection.Months[0].Realized)
		}
	})
}

func TestDashboardFinancialProjectionOverflowAndErrors(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("overflow is an error", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-overflow")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-max-1", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: math.MaxInt64, Status: "paid", Description: "Max", CreatedAt: now.Unix()}, now.Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-max-2", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 1, Status: "paid", Description: "Um", CreatedAt: now.Unix()}, now.Unix())
		if _, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now); err == nil {
			t.Fatal("overflow must return an error, not zero")
		}
	})

	t.Run("query failure is an error", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-query-failure")
		defer db.Close()
		execTestSQL(t, db, `DROP TABLE invoice_payments`)
		if _, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now); err == nil {
			t.Fatal("query failure must return an error, not a zero projection")
		}
	})

	t.Run("closed db and canceled context are errors", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-error")
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now); err == nil {
			t.Fatal("closed db must return an error")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := BuildDashboardFinancialProjection(ctx, openTestDB(t), base.ws, selected, now); err == nil {
			t.Fatal("canceled context must return an error")
		}
	})
}

func TestDashboardFinancialProjectionUsesOneReadTransactionSnapshot(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)
	path := filepath.Join(t.TempDir(), "projection-snapshot.db")
	dbs := openLockingRuntimeDBsAt(t, path, "")
	base := seedF1Base(t, dbs.DB, "ws-proj-snapshot")
	addF1Tx(t, dbs.DB, f1TxSeed{ID: "snapshot-1", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 10000, Status: "pending", Description: "Primeiro", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})
	period := ReportPeriod{Start: selected.Unix(), End: selected.AddDate(0, 3, 0).Unix() - 1}

	ctx := context.Background()
	tx, err := dbs.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer tx.Rollback()

	_, before, err := buildPrevisaoCashFlowData(tx, base.ws, period)
	if err != nil {
		t.Fatalf("first snapshot read: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("first snapshot pending = %d, want 1", len(before))
	}

	addF1Tx(t, dbs.Immediate, f1TxSeed{ID: "snapshot-2", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "pending", Description: "Segundo", DueDate: time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})

	_, after, err := buildPrevisaoCashFlowData(tx, base.ws, period)
	if err != nil {
		t.Fatalf("second snapshot read: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("snapshot changed after concurrent write: before=%d after=%d", len(before), len(after))
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback read tx: %v", err)
	}

	_, fresh, err := buildPrevisaoCashFlowData(dbs.DB, base.ws, period)
	if err != nil {
		t.Fatalf("fresh read: %v", err)
	}
	if len(fresh) != 2 {
		t.Fatalf("fresh read pending = %d, want 2 after commit", len(fresh))
	}
}

func TestDashboardFinancialProjectionInvoiceWithoutDueDate(t *testing.T) {
	now := projectionTestNow()
	selected := projectionTestSelection(2026, time.November)

	t.Run("zero due date is not allocated", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-invoice-zero-due")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-zero-inv', ?, '2026-11', ?, 0, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-zero-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-zero-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra sem vencimento", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.ObligationsAvailable {
			t.Fatalf("obligations unavailable: %+v", projection.Reasons)
		}
		if projection.ObligationsWithoutDateCount != 1 {
			t.Fatalf("without-date count = %d, want 1", projection.ObligationsWithoutDateCount)
		}
		month := projectionMonth(t, projection, 0)
		if month.State != FinancialProjectionPartial {
			t.Fatalf("month state = %s, want partial", month.State)
		}
		if month.Pending.Count != 0 || projectionCents(month.Pending.Expense) != 0 || month.Projected.Available {
			t.Fatalf("invoice due 0 leaked into month: %+v", month.Pending)
		}
		if projection.Undated.HasRegistered || projection.Undated.Count != 0 {
			t.Fatalf("invoice due 0 must stay outside Undated amount: %+v", projection.Undated)
		}
		if !projection.Overdue.Available || projection.Overdue.Count != 0 || projection.Overdue.WithoutDueDateCount != 1 {
			t.Fatalf("invoice due 0 overdue = %+v, want only without-date count", projection.Overdue)
		}
	})

	t.Run("negative due date is not allocated", func(t *testing.T) {
		db, base := newProjectionFixture(t, "ws-proj-invoice-negative-due")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('proj-negative-inv', ?, '2026-11', ?, -100, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "proj-negative-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "proj-negative-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra vencimento inválido", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})

		projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, selected, now)
		if err != nil {
			t.Fatalf("build projection: %v", err)
		}
		if !projection.ObligationsAvailable || projection.ObligationsWithoutDateCount != 1 {
			t.Fatalf("negative invoice obligations = %v/%d", projection.ObligationsAvailable, projection.ObligationsWithoutDateCount)
		}
		month := projectionMonth(t, projection, 0)
		if month.State != FinancialProjectionPartial || month.Pending.Count != 0 || month.Projected.Available {
			t.Fatalf("negative invoice month = %+v, want no allocation and partial", month)
		}
		if projection.Overdue.HasRegistered || projection.Undated.HasRegistered {
			t.Fatalf("negative invoice leaked: overdue=%+v undated=%+v", projection.Overdue, projection.Undated)
		}
	})

	t.Run("null due date is not representable", func(t *testing.T) {
		db, _ := newProjectionFixture(t, "ws-proj-invoice-null-due")
		defer db.Close()
		var notNull int
		if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('invoices') WHERE name = 'due_date'`).Scan(&notNull); err != nil {
			t.Fatalf("read invoices.due_date schema: %v", err)
		}
		if notNull != 1 {
			t.Fatalf("invoices.due_date notnull = %d, want 1 (NULL is not representable)", notNull)
		}
	})
}

func TestDashboardFinancialProjectionPastMonthKeepsHistoricalRealizedWhenF1Unavailable(t *testing.T) {
	now := projectionTestNow()
	db, base := newProjectionFixture(t, "ws-proj-past-f1")
	defer db.Close()

	addProjectionPaidTx(t, db, f1TxSeed{ID: "proj-past-f1-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 12000, Status: "paid", Description: "Receita histórica", CreatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix())
	addF1Tx(t, db, f1TxSeed{ID: "proj-past-f1-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, Type: "EXPENSE", Amount: 5000, Status: "pending", Description: "Cartão sem fatura", CreatedAt: now.Unix()})

	projection, err := BuildDashboardFinancialProjection(context.Background(), db, base.ws, projectionTestSelection(2026, time.October), now)
	if err != nil {
		t.Fatalf("build projection: %v", err)
	}
	if projection.ObligationsAvailable {
		t.Fatal("F1 must be unavailable for card expense without invoice")
	}
	past := projectionMonth(t, projection, 0)
	if !past.IsPast || past.State != FinancialProjectionComplete {
		t.Fatalf("past month = %+v, want historical complete", past)
	}
	if past.Pending.Available || past.Projected.Available {
		t.Fatalf("past month must not project: %+v", past)
	}
	if projectionCents(past.Realized.OperationalIncome) != 12000 {
		t.Fatalf("past realized = %+v, want 12000", past.Realized)
	}
	if projection.Months[1].State != FinancialProjectionUnavailable || projection.Months[2].State != FinancialProjectionUnavailable {
		t.Fatalf("future states = %s/%s, want unavailable", projection.Months[1].State, projection.Months[2].State)
	}
	if projection.Overdue.Available {
		t.Fatalf("past month must not expose overdue projection: %+v", projection.Overdue)
	}
}

func projectionTableCounts(t *testing.T, db *sql.DB) string {
	t.Helper()
	var transactions, invoices, payments, refunds, recurrence int64
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions`).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoices`).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments`).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_refunds`).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_materialization_state`).Scan(&recurrence); err != nil {
		t.Fatal(err)
	}
	return fmtProjectionCounts(transactions, invoices, payments, refunds, recurrence)
}

func fmtProjectionCounts(transactions, invoices, payments, refunds, recurrence int64) string {
	return fmt.Sprintf("%d/%d/%d/%d/%d", transactions, invoices, payments, refunds, recurrence)
}
