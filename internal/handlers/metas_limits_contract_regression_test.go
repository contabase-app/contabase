package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func tagContaining(body, needle, tag string) string {
	idx := strings.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(body[:idx], "<"+tag)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[idx:], ">")
	if end < 0 {
		return ""
	}
	return body[start : idx+end+1]
}

func inputTagContaining(body, needle string) string {
	return tagContaining(body, needle, "input")
}

func renderMetasTemplateForTest(t *testing.T, tpl *template.Template, name string, data any) string {
	t.Helper()
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return out.String()
}

func testLimitCardForContract(id string, consumedPercent int) LimiteCard {
	return LimiteCard{
		ID:           id,
		CategoryID:   "cat-food",
		CategoryName: "Alimentação",
		CategoryIcon: "utensils",
		MaxAmount:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
		Spent:        MoneyDisplay{Reais: "0", Cents: ",00"},
		Remaining:    MoneyDisplay{Reais: "1.800", Cents: ",00"},
		Percent:      consumedPercent,
	}
}

func TestMetasLimitsOverviewContract(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	for _, tc := range []struct {
		count int
		label string
	}{
		{count: 0, label: "0 limites ativos"},
		{count: 1, label: "1 limite ativo"},
		{count: 2, label: "2 limites ativos"},
	} {
		body := renderMetasTemplateForTest(t, tpl, "metas-limites-panel", MetasData{
			LimitesCount: tc.count,
			TotalLimits:  MoneyDisplay{Reais: "1.234", Cents: ",56"},
		})
		for _, want := range []string{
			`<section class="ui-panel metas-overview" aria-labelledby="metas-limites-overview-title">`,
			`<span class="ui-section-icon" aria-hidden="true"><i data-lucide="gauge"></i></span>`,
			`<h2 id="metas-limites-overview-title">Limites</h2>`,
			`<span class="ui-pill">` + tc.label + `</span>`,
			`<div class="ui-kpi-grid metas-overview__grid metas-overview__grid--1">`,
			`<span class="ui-kpi__label">Gastos este mês</span>`,
			`<strong class="ui-kpi__value" data-sensitive>R$ 1.234,56</strong>`,
			`Nenhum limite definido`,
			`Use o botão + para criar seu primeiro limite.`,
			`class="ui-empty" role="status"`,
		} {
			assertContains(t, body, want)
		}
		if strings.Contains(body, "Reservas") || strings.Contains(body, "Reservado") || strings.Contains(body, "Disponível estimado") {
			t.Fatal("limits overview must not render reserve indicators")
		}
	}
}

func TestMetasLimitCardContract(t *testing.T) {
	body := renderMetasTemplateForTest(t, testMetasPageTemplates(t), "metas-limit-card", testLimitCardForContract("lim-1", 0))
	for _, expected := range []string{
		`<article data-meta-card class="metas-card metas-card--limit relative flex flex-col">`,
		`href="/metas/limite/historico?limit_id=lim-1"`,
		`hx-get="/metas/limite/historico?limit_id=lim-1"`,
		`hx-target="#bottom-sheet-container"`,
		`aria-label="Abrir histórico do limite Alimentação"`,
		`<p class="metas-card-title">Alimentação</p>`,
		`<p class="metas-card-subtitle">Limite mensal</p>`,
		`aria-label="Ações do limite" aria-expanded="false" aria-controls="metas-limite-menu-lim-1"`,
		`<div id="metas-limite-menu-lim-1" data-ck-panel class="metas-menu-panel hidden">`,
		`<p class="metas-box-label">Disponível</p>`,
		`<p class="metas-box-value metas-box-value-ok" data-sensitive>R$ 1.800,00</p>`,
		`<div class="metas-card__progress metas-progress-wrap metas-progress-tone-ok" data-metas-p="100">`,
		`<progress class="metas-progress" value="100" max="100" aria-label="Alimentação: 100% do limite disponível">100%</progress>`,
		`<span class="metas-progress-chip" aria-hidden="true">100% disponível</span>`,
		`<span class="metas-card__fact-label">Utilizado</span>`,
		`<span class="metas-card__fact-value" data-sensitive>R$ 0,00</span>`,
		`<span class="metas-card__fact-label">Limite</span>`,
		`<span class="metas-card__fact-value" data-sensitive>R$ 1.800,00</span>`,
		`hx-get="/metas/novo?aba=limite&id=lim-1"`,
		`<a href="/lancamentos?categoria=cat-food" data-ck-close class="cb-meta-menu-item">`,
	} {
		assertContains(t, body, expected)
	}
	for _, forbidden := range []string{"Reservas", "Nova reserva", "cb-card-dense"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("limit card retained reserve surface %q", forbidden)
		}
	}
	if got := strings.Count(body, "data-sensitive"); got != 3 {
		t.Fatalf("limit card monetary fields = %d, want 3", got)
	}
	if strings.Contains(body, `aria-label="R$`) || strings.Contains(body, `title="R$`) {
		t.Fatal("limit card must not expose money in aria-label/title")
	}
}

