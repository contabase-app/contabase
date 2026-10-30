package handlers

import (
	"os"
	"strings"
	"testing"
)

func TestLancamentosF6EmptyStatePeriodWithoutMovements(t *testing.T) {
	html := renderLancamentosListHTML(t, LancamentosData{})
	for _, want := range []string{
		`Nenhuma movimentação neste período`,
		`Quando houver lançamentos, eles aparecerão aqui.`,
		`class="ui-empty lancamentos-empty"`,
	} {
		assertContains(t, html, want)
	}
	if strings.Contains(html, `lancamentos-empty__action`) {
		t.Fatal("period empty state must not offer a filter reset when there are no filters")
	}
	if strings.Contains(html, `id="lancamentos-invoices"`) {
		t.Fatal("empty period must not render an empty invoice panel")
	}
}

func TestLancamentosF6EmptyStateSearchWithoutResults(t *testing.T) {
	html := renderLancamentosListHTML(t, LancamentosData{
		Filters:          LancamentosFilters{Busca: "zzzz"},
		HasActiveFilters: true,
		FilterLabels:     []FilterLabelItem{{Key: "busca", Label: "zzzz"}},
		ClearFiltersURL:  "/lancamentos?mes=10&ano=2026",
	})
	for _, want := range []string{
		`Nenhum resultado para “zzzz”`,
		`A busca não encontrou lançamentos neste período. Tente outro termo ou limpe a busca.`,
		`data-onclick-ev="handleClearLancamentosFilters"`,
		`href="/lancamentos?mes=10&amp;ano=2026"`,
		`Limpar busca`,
		`data-has-active-filters="true"`,
	} {
		assertContains(t, html, want)
	}
	if strings.Contains(html, `Nenhum resultado com os filtros atuais`) {
		t.Fatal("search-only empty state must not be labelled as a filter-only result")
	}
}

func TestLancamentosF6EmptyStateFiltersWithoutResults(t *testing.T) {
	html := renderLancamentosListHTML(t, LancamentosData{
		Filters:          LancamentosFilters{Tipos: []string{"receita"}, Situacoes: []string{"agendado"}},
		HasActiveFilters: true,
		FilterLabels: []FilterLabelItem{
			{Key: "tipo", Label: "Receitas"},
			{Key: "situacao", Label: "Agendados"},
		},
		ClearFiltersURL: "/lancamentos?mes=10&ano=2026",
	})
	for _, want := range []string{
		`Nenhum resultado com os filtros atuais`,
		`Os filtros aplicados não retornaram lançamentos neste período. Ajuste ou redefina os critérios.`,
		`Limpar filtros`,
		`data-onclick-ev="handleClearLancamentosFilters"`,
	} {
		assertContains(t, html, want)
	}
	if strings.Contains(html, `Nenhum resultado para “`) {
		t.Fatal("filter-only empty state must not use the search-specific title")
	}
}

func TestLancamentosF6EmptyStateCombinedSearchAndFilters(t *testing.T) {
	html := renderLancamentosListHTML(t, LancamentosData{
		Filters:          LancamentosFilters{Busca: "zzzz", Tipos: []string{"receita"}},
		HasActiveFilters: true,
		FilterLabels: []FilterLabelItem{
			{Key: "tipo", Label: "Receitas"},
			{Key: "busca", Label: "zzzz"},
		},
		ClearFiltersURL: "/lancamentos?mes=10&ano=2026",
	})
	for _, want := range []string{
		`Nenhum resultado para “zzzz”`,
		`A busca combinada com os filtros atuais não retornou lançamentos.`,
		`Limpar busca e filtros`,
	} {
		assertContains(t, html, want)
	}
}

