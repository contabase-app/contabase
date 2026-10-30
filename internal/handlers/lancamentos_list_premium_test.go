package handlers

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

// F4 — Premium transaction list. These tests freeze the swipe, payment,
// selection, edit/delete and privacy contracts while the visual layer moves
// from independent cards to one panel of financial rows.
func TestLancamentosTransactionRowPremiumContracts(t *testing.T) {
	paidAt := int64(1)
	row := TransactionRow{
		ID:                          "tx-f4-paid-income",
		Description:                 "Receita de consultoria",
		Type:                        "INCOME",
		Amount:                      125000,
		AmountDisplay:               "+ R$ 1.250,00",
		DateLabel:                   "5 OUT",
		AccountName:                 "Conta Principal",
		CategoryName:                "Consultoria",
		CategoryColor:               "#22C55E",
		CategoryIcon:                "briefcase",
		PaymentStatus:               "paid",
		PaymentTone:                 "success",
		PaymentIcon:                 "check-circle-2",
		PaymentTitle:                "Recebido — toque para remover recebimento",
		PaymentConfirm:              "Remover recebimento?",
		PaymentAccountID:            "acc-1",
		PaymentAccountChangeAllowed: true,
		PaidAt:                      &paidAt,
		PaidAtDisplay:               "05/10/2026",
		CanGenerateReceipt:          true,
		HasInstallmentInfo:          true,
		InstallmentNumber:           2,
		TotalInstallments:           6,
	}

	html := renderLancamentoRowHTML(t, row)

	for _, want := range []string{
		`id="tx-tx-f4-paid-income"`,
		`class="tx-row`,
		`data-tx-id="tx-f4-paid-income"`,
		`data-payment-id="tx-f4-paid-income"`,
		`data-payment-context="lancamentos"`,
		`data-payment-post-url="/transacoes/tx-f4-paid-income/status-pagamento"`,
		`data-payment-target="#tx-tx-f4-paid-income"`,
		`data-payment-swap="outerHTML"`,
		`data-payment-description="Receita de consultoria"`,
		`data-payment-amount="+ R$ 1.250,00"`,
		`data-payment-type="INCOME"`,
		`data-payment-account-id="acc-1"`,
		`data-payment-account-name="Conta Principal"`,
		`data-payment-account-change-allowed="true"`,
		`data-swipe-row="true"`,
		`data-swipe-right-url="/transacoes/tx-f4-paid-income/status-pagamento"`,
		`data-swipe-right-method="POST"`,
		`data-swipe-right-target="#tx-tx-f4-paid-income"`,
		`data-swipe-right-swap="outerHTML"`,
		`data-swipe-right-confirm="Remover recebimento?"`,
		`data-swipe-bg-left`,
		`data-swipe-bg-right`,
		`class="lancamentos-swipe-bg is-reverse"`,
		`data-swipe-content`,
		`class="ui-row ui-row--transaction"`,
		`data-tx-checkbox`,
		`aria-label="Selecionar Receita de consultoria"`,
		`class="tx-status tx-status-success ui-row__status"`,
		`hx-post="/transacoes/tx-f4-paid-income/status-pagamento"`,
		`hx-target="closest .tx-row"`,
		`hx-confirm="Remover recebimento?"`,
		`aria-label="Recebido — toque para remover recebimento — Receita de consultoria"`,
		`class="tx-edit-trigger ui-row__main ui-row__main--button"`,
		`hx-get="/transacoes/tx-f4-paid-income"`,
		`hx-target="#bottom-sheet-container"`,
		`hx-swap="innerHTML"`,
		`hx-push-url="false"`,
		`<span class="sr-only">Editar lançamento: </span>`,
		`title="Receita de consultoria"`,
		`class="ui-row__meta"`,
		`class="lancamentos-row-category" data-cb-color="#22C55E" title="Consultoria"`,
		`class="ui-row__note is-paid"`,
		`Pago em 05/10/2026`,
	} {
		assertContains(t, html, want)
	}

	// The amount block is outside the edit trigger and carries privacy.
	assertContains(t, html, `class="ui-row__amount is-income" data-sensitive`)
	assertContains(t, html, `class="lancamentos-row-date">5 OUT</span>`)
	assertContains(t, html, `ui-badge ui-badge--neutral">2/6</span>`)
	assertContains(t, html, `class="tx-desktop-actions"`)
	assertContains(t, html, `aria-label="Editar Receita de consultoria"`)
	assertContains(t, html, `aria-label="Recibo de Receita de consultoria"`)
	assertContains(t, html, `aria-label="Excluir Receita de consultoria"`)
	assertContains(t, html, `data-row-delete="tx-f4-paid-income"`)

	// The edit trigger must not carry a label that omits the visible metadata.
	editStart := strings.Index(html, `class="tx-edit-trigger`)
	if editStart < 0 {
		t.Fatalf("edit trigger missing in row:\n%s", html)
	}
	tagStart := strings.LastIndex(html[:editStart], "<button")
	tagEnd := strings.Index(html[editStart:], ">")
	if tagStart < 0 || tagEnd < 0 {
		t.Fatalf("could not inspect edit trigger opening tag:\n%s", html)
	}
	openingTag := html[tagStart : editStart+tagEnd+1]
	if strings.Contains(openingTag, "aria-label") {
		t.Fatalf("edit trigger must use visible text + sr-only action prefix, not aria-label:\n%s", openingTag)
	}

	if strings.Contains(html, "tx-content") {
		t.Fatal("legacy card wrapper tx-content must not return")
	}
	if strings.Contains(html, "cb-status-chip") {
		t.Fatal("legacy cb-status-chip must not return in transaction rows")
	}
}

