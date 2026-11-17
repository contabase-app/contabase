package handlers

import (
	"context"
	"database/sql"
	"math/big"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

func TestObligationScheduleBuckets(t *testing.T) {
	today := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	schedule := newF1ObligationSchedule(today)
	add := func(offsetDays int, amount int64) {
		schedule.add(today.AddDate(0, 0, offsetDays).Add(12*time.Hour).Unix(), big.NewInt(amount))
	}
	add(-1, 100)
	add(0, 100)
	add(30, 100)
	add(31, 100)
	add(60, 100)
	add(61, 100)
	schedule.add(0, big.NewInt(100))

	if got := schedule.due30.Int64(); got != 300 {
		t.Fatalf("due30 = %d, want 300", got)
	}
	if got := schedule.due60.Int64(); got != 500 {
		t.Fatalf("due60 = %d, want 500", got)
	}
	if got := schedule.beyond60.Int64(); got != 100 {
		t.Fatalf("beyond60 = %d, want 100", got)
	}
	if got := schedule.withoutDate.Int64(); got != 100 || schedule.withoutDateCount != 1 {
		t.Fatalf("without date = %d count=%d, want 100/1", got, schedule.withoutDateCount)
	}
	if _, _, _, ok := schedule.finish(big.NewInt(700)); !ok {
		t.Fatal("schedule did not reconcile with U")
	}
	if _, _, _, ok := schedule.finish(big.NewInt(699)); ok {
		t.Fatal("schedule reconciled with a divergent U")
	}
}

func TestObligationScheduleUsesConfiguredCivilDay(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	now := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	today := AppCivilDateUTC(now)
	if got := today.Format("2006-01-02"); got != "2026-11-04" {
		t.Fatalf("civil day = %s, want 2026-11-04", got)
	}
	if got := f1ObligationDueDate(0, sql.NullString{String: "2026-11-05", Valid: true}); got != time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("persisted civil date = %d, want noon UTC of 2026-11-05", got)
	}
	if got := f1ObligationDueDate(now.Unix(), sql.NullString{}); got != time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("legacy timestamp civil date = %d, want noon UTC of 2026-11-04", got)
	}
	schedule := newF1ObligationSchedule(today)
	schedule.add(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix(), big.NewInt(100))
	schedule.add(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC).Unix(), big.NewInt(100))
	if got := schedule.due30.Int64(); got != 200 {
		t.Fatalf("civil-day buckets = %d, want 200", got)
	}
}

func TestObligationExpenseDueDatePersonalFallback(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	civil := func(value string) sql.NullString {
		return sql.NullString{String: value, Valid: true}
	}
	noonUTC := func(year int, month time.Month, day int) int64 {
		return time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Unix()
	}

	t.Run("explicit due civil precedes date civil", func(t *testing.T) {
		got := f1ObligationExpenseDueDate(models.WorkspaceTypePersonal, f1ExpenseRow{
			DueDateCivil: civil("2026-12-01"),
			DateCivil:    civil("2026-11-01"),
			Date:         time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC).Unix(),
		})
		if want := noonUTC(2026, time.December, 1); got != want {
			t.Fatalf("due = %d, want explicit due civil %d", got, want)
		}
	})

	t.Run("date civil precedes legacy date", func(t *testing.T) {
		got := f1ObligationExpenseDueDate(models.WorkspaceTypePersonal, f1ExpenseRow{
			DateCivil: civil("2026-11-02"),
			Date:      time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC).Unix(),
		})
		if want := noonUTC(2026, time.November, 2); got != want {
			t.Fatalf("due = %d, want date civil %d", got, want)
		}
	})

	t.Run("legacy date uses configured civil day", func(t *testing.T) {
		legacy := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC).Unix()
		got := f1ObligationExpenseDueDate(models.WorkspaceTypePersonal, f1ExpenseRow{Date: legacy})
		if want := noonUTC(2026, time.November, 4); got != want {
			t.Fatalf("due = %d, want civil day %d", got, want)
		}
	})

	t.Run("business ignores date fallback", func(t *testing.T) {
		got := f1ObligationExpenseDueDate(models.WorkspaceTypeBusiness, f1ExpenseRow{
			DateCivil: civil("2026-11-02"),
			Date:      time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC).Unix(),
		})
		if got != 0 {
			t.Fatalf("business due = %d, want no fallback", got)
		}
	})
}

