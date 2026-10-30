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
			`id="reserveInfoText" data-sensitive`,
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
}
