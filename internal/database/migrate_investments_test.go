package database

import (
	"database/sql"
	"testing"
)

// --- fixtures ---------------------------------------------------------------

func openInvestmentsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return db
}

func investmentsFreshDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openInvestmentsTestDB(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// investmentsLegacyDB64 reproduces a bank migrated to schema 64 with
// pre-existing financial data, ready for runMigrations to apply only v65.
func investmentsLegacyDB64(t *testing.T) *sql.DB {
	t.Helper()
	db := openInvestmentsTestDB(t)
	for _, m := range migrations {
		if m.version >= 65 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			t.Fatalf("legacy migration %d: %v", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seedAppliedMigrationsBefore(t, db, 65)
	seedInvestmentFixtures(t, db)
	return db
}

func seedInvestmentFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO workspaces (id, name) VALUES ('ws-1', 'WS One')`,
		`INSERT INTO workspaces (id, name) VALUES ('ws-2', 'WS Two')`,
		`INSERT INTO users (id, name, email, password_hash) VALUES ('user-1', 'Owner', 'owner@example.test', 'hash')`,
		`INSERT INTO users (id, name, email, password_hash) VALUES ('user-2', 'Other', 'other@example.test', 'hash')`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-inv', 'ws-1', 'Corretora', 'INVESTMENT', 0, 0)`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-checking', 'ws-1', 'Banco', 'CHECKING', 10000, 10000)`,
		`INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status) VALUES ('existing-transaction', 'ws-1', 'user-1', 'acc-checking', 'EXPENSE', 19999, '2026-09-01', 'Existing expense', 'paid')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed investments fixture: %v", err)
		}
	}
}

const insertValidInvestment = `INSERT INTO investments (
	id, workspace_id, account_id, name, institution, kind, principal_cents,
	tax_regime, applied_date, maturity_date, created_by
) VALUES (
	'inv-1', 'ws-1', 'acc-inv', 'CDB Banco X', 'Banco X', 'CDB', 100000,
	'IR_REGRESSIVO', '2026-01-01', NULL, 'user-1'
)`

func mustExecError(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err == nil {
		t.Fatalf("esperado REJEITADO, foi aceito: %s", statement)
	}
}

func mustExecOK(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("esperado ACEITO, foi rejeitado (%v): %s", err, statement)
	}
}

func countQueryRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func schemaObjectExists(t *testing.T, db *sql.DB, objectType, name string) bool {
	t.Helper()
	return countQueryRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, objectType, name) == 1
}

// --- schema ------------------------------------------------------------------

func TestMigration65FreshInstallCreatesSchema(t *testing.T) {
	db := investmentsFreshDB(t)
	assertMigrationApplied(t, db, 65)

	for _, table := range []string{"investments", "investment_valuations"} {
		if !schemaObjectExists(t, db, "table", table) {
			t.Fatalf("tabela %s ausente", table)
		}
	}
	for _, index := range []string{
		"idx_investments_id_workspace",
		"idx_investments_workspace_status",
		"idx_investments_workspace_account",
		"idx_investment_valuations_workspace_date",
	} {
		if !schemaObjectExists(t, db, "index", index) {
			t.Fatalf("índice %s ausente", index)
		}
	}
	var uniqueFlag int
	if err := db.QueryRow(`SELECT "unique" FROM pragma_index_list('investments') WHERE name = 'idx_investments_id_workspace'`).Scan(&uniqueFlag); err != nil {
		t.Fatal(err)
	}
	if uniqueFlag != 1 {
		t.Fatal("idx_investments_id_workspace deve ser único (pai da FK composta)")
	}
	for _, trigger := range []string{
		"trg_investments_validate_insert",
		"trg_investments_immutable_keys",
		"trg_investment_valuations_validate_insert",
		"trg_investment_valuations_validate_update",
		"trg_investment_valuations_immutable_keys",
	} {
		if !schemaObjectExists(t, db, "trigger", trigger) {
			t.Fatalf("trigger %s ausente", trigger)
		}
	}

	// Idempotência: reaplicar o runner completo não duplica nem falha.
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 65)
	if countQueryRows(t, db, `SELECT COUNT(*) FROM schema_migrations WHERE version = 65`) != 1 {
		t.Fatal("migration 65 marcada mais de uma vez")
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}

func TestMigration65UpgradeFrom64PreservesData(t *testing.T) {
	db := investmentsLegacyDB64(t)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	assertMigrationApplied(t, db, 65)

	var initial, current int64
	var typeName, transactionDescription string
	var transactionAmount int64
	if err := db.QueryRow(`SELECT initial_balance, current_balance, type FROM accounts WHERE id = 'acc-checking'`).Scan(&initial, &current, &typeName); err != nil {
		t.Fatal(err)
	}
	if initial != 10000 || current != 10000 || typeName != "CHECKING" {
		t.Fatalf("upgrade alterou dados de accounts: %d/%d/%s", initial, current, typeName)
	}
	if err := db.QueryRow(`SELECT amount, description FROM transactions WHERE id = 'existing-transaction'`).Scan(&transactionAmount, &transactionDescription); err != nil {
		t.Fatal(err)
	}
	if transactionAmount != 19999 || transactionDescription != "Existing expense" {
		t.Fatalf("upgrade alterou transação existente: %d/%s", transactionAmount, transactionDescription)
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM transactions`) != 1 {
		t.Fatal("upgrade alterou a contagem de transações")
	}

	// Reaplicação idempotente.
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}

// --- tenant ------------------------------------------------------------------

func TestMigration65TenantIsolation(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)

	// Conta de outro workspace: rejeitado.
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-cross', 'ws-2', 'acc-inv', 'X', 'CDB', 1000, 'MANUAL', '2026-01-01', 'user-2')`)

	// Conta não INVESTMENT: rejeitado.
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-checking', 'ws-1', 'acc-checking', 'X', 'CDB', 1000, 'MANUAL', '2026-01-01', 'user-1')`)

	// Conta correta: aceito.
	mustExecOK(t, db, insertValidInvestment)

	// Valuation de outro workspace: rejeitada.
	mustExecError(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-cross', 'ws-2', 'inv-1', '2026-02-01', 1100, 'user-2')`)

	// Valuation para investimento inexistente: rejeitada.
	mustExecError(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-ghost', 'ws-1', 'inv-nada', '2026-02-01', 1100, 'user-1')`)

	if countQueryRows(t, db, `SELECT COUNT(*) FROM investments`) != 1 {
		t.Fatal("apenas o investimento válido deveria existir")
	}
	if countQueryRows(t, db, `SELECT COUNT(*) FROM investment_valuations`) != 0 {
		t.Fatal("nenhuma valuation rejeitada deveria persistir")
	}
}

// --- invariantes de investment ------------------------------------------------

func TestMigration65InvestmentInvariants(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)

	// Campos históricos do lote são imutáveis.
	mustExecError(t, db, `UPDATE investments SET principal_cents = 200000 WHERE id = 'inv-1'`)
	mustExecError(t, db, `UPDATE investments SET account_id = 'acc-checking' WHERE id = 'inv-1'`)
	mustExecError(t, db, `UPDATE investments SET workspace_id = 'ws-2' WHERE id = 'inv-1'`)
	mustExecError(t, db, `UPDATE investments SET applied_date = '2026-02-01' WHERE id = 'inv-1'`)
	mustExecError(t, db, `UPDATE investments SET created_by = 'user-2' WHERE id = 'inv-1'`)
	mustExecError(t, db, `UPDATE investments SET created_at = 1 WHERE id = 'inv-1'`)

	// Campos de domínio seguem editáveis enquanto ATIVA.
	mustExecOK(t, db, `UPDATE investments SET name = 'CDB Banco Y', institution = 'Banco Y', kind = 'CDB', tax_regime = 'ISENTO_PF', maturity_date = '2027-01-01', yield_note = '110% CDI', updated_at = unixepoch() WHERE id = 'inv-1'`)

	// ATIVA não arquiva.
	mustExecError(t, db, `UPDATE investments SET archived_at = unixepoch() WHERE id = 'inv-1'`)

	// RESGATADA exige redeemed_date + redeemed_by (CHECK simétrico).
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, status, created_by
	) VALUES ('inv-redeemed-broken', 'ws-1', 'acc-inv', 'X', 'CDB', 1000, 'MANUAL', '2026-01-01', 'RESGATADA', 'user-1')`)

	// Regime fora da lista congelada (sem aliases).
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-regime', 'ws-1', 'acc-inv', 'X', 'CDB', 1000, 'TRIBUTADO', '2026-01-01', 'user-1')`)

	// Principal precisa ser positivo.
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-zero', 'ws-1', 'acc-inv', 'X', 'CDB', 0, 'MANUAL', '2026-01-01', 'user-1')`)
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-negative', 'ws-1', 'acc-inv', 'X', 'CDB', -5, 'MANUAL', '2026-01-01', 'user-1')`)

	// Campos estruturalmente obrigatórios.
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-noname', 'ws-1', 'acc-inv', '   ', 'CDB', 1000, 'MANUAL', '2026-01-01', 'user-1')`)
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-nokind', 'ws-1', 'acc-inv', 'X', '', 1000, 'MANUAL', '2026-01-01', 'user-1')`)

	// Datas civis: CHECK estrutural (semântica fina fica no internal/civildate).
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-baddate', 'ws-1', 'acc-inv', 'X', 'CDB', 1000, 'MANUAL', '01/2026', 'user-1')`)
	mustExecError(t, db, `INSERT INTO investments (
		id, workspace_id, account_id, name, kind, principal_cents, tax_regime, applied_date, created_by
	) VALUES ('inv-badmonth', 'ws-1', 'acc-inv', 'X', 'CDB', 1000, 'MANUAL', '2026-13-01', 'user-1')`)
}

