package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestConfiguracoesShellRenderContract(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/configuracoes", nil)
	rr := httptest.NewRecorder()

	handler.HandleConfiguracoesConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`id="main-content"`,
		`data-user-name="Config Admin"`,
		`data-user-initials="CA"`,
		`data-workspace-name="Config Workspace"`,
		`data-workspace-business="true"`,
		`id="settings-dynamic-payload"`,
		`data-config-section="perfil"`,
		`hx-target="#settings-dynamic-payload"`,
		`hx-select="#settings-dynamic-payload"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="true"`,
	} {
		assertContains(t, body, want)
	}
}

func TestConfiguracoesSectionsPreserveFullAndHTMXContracts(t *testing.T) {
	sections := []string{"perfil", "workspace", "categorias", "contas", "cartoes"}

	for _, section := range sections {
		t.Run(section, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)

			fullReq := httptest.NewRequest(http.MethodGet, "/configuracoes/"+section, nil)
			fullRR := httptest.NewRecorder()
			handler.HandleConfiguracoesSection(fullRR, fullReq, section)

			if fullRR.Code != http.StatusOK {
				t.Fatalf("full status = %d, want %d, body = %q", fullRR.Code, http.StatusOK, fullRR.Body.String())
			}
			assertContains(t, fullRR.Body.String(), `id="main-content"`)
			assertContains(t, fullRR.Body.String(), `id="settings-dynamic-payload"`)
			assertContains(t, fullRR.Body.String(), `data-config-section="`+section+`"`)

			partialReq := httptest.NewRequest(http.MethodGet, "/configuracoes/"+section, nil)
			partialReq.Header.Set("HX-Request", "true")
			partialRR := httptest.NewRecorder()
			handler.HandleConfiguracoesSection(partialRR, partialReq, section)

			if partialRR.Code != http.StatusOK {
				t.Fatalf("partial status = %d, want %d, body = %q", partialRR.Code, http.StatusOK, partialRR.Body.String())
			}
			assertContains(t, partialRR.Body.String(), `id="settings-dynamic-payload"`)
			if strings.Contains(partialRR.Body.String(), `id="main-content"`) {
				t.Fatalf("HTMX partial for %s must not render the full layout", section)
			}
		})
	}
}

func TestConfiguracoesAdminSectionsRenderForAdmin(t *testing.T) {
	sections := []string{"admin-users", "admin-workspaces", "admin-backups", "admin-auditoria"}

	for _, section := range sections {
		t.Run(section, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)
			req := httptest.NewRequest(http.MethodGet, "/admin/"+strings.TrimPrefix(section, "admin-"), nil)
			req.Header.Set("HX-Request", "true")
			rr := httptest.NewRecorder()

			handler.HandleConfiguracoesSection(rr, req, section)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
			}
			assertContains(t, rr.Body.String(), `id="settings-dynamic-payload"`)
			if section == "admin-users" {
				assertContains(t, rr.Body.String(), `Config Secondary`)
			}
		})
	}
}

func TestConfiguracoesLeanSectionDataKeepsRequiredFields(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	workspaceData, err := handler.buildConfigRenderData("workspace", "", "", false)
	if err != nil {
		t.Fatalf("build workspace data: %v", err)
	}
	if !workspaceData.IsBusiness || workspaceData.WorkspaceProfile.Type != models.WorkspaceTypeBusiness {
		t.Fatalf("workspace data lost business contract: %+v", workspaceData.WorkspaceProfile)
	}

	categoryData, err := handler.buildConfigRenderData("categorias", "", "", false)
	if err != nil {
		t.Fatalf("build category data: %v", err)
	}
	if !categoryData.IsBusiness {
		t.Fatalf("category data must preserve business workspace context")
	}

	adminUsersData, err := handler.buildConfigRenderData("admin-users", "", "", false)
	if err != nil {
		t.Fatalf("build admin users data: %v", err)
	}
	if len(adminUsersData.SystemWorkspaces) != 2 {
		t.Fatalf("admin users system workspaces = %d, want 2", len(adminUsersData.SystemWorkspaces))
	}
	if len(adminUsersData.UserWorkspaces) != 0 {
		t.Fatalf("lean section assembly must not load unused user workspaces")
	}
	if adminUsersData.UserName != "" || adminUsersData.ActiveWorkspaceName != "" || adminUsersData.ProfilePhotoURL != "" {
		t.Fatalf("lean section assembly must not load shell identity data")
	}

	adminWorkspacesData, err := handler.buildConfigRenderData("admin-workspaces", "", "", false)
	if err != nil {
		t.Fatalf("build admin workspaces data: %v", err)
	}
	if len(adminWorkspacesData.SystemWorkspaces) != 0 {
		t.Fatalf("system workspaces must only be loaded for admin users")
	}
}

