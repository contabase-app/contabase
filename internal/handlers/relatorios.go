package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

type RelatoriosHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

type RelatoriosData struct {
	OOB                             bool
	Title                           string
	UserInitials                    string
	ProfilePhotoURL                 string
	IsBusiness                      bool
	ActiveWorkspaceName             string
	ActiveTab                       string
	ExtratoView                     string
	CaixaModo                       string
	IsCaixaRealizado                bool
	IsCaixaPrevisao                 bool
	MesAtual                        int
	AnoAtual                        int
	MonthLabel                      string
	MonthOptions                    []MonthOption
	MonthSelectorHXGet              string
	MonthSelectorHXSelect           string
	MonthSelectorHXTarget           string
	MonthSelectorHXSwap             string
	MonthSelectorPartial            string
	MonthSelectorPrevQuery          string
	MonthSelectorNextQuery          string
	MonthSelectorCurrentQuery       string
	MesAnteriorURL                  string
	MesSeguinteURL                  string
	CurrentMonthURL                 string
	TotalReceitas                   MoneyDisplay
	TotalDespesas                   MoneyDisplay
	SaldoLiquido                    MoneyDisplay
	SaldoAcumulado                  MoneyDisplay
	SaldoAcumuladoNegativo          bool
	SaldoClass                      string
	SaldoNegativo                   bool
	Categorias                      []CategoriaBar
	Membros                         []MembroBar
	DonutEssentialPercent           int
	DonutEssentialPercentLabel      string
	DonutLifestylePercent           int
	DonutLifestylePercentLabel      string
	DonutEssentialTotal             MoneyDisplay
	DonutLifestyleTotal             MoneyDisplay
	HasUncategorizedAlert           bool
	UncategorizedPercent            int
	UncategorizedAmount             MoneyDisplay
	PendingPayableTotal             MoneyDisplay
	PendingReceivableTotal          MoneyDisplay
	OverdueReceivableTotal          MoneyDisplay
	DREMacroGroups                  []string
	DRE                             []DRERow
	CaixaMacroGroups                []string
	Caixa                           []CaixaRow
	RevenueByCategory               []RevenueCategorySlice
	RevenueByCategoryCaixa          []RevenueCategorySlice
	TopClientsByRevenue             []TopClientRevenue
	RealizedCashTotal               MoneyDisplay
	ProjectedCashTotal              MoneyDisplay
	RealizedIncomeTotal             MoneyDisplay
	RealizedExpenseTotal            MoneyDisplay
	ProjectedIncomeTotal            MoneyDisplay
	ProjectedExpenseTotal           MoneyDisplay
	CashMonthIncomeTotal            MoneyDisplay
	CashMonthExpenseTotal           MoneyDisplay
	CashMonthResultTotal            MoneyDisplay
	ProjectedEndingBalance          MoneyDisplay
	RealizedCashNegative            bool
	ProjectedCashNegative           bool
	CashMonthResultNegative         bool
	ProjectedEndingBalanceNegative  bool
	ShowProjectedEndingBalance      bool
	RealizedCashCount               int
	UpcomingCashItems               []UpcomingCashItemDisplay
	BusinessCosts                   []BusinessCostMacroRow
	BusinessCostsCaixa              []BusinessCostMacroRow
	CategoriasCaixa                 []CategoriaBar
	DonutCaixaEssentialTotal        MoneyDisplay
	DonutCaixaLifestyleTotal        MoneyDisplay
	DonutCaixaEssentialPercent      int
	DonutCaixaEssentialPercentLabel string
	DonutCaixaLifestylePercent      int
	DonutCaixaLifestylePercentLabel string
	ExtratoRows                     []StatementExtratoRow
	DailySummary                    []DailySummaryRow
	ExtratoMovementCount            int
}

type CategoriaBar struct {
	ID                  string
	Nome                string
	Icon                string
	Color               string
	Valor               MoneyDisplay
	Percent             int
	PercentLabel        string
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
	Name         string
	Amount       MoneyDisplay
	Percent      int
	PercentLabel string
	Stroke       string
	DashOffset   int
}

type TopClientRevenue struct {
	Name      string
	Amount    MoneyDisplay
	Percent   int
	BarWidth  int
	Highlight string
}

type BusinessCostMacroRow struct {
	MacroGroup   string
	Amount       MoneyDisplay
	RawAmount    int64
	Percent      int
	PercentLabel string
	Stroke       string
	DashOffset   int
}

type UpcomingCashItemDisplay struct {
	DateLabel   string
	Description string
	IsIncome    bool
	Amount      MoneyDisplay
}

type DailySummaryRow struct {
	DateLabel          string
	IncomeTotal        MoneyDisplay
	ExpenseTotal       MoneyDisplay
	NetTotal           MoneyDisplay
	AccumulatedBalance MoneyDisplay
	RawIncome          int64
	RawExpense         int64
	RawAccumulated     int64
	HasIncome          bool
	HasExpense         bool
	NetPositive        bool
	NetNegative        bool
	NetClass           string
	AccumulatedClass   string
}

type DRERow struct {
	Competencia       string
	MacroGroups       []DREMacroGroupAmount
	Resultado         MoneyDisplay
	RawResultado      int64
	ResultadoNegativo bool
}

type DREMacroGroupAmount struct {
	MacroGroup string
	Amount     MoneyDisplay
	RawAmount  int64
}

type CaixaRow struct {
	Vencimento                string
	MacroGroups               []CaixaMacroGroupAmount
	Resultado                 MoneyDisplay
	RawResultado              int64
	ResultadoNegativo         bool
	ProjectedBalance          MoneyDisplay
	RawProjectedBalance       int64
	ProjectedBalanceNegative  bool
	HasProjectedEndingBalance bool
}

type CaixaMacroGroupAmount struct {
	MacroGroup string
	Amount     MoneyDisplay
	RawAmount  int64
}

