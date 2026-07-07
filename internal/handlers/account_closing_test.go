package handlers

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func seedClosingScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('u-close', 'Close', 'close@t.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('ws-close', 'WS Close', 'business', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-close', 'u-close', 'ADMIN', ?)
	`, now)

	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-checking', 'ws-close', 'Conta Corrente', 'CHECKING', 500000, 550000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-savings', 'ws-close', 'Poupanca', 'SAVINGS', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-investment', 'ws-close', 'Investimentos', 'INVESTMENT', 200000, 200000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-wallet', 'ws-close', 'Carteira', 'WALLET', 50000, 50000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-credit', 'ws-close', 'Cartao de Credito', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
}

func TestBuildAccountClosingBalances_IncomeBeforeClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Income paid before periodEnd (July 15) should remain in balance.
	paidAt := testUnixDate("2026-07-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-inc-before', 'ws-close', 'u-close', 'acc-checking', 'INCOME', 50000, ?, 'Receita Julho', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	// current_balance was 550000 (from seed), now should be 600000.
	// Update to reflect the income.
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 600000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	if checkingBalance != 600000 {
		t.Fatalf("income before closing should remain: want 600000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_IncomeAfterClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Income paid after periodEnd (August 5). Should be reversed.
	paidAt := testUnixDate("2026-08-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-inc-after', 'ws-close', 'u-close', 'acc-checking', 'INCOME', 50000, ?, 'Receita Agosto', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 600000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// Seed balance was 550000. Income after periodEnd should be reversed -> 550000.
	if checkingBalance != 550000 {
		t.Fatalf("income after closing should be reversed: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_ExpenseBeforeClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	paidAt := testUnixDate("2026-07-20")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-exp-before', 'ws-close', 'u-close', 'acc-checking', 'EXPENSE', 30000, ?, 'Despesa Julho', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 520000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	if checkingBalance != 520000 {
		t.Fatalf("expense before closing should remain: want 520000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_ExpenseAfterClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	paidAt := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-exp-after', 'ws-close', 'u-close', 'acc-checking', 'EXPENSE', 30000, ?, 'Despesa Agosto', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 520000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// Seed 550000. Expense after periodEnd reduced to 520000.
	// Reversal: 520000 + 30000 = 550000.
	if checkingBalance != 550000 {
		t.Fatalf("expense after closing should be reversed: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_PendingDoesNotAffect(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Pending expense (not paid). Should not affect reconstruction.
	competenceDate := testUnixDate("2026-08-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pending', 'ws-close', 'u-close', 'acc-checking', 'EXPENSE', 999999, ?, 'Pendente', 'pending', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// Pending transaction should not change the reconstructed balance.
	if checkingBalance != 550000 {
		t.Fatalf("pending should not affect balance: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_TransferAfterClosing(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	paidAt := testUnixDate("2026-08-01")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-xfer', 'ws-close', 'u-close', 'acc-checking', 'acc-savings', 'TRANSFER', 20000, ?, 'Transferencia', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	// Simulate ApplyBalanceEffect: checking -20000, savings +20000.
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 530000 WHERE id = 'acc-checking'`)
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 120000 WHERE id = 'acc-savings'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance, savingsBalance int64
	for _, b := range balances {
		switch b.ID {
		case "acc-checking":
			checkingBalance = b.Balance
		case "acc-savings":
			savingsBalance = b.Balance
		}
	}

	// Source: add 20000 back -> 550000 (seed).
	if checkingBalance != 550000 {
		t.Fatalf("transfer source should reverse: want 550000, got %d", checkingBalance)
	}
	// Destination: subtract 20000 -> 100000 (seed).
	if savingsBalance != 100000 {
		t.Fatalf("transfer destination should reverse: want 100000, got %d", savingsBalance)
	}
}

