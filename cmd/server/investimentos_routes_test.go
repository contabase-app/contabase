package main

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/services"
)

type investimentosRoutesHarness struct {
	*investmentsRBACHarness
	mux   *http.ServeMux
	base  *handlers.InvestimentosHandler
	admin string
}

func newInvestimentosRoutesHarness(t *testing.T) *investimentosRoutesHarness {
	return newInvestimentosRoutesHarnessWithFlag(t, "true")
}

func newInvestimentosRoutesHarnessWithFlag(t *testing.T, flag string) *investimentosRoutesHarness {
	t.Helper()
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", flag)
	auth := newInvestmentsRBACHarness(t, nil)
	auth.exec(t, `INSERT INTO workspaces (id, name) VALUES ('workspace-b', 'B')`)
	auth.exec(t, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('workspace-b', 'admin-user', 'ADMIN')`)
	auth.exec(t, `INSERT INTO accounts (id, workspace_id, name, type, archived_at) VALUES
        ('inv-a', 'workspace-rbac', 'Investimento A', 'INVESTMENT', NULL),
        ('inv-b', 'workspace-b', 'Investimento B', 'INVESTMENT', NULL),
        ('checking-a', 'workspace-rbac', 'Corrente A', 'CHECKING', NULL),
        ('archived-a', 'workspace-rbac', 'Arquivada A', 'INVESTMENT', 100)`)
	tpl, err := newAppTemplateEngine(false, buildFuncMap())
	if err != nil {
		t.Fatal(err)
	}
	base := &handlers.InvestimentosHandler{DB: auth.db, WriterDB: auth.db, Templates: tpl}
	h := &investimentosRoutesHarness{investmentsRBACHarness: auth, mux: http.NewServeMux(), base: base}
	registerInvestimentosRoutes(h.mux, auth.service, auth.signer, base)
	h.admin = h.session(t, "admin-user", "workspace-rbac")
	return h
}

func TestInvestimentosFeatureGateOffHidesRoutesAndNavigationWithoutChangingData(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Saved lot")
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "")
	h.mux = http.NewServeMux()
	registerInvestimentosRoutes(h.mux, h.service, h.signer, h.base)
	h.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/investimentos"},
		{"GET", "/investimentos/novo"},
		{"GET", "/investimentos/" + id},
		{"POST", "/investimentos"},
		{"POST", "/investimentos/" + id + "/editar"},
		{"POST", "/investimentos/" + id + "/valuations"},
		{"POST", "/investimentos/" + id + "/resgatar"},
		{"POST", "/investimentos/" + id + "/arquivar"},
		{"GET", "/investimentos/" + id + "/auxiliar"},
	} {
		for _, session := range []string{"", h.admin} {
			rr := h.request(t, tc.method, tc.path, session, nil, false, false)
			requireInvestimentosStatus(t, rr, http.StatusNotFound)
		}
	}
	h.base.WorkspaceID, h.base.UserID = "workspace-rbac", "admin-user"
	rr := httptest.NewRecorder()
	h.base.HandleInvestimentos(rr, httptest.NewRequest("GET", "/investimentos", nil))
	requireInvestimentosStatus(t, rr, http.StatusOK)
	if strings.Contains(rr.Body.String(), `data-v2-nav-section="investments"`) || strings.Contains(rr.Body.String(), `href="/investimentos"`) {
		t.Fatal("disabled Investments link rendered in shared desktop/mobile navigation")
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments WHERE id = ?`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("investment row changed while disabled: count=%d err=%v", count, err)
	}
	t.Setenv("CONTABASE_FEATURE_INVESTMENTS", "true")
	h.mux = http.NewServeMux()
	registerInvestimentosRoutes(h.mux, h.service, h.signer, h.base)
	for _, path := range []string{"/investimentos", "/investimentos/" + id} {
		rr = h.request(t, "GET", path, h.admin, nil, false, false)
		requireInvestimentosStatus(t, rr, http.StatusOK)
		if strings.Count(rr.Body.String(), `data-v2-nav-section="investments"`) != 2 {
			t.Fatal("enabled shared navigation must render desktop and mobile links")
		}
	}
}

func investimentosRouteForm() url.Values {
	return url.Values{"account_id": {"inv-a"}, "name": {"Lote HTTP A"}, "institution": {"Banco A"}, "kind": {"CDB"}, "principal_cents": {"100000"}, "tax_regime": {"IR_REGRESSIVO"}, "applied_date": {"2026-01-15"}, "yield_note": {"100% CDI"}}
}

func (h *investimentosRoutesHarness) request(t *testing.T, method, path, session string, form url.Values, csrf, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if session != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	}
	// A forged role cookie must never authorize the request.
	req.AddCookie(&http.Cookie{Name: "workspace_role", Value: "ADMIN"})
	if csrf {
		addAdminUsersRBACCSRF(t, req, h.signer)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rr := httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	return rr
}

func requireInvestimentosStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status=%d want=%d body=%q", rr.Code, want, rr.Body.String())
	}
}

