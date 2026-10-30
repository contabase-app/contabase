package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// R2 — Fundação Premium e privacidade financeira de /relatorios.
//
// Contratos cobertos aqui:
//   - a página carrega premium.css pelo assetPath, antes do CSS da página, e
//     ativa o escopo [data-ui="premium"] sem duplicar o App Shell;
//   - o cabeçalho passa a usar ui-page-header/ui-eyebrow/ui-page-title/
//     ui-page-description com o controle global de privacidade;
//   - o botão de privacidade reusa data-privacy-toggle (sem segundo listener)
//     e o conteúdo HTMX (#relatorios-body) continua coberto pelo mesmo escopo;
//   - todo valor monetário do template permanece marcado com data-sensitive;
//   - relatorios.css aplica a máscara canônica (••••) sem alterar a geometria
//     e sem reintroduzir a ponte local --cb-*.

func relatoriosPremiumTestTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("relatorios-premium").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath":       func(path string) string { return path + "?v=test" },
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/relatorios.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
		resolveTemplatePath(t, "templates/components/seletor_meses.html"),
	))
}

func relatoriosPremiumTestData(isBusiness bool) RelatoriosData {
	return RelatoriosData{
		Title:                "Relatórios",
		UserName:             "Vitor Ferreira",
		UserInitials:         "VF",
		ActiveWorkspaceName:  "Família",
		ActiveTab:            "caixa",
		MesAtual:             10,
		AnoAtual:             2026,
		MonthLabel:           "Outubro de 2026",
		IsBusiness:           isBusiness,
		IsCaixaRealizado:     true,
		RealizedIncomeTotal:  MoneyDisplay{Reais: "12.345", Cents: ",67"},
		RealizedExpenseTotal: MoneyDisplay{Reais: "4.321", Cents: ",00"},
		CashMonthResultTotal: MoneyDisplay{Reais: "8.024", Cents: ",67"},
		RealizedCashCount:    7,
		CaixaMacroGroups:     []string{"Receitas", "Despesas"},
		Caixa: []CaixaRow{{
			Vencimento: "Outubro",
			MacroGroups: []CaixaMacroGroupAmount{{
				MacroGroup: "Receitas",
				Amount:     MoneyDisplay{Reais: "12.345", Cents: ",67"},
			}},
			Resultado: MoneyDisplay{Reais: "8.024", Cents: ",67"},
		}},
		CategoriasCaixa: []CategoriaBar{{
			Nome:         "Moradia",
			Color:        "sky",
			Valor:        MoneyDisplay{Reais: "1.234", Cents: ",56"},
			Percent:      42,
			PercentLabel: "42%",
		}},
	}
}

func renderRelatoriosPremiumPage(t *testing.T, data RelatoriosData) string {
	t.Helper()
	tpl := relatoriosPremiumTestTemplates(t)
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "relatorios-page", data); err != nil {
		t.Fatalf("render relatorios page: %v", err)
	}
	return out.String()
}

func TestRelatoriosPageAdoptsPremiumFoundationAndHeader(t *testing.T) {
	body := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))

	for _, expected := range []string{
		`/assets/css/premium.css?v=test`,
		`data-ui="premium"`,
		`cb-v2-relatorios-header ui-page-header`,
		`class="ui-eyebrow">Análises`,
		`class="ui-page-title">Relatórios`,
		`class="ui-page-description">Visualize o desempenho financeiro`,
		`data-privacy-toggle`,
		`aria-pressed="false"`,
		`aria-label="Ocultar valores financeiros"`,
		`data-tooltip-hover data-tooltip-text="Ocultar valores financeiros"`,
		`data-lucide="eye" data-privacy-icon`,
		`id="relatorios-body"`,
		`data-page-section="reports"`,
	} {
		assertContains(t, body, expected)
	}

	// premium.css é fundação: precisa carregar antes do CSS da página.
	if strings.Index(body, "/assets/css/premium.css?v=test") > strings.Index(body, "/assets/css/relatorios.css?v=test") {
		t.Fatal("premium.css must load before relatorios.css")
	}
	// O eyebrow legado do shell não pode continuar no cabeçalho.
	if strings.Contains(body, `cb-v2-eyebrow">Análises`) {
		t.Fatal("relatorios header must use ui-eyebrow, not the legacy shell eyebrow")
	}
	// O App Shell é único; a página não monta um segundo shell.
	if strings.Count(body, `data-app-shell`) != 1 {
		t.Fatalf("relatorios page must render exactly one App Shell, got %d", strings.Count(body, "data-app-shell"))
	}
}

