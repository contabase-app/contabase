package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type installmentMoveFixture struct {
	refs       []string
	invoiceIDs []string
	txIDs      []string
}

func seedInstallmentMoveChainForTest(t *testing.T, db *sql.DB, prefix string, count int) installmentMoveFixture {
	t.Helper()
	now := time.Now().Unix()
	base := time.Now().UTC().AddDate(2, 0, 0)
	base = time.Date(base.Year(), base.Month(), 1, 12, 0, 0, 0, time.UTC)
	fixture := installmentMoveFixture{
		refs:       make([]string, 0, count),
		invoiceIDs: make([]string, 0, count),
		txIDs:      make([]string, 0, count),
	}
	for i := 0; i < count; i++ {
		referenceDate := base.AddDate(0, i, 0)
		reference := referenceDate.Format("2006-01")
		invoiceID := fmt.Sprintf("invoice-%s-%02d", prefix, i+1)
		closingUnix, dueUnix := invoiceDatesForReference(referenceDate.Year(), referenceDate.Month(), 20, 10)
		execTestSQL(t, db, `
			INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, 'card-test', ?, ?, ?, 'OPEN', ?)
		`, invoiceID, reference, closingUnix, dueUnix, now)
		fixture.refs = append(fixture.refs, reference)
		fixture.invoiceIDs = append(fixture.invoiceIDs, invoiceID)
	}
	fixture.txIDs = seedInstallmentMoveRowsForTest(t, db, prefix, fixture.refs, fixture.invoiceIDs)
	return fixture
}

func seedInstallmentMoveRowsForTest(t *testing.T, db *sql.DB, prefix string, refs, invoiceIDs []string) []string {
	t.Helper()
	now := time.Now().Unix()
	total := len(invoiceIDs)
	parentID := fmt.Sprintf("%s-1", prefix)
	txIDs := make([]string, 0, total)
	for i := 0; i < total; i++ {
		txID := fmt.Sprintf("%s-%d", prefix, i+1)
		var parent interface{}
		if i > 0 {
			parent = parentID
		}
		referenceDate, err := time.Parse("2006-01", refs[i])
		if err != nil {
			t.Fatalf("parse reference %s: %v", refs[i], err)
		}
		competence := referenceDate.AddDate(0, -1, 5)
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
			VALUES (?, 'ws-test', 'user-test', 'card-test', ?, 'EXPENSE', 10000, ?, ?, ?, 'paid', ?, ?, ?, ?, ?)
		`, txID, invoiceIDs[i], competence.Unix(), competence.Format("2006-01-02"), prefix, i+1, total, parent, now, now)
		txIDs = append(txIDs, txID)
	}
	return txIDs
}

func installmentMoveRequestForTest(t *testing.T, db *sql.DB, txID, moveScope string) *http.Request {
	t.Helper()
	form := installmentMoveFormForTest(t, db, txID, moveScope)
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+txID+"/mover-fatura", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func installmentMoveFormForTest(t *testing.T, db *sql.DB, txID, moveScope string) url.Values {
	t.Helper()
	var invoiceID, reference string
	var updatedAt int64
	var parentID, recurringRuleID sql.NullString
	var totalInstallments int64
	if err := db.QueryRow(`
		SELECT t.invoice_id, i.reference, t.updated_at, t.parent_id, t.recurring_rule_id, t.total_installments
		FROM transactions t JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ? AND t.workspace_id = 'ws-test'
	`, txID).Scan(&invoiceID, &reference, &updatedAt, &parentID, &recurringRuleID, &totalInstallments); err != nil {
		t.Fatalf("installment move source %s: %v", txID, err)
	}
	destinationReference, err := nextInvoiceReference(reference)
	if err != nil {
		t.Fatalf("next invoice reference: %v", err)
	}
	expectedScope := "single"
	if parentID.Valid || recurringRuleID.Valid || totalInstallments > 1 {
		expectedScope = "chain"
	}
	form := url.Values{}
	form.Set("expected_invoice_id", invoiceID)
	form.Set("expected_destination_reference", destinationReference)
	form.Set("expected_updated_at", fmt.Sprintf("%d", updatedAt))
	form.Set("expected_move_scope", expectedScope)
	if moveScope != "" {
		form.Set("move_scope", moveScope)
	}
	return form
}

func installmentMoveStateForTest(t *testing.T, db *sql.DB, txID string) (reference, overrideID string, installmentNumber, totalInstallments, amount int64, parentID sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`
		SELECT i.reference, COALESCE(t.invoice_override_id, ''), t.installment_number, t.total_installments, t.amount, t.parent_id
		FROM transactions t JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ?
	`, txID).Scan(&reference, &overrideID, &installmentNumber, &totalInstallments, &amount, &parentID); err != nil {
		t.Fatalf("state for %s: %v", txID, err)
	}
	return
}

func invoiceMoveTotalForTest(t *testing.T, db *sql.DB, invoiceID string) (total, count int64) {
	t.Helper()
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0), COUNT(1)
		FROM transactions
		WHERE invoice_id = ? AND workspace_id = 'ws-test' AND type = 'EXPENSE'
	`, invoiceID).Scan(&total, &count); err != nil {
		t.Fatalf("invoice total for %s: %v", invoiceID, err)
	}
	return
}

