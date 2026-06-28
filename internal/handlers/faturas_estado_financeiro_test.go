package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func parseTestDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse date %q: %v", s, err)
	}
	return d
}

func TestLimitOpenInvoiceNoPaymentConsumesFullTotal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 100000 {
		t.Fatalf("used = %d, want 100000 (full total, no payment)", used)
	}
}

func TestLimitOpenInvoicePartialPaymentReducesUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 30000)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 70000 {
		t.Fatalf("used = %d, want 70000 (1000 - 300 partial = 700 pending)", used)
	}
}

func TestLimitClosedInvoicePartialPaymentReducesUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "CLOSED", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 30000)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 70000 {
		t.Fatalf("used = %d, want 70000 (CLOSED, partial 300 of 1000)", used)
	}
}

func TestLimitPaidInvoiceFullSettlementZeroUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 0 {
		t.Fatalf("used = %d, want 0 (PAID with full payment, pending=0)", used)
	}
}

func TestLimitInstallments10xNoPaymentFullUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	parent := "tx-parent"
	for i := int64(1); i <= 10; i++ {
		ref := time.Date(2026, time.Month(7+int(i-1)), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-" + ref
		seedInvoiceForCard(t, db, "ws", "card", invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, "ws", "card", invID, 100000, i, 10, parent)
	}

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 1000000 {
		t.Fatalf("used = %d, want 1000000 (10x100000, no payments)", used)
	}
}

func TestLimitInstallments10xFirstInvoicePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	parent := "tx-parent"
	for i := int64(1); i <= 10; i++ {
		ref := time.Date(2026, time.Month(7+int(i-1)), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-" + ref
		seedInvoiceForCard(t, db, "ws", "card", invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, "ws", "card", invID, 100000, i, 10, parent)
		if i == 1 {
			seedInvoicePaymentFull(t, db, "ws", invID, "card", 100000)
		}
	}

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 900000 {
		t.Fatalf("used = %d, want 900000 (first invoice PAID, pending=0)", used)
	}
}

func TestLimitReversedPaymentDoesNotReduceLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, reversed_at, created_at)
		VALUES ('pay-rev', 'ws', 'inv-1', 'card', 50000, ?, 'manual', ?, ?)
	`, now, now, now)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 100000 {
		t.Fatalf("used = %d, want 100000 (reversed payment does not reduce pending)", used)
	}
}

func TestLimitWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 1000000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 1000000)
	seedInvoiceForCard(t, db, "ws-a", "card-a", "inv-a", "2026-07", "OPEN", 2026, 7, 20)
	seedInvoiceForCard(t, db, "ws-b", "card-b", "inv-b", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-a", "card-a", "inv-a", 100000, 1, 1, "tx-a")
	seedExpenseOnInvoice(t, db, "ws-b", "card-b", "inv-b", 200000, 1, 1, "tx-b")

	usedA, _ := sumCardOutstandingLimit(db, "ws-a", "card-a")
	usedB, _ := sumCardOutstandingLimit(db, "ws-b", "card-b")
	if usedA != 100000 {
		t.Fatalf("ws-a used = %d, want 100000 (isolation)", usedA)
	}
	if usedB != 200000 {
		t.Fatalf("ws-b used = %d, want 200000 (isolation)", usedB)
	}
}

func TestLimitCardIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-ci", "card-a", "cc-a", 1000000)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-b', 'ws-ci', 'Card B', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-b', 'card-b', 20, 10, 1000000)
	`)
	seedInvoiceForCard(t, db, "ws-ci", "card-a", "inv-a", "2026-07", "OPEN", 2026, 7, 20)
	seedInvoiceForCard(t, db, "ws-ci", "card-b", "inv-b", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-ci", "card-a", "inv-a", 100000, 1, 1, "tx-a")
	seedExpenseOnInvoice(t, db, "ws-ci", "card-b", "inv-b", 200000, 1, 1, "tx-b")

	usedA, _ := sumCardOutstandingLimit(db, "ws-ci", "card-a")
	usedB, _ := sumCardOutstandingLimit(db, "ws-ci", "card-b")
	if usedA != 100000 {
		t.Fatalf("card-a used = %d, want 100000 (card isolation)", usedA)
	}
	if usedB != 200000 {
		t.Fatalf("card-b used = %d, want 200000 (card isolation)", usedB)
	}
}

func TestBadgeCicloFechaEmXDias(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	closingUnix := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Unix()
	label := computeCycleBadge(closingUnix, now)
	if label != "Fecha em 3 dias" {
		t.Fatalf("label = %q, want %q", label, "Fecha em 3 dias")
	}
}

