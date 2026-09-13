package handlers

import (
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func seedReportHelpersScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-rpt', 'User Rpt', 'u-rpt@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-rpt', 'WS Rpt', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-rpt', 'u-rpt', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-rpt', 'ws-rpt', 'Conta Corrente', 'CHECKING', 500000, 500000, ?, ?),
		('sav-rpt', 'ws-rpt', 'Poupanca', 'SAVINGS', 200000, 200000, ?, ?),
		('card-rpt', 'ws-rpt', 'Cartao Visa', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-rpt', 'card-rpt', 20, 10, 500000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-food', 'ws-rpt', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?),
		('cat-fun', 'ws-rpt', 'Lazer', 'gamepad', '#8b5cf6', 'EXPENSE', 'Estilo de Vida', ?),
		('cat-salary', 'ws-rpt', 'Salario', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-card-food', 'ws-rpt', 'Compras Cartao', 'shopping-bag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now, now, now, now)
}

func seedReportHelpersSecondWorkspace(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-rpt2', 'User Rpt2', 'u-rpt2@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-rpt2', 'WS Rpt2', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-rpt2', 'u-rpt2', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-rpt2', 'ws-rpt2', 'Conta Outro WS', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-other', 'ws-rpt2', 'Outra Categoria', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now)
}

func TestReportHelpersResolveEffectiveCashDate(t *testing.T) {
	paidAt := int64(1700000000)
	date := int64(1690000000)

	tx := models.Transaction{
		Date:   date,
		PaidAt: &paidAt,
	}
	if got := ResolveEffectiveCashDate(tx); got != paidAt {
		t.Fatalf("ResolveEffectiveCashDate with paid_at = %d, want %d", got, paidAt)
	}

	txNoPaid := models.Transaction{
		Date:   date,
		PaidAt: nil,
	}
	if got := ResolveEffectiveCashDate(txNoPaid); got != date {
		t.Fatalf("ResolveEffectiveCashDate without paid_at = %d, want %d", got, date)
	}

	zeroPaid := int64(0)
	txZeroPaid := models.Transaction{
		Date:   date,
		PaidAt: &zeroPaid,
	}
	if got := ResolveEffectiveCashDate(txZeroPaid); got != date {
		t.Fatalf("ResolveEffectiveCashDate with zero paid_at = %d, want %d", got, date)
	}
}

func TestReportHelpersResolveCompetenceDate(t *testing.T) {
	date := int64(1690000000)
	paidAt := int64(1700000000)

	tx := models.Transaction{
		Date:   date,
		PaidAt: &paidAt,
	}
	if got := ResolveCompetenceDate(tx); got != date {
		t.Fatalf("ResolveCompetenceDate = %d, want %d (always date)", got, date)
	}
}

func TestReportHelpersResolveForecastDate(t *testing.T) {
	date := int64(1690000000)
	dueDate := int64(1700000000)

	tx := models.Transaction{
		Date:    date,
		DueDate: &dueDate,
	}
	if got := ResolveForecastDate(tx); got != dueDate {
		t.Fatalf("ResolveForecastDate with due_date = %d, want %d", got, dueDate)
	}

	txNoDue := models.Transaction{
		Date:    date,
		DueDate: nil,
	}
	if got := ResolveForecastDate(txNoDue); got != date {
		t.Fatalf("ResolveForecastDate without due_date = %d, want %d", got, date)
	}

	zeroDue := int64(0)
	txZeroDue := models.Transaction{
		Date:    date,
		DueDate: &zeroDue,
	}
	if got := ResolveForecastDate(txZeroDue); got != date {
		t.Fatalf("ResolveForecastDate with zero due_date = %d, want %d", got, date)
	}
}

func TestReportHelpersReceitaPaidComPaidAtDiferenteDeDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-rpt'`)

	dateUnix := testUnixDate("2026-08-05")
	paidAtUnix := testUnixDate("2026-08-12")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, paid_at, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('inc-paid-late', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 200000, ?, ?, 'Freela pago depois', 'paid', 1, 1, ?, ?)
	`, dateUnix, paidAtUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, _, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	found := false
	for _, ev := range realized {
		if ev.Description == "Freela pago depois" {
			found = true
			if ev.DateUnix != paidAtUnix {
				t.Fatalf("cash event date = %d, want paid_at %d", ev.DateUnix, paidAtUnix)
			}
			if ev.AmountCents != 200000 {
				t.Fatalf("cash event amount = %d, want 200000", ev.AmountCents)
			}
		}
	}
	if !found {
		t.Fatal("realized cash event for 'Freela pago depois' not found")
	}

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var dreTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			dreTotal += row.AmountCents
		}
	}
	if dreTotal != 200000 {
		t.Fatalf("DRE august total = %d, want 200000 (competence date)", dreTotal)
	}
}

func TestReportHelpersReceitaPendingEntraSoNoPrevisto(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-rpt'`)

	dateUnix := testUnixDate("2026-08-05")
	dueUnix := testUnixDate("2026-08-15")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('inc-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 100000, ?, ?, 'Freela pendente', 'pending', 1, 1, ?, ?)
	`, dateUnix, dueUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	for _, ev := range realized {
		if ev.Description == "Freela pendente" {
			t.Fatal("pending income should NOT appear in realized cash")
		}
	}

	found := false
	for _, ev := range projected {
		if ev.Description == "Freela pendente" {
			found = true
			if ev.DateUnix != dueUnix {
				t.Fatalf("projected event date = %d, want due_date %d", ev.DateUnix, dueUnix)
			}
			if ev.AmountCents != 100000 {
				t.Fatalf("projected event amount = %d, want 100000", ev.AmountCents)
			}
		}
	}
	if !found {
		t.Fatal("projected cash event for pending income not found")
	}

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var dreTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			dreTotal += row.AmountCents
		}
	}
	if dreTotal != 100000 {
		t.Fatalf("DRE august total = %d, want 100000 (pending enters DRE by date)", dreTotal)
	}
}

func TestReportHelpersDespesaPaidComPaidAtDiferenteDeDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-05")
	paidAtUnix := testUnixDate("2026-08-12")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, paid_at, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('exp-paid-late', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 50000, ?, ?, 'Mercado pago depois', 'paid', 1, 1, ?, ?)
	`, dateUnix, paidAtUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, _, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	found := false
	for _, ev := range realized {
		if ev.Description == "Mercado pago depois" {
			found = true
			if ev.DateUnix != paidAtUnix {
				t.Fatalf("cash event date = %d, want paid_at %d", ev.DateUnix, paidAtUnix)
			}
			if ev.AmountCents != 50000 {
				t.Fatalf("cash event amount = %d, want 50000", ev.AmountCents)
			}
		}
	}
	if !found {
		t.Fatal("realized cash event for expense not found")
	}

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var dreTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			dreTotal += row.AmountCents
		}
	}
	if dreTotal != -50000 {
		t.Fatalf("DRE august total = %d, want -50000 (competence date)", dreTotal)
	}
}

func TestReportHelpersDespesaVencidaPendingApareceComoPrevistoVencido(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-07-10")
	dueUnix := testUnixDate("2026-07-28")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('exp-overdue', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, ?, 'Conta vencida', 'pending', 1, 1, ?, ?)
	`, dateUnix, dueUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 7)

	_, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	found := false
	for _, ev := range projected {
		if ev.Description == "Conta vencida" {
			found = true
			if ev.DateUnix != dueUnix {
				t.Fatalf("projected event date = %d, want due_date %d", ev.DateUnix, dueUnix)
			}
			if ev.AmountCents != 30000 {
				t.Fatalf("projected event amount = %d, want 30000", ev.AmountCents)
			}
			if ev.IsRealized {
				t.Fatal("overdue pending should be projected, not realized")
			}
		}
	}
	if !found {
		t.Fatal("overdue pending expense not found in projected")
	}
}

func TestReportHelpersCompraCartaoNaoAlteraCaixaRealizado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-card-01', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, time.Now().Unix())
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-purchase', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-card-01', 'EXPENSE', 15000, ?, 'Compra cartao supermercado', 'paid', 1, 1, ?, ?)
	`, dateUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, _, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	for _, ev := range realized {
		if ev.Description == "Compra cartao supermercado" {
			t.Fatal("credit card purchase should NOT appear in realized cash")
		}
	}

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var dreTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			dreTotal += row.AmountCents
		}
	}
	if dreTotal != -15000 {
		t.Fatalf("DRE august total = %d, want -15000 (card purchase enters DRE by date)", dreTotal)
	}
}

