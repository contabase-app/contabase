package services

import (
	"errors"
	"math"
	"time"
)

const MaxBoxProjectionPeriods = 1200

var (
	ErrBoxProjectionInvalidTarget       = errors.New("box projection target must be greater than zero")
	ErrBoxProjectionInvalidContribution = errors.New("box projection contribution must not be negative")
	ErrBoxProjectionInvalidYieldRate    = errors.New("box projection yield rate must be finite and non-negative")
	ErrBoxProjectionInvalidPeriods      = errors.New("box projection periods must be between zero and 1200")
	ErrBoxProjectionInvalidBaseMonth    = errors.New("box projection base month is required")
	ErrBoxProjectionInvalidTargetMonth  = errors.New("box projection target month precedes base month")
	ErrBoxProjectionNumericOverflow     = errors.New("box projection numeric result is outside int64 cents")
)

type BoxPlanStatus string

const (
	BoxPlanStatusNotApplicable BoxPlanStatus = "not_applicable"
	BoxPlanStatusGoalReached   BoxPlanStatus = "goal_reached"
	BoxPlanStatusForecast      BoxPlanStatus = "forecast"
	BoxPlanStatusNoForecast    BoxPlanStatus = "no_forecast"
)

type BoxPlanSufficiency string

const (
	BoxPlanSufficiencyNotEvaluated BoxPlanSufficiency = "not_evaluated"
	BoxPlanSufficiencyGoalReached  BoxPlanSufficiency = "goal_reached"
	BoxPlanSufficiencySufficient   BoxPlanSufficiency = "sufficient"
	BoxPlanSufficiencyInsufficient BoxPlanSufficiency = "insufficient"
)

// BoxPlanInput describes optional goal-planning inputs without conflating an
// absent target or contribution with a value of zero. MonthlyYieldRate is a
// decimal fraction: 0.008 means 0.8% per month. BaseMonth and TargetMonth are
// interpreted as civil months; their day and clock components are ignored.
type BoxPlanInput struct {
	CurrentBalance      int64
	TargetAmount        *int64
	MonthlyContribution *int64
	MonthlyYieldRate    float64
	BaseMonth           time.Time
	TargetMonth         *time.Time
}

// BoxPlanResult contains mathematical facts only. It deliberately contains no
// UI text and has no persistence semantics.
type BoxPlanResult struct {
	Status                        BoxPlanStatus
	Sufficiency                   BoxPlanSufficiency
	Periods                       int
	EstimatedCompletionMonth      time.Time
	ProjectedBalance              int64
	RequiredMonthlyContribution   int64
	MonthlyContributionDifference int64
}

type BoxProjectionStatus string

const (
	BoxProjectionStatusNoForecast BoxProjectionStatus = "no_forecast"
	BoxProjectionStatusCompleted  BoxProjectionStatus = "completed"
	BoxProjectionStatusForecast   BoxProjectionStatus = "forecast"
)

type BoxProjection struct {
	Status     BoxProjectionStatus
	MonthsLeft int
}

func EstimateBoxProjection(currentBalance, targetAmount, monthlyRecharge int64) BoxProjection {
	if targetAmount <= 0 {
		return BoxProjection{Status: BoxProjectionStatusNoForecast}
	}
	if currentBalance >= targetAmount {
		return BoxProjection{Status: BoxProjectionStatusCompleted}
	}
	if monthlyRecharge <= 0 {
		return BoxProjection{Status: BoxProjectionStatusNoForecast}
	}

	remaining := targetAmount - currentBalance
	if remaining <= 0 {
		return BoxProjection{Status: BoxProjectionStatusCompleted}
	}
	months := ceilDivInt64(remaining, monthlyRecharge)
	if months <= 0 {
		return BoxProjection{Status: BoxProjectionStatusNoForecast}
	}
	return BoxProjection{
		Status:     BoxProjectionStatusForecast,
		MonthsLeft: int(months),
	}
}

func EstimateRequiredMonthlyContribution(currentBalance, targetAmount int64, months int) (int64, bool) {
	if months <= 0 || targetAmount <= 0 {
		return 0, false
	}
	if currentBalance >= targetAmount {
		return 0, true
	}
	remaining := targetAmount - currentBalance
	return ceilDivInt64(remaining, int64(months)), true
}

