package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/admincli"
	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const legacyFixtureEmail = "legacy@example.test"
const legacyOwnPassword = "synthetic-private-password"

func TestLegacySeedCredentialStates(t *testing.T) {
	known := legacyFixtureHash(t, legacySeedPassword)
	own := legacyFixtureHash(t, legacyOwnPassword)
	for _, tc := range []struct {
		name, hash, status string
		deleted            int64
		noWorkspace        bool
		disable            bool
	}{
		{name: "empty", hash: "", status: "pending", disable: true},
		{name: "whitespace", hash: "  ", status: "active", disable: true},
		{name: "pending_placeholder", hash: "PENDING", status: "pending", disable: true},
		{name: "bcrypt_placeholder", hash: "$2a$10$placeholder", status: "pending", disable: true},
		{name: "invalid_hash", hash: "not-a-password-hash", status: "active", disable: true},
		{name: "pending_valid_hash", hash: own, status: "pending"},
		{name: "previously_repaired", hash: known, status: "active", disable: true},
		{name: "pending_known_password", hash: known, status: "pending", disable: true},
		{name: "own_password", hash: own, status: "active"},
		{name: "deleted_known_password", hash: known, status: "pending", deleted: 123, disable: true},
		{name: "deleted_own_password", hash: own, status: "active", deleted: 123},
		{name: "orphan_known_password", hash: known, status: "active", noWorkspace: true, disable: true},
		{name: "orphan_own_password", hash: own, status: "active", noWorkspace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := legacyFixture(t, tc.hash, tc.status)
			db := owner.DB
			legacyExec(t, db, `UPDATE users SET deleted_at = ? WHERE id = ?`, tc.deleted, legacySeedUserID)
			if tc.noWorkspace {
				legacyExec(t, db, `DELETE FROM workspace_members WHERE user_id = ?`, legacySeedUserID)
			}
			beforeUser := legacySnapshot(t, db, `SELECT * FROM users WHERE id = ?`, legacySeedUserID)
			beforeForeign := legacyForeignState(t, db)
			beforeFinancial := legacyFinancialState(t, db)
			if err := disableLegacySeedCredentials(db); err != nil {
				t.Fatal(err)
			}
			var hash, status string
			var deleted int64
			var mustChange int
			var expiry, activationExpiry sql.NullInt64
			var activation sql.NullString
			if err := db.QueryRow(`SELECT password_hash, status, deleted_at, must_change_password,
				temporary_password_expires_at, activation_token_hash, activation_expires_at FROM users WHERE id = ?`,
				legacySeedUserID).Scan(&hash, &status, &deleted, &mustChange, &expiry, &activation, &activationExpiry); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || deleted != tc.deleted {
				t.Fatal("containment changed activation or deletion state")
			}
			if tc.disable {
				if bcrypt.CompareHashAndPassword([]byte(hash), []byte(legacySeedPassword)) == nil || hash != disabledLegacySeedHash {
					t.Fatal("known or invalid credential was not disabled")
				}
				if mustChange != 1 || expiry.Valid || activation.Valid || activationExpiry.Valid {
					t.Fatal("containment did not require explicit administrative recovery")
				}
				for _, query := range []string{
					`SELECT count(*) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`,
					`SELECT count(*) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`,
					`SELECT count(*) FROM mfa_setup_challenges WHERE user_id = ? AND consumed_at IS NULL`,
				} {
					legacyCount(t, db, query, legacySeedUserID, 0)
				}
				legacyCount(t, db, `SELECT count(*) FROM auth_audit_events WHERE user_id = ? AND event_type = 'LEGACY_SEED_CREDENTIAL_DISABLED'`, legacySeedUserID, 1)
			} else if beforeUser != legacySnapshot(t, db, `SELECT * FROM users WHERE id = ?`, legacySeedUserID) {
				t.Fatal("legitimate password or account metadata changed")
			}
			if beforeForeign != legacyForeignState(t, db) || beforeFinancial != legacyFinancialState(t, db) {
				t.Fatal("containment changed other users, memberships or financial data")
			}
			after := legacyAuthState(t, db)
			for i := 0; i < 3; i++ {
				seedIfEmpty(db)
				if err := disableLegacySeedCredentials(db); err != nil {
					t.Fatal(err)
				}
			}
			if after != legacyAuthState(t, db) {
				t.Fatal("repeated initialization changed contained or legitimate credentials")
			}
			service := auth.NewService(db)
			password := legacyOwnPassword
			if tc.disable {
				password = legacySeedPassword
			}
			_, err := service.Authenticate(legacyFixtureEmail, password)
			if tc.disable || tc.deleted != 0 || tc.status == "pending" {
				if err == nil {
					t.Fatal("unavailable account authenticated")
				}
			} else if err != nil {
				t.Fatalf("legitimate password rejected: %v", err)
			}
		})
	}
}

