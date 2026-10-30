package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

type ConfigRepository struct {
	db *sql.DB
}

func NewConfigRepository(db *sql.DB) *ConfigRepository {
	return &ConfigRepository{db: db}
}

type ConfigCategory struct {
	ID           string
	Name         string
	Type         string
	Icon         string
	MacroGroup   string
	ParentID     string
	ParentName   string
	EffectiveMac string
}

type ConfigAccount struct {
	ID             string
	Name           string
	Type           string
	Color          string
	Icon           string
	ProviderSlug   string
	CurrentBalance int64
	SortOrder      int64
	ArchivedAt     *int64
}

type ConfigCard struct {
	AccountID               string
	Name                    string
	Color                   string
	Icon                    string
	ProviderSlug            string
	ClosingDay              int64
	DueDay                  int64
	CreditLimit             int64
	DefaultPaymentAccountID string
	SortOrder               int64
}

type Workspace struct {
	ID          string
	Name        string
	Description string
	ThemeToken  string
}

type WorkspaceMember struct {
	UserID   string
	Name     string
	Email    string
	Role     string
	JoinedAt int64
}

func (r *ConfigRepository) UserNameByID(userID string) (string, error) {
	var name string
	err := r.db.QueryRow(`SELECT name FROM users WHERE id = ?`, userID).Scan(&name)
	return name, err
}

