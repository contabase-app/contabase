package handlers

import (
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
)

// CivilDate remains exported from handlers for compatibility with the Phase 1
// contract. Its implementation lives in internal/civildate so controlled
// database maintenance can reuse the same conversions without an import cycle.
type CivilDate = civildate.CivilDate

func ParseCivilDateInput(value string) (CivilDate, error) {
	return civildate.ParseCivilDateInput(value)
}

func MustCivilDate(value string) CivilDate {
	return civildate.MustCivilDate(value)
}

func FormatCivilDate(date CivilDate) string {
	return civildate.FormatCivilDate(date)
}

func CivilDateToday(location *time.Location) CivilDate {
	return civildate.CivilDateToday(location)
}

func civilDateAt(now time.Time, location *time.Location) CivilDate {
	return civildate.CivilDateAt(now, location)
}

func CivilDateAddDays(date CivilDate, days int) (CivilDate, error) {
	return civildate.CivilDateAddDays(date, days)
}

func CivilDateAddMonths(date CivilDate, months int) (CivilDate, error) {
	return civildate.CivilDateAddMonths(date, months)
}

func CivilMonthBounds(year int, month time.Month) (CivilDate, CivilDate, error) {
	return civildate.CivilMonthBounds(year, month)
}

func CompareCivilDate(left, right CivilDate) int {
	return civildate.CompareCivilDate(left, right)
}

func LegacyUnixToCivilDateUTC(timestamp int64) CivilDate {
	return civildate.LegacyUnixToCivilDateUTC(timestamp)
}

func LegacyUnixToCivilDateInLocation(timestamp int64, location *time.Location) (CivilDate, error) {
	return civildate.LegacyUnixToCivilDateInLocation(timestamp, location)
}

func CivilDateToLegacyUnixNoonUTC(date CivilDate) int64 {
	return civildate.CivilDateToLegacyUnixNoonUTC(date)
}
