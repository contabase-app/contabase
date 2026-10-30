package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// CFG5 — Perfil corporativo premium, máscaras e isolamento de workspace.
func TestConfiguracoesWorkspacePremiumFormAndMasks(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/workspace", nil)
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, "workspace")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`/assets/js/config-masks.js?v=test`,
		`data-oninput="maskCnpjCpf"`,
		`data-oninput="maskPhone"`,
		`name="name"`,
		`name="description"`,
		`name="company_name"`,
		`name="cnpj_cpf"`,
		`name="address"`,
		`name="phone"`,
		`name="logo_light_file"`,
		`name="logo_light_url"`,
		`name="remove_logo_light"`,
		`name="logo_dark_file"`,
		`name="logo_dark_url"`,
		`name="remove_logo_dark"`,
		`hx-post="/configuracoes/workspace"`,
		`hx-target="#settings-dynamic-payload"`,
		`hx-swap="outerHTML"`,
		`hx-disabled-elt="find button[type='submit']"`,
		`hx-sync="this:drop"`,
		`hx-indicator="#htmx-indicator"`,
		`enctype="multipart/form-data"`,
		`class="ui-panel`,
		`class="ui-label"`,
		`class="ui-input`,
		`for="workspaceName"`,
		`for="workspaceCNPJ"`,
		`for="workspacePhone"`,
		`required`,
		`inputmode="numeric"`,
		`inputmode="tel"`,
	} {
		assertContains(t, body, want)
	}
	if strings.Contains(body, "data-sensitive") {
		t.Fatal("workspace corporate profile must not receive financial privacy masks")
	}
}

func TestConfiguracoesWorkspacePersonalShowsUnavailableWithoutForm(t *testing.T) {
	handler := newConfiguracoesPersonalRenderTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/configuracoes/workspace", nil)
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, "workspace")
	body := rr.Body.String()

	assertContains(t, body, "Perfil corporativo indisponível")
	if strings.Contains(body, `name="cnpj_cpf"`) {
		t.Fatal("personal workspace must not render the corporate form")
	}
}

func TestConfiguracoesWorkspaceUpdateValidatesAndIsolatesWorkspace(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('config-third', 'Config Third', '', 'business', ?, ?)
	`, now, now)

	// Campos obrigatórios ausentes: 422 e nada é atualizado.
	missing := postWorkspaceForm(t, handler, map[string]string{
		"name":         "Workspace CFG5",
		"company_name": "",
		"cnpj_cpf":     "",
	})
	if missing.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing required status = %d, want %d", missing.Code, http.StatusUnprocessableEntity)
	}
	assertWorkspaceName(t, handler, "config-workspace", "Config Workspace")

	// Workspace personal: 403 e nada é atualizado.
	personal := newConfiguracoesPersonalRenderTestHandler(t)
	forbidden := postWorkspaceForm(t, personal, validWorkspacePayload("Workspace Pessoal"))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("personal status = %d, want %d", forbidden.Code, http.StatusForbidden)
	}
	assertWorkspaceName(t, personal, "config-workspace", "Config Workspace")

	// Workspace ativo é o único atualizado; outro workspace business fica intacto.
	handler.WorkspaceID = "config-third"
	valid := postWorkspaceForm(t, handler, validWorkspacePayload("Workspace CFG5"))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid status = %d, want %d", valid.Code, http.StatusOK)
	}
	assertWorkspaceName(t, handler, "config-third", "Workspace CFG5")
	handler.WorkspaceID = "config-workspace"
	assertWorkspaceName(t, handler, "config-workspace", "Config Workspace")

	var company, cnpj, phone string
	if err := handler.DB.QueryRow(`
		SELECT company_name, cnpj_cpf, phone FROM workspaces WHERE id = 'config-third'
	`).Scan(&company, &cnpj, &phone); err != nil {
		t.Fatalf("query updated workspace: %v", err)
	}
	if company != "Empresa CFG5" || cnpj != "12.345.678/0001-95" || phone != "(11) 98765-4321" {
		t.Fatalf("unexpected updated fields: company=%q cnpj=%q phone=%q", company, cnpj, phone)
	}
}

func validWorkspacePayload(name string) map[string]string {
	return map[string]string{
		"name":         name,
		"description":  "Descrição CFG5",
		"company_name": "Empresa CFG5",
		"cnpj_cpf":     "12.345.678/0001-95",
		"address":      "Rua Sintética, 100",
		"phone":        "(11) 98765-4321",
	}
}

func postWorkspaceForm(t *testing.T, handler ConfiguracoesHandler, values map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range values {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write multipart field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/workspace", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	handler.HandleWorkspaceCorporateProfileUpdate(rr, req)
	return rr
}

func assertWorkspaceName(t *testing.T, handler ConfiguracoesHandler, workspaceID, want string) {
	t.Helper()
	var got string
	if err := handler.DB.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, workspaceID).Scan(&got); err != nil {
		t.Fatalf("query workspace %s: %v", workspaceID, err)
	}
	if got != want {
		t.Fatalf("workspace %s name = %q, want %q", workspaceID, got, want)
	}
}
