package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"strings"
	"testing"
	"time"
)

func previewTestTemplate(t *testing.T) *template.Template {
	t.Helper()
	tpl := template.New("preview").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"sub":       func(a, b int) int { return a - b },
	}))
	for _, path := range []string{
		"templates/pages/dashboard_preview.html",
		"templates/components/dashboard_preview_cards.html",
		"templates/components/credit_card.html",
		"templates/components/app_shell.html",
	} {
		if _, err := tpl.Parse(readTestFile(t, path)); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	}
	return tpl
}

func seedDashboardPreviewScenario(t *testing.T, now time.Time) (*sql.DB, f1Base) {
	t.Helper()
	db := openTestDB(t)
	base := seedF1Base(t, db, "ws-preview")
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-preview', ?, 20, 10, 1000000)
	`, base.card)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-preview', ?, ?, 300000)
	`, base.ws, base.category)
	addF1Account(t, db, "inv-preview", base.ws, "INVESTMENT", 250000, now.Unix())
	// A second workspace proves the preview keeps the tenant filter.
	other := seedF1Base(t, db, "ws-preview-other")
	addF1Account(t, db, "inv-preview-other", other.ws, "INVESTMENT", 999999, now.Unix())

	addF1Invoice(t, db, "inv-preview-card", base.card, "2026-11", "OPEN", 0, now.Unix())
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 500000, Status: "paid", Description: "Receita sintética", CreatedAt: now.Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, CategoryID: base.category, Type: "EXPENSE", Amount: 200000, Status: "paid", Description: `<script>alert("x")</script>`, CreatedAt: now.Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, CategoryID: base.category, Type: "EXPENSE", Amount: 30000, Status: "pending", Description: "Conta futura", DueDate: now.AddDate(0, 0, 3).Unix(), CreatedAt: now.Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-preview-card", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra no cartão", CreatedAt: now.Unix()})
	return db, base
}

func TestDashboardPreviewDataUsesValidatedDashboardValues(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")

	if !data.Invested.Available {
		t.Fatalf("invested indicator unavailable: %+v", data.Invested)
	}
	if got := data.Invested.Money.rawCents(); got != 250000 {
		t.Fatalf("invested = %d, want 250000 (only the active workspace)", got)
	}
	if data.Invested.AccountCount != 1 {
		t.Fatalf("invested account count = %d, want 1", data.Invested.AccountCount)
	}
	if !data.BalanceComposition.Available {
		t.Fatalf("balance composition unavailable: %+v", data.BalanceComposition)
	}
	if got := data.BalanceComposition.Total.rawCents(); got != 260000 {
		t.Fatalf("composition total = %d, want 260000", got)
	}
	if got := data.BalanceComposition.Invested.rawCents(); got != 250000 {
		t.Fatalf("composition invested = %d, want 250000", got)
	}
	if got := data.BalanceComposition.Other.rawCents(); got != 10000 {
		t.Fatalf("composition other = %d, want 10000", got)
	}
	if data.BalanceComposition.InvestedPercent != 96 || data.BalanceComposition.OtherPercent != 4 {
		t.Fatalf("composition percent = %d/%d, want 96/4", data.BalanceComposition.InvestedPercent, data.BalanceComposition.OtherPercent)
	}
	if data.BalanceComposition.InvestedAccountCount != 1 || data.BalanceComposition.OtherAccountCount != 1 {
		t.Fatalf("composition account counts = %d/%d, want 1/1", data.BalanceComposition.InvestedAccountCount, data.BalanceComposition.OtherAccountCount)
	}
	if !strings.Contains(data.BalanceComposition.AriaLabel, "96% em investimentos") || !strings.Contains(data.BalanceComposition.AriaLabel, "3,85% nas demais contas") {
		t.Fatalf("composition aria label = %q", data.BalanceComposition.AriaLabel)
	}
	if !data.Financial.Available || data.Financial.Error {
		t.Fatalf("financial view unavailable: %+v", data.Financial)
	}
	if len(data.Financial.Months) != 3 {
		t.Fatalf("financial months = %d, want 3", len(data.Financial.Months))
	}
	selected := data.Financial.Selected
	if selected.Label != "Novembro/2026" || !selected.IsCurrent || selected.StateLabel != "Projeção completa" {
		t.Fatalf("selected financial month = %+v", selected)
	}
	if got := selected.RealizedIncome.rawCents(); got != 500000 {
		t.Fatalf("financial realized income = %d, want 500000", got)
	}
	if got := selected.RealizedExpense.rawCents(); got != 200000 {
		t.Fatalf("financial realized expense = %d, want 200000", got)
	}
	if got := selected.PendingExpense.rawCents(); got != 80000 {
		t.Fatalf("financial pending expense = %d, want 80000", got)
	}
	if got := selected.ProjectedNet.rawCents(); got != 220000 {
		t.Fatalf("financial projected net = %d, want 220000", got)
	}
	if data.Financial.Months[1].Label != "Dezembro/2026" || data.Financial.Months[2].Label != "Janeiro/2027" {
		t.Fatalf("financial horizon labels = %q/%q", data.Financial.Months[1].Label, data.Financial.Months[2].Label)
	}
	if data.CardsPeriodAware {
		t.Fatal("current month cards must remain current-position cards")
	}
	if !data.CashFlow.Available || !data.CashFlow.HasData {
		t.Fatalf("cash flow unavailable or empty: %+v", data.CashFlow)
	}
	if len(data.CashFlow.Months) != 6 {
		t.Fatalf("cash flow months = %d, want 6", len(data.CashFlow.Months))
	}
	if len(data.CashFlow.GridLines) != 3 {
		t.Fatalf("cash flow grid lines = %d, want 3", len(data.CashFlow.GridLines))
	}
	if !data.CashFlow.Months[len(data.CashFlow.Months)-1].IsCurrent {
		t.Fatal("last cash flow month must be the current month")
	}
	for i, line := range data.CashFlow.GridLines {
		if line.Label == "" || line.Y <= 0 {
			t.Fatalf("grid line %d = %+v", i, line)
		}
	}
	if got := data.CashFlow.TotalIncome.rawCents(); got != 500000 {
		t.Fatalf("cash flow income = %d, want 500000", got)
	}
	if got := data.CashFlow.TotalExpense.rawCents(); got != 200000 {
		t.Fatalf("cash flow expense = %d, want 200000", got)
	}
	if len(data.CategoryBreakdown) == 0 {
		t.Fatal("category breakdown must be built from validated categories")
	}
	if len(data.PreviewCards) != 1 {
		t.Fatalf("preview cards = %d, want 1", len(data.PreviewCards))
	}
	if len(data.PreviewAccounts) < 2 {
		t.Fatalf("preview accounts = %d, want checking and investment", len(data.PreviewAccounts))
	}
	wide := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "12m", "")
	if len(wide.CashFlow.Months) != 12 {
		t.Fatalf("12m cash flow months = %d, want 12", len(wide.CashFlow.Months))
	}
	if wide.CashFlow.Range != "12m" || !wide.CashFlow.RangeOptions[1].Active {
		t.Fatalf("12m range state = %+v", wide.CashFlow)
	}
}

