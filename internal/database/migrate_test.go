package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

func TestMigration60CreatesTransactionRefundAuditContract(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 60)
	assertSchemaObjectExists(t, db, "table", "transaction_refunds")
	assertSchemaObjectExists(t, db, "trigger", "trg_transaction_refunds_validate_insert")
	assertSchemaObjectExists(t, db, "trigger", "trg_transactions_refund_floor")
}

func TestMigration61CreatesInvoiceGeneralRefundAuditContract(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 61)
	assertSchemaObjectExists(t, db, "table", "invoice_refunds")
	for _, trigger := range []string{
		"trg_invoice_refunds_validate_insert",
		"trg_invoice_refunds_no_overpay",
		"trg_invoice_refunds_immutable_update",
		"trg_invoice_refunds_no_delete",
	} {
		assertSchemaObjectExists(t, db, "trigger", trigger)
	}
}

func TestMigration63AddsSessionRecentReauthentication(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open raw memory database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		expires_at INTEGER NOT NULL,
		revoked_at INTEGER,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()),
		is_remember INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create legacy sessions fixture: %v", err)
	}
	seedAppliedMigrationsBefore(t, db, 63)
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migration 63: %v", err)
	}

	assertMigrationApplied(t, db, 63)
	assertColumnExists(t, db, "sessions", "reauthenticated_at")

	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at)
		VALUES ('session-m63', 'user-m63', 'ws-m63', 'token-m63', ?)
	`, now+3600); err != nil {
		t.Fatalf("insert session using migration 63 default: %v", err)
	}
	var reauthenticatedAt int64
	if err := db.QueryRow(`SELECT reauthenticated_at FROM sessions WHERE id = 'session-m63'`).Scan(&reauthenticatedAt); err != nil {
		t.Fatalf("query sessions.reauthenticated_at: %v", err)
	}
	if reauthenticatedAt != 0 {
		t.Fatalf("existing-session default = %d, want 0", reauthenticatedAt)
	}
}

func TestLegacyInvoiceBackfillClosingDayStartsNextCycle(t *testing.T) {
	closingUnix, dueUnix, reference := calculateInvoiceDates(
		time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC),
		1,
		7,
	)

	if reference != "2026-10" {
		t.Fatalf("reference = %q, want 2026-10", reference)
	}
	if got := time.Unix(closingUnix, 0).UTC().Format("2006-01-02"); got != "2026-10-01" {
		t.Fatalf("closing date = %q, want 2026-10-01", got)
	}
	if got := time.Unix(dueUnix, 0).UTC().Format("2006-01-02"); got != "2026-10-07" {
		t.Fatalf("due date = %q, want 2026-10-07", got)
	}
}

func TestMigration59FreshSchemaCreatesRecurringOccurrenceBlocksOnlyThroughMigration(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 59)
	assertSchemaObjectExists(t, db, "table", "recurring_occurrence_blocks")
	for _, trigger := range []string{
		"trg_recurring_occurrence_blocks_no_transaction",
		"trg_recurring_occurrence_blocks_no_skip",
		"trg_transactions_no_recurring_terminal_insert",
		"trg_transactions_no_recurring_terminal_update",
		"trg_recurring_occurrence_skips_no_block_insert",
		"trg_recurring_occurrence_skips_no_block_update",
		"trg_recurring_occurrence_blocks_immutable_update",
		"trg_recurring_occurrence_blocks_immutable_delete",
	} {
		assertSchemaObjectExists(t, db, "trigger", trigger)
	}

	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open raw memory database: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(ddl); err != nil {
		t.Fatalf("execute base DDL: %v", err)
	}
	var baseCount int
	if err := raw.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE type='table' AND name='recurring_occurrence_blocks'`).Scan(&baseCount); err != nil {
		t.Fatalf("inspect base DDL: %v", err)
	}
	if baseCount != 0 {
		t.Fatal("recurring_occurrence_blocks leaked into base DDL")
	}
	assertMigration59SchemaCanonical(t, db)
}

func TestMigration59RejectsPreexistingTransactionSkipConflictAtomically(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration59-terminal-conflict.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("execute base DDL: %v", err)
	}
	seedAppliedMigrationsBefore(t, db, 59)
	if _, err := db.Exec(`
		INSERT INTO users (id, name, email, password_hash)
		VALUES ('user-m59', 'Migration 59', 'migration59@example.test', 'hash');
		INSERT INTO workspaces (id, name, type)
		VALUES ('ws-m59', 'Migration 59', 'personal');
		INSERT INTO accounts (id, workspace_id, name, type)
		VALUES ('account-m59', 'ws-m59', 'Migration 59', 'CHECKING');
		INSERT INTO recurring_series (id, workspace_id, user_id)
		VALUES ('series-m59', 'ws-m59', 'user-m59');
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, frequency, series_id
		) VALUES (
			'rule-m59', 'ws-m59', 'user-m59', 'account-m59', 'EXPENSE', 1000,
			'fixture', 1772323200, 'MONTHLY', 'series-m59'
		);
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date,
			description, recurring_rule_id, recurrence_sequence, series_id
		) VALUES (
			'tx-m59-conflict', 'ws-m59', 'user-m59', 'account-m59',
			'EXPENSE', 1000, 1772323200, 'fixture', 'rule-m59', 7, 'series-m59'
		);
		INSERT INTO recurring_occurrence_skips (
			workspace_id, series_id, recurrence_sequence,
			original_transaction_id, reason, deleted_at
		) VALUES (
			'ws-m59', 'series-m59', 7, 'tx-m59-conflict', 'single_delete', 1772323200
		)
	`); err != nil {
		t.Fatalf("seed contradictory terminal fixture: %v", err)
	}

	err = runMigrations(db)
	if err == nil {
		t.Fatal("migration 59 accepted a committed transaction+skip conflict")
	}
	want := `apply migration 59 (recurring_occurrence_blocks): migration 59 recurring occurrence blocks: migration 59 aborted: found 1 committed recurring terminal occurrence conflict(s) between transactions and recurring_occurrence_skips; example workspace_id="ws-m59" series_id="series-m59" recurrence_sequence=7 transaction_id="tx-m59-conflict"; resolve the integrity conflict explicitly before upgrading`
	if err.Error() != want {
		t.Fatalf("migration error = %q, want deterministic %q", err, want)
	}

	var maxVersion, version59, blockTable, triggerCount, transactionCount, skipCount int
	checks := []struct {
		query string
		dest  *int
	}{
		{`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`, &maxVersion},
		{`SELECT COUNT(1) FROM schema_migrations WHERE version=59`, &version59},
		{`SELECT COUNT(1) FROM sqlite_schema WHERE type='table' AND name='recurring_occurrence_blocks'`, &blockTable},
		{`SELECT COUNT(1) FROM sqlite_schema WHERE type='trigger' AND name LIKE '%recurring_occurrence_blocks%'`, &triggerCount},
		{`SELECT COUNT(1) FROM transactions WHERE id='tx-m59-conflict' AND recurring_rule_id='rule-m59' AND series_id='series-m59' AND recurrence_sequence=7`, &transactionCount},
		{`SELECT COUNT(1) FROM recurring_occurrence_skips WHERE workspace_id='ws-m59' AND series_id='series-m59' AND recurrence_sequence=7 AND reason='single_delete'`, &skipCount},
	}
	for _, check := range checks {
		if err := db.QueryRow(check.query).Scan(check.dest); err != nil {
			t.Fatalf("verify failed migration state: %v", err)
		}
	}
	if maxVersion != 58 || version59 != 0 || blockTable != 0 || triggerCount != 0 || transactionCount != 1 || skipCount != 1 {
		t.Fatalf("failed migration mutated state: max=%d v59=%d table=%d triggers=%d tx=%d skip=%d", maxVersion, version59, blockTable, triggerCount, transactionCount, skipCount)
	}
}

