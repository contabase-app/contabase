package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/categorycontract"
)

type captureCategoryCreateTemplate struct {
	data CategoryCreateData
}

func (t *captureCategoryCreateTemplate) ExecuteTemplate(_ io.Writer, _ string, data any) error {
	if captured, ok := data.(CategoryCreateData); ok {
		t.data = captured
	}
	return nil
}

func (t *captureCategoryCreateTemplate) Lookup(string) *template.Template {
	return nil
}

func testCategoriasTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("test").Funcs(template.FuncMap{
		"filterCategories": func(categories []ConfigCategoryRow, typ string) []ConfigCategoryRow {
			var filtered []ConfigCategoryRow
			for _, c := range categories {
				if c.Type == typ {
					filtered = append(filtered, c)
				}
			}
			return filtered
		},
	}).Parse(`
{{define "configuracoes-categorias-content"}}<div id="settings-dynamic-payload">{{if .FlashError}}<div>{{.FlashError}}</div>{{end}}{{if .FlashSuccess}}<div>{{.FlashSuccess}}</div>{{end}}{{range .Categorias}}<div>{{.Name}}</div>{{end}}</div>{{end}}
{{define "config-categorias-row"}}<article>{{.Name}}</article>{{end}}
{{define "configuracoes-categorias-page"}}<html><body>{{template "configuracoes-categorias-content" .}}</body></html>{{end}}
`))
}

func seedCategoryParentTestScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-1', 'User 1', 'u1@test.com', 'hash', ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-1', 'ADMIN', ?),
			('ws-b', 'user-1', 'ADMIN', ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES
			('root-exp-a', 'ws-a', 'Root Expense A', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('child-exp-a', 'ws-a', 'Child Expense A', 'tag', '#6b7280', 'EXPENSE', 'Essencial', 'root-exp-a', ?),
			('root-inc-a', 'ws-a', 'Root Income A', 'tag', '#6b7280', 'INCOME', 'Essencial', NULL, ?),
			('child-inc-a', 'ws-a', 'Child Income A', 'tag', '#6b7280', 'INCOME', 'Essencial', 'root-inc-a', ?),
			('root-exp-b', 'ws-b', 'Root Expense B', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('root-inc-b', 'ws-b', 'Root Income B', 'tag', '#6b7280', 'INCOME', 'Essencial', NULL, ?)
	`, now, now, now, now, now, now)
}

func newConfigHandlerForWS(t *testing.T, db *sql.DB, workspaceID string) *ConfiguracoesHandler {
	t.Helper()
	return &ConfiguracoesHandler{
		DB:          db,
		Templates:   testCategoriasTemplates(t),
		WorkspaceID: workspaceID,
		UserID:      "user-1",
		ActorRole:   "ADMIN",
	}
}

func assertCategoryExists(t *testing.T, db *sql.DB, categoryID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE id = ?`, categoryID).Scan(&count); err != nil {
		t.Fatalf("query category %s: %v", categoryID, err)
	}
	if count != 1 {
		t.Fatalf("category %s count = %d, want 1", categoryID, count)
	}
}

func assertCategoryNotExists(t *testing.T, db *sql.DB, categoryID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM categories WHERE id = ?`, categoryID).Scan(&count); err != nil {
		t.Fatalf("query category %s: %v", categoryID, err)
	}
	if count != 0 {
		t.Fatalf("category %s count = %d, want 0", categoryID, count)
	}
}

func assertCategoryParentID(t *testing.T, db *sql.DB, categoryID, want string) {
	t.Helper()
	var got sql.NullString
	if err := db.QueryRow(`SELECT parent_id FROM categories WHERE id = ?`, categoryID).Scan(&got); err != nil {
		t.Fatalf("query category parent_id %s: %v", categoryID, err)
	}
	gotStr := ""
	if got.Valid {
		gotStr = got.String
	}
	if gotStr != want {
		t.Fatalf("category %s parent_id = %q, want %q", categoryID, gotStr, want)
	}
}

func assertCategoryMacroGroup(t *testing.T, db *sql.DB, categoryID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT COALESCE(macro_group, '') FROM categories WHERE id = ?`, categoryID).Scan(&got); err != nil {
		t.Fatalf("query category macro_group %s: %v", categoryID, err)
	}
	if got != want {
		t.Fatalf("category %s macro_group = %q, want %q", categoryID, got, want)
	}
}

