package handlers

import (
	"database/sql"
	"html/template"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The dashboard cash flow card owns a 6 or 12 month window. Chart bars,
// legend totals and the detail table must all come from that one window:
// months outside it never contribute, and equal totals across ranges are
// legitimate when the extra months have no realized movement.
func TestDashboardCashFlowWindowOnlyIncludesSelectedMonths(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-flow-window")

	addFlowTx(t, db, base, "tx-flow-income-jul", "INCOME", 100000, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-expense-sep", "EXPENSE", 40000, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-income-nov", "INCOME", 5000, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))

	six := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "6m", "")
	wide := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "12m", "")

	if !six.Available || !wide.Available {
		t.Fatalf("cash flow unavailable: 6m=%+v 12m=%+v", six, wide)
	}
	if len(six.Months) != 6 || len(wide.Months) != 12 {
		t.Fatalf("months = %d/%d, want 6/12", len(six.Months), len(wide.Months))
	}
	if six.Months[0].Key != "2026-06" || wide.Months[0].Key != "2025-12" {
		t.Fatalf("window starts = %q/%q, want 2026-06/2025-12", six.Months[0].Key, wide.Months[0].Key)
	}

	assertFlowTotals(t, six, 105000, 40000)
	assertFlowTotals(t, wide, 105000, 40000)
	if six.Net.rawCents() != wide.Net.rawCents() {
		t.Fatalf("same movements produced different net: 6m=%d 12m=%d", six.Net.rawCents(), wide.Net.rawCents())
	}

	// The six additional months are inside the range but empty. Identical
	// totals here are the expected result, not a stale indicator.
	for i := 0; i < 6; i++ {
		month := wide.Months[i]
		if month.Income.rawCents() != 0 || month.Expense.rawCents() != 0 {
			t.Fatalf("empty extra month %s has movements: %+v", month.Key, month)
		}
	}
}

func TestDashboardCashFlowExtraMonthsChangeTotalsWhenTheyHaveMovements(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-flow-extra")

	addFlowTx(t, db, base, "tx-flow-income-jul", "INCOME", 100000, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-expense-sep", "EXPENSE", 40000, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-income-nov", "INCOME", 5000, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))

	// Movements inside the previous six months: 2025-12 .. 2026-05.
	addFlowTx(t, db, base, "tx-flow-income-feb", "INCOME", 90000, time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-expense-apr", "EXPENSE", 30000, time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC))

	// Internal transfer and card purchase must stay out of the realized cash
	// flow in either window, even when they sit in the extra months.
	addFlowTx(t, db, base, "tx-flow-transfer", "TRANSFER", 500000, time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC))
	addF1Tx(t, db, f1TxSeed{
		ID: "tx-flow-card", WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, Type: "EXPENSE", Amount: 700000,
		Status: "paid", Description: "Compra no cartão fora do caixa",
		CreatedAt: time.Date(2026, 1, 12, 12, 0, 0, 0, time.UTC).Unix(),
	})

	six := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "6m", "")
	wide := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "12m", "")

	assertFlowTotals(t, six, 105000, 40000)
	assertFlowTotals(t, wide, 195000, 70000)
	if six.Net.rawCents() == wide.Net.rawCents() {
		t.Fatalf("extra months with movements must change the totals: %d", six.Net.rawCents())
	}

	wantIncome := map[string]int64{
		"2026-02": 90000,
		"2026-07": 100000,
		"2026-11": 5000,
	}
	wantExpense := map[string]int64{
		"2026-04": 30000,
		"2026-09": 40000,
	}
	for _, month := range wide.Months {
		if got := month.Income.rawCents(); got != wantIncome[month.Key] {
			t.Fatalf("12m income %s = %d, want %d", month.Key, got, wantIncome[month.Key])
		}
		if got := month.Expense.rawCents(); got != wantExpense[month.Key] {
			t.Fatalf("12m expense %s = %d, want %d", month.Key, got, wantExpense[month.Key])
		}
	}
}

