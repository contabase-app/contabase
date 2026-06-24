package handlers

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDashboardAndMetasLimitsUseParentAndSubcategoryConsistently(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedLimitsConsistencyScenario(t, db)

	dashboard := BuildDashboardData(db, "user-a", "ws-a")
	var dashCard *DashboardLimitCard
	for i := range dashboard.Limits {
		if dashboard.Limits[i].CategoryName == "Parent A" {
			dashCard = &dashboard.Limits[i]
			break
		}
	}
	if dashCard == nil {
		t.Fatalf("dashboard limit for Parent A not found: %#v", dashboard.Limits)
	}

	metasHandler := MetasHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	metas, err := metasHandler.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}
	var metasCard *LimiteCard
	for i := range metas.Limites {
		if metas.Limites[i].CategoryID == "cat-parent-a" {
			metasCard = &metas.Limites[i]
			break
		}
	}
	if metasCard == nil {
		t.Fatalf("metas limit for cat-parent-a not found: %#v", metas.Limites)
	}

	// 1000 (categoria pai) + 4000 (subcategoria) = 5000.
	const wantSpent = int64(5000)
	if got := moneyDisplayToCentsForTest(t, dashCard.Spent); got != wantSpent {
		t.Fatalf("dashboard spent = %d, want %d", got, wantSpent)
	}
	if got := moneyDisplayToCentsForTest(t, metasCard.Spent); got != wantSpent {
		t.Fatalf("metas spent = %d, want %d", got, wantSpent)
	}
	if dashCard.Percent != 50 || metasCard.Percent != 50 {
		t.Fatalf("percent mismatch dashboard/metas = %d/%d, want 50/50", dashCard.Percent, metasCard.Percent)
	}
}

func TestDashboardAndMetasLimitsDoNotIncludeUnrelatedOrForeignWorkspaceExpenses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedLimitsConsistencyScenario(t, db)

	dashboardA := BuildDashboardData(db, "user-a", "ws-a")
	dashboardB := BuildDashboardData(db, "user-b", "ws-b")
	metasHandlerA := MetasHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	metasA, err := metasHandlerA.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData ws-a: %v", err)
	}
	metasHandlerB := MetasHandler{DB: db, WorkspaceID: "ws-b", UserID: "user-b"}
	metasB, err := metasHandlerB.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData ws-b: %v", err)
	}

	dashSpentA := limitSpentFromDashboardByCategory(t, dashboardA.Limits, "Parent A")
	dashSpentB := limitSpentFromDashboardByCategory(t, dashboardB.Limits, "Parent B")
	metaSpentA := limitSpentFromMetasByCategoryID(t, metasA.Limites, "cat-parent-a")
	metaSpentB := limitSpentFromMetasByCategoryID(t, metasB.Limites, "cat-parent-b")

	// ws-a: 1000 (pai) + 4000 (filha). Não deve incluir 7000 da categoria não relacionada
	// nem 9000 do workspace B.
	if dashSpentA != 5000 || metaSpentA != 5000 {
		t.Fatalf("ws-a spent dashboard/metas = %d/%d, want 5000/5000", dashSpentA, metaSpentA)
	}
	// ws-b: só transações de ws-b.
	if dashSpentB != 9000 || metaSpentB != 9000 {
		t.Fatalf("ws-b spent dashboard/metas = %d/%d, want 9000/9000", dashSpentB, metaSpentB)
	}
}

func seedLimitsConsistencyScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC()
	nowUnix := now.Unix()
	currentMonth := time.Date(now.Year(), now.Month(), 10, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@exemplo.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@exemplo.com', 'hash', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('acc-a', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?),
			('acc-b', 'ws-b', 'Conta B', 'CHECKING', 0, 0, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, parent_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cat-parent-a', 'ws-a', NULL, 'Parent A', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('cat-child-a', 'ws-a', 'cat-parent-a', 'Child A', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('cat-other-a', 'ws-a', NULL, 'Other A', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('cat-parent-b', 'ws-b', NULL, 'Parent B', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('cat-child-b', 'ws-b', 'cat-parent-b', 'Child B', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES
			('limit-a', 'ws-a', 'cat-parent-a', 10000),
			('limit-b', 'ws-b', 'cat-parent-b', 20000)
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('tx-a-parent', 'ws-a', 'user-a', 'acc-a', 'cat-parent-a', 'EXPENSE', 1000, ?, 'A Parent', 'paid', 1, 1, ?, ?),
			('tx-a-child', 'ws-a', 'user-a', 'acc-a', 'cat-child-a', 'EXPENSE', 4000, ?, 'A Child', 'paid', 1, 1, ?, ?),
			('tx-a-other', 'ws-a', 'user-a', 'acc-a', 'cat-other-a', 'EXPENSE', 7000, ?, 'A Other', 'paid', 1, 1, ?, ?),
			('tx-b-child', 'ws-b', 'user-b', 'acc-b', 'cat-child-b', 'EXPENSE', 9000, ?, 'B Child', 'paid', 1, 1, ?, ?)
	`, currentMonth, nowUnix, nowUnix, currentMonth, nowUnix, nowUnix, currentMonth, nowUnix, nowUnix, currentMonth, nowUnix, nowUnix)
}

func limitSpentFromDashboardByCategory(t *testing.T, limits []DashboardLimitCard, categoryName string) int64 {
	t.Helper()
	for _, card := range limits {
		if card.CategoryName == categoryName {
			return moneyDisplayToCentsForTest(t, card.Spent)
		}
	}
	t.Fatalf("dashboard limit not found for category %q", categoryName)
	return 0
}

func limitSpentFromMetasByCategoryID(t *testing.T, limits []LimiteCard, categoryID string) int64 {
	t.Helper()
	for _, card := range limits {
		if card.CategoryID == categoryID {
			return moneyDisplayToCentsForTest(t, card.Spent)
		}
	}
	t.Fatalf("metas limit not found for category %q", categoryID)
	return 0
}

func moneyDisplayToCentsForTest(t *testing.T, money MoneyDisplay) int64 {
	t.Helper()
	raw := strings.TrimSpace(money.Reais + money.Cents)
	raw = strings.ReplaceAll(raw, ".", "")
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || parts[0] == "" {
		t.Fatalf("invalid money display: %#v", money)
	}
	reais := parts[0]
	cents := "00"
	if len(parts) > 1 {
		cents = parts[1]
	}
	if len(cents) == 1 {
		cents += "0"
	}
	if len(cents) > 2 {
		cents = cents[:2]
	}
	value, err := strconv.ParseInt(reais+cents, 10, 64)
	if err != nil {
		t.Fatalf("parse money display %#v: %v", money, err)
	}
	return value
}