func TestHandleCategoriasCreate_RejectsChildAsParent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Nova Subcategoria"},
		"type":        {"EXPENSE"},
		"parent_id":   {"child-exp-a"},
		"macro_group": {"Essencial"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "não encontrada") && !strings.Contains(body, "inválida") {
		t.Fatalf("expected parent validation error in response, got: %s", body)
	}
}

func TestHandleCategoriasCreate_AcceptsValidRootParent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Nova Subcategoria"},
		"type":        {"EXPENSE"},
		"parent_id":   {"root-exp-a"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "criada com sucesso") {
		t.Fatalf("expected success message, got: %s", body)
	}
}

func TestHandleCategoriasCreate_RejectsParentFromOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Nova Subcategoria"},
		"type":        {"EXPENSE"},
		"parent_id":   {"root-exp-b"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "não encontrada") && !strings.Contains(body, "inválida") {
		t.Fatalf("expected parent validation error in response, got: %s", body)
	}
}

func TestHandleCategoriasCreate_RejectsParentOfOtherType(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Nova Subcategoria Despesa"},
		"type":        {"EXPENSE"},
		"parent_id":   {"root-inc-a"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "mesmo tipo") {
		t.Fatalf("expected 'mesmo tipo' error in response, got: %s", body)
	}
}

func TestHandleCategoriasEdit_RejectsChildAsParent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Root Expense A Updated"},
		"type":        {"EXPENSE"},
		"parent_id":   {"child-exp-a"},
		"macro_group": {"Essencial"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/root-exp-a", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasEdit(rr, req, "root-exp-a")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "não encontrada") && !strings.Contains(body, "inválida") {
		t.Fatalf("expected parent validation error in response, got: %s", body)
	}
}

func TestHandleCategoriasInlineSave_RejectsChildAsParent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Root Expense A Updated"},
		"parent_id":   {"child-exp-a"},
		"macro_group": {"Essencial"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/root-exp-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "root-exp-a")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "inválida") {
		t.Fatalf("expected parent validation error in response, got: %s", body)
	}
}

func TestHandleCategoriasInlineSave_KeepsCanonicalMacroGroupOnUpdate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	preMacro := ""
	if err := db.QueryRow(`SELECT COALESCE(macro_group, '') FROM categories WHERE id = 'root-exp-a'`).Scan(&preMacro); err != nil {
		t.Fatalf("pre-query macro_group: %v", err)
	}
	if preMacro != "Essencial" {
		t.Fatalf("pre-update macro_group = %q, want %q", preMacro, "Essencial")
	}

	form := url.Values{
		"name":        {"Root Expense A Updated"},
		"parent_id":   {""},
		"macro_group": {"Essencial"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/root-exp-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "root-exp-a")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	postMacro := ""
	if err := db.QueryRow(`SELECT COALESCE(macro_group, '') FROM categories WHERE id = 'root-exp-a'`).Scan(&postMacro); err != nil {
		t.Fatalf("post-query macro_group: %v", err)
	}
	if postMacro != "Essencial" {
		t.Fatalf("post-update macro_group = %q, want %q (should preserve canonical name)", postMacro, "Essencial")
	}
}

func TestHandleCategoriasInlineSave_ParentInheritsCorrectMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"New Child of Root"},
		"parent_id":   {"root-exp-a"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/child-exp-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "child-exp-a")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	postMacro := ""
	if err := db.QueryRow(`SELECT COALESCE(macro_group, '') FROM categories WHERE id = 'child-exp-a'`).Scan(&postMacro); err != nil {
		t.Fatalf("post-query macro_group: %v", err)
	}
	if postMacro != "Essencial" {
		t.Fatalf("post-update child macro_group = %q, want %q (should inherit parent's canonical macro)", postMacro, "Essencial")
	}
}

func TestQueryCategoryParentOptions_ExcludesChildCategories(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	options, err := h.queryCategoryParentOptions("root-exp-a", "EXPENSE", "Essencial")
	if err != nil {
		t.Fatalf("queryCategoryParentOptions error: %v", err)
	}

	for _, opt := range options {
		if opt.ID == "child-exp-a" || opt.ID == "child-inc-a" {
			t.Fatalf("parent options include child category %s (%s)", opt.ID, opt.Name)
		}
	}

	hasRootExp := false
	for _, opt := range options {
		if opt.ID == "root-exp-a" {
			hasRootExp = true
			break
		}
	}
	if hasRootExp {
		t.Fatalf("parent options should exclude current category (root-exp-a)")
	}

	// All other root categories in this scenario have different type,
	// so 0 matching options is correct (current ID excluded, type mismatch for income).
	// The important validation: no child categories appear.
	for _, opt := range options {
		if opt.ID == "child-exp-a" || opt.ID == "child-inc-a" {
			t.Fatalf("parent options include child category %s (%s)", opt.ID, opt.Name)
		}
	}
}

