package handlers

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAjudaPageRenderContracts(t *testing.T) {
	tpl := testAjudaRenderTemplates(t)
	data := struct {
		Title               string
		UserInitials        string
		UserFirstName       string
		ActiveWorkspaceName string
		ProfilePhotoURL     string
		IsBusiness          bool
	}{
		Title:               "Ajuda",
		UserInitials:        "UA",
		UserFirstName:       "User",
		ActiveWorkspaceName: "Workspace A",
		IsBusiness:          true,
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "ajuda-page", data); err != nil {
		t.Fatalf("render ajuda-page: %v", err)
	}

	html := out.String()
	for _, want := range []string{
		`data-page-title="Ajuda"`,
		`id="main-content"`,
		`id="help-search"`,
		`id="perfis-permissoes"`,
		`id="custo-de-vida"`,
		`id="tendencia-saldo"`,
		`id="categorias-hierarquia"`,
		`id="caixinhas-metas"`,
		`id="motor-temporal"`,
		`id="limite-gastos"`,
		`id="seguranca-backups"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("ajuda-page missing %q\nbody:\n%s", want, html)
		}
	}
}

func TestLayoutKeepsHelpSearchScriptReference(t *testing.T) {
	layoutPath := resolveTemplatePath(t, "templates/layout.html")
	content, err := os.ReadFile(filepath.Clean(layoutPath))
	if err != nil {
		t.Fatalf("read layout template: %v", err)
	}

	if !strings.Contains(string(content), `/assets/js/ajuda-search.js`) {
		t.Fatalf("layout template missing help search script reference")
	}
}

func testAjudaRenderTemplates(t *testing.T) *template.Template {
	t.Helper()

	stubs := `
{{define "layout-start"}}<html><body><div id="main-content">{{end}}
{{define "layout-end"}}</div></body></html>{{end}}
`

	pagePath := resolveTemplatePath(t, "templates/pages/ajuda.html")
	pageBytes, err := os.ReadFile(filepath.Clean(pagePath))
	if err != nil {
		t.Fatalf("read ajuda page template: %v", err)
	}

	return template.Must(template.New("ajuda-test").Parse(stubs + string(pageBytes)))
}
