package handlers

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedProjectionTransaction(t *testing.T, db *sql.DB, id, workspaceID, userID, accountID, kind string, amount, date, due int64, status string) {
	t.Helper()
	var paidAt any
	if status == "paid" {
		paidAt = date
	}
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, due_date, paid_at, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'projection test', ?, 1, 1, ?, ?)`,
		id, workspaceID, userID, accountID, kind, amount, date, due, paidAt, status, time.Now().Unix(), time.Now().Unix())
}

func TestProjectionPeriodNormalFiltersAndWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, checking, card = "projection-a", "projection-user-a", "projection-checking-a", "projection-card-a"
	seedOverflowReportWorkspace(t, db, ws, user, checking, "personal", 100000)
	seedOverflowReportWorkspace(t, db, "projection-b", "projection-user-b", "projection-checking-b", "personal", math.MaxInt64)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	due := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC).Unix()
	end := time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC).Unix()
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Card', 'CREDIT_CARD', 0, ?, ?, ?)`, card, ws, math.MaxInt64, now.Unix(), now.Unix())
	seedProjectionTransaction(t, db, "projection-income", ws, user, checking, "INCOME", 25000, due, due, "pending")
	seedProjectionTransaction(t, db, "projection-expense", ws, user, checking, "EXPENSE", 10000, due, due, "pending")
	seedProjectionTransaction(t, db, "projection-paid", ws, user, checking, "INCOME", 7000, due, due, "paid")
	seedProjectionTransaction(t, db, "projection-outside", ws, user, checking, "INCOME", 9000, end+86400, end+86400, "pending")
	seedProjectionTransaction(t, db, "projection-before-today", ws, user, checking, "INCOME", 9000, now.AddDate(0, 0, -1).Unix(), now.AddDate(0, 0, -1).Unix(), "pending")
	seedProjectionTransaction(t, db, "projection-invoice-payment-prefix", ws, user, checking, "EXPENSE", 9000, due, due, "pending")
	execTestSQL(t, db, `UPDATE transactions SET description=? WHERE id='projection-invoice-payment-prefix' AND workspace_id=?`, invoicePaymentDescriptionPrefix+"probe", ws)
	seedProjectionTransaction(t, db, "projection-card-transaction", ws, user, card, "EXPENSE", 8000, due, due, "pending")
	seedProjectionTransaction(t, db, "projection-foreign", "projection-b", "projection-user-b", "projection-checking-b", "INCOME", math.MaxInt64, due, due, "pending")
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('projection-invoice', ?, '2026-08', ?, ?, 'OPEN', ?)`, card, due, due, now.Unix())
	seedProjectionTransaction(t, db, "projection-purchase", ws, user, card, "EXPENSE", 5000, due, due, "paid")
	execTestSQL(t, db, `UPDATE transactions SET invoice_id='projection-invoice' WHERE id='projection-purchase' AND workspace_id=?`, ws)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('projection-invoice-outside', ?, '2026-09', ?, ?, 'OPEN', ?)`, card, end+86400, end+86400, now.Unix())
	seedProjectionTransaction(t, db, "projection-purchase-outside", ws, user, card, "EXPENSE", 7000, end+86400, end+86400, "paid")
	execTestSQL(t, db, `UPDATE transactions SET invoice_id='projection-invoice-outside' WHERE id='projection-purchase-outside' AND workspace_id=?`, ws)
	got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, ws, end, now)
	if err != nil || !show || got != 110000 {
		t.Fatalf("period balance = %d, %v, %v; want 110000", got, show, err)
	}
	if got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, ws, now.AddDate(0, -1, 0).Unix(), now); err != nil || show || got != 0 {
		t.Fatalf("past period = %d, %v, %v", got, show, err)
	}
}

func TestProjectionPeriodExactLimitsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		balance         int64
		income, expense []int64
		want            int64
		outOfRange      bool
	}{
		{"empty", 0, nil, nil, 0, false},
		{"maximum", math.MaxInt64, nil, nil, math.MaxInt64, false},
		{"minimum", math.MinInt64, nil, nil, math.MinInt64, false},
		{"positive cancellation", math.MaxInt64, []int64{1}, []int64{1}, math.MaxInt64, false},
		{"positive reordered", math.MaxInt64, []int64{1, 2}, []int64{2, 1}, math.MaxInt64, false},
		{"negative cancellation", math.MinInt64, []int64{1}, []int64{1}, math.MinInt64, false},
		{"negative reordered", math.MinInt64, []int64{2, 1}, []int64{1, 2}, math.MinInt64, false},
		{"above maximum", math.MaxInt64, []int64{1}, nil, 0, true},
		{"below minimum", math.MinInt64, nil, []int64{1}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			const ws, user, account = "projection-limit", "projection-limit-user", "projection-limit-account"
			seedOverflowReportWorkspace(t, db, ws, user, account, "personal", tc.balance)
			now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
			due := now.AddDate(0, 0, 1).Unix()
			for i, amount := range tc.income {
				seedProjectionTransaction(t, db, tc.name+"-income-"+string(rune('a'+i)), ws, user, account, "INCOME", amount, due, due, "pending")
			}
			for i, amount := range tc.expense {
				seedProjectionTransaction(t, db, tc.name+"-expense-"+string(rune('a'+i)), ws, user, account, "EXPENSE", amount, due, due, "pending")
			}
			got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, ws, due, now)
			if tc.outOfRange {
				if !errors.Is(err, errReportAggregateOverflow) || show {
					t.Fatalf("want out-of-range without display, got %d, %v, %v", got, show, err)
				}
			} else if err != nil || !show || got != tc.want {
				t.Fatalf("balance = %d, %v, %v; want %d", got, show, err, tc.want)
			}
		})
	}
}

func TestProjectionPeriodInvoiceIntermediateExceedsInt64(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, checking, card = "projection-invoice-ws", "projection-invoice-user", "projection-invoice-checking", "projection-invoice-card"
	seedOverflowReportWorkspace(t, db, ws, user, checking, "personal", math.MaxInt64)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	due := now.AddDate(0, 0, 5).Unix()
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Card', 'CREDIT_CARD', 0, 0, ?, ?)`, card, ws, now.Unix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('projection-invoice-over', ?, '2026-08', ?, ?, 'OPEN', ?)`, card, due, due, now.Unix())
	for i, amount := range []int64{math.MaxInt64, 1} {
		id := "invoice-item-" + string(rune('a'+i))
		seedProjectionTransaction(t, db, id, ws, user, card, "EXPENSE", amount, due, due, "paid")
		execTestSQL(t, db, `UPDATE transactions SET invoice_id='projection-invoice-over' WHERE id=? AND workspace_id=?`, id, ws)
	}
	got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, ws, due, now)
	if err != nil || !show || got != -1 {
		t.Fatalf("invoice cancellation = %d, %v, %v; want -1", got, show, err)
	}
}

func TestProjectedAccumulatedExactTransactionsAndChronology(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "projection-series", "projection-series-user", "projection-series-account"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
	today := AppCivilDateUTC(time.Now())
	current := appMonthStartUTC(time.Now())
	future := current.AddDate(0, 1, 0)
	seedProjectionTransaction(t, db, "series-income", ws, user, account, "INCOME", 250, today.Unix(), today.Unix(), "pending")
	seedProjectionTransaction(t, db, "series-expense", ws, user, account, "EXPENSE", 100, today.Unix(), today.Unix(), "pending")
	seedProjectionTransaction(t, db, "series-future", ws, user, account, "INCOME", 50, future.AddDate(0, 0, 2).Unix(), future.AddDate(0, 0, 2).Unix(), "pending")
	if got, err := calcProjectedAccumulatedBalance(db, ws, 1000, current); err != nil || got != 1150 {
		t.Fatalf("current point = %d, %v; want 1150", got, err)
	}
	if got, err := calcProjectedAccumulatedBalance(db, ws, 1000, future); err != nil || got != 1200 {
		t.Fatalf("future point = %d, %v; want 1200", got, err)
	}
	seedOverflowReportWorkspace(t, db, "projection-series-b", "projection-series-user-b", "projection-series-account-b", "personal", 0)
	seedProjectionTransaction(t, db, "series-foreign", "projection-series-b", "projection-series-user-b", "projection-series-account-b", "INCOME", math.MaxInt64, today.Unix(), today.Unix(), "pending")
	if got, err := calcProjectedAccumulatedBalance(db, ws, 1000, current); err != nil || got != 1150 {
		t.Fatalf("foreign workspace changed A: %d, %v", got, err)
	}
}