func TestHandleMoverFaturaParceladaEscopoSingleMoveSomenteAtual(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	fixture := seedInstallmentMoveChainForTest(t, db, "single-move", 6)

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, fixture.txIDs[2], "single"), fixture.txIDs[2])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	wantRefs := []string{fixture.refs[0], fixture.refs[1], fixture.refs[3], fixture.refs[3], fixture.refs[4], fixture.refs[5]}
	for i, txID := range fixture.txIDs {
		reference, overrideID, installmentNumber, totalInstallments, amount, parentID := installmentMoveStateForTest(t, db, txID)
		if reference != wantRefs[i] {
			t.Fatalf("parcela %d referência = %q, want %q", i+1, reference, wantRefs[i])
		}
		if installmentNumber != int64(i+1) || totalInstallments != 6 || amount != 10000 {
			t.Fatalf("parcela %d metadata alterada: inst=%d total=%d amount=%d", i+1, installmentNumber, totalInstallments, amount)
		}
		if i == 2 {
			if overrideID == "" || overrideID != fixture.invoiceIDs[3] {
				t.Fatalf("parcela movida override = %q, want %q", overrideID, fixture.invoiceIDs[3])
			}
		} else if overrideID != "" {
			t.Fatalf("parcela %d não deveria receber override: %q", i+1, overrideID)
		}
		if i > 0 && (!parentID.Valid || parentID.String != fixture.txIDs[0]) {
			t.Fatalf("parcela %d perdeu parent_id: %v", i+1, parentID)
		}
	}

	sourceTotal, sourceCount := invoiceMoveTotalForTest(t, db, fixture.invoiceIDs[2])
	destinationTotal, destinationCount := invoiceMoveTotalForTest(t, db, fixture.invoiceIDs[3])
	if sourceTotal != 0 || sourceCount != 0 {
		t.Fatalf("fatura de origem = total %d count %d, want 0/0", sourceTotal, sourceCount)
	}
	if destinationTotal != 20000 || destinationCount != 2 {
		t.Fatalf("fatura de destino = total %d count %d, want 20000/2", destinationTotal, destinationCount)
	}
}

func TestHandleMoverFaturaParceladaEscopoFuturoDeslocaCadeia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	fixture := seedInstallmentMoveChainForTest(t, db, "future-move", 6)
	lastNextReference, err := nextInvoiceReference(fixture.refs[5])
	if err != nil {
		t.Fatal(err)
	}

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, fixture.txIDs[2], "future"), fixture.txIDs[2])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	wantRefs := []string{fixture.refs[0], fixture.refs[1], fixture.refs[3], fixture.refs[4], fixture.refs[5], lastNextReference}
	for i, txID := range fixture.txIDs {
		reference, overrideID, installmentNumber, totalInstallments, amount, _ := installmentMoveStateForTest(t, db, txID)
		if reference != wantRefs[i] {
			t.Fatalf("parcela %d referência = %q, want %q", i+1, reference, wantRefs[i])
		}
		if installmentNumber != int64(i+1) || totalInstallments != 6 || amount != 10000 {
			t.Fatalf("parcela %d metadata alterada: inst=%d total=%d amount=%d", i+1, installmentNumber, totalInstallments, amount)
		}
		if i >= 2 {
			if overrideID == "" {
				t.Fatalf("parcela %d futura sem override", i+1)
			}
		} else if overrideID != "" {
			t.Fatalf("parcela %d anterior recebeu override: %q", i+1, overrideID)
		}
	}

	for i := 3; i <= 5; i++ {
		total, count := invoiceMoveTotalForTest(t, db, fixture.invoiceIDs[i])
		if total != 10000 || count != 1 {
			t.Fatalf("fatura %s após deslocamento = total %d count %d, want 10000/1", fixture.refs[i], total, count)
		}
	}
	var newInvoiceID string
	if err := db.QueryRow(`SELECT id FROM invoices WHERE account_id = 'card-test' AND reference = ?`, lastNextReference).Scan(&newInvoiceID); err != nil {
		t.Fatalf("new invoice %s: %v", lastNextReference, err)
	}
	newTotal, newCount := invoiceMoveTotalForTest(t, db, newInvoiceID)
	if newTotal != 10000 || newCount != 1 {
		t.Fatalf("fatura final = total %d count %d, want 10000/1", newTotal, newCount)
	}
}