func (h *investimentosRoutesHarness) create(t *testing.T, name string) string {
	t.Helper()
	form := investimentosRouteForm()
	form.Set("name", name)
	rr := h.request(t, "POST", "/investimentos", h.admin, form, true, false)
	requireInvestimentosStatus(t, rr, 303)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE name = ? AND workspace_id = 'workspace-rbac'`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestInvestimentosRoutesRBACAndCSRF(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "RBAC lot")
	routes := []struct{ method, path string }{
		{"GET", "/investimentos"}, {"POST", "/investimentos"},
		{"GET", "/investimentos/" + id}, {"POST", "/investimentos/" + id + "/editar"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			rr := h.request(t, route.method, route.path, "", investimentosRouteForm(), true, false)
			requireInvestimentosStatus(t, rr, 303)
			if rr.Header().Get("Location") != "/login" {
				t.Fatal("missing login redirect")
			}
			// A nil base panics if the protected callback reaches handler binding.
			// Auth has its own DB; a 403 proves the guard returned beforehand.
			blocked := http.NewServeMux()
			registerInvestimentosRoutes(blocked, h.service, h.signer, nil)
			normal := h.mux
			h.mux = blocked
			defer func() { h.mux = normal }()
			for _, user := range []string{"manager-user", "regular-user"} {
				session := h.session(t, user, "workspace-rbac")
				for _, hx := range []bool{false, true} {
					rr := h.request(t, route.method, route.path, session, investimentosRouteForm(), true, hx)
					requireInvestimentosStatus(t, rr, 403)
					if hx && rr.Header().Get("HX-Retarget") != "#main-content" {
						t.Fatal("403 bypassed respondForbidden")
					}
				}
			}
			if route.method == "POST" {
				requireInvestimentosStatus(t, h.request(t, route.method, route.path, h.admin, investimentosRouteForm(), false, false), 403)
			}
		})
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("denials mutated investments: count=%d err=%v", count, err)
	}
}

func TestInvestimentosRoutesCRUDAndTenantAuthority(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	form := investimentosRouteForm()
	for key, value := range map[string]string{"workspace_id": "workspace-b", "created_by": "regular-user", "status": "RESGATADA", "archived_at": "1", "updated_at": "0"} {
		form.Set(key, value)
	}
	rr := h.request(t, "POST", "/investimentos", h.admin, form, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Refresh") != "true" {
		t.Fatal("missing HX-Refresh")
	}
	invs, err := services.ListInvestments(context.Background(), h.db, "workspace-rbac", services.InvestmentListAll)
	if err != nil || len(invs) != 1 {
		t.Fatalf("create: %v %+v", err, invs)
	}
	inv := invs[0]
	if inv.WorkspaceID != "workspace-rbac" || inv.CreatedBy != "admin-user" || inv.Status != "ATIVA" || inv.ArchivedAt != nil {
		t.Fatalf("forged authority: %+v", inv)
	}
	for _, account := range []string{"checking-a", "archived-a", "inv-b", "missing"} {
		t.Run(account, func(t *testing.T) {
			invalid := investimentosRouteForm()
			invalid.Set("account_id", account)
			requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, invalid, true, false), 400)
		})
	}
	b := h.session(t, "admin-user", "workspace-b")
	bForm := investimentosRouteForm()
	bForm.Set("account_id", "inv-b")
	bForm.Set("name", "Lote exclusivo B")
	rr = h.request(t, "POST", "/investimentos", b, bForm, true, false)
	requireInvestimentosStatus(t, rr, 303)
	if rr.Header().Get("Location") != "/investimentos" {
		t.Fatal("invalid redirect")
	}
	for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"POST", "/editar"}} {
		foreign := h.request(t, route.method, "/investimentos/"+inv.ID+route.suffix, b, investimentosRouteForm(), true, false)
		missing := h.request(t, route.method, "/investimentos/absent"+route.suffix, b, investimentosRouteForm(), true, false)
		requireInvestimentosStatus(t, foreign, 404)
		requireInvestimentosStatus(t, missing, 404)
		if foreign.Body.String() != missing.Body.String() {
			t.Fatal("foreign ID disclosure")
		}
	}
	for _, session := range []string{h.admin, b} {
		rr = h.request(t, "GET", "/investimentos", session, nil, false, false)
		requireInvestimentosStatus(t, rr, 200)
		if session == h.admin && strings.Contains(rr.Body.String(), "Lote exclusivo B") {
			t.Fatal("B leaked into A")
		}
		if session == b && strings.Contains(rr.Body.String(), "Lote HTTP A") {
			t.Fatal("A leaked into B")
		}
	}
	h.exec(t, `UPDATE investments SET updated_at=100 WHERE id=?`, inv.ID)
	edit := investimentosRouteForm()
	for key, value := range map[string]string{"name": "Editado", "account_id": "inv-b", "principal_cents": "999999", "applied_date": "2020-01-01", "status": "RESGATADA", "redeemed_date": "2026-05-01", "redeemed_by": "regular-user", "archived_at": "123", "workspace_id": "workspace-b", "created_by": "regular-user", "updated_at": "0", "tax_regime": "ISENTO_PF", "maturity_date": "2027-01-15"} {
		edit.Set(key, value)
	}
	before := time.Now().Unix()
	rr = h.request(t, "POST", "/investimentos/"+inv.ID+"/editar?updated_at=1", h.admin, edit, true, false)
	requireInvestimentosStatus(t, rr, 303)
	if rr.Header().Get("Location") != "/investimentos" {
		t.Fatal("invalid edit redirect")
	}
	updated, err := services.GetInvestment(context.Background(), h.db, "workspace-rbac", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Editado" || updated.UpdatedAt <= 100 || updated.UpdatedAt < before || updated.UpdatedAt > time.Now().Unix() {
		t.Fatalf("edit timestamp: %+v", updated)
	}
	if updated.AccountID != inv.AccountID || updated.PrincipalCents != inv.PrincipalCents || updated.AppliedDate != inv.AppliedDate || updated.Status != "ATIVA" || updated.RedeemedBy != nil || updated.RedeemedDate != nil || updated.ArchivedAt != nil || updated.CreatedBy != inv.CreatedBy || updated.WorkspaceID != inv.WorkspaceID {
		t.Fatalf("immutable fields changed: %+v", updated)
	}
	rr = h.request(t, "POST", "/investimentos/"+inv.ID+"/editar", h.admin, edit, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Refresh") != "true" {
		t.Fatal("edit missing HX-Refresh")
	}
	h.exec(t, `UPDATE investments SET status='RESGATADA', redeemed_date='2026-06-01', redeemed_by='admin-user' WHERE id=?`, inv.ID)
	edit.Set("name", "Forbidden change")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+inv.ID+"/editar", h.admin, edit, true, false), 409)
	unchanged, err := services.GetInvestment(context.Background(), h.db, "workspace-rbac", inv.ID)
	if err != nil || unchanged.Name != "Editado" {
		t.Fatalf("redeemed edited: %+v %v", unchanged, err)
	}
	var txCount, balance int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&txCount); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT SUM(current_balance) FROM accounts`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if txCount != 0 || balance != 0 {
		t.Fatalf("ledger mutated: tx=%d balance=%d", txCount, balance)
	}
}

