package handlers

import (
	"database/sql"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/icons"
)

// postForm monta um POST urlencoded real: sem o Content-Type o ParseForm
// ignora o corpo e os handlers cairiam em validação de campos vazios.
func postForm(t *testing.T, path string, form url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

type iconHarness struct {
	db *sql.DB
	ws string
}

func newIconHarness(t *testing.T, workspaceType string) iconHarness {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('icon-user', 'Icon User', 'icon-user@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-icon', 'Icon Workspace', '', ?, ?, ?)
	`, workspaceType, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-icon', 'icon-user', 'ADMIN', ?)
	`, now)
	return iconHarness{db: db, ws: "ws-icon"}
}

func (h iconHarness) categoriesHandler(t *testing.T) ConfiguracoesHandler {
	t.Helper()
	return ConfiguracoesHandler{
		DB:                  h.db,
		Templates:           testConfiguracoesRenderTemplates(t),
		AutomaticBackupsDir: t.TempDir(),
		WorkspaceID:         h.ws,
		UserID:              "icon-user",
		CanConfigRead:       true,
		CanConfigWrite:      true,
	}
}

func (h iconHarness) metasHandler(t *testing.T) MetasHandler {
	t.Helper()
	return MetasHandler{ActorRole: testMetasAdminRole,
		DB:          h.db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: h.ws,
		UserID:      "icon-user",
	}
}

func (h iconHarness) categoryIcon(t *testing.T, id string) string {
	t.Helper()
	var icon string
	if err := h.db.QueryRow(`SELECT icon FROM categories WHERE id = ?`, id).Scan(&icon); err != nil {
		t.Fatalf("read category icon: %v", err)
	}
	return icon
}

func (h iconHarness) categoryByName(t *testing.T, name string) (string, string) {
	t.Helper()
	var id, icon string
	if err := h.db.QueryRow(`SELECT id, icon FROM categories WHERE workspace_id = ? AND name = ?`, h.ws, name).Scan(&id, &icon); err != nil {
		t.Fatalf("read category %q: %v", name, err)
	}
	return id, icon
}

// ---------------------------------------------------------------------------
// Categorias
// ---------------------------------------------------------------------------

// A escolha explícita do seletor é persistida como chave do catálogo.
func TestCategoryCreatePersistsIconPickerChoice(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.categoriesHandler(t)

	form := url.Values{"name": {"Alimentação"}, "type": {"EXPENSE"}, "icon": {"pizza"}}
	rr := httptest.NewRecorder()
	handler.HandleCategoriasCreate(rr, postForm(t, "/configuracoes/categorias", form))
	rr.Result().Body.Close()

	_, icon := h.categoryByName(t, "Alimentação")
	if icon != "pizza" {
		t.Fatalf("escolha do seletor não foi persistida: %q", icon)
	}
}

// Sem escolha, o ícone é derivado do nome — nunca fica no default genérico.
func TestCategoryCreateWithoutChoiceDerivesIconFromName(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.categoriesHandler(t)

	form := url.Values{"name": {"Energia elétrica"}, "type": {"EXPENSE"}}
	rr := httptest.NewRecorder()
	handler.HandleCategoriasCreate(rr, postForm(t, "/configuracoes/categorias", form))
	rr.Result().Body.Close()

	_, icon := h.categoryByName(t, "Energia elétrica")
	if icon != "zap" {
		t.Fatalf("ícone derivado do nome = %q, want zap", icon)
	}
}

// Um valor fora do catálogo não pode virar dado persistido.
func TestCategoryCreateRejectsUnknownIconValue(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.categoriesHandler(t)

	form := url.Values{"name": {"Categoria Estranha"}, "type": {"EXPENSE"}, "icon": {"<svg onload=alert(1)>"}}
	rr := httptest.NewRecorder()
	handler.HandleCategoriasCreate(rr, postForm(t, "/configuracoes/categorias", form))
	rr.Result().Body.Close()

	_, icon := h.categoryByName(t, "Categoria Estranha")
	if !icons.IsValid(icon) {
		t.Fatalf("valor inválido chegou ao banco: %q", icon)
	}
	if strings.ContainsAny(icon, "<>/") {
		t.Fatalf("chave persistida contém caracteres inseguros: %q", icon)
	}
}

