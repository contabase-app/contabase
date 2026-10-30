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

// ---------------------------------------------------------------------------
// Metas e reservas
// ---------------------------------------------------------------------------

func metasBoxForm(name, iconKey string) url.Values {
	return url.Values{"name": {name}, "target_amount": {"1000"}, "icon_key": {iconKey}}
}

func (h iconHarness) createBox(t *testing.T, handler MetasHandler, form url.Values) {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.HandleCriarCaixinha(rr, postForm(t, "/metas/caixinha", form))
	rr.Result().Body.Close()
}

func TestBoxCreateAndUpdatePersistIconKey(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)

	h.createBox(t, handler, metasBoxForm("Trocar de carro", "car"))

	var boxID, iconKey string
	if err := h.db.QueryRow(`SELECT id, COALESCE(icon_key, '') FROM boxes WHERE workspace_id = ?`, h.ws).Scan(&boxID, &iconKey); err != nil {
		t.Fatalf("read box: %v", err)
	}
	if iconKey != "car" {
		t.Fatalf("icon_key da reserva = %q, want car", iconKey)
	}

	update := metasBoxForm("Trocar de carro", "house")
	update.Set("box_id", boxID)
	h.createBox(t, handler, update)

	if err := h.db.QueryRow(`SELECT COALESCE(icon_key, '') FROM boxes WHERE id = ?`, boxID).Scan(&iconKey); err != nil {
		t.Fatalf("read box icon_key: %v", err)
	}
	if iconKey != "house" {
		t.Fatalf("troca de ícone da reserva não persistiu: %q", iconKey)
	}
}

// Reserva sem ícone próprio e sem categoria continua reconhecível como meta.
func TestBoxWithoutIconFallsBackToSavingsMark(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	h.createBox(t, handler, metasBoxForm("Reserva sem categoria", ""))

	boxes := queryDashboardBoxes(h.db, h.ws)
	if len(boxes) != 1 {
		t.Fatalf("dashboard boxes = %d, want 1", len(boxes))
	}
	if got := boxes[0].IconOrDefault(); got != icons.FallbackGoal {
		t.Fatalf("fallback da reserva = %q, want %q", got, icons.FallbackGoal)
	}
}

// Reserva sem ícone próprio herda o ícone da categoria vinculada.
func TestBoxWithoutOwnIconInheritsCategoryIcon(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-box', ?, 'Viagens', 'plane', '#6b7280', 'EXPENSE', ?)
	`, h.ws, time.Now().UTC().Unix())

	form := metasBoxForm("Viagem Europa", "")
	form.Set("category_id", "cat-box")
	h.createBox(t, handler, form)

	boxes := queryDashboardBoxes(h.db, h.ws)
	if len(boxes) != 1 {
		t.Fatalf("dashboard boxes = %d, want 1", len(boxes))
	}
	if got := boxes[0].IconOrDefault(); got != "plane" {
		t.Fatalf("herança da categoria na reserva = %q, want plane", got)
	}
}

// ---------------------------------------------------------------------------
// Herança na página de Metas
// ---------------------------------------------------------------------------

func TestMetasDataResolvesLimitAndBoxIcons(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	now := time.Now().UTC().Unix()
	execTestSQL(t, h.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES
			('cat-a', ?, 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', ?),
			('cat-b', ?, 'Viagens', 'plane', '#6b7280', 'EXPENSE', ?),
			('cat-c', ?, 'Transporte', 'bus', '#6b7280', 'EXPENSE', ?)
	`, h.ws, now, h.ws, now, h.ws, now)
	execTestSQL(t, h.db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, icon_key, created_at, updated_at)
		VALUES
			('lim-inherit', ?, 'cat-a', 100000, 80, NULL, ?, ?),
			('lim-own', ?, 'cat-c', 200000, 80, 'pizza', ?, ?)
	`, h.ws, now, now, h.ws, now, now)
	execTestSQL(t, h.db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, icon_key, created_at, updated_at)
		VALUES
			('box-inherit', ?, 'Reserva herdada', 'cat-b', 100000, 0, NULL, ?, ?),
			('box-own', ?, 'Reserva própria', 'cat-b', 100000, 0, 'house', ?, ?)
	`, h.ws, now, now, h.ws, now, now)

	data, err := handler.buildMetasData("test", "limites")
	if err != nil {
		t.Fatalf("buildMetasData: %v", err)
	}

	limits := map[string]string{}
	for _, card := range data.Limites {
		limits[card.ID] = card.IconOrDefault()
	}
	if limits["lim-inherit"] != "utensils" {
		t.Errorf("limite sem ícone = %q, want utensils herdado", limits["lim-inherit"])
	}
	if limits["lim-own"] != "pizza" {
		t.Errorf("limite com ícone próprio = %q, want pizza", limits["lim-own"])
	}

	boxes := map[string]string{}
	for _, card := range data.Caixinhas {
		boxes[card.ID] = card.IconOrDefault()
	}
	if boxes["box-inherit"] != "plane" {
		t.Errorf("reserva sem ícone = %q, want plane herdado", boxes["box-inherit"])
	}
	if boxes["box-own"] != "house" {
		t.Errorf("reserva com ícone próprio = %q, want house", boxes["box-own"])
	}
}

