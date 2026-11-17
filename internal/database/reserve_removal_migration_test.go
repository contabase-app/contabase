package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReservePhysicalRemovalCleanInstallAndReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "clean.db")
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open clean database: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(db, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("clean install retained reserve table %s", table)
		}
	}
	if err := sqlite79Integrity(db); err != nil {
		t.Fatalf("clean install integrity: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Reopen clean database: %v", err)
	}
	defer reopened.Close()
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(reopened, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("reopen recreated reserve table %s", table)
		}
	}
	if err := reopened.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("reopened schema version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestReservePhysicalRemovalRejectsUnexpectedTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unexpected.db")
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE boxes (id TEXT PRIMARY KEY)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open("file:" + dbPath); err == nil {
		t.Fatal("schema 83 with a recreated reserve table must fail closed")
	}
}

func reserveTableExists(q interface {
	QueryRow(string, ...any) *sql.Row
}, table string) (bool, error) {
	var count int
	err := q.QueryRow(`SELECT COUNT(1) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count)
	return count > 0, err
}

func TestReservePhysicalRemovalUpgradesSchema77Fixture(t *testing.T) {
	schema, err := os.ReadFile("testdata/investment_schema77.sql")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "legacy77.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(schema)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("upgrade schema 77: %v", err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("upgraded version = %d, want %d", version, currentSchemaVersion)
	}
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(db, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("upgrade retained reserve table %s", table)
		}
	}
	if err := sqlite79Integrity(db); err != nil {
		t.Fatalf("upgraded integrity: %v", err)
	}
}

func reserveRemovalSchema82(t *testing.T) *sql.DB {
	t.Helper()
	db := migration77Fixture(t)
	applyMigrations79Through82ForTest(t, db)
	mustExecOK(t, db, `INSERT INTO users(id,name,email,password_hash) VALUES
		('reserve-user','Reserve','reserve@example.test','hash')`)
	mustExecOK(t, db, `INSERT INTO workspaces(id,name) VALUES ('ws-a','A'),('ws-b','B')`)
	mustExecOK(t, db, `INSERT INTO accounts(id,workspace_id,name,type) VALUES
		('acc-a','ws-a','A','CHECKING'),('acc-b','ws-b','B','CHECKING')`)
	return db
}

func reserveRemovalExpense(t *testing.T, db *sql.DB, id, workspace, account string, amount int64) {
	t.Helper()
	mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,description,status)
		VALUES(?,?,'reserve-user',?,'EXPENSE',?,1,'reserve fixture','paid')`, id, workspace, account, amount)
}

func reserveRemovalBox(t *testing.T, db *sql.DB, id, workspace string) {
	t.Helper()
	mustExecOK(t, db, `INSERT INTO boxes(id,workspace_id,name) VALUES(?,?,?)`, id, workspace, id)
}

func reserveRemovalLedger(t *testing.T, db *sql.DB, id, box, source string, amount int64) {
	t.Helper()
	mustExecOK(t, db, `INSERT INTO box_virtual_ledger(id,box_id,amount,type,source_transaction_id,reference_date)
		VALUES(?,?,?,'CONSUME',?,1)`, id, box, amount, source)
}

