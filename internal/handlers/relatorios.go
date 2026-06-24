package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RelatoriosHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

type RelatoriosData struct {
	OOB                       bool
	Title                     string
	UserInitials              string
	ProfilePhotoURL           string
	IsBusiness                bool
	ActiveWorkspaceName       string
	MesAtual                  int
	AnoAtual                  int
	MonthLabel                string
	MonthOptions              []MonthOption
	MonthSelectorHXGet        string
	MonthSelectorHXSelect     string
	MonthSelectorHXTarget     string
	MonthSelectorHXSwap       string
	MonthSelectorPartial      string
	MonthSelectorPrevQuery    string
	MonthSelectorNextQuery    string
	MonthSelectorCurrentQuery string
	MesAnteriorURL            string
	MesSeguinteURL            string
	CurrentMonthURL           string
	TotalReceitas             MoneyDisplay
	TotalDespesas             MoneyDisplay
	SaldoLiquido              MoneyDisplay
	SaldoAcumulado            MoneyDisplay
	SaldoAcumuladoNegativo    bool
	SaldoClass                string
	SaldoNegativo             bool
	Categorias                []CategoriaBar
	Membros                   []MembroBar
	DonutEssentialPercent     int
	DonutLifestylePercent     int
	DonutEssentialTotal       MoneyDisplay
	DonutLifestyleTotal       MoneyDisplay
	CashflowLinePath          string
	CashflowProjectionPath    string
	CashflowProjectionClass   string
	CashflowGridLines         []float64
	CashflowLabels            []CashflowLabel
	HasUncategorizedAlert     bool
	UncategorizedPercent      int
	UncategorizedAmount       MoneyDisplay
	PendingPayableTotal       MoneyDisplay
	PendingReceivableTotal    MoneyDisplay
	OverdueReceivableTotal    MoneyDisplay
	DREMacroGroups            []string
	DRE                       []DRERow
	RevenueByCategory         []RevenueCategorySlice
	TopClientsByRevenue       []TopClientRevenue
}

type CashflowLabel struct {
	X    float64
	Text string
}

type CategoriaBar struct {
	ID                  string
	Nome                string
	Icon                string
	Color               string
	Valor               MoneyDisplay
	Percent             int
	IsChild             bool
	ParentID            string
	ParentName          string
	IsGroup             bool
	IsDirectParentEntry bool
}

type MembroBar struct {
	Nome    string
	Valor   MoneyDisplay
	Percent int
	Color   string
}

type RevenueCategorySlice struct {
	Name       string
	Amount     MoneyDisplay
	Percent    int
	Stroke     string
	DashOffset int
}

type TopClientRevenue struct {
	Name      string
	Amount    MoneyDisplay
	Percent   int
	BarWidth  int
	Highlight string
}

type DRERow struct {
	Competencia string
	MacroGroups []DREMacroGroupAmount
	Resultado   MoneyDisplay
}

type DREMacroGroupAmount struct {
	MacroGroup string
	Amount     MoneyDisplay
	RawAmount  int64
}