func TestDefaultMacroGroupForWorkspace_ReturnsCanonicalValues(t *testing.T) {
	tests := []struct {
		name       string
		isBusiness bool
		typ        string
		want       string
	}{
		{"personal income", false, "INCOME", "Receitas"},
		{"personal expense", false, "EXPENSE", "Estilo de Vida"},
		{"business income", true, "INCOME", "Receitas Operacionais"},
		{"business expense", true, "EXPENSE", "Custos Operacionais"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultMacroGroupForWorkspace(tt.isBusiness, tt.typ)
			if got != tt.want {
				t.Fatalf("defaultMacroGroupForWorkspace(%v, %q) = %q, want %q",
					tt.isBusiness, tt.typ, got, tt.want)
			}
		})
	}
}

func TestConfiguracoesCategoriasTemplateUsesCanonicalBusinessDefaultMacro(t *testing.T) {
	source, err := os.ReadFile("../../templates/pages/configuracoes_categorias.html")
	if err != nil {
		t.Fatalf("read categorias template: %v", err)
	}
	compSource, err := os.ReadFile("../../templates/pages/configuracoes_components.html")
	if err != nil {
		t.Fatalf("read components template: %v", err)
	}
	tpl := template.Must(template.New("categorias").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"filterCategories": func(categories []ConfigCategoryRow, typ string) []ConfigCategoryRow {
			var filtered []ConfigCategoryRow
			for _, c := range categories {
				if c.Type == typ {
					filtered = append(filtered, c)
				}
			}
			return filtered
		},
	}).Parse(string(compSource)))
	tpl = template.Must(tpl.Parse(string(source)))

	var buf bytes.Buffer
	data := struct {
		IsBusiness            bool
		Categorias            []ConfigCategoryRow
		FlashError            string
		FlashSuccess          string
		CategoryIncomeMacros  []categorycontract.MacroGroup
		CategoryExpenseMacros []categorycontract.MacroGroup
	}{
		IsBusiness:            true,
		CategoryIncomeMacros:  categoryMacroGroupsForTemplate(true, "INCOME"),
		CategoryExpenseMacros: categoryMacroGroupsForTemplate(true, "EXPENSE"),
	}
	if err := tpl.ExecuteTemplate(&buf, "configuracoes-categorias-content", data); err != nil {
		t.Fatalf("execute categorias content: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, `data-cat-default-macro="Custos Operacionais"`) {
		t.Fatalf("business default macro should be canonical, body: %s", body)
	}
	if strings.Contains(body, "OPERATING_COSTS") {
		t.Fatalf("business default macro rendered legacy value: %s", body)
	}
}

func seedBusinessCategoryParentTestScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-biz', 'Biz User', 'biz@test.com', 'hash', ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-biz', 'Business WS', '', 'business', ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-biz', 'user-biz', 'ADMIN', ?)
	`, now)

	execTestSQL(t, db, fmt.Sprintf(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES
			('biz-root-exp', 'ws-biz', 'Biz Root Expense', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', NULL, %d),
			('biz-child-exp', 'ws-biz', 'Biz Child Expense', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', 'biz-root-exp', %d),
			('biz-root-inc', 'ws-biz', 'Biz Root Income', 'tag', '#6b7280', 'INCOME', 'Receitas Operacionais', NULL, %d)
	`, now, now, now))
}

func TestHandleCategoriasCreate_BusinessRejectsChildAsParent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBusinessCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-biz")

	form := url.Values{
		"name":        {"Nova Subcategoria Biz"},
		"type":        {"EXPENSE"},
		"parent_id":   {"biz-child-exp"},
		"macro_group": {"Custos Operacionais"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "não encontrada") && !strings.Contains(body, "inválida") {
		t.Fatalf("expected parent validation error in response, got: %s", body)
	}
}

