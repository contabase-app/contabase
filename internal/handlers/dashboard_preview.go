package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

// DashboardV2Handler renders the canonical dashboard at /.
type DashboardV2Handler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	UserID      string
	WorkspaceID string
}

const dashboardCanonicalBasePath = "/"

const (
	previewCashFlowDefaultRange = "6m"
	previewCashFlowWideRange    = "12m"
	previewChartWidth           = 640
	previewChartHeight          = 220
	previewChartPadLeft         = 54
	previewChartPadRight        = 12
	previewChartPadTop          = 14
	previewChartPadBottom       = 30
)

type DashboardPreviewData struct {
	DashboardData
	DashboardRefreshURL string
	LayoutPending       bool
	Invested            PreviewInvested
	BalanceComposition  PreviewBalanceComposition
	Financial           PreviewFinancialView
	CashFlow            PreviewCashFlow
	CategoryBreakdown   []PreviewCategoryItem
	PreviewAccounts     []PreviewAccount
	PreviewCards        []PreviewCreditCard
	CardsPeriodAware    bool
	SelectedMonth       string
	SelectedMonthLabel  string
	MonthFilter         PreviewMonthFilter
}

type PreviewMonthFilter struct {
	Label         string
	PreviousLabel string
	NextLabel     string
	PreviousHref  string
	NextHref      string
	CurrentHref   string
	CanGoNext     bool
	IsCurrent     bool
}

type PreviewInvested struct {
	Available    bool
	Money        MoneyDisplay
	AccountCount int
	Note         string
}

// PreviewBalanceComposition is a presentation-only split of the same accounts
// that already form the Saldo total. It is only available when the account
// snapshot can be proven to match the validated real balance exactly.
type PreviewBalanceComposition struct {
	Available            bool
	Total                MoneyDisplay
	Invested             MoneyDisplay
	Other                MoneyDisplay
	InvestedPercent      int
	OtherPercent         int
	InvestedBarPercent   int
	OtherBarPercent      int
	InvestedShareLabel   string
	OtherShareLabel      string
	InvestedAccountCount int
	OtherAccountCount    int
	AriaLabel            string
}

type PreviewFinancialView struct {
	Available                   bool
	Error                       bool
	ErrorMessage                string
	MonthLabel                  string
	State                       string
	StateLabel                  string
	StateClass                  string
	Months                      []PreviewFinancialMonth
	Selected                    PreviewFinancialMonth
	Overdue                     PreviewFinancialOverdue
	Undated                     PreviewFinancialUndated
	Recurrence                  PreviewFinancialRecurrence
	ObligationsWithoutDateCount int
	HasAdjustments              bool
}

type PreviewFinancialMonth struct {
	Key                 string
	Label               string
	TabLabel            string
	TabAriaLabel        string
	IsPast              bool
	IsCurrent           bool
	IsFuture            bool
	State               string
	StateLabel          string
	StateClass          string
	StateReason         string
	RealizedIncome      MoneyDisplay
	AdjustmentIncome    MoneyDisplay
	HasAdjustments      bool
	RealizedExpense     MoneyDisplay
	RealizedNet         MoneyDisplay
	RealizedNegative    bool
	RealizedToneClass   string
	PendingIncome       MoneyDisplay
	PendingExpense      MoneyDisplay
	PendingNet          MoneyDisplay
	PendingAvailable    bool
	PendingCount        int
	HasPendingItems     bool
	WithoutDueDateCount int
	NoPendingRegistered bool
	HasRegistered       bool
	HasPendingIncome    bool
	HasPendingExpense   bool
	ProjectedNet        MoneyDisplay
	ProjectedAvailable  bool
	ProjectedNegative   bool
	ProjectedLabel      string
	ProjectedToneClass  string
	HasRealized         bool
}

type PreviewFinancialOverdue struct {
	Available           bool
	HasRegistered       bool
	Count               int
	WithoutDueDateCount int
	Income              MoneyDisplay
	Expense             MoneyDisplay
	Net                 MoneyDisplay
	HasIncome           bool
	HasExpense          bool
	NetNegative         bool
	StateLabel          string
	StateClass          string
}

type PreviewFinancialUndated struct {
	HasRegistered bool
	Count         int
}

type PreviewFinancialRecurrence struct {
	Active      bool
	Covered     bool
	ShowWarning bool
	Warning     string
	ToneClass   string
}

type PreviewCashFlow struct {
	Available         bool
	HasData           bool
	Range             string
	RangeLabel        string
	RangeOptions      []PreviewCashFlowRange
	Months            []PreviewCashFlowMonth
	GridLines         []PreviewCashFlowGridLine
	TotalIncome       MoneyDisplay
	OperationalIncome MoneyDisplay
	AdjustmentIncome  MoneyDisplay
	HasAdjustments    bool
	TotalExpense      MoneyDisplay
	Net               MoneyDisplay
	NetNegative       bool
	NetLabel          string
	NetToneClass      string
	ChartWidth        int
	ChartHeight       int
	PlotLeft          int
	PlotRight         int
	BaselineY         int
	MaxLabel          string
	RetryHref         string
}

