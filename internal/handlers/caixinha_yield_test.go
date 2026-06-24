package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestParseMonthlyYieldRate(t *testing.T) {
	tests := []struct {
		input    string
		wantRate float64
		wantErr  bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"0,8", 0.008, false},
		{"0.8", 0.008, false},
		{"1,5", 0.015, false},
		{"5", 0.05, false},
		{"100", 1.0, false},
		{"-1", 0, false},
		{"abc", 0, true},
		{"101", 0, true},
	}
	for _, tt := range tests {
		rate, err := parseMonthlyYieldRate(tt.input)
		if tt.wantErr && err == nil {
			t.Errorf("parse(%q) expected error, got rate=%f", tt.input, rate)
		}
		if !tt.wantErr {
			if err != nil {
				t.Errorf("parse(%q) unexpected error: %v", tt.input, err)
			}
			if rate != tt.wantRate {
				t.Errorf("parse(%q) = %f, want %f", tt.input, rate, tt.wantRate)
			}
		}
	}
}

func TestFormatMonthlyYieldRateForInput(t *testing.T) {
	if got := formatMonthlyYieldRateForInput(0); got != "" {
		t.Fatalf("format(0) = %q, want empty", got)
	}
	if got := formatMonthlyYieldRateForInput(0.008); got != "0,8" {
		t.Fatalf("format(0.008) = %q, want 0,8", got)
	}
	if got := formatMonthlyYieldRateForInput(-0.01); got != "" {
		t.Fatalf("format(-0.01) = %q, want empty", got)
	}
}

func TestBuildMetasDataYieldProjectionShowsWhenRateReducesMonths(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Unix()
	seedMetasYieldScenario(t, db, now)

	h := MetasHandler{DB: db, WorkspaceID: "ws-yield", UserID: "user-yield"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	boxYield := mustFindCaixinha(t, data.Caixinhas, "box-with-yield")
	if boxYield.YieldForecastLabel == "" {
		t.Fatalf("box with yield_rate=0.02 should show YieldForecastLabel, got empty")
	}

	boxNoYield := mustFindCaixinha(t, data.Caixinhas, "box-no-yield")
	if boxNoYield.YieldForecastLabel != "" {
		t.Fatalf("box with yield_rate=0 should not show YieldForecastLabel, got %q", boxNoYield.YieldForecastLabel)
	}

	boxCompleted := mustFindCaixinha(t, data.Caixinhas, "box-completed-yield")
	if boxCompleted.YieldForecastLabel != "" {
		t.Fatalf("completed box should not show YieldForecastLabel, got %q", boxCompleted.YieldForecastLabel)
	}

	boxNoTarget := mustFindCaixinha(t, data.Caixinhas, "box-no-target-yield")
	if boxNoTarget.YieldForecastLabel != "" {
		t.Fatalf("no-target box should not show YieldForecastLabel, got %q", boxNoTarget.YieldForecastLabel)
	}
}

func TestBuildMetasDataYieldDoesNotAffectBalance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Unix()
	seedMetasYieldScenario(t, db, now)

	h := MetasHandler{DB: db, WorkspaceID: "ws-yield", UserID: "user-yield"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	boxYield := mustFindCaixinha(t, data.Caixinhas, "box-with-yield")
	balanceCents := moneyDisplayToCentsForTest(t, boxYield.Balance)
	if balanceCents != 80000 {
		t.Fatalf("balance with yield = %d, want 80000 (yield does not affect balance)", balanceCents)
	}

	reservedCents := moneyDisplayToCentsForTest(t, data.ReservedBalance)
	if reservedCents != 220000 {
		t.Fatalf("reserved balance = %d, want 220000 (yield does not affect reserved)", reservedCents)
	}
}

func TestBuildMetasDataYieldWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Unix()
	seedMetasYieldScenario(t, db, now)

	h := MetasHandler{DB: db, WorkspaceID: "ws-other-yield", UserID: "user-other-yield"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}
	if len(data.Caixinhas) != 0 {
		t.Fatalf("caixinhas count in wrong workspace = %d, want 0", len(data.Caixinhas))
	}
}

func seedMetasYieldScenario(t *testing.T, db *sql.DB, now int64) {
	t.Helper()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-yield', 'User Yield', 'yield@example.com', 'hash', ?, ?),
			('user-other-yield', 'User Other', 'other-yield@example.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-yield', 'WS Yield', '', 'personal', ?, ?),
			('ws-other-yield', 'WS Other', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-yield', 'user-yield', 'ADMIN', ?),
			('ws-other-yield', 'user-other-yield', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-yield', 'ws-yield', 'Conta', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('cat-yield-1', 'ws-yield', 'Cat Yield 1', 'EXPENSE', ?),
			('cat-yield-2', 'ws-yield', 'Cat Yield 2', 'EXPENSE', ?),
			('cat-yield-3', 'ws-yield', 'Cat Yield 3', 'EXPENSE', ?),
			('cat-yield-4', 'ws-yield', 'Cat Yield 4', 'EXPENSE', ?)
	`, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, monthly_yield_rate, created_at, updated_at)
		VALUES
			('box-with-yield', 'ws-yield', 'With Yield', 'cat-yield-1', 100000, 5000, 0.02, ?, ?),
			('box-no-yield', 'ws-yield', 'No Yield', 'cat-yield-2', 100000, 50000, 0.0, ?, ?),
			('box-completed-yield', 'ws-yield', 'Completed', 'cat-yield-3', 50000, 5000, 0.01, ?, ?),
			('box-no-target-yield', 'ws-yield', 'No Target', 'cat-yield-4', 0, 5000, 0.008, ?, ?)
	`, now, now, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('lv-y1', 'box-with-yield', 80000, 'RECHARGE', 'Aporte', ?, ?),
			('lv-y2', 'box-no-yield', 50000, 'RECHARGE', 'Aporte', ?, ?),
			('lv-y3', 'box-completed-yield', 60000, 'RECHARGE', 'Aporte', ?, ?),
			('lv-y4', 'box-no-target-yield', 30000, 'RECHARGE', 'Aporte', ?, ?)
	`, now, now, now, now, now, now, now, now)
}
