package admincli

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func TestResetAdminPassword(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Create test workspace
	workspaceID := uuid.NewString()
	_, err = db.Exec(`INSERT INTO workspaces (id, name, type) VALUES (?, 'Test Workspace', 'business')`, workspaceID)
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	// Create admin user
	adminID := uuid.NewString()
	adminEmail := "admin@example.com"
	oldPassword := "old-password-123"
	oldPasswordHash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash old password: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status)
		VALUES (?, 'Admin', ?, ?, 'active')
	`, adminID, adminEmail, string(oldPasswordHash))
	if err != nil {
		t.Fatalf("failed to create admin user: %v", err)
	}

	// Assign admin role
	_, err = db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', ?)
	`, workspaceID, adminID, time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to assign admin role: %v", err)
	}

	// Create a dummy session for the admin
	_, err = db.Exec(`
		INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, 'dummyhash', ?, ?)
	`, uuid.NewString(), adminID, workspaceID, time.Now().Add(24*time.Hour).Unix(), time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create regular and manager users
	userID := uuid.NewString()
	userEmail := "user@example.com"
	_, err = db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status)
		VALUES (?, 'User', ?, ?, 'active')
	`, userID, userEmail, string(oldPasswordHash))
	if err != nil {
		t.Fatalf("failed to create regular user: %v", err)
	}
	managerID := uuid.NewString()
	managerEmail := "manager@example.com"
	_, err = db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status)
		VALUES (?, 'Manager', ?, ?, 'active')
	`, managerID, managerEmail, string(oldPasswordHash))
	if err != nil {
		t.Fatalf("failed to create manager user: %v", err)
	}

	// Assign user roles
	_, err = db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'USER', ?)
	`, workspaceID, userID, time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to assign user role: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'MANAGER', ?)
	`, workspaceID, managerID, time.Now().Unix())
	if err != nil {
		t.Fatalf("failed to assign manager role: %v", err)
	}
	seedAdminCLIAuthState(t, db, workspaceID, userID)
	seedAdminCLIAuthState(t, db, workspaceID, managerID)

	for _, tc := range []struct {
		name   string
		userID string
		email  string
	}{
		{"success_admin_user", adminID, adminEmail},
		{"success_regular_user", userID, userEmail},
		{"success_manager_user", managerID, managerEmail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ResetAdminPassword(db, tc.email)
			if err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if result.Email != tc.email || result.UserID != tc.userID {
				t.Fatalf("unexpected result: %+v", result)
			}
			if len(result.TemporaryPassword) < 20 {
				t.Fatalf("temporary password too short: %d", len(result.TemporaryPassword))
			}
			assertAdminCLIPassword(t, db, tc.userID, result.TemporaryPassword)
			assertAdminCLIAuthentication(t, db, tc.email, tc.userID, oldPassword, result.TemporaryPassword)
			assertAdminCLITemporaryPasswordFlags(t, db, tc.userID, result.ExpiresAt)
			assertAdminCLIAuthStateRevoked(t, db, tc.userID)
			assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_audit_events WHERE user_id = ? AND event_type = 'USER_TEMPORARY_PASSWORD_RESET_LOCAL_RECOVERY'`, tc.userID, 1)
			assertAdminCLIAuditDoesNotContain(t, db, tc.userID, result.TemporaryPassword)
		})
	}

	t.Run("fail_user_not_found", func(t *testing.T) {
		_, err := ResetAdminPassword(db, "nobody@example.com")
		if err != ErrUserNotFound {
			t.Errorf("expected ErrUserNotFound, got: %v", err)
		}
	})
}

func TestListAndUnlockAuthLockouts(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	now := time.Now()
	workspaceID := uuid.NewString()
	userID := uuid.NewString()
	otherUserID := uuid.NewString()
	expiredUserID := uuid.NewString()
	mustExec(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES (?, 'Test Workspace', 'business', ?, ?)`, workspaceID, now.Unix(), now.Unix())
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES (?, 'Locked User', 'locked@example.com', 'hash', 'active', ?, ?)`, userID, now.Unix(), now.Unix())
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES (?, 'Other User', 'other@example.com', 'hash', 'active', ?, ?)`, otherUserID, now.Unix(), now.Unix())
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES (?, 'Expired User', 'expired@example.com', 'hash', 'active', ?, ?)`, expiredUserID, now.Unix(), now.Unix())
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, workspaceID, userID, now.Unix())
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'USER', ?)`, workspaceID, otherUserID, now.Unix())
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'USER', ?)`, workspaceID, expiredUserID, now.Unix())
	mustExec(t, db, `INSERT INTO auth_lockouts (user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at) VALUES (?, 8, 0, ?, ?, ?, 'password', ?)`, userID, now.Unix()-60, now.Unix(), now.Add(15*time.Minute).Unix(), now.Unix())
	mustExec(t, db, `INSERT INTO auth_lockouts (user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at) VALUES (?, 0, 5, ?, ?, ?, 'totp', ?)`, otherUserID, now.Unix()-60, now.Unix(), now.Add(10*time.Minute).Unix(), now.Unix())
	mustExec(t, db, `INSERT INTO auth_lockouts (user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at) VALUES (?, 8, 0, ?, ?, ?, 'password', ?)`, expiredUserID, now.Unix()-1800, now.Unix()-1200, now.Add(-time.Minute).Unix(), now.Unix()-1200)

	active, err := ListAuthLockouts(db, false, now)
	if err != nil {
		t.Fatalf("list active lockouts: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active lockouts = %d, want 2", len(active))
	}

	all, err := ListAuthLockouts(db, true, now)
	if err != nil {
		t.Fatalf("list all lockouts: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all lockouts = %d, want 3", len(all))
	}

	result, err := UnlockAuthLockoutByEmail(db, "LOCKED@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("unlock by email: %v", err)
	}
	if !result.Removed || result.UserID != userID || result.Email != "locked@example.com" {
		t.Fatalf("unexpected unlock by email result: %+v", result)
	}
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, userID, 0)

	result, err = UnlockAuthLockoutByUserID(db, otherUserID)
	if err != nil {
		t.Fatalf("unlock by user id: %v", err)
	}
	if !result.Removed || result.Email != "other@example.com" {
		t.Fatalf("unexpected unlock by user id result: %+v", result)
	}
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, otherUserID, 0)

	cleared, err := ClearExpiredAuthLockouts(db, now)
	if err != nil {
		t.Fatalf("clear expired lockouts: %v", err)
	}
	if cleared.Removed != 1 {
		t.Fatalf("cleared expired = %d, want 1", cleared.Removed)
	}
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, expiredUserID, 0)

	if _, err := UnlockAuthLockoutByEmail(db, "missing@example.com"); err != ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestDisableAdmin2FA(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	workspaceID := uuid.NewString()
	adminID := uuid.NewString()
	managerID := uuid.NewString()
	userID := uuid.NewString()
	now := time.Now().Unix()
	mustExec(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES (?, 'Test Workspace', 'business', ?, ?)`, workspaceID, now, now)
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at, created_at, updated_at)
		VALUES (?, 'Admin', 'admin2fa@example.com', 'oldhash', 'active', 1, 'v1:encrypted-secret', '[{"hash":"backup"}]', ?, ?, ?)`, adminID, now, now, now)
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, workspaceID, adminID, now)
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at, created_at, updated_at)
		VALUES (?, 'Manager', 'manager2fa@example.com', 'oldhash', 'active', 1, 'v1:encrypted-secret', '[{"hash":"backup"}]', ?, ?, ?)`, managerID, now, now, now)
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'MANAGER', ?)`, workspaceID, managerID, now)
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at, created_at, updated_at)
		VALUES (?, 'User', 'regular2fa@example.com', 'oldhash', 'active', 1, 'v1:encrypted-secret', '[{"hash":"backup"}]', ?, ?, ?)`, userID, now, now, now)
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'USER', ?)`, workspaceID, userID, now)
	mustExec(t, db, `INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, is_remember, created_at) VALUES (?, ?, ?, 'session-hash', ?, 1, ?)`, uuid.NewString(), adminID, workspaceID, time.Now().Add(24*time.Hour).Unix(), now)
	mustExec(t, db, `INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at, remember_me, created_at) VALUES (?, ?, 'preauth-hash', 'TOTP', ?, 1, ?)`, uuid.NewString(), adminID, time.Now().Add(5*time.Minute).Unix(), now)
	mustExec(t, db, `INSERT INTO auth_lockouts (user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at) VALUES (?, 0, 5, ?, ?, ?, 'totp', ?)`, adminID, now, now, now+600, now)
	seedAdminCLIAuthState(t, db, workspaceID, managerID)
	seedAdminCLIAuthState(t, db, workspaceID, userID)

	result, err := DisableAdmin2FA(db, "ADMIN2FA@EXAMPLE.COM")
	if err != nil {
		t.Fatalf("disable admin 2fa: %v", err)
	}
	if result.Email != "admin2fa@example.com" || !result.WasEnabled {
		t.Fatalf("unexpected result: %+v", result)
	}

	var enabled int
	var secret sql.NullString
	var codes string
	var enabledAt sql.NullInt64
	if err := db.QueryRow(`SELECT totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at FROM users WHERE id = ?`, adminID).Scan(&enabled, &secret, &codes, &enabledAt); err != nil {
		t.Fatalf("query 2fa fields: %v", err)
	}
	if enabled != 0 || secret.Valid || codes != "[]" || enabledAt.Valid {
		t.Fatalf("2FA fields not cleared: enabled=%d secret_valid=%v codes=%q enabled_at_valid=%v", enabled, secret.Valid, codes, enabledAt.Valid)
	}
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NOT NULL`, adminID, 1)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NOT NULL`, adminID, 1)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, adminID, 0)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_audit_events WHERE user_id = ? AND event_type = 'USER_2FA_DISABLED_LOCAL_RECOVERY'`, adminID, 1)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND role = 'ADMIN'`, adminID, 1)

	if _, err := DisableUser2FA(db, "manager2fa@example.com"); err != nil {
		t.Fatalf("disable manager 2fa: %v", err)
	}
	assertAdminCLI2FADisabled(t, db, managerID)
	assertAdminCLIAuthStateRevoked(t, db, managerID)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND role = 'MANAGER'`, managerID, 1)

	if _, err := DisableUser2FA(db, "regular2fa@example.com"); err != nil {
		t.Fatalf("disable user 2fa: %v", err)
	}
	assertAdminCLI2FADisabled(t, db, userID)
	assertAdminCLIAuthStateRevoked(t, db, userID)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND role = 'USER'`, userID, 1)

	if _, err := DisableAdmin2FA(db, "missing@example.com"); err != ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestRepairOrphanCreditCardsDetectsNone(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)

	result, err := RepairOrphanCreditCards(db, true)
	if err != nil {
		t.Fatalf("RepairOrphanCreditCards dry-run: %v", err)
	}
	if result.Diagnosed != 0 {
		t.Fatalf("expected 0 orphans, got %d", result.Diagnosed)
	}
}

func TestRepairOrphanCreditCardsDryRunDoesNotAlterDB(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)
	createOrphanCard(t, db, "orphan-3", "Cartao Orfao 3", "ws-1")

	_, err = RepairOrphanCreditCards(db, true)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	var countAfterDryRun int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_cards WHERE account_id = 'orphan-3'`).Scan(&countAfterDryRun); err != nil {
		t.Fatalf("count after dry-run: %v", err)
	}
	if countAfterDryRun != 0 {
		t.Fatalf("dry-run should NOT create credit_cards rows, but found %d", countAfterDryRun)
	}

	var totalCards int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_cards`).Scan(&totalCards); err != nil {
		t.Fatalf("count all credit_cards: %v", err)
	}
	if totalCards != 1 {
		t.Fatalf("expected 1 pre-existing credit_cards row, got %d", totalCards)
	}
}

func TestRepairOrphanCreditCardsRepairsOne(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)

	createOrphanCard(t, db, "orphan-card-1", "Cartao Orfao", "ws-1")

	result, err := RepairOrphanCreditCards(db, true)
	if err != nil {
		t.Fatalf("RepairOrphanCreditCards dry-run: %v", err)
	}
	if result.Diagnosed != 1 {
		t.Fatalf("dry-run: expected 1 orphan, got %d", result.Diagnosed)
	}
	if result.Repaired != 0 {
		t.Fatalf("dry-run: expected 0 repaired, got %d", result.Repaired)
	}

	result, err = RepairOrphanCreditCards(db, false)
	if err != nil {
		t.Fatalf("RepairOrphanCreditCards repair: %v", err)
	}
	if result.Diagnosed != 1 {
		t.Fatalf("repair: expected 1 orphan, got %d", result.Diagnosed)
	}
	if result.Repaired != 1 {
		t.Fatalf("repair: expected 1 repaired, got %d", result.Repaired)
	}

	assertCreditCardExists(t, db, "orphan-card-1", 20, 10, 0)
}

func TestRepairOrphanCreditCardsIsIdempotent(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)
	createOrphanCard(t, db, "orphan-card-2", "Cartao Orfao 2", "ws-1")

	_, err = RepairOrphanCreditCards(db, false)
	if err != nil {
		t.Fatalf("first repair: %v", err)
	}

	result, err := RepairOrphanCreditCards(db, true)
	if err != nil {
		t.Fatalf("second run dry-run: %v", err)
	}
	if result.Diagnosed != 0 {
		t.Fatalf("expected 0 orphans after repair, got %d", result.Diagnosed)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM credit_cards WHERE account_id = 'orphan-card-2'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 credit_cards row, got %d (possible duplicate)", count)
	}
}

func TestRepairOrphanCreditCardsDoesNotAffectValidCard(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)

	_, err = RepairOrphanCreditCards(db, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}

	assertCreditCardExists(t, db, "card-1", 25, 5, 500000)
}

func TestRepairOrphanCreditCardsHandlesMultipleWorkspaces(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)

	now := time.Now().Unix()
	mustExec(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-2', 'Workspace 2', 'personal', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('orphan-ws2', 'ws-2', 'Cartao WS2', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-ws2', 'ws-2', 'Cartao Valido WS2', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-ws2', 'card-ws2', 15, 7, 100000)`)

	result, err := RepairOrphanCreditCards(db, false)
	if err != nil {
		t.Fatalf("repair multi-workspace: %v", err)
	}
	if result.Diagnosed != 1 {
		t.Fatalf("expected 1 orphan across workspaces, got %d", result.Diagnosed)
	}
	if result.Repaired != 1 {
		t.Fatalf("expected 1 repaired, got %d", result.Repaired)
	}

	assertCreditCardExists(t, db, "orphan-ws2", 20, 10, 0)
	assertCreditCardExists(t, db, "card-ws2", 15, 7, 100000)
}

