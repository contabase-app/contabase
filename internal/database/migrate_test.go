package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/categorycontract"
)

func TestOpenRegistersSchemaMigrationsOnFreshMemory(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != len(migrations) {
		t.Fatalf("expected %d applied migrations, got %d", len(migrations), count)
	}
}

func TestOpenCreatesAuthLockoutsTable(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertColumnExists(t, db, "auth_lockouts", "user_id")
	assertColumnExists(t, db, "auth_lockouts", "failed_password_count")
	assertColumnExists(t, db, "auth_lockouts", "failed_2fa_count")
	assertColumnExists(t, db, "auth_lockouts", "first_failed_at")
	assertColumnExists(t, db, "auth_lockouts", "last_failed_at")
	assertColumnExists(t, db, "auth_lockouts", "locked_until")
	assertColumnExists(t, db, "auth_lockouts", "lock_reason")
	assertColumnExists(t, db, "auth_lockouts", "updated_at")

	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = 'idx_auth_lockouts_locked_until'`).Scan(&indexCount); err != nil {
		t.Fatalf("query auth_lockouts index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("expected idx_auth_lockouts_locked_until to exist")
	}
}

func TestCategoriesSchemaAcceptsCanonicalMacroGroups(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, type) VALUES ('ws-canonical-macro', 'Canonical Macro', 'business')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	macroGroups := []string{
		"Essencial",
		"Estilo de Vida",
		"Receitas",
		categorycontract.MacroReceitasOperacionais,
		categorycontract.MacroReceitasFinanceiras,
		categorycontract.MacroOutrasEntradas,
		categorycontract.MacroDeducoesImpostos,
		categorycontract.MacroCustosOperacionais,
		categorycontract.MacroDespesasAdministrativas,
		categorycontract.MacroDespesasComerciais,
		categorycontract.MacroEquipePrestadores,
		categorycontract.MacroFinanceiro,
		categorycontract.MacroRetiradasDistribuicao,
		categorycontract.MacroInvestimentosPatrimonial,
		categorycontract.MacroOutrasSaidas,
		categorycontract.MacroInvestimentosOutros,
	}
	for i, macroGroup := range macroGroups {
		if _, err := db.Exec(`
			INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
			VALUES (?, 'ws-canonical-macro', ?, 'EXPENSE', ?, unixepoch())
		`, fmt.Sprintf("cat-canonical-%d", i), macroGroup, macroGroup); err != nil {
			t.Fatalf("insert category with macro_group %q: %v", macroGroup, err)
		}
	}
}

func TestMigration49RepairsBusinessCanonicalCategories(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration49.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("execute ddl fixture: %v", err)
	}
	seedAppliedMigrationsBefore(t, db, 49)

	seedReseedWorkspace(t, db, "ws-mig49-business", "business")
	seedReseedWorkspace(t, db, "ws-mig49-personal", "personal")
	seedReseedLedgerBase(t, db, "ws-mig49-business")
	insertReseedCategory(t, db, "mig49-custom", "ws-mig49-business", "Categoria Customizada", "EXPENSE", "Custom", "")
	insertReseedCategory(t, db, "mig49-invest-old", "ws-mig49-business", "Investimentos/Outros", "EXPENSE", categorycontract.MacroInvestimentosOutros, "")
	insertReseedTransaction(t, db, "tx-mig49-invest-old", "ws-mig49-business", "mig49-invest-old")
	insertReseedCategory(t, db, "mig49-personal-custom", "ws-mig49-personal", "Categoria Personal", "EXPENSE", "Estilo de Vida", "")

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	assertMigrationApplied(t, db, 49)

	categories := querySeededCategoriesFromDB(t, db, "ws-mig49-business")
	assertSeedParent(t, categories, "Rendimentos financeiros", "INCOME", categorycontract.MacroReceitasFinanceiras)
	assertSeedParent(t, categories, "Retiradas e Distribuição", "EXPENSE", categorycontract.MacroRetiradasDistribuicao)
	assertSeedParent(t, categories, "Investimentos", "EXPENSE", categorycontract.MacroInvestimentosPatrimonial)
	assertSeedParent(t, categories, "Outras Entradas", "INCOME", categorycontract.MacroOutrasEntradas)
	assertSeedParent(t, categories, "Outras Saídas", "EXPENSE", categorycontract.MacroOutrasSaidas)
	assertReseedCategoryExists(t, db, "mig49-custom")
	assertReseedCategoryExists(t, db, "mig49-invest-old")
	assertReseedTransactionCategory(t, db, "tx-mig49-invest-old", "mig49-invest-old")
	if count := countReseedCategoriesByName(t, db, "ws-mig49-personal", "Rendimentos financeiros"); count != 0 {
		t.Fatalf("personal workspace received business financial category: count=%d", count)
	}

	countAfterFirstRun := countReseedCategories(t, db, "ws-mig49-business")
	if err := runMigrations(db); err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}
	countAfterSecondRun := countReseedCategories(t, db, "ws-mig49-business")
	if countAfterSecondRun != countAfterFirstRun {
		t.Fatalf("migration rerun changed business categories: first=%d second=%d", countAfterFirstRun, countAfterSecondRun)
	}
	assertNoDuplicateReseedCategories(t, db, "ws-mig49-business")
}

func TestMigration50CreatesRecurringSeriesVersioningSchemaOnFreshDatabase(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertSchemaObjectExists(t, db, "table", "recurring_series")
	assertColumnExists(t, db, "recurring_rules", "series_id")
	assertColumnExists(t, db, "recurring_rules", "parent_rule_id")
	assertColumnExists(t, db, "recurring_rules", "effective_start_sequence")
	assertColumnExists(t, db, "recurring_rules", "effective_end_sequence")
	assertColumnExists(t, db, "recurring_rules", "starts_at")
	assertColumnExists(t, db, "recurring_rules", "end_date")
	assertColumnExists(t, db, "recurring_rules", "contact_id")
	assertColumnExists(t, db, "recurring_rules", "due_date_policy")
	assertColumnExists(t, db, "recurring_rules", "due_date_offset_days")
	assertColumnExists(t, db, "transactions", "series_id")
	assertColumnExists(t, db, "transactions", "is_recurring_exception")
	assertColumnExists(t, db, "transactions", "recurring_exception_reason")
	assertSchemaObjectExists(t, db, "index", "idx_transactions_workspace_recurring_sequence_unique")
	assertSchemaObjectExists(t, db, "index", "idx_transactions_workspace_series_sequence")
	assertSchemaObjectExists(t, db, "index", "idx_transactions_workspace_series_date")
}

func TestMigration50BackfillsRecurringSeriesWithoutRewritingTransactions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration50-backfill.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	createLegacyRecurringVersioningFixture(t, db)
	seedAppliedMigrationsBefore(t, db, 50)

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	assertMigrationApplied(t, db, 50)

	var seriesCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_series`).Scan(&seriesCount); err != nil {
		t.Fatalf("count recurring_series: %v", err)
	}
	if seriesCount != 2 {
		t.Fatalf("recurring_series count = %d, want 2", seriesCount)
	}

	var (
		seriesID               sql.NullString
		effectiveStartSequence int
		contactID              sql.NullString
		dueDatePolicy          sql.NullString
	)
	if err := db.QueryRow(`
		SELECT series_id, effective_start_sequence, contact_id, due_date_policy
		FROM recurring_rules
		WHERE id = 'rule-a'
	`).Scan(&seriesID, &effectiveStartSequence, &contactID, &dueDatePolicy); err != nil {
		t.Fatalf("query backfilled recurring rule: %v", err)
	}
	if !seriesID.Valid || seriesID.String != "rule-a" {
		t.Fatalf("recurring_rules.series_id = %q valid=%v, want rule-a", seriesID.String, seriesID.Valid)
	}
	if effectiveStartSequence != 1 {
		t.Fatalf("effective_start_sequence = %d, want 1", effectiveStartSequence)
	}
	if contactID.Valid {
		t.Fatalf("contact_id = %q, want NULL during safe backfill", contactID.String)
	}
	if dueDatePolicy.Valid {
		t.Fatalf("due_date_policy = %q, want NULL during safe backfill", dueDatePolicy.String)
	}

	assertTransactionSeries50(t, db, "tx-a-1", "rule-a", 0, 1714521600)
	assertTransactionSeries50(t, db, "tx-b-1", "rule-b", 0, 1714521600)
	assertTransactionSeries50(t, db, "tx-cross-workspace", "", 0, 1717200000)
}