func TestProjectedAccumulatedExactBoundariesAndRecurring(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "projection-recurring", "projection-recurring-user", "projection-recurring-account"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
	today := AppCivilDateUTC(time.Now())
	month := appMonthStartUTC(time.Now())
	end := month.AddDate(0, 1, 0)
	remainingDays := int64(end.Sub(today).Hours() / 24)
	seedProjectionTransaction(t, db, "recurring-max", ws, user, account, "INCOME", math.MaxInt64, today.Unix(), today.Unix(), "pending")
	seedProjectionTransaction(t, db, "recurring-plus", ws, user, account, "INCOME", 1, today.Unix(), today.Unix(), "pending")
	execTestSQL(t, db, `INSERT INTO recurring_rules
		(id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES ('projection-rule', ?, ?, ?, 'EXPENSE', 1, 'recurring expense', ?, 'DAILY', 'PENDING', 1, ?, ?)`,
		ws, user, account, today.AddDate(0, 0, -1).Unix(), time.Now().Unix(), time.Now().Unix())
	got, err := calcProjectedAccumulatedBalance(db, ws, 0, month)
	want := math.MaxInt64 - (remainingDays - 1)
	if err != nil || got != want {
		t.Fatalf("recurring cancellation = %d, %v; want %d", got, err, want)
	}
	if _, err := calcProjectedAccumulatedBalance(db, ws, math.MaxInt64, month); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("final high out-of-range = %v", err)
	}
}