func TestHandleMoverFaturaFuturoComFaturaPagaFazRollbackIntegral(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	fixture := seedInstallmentMoveChainForTest(t, db, "paid-block", 6)

	execTestSQL(t, db, `
		UPDATE invoices SET status = 'PAID', paid_at = ?, paid_amount = 0
		WHERE id = ? AND account_id = 'card-test'
	`, time.Now().Unix(), fixture.invoiceIDs[4])

	var invoicesBefore int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoices WHERE account_id = 'card-test'`).Scan(&invoicesBefore); err != nil {
		t.Fatal(err)
	}

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, fixture.txIDs[2], "future"), fixture.txIDs[2])
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}

	for i, txID := range fixture.txIDs {
		reference, overrideID, _, _, _, _ := installmentMoveStateForTest(t, db, txID)
		if reference != fixture.refs[i] || overrideID != "" {
			t.Fatalf("parcela %d mudou após rollback: ref=%q override=%q", i+1, reference, overrideID)
		}
	}
	var invoicesAfter int
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoices WHERE account_id = 'card-test'`).Scan(&invoicesAfter); err != nil {
		t.Fatal(err)
	}
	if invoicesAfter != invoicesBefore {
		t.Fatalf("faturas criadas durante rollback: before=%d after=%d", invoicesBefore, invoicesAfter)
	}
}

func TestHandleMoverFaturaFuturoIsolaOutraSerieNaMesmaFatura(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	fixture := seedInstallmentMoveChainForTest(t, db, "series-a", 6)
	otherPrefix := "series-b"
	otherTxIDs := seedInstallmentMoveRowsForTest(t, db, otherPrefix, fixture.refs[1:4], fixture.invoiceIDs[1:4])
	otherRefs := []string{fixture.refs[1], fixture.refs[2], fixture.refs[3]}

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, fixture.txIDs[2], "future"), fixture.txIDs[2])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	for i, txID := range otherTxIDs {
		reference, overrideID, _, _, _, _ := installmentMoveStateForTest(t, db, txID)
		if reference != otherRefs[i] || overrideID != "" {
			t.Fatalf("série B parcela %d alterada: ref=%q override=%q", i+1, reference, overrideID)
		}
	}
}

func TestHandleMoverFaturaReplayComEscopoFuturoNaoDuplicaMovimento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	fixture := seedInstallmentMoveChainForTest(t, db, "replay-move", 6)
	firstForm := installmentMoveFormForTest(t, db, fixture.txIDs[2], "future")

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	first := httptest.NewRecorder()
	reqFirst := httptest.NewRequest(http.MethodPost, "/transacoes/"+fixture.txIDs[2]+"/mover-fatura", strings.NewReader(firstForm.Encode()))
	reqFirst.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.HandleMoverFatura(first, reqFirst, fixture.txIDs[2])
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %q", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	reqSecond := httptest.NewRequest(http.MethodPost, "/transacoes/"+fixture.txIDs[2]+"/mover-fatura", strings.NewReader(firstForm.Encode()))
	reqSecond.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.HandleMoverFatura(second, reqSecond, fixture.txIDs[2])
	if second.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409, body = %q", second.Code, second.Body.String())
	}

	lastNextReference, err := nextInvoiceReference(fixture.refs[5])
	if err != nil {
		t.Fatal(err)
	}
	wantRefs := []string{fixture.refs[0], fixture.refs[1], fixture.refs[3], fixture.refs[4], fixture.refs[5], lastNextReference}
	for i, txID := range fixture.txIDs {
		reference, _, _, _, _, _ := installmentMoveStateForTest(t, db, txID)
		if reference != wantRefs[i] {
			t.Fatalf("parcela %d após replay = %q, want %q", i+1, reference, wantRefs[i])
		}
	}
}

func TestHandleMoverFaturaRecorrenciaRejeitaEscopoDeParcelamento(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "OPEN")
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES ('rule-move-test', 'ws-test', 'user-test', 'card-test', 'EXPENSE', 5000, 'Recorrente teste', ?, 'MONTHLY', 'PAID', 1, ?, ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
		VALUES ('recurring-move-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 5000, ?, 'Recorrente teste', 'paid', 1, 1, 'rule-move-test', 1, ?, ?)
	`, now, now, now)

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, "recurring-move-test", "single"), "recurring-move-test")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %q", rr.Code, rr.Body.String())
	}
	var invoiceID string
	var overrideID sql.NullString
	if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE id = 'recurring-move-test'`).Scan(&invoiceID, &overrideID); err != nil {
		t.Fatal(err)
	}
	if invoiceID != "invoice-2026-08" || overrideID.Valid {
		t.Fatalf("recorrência alterada: invoice=%q override=%v", invoiceID, overrideID)
	}
}

