package handlers

import (
	"testing"
	"time"
)

func TestCivilDateSaoPauloPreservesCalendarInput(t *testing.T) {
	location := mustCivilDateLocation(t, "America/Sao_Paulo")
	date := MustCivilDate("2026-07-12")

	got, err := LegacyUnixToCivilDateInLocation(CivilDateToLegacyUnixNoonUTC(date), location)
	if err != nil {
		t.Fatalf("legacy date in Sao Paulo: %v", err)
	}
	if got.String() != "2026-07-12" {
		t.Fatalf("calendar date = %s, want 2026-07-12", got)
	}
}

func TestCivilDateConfiguredTimezoneDoesNotDependOnProcessTZ(t *testing.T) {
	t.Setenv("TZ", "UTC")
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	location := appTimeLocation()
	if location.String() != "America/Sao_Paulo" {
		t.Fatalf("app location = %q, want America/Sao_Paulo", location)
	}

	date := MustCivilDate("2026-07-12")
	got, err := LegacyUnixToCivilDateInLocation(CivilDateToLegacyUnixNoonUTC(date), location)
	if err != nil {
		t.Fatalf("legacy date in configured location: %v", err)
	}
	if got != date {
		t.Fatalf("calendar date = %s, want %s", got, date)
	}
}

func TestLegacyUnixCivilDateCharacterizationAtUTCMidnight(t *testing.T) {
	location := mustCivilDateLocation(t, "America/Sao_Paulo")
	timestamp := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC).Unix()

	if got := LegacyUnixToCivilDateUTC(timestamp).String(); got != "2026-07-12" {
		t.Fatalf("UTC civil date = %s, want 2026-07-12", got)
	}
	got, err := LegacyUnixToCivilDateInLocation(timestamp, location)
	if err != nil {
		t.Fatalf("legacy date in Sao Paulo: %v", err)
	}
	if got.String() != "2026-07-11" {
		t.Fatalf("Sao Paulo civil date = %s, want 2026-07-11", got)
	}
}

func TestLegacyUnixCivilDateCharacterizationAtUTCNoon(t *testing.T) {
	location := mustCivilDateLocation(t, "America/Sao_Paulo")
	timestamp := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC).Unix()

	got, err := LegacyUnixToCivilDateInLocation(timestamp, location)
	if err != nil {
		t.Fatalf("legacy date in Sao Paulo: %v", err)
	}
	if got.String() != "2026-07-12" {
		t.Fatalf("Sao Paulo civil date = %s, want 2026-07-12", got)
	}
}

func TestCivilDateAddMonthsClampsCalendarDays(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		months int
		want   string
	}{
		{name: "january 31 to february", input: "2026-01-31", months: 1, want: "2026-02-28"},
		{name: "march 31 back to february", input: "2026-03-31", months: -1, want: "2026-02-28"},
		{name: "leap year", input: "2024-01-31", months: 1, want: "2024-02-29"},
		{name: "august 31 to short month", input: "2026-08-31", months: 1, want: "2026-09-30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CivilDateAddMonths(MustCivilDate(tt.input), tt.months)
			if err != nil {
				t.Fatalf("CivilDateAddMonths: %v", err)
			}
			if got.String() != tt.want {
				t.Fatalf("CivilDateAddMonths(%s, %d) = %s, want %s", tt.input, tt.months, got, tt.want)
			}
		})
	}
}

func TestCivilDateMonthBounds(t *testing.T) {
	start, nextStart, err := CivilMonthBounds(2026, time.July)
	if err != nil {
		t.Fatalf("CivilMonthBounds: %v", err)
	}
	if start.String() != "2026-07-01" || nextStart.String() != "2026-08-01" {
		t.Fatalf("bounds = [%s, %s), want [2026-07-01, 2026-08-01)", start, nextStart)
	}
}

func TestCivilDatePersistenceIsIndependentOfExtremeOffsets(t *testing.T) {
	date := MustCivilDate("2026-07-12")
	for _, timezone := range []string{"Pacific/Kiritimati", "America/Adak"} {
		t.Run(timezone, func(t *testing.T) {
			location := mustCivilDateLocation(t, timezone)
			if got := civilDateAt(time.Date(2026, time.July, 12, 12, 0, 0, 0, location), location).String(); got != date.String() {
				t.Fatalf("civil date = %s, want %s", got, date)
			}
			if got := FormatCivilDate(date); got != "2026-07-12" {
				t.Fatalf("serialized civil date = %s, want 2026-07-12", got)
			}
		})
	}
}

func TestCivilDateRejectsInvalidInputs(t *testing.T) {
	for _, input := range []string{
		"",
		"2026-7-1",
		"2026-02-30",
		"2026-07-12T00:00:00",
		"2026-07-12Z",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseCivilDateInput(input); err == nil {
				t.Fatalf("ParseCivilDateInput(%q) accepted invalid input", input)
			}
		})
	}
}

func TestCivilDateCompareAndAddDays(t *testing.T) {
	start := MustCivilDate("2026-02-28")
	next, err := CivilDateAddDays(start, 1)
	if err != nil {
		t.Fatalf("CivilDateAddDays: %v", err)
	}
	if next.String() != "2026-03-01" {
		t.Fatalf("next date = %s, want 2026-03-01", next)
	}
	if got := CompareCivilDate(start, next); got >= 0 {
		t.Fatalf("CompareCivilDate(start, next) = %d, want negative", got)
	}
}

func mustCivilDateLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return location
}
