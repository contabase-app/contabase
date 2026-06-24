package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testRelatoriosBranchTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("relatorios-branches").Parse(`
{{define "relatorios-page"}}<div data-template="page">{{template "relatorios-body" .}}</div>{{end}}
{{define "relatorios-body"}}<div id="relatorios-body" data-template="body">body-content</div>{{end}}
`))
}

func TestRelatoriosRenderBranches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		Templates:   testRelatoriosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	tests := []struct {
		name         string
		url          string
		hxRequest    bool
		wantStatus   int
		wantFragment string
		wantMissing  string
	}{
		{
			name:         "full page render",
			url:          "/relatorios?mes=8&ano=2026",
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="page"`,
		},
		{
			name:         "htmx partial renders body only",
			url:          "/relatorios?mes=8&ano=2026&partial=content",
			hxRequest:    true,
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="body"`,
			wantMissing:  `data-template="page"`,
		},
		{
			name:         "htmx navigation without partial renders full page",
			url:          "/relatorios?mes=8&ano=2026",
			hxRequest:    true,
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="page"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.hxRequest {
				req.Header.Set("HX-Request", "true")
			}
			rr := httptest.NewRecorder()
			handler.HandleRelatoriosConceito(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			body := rr.Body.String()
			if !strings.Contains(body, tc.wantFragment) {
				t.Fatalf("body missing %q\nbody:\n%s", tc.wantFragment, body)
			}
			if tc.wantMissing != "" && strings.Contains(body, tc.wantMissing) {
				t.Fatalf("body should not contain %q\nbody:\n%s", tc.wantMissing, body)
			}
		})
	}
}

func TestRelatoriosPartialHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		Templates:   testRelatoriosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/relatorios?mes=7&ano=2026&partial=content", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleRelatoriosConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	replaceUrl := rr.Header().Get("HX-Replace-Url")
	if replaceUrl == "" {
		t.Fatal("missing HX-Replace-Url header")
	}
	if strings.Contains(replaceUrl, "partial=") {
		t.Errorf("HX-Replace-Url %q should not contain partial=", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "mes=7") {
		t.Errorf("HX-Replace-Url %q should contain mes=7", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "ano=2026") {
		t.Errorf("HX-Replace-Url %q should contain ano=2026", replaceUrl)
	}
}

func TestRelatoriosFullRenderNoHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		Templates:   testRelatoriosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/relatorios?mes=7&ano=2026", nil)
	rr := httptest.NewRecorder()
	handler.HandleRelatoriosConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Header().Get("HX-Replace-Url") != "" {
		t.Errorf("full render should not set HX-Replace-Url, got %q", rr.Header().Get("HX-Replace-Url"))
	}
}

func TestRelatoriosMonthSelectorPartialContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := RelatoriosHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	if data.MonthSelectorHXTarget != "#relatorios-body" {
		t.Errorf("MonthSelectorHXTarget = %q, want #relatorios-body", data.MonthSelectorHXTarget)
	}
	if data.MonthSelectorHXSelect != "#relatorios-body" {
		t.Errorf("MonthSelectorHXSelect = %q, want #relatorios-body", data.MonthSelectorHXSelect)
	}
	if data.MonthSelectorHXSwap != "outerHTML" {
		t.Errorf("MonthSelectorHXSwap = %q, want outerHTML", data.MonthSelectorHXSwap)
	}
	if data.MonthSelectorPartial != "content" {
		t.Errorf("MonthSelectorPartial = %q, want content", data.MonthSelectorPartial)
	}
	for _, q := range []string{data.MonthSelectorPrevQuery, data.MonthSelectorNextQuery, data.MonthSelectorCurrentQuery} {
		if strings.Contains(q, "partial=") {
			t.Errorf("month query %q should not contain partial=", q)
		}
	}
}
