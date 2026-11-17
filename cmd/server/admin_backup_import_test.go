package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	contabaseDB "github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

func TestAdminRestoreOnlineSnapshotPreservesCommittedBusinessState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	sourcePath := filepath.Join(root, "source.db")
	source := createRestoreTestDB(t, sourcePath, "snapshot")
	defer source.Close()
	if _, err := source.Exec(`
		PRAGMA wal_autocheckpoint=0;
		CREATE TABLE financial_state(a INTEGER NOT NULL, b INTEGER NOT NULL, counter INTEGER NOT NULL);
		CREATE TABLE financial_history(seq INTEGER PRIMARY KEY, amount INTEGER NOT NULL);
		INSERT INTO financial_state VALUES(1000,1000,0);
		BEGIN IMMEDIATE;
		UPDATE financial_state SET a=a-50,b=b+50,counter=counter+1;
		INSERT INTO financial_history VALUES(1,50);
		COMMIT;
	`); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "new-backup.db")
	if err := contabaseDB.CreateSQLiteBackupFileContext(context.Background(), source, sourcePath, candidate); err != nil {
		t.Fatal(err)
	}
	// Later source changes must not change the captured generation.
	if _, err := source.Exec(`BEGIN IMMEDIATE;
		UPDATE financial_state SET a=a-50,b=b+50,counter=counter+1;
		INSERT INTO financial_history VALUES(2,50); COMMIT;`); err != nil {
		t.Fatal(err)
	}
	livePath := filepath.Join(root, "restored.db")
	current := createRestoreTestServerRuntime(t, livePath, "before-restore")
	_, refreshed, err := restoreAdminBackupFromFile(current, "file:"+livePath, livePath, candidate, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer refreshed.Close()
	assertRestoreMarker(t, livePath, "snapshot")
	if err := validateSQLiteIntegrity(refreshed.DBs.DB); err != nil {
		t.Fatal(err)
	}
	var a, b, counter, count, sum int
	err = refreshed.DBs.DB.QueryRow(`SELECT a,b,counter,(SELECT count(*) FROM financial_history),
		(SELECT sum(amount) FROM financial_history) FROM financial_state`).Scan(&a, &b, &counter, &count, &sum)
	if err != nil || a != 950 || b != 1050 || counter != 1 || count != 1 || sum != 50 {
		t.Fatalf("restored impossible/unexpected snapshot: %d %d %d %d %d err=%v", a, b, counter, count, sum, err)
	}
}

func TestAdminImportBackupValidCandidate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "custom-contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	result, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime)
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer refreshed.Close()

	if result.PreRestoreBackupPath == "" {
		t.Fatalf("expected pre-restore backup path")
	}
	assertRestoreMarker(t, dbPath, "candidate")
	assertRestoredAuthStateInvalidated(t, refreshed.DBs.DB)
	assertRestoreMarker(t, result.PreRestoreBackupPath, "current")
}

func TestAdminImportBackupRefreshesExistingAuthServiceDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime-contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	authService := auth.NewService(current.DBs.DB)
	coordinator := newServerRuntimeCoordinator(current, func(runtime *serverRuntime) {
		authService.DB = runtime.DBs.DB
	})
	err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		_, next, restoreErr := restoreAdminBackupFromFile(old, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime)
		return next, restoreErr
	})
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer coordinator.Close()
	if authService.DB == current.DBs.DB {
		t.Fatalf("expected existing auth service to point at refreshed db")
	}
	if _, err := isDatabaseBootstrapMode(authService.DB); err != nil {
		t.Fatalf("bootstrap mode check should use live refreshed db: %v", err)
	}
	assertRestoreMarker(t, dbPath, "candidate")
}

func TestAdminImportBackupClosesBothOldPoolsBeforeOpeningReplacement(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime-contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	oldDB := current.DBs.DB
	oldImmediate := current.DBs.Immediate
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	openCalled := false
	openFn := func(dbURL string) (*contabaseDB.RuntimeDBs, error) {
		openCalled = true
		if err := oldDB.Ping(); err == nil {
			return nil, fmt.Errorf("old deferred pool remained open")
		}
		if err := oldImmediate.Ping(); err == nil {
			return nil, fmt.Errorf("old immediate pool remained open")
		}
		return openRestoreTestRuntimeDBs(dbURL)
	}

	_, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openFn, startRestoreTestServerRuntime)
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer refreshed.Close()
	if !openCalled {
		t.Fatalf("expected replacement owner to be opened")
	}
	if refreshed == nil || refreshed.DBs == nil || refreshed.DBs.DB == nil || refreshed.DBs.Immediate == nil {
		t.Fatalf("expected both replacement pools to be refreshed together")
	}
	if refreshed.DBs.DB == oldDB || refreshed.DBs.Immediate == oldImmediate {
		t.Fatalf("replacement owner retained an old pool handle")
	}
	assertRestoreMarker(t, dbPath, "candidate")
}

