package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

// O resumo de /lancamentos responde ao mesmo conjunto filtrado da lista. Os
// testes abaixo cobrem o contrato: separação Entradas/Saídas confirmadas,
// preservação de Pendente/Vencido/Agendado, exclusão de transferência e
// isolamento de workspace.

func TestLancamentosResumoStatusDefaultReflectsVisibleItems(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}
	data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	// Nenhuma receita confirmada no cenário: Entradas zeradas, Saídas com o
	// mesmo total que o antigo agregado "Pago".
	assertLancamentosSummary(t, data, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, data, "saidas_confirmadas", 16000, 2)
	assertLancamentosSummary(t, data, "pendente", 61000, 2)
	assertLancamentosSummary(t, data, "vencido", 0, 0)
	assertLancamentosSummary(t, data, "agendado", 33000, 1)

	if _, ok := lancamentosSummaryByKey(data, "pago"); ok {
		t.Fatalf("resumo ainda expõe o agregado genérico %q: %#v", "pago", data.ResumoStatus)
	}
}

func TestLancamentosResumoStatusOrderPutsDirectionalMetricsFirst(t *testing.T) {
	transactions := []TransactionRow{
		{Type: models.TransactionTypeIncome, Amount: 5000, PaymentStatus: models.PaymentStatusPaid},
		{Type: models.TransactionTypeExpense, Amount: 7000, PaymentStatus: models.PaymentStatusPaid},
	}
	items := mustBuildLancamentosStatusSummary(t, transactions, nil)
	wantKeys := []string{"entradas_confirmadas", "saidas_confirmadas", "pendente", "vencido", "agendado"}
	if len(items) != len(wantKeys) {
		t.Fatalf("summary length = %d, want %d", len(items), len(wantKeys))
	}
	for i, key := range wantKeys {
		if items[i].Key != key {
			t.Fatalf("summary[%d].Key = %q, want %q", i, items[i].Key, key)
		}
	}
	if !items[0].Primary || !items[1].Primary {
		t.Fatalf("Entradas/Saídas devem ser as métricas primárias: %#v", items[:2])
	}
	for _, item := range items[2:] {
		if item.Primary {
			t.Fatalf("métrica de lifecycle não pode ser primária: %#v", item)
		}
	}
}

// A: receita confirmada alimenta Entradas confirmadas.
// B: despesa confirmada alimenta Saídas confirmadas.
// C: receita pendente NÃO entra em Entradas confirmadas.
// D: despesa pendente NÃO entra em Saídas confirmadas.
// E: vencido continua Vencido. F: agendado continua Agendado.
func TestLancamentosResumoStatusSplitsConfirmedByDirection(t *testing.T) {
	transactions := []TransactionRow{
		{Type: models.TransactionTypeExpense, Amount: 1000, PaymentStatus: models.PaymentStatusPaid},
		{Type: models.TransactionTypeExpense, Amount: 2000, IsPending: true},
		{Type: models.TransactionTypeExpense, Amount: 3000, IsPending: true, IsOverdue: true},
		{Type: models.TransactionTypeExpense, Amount: 4000, IsPending: true, IsScheduled: true},
		{Type: models.TransactionTypeIncome, Amount: 100000, PaymentStatus: models.PaymentStatusPaid},
		{Type: models.TransactionTypeIncome, Amount: 200000, IsPending: true, IsOverdue: true},
	}
	data := LancamentosData{ResumoStatus: mustBuildLancamentosStatusSummary(t, transactions, nil)}

	assertLancamentosSummary(t, data, "entradas_confirmadas", 100000, 1)
	assertLancamentosSummary(t, data, "saidas_confirmadas", 1000, 1)
	assertLancamentosSummary(t, data, "pendente", 2000, 1)
	assertLancamentosSummary(t, data, "vencido", 3000, 1)
	assertLancamentosSummary(t, data, "agendado", 4000, 1)
}

