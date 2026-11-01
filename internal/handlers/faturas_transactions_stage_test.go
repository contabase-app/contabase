package handlers

import (
	"bytes"
	"html"
	"html/template"
	"os"
	"strings"
	"testing"
)

func renderInvoiceTransactionsForStageD(t *testing.T, data FaturaData) string {
	t.Helper()
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/faturas.html"))
	if err != nil {
		t.Fatalf("read faturas template: %v", err)
	}
	tpl := template.Must(template.New("faturas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(raw)))
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "invoice-transactions", data); err != nil {
		t.Fatalf("execute invoice-transactions: %v", err)
	}
	return out.String()
}

func stageDTransactionRow(index int) TransactionRow {
	base := TransactionRow{
		ID:                 "tx-" + string(rune('a'+index)),
		DateLabel:          "27 Out",
		TimeDisplay:        "09:00",
		Description:        "Compra " + string(rune('A'+index)),
		CategoryName:       "Casa",
		CategoryIcon:       "home",
		CategoryColor:      "amber",
		AmountDisplay:      "- R$ 39,99",
		AmountInput:        "39,99",
		AmountClass:        "text-[#FE414F]",
		Type:               "EXPENSE",
		UpdatedAt:          100 + int64(index),
		ListIndex:          index,
		HasInstallmentInfo: true,
		InstallmentNumber:  2,
		TotalInstallments:  6,
		PaymentStatus:      "pending",
	}
	return base
}

func stageDInvoiceData() FaturaData {
	rows := []TransactionRow{
		stageDTransactionRow(0),
		stageDTransactionRow(1),
		stageDTransactionRow(2),
		stageDTransactionRow(3),
	}
	// Refunded row
	rows[1].HasRefund = true
	rows[1].IsSeries = true
	rows[1].RefundedAmount = 1000
	rows[1].RefundedAmountDisplay = "+ R$ 10,00"
	rows[1].EffectiveAmountDisplay = "R$ 29,99"
	rows[1].RefundDescriptions = "Estorno parcial"
	// Income row
	rows[2].Type = "INCOME"
	rows[2].AmountDisplay = "+ R$ 50,00"
	// Projected recurring row
	rows[3].IsProjected = true
	rows[3].IsSeries = true
	rows[3].TotalInstallments = 1
	rows[3].AmountDisplay = "- R$ 12,90"
	rows[3].AmountClass = "text-indigo-300"
	rows[3].PaymentStatus = "pending"
	rows[3].CategoryIcon = "repeat"

	return FaturaData{
		InvoiceID:             "invoice-stage-d",
		NextReference:         "2026-12",
		CanMutateItems:        true,
		InitialVisibleItems:   2,
		SortOrder:             "asc",
		SortRecentURL:         "/faturas?cartao=card&mes=11",
		SortChronologicalURL:  "/faturas?cartao=card&mes=11&ordem=asc",
		VisibleItemCountLabel: "Você está vendo 2 de 4 lançamentos.",
		HiddenItemCountLabel:  "Mais 2 lançamentos disponíveis.",
		LoadMoreButtonLabel:   "Carregar mais lançamentos",
		Transactions:          rows,
		TransactionGroups: []TransactionDayGroup{
			{DateLabel: "27 Out", FirstIndex: 0, Transactions: rows[:2]},
			{DateLabel: "26 Out", FirstIndex: 2, Transactions: rows[2:]},
		},
	}
}

func TestInvoiceTransactionsUseCanonicalListAndPreserveContracts(t *testing.T) {
	rendered := renderInvoiceTransactionsForStageD(t, stageDInvoiceData())
	unescaped := html.UnescapeString(rendered)

	for _, expected := range []string{
		`id="invoice-transactions"`,
		`class="ui-panel cb-invoice-transactions"`,
		`class="ui-tabs cb-invoice-sort"`,
		`ui-tab is-active`,
		`aria-current="true"`,
		`aria-label="Ordenar por compras recentes"`,
		`>Recentes</span>`,
		`aria-label="Ordenar por ordem cronológica"`,
		`>Cronológica</span>`,
		`4 lançamentos`,
		`cb-invoice-day-label`,
		`class="ui-row ui-row--invoice cb-invoice-row"`,
		`data-show-more-group="faturas"`,
		`data-show-more-initial="2"`,
		`data-show-more-total="4"`,
		`data-show-more-one-way`,
		`data-show-more-item="0"`,
		`data-show-more-item="2"`,
		`data-show-more-toggle`,
		`data-onclick-ev="toggleShowMore"`,
		"Você está vendo 2 de 4 lançamentos.",
		"Carregar mais lançamentos",
		// Swipe contracts (unchanged).
		`data-swipe-row="true"`,
		`data-swipe-left-url="/transacoes/tx-b?invoice_id=invoice-stage-d"`,
		`data-swipe-left-method="DELETE"`,
		`data-swipe-left-confirm="Remover este lançamento?"`,
		`data-swipe-right-url="/transacoes/tx-a/mover-fatura"`,
		`data-swipe-right-method="POST"`,
		`"expected_invoice_id":"invoice-stage-d"`,
		`"expected_destination_reference":"2026-12"`,
		`"expected_move_scope":"chain"`,
		// Edit, refund and move actions.
		`hx-get="/transacoes/tx-a/editar?invoice_id=invoice-stage-d"`,
		`hx-target="#bottom-sheet-container"`,
		`hx-push-url="false"`,
		`hx-get="/transacoes/tx-b/reembolsos"`,
		`hx-get="/transacoes/tx-b/reembolso"`,
		`hx-post="/transacoes/tx-a/mover-fatura"`,
		`hx-indicator="#htmx-indicator"`,
		`data-tooltip-text="Gerenciar reembolsos"`,
		`data-tooltip-text="Informar reembolso"`,
		`data-tooltip-text="Mover para próxima fatura"`,
		// Badges and values.
		`2/6`,
		`Reembolso <span data-sensitive>+ R$ 10,00</span>`,
		`Líquido <span data-sensitive>R$ 29,99</span>`,
		`Previsto`,
		`Recorrente`,
		`- R$ 39,99`,
		`+ R$ 50,00`,
		`- R$ 12,90`,
		`data-sensitive`,
		// Swipe hint text (gesture mapping contract).
		"para a Esquerda (&larr;) para mover",
		"parcelas permitem escolher o escopo",
		"para a Direita (&rarr;) para apagar",
	} {
		if !strings.Contains(rendered, expected) && !strings.Contains(unescaped, expected) {
			t.Fatalf("transactions missing %q", expected)
		}
	}
	if got := strings.Count(rendered, `class="cb-v2-swipe-hint`); got != 1 {
		t.Fatalf("swipe hint count = %d, want 1", got)
	}

	// Amount semantics come from the transaction type, not the legacy hex class.
	for _, expected := range []string{
		`ui-row__amount is-expense`,
		`ui-row__amount is-income`,
		`ui-row__amount is-projected`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("transactions missing amount semantic %q", expected)
		}
	}

	// Legacy presentation and literal palettes are gone.
	for _, legacy := range []string{
		`text-[#FE414F]`, `text-indigo-300`,
		`bg-amber-500/10`, `text-amber-600`, `bg-{{.CategoryColor}}`, `text-{{.CategoryColor}}`,
		`font-black`, `cb-status-chip`, `rounded-xl border border-[var(--cb-border-subtle)] bg-[var(--cb-surface-2)] p-3`,
	} {
		if strings.Contains(unescaped, legacy) {
			t.Fatalf("transactions kept legacy presentation %q", legacy)
		}
	}

	// Projected rows are visually distinct and never gain confirmed-row actions.
	projectedStart := strings.Index(rendered, "is-projected")
	if projectedStart < 0 {
		t.Fatal("projected row missing")
	}
	projectedBlock := rendered[projectedStart:]
	if strings.Contains(projectedBlock, `data-swipe-row="true"`) {
		t.Fatal("projected rows must not be swipe/confirmed rows")
	}
}

func TestInvoiceTransactionsEmptyStateAndOOB(t *testing.T) {
	html := renderInvoiceTransactionsForStageD(t, FaturaData{
		InvoiceID:            "invoice-empty",
		Transactions:         nil,
		OOB:                  true,
		SortOrder:            "desc",
		SortRecentURL:        "/faturas?cartao=card",
		SortChronologicalURL: "/faturas?cartao=card&ordem=asc",
	})
	for _, expected := range []string{
		`id="invoice-transactions" hx-swap-oob="outerHTML"`,
		`class="ui-empty cb-invoice-empty"`,
		`Nenhum lançamento nesta fatura ainda`,
		`ui-empty__title`,
		`ui-empty__text`,
	} {
		if !strings.Contains(html, expected) {
			t.Fatalf("empty transactions missing %q", expected)
		}
	}
	if strings.Contains(html, `data-show-more-toggle`) {
		t.Fatal("empty transactions must not render the expansion control")
	}
	if strings.Contains(html, `cb-v2-swipe-hint`) {
		t.Fatal("empty transactions must not render the swipe hint")
	}
}
