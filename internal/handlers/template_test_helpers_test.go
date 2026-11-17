package handlers

import (
	"database/sql"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resolveTemplatePath(t *testing.T, relative string) string {
	t.Helper()

	candidates := []string{
		filepath.Join("..", "..", relative),
		relative,
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("template path not found for %s", relative)
	return ""
}

func assertContains(t *testing.T, body, token string) {
	t.Helper()
	if !strings.Contains(body, token) {
		t.Fatalf("response missing token %q\nbody:\n%s", token, body)
	}
}

func testMetasPageTemplates(t *testing.T) *template.Template {
	t.Helper()

	metasPath := resolveTemplatePath(t, "templates/pages/metas.html")
	shellPath := resolveTemplatePath(t, "templates/components/app_shell.html")
	metasContent, err := os.ReadFile(metasPath)
	if err != nil {
		t.Fatalf("read metas template: %v", err)
	}
	shellContent, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("read app shell template: %v", err)
	}

	return template.Must(template.New("metas-test").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
	})).Parse(string(metasContent) + string(shellContent)))
}

func testHistoryTemplates(t *testing.T) *template.Template {
	t.Helper()
	metasPath := resolveTemplatePath(t, "templates/pages/metas.html")
	content, err := os.ReadFile(metasPath)
	if err != nil {
		t.Fatalf("read metas template: %v", err)
	}
	return template.Must(template.New("metas-history-test").Funcs(withAppShellTestFuncs(template.FuncMap{})).Parse(string(content)))
}

func testMetasMutationTemplates(t *testing.T) *template.Template {
	t.Helper()
	return testMetasPageTemplates(t)
}

func assertTransactionExists(t *testing.T, db *sql.DB, txID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = ?`, txID).Scan(&count); err != nil {
		t.Fatalf("count existing transaction: %v", err)
	}
	if count != 1 {
		t.Fatalf("transaction %s count = %d, want 1", txID, count)
	}
}

func testMetasFormTemplates(t *testing.T) *template.Template {
	t.Helper()
	formPath := resolveTemplatePath(t, "templates/components/form_meta.html")
	content, err := os.ReadFile(formPath)
	if err != nil {
		t.Fatalf("read metas form template: %v", err)
	}
	pickerPath := resolveTemplatePath(t, "templates/components/icon_picker.html")
	pickerContent, err := os.ReadFile(pickerPath)
	if err != nil {
		t.Fatalf("read icon picker template: %v", err)
	}
	return template.Must(template.New("metas-form-test").
		Funcs(withAppShellTestFuncs(template.FuncMap{})).
		Parse(string(content) + string(pickerContent)))
}

// readTestFile reads a repository-relative fixture; kept as an alias because
// many financial tests use this name.
func readTestFile(t *testing.T, path string) string {
	t.Helper()
	return readTemplateFile(t, path)
}
