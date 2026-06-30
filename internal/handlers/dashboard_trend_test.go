package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQueryAccountTrendIncomePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// INCOME paid 500000 cents (R$ 5.000,00) on active checking account
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-income', 'ws-a', 'user-a', 'a-check', 'a-cat', 'INCOME', 500000, ?, 'Freela', 'paid', ?, ?)
	`, now.AddDate(0, 0, -15).Unix(), now.Unix(), now.Unix())

	percent, direction := queryAccountTrend(db, "ws-a", "a-check", 1500000, now)
	if direction != "up" {
		t.Errorf("direction = %q, want up", direction)
	}
	if !strings.HasPrefix(percent, "+") {
		t.Errorf("percent sign: %q, want positive", percent)
	}
}

func TestQueryAccountTrendExpensePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-expense', 'ws-a', 'user-a', 'a-check', 'a-cat', 'EXPENSE', 200000, ?, 'Mercado', 'paid', ?, ?)
	`, now.AddDate(0, 0, -10).Unix(), now.Unix(), now.Unix())

	percent, direction := queryAccountTrend(db, "ws-a", "a-check", 800000, now)
	if direction != "down" {
		t.Errorf("direction = %q, want down", direction)
	}
	if !strings.HasPrefix(percent, "-") {
		t.Errorf("percent sign: %q, want negative", percent)
	}
}

func TestQueryAccountTrendTransferOut(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-xfer-out', 'ws-a', 'user-a', 'a-check', 'a-savings', 'TRANSFER', 100000, ?, 'Transferencia p/ poupanca', 'paid', ?, ?)
	`, now.AddDate(0, 0, -5).Unix(), now.Unix(), now.Unix())

	percent, direction := queryAccountTrend(db, "ws-a", "a-check", 900000, now)
	if direction != "down" {
		t.Errorf("direction = %q, want down", direction)
	}
	if !strings.HasPrefix(percent, "-") {
		t.Errorf("percent sign: %q, want negative", percent)
	}
}

func TestQueryAccountTrendTransferIn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-xfer-in', 'ws-a', 'user-a', 'a-savings', 'a-check', 'TRANSFER', 50000, ?, 'Transferencia da poupanca', 'paid', ?, ?)
	`, now.AddDate(0, 0, -5).Unix(), now.Unix(), now.Unix())

	percent, direction := queryAccountTrend(db, "ws-a", "a-check", 1050000, now)
	if direction != "up" {
		t.Errorf("direction = %q, want up", direction)
	}
	if !strings.HasPrefix(percent, "+") {
		t.Errorf("percent sign: %q, want positive", percent)
	}
}

func TestQueryBalanceTrendInternalTransferZero(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Transfer between two active non-credit-card accounts: should net to 0
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-internal', 'ws-a', 'user-a', 'a-check', 'a-savings', 'TRANSFER', 50000, ?, 'Transferencia interna', 'paid', ?, ?)
	`, now.AddDate(0, 0, -3).Unix(), now.Unix(), now.Unix())

	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	if net != 0 {
		t.Errorf("internal transfer net = %d, want 0", net)
	}
}

func TestQueryBalanceTrendTransferActiveToArchivedIsOutflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Archive a-savings
	execTestSQL(t, db, `UPDATE accounts SET archived_at = ? WHERE id = 'a-savings'`, now.Unix())

	// Transfer from active (a-check) to archived (a-savings): should count as outflow
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-to-archived', 'ws-a', 'user-a', 'a-check', 'a-savings', 'TRANSFER', 30000, ?, 'Transferencia p/ arquivada', 'paid', ?, ?)
	`, now.AddDate(0, 0, -3).Unix(), now.Unix(), now.Unix())

	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	if net != -30000 {
		t.Errorf("transfer to archived net = %d, want -30000", net)
	}
}

