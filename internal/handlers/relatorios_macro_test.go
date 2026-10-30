package handlers

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/categorycontract"
)

func seedMacroContabilScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('u-macro', 'Macro', 'macro@t.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('ws-macro', 'WS Macro', 'business', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-macro', 'u-macro', 'ADMIN', ?)
	`, now)

	// Categories with macro groups
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-receita-fin', 'ws-macro', 'Juros Aplicacao', 'INCOME', '`+categorycontract.MacroReceitasFinanceiras+`', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-receita-op', 'ws-macro', 'Consultoria', 'INCOME', '`+categorycontract.MacroReceitasOperacionais+`', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-retirada', 'ws-macro', 'Pro-labore Socio', 'EXPENSE', '`+categorycontract.MacroRetiradasDistribuicao+`', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-equipe', 'ws-macro', 'Salario Dev', 'EXPENSE', '`+categorycontract.MacroEquipePrestadores+`', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-invest', 'ws-macro', 'Compra Ativo', 'EXPENSE', '`+categorycontract.MacroInvestimentosPatrimonial+`', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-invest-legacy', 'ws-macro', 'Invest Antigo', 'EXPENSE', '`+categorycontract.MacroInvestimentosOutros+`', ?)`, now)

	// Account
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-macro', 'ws-macro', 'Conta Macro', 'CHECKING', 0, 0, ?, ?)`, now, now)
}

func TestContabilRendimentosFinanceirosMacroGroup(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	// Insert income with Receitas Financeiras macro.
	competenceDate := testUnixDate("2026-08-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-rf-1', 'ws-macro', 'u-macro', 'acc-macro', 'cat-receita-fin', 'INCOME', 50000, ?, 'Receita Financeira', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	rendimentos, err := handler.queryRendimentosFinanceiros(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryRendimentosFinanceiros: %v", err)
	}
	if len(rendimentos) == 0 {
		t.Fatalf("should find rendimentos via macro Receitas Financeiras")
	}
	if rendimentos[0].RawAmount != 50000 {
		t.Fatalf("rendimentos amount mismatch: want 50000, got %d", rendimentos[0].RawAmount)
	}
}

func TestContabilRendimentosNaoDependeDeNomeCategoria(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	// Category with arbitrary name but macro = Receitas Financeiras.
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-rf-arbitrary', 'ws-macro', 'Nome Qualquer', 'INCOME', '`+categorycontract.MacroReceitasFinanceiras+`', ?)`, now)

	competenceDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-rf-arb', 'ws-macro', 'u-macro', 'acc-macro', 'cat-rf-arbitrary', 'INCOME', 30000, ?, 'Nome Arbitrario', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	rendimentos, err := handler.queryRendimentosFinanceiros(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryRendimentosFinanceiros: %v", err)
	}

	total := int64(0)
	for _, r := range rendimentos {
		total += r.RawAmount
	}
	if total != 30000 {
		t.Fatalf("should find income in macro Receitas Financeiras regardless of category name: want 30000, got %d", total)
	}
}

func TestContabilRetiradasPorMacroNaoPorNome(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	competenceDate := testUnixDate("2026-08-20")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-ret-1', 'ws-macro', 'u-macro', 'acc-macro', 'cat-retirada', 'EXPENSE', 100000, ?, 'Pro-labore Socio', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	amount, err := handler.queryProLaboreAmount(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryProLaboreAmount: %v", err)
	}
	if amount != 100000 {
		t.Fatalf("retiradas should appear via macro Retiradas/Distribuição: want 100000, got %d", amount)
	}

	// Also check HasRetiradasDistribuicao flag via build.
	data, err := handler.buildRelatoriosDataWithModo("test-retiradas", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if !data.HasRetiradasDistribuicao {
		t.Fatalf("HasRetiradasDistribuicao should be true")
	}
}

func TestContabilProLaboreEquipePrestadoresNaoApareceComoRetirada(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	// Expense in Equipe e Prestadores (NOT Retiradas/Distribuição).
	competenceDate := testUnixDate("2026-08-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-equipe-1', 'ws-macro', 'u-macro', 'acc-macro', 'cat-equipe', 'EXPENSE', 200000, ?, 'Salario', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	amount, err := handler.queryProLaboreAmount(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryProLaboreAmount: %v", err)
	}
	if amount != 0 {
		t.Fatalf("Equipe e Prestadores should NOT appear as retirada: got %d", amount)
	}
}

