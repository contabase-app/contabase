package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

func TestDashboardBalanceSeriesRouteRequiresSessionAndScopesWorkspace(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-series-route', 'workspace-rbac', 'Conta Route', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{12}, 32), ttl: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/balance-series", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !hasPermission(ctx.Role, permDashboardRead) {
			respondForbidden(w, r)
			return
		}
		h := &handlers.DashboardBalanceSeriesHandler{
			DB:          db,
			WorkspaceID: ctx.ActiveWorkspaceID,
		}
		h.ServeHTTP(w, r)
	}))

	t.Run("without session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/dashboard/balance-series?period=30d", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want redirect", rec.Code)
		}
		if got := rec.Header().Get("Location"); got != "/login" {
			t.Fatalf("location = %q", got)
		}
	})

	t.Run("admin reads only active workspace", func(t *testing.T) {
		session := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)
		req := httptest.NewRequest(http.MethodGet, "/dashboard/balance-series?period=30d", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
			t.Fatalf("cache control = %q", got)
		}
		if !strings.Contains(rec.Body.String(), `"balance_cents":"100000"`) {
			t.Fatalf("unexpected body: %s", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "workspace-rbac") || strings.Contains(rec.Body.String(), "Conta Route") {
			t.Fatalf("route leaked identity: %s", rec.Body.String())
		}
	})

	t.Run("invalid period", func(t *testing.T) {
		session := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)
		req := httptest.NewRequest(http.MethodGet, "/dashboard/balance-series?period=99d", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", rec.Code)
		}
	})
}