func TestMetasLimitCardAvailableSemantics(t *testing.T) {
	for _, tc := range []struct {
		consumed  int
		available string
		chip      string
		tone      string
		progress  string
		aria      string
	}{
		{consumed: 0, available: "100", chip: "100% disponível", tone: "ok", progress: "100", aria: "100% do limite disponível"},
		{consumed: 10, available: "90", chip: "90% disponível", tone: "ok", progress: "90", aria: "90% do limite disponível"},
		{consumed: 30, available: "70", chip: "70% disponível", tone: "ok", progress: "70", aria: "70% do limite disponível"},
		{consumed: 75, available: "25", chip: "25% disponível", tone: "attention", progress: "25", aria: "25% do limite disponível"},
		{consumed: 100, available: "0", chip: "Excedido", tone: "exceeded", progress: "0", aria: "limite excedido"},
		{consumed: 120, available: "0", chip: "Excedido", tone: "exceeded", progress: "0", aria: "limite excedido"},
	} {
		t.Run(fmt.Sprintf("%d-consumed", tc.consumed), func(t *testing.T) {
			body := renderMetasTemplateForTest(t, testMetasPageTemplates(t), "metas-limit-card", testLimitCardForContract("lim-state", tc.consumed))
			assertContains(t, body, fmt.Sprintf(`data-metas-p="%s"`, tc.available))
			assertContains(t, body, "metas-progress-tone-"+tc.tone)
			assertContains(t, body, fmt.Sprintf(`<progress class="metas-progress" value="%s" max="100" aria-label="Alimentação: %s">`, tc.progress, tc.aria))
			assertContains(t, body, fmt.Sprintf(`<span class="metas-progress-chip" aria-hidden="true">%s</span>`, tc.chip))
			if strings.Contains(body, "% utilizado") {
				t.Fatal("limit progress must describe available budget, not consumption")
			}
		})
	}
}

func TestMetasLimitCardsSupportVariableCountsAndMaskValues(t *testing.T) {
	for _, count := range []int{1, 2, 6, 8} {
		t.Run(fmt.Sprintf("%d-cards", count), func(t *testing.T) {
			cards := make([]LimiteCard, 0, count)
			for i := 0; i < count; i++ {
				card := testLimitCardForContract(fmt.Sprintf("lim-%d", i+1), 10+i)
				card.CategoryName = fmt.Sprintf("Limite %d", i+1)
				cards = append(cards, card)
			}
			body := renderMetasTemplateForTest(t, testMetasPageTemplates(t), "metas-limites-panel", MetasData{
				LimitesCount: count,
				TotalLimits:  MoneyMinor(123456),
				Limites:      cards,
			})
			if got := strings.Count(body, `<article data-meta-card `); got != count {
				t.Fatalf("rendered %d cards, want %d", got, count)
			}
			if got := strings.Count(body, `aria-controls="metas-limite-menu-`); got != count {
				t.Fatalf("rendered %d aria-controls, want %d", got, count)
			}
			assertContains(t, body, `class="grid grid-cols-1 md:grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-3 w-full"`)
			for _, value := range []string{"R$ 1.234,56", "R$ 1.800,00"} {
				if strings.Contains(body, `aria-label="`+value) || strings.Contains(body, `title="`+value) {
					t.Fatalf("card exposed %q in an attribute", value)
				}
			}
		})
	}
}