func TestReportHelpersFaturaAbertaEntraNoPrevisto(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-card-open', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, time.Now().Unix())
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-purchase-open', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-card-open', 'EXPENSE', 25000, ?, 'Compra delivery', 'paid', 1, 1, ?, ?)
	`, purchaseDate, time.Now().Unix(), time.Now().Unix())

	septPeriod := NewReportPeriodFromMonth(2026, 9)

	_, projected, err := BuildCashFlowData(db, "ws-rpt", septPeriod)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	found := false
	for _, ev := range projected {
		if ev.Source == CashSourceInvoiceProjected {
			found = true
			if ev.DateUnix != invoiceDue {
				t.Fatalf("invoice projected date = %d, want due_date %d", ev.DateUnix, invoiceDue)
			}
			if ev.AmountCents != 25000 {
				t.Fatalf("invoice projected amount = %d, want 25000", ev.AmountCents)
			}
			if ev.IsRealized {
				t.Fatal("invoice projected should not be realized")
			}
		}
	}
	if !found {
		t.Fatal("open invoice not found in projected cash")
	}
}

func TestReportHelpersPagamentoParcialFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-partial', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-purchase-partial', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-partial', 'EXPENSE', 40000, ?, 'Compra grande', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-partial', 'ws-rpt', 'u-rpt', 'chk-rpt', NULL, 'EXPENSE', 25000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('pay-row-partial', 'ws-rpt', 'inv-partial', 'chk-rpt', 'pay-partial', 25000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	septPeriod := NewReportPeriodFromMonth(2026, 9)

	realized, projected, err := BuildCashFlowData(db, "ws-rpt", septPeriod)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	foundPayment := false
	for _, ev := range realized {
		if ev.Source == CashSourceInvoicePayment {
			foundPayment = true
			if ev.DateUnix != paymentDate {
				t.Fatalf("invoice payment date = %d, want %d", ev.DateUnix, paymentDate)
			}
			if ev.AmountCents != 25000 {
				t.Fatalf("invoice payment amount = %d, want 25000", ev.AmountCents)
			}
		}
	}
	if !foundPayment {
		t.Fatal("partial invoice payment not found in realized cash")
	}

	foundProjected := false
	for _, ev := range projected {
		if ev.Source == CashSourceInvoiceProjected {
			foundProjected = true
			pendingExpected := int64(15000)
			if ev.AmountCents != pendingExpected {
				t.Fatalf("invoice projected pending = %d, want %d (40000 total - 25000 paid)", ev.AmountCents, pendingExpected)
			}
		}
	}
	if !foundProjected {
		t.Fatal("remaining invoice pending not found in projected")
	}
}

func TestReportHelpersPagamentoTotalFaturaZeraPendente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-full', 'card-rpt', '2026-08', ?, ?, 'PAID', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-purchase-full', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-full', 'EXPENSE', 40000, ?, 'Compra total', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-full', 'ws-rpt', 'u-rpt', 'chk-rpt', NULL, 'EXPENSE', 40000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('pay-row-full', 'ws-rpt', 'inv-full', 'chk-rpt', 'pay-full', 40000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	septPeriod := NewReportPeriodFromMonth(2026, 9)

	_, projected, err := BuildCashFlowData(db, "ws-rpt", septPeriod)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	for _, ev := range projected {
		if ev.Source == CashSourceInvoiceProjected {
			t.Fatalf("fully paid invoice should NOT appear in projected; got amount=%d", ev.AmountCents)
		}
	}
}

func TestReportHelpersCurrentInvoiceAllReversedDoesNotUseLegacy(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedReportHelpersScenario(t, db)

	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-08-05")
	dueDate := testUnixDate("2026-09-10")
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES ('inv-current-reversed', 'card-rpt', '2026-08', ?, ?, 'OPEN', 40000, ?)
	`, testUnixDate("2026-08-20"), dueDate, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-current-reversed', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-current-reversed', 'EXPENSE', 40000, ?, 'Compra revertida current', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, reversed_at, created_at)
		VALUES ('ip-current-reversed', 'ws-rpt', 'inv-current-reversed', 'chk-rpt', 40000, ?, 'manual', ?, ?)
	`, testUnixDate("2026-09-01"), testUnixDate("2026-09-02"), now)

	period := NewReportPeriodFromMonth(2026, 9)
	for name, query := range map[string]func(*sql.DB, string, ReportPeriod) ([]CashEvent, error){
		"projected":   QueryInvoiceProjectedCashEvents,
		"by_due_date": QueryInvoiceCashEventsByDueDate,
	} {
		events, err := query(db, "ws-rpt", period)
		if err != nil {
			t.Fatalf("%s query: %v", name, err)
		}
		if len(events) != 1 || events[0].AmountCents != 40000 {
			t.Fatalf("%s events = %#v, want one CURRENT pending event of 40000", name, events)
		}
	}

	projectedBalance, show, err := BuildProjectedBalanceUntilPeriodEnd(db, "ws-rpt", period.End, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("BuildProjectedBalanceUntilPeriodEnd: %v", err)
	}
	if !show || projectedBalance != 660000 {
		t.Fatalf("projected balance = %d/show:%v, want 660000/true", projectedBalance, show)
	}
}

func TestReportHelpersCompraCartaoPagamentoFaturaNaoDuplicaCaixa(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-no-dup', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-purchase-no-dup', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-no-dup', 'EXPENSE', 30000, ?, 'Compra sem duplicar', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-no-dup', 'ws-rpt', 'u-rpt', 'chk-rpt', NULL, 'EXPENSE', 30000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('pay-row-no-dup', 'ws-rpt', 'inv-no-dup', 'chk-rpt', 'pay-no-dup', 30000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	septPeriod := NewReportPeriodFromMonth(2026, 9)

	realized, _, err := BuildCashFlowData(db, "ws-rpt", septPeriod)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	var cardPurchaseFound bool
	var invoicePaymentCount int
	for _, ev := range realized {
		if ev.Description == "Compra sem duplicar" {
			cardPurchaseFound = true
		}
		if ev.Source == CashSourceInvoicePayment {
			invoicePaymentCount++
		}
	}

	if cardPurchaseFound {
		t.Fatal("credit card purchase should NOT appear in realized cash")
	}
	if invoicePaymentCount != 1 {
		t.Fatalf("invoice payment events = %d, want 1 as the only cash event for card spending", invoicePaymentCount)
	}
	if got := mustSumCashExpense(t, realized); got != 30000 {
		t.Fatalf("realized expense = %d, want 30000 without duplicated invoice payment", got)
	}
}

func TestReportHelpersDREIncluiCompraCartaoEExcluiPagamentoFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-dre', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('card-purchase-dre', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-dre', 'EXPENSE', 30000, ?, 'Compra cartao DRE', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-dre', 'ws-rpt', 'u-rpt', 'chk-rpt', 'EXPENSE', 30000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('pay-row-dre', 'ws-rpt', 'inv-dre', 'chk-rpt', 'pay-dre', 30000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var augustTotal, septTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			augustTotal += row.AmountCents
		}
		if row.Competencia == "2026-09" {
			septTotal += row.AmountCents
		}
	}

	if augustTotal != -30000 {
		t.Fatalf("DRE august total = %d, want -30000 (card purchase enters DRE in august by date)", augustTotal)
	}
	if septTotal != 0 {
		t.Fatalf("DRE sept total = %d, want 0 (invoice payment excluded from DRE)", septTotal)
	}
}

func TestReportHelpersTransferenciaInternaZeraConsolidado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	transferDate := testUnixDate("2026-08-10")
	transferAmount := int64(50000)

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('transfer-01', 'ws-rpt', 'u-rpt', 'chk-rpt', 'sav-rpt', 'TRANSFER', ?, ?, 'Transferencia interna', 'paid', ?, 1, 1, ?, ?)
	`, transferAmount, transferDate, transferDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	for _, ev := range realized {
		if ev.Description == "Transferencia interna" {
			t.Fatal("transfer should NOT appear in consolidated cash events")
		}
	}
	for _, ev := range projected {
		if ev.Description == "Transferencia interna" {
			t.Fatal("transfer should NOT appear in projected cash events")
		}
	}
}

