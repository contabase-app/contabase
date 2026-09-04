package handlers

import (
	"bytes"
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/categorycontract"
)

type contabilSmokeExpectations struct {
	OperationalResult      int64
	CashResult             int64
	FinancialRevenue       int64
	Withdrawals            int64
	PatrimonialInvestments int64
	MainClosingBalance     int64
	ReserveClosingBalance  int64
	TotalClosingBalance    int64
}

func TestRelatoriosContabilSmokeBusinessAllMacrosJuly2026(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	want := seedRelatoriosContabilSmokeBusinessJuly2026(t, db)
	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-contabil-smoke", UserID: "u-contabil-smoke"}

	data, err := handler.buildRelatoriosDataWithModo("", "contabil", "movimentos", "realizado", 7, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}

	if !data.IsBusiness {
		t.Fatal("fixture should render Business accounting report")
	}
	if !data.HasContabilDREOperacional {
		t.Fatal("expected operational DRE row")
	}
	if got := data.ContabilDREOperacional.RawResultado; got != want.OperationalResult {
		t.Fatalf("operational result = %d, want %d", got, want.OperationalResult)
	}
	if !data.HasContabilCaixaAtual {
		t.Fatal("expected realized cash row")
	}
	if got := data.ContabilCaixaAtual.RawResultado; got != want.CashResult {
		t.Fatalf("realized cash result = %d, want %d", got, want.CashResult)
	}
	if got := sumRendimentosFinanceiros(data.ContabilRendimentos); got != want.FinancialRevenue {
		t.Fatalf("financial revenues = %d, want %d", got, want.FinancialRevenue)
	}
	if got := data.ContabilProLabore.rawCents(); got != want.Withdrawals {
		t.Fatalf("withdrawals = %d, want %d", got, want.Withdrawals)
	}
	if got := data.ContabilInvestimentosPatrimonialRaw; got != want.PatrimonialInvestments {
		t.Fatalf("patrimonial investments = %d, want %d", got, want.PatrimonialInvestments)
	}

	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroReceitasOperacionais, 500000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroReceitasFinanceiras, 12000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroOutrasEntradas, 30000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroDeducoesImpostos, -40000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroCustosOperacionais, -70000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroDespesasAdministrativas, -25000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroDespesasComerciais, -80000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroEquipePrestadores, -90000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroFinanceiro, -8000)
	assertDREMacroAmount(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroOutrasSaidas, -15000)
	assertNoDREMacro(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroRetiradasDistribuicao)
	assertNoDREMacro(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroInvestimentosPatrimonial)
	assertNoDREMacro(t, data.ContabilDREOperacional.MacroGroups, categorycontract.MacroInvestimentosOutros)

	assertBusinessMacroAmount(t, data.ContabilReceitasMacro, categorycontract.MacroReceitasOperacionais, 500000)
	assertBusinessMacroAmount(t, data.ContabilReceitasMacro, categorycontract.MacroReceitasFinanceiras, 12000)
	assertBusinessMacroAmount(t, data.ContabilReceitasMacro, categorycontract.MacroOutrasEntradas, 30000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroDeducoesImpostos, 40000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroCustosOperacionais, 70000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroDespesasAdministrativas, 25000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroDespesasComerciais, 80000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroEquipePrestadores, 90000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroFinanceiro, 8000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroRetiradasDistribuicao, 60000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroInvestimentosPatrimonial, 110000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroOutrasSaidas, 15000)
	assertBusinessMacroAmount(t, data.ContabilDespesasMacro, categorycontract.MacroInvestimentosOutros, 22000)

	assertCaixaMacroAmount(t, data.ContabilCaixaAtual.MacroGroups, categorycontract.MacroReceitasOperacionais, 500000)
	assertCaixaMacroAmount(t, data.ContabilCaixaAtual.MacroGroups, categorycontract.MacroReceitasFinanceiras, 12000)
	assertCaixaMacroAmount(t, data.ContabilCaixaAtual.MacroGroups, categorycontract.MacroOutrasEntradas, 30000)
	assertCaixaMacroAmount(t, data.ContabilCaixaAtual.MacroGroups, categorycontract.MacroDespesasComerciais, -35000)
	assertCaixaMacroAmount(t, data.ContabilCaixaAtual.MacroGroups, "Fatura de cartão", -45000)

	assertAccountClosingBalance(t, data.ContabilSaldosContas, "Conta Movimento Smoke", want.MainClosingBalance)
	assertAccountClosingBalance(t, data.ContabilSaldosContas, "Reserva Smoke", want.ReserveClosingBalance)

	if got := data.ContabilSaldoTotalRaw; got != want.TotalClosingBalance {
		t.Fatalf("total reconstructed balance = %d, want %d", got, want.TotalClosingBalance)
	}

	if data.HasContabilMissingReceitasFinanceiras {
		t.Fatal("Receitas Financeiras exists in fixture and should not trigger missing category alert")
	}
	if !data.HasContabilInvestimentosOutrosLegacy {
		t.Fatal("legacy Investimentos/Outros movement should trigger review alert")
	}
	if !data.HasContabilOutrasEntradasMovimento || !data.HasContabilOutrasSaidasMovimento {
		t.Fatal("generic Outras Entradas/Saidas movements should trigger generic review alert")
	}

	assertContabilSmokeTela(t, data)
	assertContabilSmokePDF(t, handler)
	assertContabilSmokeNoTransactionsWereReclassified(t, db)
}