func TestMetasLimitFormContract(t *testing.T) {
	form := renderMetasTemplateForTest(t, testMetasFormTemplates(t), "metas-form", MetaFormData{
		Mode:           "create",
		CategoryID:     "cat-1",
		CategoryName:   "Alimentação",
		CategoryIcon:   "utensils",
		CategoryColor:  "#8b5cf6",
		MaxAmountInput: "1.800,00",
	})
	for _, expected := range []string{
		`data-metas-sheet`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="meta-sheet-title"`,
		`aria-describedby="meta-sheet-description"`,
		`data-meta-form-root`,
		`data-metas-form-panel`,
		`data-open-tab="limite"`,
		`id="meta-limite-form"`,
		`hx-post="/metas/limite"`,
		`hx-swap="none"`,
		`hx-sync="this:drop"`,
		`name="limit_id"`,
		`name="tipo" value="EXPENSE"`,
		`name="category_id"`,
		`name="max_amount_monthly"`,
		`name="icon_key"`,
		`id="categoriaPicker_limite"`,
		`id="categoriaId_limite"`,
		`data-meta-category-picker`,
		`data-money-mask`,
		`value="R$ 1.800,00"`,
		`data-meta-create-submit`,
		`data-meta-submit-label`,
		`Criar limite`,
		`Teto de gasto do mês; não é saldo em conta.`,
		`Fechar formulário de limite`,
	} {
		assertContains(t, form, expected)
	}
	for _, forbidden := range []string{`meta-caixinha-form`, `tab-caixinha`, `caixinhaName`, `>Reservas<`, `hx-post="/metas/caixinha"`} {
		if strings.Contains(form, forbidden) {
			t.Fatalf("limit form retained reserve surface %q", forbidden)
		}
	}
	pickerTag := tagContaining(form, `data-meta-category-picker`, "button")
	if !strings.Contains(pickerTag, `data-metas-form-initial`) {
		t.Fatal("create limit must focus the category picker")
	}
	amountTag := inputTagContaining(form, `id="limiteMaxAmount"`)
	if strings.Contains(amountTag, `data-metas-form-initial`) {
		t.Fatal("create limit must not mark the amount as the initial focus target")
	}

	edit := renderMetasTemplateForTest(t, testMetasFormTemplates(t), "metas-form", MetaFormData{
		Mode:           "edit",
		LimitID:        "lim-1",
		CategoryID:     "cat-1",
		CategoryName:   "Alimentação",
		CategoryIcon:   "utensils",
		CategoryColor:  "#8b5cf6",
		MaxAmountInput: "1.800,00",
	})
	for _, expected := range []string{
		`hx-delete="/metas/limite/lim-1"`,
		`hx-target="closest .sheet-in" hx-swap="delete"`,
		`hx-confirm="Tem certeza que deseja excluir este limite?"`,
		`metas-form__danger-zone`,
		`form="meta-limite-form"`,
		`Salvar`,
		`data-metas-form-initial`,
	} {
		assertContains(t, edit, expected)
	}
	amountTag = inputTagContaining(edit, `id="limiteMaxAmount"`)
	if !strings.Contains(amountTag, `data-metas-form-initial`) {
		t.Fatal("edit limit must focus the amount")
	}
}

func TestMetasLimitHistoryContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-fin-limit-history")
	now := appTime(time.Now())
	monthDate := func(day int) int64 {
		return CivilDateToLegacyUnixNoonUTC(MustCivilDate(fmt.Sprintf("%04d-%02d-%02d", now.Year(), now.Month(), day)))
	}
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-fin-history', ?, ?, 100000)
	`, base.ws, base.category)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES
			('tx-fin-old', ?, ?, ?, ?, 'EXPENSE', 1000, ?, 'Gasto antigo', 'paid', ?, ?),
			('tx-fin-new', ?, ?, ?, ?, 'EXPENSE', 2000, ?, 'Gasto recente', 'paid', ?, ?)
	`, base.ws, base.user, base.checking, base.category, monthDate(5), now.Unix(), now.Unix(),
		base.ws, base.user, base.checking, base.category, monthDate(20), now.Unix(), now.Unix())

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, Templates: testHistoryTemplates(t), WorkspaceID: base.ws, UserID: base.user}
	rr := httptest.NewRecorder()
	h.HandleHistoricoLimite(rr, httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id=limit-fin-history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("limit history status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, expected := range []string{
		`data-metas-history="limit"`,
		`data-metas-history-panel`,
		`aria-labelledby="limit-history-title"`,
		`aria-describedby="limit-history-description"`,
		`role="region" aria-label="Lançamentos do limite `,
		`metas-history__panel-title">Lançamentos deste mês`,
		`ui-row metas-history__row`,
		`metas-history__eyebrow">Categoria`,
		`Gasto recente`,
		`Gasto antigo`,
		`data-sensitive`,
	} {
		assertContains(t, body, expected)
	}
	if strings.Index(body, "Gasto recente") > strings.Index(body, "Gasto antigo") {
		t.Fatal("limit history must keep the newest transaction first")
	}
	if got, want := strings.Count(body, "R$"), strings.Count(body, "data-sensitive"); got != want {
		t.Fatalf("limit history money values=%d sensitive=%d", got, want)
	}
	for _, forbidden := range []string{"Mostrar mais", "Carregar mais", "hx-get=", "hx-post=", "Reservado", "Disponível estimado"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("limit history retained %q", forbidden)
		}
	}
}