// Trocar o ícone manualmente tem de sobreviver a uma edição que não mexe nele.
func TestCategoryInlineSaveUpdatesBudgetIconAndKeepsManualChoice(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.categoriesHandler(t)
	categoryID := "cat-inline"
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES (?, ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, categoryID, h.ws, time.Now().UTC().Unix())

	// Usuário troca para pizza.
	save := url.Values{"name": {"Alimentação"}, "icon": {"pizza"}, "parent_id": {""}, "macro_group": {"Essencial"}}
	rr := httptest.NewRecorder()
	handler.HandleCategoriasInlineSave(rr, postForm(t, "/configuracoes/categorias/"+categoryID+"/salvar", save), categoryID)
	rr.Result().Body.Close()
	if got := h.categoryIcon(t, categoryID); got != "pizza" {
		t.Fatalf("troca de ícone não persistiu: %q", got)
	}

	// Edição posterior que reenvia a escolha existente mantém pizza.
	save2 := url.Values{"name": {"Alimentação e Lazer"}, "icon": {"pizza"}, "parent_id": {""}, "macro_group": {"Essencial"}}
	rr = httptest.NewRecorder()
	handler.HandleCategoriasInlineSave(rr, postForm(t, "/configuracoes/categorias/"+categoryID+"/salvar", save2), categoryID)
	rr.Result().Body.Close()
	if got := h.categoryIcon(t, categoryID); got != "pizza" {
		t.Fatalf("renomear a categoria reverteu a escolha manual: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Limites
// ---------------------------------------------------------------------------

func metasLimitForm(categoryID, maxAmount, iconKey string) url.Values {
	return url.Values{"category_id": {categoryID}, "max_amount_monthly": {maxAmount}, "icon_key": {iconKey}}
}

func (h iconHarness) createLimit(t *testing.T, handler MetasHandler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.HandleCriarLimite(rr, postForm(t, "/metas/limite", form))
	rr.Result().Body.Close()
	return rr
}

func TestLimitCreatePersistsIconKey(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-limit', ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', ?)
	`, h.ws, time.Now().UTC().Unix())

	h.createLimit(t, handler, metasLimitForm("cat-limit", "1000", "pizza"))

	var iconKey string
	if err := h.db.QueryRow(`SELECT COALESCE(icon_key, '') FROM cost_limits WHERE workspace_id = ?`, h.ws).Scan(&iconKey); err != nil {
		t.Fatalf("read limit icon_key: %v", err)
	}
	if iconKey != "pizza" {
		t.Fatalf("icon_key do limite = %q, want pizza", iconKey)
	}
}

// Editar um limite pode trocar o ícone, e um valor inválido não é gravado.
func TestLimitUpdateReplacesIconKeyAndIgnoresInvalidInput(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-limit', ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', ?)
	`, h.ws, time.Now().UTC().Unix())

	h.createLimit(t, handler, metasLimitForm("cat-limit", "1000", "pizza"))

	var limitID string
	if err := h.db.QueryRow(`SELECT id FROM cost_limits WHERE workspace_id = ?`, h.ws).Scan(&limitID); err != nil {
		t.Fatalf("read limit id: %v", err)
	}

	update := metasLimitForm("cat-limit", "2000", "car")
	update.Set("limit_id", limitID)
	h.createLimit(t, handler, update)

	var iconKey string
	if err := h.db.QueryRow(`SELECT COALESCE(icon_key, '') FROM cost_limits WHERE id = ?`, limitID).Scan(&iconKey); err != nil {
		t.Fatalf("read limit icon_key: %v", err)
	}
	if iconKey != "car" {
		t.Fatalf("troca de ícone do limite não persistiu: %q", iconKey)
	}

	// Um valor inválido nunca é persistido. Como o seletor usa "vazio" para
	// dizer "Automático", um valor fora do catálogo é tratado como ausência de
	// escolha: o limite passa a herdar o ícone da categoria.
	invalid := metasLimitForm("cat-limit", "2000", "javascript:alert(1)")
	invalid.Set("limit_id", limitID)
	h.createLimit(t, handler, invalid)

	if err := h.db.QueryRow(`SELECT COALESCE(icon_key, '') FROM cost_limits WHERE id = ?`, limitID).Scan(&iconKey); err != nil {
		t.Fatalf("read limit icon_key: %v", err)
	}
	if iconKey != "" {
		t.Fatalf("valor inválido foi persistido como ícone do limite: %q", iconKey)
	}
	if icons.IsValid(iconKey) {
		t.Fatalf("icon_key persistido não deveria ser uma chave válida: %q", iconKey)
	}

	// E o limite volta a herdar, sem perder o ícone da categoria.
	limits := queryDashboardLimits(h.db, h.ws, time.Now().UTC())
	if len(limits) != 1 {
		t.Fatalf("dashboard limits = %d, want 1", len(limits))
	}
	if got := limits[0].IconOrDefault(); got != "utensils" {
		t.Fatalf("após valor inválido o limite deveria herdar da categoria: %q", got)
	}
}

// Sem ícone próprio, o Limite herda o da categoria vinculada.
func TestLimitWithoutOwnIconInheritsCategoryIcon(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-limit', ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', ?)
	`, h.ws, time.Now().UTC().Unix())

	h.createLimit(t, handler, metasLimitForm("cat-limit", "1000", ""))

	limits := queryDashboardLimits(h.db, h.ws, time.Now().UTC())
	if len(limits) != 1 {
		t.Fatalf("dashboard limits = %d, want 1", len(limits))
	}
	if got := limits[0].IconOrDefault(); got != "utensils" {
		t.Fatalf("limite sem ícone próprio = %q, want herança utensils", got)
	}
	if limits[0].IconLabel() != icons.Label("utensils") {
		t.Fatalf("rótulo do ícone herdado = %q", limits[0].IconLabel())
	}
}

// Com ícone próprio, ele vence a herança.
func TestLimitWithOwnIconOverridesCategoryIcon(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-limit', ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', ?)
	`, h.ws, time.Now().UTC().Unix())

	h.createLimit(t, handler, metasLimitForm("cat-limit", "1000", "car"))

	limits := queryDashboardLimits(h.db, h.ws, time.Now().UTC())
	if len(limits) != 1 {
		t.Fatalf("dashboard limits = %d, want 1", len(limits))
	}
	if got := limits[0].IconOrDefault(); got != "car" {
		t.Fatalf("ícone próprio não prevaleceu: %q", got)
	}
}

