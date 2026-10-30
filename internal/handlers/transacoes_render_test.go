package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func TestHandleListarTransacoesRenderBranches(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		Templates:   testLancamentosBranchTemplates(t),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	tests := []struct {
		name         string
		url          string
		hxRequest    bool
		wantStatus   int
		wantFragment string
	}{
		{
			name:         "full page without htmx renders page template",
			url:          "/lancamentos?mes=8&ano=2026",
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="page"`,
		},
		{
			name:         "htmx list partial renders list template",
			url:          "/lancamentos?mes=8&ano=2026&partial=lista",
			hxRequest:    true,
			wantStatus:   http.StatusOK,
			wantFragment: `data-template="list"`,
		},
		{
			name:         "htmx month navigation keeps page template",
			url:          "/lancamentos?mes=8&ano=2026",
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

			handler.HandleListarTransacoes(rr, req)

			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.wantFragment) {
				t.Fatalf("body missing %q\nbody:\n%s", tc.wantFragment, rr.Body.String())
			}
		})
	}
}

func TestBuildLancamentosDataMonthSelectorUsesListPartialContract(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	if data.MonthSelectorHXSelect != "#lancamentos-list-wrapper" {
		t.Fatalf("MonthSelectorHXSelect = %q, want #lancamentos-list-wrapper", data.MonthSelectorHXSelect)
	}
	if data.MonthSelectorHXTarget != "#lancamentos-list-wrapper" {
		t.Fatalf("MonthSelectorHXTarget = %q, want #lancamentos-list-wrapper", data.MonthSelectorHXTarget)
	}
	if data.MonthSelectorPartial != "lista" {
		t.Fatalf("MonthSelectorPartial = %q, want lista", data.MonthSelectorPartial)
	}
	if strings.Contains(data.MonthSelectorPrevQuery, "partial=") {
		t.Fatalf("MonthSelectorPrevQuery = %q, did not expect partial=", data.MonthSelectorPrevQuery)
	}
	if strings.Contains(data.MonthSelectorNextQuery, "partial=") {
		t.Fatalf("MonthSelectorNextQuery = %q, did not expect partial=", data.MonthSelectorNextQuery)
	}
	if strings.Contains(data.MonthSelectorCurrentQuery, "partial=") {
		t.Fatalf("MonthSelectorCurrentQuery = %q, did not expect partial=", data.MonthSelectorCurrentQuery)
	}
	if strings.Contains(data.ClearFiltersURL, "partial=") {
		t.Fatalf("ClearFiltersURL = %q, did not expect partial=", data.ClearFiltersURL)
	}
	if len(data.MonthOptions) == 0 {
		t.Fatalf("MonthOptions = 0, want > 0")
	}
	for _, option := range data.MonthOptions {
		if strings.Contains(option.Query, "partial=") {
			t.Fatalf("month option query = %q, did not expect partial=", option.Query)
		}
	}
}

func TestBuildLancamentosMonthOptionsAreContinuousAroundSelection(t *testing.T) {
	selected := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	options := buildMonthOptionsAt("account-1", selected, LancamentosFilters{Order: "desc"}, selected)

	if len(options) != 12 {
		t.Fatalf("month options = %d, want 12", len(options))
	}
	if options[0].Label != "Abr" || options[0].Year != "2026" {
		t.Fatalf("first rolling option = %+v, want Abr/2026", options[0])
	}
	if options[5].Label != "Set" || !options[5].IsActive {
		t.Fatalf("selected month must remain in the sixth position: %+v", options[5])
	}
	if options[11].Label != "Mar" || options[11].Year != "2027" {
		t.Fatalf("rolling range must continue into next year: %+v", options[11])
	}
	for _, expected := range []string{"mes=3", "ano=2027", "conta=account-1", "ordem=desc"} {
		if !strings.Contains(options[11].Query, expected) {
			t.Fatalf("cross-year option lost %q in query %q", expected, options[11].Query)
		}
	}
}

func TestLancamentosMonthSelectorDoesNotInheritListPartial(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	tpl := template.Must(template.New("lancamentos-actual").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/seletor_meses.html"),
	))

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "lancamentos-content", data); err != nil {
		t.Fatalf("render lancamentos-content: %v", err)
	}
	body := out.String()

	monthRail := strings.Index(body, `id="monthRail"`)
	if monthRail < 0 {
		t.Fatalf("rendered page missing month rail\nbody:\n%s", body)
	}
	formStart := strings.Index(body, `id="lancamentosFilters"`)
	if formStart < 0 {
		t.Fatalf("rendered page missing lancamentos filters form\nbody:\n%s", body)
	}
	formEndOffset := strings.Index(body[formStart:], `</form>`)
	if formEndOffset < 0 {
		t.Fatalf("rendered page missing lancamentos filters form close\nbody:\n%s", body)
	}
	formEnd := formStart + formEndOffset
	if monthRail > formStart && monthRail < formEnd {
		t.Fatalf("month rail rendered inside filters form; it would inherit partial=lista")
	}

	assertContains(t, body, `name="partial" value="lista"`)
	assertContains(t, body, `hx-target="#lancamentos-list-wrapper"`)
	assertContains(t, body, `hx-select="#lancamentos-list-wrapper"`)
	assertContains(t, body, `id="lancamentos-filter-period"`)
}

func TestLancamentosPageUsesGlobalAppShell(t *testing.T) {
	tpl := template.Must(template.New("lancamentos-app-shell").Funcs(withAppShellTestFuncs(template.FuncMap{
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
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
		resolveTemplatePath(t, "templates/components/app_shell.html"),
		resolveTemplatePath(t, "templates/components/seletor_meses.html"),
	))

	data := LancamentosData{
		Title:               "Lançamentos",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Família",
		MonthLabel:          "Agosto de 2026",
	}
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "lancamentos-page", data); err != nil {
		t.Fatalf("render lancamentos page: %v", err)
	}

	body := out.String()
	for _, expected := range []string{
		`data-app-shell`,
		`data-page-section="transactions"`,
		`data-v2-nav-section="transactions"`,
		`cb-v2-lancamentos-header`,
		`aria-label="Adicionar lançamento"`,
		`id="payment-confirm-modal"`,
		`id="global-delete-scope-modal"`,
		`/assets/js/payment-confirm.js?v=test`,
		`/assets/css/lancamentos.css?v=test`,
	} {
		assertContains(t, body, expected)
	}
	if strings.Contains(body, `{{template "layout-start"`) {
		t.Fatal("lancamentos page must use the global app shell, not the legacy layout")
	}
}

func TestLancamentosBulkPaidUsesPaymentDateModalContract(t *testing.T) {
	lancamentosBytes, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/lancamentos.html"))
	if err != nil {
		t.Fatalf("read lancamentos template: %v", err)
	}
	lancamentos := string(lancamentosBytes)
	assertContains(t, lancamentos, `data-bulk-paid-form`)
	assertContains(t, lancamentos, `data-bulk-mark-paid`)
	assertContains(t, lancamentos, `type="button" data-bulk-mark-paid`)
	if strings.Contains(lancamentos, `hx-confirm="Marcar como pagos?"`) || strings.Contains(lancamentos, `type="submit" data-bulk-mark-paid`) {
		t.Fatal("bulk paid action must open payment-date modal, not submit directly")
	}

	layoutBytes, err := os.ReadFile(resolveTemplatePath(t, "templates/components/app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell template: %v", err)
	}
	layout := string(layoutBytes)
	assertContains(t, layout, `name="payment_date"`)
	assertContains(t, layout, `Pagar agora`)
	assertContains(t, layout, `Agendar`)
	if strings.Contains(layout, `Confirmar como pago`) || strings.Contains(layout, `Registrar agendamento`) {
		t.Fatal("payment modal must use concise paid/scheduled labels")
	}

	paymentJS, err := os.ReadFile(resolveTemplatePath(t, "assets/js/payment-confirm.js"))
	if err != nil {
		t.Fatalf("read payment-confirm.js: %v", err)
	}
	payment := string(paymentJS)
	assertContains(t, payment, `openBulkPaymentConfirmModal`)
	assertContains(t, payment, `Confirmar pagamentos`)
	assertContains(t, payment, `Agendar pagamento`)
	assertContains(t, payment, `Pagar agora`)
	assertContains(t, payment, `status_pagamento`)

	bulkJS, err := os.ReadFile(resolveTemplatePath(t, "assets/js/bulk-select.js"))
	if err != nil {
		t.Fatalf("read bulk-select.js: %v", err)
	}
	bulk := string(bulkJS)
	assertContains(t, bulk, `openBulkPaymentConfirmModal`)
	assertContains(t, bulk, `data-bulk-active`)
	assertContains(t, bulk, `has-bulk-selection`)

	fabJS, err := os.ReadFile(resolveTemplatePath(t, "assets/js/fab-controller.js"))
	if err != nil {
		t.Fatalf("read fab-controller.js: %v", err)
	}
	fab := string(fabJS)
	assertContains(t, fab, `has-bulk-selection`)
	assertContains(t, fab, `(max-width: 767px)`)
}

func TestLancamentosTemplatePlacesInvoicesBeforeBulkSelection(t *testing.T) {
	lancamentosBytes, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/lancamentos.html"))
	if err != nil {
		t.Fatalf("read lancamentos template: %v", err)
	}
	lancamentos := string(lancamentosBytes)
	invoicesIndex := strings.Index(lancamentos, `{{template "lancamentos-invoices" .}}`)
	bulkIndex := strings.Index(lancamentos, `data-bulk-toggle-mode>Selecionar`)
	if invoicesIndex < 0 || bulkIndex < 0 {
		t.Fatalf("template markers not found: invoices=%d bulk=%d", invoicesIndex, bulkIndex)
	}
	if invoicesIndex > bulkIndex {
		t.Fatalf("invoice cards must render before bulk selection control")
	}
}

func testLancamentosBranchTemplates(t *testing.T) *template.Template {
	t.Helper()

	return template.Must(template.New("lancamentos-branches").Parse(`
{{define "lancamentos-page"}}<div data-template="page"></div>{{end}}
{{define "lancamentos-content"}}<div data-template="content"></div>{{end}}
{{define "lancamentos-list"}}<div data-template="list"></div>{{end}}
`))
}

func TestVisualRegressionInvoiceAndFormEmblems(t *testing.T) {
	// F8: the legacy invoice-row component was removed; the emblem contract
	// now lives in the invoice-row define of lancamentos.html (ui-row family).
	tplInvoice := template.Must(template.New("invoice-test").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
	))

	t.Run("invoice-row with ProviderMark MP", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:               "inv-1",
			CardColor:        "#009EE3",
			CardProviderMark: "MP",
			CardIcon:         "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "MP")
		assertContains(t, html, "lancamentos-invoice-mark")
		assertContains(t, html, `data-cb-color="#009EE3"`)
	})

	t.Run("invoice-row with provider logo", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			CardColor:            "#EC7000",
			CardProviderMark:     "Itaú",
			CardProviderLogoPath: "institutions/itau.png",
			CardIcon:             "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `src="institutions/itau.png?v=test"`)
		assertContains(t, html, `class="institution-logo"`)
	})

	t.Run("invoice-row with ProviderMark INTER", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:               "inv-2",
			CardColor:        "#FF7A00",
			CardProviderMark: "INTER",
			CardIcon:         "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "INTER")
		assertContains(t, html, "lancamentos-invoice-mark")
	})

	t.Run("invoice-row with empty ProviderMark fallback", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:               "inv-3",
			CardColor:        "#6B7280",
			CardProviderMark: "",
			CardIcon:         "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		if strings.Contains(html, "lancamentos-invoice-mark") {
			t.Errorf("expected no invoice mark for empty ProviderMark, got: %s", html)
		}
		assertContains(t, html, "data-lucide=\"credit-card\"")
	})

	// Test form_lancamento.html rendering
	tplForm := template.Must(template.New("form-test").Funcs(template.FuncMap{
		"appVersion": func() string { return "test" },
		"assetPath":  func(path string) string { return path + "?v=test" },
		"intRange": func(start, end int) []int {
			var res []int
			for i := start; i <= end; i++ {
				res = append(res, i)
			}
			return res
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/components/form_lancamento.html"),
		resolveTemplatePath(t, "templates/components/financial-emblem.html"),
	))

	t.Run("form-lancamento initial with OrigemProviderMark NU", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			OrigemColor:        "#8A05BE",
			OrigemProviderMark: "NU",
			OrigemIcon:         "credit-card",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "NU")
		assertContains(t, html, "origemIconText")
		assertContains(t, html, "emblem-len-2")
		assertContains(t, html, `id="origemIcon" class="hidden`)
	})

	t.Run("form-lancamento initial with provider logo", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			OrigemColor:            "#EC7000",
			OrigemProviderMark:     "Itaú",
			OrigemProviderLogoPath: "institutions/itau.png",
			OrigemIcon:             "building-2",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `id="origemIconImage" src="institutions/itau.png?v=test"`)
	})

	t.Run("form-lancamento initial with empty OrigemProviderMark fallback", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			OrigemColor:        "#6B7280",
			OrigemProviderMark: "",
			OrigemIcon:         "wallet",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `id="origemIconText" class="hidden`)
		if strings.Contains(html, `id="origemIcon" class="hidden`) {
			t.Errorf("expected icon to be visible, but it is hidden: %s", html)
		}
		assertContains(t, html, "wallet")
	})

	t.Run("form-lancamento new form starts with empty category placeholder", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()

		seedInvoicePaymentScenario(t, db)
		now := time.Now().Unix()
		execTestSQL(t, db, `
			INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
			VALUES
				('cat-form-expense', 'ws-test', 'Alimentação', 'utensils', '#f97316', 'EXPENSE', ?),
				('cat-form-income', 'ws-test', 'Salário', 'briefcase', '#22c55e', 'INCOME', ?)
		`, now, now)

		handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
		accounts, err := handler.queryFormAccounts()
		if err != nil {
			t.Fatalf("queryFormAccounts: %v", err)
		}
		categories, err := handler.queryFormCategories()
		if err != nil {
			t.Fatalf("queryFormCategories: %v", err)
		}

		data := newFormTransacaoData(accounts, categories)
		if data.CategoriaID != "" {
			t.Fatalf("new form categoria_id = %q, want empty", data.CategoriaID)
		}
		if data.CategoriaNome != "" {
			t.Fatalf("new form categoria nome = %q, want empty", data.CategoriaNome)
		}
		if data.CategoriaType != "" {
			t.Fatalf("new form categoria type = %q, want empty", data.CategoriaType)
		}

		var buf bytes.Buffer
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento new form: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `name="categoria_id" value=""`)
		assertContains(t, html, `id="categoriaNome"`)
		assertContains(t, html, `Selecione uma categoria`)
	})

	t.Run("form-lancamento initial with DestinoProviderMark INTER", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			DestinoColor:        "#FF7A00",
			DestinoProviderMark: "INTER",
			DestinoIcon:         "credit-card",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "INTER")
		assertContains(t, html, "destinoIconText")
		assertContains(t, html, "emblem-len-5")
		assertContains(t, html, `id="destinoIcon" class="hidden`)
	})

	t.Run("form-lancamento initial with empty DestinoProviderMark fallback", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			DestinoColor:        "#6B7280",
			DestinoProviderMark: "",
			DestinoIcon:         "wallet",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `id="destinoIconText" class="hidden`)
		if strings.Contains(html, `id="destinoIcon" class="hidden`) {
			t.Errorf("expected icon to be visible, but it is hidden: %s", html)
		}
		assertContains(t, html, "wallet")
	})

	t.Run("form-lancamento initial with both OrigemProviderMark and DestinoProviderMark", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			OrigemColor:         "#8A05BE",
			OrigemProviderMark:  "NU",
			OrigemIcon:          "credit-card",
			DestinoColor:        "#009EE3",
			DestinoProviderMark: "MP",
			DestinoIcon:         "credit-card",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "NU")
		assertContains(t, html, "origemIconText")
		assertContains(t, html, "emblem-len-2")
		assertContains(t, html, `id="origemIcon" class="hidden`)

		assertContains(t, html, "MP")
		assertContains(t, html, "destinoIconText")
		assertContains(t, html, "emblem-len-2")
		assertContains(t, html, `id="destinoIcon" class="hidden`)
	})

	t.Run("form-lancamento initial with mixed marks (origin empty, destination set)", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			OrigemColor:         "#6B7280",
			OrigemProviderMark:  "",
			OrigemIcon:          "wallet",
			DestinoColor:        "#FF7A00",
			DestinoProviderMark: "INTER",
			DestinoIcon:         "credit-card",
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `id="origemIconText" class="hidden`)
		if strings.Contains(html, `id="origemIcon" class="hidden`) {
			t.Errorf("expected origin icon to be visible, but it is hidden: %s", html)
		}

		assertContains(t, html, "INTER")
		assertContains(t, html, "destinoIconText")
		assertContains(t, html, "emblem-len-5")
		assertContains(t, html, `id="destinoIcon" class="hidden`)
	})

	t.Run("form-lancamento business renders contact section and inline create action", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			IsBusiness:       true,
			CanCreateContact: true,
			Contacts: []ContatoRow{
				{ID: "ct-1", Name: "Cliente A", Type: "client", TypeLabel: "Cliente"},
			},
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento business: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `id="contatoCombobox"`)
		assertContains(t, html, `id="contatoCreateOption"`)
		assertContains(t, html, `hx-get="/api/contatos/novo"`)
		assertContains(t, html, `name="contact_id"`)
	})

	t.Run("form-lancamento personal omits contact section", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			IsBusiness: false,
			Contacts: []ContatoRow{
				{ID: "ct-1", Name: "Cliente A", Type: "client", TypeLabel: "Cliente"},
			},
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento personal: %v", err)
		}
		html := buf.String()
		if strings.Contains(html, `id="contatoCombobox"`) {
			t.Fatalf("form personal nao deve renderizar seletor de contato")
		}
		if strings.Contains(html, `id="contatoCreateOption"`) {
			t.Fatalf("form personal nao deve renderizar botao de criar contato")
		}
	})

	t.Run("form-lancamento renders hidden category options with parent hierarchy", func(t *testing.T) {
		var buf bytes.Buffer
		data := FormTransacaoData{
			Categories: []FormCategory{
				{ID: "cat-parent", Name: "Alimentacao", Icon: "utensils", Color: "#EF4444", Type: "EXPENSE"},
				{ID: "cat-child", Name: "Supermercado", Icon: "shopping-cart", Color: "#F97316", Type: "EXPENSE", ParentID: "cat-parent", ParentName: "Alimentacao"},
				{ID: "cat-std", Name: "Salario", Icon: "briefcase", Color: "#22C55E", Type: "INCOME"},
			},
		}
		if err := tplForm.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("failed to execute form-lancamento with categories: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `data-category-id="cat-parent"`)
		assertContains(t, html, `data-category-name="Alimentacao"`)
		assertContains(t, html, `data-category-id="cat-child"`)
		assertContains(t, html, `data-category-parent-id="cat-parent"`)
		assertContains(t, html, `data-category-parent-name="Alimentacao"`)
		assertContains(t, html, `data-category-id="cat-std"`)
		// cat-parent and cat-std are root categories; they must NOT have parent data attributes
		parentAttrCount := strings.Count(html, `data-category-parent-id`)
		if parentAttrCount != 1 {
			t.Errorf("expected exactly 1 data-category-parent-id occurrence, got %d", parentAttrCount)
		}
	})
}

