package handlers

// R1.3-D5-SEC-R2-RED — A1: transferências envolvendo contas CREDIT_CARD.
//
// Defeito (D5-SEC-A): validateTransferAccounts só compara origem≠destino;
// nenhuma camada (criação, edição, confirmação) checa o tipo de conta. Uma
// transferência com cartão de crédito em qualquer lado é aceita e produz efeito
// de saldo artificial (ApplyBalanceEffect não ignora cartões para TRANSFER),
// contrariando o modelo em que cartão não é caixa (EXPENSE/INCOME em cartão não
// tocam saldo; settlement de investimentos exclui transferências com destino
// cartão do cálculo de caixa).
//
// Contrato esperado (fica GREEN após a correção A1):
//   - transferência com CREDIT_CARD como origem, destino ou ambos é rejeitada
//     antes de qualquer efeito financeiro (sem linha em transactions, sem
//     efeito de saldo, sem regra/série de recorrência, sem histórico);
//   - o mesmo vale para criação paga, criação com status pendente, criação
//     parcelada, recorrência, edição individual e confirmação de pendente;
//   - fluxos legítimos permanecem: transferência entre contas de caixa,
//     despesa em cartão com fatura, linhas legadas de investimento;
//   - registros legados permanecem legíveis, sem regularização automática;
//   - isolamento por workspace_id preservado (conta estrangeira é rejeitada).
//
// Hoje estes testes falham de forma determinística (RED) exibindo o defeito.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fixture e helpers
// ---------------------------------------------------------------------------

