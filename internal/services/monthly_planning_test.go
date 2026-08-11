package services

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
)

func TestMonthlyPlanningProgrammedContributionsMatchR3(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
	seedPlanningWorkspace(t, db, "ws-other", "personal", 0)
	base := planningBase(2026, time.August)

	insertPlanningBox(t, db, "box-a", "ws-plan", 10000, 0, 1, "ACTIVE")
	insertPlanningLedger(t, db, "ledger-a-current-month", "box-a", 10000)
	insertPlanningBox(t, db, "box-b", "ws-plan", 1000, 10000, 1, "ACTIVE")
	insertPlanningLedger(t, db, "ledger-b", "box-b", 9500)
	insertPlanningBox(t, db, "box-c", "ws-plan", 1000, 10000, 0, "ACTIVE")
	insertPlanningLedger(t, db, "ledger-c", "box-c", 9500)
	insertPlanningBox(t, db, "box-d", "ws-plan", 5000, 10000, 0, "BLOCKED")
	insertPlanningBox(t, db, "box-e", "ws-plan", 0, 10000, 0, "ACTIVE")
	insertPlanningBox(t, db, "box-other", "ws-other", 999999, 0, 1, "ACTIVE")

	snapshot, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-plan", base)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if snapshot.ProgrammedContributionsCents != 11500 {
		t.Fatalf("programmed contributions = %d, want 11500", snapshot.ProgrammedContributionsCents)
	}
}

func TestMonthlyPlanningManualReferencesNullZeroPriorityAndIsolation(t *testing.T) {
	tests := []struct {
		name          string
		references    MonthlyPlanningReferences
		wantIncome    MonthlyReferenceSource
		wantTotal     MonthlyReferenceSource
		wantEssential MonthlyReferenceSource
	}{
		{name: "all null", references: MonthlyPlanningReferences{}, wantIncome: MonthlyReferenceSourceUnavailable, wantTotal: MonthlyReferenceSourceUnavailable, wantEssential: MonthlyReferenceSourceUnavailable},
		{name: "only income", references: MonthlyPlanningReferences{IncomeCents: planningInt64(0)}, wantIncome: MonthlyReferenceSourceManual, wantTotal: MonthlyReferenceSourceUnavailable, wantEssential: MonthlyReferenceSourceUnavailable},
		{name: "only total", references: MonthlyPlanningReferences{TotalCostCents: planningInt64(0)}, wantIncome: MonthlyReferenceSourceUnavailable, wantTotal: MonthlyReferenceSourceManual, wantEssential: MonthlyReferenceSourceUnavailable},
		{name: "only essential", references: MonthlyPlanningReferences{EssentialCostCents: planningInt64(0)}, wantIncome: MonthlyReferenceSourceUnavailable, wantTotal: MonthlyReferenceSourceUnavailable, wantEssential: MonthlyReferenceSourceManual},
		{name: "all manual", references: MonthlyPlanningReferences{IncomeCents: planningInt64(500000), TotalCostCents: planningInt64(300000), EssentialCostCents: planningInt64(100000)}, wantIncome: MonthlyReferenceSourceManual, wantTotal: MonthlyReferenceSourceManual, wantEssential: MonthlyReferenceSourceManual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
			seedPlanningWorkspace(t, db, "ws-other", "personal", 0)
			if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-plan", tt.references); err != nil {
				t.Fatalf("update references: %v", err)
			}
			snapshot, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-plan", planningBase(2026, time.August))
			if err != nil {
				t.Fatalf("build snapshot: %v", err)
			}
			if snapshot.IncomeReferenceSource != tt.wantIncome || snapshot.TotalCostReferenceSource != tt.wantTotal || snapshot.EssentialCostReferenceSource != tt.wantEssential {
				t.Fatalf("sources = income:%s total:%s essential:%s", snapshot.IncomeReferenceSource, snapshot.TotalCostReferenceSource, snapshot.EssentialCostReferenceSource)
			}
			wantMargin := tt.wantIncome != MonthlyReferenceSourceUnavailable && tt.wantTotal != MonthlyReferenceSourceUnavailable
			if snapshot.MarginAvailable != wantMargin {
				t.Fatalf("margin availability = %v, want %v", snapshot.MarginAvailable, wantMargin)
			}
			other, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-other", planningBase(2026, time.August))
			if err != nil {
				t.Fatalf("build other snapshot: %v", err)
			}
			if other.IncomeReferenceSource != MonthlyReferenceSourceUnavailable || other.TotalCostReferenceSource != MonthlyReferenceSourceUnavailable || other.EssentialCostReferenceSource != MonthlyReferenceSourceUnavailable {
				t.Fatalf("manual references leaked to other workspace: %+v", other)
			}
		})
	}

	db := openDB(t)
	defer db.Close()
	seedPlanningWorkspace(t, db, "ws-negative", "personal", 0)
	if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-negative", MonthlyPlanningReferences{IncomeCents: planningInt64(-1)}); err == nil {
		t.Fatal("negative manual reference should fail")
	}
}