func TestQueryBalanceTrendTransferArchivedToActiveIsInflow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Archive a-savings
	execTestSQL(t, db, `UPDATE accounts SET archived_at = ? WHERE id = 'a-savings'`, now.Unix())

	// Transfer from archived (a-savings) to active (a-check): should count as inflow
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-from-archived', 'ws-a', 'user-a', 'a-savings', 'a-check', 'TRANSFER', 30000, ?, 'Transferencia da arquivada', 'paid', ?, ?)
	`, now.AddDate(0, 0, -3).Unix(), now.Unix(), now.Unix())

	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	if net != 30000 {
		t.Errorf("transfer from archived net = %d, want 30000", net)
	}
}

func TestQueryBalanceTrendCardPurchasePlusPaymentCountsOnlyPayment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Setup credit card account
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-card', 'ws-a', 'Cartao A', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now.Unix(), now.Unix())
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-a', 'a-card', 15, 5, 500000)
	`)

	// Credit card purchase (EXPENSE on CREDIT_CARD account): should NOT be counted in consolidated
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-card-purchase', 'ws-a', 'user-a', 'a-card', 'a-cat', 'EXPENSE', 150000, ?, 'Compra cartao', 'paid', ?, ?)
	`, now.AddDate(0, 0, -10).Unix(), now.Unix(), now.Unix())

	// Invoice payment (EXPENSE on CHECKING account): SHOULD be counted
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pay-invoice', 'ws-a', 'user-a', 'a-check', 'a-cat', 'EXPENSE', 150000, ?, 'Pagamento fatura', 'paid', ?, ?)
	`, now.AddDate(0, 0, -5).Unix(), now.Unix(), now.Unix())

	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	// Only the invoice payment (-150000) should be counted, not the card purchase
	if net != -150000 {
		t.Errorf("card+purchase net = %d, want -150000 (only bank payment counts)", net)
	}
}

func TestQueryBalanceTrendExcludesArchivedAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Create an account and archive it
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-archived', 'ws-a', 'Conta Arquivada', 'CHECKING', 0, 0, ?, ?)
	`, now.Unix(), now.Unix())
	execTestSQL(t, db, `UPDATE accounts SET archived_at = ? WHERE id = 'a-archived'`, now.Unix())

	// EXPENSE on archived account: should NOT be counted
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-archived-exp', 'ws-a', 'user-a', 'a-archived', 'a-cat', 'EXPENSE', 80000, ?, 'Despesa arquivada', 'paid', ?, ?)
	`, now.AddDate(0, 0, -7).Unix(), now.Unix(), now.Unix())

	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	if net != 0 {
		t.Errorf("archived account expense net = %d, want 0", net)
	}
}

func TestQueryBalanceTrendOtherWorkspaceDoesNotLeak(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// Insert transaction in ws-b (colliding account_id pattern)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-ws-b', 'ws-b', 'user-b', 'b-check', 'b-cat', 'INCOME', 999999, ?, 'Receita workspace B', 'paid', ?, ?)
	`, now.AddDate(0, 0, -15).Unix(), now.Unix(), now.Unix())

	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	if net != 0 {
		t.Errorf("cross-workspace leak: net = %d, want 0", net)
	}
}

func TestQueryBalanceTrendWithKnownValues(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	// INCOME on a-check: +500000
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-inc-1', 'ws-a', 'user-a', 'a-check', 'a-cat', 'INCOME', 500000, ?, 'INCOME 1', 'paid', ?, ?)
	`, now.AddDate(0, 0, -20).Unix(), now.Unix(), now.Unix())
	// EXPENSE on a-check: -200000
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-exp-1', 'ws-a', 'user-a', 'a-check', 'a-cat', 'EXPENSE', 200000, ?, 'EXPENSE 1', 'paid', ?, ?)
	`, now.AddDate(0, 0, -15).Unix(), now.Unix(), now.Unix())
	// Transfer a-check -> a-savings (both active, non-credit-card): net 0
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-xfer-int', 'ws-a', 'user-a', 'a-check', 'a-savings', 'TRANSFER', 100000, ?, 'Transferencia interna', 'paid', ?, ?)
	`, now.AddDate(0, 0, -10).Unix(), now.Unix(), now.Unix())

	// Expected net: 500000 - 200000 + 0 = 300000
	net := queryRealAccountNetCashFlow(db, "ws-a", now.AddDate(0, 0, -30).Unix(), now.Unix())
	if net != 300000 {
		t.Errorf("net = %d, want 300000", net)
	}

	// Balance trend with currentBalance = 1300000 (initial 1000000 + net 300000)
	percent, direction := queryBalanceTrend(db, "ws-a", 1300000, now)
	if direction != "up" {
		t.Errorf("direction = %q, want up", direction)
	}
	if !strings.HasPrefix(percent, "+") {
		t.Errorf("percent sign: %q, want positive", percent)
	}
}

func TestFormatTrendPercentNormal(t *testing.T) {
	tests := []struct {
		name     string
		current  int64
		previous int64
		want     string
	}{
		{"positive_30pct", 13000, 10000, "+30,0%"},
		{"negative_20pct", 8000, 10000, "-20,0%"},
		{"positive_small", 1010, 1000, "+1,0%"},
		{"negative_small", 990, 1000, "-1,0%"},
		{"zero_delta", 5000, 5000, "0,0%"},
		{"positive_one_tenth", 1001, 1000, "+0,1%"},
		{"negative_one_tenth", 999, 1000, "-0,1%"},
		{"positive_900pct", 10000, 1000, "+900,0%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTrendPercent(tt.current, tt.previous)
			if got != tt.want {
				t.Errorf("formatTrendPercent(%d, %d) = %q, want %q", tt.current, tt.previous, got, tt.want)
			}
		})
	}
}

