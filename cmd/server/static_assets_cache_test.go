package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerRuntimeImageCopiesCompleteAssetsTree(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	dockerfilePath := filepath.Join(repoRoot, "Dockerfile")
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	const completeAssetsCopy = "COPY --from=builder /app/assets ./assets"
	if !strings.Contains(string(raw), completeAssetsCopy) {
		t.Fatalf("Docker runtime stage must copy the complete assets tree with %q", completeAssetsCopy)
	}

	for _, required := range []string{
		"assets/contabase-monogram.svg",
		"assets/favicon.ico",
		"assets/manifest.json",
		"assets/pwa-icon-dark-192x192.png",
		"assets/pwa-icon-dark-512x512.png",
		"assets/pwa-icon-dark-maskable-512x512.png",
	} {
		info, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(required)))
		if err != nil {
			t.Fatalf("required runtime asset %s: %v", required, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("required runtime asset %s must be a non-empty regular file", required)
		}
	}
}

func TestStaticAssetsCacheControlHeader(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "js"), 0o700); err != nil {
		t.Fatalf("mkdir js: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "css"), 0o700); err != nil {
		t.Fatalf("mkdir css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "js", "test.js"), []byte("var a=1;"), 0o600); err != nil {
		t.Fatalf("write test.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "css", "test.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatalf("write test.css: %v", err)
	}

	fs := http.FileServer(http.Dir(dir))
	handler := http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
		fs.ServeHTTP(w, r)
	}))

	t.Run("js file returns Cache-Control", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/js/test.js", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rr.Code)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=3600, must-revalidate" {
			t.Errorf("expected Cache-Control: public, max-age=3600, must-revalidate, got: %q", cc)
		}
	})

	t.Run("css file returns Cache-Control", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/css/test.css", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rr.Code)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=3600, must-revalidate" {
			t.Errorf("expected Cache-Control: public, max-age=3600, must-revalidate, got: %q", cc)
		}
	})

	t.Run("nonexistent file returns 404 without cache header overwrite", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/nonexistent.js", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("expected status 404, got %d", rr.Code)
		}
	})

	t.Run("subdirectory traversal is blocked", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/../main.go", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound && rr.Code != http.StatusBadRequest {
			t.Errorf("expected blocked traversal, got status %d", rr.Code)
		}
	})
}