func TestTransactionRowConfirmTexts(t *testing.T) {
	tplRow := template.Must(template.New("row-test").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/pages/lancamentos.html"),
	))

	t.Run("renders confirm attributes on transaction row status toggle", func(t *testing.T) {
		var buf bytes.Buffer
		data := TransactionRow{
			ID:                          "tx-123",
			AccountName:                 "Inter",
			PaymentAccountID:            "acc-inter",
			PaymentAccountChangeAllowed: true,
			PaymentConfirm:              "Marcar este lançamento como pago?",
			PaymentConfirmTitle:         "Confirmar pagamento",
			PaymentIcon:                 "clock",
			PaymentTitle:                "Pendente — toque para marcar como pago",
			PaymentStatus:               "pending",
			IsPending:                   true,
		}
		if err := tplRow.ExecuteTemplate(&buf, "lancamento-row", data); err != nil {
			t.Fatalf("failed to execute lancamento-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `class="tx-edit-trigger`)
		assertContains(t, html, `<span class="sr-only">Editar lançamento: </span>`)
		assertContains(t, html, `data-payment-confirm`)
		assertContains(t, html, `data-swipe-right-payment-confirm="true"`)
		assertContains(t, html, `data-payment-post-url="/transacoes/tx-123/status-pagamento"`)
		assertContains(t, html, `data-payment-account-id="acc-inter"`)
		assertContains(t, html, `data-payment-account-name="Inter"`)
		assertContains(t, html, `data-payment-account-change-allowed="true"`)
		if strings.Contains(html, `data-swipe-right-url="/transacoes/tx-123/status-pagamento"`) {
			t.Fatal("pending transaction row must not post payment status directly from swipe")
		}
		assertContains(t, html, `data-lucide="clock"`)
	})

	t.Run("renders invoice columns with isolated financial summary", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:                "invoice-mobile",
			AccountName:       "Cartão com nome muito extenso",
			ReferenceLabel:    "Agosto de 2026",
			DateLabel:         "25/08/2026",
			ItemCount:         12,
			FaturaURL:         "/cartoes/faturas/invoice-mobile",
			PendingMoney:      MoneyDisplay{Reais: "1.234", Cents: ",56"},
			TotalMoney:        MoneyDisplay{Reais: "2.000", Cents: ",00"},
			PaidMoney:         MoneyDisplay{Reais: "765", Cents: ",44"},
			HasPartialPayment: true,
			BadgeLabel:        "Parcial",
			CardColor:         "#009EE3",
			CardProviderMark:  "MP",
		}
		if err := tplRow.ExecuteTemplate(&buf, "invoice-row", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		for _, want := range []string{
			`class="ui-row ui-row--invoice-summary ui-row--link`,
			`href="/cartoes/faturas/invoice-mobile"`,
			`hx-get="/cartoes/faturas/invoice-mobile"`,
			`hx-target="#main-content"`,
			`hx-select="#main-content"`,
			`hx-push-url="true"`,
			`data-swipe-row="true"`,
			`data-swipe-right-url="/cartoes/faturas/invoice-mobile"`,
			`class="ui-avatar" data-cb-color="#009EE3" aria-hidden="true"`,
			`lancamentos-invoice-mark">MP</span>`,
			`class="ui-row__title lancamentos-invoice-title"`,
			`Cartão com nome muito extenso`,
			`— Agosto de 2026`,
			`Vence em 25/08/2026`,
			`12 itens`,
			`Total R$ 2.000,00`,
			`Pago R$ 765,44`,
			`class="ui-row__amount" data-sensitive>R$ 1.234,56</span>`,
			`ui-badge--info">Parcial</span>`,
		} {
			assertContains(t, html, want)
		}
		if strings.Contains(html, `class="invoice-content`) || strings.Contains(html, `class="invoice-copy`) || strings.Contains(html, `class="invoice-summary`) {
			t.Fatal("Legacy invoice card classes must not return to /lancamentos")
		}
		if got := strings.Count(html, `data-sensitive`); got != 2 {
			t.Fatalf("invoice privacy values = %d, want 2 (pending + partial detail)", got)
		}
	})

	t.Run("renders transfer origin and destination without category", func(t *testing.T) {
		var buf bytes.Buffer
		data := TransactionRow{
			ID:                     "transfer-123",
			Type:                   models.TransactionTypeTransfer,
			Description:            "Transferência entre contas",
			AccountName:            "Inter",
			DestinationAccountName: "Nubank",
			CategoryName:           "Categoria não deve aparecer",
			PaymentIcon:            "arrow-left-right",
			PaymentTone:            "success",
			PaymentStatus:          models.PaymentStatusPaid,
		}
		if err := tplRow.ExecuteTemplate(&buf, "lancamento-row", data); err != nil {
			t.Fatalf("failed to execute transfer row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `<span class="lancamentos-row-transfer__account" title="Inter">Inter</span>`)
		assertContains(t, html, `&rarr;`)
		assertContains(t, html, `<span class="lancamentos-row-transfer__account" title="Nubank">Nubank</span>`)
		assertContains(t, html, `class="tx-status tx-status-success ui-row__status"`)
		assertContains(t, html, `data-lucide="arrow-left-right"`)
		if strings.Contains(html, data.CategoryName) {
			t.Fatalf("transfer row must not render category: %s", html)
		}
	})
}

func TestTransactionVisualStatusKeepsTransferIconAcrossPaymentStates(t *testing.T) {
	now := time.Now().In(appTimeLocation())
	todayUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	scheduledAt := todayUTC + 3600

	for _, tc := range []struct {
		name          string
		paymentStatus string
		displayDate   int64
		scheduledAt   *int64
		wantTone      string
		wantTitle     string
	}{
		{name: "confirmed", paymentStatus: models.PaymentStatusPaid, displayDate: todayUTC, wantTone: "success", wantTitle: "Transferência confirmada"},
		{name: "pending", paymentStatus: models.PaymentStatusPending, displayDate: todayUTC, wantTone: "warning", wantTitle: "Transferência pendente"},
		{name: "overdue", paymentStatus: models.PaymentStatusPending, displayDate: todayUTC - 24*60*60, wantTone: "danger", wantTitle: "Transferência atrasada"},
		{name: "scheduled", paymentStatus: models.PaymentStatusPending, displayDate: todayUTC, scheduledAt: &scheduledAt, wantTone: "scheduled", wantTitle: "Transferência agendada"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := transactionVisualStatus(models.TransactionTypeTransfer, tc.paymentStatus, tc.displayDate, tc.scheduledAt)
			if got.Icon != "arrow-left-right" || got.Tone != tc.wantTone || got.Title != tc.wantTitle {
				t.Fatalf("visual = %#v, want icon arrow-left-right, tone %q, title %q", got, tc.wantTone, tc.wantTitle)
			}
		})
	}
}

func TestLancamentosAccountFiltersIncludeIncomingAndOutgoingTransfers(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	for _, account := range []struct {
		id   string
		name string
	}{
		{id: "checking-destination", name: "Nubank"},
		{id: "checking-origin", name: "Inter"},
	} {
		execTestSQL(t, db, `
			INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
			VALUES (?, 'ws-test', ?, 'CHECKING', 0, 0, ?, ?)
		`, account.id, account.name, now, now)
	}

	transferDate := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC).Unix()
	for _, tx := range []struct {
		id          string
		origin      string
		destination string
		description string
	}{
		{id: "transfer-out", origin: "checking-test", destination: "checking-destination", description: "Saída da conta filtrada"},
		{id: "transfer-in", origin: "checking-origin", destination: "checking-test", description: "Entrada na conta filtrada"},
		{id: "transfer-unrelated", origin: "checking-origin", destination: "checking-destination", description: "Transferência não relacionada"},
	} {
		execTestSQL(t, db, `
			INSERT INTO transactions (
				id, workspace_id, user_id, account_id, destination_account_id,
				type, amount, date, description, status,
				installment_number, total_installments, created_at, updated_at
			) VALUES (?, 'ws-test', 'user-test', ?, ?, 'TRANSFER', 1000, ?, ?, 'paid', 1, 1, ?, ?)
		`, tx.id, tx.origin, tx.destination, transferDate, tx.description, now, now)
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	assertTransfers := func(t *testing.T, data LancamentosData) {
		t.Helper()
		byID := make(map[string]TransactionRow, len(data.Transactions))
		for _, row := range data.Transactions {
			byID[row.ID] = row
		}
		if len(byID) != 2 {
			t.Fatalf("filtered transfers = %v, want only incoming and outgoing", byID)
		}
		if _, ok := byID["transfer-out"]; !ok {
			t.Fatal("outgoing transfer missing from account filter")
		}
		incoming, ok := byID["transfer-in"]
		if !ok {
			t.Fatal("incoming transfer missing from account filter")
		}
		if incoming.AccountName != "Inter" || incoming.DestinationAccountName != "Conta Teste" {
			t.Fatalf("incoming transfer route = %q -> %q", incoming.AccountName, incoming.DestinationAccountName)
		}
		if _, ok := byID["transfer-unrelated"]; ok {
			t.Fatal("unrelated transfer must not match account filter")
		}
	}

	filters := LancamentosFilters{Tipos: []string{"transferencia"}, Situacoes: []string{"pago"}}
	fromDashboard, err := handler.buildLancamentosData("checking-test", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData dashboard account filter: %v", err)
	}
	assertTransfers(t, fromDashboard)

	filters.OrigemIDs = []string{"checking-test"}
	fromFilterPanel, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData filter panel account: %v", err)
	}
	assertTransfers(t, fromFilterPanel)
}

