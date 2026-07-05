package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func testRelatoriosBranchTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("relatorios-branches").Parse(`
{{define "relatorios-page"}}<div data-template="page">{{template "relatorios-body" .}}</div>{{end}}
{{define "relatorios-body"}}<div id="relatorios-body" data-template="body">body-content</div>{{end}}
`))
}

func TestRelatoriosRenderBranches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)

	handler := RelatoriosHandler{
		DB:          db,
		Templates:   testRelatoriosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	tests := []struct {
		name         string
		url          string
		hxRequest    bool
		wantStatus   int
		wantFragment string
		wantMissing  string
	}{
		{
			name:         "full page render",
			url:          "/relatorios?mes=8&ano=2026",
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="page"`,
		},
		{
			name:         "htmx partial renders body only",
			url:          "/relatorios?mes=8&ano=2026&partial=content",
			hxRequest:    true,
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="body"`,
			wantMissing:  `data-template="page"`,
		},
		{
			name:         "htmx navigation without partial renders full page",
			url:          "/relatorios?mes=8&ano=2026",
			hxRequest:    true,
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="page"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.hxRequest {
				req.Header.Set("HX-Request", "true")
			}
			rr := httptest.NewRecorder()
			handler.HandleRelatoriosConceito(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			body := rr.Body.String()
			if !strings.Contains(body, tc.wantFragment) {
				t.Fatalf("body missing %q\nbody:\n%s", tc.wantFragment, body)
			}
			if tc.wantMissing != "" && strings.Contains(body, tc.wantMissing) {
				t.Fatalf("body should not contain %q\nbody:\n%s", tc.wantMissing, body)
			}
		})
	}
}

func TestRelatoriosPartialHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		Templates:   testRelatoriosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/relatorios?mes=7&ano=2026&partial=content", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleRelatoriosConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	replaceUrl := rr.Header().Get("HX-Replace-Url")
	if replaceUrl == "" {
		t.Fatal("missing HX-Replace-Url header")
	}
	if strings.Contains(replaceUrl, "partial=") {
		t.Errorf("HX-Replace-Url %q should not contain partial=", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "mes=7") {
		t.Errorf("HX-Replace-Url %q should contain mes=7", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "ano=2026") {
		t.Errorf("HX-Replace-Url %q should contain ano=2026", replaceUrl)
	}
}

func TestRelatoriosFullRenderNoHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		Templates:   testRelatoriosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/relatorios?mes=7&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleRelatoriosConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Header().Get("HX-Replace-Url") != "" {
		t.Errorf("full render should not set HX-Replace-Url, got %q", rr.Header().Get("HX-Replace-Url"))
	}
}

func TestRelatoriosActiveTabsUseAccentState(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `bg-[var(--cb-accent)] border-[var(--cb-accent)] text-white`) {
		t.Fatalf("main active tab should use accent state")
	}
	if !strings.Contains(body, `bg-[var(--cb-accent)] border border-[var(--cb-accent)] text-white`) {
		t.Fatalf("active subtab should use accent state")
	}
}

func TestRelatoriosMonthSelectorPartialContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if data.MonthSelectorHXTarget != "#relatorios-body" {
		t.Errorf("MonthSelectorHXTarget = %q, want #relatorios-body", data.MonthSelectorHXTarget)
	}
	if data.MonthSelectorHXSelect != "#relatorios-body" {
		t.Errorf("MonthSelectorHXSelect = %q, want #relatorios-body", data.MonthSelectorHXSelect)
	}
	if data.MonthSelectorHXSwap != "outerHTML" {
		t.Errorf("MonthSelectorHXSwap = %q, want outerHTML", data.MonthSelectorHXSwap)
	}
	if data.MonthSelectorPartial != "content" {
		t.Errorf("MonthSelectorPartial = %q, want content", data.MonthSelectorPartial)
	}
	for _, q := range []string{data.MonthSelectorPrevQuery, data.MonthSelectorNextQuery, data.MonthSelectorCurrentQuery} {
		if strings.Contains(q, "partial=") {
			t.Errorf("month query %q should not contain partial=", q)
		}
		if !strings.Contains(q, "tab=caixa") {
			t.Errorf("month query %q should contain tab=caixa", q)
		}
		if !strings.Contains(q, "modo=realizado") {
			t.Errorf("month query %q should contain modo=realizado", q)
		}
		if strings.Contains(q, "view=") {
			t.Errorf("month query %q should not contain view= when view is default 'movimentos'", q)
		}
	}

	for _, opt := range data.MonthOptions {
		if !strings.Contains(opt.Query, "tab=caixa") {
			t.Errorf("month option query %q should contain tab=caixa", opt.Query)
		}
		if !strings.Contains(opt.Query, "modo=realizado") {
			t.Errorf("month option query %q should contain modo=realizado", opt.Query)
		}
		if strings.Contains(opt.Query, "view=") {
			t.Errorf("month option query %q should not contain view= when view is default 'movimentos'", opt.Query)
		}
	}
}

func TestRelatoriosMonthSelectorPreservesTabAndView(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)

	handler := RelatoriosHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	tests := []struct {
		name         string
		tab          string
		view         string
		wantQueryHas []string
		wantQueryNot []string
	}{
		{
			name:         "tab=caixa preserves tab",
			tab:          "caixa",
			view:         "movimentos",
			wantQueryHas: []string{"tab=caixa", "modo=realizado"},
			wantQueryNot: []string{"view="},
		},
		{
			name:         "tab=competencia preserves tab",
			tab:          "competencia",
			view:         "movimentos",
			wantQueryHas: []string{"tab=competencia"},
			wantQueryNot: []string{"view="},
		},
		{
			name:         "tab=extrato preserves tab",
			tab:          "extrato",
			view:         "movimentos",
			wantQueryHas: []string{"tab=extrato"},
			wantQueryNot: []string{"view="},
		},
		{
			name:         "tab=extrato view=diario preserves both",
			tab:          "extrato",
			view:         "diario",
			wantQueryHas: []string{"tab=extrato", "view=diario"},
			wantQueryNot: nil,
		},
		{
			name:         "tab=extrato view=movimentos omits default view",
			tab:          "extrato",
			view:         "movimentos",
			wantQueryHas: []string{"tab=extrato"},
			wantQueryNot: []string{"view=movimentos", "view="},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := handler.buildRelatoriosData("", tc.tab, tc.view, 8, 2026)
			if err != nil {
				t.Fatalf("buildRelatoriosData: %v", err)
			}

			queries := []string{data.MonthSelectorPrevQuery, data.MonthSelectorNextQuery, data.MonthSelectorCurrentQuery}
			for _, q := range queries {
				for _, want := range tc.wantQueryHas {
					if !strings.Contains(q, want) {
						t.Errorf("query %q missing expected %q", q, want)
					}
				}
				for _, unwanted := range tc.wantQueryNot {
					if strings.Contains(q, unwanted) {
						t.Errorf("query %q should not contain %q", q, unwanted)
					}
				}
			}

			for _, opt := range data.MonthOptions {
				for _, want := range tc.wantQueryHas {
					if !strings.Contains(opt.Query, want) {
						t.Errorf("option query %q missing expected %q", opt.Query, want)
					}
				}
				for _, unwanted := range tc.wantQueryNot {
					if strings.Contains(opt.Query, unwanted) {
						t.Errorf("option query %q should not contain %q", opt.Query, unwanted)
					}
				}
			}
		})
	}
}

func TestRelatoriosMonthSelectorPreservesCaixaPrevisao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}

	for _, q := range []string{data.MonthSelectorPrevQuery, data.MonthSelectorNextQuery, data.MonthSelectorCurrentQuery} {
		if !strings.Contains(q, "tab=caixa") || !strings.Contains(q, "modo=previsao") {
			t.Fatalf("month query %q should preserve tab=caixa and modo=previsao", q)
		}
	}
	for _, opt := range data.MonthOptions {
		if !strings.Contains(opt.Query, "tab=caixa") || !strings.Contains(opt.Query, "modo=previsao") {
			t.Fatalf("month option %q should preserve tab=caixa and modo=previsao", opt.Query)
		}
	}
}

