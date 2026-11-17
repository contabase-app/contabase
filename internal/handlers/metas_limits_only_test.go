package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetasPageRendersOnlyLimits(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-metas-limits")
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, created_at, updated_at)
		VALUES ('limit-only', ?, ?, 5000, 80, unixepoch(), unixepoch())
	`, base.ws, base.category)

	h := MetasHandler{
		ActorRole:   testMetasAdminRole,
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: base.ws,
		UserID:      base.user,
	}
	rr := httptest.NewRecorder()
	h.HandleMetasConceito(rr, httptest.NewRequest(http.MethodGet, "/metas?aba=reservas", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"Metas", "Limites", "Gastos este mês", "limit-only"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metas page missing %q", want)
		}
	}
	for _, removed := range []string{
		`role="tablist"`,
		`metas-caixinhas-panel`,
		`Nova reserva`,
		`/metas/caixinha`,
		`>Reservas<`,
	} {
		if strings.Contains(body, removed) {
			t.Fatalf("metas page still contains reserve surface %q", removed)
		}
	}
}

func TestMetasFormRendersOnlyLimitForm(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-metas-form")

	h := MetasHandler{
		ActorRole:   testMetasAdminRole,
		DB:          db,
		Templates:   testMetasFormTemplates(t),
		WorkspaceID: base.ws,
		UserID:      base.user,
	}
	rr := httptest.NewRecorder()
	h.HandleNovaMeta(rr, httptest.NewRequest(http.MethodGet, "/metas/novo?aba=reserva", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`id="meta-limite-form"`, `Novo limite`, `name="max_amount_monthly"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("limit form missing %q", want)
		}
	}
	for _, removed := range []string{`id="meta-caixinha-form"`, `id="tab-caixinha"`, `caixinhaName`, `Reservas`} {
		if strings.Contains(body, removed) {
			t.Fatalf("limit form still contains reserve surface %q", removed)
		}
	}
}

func TestMetasHTMXPartialRendersOnlyLimits(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	base := seedF1Base(t, db, "ws-metas-htmx")
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, created_at, updated_at)
		VALUES ('limit-htmx', ?, ?, 5000, 80, unixepoch(), unixepoch())
	`, base.ws, base.category)

	h := MetasHandler{
		ActorRole:   testMetasAdminRole,
		DB:          db,
		Templates:   testMetasPageTemplates(t),
		WorkspaceID: base.ws,
		UserID:      base.user,
	}
	req := httptest.NewRequest(http.MethodGet, "/metas?aba=reservas&partial=conteudo", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	h.HandleMetasConceito(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`id="metas-tabs"`, `metas-panel-limites`, `Gastos este mês`, `hx-swap-oob="outerHTML"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("HTMX partial missing %q", want)
		}
	}
	for _, removed := range []string{`role="tablist"`, `metas-caixinhas-panel`, `/metas/caixinha`} {
		if strings.Contains(body, removed) {
			t.Fatalf("HTMX partial still contains reserve surface %q", removed)
		}
	}
}