// TestLancamentosChronologicalAscendingOrder verifica que a lista de lançamentos
// é exibida em ordem cronológica crescente: dia 01 no topo, fim do mês no final.
func TestLancamentosChronologicalAscendingOrder(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()

	// Agosto 2026 está no futuro (execução em junho 2026).
	// Transações "paid" são excluídas pelo filtro padrão (pendente+vencido).
	// Inserimos com status 'paid' e usamos filtro explícito {Situacoes: ["pago"]}
	// para testar a ordenação isolada do status.
	dateAug01 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dateAug10 := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	dateAug30 := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC).Unix()

	// Inseridos intencionalmente fora de ordem (30 > 01 > 10) para provar que
	// a ordenação é determinada pela query/sort e não pela ordem de inserção.
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-aug30', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 1000, ?, 'Despesa dia 30', 'paid', 1, 1, ?, ?)
	`, dateAug30, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-aug01', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 2000, ?, 'Despesa dia 01', 'paid', 1, 1, ?, ?)
	`, dateAug01, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-aug10', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 3000, ?, 'Despesa dia 10', 'paid', 1, 1, ?, ?)
	`, dateAug10, now, now)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	// Filtro explícito "pago" para incluir as transações acima
	filters := LancamentosFilters{Situacoes: []string{"pago"}}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData failed: %v", err)
	}

	// Coleta somente as transações da conta corrente (ignorando a fatura do cartão)
	var checkingTx []TransactionRow
	for _, tx := range data.Transactions {
		if tx.AccountName == "Conta Teste" {
			checkingTx = append(checkingTx, tx)
		}
	}

	if len(checkingTx) != 3 {
		t.Fatalf("esperava 3 transações na conta corrente, obteve %d (total data.Transactions=%d)", len(checkingTx), len(data.Transactions))
	}

	// Ordem esperada: dia 01 primeiro, dia 10 segundo, dia 30 terceiro
	if checkingTx[0].ID != "tx-aug01" {
		t.Errorf("1ª transação esperada: tx-aug01 (dia 01), obteve: %s (date=%d)", checkingTx[0].ID, checkingTx[0].Date)
	}
	if checkingTx[1].ID != "tx-aug10" {
		t.Errorf("2ª transação esperada: tx-aug10 (dia 10), obteve: %s (date=%d)", checkingTx[1].ID, checkingTx[1].Date)
	}
	if checkingTx[2].ID != "tx-aug30" {
		t.Errorf("3ª transação esperada: tx-aug30 (dia 30), obteve: %s (date=%d)", checkingTx[2].ID, checkingTx[2].Date)
	}

	// Validação adicional: verifica que DateUnix cresce monotonicamente
	for i := 1; i < len(checkingTx); i++ {
		if checkingTx[i].Date < checkingTx[i-1].Date {
			t.Errorf("ordenação incorreta: tx[%d].Date(%d) < tx[%d].Date(%d)", i, checkingTx[i].Date, i-1, checkingTx[i-1].Date)
		}
	}
}

// TestLancamentosNoVolumeLimitOver200 garante que meses com mais de 200 lançamentos
// retornam todos os registros sem truncamento silencioso.
// Antes da remoção do LIMIT 200, lançamentos do fim do mês eram ocultados em Workspaces Business.
func TestLancamentosNoVolumeLimitOver200(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	const totalTx = 201

	// Insere 201 transações distribuídas ao longo de agosto 2026 com status 'paid'.
	// Inseridas com datas crescentes: dias 1..28 com múltiplas transações por dia.
	for i := 0; i < totalTx; i++ {
		// Distribui pelos 28 primeiros dias do mês (1 a 28), várias por dia
		day := (i % 28) + 1
		txDate := time.Date(2026, 8, day, 10, i, 0, 0, time.UTC).Unix()
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 100, ?, ?, 'paid', 1, 1, ?, ?)
		`, fmt.Sprintf("tx-vol-%03d", i), txDate, fmt.Sprintf("Despesa volume %d", i), now+int64(i), now+int64(i))
	}

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	filters := LancamentosFilters{Situacoes: []string{"pago"}}
	data, err := handler.buildLancamentosData("", 8, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData failed: %v", err)
	}

	// Coleta somente as transações da conta corrente
	var checkingTx []TransactionRow
	for _, tx := range data.Transactions {
		if tx.AccountName == "Conta Teste" {
			checkingTx = append(checkingTx, tx)
		}
	}

	// Deve retornar todos os 201 lançamentos sem truncamento
	if len(checkingTx) < totalTx {
		t.Errorf("limite silencioso detectado: esperava >= %d transações, obteve %d — lançamentos do mês estão sendo ocultados", totalTx, len(checkingTx))
	}

	// Orderm crescente deve estar preservada
	for i := 1; i < len(checkingTx); i++ {
		if checkingTx[i].Date < checkingTx[i-1].Date {
			t.Errorf("ordenação incorreta em posição %d: Date(%d) < Date(%d)", i, checkingTx[i].Date, checkingTx[i-1].Date)
		}
	}
}

