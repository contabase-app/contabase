package handlers

// R1.3-D5-SEC-R2-RED — A2: exclusão de categorias destrói vínculos financeiros.
//
// Defeito (D5-SEC-A): HandleCategoriasDelete executa DELETE cru em categories
// e depende apenas das FKs do schema:
//   - transactions.category_id     ON DELETE SET NULL  → histórico perde a categoria;
//   - recurring_rules.category_id  ON DELETE SET NULL  → regra ativa fica sem categoria;
//   - cost_limits.category_id      ON DELETE CASCADE   → o limite é apagado junto;
//   - categories.parent_id         ON DELETE CASCADE   → descendentes são apagados.
// A única proteção real é a FK RESTRICT de investment_result_classifications.
// Além disso, toda rejeição é renderizada como HTTP 200 com flash, violando o
// contrato de status (409 para vínculo, 404 para inexistente/estrangeiro).
//
// Contrato esperado (fica GREEN após a correção A2):
//   - categoria com transação histórica, regra recorrente (ativa ou inativa),
//     limite, classificação ou descendente utilizada → 409, sem SET NULL e sem
//     CASCADE (nenhum vínculo destruído);
//   - categoria pai com subcategoria (mesmo vazia) → 409 (não destruir
//     descendentes silenciosamente);
//   - categoria inexistente ou de outro workspace → 404;
//   - categoria sem dependências → excluída com sucesso (200);
//   - mensagem de rejeição nunca afirma sucesso ("Categoria removida.");
//   - reseed administrativo continua preservando categorias com uso;
//   - o mesmo contrato vale em todos os workspaces.
//
// Hoje estes testes falham de forma determinística (RED) exibindo o defeito.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

type categoryDeleteFixture struct {
	db *sql.DB
	a  *ConfiguracoesHandler // workspace ws-a
	b  *ConfiguracoesHandler // workspace ws-b
}

func seedCategoryDeleteScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-1', 'User 1', 'u1@test.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-a', 'user-1', 'ADMIN', ?), ('ws-b', 'user-1', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('acc-a', 'ws-a', 'Conta A', 'CHECKING', 100000, 100000, ?, ?),
			('acc-b', 'ws-b', 'Conta B', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES
			('cat-tx-a', 'ws-a', 'Mercado', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-rule-a', 'ws-a', 'Assinaturas', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-rule-inact-a', 'ws-a', 'Antiga', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-limit-a', 'ws-a', 'Lazer', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-parent-a', 'ws-a', 'Casa', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-child-used-a', 'ws-a', 'Manutenção', 'tag', '#6b7280', 'EXPENSE', 'Essencial', 'cat-parent-a', ?),
			('cat-parent2-a', 'ws-a', 'Educação', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-child-empty-a', 'ws-a', 'Cursos', 'tag', '#6b7280', 'EXPENSE', 'Essencial', 'cat-parent2-a', ?),
			('cat-free-a', 'ws-a', 'Sobrando', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			('cat-tx-b', 'ws-b', 'Mercado B', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?)
	`, now, now, now, now, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('tx-hist-a', 'ws-a', 'user-1', 'acc-a', 'cat-tx-a', 'EXPENSE', 1000, ?, 'Compra histórica', 'paid', 1, 1, ?, ?),
			('tx-child-a', 'ws-a', 'user-1', 'acc-a', 'cat-child-used-a', 'EXPENSE', 2000, ?, 'Compra filha', 'paid', 1, 1, ?, ?),
			('tx-hist-b', 'ws-b', 'user-1', 'acc-b', 'cat-tx-b', 'EXPENSE', 3000, ?, 'Compra B', 'paid', 1, 1, ?, ?)
	`, now, now, now, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES
			('rule-active', 'ws-a', 'user-1', 'acc-a', 'cat-rule-a', 'EXPENSE', 5000, 'Regra ativa', ?, 'MONTHLY', 'PAID', 1, ?, ?),
			('rule-inactive', 'ws-a', 'user-1', 'acc-a', 'cat-rule-inact-a', 'EXPENSE', 7000, 'Regra inativa', ?, 'MONTHLY', 'PAID', 0, ?, ?)
	`, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold)
		VALUES ('limit-a', 'ws-a', 'cat-limit-a', 50000, 0)
	`)
}

func newCategoryDeleteFixture(t *testing.T) categoryDeleteFixture {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedCategoryDeleteScenario(t, db)
	return categoryDeleteFixture{
		db: db,
		a:  newConfigHandlerForWS(t, db, "ws-a"),
		b:  newConfigHandlerForWS(t, db, "ws-b"),
	}
}

func requestCategoryDelete(h *ConfiguracoesHandler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/configuracoes/categorias/"+id, nil)
	rr := httptest.NewRecorder()
	h.HandleCategoriasDelete(rr, req, id)
	return rr
}

func requireDeleteRejectedConflict(t *testing.T, rr *httptest.ResponseRecorder, db *sql.DB, categoryID string) {
	t.Helper()
	if rr.Code != http.StatusConflict {
		t.Fatalf("RED A2: exclusão de categoria com vínculos deve responder 409; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	assertCategoryExists(t, db, categoryID)
	if body := rr.Body.String(); containsSuccessFlash(body) {
		t.Fatalf("resposta de rejeição não pode afirmar sucesso: %.300s", body)
	}
}

func containsSuccessFlash(body string) bool {
	return strings.Contains(body, "Categoria removida.")
}

func requireCategoryLinkIntact(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	var count int
	if err := db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatalf("query vínculo (%s): %v", query, err)
	}
	if count != 1 {
		t.Fatalf("vínculo destruído (%s): %d, esperado 1", query, count)
	}
}

// ---------------------------------------------------------------------------
// RED — vínculos financeiros
// ---------------------------------------------------------------------------

// Categoria usada por transação histórica: hoje o DELETE passa e o histórico
// perde a categoria (SET NULL) sem qualquer aviso destrutivo real.
func TestCategoryDeleteWithHistoricalTransactionReturns409AndPreservesLink(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-tx-a")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-tx-a")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM transactions WHERE id = 'tx-hist-a' AND category_id = 'cat-tx-a'`)
}

// Regra recorrente ATIVA: hoje a regra sobrevive sem categoria (SET NULL).
func TestCategoryDeleteWithActiveRecurringRuleReturns409AndPreservesLink(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-rule-a")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-rule-a")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM recurring_rules WHERE id = 'rule-active' AND category_id = 'cat-rule-a' AND active = 1`)
}

// Regra recorrente INATIVA: o vínculo histórico importa tanto quanto o ativo.
func TestCategoryDeleteWithInactiveRecurringRuleReturns409AndPreservesLink(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-rule-inact-a")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-rule-inact-a")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM recurring_rules WHERE id = 'rule-inactive' AND category_id = 'cat-rule-inact-a' AND active = 0`)
}

// Limite de gasto: hoje o CASCADE apaga o limite junto com a categoria.
func TestCategoryDeleteWithCostLimitReturns409AndPreservesLimit(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-limit-a")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-limit-a")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM cost_limits WHERE id = 'limit-a' AND category_id = 'cat-limit-a'`)
}