func TestDashboardCashFlowRangeToggleKeepsChartIndicatorsAndDetailsInSync(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-flow-toggle")

	addFlowTx(t, db, base, "tx-flow-income-jul", "INCOME", 100000, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-expense-sep", "EXPENSE", 40000, time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-income-feb", "INCOME", 90000, time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC))

	first := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "6m", "")
	wide := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "12m", "")
	again := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "6m", "")

	if !reflect.DeepEqual(first, again) {
		t.Fatalf("6m -> 12m -> 6m changed the narrow window:\nfirst=%+v\nagain=%+v", first, again)
	}
	if first.Range != "6m" || !first.RangeOptions[0].Active || first.RangeOptions[1].Active {
		t.Fatalf("6m range state = %+v", first)
	}
	if wide.Range != "12m" || wide.RangeOptions[0].Active || !wide.RangeOptions[1].Active {
		t.Fatalf("12m range state = %+v", wide)
	}
	if !strings.Contains(wide.RangeOptions[1].Href, "fluxo=12m") || strings.Contains(wide.RangeOptions[0].Href, "fluxo=") {
		t.Fatalf("range hrefs = %q / %q", wide.RangeOptions[0].Href, wide.RangeOptions[1].Href)
	}

	assertFlowIndicatorsMatchMonths(t, first)
	assertFlowIndicatorsMatchMonths(t, wide)

	rendered := renderCashFlowCard(t, wide)
	if got := strings.Count(rendered, "data-flow-index="); got != 12 {
		t.Fatalf("12m chart groups = %d, want 12", got)
	}
	if got := strings.Count(rendered, "<tr><th scope=\"row\">"); got != 12 {
		t.Fatalf("12m detail rows = %d, want 12", got)
	}
	if !strings.Contains(rendered, "Últimos 12 meses") {
		t.Fatal("12m card must label the selected window")
	}
	if !strings.Contains(rendered, "Economia do período") {
		t.Fatal("positive result must keep the Economia label")
	}
}

func TestDashboardCashFlowKeepsWorkspaceIsolation(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-flow-owner")
	other := seedF1Base(t, db, "ws-flow-foreign")

	addFlowTx(t, db, base, "tx-flow-income-jul", "INCOME", 100000, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, other, "tx-flow-foreign-income", "INCOME", 9999999, time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, other, "tx-flow-foreign-expense", "EXPENSE", 8888888, time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC))

	for _, rng := range []string{"6m", "12m"} {
		got := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, rng, "")
		assertFlowTotals(t, got, 100000, 0)
	}
}

func TestDashboardCashFlowEmptyNarrowWindowStillShowsTheWideRange(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-flow-narrow-empty")

	addFlowTx(t, db, base, "tx-flow-old-income", "INCOME", 70000, time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC))
	addFlowTx(t, db, base, "tx-flow-old-expense", "EXPENSE", 20000, time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC))

	six := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "6m", "")
	wide := buildPreviewCashFlow(db, base.ws, now, dashboardCanonicalBasePath, "12m", "")

	if six.HasData {
		t.Fatalf("6m without movements must render the empty state: %+v", six)
	}
	assertFlowTotals(t, six, 0, 0)
	if !wide.HasData {
		t.Fatalf("12m with movements must render data: %+v", wide)
	}
	assertFlowTotals(t, wide, 70000, 20000)
}

func addFlowTx(t *testing.T, db *sql.DB, base f1Base, id, txType string, amount int64, at time.Time) {
	t.Helper()
	addF1Tx(t, db, f1TxSeed{
		ID: id, WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking,
		CategoryID: base.category, Type: txType, Amount: amount, Status: "paid",
		Description: id, CreatedAt: at.Unix(),
	})
}

func renderCashFlowCard(t *testing.T, data PreviewCashFlow) string {
	t.Helper()
	tpl := template.Must(template.New("cashflow").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"sub":       func(a, b int) int { return a - b },
	}).Parse(readTestFile(t, "templates/components/dashboard_preview_cards.html")))
	payload := struct {
		CashFlow           PreviewCashFlow
		MonthFilter        struct{ IsCurrent bool }
		SelectedMonthLabel string
	}{CashFlow: data, SelectedMonthLabel: "Novembro/2026"}
	payload.MonthFilter.IsCurrent = true
	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "dashboard-preview-cashflow", payload); err != nil {
		t.Fatalf("render cash flow card: %v", err)
	}
	return out.String()
}

func assertFlowTotals(t *testing.T, data PreviewCashFlow, income, expense int64) {
	t.Helper()
	if got := data.OperationalIncome.rawCents(); got != income {
		t.Fatalf("operational income = %d, want %d", got, income)
	}
	if got := data.TotalIncome.rawCents(); got != income {
		t.Fatalf("total income = %d, want %d", got, income)
	}
	if got := data.TotalExpense.rawCents(); got != expense {
		t.Fatalf("total expense = %d, want %d", got, expense)
	}
	if got := data.Net.rawCents(); got != income-expense {
		t.Fatalf("net = %d, want %d", got, income-expense)
	}
}

func assertFlowIndicatorsMatchMonths(t *testing.T, data PreviewCashFlow) {
	t.Helper()
	var income, expense int64
	for _, month := range data.Months {
		income += month.Income.rawCents()
		expense += month.Expense.rawCents()
	}
	if income != data.TotalIncome.rawCents() {
		t.Fatalf("indicator income %d != chart/table sum %d", data.TotalIncome.rawCents(), income)
	}
	if expense != data.TotalExpense.rawCents() {
		t.Fatalf("indicator expense %d != chart/table sum %d", data.TotalExpense.rawCents(), expense)
	}
	if data.TotalIncome.rawCents()-data.TotalExpense.rawCents() != data.Net.rawCents() {
		t.Fatalf("net %d is not income minus expense", data.Net.rawCents())
	}
}
