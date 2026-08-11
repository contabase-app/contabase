package services

import (
	"errors"
	"math"
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

func TestEstimateBoxProjectionWithYieldUsesEndOfMonthContribution(t *testing.T) {
	got := EstimateBoxProjectionWithYield(26500000, 500000000, 1500000, 0.008)
	if got.Status != BoxProjectionStatusForecast {
		t.Fatalf("projection status = %q, want forecast", got.Status)
	}
	if got.MonthsLeft < 145 || got.MonthsLeft > 149 {
		t.Fatalf("yield months = %d, want around 147", got.MonthsLeft)
	}
	if got.MonthsLeft == 316 {
		t.Fatalf("yield months = 316, want formula to include current balance and monthly yield")
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

func TestProjectBoxBalanceUsesExactIntegerPathAtZeroYield(t *testing.T) {
	got, err := ProjectBoxBalance(100, 33, 0, 3)
	if err != nil {
		t.Fatalf("ProjectBoxBalance: %v", err)
	}
	if got != 199 {
		t.Fatalf("projected balance = %d, want 199", got)
	}
}

func TestProjectBoxBalanceUsesEndOfPeriodContributions(t *testing.T) {
	got, err := ProjectBoxBalance(10000, 1000, 0.01, 2)
	if err != nil {
		t.Fatalf("ProjectBoxBalance: %v", err)
	}
	// 10000*1.01^2 + 1000*(1.01^2-1)/0.01 = 12211 cents.
	if got != 12211 {
		t.Fatalf("projected balance = %d, want 12211", got)
	}
}

func TestProjectBoxBalanceRoundsEstimatedMoneyToNearestCent(t *testing.T) {
	got, err := ProjectBoxBalance(0, 1, 0.5, 2)
	if err != nil {
		t.Fatalf("ProjectBoxBalance: %v", err)
	}
	if got != 3 {
		t.Fatalf("projected balance = %d, want 3 (2.5 rounded to nearest cent)", got)
	}
}

func TestRequiredMonthlyContributionForPeriodsRoundsUpWithoutYield(t *testing.T) {
	required, err := RequiredMonthlyContributionForPeriods(0, 100, 0, 3)
	if err != nil {
		t.Fatalf("RequiredMonthlyContributionForPeriods: %v", err)
	}
	if required != 34 {
		t.Fatalf("required contribution = %d, want 34", required)
	}

	projected, err := ProjectBoxBalance(0, required, 0, 3)
	if err != nil {
		t.Fatalf("project required contribution: %v", err)
	}
	below, err := ProjectBoxBalance(0, required-1, 0, 3)
	if err != nil {
		t.Fatalf("project one cent below: %v", err)
	}
	if projected < 100 || below >= 100 {
		t.Fatalf("minimal contribution invariant failed: required projects %d, one cent below projects %d", projected, below)
	}
}

func TestRequiredMonthlyContributionForPeriodsWithYieldIsMinimalAndNotAboveZeroYield(t *testing.T) {
	const (
		balance = int64(10000)
		target  = int64(50000)
		periods = 12
		rate    = 0.008
	)
	required, err := RequiredMonthlyContributionForPeriods(balance, target, rate, periods)
	if err != nil {
		t.Fatalf("required with yield: %v", err)
	}
	withoutYield, err := RequiredMonthlyContributionForPeriods(balance, target, 0, periods)
	if err != nil {
		t.Fatalf("required without yield: %v", err)
	}
	if required > withoutYield {
		t.Fatalf("required with positive yield = %d, want <= zero-yield %d", required, withoutYield)
	}

	projected, err := ProjectBoxBalance(balance, required, rate, periods)
	if err != nil {
		t.Fatalf("project required contribution: %v", err)
	}
	below, err := ProjectBoxBalance(balance, required-1, rate, periods)
	if err != nil {
		t.Fatalf("project one cent below: %v", err)
	}
	if projected < target || below >= target {
		t.Fatalf("minimal contribution invariant failed: required projects %d, one cent below projects %d, target %d", projected, below, target)
	}
}

func TestRequiredMonthlyContributionForPeriodsUsesUnroundedBalanceForMinimality(t *testing.T) {
	required, err := RequiredMonthlyContributionForPeriods(90, 100, 0.11, 1)
	if err != nil {
		t.Fatalf("RequiredMonthlyContributionForPeriods: %v", err)
	}
	if required != 1 {
		t.Fatalf("required contribution = %d, want 1", required)
	}

	withoutContribution, err := projectedBoxBalanceFloat(90, 0, 0.11, 1)
	if err != nil {
		t.Fatalf("raw projection without contribution: %v", err)
	}
	withContribution, err := projectedBoxBalanceFloat(90, required, 0.11, 1)
	if err != nil {
		t.Fatalf("raw projection with required contribution: %v", err)
	}
	if withoutContribution >= 100 || withContribution < 100 {
		t.Fatalf("raw minimality failed: without contribution %.17g, with contribution %.17g", withoutContribution, withContribution)
	}

	publicBalance, err := ProjectBoxBalance(90, 0, 0.11, 1)
	if err != nil {
		t.Fatalf("public projection without contribution: %v", err)
	}
	if publicBalance != 100 {
		t.Fatalf("public projected balance = %d, want rounded 100", publicBalance)
	}
}

func TestRequiredMonthlyContributionForPeriodsAllowsYieldAlone(t *testing.T) {
	required, err := RequiredMonthlyContributionForPeriods(9900, 10000, 0.02, 1)
	if err != nil {
		t.Fatalf("RequiredMonthlyContributionForPeriods: %v", err)
	}
	if required != 0 {
		t.Fatalf("required contribution = %d, want 0 when yield alone reaches target", required)
	}
}

func TestPeriodsToReachBoxTargetContributionMonotonicity(t *testing.T) {
	contributions := []int64{100, 250, 500, 1000}
	previousPeriods := MaxBoxProjectionPeriods + 1
	for _, contribution := range contributions {
		periods, found, err := PeriodsToReachBoxTarget(0, 10000, contribution, 0.008)
		if err != nil {
			t.Fatalf("PeriodsToReachBoxTarget(%d): %v", contribution, err)
		}
		if !found {
			t.Fatalf("contribution %d produced no forecast", contribution)
		}
		if periods > previousPeriods {
			t.Fatalf("periods increased from %d to %d when contribution increased to %d", previousPeriods, periods, contribution)
		}
		previousPeriods = periods
	}
}

func TestPeriodsToReachBoxTargetZeroContributionCases(t *testing.T) {
	periods, found, err := PeriodsToReachBoxTarget(1000, 10000, 0, 0)
	if err != nil {
		t.Fatalf("zero contribution, zero yield: %v", err)
	}
	if found || periods != 0 {
		t.Fatalf("zero contribution result = periods %d found %v, want no forecast", periods, found)
	}

	periods, found, err = PeriodsToReachBoxTarget(8000, 10000, 0, 0.01)
	if err != nil {
		t.Fatalf("yield-only projection: %v", err)
	}
	if !found || periods <= 0 {
		t.Fatalf("yield-only result = periods %d found %v, want forecast", periods, found)
	}
}

func TestPeriodsToReachBoxTargetStopsAtExplicitLimit(t *testing.T) {
	periods, found, err := PeriodsToReachBoxTarget(0, 2000, 1, 0)
	if err != nil {
		t.Fatalf("PeriodsToReachBoxTarget: %v", err)
	}
	if found || periods != 0 {
		t.Fatalf("projection = periods %d found %v, want no forecast beyond %d periods", periods, found, MaxBoxProjectionPeriods)
	}
}

func TestProjectionPeriodsBetweenMonthsCivilCases(t *testing.T) {
	tests := []struct {
		name   string
		base   time.Time
		target time.Time
		want   int
	}{
		{
			name:   "current civil month remains one period",
			base:   time.Date(2026, time.August, 31, 23, 0, 0, 0, time.FixedZone("BRT", -3*60*60)),
			target: time.Date(2026, time.August, 1, 1, 0, 0, 0, time.UTC),
			want:   1,
		},
		{
			name:   "december to january",
			base:   time.Date(2026, time.December, 15, 12, 0, 0, 0, time.UTC),
			target: time.Date(2027, time.January, 1, 12, 0, 0, 0, time.UTC),
			want:   1,
		},
		{
			name:   "leap february",
			base:   time.Date(2028, time.January, 31, 12, 0, 0, 0, time.UTC),
			target: time.Date(2028, time.February, 29, 12, 0, 0, 0, time.UTC),
			want:   1,
		},
		{
			name:   "non-leap february",
			base:   time.Date(2027, time.January, 31, 12, 0, 0, 0, time.UTC),
			target: time.Date(2027, time.February, 28, 12, 0, 0, 0, time.UTC),
			want:   1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ProjectionPeriodsBetweenMonths(tc.base, tc.target)
			if err != nil {
				t.Fatalf("ProjectionPeriodsBetweenMonths: %v", err)
			}
			if got != tc.want {
				t.Fatalf("periods = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestProjectionPeriodsBetweenMonthsRejectsPastMonth(t *testing.T) {
	_, err := ProjectionPeriodsBetweenMonths(
		time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
	)
	if !errors.Is(err, ErrBoxProjectionInvalidTargetMonth) {
		t.Fatalf("error = %v, want ErrBoxProjectionInvalidTargetMonth", err)
	}
}

func TestEvaluateBoxPlanRejectsPastTargetMonthEvenWhenGoalIsReached(t *testing.T) {
	target := int64(10000)
	base := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	targetMonth := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	_, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance: 12000,
		TargetAmount:   &target,
		BaseMonth:      base,
		TargetMonth:    &targetMonth,
	})
	if !errors.Is(err, ErrBoxProjectionInvalidTargetMonth) {
		t.Fatalf("error = %v, want ErrBoxProjectionInvalidTargetMonth", err)
	}
}

func TestProjectionCompletionMonthCrossesYearDeterministically(t *testing.T) {
	got, err := ProjectionCompletionMonth(time.Date(2026, time.December, 31, 23, 0, 0, 0, time.UTC), 1)
	if err != nil {
		t.Fatalf("ProjectionCompletionMonth: %v", err)
	}
	want := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("completion month = %s, want %s", got, want)
	}
}

func TestEvaluateBoxPlanTargetAndDateWithoutYield(t *testing.T) {
	target := int64(10000)
	base := time.Date(2026, time.June, 15, 12, 0, 0, 0, time.UTC)
	targetMonth := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

	got, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance: 1000,
		TargetAmount:   &target,
		BaseMonth:      base,
		TargetMonth:    &targetMonth,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan: %v", err)
	}
	if got.Status != BoxPlanStatusForecast || got.Periods != 3 || got.RequiredMonthlyContribution != 3000 {
		t.Fatalf("result = %#v, want forecast/3 periods/3000 required", got)
	}
	if got.Sufficiency != BoxPlanSufficiencyNotEvaluated {
		t.Fatalf("sufficiency = %q, want not_evaluated without supplied contribution", got.Sufficiency)
	}
}

func TestEvaluateBoxPlanTargetAndDateWithYield(t *testing.T) {
	target := int64(50000)
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	targetMonth := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)

	got, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance:   10000,
		TargetAmount:     &target,
		MonthlyYieldRate: 0.008,
		BaseMonth:        base,
		TargetMonth:      &targetMonth,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan: %v", err)
	}
	if got.RequiredMonthlyContribution <= 0 || got.RequiredMonthlyContribution >= 3334 {
		t.Fatalf("required with yield = %d, want between 1 and zero-yield requirement 3334", got.RequiredMonthlyContribution)
	}
}

func TestEvaluateBoxPlanDateAndContributionWithoutTarget(t *testing.T) {
	contribution := int64(1000)
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	targetMonth := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)

	got, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance:      5000,
		MonthlyContribution: &contribution,
		BaseMonth:           base,
		TargetMonth:         &targetMonth,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan: %v", err)
	}
	if got.Status != BoxPlanStatusForecast || got.Periods != 3 || got.ProjectedBalance != 8000 {
		t.Fatalf("result = %#v, want forecast/3 periods/8000 projected", got)
	}
}

