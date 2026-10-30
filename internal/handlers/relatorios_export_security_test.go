package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

func TestRelatoriosCSVRejectsUnknownTypeBeforeHeaders(t *testing.T) {
	h := &RelatoriosHandler{ActorRole: models.RoleAdmin}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/relatorios/export.csv?tipo=evil%0D%0AX-Test:1", nil)

	h.HandleExportarCSV(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if got := rr.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("invalid type must not set Content-Disposition, got %q", got)
	}
	if !strings.Contains(rr.Body.String(), "tipo de exportação inválido") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}
