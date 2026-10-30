package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildMetasDataLoadsRenderMetadata(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

	now := time.Now().UTC().Unix()
	uploadRoot := t.TempDir()
	t.Setenv("UPLOADS_DIR", uploadRoot)

	execTestSQL(t, db, `UPDATE users SET profile_photo_path = '/uploads/profile/user-a.png', updated_at = ? WHERE id = 'user-a'`, now)

	profileDir := filepath.Join(uploadRoot, "profile")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("create profile dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "user-a.png"), []byte("profile"), 0o600); err != nil {
		t.Fatalf("write profile file: %v", err)
	}

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	if data.UserName != "User A" {
		t.Fatalf("UserName = %q, want %q", data.UserName, "User A")
	}
	if data.UserFirstName != "User" {
		t.Fatalf("UserFirstName = %q, want %q", data.UserFirstName, "User")
	}
	if data.UserInitials != "UA" {
		t.Fatalf("UserInitials = %q, want %q", data.UserInitials, "UA")
	}
	if data.ActiveWorkspaceName != "Workspace A" {
		t.Fatalf("ActiveWorkspaceName = %q, want %q", data.ActiveWorkspaceName, "Workspace A")
	}
	if data.IsBusiness {
		t.Fatalf("IsBusiness = true, want false")
	}
	wantPhoto := fmt.Sprintf("/uploads/profile/user-a.png?v=%d", now)
	if data.ProfilePhotoURL != wantPhoto {
		t.Fatalf("ProfilePhotoURL = %q, want %q", data.ProfilePhotoURL, wantPhoto)
	}
}

func TestLoadMetasRenderMetadataFallsBackForMissingUser(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-meta', 'Workspace Meta', '', 'business', ?, ?)
	`, now, now)

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		WorkspaceID: "ws-meta",
		UserID:      "user-missing",
	}

	meta := h.loadMetasRenderMetadata()
	if meta.UserName != "Usuário" {
		t.Fatalf("UserName = %q, want %q", meta.UserName, "Usuário")
	}
	if meta.UserFirstName != "Usuário" {
		t.Fatalf("UserFirstName = %q, want %q", meta.UserFirstName, "Usuário")
	}
	if meta.UserInitials != "US" {
		t.Fatalf("UserInitials = %q, want %q", meta.UserInitials, "US")
	}
	if meta.ProfilePhotoURL != "" {
		t.Fatalf("ProfilePhotoURL = %q, want empty", meta.ProfilePhotoURL)
	}
	if meta.ActiveWorkspaceName != "Workspace Meta" {
		t.Fatalf("ActiveWorkspaceName = %q, want %q", meta.ActiveWorkspaceName, "Workspace Meta")
	}
	if !meta.IsBusiness {
		t.Fatalf("IsBusiness = false, want true")
	}
}

func TestHandleListarMetasRendersCaixinhaStates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-a', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES ('lv-excedente', 'a-box-no-recharge', -2500, 'CONSUME', 'Consumo que gera excedente', ?, ?)
	`, now, now)

	execTestSQL(t, db, `UPDATE boxes SET category_id = NULL WHERE id = 'a-box-rounded'`)

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	t.Run("full page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas", nil)
		rr := httptest.NewRecorder()

		h.HandleListarMetas(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}

		body := rr.Body.String()
		currentMonth := time.Date(time.Now().UTC().Year(), time.Now().UTC().Month(), 1, 12, 0, 0, 0, time.UTC)
		targetLabel := formatMonthLabelFromUnix(currentMonth.AddDate(0, 3, 0).Unix())
		assertContains(t, body, "excedente")
		assertContains(t, body, "Previsão:")
		assertContains(t, body, "Sem categoria")
		assertContains(t, body, "Sem prazo")
		assertContains(t, body, targetLabel)
		if strings.Contains(body, "No prazo") {
			t.Fatalf("body should not contain generic deadline badge No prazo")
		}
		assertContains(t, body, `hx-get="/metas/novo?aba=caixinha&id=`)
	})

	t.Run("htmx partial", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas&partial=conteudo", nil)
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()

		h.HandleListarMetas(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}

		body := rr.Body.String()
		assertContains(t, body, `id="metas-tabs"`)
		assertContains(t, body, `id="metas-primary-action"`)
		assertContains(t, body, `hx-swap-oob="outerHTML"`)
		assertContains(t, body, `Nova reserva`)
		if strings.Contains(body, `id="fab-primary"`) {
			t.Fatalf("partial metas response must not replace the global launch FAB\nbody=%s", body)
		}
		assertContains(t, body, "Previsão:")
		assertContains(t, body, "Sem categoria")
	})

	t.Run("htmx full page keeps global launch FAB intact", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas", nil)
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()

		h.HandleListarMetas(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}

		body := rr.Body.String()
		if strings.Contains(body, `hx-swap-oob="outerHTML"`) {
			t.Fatalf("full htmx metas response must not emit a header action swap\nbody=%s", body)
		}
		if strings.Contains(body, `id="fab-primary"`) {
			t.Fatalf("htmx metas response must not replace the global launch FAB\nbody=%s", body)
		}
	})
}