func TestHandleCategoriasCreate_BusinessCreatesWithParentInheritsMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBusinessCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-biz")

	form := url.Values{
		"name":        {"Nova Subcategoria Biz"},
		"type":        {"EXPENSE"},
		"parent_id":   {"biz-root-exp"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "criada com sucesso") {
		t.Fatalf("expected success message, got: %s", body)
	}
}

func TestHandleCategoriasCreate_EmptyParentAllowed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Nova Categoria Raiz"},
		"type":        {"EXPENSE"},
		"parent_id":   {""},
		"macro_group": {"Estilo de Vida"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "criada com sucesso") {
		t.Fatalf("expected success message, got: %s", body)
	}
}

func TestHandleCategoriasCreate_RejectsIncomeWithEssencialMacro(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Receita Invalida"},
		"type":        {"INCOME"},
		"parent_id":   {""},
		"macro_group": {"Essencial"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "válido para o tipo") {
		t.Fatalf("expected macro_group type validation error, got: %s", body)
	}
}

func TestHandleCategoriasCreate_RejectsExpenseWithReceitasMacro(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Despesa Invalida"},
		"type":        {"EXPENSE"},
		"parent_id":   {""},
		"macro_group": {"Receitas"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "válido para o tipo") {
		t.Fatalf("expected macro_group type validation error, got: %s", body)
	}
}

func TestHandleCategoriasInlineSave_RejectsIncomeWithEssencialMacro(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Root Income A Updated"},
		"parent_id":   {""},
		"macro_group": {"Essencial"},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/root-inc-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "root-inc-a")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestValidMacroGroupsForType_Personal(t *testing.T) {
	incomeMacros := validMacroGroupsForType(false, "INCOME")
	if len(incomeMacros) != 1 || incomeMacros[0] != "Receitas" {
		t.Fatalf("personal income macros = %v, want [Receitas]", incomeMacros)
	}

	expenseMacros := validMacroGroupsForType(false, "EXPENSE")
	if len(expenseMacros) != 2 {
		t.Fatalf("personal expense macros count = %d, want 2", len(expenseMacros))
	}
	hasEssencial := false
	hasEstiloVida := false
	for _, m := range expenseMacros {
		if m == "Essencial" {
			hasEssencial = true
		}
		if m == "Estilo de Vida" {
			hasEstiloVida = true
		}
	}
	if !hasEssencial || !hasEstiloVida {
		t.Fatalf("personal expense macros missing required groups: %v", expenseMacros)
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %q, want %q: got=%v want=%v", i, got[i], want[i], got, want)
		}
	}
}

func TestValidMacroGroupsForType_Business(t *testing.T) {
	incomeMacros := validMacroGroupsForType(true, "INCOME")
	assertStringSliceEqual(t, incomeMacros, []string{
		categorycontract.MacroReceitasOperacionais,
		categorycontract.MacroReceitasFinanceiras,
		categorycontract.MacroOutrasEntradas,
	})

	expenseMacros := validMacroGroupsForType(true, "EXPENSE")
	assertStringSliceEqual(t, expenseMacros, []string{
		categorycontract.MacroDeducoesImpostos,
		categorycontract.MacroCustosOperacionais,
		categorycontract.MacroDespesasAdministrativas,
		categorycontract.MacroDespesasComerciais,
		categorycontract.MacroEquipePrestadores,
		categorycontract.MacroFinanceiro,
		categorycontract.MacroRetiradasDistribuicao,
		categorycontract.MacroInvestimentosPatrimonial,
		categorycontract.MacroOutrasSaidas,
		categorycontract.MacroInvestimentosOutros,
	})
}