func TestMonthlyPlanningMarginAndCoverage(t *testing.T) {
	t.Run("positive margin and 6.8 months", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 680000)
		insertPlanningBox(t, db, "box-plan", "ws-plan", 100000, 0, 1, "ACTIVE")
		updatePlanningReferences(t, db, "ws-plan", 500000, 300000, 100000)
		snapshot := mustPlanningSnapshot(t, db, "ws-plan", planningBase(2026, time.August))
		if !snapshot.MarginAvailable || snapshot.MarginCents != 100000 {
			t.Fatalf("margin = available:%v cents:%d", snapshot.MarginAvailable, snapshot.MarginCents)
		}
		if !snapshot.CoverageAvailable || snapshot.CoverageTenths != 68 {
			t.Fatalf("coverage = available:%v tenths:%d", snapshot.CoverageAvailable, snapshot.CoverageTenths)
		}
	})

	t.Run("negative margin", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningBox(t, db, "box-plan", "ws-plan", 100000, 0, 1, "ACTIVE")
		updatePlanningReferences(t, db, "ws-plan", 300000, 300000, 100000)
		snapshot := mustPlanningSnapshot(t, db, "ws-plan", planningBase(2026, time.August))
		if !snapshot.MarginAvailable || snapshot.MarginCents != -100000 {
			t.Fatalf("margin = available:%v cents:%d", snapshot.MarginAvailable, snapshot.MarginCents)
		}
	})

	t.Run("negative available becomes zero coverage", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningBox(t, db, "box-reserved", "ws-plan", 0, 0, 1, "ACTIVE")
		insertPlanningLedger(t, db, "ledger-reserved", "box-reserved", 50000)
		if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-plan", MonthlyPlanningReferences{EssentialCostCents: planningInt64(100000)}); err != nil {
			t.Fatalf("update essential: %v", err)
		}
		snapshot := mustPlanningSnapshot(t, db, "ws-plan", planningBase(2026, time.August))
		if snapshot.AvailableBalanceCents != -50000 || !snapshot.CoverageAvailable || snapshot.CoverageTenths != 0 {
			t.Fatalf("negative available coverage: %+v", snapshot)
		}
	})

	t.Run("zero and absent essential are unavailable", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-zero", "personal", 100000)
		seedPlanningWorkspace(t, db, "ws-null", "personal", 100000)
		if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-zero", MonthlyPlanningReferences{EssentialCostCents: planningInt64(0)}); err != nil {
			t.Fatalf("update zero essential: %v", err)
		}
		if got := mustPlanningSnapshot(t, db, "ws-zero", planningBase(2026, time.August)); got.CoverageAvailable {
			t.Fatalf("zero essential produced coverage: %+v", got)
		}
		if got := mustPlanningSnapshot(t, db, "ws-null", planningBase(2026, time.August)); got.CoverageAvailable {
			t.Fatalf("absent essential produced coverage: %+v", got)
		}
	})
}