func TestAdminImportBackupRejectsInvalidMagicHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-sqlite.db")
	if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write invalid candidate: %v", err)
	}

	if err := validateSQLiteBackupCandidate(path); err == nil {
		t.Fatalf("expected invalid magic header to be rejected")
	}
}

func TestAdminImportBackupRejectsInvalidIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	data := append([]byte("SQLite format 3\x00"), []byte("corrupt")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write corrupt candidate: %v", err)
	}

	if err := validateSQLiteBackupCandidate(path); err == nil {
		t.Fatalf("expected corrupt sqlite candidate to be rejected")
	}
}

func TestAdminImportBackupRejectsInvalidSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-schema.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open invalid schema db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (id TEXT)`); err != nil {
		_ = db.Close()
		t.Fatalf("create unrelated table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close invalid schema db: %v", err)
	}

	if err := validateSQLiteBackupCandidate(path); err == nil {
		t.Fatalf("expected invalid schema candidate to be rejected")
	}
}

func TestAdminImportBackupUsesCustomDatabasePath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "tenant-db.db")
	hardcodedDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(hardcodedDir, 0o755); err != nil {
		t.Fatalf("create hardcoded dir: %v", err)
	}
	hardcodedPath := filepath.Join(hardcodedDir, "contabase.db")
	hardcodedDB := createRestoreTestDB(t, hardcodedPath, "hardcoded")
	hardcodedDB.Close()

	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	_, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime)
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer refreshed.Close()

	assertRestoreMarker(t, dbPath, "candidate")
	assertRestoredAuthStateInvalidated(t, refreshed.DBs.DB)
	assertRestoreMarker(t, hardcodedPath, "hardcoded")
}

func TestAdminImportBackupLegacyCandidateRunsMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	t.Setenv("DATA_DIR", dir)
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "legacy.db")
	createLegacyBootstrapRestoreCandidate(t, candidatePath)

	_, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, contabaseDB.OpenRuntimeDBs, startRestoreTestServerRuntime)
	if err != nil {
		t.Fatalf("restore legacy admin backup: %v", err)
	}
	defer refreshed.Close()

	assertColumnExistsInBootstrapRestore(t, refreshed.DBs.DB, "users", "status")
	assertColumnExistsInBootstrapRestore(t, refreshed.DBs.DB, "workspaces", "type")
	assertColumnExistsInBootstrapRestore(t, refreshed.DBs.DB, "accounts", "icon")
	assertTableExistsInBootstrapRestore(t, refreshed.DBs.DB, "schema_migrations")
	assertLegacyAccountMigratedInRestore(t, refreshed.DBs.DB)
}

func TestAdminImportBackupRollsBackWhenReopenFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rollback-contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	calls := 0
	openFn := func(dbURL string) (*contabaseDB.RuntimeDBs, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("forced reopen failure")
		}
		return openRestoreTestRuntimeDBs(dbURL)
	}

	result, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openFn, startRestoreTestServerRuntime)
	if err == nil {
		t.Fatalf("expected restore failure")
	}
	if !result.RolledBack {
		t.Fatalf("expected rollback to be reported")
	}
	if refreshed == nil {
		t.Fatalf("expected rollback database to be refreshed")
	}
	defer refreshed.Close()
	assertRestoreMarker(t, dbPath, "current")
}

func TestAdminImportBackupRemovesStaleWALAndSHM(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wal-shm-contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()
	if err := os.WriteFile(dbPath+"-wal", []byte("stale wal"), 0o600); err != nil {
		t.Fatalf("write stale wal: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("stale shm"), 0o600); err != nil {
		t.Fatalf("write stale shm: %v", err)
	}

	_, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBsNoWAL, startRestoreTestServerRuntime)
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer refreshed.Close()

	assertSidecarDoesNotContain(t, dbPath+"-wal", "stale wal")
	assertSidecarDoesNotContain(t, dbPath+"-shm", "stale shm")
}

func TestAdminImportBackupValidationFailureDoesNotCreatePreRestoreBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	defer current.Close()
	candidatePath := filepath.Join(dir, "invalid.db")
	if err := os.WriteFile(candidatePath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write invalid candidate: %v", err)
	}

	_, returned, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime)
	if err == nil {
		t.Fatalf("expected restore to reject invalid candidate")
	}
	if returned != current {
		t.Fatalf("invalid candidate must preserve the current runtime")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "backups", "pre-restore")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no pre-restore backup for invalid candidate, stat err=%v", statErr)
	}
	assertRestoreMarker(t, dbPath, "current")
}

func TestAdminImportBackupCandidateCleanupIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	createRestoreTestDB(t, dbPath, "current").Close()
	path, cleanup, err := saveAdminBackupImportUpload(dbPath, "candidate.db", strings.NewReader("candidate"))
	if err != nil {
		t.Fatalf("stage candidate: %v", err)
	}
	cleanup()
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("staged candidate remained after cleanup: %v", err)
	}
}

func TestAdminImportBackupCandidateAndRollbackFailureReturnsNoRuntime(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()
	openFn := func(string) (*contabaseDB.RuntimeDBs, error) {
		return nil, errors.New("forced candidate and rollback open failure")
	}
	result, runtime, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, openFn, startRestoreTestServerRuntime)
	if err == nil || runtime != nil || result.RolledBack || result.RollbackState != adminRollbackFailed || result.RuntimeState != adminRuntimeUnknown {
		t.Fatalf("double failure must be fail-closed: runtime=%v rolled_back=%v err=%v", runtime, result.RolledBack, err)
	}
}

func TestAdminImportBackupCanceledBeforeCommitPreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "cancel-before-commit.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	defer current.Close()
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	ctx, cancel := context.WithCancel(context.Background())
	validator := func(string) error {
		cancel()
		return nil
	}
	result, returned, err := restoreSQLiteBackupFromFile(ctx, current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validator)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error = %v, want context.Canceled", err)
	}
	if returned != current {
		t.Fatal("canceled restore must preserve the current runtime")
	}
	if result.RolledBack {
		t.Fatal("canceled restore before commit must not report rollback")
	}
	assertRestoreMarker(t, dbPath, "current")
	if _, statErr := os.Stat(filepath.Join(dir, "backups", "pre-restore")); !os.IsNotExist(statErr) {
		t.Fatalf("canceled restore created a pre-restore backup: %v", statErr)
	}
}

func TestAdminImportBackupCompletesAfterCommitWhenClientCancels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "cancel-after-commit.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	ctx, cancel := context.WithCancel(context.Background())
	startFn := func(dbs *contabaseDB.RuntimeDBs) (*serverRuntime, error) {
		cancel()
		return startRestoreTestServerRuntime(dbs)
	}
	result, refreshed, err := restoreSQLiteBackupFromFile(ctx, current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startFn, validateSQLiteBackupCandidate)
	if err != nil {
		t.Fatalf("restore after commitment: %v", err)
	}
	if refreshed == nil {
		t.Fatal("restore after commitment returned no runtime")
	}
	defer refreshed.Close()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test did not cancel the request after commitment")
	}
	if result.RolledBack {
		t.Fatal("successful restore reported rollback")
	}
	assertRestoreMarker(t, dbPath, "candidate")
	if err := validateSQLiteIntegrity(refreshed.DBs.DB); err != nil {
		t.Fatalf("restored integrity: %v", err)
	}
	if err := validateSQLiteForeignKeys(refreshed.DBs.DB); err != nil {
		t.Fatalf("restored foreign keys: %v", err)
	}
}

func TestAdminImportBackupValidSyntheticBeta7Data(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "beta7-contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "beta7-candidate.db")
	candidate := createRestoreTestDB(t, candidatePath, "candidate")
	statements := []string{
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('b1-checking', 'restore-workspace', 'Conta Sintetica', 'CHECKING', 100000, 100000, unixepoch(), unixepoch())`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('b1-card', 'restore-workspace', 'Cartao Sintetico', 'CREDIT_CARD', 0, 0, unixepoch(), unixepoch())`,
		`INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('b1-cc', 'b1-card', 20, 10, 500000)`,
		`INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('b1-inv', 'b1-card', '2026-10', unixepoch(), unixepoch() + 86400, 'OPEN', unixepoch())`,
		`INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, created_at, updated_at) VALUES ('b1-tx', 'restore-workspace', 'restore-user', 'b1-card', 'b1-inv', 'EXPENSE', 12345, unixepoch(), 'Compra sintetica', 'paid', unixepoch(), unixepoch())`,
		`INSERT INTO boxes (id, workspace_id, name, target_amount, monthly_recharge, created_at, updated_at) VALUES ('b1-box', 'restore-workspace', 'Reserva Sintetica', 500000, 0, unixepoch(), unixepoch())`,
		`INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at) VALUES ('b1-lv', 'b1-box', 50000, 'RECHARGE', 'Aporte sintetico', unixepoch(), unixepoch())`,
	}
	for _, statement := range statements {
		if _, err := candidate.Exec(statement); err != nil {
			_ = candidate.Close()
			t.Fatalf("seed synthetic Beta 7 data: %v", err)
		}
	}
	if err := candidate.Close(); err != nil {
		t.Fatalf("close synthetic candidate: %v", err)
	}

	result, refreshed, err := restoreAdminBackupFromFile(current, "file:"+dbPath, dbPath, candidatePath, contabaseDB.OpenRuntimeDBs, func(dbs *contabaseDB.RuntimeDBs) (*serverRuntime, error) {
		return startServerRuntime(dbs, serverRuntimeOptions{})
	})
	if err != nil {
		t.Fatalf("restore synthetic Beta 7 backup: %v", err)
	}
	if refreshed == nil {
		t.Fatal("restore returned no runtime")
	}
	defer refreshed.Close()
	if result.RolledBack {
		t.Fatal("valid synthetic backup reported rollback")
	}
	assertRestoreMarker(t, dbPath, "candidate")
	if err := validateSQLiteIntegrity(refreshed.DBs.DB); err != nil {
		t.Fatalf("restored integrity: %v", err)
	}
	if err := validateSQLiteForeignKeys(refreshed.DBs.DB); err != nil {
		t.Fatalf("restored foreign keys: %v", err)
	}
	var accounts, cards, invoices, transactions, reserveObjects int
	if err := refreshed.DBs.DB.QueryRow(`SELECT (SELECT count(*) FROM accounts), (SELECT count(*) FROM credit_cards), (SELECT count(*) FROM invoices), (SELECT count(*) FROM transactions), (SELECT count(*) FROM sqlite_schema WHERE name IN ('boxes','box_virtual_ledger','box_contribution_lifecycle_events'))`).Scan(&accounts, &cards, &invoices, &transactions, &reserveObjects); err != nil {
		t.Fatalf("count restored synthetic data: %v", err)
	}
	if accounts < 2 || cards != 1 || invoices != 1 || transactions != 1 || reserveObjects != 0 {
		t.Fatalf("restored synthetic counts accounts=%d cards=%d invoices=%d transactions=%d reserveObjects=%d", accounts, cards, invoices, transactions, reserveObjects)
	}
	var journalMode string
	if err := refreshed.DBs.DB.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read restored journal mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("restored journal mode = %q, want wal", journalMode)
	}
	var workspaceID string
	var amount, balance int64
	if err := refreshed.DBs.DB.QueryRow(`SELECT workspace_id, amount,
		(SELECT current_balance FROM accounts WHERE id='b1-checking')
		FROM transactions WHERE id='b1-tx'`).Scan(&workspaceID, &amount, &balance); err != nil {
		t.Fatal(err)
	}
	if workspaceID != "restore-workspace" || amount != 12345 || balance != 100000 {
		t.Fatalf("financial/workspace invariant changed: workspace=%s amount=%d balance=%d", workspaceID, amount, balance)
	}
	assertRestoredAuthStateInvalidated(t, refreshed.DBs.DB)
}

func TestAdminBackupImportFailurePublishesSpecificAlert(t *testing.T) {
	rr := httptest.NewRecorder()
	writeAdminBackupImportFailure(rr, adminBackupImportBusyMessage, http.StatusConflict)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusConflict)
	}
	trigger := rr.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "mostrarAlerta") || !strings.Contains(trigger, "sistema está concluindo") {
		t.Fatalf("expected specific alert trigger, got %q", trigger)
	}
	if !strings.Contains(rr.Body.String(), `id="settings-dynamic-payload"`) {
		t.Fatalf("expected settings target in error body, got %q", rr.Body.String())
	}
}

func TestAdminBackupImportForbiddenPublishesSpecificAlert(t *testing.T) {
	rr := httptest.NewRecorder()
	writeAdminBackupImportForbidden(rr, httptest.NewRequest(http.MethodPost, "/admin/backups/importar", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if trigger := rr.Header().Get("HX-Trigger"); !strings.Contains(trigger, "mostrarAlerta") || !strings.Contains(trigger, "permissão") {
		t.Fatalf("expected specific forbidden trigger, got %q", trigger)
	}
}

func TestAdminBackupImportRateLimitResponseIsFriendly(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	tpl, err := newAppTemplateEngine(false, buildFuncMap())
	if err != nil {
		t.Fatalf("load templates: %v", err)
	}
	h := handlers.ConfiguracoesHandler{
		DB:                  db,
		Templates:           tpl,
		AutomaticBackupsDir: t.TempDir(),
		WorkspaceID:         "workspace-rbac",
		UserID:              "admin-user",
		ActorRole:           models.RoleAdmin,
		SessionToken:        "session-token",
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/backups/importar", nil)

	writeAdminBackupImportRateLimitExceeded(rr, req, h, rateLimitDecision{RetryAfter: 125 * time.Second})

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusTooManyRequests)
	}
	assertHeader(t, rr, "Retry-After", "125")
	assertHeader(t, rr, "Cache-Control", "no-store")
	trigger := rr.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "mostrarAlerta") || !strings.Contains(trigger, "3 minutos") {
		t.Fatalf("expected HX-Trigger with friendly alert, got %q", trigger)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="settings-dynamic-payload"`) {
		t.Fatalf("expected settings fragment body, got %q", body)
	}
	if !strings.Contains(body, "Muitas tentativas de importação. Aguarde 3 minutos e tente novamente.") {
		t.Fatalf("expected friendly rate-limit copy in body, got %q", body)
	}
}

