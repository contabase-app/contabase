package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCriarLimiteDuplicadoRetornaErroAmigavel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedLimitDuplicateScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	form := url.Values{}
	form.Set("category_id", "cat-a1")
	form.Set("max_amount_monthly", "100,00")
	req := httptest.NewRequest(http.MethodPost, "/metas/limite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleCriarLimite(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Já existe um limite para esta categoria") {
		t.Fatalf("expected duplicate error message, got: %s", body)
	}
}

func TestEditarLimiteMesmaCategoriaPassa(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedLimitDuplicateScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	form := url.Values{}
	form.Set("limit_id", "limit-a1")
	form.Set("category_id", "cat-a1")
	form.Set("max_amount_monthly", "200,00")
	req := httptest.NewRequest(http.MethodPost, "/metas/limite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleCriarLimite(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Já existe um limite") {
		t.Fatalf("edit with same category should succeed, got error: %s", body)
	}
	if w.Code >= 400 {
		t.Fatalf("expected success, got status %d: %s", w.Code, body)
	}
}

func TestEditarLimiteParaCategoriaDeOutroLimiteBloqueado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedLimitDuplicateScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	form := url.Values{}
	form.Set("limit_id", "limit-a1")
	form.Set("category_id", "cat-a2")
	form.Set("max_amount_monthly", "200,00")
	req := httptest.NewRequest(http.MethodPost, "/metas/limite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleCriarLimite(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Já existe um limite para esta categoria") {
		t.Fatalf("edit to taken category should fail, got: %s", body)
	}
}

func TestCriarLimiteOutroWorkspaceNaoBloqueado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedLimitDuplicateScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-b",
		UserID:      "user-b",
	}

	form := url.Values{}
	form.Set("category_id", "cat-b1")
	form.Set("max_amount_monthly", "500,00")
	req := httptest.NewRequest(http.MethodPost, "/metas/limite", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleCriarLimite(w, req)

	body := w.Body.String()
	if strings.Contains(body, "Já existe um limite") {
		t.Fatalf("different workspace should not block, got error: %s", body)
	}
	if w.Code >= 400 {
		t.Fatalf("expected success, got status %d: %s", w.Code, body)
	}
}

func seedLimitDuplicateScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-a','A','a@x.com','h',?,?), ('user-b','B','b@x.com','h',?,?)`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, created_at, updated_at) VALUES ('ws-a','WA','','personal',?,?), ('ws-b','WB','','personal',?,?)`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-a','user-a','ADMIN',?), ('ws-b','user-b','ADMIN',?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-a','ws-a','C','CHECKING',0,0,?,?), ('acc-b','ws-b','C','CHECKING',0,0,?,?)`, now, now, now, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at) VALUES ('cat-a1','ws-a','Cat A1','tag','#6b7280','EXPENSE',?), ('cat-a2','ws-a','Cat A2','tag','#6b7280','EXPENSE',?), ('cat-b1','ws-b','Cat B1','tag','#6b7280','EXPENSE',?)`, now, now, now)
	execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES ('limit-a1','ws-a','cat-a1',10000), ('limit-a2','ws-a','cat-a2',20000)`)
}