// Transferência não é entrada nem saída: move saldo entre contas do mesmo
// workspace sem criar receita ou despesa, e na visão global a mesma linha seria
// saída da origem e entrada no destino ao mesmo tempo. Fica fora dos dois
// agregados direcionais, como em queryDashboardMonthlySummary.
func TestLancamentosResumoStatusExcludesTransfersFromDirectionalMetrics(t *testing.T) {
	transactions := []TransactionRow{
		{Type: models.TransactionTypeTransfer, Amount: 300000, PaymentStatus: models.PaymentStatusPaid},
		{Type: models.TransactionTypeTransfer, Amount: 400000, IsPending: true},
		{Type: models.TransactionTypeTransfer, Amount: 500000, IsPending: true, IsScheduled: true},
		{Type: models.TransactionTypeIncome, Amount: 7000, PaymentStatus: models.PaymentStatusPaid},
	}
	data := LancamentosData{ResumoStatus: mustBuildLancamentosStatusSummary(t, transactions, nil)}

	assertLancamentosSummary(t, data, "entradas_confirmadas", 7000, 1)
	assertLancamentosSummary(t, data, "saidas_confirmadas", 0, 0)
	assertLancamentosSummary(t, data, "pendente", 0, 0)
	assertLancamentosSummary(t, data, "vencido", 0, 0)
	assertLancamentosSummary(t, data, "agendado", 0, 0)
}

func TestLancamentosResumoStatusSaidasConfirmadasReflectsVisiblePaidItems(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}
	data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Situacoes: []string{"pago"}})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	assertLancamentosSummary(t, data, "saidas_confirmadas", 16000, 2)
	assertLancamentosSummary(t, data, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, data, "pendente", 0, 0)
	assertLancamentosSummary(t, data, "vencido", 0, 0)
	assertLancamentosSummary(t, data, "agendado", 0, 0)
}

// G: filtro por conta, categoria e busca restringem o resumo ao conjunto visível.
func TestLancamentosResumoStatusRespectsCategoryAccountSearchAndType(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}

	byCategory, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Categorias: []string{"cat-food-summary"}})
	if err != nil {
		t.Fatalf("buildLancamentosData category: %v", err)
	}
	assertLancamentosSummary(t, byCategory, "saidas_confirmadas", 11000, 1)
	assertLancamentosSummary(t, byCategory, "pendente", 22000, 1)
	assertLancamentosSummary(t, byCategory, "agendado", 33000, 1)

	byAccount, err := handler.buildLancamentosData("checking-summary", 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData account: %v", err)
	}
	assertLancamentosSummary(t, byAccount, "saidas_confirmadas", 16000, 2)
	assertLancamentosSummary(t, byAccount, "pendente", 22000, 1)
	assertLancamentosSummary(t, byAccount, "agendado", 33000, 1)

	bySearch, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Busca: "Card Purchase"})
	if err != nil {
		t.Fatalf("buildLancamentosData search: %v", err)
	}
	assertLancamentosSummary(t, bySearch, "saidas_confirmadas", 0, 0)
	assertLancamentosSummary(t, bySearch, "pendente", 39000, 1)
	assertLancamentosSummary(t, bySearch, "agendado", 0, 0)

	// Tipo receita: a única receita do conjunto está pendente, então Entradas
	// confirmadas fica zerada mesmo com a receita visível na lista.
	byType, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Tipos: []string{"receita"}})
	if err != nil {
		t.Fatalf("buildLancamentosData type: %v", err)
	}
	assertLancamentosSummary(t, byType, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, byType, "saidas_confirmadas", 0, 0)
	assertLancamentosSummary(t, byType, "pendente", 0, 0)
	assertLancamentosSummary(t, byType, "agendado", 0, 0)

	// Tipo transferência: nada entra nos agregados direcionais nem no lifecycle.
	byTransfer, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Tipos: []string{"transferencia"}})
	if err != nil {
		t.Fatalf("buildLancamentosData transfer: %v", err)
	}
	assertLancamentosSummary(t, byTransfer, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, byTransfer, "saidas_confirmadas", 0, 0)
	assertLancamentosSummary(t, byTransfer, "pendente", 0, 0)
	assertLancamentosSummary(t, byTransfer, "vencido", 0, 0)
	assertLancamentosSummary(t, byTransfer, "agendado", 0, 0)
}