func TestFormatTrendPercentClamp(t *testing.T) {
	tests := []struct {
		name     string
		current  int64
		previous int64
		want     string
	}{
		{"base_zero", 5000, 0, "novo"},
		{"base_tiny_1", 100000, 1, "novo"},
		{"base_tiny_9", 100000, 9, "novo"},
		{"extreme_positive", 2000000, 1000, ">+999%"},
		{"just_under_cap_positive", 10990, 1000, "+999,0%"},
		{"just_over_cap_positive", 10991, 1000, ">+999%"},
		{"just_under_cap_negative", 1000, 11000, "-90,9%"},
		{"zero_delta_small_base", 5, 5, "0,0%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTrendPercent(tt.current, tt.previous)
			if got != tt.want {
				t.Errorf("formatTrendPercent(%d, %d) = %q, want %q", tt.current, tt.previous, got, tt.want)
			}
		})
	}
}

func TestHandleHistoricoLimiteOtherWorkspaceCategoryDoesNotLeak(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	// Create a category in ws-b with same ID as ws-a category
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-a', 'ws-a', 'a-cat', 100000)
	`)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id=limit-a", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoLimite(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	// Should show ws-a category name, not ws-b
	if strings.Contains(body, "Categoria B") {
		t.Fatalf("history leaked ws-b category label into ws-a response")
	}
	// ws-a category should still appear
	assertContains(t, body, "Categoria A")
}

func TestHandleHistoricoCaixinhaOtherWorkspaceAccountDoesNotLeakLabel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Now().UTC().Unix()

	// Give ws-a account a distinctive name
	execTestSQL(t, db, `
		UPDATE accounts SET name = 'Conta A - ws-a' WHERE id = 'a-check' AND workspace_id = 'ws-a'
	`)
	// Create a separate ws-b account with different ID
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('b-check-extra', 'ws-b', 'Conta B - ws-b', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	// Create box and ledger event in ws-a only
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('box-isolation', 'ws-a', 'Box Isolation', 'a-cat', 100000, 10000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-isolation', 'ws-a', 'user-a', 'a-check', 'a-cat', 'EXPENSE', 5000, ?, 'Despesa ws-a', 'paid', ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, source_transaction_id, reference_date, created_at)
		VALUES ('h-isolation', 'box-isolation', -5000, 'CONSUME', 'Consumo', 'tx-isolation', ?, ?)
	`, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id=box-isolation", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	// Must show ws-a account name via LEFT JOIN
	assertContains(t, body, "Conta A - ws-a")
	// Must NOT show ws-b account name
	if strings.Contains(body, "Conta B - ws-b") {
		t.Fatalf("history leaked ws-b account name into ws-a response via JOIN")
	}
}

func TestMetasCardOpensHistoryNotMenuOnBodyClick(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTrendScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('box-test', 'ws-a', 'Box Teste', 'a-cat', 100000, 10000, ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-test', 'ws-a', 'a-cat', 50000)
	`)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	// Render metas page
	req := httptest.NewRequest(http.MethodGet, "/metas?aba=limites", nil)
	rr := httptest.NewRecorder()
	h.HandleListarMetas(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// Card body should link to history, not edit form
	assertContains(t, body, `/metas/limite/historico?limit_id=`)
	assertContains(t, body, `hx-target="#bottom-sheet-container"`)

	// Menu should exist with edit link
	assertContains(t, body, `data-caixinha-dropdown`)
	assertContains(t, body, `data-ck-toggle`)
	assertContains(t, body, `data-ck-panel`)
	assertContains(t, body, `/metas/novo?aba=limite&id=`) // edit link in menu
}

func seedTrendScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@exemplo.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@exemplo.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, color, icon, created_at)
		VALUES
			('a-cat', 'ws-a', 'Categoria A', 'EXPENSE', '#6b7280', 'tag', ?),
			('b-cat', 'ws-b', 'Categoria B', 'EXPENSE', '#6b7280', 'tag', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-check', 'ws-a', 'Conta Corrente A', 'CHECKING', 1000000, 1000000, ?, ?),
			('a-savings', 'ws-a', 'Poupanca A', 'SAVINGS', 0, 0, ?, ?),
			('b-check', 'ws-b', 'Conta Corrente B', 'CHECKING', 500000, 500000, ?, ?)
	`, now, now, now, now, now, now)
}