func (h *RelatoriosHandler) HandleExibirRelatorios(w http.ResponseWriter, r *http.Request) {
	mesStr := r.URL.Query().Get("mes")
	anoStr := r.URL.Query().Get("ano")
	tab := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("tab")))
	if tab != "caixa" && tab != "competencia" && tab != "extrato" {
		tab = "caixa"
	}
	if tab == "competencia" && workspaceType(h.DB, h.WorkspaceID) != "business" {
		tab = "caixa"
	}
	modo := normalizeCaixaModo(r.URL.Query().Get("modo"))

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

	view := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("view")))
	data, err := h.buildRelatoriosDataWithModo("", tab, view, modo, mes, ano)
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
	tab := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("tab")))
	if tab != "caixa" && tab != "competencia" && tab != "extrato" {
		tab = "caixa"
	}
	if tab == "competencia" && workspaceType(h.DB, h.WorkspaceID) != "business" {
		tab = "caixa"
	}
	modo := normalizeCaixaModo(r.URL.Query().Get("modo"))

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
	view := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("view")))
	data, err := h.buildRelatoriosDataWithModo(reqID, tab, view, modo, mes, ano)
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
	case "caixa":
		modo := normalizeCaixaModo(r.URL.Query().Get("modo"))
		ano := time.Now().Year()
		mes := int(time.Now().Month())
		if raw := strings.TrimSpace(r.URL.Query().Get("mes")); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 12 {
				mes = v
			}
		}
		if raw := strings.TrimSpace(r.URL.Query().Get("ano")); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v >= 2020 && v <= time.Now().Year()+10 {
				ano = v
			}
		}
		if modo == "previsao" {
			rows, err := h.queryCaixaRows(ano, mes, modo)
			if err != nil {
				http.Error(w, "erro ao gerar csv", http.StatusInternalServerError)
				return
			}
			_ = writer.Write([]string{"mes", "macro_group", "total_centavos", "saldo_projetado_centavos"})
			for _, row := range rows {
				projectedBalance := ""
				if row.HasProjectedEndingBalance {
					projectedBalance = strconv.FormatInt(row.RawProjectedBalance, 10)
				}
				for _, item := range row.MacroGroups {
					_ = writer.Write([]string{
						row.Vencimento,
						sanitizeCSVField(item.MacroGroup),
						strconv.FormatInt(item.RawAmount, 10),
						projectedBalance,
					})
				}
				if len(row.MacroGroups) == 0 {
					_ = writer.Write([]string{row.Vencimento, "", "0", projectedBalance})
				}
			}
			return
		}
		rows, err := h.queryCaixaRawRows(ano, mes, modo)
		if err != nil {
			http.Error(w, "erro ao gerar csv", http.StatusInternalServerError)
			return
		}
		_ = writer.Write([]string{"mes", "macro_group", "total_centavos"})
		for _, row := range rows {
			_ = writer.Write([]string{
				row.Vencimento,
				sanitizeCSVField(row.MacroGroup),
				strconv.FormatInt(row.AmountCents, 10),
			})
		}
	case "extrato-movimentos", "extrato-diario":
		mes, ano, err := parseExtratoExportPeriod(r)
		if err != nil {
			http.Error(w, "periodo invalido", http.StatusBadRequest)
			return
		}
		data, err := h.buildRelatoriosData("", "extrato", "movimentos", mes, ano)
		if err != nil {
			http.Error(w, "erro ao gerar csv", http.StatusInternalServerError)
			return
		}
		if tipo == "extrato-movimentos" {
			_ = writer.Write([]string{"data", "descricao", "conta", "categoria", "tipo", "entrada_centavos", "saida_centavos", "valor_centavos"})
			for _, row := range data.ExtratoRows {
				txType := row.Tipo
				if txType == models.TransactionTypeIncome {
					txType = "Entrada"
				} else if txType == models.TransactionTypeExpense {
					txType = "Saida"
				} else if txType == models.TransactionTypeTransfer {
					txType = "Transferencia"
				}
				var entradaCents, saidaCents int64
				if row.Tipo == models.TransactionTypeIncome || row.Entrada.Reais != "" {
					entradaCents = row.AmountCents
				}
				if row.Tipo == models.TransactionTypeExpense || row.Saida.Reais != "" {
					saidaCents = row.AmountCents
				}
				_ = writer.Write([]string{
					row.DateLabel,
					sanitizeCSVField(row.Description),
					sanitizeCSVField(row.AccountName),
					sanitizeCSVField(row.CategoryName),
					txType,
					strconv.FormatInt(entradaCents, 10),
					strconv.FormatInt(saidaCents, 10),
					strconv.FormatInt(0, 10),
				})
			}
		} else {
			_ = writer.Write([]string{"data", "entradas_centavos", "saidas_centavos", "resultado_centavos", "acumulado_centavos"})
			for _, row := range data.DailySummary {
				_ = writer.Write([]string{
					row.DateLabel,
					strconv.FormatInt(row.RawIncome, 10),
					strconv.FormatInt(row.RawExpense, 10),
					strconv.FormatInt(row.RawIncome-row.RawExpense, 10),
					strconv.FormatInt(row.RawAccumulated, 10),
				})
			}
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

func parseExtratoExportPeriod(r *http.Request) (int, int, error) {
	now := time.Now().UTC()
	mes := int(now.Month())
	ano := now.Year()

	if raw := strings.TrimSpace(r.URL.Query().Get("mes")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 12 {
			return 0, 0, fmt.Errorf("invalid mes")
		}
		mes = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("ano")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2020 || parsed > now.Year()+10 {
			return 0, 0, fmt.Errorf("invalid ano")
		}
		ano = parsed
	}

	return mes, ano, nil
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
		PercentLabel:        percentLabel(amount, totalDespesas),
		IsChild:             isChild,
		ParentID:            parentID,
		ParentName:          parentName,
		IsGroup:             isGroup,
		IsDirectParentEntry: isDirectParentEntry,
	}
}

func categoriaPercent(amount int64, totalDespesas int64) int {
	if totalDespesas <= 0 || amount <= 0 {
		return 0
	}
	return int((amount * 100) / totalDespesas)
}

func percentLabel(amount, total int64) string {
	if total <= 0 || amount <= 0 {
		return "0%"
	}
	if amount*10000 < total {
		return "<0,01%"
	}
	basisPoints := ((amount * 10000) + (total / 2)) / total
	if basisPoints == 0 {
		return "<0,01%"
	}
	if basisPoints < 1000 {
		return fmt.Sprintf("%d,%02d%%", basisPoints/100, basisPoints%100)
	}
	roundedPercent := ((amount * 100) + (total / 2)) / total
	return fmt.Sprintf("%d%%", roundedPercent)
}

func (h *RelatoriosHandler) buildRelatoriosData(reqID, tab, view string, mes, ano int) (RelatoriosData, error) {
	return h.buildRelatoriosDataWithModo(reqID, tab, view, "realizado", mes, ano)
}