func TestMetasLimitHistoryEmptyState(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-fin-limit-empty")
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-fin-empty', ?, ?, 100000)
	`, base.ws, base.category)

	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, Templates: testHistoryTemplates(t), WorkspaceID: base.ws, UserID: base.user}
	rr := httptest.NewRecorder()
	h.HandleHistoricoLimite(rr, httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id=limit-fin-empty", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("empty limit history status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, expected := range []string{
		`ui-empty metas-history__empty`,
		`data-lucide="inbox"`,
		`Nenhum lançamento este mês`,
		`role="status"`,
	} {
		assertContains(t, body, expected)
	}
	if strings.Contains(body, "metas-history__row") || strings.Contains(body, `tabindex="0"`) {
		t.Fatal("empty limit history must not render rows or add a tab stop")
	}
}

func TestMetasHeaderPrivacyAndHTMXContract(t *testing.T) {
	tpl := testMetasPageTemplates(t)
	full := renderMetasTemplateForTest(t, tpl, "metas-page", MetasData{Title: "Metas", UserName: "Vitor", UserInitials: "VF", ActiveWorkspaceName: "Família"})
	for _, expected := range []string{
		`data-ui="premium"`,
		`data-page-section="goals"`,
		`class="ui-eyebrow">Planejamento`,
		`class="ui-page-title">Metas`,
		`class="ui-page-description">Defina limites de gastos por categoria.`,
		`data-privacy-toggle`,
		`aria-pressed="false"`,
		`aria-label="Ocultar valores financeiros"`,
		`id="metas-primary-action"`,
		`hx-get="/metas/novo?aba=limite"`,
		`hx-target="#bottom-sheet-container"`,
		`id="bottom-sheet-container"`,
		`id="metas-limites-overview-title"`,
	} {
		assertContains(t, full, expected)
	}
	for _, forbidden := range []string{"Metas e Reservas", `role="tablist"`, "Nova reserva", `aba=reservas`} {
		if strings.Contains(full, forbidden) {
			t.Fatalf("metas page retained %q", forbidden)
		}
	}

	partial := renderMetasTemplateForTest(t, tpl, "metas-tabs", MetasData{ContentOOB: true, HeaderActionOOB: true, LimitesCount: 1, TotalLimits: MoneyMinor(1234)})
	for _, expected := range []string{
		`id="metas-tabs"`,
		`data-active-tab="limites"`,
		`hx-swap-oob="outerHTML"`,
		`id="metas-primary-action"`,
		`<section class="ui-panel metas-overview"`,
	} {
		assertContains(t, partial, expected)
	}
	if got := strings.Count(partial, `id="metas-tabs"`); got != 1 {
		t.Fatalf("metas-tabs rendered %d times, want 1", got)
	}
	if got := strings.Count(partial, `<section class="ui-panel metas-overview"`); got != 1 {
		t.Fatalf("overview panel rendered %d times, want 1", got)
	}
}

func TestMetasLimitCategoryScopeAndForeignLimit(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES
		('limit-fin-own', 'scope-a', 'cat-a', 10000),
		('limit-fin-foreign', 'scope-b', 'cat-b', 20000),
		('limit-fin-cross', 'scope-a', 'cat-b', 30000)`)
	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "scope-a", UserID: "scope-user"}
	data, err := h.buildMetasData("")
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Limites) != 1 || data.Limites[0].ID != "limit-fin-own" {
		t.Fatalf("limit scope = %+v, want only limit-fin-own", data.Limites)
	}
	var form MetaFormData
	if err := h.loadLimitFormData(&form, "limit-fin-own"); err != nil || form.CategoryID != "cat-a" {
		t.Fatalf("own limit form = %+v err=%v", form, err)
	}
	for _, foreign := range []string{"limit-fin-foreign", "limit-fin-cross", "missing-fin-limit"} {
		var form MetaFormData
		if err := h.loadLimitFormData(&form, foreign); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("foreign/missing limit %s err=%v, want sql.ErrNoRows", foreign, err)
		}
	}
}