func TestAdminBackupImportSuccessRedirectsWithoutRenderingSettings(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/backups/importar", nil)
	req.Header.Set("HX-Request", "true")

	writeAdminBackupImportSuccess(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	assertHeader(t, rr, "HX-Redirect", "/login")
	assertHeader(t, rr, "Cache-Control", "no-store")
	body := rr.Body.String()
	if strings.Contains(body, `id="settings-dynamic-payload"`) {
		t.Fatalf("success response must not render settings partial, got %q", body)
	}
	if !strings.Contains(body, "Backup importado com sucesso") {
		t.Fatalf("expected simple success body, got %q", body)
	}
}

func TestAdminBackupImportSuccessRedirectsNonHTMX(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/backups/importar", nil)

	writeAdminBackupImportSuccess(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	assertHeader(t, rr, "Location", "/login")
	assertHeader(t, rr, "Cache-Control", "no-store")
}

func TestAdminBackupImportFormPreventsDuplicateSubmits(t *testing.T) {
	body, err := os.ReadFile("../../templates/pages/configuracoes_admin_backups.html")
	if err != nil {
		t.Fatalf("read backup template: %v", err)
	}
	markup := string(body)
	for _, want := range []string{
		`hx-post="/admin/backups/importar"`,
		`hx-encoding="multipart/form-data"`,
		`hx-sync="this:drop"`,
		`hx-disabled-elt="find button[type='submit']"`,
		`hx-indicator="#htmx-indicator"`,
		`type="submit"`,
		`type="file"`,
		`name="backup_file"`,
		`name="manifest_file"`,
		`name="legacy_restore"`,
		`required`,
		`Importando...`,
		`accept=".db"`,
		"data-backup-dropzone",
		"data-backup-import-form",
		`data-backup-import-state role="status"`,
		`data-backup-import-status tabindex="-1"`,
		"data-backup-import-status-icon",
		"data-backup-import-status-text",
		"data-backup-import-dismiss",
		`Dispensar`,
		`data-lucide="download"`,
		`data-lucide="upload"`,
		`ui-btn admin-danger-btn`,
		`Ação destrutiva`,
		`admin-restore-warning`,
		`DATA_DIR/backups/`,
	} {
		if !strings.Contains(markup, want) {
			t.Fatalf("expected backup import form markup to contain %q", want)
		}
	}
	if strings.Contains(markup, `method="post"`) {
		t.Fatalf("backup import form should not mix native POST with hx-post")
	}
	if strings.Contains(markup, `class="cb-card-muted border-[var(--cb-danger)]/20`) {
		t.Fatalf("backup import form must keep danger styling limited to warning copy")
	}
}

func createRestoreTestDB(t *testing.T, dbPath, marker string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open restore test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		t.Fatalf("enable wal: %v", err)
	}
	frozen, err := os.ReadFile("../../internal/database/testdata/investment_schema77.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(frozen)); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`CREATE TABLE restore_marker (value TEXT NOT NULL)`,
		`INSERT INTO users(id,name,email,password_hash) VALUES('restore-user','Synthetic','restore@example.test','hash')`,
		`INSERT INTO workspaces(id,name) VALUES('restore-workspace','Synthetic')`,
		`INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('restore-workspace','restore-user','ADMIN')`,
		`INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, is_remember, created_at) VALUES ('session-1', 'restore-user', 'restore-workspace', 'restore-session-hash', unixepoch() + 3600, 1, unixepoch())`,
		`INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at, remember_me, created_at) VALUES ('preauth-1', 'restore-user', 'restore-preauth-hash', 'TOTP', unixepoch() + 300, 1, unixepoch())`,
		`INSERT INTO restore_marker (value) VALUES (?)`,
	}
	for i, stmt := range stmts {
		var err error
		if i == len(stmts)-1 {
			_, err = db.Exec(stmt, marker)
		} else {
			_, err = db.Exec(stmt)
		}
		if err != nil {
			_ = db.Close()
			t.Fatalf("seed restore test db: %v", err)
		}
	}
	return db
}

func createRestoreTestRuntimeDBs(t *testing.T, dbPath, marker string) *contabaseDB.RuntimeDBs {
	t.Helper()
	seedDB := createRestoreTestDB(t, dbPath, marker)
	if err := seedDB.Close(); err != nil {
		t.Fatalf("close seeded restore test db: %v", err)
	}
	dbs, err := openRestoreTestRuntimeDBs("file:" + dbPath)
	if err != nil {
		t.Fatalf("open restore test runtime dbs: %v", err)
	}
	return dbs
}

func createRestoreTestServerRuntime(t *testing.T, dbPath, marker string) *serverRuntime {
	t.Helper()
	return &serverRuntime{DBs: createRestoreTestRuntimeDBs(t, dbPath, marker)}
}

func startRestoreTestServerRuntime(dbs *contabaseDB.RuntimeDBs) (*serverRuntime, error) {
	return &serverRuntime{DBs: dbs}, nil
}

func openRestoreTestRuntimeDBs(dbURL string) (*contabaseDB.RuntimeDBs, error) {
	return openRestoreTestRuntimeDBsWithJournal(dbURL, true)
}

func openRestoreTestRuntimeDBsNoWAL(dbURL string) (*contabaseDB.RuntimeDBs, error) {
	return openRestoreTestRuntimeDBsWithJournal(dbURL, false)
}

func openRestoreTestRuntimeDBsWithJournal(dbURL string, enableWAL bool) (*contabaseDB.RuntimeDBs, error) {
	normalURL := restoreTestDSNWithTxLock(dbURL, "deferred")
	immediateURL := restoreTestDSNWithTxLock(dbURL, "immediate")

	var normal *sql.DB
	var err error
	if enableWAL {
		normal, err = openRestoreTestDB(normalURL)
	} else {
		normal, err = openRestoreTestDBNoWAL(normalURL)
	}
	if err != nil {
		return nil, err
	}

	immediate, err := sql.Open("sqlite", immediateURL)
	if err != nil {
		_ = normal.Close()
		return nil, err
	}
	immediate.SetMaxOpenConns(1)
	if err := immediate.Ping(); err != nil {
		_ = immediate.Close()
		_ = normal.Close()
		return nil, err
	}
	return &contabaseDB.RuntimeDBs{DB: normal, Immediate: immediate}, nil
}

func restoreTestDSNWithTxLock(dbURL, mode string) string {
	separator := "?"
	if strings.Contains(dbURL, "?") {
		separator = "&"
	}
	return dbURL + separator + "_txlock=" + mode
}

func openRestoreTestDB(dbURL string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openRestoreTestDBNoWAL(dbURL string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func assertRestoreMarker(t *testing.T, dbPath, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open marker db: %v", err)
	}
	defer db.Close()
	var got string
	if err := db.QueryRow(`SELECT value FROM restore_marker LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("query restore marker: %v", err)
	}
	if got != want {
		t.Fatalf("restore marker = %q, want %q", got, want)
	}
}

func assertRestoredAuthStateInvalidated(t *testing.T, db *sql.DB) {
	t.Helper()
	var activeSessions int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sessions WHERE revoked_at IS NULL`).Scan(&activeSessions); err != nil {
		t.Fatalf("count active restored sessions: %v", err)
	}
	if activeSessions != 0 {
		t.Fatalf("expected restored sessions revoked, got %d active", activeSessions)
	}
	var activePreAuth int
	if err := db.QueryRow(`SELECT COUNT(1) FROM pre_auth_sessions WHERE consumed_at IS NULL`).Scan(&activePreAuth); err != nil {
		t.Fatalf("count active restored pre-auth sessions: %v", err)
	}
	if activePreAuth != 0 {
		t.Fatalf("expected restored pre-auth consumed, got %d active", activePreAuth)
	}
}

func assertSidecarDoesNotContain(t *testing.T, path, stale string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read sidecar %s: %v", filepath.Base(path), err)
	}
	if string(data) == stale {
		t.Fatalf("sidecar %s still contains stale content", filepath.Base(path))
	}
}

func TestAdminBackupImportShellLoadsFeedbackScripts(t *testing.T) {
	tpl := template.Must(template.New("backup-shell").Funcs(buildFuncMap()).ParseFiles("../../templates/components/app_shell.html"))
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "app-v2-document-end", nil); err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"/assets/js/backup-dropzone.js", "/assets/js/backup-import-feedback.js"} {
		if strings.Count(out.String(), `src="`+asset+`"`) != 1 {
			t.Fatalf("app shell must load %s exactly once", asset)
		}
		if _, err := os.Stat("../.." + asset); err != nil {
			t.Fatalf("script referenced by shell must exist: %s: %v", asset, err)
		}
	}
}

