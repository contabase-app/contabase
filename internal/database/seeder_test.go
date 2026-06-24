package database

import (
	"database/sql"
	"sort"
	"strings"
	"testing"
)

type seededCategory struct {
	ID         string
	Name       string
	Type       string
	MacroGroup string
	ParentID   string
	ParentName string
}

func TestSeederPersonalWorkspace(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-personal-1"
	workspaceType := "personal"

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Test Workspace', ?)`, workspaceID, workspaceType); err != nil {
		t.Fatalf("failed to insert dummy workspace: %v", err)
	}

	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx failed: %v", err)
	}
	if err := SeedWorkspaceAccountsTx(tx, workspaceID, workspaceType); err != nil {
		t.Fatalf("SeedWorkspaceAccountsTx failed: %v", err)
	}

	var catCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, workspaceID).Scan(&catCount); err != nil {
		t.Fatalf("failed to count categories: %v", err)
	}
	if catCount == 0 {
		t.Fatalf("expected categories to be seeded, got 0")
	}

	var acctCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type = 'CHECKING'`, workspaceID).Scan(&acctCount); err != nil {
		t.Fatalf("failed to count checking accounts: %v", err)
	}
	if acctCount != 0 {
		t.Errorf("expected no default checking accounts, got %d", acctCount)
	}

	var cardCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD'`, workspaceID).Scan(&cardCount); err != nil {
		t.Fatalf("failed to count credit card accounts: %v", err)
	}
	if cardCount != 0 {
		t.Fatalf("expected no default credit card accounts, got %d", cardCount)
	}

	var cardDetailsCount int
	if err := tx.QueryRow(`
		SELECT COUNT(1) FROM credit_cards cc 
		JOIN accounts a ON a.id = cc.account_id 
		WHERE a.workspace_id = ?`, workspaceID).Scan(&cardDetailsCount); err != nil {
		t.Fatalf("failed to count credit cards details: %v", err)
	}
	if cardDetailsCount != 0 {
		t.Fatalf("expected no default credit cards details, got %d", cardDetailsCount)
	}

	// Test idempotent execution
	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx (re-run) failed: %v", err)
	}
	if err := SeedWorkspaceAccountsTx(tx, workspaceID, workspaceType); err != nil {
		t.Fatalf("SeedWorkspaceAccountsTx (re-run) failed: %v", err)
	}

	var catCountAfter int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, workspaceID).Scan(&catCountAfter); err != nil {
		t.Fatalf("failed to count categories after re-run: %v", err)
	}
	if catCountAfter != catCount {
		t.Fatalf("expected categories count to remain %d, got %d", catCount, catCountAfter)
	}

	var acctCountAfter int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type = 'CHECKING'`, workspaceID).Scan(&acctCountAfter); err != nil {
		t.Fatalf("failed to count checking accounts after re-run: %v", err)
	}
	if acctCountAfter != acctCount {
		t.Fatalf("expected checking accounts count to remain %d, got %d", acctCount, acctCountAfter)
	}
}

func TestSeederBusinessWorkspace(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-business-1"
	workspaceType := "business"

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Test Workspace Business', ?)`, workspaceID, workspaceType); err != nil {
		t.Fatalf("failed to insert dummy workspace: %v", err)
	}

	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx failed: %v", err)
	}
	if err := SeedWorkspaceAccountsTx(tx, workspaceID, workspaceType); err != nil {
		t.Fatalf("SeedWorkspaceAccountsTx failed: %v", err)
	}

	// Verify categories macro groups logic
	var invalidMacroCount int
	err = tx.QueryRow(`
		SELECT COUNT(1) FROM categories 
		WHERE workspace_id = ? 
		AND macro_group NOT IN (
			'Receitas Operacionais',
			'Deduções/Impostos',
			'Custos Operacionais',
			'Despesas Administrativas',
			'Despesas Comerciais',
			'Equipe e Prestadores',
			'Financeiro',
			'Investimentos/Outros'
		) AND parent_id IS NULL
	`, workspaceID).Scan(&invalidMacroCount)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("failed to check macro groups: %v", err)
	}
	if invalidMacroCount > 0 {
		t.Fatalf("expected 0 invalid macro groups in business seed, got %d", invalidMacroCount)
	}

	var accountCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ?`, workspaceID).Scan(&accountCount); err != nil {
		t.Fatalf("failed to count business accounts: %v", err)
	}
	if accountCount != 0 {
		t.Fatalf("expected no default business accounts or cards, got %d", accountCount)
	}
}

func TestSeederPersonalCanonicalCategoryTree(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-personal-canonical"
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Personal Canonical', 'personal')`, workspaceID); err != nil {
		t.Fatalf("failed to insert workspace: %v", err)
	}
	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, "personal"); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx failed: %v", err)
	}

	categories := querySeededCategories(t, tx, workspaceID)
	assertSeedMacroGroups(t, categories, []string{"Essencial", "Estilo de Vida", "Receitas"})
	assertNoLegacySeedMacroGroups(t, categories)
	assertSeedChildrenMatchParents(t, categories)
	assertNoDuplicateRootNames(t, categories)

	assertSeedParent(t, categories, "Alimentação", "EXPENSE", "Essencial")
	assertSeedParent(t, categories, "Refeições", "EXPENSE", "Estilo de Vida")
	assertSeedParent(t, categories, "Trabalho e Renda", "INCOME", "Receitas")

	assertSeedChild(t, categories, "Saúde", "Farmácia", "EXPENSE", "Essencial")
	assertSeedChild(t, categories, "Pet", "Ração", "EXPENSE", "Essencial")
	assertSeedChild(t, categories, "Lazer Familiar", "Brinquedos pet", "EXPENSE", "Estilo de Vida")
	assertSeedChild(t, categories, "Refeições", "Delivery", "EXPENSE", "Estilo de Vida")
	assertSeedChild(t, categories, "Alimentação", "Supermercado", "EXPENSE", "Essencial")
	assertSeedChildMissing(t, categories, "Pet", "Brinquedos pet")
}

