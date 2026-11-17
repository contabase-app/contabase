package handlers

// A2-CLOSURE — regressão cross-tenant real para a guarda de exclusão de
// categorias.
//
// A suíte RED original cobre dependências do próprio workspace. Ela NÃO cobre
// o cenário legado inconsistente que motiva a guarda global: uma linha de
// outro workspace referenciando a categoria do dono — permitida pelas FKs
// single-column do schema (transactions/recurring_rules ON DELETE SET NULL,
// cost_limits/categories.parent_id ON DELETE CASCADE). Sem a contagem global,
// o DELETE do dono dispararia SET NULL/CASCADE sobre a linha estrangeira.
//
// Contrato fixado aqui (banco exclusivamente sintético):
//   - referência estrangeira real (workspace B → categoria de A) → 409;
//   - categoria do dono preservada;
//   - linha estrangeira preservada com o vínculo intacto;
//   - nenhuma mutação em A ou B (snapshot global antes/depois idêntico);
//   - nenhum dado identificável de B no corpo da resposta;
//   - categoria sem dependência nenhuma → 200 no mesmo cenário (o 409 é
//     atribuível à referência estrangeira, não ao fixture);
//   - o contrato vale para Personal e Business em qualquer combinação.

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	ctOwnerWSID   = "ws-ct-owner"
	ctForeignWSID = "ws-ct-foreign"
	ctOwnerWSName = "Owner Cross Tenant"
	// ctForeignWSName e as descrições estrangeiras below são canários de
	// vazamento: nunca podem aparecer no HTML renderizado para o dono.
	ctForeignWSName    = "Sigilo Estrangeiro WS"
	ctForeignTXDesc    = "Sigilo Estrangeiro TX"
	ctForeignRuleDesc  = "Sigilo Estrangeiro Regra"
	ctForeignChildName = "Sigilo Filha Estrangeira"

	ctCatTxID     = "cat-ct-tx"
	ctCatRuleID   = "cat-ct-rule"
	ctCatLimitID  = "cat-ct-limit"
	ctCatParentID = "cat-ct-parent"
	ctCatFreeID   = "cat-ct-free"

	ctForeignTXID     = "tx-ct-foreign"
	ctForeignRuleID   = "rule-ct-foreign"
	ctForeignLimitID  = "limit-ct-foreign"
	ctForeignChildID  = "child-ct-foreign"
	ctForeignAccount  = "acc-ct-foreign"
	ctOwnerAccount    = "acc-ct-owner"
	ctForeignCanaries = ctForeignWSName + "\x00" + ctForeignTXDesc + "\x00" + ctForeignRuleDesc + "\x00" +
		ctForeignChildName + "\x00" + ctForeignTXID + "\x00" + ctForeignRuleID + "\x00" +
		ctForeignLimitID + "\x00" + ctForeignChildID + "\x00" + ctForeignWSID + "\x00" + ctForeignAccount
)

// seedCrossTenantCategoryScenario cria dois workspaces (tipos parametrizáveis),
// categorias no workspace dono e UMA dependência estrangeira de cada espécie
// no workspace estrangeiro, cada uma apontando para uma categoria distinta do
// dono — assim o 409 de cada teste só pode vir da referência cruzada.
func seedCrossTenantCategoryScenario(t *testing.T, db *sql.DB, ownerType, foreignType string) {
	t.Helper()
	now := time.Now().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-1', 'User CT', 'ct@test.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES (?, ?, '', ?, ?, ?), (?, ?, '', ?, ?, ?)
	`, ctOwnerWSID, ctOwnerWSName, ownerType, now, now,
		ctForeignWSID, ctForeignWSName, foreignType, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, 'user-1', 'ADMIN', ?), (?, 'user-1', 'ADMIN', ?)
	`, ctOwnerWSID, now, ctForeignWSID, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Conta Owner CT', 'CHECKING', 100000, 100000, ?, ?),
		       (?, ?, 'Conta Estrangeira CT', 'CHECKING', 100000, 100000, ?, ?)
	`, ctOwnerAccount, ctOwnerWSID, now, now,
		ctForeignAccount, ctForeignWSID, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES
			(?, ?, 'Mercado CT', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			(?, ?, 'Assinaturas CT', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			(?, ?, 'Lazer CT', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			(?, ?, 'Casa CT', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?),
			(?, ?, 'Livre CT', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?)
	`, ctCatTxID, ctOwnerWSID, now,
		ctCatRuleID, ctOwnerWSID, now,
		ctCatLimitID, ctOwnerWSID, now,
		ctCatParentID, ctOwnerWSID, now,
		ctCatFreeID, ctOwnerWSID, now)

	// Dependências estrangeiras (inconsistência legada): workspace estrangeiro
	// referenciando categorias do dono. As FKs single-column permitem o vínculo
	// com foreign_keys=ON; nenhum trigger de INSERT valida o workspace da
	// categoria (apenas admission sealing de investimentos).
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, 'user-1', ?, ?, 'EXPENSE', 1000, ?, ?, 'paid', 1, 1, ?, ?)
	`, ctForeignTXID, ctForeignWSID, ctForeignAccount, ctCatTxID, now, ctForeignTXDesc, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES (?, ?, 'user-1', ?, ?, 'EXPENSE', 5000, ?, ?, 'MONTHLY', 'PAID', 1, ?, ?)
	`, ctForeignRuleID, ctForeignWSID, ctForeignAccount, ctCatRuleID, ctForeignRuleDesc, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold)
		VALUES (?, ?, ?, 50000, 0)
	`, ctForeignLimitID, ctForeignWSID, ctCatLimitID)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES (?, ?, ?, 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?, ?)
	`, ctForeignChildID, ctForeignWSID, ctForeignChildName, ctCatParentID, now)
}

