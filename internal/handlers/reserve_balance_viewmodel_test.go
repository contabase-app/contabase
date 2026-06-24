package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestDashboardAndMetasExposeReserveContractByWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedReserveBalanceViewModelScenario(t, db)

	dashboardA := BuildDashboardData(db, "user-a", "ws-a")
	if got := moneyDisplayToCentsForTest(t, dashboardA.Balance.Money); got != 15000 {
		t.Fatalf("dashboard ws-a saldo real = %d, want 15000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardA.Balance.Reserved); got != 2000 {
		t.Fatalf("dashboard ws-a reservado = %d, want 2000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardA.Balance.Free); got != 13000 {
		t.Fatalf("dashboard ws-a saldo livre = %d, want 13000", got)
	}
	if dashboardA.Balance.FreeIsNegative {
		t.Fatalf("dashboard ws-a free negative = true, want false")
	}

	metasHandlerA := MetasHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	metasA, err := metasHandlerA.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData ws-a: %v", err)
	}
	if got := moneyDisplayToCentsForTest(t, metasA.RealBalance); got != 15000 {
		t.Fatalf("metas ws-a saldo real = %d, want 15000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasA.ReservedBalance); got != 2000 {
		t.Fatalf("metas ws-a reservado = %d, want 2000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasA.FreeBalance); got != 13000 {
		t.Fatalf("metas ws-a saldo livre = %d, want 13000", got)
	}
	if metasA.FreeBalanceNegative {
		t.Fatalf("metas ws-a free negative = true, want false")
	}

	dashboardB := BuildDashboardData(db, "user-b", "ws-b")
	if got := moneyDisplayToCentsForTest(t, dashboardB.Balance.Money); got != 7000 {
		t.Fatalf("dashboard ws-b saldo real = %d, want 7000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardB.Balance.Reserved); got != 9000 {
		t.Fatalf("dashboard ws-b reservado = %d, want 9000", got)
	}
	if got := moneyDisplayToCentsForTest(t, dashboardB.Balance.Free); got != -2000 {
		t.Fatalf("dashboard ws-b saldo livre = %d, want -2000", got)
	}
	if !dashboardB.Balance.FreeIsNegative {
		t.Fatalf("dashboard ws-b free negative = false, want true")
	}

	metasHandlerB := MetasHandler{DB: db, WorkspaceID: "ws-b", UserID: "user-b"}
	metasB, err := metasHandlerB.buildMetasData("", "")
	if err != nil {
		t.Fatalf("buildMetasData ws-b: %v", err)
	}
	if got := moneyDisplayToCentsForTest(t, metasB.RealBalance); got != 7000 {
		t.Fatalf("metas ws-b saldo real = %d, want 7000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasB.ReservedBalance); got != 9000 {
		t.Fatalf("metas ws-b reservado = %d, want 9000", got)
	}
	if got := moneyDisplayToCentsForTest(t, metasB.FreeBalance); got != -2000 {
		t.Fatalf("metas ws-b saldo livre = %d, want -2000", got)
	}
	if !metasB.FreeBalanceNegative {
		t.Fatalf("metas ws-b free negative = false, want true")
	}
}

func seedReserveBalanceViewModelScenario(t *testing.T, db *sql.DB) {
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
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('a-cat-1', 'ws-a', 'A Cat 1', 'EXPENSE', ?),
			('a-cat-2', 'ws-a', 'A Cat 2', 'EXPENSE', ?),
			('b-cat-1', 'ws-b', 'B Cat 1', 'EXPENSE', ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-check', 'ws-a', 'Conta A', 'CHECKING', 10000, 10000, ?, ?),
			('a-save', 'ws-a', 'Poupança A', 'SAVINGS', 5000, 5000, ?, ?),
			('a-card', 'ws-a', 'Card A', 'CREDIT_CARD', 0, 90000, ?, ?),
			('b-check', 'ws-b', 'Conta B', 'CHECKING', 7000, 7000, ?, ?),
			('b-card', 'ws-b', 'Card B', 'CREDIT_CARD', 0, 2000, ?, ?)
	`, now, now, now, now, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('a-box-1', 'ws-a', 'A Box 1', 'a-cat-1', 0, 0, ?, ?),
			('a-box-2', 'ws-a', 'A Box 2', 'a-cat-2', 0, 0, ?, ?),
			('b-box-1', 'ws-b', 'B Box 1', 'b-cat-1', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('a-ledger-1', 'a-box-1', 1200, 'RECHARGE', 'Aporte', ?, ?),
			('a-ledger-2', 'a-box-2', 800, 'RECHARGE', 'Aporte', ?, ?),
			('b-ledger-1', 'b-box-1', 9000, 'RECHARGE', 'Aporte', ?, ?)
	`, now, now, now, now, now, now)
}
