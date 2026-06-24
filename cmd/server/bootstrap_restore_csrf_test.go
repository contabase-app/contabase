package main

import (
	"bytes"
	"database/sql"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	contabaseDB "github.com/contabase-app/contabase/internal/database"
)

func TestSetupPageRendersCSRFForCreateAndRestoreForms(t *testing.T) {
	tpl, err := template.ParseFiles("../../templates/pages/setup.html")
	if err != nil {
		t.Fatalf("parse setup template: %v", err)
	}
	data := struct {
		Error     string
		CSRFToken string
	}{
		CSRFToken: "test-csrf-token",
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "setup-page", data); err != nil {
		t.Fatalf("execute setup template: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `action="/setup/restaurar"`) {
		t.Fatalf("expected restore form action to be rendered")
	}
	if got := strings.Count(body, `name="csrf_token" value="test-csrf-token"`); got != 2 {
		t.Fatalf("expected CSRF token in setup and restore forms, got %d occurrences", got)
	}
	for _, want := range []string{`id="setup-backup-file"`, `id="setup-file-name"`, "Arquivo selecionado:"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected setup template to expose selected backup file marker %q", want)
		}
	}
}

func TestBootstrapRestoreMultipartCSRFTokenAndBackupCandidate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "candidate.db")
	createRestoreTestDB(t, dbPath, "candidate").Close()
	signer := newBootstrapRestoreTestCSRFSigner()
	csrfToken := signer.issue(time.Now())
	req := newBootstrapRestoreMultipartRequest(t, map[string]string{
		"csrf_token":  csrfToken,
		"setup_token": testBootstrapSetupToken,
	}, "candidate.db", dbPath)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	rr := httptest.NewRecorder()

	if err := prepareBootstrapRestoreForm(rr, req, bootstrapRestoreMaxBodyBytes); err != nil {
		t.Fatalf("prepare bootstrap restore form: %v", err)
	}
	if !csrfRequestValid(signer, req) {
		t.Fatalf("expected multipart hidden CSRF token to validate after form preparation")
	}
	if !newBootstrapSetupGuard(testBootstrapSetupToken).Allow(req) {
		t.Fatalf("expected valid local setup token to be accepted")
	}
	candidatePath := uploadedRestoreCandidatePath(t, req)
	if err := validateSQLiteBackupCandidate(candidatePath); err != nil {
		t.Fatalf("expected valid SQLite backup candidate: %v", err)
	}
}

func TestBootstrapRestoreAcceptsCSRFHeaderForHTMXStyleRequest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "candidate.db")
	createRestoreTestDB(t, dbPath, "candidate").Close()
	signer := newBootstrapRestoreTestCSRFSigner()
	csrfToken := signer.issue(time.Now())
	req := newBootstrapRestoreMultipartRequest(t, map[string]string{
		"setup_token": testBootstrapSetupToken,
	}, "candidate.db", dbPath)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	req.Header.Set(csrfHeaderName, csrfToken)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()

	if err := prepareBootstrapRestoreForm(rr, req, bootstrapRestoreMaxBodyBytes); err != nil {
		t.Fatalf("prepare bootstrap restore form: %v", err)
	}
	if !csrfRequestValid(signer, req) {
		t.Fatalf("expected CSRF header to validate on multipart restore request")
	}
	if !newBootstrapSetupGuard(testBootstrapSetupToken).Allow(req) {
		t.Fatalf("expected setup token to validate")
	}
}

func TestBootstrapRestoreMissingCSRFShowsReloadGuidance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "candidate.db")
	createRestoreTestDB(t, dbPath, "candidate").Close()
	req := newBootstrapRestoreMultipartRequest(t, map[string]string{
		"setup_token": testBootstrapSetupToken,
	}, "candidate.db", dbPath)
	signer := newBootstrapRestoreTestCSRFSigner()
	rr := httptest.NewRecorder()

	if err := prepareBootstrapRestoreForm(rr, req, bootstrapRestoreMaxBodyBytes); err != nil {
		t.Fatalf("prepare bootstrap restore form: %v", err)
	}
	if csrfRequestValid(signer, req) {
		t.Fatalf("expected missing CSRF token to be rejected")
	}
	handleSetupCSRFError(rr, setupGuardTemplateEngine{}, "fresh-csrf")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Recarregue /setup") {
		t.Fatalf("expected reload guidance, got %q", body)
	}
	if strings.Contains(body, testBootstrapSetupToken) {
		t.Fatalf("setup token leaked in CSRF error body")
	}
}

