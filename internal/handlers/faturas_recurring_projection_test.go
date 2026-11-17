package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type invoiceRecurringFixture struct {
	db          *sql.DB
	base        f1Base
	reference   string
	invoiceID   string
	seriesID    string
	ruleID      string
	txID        string
	occurrence  time.Time
	otherCard   string
	otherTxID   string
	otherInvID  string
	otherWSTxID string
}

// seedInvoiceRecurringFixture builds a credit-card invoice with one materialized
// occurrence of an active monthly recurrence. Both the invoice and the
// transaction belong to base.ws only.
func seedInvoiceRecurringFixture(t *testing.T, ws string) invoiceRecurringFixture {
	t.Helper()
	now := time.Now().UTC()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	base := seedF1Base(t, db, ws)
	fixture := invoiceRecurringFixture{
		db:         db,
		base:       base,
		reference:  "2026-11",
		invoiceID:  "invoice-" + ws,
		seriesID:   "series-" + ws,
		ruleID:     "rule-" + ws,
		txID:       "tx-" + ws,
		otherInvID: "invoice-other-" + ws,
		otherCard:  "card-other-" + ws,
		otherTxID:  "tx-other-" + ws,
	}

	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES (?, ?, 20, 10, 1000000)
	`, "cc-"+ws, base.card)

	cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
	occurrence := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
	if occurrence.Unix() >= cycleEnd {
		t.Fatalf("fixture occurrence %s outside invoice cycle", occurrence)
	}
	fixture.occurrence = occurrence
	occurrenceCivil := occurrence.Format("2006-01-02")

	addF1Invoice(t, db, fixture.invoiceID, base.card, fixture.reference, "OPEN", 0, now.Unix())
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active)
		VALUES (?, ?, ?, 1)
	`, fixture.seriesID, base.ws, base.user)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id,
			type, amount, description, start_date, start_date_civil,
			starts_at, starts_at_civil, frequency, default_payment_status,
			active, series_id, effective_start_sequence, due_date_offset_days,
			created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?,
			'EXPENSE', 12990, 'Assinatura sintética', ?, ?,
			?, ?, 'MONTHLY', 'PENDING',
			1, ?, 1, 0,
			?, ?
		)
	`, fixture.ruleID, base.ws, base.user, base.card, base.category,
		occurrence.Unix(), occurrenceCivil,
		occurrence.Unix(), occurrenceCivil,
		fixture.seriesID, now.Unix(), now.Unix())
	addF1Tx(t, db, f1TxSeed{
		ID: fixture.txID, WorkspaceID: base.ws, UserID: base.user, AccountID: base.card,
		CategoryID: base.category, InvoiceID: fixture.invoiceID, Type: "EXPENSE", Amount: 12990,
		Status: "paid", Description: "Assinatura sintética",
		RecurringRuleID: fixture.ruleID, SeriesID: fixture.seriesID, RecurrenceSequence: 1,
		CreatedAt: occurrence.Unix(),
	})

	// A second invoice in the same workspace proves the swipe URL cannot target
	// an invoice other than the row's own invoice.
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Cartão secundário', 'CREDIT_CARD', 0, 0, ?, ?)
	`, fixture.otherCard, base.ws, now.Unix(), now.Unix())
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES (?, ?, 20, 10, 1000000)
	`, "cc-other-"+ws, fixture.otherCard)
	addF1Invoice(t, db, fixture.otherInvID, fixture.otherCard, fixture.reference, "OPEN", 0, now.Unix())
	addF1Tx(t, db, f1TxSeed{
		ID: fixture.otherTxID, WorkspaceID: base.ws, UserID: base.user, AccountID: fixture.otherCard,
		CategoryID: base.category, InvoiceID: fixture.otherInvID, Type: "EXPENSE", Amount: 5000,
		Status: "paid", Description: "Compra em outra fatura", CreatedAt: occurrence.Unix(),
	})

	// And a foreign workspace transaction keeps the tenant boundary.
	foreign := seedF1Base(t, db, ws+"-foreign")
	addF1Invoice(t, db, "invoice-foreign-"+ws, foreign.card, fixture.reference, "OPEN", 0, now.Unix())
	addF1Tx(t, db, f1TxSeed{
		ID: "tx-foreign-" + ws, WorkspaceID: foreign.ws, UserID: foreign.user, AccountID: foreign.card,
		CategoryID: foreign.category, InvoiceID: "invoice-foreign-" + ws, Type: "EXPENSE", Amount: 7700,
		Status: "paid", Description: "Compra de outro workspace", CreatedAt: occurrence.Unix(),
	})
	fixture.otherWSTxID = "tx-foreign-" + ws

	return fixture
}

func deleteInvoiceTransaction(t *testing.T, fixture invoiceRecurringFixture, transactionID, invoiceID string) *httptest.ResponseRecorder {
	t.Helper()
	handler := TransactionHandler{DB: fixture.db, WorkspaceID: fixture.base.ws, UserID: fixture.base.user, Templates: testOOBTemplates(t)}
	req := httptest.NewRequest(http.MethodDelete, "/transacoes/"+transactionID+"?invoice_id="+invoiceID, nil)
	req.Header.Set("HX-Current-URL", "http://localhost/lancamentos")
	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, req, transactionID)
	return rr
}

func countTransactions(t *testing.T, db *sql.DB, workspaceID, transactionID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = ? AND id = ?`, workspaceID, transactionID).Scan(&count); err != nil {
		t.Fatalf("count transaction: %v", err)
	}
	return count
}