func TestMigration59CanonicalUpgradeZeroBackfillAndReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migration59-canonical-v58.db")
	db := openCanonicalMigration58Fixture(t, dbPath)
	if _, err := db.Exec(`
		INSERT INTO recurring_occurrence_skips (
			workspace_id, series_id, recurrence_sequence, reason, deleted_at
		) VALUES ('ws-m59-preserved', 'series-m59-preserved', 4, 'single_delete', 1772323200)
	`); err != nil {
		t.Fatalf("seed canonical v58 data: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("upgrade canonical v58 to v59: %v", err)
	}
	assertMigrationApplied(t, db, 59)
	assertMigration59SchemaCanonical(t, db)
	assertTableRowCount(t, db, "recurring_occurrence_blocks", 0)
	var preserved int
	if err := db.QueryRow(`
		SELECT COUNT(1) FROM recurring_occurrence_skips
		WHERE workspace_id='ws-m59-preserved' AND series_id='series-m59-preserved'
		  AND recurrence_sequence=4 AND reason='single_delete'
	`).Scan(&preserved); err != nil {
		t.Fatalf("read preserved v58 data: %v", err)
	}
	if preserved != 1 {
		t.Fatalf("preserved v58 rows = %d, want 1", preserved)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated v59 database: %v", err)
	}

	reopened, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("reopen v59 database: %v", err)
	}
	defer reopened.Close()
	reopened.SetMaxOpenConns(1)
	if err := runMigrations(reopened); err != nil {
		t.Fatalf("rerun migrations on v59 database: %v", err)
	}
	assertMigration59SchemaCanonical(t, reopened)
	assertTableRowCount(t, reopened, "recurring_occurrence_blocks", 0)
	if err := reopened.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips WHERE workspace_id='ws-m59-preserved'`).Scan(&preserved); err != nil {
		t.Fatalf("read preserved data after reopen: %v", err)
	}
	if preserved != 1 {
		t.Fatalf("preserved rows after reopen = %d, want 1", preserved)
	}
}

func TestMigration59PreflightIsWorkspaceScoped(t *testing.T) {
	db := openCanonicalMigration58Fixture(t, filepath.Join(t.TempDir(), "migration59-workspace.db"))
	defer db.Close()
	if _, err := db.Exec(`
		INSERT INTO users (id, name, email, password_hash)
		VALUES ('user-a', 'User A', 'user-a@example.test', 'hash');
		INSERT INTO workspaces (id, name, type)
		VALUES ('ws-m59-a', 'Workspace A', 'personal'), ('ws-m59-b', 'Workspace B', 'personal');
		INSERT INTO accounts (id, workspace_id, name, type)
		VALUES ('account-a', 'ws-m59-a', 'Account A', 'CHECKING');
		INSERT INTO recurring_series (id, workspace_id, user_id)
		VALUES ('series-shared', 'ws-m59-a', 'user-a');
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, type, amount, description,
			start_date, frequency, series_id
		) VALUES (
			'rule-shared', 'ws-m59-a', 'user-a', 'account-a', 'EXPENSE', 1000,
			'fixture', 1772323200, 'MONTHLY', 'series-shared'
		);
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date,
			description, recurring_rule_id, recurrence_sequence, series_id
		) VALUES (
			'tx-m59-ws-a', 'ws-m59-a', 'user-a', 'account-a', 'EXPENSE', 1000,
			1772323200, 'fixture', 'rule-shared', 7, 'series-shared'
		);
		INSERT INTO recurring_occurrence_skips (
			workspace_id, series_id, recurrence_sequence, reason, deleted_at
		) VALUES ('ws-m59-b', 'series-shared', 7, 'single_delete', 1772323200)
	`); err != nil {
		t.Fatalf("seed cross-workspace terminal identities: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("workspace-isolated v59 upgrade: %v", err)
	}
	assertMigrationApplied(t, db, 59)
	assertMigration59SchemaCanonical(t, db)
	var transactions, skips int
	_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id='tx-m59-ws-a'`).Scan(&transactions)
	_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips WHERE workspace_id='ws-m59-b'`).Scan(&skips)
	if transactions != 1 || skips != 1 {
		t.Fatalf("cross-workspace data changed: transactions=%d skips=%d", transactions, skips)
	}
}

func TestMigration59RejectsPreexistingIncompatibleTable(t *testing.T) {
	db := openCanonicalMigration58Fixture(t, filepath.Join(t.TempDir(), "migration59-incompatible.db"))
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE recurring_occurrence_blocks (
			workspace_id TEXT PRIMARY KEY,
			legacy_marker TEXT NOT NULL
		);
		INSERT INTO recurring_occurrence_blocks(workspace_id, legacy_marker)
		VALUES ('ws-legacy', 'preserve-me')
	`); err != nil {
		t.Fatalf("seed incompatible v59 table: %v", err)
	}

	err := runMigrations(db)
	if err == nil || !strings.Contains(err.Error(), "table recurring_occurrence_blocks already exists") {
		t.Fatalf("incompatible table migration error = %v", err)
	}
	var version59, legacyRows, triggerCount int
	_ = db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=59`).Scan(&version59)
	_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE workspace_id='ws-legacy' AND legacy_marker='preserve-me'`).Scan(&legacyRows)
	_ = db.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE type='trigger' AND name IN (
		'trg_recurring_occurrence_blocks_no_transaction',
		'trg_recurring_occurrence_blocks_no_skip',
		'trg_transactions_no_recurring_terminal_insert',
		'trg_transactions_no_recurring_terminal_update',
		'trg_recurring_occurrence_skips_no_block_insert',
		'trg_recurring_occurrence_skips_no_block_update',
		'trg_recurring_occurrence_blocks_immutable_update',
		'trg_recurring_occurrence_blocks_immutable_delete'
	)`).Scan(&triggerCount)
	if version59 != 0 || legacyRows != 1 || triggerCount != 0 {
		t.Fatalf("incompatible object state changed: v59=%d legacy=%d triggers=%d", version59, legacyRows, triggerCount)
	}
}

func TestMigration59ValidatorRejectsSchemaClassesAndRollsBackObjects(t *testing.T) {
	tests := []struct {
		name string
		ddl  string
	}{
		{
			name: "missing column",
			ddl: `CREATE TABLE recurring_occurrence_blocks (
				workspace_id TEXT NOT NULL,
				series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`,
		},
		{
			name: "wrong primary and unique",
			ddl: `CREATE TABLE recurring_occurrence_blocks (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurring_rule_id TEXT NOT NULL, recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				occurrence_date_civil TEXT NOT NULL, invoice_id TEXT NOT NULL,
				invoice_reference TEXT NOT NULL, invoice_status TEXT NOT NULL CHECK (invoice_status IN ('CLOSED', 'PAID')),
				reason TEXT NOT NULL CHECK (reason = 'frozen_invoice'), created_at INTEGER NOT NULL,
				PRIMARY KEY (series_id, workspace_id, recurrence_sequence),
				UNIQUE (workspace_id, recurrence_sequence, recurring_rule_id)
			)`,
		},
		{
			name: "wrong checks",
			ddl: `CREATE TABLE recurring_occurrence_blocks (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurring_rule_id TEXT NOT NULL, recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence >= 0),
				occurrence_date_civil TEXT NOT NULL, invoice_id TEXT NOT NULL,
				invoice_reference TEXT NOT NULL, invoice_status TEXT NOT NULL CHECK (invoice_status IN ('OPEN', 'CLOSED', 'PAID')),
				reason TEXT NOT NULL CHECK (reason IN ('frozen_invoice', 'other')), created_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence),
				UNIQUE (workspace_id, recurring_rule_id, recurrence_sequence)
			)`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openCanonicalMigration58Fixture(t, filepath.Join(t.TempDir(), "validator.db"))
			defer db.Close()
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(tc.ddl); err != nil {
				t.Fatalf("create incompatible table: %v", err)
			}
			if err := validateRecurringOccurrenceBlocksSchema(tx); err == nil {
				t.Fatal("validator accepted incompatible table")
			}
		})
	}

	t.Run("mutated trigger rolls back all v59 objects", func(t *testing.T) {
		db := openCanonicalMigration58Fixture(t, filepath.Join(t.TempDir(), "validator-trigger.db"))
		defer db.Close()
		if _, err := db.Exec(`
			INSERT INTO recurring_occurrence_skips (
				workspace_id, series_id, recurrence_sequence, reason, deleted_at
			) VALUES ('ws-validator', 'series-validator', 2, 'single_delete', 1772323200)
		`); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(recurringOccurrenceBlocksDDL); err != nil {
			t.Fatal(err)
		}
		for _, trigger := range recurringOccurrenceBlocksTriggerContract {
			if _, err := tx.Exec(trigger.ddl); err != nil {
				_ = tx.Rollback()
				t.Fatalf("create canonical trigger %s: %v", trigger.name, err)
			}
		}
		if _, err := tx.Exec(`
			DROP TRIGGER trg_recurring_occurrence_blocks_immutable_delete;
			CREATE TRIGGER trg_recurring_occurrence_blocks_immutable_delete
			BEFORE DELETE ON recurring_occurrence_blocks BEGIN SELECT 1; END
		`); err != nil {
			_ = tx.Rollback()
			t.Fatalf("mutate trigger: %v", err)
		}
		if err := validateRecurringOccurrenceBlocksSchema(tx); err == nil {
			_ = tx.Rollback()
			t.Fatal("validator accepted mutated trigger")
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback invalid v59 objects: %v", err)
		}
		assertSchemaObjectMissing(t, db, "table", "recurring_occurrence_blocks")
		var version59, triggerCount, preserved int
		_ = db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version=59`).Scan(&version59)
		_ = db.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE type='trigger' AND name LIKE 'trg_recurring_occurrence_blocks%'`).Scan(&triggerCount)
		_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips WHERE workspace_id='ws-validator'`).Scan(&preserved)
		if version59 != 0 || triggerCount != 0 || preserved != 1 {
			t.Fatalf("rollback state: v59=%d triggers=%d preserved=%d", version59, triggerCount, preserved)
		}
	})
}

func openCanonicalMigration58Fixture(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open canonical v58 fixture: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("execute canonical base DDL: %v", err)
	}
	seedAppliedMigrationsBefore(t, db, 59)
	return db
}

func assertMigration59SchemaCanonical(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin migration 59 schema validation: %v", err)
	}
	defer tx.Rollback()
	if err := validateRecurringOccurrenceBlocksSchema(tx); err != nil {
		t.Fatalf("validate migration 59 schema: %v", err)
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
		27: "cost_limits timestamps on populated SQLite tables — transactional preservation covered by F10",
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

	// The destructive-migration runbook is private documentation, intentionally
	// excluded from public checkouts by export-ignore. The hardcoded allowlist
	// above provides the authoritative enforcement; file existence is optional.
	{
		docPath := filepath.Join("..", "..", "internal", "database", "migrations-destrutivas.md")
		if _, err := os.Stat(docPath); err != nil {
			if os.IsNotExist(err) {
				t.Logf("destructive migration runbook nao encontrado; allowlist acima cobre a revisao explicita")
				return
			}
			t.Fatalf("cannot stat destructive migration runbook: %v", err)
		}
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 28)
	assertColumnExists(t, db, "workspace_members", "custom_permissions")
	assertMigrationApplied(t, db, 28)
	if err := db.Close(); err != nil {
		t.Fatalf("close migrated legacy workspace members database: %v", err)
	}

	db2 := openMigratedRawDBThroughVersionForTest(t, dbPath, 28)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 28)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 29)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 29)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 30)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 31)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 32)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 41)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 35)
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

	db2 := openMigratedRawDBThroughVersionForTest(t, dbPath, 35)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 36)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 39)
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 51)
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

func TestMigration52AddsCivilDateColumnsOnFreshDatabase(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 52)

	civilColumns := map[string][]string{
		"transactions":                    {"date_civil", "due_date_civil", "paid_date_civil", "scheduled_payment_date_civil"},
		"recurring_rules":                 {"start_date_civil", "starts_at_civil", "end_date_civil", "generated_until_civil"},
		"recurring_materialization_state": {"materialized_until_civil"},
		"invoice_payments":                {"paid_date_civil"},
		"invoices":                        {"closing_date_civil", "due_date_civil"},
	}
	for table, columns := range civilColumns {
		for _, column := range columns {
			assertColumnExists(t, db, table, column)
			assertColumnNullable(t, db, table, column)
		}
	}

	// Legacy INTEGER columns must remain untouched next to the new civil columns.
	assertColumnExists(t, db, "transactions", "date")
	assertColumnExists(t, db, "transactions", "due_date")
	assertColumnExists(t, db, "transactions", "paid_at")
	assertColumnExists(t, db, "transactions", "scheduled_payment_at")
	assertColumnExists(t, db, "recurring_rules", "start_date")
	assertColumnExists(t, db, "recurring_rules", "starts_at")
	assertColumnExists(t, db, "recurring_rules", "end_date")
	assertColumnExists(t, db, "recurring_rules", "generated_until")
	assertColumnExists(t, db, "recurring_materialization_state", "materialized_until")
	assertColumnExists(t, db, "invoice_payments", "paid_at")
	assertColumnExists(t, db, "invoices", "closing_date")
	assertColumnExists(t, db, "invoices", "due_date")

	for _, index := range []string{
		"idx_transactions_workspace_date_civil",
		"idx_transactions_workspace_due_date_civil",
		"idx_transactions_workspace_paid_date_civil",
		"idx_transactions_workspace_scheduled_payment_date_civil",
		"idx_recurring_rules_workspace_start_date_civil",
		"idx_recurring_rules_workspace_starts_at_civil",
		"idx_recurring_rules_workspace_end_date_civil",
		"idx_recurring_materialization_state_workspace_materialized_until_civil",
		"idx_invoice_payments_workspace_paid_date_civil",
		"idx_invoices_closing_date_civil",
		"idx_invoices_due_date_civil",
	} {
		assertSchemaObjectExists(t, db, "index", index)
	}
}

