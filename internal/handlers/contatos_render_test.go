package handlers

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContatosRenderContracts(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	data := ContatosData{
		Title:                     "Contatos",
		Query:                     "ana",
		Tipo:                      "client",
		TipoLabel:                 "Clientes",
		CustomClientIDPlaceholder: "CLI-001",
		Contatos: []ContatoRow{
			{
				ID:        "contact-1",
				Name:      "Ana Lima",
				Type:      "client",
				TypeLabel: "Cliente",
				CreatedAt: "10/06/2026",
			},
		},
	}

	var page bytes.Buffer
	if err := tpl.ExecuteTemplate(&page, "contatos-page", data); err != nil {
		t.Fatalf("render contatos-page: %v", err)
	}
	pageHTML := page.String()
	for _, want := range []string{
		`data-page-title="Contatos"`,
		`id="contatos-list"`,
		`hx-post="/contatos"`,
		`hx-target="#contatos-list"`,
		`hx-swap="outerHTML"`,
		`placeholder="CLI-001"`,
		`name="custom_client_id"`,
		`hx-get="/contatos?tipo=client&partial=lista"`,
		`hx-select="#contatos-grid"`,
		`hx-push-url="/contatos?tipo=client"`,
	} {
		if !strings.Contains(pageHTML, want) {
			t.Fatalf("contatos-page missing %q\nbody:\n%s", want, pageHTML)
		}
	}

	var list bytes.Buffer
	if err := tpl.ExecuteTemplate(&list, "contatos-list", data); err != nil {
		t.Fatalf("render contatos-list: %v", err)
	}
	listHTML := list.String()
	for _, want := range []string{
		`id="contatos-list"`,
		`hx-get="/contatos?tipo=client&partial=lista"`,
		`hx-push-url="/contatos?tipo=client"`,
		`hx-target="#contatos-grid"`,
		`hx-select="#contatos-grid"`,
		`data-stop-propagation`,
		`id="contato-contact-1"`,
	} {
		if !strings.Contains(listHTML, want) {
			t.Fatalf("contatos-list missing %q\nbody:\n%s", want, listHTML)
		}
	}
	if got := strings.Count(listHTML, `hx-delete="/contatos/contact-1"`); got != 1 {
		t.Fatalf("contatos-list expected 1 delete trigger, got %d\nbody:\n%s", got, listHTML)
	}

	var row bytes.Buffer
	if err := tpl.ExecuteTemplate(&row, "contato-row", ContatoRow{
		ID:        "contact-1",
		Name:      "Ana Lima",
		Type:      "client",
		TypeLabel: "Cliente",
		CreatedAt: "10/06/2026",
	}); err != nil {
		t.Fatalf("render contato-row: %v", err)
	}
	rowHTML := row.String()
	for _, want := range []string{
		`data-stop-propagation`,
		`hx-get="/contatos/contact-1"`,
		`hx-target="#contato-contact-1"`,
		`hx-swap="outerHTML"`,
		`hx-delete="/contatos/contact-1"`,
		`hx-swap="delete"`,
		`type="button"`,
	} {
		if !strings.Contains(rowHTML, want) {
			t.Fatalf("contato-row missing %q\nbody:\n%s", want, rowHTML)
		}
	}
	if got := strings.Count(rowHTML, `hx-delete="/contatos/contact-1"`); got != 1 {
		t.Fatalf("contato-row expected 1 delete trigger, got %d\nbody:\n%s", got, rowHTML)
	}

	var form bytes.Buffer
	if err := tpl.ExecuteTemplate(&form, "contato-row-form", ContatoRow{
		ID:                        "contact-1",
		CustomClientID:            "CLI-007",
		CustomClientIDPlaceholder: "",
		Name:                      "Ana Lima",
		Document:                  "123.456.789-00",
		Type:                      "client",
		TypeLabel:                 "Cliente",
		Email:                     "ana@example.com",
		Phone:                     "(11) 99999-9999",
		CreatedAt:                 "10/06/2026",
	}); err != nil {
		t.Fatalf("render contato-row-form: %v", err)
	}
	formHTML := form.String()
	for _, want := range []string{
		`hx-post="/contatos/contact-1/salvar"`,
		`hx-target="#contato-contact-1"`,
		`hx-swap="outerHTML"`,
		`name="type"`,
		`name="custom_client_id"`,
		`name="document"`,
		`name="name"`,
		`name="email"`,
		`name="phone"`,
		`value="Ana Lima"`,
		`value="CLI-007"`,
		`value="123.456.789-00"`,
		`value="ana@example.com"`,
		`value="(11) 99999-9999"`,
	} {
		if !strings.Contains(formHTML, want) {
			t.Fatalf("contato-row-form missing %q\nbody:\n%s", want, formHTML)
		}
	}
}

func testContatosRenderTemplates(t *testing.T) *template.Template {
	t.Helper()

	funcs := template.FuncMap{
		"upper": strings.ToUpper,
		"slice": func(s string, start, end int) string {
			r := []rune(s)
			if start < 0 {
				start = 0
			}
			if end > len(r) {
				end = len(r)
			}
			if start > end {
				start = end
			}
			return string(r[start:end])
		},
	}

	stubs := `
{{define "layout-start"}}<html><body><div id="main-content">{{end}}
{{define "layout-end"}}</div></body></html>{{end}}
`

	pagePath := resolveTemplatePath(t, "templates/pages/contatos.html")
	contactsPath := resolveTemplatePath(t, "templates/components/contatos.html")
	fabPath := resolveTemplatePath(t, "templates/components/fab.html")

	pageBytes, err := os.ReadFile(filepath.Clean(pagePath))
	if err != nil {
		t.Fatalf("read contatos page template: %v", err)
	}
	contactsBytes, err := os.ReadFile(filepath.Clean(contactsPath))
	if err != nil {
		t.Fatalf("read contatos component template: %v", err)
	}
	fabBytes, err := os.ReadFile(filepath.Clean(fabPath))
	if err != nil {
		t.Fatalf("read fab component template: %v", err)
	}

	return template.Must(template.New("contatos-test").Funcs(funcs).Parse(stubs + string(pageBytes) + string(contactsBytes) + string(fabBytes)))
}