func TestLancamentosTransactionRowStatesAndSwipeDirections(t *testing.T) {
	pending := TransactionRow{
		ID:                 "tx-f4-pending",
		Description:        "Conta de energia",
		Type:               "EXPENSE",
		AmountDisplay:      "- R$ 125,00",
		DateLabel:          "30 OUT",
		AccountName:        "Conta Principal",
		PaymentStatus:      "pending",
		PaymentTone:        "warning",
		PaymentIcon:        "clock",
		PaymentTitle:       "Pendente — toque para marcar como pago",
		IsPending:          true,
		HasInstallmentInfo: true,
		InstallmentNumber:  1,
		TotalInstallments:  1,
	}
	pendingHTML := renderLancamentoRowHTML(t, pending)
	for _, want := range []string{
		`class="tx-status tx-status-warning ui-row__status"`,
		`data-payment-confirm`,
		`data-swipe-right-payment-confirm="true"`,
		`class="lancamentos-swipe-bg is-pay"`,
		`class="ui-badge ui-badge--warning">Pendente</span>`,
		`class="ui-row__amount is-expense" data-sensitive`,
	} {
		assertContains(t, pendingHTML, want)
	}
	if strings.Contains(pendingHTML, `hx-post="/transacoes/tx-f4-pending/status-pagamento"`) {
		t.Fatal("pending row must open payment confirmation, not post directly")
	}

	overdue := TransactionRow{
		ID:            "tx-f4-overdue",
		Description:   "Fornecedor atrasado",
		Type:          "EXPENSE",
		AmountDisplay: "- R$ 90,00",
		DateLabel:     "20 OUT",
		AccountName:   "Conta Principal",
		PaymentStatus: "pending",
		PaymentTone:   "danger",
		PaymentIcon:   "triangle-alert",
		PaymentTitle:  "Atrasado — toque para marcar como pago",
		IsPending:     true,
		IsOverdue:     true,
	}
	overdueHTML := renderLancamentoRowHTML(t, overdue)
	for _, want := range []string{
		`class="tx-row overdue"`,
		`class="lancamentos-swipe-bg is-pay"`,
		`class="ui-badge ui-badge--danger">Vencido</span>`,
		`class="tx-status tx-status-danger ui-row__status"`,
	} {
		assertContains(t, overdueHTML, want)
	}

	scheduled := TransactionRow{
		ID:                        "tx-f4-scheduled",
		Description:               "Assinatura",
		Type:                      "EXPENSE",
		AmountDisplay:             "- R$ 85,00",
		DateLabel:                 "29 OUT",
		AccountName:               "Conta Principal",
		PaymentStatus:             "pending",
		PaymentTone:               "scheduled",
		PaymentIcon:               "timer",
		PaymentTitle:              "Agendado — toque para confirmar pagamento",
		IsPending:                 true,
		IsScheduled:               true,
		ScheduledPaymentAtDisplay: "05/11/2026",
	}
	scheduledHTML := renderLancamentoRowHTML(t, scheduled)
	for _, want := range []string{
		`class="ui-badge ui-badge--accent">Agendado</span>`,
		`class="ui-row__note is-scheduled"`,
		`Agendado para 05/11/2026`,
		`class="tx-status tx-status-scheduled ui-row__status"`,
	} {
		assertContains(t, scheduledHTML, want)
	}

	transfer := TransactionRow{
		ID:                     "tx-f4-transfer",
		Description:            "Transferência entre contas",
		Type:                   "TRANSFER",
		AmountDisplay:          "R$ 300,00",
		DateLabel:              "7 OUT",
		AccountName:            "Conta A",
		DestinationAccountName: "Conta B",
		PaymentStatus:          "paid",
		PaymentTone:            "success",
		PaymentIcon:            "arrow-left-right",
		PaymentTitle:           "Transferência confirmada",
	}
	transferHTML := renderLancamentoRowHTML(t, transfer)
	for _, want := range []string{
		`class="ui-row__amount is-transfer" data-sensitive`,
		`class="lancamentos-row-transfer"`,
		`title="Conta A">Conta A</span>`,
		`<span class="sr-only">para</span>`,
		`title="Conta B">Conta B</span>`,
	} {
		assertContains(t, transferHTML, want)
	}
	if strings.Contains(transferHTML, `ui-badge--warning`) || strings.Contains(transferHTML, `ui-badge--danger`) {
		t.Fatal("transfer must not inherit income/expense or lifecycle status semantics")
	}
}

