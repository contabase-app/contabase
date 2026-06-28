package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func seedCreditCardWithLimit(t *testing.T, db *sql.DB, wsID, cardID, ccID string, creditLimit int64) {
	t.Helper()
	now := time.Now().Unix()
	userID := "user-" + wsID
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'hash', ?, ?)
	`, userID, userID, userID+"@example.com", now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES (?, ?, '', ?, ?)
	`, wsID, wsID, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', ?)
	`, wsID, userID, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, ?, 'CREDIT_CARD', 0, 0, ?, ?)
	`, cardID, wsID, cardID, now, now)
	if creditLimit > 0 {
		execTestSQL(t, db, `
			INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
			VALUES (?, ?, 20, 10, ?)
		`, ccID, cardID, creditLimit)
	}
}

func seedInvoiceForCard(t *testing.T, db *sql.DB, wsID, cardID, invoiceID, reference, status string, closingYear, closingMonth, closingDay int) {
	t.Helper()
	now := time.Now().Unix()
	closeDate := time.Date(closingYear, time.Month(closingMonth), closingDay, 12, 0, 0, 0, time.UTC)
	dueDate := closeDate.AddDate(0, 0, 20)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, invoiceID, cardID, reference, closeDate.Unix(), dueDate.Unix(), status, now)
}

func seedExpenseOnInvoice(t *testing.T, db *sql.DB, wsID, cardID, invoiceID string, amount, installmentNumber, totalInstallments int64, parentID string) string {
	t.Helper()
	now := time.Now().Unix()
	userID := "user-" + wsID
	var pid interface{}
	if installmentNumber == 1 {
		pid = nil
	} else {
		pid = parentID
	}
	txID := "tx-" + invoiceID + "-" + string(rune('a'+int(installmentNumber)))
	if installmentNumber == 1 {
		txID = parentID
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', ?, ?, 'Compra Teste', 'paid', ?, ?, ?, ?, ?)
	`, txID, wsID, userID, cardID, invoiceID, amount, now, installmentNumber, totalInstallments, pid, now, now)
	return txID
}

func seedInvoicePaymentFull(t *testing.T, db *sql.DB, wsID, invoiceID, accountID string, amount int64) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'manual', ?)
	`, "pay-"+invoiceID, wsID, invoiceID, accountID, amount, now, now)
}

