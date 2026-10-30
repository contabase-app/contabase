package main

import (
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/contabase-app/contabase/internal/assets"
)

const (
	assetCacheImmutable  = "public, max-age=31536000, immutable"
	assetCacheRevalidate = "public, max-age=0, must-revalidate"
)

// newAssetHandler serves files from root and marks content-addressed requests
// as immutable. Requests without the current hash stay revalidatable, so a
// stale HTML document can never keep an old asset indefinitely.
func newAssetHandler(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fullPath := assetFilePath(root, r.URL.Path)
		hash, ok := assets.ContentHash(fullPath)
		if ok {
			w.Header().Set("ETag", `"`+hash+`"`)
			if r.URL.Query().Get("v") == hash {
				w.Header().Set("Cache-Control", assetCacheImmutable)
			} else {
				w.Header().Set("Cache-Control", assetCacheRevalidate)
			}
		} else {
			w.Header().Set("Cache-Control", assetCacheRevalidate)
		}
		files.ServeHTTP(w, r)
	})
}

func assetFilePath(root, urlPath string) string {
	clean := path.Clean("/" + strings.TrimPrefix(urlPath, "/"))
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
}
