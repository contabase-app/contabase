package handlers

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func previewFinancialCardForTest(t *testing.T, db *sql.DB, base f1Base, now time.Time, month string) (DashboardPreviewData, string) {
	t.Helper()
	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", month)
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-financial", data); err != nil {
		t.Fatalf("render financial card: %v", err)
	}
	return data, out.String()
}

func previewFinancePanelBody(body, key string) string {
	marker := `id="cbp-finance-panel-` + key + `"`
	start := strings.Index(body, marker)
	if start < 0 {
		return ""
	}
	panel := body[start:]
	if next := strings.Index(panel[1:], `id="cbp-finance-panel-`); next >= 0 {
		panel = panel[:next+1]
	}
	if end := strings.Index(panel, "</section>"); end >= 0 {
		panel = panel[:end]
	}
	return panel
}

func TestDashboardPreviewFinancialIntegration(t *testing.T) {
	t.Run("current month complete", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := seedDashboardPreviewScenario(t, now)
		defer db.Close()

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if selected.Label != "Novembro/2026" || !selected.IsCurrent || selected.StateLabel != "Projeção completa" {
			t.Fatalf("selected month = %+v", selected)
		}
		if got := selected.RealizedIncome.rawCents(); got != 500000 {
			t.Fatalf("realized income = %d, want 500000", got)
		}
		if got := selected.RealizedExpense.rawCents(); got != 200000 {
			t.Fatalf("realized expense = %d, want 200000", got)
		}
		if got := selected.PendingExpense.rawCents(); got != 80000 {
			t.Fatalf("pending expense = %d, want 80000", got)
		}
		if got := selected.ProjectedNet.rawCents(); got != 220000 {
			t.Fatalf("projected net = %d, want 220000", got)
		}
		if selected.RealizedNegative || selected.RealizedToneClass != "is-positive" {
			t.Fatalf("positive realized tone = %+v", selected)
		}
		for _, want := range []string{"Visão financeira", "Realizado", "Movimentação do mês.", "Projetado", "Compromissos registrados.", "Entradas", "A receber", "Saídas", "A pagar", "Resultado realizado", "Resultado projetado", "Projeção completa", "Novembro/2026", "Dezembro/2026", "Janeiro/2027"} {
			if !strings.Contains(body, want) {
				t.Fatalf("complete card missing %q", want)
			}
		}
	})

	t.Run("year boundary", func(t *testing.T) {
		now := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)
		db, base := seedDashboardPreviewScenario(t, now)
		defer db.Close()

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		if len(data.Financial.Months) != 3 {
			t.Fatalf("months = %d, want 3", len(data.Financial.Months))
		}
		labels := []string{data.Financial.Months[0].Label, data.Financial.Months[1].Label, data.Financial.Months[2].Label}
		want := []string{"Dezembro/2026", "Janeiro/2027", "Fevereiro/2027"}
		for i := range want {
			if labels[i] != want[i] {
				t.Fatalf("month %d = %q, want %q", i, labels[i], want[i])
			}
		}
		for _, label := range want {
			if !strings.Contains(body, label) {
				t.Fatalf("year-boundary card missing %q", label)
			}
		}
	})

	t.Run("past month is historical", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-preview-fin-past")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-past-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 12000, Status: "paid", Description: "Receita outubro", CreatedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC).Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-past-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 5000, Status: "paid", Description: "Despesa outubro", CreatedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).Unix())

		data, body := previewFinancialCardForTest(t, db, base, now, "2026-10")
		selected := data.Financial.Selected
		if !selected.IsPast || selected.StateLabel != "Histórico" {
			t.Fatalf("past selection = %+v", selected)
		}
		if selected.PendingAvailable || selected.ProjectedAvailable {
			t.Fatalf("past month must not project: %+v", selected)
		}
		if got := selected.RealizedNet.rawCents(); got != 7000 {
			t.Fatalf("past realized net = %d, want 7000", got)
		}
		firstPanel := previewFinancePanelBody(body, "2026-10")
		if firstPanel == "" {
			t.Fatal("historical panel missing")
		}
		for _, want := range []string{"Histórico", "Resultado realizado", "Entradas", "Saídas"} {
			if !strings.Contains(firstPanel, want) {
				t.Fatalf("historical panel missing %q", want)
			}
		}
		if strings.Contains(firstPanel, "Resultado projetado") || strings.Contains(firstPanel, `aria-label="Projetado"`) {
			t.Fatal("historical panel must not render projected result")
		}
	})

	t.Run("partial recurrence", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := seedDashboardPreviewScenario(t, now)
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, active, created_at, updated_at) VALUES ('preview-recurring', ?, ?, ?, 'EXPENSE', 1000, 'Recorrente', ?, 'MONTHLY', 1, ?, ?)`, base.ws, base.user, base.checking, now.Unix(), now.Unix(), now.Unix())

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if selected.StateLabel != "Projeção parcial" || !data.Financial.Recurrence.ShowWarning {
			t.Fatalf("partial recurrence = %+v/%+v", selected, data.Financial.Recurrence)
		}
		if !selected.ProjectedAvailable {
			t.Fatal("partial recurrence with registered pending must still expose projected result")
		}
		for _, want := range []string{"Projeção parcial", "Recorrências futuras podem estar incompletas"} {
			if !strings.Contains(body, want) {
				t.Fatalf("partial card missing %q", want)
			}
		}
	})

	t.Run("unavailable F1", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := seedDashboardPreviewScenario(t, now)
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "preview-f1-invalid", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, Type: "EXPENSE", Amount: 5000, Status: "pending", Description: "Cartão sem fatura", CreatedAt: now.Unix()})

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if data.Financial.Error || selected.StateLabel != "Projeção indisponível" {
			t.Fatalf("unavailable F1 = %+v/%+v", data.Financial, selected)
		}
		if selected.PendingAvailable || selected.ProjectedAvailable {
			t.Fatalf("unavailable month must not project: %+v", selected)
		}
		if got := selected.RealizedIncome.rawCents(); got != 500000 {
			t.Fatalf("realized income must remain visible, got %d", got)
		}
		for _, want := range []string{"Projeção indisponível", "Indisponível", "Entradas"} {
			if !strings.Contains(body, want) {
				t.Fatalf("unavailable card missing %q", want)
			}
		}
		if strings.Contains(body, "Resultado projetado R$ 0,00") {
			t.Fatal("unavailable projection must not become zero")
		}
	})

	t.Run("no pending registered", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-preview-fin-no-pending")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-no-pending-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "paid", Description: "Receita", CreatedAt: now.Unix()}, now.Unix())

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if selected.StateLabel != "Projeção parcial" || !selected.NoPendingRegistered || selected.ProjectedAvailable {
			t.Fatalf("no-pending month = %+v", selected)
		}
		for _, want := range []string{"Projeção parcial", "Nenhum compromisso pendente registrado"} {
			if !strings.Contains(body, want) {
				t.Fatalf("no-pending card missing %q", want)
			}
		}
		if strings.Contains(body, "Resultado projetado R$ 0,00") {
			t.Fatal("no pending must not be presented as projected zero")
		}
	})

	t.Run("negative realized and projected", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-preview-fin-negative")
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-neg-income", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 10000, Status: "paid", Description: "Receita", CreatedAt: now.Unix()}, now.Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-neg-expense", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 30000, Status: "paid", Description: "Despesa", CreatedAt: now.Unix()}, now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "preview-neg-pending", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "pending", Description: "Pendente", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if !selected.RealizedNegative || selected.RealizedToneClass != "is-negative" {
			t.Fatalf("negative realized = %+v", selected)
		}
		if !selected.ProjectedNegative || selected.ProjectedLabel != "Déficit projetado" || selected.ProjectedToneClass != "is-negative" {
			t.Fatalf("negative projected = %+v", selected)
		}
		for _, want := range []string{"Déficit projetado", "Resultado realizado", "is-negative"} {
			if !strings.Contains(body, want) {
				t.Fatalf("negative card missing %q", want)
			}
		}
		if strings.Contains(strings.ToLower(body), "dívida") {
			t.Fatal("negative result must not be called debt")
		}
	})

	t.Run("reversal is a cash adjustment", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-preview-fin-reversal")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('preview-rev-inv', ?, '2026-11', ?, ?, 'CLOSED', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "preview-rev-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "preview-rev-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-rev-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 50000, Status: "paid", Description: invoicePaymentDescription("Cartão"), CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix())
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-rev-reversal", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "INCOME", Amount: 50000, Status: "paid", Description: "Ajuste atípico de caixa", CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix())
		addF1InvoicePayment(t, db, "preview-rev-ip", base.ws, "preview-rev-inv", base.checking, "preview-rev-payment", "preview-rev-reversal", 50000, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix(), base.user, now.Unix())

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if got := selected.AdjustmentIncome.rawCents(); got != 50000 {
			t.Fatalf("adjustment income = %d, want 50000", got)
		}
		if got := selected.RealizedIncome.rawCents(); got != 0 {
			t.Fatalf("reversal must not inflate operational income, got %d", got)
		}
		if !selected.HasAdjustments {
			t.Fatal("reversal must expose HasAdjustments")
		}
		for _, want := range []string{"Ajustes de caixa", "reversões de pagamento, fora da receita operacional"} {
			if !strings.Contains(body, want) {
				t.Fatalf("reversal card missing %q", want)
			}
		}
	})

	t.Run("partial invoice has no double count", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-preview-fin-partial-invoice")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('preview-partial-inv', ?, '2026-11', ?, ?, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "preview-partial-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "preview-partial-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-partial-payment", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "paid", Description: invoicePaymentDescription("Cartão"), CreatedAt: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix()}, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Unix())
		addF1InvoicePayment(t, db, "preview-partial-ip", base.ws, "preview-partial-inv", base.checking, "preview-partial-payment", "", 20000, 0, base.user, now.Unix())

		data, _ := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if got := selected.RealizedExpense.rawCents(); got != 20000 {
			t.Fatalf("realized invoice payment = %d, want 20000", got)
		}
		if got := selected.PendingExpense.rawCents(); got != 30000 {
			t.Fatalf("pending invoice residual = %d, want 30000", got)
		}
		if got := selected.ProjectedNet.rawCents(); got != -50000 {
			t.Fatalf("projected invoice result = %d, want -50000 (payment + residual)", got)
		}
	})

	t.Run("invoice without due date is not allocated", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := newProjectionFixture(t, "ws-preview-fin-invoice-undated")
		defer db.Close()
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at) VALUES ('preview-undated-inv', ?, '2026-11', ?, 0, 'OPEN', 0, ?)`, base.card, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Unix(), now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "preview-undated-purchase", WorkspaceID: base.ws, UserID: base.user, AccountID: base.card, InvoiceID: "preview-undated-inv", Type: "EXPENSE", Amount: 50000, Status: "paid", Description: "Compra sem vencimento", CreatedAt: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).Unix()})

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if selected.PendingCount != 0 || selected.PendingExpense.rawCents() != 0 || selected.ProjectedAvailable {
			t.Fatalf("undated invoice leaked into month: %+v", selected)
		}
		if selected.StateLabel != "Projeção parcial" || data.Financial.ObligationsWithoutDateCount != 1 {
			t.Fatalf("undated invoice state = %+v/%d", selected, data.Financial.ObligationsWithoutDateCount)
		}
		if data.Financial.Undated.HasRegistered {
			t.Fatalf("invoice residual must stay outside Undated amount: %+v", data.Financial.Undated)
		}
		for _, want := range []string{"Compromissos sem vencimento", "A composição financeira registra", "não é exibido", "Não há pendentes datados neste mês"} {
			if !strings.Contains(body, want) {
				t.Fatalf("undated invoice card missing %q", want)
			}
		}
	})

	t.Run("overdue is separate", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := seedDashboardPreviewScenario(t, now)
		defer db.Close()
		addF1Tx(t, db, f1TxSeed{ID: "preview-overdue", WorkspaceID: base.ws, UserID: base.user, AccountID: base.checking, Type: "EXPENSE", Amount: 20000, Status: "pending", Description: "Vencida", DueDate: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})

		data, body := previewFinancialCardForTest(t, db, base, now, "")
		if !data.Financial.Overdue.Available || !data.Financial.Overdue.HasRegistered || !data.Financial.Overdue.HasExpense {
			t.Fatalf("overdue = %+v", data.Financial.Overdue)
		}
		if got := data.Financial.Overdue.Expense.rawCents(); got != 20000 {
			t.Fatalf("overdue expense = %d, want 20000", got)
		}
		if !strings.Contains(body, "Vencidos de meses anteriores") {
			t.Fatal("overdue section missing")
		}
	})

	t.Run("workspace isolation", func(t *testing.T) {
		now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
		db, base := seedDashboardPreviewScenario(t, now)
		defer db.Close()
		addProjectionPaidTx(t, db, f1TxSeed{ID: "preview-other-income", WorkspaceID: "ws-preview-other", UserID: "user-ws-preview-other", AccountID: "check-ws-preview-other", Type: "INCOME", Amount: 900000, Status: "paid", Description: "Outro workspace", CreatedAt: now.Unix()}, now.Unix())
		addF1Tx(t, db, f1TxSeed{ID: "preview-other-pending", WorkspaceID: "ws-preview-other", UserID: "user-ws-preview-other", AccountID: "check-ws-preview-other", Type: "EXPENSE", Amount: 80000, Status: "pending", Description: "Outro workspace", DueDate: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix(), CreatedAt: now.Unix()})

		data, _ := previewFinancialCardForTest(t, db, base, now, "")
		selected := data.Financial.Selected
		if got := selected.RealizedIncome.rawCents(); got != 500000 {
			t.Fatalf("workspace leak in realized income: %d", got)
		}
		if got := selected.PendingExpense.rawCents(); got != 80000 {
			t.Fatalf("workspace leak in pending expense: %d", got)
		}
	})

	t.Run("aggregator error is not zero", func(t *testing.T) {
		view := buildPreviewFinancialView(DashboardFinancialProjection{}, errors.New("projection failed"), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
		if !view.Error || view.Available || view.ErrorMessage == "" {
			t.Fatalf("error view = %+v", view)
		}
		data := DashboardPreviewData{SelectedMonthLabel: "Novembro/2026", Financial: view}
		var out strings.Builder
		if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-financial", data); err != nil {
			t.Fatalf("render error card: %v", err)
		}
		body := out.String()
		for _, want := range []string{"Projeção indisponível", "Não foi possível carregar a visão financeira agora."} {
			if !strings.Contains(body, want) {
				t.Fatalf("error card missing %q", want)
			}
		}
		if strings.Contains(body, "R$ 0,00") {
			t.Fatal("aggregator error must not render zero")
		}
	})
}

