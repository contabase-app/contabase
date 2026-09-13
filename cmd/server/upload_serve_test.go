package main

import (
	"bytes"
	"database/sql"
	"fmt"
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

func TestProfileUploadRouteRequiresAuthenticationAndSharedWorkspace(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()
	now := time.Now().Unix()
	if _, err := db.Exec(`
		UPDATE users SET profile_photo_path = '/uploads/profile/shared.png' WHERE id = 'admin-user';
		INSERT INTO users (id, name, email, password_hash, profile_photo_path, status, created_at, updated_at)
		VALUES ('foreign-user', 'Foreign User', 'foreign@example.test', 'hash', '/uploads/profile/foreign.png', 'active', ?, ?);
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('workspace-foreign-profile', 'Foreign Profile', '', 'personal', ?, ?);
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('workspace-foreign-profile', 'foreign-user', 'USER', ?)
	`, now, now, now, now, now); err != nil {
		t.Fatalf("seed profile visibility: %v", err)
	}

	root := t.TempDir()
	t.Setenv("DATA_DIR", filepath.Join(root, "state"))
	t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads-root"))
	if err := os.MkdirAll(paths.ProfileUploadsDir(), 0o700); err != nil {
		t.Fatalf("mkdir profile uploads: %v", err)
	}
	for _, name := range []string{"shared.png", "foreign.png"} {
		if err := os.WriteFile(filepath.Join(paths.ProfileUploadsDir(), name), []byte("image"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	authService := auth.NewService(db)
	sessionToken := createAdminUsersRBACSession(t, authService, "regular-user", "USER")
	signer := &csrfSigner{secret: bytes.Repeat([]byte{8}, 32), ttl: time.Hour}
	handler := profileUploadRoute(authService, signer)

	cases := []struct {
		name          string
		method        string
		path          string
		authenticated bool
		csrf          bool
		want          int
	}{
		{"anonymous shared profile", http.MethodGet, "/uploads/profile/shared.png", false, false, http.StatusSeeOther},
		{"member shared profile", http.MethodGet, "/uploads/profile/shared.png", true, false, http.StatusOK},
		{"member shared profile head", http.MethodHead, "/uploads/profile/shared.png", true, false, http.StatusOK},
		{"foreign profile", http.MethodGet, "/uploads/profile/foreign.png", true, false, http.StatusNotFound},
		{"unknown profile", http.MethodGet, "/uploads/profile/unknown.png", true, false, http.StatusNotFound},
		{"mutation method", http.MethodPost, "/uploads/profile/shared.png", true, true, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.authenticated {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
			}
			if tc.csrf {
				addAdminUsersRBACCSRF(t, req, signer)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestProfileUploadRouteUsesCurrentRuntimeDatabase(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_DIR", filepath.Join(root, "data"))
			t.Setenv("UPLOADS_DIR", filepath.Join(root, "uploads"))
			if err := os.MkdirAll(paths.ProfileUploadsDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			current := openAdminUsersRBACTestDB(t)
			defer func() { current.Close() }()
			authService := auth.NewService(current)
			signer := &csrfSigner{secret: bytes.Repeat([]byte{8}, 32), ttl: time.Hour}
			mux := http.NewServeMux()
			mux.HandleFunc("/uploads/profile/", profileUploadRoute(authService, signer))
			for generation := 1; generation <= 3; generation++ {
				if generation > 1 {
					if err := current.Close(); err != nil {
						t.Fatal(err)
					}
					current = openAdminUsersRBACTestDB(t)
					authService.DB = current
				}
				name := fmt.Sprintf("generation-%d.png", generation)
				if _, err := current.Exec(`UPDATE workspaces SET type = ? WHERE id = 'workspace-rbac'`, kind); err != nil {
					t.Fatal(err)
				}
				if _, err := current.Exec(`UPDATE users SET profile_photo_path = ? WHERE id = 'admin-user'`, "/uploads/profile/"+name); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(paths.ProfileUploadsDir(), name), []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
				token := createAdminUsersRBACSession(t, authService, "regular-user", "USER")
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					req := httptest.NewRequest(method, "/uploads/profile/"+name, nil)
					req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
					rr := httptest.NewRecorder()
					mux.ServeHTTP(rr, req)
					if rr.Code != http.StatusOK {
						t.Fatalf("G%d %s status=%d body=%s", generation, method, rr.Code, rr.Body.String())
					}
					assertHeader(t, rr, "Cache-Control", "no-store")
					if method == http.MethodGet && rr.Body.String() != name {
						t.Fatalf("G%d returned unexpected image: %s", generation, rr.Body.String())
					}
				}
				if generation > 1 {
					req := httptest.NewRequest(http.MethodGet, "/uploads/profile/generation-1.png", nil)
					req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
					rr := httptest.NewRecorder()
					mux.ServeHTTP(rr, req)
					if rr.Code != http.StatusNotFound {
						t.Fatalf("old photo with no current membership reference: status=%d", rr.Code)
					}
				}
			}
		})
	}
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