func TestRelatoriosExtratoTemplateParsesAndRenders(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}

	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}

	tpl := template.Must(template.New("relatorios-smoke").Funcs(funcs).Parse(string(raw)))
	template.Must(tpl.New("layout-start").Parse("<html><body>"))
	template.Must(tpl.New("layout-end").Parse("</body></html>"))
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))

	data := RelatoriosData{
		ActiveTab:   "extrato",
		ExtratoView: "movimentos",
		MesAtual:    8,
		AnoAtual:    2026,
		MonthLabel:  "Agosto 2026",
		ExtratoRows: []StatementExtratoRow{
			{
				DateLabel:   "10/08",
				Description: "Freela",
				AccountName: "Conta Corrente",
				ContactName: "Cliente Alpha",
				Tipo:        "INCOME",
				IsRealized:  true,
				AmountCents: 120000,
				Entrada:     MoneyMinor(120000),
			},
		},
		DailySummary: []DailySummaryRow{
			{
				DateLabel:          "10/08",
				IncomeTotal:        MoneyMinor(120000),
				ExpenseTotal:       MoneyMinor(0),
				NetTotal:           MoneyMinor(120000),
				AccumulatedBalance: MoneyMinor(120000),
				RawIncome:          120000,
				RawExpense:         0,
				RawAccumulated:     120000,
				HasIncome:          true,
				NetPositive:        true,
				NetClass:           "text-[var(--cb-success)]",
			},
		},
		ExtratoMovementCount: 1,
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"Extrato — Agosto 2026",
		"Movimentos confirmados do período",
		"1 confirmados",
		"Somente movimentos liquidados no período selecionado.",
		"Consolidação do período",
		"Movimentos",
		"Resumo diário",
		"Cliente Alpha",
		"Transferências internas aparecem no extrato de movimentos e ficam fora do fechamento diário.",
		"Extrato",
		"PDF",
		"CSV",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered body missing %q", want)
		}
	}
	// Ensure Extrato section does not render pending/status-previsto chips.
	extratoStart := strings.Index(body, "Extrato —")
	if extratoStart < 0 {
		t.Fatal("missing Extrato header")
	}
	extratoBody := body[extratoStart:]
	if strings.Contains(extratoBody, "Previsto") {
		t.Fatal("Extrato section should not contain Previsto status")
	}

	// Ensure Movimentos table uses full width and no fixed 60rem min-width.
	movimentosStart := strings.Index(body, "Lançamentos")
	if movimentosStart < 0 {
		t.Fatal("missing Lançamentos section")
	}
	movimentosBody := body[movimentosStart:]
	if strings.Contains(movimentosBody, "min-w-[60rem]") {
		t.Fatal("Movimentos table should not force min-w-[60rem]")
	}
	if !strings.Contains(movimentosBody, "reports-table-compact w-full") {
		t.Fatal("Movimentos table should use w-full")
	}
}

