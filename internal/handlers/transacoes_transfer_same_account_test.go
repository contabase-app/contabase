package handlers

// F8-01 — integridade de transferências: origem e destino devem ser contas
// diferentes. A transferência é UMA linha (account_id = origem,
// destination_account_id = destino) com dois efeitos de saldo sequenciais;
// origem = destino anula o saldo persistido mas persiste ruído contábil
// (extrato por conta duplica a linha e o trend por conta diverge do saldo).

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type transferFixture struct {
	db *sql.DB
	h  TransactionHandler
}

func setupTransferFixture(t *testing.T) transferFixture {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('dest-test', 'ws-test', 'Conta Destino', 'CHECKING', 50000, 50000, strftime('%s','now'), strftime('%s','now'))
	`)
	return transferFixture{db: db, h: testPaidInvoiceMutationHandler(db)}
}

func transferFormValues(origem, destino string) url.Values {
	return url.Values{
		"tipo":             {"transferencia"},
		"valor":            {"100,00"},
		"descricao":        {"Transferencia de teste"},
		"data":             {"2026-10-20"},
		"origem_conta_id":  {origem},
		"destino_conta_id": {destino},
		"status_pagamento": {"paid"},
	}
}

func saveTransfer(t *testing.T, f transferFixture, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	f.h.HandleSalvarTransacao(rr, formPostMultipart(t, "/transacoes", values))
	return rr
}

func countRows(t *testing.T, db *sql.DB, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestTransferSameAccountCreateRejected(t *testing.T) {
	f := setupTransferFixture(t)

	rr := saveTransfer(t, f, transferFormValues("checking-test", "checking-test"))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (inline validation contract)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "A conta de origem e a conta de destino devem ser diferentes") {
		t.Fatalf("body missing same-account message: %q", rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("self-transfer rows = %d, want 0", n)
	}
	if bal := accountBalance(t, f.db, "checking-test"); bal != 100000 {
		t.Fatalf("origin balance = %d, want 100000 (unchanged)", bal)
	}
}

func TestTransferSameAccountRecurringRejectedBeforeSeriesInsert(t *testing.T) {
	f := setupTransferFixture(t)

	values := transferFormValues("checking-test", "checking-test")
	values.Set("lancamento_fixo", "on")
	values.Set("recorrencia", "monthly")
	rr := saveTransfer(t, f, values)

	if !strings.Contains(rr.Body.String(), "A conta de origem e a conta de destino devem ser diferentes") {
		t.Fatalf("body missing same-account message: %q", rr.Body.String())
	}
	assertNoTransferSideEffects := func() {
		t.Helper()
		if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
			t.Fatalf("transfer rows = %d, want 0 (no partial recurrence)", n)
		}
		if n := countRows(t, f.db, `SELECT COUNT(*) FROM recurring_rules WHERE type='TRANSFER'`); n != 0 {
			t.Fatalf("transfer rules = %d, want 0 (no partial recurrence)", n)
		}
		if n := countRows(t, f.db, `SELECT COUNT(*) FROM recurring_series`); n != 0 {
			t.Fatalf("recurring series = %d, want 0 (no partial recurrence)", n)
		}
	}
	assertNoTransferSideEffects()
}

func TestTransferValidCreatesSingleBalancedRow(t *testing.T) {
	f := setupTransferFixture(t)

	rr := saveTransfer(t, f, transferFormValues("checking-test", "dest-test"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 1 {
		t.Fatalf("transfer rows = %d, want 1 (single-row representation)", n)
	}
	if bal := accountBalance(t, f.db, "checking-test"); bal != 90000 {
		t.Fatalf("origin balance = %d, want 90000 (debit)", bal)
	}
	if bal := accountBalance(t, f.db, "dest-test"); bal != 60000 {
		t.Fatalf("destination balance = %d, want 60000 (credit)", bal)
	}

	var amount int64
	var accountID, destID string
	if err := f.db.QueryRow(`SELECT amount, account_id, destination_account_id FROM transactions WHERE type='TRANSFER'`).Scan(&amount, &accountID, &destID); err != nil {
		t.Fatalf("query transfer: %v", err)
	}
	if amount != 10000 || accountID != "checking-test" || destID != "dest-test" {
		t.Fatalf("row = (%d,%s,%s), want (10000,checking-test,dest-test)", amount, accountID, destID)
	}

	// Read-models: trend per account must match the persisted balance delta.
	var netFlowA, netFlowB int64
	err := f.db.QueryRow(`
		SELECT COALESCE(SUM(CASE
			WHEN t.type = 'INCOME' THEN t.amount
			WHEN t.type = 'EXPENSE' THEN -t.amount
			WHEN t.type = 'TRANSFER' AND t.account_id = ? THEN -t.amount
			WHEN t.type = 'TRANSFER' AND t.destination_account_id = ? THEN t.amount
			ELSE 0 END), 0)
		FROM financial_transactions t
		WHERE t.workspace_id = 'ws-test' AND t.status='paid'
		AND (t.account_id = ? OR t.destination_account_id = ?)
	`, "checking-test", "checking-test", "checking-test", "checking-test").Scan(&netFlowA)
	if err != nil {
		t.Fatalf("trend A: %v", err)
	}
	f.db.QueryRow(`
		SELECT COALESCE(SUM(CASE
			WHEN t.type = 'INCOME' THEN t.amount
			WHEN t.type = 'EXPENSE' THEN -t.amount
			WHEN t.type = 'TRANSFER' AND t.account_id = ? THEN -t.amount
			WHEN t.type = 'TRANSFER' AND t.destination_account_id = ? THEN t.amount
			ELSE 0 END), 0)
		FROM financial_transactions t
		WHERE t.workspace_id = 'ws-test' AND t.status='paid'
		AND (t.account_id = ? OR t.destination_account_id = ?)
	`, "dest-test", "dest-test", "dest-test", "dest-test").Scan(&netFlowB)
	if netFlowA != -10000 || netFlowB != 10000 {
		t.Fatalf("trend flows = (%d,%d), want (-10000,10000)", netFlowA, netFlowB)
	}
}

func TestTransferSameNameDifferentAccountsAllowed(t *testing.T) {
	f := setupTransferFixture(t)
	execTestSQL(t, f.db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('salario-a', 'ws-test', 'Conta Salario', 'CHECKING', 10000, 10000, strftime('%s','now'), strftime('%s','now'))
	`)
	execTestSQL(t, f.db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('salario-b', 'ws-test', 'Conta Salario', 'CHECKING', 10000, 10000, strftime('%s','now'), strftime('%s','now'))
	`)

	rr := saveTransfer(t, f, transferFormValues("salario-a", "salario-b"))
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "devem ser diferentes") {
		t.Fatalf("same-name different-id transfer rejected: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER' AND account_id='salario-a' AND destination_account_id='salario-b'`); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	if bal := accountBalance(t, f.db, "salario-a"); bal != 0 {
		t.Fatalf("salario-a balance = %d, want 0", bal)
	}
	if bal := accountBalance(t, f.db, "salario-b"); bal != 20000 {
		t.Fatalf("salario-b balance = %d, want 20000", bal)
	}
}

