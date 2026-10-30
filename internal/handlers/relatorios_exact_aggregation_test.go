package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReportExactAmountBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		values []int64
		want   int64
		fail   bool
	}{
		{"empty", nil, 0, false},
		{"normal", []int64{100, 25}, 125, false},
		{"positive and negative", []int64{100, -25}, 75, false},
		{"maximum", []int64{math.MaxInt64}, math.MaxInt64, false},
		{"minimum", []int64{math.MinInt64}, math.MinInt64, false},
		{"above maximum", []int64{math.MaxInt64, 1}, 0, true},
		{"below minimum", []int64{math.MinInt64, -1}, 0, true},
		{"positive cancellation", []int64{math.MaxInt64, 1, -1}, math.MaxInt64, false},
		{"positive reordered", []int64{-1, math.MaxInt64, 1}, math.MaxInt64, false},
		{"negative cancellation", []int64{math.MinInt64, -1, 1}, math.MinInt64, false},
		{"negative reordered", []int64{1, math.MinInt64, -1}, math.MinInt64, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var amount reportExactAmount
			for _, value := range tc.values {
				amount.add(value)
			}
			got, err := amount.int64("test aggregate")
			if tc.fail {
				if !errors.Is(err, errReportAggregateOverflow) {
					t.Fatalf("want out of range, got %d, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestCashEventSumsCancelBeforeRangeCheck(t *testing.T) {
	for _, sum := range []struct {
		name string
		fn   func([]CashEvent) (int64, error)
	}{
		{"income", SumCashIncome},
		{"expense", SumCashExpense},
		{"net", SumCashEvents},
	} {
		t.Run(sum.name, func(t *testing.T) {
			isIncome := sum.name != "expense"
			values := []CashEvent{
				{IsIncome: isIncome, AmountCents: math.MaxInt64},
				{IsIncome: isIncome, AmountCents: 1},
				{IsIncome: isIncome, AmountCents: -1},
			}
			want := int64(math.MaxInt64)
			if sum.name == "net" {
				want = math.MaxInt64
			}
			if got, err := sum.fn(values); err != nil || got != want {
				t.Fatalf("cancelled sum = %d, %v; want %d", got, err, want)
			}
			values = values[:2]
			if _, err := sum.fn(values); !errors.Is(err, errReportAggregateOverflow) {
				t.Fatalf("final out of range should fail: %v", err)
			}
		})
	}
	negative := []CashEvent{
		{IsIncome: true, AmountCents: math.MinInt64},
		{IsIncome: true, AmountCents: -1},
		{IsIncome: true, AmountCents: 1},
	}
	if got, err := SumCashEvents(negative); err != nil || got != math.MinInt64 {
		t.Fatalf("negative cancellation = %d, %v", got, err)
	}
	negativeExpense := []CashEvent{
		{AmountCents: math.MinInt64},
		{AmountCents: -1},
		{AmountCents: 1},
	}
	if got, err := SumCashExpense(negativeExpense); err != nil || got != math.MinInt64 {
		t.Fatalf("expense negative cancellation = %d, %v", got, err)
	}
	if got, err := SumCashEvents([]CashEvent{{AmountCents: math.MinInt64}}); !errors.Is(err, errReportAggregateOverflow) || got != 0 {
		t.Fatalf("negating MinInt64 expense must fail: %d, %v", got, err)
	}
}

func TestReportGroupedExactCancellation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	query := `SELECT category_name, amount FROM (
		SELECT 'A' AS category_name, ? AS amount
		UNION ALL SELECT 'A', ?
		UNION ALL SELECT 'A', ?
		UNION ALL SELECT 'B', ?)`
	for _, values := range [][4]int64{
		{math.MaxInt64, 1, -1, 3},
		{-1, math.MaxInt64, 1, 3},
	} {
		groups, err := queryReportGroupedAmounts(context.Background(), db, "category", query, values[0], values[1], values[2], values[3])
		if err != nil || len(groups) != 2 || groups[0].name != "A" || groups[0].cents != math.MaxInt64 || groups[1].cents != 3 {
			t.Fatalf("grouped cancellation = %#v, %v", groups, err)
		}
	}
	if _, err := queryReportGroupedAmounts(context.Background(), db, "category", query, math.MaxInt64, 1, 0, 3); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("out of range category should fail: %v", err)
	}
}

func TestReportMonthlyMacroCancellation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "ws-exact-macro", "user-exact-macro", "account-exact-macro"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
	date := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	seedOverflowReportTransaction(t, db, "macro-max", ws, user, account, "INCOME", math.MaxInt64, date, "paid")
	seedOverflowReportTransaction(t, db, "macro-plus", ws, user, account, "INCOME", 1, date, "paid")
	seedOverflowReportTransaction(t, db, "macro-minus", ws, user, account, "EXPENSE", 1, date, "paid")

	dre, err := BuildDREData(db, ws, 2026)
	if err != nil || len(dre) != 1 || dre[0].AmountCents != math.MaxInt64 {
		t.Fatalf("DRE cancellation = %#v, %v", dre, err)
	}
	caixa, err := BuildCaixaRealizadoData(db, ws, 2026)
	if err != nil || len(caixa) != 1 || caixa[0].AmountCents != math.MaxInt64 {
		t.Fatalf("cash cancellation = %#v, %v", caixa, err)
	}
	seedOverflowReportTransaction(t, db, "macro-over", ws, user, account, "INCOME", 1, date, "paid")
	if _, err := BuildDREData(db, ws, 2026); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("DRE final out of range: %v", err)
	}
	if _, err := BuildCaixaRealizadoData(db, ws, 2026); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("cash final out of range: %v", err)
	}
}

