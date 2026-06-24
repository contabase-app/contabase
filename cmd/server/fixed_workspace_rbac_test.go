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

func TestFixedWorkspaceRBACMatrixIgnoresCustomPermissions(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`
		UPDATE workspace_members
		SET custom_permissions = ?
		WHERE workspace_id = 'workspace-rbac' AND user_id = 'regular-user'
	`, models.PermissionListToJSON([]string{
		models.PermissionReportsView,
		models.PermissionContactsDelete,
		"config:read",
		"config:write",
	})); err != nil {
		t.Fatalf("seed user custom permissions: %v", err)
	}

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{8}, 32), ttl: time.Hour}
	handler := newFixedWorkspaceRBACRouteTestHandler(authService, signer)
	sessions := map[string]string{
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
		{"user reads launches by fixed role", http.MethodGet, "/lancamentos", models.RoleUser, http.StatusNoContent},
		{"user reads invoice screen by fixed role", http.MethodGet, "/faturas", models.RoleUser, http.StatusNoContent},
		{"user opens goals overview by fixed role", http.MethodGet, "/metas", models.RoleUser, http.StatusNoContent},
		{"user opens predictive transaction by fixed role", http.MethodGet, "/transacoes/preditiva", models.RoleUser, http.StatusNoContent},
		{"user custom reports view is ignored", http.MethodGet, "/relatorios", models.RoleUser, http.StatusForbidden},
		{"manager reads reports by fixed role", http.MethodGet, "/relatorios", models.RoleManager, http.StatusNoContent},
		{"user custom contacts delete is ignored", http.MethodDelete, "/contatos/contact-id", models.RoleUser, http.StatusForbidden},
		{"manager deletes contacts by fixed role", http.MethodDelete, "/contatos/contact-id", models.RoleManager, http.StatusNoContent},
		{"user custom config read is ignored", http.MethodGet, "/configuracoes/categorias", models.RoleUser, http.StatusForbidden},
		{"manager reads config by fixed role", http.MethodGet, "/configuracoes/categorias", models.RoleManager, http.StatusNoContent},
		{"user corporate profile read is ignored", http.MethodGet, "/configuracoes/workspace", models.RoleUser, http.StatusForbidden},
		{"manager reads corporate profile by fixed role", http.MethodGet, "/configuracoes/workspace", models.RoleManager, http.StatusNoContent},
		{"user corporate profile write is ignored", http.MethodPost, "/configuracoes/workspace", models.RoleUser, http.StatusForbidden},
		{"manager writes corporate profile by fixed role", http.MethodPost, "/configuracoes/workspace", models.RoleManager, http.StatusNoContent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[tc.role]})
			if tc.method != http.MethodGet {
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

func TestConfiguracoesWorkspaceRequiresBusinessWorkspace(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{10}, 32), ttl: time.Hour}
	session := createAdminUsersRBACSession(t, authService, "manager-user", models.RoleManager)

	mux := http.NewServeMux()
	mux.HandleFunc("/configuracoes/workspace", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !models.HasPermission(&ctx.Member, string(permConfigRead)) {
			respondForbidden(w, r)
			return
		}
		if rawWorkspaceType(db, ctx.ActiveWorkspaceID) != models.WorkspaceTypeBusiness {
			respondForbidden(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/workspace", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusForbidden, rr.Body.String())
	}
}

func newFixedWorkspaceRBACRouteTestHandler(authService *auth.Service, signer *csrfSigner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/lancamentos", fixedWorkspaceRBACHandler(authService, signer, http.MethodGet, func(ctx authContext) bool {
		return hasPermission(ctx.Role, permTransactionsRead)
	}))
	mux.HandleFunc("/faturas", fixedWorkspaceRBACHandler(authService, signer, http.MethodGet, func(ctx authContext) bool {
		return hasPermission(ctx.Role, permTransactionsRead)
	}))
	mux.HandleFunc("/metas", fixedWorkspaceRBACHandler(authService, signer, http.MethodGet, func(ctx authContext) bool {
		return hasPermission(ctx.Role, permDashboardRead)
	}))
	mux.HandleFunc("/transacoes/preditiva", fixedWorkspaceRBACHandler(authService, signer, http.MethodGet, func(ctx authContext) bool {
		return hasPermission(ctx.Role, permTransactionsCreate)
	}))
	mux.HandleFunc("/relatorios", fixedWorkspaceRBACHandler(authService, signer, http.MethodGet, func(ctx authContext) bool {
		return models.HasPermission(&ctx.Member, models.PermissionReportsView)
	}))
	mux.HandleFunc("/contatos/contact-id", fixedWorkspaceRBACHandler(authService, signer, http.MethodDelete, func(ctx authContext) bool {
		return models.HasPermission(&ctx.Member, models.PermissionContactsDelete)
	}))
	mux.HandleFunc("/configuracoes/categorias", fixedWorkspaceRBACHandler(authService, signer, http.MethodGet, func(ctx authContext) bool {
		return models.HasPermission(&ctx.Member, string(permConfigRead))
	}))
	mux.HandleFunc("/configuracoes/workspace", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		switch r.Method {
		case http.MethodGet:
			if !models.HasPermission(&ctx.Member, string(permConfigRead)) {
				respondForbidden(w, r)
				return
			}
		case http.MethodPost:
			if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
				respondForbidden(w, r)
				return
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return mux
}

func fixedWorkspaceRBACHandler(authService *auth.Service, signer *csrfSigner, method string, allowed func(authContext) bool) http.HandlerFunc {
	return withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !allowed(ctx) {
			respondForbidden(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
