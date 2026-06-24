package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContasListPartialRendersWithMinimalData(t *testing.T) {
	tplBytes, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/contas.html"))
	if err != nil {
		t.Fatalf("read contas template: %v", err)
	}
	tpl := template.Must(template.New("contas").Parse(string(tplBytes)))

	pay := PendingItemRow{
		ID: "pay-1", Description: "Aluguel", Amount: 150000,
		AmountMoney: MoneyMinor(150000), DueDate: "10/07/2026",
		AccountName: "Nubank", ContactName: "Imobiliária",
	}
	rec := PendingItemRow{
		ID: "rec-1", Description: "Fatura cliente", Amount: 300000,
		AmountMoney: MoneyMinor(300000), DueDate: "15/07/2026",
		AccountName: "PJ", ContactName: "ACME",
	}

	tests := []struct {
		name    string
		data    ContasData
		want    []string
		notWant []string
	}{
		{
			name: "aba=pagar shows only payables",
			data: ContasData{Aba: "pagar", Payables: []PendingItemRow{pay}},
			want: []string{
				`id="contas-list"`,
				`value="pagar"`,
				`id="tx-pay-1"`,
				`R$ 1.500`,
				`Aluguel`,
				`cb-tab-active`,
				`Marcar como pago?`,
				`hx-post="/transacoes/pay-1/status-pagamento"`,
			},
			notWant: []string{`id="tx-rec-1"`, `Marcar como recebido?`, `hx-push-url="/contas?aba=`},
		},
		{
			name: "aba=receber shows only receivables",
			data: ContasData{Aba: "receber", Receivables: []PendingItemRow{rec}},
			want: []string{
				`id="contas-list"`,
				`value="receber"`,
				`id="tx-rec-1"`,
				`R$ 3.000`,
				`Fatura cliente`,
				`Marcar como recebido?`,
				`hx-post="/transacoes/rec-1/status-pagamento"`,
			},
			notWant: []string{`id="tx-pay-1"`, `Marcar como pago?`, `hx-push-url="/contas?aba=`},
		},
		{
			name: "aba=pagar empty list shows empty state",
			data: ContasData{Aba: "pagar"},
			want: []string{
				`id="contas-list"`,
				`Sem contas pendentes`,
			},
			notWant: []string{`id="tx-`, `Sem recebiveis pendentes`},
		},
		{
			name: "aba=receber empty list shows empty state",
			data: ContasData{Aba: "receber"},
			want: []string{
				`id="contas-list"`,
				`Sem recebiveis pendentes`,
			},
			notWant: []string{`id="tx-`, `Sem contas pendentes`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			if err := tpl.ExecuteTemplate(&buf, "contas-list", tt.data); err != nil {
				t.Fatalf("render contas-list: %v", err)
			}
			html := buf.String()
			for _, w := range tt.want {
				if !strings.Contains(html, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(html, nw) {
					t.Errorf("unexpected %q", nw)
				}
			}
		})
	}
}

func TestContasBodyRendersWithSummaryAndRefresh(t *testing.T) {
	contasBytes, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/contas.html"))
	if err != nil {
		t.Fatalf("read contas template: %v", err)
	}
	stub := `{{define "seletor_meses"}}<div id="monthSelector">seletor</div>{{end}}`
	tpl := template.Must(template.New("contas").Parse(string(contasBytes) + "\n" + stub))

	data := ContasData{
		Aba:                   "pagar",
		PeriodoRef:            "2026-07",
		TotalPayables:         MoneyMinor(150000),
		TotalReceivables:      MoneyMinor(300000),
		TotalOverdueReceivabs: MoneyMinor(50000),
		Payables: []PendingItemRow{{
			ID: "p1", Description: "Aluguel", AmountMoney: MoneyMinor(150000),
			DueDate: "10/07/2026", AccountName: "Nubank",
		}},
	}

	var buf strings.Builder
	if err := tpl.ExecuteTemplate(&buf, "contas-body", data); err != nil {
		t.Fatalf("render contas-body: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		`id="contas-body"`,
		`refreshFinancials`,
		`hx-get="/contas?partial=content"`,
		`hx-disinherit="*"`,
		`id="contasFilters"`,
		`id="contas-list"`,
		`id="tx-p1"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("contas-body missing %q", want)
		}
	}
	if strings.Contains(html, `name="partial" value="lista"`) {
		t.Error("contasFilters should not contain partial=lista hidden input")
	}
	if !strings.Contains(html, `"partial":"lista"`) {
		t.Error("search input missing hx-vals partial=lista")
	}
	if strings.Contains(html, `"partial":"content"`) {
		t.Error("contas-body should not use hx-vals for partial=content (causes inheritance duplication)")
	}
}

func testContasBranchTemplates(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("contas-branches").Parse(`
{{define "contas-page"}}<div data-template="page">{{template "contas-body" .}}</div>{{end}}
{{define "contas-body"}}<div id="contas-body" data-template="body">body</div>{{end}}
{{define "contas-list"}}<div id="contas-list" data-template="list">list</div>{{end}}
`))
}

func TestContasPartialContentHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	handler := ContasHandler{
		DB:          db,
		Templates:   testContasBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&partial=content", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)

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
	if !strings.Contains(replaceUrl, "periodo=2026-07") {
		t.Errorf("HX-Replace-Url %q should contain periodo=2026-07", replaceUrl)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-template="body"`) {
		t.Error("should render contas-body template")
	}
	if strings.Contains(body, `data-template="page"`) {
		t.Error("should not render page wrapper in partial=content")
	}
}

func TestContasFullRenderNoHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	handler := ContasHandler{
		DB:          db,
		Templates:   testContasBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if rr.Header().Get("HX-Replace-Url") != "" {
		t.Errorf("full render should not set HX-Replace-Url, got %q", rr.Header().Get("HX-Replace-Url"))
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-template="page"`) {
		t.Error("should render page wrapper")
	}
}

func TestContasPartialListaStillWorks(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	handler := ContasHandler{
		DB:          db,
		Templates:   testContasBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/contas?periodo=2026-07&partial=lista&aba=pagar", nil)
	rr := httptest.NewRecorder()
	handler.HandleContasConceito(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	replaceUrl := rr.Header().Get("HX-Replace-Url")
	if replaceUrl == "" {
		t.Fatal("partial=lista should set HX-Replace-Url to preserve periodo")
	}
	if strings.Contains(replaceUrl, "partial=") {
		t.Errorf("HX-Replace-Url %q should not contain partial=", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "periodo=2026-07") {
		t.Errorf("HX-Replace-Url %q should contain periodo=2026-07", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "aba=pagar") {
		t.Errorf("HX-Replace-Url %q should contain aba=pagar", replaceUrl)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-template="list"`) {
		t.Error("should render contas-list template")
	}
	if strings.Contains(body, `data-template="body"`) {
		t.Error("should not render contas-body for partial=lista")
	}
}

func TestIsHXFromContas(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://localhost:8080/contas", true},
		{"http://localhost:8080/contas?periodo=2026-07", true},
		{"http://localhost:8080/contas?periodo=2026-07&aba=pagar", true},
		{"/contas", true},
		{"/contas?periodo=2026-07", true},
		{"http://localhost:8080/lancamentos", false},
		{"http://localhost:8080/lancamentos?mes=7", false},
		{"http://localhost:8080/configuracoes/contas", false},
		{"http://localhost:8080/", false},
		{"", false},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/dummy", nil)
		if tt.url != "" {
			req.Header.Set("HX-Current-URL", tt.url)
		}
		if got := isHXFromContas(req); got != tt.want {
			t.Errorf("isHXFromContas(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestRespondContasRefreshAndCloseSheet(t *testing.T) {
	rr := httptest.NewRecorder()
	respondContasRefreshAndCloseSheet(rr)

	if rr.Header().Get("HX-Reswap") != "none" {
		t.Errorf("HX-Reswap = %q, want none", rr.Header().Get("HX-Reswap"))
	}
	if rr.Header().Get("HX-Trigger") != "refreshFinancials" {
		t.Errorf("HX-Trigger = %q, want refreshFinancials", rr.Header().Get("HX-Trigger"))
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="bottom-sheet-container"`) {
		t.Error("missing bottom-sheet OOB close")
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Error("missing OOB swap attribute")
	}
}