func TestMigration50FailsOnDuplicateRecurringRuleSequence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration50-duplicate-sequence.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	createLegacyRecurringVersioningFixture(t, db)
	seedAppliedMigrationsBefore(t, db, 50)
	if _, err := db.Exec(`
		INSERT INTO transactions (id, workspace_id, recurring_rule_id, recurrence_sequence, date)
		VALUES ('tx-a-duplicate', 'ws-a', 'rule-a', 1, 1719792000)
	`); err != nil {
		t.Fatalf("insert duplicate recurring sequence fixture: %v", err)
	}

	err = runMigrations(db)
	if err == nil {
		t.Fatalf("expected migration 50 to fail on duplicate recurring_rule_id recurrence_sequence")
	}
	msg := err.Error()
	for _, want := range []string{
		"migration 50 aborted",
		"duplicate (workspace_id, recurring_rule_id, recurrence_sequence)",
		`recurring_rule_id="rule-a"`,
		"recurrence_sequence=1",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("duplicate error message = %q, want substring %q", msg, want)
		}
	}

	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Fatalf("stat duplicate fixture db: %v", statErr)
	}
	var indexCount int
	if scanErr := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = 'idx_transactions_workspace_recurring_sequence_unique'`).Scan(&indexCount); scanErr != nil {
		t.Fatalf("query unique index after failed migration: %v", scanErr)
	}
	if indexCount != 0 {
		t.Fatalf("unique index created despite duplicate preflight failure")
	}
}

// TestMigration50ForeignKeyListMatchesFreshSchemaConstraints pins the fresh-install side of
// the intentional fresh-vs-migrated divergence documented on migrateRecurringSeriesVersioningBase.
// On a clean database the new recurring-series columns carry real FOREIGN KEY constraints
// because sqlite.go creates the tables with them inline.
func TestMigration50ForeignKeyListMatchesFreshSchemaConstraints(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	rules := foreignKeyTargets(t, db, "recurring_rules")
	assertForeignKeyTarget(t, "recurring_rules", rules, "series_id", "recurring_series")
	assertForeignKeyTarget(t, "recurring_rules", rules, "parent_rule_id", "recurring_rules")
	assertForeignKeyTarget(t, "recurring_rules", rules, "contact_id", "contacts")

	transactions := foreignKeyTargets(t, db, "transactions")
	assertForeignKeyTarget(t, "transactions", transactions, "series_id", "recurring_series")
}

// TestMigration50ForeignKeyListOmitsConstraintsOnMigratedDatabase pins the migrated-install
// side of the SAME intentional divergence. ALTER TABLE ADD COLUMN cannot attach FKs in SQLite
// and Fase 1 deliberately avoids a table rebuild, so on existing databases the new columns
// exist WITHOUT the FKs that a fresh install gets. This is known and accepted: preflight,
// indexes, and application-level validation compensate until a later phase rebuilds the tables.
func TestMigration50ForeignKeyListOmitsConstraintsOnMigratedDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration50-fk-divergence.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	createLegacyRecurringVersioningFixture(t, db)
	seedAppliedMigrationsBefore(t, db, 50)

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	assertMigrationApplied(t, db, 50)

	assertColumnExists(t, db, "recurring_rules", "series_id")
	assertColumnExists(t, db, "recurring_rules", "parent_rule_id")
	assertColumnExists(t, db, "recurring_rules", "contact_id")
	assertColumnExists(t, db, "transactions", "series_id")

	rules := foreignKeyTargets(t, db, "recurring_rules")
	assertNoForeignKeyFrom(t, "recurring_rules", rules, "series_id")
	assertNoForeignKeyFrom(t, "recurring_rules", rules, "parent_rule_id")
	assertNoForeignKeyFrom(t, "recurring_rules", rules, "contact_id")

	transactions := foreignKeyTargets(t, db, "transactions")
	assertNoForeignKeyFrom(t, "transactions", transactions, "series_id")
}

func TestDestructiveMigrationsRequireExplicitReview(t *testing.T) {
	sourceBytes, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatalf("read migrate.go: %v", err)
	}

	allowlist := map[int]string{
		12: "categories rebuild — destructive migration reviewed and allowed",
		30: "box_virtual_ledger rebuild — destructive migration reviewed and allowed",
		31: "box_virtual_ledger rebuild — destructive migration reviewed and allowed",
		35: "accounts rebuild to add WALLET type and icon column — destructive migration reviewed and allowed",
		41: "boxes rebuild to make category optional — destructive migration reviewed and allowed",
	}
	findings := destructiveMigrationFindings(t, string(sourceBytes))

	for version, patterns := range findings {
		if _, ok := allowlist[version]; ok {
			continue
		}
		t.Errorf("migration %d uses destructive SQL patterns %v without explicit review allowlist", version, patterns)
	}
	for version, reason := range allowlist {
		if _, ok := findings[version]; !ok {
			t.Errorf("destructive migration allowlist entry %d is stale or undetected: %s", version, reason)
		}
	}

	docPath := filepath.Join("..", "..", ".docs", "migrations-destrutivas.md")
	if _, err := os.Stat(docPath); err != nil {
		if os.IsNotExist(err) {
			// O runbook é documentação interna; no checkout público ele é propositalmente
			// omitido pelo export-ignore. Nesse ambiente, a verificação de existência é
			// desnecessária; o allowlist acima já garante revisão explícita.
			t.Logf("destructive migration runbook nao encontrado em %s (checkout publico?); pulando verificacao de existencia", docPath)
			return
		}
		t.Fatalf("cannot stat destructive migration runbook at %s: %v", docPath, err)
	}
}

func TestOpenCreatesPreMigrationBackupForExistingFileDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "existing.db")

	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw existing database: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE legacy_marker (id INTEGER PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
		t.Fatalf("create legacy marker: %v", err)
	}
	if _, err := rawDB.Exec(`INSERT INTO legacy_marker (note) VALUES ('before migrations')`); err != nil {
		t.Fatalf("insert legacy marker: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw existing database: %v", err)
	}

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open existing database: %v", err)
	}
	defer db.Close()

	backupPath := singlePreMigrationBackup(t, dir)
	backupDB, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open pre-migration backup: %v", err)
	}
	defer backupDB.Close()

	var note string
	if err := backupDB.QueryRow(`SELECT note FROM legacy_marker WHERE id = 1`).Scan(&note); err != nil {
		t.Fatalf("query legacy marker in backup: %v", err)
	}
	if note != "before migrations" {
		t.Fatalf("expected legacy marker from pre-migration backup, got %q", note)
	}

	var schemaMigrationTables int
	if err := backupDB.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&schemaMigrationTables); err != nil {
		t.Fatalf("check schema_migrations in backup: %v", err)
	}
	if schemaMigrationTables != 0 {
		t.Fatalf("expected backup before DDL/migrations, found schema_migrations table")
	}
}

func TestOpenSkipsPreMigrationBackupForNewFileDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "new.db")

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open new database: %v", err)
	}
	defer db.Close()

	backupDir := filepath.Join(dir, "backups", "pre-migration")
	entries, err := os.ReadDir(backupDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read pre-migration backup dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no pre-migration backup for new database, got %d entries", len(entries))
	}
}

func TestPreMigrationBackupCheckpointsWALBeforeCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "wal.db")

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open wal database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	stmts := []string{
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE wal_marker (id INTEGER PRIMARY KEY, note TEXT NOT NULL)`,
		`INSERT INTO wal_marker (note) VALUES ('checkpointed')`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("prepare wal database: %v", err)
		}
	}

	backupPath, err := createPreMigrationBackup(db, dbPath)
	if err != nil {
		t.Fatalf("create pre-migration backup with WAL: %v", err)
	}

	backupDB, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatalf("open WAL backup: %v", err)
	}
	defer backupDB.Close()

	var note string
	if err := backupDB.QueryRow(`SELECT note FROM wal_marker WHERE id = 1`).Scan(&note); err != nil {
		t.Fatalf("query WAL marker in backup: %v", err)
	}
	if note != "checkpointed" {
		t.Fatalf("expected checkpointed WAL data in backup, got %q", note)
	}
}