func TestBadgeCicloFechada(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	closingUnix := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC).Unix()
	label := computeCycleBadge(closingUnix, now)
	if label != "Fechada" {
		t.Fatalf("label = %q, want %q", label, "Fechada")
	}
}

func TestBadgeCicloAberta(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	closingUnix := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()
	label := computeCycleBadge(closingUnix, now)
	if label != "Aberta" {
		t.Fatalf("label = %q, want %q", label, "Aberta")
	}
}

func TestBadgeFinanceiraVazia(t *testing.T) {
	label := computeFinancialBadge(0, 0)
	if label != "Vazia" {
		t.Fatalf("label = %q, want %q", label, "Vazia")
	}
}

func TestBadgeFinanceiraPendente(t *testing.T) {
	label := computeFinancialBadge(5000, 0)
	if label != "Pendente" {
		t.Fatalf("label = %q, want %q", label, "Pendente")
	}
}

func TestBadgeFinanceiraParcial(t *testing.T) {
	label := computeFinancialBadge(7000, 3000)
	if label != "Parcial" {
		t.Fatalf("label = %q, want %q", label, "Parcial")
	}
}

func TestBadgeFinanceiraPaga(t *testing.T) {
	label := computeFinancialBadge(0, 5000)
	if label != "Paga" {
		t.Fatalf("label = %q, want %q", label, "Paga")
	}
}

func TestBadgeFinanceiraPagaSobrepaid(t *testing.T) {
	label := computeFinancialBadge(-500, 1000)
	if label != "Paga" {
		t.Fatalf("label = %q, want %q", label, "Paga")
	}
}

func TestReconcileInvoiceStatusTxDropsPaidWhenPendingReturns(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ? WHERE id = 'inv-1'`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET amount = 150000 WHERE id = 'tx-1'`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if err := reconcileInvoiceStatusTx(tx, "ws", "inv-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, "inv-1").Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status == "PAID" {
		t.Fatalf("invoice should have reopened after pending increased (150000-100000=50000), still PAID")
	}
}

func TestReconcileInvoiceStatusTxSetsPaidWhenPendingZero(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if err := reconcileInvoiceStatusTx(tx, "ws", "inv-1"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, "inv-1").Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("status = %q, want PAID (pending=0)", status)
	}
}

func TestDashboardAndInvoiceShowSameLimit(t *testing.T) {
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
		t.Fatalf("invoice %s%s != dashboard %s%s", invAvailable.Reais, invAvailable.Cents, dashAvailable.Reais, dashAvailable.Cents)
	}
}

func TestNoSchemaChangeNoPartialColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	var hasPartial bool
	err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('invoices') WHERE name = 'partial'`).Scan(&hasPartial)
	if err != nil {
		t.Fatalf("pragma query: %v", err)
	}
	if hasPartial {
		t.Fatalf("invoices.partial column should NOT exist (no schema change)")
	}
	var hasPartialStatus bool
	err = db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('invoices') WHERE name = 'partial_status'`).Scan(&hasPartialStatus)
	if err != nil {
		t.Fatalf("pragma query: %v", err)
	}
	if hasPartialStatus {
		t.Fatalf("invoices.partial_status column should NOT exist (no schema change)")
	}
}

