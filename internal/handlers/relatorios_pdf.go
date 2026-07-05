package handlers

import (
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

const (
	pdfPageWidth  = 595.28
	pdfPageHeight = 841.89

	drePDFMargin         = 42.0
	drePDFContentWidth   = pdfPageWidth - drePDFMargin*2
	drePDFBlockGap       = 34.0
	drePDFTableHeaderGap = 22.0
	drePDFTableRowGap    = 20.0
)

type workspaceReportIdentity struct {
	Name         string
	Type         string
	CompanyName  string
	CNPJCPF      string
	Address      string
	Phone        string
	LogoLightURL string
	LogoDarkURL  string
}

func (id workspaceReportIdentity) displayName() string {
	if strings.EqualFold(id.Type, "business") && strings.TrimSpace(id.CompanyName) != "" {
		return strings.TrimSpace(id.CompanyName)
	}
	if strings.TrimSpace(id.Name) != "" {
		return strings.TrimSpace(id.Name)
	}
	return "Workspace"
}

func (id workspaceReportIdentity) preferredLogoURL() string {
	if strings.TrimSpace(id.LogoLightURL) != "" {
		return strings.TrimSpace(id.LogoLightURL)
	}
	return strings.TrimSpace(id.LogoDarkURL)
}

type pdfCategorySummaryRow struct {
	Name         string
	Amount       int64
	Percent      int
	PercentLabel string
}

func (h *RelatoriosHandler) HandleExportarExtratoPDF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tipo := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("tipo")))
	if tipo != "extrato-movimentos" && tipo != "extrato-diario" {
		http.Error(w, "tipo de exportação inválido", http.StatusBadRequest)
		return
	}

	mes, ano, err := parseExtratoExportPeriod(r)
	if err != nil {
		http.Error(w, "periodo invalido", http.StatusBadRequest)
		return
	}

	view := "movimentos"
	if tipo == "extrato-diario" {
		view = "diario"
	}

	data, err := h.buildRelatoriosData("", "extrato", view, mes, ano)
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	identity, err := h.queryWorkspaceReportIdentity()
	if err != nil {
		http.Error(w, "erro ao carregar workspace", http.StatusInternalServerError)
		return
	}

	periodLabel := drePDFMonthLabel(mes, ano)
	filename := fmt.Sprintf("extrato-movimentos-%04d-%02d.pdf", ano, mes)
	if tipo == "extrato-diario" {
		filename = fmt.Sprintf("extrato-diario-%04d-%02d.pdf", ano, mes)
	}

	var pdfBytes []byte
	if tipo == "extrato-movimentos" {
		pdfBytes, err = buildExtratoMovimentosPDFReport(data, identity, h.WorkspaceID, periodLabel, mes, ano, time.Now())
	} else {
		pdfBytes, err = buildExtratoDiarioPDFReport(data, identity, h.WorkspaceID, periodLabel, mes, ano, time.Now())
	}
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pdfBytes)
}

func (h *RelatoriosHandler) HandleExportarDREPDF(w http.ResponseWriter, r *http.Request) {
	mes, ano, err := parseDREPDFPeriod(r)
	if err != nil {
		http.Error(w, "periodo invalido", http.StatusBadRequest)
		return
	}

	data, err := h.buildRelatoriosData("", "competencia", "movimentos", mes, ano)
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	identity, err := h.queryWorkspaceReportIdentity()
	if err != nil {
		http.Error(w, "erro ao carregar workspace", http.StatusInternalServerError)
		return
	}

	revenueCategories, expenseCategories, err := h.queryDREPDFCategorySummaries(mes, ano)
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	pdfBytes, err := buildDREPDFReport(data, identity, h.WorkspaceID, mes, ano, revenueCategories, expenseCategories, time.Now())
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	competencia := fmt.Sprintf("%04d-%02d", ano, mes)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="dre-%s.pdf"`, competencia))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pdfBytes)
}

func (h *RelatoriosHandler) HandleExportarCaixaPDF(w http.ResponseWriter, r *http.Request) {
	mes, ano, err := parseDREPDFPeriod(r)
	if err != nil {
		http.Error(w, "periodo invalido", http.StatusBadRequest)
		return
	}
	modo := normalizeCaixaModo(r.URL.Query().Get("modo"))

	data, err := h.buildRelatoriosDataWithModo("", "caixa", "movimentos", modo, mes, ano)
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	identity, err := h.queryWorkspaceReportIdentity()
	if err != nil {
		http.Error(w, "erro ao carregar workspace", http.StatusInternalServerError)
		return
	}

	revenueCategories, expenseCategories, err := h.queryCaixaPDFCategorySummaries(mes, ano, modo)
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	pdfBytes, err := buildCaixaPDFReport(data, identity, h.WorkspaceID, mes, ano, revenueCategories, expenseCategories, time.Now())
	if err != nil {
		http.Error(w, "erro ao gerar pdf", http.StatusInternalServerError)
		return
	}

	vencimento := fmt.Sprintf("%04d-%02d", ano, mes)
	filenamePrefix := "caixa"
	if modo == "previsao" {
		filenamePrefix = "caixa-previsao"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.pdf"`, filenamePrefix, vencimento))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(pdfBytes)
}

func parseDREPDFPeriod(r *http.Request) (int, int, error) {
	now := time.Now().UTC()
	mes := int(now.Month())
	ano := now.Year()

	if raw := strings.TrimSpace(r.URL.Query().Get("month")); raw != "" {
		parts := strings.Split(raw, "-")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("invalid month")
		}
		parsedYear, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, err
		}
		parsedMonth, err := strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, err
		}
		if parsedYear < 2020 || parsedYear > now.Year()+10 || parsedMonth < 1 || parsedMonth > 12 {
			return 0, 0, fmt.Errorf("invalid month")
		}
		return parsedMonth, parsedYear, nil
	}

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

func (h *RelatoriosHandler) queryWorkspaceReportIdentity() (workspaceReportIdentity, error) {
	var row workspaceReportIdentity
	err := h.DB.QueryRow(`
		SELECT
			COALESCE(name, ''),
			COALESCE(type, ''),
			COALESCE(company_name, ''),
			COALESCE(cnpj_cpf, ''),
			COALESCE(address, ''),
			COALESCE(phone, ''),
			COALESCE(logo_light_url, ''),
			COALESCE(logo_dark_url, '')
		FROM workspaces
		WHERE id = ?
	`, h.WorkspaceID).Scan(
		&row.Name,
		&row.Type,
		&row.CompanyName,
		&row.CNPJCPF,
		&row.Address,
		&row.Phone,
		&row.LogoLightURL,
		&row.LogoDarkURL,
	)
	if err == sql.ErrNoRows {
		return workspaceReportIdentity{Name: "Workspace"}, nil
	}
	return row, err
}

func (h *RelatoriosHandler) queryDREPDFCategorySummaries(mes, ano int) ([]pdfCategorySummaryRow, []pdfCategorySummaryRow, error) {
	start := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(ano, time.Month(mes)+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second).Unix()
	revenues, err := queryDREPDFCategorySummary(h.DB, h.WorkspaceID, "INCOME", start, end)
	if err != nil {
		return nil, nil, err
	}
	expenses, err := queryDREPDFCategorySummary(h.DB, h.WorkspaceID, "EXPENSE", start, end)
	if err != nil {
		return nil, nil, err
	}
	return revenues, expenses, nil
}

