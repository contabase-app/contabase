package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

const seriesTestWorkspace = "ws-series"
const seriesTestUser = "u-series"

func seriesTestLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load timezone: %v", err)
	}
	return loc
}

func seedSeriesWorkspace(t *testing.T, db *sql.DB, workspaceID, userID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES (?, 'Series User', ?, 'hash', 'active', ?, ?)`, userID, userID+"@example.com", now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES (?, 'Series Workspace', 'personal', ?, ?)`, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', ?)`, workspaceID, userID, now)
}

func insertSeriesAccount(t *testing.T, db *sql.DB, id, workspaceID, kind string, initial, current, createdAt int64, archivedAt interface{}) {
	t.Helper()
	execTestSQL(t, db, `INSERT INTO accounts
		(id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at, archived_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceID, "Conta "+id, kind, initial, current, createdAt, createdAt, archivedAt)
}

func insertSeriesTransaction(t *testing.T, db *sql.DB, id, workspaceID, userID, accountID, destinationID, kind string, amount, dateUnix, createdAt int64, status string) {
	t.Helper()
	var destination interface{}
	if destinationID != "" {
		destination = destinationID
	}
	var paidAt interface{}
	if status == "paid" {
		paidAt = dateUnix
	}
	dateCivil := time.Unix(dateUnix, 0).UTC().Format("2006-01-02")
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, date_civil, description, status, paid_at, effect_mode, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'Teste', ?, ?, 'STANDARD', ?, ?)`,
		id, workspaceID, userID, accountID, destination, kind, amount, dateUnix, dateCivil, status, paidAt, createdAt, createdAt)
}

func seriesTestNow() time.Time {
	return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
}

func seriesTestPeriod(t *testing.T, key string) balanceSeriesPeriod {
	t.Helper()
	period, ok := parseBalanceSeriesPeriod(key)
	if !ok {
		t.Fatalf("period %q not found", key)
	}
	return period
}

func seriesPointAt(t *testing.T, data DashboardBalanceSeries, date string) DashboardBalancePoint {
	t.Helper()
	for _, point := range data.Points {
		if point.Date == date {
			return point
		}
	}
	t.Fatalf("point %s not found in %v", date, data.Points)
	return DashboardBalancePoint{}
}

func TestBalanceSeriesIncomeExpensePendingAndTransfer(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-a", seriesTestWorkspace, "CHECKING", 100000, 100000, created, nil)
	insertSeriesAccount(t, db, "acc-b", seriesTestWorkspace, "SAVINGS", 0, 0, created, nil)

	insertSeriesTransaction(t, db, "tx-income", seriesTestWorkspace, seriesTestUser, "acc-a", "", "INCOME", 5000, seriesNoonUTC(2026, 10, 2), created, "paid")
	insertSeriesTransaction(t, db, "tx-expense", seriesTestWorkspace, seriesTestUser, "acc-a", "", "EXPENSE", 3000, seriesNoonUTC(2026, 10, 3), created, "paid")
	insertSeriesTransaction(t, db, "tx-pending", seriesTestWorkspace, seriesTestUser, "acc-a", "", "EXPENSE", 9000, seriesNoonUTC(2026, 10, 4), created, "pending")
	insertSeriesTransaction(t, db, "tx-transfer", seriesTestWorkspace, seriesTestUser, "acc-a", "acc-b", "TRANSFER", 4000, seriesNoonUTC(2026, 10, 5), created, "paid")
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 98000 WHERE id = 'acc-a'`)
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 4000 WHERE id = 'acc-b'`)

	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	wantTotal := int64(102000)
	if data.Status != "reconstructed" || !data.Reconciled {
		t.Fatalf("unexpected status: %+v", data)
	}
	if got := seriesPointAt(t, data, "2026-10-01").BalanceCents; got != "100000" {
		t.Fatalf("before events = %s, want 100000", got)
	}
	if got := seriesPointAt(t, data, "2026-10-02").BalanceCents; got != "105000" {
		t.Fatalf("after income = %s, want 105000", got)
	}
	if got := seriesPointAt(t, data, "2026-10-03").BalanceCents; got != "102000" {
		t.Fatalf("after expense = %s, want 102000", got)
	}
	if got := seriesPointAt(t, data, "2026-10-04").BalanceCents; got != "102000" {
		t.Fatalf("pending must not affect: got %s", got)
	}
	if got := seriesPointAt(t, data, "2026-10-05").BalanceCents; got != "102000" {
		t.Fatalf("internal transfer must net zero: got %s", got)
	}
	if data.Reconciled && data.Points[len(data.Points)-1].BalanceCents != formatCents(wantTotal) {
		t.Fatalf("last point = %s, want %s", data.Points[len(data.Points)-1].BalanceCents, formatCents(wantTotal))
	}
}