// seedBusinessTransferWorkspace adiciona um workspace BUSINESS (ws-biz) com
// contas de caixa, cartão e destinho, reusando user-test do cenário de faturas.
func seedBusinessTransferWorkspace(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-biz', 'Workspace Business', '', 'business', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-biz', 'user-test', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('checking-biz', 'ws-biz', 'Corrente Business', 'CHECKING', 200000, 200000, ?, ?),
			('dest-biz', 'ws-biz', 'Destino Business', 'CHECKING', 50000, 50000, ?, ?),
			('card-biz', 'ws-biz', 'Cartao Business', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-biz', 'card-biz', 20, 10, 500000)
	`)
}

func newCreditCardTransferHandler(db *sql.DB, workspaceID string) TransactionHandler {
	return TransactionHandler{
		DB:          db,
		WriterDB:    db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: workspaceID,
		UserID:      "user-test",
	}
}

func creditCardTransferForm(origem, destino string) url.Values {
	return url.Values{
		"tipo":             {"transferencia"},
		"valor":            {"100,00"},
		"descricao":        {"Transferencia com cartao"},
		"data":             {"2026-10-20"},
		"origem_conta_id":  {origem},
		"destino_conta_id": {destino},
		"status_pagamento": {"paid"},
	}
}

// saveCreditCardTransfer executa o handler real de criação com o formulário dado.
func saveCreditCardTransfer(t *testing.T, h TransactionHandler, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.HandleSalvarTransacao(rr, formPostMultipart(t, "/transacoes", values))
	return rr
}

// transferCreateRejected detecta o canal de rejeição do formulário de criação:
// status HTTP de erro OU mensagem inline no bloco lancamento-form-error
// (contrato HTMX: respostas de validação chegam como 200 + div com borda rose).
func transferCreateRejected(rr *httptest.ResponseRecorder) bool {
	if rr.Code != http.StatusOK {
		return true
	}
	return strings.Contains(rr.Body.String(), "border-rose-500")
}

type accountBalances map[string]int64

// accountBalanceAnyWS consulta saldo sem escopo de workspace (o helper
// accountBalance existente é fixado em ws-test; os cenários business e de
// isolamento precisam ler contas de outros workspaces).
func accountBalanceAnyWS(t *testing.T, db *sql.DB, accountID string) int64 {
	t.Helper()
	var balance int64
	if err := db.QueryRow(`SELECT COALESCE(current_balance, 0) FROM accounts WHERE id = ?`, accountID).Scan(&balance); err != nil {
		t.Fatalf("query balance for %s: %v", accountID, err)
	}
	return balance
}

func snapshotBalances(t *testing.T, db *sql.DB, accountIDs ...string) accountBalances {
	t.Helper()
	out := accountBalances{}
	for _, id := range accountIDs {
		out[id] = accountBalanceAnyWS(t, db, id)
	}
	return out
}

func requireBalancesUnchanged(t *testing.T, db *sql.DB, before accountBalances) {
	t.Helper()
	for id, want := range before {
		if got := accountBalanceAnyWS(t, db, id); got != want {
			t.Fatalf("saldo de %s = %d, esperado %d (inalterado)", id, got, want)
		}
	}
}

func requireNoCardTransferRows(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	if n := countRows(t, db, `SELECT COUNT(*) FROM transactions WHERE workspace_id = ? AND type = 'TRANSFER'`, workspaceID); n != 0 {
		t.Fatalf("linhas TRANSFER no workspace %s = %d, esperado 0", workspaceID, n)
	}
}

// ---------------------------------------------------------------------------
// RED — criação (Personal)
// ---------------------------------------------------------------------------

// Cartão como ORIGEM: hoje a transferência é aceita e o cartão é debitado em
// saldo (que não representa caixa), criando dinheiro artificial no destino.
func TestCreditCardTransferOriginCreateIsRejectedWithoutEffects(t *testing.T) {
	f := setupTransferFixture(t)
	before := snapshotBalances(t, f.db, "card-test", "dest-test", "checking-test")

	rr := saveCreditCardTransfer(t, f.h, creditCardTransferForm("card-test", "dest-test"))

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1: transferência com cartão como ORIGEM deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, f.db, "ws-test")
	requireBalancesUnchanged(t, f.db, before)
}

// Cartão como DESTINO: hoje o caixa real sai da origem e "entra" no cartão,
// destruindo valor real e inflando um saldo sem significado.
func TestCreditCardTransferDestinationCreateIsRejectedWithoutEffects(t *testing.T) {
	f := setupTransferFixture(t)
	before := snapshotBalances(t, f.db, "card-test", "dest-test", "checking-test")

	rr := saveCreditCardTransfer(t, f.h, creditCardTransferForm("checking-test", "card-test"))

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1: transferência com cartão como DESTINO deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, f.db, "ws-test")
	requireBalancesUnchanged(t, f.db, before)
}

// Cartão nos DOIS lados: mesmo sendo contas diferentes, a validação atual
// aceita e movimenta saldos artificiais nos dois cartões.
func TestCreditCardTransferBothSidesCreateIsRejectedWithoutEffects(t *testing.T) {
	f := setupTransferFixture(t)
	execTestSQL(t, f.db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card2-test', 'ws-test', 'Cartao Dois', 'CREDIT_CARD', 0, 0, strftime('%s','now'), strftime('%s','now'))
	`)
	execTestSQL(t, f.db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-2', 'card2-test', 20, 10, 500000)
	`)
	before := snapshotBalances(t, f.db, "card-test", "card2-test")

	rr := saveCreditCardTransfer(t, f.h, creditCardTransferForm("card-test", "card2-test"))

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1: transferência cartão→cartão deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, f.db, "ws-test")
	requireBalancesUnchanged(t, f.db, before)
}

// Status pendente pedido no formulário: a criação força "paid" para TRANSFER,
// mas a rejeição deve valer para qualquer intenção de transferência inválida.
func TestCreditCardTransferPendingStatusCreateIsRejectedWithoutEffects(t *testing.T) {
	f := setupTransferFixture(t)
	before := snapshotBalances(t, f.db, "card-test", "dest-test")

	values := creditCardTransferForm("card-test", "dest-test")
	values.Set("status_pagamento", "pending")
	rr := saveCreditCardTransfer(t, f.h, values)

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1: transferência pendente com cartão como origem deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, f.db, "ws-test")
	requireBalancesUnchanged(t, f.db, before)
}

// Recorrência: hoje a regra, a série e a projeção de ocorrências são criadas
// com o cartão como origem antes de qualquer possibilidade de desfazer.
func TestCreditCardTransferRecurringCreateIsRejectedWithoutRuleOrSeries(t *testing.T) {
	f := setupTransferFixture(t)
	before := snapshotBalances(t, f.db, "card-test", "dest-test")

	values := creditCardTransferForm("card-test", "dest-test")
	values.Set("lancamento_fixo", "on")
	values.Set("recorrencia", "monthly")
	rr := saveCreditCardTransfer(t, f.h, values)

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1: transferência recorrente com cartão deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, f.db, "ws-test")
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM recurring_rules WHERE type = 'TRANSFER'`); n != 0 {
		t.Fatalf("regras TRANSFER = %d, esperado 0", n)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM recurring_series`); n != 0 {
		t.Fatalf("séries de recorrência = %d, esperado 0", n)
	}
	requireBalancesUnchanged(t, f.db, before)
}

// Parcelada: hoje três linhas são geradas com o cartão como origem.
func TestCreditCardTransferInstallmentsCreateIsRejectedWithoutRows(t *testing.T) {
	f := setupTransferFixture(t)
	before := snapshotBalances(t, f.db, "card-test", "dest-test")

	values := creditCardTransferForm("card-test", "dest-test")
	values.Set("parcelada", "on")
	values.Set("parcelas", "3")
	rr := saveCreditCardTransfer(t, f.h, values)

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1: transferência parcelada com cartão deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, f.db, "ws-test")
	requireBalancesUnchanged(t, f.db, before)
}

// ---------------------------------------------------------------------------
// RED — criação (Business)
// ---------------------------------------------------------------------------

func TestCreditCardTransferBusinessWorkspaceOriginCreateIsRejected(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	seedBusinessTransferWorkspace(t, db)
	h := newCreditCardTransferHandler(db, "ws-biz")
	before := snapshotBalances(t, db, "card-biz", "checking-biz", "dest-biz")

	rr := saveCreditCardTransfer(t, h, creditCardTransferForm("card-biz", "dest-biz"))

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1 (business): transferência com cartão como origem deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, db, "ws-biz")
	requireBalancesUnchanged(t, db, before)
}

func TestCreditCardTransferBusinessWorkspaceDestinationCreateIsRejected(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	seedBusinessTransferWorkspace(t, db)
	h := newCreditCardTransferHandler(db, "ws-biz")
	before := snapshotBalances(t, db, "card-biz", "checking-biz", "dest-biz")

	rr := saveCreditCardTransfer(t, h, creditCardTransferForm("checking-biz", "card-biz"))

	if !transferCreateRejected(rr) {
		t.Fatalf("RED A1 (business): transferência com cartão como destino deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, db, "ws-biz")
	requireBalancesUnchanged(t, db, before)
}

// ---------------------------------------------------------------------------
// RED — edição individual
// ---------------------------------------------------------------------------

func createValidTransferForEdit(t *testing.T, f transferFixture) {
	t.Helper()
	rr := saveCreditCardTransfer(t, f.h, creditCardTransferForm("checking-test", "dest-test"))
	if rr.Code != http.StatusOK {
		t.Fatalf("setup: transferência válida falhou: status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 1 {
		t.Fatalf("setup: linhas TRANSFER = %d, esperado 1", n)
	}
}

func editTransferForm(t *testing.T, origem, destino string) url.Values {
	t.Helper()
	return url.Values{
		"tipo":             {"transferencia"},
		"valor":            {"100,00"},
		"descricao":        {"Transferencia editada"},
		"data":             {"2026-10-20"},
		"origem_conta_id":  {origem},
		"destino_conta_id": {destino},
		"status_pagamento": {"paid"},
		"escopo":           {"single"},
	}
}

func submitTransferEdit(t *testing.T, h TransactionHandler, id string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, formPostMultipart(t, "/transacoes/"+id, values), id)
	return rr
}

func transferIDForEdit(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM transactions WHERE type='TRANSFER' LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("query transfer id: %v", err)
	}
	return id
}

// Editar uma transferência válida trocando o DESTINO para cartão: hoje o edit
// é aceito, reverte o efeito antigo e aplica efeito novo envolvendo o cartão.
func TestCreditCardTransferEditDestinationIsRejectedWithoutEffects(t *testing.T) {
	f := setupTransferFixture(t)
	createValidTransferForEdit(t, f)
	before := snapshotBalances(t, f.db, "checking-test", "dest-test", "card-test")
	id := transferIDForEdit(t, f.db)

	rr := submitTransferEdit(t, f.h, id, editTransferForm(t, "checking-test", "card-test"))

	if rr.Code < 400 {
		t.Fatalf("RED A1: editar transferência para destino cartão deve ser rejeitado com 4xx; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	var dest sql.NullString
	if err := f.db.QueryRow(`SELECT destination_account_id FROM transactions WHERE id = ?`, id).Scan(&dest); err != nil {
		t.Fatalf("query destination: %v", err)
	}
	if !dest.Valid || dest.String != "dest-test" {
		t.Fatalf("destination_account_id = %+v, esperado dest-test (inalterado)", dest)
	}
	requireBalancesUnchanged(t, f.db, before)
}

// Editar trocando a ORIGEM para cartão: mesmo contrato.
func TestCreditCardTransferEditOriginIsRejectedWithoutEffects(t *testing.T) {
	f := setupTransferFixture(t)
	createValidTransferForEdit(t, f)
	before := snapshotBalances(t, f.db, "checking-test", "dest-test", "card-test")
	id := transferIDForEdit(t, f.db)

	rr := submitTransferEdit(t, f.h, id, editTransferForm(t, "card-test", "dest-test"))

	if rr.Code < 400 {
		t.Fatalf("RED A1: editar transferência com origem cartão deve ser rejeitado com 4xx; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	var origin string
	if err := f.db.QueryRow(`SELECT account_id FROM transactions WHERE id = ?`, id).Scan(&origin); err != nil {
		t.Fatalf("query origin: %v", err)
	}
	if origin != "checking-test" {
		t.Fatalf("account_id = %s, esperado checking-test (inalterado)", origin)
	}
	requireBalancesUnchanged(t, f.db, before)
}

func TestCreditCardTransferRecurringAllEditDestinationIsRejectedWithoutEffects(t *testing.T) {
	f := newRecurringAll4BB2Fixture(t, false, "2026-01-10", nil)
	t.Cleanup(f.close)
	seedBusinessTransferWorkspace(t, f.db)
	preflightReached := false
	f.handler.testHooks = &recurringEditTestHooks{afterPreflight: func(scope string) {
		if scope == "all" {
			preflightReached = true
		}
	}}
	fields := map[string]string{
		"tipo":             "transferencia",
		"destino_conta_id": "checking-alt-b2",
		"status_pagamento": "paid",
		"categoria_id":     "",
	}
	// Establish a valid transfer rule through the same series edit path.
	rr := f.request(t, 1, fields)
	recurringAll4BB2AssertStatus(t, rr, http.StatusOK)
	if !preflightReached {
		t.Fatal("setup: edição não executou o preflight de updateAllRecurringSeries4BB2Tx")
	}
	var destination string
	if err := f.db.QueryRow(`SELECT destination_account_id FROM recurring_rules WHERE id=? AND type='TRANSFER'`, f.ruleID).Scan(&destination); err != nil || destination != "checking-alt-b2" {
		t.Fatalf("setup: regra de transferência inválida: destino=%q erro=%v", destination, err)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE series_id=? AND type='TRANSFER' AND account_id='checking-test' AND destination_account_id='checking-alt-b2'`, f.seriesID); n != len(f.ids) {
		t.Fatalf("setup: ocorrências válidas=%d, esperado %d", n, len(f.ids))
	}
	tables := []string{"accounts", "transactions", "recurring_rules", "recurring_series", "transaction_status_history"}
	snapshot := func() string {
		var out strings.Builder
		for _, table := range tables {
			out.WriteString(recurringAll4BB2RowsSnapshot(t, f.db, table, "SELECT * FROM "+table+" ORDER BY id"))
		}
		return out.String()
	}
	before := snapshot()
	balances := snapshotBalances(t, f.db, "checking-test", "checking-alt-b2", "card-test", "checking-biz", "dest-biz", "card-biz")
	preflightReached = false
	fields["destino_conta_id"] = "card-test"
	rr = f.request(t, 1, fields)
	t.Logf("scope=all: status=%d preflight_da_série=%t", rr.Code, preflightReached)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("RED A1: edição scope=all para destino CREDIT_CARD deve responder 422; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	if err := f.db.QueryRow(`SELECT destination_account_id FROM recurring_rules WHERE id=?`, f.ruleID).Scan(&destination); err != nil {
		t.Fatal(err)
	}
	if destination != "checking-alt-b2" {
		t.Errorf("regra alterada parcialmente: destination_account_id=%q", destination)
	}
	// Global snapshots also protect foreign workspaces and detect added rows.
	if after := snapshot(); after != before {
		t.Error("rejeição alterou contas, ocorrências, regras, séries ou histórico de algum workspace")
	}
	requireBalancesUnchanged(t, f.db, balances)
}