func (h *RelatoriosHandler) buildRelatoriosDataWithModo(reqID, tab, view, modo string, mes, ano int) (RelatoriosData, error) {
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	if tab != "caixa" && tab != "competencia" && tab != "extrato" {
		tab = "caixa"
	}
	if tab == "competencia" && !isBusiness {
		tab = "caixa"
	}
	if view != "movimentos" && view != "diario" {
		view = "movimentos"
	}
	modo = normalizeCaixaModo(modo)
	tS := time.Now()
	months := []string{
		"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho",
		"Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro",
	}

	extraParams := tabViewExtraParams(tab, view, modo)
	data := RelatoriosData{
		Title:                     "Relatórios",
		UserInitials:              queryUserInitialsByID(h.DB, h.UserID),
		ProfilePhotoURL:           queryUserProfilePhotoURL(h.DB, h.UserID),
		IsBusiness:                isBusiness,
		ActiveWorkspaceName:       queryWorkspaceName(h.DB, h.WorkspaceID),
		ActiveTab:                 tab,
		ExtratoView:               view,
		CaixaModo:                 modo,
		IsCaixaRealizado:          modo == "realizado",
		IsCaixaPrevisao:           modo == "previsao",
		MesAtual:                  mes,
		AnoAtual:                  ano,
		MonthLabel:                fmt.Sprintf("%s %d", months[mes-1], ano),
		MonthSelectorHXGet:        "/relatorios",
		MonthSelectorHXTarget:     "#relatorios-body",
		MonthSelectorHXSelect:     "#relatorios-body",
		MonthSelectorHXSwap:       "outerHTML",
		MonthSelectorPartial:      "content",
		MonthSelectorPrevQuery:    relatoriosMonthQuery(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Year(), extraParams),
		MonthSelectorNextQuery:    relatoriosMonthQuery(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Year(), extraParams),
		MonthSelectorCurrentQuery: relatoriosMonthQuery(int(time.Now().UTC().Month()), time.Now().UTC().Year(), extraParams),
		MesAnteriorURL:            relatoriosURL(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Year(), extraParams),
		MesSeguinteURL:            relatoriosURL(int(time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Month()), time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0).Year(), extraParams),
		CurrentMonthURL:           relatoriosURL(int(time.Now().UTC().Month()), time.Now().UTC().Year(), extraParams),
		MonthOptions:              buildRelatoriosMonthOptions(ano, mes, tab, view, modo),
	}

	loc := time.UTC
	monthStart := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, loc).Unix()
	nextMonth := time.Date(ano, time.Month(mes)+1, 1, 0, 0, 0, 0, loc)
	monthEnd := nextMonth.Add(-1 * time.Second).Unix()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	perfStep(reqID, "Relatorios", "init+metadata", time.Since(tS))
	tS = time.Now()

	period := NewReportPeriodFromMonth(ano, mes)
	var totalReceitas, totalDespesas int64
	if tab == "caixa" && modo == "realizado" {
		if err := h.DB.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END), 0)
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
			  AND t.status = 'paid'
			  AND t.type IN ('INCOME', 'EXPENSE')
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ? AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
		`, h.WorkspaceID, monthStart, monthEnd).Scan(&totalReceitas, &totalDespesas); err != nil {
			return data, fmt.Errorf("query totals caixa realizado: %w", err)
		}
	} else if tab == "caixa" {
		realized, projected, err := BuildPrevisaoCashFlowData(h.DB, h.WorkspaceID, period)
		if err != nil {
			return data, fmt.Errorf("build caixa previsao totals: %w", err)
		}
		allForecastEvents := append(append([]CashEvent{}, realized...), projected...)
		totalReceitas = SumCashIncome(allForecastEvents)
		totalDespesas = SumCashExpense(allForecastEvents)
	} else {
		if err := h.DB.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
				FROM transactions
				WHERE workspace_id = ? AND date >= ? AND date <= ?
				  AND `+excludeInvoicePaymentCompetenceClause("")+`
			`, h.WorkspaceID, monthStart, monthEnd).Scan(&totalReceitas, &totalDespesas); err != nil {
			return data, fmt.Errorf("query totals: %w", err)
		}
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

	var realizedEvents, projectedEvents []CashEvent
	var err error
	if tab == "caixa" {
		if modo == "previsao" {
			realizedEvents, projectedEvents, err = BuildPrevisaoCashFlowData(h.DB, h.WorkspaceID, period)
			if err != nil {
				return data, fmt.Errorf("build previsao cash flow data: %w", err)
			}
		} else {
			realizedEvents, err = BuildRealizadoCashFlowData(h.DB, h.WorkspaceID, period)
			if err != nil {
				return data, fmt.Errorf("build realizado cash flow data: %w", err)
			}
			projectedEvents = []CashEvent{}
		}
	} else {
		realizedEvents, projectedEvents, err = BuildCashFlowDataByDueDate(h.DB, h.WorkspaceID, period)
		if err != nil {
			return data, fmt.Errorf("build cash flow data: %w", err)
		}
	}
	realizedIncomeTotal := SumCashIncome(realizedEvents)
	realizedExpenseTotal := SumCashExpense(realizedEvents)
	realizedCashTotal := SumCashEvents(realizedEvents)
	projectedIncomeTotal := SumCashIncome(projectedEvents)
	projectedExpenseTotal := SumCashExpense(projectedEvents)
	projectedCashTotal := SumCashEvents(projectedEvents)
	var monthIncomeTotal, monthExpenseTotal, monthResultTotal int64
	if tab == "caixa" && modo == "realizado" {
		monthIncomeTotal = realizedIncomeTotal
		monthExpenseTotal = realizedExpenseTotal
		monthResultTotal = realizedCashTotal
	} else if tab == "caixa" {
		monthIncomeTotal = realizedIncomeTotal + projectedIncomeTotal
		monthExpenseTotal = realizedExpenseTotal + projectedExpenseTotal
		monthResultTotal = realizedCashTotal + projectedCashTotal
		projectedIncomeTotal = monthIncomeTotal
		projectedExpenseTotal = monthExpenseTotal
		projectedCashTotal = monthResultTotal
	} else {
		monthIncomeTotal = realizedIncomeTotal + projectedIncomeTotal
		monthExpenseTotal = realizedExpenseTotal + projectedExpenseTotal
		monthResultTotal = realizedCashTotal + projectedCashTotal
	}

	data.RealizedIncomeTotal = MoneyMinor(realizedIncomeTotal)
	data.RealizedExpenseTotal = MoneyMinor(realizedExpenseTotal)
	data.RealizedCashTotal = MoneyMinor(realizedCashTotal)
	data.ProjectedIncomeTotal = MoneyMinor(projectedIncomeTotal)
	data.ProjectedExpenseTotal = MoneyMinor(projectedExpenseTotal)
	data.ProjectedCashTotal = MoneyMinor(projectedCashTotal)
	data.CashMonthIncomeTotal = MoneyMinor(monthIncomeTotal)
	data.CashMonthExpenseTotal = MoneyMinor(monthExpenseTotal)
	data.CashMonthResultTotal = MoneyMinor(monthResultTotal)
	data.RealizedCashNegative = realizedCashTotal < 0
	data.ProjectedCashNegative = projectedCashTotal < 0
	data.CashMonthResultNegative = monthResultTotal < 0
	data.RealizedCashCount = len(realizedEvents)
	if tab == "caixa" && modo == "previsao" {
		projectedEndingBalance, showProjectedEndingBalance, err := BuildProjectedBalanceUntilPeriodEnd(h.DB, h.WorkspaceID, monthEnd, time.Now())
		if err != nil {
			return data, fmt.Errorf("build projected ending balance: %w", err)
		}
		data.ShowProjectedEndingBalance = showProjectedEndingBalance
		if showProjectedEndingBalance {
			data.ProjectedEndingBalance = MoneyMinor(projectedEndingBalance)
			data.ProjectedEndingBalanceNegative = projectedEndingBalance < 0
		}
	}

	data.UpcomingCashItems = []UpcomingCashItemDisplay{}
	if tab != "caixa" && len(projectedEvents) > 0 {
		if len(projectedEvents) > 5 {
			sort.SliceStable(projectedEvents, func(i, j int) bool {
				return projectedEvents[i].DateUnix < projectedEvents[j].DateUnix
			})
			projectedEvents = projectedEvents[:5]
		}
		data.UpcomingCashItems = make([]UpcomingCashItemDisplay, 0, len(projectedEvents))
		for _, ev := range projectedEvents {
			t := time.Unix(ev.DateUnix, 0).UTC()
			data.UpcomingCashItems = append(data.UpcomingCashItems, UpcomingCashItemDisplay{
				DateLabel:   fmt.Sprintf("%02d/%02d", t.Day(), int(t.Month())),
				Description: ev.Description,
				IsIncome:    ev.IsIncome,
				Amount:      MoneyMinor(ev.AmountCents),
			})
		}
	}
	perfStep(reqID, "Relatorios", "cashFlowData", time.Since(tS))
	tS = time.Now()

	var totalBalance int64
	_ = h.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(current_balance), 0) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, h.WorkspaceID).Scan(&totalBalance)
	var saldoAcumulado int64
	if tab == "caixa" && modo == "realizado" {
		saldoAcumulado = totalBalance
	} else {
		selectedMonth := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)
		saldoAcumulado = calcProjectedAccumulatedBalance(h.DB, h.WorkspaceID, totalBalance, selectedMonth)
	}
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

		costs, err := h.queryBusinessCostsByMacro(ctx, mes, ano)
		if err != nil {
			return data, fmt.Errorf("query business costs by macro: %w", err)
		}
		data.BusinessCosts = costs

		revenueSlicesCaixa, err := h.queryRevenueByCategoryCaixa(ctx, monthStart, monthEnd, modo)
		if err != nil {
			return data, fmt.Errorf("query revenue by category caixa: %w", err)
		}
		data.RevenueByCategoryCaixa = revenueSlicesCaixa

		costsCaixa, err := h.queryBusinessCostsByMacroCaixa(ctx, monthStart, monthEnd, modo)
		if err != nil {
			return data, fmt.Errorf("query business costs by macro caixa: %w", err)
		}
		data.BusinessCostsCaixa = costsCaixa
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
		data.DonutEssentialPercentLabel = percentLabel(essentialTotal, totalDespesas)
		data.DonutLifestylePercentLabel = percentLabel(lifestyleTotal, totalDespesas)

		essentialTotalCaixa, lifestyleTotalCaixa, err := h.queryMacroGroupTotalsCaixa(ctx, monthStart, monthEnd, modo)
		if err != nil {
			return data, fmt.Errorf("query macro groups caixa: %w", err)
		}
		data.DonutCaixaEssentialTotal = MoneyMinor(essentialTotalCaixa)
		data.DonutCaixaLifestyleTotal = MoneyMinor(lifestyleTotalCaixa)
		if monthExpenseTotal > 0 {
			data.DonutCaixaEssentialPercent = int((essentialTotalCaixa * 100) / monthExpenseTotal)
			data.DonutCaixaLifestylePercent = int((lifestyleTotalCaixa * 100) / monthExpenseTotal)
		}
		data.DonutCaixaEssentialPercentLabel = percentLabel(essentialTotalCaixa, monthExpenseTotal)
		data.DonutCaixaLifestylePercentLabel = percentLabel(lifestyleTotalCaixa, monthExpenseTotal)
	}
	perfStep(reqID, "Relatorios", "businessOrMacro", time.Since(tS))
	tS = time.Now()

	dreRows, err := h.queryDRERows(ano)
	if err != nil {
		return data, fmt.Errorf("query dre: %w", err)
	}
	data.DRE = dreRows
	data.DREMacroGroups = extractDREMacroGroups(dreRows)

	caixaRows, err := h.queryCaixaRows(ano, mes, modo)
	if err != nil {
		return data, fmt.Errorf("query caixa: %w", err)
	}
	data.Caixa = caixaRows
	data.CaixaMacroGroups = extractCaixaMacroGroups(caixaRows)
	if tab == "caixa" && modo == "previsao" {
		selectedVencimento := fmt.Sprintf("%04d-%02d", ano, mes)
		for _, row := range caixaRows {
			if row.Vencimento != selectedVencimento {
				continue
			}
			income, expense := caixaRowIncomeExpense(row)
			result := income - expense
			data.ProjectedIncomeTotal = MoneyMinor(income)
			data.ProjectedExpenseTotal = MoneyMinor(expense)
			data.ProjectedCashTotal = MoneyMinor(result)
			data.CashMonthIncomeTotal = MoneyMinor(income)
			data.CashMonthExpenseTotal = MoneyMinor(expense)
			data.CashMonthResultTotal = MoneyMinor(result)
			data.ProjectedCashNegative = result < 0
			data.CashMonthResultNegative = result < 0
			data.ShowProjectedEndingBalance = row.HasProjectedEndingBalance
			if row.HasProjectedEndingBalance {
				data.ProjectedEndingBalance = row.ProjectedBalance
				data.ProjectedEndingBalanceNegative = row.ProjectedBalanceNegative
			} else {
				data.ProjectedEndingBalance = MoneyDisplay{}
				data.ProjectedEndingBalanceNegative = false
			}
			break
		}
	}

	categoriasCaixa, err := h.queryExpenseCategoriesCaixa(ctx, monthStart, monthEnd, modo)
	if err != nil {
		return data, fmt.Errorf("query expense categories caixa: %w", err)
	}
	data.CategoriasCaixa = categoriasCaixa
	perfStep(reqID, "Relatorios", "queryDRE+Caixa", time.Since(tS))
	tS = time.Now()

	extratoRows, err := BuildExtratoData(h.DB, h.WorkspaceID, period)
	if err != nil {
		return data, fmt.Errorf("build extrato data: %w", err)
	}
	data.ExtratoRows = extratoRows
	perfStep(reqID, "Relatorios", "extratoData", time.Since(tS))
	tS = time.Now()

	dailyMap := make(map[string]*DailySummaryRow)
	var dayOrder []string
	var accumulated int64
	for _, r := range extratoRows {
		if r.IsTransfer {
			continue
		}
		dl := r.DateLabel
		ds, ok := dailyMap[dl]
		if !ok {
			ds = &DailySummaryRow{DateLabel: dl}
			dailyMap[dl] = ds
			dayOrder = append(dayOrder, dl)
		}
		if r.Tipo == "INCOME" {
			ds.RawIncome += r.AmountCents
		} else if r.Tipo == "EXPENSE" {
			ds.RawExpense += r.AmountCents
		}
	}
	for _, dl := range dayOrder {
		ds := dailyMap[dl]
		net := ds.RawIncome - ds.RawExpense
		accumulated += net
		ds.IncomeTotal = MoneyMinor(ds.RawIncome)
		ds.ExpenseTotal = MoneyMinor(ds.RawExpense)
		ds.NetTotal = MoneyMinor(net)
		ds.RawAccumulated = accumulated
		ds.AccumulatedBalance = MoneyMinor(accumulated)
		ds.HasIncome = ds.RawIncome > 0
		ds.HasExpense = ds.RawExpense > 0
		ds.NetPositive = net > 0
		ds.NetNegative = net < 0
		if net < 0 {
			ds.NetClass = "text-[var(--cb-danger)]"
		} else {
			ds.NetClass = "text-[var(--cb-success)]"
		}
		if accumulated < 0 {
			ds.AccumulatedClass = "text-[var(--cb-danger)]"
		} else {
			ds.AccumulatedClass = ""
		}
		data.DailySummary = append(data.DailySummary, *ds)
	}
	data.ExtratoMovementCount = len(extratoRows)
	perfStep(reqID, "Relatorios", "extratoData", time.Since(tS))

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
			Competencia:       competencia,
			MacroGroups:       groupAmounts,
			Resultado:         MoneyMinor(total),
			RawResultado:      total,
			ResultadoNegativo: total < 0,
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
			Name:         item.Name,
			Amount:       MoneyMinor(item.Amount),
			Percent:      percent,
			PercentLabel: percentLabel(item.Amount, totalRevenue),
			Stroke:       colors[idx%len(colors)],
			DashOffset:   25 - cumulative,
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