func TestBuildAccountClosingBalances_InvoicePaymentNoDoubleCount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Invoice payment creates an EXPENSE on the checking account.
	// The invoice_payments table links it, but we only care about the EXPENSE.
	paidAt := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-inv-pay', 'ws-close', 'u-close', 'acc-checking', 'EXPENSE', 45000, ?, 'Pagamento de fatura', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	// Also add invoice_payments record (should NOT cause double-counting).
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-close', 'acc-credit', 20, 10, 500000)
	`)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-close', 'acc-credit', '2026-08', ?, ?, 'OPEN', ?)
	`, paidAt, paidAt, paidAt)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at)
		VALUES ('ip-close', 'ws-close', 'inv-close', 'acc-checking', 'tx-inv-pay', 45000, ?, 'manual', ?)
	`, paidAt, paidAt)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 505000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// Seed 550000. Expense after periodEnd dropped to 505000.
	// Reversal: 505000 + 45000 = 550000. No double-counting with invoice_payments.
	if checkingBalance != 550000 {
		t.Fatalf("invoice payment should not double-count: want %d, got %d", 550000, checkingBalance)
	}
}

func TestBuildAccountClosingBalances_CreditCardPurchaseNoBankEffect(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Purchase on credit card. Should NOT affect any real account balance.
	paidAt := testUnixDate("2026-07-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-cc-purchase', 'ws-close', 'u-close', 'acc-credit', 'EXPENSE', 75000, ?, 'Compra Cartao', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// Credit card purchase should not affect checking balance.
	if checkingBalance != 550000 {
		t.Fatalf("credit card purchase should not affect bank balance: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_CreditCardNotInResults(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	for _, b := range balances {
		if b.Type == "CREDIT_CARD" {
			t.Fatalf("CREDIT_CARD should not appear in real account balances")
		}
		if b.ID == "acc-credit" {
			t.Fatalf("credit card account should not be in results")
		}
	}
}

func TestBuildAccountClosingBalances_InvestmentAsRealAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Income on investment account after periodEnd. Should be reversed.
	paidAt := testUnixDate("2026-08-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-inv-after', 'ws-close', 'u-close', 'acc-investment', 'INCOME', 30000, ?, 'Rendimento Agosto', 'paid', ?, ?, ?)
	`, paidAt, paidAt, paidAt, paidAt)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 230000 WHERE id = 'acc-investment'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var invBalance int64
	for _, b := range balances {
		if b.ID == "acc-investment" {
			invBalance = b.Balance
		}
	}
	// Seed 200000. Income after periodEnd reversed -> 200000.
	if invBalance != 200000 {
		t.Fatalf("investment income after closing should be reversed: want 200000, got %d", invBalance)
	}
}

func TestBuildAccountClosingBalances_WorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	now := time.Now().Unix()
	// Create another workspace with its own accounts.
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('ws-other', 'Other WS', 'business', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-other', 'ws-other', 'Outra Conta', 'CHECKING', 999999, 999999, ?, ?)
	`, now, now)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	for _, b := range balances {
		if b.ID == "acc-other" {
			t.Fatalf("workspace isolation broken: other workspace account appeared")
		}
	}

	// Verify ws-other isolation separately.
	otherBalances, err := BuildAccountClosingBalances(db, "ws-other", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances ws-other: %v", err)
	}
	if len(otherBalances) != 1 || otherBalances[0].ID != "acc-other" {
		t.Fatalf("ws-other should have exactly its own account")
	}
}

func TestBuildAccountClosingBalances_ArchivedAccountAppears(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	now := time.Now().Unix()
	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Add an archived account with non-zero balance.
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, archived_at, created_at, updated_at)
		VALUES ('acc-archived', 'ws-close', 'Conta Arquivada', 'CHECKING', 50000, 50000, ?, ?, ?)
	`, now, now, now)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var foundArchived bool
	for _, b := range balances {
		if b.ID == "acc-archived" {
			foundArchived = true
			if !b.IsArchived {
				t.Fatalf("archived account should have IsArchived = true")
			}
		}
	}
	if !foundArchived {
		t.Fatalf("archived account with non-zero balance should appear")
	}
}

