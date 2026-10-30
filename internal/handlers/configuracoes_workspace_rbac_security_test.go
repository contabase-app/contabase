package handlers

import (
	"context"
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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
			if _, err := fixtureHandlerExec(t, db, `
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

func TestAdminWorkspaceDeleteRollsBackBeforeRendering(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace string
		status    int
		message   string
		setup     func(*testing.T, *sql.DB)
	}{
		{
			name: "foreign key restriction", workspace: "ws-test", status: http.StatusUnprocessableEntity,
			message: "vínculos ativos",
			setup: func(t *testing.T, db *sql.DB) {
				expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
				insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-r", "2026-07")
			},
		},
		{
			name: "statement failure", workspace: "ws-test", status: http.StatusOK,
			message: "Não foi possível excluir o workspace.",
			setup: func(t *testing.T, db *sql.DB) {
				seedInvoicePaymentScenario(t, db)
				// Connection-local fault injection; no production schema change.
				execTestSQL(t, db, `CREATE TEMP TRIGGER fail_workspace_delete BEFORE DELETE ON workspaces BEGIN SELECT RAISE(ABORT, 'forced workspace delete failure'); END`)
			},
		},
		{
			name: "workspace not found", workspace: "ws-missing", status: http.StatusOK,
			message: "Workspace não encontrado.",
			setup:   seedInvoicePaymentScenario,
		},
		{
			name: "cascade leaves protected block", workspace: "ws-test", status: http.StatusOK,
			message: "Nenhuma alteração foi aplicada.",
			setup: func(t *testing.T, db *sql.DB) {
				seedInvoicePaymentScenario(t, db)
				// Remove only fixture purchases so the real cascade can reach its
				// residue check instead of stopping at an account FK restriction.
				execTestSQL(t, db, `DELETE FROM transactions WHERE workspace_id='ws-test'`)
				expected := recurringOccurrenceBlockExpectation{WorkspaceID: "ws-test", SeriesID: "series-residue", RecurrenceSequence: 1}
				insertBlockFixture(t, db, expected, "rule-residue", "2026-07-15", "invoice-2026-08", "2026-08")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			tc.setup(t, db)
			if db.Stats().MaxOpenConnections != 1 {
				t.Fatal("workspace deletion must exercise the production single-connection pool")
			}
			before := recurringBlockStateSnapshot(t, db, "ws-test")
			handler := ConfiguracoesHandler{
				DB: db, WorkspaceID: "ws-test", UserID: "user-test", ActorRole: models.RoleAdmin,
				Templates: template.Must(template.New("test").Parse(`{{define "configuracoes-admin_workspaces-content"}}{{.FlashError}}{{end}}`)),
			}
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				handler.HandleAdminWorkspaceDelete(response, httptest.NewRequest(http.MethodDelete, "/workspaces/"+tc.workspace, nil), tc.workspace)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = db.Close()
				t.Fatal("workspace deletion did not finish rendering with a single connection")
			}
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.message) {
				t.Fatalf("response status=%d body=%q, want %d containing %q", response.Code, response.Body.String(), tc.status, tc.message)
			}
			if tc.status == http.StatusUnprocessableEntity && !strings.Contains(response.Header().Get("HX-Trigger"), "mostrarAlerta") {
				t.Fatal("protected workspace restriction lost its alert trigger")
			}
			if db.Stats().InUse != 0 {
				t.Fatal("workspace deletion retained a pool connection")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("pool unavailable for a subsequent transaction: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, recurringBlockStateSnapshot(t, db, "ws-test")) {
				t.Fatal("failed workspace deletion changed workspace, financial or recurring state")
			}
		})
	}
}
