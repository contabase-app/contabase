package main

import (
	"database/sql"
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
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	var refreshed *sql.DB
	result, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDB, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer refreshed.Close()

	if result.PreRestoreBackupPath == "" {
		t.Fatalf("expected pre-restore backup path")
	}
	assertRestoreMarker(t, dbPath, "candidate")
	assertRestoredAuthStateInvalidated(t, refreshed)
	assertRestoreMarker(t, result.PreRestoreBackupPath, "current")
}

func TestAdminImportBackupRefreshesExistingAuthServiceDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "runtime-contabase.db")
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	authService := auth.NewService(currentDB)
	var runtimeDB *sql.DB = currentDB
	_, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDB, func(newDB *sql.DB) {
		runtimeDB = newDB
		authService.DB = newDB
	})
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer runtimeDB.Close()

	if runtimeDB == currentDB {
		t.Fatalf("expected runtime db reference to be replaced")
	}
	if authService.DB != runtimeDB {
		t.Fatalf("expected existing auth service to point at refreshed db")
	}
	if _, err := isDatabaseBootstrapMode(authService.DB); err != nil {
		t.Fatalf("bootstrap mode check should use live refreshed db: %v", err)
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

	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	var refreshed *sql.DB
	_, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDB, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore admin backup: %v", err)
	}
	defer refreshed.Close()

	assertRestoreMarker(t, dbPath, "candidate")
	assertRestoredAuthStateInvalidated(t, refreshed)
	assertRestoreMarker(t, hardcodedPath, "hardcoded")
}

func TestAdminImportBackupLegacyCandidateRunsMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "legacy.db")
	createLegacyBootstrapRestoreCandidate(t, candidatePath)

	var refreshed *sql.DB
	_, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, contabaseDB.Open, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore legacy admin backup: %v", err)
	}
	defer refreshed.Close()

	assertColumnExistsInBootstrapRestore(t, refreshed, "users", "status")
	assertColumnExistsInBootstrapRestore(t, refreshed, "workspaces", "type")
	assertColumnExistsInBootstrapRestore(t, refreshed, "accounts", "icon")
	assertTableExistsInBootstrapRestore(t, refreshed, "schema_migrations")
	assertLegacyAccountMigratedInRestore(t, refreshed)
}

func TestAdminImportBackupRollsBackWhenReopenFails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rollback-contabase.db")
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	calls := 0
	var refreshed *sql.DB
	openFn := func(dbURL string) (*sql.DB, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("forced reopen failure")
		}
		return openRestoreTestDB(dbURL)
	}

	result, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openFn, func(db *sql.DB) {
		refreshed = db
	})
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
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()
	if err := os.WriteFile(dbPath+"-wal", []byte("stale wal"), 0o600); err != nil {
		t.Fatalf("write stale wal: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("stale shm"), 0o600); err != nil {
		t.Fatalf("write stale shm: %v", err)
	}

	var refreshed *sql.DB
	_, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDBNoWAL, func(db *sql.DB) {
		refreshed = db
	})
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
	currentDB := createRestoreTestDB(t, dbPath, "current")
	defer currentDB.Close()
	candidatePath := filepath.Join(dir, "invalid.db")
	if err := os.WriteFile(candidatePath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write invalid candidate: %v", err)
	}

	_, err := restoreAdminBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDB, func(*sql.DB) {})
	if err == nil {
		t.Fatalf("expected restore to reject invalid candidate")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "backups", "pre-restore")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no pre-restore backup for invalid candidate, stat err=%v", statErr)
	}
	assertRestoreMarker(t, dbPath, "current")
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