func TestEvaluateBoxPlanAllFieldsSufficiency(t *testing.T) {
	target := int64(10000)
	base := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
	targetMonth := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name         string
		contribution int64
		want         BoxPlanSufficiency
		wantDiff     int64
	}{
		{name: "sufficient", contribution: 3000, want: BoxPlanSufficiencySufficient, wantDiff: 0},
		{name: "insufficient by one cent monthly", contribution: 2999, want: BoxPlanSufficiencyInsufficient, wantDiff: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateBoxPlan(BoxPlanInput{
				CurrentBalance:      1000,
				TargetAmount:        &target,
				MonthlyContribution: &tc.contribution,
				BaseMonth:           base,
				TargetMonth:         &targetMonth,
			})
			if err != nil {
				t.Fatalf("EvaluateBoxPlan: %v", err)
			}
			if got.Sufficiency != tc.want || got.MonthlyContributionDifference != tc.wantDiff {
				t.Fatalf("result = %#v, want sufficiency %q difference %d", got, tc.want, tc.wantDiff)
			}
		})
	}
}

func TestEvaluateBoxPlanUsesUnroundedBalanceForSufficiency(t *testing.T) {
	target := int64(100)
	contribution := int64(0)
	base := time.Date(2026, time.June, 15, 23, 30, 0, 0, time.FixedZone("boundary", -3*60*60))
	targetMonth := base

	got, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance:      90,
		TargetAmount:        &target,
		MonthlyContribution: &contribution,
		MonthlyYieldRate:    0.11,
		BaseMonth:           base,
		TargetMonth:         &targetMonth,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan: %v", err)
	}
	if got.Status != BoxPlanStatusForecast || got.Periods != 1 {
		t.Fatalf("result = %#v, want forecast with one period", got)
	}
	if got.ProjectedBalance != 100 || got.RequiredMonthlyContribution != 1 {
		t.Fatalf("result = %#v, want rounded balance 100 and required contribution 1", got)
	}
	if got.Sufficiency != BoxPlanSufficiencyInsufficient || got.MonthlyContributionDifference != -1 {
		t.Fatalf("result = %#v, want mathematically insufficient with difference -1", got)
	}
}

