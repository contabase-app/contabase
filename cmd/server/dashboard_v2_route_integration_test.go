package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
)

type dashboardRouteFixture struct {
	db        *sql.DB
	service   *auth.Service
	signer    *csrfSigner
	route     http.Handler
	scheduled []string
}

func newDashboardRouteFixture(t *testing.T, empty bool) *dashboardRouteFixture {
	t.Helper()
	var db *sql.DB
	if empty {
		var err error
		db, err = openRawServerFixture(t)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		db = openAdminUsersRBACTestDB(t)
	}
	tpl, err := newAppTemplateEngine(false, buildFuncMap())
	if err != nil {
		t.Fatal(err)
	}
	f := &dashboardRouteFixture{db: db, service: auth.NewService(db)}
	mux := http.NewServeMux()
	signer := &csrfSigner{secret: bytes.Repeat([]byte{21}, 32), ttl: time.Hour}
	f.signer = signer
	registerDashboardRoute(mux, newSQLDBProvider(db), tpl, f.service, signer, func(workspaceID string) {
		f.scheduled = append(f.scheduled, workspaceID)
	})
	f.route = mux
	return f
}

func (f *dashboardRouteFixture) session(t *testing.T, user, workspace string) string {
	t.Helper()
	token, _, err := f.service.CreateSession(user, workspace, time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (f *dashboardRouteFixture) get(path, token string, hx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.route.ServeHTTP(rec, req)
	return rec
}

func TestDashboardV2OfficialRouteRequiresSetupAndSession(t *testing.T) {
	t.Run("without users", func(t *testing.T) {
		f := newDashboardRouteFixture(t, true)
		for _, hx := range []bool{false, true} {
			rec := f.get("/", "", hx)
			if hx {
				if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/setup" {
					t.Fatalf("HX setup response = %d, %v", rec.Code, rec.Header())
				}
			} else if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
				t.Fatalf("setup response = %d, %v", rec.Code, rec.Header())
			}
		}
		if len(f.scheduled) != 0 {
			t.Fatal("setup request scheduled materialization")
		}
	})
	t.Run("without session", func(t *testing.T) {
		f := newDashboardRouteFixture(t, false)
		rec := f.get("/", "", false)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Fatalf("login response = %d, %v", rec.Code, rec.Header())
		}
		if len(f.scheduled) != 0 {
			t.Fatal("anonymous request scheduled materialization")
		}
	})
}

func TestDashboardV2OfficialRouteDeniesUnknownRole(t *testing.T) {
	f := newDashboardRouteFixture(t, false)
	// An invalid role exercises the route's deny guard without changing the
	// existing permissions assigned to supported roles.
	if _, err := f.db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE workspace_members SET role = 'UNAUTHORIZED_FIXTURE' WHERE user_id = 'regular-user'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatal(err)
	}
	token := f.session(t, "regular-user", "workspace-rbac")
	for _, hx := range []bool{false, true} {
		rec := f.get("/", token, hx)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if hx && rec.Header().Get("HX-Retarget") != "#main-content" {
			t.Fatal("denied HX response must target main-content")
		}
		if strings.Contains(rec.Body.String(), `id="dashboard-content"`) {
			t.Fatal("denied response returned swappable dashboard")
		}
	}
	if len(f.scheduled) != 0 {
		t.Fatal("denied request scheduled materialization")
	}
}

