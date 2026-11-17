package handlers

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

type f1Base struct {
	ws       string
	user     string
	checking string
	card     string
	category string
	now      int64
}

func seedF1Base(t *testing.T, db *sql.DB, ws string) f1Base {
	t.Helper()
	now := time.Now().UTC().Unix()
	base := f1Base{
		ws:       ws,
		user:     "user-" + ws,
		checking: "check-" + ws,
		card:     "card-" + ws,
		category: "cat-" + ws,
		now:      now,
	}
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'hash', ?, ?)
	`, base.user, base.user, base.user+"@example.com", now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES (?, ?, '', ?, ?)
	`, ws, ws, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', ?)
	`, ws, base.user, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			(?, ?, 'Conta', 'CHECKING', 10000, 10000, ?, ?),
			(?, ?, 'Cartão', 'CREDIT_CARD', 0, 0, ?, ?)
	`, base.checking, ws, now, now, base.card, ws, now, now)
	addF1Category(t, db, base.category, ws, "")
	return base
}

func addF1Category(t *testing.T, db *sql.DB, id, ws, parentID string) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, parent_id, created_at)
		VALUES (?, ?, ?, 'EXPENSE', ?, ?)
	`, id, ws, id, f1NullableString(parentID), time.Now().UTC().Unix())
}

func addF1Account(t *testing.T, db *sql.DB, id, ws, accountType string, balance, now int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, ws, id, accountType, balance, balance, now, now)
}

func addF1Invoice(t *testing.T, db *sql.DB, id, accountID, reference, status string, paidAmount, now int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, id, accountID, reference, now, now+86400, status, paidAmount, now)
}

type f1TxSeed struct {
	ID                       string
	WorkspaceID              string
	UserID                   string
	AccountID                string
	DestinationAccountID     string
	CategoryID               string
	InvoiceID                string
	InvoiceOverrideID        string
	Type                     string
	Amount                   int64
	Status                   string
	Description              string
	EffectMode               string
	DueDate                  int64
	ParentID                 string
	RecurringRuleID          string
	SeriesID                 string
	RecurrenceSequence       int64
	IsRecurringException     bool
	RecurringExceptionReason string
	InstallmentNumber        int64
	TotalInstallments        int64
	CreatedAt                int64
}

func addF1Tx(t *testing.T, db *sql.DB, seed f1TxSeed) {
	t.Helper()
	typ := seed.Type
	if typ == "" {
		typ = "EXPENSE"
	}
	status := seed.Status
	if status == "" {
		status = "pending"
	}
	effect := seed.EffectMode
	if effect == "" {
		effect = "STANDARD"
	}
	installment := seed.InstallmentNumber
	if installment == 0 {
		installment = 1
	}
	total := seed.TotalInstallments
	if total == 0 {
		total = 1
	}
	exception := 0
	if seed.IsRecurringException {
		exception = 1
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, destination_account_id, category_id,
			invoice_id, invoice_override_id, type, amount, date, description,
			due_date,
			installment_number, total_installments, parent_id,
			recurring_rule_id, recurrence_sequence, series_id,
			is_recurring_exception, recurring_exception_reason,
			status, effect_mode, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, seed.ID, seed.WorkspaceID, seed.UserID, seed.AccountID,
		f1NullableString(seed.DestinationAccountID), f1NullableString(seed.CategoryID),
		f1NullableString(seed.InvoiceID), f1NullableString(seed.InvoiceOverrideID),
		typ, seed.Amount, seed.CreatedAt, seed.Description,
		f1NullableInt(seed.DueDate),
		installment, total, f1NullableString(seed.ParentID),
		f1NullableString(seed.RecurringRuleID), f1NullableInt(seed.RecurrenceSequence), f1NullableString(seed.SeriesID),
		exception, f1NullableString(seed.RecurringExceptionReason),
		status, effect, seed.CreatedAt, seed.CreatedAt)
}

func addF1InvoicePayment(t *testing.T, db *sql.DB, id, ws, invoiceID, accountID, transactionID, reversalID string, amount, reversedAt int64, userID string, now int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, transaction_id, reversal_transaction_id,
			amount_cents, paid_at, source, reversed_at, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'manual', ?, ?, ?)
	`, id, ws, invoiceID, accountID, f1NullableString(transactionID), f1NullableString(reversalID),
		amount, now, f1NullableInt(reversedAt), userID, now)
}

func addF1TransactionRefund(t *testing.T, db *sql.DB, id, ws, transactionID string, amount, reversedAt int64, userID string, now int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO transaction_refunds (
			id, workspace_id, transaction_id, amount_cents, description,
			created_by, created_at, reversed_at, reversed_by
		) VALUES (?, ?, ?, ?, 'reembolso teste', ?, ?, ?, ?)
	`, id, ws, transactionID, amount, userID, now, f1NullableInt(reversedAt), f1NullableString(map[bool]string{true: userID, false: ""}[reversedAt > 0]))
}

func addF1InvoiceRefund(t *testing.T, db *sql.DB, id, ws, invoiceID string, amount, reversedAt int64, userID string, now int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO invoice_refunds (
			id, workspace_id, invoice_id, amount_cents, description,
			created_by, created_at, reversed_at, reversed_by
		) VALUES (?, ?, ?, ?, 'reembolso geral teste', ?, ?, ?, ?)
	`, id, ws, invoiceID, amount, userID, now, f1NullableInt(reversedAt), f1NullableString(map[bool]string{true: userID, false: ""}[reversedAt > 0]))
}

func f1NullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func f1NullableInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func mustF1(t *testing.T, db *sql.DB, ws string) services.WorkspaceFinancialPositionF1 {
	t.Helper()
	got, err := services.CalculateWorkspaceFinancialPositionF1(db, ws, loadWorkspaceObligationsF1)
	if err != nil {
		t.Fatalf("calculate F1 financial position: %v", err)
	}
	return got
}

func requireF1Reliable(t *testing.T, got services.WorkspaceFinancialPositionF1) {
	t.Helper()
	if !got.IsReliable() {
		t.Fatalf("F1 unavailable: %s", got.ReasonText())
	}
}

func requireF1Unavailable(t *testing.T, got services.WorkspaceFinancialPositionF1, code string) {
	t.Helper()
	if got.IsReliable() {
		t.Fatalf("F1 unexpectedly reliable: %+v", got)
	}
	if code == "" {
		return
	}
	for _, reason := range got.Reasons {
		if reason.Code == code {
			return
		}
	}
	t.Fatalf("F1 missing reason %q: %+v", code, got.Reasons)
}
