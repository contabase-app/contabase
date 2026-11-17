package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// F7 keeps the transaction sheet visually modern while every functional
// contract (ids, names, htmx endpoints, hidden fields and button types) stays
// frozen. These assertions fail before a visual change can silently break the
// form submit path.
func TestFormLancamentoF7SheetContract(t *testing.T) {
	tpl := template.Must(template.New("form-f7").Funcs(template.FuncMap{
		"appVersion": func() string { return "test" },
		"assetPath":  func(path string) string { return path + "?v=test" },
		"intRange": func(start, end int) []int {
			var res []int
			for i := start; i <= end; i++ {
				res = append(res, i)
			}
			return res
		},
		"hasString": func(items []string, target string) bool {
			for _, item := range items {
				if item == target {
					return true
				}
			}
			return false
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/components/form_lancamento.html"),
		resolveTemplatePath(t, "templates/components/financial-emblem.html"),
	))

	render := func(data FormTransacaoData) string {
		t.Helper()
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
			t.Fatalf("render form-lancamento: %v", err)
		}
		return buf.String()
	}

	t.Run("create sheet exposes a named dialog and a guarded footer", func(t *testing.T) {
		html := render(FormTransacaoData{TipoInicial: "despesa"})

		for _, want := range []string{
			`role="dialog"`,
			`aria-modal="true"`,
			`aria-labelledby="lancamento-sheet-title"`,
			`aria-describedby="lancamento-sheet-context"`,
			`id="lancamento-sheet-title"`,
			`Nova despesa`,
			`hx-post="/transacoes"`,
			`hx-sync="this:drop"`,
			`enctype="multipart/form-data"`,
			`id="valor" name="valor"`,
			`data-money-mask`,
			`id="lancamentoSubmit"`,
			`form="lancamento-form"`,
			`data-form-footer`,
			`data-onclick="closeBottomSheet">`,
			`id="lancamento-form-error-region"`,
			`aria-live="assertive"`,
			`id="transferDirectionHint"`,
			`id="limitInfoText" data-sensitive`,
		} {
			assertContains(t, html, want)
		}
		if strings.Contains(html, `hx-put=`) {
			t.Fatalf("create sheet must not render an edit endpoint")
		}
	})

	t.Run("edit sheet keeps htmx targets and series scope modals", func(t *testing.T) {
		html := render(FormTransacaoData{
			IsEdit:        true,
			EditID:        "tx-1",
			IsSeriesEdit:  true,
			StatusInicial: "paid",
			TipoInicial:   "despesa",
		})

		for _, want := range []string{
			`hx-put="/transacoes/tx-1"`,
			`hx-target="#tx-tx-1"`,
			`hx-swap="outerHTML"`,
			`Editar lançamento`,
			`id="seriesScopeModal"`,
			`id="seriesDeleteModal"`,
			`data-series-delete-open`,
			`data-series-scope-confirm`,
			`data-series-delete-confirm`,
			`aria-label="Salvar alterações"`,
			`Salvar`,
		} {
			assertContains(t, html, want)
		}
		if strings.Contains(html, `hx-post="/transacoes"`) {
			t.Fatalf("edit sheet must not render the create endpoint")
		}
	})

	t.Run("transfer keeps both pickers and the direction hint", func(t *testing.T) {
		html := render(FormTransacaoData{TipoInicial: "transferencia"})

		for _, want := range []string{
			`Nova transferência`,
			`id="origemPicker"`,
			`id="destinoPicker"`,
			`id="transferDirectionHint"`,
			`data-type="transferencia" aria-pressed="true"`,
			`data-type="despesa" aria-pressed="false"`,
		} {
			assertContains(t, html, want)
		}
	})

	t.Run("date labels follow the persisted field semantics", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			data       FormTransacaoData
			label      string
			dueUI      bool
			business   bool
			competence bool
			dueMeta    bool
		}{
			{name: "pending expense", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "pending"}, label: "Data de vencimento"},
			{name: "paid expense", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "paid"}, label: "Data do lançamento"},
			{name: "paid card expense", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "paid", OrigemTipo: "cartao"}, label: "Data da compra"},
			{name: "pending card expense", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "pending", OrigemTipo: "cartao"}, label: "Data da compra"},
			{name: "pending income", data: FormTransacaoData{TipoInicial: "receita", StatusInicial: "pending"}, label: "Data de recebimento prevista"},
			{name: "received income", data: FormTransacaoData{TipoInicial: "receita", StatusInicial: "paid"}, label: "Data do lançamento"},
			{name: "transfer", data: FormTransacaoData{TipoInicial: "transferencia", StatusInicial: "paid"}, label: "Data da transferência"},
			{name: "explicit due differs", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "pending", DataPreenchida: "2026-11-10", DueDatePreenchida: "2026-11-15", VencimentoDisplay: "15/11/2026"}, label: "Data do lançamento", dueMeta: true},
			{name: "business competence", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "pending", IsBusiness: true}, label: "Data de competência", dueUI: true, business: true, competence: true},
			{name: "business explicit due", data: FormTransacaoData{TipoInicial: "despesa", StatusInicial: "pending", IsBusiness: true, DataPreenchida: "2026-11-10", DueDatePreenchida: "2026-11-15"}, label: "Data de competência", dueUI: true, business: true, competence: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				data := tc.data
				data.IsEdit = true
				html := render(data)
				labelStart := strings.Index(html, `id="dataLancamentoLabel"`)
				if labelStart < 0 {
					t.Fatal("primary date label is missing")
				}
				labelEnd := strings.Index(html[labelStart:], `</label>`)
				if labelEnd < 0 {
					t.Fatal("primary date label is not closed")
				}
				primaryLabel := html[labelStart : labelStart+labelEnd]
				if !strings.Contains(primaryLabel, tc.label) {
					t.Fatalf("primary date label = %q, want %q", primaryLabel, tc.label)
				}
				if got := strings.Contains(html, "Competência"); got != tc.competence {
					t.Fatalf("competence meta = %t, want %t", got, tc.competence)
				}
				if got := strings.Contains(html, ">Vencimento<"); got != tc.dueMeta {
					t.Fatalf("due meta = %t, want %t", got, tc.dueMeta)
				}
				if got := strings.Contains(html, `id="dueDateSection"`); got != tc.dueUI {
					t.Fatalf("due date section = %t, want %t", got, tc.dueUI)
				}
				if got := strings.Contains(html, `data-is-business="true"`); got != tc.business {
					t.Fatalf("business marker = %t, want %t", got, tc.business)
				}
				if got := strings.Contains(html, `data-explicit-due-differs="true"`); got != tc.data.ExplicitDueDiffers() {
					t.Fatalf("explicit due marker = %t, want %t", got, tc.data.ExplicitDueDiffers())
				}
			})
		}
	})

	t.Run("paid_at is shown separately and follows the transaction type", func(t *testing.T) {
		expense := render(FormTransacaoData{TipoInicial: "despesa", StatusInicial: "paid", IsEdit: true, DataPreenchida: "2026-11-10", PaidAtDisplay: "05/11/2026"})
		assertContains(t, expense, "Data do lançamento")
		assertContains(t, expense, `name="data" value="2026-11-10"`)
		assertContains(t, expense, "05/11/2026")
		if got := strings.Count(expense, "Data de pagamento"); got != 1 {
			t.Fatalf("payment date label count = %d, want only the paid_at meta row", got)
		}
		if strings.Contains(expense, "Competência") {
			t.Fatal("Personal paid expense must not show competence meta")
		}

		income := render(FormTransacaoData{TipoInicial: "receita", StatusInicial: "paid", IsEdit: true, DataPreenchida: "2026-11-10", PaidAtDisplay: "05/11/2026"})
		assertContains(t, income, "Data do lançamento")
		assertContains(t, income, `name="data" value="2026-11-10"`)
		assertContains(t, income, "05/11/2026")
		if got := strings.Count(income, "Data de recebimento"); got != 1 {
			t.Fatalf("receipt date label count = %d, want only the paid_at meta row", got)
		}
		if strings.Contains(income, "Data de pagamento") {
			t.Fatal("income paid_at must be labeled as receipt")
		}

		businessIncome := render(FormTransacaoData{TipoInicial: "receita", StatusInicial: "paid", IsBusiness: true, IsEdit: true, DataPreenchida: "2026-11-10", PaidAtDisplay: "05/11/2026"})
		assertContains(t, businessIncome, "Data de competência")
		assertContains(t, businessIncome, "Data de pagamento")
		if strings.Contains(businessIncome, "Data de recebimento") {
			t.Fatal("Business paid_at label must remain unchanged")
		}
	})
}