func TestLancamentosTransactionRowLongDescriptionKeepsFullAccessibleText(t *testing.T) {
	longDescription := strings.Repeat("Descricao longa para validar truncamento visual ", 3)
	row := TransactionRow{
		ID:            "tx-f4-long",
		Description:   longDescription,
		Type:          "EXPENSE",
		AmountDisplay: "- R$ 10,00",
		DateLabel:     "10 OUT",
		AccountName:   "Conta Principal",
		PaymentStatus: "paid",
		PaymentTone:   "success",
		PaymentIcon:   "check-circle-2",
		PaymentTitle:  "Pago",
	}
	html := renderLancamentoRowHTML(t, row)
	assertContains(t, html, `title="`+longDescription+`"`)
	assertContains(t, html, `>`+longDescription+`</span>`)
	if strings.Count(html, longDescription) < 2 {
		t.Fatal("full description must survive in the visible text and title attribute")
	}
}

func TestLancamentosTransactionRowSeriesAndReceiptConditions(t *testing.T) {
	row := TransactionRow{
		ID:                 "tx-f4-series",
		Description:        "Assinatura recorrente",
		Type:               "EXPENSE",
		AmountDisplay:      "- R$ 49,90",
		DateLabel:          "10 OUT",
		AccountName:        "Conta Principal",
		PaymentStatus:      "paid",
		PaymentTone:        "success",
		PaymentIcon:        "check-circle-2",
		PaymentTitle:       "Pago",
		IsSeries:           true,
		HasInstallmentInfo: true,
		InstallmentNumber:  1,
		TotalInstallments:  1,
	}
	html := renderLancamentoRowHTML(t, row)
	for _, want := range []string{
		`data-is-series="true"`,
		`data-row-is-series="true"`,
		`class="ui-badge ui-badge--accent">Recorrente</span>`,
	} {
		assertContains(t, html, want)
	}
	if strings.Contains(html, `aria-label="Recibo`) {
		t.Fatal("receipt action must be omitted when CanGenerateReceipt is false")
	}
	if strings.Contains(html, `aria-label="Editar`) == false {
		t.Fatal("desktop edit action must remain available")
	}
}

