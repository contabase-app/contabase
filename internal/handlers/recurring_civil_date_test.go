package handlers

import (
	"fmt"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

func TestRecurringCivilDateFromOffsetDailyAndWeekly(t *testing.T) {
	anchor := MustCivilWriteDate("2026-07-12")
	tests := []struct {
		name, freq string
		offset     int64
		want       string
	}{
		{"daily 0", models.RecurrenceFrequencyDaily, 0, "2026-07-12"},
		{"daily 1", models.RecurrenceFrequencyDaily, 1, "2026-07-13"},
		{"daily 31", models.RecurrenceFrequencyDaily, 31, "2026-08-12"},
		{"weekly 1", models.RecurrenceFrequencyWeekly, 1, "2026-07-19"},
		{"biweekly 1", models.RecurrenceFrequencyBiweekly, 1, "2026-07-26"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recurrenceCivilDateFromOffset(anchor, tc.freq, tc.offset)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got.Civil() != tc.want {
				t.Fatalf("%q", got.Civil())
			}
			wantLegacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate(tc.want))
			if got.LegacyUnix() != wantLegacy {
				t.Fatalf("legacy %d want %d", got.LegacyUnix(), wantLegacy)
			}
		})
	}
}

func TestRecurringCivilDateFromOffsetMonthlyAnchored(t *testing.T) {
	anchor := MustCivilWriteDate("2026-01-31")
	tests := []struct {
		name string
		freq string
		offs int64
		want string
	}{
		{"Jan31+0", models.RecurrenceFrequencyMonthly, 0, "2026-01-31"},
		{"Jan31+1m→Feb28", models.RecurrenceFrequencyMonthly, 1, "2026-02-28"},
		{"Jan31+2m→Mar31", models.RecurrenceFrequencyMonthly, 2, "2026-03-31"},
		{"Jan31+3m→Apr30", models.RecurrenceFrequencyMonthly, 3, "2026-04-30"},
		{"BIMONTHLY+1=+2m", models.RecurrenceFrequencyBimonthly, 1, "2026-03-31"},
		{"QUARTERLY+1=+3m", models.RecurrenceFrequencyQuarterly, 1, "2026-04-30"},
		{"SEMIANNUAL+1=+6m", models.RecurrenceFrequencySemiannual, 1, "2026-07-31"},
		{"ANNUAL+1=+12m", models.RecurrenceFrequencyAnnual, 1, "2027-01-31"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recurrenceCivilDateFromOffset(anchor, tc.freq, tc.offs)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got.Civil() != tc.want {
				t.Fatalf("got %q want %q", got.Civil(), tc.want)
			}
			wantLegacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate(tc.want))
			if got.LegacyUnix() != wantLegacy {
				t.Fatalf("legacy %d want %d", got.LegacyUnix(), wantLegacy)
			}
		})
	}
}

func TestRecurringCivilDateFromOffsetLeapYear(t *testing.T) {
	anchor := MustCivilWriteDate("2024-01-31")
	tests := []struct {
		name string
		offs int64
		want string
	}{
		{"leap Feb29", 1, "2024-02-29"},
		{"Mar31 after leap", 2, "2024-03-31"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recurrenceCivilDateFromOffset(anchor, models.RecurrenceFrequencyMonthly, tc.offs)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got.Civil() != tc.want {
				t.Fatalf("got %q want %q", got.Civil(), tc.want)
			}
		})
	}

	// Feb 29 + 12 months = Feb 28 (non-leap year clamp).
	feb29 := MustCivilWriteDate("2024-02-29")
	got, err := recurrenceCivilDateFromOffset(feb29, models.RecurrenceFrequencyAnnual, 1)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got.Civil() != "2025-02-28" {
		t.Fatalf("Feb29+12m = %q, want 2025-02-28", got.Civil())
	}
}

func TestRecurringCivilDateFromOffsetYearCross(t *testing.T) {
	anchor := MustCivilWriteDate("2025-12-15")
	got, _ := recurrenceCivilDateFromOffset(anchor, models.RecurrenceFrequencyMonthly, 1)
	if got.Civil() != "2026-01-15" {
		t.Fatalf("got %q", got.Civil())
	}
}

