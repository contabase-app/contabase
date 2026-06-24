package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestMetasCaixinhaSimpleProjectionLabels(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

	handler := MetasHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	data, err := handler.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if len(data.Caixinhas) != 5 {
		t.Fatalf("caixinhas count = %d, want 5", len(data.Caixinhas))
	}

	completed := mustFindCaixinha(t, data.Caixinhas, "a-box-completed")
	if completed.ForecastLabel != "Previsão: meta concluída" || completed.MonthsLeft != 0 {
		t.Fatalf("completed projection = %q/%d, want Previsão: meta concluída/0", completed.ForecastLabel, completed.MonthsLeft)
	}
	if completed.RequiredState != requiredStateCompleted {
		t.Fatalf("completed required state = %q, want %q", completed.RequiredState, requiredStateCompleted)
	}

	withRecharge := mustFindCaixinha(t, data.Caixinhas, "a-box-recharge")
	if withRecharge.ForecastLabel != "Previsão: 3 meses" || withRecharge.MonthsLeft != 3 {
		t.Fatalf("recharge projection = %q/%d, want Previsão: 3 meses/3", withRecharge.ForecastLabel, withRecharge.MonthsLeft)
	}
	if withRecharge.RequiredState != requiredStateValue {
		t.Fatalf("recharge required state = %q, want %q", withRecharge.RequiredState, requiredStateValue)
	}
	if got := moneyDisplayToCentsForTest(t, withRecharge.RequiredMonthly); got != 3000 {
		t.Fatalf("recharge required monthly = %d, want 3000", got)
	}
	if withRecharge.TargetMonth == "" {
		t.Fatalf("recharge target month label empty")
	}

	withoutRecharge := mustFindCaixinha(t, data.Caixinhas, "a-box-no-recharge")
	if withoutRecharge.ForecastLabel != "Previsão: sem previsão" || withoutRecharge.MonthsLeft != 0 {
		t.Fatalf("no recharge projection = %q/%d, want Previsão: sem previsão/0", withoutRecharge.ForecastLabel, withoutRecharge.MonthsLeft)
	}
	if withoutRecharge.RequiredState != requiredStateNoDeadline {
		t.Fatalf("no recharge required state = %q, want %q", withoutRecharge.RequiredState, requiredStateNoDeadline)
	}

	withoutTarget := mustFindCaixinha(t, data.Caixinhas, "a-box-no-target")
	if withoutTarget.ForecastLabel != "Previsão: sem previsão" || withoutTarget.MonthsLeft != 0 {
		t.Fatalf("no target projection = %q/%d, want Previsão: sem previsão/0", withoutTarget.ForecastLabel, withoutTarget.MonthsLeft)
	}
	if withoutTarget.RequiredState != requiredStateNone {
		t.Fatalf("no target required state = %q, want empty", withoutTarget.RequiredState)
	}

	rounded := mustFindCaixinha(t, data.Caixinhas, "a-box-rounded")
	if rounded.ForecastLabel != "Previsão: 3 meses" || rounded.MonthsLeft != 3 {
		t.Fatalf("rounded projection = %q/%d, want Previsão: 3 meses/3", rounded.ForecastLabel, rounded.MonthsLeft)
	}
	if rounded.RequiredState != requiredStateNoDeadline {
		t.Fatalf("rounded required state = %q, want %q for expired target date", rounded.RequiredState, requiredStateNoDeadline)
	}
}

func seedCaixinhaProjectionScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()
	currentMonth := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 12, 0, 0, 0, time.UTC)
	plusThreeMonths := currentMonth.AddDate(0, 3, 0).Unix()
	plusFourMonths := currentMonth.AddDate(0, 4, 0).Unix()
	previousMonth := currentMonth.AddDate(0, -1, 0).Unix()

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
			('a-cat-3', 'ws-a', 'A Cat 3', 'EXPENSE', ?),
			('a-cat-4', 'ws-a', 'A Cat 4', 'EXPENSE', ?),
			('a-cat-5', 'ws-a', 'A Cat 5', 'EXPENSE', ?),
			('b-cat-1', 'ws-b', 'B Cat 1', 'EXPENSE', ?)
	`, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, target_date, created_at, updated_at)
		VALUES
			('a-box-completed', 'ws-a', 'A Completed', 'a-cat-1', 1000, 100, ?, ?, ?),
			('a-box-recharge', 'ws-a', 'A Recharge', 'a-cat-2', 10000, 3000, ?, ?, ?),
			('a-box-no-recharge', 'ws-a', 'A No Recharge', 'a-cat-3', 10000, 0, NULL, ?, ?),
			('a-box-no-target', 'ws-a', 'A No Target', 'a-cat-4', 0, 1000, ?, ?, ?),
			('a-box-rounded', 'ws-a', 'A Rounded', 'a-cat-5', 10000, 4000, ?, ?, ?),
			('b-box-other', 'ws-b', 'B Other', 'b-cat-1', 99999, 9999, ?, ?, ?)
		`,
		currentMonth.Unix(), now, now,
		plusThreeMonths, now, now,
		now, now,
		plusFourMonths, now, now,
		previousMonth, now, now,
		plusFourMonths, now, now,
	)

	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('l1', 'a-box-completed', 1200, 'RECHARGE', 'seed', ?, ?),
			('l2', 'a-box-recharge', 1000, 'RECHARGE', 'seed', ?, ?),
			('l3', 'a-box-no-recharge', 1000, 'RECHARGE', 'seed', ?, ?),
			('l4', 'a-box-no-target', 1000, 'RECHARGE', 'seed', ?, ?),
			('l5', 'a-box-rounded', 1000, 'RECHARGE', 'seed', ?, ?),
			('l6', 'b-box-other', 50000, 'RECHARGE', 'seed', ?, ?)
	`, now, now, now, now, now, now, now, now, now, now, now, now)
}

func mustFindCaixinha(t *testing.T, rows []CaixinhaCard, id string) CaixinhaCard {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("caixinha %s not found", id)
	return CaixinhaCard{}
}