// H: filtros combinados (conta + situação + categoria) reduzem o resumo à
// interseção, sem recalcular com uma query mais ampla que a lista.
func TestLancamentosResumoStatusRespectsCombinedFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary", UserID: "user-summary"}

	combined, err := handler.buildLancamentosData("checking-summary", 8, year, LancamentosFilters{
		Situacoes:  []string{"pago"},
		Categorias: []string{"cat-food-summary"},
	})
	if err != nil {
		t.Fatalf("buildLancamentosData combined: %v", err)
	}
	assertLancamentosSummary(t, combined, "saidas_confirmadas", 11000, 1)
	assertLancamentosSummary(t, combined, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, combined, "pendente", 0, 0)

	// Busca textual que não casa com nada.
	empty, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Busca: "zzz-nao-existe-zzz"})
	if err != nil {
		t.Fatalf("buildLancamentosData empty search: %v", err)
	}
	assertLancamentosSummary(t, empty, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, empty, "saidas_confirmadas", 0, 0)
	assertLancamentosSummary(t, empty, "pendente", 0, 0)
	assertLancamentosSummary(t, empty, "vencido", 0, 0)
	assertLancamentosSummary(t, empty, "agendado", 0, 0)
}

func TestLancamentosResumoStatusKeepsWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	year := time.Now().UTC().Year() + 2
	seedLancamentosStatusSummaryScenario(t, db, year)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-summary-other", UserID: "user-summary"}
	data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	assertLancamentosSummary(t, data, "entradas_confirmadas", 0, 0)
	assertLancamentosSummary(t, data, "saidas_confirmadas", 0, 0)
	assertLancamentosSummary(t, data, "pendente", 99000, 1)
	assertLancamentosSummary(t, data, "agendado", 0, 0)
}

// O resultado líquido deriva dos dois agregados direcionais sem regra nova e
// ignora Pendente/Vencido/Agendado.
func TestLancamentosResumoResultadoLiquidoDerivesFromDirectionalMetrics(t *testing.T) {
	cases := []struct {
		name     string
		entradas int64
		saidas   int64
		want     string
		negative bool
	}{
		{name: "positivo", entradas: 63850, saidas: 42798, want: "+ R$ 210,52"},
		{name: "negativo", entradas: 10000, saidas: 42798, want: "- R$ 327,98", negative: true},
		{name: "zero", entradas: 42798, saidas: 42798, want: "R$ 0,00"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := LancamentosData{}
			data.ResumoStatus = mustBuildLancamentosStatusSummary(t, []TransactionRow{
				{Type: models.TransactionTypeIncome, Amount: tc.entradas, PaymentStatus: models.PaymentStatusPaid},
				{Type: models.TransactionTypeExpense, Amount: tc.saidas, PaymentStatus: models.PaymentStatusPaid},
			}, nil)
			applyLancamentosResumoDerivados(&data)

			if !data.ResumoLiquidoTemDados {
				t.Fatalf("resultado líquido não foi calculado")
			}
			if data.ResumoLiquido != tc.want {
				t.Fatalf("resultado líquido = %q, want %q", data.ResumoLiquido, tc.want)
			}
			if data.ResumoLiquidoNegativo != tc.negative {
				t.Fatalf("negativo = %v, want %v", data.ResumoLiquidoNegativo, tc.negative)
			}
		})
	}
}