func TestInvestimentosRoutesFiltersAndRealTemplates(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	active := h.create(t, "Active lot")
	redeemed := h.create(t, "Redeemed lot")
	archived := h.create(t, "Archived lot")
	for _, id := range []string{redeemed, archived} {
		h.exec(t, `UPDATE investments SET status='RESGATADA', redeemed_date='2026-06-01', redeemed_by='admin-user' WHERE id=?`, id)
	}
	h.exec(t, `UPDATE investments SET archived_at=200 WHERE id=?`, archived)
	for _, tc := range []struct {
		filter                     string
		active, redeemed, archived bool
	}{
		{"ATIVAS", true, false, false}, {"RESGATADAS", false, true, false}, {"ARQUIVADAS", false, false, true}, {"TODAS", true, true, true}, {"garbage' OR 1=1", true, false, false}, {"", true, false, false},
	} {
		t.Run(tc.filter, func(t *testing.T) {
			rr := h.request(t, "GET", "/investimentos?status="+url.QueryEscape(tc.filter), h.admin, nil, false, false)
			requireInvestimentosStatus(t, rr, 200)
			for name, want := range map[string]bool{"Active lot": tc.active, "Redeemed lot": tc.redeemed, "Archived lot": tc.archived} {
				if strings.Contains(rr.Body.String(), name) != want {
					t.Fatalf("filter=%q name=%q want=%v", tc.filter, name, want)
				}
			}
			for _, bad := range []string{`value="inv-b"`, `value="checking-a"`, `value="archived-a"`} {
				if strings.Contains(rr.Body.String(), bad) {
					t.Fatalf("bad account option %s", bad)
				}
			}
			for _, want := range []string{`hx-post="/investimentos"`, `hx-swap="none"`, `hx-indicator="#htmx-indicator"`, `name="csrf_token" value="`, `name="principal_cents"`, `name="account_id"`} {
				if !strings.Contains(rr.Body.String(), want) {
					t.Fatalf("missing form contract: %s", want)
				}
			}
			// The shared nav is rendered in the desktop sidebar and mobile drawer.
			if strings.Count(rr.Body.String(), `data-v2-nav-section="investments"`) < 2 {
				t.Fatal("desktop/mobile nav missing")
			}
		})
	}
	rr := h.request(t, "GET", "/investimentos/"+active, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if !strings.Contains(rr.Body.String(), "Sem posição registrada") || !strings.Contains(rr.Body.String(), "R$ 1.000,00") {
		t.Fatal("principal fallback missing")
	}
	for _, forbidden := range []string{`name="account_id"`, `name="principal_cents"`, `name="applied_date"`, `name="status"`, `name="archived_at"`, `hx-post="/investimentos/` + active + `/valuation"`} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("edit exposes forbidden field: %s", forbidden)
		}
	}
	h.exec(t, `INSERT INTO investment_valuations (id,workspace_id,investment_id,valuation_date,amount_cents,created_by,created_at) VALUES ('v1','workspace-rbac',?,'2026-02-01',110000,'admin-user',200)`, active)
	rr = h.request(t, "GET", "/investimentos/"+active, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if strings.Contains(rr.Body.String(), "Sem posição registrada") || !strings.Contains(rr.Body.String(), "R$ 1.100,00") || !strings.Contains(rr.Body.String(), "2026-02-01") {
		t.Fatal("valuation not rendered")
	}
	rr = h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if !strings.Contains(rr.Body.String(), "R$ 1.100,00") {
		t.Fatal("list current value not derived")
	}
	for _, tc := range []struct {
		state, id    string
		allowArchive bool
	}{
		{"RESGATADA", redeemed, true},
		{"ARQUIVADA", archived, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			rr := h.request(t, "GET", "/investimentos/"+tc.id, h.admin, nil, false, false)
			requireInvestimentosStatus(t, rr, 200)
			body := rr.Body.String()
			archivePost := `hx-post="/investimentos/` + tc.id + `/arquivar"`
			if strings.Contains(body, archivePost) != tc.allowArchive {
				t.Fatalf("%s archive action: want presence=%v", tc.state, tc.allowArchive)
			}
			for _, action := range []string{"valuations", "editar", "resgatar"} {
				if strings.Contains(body, `hx-post="/investimentos/`+tc.id+`/`+action+`"`) {
					t.Fatalf("%s exposes forbidden investment action %s", tc.state, action)
				}
			}
			for _, action := range []string{"unarchive", "reabrir"} {
				if strings.Contains(body, `/investimentos/`+tc.id+`/`+action) {
					t.Fatalf("%s exposes forbidden lifecycle route %s", tc.state, action)
				}
			}
		})
	}
	// HTML is escaped by the real template engine.
	h.exec(t, `UPDATE investments SET name='<script>alert(1)</script>' WHERE id=?`, active)
	rr = h.request(t, "GET", "/investimentos/"+active, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if strings.Contains(rr.Body.String(), "<script>alert(1)</script>") || !strings.Contains(rr.Body.String(), "&lt;script&gt;") {
		t.Fatal("unescaped name")
	}
	b := h.session(t, "admin-user", "workspace-b")
	rr = h.request(t, "GET", "/investimentos", b, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if !strings.Contains(rr.Body.String(), "Nenhum investimento neste filtro") {
		t.Fatal("empty state missing")
	}
}

func TestInvestimentosRoutesShapesAndMembership(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/investimentos/", 404}, {"GET", "/investimentos/id/extra", 404}, {"POST", "/investimentos/id/valuation", 404},
		{"POST", "/investimentos/id/resgatar/extra", 404}, {"POST", "/investimentos/id/arquivar/extra", 404}, {"GET", "/investimentos/id/editar/extra", 404},
		{"GET", "/investimentos/id/", 404}, {"GET", "/investimentos/id/editar", 405}, {"POST", "/investimentos/id", 405},
		{"PUT", "/investimentos", 405}, {"DELETE", "/investimentos/id", 405}, {"HEAD", "/investimentos", 405},
	} {
		requireInvestimentosStatus(t, h.request(t, tc.method, tc.path, h.admin, investimentosRouteForm(), true, false), tc.status)
	}
	h.exec(t, `UPDATE workspace_members SET role='USER' WHERE user_id='admin-user' AND workspace_id='workspace-b'`)
	if err := h.service.UpdateSessionWorkspace(h.admin, "workspace-b"); err != nil {
		t.Fatal(err)
	}
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos", h.admin, nil, false, false), 403)
	h.exec(t, `DELETE FROM workspace_members WHERE user_id='admin-user' AND workspace_id='workspace-b'`)
	requireInvestimentosStatus(t, h.request(t, "GET", "/investimentos", h.admin, nil, false, false), 303)
}