func TestMigration22UpdatesInvoiceReferenceAfterClosingRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	stmts := []string{
		`CREATE TABLE credit_cards (
			account_id TEXT PRIMARY KEY,
			closing_day INTEGER NOT NULL,
			due_day INTEGER NOT NULL
		)`,
		`CREATE TABLE invoices (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			reference TEXT NOT NULL,
			closing_date INTEGER NOT NULL,
			due_date INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'OPEN',
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`INSERT INTO credit_cards (account_id, closing_day, due_day) VALUES ('card-1', 10, 5)`,
		`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		 VALUES ('invoice-1', 'card-1', '2024-01', ?, ?, 'OPEN', unixepoch())`,
	}
	for _, stmt := range stmts[:3] {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed migration v22 schema: %v", err)
		}
	}
	dueDate := time.Date(2024, time.February, 5, 12, 0, 0, 0, time.UTC).Unix()
	closingDate := time.Date(2024, time.January, 10, 12, 0, 0, 0, time.UTC).Unix()
	if _, err := db.Exec(stmts[3], closingDate, dueDate); err != nil {
		t.Fatalf("seed migration v22 invoice: %v", err)
	}

	m := migrationByVersion(t, 22)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin migration v22 tx: %v", err)
	}
	if err := m.up(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("run migration v22: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration v22: %v", err)
	}

	var reference string
	if err := db.QueryRow(`SELECT reference FROM invoices WHERE id = 'invoice-1'`).Scan(&reference); err != nil {
		t.Fatalf("query migrated invoice reference: %v", err)
	}
	if reference != "2024-02" {
		t.Fatalf("expected reference 2024-02, got %s", reference)
	}
}

func TestOpenMigratesLegacyDatabaseAndReopenIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy database: %v", err)
	}

	legacyDDL := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			initial_balance INTEGER NOT NULL DEFAULT 0,
			current_balance INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE categories (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			icon TEXT NOT NULL DEFAULT 'tag',
			color TEXT NOT NULL DEFAULT '#6b7280',
			type TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE invoices (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			reference TEXT NOT NULL,
			closing_date INTEGER NOT NULL,
			due_date INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'OPEN',
			paid_at INTEGER,
			paid_amount INTEGER,
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE transactions (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			destination_account_id TEXT,
			category_id TEXT,
			invoice_id TEXT,
			invoice_override_id TEXT,
			type TEXT NOT NULL,
			amount INTEGER NOT NULL,
			date INTEGER NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			installment_number INTEGER NOT NULL DEFAULT 1,
			total_installments INTEGER NOT NULL DEFAULT 1,
			parent_id TEXT,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE boxes (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			category_id TEXT NOT NULL,
			target_amount INTEGER NOT NULL DEFAULT 0,
			monthly_recharge INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE box_virtual_ledger (
			id TEXT PRIMARY KEY,
			box_id TEXT NOT NULL,
			amount INTEGER NOT NULL,
			type TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			reference_date INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
	}
	for _, stmt := range legacyDDL {
		if _, err := rawDB.Exec(stmt); err != nil {
			t.Fatalf("seed legacy DDL: %v", err)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw legacy database: %v", err)
	}

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy database: %v", err)
	}

	assertColumnExists(t, db, "transactions", "payment_status")
	assertColumnExists(t, db, "transactions", "recurring_rule_id")
	assertColumnExists(t, db, "transactions", "recurrence_sequence")
	assertColumnExists(t, db, "transactions", "notes")
	assertColumnExists(t, db, "transactions", "attachment_path")
	assertColumnExists(t, db, "users", "last_notifications_clear_at")
	assertColumnExists(t, db, "users", "status")
	assertColumnExists(t, db, "users", "profile_photo_path")
	assertColumnExists(t, db, "users", "activation_token_hash")
	assertColumnExists(t, db, "users", "activation_expires_at")
	assertColumnExists(t, db, "users", "totp_enabled")
	assertColumnExists(t, db, "users", "totp_secret_enc")
	assertColumnExists(t, db, "users", "totp_backup_codes")
	assertColumnExists(t, db, "users", "totp_enabled_at")
	assertColumnExists(t, db, "users", "must_change_password")
	assertColumnExists(t, db, "users", "temporary_password_expires_at")
	assertColumnExists(t, db, "categories", "is_fixed")
	assertColumnExists(t, db, "categories", "macro_group")
	assertColumnExists(t, db, "categories", "parent_id")
	assertColumnExists(t, db, "accounts", "color")
	assertColumnExists(t, db, "accounts", "provider_slug")
	assertColumnExists(t, db, "workspaces", "theme_token")
	assertColumnExists(t, db, "workspaces", "smtp_host")
	assertColumnExists(t, db, "workspaces", "smtp_port")
	assertColumnExists(t, db, "workspaces", "smtp_user")
	assertColumnExists(t, db, "workspaces", "smtp_pass")
	assertColumnExists(t, db, "workspaces", "notification_email")
	assertColumnExists(t, db, "workspaces", "email_preferences")
	assertColumnExists(t, db, "contacts", "custom_client_id")
	assertColumnExists(t, db, "workspace_members", "custom_permissions")
	assertColumnExists(t, db, "boxes", "target_date")
	assertColumnExists(t, db, "box_virtual_ledger", "source_transaction_id")
	assertColumnExists(t, db, "box_virtual_ledger", "reversal_of_ledger_id")
	assertColumnExists(t, db, "sessions", "workspace_id")
	assertTableExists(t, db, "pre_auth_sessions")
	assertTableExists(t, db, "auth_audit_events")
	assertTableExists(t, db, "security_logs")
	assertTableExists(t, db, "user_notifications")

	var migrationCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count schema_migrations after migrate: %v", err)
	}
	if migrationCount != len(migrations) {
		t.Fatalf("expected %d applied migrations after migrate, got %d", len(migrations), migrationCount)
	}
	db.Close()

	db2, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer db2.Close()

	if err := db2.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count schema_migrations after reopen: %v", err)
	}
	if migrationCount != len(migrations) {
		t.Fatalf("expected %d applied migrations after reopen, got %d", len(migrations), migrationCount)
	}
}

func TestMigration28AddsCustomPermissionsWhenPreviousMigrationsAreMarkedApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-workspace-members.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy workspace members database: %v", err)
	}
	seedWorkspaceMembersTable(t, rawDB, false)
	seedAppliedMigrationsBefore(t, rawDB, 28)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw legacy workspace members database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	assertColumnExists(t, db, "workspace_members", "custom_permissions")
	assertMigrationApplied(t, db, 28)
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated legacy workspace members database: %v", err)
	}

	db2 := openMigratedRawDB(t, dbPath)
	defer db2.Close()
	assertColumnExists(t, db2, "workspace_members", "custom_permissions")
	assertMigrationApplied(t, db2, 28)
}

func TestMigration28RegistersWhenCustomPermissionsAlreadyExists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "patched-workspace-members.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw patched workspace members database: %v", err)
	}
	seedWorkspaceMembersTable(t, rawDB, true)
	if _, err := rawDB.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, custom_permissions, joined_at) VALUES ('workspace-1', 'user-1', 'USER', '', unixepoch())`); err != nil {
		t.Fatalf("seed patched workspace member: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 28)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw patched workspace members database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertColumnExists(t, db, "workspace_members", "custom_permissions")
	assertMigrationApplied(t, db, 28)

	var permissions string
	if err := db.QueryRow(`SELECT custom_permissions FROM workspace_members WHERE workspace_id = 'workspace-1' AND user_id = 'user-1'`).Scan(&permissions); err != nil {
		t.Fatalf("query normalized custom_permissions: %v", err)
	}
	if permissions != "[]" {
		t.Fatalf("expected blank custom_permissions normalized to [], got %q", permissions)
	}
}

func TestOpenMigratesLegacyContactsWithoutCustomClientID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-contacts.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy contacts database: %v", err)
	}
	legacyDDL := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL
		)`,
		`CREATE TABLE contacts (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			document TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL CHECK (type IN ('client', 'vendor')),
			email TEXT NOT NULL DEFAULT '',
			phone TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
	}
	for _, stmt := range legacyDDL {
		if _, err := rawDB.Exec(stmt); err != nil {
			t.Fatalf("seed legacy contacts DDL: %v", err)
		}
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw legacy contacts database: %v", err)
	}

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open migrated legacy contacts database: %v", err)
	}
	defer db.Close()

	assertColumnExists(t, db, "contacts", "custom_client_id")
	assertColumnExists(t, db, "workspaces", "theme_token")
}