func TestIsMacroGroupValidForType(t *testing.T) {
	if !isMacroGroupValidForType(false, "", "INCOME") {
		t.Fatal("empty macro should be valid")
	}
	if !isMacroGroupValidForType(false, "Receitas", "INCOME") {
		t.Fatal("Receitas should be valid for INCOME")
	}
	if isMacroGroupValidForType(false, "Essencial", "INCOME") {
		t.Fatal("Essencial should NOT be valid for INCOME")
	}
	if isMacroGroupValidForType(false, "Estilo de Vida", "INCOME") {
		t.Fatal("Estilo de Vida should NOT be valid for INCOME")
	}
	if !isMacroGroupValidForType(false, "Essencial", "EXPENSE") {
		t.Fatal("Essencial should be valid for EXPENSE")
	}
	if !isMacroGroupValidForType(false, "Estilo de Vida", "EXPENSE") {
		t.Fatal("Estilo de Vida should be valid for EXPENSE")
	}
	if isMacroGroupValidForType(false, "Receitas", "EXPENSE") {
		t.Fatal("Receitas should NOT be valid for EXPENSE")
	}
	if !isMacroGroupValidForType(true, "Receitas Operacionais", "INCOME") {
		t.Fatal("Receitas Operacionais should be valid for INCOME business")
	}
	if !isMacroGroupValidForType(true, "Receitas Financeiras", "INCOME") {
		t.Fatal("Receitas Financeiras should be valid for INCOME business")
	}
	if !isMacroGroupValidForType(true, "Outras Entradas", "INCOME") {
		t.Fatal("Outras Entradas should be valid for INCOME business")
	}
	if isMacroGroupValidForType(true, "Custos Operacionais", "INCOME") {
		t.Fatal("Custos Operacionais should NOT be valid for INCOME business")
	}
	if isMacroGroupValidForType(true, "Receitas", "INCOME") {
		t.Fatal("Receitas personal should NOT be valid for INCOME business")
	}
	if !isMacroGroupValidForType(true, "Retiradas/Distribuição", "EXPENSE") {
		t.Fatal("Retiradas/Distribuição should be valid for EXPENSE business")
	}
	if !isMacroGroupValidForType(true, "Investimentos/Patrimonial", "EXPENSE") {
		t.Fatal("Investimentos/Patrimonial should be valid for EXPENSE business")
	}
	if !isMacroGroupValidForType(true, "Investimentos/Outros", "EXPENSE") {
		t.Fatal("Investimentos/Outros legacy should remain valid for EXPENSE business")
	}
	if isMacroGroupValidForType(false, "Financeiro", "EXPENSE") {
		t.Fatal("Financeiro business should NOT be valid for personal expense")
	}
}

func TestHandleCategoryCreateForm_BusinessIncomeReceivesEconomicNatureOptions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBusinessCategoryParentTestScenario(t, db)

	tpl := &captureCategoryCreateTemplate{}
	h := TransactionHandler{DB: db, WorkspaceID: "ws-biz", UserID: "user-biz", Templates: tpl}
	req := httptest.NewRequest(http.MethodGet, "/api/categorias/nova?tipo=receita", nil)
	rr := httptest.NewRecorder()

	h.HandleCategoryCreateForm(rr, req)

	assertMacroOptionNames(t, tpl.data.MacroGroups, []string{
		categorycontract.MacroReceitasOperacionais,
		categorycontract.MacroReceitasFinanceiras,
		categorycontract.MacroOutrasEntradas,
	})
	if tpl.data.DefaultMacro != categorycontract.MacroReceitasOperacionais {
		t.Fatalf("default macro = %q, want %q", tpl.data.DefaultMacro, categorycontract.MacroReceitasOperacionais)
	}
}

func TestHandleCategoryCreateForm_BusinessExpenseOmitsLegacyFromPreferredOptions(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBusinessCategoryParentTestScenario(t, db)

	tpl := &captureCategoryCreateTemplate{}
	h := TransactionHandler{DB: db, WorkspaceID: "ws-biz", UserID: "user-biz", Templates: tpl}
	req := httptest.NewRequest(http.MethodGet, "/api/categorias/nova?tipo=despesa", nil)
	rr := httptest.NewRecorder()

	h.HandleCategoryCreateForm(rr, req)

	assertMacroOptionNames(t, tpl.data.MacroGroups, []string{
		categorycontract.MacroDeducoesImpostos,
		categorycontract.MacroCustosOperacionais,
		categorycontract.MacroDespesasAdministrativas,
		categorycontract.MacroDespesasComerciais,
		categorycontract.MacroEquipePrestadores,
		categorycontract.MacroFinanceiro,
		categorycontract.MacroRetiradasDistribuicao,
		categorycontract.MacroInvestimentosPatrimonial,
		categorycontract.MacroOutrasSaidas,
	})
}

func assertMacroOptionNames(t *testing.T, got []categorycontract.MacroGroup, want []string) {
	t.Helper()
	names := make([]string, 0, len(got))
	for _, item := range got {
		names = append(names, item.Name)
	}
	assertStringSliceEqual(t, names, want)
}

