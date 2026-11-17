package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardMaterializationOnlyRunsOnFullPageLoad(t *testing.T) {
	full := httptest.NewRequest(http.MethodGet, "/", nil)
	if !shouldScheduleDashboardMaterialization(full) {
		t.Fatal("full dashboard load must schedule materialization")
	}

	refresh := httptest.NewRequest(http.MethodGet, "/?fluxo=12m", nil)
	refresh.Header.Set("HX-Request", "true")
	if shouldScheduleDashboardMaterialization(refresh) {
		t.Fatal("HTMX refresh must not schedule materialization again")
	}
	for _, method := range []string{http.MethodPost, http.MethodHead, http.MethodPut, http.MethodDelete} {
		if shouldScheduleDashboardMaterialization(httptest.NewRequest(method, "/", nil)) {
			t.Fatalf("rejected method %s must not schedule materialization", method)
		}
	}

	if !shouldScheduleDashboardMaterialization(nil) {
		t.Fatal("nil request must keep the safe default of scheduling")
	}
}