func runReserveRemovalForTest(t *testing.T, db *sql.DB) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateReservePhysicalRemoval(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func TestReservePhysicalRemovalAllowsCommonExpenseLink(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalExpense(t, db, "common-expense", "ws-a", "acc-a", 1234)
	reserveRemovalBox(t, db, "box-common", "ws-a")
	reserveRemovalBox(t, db, "box-without-links", "ws-a")
	reserveRemovalLedger(t, db, "ledger-common", "box-common", "common-expense", -1234)

	if err := runReserveRemovalForTest(t, db); err != nil {
		t.Fatalf("common expense link must not block removal: %v", err)
	}
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(db, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("reserve table %s survived", table)
		}
	}
	var count, sum int64
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(amount),0) FROM transactions WHERE workspace_id='ws-a'`).Scan(&count, &sum); err != nil {
		t.Fatal(err)
	}
	if count != 1 || sum != 1234 {
		t.Fatalf("financial transaction changed: count=%d sum=%d", count, sum)
	}
}

func TestReservePhysicalRemovalBlocksRelevantInvestmentLink(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalExpense(t, db, "investment-purchase:inv-1", "ws-a", "acc-a", 50000)
	reserveRemovalBox(t, db, "box-investment", "ws-a")
	reserveRemovalLedger(t, db, "ledger-investment", "box-investment", "investment-purchase:inv-1", -50000)

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "investment purchase") {
		t.Fatalf("relevant investment link error = %v", err)
	}
	for _, table := range reserveRemovalTables {
		exists, tableErr := reserveTableExists(db, table)
		if tableErr != nil {
			t.Fatal(tableErr)
		}
		if !exists {
			t.Fatalf("blocked migration removed %s", table)
		}
	}
}

func TestReservePhysicalRemovalBlocksCrossWorkspaceReference(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalExpense(t, db, "foreign-expense", "ws-b", "acc-b", 900)
	reserveRemovalBox(t, db, "box-a", "ws-a")
	reserveRemovalLedger(t, db, "ledger-cross", "box-a", "foreign-expense", -900)

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "cross workspace") {
		t.Fatalf("cross-workspace error = %v", err)
	}
}

func TestReservePhysicalRemovalBlocksOrphanReference(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalBox(t, db, "box-orphan", "ws-a")
	reserveRemovalLedger(t, db, "ledger-orphan", "box-orphan", "missing-expense", -700)

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "missing transactions") {
		t.Fatalf("orphan error = %v", err)
	}
}

func TestReservePhysicalRemovalBlocksUnexpectedTrigger(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalBox(t, db, "box-trigger", "ws-a")
	mustExecOK(t, db, `CREATE TRIGGER trg_extra_reserve AFTER INSERT ON boxes BEGIN SELECT 1; END`)

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "unexpected schema object") {
		t.Fatalf("unexpected trigger error = %v", err)
	}
}

func TestReservePhysicalRemovalBlocksUnexpectedForeignKey(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalBox(t, db, "box-fk", "ws-a")
	mustExecOK(t, db, `CREATE TABLE reserve_shadow(id TEXT PRIMARY KEY, box_id TEXT, FOREIGN KEY(box_id) REFERENCES boxes(id))`)

	err := runReserveRemovalForTest(t, db)
	if err == nil || (!strings.Contains(err.Error(), "unexpected foreign key") && !strings.Contains(err.Error(), "unexpected schema object")) {
		t.Fatalf("unexpected foreign key error = %v", err)
	}
}

func TestReservePhysicalRemovalRollbackOnPostflightFailure(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	reserveRemovalBox(t, db, "box-rollback", "ws-a")
	mustExecOK(t, db, `INSERT INTO user_notifications(id,user_id,type) VALUES('reserve-notification','reserve-user','caixinha.recharge')`)
	mustExecOK(t, db, `CREATE TRIGGER trg_reserve_notification_reinsert AFTER DELETE ON user_notifications
		WHEN OLD.type LIKE 'caixinha.%'
		BEGIN
			INSERT INTO user_notifications(id,user_id,type) VALUES('reserve-notification-copy',OLD.user_id,OLD.type);
		END`)
	var beforeVersion int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "reserve notifications") {
		t.Fatalf("postflight failure error = %v", err)
	}
	for _, table := range reserveRemovalTables {
		exists, tableErr := reserveTableExists(db, table)
		if tableErr != nil {
			t.Fatal(tableErr)
		}
		if !exists {
			t.Fatalf("rollback did not restore %s", table)
		}
	}
	var notifications, afterVersion int
	if err := db.QueryRow(`SELECT COUNT(1) FROM user_notifications WHERE type LIKE 'caixinha.%'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("notifications after rollback = %d, want 1", notifications)
	}
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if afterVersion != beforeVersion {
		t.Fatalf("migration registry changed: %d -> %d", beforeVersion, afterVersion)
	}
}

func TestReservePhysicalRemovalBackupRestoreRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	dbPath := filepath.Join(t.TempDir(), "schema82.db")
	source := reserveRemovalSchema82(t)
	reserveRemovalExpense(t, source, "backup-expense", "ws-a", "acc-a", 4321)
	reserveRemovalBox(t, source, "backup-box", "ws-a")
	reserveRemovalLedger(t, source, "backup-ledger", "backup-box", "backup-expense", -4321)
	mustExecOK(t, source, `VACUUM INTO ?`, dbPath)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	live, err := OpenRuntimeDBs(dbPath)
	if err != nil {
		t.Fatalf("upgrade schema 82 with backup: %v", err)
	}
	defer live.Close()
	if countQueryRows(t, live.DB, `SELECT MAX(version) FROM schema_migrations`) != currentSchemaVersion {
		t.Fatal("live database did not reach schema 83")
	}
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(live.DB, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("live database retained %s", table)
		}
	}

	backups, err := filepath.Glob(filepath.Join(dataDir, "backups", "pre-migration", "*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup = %v, %v", backups, err)
	}
	if _, err := os.Stat(preMigrationBackupManifestPath(backups[0])); err != nil {
		t.Fatalf("pre-migration backup manifest: %v", err)
	}
	if err := ValidatePreMigrationBackup(backups[0]); err != nil {
		t.Fatalf("pre-migration backup validation: %v", err)
	}
	backup, err := sql.Open("sqlite", sqliteBackupURI(backups[0], "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	backup.SetMaxOpenConns(1)
	defer backup.Close()
	if got := countQueryRows(t, backup, `SELECT MAX(version) FROM schema_migrations`); got != 82 {
		t.Fatalf("backup schema = %d, want 82", got)
	}
	if got := countQueryRows(t, backup, `SELECT COUNT(1) FROM box_virtual_ledger WHERE id='backup-ledger'`); got != 1 {
		t.Fatal("backup lost reserve history")
	}
	if got := countQueryRows(t, backup, `SELECT amount FROM transactions WHERE id='backup-expense'`); got != 4321 {
		t.Fatal("backup lost financial transaction")
	}
	if err := sqlite79Integrity(backup); err != nil {
		t.Fatalf("backup integrity: %v", err)
	}

	restoredPath := filepath.Join(t.TempDir(), "restored82.db")
	if err := CreateSQLiteBackupFile(backup, backups[0], restoredPath); err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	restored, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+restoredPath))
	if err != nil {
		t.Fatal(err)
	}
	restored.SetMaxOpenConns(1)
	defer restored.Close()
	if got := countQueryRows(t, restored, `SELECT MAX(version) FROM schema_migrations`); got != 82 {
		t.Fatalf("restored schema = %d, want 82", got)
	}
	if got := countQueryRows(t, restored, `SELECT COUNT(1) FROM boxes WHERE id='backup-box'`); got != 1 {
		t.Fatal("restored backup lost reserve row")
	}
	if err := runReserveRemovalForTest(t, restored); err != nil {
		t.Fatalf("experimental migration on restored backup: %v", err)
	}
	if got := countQueryRows(t, restored, `SELECT COUNT(1) FROM transactions WHERE id='backup-expense' AND amount=4321`); got != 1 {
		t.Fatal("experimental migration changed financial data")
	}
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(restored, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("restored database retained %s", table)
		}
	}
	if err := sqlite79Integrity(restored); err != nil {
		t.Fatalf("restored schema 83 integrity: %v", err)
	}
}

