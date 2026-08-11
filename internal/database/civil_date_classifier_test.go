package database

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
)

func TestClassifyCivilPairDateCivilMissing(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-06-05"))
	c := cl.ClassifyPair("date_civil", legacy, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil for date_civil missing")
	}
	if c.ReasonCode != ReasonDateCivilMissing {
		t.Fatalf("reason = %q, want DATE_CIVIL_MISSING", c.ReasonCode)
	}
	if !c.Deterministic {
		t.Fatal("expected deterministic=true for missing date_civil")
	}
	if c.RepairOutcome != CivilRepairOutcomeAutoRepair {
		t.Fatalf("repair_outcome = %q, want AUTO_REPAIR", c.RepairOutcome)
	}
	if c.ProposedUTCValue != "2026-06-05" {
		t.Fatalf("proposed = %q, want 2026-06-05", c.ProposedUTCValue)
	}
}

func TestClassifyCivilPairDateCivilMidnightUTC(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	midnight := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC).Unix()
	c := cl.ClassifyPair("date_civil", midnight, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ProposedUTCValue != "2026-06-05" {
		t.Fatalf("midnight UTC proposed = %q, want 2026-06-05", c.ProposedUTCValue)
	}
}

func TestClassifyCivilPairDateCivilNoonUTC(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	noon := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC).Unix()
	c := cl.ClassifyPair("date_civil", noon, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ProposedUTCValue != "2026-06-05" {
		t.Fatalf("noon UTC proposed = %q, want 2026-06-05", c.ProposedUTCValue)
	}
}

func TestClassifyCivilPairDateCivilValid(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-06-05"))
	c := cl.ClassifyPair("date_civil", legacy, sql.NullString{String: "2026-06-05", Valid: true})
	if c == nil || c.ReasonCode != "" {
		t.Fatal("expected empty reason for valid pair")
	}
}

