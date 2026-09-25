package main

import (
	"bytes"
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

type investmentsRBACProbe struct {
	method  string
	handler http.HandlerFunc
}

type investmentsRBACHarness struct {
	db        *sql.DB
	service   *auth.Service
	signer    *csrfSigner
	probes    []investmentsRBACProbe
	protected []authContext
}

func newInvestmentsRBACHarness(t *testing.T, db *sql.DB) *investmentsRBACHarness {
	t.Helper()
	if db == nil {
		db = openAdminUsersRBACTestDB(t)
		t.Cleanup(func() { _ = db.Close() })
	}
	h := &investmentsRBACHarness{
		db: db, service: auth.NewService(db),
		signer: &csrfSigner{secret: bytes.Repeat([]byte{31}, 32), ttl: time.Hour},
	}
	for _, operation := range []struct{ method, token string }{
		{http.MethodGet, models.PermissionInvestmentsRead},
		{http.MethodPost, models.PermissionInvestmentsWrite},
	} {
		// Test-only I3 contract: withAuth resolves membership (and checks CSRF
		// for writes); the permission guard must return before the service.
		// I3 must enforce this on real routes and retain tenant-aware SQL.
		h.probes = append(h.probes, investmentsRBACProbe{operation.method,
			withAuth(h.service, h.signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
				if r.Method != operation.method {
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
				if !models.HasPermission(&ctx.Member, operation.token) {
					respondForbidden(w, r)
					return
				}
				h.protected = append(h.protected, ctx)
				w.WriteHeader(http.StatusNoContent)
			}),
		})
	}
	return h
}

func (h *investmentsRBACHarness) session(t *testing.T, userID, workspaceID string) string {
	t.Helper()
	token, _, err := h.service.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return token
}

func (h *investmentsRBACHarness) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.db.Exec(query, args...); err != nil {
		t.Fatalf("fixture update: %v", err)
	}
}

func (h *investmentsRBACHarness) assertAccess(t *testing.T, session string, status int, want *models.WorkspaceMember) {
	t.Helper()
	for _, probe := range h.probes {
		before := len(h.protected)
		req := httptest.NewRequest(probe.method, "/test-only/investments-rbac", nil)
		if session != "" {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		}
		// Deliberately forged even for USER/MANAGER and after role changes.
		req.AddCookie(&http.Cookie{Name: "workspace_role", Value: models.RoleAdmin})
		if probe.method == http.MethodPost {
			addAdminUsersRBACCSRF(t, req, h.signer)
		}
		rr := httptest.NewRecorder()
		probe.handler.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s status = %d, want %d; body=%q", probe.method, rr.Code, status, rr.Body.String())
		}
		if want == nil {
			if len(h.protected) != before {
				t.Fatalf("%s denied request executed protected callback", probe.method)
			}
			if status == http.StatusSeeOther && rr.Header().Get("Location") != "/login" {
				t.Fatalf("unauthenticated request redirected to %q", rr.Header().Get("Location"))
			}
			continue
		}
		if len(h.protected) != before+1 {
			t.Fatalf("%s authorized request did not execute exactly one callback", probe.method)
		}
		got := h.protected[before]
		if got.UserID != want.UserID || got.ActiveWorkspaceID != want.WorkspaceID || got.Role != want.Role ||
			got.Member.UserID != want.UserID || got.Member.WorkspaceID != want.WorkspaceID || got.Member.Role != want.Role {
			t.Fatalf("callback identity = (%q, %q, %q), member = %+v; want %+v",
				got.UserID, got.ActiveWorkspaceID, got.Role, got.Member, want)
		}
	}
}

