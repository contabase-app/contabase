package handlers

import (
	"fmt"
	"testing"
	"time"
)

func TestDashboardHomeShowsFirstSixLimitsAndReserves(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"ws-home-six", "Home Six", "personal", now, now)
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("home-%02d", i)
		name := fmt.Sprintf("Item %02d", i)
		execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"category-"+id, "ws-home-six", name, "circle", "#6b7280", "EXPENSE", now)
		execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES (?, ?, ?, ?)`,
			"limit-"+id, "ws-home-six", "category-"+id, 10000)
		execTestSQL(t, db, `INSERT INTO boxes (id, workspace_id, name, target_amount, monthly_recharge, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			"box-"+id, "ws-home-six", name, 10000, 0, now, now)
	}

	limits := queryDashboardLimits(db, "ws-home-six", time.Now())
	boxes := queryDashboardBoxes(db, "ws-home-six")
	if len(limits) != 6 || len(boxes) != 6 {
		t.Fatalf("Home counts: limits=%d boxes=%d, want six each", len(limits), len(boxes))
	}
	for i := 0; i < 6; i++ {
		want := fmt.Sprintf("Item %02d", i+1)
		if limits[i].CategoryName != want || boxes[i].Name != want {
			t.Fatalf("Home order at %d: limit=%q box=%q, want %q", i, limits[i].CategoryName, boxes[i].Name, want)
		}
	}
}