func TestMonthlyPlanningRecurringIncomeIsStructuralAndConservative(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
	seedPlanningWorkspace(t, db, "ws-other", "personal", 0)
	base := planningBase(2026, time.August)
	insertPlanningRecurringRule(t, db, "rule-a", "ws-plan", "INCOME", 100000, "MONTHLY", 1, "")
	insertPlanningRecurringRule(t, db, "rule-b", "ws-plan", "INCOME", 200000, "MONTHLY", 1, "")
	insertPlanningRecurringRule(t, db, "rule-inactive", "ws-plan", "INCOME", 900000, "MONTHLY", 0, "")
	insertPlanningRecurringRule(t, db, "rule-expense", "ws-plan", "EXPENSE", 900000, "MONTHLY", 1, "")
	insertPlanningRecurringRule(t, db, "rule-other", "ws-other", "INCOME", 900000, "MONTHLY", 1, "")
	insertPlanningRecurringRule(t, db, "rule-ended", "ws-plan", "INCOME", 900000, "MONTHLY", 1, "")
	exec(t, db, `UPDATE recurring_rules SET end_date_civil = '2026-07-31' WHERE id = 'rule-ended'`)
	insertPlanningRecurringRule(t, db, "rule-exhausted", "ws-plan", "INCOME", 900000, "MONTHLY", 1, "")
	exec(t, db, `UPDATE recurring_rules SET total_occurrences = 1 WHERE id = 'rule-exhausted'`)
	exec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active) VALUES ('series-versioned', 'ws-plan', 'user-ws-plan', 1)`)
	insertPlanningRecurringRule(t, db, "rule-version-old", "ws-plan", "INCOME", 900000, "MONTHLY", 0, "series-versioned")
	exec(t, db, `UPDATE recurring_rules SET effective_end_sequence = 1 WHERE id = 'rule-version-old'`)
	insertPlanningRecurringRule(t, db, "rule-version-leaf", "ws-plan", "INCOME", 50000, "MONTHLY", 1, "series-versioned")
	exec(t, db, `UPDATE recurring_rules SET parent_rule_id = 'rule-version-old', effective_start_sequence = 2, starts_at_civil = '2026-02-01' WHERE id = 'rule-version-leaf'`)
	exec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active) VALUES ('series-inactive', 'ws-plan', 'user-ws-plan', 0)`)
	insertPlanningRecurringRule(t, db, "rule-inactive-series", "ws-plan", "INCOME", 900000, "MONTHLY", 1, "series-inactive")
	insertPlanningTransaction(t, db, "occurrence-a", "ws-plan", "checking-ws-plan", "", "INCOME", 100000, "2026-08-05", "rule-a")

	snapshot := mustPlanningSnapshot(t, db, "ws-plan", base)
	if snapshot.IncomeReferenceSource != MonthlyReferenceSourceRecurring || snapshot.IncomeReferenceCents != 350000 {
		t.Fatalf("recurring income = source:%s cents:%d", snapshot.IncomeReferenceSource, snapshot.IncomeReferenceCents)
	}

	insertPlanningRecurringRule(t, db, "rule-weekly", "ws-plan", "INCOME", 10000, "WEEKLY", 1, "")
	snapshot = mustPlanningSnapshot(t, db, "ws-plan", base)
	if snapshot.IncomeReferenceSource != MonthlyReferenceSourceUnavailable {
		t.Fatalf("mixed non-monthly recurrence should be unavailable: %+v", snapshot)
	}

	if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-plan", MonthlyPlanningReferences{IncomeCents: planningInt64(700000)}); err != nil {
		t.Fatalf("set manual income: %v", err)
	}
	snapshot = mustPlanningSnapshot(t, db, "ws-plan", base)
	if snapshot.IncomeReferenceSource != MonthlyReferenceSourceManual || snapshot.IncomeReferenceCents != 700000 {
		t.Fatalf("manual income did not take priority: %+v", snapshot)
	}
}