func TestRepairOrphanCreditCardsDoesNotAlterOtherTables(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	seedOrphanRepairScenario(t, db)
	createOrphanCard(t, db, "orphan-4", "Cartao Orfao 4", "ws-1")

	var balanceBefore, balanceAfter int64
	db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-1'`).Scan(&balanceBefore)

	_, err = RepairOrphanCreditCards(db, false)
	if err != nil {
		t.Fatalf("repair: %v", err)
	}

	db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-1'`).Scan(&balanceAfter)
	if balanceAfter != balanceBefore {
		t.Fatalf("balance changed from %d to %d", balanceBefore, balanceAfter)
	}

	var accType string
	db.QueryRow(`SELECT type FROM accounts WHERE id = 'orphan-4'`).Scan(&accType)
	if accType != "CREDIT_CARD" {
		t.Fatalf("account type changed to %s", accType)
	}

	var invoiceCount int
	db.QueryRow(`SELECT COUNT(1) FROM invoices`).Scan(&invoiceCount)
	if invoiceCount != 0 {
		t.Fatalf("unexpected invoices created: %d", invoiceCount)
	}

	var txCount int
	db.QueryRow(`SELECT COUNT(1) FROM transactions`).Scan(&txCount)
	if txCount != 0 {
		t.Fatalf("unexpected transactions created: %d", txCount)
	}
}