func TestDashboardPreviewTemplateRendersMobileOrderAndRealData(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	SortDashboardCardsByPaymentPriority(data.Cards, now)
	data.PreviewCards = buildPreviewCards(data.Cards)

	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render preview: %v", err)
	}
	body := out.String()

	sections := []string{
		`data-cbp-section="balance"`,
		`data-cbp-section="finance"`,
		`data-cbp-section="cashflow"`,
		`data-cbp-section="accounts"`,
		`data-cbp-section="commitments"`,
		`data-cbp-section="cards"`,
		`data-cbp-section="categories"`,
		`data-cbp-section="goals"`,
	}
	last := -1
	for _, section := range sections {
		index := strings.Index(body, section)
		if index < 0 {
			t.Fatalf("missing section %s", section)
		}
		if index <= last {
			t.Fatalf("section %s is out of mobile order", section)
		}
		last = index
	}

	for _, required := range []string{
		"data-v2-carousel",
		"data-v2-carousel-prev",
		"data-v2-carousel-next",
		"data-wallet-position",
		"data-v2-action-tabs",
		"cbp-progress",
		"cbp-category-progress",
		"Composição do saldo",
		"Investimentos",
		"Demais contas",
		"data-cbp-flow-plot",
		"data-cbp-flow-toggle",
		"data-cbp-flow-panel",
		"cbp-flow-tooltip",
		"cb-card-face",
		"cb-card-progress",
		`data-card-count="1"`,
		"data-wallet-add-card",
		"Adicionar cartão",
		"cbp-month-filter",
		"data-flow-net-tone",
		"cbp-flow-grid",
		"cbp-flow-current-band",
		"cbp-commitment-summary",
		"Total a pagar",
		"Próximos 7 dias + atrasados",
		"cbp-period-badge is-selected",
		"incluídos no Saldo total",
		"Fluxo de caixa",
		"Metas de gastos",
		"Visão financeira",
		"Entradas",
		"A receber",
		"Saídas",
		"A pagar",
		"Resultado realizado",
		"Resultado projetado",
		"Projeção completa",
		"Meses da visão financeira",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("rendered preview missing %q", required)
		}
	}

	for _, forbidden := range []string{
		`href="#"`,
		"Open Finance",
		"criptografados de ponta a ponta",
		"339.852,17",
		"Muito acima do previsto",
		"Acima do previsto",
		"Sem meta definida",
		"Resultado dos pendentes",
		"Mês selecionado e próximos dois",
		"cbp-finance-months",
		"cbp-donut",
		`data-cbp-section="transactions"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("rendered preview must not contain %q", forbidden)
		}
	}
}

// TestDashboardPreviewSSRCanonicalOrder trava o contrato da B10.1: o SSR
// entrega os cards na ordem canônica mobile e deixa os placeholders das regiões
// vazios; dashboard-preview-layout.js monta o desktop usando os mesmos nós e
// devolve a ordem canônica em <=1199px. As duas ordens responsivas são
// verificadas nas evidências Playwright.
func TestDashboardPreviewSSRCanonicalOrder(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	SortDashboardCardsByPaymentPriority(data.Cards, now)
	data.PreviewCards = buildPreviewCards(data.Cards)
	data.LayoutPending = true

	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render preview: %v", err)
	}
	body := out.String()

	for _, required := range []string{
		"cbp-grid--contract",
		"is-layout-pending",
		`data-cbp-region="left"`,
		`data-cbp-region="right"`,
		"data-cbp-region-pair",
		"/assets/js/dashboard-preview-layout.js",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("rendered preview missing %q", required)
		}
	}

	canonical := []string{
		"balance",
		"finance",
		"cashflow",
		"accounts",
		"commitments",
		"cards",
		"categories",
		"goals",
	}

	last := -1
	for position, section := range canonical {
		index := strings.Index(body, `data-cbp-section="`+section+`"`)
		if index < 0 {
			t.Fatalf("missing section %q", section)
		}
		if index <= last {
			t.Fatalf("SSR order changed: %q should be at position %d of the canonical order", section, position+1)
		}
		last = index
	}
}

func TestDashboardPreviewCategoriesUseExactRemainder(t *testing.T) {
	items := []DashboardCategoryItem{
		{Name: "A", AmountCents: 5000, SharePercent: 50, BarPercent: 50, ShareLabel: "50%"},
		{Name: "B", AmountCents: 3000, SharePercent: 30, BarPercent: 30, ShareLabel: "30%"},
	}
	breakdown := buildPreviewCategories(items, 10000)
	if len(breakdown) != 3 {
		t.Fatalf("breakdown len = %d, want 3 (two categories plus exact remainder)", len(breakdown))
	}
	if breakdown[0].SharePercent != 50 || breakdown[1].BarPercent != 30 {
		t.Fatalf("category share fields were not preserved: %#v", breakdown)
	}
	other := breakdown[2]
	if other.Name != "Outros" || other.Amount.rawCents() != 2000 || other.ShareLabel != "20%" || other.BarPercent != 20 || other.ColorClass != "cbp-chart-other" {
		t.Fatalf("other remainder = %#v", other)
	}

	exact := buildPreviewCategories(items, 8000)
	if len(exact) != 2 {
		t.Fatalf("exact breakdown len = %d, want 2 without an empty Outros row", len(exact))
	}
}

func TestDashboardPreviewMonthFilterUsesOnlyMonthlyData(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-oct-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 300000, Status: "paid", Description: "Receita de outubro", CreatedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-oct-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, CategoryID: base.category, Type: "EXPENSE", Amount: 100000, Status: "paid", Description: "Despesa de outubro", CreatedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix()})

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "2026-10")
	if data.SelectedMonth != "2026-10" || data.SelectedMonthLabel != "Outubro/2026" {
		t.Fatalf("selected month = %q/%q, want 2026-10/Outubro/2026", data.SelectedMonth, data.SelectedMonthLabel)
	}
	if data.MonthFilter.IsCurrent {
		t.Fatal("October must not be marked as the current month")
	}
	if !data.MonthFilter.CanGoNext {
		t.Fatal("October must allow navigating forward to November")
	}
	if !strings.Contains(data.MonthFilter.PreviousHref, "mes=2026-09") {
		t.Fatalf("previous month href = %q, want mes=2026-09", data.MonthFilter.PreviousHref)
	}
	if data.MonthFilter.NextHref != "/" {
		t.Fatalf("next month href = %q, want current-month reset URL", data.MonthFilter.NextHref)
	}
	if data.MonthFilter.CurrentHref != "/" {
		t.Fatalf("current month href = %q, want /", data.MonthFilter.CurrentHref)
	}
	if !data.Financial.Selected.IsPast || data.Financial.MonthLabel != "Outubro/2026" {
		t.Fatalf("October financial month = %+v", data.Financial.Selected)
	}
	if got := data.Financial.Selected.RealizedIncome.rawCents(); got != 300000 {
		t.Fatalf("October financial income = %d, want 300000", got)
	}
	if got := data.Financial.Selected.RealizedExpense.rawCents(); got != 100000 {
		t.Fatalf("October financial expense = %d, want 100000", got)
	}
	if data.Financial.Selected.PendingAvailable || data.Financial.Selected.ProjectedAvailable {
		t.Fatalf("October financial view must hide projection: %+v", data.Financial.Selected)
	}
	if data.Categories.CurrentLabel == "" {
		t.Fatal("October categories label must be populated")
	}
	if got := data.Categories.Total.rawCents(); got != 100000 {
		t.Fatalf("October categories total = %d, want 100000", got)
	}
	for _, item := range data.CategoryBreakdown {
		if item.Name == "Outros" {
			t.Fatalf("October must not aggregate Outros when Moradia is 100%%: %#v", item)
		}
	}
	if got := data.CashFlow.RangeOptions[1].Href; !strings.Contains(got, "mes=2026-10") || !strings.Contains(got, "fluxo=12m") {
		t.Fatalf("cash flow href must preserve month and range: %q", got)
	}
	if got := data.CashFlow.RetryHref; !strings.Contains(got, "mes=2026-10") {
		t.Fatalf("retry href must preserve month: %q", got)
	}

	current := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	if !current.MonthFilter.IsCurrent || current.SelectedMonth != "2026-11" {
		t.Fatalf("current month state = %+v", current.MonthFilter)
	}
	if !current.Financial.Selected.IsCurrent || current.Financial.MonthLabel != "Novembro/2026" {
		t.Fatalf("current financial month = %+v", current.Financial.Selected)
	}
	if current.MonthFilter.CanGoNext {
		t.Fatal("the current month must not allow navigating into the future")
	}
}

func previewSectionBody(t *testing.T, body, name string) string {
	t.Helper()
	marker := `data-cbp-section="` + name + `"`
	index := strings.Index(body, marker)
	if index < 0 {
		t.Fatalf("missing section %s", name)
	}
	start := strings.LastIndex(body[:index], "<section")
	if start < 0 {
		t.Fatalf("missing section start for %s", name)
	}
	end := strings.Index(body[index:], "</section>")
	if end < 0 {
		t.Fatalf("missing section end for %s", name)
	}
	return body[start : index+end+len("</section>")]
}

func TestDashboardPreviewPastMonthFocusAndHistoricalCards(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	addF1Invoice(t, db, "inv-preview-oct", base.card, "2026-10", "OPEN", 0, now.Unix())
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-oct-card", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "inv-preview-oct", Type: "EXPENSE", Amount: 120000, Status: "paid", Description: "Compra de outubro", CreatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-oct-income-focus", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 300000, Status: "paid", Description: "Receita de outubro", CreatedAt: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).Unix()})
	addF1Tx(t, db, f1TxSeed{ID: "tx-preview-oct-expense-focus", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, CategoryID: base.category, Type: "EXPENSE", Amount: 100000, Status: "paid", Description: "Despesa de outubro", CreatedAt: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC).Unix()})

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "2026-10")
	if !data.CardsPeriodAware {
		t.Fatal("October must use the existing invoice-by-competence support")
	}
	if len(data.PreviewCards) != 1 {
		t.Fatalf("October cards = %d, want 1", len(data.PreviewCards))
	}
	card := data.PreviewCards[0]
	if !card.Historical || card.ReferenceLabel != "Outubro/2026" {
		t.Fatalf("historical card identity = %+v", card)
	}
	if card.InvoiceID != "inv-preview-oct" || card.Money.rawCents() != 120000 {
		t.Fatalf("historical invoice = %q/%d, want inv-preview-oct/120000", card.InvoiceID, card.Money.rawCents())
	}
	if card.DueLabel == "" || card.StatusLabel == "" {
		t.Fatalf("historical card must carry due date and current stored status: %+v", card)
	}

	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render October preview: %v", err)
	}
	body := out.String()

	for _, required := range []string{
		"cbp-temporal-notice",
		"Indicadores mensais em destaque",
		"Cartões exibem a fatura da competência",
		"Ver dados atuais",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("October preview missing %q", required)
		}
	}
	expectedOrder := []string{"finance", "categories", "goals", "cards", "balance", "cashflow", "commitments", "accounts"}
	lastIndex := -1
	for _, name := range expectedOrder {
		marker := `data-cbp-section="` + name + `"`
		index := strings.Index(body, marker)
		if index < 0 || index <= lastIndex {
			t.Fatalf("historical section %s is missing or out of order", name)
		}
		lastIndex = index
	}
	if strings.Contains(body, `data-cbp-section="transactions"`) {
		t.Fatal("transactions card must not be rendered in the preview")
	}

	if got := strings.Count(body, "data-cbp-focus-overlay"); got != 4 {
		t.Fatalf("focus overlays = %d, want 4 (current-position/window cards)", got)
	}
	if got := strings.Count(body, "data-cbp-focus-toggle aria-expanded"); got != 4 {
		t.Fatalf("focus controls = %d, want 4", got)
	}
	if got := strings.Count(body, "is-focus-muted"); got != 4 {
		t.Fatalf("focus-muted cards = %d, want 4", got)
	}
	if got := strings.Count(body, `inert aria-hidden="true"`); got != 4 {
		t.Fatalf("inert focus contents = %d, want 4", got)
	}
	if strings.Contains(body, "is-off-period") {
		t.Fatal("the old opacity-only off-period class must be gone")
	}

	overlayMessages := map[string][]string{
		"balance":     {"Posição atual", "não corresponde a Outubro/2026", "data-cbp-focus-overlay", "data-cbp-focus-toggle aria-expanded", `inert aria-hidden="true"`},
		"cashflow":    {"Janela própria", "histórico de 6 ou 12 meses", "data-cbp-focus-overlay", "data-cbp-focus-toggle aria-expanded", `inert aria-hidden="true"`},
		"accounts":    {"Posição atual", "posição atual das contas", "data-cbp-focus-overlay", "data-cbp-focus-toggle aria-expanded", `inert aria-hidden="true"`},
		"commitments": {"Janela atual", "janela futura própria", "data-cbp-focus-overlay", "data-cbp-focus-toggle aria-expanded", `inert aria-hidden="true"`},
	}
	for name, wants := range overlayMessages {
		section := previewSectionBody(t, body, name)
		for _, want := range wants {
			if !strings.Contains(section, want) {
				t.Fatalf("section %s missing %q", name, want)
			}
		}
	}

	cards := previewSectionBody(t, body, "cards")
	for _, want := range []string{"Fatura de Outubro/2026", "Situação atual do registro", "Valor e vencimento da competência.", "Vencimento"} {
		if !strings.Contains(cards, want) {
			t.Fatalf("historical cards missing %q", want)
		}
	}
	for _, forbidden := range []string{"data-cbp-focus-overlay", "data-cbp-focus-toggle aria-expanded", "Posição atual", "is-focus-muted"} {
		if strings.Contains(cards, forbidden) {
			t.Fatalf("period-aware cards must not contain %q", forbidden)
		}
	}
	for _, name := range []string{"finance", "categories", "goals"} {
		section := previewSectionBody(t, body, name)
		if strings.Contains(section, "data-cbp-focus-overlay") || strings.Contains(section, "data-cbp-focus-toggle aria-expanded") {
			t.Fatalf("monthly card %s must stay fully visible", name)
		}
		if !strings.Contains(section, "Outubro/2026") {
			t.Fatalf("monthly card %s must carry the selected month", name)
		}
	}

	finance := previewSectionBody(t, body, "finance")
	for _, want := range []string{"Histórico", "Resultado realizado", "R$ 0,00"} {
		if !strings.Contains(finance, want) {
			t.Fatalf("October finance view missing %q", want)
		}
	}
	for _, forbidden := range []string{"o maior valor do par ocupa 100%", "Acima do previsto", "Muito acima do previsto", "Sem meta definida", "Orçamento do mês"} {
		if strings.Contains(finance, forbidden) {
			t.Fatalf("October finance view must not contain %q", forbidden)
		}
	}

	hero := previewSectionBody(t, body, "balance")
	for _, forbidden := range []string{"Obrigações", "Disponível", "cbp-mini"} {
		if strings.Contains(hero, forbidden) {
			t.Fatalf("balance card must not contain %q", forbidden)
		}
	}

	current := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	if current.CardsPeriodAware {
		t.Fatal("current month cards must not use historical card presentation")
	}
	out.Reset()
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", current); err != nil {
		t.Fatalf("render current preview: %v", err)
	}
	currentBody := out.String()
	for _, forbidden := range []string{"cbp-temporal-notice", "data-cbp-focus-overlay", "data-cbp-focus-toggle aria-expanded", "Ver dados atuais", "Fatura de Outubro/2026"} {
		if strings.Contains(currentBody, forbidden) {
			t.Fatalf("current month must not contain %q", forbidden)
		}
	}
}

func TestDashboardPreviewAccountsUseValidatedTrendDirection(t *testing.T) {
	data := DashboardPreviewData{
		MonthFilter: PreviewMonthFilter{IsCurrent: true},
		PreviewAccounts: []PreviewAccount{
			{AccountCard: AccountCard{ID: "up", Name: "Conta em alta", TrendDirection: "up", TrendPercent: "+2,0%", Money: MoneyMinor(1000)}},
			{AccountCard: AccountCard{ID: "down", Name: "Conta em queda", TrendDirection: "down", TrendPercent: "-2,0%", Money: MoneyMinor(1000)}},
			{AccountCard: AccountCard{ID: "flat", Name: "Conta estável", TrendDirection: "flat", TrendPercent: "0,0%", Money: MoneyMinor(1000)}},
			{AccountCard: AccountCard{ID: "unavailable", Name: "Conta sem histórico", TrendDirection: "", TrendPercent: "Indisponível", Money: MoneyMinor(1000)}},
		},
	}
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-accounts", data); err != nil {
		t.Fatalf("render accounts: %v", err)
	}
	body := out.String()
	for _, want := range []string{"is-positive", "is-negative", "is-neutral", "Indisponível"} {
		if !strings.Contains(body, want) {
			t.Fatalf("accounts render missing %q", want)
		}
	}
}

func TestDashboardPreviewHistoricalCardsUseExistingInvoiceQuery(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-preview-history")

	extraCards := []struct {
		id     string
		status string
		amount int64
		paid   int64
	}{
		{id: "card-history-open", status: "OPEN", amount: 10000},
		{id: "card-history-closed", status: "CLOSED", amount: 20000},
		{id: "card-history-paid", status: "PAID", amount: 30000, paid: 30000},
		{id: "card-history-none"},
	}
	for _, card := range extraCards {
		addF1Account(t, db, card.id, base.ws, "CREDIT_CARD", 0, now.Unix())
		if card.id == "card-history-none" {
			continue
		}
		execTestSQL(t, db, `
			INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
			VALUES (?, ?, 20, 10, 1000000)
		`, "cc-"+card.id, card.id)
		invoiceID := "inv-" + card.id
		addF1Invoice(t, db, invoiceID, card.id, "2026-10", card.status, card.paid, now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "tx-" + card.id, WorkspaceID: base.ws, UserID: base.user, AccountID: card.id, InvoiceID: invoiceID, Type: "EXPENSE", Amount: card.amount, Status: "paid", Description: "Compra histórica", CreatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix()})
		if card.status == "PAID" {
			execTestSQL(t, db, `UPDATE invoices SET paid_at = ? WHERE id = ?`, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix(), invoiceID)
		}
	}

	selected := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cards, err := queryFaturasIndexCardsAt(db, base.ws, 2026, time.October, now)
	if err != nil {
		t.Fatalf("query historical cards: %v", err)
	}
	preview := buildPreviewCardsForPeriod(cards, &selected)
	if len(preview) != 5 {
		t.Fatalf("historical preview cards = %d, want 5", len(preview))
	}
	byName := make(map[string]PreviewCreditCard, len(preview))
	for _, card := range preview {
		byName[card.Name] = card
		if !card.Historical || card.ReferenceLabel != "Outubro/2026" {
			t.Fatalf("historical identity for %s = %+v", card.Name, card)
		}
	}

	open := byName["card-history-open"]
	if open.Status != "OPEN" || open.StatusLabel == "" || open.Money.rawCents() != 10000 {
		t.Fatalf("open historical card = %+v", open)
	}
	closed := byName["card-history-closed"]
	if closed.Status != "CLOSED" || closed.StatusLabel == "" || closed.Money.rawCents() != 20000 {
		t.Fatalf("closed historical card = %+v", closed)
	}
	paid := byName["card-history-paid"]
	if paid.Status != "PAID" || paid.StatusLabel != "Pago" || paid.Money.rawCents() != 30000 {
		t.Fatalf("paid historical card = %+v", paid)
	}
	none := byName["card-history-none"]
	if none.InvoiceID != "" || none.StatusLabel != "Sem fatura" || none.Money.rawCents() != 0 {
		t.Fatalf("missing historical invoice card = %+v", none)
	}
	for _, card := range []PreviewCreditCard{open, closed, paid} {
		if card.DueLabel == "" {
			t.Fatalf("historical card %s must include a due date", card.Name)
		}
		if !strings.Contains(card.Href, "mes=10") || !strings.Contains(card.Href, "ano=2026") {
			t.Fatalf("historical link for %s = %q, want October 2026 invoice hub", card.Name, card.Href)
		}
	}

	current := buildPreviewCards([]CreditCardCard{{ID: base.card, InvoiceID: "inv-current", Status: "OPEN", Name: "Cartão"}})
	if len(current) != 1 || current[0].Historical || current[0].ReferenceLabel != "" {
		t.Fatalf("current cards must not use historical labels: %+v", current)
	}
}

func TestDashboardPreviewFinancialRendersPartialAndUnavailableStates(t *testing.T) {
	partialMonth := PreviewFinancialMonth{
		Key:                "2026-11",
		Label:              "Novembro/2026",
		IsCurrent:          true,
		State:              "PARTIAL",
		StateLabel:         "Projeção parcial",
		StateClass:         "is-partial",
		StateReason:        "Recorrências futuras podem estar incompletas.",
		RealizedIncome:     MoneyMinor(100000),
		RealizedExpense:    MoneyMinor(150000),
		RealizedNet:        MoneyMinor(-50000),
		RealizedNegative:   true,
		RealizedToneClass:  "is-negative",
		PendingIncome:      MoneyMinor(20000),
		PendingExpense:     MoneyMinor(10000),
		PendingAvailable:   true,
		PendingCount:       1,
		HasPendingIncome:   true,
		ProjectedNet:       MoneyMinor(-40000),
		ProjectedAvailable: true,
		ProjectedNegative:  true,
		ProjectedLabel:     "Déficit projetado",
		ProjectedToneClass: "is-negative",
		HasAdjustments:     true,
		AdjustmentIncome:   MoneyMinor(5000),
	}
	data := DashboardPreviewData{
		SelectedMonthLabel: "Novembro/2026",
		MonthFilter:        PreviewMonthFilter{IsCurrent: true},
		Financial: PreviewFinancialView{
			Available:                   true,
			MonthLabel:                  "Novembro/2026",
			State:                       "PARTIAL",
			Selected:                    partialMonth,
			Months:                      []PreviewFinancialMonth{partialMonth, {Key: "2026-12", Label: "Dezembro/2026", StateLabel: "Projeção completa", StateClass: "is-complete"}, {Key: "2027-01", Label: "Janeiro/2027", StateLabel: "Projeção parcial", StateClass: "is-partial"}},
			Overdue:                     PreviewFinancialOverdue{Available: true, HasRegistered: true, Count: 1, Income: MoneyMinor(10000), HasIncome: true, StateLabel: "Projeção completa", StateClass: "is-complete"},
			Undated:                     PreviewFinancialUndated{HasRegistered: true, Count: 2},
			Recurrence:                  PreviewFinancialRecurrence{ShowWarning: true, Warning: "Recorrências futuras podem estar incompletas.", ToneClass: "is-partial"},
			ObligationsWithoutDateCount: 1,
		},
	}
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-financial", data); err != nil {
		t.Fatalf("render partial finance view: %v", err)
	}
	body := out.String()
	for _, want := range []string{"Projeção parcial", "Ajustes de caixa", "Déficit projetado", "Recorrências futuras", "Vencidos de meses anteriores", "Compromissos sem vencimento", "A composição financeira registra", "Não alocados a um mês", "data-sensitive"} {
		if !strings.Contains(body, want) {
			t.Fatalf("partial finance view missing %q", want)
		}
	}

	unavailable := partialMonth
	unavailable.State = "UNAVAILABLE"
	unavailable.StateLabel = "Projeção indisponível"
	unavailable.StateClass = "is-unavailable"
	unavailable.StateReason = "A composição financeira do workspace não está confiável."
	unavailable.PendingAvailable = false
	unavailable.ProjectedAvailable = false
	data.Financial.Selected = unavailable
	data.Financial.Months = []PreviewFinancialMonth{unavailable}
	data.Financial.Overdue = PreviewFinancialOverdue{}
	data.Financial.Undated = PreviewFinancialUndated{}
	data.Financial.Recurrence = PreviewFinancialRecurrence{}
	data.Financial.ObligationsWithoutDateCount = 0
	out.Reset()
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-financial", data); err != nil {
		t.Fatalf("render unavailable finance view: %v", err)
	}
	body = out.String()
	for _, want := range []string{"Projeção indisponível", "Resultado realizado", "Indisponível", "A composição financeira do workspace não está confiável."} {
		if !strings.Contains(body, want) {
			t.Fatalf("unavailable finance view missing %q", want)
		}
	}
	if strings.Contains(body, "Resultado projetado R$ 0,00") {
		t.Fatal("unavailable projection must not become a zero amount")
	}

	data.Financial = PreviewFinancialView{Error: true, ErrorMessage: "Não foi possível carregar a visão financeira agora. Tente novamente em instantes."}
	out.Reset()
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-financial", data); err != nil {
		t.Fatalf("render error finance view: %v", err)
	}
	body = out.String()
	for _, want := range []string{"Projeção indisponível", "Não foi possível carregar a visão financeira agora."} {
		if !strings.Contains(body, want) {
			t.Fatalf("error finance view missing %q", want)
		}
	}
	if strings.Contains(body, "R$ 0,00") {
		t.Fatal("aggregator error must not be rendered as a zero financial value")
	}
}

func TestDashboardPreviewHistoricalCardsFallbackKeepsCardsBelowMonthlyBlocks(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "2026-10")
	data.CardsPeriodAware = false
	data.PreviewCards = buildPreviewCards(data.Cards)

	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render fallback preview: %v", err)
	}
	body := out.String()
	expectedOrder := []string{"finance", "categories", "goals", "balance", "cashflow", "commitments", "accounts", "cards"}
	lastIndex := -1
	for _, name := range expectedOrder {
		marker := `data-cbp-section="` + name + `"`
		index := strings.Index(body, marker)
		if index < 0 || index <= lastIndex {
			t.Fatalf("fallback section %s is missing or out of order", name)
		}
		lastIndex = index
	}
	if !strings.Contains(body, "is-historical-focus is-cards-fallback") {
		t.Fatal("fallback historical grid class missing")
	}
	if got := strings.Count(body, "data-cbp-focus-overlay"); got != 5 {
		t.Fatalf("fallback focus overlays = %d, want 5", got)
	}
	cards := previewSectionBody(t, body, "cards")
	if !strings.Contains(cards, "Posição atual") || !strings.Contains(cards, "data-cbp-focus-toggle aria-expanded") {
		t.Fatal("fallback cards must keep the safe current-position overlay")
	}
}

func TestDashboardPreviewBalanceCompositionRequiresProvenSnapshot(t *testing.T) {
	cases := []struct {
		name     string
		total    int64
		accounts []AccountCard
		want     bool
	}{
		{
			name:  "proven mixed snapshot",
			total: 300,
			accounts: []AccountCard{
				{Money: MoneyMinor(100), AccountType: "INVESTMENT"},
				{Money: MoneyMinor(200), AccountType: "CHECKING"},
			},
			want: true,
		},
		{
			name:  "all accounts invested",
			total: 100,
			accounts: []AccountCard{
				{Money: MoneyMinor(100), AccountType: "INVESTMENT"},
			},
			want: true,
		},
		{
			name:  "account sum differs from real balance",
			total: 301,
			accounts: []AccountCard{
				{Money: MoneyMinor(100), AccountType: "INVESTMENT"},
				{Money: MoneyMinor(200), AccountType: "CHECKING"},
			},
			want: false,
		},
		{
			name:  "negative account",
			total: 100,
			accounts: []AccountCard{
				{Money: MoneyMinor(200), AccountType: "INVESTMENT"},
				{Money: MoneyMinor(-100), AccountType: "CHECKING"},
			},
			want: false,
		},
		{
			name:  "zero total",
			total: 0,
			accounts: []AccountCard{
				{Money: MoneyMinor(0), AccountType: "CHECKING"},
			},
			want: false,
		},
		{
			name:  "no investment account",
			total: 100,
			accounts: []AccountCard{
				{Money: MoneyMinor(100), AccountType: "CHECKING"},
			},
			want: false,
		},
		{
			name:  "invested exceeds total",
			total: 100,
			accounts: []AccountCard{
				{Money: MoneyMinor(200), AccountType: "INVESTMENT"},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := DashboardData{
				Balance: BalanceData{
					FinancialIndicatorAvailability: FinancialIndicatorAvailability{RealBalanceAvailable: true},
					Money:                          MoneyMinor(tc.total),
				},
				Accounts: tc.accounts,
			}
			got := buildPreviewBalanceComposition(base)
			if got.Available != tc.want {
				t.Fatalf("composition available = %v, want %v (%+v)", got.Available, tc.want, got)
			}
			if !tc.want {
				return
			}
			if got.Invested.rawCents()+got.Other.rawCents() != tc.total {
				t.Fatalf("composition values = %d + %d, want total %d", got.Invested.rawCents(), got.Other.rawCents(), tc.total)
			}
			if got.InvestedBarPercent+got.OtherBarPercent != 100 {
				t.Fatalf("composition bar = %d + %d, want 100", got.InvestedBarPercent, got.OtherBarPercent)
			}
		})
	}
}

func TestDashboardPreviewNetPresentationDoesNotCallNegativeResultDebt(t *testing.T) {
	cases := []struct {
		net   int64
		label string
		tone  string
	}{
		{net: 100, label: "Economia do período", tone: "is-positive"},
		{net: 0, label: "Resultado do período", tone: "is-neutral"},
		{net: -100, label: "Déficit do período", tone: "is-negative"},
	}
	for _, tc := range cases {
		label, tone := previewNetPresentation(tc.net)
		if label != tc.label || tone != tc.tone {
			t.Fatalf("previewNetPresentation(%d) = %q/%q, want %q/%q", tc.net, label, tone, tc.label, tc.tone)
		}
		if tc.net < 0 && strings.Contains(strings.ToLower(label), "dívida") {
			t.Fatalf("negative result must not be called debt: %q", label)
		}
	}
}

func TestDashboardPreviewCategoryColorIdentityIsStable(t *testing.T) {
	if got := dashboardCategoryTone("cat-stable"); got < 1 || got > 5 {
		t.Fatalf("category tone = %d, want a stable 1-5 color", got)
	}
	if dashboardCategoryTone("cat-stable") != dashboardCategoryTone("cat-stable") {
		t.Fatal("category tone must not vary between calls")
	}
	if previewCategoryColorClass(0) != "cbp-chart-other" {
		t.Fatalf("unknown category tone must use the neutral other color")
	}
}

func TestDashboardPreviewCompositionBarHandlesExtremeShares(t *testing.T) {
	cases := []struct {
		name          string
		invested      int64
		other         int64
		wantInvested  int
		wantOther     int
		wantInvestedP int
	}{
		{name: "three percent", invested: 300, other: 9700, wantInvested: 3, wantOther: 97, wantInvestedP: 3},
		{name: "fifty percent", invested: 5000, other: 5000, wantInvested: 50, wantOther: 50, wantInvestedP: 50},
		{name: "ninety six percent", invested: 9600, other: 400, wantInvested: 96, wantOther: 4, wantInvestedP: 96},
		{name: "all invested", invested: 10000, other: 0, wantInvested: 100, wantOther: 0, wantInvestedP: 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := DashboardData{
				Balance: BalanceData{
					FinancialIndicatorAvailability: FinancialIndicatorAvailability{RealBalanceAvailable: true},
					Money:                          MoneyMinor(tc.invested + tc.other),
				},
				Accounts: []AccountCard{
					{Money: MoneyMinor(tc.invested), AccountType: "INVESTMENT"},
					{Money: MoneyMinor(tc.other), AccountType: "CHECKING"},
				},
			}
			comp := buildPreviewBalanceComposition(base)
			if !comp.Available {
				t.Fatalf("composition unavailable: %+v", comp)
			}
			if comp.InvestedBarPercent != tc.wantInvested || comp.OtherBarPercent != tc.wantOther {
				t.Fatalf("bar percentages = %d/%d, want %d/%d", comp.InvestedBarPercent, comp.OtherBarPercent, tc.wantInvested, tc.wantOther)
			}
			if comp.InvestedPercent != tc.wantInvestedP {
				t.Fatalf("invested percent = %d, want %d", comp.InvestedPercent, tc.wantInvestedP)
			}

			data := DashboardPreviewData{
				MonthFilter:        PreviewMonthFilter{IsCurrent: true},
				BalanceComposition: comp,
				Invested:           PreviewInvested{Available: true, Money: comp.Invested, AccountCount: 1},
			}
			data.Balance = base.Balance
			data.Balance.TrendPercent = "0%"
			data.Balance.TrendDirection = "neutral"
			var out strings.Builder
			if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-hero", data); err != nil {
				t.Fatalf("render hero: %v", err)
			}
			body := out.String()
			if !strings.Contains(body, fmt.Sprintf(`width="%d"`, tc.wantInvested)) {
				t.Fatalf("hero missing invested width %d", tc.wantInvested)
			}
			if tc.wantOther > 0 {
				if !strings.Contains(body, fmt.Sprintf(`x="%d"`, tc.wantInvested)) || !strings.Contains(body, fmt.Sprintf(`width="%d"`, tc.wantOther)) {
					t.Fatalf("hero missing adjacent other segment x=%d width=%d", tc.wantInvested, tc.wantOther)
				}
			} else if strings.Contains(body, "cbp-composition-other") {
				t.Fatal("all-invested composition must not render an empty other segment")
			}
			if strings.Contains(body, `rx=`) {
				t.Fatal("composition bar must rely on the wrapper clip instead of per-rect rounded corners")
			}
		})
	}
}

func TestDashboardPreviewCommitmentTotalsMatchWindow(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	assertPreviewCommitmentTotals(t, "payables", data.PendingPayables, data.Payable7dTotal, data.Payable7dCount)
	assertPreviewCommitmentTotals(t, "receivables", data.PendingReceivables, data.Receivable7dTotal, data.Receivable7dCount)

	db2 := openTestDB(t)
	defer db2.Close()
	base2 := seedF1Base(t, db2, "ws-commit-window")
	addF1Tx(t, db2, f1TxSeed{ID: "tx-commit-payable", WorkspaceID: base2.ws, UserID: base2.user, AccountID: base2.checking, Type: "EXPENSE", Amount: 2500, Status: "pending", Description: "Conta a pagar", DueDate: now.AddDate(0, 0, 2).Unix(), CreatedAt: now.Unix()})
	addF1Tx(t, db2, f1TxSeed{ID: "tx-commit-receivable", WorkspaceID: base2.ws, UserID: base2.user, AccountID: base2.checking, Type: "INCOME", Amount: 4200, Status: "pending", Description: "Conta a receber", DueDate: now.AddDate(0, 0, 3).Unix(), CreatedAt: now.Unix()})
	data2 := buildDashboardPreviewDataAt(db2, base2.user, base2.ws, now, "6m", "")
	assertPreviewCommitmentTotals(t, "payables", data2.PendingPayables, data2.Payable7dTotal, data2.Payable7dCount)
	assertPreviewCommitmentTotals(t, "receivables", data2.PendingReceivables, data2.Receivable7dTotal, data2.Receivable7dCount)
}

func assertPreviewCommitmentTotals(t *testing.T, label string, items []DashboardPayableItem, total MoneyDisplay, count int) {
	t.Helper()
	if len(items) != count {
		t.Fatalf("%s list/count = %d/%d", label, len(items), count)
	}
	var sum int64
	for _, item := range items {
		next, err := addInt64Checked(sum, item.Amount.rawCents())
		if err != nil {
			t.Fatalf("%s sum overflow: %v", label, err)
		}
		sum = next
	}
	if got := total.rawCents(); got != sum {
		t.Fatalf("%s total = %d, want visible item sum %d", label, got, sum)
	}
}

func TestDashboardPreviewChartAxisLabelsAreCompact(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{cents: 0, want: "0"},
		{cents: 50000, want: "500"},
		{cents: 97500, want: "975"},
		{cents: 597500, want: "6 mil"},
		{cents: 1195000, want: "12 mil"},
		{cents: 2390000, want: "24 mil"},
	}
	for _, tc := range cases {
		if got := previewChartAxisLabel(tc.cents); got != tc.want {
			t.Fatalf("previewChartAxisLabel(%d) = %q, want %q", tc.cents, got, tc.want)
		}
	}
}

// Adapted from the removed V1 render suite: the zero-limit presentation is
// still a valid invariant of the canonical dashboard.
func TestDashboardPreviewZeroLimitDoesNotInventPercent(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()
	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	if len(data.Limits) != 1 {
		t.Fatalf("limits = %d, want 1", len(data.Limits))
	}
	data.Limits[0].Limit = MoneyMinor(0)
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render preview: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "Sem limite definido para esta categoria.") {
		t.Fatal("zero limit must be disclosed without inventing a percentage")
	}
	if !strings.Contains(body, "gastos") {
		t.Fatal("zero limit must still show the spent amount")
	}
	if strings.Contains(body, "% de R$ 0,00") {
		t.Fatal("zero limit must not render a percentage over zero")
	}
}

// Adapted from the removed V1 render suite: an empty month must not invent
// category rows in the canonical dashboard.
func TestDashboardPreviewCategoriesEmptyStateDoesNotInventRows(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-preview-empty-categories")

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "")
	if len(data.CategoryBreakdown) != 0 {
		t.Fatalf("category breakdown = %d, want 0", len(data.CategoryBreakdown))
	}
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render preview: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "Nenhuma despesa registrada neste período.") {
		t.Fatal("empty categories must render the empty state")
	}
	if strings.Contains(body, "cbp-category-bar-row") {
		t.Fatal("empty categories must not invent rows")
	}
}
