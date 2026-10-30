package main

import (
	"bytes"
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

// transacoesEditarStubTemplates keeps HandleDetalheTransacao rendering without
// loading the full template tree; the RBAC contract only needs the handler to
// reach (and render) the edit form.
type transacoesEditarStubTemplates struct{}

func (transacoesEditarStubTemplates) ExecuteTemplate(wr io.Writer, name string, data any) error {
	_, err := wr.Write([]byte(`<div id="stub-form-lancamento"></div>`))
	return err
}

func (transacoesEditarStubTemplates) Lookup(name string) *template.Template { return nil }

// TestTransacoesEditarAliasEnforcesReadPermission locks the F8 security fix:
// the GET /transacoes/{id}/editar alias mirrors the canonical route's explicit
// transactions:read check on top of the existing session + workspace scoping.
func TestTransacoesEditarAliasEnforcesReadPermission(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedTransacoesEditarRBACData(t, db)

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{23}, 32), ttl: time.Hour}
	mux := http.NewServeMux()
	txBase := handlers.TransactionHandler{DB: db, Templates: transacoesEditarStubTemplates{}}
	registerTransacoesRoutes(mux, db, authService, signer, &txBase)

	sessions := map[string]string{
		models.RoleUser:    createAdminUsersRBACSession(t, authService, "regular-user", models.RoleUser),
		models.RoleManager: createAdminUsersRBACSession(t, authService, "manager-user", models.RoleManager),
		models.RoleAdmin:   createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin),
	}

	t.Run("authenticated roles with read render the edit form", func(t *testing.T) {
		for role := range sessions {
			req := httptest.NewRequest(http.MethodGet, "/transacoes/tx-editar-rbac/editar", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[role]})
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("role %s status = %d, want 200, body = %q", role, rr.Code, rr.Body.String())
			}
			if !bytes.Contains(rr.Body.Bytes(), []byte(`id="stub-form-lancamento"`)) {
				t.Fatalf("role %s response did not render the edit form: %q", role, rr.Body.String())
			}
		}
	})

	t.Run("unauthenticated request is redirected to login", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/transacoes/tx-editar-rbac/editar", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Fatalf("response = %d location=%q, want 303 /login", rr.Code, rr.Header().Get("Location"))
		}
	})

	t.Run("transaction from another workspace is not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/transacoes/tx-other-workspace/editar", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[models.RoleUser]})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, body = %q", rr.Code, rr.Body.String())
		}
	})

	t.Run("nonexistent transaction is not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/transacoes/tx-inexistente/editar", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[models.RoleUser]})
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, body = %q", rr.Code, rr.Body.String())
		}
	})

	t.Run("read without update keeps saving forbidden", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/transacoes/tx-editar-rbac", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[models.RoleUser]})
		addAdminUsersRBACCSRF(t, req, signer)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body = %q", rr.Code, rr.Body.String())
		}
	})

	t.Run("update permission passes the RBAC gate on the canonical route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/transacoes/tx-editar-rbac", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[models.RoleManager]})
		addAdminUsersRBACCSRF(t, req, signer)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		// RBAC passed: the empty form is rejected by validation (400 "valor
		// inválido"), which is the current product contract for update attempts.
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (validation), not 403, body = %q", rr.Code, rr.Body.String())
		}
		if !bytes.Contains(rr.Body.Bytes(), []byte("valor")) {
			t.Fatalf("unexpected validation body: %q", rr.Body.String())
		}
	})
}

// TestTransacoesEditarAliasGuardSemantics documents why the explicit check is
// defense in depth: with today's fixed role matrix every role carries
// transactions:read, so the branch only triggers for unknown/empty roles.
func TestTransacoesEditarAliasGuardSemantics(t *testing.T) {
	if hasPermission("", permTransactionsRead) {
		t.Fatal("empty role must not grant transactions:read")
	}
	if hasPermission("VIEWER", permTransactionsRead) {
		t.Fatal("unknown role must not grant transactions:read")
	}
	for _, role := range []string{models.RoleAdmin, models.RoleManager, models.RoleUser} {
		if !hasPermission(role, permTransactionsRead) {
			t.Fatalf("role %s must keep transactions:read", role)
		}
		if hasPermission(role, permTransactionsUpdate) != (role != models.RoleUser) {
			t.Fatalf("role %s transactions:update matrix changed", role)
		}
	}
}

func seedTransacoesEditarRBACData(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	type stmt struct {
		name  string
		query string
		args  []any
	}
	statements := []stmt{
		{name: "own account", query: `
			INSERT INTO accounts (id, workspace_id, name, type, created_at, updated_at)
			VALUES ('acc-editar-rbac', 'workspace-rbac', 'Conta RBAC', 'CHECKING', ?, ?)`, args: []any{now, now}},
		{name: "own transaction", query: `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, created_at, updated_at)
			VALUES ('tx-editar-rbac', 'workspace-rbac', 'admin-user', 'acc-editar-rbac', 'EXPENSE', 1234, ?, 'Editar RBAC', 'paid', ?, ?)`, args: []any{now, now, now}},
		{name: "other workspace", query: `
			INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
			VALUES ('workspace-editar-other', 'Outro Workspace', '', 'personal', ?, ?)`, args: []any{now, now}},
		{name: "other workspace account", query: `
			INSERT INTO accounts (id, workspace_id, name, type, created_at, updated_at)
			VALUES ('acc-editar-other', 'workspace-editar-other', 'Conta Outra', 'CHECKING', ?, ?)`, args: []any{now, now}},
		{name: "other workspace transaction", query: `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, created_at, updated_at)
			VALUES ('tx-other-workspace', 'workspace-editar-other', 'admin-user', 'acc-editar-other', 'EXPENSE', 5678, ?, 'Outro workspace', 'paid', ?, ?)`, args: []any{now, now, now}},
	}
	for _, s := range statements {
		if _, err := db.Exec(s.query, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}
}