func assertCreditCardExists(t *testing.T, db *sql.DB, accountID string, closingDay, dueDay, creditLimit int64) {
	t.Helper()
	var gotClosing, gotDue, gotLimit int64
	if err := db.QueryRow(`
		SELECT closing_day, due_day, credit_limit FROM credit_cards WHERE account_id = ?
	`, accountID).Scan(&gotClosing, &gotDue, &gotLimit); err != nil {
		t.Fatalf("credit card query for %s: %v", accountID, err)
	}
	if gotClosing != closingDay {
		t.Fatalf("%s closing_day = %d, want %d", accountID, gotClosing, closingDay)
	}
	if gotDue != dueDay {
		t.Fatalf("%s due_day = %d, want %d", accountID, gotDue, dueDay)
	}
	if gotLimit != creditLimit {
		t.Fatalf("%s credit_limit = %d, want %d", accountID, gotLimit, creditLimit)
	}
}

func createOrphanCard(t *testing.T, db *sql.DB, accountID, name, workspaceID string) {
	t.Helper()
	now := time.Now().Unix()
	_, err := db.Exec(`
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, ?, 'CREDIT_CARD', 0, 0, ?, ?)
	`, accountID, workspaceID, name, now, now)
	if err != nil {
		t.Fatalf("create orphan card %s: %v", name, err)
	}
}

func seedOrphanRepairScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	mustExec(t, db, `INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at) VALUES ('user-1', 'User', 'u@e.com', 'h', 'active', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-1', 'Workspace', 'personal', ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-1', 'user-1', 'ADMIN', ?)`, now)
	mustExec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-1', 'ws-1', 'Cartao Valido', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	mustExec(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-1', 'card-1', 25, 5, 500000)`)
	mustExec(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('checking-1', 'ws-1', 'Conta Corrente', 'CHECKING', 0, 0, ?, ?)`, now, now)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec failed: %v\nquery: %s", err, query)
	}
}

func assertAdminCLICount(t *testing.T, db *sql.DB, query string, arg any, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query, arg).Scan(&got); err != nil {
		t.Fatalf("count query failed: %v\nquery: %s", err, query)
	}
	if got != want {
		t.Fatalf("count = %d, want %d\nquery: %s", got, want, query)
	}
}

func seedAdminCLIAuthState(t *testing.T, db *sql.DB, workspaceID, userID string) {
	t.Helper()
	now := time.Now().Unix()
	mustExec(t, db, `INSERT INTO sessions (id, user_id, workspace_id, token_hash, expires_at, is_remember, created_at) VALUES (?, ?, ?, ?, ?, 1, ?)`, uuid.NewString(), userID, workspaceID, "session-"+userID, now+3600, now)
	mustExec(t, db, `INSERT INTO pre_auth_sessions (id, user_id, token_hash, method, expires_at, remember_me, created_at) VALUES (?, ?, ?, 'TOTP', ?, 1, ?)`, uuid.NewString(), userID, "preauth-"+userID, now+300, now)
	mustExec(t, db, `INSERT OR REPLACE INTO auth_lockouts (user_id, failed_password_count, failed_2fa_count, first_failed_at, last_failed_at, locked_until, lock_reason, updated_at) VALUES (?, 0, 4, ?, ?, ?, 'totp', ?)`, userID, now, now, now+600, now)
}