func TestContabilInvestimentosPatrimonialSeparado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	competenceDate := testUnixDate("2026-08-10")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-inv-1', 'ws-macro', 'u-macro', 'acc-macro', 'cat-invest', 'EXPENSE', 150000, ?, 'Compra Ativo', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	amount, err := handler.queryInvestimentosPatrimonialAmount(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryInvestimentosPatrimonialAmount: %v", err)
	}
	if amount != 150000 {
		t.Fatalf("Investimentos/Patrimonial amount: want 150000, got %d", amount)
	}

	data, err := handler.buildRelatoriosDataWithModo("test-invest", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if !data.HasContabilInvestimentosPatrimonial {
		t.Fatalf("HasContabilInvestimentosPatrimonial should be true")
	}
	if data.ContabilInvestimentosPatrimonialRaw != 150000 {
		t.Fatalf("ContabilInvestimentosPatrimonialRaw mismatch: want 150000, got %d", data.ContabilInvestimentosPatrimonialRaw)
	}
}

func TestContabilInvestimentosOutrosLegacyAlert(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	competenceDate := testUnixDate("2026-08-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-legacy-1', 'ws-macro', 'u-macro', 'acc-macro', 'cat-invest-legacy', 'EXPENSE', 50000, ?, 'Invest Antigo', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	hasLegacy, err := handler.queryInvestimentosOutrosLegacyExists(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryInvestimentosOutrosLegacyExists: %v", err)
	}
	if !hasLegacy {
		t.Fatalf("should detect Investimentos/Outros legacy transactions")
	}

	data, err := handler.buildRelatoriosDataWithModo("test-legacy", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if !data.HasContabilInvestimentosOutrosLegacy {
		t.Fatalf("HasContabilInvestimentosOutrosLegacy should be true")
	}
}

func TestContabilInvestimentosOutrosLegacyNoAlertWithoutData(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	// No legacy transactions inserted.
	data, err := handler.buildRelatoriosDataWithModo("test-no-legacy", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if data.HasContabilInvestimentosOutrosLegacy {
		t.Fatalf("HasContabilInvestimentosOutrosLegacy should be false without data")
	}
}

func TestContabilBusinessOnly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	// Business workspace — contabil tab should be available.
	data, err := handler.buildRelatoriosDataWithModo("test-biz", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo business: %v", err)
	}
	if data.ActiveTab != "contabil" {
		t.Fatalf("business should keep contabil tab, got %s", data.ActiveTab)
	}
}

