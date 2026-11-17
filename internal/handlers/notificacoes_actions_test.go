package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

// The notification cards expose one presentation target per computed record and
// a payment shortcut only when the existing status flow can handle it. The
// destination routes keep their own workspace guards.
func TestNotificacoesInteractiveTargetsRespectWorkspaceAndPermissions(t *testing.T) {
	db := seedTenantRuntimeScope(t)

	now := time.Now()
	yesterday := now.Add(-48 * time.Hour).Unix()
	closing := now.Add(-24 * time.Hour).Unix()
	due := now.Add(7 * 24 * time.Hour).Unix()
	monthStart := appMonthStartUTC(now).Add(24 * time.Hour).Unix()

	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, description, status, updated_at)
		VALUES ('tx-overdue-a', 'scope-a', 'scope-user', 'bank-a', 'EXPENSE', 1000, ?, 'Overdue A', 'pending', 123)`, yesterday)
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, description, status, updated_at)
		VALUES ('tx-overdue-b', 'scope-b', 'scope-user', 'bank-b', 'EXPENSE', 2000, ?, 'Overdue B', 'pending', 456)`, yesterday)
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, type, amount, date, description, status, updated_at)
		VALUES ('tx-overdue-card', 'scope-a', 'scope-user', 'card-a', 'EXPENSE', 500, ?, 'Card pending', 'pending', 789)`, yesterday)

	execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED', closing_date = ?, due_date = ? WHERE id IN ('inv-a', 'inv-b')`, closing, due)

	execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES
		('limit-a', 'scope-a', 'cat-a', 1000),
		('limit-b', 'scope-b', 'cat-b', 1000)`)
	execTestSQL(t, db, `INSERT INTO transactions
		(id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, updated_at)
		VALUES
		('tx-limit-a', 'scope-a', 'scope-user', 'bank-a', 'cat-a', 'EXPENSE', 2000, ?, 'Limit A', 'paid', 111),
		('tx-limit-b', 'scope-b', 'scope-user', 'bank-b', 'cat-b', 'EXPENSE', 5000, ?, 'Limit B', 'paid', 222)`,
		monthStart, monthStart)

	execTestSQL(t, db, `INSERT INTO user_notifications
		(id, user_id, workspace_id, title, message, type, created_at)
		VALUES
		('scheduled-a', 'scope-user', 'scope-a', 'Pagamento automático executado', 'A', ?, 100),
		('scheduled-b', 'scope-user', 'scope-b', 'Pagamento automático executado', 'B', ?, 200)`,
		scheduledPaymentNotificationType, scheduledPaymentNotificationType)

	handler := NotificacoesHandler{
		DB:                     db,
		WorkspaceID:            "scope-a",
		UserID:                 "scope-user",
		CanReadTransactions:    true,
		CanViewDashboard:       true,
		CanUpdatePaymentStatus: true,
	}

	pending := handler.pendingOverdueExpenses()
	if len(pending) != 2 {
		t.Fatalf("pending items = %d, want 2 (bank + card fixture)", len(pending))
	}
	byID := make(map[string]NotificationItem, len(pending))
	for _, item := range pending {
		byID[item.TargetID] = item
	}
	overdue, ok := byID["tx-overdue-a"]
	if !ok {
		t.Fatalf("own overdue notification missing: %+v", pending)
	}
	if overdue.TargetType != notificationTargetTransaction || !overdue.CanOpen || !overdue.CanPay {
		t.Fatalf("overdue target/permissions = %+v", overdue)
	}
	if overdue.PaymentAccountID != "bank-a" || overdue.PaymentAccountName != "bank-a" || overdue.PaymentType != models.TransactionTypeExpense {
		t.Fatalf("overdue payment data = %+v", overdue)
	}
	if overdue.PaymentAmount != formatCurrencyLabel(1000) {
		t.Fatalf("overdue payment amount = %q", overdue.PaymentAmount)
	}
	card, ok := byID["tx-overdue-card"]
	if !ok {
		t.Fatalf("credit-card pending notification missing: %+v", pending)
	}
	if !card.CanOpen || card.CanPay {
		t.Fatalf("credit-card schedule eligibility = %+v", card)
	}
	if _, leaked := byID["tx-overdue-b"]; leaked {
		t.Fatal("cross-workspace overdue notification leaked")
	}

	invoices := handler.closedInvoices()
	if len(invoices) != 1 || invoices[0].TargetID != "inv-a" || invoices[0].TargetType != notificationTargetInvoice || !invoices[0].CanOpen {
		t.Fatalf("closed invoice target = %+v", invoices)
	}

	limits := handler.exceededCostLimits(false)
	if len(limits) != 1 || limits[0].TargetID != "limit-a" || limits[0].TargetType != notificationTargetLimit || !limits[0].CanOpen {
		t.Fatalf("cost limit target = %+v", limits)
	}

	scheduled := handler.scheduledPaymentNotifications()
	if len(scheduled) != 1 || scheduled[0].TargetType != "" || scheduled[0].TargetID != "" || scheduled[0].CanOpen || scheduled[0].HasMoney {
		t.Fatalf("scheduled receipt contract = %+v", scheduled)
	}

	restricted := handler
	restricted.CanReadTransactions = false
	restricted.CanViewDashboard = false
	restricted.CanUpdatePaymentStatus = false
	for _, item := range restricted.pendingOverdueExpenses() {
		if item.CanOpen || item.CanPay {
			t.Fatalf("restricted pending item exposed actions: %+v", item)
		}
	}
	for _, item := range restricted.closedInvoices() {
		if item.CanOpen {
			t.Fatalf("restricted invoice item exposed link: %+v", item)
		}
	}
	for _, item := range restricted.exceededCostLimits(false) {
		if item.CanOpen {
			t.Fatalf("restricted limit item exposed link: %+v", item)
		}
	}
}

func TestNotificacoesPageRendersIndependentControls(t *testing.T) {
	tpl := testNotificacoesRenderTemplates(t)
	data := NotificacoesData{
		Title: "Notificações",
		Items: []NotificationItem{
			{
				Key:         "invoice:inv-1",
				Title:       "Fatura aguardando pagamento",
				Description: "Cartão A · ref. 2026-08 · vence em 10/09",
				Money:       MoneyMinor(12345),
				HasMoney:    true,
				Icon:        "triangle-alert",
				Color:       "amber",
				TargetType:  notificationTargetInvoice,
				TargetID:    "inv-1",
				CanOpen:     true,
			},
			{
				Key:         "limit:limit-1",
				Title:       "Limite de categoria estourado",
				Description: "Mercado · limite R$ 100,00",
				Money:       MoneyMinor(2500),
				HasMoney:    true,
				Icon:        "gauge",
				Color:       "red",
				TargetType:  notificationTargetLimit,
				TargetID:    "limit-1",
				CanOpen:     true,
			},
			{
				Key:                "transaction:tx-1",
				Title:              "Despesa vencida",
				Description:        "Aluguel · vencida em 05/09",
				Money:              MoneyMinor(1000),
				HasMoney:           true,
				Icon:               "clock-alert",
				Color:              "rose",
				TargetType:         notificationTargetTransaction,
				TargetID:           "tx-1",
				CanOpen:            true,
				CanPay:             true,
				PaymentType:        models.TransactionTypeExpense,
				PaymentAccountID:   "bank-1",
				PaymentAccountName: "Conta principal",
				PaymentAmount:      formatCurrencyLabel(1000),
			},
			{
				Key:         "scheduled_payment_notif:scheduled-1",
				Title:       "Pagamento automático executado",
				Description: "Aluguel em 05/09.",
				Icon:        "badge-check",
				Color:       "emerald",
			},
		},
	}

	var out strings.Builder
	if err := tpl.ExecuteTemplate(&out, "notificacoes-page", data); err != nil {
		t.Fatalf("render notificacoes page: %v", err)
	}
	body := out.String()

	for _, want := range []string{
		`href="/cartoes/faturas/inv-1"`,
		`aria-label="Abrir fatura: Cartão A · ref. 2026-08 · vence em 10/09"`,
		`href="/metas/limite/historico?limit_id=limit-1"`,
		`hx-target="#bottom-sheet-container"`,
		`href="/transacoes/tx-1?origem=notificacoes"`,
		`hx-get="/transacoes/tx-1?origem=notificacoes"`,
		`data-payment-confirm`,
		`data-payment-post-url="/transacoes/tx-1/status-pagamento"`,
		`data-payment-account-id="bank-1"`,
		`data-payment-account-name="Conta principal"`,
		`data-payment-amount="R$ 10,00"`,
		`data-payment-type="EXPENSE"`,
		`data-payment-context="notificacoes"`,
		`data-payment-account-change-allowed="false"`,
		`data-sensitive`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("notificacoes page missing %q", want)
		}
	}
	if got := strings.Count(body, `data-notification-open`); got != 3 {
		t.Fatalf("actionable links = %d, want 3", got)
	}
	if got := strings.Count(body, `data-payment-confirm>`); got != 1 {
		t.Fatalf("payment actions = %d, want 1", got)
	}
	payButton := regexp.MustCompile(`(?s)<button[^>]*data-payment-confirm[^>]*>`).FindString(body)
	if payButton == "" || strings.Contains(payButton, "hx-post") || strings.Contains(payButton, "hx-put") {
		t.Fatalf("payment shortcut must open the modal without mutating: %q", payButton)
	}
	emptyLink := regexp.MustCompile(`(?s)<a[^>]*data-notification-open[^>]*></a>`)
	if got := len(emptyLink.FindAllString(body, -1)); got != 3 {
		t.Fatalf("main links must stay empty overlays, got %d", got)
	}
	if strings.Contains(body, "R$ 0,00") {
		t.Fatal("scheduled receipt must not render a fake zero amount")
	}

	restricted := data
	for i := range restricted.Items {
		restricted.Items[i].CanOpen = false
		restricted.Items[i].CanPay = false
	}
	var restrictedOut strings.Builder
	if err := tpl.ExecuteTemplate(&restrictedOut, "notificacoes-page", restricted); err != nil {
		t.Fatalf("render restricted page: %v", err)
	}
	restrictedBody := restrictedOut.String()
	if strings.Contains(restrictedBody, "data-notification-open") {
		t.Fatal("restricted page rendered a navigation link")
	}
	if strings.Contains(restrictedBody, "data-payment-confirm>") {
		t.Fatal("restricted page rendered a payment action")
	}
}

func TestNotificacoesEditReturnContextUsesRefresh(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedFormSchedulingScenario(t, db)

	today := civildate.CivilDateToday(appTimeLocation()).String()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			date, date_civil, description, status, created_at, updated_at
		) VALUES (
			'tx-notification-edit', 'ws-test', 'user-test', 'checking-test', 'cat-form-expense',
			'EXPENSE', 1000, ?, ?, 'Notification edit', 'pending', ?, ?
		)
	`, MustCivilWriteDate(today).LegacyUnix(), today, now, now)

	var captured FormTransacaoData
	handler := testPaidInvoiceMutationHandler(db)
	handler.Templates = mustTemplateWithCapture(t, "form-lancamento", func(data FormTransacaoData) {
		captured = data
	})

	getRR := httptest.NewRecorder()
	handler.HandleDetalheTransacao(getRR, httptest.NewRequest(http.MethodGet, "/transacoes/tx-notification-edit?origem=notificacoes", nil), "tx-notification-edit")
	if getRR.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %q", getRR.Code, getRR.Body.String())
	}
	if captured.ReturnContext != notificacoesReturnContext {
		t.Fatalf("return context = %q", captured.ReturnContext)
	}

	unknownRR := httptest.NewRecorder()
	handler.HandleDetalheTransacao(unknownRR, httptest.NewRequest(http.MethodGet, "/transacoes/tx-notification-edit?origem=outro", nil), "tx-notification-edit")
	if unknownRR.Code != http.StatusOK || captured.ReturnContext != "" {
		t.Fatalf("unknown context accepted: status=%d context=%q", unknownRR.Code, captured.ReturnContext)
	}

	updateRR := httptest.NewRecorder()
	updateReq := newMultipartUpdateRequest(t, "/transacoes/tx-notification-edit?origem=notificacoes", map[string]string{
		"tipo":             "despesa",
		"valor":            "10,00",
		"descricao":        "Notification edit",
		"data":             today,
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "pending",
		"escopo_edicao":    "single",
	})
	updateReq.Header.Set("HX-Request", "true")
	handler.HandleAtualizarTransacao(updateRR, updateReq, "tx-notification-edit")
	if updateRR.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %q", updateRR.Code, updateRR.Body.String())
	}
	if got := updateRR.Header().Get("HX-Refresh"); got != "true" {
		t.Fatalf("update HX-Refresh = %q, want true", got)
	}
	if updateRR.Body.Len() != 0 {
		t.Fatalf("update body should be empty for refresh, got %q", updateRR.Body.String())
	}
}