func TestEvaluateBoxPlanTargetAndContributionReturnsCompletionMonth(t *testing.T) {
	target := int64(3000)
	contribution := int64(1000)
	base := time.Date(2026, time.December, 15, 12, 0, 0, 0, time.UTC)

	got, err := EvaluateBoxPlan(BoxPlanInput{
		TargetAmount:        &target,
		MonthlyContribution: &contribution,
		BaseMonth:           base,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan: %v", err)
	}
	wantMonth := time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC)
	if got.Status != BoxPlanStatusForecast || got.Periods != 3 || !got.EstimatedCompletionMonth.Equal(wantMonth) {
		t.Fatalf("result = %#v, want 3 periods and completion %s", got, wantMonth)
	}
}

func TestEvaluateBoxPlanTargetAndContributionWithYield(t *testing.T) {
	target := int64(20000)
	contribution := int64(1000)
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	withYield, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance:      5000,
		TargetAmount:        &target,
		MonthlyContribution: &contribution,
		MonthlyYieldRate:    0.008,
		BaseMonth:           base,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan with yield: %v", err)
	}
	withoutYield, err := EvaluateBoxPlan(BoxPlanInput{
		CurrentBalance:      5000,
		TargetAmount:        &target,
		MonthlyContribution: &contribution,
		BaseMonth:           base,
	})
	if err != nil {
		t.Fatalf("EvaluateBoxPlan without yield: %v", err)
	}
	if withYield.Status != BoxPlanStatusForecast || withYield.Periods <= 0 {
		t.Fatalf("with-yield result = %#v, want forecast", withYield)
	}
	if withYield.Periods > withoutYield.Periods {
		t.Fatalf("positive yield periods = %d, want <= zero-yield periods %d", withYield.Periods, withoutYield.Periods)
	}
}

