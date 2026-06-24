package handlers

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"

	"golang.org/x/crypto/bcrypt"
)

type adminUsersRecoveryTestTemplates struct{}

func (adminUsersRecoveryTestTemplates) ExecuteTemplate(w io.Writer, name string, data any) error {
	_, _ = w.Write([]byte("ok"))
	return nil
}

func (adminUsersRecoveryTestTemplates) Lookup(name string) *template.Template {
	return template.New(name)
}

func TestAdminUsersRecoveryRenderUsesContextualAdminConfirmation(t *testing.T) {
	content, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/configuracoes_admin_users.html"))
	if err != nil {
		t.Fatalf("read admin users template: %v", err)
	}

	body := string(content)
	assertContains(t, body, `data-admin-recovery-action="password"`)
	assertContains(t, body, `data-admin-recovery-action="sessions"`)
	assertContains(t, body, `data-admin-recovery-action="2fa"`)
	assertContains(t, body, "Ações em administradores exigem confirmação adicional.")
	assertContains(t, body, "Essa ação será registrada em auditoria.")
	assertContains(t, body, "Para evitar alteração acidental no administrador errado, digite o e-mail abaixo para confirmar:")
	assertContains(t, body, `placeholder="Digite {{.Email}}"`)
	assertContains(t, body, "Confirmar geração de senha temporária")
	assertContains(t, body, "Confirmar revogação de sessões")
	assertContains(t, body, "Confirmar desativação de 2FA")

	if strings.Contains(body, "Digite o e-mail do administrador para confirmar:") {
		t.Fatalf("admin recovery template still renders the old confirmation text format")
	}

	if strings.Contains(body, `placeholder="Confirme:`) {
		t.Fatalf("admin recovery template still renders inline duplicated confirmation placeholders")
	}
}

func TestHandleAdminUsersSaveRejectsManagerBeforeMembershipMutation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersHandlerRBACScenario(t, db)

	handler := ConfiguracoesHandler{
		DB:          db,
		WorkspaceID: "workspace-a",
		UserID:      "manager-user",
		ActorRole:   models.RoleManager,
	}
	form := url.Values{}
	form.Set("user_id", "target-user")
	form.Set("name", "Target User")
	form.Set("email", "target@example.com")
	form.Set("role", models.RoleUser)
	form.Add("workspace_ids", "workspace-b")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersSave(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	assertWorkspaceMemberCount(t, db, "workspace-b", "target-user", 0)
	assertWorkspaceMemberCount(t, db, "workspace-a", "target-user", 1)
}