func TestMetasPageUsesGlobalAppShell(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	data := MetasData{
		Title:               "Metas",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		Aba:                 "limites",
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-page", data); err != nil {
		t.Fatalf("render metas page: %v", err)
	}

	body := out.String()
	for _, expected := range []string{
		`data-app-shell`,
		`data-page-section="goals"`,
		`data-v2-nav-section="goals"`,
		`cb-v2-metas-header`,
		`id="metas-primary-action"`,
		`Novo limite`,
		`/assets/css/metas.css?v=test`,
		`/assets/js/metas-caixinhas.js?v=test`,
		`/assets/js/form-meta.js?v=test`,
	} {
		assertContains(t, body, expected)
	}
	if strings.Contains(body, `{{template "layout-start"`) {
		t.Fatal("metas page must use the global app shell, not the legacy layout")
	}
}

func TestMetasHeaderActionUsesWorkspaceContext(t *testing.T) {
	tpl := testMetasPageTemplates(t)

	for _, tc := range []struct {
		name       string
		aba        string
		isBusiness bool
		want       string
	}{
		{name: "personal limit", aba: "limites", want: "Novo limite"},
		{name: "business goal", aba: "limites", isBusiness: true, want: "Nova meta"},
		{name: "reserve", aba: "caixinhas", want: "Nova reserva"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := tpl.ExecuteTemplate(&out, "metas-header-action", MetasData{Aba: tc.aba, IsBusiness: tc.isBusiness}); err != nil {
				t.Fatalf("render metas action: %v", err)
			}
			assertContains(t, out.String(), tc.want)
		})
	}
}

func TestMetasTransactionLinksUseDocumentNavigation(t *testing.T) {
	pageBytes, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/metas.html"))
	if err != nil {
		t.Fatalf("read metas page: %v", err)
	}
	page := string(pageBytes)
	assertContains(t, page, `<a href="/lancamentos?categoria={{.CategoryID}}" data-ck-close class="cb-meta-menu-item">`)
	if strings.Contains(page, `hx-get="/lancamentos?categoria={{.CategoryID}}"`) {
		t.Fatal("Metas transaction links must use document navigation to preserve the V2 app shell")
	}
}