func queryDREPDFCategorySummary(db *sql.DB, workspaceID, txType string, start, end int64) ([]pdfCategorySummaryRow, error) {
	rows, err := db.Query(`
		SELECT
			COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), 'Sem categoria') AS category_name,
			COALESCE(SUM(t.amount), 0) AS total
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
		  AND t.type = ?
		  AND t.date >= ? AND t.date <= ?
		  AND `+excludeInvoicePaymentCompetenceClause("t")+`
		GROUP BY category_name
		ORDER BY total DESC, category_name ASC
		LIMIT 5
	`, workspaceID, txType, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawRow struct {
		Name   string
		Amount int64
	}
	raw := make([]rawRow, 0, 5)
	var total int64
	for rows.Next() {
		var item rawRow
		if err := rows.Scan(&item.Name, &item.Amount); err != nil {
			return nil, err
		}
		if item.Amount <= 0 {
			continue
		}
		total += item.Amount
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if total <= 0 || len(raw) == 0 {
		return nil, nil
	}

	out := make([]pdfCategorySummaryRow, 0, len(raw))
	cumulative := 0
	for idx, item := range raw {
		percent := int((item.Amount * 100) / total)
		if idx == len(raw)-1 {
			percent = 100 - cumulative
		}
		if percent < 0 {
			percent = 0
		}
		out = append(out, pdfCategorySummaryRow{
			Name:         item.Name,
			Amount:       item.Amount,
			Percent:      percent,
			PercentLabel: percentLabel(item.Amount, total),
		})
		cumulative += percent
	}
	return out, nil
}

func (h *RelatoriosHandler) queryCaixaPDFCategorySummaries(mes, ano int, modo string) ([]pdfCategorySummaryRow, []pdfCategorySummaryRow, error) {
	start := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(ano, time.Month(mes)+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second).Unix()
	revenues, err := queryCaixaPDFCategorySummary(h.DB, h.WorkspaceID, "INCOME", start, end, modo)
	if err != nil {
		return nil, nil, err
	}
	expenseRows, err := h.queryCaixaExpenseCategoryRows(context.Background(), start, end, modo)
	if err != nil {
		return nil, nil, err
	}
	expenses := pdfCategorySummaryRowsFromCategoriaRows(expenseRows.rows, expenseRows.total, 5)
	return revenues, expenses, nil
}

func pdfCategorySummaryRowsFromCategoriaRows(rows []categoriaBreakdownRow, total int64, limit int) []pdfCategorySummaryRow {
	if total <= 0 || len(rows) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(rows) {
		limit = len(rows)
	}
	out := make([]pdfCategorySummaryRow, 0, limit)
	cumulative := 0
	for idx, row := range rows[:limit] {
		if row.Total <= 0 {
			continue
		}
		percent := int((row.Total * 100) / total)
		if idx == limit-1 {
			percent = 100 - cumulative
		}
		if percent < 0 {
			percent = 0
		}
		out = append(out, pdfCategorySummaryRow{
			Name:         row.Name,
			Amount:       row.Total,
			Percent:      percent,
			PercentLabel: percentLabel(row.Total, total),
		})
		cumulative += percent
	}
	return out
}

func queryCaixaPDFCategorySummary(db *sql.DB, workspaceID, txType string, start, end int64, modo string) ([]pdfCategorySummaryRow, error) {
	if normalizeCaixaModo(modo) == "previsao" {
		return queryCaixaPrevisaoPDFCategorySummary(db, workspaceID, txType, start, end)
	}
	rows, err := db.Query(`
		SELECT category_name, COALESCE(SUM(total), 0)
		FROM (
			SELECT
				COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), CASE WHEN ip.id IS NOT NULL THEN 'Fatura de cartão' ELSE 'Sem categoria' END) AS category_name,
				t.amount AS total
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			LEFT JOIN invoice_payments ip ON ip.transaction_id = t.id AND ip.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
			  AND t.type = ?
			  AND t.status = 'paid'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
		)
		GROUP BY category_name
		ORDER BY total DESC, category_name ASC
		LIMIT 5
	`, workspaceID, txType, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawRow struct {
		Name   string
		Amount int64
	}
	raw := make([]rawRow, 0, 5)
	var total int64
	for rows.Next() {
		var item rawRow
		if err := rows.Scan(&item.Name, &item.Amount); err != nil {
			return nil, err
		}
		if item.Amount <= 0 {
			continue
		}
		total += item.Amount
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if total <= 0 || len(raw) == 0 {
		return nil, nil
	}

	out := make([]pdfCategorySummaryRow, 0, len(raw))
	cumulative := 0
	for idx, item := range raw {
		percent := int((item.Amount * 100) / total)
		if idx == len(raw)-1 {
			percent = 100 - cumulative
		}
		if percent < 0 {
			percent = 0
		}
		out = append(out, pdfCategorySummaryRow{
			Name:         item.Name,
			Amount:       item.Amount,
			Percent:      percent,
			PercentLabel: percentLabel(item.Amount, total),
		})
		cumulative += percent
	}
	return out, nil
}

func queryCaixaPrevisaoPDFCategorySummary(db *sql.DB, workspaceID, txType string, start, end int64) ([]pdfCategorySummaryRow, error) {
	rows, err := db.Query(`
		SELECT category_name, COALESCE(SUM(total), 0)
		FROM (
			SELECT
				COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), 'Sem categoria') AS category_name,
				t.amount AS total
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ?
			  AND t.type = ?
			  AND t.status = 'paid'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`

			UNION ALL

			SELECT
				COALESCE(NULLIF(TRIM(COALESCE(c.name, p.name, '')), ''), 'Sem categoria') AS category_name,
				t.amount AS total
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ?
			  AND t.type = ?
			  AND t.status = 'pending'
			  AND a.type != 'CREDIT_CARD'
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			  AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
		)
		GROUP BY category_name
		ORDER BY total DESC, category_name ASC
		LIMIT 5
	`, workspaceID, txType, start, end, workspaceID, txType, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawRow struct {
		Name   string
		Amount int64
	}
	raw := make([]rawRow, 0, 5)
	var total int64
	for rows.Next() {
		var item rawRow
		if err := rows.Scan(&item.Name, &item.Amount); err != nil {
			return nil, err
		}
		if item.Amount <= 0 {
			continue
		}
		total += item.Amount
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if total <= 0 || len(raw) == 0 {
		return nil, nil
	}

	out := make([]pdfCategorySummaryRow, 0, len(raw))
	cumulative := 0
	for idx, item := range raw {
		percent := int((item.Amount * 100) / total)
		if idx == len(raw)-1 {
			percent = 100 - cumulative
		}
		if percent < 0 {
			percent = 0
		}
		out = append(out, pdfCategorySummaryRow{
			Name:         item.Name,
			Amount:       item.Amount,
			Percent:      percent,
			PercentLabel: percentLabel(item.Amount, total),
		})
		cumulative += percent
	}
	return out, nil
}

func buildDREPDFReport(data RelatoriosData, identity workspaceReportIdentity, workspaceID string, mes, ano int, revenueCategories, expenseCategories []pdfCategorySummaryRow, generatedAt time.Time) ([]byte, error) {
	competencia := fmt.Sprintf("%04d-%02d", ano, mes)
	periodLabel := drePDFMonthLabel(mes, ano)
	selected := dreRowForCompetencia(data.DRE, competencia)
	logo, _ := loadWorkspacePDFLogo(workspaceID, identity.preferredLogoURL())

	doc := newDREPDFPage()
	pages := []*pdfCanvas{doc}

	headerY := pdfPageHeight - 62
	if logo != nil {
		ratio := math.Min(54/float64(logo.Width), 30/float64(logo.Height))
		doc.image("Im1", drePDFMargin, headerY-26, float64(logo.Width)*ratio, float64(logo.Height)*ratio)
	} else {
		doc.setColor(pdfColor{103, 68, 241})
		doc.rect(drePDFMargin, headerY-28, 30, 30, true)
		doc.text(drePDFMargin+7, headerY-10, 11.2, pdfColor{255, 255, 255}, initials(identity.displayName()))
	}

	doc.text(84, headerY-4, 12.2, pdfColor{24, 24, 27}, identity.displayName())
	doc.text(84, headerY-20, 7.5, pdfColor{113, 113, 122}, "Relatório gerencial por competência")
	doc.text(402, headerY-4, 14.2, pdfColor{24, 24, 27}, "DRE Simplificada")
	doc.text(402, headerY-20, 7.8, pdfColor{113, 113, 122}, "Período: "+periodLabel+" ("+competencia+")")

	infoY := headerY - 48
	for _, line := range drePDFIdentityLines(identity) {
		doc.text(drePDFMargin, infoY, 7.8, pdfColor{82, 82, 91}, line)
		infoY -= 11
	}
	doc.text(402, headerY-46, 7.5, pdfColor{82, 82, 91}, "Regime de competência")
	doc.text(402, headerY-56, 7.5, pdfColor{82, 82, 91}, "Inclui lançamentos pagos e pendentes")
	doc.text(402, headerY-66, 7.5, pdfColor{82, 82, 91}, "Exclui pagamentos de fatura")

	cardY := pdfPageHeight - 206
	cardW := 156.0
	doc.summaryCard(drePDFMargin, cardY, cardW, "Receitas", formatCurrencyLabel(data.TotalReceitas.rawCents()), pdfColor{34, 197, 94})
	doc.summaryCard(drePDFMargin+cardW+16, cardY, cardW, "Despesas", formatCurrencyLabel(data.TotalDespesas.rawCents()), pdfColor{239, 68, 68})
	resultColor := pdfColor{103, 68, 241}
	if data.SaldoNegativo {
		resultColor = pdfColor{239, 68, 68}
	}
	doc.summaryCard(drePDFMargin+(cardW+16)*2, cardY, cardW, "Resultado", formatCurrencyLabel(data.SaldoLiquido.rawCents()), resultColor)

	tableTop := cardY - drePDFBlockGap
	doc.dreSectionTitle(drePDFMargin, tableTop, "Tabela DRE por competência")
	tableY := tableTop - drePDFTableHeaderGap
	dreDetailCols := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Grupo / macro grupo", Width: 260, Align: "left"},
		{Label: "Tipo", Width: 82, Align: "left"},
		{Label: "Valor", Width: 150, Align: "right"},
	}, drePDFContentWidth)
	doc.dreTableHeader(drePDFMargin, tableY, dreDetailCols)
	tableY -= drePDFTableRowGap

	if selected == nil || len(selected.MacroGroups) == 0 {
		doc.text(52, tableY, 8.4, pdfColor{113, 113, 122}, "Sem dados de DRE para a competência selecionada.")
		tableY -= drePDFTableRowGap
	} else {
		for _, item := range selected.MacroGroups {
			kind := "Receita"
			valueColor := pdfColor{34, 197, 94}
			if item.RawAmount < 0 {
				kind = "Despesa"
				valueColor = pdfColor{239, 68, 68}
			}
			doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: item.MacroGroup, Width: dreDetailCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: kind, Width: dreDetailCols[1].Width, Align: "left", Color: pdfColor{82, 82, 91}},
				{Text: formatCurrencyLabel(item.RawAmount), Width: dreDetailCols[2].Width, Align: "right", Color: valueColor},
			})
			tableY -= drePDFTableRowGap
		}
		resultRowColor := pdfColor{103, 68, 241}
		if selected.RawResultado < 0 {
			resultRowColor = pdfColor{239, 68, 68}
		}
		doc.dreTableRowEmphasis(drePDFMargin, tableY, []pdfTableCell{
			{Text: "Resultado", Width: dreDetailCols[0].Width + dreDetailCols[1].Width, Align: "left", Color: pdfColor{24, 24, 27}},
			{Text: formatCurrencyLabel(selected.RawResultado), Width: dreDetailCols[2].Width, Align: "right", Color: resultRowColor},
		})
		tableY -= drePDFBlockGap
	}

	if data.IsBusiness {
		doc.dreSectionTitle(drePDFMargin, tableY, "Custos por Grupo Macro")
		tableY -= drePDFTableHeaderGap
		if len(data.BusinessCosts) == 0 {
			doc.text(52, tableY, 8.4, pdfColor{113, 113, 122}, "Sem custos operacionais no periodo selecionado.")
			tableY -= drePDFTableRowGap
		} else {
			dreCostsCols := fitPDFColumnsToWidth([]pdfTableColumn{
				{Label: "Grupo macro", Width: 300, Align: "left"},
				{Label: "Participacao", Width: 92, Align: "right"},
				{Label: "Valor", Width: 100, Align: "right"},
			}, drePDFContentWidth)
			doc.dreTableHeader(drePDFMargin, tableY, dreCostsCols)
			tableY -= drePDFTableRowGap
			for _, item := range data.BusinessCosts {
				doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
					{Text: item.MacroGroup, Width: dreCostsCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
					{Text: businessCostPercentText(item), Width: dreCostsCols[1].Width, Align: "right", Color: pdfColor{82, 82, 91}},
					{Text: "-" + formatCurrencyLabel(item.RawAmount), Width: dreCostsCols[2].Width, Align: "right", Color: pdfColor{239, 68, 68}},
				})
				tableY -= drePDFTableRowGap
			}
		}
	}

	if len(revenueCategories) > 0 || len(expenseCategories) > 0 {
		doc = newDREPDFPage()
		pages = append(pages, doc)
		doc.text(drePDFMargin, headerY-4, 14.2, pdfColor{24, 24, 27}, "DRE Simplificada")
		doc.text(drePDFMargin, headerY-20, 7.8, pdfColor{113, 113, 122}, "Detalhamento por categoria - "+periodLabel+" ("+competencia+")")
		tableY = headerY - 60
	}

	var dreCatCols []pdfTableColumn
	if len(revenueCategories) > 0 {
		doc.dreSectionTitle(drePDFMargin, tableY, "Receitas por categoria")
		tableY -= drePDFTableHeaderGap
		dreCatCols = fitPDFColumnsToWidth([]pdfTableColumn{
			{Label: "Categoria", Width: 300, Align: "left"},
			{Label: "Participacao", Width: 92, Align: "right"},
			{Label: "Valor", Width: 100, Align: "right"},
		}, drePDFContentWidth)
		doc.dreTableHeader(drePDFMargin, tableY, dreCatCols)
		tableY -= drePDFTableRowGap
		for _, item := range revenueCategories {
			doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: item.Name, Width: dreCatCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: pdfCategoryPercentText(item), Width: dreCatCols[1].Width, Align: "right", Color: pdfColor{82, 82, 91}},
				{Text: formatCurrencyLabel(item.Amount), Width: dreCatCols[2].Width, Align: "right", Color: pdfColor{34, 197, 94}},
			})
			tableY -= drePDFTableRowGap
		}
		tableY -= 18
	}

	if len(expenseCategories) > 0 {
		doc.dreSectionTitle(drePDFMargin, tableY, "Gastos por categoria")
		tableY -= drePDFTableHeaderGap
		if dreCatCols == nil {
			dreCatCols = fitPDFColumnsToWidth([]pdfTableColumn{
				{Label: "Categoria", Width: 300, Align: "left"},
				{Label: "Participacao", Width: 92, Align: "right"},
				{Label: "Valor", Width: 100, Align: "right"},
			}, drePDFContentWidth)
		}
		doc.dreTableHeader(drePDFMargin, tableY, dreCatCols)
		tableY -= drePDFTableRowGap
		for _, item := range expenseCategories {
			doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: item.Name, Width: dreCatCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: pdfCategoryPercentText(item), Width: dreCatCols[1].Width, Align: "right", Color: pdfColor{82, 82, 91}},
				{Text: "-" + formatCurrencyLabel(item.Amount), Width: dreCatCols[2].Width, Align: "right", Color: pdfColor{239, 68, 68}},
			})
			tableY -= drePDFTableRowGap
		}
	}

	for i, page := range pages {
		drawCompactDREPDFFooter(page, identity, generatedAt, i+1, len(pages))
	}

	return buildPDFDocument(pages, logo), nil
}

func caixaRowForVencimento(rows []CaixaRow, vencimento string) *CaixaRow {
	for i := range rows {
		if rows[i].Vencimento == vencimento {
			return &rows[i]
		}
	}
	return nil
}

func buildCaixaSummaryColumns(contentWidth float64, isPrevisao bool) []pdfTableColumn {
	if isPrevisao {
		return []pdfTableColumn{
			{Label: "Mes", Width: math.Floor(contentWidth*0.25), Align: "left"},
			{Label: "Resultado", Width: math.Floor(contentWidth*0.35), Align: "right"},
			{Label: "Saldo Projetado", Width: contentWidth - math.Floor(contentWidth*0.25) - math.Floor(contentWidth*0.35), Align: "right"},
		}
	}
	return []pdfTableColumn{
		{Label: "Mes", Width: math.Floor(contentWidth*0.35), Align: "left"},
		{Label: "Resultado", Width: contentWidth - math.Floor(contentWidth*0.35), Align: "right"},
	}
}

func buildCaixaPDFReport(data RelatoriosData, identity workspaceReportIdentity, workspaceID string, mes, ano int, revenueCategories, expenseCategories []pdfCategorySummaryRow, generatedAt time.Time) ([]byte, error) {
	vencimento := fmt.Sprintf("%04d-%02d", ano, mes)
	periodLabel := drePDFMonthLabel(mes, ano)
	selected := caixaRowForVencimento(data.Caixa, vencimento)
	logo, _ := loadWorkspacePDFLogo(workspaceID, identity.preferredLogoURL())
	title := "Demonstrativo de Caixa Realizado"
	subtitle := "Relatório gerencial de caixa realizado"
	baseLine := "Regime de caixa por data de pagamento"
	scopeLine := "Apenas lançamentos pagos"
	cardLine := "Compras no cartão não entram; pagamentos de fatura sim"
	cardIncomeLabel := "Entradas recebidas"
	cardExpenseLabel := "Saídas pagas"
	cardResultLabel := "Resultado realizado"
	tableTitle := "Demonstrativo de Caixa Realizado"
	titleMode := "Realizado"
	if data.IsCaixaPrevisao {
		title = "Demonstrativo de Caixa Previsto"
		titleMode = "Previsto"
		subtitle = "Relatório gerencial de caixa previsto"
		baseLine = "Previsão por vencimento - próximos 12 meses"
		scopeLine = "Inclui realizados e pendentes"
		cardLine = "Compras no cartão não duplicam a fatura"
		cardIncomeLabel = "Entradas previstas"
		cardExpenseLabel = "Saídas previstas"
		cardResultLabel = "Resultado previsto"
		tableTitle = "Demonstrativo de Caixa Previsto"
	}

	doc := newDREPDFPage()
	pages := []*pdfCanvas{doc}

	headerY := pdfPageHeight - 62
	if logo != nil {
		ratio := math.Min(54/float64(logo.Width), 30/float64(logo.Height))
		doc.image("Im1", drePDFMargin, headerY-26, float64(logo.Width)*ratio, float64(logo.Height)*ratio)
	} else {
		doc.setColor(pdfColor{103, 68, 241})
		doc.rect(drePDFMargin, headerY-28, 30, 30, true)
		doc.text(drePDFMargin+7, headerY-10, 11.2, pdfColor{255, 255, 255}, initials(identity.displayName()))
	}

	doc.text(84, headerY-4, 12.2, pdfColor{24, 24, 27}, identity.displayName())
	doc.text(84, headerY-20, 7.5, pdfColor{113, 113, 122}, subtitle)
	doc.text(360, headerY-2, 12.4, pdfColor{24, 24, 27}, "Demonstrativo de Caixa")
	doc.text(360, headerY-17, 10.2, pdfColor{24, 24, 27}, titleMode)
	doc.text(360, headerY-31, 7.8, pdfColor{113, 113, 122}, "Período: "+periodLabel+" ("+vencimento+")")

	infoY := headerY - 48
	for _, line := range drePDFIdentityLines(identity) {
		doc.text(drePDFMargin, infoY, 7.8, pdfColor{82, 82, 91}, line)
		infoY -= 11
	}
	doc.text(360, headerY-49, 7.5, pdfColor{82, 82, 91}, baseLine)
	doc.text(360, headerY-59, 7.5, pdfColor{82, 82, 91}, scopeLine)
	doc.text(360, headerY-69, 7.5, pdfColor{82, 82, 91}, cardLine)

	cardY := pdfPageHeight - 206
	cardW := 156.0
	cardGap := 16.0
	if data.IsCaixaPrevisao {
		cardW = (drePDFContentWidth - 30) / 4
		cardGap = 10
	}
	doc.summaryCard(drePDFMargin, cardY, cardW, cardIncomeLabel, formatCurrencyLabel(data.CashMonthIncomeTotal.rawCents()), pdfColor{34, 197, 94})
	doc.summaryCard(drePDFMargin+cardW+cardGap, cardY, cardW, cardExpenseLabel, formatCurrencyLabel(data.CashMonthExpenseTotal.rawCents()), pdfColor{239, 68, 68})
	resultColor := pdfColor{103, 68, 241}
	if data.CashMonthResultNegative {
		resultColor = pdfColor{239, 68, 68}
	}
	doc.summaryCard(drePDFMargin+(cardW+cardGap)*2, cardY, cardW, cardResultLabel, formatCurrencyLabel(data.CashMonthResultTotal.rawCents()), resultColor)
	if data.IsCaixaPrevisao {
		projectedBalanceText := "-"
		projectedBalanceColor := pdfColor{14, 165, 233}
		if data.ShowProjectedEndingBalance {
			projectedBalanceText = formatCurrencyLabel(data.ProjectedEndingBalance.rawCents())
			if data.ProjectedEndingBalanceNegative {
				projectedBalanceColor = pdfColor{239, 68, 68}
			}
		}
		doc.summaryCard(drePDFMargin+(cardW+cardGap)*3, cardY, cardW, "Saldo projetado", projectedBalanceText, projectedBalanceColor)
	}

	tableTop := cardY - drePDFBlockGap
	doc.dreSectionTitle(drePDFMargin, tableTop, tableTitle)
	tableY := tableTop - drePDFTableHeaderGap
	summaryCols := buildCaixaSummaryColumns(drePDFContentWidth, data.IsCaixaPrevisao)
	doc.dreTableHeader(drePDFMargin, tableY, summaryCols)
	tableY -= drePDFTableRowGap
	for _, row := range data.Caixa {
		resultColor := pdfColor{24, 24, 27}
		if row.ResultadoNegativo {
			resultColor = pdfColor{239, 68, 68}
		}
		cells := []pdfTableCell{
			{Text: row.Vencimento, Width: summaryCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
			{Text: formatCurrencyLabel(row.RawResultado), Width: summaryCols[1].Width, Align: "right", Color: resultColor},
		}
		if data.IsCaixaPrevisao {
			balanceText := "-"
			balanceColor := pdfColor{14, 165, 233}
			if row.HasProjectedEndingBalance {
				balanceText = formatCurrencyLabel(row.RawProjectedBalance)
				if row.ProjectedBalanceNegative {
					balanceColor = pdfColor{239, 68, 68}
				}
			}
			cells = append(cells, pdfTableCell{Text: balanceText, Width: summaryCols[2].Width, Align: "right", Color: balanceColor})
		}
		doc.dreTableRow(drePDFMargin, tableY, cells)
		tableY -= drePDFTableRowGap
	}
	tableY -= 12

	doc.dreSectionTitle(drePDFMargin, tableY, "Detalhe do mês selecionado")
	tableY -= drePDFTableHeaderGap
	detailCols := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Grupo / macro grupo", Width: 260, Align: "left"},
		{Label: "Tipo", Width: 82, Align: "left"},
		{Label: "Valor", Width: 150, Align: "right"},
	}, drePDFContentWidth)
	doc.dreTableHeader(drePDFMargin, tableY, detailCols)
	tableY -= drePDFTableRowGap

	if selected == nil || len(selected.MacroGroups) == 0 {
		doc.text(52, tableY, 8.4, pdfColor{113, 113, 122}, "Sem dados de caixa para o vencimento selecionado.")
		tableY -= drePDFTableRowGap
	} else {
		for _, item := range selected.MacroGroups {
			kind := "Receita"
			valueColor := pdfColor{34, 197, 94}
			if item.RawAmount < 0 {
				kind = "Despesa"
				valueColor = pdfColor{239, 68, 68}
			}
			doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: item.MacroGroup, Width: detailCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: kind, Width: detailCols[1].Width, Align: "left", Color: pdfColor{82, 82, 91}},
				{Text: formatCurrencyLabel(item.RawAmount), Width: detailCols[2].Width, Align: "right", Color: valueColor},
			})
			tableY -= drePDFTableRowGap
		}
		resultRowColor := pdfColor{103, 68, 241}
		if selected.RawResultado < 0 {
			resultRowColor = pdfColor{239, 68, 68}
		}
		doc.dreTableRowEmphasis(drePDFMargin, tableY, []pdfTableCell{
			{Text: "Resultado", Width: detailCols[0].Width + detailCols[1].Width, Align: "left", Color: pdfColor{24, 24, 27}},
			{Text: formatCurrencyLabel(selected.RawResultado), Width: detailCols[2].Width, Align: "right", Color: resultRowColor},
		})
		tableY -= drePDFBlockGap
	}

	if data.IsBusiness {
		doc.dreSectionTitle(drePDFMargin, tableY, "Custos por Grupo Macro")
		tableY -= drePDFTableHeaderGap
		if len(data.BusinessCostsCaixa) == 0 {
			doc.text(52, tableY, 8.4, pdfColor{113, 113, 122}, "Sem custos operacionais no periodo selecionado.")
			tableY -= drePDFTableRowGap
		} else {
			dreCostsCols := fitPDFColumnsToWidth([]pdfTableColumn{
				{Label: "Grupo macro", Width: 300, Align: "left"},
				{Label: "Participacao", Width: 92, Align: "right"},
				{Label: "Valor", Width: 100, Align: "right"},
			}, drePDFContentWidth)
			doc.dreTableHeader(drePDFMargin, tableY, dreCostsCols)
			tableY -= drePDFTableRowGap
			for _, item := range data.BusinessCostsCaixa {
				doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
					{Text: item.MacroGroup, Width: dreCostsCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
					{Text: businessCostPercentText(item), Width: dreCostsCols[1].Width, Align: "right", Color: pdfColor{82, 82, 91}},
					{Text: "-" + formatCurrencyLabel(item.RawAmount), Width: dreCostsCols[2].Width, Align: "right", Color: pdfColor{239, 68, 68}},
				})
				tableY -= drePDFTableRowGap
			}
		}
	}

	if len(revenueCategories) > 0 || len(expenseCategories) > 0 {
		doc = newDREPDFPage()
		pages = append(pages, doc)
		doc.text(drePDFMargin, headerY-4, 12.4, pdfColor{24, 24, 27}, title)
		doc.text(drePDFMargin, headerY-20, 7.8, pdfColor{113, 113, 122}, "Detalhamento por categoria - "+periodLabel+" ("+vencimento+")")
		tableY = headerY - 60
	}

	var catCols []pdfTableColumn
	if len(revenueCategories) > 0 {
		doc.dreSectionTitle(drePDFMargin, tableY, "Receitas por categoria")
		tableY -= drePDFTableHeaderGap
		catCols = fitPDFColumnsToWidth([]pdfTableColumn{
			{Label: "Categoria", Width: 300, Align: "left"},
			{Label: "Participacao", Width: 92, Align: "right"},
			{Label: "Valor", Width: 100, Align: "right"},
		}, drePDFContentWidth)
		doc.dreTableHeader(drePDFMargin, tableY, catCols)
		tableY -= drePDFTableRowGap
		for _, item := range revenueCategories {
			doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: item.Name, Width: catCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: pdfCategoryPercentText(item), Width: catCols[1].Width, Align: "right", Color: pdfColor{82, 82, 91}},
				{Text: formatCurrencyLabel(item.Amount), Width: catCols[2].Width, Align: "right", Color: pdfColor{34, 197, 94}},
			})
			tableY -= drePDFTableRowGap
		}
		tableY -= 18
	}

	if len(expenseCategories) > 0 {
		doc.dreSectionTitle(drePDFMargin, tableY, "Gastos por categoria")
		tableY -= drePDFTableHeaderGap
		if catCols == nil {
			catCols = fitPDFColumnsToWidth([]pdfTableColumn{
				{Label: "Categoria", Width: 300, Align: "left"},
				{Label: "Participacao", Width: 92, Align: "right"},
				{Label: "Valor", Width: 100, Align: "right"},
			}, drePDFContentWidth)
		}
		doc.dreTableHeader(drePDFMargin, tableY, catCols)
		tableY -= drePDFTableRowGap
		for _, item := range expenseCategories {
			doc.dreTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: item.Name, Width: catCols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: pdfCategoryPercentText(item), Width: catCols[1].Width, Align: "right", Color: pdfColor{82, 82, 91}},
				{Text: "-" + formatCurrencyLabel(item.Amount), Width: catCols[2].Width, Align: "right", Color: pdfColor{239, 68, 68}},
			})
			tableY -= drePDFTableRowGap
		}
	}

	for i, page := range pages {
		drawCompactDREPDFFooter(page, identity, generatedAt, i+1, len(pages))
	}

	return buildPDFDocument(pages, logo), nil
}

func pdfCategoryPercentText(item pdfCategorySummaryRow) string {
	if item.PercentLabel != "" {
		return item.PercentLabel
	}
	return fmt.Sprintf("%d%%", item.Percent)
}

func businessCostPercentText(item BusinessCostMacroRow) string {
	if item.PercentLabel != "" {
		return item.PercentLabel
	}
	return fmt.Sprintf("%d%%", item.Percent)
}

func buildExtratoMovimentosPDFReport(data RelatoriosData, identity workspaceReportIdentity, workspaceID, periodLabel string, mes, ano int, generatedAt time.Time) ([]byte, error) {
	logo, _ := loadWorkspacePDFLogo(workspaceID, identity.preferredLogoURL())
	doc := newDREPDFPage()

	headerY := pdfPageHeight - 62
	if logo != nil {
		ratio := math.Min(54/float64(logo.Width), 30/float64(logo.Height))
		doc.image("Im1", drePDFMargin, headerY-26, float64(logo.Width)*ratio, float64(logo.Height)*ratio)
	} else {
		doc.setColor(pdfColor{103, 68, 241})
		doc.rect(drePDFMargin, headerY-28, 30, 30, true)
		doc.text(drePDFMargin+7, headerY-10, 11.2, pdfColor{255, 255, 255}, initials(identity.displayName()))
	}

	doc.text(84, headerY-4, 12.2, pdfColor{24, 24, 27}, identity.displayName())
	doc.text(84, headerY-20, 7.5, pdfColor{113, 113, 122}, "Relatório de movimentos confirmados")
	doc.text(402, headerY-4, 14.2, pdfColor{24, 24, 27}, "Extrato")
	doc.text(402, headerY-20, 7.8, pdfColor{113, 113, 122}, "Período: "+periodLabel)

	infoY := headerY - 48
	for _, line := range drePDFIdentityLines(identity) {
		doc.text(drePDFMargin, infoY, 7.8, pdfColor{82, 82, 91}, line)
		infoY -= 11
	}
	doc.text(402, headerY-46, 7.5, pdfColor{82, 82, 91}, "Movimentos confirmados por conta")
	doc.text(402, headerY-56, 7.5, pdfColor{82, 82, 91}, fmt.Sprintf("%d linhas confirmadas no período", data.ExtratoMovementCount))
	doc.text(402, headerY-66, 7.5, pdfColor{82, 82, 91}, "Transferências listadas separadamente")

	tableTop := infoY - 28
	doc.compactSectionTitle(drePDFMargin, tableTop, 511, "Movimentos confirmados")
	tableY := tableTop - drePDFTableHeaderGap
	cols := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Data", Width: 55, Align: "left"},
		{Label: "Descrição", Width: 170, Align: "left"},
		{Label: "Conta", Width: 100, Align: "left"},
		{Label: "Tipo", Width: 80, Align: "left"},
		{Label: "Entrada", Width: 75, Align: "right"},
		{Label: "Saída", Width: 75, Align: "right"},
	}, drePDFContentWidth)
	doc.compactTableHeader(drePDFMargin, tableY, cols)
	tableY -= drePDFTableRowGap

	if len(data.ExtratoRows) == 0 {
		doc.text(52, tableY, 8.4, pdfColor{113, 113, 122}, "Nenhum movimento confirmado no periodo.")
		tableY -= drePDFTableRowGap
	} else {
		for _, row := range data.ExtratoRows {
			kind := "Transferencia"
			if row.Tipo == models.TransactionTypeIncome {
				kind = "Entrada"
			} else if row.Tipo == models.TransactionTypeExpense {
				kind = "Saida"
			}
			entradaText := "-"
			saidaText := "-"
			if row.Entrada.Reais != "" {
				entradaText = formatCurrencyLabel(row.AmountCents)
			}
			if row.Saida.Reais != "" {
				saidaText = "-" + formatCurrencyLabel(row.AmountCents)
			}
			doc.compactTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: row.DateLabel, Width: cols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: row.Description, Width: cols[1].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: row.AccountName, Width: cols[2].Width, Align: "left", Color: pdfColor{82, 82, 91}},
				{Text: kind, Width: cols[3].Width, Align: "left", Color: pdfColor{82, 82, 91}},
				{Text: entradaText, Width: cols[4].Width, Align: "right", Color: pdfColor{34, 197, 94}},
				{Text: saidaText, Width: cols[5].Width, Align: "right", Color: pdfColor{239, 68, 68}},
			})
			tableY -= drePDFTableRowGap
		}
	}

	drawCompactDREPDFFooter(doc, identity, generatedAt, 1, 1)
	return buildPDFDocument([]*pdfCanvas{doc}, logo), nil
}

func buildExtratoDiarioPDFReport(data RelatoriosData, identity workspaceReportIdentity, workspaceID, periodLabel string, mes, ano int, generatedAt time.Time) ([]byte, error) {
	logo, _ := loadWorkspacePDFLogo(workspaceID, identity.preferredLogoURL())
	doc := newDREPDFPage()

	headerY := pdfPageHeight - 62
	if logo != nil {
		ratio := math.Min(54/float64(logo.Width), 30/float64(logo.Height))
		doc.image("Im1", drePDFMargin, headerY-26, float64(logo.Width)*ratio, float64(logo.Height)*ratio)
	} else {
		doc.setColor(pdfColor{103, 68, 241})
		doc.rect(drePDFMargin, headerY-28, 30, 30, true)
		doc.text(drePDFMargin+7, headerY-10, 11.2, pdfColor{255, 255, 255}, initials(identity.displayName()))
	}

	doc.text(84, headerY-4, 12.2, pdfColor{24, 24, 27}, identity.displayName())
	doc.text(84, headerY-20, 7.5, pdfColor{113, 113, 122}, "Resumo diário de movimentos confirmados")
	doc.text(402, headerY-4, 14.2, pdfColor{24, 24, 27}, "Extrato Diário")
	doc.text(402, headerY-20, 7.8, pdfColor{113, 113, 122}, "Período: "+periodLabel)

	infoY := headerY - 48
	for _, line := range drePDFIdentityLines(identity) {
		doc.text(drePDFMargin, infoY, 7.8, pdfColor{82, 82, 91}, line)
		infoY -= 11
	}
	doc.text(402, headerY-46, 7.5, pdfColor{82, 82, 91}, "Fechamento diário do período")
	doc.text(402, headerY-56, 7.5, pdfColor{82, 82, 91}, fmt.Sprintf("%d dias com movimento", len(data.DailySummary)))
	doc.text(402, headerY-66, 7.5, pdfColor{82, 82, 91}, "Sem transferências internas")

	tableTop := infoY - 28
	doc.compactSectionTitle(drePDFMargin, tableTop, 511, "Resumo diário")
	tableY := tableTop - drePDFTableHeaderGap
	cols := fitPDFColumnsToWidth([]pdfTableColumn{
		{Label: "Dia", Width: 70, Align: "left"},
		{Label: "Entradas", Width: 110, Align: "right"},
		{Label: "Saídas", Width: 110, Align: "right"},
		{Label: "Resultado", Width: 110, Align: "right"},
		{Label: "Acumulado", Width: 110, Align: "right"},
	}, drePDFContentWidth)
	doc.compactTableHeader(drePDFMargin, tableY, cols)
	tableY -= drePDFTableRowGap

	if len(data.DailySummary) == 0 {
		doc.text(52, tableY, 8.4, pdfColor{113, 113, 122}, "Nenhum dia com movimentos confirmados no periodo.")
		tableY -= drePDFTableRowGap
	} else {
		for _, row := range data.DailySummary {
			net := row.RawIncome - row.RawExpense
			netText := formatCurrencyLabel(net)
			if net > 0 {
				netText = "+" + netText
			}
			doc.compactTableRow(drePDFMargin, tableY, []pdfTableCell{
				{Text: row.DateLabel, Width: cols[0].Width, Align: "left", Color: pdfColor{39, 39, 42}},
				{Text: formatCurrencyLabel(row.RawIncome), Width: cols[1].Width, Align: "right", Color: pdfColor{34, 197, 94}},
				{Text: "-" + formatCurrencyLabel(row.RawExpense), Width: cols[2].Width, Align: "right", Color: pdfColor{239, 68, 68}},
				{Text: netText, Width: cols[3].Width, Align: "right", Color: pdfColor{24, 24, 27}},
				{Text: formatCurrencyLabel(row.RawAccumulated), Width: cols[4].Width, Align: "right", Color: pdfColor{24, 24, 27}},
			})
			tableY -= drePDFTableRowGap
		}
	}

	drawCompactDREPDFFooter(doc, identity, generatedAt, 1, 1)
	return buildPDFDocument([]*pdfCanvas{doc}, logo), nil
}

func newDREPDFPage() *pdfCanvas {
	doc := newPDFCanvas()
	doc.setColor(pdfColor{245, 247, 250})
	doc.rect(0, 0, pdfPageWidth, pdfPageHeight, true)
	doc.setColor(pdfColor{255, 255, 255})
	doc.rect(32, 32, pdfPageWidth-64, pdfPageHeight-64, true)
	return doc
}

func drawDREPDFFooter(doc *pdfCanvas, identity workspaceReportIdentity, generatedAt time.Time, page, totalPages int) {
	footer := fmt.Sprintf("Gerado pelo ContaBase em %s - %s", generatedAt.Format("02/01/2006 15:04"), identity.displayName())
	doc.line(42, 58, pdfPageWidth-42, 58, pdfColor{228, 228, 231})
	doc.text(42, 42, 8, pdfColor{113, 113, 122}, footer)
	doc.text(420, 42, 8, pdfColor{113, 113, 122}, "Workspace: "+identity.Name)
	doc.text(500, 28, 8, pdfColor{113, 113, 122}, fmt.Sprintf("Pagina %d/%d", page, totalPages))
}

func drawCompactDREPDFFooter(doc *pdfCanvas, identity workspaceReportIdentity, generatedAt time.Time, page, totalPages int) {
	footer := fmt.Sprintf("Gerado pelo ContaBase em %s - %s", generatedAt.Format("02/01/2006 15:04"), identity.displayName())
	doc.line(drePDFMargin, 58, pdfPageWidth-drePDFMargin, 58, pdfColor{228, 228, 231})
	doc.text(drePDFMargin, 42, 7.2, pdfColor{113, 113, 122}, footer)
	doc.text(420, 42, 7.2, pdfColor{113, 113, 122}, "Workspace: "+identity.Name)
	doc.text(505, 28, 7.2, pdfColor{113, 113, 122}, fmt.Sprintf("Pagina %d/%d", page, totalPages))
}

func drePDFMonthLabel(mes, ano int) string {
	months := []string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}
	if mes < 1 || mes > 12 {
		return fmt.Sprintf("%02d/%04d", mes, ano)
	}
	return fmt.Sprintf("%s %d", months[mes-1], ano)
}

func drePDFIdentityLines(identity workspaceReportIdentity) []string {
	lines := make([]string, 0, 4)
	if strings.TrimSpace(identity.CNPJCPF) != "" {
		lines = append(lines, "CNPJ/CPF: "+strings.TrimSpace(identity.CNPJCPF))
	}
	if strings.TrimSpace(identity.Address) != "" {
		lines = append(lines, "Endereço: "+strings.TrimSpace(identity.Address))
	}
	if strings.TrimSpace(identity.Phone) != "" {
		lines = append(lines, "Telefone: "+strings.TrimSpace(identity.Phone))
	}
	if len(lines) == 0 {
		lines = append(lines, "Workspace: "+identity.displayName())
	}
	return lines
}

func dreRowForCompetencia(rows []DRERow, competencia string) *DRERow {
	for i := range rows {
		if rows[i].Competencia == competencia {
			return &rows[i]
		}
	}
	return nil
}

func loadWorkspacePDFLogo(workspaceID, logoURL string) (*pdfImage, error) {
	logoURL = strings.TrimSpace(logoURL)
	prefix := "/uploads/workspaces/" + workspaceID + "/"
	if logoURL == "" || !strings.HasPrefix(logoURL, prefix) {
		return nil, fmt.Errorf("invalid logo")
	}
	fileName := strings.TrimPrefix(logoURL, prefix)
	if fileName == "" || filepath.Base(fileName) != fileName {
		return nil, fmt.Errorf("invalid logo file")
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".png", ".jpg", ".jpeg":
	default:
		return nil, fmt.Errorf("unsupported logo format")
	}
	fullPath := filepath.Join(paths.WorkspaceUploadsDir(workspaceID), fileName)
	baseAbs, err := filepath.Abs(paths.WorkspaceUploadsDir(workspaceID))
	if err != nil {
		return nil, err
	}
	fullAbs, err := filepath.Abs(fullPath)
	if err != nil {
		return nil, err
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return nil, fmt.Errorf("logo outside workspace")
	}
	f, err := os.Open(fullAbs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return newPDFImage(img)
}

type pdfColor struct {
	R, G, B int
}

type pdfImage struct {
	Width  int
	Height int
	Data   []byte
}

func newPDFImage(src image.Image) (*pdfImage, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid image")
	}
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Over)

	raw := make([]byte, 0, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := rgba.PixOffset(x, y)
			raw = append(raw, rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2])
		}
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return &pdfImage{Width: w, Height: h, Data: compressed.Bytes()}, nil
}

type pdfCanvas struct {
	content bytes.Buffer
}

type pdfTableColumn struct {
	Label string
	Width float64
	Align string
}

type pdfTableCell struct {
	Text  string
	Width float64
	Align string
	Color pdfColor
}

func newPDFCanvas() *pdfCanvas {
	return &pdfCanvas{}
}

func (p *pdfCanvas) setColor(c pdfColor) {
	fmt.Fprintf(&p.content, "%.3f %.3f %.3f rg\n", float64(c.R)/255, float64(c.G)/255, float64(c.B)/255)
}

func (p *pdfCanvas) rect(x, y, w, h float64, fill bool) {
	op := "S"
	if fill {
		op = "f"
	}
	fmt.Fprintf(&p.content, "%.2f %.2f %.2f %.2f re %s\n", x, y, w, h, op)
}

func (p *pdfCanvas) line(x1, y1, x2, y2 float64, c pdfColor) {
	fmt.Fprintf(&p.content, "%.3f %.3f %.3f RG 0.75 w %.2f %.2f m %.2f %.2f l S\n", float64(c.R)/255, float64(c.G)/255, float64(c.B)/255, x1, y1, x2, y2)
}

func (p *pdfCanvas) text(x, y, size float64, c pdfColor, text string) {
	escaped := pdfEscapeText(text)
	fmt.Fprintf(&p.content, "BT /F1 %.2f Tf %.3f %.3f %.3f rg 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n",
		size, float64(c.R)/255, float64(c.G)/255, float64(c.B)/255, x, y, escaped)
}

func (p *pdfCanvas) image(name string, x, y, w, h float64) {
	fmt.Fprintf(&p.content, "q %.2f 0 0 %.2f %.2f %.2f cm /%s Do Q\n", w, h, x, y, name)
}

func (p *pdfCanvas) summaryCard(x, y, w float64, title, value string, color pdfColor) {
	p.setColor(pdfColor{249, 250, 251})
	p.rect(x, y, w, 48, true)
	p.line(x, y+48, x+w, y+48, pdfColor{228, 228, 231})
	p.line(x, y, x+w, y, pdfColor{228, 228, 231})
	p.text(x+12, y+30, 7.1, pdfColor{113, 113, 122}, strings.ToUpper(title))
	p.text(x+12, y+11, 12.2, color, value)
}

func (p *pdfCanvas) dreSectionTitle(x, y float64, title string) {
	p.text(x, y, 10.2, pdfColor{24, 24, 27}, title)
	p.line(x, y-8, x+drePDFContentWidth, y-8, pdfColor{228, 228, 231})
}

func (p *pdfCanvas) dreTableHeader(x, y float64, cols []pdfTableColumn) {
	p.setColor(pdfColor{248, 250, 252})
	total := 0.0
	for _, col := range cols {
		total += col.Width
	}
	p.rect(x, y-5, total, 18, true)
	cursor := x
	for _, col := range cols {
		label := strings.ToUpper(col.Label)
		p.text(alignedX(cursor, col.Width, col.Align, label, 6.6), y+1, 6.6, pdfColor{82, 82, 91}, label)
		cursor += col.Width
	}
}

func (p *pdfCanvas) dreTableRow(x, y float64, cells []pdfTableCell) {
	p.line(x, y-6, x+492, y-6, pdfColor{244, 244, 245})
	cursor := x
	for _, cell := range cells {
		p.text(alignedX(cursor, cell.Width, cell.Align, cell.Text, 7.7), y, 7.7, cell.Color, cell.Text)
		cursor += cell.Width
	}
}

func (p *pdfCanvas) dreTableRowEmphasis(x, y float64, cells []pdfTableCell) {
	p.setColor(pdfColor{245, 243, 255})
	p.rect(x, y-7, 492, 20, true)
	cursor := x
	for _, cell := range cells {
		p.text(alignedX(cursor, cell.Width, cell.Align, cell.Text, 8.2), y, 8.2, cell.Color, cell.Text)
		cursor += cell.Width
	}
}

func (p *pdfCanvas) compactSectionTitle(x, y, w float64, title string) {
	p.text(x, y, 10.2, pdfColor{24, 24, 27}, title)
	p.line(x, y-8, x+w, y-8, pdfColor{228, 228, 231})
}

func (p *pdfCanvas) compactTableHeader(x, y float64, cols []pdfTableColumn) {
	total := pdfTableWidth(cols)
	p.setColor(pdfColor{248, 250, 252})
	p.rect(x, y-5, total, 18, true)
	cursor := x
	for _, col := range cols {
		label := fitPDFText(strings.ToUpper(col.Label), col.Width, 6.6)
		p.text(alignedX(cursor, col.Width, col.Align, label, 6.6), y+1, 6.6, pdfColor{82, 82, 91}, label)
		cursor += col.Width
	}
}

func (p *pdfCanvas) compactTableRow(x, y float64, cells []pdfTableCell) {
	total := pdfCellWidth(cells)
	p.line(x, y-6, x+total, y-6, pdfColor{244, 244, 245})
	cursor := x
	for _, cell := range cells {
		text := fitPDFText(cell.Text, cell.Width, 7.7)
		p.text(alignedX(cursor, cell.Width, cell.Align, text, 7.7), y, 7.7, cell.Color, text)
		cursor += cell.Width
	}
}

func (p *pdfCanvas) sectionTitle(x, y float64, title string) {
	p.text(x, y, 13, pdfColor{24, 24, 27}, title)
	p.line(x, y-10, pdfPageWidth-x, y-10, pdfColor{228, 228, 231})
}

func (p *pdfCanvas) tableHeader(x, y float64, cols []pdfTableColumn) {
	p.setColor(pdfColor{244, 244, 245})
	total := 0.0
	for _, col := range cols {
		total += col.Width
	}
	p.rect(x, y-6, total, 22, true)
	cursor := x
	for _, col := range cols {
		p.text(alignedX(cursor, col.Width, col.Align, col.Label, 9), y+1, 8, pdfColor{82, 82, 91}, strings.ToUpper(col.Label))
		cursor += col.Width
	}
}

func (p *pdfCanvas) tableRow(x, y float64, cells []pdfTableCell) {
	p.line(x, y-7, x+492, y-7, pdfColor{244, 244, 245})
	cursor := x
	for _, cell := range cells {
		p.text(alignedX(cursor, cell.Width, cell.Align, cell.Text, 9), y, 9, cell.Color, cell.Text)
		cursor += cell.Width
	}
}

func (p *pdfCanvas) tableRowEmphasis(x, y float64, cells []pdfTableCell) {
	p.setColor(pdfColor{245, 243, 255})
	p.rect(x, y-8, 492, 24, true)
	cursor := x
	for _, cell := range cells {
		p.text(alignedX(cursor, cell.Width, cell.Align, cell.Text, 10), y, 10, cell.Color, cell.Text)
		cursor += cell.Width
	}
}

func alignedX(x, w float64, align, text string, size float64) float64 {
	if align != "right" {
		return x + 10
	}
	estimate := float64(len([]rune(text))) * size * 0.48
	return x + w - estimate - 10
}

func fitPDFColumnsToWidth(cols []pdfTableColumn, target float64) []pdfTableColumn {
	total := pdfTableWidth(cols)
	if total == target || total == 0 || len(cols) == 0 {
		return cols
	}
	scale := target / total
	out := make([]pdfTableColumn, len(cols))
	scaledTotal := 0.0
	for i, col := range cols {
		out[i] = col
		if i == len(cols)-1 {
			out[i].Width = target - scaledTotal
			continue
		}
		out[i].Width = math.Floor(col.Width*scale*10) / 10
		scaledTotal += out[i].Width
	}
	return out
}

func fitPDFText(text string, width, size float64) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	maxChars := int(math.Floor((width - 20) / (size * 0.48)))
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	if maxChars <= 3 {
		return string(runes[:maxChars])
	}
	return string(runes[:maxChars-3]) + "..."
}

func pdfTableWidth(cols []pdfTableColumn) float64 {
	total := 0.0
	for _, col := range cols {
		total += col.Width
	}
	return total
}

func pdfCellWidth(cells []pdfTableCell) float64 {
	total := 0.0
	for _, cell := range cells {
		total += cell.Width
	}
	return total
}

func (p *pdfCanvas) build(img *pdfImage) []byte {
	return buildPDFDocument([]*pdfCanvas{p}, img)
}

func buildPDFDocument(pages []*pdfCanvas, img *pdfImage) []byte {
	if len(pages) == 0 {
		pages = []*pdfCanvas{newPDFCanvas()}
	}

	builder := newPDFBuilder()

	initEmbeddedFont()
	var fontObj int
	if embeddedFontReady {
		fontObj = addEmbeddedFontObjects(builder)
	} else {
		fontObj = builder.addString("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	}

	imageObj := 0
	if img != nil {
		var obj bytes.Buffer
		fmt.Fprintf(&obj, "<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", img.Width, img.Height, len(img.Data))
		obj.Write(img.Data)
		obj.WriteString("\nendstream")
		imageObj = builder.addBytes(obj.Bytes())
	}

	resources := fmt.Sprintf("<< /Font << /F1 %d 0 R >>", fontObj)
	if imageObj != 0 {
		resources += fmt.Sprintf(" /XObject << /Im1 %d 0 R >>", imageObj)
	}
	resources += " >>"

	contentObjs := make([]int, 0, len(pages))
	for _, page := range pages {
		contentObjs = append(contentObjs, builder.addStream(page.content.Bytes()))
	}

	pagesObjNum := len(builder.objects) + len(contentObjs) + 1
	pageObjs := make([]int, 0, len(contentObjs))
	for _, contentObj := range contentObjs {
		pageObjs = append(pageObjs, builder.addString(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.2f %.2f] /Resources %s /Contents %d 0 R >>", pagesObjNum, pdfPageWidth, pdfPageHeight, resources, contentObj)))
	}

	var kids strings.Builder
	for _, pageObj := range pageObjs {
		fmt.Fprintf(&kids, "%d 0 R ", pageObj)
	}
	pagesObj := builder.addString(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.TrimSpace(kids.String()), len(pageObjs)))
	catalogObj := builder.addString(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesObj))
	return builder.bytes(catalogObj)
}

func addEmbeddedFontObjects(builder *pdfBuilder) int {
	fontFileObj := builder.addStream(embeddedFontData)

	var widthsBuf bytes.Buffer
	widthsBuf.WriteString("[")
	for i, w := range embeddedFontWidths {
		if i > 0 {
			widthsBuf.WriteByte(' ')
		}
		fmt.Fprintf(&widthsBuf, "%d", w)
	}
	widthsBuf.WriteString("]")

	fontDescObj := builder.addString(fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /%s /Flags 32 /FontBBox [-600 -200 1200 800] /ItalicAngle 0 /Ascent 800 /Descent -200 /CapHeight 700 /StemV 80 /FontFile2 %d 0 R >>",
		embeddedFontName, fontFileObj,
	))

	return builder.addString(fmt.Sprintf(
		"<< /Type /Font /Subtype /TrueType /BaseFont /%s /FirstChar 32 /LastChar 255 /Widths %s /FontDescriptor %d 0 R /Encoding /WinAnsiEncoding >>",
		embeddedFontName, widthsBuf.String(), fontDescObj,
	))
}

type pdfBuilder struct {
	objects [][]byte
}

func newPDFBuilder() *pdfBuilder {
	return &pdfBuilder{objects: make([][]byte, 0, 8)}
}

func (b *pdfBuilder) addString(s string) int {
	return b.addBytes([]byte(s))
}

func (b *pdfBuilder) addBytes(data []byte) int {
	b.objects = append(b.objects, data)
	return len(b.objects)
}

func (b *pdfBuilder) addStream(data []byte) int {
	var obj bytes.Buffer
	fmt.Fprintf(&obj, "<< /Length %d >>\nstream\n", len(data))
	obj.Write(data)
	obj.WriteString("\nendstream")
	return b.addBytes(obj.Bytes())
}

func (b *pdfBuilder) bytes(catalogObj int) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	out.Write([]byte{'%', 0xe2, 0xe3, 0xcf, 0xd3, '\n'})
	offsets := make([]int, len(b.objects)+1)
	for i, obj := range b.objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		out.Write(obj)
		out.WriteString("\nendobj\n")
	}
	xrefOffset := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(b.objects)+1)
	out.WriteString("0000000000 65535 f \n")
	for i := 1; i < len(offsets); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(b.objects)+1, catalogObj, xrefOffset)
	return out.Bytes()
}

func pdfEscapeText(text string) string {
	var buf bytes.Buffer
	for _, r := range text {
		if r == '\n' || r == '\r' {
			r = ' '
		} else if r == '–' || r == '—' {
			r = '-'
		}
		b, ok := winAnsiByte(r)
		if !ok {
			b = '?'
		}
		switch b {
		case '\\', '(', ')':
			buf.WriteByte('\\')
			buf.WriteByte(b)
		default:
			if b < 32 {
				buf.WriteByte(' ')
			} else if b >= 128 {
				fmt.Fprintf(&buf, "\\%03o", b)
			} else {
				buf.WriteByte(b)
			}
		}
	}
	return buf.String()
}

func winAnsiByte(r rune) (byte, bool) {
	if r >= 32 && r <= 126 {
		return byte(r), true
	}
	if r >= 160 && r <= 255 {
		return byte(r), true
	}
	switch r {
	case '€':
		return 128, true
	case '‚':
		return 130, true
	case 'ƒ':
		return 131, true
	case '„':
		return 132, true
	case '…':
		return 133, true
	case '†':
		return 134, true
	case '‡':
		return 135, true
	case 'ˆ':
		return 136, true
	case '‰':
		return 137, true
	case 'Š':
		return 138, true
	case '‹':
		return 139, true
	case 'Œ':
		return 140, true
	case 'Ž':
		return 142, true
	case '‘':
		return 145, true
	case '’':
		return 146, true
	case '“':
		return 147, true
	case '”':
		return 148, true
	case '•':
		return 149, true
	case '™':
		return 153, true
	case 'š':
		return 154, true
	case '›':
		return 155, true
	case 'œ':
		return 156, true
	case 'ž':
		return 158, true
	case 'Ÿ':
		return 159, true
	default:
		return 0, false
	}
}

func (m MoneyDisplay) rawCents() int64 {
	reais := strings.ReplaceAll(strings.ReplaceAll(m.Reais, ".", ""), "-", "")
	reais = strings.TrimSpace(reais)
	cents := strings.TrimPrefix(strings.TrimSpace(m.Cents), ",")
	if cents == "" {
		cents = "00"
	}
	value, _ := strconv.ParseInt(reais+cents, 10, 64)
	if strings.HasPrefix(strings.TrimSpace(m.Reais), "-") {
		value *= -1
	}
	return value
}

var (
	embeddedFontInit   sync.Once
	embeddedFontData   []byte
	embeddedFontWidths [224]int
	embeddedFontReady  bool
	embeddedFontName   = "CBEmbedded-Regular"
)

func initEmbeddedFont() {
	embeddedFontInit.Do(func() {
		embeddedFontData = goregular.TTF
		f, err := sfnt.Parse(embeddedFontData)
		if err != nil {
			return
		}
		var buf sfnt.Buffer
		missingGlyphIdx, _ := f.GlyphIndex(&buf, 0)
		missingAdv, _ := f.GlyphAdvance(&buf, missingGlyphIdx, fixed.I(1000), font.HintingNone)
		defaultWidth := 600
		if missingAdv > 0 {
			defaultWidth = missingAdv.Round()
		}
		for b := 32; b <= 255; b++ {
			r := winAnsiToUnicode(byte(b))
			if r == 0 {
				embeddedFontWidths[b-32] = defaultWidth
				continue
			}
			idx, err := f.GlyphIndex(&buf, r)
			if err != nil || idx == 0 {
				embeddedFontWidths[b-32] = defaultWidth
				continue
			}
			adv, err := f.GlyphAdvance(&buf, idx, fixed.I(1000), font.HintingNone)
			if err != nil {
				embeddedFontWidths[b-32] = defaultWidth
				continue
			}
			w := adv.Round()
			if w <= 0 {
				w = defaultWidth
			}
			embeddedFontWidths[b-32] = w
		}
		embeddedFontReady = true
	})
}

func winAnsiToUnicode(b byte) rune {
	if b >= 32 && b <= 126 {
		return rune(b)
	}
	if b >= 160 && b <= 255 {
		return rune(b)
	}
	switch b {
	case 128:
		return '€'
	case 130:
		return '‚'
	case 131:
		return 'ƒ'
	case 132:
		return '„'
	case 133:
		return '…'
	case 134:
		return '†'
	case 135:
		return '‡'
	case 136:
		return 'ˆ'
	case 137:
		return '‰'
	case 138:
		return 'Š'
	case 139:
		return '‹'
	case 140:
		return 'Œ'
	case 142:
		return 'Ž'
	case 145:
		return '‘'
	case 146:
		return '’'
	case 147:
		return '“'
	case 148:
		return '”'
	case 149:
		return '•'
	case 153:
		return '™'
	case 154:
		return 'š'
	case 155:
		return '›'
	case 156:
		return 'œ'
	case 158:
		return 'ž'
	case 159:
		return 'Ÿ'
	default:
		return 0
	}
}
