package handlers

import (
	"database/sql"
	"fmt"
	"hash/fnv"
	"math/big"
	"strings"
	"time"
)

// DashboardCategoryItem is one row of "Principais categorias". It is a
// presentation projection over the canonical competence expense breakdown:
// the same net amount, parent grouping, and invoice-payment exclusion used by
// Relatórios > Competência and by the Dashboard cost limits.
type DashboardCategoryItem struct {
	ID                   string
	Name                 string
	Icon                 string
	Tone                 int
	Amount               MoneyDisplay
	AmountCents          int64
	SharePercent         int
	BarPercent           int
	HasShare             bool
	ShareLabel           string
	VariationLabel       string
	VariationClass       string
	VariationDescription string
	HasBase              bool
}

type DashboardCategoriesData struct {
	Items          []DashboardCategoryItem
	Total          MoneyDisplay
	CurrentLabel   string
	PreviousLabel  string
	CurrentStart   string
	CurrentEnd     string
	PreviousStart  string
	PreviousEnd    string
	HasData        bool
	ComparisonNote string
}

const dashboardCategoriesLimit = 5

// BuildDashboardCategories compares the elapsed part of the current month with
// the same elapsed interval of the previous month. It uses the competence
// criterion (transaction date), net of refunds and excluding invoice payments,
// so a card purchase is counted once and the invoice payment never appears as a
// second expense. It never invents a historical snapshot or a cash-flow rule.
func BuildDashboardCategories(db *sql.DB, workspaceID string, now time.Time) DashboardCategoriesData {
	data := DashboardCategoriesData{}
	if db == nil || strings.TrimSpace(workspaceID) == "" {
		return data
	}

	today := AppCivilDateUTC(now)
	currentStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	currentEnd := today
	previousStart := currentStart.AddDate(0, -1, 0)
	previousEnd := sameDayPreviousMonth(previousStart, today.Day())

	data.CurrentStart = currentStart.Format("2006-01-02")
	data.CurrentEnd = currentEnd.Format("2006-01-02")
	data.PreviousStart = previousStart.Format("2006-01-02")
	data.PreviousEnd = previousEnd.Format("2006-01-02")
	currentLabel := dashboardCategoryPeriodLabel(currentStart, currentEnd, false)
	previousLabel := dashboardCategoryPeriodLabel(previousStart, previousEnd, previousStart.Year() != currentStart.Year())
	data.CurrentLabel = currentLabel
	data.PreviousLabel = previousLabel
	data.ComparisonNote = fmt.Sprintf("%s comparado com %s", currentLabel, previousLabel)

	currentBars, currentTotal, err := queryDashboardCategoryPeriod(db, workspaceID, currentStart.Unix(), endOfCivilDayUnix(currentEnd))
	if err != nil {
		return data
	}
	previousBars, _, err := queryDashboardCategoryPeriod(db, workspaceID, previousStart.Unix(), endOfCivilDayUnix(previousEnd))
	if err != nil {
		return data
	}

	currentTop := dashboardTopExpenseCategories(currentBars)
	previousByID := make(map[string]int64)
	for _, bar := range dashboardTopExpenseCategories(previousBars) {
		previousByID[bar.ID] = bar.Valor.rawCents()
	}

	data.Total = MoneyMinor(currentTotal)
	for _, bar := range currentTop {
		if len(data.Items) >= dashboardCategoriesLimit {
			break
		}
		amount := bar.Valor.rawCents()
		if amount <= 0 {
			continue
		}
		item := DashboardCategoryItem{
			ID:          bar.ID,
			Name:        strings.TrimSpace(bar.Nome),
			Icon:        strings.TrimSpace(bar.Icon),
			Tone:        dashboardCategoryTone(bar.ID),
			Amount:      bar.Valor,
			AmountCents: amount,
		}
		if currentTotal > 0 {
			item.HasShare = true
			item.SharePercent = reportPercent(amount, currentTotal)
			item.BarPercent = dashboardRoundedPercent(amount, currentTotal)
			if item.BarPercent < 1 {
				item.BarPercent = 1
			}
			if item.BarPercent > 100 {
				item.BarPercent = 100
			}
			item.ShareLabel = percentLabel(amount, currentTotal)
		}
		if item.Name == "" {
			item.Name = "Sem categoria"
		}
		if item.Icon == "" {
			item.Icon = "tag"
		}
		item.applyVariation(previousByID[bar.ID], previousLabel)
		data.Items = append(data.Items, item)
	}
	data.HasData = len(data.Items) > 0
	return data
}

func (item *DashboardCategoryItem) applyVariation(previous int64, previousLabel string) {
	if previous <= 0 {
		item.HasBase = false
		item.VariationLabel = "Novo gasto"
		item.VariationClass = "is-new"
		item.VariationDescription = fmt.Sprintf("Novo gasto: sem despesa comparável em %s.", previousLabel)
		return
	}
	item.HasBase = true
	delta := new(big.Int).Sub(big.NewInt(item.AmountCents), big.NewInt(previous))
	absDelta := new(big.Int).Abs(new(big.Int).Set(delta))
	numerator := new(big.Int).Mul(absDelta, big.NewInt(1000))
	numerator.Add(numerator, big.NewInt(previous/2))
	tenths := new(big.Int).Quo(numerator, big.NewInt(previous)).Int64()
	absLabel := formatCategoryPercentTenths(tenths)
	item.HasBase = true

	switch delta.Sign() {
	case 1:
		item.VariationLabel = "+" + absLabel
		item.VariationClass = "is-up"
		if tenths >= 200 {
			item.VariationClass += " is-alert"
		}
		item.VariationDescription = fmt.Sprintf("Aumento de %s em relação a %s.", absLabel, previousLabel)
	case -1:
		item.VariationLabel = "−" + absLabel
		item.VariationClass = "is-down"
		item.VariationDescription = fmt.Sprintf("Redução de %s em relação a %s.", absLabel, previousLabel)
	default:
		item.VariationLabel = "0,0%"
		item.VariationClass = "is-flat"
		item.VariationDescription = fmt.Sprintf("Sem variação em relação a %s.", previousLabel)
	}
}

