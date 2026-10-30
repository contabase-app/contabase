package handlers

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/contabase-app/contabase/internal/categorycontract"
)

func (h *RelatoriosHandler) buildContabilCSVData(w http.ResponseWriter, r *http.Request) (RelatoriosData, int, int, bool) {
	mes, ano, err := parseDREPDFPeriod(r)
	if err != nil {
		http.Error(w, "periodo invalido", http.StatusBadRequest)
		return RelatoriosData{}, 0, 0, false
	}

	data, err := h.buildRelatoriosDataWithModo("", "contabil", "movimentos", "realizado", mes, ano)
	if err != nil {
		http.Error(w, "erro ao gerar csv", http.StatusInternalServerError)
		return RelatoriosData{}, 0, 0, false
	}

	if !data.IsBusiness {
		http.Error(w, "relatorio contabil disponivel apenas para workspaces business", http.StatusForbidden)
		return RelatoriosData{}, 0, 0, false
	}

	return data, mes, ano, true
}

func (h *RelatoriosHandler) HandleExportarContabilResumoCSV(w http.ResponseWriter, r *http.Request) {
	if !h.requireReportsPermission(w) {
		return
	}
	data, mes, ano, ok := h.buildContabilCSVData(w, r)
	if !ok {
		return
	}

	periodo := fmt.Sprintf("%04d-%02d", ano, mes)
	workspace := queryWorkspaceName(h.DB, h.WorkspaceID)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="relatorio-contabil-resumo-%s.csv"`, periodo))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	writeContabilResumoCSVBody(w, data, periodo, workspace)
}

func writeContabilResumoCSVBody(out io.Writer, data RelatoriosData, periodo, workspace string) {
	writer := csv.NewWriter(out)
	defer writer.Flush()

	_ = writer.Write([]string{
		"periodo",
		"workspace",
		"tipo",
		"resultado_operacional_centavos",
		"caixa_realizado_centavos",
		"movimentos_confirmados",
		"retiradas_distribuicao_centavos",
		"investimentos_patrimonial_centavos",
		"saldo_total_reconstruido_centavos",
		"saldo_referencia",
	})

	_ = writer.Write([]string{
		periodo,
		sanitizeCSVField(workspace),
		"business",
		strconv.FormatInt(data.ContabilDREOperacional.RawResultado, 10),
		strconv.FormatInt(data.ContabilCaixaAtual.RawResultado, 10),
		strconv.Itoa(data.ContabilExtratoMovementCount),
		strconv.FormatInt(data.ContabilProLabore.rawCents(), 10),
		strconv.FormatInt(data.ContabilInvestimentosPatrimonialRaw, 10),
		strconv.FormatInt(data.ContabilSaldoTotalRaw, 10),
		sanitizeCSVField(data.ContabilSaldoReferencia),
	})
}

func (h *RelatoriosHandler) HandleExportarContabilMacrosCSV(w http.ResponseWriter, r *http.Request) {
	if !h.requireReportsPermission(w) {
		return
	}
	data, mes, ano, ok := h.buildContabilCSVData(w, r)
	if !ok {
		return
	}

	periodo := fmt.Sprintf("%04d-%02d", ano, mes)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="relatorio-contabil-macros-%s.csv"`, periodo))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	writeContabilMacrosCSVBody(w, data, periodo)
}

func writeContabilMacrosCSVBody(out io.Writer, data RelatoriosData, periodo string) {
	writer := csv.NewWriter(out)
	defer writer.Flush()

	_ = writer.Write([]string{"periodo", "base", "macro_group", "natureza", "total_centavos", "percentual", "observacao"})

	writeMacroRow := func(base, macroGroup string, raw int64, baseAbsTotal int64, observacao string) {
		_ = writer.Write([]string{
			periodo,
			base,
			sanitizeCSVField(macroGroup),
			contabilCSVNatureza(raw),
			strconv.FormatInt(raw, 10),
			contabilCSVPercent(raw, baseAbsTotal),
			sanitizeCSVField(observacao),
		})
	}

	if data.HasContabilDREOperacional {
		total := absSumDREMacros(data.ContabilDREOperacional.MacroGroups)
		for _, row := range data.ContabilDREOperacional.MacroGroups {
			writeMacroRow("competencia_operacional", row.MacroGroup, row.RawAmount, total, "")
		}
	}

	if data.HasContabilCaixaAtual {
		total := absSumCaixaMacros(data.ContabilCaixaAtual.MacroGroups)
		for _, row := range data.ContabilCaixaAtual.MacroGroups {
			writeMacroRow("caixa_realizado", row.MacroGroup, row.RawAmount, total, "")
		}
	}

	receitasTotal := absSumBusinessMacros(data.ContabilReceitasMacro)
	for _, row := range data.ContabilReceitasMacro {
		writeMacroRow("receitas", row.MacroGroup, row.RawAmount, receitasTotal, "")
	}

	despesasTotal := absSumBusinessMacros(data.ContabilDespesasMacro)
	for _, row := range data.ContabilDespesasMacro {
		writeMacroRow("despesas", row.MacroGroup, row.RawAmount, despesasTotal, "")
	}

	tributosTotal := absSumImpostos(data.ContabilImpostos)
	for _, row := range data.ContabilImpostos {
		writeMacroRow("tributos", row.MacroGroup, row.RawAmount, tributosTotal, "")
	}

	rendimentosTotal := absSumRendimentos(data.ContabilRendimentos)
	for _, row := range data.ContabilRendimentos {
		writeMacroRow("receitas_financeiras", row.CategoryName, row.RawAmount, rendimentosTotal, "")
	}

	writeMacroRow("retiradas_distribuicao", categorycontract.MacroRetiradasDistribuicao, data.ContabilProLabore.rawCents(), data.ContabilProLabore.rawCents(), "")

	writeMacroRow("investimentos_patrimonial", categorycontract.MacroInvestimentosPatrimonial, data.ContabilInvestimentosPatrimonialRaw, data.ContabilInvestimentosPatrimonialRaw, "")

	for _, alerta := range contabilCSVAlertas(data) {
		_ = writer.Write([]string{
			periodo,
			"alertas",
			sanitizeCSVField(alerta.macroGroup),
			"",
			"",
			"",
			sanitizeCSVField(alerta.observacao),
		})
	}
}