func TestHandleMoverFaturaParceladaSemNumeracaoValidaBloqueia(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	fixture := seedInstallmentMoveChainForTest(t, db, "invalid-number", 6)
	execTestSQL(t, db, `UPDATE transactions SET installment_number = 0 WHERE id = ?`, fixture.txIDs[2])

	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, fixture.txIDs[2], "future"), fixture.txIDs[2])
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}
	reference, overrideID, _, _, _, _ := installmentMoveStateForTest(t, db, fixture.txIDs[2])
	if reference != fixture.refs[2] || overrideID != "" {
		t.Fatalf("parcela inválida mudou: ref=%q override=%q", reference, overrideID)
	}
}

func TestHandleMoverFaturaEscopoFuturoRejeitaNaoParcelado(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	handler := TransactionHandler{DB: db, Templates: testOOBTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
	rr := httptest.NewRecorder()
	handler.HandleMoverFatura(rr, installmentMoveRequestForTest(t, db, "purchase-test", "future"), "purchase-test")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %q", rr.Code, rr.Body.String())
	}
}

func TestFaturasInstallmentMoveUsesExplicitScopeChoice(t *testing.T) {
	row := stageDTransactionRow(0)
	row.ID = "tx-scope-choice"
	row.IsSeries = true
	row.IsInstallmentSeries = true
	row.InstallmentNumber = 3
	row.TotalInstallments = 6
	row.HasInstallmentInfo = true
	row.UpdatedAt = 123
	data := FaturaData{
		InvoiceID:           "invoice-scope-choice",
		NextReference:       "2030-04",
		CanMutateItems:      true,
		InitialVisibleItems: 30,
		Transactions:        []TransactionRow{row},
		TransactionGroups: []TransactionDayGroup{{
			DateLabel:    row.DateLabel,
			FirstIndex:   0,
			Transactions: []TransactionRow{row},
		}},
	}
	rendered := html.UnescapeString(renderInvoiceTransactionsForStageD(t, data))
	for _, expected := range []string{
		`data-swipe-right-move-scope="true"`,
		`data-installment-label="3/6"`,
		`data-onclick-ev="openInvoiceMoveScopeFromButton"`,
		`data-invoice-move-url="/transacoes/tx-scope-choice/mover-fatura"`,
		`data-invoice-move-label="3/6"`,
		`"expected_move_scope":"chain"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("installment move markup missing %q", expected)
		}
	}
	if strings.Contains(rendered, `hx-post="/transacoes/tx-scope-choice/mover-fatura"`) {
		t.Fatal("installment row must open the scope choice instead of posting directly")
	}

	shell := readTestFile(t, "templates/components/app_shell.html")
	for _, expected := range []string{
		`id="invoice-move-scope-modal"`,
		`name="invoice_move_scope" value="single"`,
		`name="invoice_move_scope" value="future"`,
		`data-onclick="submitInvoiceMoveScope"`,
	} {
		if !strings.Contains(shell, expected) {
			t.Fatalf("shared shell missing move scope contract %q", expected)
		}
	}
	js := readTestFile(t, "assets/js/faturas.js")
	for _, expected := range []string{
		"window.openInvoiceMoveScopeFromRow",
		"window.openInvoiceMoveScopeFromButton",
		"move_scope: scope",
		"window.htmx.ajax('POST', pending.url, options)",
	} {
		if !strings.Contains(js, expected) {
			t.Fatalf("faturas.js missing move scope contract %q", expected)
		}
	}
}

func TestFormLancamentoInstallmentMoveUsesScopeChoice(t *testing.T) {
	data := FormTransacaoData{
		IsEdit:                  true,
		EditID:                  "tx-sheet-scope",
		ReturnInvoiceID:         "invoice-sheet",
		QuickMoveDestinationRef: "2030-04",
		TransactionUpdatedAt:    123,
		QuickMoveScope:          "chain",
		IsInstallmentSeriesEdit: true,
		InstallmentNumber:       3,
		TotalInstallments:       6,
	}
	var buf bytes.Buffer
	if err := manualInvoiceTemplateForTest(t).ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
		t.Fatalf("execute form-lancamento: %v", err)
	}
	rendered := html.UnescapeString(buf.String())
	for _, expected := range []string{
		`data-onclick-ev="openInvoiceMoveScopeFromButton"`,
		`data-invoice-move-url="/transacoes/tx-sheet-scope/mover-fatura"`,
		`data-invoice-move-label="3/6"`,
		`"expected_move_scope":"chain"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("sheet installment move missing %q", expected)
		}
	}
	if strings.Contains(rendered, `hx-post="/transacoes/tx-sheet-scope/mover-fatura"`) {
		t.Fatal("sheet installment move must open the scope choice instead of posting directly")
	}
}
