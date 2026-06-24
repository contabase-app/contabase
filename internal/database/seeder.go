// Package database — Seeder de catálogos estruturais de Workspace
//
// Este arquivo é responsável por popular categorias estruturais de um
// workspace recém-criado. Ele é invocado automaticamente durante a criação de
// qualquer workspace (setup inicial, registro de usuário e criação
// administrativa via painel).
//
// Regras de segurança que este módulo respeita (ver .docs/AI_RULES.md):
//   - Todo INSERT carrega o workspace_id explícito → Isolamento Multi-Tenant.
//   - Toda a carga é executada dentro de uma única *sql.Tx → Atomicidade.
//   - Categorias filhas recebem o UUID do pai gerado na mesma iteração.
//   - Verificação prévia impede duplicidade em caso de re-execução acidental.
package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// categoryNode representa um nó da árvore de categorias a ser semeado.
// Cada nó é uma Categoria Pai; seus Children são as Subcategorias.
type categoryNode struct {
	Name       string   // Nome da Categoria Pai (exibido na interface)
	Type       string   // "EXPENSE" ou "INCOME"
	MacroGroup string   // Grupo Macro para filtros de DRE/Dashboard
	Children   []string // Nomes das Subcategorias vinculadas a este pai
}

type CategoryReseedItem struct {
	WorkspaceID string
	ID          string
	Name        string
	Type        string
	MacroGroup  string
	ParentID    string
	ParentName  string
	Reason      string
}

type CategoryReseedReport struct {
	WorkspaceID             string
	WorkspaceType           string
	TotalBefore             int
	TotalAfter              int
	RemoveCandidates        []CategoryReseedItem
	PreservedByUsage        []CategoryReseedItem
	PreservedByDependency   []CategoryReseedItem
	CanonicalCreated        []CategoryReseedItem
	CanonicalAlreadyExisted []CategoryReseedItem
	Conflicts               []CategoryReseedItem
	Applied                 bool
}

type categoryReseedRow struct {
	CategoryReseedItem
	ParentMissing      bool
	TransactionCount   int
	RecurringRuleCount int
	CostLimitCount     int
	BoxCount           int
}

// ---------------------------------------------------------------------------
// Ponto de entrada público — aceita *sql.DB e gerencia a transação
// ---------------------------------------------------------------------------