func TestLegacySeedNewInstallationKeepsExplicitBootstrap(t *testing.T) {
	owner := legacyEmptyFixture(t)
	db := owner.DB
	seedIfEmpty(db)
	if err := disableLegacySeedCredentials(db); err != nil {
		t.Fatal(err)
	}
	legacyCount(t, db, `SELECT count(*) FROM users WHERE 1 = ?`, 1, 0)
	allowed, err := setupAllowed(db)
	if err != nil || !allowed {
		t.Fatalf("empty installation cannot enter explicit setup: %v", err)
	}
	userID, err := runInitialSetup(db, "Synthetic Admin", "new@example.test", legacyOwnPassword, "personal")
	if err != nil || userID == legacySeedUserID || userID == "" {
		t.Fatalf("bootstrap failed to create a new identity: %v", err)
	}
	before := legacyAuthState(t, db)
	if err := disableLegacySeedCredentials(db); err != nil {
		t.Fatal(err)
	}
	if before != legacyAuthState(t, db) {
		t.Fatal("new random identity was changed")
	}
	allowed, err = setupAllowed(db)
	if err != nil || allowed {
		t.Fatal("setup reopened after first admin")
	}
	service := auth.NewService(db)
	response := legacyLogin(t, db, service, "new@example.test", legacyOwnPassword)
	legacyLogout(t, service, response)
}