func TestMonthlyPlanningRecurringIncomeFiltersIneligibleBeforeFrequency(t *testing.T) {
	base := planningBase(2026, time.August)

	t.Run("monthly valid and ended weekly", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningRecurringRule(t, db, "monthly-valid", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "")
		insertPlanningRecurringRule(t, db, "weekly-ended", "ws-plan", "INCOME", 9000, "WEEKLY", 1, "")
		exec(t, db, `UPDATE recurring_rules SET end_date_civil = '2026-07-31' WHERE id = 'weekly-ended'`)
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceRecurring, 1000)
	})

	t.Run("monthly valid and future weekly", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningRecurringRule(t, db, "monthly-valid", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "")
		insertPlanningRecurringRule(t, db, "weekly-future", "ws-plan", "INCOME", 9000, "WEEKLY", 1, "")
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceUnavailable, 0)
	})

	t.Run("monthly valid and exhausted annual", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningRecurringRule(t, db, "annual-exhausted", "ws-plan", "INCOME", 9000, "ANNUAL", 1, "")
		exec(t, db, `UPDATE recurring_rules SET total_occurrences = 1 WHERE id = 'annual-exhausted'`)
		insertPlanningRecurringRule(t, db, "monthly-valid", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "")
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceRecurring, 1000)
	})

	t.Run("only ended non-monthly", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningRecurringRule(t, db, "weekly-ended", "ws-plan", "INCOME", 9000, "WEEKLY", 1, "")
		exec(t, db, `UPDATE recurring_rules SET end_date_civil = '2026-07-31' WHERE id = 'weekly-ended'`)
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceUnavailable, 0)
	})

	t.Run("ended monthly and future monthly", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningRecurringRule(t, db, "monthly-ended", "ws-plan", "INCOME", 9000, "MONTHLY", 1, "")
		exec(t, db, `UPDATE recurring_rules SET end_date_civil = '2026-07-31' WHERE id = 'monthly-ended'`)
		insertPlanningRecurringRule(t, db, "monthly-valid", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "")
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceRecurring, 1000)
	})

	t.Run("materialized monthly occurrence does not exhaust series", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningRecurringRule(t, db, "monthly-valid", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "")
		insertPlanningTransaction(t, db, "monthly-current", "ws-plan", "checking-ws-plan", "", "INCOME", 1000, "2026-08-01", "monthly-valid")
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceRecurring, 1000)
	})

	t.Run("other workspace non-monthly does not interfere", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		seedPlanningWorkspace(t, db, "ws-other", "personal", 0)
		insertPlanningRecurringRule(t, db, "monthly-valid", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "")
		insertPlanningRecurringRule(t, db, "weekly-other", "ws-other", "INCOME", 9000, "WEEKLY", 1, "")
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceRecurring, 1000)
	})

	t.Run("inactive old non-monthly version does not interfere", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		exec(t, db, `INSERT INTO recurring_series (id, workspace_id, user_id, active) VALUES ('series-versioned', 'ws-plan', 'user-ws-plan', 1)`)
		insertPlanningRecurringRule(t, db, "annual-old", "ws-plan", "INCOME", 9000, "ANNUAL", 0, "series-versioned")
		exec(t, db, `UPDATE recurring_rules SET effective_end_sequence = 1 WHERE id = 'annual-old'`)
		insertPlanningRecurringRule(t, db, "monthly-leaf", "ws-plan", "INCOME", 1000, "MONTHLY", 1, "series-versioned")
		exec(t, db, `UPDATE recurring_rules SET parent_rule_id = 'annual-old', effective_start_sequence = 2, starts_at_civil = '2026-02-01' WHERE id = 'monthly-leaf'`)
		assertPlanningRecurringIncome(t, db, "ws-plan", base, MonthlyReferenceSourceRecurring, 1000)
	})
}

