package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// C4 — Sheets Premium de criação/edição e exclusão segura.
//
// O sheet reutiliza o #bottom-sheet-container global: não tem overlay próprio,
// fecha por closeBottomSheet (ESC/histórico/scroll ficam no overlay-stack) e
// mantém os contratos POST/DELETE existentes.

func TestContatosCreateSheetRendersCanonicalForm(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	h := ContatosHandler{DB: nil, Templates: tpl, Role: "MANAGER"}

	rr := httptest.NewRecorder()
	h.HandleNovoContatoSheet(rr, httptest.NewRequest(http.MethodGet, "/contatos/novo?tipo=vendor", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, expected := range []string{
		`data-contatos-sheet="create"`,
		`role="dialog"`,
		`aria-modal="true"`,
		`class="sheet-in contatos-sheet__dialog"`,
		`hx-post="/contatos"`,
		`hx-target="#contatos-list"`,
		`hx-swap="outerHTML"`,
		`hx-sync="this:drop"`,
		`name="type"`,
		`<option value="vendor" selected>Fornecedor</option>`,
		`name="custom_client_id"`,
		`placeholder="CLI-001"`,
		`name="document"`,
		`data-oninput="maskCnpjCpfContato"`,
		`name="name"`,
		`data-contato-sheet-initial`,
		`name="email"`,
		`name="phone"`,
		`data-oninput="maskPhoneContato"`,
		`class="ui-input`,
		`class="ui-select"`,
		`data-onclick="closeBottomSheet"`,
	} {
		assertContains(t, body, expected)
	}
	// O sheet vive dentro do container global; não pode registrar um overlay
	// paralelo nem duplicar o gerenciador de foco/ESC.
	if strings.Contains(body, `data-contabase-overlay`) {
		t.Fatal("sheet must rely on the shared #bottom-sheet-container overlay")
	}
	if strings.Contains(body, `Zona de perigo`) {
		t.Fatal("creation sheet must not render the danger zone")
	}
}

func TestContatosCreateSheetDefaultsToClientWithoutFilter(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	h := ContatosHandler{Templates: tpl}

	for _, url := range []string{"/contatos/novo", "/contatos/novo?tipo=todos", "/contatos/novo?tipo=invalido"} {
		rr := httptest.NewRecorder()
		h.HandleNovoContatoSheet(rr, httptest.NewRequest(http.MethodGet, url, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", url, rr.Code)
		}
		assertContains(t, rr.Body.String(), `<option value="client" selected>Cliente</option>`)
	}
}

func TestContatosEditSheetCarriesDangerZoneAndUpdateContract(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()

	h := ContatosHandler{
		DB:          db,
		Templates:   testContatosRenderTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
		Role:        "MANAGER",
	}
	rr := httptest.NewRecorder()
	h.HandleDetalheContato(rr, httptest.NewRequest(http.MethodGet, "/contatos/a-contact", nil), "a-contact")

	body := rr.Body.String()
	for _, expected := range []string{
		`data-contatos-sheet="edit"`,
		`data-contato-id="a-contact"`,
		`hx-post="/contatos/a-contact/salvar"`,
		`hx-target="#contatos-list"`,
		`data-contato-delete="a-contact"`,
		`data-contato-name="A Contact"`,
		`Zona de perigo`,
		`Excluir contato`,
	} {
		assertContains(t, body, expected)
	}
}

func TestContatosCreateResponseClosesSheetAndKeepsFilters(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()
	insertContatosC3Fixture(t, db)

	capture := &contatosC3Capture{}
	h := ContatosHandler{DB: db, Templates: capture, WorkspaceID: "ws-a", UserID: "user-a", Role: "MANAGER"}

	req := httptest.NewRequest(http.MethodPost, "/contatos", strings.NewReader("name=Novo+Fornecedor&type=vendor"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Current-URL", "http://localhost:8080/contatos?tipo=vendor&q=ana")

	rr := httptest.NewRecorder()
	h.HandleCriarContato(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	assertContains(t, rr.Body.String(), `id="bottom-sheet-container" hx-swap-oob="innerHTML"`)
	if strings.Contains(rr.Body.String(), `id="contato-form-error"`) {
		t.Fatal("sheet error region must not be re-created after the sheet is cleared")
	}
	if capture.data.Query != "ana" || capture.data.Tipo != "vendor" {
		t.Fatalf("response state = (%q, %q), want (ana, vendor)", capture.data.Query, capture.data.Tipo)
	}
}

func TestContatosDeleteContractRemainsAvailable(t *testing.T) {
	db := newContatosC3DB(t)
	defer db.Close()

	h := ContatosHandler{DB: db, WorkspaceID: "ws-a"}
	rr := httptest.NewRecorder()
	h.HandleExcluirContato(rr, httptest.NewRequest(http.MethodDelete, "/contatos/b-contact", nil), "b-contact")

	// b-contact pertence a outro workspace: nada é excluído. O contrato atual
	// responde erro (0 linhas), sem tocar no registro de outro workspace.
	if rr.Code == http.StatusOK {
		t.Fatalf("cross-workspace delete must not succeed; body=%s", rr.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE id = 'b-contact' AND workspace_id = 'ws-b'`).Scan(&count); err != nil {
		t.Fatalf("query b-contact: %v", err)
	}
	if count != 1 {
		t.Fatalf("cross-workspace contact count = %d, want 1", count)
	}
}

func TestContatosPrivacyDoesNotMaskSheetInputs(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	h := ContatosHandler{DB: newContatosC3DB(t), Templates: tpl, WorkspaceID: "ws-a", Role: "MANAGER"}
	defer h.DB.Close()

	rr := httptest.NewRecorder()
	h.HandleDetalheContato(rr, httptest.NewRequest(http.MethodGet, "/contatos/a-contact", nil), "a-contact")
	body := rr.Body.String()

	// Inputs editáveis não levam data-sensitive (a máscara é só de listagem).
	if strings.Contains(body, `input[data-sensitive]`) {
		t.Fatal("editable inputs must never be marked as sensitive")
	}
	for _, input := range []string{`name="document"`, `name="email"`, `name="phone"`} {
		assertContains(t, body, input)
	}
	if strings.Contains(body, `data-sensitive title=`) {
		t.Fatal("sheet must not duplicate sensitive values in title attributes")
	}
}