type compiledCSSRule struct {
	selectors []string
	decls     map[string]string
}

func collectCompiledCSSRules(css string, rules *[]compiledCSSRule) {
	for {
		open := strings.Index(css, "{")
		if open < 0 {
			return
		}
		prelude := strings.TrimSpace(css[:open])
		depth := 1
		i := open + 1
		for i < len(css) && depth > 0 {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			i++
		}
		body := css[open+1 : i-1]
		quoteStripper := strings.NewReplacer(`"`, "", "'", "")
		switch {
		case strings.HasPrefix(prelude, "@") && strings.Contains(body, "{"):
			collectCompiledCSSRules(body, rules)
		case !strings.HasPrefix(prelude, "@"):
			rule := compiledCSSRule{decls: map[string]string{}}
			for _, selector := range strings.Split(prelude, ",") {
				rule.selectors = append(rule.selectors, quoteStripper.Replace(strings.TrimSpace(selector)))
			}
			for _, decl := range strings.Split(body, ";") {
				prop, value, found := strings.Cut(decl, ":")
				if !found {
					continue
				}
				rule.decls[strings.TrimSpace(prop)] = strings.TrimSpace(value)
			}
			*rules = append(*rules, rule)
		}
		css = css[i:]
	}
}

func findCompiledCSSRule(rules []compiledCSSRule, selector string) (map[string]string, bool) {
	for _, rule := range rules {
		for _, candidate := range rule.selectors {
			if candidate == selector {
				return rule.decls, true
			}
		}
	}
	return nil, false
}

