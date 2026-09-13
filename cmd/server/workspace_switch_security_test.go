package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/models"
)

func TestWorkspaceSwitchUsesServerMembershipAndRebindsRole(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('workspace-user', 'Workspace User', '', 'personal', ?, ?),
		       ('workspace-foreign', 'Workspace Foreign', '', 'personal', ?, ?);
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('workspace-user', 'admin-user', 'USER', ?)
	`, now, now, now, now, now); err != nil {
		t.Fatalf("seed switch workspaces: %v", err)
	}

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{23}, 32), ttl: time.Hour}
	session, _, err := authService.CreateSession("admin-user", "workspace-rbac", time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	handler := workspaceSwitchRoute(authService, signer)

	switchRequest := func(workspaceID string) *http.Request {
		form := url.Values{"workspace_id": {workspaceID}}
		req := httptest.NewRequest(http.MethodPost, "/workspace/switch", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		req.AddCookie(&http.Cookie{Name: "workspace_role", Value: models.RoleAdmin})
		addAdminUsersRBACCSRF(t, req, signer)
		return req
	}
	activeWorkspace := func() string {
		var workspaceID string
		if err := db.QueryRow(`SELECT workspace_id FROM sessions WHERE user_id = 'admin-user' AND revoked_at IS NULL`).Scan(&workspaceID); err != nil {
			t.Fatalf("load active workspace: %v", err)
		}
		return workspaceID
	}

	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, switchRequest("workspace-foreign"))
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("foreign switch status = %d, want %d", foreignResponse.Code, http.StatusForbidden)
	}
	if got := activeWorkspace(); got != "workspace-rbac" {
		t.Fatalf("foreign switch changed session to %q", got)
	}

	allowedResponse := httptest.NewRecorder()
	handler.ServeHTTP(allowedResponse, switchRequest("workspace-user"))
	if allowedResponse.Code != http.StatusSeeOther {
		t.Fatalf("allowed switch status = %d, want %d", allowedResponse.Code, http.StatusSeeOther)
	}
	if got := activeWorkspace(); got != "workspace-user" {
		t.Fatalf("allowed switch kept session at %q", got)
	}

	adminProbe := withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	probeRequest := httptest.NewRequest(http.MethodGet, "/admin/probe", nil)
	probeRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	probeRequest.AddCookie(&http.Cookie{Name: "workspace_role", Value: models.RoleAdmin})
	probeResponse := httptest.NewRecorder()
	adminProbe.ServeHTTP(probeResponse, probeRequest)
	if probeResponse.Code != http.StatusForbidden {
		t.Fatalf("forged role cookie status = %d, want %d", probeResponse.Code, http.StatusForbidden)
	}

	if _, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id = 'workspace-user' AND user_id = 'admin-user'`); err != nil {
		t.Fatalf("remove active membership: %v", err)
	}
	revokedMembershipResponse := httptest.NewRecorder()
	adminProbe.ServeHTTP(revokedMembershipResponse, probeRequest)
	if revokedMembershipResponse.Code != http.StatusSeeOther {
		t.Fatalf("removed membership status = %d, want %d", revokedMembershipResponse.Code, http.StatusSeeOther)
	}
}