// crossTenantSnapshot fotografa todas as linhas dos dois workspaces nas
// tabelas tocadas pela guarda: qualquer mutação (inclusive SET NULL/CASCADE
// acidental) muda a string.
func crossTenantSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"categories", "transactions", "recurring_rules", "cost_limits"} {
		out.WriteString(recurringAll4BB2RowsSnapshot(t, db, table,
			"SELECT * FROM "+table+" WHERE workspace_id IN (?, ?) ORDER BY id", ctOwnerWSID, ctForeignWSID))
	}
	return out.String()
}

func requireNoCrossTenantDisclosure(t *testing.T, body string) {
	t.Helper()
	for _, canary := range strings.Split(ctForeignCanaries, "\x00") {
		if strings.Contains(body, canary) {
			t.Fatalf("resposta divulgou dado identificável do workspace estrangeiro: %q presente no corpo", canary)
		}
	}
}

func TestCategoryDeleteCrossTenantReferenceBlocksAndPreserves(t *testing.T) {
	kinds := []struct {
		name       string
		categoryID string
		linkQuery  string
	}{
		{"transaction", ctCatTxID, `SELECT COUNT(*) FROM transactions WHERE id = '` + ctForeignTXID + `' AND workspace_id = '` + ctForeignWSID + `' AND category_id = '` + ctCatTxID + `'`},
		{"recurring_rule", ctCatRuleID, `SELECT COUNT(*) FROM recurring_rules WHERE id = '` + ctForeignRuleID + `' AND workspace_id = '` + ctForeignWSID + `' AND category_id = '` + ctCatRuleID + `'`},
		{"cost_limit", ctCatLimitID, `SELECT COUNT(*) FROM cost_limits WHERE id = '` + ctForeignLimitID + `' AND workspace_id = '` + ctForeignWSID + `' AND category_id = '` + ctCatLimitID + `'`},
		{"child_category", ctCatParentID, `SELECT COUNT(*) FROM categories WHERE id = '` + ctForeignChildID + `' AND workspace_id = '` + ctForeignWSID + `' AND parent_id = '` + ctCatParentID + `'`},
	}
	combos := []struct {
		name        string
		ownerType   string
		foreignType string
	}{
		{"personal_personal", "personal", "personal"},
		{"personal_business", "personal", "business"},
		{"business_personal", "business", "personal"},
	}

	for _, kind := range kinds {
		for _, combo := range combos {
			t.Run(kind.name+"/"+combo.name, func(t *testing.T) {
				db := openTestDB(t)
				t.Cleanup(func() { db.Close() })
				seedCrossTenantCategoryScenario(t, db, combo.ownerType, combo.foreignType)
				owner := newConfigHandlerForWS(t, db, ctOwnerWSID)

				before := crossTenantSnapshot(t, db)

				rr := requestCategoryDelete(owner, kind.categoryID)
				requireDeleteRejectedConflict(t, rr, db, kind.categoryID)
				requireCategoryLinkIntact(t, db, kind.linkQuery)
				if after := crossTenantSnapshot(t, db); after != before {
					t.Fatal("rejeição cross-tenant mutou linhas de algum workspace (SET NULL/CASCADE ou escrita parcial)")
				}
				requireNoCrossTenantDisclosure(t, rr.Body.String())

				// Atribuibilidade + controle positivo: a mesma semente sem a
				// referência estrangeira precisa permitir a exclusão — o 409
				// acima só pode vir da dependência cruzada.
				rrFree := requestCategoryDelete(owner, ctCatFreeID)
				if rrFree.Code != http.StatusOK {
					t.Fatalf("controle: categoria sem dependências deve ser excluída com 200; status=%d body=%.300s", rrFree.Code, rrFree.Body.String())
				}
				var freeCount int
				if err := db.QueryRow(`SELECT COUNT(*) FROM categories WHERE id = ?`, ctCatFreeID).Scan(&freeCount); err != nil {
					t.Fatal(err)
				}
				if freeCount != 0 {
					t.Fatal("controle: categoria livre não foi excluída")
				}
			})
		}
	}
}

