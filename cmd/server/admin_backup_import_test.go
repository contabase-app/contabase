package main

import (
	"database/sql"
	"errors"
	"fmt"
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
	path, cleanup, err := saveAdminBackupImportUpload(dbPath, strings.NewReader("candidate"))
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
	if err == nil || runtime != nil || !result.RolledBack {
		t.Fatalf("double failure must be fail-closed: runtime=%v rolled_back=%v err=%v", runtime, result.RolledBack, err)
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
		DB:           db,
		Templates:    tpl,
		WorkspaceID:  "workspace-rbac",
		UserID:       "admin-user",
		ActorRole:    models.RoleAdmin,
		SessionToken: "session-token",
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
		`type="submit"`,
		`required`,
		`Importando...`,
		`accept=".db"`,
		"data-backup-dropzone",
		`data-lucide="download"`,
		`data-lucide="upload"`,
	} {
		if !strings.Contains(markup, want) {
			t.Fatalf("expected backup import form markup to contain %q", want)
		}
	}
	if strings.Contains(markup, `method="post"`) {
		t.Fatalf("backup import form should not mix native POST with hx-post")
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
	stmts := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY)`,
		`CREATE TABLE workspaces (id TEXT PRIMARY KEY)`,
		`CREATE TABLE workspace_members (workspace_id TEXT NOT NULL, user_id TEXT NOT NULL, role TEXT NOT NULL)`,
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, workspace_id TEXT NOT NULL, token_hash TEXT NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER, is_remember INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
		`CREATE TABLE pre_auth_sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token_hash TEXT NOT NULL, method TEXT NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER, remember_me INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
		`CREATE TABLE restore_marker (value TEXT NOT NULL)`,
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