func TestConfiguracoesAuditoriaFullPageAndRowsContracts(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	execTestSQL(t, handler.DB, `
		UPDATE workspaces
		SET smtp_host = 'smtp.config.test',
			smtp_port = 2525,
			notification_email = 'alerts@config.test',
			email_preferences = '["auth.failed","workspace.edit"]'
		WHERE id = 'config-workspace'
	`)
	execTestSQL(t, handler.DB, `
		INSERT INTO security_logs (id, workspace_id, user_id, event_type, severity, ip_address, metadata, created_at)
		VALUES ('audit-row', 'config-workspace', 'config-user', 'auth.failed', 'WARNING', '127.0.0.1', '{"status":"blocked","email":"target@example.com"}', unixepoch())
	`)

	fullReq := httptest.NewRequest(http.MethodGet, "/admin/auditoria", nil)
	fullRR := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(fullRR, fullReq, "admin-auditoria")

	if fullRR.Code != http.StatusOK {
		t.Fatalf("full status = %d, want %d, body = %q", fullRR.Code, http.StatusOK, fullRR.Body.String())
	}
	fullBody := fullRR.Body.String()
	for _, want := range []string{
		`id="main-content"`,
		`id="settings-dynamic-payload"`,
		`id="auditoria-table-body"`,
		`hx-target="#auditoria-table-body"`,
		`value="smtp.config.test"`,
		`value="2525"`,
		`value="alerts@config.test"`,
		`value="auth.failed" checked`,
		`value="workspace.edit" checked`,
		`auth.failed`,
		`target@example.com`,
	} {
		assertContains(t, fullBody, want)
	}

	handler.WorkspaceID = "missing-settings-workspace"
	handler.AuditEventFilter = "auth.failed"
	handler.AuditSeverityFilter = "warning"
	execTestSQL(t, handler.DB, `
		INSERT INTO security_logs (id, workspace_id, user_id, event_type, severity, ip_address, metadata, created_at)
		VALUES ('global-audit-row', NULL, 'config-user', 'auth.failed', 'WARNING', '10.0.0.1', '{"status":"denied","email_tentado":"global@example.com"}', unixepoch())
	`)

	rowsReq := httptest.NewRequest(http.MethodGet, "/admin/auditoria?event_type=auth.failed&severity=warning", nil)
	rowsReq.Header.Set("HX-Request", "true")
	rowsReq.Header.Set("HX-Target", "auditoria-table-body")
	rowsRR := httptest.NewRecorder()
	handler.HandleAdminAuditoriaRows(rowsRR, rowsReq)

	if rowsRR.Code != http.StatusOK {
		t.Fatalf("rows status = %d, want %d, body = %q", rowsRR.Code, http.StatusOK, rowsRR.Body.String())
	}
	assertContains(t, rowsRR.Body.String(), `<tr`)
	assertContains(t, rowsRR.Body.String(), `auth.failed`)
	assertContains(t, rowsRR.Body.String(), `WARNING`)
	assertContains(t, rowsRR.Body.String(), `global@example.com`)
	assertContains(t, rowsRR.Body.String(), `denied`)
	if strings.Contains(rowsRR.Body.String(), `id="settings-dynamic-payload"`) {
		t.Fatalf("audit rows fragment must not render the settings payload wrapper")
	}
}

func newConfiguracoesRenderTestHandler(t *testing.T) ConfiguracoesHandler {
	t.Helper()

	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES ('config-user', 'Config Admin', 'config@example.com', 'hash', 'active', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('config-workspace', 'Config Workspace', '', 'business', ?, ?),
			('config-secondary', 'Config Secondary', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('config-workspace', 'config-user', 'ADMIN', ?)
	`, now)

	return ConfiguracoesHandler{
		DB:             db,
		Templates:      testConfiguracoesRenderTemplates(t),
		WorkspaceID:    "config-workspace",
		UserID:         "config-user",
		ActorRole:      models.RoleAdmin,
		CanConfigRead:  true,
		CanConfigWrite: true,
	}
}

func testConfiguracoesRenderTemplates(t *testing.T) *template.Template {
	t.Helper()

	stubs := `
{{define "layout-start"}}<html><body><div id="main-content" data-user-name="{{.UserName}}" data-user-initials="{{.UserInitials}}" data-workspace-name="{{.ActiveWorkspaceName}}" data-workspace-business="{{.IsBusiness}}">{{end}}
{{define "layout-end"}}</div></body></html>{{end}}
`
	tpl := template.Must(template.New("configuracoes-test").Funcs(template.FuncMap{
		"filterCategories": func(categories []ConfigCategoryRow, typ string) []ConfigCategoryRow {
			var filtered []ConfigCategoryRow
			for _, category := range categories {
				if category.Type == typ {
					filtered = append(filtered, category)
				}
			}
			return filtered
		},
	}).Parse(stubs))
	files := []string{
		"templates/components/financial-emblem.html",
		"templates/pages/configuracoes.html",
		"templates/pages/configuracoes_components.html",
		"templates/pages/configuracoes_perfil.html",
		"templates/pages/configuracoes_workspace.html",
		"templates/pages/configuracoes_categorias.html",
		"templates/pages/configuracoes_contas.html",
		"templates/pages/configuracoes_cartoes.html",
		"templates/pages/configuracoes_admin_users.html",
		"templates/pages/configuracoes_admin_workspaces.html",
		"templates/pages/configuracoes_admin_backups.html",
		"templates/pages/configuracoes_admin_auditoria.html",
	}
	for _, relative := range files {
		path := resolveTemplatePath(t, relative)
		content, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		if _, err := tpl.Parse(string(content)); err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
	}
	return tpl
}