func TestRecurringCivilDateFromOffsetErrors(t *testing.T) {
	anchor := MustCivilWriteDate("2026-01-01")
	if _, err := recurrenceCivilDateFromOffset(anchor, models.RecurrenceFrequencyMonthly, -1); err == nil {
		t.Fatal("negative offset accepted")
	}
	if _, err := recurrenceCivilDateFromOffset(anchor, "INVALID", 0); err == nil {
		t.Fatal("invalid frequency accepted")
	}
}

func TestRecurringCivilDateDoesNotMutateAnchor(t *testing.T) {
	anchor := MustCivilWriteDate("2026-01-31")
	before := anchor.Civil()
	recurrenceCivilDateFromOffset(anchor, models.RecurrenceFrequencyMonthly, 5)
	if anchor.Civil() != before {
		t.Fatal("anchor mutated")
	}
}

func TestCivilDateDaysBetween(t *testing.T) {
	tests := []struct {
		start, end string
		want       int64
	}{
		{"2026-07-12", "2026-07-12", 0},
		{"2026-07-12", "2026-07-13", 1},
		{"2026-07-13", "2026-07-12", -1},
		{"2026-01-31", "2026-02-28", 28},
		{"2024-01-31", "2024-02-29", 29},
		{"2025-12-31", "2026-01-01", 1},
		{"2026-01-01", "2025-12-31", -1},
	}
	for _, tc := range tests {
		name := fmt.Sprintf("%s→%s", tc.start, tc.end)
		t.Run(name, func(t *testing.T) {
			start := civildate.MustCivilDate(tc.start)
			end := civildate.MustCivilDate(tc.end)
			got := civildate.CivilDateDaysBetween(start, end)
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestRecurringCivilHorizon(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	now := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	got, err := recurrenceCivilHorizon(now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got.Civil() != "2027-10-12" {
		t.Fatalf("got %q want 2027-10-12 (15 months from now in SP)", got.Civil())
	}
	wantLegacy := time.Date(2027, 10, 12, 12, 0, 0, 0, time.UTC).Unix()
	if got.LegacyUnix() != wantLegacy {
		t.Fatalf("legacy %d want %d", got.LegacyUnix(), wantLegacy)
	}
}

func TestRecurringCivilHorizonNearMidnightSP(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	// 02:00 UTC = 23:00 SP (previous day). Horizon should be SP day + 15 months.
	now := time.Date(2026, 7, 12, 2, 0, 0, 0, time.UTC)
	got, err := recurrenceCivilHorizon(now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got.Civil() != "2027-10-11" {
		t.Fatalf("got %q want 2027-10-11 (SP day July 11 + 15 months)", got.Civil())
	}
}

func TestRecurringCivilHorizonEnvironmentIndependence(t *testing.T) {
	t.Setenv("TZ", "UTC")
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	now := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	got, err := recurrenceCivilHorizon(now)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got.Civil() != "2027-10-12" {
		t.Fatalf("got %q", got.Civil())
	}
}

func TestAddDaysCoherentPair(t *testing.T) {
	anchor := MustCivilWriteDate("2026-07-12")
	got, err := anchor.AddDays(15)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got.Civil() != "2026-07-27" {
		t.Fatalf("got %q", got.Civil())
	}
	wantLegacy := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC).Unix()
	if got.LegacyUnix() != wantLegacy {
		t.Fatalf("legacy %d want %d", got.LegacyUnix(), wantLegacy)
	}
}

func TestEnvironmentIndependence(t *testing.T) {
	t.Setenv("TZ", "UTC")
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	anchor := MustCivilWriteDate("2026-07-12")
	got, _ := recurrenceCivilDateFromOffset(anchor, models.RecurrenceFrequencyMonthly, 1)
	if got.Civil() != "2026-08-12" {
		t.Fatalf("got %q", got.Civil())
	}
	wantLegacy := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC).Unix()
	if got.LegacyUnix() != wantLegacy {
		t.Fatalf("legacy %d want %d", got.LegacyUnix(), wantLegacy)
	}
}