func TestReservePhysicalRemovalRuntimePreflight(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "schema82-preflight.db")
	source := reserveRemovalSchema82(t)
	mustExecOK(t, source, `VACUUM INTO ?`, dbPath)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	// Read-only inspection accepts an intact pre-83 backup for restore checks,
	// and must not mutate it.
	if err := ValidateSQLiteRuntimeSchema("file:" + dbPath); err != nil {
		t.Fatalf("schema 82 read-only preflight: %v", err)
	}
	raw, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if got := countQueryRows(t, raw, `SELECT MAX(version) FROM schema_migrations`); got != 82 {
		raw.Close()
		t.Fatalf("preflight changed schema to %d", got)
	}
	raw.Close()

	cleanPath := filepath.Join(t.TempDir(), "schema83-clean.db")
	clean, err := Open("file:" + cleanPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := clean.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSQLiteRuntimeSchema("file:" + cleanPath); err != nil {
		t.Fatalf("schema 83 read-only preflight: %v", err)
	}

	raw, err = sql.Open("sqlite", normalizeSQLiteDSN("file:"+cleanPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE boxes(id TEXT PRIMARY KEY)`); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	raw.Close()
	if err := ValidateSQLiteRuntimeSchema("file:" + cleanPath); err == nil {
		t.Fatal("schema 83 with a recreated reserve table passed read-only preflight")
	}
}

func TestReservePhysicalRemovalAllowsCommonExpenseOnInvestmentAccount(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	mustExecOK(t, db, `UPDATE accounts SET type='INVESTMENT' WHERE workspace_id='ws-a' AND id='acc-a'`)
	ids := []string{
		"common-investment-account-expense-1",
		"common-investment-account-expense-2",
		"common-investment-account-expense-3",
		"common-investment-account-expense-4",
	}
	for i, id := range ids {
		reserveRemovalExpense(t, db, id, "ws-a", "acc-a", int64(3000+i))
		reserveRemovalBox(t, db, "box-investment-account-"+id, "ws-a")
		reserveRemovalLedger(t, db, "ledger-investment-account-"+id, "box-investment-account-"+id, id, int64(-3000-i))
	}

	if err := runReserveRemovalForTest(t, db); err != nil {
		t.Fatalf("common expenses on an investment account must not block removal: %v", err)
	}
	for _, table := range reserveRemovalTables {
		exists, err := reserveTableExists(db, table)
		if err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("reserve table %s survived", table)
		}
	}
	if got := countQueryRows(t, db, `SELECT COUNT(1) FROM transactions WHERE id LIKE 'common-investment-account-expense-%'`); got != len(ids) {
		t.Fatalf("common investment-account transactions preserved = %d, want %d", got, len(ids))
	}
}

func newReserveRemovalRelevantDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range []string{
		`CREATE TABLE boxes(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL)`,
		`CREATE TABLE box_virtual_ledger(id TEXT PRIMARY KEY, box_id TEXT NOT NULL, source_transaction_id TEXT)`,
		`CREATE TABLE transactions(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, effect_mode TEXT NOT NULL DEFAULT 'STANDARD')`,
		`CREATE TABLE investment_purchases(id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL)`,
		`CREATE TABLE investment_purchase_funding(workspace_id TEXT NOT NULL, transaction_id TEXT NOT NULL)`,
		`CREATE TABLE investment_cancellations(workspace_id TEXT NOT NULL, original_transaction_id TEXT, compensation_transaction_id TEXT)`,
		`CREATE TABLE investment_purchase_reversals(workspace_id TEXT NOT NULL, original_transaction_id TEXT, compensation_transaction_id TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func addReserveRelevantLink(t *testing.T, db *sql.DB, suffix, transactionID string) {
	t.Helper()
	mustExecOK(t, db, `INSERT INTO boxes(id,workspace_id) VALUES(?, 'ws-1')`, "box-"+suffix)
	mustExecOK(t, db, `INSERT INTO box_virtual_ledger(id,box_id,source_transaction_id) VALUES(?,?,?)`, "ledger-"+suffix, "box-"+suffix, transactionID)
}

func TestReservePhysicalRemovalRelevantLinkBranches(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
		want  int64
	}{
		{"none", nil, 0},
		{"ordinary-expense", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('ordinary','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "ordinary", "ordinary")
		}, 0},
		{"purchase-row", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO investment_purchases(id,workspace_id) VALUES('p1','ws-1')`)
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('investment-purchase:p1','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "purchase-row", "investment-purchase:p1")
		}, 1},
		{"funding", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO investment_purchase_funding(workspace_id,transaction_id) VALUES('ws-1','manual-funding')`)
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('manual-funding','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "funding", "manual-funding")
		}, 1},
		{"cancellation-original", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO investment_cancellations(workspace_id,original_transaction_id,compensation_transaction_id) VALUES('ws-1','cancel-original','')`)
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('cancel-original','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "cancel-original", "cancel-original")
		}, 1},
		{"cancellation-compensation", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO investment_cancellations(workspace_id,original_transaction_id,compensation_transaction_id) VALUES('ws-1','','cancel-compensation')`)
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('cancel-compensation','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "cancel-compensation", "cancel-compensation")
		}, 1},
		{"reversal-original", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO investment_purchase_reversals(workspace_id,original_transaction_id,compensation_transaction_id) VALUES('ws-1','reversal-original','')`)
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('reversal-original','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "reversal-original", "reversal-original")
		}, 1},
		{"reversal-compensation", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO investment_purchase_reversals(workspace_id,original_transaction_id,compensation_transaction_id) VALUES('ws-1','','reversal-compensation')`)
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('reversal-compensation','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "reversal-compensation", "reversal-compensation")
		}, 1},
		{"purchase-prefix", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('investment-purchase:orphan-prefix','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "purchase-prefix", "investment-purchase:orphan-prefix")
		}, 1},
		{"cancellation-prefix", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('investment-cancellation:orphan-prefix','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "cancellation-prefix", "investment-cancellation:orphan-prefix")
		}, 1},
		{"reversal-prefix", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('investment-reversal:orphan-prefix','ws-1','STANDARD')`)
			addReserveRelevantLink(t, db, "reversal-prefix", "investment-reversal:orphan-prefix")
		}, 1},
		{"classification-only", func(t *testing.T, db *sql.DB) {
			mustExecOK(t, db, `INSERT INTO transactions(id,workspace_id,effect_mode) VALUES('classification-only','ws-1','CLASSIFICATION_ONLY')`)
			addReserveRelevantLink(t, db, "classification-only", "classification-only")
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newReserveRemovalRelevantDB(t)
			if tc.setup != nil {
				tc.setup(t, db)
			}
			got, err := countReserveRemovalRelevantLinks(db)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("relevant links = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReservePhysicalRemovalDeletesOnlyReserveNotificationsAndDismissals(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	for _, statement := range []string{
		`INSERT INTO user_notifications(id,user_id,workspace_id,type,dedupe_key) VALUES
			('reserve-notification','reserve-user','ws-a','caixinha.aporte','caixinha.aporte:1'),
			('common-notification','reserve-user','ws-a','scheduled_payment','scheduled:1')`,
		`INSERT INTO user_notification_dismissals(user_id,workspace_id,notification_key) VALUES
			('reserve-user','ws-a','box_goal:box-1'),
			('reserve-user','ws-a','caixinha_notif:reserve-notification'),
			('reserve-user','ws-a','caixinha.legacy'),
			('reserve-user','ws-a','limit:limit-1'),
			('reserve-user','ws-a','scheduled:1')`,
	} {
		mustExecOK(t, db, statement)
	}

	if err := runReserveRemovalForTest(t, db); err != nil {
		t.Fatalf("migration with reserve notifications: %v", err)
	}
	if got := countQueryRows(t, db, `SELECT COUNT(1) FROM user_notifications WHERE id='reserve-notification'`); got != 0 {
		t.Fatal("reserve notification survived")
	}
	if got := countQueryRows(t, db, `SELECT COUNT(1) FROM user_notifications WHERE id='common-notification'`); got != 1 {
		t.Fatal("common notification was removed")
	}
	if got := countQueryRows(t, db, `SELECT COUNT(1) FROM user_notification_dismissals WHERE notification_key IN ('box_goal:box-1','caixinha_notif:reserve-notification','caixinha.legacy')`); got != 0 {
		t.Fatal("reserve notification dismissal survived")
	}
	if got := countQueryRows(t, db, `SELECT COUNT(1) FROM user_notification_dismissals WHERE notification_key IN ('limit:limit-1','scheduled:1')`); got != 2 {
		t.Fatal("common notification dismissal was removed")
	}
}

func TestReservePhysicalRemovalBlocksUnexpectedSharedTrigger(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	mustExecOK(t, db, `CREATE TRIGGER trg_shared_reserve AFTER UPDATE ON accounts
		WHEN EXISTS(SELECT 1 FROM boxes) BEGIN SELECT 1; END`)

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "unexpected schema object") {
		t.Fatalf("shared trigger error = %v", err)
	}
	if exists, err := reserveTableExists(db, "boxes"); err != nil || !exists {
		t.Fatalf("blocked migration removed boxes: exists=%v err=%v", exists, err)
	}
}

func TestReservePhysicalRemovalBlocksUnexpectedView(t *testing.T) {
	db := reserveRemovalSchema82(t)
	defer db.Close()
	mustExecOK(t, db, `CREATE VIEW reserve_boxes_view AS SELECT id, workspace_id FROM boxes`)

	err := runReserveRemovalForTest(t, db)
	if err == nil || !strings.Contains(err.Error(), "unexpected schema object") {
		t.Fatalf("view error = %v", err)
	}
}

func TestPreMigrationBackupManifestAndValidation(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	dbPath := filepath.Join(t.TempDir(), "schema82-backup-source.db")
	source := reserveRemovalSchema82(t)
	reserveRemovalExpense(t, source, "backup-manifest-expense", "ws-a", "acc-a", 777)
	mustExecOK(t, source, `VACUUM INTO ?`, dbPath)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", normalizeSQLiteDSN("file:"+dbPath))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,description,status)
		VALUES('wal-expense','ws-a','reserve-user','acc-a','EXPENSE',888,1,'wal fixture','paid')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		db.Close()
		t.Fatalf("WAL fixture missing: %v", err)
	}
	backupPath, err := createPreMigrationBackup(db, dbPath)
	if err != nil {
		db.Close()
		t.Fatalf("create pre-migration backup: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(preMigrationBackupManifestPath(backupPath)); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if err := ValidatePreMigrationBackup(backupPath); err != nil {
		t.Fatalf("validate pre-migration backup: %v", err)
	}

	backup, err := sql.Open("sqlite", sqliteBackupURI(backupPath, "mode=ro&immutable=1"))
	if err != nil {
		t.Fatal(err)
	}
	backup.SetMaxOpenConns(1)
	if got := countQueryRows(t, backup, `SELECT COUNT(1) FROM transactions WHERE id='wal-expense'`); got != 1 {
		backup.Close()
		t.Fatal("pre-migration backup lost committed WAL data")
	}
	backup.Close()

	file, err := os.OpenFile(backupPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tamper")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePreMigrationBackup(backupPath); err == nil {
		t.Fatal("tampered pre-migration backup passed validation")
	}
}

func TestReservePhysicalRemovalInvariantsPinned(t *testing.T) {
	if reservePhysicalRemovalSourceVersion != 82 {
		t.Fatalf("source schema = %d, want 82", reservePhysicalRemovalSourceVersion)
	}
	if reservePhysicalRemovalVersion != 83 {
		t.Fatalf("removal schema = %d, want 83", reservePhysicalRemovalVersion)
	}
	if hasReservePhysicalRemoval(82) {
		t.Fatal("schema 82 must not be treated as post-removal")
	}
	if !hasReservePhysicalRemoval(83) || !hasReservePhysicalRemoval(84) {
		t.Fatal("schemas 83 and later must require reserve absence")
	}
}
