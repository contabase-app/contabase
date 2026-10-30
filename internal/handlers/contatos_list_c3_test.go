package handlers

import (
	"bytes"
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// C3 — Cards, listagem, filtros e permissões de Contatos.
//
// Contratos cobertos aqui:
//   - iniciais do avatar sem slicing por bytes (1 caractere, acentos, Unicode);
//   - tabs Premium com ARIA, roving tabindex e filtros preservados;
//   - busca e tipo combinados, com "%"_ escapados como literais;
//   - resposta de criação coerente com a seleção ativa;
//   - resposta de edição devolvendo o card (não a linha legada);
//   - matriz de permissões ADMIN/MANAGER/USER e isolamento por workspace.

type contatosC3Capture struct {
	name string
	data ContatosData
}

func (c *contatosC3Capture) ExecuteTemplate(w io.Writer, name string, data any) error {
	c.name = name
	if d, ok := data.(ContatosData); ok {
		c.data = d
	}
	return nil
}

func (c *contatosC3Capture) Lookup(name string) *template.Template { return template.New(name) }

func newContatosC3DB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	seedTenantIsolationScenario(t, db)
	return db
}

func insertContatosC3Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO contacts (id, workspace_id, custom_client_id, name, document, type, email, phone, created_at)
		VALUES
			('c3-ana-client', 'ws-a', 'C3-001', 'Ana Lima', '123.456.789-00', 'client', 'ana.c3@example.com', '(11) 90000-0001', unixepoch()),
			('c3-ana-vendor', 'ws-a', 'C3-002', 'Ana Store', '', 'vendor', '', '', unixepoch()),
			('c3-bruno', 'ws-a', 'C3-003', 'Bruno Costa', '', 'client', '', '', unixepoch()),
			('c3-percent', 'ws-a', 'C3-004', '100% Ltda', '', 'vendor', '', '', unixepoch()),
			('c3-underscore', 'ws-a', 'C3-005', 'Ana_Lima', '', 'vendor', '', '', unixepoch())
	`)
}

func TestContactInitialsHandlesUnicodeAndSingleCharacter(t *testing.T) {
	cases := map[string]string{
		"A":               "A",
		"a":               "A",
		"Ana":             "A",
		"Ana Lima":        "AL",
		"Ana Maria Lima":  "AL",
		"  ana   maria  ": "AM",
		"Ana de Lima":     "AL",
		"Álvaro Évora":    "ÁÉ",
		"É":               "É",
		"😀":               "😀",
		"":                "?",
		"   ":             "?",
	}
	for name, want := range cases {
		if got := contactInitials(name); got != want {
			t.Fatalf("contactInitials(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestContatoCardRendersSingleCharacterNameWithoutPanic(t *testing.T) {
	tpl := testContatosRenderTemplates(t)

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contato-card", ContatoRow{
		ID:        "contact-1",
		Name:      "A",
		Initials:  contactInitials("A"),
		Type:      "client",
		TypeLabel: "Cliente",
		CreatedAt: "10/06/2026",
	}); err != nil {
		t.Fatalf("render single-character card: %v", err)
	}
	body := out.String()
	assertContains(t, body, `<span class="cb-contact-avatar cb-contact-avatar-client" aria-hidden="true">A</span>`)
	assertContains(t, body, `<h3>A</h3>`)
	if strings.Contains(body, `cb-contact-actions`) {
		t.Fatal("a role without update/delete must not render card actions")
	}
}

func TestContatosListAdoptsPremiumTabsWithARIA(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	data := contatosPremiumFixture()

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contatos-list", data); err != nil {
		t.Fatalf("render contatos-list: %v", err)
	}
	body := out.String()

	for _, expected := range []string{
		`class="cb-tabs cb-v2-contatos-tabs" role="tablist" aria-label="Filtrar contatos por tipo" aria-orientation="horizontal"`,
		`id="contatos-tab-todos"`,
		`id="contatos-tab-client"`,
		`id="contatos-tab-vendor"`,
		`role="tab"`,
		`aria-controls="contatos-grid"`,
		`aria-selected="true"`,
		`aria-selected="false"`,
		`tabindex="0"`,
		`tabindex="-1"`,
		`id="contatos-grid" role="tabpanel" aria-labelledby="contatos-tab-client"`,
		`hx-include="[name='q']"`,
		`hx-push-url="true"`,
		`hx-replace-url="true"`,
		`value="ana"`,
	} {
		assertContains(t, body, expected)
	}

	var page bytes.Buffer
	if err := tpl.ExecuteTemplate(&page, "contatos-page", data); err != nil {
		t.Fatalf("render contatos-page: %v", err)
	}
	assertContains(t, page.String(), `/assets/js/contatos-tabs.js?v=test`)
	// A aba ativa é a única com aria-selected="true" e tabindex="0".
	if got := strings.Count(body, `aria-selected="true"`); got != 1 {
		t.Fatalf("expected exactly one aria-selected=true, got %d\nbody:\n%s", got, body)
	}
	if got := strings.Count(body, `tabindex="0"`); got != 1 {
		t.Fatalf("expected exactly one tabindex=0, got %d\nbody:\n%s", got, body)
	}
	// Contadores ficam fora desta fase: os totais não podem ser derivados com
	// semântica segura quando a busca está ativa.
	if strings.Contains(body, `ui-pill`) {
		t.Fatal("tabs must not render counters without an unambiguous total")
	}
}

func TestContatosListCombinesQueryAndType(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()
	insertContatosC3Fixture(t, db)

	h := ContatosHandler{DB: db, WorkspaceID: "ws-a", Role: "ADMIN"}

	cases := []struct {
		query string
		tipo  string
		want  []string
	}{
		{query: "ana", tipo: "client", want: []string{"c3-ana-client"}},
		{query: "ana", tipo: "vendor", want: []string{"c3-ana-vendor", "c3-underscore"}},
		{query: "ana", tipo: "todos", want: []string{"c3-ana-client", "c3-ana-vendor", "c3-underscore"}},
		{query: "", tipo: "vendor", want: []string{"c3-percent", "c3-ana-vendor", "c3-underscore"}},
		{query: "bruno", tipo: "todos", want: []string{"c3-bruno"}},
	}

	for _, tc := range cases {
		data, err := h.buildContatosListData(tc.query, tc.tipo, false)
		if err != nil {
			t.Fatalf("build list (q=%q, tipo=%q): %v", tc.query, tc.tipo, err)
		}
		got := make([]string, 0, len(data.Contatos))
		for _, row := range data.Contatos {
			got = append(got, row.ID)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("q=%q tipo=%q = %v, want %v", tc.query, tc.tipo, got, tc.want)
		}
		if data.Query != tc.query || data.Tipo != tc.tipo {
			t.Fatalf("list state = (q=%q, tipo=%q), want (%q, %q)", data.Query, data.Tipo, tc.query, tc.tipo)
		}
	}
}

func TestContatosSearchTreatsLikeWildcardsLiterally(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()
	insertContatosC3Fixture(t, db)

	h := ContatosHandler{DB: db, WorkspaceID: "ws-a", Role: "ADMIN"}

	cases := []struct {
		query string
		want  []string
	}{
		{query: "%", want: []string{"c3-percent"}},
		{query: "_", want: []string{"c3-underscore"}},
		{query: `\`, want: nil},
	}
	for _, tc := range cases {
		rows, err := h.queryContatos(tc.query)
		if err != nil {
			t.Fatalf("queryContatos(%q): %v", tc.query, err)
		}
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.ID)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("queryContatos(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestContatosCreateResponsePreservesActiveFilters(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()
	insertContatosC3Fixture(t, db)

	capture := &contatosC3Capture{}
	h := ContatosHandler{DB: db, Templates: capture, WorkspaceID: "ws-a", UserID: "user-a", Role: "MANAGER"}

	form := url.Values{}
	form.Set("name", "Novo Fornecedor")
	form.Set("type", "vendor")
	req := httptest.NewRequest(http.MethodPost, "/contatos", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://localhost:8080/contatos?tipo=vendor&q=ana&partial=lista")

	rr := httptest.NewRecorder()
	h.HandleCriarContato(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if capture.name != "contatos-list" {
		t.Fatalf("rendered template = %q, want contatos-list", capture.name)
	}
	if capture.data.Query != "ana" || capture.data.Tipo != "vendor" {
		t.Fatalf("create response state = (q=%q, tipo=%q), want (ana, vendor)", capture.data.Query, capture.data.Tipo)
	}
	// A lista devolvida precisa continuar filtrada pelo estado ativo.
	if len(capture.data.Contatos) == 0 {
		t.Fatal("create response listed no contacts; the active vendor filter was not applied")
	}
	for _, row := range capture.data.Contatos {
		if row.Type != "vendor" {
			t.Fatalf("create response listed %q (%s), want only vendors", row.Name, row.Type)
		}
	}
}

func TestContatosEditFormRequiresUpdatePermission(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()

	handlerTpl := testContatosRenderTemplates(t)

	t.Run("user cannot open the edit sheet", func(t *testing.T) {
		h := ContatosHandler{DB: db, Templates: handlerTpl, WorkspaceID: "ws-a", UserID: "user-a", Role: "USER"}
		rr := httptest.NewRecorder()
		h.HandleDetalheContato(rr, httptest.NewRequest(http.MethodGet, "/contatos/a-contact", nil), "a-contact")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("manager opens the edit sheet", func(t *testing.T) {
		h := ContatosHandler{DB: db, Templates: handlerTpl, WorkspaceID: "ws-a", UserID: "user-a", Role: "MANAGER"}
		rr := httptest.NewRecorder()
		h.HandleDetalheContato(rr, httptest.NewRequest(http.MethodGet, "/contatos/a-contact", nil), "a-contact")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		assertContains(t, body, `data-contatos-sheet="edit"`)
		assertContains(t, body, `hx-post="/contatos/a-contact/salvar"`)
		assertContains(t, body, `hx-target="#contatos-list"`)
		assertContains(t, body, `data-contato-delete="a-contact"`)
		assertContains(t, body, `Zona de perigo`)
	})

	t.Run("other workspace contact stays hidden", func(t *testing.T) {
		h := ContatosHandler{DB: db, Templates: handlerTpl, WorkspaceID: "ws-a", UserID: "user-a", Role: "MANAGER"}
		rr := httptest.NewRecorder()
		h.HandleDetalheContato(rr, httptest.NewRequest(http.MethodGet, "/contatos/b-contact", nil), "b-contact")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestContatosUpdateResponseReturnsFilteredListAndClosesSheet(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()
	insertContatosC3Fixture(t, db)

	h := ContatosHandler{
		DB:          db,
		Templates:   testContatosRenderTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
		Role:        "MANAGER",
	}

	form := url.Values{}
	form.Set("name", "Ana Editada")
	form.Set("type", "client")
	form.Set("custom_client_id", "C3-001")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/contatos/c3-ana-client/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://localhost:8080/contatos?tipo=client&q=ana")
	h.HandleAtualizarContato(rr, req, "c3-ana-client")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	assertContains(t, body, `id="contatos-list"`)
	assertContains(t, body, `id="contato-c3-ana-client"`)
	assertContains(t, body, `<h3>Ana Editada</h3>`)
	assertContains(t, body, `id="bottom-sheet-container" hx-swap-oob="innerHTML"`)
	if strings.Contains(body, `cb-contact-row`) {
		t.Fatal("update must re-render the list, not a legacy row fragment")
	}
	// A seleção ativa (tipo=client, q=ana) é respeitada na resposta.
	assertContains(t, body, `hx-get="/contatos?tipo=client&partial=lista"`)
	if strings.Contains(body, `>Fornecedor<`) {
		t.Fatal("filtered update response must not list vendors")
	}
}

func TestContatosUpdateRemovesContactThatLeavesActiveFilter(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()
	insertContatosC3Fixture(t, db)

	h := ContatosHandler{
		DB:          db,
		Templates:   testContatosRenderTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
		Role:        "MANAGER",
	}

	form := url.Values{}
	form.Set("name", "Ana Editada")
	form.Set("type", "vendor")
	form.Set("custom_client_id", "C3-001")

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/contatos/c3-ana-client/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://localhost:8080/contatos?tipo=client&q=ana")
	h.HandleAtualizarContato(rr, req, "c3-ana-client")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, `id="contato-c3-ana-client"`) {
		t.Fatal("contact that no longer matches the active type filter must leave the list")
	}
	assertContains(t, body, `class="ui-empty" role="status"`)
}

func TestContatosCardActionsFollowFixedRoleMatrix(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()

	tpl := testContatosRenderTemplates(t)

	cases := []struct {
		role      string
		canUpdate bool
		canDelete bool
	}{
		{role: "ADMIN", canUpdate: true, canDelete: true},
		{role: "MANAGER", canUpdate: true, canDelete: true},
		{role: "USER", canUpdate: false, canDelete: false},
		{role: "", canUpdate: false, canDelete: false},
	}

	for _, tc := range cases {
		h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-a", Role: tc.role}
		data, err := h.buildContatosListData("", "", false)
		if err != nil {
			t.Fatalf("build list (role=%q): %v", tc.role, err)
		}
		if len(data.Contatos) == 0 {
			t.Fatalf("role=%q: seeded workspace has no contacts", tc.role)
		}
		for _, row := range data.Contatos {
			if row.CanUpdate != tc.canUpdate || row.CanDelete != tc.canDelete {
				t.Fatalf("role=%q flags = (update:%v, delete:%v), want (%v, %v)", tc.role, row.CanUpdate, row.CanDelete, tc.canUpdate, tc.canDelete)
			}
		}

		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "contatos-list", data); err != nil {
			t.Fatalf("render list (role=%q): %v", tc.role, err)
		}
		body := out.String()
		hasEdit := strings.Contains(body, `hx-get="/contatos/a-contact"`)
		if hasEdit != tc.canUpdate {
			t.Fatalf("role=%q rendered edit=%v, want edit=%v", tc.role, hasEdit, tc.canUpdate)
		}
		// A exclusão deixou de viver no card: só existe na danger zone do sheet,
		// que exige o GET de edição (transactions:update).
		if strings.Contains(body, `hx-delete="/contatos/a-contact"`) {
			t.Fatalf("role=%q: card must not expose a delete action", tc.role)
		}
	}
}

func TestContatosUpdateKeepsWorkspaceIsolation(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()

	h := ContatosHandler{DB: db, Templates: testContatosRenderTemplates(t), WorkspaceID: "ws-a", Role: "MANAGER"}

	form := url.Values{}
	form.Set("name", "Invasão")
	form.Set("type", "client")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/contatos/b-contact/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.HandleAtualizarContato(rr, req, "b-contact")

	if rr.Code == http.StatusOK {
		t.Fatalf("cross-workspace update must not succeed; body=%s", rr.Body.String())
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM contacts WHERE id = 'b-contact'`).Scan(&name); err != nil {
		t.Fatalf("query b-contact: %v", err)
	}
	if name != "B Contact" {
		t.Fatalf("cross-workspace contact name = %q, want unchanged", name)
	}
}

func TestContactListStateFallsBackToCurrentURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/contatos", nil)
	req.Header.Set("HX-Current-URL", "http://localhost:8080/contatos?tipo=client&q=ana%20lima")

	query, tipo := contactListState(req)
	if query != "ana lima" || tipo != "client" {
		t.Fatalf("contactListState = (%q, %q), want (ana lima, client)", query, tipo)
	}

	formReq := httptest.NewRequest(http.MethodPost, "/contatos", strings.NewReader("q=beta&tipo=vendor"))
	formReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	query, tipo = contactListState(formReq)
	if query != "beta" || tipo != "vendor" {
		t.Fatalf("contactListState(form) = (%q, %q), want (beta, vendor)", query, tipo)
	}
}