func TestAdminBackupImportCompiledCSSIndicatorRules(t *testing.T) {
	data, err := os.ReadFile("../../assets/css/style.css")
	if err != nil {
		t.Fatalf("read compiled css: %v", err)
	}
	var rules []compiledCSSRule
	collectCompiledCSSRules(string(data), &rules)

	idle, ok := findCompiledCSSRule(rules, ".htmx-indicator")
	if !ok {
		t.Fatal("compiled css must style .htmx-indicator")
	}
	if idle["opacity"] != "0" {
		t.Fatalf("idle .htmx-indicator opacity = %q, want 0", idle["opacity"])
	}

	requestSelectors := []string{
		".htmx-request .htmx-indicator",
		".htmx-request.htmx-indicator",
		"[data-backup-dropzone] button[type=submit][data-disabled-by-htmx] .htmx-indicator",
	}
	for _, selector := range requestSelectors {
		decls, ok := findCompiledCSSRule(rules, selector)
		if !ok {
			t.Fatalf("compiled css must style %q", selector)
		}
		if decls["opacity"] != "1" {
			t.Fatalf("request-state %q opacity = %q, want 1", selector, decls["opacity"])
		}
		switch decls["transition"] {
		case "opacity 200ms ease-in", "opacity .2s ease-in":
			// minifier may rewrite 200ms as .2s
		default:
			t.Fatalf("request-state %q transition = %q, want opacity 200ms ease-in", selector, decls["transition"])
		}
	}
}