// ---------------------------------------------------------------------------
// Faixas semânticas dos limites (centralizadas)
// ---------------------------------------------------------------------------

func TestLimitToneThresholdsAreCentralized(t *testing.T) {
	cases := []struct {
		percent int
		want    string
	}{
		{0, ToneOK},
		{69, ToneOK},
		{70, ToneAttention},
		{89, ToneAttention},
		{90, ToneNear},
		{99, ToneNear},
		{100, ToneExceeded},
		{180, ToneExceeded},
	}
	for _, tc := range cases {
		card := DashboardLimitCard{PercentExact: tc.percent}
		if got := card.Tone(); got != tc.want {
			t.Errorf("Tone(%d%%) = %q, want %q", tc.percent, got, tc.want)
		}
		if card.ToneLabel() == "" {
			t.Errorf("ToneLabel(%d%%) vazio", tc.percent)
		}
	}

	// A Meta usa outra régua: evolução em direção ao alvo, não consumo.
	progressCases := []struct {
		percent    int
		isNegative bool
		want       string
	}{
		{0, false, ToneInitial},
		{3, false, ToneInitial},
		{24, false, ToneInitial},
		{25, false, ToneInfo},
		{40, false, ToneInfo},
		{69, false, ToneInfo},
		{70, false, ToneOK},
		{99, false, ToneOK},
		{100, false, ToneComplete},
		{150, false, ToneComplete},
		{90, true, ToneExceeded},
	}
	for _, tc := range progressCases {
		card := CaixinhaCard{Percent: tc.percent, IsNegative: tc.isNegative}
		if got := card.ProgressTone(); got != tc.want {
			t.Errorf("ProgressTone(%d%%, negative=%t) = %q, want %q", tc.percent, tc.isNegative, got, tc.want)
		}
		if card.ProgressToneLabel() == "" {
			t.Errorf("ProgressToneLabel(%d%%) vazio", tc.percent)
		}
	}
}

// O percentual exato preserva o excesso para a interface distinguir
// "no limite" de "estourado".
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