func TestMetaFormTabsUseDedicatedController(t *testing.T) {
	formBytes, err := os.ReadFile(resolveTemplatePath(t, "templates/components/form_meta.html"))
	if err != nil {
		t.Fatalf("read metas form template: %v", err)
	}
	form := string(formBytes)
	assertContains(t, form, `data-meta-tab="limite"`)
	assertContains(t, form, `data-meta-tab="caixinha"`)
	if strings.Contains(form, `data-onclick-tab="toggleMetaTab"`) {
		t.Fatal("metas form tabs must use their dedicated controller instead of global click delegation")
	}

	scriptBytes, err := os.ReadFile(resolveTemplatePath(t, "assets/js/form-meta.js"))
	if err != nil {
		t.Fatalf("read metas form script: %v", err)
	}
	script := string(scriptBytes)
	assertContains(t, script, `function initMetaTabs(root)`)
	assertContains(t, script, `data-meta-tab-ready`)
	assertContains(t, script, `window.toggleMetaTab(tabButton.dataset.metaTab || 'limite')`)
}

func TestHandleListarMetasRendersLimitCards(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedLimitDuplicateScenario(t, db)

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas?aba=limites", nil)
	rr := httptest.NewRecorder()

	h.HandleListarMetas(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	assertContains(t, body, `/metas/limite/historico?limit_id=`)
	assertContains(t, body, `hx-target="#bottom-sheet-container"`)
	assertContains(t, body, `hx-get="/metas/novo?aba=limite&id=`)
	assertContains(t, body, `hx-push-url="false"`)
}

func TestHandleNovaMetaEditsBoxWithoutCategory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)
	execTestSQL(t, db, `UPDATE boxes SET category_id = NULL WHERE id = 'a-box-rounded'`)

	h := MetasHandler{ActorRole: testMetasAdminRole,
		DB:          db,
		Templates:   testMetasFormTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/novo?aba=caixinha&id=a-box-rounded", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()

	h.HandleNovaMeta(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	assertContains(t, body, `data-open-tab="caixinha"`)
	assertContains(t, body, `name="box_id" value="a-box-rounded"`)
	assertContains(t, body, `name="category_id" value=""`)
	assertContains(t, body, `Sem categoria`)
	assertContains(t, body, `aria-label="Salvar reserva"`)
}

func TestBuildMetasDataHandlesLegacyBoxesWithoutTargetDateColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)
	restoreClassificationReserveGuards := suspendClassificationReserveTriggersForFixture(t, db)

	execTestSQL(t, db, `PRAGMA foreign_keys = OFF`)
	execTestSQL(t, db, `
		CREATE TABLE boxes_legacy (
			id               TEXT PRIMARY KEY,
			workspace_id     TEXT    NOT NULL,
			name             TEXT    NOT NULL,
			description      TEXT    NOT NULL DEFAULT '',
			category_id      TEXT    NOT NULL,
			target_amount    INTEGER NOT NULL DEFAULT 0,
			monthly_recharge INTEGER NOT NULL DEFAULT 0,
			created_at       INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at       INTEGER NOT NULL DEFAULT (unixepoch())
		)
	`)
	execTestSQL(t, db, `
		INSERT INTO boxes_legacy (id, workspace_id, name, description, category_id, target_amount, monthly_recharge, created_at, updated_at)
		SELECT id, workspace_id, name, description, category_id, target_amount, monthly_recharge, created_at, updated_at
		FROM boxes
	`)
	execTestSQL(t, db, `DROP TABLE boxes`)
	execTestSQL(t, db, `ALTER TABLE boxes_legacy RENAME TO boxes`)
	execTestSQL(t, db, `CREATE INDEX IF NOT EXISTS idx_boxes_workspace ON boxes(workspace_id)`)
	execTestSQL(t, db, `CREATE INDEX IF NOT EXISTS idx_boxes_workspace_category ON boxes(workspace_id, category_id)`)
	restoreClassificationReserveGuards()
	execTestSQL(t, db, `PRAGMA foreign_keys = ON`)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	data, err := h.buildMetasData("", "caixinhas")
	if err != nil {
		t.Fatalf("buildMetasData legacy boxes schema: %v", err)
	}
	if len(data.Caixinhas) == 0 {
		t.Fatalf("caixinhas count = 0, want > 0")
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