func TestNotificacoesDestinationRoutesRejectForeignWorkspace(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES ('limit-b-foreign', 'scope-b', 'cat-b', 1000)`)

	faturas := FaturasHandler{DB: db, WorkspaceID: "scope-a"}
	invoiceOK := httptest.NewRecorder()
	faturas.HandleFaturaConceitoPorID(invoiceOK, httptest.NewRequest(http.MethodGet, "/cartoes/faturas/inv-a", nil), "inv-a")
	if invoiceOK.Code != http.StatusSeeOther {
		t.Fatalf("own invoice status = %d", invoiceOK.Code)
	}
	location := invoiceOK.Header().Get("Location")
	if !strings.Contains(location, "cartao=card-a") || !strings.Contains(location, "mes=8") || !strings.Contains(location, "ano=2099") {
		t.Fatalf("invoice hub redirect lost stored reference: %q", location)
	}

	invoiceRR := httptest.NewRecorder()
	faturas.HandleFaturaConceitoPorID(invoiceRR, httptest.NewRequest(http.MethodGet, "/cartoes/faturas/inv-b", nil), "inv-b")
	if invoiceRR.Code != http.StatusNotFound {
		t.Fatalf("foreign invoice status = %d", invoiceRR.Code)
	}

	trans := TransactionHandler{DB: db, WorkspaceID: "scope-a", UserID: "scope-user"}
	transactionRR := httptest.NewRecorder()
	trans.HandleDetalheTransacao(transactionRR, httptest.NewRequest(http.MethodGet, "/transacoes/tx-b", nil), "tx-b")
	if transactionRR.Code != http.StatusNotFound {
		t.Fatalf("foreign transaction status = %d", transactionRR.Code)
	}

	metas := MetasHandler{DB: db, WorkspaceID: "scope-a", UserID: "scope-user", ActorRole: "ADMIN"}
	limitRR := httptest.NewRecorder()
	metas.HandleHistoricoLimite(limitRR, httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id=limit-b-foreign", nil))
	if limitRR.Code != http.StatusNotFound {
		t.Fatalf("foreign limit status = %d, body = %q", limitRR.Code, limitRR.Body.String())
	}
}

func mustTemplateWithCapture(t *testing.T, name string, capture func(FormTransacaoData)) *template.Template {
	t.Helper()
	return template.Must(template.New("test").Funcs(template.FuncMap{
		"capture": func(data FormTransacaoData) string {
			capture(data)
			return "captured"
		},
	}).Parse(`{{define "form-lancamento"}}{{capture .}}{{end}}`))
}