func TestDashboardPreviewFinancialTabs(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data, body := previewFinancialCardForTest(t, db, base, now, "")
	if len(data.Financial.Months) != 3 {
		t.Fatalf("months = %d, want 3", len(data.Financial.Months))
	}
	wantTabs := []struct {
		key  string
		tab  string
		aria string
	}{
		{key: "2026-11", tab: "Nov", aria: "Nov, Novembro de 2026"},
		{key: "2026-12", tab: "Dez", aria: "Dez, Dezembro de 2026"},
		{key: "2027-01", tab: "Jan", aria: "Jan, Janeiro de 2027"},
	}
	for i, want := range wantTabs {
		month := data.Financial.Months[i]
		if month.TabLabel != want.tab || month.TabAriaLabel != want.aria {
			t.Fatalf("month %d labels = %q/%q, want %q/%q", i, month.TabLabel, month.TabAriaLabel, want.tab, want.aria)
		}
		for _, fragment := range []string{
			`id="cbp-finance-tab-` + want.key + `"`,
			`aria-controls="cbp-finance-panel-` + want.key + `"`,
			`aria-label="` + want.aria + `"`,
			`id="cbp-finance-panel-` + want.key + `"`,
			`aria-labelledby="cbp-finance-tab-` + want.key + `"`,
			`>` + want.tab + `</span>`,
		} {
			if !strings.Contains(body, fragment) {
				t.Fatalf("financial tabs missing %q", fragment)
			}
		}
	}
	if got := strings.Count(body, `role="tab"`); got != 3 {
		t.Fatalf("tabs = %d, want 3", got)
	}
	if got := strings.Count(body, `role="tabpanel"`); got != 3 {
		t.Fatalf("tab panels = %d, want 3", got)
	}
	if got := strings.Count(body, `aria-selected="true"`); got != 1 {
		t.Fatalf("selected tabs = %d, want 1", got)
	}
	if got := strings.Count(body, `data-cbp-finance-panel hidden`); got != 2 {
		t.Fatalf("hidden panels = %d, want 2", got)
	}
	if !strings.Contains(body, `data-cbp-finance-panel>`) {
		t.Fatal("first panel must remain visible in the initial HTML")
	}
	if got := strings.Count(body, `tabindex="0"`); got != 1 {
		t.Fatalf("roving tabindex 0 = %d, want 1", got)
	}
	if got := strings.Count(body, `tabindex="-1"`); got != 2 {
		t.Fatalf("roving tabindex -1 = %d, want 2", got)
	}
	if !strings.Contains(body, `data-cbp-finance-tabs`) {
		t.Fatal("tablist marker missing")
	}
	firstPanel := previewFinancePanelBody(body, "2026-11")
	if firstPanel == "" || !strings.Contains(firstPanel, "Novembro/2026") {
		t.Fatalf("first panel = %q", firstPanel)
	}
	if strings.Contains(firstPanel, "Resultado projetado R$ 0,00") {
		t.Fatal("first panel must use the aggregator result, not a client-side zero")
	}
}

