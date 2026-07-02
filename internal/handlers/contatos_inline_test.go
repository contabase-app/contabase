package handlers

import (
	"database/sql"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubContatoTpl captura chamadas de ExecuteTemplate para validar a renderização
// do formulário inline de contato sem depender do parse real dos templates.
type stubContatoTpl struct {
	lastTemplate string
	lastData     ContatoInlineCreateData
	calls        int
}

func (s *stubContatoTpl) ExecuteTemplate(wr io.Writer, name string, data any) error {
	s.calls++
	s.lastTemplate = name
	if d, ok := data.(ContatoInlineCreateData); ok {
		s.lastData = d
	}
	return nil
}

func (s *stubContatoTpl) Lookup(name string) *template.Template { return nil }

func newBusinessWorkspaceContactTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-biz', 'Workspace Business', '', 'business', ?, ?)
	`, now, now)
	return db
}

func postContatoInlineForm(t *testing.T, db *sql.DB, workspaceID, name, contactType, document, email, phone string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("name", name)
	form.Set("type", contactType)
	form.Set("document", document)
	form.Set("email", email)
	form.Set("phone", phone)
	req := httptest.NewRequest(http.MethodPost, "/api/contatos/inline", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: &stubContatoTpl{}, WorkspaceID: workspaceID, UserID: "user-biz"}
	h.HandleContatoInlineCreate(rec, req)
	return rec
}

func postContatoInlineFormFull(t *testing.T, db *sql.DB, workspaceID, name, contactType, document, email, phone, customClientID string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	form.Set("name", name)
	form.Set("type", contactType)
	form.Set("document", document)
	form.Set("email", email)
	form.Set("phone", phone)
	form.Set("custom_client_id", customClientID)
	req := httptest.NewRequest(http.MethodPost, "/api/contatos/inline", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: &stubContatoTpl{}, WorkspaceID: workspaceID, UserID: "user-biz"}
	h.HandleContatoInlineCreate(rec, req)
	return rec
}

func TestHandleContatoInlineCreate_BusinessInsereContatoNoWorkspace(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	rec := postContatoInlineForm(t, db, "ws-biz", "Padaria Estrela", "vendor", "12.345.678/0001-99", "contato@padaria.com", "(11) 98888-7777")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "contactCreated") {
		t.Fatalf("HX-Trigger = %q, esperava contactCreated", trigger)
	}
	if !strings.Contains(trigger, "Padaria Estrela") {
		t.Fatalf("HX-Trigger = %q, esperava nome do contato", trigger)
	}

	var name, ctype string
	if err := db.QueryRow(`SELECT name, type FROM contacts WHERE workspace_id = ? ORDER BY created_at DESC LIMIT 1`, "ws-biz").Scan(&name, &ctype); err != nil {
		t.Fatalf("contato não inserido: %v", err)
	}
	if name != "Padaria Estrela" || ctype != "vendor" {
		t.Fatalf("contato inserido = (%s,%s), want (Padaria Estrela,vendor)", name, ctype)
	}
}

func TestHandleContatoInlineCreate_IsolamentoWorkspace(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-biz-outro', 'Workspace Business Outro', '', 'business', ?, ?)
	`, now, now)

	_ = postContatoInlineForm(t, db, "ws-biz", "Fornecedor A", "vendor", "", "", "")

	var countOutro int
	if err := db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE workspace_id = ?`, "ws-biz-outro").Scan(&countOutro); err != nil {
		t.Fatalf("query ws-biz-outro: %v", err)
	}
	if countOutro != 0 {
		t.Fatalf("vazou contato para outro workspace: count=%d", countOutro)
	}

	var countBiz, countAll int
	if err := db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE workspace_id = ?`, "ws-biz").Scan(&countBiz); err != nil {
		t.Fatalf("query ws-biz: %v", err)
	}
	_ = db.QueryRow(`SELECT COUNT(1) FROM contacts`).Scan(&countAll)
	if countBiz != 1 {
		t.Fatalf("contatos no ws-biz = %d, want 1", countBiz)
	}
	if countAll != 1 {
		t.Fatalf("contatos totais = %d, want 1", countAll)
	}
}