func TestRelatoriosCaixaTemplateNaoRenderizaPrevistoNemProximosVencimentos(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}

	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}

	tpl := template.Must(template.New("relatorios-caixa-smoke").Funcs(funcs).Parse(string(raw)))
	template.Must(tpl.New("layout-start").Parse("<html><body>"))
	template.Must(tpl.New("layout-end").Parse("</body></html>"))
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))

	data := RelatoriosData{
		ActiveTab:             "caixa",
		IsBusiness:            true,
		MesAtual:              8,
		AnoAtual:              2026,
		MonthLabel:            "Agosto 2026",
		RealizedIncomeTotal:   MoneyMinor(100000),
		RealizedExpenseTotal:  MoneyMinor(50000),
		RealizedCashTotal:     MoneyMinor(50000),
		CashMonthIncomeTotal:  MoneyMinor(100000),
		CashMonthExpenseTotal: MoneyMinor(50000),
		CashMonthResultTotal:  MoneyMinor(50000),
		RealizedCashCount:     2,
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"Entradas recebidas",
		"Dinheiro entrou",
		"Saídas pagas",
		"Dinheiro saiu",
		"Resultado realizado",
		"Entradas - saídas",
		"Movimentos confirmados",
		"Registros liquidados",
		"Demonstrativo de Caixa Realizado",
		"Por data de pagamento",
		"Base pela data efetiva de pagamento/recebimento",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered caixa body missing %q", want)
		}
	}

	for _, notWant := range []string{
		"Total previsto",
		"Impacto esperado no mês",
		"Próximos vencimentos",
		"Nenhum movimento previsto",
		"Realizado + previsto",
		"Inclui lançamentos pagos e pendentes",
	} {
		if strings.Contains(body, notWant) {
			t.Fatalf("rendered caixa body should not contain %q", notWant)
		}
	}
}

func TestRelatoriosCaixaTemplatePrevisaoRenderizaSubmodo(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}

	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}

	tpl := template.Must(template.New("relatorios-caixa-previsao-smoke").Funcs(funcs).Parse(string(raw)))
	template.Must(tpl.New("layout-start").Parse("<html><body>"))
	template.Must(tpl.New("layout-end").Parse("</body></html>"))
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))

	data := RelatoriosData{
		ActiveTab:                  "caixa",
		CaixaModo:                  "previsao",
		IsCaixaPrevisao:            true,
		IsBusiness:                 true,
		MesAtual:                   8,
		AnoAtual:                   2026,
		MonthLabel:                 "Agosto 2026",
		ProjectedIncomeTotal:       MoneyMinor(100000),
		ProjectedExpenseTotal:      MoneyMinor(55000),
		ProjectedCashTotal:         MoneyMinor(45000),
		CashMonthIncomeTotal:       MoneyMinor(100000),
		CashMonthExpenseTotal:      MoneyMinor(55000),
		CashMonthResultTotal:       MoneyMinor(45000),
		ProjectedEndingBalance:     MoneyMinor(245000),
		ShowProjectedEndingBalance: true,
		Caixa: []CaixaRow{
			{
				Vencimento:   "2026-08",
				MacroGroups:  []CaixaMacroGroupAmount{{MacroGroup: "Essencial", Amount: MoneyMinor(-55000), RawAmount: -55000}},
				Resultado:    MoneyMinor(-55000),
				RawResultado: -55000,
			},
		},
		CaixaMacroGroups: []string{"Essencial"},
		RevenueByCategoryCaixa: []RevenueCategorySlice{{
			Name:         "Salario",
			Amount:       MoneyMinor(100000),
			Percent:      100,
			PercentLabel: "100%",
			Stroke:       "rgb(16 185 129)",
		}},
		BusinessCostsCaixa: []BusinessCostMacroRow{{
			MacroGroup:   "Essencial",
			Amount:       MoneyMinor(55000),
			RawAmount:    55000,
			Percent:      100,
			PercentLabel: "100%",
			Stroke:       "rgb(239 68 68)",
		}},
		CategoriasCaixa: []CategoriaBar{{
			Nome:         "Compras Cartao",
			Icon:         "shopping-bag",
			Color:        "#6b7280",
			Valor:        MoneyMinor(55000),
			Percent:      100,
			PercentLabel: "100%",
		}},
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"Realizado",
		"Previsão",
		"Entradas previstas",
		"Saídas previstas",
		"Resultado previsto",
		"SALDO PROJETADO",
		"Saldo estimado no fim do mês",
		"R$ 2.450,00",
		"Demonstrativo de Caixa Previsto",
		"Por vencimento — próximos 12 meses",
		"Saldo Projetado",
		"/relatorios/export.csv?tipo=caixa",
		"mes=8",
		"Receitas previstas por Categoria",
		"Custos por Grupo Macro",
		"Gastos por Categoria",
		"Compras Cartao",
		"modo=previsao",
		"Base híbrida de caixa",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered caixa previsao body missing %q", want)
		}
	}
	for _, notWant := range []string{
		"Próximos vencimentos",
		"Entradas recebidas",
		"Saídas pagas",
	} {
		if strings.Contains(body, notWant) {
			t.Fatalf("rendered caixa previsao body should not contain %q", notWant)
		}
	}
}