func TestTransferUnknownOriginRejected(t *testing.T) {
	f := setupTransferFixture(t)

	rr := saveTransfer(t, f, transferFormValues("no-such-account", "dest-test"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 inline error", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "conta de origem") {
		t.Fatalf("body missing origin error: %q", rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	if bal := accountBalance(t, f.db, "dest-test"); bal != 50000 {
		t.Fatalf("destination balance = %d, want 50000 (unchanged)", bal)
	}
}

func TestTransferUnknownDestinationRejected(t *testing.T) {
	f := setupTransferFixture(t)

	rr := saveTransfer(t, f, transferFormValues("checking-test", "no-such-account"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 inline error", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "conta de origem") && !strings.Contains(rr.Body.String(), "conta") {
		t.Fatalf("body missing account error: %q", rr.Body.String())
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	if bal := accountBalance(t, f.db, "checking-test"); bal != 100000 {
		t.Fatalf("origin balance = %d, want 100000 (unchanged)", bal)
	}
}

func TestTransferCrossWorkspaceDestinationRejected(t *testing.T) {
	f := setupTransferFixture(t)
	now := "strftime('%s','now')"
	execTestSQL(t, f.db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-other', 'Outro Workspace', '', `+now+`, `+now+`)
	`)
	execTestSQL(t, f.db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('other-acc', 'ws-other', 'Conta Outro WS', 'CHECKING', 70000, 70000, `+now+`, `+now+`)
	`)

	rr := saveTransfer(t, f, transferFormValues("checking-test", "other-acc"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 inline error", rr.Code)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("rows = %d, want 0 (workspace isolation)", n)
	}
	if bal := accountBalance(t, f.db, "checking-test"); bal != 100000 {
		t.Fatalf("origin balance = %d, want 100000 (unchanged)", bal)
	}
	var otherBal int64
	f.db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'other-acc'`).Scan(&otherBal)
	if otherBal != 70000 {
		t.Fatalf("other workspace balance = %d, want 70000 (untouched)", otherBal)
	}
}

func TestTransferEditToSameAccountRejected(t *testing.T) {
	f := setupTransferFixture(t)
	if rr := saveTransfer(t, f, transferFormValues("checking-test", "dest-test")); rr.Code != http.StatusOK {
		t.Fatalf("create failed: %d", rr.Code)
	}

	var id string
	f.db.QueryRow(`SELECT id FROM transactions WHERE type='TRANSFER'`).Scan(&id)

	values := transferFormValues("checking-test", "checking-test")
	values.Set("escopo_edicao", "single")
	rr := httptest.NewRecorder()
	f.h.HandleAtualizarTransacao(rr, formPutMultipartSameAccount(t, "/transacoes/"+id, values), id)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%q, want 422", rr.Code, rr.Body.String())
	}
	var destID sql.NullString
	f.db.QueryRow(`SELECT destination_account_id FROM transactions WHERE id = ?`, id).Scan(&destID)
	if !destID.Valid || destID.String != "dest-test" {
		t.Fatalf("edited destination = %v, want dest-test (row unchanged)", destID)
	}
	if bal, want := accountBalance(t, f.db, "checking-test"), int64(90000); bal != want {
		t.Fatalf("origin balance = %d, want %d (unchanged)", bal, want)
	}
	if bal, want := accountBalance(t, f.db, "dest-test"), int64(60000); bal != want {
		t.Fatalf("destination balance = %d, want %d (unchanged)", bal, want)
	}
}

func TestTransferEditValidAmountAdjustsBalances(t *testing.T) {
	f := setupTransferFixture(t)
	if rr := saveTransfer(t, f, transferFormValues("checking-test", "dest-test")); rr.Code != http.StatusOK {
		t.Fatalf("create failed: %d", rr.Code)
	}

	var id string
	f.db.QueryRow(`SELECT id FROM transactions WHERE type='TRANSFER'`).Scan(&id)

	values := transferFormValues("checking-test", "dest-test")
	values.Set("valor", "200,00")
	values.Set("escopo_edicao", "single")
	rr := httptest.NewRecorder()
	f.h.HandleAtualizarTransacao(rr, formPutMultipartSameAccount(t, "/transacoes/"+id, values), id)
	if rr.Code != http.StatusOK {
		t.Fatalf("edit failed: %d body=%s", rr.Code, rr.Body.String())
	}

	if bal, want := accountBalance(t, f.db, "checking-test"), int64(80000); bal != want {
		t.Fatalf("origin balance = %d, want %d", bal, want)
	}
	if bal, want := accountBalance(t, f.db, "dest-test"), int64(70000); bal != want {
		t.Fatalf("destination balance = %d, want %d", bal, want)
	}
	var amount int64
	f.db.QueryRow(`SELECT amount FROM transactions WHERE id = ?`, id).Scan(&amount)
	if amount != 20000 {
		t.Fatalf("amount = %d, want 20000", amount)
	}
}

func TestTransferInvalidAmountRejected(t *testing.T) {
	f := setupTransferFixture(t)
	values := transferFormValues("checking-test", "dest-test")
	values.Set("valor", "0,00")

	rr := saveTransfer(t, f, values)
	trigger := rr.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, `"field": "valor"`) {
		t.Fatalf("HX-Trigger missing valor field error: %q", trigger)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	if bal := accountBalance(t, f.db, "checking-test"); bal != 100000 {
		t.Fatalf("origin balance = %d, want 100000 (unchanged)", bal)
	}
}

func TestTransferPersistenceFailureRollsBackRecurrence(t *testing.T) {
	f := setupTransferFixture(t)
	execTestSQL(t, f.db, `
		CREATE TRIGGER abort_transfer_insert BEFORE INSERT ON transactions
		WHEN NEW.type = 'TRANSFER'
		BEGIN
			SELECT RAISE(ABORT, 'forced persistence failure');
		END
	`)

	values := transferFormValues("checking-test", "dest-test")
	values.Set("lancamento_fixo", "on")
	values.Set("recorrencia", "monthly")
	rr := saveTransfer(t, f, values)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 on persistence failure", rr.Code)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("transfer rows = %d, want 0 (atomic rollback)", n)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM recurring_rules WHERE type='TRANSFER'`); n != 0 {
		t.Fatalf("transfer rules = %d, want 0 (atomic rollback)", n)
	}
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM recurring_series`); n != 0 {
		t.Fatalf("recurring series = %d, want 0 (atomic rollback)", n)
	}
	if bal := accountBalance(t, f.db, "checking-test"); bal != 100000 {
		t.Fatalf("origin balance = %d, want 100000 (no partial effect)", bal)
	}
	if bal := accountBalance(t, f.db, "dest-test"); bal != 50000 {
		t.Fatalf("destination balance = %d, want 50000 (no partial effect)", bal)
	}
}

func TestTransferRepeatedSubmissionCreatesIndependentRows(t *testing.T) {
	f := setupTransferFixture(t)

	for i := 0; i < 2; i++ {
		if rr := saveTransfer(t, f, transferFormValues("checking-test", "dest-test")); rr.Code != http.StatusOK {
			t.Fatalf("submission %d failed: %d", i+1, rr.Code)
		}
	}
	// Duplicatas idênticas são legítimas no domínio (rate limiting é camada HTTP);
	// o contrato é: cada submissão gera exatamente uma linha e um par débito/crédito.
	if n := countRows(t, f.db, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	if bal, want := accountBalance(t, f.db, "checking-test"), int64(80000); bal != want {
		t.Fatalf("origin balance = %d, want %d", bal, want)
	}
	if bal, want := accountBalance(t, f.db, "dest-test"), int64(70000); bal != want {
		t.Fatalf("destination balance = %d, want %d", bal, want)
	}
}

func TestNewFormTransacaoDataDestinationNotMirroringOrigin(t *testing.T) {
	accounts := []FormAccount{
		{ID: "acc-1", Name: "Conta Um", Tipo: "conta"},
		{ID: "acc-2", Name: "Conta Dois", Tipo: "conta"},
	}

	data := newFormTransacaoData(accounts, nil)
	if data.OrigemContaID != "acc-1" {
		t.Fatalf("origin = %q, want acc-1", data.OrigemContaID)
	}
	if data.DestinoContaID == data.OrigemContaID {
		t.Fatalf("destination mirrors origin (%q); must differ", data.DestinoContaID)
	}
	if data.DestinoContaID == "" {
		t.Fatal("destination should pre-select a distinct account when one exists")
	}

	single := newFormTransacaoData([]FormAccount{accounts[0]}, nil)
	if single.DestinoContaID != "" || single.DestinoNome != "Conta de destino" {
		t.Fatalf("single-account workspace destination = (%q,%q), want unselected", single.DestinoContaID, single.DestinoNome)
	}
}

func formPutMultipartSameAccount(t *testing.T, path string, values url.Values) *http.Request {
	t.Helper()
	req := formPostMultipart(t, path, values)
	req.Method = http.MethodPut
	return req
}
