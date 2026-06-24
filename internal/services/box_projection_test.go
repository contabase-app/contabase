package services

import (
	"testing"
	"time"
)

func TestEstimateBoxProjectionCompleted(t *testing.T) {
	got := EstimateBoxProjection(12000, 10000, 500)
	if got.Status != BoxProjectionStatusCompleted || got.MonthsLeft != 0 {
		t.Fatalf("projection = %#v, want completed with zero months", got)
	}
}

func TestEstimateBoxProjectionForecast(t *testing.T) {
	got := EstimateBoxProjection(1000, 10000, 3000)
	if got.Status != BoxProjectionStatusForecast || got.MonthsLeft != 3 {
		t.Fatalf("projection = %#v, want forecast with 3 months", got)
	}
}

func TestEstimateBoxProjectionNoForecastWithoutRecharge(t *testing.T) {
	got := EstimateBoxProjection(1000, 10000, 0)
	if got.Status != BoxProjectionStatusNoForecast || got.MonthsLeft != 0 {
		t.Fatalf("projection = %#v, want no_forecast with zero months", got)
	}
}

func TestEstimateBoxProjectionNoForecastWithoutTarget(t *testing.T) {
	got := EstimateBoxProjection(1000, 0, 500)
	if got.Status != BoxProjectionStatusNoForecast || got.MonthsLeft != 0 {
		t.Fatalf("projection = %#v, want no_forecast with zero months", got)
	}
}

func TestEstimateBoxProjectionRoundsMonthsUp(t *testing.T) {
	got := EstimateBoxProjection(1000, 10000, 4000)
	if got.Status != BoxProjectionStatusForecast || got.MonthsLeft != 3 {
		t.Fatalf("projection = %#v, want forecast with 3 months (ceil)", got)
	}
}

func TestEstimateRequiredMonthlyContribution(t *testing.T) {
	got, ok := EstimateRequiredMonthlyContribution(1000, 10000, 4)
	if !ok || got != 2250 {
		t.Fatalf("required monthly = %d, ok=%v, want 2250,true", got, ok)
	}
}

func TestMonthsUntilTargetDate(t *testing.T) {
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

	if got := MonthsUntilTargetDate(0, now); got != 0 {
		t.Fatalf("months without date = %d, want 0", got)
	}
	if got := MonthsUntilTargetDate(time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC).Unix(), now); got != 0 {
		t.Fatalf("months with past date = %d, want 0", got)
	}
	if got := MonthsUntilTargetDate(time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC).Unix(), now); got != 1 {
		t.Fatalf("months same month = %d, want 1", got)
	}
	if got := MonthsUntilTargetDate(time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC).Unix(), now); got != 3 {
		t.Fatalf("months future date = %d, want 3", got)
	}
}

func TestEstimateRequiredMonthlyContributionByTargetDate(t *testing.T) {
	now := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)

	required, months, ok := EstimateRequiredMonthlyContributionByTargetDate(1000, 10000, time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC).Unix(), now)
	if !ok || months != 3 || required != 3000 {
		t.Fatalf("required by date = %d months=%d ok=%v, want 3000/3/true", required, months, ok)
	}

	required, months, ok = EstimateRequiredMonthlyContributionByTargetDate(12000, 10000, time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC).Unix(), now)
	if !ok || months != 3 || required != 0 {
		t.Fatalf("required by date (completed) = %d months=%d ok=%v, want 0/3/true", required, months, ok)
	}

	required, months, ok = EstimateRequiredMonthlyContributionByTargetDate(1000, 10000, time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC).Unix(), now)
	if ok || months != 0 || required != 0 {
		t.Fatalf("required by date (past) = %d months=%d ok=%v, want 0/0/false", required, months, ok)
	}
}

func TestEstimateBoxProjectionWithYieldSameAsSimpleWhenRateZero(t *testing.T) {
	simple := EstimateBoxProjection(1000, 10000, 3000)
	withYield := EstimateBoxProjectionWithYield(1000, 10000, 3000, 0)
	if withYield.Status != simple.Status || withYield.MonthsLeft != simple.MonthsLeft {
		t.Fatalf("with yield 0 = %#v, want same as simple: %#v", withYield, simple)
	}
}

func TestEstimateBoxProjectionWithYieldReducesMonths(t *testing.T) {
	got := EstimateBoxProjectionWithYield(800000, 1000000, 50000, 0.02)
	if got.Status != BoxProjectionStatusForecast {
		t.Fatalf("projection status = %q, want forecast", got.Status)
	}
	if got.MonthsLeft >= 4 {
		t.Fatalf("yield months = %d, want < 4 (simple projection = ceil(20000/5000) = 4, yield should reduce)", got.MonthsLeft)
	}
	if got.MonthsLeft <= 0 {
		t.Fatalf("yield months = %d, want > 0", got.MonthsLeft)
	}
}

func TestEstimateBoxProjectionWithYieldCompletedWhenAlreadyDone(t *testing.T) {
	got := EstimateBoxProjectionWithYield(12000, 10000, 500, 0.01)
	if got.Status != BoxProjectionStatusCompleted || got.MonthsLeft != 0 {
		t.Fatalf("projection = %#v, want completed", got)
	}
}

func TestEstimateBoxProjectionWithYieldNoTarget(t *testing.T) {
	got := EstimateBoxProjectionWithYield(1000, 0, 500, 0.01)
	if got.Status != BoxProjectionStatusNoForecast {
		t.Fatalf("projection = %#v, want no_forecast", got)
	}
}

func TestEstimateBoxProjectionWithYieldCompoundInterestAlone(t *testing.T) {
	got := EstimateBoxProjectionWithYield(80000, 100000, 0, 0.01)
	if got.Status != BoxProjectionStatusForecast {
		t.Fatalf("projection status = %q, want forecast (interest alone should reach target)", got.Status)
	}
	if got.MonthsLeft <= 0 {
		t.Fatalf("yield months = %d, want > 0", got.MonthsLeft)
	}
}

func TestEstimateBoxProjectionWithYieldWithoutRechargeAndRate(t *testing.T) {
	got := EstimateBoxProjectionWithYield(1000, 10000, 0, 0)
	if got.Status != BoxProjectionStatusNoForecast {
		t.Fatalf("projection = %#v, want no_forecast (no recharge, no rate)", got)
	}
}

func TestEstimateBoxProjectionWithYieldNegativeRateTreatedAsZero(t *testing.T) {
	simple := EstimateBoxProjection(1000, 10000, 3000)
	withYield := EstimateBoxProjectionWithYield(1000, 10000, 3000, -0.01)
	if withYield.Status != simple.Status || withYield.MonthsLeft != simple.MonthsLeft {
		t.Fatalf("with negative yield = %#v, want same as simple: %#v", withYield, simple)
	}
}