func TestReportBalanceWorkspaceAndHTTPPDFBoundaries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const wsA, userA = "ws-exact-a", "user-exact-a"
	const wsB, userB = "ws-exact-b", "user-exact-b"
	seedOverflowReportWorkspace(t, db, wsA, userA, "account-exact-a", "personal", 123)
	seedOverflowReportWorkspace(t, db, wsB, userB, "account-exact-b", "personal", math.MaxInt64)
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('account-exact-b-plus', ?, 'Mais', 'CHECKING', 0, 1, ?, ?),
		       ('account-exact-b-minus', ?, 'Menos', 'CHECKING', 0, -1, ?, ?)`, wsB, now, now, wsB, now, now)
	appNow := appTime(time.Now())
	hA := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: wsA, UserID: userA}
	hB := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: wsB, UserID: userB}
	data, err := hB.buildRelatoriosData("", "caixa", "movimentos", int(appNow.Month()), appNow.Year())
	if err != nil || data.SaldoAcumulado.rawCents() != math.MaxInt64 {
		t.Fatalf("B cancellation balance = %+v, %v", data.SaldoAcumulado, err)
	}
	data, err = hA.buildRelatoriosData("", "caixa", "movimentos", int(appNow.Month()), appNow.Year())
	if err != nil || data.SaldoAcumulado.rawCents() != 123 {
		t.Fatalf("A should be isolated: %+v, %v", data.SaldoAcumulado, err)
	}
	validPDF := httptest.NewRecorder()
	hA.HandleExportarCaixaPDF(validPDF, httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf", nil))
	if validPDF.Code != http.StatusOK || !strings.HasPrefix(validPDF.Body.String(), "%PDF") {
		t.Fatalf("valid PDF = %d, prefix %.12q", validPDF.Code, validPDF.Body.String())
	}
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('account-exact-b-over', ?, 'Excesso', 'CHECKING', 0, 1, ?, ?)`, wsB, now, now)
	if _, err := hB.buildRelatoriosData("", "caixa", "movimentos", int(appNow.Month()), appNow.Year()); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("B final out of range: %v", err)
	}
	httpResult := httptest.NewRecorder()
	hB.HandleExibirRelatorios(httpResult, httptest.NewRequest(http.MethodGet, "/relatorios", nil))
	if httpResult.Code != http.StatusInternalServerError || strings.Contains(httpResult.Body.String(), "R$") {
		t.Fatalf("HTML must fail closed: %d %q", httpResult.Code, httpResult.Body.String())
	}
	invalidPDF := httptest.NewRecorder()
	hB.HandleExportarCaixaPDF(invalidPDF, httptest.NewRequest(http.MethodGet, "/relatorios/caixa.pdf", nil))
	if invalidPDF.Code != http.StatusInternalServerError || strings.HasPrefix(invalidPDF.Body.String(), "%PDF") {
		t.Fatalf("overflow PDF must fail closed: %d %.12q", invalidPDF.Code, invalidPDF.Body.String())
	}
	data, err = hA.buildRelatoriosData("", "caixa", "movimentos", int(appNow.Month()), appNow.Year())
	if err != nil || data.SaldoAcumulado.rawCents() != 123 {
		t.Fatalf("B overflow contaminated A: %+v, %v", data.SaldoAcumulado, err)
	}
}

func TestInvoicePaymentAllocationSumOutOfRange(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentCompetenceScenario(t, db, "2026-08-05", "2026-08-10")
	date := testUnixDate("2026-08-10")
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES ('payment-over-range', 'ws-test', 'invoice-test', 'checking-test', ?, ?, 'manual', ?)`, math.MaxInt64, date, time.Now().Unix())
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-test"}
	if _, err := h.queryInvoiceAllocationTargets(context.Background(), date, date, "paid"); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("invoice payment allocation must report out of range: %v", err)
	}
}

func TestInvoicePendingExactAfterIntermediateOverflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentCompetenceScenario(t, db, "2026-08-05", "2026-08-10")
	date := testUnixDate("2026-08-10")
	execTestSQL(t, db, `UPDATE invoices SET status = 'OPEN' WHERE id = 'invoice-test'`)
	execTestSQL(t, db, `UPDATE transactions SET amount = ? WHERE id = 'purchase-test' AND workspace_id = 'ws-test'`, math.MaxInt64)
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-extra', 'ws-test', 'user-test', 'card-test', 'invoice-test', 'EXPENSE', 25000, ?, 'extra', 'paid', 1, 1, ?, ?)`, date, time.Now().Unix(), time.Now().Unix())
	invoices, err := queryReportPendingInvoices(context.Background(), db, "ws-test", date, date+86400, true, false)
	if err != nil || len(invoices) != 1 || invoices[0].pending != math.MaxInt64 {
		t.Fatalf("invoice intermediate cancellation = %#v, %v", invoices, err)
	}
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-test"}
	targets, err := h.queryInvoiceAllocationTargets(context.Background(), date, date+86400, "pending")
	if err != nil || targets["invoice-test"] != math.MaxInt64 {
		t.Fatalf("invoice pending allocation target = %#v, %v", targets, err)
	}
	execTestSQL(t, db, `UPDATE transactions SET amount = 25001 WHERE id = 'purchase-extra' AND workspace_id = 'ws-test'`)
	if _, err := queryReportPendingInvoices(context.Background(), db, "ws-test", date, date+86400, true, false); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("invoice pending final out of range: %v", err)
	}
}

