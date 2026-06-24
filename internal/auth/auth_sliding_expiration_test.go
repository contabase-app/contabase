package auth_test

import (
	"database/sql"
	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"testing"
	"time"

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