func TestSeederBusinessCanonicalCategoryTree(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-business-canonical"
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Business Canonical', 'business')`, workspaceID); err != nil {
		t.Fatalf("failed to insert workspace: %v", err)
	}
	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, "business"); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx failed: %v", err)
	}

	categories := querySeededCategories(t, tx, workspaceID)
	assertSeedMacroGroups(t, categories, []string{
		"Custos Operacionais",
		"Despesas Administrativas",
		"Despesas Comerciais",
		"Deduções/Impostos",
		"Equipe e Prestadores",
		"Financeiro",
		"Investimentos/Outros",
		"Receitas Operacionais",
	})
	assertNoLegacySeedMacroGroups(t, categories)
	assertSeedChildrenMatchParents(t, categories)
	assertNoDuplicateRootNames(t, categories)

	assertSeedParent(t, categories, "Vendas", "INCOME", "Receitas Operacionais")
	assertSeedParent(t, categories, "Inteligência Artificial", "EXPENSE", "Custos Operacionais")
	assertSeedParent(t, categories, "Sistemas e Produtividade", "EXPENSE", "Despesas Administrativas")
	assertSeedParent(t, categories, "Marketing e Vendas", "EXPENSE", "Despesas Comerciais")
	assertSeedParent(t, categories, "Equipe", "EXPENSE", "Equipe e Prestadores")
	assertSeedParent(t, categories, "Financeiro", "EXPENSE", "Financeiro")

	assertSeedChild(t, categories, "Inteligência Artificial", "APIs de IA", "EXPENSE", "Custos Operacionais")
	assertSeedChild(t, categories, "Sistemas e Produtividade", "IA administrativa", "EXPENSE", "Despesas Administrativas")
	assertSeedChild(t, categories, "Marketing e Vendas", "IA para marketing", "EXPENSE", "Despesas Comerciais")
}