func TestEvaluateBoxPlanGoalReachedExactlyOrAbove(t *testing.T) {
	for _, balance := range []int64{10000, 12000} {
		target := int64(10000)
		got, err := EvaluateBoxPlan(BoxPlanInput{CurrentBalance: balance, TargetAmount: &target})
		if err != nil {
			t.Fatalf("EvaluateBoxPlan(%d): %v", balance, err)
		}
		if got.Status != BoxPlanStatusGoalReached || got.Sufficiency != BoxPlanSufficiencyGoalReached || got.RequiredMonthlyContribution != 0 {
			t.Fatalf("result for balance %d = %#v, want goal reached", balance, got)
		}
	}
}

func TestEvaluateBoxPlanDistinguishesAbsentAndZeroTarget(t *testing.T) {
	absent, err := EvaluateBoxPlan(BoxPlanInput{})
	if err != nil {
		t.Fatalf("absent target: %v", err)
	}
	if absent.Status != BoxPlanStatusNotApplicable {
		t.Fatalf("absent target status = %q, want not_applicable", absent.Status)
	}

	zero := int64(0)
	_, err = EvaluateBoxPlan(BoxPlanInput{TargetAmount: &zero})
	if !errors.Is(err, ErrBoxProjectionInvalidTarget) {
		t.Fatalf("zero target error = %v, want ErrBoxProjectionInvalidTarget", err)
	}
}