func (h *RelatoriosHandler) HandleExportarContabilSaldosCSV(w http.ResponseWriter, r *http.Request) {
	if !h.requireReportsPermission(w) {
		return
	}
	data, mes, ano, ok := h.buildContabilCSVData(w, r)
	if !ok {
		return
	}

	periodo := fmt.Sprintf("%04d-%02d", ano, mes)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="relatorio-contabil-saldos-%s.csv"`, periodo))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	writeContabilSaldosCSVBody(w, data, periodo)
}

func writeContabilSaldosCSVBody(out io.Writer, data RelatoriosData, periodo string) {
	writer := csv.NewWriter(out)
	defer writer.Flush()

	_ = writer.Write([]string{"periodo", "referencia", "conta", "tipo_conta", "saldo_reconstruido_centavos", "arquivada"})

	for _, acc := range data.ContabilSaldosContas {
		_ = writer.Write([]string{
			periodo,
			sanitizeCSVField(data.ContabilSaldoReferencia),
			sanitizeCSVField(acc.Name),
			sanitizeCSVField(acc.Type),
			strconv.FormatInt(acc.RawBalance, 10),
			strconv.FormatBool(acc.IsArchived),
		})
	}

	_ = writer.Write([]string{
		periodo,
		sanitizeCSVField(data.ContabilSaldoReferencia),
		"TOTAL",
		"TOTAL",
		strconv.FormatInt(data.ContabilSaldoTotalRaw, 10),
		strconv.FormatBool(false),
	})
}

type contabilCSVAlerta struct {
	macroGroup string
	observacao string
}

func contabilCSVAlertas(data RelatoriosData) []contabilCSVAlerta {
	var alertas []contabilCSVAlerta
	if data.HasContabilMissingReceitasFinanceiras {
		alertas = append(alertas, contabilCSVAlerta{
			macroGroup: categorycontract.MacroReceitasFinanceiras,
			observacao: "Nenhuma categoria em Receitas Financeiras",
		})
	}
	if data.HasContabilInvestimentosOutrosLegacy {
		alertas = append(alertas, contabilCSVAlerta{
			macroGroup: categorycontract.MacroInvestimentosOutros,
			observacao: "Movimento legado em Investimentos/Outros para revisao",
		})
	}
	if data.HasContabilOutrasEntradasMovimento {
		alertas = append(alertas, contabilCSVAlerta{
			macroGroup: categorycontract.MacroOutrasEntradas,
			observacao: "Movimento em grupo generico Outras Entradas para revisao",
		})
	}
	if data.HasContabilOutrasSaidasMovimento {
		alertas = append(alertas, contabilCSVAlerta{
			macroGroup: categorycontract.MacroOutrasSaidas,
			observacao: "Movimento em grupo generico Outras Saidas para revisao",
		})
	}
	return alertas
}

func contabilCSVNatureza(raw int64) string {
	if raw < 0 {
		return "saida"
	}
	return "entrada"
}

func contabilCSVPercent(raw int64, baseAbsTotal int64) string {
	if baseAbsTotal == 0 {
		return ""
	}
	value := raw
	if value < 0 {
		value = -value
	}
	pct := float64(value) / float64(baseAbsTotal) * 100
	return fmt.Sprintf("%.1f", pct)
}

func absSumDREMacros(rows []DREMacroGroupAmount) int64 {
	var total int64
	for _, row := range rows {
		total += absInt64(row.RawAmount)
	}
	return total
}

func absSumCaixaMacros(rows []CaixaMacroGroupAmount) int64 {
	var total int64
	for _, row := range rows {
		total += absInt64(row.RawAmount)
	}
	return total
}

func absSumBusinessMacros(rows []BusinessCostMacroRow) int64 {
	var total int64
	for _, row := range rows {
		total += absInt64(row.RawAmount)
	}
	return total
}

func absSumImpostos(rows []ContabilImposto) int64 {
	var total int64
	for _, row := range rows {
		total += absInt64(row.RawAmount)
	}
	return total
}

func absSumRendimentos(rows []ContabilRendimentoFinanceiro) int64 {
	var total int64
	for _, row := range rows {
		total += absInt64(row.RawAmount)
	}
	return total
}