func TestMigration52AddsCivilDateColumnsToExistingDatabaseWithoutTouchingData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "civil-date-columns.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("execute ddl fixture: %v", err)
	}
	seedAppliedMigrationsBefore(t, db, 52)

	const (
		txDate            = int64(1714521600)
		invoiceClosing    = int64(1717200000)
		invoiceDue        = int64(1719792000)
		paymentPaidAt     = int64(1719878400)
		ruleStart         = int64(1711929600)
		materializedUntil = int64(1722470400)
	)
	seed := []string{
		`INSERT INTO users (id, name, email, password_hash) VALUES ('u-1', 'User 1', 'u1@example.test', 'hash')`,
		`INSERT INTO workspaces (id, name, type) VALUES ('ws-1', 'Workspace 1', 'personal')`,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-1', 'u-1', 'ADMIN')`,
		`INSERT INTO accounts (id, workspace_id, name, type) VALUES ('acc-1', 'ws-1', 'Conta', 'CHECKING')`,
		`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, due_date, paid_at, scheduled_payment_at)
			VALUES ('tx-1', 'ws-1', 'u-1', 'acc-1', 'EXPENSE', 1000, 1714521600, 1714521600, 1714521600, 1714521600)`,
		`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status)
			VALUES ('inv-1', 'acc-1', '2024-06', 1717200000, 1719792000, 'OPEN')`,
		`CREATE TABLE IF NOT EXISTS invoice_payments (
			id             TEXT PRIMARY KEY,
			workspace_id   TEXT NOT NULL,
			invoice_id     TEXT NOT NULL,
			account_id     TEXT NOT NULL,
			transaction_id TEXT,
			amount_cents   INTEGER NOT NULL CHECK(amount_cents > 0),
			paid_at        INTEGER NOT NULL,
			note           TEXT,
			source         TEXT NOT NULL DEFAULT 'manual',
			reversed_at    INTEGER,
			created_by     TEXT,
			created_at     INTEGER NOT NULL,
			FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
			FOREIGN KEY (invoice_id) REFERENCES invoices(id) ON DELETE CASCADE,
			FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
		)`,
		`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, created_at)
			VALUES ('ip-1', 'ws-1', 'inv-1', 'acc-1', 1000, 1719878400, 1719878400)`,
		`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, start_date, frequency, generated_until, starts_at, end_date)
			VALUES ('rr-1', 'ws-1', 'u-1', 'acc-1', 'EXPENSE', 1000, 1711929600, 'MONTHLY', 1722470400, 1711929600, NULL)`,
		`INSERT INTO recurring_materialization_state (workspace_id, materialized_until) VALUES ('ws-1', 1722470400)`,
	}
	for _, stmt := range seed {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed civil date fixture %q: %v", stmt, err)
		}
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	assertMigrationApplied(t, db, 52)

	// New civil columns exist and stay NULL: the migration performs no backfill.
	assertCivilColumnNull(t, db, "transactions", "date_civil", "id = 'tx-1'")
	assertCivilColumnNull(t, db, "transactions", "due_date_civil", "id = 'tx-1'")
	assertCivilColumnNull(t, db, "transactions", "paid_date_civil", "id = 'tx-1'")
	assertCivilColumnNull(t, db, "transactions", "scheduled_payment_date_civil", "id = 'tx-1'")
	assertCivilColumnNull(t, db, "recurring_rules", "start_date_civil", "id = 'rr-1'")
	assertCivilColumnNull(t, db, "recurring_rules", "starts_at_civil", "id = 'rr-1'")
	assertCivilColumnNull(t, db, "recurring_rules", "end_date_civil", "id = 'rr-1'")
	assertCivilColumnNull(t, db, "recurring_rules", "generated_until_civil", "id = 'rr-1'")
	assertCivilColumnNull(t, db, "recurring_materialization_state", "materialized_until_civil", "workspace_id = 'ws-1'")
	assertCivilColumnNull(t, db, "invoice_payments", "paid_date_civil", "id = 'ip-1'")
	assertCivilColumnNull(t, db, "invoices", "closing_date_civil", "id = 'inv-1'")
	assertCivilColumnNull(t, db, "invoices", "due_date_civil", "id = 'inv-1'")

	// Legacy INTEGER data is preserved byte-for-byte.
	assertIntColumnValue(t, db, "transactions", "date", "id = 'tx-1'", txDate)
	assertIntColumnValue(t, db, "transactions", "due_date", "id = 'tx-1'", txDate)
	assertIntColumnValue(t, db, "transactions", "paid_at", "id = 'tx-1'", txDate)
	assertIntColumnValue(t, db, "transactions", "scheduled_payment_at", "id = 'tx-1'", txDate)
	assertIntColumnValue(t, db, "invoices", "closing_date", "id = 'inv-1'", invoiceClosing)
	assertIntColumnValue(t, db, "invoices", "due_date", "id = 'inv-1'", invoiceDue)
	assertIntColumnValue(t, db, "invoice_payments", "paid_at", "id = 'ip-1'", paymentPaidAt)
	assertIntColumnValue(t, db, "recurring_rules", "start_date", "id = 'rr-1'", ruleStart)
	assertIntColumnValue(t, db, "recurring_materialization_state", "materialized_until", "workspace_id = 'ws-1'", materializedUntil)

	for _, index := range []string{
		"idx_transactions_workspace_date_civil",
		"idx_transactions_workspace_due_date_civil",
		"idx_transactions_workspace_paid_date_civil",
		"idx_transactions_workspace_scheduled_payment_date_civil",
		"idx_recurring_rules_workspace_start_date_civil",
		"idx_recurring_rules_workspace_starts_at_civil",
		"idx_recurring_rules_workspace_end_date_civil",
		"idx_recurring_materialization_state_workspace_materialized_until_civil",
		"idx_invoice_payments_workspace_paid_date_civil",
		"idx_invoices_closing_date_civil",
		"idx_invoices_due_date_civil",
	} {
		assertSchemaObjectExists(t, db, "index", index)
	}

	// Re-running the migration must be idempotent.
	if err := runMigrations(db); err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}
}

func TestMigration58AddsInvoicePaymentReversalIdentityOnFreshDatabase(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open memory database: %v", err)
	}
	defer db.Close()

	assertMigrationApplied(t, db, 58)
	for _, column := range []string{
		"reversed_by",
		"reversal_reason",
		"reversal_transaction_id",
		"reversed_date_civil",
	} {
		assertColumnExists(t, db, "invoice_payments", column)
		assertColumnNullable(t, db, "invoice_payments", column)
	}
	assertSchemaObjectExists(t, db, "index", "uq_invoice_payments_workspace_transaction")
	assertSchemaObjectExists(t, db, "index", "uq_invoice_payments_workspace_reversal_transaction")
	assertIndexColumnsAndPredicate(t, db, "uq_invoice_payments_workspace_transaction", "(workspace_id, transaction_id)", "WHERE transaction_id IS NOT NULL")
	assertIndexColumnsAndPredicate(t, db, "uq_invoice_payments_workspace_reversal_transaction", "(workspace_id, reversal_transaction_id)", "WHERE reversal_transaction_id IS NOT NULL")

	foreignKeys := invoicePaymentForeignKeys(t, db)
	reversalFK, ok := foreignKeys["reversal_transaction_id"]
	if !ok {
		t.Fatal("invoice_payments.reversal_transaction_id foreign key not found")
	}
	if reversalFK.table != "transactions" || reversalFK.to != "id" || reversalFK.onDelete != "RESTRICT" {
		t.Fatalf("reversal_transaction_id foreign key = %+v, want transactions(id) ON DELETE RESTRICT", reversalFK)
	}
	if _, ok := foreignKeys["reversed_by"]; ok {
		t.Fatal("invoice_payments.reversed_by unexpectedly has a foreign key")
	}
}

