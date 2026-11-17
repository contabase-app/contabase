package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReservasRemovalTombstonesRejectEveryMutationMethod(t *testing.T) {
	mux := http.NewServeMux()
	registerReservasRemovalTombstones(mux)

	for _, path := range []string{
		"/metas/caixinha",
		"/metas/caixinha/aporte",
		"/metas/caixinha/resgate",
		"/metas/caixinha/aporte/sheet",
		"/metas/caixinha/resgate/sheet",
		"/metas/caixinha/historico",
		"/metas/caixinha/box-1",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
			if rr.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404", method, path, rr.Code)
			}
		}
	}
}