func TestLancamentosResumoOverflowReadModel(t *testing.T) {
	cases := []struct {
		name    string
		txType  string
		amounts []int64
		filter  string
		key     string
		want    int64
		wantErr bool
	}{
		{name: "income overflow", txType: "INCOME", amounts: []int64{5_000_000_000_000_000_000, 5_000_000_000_000_000_000}, filter: "receita", key: lancamentosResumoEntradasConfirmadas, wantErr: true},
		{name: "expense overflow", txType: "EXPENSE", amounts: []int64{5_000_000_000_000_000_000, 5_000_000_000_000_000_000}, filter: "despesa", key: lancamentosResumoSaidasConfirmadas, wantErr: true},
		{name: "exact max", txType: "INCOME", amounts: []int64{math.MaxInt64 - 1, 1}, filter: "receita", key: lancamentosResumoEntradasConfirmadas, want: math.MaxInt64},
		{name: "max plus one", txType: "INCOME", amounts: []int64{math.MaxInt64, 1}, filter: "receita", key: lancamentosResumoEntradasConfirmadas, wantErr: true},
		{name: "single max", txType: "INCOME", amounts: []int64{math.MaxInt64}, filter: "receita", key: lancamentosResumoEntradasConfirmadas, want: math.MaxInt64},
		{name: "empty", txType: "INCOME", filter: "receita", key: lancamentosResumoEntradasConfirmadas},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			const (
				workspaceID = "ws-lancamentos-overflow"
				userID      = "user-lancamentos-overflow"
				accountID   = "account-lancamentos-overflow"
			)
			seedOverflowReportWorkspace(t, db, workspaceID, userID, accountID, "personal", 0)
			year := time.Now().UTC().Year() + 2
			date := time.Date(year, time.August, 15, 12, 0, 0, 0, time.UTC).Unix()
			counterpartType := "EXPENSE"
			if tc.txType == "EXPENSE" {
				counterpartType = "INCOME"
			}
			// Alternar direções mantém o saldo histórico entre zero e cada
			// valor individual; só o bucket filtrado ultrapassa int64.
			for i, amount := range tc.amounts {
				if tc.txType == "EXPENSE" {
					seedOverflowReportTransaction(t, db, fmt.Sprintf("tx-lancamentos-counterpart-%d", i), workspaceID, userID, accountID, counterpartType, amount, date, "paid")
				}
				seedOverflowReportTransaction(t, db, fmt.Sprintf("tx-lancamentos-overflow-%d", i), workspaceID, userID, accountID, tc.txType, amount, date, "paid")
				if tc.txType == "INCOME" {
					seedOverflowReportTransaction(t, db, fmt.Sprintf("tx-lancamentos-counterpart-%d", i), workspaceID, userID, accountID, counterpartType, amount, date, "paid")
				}
			}

			handler := TransactionHandler{DB: db, WorkspaceID: workspaceID, UserID: userID}
			data, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{Tipos: []string{tc.filter}})
			if tc.wantErr {
				if !errors.Is(err, errReportAggregateOverflow) {
					t.Fatalf("overflow error = %v, want report aggregate overflow", err)
				}
				if len(data.ResumoStatus) != 0 || data.ResumoLiquidoTemDados {
					t.Fatalf("overflow returned partial summary: %#v", data)
				}
			} else {
				if err != nil {
					t.Fatalf("representable summary: %v", err)
				}
				assertLancamentosSummary(t, data, tc.key, tc.want, len(tc.amounts))
			}

			var balance int64
			if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ? AND workspace_id = ?`, accountID, workspaceID).Scan(&balance); err != nil {
				t.Fatal(err)
			}
			if balance != 0 {
				t.Fatalf("read-model changed account balance to %d", balance)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id = ?`, workspaceID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2*len(tc.amounts) {
				t.Fatalf("read-model changed transaction count to %d, want %d", count, 2*len(tc.amounts))
			}
		})
	}
}

