package handlers

import (
	"strings"
	"testing"
)

// R6 — Controles de exportação de /relatorios.
//
// Contratos cobertos:
//   - todos os controles usam a família ui-btn secundária (sem CTA primário);
//   - o agrupamento mantém a orientação de que o arquivo inclui valores
//     completos, mesmo com a privacidade visual ativa;
//   - endpoints, parâmetros de período/modo e opções de menu continuam iguais;
//   - a antecipação local de 44px no mobile fica restrita às exportações.

func TestRelatoriosExportControlsUsePremiumSecondaryStyle(t *testing.T) {
	personalData := relatoriosPremiumTestData(false)
	personalData.CaixaModo = "realizado"
	personal := renderRelatoriosPremiumPage(t, personalData)

	if got := strings.Count(personal, "reports-export-cluster"); got != 2 {
		t.Fatalf("personal page must render 2 export clusters, got %d", got)
	}
	if got := strings.Count(personal, "ui-btn ui-btn--secondary ui-btn--sm"); got != 4 {
		t.Fatalf("personal page must render 4 premium export buttons, got %d", got)
	}
	if got := strings.Count(personal, "Inclui os valores completos."); got != 2 {
		t.Fatalf("personal page must expose the privacy hint twice, got %d", got)
	}

	for _, expected := range []string{
		`href="/relatorios/caixa.pdf?modo=realizado&mes=10&ano=2026"`,
		`href="/relatorios/export.csv?tipo=caixa&modo=realizado&mes=10&ano=2026"`,
		`href="/relatorios/export.pdf?tipo=extrato-movimentos&mes=10&ano=2026"`,
		`href="/relatorios/export.pdf?tipo=extrato-diario&mes=10&ano=2026"`,
		`href="/relatorios/export.csv?tipo=extrato-movimentos&mes=10&ano=2026"`,
		`href="/relatorios/export.csv?tipo=extrato-diario&mes=10&ano=2026"`,
		`aria-label="Exportar extrato em PDF"`,
		`aria-label="Exportar extrato em CSV"`,
		`/assets/js/dropdown.js`,
		`data-dropdown-toggle`,
		`data-dropdown-panel`,
	} {
		assertContains(t, personal, expected)
	}

	// Nenhum agrupamento de exportação pode voltar ao botão legado nem a
	// tamanhos/typografia antigos.
	for _, part := range strings.Split(personal, "reports-export-cluster")[1:] {
		window := part
		if len(window) > 1400 {
			window = window[:1400]
		}
		for _, forbidden := range []string{"cb-button", "text-[10px]", "h-8 px-3"} {
			if strings.Contains(window, forbidden) {
				t.Fatalf("export cluster must not use legacy styling %q", forbidden)
			}
		}
	}

	// Previsão preserva o modo nos endpoints.
	previsao := relatoriosPremiumTestData(false)
	previsao.IsCaixaPrevisao = true
	previsao.IsCaixaRealizado = false
	previsao.CaixaModo = "previsao"
	prevBody := renderRelatoriosPremiumPage(t, previsao)
	for _, expected := range []string{
		`caixa.pdf?modo=previsao&mes=10&ano=2026`,
		`export.csv?tipo=caixa&modo=previsao&mes=10&ano=2026`,
	} {
		assertContains(t, prevBody, expected)
	}
}

func TestRelatoriosBusinessExportOptionsArePreserved(t *testing.T) {
	business := relatoriosPremiumTestData(true)
	business.ActiveTab = "contabil"
	body := renderRelatoriosPremiumPage(t, business)

	if got := strings.Count(body, "reports-export-cluster"); got != 4 {
		t.Fatalf("business page must render 4 export clusters, got %d", got)
	}
	for _, expected := range []string{
		`href="/relatorios/contabil.pdf?mes=10&ano=2026"`,
		`href="/relatorios/contabil-resumo.csv?mes=10&ano=2026"`,
		`href="/relatorios/contabil-macros.csv?mes=10&ano=2026"`,
		`href="/relatorios/contabil-saldos.csv?mes=10&ano=2026"`,
		`href="/relatorios/contabil.zip?mes=10&ano=2026"`,
		`href="/relatorios/dre.pdf?mes=10&ano=2026"`,
		`href="/relatorios/export.csv?tipo=dre&ano=2026"`,
		`aria-label="Exportar relatório contábil em CSV"`,
		`reports-export-toggle`,
	} {
		assertContains(t, body, expected)
	}
	if got := strings.Count(body, "Inclui os valores completos."); got != 4 {
		t.Fatalf("business page must expose the privacy hint four times, got %d", got)
	}
}

func TestRelatoriosExportCSSKeepsTouchAndToolbarContracts(t *testing.T) {
	css := readTestFile(t, "assets/css/relatorios.css")
	for _, expected := range []string{
		`.reports-export-cluster`,
		`.reports-export-hint`,
		`.reports-export-toggle`,
		`min-width: 6.5rem`,
		`@media (max-width: 1023px)`,
		`.reports-export-cluster .ui-btn`,
		`min-height: 44px`,
		`.reports-export-menu .cb-meta-menu-item:focus-visible`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("relatorios.css missing export contract %q", expected)
		}
	}

	// A antecipação de toque é local; tabs/tabelas não são redefinidas aqui.
	if strings.Contains(css, `!important`) {
		t.Fatal("export controls must not introduce !important")
	}
}
