package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

// FinancialProjectionStatus describes whether a month can be read as a
// complete projection, a partial projection, or not at all.
type FinancialProjectionStatus string

const (
	FinancialProjectionComplete    FinancialProjectionStatus = "COMPLETE"
	FinancialProjectionPartial     FinancialProjectionStatus = "PARTIAL"
	FinancialProjectionUnavailable FinancialProjectionStatus = "UNAVAILABLE"
)

// FinancialProjectionRealized separates operational cash income from invoice
// payment reversals, which are cash adjustments rather than revenue.
type FinancialProjectionRealized struct {
	OperationalIncome MoneyDisplay
	AdjustmentIncome  MoneyDisplay
	Expense           MoneyDisplay
	Net               MoneyDisplay
	Count             int
}

// FinancialProjectionPending exposes only amounts with a reliable due date.
// Items without a due date are counted separately and never assigned to a
// month by their competence date.
type FinancialProjectionPending struct {
	Income              MoneyDisplay
	Expense             MoneyDisplay
	Net                 MoneyDisplay
	Count               int
	WithoutDueDateCount int
	HasRegistered       bool
	Available           bool
}

// FinancialProjectionProjected is the cash result if the reliable registered
// pending items in the month are settled. It is not a bank balance or profit.
type FinancialProjectionProjected struct {
	Income    MoneyDisplay
	Expense   MoneyDisplay
	Net       MoneyDisplay
	Available bool
}

// FinancialProjectionMonth is one civil month in the three-month horizon.
type FinancialProjectionMonth struct {
	Key       string
	Label     string
	StartUnix int64
	EndUnix   int64
	IsPast    bool
	IsCurrent bool
	IsFuture  bool
	State     FinancialProjectionStatus
	Reasons   []string
	Realized  FinancialProjectionRealized
	Pending   FinancialProjectionPending
	Projected FinancialProjectionProjected
}

// FinancialProjectionOverdue is the separate prior-month overdue bucket. It is
// never silently added to a month's projected result.
type FinancialProjectionOverdue struct {
	Income              MoneyDisplay
	Expense             MoneyDisplay
	Net                 MoneyDisplay
	Count               int
	WithoutDueDateCount int
	HasRegistered       bool
	Available           bool
	State               FinancialProjectionStatus
	Reasons             []string
}

// FinancialProjectionUndated is the workspace-level count/amount of pending
// items without a reliable due date. These items are not allocated to months.
type FinancialProjectionUndated struct {
	Income        MoneyDisplay
	Expense       MoneyDisplay
	Net           MoneyDisplay
	Count         int
	HasRegistered bool
	Available     bool
}

// FinancialProjectionRecurrence reports whether the materialization state
// covers the requested three-month horizon. This layer never materializes.
type FinancialProjectionRecurrence struct {
	Covered           bool
	ActiveRecurring   bool
	MaterializedUntil string
	State             FinancialProjectionStatus
	Reason            string
}

// DashboardFinancialProjection is the read-only contract for the future
// Visão financeira card. It does not include saldo projetado or available cash.
type DashboardFinancialProjection struct {
	WorkspaceID                 string
	SelectedMonth               string
	SelectedMonthLabel          string
	GeneratedAt                 int64
	SnapshotConsistent          bool
	ObligationsAvailable        bool
	ObligationsReason           string
	ObligationsWithoutDateCount int64
	State                       FinancialProjectionStatus
	Reasons                     []string
	Months                      []FinancialProjectionMonth
	Overdue                     FinancialProjectionOverdue
	Undated                     FinancialProjectionUndated
	Recurrence                  FinancialProjectionRecurrence
}