func TestMigration58PreservesExistingInvoicePaymentAndIsIdempotent(t *testing.T) {
	db := openPreMigration58InvoicePaymentsDB(t)
	defer db.Close()
	seedMigration58Workspace(t, db, "ws-a", "account-a", "invoice-a", "user-a")
	if _, err := db.Exec(`
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description)
		VALUES ('tx-original', 'ws-a', 'user-a', 'account-a', 'EXPENSE', 4321, 1719878400, 'original')
	`); err != nil {
		t.Fatalf("seed original transaction: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, transaction_id,
			amount_cents, paid_at, paid_date_civil, note, source,
			reversed_at, created_by, created_at
		) VALUES (
			'payment-a', 'ws-a', 'invoice-a', 'account-a', 'tx-original',
			4321, 1719878400, '2024-07-02', 'legacy note', 'manual',
			1719964800, 'user-a', 1719878401
		)
	`); err != nil {
		t.Fatalf("seed existing invoice payment: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("run migration 58: %v", err)
	}
	assertMigrationApplied(t, db, 58)

	var (
		workspaceID, invoiceID, accountID, transactionID  string
		amountCents, paidAt, reversedAt, createdAt        int64
		paidDateCivil, note, source, createdBy            string
		reversedBy, reversalReason, reversalTransactionID sql.NullString
		reversedDateCivil                                 sql.NullString
	)
	if err := db.QueryRow(`
		SELECT workspace_id, invoice_id, account_id, transaction_id,
		       amount_cents, paid_at, paid_date_civil, note, source,
		       reversed_at, created_by, created_at,
		       reversed_by, reversal_reason, reversal_transaction_id, reversed_date_civil
		FROM invoice_payments
		WHERE id = 'payment-a'
	`).Scan(
		&workspaceID, &invoiceID, &accountID, &transactionID,
		&amountCents, &paidAt, &paidDateCivil, &note, &source,
		&reversedAt, &createdBy, &createdAt,
		&reversedBy, &reversalReason, &reversalTransactionID, &reversedDateCivil,
	); err != nil {
		t.Fatalf("query migrated invoice payment: %v", err)
	}
	if workspaceID != "ws-a" || invoiceID != "invoice-a" || accountID != "account-a" || transactionID != "tx-original" ||
		amountCents != 4321 || paidAt != 1719878400 || paidDateCivil != "2024-07-02" || note != "legacy note" ||
		source != "manual" || reversedAt != 1719964800 || createdBy != "user-a" || createdAt != 1719878401 {
		t.Fatalf("existing invoice payment changed during migration")
	}
	if reversedBy.Valid || reversalReason.Valid || reversalTransactionID.Valid || reversedDateCivil.Valid {
		t.Fatalf("new reversal fields = reversed_by:%v reason:%v transaction:%v civil:%v, want all NULL", reversedBy, reversalReason, reversalTransactionID, reversedDateCivil)
	}
	assertIntColumnValue(t, db, "invoices", "paid_amount", "id = 'invoice-a'", 4321)

	if err := runMigrations(db); err != nil {
		t.Fatalf("rerun migrations: %v", err)
	}
	assertMigrationApplied(t, db, 58)
}

func TestMigration58EnforcesWorkspacePaymentIdentities(t *testing.T) {
	db := openPreMigration58InvoicePaymentsDB(t)
	defer db.Close()
	seedMigration58Workspace(t, db, "ws-a", "account-a", "invoice-a", "user-a")
	seedMigration58Workspace(t, db, "ws-b", "account-b", "invoice-b", "user-b")

	for _, stmt := range []string{
		`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date)
		 VALUES ('tx-reversal-a', 'ws-a', 'user-a', 'account-a', 'EXPENSE', 100, 1719878400)`,
		`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, created_at)
		 VALUES ('payment-a-1', 'ws-a', 'invoice-a', 'account-a', 'shared-original', 100, 1719878400, 1719878400)`,
		`INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, created_at)
		 VALUES ('payment-b-1', 'ws-b', 'invoice-b', 'account-b', 'shared-original', 100, 1719878400, 1719878400)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed identity fixture: %v", err)
		}
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("run migration 58: %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, created_at)
		VALUES ('payment-a-duplicate', 'ws-a', 'invoice-a', 'account-a', 'shared-original', 100, 1719878400, 1719878400)
	`); err == nil {
		t.Fatal("duplicate original payment identity in one workspace was accepted")
	}
	if _, err := db.Exec(`UPDATE invoice_payments SET reversal_transaction_id = 'tx-reversal-a' WHERE id = 'payment-a-1'`); err != nil {
		t.Fatalf("set first reversal identity: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM transactions WHERE id = 'tx-reversal-a'`); err == nil {
		t.Fatal("ON DELETE RESTRICT accepted deletion of a referenced reversal transaction")
	}
	if _, err := db.Exec(`
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, amount_cents, paid_at,
			reversal_transaction_id, created_at
		) VALUES (
			'payment-a-reversal-duplicate', 'ws-a', 'invoice-a', 'account-a', 100, 1719878400,
			'tx-reversal-a', 1719878400
		)
	`); err == nil {
		t.Fatal("duplicate reversal payment identity in one workspace was accepted")
	}
}

func TestMigration58DuplicateHistoricalIdentityRollsBackAtomically(t *testing.T) {
	db := openPreMigration58InvoicePaymentsDB(t)
	defer db.Close()
	seedMigration58Workspace(t, db, "ws-a", "account-a", "invoice-a", "user-a")
	for _, paymentID := range []string{"payment-a-1", "payment-a-2"} {
		if _, err := db.Exec(`
			INSERT INTO invoice_payments (
				id, workspace_id, invoice_id, account_id, transaction_id,
				amount_cents, paid_at, created_at
			) VALUES (?, 'ws-a', 'invoice-a', 'account-a', 'duplicate-original', 100, 1719878400, 1719878400)
		`, paymentID); err != nil {
			t.Fatalf("seed duplicate historical identity: %v", err)
		}
	}

	err := runMigrations(db)
	if err == nil {
		t.Fatal("migration 58 accepted duplicate historical original payment identity")
	}
	if !strings.Contains(err.Error(), `workspace_id="ws-a" transaction_id="duplicate-original" count=2`) {
		t.Fatalf("migration error = %q, want deterministic duplicate identity evidence", err)
	}

	for _, column := range []string{"reversed_by", "reversal_reason", "reversal_transaction_id", "reversed_date_civil"} {
		assertColumnMissing(t, db, "invoice_payments", column)
	}
	assertSchemaObjectMissing(t, db, "index", "uq_invoice_payments_workspace_transaction")
	assertSchemaObjectMissing(t, db, "index", "uq_invoice_payments_workspace_reversal_transaction")
	var applied int
	if err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = 58`).Scan(&applied); err != nil {
		t.Fatalf("query migration 58 registration: %v", err)
	}
	if applied != 0 {
		t.Fatalf("migration 58 registration count = %d, want 0 after rollback", applied)
	}
	assertTableRowCount(t, db, "invoice_payments", 2)
}

func openPreMigration58InvoicePaymentsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open pre-migration 58 database: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("execute ddl for pre-migration 58 fixture: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE invoice_payments (
		id             TEXT PRIMARY KEY,
		workspace_id   TEXT NOT NULL,
		invoice_id     TEXT NOT NULL,
		account_id     TEXT NOT NULL,
		transaction_id TEXT,
		amount_cents   INTEGER NOT NULL CHECK(amount_cents > 0),
		paid_at        INTEGER NOT NULL,
		paid_date_civil TEXT,
		note           TEXT,
		source         TEXT NOT NULL DEFAULT 'manual',
		reversed_at    INTEGER,
		created_by     TEXT,
		created_at     INTEGER NOT NULL,
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
		FOREIGN KEY (invoice_id) REFERENCES invoices(id) ON DELETE CASCADE,
		FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
	)`); err != nil {
		db.Close()
		t.Fatalf("create pre-migration 58 invoice_payments: %v", err)
	}
	seedAppliedMigrationsBefore(t, db, 58)
	return db
}

func seedMigration58Workspace(t *testing.T, db *sql.DB, workspaceID, accountID, invoiceID, userID string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, name, email, password_hash) VALUES (?, ?, ?, 'hash')`, []any{userID, userID, userID + "@example.test"}},
		{`INSERT INTO workspaces (id, name, type) VALUES (?, ?, 'personal')`, []any{workspaceID, workspaceID}},
		{`INSERT INTO accounts (id, workspace_id, name, type) VALUES (?, ?, ?, 'CHECKING')`, []any{accountID, workspaceID, accountID}},
		{`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount) VALUES (?, ?, '2024-07', 1719878400, 1720483200, 'PAID', 4321)`, []any{invoiceID, accountID}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed migration 58 workspace fixture: %v", err)
		}
	}
}

type invoicePaymentForeignKey struct {
	table    string
	to       string
	onDelete string
}

func invoicePaymentForeignKeys(t *testing.T, db *sql.DB) map[string]invoicePaymentForeignKey {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list(invoice_payments)`)
	if err != nil {
		t.Fatalf("query invoice_payments foreign keys: %v", err)
	}
	defer rows.Close()

	foreignKeys := make(map[string]invoicePaymentForeignKey)
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan invoice_payments foreign key: %v", err)
		}
		foreignKeys[from] = invoicePaymentForeignKey{table: table, to: to, onDelete: onDelete}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate invoice_payments foreign keys: %v", err)
	}
	return foreignKeys
}

func assertColumnMissing(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		if name == column {
			t.Fatalf("column %s.%s exists, want rollback to remove it", table, column)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info %s rows: %v", table, err)
	}
}

func assertSchemaObjectMissing(t *testing.T, db *sql.DB, objectType, name string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = ? AND name = ?`, objectType, name).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for absent %s %s: %v", objectType, name, err)
	}
	if count != 0 {
		t.Fatalf("schema object %s %s count = %d, want 0", objectType, name, count)
	}
}

func assertIndexColumnsAndPredicate(t *testing.T, db *sql.DB, name, columns, predicate string) {
	t.Helper()
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&definition); err != nil {
		t.Fatalf("query index %s definition: %v", name, err)
	}
	normalized := strings.Join(strings.Fields(definition), " ")
	if !strings.Contains(normalized, columns) || !strings.Contains(normalized, predicate) {
		t.Fatalf("index %s definition = %q, want columns %q and predicate %q", name, normalized, columns, predicate)
	}
}

func assertCivilColumnNull(t *testing.T, db *sql.DB, table, column, where string) {
	t.Helper()

	var value sql.NullString
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s`, column, table, where)
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("query %s.%s: %v", table, column, err)
	}
	if value.Valid {
		t.Fatalf("%s.%s = %q, want NULL after additive migration", table, column, value.String)
	}
}

func assertIntColumnValue(t *testing.T, db *sql.DB, table, column, where string, want int64) {
	t.Helper()

	var value sql.NullInt64
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s`, column, table, where)
	if err := db.QueryRow(query).Scan(&value); err != nil {
		t.Fatalf("query %s.%s: %v", table, column, err)
	}
	if !value.Valid || value.Int64 != want {
		t.Fatalf("%s.%s = valid=%v value=%d, want %d", table, column, value.Valid, value.Int64, want)
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

func openMigratedRawDBThroughVersionForTest(t *testing.T, dbPath string, maxVersion int) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw migrated database through version %d: %v", maxVersion, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("ping raw migrated database through version %d: %v", maxVersion, err)
	}
	if err := runMigrationsThroughVersionForTest(db, maxVersion); err != nil {
		db.Close()
		t.Fatalf("run migrations through version %d on raw database: %v", maxVersion, err)
	}
	return db
}

// runMigrationsThroughVersionForTest keeps isolated historical migration tests
// on their declared target. Temporary applied markers make the production
// runner skip later migrations; the real migration functions, transactions,
// registration, foreign-key handling, and final integrity check remain in use.
func runMigrationsThroughVersionForTest(db *sql.DB, maxVersion int) error {
	var temporaryVersions []int
	for _, m := range migrations {
		if m.version <= maxVersion {
			continue
		}
		result, err := db.Exec(`
			INSERT OR IGNORE INTO schema_migrations(version, name, applied_at)
			VALUES (?, ?, unixepoch())
		`, m.version, "test-only excluded: "+m.name)
		if err != nil {
			return fmt.Errorf("mark migration %d excluded for isolated test: %w", m.version, err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read excluded marker %d result: %w", m.version, err)
		}
		if inserted == 1 {
			temporaryVersions = append(temporaryVersions, m.version)
		}
	}

	runErr := runMigrations(db)
	for _, version := range temporaryVersions {
		if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = ?`, version); err != nil {
			if runErr != nil {
				return fmt.Errorf("migration run failed: %v; remove test-only marker %d: %w", runErr, version, err)
			}
			return fmt.Errorf("remove test-only marker %d: %w", version, err)
		}
	}
	return runErr
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

	db := openMigratedRawDBThroughVersionForTest(t, dbPath, 42)
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