func TestNewProjectionAPIRejectsInvalidContributionAndRates(t *testing.T) {
	target := int64(10000)
	negativeContribution := int64(-1)
	if _, err := EvaluateBoxPlan(BoxPlanInput{TargetAmount: &target, MonthlyContribution: &negativeContribution}); !errors.Is(err, ErrBoxProjectionInvalidContribution) {
		t.Fatalf("negative contribution error = %v", err)
	}

	for _, rate := range []float64{-0.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := EvaluateBoxPlan(BoxPlanInput{TargetAmount: &target, MonthlyYieldRate: rate}); !errors.Is(err, ErrBoxProjectionInvalidYieldRate) {
			t.Fatalf("rate %v error = %v, want ErrBoxProjectionInvalidYieldRate", rate, err)
		}
	}
}

func TestProjectionAPIRejectsNumericInfinityInsteadOfExposingIt(t *testing.T) {
	_, err := ProjectBoxBalance(math.MaxInt64, math.MaxInt64, 0, 2)
	if !errors.Is(err, ErrBoxProjectionNumericOverflow) {
		t.Fatalf("integer overflow error = %v, want ErrBoxProjectionNumericOverflow", err)
	}

	_, err = ProjectBoxBalance(1, 1, math.MaxFloat64, MaxBoxProjectionPeriods)
	if !errors.Is(err, ErrBoxProjectionNumericOverflow) {
		t.Fatalf("float overflow error = %v, want ErrBoxProjectionNumericOverflow", err)
	}

	_, err = RequiredMonthlyContributionForPeriods(math.MinInt64, math.MaxInt64, 0, 1)
	if !errors.Is(err, ErrBoxProjectionNumericOverflow) {
		t.Fatalf("difference overflow error = %v, want ErrBoxProjectionNumericOverflow", err)
	}
}
