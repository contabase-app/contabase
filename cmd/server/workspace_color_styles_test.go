package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/auth"
)

func TestWorkspaceColorStylesAreValidatedAndTenantScoped(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, name, description, type) VALUES ('workspace-other', 'Other', '', 'personal');
		INSERT INTO accounts (id, workspace_id, name, type, color) VALUES
			('account-current', 'workspace-rbac', 'Current', 'CHECKING', '#123456'),
			('account-invalid', 'workspace-rbac', 'Invalid', 'CHECKING', 'red};body{display:none}'),
			('account-foreign', 'workspace-other', 'Foreign', 'CHECKING', '#ABCDEF');
		INSERT INTO categories (id, workspace_id, name, color, type) VALUES
			('category-current', 'workspace-rbac', 'Current category', '#654321', 'EXPENSE'),
			('category-foreign', 'workspace-other', 'Foreign category', '#FEDCBA', 'EXPENSE');
	`); err != nil {
		t.Fatalf("seed workspace colors: %v", err)
	}

	authService := auth.NewService(db)
	session := createAdminUsersRBACSession(t, authService, "regular-user", "USER")
	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("create csrf signer: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/workspace/colors.css", withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		serveWorkspaceColorStyles(w, r, db, ctx.ActiveWorkspaceID)
	}))

	req := httptest.NewRequest(http.MethodGet, "/workspace/colors.css", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "private, no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
	body := rr.Body.String()
	for _, want := range []string{`data-cb-color="#123456" i`, `data-cb-color="#654321" i`, `--cb-runtime-color-value:#123456`} {
		if !strings.Contains(body, want) {
			t.Errorf("stylesheet is missing %q", want)
		}
	}
	for _, forbidden := range []string{"#ABCDEF", "#FEDCBA", "body{display:none}", "account-foreign", "category-foreign"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("stylesheet leaked or emitted invalid foreign content %q", forbidden)
		}
	}
}

func TestWorkspaceColorStylesRejectsUnauthenticatedRequest(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	authService := auth.NewService(db)
	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("create csrf signer: %v", err)
	}
	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		serveWorkspaceColorStyles(w, r, db, ctx.ActiveWorkspaceID)
	})

	rr := httptest.NewRecorder()
	handler(rr, httptest.NewRequest(http.MethodGet, "/workspace/colors.css", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
}
