package services

import "time"

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
	currentMonth := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
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

func ceilDivInt64(a, b int64) int64 {
	if b <= 0 || a <= 0 {
		return 0
	}
	return (a + b - 1) / b
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
	const maxMonths = 1200

	for months < maxMonths && balance < target {
		balance += recharge
		balance *= (1.0 + r)
		months++
	}

	if months >= maxMonths || months <= 0 {
		return BoxProjection{Status: BoxProjectionStatusNoForecast}
	}

	return BoxProjection{
		Status:     BoxProjectionStatusForecast,
		MonthsLeft: months,
	}
}