// Render failures must not flush partially generated HTML or underlying errors.
type investimentosBrokenTemplate struct{}

func (investimentosBrokenTemplate) Lookup(string) *template.Template { return nil }

func (investimentosBrokenTemplate) ExecuteTemplate(w io.Writer, _ string, _ any) error {
	_, _ = io.WriteString(w, "partial-private-content")
	return io.ErrUnexpectedEOF
}

func TestInvestimentosRoutesRebindAndRenderFailure(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	h.base.Templates = investimentosBrokenTemplate{}
	rr := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 500)
	if strings.Contains(rr.Body.String(), "partial-private-content") || strings.Contains(rr.Body.String(), "EOF") {
		t.Fatal("render error leaked")
	}
	// Route closures must read the current base after runtime dependencies change.
	h.base.WriterDB = nil
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", h.admin, investimentosRouteForm(), true, false), 500)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investments`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("nil writer mutated db: %d %v", count, err)
	}
}

func TestInvestimentosRoutesRenderedCSRFAndReadPool(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	// Reads remain available without a writer, including real page rendering.
	writer := h.base.WriterDB
	h.base.WriterDB = nil
	rr := h.request(t, "GET", "/investimentos", h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(rr.Body.String())
	if len(match) != 2 || !h.signer.validate(match[1], time.Now()) {
		t.Fatal("form did not render a signed CSRF token")
	}
	h.base.WriterDB = writer
	form := investimentosRouteForm()
	form.Set("csrf_token", match[1])
	req := httptest.NewRequest("POST", "/investimentos", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.admin})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: match[1]})
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	requireInvestimentosStatus(t, rr, 303)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM investments`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h.base.WriterDB = nil
	rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/editar", h.admin, form, true, false), 500)
}

func investimentosValuationForm() url.Values {
	return url.Values{"valuation_date": {"2026-02-01"}, "amount_cents": {"110000"}}
}

func TestInvestimentosValuationRoutesRBACAndCSRF(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Valuation authorization")
	path := "/investimentos/" + id + "/valuations"
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		for _, hx := range []bool{false, true} {
			requireInvestimentosStatus(t, h.request(t, "POST", path, session, investimentosValuationForm(), true, hx), 403)
		}
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investimentosValuationForm(), false, false), 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_valuations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied requests created %d rows: %v", count, err)
	}
	// A nil writer would produce 500 if the handler were reached. A malformed
	// form would produce 400. Permission denial must precede both paths.
	writer := h.base.WriterDB
	h.base.WriterDB = nil
	for _, user := range []string{"manager-user", "regular-user"} {
		session := h.session(t, user, "workspace-rbac")
		requireInvestimentosStatus(t, h.request(t, "POST", path, session, url.Values{"amount_cents": {"invalid"}}, true, false), 403)
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investimentosValuationForm(), false, false), 403)
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investimentosValuationForm(), true, false), 500)
	h.base.WriterDB = writer
	requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investimentosValuationForm(), true, false), 303)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_valuations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("authorized request: %d %v", count, err)
	}
}

func TestInvestimentosValuationRoutesAuthorityAndTenant(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Valuation A")
	sessionB := h.session(t, "admin-user", "workspace-b")
	formB := investimentosRouteForm()
	formB.Set("account_id", "inv-b")
	formB.Set("name", "Valuation B")
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos", sessionB, formB, true, false), 303)
	var idB string
	if err := h.db.QueryRow(`SELECT id FROM investments WHERE workspace_id='workspace-b'`).Scan(&idB); err != nil {
		t.Fatal(err)
	}
	forged := investimentosValuationForm()
	for key, value := range map[string]string{"investment_id": idB, "workspace_id": "workspace-b", "created_by": "regular-user", "updated_by": "manager-user", "status": "RESGATADA", "archived_at": "1", "redeemed_by": "regular-user"} {
		forged.Set(key, value)
	}
	// Stale base identity is also overwritten by the authenticated request bind.
	h.base.WorkspaceID = "workspace-b"
	h.base.UserID = "regular-user"
	path := "/investimentos/" + id + "/valuations"
	rr := h.request(t, "POST", path, h.admin, forged, true, true)
	requireInvestimentosStatus(t, rr, 200)
	if rr.Header().Get("HX-Refresh") != "true" {
		t.Fatal("HTMX did not refresh detail")
	}
	first, err := services.GetInvestmentValuations(context.Background(), h.db, "workspace-rbac", id)
	if err != nil || len(first) != 1 || first[0].CreatedBy != "admin-user" || first[0].UpdatedBy != nil || first[0].UpdatedAt != nil || first[0].AmountCents != 110000 {
		t.Fatalf("first: %+v %v", first, err)
	}
	forged.Set("amount_cents", "0")
	rr = h.request(t, "POST", path, h.admin, forged, true, false)
	requireInvestimentosStatus(t, rr, 303)
	if rr.Header().Get("Location") != "/investimentos/"+id {
		t.Fatalf("redirect=%s", rr.Header().Get("Location"))
	}
	rows, err := services.GetInvestmentValuations(context.Background(), h.db, "workspace-rbac", id)
	if err != nil || len(rows) != 1 || rows[0].ID != first[0].ID || rows[0].CreatedAt != first[0].CreatedAt || rows[0].CreatedBy != "admin-user" || rows[0].UpdatedBy == nil || *rows[0].UpdatedBy != "admin-user" || rows[0].AmountCents != 0 {
		t.Fatalf("correction authority: %+v %v", rows, err)
	}
	foreign := h.request(t, "POST", "/investimentos/"+idB+"/valuations", h.admin, forged, true, false)
	missing := h.request(t, "POST", "/investimentos/missing/valuations", h.admin, forged, true, false)
	requireInvestimentosStatus(t, foreign, 404)
	requireInvestimentosStatus(t, missing, 404)
	if foreign.Body.String() != missing.Body.String() {
		t.Fatal("foreign investment disclosed")
	}
	requireInvestimentosStatus(t, h.request(t, "POST", path, sessionB, forged, true, false), 404)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_valuations WHERE workspace_id='workspace-b'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cross-tenant write: %d %v", count, err)
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+idB+"/valuations", sessionB, investimentosValuationForm(), true, false), 303)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("valuation wrote ledger: %d %v", count, err)
	}
}

func TestInvestimentosValuationRoutesValidationAndShapes(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Valuation routes")
	base := "/investimentos/" + id
	for _, tc := range []struct {
		method, suffix string
		status         int
	}{
		{"POST", "/valuation", 404}, {"GET", "/valuation", 404},
		{"GET", "/valuations", 405}, {"PUT", "/valuations", 405}, {"DELETE", "/valuations", 405},
		{"POST", "/valuations/extra", 404}, {"GET", "/valuations/extra", 404}, {"POST", "/valuations/", 404},
		{"POST", "/positions", 404}, {"POST", "/resgate", 404}, {"POST", "/archive", 404},
	} {
		t.Run(tc.method+tc.suffix, func(t *testing.T) {
			requireInvestimentosStatus(t, h.request(t, tc.method, base+tc.suffix, h.admin, investimentosValuationForm(), true, false), tc.status)
		})
	}
	requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/valuations", h.admin, investimentosValuationForm(), true, false), 405) // interpreted as a resource ID, not a root write endpoint
	for _, date := range []string{"invalid", "2026-02-30", "2026-01-14"} {
		form := investimentosValuationForm()
		form.Set("valuation_date", date)
		requireInvestimentosStatus(t, h.request(t, "POST", base+"/valuations", h.admin, form, true, false), 400)
	}
	requireInvestimentosStatus(t, h.request(t, "POST", base+"/valuations", h.admin, investimentosValuationForm(), true, false), 303)
	h.exec(t, `UPDATE investments SET status='RESGATADA', redeemed_date='2026-02-01', redeemed_by='admin-user' WHERE id=?`, id)
	requireInvestimentosStatus(t, h.request(t, "POST", base+"/valuations", h.admin, investimentosValuationForm(), true, false), 409)
}

func TestInvestimentosValuationRenderedFormAndHistory(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Position form")
	rr := h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	match := regexp.MustCompile(`(?s)<form[^>]*action="/investimentos/` + id + `/valuations".*?</form>`).FindString(rr.Body.String())
	if match == "" {
		t.Fatal("ATIVA valuation form missing")
	}
	for _, want := range []string{`method="post"`, `hx-post="/investimentos/` + id + `/valuations"`, `hx-swap="none"`, `hx-indicator="#htmx-indicator"`, `name="valuation_date"`, `name="amount_cents"`, `min="0"`, `min="2026-01-15"`} {
		if !strings.Contains(match, want) {
			t.Fatalf("form missing %s", want)
		}
	}
	for _, forbidden := range []string{"workspace_id", "investment_id", "created_by", "updated_by", "status", "archived_at", "redeemed_by"} {
		if strings.Contains(match, `name="`+forbidden+`"`) {
			t.Fatalf("external authority field %s", forbidden)
		}
	}
	token := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(match)
	if len(token) != 2 || !h.signer.validate(token[1], time.Now()) {
		t.Fatal("invalid rendered CSRF")
	}
	// Submit the actual rendered token via cookie + form through withAuth.
	form := investimentosValuationForm()
	form.Set("csrf_token", token[1])
	req := httptest.NewRequest("POST", "/investimentos/"+id+"/valuations", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.admin})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token[1]})
	rr = httptest.NewRecorder()
	h.mux.ServeHTTP(rr, req)
	requireInvestimentosStatus(t, rr, 303)
	rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if strings.Contains(rr.Body.String(), "Sem posição registrada") || !strings.Contains(rr.Body.String(), "R$ 1.100,00") {
		t.Fatal("detail current value not updated")
	}
	h.exec(t, `UPDATE investments SET status='RESGATADA', redeemed_date='2026-02-01', redeemed_by='admin-user' WHERE id=?`, id)
	rr = h.request(t, "GET", "/investimentos/"+id, h.admin, nil, false, false)
	requireInvestimentosStatus(t, rr, 200)
	if strings.Contains(rr.Body.String(), `hx-post="/investimentos/`+id+`/valuations"`) || strings.Contains(rr.Body.String(), `name="amount_cents"`) {
		t.Fatal("RESGATADA has valuation form")
	}
	table := regexp.MustCompile(`(?s)<table.*?</table>`).FindString(rr.Body.String())
	if !strings.Contains(table, "2026-02-01") || !strings.Contains(table, "R$ 1.100,00") || strings.Contains(table, "<input") || strings.Contains(table, "<button") || strings.Contains(table, "hx-post") {
		t.Fatalf("history not read-only: %s", table)
	}
}

func investimentosLifecycleForm() url.Values {
	return url.Values{"redeemed_date": {"2026-02-01"}, "final_amount_cents": {"120000"}}
}

func TestInvestimentosLifecycleRoutesRBACCSRFAndShapes(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Lifecycle guards")
	for _, action := range []string{"resgatar", "arquivar"} {
		t.Run(action, func(t *testing.T) {
			path := "/investimentos/" + id + "/" + action
			normal := h.mux
			blocked := http.NewServeMux()
			registerInvestimentosRoutes(blocked, h.service, h.signer, nil)
			h.mux = blocked
			// nil base proves every denial returns before handler binding/service.
			for _, user := range []string{"manager-user", "regular-user"} {
				session := h.session(t, user, "workspace-rbac")
				for _, hx := range []bool{false, true} {
					rr := h.request(t, "POST", path, session, investimentosLifecycleForm(), true, hx)
					requireInvestimentosStatus(t, rr, 403)
					if hx && rr.Header().Get("HX-Retarget") != "#main-content" {
						t.Fatal("respondForbidden bypassed")
					}
				}
			}
			requireInvestimentosStatus(t, h.request(t, "POST", path, h.admin, investimentosLifecycleForm(), false, false), 403)
			requireInvestimentosStatus(t, h.request(t, "POST", path, "", investimentosLifecycleForm(), true, false), 303)
			for _, method := range []string{"GET", "PUT", "DELETE", "PATCH", "HEAD"} {
				requireInvestimentosStatus(t, h.request(t, method, path, h.admin, investimentosLifecycleForm(), true, false), 405)
			}
			for _, suffix := range []string{"/extra", "/"} {
				requireInvestimentosStatus(t, h.request(t, "POST", path+suffix, h.admin, investimentosLifecycleForm(), true, false), 404)
			}
			h.mux = normal
		})
	}
	for _, action := range []string{"reabrir", "unarchive", "resgate-parcial", "redeem", "archive"} {
		requireInvestimentosStatus(t, h.request(t, "POST", "/investimentos/"+id+"/"+action, h.admin, investimentosLifecycleForm(), true, false), 404)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM investment_valuations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("denials mutated rows=%d %v", n, err)
	}
	inv, err := services.GetInvestment(context.Background(), h.db, "workspace-rbac", id)
	if err != nil || inv.Status != "ATIVA" || inv.ArchivedAt != nil {
		t.Fatalf("denials changed lifecycle=%+v %v", inv, err)
	}
}

func TestInvestimentosLifecycleRoutesAuthorityAndOutcomes(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	sessionB := h.session(t, "admin-user", "workspace-b")
	foreign, err := services.CreateInvestment(context.Background(), h.db, services.CreateInvestmentInput{WorkspaceID: "workspace-b", AccountID: "inv-b", CreatedBy: "admin-user", Name: "Foreign", Kind: "CDB", TaxRegime: "MANUAL", PrincipalCents: 1, AppliedDate: "2026-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	for _, hx := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "htmx"}[hx], func(t *testing.T) {
			id := h.create(t, "Lifecycle "+map[bool]string{false: "normal", true: "htmx"}[hx])
			base := "/investimentos/" + id
			form := investimentosLifecycleForm()
			form.Set("final_amount_cents", "0")
			for key, value := range map[string]string{"workspace_id": "workspace-b", "investment_id": foreign.ID, "user_id": "regular-user", "redeemed_by": "regular-user", "created_by": "regular-user", "updated_by": "regular-user", "status": "ATIVA", "archived_at": "1"} {
				form.Set(key, value)
			}
			h.base.WorkspaceID = "workspace-b"
			h.base.UserID = "regular-user"
			requireInvestimentosStatus(t, h.request(t, "POST", base+"/arquivar", h.admin, form, true, hx), 409)
			for _, action := range []string{"resgatar", "arquivar"} {
				foreignRR := h.request(t, "POST", "/investimentos/"+foreign.ID+"/"+action, h.admin, form, true, hx)
				missingRR := h.request(t, "POST", "/investimentos/missing/"+action, h.admin, form, true, hx)
				requireInvestimentosStatus(t, foreignRR, 404)
				requireInvestimentosStatus(t, missingRR, 404)
				if foreignRR.Body.String() != missingRR.Body.String() {
					t.Fatal("tenant existence leaked")
				}
				requireInvestimentosStatus(t, h.request(t, "POST", base+"/"+action, sessionB, form, true, hx), 404)
			}
			for _, date := range []string{"invalid", "2026-02-30", "2026-01-14"} {
				bad := investimentosLifecycleForm()
				bad.Set("redeemed_date", date)
				requireInvestimentosStatus(t, h.request(t, "POST", base+"/resgatar", h.admin, bad, true, hx), 400)
			}
			rr := h.request(t, "POST", base+"/resgatar", h.admin, form, true, hx)
			want := 303
			if hx {
				want = 200
			}
			requireInvestimentosStatus(t, rr, want)
			if hx {
				if rr.Header().Get("HX-Refresh") != "true" {
					t.Fatal("missing refresh")
				}
			} else if rr.Header().Get("Location") != base {
				t.Fatal("wrong detail redirect")
			}
			inv, err := services.GetInvestment(context.Background(), h.db, "workspace-rbac", id)
			if err != nil || inv.Status != "RESGATADA" || inv.RedeemedDate == nil || *inv.RedeemedDate != "2026-02-01" || inv.RedeemedBy == nil || *inv.RedeemedBy != "admin-user" || inv.ArchivedAt != nil {
				t.Fatalf("identity=%+v %v", inv, err)
			}
			vals, err := services.GetInvestmentValuations(context.Background(), h.db, "workspace-rbac", id)
			if err != nil || len(vals) != 1 || vals[0].AmountCents != 0 || vals[0].CreatedBy != "admin-user" {
				t.Fatalf("final=%+v %v", vals, err)
			}
			requireInvestimentosStatus(t, h.request(t, "POST", base+"/resgatar", h.admin, form, true, hx), 409)
			rr = h.request(t, "POST", base+"/arquivar", h.admin, form, true, hx)
			requireInvestimentosStatus(t, rr, want)
			if hx {
				if rr.Header().Get("HX-Refresh") != "true" {
					t.Fatal("missing archive refresh")
				}
			} else if rr.Header().Get("Location") != "/investimentos" {
				t.Fatal("wrong list redirect")
			}
			requireInvestimentosStatus(t, h.request(t, "POST", base+"/arquivar", h.admin, form, true, hx), 409)
			after, err := services.GetInvestmentValuations(context.Background(), h.db, "workspace-rbac", id)
			if err != nil || !reflect.DeepEqual(vals, after) {
				t.Fatal("archive changed valuations")
			}
		})
	}
	untouched, err := services.GetInvestment(context.Background(), h.db, "workspace-b", foreign.ID)
	if err != nil || untouched.Status != "ATIVA" || untouched.ArchivedAt != nil {
		t.Fatal("foreign lot mutated")
	}
}

func TestInvestimentosLifecycleRenderedFormsHistoryAndFilters(t *testing.T) {
	h := newInvestimentosRoutesHarness(t)
	id := h.create(t, "Lifecycle render")
	base := "/investimentos/" + id
	history := ""
	for _, state := range []string{"ATIVA", "RESGATADA", "ARQUIVADA"} {
		rr := h.request(t, "GET", base, h.admin, nil, false, false)
		requireInvestimentosStatus(t, rr, 200)
		body := rr.Body.String()
		for _, action := range []string{"editar", "valuations", "resgatar", "arquivar"} {
			want := (state == "ATIVA" && action != "arquivar") || (state == "RESGATADA" && action == "arquivar")
			if strings.Contains(body, `action="`+base+`/`+action+`"`) != want {
				t.Fatalf("state %s action %s visibility", state, action)
			}
		}
		for _, action := range []string{"reabrir", "unarchive", "resgate-parcial"} {
			if strings.Contains(body, `/`+action+`"`) {
				t.Fatal("forbidden action")
			}
		}
		if state != "ATIVA" {
			if !strings.Contains(body, "RESGATADA") || !strings.Contains(body, "R$ 1.200,00") {
				t.Fatal("redeemed detail missing")
			}
			table := regexp.MustCompile(`(?s)<table.*?</table>`).FindString(body)
			if !strings.Contains(table, "2026-02-01") || !strings.Contains(table, "R$ 1.200,00") {
				t.Fatal("final history missing")
			}
			if state == "RESGATADA" {
				history = table
			} else if table != history {
				t.Fatal("archive changed rendered history")
			}
		}
		if state == "ARQUIVADA" {
			continue
		}
		action := "resgatar"
		if state == "RESGATADA" {
			action = "arquivar"
		}
		formHTML := regexp.MustCompile(`(?s)<form[^>]*action="` + base + `/` + action + `".*?</form>`).FindString(body)
		for _, want := range []string{`method="post"`, `hx-post="` + base + `/` + action + `"`, `hx-swap="none"`, `hx-indicator="#htmx-indicator"`} {
			if !strings.Contains(formHTML, want) {
				t.Fatalf("form missing %s", want)
			}
		}
		names := regexp.MustCompile(`name="([^"]+)"`).FindAllStringSubmatch(formHTML, -1)
		allowed := map[string]bool{"csrf_token": true}
		if action == "resgatar" {
			allowed["redeemed_date"] = true
			allowed["final_amount_cents"] = true
		}
		if len(names) != len(allowed) {
			t.Fatalf("fields=%v", names)
		}
		for _, name := range names {
			if !allowed[name[1]] {
				t.Fatalf("external authority %s", name[1])
			}
		}
		token := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(formHTML)
		if len(token) != 2 || !h.signer.validate(token[1], time.Now()) {
			t.Fatal("rendered CSRF invalid")
		}
		form := investimentosLifecycleForm()
		form.Set("csrf_token", token[1])
		req := httptest.NewRequest("POST", base+"/"+action, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.admin})
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token[1]})
		rr = httptest.NewRecorder()
		h.mux.ServeHTTP(rr, req)
		requireInvestimentosStatus(t, rr, 303)
	}
	for _, filter := range []string{"ATIVAS", "RESGATADAS", "ARQUIVADAS", "TODAS"} {
		rr := h.request(t, "GET", "/investimentos?status="+filter, h.admin, nil, false, false)
		requireInvestimentosStatus(t, rr, 200)
		want := filter == "ARQUIVADAS" || filter == "TODAS"
		if strings.Contains(rr.Body.String(), `href="`+base+`"`) != want {
			t.Fatalf("filter %s visibility", filter)
		}
	}
}
