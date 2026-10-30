package handlers

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestContasPageUsesGlobalAppShell(t *testing.T) {
	tpl := template.Must(template.New("contas-app-shell").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	})).ParseFiles(
		resolveTemplatePath(t, "templates/pages/contas.html"),
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
		resolveTemplatePath(t, "templates/components/seletor_meses.html"),
	))

	data := ContasData{
		Title:               "Contas",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		Aba:                 "pagar",
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contas-page", data); err != nil {
		t.Fatalf("render contas page: %v", err)
	}

	body := out.String()
	for _, expected := range []string{
		`data-app-shell`,
		`data-page-section="accounts"`,
		`data-v2-nav-section="accounts"`,
		`cb-v2-contas-header`,
		`id="payment-confirm-modal"`,
		`/assets/css/contas.css?v=test`,
		`/assets/js/payment-confirm.js?v=test`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("contas page missing %q", expected)
		}
	}
	if strings.Contains(body, `{{template "layout-start"`) {
		t.Fatal("contas page must use the global app shell, not the legacy layout")
	}
}

func TestContasMonthOptionsAreContinuousAroundSelection(t *testing.T) {
	selected := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	options := buildContasMonthOptionsAt(selected, "aluguel", selected)

	if len(options) != 12 {
		t.Fatalf("month options = %d, want 12", len(options))
	}
	if options[0].Label != "Abr" || options[0].Year != "2026" {
		t.Fatalf("first rolling option = %+v, want Abr/2026", options[0])
	}
	if !options[5].IsActive || options[5].Label != "Set" {
		t.Fatalf("selected month must remain in the sixth position: %+v", options[5])
	}
	if options[11].Label != "Mar" || options[11].Year != "2027" || !strings.Contains(options[11].Query, "periodo=2027-03") || !strings.Contains(options[11].Query, "q=aluguel") {
		t.Fatalf("cross-year option lost the Contas query contract: %+v", options[11])
	}
}

func TestContasListPartialRendersWithMinimalData(t *testing.T) {
	tplBytes, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/contas.html"))
	if err != nil {
		t.Fatalf("read contas template: %v", err)
	}
	tpl := template.Must(template.New("contas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(tplBytes)))

	pay := PendingItemRow{
		ID: "pay-1", Description: "Aluguel", Amount: 150000,
		AmountMoney: MoneyMinor(150000), DueDate: "10/07/2026",
		AccountName: "Nubank", ContactName: "Imobiliária",
		AccountColor: "#820AD1", ProviderMark: "NU",
		ProviderLogoPath: "institutions/nubank.png",
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
				`ui-tab is-active`,
				`data-payment-confirm`,
				`data-payment-id="pay-1"`,
				`data-swipe-right-payment-confirm="true"`,
				`ui-row__account-logo`,
				`src="institutions/nubank.png"`,
			},
			notWant: []string{`id="tx-rec-1"`, `hx-push-url="/contas?aba=`, `data-swipe-right-url="/transacoes/pay-1/status-pagamento"`},
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
				`data-payment-confirm`,
				`data-payment-id="rec-1"`,
				`data-swipe-right-payment-confirm="true"`,
			},
			notWant: []string{`id="tx-pay-1"`, `hx-push-url="/contas?aba=`, `data-swipe-right-url="/transacoes/rec-1/status-pagamento"`},
		},
		{
			name: "aba=pagar empty list shows empty state",
			data: ContasData{Aba: "pagar"},
			want: []string{
				`id="contas-list"`,
				`Sem contas pendentes`,
			},
			notWant: []string{`id="tx-`, `Sem recebíveis pendentes`},
		},
		{
			name: "invoice row keeps the card link and provider identity",
			data: ContasData{Aba: "pagar", Payables: []PendingItemRow{{
				ID: "inv-1", IsInvoice: true, Description: "Fatura Itaú - 2026-10",
				Amount: 235000, AmountMoney: MoneyMinor(235000), DueDate: "05/11/2026",
				AccountName: "Itaú", AccountColor: "#EC7000", ProviderMark: "ITA",
				ProviderLogoPath: "institutions/itau.png",
			}}},
			want: []string{
				`id="invoice-inv-1"`,
				`href="/cartoes/faturas/inv-1"`,
				`src="institutions/itau.png"`,
				`ui-badge--warning`,
				`Fatura`,
			},
			notWant: []string{`data-payment-confirm`, `data-swipe-row="true"`},
		},
		{
			name: "aba=receber empty list shows empty state",
			data: ContasData{Aba: "receber"},
			want: []string{
				`id="contas-list"`,
				`Sem recebíveis pendentes`,
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

func TestPaymentConfirmModalIsAvailableFromAppShell(t *testing.T) {
	layoutBytes, err := os.ReadFile(filepath.Join(projectRoot(), "templates/components/app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell template: %v", err)
	}
	layout := string(layoutBytes)
	for _, want := range []string{
		`id="payment-confirm-modal"`,
		`Confirmar pagamento`,
		`id="payment-confirm-date"`,
		`/assets/js/payment-confirm.js`,
	} {
		if !strings.Contains(layout, want) {
			t.Fatalf("app shell missing payment modal contract %q", want)
		}
	}
}

func TestContasBodyRendersWithSummaryAndRefresh(t *testing.T) {
	contasBytes, err := os.ReadFile(filepath.Join(projectRoot(), "templates/pages/contas.html"))
	if err != nil {
		t.Fatalf("read contas template: %v", err)
	}
	stub := `{{define "seletor_meses"}}<div id="monthSelector">seletor</div>{{end}}`
	tpl := template.Must(template.New("contas").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).Parse(string(contasBytes) + "\n" + stub))

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

func TestContasOverdueTotalsIgnoreScheduledRows(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")

	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	insertScheduledPaymentTestTransaction(t, db, "tx-contas-scheduled-income", models.TransactionTypeIncome, "checking-test", "", "pending", nil)
	execTestSQL(t, db, `UPDATE transactions
		SET scheduled_payment_at = ?, scheduled_payment_created_by = 'user-test'
		WHERE id = 'tx-contas-scheduled-income'`, testAppCivilDate("2026-07-10"))

	overdue, err := queryPendingItemsFiltered(db, "ws-test", models.TransactionTypeIncome, true, 7, 2026, "")
	if err != nil {
		t.Fatalf("query overdue receivables: %v", err)
	}
	if len(overdue) != 0 {
		t.Fatalf("overdue receivables = %#v, want none for scheduled row", overdue)
	}

	items, err := queryPendingItemsFiltered(db, "ws-test", models.TransactionTypeIncome, false, 7, 2026, "")
	if err != nil {
		t.Fatalf("query receivables: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("receivables = %#v, want one scheduled row", items)
	}
	if !items[0].IsScheduled || items[0].IsOverdue {
		t.Fatalf("scheduled row flags = scheduled:%v overdue:%v", items[0].IsScheduled, items[0].IsOverdue)
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