func TestReportHelpersTransferenciaAparecePorContaNoExtrato(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	transferDate := testUnixDate("2026-08-10")
	transferAmount := int64(50000)

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('transfer-stmt', 'ws-rpt', 'u-rpt', 'chk-rpt', 'sav-rpt', 'TRANSFER', ?, ?, 'Transferencia para poupanca', 'paid', ?, 1, 1, ?, ?)
	`, transferAmount, transferDate, transferDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	stmtChk, err := BuildStatementData(db, "ws-rpt", "chk-rpt", period)
	if err != nil {
		t.Fatalf("BuildStatementData checking: %v", err)
	}
	stmtSav, err := BuildStatementData(db, "ws-rpt", "sav-rpt", period)
	if err != nil {
		t.Fatalf("BuildStatementData savings: %v", err)
	}

	foundInChecking := false
	for _, row := range stmtChk {
		if row.Description == "Transferencia para poupanca" {
			foundInChecking = true
			if row.AmountCents != transferAmount {
				t.Fatalf("transfer amount in checking = %d, want %d", row.AmountCents, transferAmount)
			}
			if row.Type != "TRANSFER" {
				t.Fatalf("transfer type = %s, want TRANSFER", row.Type)
			}
		}
	}
	if !foundInChecking {
		t.Fatal("transfer not found in checking account statement")
	}

	foundInSavings := false
	for _, row := range stmtSav {
		if row.Description == "Transferencia para poupanca" {
			foundInSavings = true
		}
	}
	if !foundInSavings {
		t.Fatal("transfer not found in savings account statement (destination_account_id)")
	}
}

func TestReportHelpersBusinessCustosPorMacroGrupo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-biz', 'Biz User', 'u-biz@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-biz', 'Empresa X', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-biz', 'u-biz', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('chk-biz', 'ws-biz', 'Conta PJ', 'CHECKING', 1000000, 1000000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-biz-rev', 'ws-biz', 'Consultoria', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('cat-biz-cost', 'ws-biz', 'Servidor', 'server', '#f97316', 'EXPENSE', 'Custos Operacionais', ?),
		('cat-biz-mkt', 'ws-biz', 'Marketing', 'megaphone', '#8b5cf6', 'EXPENSE', 'Custos Operacionais', ?)
	`, now, now, now)

	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('biz-inc-1', 'ws-biz', 'u-biz', 'chk-biz', 'cat-biz-rev', 'INCOME', 500000, ?, 'Consultoria agosto', 'paid', ?, 1, 1, ?, ?),
			('biz-exp-1', 'ws-biz', 'u-biz', 'chk-biz', 'cat-biz-cost', 'EXPENSE', 80000, ?, 'Servidor cloud', 'paid', ?, 1, 1, ?, ?),
			('biz-exp-2', 'ws-biz', 'u-biz', 'chk-biz', 'cat-biz-mkt', 'EXPENSE', 20000, ?, 'Ads', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, now, now, augDate, augDate, now, now, augDate, augDate, now, now)

	dreRows, err := BuildDREData(db, "ws-biz", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var operatingCostsTotal int64
	var operatingRevenueTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			if row.MacroGroup == "Custos Operacionais" {
				operatingCostsTotal += row.AmountCents
			}
			if row.MacroGroup == "Receitas Operacionais" {
				operatingRevenueTotal += row.AmountCents
			}
		}
	}

	if operatingCostsTotal != -100000 {
		t.Fatalf("Custos Operacionais total = %d, want -100000", operatingCostsTotal)
	}
	if operatingRevenueTotal != 500000 {
		t.Fatalf("Receitas Operacionais total = %d, want 500000", operatingRevenueTotal)
	}

	period := NewReportPeriodFromMonth(2026, 8)
	realized, _, err := BuildCashFlowData(db, "ws-biz", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	netCash := mustSumCashEvents(t, realized)
	if netCash != 400000 {
		t.Fatalf("business net cash = %d, want 400000 (500000 income - 100000 expenses)", netCash)
	}
}

func TestReportHelpersPersonalEssencialEstiloDeVida(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-rpt'`)

	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('pers-food', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, 'Supermercado', 'paid', ?, 1, 1, ?, ?),
			('pers-fun', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-fun', 'EXPENSE', 15000, ?, 'Cinema', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var essentialTotal, lifestyleTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			if row.MacroGroup == "Essencial" {
				essentialTotal += row.AmountCents
			}
			if row.MacroGroup == "Estilo de Vida" {
				lifestyleTotal += row.AmountCents
			}
		}
	}

	if essentialTotal != -30000 {
		t.Fatalf("Essencial total = %d, want -30000", essentialTotal)
	}
	if lifestyleTotal != -15000 {
		t.Fatalf("Estilo de Vida total = %d, want -15000", lifestyleTotal)
	}
}

func TestReportHelpersWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	seedReportHelpersSecondWorkspace(t, db)

	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ws1-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 50000, ?, 'Despesa WS1', 'paid', ?, 1, 1, ?, ?),
			('ws2-exp', 'ws-rpt2', 'u-rpt2', 'chk-rpt2', 'cat-other', 'EXPENSE', 99999, ?, 'Despesa WS2', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realizedWS1, _, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData ws1: %v", err)
	}

	for _, ev := range realizedWS1 {
		if ev.Description == "Despesa WS2" {
			t.Fatal("WS2 expense leaked into WS1 cash flow")
		}
	}

	var foundWS1 bool
	for _, ev := range realizedWS1 {
		if ev.Description == "Despesa WS1" {
			foundWS1 = true
		}
	}
	if !foundWS1 {
		t.Fatal("WS1 expense not found in own cash flow")
	}

	realizedWS2, _, err := BuildCashFlowData(db, "ws-rpt2", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData ws2: %v", err)
	}

	for _, ev := range realizedWS2 {
		if ev.Description == "Despesa WS1" {
			t.Fatal("WS1 expense leaked into WS2 cash flow")
		}
	}

	dreWS1, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData ws1: %v", err)
	}
	dreWS2, err := BuildDREData(db, "ws-rpt2", 2026)
	if err != nil {
		t.Fatalf("BuildDREData ws2: %v", err)
	}

	var ws1Total int64
	for _, row := range dreWS1 {
		if row.Competencia == "2026-08" {
			ws1Total += row.AmountCents
		}
	}
	if ws1Total != -50000 {
		t.Fatalf("WS1 DRE total = %d, want -50000", ws1Total)
	}

	var ws2Total int64
	for _, row := range dreWS2 {
		if row.Competencia == "2026-08" {
			ws2Total += row.AmountCents
		}
	}
	if ws2Total != -99999 {
		t.Fatalf("WS2 DRE total = %d, want -99999", ws2Total)
	}
}

func TestReportHelpersFallbackLegadoSemPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-05")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('legacy-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 45000, ?, 'Despesa antiga sem paid_at', 'paid', 1, 1, ?, ?)
	`, dateUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, _, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	found := false
	for _, ev := range realized {
		if ev.Description == "Despesa antiga sem paid_at" {
			found = true
			if ev.DateUnix != dateUnix {
				t.Fatalf("legacy cash event date = %d, want date %d (fallback quando paid_at ausente)", ev.DateUnix, dateUnix)
			}
			if ev.AmountCents != 45000 {
				t.Fatalf("legacy cash event amount = %d, want 45000", ev.AmountCents)
			}
			if !ev.IsRealized {
				t.Fatal("legacy paid should be realized")
			}
		}
	}

	if !found {
		t.Fatal("legacy paid transaction without paid_at MUST appear in realized cash (fallback to date)")
	}

	_, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData projected: %v", err)
	}

	for _, ev := range projected {
		if ev.Description == "Despesa antiga sem paid_at" {
			t.Fatal("paid transaction should NOT appear in projected (it IS paid)")
		}
	}

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var augustTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			augustTotal += row.AmountCents
		}
	}
	if augustTotal != -45000 {
		t.Fatalf("DRE august total = %d, want -45000 (legacy enters DRE by date)", augustTotal)
	}
}

func TestReportHelpersFallbackLegadoComPaidAtZero(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-05")
	zeroPaidAt := int64(0)

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, paid_at, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('legacy-zero', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 35000, ?, ?, ?, 'Despesa com paid_at zero', 'paid', 1, 1, ?, ?)
	`, dateUnix, zeroPaidAt, dateUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	realized, _, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	found := false
	for _, ev := range realized {
		if ev.Description == "Despesa com paid_at zero" {
			found = true
			if ev.DateUnix != dateUnix {
				t.Fatalf("paid_at=0 cash event date = %d, want date %d (NULLIF trata 0 como ausente)", ev.DateUnix, dateUnix)
			}
			if ev.AmountCents != 35000 {
				t.Fatalf("paid_at=0 cash event amount = %d, want 35000", ev.AmountCents)
			}
			if !ev.IsRealized {
				t.Fatal("paid transaction with paid_at=0 should be realized (fallback to date)")
			}
		}
	}

	if !found {
		t.Fatal("paid transaction with paid_at=0 MUST appear in realized cash (NULLIF treats 0 as absent)")
	}

	dreRows, err := BuildDREData(db, "ws-rpt", 2026)
	if err != nil {
		t.Fatalf("BuildDREData: %v", err)
	}

	var augustTotal int64
	for _, row := range dreRows {
		if row.Competencia == "2026-08" {
			augustTotal += row.AmountCents
		}
	}
	if augustTotal != -35000 {
		t.Fatalf("DRE august total = %d, want -35000 (paid_at=0 enters DRE by date)", augustTotal)
	}
}