// Follow the C2B service fixture: reference -> integration -> settlement ->
// application -> classification. No audit rows or protected links are forged.
func seedCategoryInvestmentClassification(t *testing.T, p *database.RuntimeDBs) services.InvestmentResultClassification {
	t.Helper()
	ctx := context.Background()
	must := func(stage string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("setup %s: %v", stage, err)
		}
	}
	seedCategoryDeleteScenario(t, p.DB)
	execTestSQL(t, p.Immediate, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance) VALUES('inv-account','ws-a','Corretora sintética','INVESTMENT',500000,500000)`)
	execTestSQL(t, p.Immediate, `INSERT INTO categories(id,workspace_id,name,type,macro_group) VALUES('cat-investment-result','ws-a','Resultado realizado','INCOME','Receitas Financeiras')`)
	inv, err := services.CreateInvestment(ctx, p.Immediate, services.CreateInvestmentInput{
		WorkspaceID: "ws-a", AccountID: "inv-account", CreatedBy: "user-1",
		Name: "CDB sintético", Institution: "Corretora sintética", Kind: "CDB",
		PrincipalCents: 500000, TaxRegime: models.InvestmentTaxRegimeIRRegressivo, AppliedDate: "2026-01-15",
	})
	must("investment", err)
	rootPosition, err := services.UpsertInvestmentValuation(ctx, p.Immediate, services.UpsertInvestmentValuationInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-1", ValuationDate: "2026-02-01", AmountCents: 500000,
	})
	must("root position", err)
	candidate, err := services.ReadInvestmentReferenceCandidate(ctx, p.DB, "ws-a", inv.ID, rootPosition.ID)
	must("reference candidate", err)
	principal := int64(500000)
	root, err := services.RecordInvestmentReference(ctx, p.Immediate, services.RecordInvestmentReferenceInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, AccountID: "inv-account", PositionID: rootPosition.ID,
		CreatedBy: "user-1", RequestID: uuid.NewString(), Reason: "Conciliação sintética explícita",
		CoverageToken: candidate.CoverageToken, RepresentedCents: &principal, Confirm: true,
	})
	must("reference", err)
	integrate := func(positionID string, version int64) services.InvestmentIntegrationResult {
		t.Helper()
		c, err := services.ReadInvestmentReferenceCandidate(ctx, p.DB, "ws-a", inv.ID, positionID)
		must("integration candidate", err)
		r, err := services.IntegrateInvestmentPosition(ctx, p.Immediate, services.IntegrateInvestmentPositionInput{
			WorkspaceID: "ws-a", InvestmentID: inv.ID, AccountID: "inv-account", ReferenceRevisionID: root.ID,
			PositionID: positionID, CoverageToken: c.CoverageToken, CreatedBy: "user-1", RequestID: uuid.NewString(),
			Reason: "Posição sintética conciliada", ExpectedVersion: version, DayClosed: true,
		})
		must("integration", err)
		return r
	}
	integrate(rootPosition.ID, 0)
	position, err := services.UpsertInvestmentValuation(ctx, p.Immediate, services.UpsertInvestmentValuationInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-1", ValuationDate: "2026-03-01", AmountCents: 530000,
	})
	must("position", err)
	financial := integrate(position.ID, 1)
	withdrawal, err := services.CreateInvestmentWithdrawal(ctx, p.Immediate, services.CreateInvestmentWithdrawalInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-1", RequestID: uuid.NewString(),
		WithdrawalDate: "2026-03-15", Mode: services.InvestmentWithdrawalTotal, ReceivedCents: 525500,
		Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + inv.ID, ReceivedCents: 525500, PrincipalCents: &principal}},
	})
	must("withdrawal", err)
	gross, tax, zero := int64(530000), int64(4500), int64(0)
	settlement, err := services.RecordInvestmentSettlement(ctx, p.Immediate, services.RecordInvestmentSettlementInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, WithdrawalID: withdrawal.ID, AccountID: "inv-account", DestinationAccountID: "inv-account",
		EconomicDate: withdrawal.WithdrawalDate, CreatedBy: "user-1", RequestID: uuid.NewString(), Reason: "Extrato sintético de liquidação",
		GrossCents: &gross, IRCents: &tax, IOFCents: &zero, FeesCents: &zero, NetCents: 525500, PatrimonyCents: &gross,
		PatrimonyEvidence: "Parcela patrimonial comprovada no extrato sintético", ConfirmReceipt: true, ReceiptEvidence: "Valor líquido disponível na corretora sintética",
	})
	must("settlement", err)
	var balanceVersion int64
	must("balance version", p.DB.QueryRow(`SELECT balance_version FROM accounts WHERE id='inv-account'`).Scan(&balanceVersion))
	application, err := services.ApplyInvestmentSettlement(ctx, p.Immediate, services.ApplyInvestmentSettlementInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, WithdrawalID: withdrawal.ID, SettlementRevisionID: settlement.ID,
		ReferenceRevisionID: root.ID, FinancialRevisionID: financial.ID, ExpectedSettlementVersion: settlement.Version,
		ExpectedFinancialVersion: financial.Version, ExpectedBalanceVersion: balanceVersion, ExpectedDestinationVersion: balanceVersion,
		CreatedBy: "user-1", RequestID: uuid.NewString(), Reason: "Liquidação sintética comprovada",
		ReconciliationEvidence: "Extratos sintéticos conferidos; sem efeitos equivalentes prévios",
	})
	must("application", err)
	classification, err := services.ClassifyInvestmentSettlementResult(ctx, p.Immediate, services.ClassifyInvestmentSettlementInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, WithdrawalID: withdrawal.ID, SettlementRevisionID: settlement.ID,
		ApplicationID: application.ID, ExpectedSettlementVersion: settlement.Version, CreatedBy: "user-1", RequestID: uuid.NewString(),
		CategoryID: "cat-investment-result", LegacyReconciliationEvidence: "Extratos sintéticos conferidos; sem reconhecimento gerencial prévio de resultado, IR, IOF ou taxas",
	})
	must("classification", err)
	if classification.State != services.InvestmentClassificationCompleted || classification.Nature != "REALIZED_GAIN" || classification.TransactionID == "" || classification.ResultCents == nil || *classification.ResultCents != 25500 {
		t.Fatalf("setup: classificação inesperada: %+v", classification)
	}
	return classification
}

func TestCategoryDeleteWithInvestmentResultClassificationReturns409AndPreservesLinks(t *testing.T) {
	p, err := database.OpenRuntimeDBs(filepath.Join(t.TempDir(), "category-investment.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	for _, db := range []*sql.DB{p.DB, p.Immediate} {
		if enabled := countRows(t, db, `PRAGMA foreign_keys`); enabled != 1 {
			t.Fatal("setup: foreign keys devem permanecer ativas")
		}
	}
	classification := seedCategoryInvestmentClassification(t, p)
	h := newConfigHandlerForWS(t, p.DB, "ws-a")
	tables := []string{"categories", "accounts", "transactions", "transaction_status_history", "recurring_rules", "recurring_series", "cost_limits",
		"account_balance_adjustments", "investments", "investment_valuations", "investment_purchases", "investment_withdrawals",
		"investment_withdrawal_allocations", "investment_reference_revisions", "investment_financial_revisions", "investment_financial_adjustments",
		"investment_purchase_funding", "investment_settlement_revisions", "investment_settlement_receipts", "investment_settlement_applications",
		"investment_settlement_prior_effects", "investment_result_classifications"}
	snapshot := func() string {
		var out strings.Builder
		for _, table := range tables {
			out.WriteString(recurringAll4BB2RowsSnapshot(t, p.DB, table, "SELECT * FROM "+table+" ORDER BY rowid"))
		}
		return out.String()
	}
	requireCategoryLinkIntact(t, p.DB, `SELECT COUNT(*) FROM investment_result_classifications WHERE id=? AND category_id='cat-investment-result' AND transaction_id=?`, classification.ID, classification.TransactionID)
	before := snapshot()
	rr := requestCategoryDelete(h, "cat-investment-result")
	// RESTRICT already preserves the graph today; the RED is the false HTTP 200.
	assertCategoryExists(t, p.DB, "cat-investment-result")
	requireCategoryLinkIntact(t, p.DB, `SELECT COUNT(*) FROM investment_result_classifications WHERE id=? AND category_id='cat-investment-result' AND transaction_id=?`, classification.ID, classification.TransactionID)
	requireCategoryLinkIntact(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE id=? AND category_id='cat-investment-result' AND effect_mode='CLASSIFICATION_ONLY' AND amount=25500`, classification.TransactionID)
	if after := snapshot(); after != before {
		t.Error("exclusão alterou categoria, classificação, dependentes ou estado financeiro")
	}
	if containsSuccessFlash(rr.Body.String()) {
		t.Error("resposta de rejeição afirmou sucesso")
	}
	if rr.Code != http.StatusConflict {
		t.Errorf("RED A2: categoria com classificação de investimento deve responder 409 com grafo preservado; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
}

// Pai com descendente UTILIZADO: hoje o CASCADE apaga pai e filho e o SET NULL
// destrói o vínculo da transação com o filho.
func TestCategoryDeleteWithUsedDescendantReturns409AndPreservesDescendant(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-parent-a")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-parent-a")
	assertCategoryExists(t, f.db, "cat-child-used-a")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM categories WHERE id = 'cat-child-used-a' AND parent_id = 'cat-parent-a'`)
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM transactions WHERE id = 'tx-child-a' AND category_id = 'cat-child-used-a'`)
}