func TestContabilPersonalNaoExpoeDados(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-pers', 'Pers', 'p@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-pers', 'WS Pers', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-pers', 'u-pers', 'ADMIN', ?)`, now)

	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-pers", UserID: "u-pers"}

	data, err := handler.buildRelatoriosDataWithModo("test-pers", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo personal: %v", err)
	}
	// Personal should fallback to caixa.
	if data.ActiveTab != "caixa" {
		t.Fatalf("personal contabil should fallback to caixa, got %s", data.ActiveTab)
	}
	if data.HasContabilInvestimentosPatrimonial {
		t.Fatalf("personal should not have investimentos patrimonial data")
	}
	if data.HasContabilInvestimentosOutrosLegacy {
		t.Fatalf("personal should not have investimentos outros legacy flag")
	}
}

func TestContabilTemplateMostraMacroReference(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "Natureza Econômica") {
		t.Fatalf("template should reference macro/Natureza Econômica classification")
	}
	if !strings.Contains(body, "Retiradas/Distribuição") {
		t.Fatalf("template should use 'Retiradas/Distribuição' label")
	}
	if !strings.Contains(body, "Investimentos/Patrimonial") {
		t.Fatalf("template should have Investimentos/Patrimonial section")
	}
	if !strings.Contains(body, "Investimentos/Outros") {
		t.Fatalf("template should have Investimentos/Outros legacy alert")
	}
	if !strings.Contains(body, "Receitas Financeiras") {
		t.Fatalf("template should reference Receitas Financeiras macro")
	}
	if !strings.Contains(body, "fora do resultado operacional") {
		t.Fatalf("template should note Investments are outside operational result")
	}
	if !strings.Contains(body, "Resultado operacional") {
		t.Fatalf("template should show operational result label")
	}
	if !strings.Contains(body, "Ver DRE completa") {
		t.Fatalf("template should link to full DRE tab")
	}
	if strings.Contains(body, "Saldo atual para conferência") {
		t.Fatalf("template should not contain old saldo label")
	}
}

func TestFilterDREOperationalExcludesRetiradas(t *testing.T) {
	in := DRERow{
		Competencia: "2026-08",
		MacroGroups: []DREMacroGroupAmount{
			{MacroGroup: categorycontract.MacroReceitasOperacionais, RawAmount: 100000},
			{MacroGroup: categorycontract.MacroDeducoesImpostos, RawAmount: -15000},
			{MacroGroup: categorycontract.MacroCustosOperacionais, RawAmount: -30000},
			{MacroGroup: categorycontract.MacroRetiradasDistribuicao, RawAmount: -20000},
			{MacroGroup: categorycontract.MacroEquipePrestadores, RawAmount: -25000},
		},
	}
	out, err := filterDREOperational(in)
	if err != nil {
		t.Fatal(err)
	}
	// Operational result: 100000 - 15000 - 30000 - 25000 = 30000 (Retiradas excluded)
	if out.RawResultado != 30000 {
		t.Fatalf("operational result should exclude Retiradas: want 30000, got %d", out.RawResultado)
	}
	for _, mg := range out.MacroGroups {
		if mg.MacroGroup == categorycontract.MacroRetiradasDistribuicao {
			t.Fatalf("Retiradas should not appear in operational macro groups")
		}
	}
}

func TestFilterDREOperationalExcludesInvestimentos(t *testing.T) {
	in := DRERow{
		Competencia: "2026-08",
		MacroGroups: []DREMacroGroupAmount{
			{MacroGroup: categorycontract.MacroReceitasOperacionais, RawAmount: 100000},
			{MacroGroup: categorycontract.MacroInvestimentosPatrimonial, RawAmount: -40000},
			{MacroGroup: categorycontract.MacroDespesasAdministrativas, RawAmount: -10000},
			{MacroGroup: categorycontract.MacroInvestimentosOutros, RawAmount: -5000},
		},
	}
	out, err := filterDREOperational(in)
	if err != nil {
		t.Fatal(err)
	}
	// Operational result: 100000 - 10000 = 90000 (both investment macros excluded)
	if out.RawResultado != 90000 {
		t.Fatalf("operational result should exclude Investimentos: want 90000, got %d", out.RawResultado)
	}
	for _, mg := range out.MacroGroups {
		if mg.MacroGroup == categorycontract.MacroInvestimentosPatrimonial ||
			mg.MacroGroup == categorycontract.MacroInvestimentosOutros {
			t.Fatalf("Investimentos should not appear in operational macro groups")
		}
	}
}

func TestFilterDREOperationalIncludesReceitasFinanceiras(t *testing.T) {
	in := DRERow{
		Competencia: "2026-08",
		MacroGroups: []DREMacroGroupAmount{
			{MacroGroup: categorycontract.MacroReceitasFinanceiras, RawAmount: 15000},
			{MacroGroup: categorycontract.MacroReceitasOperacionais, RawAmount: 50000},
			{MacroGroup: categorycontract.MacroFinanceiro, RawAmount: -5000},
		},
	}
	out, err := filterDREOperational(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.RawResultado != 60000 {
		t.Fatalf("Receitas Financeiras should be in operational result: want 60000, got %d", out.RawResultado)
	}
	found := false
	for _, mg := range out.MacroGroups {
		if mg.MacroGroup == categorycontract.MacroReceitasFinanceiras {
			found = true
		}
	}
	if !found {
		t.Fatalf("Receitas Financeiras should be in operational macro groups")
	}
}

func TestFilterDREOperationalEmptyInEmptyOut(t *testing.T) {
	in := DRERow{Competencia: "2026-08"}
	out, err := filterDREOperational(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.RawResultado != 0 {
		t.Fatalf("empty input should produce empty output")
	}
	if len(out.MacroGroups) != 0 {
		t.Fatalf("empty input should have no macro groups")
	}
}

func TestContabilMissingReceitasFinanceirasAlert(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	// Delete the Receitas Financeiras category to simulate missing.
	db.Exec(`DELETE FROM categories WHERE macro_group = ?`, categorycontract.MacroReceitasFinanceiras)

	data, err := handler.buildRelatoriosDataWithModo("test-missing-rf", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if !data.HasContabilMissingReceitasFinanceiras {
		t.Fatalf("should alert missing Receitas Financeiras category")
	}
}

func TestContabilMissingReceitasFinanceirasNoAlertWhenExists(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	data, err := handler.buildRelatoriosDataWithModo("test-has-rf", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if data.HasContabilMissingReceitasFinanceiras {
		t.Fatalf("should NOT alert when Receitas Financeiras category exists")
	}
}

func TestContabilOutrasEntradasAlert(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-outras-e', 'ws-macro', 'Receita Generica', 'INCOME', '`+categorycontract.MacroOutrasEntradas+`', ?)`, now)

	competenceDate := testUnixDate("2026-08-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-outras-e', 'ws-macro', 'u-macro', 'acc-macro', 'cat-outras-e', 'INCOME', 10000, ?, 'Outras Entradas', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	has, err := handler.queryOutrasEntradasExists(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryOutrasEntradasExists: %v", err)
	}
	if !has {
		t.Fatalf("should detect Outras Entradas movements")
	}

	data, err := handler.buildRelatoriosDataWithModo("test-outras-e", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if !data.HasContabilOutrasEntradasMovimento {
		t.Fatalf("HasContabilOutrasEntradasMovimento should be true")
	}
}