func TestClassifyCivilPairDateCivilInvalidFormat(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-06-05"))
	c := cl.ClassifyPair("date_civil", legacy, sql.NullString{String: "2026-02-30", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonDateCivilInvalid {
		t.Fatalf("reason = %q, want DATE_CIVIL_INVALID", c.ReasonCode)
	}
	if c.Deterministic {
		t.Fatal("expected deterministic=false")
	}
}

func TestClassifyCivilPairDateCivilMismatch(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-06-05"))
	c := cl.ClassifyPair("date_civil", legacy, sql.NullString{String: "2026-07-01", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonDateCivilMismatch {
		t.Fatalf("reason = %q, want DATE_CIVIL_MISMATCH", c.ReasonCode)
	}
	if c.ProposedUTCValue != "2026-06-05" {
		t.Fatalf("proposed = %q, want 2026-06-05", c.ProposedUTCValue)
	}
}

func TestClassifyCivilPairLegacyDateMissingDate(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	c := cl.ClassifyPair("date_civil", 0, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil for date_civil with legacy=0")
	}
	if c.ReasonCode != ReasonLegacyDateMissing {
		t.Fatalf("reason = %q, want LEGACY_DATE_MISSING", c.ReasonCode)
	}
	if c.Deterministic {
		t.Fatal("expected deterministic=false for LEGACY_DATE_MISSING")
	}
}

func TestClassifyCivilPairDueDateAbsentBoth(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	c := cl.ClassifyPair("due_date_civil", 0, sql.NullString{})
	if c != nil {
		t.Fatalf("expected nil for due_date_civil with both absent, got reason=%q", c.ReasonCode)
	}
}

func TestClassifyCivilPairDueDateCivilSetWithoutLegacy(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	c := cl.ClassifyPair("due_date_civil", 0, sql.NullString{String: "2026-07-15", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonDueDateCivilMismatch {
		t.Fatalf("reason = %q, want DUE_DATE_CIVIL_MISMATCH", c.ReasonCode)
	}
}

func TestClassifyCivilPairDueDateCivilMissing(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.MustCivilDate("2026-07-15")
	legacyUnix := civildate.CivilDateToLegacyUnixNoonUTC(legacy)
	c := cl.ClassifyPair("due_date_civil", legacyUnix, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonDueDateCivilMissing {
		t.Fatalf("reason = %q, want DUE_DATE_CIVIL_MISSING", c.ReasonCode)
	}
	if !c.Deterministic {
		t.Fatal("expected deterministic=true")
	}
	if c.RepairOutcome != CivilRepairOutcomeAutoRepair {
		t.Fatalf("repair_outcome = %q, want AUTO_REPAIR", c.RepairOutcome)
	}
}

func TestClassifyCivilPairDueDateCivilValid(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.MustCivilDate("2026-07-15")
	legacyUnix := civildate.CivilDateToLegacyUnixNoonUTC(legacy)
	c := cl.ClassifyPair("due_date_civil", legacyUnix, sql.NullString{String: "2026-07-15", Valid: true})
	if c == nil || c.ReasonCode != "" {
		t.Fatal("expected valid")
	}
}

func TestClassifyCivilPairStartDateCivilMissing(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-01-10"))
	c := cl.ClassifyPair("start_date_civil", legacy, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonStartDateCivilMissing {
		t.Fatalf("reason = %q, want START_DATE_CIVIL_MISSING", c.ReasonCode)
	}
	if c.RepairOutcome != CivilRepairOutcomeAutoRepair {
		t.Fatalf("repair_outcome = %q, want AUTO_REPAIR", c.RepairOutcome)
	}
}

func TestClassifyCivilPairStartDateCivilSetWithoutLegacy(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	c := cl.ClassifyPair("start_date_civil", 0, sql.NullString{String: "2026-01-10", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonStartDateCivilMismatch {
		t.Fatalf("reason = %q, want START_DATE_CIVIL_MISMATCH", c.ReasonCode)
	}
}

func TestClassifyCivilPairStartsAtCivilMissing(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	legacy := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-01-10"))
	c := cl.ClassifyPair("starts_at_civil", legacy, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonStartsAtCivilMissing {
		t.Fatalf("reason = %q, want STARTS_AT_CIVIL_MISSING", c.ReasonCode)
	}
	if c.RepairOutcome != CivilRepairOutcomeAutoRepair {
		t.Fatalf("repair_outcome = %q, want AUTO_REPAIR", c.RepairOutcome)
	}
}

func TestClassifyCivilPairStartsAtCivilSetWithoutLegacy(t *testing.T) {
	cl := NewCivilDateClassifierNoTimezone()
	c := cl.ClassifyPair("starts_at_civil", 0, sql.NullString{String: "2026-06-05", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonStartsAtCivilMismatch {
		t.Fatalf("reason = %q, want STARTS_AT_CIVIL_MISMATCH", c.ReasonCode)
	}
}

func TestClassifyCivilPairTimezoneConvergent(t *testing.T) {
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	noon := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC).Unix()
	c := ClassifyCivilPair("date_civil", noon, sql.NullString{}, loc)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.CachedUTC != "2026-06-05" {
		t.Fatalf("UTC = %q", c.CachedUTC)
	}
	if c.CachedTimezone != "2026-06-05" {
		t.Fatalf("TZ = %q", c.CachedTimezone)
	}
	if c.TimezoneDivergent {
		t.Fatal("UTC and UTC timezone should not be divergent")
	}
}

func TestClassifyCivilPairTimezoneDivergent(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC).Unix()
	c := ClassifyCivilPair("date_civil", midnight, sql.NullString{}, loc)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.CachedUTC != "2026-06-05" {
		t.Fatalf("cached_utc = %q", c.CachedUTC)
	}
	if c.CachedTimezone != "2026-06-04" {
		t.Fatalf("cached_timezone = %q, want 2026-06-04 (America/Sao_Paulo at 0h UTC = previous day)", c.CachedTimezone)
	}
	if !c.TimezoneDivergent {
		t.Fatal("midnight UTC should diverge from America/Sao_Paulo")
	}
}

func TestClassifyCivilPairTimezoneMiddaySameDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	noon := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC).Unix()
	c := ClassifyCivilPair("date_civil", noon, sql.NullString{}, loc)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.CachedUTC != "2026-06-05" {
		t.Fatalf("UTC = %q", c.CachedUTC)
	}
	if c.CachedTimezone != "2026-06-05" {
		t.Fatalf("TZ = %q", c.CachedTimezone)
	}
	if c.TimezoneDivergent {
		t.Fatal("noon UTC should be same day in America/Sao_Paulo")
	}
}

func TestClassifyCivilPairMismatchWithTimezoneDivergence(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC).Unix()
	c := ClassifyCivilPair("date_civil", midnight, sql.NullString{String: "2026-06-04", Valid: true}, loc)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonTimezoneAmbiguous {
		t.Fatalf("reason = %q, want HUMAN_REVIEW_TIMEZONE_AMBIGUOUS", c.ReasonCode)
	}
}

func TestClassifyCivilPairMismatchWithoutTimezoneDivergence(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	noon := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC).Unix()
	c := ClassifyCivilPair("date_civil", noon, sql.NullString{String: "2026-07-01", Valid: true}, loc)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonDateCivilMismatch {
		t.Fatalf("reason = %q, want DATE_CIVIL_MISMATCH (same day in both timezones)", c.ReasonCode)
	}
}

func TestResolveCivilRepairTimezoneDefault(t *testing.T) {
	os.Unsetenv("CONTABASE_TIMEZONE")
	os.Unsetenv("TZ")
	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if loc.String() != civilRepairDefaultTimezone {
		t.Fatalf("timezone = %q, want %q", loc.String(), civilRepairDefaultTimezone)
	}
}

func TestResolveCivilRepairTimezoneFromEnv(t *testing.T) {
	os.Setenv("CONTABASE_TIMEZONE", "UTC")
	defer os.Unsetenv("CONTABASE_TIMEZONE")
	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if loc.String() != "UTC" {
		t.Fatalf("timezone = %q, want UTC", loc.String())
	}
}

func TestResolveCivilRepairTimezoneFromTZ(t *testing.T) {
	os.Unsetenv("CONTABASE_TIMEZONE")
	os.Setenv("TZ", "America/Fortaleza")
	defer os.Unsetenv("TZ")
	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if loc.String() != "America/Fortaleza" {
		t.Fatalf("timezone = %q, want America/Fortaleza", loc.String())
	}
}

func TestResolveCivilRepairTimezoneInvalid(t *testing.T) {
	os.Unsetenv("CONTABASE_TIMEZONE")
	os.Unsetenv("TZ")
	os.Setenv("CONTABASE_TIMEZONE", "Invalid/Timezone")
	defer os.Unsetenv("CONTABASE_TIMEZONE")
	_, err := resolveCivilRepairTimezone()
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestResolveCivilRepairTimezoneCONTABASE_TIMEZONEPrecedence(t *testing.T) {
	os.Setenv("CONTABASE_TIMEZONE", "UTC")
	os.Setenv("TZ", "America/Sao_Paulo")
	defer os.Unsetenv("CONTABASE_TIMEZONE")
	defer os.Unsetenv("TZ")
	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if loc.String() != "UTC" {
		t.Fatalf("CONTABASE_TIMEZONE should take precedence, got %q", loc.String())
	}
}

func TestClassifyPaidDateCivilPendingNullBoth(t *testing.T) {
	c := ClassifyPaidDateCivil("pending", sql.NullInt64{}, sql.NullString{})
	if c != nil {
		t.Fatal("expected nil for pending with both null")
	}
}

func TestClassifyPaidDateCivilPendingWithPaidAtNoCivil(t *testing.T) {
	c := ClassifyPaidDateCivil("pending", sql.NullInt64{Int64: 1000, Valid: true}, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonPaidDateStructural {
		t.Fatalf("reason = %q, want PAID_DATE_STRUCTURAL_INVALID", c.ReasonCode)
	}
}

func TestClassifyPaidDateCivilPaidWithBothNull(t *testing.T) {
	c := ClassifyPaidDateCivil("paid", sql.NullInt64{}, sql.NullString{})
	if c != nil {
		t.Fatal("expected nil for legacy paid")
	}
}

func TestClassifyPaidDateCivilPaidWithPaidAtNoCivil(t *testing.T) {
	c := ClassifyPaidDateCivil("paid", sql.NullInt64{Int64: 1000, Valid: true}, sql.NullString{})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonPaidDateUnknown {
		t.Fatalf("reason = %q, want HUMAN_REVIEW_PAID_DATE", c.ReasonCode)
	}
	if !c.Deterministic {
		t.Fatal("expected deterministic for paid with paid_at")
	}
	if c.RepairOutcome != CivilRepairOutcomeHumanReview {
		t.Fatalf("paid_date_civil should never be AUTO_REPAIR, got %q", c.RepairOutcome)
	}
}

func TestClassifyPaidDateCivilPaidCanonicalPair(t *testing.T) {
	paidAt := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-08-15"))
	c := ClassifyPaidDateCivil("paid", sql.NullInt64{Int64: paidAt, Valid: true}, sql.NullString{String: "2026-08-15", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil for canonical pair")
	}
	if c.ReasonCode != "" {
		t.Fatal("expected valid (empty reason) for canonical pair")
	}
}

func TestClassifyPaidDateCivilPaidDivergentPair(t *testing.T) {
	paidAt := civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-08-15"))
	c := ClassifyPaidDateCivil("paid", sql.NullInt64{Int64: paidAt, Valid: true}, sql.NullString{String: "2026-08-16", Valid: true})
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.ReasonCode != ReasonPaidDateMismatch {
		t.Fatalf("reason = %q, want HUMAN_REVIEW_PAID_DATE_MISMATCH", c.ReasonCode)
	}
	if c.RepairOutcome != CivilRepairOutcomeHumanReview {
		t.Fatalf("repair_outcome = %q, want HUMAN_REVIEW", c.RepairOutcome)
	}
}

func TestClassifyPaidDateCivilNonPendingNonPaid(t *testing.T) {
	c := ClassifyPaidDateCivil("cancelled", sql.NullInt64{Int64: 1000, Valid: true}, sql.NullString{String: "2026-01-01", Valid: true})
	if c != nil {
		t.Fatalf("expected nil for non-pending/non-paid status, got reason=%q", c.ReasonCode)
	}
}

func TestNoUseOfTimeLocal(t *testing.T) {
	loc, err := resolveCivilRepairTimezone()
	if err != nil {
		t.Fatal(err)
	}
	if loc == time.Local {
		t.Fatal("resolveCivilRepairTimezone returned time.Local")
	}
}

func TestNonAutoRepairFieldIgnored(t *testing.T) {
	c := ClassifyCivilPair("some_unknown_field", civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate("2026-01-01")), sql.NullString{}, nil)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	if c.RepairOutcome == CivilRepairOutcomeAutoRepair {
		t.Fatal("unknown field should not be AUTO_REPAIR")
	}
}