func MonthsUntilTargetDate(targetDateUnix int64, now time.Time) int {
	if targetDateUnix <= 0 {
		return 0
	}

	targetDate := time.Unix(targetDateUnix, 0).UTC()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	targetMonth := time.Date(targetDate.Year(), targetDate.Month(), 1, 0, 0, 0, 0, time.UTC)
	if targetMonth.Before(currentMonth) {
		return 0
	}

	months := int(targetMonth.Year()-currentMonth.Year())*12 + int(targetMonth.Month()-currentMonth.Month())
	if months <= 0 {
		return 1
	}
	return months
}

func EstimateRequiredMonthlyContributionByTargetDate(currentBalance, targetAmount, targetDateUnix int64, now time.Time) (int64, int, bool) {
	months := MonthsUntilTargetDate(targetDateUnix, now)
	if months <= 0 {
		return 0, 0, false
	}
	required, ok := EstimateRequiredMonthlyContribution(currentBalance, targetAmount, months)
	if !ok {
		return 0, months, false
	}
	return required, months, true
}

// EvaluateBoxPlan evaluates the supported combinations of target, target month
// and monthly contribution. Contributions are applied at the end of each
// monthly period. The function is deterministic and never reads the clock.
func EvaluateBoxPlan(input BoxPlanInput) (BoxPlanResult, error) {
	result := BoxPlanResult{
		Status:      BoxPlanStatusNotApplicable,
		Sufficiency: BoxPlanSufficiencyNotEvaluated,
	}

	if err := validateProjectionYieldRate(input.MonthlyYieldRate); err != nil {
		return result, err
	}
	if input.TargetAmount != nil && *input.TargetAmount <= 0 {
		return result, ErrBoxProjectionInvalidTarget
	}
	if input.MonthlyContribution != nil && *input.MonthlyContribution < 0 {
		return result, ErrBoxProjectionInvalidContribution
	}
	targetPeriods := 0
	var err error
	if input.TargetMonth != nil {
		targetPeriods, err = ProjectionPeriodsBetweenMonths(input.BaseMonth, *input.TargetMonth)
		if err != nil {
			return result, err
		}
	}

	if input.TargetAmount != nil && input.CurrentBalance >= *input.TargetAmount {
		result.Status = BoxPlanStatusGoalReached
		result.Sufficiency = BoxPlanSufficiencyGoalReached
		result.ProjectedBalance = input.CurrentBalance
		return result, nil
	}

	if input.TargetMonth != nil {
		result.Periods = targetPeriods
		contribution := int64(0)
		if input.MonthlyContribution != nil {
			contribution = *input.MonthlyContribution
		}
		result.ProjectedBalance, err = ProjectBoxBalance(
			input.CurrentBalance,
			contribution,
			input.MonthlyYieldRate,
			targetPeriods,
		)
		if err != nil {
			return result, err
		}
		result.Status = BoxPlanStatusForecast

		if input.TargetAmount == nil {
			return result, nil
		}

		result.RequiredMonthlyContribution, err = RequiredMonthlyContributionForPeriods(
			input.CurrentBalance,
			*input.TargetAmount,
			input.MonthlyYieldRate,
			targetPeriods,
		)
		if err != nil {
			return result, err
		}
		if input.MonthlyContribution != nil {
			result.MonthlyContributionDifference = contribution - result.RequiredMonthlyContribution
			mathematicallySufficient := result.ProjectedBalance >= *input.TargetAmount
			if input.MonthlyYieldRate > 0 {
				rawProjectedBalance, rawErr := projectedBoxBalanceFloat(
					input.CurrentBalance,
					contribution,
					input.MonthlyYieldRate,
					targetPeriods,
				)
				if rawErr != nil {
					return result, rawErr
				}
				mathematicallySufficient = rawProjectedBalance >= float64(*input.TargetAmount)
			}
			if mathematicallySufficient {
				result.Sufficiency = BoxPlanSufficiencySufficient
			} else {
				result.Sufficiency = BoxPlanSufficiencyInsufficient
			}
		}
		return result, nil
	}

	if input.TargetAmount == nil {
		return result, nil
	}
	if input.MonthlyContribution == nil {
		result.Status = BoxPlanStatusNoForecast
		return result, nil
	}

	periods, found, err := PeriodsToReachBoxTarget(
		input.CurrentBalance,
		*input.TargetAmount,
		*input.MonthlyContribution,
		input.MonthlyYieldRate,
	)
	if err != nil {
		return result, err
	}
	if !found {
		result.Status = BoxPlanStatusNoForecast
		return result, nil
	}
	result.Status = BoxPlanStatusForecast
	result.Periods = periods
	result.ProjectedBalance, err = ProjectBoxBalance(
		input.CurrentBalance,
		*input.MonthlyContribution,
		input.MonthlyYieldRate,
		periods,
	)
	if err != nil {
		return result, err
	}
	result.EstimatedCompletionMonth, err = ProjectionCompletionMonth(input.BaseMonth, periods)
	if err != nil {
		return result, err
	}
	return result, nil
}

