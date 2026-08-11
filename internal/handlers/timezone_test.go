package handlers

import (
	"testing"
	"time"
)

func dateTimeInSaoPaulo(unix int64) (string, string) {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	t := time.Unix(unix, 0).In(loc)
	return t.Format("02/01/2006 15:04"), loc.String()
}

func dateTimeInNewYork(unix int64) (string, string) {
	loc, _ := time.LoadLocation("America/New_York")
	t := time.Unix(unix, 0).In(loc)
	return t.Format("02/01/2006 15:04"), loc.String()
}

func TestFormatAppTimeDefaultTimezone(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "")
	t.Setenv("TZ", "")

	unix := int64(1754150400)
	display := FormatAppTime(time.Unix(unix, 0))

	expectedDT, expectedTZ := dateTimeInSaoPaulo(unix)
	if display.DateTime != expectedDT {
		t.Fatalf("expected DateTime %q, got %q", expectedDT, display.DateTime)
	}
	if display.Timezone != expectedTZ {
		t.Fatalf("expected Timezone %q, got %q", expectedTZ, display.Timezone)
	}
}

func TestFormatAppTimeOverridesTZ(t *testing.T) {
	t.Setenv("TZ", "UTC")
	t.Setenv("CONTABASE_TIMEZONE", "America/New_York")

	unix := int64(1754150400)
	display := FormatAppTime(time.Unix(unix, 0))

	expectedDT, expectedTZ := dateTimeInNewYork(unix)
	if display.DateTime != expectedDT {
		t.Fatalf("expected DateTime %q, got %q", expectedDT, display.DateTime)
	}
	if display.Timezone != expectedTZ {
		t.Fatalf("expected Timezone %q, got %q", expectedTZ, display.Timezone)
	}
}

func TestFormatAppTimeInvalidConfigFallsBack(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "Invalid/Zone")
	t.Setenv("TZ", "")

	unix := int64(1754150400)
	display := FormatAppTime(time.Unix(unix, 0))

	expectedDT, expectedTZ := dateTimeInSaoPaulo(unix)
	if display.DateTime != expectedDT {
		t.Fatalf("expected DateTime %q, got %q", expectedDT, display.DateTime)
	}
	if display.Timezone != expectedTZ {
		t.Fatalf("expected Timezone %q, got %q", expectedTZ, display.Timezone)
	}
}

func TestFormatAppTimeDayBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "")
	t.Setenv("TZ", "")

	unix := int64(1754150399)
	display := FormatAppTime(time.Unix(unix, 0))

	expectedDT, expectedTZ := dateTimeInSaoPaulo(unix)
	if display.DateTime != expectedDT {
		t.Fatalf("expected DateTime %q, got %q", expectedDT, display.DateTime)
	}
	if display.Timezone != expectedTZ {
		t.Fatalf("expected Timezone %q, got %q", expectedTZ, display.Timezone)
	}
}

func TestFormatAppTimeYearBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "")
	t.Setenv("TZ", "")

	unix := int64(1735689599)
	display := FormatAppTime(time.Unix(unix, 0))

	expectedDT, expectedTZ := dateTimeInSaoPaulo(unix)
	if display.DateTime != expectedDT {
		t.Fatalf("expected DateTime %q, got %q", expectedDT, display.DateTime)
	}
	if display.Timezone != expectedTZ {
		t.Fatalf("expected Timezone %q, got %q", expectedTZ, display.Timezone)
	}
}

func TestFormatAppTimeDSTZone(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/New_York")
	t.Setenv("TZ", "")

	unix := int64(1754150400)
	display := FormatAppTime(time.Unix(unix, 0))

	expectedDT, expectedTZ := dateTimeInNewYork(unix)
	if display.DateTime != expectedDT {
		t.Fatalf("expected DateTime %q, got %q", expectedDT, display.DateTime)
	}
	if display.Timezone != expectedTZ {
		t.Fatalf("expected Timezone %q, got %q", expectedTZ, display.Timezone)
	}
}

func TestFormatAppTimeFormatPattern(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "")
	t.Setenv("TZ", "")

	display := FormatAppTime(time.Unix(1754150400, 0))

	if len(display.DateTime) != 16 {
		t.Fatalf("expected DateTime length 16 (DD/MM/YYYY HH:mm), got %d", len(display.DateTime))
	}
	if display.DateTime[2] != '/' || display.DateTime[5] != '/' {
		t.Fatalf("expected DD/MM/YYYY separators, got %q", display.DateTime)
	}
	if display.DateTime[10] != ' ' {
		t.Fatalf("expected space separator, got %q", display.DateTime)
	}
	if display.DateTime[13] != ':' {
		t.Fatalf("expected HH:mm separator, got %q", display.DateTime)
	}
	if display.Timezone == "" {
		t.Fatal("expected non-empty timezone")
	}
}
