package handlers

import (
	"bytes"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type workspaceProfileTestTemplates struct{}

func (workspaceProfileTestTemplates) ExecuteTemplate(w io.Writer, name string, data any) error {
	return nil
}

func (workspaceProfileTestTemplates) Lookup(name string) *template.Template {
	return template.New(name)
}

func TestHandleWorkspaceCorporateProfileUpdateUsesActiveWorkspaceOnly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	root := t.TempDir()
	t.Setenv("UPLOADS_DIR", root)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'User', 'user@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, company_name, cnpj_cpf, address, phone, logo_light_url, created_at, updated_at) VALUES ('ws-active', 'Ativo', 'desc antiga', 'business', 'Empresa Antiga', '11', 'Rua A', '1111', '/uploads/workspaces/ws-active/logo-light.png', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, company_name, cnpj_cpf, address, phone, logo_dark_url, created_at, updated_at) VALUES ('ws-other', 'Outro', 'desc outro', 'business', 'Empresa Outro', '22', 'Rua B', '2222', '/uploads/workspaces/ws-other/logo-dark.png', ?, ?)`, now, now)

	mustWriteFile(t, filepath.Join(root, "workspaces", "ws-active", "logo-light.png"))
	mustWriteFile(t, filepath.Join(root, "workspaces", "ws-other", "logo-dark.png"))

	handler := ConfiguracoesHandler{
		DB:             db,
		Templates:      workspaceProfileTestTemplates{},
		WorkspaceID:    "ws-active",
		UserID:         "user-1",
		ActorRole:      "ADMIN",
		CanConfigRead:  true,
		CanConfigWrite: true,
	}

	req := newWorkspaceProfileMultipartRequest(t, map[string]string{
		"workspace_id":   "ws-other",
		"name":           "Workspace Novo",
		"description":    "descricao nova",
		"company_name":   "Empresa Nova",
		"cnpj_cpf":       "12.345.678/0001-00",
		"address":        "Rua Nova",
		"phone":          "(11) 99999-9999",
		"logo_light_url": "/uploads/workspaces/ws-active/logo-light.png",
		"logo_dark_url":  "/uploads/workspaces/ws-other/logo-dark.png",
	})
	rr := httptest.NewRecorder()

	handler.HandleWorkspaceCorporateProfileUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var activeName, activeDesc, activeCompany, activeDoc, activeAddress, activePhone, activeLight, activeDark string
	if err := db.QueryRow(`SELECT name, description, company_name, cnpj_cpf, address, phone, COALESCE(logo_light_url, ''), COALESCE(logo_dark_url, '') FROM workspaces WHERE id = 'ws-active'`).Scan(&activeName, &activeDesc, &activeCompany, &activeDoc, &activeAddress, &activePhone, &activeLight, &activeDark); err != nil {
		t.Fatalf("query active workspace: %v", err)
	}
	if activeName != "Workspace Novo" || activeDesc != "descricao nova" || activeCompany != "Empresa Nova" || activeDoc != "12.345.678/0001-00" || activeAddress != "Rua Nova" || activePhone != "(11) 99999-9999" {
		t.Fatalf("active workspace not updated correctly: %q %q %q %q %q %q", activeName, activeDesc, activeCompany, activeDoc, activeAddress, activePhone)
	}
	if activeLight != "/uploads/workspaces/ws-active/logo-light.png" {
		t.Fatalf("active light logo = %q, want active path preserved", activeLight)
	}
	if activeDark != "" {
		t.Fatalf("active dark logo = %q, want empty because forged path must be ignored", activeDark)
	}

	var otherName, otherDesc, otherCompany, otherDark string
	if err := db.QueryRow(`SELECT name, description, company_name, COALESCE(logo_dark_url, '') FROM workspaces WHERE id = 'ws-other'`).Scan(&otherName, &otherDesc, &otherCompany, &otherDark); err != nil {
		t.Fatalf("query other workspace: %v", err)
	}
	if otherName != "Outro" || otherDesc != "desc outro" || otherCompany != "Empresa Outro" || otherDark != "/uploads/workspaces/ws-other/logo-dark.png" {
		t.Fatalf("other workspace was modified: %q %q %q %q", otherName, otherDesc, otherCompany, otherDark)
	}
}

