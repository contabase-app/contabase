package handlers

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRespondContatoFormErrorPreservesHTMXTarget(t *testing.T) {
	rr := httptest.NewRecorder()
	respondContatoFormError(rr, "nome e tipo são obrigatórios")

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnprocessableEntity)
	}
	if !strings.Contains(rr.Body.String(), "nome e tipo são obrigatórios") {
		t.Fatalf("response missing validation message: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `hx-swap-oob`) {
		t.Fatalf("validation response must not replace an unrelated HTMX target: %s", rr.Body.String())
	}
}

func TestContatosRenderContracts(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	data := ContatosData{
		Title:                     "Contatos",
		UserName:                  "Vitor Ferreira",
		UserInitials:              "VF",
		ActiveWorkspaceName:       "Empresa",
		IsBusiness:                true,
		Query:                     "ana",
		Tipo:                      "client",
		TipoLabel:                 "Clientes",
		CustomClientIDPlaceholder: "CLI-001",
		Contatos: []ContatoRow{
			{
				ID:        "contact-1",
				Name:      "Ana Lima",
				Initials:  "AL",
				Type:      "client",
				TypeLabel: "Cliente",
				CreatedAt: "10/06/2026",
				CanUpdate: true,
				CanDelete: true,
			},
		},
	}

	var page bytes.Buffer
	if err := tpl.ExecuteTemplate(&page, "contatos-page", data); err != nil {
		t.Fatalf("render contatos-page: %v", err)
	}
	pageHTML := page.String()
	for _, want := range []string{
		`data-app-shell`,
		`data-page-section="contacts"`,
		`data-v2-nav-section="contacts"`,
		`cb-v2-contatos-header`,
		`data-page-title="Contatos"`,
		`id="contatos-list"`,
		`data-contact-create`,
		`hx-get="/contatos/novo?tipo=client"`,
		`hx-target="#bottom-sheet-container"`,
		`hx-swap="innerHTML"`,
		`hx-get="/contatos?tipo=client&partial=lista"`,
		`hx-select="#contatos-grid"`,
		`hx-push-url="true"`,
		`/assets/css/contatos.css?v=test`,
		`/assets/js/contatos-modal.js?v=test`,
		`/assets/js/contatos-tabs.js?v=test`,
	} {
		if !strings.Contains(pageHTML, want) {
			t.Fatalf("contatos-page missing %q\nbody:\n%s", want, pageHTML)
		}
	}
	if strings.Contains(pageHTML, `{{template "layout-start"`) {
		t.Fatal("contatos page must use the global app shell, not the legacy layout")
	}
	if strings.Contains(pageHTML, `id="contatos-modal"`) {
		t.Fatal("the legacy contacts modal must be gone; creation lives in the global sheet")
	}

	var list bytes.Buffer
	if err := tpl.ExecuteTemplate(&list, "contatos-list", data); err != nil {
		t.Fatalf("render contatos-list: %v", err)
	}
	listHTML := list.String()
	for _, want := range []string{
		`id="contatos-list"`,
		`hx-get="/contatos?tipo=client&partial=lista"`,
		`hx-push-url="true"`,
		`hx-target="#contatos-grid"`,
		`hx-select="#contatos-grid"`,
		`data-stop-propagation`,
		`id="contato-contact-1"`,
		`hx-get="/contatos/contact-1"`,
		`hx-target="#bottom-sheet-container"`,
	} {
		if !strings.Contains(listHTML, want) {
			t.Fatalf("contatos-list missing %q\nbody:\n%s", want, listHTML)
		}
	}
	if strings.Contains(listHTML, `hx-delete="/contatos/contact-1"`) {
		t.Fatal("deletion moved to the edit sheet danger zone; cards must not expose it")
	}

	var row bytes.Buffer
	if err := tpl.ExecuteTemplate(&row, "contato-card", ContatoRow{
		ID:        "contact-1",
		Name:      "Ana Lima",
		Initials:  "AL",
		Type:      "client",
		TypeLabel: "Cliente",
		CreatedAt: "10/06/2026",
		CanUpdate: true,
		CanDelete: true,
	}); err != nil {
		t.Fatalf("render contato-card: %v", err)
	}
	rowHTML := row.String()
	for _, want := range []string{
		`class="cb-contact-card"`,
		`>AL</span>`,
		`data-stop-propagation`,
		`hx-get="/contatos/contact-1"`,
		`hx-target="#bottom-sheet-container"`,
		`hx-swap="innerHTML"`,
		`hx-push-url="false"`,
		`type="button"`,
	} {
		if !strings.Contains(rowHTML, want) {
			t.Fatalf("contato-card missing %q\nbody:\n%s", want, rowHTML)
		}
	}
	if strings.Contains(rowHTML, `hx-delete=`) {
		t.Fatal("contato-card must not render a delete action")
	}

	var createSheet bytes.Buffer
	if err := tpl.ExecuteTemplate(&createSheet, "contato-create-sheet", ContatosData{
		Type:                      "client",
		CustomClientIDPlaceholder: "CLI-001",
	}); err != nil {
		t.Fatalf("render contato-create-sheet: %v", err)
	}
	createHTML := createSheet.String()
	for _, want := range []string{
		`data-contatos-sheet="create"`,
		`class="sheet-in contatos-sheet__dialog"`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="contato-sheet-title"`,
		`aria-describedby="contato-sheet-description"`,
		`hx-post="/contatos"`,
		`hx-target="#contatos-list"`,
		`hx-swap="outerHTML"`,
		`id="contato-form-error"`,
		`name="type"`,
		`name="custom_client_id"`,
		`name="document"`,
		`name="name"`,
		`name="email"`,
		`name="phone"`,
		`placeholder="CLI-001"`,
		`data-contato-sheet-initial`,
		`data-oninput="maskCnpjCpfContato"`,
		`data-oninput="maskPhoneContato"`,
		`class="ui-input"`,
		`class="ui-select"`,
		`data-onclick="closeBottomSheet"`,
	} {
		if !strings.Contains(createHTML, want) {
			t.Fatalf("contato-create-sheet missing %q\nbody:\n%s", want, createHTML)
		}
	}
	if strings.Contains(createHTML, `data-contatos-sheet="edit"`) || strings.Contains(createHTML, `Zona de perigo`) {
		t.Fatal("creation sheet must not render the danger zone")
	}

	var editSheet bytes.Buffer
	if err := tpl.ExecuteTemplate(&editSheet, "contato-edit-sheet", ContatoRow{
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
		CanUpdate:                 true,
		CanDelete:                 true,
	}); err != nil {
		t.Fatalf("render contato-edit-sheet: %v", err)
	}
	editHTML := editSheet.String()
	for _, want := range []string{
		`data-contatos-sheet="edit"`,
		`hx-post="/contatos/contact-1/salvar"`,
		`hx-target="#contatos-list"`,
		`value="Ana Lima"`,
		`value="CLI-007"`,
		`value="123.456.789-00"`,
		`value="ana@example.com"`,
		`value="(11) 99999-9999"`,
		`data-contato-delete="contact-1"`,
		`data-contato-name="Ana Lima"`,
		`Zona de perigo`,
	} {
		if !strings.Contains(editHTML, want) {
			t.Fatalf("contato-edit-sheet missing %q\nbody:\n%s", want, editHTML)
		}
	}

	var noDanger bytes.Buffer
	if err := tpl.ExecuteTemplate(&noDanger, "contato-edit-sheet", ContatoRow{ID: "contact-2", Name: "Sem Exclusao", Type: "client", CanUpdate: true, CanDelete: false}); err != nil {
		t.Fatalf("render contato-edit-sheet without permission: %v", err)
	}
	if strings.Contains(noDanger.String(), `Zona de perigo`) {
		t.Fatal("danger zone must not render without contacts.delete")
	}
}

func testContatosRenderTemplates(t *testing.T) *template.Template {
	t.Helper()

	funcs := template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"upper":     strings.ToUpper,
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

	return template.Must(template.New("contatos-test").Funcs(withAppShellTestFuncs(funcs)).ParseFiles(
		resolveTemplatePath(t, "templates/pages/contatos.html"),
		resolveTemplatePath(t, "templates/components/contatos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
	))
}