func (h *RelatoriosHandler) queryBusinessCostsByMacro(ctx context.Context, mes, ano int) ([]BusinessCostMacroRow, error) {
	dreRows, err := BuildDREData(h.DB, h.WorkspaceID, ano)
	if err != nil {
		return nil, err
	}

	competencia := fmt.Sprintf("%04d-%02d", ano, mes)

	type macroAcc struct {
		total  int64
		isCost bool
	}
	macroExpenses := make(map[string]*macroAcc)
	var totalCost int64

	for _, row := range dreRows {
		if row.Competencia != competencia {
			continue
		}
		if row.AmountCents >= 0 {
			continue
		}
		cost := -row.AmountCents
		mg := row.MacroGroup
		if acc, ok := macroExpenses[mg]; ok {
			acc.total += cost
		} else {
			macroExpenses[mg] = &macroAcc{total: cost, isCost: true}
		}
		totalCost += cost
	}

	var out []BusinessCostMacroRow
	for mg, acc := range macroExpenses {
		if acc.total <= 0 {
			continue
		}
		pct := 0
		if totalCost > 0 {
			pct = int((acc.total * 100) / totalCost)
		}
		out = append(out, BusinessCostMacroRow{
			MacroGroup:   mg,
			Amount:       MoneyMinor(acc.total),
			RawAmount:    acc.total,
			Percent:      pct,
			PercentLabel: percentLabel(acc.total, totalCost),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RawAmount != out[j].RawAmount {
			return out[i].RawAmount > out[j].RawAmount
		}
		return out[i].MacroGroup < out[j].MacroGroup
	})

	colors := []string{
		"rgb(239 68 68)",
		"rgb(249 115 22)",
		"rgb(234 179 8)",
		"rgb(168 85 247)",
		"rgb(99 102 241)",
		"rgb(14 165 233)",
	}
	cumulative := 0
	for i := range out {
		out[i].Stroke = colors[i%len(colors)]
		out[i].DashOffset = 25 - cumulative
		cumulative += out[i].Percent
	}

	return out, nil
}

func (h *RelatoriosHandler) queryCaixaRows(ano, mes int, modo string) ([]CaixaRow, error) {
	modo = normalizeCaixaModo(modo)
	rawRows, err := h.queryCaixaRawRows(ano, mes, modo)
	if err != nil {
		return nil, err
	}
	groupOrder := make([]string, 0)
	groupSeen := make(map[string]struct{})
	rowsByVencimento := make(map[string]map[string]int64)
	vencimentos := make([]string, 0)
	vencimentoSeen := make(map[string]struct{})
	for _, raw := range rawRows {
		if _, ok := vencimentoSeen[raw.Vencimento]; !ok {
			vencimentoSeen[raw.Vencimento] = struct{}{}
			vencimentos = append(vencimentos, raw.Vencimento)
		}
		if _, ok := groupSeen[raw.MacroGroup]; !ok {
			groupSeen[raw.MacroGroup] = struct{}{}
			groupOrder = append(groupOrder, raw.MacroGroup)
		}
		if _, ok := rowsByVencimento[raw.Vencimento]; !ok {
			rowsByVencimento[raw.Vencimento] = make(map[string]int64)
		}
		rowsByVencimento[raw.Vencimento][raw.MacroGroup] += raw.AmountCents
	}
	for _, vencimento := range caixaWindowMonths(ano, mes, modo) {
		if _, ok := vencimentoSeen[vencimento]; ok {
			continue
		}
		vencimentoSeen[vencimento] = struct{}{}
		vencimentos = append(vencimentos, vencimento)
		if _, ok := rowsByVencimento[vencimento]; !ok {
			rowsByVencimento[vencimento] = make(map[string]int64)
		}
	}
	sort.Strings(vencimentos)
	out := make([]CaixaRow, 0, len(vencimentos))
	for _, vencimento := range vencimentos {
		groupAmounts := make([]CaixaMacroGroupAmount, 0, len(groupOrder))
		var total int64
		for _, macroGroup := range groupOrder {
			value := rowsByVencimento[vencimento][macroGroup]
			groupAmounts = append(groupAmounts, CaixaMacroGroupAmount{
				MacroGroup: macroGroup,
				Amount:     MoneyMinor(value),
				RawAmount:  value,
			})
			total += value
		}
		out = append(out, CaixaRow{
			Vencimento:        vencimento,
			MacroGroups:       groupAmounts,
			Resultado:         MoneyMinor(total),
			RawResultado:      total,
			ResultadoNegativo: total < 0,
		})
	}
	if modo == "previsao" {
		if err := h.applyProjectedBalancesToCaixaRows(out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (h *RelatoriosHandler) queryCaixaRawRows(ano, mes int, modo string) ([]CaixaRowView, error) {
	if normalizeCaixaModo(modo) == "previsao" {
		return BuildCaixaData(h.DB, h.WorkspaceID, ano, mes)
	}
	return BuildCaixaRealizadoData(h.DB, h.WorkspaceID, ano)
}

func caixaRowIncomeExpense(row CaixaRow) (int64, int64) {
	var income, expense int64
	for _, item := range row.MacroGroups {
		if item.RawAmount > 0 {
			income += item.RawAmount
		} else if item.RawAmount < 0 {
			expense += -item.RawAmount
		}
	}
	return income, expense
}

func (h *RelatoriosHandler) applyProjectedBalancesToCaixaRows(rows []CaixaRow) error {
	now := time.Now()
	for i := range rows {
		periodEnd, ok := caixaRowPeriodEnd(rows[i].Vencimento)
		if !ok {
			continue
		}
		balance, show, err := BuildProjectedBalanceUntilPeriodEnd(h.DB, h.WorkspaceID, periodEnd, now)
		if err != nil {
			return err
		}
		rows[i].HasProjectedEndingBalance = show
		if show {
			rows[i].RawProjectedBalance = balance
			rows[i].ProjectedBalance = MoneyMinor(balance)
			rows[i].ProjectedBalanceNegative = balance < 0
		}
	}
	return nil
}

func caixaRowPeriodEnd(vencimento string) (int64, bool) {
	parsed, err := time.Parse("2006-01", vencimento)
	if err != nil {
		return 0, false
	}
	return parsed.AddDate(0, 1, 0).Add(-time.Second).Unix(), true
}

func caixaWindowMonths(ano, mes int, modo string) []string {
	if normalizeCaixaModo(modo) == "previsao" {
		start := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)
		out := make([]string, 0, 13)
		for i := 0; i <= 12; i++ {
			out = append(out, start.AddDate(0, i, 0).Format("2006-01"))
		}
		return out
	}
	out := make([]string, 0, 12)
	for month := 1; month <= 12; month++ {
		out = append(out, fmt.Sprintf("%04d-%02d", ano, month))
	}
	return out
}

func extractCaixaMacroGroups(rows []CaixaRow) []string {
	if len(rows) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows[0].MacroGroups))
	for _, item := range rows[0].MacroGroups {
		out = append(out, item.MacroGroup)
	}
	return out
}

func caixaMacroValue(groups []CaixaMacroGroupAmount, macroGroup string) MoneyDisplay {
	for _, item := range groups {
		if item.MacroGroup == macroGroup {
			return item.Amount
		}
	}
	return MoneyMinor(0)
}

func (h *RelatoriosHandler) queryRevenueByCategoryCaixa(ctx context.Context, monthStart, monthEnd int64, modo string) ([]RevenueCategorySlice, error) {
	sourceSQL := `
		SELECT
			COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), 'Sem categoria') AS category_name,
			t.amount AS amount
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
		  AND t.type = 'INCOME'
		  AND t.status = 'paid'
		  AND a.type != 'CREDIT_CARD'
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
	`
	args := []any{h.WorkspaceID, monthStart, monthEnd}
	if normalizeCaixaModo(modo) == "previsao" {
		sourceSQL += `
			UNION ALL
			SELECT
				COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), 'Sem categoria') AS category_name,
				t.amount AS amount
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ?
			  AND t.type = 'INCOME'
			  AND t.status = 'pending'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
		`
		args = append(args, h.WorkspaceID, monthStart, monthEnd)
	}
	rows, err := h.DB.QueryContext(ctx, `
		SELECT
			category_name,
			COALESCE(SUM(amount), 0) AS total
		FROM (`+sourceSQL+`)
		GROUP BY category_name
		ORDER BY total DESC
	`, args...)
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
			Name:         item.Name,
			Amount:       MoneyMinor(item.Amount),
			Percent:      percent,
			PercentLabel: percentLabel(item.Amount, totalRevenue),
			Stroke:       colors[idx%len(colors)],
			DashOffset:   25 - cumulative,
		})
		cumulative += percent
	}
	return slices, nil
}