func civilTimestamp(s string) int64 {
	parsed, _ := time.Parse("2006-01-02", s)
	return parsed.Unix()
}

func snapshotRecurringRows(t *testing.T, db *sql.DB, workspaceID string) string {
	t.Helper()
	var b strings.Builder
	rows, err := db.Query(`SELECT * FROM recurring_series WHERE workspace_id=? ORDER BY id`, workspaceID)
	if err != nil {
		t.Fatalf("snapshot series: %v", err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	fmt.Fprintf(&b, "series|%s", strings.Join(cols, ","))
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		b.WriteByte('\n')
		for i, v := range vals {
			if i > 0 {
				b.WriteByte('|')
			}
			fmt.Fprintf(&b, "%v", v)
		}
	}
	rows.Close()

	rows, err = db.Query(`SELECT * FROM recurring_rules WHERE workspace_id=? ORDER BY id`, workspaceID)
	if err != nil {
		t.Fatalf("snapshot rules: %v", err)
	}
	defer rows.Close()
	cols, _ = rows.Columns()
	fmt.Fprintf(&b, "\nrules|%s", strings.Join(cols, ","))
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		b.WriteByte('\n')
		for i, v := range vals {
			if i > 0 {
				b.WriteByte('|')
			}
			fmt.Fprintf(&b, "%v", v)
		}
	}
	return b.String()
}

func TestMigration54RecurringOccurrenceSkips(t *testing.T) {
	assertSchema := func(t *testing.T, db *sql.DB) {
		t.Helper()
		rows, err := db.Query(`PRAGMA table_info(recurring_occurrence_skips)`)
		if err != nil {
			t.Fatalf("pragma table_info: %v", err)
		}
		defer rows.Close()
		type column struct {
			name     string
			typeName string
			notNull  int
			pk       int
		}
		var got []column
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, pk int
			var defaultValue interface{}
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
				t.Fatalf("scan table_info: %v", err)
			}
			got = append(got, column{name: name, typeName: strings.ToUpper(columnType), notNull: notNull, pk: pk})
		}
		want := []column{
			{name: "workspace_id", typeName: "TEXT", notNull: 1, pk: 1},
			{name: "series_id", typeName: "TEXT", notNull: 1, pk: 2},
			{name: "recurrence_sequence", typeName: "INTEGER", notNull: 1, pk: 3},
			{name: "original_transaction_id", typeName: "TEXT", notNull: 0, pk: 0},
			{name: "reason", typeName: "TEXT", notNull: 1, pk: 0},
			{name: "deleted_by", typeName: "TEXT", notNull: 0, pk: 0},
			{name: "deleted_at", typeName: "INTEGER", notNull: 1, pk: 0},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("skip schema = %#v, want %#v", got, want)
		}
	}

	t.Run("fresh_schema_and_workspace_primary_key", func(t *testing.T) {
		db, err := Open(":memory:")
		if err != nil {
			t.Fatalf("open fresh: %v", err)
		}
		defer db.Close()
		assertMigrationApplied(t, db, 54)
		assertSchema(t, db)

		insert := `INSERT INTO recurring_occurrence_skips
			(workspace_id, series_id, recurrence_sequence, original_transaction_id, reason, deleted_at)
			VALUES (?, 'same-series', 3, ?, 'single_delete', 123)`
		if _, err := db.Exec(insert, "ws-a", "tx-a"); err != nil {
			t.Fatalf("insert workspace A: %v", err)
		}
		if _, err := db.Exec(insert, "ws-b", "tx-b"); err != nil {
			t.Fatalf("same series/sequence in workspace B must be isolated: %v", err)
		}
		if _, err := db.Exec(insert, "ws-a", "tx-a-retry"); err == nil {
			t.Fatal("duplicate workspace/series/sequence should fail")
		}
		if _, err := db.Exec(`INSERT INTO recurring_occurrence_skips
			(workspace_id, series_id, recurrence_sequence, reason, deleted_at)
			VALUES ('ws-a', 'same-series', 4, 'invalid', 123)`); err == nil {
			t.Fatal("uncontrolled skip reason should fail")
		}
	})

	t.Run("upgrade_reopen_idempotent_without_backfill", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(ddl); err != nil {
			t.Fatalf("fresh ddl fixture: %v", err)
		}
		if _, err := raw.Exec(`DROP TABLE recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop post-v53 table from fixture: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint v53 fixture: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw: %v", err)
		}

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open v53 for migration: %v", err)
		}
		opened.SetMaxOpenConns(1)
		if err := runMigrations(opened); err != nil {
			t.Fatalf("upgrade v53 to v54: %v", err)
		}
		assertMigrationApplied(t, opened, 54)
		assertSchema(t, opened)
		var count int
		if err := opened.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips`).Scan(&count); err != nil {
			t.Fatalf("count skips: %v", err)
		}
		if count != 0 {
			t.Fatalf("migration backfilled %d skip(s), want zero", count)
		}
		if err := opened.Close(); err != nil {
			t.Fatalf("close upgraded: %v", err)
		}

		reopened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen upgraded: %v", err)
		}
		defer reopened.Close()
		reopened.SetMaxOpenConns(1)
		if err := runMigrations(reopened); err != nil {
			t.Fatalf("rerun migrations after reopen: %v", err)
		}
		assertMigrationApplied(t, reopened, 54)
		assertSchema(t, reopened)
		if err := runMigrations(reopened); err != nil {
			t.Fatalf("repeat migrations: %v", err)
		}
	})

	t.Run("incompatible_existing_table_rolls_back_version", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-incompatible.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(ddl); err != nil {
			t.Fatalf("fresh ddl fixture: %v", err)
		}
		if _, err := raw.Exec(`DROP TABLE recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop table: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE recurring_occurrence_skips (workspace_id TEXT PRIMARY KEY)`); err != nil {
			t.Fatalf("create incompatible table: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint incompatible fixture: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw: %v", err)
		}

		migrationDB, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open incompatible fixture: %v", err)
		}
		migrationDB.SetMaxOpenConns(1)
		if err := runMigrations(migrationDB); err == nil {
			migrationDB.Close()
			t.Fatal("migration 54 should reject incompatible table")
		}
		migrationDB.Close()
		inspect, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("inspect failed migration: %v", err)
		}
		defer inspect.Close()
		var version int
		if err := inspect.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
			t.Fatalf("read migration version: %v", err)
		}
		if version != 53 {
			t.Fatalf("version after failed migration = %d, want 53", version)
		}
		var columnCount int
		if err := inspect.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('recurring_occurrence_skips')`).Scan(&columnCount); err != nil {
			t.Fatalf("inspect incompatible table: %v", err)
		}
		if columnCount != 1 {
			t.Fatalf("failed migration changed incompatible table: columns=%d", columnCount)
		}
	})

	t.Run("reject_extra_column", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
				reason TEXT NOT NULL, deleted_by TEXT, deleted_at INTEGER NOT NULL,
				extra_col TEXT, PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_missing_column", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
				reason TEXT NOT NULL, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_divergent_nullability", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
				reason TEXT NOT NULL, deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_divergent_pk", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
				reason TEXT NOT NULL, deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id)
			)`)
	})

	t.Run("reject_missing_check_sequence", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_altered_check_sequence", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence >= 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_missing_check_reason", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL, deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_extra_check_deleted_at_P1", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT,
				deleted_at INTEGER NOT NULL CHECK (deleted_at > 0),
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_default_value", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0) DEFAULT 1,
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_foreign_key", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence),
				FOREIGN KEY (workspace_id) REFERENCES workspaces(id)
			)`)
	})

	t.Run("reject_extra_unique_index", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			);
			CREATE UNIQUE INDEX idx_skips_tx ON recurring_occurrence_skips(original_transaction_id)`)
	})

	t.Run("reject_trigger", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-reject-trigger.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(ddl); err != nil {
			t.Fatalf("fresh ddl: %v", err)
		}
		if _, err := raw.Exec(`DROP TABLE recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop table: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE recurring_occurrence_skips (
			workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
			recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
			original_transaction_id TEXT,
			reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
			deleted_by TEXT, deleted_at INTEGER NOT NULL,
			PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
		)`); err != nil {
			t.Fatalf("create table: %v", err)
		}
		if _, err := raw.Exec(`CREATE TRIGGER trg_skips_audit AFTER INSERT ON recurring_occurrence_skips
		BEGIN
			INSERT INTO schema_migrations(version, name) VALUES (-1, 'test');
		END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		migrationErr := runMigrations(opened)
		opened.Close()
		if migrationErr == nil {
			t.Fatal("migration 54 should reject table with trigger")
		}
	})

	t.Run("reject_strict", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			) STRICT`)
	})

	t.Run("reject_without_rowid", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			) WITHOUT ROWID`)
	})

	t.Run("version_not_registered_after_rejection", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-version-rollback.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(ddl); err != nil {
			t.Fatalf("fresh ddl: %v", err)
		}
		if _, err := raw.Exec(`DROP TABLE recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop table: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE recurring_occurrence_skips (
			workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
			recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
			reason TEXT NOT NULL, deleted_by TEXT, deleted_at INTEGER NOT NULL,
			extra TEXT, PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
		)`); err != nil {
			t.Fatalf("create incompatible: %v", err)
		}
		if _, err := raw.Exec(`INSERT INTO recurring_occurrence_skips
			(workspace_id, series_id, recurrence_sequence, reason, deleted_at)
			VALUES ('ws', 's', 1, 'single_delete', 100)`); err != nil {
			t.Fatalf("insert existing row: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open for migration: %v", err)
		}
		opened.SetMaxOpenConns(1)
		if err := runMigrations(opened); err == nil {
			opened.Close()
			t.Fatal("migration 54 should reject incompatible pre-existing table")
		}
		opened.Close()

		inspect, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		defer inspect.Close()
		var version int
		if err := inspect.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
			t.Fatalf("read version: %v", err)
		}
		if version != 53 {
			t.Fatalf("version after rollback = %d, want 53", version)
		}
		var rowCount int
		if err := inspect.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips`).Scan(&rowCount); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if rowCount != 1 {
			t.Fatalf("rows after rollback = %d, want 1", rowCount)
		}
	})

	t.Run("reject_named_check_constraint_sequence", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CONSTRAINT seq_pos CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_named_check_constraint_reason", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CONSTRAINT reason_check CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_on_conflict_replace", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence) ON CONFLICT REPLACE
			)`)
	})

	t.Run("reject_collate", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL COLLATE NOCASE, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_literal_SINGLE_DELETE_uppercase", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('SINGLE_DELETE')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_sequence_ge_1", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence >= 1),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_zero_lt_sequence", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (0 < recurrence_sequence),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_pk_order_different", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (series_id, workspace_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_extra_reason_value", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete', 'future_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("canonical_preexisting_table_accepted_and_preserves_rows", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-canonical.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL DEFAULT (unixepoch()))`); err != nil {
			t.Fatalf("create migrations: %v", err)
		}
		for _, m := range migrations {
			if m.version >= 54 {
				break
			}
			if _, err := raw.Exec(`INSERT OR IGNORE INTO schema_migrations(version, name) VALUES(?, ?)`, m.version, m.name); err != nil {
				t.Fatalf("seed migration %d: %v", m.version, err)
			}
		}
		if _, err := raw.Exec(authorizedSkipsDDL); err != nil {
			t.Fatalf("create canonical table: %v", err)
		}
		if _, err := raw.Exec(`INSERT INTO recurring_occurrence_skips (workspace_id, series_id, recurrence_sequence, reason, deleted_at) VALUES ('ws-pre', 's-pre', 1, 'single_delete', 100)`); err != nil {
			t.Fatalf("insert pre-existing row: %v", err)
		}
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		if err := runMigrationsThroughVersionForTest(opened, 54); err != nil {
			t.Fatalf("migration failed on canonical table: %v", err)
		}
		opened.Close()

		inspect, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		defer inspect.Close()

		var version int
		if err := inspect.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
			t.Fatalf("read version: %v", err)
		}
		if version != 54 {
			t.Fatalf("version = %d, want 54", version)
		}
		var rowCount int
		if err := inspect.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips`).Scan(&rowCount); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if rowCount != 1 {
			t.Fatalf("pre-existing rows lost: count=%d, want 1", rowCount)
		}

		var actualDDL string
		if err := inspect.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='recurring_occurrence_skips'`).Scan(&actualDDL); err != nil {
			t.Fatalf("read DDL: %v", err)
		}
		if normalizeSkipsDDL(actualDDL) != normalizeSkipsDDL(authorizedSkipsDDL) {
			t.Fatalf("DDL changed after migration:\ngot:  %q\nwant: %q", normalizeSkipsDDL(actualDDL), normalizeSkipsDDL(authorizedSkipsDDL))
		}

		if err := inspect.Close(); err != nil {
			t.Fatalf("close inspect: %v", err)
		}

		reopened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen again: %v", err)
		}
		defer reopened.Close()
		reopened.SetMaxOpenConns(1)
		if err := runMigrationsThroughVersionForTest(reopened, 54); err != nil {
			t.Fatalf("rerun migrations: %v", err)
		}
		var rowCountReopen int
		if err := reopened.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips`).Scan(&rowCountReopen); err != nil {
			t.Fatalf("count rows after reopen: %v", err)
		}
		if rowCountReopen != 1 {
			t.Fatalf("rows after reopen = %d, want 1", rowCountReopen)
		}
	})

	t.Run("reject_column_type_mismatch", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id INTEGER NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_column_order_different", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				series_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			)`)
	})

	t.Run("reject_non_unique_extra_index", func(t *testing.T) {
		testMigration54RejectIncompatible(t, `DROP TABLE recurring_occurrence_skips;
			CREATE TABLE recurring_occurrence_skips (
				workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
				recurrence_sequence INTEGER NOT NULL CHECK (recurrence_sequence > 0),
				original_transaction_id TEXT,
				reason TEXT NOT NULL CHECK (reason IN ('single_delete')),
				deleted_by TEXT, deleted_at INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
			);
			CREATE INDEX idx_skips_deleted ON recurring_occurrence_skips(deleted_at)`)
	})

	t.Run("reject_trigger_before", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-reject-trigger-before.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`DROP TABLE IF EXISTS recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if _, err := raw.Exec(authorizedSkipsDDL); err != nil {
			t.Fatalf("create canonical: %v", err)
		}
		if _, err := raw.Exec(`CREATE TRIGGER trg_skips_before BEFORE INSERT ON recurring_occurrence_skips
		BEGIN INSERT INTO schema_migrations(version, name) VALUES (-1, 'test'); END`); err != nil {
			t.Fatalf("create BEFORE trigger: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		migrationErr := runMigrations(opened)
		opened.Close()
		if migrationErr == nil {
			t.Fatal("migration 54 should reject table with BEFORE trigger")
		}
	})

	t.Run("reject_trigger_after", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-reject-trigger-after.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`DROP TABLE IF EXISTS recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if _, err := raw.Exec(authorizedSkipsDDL); err != nil {
			t.Fatalf("create canonical: %v", err)
		}
		if _, err := raw.Exec(`CREATE TRIGGER trg_skips_after AFTER INSERT ON recurring_occurrence_skips
		BEGIN INSERT INTO schema_migrations(version, name) VALUES (-1, 'test'); END`); err != nil {
			t.Fatalf("create schema_migrations: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		migrationErr := runMigrations(opened)
		opened.Close()
		if migrationErr == nil {
			t.Fatal("migration 54 should reject table with AFTER trigger")
		}
	})

	t.Run("reject_partial_index", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-reject-partial-idx.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`DROP TABLE IF EXISTS recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if _, err := raw.Exec(authorizedSkipsDDL); err != nil {
			t.Fatalf("create canonical: %v", err)
		}
		if _, err := raw.Exec(`CREATE INDEX idx_skips_partial ON recurring_occurrence_skips(deleted_at) WHERE deleted_at > 0`); err != nil {
			t.Fatalf("create partial index: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		migrationErr := runMigrations(opened)
		opened.Close()
		if migrationErr == nil {
			t.Fatal("migration 54 should reject table with partial index")
		}
	})

	t.Run("reject_expression_index", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-reject-expr-idx.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`DROP TABLE IF EXISTS recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if _, err := raw.Exec(authorizedSkipsDDL); err != nil {
			t.Fatalf("create canonical: %v", err)
		}
		if _, err := raw.Exec(`CREATE INDEX idx_skips_expr ON recurring_occurrence_skips(ABS(recurrence_sequence))`); err != nil {
			t.Fatalf("create expression index: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		migrationErr := runMigrations(opened)
		opened.Close()
		if migrationErr == nil {
			t.Fatal("migration 54 should reject table with expression index")
		}
	})

	t.Run("reject_table_with_generated_extra_column", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "migration54-reject-generated.db")
		raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw: %v", err)
		}
		raw.SetMaxOpenConns(1)
		if _, err := raw.Exec(`DROP TABLE IF EXISTS recurring_occurrence_skips`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if _, err := raw.Exec(`CREATE TABLE recurring_occurrence_skips (
			workspace_id TEXT NOT NULL, series_id TEXT NOT NULL,
			recurrence_sequence INTEGER NOT NULL, original_transaction_id TEXT,
			reason TEXT NOT NULL, deleted_by TEXT, deleted_at INTEGER NOT NULL,
			generated_col TEXT GENERATED ALWAYS AS (workspace_id || series_id),
			real_col TEXT NOT NULL,
			PRIMARY KEY (workspace_id, series_id, recurrence_sequence)
		)`); err != nil {
			t.Fatalf("create table with generated column: %v", err)
		}
		seedAppliedMigrationsBefore(t, raw, 54)
		if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		raw.Close()

		opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		opened.SetMaxOpenConns(1)
		migrationErr := runMigrations(opened)
		opened.Close()
		if migrationErr == nil {
			t.Fatal("migration 54 should reject table with extra generated column")
		}
	})
}