func TestReportPDFCategoryExactBoundary(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "ws-exact-pdf", "user-exact-pdf", "account-exact-pdf"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 0)
	date := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	seedOverflowReportTransaction(t, db, "pdf-max", ws, user, account, "INCOME", math.MaxInt64, date, "paid")
	for _, query := range []struct {
		name string
		fn   func() ([]pdfCategorySummaryRow, error)
	}{
		{"DRE", func() ([]pdfCategorySummaryRow, error) {
			return queryDREPDFCategorySummary(db, ws, "INCOME", date, date)
		}},
		{"cash", func() ([]pdfCategorySummaryRow, error) {
			return queryCaixaPDFCategorySummary(db, ws, "INCOME", date, date, "realizado")
		}},
		{"forecast", func() ([]pdfCategorySummaryRow, error) {
			return queryCaixaPDFCategorySummary(db, ws, "INCOME", date, date, "previsao")
		}},
	} {
		t.Run(query.name, func(t *testing.T) {
			rows, err := query.fn()
			if err != nil || len(rows) != 1 || rows[0].Amount != math.MaxInt64 || rows[0].Percent != 100 {
				t.Fatalf("maximum PDF category = %#v, %v", rows, err)
			}
		})
	}
	seedOverflowReportTransaction(t, db, "pdf-plus", ws, user, account, "INCOME", 1, date, "paid")
	for _, query := range []struct {
		name string
		fn   func() ([]pdfCategorySummaryRow, error)
	}{
		{"DRE", func() ([]pdfCategorySummaryRow, error) {
			return queryDREPDFCategorySummary(db, ws, "INCOME", date, date)
		}},
		{"cash", func() ([]pdfCategorySummaryRow, error) {
			return queryCaixaPDFCategorySummary(db, ws, "INCOME", date, date, "realizado")
		}},
		{"forecast", func() ([]pdfCategorySummaryRow, error) {
			return queryCaixaPDFCategorySummary(db, ws, "INCOME", date, date, "previsao")
		}},
	} {
		t.Run(query.name+" overflow", func(t *testing.T) {
			if _, err := query.fn(); !errors.Is(err, errReportAggregateOverflow) {
				t.Fatalf("PDF category final out of range: %v", err)
			}
		})
	}
}

func TestReportDailyAndAccountingKnownTotals(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const ws, user, account = "ws-exact-daily", "user-exact-daily", "account-exact-daily"
	seedOverflowReportWorkspace(t, db, ws, user, account, "personal", 1000)
	day1 := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	day2 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC).Unix()
	seedOverflowReportTransaction(t, db, "daily-income-1", ws, user, account, "INCOME", 100, day1, "paid")
	seedOverflowReportTransaction(t, db, "daily-expense-1", ws, user, account, "EXPENSE", 30, day1, "paid")
	seedOverflowReportTransaction(t, db, "daily-income-2", ws, user, account, "INCOME", 50, day2, "paid")
	seedOverflowReportTransaction(t, db, "daily-expense-2", ws, user, account, "EXPENSE", 20, day2, "paid")
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: ws, UserID: user}
	data, err := h.buildRelatoriosData("", "caixa", "diario", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	if data.TotalReceitas.rawCents() != 150 || data.TotalDespesas.rawCents() != 50 || data.SaldoLiquido.rawCents() != 100 {
		t.Fatalf("cash totals = %+v %+v %+v", data.TotalReceitas, data.TotalDespesas, data.SaldoLiquido)
	}
	if len(data.DailySummary) != 2 {
		t.Fatalf("daily summary = %#v", data.DailySummary)
	}
	wantNets := map[string]int64{"10/08": 70, "11/08": 30}
	for _, row := range data.DailySummary {
		if want, ok := wantNets[row.DateLabel]; !ok || row.NetTotal.rawCents() != want {
			t.Fatalf("daily row = %#v, want net %d", row, want)
		}
	}
	period := NewReportPeriodFromMonth(2026, 8)
	closeData, err := BuildAccountingCloseData(db, ws, period)
	if err != nil || closeData.TotalIncomeRealized != 150 || closeData.TotalExpenseRealized != 50 || closeData.NetResult != 100 || closeData.ClosingBalance != 1100 {
		t.Fatalf("accounting totals = %#v, %v", closeData, err)
	}
}
