package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHandleHistoricoCaixinhaListsEventsWithFriendlyLabels(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-checking', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-1', 'ws-a', 'user-a', 'a-checking', 'a-cat', 'EXPENSE', 2500, ?, 'Consumo manual', 'paid', ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, source_transaction_id, reference_date, created_at)
		VALUES
			('h-rch1', 'a-box', 5000, 'RECHARGE', 'Aporte manual', NULL, ?, ?),
			('h-rel1', 'a-box', -1000, 'RELEASE', 'Liberacao parcial', NULL, ?, ?),
			('h-cns1', 'a-box', -2500, 'CONSUME', 'Consumo automatico', 'tx-1', ?, ?),
			('h-rev1', 'a-box', 2500, 'REVERSAL', 'Ajuste compensatorio', 'tx-1', ?, ?)
	`, now, now, now, now, now, now, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id=a-box", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	assertContains(t, body, "Caixinha A")
	assertContains(t, body, "Aporte")
	assertContains(t, body, "Liberacao")
	assertContains(t, body, "Consumo")
	assertContains(t, body, "Ajuste compensatorio")
	assertContains(t, body, "+R$ 50,00")
	assertContains(t, body, "R$ -10,00")
	assertContains(t, body, "R$ -25,00")
	assertContains(t, body, "+R$ 25,00")
	assertContains(t, body, "Conta · Conta A")
}

func TestHandleHistoricoLimiteShowsAccountContext(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('a-card', 'ws-a', 'Cartão A', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly)
		VALUES ('limit-a', 'ws-a', 'a-cat', 100000)
	`)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-card', 'ws-a', 'user-a', 'a-card', 'a-cat', 'EXPENSE', 12500, ?, 'Compra no cartão', 'paid', ?, ?)
	`, now, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/limite/historico?limit_id=limit-a", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoLimite(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	assertContains(t, body, "Cartão · Cartão A")
	assertContains(t, body, "Compra no cartão")
}

func TestHandleHistoricoCaixinhaEmptyBoxShowsEmptyState(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id=a-box", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	assertContains(t, body, "Caixinha A")
	assertContains(t, body, "Nenhum evento registrado")
}

func TestHandleHistoricoCaixinhaRejectsForeignWorkspaceBox(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id=b-box", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleHistoricoCaixinhaRejectsMissingBoxID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestMetasPageRendersActionsInMenu(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-a', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas", nil)
	rr := httptest.NewRecorder()
	h.HandleListarMetas(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	// Valida botao "Mais" unificado (mobile + desktop)
	assertContains(t, body, "Mais")

	// Valida opcoes do menu dropdown
	assertContains(t, body, "Aportar")
	assertContains(t, body, "Liberar")
	assertContains(t, body, "Editar")

	// Valida que o dropdown usa a arquitetura data-caixinha-dropdown (sem <details>)
	if strings.Contains(body, `<details`) {
		t.Fatalf("template should not contain <details> elements")
	}
	assertContains(t, body, `data-caixinha-dropdown`)
	assertContains(t, body, `data-ck-toggle`)

	// Valida que os botoes do menu carregam o sheet de aporte/resgate
	assertContains(t, body, `hx-get="/metas/caixinha/aporte/sheet`)
	assertContains(t, body, `hx-get="/metas/caixinha/resgate/sheet`)
}

func TestMetasPageVerLancamentosLinkConditional(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-a', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas", nil)
	rr := httptest.NewRecorder()
	h.HandleListarMetas(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()

	if !strings.Contains(body, "Lançamentos") {
		t.Fatalf("listing missing 'Lançamentos' (with cedilha) for boxes with categories")
	}

	if !strings.Contains(body, `/lancamentos?categoria=`) {
		t.Fatalf("'Lancamentos' link missing categoria query parameter")
	}

	if !strings.Contains(body, `/lancamentos?categoria=a-cat-`) {
		t.Fatalf("'Lancamentos' link missing expected category id pattern, body:\n%s", body)
	}
}

func TestMetasPageCardOpensHistoryNotEditForm(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaProjectionScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-a', 'ws-a', 'Conta A', 'CHECKING', 0, 0, ?, ?)
	`, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas?aba=caixinhas", nil)
	rr := httptest.NewRecorder()
	h.HandleListarMetas(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()
	if !strings.Contains(body, `/metas/caixinha/historico?box_id=`) {
		t.Fatalf("card hx-get should point to history, not edit form")
	}
	if !strings.Contains(body, `hx-target="#bottom-sheet-container"`) {
		t.Fatalf("card should target bottom-sheet-container")
	}
}

func TestHandleHistoricoCaixinhaShowsOnlyWorkspaceEvents(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedHistoryScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES ('h-b-rch', 'b-box', 99999, 'RECHARGE', 'Aporte workspace B', ?, ?)
	`, now, now)

	h := MetasHandler{
		DB:          db,
		Templates:   testHistoryTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodGet, "/metas/caixinha/historico?box_id=a-box", nil)
	rr := httptest.NewRecorder()
	h.HandleHistoricoCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()
	if strings.Contains(body, "workspace B") {
		t.Fatalf("history leaked workspace B event into workspace A response")
	}
	if strings.Contains(body, "99999") || strings.Contains(body, "999,00") {
		t.Fatalf("history leaked foreign wallet amount")
	}
}

func TestBoxLedgerTypeLabelMapsAllKnownTypes(t *testing.T) {
	cases := map[string]string{
		"RECHARGE": "Aporte",
		"BONUS":    "Ajuste/Bonus",
		"RELEASE":  "Liberacao",
		"CONSUME":  "Consumo",
		"REVERSAL": "Ajuste compensatorio",
		"UNKNOWN":  "UNKNOWN",
		"":         "",
	}
	for typ, want := range cases {
		got := boxLedgerTypeLabel(typ)
		if got != want {
			t.Errorf("boxLedgerTypeLabel(%q) = %q, want %q", typ, got, want)
		}
	}
}

func seedHistoryScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@exemplo.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@exemplo.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('a-cat', 'ws-a', 'Categoria A', 'EXPENSE', ?),
			('b-cat', 'ws-b', 'Categoria B', 'EXPENSE', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('a-box', 'ws-a', 'Caixinha A', 'a-cat', 100000, 10000, ?, ?),
			('b-box', 'ws-b', 'Caixinha B', 'b-cat', 100000, 10000, ?, ?)
	`, now, now, now, now)
}

func testHistoryTemplates(t *testing.T) TemplateEngine {
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
	return template.Must(template.New("history-test").Parse(stubs + string(content)))
}