func TestRestoreCandidateRejectsInvalidPreMigrationManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.db")
	db, err := contabaseDB.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".manifest.json", []byte(`{"version":1,"file":"candidate.db","size":1,"sha256":"bad","schema_version":83,"created_at":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreCandidate(path, true, false); err == nil {
		t.Fatal("invalid pre-migration manifest accepted by restore validation")
	}
}

func writeRestoreManifest(t *testing.T, path, file, hash string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if file == "" {
		file = filepath.Base(path)
	}
	if hash == "" {
		hash = hex.EncodeToString(sum[:])
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var schemaVersion int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"version":        1,
		"file":           file,
		"size":           len(data),
		"sha256":         hash,
		"schema_version": schemaVersion,
		"created_at":     1,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".manifest.json", encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreCandidateManifestPolicy(t *testing.T) {
	t.Run("new backup with valid manifest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		writeRestoreManifest(t, path, "", "")
		if err := validateRestoreCandidate(path, true, false); err != nil {
			t.Fatalf("valid manifest rejected: %v", err)
		}
	})
	t.Run("new backup with missing manifest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		if err := validateRestoreCandidate(path, false, false); err == nil {
			t.Fatal("missing manifest accepted in normal restore")
		}
	})
	t.Run("new backup with invalid hash", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		writeRestoreManifest(t, path, "", strings.Repeat("0", 64))
		if err := validateRestoreCandidate(path, true, false); err == nil {
			t.Fatal("invalid manifest hash accepted")
		}
	})
	t.Run("manifest for another file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		writeRestoreManifest(t, path, "other.db", "")
		if err := validateRestoreCandidate(path, true, false); err == nil {
			t.Fatal("manifest from another file accepted")
		}
	})
	t.Run("malformed manifest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		if err := os.WriteFile(path+".manifest.json", []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateRestoreCandidate(path, true, false); err == nil {
			t.Fatal("malformed manifest accepted")
		}
	})
	t.Run("tampered sqlite backup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		writeRestoreManifest(t, path, "", "")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
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
		if err := validateRestoreCandidate(path, true, false); err == nil {
			t.Fatal("tampered backup accepted")
		}
	})
	t.Run("legacy missing manifest is explicit only", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.db")
		createRestoreTestDB(t, path, "legacy").Close()
		if err := validateRestoreCandidate(path, false, false); err == nil {
			t.Fatal("legacy backup accepted without explicit legacy mode")
		}
		if err := validateRestoreCandidate(path, false, true); err != nil {
			t.Fatalf("explicit legacy backup rejected: %v", err)
		}
	})
	t.Run("manifest and legacy are mutually exclusive", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.db")
		createRestoreTestDB(t, path, "new").Close()
		writeRestoreManifest(t, path, "", "")
		if err := validateRestoreCandidate(path, true, true); err == nil {
			t.Fatal("manifest plus legacy accepted")
		}
	})
	t.Run("renaming does not bypass manifest identity", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "original.db")
		createRestoreTestDB(t, path, "rename").Close()
		writeRestoreManifest(t, path, "", "")
		renamed := filepath.Join(dir, "renamed.db")
		if err := os.Rename(path, renamed); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path + ".manifest.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(renamed+".manifest.json", data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateRestoreCandidate(renamed, true, false); err == nil {
			t.Fatal("renamed backup bypassed manifest file identity")
		}
	})
}

func TestRestoreCandidateFailureDoesNotReplacePersistedDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "current.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	defer func() {
		if current != nil && current.DBs != nil {
			current.Close()
		}
	}()
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	validator := func(path string) error {
		return validateRestoreCandidate(path, false, false)
	}
	result, next, err := restoreSQLiteBackupFromFile(context.Background(), current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validator)
	if err == nil {
		t.Fatal("restore without manifest succeeded")
	}
	if next != current {
		t.Fatal("failed restore returned a different runtime")
	}
	if result.CommitmentReached {
		t.Fatal("failed validation reached restore commitment")
	}
	assertRestoreMarker(t, dbPath, "current")
}

func TestRestoreUploadStagingPreservesManifestFilename(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	createRestoreTestDB(t, sourcePath, "staged").Close()
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dbPath := filepath.Join(dir, "contabase.db")
	stagedPath, cleanup, err := saveAdminBackupImportUpload(dbPath, "contabase-pre-migration-test.db", file)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Base(stagedPath) != "contabase-pre-migration-test.db" {
		t.Fatalf("staged filename = %q", filepath.Base(stagedPath))
	}
	writeRestoreManifest(t, stagedPath, "", "")
	manifestData, err := os.ReadFile(stagedPath + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stagedPath + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := saveRestoreManifestUpload(stagedPath, bytes.NewReader(manifestData)); err != nil {
		t.Fatalf("save uploaded manifest: %v", err)
	}
	if err := validateRestoreCandidate(stagedPath, true, false); err != nil {
		t.Fatalf("staged backup with manifest rejected: %v", err)
	}
	if err := os.Remove(stagedPath + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreCandidate(stagedPath, false, false); err == nil {
		t.Fatal("staged backup without manifest accepted in normal restore")
	}
	if err := validateRestoreCandidate(stagedPath, false, true); err != nil {
		t.Fatalf("staged legacy backup rejected in explicit legacy mode: %v", err)
	}
}

func TestRestoreCandidateManifestAcceptedByPersistedRestore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()
	writeRestoreManifest(t, candidatePath, "", "")

	validator := func(path string) error {
		return validateRestoreCandidate(path, true, false)
	}
	result, next, err := restoreSQLiteBackupFromFile(context.Background(), current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validator)
	if err != nil {
		t.Fatalf("restore with manifest: %v", err)
	}
	if next == nil {
		t.Fatal("restore with manifest returned no runtime")
	}
	defer next.Close()
	if result.RolledBack {
		t.Fatal("valid manifest restore reported rollback")
	}
	assertRestoreMarker(t, dbPath, "candidate")
}

func TestRestoreCandidateLegacyAcceptedByPersistedRestore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	dbPath := filepath.Join(dir, "contabase.db")
	current := createRestoreTestServerRuntime(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "legacy-candidate.db")
	createRestoreTestDB(t, candidatePath, "legacy").Close()

	validator := func(path string) error {
		return validateRestoreCandidate(path, false, true)
	}
	result, next, err := restoreSQLiteBackupFromFile(context.Background(), current, "file:"+dbPath, dbPath, candidatePath, openRestoreTestRuntimeDBs, startRestoreTestServerRuntime, validator)
	if err != nil {
		t.Fatalf("explicit legacy restore: %v", err)
	}
	if next == nil {
		t.Fatal("explicit legacy restore returned no runtime")
	}
	defer next.Close()
	if result.RolledBack {
		t.Fatal("explicit legacy restore reported rollback")
	}
	assertRestoreMarker(t, dbPath, "legacy")
}
