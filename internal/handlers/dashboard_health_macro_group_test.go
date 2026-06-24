package handlers

import (
	"testing"
	"time"
)

func TestDashboardHealthBusinessUsesCanonicalAndLegacyCostMacroGroups(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	createdAt := now.Unix()

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-health', 'Health User', 'health@test.com', 'hash', ?, ?)`, createdAt, createdAt)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-health', 'Health WS', 'business', ?, ?)`, createdAt, createdAt)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-health', 'u-health', 'ADMIN', ?)`, createdAt)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-health', 'ws-health', 'Conta', 'CHECKING', 0, 0, ?, ?)`, createdAt, createdAt)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cat-cost-canonical', 'ws-health', 'Custo Canonico', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', ?),
			('cat-cost-legacy', 'ws-health', 'Custo Legado', 'tag', '#6b7280', 'EXPENSE', 'OPERATING_COSTS', ?),
			('cat-admin', 'ws-health', 'Administrativo', 'tag', '#6b7280', 'EXPENSE', 'Despesas Administrativas', ?)
	`, createdAt, createdAt, createdAt)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES
			('tx-income', 'ws-health', 'u-health', 'acc-health', NULL, 'INCOME', 100000, ?, 'Receita', 'paid', ?, ?),
			('tx-cost-canonical', 'ws-health', 'u-health', 'acc-health', 'cat-cost-canonical', 'EXPENSE', 30000, ?, 'Custo canonico', 'paid', ?, ?),
			('tx-cost-legacy', 'ws-health', 'u-health', 'acc-health', 'cat-cost-legacy', 'EXPENSE', 20000, ?, 'Custo legado', 'paid', ?, ?),
			('tx-admin', 'ws-health', 'u-health', 'acc-health', 'cat-admin', 'EXPENSE', 10000, ?, 'Administrativo', 'paid', ?, ?)
	`, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt, createdAt)

	health := queryDashboardHealth(db, "ws-health", 0, true)
	assertMoneyDisplay(t, "gross profit", health.GrossProfit, "500", ",00")
}