func TestLancamentosResumoOverflowLifecycleAndInvoices(t *testing.T) {
	const large = int64(5_000_000_000_000_000_000)
	cases := []struct {
		name         string
		transactions []TransactionRow
		invoices     []InvoiceRow
	}{
		{name: "pending expenses", transactions: []TransactionRow{{Type: models.TransactionTypeExpense, Amount: large, IsPending: true}, {Type: models.TransactionTypeExpense, Amount: large, IsPending: true}}},
		{name: "overdue expenses", transactions: []TransactionRow{{Type: models.TransactionTypeExpense, Amount: large, IsOverdue: true}, {Type: models.TransactionTypeExpense, Amount: large, IsOverdue: true}}},
		{name: "scheduled expenses", transactions: []TransactionRow{{Type: models.TransactionTypeExpense, Amount: large, IsScheduled: true}, {Type: models.TransactionTypeExpense, Amount: large, IsScheduled: true}}},
		{name: "expense and invoice", transactions: []TransactionRow{{Type: models.TransactionTypeExpense, Amount: large, IsPending: true}}, invoices: []InvoiceRow{{Pending: large}}},
		{name: "overdue invoices", invoices: []InvoiceRow{{Pending: large, IsOverdue: true}, {Pending: large, IsOverdue: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, err := buildLancamentosStatusSummary(tc.transactions, tc.invoices)
			if !errors.Is(err, errReportAggregateOverflow) || items != nil {
				t.Fatalf("summary = %#v, err = %v; want no partial summary and overflow error", items, err)
			}
		})
	}
}

func TestLancamentosResumoNormalValuesAndFilteredOverflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		workspaceID = "ws-lancamentos-filter"
		userID      = "user-lancamentos-filter"
		accountID   = "account-lancamentos-small"
		largeID     = "account-lancamentos-large"
	)
	seedOverflowReportWorkspace(t, db, workspaceID, userID, accountID, "personal", 0)
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Conta Extrema', 'CHECKING', 0, 0, ?, ?)`, largeID, workspaceID, now, now)
	year := time.Now().UTC().Year() + 2
	date := time.Date(year, time.August, 15, 12, 0, 0, 0, time.UTC).Unix()
	seedOverflowReportTransaction(t, db, "small-income", workspaceID, userID, accountID, "INCOME", 58_850, date, "paid")
	seedOverflowReportTransaction(t, db, "small-expense", workspaceID, userID, accountID, "EXPENSE", 250_284, date, "paid")
	seedOverflowReportTransaction(t, db, "large-income-1", workspaceID, userID, largeID, "INCOME", 5_000_000_000_000_000_000, date, "paid")
	seedOverflowReportTransaction(t, db, "large-expense-1", workspaceID, userID, largeID, "EXPENSE", 5_000_000_000_000_000_000, date, "paid")
	seedOverflowReportTransaction(t, db, "large-income-2", workspaceID, userID, largeID, "INCOME", 5_000_000_000_000_000_000, date, "paid")
	seedOverflowReportTransaction(t, db, "large-expense-2", workspaceID, userID, largeID, "EXPENSE", 5_000_000_000_000_000_000, date, "paid")

	handler := TransactionHandler{DB: db, WorkspaceID: workspaceID, UserID: userID}
	data, err := handler.buildLancamentosData(accountID, 8, year, LancamentosFilters{})
	if err != nil {
		t.Fatalf("filtered summary: %v", err)
	}
	assertLancamentosSummary(t, data, lancamentosResumoEntradasConfirmadas, 58_850, 1)
	assertLancamentosSummary(t, data, lancamentosResumoSaidasConfirmadas, 250_284, 1)
	if data.ResumoLiquido != "- R$ 1.914,34" || !data.ResumoLiquidoNegativo {
		t.Fatalf("filtered net = %q, negative = %v", data.ResumoLiquido, data.ResumoLiquidoNegativo)
	}
	if _, err := handler.buildLancamentosData("", 8, year, LancamentosFilters{}); !errors.Is(err, errReportAggregateOverflow) {
		t.Fatalf("unfiltered overflow = %v, want report aggregate overflow", err)
	}
}