func TestWorkspaceCategoryReseedDryRunReportsWithoutDeleting(t *testing.T) {
	db := openSeederTestDB(t)
	defer db.Close()
	seedReseedWorkspace(t, db, "ws-reseed-dry", "personal")
	insertReseedCategory(t, db, "old-root", "ws-reseed-dry", "Legado Pai", "EXPENSE", "LIFESTYLE", "")
	insertReseedCategory(t, db, "old-child", "ws-reseed-dry", "Legado Filho", "EXPENSE", "LIFESTYLE", "old-root")
	insertOrphanReseedCategory(t, db, "orphan-unused", "ws-reseed-dry", "Orfa Sem Uso", "EXPENSE", "LIFESTYLE", "missing-parent")

	before := countReseedCategories(t, db, "ws-reseed-dry")
	dryRun, err := DryRunWorkspaceCategoryReseed(db, "ws-reseed-dry")
	if err != nil {
		t.Fatalf("dry-run reseed: %v", err)
	}
	afterDryRun := countReseedCategories(t, db, "ws-reseed-dry")
	if before != afterDryRun {
		t.Fatalf("dry-run changed category count: before=%d after=%d", before, afterDryRun)
	}
	if dryRun.Applied {
		t.Fatalf("dry-run report marked as applied")
	}
	assertReseedReportHasItem(t, dryRun.RemoveCandidates, "old-root")
	assertReseedReportHasItem(t, dryRun.RemoveCandidates, "old-child")
	assertReseedReportHasItem(t, dryRun.RemoveCandidates, "orphan-unused")
	assertReseedRemovalOrder(t, dryRun.RemoveCandidates, "old-child", "old-root")
	assertReseedReportHasCategory(t, dryRun.CanonicalCreated, "Alimentação", "")

	applied, err := ApplyWorkspaceCategoryReseed(db, "ws-reseed-dry")
	if err != nil {
		t.Fatalf("apply reseed: %v", err)
	}
	assertSameReseedIDs(t, dryRun.RemoveCandidates, applied.RemoveCandidates)
	assertReseedCategoryMissing(t, db, "old-root")
	assertReseedCategoryMissing(t, db, "old-child")
	assertReseedCategoryMissing(t, db, "orphan-unused")
}

func TestWorkspaceCategoryReseedApplyPreservesUsageDependenciesAndOrphans(t *testing.T) {
	db := openSeederTestDB(t)
	defer db.Close()
	seedReseedWorkspace(t, db, "ws-reseed-preserve", "personal")
	seedReseedLedgerBase(t, db, "ws-reseed-preserve")

	insertReseedCategory(t, db, "unused-root", "ws-reseed-preserve", "Sem Uso", "EXPENSE", "LIFESTYLE", "")
	insertReseedCategory(t, db, "used-tx", "ws-reseed-preserve", "Usada Lancamento", "EXPENSE", "LIFESTYLE", "")
	insertReseedTransaction(t, db, "tx-used", "ws-reseed-preserve", "used-tx")
	insertReseedCategory(t, db, "parent-with-used-child", "ws-reseed-preserve", "Pai Com Filho Usado", "EXPENSE", "LIFESTYLE", "")
	insertReseedCategory(t, db, "used-child", "ws-reseed-preserve", "Filho Usado", "EXPENSE", "LIFESTYLE", "parent-with-used-child")
	insertReseedTransaction(t, db, "tx-child", "ws-reseed-preserve", "used-child")
	insertReseedCategory(t, db, "limit-cat", "ws-reseed-preserve", "Categoria Limite", "EXPENSE", "LIFESTYLE", "")
	insertReseedCostLimit(t, db, "limit-1", "ws-reseed-preserve", "limit-cat")
	insertReseedCategory(t, db, "box-cat", "ws-reseed-preserve", "Categoria Caixinha", "EXPENSE", "LIFESTYLE", "")
	insertReseedBox(t, db, "box-1", "ws-reseed-preserve", "box-cat")
	insertReseedCategory(t, db, "rule-cat", "ws-reseed-preserve", "Categoria Recorrencia", "EXPENSE", "LIFESTYLE", "")
	insertReseedRecurringRule(t, db, "rule-1", "ws-reseed-preserve", "rule-cat")
	insertOrphanReseedCategory(t, db, "orphan-used", "ws-reseed-preserve", "Orfa Usada", "EXPENSE", "LIFESTYLE", "missing-parent")
	insertReseedTransaction(t, db, "tx-orphan", "ws-reseed-preserve", "orphan-used")
	insertOrphanReseedCategory(t, db, "orphan-unused-apply", "ws-reseed-preserve", "Orfa Sem Uso Apply", "EXPENSE", "LIFESTYLE", "missing-parent")

	report, err := ApplyWorkspaceCategoryReseed(db, "ws-reseed-preserve")
	if err != nil {
		t.Fatalf("apply reseed: %v", err)
	}

	assertReseedCategoryMissing(t, db, "unused-root")
	assertReseedCategoryMissing(t, db, "orphan-unused-apply")
	assertReseedCategoryExists(t, db, "used-tx")
	assertReseedCategoryExists(t, db, "parent-with-used-child")
	assertReseedCategoryExists(t, db, "used-child")
	assertReseedCategoryExists(t, db, "limit-cat")
	assertReseedCategoryExists(t, db, "box-cat")
	assertReseedCategoryExists(t, db, "rule-cat")
	assertReseedCategoryExists(t, db, "orphan-used")

	assertReseedReportHasItem(t, report.PreservedByUsage, "used-tx")
	assertReseedReportHasItem(t, report.PreservedByUsage, "used-child")
	assertReseedReportHasItem(t, report.PreservedByUsage, "rule-cat")
	assertReseedReportHasItem(t, report.PreservedByDependency, "parent-with-used-child")
	assertReseedReportHasItem(t, report.PreservedByDependency, "limit-cat")
	assertReseedReportHasItem(t, report.PreservedByDependency, "box-cat")
	assertReseedReportHasItem(t, report.Conflicts, "orphan-used")
	assertSeedParent(t, querySeededCategoriesFromDB(t, db, "ws-reseed-preserve"), "Refeições", "EXPENSE", "Estilo de Vida")
}

