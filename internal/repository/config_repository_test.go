package repository

import (
	"testing"

	"github.com/contabase-app/contabase/internal/database"
)

func TestCategoriesByWorkspaceNormalizesLegacyMacroGroups(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, type) VALUES ('ws-config', 'Config WS', 'personal')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES
			('cat-root', 'ws-config', 'Root', 'tag', '#6b7280', 'EXPENSE', 'ESSENTIAL', NULL, unixepoch()),
			('cat-child', 'ws-config', 'Child', 'tag', '#6b7280', 'EXPENSE', 'LIFESTYLE', 'cat-root', unixepoch())
	`); err != nil {
		t.Fatalf("insert categories: %v", err)
	}

	rows, err := NewConfigRepository(db).CategoriesByWorkspace("ws-config")
	if err != nil {
		t.Fatalf("CategoriesByWorkspace: %v", err)
	}
	got := map[string]ConfigCategory{}
	for _, row := range rows {
		got[row.ID] = row
	}
	if got["cat-root"].MacroGroup != "Essencial" {
		t.Fatalf("root MacroGroup = %q, want Essencial", got["cat-root"].MacroGroup)
	}
	if got["cat-child"].MacroGroup != "Estilo de Vida" {
		t.Fatalf("child MacroGroup = %q, want Estilo de Vida", got["cat-child"].MacroGroup)
	}
	if got["cat-child"].EffectiveMac != "Estilo de Vida" {
		t.Fatalf("child EffectiveMac = %q, want Estilo de Vida", got["cat-child"].EffectiveMac)
	}
}
