// Package civildate models financial calendar dates without attaching an
// instant or timezone to their persisted representation.
package civildate

import (
	"fmt"
	"time"
)

// CivilDate represents a financial calendar date without a clock, offset, or
// timezone. Optional civil dates must be modeled outside this value (for
// example, with a pointer or sql.NullString); an empty string is never valid.
type CivilDate struct {
	year  int
	month time.Month
	day   int
}

// ParseCivilDateInput validates an exact ISO calendar date (YYYY-MM-DD).
// It intentionally does not use time.Parse, because a financial date is not
// an instant and must not acquire a timezone during parsing.
func ParseCivilDateInput(value string) (CivilDate, error) {
	if len(value) != len("2006-01-02") || value[4] != '-' || value[7] != '-' {
		return CivilDate{}, fmt.Errorf("data civil inválida: esperado YYYY-MM-DD")
	}

	year, ok := parseCivilDateDigits(value[0:4])
	if !ok || year < 1 {
		return CivilDate{}, fmt.Errorf("data civil inválida: ano")
	}
	month, ok := parseCivilDateDigits(value[5:7])
	if !ok || month < 1 || month > 12 {
		return CivilDate{}, fmt.Errorf("data civil inválida: mês")
	}
	day, ok := parseCivilDateDigits(value[8:10])
	if !ok || day < 1 || day > 31 {
		return CivilDate{}, fmt.Errorf("data civil inválida: dia")
	}

	date := CivilDate{year: year, month: time.Month(month), day: day}
	if !date.isValid() {
		return CivilDate{}, fmt.Errorf("data civil inválida: %s", value)
	}
	return date, nil
}

// MustCivilDate is ParseCivilDateInput for fixed, programmer-supplied dates.
func MustCivilDate(value string) CivilDate {
	date, err := ParseCivilDateInput(value)
	if err != nil {
		panic(err)
	}
	return date
}

// FormatCivilDate serializes a valid CivilDate as YYYY-MM-DD.
func FormatCivilDate(date CivilDate) string {
	date.mustBeValid()
	return fmt.Sprintf("%04d-%02d-%02d", date.year, date.month, date.day)
}

func (date CivilDate) String() string {
	return FormatCivilDate(date)
}

// CivilDateToday returns today's calendar date in the explicitly supplied
// financial timezone. It never falls back to time.Local.
func CivilDateToday(location *time.Location) CivilDate {
	return CivilDateAt(time.Now(), location)
}

// CivilDateAt returns the civil calendar date of an explicit instant in an
// explicit timezone. It is primarily useful for deterministic tests and
// divergence reports; persisted civil dates remain timezone-free strings.
func CivilDateAt(now time.Time, location *time.Location) CivilDate {
	if location == nil {
		panic("timezone financeira obrigatória")
	}
	local := now.In(location)
	return MustCivilDate(fmt.Sprintf("%04d-%02d-%02d", local.Year(), local.Month(), local.Day()))
}

