package services

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/boxcontribution"
	"github.com/contabase-app/contabase/internal/categorycontract"
	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

type MonthlyReferenceSource string

const (
	MonthlyReferenceSourceManual      MonthlyReferenceSource = "MANUAL"
	MonthlyReferenceSourceRecurring   MonthlyReferenceSource = "RECURRING"
	MonthlyReferenceSourceHistorical  MonthlyReferenceSource = "HISTORICAL"
	MonthlyReferenceSourceUnavailable MonthlyReferenceSource = "UNAVAILABLE"
)

type MonthlyPlanningReferences struct {
	IncomeCents        *int64
	TotalCostCents     *int64
	EssentialCostCents *int64
}

type MonthlyPlanningSnapshot struct {
	ProgrammedContributionsCents int64
	IncomeReferenceCents         int64
	IncomeReferenceSource        MonthlyReferenceSource
	TotalCostReferenceCents      int64
	TotalCostReferenceSource     MonthlyReferenceSource
	EssentialCostReferenceCents  int64
	EssentialCostReferenceSource MonthlyReferenceSource
	MarginCents                  int64
	MarginAvailable              bool
	AvailableBalanceCents        int64
	CoverageTenths               int64
	CoverageAvailable            bool
	HistoricalMonths             int
}

type monthlyPlanningQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func UpdateMonthlyPlanningReferences(ctx context.Context, db *sql.DB, workspaceID string, references MonthlyPlanningReferences) error {
	if db == nil {
		return fmt.Errorf("monthly planning references: nil database")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return fmt.Errorf("monthly planning references: workspace is required")
	}
	for name, value := range map[string]*int64{
		"income":         references.IncomeCents,
		"total cost":     references.TotalCostCents,
		"essential cost": references.EssentialCostCents,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("monthly planning references: %s cannot be negative", name)
		}
	}

	result, err := db.ExecContext(ctx, `
		UPDATE workspaces
		SET monthly_income_reference = ?,
			monthly_total_cost_reference = ?,
			monthly_essential_cost_reference = ?,
			updated_at = unixepoch()
		WHERE id = ?
	`, nullableMonthlyReference(references.IncomeCents), nullableMonthlyReference(references.TotalCostCents), nullableMonthlyReference(references.EssentialCostCents), workspaceID)
	if err != nil {
		return fmt.Errorf("monthly planning references update workspace %q: %w", workspaceID, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("monthly planning references update result workspace %q: %w", workspaceID, err)
	}
	if rows != 1 {
		return fmt.Errorf("monthly planning references workspace %q affected %d rows", workspaceID, rows)
	}
	return nil
}