func TestCreditCardAvailableLimitSinglePurchaseOnCurrentInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID      = "ws-single"
		cardID    = "card-single"
		ccID      = "cc-single"
		invID     = "inv-single-2026-07"
	)

	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	seedInvoiceForCard(t, db, wsID, cardID, invID, "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, wsID, cardID, invID, 100000, 1, 1, "tx-single-parent")

	data, err := buildFaturaDataForInvoice(db, wsID, invID, "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "1.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 1.000,00", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "9.000" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 9.000,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitTenInstallmentsAcrossOpenInvoices(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-10x"
		cardID = "card-10x"
		ccID   = "cc-10x"
	)

	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	parentID := "tx-10x-parent"
	for i := int64(1); i <= 10; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-10x-" + ref
		seedInvoiceForCard(t, db, wsID, cardID, invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, invID, 100000, i, 10, parentID)
	}

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-10x-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "10.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 10.000,00", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "0" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 0,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitAfterFirstInvoicePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-paid1"
		cardID = "card-paid1"
		ccID   = "cc-paid1"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	parentID := "tx-paid1-parent"
	for i := int64(1); i <= 10; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-paid1-" + ref
		status := "OPEN"
		seedInvoiceForCard(t, db, wsID, cardID, invID, ref, status, 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, invID, 100000, i, 10, parentID)
		if i == 1 {
			execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ? WHERE id = ?`, time.Now().Unix(), invID)
			seedInvoicePaymentFull(t, db, wsID, invID, cardID, 100000)
		}
	}

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-paid1-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "9.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 9.000,00 (PAID invoice excluded)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "1.000" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 1.000,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitAfterFiveInvoicesPaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-paid5"
		cardID = "card-paid5"
		ccID   = "cc-paid5"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	parentID := "tx-paid5-parent"
	for i := int64(1); i <= 10; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-paid5-" + ref
		status := "OPEN"
		seedInvoiceForCard(t, db, wsID, cardID, invID, ref, status, 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, invID, 100000, i, 10, parentID)
		if i <= 5 {
			execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ? WHERE id = ?`, time.Now().Unix(), invID)
			seedInvoicePaymentFull(t, db, wsID, invID, cardID, 100000)
		}
	}

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-paid5-2026-08", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "5.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 5.000,00 (5 PAID invoices excluded)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "5.000" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 5.000,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitTwoInstallmentPurchases(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-2purch"
		cardID = "card-2purch"
		ccID   = "cc-2purch"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)

	parentA := "tx-2purch-parent-a"
	for i := int64(1); i <= 5; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		seedInvoiceForCard(t, db, wsID, cardID, "inv-2a-"+ref, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, "inv-2a-"+ref, 200000, i, 5, parentA)
	}
	parentB := "tx-2purch-parent-b"
	for i := int64(1); i <= 3; i++ {
		m := time.Month(12 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		seedInvoiceForCard(t, db, wsID, cardID, "inv-2b-"+ref, ref, "OPEN", 2026, 12+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, "inv-2b-"+ref, 100000, i, 3, parentB)
	}

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-2a-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	// 5x200 = 1000000 + 3x100 = 300000 = 1300000 total
	if data.LimitUsed.Reais != "13.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 13.000,00 (5x R$2.000 + 3x R$1.000)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "0" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 0,00 (clamped)", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
	if data.LimitPercent != 0 {
		t.Fatalf("LimitPercent = %d, want 0", data.LimitPercent)
	}
}

func TestCreditCardAvailableLimitRecurringProjectionsOnCard(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-recur"
		cardID = "card-recur"
		ccID   = "cc-recur"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	now := time.Now().Unix()
	userID := "user-" + wsID
	ruleID := "rule-limit-recur"
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'EXPENSE', 50000, 'Recorrente Limit', ?, 'MONTHLY', 'PAID', 1, ?, ?)
	`, ruleID, wsID, userID, cardID, testUnixDate("2026-07-24"), now, now)

	for i := int64(1); i <= 3; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-recur-" + ref
		seedInvoiceForCard(t, db, wsID, cardID, invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'EXPENSE', 50000, ?, 'Recorrente Limit', 'paid', 1, 1, ?, ?, ?, ?)
		`, "tx-recur-"+ref, wsID, userID, cardID, invID, testUnixDate(fmtDate(2026, int(7+(i-1)), 24)), ruleID, i, now, now)
	}

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-recur-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "1.500" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 1.500,00 (3x R$ 500,00)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "8.500" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 8.500,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitClosedInvoiceConsumesLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-closed"
		cardID = "card-closed"
		ccID   = "cc-closed"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	seedInvoiceForCard(t, db, wsID, cardID, "inv-closed-2026-07", "2026-07", "CLOSED", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, wsID, cardID, "inv-closed-2026-07", 200000, 1, 1, "tx-closed-parent")

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-closed-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "2.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 2.000,00 (CLOSED still consumes limit)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
}