func TestBuildAccountClosingBalances_ArchivedAccountWithZeroBalanceExcluded(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	now := time.Now().Unix()
	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Add an archived account with zero balance and no post-period movements.
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, archived_at, created_at, updated_at)
		VALUES ('acc-arch-zero', 'ws-close', 'Arquivada Zero', 'SAVINGS', 0, 0, ?, ?, ?)
	`, now, now, now)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	for _, b := range balances {
		if b.ID == "acc-arch-zero" {
			t.Fatalf("archived account with zero reconstructed balance and no movements should be excluded")
		}
	}
}

func TestBuildAccountClosingBalances_ValidatesAllRealTypes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	wantTypes := map[string]bool{
		"CHECKING":   true,
		"SAVINGS":    true,
		"INVESTMENT": true,
		"WALLET":     true,
	}
	for _, b := range balances {
		if !wantTypes[b.Type] {
			t.Fatalf("unexpected account type %q in results", b.Type)
		}
		delete(wantTypes, b.Type)
	}
	if len(wantTypes) > 0 {
		t.Fatalf("missing expected account types: %v", wantTypes)
	}

	// Verify CREDIT_CARD is absent.
	for _, b := range balances {
		if b.Type == "CREDIT_CARD" {
			t.Fatalf("CREDIT_CARD should not appear")
		}
	}
}

func TestBuildAccountClosingBalances_EmptyWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	balances, err := BuildAccountClosingBalances(db, "nonexistent", 0)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances empty: %v", err)
	}
	if len(balances) != 0 {
		t.Fatalf("empty workspace should return empty slice")
	}
}

func TestBuildAccountClosingBalances_EffectiveDateUsesPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Transaction with competence date (date) in July but paid_at in August.
	// Effective date is COALESCE(NULLIF(paid_at, 0), date) -> paid_at = August.
	// After periodEnd: should be reversed.
	competenceDate := testUnixDate("2026-07-10")
	paidAt := testUnixDate("2026-08-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-eff-pd', 'ws-close', 'u-close', 'acc-checking', 'INCOME', 40000, ?, 'Competencia Julho pago Agosto', 'paid', ?, ?, ?)
	`, competenceDate, paidAt, paidAt, paidAt)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 590000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// Income paid in August. Should be reversed from current: 590000 - 40000 = 550000 (seed).
	if checkingBalance != 550000 {
		t.Fatalf("effective date should use paid_at: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_UITextDoesNotClaimAccounting(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	body := string(raw)

	if strings.Contains(body, "Saldo atual para conferência") {
		t.Fatalf("template should not contain old 'Saldo atual para conferência' text")
	}
	if !strings.Contains(body, "Saldo por conta em") {
		t.Fatalf("template should contain 'Saldo por conta em' with date reference")
	}
	if !strings.Contains(body, "reconstruído") {
		t.Fatalf("template should contain 'reconstruído' badge")
	}
	if !strings.Contains(body, "Estimado a partir do saldo atual") {
		t.Fatalf("template should contain the new estimated copy text")
	}
	if !strings.Contains(body, "Não substitui extrato bancário, conciliação ou fechamento contábil auditável") {
		t.Fatalf("template should contain disclaimer about not replacing bank statement or auditable close")
	}
}

func TestBuildAccountClosingBalances_EffectiveDateFallbackNullPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Transaction with paid_at = NULL. Effective date = date (competence).
	// date = August (after periodEnd). Should be reversed.
	competenceDate := testUnixDate("2026-08-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-pd-null', 'ws-close', 'u-close', 'acc-checking', 'INCOME', 40000, ?, 'Competencia Agosto paid_at NULL', 'paid', NULL, ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 590000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// paid_at = NULL, effective = date = August (after periodEnd). Reversed -> 550000.
	if checkingBalance != 550000 {
		t.Fatalf("NULL paid_at should fall back to date: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_EffectiveDateFallbackZeroPaidAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Transaction with paid_at = 0. Effective date should fall back to date (competence).
	// date = August (after periodEnd). Should be reversed.
	competenceDate := testUnixDate("2026-08-05")
	paidAtZero := int64(0)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-pd-zero', 'ws-close', 'u-close', 'acc-checking', 'INCOME', 40000, ?, 'Competencia Agosto paid_at 0', 'paid', ?, ?, ?)
	`, competenceDate, paidAtZero, competenceDate, competenceDate)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 590000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// paid_at = 0, NULLIF makes it NULL, COALESCE falls back to date = August (after). Reversed -> 550000.
	if checkingBalance != 550000 {
		t.Fatalf("zero paid_at should fall back to date via NULLIF: want 550000, got %d", checkingBalance)
	}
}

func TestBuildAccountClosingBalances_EffectiveDatePaidAtBeforeClosingUsesDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedClosingScenario(t, db)

	periodEnd := NewReportPeriodFromMonth(2026, 7).End

	// Transaction paid in July (before periodEnd) but with NULL paid_at.
	// Effective date = COALESCE(NULL, date) = date = July (before periodEnd).
	// Should NOT be reversed.
	competenceDate := testUnixDate("2026-07-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at)
		VALUES ('tx-pd-before-null', 'ws-close', 'u-close', 'acc-checking', 'INCOME', 30000, ?, 'Competencia Julho paid_at NULL', 'paid', NULL, ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	execTestSQL(t, db, `UPDATE accounts SET current_balance = 580000 WHERE id = 'acc-checking'`)

	balances, err := BuildAccountClosingBalances(db, "ws-close", periodEnd)
	if err != nil {
		t.Fatalf("BuildAccountClosingBalances: %v", err)
	}

	var checkingBalance int64
	for _, b := range balances {
		if b.ID == "acc-checking" {
			checkingBalance = b.Balance
		}
	}
	// paid_at = NULL, effective = date = July (before). Should remain 580000.
	if checkingBalance != 580000 {
		t.Fatalf("NULL paid_at with date before closing should not be reversed: want 580000, got %d", checkingBalance)
	}
}