type PreviewCashFlowGridLine struct {
	Y     int
	Label string
}

type PreviewCashFlowRange struct {
	Value  string
	Label  string
	Href   string
	Active bool
}

type PreviewCashFlowMonth struct {
	Key          string
	Label        string
	FullLabel    string
	Income       MoneyDisplay
	Expense      MoneyDisplay
	Net          MoneyDisplay
	NetNegative  bool
	NetLabel     string
	NetToneClass string
	IsCurrent    bool
	IncomeX      int
	IncomeY      int
	IncomeW      int
	IncomeH      int
	ExpenseX     int
	ExpenseY     int
	ExpenseW     int
	ExpenseH     int
	LabelX       int
	LabelY       int
	HitX         int
	HitY         int
	HitW         int
	HitH         int
}

type PreviewCategoryItem struct {
	Name         string
	Icon         string
	Amount       MoneyDisplay
	ShareLabel   string
	SharePercent int
	BarPercent   int
	ColorClass   string
}

type PreviewAccount struct {
	AccountCard
	ToneClass string
}

type PreviewCreditCard struct {
	CreditCardCard
}

func BuildDashboardPreviewData(db *sql.DB, userID, workspaceID, cashFlowRange, month string) DashboardPreviewData {
	return buildDashboardPreviewDataAtContext(context.Background(), db, userID, workspaceID, time.Now(), cashFlowRange, month)
}

func buildDashboardPreviewDataAt(db *sql.DB, userID, workspaceID string, now time.Time, cashFlowRange, monthRaw string) DashboardPreviewData {
	return buildDashboardPreviewDataAtContext(context.Background(), db, userID, workspaceID, now, cashFlowRange, monthRaw)
}

func buildDashboardPreviewDataAtContext(ctx context.Context, db *sql.DB, userID, workspaceID string, now time.Time, cashFlowRange, monthRaw string) DashboardPreviewData {
	return buildDashboardV2DataAtContext(ctx, db, userID, workspaceID, now, cashFlowRange, monthRaw)
}

