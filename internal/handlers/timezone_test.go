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

func TestAppTimeUsesSaoPauloCivilDayAcrossUTCBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	t.Setenv("TZ", "UTC")

	instant := time.Date(2026, time.September, 1, 0, 30, 0, 0, time.UTC)
	got := appTime(instant)

	if got.Location().String() != "America/Sao_Paulo" {
		t.Fatalf("location = %q, want America/Sao_Paulo", got.Location())
	}
	if got.Format("2006-01-02 15:04") != "2026-08-31 21:30" {
		t.Fatalf("app time = %q, want 2026-08-31 21:30", got.Format("2006-01-02 15:04"))
	}
}

func TestCivilMonthConsumersUseSaoPauloAcrossUTCMonthBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	t.Setenv("TZ", "UTC")

	instant := time.Date(2026, time.September, 1, 1, 30, 0, 0, time.UTC)
	selectedMonth := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)

	if got := AppCivilDateUTC(instant); !got.Equal(time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("civil date = %s, want 2026-08-31", got.Format("2006-01-02"))
	}
	if got := dashboardCashSummaryMonthLabel(instant); got != "Agosto/2026" {
		t.Fatalf("dashboard month label = %q, want Agosto/2026", got)
	}
	if got := dashboardPendingWindowEnd(instant); got != time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("dashboard pending window end = %d, want Sao Paulo August 31 plus eight civil days", got)
	}
	if got := computeCycleBadge(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC).Unix(), instant); got != "Fecha em 1 dia" {
		t.Fatalf("invoice cycle badge = %q, want Fecha em 1 dia", got)
	}

	selectors := []struct {
		name    string
		options []MonthOption
	}{
		{name: "lancamentos", options: buildMonthOptionsAt("", selectedMonth, LancamentosFilters{}, instant)},
		{name: "contas", options: buildContasMonthOptionsAt(selectedMonth, "", instant)},
		{name: "faturas", options: buildFaturaMonthOptionsAt("card-test", selectedMonth, "desc", instant)},
		{name: "relatorios", options: buildRelatoriosMonthOptionsAt(2026, 8, "caixa", "mensal", "realizado", instant)},
	}

	for _, selector := range selectors {
		t.Run(selector.name, func(t *testing.T) {
			var current []MonthOption
			for _, option := range selector.options {
				if option.IsCurrent {
					current = append(current, option)
				}
			}
			if len(current) != 1 {
				t.Fatalf("current options = %d, want 1", len(current))
			}
			if current[0].Label != "Ago" || current[0].Year != "2026" {
				t.Fatalf("current month = %s/%s, want Ago/2026", current[0].Label, current[0].Year)
			}
		})
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