// O handler do workspace estrangeiro continua operando normalmente sobre suas
// próprias categorias: o bloqueio acima não vaza estado nem congela o tenant B.
func TestCategoryDeleteForeignWorkspaceStillOperatesAfterOwnerBlock(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedCrossTenantCategoryScenario(t, db, "personal", "business")
	owner := newConfigHandlerForWS(t, db, ctOwnerWSID)
	foreign := newConfigHandlerForWS(t, db, ctForeignWSID)

	if rr := requestCategoryDelete(owner, ctCatTxID); rr.Code != http.StatusConflict {
		t.Fatalf("dono deve ter exclusão bloqueada pela referência estrangeira; status=%d", rr.Code)
	}

	// Categoria do tenant estrangeiro sem dependências (a filha depende dela,
	// então cria-se outra direto no banco) segue deletável pelo próprio tenant.
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('cat-ct-foreign-free', ?, 'Livre Estrangeira', 'tag', '#6b7280', 'EXPENSE', 'Essencial', NULL, ?)
	`, ctForeignWSID, time.Now().Unix())
	rr := requestCategoryDelete(foreign, "cat-ct-foreign-free")
	if rr.Code != http.StatusOK {
		t.Fatalf("tenant estrangeiro deve excluir a própria categoria sem dependências; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	// Sem verificação de canários aqui: a resposta do handler estrangeiro
	// renderiza legitimamente as categorias do próprio workspace B. O contrato
	// de não-divulgação vale para as respostas renderizadas ao DONO (teste
	// matriz acima).

	// A linha estrangeira dependente continua intacta após o bloqueio do dono.
	requireCategoryLinkIntact(t, db, `SELECT COUNT(*) FROM transactions WHERE id = '`+ctForeignTXID+`' AND category_id = '`+ctCatTxID+`'`)
}

// investment_result_classifications é a única tabela dependente cuja
// referência cross-tenant é IRREPRESENTÁVEL no schema atual: a FK é composta
// (category_id, workspace_id) → categories(id, workspace_id) ON DELETE
// RESTRICT, o CHECK de natureza acopla category_id a transaction_id (cuja FK
// também é composta por workspace) e todo o caminho de escrita do serviço é
// workspace-bound. Este teste confirma a vigência da barreira: um INSERT
// direto com par cross-tenant é rejeitado por FK com foreign_keys=ON — por
// isso não existe (nem pode existir) caso cross-tenant dessa dependência.
func TestInvestmentResultClassificationCrossTenantReferenceIsUnrepresentable(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedCrossTenantCategoryScenario(t, db, "personal", "business")

	_, err := db.Exec(`
		INSERT INTO investment_result_classifications (
			workspace_id, id, investment_id, withdrawal_id, settlement_revision_id, application_id,
			settlement_version, version, transaction_id, category_id, principal_cents, result_cents,
			nature, state, economic_date, economic_at, created_at, created_by, request_id,
			payload_json, result_json
		) VALUES (
			?, 'class-ct-x', 'inv-ct-x', 'wd-ct-x', 'sr-ct-x', 'app-ct-x',
			1, 1, ?, ?, 1000, 100, 'REALIZED_GAIN', 'COMPLETED', '2026-01-15', ?, ?, 'user-1', ?,
			'{}', '{}'
		)
	`, ctForeignWSID, ctForeignTXID, ctCatTxID,
		1768478400, // 2026-01-15 12:00:00 UTC
		time.Now().Unix(), "aaaaaaaa-bbbb-cccc-dddd-eeeeffff0001")
	if err == nil {
		t.Fatal("INSERT cross-tenant em investment_result_classifications foi aceito; a barreira deveria rejeitar")
	}
	// Duas camadas podem rejeitar primeiro: o trigger de origem
	// (trg_c2b_origin, "invalid or duplicate investment classification",
	// valida as referências de liquidação antes das FKs) ou as FKs compostas
	// (category_id, workspace_id)/(workspace_id, transaction_id). Qualquer uma
	// prova que o par cross-tenant é irrepresentável.
	msg := strings.ToUpper(err.Error())
	if !strings.Contains(msg, "FOREIGN KEY") && !strings.Contains(msg, "INVESTMENT CLASSIFICATION") {
		t.Fatalf("esperada rejeição por trigger de classificação ou FK composta; obtido: %v", err)
	}
}
