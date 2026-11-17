package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R1.3-D3-B1: o refresh do Dashboard é um GET autenticado. Uma sessão expirada
// ou um papel sem dashboard_read não pode devolver um fragmento que substitua o
// grid existente.
func TestDashboardRefreshUnauthorizedDoesNotReturnSwappableDashboard(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?fluxo=12m", nil)
	req.Header.Set("HX-Request", "true")

	respondUnauthorized(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if got := rr.Header().Get("HX-Redirect"); got != "/login" {
		t.Fatalf("HX-Redirect = %q, want /login", got)
	}
	if strings.Contains(rr.Body.String(), `id="dashboard-content"`) {
		t.Fatalf("unauthorized response leaked dashboard markup: %q", rr.Body.String())
	}
}

func TestDashboardRefreshForbiddenKeepsDashboardOutsideSwapTarget(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?fluxo=12m", nil)
	req.Header.Set("HX-Request", "true")

	respondForbidden(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
	if got := rr.Header().Get("HX-Retarget"); got != "#main-content" {
		t.Fatalf("HX-Retarget = %q, want #main-content", got)
	}
	body := rr.Body.String()
	if strings.Contains(body, `id="dashboard-content"`) {
		t.Fatalf("forbidden response leaked dashboard markup: %q", body)
	}
	if !strings.Contains(body, "Acesso negado") {
		t.Fatalf("forbidden response missing the access-denied notice: %q", body)
	}
}