func (r *ConfigRepository) CategoriesByWorkspace(workspaceID string) ([]ConfigCategory, error) {
	rows, err := r.db.Query(`
		SELECT c.id, c.name, c.type, COALESCE(c.icon, ''), COALESCE(c.macro_group, ''), COALESCE(c.parent_id, ''), COALESCE(p.name, ''), COALESCE(c.macro_group, p.macro_group, 'Estilo de Vida')
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.workspace_id = ?
		ORDER BY c.type, COALESCE(p.name, c.name), c.name
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConfigCategory
	for rows.Next() {
		var item ConfigCategory
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.Icon, &item.MacroGroup, &item.ParentID, &item.ParentName, &item.EffectiveMac); err != nil {
			return nil, err
		}
		item.MacroGroup = canonicalConfigMacroGroup(item.MacroGroup)
		item.EffectiveMac = canonicalConfigMacroGroup(item.EffectiveMac)
		out = append(out, item)
	}
	return out, rows.Err()
}

func canonicalConfigMacroGroup(group string) string {
	switch strings.ToUpper(strings.TrimSpace(group)) {
	case "ESSENTIAL":
		return "Essencial"
	case "LIFESTYLE":
		return "Estilo de Vida"
	case "OPERATING_COSTS":
		return "Custos Operacionais"
	case "OPERATING_REVENUE", "OPERATIONAL_REVENUE":
		return "Receitas Operacionais"
	default:
		return strings.TrimSpace(group)
	}
}

func (r *ConfigRepository) AccountsByWorkspace(workspaceID string) ([]ConfigAccount, error) {
	rows, err := r.db.Query(`
		SELECT id, name, type, COALESCE(NULLIF(color, ''), '#6B7280'), COALESCE(NULLIF(icon, ''), ''), COALESCE(NULLIF(provider_slug, ''), 'custom'), current_balance, sort_order
		FROM accounts
		WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL
		ORDER BY sort_order ASC, name ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConfigAccount
	for rows.Next() {
		var item ConfigAccount
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.Color, &item.Icon, &item.ProviderSlug, &item.CurrentBalance, &item.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ConfigRepository) ArchivedAccountsByWorkspace(workspaceID string) ([]ConfigAccount, error) {
	rows, err := r.db.Query(`
		SELECT id, name, type, COALESCE(NULLIF(color, ''), '#6B7280'), COALESCE(NULLIF(icon, ''), ''), COALESCE(NULLIF(provider_slug, ''), 'custom'), current_balance, sort_order
		FROM accounts
		WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NOT NULL
		ORDER BY sort_order ASC, name ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConfigAccount
	for rows.Next() {
		var item ConfigAccount
		if err := rows.Scan(&item.ID, &item.Name, &item.Type, &item.Color, &item.Icon, &item.ProviderSlug, &item.CurrentBalance, &item.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ConfigRepository) CardsByWorkspace(workspaceID string) ([]ConfigCard, error) {
	rows, err := r.db.Query(`
		SELECT a.id, a.name, COALESCE(NULLIF(a.color, ''), '#6B7280'), COALESCE(NULLIF(a.icon, ''), ''), COALESCE(NULLIF(a.provider_slug, ''), 'custom'), cc.closing_day, cc.due_day, cc.credit_limit, COALESCE(cc.default_payment_account_id, ''), a.sort_order
		FROM accounts a
		JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND a.archived_at IS NULL
		ORDER BY a.sort_order ASC, a.name ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConfigCard
	for rows.Next() {
		var item ConfigCard
		if err := rows.Scan(&item.AccountID, &item.Name, &item.Color, &item.Icon, &item.ProviderSlug, &item.ClosingDay, &item.DueDay, &item.CreditLimit, &item.DefaultPaymentAccountID, &item.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ConfigRepository) ArchivedCardsByWorkspace(workspaceID string) ([]ConfigCard, error) {
	rows, err := r.db.Query(`
		SELECT a.id, a.name, COALESCE(NULLIF(a.color, ''), '#6B7280'), COALESCE(NULLIF(a.icon, ''), ''), COALESCE(NULLIF(a.provider_slug, ''), 'custom'), cc.closing_day, cc.due_day, cc.credit_limit, COALESCE(cc.default_payment_account_id, ''), a.sort_order
		FROM accounts a
		JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND a.archived_at IS NOT NULL
		ORDER BY a.sort_order ASC, a.name ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ConfigCard
	for rows.Next() {
		var item ConfigCard
		if err := rows.Scan(&item.AccountID, &item.Name, &item.Color, &item.Icon, &item.ProviderSlug, &item.ClosingDay, &item.DueDay, &item.CreditLimit, &item.DefaultPaymentAccountID, &item.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *ConfigRepository) WorkspaceByID(workspaceID string) (Workspace, error) {
	var ws Workspace
	err := r.db.QueryRow(`
		SELECT id, name, description, COALESCE(theme_token, '')
		FROM workspaces
		WHERE id = ?
	`, workspaceID).Scan(&ws.ID, &ws.Name, &ws.Description, &ws.ThemeToken)
	return ws, err
}

func (r *ConfigRepository) ArchiveAccountTx(tx *sql.Tx, workspaceID, accountID string) error {
	result, err := tx.Exec(`UPDATE accounts SET archived_at = unixepoch() WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, accountID, workspaceID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *ConfigRepository) UnarchiveAccountTx(tx *sql.Tx, workspaceID, accountID string) error {
	result, err := tx.Exec(`UPDATE accounts SET archived_at = NULL WHERE id = ? AND workspace_id = ? AND archived_at IS NOT NULL`, accountID, workspaceID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *ConfigRepository) WorkspaceMembers(workspaceID string) ([]WorkspaceMember, error) {
	rows, err := r.db.Query(`
		SELECT u.id, u.name, u.email, wm.role, wm.joined_at
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ?
		ORDER BY wm.joined_at ASC
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []WorkspaceMember
	for rows.Next() {
		var item WorkspaceMember
		if err := rows.Scan(&item.UserID, &item.Name, &item.Email, &item.Role, &item.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type OrphanCardInfo struct {
	AccountID   string
	Name        string
	WorkspaceID string
}

type RepairOrphanCardsResult struct {
	Diagnosed int
	Repaired  int
	Orphans   []OrphanCardInfo
}

func (r *ConfigRepository) RepairOrphanCreditCards(dryRun bool) (RepairOrphanCardsResult, error) {
	var result RepairOrphanCardsResult
	orphans, err := r.diagnoseOrphanCreditCards()
	if err != nil {
		return result, err
	}
	result.Orphans = orphans
	result.Diagnosed = len(orphans)
	if dryRun || result.Diagnosed == 0 {
		return result, nil
	}

	tx, err := r.db.Begin()
	if err != nil {
		return result, fmt.Errorf("iniciar transação de reparo: %w", err)
	}
	defer tx.Rollback()

	for _, item := range result.Orphans {
		res, err := tx.Exec(`
			INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
			SELECT ?, owner.id, 20, 10, 0
			FROM accounts owner
			WHERE owner.id = ? AND owner.workspace_id = ? AND owner.type = 'CREDIT_CARD'
		`, uuid.NewString(), item.AccountID, item.WorkspaceID)
		if err != nil {
			return result, fmt.Errorf("reparar cartão %s (%s): %w", item.Name, item.AccountID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("cardinalidade do reparo %s: %w", item.AccountID, err)
		}
		if affected != 1 {
			return result, fmt.Errorf("reparar cartão %s (%s): esperado 1 registro, obtidos %d", item.Name, item.AccountID, affected)
		}
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit reparo: %w", err)
	}
	result.Repaired = result.Diagnosed
	return result, nil
}

func (r *ConfigRepository) diagnoseOrphanCreditCards() ([]OrphanCardInfo, error) {
	tx, err := r.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("diagnóstico de cartões órfãos: %w", err)
	}
	defer tx.Rollback()
	// CONTROL-PLANE GLOBAL: IDs only, never discovery from financial data.
	rows, err := tx.Query(`SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("diagnóstico de cartões órfãos: %w", err)
	}
	var workspaceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("leitura de workspace: %w", err)
		}
		workspaceIDs = append(workspaceIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var orphans []OrphanCardInfo
	for _, workspaceID := range workspaceIDs {
		items, err := diagnoseWorkspaceOrphanCreditCards(tx, workspaceID)
		if err != nil {
			return nil, err
		}
		orphans = append(orphans, items...)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("diagnóstico de cartões órfãos: %w", err)
	}
	// SQLite's existing BINARY order, without an additional functional tie-break.
	sort.SliceStable(orphans, func(i, j int) bool {
		if orphans[i].WorkspaceID != orphans[j].WorkspaceID {
			return orphans[i].WorkspaceID < orphans[j].WorkspaceID
		}
		return orphans[i].Name < orphans[j].Name
	})
	return orphans, nil
}

func diagnoseWorkspaceOrphanCreditCards(tx *sql.Tx, workspaceID string) ([]OrphanCardInfo, error) {
	rows, err := tx.Query(`
		SELECT a.id, a.name, a.workspace_id
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND cc.account_id IS NULL
		ORDER BY a.workspace_id, a.name
	`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("diagnóstico de cartões órfãos: %w", err)
	}
	defer rows.Close()
	var out []OrphanCardInfo
	for rows.Next() {
		var item OrphanCardInfo
		if err := rows.Scan(&item.AccountID, &item.Name, &item.WorkspaceID); err != nil {
			return nil, fmt.Errorf("leitura de cartão órfão: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
