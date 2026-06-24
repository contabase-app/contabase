package handlers

import (
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

	h := MetasHandler{
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

	h := MetasHandler{
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

	execTestSQL(t, db, `PRAGMA foreign_keys = OFF`)
	execTestSQL(t, db, `UPDATE boxes SET category_id = 'cat-inexistente' WHERE id = 'a-box-rounded'`)
	execTestSQL(t, db, `PRAGMA foreign_keys = ON`)

	h := MetasHandler{
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
		// promoted template shows empty category name instead of "Sem categoria vinculada" text
		assertContains(t, body, "excedente")
		assertContains(t, body, "Previsão:")
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
		assertContains(t, body, `hx-swap-oob="outerHTML"`)
		if count := strings.Count(body, `id="fab-primary"`); count != 1 {
			t.Fatalf("fab-primary count = %d, want 1\nbody=%s", count, body)
		}
		// promoted template shows empty category name instead of "Sem categoria vinculada" text
		assertContains(t, body, "Previsão:")
	})

	t.Run("htmx full page keeps fab oob", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas", nil)
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()

		h.HandleListarMetas(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}

		body := rr.Body.String()
		assertContains(t, body, `fab-primary`)
		assertContains(t, body, `hx-swap-oob="outerHTML"`)
		if count := strings.Count(body, `id="fab-primary"`); count != 1 {
			t.Fatalf("fab-primary count = %d, want 1\nbody=%s", count, body)
		}
	})
}

func TestHandleListarMetasRendersLimitCards(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedLimitDuplicateScenario(t, db)

	h := MetasHandler{
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

func TestBuildMetasDataHandlesLegacyBoxesWithoutTargetDateColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

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
	execTestSQL(t, db, `PRAGMA foreign_keys = ON`)

	h := MetasHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
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
	content, err := os.ReadFile(metasPath)
	if err != nil {
		t.Fatalf("read metas template: %v", err)
	}

	stubs := `
{{define "layout-start"}}<html><body><div id="main-content">{{end}}
{{define "layout-end"}}</div></body></html>{{end}}
{{define "fab-metas-oob"}}<div id="fab-primary" hx-swap-oob="outerHTML">fab</div>{{end}}
`
	return template.Must(template.New("metas-test").Parse(stubs + string(content)))
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
