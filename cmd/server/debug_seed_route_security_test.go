package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
)

func TestSeedDemoB2BRouteRequiresPostCSRFAndDebugMode(t *testing.T) {
	tests := []struct {
		name      string
		debugMode string
		method    string
		csrf      bool
		wantCode  int
		wantRows  int
	}{
		{"get cannot mutate", "true", http.MethodGet, false, http.StatusMethodNotAllowed, 0},
		{"post requires csrf", "true", http.MethodPost, false, http.StatusForbidden, 0},
		{"production mode remains disabled", "false", http.MethodPost, true, http.StatusForbidden, 0},
		{"debug post with csrf applies seed", "true", http.MethodPost, true, http.StatusOK, 12},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_DEBUG", tc.debugMode)
			db := openDebugSeedRouteTestDB(t)
			defer db.Close()
			authService := auth.NewService(db)
			sessionToken, _, err := authService.CreateSession("seed-user", "seed-ws", time.Hour, false)
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			signer := &csrfSigner{secret: bytes.Repeat([]byte{9}, 32), ttl: time.Hour}
			handler := seedDemoB2BRoute(db, authService, signer, newMemoryRateLimiter(), rateLimitPolicy{Limit: 3, Window: 30 * time.Minute})

			req := httptest.NewRequest(tc.method, "/admin/seed-demo-b2b", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
			if tc.csrf {
				addAdminUsersRBACCSRF(t, req, signer)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tc.wantCode, rr.Body.String())
			}
			var rows int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'seed-ws'`).Scan(&rows); err != nil {
				t.Fatalf("count transactions: %v", err)
			}
			if rows != tc.wantRows {
				t.Fatalf("seeded transactions = %d, want %d", rows, tc.wantRows)
			}
		})
	}
}

func TestSeedDemoB2BRouteKeepsPerWorkspaceUserRateLimit(t *testing.T) {
	t.Setenv("APP_DEBUG", "true")
	db := openDebugSeedRouteTestDB(t)
	defer db.Close()
	authService := auth.NewService(db)
	sessionToken, _, err := authService.CreateSession("seed-user", "seed-ws", time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	signer := &csrfSigner{secret: bytes.Repeat([]byte{10}, 32), ttl: time.Hour}
	handler := seedDemoB2BRoute(db, authService, signer, newMemoryRateLimiter(), rateLimitPolicy{Limit: 1, Window: time.Hour})

	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/admin/seed-demo-b2b", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		addAdminUsersRBACCSRF(t, req, signer)
		return req
	}
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request())
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", first.Code, http.StatusOK)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response missing Retry-After")
	}
}

func openDebugSeedRouteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openRawServerFixture(t)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES ('seed-user', 'Seed User', 'seed@example.test', 'hash', 'active', ?, ?);
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('seed-ws', 'Seed Workspace', 'business', ?, ?);
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('seed-ws', 'seed-user', 'ADMIN', ?)
	`, now, now, now, now, now); err != nil {
		_ = db.Close()
		t.Fatalf("seed database: %v", err)
	}
	return db
}