func TestMonthlyPlanningRecurringEligibilityRequiresFutureOccurrence(t *testing.T) {
	tests := []struct {
		name           string
		asOf           string
		startsAt       string
		frequency      string
		endDate        sql.NullString
		effectiveStart int64
		effectiveEnd   sql.NullInt64
		ruleTotal      sql.NullInt64
		seriesTotal    sql.NullInt64
		want           bool
	}{
		{name: "unbounded has next month", asOf: "2026-08-15", startsAt: "2026-01-20", frequency: "MONTHLY", effectiveStart: 1, want: true},
		{name: "finite monthly rule exhausted", asOf: "2026-08-15", startsAt: "2026-01-20", frequency: "MONTHLY", effectiveStart: 1, ruleTotal: sql.NullInt64{Int64: 7, Valid: true}, want: false},
		{name: "monthly end date before next occurrence", asOf: "2026-08-01", startsAt: "2026-01-20", frequency: "MONTHLY", endDate: sql.NullString{String: "2026-08-05", Valid: true}, effectiveStart: 1, want: false},
		{name: "monthly end date includes next occurrence", asOf: "2026-08-01", startsAt: "2026-01-20", frequency: "MONTHLY", endDate: sql.NullString{String: "2026-08-20", Valid: true}, effectiveStart: 1, want: true},
		{name: "future start is predictable", asOf: "2026-08-15", startsAt: "2026-09-20", frequency: "MONTHLY", effectiveStart: 1, want: true},
		{name: "closed version range", asOf: "2026-01-01", startsAt: "2026-01-20", frequency: "MONTHLY", effectiveStart: 2, effectiveEnd: sql.NullInt64{Int64: 1, Valid: true}, want: false},
		{name: "finite weekly rule exhausted", asOf: "2026-08-15", startsAt: "2026-01-01", frequency: "WEEKLY", effectiveStart: 1, ruleTotal: sql.NullInt64{Int64: 33, Valid: true}, want: false},
		{name: "weekly end before next occurrence", asOf: "2026-08-15", startsAt: "2026-01-01", frequency: "WEEKLY", endDate: sql.NullString{String: "2026-08-15", Valid: true}, effectiveStart: 1, want: false},
		{name: "annual next occurrence remains", asOf: "2026-08-15", startsAt: "2026-01-01", frequency: "ANNUAL", effectiveStart: 1, ruleTotal: sql.NullInt64{Int64: 2, Valid: true}, want: true},
		{name: "leap annual clamps canonically", asOf: "2025-02-28", startsAt: "2024-02-29", frequency: "ANNUAL", effectiveStart: 1, ruleTotal: sql.NullInt64{Int64: 2, Valid: true}, want: true},
		{name: "unknown frequency explicitly ended", asOf: "2026-08-15", startsAt: "2026-01-01", frequency: "UNKNOWN", endDate: sql.NullString{String: "2026-07-31", Valid: true}, effectiveStart: 1, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := recurringCandidateEligible(tt.asOf, tt.startsAt, tt.frequency, tt.endDate, tt.effectiveStart, tt.effectiveEnd, tt.ruleTotal, tt.seriesTotal)
			if err != nil {
				t.Fatalf("eligibility: %v", err)
			}
			if got != tt.want {
				t.Fatalf("eligible = %v, want %v", got, tt.want)
			}
		})
	}
}

func assertPlanningRecurringIncome(t *testing.T, db *sql.DB, workspaceID string, base time.Time, wantSource MonthlyReferenceSource, wantCents int64) {
	t.Helper()
	snapshot := mustPlanningSnapshot(t, db, workspaceID, base)
	if snapshot.IncomeReferenceSource != wantSource || snapshot.IncomeReferenceCents != wantCents {
		t.Fatalf("recurring income = source:%s cents:%d, want source:%s cents:%d", snapshot.IncomeReferenceSource, snapshot.IncomeReferenceCents, wantSource, wantCents)
	}
}