// buildDashboardV2DataFromSnapshot composes the canonical dashboard from one read transaction.
func buildDashboardV2DataFromSnapshot(ctx context.Context, tx *sql.Tx, userID, workspaceID string, now time.Time, cashFlowRange, monthRaw string) (DashboardPreviewData, error) {
	db := &dashboardSnapshotReader{Tx: tx}
	base := buildDashboardDataReadAt(db, userID, workspaceID, now)

	selectedMonth, isCurrentMonth := previewResolveMonth(now, monthRaw)
	selectedLabel := dashboardCashSummaryMonthLabel(selectedMonth)
	if !isCurrentMonth {
		base.Categories = BuildDashboardCategories(db, workspaceID, selectedMonth)
		base.Limits = queryDashboardLimits(db, workspaceID, selectedMonth)
	}

	data := DashboardPreviewData{
		DashboardData:      base,
		SelectedMonth:      selectedMonth.Format("2006-01"),
		SelectedMonthLabel: selectedLabel,
		MonthFilter:        previewMonthFilter(dashboardCanonicalBasePath, selectedMonth, isCurrentMonth, now, cashFlowRange),
	}
	rangeValue, _, _ := previewCashFlowRange(cashFlowRange)
	data.DashboardRefreshURL = dashboardMonthHref(dashboardCanonicalBasePath, previewSelectedMonthQuery(selectedMonth, isCurrentMonth), rangeValue)
	data.Invested = buildPreviewInvested(base)
	data.BalanceComposition = buildPreviewBalanceComposition(base)

	projectionMonth := time.Date(selectedMonth.Year(), selectedMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	financialProjection, financialErr := buildDashboardFinancialProjectionTxWithObligations(ctx, tx, workspaceID, projectionMonth, now, loadWorkspaceObligationsF1Strict)
	if financialErr != nil {
		db.record(financialErr)
		log.Printf("dashboard preview financial projection unavailable: %v", financialErr)
	}
	data.Financial = buildPreviewFinancialView(financialProjection, financialErr, selectedMonth)

	data.CashFlow = buildPreviewCashFlow(db, workspaceID, now, dashboardCanonicalBasePath, cashFlowRange, previewSelectedMonthQuery(selectedMonth, isCurrentMonth))
	data.CategoryBreakdown = buildPreviewCategories(base.Categories.Items, base.Categories.Total.rawCents())
	data.PreviewAccounts = buildPreviewAccounts(base.Accounts)
	data.PreviewCards = buildPreviewCards(base.Cards)
	if !isCurrentMonth {
		historicalCards, err := queryFaturasIndexCardsAt(db, workspaceID, selectedMonth.Year(), selectedMonth.Month(), now)
		if err != nil {
			db.record(err)
			log.Printf("dashboard preview historical cards error: %v", err)
		} else {
			data.CardsPeriodAware = true
			data.PreviewCards = buildPreviewCardsForPeriod(historicalCards, &selectedMonth)
		}
	}
	return data, db.readError()
}

func (h *DashboardV2Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	data, err := buildDashboardV2Snapshot(r.Context(), h.DB, h.UserID, h.WorkspaceID, time.Now(), r.URL.Query().Get("fluxo"), r.URL.Query().Get("mes"), r.Header.Get("HX-Request") == "")
	if err != nil {
		http.Error(w, "dashboard snapshot unavailable", http.StatusServiceUnavailable)
		return
	}
	// The anti-flash gate belongs to the first paint only. An HTMX refresh must
	// never hide the grid behind a 1.5s reveal animation.
	data.LayoutPending = r.Header.Get("HX-Request") == ""
	if !data.CardsPeriodAware {
		SortDashboardCardsByPaymentPriority(data.Cards, time.Now())
		data.PreviewCards = buildPreviewCards(data.Cards)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := h.Templates.ExecuteTemplate(w, "dashboard-preview-page", data); err != nil {
		log.Printf("dashboard template error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func buildPreviewInvested(base DashboardData) PreviewInvested {
	var total int64
	count := 0
	for _, account := range base.Accounts {
		if account.AccountType != models.AccountTypeInvestment {
			continue
		}
		next, err := addInt64Checked(total, account.Money.rawCents())
		if err != nil {
			return PreviewInvested{Note: "O total investido excede o limite numérico com segurança."}
		}
		total = next
		count++
	}

	note := "Nenhuma conta de investimento ativa."
	if count == 1 {
		note = "1 conta de investimento · já incluída no Saldo total."
	} else if count > 1 {
		note = fmt.Sprintf("%d contas de investimento · já incluídas no Saldo total.", count)
	}
	return PreviewInvested{Available: true, Money: MoneyMinor(total), AccountCount: count, Note: note}
}

// buildPreviewBalanceComposition proves that the account snapshot already used
// by the Dashboard is exactly the same set that forms the real balance. It does
// not query accounts again or infer a new financial rule.
func buildPreviewBalanceComposition(base DashboardData) PreviewBalanceComposition {
	if !base.Balance.RealBalanceAvailable {
		return PreviewBalanceComposition{}
	}
	total := base.Balance.Money.rawCents()
	if total <= 0 {
		return PreviewBalanceComposition{}
	}

	var accountSum, invested int64
	investedCount, otherCount := 0, 0
	for _, account := range base.Accounts {
		cents := account.Money.rawCents()
		if cents < 0 {
			return PreviewBalanceComposition{}
		}
		next, err := addInt64Checked(accountSum, cents)
		if err != nil {
			return PreviewBalanceComposition{}
		}
		accountSum = next
		if account.AccountType == models.AccountTypeInvestment {
			next, err = addInt64Checked(invested, cents)
			if err != nil {
				return PreviewBalanceComposition{}
			}
			invested = next
			investedCount++
			continue
		}
		otherCount++
	}
	if accountSum != total || invested <= 0 || invested > total {
		return PreviewBalanceComposition{}
	}

	other := total - invested
	investedPercent := previewRatioPercent(invested, total)
	investedBarPercent := investedPercent
	otherBarPercent := 100 - investedBarPercent
	if other > 0 {
		// Keep both segments visible without changing the financial values.
		if investedBarPercent < 1 {
			investedBarPercent = 1
		}
		if investedBarPercent > 99 {
			investedBarPercent = 99
		}
		otherBarPercent = 100 - investedBarPercent
	} else {
		investedBarPercent = 100
		otherBarPercent = 0
	}

	investedLabel := percentLabel(invested, total)
	otherLabel := percentLabel(other, total)
	return PreviewBalanceComposition{
		Available:            true,
		Total:                MoneyMinor(total),
		Invested:             MoneyMinor(invested),
		Other:                MoneyMinor(other),
		InvestedPercent:      investedPercent,
		OtherPercent:         100 - investedPercent,
		InvestedBarPercent:   investedBarPercent,
		OtherBarPercent:      otherBarPercent,
		InvestedShareLabel:   investedLabel,
		OtherShareLabel:      otherLabel,
		InvestedAccountCount: investedCount,
		OtherAccountCount:    otherCount,
		AriaLabel:            fmt.Sprintf("Composição do saldo: %s em investimentos e %s nas demais contas.", investedLabel, otherLabel),
	}
}

// buildPreviewFinancialView maps the approved projection contract to the
// card's presentation fields. It never recalculates a financial value.
func buildPreviewFinancialView(projection DashboardFinancialProjection, projectionErr error, selected time.Time) PreviewFinancialView {
	monthLabel := projection.SelectedMonthLabel
	if monthLabel == "" {
		monthLabel = dashboardCashSummaryMonthLabel(selected)
	}
	if projectionErr != nil {
		return PreviewFinancialView{
			Available:    false,
			Error:        true,
			ErrorMessage: "Não foi possível carregar a visão financeira agora. Tente novamente em instantes.",
			MonthLabel:   monthLabel,
		}
	}

	view := PreviewFinancialView{
		Available:                   true,
		MonthLabel:                  monthLabel,
		State:                       string(projection.State),
		ObligationsWithoutDateCount: int(projection.ObligationsWithoutDateCount),
		Recurrence:                  previewFinancialRecurrenceView(projection.Recurrence),
	}
	view.StateLabel, view.StateClass = previewFinancialStatePresentation(projection.State, false)

	for _, month := range projection.Months {
		view.Months = append(view.Months, previewFinancialMonthView(month))
	}
	if len(view.Months) > 0 {
		view.Selected = view.Months[0]
		view.State = view.Selected.State
		view.StateLabel = view.Selected.StateLabel
		view.StateClass = view.Selected.StateClass
		view.HasAdjustments = view.Selected.HasAdjustments
	} else {
		view.Available = false
		view.Error = true
		view.ErrorMessage = "Não foi possível carregar a visão financeira agora. Tente novamente em instantes."
	}
	view.Overdue = previewFinancialOverdueView(projection.Overdue)
	view.Undated = PreviewFinancialUndated{
		HasRegistered: projection.Undated.HasRegistered,
		Count:         projection.Undated.Count,
	}
	if view.Undated.HasRegistered || view.ObligationsWithoutDateCount > 0 {
		view.Selected.NoPendingRegistered = false
		if len(view.Months) > 0 {
			view.Months[0] = view.Selected
		}
	}
	if view.Recurrence.ShowWarning && strings.Contains(view.Selected.StateReason, "Recorr") {
		view.Recurrence.ShowWarning = false
	}
	return view
}

func previewFinancialMonthView(month FinancialProjectionMonth) PreviewFinancialMonth {
	tabLabel, tabAriaLabel := previewFinancialTabLabels(month.Key, month.Label)
	view := PreviewFinancialMonth{
		Key:                 month.Key,
		Label:               month.Label,
		TabLabel:            tabLabel,
		TabAriaLabel:        tabAriaLabel,
		IsPast:              month.IsPast,
		IsCurrent:           month.IsCurrent,
		IsFuture:            month.IsFuture,
		State:               string(month.State),
		RealizedIncome:      month.Realized.OperationalIncome,
		AdjustmentIncome:    month.Realized.AdjustmentIncome,
		HasAdjustments:      month.Realized.AdjustmentIncome.rawCents() != 0,
		RealizedExpense:     month.Realized.Expense,
		RealizedNet:         month.Realized.Net,
		PendingIncome:       month.Pending.Income,
		PendingExpense:      month.Pending.Expense,
		PendingNet:          month.Pending.Net,
		PendingAvailable:    month.Pending.Available,
		PendingCount:        month.Pending.Count,
		HasPendingItems:     month.Pending.Available && month.Pending.Count > 0,
		WithoutDueDateCount: month.Pending.WithoutDueDateCount,
		NoPendingRegistered: month.Pending.Available && month.Pending.Count == 0 && month.Pending.WithoutDueDateCount == 0,
		HasRegistered:       month.Pending.HasRegistered,
		ProjectedNet:        month.Projected.Net,
		ProjectedAvailable:  month.Projected.Available,
		HasRealized:         month.Realized.Count > 0,
	}
	view.StateLabel, view.StateClass = previewFinancialStatePresentation(month.State, month.IsPast)
	view.StateReason = previewFinancialReason(month.Reasons)
	view.RealizedToneClass, view.RealizedNegative = previewFinancialTone(month.Realized.Net.rawCents())
	view.ProjectedToneClass, view.ProjectedNegative = previewFinancialTone(month.Projected.Net.rawCents())
	view.ProjectedLabel = "Resultado projetado"
	if view.ProjectedNegative {
		view.ProjectedLabel = "Déficit projetado"
	}
	view.HasPendingIncome = month.Pending.Income.rawCents() != 0
	view.HasPendingExpense = month.Pending.Expense.rawCents() != 0
	return view
}

func previewFinancialTabLabels(key, fallback string) (string, string) {
	parsed, err := time.Parse("2006-01", strings.TrimSpace(key))
	if err != nil {
		return fallback, fallback
	}
	short := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	full := []string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}
	month := int(parsed.Month()) - 1
	if month < 0 || month >= len(short) {
		return fallback, fallback
	}
	return short[month], fmt.Sprintf("%s, %s de %d", short[month], full[month], parsed.Year())
}

func previewFinancialOverdueView(overdue FinancialProjectionOverdue) PreviewFinancialOverdue {
	view := PreviewFinancialOverdue{
		Available:           overdue.Available,
		HasRegistered:       overdue.HasRegistered,
		Count:               overdue.Count,
		WithoutDueDateCount: overdue.WithoutDueDateCount,
		Income:              overdue.Income,
		Expense:             overdue.Expense,
		Net:                 overdue.Net,
		HasIncome:           overdue.Income.rawCents() != 0,
		HasExpense:          overdue.Expense.rawCents() != 0,
		NetNegative:         overdue.Net.rawCents() < 0,
	}
	view.StateLabel, view.StateClass = previewFinancialStatePresentation(overdue.State, false)
	return view
}

func previewFinancialRecurrenceView(recurrence FinancialProjectionRecurrence) PreviewFinancialRecurrence {
	view := PreviewFinancialRecurrence{
		Active:  recurrence.ActiveRecurring,
		Covered: recurrence.Covered,
	}
	if !recurrence.ActiveRecurring {
		return view
	}
	switch recurrence.State {
	case FinancialProjectionPartial:
		view.ShowWarning = true
		view.Warning = "Recorrências futuras podem estar incompletas."
		view.ToneClass = "is-partial"
	case FinancialProjectionUnavailable:
		view.ShowWarning = true
		view.Warning = "Não foi possível confirmar a cobertura das recorrências."
		view.ToneClass = "is-unavailable"
	}
	return view
}

func previewFinancialStatePresentation(state FinancialProjectionStatus, isPast bool) (string, string) {
	if isPast {
		return "Histórico", "is-history"
	}
	switch state {
	case FinancialProjectionComplete:
		return "Projeção completa", "is-complete"
	case FinancialProjectionPartial:
		return "Projeção parcial", "is-partial"
	case FinancialProjectionUnavailable:
		return "Projeção indisponível", "is-unavailable"
	default:
		return "Projeção indisponível", "is-unavailable"
	}
}

func previewFinancialReason(reasons []string) string {
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		switch {
		case strings.Contains(reason, "Materialização recorrente"):
			return "Recorrências futuras podem estar incompletas."
		case strings.Contains(reason, "Estado de materialização recorrente"):
			return "Não foi possível confirmar a cobertura das recorrências."
		case strings.Contains(reason, "sem vencimento") || strings.Contains(reason, "sem data utilizável"):
			return "Há compromissos sem vencimento não alocados a um mês."
		case strings.Contains(reason, "Composição financeira indisponível"):
			return "A composição financeira do workspace não está confiável."
		case strings.Contains(reason, "Nenhum compromisso pendente"):
			return "Nenhum compromisso pendente registrado; a projeção não presume compromissos futuros."
		case strings.Contains(reason, "Mês passado"):
			return "Histórico: somente valores realizados."
		default:
			return reason
		}
	}
	return ""
}

func previewFinancialTone(cents int64) (string, bool) {
	switch {
	case cents < 0:
		return "is-negative", true
	case cents > 0:
		return "is-positive", false
	default:
		return "is-neutral", false
	}
}

// previewNetPresentation names entradas minus saídas without turning a
// negative result into debt, delinquency or a bank balance claim.
func previewNetPresentation(net int64) (string, string) {
	switch {
	case net > 0:
		return "Economia do período", "is-positive"
	case net < 0:
		return "Déficit do período", "is-negative"
	default:
		return "Resultado do período", "is-neutral"
	}
}

func buildPreviewCashFlow(db reportQueryer, workspaceID string, now time.Time, basePath, rawRange, selectedMonthQuery string) PreviewCashFlow {
	rangeValue, monthCount, rangeLabel := previewCashFlowRange(rawRange)
	data := PreviewCashFlow{
		Available:    true,
		Range:        rangeValue,
		RangeLabel:   rangeLabel,
		RangeOptions: previewCashFlowRangeOptions(basePath, rangeValue, selectedMonthQuery),
		ChartWidth:   previewChartWidth,
		ChartHeight:  previewChartHeight,
		PlotLeft:     previewChartPadLeft,
		PlotRight:    previewChartWidth - previewChartPadRight,
		BaselineY:    previewChartHeight - previewChartPadBottom,
		MaxLabel:     "—",
		RetryHref:    dashboardMonthHref(basePath, selectedMonthQuery, rangeValue),
	}

	currentMonth := time.Date(appTime(now).Year(), appTime(now).Month(), 1, 0, 0, 0, 0, time.UTC)
	start := currentMonth.AddDate(0, -(monthCount - 1), 0)
	end := currentMonth.AddDate(0, 1, 0).Add(-time.Second)

	// One extra civil day on each side absorbs the application-timezone offset;
	// the month bucket below is decided by the persisted civil date.
	queryStart := start.AddDate(0, 0, -1)
	queryEnd := end.AddDate(0, 0, 1)
	events, err := buildRealizadoCashFlowData(db, workspaceID, ReportPeriod{Start: queryStart.Unix(), End: queryEnd.Unix()})
	if err != nil {
		recordDashboardReadError(db, err)
		data.Available = false
		return data
	}

	incomeByMonth := make([]int64, monthCount)
	expenseByMonth := make([]int64, monthCount)
	var totalIncome, operationalIncome, adjustmentIncome, totalExpense int64
	monthIndex := make(map[string]int, monthCount)
	labels := make([]string, monthCount)
	fullLabels := make([]string, monthCount)
	keys := make([]string, monthCount)
	for i := 0; i < monthCount; i++ {
		month := start.AddDate(0, i, 0)
		keys[i] = month.Format("2006-01")
		monthIndex[keys[i]] = i
		labels[i] = previewMonthShort(month)
		fullLabels[i] = previewMonthFull(month)
	}

	for _, event := range events {
		index, ok := monthIndex[cashEventCivilMonth(event)]
		if !ok {
			continue
		}
		if event.IsIncome {
			incomeByMonth[index], err = addInt64Checked(incomeByMonth[index], event.AmountCents)
			if event.IsInvoicePaymentReversal {
				adjustmentIncome, err = addInt64Checked(adjustmentIncome, event.AmountCents)
			} else {
				operationalIncome, err = addInt64Checked(operationalIncome, event.AmountCents)
			}
		} else {
			expenseByMonth[index], err = addInt64Checked(expenseByMonth[index], event.AmountCents)
		}
		if err != nil {
			data.Available = false
			return data
		}
	}

	var maxValue int64
	for i := 0; i < monthCount; i++ {
		if incomeByMonth[i] > maxValue {
			maxValue = incomeByMonth[i]
		}
		if expenseByMonth[i] > maxValue {
			maxValue = expenseByMonth[i]
		}
		totalIncome, err = addInt64Checked(totalIncome, incomeByMonth[i])
		if err != nil {
			data.Available = false
			return data
		}
		totalExpense, err = addInt64Checked(totalExpense, expenseByMonth[i])
		if err != nil {
			data.Available = false
			return data
		}
	}
	net, err := subInt64Checked(totalIncome, totalExpense)
	if err != nil {
		data.Available = false
		return data
	}
	data.TotalIncome = MoneyMinor(totalIncome)
	data.OperationalIncome = MoneyMinor(operationalIncome)
	data.AdjustmentIncome = MoneyMinor(adjustmentIncome)
	data.HasAdjustments = adjustmentIncome != 0
	data.TotalExpense = MoneyMinor(totalExpense)
	data.Net = MoneyMinor(net)
	data.NetNegative = net < 0
	data.NetLabel, data.NetToneClass = previewNetPresentation(net)
	data.MaxLabel = formatCurrencyLabel(maxValue)
	data.HasData = maxValue > 0

	plotLeft := previewChartPadLeft
	plotRight := previewChartWidth - previewChartPadRight
	plotTop := previewChartPadTop
	plotBottom := previewChartHeight - previewChartPadBottom
	plotWidth := plotRight - plotLeft
	plotHeight := plotBottom - plotTop
	groupWidth := float64(plotWidth) / float64(monthCount)
	barWidth := math.Min(14, math.Max(4, groupWidth*0.16))

	for _, ratio := range []float64{0.25, 0.5, 0.75} {
		value := int64(math.Round(float64(maxValue) * ratio))
		if value <= 0 {
			continue
		}
		data.GridLines = append(data.GridLines, PreviewCashFlowGridLine{
			Y:     plotBottom - previewBarHeight(value, maxValue, plotHeight),
			Label: previewChartAxisLabel(value),
		})
	}

	for i := 0; i < monthCount; i++ {
		incomeHeight := previewBarHeight(incomeByMonth[i], maxValue, plotHeight)
		expenseHeight := previewBarHeight(expenseByMonth[i], maxValue, plotHeight)
		center := float64(plotLeft) + groupWidth*(float64(i)+0.5)
		incomeX := int(math.Round(center - barWidth - 2))
		expenseX := int(math.Round(center + 2))
		labelX := int(math.Round(center))
		hitX := int(math.Round(float64(plotLeft) + groupWidth*float64(i)))
		hitW := int(math.Round(groupWidth))
		monthNet, netErr := subInt64Checked(incomeByMonth[i], expenseByMonth[i])
		if netErr != nil {
			data.Available = false
			return data
		}
		monthNetLabel, monthNetTone := previewNetPresentation(monthNet)
		data.Months = append(data.Months, PreviewCashFlowMonth{
			Key:          keys[i],
			Label:        labels[i],
			FullLabel:    fullLabels[i],
			Income:       MoneyMinor(incomeByMonth[i]),
			Expense:      MoneyMinor(expenseByMonth[i]),
			Net:          MoneyMinor(monthNet),
			NetNegative:  monthNet < 0,
			NetLabel:     monthNetLabel,
			NetToneClass: monthNetTone,
			IsCurrent:    i == monthCount-1,
			IncomeX:      incomeX,
			IncomeY:      plotBottom - incomeHeight,
			IncomeW:      int(math.Round(barWidth)),
			IncomeH:      incomeHeight,
			ExpenseX:     expenseX,
			ExpenseY:     plotBottom - expenseHeight,
			ExpenseW:     int(math.Round(barWidth)),
			ExpenseH:     expenseHeight,
			LabelX:       labelX,
			LabelY:       previewChartHeight - 9,
			HitX:         hitX,
			HitY:         plotTop,
			HitW:         hitW,
			HitH:         plotBottom - plotTop,
		})
	}
	return data
}

func buildPreviewCategories(items []DashboardCategoryItem, totalCents int64) []PreviewCategoryItem {
	if len(items) == 0 || totalCents <= 0 {
		return nil
	}
	var shownCents int64
	out := make([]PreviewCategoryItem, 0, len(items)+1)
	for _, item := range items {
		if item.AmountCents <= 0 {
			continue
		}
		icon := item.Icon
		if icon == "" {
			icon = "tag"
		}
		out = append(out, PreviewCategoryItem{
			Name:         item.Name,
			Icon:         icon,
			Amount:       item.Amount,
			ShareLabel:   item.ShareLabel,
			SharePercent: item.SharePercent,
			BarPercent:   item.BarPercent,
			ColorClass:   previewCategoryColorClass(item.Tone),
		})
		next, err := addInt64Checked(shownCents, item.AmountCents)
		if err != nil {
			return out
		}
		shownCents = next
	}

	otherCents := totalCents - shownCents
	if otherCents <= 0 {
		return out
	}
	barPercent := dashboardRoundedPercent(otherCents, totalCents)
	if barPercent < 1 {
		barPercent = 1
	}
	if barPercent > 100 {
		barPercent = 100
	}
	out = append(out, PreviewCategoryItem{
		Name:         "Outros",
		Icon:         "ellipsis",
		Amount:       MoneyMinor(otherCents),
		ShareLabel:   percentLabel(otherCents, totalCents),
		SharePercent: dashboardRoundedPercent(otherCents, totalCents),
		BarPercent:   barPercent,
		ColorClass:   "cbp-chart-other",
	})
	return out
}

func buildPreviewAccounts(accounts []AccountCard) []PreviewAccount {
	out := make([]PreviewAccount, 0, len(accounts))
	for i, account := range accounts {
		out = append(out, PreviewAccount{
			AccountCard: account,
			ToneClass:   fmt.Sprintf("cbp-avatar-tone-%d", i%5),
		})
	}
	return out
}

func buildPreviewCards(cards []CreditCardCard) []PreviewCreditCard {
	return buildPreviewCardsForPeriod(cards, nil)
}

func buildPreviewCardsForPeriod(cards []CreditCardCard, selected *time.Time) []PreviewCreditCard {
	out := make([]PreviewCreditCard, 0, len(cards))
	for index, card := range cards {
		statusTone := ""
		switch {
		case card.InvoiceID == "":
			statusTone = ""
		case card.Status == "PAID":
			statusTone = "is-paid"
		case card.Status == "CLOSED":
			statusTone = "is-closed"
		case card.StatusLabel == "Vencido":
			statusTone = "is-overdue"
		default:
			statusTone = "is-open"
		}
		pending := card.PaymentState.Pending
		if pending < 0 {
			pending = 0
		}
		item := PreviewCreditCard{CreditCardCard: card}
		item.WalletIndex = strconv.Itoa(index)
		item.StatusToneClass = statusTone
		item.Pending = MoneyMinor(pending)
		item.HasPending = card.InvoiceID != "" && pending > 0 && pending < card.PaymentState.Total
		if selected != nil {
			item.Historical = true
			item.ReferenceLabel = dashboardCashSummaryMonthLabel(*selected)
			if card.InvoiceID != "" && card.DueDateUnix > 0 {
				item.DueLabel = time.Unix(card.DueDateUnix, 0).UTC().Format("02/01/2006")
			}
			item.Href = faturasIndexURL(card.ID, int(selected.Month()), selected.Year(), "desc")
		}
		out = append(out, item)
	}
	return out
}

func previewCashFlowRange(raw string) (string, int, string) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case previewCashFlowWideRange:
		return previewCashFlowWideRange, 12, "Últimos 12 meses"
	default:
		return previewCashFlowDefaultRange, 6, "Últimos 6 meses"
	}
}