func TestContabilOutrasSaidasAlert(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedMacroContabilScenario(t, db)

	ctx := context.Background()
	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-macro", UserID: "u-macro"}

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at)
		VALUES ('cat-outras-s', 'ws-macro', 'Saida Generica', 'EXPENSE', '`+categorycontract.MacroOutrasSaidas+`', ?)`, now)

	competenceDate := testUnixDate("2026-08-15")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-outras-s', 'ws-macro', 'u-macro', 'acc-macro', 'cat-outras-s', 'EXPENSE', 5000, ?, 'Outras Saidas', 'paid', ?, ?)
	`, competenceDate, competenceDate, competenceDate)

	monthStart := testUnixDate("2026-08-01")
	monthEnd := testUnixDate("2026-08-31")

	has, err := handler.queryOutrasSaidasExists(ctx, monthStart, monthEnd)
	if err != nil {
		t.Fatalf("queryOutrasSaidasExists: %v", err)
	}
	if !has {
		t.Fatalf("should detect Outras Saidas movements")
	}

	data, err := handler.buildRelatoriosDataWithModo("test-outras-s", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	if !data.HasContabilOutrasSaidasMovimento {
		t.Fatalf("HasContabilOutrasSaidasMovimento should be true")
	}
}

func TestContabilAlertsBusinessOnly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('u-alert', 'Alert', 'a@t.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-alert', 'WS Alert', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-alert', 'u-alert', 'ADMIN', ?)`, now)

	handler := &RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-alert", UserID: "u-alert"}

	data, err := handler.buildRelatoriosDataWithModo("test-alert-pers", "contabil", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosDataWithModo: %v", err)
	}
	// Personal should fallback to caixa and not show contabil alerts.
	if data.HasContabilMissingReceitasFinanceiras {
		t.Fatalf("personal should not show missing Receitas Financeiras alert")
	}
	if data.HasContabilOutrasEntradasMovimento || data.HasContabilOutrasSaidasMovimento {
		t.Fatalf("personal should not show generic group alert")
	}
}

func TestContabilTemplateHelpTextsPresent(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/configuracoes_categorias.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "Grupo Macro define como a categoria aparece nos relatórios") {
		t.Fatalf("create form should explain macro group purpose")
	}
	if !strings.Contains(body, "Receitas Financeiras") {
		t.Fatalf("create form should mention Receitas Financeiras")
	}
	if !strings.Contains(body, "fora do resultado operacional") {
		t.Fatalf("create form should explain operational exclusion")
	}
}

func TestContabilTemplateInlineEditHelpTextPresent(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/configuracoes_components.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "Grupo Macro define como a categoria aparece nos relatórios") {
		t.Fatalf("inline edit form should explain macro group purpose")
	}
}

func TestContabilTemplateAlertTextsPresent(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, "Nenhuma categoria em Receitas Financeiras") {
		t.Fatalf("template should have missing Receitas Financeiras alert")
	}
	if !strings.Contains(body, "Rendimentos podem não ser destacados.") {
		t.Fatalf("template should use shorter Receitas Financeiras alert copy")
	}
	if !strings.Contains(body, "Grupos genéricos em uso") {
		t.Fatalf("template should have generic groups alert")
	}
	if !strings.Contains(body, "Revise a classificação das categorias") {
		t.Fatalf("template should suggest reviewing classification")
	}
}