func TestMonthlyPlanningHistoricalCostsUseSixCompleteMonthsAndCanonicalExclusions(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedPlanningWorkspace(t, db, "ws-plan", "personal", 680000)
	seedPlanningWorkspace(t, db, "ws-other", "personal", 0)
	for month := 1; month <= 6; month++ {
		date := planningCivil(2026, time.Month(month), 10)
		insertPlanningTransaction(t, db, "essential-"+date[:7], "ws-plan", "checking-ws-plan", "essential-ws-plan", "EXPENSE", 10000, date, "")
		insertPlanningTransaction(t, db, "lifestyle-"+date[:7], "ws-plan", "checking-ws-plan", "lifestyle-ws-plan", "EXPENSE", 5000, date, "")
	}
	insertPlanningTransaction(t, db, "card-purchase", "ws-plan", "card-ws-plan", "essential-ws-plan", "EXPENSE", 6000, "2026-03-12", "")
	insertPlanningTransaction(t, db, "current-incomplete", "ws-plan", "checking-ws-plan", "essential-ws-plan", "EXPENSE", 999000, "2026-07-01", "")
	insertPlanningTransaction(t, db, "transfer-excluded", "ws-plan", "checking-ws-plan", "", "TRANSFER", 999000, "2026-04-05", "")
	insertPlanningTransaction(t, db, "other-workspace", "ws-other", "checking-ws-other", "essential-ws-other", "EXPENSE", 999000, "2026-03-05", "")
	insertPlanningInvoicePaymentTransaction(t, db, "ws-plan", "2026-05-05", 777000)

	snapshot := mustPlanningSnapshot(t, db, "ws-plan", planningBase(2026, time.July))
	if snapshot.HistoricalMonths != 6 {
		t.Fatalf("historical months = %d, want 6", snapshot.HistoricalMonths)
	}
	if snapshot.TotalCostReferenceSource != MonthlyReferenceSourceHistorical || snapshot.TotalCostReferenceCents != 16000 {
		t.Fatalf("total historical = source:%s cents:%d", snapshot.TotalCostReferenceSource, snapshot.TotalCostReferenceCents)
	}
	if snapshot.EssentialCostReferenceSource != MonthlyReferenceSourceHistorical || snapshot.EssentialCostReferenceCents != 11000 {
		t.Fatalf("essential historical = source:%s cents:%d", snapshot.EssentialCostReferenceSource, snapshot.EssentialCostReferenceCents)
	}

	// A later reversal must not turn the structurally linked May payment into a
	// historical operating cost.
	insertPlanningTransaction(t, db, "invoice-payment-reversal-tx", "ws-plan", "checking-ws-plan", "", "INCOME", 777000, "2026-08-05", "")
	reversedAt := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-08-05"))
	exec(t, db, `
		UPDATE invoice_payments
		SET reversed_at = ?, reversal_transaction_id = 'invoice-payment-reversal-tx'
		WHERE id = 'invoice-payment-plan' AND workspace_id = 'ws-plan'
	`, reversedAt)

	afterReversal := mustPlanningSnapshot(t, db, "ws-plan", planningBase(2026, time.July))
	if afterReversal.HistoricalMonths != 6 || afterReversal.TotalCostReferenceCents != 16000 || afterReversal.EssentialCostReferenceCents != 11000 {
		t.Fatalf("historical costs changed after later invoice-payment reversal: %+v", afterReversal)
	}
}

func TestMonthlyPlanningHistoricalMinimumYearTurnAndBusinessPolicy(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	seedPlanningWorkspace(t, db, "ws-three", "personal", 0)
	seedPlanningWorkspace(t, db, "ws-two", "personal", 0)
	seedPlanningWorkspace(t, db, "ws-business", "business", 0)
	for i, date := range []string{"2023-12-10", "2024-01-10", "2024-02-29"} {
		amount := int64((i + 1) * 10000)
		insertPlanningTransaction(t, db, "three-"+date, "ws-three", "checking-ws-three", "essential-ws-three", "EXPENSE", amount, date, "")
		insertPlanningTransaction(t, db, "business-"+date, "ws-business", "checking-ws-business", "essential-ws-business", "EXPENSE", amount, date, "")
		if i < 2 {
			insertPlanningTransaction(t, db, "two-"+date, "ws-two", "checking-ws-two", "essential-ws-two", "EXPENSE", amount, date, "")
		}
	}
	insertPlanningTransaction(t, db, "current-excluded", "ws-three", "checking-ws-three", "essential-ws-three", "EXPENSE", 900000, "2024-03-01", "")
	base := planningBase(2024, time.March)

	three := mustPlanningSnapshot(t, db, "ws-three", base)
	if three.HistoricalMonths != 3 || three.TotalCostReferenceCents != 20000 || three.EssentialCostReferenceCents != 20000 {
		t.Fatalf("three-month history: %+v", three)
	}
	two := mustPlanningSnapshot(t, db, "ws-two", base)
	if two.HistoricalMonths != 2 || two.TotalCostReferenceSource != MonthlyReferenceSourceUnavailable || two.EssentialCostReferenceSource != MonthlyReferenceSourceUnavailable {
		t.Fatalf("two-month history should be unavailable: %+v", two)
	}
	business := mustPlanningSnapshot(t, db, "ws-business", base)
	if business.TotalCostReferenceSource != MonthlyReferenceSourceHistorical || business.TotalCostReferenceCents != 20000 {
		t.Fatalf("business total historical: %+v", business)
	}
	if business.EssentialCostReferenceSource != MonthlyReferenceSourceUnavailable {
		t.Fatalf("business inferred essential cost: %+v", business)
	}
	if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-business", MonthlyPlanningReferences{EssentialCostCents: planningInt64(15000)}); err != nil {
		t.Fatalf("set business essential: %v", err)
	}
	business = mustPlanningSnapshot(t, db, "ws-business", base)
	if business.EssentialCostReferenceSource != MonthlyReferenceSourceManual || business.EssentialCostReferenceCents != 15000 {
		t.Fatalf("business manual essential: %+v", business)
	}
}