func TestRelatoriosCaixaTemplateRealizadoMantemMovimentosConfirmados(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}

	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}

	tpl := template.Must(template.New("relatorios-caixa-realizado-smoke").Funcs(funcs).Parse(string(raw)))
	template.Must(tpl.New("layout-start").Parse("<html><body>"))
	template.Must(tpl.New("layout-end").Parse("</body></html>"))
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))

	data := RelatoriosData{
		ActiveTab:            "caixa",
		CaixaModo:            "realizado",
		IsCaixaRealizado:     true,
		IsBusiness:           true,
		MesAtual:             8,
		AnoAtual:             2026,
		MonthLabel:           "Agosto 2026",
		RealizedIncomeTotal:  MoneyMinor(100000),
		RealizedExpenseTotal: MoneyMinor(55000),
		CashMonthResultTotal: MoneyMinor(45000),
		RealizedCashCount:    4,
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"Entradas recebidas",
		"Saídas pagas",
		"Resultado realizado",
		"Movimentos confirmados",
		"Registros liquidados",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered caixa realizado body missing %q", want)
		}
	}
	for _, notWant := range []string{"SALDO PROJETADO", "Fim do mês", "Saldo Projetado"} {
		if strings.Contains(body, notWant) {
			t.Fatalf("rendered caixa realizado body should not contain %q", notWant)
		}
	}
}

func TestRelatoriosPersonalNaoRenderizaCompetenciaOuPorLancamento(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}

	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}

	tpl := template.Must(template.New("relatorios-personal-tabs").Funcs(funcs).Parse(string(raw)))
	template.Must(tpl.New("layout-start").Parse("<html><body>"))
	template.Must(tpl.New("layout-end").Parse("</body></html>"))
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))

	data := RelatoriosData{
		ActiveTab:        "caixa",
		CaixaModo:        "realizado",
		IsCaixaRealizado: true,
		IsBusiness:       false,
		MesAtual:         8,
		AnoAtual:         2026,
		MonthLabel:       "Agosto 2026",
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Fluxo de Caixa") || !strings.Contains(body, "Extrato") {
		t.Fatalf("personal deveria renderizar Fluxo de Caixa e Extrato")
	}
	for _, notWant := range []string{"Por lançamento", "Competência", "DRE Simplificada"} {
		if strings.Contains(body, notWant) {
			t.Fatalf("personal nao deveria renderizar %q", notWant)
		}
	}
}

func TestRelatoriosBusinessRenderizaCompetencia(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}

	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}

	tpl := template.Must(template.New("relatorios-business-tabs").Funcs(funcs).Parse(string(raw)))
	template.Must(tpl.New("layout-start").Parse("<html><body>"))
	template.Must(tpl.New("layout-end").Parse("</body></html>"))
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))

	data := RelatoriosData{
		ActiveTab:      "competencia",
		IsBusiness:     true,
		MesAtual:       8,
		AnoAtual:       2026,
		MonthLabel:     "Agosto 2026",
		DRE:            []DRERow{{Competencia: "2026-08", Resultado: MoneyMinor(0)}},
		DREMacroGroups: []string{},
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Competência") || !strings.Contains(body, "DRE Simplificada") {
		t.Fatalf("business deveria renderizar Competência/DRE")
	}
	if strings.Contains(body, "Por lançamento") {
		t.Fatal("business nao deveria renderizar Por lançamento")
	}
}