func loadF1WindowObligations(t *testing.T, db *sql.DB, workspaceID string) services.WorkspaceObligationsF1 {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := loadWorkspaceObligationsF1(context.Background(), tx, workspaceID)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestObligationWindowLoaderReconciliation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-win-reconcile")
	today := AppCivilDateUTC(time.Now())
	due := func(days int) int64 {
		return today.AddDate(0, 0, days).Add(12 * time.Hour).Unix()
	}

	addF1Invoice(t, db, "inv-win-open", base.card, "2026-01", "OPEN", 0, base.now)
	execTestSQL(t, db, `UPDATE invoices SET due_date = ? WHERE id = 'inv-win-open'`, due(5))
	addF1Tx(t, db, f1TxSeed{ID: "purchase-win-open", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-win-open", Type: "EXPENSE", Amount: 1000, Status: "paid", CreatedAt: base.now})
	addF1TransactionRefund(t, db, "refund-win-open", base.ws, "purchase-win-open", 100, 0, base.user, base.now)
	addF1InvoiceRefund(t, db, "general-refund-win-open", base.ws, "inv-win-open", 100, 0, base.user, base.now)

	addF1Invoice(t, db, "inv-win-closed", base.card, "2026-02", "CLOSED", 0, base.now)
	execTestSQL(t, db, `UPDATE invoices SET due_date = ? WHERE id = 'inv-win-closed'`, due(45))
	addF1Tx(t, db, f1TxSeed{ID: "purchase-win-closed", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-win-closed", Type: "EXPENSE", Amount: 2000, Status: "paid", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "payment-win-closed", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 500, Status: "paid", Description: invoicePaymentDescription("Cartão Janela"), CreatedAt: base.now})
	addF1InvoicePayment(t, db, "ip-win-closed", base.ws, "inv-win-closed", base.checking, "payment-win-closed", "", 500, 0, base.user, base.now)

	addF1Invoice(t, db, "inv-win-paid", base.card, "2026-03", "PAID", 500, base.now)
	execTestSQL(t, db, `UPDATE invoices SET due_date = ? WHERE id = 'inv-win-paid'`, due(90))
	addF1Tx(t, db, f1TxSeed{ID: "purchase-win-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-win-paid", Type: "EXPENSE", Amount: 3000, Status: "paid", CreatedAt: base.now})

	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, frequency, active, created_at, updated_at
		) VALUES ('rule-win', ?, ?, ?, 'EXPENSE', 600, 'janela', ?, 'MONTHLY', 1, ?, ?)
	`, base.ws, base.user, base.checking, base.now, base.now, base.now)

	addF1Tx(t, db, f1TxSeed{ID: "pending-win-overdue", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 300, Status: "pending", CreatedAt: base.now, DueDate: due(-1)})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-10", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 2000, Status: "pending", CreatedAt: base.now, DueDate: due(10)})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-45", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 3000, Status: "pending", CreatedAt: base.now, DueDate: due(45)})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-90", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 7000, Status: "pending", CreatedAt: base.now, DueDate: due(90)})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-no-date", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 400, Status: "pending", CreatedAt: base.now})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 500, Status: "paid", CreatedAt: base.now, DueDate: due(10)})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-recurring", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 600, Status: "pending", CreatedAt: base.now, DueDate: due(20), RecurringRuleID: "rule-win", RecurrenceSequence: 1})
	addF1Tx(t, db, f1TxSeed{ID: "pending-win-recurring-paid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 700, Status: "paid", CreatedAt: base.now, DueDate: due(20), RecurringRuleID: "rule-win", RecurrenceSequence: 2})

	got := loadF1WindowObligations(t, db, base.ws)
	if got.Amount != 18100 || got.Due30 != 4100 || got.Due60 != 8600 || got.DueBeyond60 != 9500 || got.DueWithoutDate != 0 || got.DueWithoutDateCount != 0 {
		t.Fatalf("schedule = %+v, want amount 18100 due30 4100 due60 8600 beyond 9500 without 0/0", got)
	}
	if !got.ScheduleAvailable || got.Due60+got.DueBeyond60+got.DueWithoutDate != got.Amount {
		t.Fatalf("schedule did not reconcile: %+v", got)
	}
	if got.Due30 > got.Due60 {
		t.Fatalf("due30 > due60: %+v", got)
	}

	position := mustF1(t, db, base.ws)
	requireF1Reliable(t, position)
	if position.Obligations != got.Amount || position.ObligationsDue30 != got.Due30 || position.ObligationsDue60 != got.Due60 || !position.ObligationsScheduleAvailable {
		t.Fatalf("service did not expose schedule: %+v", position)
	}
	dashboard := BuildDashboardData(db, base.user, base.ws)
	if moneyDisplayToCentsForTest(t, dashboard.Balance.Obligations30) != 4100 || moneyDisplayToCentsForTest(t, dashboard.Balance.Obligations60) != 8600 {
		t.Fatal("dashboard period values diverged")
	}
	if moneyDisplayToCentsForTest(t, dashboard.Balance.Obligations) != 18100 {
		t.Fatal("dashboard total obligations changed with the presentation window")
	}
}

func TestObligationWindowWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	a := seedF1Base(t, db, "ws-win-iso-a")
	b := seedF1Base(t, db, "ws-win-iso-b")
	today := AppCivilDateUTC(time.Now())
	due := today.AddDate(0, 0, 5).Add(12 * time.Hour).Unix()
	addF1Tx(t, db, f1TxSeed{ID: "win-iso-a", WorkspaceID: a.ws, UserID: a.user, AccountID: a.checking, Type: "EXPENSE", Amount: 100, Status: "pending", CreatedAt: a.now, DueDate: due})
	addF1Tx(t, db, f1TxSeed{ID: "win-iso-b", WorkspaceID: b.ws, UserID: b.user, AccountID: b.checking, Type: "EXPENSE", Amount: 200, Status: "pending", CreatedAt: b.now, DueDate: due})

	for _, tc := range []struct {
		ws   string
		want int64
	}{{a.ws, 100}, {b.ws, 200}} {
		got := loadF1WindowObligations(t, db, tc.ws)
		if got.Due30 != tc.want || got.Due60 != tc.want || !got.ScheduleAvailable {
			t.Fatalf("workspace %s schedule = %+v, want due30/due60 %d", tc.ws, got, tc.want)
		}
	}
}

func TestObligationWindowWorkspaceTypeRules(t *testing.T) {
	today := AppCivilDateUTC(time.Now())
	due := today.AddDate(0, 0, 5).Add(12 * time.Hour).Unix()

	t.Run("personal without due uses date", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-win-personal-date")
		addF1Tx(t, db, f1TxSeed{ID: "win-personal-date", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 700, Status: "pending", CreatedAt: today.Add(12 * time.Hour).Unix()})
		got := loadF1WindowObligations(t, db, base.ws)
		if got.Amount != 700 || got.Due30 != 700 || got.Due60 != 700 || got.DueWithoutDateCount != 0 || !got.ScheduleAvailable {
			t.Fatalf("personal date fallback = %+v, want amount 700 due30/due60 700 without 0", got)
		}
	})

	t.Run("personal explicit due precedes date", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-win-personal-due")
		addF1Tx(t, db, f1TxSeed{ID: "win-personal-due", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 1000, Status: "pending", CreatedAt: today.AddDate(0, 0, 100).Unix(), DueDate: due})
		got := loadF1WindowObligations(t, db, base.ws)
		if got.Amount != 1000 || got.Due30 != 1000 || got.Due60 != 1000 || got.DueBeyond60 != 0 || !got.ScheduleAvailable {
			t.Fatalf("personal due precedence = %+v, want amount/due30/due60 1000", got)
		}
	})

	t.Run("business without due does not fallback", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-win-business-no-due")
		execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = ?`, base.ws)
		addF1Tx(t, db, f1TxSeed{ID: "win-business-no-due", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 700, Status: "pending", CreatedAt: today.Add(12 * time.Hour).Unix()})
		got := loadF1WindowObligations(t, db, base.ws)
		if got.Amount != 700 || got.Due30 != 0 || got.Due60 != 0 || got.DueWithoutDate != 700 || got.DueWithoutDateCount != 1 || !got.ScheduleAvailable {
			t.Fatalf("business no fallback = %+v, want amount 700 without 700/1", got)
		}
	})

	t.Run("business explicit due is used", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-win-business-due")
		execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = ?`, base.ws)
		addF1Tx(t, db, f1TxSeed{ID: "win-business-due", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 800, Status: "pending", CreatedAt: today.AddDate(0, 0, 100).Unix(), DueDate: due})
		got := loadF1WindowObligations(t, db, base.ws)
		if got.Amount != 800 || got.Due30 != 800 || got.Due60 != 800 || got.DueWithoutDateCount != 0 || !got.ScheduleAvailable {
			t.Fatalf("business explicit due = %+v, want amount/due30/due60 800", got)
		}
	})

	t.Run("unknown workspace type fails conservatively", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		base := seedF1Base(t, db, "ws-win-unknown-type")
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = ON`)
		execTestSQL(t, db, `UPDATE workspaces SET type = 'mystery' WHERE id = ?`, base.ws)
		tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		_, err = loadWorkspaceObligationsF1(context.Background(), tx, base.ws)
		tx.Rollback()
		if err == nil {
			t.Fatal("unknown workspace type did not fail conservatively")
		}
	})
}