func TestLegacySeedRecoveryPreserves2FAAndRequiresNewPassword(t *testing.T) {
	for _, use2FA := range []bool{false, true} {
		t.Run(fmt.Sprintf("totp_%t", use2FA), func(t *testing.T) {
			owner := legacyFixture(t, legacyFixtureHash(t, legacySeedPassword), "active")
			db := owner.DB
			service := auth.NewService(db)
			if use2FA {
				secret, err := encryptTextForAuth(testTOTPSecret)
				if err != nil {
					t.Fatal(err)
				}
				if err := service.UpdateTOTPSetup(legacySeedUserID, secret, `[]`, true); err != nil {
					t.Fatal(err)
				}
			}
			beforeFactors := legacySnapshot(t, db, `SELECT totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at, totp_last_used_step FROM users WHERE id = ?`, legacySeedUserID)
			oldSession, _, err := service.CreateSession(legacySeedUserID, "legacy-personal", time.Hour, false)
			if err != nil {
				t.Fatal(err)
			}
			oldPreauth, _, err := service.CreatePreAuthSession(legacySeedUserID, "TOTP", time.Hour, false)
			if err != nil {
				t.Fatal(err)
			}
			oldSetup, _, err := service.CreateMFASetupChallenge(legacySeedUserID, "synthetic-secret", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := disableLegacySeedCredentials(db); err != nil {
				t.Fatal(err)
			}
			if _, _, _, _, err := service.ResolveAndRefreshSession(oldSession); err == nil {
				t.Fatal("previous session survived containment")
			}
			if _, _, _, err := service.ResolvePreAuthSession(oldPreauth); err == nil {
				t.Fatal("previous pre-auth challenge survived containment")
			}
			if _, err := service.ResolveMFASetupChallenge(oldSetup, legacySeedUserID); err == nil {
				t.Fatal("previous MFA setup challenge survived containment")
			}
			denied := legacyLogin(t, db, service, legacyFixtureEmail, legacySeedPassword)
			if denied.Code != http.StatusUnauthorized || findCookie(denied.Result().Cookies(), sessionCookieName) != nil {
				t.Fatal("known password obtained login access")
			}
			result, err := admincli.ResetAdminPassword(db, legacyFixtureEmail)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.TemporaryPassword) < 20 || result.TemporaryPassword == legacySeedPassword {
				t.Fatal("recovery did not issue a fresh temporary credential")
			}
			if beforeFactors != legacySnapshot(t, db, `SELECT totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at, totp_last_used_step FROM users WHERE id = ?`, legacySeedUserID) {
				t.Fatal("containment or password recovery modified enrolled 2FA")
			}
			state, err := service.TemporaryPasswordState(legacySeedUserID, time.Now())
			if err != nil || !state.Required || state.Expired || state.ExpiresAt != result.ExpiresAt {
				t.Fatal("temporary recovery credential lacks mandatory change or expiry")
			}
			beforeRecoveryRestart := legacyAuthState(t, db)
			if err := disableLegacySeedCredentials(db); err != nil {
				t.Fatal(err)
			}
			if beforeRecoveryRestart != legacyAuthState(t, db) {
				t.Fatal("startup invalidated a securely recovered credential")
			}
			response := legacyLogin(t, db, service, legacyFixtureEmail, result.TemporaryPassword)
			if use2FA {
				if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login/2fa" || findCookie(response.Result().Cookies(), sessionCookieName) != nil {
					t.Fatal("password recovery bypassed 2FA")
				}
				preauth := findCookie(response.Result().Cookies(), preAuthCookieName)
				if preauth == nil {
					t.Fatal("missing 2FA challenge")
				}
				code, err := totp.GenerateCode(testTOTPSecret, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(url.Values{"code": {code}}.Encode()))
				request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				request.AddCookie(preauth)
				response = httptest.NewRecorder()
				handleLogin2FASubmit(response, request, dummyTemplateEngine{}, service, db, "csrf")
			}
			if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/trocar-senha" {
				t.Fatal("recovered account did not require password change")
			}
			cookie := findCookie(response.Result().Cookies(), sessionCookieName)
			if cookie == nil {
				t.Fatal("missing temporary authenticated session")
			}
			request := httptest.NewRequest(http.MethodPost, "/trocar-senha", strings.NewReader(url.Values{
				"new_password": {legacyOwnPassword}, "confirm_password": {legacyOwnPassword},
			}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(cookie)
			changed := httptest.NewRecorder()
			handleRequiredPasswordChangeSubmit(changed, request, dummyTemplateEngine{}, service, db, authContext{
				UserID: legacySeedUserID, ActiveWorkspaceID: "legacy-personal", Role: "ADMIN", SessionToken: cookie.Value,
			}, "csrf")
			if changed.Code != http.StatusSeeOther || changed.Header().Get("Location") != "/" {
				t.Fatal("mandatory password change failed")
			}
			if _, err := service.Authenticate(legacyFixtureEmail, legacyOwnPassword); err != nil {
				t.Fatal("new private password rejected")
			}
			if _, err := service.Authenticate(legacyFixtureEmail, result.TemporaryPassword); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatal("temporary credential remained valid after password change")
			}
			legacyLogout(t, service, response)
		})
	}
}