func TestReportHelpersStatementData(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('stmt-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 100000, ?, 'Salario', 'paid', ?, 1, 1, ?, ?),
			('stmt-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 25000, ?, 'Mercado', 'paid', ?, 1, 1, ?, ?),
			('stmt-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 10000, ?, 'Conta futura', 'pending', NULL, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	stmtRows, err := BuildStatementData(db, "ws-rpt", "chk-rpt", period)
	if err != nil {
		t.Fatalf("BuildStatementData: %v", err)
	}

	if len(stmtRows) != 3 {
		t.Fatalf("statement rows = %d, want 3", len(stmtRows))
	}

	for _, row := range stmtRows {
		if row.Description == "Salario" && !row.IsRealized {
			t.Fatal("paid income should be realized in statement")
		}
		if row.Description == "Mercado" && !row.IsRealized {
			t.Fatal("paid expense should be realized in statement")
		}
		if row.Description == "Conta futura" && row.IsRealized {
			t.Fatal("pending expense should NOT be realized in statement")
		}
	}
}

func TestReportHelpersAccountingCloseData(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('close-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 200000, ?, 'Salario', 'paid', ?, 1, 1, ?, ?),
			('close-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 80000, ?, 'Mercado', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	closeData, err := BuildAccountingCloseData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildAccountingCloseData: %v", err)
	}

	if closeData.TotalIncomeRealized != 200000 {
		t.Fatalf("TotalIncomeRealized = %d, want 200000", closeData.TotalIncomeRealized)
	}
	if closeData.TotalExpenseRealized != 80000 {
		t.Fatalf("TotalExpenseRealized = %d, want 80000", closeData.TotalExpenseRealized)
	}
	if closeData.NetResult != 120000 {
		t.Fatalf("NetResult = %d, want 120000", closeData.NetResult)
	}
	if closeData.RealizedNetCashflow != 120000 {
		t.Fatalf("RealizedNetCashflow = %d, want 120000", closeData.RealizedNetCashflow)
	}
}

func TestReportHelpersReportPeriod(t *testing.T) {
	period := NewReportPeriodFromMonth(2026, 8)

	augStart := testUnixDate("2026-08-01")
	augEnd := testUnixDate("2026-08-31")
	endOfDay := int64(23*3600 + 59*60 + 59)

	if period.Start != augStart {
		t.Fatalf("period start = %d, want %d", period.Start, augStart)
	}
	if period.End != augEnd+endOfDay {
		t.Fatalf("period end = %d, want %d", period.End, augEnd+endOfDay)
	}

	yearPeriod := NewReportPeriodFromYear(2026)
	janStart := testUnixDate("2026-01-01")
	decEnd := testUnixDate("2026-12-31") + endOfDay

	if yearPeriod.Start != janStart {
		t.Fatalf("year period start = %d, want %d", yearPeriod.Start, janStart)
	}
	if yearPeriod.End != decEnd {
		t.Fatalf("year period end = %d, want %d", yearPeriod.End, decEnd)
	}
}

func mustSumCashEvents(t *testing.T, events []CashEvent) int64 {
	t.Helper()
	total, err := SumCashEvents(events)
	if err != nil {
		t.Fatalf("SumCashEvents: %v", err)
	}
	return total
}

func mustSumCashIncome(t *testing.T, events []CashEvent) int64 {
	t.Helper()
	total, err := SumCashIncome(events)
	if err != nil {
		t.Fatalf("SumCashIncome: %v", err)
	}
	return total
}

func mustSumCashExpense(t *testing.T, events []CashEvent) int64 {
	t.Helper()
	total, err := SumCashExpense(events)
	if err != nil {
		t.Fatalf("SumCashExpense: %v", err)
	}
	return total
}

func TestReportHelpersSumCashEvents(t *testing.T) {
	events := []CashEvent{
		{IsIncome: true, AmountCents: 100000},
		{IsIncome: false, AmountCents: 30000},
		{IsIncome: true, AmountCents: 50000},
		{IsIncome: false, AmountCents: 20000},
	}

	net := mustSumCashEvents(t, events)
	if net != 100000 {
		t.Fatalf("SumCashEvents = %d, want 100000 (150000 income - 50000 expense)", net)
	}
}

// TestSumCashExactLimits garante que valores exatamente nos limites de int64
// permanecem válidos: nada de rejeitar a fronteira legítima.
func TestSumCashExactLimits(t *testing.T) {
	cases := []struct {
		name   string
		events []CashEvent
		call   func([]CashEvent) (int64, error)
		want   int64
	}{
		{"SumCashIncome MaxInt64 sozinho", []CashEvent{{IsIncome: true, AmountCents: math.MaxInt64}}, SumCashIncome, math.MaxInt64},
		{"SumCashIncome zero + MaxInt64", []CashEvent{{IsIncome: true, AmountCents: 0}, {IsIncome: true, AmountCents: math.MaxInt64}}, SumCashIncome, math.MaxInt64},
		{"SumCashIncome MaxInt64-1 + 1", []CashEvent{{IsIncome: true, AmountCents: math.MaxInt64 - 1}, {IsIncome: true, AmountCents: 1}}, SumCashIncome, math.MaxInt64},
		{"SumCashIncome MinInt64 sozinho", []CashEvent{{IsIncome: true, AmountCents: math.MinInt64}}, SumCashIncome, math.MinInt64},
		{"SumCashIncome MinInt64+1 + (-1)", []CashEvent{{IsIncome: true, AmountCents: math.MinInt64 + 1}, {IsIncome: true, AmountCents: -1}}, SumCashIncome, math.MinInt64},
		{"SumCashExpense MaxInt64 sozinho", []CashEvent{{AmountCents: math.MaxInt64}}, SumCashExpense, math.MaxInt64},
		{"SumCashEvents MaxInt64 receita isolada", []CashEvent{{IsIncome: true, AmountCents: math.MaxInt64}}, SumCashEvents, math.MaxInt64},
		{"SumCashEvents MinInt64 receita isolada", []CashEvent{{IsIncome: true, AmountCents: math.MinInt64}}, SumCashEvents, math.MinInt64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(tc.events)
			if err != nil {
				t.Fatalf("exact int64 limit must succeed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSumCashOverflowAndUnderflow garante que ultrapassar int64 em qualquer
// direção falha de forma explícita, sem saturação e sem wrap silencioso.
func TestSumCashOverflowAndUnderflow(t *testing.T) {
	cases := []struct {
		name   string
		events []CashEvent
		call   func([]CashEvent) (int64, error)
	}{
		{"SumCashIncome MaxInt64 + 1", []CashEvent{{IsIncome: true, AmountCents: math.MaxInt64}, {IsIncome: true, AmountCents: 1}}, SumCashIncome},
		{"SumCashIncome MinInt64 + (-1)", []CashEvent{{IsIncome: true, AmountCents: math.MinInt64}, {IsIncome: true, AmountCents: -1}}, SumCashIncome},
		{"SumCashExpense MaxInt64 + 1", []CashEvent{{AmountCents: math.MaxInt64}, {AmountCents: 1}}, SumCashExpense},
		{"SumCashEvents net MaxInt64 + 1", []CashEvent{{IsIncome: true, AmountCents: math.MaxInt64}, {IsIncome: true, AmountCents: 1}}, SumCashEvents},
		{"SumCashEvents net MinInt64 - 1", []CashEvent{{IsIncome: true, AmountCents: math.MinInt64}, {AmountCents: 1}}, SumCashEvents},
		{"SumCashEvents zero - MinInt64", []CashEvent{{AmountCents: math.MinInt64}}, SumCashEvents},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call(tc.events)
			if err == nil {
				t.Fatalf("expected overflow/underflow error, got %d", got)
			}
			if !errors.Is(err, errReportAggregateOverflow) {
				t.Fatalf("expected errReportAggregateOverflow, got %v", err)
			}
			if got != 0 {
				t.Fatalf("error path must not return a value, got %d", got)
			}
		})
	}
}

func TestRelatoriosBusinessCostsByMacro(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-biz2', 'Biz2', 'u-biz2@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-biz2', 'Biz WS', 'business', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-biz2', 'u-biz2', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('chk-biz2', 'ws-biz2', 'Conta PJ', 'CHECKING', 1000000, 1000000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('biz-cat-rev', 'ws-biz2', 'Consultoria', 'briefcase', '#22c55e', 'INCOME', 'Receitas Operacionais', ?),
		('biz-cat-srv', 'ws-biz2', 'Servidor', 'server', '#f97316', 'EXPENSE', 'Custos Operacionais', ?),
		('biz-cat-mkt', 'ws-biz2', 'Marketing', 'megaphone', '#8b5cf6', 'EXPENSE', 'Custos Operacionais', ?),
		('biz-cat-esc', 'ws-biz2', 'Escritorio', 'building', '#ec4899', 'EXPENSE', 'Custos Operacionais', ?)
	`, now, now, now, now)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('biz2-rev', 'ws-biz2', 'u-biz2', 'chk-biz2', 'biz-cat-rev', 'INCOME', 800000, ?, 'Consultoria', 'paid', ?, 1, 1, ?, ?),
			('biz2-srv', 'ws-biz2', 'u-biz2', 'chk-biz2', 'biz-cat-srv', 'EXPENSE', 120000, ?, 'Servidor', 'paid', ?, 1, 1, ?, ?),
			('biz2-mkt', 'ws-biz2', 'u-biz2', 'chk-biz2', 'biz-cat-mkt', 'EXPENSE', 50000, ?, 'Ads', 'paid', ?, 1, 1, ?, ?),
			('biz2-esc', 'ws-biz2', 'u-biz2', 'chk-biz2', 'biz-cat-esc', 'EXPENSE', 30000, ?, 'Aluguel', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, now, now, augDate, augDate, now, now, augDate, augDate, now, now, augDate, augDate, now, now)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-biz2", UserID: "u-biz2"}

	costs, err := handler.queryBusinessCostsByMacro(nil, 8, 2026)
	if err != nil {
		t.Fatalf("queryBusinessCostsByMacro: %v", err)
	}

	if len(costs) == 0 {
		t.Fatal("expected at least one macro group cost")
	}

	var foundOperational bool
	for _, c := range costs {
		if c.MacroGroup == "Custos Operacionais" {
			foundOperational = true
			if c.RawAmount != 200000 {
				t.Fatalf("Custos Operacionais = %d, want 200000 (120+50+30k)", c.RawAmount)
			}
			if c.Percent != 100 {
				t.Fatalf("Custos Operacionais percent = %d, want 100", c.Percent)
			}
		}
	}
	if !foundOperational {
		t.Fatalf("Custos Operacionais not found in business costs: %#v", costs)
	}
}

func TestRelatoriosCashFlowKPIsInData(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('cf-income', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 150000, ?, 'Salario', 'paid', ?, 1, 1, ?, ?),
			('cf-expense', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 40000, ?, 'Mercado', 'paid', ?, 1, 1, ?, ?),
			('cf-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 20000, ?, 'Conta futura', 'pending', NULL, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)
	realized, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	realizedNet := mustSumCashEvents(t, realized)
	projectedNet := mustSumCashEvents(t, projected)

	if realizedNet != 110000 {
		t.Fatalf("realized net = %d, want 110000 (150000 income - 40000 expense)", realizedNet)
	}
	if projectedNet != -20000 {
		t.Fatalf("projected net = %d, want -20000 (pending expense)", projectedNet)
	}
}

func TestRelatoriosCashFlowExcludesCreditCardFromRealized(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-05")
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-cf-kpi', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, testUnixDate("2026-08-20"), testUnixDate("2026-09-10"), now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('cf-card', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-cf-kpi', 'EXPENSE', 50000, ?, 'Compra cartao KPI', 'paid', 1, 1, ?, ?)
	`, dateUnix, now, now)

	period := NewReportPeriodFromMonth(2026, 8)
	realized, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	for _, ev := range realized {
		if ev.Description == "Compra cartao KPI" {
			t.Fatal("credit card expense should NOT appear in realized cash")
		}
	}
	_ = projected
}

func TestExtratoReceitaPaidApareceComoEntrada(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('ext-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 120000, ?, 'Freela Agosto', 'paid', ?, 1, 1, ?, ?)
	`, dateUnix, dateUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)
	rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.Description == "Freela Agosto" {
			found = true
			if r.Entrada.Reais != "1.200" || r.Entrada.Cents != ",00" {
				t.Fatalf("entrada = R$ %s%s, want R$ 1.200,00", r.Entrada.Reais, r.Entrada.Cents)
			}
			if r.Saida.Reais != "" {
				t.Fatal("income should have empty Saida")
			}
			if r.AccountName != "Conta Corrente" {
				t.Fatalf("account = %q, want Conta Corrente", r.AccountName)
			}
			if !r.IsRealized {
				t.Fatal("paid income should be IsRealized=true")
			}
		}
	}
	if !found {
		t.Fatal("extrato row for paid income not found")
	}
}

func TestExtratoMantemContatoQuandoHaVinculo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	now := time.Now().Unix()
	dateUnix := testUnixDate("2026-08-10")
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('ct-ext', 'ws-rpt', 'Cliente Extrato', 'client', ?)`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, contact_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('ext-contact', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'ct-ext', 'INCOME', 45000, ?, 'Servico com contato', 'paid', ?, 1, 1, ?, ?)
	`, dateUnix, dateUnix, now, now)

	period := NewReportPeriodFromMonth(2026, 8)
	rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData: %v", err)
	}

	for _, r := range rows {
		if r.Description == "Servico com contato" {
			if r.ContactName != "Cliente Extrato" {
				t.Fatalf("contact = %q, want %q", r.ContactName, "Cliente Extrato")
			}
			return
		}
	}
	t.Fatal("extrato row with contact not found")
}

func TestExtratoDespesaPaidApareceComoSaida(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	dateUnix := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('ext-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 35000, ?, 'Supermercado', 'paid', ?, 1, 1, ?, ?)
	`, dateUnix, dateUnix, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)
	rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData: %v", err)
	}

	var found bool
	for _, r := range rows {
		if r.Description == "Supermercado" {
			found = true
			if r.Saida.Reais != "350" || r.Saida.Cents != ",00" {
				t.Fatalf("saida = R$ %s%s, want R$ 350,00", r.Saida.Reais, r.Saida.Cents)
			}
			if r.Entrada.Reais != "" {
				t.Fatal("expense should have empty Entrada")
			}
			if !r.IsRealized {
				t.Fatal("paid expense should be IsRealized=true")
			}
		}
	}
	if !found {
		t.Fatal("extrato row for paid expense not found")
	}
}

func TestExtratoPagamentoFaturaApareceComoSaida(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-ext-pay', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('pur-ext-pay', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-ext-pay', 'EXPENSE', 40000, ?, 'Compra cartao extrato', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-ext-pay', 'ws-rpt', 'u-rpt', 'chk-rpt', 'EXPENSE', 40000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('invp-ext-pay', 'ws-rpt', 'inv-ext-pay', 'chk-rpt', 'pay-ext-pay', 40000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	period := NewReportPeriodFromMonth(2026, 9)
	rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData: %v", err)
	}

	var foundPayment bool
	for _, r := range rows {
		if r.Description == "Pagamento de Fatura - Cartao Visa" {
			foundPayment = true
			if r.Saida.Reais != "400" || r.Saida.Cents != ",00" {
				t.Fatalf("invoice payment saida = R$ %s%s, want R$ 400,00", r.Saida.Reais, r.Saida.Cents)
			}
			if r.AccountName != "Conta Corrente" {
				t.Fatalf("invoice payment account = %q, want Conta Corrente", r.AccountName)
			}
		}
	}
	if !foundPayment {
		t.Fatal("invoice payment transaction not found in extrato")
	}
}

func TestExtratoCompraCartaoNaoEntraEPagamentoNaoDuplica(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-ext-card', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('pur-ext-card', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-ext-card', 'EXPENSE', 35000, ?, 'Compra cartao extrato', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-ext-card', 'ws-rpt', 'u-rpt', 'chk-rpt', 'EXPENSE', 35000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('invp-ext-card', 'ws-rpt', 'inv-ext-card', 'chk-rpt', 'pay-ext-card', 35000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	augustPeriod := NewReportPeriodFromMonth(2026, 8)
	augRows, err := BuildExtratoData(db, "ws-rpt", augustPeriod)
	if err != nil {
		t.Fatalf("BuildExtratoData august: %v", err)
	}

	for _, r := range augRows {
		if r.Description == "Compra cartao extrato" {
			t.Fatal("credit card purchase should NOT appear in consolidated extrato")
		}
	}

	septPeriod := NewReportPeriodFromMonth(2026, 9)
	septRows, err := BuildExtratoData(db, "ws-rpt", septPeriod)
	if err != nil {
		t.Fatalf("BuildExtratoData sept: %v", err)
	}

	var cardPurchaseCount, invoicePaymentCount int
	for _, r := range septRows {
		if r.Description == "Compra cartao extrato" {
			cardPurchaseCount++
		}
		if r.Description == "Pagamento de Fatura - Cartao Visa" {
			invoicePaymentCount++
		}
	}
	if cardPurchaseCount != 0 {
		t.Fatalf("expected 0 card purchase rows in sept extrato, got %d", cardPurchaseCount)
	}
	if invoicePaymentCount != 1 {
		t.Fatalf("expected 1 invoice payment row in sept extrato, got %d", invoicePaymentCount)
	}
}

func TestExtratoTransferenciaAmbosLados(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	transferDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('ext-transfer', 'ws-rpt', 'u-rpt', 'chk-rpt', 'sav-rpt', 'TRANSFER', 50000, ?, 'Transferencia poupanca', 'paid', ?, 1, 1, ?, ?)
	`, transferDate, transferDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)
	rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData: %v", err)
	}

	var fromChecking, toSavings bool
	for _, r := range rows {
		if r.Description == "Transferencia poupanca" {
			if !r.IsTransfer {
				t.Fatal("transfer row should have IsTransfer=true")
			}
			if r.AccountName == "Conta Corrente" {
				fromChecking = true
				if r.Saida.Reais != "500" || r.Saida.Cents != ",00" {
					t.Fatalf("transfer from checking saida = R$ %s%s, want R$ 500,00", r.Saida.Reais, r.Saida.Cents)
				}
				if r.Entrada.Reais != "" {
					t.Fatal("transfer source should have empty Entrada")
				}
			}
			if r.AccountName == "Poupanca" {
				toSavings = true
				if r.Entrada.Reais != "500" || r.Entrada.Cents != ",00" {
					t.Fatalf("transfer to savings entrada = R$ %s%s, want R$ 500,00", r.Entrada.Reais, r.Entrada.Cents)
				}
				if r.Saida.Reais != "" {
					t.Fatal("transfer destination should have empty Saida")
				}
			}
		}
	}
	if !fromChecking {
		t.Fatal("transfer not found in checking account (source)")
	}
	if !toSavings {
		t.Fatal("transfer not found in savings account (destination)")
	}
}

func TestExtratoWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	seedReportHelpersSecondWorkspace(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ext-ws1', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 50000, ?, 'Despesa WS1', 'paid', ?, 1, 1, ?, ?),
			('ext-ws2', 'ws-rpt2', 'u-rpt2', 'chk-rpt2', 'cat-other', 'EXPENSE', 99999, ?, 'Despesa WS2', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)

	ws1Rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData ws1: %v", err)
	}
	for _, r := range ws1Rows {
		if r.Description == "Despesa WS2" {
			t.Fatal("WS2 expense leaked into WS1 extrato")
		}
	}

	ws2Rows, err := BuildExtratoData(db, "ws-rpt2", period)
	if err != nil {
		t.Fatalf("BuildExtratoData ws2: %v", err)
	}
	for _, r := range ws2Rows {
		if r.Description == "Despesa WS1" {
			t.Fatal("WS1 expense leaked into WS2 extrato")
		}
	}
}

func TestSumCashIncomeExpenseSeparation(t *testing.T) {
	events := []CashEvent{
		{IsIncome: true, AmountCents: 100000, Description: "Freela"},
		{IsIncome: false, AmountCents: 30000, Description: "Mercado"},
		{IsIncome: true, AmountCents: 50000, Description: "Venda"},
		{IsIncome: false, AmountCents: 20000, Description: "Conta"},
		{IsIncome: true, AmountCents: 25000, Description: "Bonus"},
	}

	income := mustSumCashIncome(t, events)
	expense := mustSumCashExpense(t, events)
	net := mustSumCashEvents(t, events)

	if income != 175000 {
		t.Fatalf("SumCashIncome = %d, want 175000", income)
	}
	if expense != 50000 {
		t.Fatalf("SumCashExpense = %d, want 50000", expense)
	}
	if net != 125000 {
		t.Fatalf("SumCashEvents = %d, want 125000", net)
	}
}

func TestRelatoriosCashFlowTabIncomeExpenseSeparation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	augPending := testUnixDate("2026-08-25")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, paid_at, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('cf-inc-paid', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 150000, ?, ?, NULL, 'Salario', 'paid', 1, 1, ?, ?),
			('cf-exp-paid', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 40000, ?, ?, NULL, 'Mercado', 'paid', 1, 1, ?, ?),
			('cf-exp-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 20000, ?, NULL, ?, 'Conta pendente', 'pending', 1, 1, ?, ?),
			('cf-inc-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 80000, ?, NULL, ?, 'Freela pendente', 'pending', 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(),
		augDate, augDate, time.Now().Unix(), time.Now().Unix(),
		augDate, augPending, time.Now().Unix(), time.Now().Unix(),
		augDate, augPending, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)
	realized, projected, err := BuildCashFlowData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	realizedIncome := mustSumCashIncome(t, realized)
	realizedExpense := mustSumCashExpense(t, realized)
	projectedIncome := mustSumCashIncome(t, projected)
	projectedExpense := mustSumCashExpense(t, projected)

	if realizedIncome != 150000 {
		t.Fatalf("realized income = %d, want 150000", realizedIncome)
	}
	if realizedExpense != 40000 {
		t.Fatalf("realized expense = %d, want 40000", realizedExpense)
	}
	if projectedIncome != 80000 {
		t.Fatalf("projected income = %d, want 80000", projectedIncome)
	}
	if projectedExpense != 20000 {
		t.Fatalf("projected expense = %d, want 20000", projectedExpense)
	}
}

func TestRelatoriosCaixaTabSemUpcomingItems(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	due1 := testUnixDate("2026-08-15")
	due2 := testUnixDate("2026-08-20")
	due3 := testUnixDate("2026-08-25")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('up-pend-1', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 15000, ?, ?, 'Internet', 'pending', 1, 1, ?, ?),
			('up-pend-2', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 22000, ?, ?, 'Luz', 'pending', 1, 1, ?, ?),
			('up-pend-3', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 50000, ?, ?, 'Freela', 'pending', 1, 1, ?, ?)
	`, augDate, due1, time.Now().Unix(), time.Now().Unix(),
		augDate, due2, time.Now().Unix(), time.Now().Unix(),
		augDate, due3, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "caixa", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.UpcomingCashItems) != 0 {
		t.Fatalf("Caixa realizado nao deve ter upcoming items: %#v", data.UpcomingCashItems)
	}
}

func TestRelatoriosCompetenciaTabUpcomingItemsComPendentes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-rpt'`)

	augDate := testUnixDate("2026-08-10")
	due1 := testUnixDate("2026-08-15")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, due_date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('up-pend-comp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 15000, ?, ?, 'Internet', 'pending', 1, 1, ?, ?)
	`, augDate, due1, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.UpcomingCashItems) == 0 {
		t.Fatal("expected upcoming items with pending transactions in competencia tab")
	}

	foundInternet := false
	for _, item := range data.UpcomingCashItems {
		if item.Description == "Internet" {
			foundInternet = true
			if item.IsIncome {
				t.Fatal("Internet should be expense, not income")
			}
			if item.Amount.Reais != "150" || item.Amount.Cents != ",00" {
				t.Fatalf("Internet amount = R$ %s%s, want R$ 150,00", item.Amount.Reais, item.Amount.Cents)
			}
		}
	}
	if !foundInternet {
		t.Fatal("Internet not found in upcoming items")
	}
}

func TestRelatoriosCashFlowTabFallbackTabInvalida(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-rpt'`)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}

	tabs := []string{"", "invalid", "caixa", "competencia", "extrato", "CAIXA", "Caixa"}

	for _, tab := range tabs {
		data, err := handler.buildRelatoriosData("", tab, "movimentos", 8, 2026)
		if err != nil {
			t.Fatalf("buildRelatoriosData with tab=%q: %v", tab, err)
		}

		expected := tab
		if expected == "" || expected == "invalid" || expected == "CAIXA" || expected == "Caixa" {
			expected = "caixa"
		}

		if data.ActiveTab != expected {
			t.Fatalf("tab=%q: ActiveTab = %q, want %q", tab, data.ActiveTab, expected)
		}
	}
}

func TestRelatoriosCashFlowTabInvoiceAsProjectedExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-cf-tab', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('cf-tab-card', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-cf-tab', 'EXPENSE', 35000, ?, 'Compra cartao tab', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)

	septPeriod := NewReportPeriodFromMonth(2026, 9)
	_, projected, err := BuildCashFlowData(db, "ws-rpt", septPeriod)
	if err != nil {
		t.Fatalf("BuildCashFlowData: %v", err)
	}

	projectedExpense := mustSumCashExpense(t, projected)
	projectedIncome := mustSumCashIncome(t, projected)

	if projectedExpense < 35000 {
		t.Fatalf("projected expense = %d, want at least 35000 (invoice pending)", projectedExpense)
	}
	if projectedIncome != 0 {
		t.Fatalf("projected income = %d, want 0 (invoice is expense)", projectedIncome)
	}
	foundInvoice := false
	for _, ev := range projected {
		if ev.Source == CashSourceInvoiceProjected {
			foundInvoice = true
			if ev.IsIncome {
				t.Fatal("invoice projected should not be income")
			}
		}
	}
	if !foundInvoice {
		t.Fatal("invoice projected event not found")
	}
}

