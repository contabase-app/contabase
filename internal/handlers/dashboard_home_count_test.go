package handlers

import (
	"fmt"
	"testing"
	"time"
)

func TestDashboardHomeShowsFirstSixLimits(t *testing.T) {
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
	}

	limits := queryDashboardLimits(db, "ws-home-six", time.Now())
	if len(limits) != 6 {
		t.Fatalf("Home limits = %d, want six", len(limits))
	}
	for i := 0; i < 6; i++ {
		want := fmt.Sprintf("Item %02d", i+1)
		if limits[i].CategoryName != want {
			t.Fatalf("Home limit order at %d = %q, want %q", i, limits[i].CategoryName, want)
		}
	}
}
