package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleExcluirContatoContracts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedTenantIsolationScenario(t, db)

	handler := ContatosHandler{
		DB:          db,
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	t.Run("success returns 200 and empty body", func(t *testing.T) {
		execTestSQL(t, db, `
			INSERT INTO contacts (id, workspace_id, custom_client_id, name, type, created_at)
			VALUES ('free-contact', 'ws-a', 'A-999', 'Free Contact', 'client', unixepoch())
		`)

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/contatos/free-contact", nil)

		handler.HandleExcluirContato(rr, req, "free-contact")

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty", rr.Body.String())
		}
		if got := rr.Header().Get("HX-Trigger"); got != "" {
			t.Fatalf("HX-Trigger = %q, want empty", got)
		}

		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE id = ? AND workspace_id = ?`, "free-contact", "ws-a").Scan(&count); err != nil {
			t.Fatalf("verify deleted contact: %v", err)
		}
		if count != 0 {
			t.Fatalf("deleted contact count = %d, want 0", count)
		}
	})

	t.Run("fk restriction keeps 422 and trigger", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/contatos/a-contact", nil)

		handler.HandleExcluirContato(rr, req, "a-contact")

		if rr.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusUnprocessableEntity, rr.Body.String())
		}
		if got := rr.Header().Get("HX-Trigger"); got != `{"mostrarAlerta":"Não é possível excluir um contato que possui lançamentos financeiros ativos."}` {
			t.Fatalf("HX-Trigger = %q, want contact alert", got)
		}
		if !strings.Contains(rr.Body.String(), "contato com lançamentos vinculados") {
			t.Fatalf("body = %q, want fk error message", rr.Body.String())
		}

		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE id = ? AND workspace_id = ?`, "a-contact", "ws-a").Scan(&count); err != nil {
			t.Fatalf("verify restricted contact: %v", err)
		}
		if count != 1 {
			t.Fatalf("restricted contact count = %d, want 1", count)
		}
	})
}