func TestMigration29AddsBoxesTargetDateWhenPreviousMigrationsAreMarkedApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-boxes.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy boxes database: %v", err)
	}
	seedBoxesTable(t, rawDB, false)
	seedAppliedMigrationsBefore(t, rawDB, 29)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw legacy boxes database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertColumnExists(t, db, "boxes", "target_date")
	assertMigrationApplied(t, db, 29)
}

func TestMigration29RegistersWhenTargetDateAlreadyExists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "patched-boxes.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw patched boxes database: %v", err)
	}
	seedBoxesTable(t, rawDB, true)
	if _, err := rawDB.Exec(`INSERT INTO boxes (id, workspace_id, name, category_id, target_date, created_at, updated_at) VALUES ('box-1', 'workspace-1', 'Box 1', 'category-1', 0, unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("seed patched box: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 29)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw patched boxes database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertColumnExists(t, db, "boxes", "target_date")
	assertMigrationApplied(t, db, 29)

	var targetDate sql.NullInt64
	if err := db.QueryRow(`SELECT target_date FROM boxes WHERE id = 'box-1'`).Scan(&targetDate); err != nil {
		t.Fatalf("query target_date: %v", err)
	}
	if !targetDate.Valid || targetDate.Int64 != 0 {
		t.Fatalf("expected target_date persisted as 0, got valid=%v value=%d", targetDate.Valid, targetDate.Int64)
	}
}

func TestMigration30AllowsReleaseEventsWhenPreviousMigrationsAreMarkedApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-ledger.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw legacy ledger database: %v", err)
	}
	seedLegacyBoxVirtualLedgerTable(t, rawDB)
	seedAppliedMigrationsBefore(t, rawDB, 30)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw legacy ledger database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 30)

	if _, err := db.Exec(`INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES ('l2', 'box-1', -500, 'RELEASE', 'Liberação de reserva', unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("insert release ledger event after migration: %v", err)
	}

	var total int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'box-1'`).Scan(&total); err != nil {
		t.Fatalf("sum ledger amounts: %v", err)
	}
	if total != 500 {
		t.Fatalf("ledger total after release = %d, want 500", total)
	}
}

func TestMigration31SupportsConsumeAndReversalEventsWhenPreviousMigrationsAreMarkedApplied(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-ledger-v30.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw v30 ledger database: %v", err)
	}
	seedV30BoxVirtualLedgerTable(t, rawDB)
	seedAppliedMigrationsBefore(t, rawDB, 31)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw v30 ledger database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 31)
	assertColumnExists(t, db, "box_virtual_ledger", "source_transaction_id")
	assertColumnExists(t, db, "box_virtual_ledger", "reversal_of_ledger_id")

	if _, err := db.Exec(`INSERT INTO box_virtual_ledger
		(id, box_id, amount, type, description, source_transaction_id, reversal_of_ledger_id, reference_date, created_at)
		VALUES ('l3', 'box-1', -200, 'CONSUME', 'Consumo automático por lançamento', 'tx-1', NULL, unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("insert consume ledger event after migration: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO box_virtual_ledger
		(id, box_id, amount, type, description, source_transaction_id, reversal_of_ledger_id, reference_date, created_at)
		VALUES ('l4', 'box-1', 200, 'REVERSAL', 'Reversão de consumo', 'tx-1', 'l3', unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("insert reversal ledger event after migration: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO box_virtual_ledger
		(id, box_id, amount, type, description, source_transaction_id, reversal_of_ledger_id, reference_date, created_at)
		VALUES ('l5', 'box-1', 200, 'CONSUME', 'invalid', 'tx-2', NULL, unixepoch(), unixepoch())`); err == nil {
		t.Fatalf("expected consume with positive amount to fail check constraint")
	}

	var referenceID sql.NullString
	if err := db.QueryRow(`SELECT reversal_of_ledger_id FROM box_virtual_ledger WHERE id = 'l4'`).Scan(&referenceID); err != nil {
		t.Fatalf("query reversal_of_ledger_id: %v", err)
	}
	if !referenceID.Valid || referenceID.String != "l3" {
		t.Fatalf("expected reversal linked to l3, got valid=%v value=%q", referenceID.Valid, referenceID.String)
	}

	var total int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'box-1'`).Scan(&total); err != nil {
		t.Fatalf("sum ledger amounts: %v", err)
	}
	if total != 500 {
		t.Fatalf("ledger total after consume+reversal = %d, want 500", total)
	}
}

func TestMigration32AddsMonthlyYieldRateToBoxes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "boxes-pre-yield.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw pre-yield database: %v", err)
	}
	seedBoxesTable(t, rawDB, true)
	if _, err := rawDB.Exec(`INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, target_date, created_at, updated_at)
		VALUES ('box-1', 'workspace-1', 'Test Box', 'cat-1', 10000, 500, NULL, unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("seed box fixture: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 32)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw pre-yield database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 32)
	assertColumnExists(t, db, "boxes", "monthly_yield_rate")

	var rate float64
	if err := db.QueryRow(`SELECT monthly_yield_rate FROM boxes WHERE id = 'box-1'`).Scan(&rate); err != nil {
		t.Fatalf("query monthly_yield_rate after migration: %v", err)
	}
	if rate != 0.0 {
		t.Fatalf("monthly_yield_rate default = %f, want 0.0", rate)
	}

	if _, err := db.Exec(`UPDATE boxes SET monthly_yield_rate = 0.008 WHERE id = 'box-1'`); err != nil {
		t.Fatalf("update monthly_yield_rate: %v", err)
	}
	if err := db.QueryRow(`SELECT monthly_yield_rate FROM boxes WHERE id = 'box-1'`).Scan(&rate); err != nil {
		t.Fatalf("query updated monthly_yield_rate: %v", err)
	}
	if rate != 0.008 {
		t.Fatalf("monthly_yield_rate after update = %f, want 0.008", rate)
	}
}

func TestMigration41AllowsBoxesWithoutCategory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "boxes-category-nullable.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw boxes-category-nullable database: %v", err)
	}
	seedBoxesTable(t, rawDB, true)
	if _, err := rawDB.Exec(`ALTER TABLE boxes ADD COLUMN monthly_yield_rate REAL NOT NULL DEFAULT 0.0`); err != nil {
		t.Fatalf("add monthly_yield_rate fixture: %v", err)
	}
	if _, err := rawDB.Exec(`INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, target_date, monthly_yield_rate, created_at, updated_at)
		VALUES ('box-1', 'workspace-1', 'Test Box', 'category-1', 10000, 500, NULL, 0.008, unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("seed box fixture: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE INDEX boxes_custom_name_idx ON boxes(name)`); err != nil {
		t.Fatalf("create custom boxes index fixture: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE box_audit (box_id TEXT NOT NULL, category_id TEXT)`); err != nil {
		t.Fatalf("create box audit fixture: %v", err)
	}
	if _, err := rawDB.Exec(`
		CREATE TRIGGER boxes_custom_category_audit
		AFTER UPDATE OF category_id ON boxes
		BEGIN
			INSERT INTO box_audit (box_id, category_id) VALUES (NEW.id, NEW.category_id);
		END
	`); err != nil {
		t.Fatalf("create custom boxes trigger fixture: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 41)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw boxes-category-nullable database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 41)
	assertColumnNullable(t, db, "boxes", "category_id")
	assertSchemaObjectExists(t, db, "index", "boxes_custom_name_idx")
	assertSchemaObjectExists(t, db, "trigger", "boxes_custom_category_audit")

	if _, err := db.Exec(`UPDATE boxes SET category_id = NULL WHERE id = 'box-1'`); err != nil {
		t.Fatalf("clear box category_id after migration: %v", err)
	}
	var categoryID sql.NullString
	var targetAmount, monthlyRecharge int64
	var targetDate sql.NullInt64
	var monthlyYieldRate float64
	if err := db.QueryRow(`SELECT category_id, target_amount, monthly_recharge, target_date, monthly_yield_rate FROM boxes WHERE id = 'box-1'`).
		Scan(&categoryID, &targetAmount, &monthlyRecharge, &targetDate, &monthlyYieldRate); err != nil {
		t.Fatalf("query migrated box: %v", err)
	}
	if categoryID.Valid {
		t.Fatalf("category_id valid = true, want NULL")
	}
	if targetAmount != 10000 || monthlyRecharge != 500 || targetDate.Valid || monthlyYieldRate != 0.008 {
		t.Fatalf("migrated fields = target=%d recharge=%d dateValid=%v yield=%f", targetAmount, monthlyRecharge, targetDate.Valid, monthlyYieldRate)
	}
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_audit WHERE box_id = 'box-1' AND category_id IS NULL`).Scan(&auditCount); err != nil {
		t.Fatalf("query box audit trigger result: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("box audit rows = %d, want 1", auditCount)
	}
}

func TestMigration35BackfillsLegacyAccountsRepairsWorkspaceFKAndReopenIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "accounts-pre-migration-35.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw pre-migration-35 database: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL
		)`,
		`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL
		)`,
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			color TEXT NOT NULL DEFAULT '',
			provider_slug TEXT NOT NULL DEFAULT '',
			initial_balance INTEGER NOT NULL DEFAULT 0,
			current_balance INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`CREATE TABLE credit_cards (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL UNIQUE,
			closing_day INTEGER NOT NULL,
			due_day INTEGER NOT NULL,
			credit_limit INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE invoices (
			id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			reference TEXT NOT NULL,
			closing_date INTEGER NOT NULL,
			due_date INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'OPEN',
			FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE transactions (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			account_id TEXT NOT NULL,
			destination_account_id TEXT,
			type TEXT NOT NULL,
			amount INTEGER NOT NULL,
			date INTEGER NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE RESTRICT,
			FOREIGN KEY (destination_account_id) REFERENCES accounts(id) ON DELETE SET NULL
		)`,
		`INSERT INTO users (id, name, email, password_hash) VALUES ('user-1', 'Legacy User', 'legacy@example.test', 'hash')`,
		`INSERT INTO workspaces (id, name) VALUES ('workspace-main', 'Workspace Principal')`,
		`INSERT INTO accounts (id, workspace_id, name, type, color, provider_slug, initial_balance, current_balance)
		 VALUES ('account-orphan-pagbank', 'workspace-missing', 'PagBank Antigo', 'CHECKING', '', '', 12345, 67890)`,
		`INSERT INTO accounts (id, workspace_id, name, type, color, provider_slug, initial_balance, current_balance)
		 VALUES ('account-custom-invalid', 'workspace-main', 'Carteira antiga', 'LEGACY_CASH', '', '', 900, 1200)`,
		`INSERT INTO accounts (id, workspace_id, name, type, color, provider_slug, initial_balance, current_balance)
		 VALUES ('account-picpay', 'workspace-main', 'PicPay pessoal', 'WALLET', '#123456', '', 100, 200)`,
		`INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		 VALUES ('card-1', 'account-orphan-pagbank', 10, 20, 50000)`,
		`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status)
		 VALUES ('invoice-1', 'account-orphan-pagbank', '2026-06', unixepoch(), unixepoch(), 'OPEN')`,
		`INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description)
		 VALUES ('tx-1', 'workspace-main', 'user-1', 'account-orphan-pagbank', 'account-custom-invalid', 'TRANSFER', 700, unixepoch(), 'fixture')`,
	}
	for _, stmt := range stmts {
		if _, err := rawDB.Exec(stmt); err != nil {
			_ = rawDB.Close()
			t.Fatalf("seed pre-migration-35 fixture: %v", err)
		}
	}
	seedAppliedMigrationsBefore(t, rawDB, 35)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw pre-migration-35 database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	assertMigrationApplied(t, db, 35)
	assertColumnExists(t, db, "accounts", "icon")
	assertNoForeignKeyCheckRows(t, db)

	assertMigratedAccount35(t, db, "account-orphan-pagbank", migratedAccount35{
		WorkspaceID:    "workspace-main",
		Type:           "CHECKING",
		Color:          "#FFE72D",
		Icon:           "building-2",
		ProviderSlug:   "pagbank",
		InitialBalance: 12345,
		CurrentBalance: 67890,
	})
	assertMigratedAccount35(t, db, "account-custom-invalid", migratedAccount35{
		WorkspaceID:    "workspace-main",
		Type:           "CHECKING",
		Color:          "#6B7280",
		Icon:           "wallet",
		ProviderSlug:   "custom",
		InitialBalance: 900,
		CurrentBalance: 1200,
	})
	assertMigratedAccount35(t, db, "account-picpay", migratedAccount35{
		WorkspaceID:    "workspace-main",
		Type:           "WALLET",
		Color:          "#123456",
		Icon:           "wallet-cards",
		ProviderSlug:   "picpay",
		InitialBalance: 100,
		CurrentBalance: 200,
	})
	assertTableRowCount(t, db, "credit_cards", 1)
	assertTableRowCount(t, db, "invoices", 1)
	assertTableRowCount(t, db, "transactions", 1)
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated pre-migration-35 database: %v", err)
	}

	db2 := openMigratedRawDB(t, dbPath)
	defer db2.Close()
	assertMigrationApplied(t, db2, 35)
	assertNoForeignKeyCheckRows(t, db2)
	assertMigratedAccount35(t, db2, "account-orphan-pagbank", migratedAccount35{
		WorkspaceID:    "workspace-main",
		Type:           "CHECKING",
		Color:          "#FFE72D",
		Icon:           "building-2",
		ProviderSlug:   "pagbank",
		InitialBalance: 12345,
		CurrentBalance: 67890,
	})
	assertTableRowCount(t, db2, "credit_cards", 1)
	assertTableRowCount(t, db2, "invoices", 1)
	assertTableRowCount(t, db2, "transactions", 1)
}

func TestMigration36AddsArchivedAtColumnAndIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pre-archived.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw pre-archived database: %v", err)
	}
	stmts := []string{
		`CREATE TABLE accounts (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			initial_balance INTEGER NOT NULL DEFAULT 0,
			current_balance INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`,
		`INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-1', 'ws-1', 'Test', 'CHECKING')`,
	}
	for _, stmt := range stmts {
		if _, err := rawDB.Exec(stmt); err != nil {
			t.Fatalf("seed legacy accounts fixture: %v", err)
		}
	}
	seedAppliedMigrationsBefore(t, rawDB, 36)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw legacy database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 36)
	assertColumnExists(t, db, "accounts", "archived_at")

	var archived interface{}
	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'acc-1'`).Scan(&archived); err != nil {
		t.Fatalf("query archived_at after migration: %v", err)
	}
	if archived != nil {
		t.Fatalf("archived_at for existing account = %v, want nil (active)", archived)
	}
}

func TestMigration39CreatesInvoicePaymentsSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "invoice-payments-schema.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 39)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 39)
	assertTableExists(t, db, "invoice_payments")
	assertColumnExists(t, db, "invoice_payments", "id")
	assertColumnExists(t, db, "invoice_payments", "workspace_id")
	assertColumnExists(t, db, "invoice_payments", "invoice_id")
	assertColumnExists(t, db, "invoice_payments", "account_id")
	assertColumnExists(t, db, "invoice_payments", "amount_cents")
}

func assertTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	if count != 1 {
		t.Fatalf("table %s not found", table)
	}
}

func TestMigration51CreatesRecurringMaterializationStateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recurring-materialization-state.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 51)
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 51)
	assertTableExists(t, db, "recurring_materialization_state")
	assertColumnExists(t, db, "recurring_materialization_state", "workspace_id")
	assertColumnExists(t, db, "recurring_materialization_state", "materialized_until")
	assertColumnExists(t, db, "recurring_materialization_state", "last_attempt_at")
	assertColumnExists(t, db, "recurring_materialization_state", "last_success_at")
	assertColumnExists(t, db, "recurring_materialization_state", "last_error")
	assertColumnExists(t, db, "recurring_materialization_state", "status")
	assertColumnExists(t, db, "recurring_materialization_state", "locked_until")
	assertColumnExists(t, db, "recurring_materialization_state", "lock_owner")

	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = 'idx_recurring_materialization_state_locked_until'`).Scan(&indexCount); err != nil {
		t.Fatalf("check locked_until index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("expected idx_recurring_materialization_state_locked_until to exist")
	}
}