func TestQueryCategoryParentOptions_FiltersByMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	optionsAll, err := h.queryCategoryParentOptions("", "EXPENSE", "Essencial")
	if err != nil {
		t.Fatalf("queryCategoryParentOptions error: %v", err)
	}
	for _, opt := range optionsAll {
		if opt.ID == "root-inc-a" {
			t.Fatalf("parent options with macro=Essencial should not include INCOME category root-inc-a")
		}
	}
}

func TestBuildCategoryTree_GroupChildrenUnderParent(t *testing.T) {
	flat := []ConfigCategoryRow{
		{ID: "1", Name: "Moradia", Type: "EXPENSE", MacroGroup: "Essencial", ParentID: ""},
		{ID: "2", Name: "Aluguel", Type: "EXPENSE", MacroGroup: "Essencial", ParentID: "1"},
		{ID: "3", Name: "Condominio", Type: "EXPENSE", MacroGroup: "Essencial", ParentID: "1"},
		{ID: "4", Name: "Lazer Familiar", Type: "EXPENSE", MacroGroup: "Estilo de Vida", ParentID: ""},
		{ID: "5", Name: "Brinquedos", Type: "EXPENSE", MacroGroup: "Estilo de Vida", ParentID: "4"},
	}

	tree := buildCategoryTree(flat)

	if len(tree) != 2 {
		t.Fatalf("expected 2 root nodes, got %d", len(tree))
	}

	var moradia, lazerFamiliar *ConfigCategoryRow
	for _, root := range tree {
		if root.ID == "1" {
			moradia = root
		}
		if root.ID == "4" {
			lazerFamiliar = root
		}
	}
	if moradia == nil || lazerFamiliar == nil {
		t.Fatal("expected Moradia and Lazer Familiar as roots")
	}

	if len(moradia.Children) != 2 {
		t.Fatalf("Moradia children count = %d, want 2", len(moradia.Children))
	}
	if len(lazerFamiliar.Children) != 1 {
		t.Fatalf("Lazer Familiar children count = %d, want 1", len(lazerFamiliar.Children))
	}

	moradiaChildNames := []string{moradia.Children[0].Name, moradia.Children[1].Name}
	hasAluguel := false
	hasCondominio := false
	for _, name := range moradiaChildNames {
		if name == "Aluguel" {
			hasAluguel = true
		}
		if name == "Condominio" {
			hasCondominio = true
		}
	}
	if !hasAluguel || !hasCondominio {
		t.Fatalf("Moradia children: %v, want Aluguel and Condominio", moradiaChildNames)
	}

	if lazerFamiliar.Children[0].Name != "Brinquedos" {
		t.Fatalf("Lazer Familiar child = %q, want Brinquedos", lazerFamiliar.Children[0].Name)
	}

	var aluguelUnderLazerFamiliar bool
	for _, child := range lazerFamiliar.Children {
		if child.Name == "Aluguel" {
			aluguelUnderLazerFamiliar = true
		}
	}
	if aluguelUnderLazerFamiliar {
		t.Fatal("Aluguel should NOT be under Lazer Familiar")
	}
}

func TestHandleCategoriasCreate_IncomeDefaultMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Nova Receita"},
		"type":        {"INCOME"},
		"parent_id":   {""},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasCreate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "criada com sucesso") {
		t.Fatalf("expected success message, got: %s", body)
	}
}

func TestHandleCategoriasInlineSave_RejectsParentWithDifferentMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('root-exp-lifestyle', 'ws-a', 'Root Lifestyle', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', NULL, ?)`, now)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Child Expense A Renamed"},
		"parent_id":   {"root-exp-lifestyle"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/child-exp-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "child-exp-a")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (should reject parent from different macro_group)", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleCategoriasInlineSave_RejectsRootReparentToDifferentMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('root-exp-lifestyle', 'ws-a', 'Root Lifestyle', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', NULL, ?)`, now)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Root Expense A Renamed"},
		"parent_id":   {"root-exp-lifestyle"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/root-exp-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "root-exp-a")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (should reject root reparent to different macro_group)", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleCategoriasInlineSave_AcceptsParentWithSameMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCategoryParentTestScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('root-exp-essencial-2', 'ws-a', 'Root Essencial 2', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?)`, now)

	h := newConfigHandlerForWS(t, db, "ws-a")

	form := url.Values{
		"name":        {"Child Expense A"},
		"parent_id":   {"root-exp-essencial-2"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/child-exp-a/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "child-exp-a")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (should accept parent with same macro_group)", rr.Code, http.StatusOK)
	}
}