// --- invariantes de valuation ---------------------------------------------------

func TestMigration65ValuationInvariants(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)

	// Antes da aplicação: rejeitada.
	mustExecError(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-early', 'ws-1', 'inv-1', '2025-12-31', 1000, 'user-1')`)

	// Zero é permitido (perda total); negativo é rejeitado.
	mustExecOK(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-zero', 'ws-1', 'inv-1', '2026-02-01', 0, 'user-1')`)
	mustExecError(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-negative', 'ws-1', 'inv-1', '2026-03-01', -1, 'user-1')`)

	// Par updated_by/updated_at é inseparável.
	mustExecError(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by, updated_by
	) VALUES ('val-orphan-update', 'ws-1', 'inv-1', '2026-04-01', 1000, 'user-1', 'user-2')`)

	// Identidade/autoria imutáveis; amount mutável.
	mustExecError(t, db, `UPDATE investment_valuations SET valuation_date = '2026-05-01' WHERE id = 'val-zero'`)
	mustExecError(t, db, `UPDATE investment_valuations SET id = 'val-other' WHERE id = 'val-zero'`)
	mustExecError(t, db, `UPDATE investment_valuations SET investment_id = 'inv-outro' WHERE id = 'val-zero'`)
	mustExecError(t, db, `UPDATE investment_valuations SET workspace_id = 'ws-2' WHERE id = 'val-zero'`)
	mustExecError(t, db, `UPDATE investment_valuations SET created_by = 'user-2' WHERE id = 'val-zero'`)
	mustExecError(t, db, `UPDATE investment_valuations SET created_at = 1 WHERE id = 'val-zero'`)
	mustExecOK(t, db, `UPDATE investment_valuations SET amount_cents = 500 WHERE id = 'val-zero'`)
}

func TestMigration65SameDayUpsertCorrectsSingleRow(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)

	upsert := `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at
	)
	VALUES (?, ?, ?, ?, ?, ?, unixepoch())
	ON CONFLICT(workspace_id, investment_id, valuation_date)
	DO UPDATE SET
		amount_cents = excluded.amount_cents,
		updated_by   = excluded.created_by,
		updated_at   = unixepoch()`

	if _, err := db.Exec(upsert, "v", "ws-1", "inv-1", "2026-02-01", 1000, "user-1"); err != nil {
		t.Fatal(err)
	}
	// Correção no mesmo dia por outro autor.
	if _, err := db.Exec(upsert, "v2", "ws-1", "inv-1", "2026-02-01", 1200, "user-2"); err != nil {
		t.Fatal(err)
	}

	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_valuations WHERE investment_id = 'inv-1'`); got != 1 {
		t.Fatalf("same-day UPSERT criou %d linhas, want 1", got)
	}
	var amount int64
	var createdBy, updatedBy string
	var createdAt, updatedAt int64
	err := db.QueryRow(`SELECT amount_cents, created_by, created_at, updated_by, updated_at
		FROM investment_valuations WHERE investment_id = 'inv-1'`).
		Scan(&amount, &createdBy, &createdAt, &updatedBy, &updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if amount != 1200 {
		t.Fatalf("amount = %d, want 1200 (última correção)", amount)
	}
	if createdBy != "user-1" || updatedBy != "user-2" {
		t.Fatalf("autoria: first=%s updated=%s, want user-1/user-2", createdBy, updatedBy)
	}
	if updatedAt == 0 || updatedAt < createdAt {
		t.Fatalf("updated_at=%d deve estar preenchido e >= created_at=%d", updatedAt, createdAt)
	}

	// Nova data cria linha nova (sem correção).
	if _, err := db.Exec(upsert, "v3", "ws-1", "inv-1", "2026-03-01", 1300, "user-1"); err != nil {
		t.Fatal(err)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_valuations WHERE investment_id = 'inv-1'`); got != 2 {
		t.Fatalf("linhas após nova data = %d, want 2", got)
	}
}

// --- resgate e arquivamento (schema garante o fluxo futuro) ----------------------

func TestMigration65RedeemFlowFeasibleAndFrozenAfterFlip(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)

	upsert := `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at
	)
	VALUES (?, ?, ?, ?, ?, ?, unixepoch())
	ON CONFLICT(workspace_id, investment_id, valuation_date)
	DO UPDATE SET
		amount_cents = excluded.amount_cents,
		updated_by   = excluded.created_by,
		updated_at   = unixepoch()`

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// 1) UPSERT da valuation final enquanto ATIVA.
	if _, err := tx.Exec(upsert, "val-redeem", "ws-1", "inv-1", "2026-06-30", 150000, "user-1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	// 2) Flip guardado por status = ATIVA.
	result, err := tx.Exec(`UPDATE investments
		SET status = 'RESGATADA', redeemed_date = '2026-06-30', redeemed_by = 'user-1', updated_at = unixepoch()
		WHERE id = 'inv-1' AND workspace_id = 'ws-1' AND status = 'ATIVA'`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if affected != 1 {
		tx.Rollback()
		t.Fatalf("rows affected = %d, want 1", affected)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Nova valuation pós-resgate: rejeitada.
	mustExecError(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-after', 'ws-1', 'inv-1', '2026-07-01', 160000, 'user-1')`)

	// Correção da valuation da data de resgate pós-flip: rejeitada (consequência aceita).
	mustExecError(t, db, `UPDATE investment_valuations SET amount_cents = 140000 WHERE id = 'val-redeem'`)

	// Re-resgate concorrente: 0 linhas afetadas.
	result, err = db.Exec(`UPDATE investments
		SET status = 'RESGATADA', redeemed_date = '2026-06-30', redeemed_by = 'user-1', updated_at = unixepoch()
		WHERE id = 'inv-1' AND workspace_id = 'ws-1' AND status = 'ATIVA'`)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 0 {
		t.Fatalf("re-resgate afetou %d linhas, want 0", affected)
	}
}