func previewCashFlowRangeOptions(basePath, active, selectedMonthQuery string) []PreviewCashFlowRange {
	return []PreviewCashFlowRange{
		{Value: "6m", Label: "6M", Href: dashboardMonthHref(basePath, selectedMonthQuery, "6m"), Active: active == "6m"},
		{Value: "12m", Label: "12M", Href: dashboardMonthHref(basePath, selectedMonthQuery, "12m"), Active: active == "12m"},
	}
}

// dashboardMonthHref builds the dashboard month/range URL.
func dashboardMonthHref(basePath, month, cashFlowRange string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		basePath = dashboardCanonicalBasePath
	}
	params := url.Values{}
	if strings.TrimSpace(month) != "" {
		params.Set("mes", month)
	}
	if cashFlowRange != "" && cashFlowRange != previewCashFlowDefaultRange {
		params.Set("fluxo", cashFlowRange)
	}
	if len(params) == 0 {
		return basePath
	}
	return basePath + "?" + params.Encode()
}

func previewResolveMonth(now time.Time, raw string) (time.Time, bool) {
	current := appTime(now)
	currentMonthStart := time.Date(current.Year(), current.Month(), 1, 12, 0, 0, 0, time.UTC)
	currentReference := time.Date(current.Year(), current.Month(), current.Day(), 12, 0, 0, 0, time.UTC)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return currentReference, true
	}
	parsed, err := time.Parse("2006-01", raw)
	if err != nil {
		return currentReference, true
	}
	selectedMonthStart := time.Date(parsed.Year(), parsed.Month(), 1, 12, 0, 0, 0, time.UTC)
	if selectedMonthStart.After(currentMonthStart) {
		return currentReference, true
	}
	if selectedMonthStart.Equal(currentMonthStart) {
		return currentReference, true
	}
	// A past month is compared as a closed month. BuildDashboardCategories
	// uses the reference day as its cut-off, so the last day keeps the whole
	// historical month instead of truncating it at "today".
	lastDay := time.Date(parsed.Year(), parsed.Month()+1, 0, 12, 0, 0, 0, time.UTC)
	return lastDay, false
}

