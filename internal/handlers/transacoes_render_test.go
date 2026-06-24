package handlers

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		resolveTemplatePath(t, "templates/components/invoice-row.html"),
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

func testLancamentosBranchTemplates(t *testing.T) *template.Template {
	t.Helper()

	return template.Must(template.New("lancamentos-branches").Parse(`
{{define "lancamentos-page"}}<div data-template="page"></div>{{end}}
{{define "lancamentos-content"}}<div data-template="content"></div>{{end}}
{{define "lancamentos-list"}}<div data-template="list"></div>{{end}}
`))
}

func TestVisualRegressionInvoiceAndFormEmblems(t *testing.T) {
	// Test invoice-row.html rendering
	tplInvoice := template.Must(template.New("invoice-test").ParseFiles(
		resolveTemplatePath(t, "templates/components/invoice-row.html"),
	))

	t.Run("invoice-row with ProviderMark MP", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:               "inv-1",
			CardColor:        "#009EE3",
			CardProviderMark: "MP",
			CardIcon:         "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row-legacy", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "MP")
		assertContains(t, html, "emblem-len-2")
		assertContains(t, html, "emblem-text")
	})

	t.Run("invoice-row with ProviderMark INTER", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:               "inv-2",
			CardColor:        "#FF7A00",
			CardProviderMark: "INTER",
			CardIcon:         "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row-legacy", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, "INTER")
		assertContains(t, html, "emblem-len-5")
		assertContains(t, html, "emblem-text")
	})

	t.Run("invoice-row with empty ProviderMark fallback", func(t *testing.T) {
		var buf bytes.Buffer
		data := InvoiceRow{
			ID:               "inv-3",
			CardColor:        "#6B7280",
			CardProviderMark: "",
			CardIcon:         "credit-card",
		}
		if err := tplInvoice.ExecuteTemplate(&buf, "invoice-row-legacy", data); err != nil {
			t.Fatalf("failed to execute invoice-row: %v", err)
		}
		html := buf.String()
		if strings.Contains(html, "emblem-text") {
			t.Errorf("expected no emblem-text for empty ProviderMark, got: %s", html)
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
			ID:                  "tx-123",
			PaymentConfirm:      "Marcar este lançamento como pago?",
			PaymentConfirmTitle: "Confirmar pagamento",
			PaymentIcon:         "clock",
			PaymentTitle:        "Pendente — toque para marcar como pago",
			IsPending:           true,
		}
		if err := tplRow.ExecuteTemplate(&buf, "lancamento-row", data); err != nil {
			t.Fatalf("failed to execute lancamento-row: %v", err)
		}
		html := buf.String()
		assertContains(t, html, `hx-confirm="Marcar este lançamento como pago?"`)
		assertContains(t, html, `hx-post="/transacoes/tx-123/status-pagamento"`)
		assertContains(t, html, `data-lucide="clock"`)
	})
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