func TestWorkspaceCategoryReseedBusinessIsIdempotentAndIsolated(t *testing.T) {
	db := openSeederTestDB(t)
	defer db.Close()
	seedReseedWorkspace(t, db, "ws-reseed-business", "business")
	seedReseedWorkspace(t, db, "ws-reseed-other", "personal")
	insertReseedCategory(t, db, "old-business", "ws-reseed-business", "Legado Business", "EXPENSE", "OPERATING_COSTS", "")
	insertReseedCategory(t, db, "old-other", "ws-reseed-other", "Legado Outro Workspace", "EXPENSE", "LIFESTYLE", "")

	report, err := ApplyWorkspaceCategoryReseed(db, "ws-reseed-business")
	if err != nil {
		t.Fatalf("apply business reseed: %v", err)
	}
	if !report.Applied || report.WorkspaceType != "business" {
		t.Fatalf("unexpected report metadata: applied=%v type=%q", report.Applied, report.WorkspaceType)
	}
	assertReseedCategoryMissing(t, db, "old-business")
	assertReseedCategoryExists(t, db, "old-other")
	categories := querySeededCategoriesFromDB(t, db, "ws-reseed-business")
	assertSeedParent(t, categories, "Inteligência Artificial", "EXPENSE", "Custos Operacionais")
	assertSeedChild(t, categories, "Inteligência Artificial", "APIs de IA", "EXPENSE", "Custos Operacionais")

	countAfterFirstApply := countReseedCategories(t, db, "ws-reseed-business")
	second, err := ApplyWorkspaceCategoryReseed(db, "ws-reseed-business")
	if err != nil {
		t.Fatalf("reapply business reseed: %v", err)
	}
	countAfterSecondApply := countReseedCategories(t, db, "ws-reseed-business")
	if countAfterSecondApply != countAfterFirstApply {
		t.Fatalf("reapply changed category count: first=%d second=%d", countAfterFirstApply, countAfterSecondApply)
	}
	if len(second.RemoveCandidates) != 0 || len(second.CanonicalCreated) != 0 {
		t.Fatalf("reapply should not remove/create categories, got removed=%d created=%d", len(second.RemoveCandidates), len(second.CanonicalCreated))
	}
	assertNoDuplicateReseedCategories(t, db, "ws-reseed-business")
	assertReseedCategoryExists(t, db, "old-other")
}

