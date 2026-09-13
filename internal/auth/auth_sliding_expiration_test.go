package auth_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

func setupTestDB(t *testing.T) (*sql.DB, *auth.Service, string, string) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	userID := uuid.NewString()
	workspaceID := uuid.NewString()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	now := time.Now().Unix()
	db.Exec(`INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, "U", "u@u", string(hash), now, now)
	db.Exec(`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		workspaceID, "W", now, now)
	db.Exec(`INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`,
		workspaceID, userID, now)
	return db, auth.NewService(db), userID, workspaceID
}

func TestResolveAndRefreshSession(t *testing.T) {
	db, svc, userID, workspaceID := setupTestDB(t)
	defer db.Close()

	t.Run("padrao_antes_do_limiar", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		// TTL é 24h. Limiar é 12h. A sessão foi recém-criada, faltam 24h (maior que 12h).
		_, _, newMaxAge, refreshed, err := svc.ResolveAndRefreshSession(token)
		if err != nil {
			t.Fatal(err)
		}
		if refreshed {
			t.Fatal("expected not to refresh")
		}
		if newMaxAge != 0 {
			t.Fatal("expected newMaxAge = 0")
		}
	})

	t.Run("padrao_depois_do_limiar", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		// Força a expiração para faltar 11h (menor que limiar de 12h)
		now := time.Now().Unix()
		db.Exec(`UPDATE sessions SET expires_at = ?`, now+11*3600)

		_, _, newMaxAge, refreshed, err := svc.ResolveAndRefreshSession(token)
		if err != nil {
			t.Fatal(err)
		}
		if !refreshed {
			t.Fatal("expected to refresh")
		}
		if newMaxAge != 24*3600 {
			t.Fatalf("expected newMaxAge = 24h, got %d", newMaxAge)
		}
	})

	t.Run("remember_antes_do_limiar", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 30*24*time.Hour, true)
		if err != nil {
			t.Fatal(err)
		}
		// TTL é 30d. Limiar é 15d. Sessão criada faltam 30d (maior que 15d).
		_, _, newMaxAge, refreshed, err := svc.ResolveAndRefreshSession(token)
		if err != nil {
			t.Fatal(err)
		}
		if refreshed {
			t.Fatal("expected not to refresh")
		}
		if newMaxAge != 0 {
			t.Fatal("expected newMaxAge = 0")
		}
	})

	t.Run("remember_depois_do_limiar", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 30*24*time.Hour, true)
		if err != nil {
			t.Fatal(err)
		}
		// Força expiração para faltar 14d (menor que 15d)
		now := time.Now().Unix()
		db.Exec(`UPDATE sessions SET expires_at = ?`, now+14*24*3600)

		_, _, newMaxAge, refreshed, err := svc.ResolveAndRefreshSession(token)
		if err != nil {
			t.Fatal(err)
		}
		if !refreshed {
			t.Fatal("expected to refresh")
		}
		if newMaxAge != 30*24*3600 {
			t.Fatalf("expected newMaxAge = 30d, got %d", newMaxAge)
		}
	})

	t.Run("revogada_nao_renova", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		svc.RevokeSession(token)

		_, _, _, refreshed, err := svc.ResolveAndRefreshSession(token)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if refreshed {
			t.Fatal("expected not to refresh")
		}
	})

	t.Run("expirada_nao_renova", func(t *testing.T) {
		token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		// Força expiração no passado
		db.Exec(`UPDATE sessions SET expires_at = ?`, now-3600)

		_, _, _, refreshed, err := svc.ResolveAndRefreshSession(token)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if refreshed {
			t.Fatal("expected not to refresh")
		}
	})
}

func TestResolveAndRefreshSessionEnforcesAbsoluteLifetime(t *testing.T) {
	tests := []struct {
		name             string
		remember         bool
		initialTTL       time.Duration
		absoluteLifetime time.Duration
	}{
		{"standard", false, 24 * time.Hour, 7 * 24 * time.Hour},
		{"remember", true, 30 * 24 * time.Hour, 90 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, svc, userID, workspaceID := setupTestDB(t)
			defer db.Close()
			token, _, err := svc.CreateSession(userID, workspaceID, tc.initialTTL, tc.remember)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().Unix()
			if _, err := db.Exec(`UPDATE sessions SET created_at = ?, expires_at = ?`, now-int64(tc.absoluteLifetime/time.Second)-1, now+int64(tc.initialTTL/time.Second)); err != nil {
				t.Fatalf("age session: %v", err)
			}
			if _, _, _, refreshed, err := svc.ResolveAndRefreshSession(token); !errors.Is(err, sql.ErrNoRows) || refreshed {
				t.Fatalf("absolute-expired session: refreshed=%v err=%v", refreshed, err)
			}
		})
	}
}

func TestResolveAndRefreshSessionCapsRefreshAtAbsoluteLifetime(t *testing.T) {
	db, svc, userID, workspaceID := setupTestDB(t)
	defer db.Close()
	token, _, err := svc.CreateSession(userID, workspaceID, 24*time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	absoluteExpiresAt := now + int64((6*time.Hour)/time.Second)
	createdAt := absoluteExpiresAt - int64((7*24*time.Hour)/time.Second)
	if _, err := db.Exec(`UPDATE sessions SET created_at = ?, expires_at = ?`, createdAt, now+11*3600); err != nil {
		t.Fatalf("prepare near-absolute session: %v", err)
	}

	_, _, maxAge, refreshed, err := svc.ResolveAndRefreshSession(token)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("expected over-limit expiration to be capped")
	}
	if maxAge < int((5*time.Hour+59*time.Minute)/time.Second) || maxAge > int((6*time.Hour)/time.Second) {
		t.Fatalf("capped max age = %d, want approximately 6h", maxAge)
	}
	var storedExpiresAt int64
	if err := db.QueryRow(`SELECT expires_at FROM sessions`).Scan(&storedExpiresAt); err != nil {
		t.Fatalf("load capped expiration: %v", err)
	}
	if storedExpiresAt != absoluteExpiresAt {
		t.Fatalf("stored expiration = %d, want absolute cap %d", storedExpiresAt, absoluteExpiresAt)
	}
	if _, _, secondMaxAge, secondRefresh, err := svc.ResolveAndRefreshSession(token); err != nil || secondRefresh || secondMaxAge != 0 {
		t.Fatalf("capped session rewrote again: maxAge=%d refreshed=%v err=%v", secondMaxAge, secondRefresh, err)
	}
}

func TestCreateSessionCapsExcessiveRequestedTTL(t *testing.T) {
	tests := []struct {
		name       string
		remember   bool
		requested  time.Duration
		maximumAge time.Duration
	}{
		{"standard", false, 30 * 24 * time.Hour, 7 * 24 * time.Hour},
		{"remember", true, 365 * 24 * time.Hour, 90 * 24 * time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, svc, userID, workspaceID := setupTestDB(t)
			defer db.Close()
			startedAt := time.Now()
			_, expiresAt, err := svc.CreateSession(userID, workspaceID, tc.requested, tc.remember)
			if err != nil {
				t.Fatal(err)
			}
			age := expiresAt.Sub(startedAt)
			if age < tc.maximumAge-time.Second || age > tc.maximumAge+time.Second {
				t.Fatalf("created lifetime = %s, want %s", age, tc.maximumAge)
			}
		})
	}
}