func singlePreMigrationBackup(t *testing.T, dataDir string) string {
	t.Helper()
	backupDir := filepath.Join(dataDir, "backups", "pre-migration")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("read pre-migration backup dir: %v", err)
	}

	var backups []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".db" {
			continue
		}
		backups = append(backups, filepath.Join(backupDir, entry.Name()))
	}
	if len(backups) != 1 {
		t.Fatalf("expected one pre-migration backup, got %d", len(backups))
	}

	return backups[0]
}

func destructiveMigrationFindings(t *testing.T, source string) map[int][]string {
	t.Helper()

	patterns := map[string]*regexp.Regexp{
		"DROP TABLE":             regexp.MustCompile(`(?is)\bDROP\s+TABLE\b`),
		"RENAME TO":              regexp.MustCompile(`(?is)\bRENAME\s+TO\b`),
		"CREATE TABLE *_new":     regexp.MustCompile(`(?is)\bCREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+\w+_new\b`),
		"CREATE TABLE AS SELECT": regexp.MustCompile(`(?is)\bCREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+\w+\s+AS\s+SELECT\b`),
	}

	found := make(map[int]map[string]bool)
	for label, pattern := range patterns {
		for _, location := range pattern.FindAllStringIndex(source, -1) {
			version, ok := migrationVersionBefore(source, location[0])
			if !ok {
				t.Fatalf("destructive SQL pattern %q found outside a migration version", label)
			}
			if found[version] == nil {
				found[version] = make(map[string]bool)
			}
			found[version][label] = true
		}
	}

	result := make(map[int][]string)
	for version, patternSet := range found {
		for label := range patternSet {
			result[version] = append(result[version], label)
		}
		sort.Strings(result[version])
	}
	return result
}