// ---------------------------------------------------------------------------
// RED — confirmação de transferência pendente legada
// ---------------------------------------------------------------------------

func seedLegacyPendingCardTransfer(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'card-test', 'checking-test', 'TRANSFER', 10000, ?, 'Transferencia legada pendente', 'pending', 1, 1, ?, ?)
	`, id, now, now, now)
}

func confirmTransferPayment(t *testing.T, h TransactionHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(url.Values{"payment_action": {"paid"}}.Encode())
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+id+"/status-pagamento", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	h.HandleTogglePagamento(rr, req, id)
	return rr
}

// Confirmar (pagar) uma transferência pendente legada cartão→caixa: hoje a
// confirmação é aceita e aplica efeito de saldo artificial no cartão.
func TestCreditCardPendingTransferConfirmationIsRejectedWithoutBalanceEffect(t *testing.T) {
	f := setupTransferFixture(t)
	seedLegacyPendingCardTransfer(t, f.db, "legacy-card-pending")
	before := snapshotBalances(t, f.db, "card-test", "checking-test")

	rr := confirmTransferPayment(t, f.h, "legacy-card-pending")

	if rr.Code < 400 {
		t.Fatalf("RED A1: confirmar transferência pendente envolvendo cartão deve ser rejeitado com 4xx; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	var status string
	if err := f.db.QueryRow(`SELECT status FROM transactions WHERE id = 'legacy-card-pending'`).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("status = %s, esperado pending (inalterado)", status)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transaction_status_history WHERE transaction_id = 'legacy-card-pending'`); n != 0 {
		t.Fatalf("histórico de status = %d, esperado 0", n)
	}
	requireBalancesUnchanged(t, f.db, before)
}

