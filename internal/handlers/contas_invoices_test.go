package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestContasPayablesIncludesCardInvoices(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedContasInvoiceScenario(t, db)

	t.Run("faturas OPEN e CLOSED no periodo aparecem com saldo pendente", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas", "EXPENSE", false, 8, 2026, "")
		if err != nil {
			t.Fatalf("query payables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		mustContain := []string{
			"Despesa comum pendente agosto",
			"Fatura Cartao Contas A - 2026-08",
			"Fatura Cartao Contas B - 2026-08",
		}
		for _, w := range mustContain {
			if !contasContains(descs, w) {
				t.Errorf("payables missing %q; got %v", w, descs)
			}
		}

		mustNotContain := []string{
			"Despesa comum paga",
			"Compra fatura aberta",
			"Compra fatura fechada",
			"Compra fatura paga",
			"Compra fatura outro workspace",
			"Pagamento de Fatura - Cartao Contas",
			"Fatura Cartao Contas A - 2026-10",
			"Fatura Cartao Outro WS - 2026-08",
		}
		for _, nw := range mustNotContain {
			if contasContains(descs, nw) {
				t.Errorf("payables unexpectedly contains %q; got %v", nw, descs)
			}
		}

		if row, ok := contasFindByDesc(rows, "Fatura Cartao Contas A - 2026-08"); !ok {
			t.Errorf("expected to find invoice 2026-08 row")
		} else if row.Amount != 20000 {
			t.Errorf("invoice 2026-08 amount = %d, want 20000 (10000+15000-5000)", row.Amount)
		} else if !row.IsInvoice {
			t.Errorf("invoice row should be flagged IsInvoice=true")
		}

		if row, ok := contasFindByDesc(rows, "Fatura Cartao Contas B - 2026-08"); !ok {
			t.Errorf("expected to find invoice closed 2026-08 row")
		} else if row.Amount != 40000 {
			t.Errorf("invoice closed 2026-08 amount = %d, want 40000", row.Amount)
		} else if row.IsOverdue {
			t.Errorf("invoice closed 2026-08 (vence 2026-08-20) should not be overdue hoje=2026-08-10")
		}
	})

	t.Run("fatura PAID nao aparece mesmo sem filtro de mes", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas", "EXPENSE", false, 0, 0, "")
		if err != nil {
			t.Fatalf("query payables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		if contasContains(descs, "Fatura Cartao Contas A - 2026-10") {
			t.Errorf("PAID invoice should not appear; got %v", descs)
		}
		if !contasContains(descs, "Fatura Cartao Contas A - 2026-08") {
			t.Errorf("OPEN invoice without month filter should appear; got %v", descs)
		}
	})

	t.Run("fatura com saldo pendente zero nao aparece", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas-zero", "EXPENSE", false, 8, 2026, "")
		if err != nil {
			t.Fatalf("query payables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		if contasContains(descs, "Fatura Cartao Zero - 2026-08") {
			t.Errorf("invoice with zero pending balance should not appear; got %v", descs)
		}
	})

	t.Run("fatura fora do mes selecionado nao aparece", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas", "EXPENSE", false, 9, 2026, "")
		if err != nil {
			t.Fatalf("query payables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		if contasContains(descs, "Fatura Cartao Contas A - 2026-08") {
			t.Errorf("invoice 2026-08 should not appear when filtro mes=9; got %v", descs)
		}
		if contasContains(descs, "Fatura Cartao Contas B - 2026-08") {
			t.Errorf("invoice 2026-08 (closed) should not appear when filtro mes=9; got %v", descs)
		}
	})

	t.Run("aba receber nao inclui faturas", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas", "INCOME", false, 8, 2026, "")
		if err != nil {
			t.Fatalf("query receivables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		if contasContains(descs, "Fatura") {
			t.Errorf("receivables should not contain invoices; got %v", descs)
		}
		if !contasContains(descs, "Receita pendente agosto") {
			t.Errorf("receivables missing normal income; got %v", descs)
		}
	})

	t.Run("busca por nome do cartao retorna fatura", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas", "EXPENSE", false, 0, 0, "Cartao Contas A")
		if err != nil {
			t.Fatalf("query payables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		if !contasContains(descs, "Fatura Cartao Contas A - 2026-08") {
			t.Errorf("search by card name should match invoice; got %v", descs)
		}
	})

	t.Run("outro workspace nao vaza fatura", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-contas", "EXPENSE", false, 0, 0, "")
		if err != nil {
			t.Fatalf("query payables error: %v", err)
		}
		descs := contasPayableDescriptions(rows)
		if contasContains(descs, "Fatura Cartao Outro WS - 2026-08") {
			t.Errorf("cross-workspace invoice leaked; got %v", descs)
		}
	})
}

func seedContasInvoiceScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-contas', 'User Contas', 'contas@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-other-contas', 'User Other Contas', 'other-contas@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-contas', 'Contas WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-other-contas', 'Other Contas WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-contas-zero', 'Contas Zero WS', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-contas', 'user-contas', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-other-contas', 'user-other-contas', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-contas-zero', 'user-contas', 'ADMIN', ?)`, now)

	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-contas', 'ws-contas', 'Conta Contas', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-contas-a', 'ws-contas', 'Cartao Contas A', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-contas-b', 'ws-contas', 'Cartao Contas B', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-other-contas', 'ws-other-contas', 'Conta Other', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-other-contas', 'ws-other-contas', 'Cartao Outro WS', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-zero', 'ws-contas-zero', 'Cartao Zero', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)

	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-contas-a', 'card-contas-a', 20, 15, 500000)`)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-contas-b', 'card-contas-b', 20, 20, 500000)`)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-other-contas', 'card-other-contas', 20, 12, 500000)`)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-zero', 'card-zero', 20, 15, 500000)`)

	insertDashboardPendingTx(t, db, "pay-common-pending", "ws-contas", "user-contas", "acc-contas", "", "EXPENSE", 12000, "2026-08-01", "2026-08-12", "Despesa comum pendente agosto", "pending")
	insertDashboardPendingTx(t, db, "pay-common-paid", "ws-contas", "user-contas", "acc-contas", "", "EXPENSE", 44000, "2026-08-02", "2026-08-05", "Despesa comum paga", "paid")
	insertDashboardPendingTx(t, db, "rec-common-pending", "ws-contas", "user-contas", "acc-contas", "", "INCOME", 33000, "2026-08-03", "2026-08-20", "Receita pendente agosto", "pending")

	insertDashboardInvoice(t, db, "invoice-open", "card-contas-a", "2026-08", "2026-07-20", "2026-08-15", "OPEN", 0)
	insertDashboardInvoice(t, db, "invoice-closed", "card-contas-b", "2026-08", "2026-07-20", "2026-08-20", "CLOSED", 0)
	insertDashboardInvoice(t, db, "invoice-paid", "card-contas-a", "2026-10", "2026-08-20", "2026-09-15", "PAID", 99999)
	insertDashboardInvoice(t, db, "invoice-other-ws", "card-other-contas", "2026-08", "2026-07-20", "2026-08-12", "OPEN", 0)
	insertDashboardInvoice(t, db, "invoice-zero", "card-zero", "2026-08", "2026-07-20", "2026-08-15", "OPEN", 0)

	insertDashboardCardPurchase(t, db, "purchase-open-1", "ws-contas", "user-contas", "card-contas-a", "invoice-open", 10000, "2026-07-10", "Compra fatura aberta")
	insertDashboardCardPurchase(t, db, "purchase-open-2", "ws-contas", "user-contas", "card-contas-a", "invoice-open", 15000, "2026-07-12", "Compra fatura aberta 2")
	insertDashboardCardPurchase(t, db, "purchase-closed", "ws-contas", "user-contas", "card-contas-b", "invoice-closed", 40000, "2026-07-15", "Compra fatura fechada")
	insertDashboardCardPurchase(t, db, "purchase-paid", "ws-contas", "user-contas", "card-contas-a", "invoice-paid", 60000, "2026-07-25", "Compra fatura paga")
	insertDashboardCardPurchase(t, db, "purchase-other-ws", "ws-other-contas", "user-other-contas", "card-other-contas", "invoice-other-ws", 80000, "2026-07-10", "Compra fatura outro workspace")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-invoice-partial-tx', 'ws-contas', 'user-contas', 'acc-contas', 'EXPENSE', 5000, ?, 'Pagamento de Fatura - Cartao Contas A', 'paid', 1, 1, ?, ?)
	`, testUnixDate("2026-08-05"), now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES ('pay-invoice-partial', 'ws-contas', 'invoice-open', 'acc-contas', 'pay-invoice-partial-tx', 5000, ?, 'manual', ?)
	`, testUnixDate("2026-08-05"), now)

	insertDashboardCardPurchase(t, db, "purchase-zero", "ws-contas-zero", "user-contas", "card-zero", "invoice-zero", 30000, "2026-07-10", "Compra fatura zero")
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES ('pay-invoice-zero-full', 'ws-contas-zero', 'invoice-zero', 'card-zero', 30000, ?, 'manual', ?)
	`, testUnixDate("2026-08-01"), now)
}

func contasPayableDescriptions(rows []PendingItemRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Description)
	}
	return out
}

func contasContains(items []string, want string) bool {
	for _, v := range items {
		if v == want {
			return true
		}
	}
	return false
}

func contasFindByDesc(rows []PendingItemRow, desc string) (PendingItemRow, bool) {
	for _, r := range rows {
		if r.Description == desc {
			return r, true
		}
	}
	return PendingItemRow{}, false
}