func seedRelatoriosContabilSmokeBusinessJuly2026(t *testing.T, db *sql.DB) contabilSmokeExpectations {
	t.Helper()

	now := time.Now().Unix()
	july := func(day int) int64 {
		return testUnixDate("2026-07-" + twoDigit(day))
	}
	august3 := testUnixDate("2026-08-03")

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-contabil-smoke', 'Contabil Smoke', 'contabil-smoke@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, company_name, cnpj_cpf, address, phone, created_at, updated_at)
		VALUES ('ws-contabil-smoke', 'Workspace Contabil Smoke', 'business', 'ContaBase Smoke Ltda', '11.222.333/0001-44', 'Rua Julho, 2026', '(11) 4002-2026', ?, ?)
	`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-contabil-smoke', 'u-contabil-smoke', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES
		('acc-smoke-main', 'ws-contabil-smoke', 'Conta Movimento Smoke', 'CHECKING', 1000000, 1102000, ?, ?),
		('acc-smoke-reserve', 'ws-contabil-smoke', 'Reserva Smoke', 'SAVINGS', 200000, 220000, ?, ?),
		('acc-smoke-card', 'ws-contabil-smoke', 'Cartao Smoke', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now, now, now, now, now)

	seedContabilSmokeCategories(t, db, now)

	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_at, paid_amount, created_at) VALUES
		('inv-smoke-card-2026-07', 'acc-smoke-card', '2026-07', ?, ?, 'PAID', ?, 45000, ?)
	`, july(20), july(25), july(25), now)

	insertSmokeTransaction(t, db, "tx-smoke-revenue", "acc-smoke-main", "cat-smoke-rev-op", "", "INCOME", 500000, july(5), july(5), "Receita operacional paga")
	insertSmokeTransaction(t, db, "tx-smoke-fin-revenue", "acc-smoke-main", "cat-smoke-rev-fin", "", "INCOME", 12000, july(6), july(6), "Rendimentos financeiros")
	insertSmokeTransaction(t, db, "tx-smoke-other-in", "acc-smoke-main", "cat-smoke-other-in", "", "INCOME", 30000, july(7), july(7), "Outras entradas")
	insertSmokeTransaction(t, db, "tx-smoke-tax", "acc-smoke-main", "cat-smoke-tax", "", "EXPENSE", 40000, july(8), july(8), "Impostos sobre receita")
	insertSmokeTransaction(t, db, "tx-smoke-costs", "acc-smoke-main", "cat-smoke-costs", "", "EXPENSE", 70000, july(9), july(9), "Custo operacional")
	insertSmokeTransaction(t, db, "tx-smoke-admin", "acc-smoke-main", "cat-smoke-admin", "", "EXPENSE", 25000, july(10), july(10), "Despesa administrativa")
	insertSmokeTransaction(t, db, "tx-smoke-sales", "acc-smoke-main", "cat-smoke-sales", "", "EXPENSE", 35000, july(11), july(11), "Despesa comercial")
	insertSmokeTransaction(t, db, "tx-smoke-card-purchase", "acc-smoke-card", "cat-smoke-sales", "inv-smoke-card-2026-07", "EXPENSE", 45000, july(12), july(12), "Compra no cartao")
	insertSmokeTransaction(t, db, "tx-smoke-team", "acc-smoke-main", "cat-smoke-team", "", "EXPENSE", 90000, july(13), july(13), "Equipe e prestadores")
	insertSmokeTransaction(t, db, "tx-smoke-finance", "acc-smoke-main", "cat-smoke-finance", "", "EXPENSE", 8000, july(14), july(14), "Tarifa financeira")
	insertSmokeTransaction(t, db, "tx-smoke-withdrawal", "acc-smoke-main", "cat-smoke-withdrawal", "", "EXPENSE", 60000, july(15), july(15), "Retirada de socios")
	insertSmokeTransaction(t, db, "tx-smoke-invest", "acc-smoke-main", "cat-smoke-invest", "", "EXPENSE", 110000, july(16), july(16), "Investimento patrimonial")
	insertSmokeTransaction(t, db, "tx-smoke-other-out", "acc-smoke-main", "cat-smoke-other-out", "", "EXPENSE", 15000, july(17), july(17), "Outras saidas")
	insertSmokeTransaction(t, db, "tx-smoke-legacy-invest", "acc-smoke-main", "cat-smoke-legacy-invest", "", "EXPENSE", 22000, july(18), july(18), "Investimentos outros legado")
	insertSmokeTransaction(t, db, "tx-smoke-card-payment", "acc-smoke-main", "", "", "EXPENSE", 45000, july(25), july(25), "Pagamento fatura Smoke")
	insertSmokeTransfer(t, db, "tx-smoke-transfer", "acc-smoke-main", "acc-smoke-reserve", 20000, july(20), "Transferencia para reserva")
	insertSmokeTransaction(t, db, "tx-smoke-post-period", "acc-smoke-main", "cat-smoke-rev-op", "", "INCOME", 100000, august3, august3, "Receita depois do fechamento")

	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at) VALUES
		('ip-smoke-card-2026-07', 'ws-contabil-smoke', 'inv-smoke-card-2026-07', 'acc-smoke-main', 'tx-smoke-card-payment', 45000, ?, 'manual', 'u-contabil-smoke', ?)
	`, july(25), now)

	return contabilSmokeExpectations{
		OperationalResult:      214000,
		CashResult:             22000,
		FinancialRevenue:       12000,
		Withdrawals:            60000,
		PatrimonialInvestments: 110000,
		MainClosingBalance:     1002000,
		ReserveClosingBalance:  220000,
		TotalClosingBalance:    1222000,
	}
}

func seedContabilSmokeCategories(t *testing.T, db *sql.DB, now int64) {
	t.Helper()

	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES
		('cat-smoke-rev-op', 'ws-contabil-smoke', 'Receita operacional smoke', 'briefcase', '#22c55e', 'INCOME', ?, ?),
		('cat-smoke-rev-fin', 'ws-contabil-smoke', 'Rendimentos financeiros', 'trending-up', '#0ea5e9', 'INCOME', ?, ?),
		('cat-smoke-other-in', 'ws-contabil-smoke', 'Outras Entradas smoke', 'plus', '#14b8a6', 'INCOME', ?, ?),
		('cat-smoke-tax', 'ws-contabil-smoke', 'Impostos smoke', 'receipt', '#ef4444', 'EXPENSE', ?, ?),
		('cat-smoke-costs', 'ws-contabil-smoke', 'Custos smoke', 'factory', '#f97316', 'EXPENSE', ?, ?),
		('cat-smoke-admin', 'ws-contabil-smoke', 'Administrativas smoke', 'building', '#a855f7', 'EXPENSE', ?, ?),
		('cat-smoke-sales', 'ws-contabil-smoke', 'Comerciais smoke', 'megaphone', '#ec4899', 'EXPENSE', ?, ?),
		('cat-smoke-team', 'ws-contabil-smoke', 'Equipe smoke', 'users', '#6366f1', 'EXPENSE', ?, ?),
		('cat-smoke-finance', 'ws-contabil-smoke', 'Financeiro smoke', 'landmark', '#64748b', 'EXPENSE', ?, ?),
		('cat-smoke-withdrawal', 'ws-contabil-smoke', 'Retiradas smoke', 'wallet', '#f59e0b', 'EXPENSE', ?, ?),
		('cat-smoke-invest', 'ws-contabil-smoke', 'Investimentos patrimoniais smoke', 'hammer', '#0f766e', 'EXPENSE', ?, ?),
		('cat-smoke-other-out', 'ws-contabil-smoke', 'Outras Saidas smoke', 'minus', '#78716c', 'EXPENSE', ?, ?),
		('cat-smoke-legacy-invest', 'ws-contabil-smoke', 'Investimentos Outros legado smoke', 'archive', '#71717a', 'EXPENSE', ?, ?)
	`,
		categorycontract.MacroReceitasOperacionais, now,
		categorycontract.MacroReceitasFinanceiras, now,
		categorycontract.MacroOutrasEntradas, now,
		categorycontract.MacroDeducoesImpostos, now,
		categorycontract.MacroCustosOperacionais, now,
		categorycontract.MacroDespesasAdministrativas, now,
		categorycontract.MacroDespesasComerciais, now,
		categorycontract.MacroEquipePrestadores, now,
		categorycontract.MacroFinanceiro, now,
		categorycontract.MacroRetiradasDistribuicao, now,
		categorycontract.MacroInvestimentosPatrimonial, now,
		categorycontract.MacroOutrasSaidas, now,
		categorycontract.MacroInvestimentosOutros, now,
	)
}

