package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestAdminWorkspaceHandlersRejectNonAdminBeforeMutation(t *testing.T) {
	for _, role := range []string{models.RoleManager, models.RoleUser} {
		t.Run(role, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			now := time.Now().Unix()
			if _, err := db.Exec(`
				INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
				VALUES ('workspace-actor', 'Actor', 'actor@example.test', 'hash', 'active', ?, ?);
				INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
				VALUES ('workspace-protected', 'Protected', '', 'personal', ?, ?)
			`, now, now, now, now); err != nil {
				t.Fatalf("seed workspace RBAC: %v", err)
			}
			handler := ConfiguracoesHandler{DB: db, UserID: "workspace-actor", ActorRole: role}

			createForm := url.Values{"name": {"Unauthorized workspace"}, "type": {"personal"}}
			createRequest := httptest.NewRequest(http.MethodPost, "/admin/workspaces/salvar", strings.NewReader(createForm.Encode()))
			createRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			createResponse := httptest.NewRecorder()
			handler.HandleAdminWorkspacesCreate(createResponse, createRequest)
			if createResponse.Code != http.StatusForbidden {
				t.Fatalf("create status = %d, want %d", createResponse.Code, http.StatusForbidden)
			}

			editForm := url.Values{"name": {"Mutated"}, "type": {"personal"}}
			editRequest := httptest.NewRequest(http.MethodPost, "/admin/workspaces/editar", strings.NewReader(editForm.Encode()))
			editRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			editResponse := httptest.NewRecorder()
			handler.HandleAdminWorkspaceEdit(editResponse, editRequest, "workspace-protected")
			if editResponse.Code != http.StatusForbidden {
				t.Fatalf("edit status = %d, want %d", editResponse.Code, http.StatusForbidden)
			}

			deleteResponse := httptest.NewRecorder()
			handler.HandleAdminWorkspaceDelete(deleteResponse, httptest.NewRequest(http.MethodDelete, "/workspaces/workspace-protected", nil), "workspace-protected")
			if deleteResponse.Code != http.StatusForbidden {
				t.Fatalf("delete status = %d, want %d", deleteResponse.Code, http.StatusForbidden)
			}

			var name string
			if err := db.QueryRow(`SELECT name FROM workspaces WHERE id = 'workspace-protected'`).Scan(&name); err != nil {
				t.Fatalf("protected workspace missing: %v", err)
			}
			if name != "Protected" {
				t.Fatalf("protected workspace name = %q", name)
			}
			var unauthorizedCount int
			if err := db.QueryRow(`SELECT COUNT(1) FROM workspaces WHERE name = 'Unauthorized workspace'`).Scan(&unauthorizedCount); err != nil {
				t.Fatalf("count unauthorized workspace: %v", err)
			}
			if unauthorizedCount != 0 {
				t.Fatalf("unauthorized workspace count = %d", unauthorizedCount)
			}
		})
	}
}