func TestWorkspaceCategoryReseedApplyRollsBackOnSeedError(t *testing.T) {
	db := openSeederTestDB(t)
	defer db.Close()
	seedReseedWorkspace(t, db, "ws-reseed-rollback", "personal")
	insertReseedCategory(t, db, "old-rollback", "ws-reseed-rollback", "Legado Rollback", "EXPENSE", "LIFESTYLE", "")
	if _, err := db.Exec(`
		CREATE TRIGGER fail_reseed_insert
		BEFORE INSERT ON categories
		WHEN NEW.workspace_id = 'ws-reseed-rollback' AND NEW.name = 'Trabalho e Renda'
		BEGIN
			SELECT RAISE(ABORT, 'forced reseed failure');
		END
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if _, err := ApplyWorkspaceCategoryReseed(db, "ws-reseed-rollback"); err == nil {
		t.Fatalf("expected apply reseed to fail")
	}
	assertReseedCategoryExists(t, db, "old-rollback")
}

func TestSeederTypesAreConsistent(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-check"

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name) VALUES (?, 'Test Workspace Check')`, workspaceID); err != nil {
		t.Fatalf("failed to insert dummy workspace: %v", err)
	}

	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, "business"); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx failed: %v", err)
	}

	// Parent type must match child type
	rows, err := tx.Query(`
		SELECT c.id, c.type, p.type 
		FROM categories c 
		JOIN categories p ON c.parent_id = p.id 
		WHERE c.workspace_id = ?
	`, workspaceID)
	if err != nil {
		t.Fatalf("failed to query categories: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, cType, pType string
		if err := rows.Scan(&id, &cType, &pType); err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}
		if cType != pType {
			t.Fatalf("category type mismatch: child %s (%s) vs parent (%s)", id, cType, pType)
		}
		if cType != "INCOME" && cType != "EXPENSE" {
			t.Fatalf("invalid category type: %s", cType)
		}
	}
}

func TestSeederPartialExistingCategories(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-partial-cat"
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Test Workspace', 'personal')`, workspaceID); err != nil {
		t.Fatalf("failed to insert dummy workspace: %v", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, is_fixed, created_at)
		VALUES ('cat-1', ?, 'Custom Cat', 'tag', '#000000', 'EXPENSE', 0, unixepoch())
	`, workspaceID); err != nil {
		t.Fatalf("failed to insert existing category: %v", err)
	}

	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, "personal"); err != nil {
		t.Fatalf("SeedWorkspaceCategoriesTx failed: %v", err)
	}

	var catCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, workspaceID).Scan(&catCount); err != nil {
		t.Fatalf("failed to count categories: %v", err)
	}
	if catCount != 1 {
		t.Fatalf("expected exactly 1 category, got %d", catCount)
	}
}

func TestSeederPartialExistingAccounts(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-partial-acc"
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Test Workspace', 'personal')`, workspaceID); err != nil {
		t.Fatalf("failed to insert dummy workspace: %v", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO accounts (id, workspace_id, name, type, color, provider_slug, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-1', ?, 'Custom Account', 'CHECKING', '#000000', 'default', 0, 0, unixepoch(), unixepoch())
	`, workspaceID); err != nil {
		t.Fatalf("failed to insert existing account: %v", err)
	}

	if err := SeedWorkspaceAccountsTx(tx, workspaceID, "personal"); err != nil {
		t.Fatalf("SeedWorkspaceAccountsTx failed: %v", err)
	}

	var acctCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD'`, workspaceID).Scan(&acctCount); err != nil {
		t.Fatalf("failed to count checking accounts: %v", err)
	}
	if acctCount != 1 {
		t.Fatalf("expected exactly 1 checking account, got %d", acctCount)
	}

	var cardCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD'`, workspaceID).Scan(&cardCount); err != nil {
		t.Fatalf("failed to count credit card accounts: %v", err)
	}
	if cardCount != 0 {
		t.Fatalf("expected no credit cards to be seeded, got %d", cardCount)
	}
}