func TestInvestmentsRBACRoleMatrixAndLegacyPermissions(t *testing.T) {
	for _, legacy := range []string{"[]", `["investments:read"]`, `["investments:write"]`, `["investments:read","investments:write"]`} {
		t.Run(legacy, func(t *testing.T) {
			h := newInvestmentsRBACHarness(t, nil)
			h.exec(t, `UPDATE workspace_members SET custom_permissions = ?`, legacy)
			for _, member := range []models.WorkspaceMember{
				{UserID: "admin-user", WorkspaceID: "workspace-rbac", Role: models.RoleAdmin},
				{UserID: "manager-user", WorkspaceID: "workspace-rbac", Role: models.RoleManager},
				{UserID: "regular-user", WorkspaceID: "workspace-rbac", Role: models.RoleUser},
			} {
				session := h.session(t, member.UserID, member.WorkspaceID)
				if member.Role == models.RoleAdmin {
					h.assertAccess(t, session, http.StatusNoContent, &member)
				} else {
					h.assertAccess(t, session, http.StatusForbidden, nil)
				}
				loaded, err := h.service.ResolveWorkspaceMember(member.UserID, member.WorkspaceID)
				if err != nil || loaded.CustomPermissionsRaw != legacy || loaded.Role != member.Role {
					t.Fatalf("legacy membership changed: %+v, err=%v", loaded, err)
				}
			}
		})
	}
}