func TestLancamentosF6InvoiceOnlyAndTransactionOnlyStates(t *testing.T) {
	invoiceOnly := renderLancamentosListHTML(t, LancamentosData{
		Invoices: []InvoiceRow{
			{
				ID:             "inv-only",
				AccountName:    "Inter",
				ReferenceLabel: "Out/2026",
				DateLabel:      "07/10/2026",
				ItemCount:      2,
				FaturaURL:      "/cartoes/card/faturas?mes=10&ano=2026",
				PendingMoney:   MoneyDisplay{Reais: "10", Cents: ",00"},
				BadgeLabel:     "Fechada",
			},
		},
	})
	for _, want := range []string{
		`id="lancamentos-invoices"`,
		`Nenhuma movimentação individual`,
		`As faturas consolidadas do período estão exibidas acima.`,
	} {
		assertContains(t, invoiceOnly, want)
	}
	if strings.Contains(invoiceOnly, `Nenhuma movimentação neste período`) {
		t.Fatal("invoice-only period must not claim the whole period is empty")
	}

	transactionOnly := renderLancamentosListHTML(t, LancamentosData{
		Transactions: []TransactionRow{
			{ID: "tx-only", Description: "Movimentação", Type: "EXPENSE", AmountDisplay: "- R$ 10,00", DateLabel: "5 OUT", PaymentStatus: "paid"},
		},
	})
	assertContains(t, transactionOnly, `id="tx-only"`)
	if strings.Contains(transactionOnly, `id="lancamentos-invoices"`) {
		t.Fatal("transaction-only period must not render an empty invoice panel")
	}
	if strings.Contains(transactionOnly, `lancamentos-empty`) {
		t.Fatal("transaction-only period must not render an empty state")
	}
}

func TestLancamentosF6ActiveFilterIndicatorAndSearchResetContract(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/lancamentos.html"))
	if err != nil {
		t.Fatalf("read lancamentos template: %v", err)
	}
	html := string(raw)
	for _, want := range []string{
		`data-filter-indicator`,
		`lancamentos-filter-indicator`,
		`data-has-active-filters`,
		`aria-label="Abrir filtros{{if .HasActiveFilters}}, filtros ativos{{end}}"`,
		`data-onclick-ev="handleClearLancamentosFilters"`,
		`data-clear-filters-url="{{$.ClearFiltersURL}}"`,
	} {
		assertContains(t, html, want)
	}

	jsRaw, err := os.ReadFile(resolveTemplatePath(t, "assets/js/lancamentos-filters.js"))
	if err != nil {
		t.Fatalf("read lancamentos-filters.js: %v", err)
	}
	js := string(jsRaw)
	for _, want := range []string{
		`function syncLancamentosSearchFromURL()`,
		`function resetLancamentosFilters(mes, ano)`,
		`data-clear-filters-url`,
		`target: '#main-content'`,
		`select: '#main-content'`,
		`swap: 'outerHTML'`,
		`htmx:historyRestore`,
		`window.addEventListener('popstate'`,
		`function trapLancamentosFilterFocus(event)`,
		`event.key === 'Escape'`,
		`focusLancamentosFilterPanel`,
	} {
		assertContains(t, js, want)
	}
	if strings.Contains(js, "input.value = ''") {
		t.Fatal("clear must re-render state instead of indiscriminately blanking the search input")
	}

	cssRaw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/lancamentos.css"))
	if err != nil {
		t.Fatalf("read lancamentos.css: %v", err)
	}
	css := string(cssRaw)
	for _, want := range []string{
		`.lancamentos-filter-indicator`,
		`.lancamentos-filter-trigger.is-active .lancamentos-filter-indicator`,
		`.lancamentos-empty`,
		`.lancamentos-empty--compact`,
		`.ui-row--invoice-summary.is-long-name .ui-row__main`,
		`flex: 1 1 0`,
		`-webkit-line-clamp: 3`,
		`min-height: 44px`,
	} {
		assertContains(t, css, want)
	}
}

func TestLancamentosF6InvoiceRowsDoNotExposeBrokenBulkDeleteSwipe(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/lancamentos.html"))
	if err != nil {
		t.Fatalf("read lancamentos template: %v", err)
	}
	html := string(raw)
	start := strings.Index(html, `{{define "invoice-row"}}`)
	if start < 0 {
		t.Fatal("invoice-row template not found")
	}
	block := html[start:]
	if strings.Contains(block, `data-swipe-left-url`) || strings.Contains(block, `data-swipe-bg-left`) {
		t.Fatal("invoice row must not advertise the transaction bulk-delete swipe")
	}
	if !strings.Contains(block, `data-swipe-right-url`) {
		t.Fatal("invoice row must keep its valid right swipe to open the invoice")
	}
}
