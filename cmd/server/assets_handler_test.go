package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/assets"
)

func newAssetFixture(t *testing.T) (root string, handler http.Handler) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "css"), 0o755); err != nil {
		t.Fatalf("mkdir css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "css", "app.css"), []byte("body{color:red}"), 0o600); err != nil {
		t.Fatalf("write css: %v", err)
	}
	return root, http.StripPrefix("/assets/", newAssetHandler(root))
}

func serveAsset(handler http.Handler, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestAssetHandlerMarksCurrentHashAsImmutable(t *testing.T) {
	root, handler := newAssetFixture(t)
	hash, ok := assets.ContentHash(filepath.Join(root, "css", "app.css"))
	if !ok {
		t.Fatal("expected test asset hash")
	}

	rr := serveAsset(handler, "/assets/css/app.css?v="+hash, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != assetCacheImmutable {
		t.Fatalf("Cache-Control = %q, want %q", got, assetCacheImmutable)
	}
	if got, want := rr.Header().Get("ETag"), `"`+hash+`"`; got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
	if !strings.Contains(rr.Body.String(), "body{color:red}") {
		t.Fatalf("unexpected body: %q", rr.Body.String())
	}
}

func TestAssetHandlerRevalidatesUnversionedAndStaleRequests(t *testing.T) {
	_, handler := newAssetFixture(t)
	for _, target := range []string{
		"/assets/css/app.css",
		"/assets/css/app.css?v=deadbeef",
	} {
		t.Run(target, func(t *testing.T) {
			rr := serveAsset(handler, target, nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if got := rr.Header().Get("Cache-Control"); got != assetCacheRevalidate {
				t.Fatalf("Cache-Control = %q, want %q", got, assetCacheRevalidate)
			}
			if rr.Header().Get("ETag") == "" {
				t.Fatal("expected ETag for readable asset")
			}
		})
	}
}

func TestAssetHandlerHonorsIfNoneMatch(t *testing.T) {
	_, handler := newAssetFixture(t)
	etag := serveAsset(handler, "/assets/css/app.css", nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag")
	}

	conditional := serveAsset(handler, "/assets/css/app.css", map[string]string{"If-None-Match": etag})
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", conditional.Code)
	}
	if conditional.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", conditional.Body.String())
	}
}

func TestAssetHandlerDoesNotServeOutsideRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "assets")
	if err := os.MkdirAll(filepath.Join(root, "css"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "outside.txt"), []byte("outside-secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	handler := http.StripPrefix("/assets/", newAssetHandler(root))

	rr := serveAsset(handler, "/assets/../outside.txt", nil)
	if rr.Code == http.StatusOK && strings.Contains(rr.Body.String(), "outside-secret") {
		t.Fatalf("served file outside assets root: status=%d body=%q", rr.Code, rr.Body.String())
	}
}