// BuildDashboardFinancialProjection reads the projection from one read-only
// SQLite snapshot. It never commits, writes or materializes recurring entries.
func BuildDashboardFinancialProjection(ctx context.Context, db *sql.DB, workspaceID string, selectedMonth, now time.Time) (DashboardFinancialProjection, error) {
	var projection DashboardFinancialProjection
	if db == nil {
		return projection, fmt.Errorf("dashboard financial projection: db obrigatório")
	}
	workspaceID = trimProjectionWorkspace(workspaceID)
	if workspaceID == "" {
		return projection, fmt.Errorf("dashboard financial projection: workspace obrigatório")
	}
	if err := ctx.Err(); err != nil {
		return projection, err
	}

	selected := time.Date(selectedMonth.Year(), selectedMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return projection, fmt.Errorf("dashboard financial projection: abrir snapshot: %w", err)
	}
	defer tx.Rollback()

	return buildDashboardFinancialProjectionTx(ctx, tx, workspaceID, selected, now)
}

func trimProjectionWorkspace(value string) string {
	return strings.TrimSpace(value)
}

// buildDashboardFinancialProjectionTx is split from the public entrypoint so
// tests can prove that all components observe one transaction snapshot.
func buildDashboardFinancialProjectionTx(ctx context.Context, tx *sql.Tx, workspaceID string, selectedMonth, now time.Time) (DashboardFinancialProjection, error) {
	return buildDashboardFinancialProjectionTxWithObligations(ctx, tx, workspaceID, selectedMonth, now, loadWorkspaceObligationsF1)
}

func buildDashboardFinancialProjectionTxWithObligations(ctx context.Context, tx *sql.Tx, workspaceID string, selectedMonth, now time.Time, probe services.WorkspaceObligationsProbe) (DashboardFinancialProjection, error) {
	var projection DashboardFinancialProjection
	if tx == nil {
		return projection, fmt.Errorf("dashboard financial projection: tx obrigatória")
	}
	if err := ctx.Err(); err != nil {
		return projection, err
	}

	// Widen the SQL window by one civil day; the month/civil filter below is
	// authoritative and prevents boundary events from leaking between months.
	period := ReportPeriod{
		Start: selectedMonth.AddDate(0, 0, -1).Unix(),
		End:   selectedMonth.AddDate(0, 3, 0).AddDate(0, 0, 1).Unix() - 1,
	}
	realized, projected, err := buildPrevisaoCashFlowData(tx, workspaceID, period)
	if err != nil {
		return projection, fmt.Errorf("dashboard financial projection: realizado/pendente: %w", err)
	}

	overdueEvents := make([]CashEvent, 0)
	if selectedMonth.Unix() > 0 {
		// The same one-day widening applies here: the civil-date cut below is
		// what decides the bucket, so an event close to the month boundary is
		// counted exactly once (overdue or first month), never both.
		overduePeriod := ReportPeriod{Start: 0, End: selectedMonth.AddDate(0, 0, 1).Unix() - 1}
		overdueTransactions, err := queryProjectedCashEventsByDueDate(tx, workspaceID, overduePeriod)
		if err != nil {
			return projection, fmt.Errorf("dashboard financial projection: atrasados: %w", err)
		}
		overdueInvoices, err := queryInvoiceCashEventsByDueDate(tx, workspaceID, overduePeriod)
		if err != nil {
			return projection, fmt.Errorf("dashboard financial projection: faturas atrasadas: %w", err)
		}
		overdueCut := selectedMonth.Format("2006-01-02")
		for _, event := range append(overdueTransactions, overdueInvoices...) {
			if cashEventCivilDate(event) < overdueCut {
				overdueEvents = append(overdueEvents, event)
			}
		}
	}

	obligations, err := probe(ctx, tx, workspaceID)
	if err != nil {
		return projection, fmt.Errorf("dashboard financial projection: obrigações: %w", err)
	}
	obligationsAvailable := projectionObligationsAvailable(obligations)

	horizonEnd := selectedMonth.AddDate(0, 3, 0).AddDate(0, 0, -1)
	recurrence, err := loadProjectionRecurrence(ctx, tx, workspaceID, horizonEnd)
	if err != nil {
		return projection, fmt.Errorf("dashboard financial projection: recorrências: %w", err)
	}

	undated, err := loadProjectionUndated(ctx, tx, workspaceID)
	if err != nil {
		return projection, fmt.Errorf("dashboard financial projection: itens sem vencimento: %w", err)
	}

	currentMonthStart := appMonthStartUTC(now)
	months := make([]FinancialProjectionMonth, 0, 3)
	for i := 0; i < 3; i++ {
		start := selectedMonth.AddDate(0, i, 0)
		end := selectedMonth.AddDate(0, i+1, 0)
		month, err := buildProjectionMonth(start, end, currentMonthStart, realized, projected, obligationsAvailable, obligations.DueWithoutDateCount > 0, recurrence, undated)
		if err != nil {
			return projection, err
		}
		months = append(months, month)
	}

	overdue, err := buildProjectionOverdue(overdueEvents, selectedMonth.Before(currentMonthStart), obligationsAvailable, undated)
	if err != nil {
		return projection, err
	}

	overall := projectionOverallState(months, overdue, recurrence, undated)
	reasons := projectionOverallReasons(obligationsAvailable, months, overdue, recurrence, undated)
	projection = DashboardFinancialProjection{
		WorkspaceID:                 workspaceID,
		SelectedMonth:               selectedMonth.Format("2006-01"),
		SelectedMonthLabel:          dashboardCashSummaryMonthLabel(time.Date(selectedMonth.Year(), selectedMonth.Month(), 1, 12, 0, 0, 0, time.UTC)),
		GeneratedAt:                 now.Unix(),
		SnapshotConsistent:          true,
		ObligationsAvailable:        obligationsAvailable,
		ObligationsReason:           obligationsReasonText(obligations),
		ObligationsWithoutDateCount: obligations.DueWithoutDateCount,
		State:                       overall,
		Reasons:                     reasons,
		Months:                      months,
		Overdue:                     overdue,
		Undated:                     undated,
		Recurrence:                  recurrence,
	}
	return projection, nil
}