func TestRelatoriosPrivacyToggleIsUniqueAndUsesGlobalMechanism(t *testing.T) {
	body := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))

	if got := strings.Count(body, `data-privacy-toggle`); got != 1 {
		t.Fatalf("relatorios must expose exactly one global privacy toggle, got %d", got)
	}
	if got := strings.Count(body, `data-privacy-icon`); got != 1 {
		t.Fatalf("privacy toggle needs exactly one icon target, got %d", got)
	}
	if strings.Contains(body, "relatorios.js") {
		t.Fatal("relatorios must not load a page-specific JS for privacy (global privacy.js owns it)")
	}

	privacyJS := readTestFile(t, "assets/js/privacy.js")
	for _, expected := range []string{
		`sessionStorage.getItem('contabasePrivacy') === '1'`,
		`document.body.classList.toggle('privacy-enabled', enabled)`,
		`if (window.__contabasePrivacyDelegated === true) return;`,
		`'#privacyToggle,[data-privacy-toggle]'`,
		`btn.setAttribute('aria-pressed', enabled ? 'true' : 'false');`,
		`btn.setAttribute('aria-label', label);`,
	} {
		if !strings.Contains(privacyJS, expected) {
			t.Fatalf("privacy.js lost the canonical global contract %q", expected)
		}
	}

	// O HTMX reaplica o estado global ao trocar #relatorios-body, sem listener
	// novo e sem segundo sistema de inicialização.
	lifecycle := readTestFile(t, "assets/js/htmx-lifecycle.js")
	if !strings.Contains(lifecycle, `runAppShellTask('initPrivacyToggle', window.initPrivacyToggle, [scope]);`) {
		t.Fatal("htmx lifecycle must re-apply the global privacy state after swaps")
	}
	if !strings.Contains(lifecycle, `document.body.addEventListener('htmx:afterSwap'`) {
		t.Fatal("htmx lifecycle must keep the canonical afterSwap hook")
	}
}

func TestRelatoriosTabsKeepHTMXContractForPersonalAndBusiness(t *testing.T) {
	personal := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))
	business := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(true))

	for _, body := range []string{personal, business} {
		for _, expected := range []string{
			`hx-target="#relatorios-body"`,
			`hx-swap="outerHTML"`,
			`hx-push-url="true"`,
			`/relatorios?tab=caixa&modo=realizado&mes=10&ano=2026&partial=content`,
			`/relatorios?tab=extrato&view=movimentos&mes=10&ano=2026&partial=content`,
		} {
			assertContains(t, body, expected)
		}
		if strings.Count(body, `hx-target="#relatorios-body"`) < 4 {
			t.Fatal("all report tabs/modes/views must keep targeting #relatorios-body")
		}
	}

	// Personal: Contábil e Competência não são renderizadas.
	if strings.Contains(personal, `tab=contabil`) || strings.Contains(personal, `tab=competencia`) {
		t.Fatal("personal workspace must not render business-only tabs")
	}
	// Business: as quatro áreas ficam disponíveis e dentro do mesmo escopo HTMX.
	for _, expected := range []string{
		`/relatorios?tab=contabil&mes=10&ano=2026&partial=content`,
		`/relatorios?tab=competencia&mes=10&ano=2026&partial=content`,
		`/relatorios?tab=extrato&view=diario&mes=10&ano=2026&partial=content`,
	} {
		assertContains(t, business, expected)
	}
}

func TestRelatoriosFinancialValuesStayMarkedSensitive(t *testing.T) {
	src := readTestFile(t, "templates/pages/relatorios.html")

	// Nenhum valor monetário pode ficar fora de um elemento data-sensitive.
	// A máscara é CSS-only; um R$ sem a marca vazaria com a privacidade ativa.
	for i, line := range strings.Split(src, "\n") {
		if !strings.Contains(line, "R$") {
			continue
		}
		if !strings.Contains(line, "data-sensitive") {
			t.Fatalf("templates/pages/relatorios.html:%d renders a monetary value without data-sensitive: %s", i+1, strings.TrimSpace(line))
		}
		if strings.Contains(line, `title="`) || strings.Contains(line, `aria-label="`) || strings.Contains(line, `sr-only`) {
			t.Fatalf("templates/pages/relatorios.html:%d leaks a monetary value into an auxiliary attribute: %s", i+1, strings.TrimSpace(line))
		}
	}

	// Os valores continuam formatados exatamente como antes; a privacidade é
	// apresentação e não pode alterar cálculo nem formatação.
	body := renderRelatoriosPremiumPage(t, relatoriosPremiumTestData(false))
	for _, expected := range []string{
		`R$ 12.345,67`,
		`R$ 4.321,00`,
		`R$ 8.024,67`,
		`R$ 1.234,56`,
	} {
		assertContains(t, body, expected)
	}
	assertContains(t, body, `ui-kpi__value" data-sensitive>R$ 12.345,67</strong>`)
	assertContains(t, body, `data-sensitive>R$ 1.234,56</span>`)
}

func TestRelatoriosCSSPrivacyCoverageFollowsCanonicalPattern(t *testing.T) {
	css := readTestFile(t, "assets/css/relatorios.css")
	for _, expected := range []string{
		`.privacy-enabled .cb-v2-relatorios-main [data-sensitive]`,
		`:is(span, strong, p, small, div, dd, b, td, th, li)[data-sensitive]::after`,
		`:is(td, th)[data-sensitive]::after`,
		`content: "••••"`,
		`visibility: hidden`,
		`user-select: none`,
		`font-variant-numeric: normal`,
		`.cb-v2-relatorios-header [data-privacy-toggle]`,
		`width: 2.75rem;`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("relatorios.css missing privacy coverage %q", expected)
		}
	}

	// A fundação Premium usa a ponte canônica de ui-tokens.css; a ponte local
	// divergente não pode voltar para a página.
	for _, forbidden := range []string{
		`--cb-bg:`,
		`--cb-surface-1:`,
		`--cb-surface-overlay:`,
		`--cb-border-focus:`,
		`--cb-accent-solid:`,
		`--cb-text-main:`,
	} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("relatorios.css must not redefine the local --cb-* bridge, found %q", forbidden)
		}
	}

	// A tipografia do h1 vem de ui-page-title; nenhum override legado pode
	// achatar a escala Premium.
	if strings.Contains(css, `.cb-v2-relatorios-header h1`) {
		t.Fatal("relatorios.css must not override the Premium page-title scale")
	}
}