func testMigration54RejectIncompatible(t *testing.T, incompatibleDDL string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "migration54-reject.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(ddl); err != nil {
		t.Fatalf("fresh ddl: %v", err)
	}
	for _, stmt := range strings.Split(incompatibleDDL, ";\n") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("exec incompatible setup %q: %v", stmt, err)
		}
	}

	if _, err := raw.Exec(`INSERT OR IGNORE INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-sent', 'Sentinel', 'personal', 0, 0)`); err != nil {
		t.Fatalf("seed sentinel workspace: %v", err)
	}
	sentinelInserted := false
	if _, err := raw.Exec(`INSERT INTO recurring_occurrence_skips
		(workspace_id, series_id, recurrence_sequence, reason, deleted_at)
		VALUES ('ws-sent', 's-sent', 1, 'single_delete', 100)`); err != nil {
		sentinelInserted = false
	} else {
		sentinelInserted = true
	}

	snapshot := captureSchemaSnapshot(t, raw)
	snapshot.sentinelInserted = sentinelInserted

	var versionBefore int
	raw.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&versionBefore)
	_ = versionBefore
	seedAppliedMigrationsBefore(t, raw, 54)
	if _, err := raw.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	raw.Close()

	opened, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	opened.SetMaxOpenConns(1)
	migrationErr := runMigrations(opened)
	opened.Close()

	if migrationErr == nil {
		t.Fatal("migration 54 should reject incompatible table")
	}

	inspect, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("inspect after rejection: %v", err)
	}
	defer inspect.Close()

	var versionAfter int
	if err := inspect.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&versionAfter); err != nil {
		t.Fatalf("version after: %v", err)
	}
	if versionAfter == 53 {
		if versionAfter >= 54 {
			t.Fatalf("migration 54 registered after rejection: version=%d", versionAfter)
		}
	} else if versionAfter != 53 {
		t.Fatalf("version after rejection = %d, want 53", versionAfter)
	}

	assertSchemaSnapshotPreserved(t, inspect, snapshot)
}