func migrationVersionBefore(source string, index int) (int, bool) {
	versionPattern := regexp.MustCompile(`version:\s*(\d+)`)
	matches := versionPattern.FindAllStringSubmatch(source[:index], -1)
	if len(matches) == 0 {
		return 0, false
	}

	version, err := strconv.Atoi(matches[len(matches)-1][1])
	if err != nil {
		return 0, false
	}
	return version, true
}

func seedWorkspaceMembersTable(t *testing.T, db *sql.DB, withCustomPermissions bool) {
	t.Helper()

	stmt := `CREATE TABLE workspace_members (
		workspace_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'USER',
		joined_at INTEGER NOT NULL DEFAULT (unixepoch()),
		PRIMARY KEY (workspace_id, user_id)
	)`
	if withCustomPermissions {
		stmt = `CREATE TABLE workspace_members (
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'USER',
			custom_permissions TEXT,
			joined_at INTEGER NOT NULL DEFAULT (unixepoch()),
			PRIMARY KEY (workspace_id, user_id)
		)`
	}
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create workspace_members fixture: %v", err)
	}
}

func seedBoxesTable(t *testing.T, db *sql.DB, withTargetDate bool) {
	t.Helper()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS workspaces (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'personal',
		created_at INTEGER NOT NULL DEFAULT (unixepoch()),
		updated_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`); err != nil {
		t.Fatalf("create boxes fixture workspaces table: %v", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('workspace-1', 'Workspace 1', '', 'personal', unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("seed boxes fixture workspace: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS categories (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'EXPENSE',
		created_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`); err != nil {
		t.Fatalf("create boxes fixture categories table: %v", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO categories (id, workspace_id, name, type, created_at)
		VALUES ('category-1', 'workspace-1', 'Category 1', 'EXPENSE', unixepoch()),
			('cat-1', 'workspace-1', 'Cat 1', 'EXPENSE', unixepoch())`); err != nil {
		t.Fatalf("seed boxes fixture categories: %v", err)
	}

	stmt := `CREATE TABLE boxes (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		name TEXT NOT NULL,
		category_id TEXT NOT NULL,
		target_amount INTEGER NOT NULL DEFAULT 0,
		monthly_recharge INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()),
		updated_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`
	if withTargetDate {
		stmt = `CREATE TABLE boxes (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			name TEXT NOT NULL,
			category_id TEXT NOT NULL,
			target_amount INTEGER NOT NULL DEFAULT 0,
			monthly_recharge INTEGER NOT NULL DEFAULT 0,
			target_date INTEGER,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)`
	}
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create boxes fixture: %v", err)
	}
}

func seedLegacyBoxVirtualLedgerTable(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE boxes (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL
		)`,
		`CREATE TABLE box_virtual_ledger (
			id TEXT PRIMARY KEY,
			box_id TEXT NOT NULL,
			amount INTEGER NOT NULL CHECK (amount > 0),
			type TEXT NOT NULL CHECK (type IN ('RECHARGE', 'BONUS')),
			description TEXT NOT NULL DEFAULT '',
			reference_date INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
		)`,
		`INSERT INTO boxes (id, workspace_id) VALUES ('box-1', 'workspace-1')`,
		`INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		 VALUES ('l1', 'box-1', 1000, 'RECHARGE', 'seed', unixepoch(), unixepoch())`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy ledger fixture: %v", err)
		}
	}
}