func TestBootstrapRestoreInvalidSetupTokenStaysGeneric(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "candidate.db")
	createRestoreTestDB(t, dbPath, "candidate").Close()
	signer := newBootstrapRestoreTestCSRFSigner()
	csrfToken := signer.issue(time.Now())
	req := newBootstrapRestoreMultipartRequest(t, map[string]string{
		"csrf_token":  csrfToken,
		"setup_token": "wrong-token-value",
	}, "candidate.db", dbPath)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	rr := httptest.NewRecorder()

	if err := prepareBootstrapRestoreForm(rr, req, bootstrapRestoreMaxBodyBytes); err != nil {
		t.Fatalf("prepare bootstrap restore form: %v", err)
	}
	if !csrfRequestValid(signer, req) {
		t.Fatalf("expected CSRF to validate before setup token check")
	}
	if requireBootstrapSetupToken(rr, req, setupGuardTemplateEngine{}, csrfToken, newBootstrapSetupGuard(testBootstrapSetupToken)) {
		t.Fatalf("expected invalid setup token to be rejected")
	}

	body := rr.Body.String()
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}
	if !strings.Contains(body, "Token local de setup inválido ou ausente.") {
		t.Fatalf("expected generic token error, got %q", body)
	}
	for _, forbidden := range []string{testBootstrapSetupToken, "wrong-token-value"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("setup token value leaked in body")
		}
	}
}

func TestBootstrapRestoreRejectsInvalidBackupCandidate(t *testing.T) {
	invalidPath := filepath.Join(t.TempDir(), "invalid.db")
	if err := os.WriteFile(invalidPath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write invalid candidate: %v", err)
	}
	signer := newBootstrapRestoreTestCSRFSigner()
	csrfToken := signer.issue(time.Now())
	req := newBootstrapRestoreMultipartRequest(t, map[string]string{
		"csrf_token":  csrfToken,
		"setup_token": testBootstrapSetupToken,
	}, "invalid.db", invalidPath)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	rr := httptest.NewRecorder()

	if err := prepareBootstrapRestoreForm(rr, req, bootstrapRestoreMaxBodyBytes); err != nil {
		t.Fatalf("prepare bootstrap restore form: %v", err)
	}
	if !csrfRequestValid(signer, req) {
		t.Fatalf("expected CSRF to validate")
	}
	if !newBootstrapSetupGuard(testBootstrapSetupToken).Allow(req) {
		t.Fatalf("expected setup token to validate")
	}
	candidatePath := uploadedRestoreCandidatePath(t, req)
	if err := validateSQLiteBackupCandidate(candidatePath); err == nil {
		t.Fatalf("expected invalid SQLite backup candidate to be rejected")
	}
}

func TestBootstrapRestoreValidCandidateReopensSuccessfully(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()

	var refreshed *sql.DB
	result, err := restoreBootstrapBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDB, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore bootstrap backup: %v", err)
	}
	defer refreshed.Close()
	if result.PreRestoreBackupPath == "" {
		t.Fatalf("expected pre-restore backup path")
	}
	assertRestoreMarker(t, dbPath, "candidate")
	assertRestoreMarker(t, result.PreRestoreBackupPath, "current")
}

func TestBootstrapRestoreLegacyCandidateRunsMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contabase.db")
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "legacy.db")
	createLegacyBootstrapRestoreCandidate(t, candidatePath)

	var refreshed *sql.DB
	_, err := restoreBootstrapBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, contabaseDB.Open, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore legacy bootstrap backup: %v", err)
	}
	defer refreshed.Close()
	assertColumnExistsInBootstrapRestore(t, refreshed, "users", "status")
	assertColumnExistsInBootstrapRestore(t, refreshed, "workspaces", "type")
	assertColumnExistsInBootstrapRestore(t, refreshed, "accounts", "icon")
	assertTableExistsInBootstrapRestore(t, refreshed, "schema_migrations")
	assertLegacyAccountMigratedInRestore(t, refreshed)
}