func captureSchemaSnapshot(t *testing.T, db *sql.DB) schemaSnapshot {
	t.Helper()
	s := schemaSnapshot{}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='recurring_occurrence_skips'`).Scan(&s.tableDDL); err != nil {
		s.tableDDL = ""
	}
	s.rowCount = 0
	if s.tableDDL != "" {
		db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips`).Scan(&s.rowCount)
	}

	idxRows, err := db.Query(`PRAGMA index_list(recurring_occurrence_skips)`)
	if err == nil {
		defer idxRows.Close()
		for idxRows.Next() {
			var seq, unique, partial int
			var name, origin string
			if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err == nil {
				s.indexes = append(s.indexes, indexSnap{name: name, unique: unique, origin: origin})
			}
		}
	}

	fkRows, err := db.Query(`PRAGMA foreign_key_list(recurring_occurrence_skips)`)
	if err == nil {
		defer fkRows.Close()
		for fkRows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err == nil {
				s.foreignKeys = append(s.foreignKeys, fkSnap{id: id, seq: seq, table: table, from: from, to: to, onUpdate: onUpdate, onDelete: onDelete, match: match})
			}
		}
	}

	trRows, err := db.Query(`SELECT name, sql FROM sqlite_schema WHERE type='trigger' AND tbl_name='recurring_occurrence_skips'`)
	if err == nil {
		defer trRows.Close()
		for trRows.Next() {
			var name, sql string
			if err := trRows.Scan(&name, &sql); err == nil {
				s.triggers = append(s.triggers, triggerSnap{name: name, sql: sql})
			}
		}
	}
	return s
}

type schemaSnapshot struct {
	tableDDL         string
	rowCount         int
	sentinelInserted bool
	indexes          []indexSnap
	foreignKeys      []fkSnap
	triggers         []triggerSnap
}

type indexSnap struct {
	name   string
	unique int
	origin string
}

type fkSnap struct {
	id       int
	seq      int
	table    string
	from     string
	to       string
	onUpdate string
	onDelete string
	match    string
}

type triggerSnap struct {
	name string
	sql  string
}

func assertSchemaSnapshotPreserved(t *testing.T, db *sql.DB, s schemaSnapshot) {
	t.Helper()
	if s.tableDDL != "" {
		var currentDDL string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name='recurring_occurrence_skips'`).Scan(&currentDDL); err != nil {
			t.Fatalf("read table DDL after rejection: %v", err)
		}
		if currentDDL != s.tableDDL {
			t.Fatalf("table DDL modified after rejection:\nbefore: %s\nafter:  %s", s.tableDDL, currentDDL)
		}
		var rowCount int
		db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips`).Scan(&rowCount)
		if s.sentinelInserted && rowCount != s.rowCount {
			t.Fatalf("row count changed after rejection: before=%d after=%d", s.rowCount, rowCount)
		}
	}

	idxRows, err := db.Query(`PRAGMA index_list(recurring_occurrence_skips)`)
	if err != nil {
		return
	}
	defer idxRows.Close()
	var currentIndexes []indexSnap
	for idxRows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			continue
		}
		currentIndexes = append(currentIndexes, indexSnap{name: name, unique: unique, origin: origin})
	}
	if len(currentIndexes) != len(s.indexes) {
		t.Fatalf("index count changed after rejection: before=%d after=%d", len(s.indexes), len(currentIndexes))
	}
	for i := range s.indexes {
		if currentIndexes[i] != s.indexes[i] {
			t.Fatalf("index diverged after rejection: before=%#v after=%#v", s.indexes[i], currentIndexes[i])
		}
	}

	var fkCount int
	fkRows, err := db.Query(`PRAGMA foreign_key_list(recurring_occurrence_skips)`)
	if err == nil {
		defer fkRows.Close()
		var currentFKs []fkSnap
		for fkRows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if err := fkRows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err == nil {
				currentFKs = append(currentFKs, fkSnap{id: id, seq: seq, table: table, from: from, to: to, onUpdate: onUpdate, onDelete: onDelete, match: match})
			}
		}
		if len(currentFKs) != len(s.foreignKeys) {
			t.Fatalf("FK count changed after rejection: before=%d after=%d", len(s.foreignKeys), len(currentFKs))
		}
		for i := range s.foreignKeys {
			if currentFKs[i] != s.foreignKeys[i] {
				t.Fatalf("FK diverged: before=%#v after=%#v", s.foreignKeys[i], currentFKs[i])
			}
		}
		fkCount = len(currentFKs)
	}
	_ = fkCount

	trRows, err := db.Query(`SELECT name, sql FROM sqlite_schema WHERE type='trigger' AND tbl_name='recurring_occurrence_skips' ORDER BY name`)
	if err != nil {
		return
	}
	defer trRows.Close()
	var currentTriggers []triggerSnap
	for trRows.Next() {
		var name, sql string
		if err := trRows.Scan(&name, &sql); err != nil {
			continue
		}
		currentTriggers = append(currentTriggers, triggerSnap{name: name, sql: sql})
	}
	if len(currentTriggers) != len(s.triggers) {
		t.Fatalf("trigger count changed after rejection: before=%d after=%d", len(s.triggers), len(currentTriggers))
	}
	for i := range s.triggers {
		if currentTriggers[i] != s.triggers[i] {
			t.Fatalf("trigger diverged after rejection: before=%#v after=%#v", s.triggers[i], currentTriggers[i])
		}
	}
}

