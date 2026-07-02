package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestDashboardBoxesIncludesNullCategory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDashboardBoxesNullCategoryScenario(t, db)

	boxes := queryDashboardBoxes(db, "ws-a")
	if len(boxes) != 2 {
		t.Fatalf("dashboard boxes count = %d, want 2 (one with category, one without)", len(boxes))
	}

	foundNoCat := false
	foundWithCat := false
	for _, b := range boxes {
		if b.Name == "Reserva sem categoria" {
			foundNoCat = true
			if b.CategoryID != "" {
				t.Fatalf("uncategorized box CategoryID = %q, want empty", b.CategoryID)
			}
			if b.Icon != "piggy-bank" {
				t.Fatalf("uncategorized box icon = %q, want piggy-bank", b.Icon)
			}
			if b.Color != "#6b7280" {
				t.Fatalf("uncategorized box color = %q, want #6b7280", b.Color)
			}
		}
		if b.Name == "Reserva com categoria" {
			foundWithCat = true
			if b.CategoryID != "a-cat" {
				t.Fatalf("categorized box CategoryID = %q, want a-cat", b.CategoryID)
			}
			if b.Icon != "home" {
				t.Fatalf("categorized box icon = %q, want home", b.Icon)
			}
			if b.Color != "#22c55e" {
				t.Fatalf("categorized box color = %q, want #22c55e", b.Color)
			}
		}
	}
	if !foundNoCat {
		t.Fatalf("uncategorized box missing from dashboard")
	}
	if !foundWithCat {
		t.Fatalf("categorized box missing from dashboard")
	}
}

func TestDashboardBoxesWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDashboardBoxesNullCategoryScenario(t, db)

	boxesWsA := queryDashboardBoxes(db, "ws-a")
	if len(boxesWsA) == 0 {
		t.Fatalf("ws-a should see its own boxes")
	}

	boxesWsB := queryDashboardBoxes(db, "ws-b")
	if len(boxesWsB) != 0 {
		t.Fatalf("ws-b should see no boxes, got %d", len(boxesWsB))
	}
}

func TestDashboardBoxesNullCategoryIconFallback(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedDashboardBoxesNullCategoryScenario(t, db)

	boxes := queryDashboardBoxes(db, "ws-a")
	for _, b := range boxes {
		if b.Icon == "" {
			t.Fatalf("box %q icon is empty, want fallback", b.Name)
		}
		if b.Color == "" {
			t.Fatalf("box %q color is empty, want fallback", b.Name)
		}
	}
}

func seedDashboardBoxesNullCategoryScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@example.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@example.com', 'hash', ?, ?)
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
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES
			('a-cat', 'ws-a', 'A Cat', 'home', '#22c55e', 'EXPENSE', ?)
	`, now)

	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('box-no-cat', 'ws-a', 'Reserva sem categoria', NULL, 100000, 0, ?, ?),
			('box-with-cat', 'ws-a', 'Reserva com categoria', 'a-cat', 200000, 5000, ?, ?)
	`, now, now, now, now)
}