func TestLegacySeedContainmentRollsBackAllAuthWrites(t *testing.T) {
	for _, table := range []string{"sessions", "pre_auth_sessions", "mfa_setup_challenges", "auth_audit_events"} {
		t.Run(table, func(t *testing.T) {
			owner := legacyFixture(t, legacyFixtureHash(t, legacySeedPassword), "active")
			db := owner.DB
			operation := "UPDATE"
			if table == "auth_audit_events" {
				operation = "INSERT"
			}
			legacyExec(t, db, `CREATE TRIGGER reject_containment BEFORE `+operation+` ON `+table+` BEGIN SELECT RAISE(ABORT, 'synthetic containment failure'); END`)
			before := legacyAuthState(t, db)
			if err := disableLegacySeedCredentials(db); err == nil {
				t.Fatal("injected storage failure was ignored")
			}
			if before != legacyAuthState(t, db) {
				t.Fatal("failed containment persisted partial authentication changes")
			}
		})
	}
}

func TestLegacySeedRuntimeFailsClosedBeforeStartingJobs(t *testing.T) {
	owner := legacyFixture(t, legacyFixtureHash(t, legacySeedPassword), "active")
	var path string
	if err := owner.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	legacyExec(t, owner.DB, `CREATE TRIGGER reject_containment BEFORE INSERT ON auth_audit_events BEGIN SELECT RAISE(ABORT, 'synthetic containment failure'); END`)
	before := legacyAuthState(t, owner.DB)
	starts := 0
	runtime, err := startServerRuntime(owner, serverRuntimeOptions{hooks: &serverRuntimeStartHooks{
		onResourceStart: func(string, *sql.DB) { starts++ },
	}})
	if err == nil || runtime != nil || starts != 0 {
		t.Fatal("failed containment published a runtime or started jobs")
	}
	if err := owner.DB.Ping(); err == nil {
		t.Fatal("failed runtime leaked its database owner")
	}
	reopened, err := database.OpenRuntimeDBs("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if before != legacyAuthState(t, reopened.DB) {
		t.Fatal("rejected runtime persisted incomplete containment")
	}
}

func TestLegacySeedRuntimeColdStartAndRestoreContainKnownCredentials(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore_%t", restore), func(t *testing.T) {
			owner := legacyFixture(t, legacyFixtureHash(t, legacySeedPassword), "active")
			var path string
			if err := owner.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
				t.Fatal(err)
			}
			start := func(dbs *database.RuntimeDBs) (*serverRuntime, error) {
				return startServerRuntime(dbs, serverRuntimeOptions{hooks: &serverRuntimeStartHooks{
					afterRuntimeDBsActivate: func() {
						var hash string
						if err := dbs.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, legacySeedUserID).Scan(&hash); err != nil || hash != disabledLegacySeedHash {
							t.Fatal("runtime activated before containing the legacy credential")
						}
					},
				}})
			}
			var runtime *serverRuntime
			var err error
			if restore {
				if err := owner.Close(); err != nil {
					t.Fatal(err)
				}
				runtime, err = reopenAdminRestoreRuntime("file:"+path, database.OpenRuntimeDBs, start, true)
			} else {
				runtime, err = start(owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := database.OpenRuntimeDBs("file:" + path)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err = start(reopened)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			legacyCount(t, runtime.DBs.DB, `SELECT count(*) FROM auth_audit_events WHERE user_id = ? AND event_type = 'LEGACY_SEED_CREDENTIAL_DISABLED'`, legacySeedUserID, 1)
		})
	}
}