func TestDashboardLimitsKeepExactPercent(t *testing.T) {
	h := newIconHarness(t, "personal")
	now := time.Now().UTC()
	execTestSQL(t, h.db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-1', ?, 'Conta', 'CHECKING', 0, 0, ?, ?)
	`, h.ws, now.Unix(), now.Unix())
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-1', ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', ?)
	`, h.ws, now.Unix())
	execTestSQL(t, h.db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, created_at, updated_at)
		VALUES ('lim-1', ?, 'cat-1', 10000, 80, ?, ?)
	`, h.ws, now.Unix(), now.Unix())
	execTestSQL(t, h.db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-1', ?, 'icon-user', 'acc-1', 'cat-1', 'EXPENSE', 15000, ?, 'Estouro', 'paid', ?, ?)
	`, h.ws, now.Unix(), now.Unix(), now.Unix())

	limits := queryDashboardLimits(h.db, h.ws, now)
	if len(limits) != 1 {
		t.Fatalf("dashboard limits = %d, want 1", len(limits))
	}
	if limits[0].PercentExact != 150 {
		t.Fatalf("PercentExact = %d, want 150", limits[0].PercentExact)
	}
	if limits[0].Percent != 100 {
		t.Fatalf("Percent da barra = %d, want saturação em 100", limits[0].Percent)
	}
	if !limits[0].IsExceeded() {
		t.Fatal("IsExceeded deveria ser verdadeiro")
	}
	if limits[0].Tone() != ToneExceeded {
		t.Fatalf("Tone = %q, want %q", limits[0].Tone(), ToneExceeded)
	}
}

