package database

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigration57WorkspaceMonthlyPlanningReferencesFreshSchema(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 57)
	for _, column := range []string{
		"monthly_income_reference",
		"monthly_total_cost_reference",
		"monthly_essential_cost_reference",
	} {
		assertColumnExists(t, db, "workspaces", column)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (
		id, name, monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference
	) VALUES ('ws-valid-planning', 'Valid', 0, 100, NULL)`); err != nil {
		t.Fatalf("insert valid references: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, monthly_income_reference) VALUES ('ws-negative-planning', 'Invalid', -1)`); err == nil {
		t.Fatal("negative monthly reference should be rejected")
	}
}

func TestMigration57UpgradePreservesFinancialDataAndLeavesReferencesNull(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration57.db")
	db, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+dbPath))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	for _, stmt := range []string{
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL DEFAULT 'personal')`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, current_balance INTEGER NOT NULL)`,
		`CREATE TABLE transactions (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, amount INTEGER NOT NULL)`,
		`CREATE TABLE boxes (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, target_amount INTEGER NOT NULL, monthly_recharge INTEGER NOT NULL)`,
		`CREATE TABLE box_virtual_ledger (id TEXT PRIMARY KEY, box_id TEXT NOT NULL, amount INTEGER NOT NULL)`,
		`CREATE TABLE invoices (id TEXT PRIMARY KEY, account_id TEXT NOT NULL, total_amount INTEGER NOT NULL)`,
		`CREATE TABLE invoice_payments (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, invoice_id TEXT NOT NULL, amount_cents INTEGER NOT NULL)`,
		`INSERT INTO workspaces (id, name) VALUES ('ws-m57', 'Fixture')`,
		`INSERT INTO accounts VALUES ('acc-m57', 'ws-m57', 12345)`,
		`INSERT INTO transactions VALUES ('tx-m57', 'ws-m57', 2345)`,
		`INSERT INTO boxes VALUES ('box-m57', 'ws-m57', 10000, 1000)`,
		`INSERT INTO box_virtual_ledger VALUES ('ledger-m57', 'box-m57', 4500)`,
		`INSERT INTO invoices VALUES ('invoice-m57', 'acc-m57', 6789)`,
		`INSERT INTO invoice_payments VALUES ('payment-m57', 'ws-m57', 'invoice-m57', 3456)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	seedAppliedMigrationsBefore(t, db, 57)

	if err := runMigrationsThroughVersionForTest(db, 57); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	assertMigrationApplied(t, db, 57)
	var accountBalance, transactionAmount, target, recharge, ledgerAmount, invoiceTotal, paymentAmount int64
	var income, totalCost, essentialCost sql.NullInt64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'acc-m57'`).Scan(&accountBalance); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if err := db.QueryRow(`SELECT amount FROM transactions WHERE id = 'tx-m57'`).Scan(&transactionAmount); err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	if err := db.QueryRow(`SELECT target_amount, monthly_recharge FROM boxes WHERE id = 'box-m57'`).Scan(&target, &recharge); err != nil {
		t.Fatalf("read box: %v", err)
	}
	if err := db.QueryRow(`SELECT amount FROM box_virtual_ledger WHERE id = 'ledger-m57'`).Scan(&ledgerAmount); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if err := db.QueryRow(`SELECT total_amount FROM invoices WHERE id = 'invoice-m57'`).Scan(&invoiceTotal); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	if err := db.QueryRow(`SELECT amount_cents FROM invoice_payments WHERE id = 'payment-m57'`).Scan(&paymentAmount); err != nil {
		t.Fatalf("read invoice payment: %v", err)
	}
	if err := db.QueryRow(`SELECT monthly_income_reference, monthly_total_cost_reference, monthly_essential_cost_reference FROM workspaces WHERE id = 'ws-m57'`).Scan(&income, &totalCost, &essentialCost); err != nil {
		t.Fatalf("read references: %v", err)
	}
	if accountBalance != 12345 || transactionAmount != 2345 || target != 10000 || recharge != 1000 || ledgerAmount != 4500 || invoiceTotal != 6789 || paymentAmount != 3456 {
		t.Fatalf("financial data changed: account=%d transaction=%d target=%d recharge=%d ledger=%d invoice=%d payment=%d", accountBalance, transactionAmount, target, recharge, ledgerAmount, invoiceTotal, paymentAmount)
	}
	if income.Valid || totalCost.Valid || essentialCost.Valid {
		t.Fatalf("migration invented references: income=%v total=%v essential=%v", income, totalCost, essentialCost)
	}
}