func (h *RelatoriosHandler) HandleExibirRelatorios(w http.ResponseWriter, r *http.Request) {
	mesStr := r.URL.Query().Get("mes")
	anoStr := r.URL.Query().Get("ano")

	now := time.Now()
	mes := int(now.Month())
	ano := now.Year()

	if mesStr != "" {
		if v, err := strconv.Atoi(mesStr); err == nil && v >= 1 && v <= 12 {
			mes = v
		}
	}
	if anoStr != "" {
		if v, err := strconv.Atoi(anoStr); err == nil && v >= 2020 {
			ano = v
		}
	}

	data, err := h.buildRelatoriosData("", mes, ano)
	if err != nil {
		log.Printf("build relatorios error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		log.Printf("template relatorios error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *RelatoriosHandler) HandleRelatoriosConceito(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	mesStr := r.URL.Query().Get("mes")
	anoStr := r.URL.Query().Get("ano")

	now := time.Now()
	mes := int(now.Month())
	ano := now.Year()

	if mesStr != "" {
		if v, err := strconv.Atoi(mesStr); err == nil && v >= 1 && v <= 12 {
			mes = v
		}
	}
	if anoStr != "" {
		if v, err := strconv.Atoi(anoStr); err == nil && v >= 2020 {
			ano = v
		}
	}

	tData := time.Now()
	data, err := h.buildRelatoriosData(reqID, mes, ano)
	perfStep(reqID, "Relatorios", "buildRelatoriosData", time.Since(tData))
	if err != nil {
		log.Printf("build relatorios error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	templateName := "relatorios-page"
	isPartial := false
	if r.Header.Get("HX-Request") != "" {
		if r.URL.Query().Get("partial") == "content" {
			templateName = "relatorios-body"
			isPartial = true
		}
	}

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template relatorios error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "Relatorios", "templateRender", time.Since(tR))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "Relatorios", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isPartial {
		cleanQ := r.URL.Query()
		cleanQ.Del("partial")
		w.Header().Set("HX-Replace-Url", "/relatorios?"+cleanQ.Encode())
	}
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

func (h *RelatoriosHandler) HandleExportarCSV(w http.ResponseWriter, r *http.Request) {
	tipo := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("tipo")))
	if tipo == "" {
		tipo = "dre"
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, tipo))
	writer := csv.NewWriter(w)
	defer writer.Flush()

	switch tipo {
	case "dre":
		ano := time.Now().Year()
		if raw := strings.TrimSpace(r.URL.Query().Get("ano")); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 2020 && v <= time.Now().Year()+10 {
				ano = v
			}
		}
		rows, err := h.queryDRERaw(ano)
		if err != nil {
			http.Error(w, "erro ao gerar csv", http.StatusInternalServerError)
			return
		}
		_ = writer.Write([]string{"competencia", "macro_group", "total_centavos"})
		for _, row := range rows {
			_ = writer.Write([]string{
				row.Competencia,
				sanitizeCSVField(row.MacroGroup),
				strconv.FormatInt(row.Amount, 10),
			})
		}
	case "pagar", "receber", "inadimplencia":
		queryType := "EXPENSE"
		onlyOverdue := false
		if tipo == "receber" || tipo == "inadimplencia" {
			queryType = "INCOME"
		}
		if tipo == "inadimplencia" {
			onlyOverdue = true
		}
		items, err := queryPendingItems(h.DB, h.WorkspaceID, queryType, onlyOverdue)
		if err != nil {
			http.Error(w, "erro ao gerar csv", http.StatusInternalServerError)
			return
		}
		_ = writer.Write([]string{"id", "descricao", "valor_centavos", "vencimento_unix", "conta", "contato"})
		for _, item := range items {
			_ = writer.Write([]string{
				item.ID,
				sanitizeCSVField(item.Description),
				strconv.FormatInt(item.Amount, 10),
				strconv.FormatInt(item.DueDateUnix, 10),
				sanitizeCSVField(item.AccountName),
				sanitizeCSVField(item.ContactName),
			})
		}
	default:
		http.Error(w, "tipo de exportação inválido", http.StatusBadRequest)
	}
}

func sanitizeCSVField(val string) string {
	trimmed := strings.TrimLeft(val, " \t\r\n")
	if len(trimmed) == 0 {
		return val
	}
	first := trimmed[0]
	if first == '=' || first == '+' || first == '-' || first == '@' || first == '\t' || first == '\r' || first == '\n' {
		return "'" + val
	}
	return val
}

type dreRawRow struct {
	Competencia string
	MacroGroup  string
	Amount      int64
}

type categoriaBreakdownRow struct {
	ID          string
	Name        string
	Icon        string
	Color       string
	ParentID    string
	ParentName  string
	ParentIcon  string
	ParentColor string
	Total       int64
}

type categoriaBreakdownGroup struct {
	parent      categoriaBreakdownRow
	directTotal int64
	children    []categoriaBreakdownRow
}

type categoriaBreakdownEntry struct {
	name       string
	total      int64
	group      *categoriaBreakdownGroup
	standalone categoriaBreakdownRow
}