// SeedWorkspaceCategories é a função de alto nível para ser chamada fora de
// uma transação existente. Ela abre uma transação própria, executa o seed e
// faz commit (ou rollback em caso de falha).
//
// Uso recomendado no fluxo de registro de usuário (quando não há tx aberta):
//
//	if err := database.SeedWorkspaceCategories(db, workspaceID, workspaceType); err != nil {
//	    log.Printf("seed falhou: %v", err)
//	}
func SeedWorkspaceCategories(db *sql.DB, workspaceID, workspaceType string) error {
	if db == nil {
		return fmt.Errorf("seeder: db não pode ser nil")
	}
	if workspaceID == "" {
		return fmt.Errorf("seeder: workspaceID não pode ser vazio")
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("seeder: falha ao abrir transação: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Ponto de entrada transacional — aceita *sql.Tx para composição atômica
// ---------------------------------------------------------------------------

// SeedWorkspaceCategoriesTx é a função de baixo nível que deve ser chamada
// dentro de uma transação já existente (ex: durante a criação do workspace
// junto com o INSERT em workspaces e workspace_members na mesma tx).
//
// Uso recomendado nos handlers e no setup inicial:
//
//	tx, _ := db.Begin()
//	defer tx.Rollback()
//	// ... INSERT INTO workspaces ...
//	// ... INSERT INTO workspace_members ...
//	if err := database.SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
//	    return err
//	}
//	tx.Commit()
func SeedWorkspaceCategoriesTx(tx *sql.Tx, workspaceID, workspaceType string) error {
	if tx == nil {
		return fmt.Errorf("seeder: tx não pode ser nil")
	}
	if workspaceID == "" {
		return fmt.Errorf("seeder: workspaceID não pode ser vazio")
	}

	// --- Guarda de duplicidade ---
	// Verifica se já existem categorias para este workspace.
	// Impede re-execução acidental e protege a idempotência do setup.
	var existingCount int
	if err := tx.QueryRow(
		`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`,
		workspaceID,
	).Scan(&existingCount); err != nil {
		return fmt.Errorf("seeder: falha ao verificar categorias existentes: %w", err)
	}
	if existingCount > 0 {
		slog.Info("seeder: workspace já possui categorias, seed ignorado",
			"workspace_id", workspaceID,
			"categorias_existentes", existingCount,
		)
		return nil
	}

	// Seleciona o dicionário correto com base no tipo de workspace.
	var tree []categoryNode
	switch workspaceType {
	case "business":
		tree = businessCategoryTree()
	default:
		tree = personalCategoryTree()
	}

	now := time.Now().Unix()

	// Itera pela árvore: insere o Pai → captura seu UUID → insere os Filhos.
	// CRÍTICO: Não há query aberta durante o loop de filhos (anti-deadlock SQLite).
	for _, parent := range tree {
		parentID := uuid.NewString()

		if _, err := tx.Exec(`
			INSERT INTO categories
				(id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at)
			VALUES
				(?, ?, ?, 'tag', '#6b7280', ?, ?, NULL, 0, ?)
		`, parentID, workspaceID, parent.Name, parent.Type, parent.MacroGroup, now); err != nil {
			return fmt.Errorf("seeder: falha ao inserir categoria pai '%s': %w", parent.Name, err)
		}

		for _, childName := range parent.Children {
			if _, err := tx.Exec(`
				INSERT INTO categories
					(id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at)
				VALUES
					(?, ?, ?, 'tag', '#6b7280', ?, ?, ?, 0, ?)
			`, uuid.NewString(), workspaceID, childName, parent.Type, parent.MacroGroup, parentID, now); err != nil {
				return fmt.Errorf("seeder: falha ao inserir subcategoria '%s' (pai: '%s'): %w", childName, parent.Name, err)
			}
		}
	}

	slog.Info("seeder: categorias estruturais instaladas com sucesso",
		"workspace_id", workspaceID,
		"workspace_type", workspaceType,
		"total_grupos", len(tree),
	)
	return nil
}

func DryRunWorkspaceCategoryReseed(db *sql.DB, workspaceID string) (CategoryReseedReport, error) {
	return workspaceCategoryReseed(db, workspaceID, false)
}

func ApplyWorkspaceCategoryReseed(db *sql.DB, workspaceID string) (CategoryReseedReport, error) {
	return workspaceCategoryReseed(db, workspaceID, true)
}

func workspaceCategoryReseed(db *sql.DB, workspaceID string, apply bool) (CategoryReseedReport, error) {
	if db == nil {
		return CategoryReseedReport{}, fmt.Errorf("category reseed: db não pode ser nil")
	}
	if strings.TrimSpace(workspaceID) == "" {
		return CategoryReseedReport{}, fmt.Errorf("category reseed: workspaceID não pode ser vazio")
	}

	tx, err := db.Begin()
	if err != nil {
		return CategoryReseedReport{}, fmt.Errorf("category reseed: falha ao abrir transação: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	report, _, err := planWorkspaceCategoryReseedTx(tx, workspaceID)
	if err != nil {
		return CategoryReseedReport{}, err
	}
	report.Applied = apply

	if !apply {
		report.TotalAfter = report.TotalBefore - len(report.RemoveCandidates) + len(report.CanonicalCreated)
		return report, nil
	}

	if err := deleteReseedCandidatesTx(tx, report.RemoveCandidates); err != nil {
		return CategoryReseedReport{}, err
	}

	created, existing, err := ensureCanonicalCategoryTreeTx(tx, workspaceID, report.WorkspaceType)
	if err != nil {
		return CategoryReseedReport{}, err
	}
	report.CanonicalCreated = created
	report.CanonicalAlreadyExisted = existing

	if err := tx.QueryRow(`SELECT COUNT(1) FROM categories WHERE workspace_id = ?`, workspaceID).Scan(&report.TotalAfter); err != nil {
		return CategoryReseedReport{}, fmt.Errorf("category reseed: falha ao contar categorias finais: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return CategoryReseedReport{}, fmt.Errorf("category reseed: falha ao confirmar transação: %w", err)
	}

	return report, nil
}

func planWorkspaceCategoryReseedTx(tx *sql.Tx, workspaceID string) (CategoryReseedReport, map[string]bool, error) {
	var workspaceType string
	if err := tx.QueryRow(`SELECT type FROM workspaces WHERE id = ?`, workspaceID).Scan(&workspaceType); err != nil {
		if err == sql.ErrNoRows {
			return CategoryReseedReport{}, nil, fmt.Errorf("category reseed: workspace %q não encontrado", workspaceID)
		}
		return CategoryReseedReport{}, nil, fmt.Errorf("category reseed: falha ao buscar workspace: %w", err)
	}
	if workspaceType != "business" {
		workspaceType = "personal"
	}

	categories, err := loadCategoryReseedRowsTx(tx, workspaceID)
	if err != nil {
		return CategoryReseedReport{}, nil, err
	}

	report := CategoryReseedReport{
		WorkspaceID:   workspaceID,
		WorkspaceType: workspaceType,
		TotalBefore:   len(categories),
	}

	canonical := canonicalCategoryKeySet(workspaceType)
	childrenByParent := make(map[string][]string)
	byID := make(map[string]categoryReseedRow)
	for _, category := range categories {
		byID[category.ID] = category
		if category.ParentID != "" {
			childrenByParent[category.ParentID] = append(childrenByParent[category.ParentID], category.ID)
		}
	}

	preserve := make(map[string]bool)
	reportedDependency := make(map[string]bool)
	for _, category := range categories {
		key := categoryReseedKey(category.Name, category.Type, category.MacroGroup, category.ParentName)
		if canonical[key] && !category.ParentMissing {
			preserve[category.ID] = true
		}

		if category.TransactionCount > 0 || category.RecurringRuleCount > 0 {
			preserve[category.ID] = true
			item := category.CategoryReseedItem
			item.Reason = reseedUsageReason(category)
			report.PreservedByUsage = append(report.PreservedByUsage, item)
		}
		if category.CostLimitCount > 0 || category.BoxCount > 0 {
			preserve[category.ID] = true
			reportedDependency[category.ID] = true
			item := category.CategoryReseedItem
			item.Reason = reseedDependencyReason(category)
			report.PreservedByDependency = append(report.PreservedByDependency, item)
		}
		if category.ParentMissing && (category.TransactionCount > 0 || category.RecurringRuleCount > 0 || category.CostLimitCount > 0 || category.BoxCount > 0) {
			item := category.CategoryReseedItem
			item.Reason = "categoria_orfa_com_uso_ou_dependencia"
			report.Conflicts = append(report.Conflicts, item)
		}
	}

	changed := true
	for changed {
		changed = false
		for _, category := range categories {
			if preserve[category.ID] {
				continue
			}
			for _, childID := range childrenByParent[category.ID] {
				if preserve[childID] {
					preserve[category.ID] = true
					changed = true
					if !reportedDependency[category.ID] {
						reportedDependency[category.ID] = true
						item := category.CategoryReseedItem
						item.Reason = "filho_preservado"
						report.PreservedByDependency = append(report.PreservedByDependency, item)
					}
					break
				}
			}
		}
	}

	removeSet := make(map[string]bool)
	for _, category := range categories {
		if preserve[category.ID] {
			continue
		}
		removeSet[category.ID] = true
		item := category.CategoryReseedItem
		if category.ParentMissing {
			item.Reason = "categoria_orfa_sem_uso"
		} else {
			item.Reason = "categoria_nao_canonica_sem_uso"
		}
		report.RemoveCandidates = append(report.RemoveCandidates, item)
	}

	sortReseedRemovalCandidates(report.RemoveCandidates, byID)
	report.CanonicalCreated, report.CanonicalAlreadyExisted = planCanonicalCategoryCreates(categories, removeSet, workspaceID, workspaceType)
	report.Conflicts = append(report.Conflicts, findCategoryReseedConflicts(categories, removeSet, workspaceType)...)
	return report, removeSet, nil
}

func loadCategoryReseedRowsTx(tx *sql.Tx, workspaceID string) ([]categoryReseedRow, error) {
	rows, err := tx.Query(`
		SELECT
			c.id,
			c.workspace_id,
			c.name,
			c.type,
			COALESCE(c.macro_group, ''),
			COALESCE(c.parent_id, ''),
			COALESCE(p.name, ''),
			CASE WHEN c.parent_id IS NOT NULL AND p.id IS NULL THEN 1 ELSE 0 END,
			(SELECT COUNT(1) FROM transactions t WHERE t.workspace_id = c.workspace_id AND t.category_id = c.id),
			(SELECT COUNT(1) FROM recurring_rules rr WHERE rr.workspace_id = c.workspace_id AND rr.category_id = c.id),
			(SELECT COUNT(1) FROM cost_limits cl WHERE cl.workspace_id = c.workspace_id AND cl.category_id = c.id),
			(SELECT COUNT(1) FROM boxes b WHERE b.workspace_id = c.workspace_id AND b.category_id = c.id)
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.workspace_id = ?
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("category reseed: falha ao listar categorias: %w", err)
	}
	defer rows.Close()

	var out []categoryReseedRow
	for rows.Next() {
		var item categoryReseedRow
		var parentMissing int
		if err := rows.Scan(
			&item.ID,
			&item.WorkspaceID,
			&item.Name,
			&item.Type,
			&item.MacroGroup,
			&item.ParentID,
			&item.ParentName,
			&parentMissing,
			&item.TransactionCount,
			&item.RecurringRuleCount,
			&item.CostLimitCount,
			&item.BoxCount,
		); err != nil {
			return nil, fmt.Errorf("category reseed: falha ao ler categoria: %w", err)
		}
		item.ParentMissing = parentMissing == 1
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("category reseed: falha ao iterar categorias: %w", err)
	}
	return out, nil
}

func deleteReseedCandidatesTx(tx *sql.Tx, candidates []CategoryReseedItem) error {
	for _, item := range candidates {
		result, err := tx.Exec(`DELETE FROM categories WHERE id = ? AND workspace_id = ?`, item.ID, item.WorkspaceID)
		if err != nil {
			return fmt.Errorf("category reseed: falha ao remover categoria %q: %w", item.Name, err)
		}
		rows, _ := result.RowsAffected()
		if rows == 0 {
			return fmt.Errorf("category reseed: categoria %q não foi removida", item.Name)
		}
	}
	return nil
}

func ensureCanonicalCategoryTreeTx(tx *sql.Tx, workspaceID, workspaceType string) ([]CategoryReseedItem, []CategoryReseedItem, error) {
	tree := personalCategoryTree()
	if workspaceType == "business" {
		tree = businessCategoryTree()
	}

	now := time.Now().Unix()
	var created []CategoryReseedItem
	var existing []CategoryReseedItem

	for _, parent := range tree {
		parentID, found, err := findCanonicalCategoryIDTx(tx, workspaceID, parent.Name, parent.Type, parent.MacroGroup, "")
		if err != nil {
			return nil, nil, err
		}
		if found {
			existing = append(existing, CategoryReseedItem{WorkspaceID: workspaceID, ID: parentID, Name: parent.Name, Type: parent.Type, MacroGroup: parent.MacroGroup})
		} else {
			parentID = uuid.NewString()
			if _, err := tx.Exec(`
				INSERT INTO categories
					(id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at)
				VALUES
					(?, ?, ?, 'tag', '#6b7280', ?, ?, NULL, 0, ?)
			`, parentID, workspaceID, parent.Name, parent.Type, parent.MacroGroup, now); err != nil {
				return nil, nil, fmt.Errorf("category reseed: falha ao criar categoria pai %q: %w", parent.Name, err)
			}
			created = append(created, CategoryReseedItem{WorkspaceID: workspaceID, ID: parentID, Name: parent.Name, Type: parent.Type, MacroGroup: parent.MacroGroup})
		}

		for _, childName := range parent.Children {
			childID, found, err := findCanonicalCategoryIDTx(tx, workspaceID, childName, parent.Type, parent.MacroGroup, parentID)
			if err != nil {
				return nil, nil, err
			}
			if found {
				existing = append(existing, CategoryReseedItem{WorkspaceID: workspaceID, ID: childID, Name: childName, Type: parent.Type, MacroGroup: parent.MacroGroup, ParentID: parentID, ParentName: parent.Name})
				continue
			}
			childID = uuid.NewString()
			if _, err := tx.Exec(`
				INSERT INTO categories
					(id, workspace_id, name, icon, color, type, macro_group, parent_id, is_fixed, created_at)
				VALUES
					(?, ?, ?, 'tag', '#6b7280', ?, ?, ?, 0, ?)
			`, childID, workspaceID, childName, parent.Type, parent.MacroGroup, parentID, now); err != nil {
				return nil, nil, fmt.Errorf("category reseed: falha ao criar subcategoria %q: %w", childName, err)
			}
			created = append(created, CategoryReseedItem{WorkspaceID: workspaceID, ID: childID, Name: childName, Type: parent.Type, MacroGroup: parent.MacroGroup, ParentID: parentID, ParentName: parent.Name})
		}
	}

	return created, existing, nil
}

func findCanonicalCategoryIDTx(tx *sql.Tx, workspaceID, name, typ, macroGroup, parentID string) (string, bool, error) {
	var id string
	var err error
	if parentID == "" {
		err = tx.QueryRow(`
			SELECT id
			FROM categories
			WHERE workspace_id = ? AND parent_id IS NULL AND name = ? AND type = ? AND COALESCE(macro_group, '') = ?
			ORDER BY created_at, id
			LIMIT 1
		`, workspaceID, name, typ, macroGroup).Scan(&id)
	} else {
		err = tx.QueryRow(`
			SELECT id
			FROM categories
			WHERE workspace_id = ? AND parent_id = ? AND name = ? AND type = ? AND COALESCE(macro_group, '') = ?
			ORDER BY created_at, id
			LIMIT 1
		`, workspaceID, parentID, name, typ, macroGroup).Scan(&id)
	}
	if err == nil {
		return id, true, nil
	}
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return "", false, fmt.Errorf("category reseed: falha ao verificar categoria canônica %q: %w", name, err)
}

func planCanonicalCategoryCreates(categories []categoryReseedRow, removeSet map[string]bool, workspaceID, workspaceType string) ([]CategoryReseedItem, []CategoryReseedItem) {
	existingKeys := make(map[string]CategoryReseedItem)
	for _, category := range categories {
		if removeSet[category.ID] || category.ParentMissing {
			continue
		}
		key := categoryReseedKey(category.Name, category.Type, category.MacroGroup, category.ParentName)
		if _, ok := existingKeys[key]; !ok {
			existingKeys[key] = category.CategoryReseedItem
		}
	}

	var created []CategoryReseedItem
	var existing []CategoryReseedItem
	tree := personalCategoryTree()
	if workspaceType == "business" {
		tree = businessCategoryTree()
	}
	for _, parent := range tree {
		parentItem := CategoryReseedItem{WorkspaceID: workspaceID, Name: parent.Name, Type: parent.Type, MacroGroup: parent.MacroGroup}
		parentKey := categoryReseedKey(parent.Name, parent.Type, parent.MacroGroup, "")
		if item, ok := existingKeys[parentKey]; ok {
			existing = append(existing, item)
		} else {
			created = append(created, parentItem)
			existingKeys[parentKey] = parentItem
		}
		for _, childName := range parent.Children {
			childItem := CategoryReseedItem{WorkspaceID: workspaceID, Name: childName, Type: parent.Type, MacroGroup: parent.MacroGroup, ParentName: parent.Name}
			childKey := categoryReseedKey(childName, parent.Type, parent.MacroGroup, parent.Name)
			if item, ok := existingKeys[childKey]; ok {
				existing = append(existing, item)
				continue
			}
			created = append(created, childItem)
			existingKeys[childKey] = childItem
		}
	}
	return created, existing
}

func findCategoryReseedConflicts(categories []categoryReseedRow, removeSet map[string]bool, workspaceType string) []CategoryReseedItem {
	canonicalByNameType := make(map[string]bool)
	canonicalKeys := canonicalCategoryKeySet(workspaceType)
	for key := range canonicalKeys {
		parts := strings.Split(key, "\x00")
		if len(parts) == 4 {
			canonicalByNameType[parts[0]+"\x00"+parts[3]] = true
		}
	}

	canonicalDuplicates := make(map[string][]categoryReseedRow)
	var conflicts []CategoryReseedItem
	for _, category := range categories {
		if removeSet[category.ID] || category.ParentMissing {
			continue
		}
		key := categoryReseedKey(category.Name, category.Type, category.MacroGroup, category.ParentName)
		if canonicalKeys[key] {
			canonicalDuplicates[key] = append(canonicalDuplicates[key], category)
			continue
		}
		if canonicalByNameType[strings.ToUpper(strings.TrimSpace(category.Type))+"\x00"+strings.TrimSpace(category.Name)] {
			item := category.CategoryReseedItem
			item.Reason = "categoria_preservada_conflita_com_nome_canonico"
			conflicts = append(conflicts, item)
		}
	}

	for _, duplicates := range canonicalDuplicates {
		if len(duplicates) < 2 {
			continue
		}
		for _, duplicate := range duplicates {
			item := duplicate.CategoryReseedItem
			item.Reason = "categoria_canonica_duplicada_preservada"
			conflicts = append(conflicts, item)
		}
	}
	return conflicts
}

func canonicalCategoryKeySet(workspaceType string) map[string]bool {
	tree := personalCategoryTree()
	if workspaceType == "business" {
		tree = businessCategoryTree()
	}
	out := make(map[string]bool)
	for _, parent := range tree {
		out[categoryReseedKey(parent.Name, parent.Type, parent.MacroGroup, "")] = true
		for _, childName := range parent.Children {
			out[categoryReseedKey(childName, parent.Type, parent.MacroGroup, parent.Name)] = true
		}
	}
	return out
}

func categoryReseedKey(name, typ, macroGroup, parentName string) string {
	return strings.ToUpper(strings.TrimSpace(typ)) + "\x00" + strings.TrimSpace(macroGroup) + "\x00" + strings.TrimSpace(parentName) + "\x00" + strings.TrimSpace(name)
}

func reseedUsageReason(category categoryReseedRow) string {
	var reasons []string
	if category.TransactionCount > 0 {
		reasons = append(reasons, "lancamentos")
	}
	if category.RecurringRuleCount > 0 {
		reasons = append(reasons, "recorrencias")
	}
	return strings.Join(reasons, ",")
}

func reseedDependencyReason(category categoryReseedRow) string {
	var reasons []string
	if category.CostLimitCount > 0 {
		reasons = append(reasons, "limites")
	}
	if category.BoxCount > 0 {
		reasons = append(reasons, "caixinhas")
	}
	return strings.Join(reasons, ",")
}

func sortReseedRemovalCandidates(candidates []CategoryReseedItem, byID map[string]categoryReseedRow) {
	depthMemo := make(map[string]int)
	var depth func(string) int
	depth = func(id string) int {
		if value, ok := depthMemo[id]; ok {
			return value
		}
		category, ok := byID[id]
		if !ok || category.ParentID == "" {
			depthMemo[id] = 0
			return 0
		}
		value := 1 + depth(category.ParentID)
		depthMemo[id] = value
		return value
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		di := depth(candidates[i].ID)
		dj := depth(candidates[j].ID)
		if di != dj {
			return di > dj
		}
		return candidates[i].Name < candidates[j].Name
	})
}

func SeedWorkspaceAccountsTx(tx *sql.Tx, workspaceID, workspaceType string) error {
	if tx == nil {
		return fmt.Errorf("seeder: tx não pode ser nil")
	}
	if workspaceID == "" {
		return fmt.Errorf("seeder: workspaceID não pode ser vazio")
	}

	var existingAccountCount int
	if err := tx.QueryRow(
		`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD'`,
		workspaceID,
	).Scan(&existingAccountCount); err != nil {
		return fmt.Errorf("seeder: falha ao verificar contas existentes: %w", err)
	}

	var existingCardCount int
	if err := tx.QueryRow(
		`SELECT COUNT(1) FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD'`,
		workspaceID,
	).Scan(&existingCardCount); err != nil {
		return fmt.Errorf("seeder: falha ao verificar cartões existentes: %w", err)
	}

	slog.Info("seeder: seed de contas e cartões reais desativado",
		"workspace_id", workspaceID,
		"workspace_type", workspaceType,
		"contas_existentes", existingAccountCount,
		"cartoes_existentes", existingCardCount,
	)
	return nil
}

// ---------------------------------------------------------------------------
// Dicionário PERSONAL — Pessoa Física / Familiar
// ---------------------------------------------------------------------------
//
// Taxonomia revisada em 2026 — nova estrutura de Grupos Macros:
//   - "Receitas"       → type = INCOME
//   - "Essencial"      → type = EXPENSE
//   - "Estilo de Vida" → type = EXPENSE

func personalCategoryTree() []categoryNode {
	return []categoryNode{
		// ── RECEITAS ───────────────────────────────────────────
		{
			Name:       "Trabalho e Renda",
			Type:       "INCOME",
			MacroGroup: "Receitas",
			Children: []string{
				"Salário",
				"Pró-labore",
				"Freelance",
				"Comissão",
				"Bônus",
				"13º salário",
				"Reembolso recebido",
			},
		},
		{
			Name:       "Rendimentos",
			Type:       "INCOME",
			MacroGroup: "Receitas",
			Children: []string{
				"Juros",
				"Dividendos",
				"Rendimentos de investimentos",
				"Cashback",
				"Aluguel recebido",
			},
		},
		{
			Name:       "Outras Receitas",
			Type:       "INCOME",
			MacroGroup: "Receitas",
			Children: []string{
				"Presente recebido",
				"Venda de item usado",
			},
		},

		// ── DESPESAS ────────────────────────────────────────────
		{
			Name:       "Moradia",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Aluguel",
				"Condomínio",
				"Energia elétrica",
				"Água",
				"Gás",
				"Internet residencial",
				"IPTU",
				"Manutenção da casa",
				"Seguro residencial",
			},
		},
		{
			Name:       "Alimentação",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Supermercado",
				"Feira",
				"Açougue",
				"Hortifruti",
				"Padaria básica",
				"Itens de cozinha",
			},
		},
		{
			Name:       "Transporte",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Combustível",
				"Transporte público",
				"Aplicativo/Taxi essencial",
				"Pedágio",
				"Estacionamento",
				"Manutenção do veículo",
				"Seguro do veículo",
				"IPVA/Licenciamento",
			},
		},
		{
			Name:       "Saúde",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Plano de saúde",
				"Consulta médica",
				"Exames",
				"Farmácia",
				"Dentista",
				"Terapia",
				"Óculos/Lentes",
			},
		},
		{
			Name:       "Pet",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Ração",
				"Areia/Higiene",
				"Veterinário",
				"Medicamentos",
				"Vacinas",
				"Plano de saúde pet",
			},
		},
		{
			Name:       "Educação e Trabalho",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Escola/Faculdade",
				"Cursos profissionais",
				"Material de estudo",
				"Livros técnicos",
				"Ferramentas de trabalho",
				"Certificações",
			},
		},
		{
			Name:       "Financeiro",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Tarifas bancárias",
				"Juros",
				"Multas",
				"IOF",
				"Anuidade de cartão",
				"Empréstimos",
				"Financiamentos",
			},
		},
		{
			Name:       "Obrigações e Documentos",
			Type:       "EXPENSE",
			MacroGroup: "Essencial",
			Children: []string{
				"Imposto de renda",
				"Taxas públicas",
				"Cartório",
				"Documentos",
				"Seguro obrigatório",
			},
		},

		// ── ESTILO DE VIDA ─────────────────────────────────────
		{
			Name:       "Refeições",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Delivery",
				"Restaurante",
				"Café",
				"Bar",
				"Marmitas",
				"Lanches",
				"Doces/Sobremesas",
			},
		},
		{
			Name:       "Lazer",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Cinema/Eventos",
				"Streaming",
				"Passeios",
				"Viagens",
				"Jogos",
				"Hobbies",
				"Clubes/Assinaturas",
			},
		},
		{
			Name:       "Compras Pessoais",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Roupas",
				"Calçados",
				"Eletrônicos",
				"Acessórios",
				"Presentes",
				"Cuidados pessoais",
				"Perfumes/Cosméticos",
			},
		},
		{
			Name:       "Casa e Decoração",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Móveis",
				"Decoração",
				"Organização",
				"Utensílios não essenciais",
				"Jardinagem",
				"Automação residencial",
			},
		},
		{
			Name:       "Lazer Familiar",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Passeios em família",
				"Brinquedos",
				"Presentes para família",
				"Banho e tosa pet",
				"Brinquedos pet",
				"Petiscos pet",
				"Acessórios pet",
				"Hotel/Creche pet",
			},
		},
		{
			Name:       "Esporte e Bem-estar",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Academia",
				"Personal trainer",
				"Suplementos",
				"Esportes",
				"Massagem/Estética",
			},
		},
		{
			Name:       "Doações e Apoio",
			Type:       "EXPENSE",
			MacroGroup: "Estilo de Vida",
			Children: []string{
				"Igreja",
				"Caridade",
				"Apoio familiar",
				"Presentes especiais",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Dicionário BUSINESS — Pessoa Jurídica / Empresarial
// ---------------------------------------------------------------------------
//
// ATENÇÃO: As strings de MacroGroup abaixo são contratos imutáveis.
// Elas alimentam os filtros de DRE/Dashboard diretamente.
// NÃO altere as strings sem sincronizar com os templates e queries.
//
// Grupos Macros canônicos do workspace business:
//   - "Receitas Operacionais"
//   - "Deduções/Impostos"
//   - "Custos Operacionais"
//   - "Despesas Administrativas"
//   - "Despesas Comerciais"
//   - "Equipe e Prestadores"
//   - "Financeiro"
//   - "Investimentos/Outros"

func businessCategoryTree() []categoryNode {
	return []categoryNode{

		// ── RECEITAS OPERACIONAIS ───────────────────────────────────────────
		{
			Name:       "Vendas",
			Type:       "INCOME",
			MacroGroup: "Receitas Operacionais",
			Children: []string{
				"Venda de produto",
				"Venda de serviço",
				"Assinatura/Recorrência",
				"Projeto pontual",
				"Consultoria",
				"Implantação/Setup",
				"Comissão recebida",
			},
		},
		{
			Name:       "Clientes",
			Type:       "INCOME",
			MacroGroup: "Receitas Operacionais",
			Children: []string{
				"Mensalidade de cliente",
				"Reembolso de cliente",
				"Upsell",
				"Renovação",
			},
		},

		// ── DEDUÇÕES/IMPOSTOS ───────────────────────────────────
		{
			Name:       "Impostos sobre Receita",
			Type:       "EXPENSE",
			MacroGroup: "Deduções/Impostos",
			Children: []string{
				"DAS/Simples Nacional",
				"ISS",
				"ICMS",
				"PIS/COFINS",
				"IRPJ/CSLL",
				"Retenções",
			},
		},
		{
			Name:       "Deduções",
			Type:       "EXPENSE",
			MacroGroup: "Deduções/Impostos",
			Children: []string{
				"Estorno",
				"Desconto concedido",
				"Chargeback",
				"Reembolso ao cliente",
			},
		},

		// ── CUSTOS OPERACIONAIS ─────────────────────────────────
		{
			Name:       "Fornecedores",
			Type:       "EXPENSE",
			MacroGroup: "Custos Operacionais",
			Children: []string{
				"Compra de mercadoria",
				"Insumos",
				"Embalagens",
				"Frete de compra",
				"Fornecedor recorrente",
			},
		},
		{
			Name:       "Entrega/Produção",
			Type:       "EXPENSE",
			MacroGroup: "Custos Operacionais",
			Children: []string{
				"Plataforma operacional",
				"Hospedagem/servidor de cliente",
				"Licenças usadas na entrega",
				"Serviços terceirizados de entrega",
			},
		},
		{
			Name:       "Inteligência Artificial",
			Type:       "EXPENSE",
			MacroGroup: "Custos Operacionais",
			Children: []string{
				"APIs de IA",
				"Modelos de IA",
				"Créditos de IA",
				"Agentes/Assistentes IA",
				"Automação com IA",
				"Infraestrutura para IA",
			},
		},

		// ── DESPESAS ADMINISTRATIVAS ────────────────────────────
		{
			Name:       "Administração",
			Type:       "EXPENSE",
			MacroGroup: "Despesas Administrativas",
			Children: []string{
				"Contabilidade",
				"Jurídico",
				"Certificado digital",
				"Banco/Tarifas",
				"Aluguel comercial",
				"Coworking",
				"Internet",
				"Energia",
				"Telefonia",
				"Material de escritório",
			},
		},
		{
			Name:       "Sistemas e Produtividade",
			Type:       "EXPENSE",
			MacroGroup: "Despesas Administrativas",
			Children: []string{
				"Ferramentas/SaaS",
				"Armazenamento em nuvem",
				"Assinaturas de produtividade",
				"IA administrativa",
			},
		},

		// ── DESPESAS COMERCIAIS ─────────────────────────────────
		{
			Name:       "Marketing e Vendas",
			Type:       "EXPENSE",
			MacroGroup: "Despesas Comerciais",
			Children: []string{
				"Google Ads",
				"Meta Ads",
				"Tráfego pago",
				"CRM",
				"Landing pages",
				"Design/Criativos",
				"IA para marketing",
				"Eventos/Networking",
				"Comissão de venda",
			},
		},
		{
			Name:       "Comercial",
			Type:       "EXPENSE",
			MacroGroup: "Despesas Comerciais",
			Children: []string{
				"Prospecção",
				"Brindes comerciais",
				"Reuniões comerciais",
			},
		},

		// ── EQUIPE E PRESTADORES ────────────────────────────────
		{
			Name:       "Equipe",
			Type:       "EXPENSE",
			MacroGroup: "Equipe e Prestadores",
			Children: []string{
				"Salários",
				"Pró-labore",
				"Freelancers",
				"Terceirizados",
				"Encargos",
				"Benefícios",
				"Treinamento da equipe",
			},
		},

		// ── FINANCEIRO ──────────────────────────────────────────
		{
			Name:       "Financeiro",
			Type:       "EXPENSE",
			MacroGroup: "Financeiro",
			Children: []string{
				"Juros pagos",
				"Multas",
				"Empréstimos",
				"Financiamentos",
				"Anuidade de cartão",
				"IOF",
				"Tarifas bancárias",
			},
		},

		// ── INVESTIMENTOS/OUTROS ────────────────────────────────
		{
			Name:       "Investimentos",
			Type:       "EXPENSE",
			MacroGroup: "Investimentos/Outros",
			Children: []string{
				"Equipamentos",
				"Computadores",
				"Móveis",
				"Software permanente",
				"Reforma",
				"Expansão",
			},
		},
		{
			Name:       "Outros",
			Type:       "EXPENSE",
			MacroGroup: "Investimentos/Outros",
			Children: []string{
				"Aporte dos sócios",
				"Venda de ativo",
				"Despesa não recorrente",
			},
		},
	}
}