func TestSeederPartialExistingCards(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory database: %v", err)
	}
	defer db.Close()

	workspaceID := "ws-partial-card"
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Test Workspace', 'personal')`, workspaceID); err != nil {
		t.Fatalf("failed to insert dummy workspace: %v", err)
	}

	if _, err := tx.Exec(`
		INSERT INTO accounts (id, workspace_id, name, type, color, provider_slug, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-1', ?, 'Custom Card', 'CREDIT_CARD', '#000000', 'default', 0, 0, unixepoch(), unixepoch())
	`, workspaceID); err != nil {
		t.Fatalf("failed to insert existing card account: %v", err)
	}

	if err := SeedWorkspaceAccountsTx(tx, workspaceID, "personal"); err != nil {
		t.Fatalf("SeedWorkspaceAccountsTx failed: %v", err)
	}

	var acctCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD'`, workspaceID).Scan(&acctCount); err != nil {
		t.Fatalf("failed to count checking accounts: %v", err)
	}
	if acctCount != 0 {
		t.Errorf("expected no checking accounts to be seeded, got %d", acctCount)
	}

	var cardCount int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD'`, workspaceID).Scan(&cardCount); err != nil {
		t.Fatalf("failed to count credit card accounts: %v", err)
	}
	if cardCount != 1 {
		t.Fatalf("expected exactly 1 credit card account, got %d", cardCount)
	}
}

func querySeededCategories(t *testing.T, tx *sql.Tx, workspaceID string) []seededCategory {
	t.Helper()
	rows, err := tx.Query(`
		SELECT c.id, c.name, c.type, COALESCE(c.macro_group, ''), COALESCE(c.parent_id, ''), COALESCE(p.name, '')
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.workspace_id = ?
	`, workspaceID)
	if err != nil {
		t.Fatalf("query categories: %v", err)
	}
	defer rows.Close()

	var categories []seededCategory
	for rows.Next() {
		var c seededCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.MacroGroup, &c.ParentID, &c.ParentName); err != nil {
			t.Fatalf("scan category: %v", err)
		}
		categories = append(categories, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate categories: %v", err)
	}
	return categories
}

func assertSeedMacroGroups(t *testing.T, categories []seededCategory, want []string) {
	t.Helper()
	seen := make(map[string]struct{})
	for _, category := range categories {
		seen[category.MacroGroup] = struct{}{}
	}
	var got []string
	for macroGroup := range seen {
		got = append(got, macroGroup)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("macro groups = %#v, want %#v", got, want)
	}
}

func assertNoLegacySeedMacroGroups(t *testing.T, categories []seededCategory) {
	t.Helper()
	legacy := map[string]bool{
		"ESSENTIAL":       true,
		"LIFESTYLE":       true,
		"OPERATING_COSTS": true,
	}
	for _, category := range categories {
		if legacy[strings.ToUpper(category.MacroGroup)] {
			t.Fatalf("category %s uses legacy macro_group %q", category.Name, category.MacroGroup)
		}
	}
}

func assertSeedChildrenMatchParents(t *testing.T, categories []seededCategory) {
	t.Helper()
	byID := make(map[string]seededCategory)
	for _, category := range categories {
		byID[category.ID] = category
	}
	for _, child := range categories {
		if child.ParentID == "" {
			continue
		}
		parent, ok := byID[child.ParentID]
		if !ok {
			t.Fatalf("child %s references missing parent %s", child.Name, child.ParentID)
		}
		if parent.ParentID != "" {
			t.Fatalf("child %s references non-root parent %s", child.Name, parent.Name)
		}
		if child.Type != parent.Type {
			t.Fatalf("child %s type = %s, parent %s type = %s", child.Name, child.Type, parent.Name, parent.Type)
		}
		if child.MacroGroup != parent.MacroGroup {
			t.Fatalf("child %s macro_group = %q, parent %s macro_group = %q", child.Name, child.MacroGroup, parent.Name, parent.MacroGroup)
		}
	}
}

func assertNoDuplicateRootNames(t *testing.T, categories []seededCategory) {
	t.Helper()
	roots := make(map[string]int)
	for _, category := range categories {
		if category.ParentID == "" {
			roots[category.Name]++
		}
	}
	for name, count := range roots {
		if count > 1 {
			t.Fatalf("root category %q appears %d times", name, count)
		}
	}
}

func assertSeedParent(t *testing.T, categories []seededCategory, name, typ, macroGroup string) {
	t.Helper()
	for _, category := range categories {
		if category.ParentID == "" && category.Name == name {
			if category.Type != typ || category.MacroGroup != macroGroup {
				t.Fatalf("parent %q = type %q macro %q, want type %q macro %q", name, category.Type, category.MacroGroup, typ, macroGroup)
			}
			return
		}
	}
	t.Fatalf("parent category %q not found", name)
}