// ---------------------------------------------------------------------------
// PASS — registros legados legíveis, sem regularização automática
// ---------------------------------------------------------------------------

func TestLegacyCreditCardTransfersStayReadableWithoutRegularization(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('dest-test', 'ws-test', 'Conta Destino', 'CHECKING', 50000, 50000, strftime('%s','now'), strftime('%s','now'))
	`)
	now := time.Now().Unix()
	// Legado PAGO (cartão→caixa) e legado PENDENTE (aba "A Transferir").
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('legacy-card-paid', 'ws-test', 'user-test', 'card-test', 'checking-test', 'TRANSFER', 10000, ?, 'Transferencia legada paga', 'paid', 1, 1, ?, ?),
			('legacy-card-pending-read', 'ws-test', 'user-test', 'checking-test', 'card-test', 'TRANSFER', 5000, ?, 'Transferencia legada pendente', 'pending', 1, 1, ?, ?)
	`, now, now, now, now, now, now)
	cardBefore := accountBalanceAnyWS(t, db, "card-test")
	checkingBefore := accountBalanceAnyWS(t, db, "checking-test")

	// Leitura pela view canônica e pela lista de transferências pendentes.
	if n := countRows(t, db, `SELECT COUNT(*) FROM financial_transactions WHERE id IN ('legacy-card-paid','legacy-card-pending-read')`); n != 2 {
		t.Fatalf("linhas legadas legíveis = %d, esperado 2", n)
	}
	pending, err := queryPendingTransfersFiltered(db, "ws-test", 0, 0, "")
	if err != nil {
		t.Fatalf("query pending transfers: %v", err)
	}
	found := false
	for _, row := range pending {
		if row.ID == "legacy-card-pending-read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("transferência pendente legada ausente da aba A Transferir (%d itens)", len(pending))
	}

	// Sem regularização automática: a leitura não pode alterar saldo ou linha.
	if got := accountBalanceAnyWS(t, db, "card-test"); got != cardBefore {
		t.Fatalf("saldo do cartão mudou após leitura: %d → %d", cardBefore, got)
	}
	if got := accountBalanceAnyWS(t, db, "checking-test"); got != checkingBefore {
		t.Fatalf("saldo da conta mudou após leitura: %d → %d", checkingBefore, got)
	}
	var updated int64
	if err := db.QueryRow(`SELECT updated_at FROM transactions WHERE id = 'legacy-card-paid'`).Scan(&updated); err != nil {
		t.Fatalf("query updated_at: %v", err)
	}
	if updated != now {
		t.Fatalf("updated_at = %d, esperado %d (linha não reescrita)", updated, now)
	}
}

