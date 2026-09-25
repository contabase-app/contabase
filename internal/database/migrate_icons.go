package database

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/contabase-app/contabase/internal/icons"
)

// migrateIconKeys adiciona a coluna icon_key às entidades que ainda não
// ofereciam seleção de ícone e aplica o backfill determinístico das categorias.
//
// Escopo:
//   - cost_limits.icon_key (Limites);
//   - boxes.icon_key (Metas/reservas);
//   - categories.icon já existia; recebe apenas backfill.
//
// A coluna nasce nullable e sem default para que "sem escolha" seja
// distinguível de "escolha explícita". A herança
// icon_key -> categoria -> fallback acontece na camada de leitura.
func migrateIconKeys(tx *sql.Tx) error {
	for _, target := range []struct{ table, ddl string }{
		{"cost_limits", "TEXT"},
		{"boxes", "TEXT"},
	} {
		exists, err := tableExists(tx, target.table)
		if err != nil {
			return fmt.Errorf("migration 66: inspecionar %s: %w", target.table, err)
		}
		if !exists {
			continue
		}
		if err := addColumnIfMissing(tx, target.table, "icon_key", target.ddl); err != nil {
			return fmt.Errorf("migration 66: adicionar %s.icon_key: %w", target.table, err)
		}
	}

	updated, err := BackfillCategoryIconsTx(tx)
	if err != nil {
		return fmt.Errorf("migration 66: backfill de ícones de categoria: %w", err)
	}
	slog.Info("migration 66: icon_key disponível e categorias normalizadas",
		"categorias_atualizadas", updated)
	return nil
}

// legacyDefaultCategoryIcon é o valor gravado pelo default histórico da coluna
// categories.icon enquanto não havia interface de escolha de ícone.
const legacyDefaultCategoryIcon = icons.DefaultKey

// BackfillCategoryIconsTx normaliza apenas categorias anteriores à migration 66.
// O runner registra a versão depois do backfill, na mesma transação: após o
// commit, até 'tag' pode ser uma escolha manual e nenhuma linha é reinterpretada.
// Sem a tabela de versões, falha fechado por falta de proveniência.
func BackfillCategoryIconsTx(tx *sql.Tx) (int, error) {
	var applied bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = 66)`).Scan(&applied); err != nil {
		return 0, fmt.Errorf("consultar proveniência da migration 66: %w", err)
	}
	if applied {
		return 0, nil
	}

	exists, err := tableExists(tx, "categories")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}

	// Lê tudo antes de escrever: o SQLite não tolera UPDATE com cursor aberto
	// na mesma conexão/transação.
	type pending struct {
		id          string
		workspaceID string
		key         string
	}
	var updates []pending

	rows, err := tx.Query(`SELECT id, workspace_id, name, COALESCE(icon, '') FROM categories`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, workspaceID, name, current string
		if err := rows.Scan(&id, &workspaceID, &name, &current); err != nil {
			rows.Close()
			return 0, err
		}
		if !categoryIconIsUnset(current) {
			continue
		}
		key := icons.CategoryIconForName(name)
		if key == "" || key == legacyDefaultCategoryIcon {
			// Sem mapeamento confiável: mantém o fallback já existente.
			continue
		}
		updates = append(updates, pending{id: id, workspaceID: workspaceID, key: key})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	updated := 0
	for _, item := range updates {
		result, err := tx.Exec(
			`UPDATE categories SET icon = ? WHERE id = ? AND workspace_id = ? AND (icon IS NULL OR TRIM(icon) = '' OR LOWER(TRIM(icon)) = ?)`,
			item.key, item.id, item.workspaceID, legacyDefaultCategoryIcon,
		)
		if err != nil {
			return 0, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if affected > 1 {
			return 0, fmt.Errorf("backfill de categoria afetou %d linhas; esperado no máximo 1", affected)
		}
		updated += int(affected)
	}
	return updated, nil
}

// categoryIconIsUnset informa se o valor armazenado representa "sem escolha".
func categoryIconIsUnset(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == "" || strings.EqualFold(trimmed, legacyDefaultCategoryIcon)
}