func TestLancamentosMoneyLenBucketScalesWithValueLength(t *testing.T) {
	cases := map[string]string{
		"R$ 0,00":                   "s",
		"R$ 588,50":                 "s",
		"R$ 12.345,67":              "s",
		"R$ 999.999,99":             "m",
		"R$ 9.876.543,21":           "m",
		"R$ 123.456.789,01":         "l",
		"R$ 9.876.543.210,98":       "l",
		"R$ 92.233.720.368.547,75":  "xl",
		"-R$ 92.233.720.368.547,75": "xl",
	}
	for amount, want := range cases {
		if got := lancamentosMoneyLenBucket(amount); got != want {
			t.Fatalf("bucket(%q) = %q, want %q", amount, got, want)
		}
	}
}

// O template real precisa renderizar as cinco métricas, sem o agregado genérico
// "Pago", com os ganchos de escala (data-len) e o swap OOB preservado.
func TestLancamentosResumoRenderContract(t *testing.T) {
	tpl := template.Must(template.New("lancamentos-resumo-contract").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(resolveTemplatePath(t, "templates/pages/lancamentos.html")))

	transactions := []TransactionRow{
		{Type: models.TransactionTypeIncome, Amount: 63850, PaymentStatus: models.PaymentStatusPaid},
		{Type: models.TransactionTypeExpense, Amount: 42798, PaymentStatus: models.PaymentStatusPaid},
		{Type: models.TransactionTypeExpense, Amount: 12000, IsPending: true},
		{Type: models.TransactionTypeExpense, Amount: 5990, IsPending: true, IsOverdue: true},
	}
	data := LancamentosData{
		MonthLabel:   "Setembro 2026",
		IsBusiness:   false,
		Transactions: transactions,
		ResumoStatus: mustBuildLancamentosStatusSummary(t, transactions, nil),
	}
	applyLancamentosResumoDerivados(&data)

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "lancamentos-resumo", data); err != nil {
		t.Fatalf("execute lancamentos-resumo: %v", err)
	}
	// html/template escapa "+" como "&#43;" no texto; normalizamos só para a
	// asserção, o navegador renderiza o sinal normalmente.
	html := strings.ReplaceAll(buf.String(), "&#43;", "+")

	for _, want := range []string{
		`id="lancamentosResumo"`,
		"Entradas confirmadas",
		"Saídas confirmadas",
		"Pendente",
		"Vencido",
		"Agendado",
		"Resultado líquido",
		"+ R$ 210,52",
		"4 itens",
		`data-len="s"`,
		`data-sensitive`,
		`cb-resumo-tone-ok`,
		`cb-resumo-tone-danger`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("resumo renderizado não contém %q\nhtml:\n%s", want, html)
		}
	}
	if strings.Contains(html, ">Pago<") {
		t.Fatalf("resumo ainda mostra o agregado genérico Pago:\n%s", html)
	}

	// Sem OOB na renderização da página; com OOB no swap parcial.
	if strings.Contains(html, "hx-swap-oob") {
		t.Fatalf("resumo da página não pode trazer hx-swap-oob:\n%s", html)
	}
	data.OOB = true
	buf.Reset()
	if err := tpl.ExecuteTemplate(&buf, "lancamentos-resumo", data); err != nil {
		t.Fatalf("execute lancamentos-resumo oob: %v", err)
	}
	if !strings.Contains(buf.String(), `hx-swap-oob="outerHTML"`) {
		t.Fatalf("resumo parcial precisa do swap OOB:\n%s", buf.String())
	}
}

func mustBuildLancamentosStatusSummary(t *testing.T, transactions []TransactionRow, invoices []InvoiceRow) []LancamentosStatusSummary {
	t.Helper()
	items, err := buildLancamentosStatusSummary(transactions, invoices)
	if err != nil {
		t.Fatalf("buildLancamentosStatusSummary: %v", err)
	}
	return items
}