func TestBalanceSplitPercentagesAreConsistent(t *testing.T) {
	cases := []struct {
		reserved int64
		free     int64
		wantR    int
		wantF    int
		hasSplit bool
	}{
		{0, 0, 0, 0, false},
		{100, 0, 100, 0, true},
		{0, 100, 0, 100, true},
		{6480, 3520, 65, 35, true},
		{-500, 1000, 0, 100, true},
		{1000, -500, 100, 0, true},
	}
	for _, tc := range cases {
		var balance BalanceData
		balance.applyBalanceSplit(tc.reserved, tc.free)
		if balance.ReservedPercent != tc.wantR || balance.FreePercent != tc.wantF {
			t.Errorf("applyBalanceSplit(%d,%d) = reservado %d%% / disponível %d%%, want %d/%d",
				tc.reserved, tc.free, balance.ReservedPercent, balance.FreePercent, tc.wantR, tc.wantF)
		}
		if balance.HasSplit != tc.hasSplit {
			t.Errorf("HasSplit(%d,%d) = %v, want %v", tc.reserved, tc.free, balance.HasSplit, tc.hasSplit)
		}
		if balance.HasSplit && balance.ReservedPercent+balance.FreePercent != 100 {
			t.Errorf("percentuais não somam 100: %d + %d", balance.ReservedPercent, balance.FreePercent)
		}
	}
}

// ---------------------------------------------------------------------------
// Contas na Home
// ---------------------------------------------------------------------------

// A Home apresenta todas as contas do workspace: nenhuma quantidade é
// recortada na apresentação.
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
func TestMetasFormRendersReusableIconPickerForLimitAndGoal(t *testing.T) {
	h := newIconHarness(t, "personal")
	handler := h.metasHandler(t)
	handler.Templates = testMetasFormAndPageTemplates(t, "personal")

	rr := httptest.NewRecorder()
	handler.HandleNovaMeta(rr, httptest.NewRequest(http.MethodGet, "/metas/novo?aba=limite", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	// Os dois formulários do modal compartilham o mesmo componente, cada um com
	// seu próprio identificador e campo oculto.
	for _, want := range []string{
		`id="limiteIcon-title"`,
		`id="caixinhaIcon-title"`,
		`name="icon_key"`,
		`data-icon-clear`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("formulário de Metas não renderizou %q", want)
		}
	}
	if got := strings.Count(body, `data-icon-picker>`); got != 2 {
		t.Fatalf("esperado um seletor por formulário, encontrados %d", got)
	}
}

func TestMetaBoxIconFallbackAcrossReadPaths(t *testing.T) {
	for _, tc := range []struct {
		name, own, category, want string
		noCategory                bool
	}{
		{"own", "car", "plane", "car", false},
		{"inherited", "", "plane", "plane", false},
		{"empty", "", "", "piggy-bank", false},
		{"invalid-category", "", "unknown-icon", "piggy-bank", false},
		{"no-category", "", "", "piggy-bank", true},
		{"invalid-own-inherits", "unknown-icon", "plane", "plane", false},
		{"invalid-both", "unknown-icon", "unknown-category", "piggy-bank", false},
		{"manual-tag", "tag", "plane", "tag", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newIconHarness(t, "personal")
			var categoryID any
			if !tc.noCategory {
				categoryID = "category"
				execTestSQL(t, h.db, `INSERT INTO categories(id,workspace_id,name,icon,type) VALUES ('category',?,'Categoria',?,'EXPENSE')`, h.ws, tc.category)
			}
			execTestSQL(t, h.db, `INSERT INTO boxes(id,workspace_id,name,category_id,target_amount,monthly_recharge,icon_key) VALUES ('box',?,'Reserva',?,10000,0,?)`, h.ws, categoryID, tc.own)
			handler := h.metasHandler(t)
			data, err := handler.buildMetasData("test", "caixinhas")
			if err != nil {
				t.Fatal(err)
			}
			for path, cards := range map[string][]CaixinhaCard{"dashboard": queryDashboardBoxes(h.db, h.ws), "metas": data.Caixinhas} {
				if len(cards) != 1 {
					t.Fatalf("%s count=%d", path, len(cards))
				}
				if got := cards[0].IconOrDefault(); got != tc.want {
					t.Errorf("%s icon=%q want %q", path, got, tc.want)
				}
			}

			handler.Templates = testMetasFormAndPageTemplates(t, "personal")

			// Sheet de aporte: o read-model da reserva resolve o mesmo ícone.
			sheet := httptest.NewRecorder()
			handler.HandleAporteCaixinhaSheet(sheet, httptest.NewRequest(http.MethodGet, "/metas/caixinha/aporte/sheet?box_id=box", nil))
			if sheet.Code != http.StatusOK {
				t.Fatalf("aporte sheet status=%d, want 200", sheet.Code)
			}
			if strings.Contains(sheet.Body.String(), "internal server error") {
				t.Error("aporte sheet caiu em erro de template")
			}
			if got := boxSheetIconKey(t, sheet.Body.String()); got != tc.want {
				t.Errorf("aporte sheet icon=%q want %q", got, tc.want)
			}

			// Seletor da reserva: o preview segue o fallback final do domínio.
			formModal := httptest.NewRecorder()
			handler.HandleNovaMeta(formModal, httptest.NewRequest(http.MethodGet, "/metas/novo?aba=caixinhas&id=box", nil))
			if formModal.Code != http.StatusOK {
				t.Fatalf("formulário da reserva status=%d, want 200", formModal.Code)
			}
			if got := boxPickerPreviewKey(t, formModal.Body.String()); got != tc.want {
				t.Errorf("preview do seletor da reserva=%q want %q", got, tc.want)
			}
		})
	}
}

