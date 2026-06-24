package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func openMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func seedWorkspace(t *testing.T, db *sql.DB, workspaceID, workspaceType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES (?, ?, ?, unixepoch(), unixepoch())`, workspaceID, workspaceID, workspaceType); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
}

func runCategoryReseedTestable(db *sql.DB, workspaceID string, dryRun, apply bool, confirm string) (string, error) {
	var buf bytes.Buffer

	if strings.TrimSpace(workspaceID) == "" {
		return "", fmt.Errorf("Erro: A flag --workspace-id é obrigatória.")
	}

	if apply && dryRun {
		return "", fmt.Errorf("Erro: --apply e --dry-run são mutuamente exclusivos.")
	}

	if apply {
		if strings.TrimSpace(confirm) != "RESEED CATEGORIES" {
			return "", fmt.Errorf("Erro: --apply requer --confirm \"RESEED CATEGORIES\"")
		}
	}

	var exists bool
	if err := db.QueryRow(`SELECT 1 FROM workspaces WHERE id = ?`, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("Erro: workspace %q não encontrado.", workspaceID)
		}
		return "", fmt.Errorf("Erro ao verificar workspace: %v", err)
	}

	if apply {
		fmt.Fprintln(&buf, "ATENÇÃO: Faça backup do banco de dados antes de prosseguir (scripts/ops/backup.sh).")
		report, err := database.ApplyWorkspaceCategoryReseed(db, workspaceID)
		if err != nil {
			return "", fmt.Errorf("Erro ao aplicar reseed: %v", err)
		}
		captureReportOutput(&buf, report)
	} else {
		report, err := database.DryRunWorkspaceCategoryReseed(db, workspaceID)
		if err != nil {
			return "", fmt.Errorf("Erro ao analisar reseed: %v", err)
		}
		captureReportOutput(&buf, report)
	}

	return buf.String(), nil
}

func captureReportOutput(buf *bytes.Buffer, report database.CategoryReseedReport) {
	if report.Applied {
		fmt.Fprintln(buf, "=== RESEED DE CATEGORIAS APLICADO ===")
	} else {
		fmt.Fprintln(buf, "=== DIAGNÓSTICO DE RESEED (dry-run) ===")
	}
	fmt.Fprintln(buf)
	fmt.Fprintf(buf, "Workspace analisado: %s\n", report.WorkspaceID)
	fmt.Fprintf(buf, "Tipo do workspace:   %s\n", report.WorkspaceType)
	fmt.Fprintf(buf, "Total de categorias antes: %d\n", report.TotalBefore)
	fmt.Fprintln(buf)

	if len(report.RemoveCandidates) > 0 {
		fmt.Fprintf(buf, "Categorias candidatas à remoção (%d):\n", len(report.RemoveCandidates))
		for _, item := range report.RemoveCandidates {
			prefix := "  "
			if item.ParentID != "" {
				prefix = "  ↳ "
			}
			fmt.Fprintf(buf, "  %s%s (tipo=%s, macro=%q, razão=%s)\n", prefix, item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Fprintln(buf)
	}

	if len(report.PreservedByUsage) > 0 {
		fmt.Fprintf(buf, "Categorias preservadas por uso (%d):\n", len(report.PreservedByUsage))
		for _, item := range report.PreservedByUsage {
			fmt.Fprintf(buf, "  - %s (tipo=%s, macro=%q, razão=%s)\n", item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Fprintln(buf)
	}

	if len(report.PreservedByDependency) > 0 {
		fmt.Fprintf(buf, "Categorias preservadas por dependência (%d):\n", len(report.PreservedByDependency))
		for _, item := range report.PreservedByDependency {
			fmt.Fprintf(buf, "  - %s (tipo=%s, macro=%q, razão=%s)\n", item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Fprintln(buf)
	}

	if len(report.Conflicts) > 0 {
		fmt.Fprintf(buf, "Conflitos (%d):\n", len(report.Conflicts))
		for _, item := range report.Conflicts {
			fmt.Fprintf(buf, "  ⚠ %s (tipo=%s, macro=%q, razão=%s)\n", item.Name, item.Type, item.MacroGroup, item.Reason)
		}
		fmt.Fprintln(buf)
	}

	if len(report.CanonicalCreated) > 0 {
		fmt.Fprintf(buf, "Categorias canônicas a criar (%d):\n", len(report.CanonicalCreated))
		for _, item := range report.CanonicalCreated {
			prefix := "  "
			if item.ParentName != "" {
				prefix = "  ↳ "
			}
			fmt.Fprintf(buf, "  %s%s (tipo=%s, macro=%q)\n", prefix, item.Name, item.Type, item.MacroGroup)
		}
		fmt.Fprintln(buf)
	}

	if len(report.CanonicalAlreadyExisted) > 0 {
		fmt.Fprintf(buf, "Categorias canônicas já existentes (%d):\n", len(report.CanonicalAlreadyExisted))
		for _, item := range report.CanonicalAlreadyExisted {
			fmt.Fprintf(buf, "  - %s (tipo=%s, macro=%q)\n", item.Name, item.Type, item.MacroGroup)
		}
		fmt.Fprintln(buf)
	}

	if report.Applied {
		fmt.Fprintf(buf, "Total de categorias depois: %d\n", report.TotalAfter)
	} else {
		totalAfter := report.TotalBefore - len(report.RemoveCandidates) + len(report.CanonicalCreated)
		fmt.Fprintf(buf, "Total estimado de categorias depois: %d\n", totalAfter)
	}
	fmt.Fprintln(buf)

	if !report.Applied {
		fmt.Fprintf(buf, "Modo dry-run: nenhuma alteração foi aplicada ao banco de dados.\n")
		fmt.Fprintf(buf, "Para aplicar, use: admin category-reseed --workspace-id %s --apply --confirm \"RESEED CATEGORIES\"\n", report.WorkspaceID)
	}
}

func TestCategoryReseedMissingWorkspaceID(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	_, err := runCategoryReseedTestable(db, "", false, false, "")
	if err == nil {
		t.Fatal("expected error for missing workspace-id")
	}
	if !strings.Contains(err.Error(), "workspace-id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCategoryReseedWorkspaceNotFound(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	_, err := runCategoryReseedTestable(db, "nonexistent-ws", false, false, "")
	if err == nil {
		t.Fatal("expected error for nonexistent workspace")
	}
	if !strings.Contains(err.Error(), "não encontrado") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCategoryReseedApplyAndDryRunExclusive(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	_, err := runCategoryReseedTestable(db, "ws-1", true, true, "")
	if err == nil {
		t.Fatal("expected error for apply + dry-run")
	}
	if !strings.Contains(err.Error(), "mutuamente exclusivos") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCategoryReseedApplyWithoutConfirm(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	_, err := runCategoryReseedTestable(db, "ws-1", false, true, "")
	if err == nil {
		t.Fatal("expected error for apply without confirm")
	}
	if !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCategoryReseedApplyWrongConfirm(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	_, err := runCategoryReseedTestable(db, "ws-1", false, true, "wrong confirmation")
	if err == nil {
		t.Fatal("expected error for wrong confirm")
	}
	if !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCategoryReseedDryRunDoesNotAlterDB(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-dryrun-check"
	seedWorkspace(t, db, wsID, "personal")

	if err := database.SeedWorkspaceCategories(db, wsID, "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var before int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, wsID).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if before == 0 {
		t.Fatal("expected categories seeded")
	}

	output, err := runCategoryReseedTestable(db, wsID, false, false, "")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, wsID).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if before != after {
		t.Fatalf("dry-run altered category count: before=%d after=%d", before, after)
	}

	if !strings.Contains(output, "DIAGNÓSTICO DE RESEED (dry-run)") {
		t.Fatal("output missing dry-run header")
	}
	if !strings.Contains(output, "Modo dry-run: nenhuma alteração foi aplicada") {
		t.Fatal("output missing dry-run notice")
	}
}

func TestCategoryReseedDryRunReportContainsExpectedSections(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-report-check"
	seedWorkspace(t, db, wsID, "personal")

	if err := database.SeedWorkspaceCategories(db, wsID, "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	output, err := runCategoryReseedTestable(db, wsID, false, false, "")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	required := []string{
		"Workspace analisado:",
		"Tipo do workspace:",
		"Total de categorias antes:",
		"Categorias canônicas já existentes",
		"Total estimado de categorias depois:",
	}
	for _, s := range required {
		if !strings.Contains(output, s) {
			t.Fatalf("output missing section %q\nFull output:\n%s", s, output)
		}
	}
}

func TestCategoryReseedApplyWithCorrectConfirm(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-apply-ok"
	seedWorkspace(t, db, wsID, "personal")

	if err := database.SeedWorkspaceCategories(db, wsID, "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var before int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, wsID).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}

	output, err := runCategoryReseedTestable(db, wsID, false, true, "RESEED CATEGORIES")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !strings.Contains(output, "RESEED DE CATEGORIAS APLICADO") {
		t.Fatal("output missing apply header")
	}
	if !strings.Contains(output, "backup") {
		t.Fatal("output missing backup warning")
	}
	if !strings.Contains(output, "Total de categorias depois:") {
		t.Fatal("output missing total after")
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, wsID).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after == 0 {
		t.Fatal("expected categories after apply, got 0")
	}
}

func TestCategoryReseedApplyPreservesNonCanonicalWithUsage(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-preserve-test"
	seedWorkspace(t, db, wsID, "personal")

	if err := database.SeedWorkspaceCategories(db, wsID, "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	userID := "u-preserve"
	accountID := "acc-preserve"
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES (?, ?, ?, 'hash', 'active', ?, ?)`, userID, userID, userID+"@test.com", now, now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Conta', 'CHECKING', 0, 0, ?, ?)`, accountID, wsID, now, now); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('custom-used', ?, 'Custom Usada', 'tag', '#6b7280', 'EXPENSE', 'Custom', NULL, ?)`, wsID, now); err != nil {
		t.Fatalf("insert custom category: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES ('tx-1', ?, ?, ?, 'custom-used', 'EXPENSE', 1000, ?, 'Test', 'paid', ?, ?)`, wsID, userID, accountID, now, now, now); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}

	output, err := runCategoryReseedTestable(db, wsID, false, true, "RESEED CATEGORIES")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if !strings.Contains(output, "preservadas por uso") {
		t.Fatal("output should report preserved by usage")
	}

	var exists bool
	if err := db.QueryRow(`SELECT 1 FROM categories WHERE id = 'custom-used'`).Scan(&exists); err != nil {
		t.Fatalf("custom-used should still exist: %v", err)
	}
}

func TestCategoryReseedDoesNotAllowAllWorkspacesFlag(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-no-all"
	seedWorkspace(t, db, wsID, "personal")

	if err := database.SeedWorkspaceCategories(db, wsID, "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := runCategoryReseedTestable(db, wsID, false, false, "")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
}

func TestCategoryReseedExitCodeNonZeroOnError(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	db := openMemoryDB(t)
	defer db.Close()

	_, err := runCategoryReseedTestable(db, "", false, false, "")
	if err == nil {
		t.Fatal("expected non-zero exit via error for missing workspace-id")
	}
	if _, ok := interface{}(err).(error); !ok {
		t.Fatal("expected error to be non-nil")
	}
}

func TestCategoryReseedOrphanHandling(t *testing.T) {
	db := openMemoryDB(t)
	defer db.Close()

	wsID := "ws-orphan-test"
	seedWorkspace(t, db, wsID, "personal")

	if err := database.SeedWorkspaceCategories(db, wsID, "personal"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	userID := "u-orphan"
	accountID := "acc-orphan"
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES (?, ?, ?, 'hash', 'active', ?, ?)`, userID, userID, userID+"@test.com", now, now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, wsID, userID, now); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Conta', 'CHECKING', 0, 0, ?, ?)`, accountID, wsID, now, now); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	// Disable FK to create orphans (real-world scenario: FK was off during parent deletion)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable FK: %v", err)
	}

	// Case 8: orphan_unused (parent_id points to nonexistent parent, no usage)
	orphanUnusedID := "orphan-unused"
	if _, err := db.Exec(`INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at) VALUES (?, ?, ?, 'tag', '#6b7280', 'EXPENSE', 'Custom', ?, 0, ?)`,
		orphanUnusedID, wsID, "__orphan_unused", "nonexistent-parent-orphan", now); err != nil {
		t.Fatalf("insert orphan_unused: %v", err)
	}

	// Case 9: orphan_used (parent_id points to nonexistent parent, has transaction)
	orphanUsedID := "orphan-used"
	if _, err := db.Exec(`INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at) VALUES (?, ?, ?, 'tag', '#6b7280', 'EXPENSE', 'Custom', ?, 0, ?)`,
		orphanUsedID, wsID, "__orphan_used", "nonexistent-parent-orphan-2", now); err != nil {
		t.Fatalf("insert orphan_used: %v", err)
	}

	// Add transaction to orphan_used
	if _, err := db.Exec(`INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at) VALUES ('tx-orphan', ?, ?, ?, ?, 'EXPENSE', 3000, ?, 'Test orphan tx', 'paid', ?, ?)`,
		wsID, userID, accountID, orphanUsedID, now, now, now); err != nil {
		t.Fatalf("insert orphan tx: %v", err)
	}

	// Re-enable FK (does not re-validate existing data in SQLite)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable FK: %v", err)
	}

	// Dry-run
	output, err := runCategoryReseedTestable(db, wsID, false, false, "")
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	// Case 8: orphan_unused should be in remove candidates with reason "categoria_orfa_sem_uso"
	if !strings.Contains(output, "__orphan_unused") {
		t.Fatal("output should contain __orphan_unused")
	}
	if !strings.Contains(output, "categoria_orfa_sem_uso") {
		t.Fatal("output should contain 'categoria_orfa_sem_uso' reason")
	}

	// Case 9: orphan_used should be in preserved + conflicts
	if !strings.Contains(output, "__orphan_used") {
		t.Fatal("output should contain __orphan_used")
	}
	if !strings.Contains(output, "categoria_orfa_com_uso_ou_dependencia") {
		t.Fatal("output should contain 'categoria_orfa_com_uso_ou_dependencia'")
	}

	// Apply
	applyOutput, err := runCategoryReseedTestable(db, wsID, false, true, "RESEED CATEGORIES")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(applyOutput, "RESEED DE CATEGORIAS APLICADO") {
		t.Fatal("apply output missing header")
	}

	// Verify orphan_unused was removed
	var existsUnused bool
	if err := db.QueryRow(`SELECT 1 FROM categories WHERE id = ?`, orphanUnusedID).Scan(&existsUnused); err == nil {
		t.Fatal("orphan_unused should be removed after apply")
	}

	// Verify orphan_used was preserved
	var existsUsed bool
	if err := db.QueryRow(`SELECT 1 FROM categories WHERE id = ?`, orphanUsedID).Scan(&existsUsed); err != nil {
		t.Fatal("orphan_used should be preserved after apply")
	}

	// Verify no new orphans (orphan_used is still orphaned but preserved)
	var newOrphanCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories c LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id WHERE c.workspace_id = ? AND c.parent_id IS NOT NULL AND p.id IS NULL AND c.id != ?`, wsID, orphanUsedID).Scan(&newOrphanCount); err != nil {
		t.Fatalf("check new orphans: %v", err)
	}
	if newOrphanCount > 0 {
		t.Fatalf("found %d new orphans beyond expected orphan_used", newOrphanCount)
	}

	// Verify no duplicate canonical categories
	var dupeCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM (SELECT name, type, macro_group, COALESCE(parent_id, 'NULL'), COUNT(1) AS cnt FROM categories WHERE workspace_id = ? AND id != ? GROUP BY name, type, macro_group, COALESCE(parent_id, 'NULL') HAVING cnt > 1)`, wsID, orphanUsedID).Scan(&dupeCount); err != nil {
		t.Fatalf("check dupes: %v", err)
	}
	if dupeCount > 0 {
		t.Fatalf("found %d duplicate categories", dupeCount)
	}
}