// ProjectBoxBalance returns the balance, in cents, after periods monthly
// periods. With a positive yield rate it uses the future-value formula for an
// ordinary annuity (end-of-period contributions):
//
//	FV(n) = B*(1+r)^n + A*((1+r)^n-1)/r
//
// Zero yield follows an integer-only path. Positive-yield output is rounded to
// the nearest cent.
func ProjectBoxBalance(currentBalance, monthlyContribution int64, monthlyYieldRate float64, periods int) (int64, error) {
	if monthlyContribution < 0 {
		return 0, ErrBoxProjectionInvalidContribution
	}
	if err := validateProjectionYieldRate(monthlyYieldRate); err != nil {
		return 0, err
	}
	if periods < 0 || periods > MaxBoxProjectionPeriods {
		return 0, ErrBoxProjectionInvalidPeriods
	}
	if periods == 0 {
		return currentBalance, nil
	}

	if monthlyYieldRate == 0 {
		if monthlyContribution > math.MaxInt64/int64(periods) {
			return 0, ErrBoxProjectionNumericOverflow
		}
		contributions := monthlyContribution * int64(periods)
		if currentBalance > 0 && contributions > math.MaxInt64-currentBalance {
			return 0, ErrBoxProjectionNumericOverflow
		}
		return currentBalance + contributions, nil
	}

	futureValue, err := projectedBoxBalanceFloat(currentBalance, monthlyContribution, monthlyYieldRate, periods)
	if err != nil {
		return 0, err
	}
	return roundedFloatCents(futureValue)
}

// RequiredMonthlyContributionForPeriods returns the smallest whole-cent
// end-of-period contribution whose projected balance reaches targetAmount.
// The positive-yield formula is used to obtain an upper bound and the result is
// normalized against the unrounded mathematical balance so that one cent less
// is insufficient whenever the required contribution is positive.
func RequiredMonthlyContributionForPeriods(currentBalance, targetAmount int64, monthlyYieldRate float64, periods int) (int64, error) {
	if targetAmount <= 0 {
		return 0, ErrBoxProjectionInvalidTarget
	}
	if err := validateProjectionYieldRate(monthlyYieldRate); err != nil {
		return 0, err
	}
	if periods <= 0 || periods > MaxBoxProjectionPeriods {
		return 0, ErrBoxProjectionInvalidPeriods
	}
	if currentBalance >= targetAmount {
		return 0, nil
	}

	if monthlyYieldRate == 0 {
		remaining, err := positiveProjectionDifference(targetAmount, currentBalance)
		if err != nil {
			return 0, err
		}
		return ceilDivInt64(remaining, int64(periods)), nil
	}

	withoutContribution, err := projectedBoxBalanceFloat(currentBalance, 0, monthlyYieldRate, periods)
	if err != nil {
		return 0, err
	}
	if withoutContribution >= float64(targetAmount) {
		return 0, nil
	}

	growth, annuityFactor, err := projectionGrowthAndAnnuity(monthlyYieldRate, periods)
	if err != nil {
		return 0, err
	}
	requiredFloat := (float64(targetAmount) - float64(currentBalance)*growth) / annuityFactor
	if math.IsNaN(requiredFloat) || math.IsInf(requiredFloat, 0) || requiredFloat <= 0 {
		return 0, ErrBoxProjectionNumericOverflow
	}
	upper, err := ceiledPositiveFloatCents(requiredFloat)
	if err != nil {
		return 0, err
	}

	// Floating-point formulae may land immediately beside a cent boundary.
	// Grow the upper bound until it is demonstrably sufficient, then binary
	// search for the minimum sufficient whole-cent contribution.
	for {
		projected, projectErr := projectedBoxBalanceFloat(currentBalance, upper, monthlyYieldRate, periods)
		if projectErr != nil {
			return 0, projectErr
		}
		if projected >= float64(targetAmount) {
			break
		}
		if upper == math.MaxInt64 {
			return 0, ErrBoxProjectionNumericOverflow
		}
		upper++
	}

	low := int64(0)
	for low < upper {
		mid := low + (upper-low)/2
		projected, projectErr := projectedBoxBalanceFloat(currentBalance, mid, monthlyYieldRate, periods)
		if projectErr != nil {
			return 0, projectErr
		}
		if projected >= float64(targetAmount) {
			upper = mid
		} else {
			low = mid + 1
		}
	}
	return low, nil
}