func assertAdminCLIAuthStateRevoked(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, userID, 0)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM pre_auth_sessions WHERE user_id = ? AND consumed_at IS NULL`, userID, 0)
	assertAdminCLICount(t, db, `SELECT COUNT(1) FROM auth_lockouts WHERE user_id = ?`, userID, 0)
}

func assertAdminCLI2FADisabled(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	var enabled int
	var secret sql.NullString
	var codes string
	var enabledAt sql.NullInt64
	if err := db.QueryRow(`SELECT totp_enabled, totp_secret_enc, totp_backup_codes, totp_enabled_at FROM users WHERE id = ?`, userID).Scan(&enabled, &secret, &codes, &enabledAt); err != nil {
		t.Fatalf("query 2fa fields: %v", err)
	}
	if enabled != 0 || secret.Valid || codes != "[]" || enabledAt.Valid {
		t.Fatalf("2FA fields not cleared: enabled=%d secret_valid=%v codes=%q enabled_at_valid=%v", enabled, secret.Valid, codes, enabledAt.Valid)
	}
}

func assertAdminCLIPassword(t *testing.T, db *sql.DB, userID, password string) {
	t.Helper()
	var hash string
	if err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		t.Fatalf("query password hash: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		t.Fatalf("password hash mismatch: %v", err)
	}
}

func assertAdminCLIAuthentication(t *testing.T, db *sql.DB, email, userID, oldPassword, newPassword string) {
	t.Helper()
	authService := auth.NewService(db)
	if _, err := authService.Authenticate(email, oldPassword); err != auth.ErrInvalidCredentials {
		t.Fatalf("old password auth error = %v, want ErrInvalidCredentials", err)
	}
	gotUserID, err := authService.Authenticate(email, newPassword)
	if err != nil {
		t.Fatalf("new password auth failed: %v", err)
	}
	if gotUserID != userID {
		t.Fatalf("authenticated user = %s, want %s", gotUserID, userID)
	}
}

func assertAdminCLITemporaryPasswordFlags(t *testing.T, db *sql.DB, userID string, expectedExpiresAt int64) {
	t.Helper()
	var mustChange int
	var expiresAt sql.NullInt64
	if err := db.QueryRow(`SELECT must_change_password, temporary_password_expires_at FROM users WHERE id = ?`, userID).Scan(&mustChange, &expiresAt); err != nil {
		t.Fatalf("query temporary password flags: %v", err)
	}
	if mustChange != 1 {
		t.Fatalf("must_change_password = %d, want 1", mustChange)
	}
	if !expiresAt.Valid || expiresAt.Int64 != expectedExpiresAt {
		t.Fatalf("temporary_password_expires_at = %v, want %d", expiresAt, expectedExpiresAt)
	}
	if expiresAt.Int64 <= time.Now().Unix() {
		t.Fatalf("temporary password expiry is not in the future: %d", expiresAt.Int64)
	}
}

func assertAdminCLIAuditDoesNotContain(t *testing.T, db *sql.DB, userID, secret string) {
	t.Helper()
	var metadata string
	if err := db.QueryRow(`SELECT COALESCE(metadata_json, '') FROM auth_audit_events WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`, userID).Scan(&metadata); err != nil {
		t.Fatalf("query audit metadata: %v", err)
	}
	if strings.Contains(metadata, secret) {
		t.Fatalf("audit metadata contains temporary password")
	}
}