// ---------------------------------------------------------------------------
// PASS — controles: fluxos legítimos preservados
// ---------------------------------------------------------------------------

func TestRegularAccountTransferStillAccepted(t *testing.T) {
	f := setupTransferFixture(t)

	rr := saveCreditCardTransfer(t, f.h, creditCardTransferForm("checking-test", "dest-test"))

	if rr.Code != http.StatusOK {
		t.Fatalf("transferência caixa→caixa deve seguir funcionando; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 1 {
		t.Fatalf("linhas TRANSFER = %d, esperado 1", n)
	}
	if got := accountBalanceAnyWS(t, f.db, "checking-test"); got != 90000 {
		t.Fatalf("saldo origem = %d, esperado 90000", got)
	}
	if got := accountBalanceAnyWS(t, f.db, "dest-test"); got != 60000 {
		t.Fatalf("saldo destino = %d, esperado 60000", got)
	}
}

func TestCreditCardExpenseStillAccepted(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('cat-card-expense', 'ws-test', 'Mercado', 'tag', '#6b7280', 'EXPENSE', 'Essencial', strftime('%s','now'))
	`)
	h := newCreditCardTransferHandler(db, "ws-test")

	values := url.Values{
		"tipo":             {"despesa"},
		"valor":            {"50,00"},
		"descricao":        {"Compra no cartao"},
		"data":             {time.Now().Format("2006-01-02")},
		"origem_conta_id":  {"card-test"},
		"categoria_id":     {"cat-card-expense"},
		"status_pagamento": {"paid"},
	}
	rr := saveCreditCardTransfer(t, h, values)

	if rr.Code != http.StatusOK {
		t.Fatalf("despesa em cartão deve seguir funcionando; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM transactions WHERE type='EXPENSE' AND account_id='card-test' AND category_id='cat-card-expense'`); n != 1 {
		t.Fatalf("linhas de despesa no cartão = %d, esperado 1", n)
	}
	if got := accountBalanceAnyWS(t, db, "card-test"); got != 0 {
		t.Fatalf("saldo do cartão = %d, esperado 0 (despesa em cartão não move saldo)", got)
	}
}

