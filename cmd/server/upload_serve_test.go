package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/paths"
)

func TestUploadedImageContentTypeBlocksSVG(t *testing.T) {
	if _, ok := uploadedImageContentType("logo.svg"); ok {
		t.Fatalf("expected svg upload serve to be blocked")
	}
}

func TestSafeServedUploadPathBlocksTraversal(t *testing.T) {
	if _, err := safeServedUploadPath(t.TempDir(), "../avatar.png"); err == nil {
		t.Fatalf("expected traversal path to be blocked")
	}
	if _, err := safeServedUploadPath(t.TempDir(), "nested/avatar.png"); err == nil {
		t.Fatalf("expected nested filename to be blocked")
	}
}

func TestServeUploadedImageSetsSafeHeaders(t *testing.T) {
	dir := t.TempDir()
	fullPath := filepath.Join(dir, "avatar.png")
	if err := os.WriteFile(fullPath, []byte("image-bytes"), 0o600); err != nil {
		t.Fatalf("write test image: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/uploads/profile/avatar.png", nil)
	rr := httptest.NewRecorder()

	serveUploadedImage(rr, req, fullPath, "image/png")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	assertHeader(t, rr, "Content-Type", "image/png")
	assertHeader(t, rr, "X-Content-Type-Options", "nosniff")
	assertHeader(t, rr, "Cache-Control", "no-store")
}

func TestServeProfileUploadUsesConfiguredUploadsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads-root"))

	if err := os.MkdirAll(paths.ProfileUploadsDir(), 0o700); err != nil {
		t.Fatalf("mkdir profile uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.ProfileUploadsDir(), "avatar.png"), []byte("image"), 0o600); err != nil {
		t.Fatalf("write profile upload: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/profile/avatar.png", nil)
	rr := httptest.NewRecorder()

	serveProfileUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	assertHeader(t, rr, "Cache-Control", "no-store")
}

func TestWorkspaceUploadServeRequiresMembershipForPathWorkspace(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()

	authService := auth.NewService(db)
	sessionToken := createAdminUsersRBACSession(t, authService, "regular-user", "USER")
	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads-root"))
	for _, path := range []string{
		paths.WorkspaceUploadsDir("workspace-rbac"),
		paths.WorkspaceUploadsDir("workspace-other"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(path, "logo.png"), []byte("image"), 0o600); err != nil {
			t.Fatalf("write logo: %v", err)
		}
	}

	signer := &csrfSigner{secret: bytes.Repeat([]byte{6}, 32), ttl: time.Hour}
	handler := newWorkspaceUploadServeTestHandler(db, authService, signer)
	cases := []struct {
		name string
		path string
		want int
	}{
		{"own workspace logo", "/uploads/workspaces/workspace-rbac/logo.png", http.StatusOK},
		{"foreign workspace logo", "/uploads/workspaces/workspace-other/logo.png", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
			if tc.want == http.StatusOK {
				if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
					t.Errorf("authenticated upload must return Cache-Control: no-store, got: %q", cc)
				}
			}
		})
	}
}

func newWorkspaceUploadServeTestHandler(db *sql.DB, authService *auth.Service, signer *csrfSigner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/uploads/workspaces/", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		tail := strings.TrimPrefix(r.URL.Path, "/uploads/workspaces/")
		parts := strings.SplitN(tail, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		workspaceID := parts[0]
		fileName := parts[1]
		if !safeUploadPathSegment(workspaceID) || !safeUploadFileName(fileName) {
			http.NotFound(w, r)
			return
		}
		contentType, ok := uploadedImageContentType(fileName)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND workspace_id = ?`, ctx.UserID, workspaceID).Scan(&count); err != nil || count == 0 {
			http.NotFound(w, r)
			return
		}
		fullPath, err := safeServedUploadPath(paths.WorkspaceUploadsDir(workspaceID), fileName)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		serveUploadedImage(w, r, fullPath, contentType)
	}))
	return mux
}
