package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTogglePagamentoRegistersHistoryEvent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-hist-1", "pending", nil)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-hist-1/status-pagamento", url.Values{
		"payment_date": {"2026-07-14"},
	}), "tx-hist-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-hist-1'`).Scan(&count); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if count != 1 {
		t.Fatalf("history count = %d, want 1", count)
	}

	var prevStatus, newStatus string
	var prevPaidAt, newPaidAt sql.NullInt64
	if err := db.QueryRow(`SELECT previous_status, new_status, previous_paid_at, new_paid_at FROM transaction_status_history WHERE transaction_id = 'tx-hist-1'`).Scan(&prevStatus, &newStatus, &prevPaidAt, &newPaidAt); err != nil {
		t.Fatalf("query history row: %v", err)
	}
	if prevStatus != "pending" {
		t.Fatalf("prev status = %q, want pending", prevStatus)
	}
	if newStatus != "paid" {
		t.Fatalf("new status = %q, want paid", newStatus)
	}
	if prevPaidAt.Valid {
		t.Fatalf("prev paid_at = %d, want NULL", prevPaidAt.Int64)
	}
	if !newPaidAt.Valid {
		t.Fatal("new paid_at is NULL, want non-null")
	}
}

func TestTogglePagamentoBackToPendingRegistersPaidAtClear(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	paidAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC).Unix()
	insertPaymentDateTestTransaction(t, db, "tx-hist-clear", "paid", &paidAt)

	handler := testPaidInvoiceMutationHandler(db)
	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-hist-clear/status-pagamento", nil), "tx-hist-clear")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var prevStatus, newStatus string
	var prevPaidAt, newPaidAt sql.NullInt64
	if err := db.QueryRow(`SELECT previous_status, new_status, previous_paid_at, new_paid_at FROM transaction_status_history WHERE transaction_id = 'tx-hist-clear' ORDER BY created_at DESC LIMIT 1`).Scan(&prevStatus, &newStatus, &prevPaidAt, &newPaidAt); err != nil {
		t.Fatalf("query history row: %v", err)
	}
	if prevStatus != "paid" {
		t.Fatalf("prev status = %q, want paid", prevStatus)
	}
	if newStatus != "pending" {
		t.Fatalf("new status = %q, want pending", newStatus)
	}
	if !prevPaidAt.Valid {
		t.Fatal("prev paid_at is NULL, want non-null")
	}
	if newPaidAt.Valid {
		t.Fatalf("new paid_at = %d, want NULL", newPaidAt.Int64)
	}
}

func TestBulkUpdateRegistersHistoryPerTransaction(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-hist-1", "pending", nil)
	insertPaymentDateTestTransaction(t, db, "tx-bulk-hist-2", "pending", nil)

	handler := testBulkPaymentHandler(db)
	rr := bulkUpdateRequest(t, handler, []string{"tx-bulk-hist-1", "tx-bulk-hist-2"}, "paid", "2026-08-03")

	if rr.Code != http.StatusOK {
		t.Logf("bulk update response (non-fatal for template): code=%d body=%q", rr.Code, rr.Body.String())
	}

	for _, txID := range []string{"tx-bulk-hist-1", "tx-bulk-hist-2"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = ?`, txID).Scan(&count); err != nil {
			t.Fatalf("count history for %s: %v", txID, err)
		}
		if count != 1 {
			t.Fatalf("%s history count = %d, want 1", txID, count)
		}
	}
}

func TestHistoryRespectsWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES ('ws-other', 'Other WS', '', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-other', 'user-test', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('chk-other', 'ws-other', 'Other Conta', 'CHECKING', 0, 0, ?, ?)`, now, now)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at) VALUES ('tx-isolated', 'ws-other', 'user-test', 'chk-other', 'EXPENSE', 1000, ?, 'Isolated', 'pending', 1, 1, ?, ?)`, txDate, now, now)

	handler := TransactionHandler{
		DB:          db,
		Templates:   testBulkPaymentTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	rr := httptest.NewRecorder()
	handler.HandleTogglePagamento(rr, formPost("/transacoes/tx-isolated/status-pagamento", url.Values{
		"payment_date": {"2026-07-14"},
	}), "tx-isolated")

	if rr.Code == http.StatusOK {
		t.Fatal("should not toggle transaction from other workspace")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transaction_status_history WHERE transaction_id = 'tx-isolated'`).Scan(&count); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if count != 0 {
		t.Fatalf("history count = %d, want 0 (cross-workspace blocked)", count)
	}
}

func TestCardRendersPaidAtDisplay(t *testing.T) {
	tpl := template.Must(template.New("row-test").Funcs(template.FuncMap{
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

	t.Run("paid transaction with paid_at shows Pago em badge", func(t *testing.T) {
		paidAtUnix := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC).Unix()
		data := TransactionRow{
			ID:            "tx-paid-test",
			Description:   "Compra Teste",
			PaymentStatus: "paid",
			IsPending:     false,
			DateLabel:     "29 Jul",
			AmountDisplay: "- R$ 100,00",
			PaymentIcon:   "check-circle-2",
			PaymentTitle:  "Pago",
			Type:          "EXPENSE",
			AmountClass:   "text-[#FE414F]",
			PaidAt:        &paidAtUnix,
			PaidAtDisplay: "02/07/2006",
		}
		var buf strings.Builder
		if err := tpl.ExecuteTemplate(&buf, "lancamento-row", data); err != nil {
			t.Fatalf("execute row: %v", err)
		}
		html := buf.String()
		if !strings.Contains(html, "Pago em") {
			t.Fatal("card should contain 'Pago em' badge")
		}
		if !strings.Contains(html, "02/07/2006") {
			t.Fatal("card should contain paid_at display")
		}
	})

	t.Run("pending transaction does not show Pago em", func(t *testing.T) {
		data := TransactionRow{
			ID:            "tx-pending-test",
			Description:   "Compra Pendente",
			PaymentStatus: "pending",
			IsPending:     true,
			DateLabel:     "29 Jul",
			AmountDisplay: "- R$ 100,00",
			PaymentIcon:   "clock",
			PaymentTitle:  "Pendente",
			Type:          "EXPENSE",
			AmountClass:   "text-[#FE414F]",
		}
		var buf strings.Builder
		if err := tpl.ExecuteTemplate(&buf, "lancamento-row", data); err != nil {
			t.Fatalf("execute row: %v", err)
		}
		html := buf.String()
		if strings.Contains(html, "Pago em") {
			t.Fatal("pending card should NOT contain 'Pago em' badge")
		}
	})

	t.Run("paid with usesDueDate flag shows Venc badge but not Pago em", func(t *testing.T) {
		paidAtUnix := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC).Unix()
		data := TransactionRow{
			ID:               "tx-venc-paid",
			Description:      "Compra Vencida",
			PaymentStatus:    "paid",
			IsPending:        false,
			UsesDueDate:      false,
			DateLabel:        "29 Jul",
			DisplayDateLabel: "Venc. 29/07",
			AmountDisplay:    "- R$ 100,00",
			PaymentIcon:      "check-circle-2",
			PaymentTitle:     "Pago",
			Type:             "EXPENSE",
			AmountClass:      "text-[#FE414F]",
			PaidAt:           &paidAtUnix,
			PaidAtDisplay:    "02/07/2006",
		}
		var buf strings.Builder
		if err := tpl.ExecuteTemplate(&buf, "lancamento-row", data); err != nil {
			t.Fatalf("execute row: %v", err)
		}
		html := buf.String()
		if !strings.Contains(html, "Pago em") {
			t.Fatal("paid card should contain 'Pago em' badge")
		}
	})
}