func TestHandleWorkspaceCorporateProfileUpdateRemoveLogoFlagsOnlyClearsDatabaseReference(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	root := t.TempDir()
	t.Setenv("UPLOADS_DIR", root)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'User', 'user@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, company_name, cnpj_cpf, address, phone, logo_light_url, logo_dark_url, created_at, updated_at) VALUES ('ws-active', 'Ativo', 'desc', 'business', 'Empresa', '11', 'Rua A', '1111', '/uploads/workspaces/ws-active/logo-light.png', '/uploads/workspaces/ws-active/logo-dark.png', ?, ?)`, now, now)

	lightPath := filepath.Join(root, "workspaces", "ws-active", "logo-light.png")
	darkPath := filepath.Join(root, "workspaces", "ws-active", "logo-dark.png")
	mustWriteFile(t, lightPath)
	mustWriteFile(t, darkPath)

	handler := ConfiguracoesHandler{
		DB:             db,
		Templates:      workspaceProfileTestTemplates{},
		WorkspaceID:    "ws-active",
		UserID:         "user-1",
		ActorRole:      "ADMIN",
		CanConfigRead:  true,
		CanConfigWrite: true,
	}

	req := newWorkspaceProfileMultipartRequest(t, map[string]string{
		"name":              "Ativo",
		"description":       "desc",
		"company_name":      "Empresa",
		"cnpj_cpf":          "11",
		"address":           "Rua A",
		"phone":             "1111",
		"logo_light_url":    "/uploads/workspaces/ws-active/logo-light.png",
		"logo_dark_url":     "/uploads/workspaces/ws-active/logo-dark.png",
		"remove_logo_light": "1",
		"remove_logo_dark":  "1",
	})
	rr := httptest.NewRecorder()

	handler.HandleWorkspaceCorporateProfileUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var activeLight, activeDark string
	if err := db.QueryRow(`SELECT COALESCE(logo_light_url, ''), COALESCE(logo_dark_url, '') FROM workspaces WHERE id = 'ws-active'`).Scan(&activeLight, &activeDark); err != nil {
		t.Fatalf("query active logos: %v", err)
	}
	if activeLight != "" || activeDark != "" {
		t.Fatalf("logos should be cleared, got light=%q dark=%q", activeLight, activeDark)
	}
	if _, err := os.Stat(lightPath); err != nil {
		t.Fatalf("light file should remain on disk: %v", err)
	}
	if _, err := os.Stat(darkPath); err != nil {
		t.Fatalf("dark file should remain on disk: %v", err)
	}
}

func TestHandleWorkspaceCorporateProfileUpdateRejectsPersonalWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'User', 'user@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, company_name, cnpj_cpf, created_at, updated_at) VALUES ('ws-personal', 'Pessoal', 'desc', 'personal', 'Empresa', '11', ?, ?)`, now, now)

	handler := ConfiguracoesHandler{
		DB:             db,
		Templates:      workspaceProfileTestTemplates{},
		WorkspaceID:    "ws-personal",
		UserID:         "user-1",
		ActorRole:      "ADMIN",
		CanConfigRead:  true,
		CanConfigWrite: true,
	}

	req := newWorkspaceProfileMultipartRequest(t, map[string]string{
		"name":         "Novo Nome",
		"description":  "nova desc",
		"company_name": "Empresa Nova",
		"cnpj_cpf":     "22",
		"address":      "Rua Nova",
		"phone":        "2222",
	})
	rr := httptest.NewRecorder()

	handler.HandleWorkspaceCorporateProfileUpdate(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}

	var name, description string
	if err := db.QueryRow(`SELECT name, description FROM workspaces WHERE id = 'ws-personal'`).Scan(&name, &description); err != nil {
		t.Fatalf("query personal workspace: %v", err)
	}
	if name != "Pessoal" || description != "desc" {
		t.Fatalf("personal workspace changed unexpectedly: %q %q", name, description)
	}
}

func newWorkspaceProfileMultipartRequest(t *testing.T, fields map[string]string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/configuracoes/workspace", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, testPNGBytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
