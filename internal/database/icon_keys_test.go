package database

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/icons"
)

func openIconTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Pré-66 real: aplica somente as versões anteriores e mantém 66 pendente.
func openPre66IconTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre66.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	execTestDB(t, db, ddl)
	for _, m := range migrations {
		if m.version >= 66 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.up(tx); err != nil {
			_ = tx.Rollback()
			t.Fatalf("migration %d: %v", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seedAppliedMigrationsBefore(t, db, 66)
	return db, path
}

func seedIconWorkspace(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestDB(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('icon-user', 'Icon User', 'icon@example.com', 'hash', ?, ?)
	`, now, now)
	execTestDB(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES (?, 'Icon Workspace', '', 'personal', ?, ?)
	`, workspaceID, now, now)
}

func insertIconCategory(t *testing.T, db *sql.DB, workspaceID, id, name, icon string) {
	t.Helper()
	execTestDB(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES (?, ?, ?, ?, '#6b7280', 'EXPENSE', ?)
	`, id, workspaceID, name, icon, time.Now().Unix())
}

func categoryIconOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var icon string
	if err := db.QueryRow(`SELECT icon FROM categories WHERE id = ?`, id).Scan(&icon); err != nil {
		t.Fatalf("read category icon: %v", err)
	}
	return icon
}

// A migration 66 precisa disponibilizar icon_key nas entidades que passam a
// aceitar escolha de ícone e continuar sendo reaplicável sem efeito colateral.
func TestMigration66AddsIconKeys(t *testing.T) {
	db := openIconTestDB(t)

	assertColumnExists(t, db, "cost_limits", "icon_key")
	assertColumnExists(t, db, "boxes", "icon_key")
	assertColumnExists(t, db, "categories", "icon")

	var name string
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version = 66`).Scan(&name); err != nil {
		t.Fatalf("migration 66 não registrada: %v", err)
	}
	if name != "icon_keys_for_categories_limits_and_boxes" {
		t.Fatalf("nome da migration 66 = %q", name)
	}

	// Reexecutar as migrations em um banco já migrado não pode quebrar.
	if err := runMigrations(db); err != nil {
		t.Fatalf("reexecução das migrations falhou: %v", err)
	}
	count := 0
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = 66`).Scan(&count); err != nil {
		t.Fatalf("recontar migration 66: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration 66 registrada %d vezes", count)
	}
}

// icon_key nasce nullable: "sem escolha" precisa ser distinguível de escolha.
func TestMigration66IconKeysStartEmptyForExistingRows(t *testing.T) {
	db := openIconTestDB(t)
	seedIconWorkspace(t, db, "ws-icons")
	now := time.Now().Unix()

	insertIconCategory(t, db, "ws-icons", "cat-1", "Limite sem ícone próprio", "tag")

	execTestDB(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, created_at, updated_at)
		VALUES ('limit-1', 'ws-icons', 'cat-1', 10000, 80, ?, ?)
	`, now, now)
	execTestDB(t, db, `
		INSERT INTO boxes (id, workspace_id, name, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('box-1', 'ws-icons', 'Reserva', 100000, 0, ?, ?)
	`, now, now)

	var limitIcon, boxIcon sql.NullString
	if err := db.QueryRow(`SELECT icon_key FROM cost_limits WHERE id = 'limit-1'`).Scan(&limitIcon); err != nil {
		t.Fatalf("read limit icon_key: %v", err)
	}
	if err := db.QueryRow(`SELECT icon_key FROM boxes WHERE id = 'box-1'`).Scan(&boxIcon); err != nil {
		t.Fatalf("read box icon_key: %v", err)
	}
	if limitIcon.Valid || boxIcon.Valid {
		t.Fatalf("icon_key deveria nascer NULL: limit=%v box=%v", limitIcon, boxIcon)
	}
}

// Backfill: categorias com o default histórico recebem o ícone derivado do nome.
func TestBackfillCategoryIconsMapsKnownNames(t *testing.T) {
	db, _ := openPre66IconTestDB(t)
	seedIconWorkspace(t, db, "ws-backfill")

	insertIconCategory(t, db, "ws-backfill", "cat-food", "Alimentação", "tag")
	insertIconCategory(t, db, "ws-backfill", "cat-energy", "Energia elétrica", "tag")
	insertIconCategory(t, db, "ws-backfill", "cat-pet", "Pet", "tag")
	insertIconCategory(t, db, "ws-backfill", "cat-empty", "Sem sentido algum", "tag")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := BackfillCategoryIconsTx(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	for id, want := range map[string]string{
		"cat-food":   "utensils",
		"cat-energy": "zap",
		"cat-pet":    "paw-print",
		// Sem mapeamento confiável, o fallback existente é preservado.
		"cat-empty": "tag",
	} {
		if got := categoryIconOf(t, db, id); got != want {
			t.Errorf("ícone de %s = %q, want %q", id, got, want)
		}
	}
}

// O backfill é idempotente: reexecutar não muda mais nada.
func TestBackfillCategoryIconsIsIdempotent(t *testing.T) {
	db, _ := openPre66IconTestDB(t)
	seedIconWorkspace(t, db, "ws-idem")
	insertIconCategory(t, db, "ws-idem", "cat-1", "Alimentação", "tag")
	insertIconCategory(t, db, "ws-idem", "cat-2", "Transporte", "tag")

	run := func() int {
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		updated, err := BackfillCategoryIconsTx(tx)
		if err != nil {
			_ = tx.Rollback()
			t.Fatalf("backfill: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return updated
	}

	first := run()
	if first != 2 {
		t.Fatalf("primeira execução atualizou %d, want 2", first)
	}
	after := map[string]string{"cat-1": categoryIconOf(t, db, "cat-1"), "cat-2": categoryIconOf(t, db, "cat-2")}

	if second := run(); second != 0 {
		t.Fatalf("segunda execução atualizou %d linhas, want 0", second)
	}
	for id, want := range after {
		if got := categoryIconOf(t, db, id); got != want {
			t.Errorf("reexecução alterou %s: %q -> %q", id, want, got)
		}
	}
}

// Escolha manual é irrevogável: o backfill nunca a sobrescreve.
func TestBackfillCategoryIconsNeverOverwritesManualChoice(t *testing.T) {
	db, _ := openPre66IconTestDB(t)
	seedIconWorkspace(t, db, "ws-manual")

	insertIconCategory(t, db, "ws-manual", "cat-manual", "Alimentação", "pizza")
	insertIconCategory(t, db, "ws-manual", "cat-unknown-manual", "Sem sentido algum", "circle")
	insertIconCategory(t, db, "ws-manual", "cat-alias", "Casa", "home")
	insertIconCategory(t, db, "ws-manual", "cat-default", "Alimentação", "tag")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := BackfillCategoryIconsTx(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if got := categoryIconOf(t, db, "cat-manual"); got != "pizza" {
		t.Errorf("escolha manual de 'Alimentação' foi sobrescrita: %q", got)
	}
	if got := categoryIconOf(t, db, "cat-unknown-manual"); got != "circle" {
		t.Errorf("escolha manual fora do catálogo foi sobrescrita: %q", got)
	}
	if got := categoryIconOf(t, db, "cat-alias"); got != "home" {
		t.Errorf("valor legado com alias foi sobrescrito: %q", got)
	}
	if got := categoryIconOf(t, db, "cat-default"); got != "utensils" {
		t.Errorf("default histórico não foi normalizado: %q", got)
	}
}

// Valores vazios também contam como "sem escolha" e recebem o mapeamento.
func TestBackfillCategoryIconsTreatsEmptyAsUnset(t *testing.T) {
	db, _ := openPre66IconTestDB(t)
	seedIconWorkspace(t, db, "ws-empty")
	insertIconCategory(t, db, "ws-empty", "cat-blank", "Transporte", "")
	insertIconCategory(t, db, "ws-empty", "cat-blank2", "Transporte", "   ")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := BackfillCategoryIconsTx(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("backfill: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, id := range []string{"cat-blank", "cat-blank2"} {
		if got := categoryIconOf(t, db, id); got != "bus" {
			t.Errorf("ícone de %s = %q, want bus", id, got)
		}
	}
}

// Novos workspaces já nascem com ícones utilizáveis, e o seeder não recria
// categorias em workspaces existentes.
func TestSeedWorkspaceCategoriesAssignsIcons(t *testing.T) {
	db := openIconTestDB(t)
	seedIconWorkspace(t, db, "ws-seed")

	if err := SeedWorkspaceCategories(db, "ws-seed", "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := db.Query(`SELECT name, icon FROM categories WHERE workspace_id = ?`, "ws-seed")
	if err != nil {
		t.Fatalf("query seeded categories: %v", err)
	}
	defer rows.Close()

	total, mapped := 0, 0
	seen := map[string]string{}
	for rows.Next() {
		var name, icon string
		if err := rows.Scan(&name, &icon); err != nil {
			t.Fatalf("scan: %v", err)
		}
		total++
		seen[name] = icon
		if icon == "" || !icons.IsValid(icon) {
			t.Errorf("categoria semeada %q recebeu ícone inválido %q", name, icon)
			continue
		}
		mapped++
		if icon != icons.CategoryIconOrDefault(name) {
			t.Errorf("categoria %q recebeu %q, want %q", name, icon, icons.CategoryIconOrDefault(name))
		}
	}
	if total == 0 {
		t.Fatal("seeder não criou categorias")
	}
	if mapped != total {
		t.Fatalf("apenas %d de %d categorias semeadas têm ícone válido", mapped, total)
	}

	// Nenhuma categoria pode continuar no default histórico sem mapeamento.
	for name, icon := range seen {
		if icon == icons.DefaultKey && icons.CategoryIconForName(name) != "" {
			t.Errorf("categoria %q ficou no default apesar de ter mapeamento", name)
		}
	}

	// Reexecutar o seeder não duplica nem reescreve ícones escolhidos.
	execTestDB(t, db, `UPDATE categories SET icon = 'pizza' WHERE workspace_id = ? AND name = 'Alimentação'`, "ws-seed")
	if err := SeedWorkspaceCategories(db, "ws-seed", "personal"); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, "ws-seed").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != total {
		t.Fatalf("reseed duplicou categorias: %d -> %d", total, count)
	}
	var foodIcon string
	if err := db.QueryRow(`SELECT icon FROM categories WHERE workspace_id = ? AND name = 'Alimentação'`, "ws-seed").Scan(&foodIcon); err != nil {
		t.Fatalf("read food icon: %v", err)
	}
	if foodIcon != "pizza" {
		t.Fatalf("reseed sobrescreveu escolha manual: %q", foodIcon)
	}
}

// O catálogo não pode conter chaves que o frontend não renderiza: o seed
// grava exatamente o que o Lucide versionado conhece.
func TestSeededIconsExistInCatalog(t *testing.T) {
	keys := map[string]bool{}
	for _, option := range icons.All() {
		keys[option.Key] = true
	}
	for _, tree := range [][]categoryNode{personalCategoryTree(), businessCategoryTree()} {
		for _, parent := range tree {
			for _, name := range append([]string{parent.Name}, parent.Children...) {
				if !keys[icons.CategoryIconOrDefault(name)] {
					t.Errorf("categoria canônica %q mapeia para chave fora do catálogo", name)
				}
			}
		}
	}
}

func TestMigration66ProvenanceAcrossWorkspacesAndBoots(t *testing.T) {
	db, path := openPre66IconTestDB(t)
	seedIconWorkspace(t, db, "ws-a")
	execTestDB(t, db, `INSERT INTO workspaces(id,name) VALUES ('ws-b','Workspace B')`)
	for _, ws := range []string{"ws-a", "ws-b"} {
		insertIconCategory(t, db, ws, ws+"-legacy", "Alimentação", "tag")
		insertIconCategory(t, db, ws, ws+"-manual", "Alimentação", "car")
		insertIconCategory(t, db, ws, ws+"-unknown", "Alimentação", "legacy-custom")
	}
	snapshot := func(db *sql.DB) map[string]string {
		t.Helper()
		rows, err := db.Query(`SELECT workspace_id, id, icon FROM categories ORDER BY workspace_id, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]string{}
		for rows.Next() {
			var ws, id, key string
			if err := rows.Scan(&ws, &id, &key); err != nil {
				t.Fatal(err)
			}
			result[ws+"/"+id] = key
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	t.Logf("pre66: %v", snapshot(db))
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []string{"ws-a", "ws-b"} {
		if got := categoryIconOf(t, db, ws+"-legacy"); got != "utensils" {
			t.Fatalf("%s initial=%s", ws, got)
		}
		if got := categoryIconOf(t, db, ws+"-manual"); got != "car" {
			t.Fatal(got)
		}
		if got := categoryIconOf(t, db, ws+"-unknown"); got != "legacy-custom" {
			t.Fatal(got)
		}
	}
	t.Logf("first migration: %v", snapshot(db))
	execTestDB(t, db, `UPDATE categories SET icon='tag' WHERE id='ws-a-legacy' AND workspace_id='ws-a'`)
	// The other workspace chooses a different key independently.
	execTestDB(t, db, `UPDATE categories SET icon='car' WHERE id='ws-b-legacy' AND workspace_id='ws-b'`)
	want := snapshot(db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	n, err := BackfillCategoryIconsTx(tx)
	if err != nil || n != 0 {
		t.Fatalf("reapply: count=%d err=%v", n, err)
	}
	if err := migrateIconKeys(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(db); !reflect.DeepEqual(got, want) {
		t.Fatalf("reapply: %v want %v", got, want)
	}
	t.Logf("manual + helper/migration reapply: %v; updated=%d", snapshot(db), n)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := snapshot(reopened); !reflect.DeepEqual(got, want) {
		t.Fatalf("second boot: %v want %v", got, want)
	}
	t.Logf("second boot: %v", snapshot(reopened))
}

// Duplicate IDs in a reduced fixture make a missing tenant predicate observable:
// one row's name must never assign the other workspace's derived icon.
func TestMigration66BackfillExplicitWorkspacePredicate(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	execTestDB(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY)`)
	execTestDB(t, db, `CREATE TABLE categories(id TEXT,workspace_id TEXT,name TEXT,icon TEXT,PRIMARY KEY(workspace_id,id))`)
	execTestDB(t, db, `INSERT INTO categories VALUES ('same','a','Alimentação','tag'),('same','b','Transporte','tag')`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	n, err := BackfillCategoryIconsTx(tx)
	if err != nil || n != 2 {
		t.Fatalf("updated=%d err=%v", n, err)
	}
	// "Transporte" mapeia canonicamente para "bus" (category_mapping.go);
	// b!=utensils prova que não herdou o ícone derivado do workspace a.
	for ws, want := range map[string]string{"a": "utensils", "b": "bus"} {
		var got string
		if err := tx.QueryRow(`SELECT icon FROM categories WHERE id='same' AND workspace_id=?`, ws).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("workspace %s: %q want %q", ws, got, want)
		}
	}
}

func TestMigration66BackfillRequiresProvenance(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := BackfillCategoryIconsTx(tx); err == nil {
		t.Fatal("missing version table must fail closed")
	}
}