// Swipe right on an invoice row runs the canonical DELETE with the invoice
// context. For a recurring series that delete records a durable occurrence
// skip; the invoice projection must honor that skip instead of rendering the
// deleted expense again as "Previsto".
func TestInvoiceDeletedRecurringOccurrenceDoesNotReappearAsProjected(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	fixture := seedInvoiceRecurringFixture(t, "ws-invoice-projection")

	if rows, err := projectedRecurringInvoiceRows(fixture.db, fixture.base.ws, fixture.base.card, fixture.reference); err != nil {
		t.Fatalf("projection before delete: %v", err)
	} else if len(rows) != 0 {
		t.Fatalf("materialized occurrence must not be projected: %+v", rows)
	}

	rr := deleteInvoiceTransaction(t, fixture, fixture.txID, fixture.invoiceID)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if countTransactions(t, fixture.db, fixture.base.ws, fixture.txID) != 0 {
		t.Fatal("deleted transaction still exists")
	}
	var skipCount int
	if err := fixture.db.QueryRow(`
		SELECT COUNT(1) FROM recurring_occurrence_skips
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = 1
	`, fixture.base.ws, fixture.seriesID).Scan(&skipCount); err != nil {
		t.Fatalf("count skip: %v", err)
	}
	if skipCount != 1 {
		t.Fatalf("occurrence skip count = %d, want 1", skipCount)
	}

	// RED before the fix: the projection only checked materialized rows, so
	// the skipped occurrence returned as a projected "Previsto" row.
	rows, err := projectedRecurringInvoiceRows(fixture.db, fixture.base.ws, fixture.base.card, fixture.reference)
	if err != nil {
		t.Fatalf("projection after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("deleted occurrence reappeared as projected: %+v", rows)
	}
	invoiceData, err := buildFaturaDataForInvoice(fixture.db, fixture.base.ws, fixture.invoiceID, "desc")
	if err != nil {
		t.Fatalf("build invoice after delete: %v", err)
	}
	if len(invoiceData.Transactions) != 0 {
		t.Fatalf("invoice list still renders the deleted occurrence: %+v", invoiceData.Transactions)
	}
	if total, count, err := CalculateVirtualInvoiceTotal(fixture.db, fixture.base.ws, fixture.base.card, fixture.reference); err != nil || total != 0 || count != 0 {
		t.Fatalf("virtual invoice total after delete = %d/%d err=%v, want 0/0", total, count, err)
	}

	// Only that occurrence is skipped: the active recurrence still projects the
	// following month.
	nextOccurrence := nextRecurrenceDate(fixture.occurrence, "MONTHLY")
	_, _, nextReference := calculateInvoiceDates(nextOccurrence, 20, 10)
	if nextReference == fixture.reference {
		t.Fatal("fixture must span two invoice references")
	}
	nextRows, err := projectedRecurringInvoiceRows(fixture.db, fixture.base.ws, fixture.base.card, nextReference)
	if err != nil {
		t.Fatalf("projection next invoice: %v", err)
	}
	found := false
	for _, row := range nextRows {
		if row.Date == nextOccurrence.Unix() {
			found = true
		}
	}
	if !found {
		t.Fatalf("recurrence lost its next occurrence in %s: %+v", nextReference, nextRows)
	}
}

func TestInvoiceSwipeDeleteRejectsMismatchedInvoiceAndForeignWorkspace(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	fixture := seedInvoiceRecurringFixture(t, "ws-invoice-guard")

	rr := deleteInvoiceTransaction(t, fixture, fixture.txID, fixture.otherInvID)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("mismatched invoice status = %d, want 404", rr.Code)
	}
	if countTransactions(t, fixture.db, fixture.base.ws, fixture.txID) != 1 {
		t.Fatal("mismatched invoice must not delete the row")
	}

	rr = deleteInvoiceTransaction(t, fixture, fixture.otherWSTxID, "invoice-foreign-"+fixture.base.ws)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace status = %d, want 404", rr.Code)
	}
	if countTransactions(t, fixture.db, fixture.base.ws+"-foreign", fixture.otherWSTxID) != 1 {
		t.Fatal("foreign workspace row must survive")
	}
}