func previewSelectedMonthQuery(selected time.Time, isCurrent bool) string {
	if isCurrent {
		return ""
	}
	return selected.Format("2006-01")
}

func previewMonthFilter(basePath string, selected time.Time, isCurrent bool, now time.Time, cashFlowRange string) PreviewMonthFilter {
	current := appTime(now)
	currentMonthStart := time.Date(current.Year(), current.Month(), 1, 12, 0, 0, 0, time.UTC)
	selectedMonthStart := time.Date(selected.Year(), selected.Month(), 1, 12, 0, 0, 0, time.UTC)
	previous := selectedMonthStart.AddDate(0, -1, 0)
	next := selectedMonthStart.AddDate(0, 1, 0)
	filter := PreviewMonthFilter{
		Label:         dashboardCashSummaryMonthLabel(selected),
		PreviousLabel: dashboardCashSummaryMonthLabel(previous),
		PreviousHref:  dashboardMonthHref(basePath, previous.Format("2006-01"), cashFlowRange),
		CurrentHref:   dashboardMonthHref(basePath, "", cashFlowRange),
		IsCurrent:     isCurrent,
	}
	if !next.After(currentMonthStart) {
		filter.CanGoNext = true
		filter.NextLabel = dashboardCashSummaryMonthLabel(next)
		nextValue := next.Format("2006-01")
		if next.Equal(currentMonthStart) {
			nextValue = ""
		}
		filter.NextHref = dashboardMonthHref(basePath, nextValue, cashFlowRange)
	}
	return filter
}

