package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

// M8 — defesa em profundidade nos GETs de Metas.
//
// A rota continua sendo a primeira barreira de permissão. Estes testes
// garantem que o handler também nega antes de tocar no banco/templates quando
// chamado diretamente, e que a matriz RBAC existente é a única fonte de regra.

func TestMetasReadPermissionMatrixUsesExistingRBAC(t *testing.T) {
	cases := []struct {
		role          string
		dashboardRead bool
		goalsWrite    bool
	}{
		{role: models.RoleAdmin, dashboardRead: true, goalsWrite: true},
		{role: models.RoleManager, dashboardRead: true, goalsWrite: true},
		{role: models.RoleUser, dashboardRead: true, goalsWrite: false},
		{role: "VIEWER", dashboardRead: false, goalsWrite: false},
		{role: "", dashboardRead: false, goalsWrite: false},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			h := MetasHandler{ActorRole: tc.role}
			if got := h.allowMetas(metasPermDashboardRead); got != tc.dashboardRead {
				t.Fatalf("dashboard:read for %q = %v, want %v", tc.role, got, tc.dashboardRead)
			}
			if got := h.allowMetas(metasPermGoalsWrite); got != tc.goalsWrite {
				t.Fatalf("goals:write for %q = %v, want %v", tc.role, got, tc.goalsWrite)
			}
		})
	}
}

func TestMetasReadHandlersDenyBeforeDataAccess(t *testing.T) {
	type handlerFunc func(*MetasHandler, http.ResponseWriter, *http.Request)
	cases := []struct {
		name string
		role string
		path string
		call handlerFunc
	}{
		{name: "list", role: "VIEWER", path: "/metas", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleListarMetas(w, r) }},
		{name: "page", role: "VIEWER", path: "/metas", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleMetasConceito(w, r) }},
		{name: "new form", role: models.RoleUser, path: "/metas/novo", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleNovaMeta(w, r) }},
		{name: "box history", role: models.RoleUser, path: "/metas/caixinha/historico?box_id=box-1", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleHistoricoCaixinha(w, r) }},
		{name: "limit history", role: "VIEWER", path: "/metas/limite/historico?limit_id=limit-1", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleHistoricoLimite(w, r) }},
		{name: "contribute sheet", role: models.RoleUser, path: "/metas/caixinha/aporte/sheet?box_id=box-1", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleAporteCaixinhaSheet(w, r) }},
		{name: "release sheet", role: models.RoleUser, path: "/metas/caixinha/resgate/sheet?box_id=box-1", call: func(h *MetasHandler, w http.ResponseWriter, r *http.Request) { h.HandleResgateCaixinhaSheet(w, r) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// DB e Templates nil: um guard que não bloqueasse antes do I/O
			// provocaria panic em vez de 403.
			h := MetasHandler{ActorRole: tc.role}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)

			tc.call(&h, rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%q", rr.Code, rr.Body.String())
			}
			if rr.Body.String() != "forbidden\n" {
				t.Fatalf("body = %q, want forbidden", rr.Body.String())
			}
		})
	}
}
