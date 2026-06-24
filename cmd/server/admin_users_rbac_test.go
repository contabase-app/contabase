package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
)

func TestAdminUserGlobalRoutesRequireAdmin(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{7}, 32), ttl: time.Hour}
	handler := newAdminUsersRBACRouteTestHandler(authService, signer)

	sessions := map[string]string{
		models.RoleAdmin:   createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin),
		models.RoleManager: createAdminUsersRBACSession(t, authService, "manager-user", models.RoleManager),
		models.RoleUser:    createAdminUsersRBACSession(t, authService, "regular-user", models.RoleUser),
	}

	cases := []struct {
		name   string
		method string
		path   string
		role   string
		want   int
	}{
		{"admin lists users", http.MethodGet, "/admin/usuarios", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot list users", http.MethodGet, "/admin/usuarios", models.RoleManager, http.StatusForbidden},
		{"user cannot list users", http.MethodGet, "/admin/usuarios", models.RoleUser, http.StatusForbidden},
		{"admin saves users", http.MethodPost, "/admin/usuarios/salvar", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot save users", http.MethodPost, "/admin/usuarios/salvar", models.RoleManager, http.StatusForbidden},
		{"user cannot save users", http.MethodPost, "/admin/usuarios/salvar", models.RoleUser, http.StatusForbidden},
		{"admin resets password", http.MethodPost, "/admin/usuarios/reset-senha", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot reset password", http.MethodPost, "/admin/usuarios/reset-senha", models.RoleManager, http.StatusForbidden},
		{"user cannot reset password", http.MethodPost, "/admin/usuarios/reset-senha", models.RoleUser, http.StatusForbidden},
		{"admin disables user 2fa", http.MethodPost, "/admin/usuarios/desativar-2fa", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot disable user 2fa", http.MethodPost, "/admin/usuarios/desativar-2fa", models.RoleManager, http.StatusForbidden},
		{"user cannot disable user 2fa", http.MethodPost, "/admin/usuarios/desativar-2fa", models.RoleUser, http.StatusForbidden},
		{"admin revokes user sessions", http.MethodPost, "/admin/usuarios/revogar-sessoes", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot revoke user sessions", http.MethodPost, "/admin/usuarios/revogar-sessoes", models.RoleManager, http.StatusForbidden},
		{"user cannot revoke user sessions", http.MethodPost, "/admin/usuarios/revogar-sessoes", models.RoleUser, http.StatusForbidden},
		{"admin deletes user", http.MethodDelete, "/users/target-user", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot delete user", http.MethodDelete, "/users/target-user", models.RoleManager, http.StatusForbidden},
		{"user cannot delete user", http.MethodDelete, "/users/target-user", models.RoleUser, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[tc.role]})
			if tc.method == http.MethodPost || tc.method == http.MethodDelete {
				addAdminUsersRBACCSRF(t, req, signer)
			}
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func newAdminUsersRBACRouteTestHandler(authService *auth.Service, signer *csrfSigner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/usuarios", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/admin/usuarios/salvar", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/admin/usuarios/reset-senha", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/admin/usuarios/desativar-2fa", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/admin/usuarios/revogar-sessoes", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/users/target-user", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func openAdminUsersRBACTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES
			('admin-user', 'Admin User', 'admin@example.com', 'hash', 'active', ?, ?),
			('manager-user', 'Manager User', 'manager@example.com', 'hash', 'active', ?, ?),
			('regular-user', 'Regular User', 'user@example.com', 'hash', 'active', ?, ?)
	`, now, now, now, now, now, now); err != nil {
		_ = db.Close()
		t.Fatalf("seed users: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('workspace-rbac', 'Workspace RBAC', '', 'personal', ?, ?)
	`, now, now); err != nil {
		_ = db.Close()
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('workspace-rbac', 'admin-user', 'ADMIN', ?),
			('workspace-rbac', 'manager-user', 'MANAGER', ?),
			('workspace-rbac', 'regular-user', 'USER', ?)
	`, now, now, now); err != nil {
		_ = db.Close()
		t.Fatalf("seed workspace members: %v", err)
	}
	return db
}

func createAdminUsersRBACSession(t *testing.T, authService *auth.Service, userID, role string) string {
	t.Helper()
	token, _, err := authService.CreateSession(userID, "workspace-rbac", time.Hour, false)
	if err != nil {
		t.Fatalf("create %s session: %v", role, err)
	}
	return token
}

func addAdminUsersRBACCSRF(t *testing.T, req *http.Request, signer *csrfSigner) {
	t.Helper()
	token := signer.issue(time.Now())
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	req.Header.Set(csrfHeaderName, token)
}