func TestCreditCardAvailableLimitPaidInvoiceDoesNotConsumeLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-paidonly"
		cardID = "card-paidonly"
		ccID   = "cc-paidonly"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	seedInvoiceForCard(t, db, wsID, cardID, "inv-paidonly-2026-07", "2026-07", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, wsID, cardID, "inv-paidonly-2026-07", 300000, 1, 1, "tx-paidonly-parent")
	seedInvoicePaymentFull(t, db, wsID, "inv-paidonly-2026-07", cardID, 300000)

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-paidonly-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "0" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 0,00 (PAID invoice with full payment, pending=0)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "10.000" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 10.000,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitInvoicePaymentReducesLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-payinv"
		cardID = "card-payinv"
		ccID   = "cc-payinv"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	seedInvoiceForCard(t, db, wsID, cardID, "inv-payinv-2026-07", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, wsID, cardID, "inv-payinv-2026-07", 500000, 1, 1, "tx-payinv-parent")
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, created_at)
		VALUES ('ipay-test', ?, 'inv-payinv-2026-07', ?, 300000, ?, 'manual', ?)
	`, wsID, cardID, now, now)

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-payinv-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "2.000" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 2.000,00 (partial payment reduces pending from 5.000 to 2.000)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
	if data.LimitAvailable.Reais != "8.000" || data.LimitAvailable.Cents != ",00" {
		t.Fatalf("LimitAvailable = R$ %s%s, want R$ 8.000,00", data.LimitAvailable.Reais, data.LimitAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitDashboardAndInvoiceShowSameValue(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-consist"
		cardID = "card-consist"
		ccID   = "cc-consist"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	parentID := "tx-consist-parent"
	for i := int64(1); i <= 10; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-consist-" + ref
		seedInvoiceForCard(t, db, wsID, cardID, invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, invID, 100000, i, 10, parentID)
	}

	invData, err := buildFaturaDataForInvoice(db, wsID, "inv-consist-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}
	dashData := BuildDashboardData(db, "user-"+wsID, wsID)

	if len(dashData.Cards) != 1 {
		t.Fatalf("dashboard cards = %d, want 1", len(dashData.Cards))
	}
	dashCard := dashData.Cards[0]

	invAvailable := invData.LimitAvailable
	dashAvailable := dashCard.LimitMoney

	if invAvailable.Reais != dashAvailable.Reais || invAvailable.Cents != dashAvailable.Cents {
		t.Fatalf("invoice LimitAvailable = R$ %s%s, dashboard LimitMoney = R$ %s%s (must be equal)",
			invAvailable.Reais, invAvailable.Cents, dashAvailable.Reais, dashAvailable.Cents)
	}
	if dashAvailable.Reais != "0" || dashAvailable.Cents != ",00" {
		t.Fatalf("dashboard LimitAvailable = R$ %s%s, want R$ 0,00", dashAvailable.Reais, dashAvailable.Cents)
	}
}

func TestCreditCardAvailableLimitExpenseWithoutInvoiceDoesNotConsumeLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-noinv"
		cardID = "card-noinv"
		ccID   = "cc-noinv"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	seedInvoiceForCard(t, db, wsID, cardID, "inv-noinv-2026-07", "2026-07", "OPEN", 2026, 7, 20)
	now := time.Now().Unix()
	userID := "user-" + wsID
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-noinv', ?, ?, ?, 'EXPENSE', 999999, ?, 'Compra sem fatura', 'paid', 1, 1, ?, ?)
	`, wsID, userID, cardID, now, now, now)

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-noinv-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.LimitUsed.Reais != "0" || data.LimitUsed.Cents != ",00" {
		t.Fatalf("LimitUsed = R$ %s%s, want R$ 0,00 (expense without invoice_id excluded)", data.LimitUsed.Reais, data.LimitUsed.Cents)
	}
}

func TestCreditCardAvailableLimitCardWithoutCreditLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-nolimit"
		cardID = "card-nolimit"
		ccID   = "cc-nolimit"
	)
	now := time.Now().Unix()
	userID := "user-" + wsID
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, userID, userID+"@example.com", now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, wsID, wsID, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, ?, 'CREDIT_CARD', 0, 0, ?, ?)`, cardID, wsID, cardID, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES (?, ?, 20, 10, 0)`, ccID, cardID)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('inv-nolimit-2026-07', ?, '2026-07', ?, ?, 'OPEN', ?)`, cardID, now, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at) VALUES ('tx-nolimit', ?, ?, ?, 'inv-nolimit-2026-07', 'EXPENSE', 500000, ?, 'Compra Teste', 'paid', 1, 1, ?, ?)`, wsID, userID, cardID, now, now, now)

	data, err := buildFaturaDataForInvoice(db, wsID, "inv-nolimit-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}

	if data.HasCreditLimit {
		t.Fatalf("HasCreditLimit should be false when credit_limit is 0")
	}
}

func TestCreditCardAvailableLimitWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 1000000)
	seedInvoiceForCard(t, db, "ws-a", "card-a", "inv-a", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-a", "card-a", "inv-a", 300000, 1, 1, "tx-a")

	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 1000000)
	seedInvoiceForCard(t, db, "ws-b", "card-b", "inv-b", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-b", "card-b", "inv-b", 700000, 1, 1, "tx-b")

	dataA, err := buildFaturaDataForInvoice(db, "ws-a", "inv-a", "desc")
	if err != nil {
		t.Fatalf("ws-a fatura: %v", err)
	}
	if dataA.LimitUsed.Reais != "3.000" || dataA.LimitUsed.Cents != ",00" {
		t.Fatalf("ws-a LimitUsed = R$ %s%s, want R$ 3.000,00 (should not include ws-b)", dataA.LimitUsed.Reais, dataA.LimitUsed.Cents)
	}

	dataB, err := buildFaturaDataForInvoice(db, "ws-b", "inv-b", "desc")
	if err != nil {
		t.Fatalf("ws-b fatura: %v", err)
	}
	if dataB.LimitUsed.Reais != "7.000" || dataB.LimitUsed.Cents != ",00" {
		t.Fatalf("ws-b LimitUsed = R$ %s%s, want R$ 7.000,00 (should not include ws-a)", dataB.LimitUsed.Reais, dataB.LimitUsed.Cents)
	}
}

func TestCreditCardAvailableLimitCardIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const wsID = "ws-cardiso"
	now := time.Now().Unix()

	userID := "user-" + wsID
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, 'hash', ?, ?)`, userID, userID, userID+"@example.com", now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`, wsID, wsID, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now)

	card1, card2 := "card-x", "card-y"
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, ?, 'CREDIT_CARD', 0, 0, ?, ?)`, card1, wsID, card1, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, ?, 'CREDIT_CARD', 0, 0, ?, ?)`, card2, wsID, card2, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-x', ?, 20, 10, 1000000)`, card1)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-y', ?, 20, 10, 1000000)`, card2)

	seedInvoiceForCard(t, db, wsID, card1, "inv-x-07", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, wsID, card1, "inv-x-07", 200000, 1, 1, "tx-card-parent")

	seedInvoiceForCard(t, db, wsID, card2, "inv-y-07", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, wsID, card2, "inv-y-07", 500000, 1, 1, "tx-cardY-parent")

	dataX, err := buildFaturaDataForInvoice(db, wsID, "inv-x-07", "desc")
	if err != nil {
		t.Fatalf("card-x fatura: %v", err)
	}
	if dataX.LimitUsed.Reais != "2.000" || dataX.LimitUsed.Cents != ",00" {
		t.Fatalf("card-X LimitUsed = R$ %s%s, want R$ 2.000,00 (should not include card-Y)", dataX.LimitUsed.Reais, dataX.LimitUsed.Cents)
	}

	dataY, err := buildFaturaDataForInvoice(db, wsID, "inv-y-07", "desc")
	if err != nil {
		t.Fatalf("card-y fatura: %v", err)
	}
	if dataY.LimitUsed.Reais != "5.000" || dataY.LimitUsed.Cents != ",00" {
		t.Fatalf("card-Y LimitUsed = R$ %s%s, want R$ 5.000,00 (should not include card-X)", dataY.LimitUsed.Reais, dataY.LimitUsed.Cents)
	}
}

// fmtDate helper to avoid import clutter
func fmtDate(year, month, day int) string {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}