// PeriodsToReachBoxTarget returns the minimum number of periods needed to
// reach a target, bounded by MaxBoxProjectionPeriods. found=false is a valid
// no-forecast result, not a numeric error.
func PeriodsToReachBoxTarget(currentBalance, targetAmount, monthlyContribution int64, monthlyYieldRate float64) (periods int, found bool, err error) {
	if targetAmount <= 0 {
		return 0, false, ErrBoxProjectionInvalidTarget
	}
	if monthlyContribution < 0 {
		return 0, false, ErrBoxProjectionInvalidContribution
	}
	if err := validateProjectionYieldRate(monthlyYieldRate); err != nil {
		return 0, false, err
	}
	if currentBalance >= targetAmount {
		return 0, true, nil
	}

	if monthlyYieldRate == 0 {
		if monthlyContribution == 0 {
			return 0, false, nil
		}
		remaining, differenceErr := positiveProjectionDifference(targetAmount, currentBalance)
		if differenceErr != nil {
			return 0, false, differenceErr
		}
		months := ceilDivInt64(remaining, monthlyContribution)
		if months <= 0 || months > MaxBoxProjectionPeriods {
			return 0, false, nil
		}
		return int(months), true, nil
	}

	for month := 1; month <= MaxBoxProjectionPeriods; month++ {
		projected, projectErr := ProjectBoxBalance(currentBalance, monthlyContribution, monthlyYieldRate, month)
		if projectErr != nil {
			return 0, false, projectErr
		}
		if projected >= targetAmount {
			return month, true, nil
		}
	}
	return 0, false, nil
}

// ProjectionPeriodsBetweenMonths preserves the existing target-month
// convention: the base civil month itself counts as one period, while future
// months use their calendar-month distance (June to September is 3).
func ProjectionPeriodsBetweenMonths(baseMonth, targetMonth time.Time) (int, error) {
	if baseMonth.IsZero() {
		return 0, ErrBoxProjectionInvalidBaseMonth
	}
	base := normalizeProjectionMonth(baseMonth)
	target := normalizeProjectionMonth(targetMonth)
	if target.Before(base) {
		return 0, ErrBoxProjectionInvalidTargetMonth
	}
	months := (target.Year()-base.Year())*12 + int(target.Month()-base.Month())
	if months == 0 {
		return 1, nil
	}
	if months > MaxBoxProjectionPeriods {
		return 0, ErrBoxProjectionInvalidPeriods
	}
	return months, nil
}

// ProjectionCompletionMonth converts a number of full future periods into the
// corresponding civil month. A zero-period completed goal remains in the base
// month; one future period from December completes in January.
func ProjectionCompletionMonth(baseMonth time.Time, periods int) (time.Time, error) {
	if baseMonth.IsZero() {
		return time.Time{}, ErrBoxProjectionInvalidBaseMonth
	}
	if periods < 0 || periods > MaxBoxProjectionPeriods {
		return time.Time{}, ErrBoxProjectionInvalidPeriods
	}
	return normalizeProjectionMonth(baseMonth).AddDate(0, periods, 0), nil
}