func seedV30BoxVirtualLedgerTable(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE boxes (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL
		)`,
		`CREATE TABLE box_virtual_ledger (
			id TEXT PRIMARY KEY,
			box_id TEXT NOT NULL,
			amount INTEGER NOT NULL CHECK (amount != 0),
			type TEXT NOT NULL CHECK (type IN ('RECHARGE', 'BONUS', 'RELEASE')),
			description TEXT NOT NULL DEFAULT '',
			reference_date INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			FOREIGN KEY (box_id) REFERENCES boxes(id) ON DELETE CASCADE
		)`,
		`INSERT INTO boxes (id, workspace_id) VALUES ('box-1', 'workspace-1')`,
		`INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		 VALUES ('l1', 'box-1', 1000, 'RECHARGE', 'seed', unixepoch(), unixepoch())`,
		`INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		 VALUES ('l2', 'box-1', -500, 'RELEASE', 'Liberação de reserva', unixepoch(), unixepoch())`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed v30 ledger fixture: %v", err)
		}
	}
}

func seedAppliedMigrationsBefore(t *testing.T, db *sql.DB, version int) {
	t.Helper()

	if _, err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`); err != nil {
		t.Fatalf("create schema_migrations fixture: %v", err)
	}
	for _, m := range migrations {
		if m.version >= version {
			continue
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, unixepoch())`, m.version, m.name); err != nil {
			t.Fatalf("seed migration %d fixture: %v", m.version, err)
		}
	}
}

func createLegacyRecurringVersioningFixture(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE workspaces (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL
		)`,
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			password_hash TEXT NOT NULL
		)`,
		`CREATE TABLE recurring_rules (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			total_occurrences INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE transactions (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			recurring_rule_id TEXT,
			recurrence_sequence INTEGER,
			date INTEGER NOT NULL
		)`,
		`INSERT INTO workspaces (id, name, type) VALUES ('ws-a', 'Workspace A', 'personal')`,
		`INSERT INTO workspaces (id, name, type) VALUES ('ws-b', 'Workspace B', 'business')`,
		`INSERT INTO users (id, name, email, password_hash) VALUES ('user-a', 'User A', 'a@example.test', 'hash')`,
		`INSERT INTO users (id, name, email, password_hash) VALUES ('user-b', 'User B', 'b@example.test', 'hash')`,
		`INSERT INTO recurring_rules (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
			VALUES ('rule-a', 'ws-a', 'user-a', 1, 12, 1711929600, 1711929700)`,
		`INSERT INTO recurring_rules (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
			VALUES ('rule-b', 'ws-b', 'user-b', 1, NULL, 1711929600, 1711929800)`,
		`INSERT INTO transactions (id, workspace_id, recurring_rule_id, recurrence_sequence, date)
			VALUES ('tx-a-1', 'ws-a', 'rule-a', 1, 1714521600)`,
		`INSERT INTO transactions (id, workspace_id, recurring_rule_id, recurrence_sequence, date)
			VALUES ('tx-b-1', 'ws-b', 'rule-b', 1, 1714521600)`,
		`INSERT INTO transactions (id, workspace_id, recurring_rule_id, recurrence_sequence, date)
			VALUES ('tx-cross-workspace', 'ws-b', 'rule-a', 9, 1717200000)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("execute recurring versioning legacy fixture: %v", err)
		}
	}
}

func assertTransactionSeries50(t *testing.T, db *sql.DB, transactionID, wantSeriesID string, wantException int, wantDate int64) {
	t.Helper()

	var (
		gotSeriesID  sql.NullString
		gotException int
		gotDate      int64
		reason       sql.NullString
	)
	if err := db.QueryRow(`
		SELECT series_id, is_recurring_exception, date, recurring_exception_reason
		FROM transactions
		WHERE id = ?
	`, transactionID).Scan(&gotSeriesID, &gotException, &gotDate, &reason); err != nil {
		t.Fatalf("query transaction %s series fields: %v", transactionID, err)
	}
	if wantSeriesID == "" {
		if gotSeriesID.Valid {
			t.Fatalf("transaction %s series_id = %q, want NULL", transactionID, gotSeriesID.String)
		}
	} else if !gotSeriesID.Valid || gotSeriesID.String != wantSeriesID {
		t.Fatalf("transaction %s series_id = %q valid=%v, want %q", transactionID, gotSeriesID.String, gotSeriesID.Valid, wantSeriesID)
	}
	if gotException != wantException {
		t.Fatalf("transaction %s is_recurring_exception = %d, want %d", transactionID, gotException, wantException)
	}
	if gotDate != wantDate {
		t.Fatalf("transaction %s date = %d, want %d", transactionID, gotDate, wantDate)
	}
	if reason.Valid {
		t.Fatalf("transaction %s recurring_exception_reason = %q, want NULL", transactionID, reason.String)
	}
}

func openMigratedRawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw migrated database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping raw migrated database: %v", err)
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		t.Fatalf("run migrations on raw database: %v", err)
	}
	return db
}

func assertMigrationApplied(t *testing.T, db *sql.DB, version int) {
	t.Helper()

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&count); err != nil {
		t.Fatalf("check migration %d applied: %v", version, err)
	}
	if count != 1 {
		t.Fatalf("expected migration %d applied, got count %d", version, count)
	}
}

type migratedAccount35 struct {
	WorkspaceID    string
	Type           string
	Color          string
	Icon           string
	ProviderSlug   string
	InitialBalance int64
	CurrentBalance int64
}

func assertMigratedAccount35(t *testing.T, db *sql.DB, accountID string, want migratedAccount35) {
	t.Helper()

	var got migratedAccount35
	if err := db.QueryRow(`
		SELECT workspace_id, type, color, icon, provider_slug, initial_balance, current_balance
		FROM accounts
		WHERE id = ?
	`, accountID).Scan(&got.WorkspaceID, &got.Type, &got.Color, &got.Icon, &got.ProviderSlug, &got.InitialBalance, &got.CurrentBalance); err != nil {
		t.Fatalf("query migrated account %s: %v", accountID, err)
	}
	if got != want {
		t.Fatalf("migrated account %s = %+v, want %+v", accountID, got, want)
	}
}

func assertNoForeignKeyCheckRows(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatalf("scan foreign_key_check row: %v", err)
		}
		if rowID.Valid {
			t.Fatalf("expected empty foreign_key_check, got table=%s rowid=%d parent=%s fk=%d", table, rowID.Int64, parent, fkID)
		}
		t.Fatalf("expected empty foreign_key_check, got table=%s rowid=NULL parent=%s fk=%d", table, parent, fkID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_check rows: %v", err)
	}
}

func assertTableRowCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()

	var got int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COUNT(1) FROM %s`, table)).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("row count %s = %d, want %d", table, got, want)
	}
}

func assertColumnExists(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()

	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		if name == column {
			return
		}
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("rows error table_info %s: %v", table, err)
	}
	t.Fatalf("column %s.%s not found", table, column)
}

func assertColumnNullable(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()

	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			if notNull != 0 {
				t.Fatalf("%s.%s notnull = %d, want 0", table, column, notNull)
			}
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s) rows: %v", table, err)
	}
	t.Fatalf("column %s.%s not found", table, column)
}

func assertSchemaObjectExists(t *testing.T, db *sql.DB, objectType, name string) {
	t.Helper()

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = ? AND name = ?`, objectType, name).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for %s %s: %v", objectType, name, err)
	}
	if count != 1 {
		t.Fatalf("schema object %s %s count = %d, want 1", objectType, name, count)
	}
}

func foreignKeyTargets(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()

	rows, err := db.Query("PRAGMA foreign_key_list(" + table + ")")
	if err != nil {
		t.Fatalf("foreign_key_list %s: %v", table, err)
	}
	defer rows.Close()

	targets := make(map[string]string)
	for rows.Next() {
		var (
			id       int
			seq      int
			refTable string
			from     string
			to       sql.NullString
			onUpdate string
			onDelete string
			match    string
		)
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign_key_list %s: %v", table, err)
		}
		targets[from] = refTable
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("foreign_key_list %s rows: %v", table, err)
	}
	return targets
}

func assertForeignKeyTarget(t *testing.T, table string, targets map[string]string, from, wantTable string) {
	t.Helper()

	got, ok := targets[from]
	if !ok {
		t.Fatalf("%s.%s missing foreign key, want -> %s", table, from, wantTable)
	}
	if got != wantTable {
		t.Fatalf("%s.%s foreign key -> %s, want -> %s", table, from, got, wantTable)
	}
}

func assertNoForeignKeyFrom(t *testing.T, table string, targets map[string]string, from string) {
	t.Helper()

	if got, ok := targets[from]; ok {
		t.Fatalf("%s.%s has foreign key -> %s on migrated database, want none (intentional fresh-vs-migrated divergence in Fase 1)", table, from, got)
	}
}

func TestMigration42AddsUsersDeletedColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "users-deleted-columns.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	seedAppliedMigrationsBefore(t, rawDB, 42)

	now := time.Now().Unix()
	_, err = rawDB.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			profile_photo_path TEXT NOT NULL DEFAULT '',
			default_workspace_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			activation_token_hash TEXT,
			activation_expires_at INTEGER,
			must_change_password INTEGER NOT NULL DEFAULT 0,
			temporary_password_expires_at INTEGER,
			totp_enabled INTEGER NOT NULL DEFAULT 0,
			totp_secret_enc TEXT,
			totp_backup_codes TEXT NOT NULL DEFAULT '[]',
			totp_enabled_at INTEGER,
			last_notifications_clear_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		)
	`)
	if err != nil {
		t.Fatalf("create legacy users table: %v", err)
	}
	_, err = rawDB.Exec(`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy-user", "Legacy User", "legacy@example.com", "hash", now, now)
	if err != nil {
		t.Fatalf("insert legacy user: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db := openMigratedRawDB(t, dbPath)
	defer db.Close()

	assertMigrationApplied(t, db, 42)
	assertColumnExists(t, db, "users", "deleted_at")
	assertColumnExists(t, db, "users", "deleted_reason")

	var deletedAt int64
	var deletedReason sql.NullString
	if err := db.QueryRow(`SELECT deleted_at, deleted_reason FROM users WHERE id = ?`, "legacy-user").Scan(&deletedAt, &deletedReason); err != nil {
		t.Fatalf("query legacy user deleted columns: %v", err)
	}
	if deletedAt != 0 {
		t.Fatalf("legacy user deleted_at = %d, want 0", deletedAt)
	}
	if deletedReason.Valid {
		t.Fatalf("legacy user deleted_reason = %q, want NULL", deletedReason.String)
	}
}

func TestOpenCreatesScheduledPaymentSchema(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertColumnExists(t, db, "transactions", "scheduled_payment_at")
	assertColumnExists(t, db, "transactions", "scheduled_payment_created_by")
	assertColumnExists(t, db, "transactions", "scheduled_payment_executed_at")
	assertColumnExists(t, db, "user_notifications", "dedupe_key")
	assertColumnExists(t, db, "transaction_status_history", "source")
	assertSchemaObjectExists(t, db, "index", "idx_transactions_scheduled_due")
	assertSchemaObjectExists(t, db, "index", "idx_user_notifications_dedupe")
}

func migrationByVersion(t *testing.T, version int) migration {
	t.Helper()
	for _, m := range migrations {
		if m.version == version {
			return m
		}
	}
	t.Fatalf("migration version %d not found", version)
	return migration{}
}