func TestLancamentosListPanelDateGroupsAndBulkContracts(t *testing.T) {
	data := LancamentosData{
		Transactions: []TransactionRow{
			{ID: "tx-a", Description: "Primeiro", Type: "INCOME", AmountDisplay: "+ R$ 10,00", DateLabel: "5 OUT", PaymentStatus: "paid"},
			{ID: "tx-b", Description: "Segundo", Type: "EXPENSE", AmountDisplay: "- R$ 20,00", DateLabel: "5 OUT", PaymentStatus: "paid"},
			{ID: "tx-c", Description: "Terceiro", Type: "EXPENSE", AmountDisplay: "- R$ 30,00", DateLabel: "6 OUT", PaymentStatus: "pending"},
		},
	}
	html := renderLancamentosListHTML(t, data)

	for _, want := range []string{
		`class="ui-panel ui-list-panel"`,
		`aria-label="Lançamentos do período"`,
		`class="ui-list-panel__head lancamentos-list-head"`,
		`Ordenado por vencimento`,
		`data-bulk-toggle-mode>Selecionar`,
		`data-bulk-toggle-all`,
		`data-bulk-count aria-live="polite"`,
		`data-bulk-bar`,
		`data-bulk-select`,
		`class="lancamentos-day"`,
		`<h3 class="lancamentos-day__label">5 OUT</h3>`,
		`<h3 class="lancamentos-day__label">6 OUT</h3>`,
		`id="lancamentos-table-body"`,
	} {
		assertContains(t, html, want)
	}

	if got := strings.Count(html, `class="lancamentos-day"`); got != 2 {
		t.Fatalf("date group headers = %d, want 2", got)
	}
	if got := strings.Count(html, `class="tx-row`); got != 3 {
		t.Fatalf("transaction rows = %d, want 3", got)
	}
	first := strings.Index(html, "5 OUT")
	second := strings.Index(html, "6 OUT")
	third := strings.Index(html, "Terceiro")
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Fatalf("date grouping/order broken: first=%d second=%d third=%d", first, second, third)
	}
	if strings.Contains(html, "tx-content") {
		t.Fatal("legacy tx-content must not appear in the list")
	}
}

func TestLancamentosListEmptyStateStaysOutsidePremiumPanel(t *testing.T) {
	html := renderLancamentosListHTML(t, LancamentosData{})
	assertContains(t, html, `ui-empty lancamentos-empty`)
	assertContains(t, html, `Nenhuma movimentação neste período`)
	if strings.Contains(html, `ui-panel ui-list-panel`) {
		t.Fatal("empty list must not be wrapped in the transaction panel")
	}
}

func TestLancamentosTransactionListCSSContract(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/lancamentos.css"))
	if err != nil {
		t.Fatalf("read lancamentos.css: %v", err)
	}
	css := string(raw)
	for _, want := range []string{
		`.ui-row--transaction`,
		`display: flex`,
		`border-bottom: 1px solid var(--ui-border)`,
		`.lancamentos-list-head`,
		`.lancamentos-day`,
		`.lancamentos-row-amount`,
		`font-variant-numeric: tabular-nums`,
		`.lancamentos-swipe-bg.is-pay`,
		`var(--ui-success)`,
		`var(--ui-danger)`,
		`var(--ui-warning)`,
		`var(--ui-accent-strong)`,
		`-webkit-line-clamp: 2`,
		`@media (max-width: 767px)`,
		`:has(.lancamentos-row-check:checked)`,
		`.bulk-mode .lancamentos-list-head__label`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("CSS da lista não contém %q", want)
		}
	}
	for _, legacy := range []string{
		`.tx-content`,
		`.tx-action-btn`,
		`pulse-overdue-icon`,
		// F8: legacy pre-F5 invoice row styles removed with the component.
		`.invoice-content`,
		`.invoice-copy`,
		`.invoice-summary-primary`,
		`.invoice-summary-detail`,
		`.line-clamp-2`,
		// F8: pre-F7 sheet animation override removed; sheet-form.css owns it.
		`@keyframes sheetIn`,
	} {
		if strings.Contains(css, legacy) {
			t.Fatalf("CSS legado %q ainda existe", legacy)
		}
	}
}

func renderLancamentoRowHTML(t *testing.T, row TransactionRow) string {
	t.Helper()
	tpl := template.Must(template.New("lancamento-row-f4").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(resolveTemplatePath(t, "templates/pages/lancamentos.html")))
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "lancamento-row", row); err != nil {
		t.Fatalf("execute lancamento-row: %v", err)
	}
	return strings.ReplaceAll(buf.String(), "&#43;", "+")
}

func renderLancamentosListHTML(t *testing.T, data LancamentosData) string {
	t.Helper()
	tpl := template.Must(template.New("lancamentos-list-f4").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
	))
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "lancamentos-list", data); err != nil {
		t.Fatalf("execute lancamentos-list: %v", err)
	}
	return strings.ReplaceAll(buf.String(), "&#43;", "+")
}