func TestMigration65ArchiveRules(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)

	// ATIVA + archived_at: rejeitado.
	mustExecError(t, db, `UPDATE investments SET archived_at = unixepoch() WHERE id = 'inv-1'`)

	// Resgata e arquiva: aceito.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE investments
		SET status = 'RESGATADA', redeemed_date = '2026-06-30', redeemed_by = 'user-1', updated_at = unixepoch()
		WHERE id = 'inv-1' AND workspace_id = 'ws-1' AND status = 'ATIVA'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	mustExecOK(t, db, `UPDATE investments SET archived_at = unixepoch(), updated_at = unixepoch()
		WHERE id = 'inv-1' AND workspace_id = 'ws-1' AND status = 'RESGATADA' AND archived_at IS NULL`)
	if archived := countQueryRows(t, db, `SELECT COUNT(*) FROM investments WHERE id = 'inv-1' AND archived_at IS NOT NULL`); archived != 1 {
		t.Fatal("arquivamento do resgatado não persistiu")
	}
}

// --- ciclo de vida das FKs --------------------------------------------------------

func TestMigration65ValuationsCascadeAndAccountsRestrict(t *testing.T) {
	db := investmentsFreshDB(t)
	seedInvestmentFixtures(t, db)
	mustExecOK(t, db, insertValidInvestment)
	mustExecOK(t, db, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by
	) VALUES ('val-cascade', 'ws-1', 'inv-1', '2026-02-01', 1100, 'user-1')`)

	// Conta com investimento: RESTRICT impede o apagamento.
	if _, err := db.Exec(`DELETE FROM accounts WHERE id = 'acc-inv'`); err == nil {
		t.Fatal("RESTRICT de account deve impedir delete com investimento vivo")
	}

	// Apagar o investimento cascadeia as valuations.
	if _, err := db.Exec(`DELETE FROM investments WHERE id = 'inv-1'`); err != nil {
		t.Fatal(err)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(*) FROM investment_valuations WHERE investment_id = 'inv-1'`); got != 0 {
		t.Fatalf("valuations após cascade = %d, want 0", got)
	}
	if err := assertDatabaseForeignKeyIntegrity(db); err != nil {
		t.Fatal(err)
	}
}
