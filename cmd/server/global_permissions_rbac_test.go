package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/models"
)

func TestGlobalAdminRoutesIgnoreWorkspaceCustomPermissions(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`
		UPDATE workspace_members
		SET custom_permissions = ?
		WHERE workspace_id = 'workspace-rbac' AND user_id = 'manager-user'
	`, models.PermissionListToJSON([]string{
		models.PermissionBackupExport,
		models.PermissionAdminAuditRead,
		"members:write",
	})); err != nil {
		t.Fatalf("seed manager custom permissions: %v", err)
	}

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{9}, 32), ttl: time.Hour}
	handler := newGlobalAdminRouteTestHandler(authService, signer)
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
		{"admin can export backup", http.MethodGet, "/admin/backups/exportar", models.RoleAdmin, http.StatusNoContent},
		{"manager with backup export cannot export backup", http.MethodGet, "/admin/backups/exportar", models.RoleManager, http.StatusForbidden},
		{"user cannot export backup", http.MethodGet, "/admin/backups/exportar", models.RoleUser, http.StatusForbidden},
		{"admin can open backup import", http.MethodPost, "/admin/backups/importar", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot import backup", http.MethodPost, "/admin/backups/importar", models.RoleManager, http.StatusForbidden},
		{"user cannot import backup", http.MethodPost, "/admin/backups/importar", models.RoleUser, http.StatusForbidden},
		{"admin can list users", http.MethodGet, "/admin/usuarios", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot list users", http.MethodGet, "/admin/usuarios", models.RoleManager, http.StatusForbidden},
		{"user cannot list users", http.MethodGet, "/admin/usuarios", models.RoleUser, http.StatusForbidden},
		{"admin can list workspaces", http.MethodGet, "/admin/workspaces", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot list workspaces", http.MethodGet, "/admin/workspaces", models.RoleManager, http.StatusForbidden},
		{"user cannot list workspaces", http.MethodGet, "/admin/workspaces", models.RoleUser, http.StatusForbidden},
		{"admin can open backups", http.MethodGet, "/admin/backups", models.RoleAdmin, http.StatusNoContent},
		{"manager cannot open backups", http.MethodGet, "/admin/backups", models.RoleManager, http.StatusForbidden},
		{"user cannot open backups", http.MethodGet, "/admin/backups", models.RoleUser, http.StatusForbidden},
		{"admin can read audit", http.MethodGet, "/admin/auditoria", models.RoleAdmin, http.StatusNoContent},
		{"manager with audit read cannot read global audit", http.MethodGet, "/admin/auditoria", models.RoleManager, http.StatusForbidden},
		{"user cannot read audit", http.MethodGet, "/admin/auditoria", models.RoleUser, http.StatusForbidden},
		{"admin can save audit", http.MethodPost, "/admin/auditoria/salvar", models.RoleAdmin, http.StatusNoContent},
		{"manager with audit read cannot save audit", http.MethodPost, "/admin/auditoria/salvar", models.RoleManager, http.StatusForbidden},
		{"user cannot save audit", http.MethodPost, "/admin/auditoria/salvar", models.RoleUser, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[tc.role]})
			if tc.method == http.MethodPost {
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

func TestWorkspaceCustomPermissionsDoNotGrantRBACForMVP(t *testing.T) {
	member := &models.WorkspaceMember{
		Role: models.RoleUser,
		CustomPermissions: []string{
			models.PermissionReportsView,
			models.PermissionContactsDelete,
		},
	}
	if models.HasPermission(member, models.PermissionReportsView) {
		t.Fatalf("reports custom permission must not grant access while fixed role matrix is active")
	}
	if models.HasPermission(member, models.PermissionContactsDelete) {
		t.Fatalf("contacts custom permission must not grant access while fixed role matrix is active")
	}
	if isGlobalAdmin(authContext{Role: member.Role, Member: *member}) {
		t.Fatalf("workspace custom permissions must not grant global admin")
	}
}

func newGlobalAdminRouteTestHandler(authService *auth.Service, signer *csrfSigner) http.Handler {
	mux := http.NewServeMux()
	register := func(method, path string) {
		mux.HandleFunc(path, withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
			if r.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !requireGlobalAdmin(w, r, ctx) {
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	register(http.MethodGet, "/admin/usuarios")
	register(http.MethodGet, "/admin/workspaces")
	register(http.MethodGet, "/admin/backups")
	register(http.MethodGet, "/admin/backups/exportar")
	register(http.MethodPost, "/admin/backups/importar")
	register(http.MethodGet, "/admin/auditoria")
	register(http.MethodPost, "/admin/auditoria/salvar")
	return mux
}