func TestDailySummaryAgrupaDoisMovimentosMesmoDia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ds-inc-1', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 50000, ?, 'Freela 1', 'paid', ?, 1, 1, ?, ?),
			('ds-inc-2', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 30000, ?, 'Freela 2', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(),
		augDate, augDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.DailySummary) != 1 {
		t.Fatalf("expected 1 day in summary, got %d", len(data.DailySummary))
	}

	ds := data.DailySummary[0]
	if ds.RawIncome != 80000 {
		t.Fatalf("daily income = %d, want 80000 (50000 + 30000)", ds.RawIncome)
	}
	if ds.RawExpense != 0 {
		t.Fatalf("daily expense = %d, want 0", ds.RawExpense)
	}
	net := ds.RawIncome - ds.RawExpense
	if net != 80000 {
		t.Fatalf("daily net = %d, want 80000", net)
	}
}

func TestDailySummarySeparaEntradaESaida(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	sameDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ds-mix-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 100000, ?, 'Venda', 'paid', ?, 1, 1, ?, ?),
			('ds-mix-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 40000, ?, 'Custo', 'paid', ?, 1, 1, ?, ?)
	`, sameDate, sameDate, time.Now().Unix(), time.Now().Unix(),
		sameDate, sameDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.DailySummary) != 1 {
		t.Fatalf("expected 1 day, got %d", len(data.DailySummary))
	}

	ds := data.DailySummary[0]
	if ds.RawIncome != 100000 {
		t.Fatalf("income = %d, want 100000", ds.RawIncome)
	}
	if ds.RawExpense != 40000 {
		t.Fatalf("expense = %d, want 40000", ds.RawExpense)
	}
}

func TestDailySummaryAcumuladoProgressivo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	day1 := testUnixDate("2026-08-05")
	day2 := testUnixDate("2026-08-10")

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ds-prog-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 200000, ?, 'Salario dia 5', 'paid', ?, 1, 1, ?, ?),
			('ds-prog-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 60000, ?, 'Compra dia 10', 'paid', ?, 1, 1, ?, ?)
	`, day1, day1, time.Now().Unix(), time.Now().Unix(),
		day2, day2, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.DailySummary) != 2 {
		t.Fatalf("expected 2 days, got %d", len(data.DailySummary))
	}

	if data.DailySummary[0].RawIncome != 200000 {
		t.Fatalf("day1 income = %d, want 200000", data.DailySummary[0].RawIncome)
	}

	if data.DailySummary[1].RawExpense != 60000 {
		t.Fatalf("day2 expense = %d, want 60000", data.DailySummary[1].RawExpense)
	}

	day1Net := data.DailySummary[0].RawIncome - data.DailySummary[0].RawExpense
	day2Net := data.DailySummary[1].RawIncome - data.DailySummary[1].RawExpense
	expectedAccumulated := day1Net + day2Net

	actualAccumulated := data.DailySummary[1].RawIncome - data.DailySummary[1].RawExpense
	t.Logf("day1 net=%d day2 net=%d accumulated=%d", day1Net, day2Net, day1Net+day2Net)

	if day1Net != 200000 {
		t.Fatalf("day1 net = %d, want 200000", day1Net)
	}
	if day2Net != -60000 {
		t.Fatalf("day2 net = %d, want -60000", day2Net)
	}
	if expectedAccumulated != 140000 {
		t.Fatalf("accumulated = %d, want 140000", expectedAccumulated)
	}
	_ = actualAccumulated
}