func BuildMonthlyPlanningSnapshot(ctx context.Context, db *sql.DB, workspaceID string, base time.Time) (MonthlyPlanningSnapshot, error) {
	var snapshot MonthlyPlanningSnapshot
	if db == nil {
		return snapshot, fmt.Errorf("monthly planning: nil database")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return snapshot, fmt.Errorf("monthly planning: workspace is required")
	}
	if base.IsZero() {
		return snapshot, fmt.Errorf("monthly planning: zero base time")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, fmt.Errorf("monthly planning begin read snapshot: %w", err)
	}
	defer tx.Rollback()

	workspaceType, references, err := loadMonthlyPlanningWorkspace(ctx, tx, workspaceID)
	if err != nil {
		return snapshot, err
	}
	snapshot.ProgrammedContributionsCents, err = programmedContributions(ctx, tx, workspaceID)
	if err != nil {
		return snapshot, err
	}

	if references.IncomeCents != nil {
		snapshot.IncomeReferenceCents = *references.IncomeCents
		snapshot.IncomeReferenceSource = MonthlyReferenceSourceManual
	} else {
		income, available, recurringErr := recurringMonthlyIncome(ctx, tx, workspaceID, base)
		if recurringErr != nil {
			return snapshot, recurringErr
		}
		if available {
			snapshot.IncomeReferenceCents = income
			snapshot.IncomeReferenceSource = MonthlyReferenceSourceRecurring
		} else {
			snapshot.IncomeReferenceSource = MonthlyReferenceSourceUnavailable
		}
	}

	var history historicalMonthlyCostResult
	if references.TotalCostCents == nil || (references.EssentialCostCents == nil && workspaceType == categorycontract.WorkspaceTypePersonal) {
		history, err = historicalMonthlyCosts(ctx, tx, workspaceID, base)
		if err != nil {
			return snapshot, err
		}
	}
	snapshot.HistoricalMonths = history.Months
	if references.TotalCostCents != nil {
		snapshot.TotalCostReferenceCents = *references.TotalCostCents
		snapshot.TotalCostReferenceSource = MonthlyReferenceSourceManual
	} else if history.Available {
		snapshot.TotalCostReferenceCents = history.TotalAverageCents
		snapshot.TotalCostReferenceSource = MonthlyReferenceSourceHistorical
	} else {
		snapshot.TotalCostReferenceSource = MonthlyReferenceSourceUnavailable
	}
	if references.EssentialCostCents != nil {
		snapshot.EssentialCostReferenceCents = *references.EssentialCostCents
		snapshot.EssentialCostReferenceSource = MonthlyReferenceSourceManual
	} else if workspaceType == categorycontract.WorkspaceTypePersonal && history.Available {
		snapshot.EssentialCostReferenceCents = history.EssentialAverageCents
		snapshot.EssentialCostReferenceSource = MonthlyReferenceSourceHistorical
	} else {
		snapshot.EssentialCostReferenceSource = MonthlyReferenceSourceUnavailable
	}

	reserveBalance, err := calculateWorkspaceReserveBalance(ctx, tx, workspaceID)
	if err != nil {
		return snapshot, fmt.Errorf("monthly planning available balance workspace %q: %w", workspaceID, err)
	}
	snapshot.AvailableBalanceCents = reserveBalance.FreeBalance

	if snapshot.IncomeReferenceSource != MonthlyReferenceSourceUnavailable && snapshot.TotalCostReferenceSource != MonthlyReferenceSourceUnavailable {
		margin, marginErr := checkedSubtract(snapshot.IncomeReferenceCents, snapshot.TotalCostReferenceCents)
		if marginErr == nil {
			margin, marginErr = checkedSubtract(margin, snapshot.ProgrammedContributionsCents)
		}
		if marginErr != nil {
			return snapshot, fmt.Errorf("monthly planning margin workspace %q: %w", workspaceID, marginErr)
		}
		snapshot.MarginCents = margin
		snapshot.MarginAvailable = true
	}

	if snapshot.EssentialCostReferenceSource != MonthlyReferenceSourceUnavailable && snapshot.EssentialCostReferenceCents > 0 {
		coverage, coverageErr := coverageInTenths(snapshot.AvailableBalanceCents, snapshot.EssentialCostReferenceCents)
		if coverageErr != nil {
			return snapshot, fmt.Errorf("monthly planning coverage workspace %q: %w", workspaceID, coverageErr)
		}
		snapshot.CoverageTenths = coverage
		snapshot.CoverageAvailable = true
	}
	if err := tx.Commit(); err != nil {
		return MonthlyPlanningSnapshot{}, fmt.Errorf("monthly planning commit read snapshot: %w", err)
	}
	return snapshot, nil
}