func TestHandleContatoInlineCreate_ValidacaoNomeObrigatorio(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	tpl := &stubContatoTpl{}
	form := url.Values{}
	form.Set("name", "")
	form.Set("type", "vendor")
	req := httptest.NewRequest(http.MethodPost, "/api/contatos/inline", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineCreate(rec, req)

	if tpl.lastTemplate != "contato-picker-create" {
		t.Fatalf("template renderizado = %q, want contato-picker-create", tpl.lastTemplate)
	}
	if !strings.Contains(tpl.lastData.Error, "Nome") {
		t.Fatalf("erro renderizado = %q, queria menção a Nome", tpl.lastData.Error)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE workspace_id = ?`, "ws-biz").Scan(&count)
	if count != 0 {
		t.Fatalf("nenhum contato deveria ser inserido, count=%d", count)
	}
}

func TestHandleContatoInlineCreate_TipoInvalidoSemInserir(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	tpl := &stubContatoTpl{}
	form := url.Values{}
	form.Set("name", "Cliente Sem Tipo")
	form.Set("type", "estranho")
	req := httptest.NewRequest(http.MethodPost, "/api/contatos/inline", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineCreate(rec, req)

	if !strings.Contains(tpl.lastData.Error, "Tipo") {
		t.Fatalf("erro renderizado = %q, queria menção a Tipo", tpl.lastData.Error)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE workspace_id = ?`, "ws-biz").Scan(&count)
	if count != 0 {
		t.Fatalf("nenhum contato deveria ser inserido com tipo inválido, count=%d", count)
	}
}

func TestHandleContatoInlineCreate_NaoBusinessRetorna404(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-pers', 'Pessoal', '', 'personal', ?, ?)
	`, now, now)

	rec := postContatoInlineForm(t, db, "ws-pers", "Contato Pessoal", "client", "", "", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 em workspace pessoal", rec.Code)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE workspace_id = ?`, "ws-pers").Scan(&count)
	if count != 0 {
		t.Fatalf("não deveria criar contato em workspace pessoal, count=%d", count)
	}
}

func TestHandleContatoInlineForm_NaoBusinessRetorna404(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-pers', 'Pessoal', '', 'personal', ?, ?)
	`, now, now)

	tpl := &stubContatoTpl{}
	req := httptest.NewRequest(http.MethodGet, "/api/contatos/novo?name=Ana", nil)
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-pers", UserID: "user-pers"}
	h.HandleContatoInlineForm(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if tpl.calls != 0 {
		t.Fatalf("template não deveria ser renderizado em workspace pessoal, calls=%d", tpl.calls)
	}
}

func TestHandleContatoInlineForm_BusinessPreencheNomeETipo(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	tpl := &stubContatoTpl{}
	req := httptest.NewRequest(http.MethodGet, "/api/contatos/novo?name=Padaria&type=client", nil)
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineForm(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if tpl.lastTemplate != "contato-picker-create" {
		t.Fatalf("template = %q, want contato-picker-create", tpl.lastTemplate)
	}
	if tpl.lastData.Name != "Padaria" {
		t.Fatalf("name preenchido = %q, want Padaria", tpl.lastData.Name)
	}
	if tpl.lastData.Type != "client" {
		t.Fatalf("type preenchido = %q, want client", tpl.lastData.Type)
	}
}

func TestHandleContatoInlineForm_BusinessPreencheCustomClientIDPlaceholder(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	tpl := &stubContatoTpl{}
	req := httptest.NewRequest(http.MethodGet, "/api/contatos/novo?name=Padaria", nil)
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineForm(rec, req)

	if tpl.lastData.CustomClientIDPlaceholder != "CLI-001" {
		t.Fatalf("CustomClientIDPlaceholder = %q, want CLI-001", tpl.lastData.CustomClientIDPlaceholder)
	}
}

func TestHandleContatoInlineCreate_SalvaCustomClientIDPreenchido(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	rec := postContatoInlineFormFull(t, db, "ws-biz", "Cliente A", "client", "12.345.678/0001-99", "a@b.com", "(11) 99999-0000", "CLI-005")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var cid string
	if err := db.QueryRow(`SELECT COALESCE(custom_client_id, '') FROM contacts WHERE workspace_id = ? ORDER BY created_at DESC LIMIT 1`, "ws-biz").Scan(&cid); err != nil {
		t.Fatalf("query custom_client_id: %v", err)
	}
	if cid != "CLI-005" {
		t.Fatalf("custom_client_id = %q, want CLI-005", cid)
	}

	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, `"customClientId":"CLI-005"`) {
		t.Fatalf("HX-Trigger = %q, esperava customClientId CLI-005", trigger)
	}
}

func TestHandleContatoInlineCreate_AutoGeraCustomClientIDQuandoVazio(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	rec := postContatoInlineForm(t, db, "ws-biz", "Cliente X", "vendor", "", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var cid string
	if err := db.QueryRow(`SELECT COALESCE(custom_client_id, '') FROM contacts WHERE workspace_id = ? ORDER BY created_at DESC LIMIT 1`, "ws-biz").Scan(&cid); err != nil {
		t.Fatalf("query custom_client_id: %v", err)
	}
	if cid != "CLI-001" {
		t.Fatalf("custom_client_id auto-gerado = %q, want CLI-001", cid)
	}
}

func TestHandleContatoInlineCreate_RejeitaCustomClientIDDuplicado(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	_ = postContatoInlineFormFull(t, db, "ws-biz", "Primeiro", "vendor", "", "", "", "CLI-007")

	tpl := &stubContatoTpl{}
	form := url.Values{}
	form.Set("name", "Segundo")
	form.Set("type", "vendor")
	form.Set("custom_client_id", "CLI-007")
	req := httptest.NewRequest(http.MethodPost, "/api/contatos/inline", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineCreate(rec, req)

	if !strings.Contains(tpl.lastData.Error, "ID de cliente") {
		t.Fatalf("erro renderizado = %q, queria menção a duplicidade de ID", tpl.lastData.Error)
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(1) FROM contacts WHERE workspace_id = ?`, "ws-biz").Scan(&count)
	if count != 1 {
		t.Fatalf("count = %d, want 1 (duplicado não inserido)", count)
	}
}