func TestMetasLimitHistoryForeignWorkspaceDoesNotLeak(t *testing.T) {
	db := seedTenantRuntimeScope(t)
	execTestSQL(t, db, `INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly) VALUES
		('limit-fin-other', 'scope-b', 'cat-b', 20000)`)
	h := MetasHandler{ActorRole: testMetasAdminRole, DB: db, Templates: testHistoryTemplates(t), WorkspaceID: "scope-a", UserID: "scope-user"}
	rr := httptest.NewRecorder()
	h.HandleHistoricoLimite(rr, httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id=limit-fin-other", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("foreign limit history status = %d, want 404", rr.Code)
	}
}

func TestMetasLimitFormIDsAreUnique(t *testing.T) {
	duplicateIDs := func(body string) []string {
		matches := regexp.MustCompile(`(?:^|\s)id="([^"]+)"`).FindAllStringSubmatch(body, -1)
		seen := map[string]bool{}
		var duplicates []string
		for _, match := range matches {
			if seen[match[1]] {
				duplicates = append(duplicates, match[1])
			}
			seen[match[1]] = true
		}
		return duplicates
	}

	for _, mode := range []struct {
		name string
		data MetaFormData
	}{
		{name: "create", data: MetaFormData{Mode: "create"}},
		{name: "edit", data: MetaFormData{Mode: "edit", LimitID: "lim-1", CategoryID: "cat-1", MaxAmountInput: "1.800,00"}},
	} {
		body := renderMetasTemplateForTest(t, testMetasFormTemplates(t), "metas-form", mode.data)
		if got := duplicateIDs(body); len(got) != 0 {
			t.Fatalf("%s form duplicate ids = %v", mode.name, got)
		}
		if got := strings.Count(body, `id="limiteMaxAmount"`); got != 1 {
			t.Fatalf("%s form amount id count = %d", mode.name, got)
		}
	}
}

func TestMetasCSSKeepsPrivacyTouchAndValidErrorRule(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	for _, expected := range []string{
		".metas-form__alert,\n[data-metas-sheet] [id=\"limite-form-error\"]:not(.hidden) {",
		".privacy-enabled .cb-v2-metas-main [data-sensitive]",
		"content: \"••••\"",
		".metas-card__menu-toggle",
		".metas-progress {",
		"min-height: 2.75rem;",
		"width: 2.75rem;",
		"height: 2.75rem;",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("metas.css missing contract %q", expected)
		}
	}
	if strings.Contains(css, "caixinha-form-error") {
		t.Fatal("metas.css must not retain the removed reserve form error selector")
	}
	open := strings.Count(css, "{")
	close := strings.Count(css, "}")
	if open != close {
		t.Fatalf("metas.css brace balance = %d/%d", open, close)
	}
}

func TestLimitToneThresholdsRemainCentralized(t *testing.T) {
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
			t.Errorf("ToneLabel(%d%%) empty", tc.percent)
		}
	}
}