func TestDailySummaryPagamentoFaturaComoSaida(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	seedReportHelpersScenario(t, db)

	purchaseDate := testUnixDate("2026-08-05")
	invoiceClos := testUnixDate("2026-08-20")
	invoiceDue := testUnixDate("2026-09-10")
	paymentDate := testUnixDate("2026-09-01")

	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-ds', 'card-rpt', '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceClos, invoiceDue, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('pur-ds', 'ws-rpt', 'u-rpt', 'card-rpt', 'cat-card-food', 'inv-ds', 'EXPENSE', 35000, ?, 'Compra cartao daily', 'paid', 1, 1, ?, ?)
	`, purchaseDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('pay-ds', 'ws-rpt', 'u-rpt', 'chk-rpt', 'EXPENSE', 35000, ?, 'Pagamento de Fatura - Cartao Visa', 'paid', ?, 1, 1, ?, ?)
	`, paymentDate, paymentDate, now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('invp-ds', 'ws-rpt', 'inv-ds', 'chk-rpt', 'pay-ds', 35000, ?, 'manual', 'u-rpt', ?)
	`, paymentDate, now)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 9, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData sept: %v", err)
	}

	foundPayment := false
	for _, ds := range data.DailySummary {
		if ds.RawExpense >= 35000 {
			foundPayment = true
		}
	}
	if !foundPayment {
		t.Fatal("invoice payment not found as expense in daily summary")
	}
}

func TestDailySummaryTransferenciaNaoDistorceConsolidado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	transferDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('ds-transfer', 'ws-rpt', 'u-rpt', 'chk-rpt', 'sav-rpt', 'TRANSFER', 50000, ?, 'Transfer diaria', 'paid', ?, 1, 1, ?, ?)
	`, transferDate, transferDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.DailySummary) != 0 {
		t.Fatalf("expected 0 daily summary rows when only transfer exists, got %d", len(data.DailySummary))
	}
	if data.ExtratoMovementCount != 2 {
		t.Fatalf("ExtratoMovementCount = %d, want 2 (both sides of transfer)", data.ExtratoMovementCount)
	}
}

func TestDailySummaryWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)
	seedReportHelpersSecondWorkspace(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ds-ws1', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 90000, ?, 'Receita WS1', 'paid', ?, 1, 1, ?, ?),
			('ds-ws2', 'ws-rpt2', 'u-rpt2', 'chk-rpt2', 'cat-other', 'EXPENSE', 99999, ?, 'Despesa WS2', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(),
		augDate, augDate, time.Now().Unix(), time.Now().Unix())

	handler1 := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	handler2 := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt2", UserID: "u-rpt2"}

	data1, _ := handler1.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	data2, _ := handler2.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)

	if len(data1.DailySummary) != 1 {
		t.Fatalf("ws1 daily summary days = %d, want 1", len(data1.DailySummary))
	}
	if data1.DailySummary[0].RawIncome != 90000 {
		t.Fatalf("ws1 income = %d, want 90000", data1.DailySummary[0].RawIncome)
	}

	if len(data2.DailySummary) != 1 {
		t.Fatalf("ws2 daily summary days = %d, want 1", len(data2.DailySummary))
	}
	if data2.DailySummary[0].RawExpense != 99999 {
		t.Fatalf("ws2 expense = %d, want 99999", data2.DailySummary[0].RawExpense)
	}
}

func TestDailySummaryTemplateRendersWithInt64Values(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('tpl-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 150000, ?, 'Template income', 'paid', ?, 1, 1, ?, ?),
			('tpl-exp', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, 'Template expense', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(),
		augDate, augDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.DailySummary) == 0 {
		t.Fatal("expected DailySummary rows")
	}

	ds := data.DailySummary[0]
	if !ds.HasIncome {
		t.Fatal("HasIncome should be true when RawIncome > 0")
	}
	if !ds.HasExpense {
		t.Fatal("HasExpense should be true when RawExpense > 0")
	}
	if !ds.NetPositive {
		t.Fatal("NetPositive should be true when income > expense")
	}
	if ds.NetNegative {
		t.Fatal("NetNegative should be false when income > expense")
	}
	if ds.NetClass != "text-[var(--cb-success)]" {
		t.Fatalf("NetClass = %q, want text-[var(--cb-success)]", ds.NetClass)
	}
	if ds.AccumulatedClass != "" {
		t.Fatalf("AccumulatedClass = %q, want empty for positive accumulated", ds.AccumulatedClass)
	}
}

func TestExtratoNaoIncluiPendente(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ext-paid', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, 'Mercado pago', 'paid', ?, 1, 1, ?, ?),
			('ext-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 20000, ?, 'Mercado pendente', 'pending', NULL, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, time.Now().Unix(), time.Now().Unix())

	period := NewReportPeriodFromMonth(2026, 8)
	rows, err := BuildExtratoData(db, "ws-rpt", period)
	if err != nil {
		t.Fatalf("BuildExtratoData: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("expected 1 extrato row, got %d", len(rows))
	}
	if rows[0].Description != "Mercado pago" {
		t.Fatalf("expected only paid expense, got %q", rows[0].Description)
	}
}

func TestExtratoSubvisaoPadraoEMovimentos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES ('ext-view', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, 'Mercado', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}

	cases := []struct {
		name     string
		view     string
		expected string
	}{
		{"default falls to movimentos", "", "movimentos"},
		{"explicit movimentos", "movimentos", "movimentos"},
		{"uppercase normalized", "MOVIMENTOS", "movimentos"},
		{"diario", "diario", "diario"},
		{"invalid falls to movimentos", "invalid", "movimentos"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := handler.buildRelatoriosData("", "extrato", tc.view, 8, 2026)
			if err != nil {
				t.Fatalf("buildRelatoriosData: %v", err)
			}
			if data.ExtratoView != tc.expected {
				t.Fatalf("ExtratoView = %q, want %q", data.ExtratoView, tc.expected)
			}
			if len(data.ExtratoRows) != 1 {
				t.Fatalf("expected 1 extrato row, got %d", len(data.ExtratoRows))
			}
			if data.ExtratoMovementCount != 1 {
				t.Fatalf("ExtratoMovementCount = %d, want 1", data.ExtratoMovementCount)
			}
		})
	}
}

func TestExtratoResumoDiarioApenasConfirmados(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ext-rs-paid', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, 'Mercado pago', 'paid', ?, 1, 1, ?, ?),
			('ext-rs-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 20000, ?, 'Mercado pendente', 'pending', NULL, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "diario", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if len(data.DailySummary) != 1 {
		t.Fatalf("expected 1 daily summary row, got %d", len(data.DailySummary))
	}
	if data.DailySummary[0].RawExpense != 30000 {
		t.Fatalf("daily expense = %d, want 30000", data.DailySummary[0].RawExpense)
	}
}

func TestExtratoTotalMovimentosContaApenasConfirmados(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedReportHelpersScenario(t, db)

	augDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES
			('ext-tm-paid', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 30000, ?, 'Mercado pago', 'paid', ?, 1, 1, ?, ?),
			('ext-tm-pending', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-food', 'EXPENSE', 20000, ?, 'Mercado pendente', 'pending', NULL, 1, 1, ?, ?),
			('ext-tm-inc', 'ws-rpt', 'u-rpt', 'chk-rpt', 'cat-salary', 'INCOME', 100000, ?, 'Salario', 'paid', ?, 1, 1, ?, ?)
	`, augDate, augDate, time.Now().Unix(), time.Now().Unix(), augDate, time.Now().Unix(), time.Now().Unix(), augDate, augDate, time.Now().Unix(), time.Now().Unix())

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-rpt", UserID: "u-rpt"}
	data, err := handler.buildRelatoriosData("", "extrato", "movimentos", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if data.ExtratoMovementCount != 2 {
		t.Fatalf("ExtratoMovementCount = %d, want 2", data.ExtratoMovementCount)
	}
}