func TestMonthlyPlanningOverflowFailsExplicitly(t *testing.T) {
	t.Run("available balance", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", math.MaxInt64)
		insertPlanningBox(t, db, "box-reserved", "ws-plan", 0, 0, 1, "ACTIVE")
		exec(t, db, `
			INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date)
			VALUES ('ledger-reserved', 'box-reserved', ?, 'RELEASE', 'Overflow fixture', 1)
		`, int64(math.MinInt64))
		if _, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-plan", planningBase(2026, time.August)); err == nil {
			t.Fatal("available balance overflow should fail")
		}
	})

	t.Run("margin", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", 0)
		insertPlanningBox(t, db, "box-overflow", "ws-plan", math.MaxInt64, 0, 1, "ACTIVE")
		if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-plan", MonthlyPlanningReferences{IncomeCents: planningInt64(0), TotalCostCents: planningInt64(math.MaxInt64)}); err != nil {
			t.Fatalf("set references: %v", err)
		}
		if _, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-plan", planningBase(2026, time.August)); err == nil {
			t.Fatal("margin underflow should fail")
		}
	})

	t.Run("coverage", func(t *testing.T) {
		db := openDB(t)
		defer db.Close()
		seedPlanningWorkspace(t, db, "ws-plan", "personal", math.MaxInt64)
		if err := UpdateMonthlyPlanningReferences(context.Background(), db, "ws-plan", MonthlyPlanningReferences{EssentialCostCents: planningInt64(1)}); err != nil {
			t.Fatalf("set references: %v", err)
		}
		if _, err := BuildMonthlyPlanningSnapshot(context.Background(), db, "ws-plan", planningBase(2026, time.August)); err == nil {
			t.Fatal("coverage overflow should fail")
		}
	})
}

func TestMonthlyPlanningCoverageRoundsToNearestTenth(t *testing.T) {
	tests := []struct {
		available int64
		essential int64
		want      int64
	}{
		{available: 680000, essential: 100000, want: 68},
		{available: 2, essential: 3, want: 7},
		{available: 1, essential: 6, want: 2},
		{available: -1, essential: 100, want: 0},
	}
	for _, tt := range tests {
		got, err := coverageInTenths(tt.available, tt.essential)
		if err != nil {
			t.Fatalf("coverage %d/%d: %v", tt.available, tt.essential, err)
		}
		if got != tt.want {
			t.Fatalf("coverage %d/%d = %d, want %d", tt.available, tt.essential, got, tt.want)
		}
	}
}

func seedPlanningWorkspace(t *testing.T, db *sql.DB, workspaceID, workspaceType string, balance int64) {
	t.Helper()
	exec(t, db, `INSERT INTO workspaces (id, name, type) VALUES (?, ?, ?)`, workspaceID, workspaceID, workspaceType)
	exec(t, db, `INSERT INTO users (id, name, email, password_hash) VALUES (?, ?, ?, 'hash')`, "user-"+workspaceID, workspaceID, workspaceID+"@example.test")
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, current_balance) VALUES (?, ?, 'Checking', 'CHECKING', ?)`, "checking-"+workspaceID, workspaceID, balance)
	exec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, current_balance) VALUES (?, ?, 'Card', 'CREDIT_CARD', 0)`, "card-"+workspaceID, workspaceID)
	exec(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group) VALUES (?, ?, 'Essential', 'EXPENSE', 'Essencial')`, "essential-"+workspaceID, workspaceID)
	exec(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group) VALUES (?, ?, 'Lifestyle', 'EXPENSE', 'Estilo de Vida')`, "lifestyle-"+workspaceID, workspaceID)
}