func buildCategoriaBreakdown(rows []categoriaBreakdownRow, totalDespesas int64) []CategoriaBar {
	groups := make(map[string]*categoriaBreakdownGroup)
	standalone := make([]categoriaBreakdownRow, 0)

	for _, row := range rows {
		if row.ID == "" {
			standalone = append(standalone, row)
			continue
		}

		if row.ParentID == "" {
			group := ensureCategoriaBreakdownGroup(groups, categoriaBreakdownRow{
				ID:    row.ID,
				Name:  row.Name,
				Icon:  row.Icon,
				Color: row.Color,
			})
			group.directTotal += row.Total
			continue
		}

		parent := categoriaBreakdownRow{
			ID:    row.ParentID,
			Name:  row.ParentName,
			Icon:  row.ParentIcon,
			Color: row.ParentColor,
		}
		if parent.Name == "" {
			parent.Name = row.ParentName
		}
		if parent.Icon == "" {
			parent.Icon = row.Icon
		}
		if parent.Color == "" {
			parent.Color = row.Color
		}

		group := ensureCategoriaBreakdownGroup(groups, parent)
		group.children = append(group.children, row)
	}

	entries := make([]categoriaBreakdownEntry, 0, len(groups)+len(standalone))
	for _, group := range groups {
		total := group.directTotal
		for _, child := range group.children {
			total += child.Total
		}
		if total == 0 {
			continue
		}
		if len(group.children) == 0 {
			entries = append(entries, categoriaBreakdownEntry{
				name:  group.parent.Name,
				total: total,
				standalone: categoriaBreakdownRow{
					ID:    group.parent.ID,
					Name:  group.parent.Name,
					Icon:  group.parent.Icon,
					Color: group.parent.Color,
					Total: total,
				},
			})
			continue
		}
		entries = append(entries, categoriaBreakdownEntry{
			name:  group.parent.Name,
			total: total,
			group: group,
		})
	}
	for _, row := range standalone {
		if row.Total == 0 {
			continue
		}
		entries = append(entries, categoriaBreakdownEntry{
			name:       row.Name,
			total:      row.Total,
			standalone: row,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].total != entries[j].total {
			return entries[i].total > entries[j].total
		}
		return entries[i].name < entries[j].name
	})

	out := make([]CategoriaBar, 0, len(rows)+len(groups))
	for _, entry := range entries {
		if entry.group == nil {
			out = append(out, categoriaBarFromRow(entry.standalone, entry.total, totalDespesas, false, "", "", false, false))
			continue
		}

		group := entry.group
		out = append(out, categoriaBarFromRow(group.parent, entry.total, totalDespesas, false, "", "", true, false))

		if group.directTotal > 0 {
			out = append(out, categoriaBarFromRow(group.parent, group.directTotal, totalDespesas, true, group.parent.ID, group.parent.Name, false, true))
		}

		sort.SliceStable(group.children, func(i, j int) bool {
			if group.children[i].Total != group.children[j].Total {
				return group.children[i].Total > group.children[j].Total
			}
			return group.children[i].Name < group.children[j].Name
		})
		for _, child := range group.children {
			out = append(out, categoriaBarFromRow(child, child.Total, totalDespesas, true, group.parent.ID, group.parent.Name, false, false))
		}
	}

	return out
}

func ensureCategoriaBreakdownGroup(groups map[string]*categoriaBreakdownGroup, parent categoriaBreakdownRow) *categoriaBreakdownGroup {
	group, ok := groups[parent.ID]
	if !ok {
		group = &categoriaBreakdownGroup{parent: parent}
		groups[parent.ID] = group
		return group
	}
	if group.parent.Name == "" {
		group.parent.Name = parent.Name
	}
	if group.parent.Icon == "" {
		group.parent.Icon = parent.Icon
	}
	if group.parent.Color == "" {
		group.parent.Color = parent.Color
	}
	return group
}

func categoriaBarFromRow(row categoriaBreakdownRow, amount int64, totalDespesas int64, isChild bool, parentID string, parentName string, isGroup bool, isDirectParentEntry bool) CategoriaBar {
	return CategoriaBar{
		ID:                  row.ID,
		Nome:                row.Name,
		Icon:                row.Icon,
		Color:               row.Color,
		Valor:               MoneyMinor(amount),
		Percent:             categoriaPercent(amount, totalDespesas),
		IsChild:             isChild,
		ParentID:            parentID,
		ParentName:          parentName,
		IsGroup:             isGroup,
		IsDirectParentEntry: isDirectParentEntry,
	}
}

func categoriaPercent(amount int64, totalDespesas int64) int {
	if totalDespesas <= 0 {
		return 0
	}
	return int((amount * 100) / totalDespesas)
}