func obligationsReasonText(obligations services.WorkspaceObligationsF1) string {
	position := services.WorkspaceFinancialPositionF1{
		Status:                       services.AvailableBalanceReliable,
		Obligations:                  obligations.Amount,
		ObligationsDue30:             obligations.Due30,
		ObligationsDue60:             obligations.Due60,
		ObligationsWithoutDateCount:  obligations.DueWithoutDateCount,
		ObligationsScheduleAvailable: obligations.ScheduleAvailable,
		Reasons:                      obligations.Reasons,
	}
	return position.ReasonText()
}

func projectionObligationsAvailable(obligations services.WorkspaceObligationsF1) bool {
	position := services.WorkspaceFinancialPositionF1{
		Status:                       services.AvailableBalanceReliable,
		Obligations:                  obligations.Amount,
		ObligationsDue30:             obligations.Due30,
		ObligationsDue60:             obligations.Due60,
		ObligationsWithoutDateCount:  obligations.DueWithoutDateCount,
		ObligationsScheduleAvailable: obligations.ScheduleAvailable,
		Reasons:                      obligations.Reasons,
	}
	return financialIndicatorAvailability(position, nil).ObligationsAvailable
}

func loadProjectionRecurrence(ctx context.Context, tx *sql.Tx, workspaceID string, horizonEnd time.Time) (FinancialProjectionRecurrence, error) {
	result := FinancialProjectionRecurrence{
		Covered: true,
		State:   FinancialProjectionComplete,
		Reason:  "Sem recorrências ativas.",
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	var activeRules int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM recurring_rules
		WHERE workspace_id = ? AND active = 1
	`, workspaceID).Scan(&activeRules); err != nil {
		return result, fmt.Errorf("contar recorrências ativas: %w", err)
	}
	if activeRules == 0 {
		return result, nil
	}
	result.ActiveRecurring = true
	result.Covered = false
	result.State = FinancialProjectionPartial

	state, err := readRecurringMaterializationStateTx(tx, workspaceID)
	if err != nil {
		return result, fmt.Errorf("ler estado de materialização: %w", err)
	}
	if state == nil {
		result.Reason = "Estado de materialização recorrente ausente."
		return result, nil
	}
	if state.MaterializedUntilCivil.Valid {
		result.MaterializedUntil = state.MaterializedUntilCivil.String
	} else if state.MaterializedUntil > 0 {
		result.MaterializedUntil = time.Unix(state.MaterializedUntil, 0).UTC().Format("2006-01-02")
	}

	horizonCivil, err := NewCivilWriteDate(horizonEnd.Format("2006-01-02"))
	if err != nil {
		result.State = FinancialProjectionUnavailable
		result.Reason = "Horizonte civil de projeção inválido."
		return result, nil
	}
	covered, err := recurringStateIsFresh(state, horizonCivil)
	if err != nil {
		result.State = FinancialProjectionUnavailable
		result.Reason = "Estado de materialização recorrente inválido."
		return result, nil
	}
	result.Covered = covered
	if !covered {
		result.Reason = fmt.Sprintf("Materialização recorrente não cobre até %s.", horizonCivil.Civil())
		return result, nil
	}
	result.State = FinancialProjectionComplete
	result.Reason = "Materialização recorrente cobre o horizonte."
	return result, nil
}

func loadProjectionUndated(ctx context.Context, tx *sql.Tx, workspaceID string) (FinancialProjectionUndated, error) {
	result := FinancialProjectionUndated{Available: true}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT t.type, t.amount, w.type, t.date, t.date_civil, t.due_date
		FROM financial_transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		JOIN workspaces w ON w.id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.status = 'pending'
		  AND t.type IN ('INCOME', 'EXPENSE')
		  AND a.type != 'CREDIT_CARD'
		  AND t.invoice_id IS NULL
		  AND (t.due_date IS NULL OR t.due_date <= 0)
		  AND COALESCE(t.description, '') NOT LIKE ?
		  AND `+excludeInvoicePaymentCompetenceClause("t")+`
	`, workspaceID, invoicePaymentDescriptionPrefix+"%")
	if err != nil {
		return result, fmt.Errorf("consultar itens sem vencimento: %w", err)
	}
	defer rows.Close()

	var income, expense int64
	count := 0
	for rows.Next() {
		var kind, workspaceType string
		var amount, dateUnix int64
		var dateCivil sql.NullString
		var dueDate sql.NullInt64
		if err := rows.Scan(&kind, &amount, &workspaceType, &dateUnix, &dateCivil, &dueDate); err != nil {
			return result, fmt.Errorf("ler item sem vencimento: %w", err)
		}
		if amount <= 0 {
			if amount < 0 {
				return result, fmt.Errorf("item sem vencimento com valor negativo")
			}
			continue
		}
		if reliablePendingDate(workspaceType, dueDate, dateUnix, dateCivil) {
			continue
		}
		if kind == "INCOME" {
			income, err = addInt64Checked(income, amount)
		} else {
			expense, err = addInt64Checked(expense, amount)
		}
		if err != nil {
			return result, fmt.Errorf("somar itens sem vencimento: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterar itens sem vencimento: %w", err)
	}
	net, err := subInt64Checked(income, expense)
	if err != nil {
		return result, fmt.Errorf("resultado de itens sem vencimento: %w", err)
	}
	result.Income = MoneyMinor(income)
	result.Expense = MoneyMinor(expense)
	result.Net = MoneyMinor(net)
	result.Count = count
	result.HasRegistered = count > 0
	return result, nil
}

func buildProjectionMonth(start, end, currentMonthStart time.Time, realized, projected []CashEvent, obligationsAvailable, obligationsWithoutDate bool, recurrence FinancialProjectionRecurrence, undated FinancialProjectionUndated) (FinancialProjectionMonth, error) {
	month := FinancialProjectionMonth{
		Key:       start.Format("2006-01"),
		Label:     dashboardCashSummaryMonthLabel(time.Date(start.Year(), start.Month(), 1, 12, 0, 0, 0, time.UTC)),
		StartUnix: start.Unix(),
		EndUnix:   end.Unix() - 1,
		IsPast:    start.Before(currentMonthStart),
		IsCurrent: start.Equal(currentMonthStart),
		IsFuture:  start.After(currentMonthStart),
		State:     FinancialProjectionComplete,
	}
	var err error
	month.Realized, err = aggregateProjectionRealized(filterProjectionEvents(realized, start, end))
	if err != nil {
		return month, fmt.Errorf("dashboard financial projection: realizado de %s: %w", month.Key, err)
	}
	if month.IsPast {
		month.Pending.Available = false
		month.Projected.Available = false
		month.Reasons = append(month.Reasons, "Mês passado: somente valores realizados.")
		return month, nil
	}
	if !obligationsAvailable {
		month.State = FinancialProjectionUnavailable
		month.Pending.Available = false
		month.Projected.Available = false
		month.Reasons = append(month.Reasons, "Composição financeira indisponível para projetar pendências.")
		return month, nil
	}
	if recurrence.State == FinancialProjectionUnavailable {
		month.State = FinancialProjectionUnavailable
		month.Pending.Available = false
		month.Projected.Available = false
		month.Reasons = append(month.Reasons, recurrence.Reason)
		return month, nil
	}
	if obligationsWithoutDate {
		month.State = FinancialProjectionPartial
		month.Reasons = append(month.Reasons, "Há compromissos sem data utilizável na composição financeira.")
	}

	month.Pending, err = aggregateProjectionPending(filterProjectionEvents(projected, start, end))
	if err != nil {
		return month, fmt.Errorf("dashboard financial projection: pendente de %s: %w", month.Key, err)
	}
	month.Pending.Available = true
	month.Pending.HasRegistered = month.Pending.Count+month.Pending.WithoutDueDateCount > 0

	if month.Pending.Count == 0 {
		month.State = FinancialProjectionPartial
		month.Projected.Available = false
		if month.Pending.WithoutDueDateCount > 0 || undated.Count > 0 {
			month.Reasons = append(month.Reasons, "Há compromissos sem vencimento; a projeção não foi alocada por mês.")
		} else {
			month.Reasons = append(month.Reasons, "Nenhum compromisso pendente registrado; a projeção não presume compromissos futuros.")
		}
		return month, nil
	}

	month.Projected, err = aggregateProjectedTotals(month.Realized, month.Pending)
	if err != nil {
		return month, fmt.Errorf("dashboard financial projection: projetado de %s: %w", month.Key, err)
	}
	month.Projected.Available = true
	if recurrence.State != FinancialProjectionComplete {
		month.State = FinancialProjectionPartial
		month.Reasons = append(month.Reasons, recurrence.Reason)
	}
	if month.Pending.WithoutDueDateCount > 0 {
		month.State = FinancialProjectionPartial
		month.Reasons = append(month.Reasons, fmt.Sprintf("%d compromisso(s) sem vencimento não entram no total do mês.", month.Pending.WithoutDueDateCount))
	}
	if undated.Count > 0 {
		month.State = FinancialProjectionPartial
		month.Reasons = append(month.Reasons, fmt.Sprintf("%d compromisso(s) sem vencimento no workspace não entram no total do mês.", undated.Count))
	}
	if obligationsWithoutDate || month.Pending.WithoutDueDateCount > 0 || undated.Count > 0 {
		// A material undated set means the projected result is not a complete
		// cash-flow closing. Keep the available components, but do not expose a
		// numeric projected result as if it were definitive.
		month.Projected.Available = false
	}
	return month, nil
}

func buildProjectionOverdue(events []CashEvent, selectedIsPast, obligationsAvailable bool, undated FinancialProjectionUndated) (FinancialProjectionOverdue, error) {
	result := FinancialProjectionOverdue{
		Available: true,
		State:     FinancialProjectionComplete,
	}
	if selectedIsPast {
		result.Available = false
		result.State = FinancialProjectionComplete
		result.Reasons = append(result.Reasons, "Mês passado: projeção de atrasados não exibida.")
		return result, nil
	}
	if !obligationsAvailable {
		result.Available = false
		result.State = FinancialProjectionUnavailable
		result.Reasons = append(result.Reasons, "Composição financeira indisponível para calcular atrasados.")
		return result, nil
	}
	pending, err := aggregateProjectionPending(events)
	if err != nil {
		return result, fmt.Errorf("dashboard financial projection: atrasados: %w", err)
	}
	result.Income = pending.Income
	result.Expense = pending.Expense
	result.Net = pending.Net
	result.Count = pending.Count
	result.WithoutDueDateCount = pending.WithoutDueDateCount
	result.HasRegistered = pending.Count+pending.WithoutDueDateCount > 0
	if result.HasRegistered {
		result.Reasons = append(result.Reasons, "Vencidos de meses anteriores são exibidos separadamente e não entram no resultado projetado do mês.")
	}
	if pending.WithoutDueDateCount > 0 || undated.Count > 0 {
		result.State = FinancialProjectionPartial
		result.Reasons = append(result.Reasons, "Há compromissos sem vencimento que podem afetar os atrasados.")
	}
	return result, nil
}

func aggregateProjectionRealized(events []CashEvent) (FinancialProjectionRealized, error) {
	var operationalIncome, adjustmentIncome, expense int64
	count := 0
	for _, event := range events {
		if event.AmountCents < 0 {
			return FinancialProjectionRealized{}, fmt.Errorf("evento realizado com valor negativo")
		}
		if event.AmountCents == 0 {
			continue
		}
		var err error
		if event.IsInvoicePaymentReversal {
			if !event.IsIncome {
				return FinancialProjectionRealized{}, fmt.Errorf("reversão de pagamento com tipo inválido")
			}
			adjustmentIncome, err = addInt64Checked(adjustmentIncome, event.AmountCents)
		} else if event.IsIncome {
			operationalIncome, err = addInt64Checked(operationalIncome, event.AmountCents)
		} else {
			expense, err = addInt64Checked(expense, event.AmountCents)
		}
		if err != nil {
			return FinancialProjectionRealized{}, err
		}
		count++
	}
	totalIncome, err := addInt64Checked(operationalIncome, adjustmentIncome)
	if err != nil {
		return FinancialProjectionRealized{}, err
	}
	net, err := subInt64Checked(totalIncome, expense)
	if err != nil {
		return FinancialProjectionRealized{}, err
	}
	return FinancialProjectionRealized{
		OperationalIncome: MoneyMinor(operationalIncome),
		AdjustmentIncome:  MoneyMinor(adjustmentIncome),
		Expense:           MoneyMinor(expense),
		Net:               MoneyMinor(net),
		Count:             count,
	}, nil
}

func aggregateProjectionPending(events []CashEvent) (FinancialProjectionPending, error) {
	var income, expense int64
	count, withoutDueDate := 0, 0
	for _, event := range events {
		if event.AmountCents < 0 {
			return FinancialProjectionPending{}, fmt.Errorf("evento pendente com valor negativo")
		}
		if event.AmountCents == 0 {
			continue
		}
		if !event.HasReliableDueDate {
			withoutDueDate++
			continue
		}
		var err error
		if event.IsIncome {
			income, err = addInt64Checked(income, event.AmountCents)
		} else {
			expense, err = addInt64Checked(expense, event.AmountCents)
		}
		if err != nil {
			return FinancialProjectionPending{}, err
		}
		count++
	}
	net, err := subInt64Checked(income, expense)
	if err != nil {
		return FinancialProjectionPending{}, err
	}
	return FinancialProjectionPending{
		Income:              MoneyMinor(income),
		Expense:             MoneyMinor(expense),
		Net:                 MoneyMinor(net),
		Count:               count,
		WithoutDueDateCount: withoutDueDate,
		HasRegistered:       count+withoutDueDate > 0,
	}, nil
}

func aggregateProjectedTotals(realized FinancialProjectionRealized, pending FinancialProjectionPending) (FinancialProjectionProjected, error) {
	income, err := addInt64Checked(realized.OperationalIncome.rawCents(), realized.AdjustmentIncome.rawCents())
	if err != nil {
		return FinancialProjectionProjected{}, err
	}
	income, err = addInt64Checked(income, pending.Income.rawCents())
	if err != nil {
		return FinancialProjectionProjected{}, err
	}
	expense, err := addInt64Checked(realized.Expense.rawCents(), pending.Expense.rawCents())
	if err != nil {
		return FinancialProjectionProjected{}, err
	}
	net, err := subInt64Checked(income, expense)
	if err != nil {
		return FinancialProjectionProjected{}, err
	}
	return FinancialProjectionProjected{
		Income:  MoneyMinor(income),
		Expense: MoneyMinor(expense),
		Net:     MoneyMinor(net),
	}, nil
}

func filterProjectionEvents(events []CashEvent, start, end time.Time) []CashEvent {
	filtered := make([]CashEvent, 0)
	startKey := start.Format("2006-01-02")
	endKey := end.Format("2006-01-02")
	for _, event := range events {
		date := cashEventCivilDate(event)
		if date >= startKey && date < endKey {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func projectionOverallState(months []FinancialProjectionMonth, overdue FinancialProjectionOverdue, recurrence FinancialProjectionRecurrence, undated FinancialProjectionUndated) FinancialProjectionStatus {
	allPast := true
	for _, month := range months {
		if !month.IsPast {
			allPast = false
			break
		}
	}
	if allPast {
		return FinancialProjectionComplete
	}
	hasComplete, hasPartial, hasUnavailable := false, false, false
	for _, month := range months {
		switch month.State {
		case FinancialProjectionComplete:
			hasComplete = true
		case FinancialProjectionPartial:
			hasPartial = true
		case FinancialProjectionUnavailable:
			hasUnavailable = true
		}
	}
	if overdue.State == FinancialProjectionPartial {
		hasPartial = true
	}
	if overdue.State == FinancialProjectionUnavailable {
		hasUnavailable = true
	}
	if recurrence.State == FinancialProjectionPartial {
		hasPartial = true
	}
	if recurrence.State == FinancialProjectionUnavailable {
		hasUnavailable = true
	}
	if undated.HasRegistered {
		hasPartial = true
	}
	if !hasComplete && !hasPartial && hasUnavailable {
		return FinancialProjectionUnavailable
	}
	if hasPartial || hasUnavailable {
		return FinancialProjectionPartial
	}
	return FinancialProjectionComplete
}

func projectionOverallReasons(obligationsAvailable bool, months []FinancialProjectionMonth, overdue FinancialProjectionOverdue, recurrence FinancialProjectionRecurrence, undated FinancialProjectionUndated) []string {
	allPast := true
	for _, month := range months {
		if !month.IsPast {
			allPast = false
			break
		}
	}
	if allPast {
		return nil
	}
	reasons := make([]string, 0)
	if !obligationsAvailable {
		reasons = append(reasons, "Composição financeira indisponível; pendências e projeção não foram calculadas.")
	}
	if recurrence.Reason != "" && recurrence.State != FinancialProjectionComplete {
		reasons = append(reasons, recurrence.Reason)
	}
	if undated.HasRegistered {
		reasons = append(reasons, fmt.Sprintf("%d compromisso(s) sem vencimento não entram nos totais mensais.", undated.Count))
	}
	for _, month := range months {
		if month.State == FinancialProjectionUnavailable {
			for _, reason := range month.Reasons {
				reasons = append(reasons, reason)
			}
			break
		}
	}
	if overdue.State == FinancialProjectionPartial {
		reasons = append(reasons, overdue.Reasons...)
	}
	return uniqueProjectionReasons(reasons)
}

func uniqueProjectionReasons(reasons []string) []string {
	seen := make(map[string]bool, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		out = append(out, reason)
	}
	return out
}