func TestAutoRoutingDoesNotRouteNewCycleToPaidInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	now := time.Now()
	pastClose := now.AddDate(0, -2, 0).Unix()
	pastDue := now.AddDate(0, -1, 15).Unix()
	futureClose := now.AddDate(0, 0, 15).Unix()
	futureDue := now.AddDate(0, 1, 5).Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES
			('inv-old', 'card', '2026-04', ?, ?, 'PAID', ?),
			('inv-current', 'card', '2026-07', ?, ?, 'OPEN', ?)
	`, pastClose, pastDue, now.Unix(), futureClose, futureDue, now.Unix())

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	txDate := now.Unix()
	invID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID == "inv-old" {
		t.Fatalf("auto-routing should not route to PAID invoice of old cycle")
	}
	// Auto-routing may find the existing OPEN invoice or create a new one;
	// the key assertion is that it does NOT route to the PAID invoice.
}

func TestPaidInvoiceAllowsMutationWithout409(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	h := TransactionHandler{DB: db, WorkspaceID: "ws", UserID: "user-ws"}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-1"}); err != nil {
		t.Fatalf("PAID invoice should not block mutation: %v", err)
	}
}

func TestPaidEarlyReceivesNewTransactionSameCycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, ref, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID != "inv-1" || ref != "2026-08" {
		t.Fatalf("expected inv-1/2026-08, got %s/%s", invID, ref)
	}
}

func TestPaidEarlyAfterClosingGoesToNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	txDate := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, ref, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID == "inv-1" || ref == "2026-08" {
		t.Fatalf("should not route to old PAID invoice, got %s/%s", invID, ref)
	}
}

func TestPaidEarlyDoesNotCreateDuplicateInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID != "inv-1" {
		t.Fatalf("should reuse existing PAID invoice inv-1, got %s", invID)
	}

	var count int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM invoices WHERE account_id = 'card' AND reference = '2026-08'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 invoice for 2026-08, got %d", count)
	}
}

func TestPaidEarlySameCycleWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 1000000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 1000000)
	seedInvoiceForCard(t, db, "ws-a", "card-a", "inv-a", "2026-08", "PAID", 2026, 7, 20)
	seedInvoiceForCard(t, db, "ws-b", "card-b", "inv-b", "2026-08", "PAID", 2026, 7, 20)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invA, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws-a", "card-a", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve ws-a: %v", err)
	}
	if invA != "inv-a" {
		t.Fatalf("ws-a should route to inv-a, got %s", invA)
	}

	invB, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws-b", "card-b", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve ws-b: %v", err)
	}
	if invB != "inv-b" {
		t.Fatalf("ws-b should route to inv-b, got %s", invB)
	}
}

func TestFaturaOffsetNextIgnoresPaidCurrentInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, ref, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "next")
	if err != nil {
		t.Fatalf("resolve next: %v", err)
	}
	if invID == "inv-1" || ref == "2026-08" {
		t.Fatalf("fatura_offset=next should not route to current PAID invoice, got %s/%s", invID, ref)
	}
	if ref != "2026-09" {
		t.Fatalf("expected next reference 2026-09, got %s", ref)
	}
}

func TestPaidEarlyReceivesTransactionAndReconcilesStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws", UserID: "user-ws"}
	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE", 50000, "Nova Compra Apos Quitacao", "", "",
		txDate, "card", "", "", 1, "paid", false, "", "", 0, false, nil, "", false,
	)
	if err != nil {
		t.Fatalf("insertTransaction: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'inv-1'`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status == "PAID" {
		t.Fatalf("invoice should have left PAID status after new expense, got %q", status)
	}

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 50000 {
		t.Fatalf("used limit = %d, want 50000 (new expense only, old was paid)", used)
	}
}

func TestHandleResolverDestinoFaturaIncludeOptionsNoHang(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-test', 'User Test', 'user-test@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-test', 'Workspace Test', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-test', 'user-test', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-test', 'ws-test', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-test', 'card-test', 20, 10, 500000)
	`)

	refs := []struct {
		ref       string
		closeDate string
		dueDate   string
		status    string
	}{
		{"2026-07", "2026-07-20", "2026-08-10", "CLOSED"},
		{"2026-08", "2026-08-20", "2026-09-10", "OPEN"},
		{"2026-09", "2026-09-20", "2026-10-10", "PAID"},
	}
	for _, r := range refs {
		closeUnix := parseTestDate(t, r.closeDate).Unix()
		dueUnix := parseTestDate(t, r.dueDate).Unix()
		execTestSQL(t, db, `
			INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, 'card-test', ?, ?, ?, ?, ?)
		`, "invoice-"+r.ref, r.ref, closeUnix, dueUnix, r.status, now)
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 25000, ?, 'Compra Teste', 'paid', 1, 1, ?, ?)
	`, parseTestDate(t, "2026-07-10").Unix(), now, now)

	handler := FaturasHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest("GET", "/cartoes/fatura-destino/card-test?data=2026-07-10&fatura_offset=auto&include_options=true", nil)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.HandleResolverDestinoFatura(rr, req, "card-test")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("HandleResolverDestinoFatura with include_options timed out (hang)")
	}

	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var payload struct {
		Reference string          `json:"reference"`
		Options   []InvoiceOption `json:"options"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Options) != 3 {
		t.Fatalf("options count = %d, want 3", len(payload.Options))
	}
	for i := 0; i < len(payload.Options)-1; i++ {
		if payload.Options[i].Reference >= payload.Options[i+1].Reference {
			t.Fatalf("options not sorted ascending: %v", payload.Options)
		}
	}

	var sensitiveCount int
	for _, opt := range payload.Options {
		if opt.IsSensitive {
			sensitiveCount++
		}
	}
	if sensitiveCount != 2 {
		t.Fatalf("sensitive options = %d, want 2 (CLOSED + PAID)", sensitiveCount)
	}

	_ = time.After(1 * time.Millisecond)
}