func normalizeProjectionMonth(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func validateProjectionYieldRate(rate float64) error {
	if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return ErrBoxProjectionInvalidYieldRate
	}
	return nil
}

func projectionGrowthAndAnnuity(rate float64, periods int) (growth, annuityFactor float64, err error) {
	if err := validateProjectionYieldRate(rate); err != nil {
		return 0, 0, err
	}
	if rate <= 0 || periods <= 0 || periods > MaxBoxProjectionPeriods {
		return 0, 0, ErrBoxProjectionInvalidPeriods
	}
	exponent := float64(periods) * math.Log1p(rate)
	growth = math.Exp(exponent)
	annuityFactor = math.Expm1(exponent) / rate
	if math.IsNaN(growth) || math.IsInf(growth, 0) || math.IsNaN(annuityFactor) || math.IsInf(annuityFactor, 0) || annuityFactor <= 0 {
		return 0, 0, ErrBoxProjectionNumericOverflow
	}
	return growth, annuityFactor, nil
}

func positiveProjectionDifference(target, current int64) (int64, error) {
	if current >= target {
		return 0, nil
	}
	if current < 0 && target > math.MaxInt64+current {
		return 0, ErrBoxProjectionNumericOverflow
	}
	return target - current, nil
}

func projectedBoxBalanceFloat(currentBalance, monthlyContribution int64, rate float64, periods int) (float64, error) {
	growth, annuityFactor, err := projectionGrowthAndAnnuity(rate, periods)
	if err != nil {
		return 0, err
	}
	futureValue := float64(currentBalance)*growth + float64(monthlyContribution)*annuityFactor
	if math.IsNaN(futureValue) || math.IsInf(futureValue, 0) {
		return 0, ErrBoxProjectionNumericOverflow
	}
	return futureValue, nil
}

func roundedFloatCents(value float64) (int64, error) {
	rounded := math.Round(value)
	const int64Limit = float64(1 << 63)
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) || rounded >= int64Limit || rounded < -int64Limit {
		return 0, ErrBoxProjectionNumericOverflow
	}
	return int64(rounded), nil
}

func ceiledPositiveFloatCents(value float64) (int64, error) {
	ceiled := math.Ceil(value)
	const int64Limit = float64(1 << 63)
	if math.IsNaN(ceiled) || math.IsInf(ceiled, 0) || ceiled <= 0 || ceiled >= int64Limit {
		return 0, ErrBoxProjectionNumericOverflow
	}
	return int64(ceiled), nil
}

func ceilDivInt64(a, b int64) int64 {
	if b <= 0 || a <= 0 {
		return 0
	}
	return 1 + (a-1)/b
}

func EstimateBoxProjectionWithYield(currentBalance, targetAmount, monthlyRecharge int64, monthlyYieldRate float64) BoxProjection {
	if targetAmount <= 0 {
		return BoxProjection{Status: BoxProjectionStatusNoForecast}
	}
	if currentBalance >= targetAmount {
		return BoxProjection{Status: BoxProjectionStatusCompleted}
	}

	balance := float64(currentBalance)
	target := float64(targetAmount)
	recharge := float64(monthlyRecharge)
	r := monthlyYieldRate

	if r <= 0 {
		if monthlyRecharge <= 0 {
			return BoxProjection{Status: BoxProjectionStatusNoForecast}
		}
		remaining := targetAmount - currentBalance
		if remaining <= 0 {
			return BoxProjection{Status: BoxProjectionStatusCompleted}
		}
		months := ceilDivInt64(remaining, monthlyRecharge)
		if months <= 0 {
			return BoxProjection{Status: BoxProjectionStatusNoForecast}
		}
		return BoxProjection{
			Status:     BoxProjectionStatusForecast,
			MonthsLeft: int(months),
		}
	}

	months := 0
	for months < MaxBoxProjectionPeriods {
		months++
		growth := math.Pow(1.0+r, float64(months))
		futureValue := balance*growth + recharge*((growth-1.0)/r)
		if futureValue >= target {
			return BoxProjection{
				Status:     BoxProjectionStatusForecast,
				MonthsLeft: months,
			}
		}
	}

	return BoxProjection{Status: BoxProjectionStatusNoForecast}
}
