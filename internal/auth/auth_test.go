package auth

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func TestAuthenticateAndSessionLifecycle(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	userID := uuid.NewString()
	workspaceID := uuid.NewString()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, "User Test", "user@test.local", string(hash), now, now)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		workspaceID, "WS", now, now)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`,
		workspaceID, userID, now)
	if err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	svc := NewService(db)

	gotUserID, err := svc.Authenticate("user@test.local", "secret123")
	if err != nil {
		t.Fatalf("authenticate valid credentials: %v", err)
	}
	if gotUserID != userID {
		t.Fatalf("expected userID %s, got %s", userID, gotUserID)
	}

	if _, err := svc.Authenticate("user@test.local", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials for wrong password, got %v", err)
	}

	token, _, err := svc.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	resolvedUserID, resolvedWorkspaceID, _, _, err := svc.ResolveAndRefreshSession(token)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if resolvedUserID != userID {
		t.Fatalf("expected resolved userID %s, got %s", userID, resolvedUserID)
	}
	if resolvedWorkspaceID != workspaceID {
		t.Fatalf("expected resolved workspaceID %s, got %s", workspaceID, resolvedWorkspaceID)
	}

	defaultWorkspaceID, role, err := svc.ResolveWorkspaceMembership(userID)
	if err != nil {
		t.Fatalf("resolve workspace: %v", err)
	}
	if defaultWorkspaceID != workspaceID {
		t.Fatalf("expected workspaceID %s, got %s", workspaceID, defaultWorkspaceID)
	}
	if role != "ADMIN" {
		t.Fatalf("expected role ADMIN, got %s", role)
	}

	if err := svc.RevokeSession(token); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, _, _, _, err := svc.ResolveAndRefreshSession(token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows after revoke, got %v", err)
	}

	// Test session created with is_remember=true
	tokenRemember, _, err := svc.CreateSession(userID, workspaceID, time.Hour, true)
	if err != nil {
		t.Fatalf("create session remember: %v", err)
	}
	var isRemember int
	err = db.QueryRow("SELECT is_remember FROM sessions WHERE token_hash = ?", hashToken(tokenRemember)).Scan(&isRemember)
	if err != nil {
		t.Fatalf("query is_remember: %v", err)
	}
	if isRemember != 1 {
		t.Fatalf("expected is_remember=1, got %d", isRemember)
	}

	// Test pre_auth_session preserves remember_me
	preToken, _, err := svc.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, true)
	if err != nil {
		t.Fatalf("create pre auth session: %v", err)
	}
	_, resolvedPreUserID, rememberMeFlag, err := svc.ResolvePreAuthSession(preToken)
	if err != nil {
		t.Fatalf("resolve pre auth session: %v", err)
	}
	if resolvedPreUserID != userID {
		t.Fatalf("expected resolved pre auth userID %s, got %s", userID, resolvedPreUserID)
	}
	if !rememberMeFlag {
		t.Fatalf("expected rememberMeFlag=true")
	}

	// Test pre_auth_session with remember_me=false
	preTokenFalse, _, err := svc.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, false)
	if err != nil {
		t.Fatalf("create pre auth session false: %v", err)
	}
	_, _, rememberMeFlagFalse, err := svc.ResolvePreAuthSession(preTokenFalse)
	if err != nil {
		t.Fatalf("resolve pre auth session false: %v", err)
	}
	if rememberMeFlagFalse {
		t.Fatalf("expected rememberMeFlag=false")
	}
}

func TestCleanupExpiredSessions(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	userID := uuid.NewString()
	workspaceID := uuid.NewString()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, "User Cleanup", "cleanup@test.local", string(hash), now, now)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		workspaceID, "WS Cleanup", now, now)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	_, err = db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`,
		workspaceID, userID, now)
	if err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	svc := NewService(db)
	nowTime := time.Now()

	t.Run("remove_expired_sessions", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		db.Exec(`UPDATE sessions SET expires_at = ? WHERE token_hash = ?`, nowTime.Unix()-3600, hashToken(token))
		sessionsDeleted, _, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if sessionsDeleted < 1 {
			t.Fatalf("expected at least 1 expired session deleted, got %d", sessionsDeleted)
		}
	})

	t.Run("preserve_valid_sessions", func(t *testing.T) {
		_, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		sessionsDeleted, _, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if sessionsDeleted != 0 {
			t.Fatalf("expected 0 valid sessions deleted, got %d", sessionsDeleted)
		}
	})

	t.Run("preserve_valid_remember_sessions", func(t *testing.T) {
		_, _, err := svc.CreateSession(userID, workspaceID, 30*24*time.Hour, true)
		if err != nil {
			t.Fatal(err)
		}
		sessionsDeleted, _, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if sessionsDeleted != 0 {
			t.Fatalf("expected 0 valid remember sessions deleted, got %d", sessionsDeleted)
		}
	})

	t.Run("remove_expired_pre_auth_sessions", func(t *testing.T) {
		preToken, _, err := svc.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, false)
		if err != nil {
			t.Fatal(err)
		}
		db.Exec(`UPDATE pre_auth_sessions SET expires_at = ? WHERE token_hash = ?`, nowTime.Unix()-300, hashToken(preToken))
		_, preAuthDeleted, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if preAuthDeleted < 1 {
			t.Fatalf("expected at least 1 expired pre_auth deleted, got %d", preAuthDeleted)
		}
	})

	t.Run("preserve_valid_pre_auth_sessions", func(t *testing.T) {
		_, _, err := svc.CreatePreAuthSession(userID, "TOTP", 5*time.Minute, false)
		if err != nil {
			t.Fatal(err)
		}
		_, preAuthDeleted, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if preAuthDeleted != 0 {
			t.Fatalf("expected 0 valid pre_auth deleted, got %d", preAuthDeleted)
		}
	})

	t.Run("cleanup_is_idempotent", func(t *testing.T) {
		s1, p1, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		s2, p2, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if s2 > 0 || p2 > 0 {
			t.Fatalf("expected idempotent cleanup (0,0), got (%d,%d) on second call after (%d,%d)", s2, p2, s1, p1)
		}
	})

	t.Run("recently_revoked_session_preserved", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		svc.RevokeSession(token)
		sessionsDeleted, _, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if sessionsDeleted != 0 {
			t.Fatalf("expected 0 recently revoked sessions deleted, got %d", sessionsDeleted)
		}
	})

	t.Run("old_revoked_session_removed", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		db.Exec(`UPDATE sessions SET expires_at = ?, revoked_at = ? WHERE token_hash = ?`,
			nowTime.Unix()-8*86400, nowTime.Unix()-8*86400, hashToken(token))
		sessionsDeleted, _, err := svc.CleanupExpiredSessions(nowTime)
		if err != nil {
			t.Fatal(err)
		}
		if sessionsDeleted < 1 {
			t.Fatalf("expected at least 1 old revoked session deleted, got %d", sessionsDeleted)
		}
	})
}
