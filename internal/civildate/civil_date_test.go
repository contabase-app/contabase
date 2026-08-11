package civildate

import (
	"fmt"
	"testing"
	"time"
)

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
		{"2024-02-28", "2024-03-01", 2},
		{"2025-02-28", "2025-03-01", 1},
		{"2025-12-31", "2026-12-31", 365},
		{"2023-12-31", "2024-12-31", 366},
	}
	for _, tc := range tests {
		name := fmt.Sprintf("%s_to_%s", tc.start, tc.end)
		t.Run(name, func(t *testing.T) {
			start := MustCivilDate(tc.start)
			end := MustCivilDate(tc.end)
			got := CivilDateDaysBetween(start, end)
			if got != tc.want {
				t.Fatalf("days = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLegacyUnixToCivilDateUTC(t *testing.T) {
	midnight := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC).Unix()
	noon := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC).Unix()
	if got := LegacyUnixToCivilDateUTC(midnight).String(); got != "2026-07-12" {
		t.Fatalf("midnight UTC = %q", got)
	}
	if got := LegacyUnixToCivilDateUTC(noon).String(); got != "2026-07-12" {
		t.Fatalf("noon UTC = %q", got)
	}
}