func (h *RelatoriosHandler) queryBusinessCostsByMacroCaixa(ctx context.Context, monthStart, monthEnd int64, modo string) ([]BusinessCostMacroRow, error) {
	macroExpenses, totalCost, err := h.queryCaixaExpenseMacroAnalysis(ctx, monthStart, monthEnd, modo)
	if err != nil {
		return nil, err
	}
	if totalCost <= 0 {
		return nil, nil
	}

	var out []BusinessCostMacroRow
	for mg, total := range macroExpenses {
		if total <= 0 {
			continue
		}
		pct := 0
		if totalCost > 0 {
			pct = int((total * 100) / totalCost)
		}
		out = append(out, BusinessCostMacroRow{
			MacroGroup:   mg,
			Amount:       MoneyMinor(total),
			RawAmount:    total,
			Percent:      pct,
			PercentLabel: percentLabel(total, totalCost),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RawAmount != out[j].RawAmount {
			return out[i].RawAmount > out[j].RawAmount
		}
		return out[i].MacroGroup < out[j].MacroGroup
	})

	colors := []string{
		"rgb(239 68 68)",
		"rgb(249 115 22)",
		"rgb(234 179 8)",
		"rgb(168 85 247)",
		"rgb(99 102 241)",
		"rgb(14 165 233)",
	}
	cumulative := 0
	for i := range out {
		out[i].Stroke = colors[i%len(colors)]
		out[i].DashOffset = 25 - cumulative
		cumulative += out[i].Percent
	}

	return out, nil
}

func (h *RelatoriosHandler) queryMacroGroupTotalsCaixa(ctx context.Context, monthStart, monthEnd int64, modo string) (int64, int64, error) {
	macroTotals, _, err := h.queryCaixaExpenseMacroAnalysis(ctx, monthStart, monthEnd, modo)
	if err != nil {
		return 0, 0, err
	}

	var essential, lifestyle int64
	for group, total := range macroTotals {
		if canonicalMacroGroup(group) == "Essencial" {
			essential += total
		} else {
			lifestyle += total
		}
	}
	return essential, lifestyle, nil
}

func (h *RelatoriosHandler) queryExpenseCategoriesCaixa(ctx context.Context, monthStart, monthEnd int64, modo string) ([]CategoriaBar, error) {
	catRows, err := h.queryCaixaExpenseCategoryRows(ctx, monthStart, monthEnd, modo)
	if err != nil {
		return nil, err
	}
	return buildCategoriaBreakdown(catRows.rows, catRows.total), nil
}

type caixaExpenseCategoryRows struct {
	rows  []categoriaBreakdownRow
	total int64
}

type caixaExpenseAnalysisRow struct {
	CategoryID string
	MacroGroup string
	Amount     int64
}

func (h *RelatoriosHandler) queryCaixaExpenseCategoryRows(ctx context.Context, monthStart, monthEnd int64, modo string) (caixaExpenseCategoryRows, error) {
	analysisRows, err := h.queryCaixaExpenseAnalysisRows(ctx, monthStart, monthEnd, modo)
	if err != nil {
		return caixaExpenseCategoryRows{}, err
	}

	totals := make(map[string]int64)
	var totalDespesas int64
	for _, row := range analysisRows {
		if row.Amount <= 0 {
			continue
		}
		totals[row.CategoryID] += row.Amount
		totalDespesas += row.Amount
	}
	rawBars, err := h.categoriaBreakdownRowsForTotals(ctx, totals)
	if err != nil {
		return caixaExpenseCategoryRows{}, err
	}
	return caixaExpenseCategoryRows{rows: rawBars, total: totalDespesas}, nil
}

func (h *RelatoriosHandler) categoriaBreakdownRowsForTotals(ctx context.Context, totals map[string]int64) ([]categoriaBreakdownRow, error) {
	if len(totals) == 0 {
		return nil, nil
	}

	rawBars := make([]categoriaBreakdownRow, 0, len(totals))
	ids := make([]string, 0, len(totals))
	for id := range totals {
		if id == "" {
			rawBars = append(rawBars, categoriaBreakdownRow{
				ID:    "",
				Name:  "Sem categoria",
				Icon:  "tag",
				Color: "#6b7280",
				Total: totals[id],
			})
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		args := make([]any, 0, len(ids)+1)
		args = append(args, h.WorkspaceID)
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		rows, err := h.DB.QueryContext(ctx, `
			SELECT
				COALESCE(c.id, ''),
				COALESCE(c.name, 'Sem categoria'),
				COALESCE(c.icon, 'tag'),
				COALESCE(c.color, '#6b7280'),
				COALESCE(c.parent_id, ''),
				COALESCE(p.name, ''),
				COALESCE(p.icon, ''),
				COALESCE(p.color, '')
			FROM categories c
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE c.workspace_id = ? AND c.id IN (`+strings.Join(placeholders, ",")+`)
		`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		for rows.Next() {
			var catID, name, icon, color, parentID, parentName, parentIcon, parentColor string
			if err := rows.Scan(&catID, &name, &icon, &color, &parentID, &parentName, &parentIcon, &parentColor); err != nil {
				return nil, err
			}
			total := totals[catID]
			if total <= 0 {
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
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	sort.SliceStable(rawBars, func(i, j int) bool {
		if rawBars[i].Total != rawBars[j].Total {
			return rawBars[i].Total > rawBars[j].Total
		}
		return rawBars[i].Name < rawBars[j].Name
	})
	return rawBars, nil
}

func (h *RelatoriosHandler) queryCaixaExpenseMacroAnalysis(ctx context.Context, monthStart, monthEnd int64, modo string) (map[string]int64, int64, error) {
	analysisRows, err := h.queryCaixaExpenseAnalysisRows(ctx, monthStart, monthEnd, modo)
	if err != nil {
		return nil, 0, err
	}

	out := make(map[string]int64)
	var total int64
	for _, row := range analysisRows {
		if row.Amount <= 0 {
			continue
		}
		macroGroup := canonicalMacroGroup(row.MacroGroup)
		out[macroGroup] += row.Amount
		total += row.Amount
	}
	return out, total, nil
}

func (h *RelatoriosHandler) queryCaixaExpenseAnalysisRows(ctx context.Context, monthStart, monthEnd int64, modo string) ([]caixaExpenseAnalysisRow, error) {
	out := make([]caixaExpenseAnalysisRow, 0)

	baseSQL := `
		SELECT
			COALESCE(t.category_id, '') AS category_id,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			t.amount
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
		  AND t.status = 'paid'
		  AND a.type != 'CREDIT_CARD'
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
		  AND COALESCE(t.description, '') NOT LIKE ?
		  AND NOT EXISTS (
			SELECT 1 FROM invoice_payments ip
			WHERE ip.workspace_id = t.workspace_id
			  AND ip.transaction_id = t.id
			  AND ip.reversed_at IS NULL
		  )
	`
	args := []any{h.WorkspaceID, monthStart, monthEnd, invoicePaymentDescriptionPrefix + "%"}
	if normalizeCaixaModo(modo) == "previsao" {
		baseSQL += `
			UNION ALL
			SELECT
				COALESCE(t.category_id, '') AS category_id,
				COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
				t.amount
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
			  AND t.status = 'pending'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
			  AND COALESCE(t.description, '') NOT LIKE ?
			  AND ` + excludeInvoicePaymentCompetenceClause("t") + `
		`
		args = append(args, h.WorkspaceID, monthStart, monthEnd, invoicePaymentDescriptionPrefix+"%")
	}
	rows, err := h.DB.QueryContext(ctx, baseSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row caixaExpenseAnalysisRow
		if err := rows.Scan(&row.CategoryID, &row.MacroGroup, &row.Amount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	paidAllocations, err := h.queryInvoiceItemAllocations(ctx, monthStart, monthEnd, "paid")
	if err != nil {
		return nil, err
	}
	out = append(out, paidAllocations...)
	if normalizeCaixaModo(modo) == "previsao" {
		pendingAllocations, err := h.queryInvoiceItemAllocations(ctx, monthStart, monthEnd, "pending")
		if err != nil {
			return nil, err
		}
		out = append(out, pendingAllocations...)
	}

	return out, nil
}

type invoiceAllocationItem struct {
	TransactionID string
	CategoryID    string
	MacroGroup    string
	Amount        int64
}

type invoiceAllocationShare struct {
	CategoryID string
	MacroGroup string
	Amount     int64
	Remainder  int64
	Source     int64
	ID         string
}

func (h *RelatoriosHandler) queryInvoiceItemAllocations(ctx context.Context, start, end int64, mode string) ([]caixaExpenseAnalysisRow, error) {
	invoiceTargets, err := h.queryInvoiceAllocationTargets(ctx, start, end, mode)
	if err != nil {
		return nil, err
	}
	if len(invoiceTargets) == 0 {
		return nil, nil
	}

	out := make([]caixaExpenseAnalysisRow, 0)
	for invoiceID, targetAmount := range invoiceTargets {
		if targetAmount <= 0 {
			continue
		}
		items, total, err := h.queryInvoiceAllocationItems(ctx, invoiceID)
		if err != nil {
			return nil, err
		}
		if total <= 0 || len(items) == 0 {
			continue
		}
		shares := allocateInvoiceItemShares(items, total, targetAmount)
		for _, share := range shares {
			if share.Amount <= 0 {
				continue
			}
			out = append(out, caixaExpenseAnalysisRow{
				CategoryID: share.CategoryID,
				MacroGroup: share.MacroGroup,
				Amount:     share.Amount,
			})
		}
	}
	return out, nil
}

func (h *RelatoriosHandler) queryInvoiceAllocationTargets(ctx context.Context, start, end int64, mode string) (map[string]int64, error) {
	targets := make(map[string]int64)
	var rows *sql.Rows
	var err error
	if mode == "paid" {
		rows, err = h.DB.QueryContext(ctx, `
			SELECT ip.invoice_id, COALESCE(SUM(ip.amount_cents), 0)
			FROM invoice_payments ip
			WHERE ip.workspace_id = ?
			  AND ip.reversed_at IS NULL
			  AND ip.paid_at >= ? AND ip.paid_at <= ?
			GROUP BY ip.invoice_id
		`, h.WorkspaceID, start, end)
	} else {
		rows, err = h.DB.QueryContext(ctx, `
			SELECT
				i.id,
				COALESCE((
					SELECT SUM(CASE WHEN tx.type = 'EXPENSE' THEN tx.amount ELSE 0 END)
					FROM transactions tx
					WHERE tx.workspace_id = a.workspace_id AND tx.invoice_id = i.id
				), 0) -
				COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id
						AND ip.invoice_id = i.id
						AND ip.reversed_at IS NULL
				), 0) AS pending_amount
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
			  AND i.status IN ('OPEN', 'CLOSED')
			  AND i.due_date >= ? AND i.due_date <= ?
		`, h.WorkspaceID, start, end)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var invoiceID string
		var amount int64
		if err := rows.Scan(&invoiceID, &amount); err != nil {
			return nil, err
		}
		if amount > 0 {
			targets[invoiceID] += amount
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return targets, nil
}

func (h *RelatoriosHandler) queryInvoiceAllocationItems(ctx context.Context, invoiceID string) ([]invoiceAllocationItem, int64, error) {
	rows, err := h.DB.QueryContext(ctx, `
		SELECT
			t.id,
			COALESCE(t.category_id, '') AS category_id,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			t.amount
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
		  AND t.invoice_id = ?
		  AND t.type = 'EXPENSE'
		ORDER BY t.amount DESC, t.id ASC
	`, h.WorkspaceID, invoiceID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]invoiceAllocationItem, 0)
	var total int64
	for rows.Next() {
		var item invoiceAllocationItem
		if err := rows.Scan(&item.TransactionID, &item.CategoryID, &item.MacroGroup, &item.Amount); err != nil {
			return nil, 0, err
		}
		if item.Amount <= 0 {
			continue
		}
		items = append(items, item)
		total += item.Amount
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func allocateInvoiceItemShares(items []invoiceAllocationItem, total, target int64) []invoiceAllocationShare {
	if total <= 0 || target <= 0 || len(items) == 0 {
		return nil
	}
	shares := make([]invoiceAllocationShare, 0, len(items))
	var allocated int64
	for _, item := range items {
		numerator := item.Amount * target
		amount := numerator / total
		remainder := numerator % total
		shares = append(shares, invoiceAllocationShare{
			CategoryID: item.CategoryID,
			MacroGroup: item.MacroGroup,
			Amount:     amount,
			Remainder:  remainder,
			Source:     item.Amount,
			ID:         item.TransactionID,
		})
		allocated += amount
	}
	residual := target - allocated
	if residual > 0 {
		sort.SliceStable(shares, func(i, j int) bool {
			if shares[i].Remainder != shares[j].Remainder {
				return shares[i].Remainder > shares[j].Remainder
			}
			if shares[i].Source != shares[j].Source {
				return shares[i].Source > shares[j].Source
			}
			return shares[i].ID < shares[j].ID
		})
		for i := int64(0); i < residual; i++ {
			shares[int(i)%len(shares)].Amount++
		}
		sort.SliceStable(shares, func(i, j int) bool {
			return shares[i].ID < shares[j].ID
		})
	}
	return shares
}

func caixaExpenseAnalysisSourceSQL(modo string) string {
	if normalizeCaixaModo(modo) == "previsao" {
		return `
			SELECT
				t.category_id AS category_id,
				COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
				t.amount AS amount
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
			  AND t.status = 'paid'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
			  AND COALESCE(t.description, '') NOT LIKE ?
			  AND NOT EXISTS (
				SELECT 1 FROM invoice_payments ip
				WHERE ip.workspace_id = t.workspace_id
				  AND ip.transaction_id = t.id
				  AND ip.reversed_at IS NULL
			  )

				UNION ALL

			SELECT
				t.category_id AS category_id,
				COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
				t.amount AS amount
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
			  AND t.status = 'pending'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
			  AND COALESCE(t.description, '') NOT LIKE ?
			  AND ` + excludeInvoicePaymentCompetenceClause("t") + `

				UNION ALL

				SELECT
					t.category_id AS category_id,
					COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
					CAST((t.amount * invoice_totals.paid_amount) / invoice_totals.total_amount AS INTEGER) AS amount
				FROM (
					SELECT
						i.id,
						a.workspace_id,
						COALESCE((
							SELECT SUM(CASE WHEN tx.type = 'EXPENSE' THEN tx.amount ELSE 0 END)
							FROM transactions tx
							WHERE tx.workspace_id = a.workspace_id AND tx.invoice_id = i.id
						), 0) AS total_amount,
						COALESCE(SUM(ip.amount_cents), 0) AS paid_amount
					FROM invoice_payments ip
					JOIN invoices i ON i.id = ip.invoice_id
					JOIN accounts a ON a.id = i.account_id
					WHERE ip.workspace_id = ?
					  AND ip.reversed_at IS NULL
					  AND ip.paid_at >= ? AND ip.paid_at <= ?
					GROUP BY i.id, a.workspace_id
				) invoice_totals
				JOIN transactions t ON t.invoice_id = invoice_totals.id AND t.workspace_id = invoice_totals.workspace_id
				LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
				LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
				WHERE t.type = 'EXPENSE'
				  AND invoice_totals.total_amount > 0
				  AND invoice_totals.paid_amount > 0

				UNION ALL

				SELECT
					t.category_id AS category_id,
					COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
					CAST((t.amount * invoice_totals.pending_amount) / invoice_totals.total_amount AS INTEGER) AS amount
				FROM (
					SELECT
						i.id,
						a.workspace_id,
						COALESCE((
							SELECT SUM(CASE WHEN tx.type = 'EXPENSE' THEN tx.amount ELSE 0 END)
							FROM transactions tx
							WHERE tx.workspace_id = a.workspace_id AND tx.invoice_id = i.id
						), 0) AS total_amount,
						COALESCE((
							SELECT SUM(ip.amount_cents)
							FROM invoice_payments ip
							WHERE ip.workspace_id = a.workspace_id
								AND ip.invoice_id = i.id
								AND ip.reversed_at IS NULL
						), 0) AS paid_amount,
						COALESCE((
							SELECT SUM(CASE WHEN tx.type = 'EXPENSE' THEN tx.amount ELSE 0 END)
							FROM transactions tx
							WHERE tx.workspace_id = a.workspace_id AND tx.invoice_id = i.id
						), 0) -
						COALESCE((
							SELECT SUM(ip.amount_cents)
							FROM invoice_payments ip
							WHERE ip.workspace_id = a.workspace_id
								AND ip.invoice_id = i.id
								AND ip.reversed_at IS NULL
						), 0) AS pending_amount
					FROM invoices i
					JOIN accounts a ON a.id = i.account_id
					WHERE a.workspace_id = ?
					  AND i.status IN ('OPEN', 'CLOSED')
					  AND i.due_date >= ? AND i.due_date <= ?
				) invoice_totals
				JOIN transactions t ON t.invoice_id = invoice_totals.id AND t.workspace_id = invoice_totals.workspace_id
				LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
				LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
				WHERE t.type = 'EXPENSE'
				  AND invoice_totals.total_amount > 0
				  AND invoice_totals.pending_amount > 0
		`
	}
	return `
		SELECT
			t.category_id AS category_id,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			t.amount AS amount
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
		  AND t.status = 'paid'
		  AND a.type != 'CREDIT_CARD'
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
		  AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
		  AND COALESCE(t.description, '') NOT LIKE ?
		  AND NOT EXISTS (
			SELECT 1 FROM invoice_payments ip
			WHERE ip.workspace_id = t.workspace_id
			  AND ip.transaction_id = t.id
			  AND ip.reversed_at IS NULL
		  )

		UNION ALL

		SELECT
			t.category_id AS category_id,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			CAST((t.amount * invoice_totals.paid_amount) / invoice_totals.total_amount AS INTEGER) AS amount
		FROM (
			SELECT
				i.id,
				a.workspace_id,
				COALESCE((
					SELECT SUM(CASE WHEN tx.type = 'EXPENSE' THEN tx.amount ELSE 0 END)
					FROM transactions tx
					WHERE tx.workspace_id = a.workspace_id AND tx.invoice_id = i.id
				), 0) AS total_amount,
				COALESCE(SUM(ip.amount_cents), 0) AS paid_amount
			FROM invoice_payments ip
			JOIN invoices i ON i.id = ip.invoice_id
			JOIN accounts a ON a.id = i.account_id
			WHERE ip.workspace_id = ?
			  AND ip.reversed_at IS NULL
			  AND ip.paid_at >= ? AND ip.paid_at <= ?
			GROUP BY i.id, a.workspace_id
		) invoice_totals
		JOIN transactions t ON t.invoice_id = invoice_totals.id AND t.workspace_id = invoice_totals.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = t.workspace_id
		WHERE t.type = 'EXPENSE'
		  AND invoice_totals.total_amount > 0
		  AND invoice_totals.paid_amount > 0
	`
}

func caixaExpenseAnalysisArgs(workspaceID string, monthStart, monthEnd int64, modo string, extra ...any) []any {
	args := []any{workspaceID, monthStart, monthEnd, invoicePaymentDescriptionPrefix + "%"}
	if normalizeCaixaModo(modo) == "previsao" {
		args = append(args,
			workspaceID, monthStart, monthEnd, invoicePaymentDescriptionPrefix+"%",
			workspaceID, monthStart, monthEnd,
			workspaceID, monthStart, monthEnd,
		)
	} else {
		args = append(args, workspaceID, monthStart, monthEnd)
	}
	args = append(args, extra...)
	return args
}

func normalizeCaixaModo(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "previsao":
		return "previsao"
	default:
		return "realizado"
	}
}

func tabViewExtraParams(tab, view, modo string) string {
	parts := "tab=" + tab
	normalizedModo := normalizeCaixaModo(modo)
	if tab == "caixa" || normalizedModo == "previsao" {
		parts += "&modo=" + normalizedModo
	}
	if view != "" && view != "movimentos" {
		parts += "&view=" + view
	}
	return parts
}

func relatoriosMonthQuery(mes, ano int, extraParams string) string {
	q := fmt.Sprintf("mes=%d&ano=%d", mes, ano)
	if extraParams != "" {
		q += "&" + extraParams
	}
	return q
}

func relatoriosURL(mes, ano int, extraParams string) string {
	return "/relatorios?" + relatoriosMonthQuery(mes, ano, extraParams)
}

func buildRelatoriosMonthOptions(ano, mesAtual int, tab, view, modo string) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	now := time.Now().UTC()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	extraParams := tabViewExtraParams(tab, view, modo)
	options := make([]MonthOption, 0, 12)
	for month := 1; month <= 12; month++ {
		m := time.Date(ano, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		query := relatoriosMonthQuery(int(m.Month()), m.Year(), extraParams)
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