func (h *RelatoriosHandler) buildRelatoriosData(reqID string, mes, ano int) (RelatoriosData, error) {
	tS := time.Now()
	months := []string{
		"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho",
		"Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro",
	}

	data := RelatoriosData{
		Title:                     "Relatórios",
		UserInitials:              queryUserInitialsByID(h.DB, h.UserID),
		ProfilePhotoURL:           queryUserProfilePhotoURL(h.DB, h.UserID),
		IsBusiness:                workspaceType(h.DB, h.WorkspaceID) == "business",
		ActiveWorkspaceName:       queryWorkspaceName(h.DB, h.WorkspaceID),
		MesAtual:                  mes,
		AnoAtual:                  ano,
		MonthLabel:                fmt.Sprintf("%s %d", months[mes-1], ano),
		MonthSelectorHXGet:        "/relatorios",
		MonthSelectorHXTarget:     "#relatorios-body",
		MonthSelectorHXSelect:     "#relatorios-body",
		MonthSelectorHXSwap:       "outerHTML",
		MonthSelectorPartial:      "content",
		MonthSelectorPrevQuery:    relatoriosMonthQuery(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Year()),
		MonthSelectorNextQuery:    relatoriosMonthQuery(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Year()),
		MonthSelectorCurrentQuery: relatoriosMonthQuery(int(time.Now().UTC().Month()), time.Now().UTC().Year()),
		MesAnteriorURL:            relatoriosURL(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Year()),
		MesSeguinteURL:            relatoriosURL(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Year()),
		CurrentMonthURL:           relatoriosURL(int(time.Now().UTC().Month()), time.Now().UTC().Year()),
		MonthOptions:              buildRelatoriosMonthOptions(ano, mes),
	}

	loc := time.UTC
	monthStart := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, loc).Unix()
	nextMonth := time.Date(ano, time.Month(mes)+1, 1, 0, 0, 0, 0, loc)
	monthEnd := nextMonth.Add(-1 * time.Second).Unix()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	perfStep(reqID, "Relatorios", "init+metadata", time.Since(tS))
	tS = time.Now()

	var totalReceitas, totalDespesas int64
	if err := h.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
			FROM transactions
			WHERE workspace_id = ? AND date >= ? AND date <= ?
			  AND `+excludeInvoicePaymentCompetenceClause("")+`
		`, h.WorkspaceID, monthStart, monthEnd).Scan(&totalReceitas, &totalDespesas); err != nil {
		return data, fmt.Errorf("query totals: %w", err)
	}

	saldo := totalReceitas - totalDespesas
	data.TotalReceitas = MoneyMinor(totalReceitas)
	data.TotalDespesas = MoneyMinor(totalDespesas)
	data.SaldoLiquido = MoneyMinor(saldo)
	data.SaldoClass = "text-[#6744F1]"
	data.SaldoNegativo = saldo < 0
	if saldo < 0 {
		data.SaldoClass = "text-[#FE414F]"
	}
	perfStep(reqID, "Relatorios", "queryTotals", time.Since(tS))
	tS = time.Now()

	var totalBalance int64
	_ = h.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(current_balance), 0) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, h.WorkspaceID).Scan(&totalBalance)
	selectedMonth := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)
	saldoAcumulado := calcProjectedAccumulatedBalance(h.DB, h.WorkspaceID, totalBalance, selectedMonth)
	data.SaldoAcumulado = MoneyMinor(saldoAcumulado)
	data.SaldoAcumuladoNegativo = saldoAcumulado < 0
	perfStep(reqID, "Relatorios", "saldoAcumulado", time.Since(tS))
	tS = time.Now()

	catRows, err := h.DB.QueryContext(ctx, `
			SELECT COALESCE(c.id, ''),
			       COALESCE(c.name, 'Sem categoria'),
			       COALESCE(c.icon, 'tag'),
			       COALESCE(c.color, '#6b7280'),
			       COALESCE(c.parent_id, ''),
			       COALESCE(p.name, ''),
			       COALESCE(p.icon, ''),
			       COALESCE(p.color, ''),
			       SUM(t.amount) AS total
			FROM transactions t
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
			  AND t.date >= ? AND t.date <= ?
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
			GROUP BY c.id
			ORDER BY total DESC
		`, h.WorkspaceID, monthStart, monthEnd)
	if err != nil {
		return data, fmt.Errorf("query categories: %w", err)
	}
	defer catRows.Close()

	var rawBars []categoriaBreakdownRow
	for catRows.Next() {
		var total int64
		var catID, name, icon, color, parentID, parentName, parentIcon, parentColor string
		if err := catRows.Scan(&catID, &name, &icon, &color, &parentID, &parentName, &parentIcon, &parentColor, &total); err != nil {
			continue
		}
		rawBars = append(rawBars, categoriaBreakdownRow{
			ID:          catID,
			Name:        name,
			Icon:        icon,
			Color:       color,
			ParentID:    parentID,
			ParentName:  parentName,
			ParentIcon:  parentIcon,
			ParentColor: parentColor,
			Total:       total,
		})
	}
	if err := catRows.Err(); err != nil {
		return data, fmt.Errorf("scan categories: %w", err)
	}
	data.Categorias = buildCategoriaBreakdown(rawBars, totalDespesas)
	perfStep(reqID, "Relatorios", "queryCategories", time.Since(tS))
	tS = time.Now()

	if data.IsBusiness {
		revenueSlices, err := h.queryRevenueByCategory(ctx, monthStart, monthEnd)
		if err != nil {
			return data, fmt.Errorf("query revenue by category: %w", err)
		}
		data.RevenueByCategory = revenueSlices
	} else {
		essentialTotal, lifestyleTotal, err := h.queryMacroGroupTotals(ctx, monthStart, monthEnd)
		if err != nil {
			return data, fmt.Errorf("query macro groups: %w", err)
		}
		data.DonutEssentialTotal = MoneyMinor(essentialTotal)
		data.DonutLifestyleTotal = MoneyMinor(lifestyleTotal)
		if totalDespesas > 0 {
			data.DonutEssentialPercent = int((essentialTotal * 100) / totalDespesas)
			data.DonutLifestylePercent = int((lifestyleTotal * 100) / totalDespesas)
		}
	}
	perfStep(reqID, "Relatorios", "businessOrMacro", time.Since(tS))
	tS = time.Now()

	line, projection, projectionClass, err := h.buildCashflowLineSVGPaths(ctx, ano, mes)
	if err != nil {
		return data, fmt.Errorf("build cashflow line: %w", err)
	}
	data.CashflowLinePath = line
	data.CashflowProjectionPath = projection
	data.CashflowProjectionClass = projectionClass
	data.CashflowGridLines = []float64{18, 40, 62}
	data.CashflowLabels = cashflowAxisLabels(ano, mes, 220)
	perfStep(reqID, "Relatorios", "cashflowLine", time.Since(tS))
	tS = time.Now()

	dreRows, err := h.queryDRERows(ano)
	if err != nil {
		return data, fmt.Errorf("query dre: %w", err)
	}
	data.DRE = dreRows
	data.DREMacroGroups = extractDREMacroGroups(dreRows)
	perfStep(reqID, "Relatorios", "queryDRE", time.Since(tS))

	return data, nil
}

func (h *RelatoriosHandler) queryDRERows(ano int) ([]DRERow, error) {
	rawRows, err := h.queryDRERaw(ano)
	if err != nil {
		return nil, err
	}
	groupOrder := make([]string, 0)
	groupSeen := make(map[string]struct{})
	rowsByCompetencia := make(map[string]map[string]int64)
	competencias := make([]string, 0)
	competenciaSeen := make(map[string]struct{})
	for _, raw := range rawRows {
		raw.MacroGroup = canonicalMacroGroup(raw.MacroGroup)
		if _, ok := competenciaSeen[raw.Competencia]; !ok {
			competenciaSeen[raw.Competencia] = struct{}{}
			competencias = append(competencias, raw.Competencia)
		}
		if _, ok := groupSeen[raw.MacroGroup]; !ok {
			groupSeen[raw.MacroGroup] = struct{}{}
			groupOrder = append(groupOrder, raw.MacroGroup)
		}
		if _, ok := rowsByCompetencia[raw.Competencia]; !ok {
			rowsByCompetencia[raw.Competencia] = make(map[string]int64)
		}
		rowsByCompetencia[raw.Competencia][raw.MacroGroup] += raw.Amount
	}
	out := make([]DRERow, 0, len(competencias))
	for _, competencia := range competencias {
		groupAmounts := make([]DREMacroGroupAmount, 0, len(groupOrder))
		var total int64
		for _, macroGroup := range groupOrder {
			value := rowsByCompetencia[competencia][macroGroup]
			groupAmounts = append(groupAmounts, DREMacroGroupAmount{
				MacroGroup: macroGroup,
				Amount:     MoneyMinor(value),
				RawAmount:  value,
			})
			total += value
		}
		out = append(out, DRERow{
			Competencia: competencia,
			MacroGroups: groupAmounts,
			Resultado:   MoneyMinor(total),
		})
	}
	return out, nil
}

func extractDREMacroGroups(rows []DRERow) []string {
	if len(rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows[0].MacroGroups))
	for _, item := range rows[0].MacroGroups {
		out = append(out, item.MacroGroup)
	}
	return out
}

func dreMacroValue(groups []DREMacroGroupAmount, macroGroup string) MoneyDisplay {
	for _, item := range groups {
		if item.MacroGroup == macroGroup {
			return item.Amount
		}
	}
	return MoneyMinor(0)
}

func (h *RelatoriosHandler) queryDRERaw(ano int) ([]dreRawRow, error) {
	start := time.Date(ano, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(ano+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	rows, err := h.DB.Query(`
		SELECT
			strftime('%Y-%m', datetime(t.date, 'unixepoch')) AS competencia,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE -t.amount END), 0) AS total
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			  AND t.type IN ('INCOME', 'EXPENSE')
			  AND t.date >= ? AND t.date < ?
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
		GROUP BY
			strftime('%Y-%m', datetime(t.date, 'unixepoch')),
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro')
		ORDER BY
			strftime('%Y-%m', datetime(t.date, 'unixepoch')) ASC,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') ASC
		`, h.WorkspaceID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dreRawRow
	for rows.Next() {
		var competencia string
		var macroGroup string
		var amount int64
		if err := rows.Scan(&competencia, &macroGroup, &amount); err != nil {
			return nil, err
		}
		out = append(out, dreRawRow{
			Competencia: competencia,
			MacroGroup:  macroGroup,
			Amount:      amount,
		})
	}
	return out, rows.Err()
}

func (h *RelatoriosHandler) queryMacroGroupTotals(ctx context.Context, monthStart, monthEnd int64) (int64, int64, error) {
	rows, err := h.DB.QueryContext(ctx, `
		SELECT COALESCE(c.macro_group, p.macro_group, 'Estilo de Vida'), COALESCE(SUM(t.amount), 0)
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ? AND t.type = 'EXPENSE' AND t.date >= ? AND t.date <= ?
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
			GROUP BY COALESCE(c.macro_group, p.macro_group, 'Estilo de Vida')
		`, h.WorkspaceID, monthStart, monthEnd)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	var essential, lifestyle int64
	for rows.Next() {
		var group string
		var total int64
		if err := rows.Scan(&group, &total); err != nil {
			return 0, 0, err
		}
		if canonicalMacroGroup(group) == "Essencial" {
			essential += total
		} else {
			lifestyle += total
		}
	}
	return essential, lifestyle, rows.Err()
}

func canonicalMacroGroup(group string) string {
	switch strings.ToUpper(strings.TrimSpace(group)) {
	case "ESSENTIAL":
		return "Essencial"
	case "LIFESTYLE":
		return "Estilo de Vida"
	case "OPERATING_COSTS":
		return "Custos Operacionais"
	case "OPERATING_REVENUE", "OPERATIONAL_REVENUE":
		return "Receitas Operacionais"
	default:
		return strings.TrimSpace(group)
	}
}

func (h *RelatoriosHandler) queryRevenueByCategory(ctx context.Context, monthStart, monthEnd int64) ([]RevenueCategorySlice, error) {
	rows, err := h.DB.QueryContext(ctx, `
		SELECT
			COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), 'Sem categoria') AS category_name,
			COALESCE(SUM(t.amount), 0) AS total
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
		  AND t.type = 'INCOME'
		  AND t.status = 'paid'
		  AND t.date >= ? AND t.date <= ?
		GROUP BY category_name
		ORDER BY total DESC
	`, h.WorkspaceID, monthStart, monthEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type revenueRaw struct {
		Name   string
		Amount int64
	}
	raw := make([]revenueRaw, 0)
	var totalRevenue int64
	for rows.Next() {
		var item revenueRaw
		if err := rows.Scan(&item.Name, &item.Amount); err != nil {
			return nil, err
		}
		if item.Amount <= 0 {
			continue
		}
		totalRevenue += item.Amount
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(raw) == 0 || totalRevenue <= 0 {
		return nil, nil
	}

	const maxSlices = 6
	if len(raw) > maxSlices {
		var others int64
		for _, item := range raw[maxSlices-1:] {
			others += item.Amount
		}
		raw = append(raw[:maxSlices-1], revenueRaw{Name: "Outras categorias", Amount: others})
	}

	colors := []string{
		"rgb(16 185 129)",
		"rgb(99 102 241)",
		"rgb(236 72 153)",
		"rgb(245 158 11)",
		"rgb(14 165 233)",
		"rgb(168 85 247)",
	}
	slices := make([]RevenueCategorySlice, 0, len(raw))
	cumulative := 0
	for idx, item := range raw {
		percent := int((item.Amount * 100) / totalRevenue)
		if idx == len(raw)-1 {
			percent = 100 - cumulative
		}
		if percent < 0 {
			percent = 0
		}
		slices = append(slices, RevenueCategorySlice{
			Name:       item.Name,
			Amount:     MoneyMinor(item.Amount),
			Percent:    percent,
			Stroke:     colors[idx%len(colors)],
			DashOffset: 25 - cumulative,
		})
		cumulative += percent
	}
	return slices, nil
}

func (h *RelatoriosHandler) queryTopClientsByRevenue(ctx context.Context, monthStart, monthEnd int64) ([]TopClientRevenue, error) {
	var totalRevenue int64
	if err := h.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM transactions
		WHERE workspace_id = ?
		  AND type = 'INCOME'
		  AND status = 'paid'
		  AND date >= ? AND date <= ?
	`, h.WorkspaceID, monthStart, monthEnd).Scan(&totalRevenue); err != nil {
		return nil, err
	}
	if totalRevenue <= 0 {
		return nil, nil
	}

	rows, err := h.DB.QueryContext(ctx, `
		SELECT
			COALESCE(NULLIF(TRIM(c.name), ''), 'Sem contato') AS contact_name,
			COALESCE(SUM(t.amount), 0) AS total
		FROM transactions t
		LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.type = 'INCOME'
		  AND t.status = 'paid'
		  AND t.date >= ? AND t.date <= ?
		GROUP BY contact_name
		ORDER BY total DESC
		LIMIT 5
	`, h.WorkspaceID, monthStart, monthEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TopClientRevenue, 0, 5)
	highlights := []string{"emerald", "indigo", "violet", "amber", "sky"}
	var topAmount int64
	for rows.Next() {
		var name string
		var amount int64
		if err := rows.Scan(&name, &amount); err != nil {
			return nil, err
		}
		if amount <= 0 {
			continue
		}
		if topAmount == 0 {
			topAmount = amount
		}
		percent := int((amount * 100) / totalRevenue)
		barWidth := int((amount * 100) / topAmount)
		if barWidth < 8 {
			barWidth = 8
		}
		out = append(out, TopClientRevenue{
			Name:      name,
			Amount:    MoneyMinor(amount),
			Percent:   percent,
			BarWidth:  barWidth,
			Highlight: highlights[len(out)%len(highlights)],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *RelatoriosHandler) queryUncategorizedExpenses(ctx context.Context, monthStart, monthEnd int64) (int64, error) {
	var total int64
	err := h.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(t.amount), 0)
		FROM transactions t
		WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
			  AND t.date >= ? AND t.date <= ?
			  AND (t.category_id IS NULL OR t.category_id = '')
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
		`, h.WorkspaceID, monthStart, monthEnd).Scan(&total)
	return total, err
}

func (h *RelatoriosHandler) buildCashflowLineSVGPaths(ctx context.Context, year, month int) (string, string, string, error) {
	type monthPoint struct {
		start int64
		end   int64
	}
	points := make([]monthPoint, 0, 6)
	base := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	for i := -3; i <= 2; i++ {
		m := base.AddDate(0, i, 0)
		start := time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0).Add(-time.Second)
		points = append(points, monthPoint{start: start.Unix(), end: end.Unix()})
	}

	var values []int64
	var accumulated int64
	for i := 0; i < 4; i++ {
		monthNet, err := h.queryNetCashflow(ctx, points[i].start, points[i].end)
		if err != nil {
			return "", "", "", err
		}
		accumulated += monthNet
		values = append(values, accumulated)
	}

	avgFixedExpense, err := h.queryAverageFixedExpense(ctx)
	if err != nil {
		return "", "", "", err
	}
	projectionRed := false
	for i := 4; i < 6; i++ {
		accumulated -= avgFixedExpense
		if accumulated < 0 {
			projectionRed = true
		}
		values = append(values, accumulated)
	}

	linePath, projectionPath := buildSVGLinePaths(values, 220, 80)
	projectionClass := "text-zinc-400"
	if projectionRed {
		projectionClass = "text-rose-300"
	}
	return linePath, projectionPath, projectionClass, nil
}

func (h *RelatoriosHandler) queryNetCashflow(ctx context.Context, monthStart, monthEnd int64) (int64, error) {
	var income, expense int64
	if err := h.DB.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM transactions
		WHERE workspace_id = ? AND status = 'paid' AND date >= ? AND date <= ?
	`, h.WorkspaceID, monthStart, monthEnd).Scan(&income, &expense); err != nil {
		return 0, err
	}
	return income - expense, nil
}