func TestInvestmentsRBACWorkspaceSwitchRebindsPermissions(t *testing.T) {
	h := newInvestmentsRBACHarness(t, nil)
	h.exec(t, `INSERT INTO workspaces (id, name) VALUES ('workspace-b', 'B'), ('workspace-foreign', 'Foreign')`)
	h.exec(t, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('workspace-b', 'admin-user', 'USER')`)
	session := h.session(t, "admin-user", "workspace-rbac")
	member := &models.WorkspaceMember{UserID: "admin-user", WorkspaceID: "workspace-rbac", Role: models.RoleAdmin}
	h.assertAccess(t, session, http.StatusNoContent, member)

	switchHandler := workspaceSwitchRoute(h.service, h.signer)
	for _, tc := range []struct {
		destination, active string
		status              int
	}{
		{"workspace-foreign", "workspace-rbac", http.StatusForbidden},
		{"nonexistent-workspace", "workspace-rbac", http.StatusForbidden},
		{"workspace-b", "workspace-b", http.StatusSeeOther},
	} {
		t.Run(tc.destination, func(t *testing.T) {
			before := len(h.protected)
			form := url.Values{"workspace_id": {tc.destination}}
			req := httptest.NewRequest(http.MethodPost, "/workspace/switch", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			req.AddCookie(&http.Cookie{Name: "workspace_role", Value: models.RoleAdmin})
			addAdminUsersRBACCSRF(t, req, h.signer)
			rr := httptest.NewRecorder()
			switchHandler.ServeHTTP(rr, req)
			if rr.Code != tc.status || len(h.protected) != before {
				t.Fatalf("switch status=%d, want=%d; protected callbacks=%d", rr.Code, tc.status, len(h.protected)-before)
			}
			userID, active, _, _, err := h.service.ResolveAndRefreshSession(session)
			if err != nil || userID != member.UserID || active != tc.active {
				t.Fatalf("switch session = (%q, %q), err=%v; want workspace %q", userID, active, err, tc.active)
			}
		})
	}
	// Same session, same handlers, stale ADMIN cookie: B's USER binding wins.
	h.assertAccess(t, session, http.StatusForbidden, nil)
	// Even a session row pointing at an existing foreign workspace cannot
	// manufacture membership. This mutation is confined to the test database.
	if err := h.service.UpdateSessionWorkspace(session, "workspace-foreign"); err != nil {
		t.Fatal(err)
	}
	h.assertAccess(t, session, http.StatusSeeOther, nil)
}

func TestInvestmentsRBACMembershipChangesTakeEffectNextRequest(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		status      int
	}{
		{"removed", `DELETE FROM workspace_members WHERE user_id = 'admin-user'`, http.StatusSeeOther},
		{"downgraded", `UPDATE workspace_members SET role = 'USER' WHERE user_id = 'admin-user'`, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newInvestmentsRBACHarness(t, nil)
			session := h.session(t, "admin-user", "workspace-rbac")
			h.assertAccess(t, session, http.StatusNoContent, &models.WorkspaceMember{
				UserID: "admin-user", WorkspaceID: "workspace-rbac", Role: models.RoleAdmin,
			})
			h.exec(t, tc.query)
			h.assertAccess(t, session, tc.status, nil)
		})
	}
}

func TestInvestmentsRBACMissingOrInvalidContext(t *testing.T) {
	for _, scenario := range []string{"missing-session", "invalid-session", "empty-workspace", "inactive-user", "deleted-user", "revoked-session"} {
		t.Run(scenario, func(t *testing.T) {
			h := newInvestmentsRBACHarness(t, nil)
			session := h.session(t, "admin-user", "workspace-rbac")
			switch scenario {
			case "missing-session":
				session = ""
			case "invalid-session":
				session = "invalid-test-session"
			case "empty-workspace":
				// Keep foreign keys enabled while constructing an empty context.
				h.exec(t, `INSERT INTO workspaces (id, name) VALUES ('', 'Invalid empty workspace')`)
				if err := h.service.UpdateSessionWorkspace(session, ""); err != nil {
					t.Fatal(err)
				}
			case "inactive-user":
				h.exec(t, `UPDATE users SET status = 'pending' WHERE id = 'admin-user'`)
			case "deleted-user":
				h.exec(t, `UPDATE users SET deleted_at = unixepoch() WHERE id = 'admin-user'`)
			case "revoked-session":
				h.exec(t, `UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = 'admin-user'`)
			}
			h.assertAccess(t, session, http.StatusSeeOther, nil)
		})
	}
}

func TestInvestmentsRBACWriteRequiresCSRF(t *testing.T) {
	h := newInvestmentsRBACHarness(t, nil)
	session := h.session(t, "admin-user", "workspace-rbac")
	req := httptest.NewRequest(http.MethodPost, "/test-only/investments-rbac", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rr := httptest.NewRecorder()
	h.probes[1].handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || len(h.protected) != 0 {
		t.Fatalf("missing CSRF: status=%d, protected callbacks=%d", rr.Code, len(h.protected))
	}
}

func TestInvestmentsRBACNewWorkspacesKeepAdminWithoutPersistedTokens(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	userID, err := runInitialSetup(db, "RBAC Owner", "owner@example.test", "test-only-password", "personal")
	if err != nil {
		t.Fatal(err)
	}
	h := newInvestmentsRBACHarness(t, db)
	workspaceID, role, err := h.service.ResolveWorkspaceMembership(userID)
	if err != nil || role != models.RoleAdmin {
		t.Fatalf("initial workspace role=%q, err=%v", role, err)
	}
	// Exercise the other real creation path as well; only rendering is stubbed.
	tpl := template.Must(template.New("configuracoes-admin_workspaces-content").Parse(`{{.Error}}`))
	creator := handlers.ConfiguracoesHandler{
		DB: db, Templates: tpl, UserID: userID, WorkspaceID: workspaceID, ActorRole: role,
	}
	form := url.Values{"name": {"RBAC New Workspace"}, "type": {"personal"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/workspaces/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	creator.HandleAdminWorkspacesCreate(rr, req)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "" {
		t.Fatalf("create workspace: status=%d, body=%q", rr.Code, rr.Body.String())
	}
	var newWorkspaceID string
	if err := db.QueryRow(`SELECT id FROM workspaces WHERE name = 'RBAC New Workspace'`).Scan(&newWorkspaceID); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []string{workspaceID, newWorkspaceID} {
		member, err := h.service.ResolveWorkspaceMember(userID, ws)
		if err != nil || member.Role != models.RoleAdmin || member.CustomPermissionsRaw != "[]" || len(member.CustomPermissions) != 0 {
			t.Fatalf("new workspace membership=%+v, err=%v", member, err)
		}
		h.assertAccess(t, h.session(t, userID, ws), http.StatusNoContent, &member)
	}
}