func TestBuildLancamentosDataSortOrderDesc(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	now := time.Now().Unix()
	dateAug01 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Unix()
	dateAug10 := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	dateAug30 := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-desc-01', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 1000, ?, 'Despesa dia 01', 'paid', 1, 1, ?, ?)
	`, dateAug01, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-desc-10', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 2000, ?, 'Despesa dia 10', 'paid', 1, 1, ?, ?)
	`, dateAug10, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-desc-30', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 3000, ?, 'Despesa dia 30', 'paid', 1, 1, ?, ?)
	`, dateAug30, now, now)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 8, 2026, LancamentosFilters{
		Situacoes: []string{"pago"},
		Order:     "desc",
	})
	if err != nil {
		t.Fatalf("buildLancamentosData failed: %v", err)
	}

	var checkingTx []TransactionRow
	for _, tx := range data.Transactions {
		if tx.AccountName == "Conta Teste" {
			checkingTx = append(checkingTx, tx)
		}
	}

	if len(checkingTx) != 3 {
		t.Fatalf("esperava 3 transações na conta corrente, obteve %d", len(checkingTx))
	}
	if checkingTx[0].ID != "tx-desc-30" || checkingTx[1].ID != "tx-desc-10" || checkingTx[2].ID != "tx-desc-01" {
		t.Fatalf("ordem desc incorreta: got [%s %s %s]", checkingTx[0].ID, checkingTx[1].ID, checkingTx[2].ID)
	}
	if data.SortOrder != "desc" {
		t.Fatalf("sort order = %q, want desc", data.SortOrder)
	}
}

func TestLancamentosMonthQueryWithFiltersPreservesStatuses(t *testing.T) {
	filters := LancamentosFilters{
		Situacoes: []string{"pendente", "vencido", "pago"},
	}
	query := lancamentosMonthQueryWithFilters("", 7, 2026, filters)
	for _, s := range []string{"situacao=pendente", "situacao=vencido", "situacao=pago"} {
		if !strings.Contains(query, s) {
			t.Errorf("query %q missing %s", query, s)
		}
	}
	if !strings.Contains(query, "mes=7") || !strings.Contains(query, "ano=2026") {
		t.Errorf("query %q missing mes/ano", query)
	}
}

func TestLancamentosMonthQueryWithFiltersPreservesAllParams(t *testing.T) {
	filters := LancamentosFilters{
		Tipos:      []string{"receita"},
		Situacoes:  []string{"pago"},
		OrigemIDs:  []string{"acc-1"},
		DestinoIDs: []string{"acc-2"},
		Categorias: []string{"cat-1"},
		Busca:      "teste",
		Order:      "desc",
	}
	query := lancamentosMonthQueryWithFilters("conta-x", 7, 2026, filters)
	for _, expected := range []string{
		"mes=7", "ano=2026", "conta=conta-x",
		"tipo=receita", "situacao=pago",
		"origem=acc-1", "destino=acc-2",
		"categoria=cat-1", "q=teste", "ordem=desc",
	} {
		if !strings.Contains(query, expected) {
			t.Errorf("query %q missing %s", query, expected)
		}
	}
	if strings.Contains(query, "partial=") {
		t.Errorf("query %q should not contain partial=", query)
	}
}

func TestLancamentosMonthQueryWithFiltersCleanWhenEmpty(t *testing.T) {
	query := lancamentosMonthQueryWithFilters("", 7, 2026, LancamentosFilters{})
	for _, absent := range []string{"situacao=", "tipo=", "q=", "ordem=", "origem=", "destino=", "categoria="} {
		if strings.Contains(query, absent) {
			t.Errorf("clean query %q should not contain %s", query, absent)
		}
	}
	if !strings.Contains(query, "mes=7") || !strings.Contains(query, "ano=2026") {
		t.Errorf("clean query %q missing mes/ano", query)
	}
}

func TestLancamentosMonthQueryWithFiltersNoEmptyQ(t *testing.T) {
	query := lancamentosMonthQueryWithFilters("", 7, 2026, LancamentosFilters{Busca: ""})
	if strings.Contains(query, "q=") {
		t.Errorf("query %q should not contain q= when search is empty", query)
	}
}

func TestLancamentosMonthQueryOmitsAscOrder(t *testing.T) {
	query := lancamentosMonthQueryWithFilters("", 7, 2026, LancamentosFilters{Order: "asc"})
	if strings.Contains(query, "ordem=") {
		t.Errorf("query %q should omit ordem= when asc (default)", query)
	}
}

func TestBuildLancamentosDataMonthSelectorPreservesFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	filters := LancamentosFilters{
		Situacoes: []string{"pendente", "vencido", "pago"},
		Order:     "desc",
	}
	data, err := handler.buildLancamentosData("", 6, 2026, filters)
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	if !strings.Contains(data.MonthSelectorPrevQuery, "mes=5") {
		t.Errorf("prev query %q missing mes=5", data.MonthSelectorPrevQuery)
	}
	for _, s := range []string{"situacao=pendente", "situacao=vencido", "situacao=pago"} {
		if !strings.Contains(data.MonthSelectorPrevQuery, s) {
			t.Errorf("prev query %q missing %s", data.MonthSelectorPrevQuery, s)
		}
	}
	if !strings.Contains(data.MonthSelectorPrevQuery, "ordem=desc") {
		t.Errorf("prev query %q missing ordem=desc", data.MonthSelectorPrevQuery)
	}

	if !strings.Contains(data.MonthSelectorNextQuery, "mes=7") {
		t.Errorf("next query %q missing mes=7", data.MonthSelectorNextQuery)
	}
	for _, s := range []string{"situacao=pendente", "situacao=vencido", "situacao=pago"} {
		if !strings.Contains(data.MonthSelectorNextQuery, s) {
			t.Errorf("next query %q missing %s", data.MonthSelectorNextQuery, s)
		}
	}

	julyOption := data.MonthOptions[6]
	if !strings.Contains(julyOption.Query, "situacao=pendente") {
		t.Errorf("july option query %q missing situacao=pendente", julyOption.Query)
	}
}

func TestBuildLancamentosDataMonthSelectorCleanWithoutFilters(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	data, err := handler.buildLancamentosData("", 6, 2026, LancamentosFilters{})
	if err != nil {
		t.Fatalf("buildLancamentosData: %v", err)
	}

	for _, q := range []string{data.MonthSelectorPrevQuery, data.MonthSelectorNextQuery, data.MonthSelectorCurrentQuery} {
		for _, absent := range []string{"situacao=", "tipo=", "q=", "origem=", "destino=", "categoria="} {
			if strings.Contains(q, absent) {
				t.Errorf("query %q should not contain %s when no filters active", q, absent)
			}
		}
	}
}

func TestLancamentosPartialResponseHXReplaceUrl(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	tpl := template.Must(template.New("lancamentos-hxurl-test").Parse(`
{{define "lancamentos-page"}}<div data-template="page"></div>{{end}}
{{define "lancamentos-content"}}<div data-template="content"></div>{{end}}
{{define "lancamentos-list"}}<div data-template="list"></div>{{end}}
`))

	handler := TransactionHandler{
		DB:          db,
		Templates:   tpl,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/lancamentos?mes=7&ano=2026&situacao=pago&partial=lista", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleListarTransacoes(rr, req)

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
	if !strings.Contains(replaceUrl, "situacao=pago") {
		t.Errorf("HX-Replace-Url %q should contain situacao=pago", replaceUrl)
	}
	if !strings.Contains(replaceUrl, "mes=7") {
		t.Errorf("HX-Replace-Url %q should contain mes=7", replaceUrl)
	}
}

func TestLancamentosPartialResponseOOBPeriod(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	tpl := template.Must(template.New("lancamentos-oob-test").Parse(`
{{define "lancamentos-page"}}<div data-template="page"></div>{{end}}
{{define "lancamentos-content"}}<div data-template="content"></div>{{end}}
{{define "lancamentos-list"}}<div data-template="list"></div>{{end}}
{{define "lancamentos-filter-period"}}<span id="lancamentos-filter-period" {{if .OOB}}hx-swap-oob="outerHTML"{{end}}><input type="hidden" name="mes" value="{{.MesAtual}}"><input type="hidden" name="ano" value="{{.AnoAtual}}"></span>{{end}}
`))

	handler := TransactionHandler{
		DB:          db,
		Templates:   tpl,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest(http.MethodGet, "/lancamentos?mes=7&ano=2026&partial=lista", nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleListarTransacoes(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()
	assertContains(t, body, `id="lancamentos-filter-period"`)
	assertContains(t, body, `hx-swap-oob="outerHTML"`)
	assertContains(t, body, `name="mes" value="7"`)
	assertContains(t, body, `name="ano" value="2026"`)
}