func insertPlanningBox(t *testing.T, db *sql.DB, boxID, workspaceID string, monthly, target, continueAfter int64, state string) {
	t.Helper()
	exec(t, db, `
		INSERT INTO boxes (id, workspace_id, name, target_amount, monthly_recharge, continue_after_target, automatic_contribution_state)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, boxID, workspaceID, boxID, target, monthly, continueAfter, state)
}

func insertPlanningLedger(t *testing.T, db *sql.DB, ledgerID, boxID string, amount int64) {
	t.Helper()
	exec(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date)
		VALUES (?, ?, ?, 'RECHARGE', 'Planning fixture', 1)
	`, ledgerID, boxID, amount)
}

func insertPlanningRecurringRule(t *testing.T, db *sql.DB, ruleID, workspaceID, ruleType string, amount int64, frequency string, active int, seriesID string) {
	t.Helper()
	var series any
	if seriesID != "" {
		series = seriesID
	}
	exec(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, start_date_civil, frequency, active, series_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '2026-01-01', ?, ?, ?)
	`, ruleID, workspaceID, "user-"+workspaceID, "checking-"+workspaceID, ruleType, amount, ruleID, civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-01-01")), frequency, active, series)
}

func insertPlanningTransaction(t *testing.T, db *sql.DB, txID, workspaceID, accountID, categoryID, txType string, amount int64, dateCivil, recurringRuleID string) {
	t.Helper()
	var category, rule any
	if categoryID != "" {
		category = categoryID
	}
	if recurringRuleID != "" {
		rule = recurringRuleID
	}
	date := civildate.MustCivilDate(dateCivil)
	exec(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			date, date_civil, description, status, recurring_rule_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'paid', ?)
	`, txID, workspaceID, "user-"+workspaceID, accountID, category, txType, amount, civildate.CivilDateToLegacyUnixNoonUTC(date), dateCivil, txID, rule)
}

func insertPlanningInvoicePaymentTransaction(t *testing.T, db *sql.DB, workspaceID, dateCivil string, amount int64) {
	t.Helper()
	insertPlanningTransaction(t, db, "invoice-payment-tx", workspaceID, "checking-"+workspaceID, "essential-"+workspaceID, "EXPENSE", amount, dateCivil, "")
	date := civildate.MustCivilDate(dateCivil)
	dateUnix := civildate.CivilDateToLegacyUnixNoonUTC(date)
	exec(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date) VALUES ('invoice-plan', ?, '2026-05', ?, ?)`, "card-"+workspaceID, dateUnix, dateUnix)
	exec(t, db, `
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at
		) VALUES ('invoice-payment-plan', ?, 'invoice-plan', ?, 'invoice-payment-tx', ?, ?, 'manual', ?)
	`, workspaceID, "checking-"+workspaceID, amount, dateUnix, dateUnix)
}

func updatePlanningReferences(t *testing.T, db *sql.DB, workspaceID string, income, total, essential int64) {
	t.Helper()
	if err := UpdateMonthlyPlanningReferences(context.Background(), db, workspaceID, MonthlyPlanningReferences{
		IncomeCents:        planningInt64(income),
		TotalCostCents:     planningInt64(total),
		EssentialCostCents: planningInt64(essential),
	}); err != nil {
		t.Fatalf("update planning references: %v", err)
	}
}

func mustPlanningSnapshot(t *testing.T, db *sql.DB, workspaceID string, base time.Time) MonthlyPlanningSnapshot {
	t.Helper()
	snapshot, err := BuildMonthlyPlanningSnapshot(context.Background(), db, workspaceID, base)
	if err != nil {
		t.Fatalf("build monthly planning snapshot: %v", err)
	}
	return snapshot
}

func planningBase(year int, month time.Month) time.Time {
	location, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		panic(err)
	}
	return time.Date(year, month, 15, 12, 0, 0, 0, location)
}

func planningCivil(year int, month time.Month, day int) string {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func planningInt64(value int64) *int64 {
	return &value
}
