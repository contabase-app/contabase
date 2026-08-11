package main

import (
	"database/sql"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func TestSeedDemoB2BDualWritesCivilDates(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('seed-user', 'Seed User', 'seed@example.test', 'hash', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('seed-ws', 'Seed Workspace', 'business', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('seed-ws', 'seed-user', 'ADMIN', ?)`, now); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	if err := seedDemoB2B(db, "seed-ws", "seed-user"); err != nil {
		t.Fatalf("seed demo B2B: %v", err)
	}

	rows, err := db.Query(`SELECT date, date_civil, due_date, due_date_civil FROM transactions WHERE workspace_id = 'seed-ws'`)
	if err != nil {
		t.Fatalf("query seeded transactions: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var date int64
		var dateCivil string
		var dueDate sql.NullInt64
		var dueCivil sql.NullString
		if err := rows.Scan(&date, &dateCivil, &dueDate, &dueCivil); err != nil {
			t.Fatalf("scan seeded transaction: %v", err)
		}
		parsedDate, parsedCivil, err := parseSeedDateStrict(dateCivil)
		if err != nil {
			t.Fatalf("parse seeded date %q: %v", dateCivil, err)
		}
		if date != parsedDate || dateCivil != parsedCivil {
			t.Fatalf("date pair diverged: %d/%q want %d/%q", date, dateCivil, parsedDate, parsedCivil)
		}
		if dueDate.Valid != dueCivil.Valid {
			t.Fatalf("due pair nullability diverged: %v/%d %v/%q", dueDate.Valid, dueDate.Int64, dueCivil.Valid, dueCivil.String)
		}
		if dueCivil.Valid {
			parsedDue, parsedDueCivil, err := parseSeedDateStrict(dueCivil.String)
			if err != nil {
				t.Fatalf("parse seeded due date %q: %v", dueCivil.String, err)
			}
			if dueDate.Int64 != parsedDue || dueCivil.String != parsedDueCivil {
				t.Fatalf("due pair diverged: %d/%q want %d/%q", dueDate.Int64, dueCivil.String, parsedDue, parsedDueCivil)
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate seeded transactions: %v", err)
	}
	if count != 12 {
		t.Fatalf("seeded transactions = %d, want 12", count)
	}
}

func TestParseSeedDateStrictRejectsInvalidCivilDate(t *testing.T) {
	if _, _, err := parseSeedDateStrict("2026-02-30"); err == nil {
		t.Fatal("invalid civil date accepted")
	}
}