func dashboardRoundedPercent(amount, total int64) int {
	if total <= 0 || amount <= 0 {
		return 0
	}
	var numerator big.Int
	numerator.Mul(big.NewInt(amount), big.NewInt(100))
	numerator.Add(&numerator, big.NewInt(total/2))
	numerator.Div(&numerator, big.NewInt(total))
	if !numerator.IsInt64() {
		return 100
	}
	value := numerator.Int64()
	if value > 100 {
		return 100
	}
	return int(value)
}

func formatCategoryPercentTenths(tenths int64) string {
	if tenths < 0 {
		tenths = -tenths
	}
	return fmt.Sprintf("%d,%d%%", tenths/10, tenths%10)
}

func sameDayPreviousMonth(previousStart time.Time, day int) time.Time {
	lastDay := previousStart.AddDate(0, 1, 0).AddDate(0, 0, -1).Day()
	if day > lastDay {
		day = lastDay
	}
	if day < 1 {
		day = 1
	}
	return time.Date(previousStart.Year(), previousStart.Month(), day, 0, 0, 0, 0, time.UTC)
}

func endOfCivilDayUnix(day time.Time) int64 {
	return day.AddDate(0, 0, 1).Add(-time.Second).Unix()
}

func dashboardCategoryPeriodLabel(start, end time.Time, includeYear bool) string {
	layout := "02/01"
	if includeYear {
		layout = "02/01/2006"
	}
	return fmt.Sprintf("%s a %s", start.Format(layout), end.Format(layout))
}

func dashboardTopExpenseCategories(bars []CategoriaBar) []CategoriaBar {
	out := make([]CategoriaBar, 0, len(bars))
	for _, bar := range bars {
		if bar.IsChild || bar.IsDirectParentEntry {
			continue
		}
		if bar.Valor.rawCents() <= 0 {
			continue
		}
		out = append(out, bar)
	}
	return out
}

func dashboardCategoryTone(id string) int {
	if strings.TrimSpace(id) == "" {
		return 0
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id))
	return int(hash.Sum32()%5) + 1
}

// queryDashboardCategoryPeriod reuses the canonical competence query from
// Relatórios. Amounts are net of transaction refunds and invoice payments are
// excluded through the same invoice_payments lineage used by the reports.
func queryDashboardCategoryPeriod(db *sql.DB, workspaceID string, start, end int64) ([]CategoriaBar, int64, error) {
	rows, err := db.Query(`
		SELECT
			COALESCE(c.id, ''),
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(c.parent_id, ''),
			COALESCE(p.name, ''),
			COALESCE(p.icon, ''),
			COALESCE(p.color, ''),
			`+transactionNetAmountTSQL+` AS total
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
		  AND t.date >= ? AND t.date <= ?
		  AND `+excludeInvoicePaymentCompetenceClause("t")+`
	`, workspaceID, start, end)
	if err != nil {
		return nil, 0, fmt.Errorf("query dashboard categories: %w", err)
	}
	defer rows.Close()

	type categoryAggregate struct {
		row    categoriaBreakdownRow
		amount reportExactAmount
	}
	byID := make(map[string]*categoryAggregate)
	var total reportExactAmount
	for rows.Next() {
		var id, name, icon, color, parentID, parentName, parentIcon, parentColor string
		var amount int64
		if err := rows.Scan(&id, &name, &icon, &color, &parentID, &parentName, &parentIcon, &parentColor, &amount); err != nil {
			return nil, 0, fmt.Errorf("scan dashboard categories: %w", err)
		}
		aggregate := byID[id]
		if aggregate == nil {
			aggregate = &categoryAggregate{row: categoriaBreakdownRow{
				ID:          id,
				Name:        name,
				Icon:        icon,
				Color:       color,
				ParentID:    parentID,
				ParentName:  parentName,
				ParentIcon:  parentIcon,
				ParentColor: parentColor,
			}}
			byID[id] = aggregate
		}
		aggregate.amount.add(amount)
		total.add(amount)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate dashboard categories: %w", err)
	}

	raw := make([]categoriaBreakdownRow, 0, len(byID))
	for _, aggregate := range byID {
		cents, err := aggregate.amount.int64("dashboard category")
		if err != nil {
			return nil, 0, err
		}
		row := aggregate.row
		row.Total = cents
		raw = append(raw, row)
	}
	totalCents, err := total.int64("dashboard categories total")
	if err != nil {
		return nil, 0, err
	}
	bars, err := buildCategoriaBreakdown(raw, totalCents)
	if err != nil {
		return nil, 0, err
	}
	return bars, totalCents, nil
}
