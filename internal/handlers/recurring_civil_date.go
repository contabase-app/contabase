package handlers

import (
	"fmt"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

// recurrenceCivilDateFromOffset computes the civil occurrence date from an
// anchor date, a standard recurrence frequency, and an offset count.
//
// offset 0 returns the anchor. offset must be non-negative.
// Unknown frequency or negative offset returns an error.
//
// Monthly-and-longer frequencies are anchored: every occurrence is computed
// from the original anchor + total month offset, never chained. This
// guarantees that 2026-01-31 MONTHLY offset 2 yields 2026-03-31 (not 2026-03-28).
func recurrenceCivilDateFromOffset(
	anchor CivilWriteDate,
	frequency string,
	offset int64,
) (CivilWriteDate, error) {
	if offset < 0 {
		return CivilWriteDate{}, fmt.Errorf("recurrence offset must be non-negative, got %d", offset)
	}
	var err error
	result := anchor
	switch frequency {
	case models.RecurrenceFrequencyDaily:
		result, err = anchor.AddDays(int(offset * 1))
	case models.RecurrenceFrequencyWeekly:
		result, err = anchor.AddDays(int(offset * 7))
	case models.RecurrenceFrequencyBiweekly:
		result, err = anchor.AddDays(int(offset * 14))
	case models.RecurrenceFrequencyMonthly:
		result, err = anchor.AddMonths(int(offset * 1))
	case models.RecurrenceFrequencyBimonthly:
		result, err = anchor.AddMonths(int(offset * 2))
	case models.RecurrenceFrequencyQuarterly:
		result, err = anchor.AddMonths(int(offset * 3))
	case models.RecurrenceFrequencySemiannual:
		result, err = anchor.AddMonths(int(offset * 6))
	case models.RecurrenceFrequencyAnnual:
		result, err = anchor.AddMonths(int(offset * 12))
	default:
		return CivilWriteDate{}, fmt.Errorf("unknown recurrence frequency: %q", frequency)
	}
	if err != nil {
		return CivilWriteDate{}, fmt.Errorf("recurrence offset %d for %q: %w", offset, frequency, err)
	}
	return result, nil
}

// recurrenceCivilHorizon returns today's civil date in the application timezone
// plus the fixed materialization horizon (15 months). The result is always
// a CivilWriteDate with its legacy INTEGER at 12:00 UTC.
//
// This function reads the application timezone (appTimeLocation) and never
// consults time.Local. The horizon period is hard-wired to 15 months and is
// shared by projection, materialization, generated_until and materialized_until.
func recurrenceCivilHorizon(now time.Time) (CivilWriteDate, error) {
	loc := appTimeLocation()
	local := now.In(loc)
	dateStr := fmt.Sprintf("%04d-%02d-%02d", local.Year(), local.Month(), local.Day())
	anchor, err := NewCivilWriteDate(dateStr)
	if err != nil {
		return CivilWriteDate{}, fmt.Errorf("recurrence horizon: %w", err)
	}
	horizon, err := anchor.AddMonths(recurringMaterializationHorizonMonths)
	if err != nil {
		return CivilWriteDate{}, fmt.Errorf("recurrence horizon: add %d months: %w", recurringMaterializationHorizonMonths, err)
	}
	return horizon, nil
}