func assertSeedChild(t *testing.T, categories []seededCategory, parentName, childName, typ, macroGroup string) {
	t.Helper()
	for _, category := range categories {
		if category.ParentName == parentName && category.Name == childName {
			if category.Type != typ || category.MacroGroup != macroGroup {
				t.Fatalf("child %q/%q = type %q macro %q, want type %q macro %q", parentName, childName, category.Type, category.MacroGroup, typ, macroGroup)
			}
			return
		}
	}
	t.Fatalf("child category %q/%q not found", parentName, childName)
}

func assertSeedChildMissing(t *testing.T, categories []seededCategory, parentName, childName string) {
	t.Helper()
	for _, category := range categories {
		if category.ParentName == parentName && category.Name == childName {
			t.Fatalf("child category %q unexpectedly found under %q", childName, parentName)
		}
	}
}

func openSeederTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func seedReseedWorkspace(t *testing.T, db *sql.DB, workspaceID, workspaceType string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES (?, ?, ?, unixepoch(), unixepoch())`, workspaceID, workspaceID, workspaceType); err != nil {
		t.Fatalf("insert workspace %s: %v", workspaceID, err)
	}
}

func seedReseedLedgerBase(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	userID := "user-" + workspaceID
	accountID := "account-" + workspaceID
	if _, err := db.Exec(`INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES (?, ?, ?, 'hash', 'active', unixepoch(), unixepoch())`, userID, userID, userID+"@example.test"); err != nil {
		t.Fatalf("insert reseed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', unixepoch())`, workspaceID, userID); err != nil {
		t.Fatalf("insert reseed workspace member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Conta Teste', 'CHECKING', 0, 0, unixepoch(), unixepoch())`, accountID, workspaceID); err != nil {
		t.Fatalf("insert reseed account: %v", err)
	}
}

func insertReseedCategory(t *testing.T, db *sql.DB, id, workspaceID, name, typ, macroGroup, parentID string) {
	t.Helper()
	if parentID == "" {
		if _, err := db.Exec(`
			INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
			VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, NULL, unixepoch())
		`, id, workspaceID, name, typ, macroGroup); err != nil {
			t.Fatalf("insert category %s: %v", id, err)
		}
		return
	}
	if _, err := db.Exec(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, ?, unixepoch())
	`, id, workspaceID, name, typ, macroGroup, parentID); err != nil {
		t.Fatalf("insert category %s: %v", id, err)
	}
}

func insertOrphanReseedCategory(t *testing.T, db *sql.DB, id, workspaceID, name, typ, macroGroup, parentID string) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys for orphan fixture: %v", err)
	}
	insertReseedCategory(t, db, id, workspaceID, name, typ, macroGroup, parentID)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable foreign keys after orphan fixture: %v", err)
	}
}

func insertReseedTransaction(t *testing.T, db *sql.DB, id, workspaceID, categoryID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', 1000, unixepoch(), 'Teste reseed', 'paid', unixepoch(), unixepoch())
	`, id, workspaceID, "user-"+workspaceID, "account-"+workspaceID, categoryID); err != nil {
		t.Fatalf("insert reseed transaction %s: %v", id, err)
	}
}

func insertReseedRecurringRule(t *testing.T, db *sql.DB, id, workspaceID, categoryID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', 1000, 'Teste regra', unixepoch(), 'MONTHLY', 'PENDING', 1, unixepoch(), unixepoch())
	`, id, workspaceID, "user-"+workspaceID, "account-"+workspaceID, categoryID); err != nil {
		t.Fatalf("insert reseed recurring rule %s: %v", id, err)
	}
}

func insertReseedCostLimit(t *testing.T, db *sql.DB, id, workspaceID, categoryID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES (?, ?, ?, 10000)`, id, workspaceID, categoryID); err != nil {
		t.Fatalf("insert reseed cost limit %s: %v", id, err)
	}
}

