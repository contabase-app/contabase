package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestCaixinhaBalanceUsesOnlyLedgerNoDoubleCounting(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDoubleCountingScenario(t, db)

	h := MetasHandler{DB: db, WorkspaceID: "ws-dc", UserID: "user-dc"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	box := mustFindCaixinha(t, data.Caixinhas, "box-decoracao")
	if box.IsNegative {
		t.Fatalf("box IsNegative = true, want false (balance should be positive)")
	}

	balanceCents := moneyDisplayToCentsForTest(t, box.Balance)
	if balanceCents != 39000 {
		t.Fatalf("balance = %d cents (R$ %.2f), want 39000 cents (R$ 390.00) — double counting detected", balanceCents, float64(balanceCents)/100.0)
	}

	if box.Percent != 39 {
		t.Fatalf("percent = %d, want 39 (39000/100000)", box.Percent)
	}
}

func TestCaixinhaDoubleCountingWithBothLedgerAndTransaction(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDoubleCountingScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-dc', 'ws-dc', 'Conta DC', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-decoracao-1', 'ws-dc', 'user-dc', 'acc-dc', 'cat-decoracao', 'EXPENSE', 1000, ?, 'Item decoracao A', 'paid', ?, ?)
	`, now, now, now)

	h := MetasHandler{DB: db, WorkspaceID: "ws-dc", UserID: "user-dc"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	box := mustFindCaixinha(t, data.Caixinhas, "box-decoracao")
	balanceCents := moneyDisplayToCentsForTest(t, box.Balance)
	if balanceCents != 39000 {
		t.Fatalf("balance = %d cents, want 39000 — expense transaction should NOT be subtracted again (ledger already has CONSUME -1000)", balanceCents)
	}
}

func TestCaixinhaBalanceWithSubcategoryConsumption(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDoubleCountingSubcategoryScenario(t, db)

	h := MetasHandler{DB: db, WorkspaceID: "ws-sc", UserID: "user-sc"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	box := mustFindCaixinha(t, data.Caixinhas, "box-parent")
	balanceCents := moneyDisplayToCentsForTest(t, box.Balance)
	if balanceCents != 39000 {
		t.Fatalf("balance with subcategory = %d cents, want 39000 — subcategory expense should not be double counted", balanceCents)
	}
}

func TestCaixinhaReleaseReducesOnce(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDoubleCountingScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES ('lv-release', 'box-decoracao', -10000, 'RELEASE', 'Liberacao parcial', ?, ?)
	`, now, now)

	h := MetasHandler{DB: db, WorkspaceID: "ws-dc", UserID: "user-dc"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	box := mustFindCaixinha(t, data.Caixinhas, "box-decoracao")
	balanceCents := moneyDisplayToCentsForTest(t, box.Balance)
	if balanceCents != 29000 {
		t.Fatalf("balance after RELEASE = %d cents, want 29000 (40000 - 1000 CONSUME - 10000 RELEASE)", balanceCents)
	}
}

func TestCaixinhaReversalRecomposesOnce(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDoubleCountingScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reversal_of_ledger_id, reference_date, created_at)
		VALUES ('lv-reversal', 'box-decoracao', 1000, 'REVERSAL', 'Estorno do consumo', 'lv-consume', ?, ?)
	`, now, now)

	h := MetasHandler{DB: db, WorkspaceID: "ws-dc", UserID: "user-dc"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	box := mustFindCaixinha(t, data.Caixinhas, "box-decoracao")
	balanceCents := moneyDisplayToCentsForTest(t, box.Balance)
	if balanceCents != 40000 {
		t.Fatalf("balance after REVERSAL = %d cents, want 40000 (REVERSAL +1000 undoes CONSUME -1000)", balanceCents)
	}
}

func TestCaixinhaWorkspaceIsolationBalance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDoubleCountingScenario(t, db)

	h := MetasHandler{DB: db, WorkspaceID: "ws-other", UserID: "user-other"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}
	if len(data.Caixinhas) != 0 {
		t.Fatalf("caixinhas count in wrong workspace = %d, want 0", len(data.Caixinhas))
	}
}

func seedDoubleCountingScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-dc', 'User DC', 'dc@example.com', 'hash', ?, ?),
			('user-other', 'User Other', 'other@example.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-dc', 'WS DC', '', 'personal', ?, ?),
			('ws-other', 'WS Other', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-dc', 'user-dc', 'ADMIN', ?),
			('ws-other', 'user-other', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES ('cat-decoracao', 'ws-dc', 'Decoração', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('box-decoracao', 'ws-dc', 'Decoração', 'cat-decoracao', 100000, 0, ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('lv-recharge', 'box-decoracao', 40000, 'RECHARGE', 'Aporte inicial', ?, ?),
			('lv-consume', 'box-decoracao', -1000, 'CONSUME', 'Consumo decoracao', ?, ?)
	`, now, now, now, now)
}

func seedDoubleCountingSubcategoryScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-sc', 'User SC', 'sc@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-sc', 'WS SC', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-sc', 'user-sc', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('cat-parent', 'ws-sc', 'Casa', 'EXPENSE', ?),
			('cat-child', 'ws-sc', 'Decoracao', 'EXPENSE', ?)
	`, now, now)
	execTestSQL(t, db, `
		UPDATE categories SET parent_id = 'cat-parent' WHERE id = 'cat-child'
	`)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('box-parent', 'ws-sc', 'Casa', 'cat-parent', 100000, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('lv-sc-recharge', 'box-parent', 40000, 'RECHARGE', 'Aporte', ?, ?),
			('lv-sc-consume', 'box-parent', -1000, 'CONSUME', 'Consumo subcategoria', ?, ?)
	`, now, now, now, now)
}