func TestObligationPersonalFallbackPreservesObligations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	personal := seedF1Base(t, db, "ws-win-fallback-personal")
	business := seedF1Base(t, db, "ws-win-fallback-business")
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = ?`, business.ws)

	addF1Tx(t, db, f1TxSeed{ID: "fallback-win-personal", WorkspaceID: personal.ws, UserID: personal.user, AccountID: personal.checking, Type: "EXPENSE", Amount: 700, Status: "pending", CreatedAt: personal.now})
	addF1Tx(t, db, f1TxSeed{ID: "fallback-win-business", WorkspaceID: business.ws, UserID: business.user, AccountID: business.checking, Type: "EXPENSE", Amount: 700, Status: "pending", CreatedAt: business.now})

	personalPosition := mustF1(t, db, personal.ws)
	businessPosition := mustF1(t, db, business.ws)
	requireF1Reliable(t, personalPosition)
	requireF1Reliable(t, businessPosition)

	if personalPosition.Obligations != 700 || businessPosition.Obligations != 700 {
		t.Fatalf("U changed with fallback: personal=%d business=%d, want 700/700", personalPosition.Obligations, businessPosition.Obligations)
	}
	if personalPosition.ObligationsDue30 != 700 || businessPosition.ObligationsDue30 != 0 {
		t.Fatalf("period distribution = personal %d / business %d, want 700/0", personalPosition.ObligationsDue30, businessPosition.ObligationsDue30)
	}
	if personalPosition.ObligationsWithoutDateCount != 0 || businessPosition.ObligationsWithoutDateCount != 1 {
		t.Fatalf("without-date counts = personal %d / business %d, want 0/1", personalPosition.ObligationsWithoutDateCount, businessPosition.ObligationsWithoutDateCount)
	}
}

func TestTransactionPrimaryDateLabel(t *testing.T) {
	for _, tc := range []struct {
		name               string
		isBusiness         bool
		transaction        string
		status             string
		originType         string
		explicitDueDiffers bool
		want               string
	}{
		{name: "personal pending expense", transaction: "EXPENSE", status: "pending", want: "Data de vencimento"},
		{name: "personal paid expense", transaction: "EXPENSE", status: "paid", want: "Data do lançamento"},
		{name: "personal pending card", transaction: "EXPENSE", status: "pending", originType: "CREDIT_CARD", want: "Data da compra"},
		{name: "personal paid card", transaction: "EXPENSE", status: "paid", originType: "CREDIT_CARD", want: "Data da compra"},
		{name: "personal paid card form kind", transaction: "EXPENSE", status: "paid", originType: "cartao", want: "Data da compra"},
		{name: "personal pending income", transaction: "INCOME", status: "pending", want: "Data de recebimento prevista"},
		{name: "personal received income", transaction: "INCOME", status: "paid", want: "Data do lançamento"},
		{name: "personal transfer", transaction: "TRANSFER", status: "paid", want: "Data da transferência"},
		{name: "personal explicit due differs", transaction: "EXPENSE", status: "pending", explicitDueDiffers: true, want: "Data do lançamento"},
		{name: "business pending expense", isBusiness: true, transaction: "EXPENSE", status: "pending", want: "Data de competência"},
		{name: "business paid expense", isBusiness: true, transaction: "EXPENSE", status: "paid", want: "Data de competência"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := transactionPrimaryDateLabel(tc.isBusiness, tc.transaction, tc.status, tc.originType, tc.explicitDueDiffers); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}