func legacyEmptyFixture(t *testing.T) *database.RuntimeDBs {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_DIR", root)
	t.Setenv("APP_ENV", "production")
	t.Setenv("AUTH_ENCRYPTION_KEY", testAuthEncryptionKey("legacy-seed-test"))
	owner, err := database.OpenRuntimeDBs("file:" + filepath.Join(root, "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	return owner
}

func legacyFixture(t *testing.T, hash, status string) *database.RuntimeDBs {
	t.Helper()
	owner := legacyEmptyFixture(t)
	db := owner.DB
	legacyExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, activation_token_hash, activation_expires_at, created_at, updated_at)
		VALUES (?, 'Legacy', ?, ?, ?, 'synthetic-activation', 9999999999, 1, 1),
		('foreign-user', 'Other', 'other@example.test', ?, 'active', NULL, NULL, 1, 1)`,
		legacySeedUserID, legacyFixtureEmail, hash, status, hash)
	legacyExec(t, db, `INSERT INTO workspaces (id, name, type) VALUES ('legacy-personal', 'Personal synthetic', 'personal'), ('foreign-business', 'Business synthetic', 'business')`)
	legacyExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('legacy-personal', ?, 'ADMIN'), ('foreign-business', 'foreign-user', 'ADMIN')`, legacySeedUserID)
	for _, user := range []string{legacySeedUserID, "foreign-user"} {
		workspace := "legacy-personal"
		if user == "foreign-user" {
			workspace = "foreign-business"
		}
		legacyExec(t, db, `INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at) VALUES (?, ?, ?, ?, 9999999999)`, user, user, workspace, "session-"+user)
		legacyExec(t, db, `INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at) VALUES (?, ?, ?, 'TOTP', 9999999999)`, user, user, "preauth-"+user)
		legacyExec(t, db, `INSERT INTO mfa_setup_challenges (id, user_id, token_hash, secret_enc, expires_at) VALUES (?, ?, ?, 'synthetic-factor', 9999999999)`, user, user, "mfa-"+user)
		legacyExec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES (?, ?, 'Synthetic account', 'CHECKING', 100000, 100000)`, workspace, workspace)
		legacyExec(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description) VALUES (?, ?, ?, ?, 'EXPENSE', 1234, 1772323200, 'Synthetic financial sentinel')`, workspace, workspace, user, workspace)
	}
	return owner
}

func legacyFixtureHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func legacyExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := serverFixtureExec(t, db, query, args...); err != nil {
		t.Fatalf("synthetic fixture SQL failed: %v", err)
	}
}

func legacyCount(t *testing.T, db *sql.DB, query string, arg any, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, arg).Scan(&got); err != nil || got != want {
		t.Fatalf("count=%d, want=%d, error=%v", got, want, err)
	}
}

func legacySnapshot(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for rows.Next() {
		values, pointers := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&result, "%#v\n", values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func legacyAuthState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var result string
	for _, table := range []string{"users", "sessions", "pre_auth_sessions", "mfa_setup_challenges", "auth_audit_events", "auth_lockouts"} {
		result += table + legacySnapshot(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
	}
	return result
}

func legacyForeignState(t *testing.T, db *sql.DB) string {
	t.Helper()
	result := legacySnapshot(t, db, `SELECT * FROM users WHERE id = 'foreign-user'`)
	for _, table := range []string{"sessions", "pre_auth_sessions", "mfa_setup_challenges", "auth_audit_events", "auth_lockouts"} {
		result += table + legacySnapshot(t, db, `SELECT * FROM `+table+` WHERE user_id = 'foreign-user' ORDER BY rowid`)
	}
	return result
}

func legacyFinancialState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var result string
	for _, table := range []string{"workspaces", "workspace_members", "accounts", "transactions", "invoices", "recurring_rules", "investments"} {
		result += table + legacySnapshot(t, db, `SELECT * FROM `+table+` ORDER BY rowid`)
	}
	return result
}

func legacyLogin(t *testing.T, db *sql.DB, service *auth.Service, email, password string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"email": {email}, "password": {password}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handleLoginSubmit(response, request, dummyTemplateEngine{}, service, db, "csrf")
	return response
}

func legacyLogout(t *testing.T, service *auth.Service, response *httptest.ResponseRecorder) {
	t.Helper()
	cookie := findCookie(response.Result().Cookies(), sessionCookieName)
	if cookie == nil {
		t.Fatal("missing final login session")
	}
	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	request.AddCookie(cookie)
	handleLogout(httptest.NewRecorder(), request, service)
	if _, _, _, _, err := service.ResolveAndRefreshSession(cookie.Value); err == nil {
		t.Fatal("logout did not revoke session")
	}
}
