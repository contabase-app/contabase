package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func seedPoliticaHibridaScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	augExpPaid := testUnixDate("2026-08-05")
	augExpPending := testUnixDate("2026-08-12")
	augIncPaid := testUnixDate("2026-08-08")
	augIncPending := testUnixDate("2026-08-15")
	augTransfer := testUnixDate("2026-08-10")

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-hib', 'User Hibrido', 'u-hib@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-hib', 'WS Hibrido', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-hib', 'u-hib', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('chk-hib', 'ws-hib', 'Conta', 'CHECKING', 500000, 500000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('poup-hib', 'ws-hib', 'Poupanca', 'SAVINGS', 200000, 200000, ?, ?)`, now, now)

	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-alim', 'ws-hib', 'Alimentacao', 'utensils', '#f97316', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-lazer', 'ws-hib', 'Lazer', 'gamepad', '#8b5cf6', 'EXPENSE', 'Estilo de Vida', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-serv', 'ws-hib', 'Servico', 'briefcase', '#22c55e', 'INCOME', NULL, ?)`, now)

	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES
		('t-exp-paid',    'ws-hib', 'u-hib', 'chk-hib',  'cat-alim', 'EXPENSE',  50000, ?, 'Mercado pago',       'paid',    ?, ?),
		('t-exp-pending', 'ws-hib', 'u-hib', 'chk-hib',  'cat-alim', 'EXPENSE',  30000, ?, 'Mercado pendente',    'pending', ?, ?),
		('t-exp-lazer',   'ws-hib', 'u-hib', 'chk-hib',  'cat-lazer','EXPENSE',  20000, ?, 'Cinema pago',         'paid',    ?, ?),
		('t-inc-paid',    'ws-hib', 'u-hib', 'chk-hib',  'cat-serv', 'INCOME',  200000, ?, 'Freela pago',         'paid',    ?, ?),
		('t-inc-pending', 'ws-hib', 'u-hib', 'chk-hib',  'cat-serv', 'INCOME',  100000, ?, 'Freela pendente',     'pending', ?, ?),
		('t-transfer',    'ws-hib', 'u-hib', 'chk-hib',  NULL,       'TRANSFER', 50000, ?, 'Transferencia',       'paid',    ?, ?)
	`, augExpPaid, now, now, augExpPending, now, now, augExpPaid, now, now, augIncPaid, now, now, augIncPending, now, now, augTransfer, now, now)
}

func TestRelatoriosPoliticaHibridaKPIsIncludesPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total receitas (inclui pending)", data.TotalReceitas, "3.000", ",00")
	assertMoneyDisplay(t, "total despesas (inclui pending)", data.TotalDespesas, "1.000", ",00")
	assertMoneyDisplay(t, "saldo liquido (inclui pending)", data.SaldoLiquido, "2.000", ",00")
}

func TestRelatoriosPoliticaHibridaCategoriesIncludesPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	var foundAlimentacao bool
	for _, cat := range data.Categorias {
		if cat.Nome == "Alimentacao" {
			foundAlimentacao = true
			assertMoneyDisplay(t, "Alimentacao (paid + pending)", cat.Valor, "800", ",00")
			break
		}
	}
	if !foundAlimentacao {
		t.Fatalf("Alimentacao not found in categories: %#v", data.Categorias)
	}

	if len(data.Categorias) < 2 {
		t.Fatalf("expected at least 2 categories, got %d", len(data.Categorias))
	}
}

func TestRelatoriosPoliticaHibridaDonutIncludesPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "DonutEssentialTotal (paid + pending)", data.DonutEssentialTotal, "800", ",00")
	assertMoneyDisplay(t, "DonutLifestyleTotal (paid only)", data.DonutLifestyleTotal, "200", ",00")

	if data.DonutEssentialPercent != 80 {
		t.Fatalf("DonutEssentialPercent = %d, want 80", data.DonutEssentialPercent)
	}
	if data.DonutLifestylePercent != 20 {
		t.Fatalf("DonutLifestylePercent = %d, want 20", data.DonutLifestylePercent)
	}
}

func TestRelatoriosPoliticaHibridaDREIncludesPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}

	raw, err := handler.queryDRERaw(2026)
	if err != nil {
		t.Fatalf("queryDRERaw: %v", err)
	}

	var total int64
	for _, row := range raw {
		total += row.Amount
	}

	if total != 200000 {
		t.Fatalf("DRE total = %d, want 200000 (income 300k - expense 100k, includes pending)", total)
	}

	var foundEssential bool
	for _, row := range raw {
		if row.Competencia == "2026-08" && row.MacroGroup == "Essencial" {
			foundEssential = true
			if row.Amount != -80000 {
				t.Fatalf("Essencial amount = %d, want -80000 (paid + pending expense)", row.Amount)
			}
		}
	}
	if !foundEssential {
		t.Fatal("DRE should include Essencial macro group for 2026-08")
	}
}

func TestCanonicalMacroGroupMapsLegacyValues(t *testing.T) {
	tests := map[string]string{
		"ESSENTIAL":           "Essencial",
		"LIFESTYLE":           "Estilo de Vida",
		"OPERATING_COSTS":     "Custos Operacionais",
		"OPERATING_REVENUE":   "Receitas Operacionais",
		"OPERATIONAL_REVENUE": "Receitas Operacionais",
	}
	for input, want := range tests {
		if got := canonicalMacroGroup(input); got != want {
			t.Fatalf("canonicalMacroGroup(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRelatoriosPoliticaHibridaRevenueExcludesPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}

	monthStart := testUnixDate("2026-08-01")
	monthEnd := lastSecondOfMonth(2026, 8)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	slices, err := handler.queryRevenueByCategory(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryRevenueByCategory: %v", err)
	}

	if len(slices) == 0 {
		t.Fatal("expected at least one revenue slice (paid income)")
	}

	assertMoneyDisplay(t, "revenue total (only paid)", slices[0].Amount, "2.000", ",00")

}

func TestRelatoriosPoliticaHibridaCashflowExcludesPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}

	monthStart := testUnixDate("2026-08-01")
	monthEnd := lastSecondOfMonth(2026, 8)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	netCash, err := handler.queryNetCashflow(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryNetCashflow: %v", err)
	}

	expectedNet := int64(200000 - 50000 - 20000)
	if netCash != expectedNet {
		t.Fatalf("net cashflow = %d, want %d (paid income 200000 - paid expenses 70000, excludes pending and transfer)", netCash, expectedNet)
	}
}

func TestRelatoriosPoliticaHibridaTransfersExcludedFromKPIs(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedPoliticaHibridaScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hib", UserID: "u-hib"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "TotalReceitas (transfer not income)", data.TotalReceitas, "3.000", ",00")
	assertMoneyDisplay(t, "TotalDespesas (transfer not expense)", data.TotalDespesas, "1.000", ",00")
}

func TestRelatoriosPoliticaHibridaInvoicePaymentExcludedFromCompetence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	purchaseDate := testUnixDate("2026-08-05")
	paymentDate := testUnixDate("2026-08-10")
	paymentTime := time.Unix(paymentDate, 0).UTC()
	reference := paymentTime.Format("2006-01")
	closingUnix := time.Date(paymentTime.Year(), paymentTime.Month()-1, 20, 12, 0, 0, 0, time.UTC).Unix()
	dueUnix := time.Date(paymentTime.Year(), paymentTime.Month(), 10, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-card', 'Card User', 'u-card@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-card', 'WS Card', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-card', 'u-card', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('chk-card', 'ws-card', 'Conta', 'CHECKING', 200000, 200000, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-card', 'ws-card', 'Cartao', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-card', 'card-card', 20, 10, 600000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-card', 'ws-card', 'Compras Cartao', 'shopping-bag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)`, now)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at) VALUES ('inv-card', 'card-card', ?, ?, ?, 'PAID', ?, 30000, ?)`, reference, closingUnix, dueUnix, paymentDate, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, created_at, updated_at) VALUES ('pur-card', 'ws-card', 'u-card', 'card-card', 'cat-card', 'inv-card', 'EXPENSE', 30000, ?, 'Compra cartao', 'paid', ?, ?)`, purchaseDate, now, now)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, created_at, updated_at) VALUES ('pay-card', 'ws-card', 'u-card', 'chk-card', NULL, NULL, 'EXPENSE', 30000, ?, ?, 'paid', ?, ?)`, paymentDate, invoicePaymentDescription("Cartao"), now, now)
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at) VALUES ('pay-card-row', 'ws-card', 'inv-card', 'chk-card', 'pay-card', 30000, ?, 'manual', 'u-card', ?)`, paymentDate, now)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-card", UserID: "u-card"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas (competencia)", data.TotalDespesas, "300", ",00")
	if len(data.Categorias) != 1 || data.Categorias[0].Nome != "Compras Cartao" {
		t.Fatalf("categorias = %#v, want only Compras Cartao (payment excluded)", data.Categorias)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	netCash, err := handler.queryNetCashflow(ctx, testUnixDate("2026-08-01"), lastSecondOfMonth(2026, 8))
	if err != nil {
		t.Fatalf("queryNetCashflow: %v", err)
	}
	if netCash != -60000 {
		t.Fatalf("net cashflow = %d, want -60000 (purchase 30000 + payment 30000 both appear as cash movement)", netCash)
	}
}

func lastSecondOfMonth(year, month int) int64 {
	nextMonth := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)
	return nextMonth.Add(-1 * time.Second).Unix()
}
