package assets_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/assets"
)

func writeAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}
}

func TestVersionedPathIsStableAndContentAddressed(t *testing.T) {
	assets.SetDebugMode(true)
	defer assets.SetDebugMode(false)

	path := filepath.Join(t.TempDir(), "app.css")
	writeAsset(t, path, "body{color:red}")

	first := assets.VersionedPath(path)
	second := assets.VersionedPath(path)
	if first != second {
		t.Fatalf("version changed without content change: %q != %q", first, second)
	}
	if !regexp.MustCompile(`\?v=[0-9a-f]{8}$`).MatchString(first) {
		t.Fatalf("unexpected versioned path: %q", first)
	}

	writeAsset(t, path, "body{color:blue}")
	third := assets.VersionedPath(path)
	if third == first {
		t.Fatalf("version did not change after content change: %q", first)
	}
}

func TestVersionedPathKeepsHashWhenOnlyModTimeChanges(t *testing.T) {
	assets.SetDebugMode(true)
	defer assets.SetDebugMode(false)

	path := filepath.Join(t.TempDir(), "app.js")
	writeAsset(t, path, "console.log('stable')")

	first := assets.VersionedPath(path)
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if got := assets.VersionedPath(path); got != first {
		t.Fatalf("version changed with identical content: %q != %q", got, first)
	}
}

func TestVersionedPathFallsBackForMissingOrDirectory(t *testing.T) {
	assets.SetDebugMode(true)
	defer assets.SetDebugMode(false)

	missing := filepath.Join(t.TempDir(), "missing.css")
	if got := assets.VersionedPath(missing); got != missing {
		t.Fatalf("missing asset = %q, want %q", got, missing)
	}

	dir := t.TempDir()
	if got := assets.VersionedPath(dir); got != dir {
		t.Fatalf("directory asset = %q, want %q", got, dir)
	}
}

func TestContentHashIsSafeForConcurrentReaders(t *testing.T) {
	assets.SetDebugMode(true)
	defer assets.SetDebugMode(false)

	path := filepath.Join(t.TempDir(), "app.css")
	writeAsset(t, path, "body{background:#fff}")

	var wg sync.WaitGroup
	results := make([]string, 24)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _ = assets.ContentHash(path)
		}(i)
	}
	wg.Wait()

	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("concurrent hash mismatch: %q != %q", results[i], results[0])
		}
	}
}
