package handlers

import (
	"strings"
	"testing"
)

// R5 — Tabelas financeiras e hierarquia do Extrato.
//
// Contratos cobertos:
//   - o Extrato usa um único painel, sem cabeçalho/descrição repetidos nem
//     painel com borda dentro de outro;
//   - as quatro tabelas reais mantêm colunas,<th scope>, valores, tipos,
//     privacidade e regiões roláveis;
//   - as tabelas DRE/Caixa preservam meses/macros/resultado e usam tokens nas
//     colunas de destaque;
//   - a composição CSS usa separadores discretos e não reintroduz card por
//     célula nem cores fixas de tabela.

func TestRelatoriosExtratoHierarchyIsSinglePanel(t *testing.T) {
	data := relatoriosPremiumTestData(false)
	data.ExtratoView = "movimentos"
	data.ExtratoMovementCount = 3
	data.ExtratoRows = []StatementExtratoRow{{
		DateLabel:   "25/10",
		Description: "Passagem e Alimentação — Jéssica",
		AccountName: "Dinheiro",
		Tipo:        "INCOME",
		Entrada:     MoneyDisplay{Reais: "200", Cents: ",00"},
	}}
	data.DailySummary = []DailySummaryRow{{
		DateLabel:          "25/10",
		IncomeTotal:        MoneyDisplay{Reais: "200", Cents: ",00"},
		HasIncome:          true,
		NetTotal:           MoneyDisplay{Reais: "200", Cents: ",00"},
		NetPositive:        true,
		AccumulatedBalance: MoneyDisplay{Reais: "200", Cents: ",00"},
	}}
	body := renderRelatoriosPremiumPage(t, data)

	for _, expected := range []string{
		`ui-panel reports-table-panel`,
		`class="reports-extrato-toolbar"`,
		`class="cb-tabs reports-view-tabs"`,
		`class="reports-extrato-note"`,
		`Tabela de movimentos do extrato`,
		`Tabela de resumo diário do extrato`,
	} {
		assertContains(t, body, expected)
	}
	for _, forbidden := range []string{
		`reports-extrato-panel-head`,
		`Consolidação do período`,
		`Compare movimentos confirmados, contas impactadas`,
		`Entradas e saídas por conta`,
		`Fechamento sem transferências`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("extrato must not repeat the legacy hierarchy element %q", forbidden)
		}
	}
	// O título do extrato aparece uma única vez (cabeçalho da página).
	if got := strings.Count(body, "Extrato — "+data.MonthLabel); got != 1 {
		t.Fatalf("extrato period must appear once, got %d", got)
	}
}

func TestRelatoriosTablesPreserveColumnsValuesAndPrivacy(t *testing.T) {
	data := relatoriosPremiumTestData(false)
	data.ExtratoView = "movimentos"
	data.ExtratoMovementCount = 3
	data.ExtratoRows = []StatementExtratoRow{
		{
			DateLabel:   "25/10",
			Description: "Passagem e Alimentação — Jéssica",
			AccountName: "Dinheiro",
			ContactName: "Jéssica",
			Tipo:        "INCOME",
			Entrada:     MoneyDisplay{Reais: "200", Cents: ",00"},
		},
		{
			DateLabel:   "25/10",
			Description: "Pagamento de Fatura - Inter",
			AccountName: "Inter | Vitor",
			Tipo:        "EXPENSE",
			Saida:       MoneyDisplay{Reais: "99", Cents: ",99"},
		},
		{
			DateLabel:   "25/10",
			Description: "Transferir XP",
			AccountName: "Itaú",
			Tipo:        "TRANSFER",
			Saida:       MoneyDisplay{Reais: "75", Cents: ",00"},
		},
	}
	data.DailySummary = []DailySummaryRow{
		{
			DateLabel:          "25/10",
			IncomeTotal:        MoneyDisplay{Reais: "200", Cents: ",00"},
			ExpenseTotal:       MoneyDisplay{Reais: "99", Cents: ",99"},
			HasIncome:          true,
			HasExpense:         true,
			NetTotal:           MoneyDisplay{Reais: "100", Cents: ",01"},
			NetPositive:        true,
			AccumulatedBalance: MoneyDisplay{Reais: "100", Cents: ",01"},
		},
	}
	body := renderRelatoriosPremiumPage(t, data)

	for _, expected := range []string{
		`<th scope="col" class="reports-table-th text-left">Data</th>`,
		`<th scope="col" class="reports-table-th text-left">Descrição</th>`,
		`<th scope="col" class="reports-table-th text-left">Movimento</th>`,
		`<th scope="col" class="reports-table-th text-left">Conta</th>`,
		`<th scope="col" class="reports-table-th text-left">Categoria</th>`,
		`<th scope="col" class="reports-table-th text-right">Entrada</th>`,
		`<th scope="col" class="reports-table-th text-right">Saída</th>`,
		`<th scope="col" class="reports-table-th text-left">Dia</th>`,
		`<th scope="col" class="reports-table-th text-right">Acumulado do período</th>`,
		`class="block reports-cell-description">Passagem e Alimentação — Jéssica</span>`,
		`class="block reports-cell-description reports-cell-meta">Jéssica</span>`,
		`data-sensitive>R$ 200,00</span>`,
		`data-sensitive>R$ 99,99</span>`,
		`data-sensitive>R$ 75,00</span>`,
		`+R$ 100,01`,
		`R$ 100,01`,
		`reports-cell-total`,
		`Entrada`,
		`Saída`,
		`Transferência`,
		`role="region" aria-describedby="reports-extrato-scroll-hint" aria-label="Tabela de movimentos do extrato" tabindex="0"`,
		`role="region" aria-describedby="reports-diario-scroll-hint" aria-label="Tabela de resumo diário do extrato" tabindex="0"`,
	} {
		assertContains(t, body, expected)
	}
	// Descrição longa quebra, não é truncada.
	start := strings.Index(body, `class="reports-table reports-table--extrato`)
	end := start + strings.Index(body[start:], "</table>")
	extratoTable := body[start:end]
	if strings.Contains(extratoTable, "truncate") {
		t.Fatal("extrato description must wrap, not truncate")
	}
	if strings.Contains(extratoTable, "border-separate") || strings.Contains(extratoTable, "rounded-l-xl") {
		t.Fatal("extrato table must not use per-cell card borders/rounding")
	}
}