// CivilDateAddDays applies calendar-day arithmetic without persisting an
// instant. A result outside the supported four-digit ISO year range fails.
func CivilDateAddDays(date CivilDate, days int) (CivilDate, error) {
	date.mustBeValid()
	shifted := time.Date(date.year, date.month, date.day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
	return civilDateFromParts(shifted.Year(), shifted.Month(), shifted.Day())
}

// CivilDateDaysBetween returns the signed number of civil calendar days
// between two valid dates: positive when end is after start, negative
// when end is before start, zero when they represent the same day.
// The function is pure — it never converts to Unix or consults a timezone.
func CivilDateDaysBetween(start, end CivilDate) int64 {
	start.mustBeValid()
	end.mustBeValid()
	julianStart := civilJulianDayNumber(start.year, int(start.month), start.day)
	julianEnd := civilJulianDayNumber(end.year, int(end.month), end.day)
	return int64(julianEnd - julianStart)
}

// civilJulianDayNumber converts a civil date to a Gregorian Julian Day Number.
// Algorithm from Fliegel & Van Flandern (1968), integer-only, no timezone.
func civilJulianDayNumber(year, month, day int) int {
	// Adjust January/February to the previous year for the formula.
	if month <= 2 {
		year--
		month += 12
	}
	return (1461*(year+4800))/4 + (367*(month-2))/12 - (3*((year+4900)/100))/4 + day - 32075
}

// CivilDateAddMonths applies clamped calendar-month arithmetic: 31 January +
// one month is 28 February (or 29 in a leap year).
func CivilDateAddMonths(date CivilDate, months int) (CivilDate, error) {
	date.mustBeValid()
	targetMonthIndex := (date.year-1)*12 + int(date.month) - 1 + months
	if targetMonthIndex < 0 {
		return CivilDate{}, fmt.Errorf("resultado de data civil fora do intervalo ISO")
	}
	targetYear := targetMonthIndex/12 + 1
	targetMonth := time.Month(targetMonthIndex%12 + 1)
	if targetYear < 1 || targetYear > 9999 {
		return CivilDate{}, fmt.Errorf("resultado de data civil fora do intervalo ISO")
	}

	lastDay := daysInCivilMonth(targetYear, targetMonth)
	targetDay := date.day
	if targetDay > lastDay {
		targetDay = lastDay
	}
	return civilDateFromParts(targetYear, targetMonth, targetDay)
}

// CivilMonthBounds returns the inclusive month start and exclusive next-month
// start as civil dates.
func CivilMonthBounds(year int, month time.Month) (CivilDate, CivilDate, error) {
	start, err := civilDateFromParts(year, month, 1)
	if err != nil {
		return CivilDate{}, CivilDate{}, err
	}
	next, err := CivilDateAddMonths(start, 1)
	if err != nil {
		return CivilDate{}, CivilDate{}, err
	}
	return start, next, nil
}

// CompareCivilDate returns -1, 0, or 1 according to calendar order.
func CompareCivilDate(left, right CivilDate) int {
	left.mustBeValid()
	right.mustBeValid()
	if left.year != right.year {
		return compareCivilDateInt(left.year, right.year)
	}
	if left.month != right.month {
		return compareCivilDateInt(int(left.month), int(right.month))
	}
	return compareCivilDateInt(left.day, right.day)
}

// LegacyUnixToCivilDateUTC preserves the legacy UTC calendar label for a Unix
// value. It exists only for controlled backfill and characterization work.
func LegacyUnixToCivilDateUTC(timestamp int64) CivilDate {
	instant := time.Unix(timestamp, 0).UTC()
	return MustCivilDate(fmt.Sprintf("%04d-%02d-%02d", instant.Year(), instant.Month(), instant.Day()))
}

// LegacyUnixToCivilDateInLocation exposes how a legacy Unix value appears in a
// supplied location. It is for divergence reporting, not normal persistence.
func LegacyUnixToCivilDateInLocation(timestamp int64, location *time.Location) (CivilDate, error) {
	if location == nil {
		return CivilDate{}, fmt.Errorf("timezone financeira obrigatória")
	}
	instant := time.Unix(timestamp, 0).In(location)
	return civilDateFromParts(instant.Year(), instant.Month(), instant.Day())
}

// CivilDateToLegacyUnixNoonUTC is a temporary bridge for legacy INTEGER
// readers. New code must persist the CivilDate string as the source of truth.
func CivilDateToLegacyUnixNoonUTC(date CivilDate) int64 {
	date.mustBeValid()
	return time.Date(date.year, date.month, date.day, 12, 0, 0, 0, time.UTC).Unix()
}

func civilDateFromParts(year int, month time.Month, day int) (CivilDate, error) {
	date := CivilDate{year: year, month: month, day: day}
	if !date.isValid() {
		return CivilDate{}, fmt.Errorf("data civil inválida: %04d-%02d-%02d", year, month, day)
	}
	return date, nil
}

func (date CivilDate) isValid() bool {
	if date.year < 1 || date.year > 9999 || date.month < time.January || date.month > time.December || date.day < 1 {
		return false
	}
	return date.day <= daysInCivilMonth(date.year, date.month)
}

func (date CivilDate) mustBeValid() {
	if !date.isValid() {
		panic("CivilDate inválida")
	}
}

func daysInCivilMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func parseCivilDateDigits(value string) (int, bool) {
	parsed := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
		parsed = parsed*10 + int(value[i]-'0')
	}
	return parsed, true
}

func compareCivilDateInt(left, right int) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