// Limite e Categoria mantêm o fallback neutro do catálogo: a marca de reserva
// é exclusiva de Meta/reserva e não pode vazar para esses domínios.
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

	// A reserva fecha em piggy-bank, que é diferente do fallback neutro.
	if icons.FallbackGoal == icons.DefaultKey {
		t.Fatal("a marca de reserva não pode ser o fallback neutro")
	}
	if got := (MetaFormData{}).BoxIconFallback(); got != icons.FallbackGoal {
		t.Errorf("fallback do formulário da reserva = %q, want %q", got, icons.FallbackGoal)
	}
	if got := (MetaFormData{CategoryIcon: "unknown-icon"}).BoxIconFallback(); got != icons.FallbackGoal {
		t.Errorf("fallback da reserva com categoria inválida = %q, want %q", got, icons.FallbackGoal)
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

// boxPickerPreviewKey extrai a chave exibida pelo seletor da reserva no
// formulário de Metas. O formulário hospeda dois seletores (Limite e Reserva),
// então o recorte é feito pelo id do próprio diálogo da reserva.
func boxPickerPreviewKey(t *testing.T, body string) string {
	t.Helper()
	for _, segment := range strings.Split(body, "data-icon-picker>") {
		if !strings.Contains(segment, "caixinhaIcon-title") {
			continue
		}
		match := regexp.MustCompile(`data-icon-trigger-mark data-icon-key="([^"]*)"`).FindStringSubmatch(segment)
		if match == nil {
			t.Fatalf("marcador do seletor da reserva não encontrado no formulário")
		}
		return match[1]
	}
	t.Fatalf("seletor da reserva não renderizado no formulário")
	return ""
}

// boxSheetIconKey extrai o ícone do cabeçalho do sheet de aporte. O cabeçalho
// pode ter o botão de fechar antes ou depois do tile; a âncora é o tile da
// reserva, não a posição do primeiro data-lucide no documento.
func boxSheetIconKey(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "metas-sheet__tile")
	if start < 0 {
		t.Fatalf("sheet de aporte não renderizou o tile do cabeçalho: %q", truncateForTest(body))
	}
	match := regexp.MustCompile(`data-lucide="([^"]*)"`).FindStringSubmatch(body[start:])
	if match == nil {
		t.Fatalf("sheet de aporte não renderizou o ícone da reserva: %q", truncateForTest(body))
	}
	return match[1]
}

func truncateForTest(value string) string {
	if len(value) <= 200 {
		return value
	}
	return value[:200] + "…"
}