func TestHandleAdminUsersResetPasswordRejectsManagerBeforeUserMutation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersHandlerRBACScenario(t, db)

	handler := ConfiguracoesHandler{
		DB:          db,
		WorkspaceID: "workspace-a",
		UserID:      "manager-user",
		ActorRole:   models.RoleManager,
	}
	form := url.Values{}
	form.Set("user_id", "target-user")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/reset-senha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersResetPassword(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	var got string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = 'target-user'`).Scan(&got); err != nil {
		t.Fatalf("query target password hash: %v", err)
	}
	if got != "old-hash" {
		t.Fatalf("password_hash = %q, want old-hash", got)
	}
}

func TestHandleAdminUsersResetPasswordGeneratesTemporaryPassword(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)

	handler := ConfiguracoesHandler{
		DB:          db,
		Templates:   adminUsersRecoveryTestTemplates{},
		WorkspaceID: "workspace-a",
		UserID:      "admin-actor",
		ActorRole:   models.RoleAdmin,
	}

	for _, tc := range []struct {
		name          string
		userID        string
		email         string
		confirm       string
		wantGenerated bool
	}{
		{"user", "target-user", "target-user@example.com", "", true},
		{"manager", "target-manager", "target-manager@example.com", "", true},
		{"admin_requires_confirmation_missing", "target-admin", "target-admin@example.com", "", false},
		{"admin_with_confirmation", "target-admin", "target-admin@example.com", "target-admin@example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantGenerated {
				seedTargetAuthState(t, db, tc.userID)
			}
			form := url.Values{}
			form.Set("user_id", tc.userID)
			form.Set("confirm_email", tc.confirm)
			req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/reset-senha", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()

			handler.HandleAdminUsersResetPassword(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
			}
			if tc.wantGenerated {
				assertAdminUserTemporaryPasswordGenerated(t, db, tc.userID)
				assertAdminUserAuthStateRevoked(t, db, tc.userID)
				assertAdminUserWorkspaceRole(t, db, tc.userID)
				assertAdminUserAuditEvent(t, db, tc.userID, "ADMIN_USER_TEMPORARY_PASSWORD_RESET")
			} else {
				assertAdminUserPasswordHash(t, db, tc.userID, "old-hash")
			}
		})
	}
}

func TestHandleAdminUsersDisable2FARecovery(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)

	handler := ConfiguracoesHandler{
		DB:          db,
		Templates:   adminUsersRecoveryTestTemplates{},
		WorkspaceID: "workspace-a",
		UserID:      "admin-actor",
		ActorRole:   models.RoleAdmin,
	}

	for _, tc := range []struct {
		name        string
		userID      string
		email       string
		confirm     string
		wantChanged bool
	}{
		{"user", "target-user", "target-user@example.com", "", true},
		{"manager", "target-manager", "target-manager@example.com", "", true},
		{"admin_requires_confirmation_missing", "target-admin", "target-admin@example.com", "", false},
		{"admin_with_confirmation", "target-admin", "target-admin@example.com", "target-admin@example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantChanged {
				seedTargetAuthState(t, db, tc.userID)
			}
			form := url.Values{}
			form.Set("user_id", tc.userID)
			form.Set("confirm_email", tc.confirm)
			req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/desativar-2fa", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()

			handler.HandleAdminUsersDisable2FA(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
			}
			if tc.wantChanged {
				assertAdminUser2FADisabled(t, db, tc.userID)
				assertAdminUserAuthStateRevoked(t, db, tc.userID)
				assertAdminUserPasswordHash(t, db, tc.userID, "old-hash")
				assertAdminUserWorkspaceRole(t, db, tc.userID)
				assertAdminUserAuditEvent(t, db, tc.userID, "ADMIN_USER_2FA_DISABLED")
			} else {
				assertAdminUser2FAEnabled(t, db, tc.userID)
			}
		})
	}
}

func TestHandleAdminUsersRevokeSessionsRecovery(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)

	handler := ConfiguracoesHandler{
		DB:          db,
		Templates:   adminUsersRecoveryTestTemplates{},
		WorkspaceID: "workspace-a",
		UserID:      "admin-actor",
		ActorRole:   models.RoleAdmin,
	}
	seedTargetAuthState(t, db, "target-user")
	form := url.Values{}
	form.Set("user_id", "target-user")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/revogar-sessoes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersRevokeSessions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	assertAdminUser2FAEnabled(t, db, "target-user")
	assertAdminUserAuthStateRevoked(t, db, "target-user")
	assertAdminUserPasswordHash(t, db, "target-user", "old-hash")
	assertAdminUserWorkspaceRole(t, db, "target-user")
	assertAdminUserAuditEvent(t, db, "target-user", "ADMIN_USER_SESSIONS_REVOKED")
}

func TestHandleAdminUsersRecoveryRejectsManagerBeforeMutation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedAdminUsersRecoveryScenario(t, db)
	seedTargetAuthState(t, db, "target-user")

	handler := ConfiguracoesHandler{
		DB:          db,
		Templates:   adminUsersRecoveryTestTemplates{},
		WorkspaceID: "workspace-a",
		UserID:      "target-manager",
		ActorRole:   models.RoleManager,
	}
	form := url.Values{}
	form.Set("user_id", "target-user")
	req := httptest.NewRequest(http.MethodPost, "/admin/usuarios/desativar-2fa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	handler.HandleAdminUsersDisable2FA(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	assertAdminUser2FAEnabled(t, db, "target-user")
}

func TestNormalizeConfigSectionHidesAdminUsersFromManager(t *testing.T) {
	if got := normalizeConfigSection("admin-users", models.RoleManager); got != "perfil" {
		t.Fatalf("manager admin-users section = %q, want perfil", got)
	}
	if got := normalizeConfigSection("admin-users", models.RoleUser); got != "perfil" {
		t.Fatalf("user admin-users section = %q, want perfil", got)
	}
	if got := normalizeConfigSection("admin-users", models.RoleAdmin); got != "admin-users" {
		t.Fatalf("admin admin-users section = %q, want admin-users", got)
	}
}

func TestNormalizeCustomPermissionsDisablesManualSubmissionsForMVP(t *testing.T) {
	got := normalizeCustomPermissions([]string{
		models.PermissionBackupExport,
		models.PermissionContactsDelete,
		models.PermissionWorkspaceEdit,
		models.PermissionReportsView,
		"config:write",
	})
	if len(got) != 0 {
		t.Fatalf("custom permissions = %v, want none", got)
	}
}

func seedAdminUsersHandlerRBACScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES
			('manager-user', 'Manager User', 'manager@example.com', 'hash', 'active', ?, ?),
			('target-user', 'Target User', 'target@example.com', 'old-hash', 'active', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('workspace-a', 'Workspace A', '', 'personal', ?, ?),
			('workspace-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('workspace-a', 'manager-user', 'MANAGER', ?),
			('workspace-a', 'target-user', 'USER', ?)
	`, now, now)
}

func seedAdminUsersRecoveryScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, status, totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at, created_at, updated_at)
		VALUES
			('admin-actor', 'Admin Actor', 'admin-actor@example.com', 'actor-hash', 'active', 0, NULL, '[]', NULL, ?, ?),
			('target-user', 'Target User', 'target-user@example.com', 'old-hash', 'active', 1, 'v1:secret', '[{"hash":"backup"}]', ?, ?, ?),
			('target-manager', 'Target Manager', 'target-manager@example.com', 'old-hash', 'active', 1, 'v1:secret', '[{"hash":"backup"}]', ?, ?, ?),
			('target-admin', 'Target Admin', 'target-admin@example.com', 'old-hash', 'active', 1, 'v1:secret', '[{"hash":"backup"}]', ?, ?, ?)
	`, now, now, now, now, now, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('workspace-a', 'Workspace A', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('workspace-a', 'admin-actor', 'ADMIN', ?),
			('workspace-a', 'target-user', 'USER', ?),
			('workspace-a', 'target-manager', 'MANAGER', ?),
			('workspace-a', 'target-admin', 'ADMIN', ?)
	`, now, now, now, now)
}

func seedTargetAuthState(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE users SET totp_enabled = 1, totp_secret_enc = 'v1:secret', totp_backup_codes = '[{"hash":"backup"}]', totp_enabled_at = ?, password_hash = 'old-hash' WHERE id = ?`, now, userID)
	execTestSQL(t, db, `INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, is_remember, created_at) VALUES (?, ?, 'workspace-a', ?, ?, 1, ?)`, "session-"+userID, userID, "hash-"+userID, now+3600, now)
	execTestSQL(t, db, `INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at, remember_me, created_at) VALUES (?, ?, ?, 'TOTP', ?, 1, ?)`, "preauth-"+userID, userID, "prehash-"+userID, now+300, now)
	execTestSQL(t, db, `INSERT OR REPLACE INTO auth_lockouts (user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at) VALUES (?, 0, 3, ?, ?, ?, 'totp', ?)`, userID, now, now, now+600, now)
}

func assertAdminUser2FADisabled(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	var enabled int
	var secret sql.NullString
	var codes string
	var enabledAt sql.NullInt64
	if err := db.QueryRow(`SELECT totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at FROM users WHERE id = ?`, userID).Scan(&enabled, &secret, &codes, &enabledAt); err != nil {
		t.Fatalf("query 2fa fields: %v", err)
	}
	if enabled != 0 || secret.Valid || codes != "[]" || enabledAt.Valid {
		t.Fatalf("2FA state not cleared for %s: enabled=%d secret=%v codes=%q enabledAt=%v", userID, enabled, secret.Valid, codes, enabledAt.Valid)
	}
}

func assertAdminUser2FAEnabled(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`SELECT totp_enabled FROM users WHERE id = ?`, userID).Scan(&enabled); err != nil {
		t.Fatalf("query 2fa enabled: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("totp_enabled for %s = %d, want 1", userID, enabled)
	}
}

func assertAdminUserAuthStateRevoked(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, userID, 0)
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`, userID, 0)
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, userID, 0)
}

func assertAdminUserPasswordHash(t *testing.T, db *sql.DB, userID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&got); err != nil {
		t.Fatalf("query password hash: %v", err)
	}
	if got != want {
		t.Fatalf("password hash for %s = %q, want %q", userID, got, want)
	}
}

func assertAdminUserTemporaryPasswordGenerated(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	var hash string
	var mustChange int
	var expiresAt sql.NullInt64
	if err := db.QueryRow(`SELECT password_hash, must_change_password, temporary_password_expires_at FROM users WHERE id = ?`, userID).Scan(&hash, &mustChange, &expiresAt); err != nil {
		t.Fatalf("query temporary password fields: %v", err)
	}
	if hash == "old-hash" {
		t.Fatalf("password hash was not replaced")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("old-hash")); err == nil {
		t.Fatalf("temporary password hash must not match old password")
	}
	if mustChange != 1 {
		t.Fatalf("must_change_password = %d, want 1", mustChange)
	}
	if !expiresAt.Valid || expiresAt.Int64 <= time.Now().Unix() {
		t.Fatalf("temporary_password_expires_at invalid: %+v", expiresAt)
	}
}

func assertAdminUserWorkspaceRole(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM workspace_members WHERE user_id = ?`, userID, 1)
}

func assertAdminUserAuditEvent(t *testing.T, db *sql.DB, userID, eventType string) {
	t.Helper()
	assertAdminUsersHandlerCount(t, db, `SELECT COUNT(1) FROM auth_audit_events WHERE user_id = ? AND event_type = '`+eventType+`'`, userID, 1)
}

func assertAdminUsersHandlerCount(t *testing.T, db *sql.DB, query, userID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, userID).Scan(&got); err != nil {
		t.Fatalf("count query failed: %v\nquery: %s", err, query)
	}
	if got != want {
		t.Fatalf("count for %s = %d, want %d\nquery: %s", userID, got, want, query)
	}
}

func assertWorkspaceMemberCount(t *testing.T, db *sql.DB, workspaceID, userID string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`
		SELECT COUNT(1)
		FROM workspace_members
		WHERE workspace_id = ? AND user_id = ?
	`, workspaceID, userID).Scan(&got); err != nil {
		t.Fatalf("query workspace member count: %v", err)
	}
	if got != want {
		t.Fatalf("workspace member count for %s/%s = %d, want %d", workspaceID, userID, got, want)
	}
}