// Linhas de investimento (escritas pelo módulo com prefixo reservado) seguem
// legíveis e intactas: a correção A1 não pode tocá-las nem regularizá-las.
func TestInvestmentTransferRowsRemainReadableAndPreserved(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('inv-test', 'ws-test', 'Investimento', 'INVESTMENT', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('investment-purchase:red-control', 'ws-test', 'user-test', 'checking-test', 'inv-test', 'TRANSFER', 10000, ?, 'Aplicação de investimento', 'paid', 1, 1, ?, ?)
	`, now, now, now)

	if n := countRows(t, db, `SELECT COUNT(*) FROM financial_transactions WHERE id = 'investment-purchase:red-control' AND destination_account_id = 'inv-test'`); n != 1 {
		t.Fatalf("linha de transferência de investimento = %d, esperado 1", n)
	}
	var dest string
	if err := db.QueryRow(`SELECT destination_account_id FROM transactions WHERE id = 'investment-purchase:red-control'`).Scan(&dest); err != nil || dest != "inv-test" {
		t.Fatalf("linha de investimento alterada: dest=%q err=%v", dest, err)
	}
}

// ---------------------------------------------------------------------------
// PASS — isolamento por workspace
// ---------------------------------------------------------------------------

func TestCreditCardTransferForeignWorkspaceAccountRejected(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	seedBusinessTransferWorkspace(t, db)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('dest-test', 'ws-test', 'Conta Destino', 'CHECKING', 50000, 50000, strftime('%s','now'), strftime('%s','now'))
	`)
	h := newCreditCardTransferHandler(db, "ws-test")
	cardBizBefore := accountBalanceAnyWS(t, db, "card-biz")

	rr := saveCreditCardTransfer(t, h, creditCardTransferForm("card-biz", "dest-test"))

	if !transferCreateRejected(rr) {
		t.Fatalf("conta de outro workspace deve ser rejeitada; status=%d body=%.300s", rr.Code, rr.Body.String())
	}
	requireNoCardTransferRows(t, db, "ws-test")
	if n := countRows(t, db, `SELECT COUNT(*) FROM transactions WHERE workspace_id = 'ws-biz' AND type = 'TRANSFER'`); n != 0 {
		t.Fatalf("linhas TRANSFER no ws-biz = %d, esperado 0", n)
	}
	if got := accountBalanceAnyWS(t, db, "card-biz"); got != cardBizBefore {
		t.Fatalf("saldo do cartão estrangeiro mudou: %d → %d", cardBizBefore, got)
	}
}