func TestProjectedAccumulatedSignedLimitsAndOrder(t *testing.T) {
	for _, tc := range []struct {
		name            string
		base            int64
		income, expense []int64
		want            int64
		outOfRange      bool
	}{
		{"zero", 0, nil, nil, 0, false},
		{"maximum", math.MaxInt64, nil, nil, math.MaxInt64, false},
		{"minimum", math.MinInt64, nil, nil, math.MinInt64, false},
		{"positive cancellation", math.MaxInt64, []int64{1}, []int64{1}, math.MaxInt64, false},
		{"positive reordered", math.MaxInt64, []int64{2, 1}, []int64{1, 2}, math.MaxInt64, false},
		{"negative cancellation", math.MinInt64, []int64{1}, []int64{1}, math.MinInt64, false},
		{"negative reordered", math.MinInt64, []int64{2, 1}, []int64{1, 2}, math.MinInt64, false},
		{"above maximum", math.MaxInt64, []int64{1}, nil, 0, true},
		{"below minimum", math.MinInt64, nil, []int64{1}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			const ws, user, account = "projection-signed", "projection-signed-user", "projection-signed-account"
			seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
			date := AppCivilDateUTC(time.Now()).Unix()
			for i, amount := range tc.income {
				seedProjectionTransaction(t, db, tc.name+"-income-"+string(rune('a'+i)), ws, user, account, "INCOME", amount, date, date, "pending")
			}
			for i, amount := range tc.expense {
				seedProjectionTransaction(t, db, tc.name+"-expense-"+string(rune('a'+i)), ws, user, account, "EXPENSE", amount, date, date, "pending")
			}
			got, err := calcProjectedAccumulatedBalance(db, ws, tc.base, appMonthStartUTC(time.Now()))
			if tc.outOfRange {
				if !errors.Is(err, errReportAggregateOverflow) {
					t.Fatalf("want out-of-range, got %d, %v", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("balance = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestProjectionErrorPropagatesToReportHTTP(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "projection-http", "projection-http-user", "projection-http-account"
	seedOverflowReportWorkspace(t, db, ws, user, account, "business", math.MaxInt64)
	date := AppCivilDateUTC(time.Now()).Unix()
	seedProjectionTransaction(t, db, "projection-http-income", ws, user, account, "INCOME", 1, date, date, "pending")
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: ws, UserID: user}
	request := httptest.NewRequest(http.MethodGet, "/relatorios?tab=competencia", nil)
	response := httptest.NewRecorder()
	h.HandleExibirRelatorios(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "R$") {
		t.Fatalf("projection overflow HTTP = %d, body %q", response.Code, response.Body.String())
	}
}

func TestProjectionPeriodErrorPropagatesToReportHTTP(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "projection-period-http", "projection-period-http-user", "projection-period-http-account"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", math.MaxInt64)
	date := AppCivilDateUTC(time.Now()).Unix()
	seedProjectionTransaction(t, db, "projection-period-http-income", ws, user, account, "INCOME", 1, date, date, "pending")
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: ws, UserID: user}
	request := httptest.NewRequest(http.MethodGet, "/relatorios?tab=caixa&modo=previsao", nil)
	response := httptest.NewRecorder()
	h.HandleExibirRelatorios(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "R$") {
		t.Fatalf("period projection overflow HTTP = %d, body %q", response.Code, response.Body.String())
	}
}

func TestProjectedAccumulatedPaidBranchExact(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "projection-paid", "projection-paid-user", "projection-paid-account"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
	current := appMonthStartUTC(time.Now())
	// Keep the paid rows inside the historical window even on the first day.
	previous := current.AddDate(0, -1, 0)
	past := previous.AddDate(0, -1, 0)
	date := previous.Unix()
	seedProjectionTransaction(t, db, "paid-max", ws, user, account, "INCOME", math.MaxInt64, date, date, "paid")
	seedProjectionTransaction(t, db, "paid-plus", ws, user, account, "INCOME", 1, date, date, "paid")
	got, err := calcProjectedAccumulatedBalance(db, ws, math.MaxInt64, past)
	if err != nil || got != -1 {
		t.Fatalf("paid branch cancellation = %d, %v; want -1", got, err)
	}
	t.Logf("historical exact=%d", got)
	if _, err := calcProjectedAccumulatedBalance(db, ws, math.MinInt64, past); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("paid branch low out-of-range = %v", err)
	} else {
		t.Logf("historical overflow=%v", err)
	}
}

func TestProjectedRecurringRulePreservesOccurrenceDates(t *testing.T) {
	start := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                 string
		rangeStart, rangeEnd time.Time
		want                 int64
	}{
		{"before March occurrence", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), 0},
		{"March occurrence", time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), -37},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow, err := projectedRecurringRuleNet("EXPENSE", 37, start.Unix(), "MONTHLY", tc.rangeStart, tc.rangeEnd)
			if err != nil {
				t.Fatal(err)
			}
			got, err := flow.int64("test recurring")
			if err != nil || got != tc.want {
				t.Fatalf("recurring flow = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestProjectedAccumulatedRecurringRulesCancelBeyondInt64(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "projection-rules-cancel", "projection-rules-user", "projection-rules-account"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
	today := AppCivilDateUTC(time.Now())
	selectedMonth := appMonthStartUTC(time.Now()).AddDate(0, 1, 0)
	for _, rule := range []struct{ id, kind string }{{"rule-income", "INCOME"}, {"rule-expense", "EXPENSE"}} {
		execTestSQL(t, db, `INSERT INTO recurring_rules
			(id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'cancelled rule', ?, 'DAILY', 'PENDING', 1, ?, ?)`,
			rule.id, ws, user, account, rule.kind, int64(math.MaxInt64), today.AddDate(0, 0, -1).Unix(), time.Now().Unix(), time.Now().Unix())
	}
	got, err := calcProjectedAccumulatedBalance(db, ws, 0, selectedMonth)
	if err != nil || got != 0 {
		t.Fatalf("opposite recurring rules must cancel: %d, %v", got, err)
	}
}
