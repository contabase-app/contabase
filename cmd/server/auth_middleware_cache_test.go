package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
)

func TestWithAuthAddsNoStoreHeaders(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('admin123', 'Admin', 'admin@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'admin123', 'ADMIN');
	`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	authService := auth.NewService(db)

	sessionToken, _, err := authService.CreateSession("admin123", "ws1", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Authenticated content"))
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status OK, got %v", rr.Code)
	}

	expectedCacheControl := "no-store, no-cache, must-revalidate, max-age=0"
	if cc := rr.Header().Get("Cache-Control"); cc != expectedCacheControl {
		t.Errorf("expected Cache-Control: %q, got: %q", expectedCacheControl, cc)
	}

	if pragma := rr.Header().Get("Pragma"); pragma != "no-cache" {
		t.Errorf("expected Pragma: no-cache, got: %q", pragma)
	}

	if expires := rr.Header().Get("Expires"); expires != "0" {
		t.Errorf("expected Expires: 0, got: %q", expires)
	}
}

func TestWithAuthHTMXRequest(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('admin123', 'Admin', 'admin@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'admin123', 'ADMIN');
	`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	authService := auth.NewService(db)
	sessionToken, _, err := authService.CreateSession("admin123", "ws1", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("HTMX partial content"))
	})

	req := httptest.NewRequest(http.MethodGet, "/partials/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	req.Header.Set("HX-Request", "true")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status OK, got %v", rr.Code)
	}

	expectedCacheControl := "no-store, no-cache, must-revalidate, max-age=0"
	if cc := rr.Header().Get("Cache-Control"); cc != expectedCacheControl {
		t.Errorf("expected Cache-Control: %q, got: %q", expectedCacheControl, cc)
	}
}

func TestWithAuthMissingSession(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	authService := auth.NewService(db)
	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		t.Errorf("handler should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	// Sem cookie de sessão intencionalmente

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// missing session redirects to /login (303)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status SeeOther, got %v", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc == "no-store, no-cache, must-revalidate, max-age=0" {
		t.Errorf("expected unauthenticated redirect to not contain the secure logged-in cache headers")
	}
}

func TestWithAuthForbiddenResponse(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('user123', 'User', 'user@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'user123', 'USER');
	`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	authService := auth.NewService(db)
	sessionToken, _, err := authService.CreateSession("user123", "ws1", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		// Mock a forbidden check
		if ctx.Role != "ADMIN" {
			respondForbidden(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status Forbidden, got %v", rr.Code)
	}

	expectedCacheControl := "no-store, no-cache, must-revalidate, max-age=0"
	if cc := rr.Header().Get("Cache-Control"); cc != expectedCacheControl {
		t.Errorf("expected Cache-Control: %q, got: %q", expectedCacheControl, cc)
	}
}

func TestWithAuthExportDownload(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('admin123', 'Admin', 'admin@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'admin123', 'ADMIN');
	`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	authService := auth.NewService(db)
	sessionToken, _, err := authService.CreateSession("admin123", "ws1", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		w.Header().Set("Content-Disposition", "attachment; filename=\"export.csv\"")
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("id,name\n1,Test"))
	})

	req := httptest.NewRequest(http.MethodGet, "/export", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status OK, got %v", rr.Code)
	}

	expectedCacheControl := "no-store, no-cache, must-revalidate, max-age=0"
	if cc := rr.Header().Get("Cache-Control"); cc != expectedCacheControl {
		t.Errorf("expected Cache-Control: %q, got: %q", expectedCacheControl, cc)
	}
}

func TestSessionCheckEndpointReturns204WhenAuthenticated(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('admin123', 'Admin', 'admin@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'admin123', 'ADMIN');
	`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	authService := auth.NewService(db)
	sessionToken, _, err := authService.CreateSession("admin123", "ws1", 24*time.Hour, false)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/session/check", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected status 204 NoContent, got %v", rr.Code)
	}

	expectedCacheControl := "no-store, no-cache, must-revalidate, max-age=0"
	if cc := rr.Header().Get("Cache-Control"); cc != expectedCacheControl {
		t.Errorf("expected Cache-Control: %q, got: %q", expectedCacheControl, cc)
	}
}

func TestSessionCheckEndpointRedirectsWhenUnauthenticated(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open memory db: %v", err)
	}
	defer db.Close()

	authService := auth.NewService(db)
	csrfSigner, err := newCSRFSigner()
	if err != nil {
		t.Fatalf("failed to create csrf signer: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO workspaces (id, name) VALUES ('ws1', 'Workspace 1');
		INSERT INTO users (id, name, email, password_hash) VALUES ('admin123', 'Admin', 'admin@example.com', 'hash');
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('ws1', 'admin123', 'ADMIN');
	`)
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	handler := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/session/check", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("expected status 303 SeeOther, got %v", rr.Code)
	}

	loc := rr.Header().Get("Location")
	if loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}
}