// ---------------------------------------------------------------------------
// Barra Disponível x Reservado
// ---------------------------------------------------------------------------

func TestDashboardShowsEveryAccountOfTheWorkspace(t *testing.T) {
	for _, total := range []int{1, 4, 10, 100} {
		t.Run(fmt.Sprintf("%d-contas", total), func(t *testing.T) {
			h := newIconHarness(t, "personal")
			now := time.Now().UTC().Unix()
			for i := 1; i <= total; i++ {
				execTestSQL(t, h.db, `
					INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
					VALUES (?, ?, ?, 'CHECKING', 1000, 1000, ?, ?)
				`, fmt.Sprintf("acc-%03d", i), h.ws, fmt.Sprintf("Conta %03d", i), now, now)
			}

			data := BuildDashboardData(h.db, "icon-user", h.ws)
			if len(data.Accounts) != total {
				t.Fatalf("contas exibidas na Home = %d, want %d", len(data.Accounts), total)
			}
			// A ordenação canônica (sort_order, name) permanece intacta.
			for i, account := range data.Accounts {
				want := fmt.Sprintf("Conta %03d", i+1)
				if account.Name != want {
					t.Fatalf("ordem da Home em %d = %q, want %q", i, account.Name, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Status das próximas ações
// ---------------------------------------------------------------------------

func TestPendingItemStatusOnlyFlagsRealOverdue(t *testing.T) {
	if label, tone := pendingItemStatus("EXPENSE", true); label != "Vencido" || tone != "danger" {
		t.Fatalf("pagamento atrasado = %q/%q", label, tone)
	}
	if label, tone := pendingItemStatus("EXPENSE", false); label != "Pendente" || tone != "neutral" {
		t.Fatalf("pagamento no prazo = %q/%q", label, tone)
	}
	if label, tone := pendingItemStatus("INCOME", false); label != "Pendente" || tone != "info" {
		t.Fatalf("recebimento no prazo = %q/%q", label, tone)
	}
}

// testMetasFormAndPageTemplates monta o conjunto que o modal de Metas precisa:
// a página, o shell, o formulário e o componente reutilizável do seletor.
func testMetasFormAndPageTemplates(t *testing.T, workspaceType string) *template.Template {
	t.Helper()
	metas := readTemplateFile(t, "templates/pages/metas.html")
	shell := readTemplateFile(t, "templates/components/app_shell.html")
	form := readTemplateFile(t, "templates/components/form_meta.html")
	picker := readTemplateFile(t, "templates/components/icon_picker.html")
	return template.Must(template.New("metas-full-test").
		Funcs(withAppShellTestFuncs(template.FuncMap{
			"assetPath": func(path string) string { return path + "?v=test" },
		})).
		Parse(metas + shell + form + picker))
}

func readTemplateFile(t *testing.T, relative string) string {
	t.Helper()
	raw, err := os.ReadFile(resolveTemplatePath(t, relative))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// Renderização do componente na página real de Configurações
// ---------------------------------------------------------------------------

// O seletor precisa chegar ao HTML servido com catálogo, busca, preview e
// estado "Automático", sem nada de SVG bruto ou estilo inline.
func TestCategorySectionRendersReusableIconPicker(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.categoriesHandler(t)

	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, httptest.NewRequest(http.MethodGet, "/configuracoes/categorias", nil), "categorias")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	for _, want := range []string{
		`data-icon-picker`,
		`data-icon-dialog`,
		`data-icon-grid`,
		`data-icon-search`,
		`data-icon-preview-mark`,
		`data-icon-trigger-mark`,
		`data-icon-clear`,
		`name="icon"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("seletor de ícones não renderizou %q", want)
		}
	}

	// O catálogo chega pronto ao cliente, com rótulos amigáveis.
	for _, want := range []string{`data-icon-key="utensils"`, `data-icon-key="paw-print"`, `Alimentação`, `Família e pets`} {
		if !strings.Contains(body, want) {
			t.Errorf("catálogo não expôs %q no HTML", want)
		}
	}

	// O contrato do catálogo é fechado: nada de SVG, HTML ou chave arbitrária.
	if strings.Contains(body, `<svg data-icon`) {
		t.Error("catálogo não pode carregar SVG bruto")
	}
	if strings.Contains(body, `data-icon-key="<`) || strings.Contains(body, `data-icon-key="&`) {
		t.Error("chave de ícone escapada sugere entrada arbitrária no catálogo")
	}

	// O input oculto é o único caminho de persistência, e nasce vazio quando
	// não há escolha (o formulário de criação começa em "Automático").
	if !strings.Contains(body, `value="" data-icon-picker-value`) {
		t.Error("formulário de criação deveria iniciar sem escolha explícita")
	}
}

// A Meta/Limite precisam do mesmo componente dentro do formulário.
func TestMetasFormRendersReusableIconPickerForLimit(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	handler.Templates = testMetasFormAndPageTemplates(t, "personal")

	rr := httptest.NewRecorder()
	handler.HandleNovaMeta(rr, httptest.NewRequest(http.MethodGet, "/metas/novo?aba=limite", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	for _, want := range []string{
		`id="limiteIcon-title"`,
		`name="icon_key"`,
		`data-icon-clear`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("formulário de Metas não renderizou %q", want)
		}
	}
	if got := strings.Count(body, `data-icon-picker>`); got != 1 {
		t.Fatalf("esperado um seletor de limite, encontrados %d", got)
	}
}

func TestLimiteAndCategoryKeepNeutralFallback(t *testing.T) {
	for name, got := range map[string]string{
		"limite sem ícone":              (LimiteCard{}).IconOrDefault(),
		"limite com categoria inválida": (LimiteCard{CategoryIcon: "unknown-icon"}).IconOrDefault(),
		"limite com escolha inválida":   (LimiteCard{IconKey: "unknown-icon"}).IconOrDefault(),
		"categoria sem ícone":           (ConfigCategoryRow{}).IconOrDefault(),
		"categoria com chave legada":    (ConfigCategoryRow{Icon: "unknown-icon"}).IconOrDefault(),
	} {
		if got != icons.DefaultKey {
			t.Errorf("%s = %q, want %q", name, got, icons.DefaultKey)
		}
	}

	// O limite continua herdando a categoria quando ela tem ícone válido.
	if got := (LimiteCard{CategoryIcon: "utensils"}).IconOrDefault(); got != "utensils" {
		t.Errorf("limite herda categoria = %q, want utensils", got)
	}

}

// O seletor do Limite segue o fallback neutro mesmo sem categoria vinculada.
func TestLimitPickerKeepsNeutralFallback(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	handler.Templates = testMetasFormAndPageTemplates(t, "personal")

	rr := httptest.NewRecorder()
	handler.HandleNovaMeta(rr, httptest.NewRequest(http.MethodGet, "/metas/novo?aba=limite", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("formulário do limite status=%d, want 200", rr.Code)
	}
	for _, segment := range strings.Split(rr.Body.String(), "data-icon-picker>") {
		if !strings.Contains(segment, "limiteIcon-title") {
			continue
		}
		match := regexp.MustCompile(`data-icon-trigger-mark data-icon-key="([^"]*)"`).FindStringSubmatch(segment)
		if match == nil {
			t.Fatal("marcador do seletor do limite não encontrado")
		}
		if match[1] != icons.DefaultKey {
			t.Errorf("preview do seletor do limite = %q, want %q", match[1], icons.DefaultKey)
		}
		return
	}
	t.Fatal("seletor do limite não renderizado")
}