func seriesTestUTC() *time.Location { return time.UTC }

func seriesNoonUTC(year int, month time.Month, day int) int64 {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Unix()
}

func TestBalanceSeriesCreditCardAndInvoicePayment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-bank", seriesTestWorkspace, "CHECKING", 100000, 100000, created, nil)
	insertSeriesAccount(t, db, "acc-card", seriesTestWorkspace, "CREDIT_CARD", 0, 0, created, nil)

	// Card purchase: paid, but the card account is excluded from the total.
	insertSeriesTransaction(t, db, "tx-card", seriesTestWorkspace, seriesTestUser, "acc-card", "", "EXPENSE", 25000, seriesNoonUTC(2026, 10, 2), created, "paid")
	// Invoice payment: an EXPENSE from the real account.
	insertSeriesTransaction(t, db, "tx-invoice", seriesTestWorkspace, seriesTestUser, "acc-bank", "", "EXPENSE", 25000, seriesNoonUTC(2026, 10, 10), created, "paid")
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 75000 WHERE id = 'acc-bank'`)

	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if got := seriesPointAt(t, data, "2026-10-09").BalanceCents; got != "100000" {
		t.Fatalf("card purchase must not change consolidated total: got %s", got)
	}
	if got := seriesPointAt(t, data, "2026-10-10").BalanceCents; got != "75000" {
		t.Fatalf("invoice payment must debit bank once: got %s", got)
	}
}

func TestBalanceSeriesTransferToCreditCardLeavesConsolidated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-bank", seriesTestWorkspace, "CHECKING", 100000, 95000, created, nil)
	insertSeriesAccount(t, db, "acc-card", seriesTestWorkspace, "CREDIT_CARD", 0, 5000, created, nil)
	insertSeriesTransaction(t, db, "tx-to-card", seriesTestWorkspace, seriesTestUser, "acc-bank", "acc-card", "TRANSFER", 5000, seriesNoonUTC(2026, 10, 4), created, "paid")

	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if got := seriesPointAt(t, data, "2026-10-03").BalanceCents; got != "100000" {
		t.Fatalf("before transfer = %s, want 100000", got)
	}
	if got := seriesPointAt(t, data, "2026-10-04").BalanceCents; got != "95000" {
		t.Fatalf("after transfer to card = %s, want 95000", got)
	}
}

func TestBalanceSeriesAdjustmentAndInvestmentValuation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-a", seriesTestWorkspace, "CHECKING", 100000, 100000, created, nil)
	insertSeriesAccount(t, db, "acc-inv", seriesTestWorkspace, "INVESTMENT", 100000, 100000, created, nil)
	execTestSQL(t, db, `INSERT INTO investments
		(id, workspace_id, account_id, name, institution, kind, principal_cents, tax_regime, applied_date, created_by, created_at, updated_at)
		VALUES ('inv-1', ?, 'acc-inv', 'Investimento Teste', '', 'CDB', 100000, 'MANUAL', '2026-01-01', ?, ?, ?)`,
		seriesTestWorkspace, seriesTestUser, created, created)
	execTestSQL(t, db, `INSERT INTO investment_valuations
		(id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at)
		VALUES ('val-1', ?, 'inv-1', '2026-10-03', 150000, ?, ?)`,
		seriesTestWorkspace, seriesTestUser, created)

	adjustAt := seriesNoonUTC(2026, 10, 5)
	if err := services.AdjustAccountBalance(context.Background(), db, services.AccountBalanceAdjustment{
		ID: "adj-1", WorkspaceID: seriesTestWorkspace, AccountID: "acc-a", UserID: seriesTestUser,
		Reason: "Conferência", ExpectedVersion: 0, TargetBalance: 120000, CreatedAt: adjustAt,
	}); err != nil {
		t.Fatalf("adjust account: %v", err)
	}

	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if got := seriesPointAt(t, data, "2026-10-04").BalanceCents; got != "200000" {
		t.Fatalf("before adjustment = %s, want 200000 (valuation must not affect)", got)
	}
	if got := seriesPointAt(t, data, "2026-10-05").BalanceCents; got != "220000" {
		t.Fatalf("after adjustment = %s, want 220000", got)
	}
}

func TestBalanceSeriesAccountLifecycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)

	insertSeriesAccount(t, db, "acc-archived", seriesTestWorkspace, "CHECKING", 50000, 50000, created, seriesNoonUTC(2026, 6, 15))
	insertSeriesAccount(t, db, "acc-created", seriesTestWorkspace, "SAVINGS", 5000, 5000, seriesNoonUTC(2026, 6, 15), nil)

	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), now, seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if data.Status != "reconstructed" {
		t.Fatalf("status = %q, want reconstructed", data.Status)
	}
	if got := seriesPointAt(t, data, "2026-06-14").BalanceCents; got != "50000" {
		t.Fatalf("before archive and creation = %s, want 50000", got)
	}
	if got := seriesPointAt(t, data, "2026-06-15").BalanceCents; got != "5000" {
		t.Fatalf("archive must drop and created account must enter = %s, want 5000", got)
	}
	if data.CoverageStart != "2026-06-01" {
		t.Fatalf("coverage start = %s", data.CoverageStart)
	}
}

func TestBalanceSeriesCreatedAccountCoverageStartsLater(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	insertSeriesAccount(t, db, "acc-new", seriesTestWorkspace, "CHECKING", 5000, 5000, seriesNoonUTC(2026, 10, 10), nil)

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), now, seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if data.Status != "partial" {
		t.Fatalf("status = %q, want partial", data.Status)
	}
	if data.CoverageStart != "2026-10-10" {
		t.Fatalf("coverage start = %q, want 2026-10-10", data.CoverageStart)
	}
	if len(data.Points) != 15 {
		t.Fatalf("points = %d, want 15", len(data.Points))
	}
	if data.Points[0].BalanceCents != "5000" {
		t.Fatalf("first point = %s", data.Points[0].BalanceCents)
	}
}

func TestBalanceSeriesTimezoneUsesLocalEndOfDay(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	loc := seriesTestLocation(t)
	created := time.Date(2026, 1, 1, 12, 0, 0, 0, loc).Unix()
	insertSeriesAccount(t, db, "acc-a", seriesTestWorkspace, "CHECKING", 100000, 100000, created, nil)

	adjustAt := time.Date(2026, 6, 10, 23, 30, 0, 0, loc).Unix()
	if err := services.AdjustAccountBalance(context.Background(), db, services.AccountBalanceAdjustment{
		ID: "adj-tz", WorkspaceID: seriesTestWorkspace, AccountID: "acc-a", UserID: seriesTestUser,
		Reason: "Fim do dia", ExpectedVersion: 0, TargetBalance: 110000, CreatedAt: adjustAt,
	}); err != nil {
		t.Fatalf("adjust account: %v", err)
	}

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, loc)
	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), now, loc)
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if got := seriesPointAt(t, data, "2026-06-09").BalanceCents; got != "100000" {
		t.Fatalf("day before adjustment = %s, want 100000", got)
	}
	if got := seriesPointAt(t, data, "2026-06-10").BalanceCents; got != "110000" {
		t.Fatalf("same local day must include 23:30 adjustment: got %s", got)
	}
}

func TestBalanceSeriesReconciliationMismatchReturnsUnavailable(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-a", seriesTestWorkspace, "CHECKING", 100000, 100000, created, nil)

	// Applied now but economically dated tomorrow: the end-of-day series cannot
	// anchor this effect without moving it artificially, so it must refuse.
	future := seriesTestNow().AddDate(0, 0, 1).Unix()
	if err := services.AdjustAccountBalance(context.Background(), db, services.AccountBalanceAdjustment{
		ID: "adj-future", WorkspaceID: seriesTestWorkspace, AccountID: "acc-a", UserID: seriesTestUser,
		Reason: "Ajuste futuro", ExpectedVersion: 0, TargetBalance: 120000, CreatedAt: future,
	}); err != nil {
		t.Fatalf("adjust account: %v", err)
	}

	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if data.Status != "unavailable" || data.Reason != "reconciliation_mismatch" {
		t.Fatalf("status = %q reason = %q, want unavailable/reconciliation_mismatch", data.Status, data.Reason)
	}
	if len(data.Points) != 0 || data.Reconciled {
		t.Fatalf("mismatch must not return points: %+v", data)
	}
	if !hasBalanceSeriesLimitation(data, "reconciliation_mismatch") {
		t.Fatalf("missing reconciliation limitation: %+v", data.Limitations)
	}
}

func hasBalanceSeriesLimitation(data DashboardBalanceSeries, code string) bool {
	for _, limitation := range data.Limitations {
		if limitation.Code == code {
			return true
		}
	}
	return false
}

func TestBalanceSeriesNoAccountsAndWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, "ws-empty", "u-empty")
	seedSeriesWorkspace(t, db, "ws-one", "u-one")
	seedSeriesWorkspace(t, db, "ws-two", "u-two")
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-one", "ws-one", "CHECKING", 10000, 10000, created, nil)
	insertSeriesAccount(t, db, "acc-two", "ws-two", "CHECKING", 20000, 20000, created, nil)

	empty, err := BuildDashboardBalanceSeries(db, "ws-empty", seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("empty series: %v", err)
	}
	if empty.Status != "unavailable" || empty.Reason != "no_accounts" {
		t.Fatalf("empty status = %q/%q", empty.Status, empty.Reason)
	}

	one, err := BuildDashboardBalanceSeries(db, "ws-one", seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("workspace one series: %v", err)
	}
	for _, point := range one.Points {
		if point.BalanceCents != "10000" {
			t.Fatalf("workspace isolation failed: %s", point.BalanceCents)
		}
	}
}

func TestBalanceSeriesNegativeAndYearBoundary(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	insertSeriesAccount(t, db, "acc-neg", seriesTestWorkspace, "CHECKING", -5000, -5000, seriesNoonUTC(2025, 1, 1), nil)

	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), now, seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if data.Points[0].Date != "2025-12-17" || data.Points[len(data.Points)-1].Date != "2026-01-15" {
		t.Fatalf("year boundary dates = %s..%s", data.Points[0].Date, data.Points[len(data.Points)-1].Date)
	}
	if data.Points[0].BalanceCents != "-5000" {
		t.Fatalf("negative balance = %s", data.Points[0].BalanceCents)
	}
	if data.Variation == nil || !data.Variation.Available || data.Variation.Direction != "flat" {
		t.Fatalf("flat negative variation = %+v", data.Variation)
	}
}

func TestBalanceSeriesRetroactiveDateMovesEffect(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	created := seriesNoonUTC(2026, 1, 1)
	insertSeriesAccount(t, db, "acc-a", seriesTestWorkspace, "CHECKING", 100000, 95000, created, nil)
	original := seriesNoonUTC(2026, 10, 20)
	insertSeriesTransaction(t, db, "tx-retro", seriesTestWorkspace, seriesTestUser, "acc-a", "", "EXPENSE", 5000, original, created, "paid")

	data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series: %v", err)
	}
	if got := seriesPointAt(t, data, "2026-10-19").BalanceCents; got != "100000" {
		t.Fatalf("before original date = %s", got)
	}
	if got := seriesPointAt(t, data, "2026-10-20").BalanceCents; got != "95000" {
		t.Fatalf("at original date = %s", got)
	}

	retro := seriesNoonUTC(2026, 10, 5)
	execTestSQL(t, db, `UPDATE transactions SET date = ?, date_civil = '2026-10-05', paid_at = ? WHERE id = 'tx-retro'`, retro, retro)
	data, err = BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, "30d"), seriesTestNow(), seriesTestUTC())
	if err != nil {
		t.Fatalf("build series after retroactive edit: %v", err)
	}
	if got := seriesPointAt(t, data, "2026-10-04").BalanceCents; got != "100000" {
		t.Fatalf("before retroactive date = %s", got)
	}
	if got := seriesPointAt(t, data, "2026-10-05").BalanceCents; got != "95000" {
		t.Fatalf("retroactive effect must move to new date = %s", got)
	}
	if !hasBalanceSeriesLimitation(data, "retroactive_changes") {
		t.Fatal("retroactive limitation missing")
	}
}

func TestBalanceSeriesSamplingAndPeriods(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	insertSeriesAccount(t, db, "acc-a", seriesTestWorkspace, "CHECKING", 100000, 100000, seriesNoonUTC(2024, 1, 1), nil)

	cases := []struct {
		period string
		points int
		step   int
		status string
		start  string
	}{
		{"30d", 30, 1, "reconstructed", "2026-09-25"},
		{"90d", 46, 2, "reconstructed", "2026-07-27"},
		{"6m", 46, 4, "reconstructed", "2026-04-28"},
		{"12m", 47, 8, "reconstructed", "2025-10-25"},
	}
	for _, tc := range cases {
		t.Run(tc.period, func(t *testing.T) {
			data, err := BuildDashboardBalanceSeries(db, seriesTestWorkspace, seriesTestPeriod(t, tc.period), seriesTestNow(), seriesTestUTC())
			if err != nil {
				t.Fatalf("build %s: %v", tc.period, err)
			}
			if data.Status != tc.status {
				t.Fatalf("status = %q", data.Status)
			}
			if data.SampleIntervalDays != tc.step {
				t.Fatalf("step = %d", data.SampleIntervalDays)
			}
			if data.CoverageStart != tc.start {
				t.Fatalf("coverage start = %s, want %s", data.CoverageStart, tc.start)
			}
			if len(data.Points) != tc.points {
				t.Fatalf("points = %d, want %d", len(data.Points), tc.points)
			}
			if len(data.Points) > 48 {
				t.Fatalf("too many points: %d", len(data.Points))
			}
			if data.Points[len(data.Points)-1].Date != "2026-10-24" {
				t.Fatalf("last date = %s", data.Points[len(data.Points)-1].Date)
			}
		})
	}
}

func TestBalanceSeriesHandlerContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedSeriesWorkspace(t, db, seriesTestWorkspace, seriesTestUser)
	insertSeriesAccount(t, db, "acc-secret-name", seriesTestWorkspace, "CHECKING", 100000, 100000, seriesNoonUTC(2026, 1, 1), nil)
	now := seriesTestNow()
	handler := &DashboardBalanceSeriesHandler{
		DB: db, WorkspaceID: seriesTestWorkspace, Location: seriesTestUTC(),
		Now: func() time.Time { return now },
	}

	t.Run("valid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/balance-series?period=30d", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("cache control = %q", got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("content type = %q", got)
		}
		var payload DashboardBalanceSeries
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("json: %v", err)
		}
		if payload.Currency != "BRL" || payload.Unit != "cents" || payload.Status != "reconstructed" {
			t.Fatalf("payload = %+v", payload)
		}
		if len(payload.Points) == 0 || payload.Points[0].BalanceCents != "100000" {
			t.Fatalf("points = %+v", payload.Points)
		}
		body := rec.Body.String()
		if strings.Contains(body, "acc-secret-name") || strings.Contains(body, "Conta acc-secret-name") {
			t.Fatalf("handler leaked account identity: %s", body)
		}
	})

	t.Run("invalid period", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/balance-series?period=1y", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", rec.Code)
		}
	})

	t.Run("method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/dashboard/balance-series?period=30d", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d", rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != http.MethodGet {
			t.Fatalf("allow = %q", got)
		}
	})
}

func TestBalanceSeriesVariationExactAndCapped(t *testing.T) {
	points := []DashboardBalancePoint{
		{Date: "2026-01-01", BalanceCents: "100000"},
		{Date: "2026-01-02", BalanceCents: "112400"},
	}
	variation := buildBalanceSeriesVariation(points)
	if variation == nil || !variation.Available || variation.PercentTenths != 124 || variation.Direction != "up" {
		t.Fatalf("variation = %+v", variation)
	}
	if variation.ChangeCents != "12400" {
		t.Fatalf("change = %s", variation.ChangeCents)
	}

	capped := buildBalanceSeriesVariation([]DashboardBalancePoint{
		{Date: "2026-01-01", BalanceCents: "1"},
		{Date: "2026-01-02", BalanceCents: "1000000"},
	})
	// A base below 10 cents is not comparable.
	if capped.Available || capped.Reason != "insufficient_base" {
		t.Fatalf("tiny base = %+v", capped)
	}
}