func insertSmokeTransaction(t *testing.T, db *sql.DB, id, accountID, categoryID, invoiceID, txType string, amount, date, paidAt int64, description string) {
	t.Helper()

	categoryValue := any(categoryID)
	if categoryID == "" {
		categoryValue = nil
	}
	invoiceValue := any(invoiceID)
	if invoiceID == "" {
		invoiceValue = nil
	}
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES (?, 'ws-contabil-smoke', 'u-contabil-smoke', ?, ?, ?, ?, ?, ?, ?, 'paid', ?, 1, 1, ?, ?)
	`, id, accountID, categoryValue, invoiceValue, txType, amount, date, description, paidAt, now, now)
}

func insertSmokeTransfer(t *testing.T, db *sql.DB, id, accountID, destinationAccountID string, amount, date int64, description string) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
		VALUES (?, 'ws-contabil-smoke', 'u-contabil-smoke', ?, ?, 'TRANSFER', ?, ?, ?, 'paid', ?, 1, 1, ?, ?)
	`, id, accountID, destinationAccountID, amount, date, description, date, now, now)
}

func assertContabilSmokeTela(t *testing.T, data RelatoriosData) {
	t.Helper()

	tpl := testRelatoriosContabilTemplate(t)
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "relatorios-page", data); err != nil {
		t.Fatalf("template execute: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		"Relatório Contábil",
		"Julho 2026",
		"Resultado operacional por competência",
		"Caixa Realizado",
		"Entradas por Macro/Categoria",
		"Saídas por Macro/Categoria",
		"Receitas Financeiras",
		"Rendimentos financeiros",
		"Retiradas/Distribuição",
		"Investimentos/Patrimonial",
		"Saldo por conta em 31/07/2026",
		"Saldo total em 31/07/2026",
		"Conta Movimento Smoke",
		"Reserva Smoke",
		"R$ 2.140,00",
		"R$ 220,00",
		"R$ 120,00",
		"R$ 600,00",
		"R$ 1.100,00",
		"R$ 10.020,00",
		"R$ 2.200,00",
		"R$ 12.220,00",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("screen missing %q", want)
		}
	}
	for _, want := range []string{
		"Investimentos/Outros",
		"Grupos genéricos",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("screen should show alert %q", want)
		}
	}
	if strings.Contains(body, "Nenhuma categoria em Receitas Financeiras") {
		t.Fatal("screen should not show missing Receitas Financeiras alert")
	}
}