func TestMigration53ActiveLeafInvariantValidAndIncompatibleChain(t *testing.T) {
	const jan10 int64 = 1768051200
	now := time.Now().Unix()

	prepareV52DB := func(t *testing.T, dir string) string {
		t.Helper()
		dbPath := filepath.Join(dir, "migration53.db")
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("open raw database: %v", err)
		}
		db.SetMaxOpenConns(1)

		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatalf("execute ddl fixture: %v", err)
		}
		seedAppliedMigrationsBefore(t, db, 53)

		for _, s := range []string{
			`INSERT OR IGNORE INTO users (id, name, email, password_hash) VALUES ('u-mig53', 'User Mig53', 'mig53@e.com', 'hash')`,
			`INSERT OR IGNORE INTO workspaces (id, name, type) VALUES ('ws-mig53', 'WS Mig53', 'personal')`,
			`INSERT OR IGNORE INTO workspace_members (workspace_id, user_id, role) VALUES ('ws-mig53', 'u-mig53', 'ADMIN')`,
		} {
			if _, err := db.Exec(s); err != nil {
				db.Close()
				t.Fatalf("seed: %v", err)
			}
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-mig53', 'ws-mig53', 'Conta', 'CHECKING', 100000, 100000, ?, ?)`, now, now); err != nil {
			db.Close()
			t.Fatalf("seed account: %v", err)
		}
		db.Close()
		return dbPath
	}

	t.Run("compatible", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := prepareV52DB(t, dir)

		dbRaw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen raw: %v", err)
		}
		dbRaw.SetMaxOpenConns(1)
		defer dbRaw.Close()

		if _, err := dbRaw.Exec(`INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-mig53-compat', 'ws-mig53', 'u-mig53', 1, ?, ?)`, now, now); err != nil {
			t.Fatalf("insert series: %v", err)
		}
		dbRaw.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, effective_end_sequence, created_at, updated_at) VALUES ('rule-mig53-a', 'ws-mig53', 'u-mig53', 'acc-mig53', NULL, 'EXPENSE', 10000, 'A', ?, 'MONTHLY', 'PENDING', 1, NULL, 'series-mig53-compat', 1, 3, ?, ?)`, jan10, now, now)
		dbRaw.Exec(`UPDATE recurring_rules SET active=0 WHERE id='rule-mig53-a'`)
		dbRaw.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig53-b', 'ws-mig53', 'u-mig53', 'acc-mig53', NULL, 'EXPENSE', 20000, 'B', ?, 'MONTHLY', 'PENDING', 1, NULL, 'series-mig53-compat', 'rule-mig53-a', 4, ?, ?)`, jan10, now, now)
		dbRaw.Exec(`UPDATE recurring_rules SET active=0 WHERE id='rule-mig53-b'`)
		dbRaw.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig53-c', 'ws-mig53', 'u-mig53', 'acc-mig53', NULL, 'EXPENSE', 30000, 'C', ?, 'MONTHLY', 'PENDING', 1, NULL, 'series-mig53-compat', 'rule-mig53-b', 7, ?, ?)`, jan10, now, now)

		rowsBefore := snapshotRecurringRows(t, dbRaw, "ws-mig53")
		var versionBefore int
		dbRaw.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&versionBefore)
		if versionBefore != 52 {
			t.Fatalf("version before = %d, want 52", versionBefore)
		}

		dbRaw.Close()

		opened, err := Open(dbPath)
		if err != nil {
			t.Fatalf("database.Open on v52 compatible: %v", err)
		}
		defer opened.Close()

		var versionAfter int
		opened.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&versionAfter)
		if versionAfter < 57 {
			t.Fatalf("version after database.Open = %d, want >= 57", versionAfter)
		}

		for _, name := range []string{
			"trg_recurring_rules_no_active_parent_insert",
			"trg_recurring_rules_no_active_parent_update",
			"trg_recurring_rules_no_reactivation_with_children",
		} {
			var count int
			opened.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count)
			if count != 1 {
				t.Fatalf("trigger %s missing after database.Open", name)
			}
		}

		var idxCount int
		opened.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_recurring_rules_parent_rule_id'`).Scan(&idxCount)
		if idxCount != 1 {
			t.Fatal("idx_recurring_rules_parent_rule_id missing after database.Open")
		}

		rowsAfter := snapshotRecurringRows(t, opened, "ws-mig53")
		if rowsAfter != rowsBefore {
			t.Fatalf("database.Open changed data: before=%q after=%q", rowsBefore, rowsAfter)
		}

		_, err = opened.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig53-child-test', 'ws-mig53', 'u-mig53', 'acc-mig53', NULL, 'EXPENSE', 10000, 'ChildTest', ?, 'MONTHLY', 'PENDING', 1, 'series-mig53-compat', 'rule-mig53-c', 8, ?, ?)`, jan10, now, now)
		if err == nil {
			t.Fatal("insert child for active parent C should be rejected by trigger")
		}
		if !strings.Contains(strings.ToUpper(err.Error()), "ACTIVE RULE CANNOT HAVE CHILDREN") {
			t.Fatalf("expected invariant trigger error, got: %v", err)
		}
	})

	t.Run("incompatible_then_fixed", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := prepareV52DB(t, dir)

		dbRaw, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen raw: %v", err)
		}
		dbRaw.SetMaxOpenConns(1)

		if _, err := dbRaw.Exec(`INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at) VALUES ('series-mig53-incomp', 'ws-mig53', 'u-mig53', 1, ?, ?)`, now, now); err != nil {
			dbRaw.Close()
			t.Fatalf("insert series: %v", err)
		}
		dbRaw.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig53-active-parent', 'ws-mig53', 'u-mig53', 'acc-mig53', NULL, 'EXPENSE', 10000, 'ActiveParent', ?, 'MONTHLY', 'PENDING', 1, NULL, 'series-mig53-incomp', 1, ?, ?)`, jan10, now, now)
		dbRaw.Exec(`INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at) VALUES ('rule-mig53-active-child', 'ws-mig53', 'u-mig53', 'acc-mig53', NULL, 'EXPENSE', 20000, 'ActiveChild', ?, 'MONTHLY', 'PENDING', 1, NULL, 'series-mig53-incomp', 'rule-mig53-active-parent', 2, ?, ?)`, jan10, now, now)

		snapshotBefore := snapshotRecurringRows(t, dbRaw, "ws-mig53")
		var versionBefore int
		dbRaw.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&versionBefore)
		if versionBefore != 52 {
			dbRaw.Close()
			t.Fatalf("version before = %d, want 52", versionBefore)
		}

		dbRaw.Close()

		_, openErr := Open(dbPath)
		if openErr == nil {
			t.Fatal("database.Open should fail on incompatible v52 database")
		}

		inspectDB, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatalf("reopen for inspection: %v", err)
		}
		defer inspectDB.Close()
		inspectDB.SetMaxOpenConns(1)

		var versionAfterFail int
		inspectDB.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&versionAfterFail)
		if versionAfterFail != 52 {
			t.Fatalf("version after incompatible = %d, want 52", versionAfterFail)
		}

		snapshotAfterFail := snapshotRecurringRows(t, inspectDB, "ws-mig53")
		if snapshotAfterFail != snapshotBefore {
			t.Fatalf("incompatible database.Open changed data: before=%q after=%q", snapshotBefore, snapshotAfterFail)
		}

		for _, name := range []string{
			"trg_recurring_rules_no_active_parent_insert",
			"trg_recurring_rules_no_active_parent_update",
			"trg_recurring_rules_no_reactivation_with_children",
		} {
			var count int
			inspectDB.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count)
			if count != 0 {
				t.Fatalf("trigger %s exists after incompatible database.Open rollback", name)
			}
		}

		var idxAfterFail int
		inspectDB.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_recurring_rules_parent_rule_id'`).Scan(&idxAfterFail)
		if idxAfterFail != 0 {
			t.Fatal("idx_recurring_rules_parent_rule_id exists after incompatible database.Open rollback")
		}

		inspectDB.Exec(`UPDATE recurring_rules SET active=0 WHERE id='rule-mig53-active-parent'`)
		inspectDB.Close()

		openedRetry, err := Open(dbPath)
		if err != nil {
			t.Fatalf("database.Open after fixing chain: %v", err)
		}
		defer openedRetry.Close()

		var versionAfterRetry int
		openedRetry.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&versionAfterRetry)
		if versionAfterRetry < 57 {
			t.Fatalf("version after retry = %d, want >= 57", versionAfterRetry)
		}

		for _, name := range []string{
			"trg_recurring_rules_no_active_parent_insert",
			"trg_recurring_rules_no_active_parent_update",
			"trg_recurring_rules_no_reactivation_with_children",
		} {
			var count int
			openedRetry.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count)
			if count != 1 {
				t.Fatalf("trigger %s missing after fixed retry", name)
			}
		}
	})

	t.Run("fresh_database", func(t *testing.T) {
		db, err := Open(":memory:")
		if err != nil {
			t.Fatalf("database.Open fresh :memory:: %v", err)
		}
		defer db.Close()

		var version int
		db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version)
		if version < 57 {
			t.Fatalf("fresh DB version = %d, want >= 57", version)
		}

		for _, name := range []string{
			"trg_recurring_rules_no_active_parent_insert",
			"trg_recurring_rules_no_active_parent_update",
			"trg_recurring_rules_no_reactivation_with_children",
		} {
			var count int
			db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&count)
			if count != 1 {
				t.Fatalf("trigger %s missing on fresh database", name)
			}
		}

		var idxCount int
		db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='index' AND name='idx_recurring_rules_parent_rule_id'`).Scan(&idxCount)
		if idxCount != 1 {
			t.Fatal("idx_recurring_rules_parent_rule_id missing on fresh database")
		}
	})
}

func TestNormalizeSkipsDDL(t *testing.T) {
	canonical := normalizeSkipsDDL(authorizedSkipsDDL)

	t.Run("canonical_matches_itself", func(t *testing.T) {
		if normalizeSkipsDDL(authorizedSkipsDDL) != canonical {
			t.Fatal("authorized DDL does not normalize to itself")
		}
	})

	t.Run("canonical_with_trailing_semicolon", func(t *testing.T) {
		if normalizeSkipsDDL(authorizedSkipsDDL+";") != canonical {
			t.Fatal("trailing semicolon not stripped")
		}
	})

	t.Run("canonical_with_trailing_semicolon_and_whitespace", func(t *testing.T) {
		if normalizeSkipsDDL(authorizedSkipsDDL+"\n  ;\n") != canonical {
			t.Fatal("trailing semicolon with whitespace not stripped")
		}
	})

	t.Run("canonical_with_double_semicolons_is_different", func(t *testing.T) {
		if normalizeSkipsDDL(authorizedSkipsDDL+";;") == canonical {
			t.Fatal("double semicolons should not normalize to canonical")
		}
	})

	t.Run("canonical_with_internal_semicolons_is_different", func(t *testing.T) {
		ddl := strings.Replace(authorizedSkipsDDL, ")", ";)", 1)
		if normalizeSkipsDDL(ddl) == canonical {
			t.Fatal("internal semicolons should not normalize to canonical")
		}
	})

	t.Run("semicolon_in_literal_preserved", func(t *testing.T) {
		ddl := strings.Replace(authorizedSkipsDDL, "'single_delete'", "'single;delete'", 1)
		n := normalizeSkipsDDL(ddl)
		if !strings.Contains(n, "'single;delete'") || n == canonical {
			t.Fatalf("semicolon in literal not preserved: %q", n)
		}
	})

	t.Run("semicolon_in_double_quote_preserved", func(t *testing.T) {
		ddl := `CREATE TABLE t ("col;a" TEXT)`
		n := normalizeSkipsDDL(ddl)
		if !strings.Contains(n, "\"col;a\"") {
			t.Fatalf("semicolon in double-quoted id not preserved: %q", n)
		}
	})

	t.Run("semicolon_in_bracket_preserved", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t ([col;a] TEXT)")
		if !strings.Contains(n, "[col;a]") {
			t.Fatalf("semicolon in bracket id not preserved: %q", n)
		}
	})

	t.Run("semicolon_in_backtick_preserved", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t (`col;a` TEXT)")
		if !strings.Contains(n, "`col;a`") {
			t.Fatalf("semicolon in backtick id not preserved: %q", n)
		}
	})

	t.Run("escaped_single_quote_in_literal", func(t *testing.T) {
		n := normalizeSkipsDDL("VALUES ('');")
		if !strings.Contains(n, "''") {
			t.Fatalf("escaped single quote broken: %q", n)
		}
	})

	t.Run("escaped_double_quote_in_identifier", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t (\"col\"\"name\" TEXT)")
		if !strings.Contains(n, "\"col\"\"name\"") {
			t.Fatalf("escaped double quote broken: %q", n)
		}
	})

	t.Run("whitespace_inside_literal_preserved", func(t *testing.T) {
		n := normalizeSkipsDDL("INSERT INTO t VALUES ('a b  c');")
		if !strings.Contains(n, "'a b  c'") {
			t.Fatalf("whitespace in literal not preserved: %q", n)
		}
	})

	t.Run("empty_input", func(t *testing.T) {
		if normalizeSkipsDDL("") != "" {
			t.Fatal("empty input should be empty")
		}
	})

	t.Run("only_semicolon", func(t *testing.T) {
		if normalizeSkipsDDL(";") != "" {
			t.Fatalf("sole semicolon should produce empty: %q", normalizeSkipsDDL(";"))
		}
	})

	t.Run("different_casing_matches", func(t *testing.T) {
		lower := strings.ToLower(authorizedSkipsDDL)
		if normalizeSkipsDDL(lower) != canonical {
			t.Fatal("case-insensitive normalization failed")
		}
	})

	t.Run("different_newlines_matches", func(t *testing.T) {
		withWin := strings.ReplaceAll(authorizedSkipsDDL, "\n", "\r\n")
		if normalizeSkipsDDL(withWin) != canonical {
			t.Fatal("\\\\r\\\\n normalization failed")
		}
	})

	t.Run("ddl_with_extra_token_is_different", func(t *testing.T) {
		if normalizeSkipsDDL(authorizedSkipsDDL+"; SELECT 1;") == canonical {
			t.Fatal("DDL with extra SELECT should not normalize to canonical")
		}
	})

	t.Run("comment_before_ddl_is_different", func(t *testing.T) {
		if normalizeSkipsDDL("/* comment */\n"+authorizedSkipsDDL) == canonical {
			t.Fatal("DDL with leading comment should not normalize to canonical")
		}
	})

	t.Run("unterminated_single_quote", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t (a TEXT, CHECK (reason IN ('single_delete)))")
		if n == canonical {
			t.Fatal("unterminated literal should not normalize to canonical")
		}
	})

	t.Run("unterminated_double_quote", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t (a TEXT)")
		_ = n
		n2 := normalizeSkipsDDL("CREATE TABLE t (\"a TEXT)")
		if n2 == n {
			t.Fatal("unterminated double-quoted id should differ from valid DDL")
		}
	})

	t.Run("unterminated_bracket", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t ([a TEXT)")
		if normalizeSkipsDDL("CREATE TABLE t (A TEXT)") == n {
			t.Fatal("unterminated bracket should differ from valid DDL")
		}
	})

	t.Run("unterminated_backtick", func(t *testing.T) {
		n := normalizeSkipsDDL("CREATE TABLE t (`a TEXT)")
		if normalizeSkipsDDL("CREATE TABLE t (A TEXT)") == n {
			t.Fatal("unterminated backtick should differ from valid DDL")
		}
	})

	t.Run("literal_SINGLE_DELETE_preserved", func(t *testing.T) {
		ddl := strings.Replace(authorizedSkipsDDL, "'single_delete'", "'SINGLE_DELETE'", 1)
		n := normalizeSkipsDDL(ddl)
		if !strings.Contains(n, "'SINGLE_DELETE'") {
			t.Fatalf("'SINGLE_DELETE' literal not preserved: %q", n)
		}
		if n == canonical {
			t.Fatal("'SINGLE_DELETE' should differ from 'single_delete'")
		}
	})

	t.Run("canonical_with_semicolon_space_semicolon_is_different", func(t *testing.T) {
		if normalizeSkipsDDL(authorizedSkipsDDL+" ; ;") == canonical {
			t.Fatal("'DDL ; ;' should not normalize to canonical")
		}
	})
}