func previewCategoryColorClass(tone int) string {
	switch tone {
	case 1:
		return "cbp-chart-0"
	case 2:
		return "cbp-chart-1"
	case 3:
		return "cbp-chart-2"
	case 4:
		return "cbp-chart-3"
	case 5:
		return "cbp-chart-4"
	default:
		return "cbp-chart-other"
	}
}

func previewRatioPercent(numerator, denominator int64) int {
	if denominator <= 0 {
		return 0
	}
	return int(math.Round(float64(numerator) * 100 / float64(denominator)))
}

func previewClampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// previewChartAxisLabel is presentation-only: it shortens an existing scale
// value for the chart axis without creating a new financial metric.
func previewChartAxisLabel(cents int64) string {
	if cents <= 0 {
		return "0"
	}
	reais := float64(cents) / 100
	if reais >= 1000 {
		return fmt.Sprintf("%.0f mil", reais/1000)
	}
	return fmt.Sprintf("%.0f", reais)
}

func previewBarHeight(value, maxValue int64, plotHeight int) int {
	if value <= 0 || maxValue <= 0 || plotHeight <= 0 {
		return 0
	}
	height := int(math.Round(float64(value) * float64(plotHeight) / float64(maxValue)))
	if height < 1 {
		return 1
	}
	if height > plotHeight {
		return plotHeight
	}
	return height
}

func previewMonthShort(month time.Time) string {
	months := []string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}
	return months[int(month.Month())-1]
}

func previewMonthFull(month time.Time) string {
	months := []string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}
	return fmt.Sprintf("%s/%d", months[int(month.Month())-1], month.Year())
}
