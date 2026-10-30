package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var staticAssetReference = regexp.MustCompile(`(?:src|href)="/assets/(?:css|js)/`)

func TestTemplatesVersionCSSAndJavaScriptAssets(t *testing.T) {
	templatesRoot := filepath.Join("..", "..", "templates")
	err := filepath.WalkDir(templatesRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if staticAssetReference.Match(data) {
			rel, _ := filepath.Rel(templatesRoot, path)
			t.Errorf("%s contains CSS/JS asset without assetPath cache busting", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
}

func TestBaseShellVersionsCriticalAssets(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "components", "app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	markup := string(data)
	for _, asset := range []string{
		`assetPath "/assets/css/style.css"`,
		`assetPath "/assets/css/geist-sans.css"`,
		`assetPath "/assets/css/app-shell.css"`,
		`assetPath "/assets/css/ui-tokens.css"`,
		`assetPath "/assets/js/theme-init.js"`,
		`assetPath "/assets/js/htmx.min.js"`,
		`assetPath "/assets/js/app-shell.js"`,
		`assetPath "/assets/js/input-modality.js"`,
		`assetPath "/assets/js/privacy.js"`,
		`assetPath "/assets/js/show-more.js"`,
	} {
		if !strings.Contains(markup, asset) {
			t.Errorf("app shell missing %s", asset)
		}
	}
}
