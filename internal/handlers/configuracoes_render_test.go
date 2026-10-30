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
		`data-app-shell`,
		`data-page-section="settings"`,
		`/assets/css/configuracoes.css`,
		`cb-v2-hub-grid`,
		`cb-v2-hub-card`,
		`data-v2-nav-group-key="settings"`,
		`data-v2-nav-group-key="administration"`,
		`href="/configuracoes/perfil"`,
		`id="global-confirm-modal"`,
		`/assets/js/modals-global.js`,
		`/assets/js/swipe.js`,
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
			assertContains(t, fullRR.Body.String(), `data-v2-nav-group-key="settings"`)
			if strings.HasPrefix(section, "admin-") {
				assertContains(t, fullRR.Body.String(), `data-v2-nav-group-key="administration"`)
			} else {
				assertContains(t, fullRR.Body.String(), `href="/configuracoes/`+section+`"`)
			}
			if section == "perfil" || section == "workspace" {
				assertContains(t, fullRR.Body.String(), `data-app-shell`)
			}

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

func TestConfiguracoesAccountAndCardFormsUseGlobalProviderLogoPicker(t *testing.T) {
	for _, section := range []string{"contas", "cartoes"} {
		t.Run(section, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)
			req := httptest.NewRequest(http.MethodGet, "/configuracoes/"+section, nil)
			req.Header.Set("HX-Request", "true")
			rr := httptest.NewRecorder()
			handler.HandleConfiguracoesSection(rr, req, section)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
			}
			for _, want := range []string{
				`data-provider-picker`,
				`data-provider-open`,
				`data-provider-dialog`,
				`data-provider-search`,
				`class="cb-provider-choice-check"`,
				`data-provider-choice`,
				`data-provider-slug="itau"`,
				`src="institutions/itau.png?v=test"`,
				`data-provider-slug="custom"`,
				`name="provider_slug" data-provider-value`,
				`<option value="" selected>Escolha a instituição</option>`,
			} {
				assertContains(t, rr.Body.String(), want)
			}
			body := rr.Body.String()
			customIndex := strings.Index(body, `data-provider-custom-choice`)
			itauIndex := strings.Index(body, `data-provider-slug="itau" data-provider-name="Itaú"`)
			if customIndex < 0 || itauIndex < 0 || customIndex > itauIndex {
				t.Fatalf("custom provider must be the first visual option")
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

func TestConfiguracoesAdminBackupsShowsSafeAutomaticBackupMetadata(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	backupName := "contabase-daily-20260825.db"
	backupPath := filepath.Join(handler.AutomaticBackupsDir, backupName)
	if err := os.WriteFile(backupPath, []byte("sqlite-backup"), 0o600); err != nil {
		t.Fatalf("write automatic backup fixture: %v", err)
	}
	createdAt := time.Date(2026, 8, 25, 14, 25, 0, 0, time.UTC)
	if err := os.Chtimes(backupPath, createdAt, createdAt); err != nil {
		t.Fatalf("set automatic backup fixture time: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/backups", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, "admin-backups")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`DATA_DIR/backups/`,
		`retenção: últimos 7 dias`,
		`Arquivos diários encontrados`,
		backupName,
		`25/08/2026 14:25`,
		`13 B`,
		`não confirma procedência, integridade nem conclusão da rotina.`,
	} {
		assertContains(t, body, want)
	}
	if strings.Contains(body, handler.AutomaticBackupsDir) {
		t.Fatalf("backup page must not expose the absolute directory %q", handler.AutomaticBackupsDir)
	}
	if strings.Contains(body, `href="`+backupName) {
		t.Fatalf("automatic backup metadata must not create a download link")
	}
}

func TestListAutomaticBackupRowsFiltersSortsAndCaps(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	dir := t.TempDir()
	names := []string{
		"contabase-daily-20260818.db",
		"contabase-daily-20260819.db",
		"contabase-daily-20260820.db",
		"contabase-daily-20260821.db",
		"contabase-daily-20260822.db",
		"contabase-daily-20260823.db",
		"contabase-daily-20260824.db",
		"contabase-daily-20260825.db",
	}
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	for i, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 2048)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		modTime := base.AddDate(0, 0, i)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("set time %s: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "manual.db"), []byte("manual"), 0o600); err != nil {
		t.Fatalf("write unrelated db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "contabase-note.txt"), []byte("note"), 0o600); err != nil {
		t.Fatalf("write unrelated text: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "contabase-directory.db"), 0o700); err != nil {
		t.Fatalf("create unrelated directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, names[7]), filepath.Join(dir, "contabase-daily-20260826.db")); err != nil {
		t.Logf("symlink fixture unavailable: %v", err)
	}

	rows, err := listAutomaticBackupRows(dir, 7)
	if err != nil {
		t.Fatalf("list automatic backups: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("rows = %d, want 7: %+v", len(rows), rows)
	}
	if rows[0].Name != names[7] || rows[6].Name != names[1] {
		t.Fatalf("unexpected order/cap: %+v", rows)
	}
	if rows[0].CreatedAt != "25/08/2026 12:00" || rows[0].Size != "2.0 KB" {
		t.Fatalf("unexpected metadata: %+v", rows[0])
	}
	for _, row := range rows {
		if filepath.Base(row.Name) != row.Name || row.Name == "contabase-daily-20260826.db" {
			t.Fatalf("unsafe backup name returned: %+v", row)
		}
	}

	missingRows, err := listAutomaticBackupRows(filepath.Join(dir, "missing"), 7)
	if err != nil || len(missingRows) != 0 {
		t.Fatalf("missing directory = (%+v, %v), want empty without error", missingRows, err)
	}
}

func TestConfiguracoesAdminUsersRenderWorkspaceRoleControls(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES ('config-target', 'Config Target', 'target@example.com', 'hash', 'active', ?, ?)
	`, now, now)
	execTestSQL(t, handler.DB, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('config-third', 'Config Third', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, handler.DB, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('config-workspace', 'config-target', 'ADMIN', ?),
			('config-secondary', 'config-target', 'USER', ?)
	`, now, now)

	req := httptest.NewRequest(http.MethodGet, "/admin/usuarios", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()

	handler.HandleConfiguracoesSection(rr, req, "admin-users")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{
		`name="workspace_roles[config-workspace]"`,
		`name="workspace_roles[config-secondary]"`,
		`name="workspace_roles[config-third]"`,
		`value="config-workspace" class="sr-only peer" data-admin-user-workspace-toggle checked`,
		`value="config-secondary" class="sr-only peer" data-admin-user-workspace-toggle checked`,
		`id="workspace-role-config-target-config-third"`,
		`data-admin-user-workspace-role class="ui-select admin-user-role-select" disabled`,
	} {
		assertContains(t, body, want)
	}
	if strings.Contains(body, `<span class="cb-label">Permissão</span><select name="role" class="cb-input"><option value="USER" selected`) {
		t.Fatalf("edit form must not keep the old global permission field")
	}
	assertContains(t, selectMarkupByID(t, body, "workspace-role-config-target-config-workspace"), `value="ADMIN" selected`)
	assertContains(t, selectMarkupByID(t, body, "workspace-role-config-target-config-secondary"), `value="USER" selected`)
	assertContains(t, selectMarkupByID(t, body, "workspace-role-config-target-config-third"), `value="USER" selected`)
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

func newConfiguracoesPersonalRenderTestHandler(t *testing.T) ConfiguracoesHandler {
	t.Helper()
	handler := newConfiguracoesRenderTestHandler(t)
	execTestSQL(t, handler.DB, `UPDATE workspaces SET type = 'personal' WHERE id = 'config-workspace'`)
	return handler
}

func TestConfiguracoesCategoriasMacroHelpBusiness(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, "categorias")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	businessTexts := []string{
		"Receitas Financeiras",
		"Retiradas/Distribui",
		"Investimentos/Patrimonial",
		"Investimentos/Outros",
		"data-is-business",
	}
	personalTexts := []string{
		"entradas de dinheiro",
		"gastos necess",
		"Estilo de Vida",
	}
	for _, text := range businessTexts {
		assertContains(t, body, text)
	}
	for _, text := range personalTexts {
		if strings.Contains(body, text) {
			t.Fatalf("Business categorias render must not contain Personal text: %q", text)
		}
	}
}

func TestConfiguracoesCategoriasMacroHelpPersonal(t *testing.T) {
	handler := newConfiguracoesPersonalRenderTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, "categorias")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	personalTexts := []string{
		"entradas de dinheiro",
		"gastos necess",
		"Estilo de Vida",
	}
	businessTexts := []string{
		"Receitas Financeiras",
		"Retiradas/Distribui",
		"Investimentos/Patrimonial",
		"Investimentos/Outros",
	}
	for _, text := range personalTexts {
		assertContains(t, body, text)
	}
	for _, text := range businessTexts {
		if strings.Contains(body, text) {
			t.Fatalf("Personal categorias render must not contain Business text: %q", text)
		}
	}
	if strings.Contains(body, "data-is-business") {
		t.Fatalf("Personal categorias render must not contain data-is-business attribute")
	}
}

func TestConfiguracoesCategoriasInlineEditHelpBusiness(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('cat-biz-edit', 'config-workspace', 'Categoria Biz', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', ?)
	`, now)

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias/cat-biz-edit/formulario", nil)
	rr := httptest.NewRecorder()
	handler.HandleCategoriasInlineForm(rr, req, "cat-biz-edit")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	businessHelp := "Receitas Financeiras = rendimentos/juros"
	assertContains(t, body, businessHelp)

	personalHelp := "gastos necess"
	if strings.Contains(body, personalHelp) {
		t.Fatalf("Business inline edit form must not contain Personal text: %q", personalHelp)
	}
}

func TestConfiguracoesCategoriasInlineEditHelpPersonal(t *testing.T) {
	handler := newConfiguracoesPersonalRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('cat-pers-edit', 'config-workspace', 'Categoria Personal', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now)

	req := httptest.NewRequest(http.MethodGet, "/configuracoes/categorias/cat-pers-edit/formulario", nil)
	rr := httptest.NewRecorder()
	handler.HandleCategoriasInlineForm(rr, req, "cat-pers-edit")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()

	personalTexts := []string{
		"gastos necess",
		"Estilo de Vida",
	}
	for _, text := range personalTexts {
		assertContains(t, body, text)
	}

	businessHelp := "Receitas Financeiras = rendimentos/juros"
	if strings.Contains(body, businessHelp) {
		t.Fatalf("Personal inline edit form must not contain Business text: %q", businessHelp)
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
		DB:                  db,
		Templates:           testConfiguracoesRenderTemplates(t),
		AutomaticBackupsDir: t.TempDir(),
		WorkspaceID:         "config-workspace",
		UserID:              "config-user",
		ActorRole:           models.RoleAdmin,
		CanConfigRead:       true,
		CanConfigWrite:      true,
	}
}

func testConfiguracoesRenderTemplates(t *testing.T) *template.Template {
	t.Helper()

	tpl := template.New("configuracoes-test").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"filterCategories": func(categories []ConfigCategoryRow, typ string) []ConfigCategoryRow {
			var filtered []ConfigCategoryRow
			for _, category := range categories {
				if category.Type == typ {
					filtered = append(filtered, category)
				}
			}
			return filtered
		},
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
		"mapGet": func(m map[string]string, key string) string {
			return m[key]
		},
	}))
	files := []string{
		"templates/components/app_shell.html",
		"templates/components/financial-emblem.html",
		"templates/components/investment_patrimony.html",
		"templates/components/icon_picker.html",
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

func selectMarkupByID(t *testing.T, body, id string) string {
	t.Helper()
	start := strings.Index(body, `id="`+id+`"`)
	if start < 0 {
		t.Fatalf("select %s not found", id)
	}
	end := strings.Index(body[start:], `</select>`)
	if end < 0 {
		t.Fatalf("select %s has no closing tag", id)
	}
	return body[start : start+end]
}