func (h *RelatoriosHandler) queryAverageFixedExpense(ctx context.Context) (int64, error) {
	var avg sql.NullFloat64
	if err := h.DB.QueryRowContext(ctx, `
		SELECT AVG(amount)
		FROM recurring_rules
		WHERE workspace_id = ? AND active = 1 AND type = 'EXPENSE'
	`, h.WorkspaceID).Scan(&avg); err != nil {
		return 0, err
	}
	if !avg.Valid {
		return 0, nil
	}
	return int64(math.Round(avg.Float64)), nil
}

func buildSVGLinePaths(values []int64, width, height float64) (string, string) {
	if len(values) < 6 {
		return "", ""
	}
	minV, maxV := values[0], values[0]
	for _, v := range values {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	if minV == maxV {
		maxV++
	}
	scaleY := func(v int64) float64 {
		span := float64(maxV - minV)
		return 10 + (float64(maxV-v)/span)*(height-20)
	}
	scaleX := func(i int) float64 {
		return 10 + (float64(i)/5.0)*(width-20)
	}
	var hist, proj string
	for i := 0; i < 4; i++ {
		p := fmt.Sprintf("%.1f,%.1f", scaleX(i), scaleY(values[i]))
		if i == 0 {
			hist = "M " + p
		} else {
			hist += " L " + p
		}
	}
	proj = fmt.Sprintf("M %.1f,%.1f L %.1f,%.1f L %.1f,%.1f", scaleX(3), scaleY(values[3]), scaleX(4), scaleY(values[4]), scaleX(5), scaleY(values[5]))
	return hist, proj
}

func cashflowAxisLabels(year, month int, width float64) []CashflowLabel {
	base := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	abbr := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	out := make([]CashflowLabel, 0, 6)
	for i := -3; i <= 2; i++ {
		d := base.AddDate(0, i, 0)
		txt := abbr[int(d.Month())-1]
		if i >= 1 {
			txt += " (P)"
		}
		x := 10 + (float64(i+3)/5.0)*(width-20)
		out = append(out, CashflowLabel{X: x, Text: txt})
	}
	return out
}

func relatoriosMonthQuery(mes, ano int) string {
	return fmt.Sprintf("mes=%d&ano=%d", mes, ano)
}

func relatoriosURL(mes, ano int) string {
	return "/relatorios?" + relatoriosMonthQuery(mes, ano)
}

func buildRelatoriosMonthOptions(ano, mesAtual int) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	now := time.Now().UTC()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	options := make([]MonthOption, 0, 12)
	for month := 1; month <= 12; month++ {
		m := time.Date(ano, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		query := relatoriosMonthQuery(int(m.Month()), m.Year())
		options = append(options, MonthOption{
			Label:     shortMonths[int(m.Month())-1],
			Year:      fmt.Sprintf("%d", m.Year()),
			URL:       "/relatorios?" + query,
			Query:     query,
			IsActive:  month == mesAtual,
			IsCurrent: m.Equal(currentMonth),
		})
	}
	return options
}