func TestHandleContatoInlineCreate_ErroPreservaCustomClientID(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	tpl := &stubContatoTpl{}
	form := url.Values{}
	form.Set("name", "")
	form.Set("type", "vendor")
	form.Set("custom_client_id", "CLI-100")
	req := httptest.NewRequest(http.MethodPost, "/api/contatos/inline", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineCreate(rec, req)

	if tpl.lastData.CustomClientID != "CLI-100" {
		t.Fatalf("CustomClientID preservado = %q, want CLI-100", tpl.lastData.CustomClientID)
	}
}

func TestHandleContatoInlineCreate_NaoExibeUUIDComoIDVisual(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	rec := postContatoInlineFormFull(t, db, "ws-biz", "Cliente Z", "client", "", "", "", "")
	trigger := rec.Header().Get("HX-Trigger")

	// O campo id no payload é o UUID interno (necessário para seleção no cliente),
	// mas o customClientId visual deve ser o auto-gerado (CLI-001), não o UUID.
	if !strings.Contains(trigger, `"customClientId":"CLI-001"`) {
		t.Fatalf("HX-Trigger = %q, esperava customClientId CLI-001, não o UUID", trigger)
	}
	if strings.Contains(trigger, `"customClientId":"`) && strings.Count(trigger, `"customClientId":"CLI-`) == 0 {
		t.Fatalf("HX-Trigger = %q, customClientId não deve ser UUID", trigger)
	}
}

func TestSplitNameDocument(t *testing.T) {
	tests := []struct {
		raw      string
		wantName string
		wantDoc  string
	}{
		{"", "", ""},
		{"Padaria", "Padaria", ""},
		{"Mônica", "Mônica", ""},
		{"Padaria Estrela · 11.111.111/0001-11", "Padaria Estrela", "11.111.111/0001-11"},
		{"João - 123.456.789-00", "João", "123.456.789-00"},
		{"Maria, 11122233344", "Maria", "11122233344"},
		{"Fornecedor · abc", "Fornecedor · abc", ""}, // segunda parte sem dígitos
		{"Empresa - xpto", "Empresa - xpto", ""},
		{"Cliente, semdoc", "Cliente, semdoc", ""},
		{"Com · múltiplos · 111111111-11", "Com · múltiplos", "111111111-11"}, // último separador
	}
	for _, tc := range tests {
		name, doc := splitNameDocument(tc.raw)
		if name != tc.wantName || doc != tc.wantDoc {
			t.Errorf("splitNameDocument(%q) = (%q, %q), want (%q, %q)", tc.raw, name, doc, tc.wantName, tc.wantDoc)
		}
	}
}

func TestHandleContatoInlineForm_ParseiaNomeDocumentoPeloSeparador(t *testing.T) {
	db := newBusinessWorkspaceContactTestDB(t)
	defer db.Close()

	tpl := &stubContatoTpl{}
	req := httptest.NewRequest(http.MethodGet, "/api/contatos/novo?name=Padaria+Estrela+%C2%B7+12.345.678%2F0001-99", nil)
	rec := httptest.NewRecorder()
	h := ContatosHandler{DB: db, Templates: tpl, WorkspaceID: "ws-biz", UserID: "user-biz"}
	h.HandleContatoInlineForm(rec, req)

	if tpl.lastData.Name != "Padaria Estrela" {
		t.Fatalf("name = %q, want Padaria Estrela", tpl.lastData.Name)
	}
	if tpl.lastData.Document != "12.345.678/0001-99" {
		t.Fatalf("document = %q, want 12.345.678/0001-99", tpl.lastData.Document)
	}
}