func TestBootstrapRestoreRejectsMissingMinimumSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-contabase.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open unrelated db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (id TEXT PRIMARY KEY)`); err != nil {
		_ = db.Close()
		t.Fatalf("create unrelated table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close unrelated db: %v", err)
	}
	if err := validateBootstrapRestoreCandidate(path); err == nil {
		t.Fatalf("expected bootstrap restore candidate without users/workspaces to be rejected")
	}
}

func TestBootstrapRestoreRollsBackWhenReopenFails(t *testing.T) {
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
			return nil, os.ErrPermission
		}
		return openRestoreTestDB(dbURL)
	}
	result, err := restoreBootstrapBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openFn, func(db *sql.DB) {
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

func TestBootstrapRestoreRemovesStaleSidecars(t *testing.T) {
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
	_, err := restoreBootstrapBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDBNoWAL, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore bootstrap backup: %v", err)
	}
	defer refreshed.Close()
	assertSidecarDoesNotContain(t, dbPath+"-wal", "stale wal")
	assertSidecarDoesNotContain(t, dbPath+"-shm", "stale shm")
}

func TestBootstrapRestoreEnsuresWritableDatabasePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file mode assertion")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "permissions-contabase.db")
	currentDB := createRestoreTestDB(t, dbPath, "current")
	candidatePath := filepath.Join(dir, "candidate.db")
	createRestoreTestDB(t, candidatePath, "candidate").Close()
	if err := os.Chmod(candidatePath, 0o400); err != nil {
		t.Fatalf("chmod candidate: %v", err)
	}

	var refreshed *sql.DB
	_, err := restoreBootstrapBackupFromFile(currentDB, "file:"+dbPath, dbPath, candidatePath, openRestoreTestDB, func(db *sql.DB) {
		refreshed = db
	})
	if err != nil {
		t.Fatalf("restore bootstrap backup: %v", err)
	}
	defer refreshed.Close()
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat restored db: %v", err)
	}
	if info.Mode().Perm()&0o600 != 0o600 {
		t.Fatalf("expected restored db to be owner-readable and owner-writable, got mode %o", info.Mode().Perm())
	}
}

func newBootstrapRestoreTestCSRFSigner() *csrfSigner {
	return &csrfSigner{secret: bytes.Repeat([]byte{7}, 32), ttl: time.Hour}
}

func newBootstrapRestoreMultipartRequest(t *testing.T, fields map[string]string, fileName, filePath string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write multipart field: %v", err)
		}
	}
	if filePath != "" {
		part, err := writer.CreateFormFile("backup_file", fileName)
		if err != nil {
			t.Fatalf("create multipart file: %v", err)
		}
		file, err := os.Open(filePath)
		if err != nil {
			t.Fatalf("open multipart source: %v", err)
		}
		if _, err := io.Copy(part, file); err != nil {
			_ = file.Close()
			t.Fatalf("copy multipart source: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close multipart source: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/setup/restaurar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func uploadedRestoreCandidatePath(t *testing.T, req *http.Request) string {
	t.Helper()
	file, header, err := req.FormFile("backup_file")
	if err != nil {
		t.Fatalf("read uploaded backup file: %v", err)
	}
	defer file.Close()
	if strings.ToLower(filepath.Ext(header.Filename)) != ".db" {
		t.Fatalf("expected .db extension, got %q", header.Filename)
	}
	candidatePath := filepath.Join(t.TempDir(), "uploaded.db")
	out, err := os.Create(candidatePath)
	if err != nil {
		t.Fatalf("create uploaded candidate: %v", err)
	}
	if _, err := io.Copy(out, file); err != nil {
		_ = out.Close()
		t.Fatalf("copy uploaded candidate: %v", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		t.Fatalf("sync uploaded candidate: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close uploaded candidate: %v", err)
	}
	return candidatePath
}

func createLegacyBootstrapRestoreCandidate(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw legacy db: %v", err)
	}
	legacyDDL := []string{
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
		`INSERT INTO users (id, name, email, password_hash) VALUES ('legacy-user', 'Legacy Admin', 'legacy@example.test', 'hash')`,
		`INSERT INTO workspaces (id, name) VALUES ('legacy-workspace', 'Legacy Workspace')`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance)
			 VALUES ('legacy-orphan-account', 'missing-workspace', 'PagBank Antigo', 'CHECKING', 12345, 67890)`,
	}
	for _, stmt := range legacyDDL {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			t.Fatalf("seed raw legacy db: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw legacy db: %v", err)
	}
}

func assertLegacyAccountMigratedInRestore(t *testing.T, db *sql.DB) {
	t.Helper()

	var workspaceID, accountType, color, icon, providerSlug string
	var initialBalance, currentBalance int64
	if err := db.QueryRow(`
		SELECT workspace_id, type, color, icon, provider_slug, initial_balance, current_balance
		FROM accounts
		WHERE id = 'legacy-orphan-account'
	`).Scan(&workspaceID, &accountType, &color, &icon, &providerSlug, &initialBalance, &currentBalance); err != nil {
		t.Fatalf("query migrated legacy restore account: %v", err)
	}
	if workspaceID != "legacy-workspace" {
		t.Fatalf("legacy restore account workspace_id = %q, want legacy-workspace", workspaceID)
	}
	if accountType != "CHECKING" || color != "#FFE72D" || icon != "building-2" || providerSlug != "pagbank" {
		t.Fatalf("legacy restore account visual fallback = type:%s color:%s icon:%s provider:%s", accountType, color, icon, providerSlug)
	}
	if initialBalance != 12345 || currentBalance != 67890 {
		t.Fatalf("legacy restore account balances = initial:%d current:%d", initialBalance, currentBalance)
	}
	assertNoForeignKeyRowsInRestore(t, db)
}

func assertNoForeignKeyRowsInRestore(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("run restore foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatalf("scan restore foreign_key_check row: %v", err)
		}
		if rowID.Valid {
			t.Fatalf("expected empty restore foreign_key_check, got table=%s rowid=%d parent=%s fk=%d", table, rowID.Int64, parent, fkID)
		}
		t.Fatalf("expected empty restore foreign_key_check, got table=%s rowid=NULL parent=%s fk=%d", table, parent, fkID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("restore foreign_key_check rows: %v", err)
	}
}

func assertColumnExistsInBootstrapRestore(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info %s: %v", table, err)
		}
		if name == column {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table info %s: %v", table, err)
	}
	t.Fatalf("expected column %s.%s to exist", table, column)
}

func assertTableExistsInBootstrapRestore(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("query table %s: %v", table, err)
	}
	if count != 1 {
		t.Fatalf("expected table %s to exist", table)
	}
}