func TestDashboardPreviewFinancialTabsKeepPerMonthTemporalState(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data, body := previewFinancialCardForTest(t, db, base, now, "2026-10")
	if len(data.Financial.Months) != 3 {
		t.Fatalf("months = %d, want 3", len(data.Financial.Months))
	}
	if !data.Financial.Months[0].IsPast || !data.Financial.Months[1].IsCurrent || !data.Financial.Months[2].IsFuture {
		t.Fatalf("month temporal states = %+v", data.Financial.Months)
	}
	firstPanel := previewFinancePanelBody(body, "2026-10")
	secondPanel := previewFinancePanelBody(body, "2026-11")
	if firstPanel == "" || secondPanel == "" {
		t.Fatal("historical/current panels missing")
	}
	if !strings.Contains(firstPanel, "Histórico") || strings.Contains(firstPanel, "Resultado projetado") {
		t.Fatalf("historical panel = %q", firstPanel)
	}
	if !strings.Contains(secondPanel, "Novembro/2026") || !strings.Contains(secondPanel, "Resultado projetado") {
		t.Fatalf("current panel = %q", secondPanel)
	}
}

func TestDashboardPreviewFinancialPrivacyAndMonthFilter(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardPreviewDataAt(db, base.user, base.ws, now, "6m", "2026-10")
	if data.Financial.MonthLabel != data.SelectedMonthLabel {
		t.Fatalf("financial month label = %q, selected = %q", data.Financial.MonthLabel, data.SelectedMonthLabel)
	}
	if data.MonthFilter.IsCurrent || data.Financial.Selected.IsCurrent {
		t.Fatalf("historical month filter = %+v / %+v", data.MonthFilter, data.Financial.Selected)
	}
	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render privacy page: %v", err)
	}
	body := out.String()
	finance := previewSectionBody(t, body, "finance")
	if got := strings.Count(finance, "data-sensitive"); got < 5 {
		t.Fatalf("financial card sensitive fields = %d, want at least 5", got)
	}
	if strings.Contains(finance, `href="#"`) {
		t.Fatal("financial card must not contain decorative href")
	}
	if strings.Contains(finance, "Orçamento do mês") {
		t.Fatal("old budget card must be removed from the variant")
	}
}
