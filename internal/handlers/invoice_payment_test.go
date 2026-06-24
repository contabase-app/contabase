package handlers

import (
	"testing"
	"time"
)

func TestCalculateInvoicePendingAmount(t *testing.T) {
	tests := []struct {
		name        string
		totalCents  int64
		paidCents   int64
		wantPending int64
	}{
		{"pending parcial", 10000, 3000, 7000},
		{"pending quitado", 10000, 10000, 0},
		{"pending com pagamento maior", 10000, 12000, 0},
		{"zero total", 0, 0, 0},
		{"negativo por seguranca", -500, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateInvoicePendingAmount(tt.totalCents, tt.paidCents)
			if got != tt.wantPending {
				t.Errorf("CalculateInvoicePendingAmount(%d, %d) = %d, want %d", tt.totalCents, tt.paidCents, got, tt.wantPending)
			}
		})
	}
}

func TestExcludeInvoicePaymentCompetenceClauseUsesStructuralLink(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec("INSERT INTO workspaces (id, name, type) VALUES ('ws-struct', 'WS Struct', 'personal')"); err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-struct', 'User Struct', 'struct@example.com', 'hash', ?, ?)", now, now); err != nil {
		t.Fatalf("seed user failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-struct', 'ws-struct', 'Account Struct', 'CHECKING')"); err != nil {
		t.Fatalf("seed account failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO invoices (id, account_id, reference, closing_date, due_date) VALUES ('inv-struct', 'acc-struct', '2026-06', 0, 0)"); err != nil {
		t.Fatalf("seed invoice failed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES
			('tx-standard', 'ws-struct', 'user-struct', 'acc-struct', 'EXPENSE', 1000, ?, ?, 'paid', ?, ?),
			('tx-renamed', 'ws-struct', 'user-struct', 'acc-struct', 'EXPENSE', 2000, ?, 'Descricao editada', 'paid', ?, ?),
			('tx-normal', 'ws-struct', 'user-struct', 'acc-struct', 'EXPENSE', 3000, ?, ?, 'paid', ?, ?)
	`, now, invoicePaymentDescription("Cartao"), now, now, now, now, now, now, invoicePaymentDescription("Cartao"), now, now); err != nil {
		t.Fatalf("seed transactions failed: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES
			('ip-standard', 'ws-struct', 'inv-struct', 'acc-struct', 'tx-standard', 1000, ?, 'manual', ?),
			('ip-renamed', 'ws-struct', 'inv-struct', 'acc-struct', 'tx-renamed', 2000, ?, 'manual', ?)
	`, now, now, now, now); err != nil {
		t.Fatalf("seed invoice payments failed: %v", err)
	}

	var total int64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM transactions
		WHERE workspace_id = 'ws-struct'
		  AND ` + excludeInvoicePaymentCompetenceClause("") + `
	`).Scan(&total)
	if err != nil {
		t.Fatalf("query structural competence filter: %v", err)
	}
	if total != 3000 {
		t.Fatalf("structural filtered total = %d, want 3000", total)
	}
}

func TestQueryAndSumActiveInvoicePayments(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec("INSERT INTO workspaces (id, name, type) VALUES ('ws-1', 'WS 1', 'personal')"); err != nil {
		t.Fatalf("seed workspace failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-1', 'ws-1', 'Account 1', 'CHECKING')"); err != nil {
		t.Fatalf("seed account failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO invoices (id, account_id, reference, closing_date, due_date) VALUES ('inv-1', 'acc-1', '2026-06', 0, 0)"); err != nil {
		t.Fatalf("seed invoice 1 failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO invoices (id, account_id, reference, closing_date, due_date) VALUES ('inv-2', 'acc-1', '2026-07', 0, 0)"); err != nil {
		t.Fatalf("seed invoice 2 failed: %v", err)
	}

	// Fatura sem pagamentos
	payments, err := QueryActiveInvoicePayments(db, "ws-1", "inv-1")
	if err != nil {
		t.Fatalf("QueryActiveInvoicePayments failed: %v", err)
	}
	if len(payments) != 0 {
		t.Errorf("expected 0 payments, got %d", len(payments))
	}
	if payments == nil {
		t.Errorf("expected empty slice, got nil")
	}

	sum, err := SumActiveInvoicePayments(db, "ws-1", "inv-1")
	if err != nil {
		t.Fatalf("SumActiveInvoicePayments failed: %v", err)
	}
	if sum != 0 {
		t.Errorf("expected sum 0, got %d", sum)
	}

	now := time.Now().Unix()
	rev := now + 100
	noteStr := "parcial"

	if _, err := db.Exec(`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, note, source, created_at)
		VALUES ('p-1', 'ws-1', 'inv-1', 'acc-1', 2000, ?, ?, 'manual', ?)`, now, noteStr, now); err != nil {
		t.Fatalf("insert p-1 failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES ('p-2', 'ws-1', 'inv-1', 'acc-1', 3000, ?, 'manual', ?)`, now+1, now+1); err != nil {
		t.Fatalf("insert p-2 failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, reversed_at, created_at)
		VALUES ('p-3', 'ws-1', 'inv-1', 'acc-1', 5000, ?, 'manual', ?, ?)`, now+2, rev, now+2); err != nil {
		t.Fatalf("insert p-3 failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES ('p-4', 'ws-1', 'inv-2', 'acc-1', 8000, ?, 'manual', ?)`, now+3, now+3); err != nil {
		t.Fatalf("insert p-4 failed: %v", err)
	}

	payments, err = QueryActiveInvoicePayments(db, "ws-1", "inv-1")
	if err != nil {
		t.Fatalf("QueryActiveInvoicePayments failed: %v", err)
	}
	if len(payments) != 2 {
		t.Fatalf("expected 2 payments, got %d", len(payments))
	}
	if payments[0].ID != "p-2" || payments[1].ID != "p-1" {
		t.Errorf("expected ordering p-2 then p-1, got %s then %s", payments[0].ID, payments[1].ID)
	}
	if payments[1].Note == nil || *payments[1].Note != "parcial" {
		t.Errorf("expected note 'parcial', got %v", payments[1].Note)
	}
	if payments[0].Note != nil {
		t.Errorf("expected nil note, got %v", payments[0].Note)
	}

	sum, err = SumActiveInvoicePayments(db, "ws-1", "inv-1")
	if err != nil {
		t.Fatalf("SumActiveInvoicePayments failed: %v", err)
	}
	if sum != 5000 {
		t.Errorf("expected sum 5000, got %d", sum)
	}
}