func TestHandleCategoriasInlineSave_BusinessRejectsParentCrossMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBusinessCategoryParentTestScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('biz-root-admin', 'ws-biz', 'Admin Cost', 'tag', '#6b7280', 'EXPENSE', 'Despesas Administrativas', NULL, ?)`, now)

	h := newConfigHandlerForWS(t, db, "ws-biz")

	form := url.Values{
		"name":        {"Biz Child Moved"},
		"parent_id":   {"biz-root-admin"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/biz-child-exp/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "biz-child-exp")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (should reject business cross-macro-group parent)", rr.Code, http.StatusBadRequest)
	}
}

func TestBusinessTemplateUsesDeduzoesImpostosWithAccent(t *testing.T) {
	source, err := os.ReadFile("../../templates/pages/configuracoes_categorias.html")
	if err != nil {
		t.Fatalf("read categorias template: %v", err)
	}
	compSource, err := os.ReadFile("../../templates/pages/configuracoes_components.html")
	if err != nil {
		t.Fatalf("read components template: %v", err)
	}
	tpl := template.Must(template.New("categorias-accent").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"filterCategories": func(categories []ConfigCategoryRow, typ string) []ConfigCategoryRow {
			var filtered []ConfigCategoryRow
			for _, c := range categories {
				if c.Type == typ {
					filtered = append(filtered, c)
				}
			}
			return filtered
		},
	}).Parse(string(compSource)))
	tpl = template.Must(tpl.Parse(string(source)))

	data := ConfiguracoesData{
		IsBusiness:            true,
		CategoryIncomeMacros:  categoryMacroGroupsForTemplate(true, "INCOME"),
		CategoryExpenseMacros: categoryMacroGroupsForTemplate(true, "EXPENSE"),
	}
	var page bytes.Buffer
	if err := tpl.ExecuteTemplate(&page, "configuracoes-categorias-content", data); err != nil {
		t.Fatalf("execute categorias content: %v", err)
	}
	if strings.Contains(page.String(), "Deducoes/Impostos") {
		t.Fatal("categorias template rendered Deducoes/Impostos instead of Deduções/Impostos")
	}
	if !strings.Contains(page.String(), "Deduções/Impostos") {
		t.Fatal("categorias template missing Deduções/Impostos with accent")
	}

	row := ConfigCategoryRow{
		ID:                "cat-tax",
		Name:              "Tax",
		Type:              "EXPENSE",
		MacroGroup:        categorycontract.MacroDeducoesImpostos,
		IsBusiness:        true,
		MacroGroupOptions: categorycontract.MacroGroupsForType(categorycontract.WorkspaceTypeBusiness, categorycontract.CategoryTypeExpense, true),
	}
	var comp bytes.Buffer
	if err := tpl.ExecuteTemplate(&comp, "config-categorias-row-edit", row); err != nil {
		t.Fatalf("execute categorias row edit: %v", err)
	}
	if strings.Contains(comp.String(), "Deducoes/Impostos") {
		t.Fatal("components template rendered Deducoes/Impostos instead of Deduções/Impostos")
	}
	if !strings.Contains(comp.String(), "Deduções/Impostos") {
		t.Fatal("components template missing Deduções/Impostos with accent")
	}
}

func TestValidMacroGroupsForType_BusinessIncludesDeduzoesImpostos(t *testing.T) {
	macros := validMacroGroupsForType(true, "EXPENSE")
	found := false
	for _, m := range macros {
		if m == "Deduções/Impostos" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("business expense macros should include Deduções/Impostos, got: %v", macros)
	}
}

func TestHandleCategoriasInlineSave_AcceptsBusinessParentWithSameMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBusinessCategoryParentTestScenario(t, db)

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('biz-root-costs-2', 'ws-biz', 'Costs 2', 'tag', '#6b7280', 'EXPENSE', 'Custos Operacionais', NULL, ?)`, now)

	h := newConfigHandlerForWS(t, db, "ws-biz")

	form := url.Values{
		"name":        {"Biz Child Expense"},
		"parent_id":   {"biz-root-costs-2"},
		"macro_group": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/configuracoes/categorias/biz-child-exp/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()

	h.HandleCategoriasInlineSave(rr, req, "biz-child-exp")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (should accept business parent with same macro_group)", rr.Code, http.StatusOK)
	}
}