// Pai com subcategoria VAZIA (sem uso): destruir descendentes em cascata
// silenciosa não é aceitável; a exclusão deve ser recusada enquanto houver
// filhos (usuário decide primeiro o que fazer com eles).
func TestCategoryDeleteWithEmptySubcategoryReturns409AndPreservesDescendant(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-parent2-a")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-parent2-a")
	assertCategoryExists(t, f.db, "cat-child-empty-a")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM categories WHERE id = 'cat-child-empty-a' AND parent_id = 'cat-parent2-a'`)
}

// O mesmo contrato se repete no workspace B (o defeito não é específico de um
// tenant).
func TestCategoryDeleteCrossWorkspaceTransactionDependencyReturns409(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.b, "cat-tx-b")

	requireDeleteRejectedConflict(t, rr, f.db, "cat-tx-b")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM transactions WHERE id = 'tx-hist-b' AND category_id = 'cat-tx-b'`)
}

// ---------------------------------------------------------------------------
// RED — contrato de status 404
// ---------------------------------------------------------------------------

func TestCategoryDeleteMissingReturns404(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-nope")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("RED A2: categoria inexistente deve responder 404; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
}

// Categoria de outro workspace: 404 (sem vazar existência) e intacta no dono.
func TestCategoryDeleteForeignWorkspaceCategoryReturns404AndPreserves(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-tx-b")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("RED A2: categoria estrangeira deve responder 404; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	assertCategoryExists(t, f.db, "cat-tx-b")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM transactions WHERE id = 'tx-hist-b' AND category_id = 'cat-tx-b'`)
}

// ---------------------------------------------------------------------------
// RED — categoria rejeitada continua utilizável (proxy de concorrência)
// ---------------------------------------------------------------------------

// Após a rejeição, a categoria precisa continuar aceitando novos vínculos: o
// guarda-chuva da exclusão não pode deixar a categoria em estado inutilizável.
// (Concorrência real delete×vinculação exige hooks de serialização que não
// existem no handler; ver lacunas no relatório R2-RED.)
func TestRejectedDeleteKeepsCategoryLinkable(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-tx-a")
	requireDeleteRejectedConflict(t, rr, f.db, "cat-tx-a")

	now := time.Now().Unix()
	execTestSQL(t, f.db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-after-reject', 'ws-a', 'user-1', 'acc-a', 'cat-tx-a', 'EXPENSE', 1500, ?, 'Compra pós-rejeição', 'paid', 1, 1, ?, ?)
	`, now, now, now)
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM transactions WHERE id = 'tx-after-reject' AND category_id = 'cat-tx-a'`)
}

// ---------------------------------------------------------------------------
// PASS — controles
// ---------------------------------------------------------------------------

func TestCategoryDeleteWithoutDependenciesSucceeds(t *testing.T) {
	f := newCategoryDeleteFixture(t)

	rr := requestCategoryDelete(f.a, "cat-free-a")

	if rr.Code != http.StatusOK {
		t.Fatalf("categoria sem dependências deve ser excluída; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	assertCategoryNotExists(t, f.db, "cat-free-a")
	if !containsSuccessFlash(rr.Body.String()) {
		t.Fatalf("corpo sem confirmação de exclusão: %.300s", rr.Body.String())
	}
}

// O reseed administrativo continua preservando categorias com uso (contrato
// público de category-reseed). A correção A2 não pode enfraquecê-lo.
func TestAdminReseedPreservesCategoriesWithUsage(t *testing.T) {
	f := newCategoryDeleteFixture(t)
	now := time.Now().Unix()
	execTestSQL(t, f.db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-c', 'Workspace C', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, f.db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-c', 'user-1', 'ADMIN', ?)
	`, now)
	execTestSQL(t, f.db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-c', 'ws-c', 'Conta C', 'CHECKING', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, f.db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('cat-used-c', 'ws-c', 'Personalizada Usada', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?),
			('cat-orphan-c', 'ws-c', 'Personalizada Orfa', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?)
	`, now, now)
	execTestSQL(t, f.db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('tx-c', 'ws-c', 'user-1', 'acc-c', 'cat-used-c', 'EXPENSE', 1000, ?, 'Compra C', 'paid', 1, 1, ?, ?)
	`, now, now, now)

	report, err := database.ApplyWorkspaceCategoryReseed(f.db, "ws-c")
	if err != nil {
		t.Fatalf("apply category reseed: %v", err)
	}
	assertCategoryExists(t, f.db, "cat-used-c")
	requireCategoryLinkIntact(t, f.db, `SELECT COUNT(*) FROM transactions WHERE id = 'tx-c' AND category_id = 'cat-used-c'`)
	assertCategoryNotExists(t, f.db, "cat-orphan-c")
	_ = report
}