func TestRelatoriosCashAndDRETablesKeepMacrosAndResultColumns(t *testing.T) {
	business := relatoriosPremiumTestData(true)
	business.IsCaixaPrevisao = true
	business.IsCaixaRealizado = false
	business.CaixaMacroGroups = []string{"Receitas", "Despesas"}
	business.Caixa = []CaixaRow{{
		Vencimento: "2026-10",
		MacroGroups: []CaixaMacroGroupAmount{
			{MacroGroup: "Receitas", Amount: MoneyDisplay{Reais: "1.000", Cents: ",00"}},
			{MacroGroup: "Despesas", Amount: MoneyDisplay{Reais: "600", Cents: ",00"}},
		},
		Resultado:                 MoneyDisplay{Reais: "400", Cents: ",00"},
		HasProjectedEndingBalance: true,
		ProjectedBalance:          MoneyDisplay{Reais: "5.400", Cents: ",00"},
	}}
	business.DREMacroGroups = []string{"Receitas", "Despesas"}
	business.DRE = []DRERow{{
		Competencia: "2026-10",
		MacroGroups: []DREMacroGroupAmount{
			{MacroGroup: "Receitas", Amount: MoneyDisplay{Reais: "1.000", Cents: ",00"}},
			{MacroGroup: "Despesas", Amount: MoneyDisplay{Reais: "600", Cents: ",00"}},
		},
		Resultado: MoneyDisplay{Reais: "400", Cents: ",00"},
	}}
	body := renderRelatoriosPremiumPage(t, business)

	for _, expected := range []string{
		`<table class="reports-dre w-full">`,
		`<th scope="col" class="reports-dre-th text-left">Mês</th>`,
		`<th scope="col" class="reports-dre-th reports-col-result text-right whitespace-nowrap">Resultado</th>`,
		`<th scope="col" class="reports-dre-th reports-col-projected text-right whitespace-nowrap">Saldo Projetado</th>`,
		`class="reports-dre-td reports-col-result text-right font-semibold`,
		`class="reports-dre-td reports-col-projected text-right font-semibold`,
		`<th scope="col" class="reports-dre-th text-left">Competência</th>`,
		`R$ 5.400,00`,
	} {
		assertContains(t, body, expected)
	}
	for _, forbidden := range []string{"bg-violet-500", "bg-sky-500", "border-separate", "rounded-r-xl"} {
		if strings.Contains(readTestFile(t, "templates/pages/relatorios.html"), forbidden) {
			t.Fatalf("cash/DRE tables must not use legacy styling %q", forbidden)
		}
	}
}

func TestRelatoriosTableCSSCoverage(t *testing.T) {
	css := readTestFile(t, "assets/css/relatorios.css")
	for _, expected := range []string{
		`.reports-table-panel`,
		`.reports-dre,`,
		`.reports-table {`,
		`border-bottom: 1px solid var(--ui-border)`,
		`.reports-col-result`,
		`.reports-col-projected`,
		`.reports-cell-description-cell`,
		`.reports-cell-description {`,
		`.reports-extrato-toolbar`,
		`.reports-extrato-note`,
		`.reports-table-empty`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("relatorios.css missing table contract %q", expected)
		}
	}
	for _, forbidden := range []string{`reports-table-compact`, `reports-extrato-panel-head`, `bg-violet-500`, `bg-sky-500`} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("relatorios.css must not keep legacy table styling %q", forbidden)
		}
	}
}