func assertContabilSmokePDF(t *testing.T, handler RelatoriosHandler) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/relatorios/contabil.pdf?month=2026-07", nil)
	rr := httptest.NewRecorder()
	handler.HandleExportarContabilPDF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", got)
	}
	if got := rr.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="relatorio-contabil-2026-07.pdf"`) {
		t.Fatalf("Content-Disposition = %q, want contabil filename", got)
	}

	body := rr.Body.String()
	decodedText := decodedPDFText(rr.Body.Bytes())
	if !strings.Contains(body, "%PDF-1.4") {
		t.Fatalf("PDF body missing header")
	}
	for _, want := range []string{
		"Relatorio Contabil",
		"Julho 2026",
		"ContaBase Smoke Ltda",
		"RESULTADO OPER.",
		"Caixa Realizado",
		"Receitas Financeiras",
		"Rendimentos financeiros",
		"Retiradas/Distribui",
		"Investimentos/Patrimonial",
		"Saldo por conta em 31/07/2026",
		"Conta Movimento Smoke",
		"Reserva Smoke",
		"R$ 2.140,00",
		"R$ 220,00",
		"R$ 120,00",
		"R$ 600,00",
		"R$ 1.100,00",
		"R$ 10.020,00",
		"R$ 2.200,00",
		"R$ 12.220,00",
		"Investimentos/Outros",
		"Outras Entradas/Saidas",
	} {
		if !strings.Contains(decodedText, want) {
			t.Fatalf("PDF decoded text missing %q\ntext:\n%s\nbody prefix: %s", want, decodedText, body[:min(len(body), 1600)])
		}
	}
	if strings.Contains(decodedText, "Nenhuma categoria em Receitas Financeiras") {
		t.Fatal("PDF should not show missing Receitas Financeiras alert")
	}
}

func assertContabilSmokeNoTransactionsWereReclassified(t *testing.T, db *sql.DB) {
	t.Helper()

	var withdrawalMacro, legacyMacro, paymentCategory sql.NullString
	if err := db.QueryRow(`
		SELECT c.macro_group
		FROM transactions t
		JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		WHERE t.id = 'tx-smoke-withdrawal'
	`).Scan(&withdrawalMacro); err != nil {
		t.Fatalf("query withdrawal macro: %v", err)
	}
	if err := db.QueryRow(`
		SELECT c.macro_group
		FROM transactions t
		JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		WHERE t.id = 'tx-smoke-legacy-invest'
	`).Scan(&legacyMacro); err != nil {
		t.Fatalf("query legacy macro: %v", err)
	}
	if err := db.QueryRow(`SELECT category_id FROM transactions WHERE id = 'tx-smoke-card-payment'`).Scan(&paymentCategory); err != nil {
		t.Fatalf("query invoice payment category: %v", err)
	}
	if withdrawalMacro.String != categorycontract.MacroRetiradasDistribuicao {
		t.Fatalf("withdrawal macro = %q, want %q", withdrawalMacro.String, categorycontract.MacroRetiradasDistribuicao)
	}
	if legacyMacro.String != categorycontract.MacroInvestimentosOutros {
		t.Fatalf("legacy investment macro = %q, want %q", legacyMacro.String, categorycontract.MacroInvestimentosOutros)
	}
	if paymentCategory.Valid {
		t.Fatalf("invoice payment category = %q, want NULL", paymentCategory.String)
	}
}

func testRelatoriosContabilTemplate(t *testing.T) *template.Template {
	t.Helper()

	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}
	funcs := template.FuncMap{
		"sub":             func(a, b int) int { return a - b },
		"colorStyle":      func(_, _ string) template.CSS { return "" },
		"dreMacroClass":   func(_ []DREMacroGroupAmount, _ string) string { return "" },
		"dreMacroValue":   dreMacroValue,
		"caixaMacroClass": func(_ []CaixaMacroGroupAmount, _ string) string { return "" },
		"caixaMacroValue": caixaMacroValue,
	}
	tpl := template.Must(template.New("relatorios-contabil-smoke").Funcs(funcs).Parse(string(raw)))
	addAppShellTestStubs(tpl)
	template.Must(tpl.New("seletor_meses").Parse("<nav>meses</nav>"))
	return tpl
}

func assertDREMacroAmount(t *testing.T, rows []DREMacroGroupAmount, macro string, want int64) {
	t.Helper()

	for _, row := range rows {
		if row.MacroGroup == macro {
			if row.RawAmount != want {
				t.Fatalf("DRE macro %q = %d, want %d", macro, row.RawAmount, want)
			}
			return
		}
	}
	t.Fatalf("DRE macro %q not found in %#v", macro, rows)
}

func assertNoDREMacro(t *testing.T, rows []DREMacroGroupAmount, macro string) {
	t.Helper()

	for _, row := range rows {
		if row.MacroGroup == macro {
			t.Fatalf("DRE macro %q should be absent, got %d", macro, row.RawAmount)
		}
	}
}

func assertBusinessMacroAmount(t *testing.T, rows []BusinessCostMacroRow, macro string, want int64) {
	t.Helper()

	for _, row := range rows {
		if row.MacroGroup == macro {
			if row.RawAmount != want {
				t.Fatalf("business macro %q = %d, want %d", macro, row.RawAmount, want)
			}
			return
		}
	}
	t.Fatalf("business macro %q not found in %#v", macro, rows)
}

func assertCaixaMacroAmount(t *testing.T, rows []CaixaMacroGroupAmount, macro string, want int64) {
	t.Helper()

	for _, row := range rows {
		if row.MacroGroup == macro {
			if row.RawAmount != want {
				t.Fatalf("cash macro %q = %d, want %d", macro, row.RawAmount, want)
			}
			return
		}
	}
	t.Fatalf("cash macro %q not found in %#v", macro, rows)
}

func assertNoCaixaMacro(t *testing.T, rows []CaixaMacroGroupAmount, macro string) {
	t.Helper()

	for _, row := range rows {
		if row.MacroGroup == macro && row.RawAmount != 0 {
			t.Fatalf("cash macro %q should not have realized value, got %d", macro, row.RawAmount)
		}
	}
}

func assertAccountClosingBalance(t *testing.T, rows []AccountBalance, accountName string, want int64) {
	t.Helper()

	for _, row := range rows {
		if row.Name == accountName {
			if row.RawBalance != want {
				t.Fatalf("closing balance %q = %d, want %d", accountName, row.RawBalance, want)
			}
			return
		}
	}
	t.Fatalf("account balance %q not found in %#v", accountName, rows)
}

func sumRendimentosFinanceiros(rows []ContabilRendimentoFinanceiro) int64 {
	var total int64
	for _, row := range rows {
		total += row.RawAmount
	}
	return total
}

func decodedPDFText(pdf []byte) string {
	var out strings.Builder
	marker := []byte(") Tj")
	searchFrom := 0
	for {
		relEnd := bytes.Index(pdf[searchFrom:], marker)
		if relEnd < 0 {
			break
		}
		end := searchFrom + relEnd
		start := pdfLiteralStart(pdf, end)
		if start >= 0 {
			out.WriteString(decodePDFLiteral(pdf[start+1 : end]))
			out.WriteByte('\n')
		}
		searchFrom = end + len(marker)
	}
	return out.String()
}

func pdfLiteralStart(pdf []byte, end int) int {
	for i := end - 1; i >= 0; i-- {
		if pdf[i] == '(' && !pdfEscaped(pdf, i) {
			return i
		}
	}
	return -1
}

func pdfEscaped(pdf []byte, idx int) bool {
	backslashes := 0
	for i := idx - 1; i >= 0 && pdf[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func decodePDFLiteral(in []byte) string {
	var out strings.Builder
	for i := 0; i < len(in); i++ {
		if in[i] != '\\' || i+1 >= len(in) {
			out.WriteByte(in[i])
			continue
		}
		next := in[i+1]
		switch next {
		case '\\', '(', ')':
			out.WriteByte(next)
			i++
		default:
			if i+3 < len(in) && isOctalDigit(in[i+1]) && isOctalDigit(in[i+2]) && isOctalDigit(in[i+3]) {
				value := (in[i+1]-'0')*64 + (in[i+2]-'0')*8 + (in[i+3] - '0')
				out.WriteRune(rune(value))
				i += 3
				continue
			}
			out.WriteByte(next)
			i++
		}
	}
	return out.String()
}

func isOctalDigit(b byte) bool {
	return b >= '0' && b <= '7'
}

func twoDigit(day int) string {
	if day < 10 {
		return "0" + string(rune('0'+day))
	}
	return string(rune('0'+day/10)) + string(rune('0'+day%10))
}