func insertReseedBox(t *testing.T, db *sql.DB, id, workspaceID, categoryID string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES (?, ?, ?, ?, 10000, 1000, unixepoch(), unixepoch())
	`, id, workspaceID, id, categoryID); err != nil {
		t.Fatalf("insert reseed box %s: %v", id, err)
	}
}

func countReseedCategories(t *testing.T, db *sql.DB, workspaceID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, workspaceID).Scan(&count); err != nil {
		t.Fatalf("count reseed categories: %v", err)
	}
	return count
}

func querySeededCategoriesFromDB(t *testing.T, db *sql.DB, workspaceID string) []seededCategory {
	t.Helper()
	rows, err := db.Query(`
		SELECT c.id, c.name, c.type, COALESCE(c.macro_group, ''), COALESCE(c.parent_id, ''), COALESCE(p.name, '')
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.workspace_id = ?
	`, workspaceID)
	if err != nil {
		t.Fatalf("query db categories: %v", err)
	}
	defer rows.Close()

	var categories []seededCategory
	for rows.Next() {
		var c seededCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.MacroGroup, &c.ParentID, &c.ParentName); err != nil {
			t.Fatalf("scan db category: %v", err)
		}
		categories = append(categories, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate db categories: %v", err)
	}
	return categories
}

func assertReseedReportHasItem(t *testing.T, items []CategoryReseedItem, id string) {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return
		}
	}
	t.Fatalf("report item %q not found in %#v", id, items)
}

func assertReseedReportHasCategory(t *testing.T, items []CategoryReseedItem, name, parentName string) {
	t.Helper()
	for _, item := range items {
		if item.Name == name && item.ParentName == parentName {
			return
		}
	}
	t.Fatalf("report category %q parent %q not found in %#v", name, parentName, items)
}

func assertReseedRemovalOrder(t *testing.T, items []CategoryReseedItem, beforeID, afterID string) {
	t.Helper()
	beforeIndex := -1
	afterIndex := -1
	for i, item := range items {
		if item.ID == beforeID {
			beforeIndex = i
		}
		if item.ID == afterID {
			afterIndex = i
		}
	}
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("expected %s before %s in removal order, got %#v", beforeID, afterID, items)
	}
}

func assertSameReseedIDs(t *testing.T, left, right []CategoryReseedItem) {
	t.Helper()
	leftIDs := reseedItemIDs(left)
	rightIDs := reseedItemIDs(right)
	if strings.Join(leftIDs, "|") != strings.Join(rightIDs, "|") {
		t.Fatalf("reseed ids differ: left=%#v right=%#v", leftIDs, rightIDs)
	}
}

func reseedItemIDs(items []CategoryReseedItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	sort.Strings(out)
	return out
}

func assertReseedCategoryExists(t *testing.T, db *sql.DB, categoryID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE id = ?`, categoryID).Scan(&count); err != nil {
		t.Fatalf("count category %s: %v", categoryID, err)
	}
	if count != 1 {
		t.Fatalf("expected category %s to exist, got count %d", categoryID, count)
	}
}

func assertReseedCategoryMissing(t *testing.T, db *sql.DB, categoryID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE id = ?`, categoryID).Scan(&count); err != nil {
		t.Fatalf("count category %s: %v", categoryID, err)
	}
	if count != 0 {
		t.Fatalf("expected category %s to be missing, got count %d", categoryID, count)
	}
}

func assertNoDuplicateReseedCategories(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT c.name, c.type, COALESCE(c.macro_group, ''), COALESCE(c.parent_id, ''), COUNT(1)
		FROM categories c
		WHERE c.workspace_id = ?
		GROUP BY c.name, c.type, COALESCE(c.macro_group, ''), COALESCE(c.parent_id, '')
		HAVING COUNT(1) > 1
	`, workspaceID)
	if err != nil {
		t.Fatalf("query duplicate categories: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, typ, macroGroup, parentID string
		var count int
		if err := rows.Scan(&name, &typ, &macroGroup, &parentID, &count); err != nil {
			t.Fatalf("scan duplicate category: %v", err)
		}
		t.Fatalf("duplicate category found: name=%q type=%q macro=%q parent=%q count=%d", name, typ, macroGroup, parentID, count)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate duplicate categories: %v", err)
	}
}