func assertLancamentosSummary(t *testing.T, data LancamentosData, key string, amount int64, count int) {
	t.Helper()
	item, ok := lancamentosSummaryByKey(data, key)
	if !ok {
		t.Fatalf("summary %q not found in %#v", key, data.ResumoStatus)
	}
	if item.Amount != formatCurrencyLabel(amount) {
		t.Fatalf("summary %s amount = %q, want %q", key, item.Amount, formatCurrencyLabel(amount))
	}
	if item.AmountCents != amount {
		t.Fatalf("summary %s cents = %d, want %d", key, item.AmountCents, amount)
	}
	if item.Count != count {
		t.Fatalf("summary %s count = %d, want %d", key, item.Count, count)
	}
	wantCountLabel := fmt.Sprintf("%d itens", count)
	if count == 1 {
		wantCountLabel = "1 item"
	}
	if item.CountLabel != wantCountLabel {
		t.Fatalf("summary %s count label = %q, want %q", key, item.CountLabel, wantCountLabel)
	}
}

func seedLancamentosStatusSummaryScenario(t *testing.T, db *sql.DB, year int) {
	t.Helper()
	now := time.Now().Unix()
	aug := func(day int) int64 {
		return testUnixDate(fmt.Sprintf("%04d-08-%02d", year, day))
	}

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-summary', 'Summary User', 'summary@test.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-summary', 'Summary WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-summary-other', 'Summary Other', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-summary', 'user-summary', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-summary-other', 'user-summary', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-summary', 'ws-summary', 'Conta Summary', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-summary', 'ws-summary', 'Card Summary', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-summary-other', 'ws-summary-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-summary', 'card-summary', 25, 15, 500000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-food-summary', 'ws-summary', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-card-summary', 'ws-summary', 'Cartao', 'credit-card', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-income-summary', 'ws-summary', 'Salario', 'briefcase', '#22c55e', 'INCOME', NULL, ?)`, now)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('invoice-summary', 'card-summary', ?, ?, ?, 'OPEN', ?)`, fmt.Sprintf("%04d-08", year), aug(5), aug(15), now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES
		('paid-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-food-summary', 'EXPENSE', 11000, ?, 'Paid Hidden', 'paid', ?, ?),
		('pending-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-food-summary', 'EXPENSE', 22000, ?, 'Pending Lunch', 'pending', ?, ?),
		('income-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-income-summary', 'INCOME', 70000, ?, 'Pending Salary', 'pending', ?, ?),
		('other-summary', 'ws-summary-other', 'user-summary', 'checking-summary-other', NULL, 'EXPENSE', 99000, ?, 'Other Workspace', 'pending', ?, ?)`,
		aug(2), now, now,
		aug(3), now, now,
		aug(3), now, now,
		aug(3), now, now,
	)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, scheduled_payment_at, created_at, updated_at) VALUES ('scheduled-summary', 'ws-summary', 'user-summary', 'checking-summary', 'cat-food-summary', 'EXPENSE', 33000, ?, 'Scheduled Rent', 'pending', ?, ?, ?)`, aug(4), aug(20), now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, created_at, updated_at) VALUES ('purchase-summary', 'ws-summary', 'user-summary', 'card-summary', 'cat-card-summary', 'invoice-summary', 'EXPENSE', 44000, ?, 'Card Purchase', 'paid', ?, ?)`, aug(6), now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, created_at, updated_at) VALUES ('payment-summary', 'ws-summary', 'user-summary', 'checking-summary', 'EXPENSE', 5000, ?, ?, 'paid', ?, ?)`, aug(7), invoicePaymentDescription("Card Summary"), now, now)
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at) VALUES ('invoice-payment-summary', 'ws-summary', 'invoice-summary', 'checking-summary', 'payment-summary', 5000, ?, 'manual', 'user-summary', ?)`, aug(7), now)
}