func loadMonthlyPlanningWorkspace(ctx context.Context, db monthlyPlanningQuerier, workspaceID string) (string, MonthlyPlanningReferences, error) {
	var workspaceType string
	var income, totalCost, essentialCost sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT type, monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference
		FROM workspaces
		WHERE id = ?
	`, workspaceID).Scan(&workspaceType, &income, &totalCost, &essentialCost)
	if err != nil {
		return "", MonthlyPlanningReferences{}, fmt.Errorf("monthly planning read workspace %q: %w", workspaceID, err)
	}
	return workspaceType, MonthlyPlanningReferences{
		IncomeCents:        nullInt64Pointer(income),
		TotalCostCents:     nullInt64Pointer(totalCost),
		EssentialCostCents: nullInt64Pointer(essentialCost),
	}, nil
}

func programmedContributions(ctx context.Context, db monthlyPlanningQuerier, workspaceID string) (int64, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT b.target_amount, b.monthly_recharge, b.continue_after_target,
			b.automatic_contribution_state, COALESCE(SUM(l.amount), 0)
		FROM boxes b
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE b.workspace_id = ?
		GROUP BY b.id, b.target_amount, b.monthly_recharge,
			b.continue_after_target, b.automatic_contribution_state
		ORDER BY b.id
	`, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("monthly planning programmed contributions workspace %q: %w", workspaceID, err)
	}
	defer rows.Close()

	var total int64
	for rows.Next() {
		var target, monthly, continueAfter, balance int64
		var state string
		if err := rows.Scan(&target, &monthly, &continueAfter, &state, &balance); err != nil {
			return 0, fmt.Errorf("monthly planning scan programmed contribution: %w", err)
		}
		decision := boxcontribution.NextAutomaticContribution(boxcontribution.NextAutomaticContributionInput{
			BalanceCents:        balance,
			TargetCents:         target,
			MonthlyCents:        monthly,
			TargetDefined:       target > 0,
			ContinueAfterTarget: continueAfter == 1,
			Active:              state == "ACTIVE",
		})
		total, err = checkedAdd(total, decision.AmountCents)
		if err != nil {
			return 0, fmt.Errorf("monthly planning programmed contributions overflow workspace %q: %w", workspaceID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("monthly planning programmed contributions rows: %w", err)
	}
	return total, nil
}

func recurringMonthlyIncome(ctx context.Context, db monthlyPlanningQuerier, workspaceID string, base time.Time) (int64, bool, error) {
	asOf := civildate.CivilDateAt(base, base.Location()).String()
	rows, err := db.QueryContext(ctx, `
		SELECT
			r.amount,
			r.frequency,
			COALESCE(
				NULLIF(r.starts_at_civil, ''),
				CASE WHEN r.starts_at IS NOT NULL AND r.starts_at > 0 THEN strftime('%Y-%m-%d', datetime(r.starts_at, 'unixepoch')) END,
				NULLIF(r.start_date_civil, ''),
				strftime('%Y-%m-%d', datetime(r.start_date, 'unixepoch'))
			),
			COALESCE(
				NULLIF(r.end_date_civil, ''),
				CASE WHEN r.end_date IS NOT NULL AND r.end_date > 0 THEN strftime('%Y-%m-%d', datetime(r.end_date, 'unixepoch')) END
			),
			COALESCE(r.effective_start_sequence, 1),
			r.effective_end_sequence,
			r.total_occurrences,
			s.total_occurrences
		FROM recurring_rules r
		LEFT JOIN recurring_series s ON s.id = r.series_id AND s.workspace_id = r.workspace_id
		WHERE r.workspace_id = ?
			AND r.active = 1
			AND r.type = 'INCOME'
			AND (r.series_id IS NULL OR s.active = 1)
		ORDER BY r.id
	`, workspaceID)
	if err != nil {
		return 0, false, fmt.Errorf("monthly planning recurring income workspace %q: %w", workspaceID, err)
	}
	defer rows.Close()

	var total int64
	count := 0
	for rows.Next() {
		var amount int64
		var frequency, startsAt string
		var endDate sql.NullString
		var effectiveStart int64
		var effectiveEnd, ruleTotal, seriesTotal sql.NullInt64
		if err := rows.Scan(&amount, &frequency, &startsAt, &endDate, &effectiveStart, &effectiveEnd, &ruleTotal, &seriesTotal); err != nil {
			return 0, false, fmt.Errorf("monthly planning scan recurring income: %w", err)
		}
		eligible, eligibilityErr := recurringCandidateEligible(asOf, startsAt, frequency, endDate, effectiveStart, effectiveEnd, ruleTotal, seriesTotal)
		if eligibilityErr != nil {
			return 0, false, fmt.Errorf("monthly planning recurring income eligibility: %w", eligibilityErr)
		}
		if !eligible {
			continue
		}
		if frequency != models.RecurrenceFrequencyMonthly {
			return 0, false, nil
		}
		total, err = checkedAdd(total, amount)
		if err != nil {
			return 0, false, fmt.Errorf("monthly planning recurring income overflow workspace %q: %w", workspaceID, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("monthly planning recurring income rows: %w", err)
	}
	return total, count > 0, nil
}

func recurringCandidateEligible(asOf, startsAt, frequency string, endDate sql.NullString, effectiveStart int64, effectiveEnd, ruleTotal, seriesTotal sql.NullInt64) (bool, error) {
	baseCivil, err := civildate.ParseCivilDateInput(asOf)
	if err != nil {
		return false, fmt.Errorf("invalid base date %q: %w", asOf, err)
	}
	startCivil, err := civildate.ParseCivilDateInput(startsAt)
	if err != nil {
		return false, fmt.Errorf("invalid recurrence start %q: %w", startsAt, err)
	}

	upperCivil := civildate.MustCivilDate("9999-12-31")
	if endDate.Valid {
		upperCivil, err = civildate.ParseCivilDateInput(endDate.String)
		if err != nil {
			return false, fmt.Errorf("invalid recurrence end %q: %w", endDate.String, err)
		}
	}

	startSequence := effectiveStart
	if startSequence < 1 {
		startSequence = 1
	}
	endSequence := int64(math.MaxInt64)
	if effectiveEnd.Valid && effectiveEnd.Int64 < endSequence {
		endSequence = effectiveEnd.Int64
	}
	for _, total := range []sql.NullInt64{ruleTotal, seriesTotal} {
		if total.Valid && total.Int64 > 0 && total.Int64 < endSequence {
			endSequence = total.Int64
		}
	}
	if startSequence > endSequence || civildate.CompareCivilDate(startCivil, upperCivil) > 0 {
		return false, nil
	}
	if civildate.CompareCivilDate(upperCivil, baseCivil) < 0 {
		return false, nil
	}

	offset, supported, err := recurringOffsetAtOrAfter(baseCivil, startCivil, asOf, startsAt, frequency)
	if err != nil {
		return false, err
	}
	if !supported {
		// An unknown active frequency cannot be normalized or proven exhausted
		// without inventing recurrence semantics, so it remains relevant and
		// makes the recurring monthly reference unavailable at the caller.
		return true, nil
	}
	minimumOffset, err := checkedSubtract(startSequence, effectiveStart)
	if err != nil {
		return false, fmt.Errorf("recurrence start sequence: %w", err)
	}
	if offset < minimumOffset {
		offset = minimumOffset
	}
	if offset < 0 {
		offset = 0
	}
	candidateCivil, err := recurringPlanningCivilDateFromOffset(startCivil, frequency, offset)
	if err != nil {
		return false, err
	}
	candidateSequence, err := checkedAdd(effectiveStart, offset)
	if err != nil {
		return false, fmt.Errorf("recurrence sequence: %w", err)
	}
	return candidateSequence >= startSequence && candidateSequence <= endSequence && civildate.CompareCivilDate(candidateCivil, upperCivil) <= 0, nil
}

func recurringOffsetAtOrAfter(baseCivil, startCivil civildate.CivilDate, asOf, startsAt, frequency string) (int64, bool, error) {
	if civildate.CompareCivilDate(startCivil, baseCivil) >= 0 {
		return 0, true, nil
	}

	switch frequency {
	case models.RecurrenceFrequencyDaily, models.RecurrenceFrequencyWeekly, models.RecurrenceFrequencyBiweekly:
		stepDays := int64(1)
		if frequency == models.RecurrenceFrequencyWeekly {
			stepDays = 7
		} else if frequency == models.RecurrenceFrequencyBiweekly {
			stepDays = 14
		}
		days := civildate.CivilDateDaysBetween(startCivil, baseCivil)
		offset := days / stepDays
		candidate, err := recurringPlanningCivilDateFromOffset(startCivil, frequency, offset)
		if err != nil {
			return 0, true, err
		}
		if civildate.CompareCivilDate(candidate, baseCivil) < 0 {
			offset++
		}
		return offset, true, nil

	case models.RecurrenceFrequencyMonthly, models.RecurrenceFrequencyBimonthly,
		models.RecurrenceFrequencyQuarterly, models.RecurrenceFrequencySemiannual,
		models.RecurrenceFrequencyAnnual:
		stepMonths := 1
		switch frequency {
		case models.RecurrenceFrequencyBimonthly:
			stepMonths = 2
		case models.RecurrenceFrequencyQuarterly:
			stepMonths = 3
		case models.RecurrenceFrequencySemiannual:
			stepMonths = 6
		case models.RecurrenceFrequencyAnnual:
			stepMonths = 12
		}
		baseMonth, err := civilMonthIndex(asOf)
		if err != nil {
			return 0, true, err
		}
		startMonth, err := civilMonthIndex(startsAt)
		if err != nil {
			return 0, true, err
		}
		offset := int64((baseMonth - startMonth) / stepMonths)
		candidate, err := recurringPlanningCivilDateFromOffset(startCivil, frequency, offset)
		if err != nil {
			return 0, true, err
		}
		if civildate.CompareCivilDate(candidate, baseCivil) < 0 {
			offset++
		}
		return offset, true, nil
	default:
		return 0, false, nil
	}
}

func recurringPlanningCivilDateFromOffset(anchor civildate.CivilDate, frequency string, offset int64) (civildate.CivilDate, error) {
	if offset < 0 {
		return civildate.CivilDate{}, fmt.Errorf("recurrence offset must be non-negative, got %d", offset)
	}
	maxInt := int64(^uint(0) >> 1)
	switch frequency {
	case models.RecurrenceFrequencyDaily:
		if offset > maxInt {
			return civildate.CivilDate{}, fmt.Errorf("recurrence daily offset %d overflows int", offset)
		}
		return civildate.CivilDateAddDays(anchor, int(offset))
	case models.RecurrenceFrequencyWeekly, models.RecurrenceFrequencyBiweekly:
		step := int64(7)
		if frequency == models.RecurrenceFrequencyBiweekly {
			step = 14
		}
		if offset > maxInt/step {
			return civildate.CivilDate{}, fmt.Errorf("recurrence day offset %d overflows int", offset)
		}
		return civildate.CivilDateAddDays(anchor, int(offset*step))
	case models.RecurrenceFrequencyMonthly, models.RecurrenceFrequencyBimonthly,
		models.RecurrenceFrequencyQuarterly, models.RecurrenceFrequencySemiannual,
		models.RecurrenceFrequencyAnnual:
		step := int64(1)
		switch frequency {
		case models.RecurrenceFrequencyBimonthly:
			step = 2
		case models.RecurrenceFrequencyQuarterly:
			step = 3
		case models.RecurrenceFrequencySemiannual:
			step = 6
		case models.RecurrenceFrequencyAnnual:
			step = 12
		}
		if offset > maxInt/step {
			return civildate.CivilDate{}, fmt.Errorf("recurrence month offset %d overflows int", offset)
		}
		return civildate.CivilDateAddMonths(anchor, int(offset*step))
	default:
		return civildate.CivilDate{}, fmt.Errorf("unknown recurrence frequency %q", frequency)
	}
}

func civilMonthIndex(value string) (int, error) {
	if len(value) != len("2006-01-02") {
		return 0, fmt.Errorf("invalid civil date %q", value)
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil {
		return 0, fmt.Errorf("invalid civil year %q: %w", value, err)
	}
	month, err := strconv.Atoi(value[5:7])
	if err != nil {
		return 0, fmt.Errorf("invalid civil month %q: %w", value, err)
	}
	return year*12 + month - 1, nil
}

type historicalMonthlyCostResult struct {
	Months                int
	Available             bool
	TotalAverageCents     int64
	EssentialAverageCents int64
}

func historicalMonthlyCosts(ctx context.Context, db monthlyPlanningQuerier, workspaceID string, base time.Time) (historicalMonthlyCostResult, error) {
	local := base.In(base.Location())
	currentStart, _, err := civildate.CivilMonthBounds(local.Year(), local.Month())
	if err != nil {
		return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning current month: %w", err)
	}
	windowStart, err := civildate.CivilDateAddMonths(currentStart, -6)
	if err != nil {
		return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning historical window: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT
			substr(COALESCE(NULLIF(t.date_civil, ''), strftime('%Y-%m-%d', datetime(t.date, 'unixepoch'))), 1, 7),
			COALESCE(SUM(t.amount), 0),
			COALESCE(SUM(CASE
				WHEN COALESCE(NULLIF(c.macro_group, ''), NULLIF(p.macro_group, ''), '') = ? THEN t.amount
				ELSE 0
			END), 0)
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			AND t.type = 'EXPENSE'
			AND COALESCE(NULLIF(t.date_civil, ''), strftime('%Y-%m-%d', datetime(t.date, 'unixepoch'))) >= ?
			AND COALESCE(NULLIF(t.date_civil, ''), strftime('%Y-%m-%d', datetime(t.date, 'unixepoch'))) < ?
			AND NOT EXISTS (
				SELECT 1
				FROM invoice_payments ip
				WHERE ip.workspace_id = t.workspace_id
					AND (ip.transaction_id = t.id OR ip.reversal_transaction_id = t.id)
			)
		GROUP BY substr(COALESCE(NULLIF(t.date_civil, ''), strftime('%Y-%m-%d', datetime(t.date, 'unixepoch'))), 1, 7)
		ORDER BY 1
	`, categorycontract.MacroPersonalEssencial, workspaceID, windowStart.String(), currentStart.String())
	if err != nil {
		return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning historical costs workspace %q: %w", workspaceID, err)
	}
	defer rows.Close()

	var totalSum, essentialSum int64
	months := 0
	for rows.Next() {
		var month string
		var total, essential int64
		if err := rows.Scan(&month, &total, &essential); err != nil {
			return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning scan historical costs: %w", err)
		}
		if len(month) != 7 {
			return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning invalid historical month %q", month)
		}
		totalSum, err = checkedAdd(totalSum, total)
		if err != nil {
			return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning total cost overflow: %w", err)
		}
		essentialSum, err = checkedAdd(essentialSum, essential)
		if err != nil {
			return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning essential cost overflow: %w", err)
		}
		months++
	}
	if err := rows.Err(); err != nil {
		return historicalMonthlyCostResult{}, fmt.Errorf("monthly planning historical cost rows: %w", err)
	}
	result := historicalMonthlyCostResult{Months: months}
	if months < 3 {
		return result, nil
	}
	result.Available = true
	result.TotalAverageCents = roundedAverage(totalSum, int64(months))
	result.EssentialAverageCents = roundedAverage(essentialSum, int64(months))
	return result, nil
}

func coverageInTenths(available, essential int64) (int64, error) {
	if essential <= 0 {
		return 0, nil
	}
	if available <= 0 {
		return 0, nil
	}
	whole := available / essential
	if whole > math.MaxInt64/10 {
		return 0, fmt.Errorf("fixed-precision coverage overflow")
	}
	remainder := available % essential
	hi, lo := bits.Mul64(uint64(remainder), 10)
	fraction, fractionRemainder := bits.Div64(hi, lo, uint64(essential))
	if fractionRemainder >= (uint64(essential)+1)/2 {
		fraction++
	}
	base := whole * 10
	if fraction > uint64(math.MaxInt64-base) {
		return 0, fmt.Errorf("fixed-precision coverage overflow")
	}
	return base + int64(fraction), nil
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("int64 overflow")
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, fmt.Errorf("int64 underflow")
	}
	return left + right, nil
}

func checkedSubtract(left, right int64) (int64, error) {
	if right > 0 && left < math.MinInt64+right {
		return 0, fmt.Errorf("int64 underflow")
	}
	if right < 0 && left > math.MaxInt64+right {
		return 0, fmt.Errorf("int64 overflow")
	}
	return left - right, nil
}

func roundedAverage(total, count int64) int64 {
	quotient := total / count
	if (total%count)*2 >= count {
		quotient++
	}
	return quotient
}

func nullableMonthlyReference(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}