func TestDashboardV2OfficialRouteScopesActiveWorkspaceAndRetiresPreview(t *testing.T) {
	f := newDashboardRouteFixture(t, false)
	for _, q := range []string{
		`INSERT INTO workspaces (id, name, type) VALUES ('workspace-b', 'Workspace B', 'personal')`,
		`INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ('workspace-b', 'admin-user', 'ADMIN')`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('account-a', 'workspace-rbac', 'ROUTE_ACCOUNT_A', 'CHECKING', 1234500, 1234500)`,
		`INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('account-b', 'workspace-b', 'ROUTE_ACCOUNT_B', 'CHECKING', 9876500, 9876500)`,
	} {
		if _, err := f.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ workspace, own, foreign string }{
		{"workspace-rbac", "ROUTE_ACCOUNT_A", "ROUTE_ACCOUNT_B"},
		{"workspace-b", "ROUTE_ACCOUNT_B", "ROUTE_ACCOUNT_A"},
	} {
		token := f.session(t, "admin-user", tc.workspace)
		for _, hx := range []bool{false, true} {
			rec := f.get("/", token, hx)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body = %s", tc.workspace, rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.own) || strings.Contains(body, tc.foreign) {
				t.Fatalf("%s response crossed workspace boundary", tc.workspace)
			}
			if n := strings.Count(body, `data-cbp-section=`); n != 8 {
				t.Fatalf("card count = %d, want 8", n)
			}
			for _, forbidden := range []string{`class="wallet-heading"`, `id="dashboard-balance"`, `id="dashboard-cards"`, "/preview/dashboard-v2", "Prévia DEV", `name="robots"`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("official page contains obsolete surface %q", forbidden)
				}
			}
			if strings.Contains(body, "is-layout-pending") == hx {
				t.Fatalf("anti-flash gate does not match full page vs HX request: HX=%v", hx)
			}
			if rec.Header().Get("X-Robots-Tag") != "" || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("unexpected official headers: %v", rec.Header())
			}
		}
		rec := f.get("/preview/dashboard-v2", token, false)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("retired preview status = %d, want 404", rec.Code)
		}
	}
	if got := strings.Join(f.scheduled, ","); got != "workspace-rbac,workspace-b" {
		t.Fatalf("materialization scheduled for %q; want only each full-page active workspace", got)
	}
}

func TestDashboardV2OfficialRouteKeepsCanonicalMonthAndFlowURLs(t *testing.T) {
	f := newDashboardRouteFixture(t, false)
	token := f.session(t, "admin-user", "workspace-rbac")
	for _, hx := range []bool{false, true} {
		rec := f.get("/?mes=2026-10&fluxo=12m", token, hx)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		for _, want := range []string{`hx-get="/?fluxo=12m&amp;mes=2026-10"`, `href="/?fluxo=12m&amp;mes=2026-09"`, `href="/?mes=2026-10"`} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Fatalf("month/flow response missing %q", want)
			}
		}
	}
	if got := strings.Join(f.scheduled, ","); got != "workspace-rbac" {
		t.Fatalf("historical materialization scheduled for %q", got)
	}
}

func TestDashboardV2OfficialRouteExpiredSessionHXRedirectsToLogin(t *testing.T) {
	f := newDashboardRouteFixture(t, false)
	token := f.session(t, "admin-user", "workspace-rbac")
	if _, err := f.db.Exec(`UPDATE sessions SET expires_at = unixepoch() - 60`); err != nil {
		t.Fatal(err)
	}
	rec := f.get("/?fluxo=12m", token, true)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login" {
		t.Fatalf("expired HX response = %d, %v", rec.Code, rec.Header())
	}
	if strings.Contains(rec.Body.String(), `id="dashboard-content"`) || len(f.scheduled) != 0 {
		t.Fatal("expired session reached dashboard render/materialization")
	}
}

func TestDashboardV2OfficialRouteRejectedMethodDoesNotMaterialize(t *testing.T) {
	f := newDashboardRouteFixture(t, false)
	token := f.session(t, "admin-user", "workspace-rbac")
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodPut, http.MethodDelete} {
		for _, hx := range []bool{false, true} {
			req := httptest.NewRequest(method, "/", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			addAdminUsersRBACCSRF(t, req, f.signer)
			if hx {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			f.route.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s HX=%v status = %d, want 405", method, hx, rec.Code)
			}
			if strings.Contains(rec.Body.String(), `id="dashboard-content"`) || len(f.scheduled) != 0 {
				t.Fatalf("rejected %s request rendered dashboard or scheduled materialization", method)
			}
		}
	}
}
