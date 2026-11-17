package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Versioned invoice materialization must use the series sequence
// (effective_start_sequence + local offset), never the loop position. These
// fixtures stay synthetic and deterministic.

type recurringInvoiceRuleTestSeed struct {
	ID               string
	SeriesID         string
	WorkspaceID      string
	UserID           string
	AccountID        string
	CategoryID       string
	Amount           int64
	Description      string
	Start            time.Time
	Frequency        string
	EffectiveStart   int64
	EffectiveEnd     sql.NullInt64
	TotalOccurrences sql.NullInt64
	EndDateCivil     sql.NullString
	Active           bool
}

func newRecurringInvoiceTestBase(t *testing.T, ws string) (*sql.DB, f1Base) {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	base := seedF1Base(t, db, ws)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES (?, ?, 20, 10, 1000000)
	`, "cc-"+ws, base.card)
	return db, base
}

func addOpenInvoiceFutureClosing(t *testing.T, db *sql.DB, id, accountID, reference string, now time.Time) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES (?, ?, ?, ?, ?, 'OPEN', 0, ?)
	`, id, accountID, reference, now.AddDate(0, 0, 30).Unix(), now.AddDate(0, 0, 40).Unix(), now.Unix())
}

func addRecurringInvoiceRule(t *testing.T, db *sql.DB, seed recurringInvoiceRuleTestSeed) {
	t.Helper()
	active := 0
	if seed.Active {
		active = 1
	}
	frequency := seed.Frequency
	if frequency == "" {
		frequency = "MONTHLY"
	}
	civil := seed.Start.Format("2006-01-02")
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id,
			type, amount, description, start_date, start_date_civil,
			starts_at, starts_at_civil, frequency, default_payment_status,
			active, series_id, effective_start_sequence, effective_end_sequence,
			total_occurrences, end_date_civil, due_date_offset_days,
			created_at, updated_at
		) VALUES (
			?, ?, ?, ?, ?,
			'EXPENSE', ?, ?, ?, ?,
			?, ?, ?, 'PENDING',
			?, ?, ?, ?,
			?, ?, 0,
			?, ?
		)
	`, seed.ID, seed.WorkspaceID, seed.UserID, seed.AccountID, seed.CategoryID,
		seed.Amount, seed.Description, seed.Start.Unix(), civil,
		seed.Start.Unix(), civil, frequency,
		active, seed.SeriesID, seed.EffectiveStart, f1NullableInt64(seed.EffectiveEnd),
		f1NullableInt64(seed.TotalOccurrences), f1NullableString(seed.EndDateCivil.String),
		seed.Start.Unix(), seed.Start.Unix())
}

func addRecurringInvoiceSeries(t *testing.T, db *sql.DB, id, ws, userID string) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active)
		VALUES (?, ?, ?, 1)
	`, id, ws, userID)
}

func f1NullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func addRecurringInvoiceTx(t *testing.T, db *sql.DB, base f1Base, id, ruleID, seriesID, invoiceID string, amount, sequence int64, at time.Time) {
	t.Helper()
	addF1Tx(t, db, f1TxSeed{
		ID: id, WorkspaceID: base.ws, UserID: base.user, AccountID: base.card,
		CategoryID: base.category, InvoiceID: invoiceID, Type: "EXPENSE", Amount: amount,
		Status: "paid", Description: "Assinatura sintética",
		RecurringRuleID: ruleID, SeriesID: seriesID, RecurrenceSequence: sequence,
		CreatedAt: at.Unix(),
	})
}

func ruleSequences(t *testing.T, db *sql.DB, workspaceID, ruleID string) []int64 {
	t.Helper()
	rows, err := db.Query(`
		SELECT recurrence_sequence FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ?
		ORDER BY recurrence_sequence
	`, workspaceID, ruleID)
	if err != nil {
		t.Fatalf("query sequences: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan sequence: %v", err)
		}
		out = append(out, seq)
	}
	return out
}

func assertSequences(t *testing.T, db *sql.DB, workspaceID, ruleID string, want ...int64) {
	t.Helper()
	got := ruleSequences(t, db, workspaceID, ruleID)
	if len(got) != len(want) {
		t.Fatalf("sequences = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequences = %v, want %v", got, want)
		}
	}
}

type versionedInvoiceScenario struct {
	db         *sql.DB
	base       f1Base
	reference  string
	invoiceID  string
	seriesID   string
	ruleID     string
	txID       string
	occurrence time.Time
}

func newVersionedInvoiceScenario(t *testing.T, ws string, effectiveStart int64) versionedInvoiceScenario {
	t.Helper()
	db, base := newRecurringInvoiceTestBase(t, ws)
	now := time.Now().UTC()
	reference := "2026-11"
	cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
	occurrence := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
	if occurrence.Unix() >= cycleEnd {
		t.Fatalf("fixture occurrence outside invoice cycle")
	}
	scenario := versionedInvoiceScenario{
		db:         db,
		base:       base,
		reference:  reference,
		invoiceID:  "invoice-" + ws,
		seriesID:   "series-" + ws,
		ruleID:     "rule-" + ws,
		txID:       "tx-" + ws,
		occurrence: occurrence,
	}
	addOpenInvoiceFutureClosing(t, db, scenario.invoiceID, base.card, reference, now)
	addRecurringInvoiceSeries(t, db, scenario.seriesID, base.ws, base.user)
	addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
		ID: scenario.ruleID, SeriesID: scenario.seriesID, WorkspaceID: base.ws,
		UserID: base.user, AccountID: base.card, CategoryID: base.category,
		Amount: 12990, Description: "Assinatura sintética", Start: occurrence,
		EffectiveStart: effectiveStart, Active: true,
	})
	addRecurringInvoiceTx(t, db, base, scenario.txID, scenario.ruleID, scenario.seriesID, scenario.invoiceID, 12990, effectiveStart, occurrence)
	return scenario
}

func deleteInvoiceTransactionViaSwipe(t *testing.T, scenario versionedInvoiceScenario) *httptest.ResponseRecorder {
	t.Helper()
	handler := TransactionHandler{DB: scenario.db, WorkspaceID: scenario.base.ws, UserID: scenario.base.user, Templates: testOOBTemplates(t)}
	req := httptest.NewRequest(http.MethodDelete, "/transacoes/"+scenario.txID+"?invoice_id="+scenario.invoiceID, nil)
	req.Header.Set("HX-Current-URL", "http://localhost/lancamentos")
	rr := httptest.NewRecorder()
	handler.HandleDeletarTransacao(rr, req, scenario.txID)
	return rr
}

func addNextOpenInvoice(t *testing.T, scenario versionedInvoiceScenario) (string, time.Time) {
	t.Helper()
	next := nextRecurrenceDate(scenario.occurrence, "MONTHLY")
	_, _, nextReference := calculateInvoiceDates(next, 20, 10)
	if nextReference == scenario.reference {
		t.Fatalf("fixture must span two invoice references")
	}
	invoiceID := scenario.invoiceID + "-next"
	addOpenInvoiceFutureClosing(t, scenario.db, invoiceID, scenario.base.card, nextReference, time.Now().UTC())
	return invoiceID, next
}

// RED 1: deleting a versioned occurrence must not resurrect it with a local
// sequence, and the next legitimate occurrence must use the series sequence.
func TestInvoiceVersionedRecurringDeleteDoesNotResurrect(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-red-versioned", 5)

	rr := deleteInvoiceTransactionViaSwipe(t, scenario)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID)
	var skipAtFive, skipAtOne int
	scenario.db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips WHERE workspace_id=? AND series_id=? AND recurrence_sequence=5`, scenario.base.ws, scenario.seriesID).Scan(&skipAtFive)
	scenario.db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_skips WHERE workspace_id=? AND series_id=? AND recurrence_sequence=1`, scenario.base.ws, scenario.seriesID).Scan(&skipAtOne)
	if skipAtFive != 1 || skipAtOne != 0 {
		t.Fatalf("skip identity = %d/%d, want 5=1 and 1=0", skipAtFive, skipAtOne)
	}

	rows, err := projectedRecurringInvoiceRows(scenario.db, scenario.base.ws, scenario.base.card, scenario.reference)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("deleted occurrence projected: %+v", rows)
	}
	if total, count, err := CalculateVirtualInvoiceTotal(scenario.db, scenario.base.ws, scenario.base.card, scenario.reference); err != nil || total != 0 || count != 0 {
		t.Fatalf("virtual total = %d/%d err=%v, want 0/0", total, count, err)
	}
	data, err := buildFaturaDataForInvoice(scenario.db, scenario.base.ws, scenario.invoiceID, "desc")
	if err != nil {
		t.Fatalf("invoice after delete: %v", err)
	}
	if len(data.Transactions) != 0 {
		t.Fatalf("invoice list still shows the deleted occurrence: %+v", data.Transactions)
	}

	nextInvoiceID, nextOccurrence := addNextOpenInvoice(t, scenario)
	if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
		t.Fatalf("materialize next invoice: %v", err)
	}
	assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 6)
	var nextDate int64
	scenario.db.QueryRow(`SELECT date FROM transactions WHERE workspace_id=? AND recurring_rule_id=? AND recurrence_sequence=6`, scenario.base.ws, scenario.ruleID).Scan(&nextDate)
	if nextDate != nextOccurrence.Unix() {
		t.Fatalf("next occurrence date = %d, want %d", nextDate, nextOccurrence.Unix())
	}

	// Repeating the delete must not recreate anything.
	handler := TransactionHandler{DB: scenario.db, WorkspaceID: scenario.base.ws, UserID: scenario.base.user, Templates: testOOBTemplates(t)}
	repeat := httptest.NewRequest(http.MethodDelete, "/transacoes/"+scenario.txID+"?invoice_id="+scenario.invoiceID, nil)
	repeat.Header.Set("HX-Current-URL", "http://localhost/lancamentos")
	repeatRR := httptest.NewRecorder()
	handler.HandleDeletarTransacao(repeatRR, repeat, scenario.txID)
	if repeatRR.Code != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d, want 404", repeatRR.Code)
	}
	assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 6)
}

// RED 2: a frozen block from an older version must not be resolved with the
// active rule's local sequence.
func TestInvoiceVersionedDeletePreservesOldFrozenBlock(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-red-block", 5)
	now := time.Now().UTC()

	oldRuleID := scenario.ruleID + "-old"
	oldInvoiceID := scenario.invoiceID + "-old"
	oldOccurrence := time.Unix(safeAddMonths(scenario.occurrence.Unix(), -4), 0).UTC()
	_, _, oldReference := calculateInvoiceDates(oldOccurrence, 20, 10)
	addF1Invoice(t, scenario.db, oldInvoiceID, scenario.base.card, oldReference, "CLOSED", 0, now.Unix())
	addRecurringInvoiceRule(t, scenario.db, recurringInvoiceRuleTestSeed{
		ID: oldRuleID, SeriesID: scenario.seriesID, WorkspaceID: scenario.base.ws,
		UserID: scenario.base.user, AccountID: scenario.base.card, CategoryID: scenario.base.category,
		Amount: 12990, Description: "Assinatura sintética", Start: oldOccurrence,
		EffectiveStart: 1, EffectiveEnd: sql.NullInt64{Int64: 4, Valid: true},
		EndDateCivil: sql.NullString{String: oldOccurrence.Format("2006-01-02"), Valid: true}, Active: false,
	})
	execTestSQL(t, scenario.db, `
		INSERT INTO recurring_occurrence_blocks (
			workspace_id, series_id, recurring_rule_id, recurrence_sequence,
			occurrence_date_civil, invoice_id, invoice_reference, invoice_status,
			reason, created_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, 'CLOSED', 'frozen_invoice', ?)
	`, scenario.base.ws, scenario.seriesID, oldRuleID, oldOccurrence.Format("2006-01-02"), oldInvoiceID, oldReference, now.Unix())

	rr := deleteInvoiceTransactionViaSwipe(t, scenario)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID)
	var blocks int
	if err := scenario.db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE workspace_id=? AND series_id=?`, scenario.base.ws, scenario.seriesID).Scan(&blocks); err != nil {
		t.Fatalf("count blocks: %v", err)
	}
	if blocks != 1 {
		t.Fatalf("frozen block count = %d, want 1", blocks)
	}
	if _, err := buildFaturaDataForInvoice(scenario.db, scenario.base.ws, scenario.invoiceID, "desc"); err != nil {
		t.Fatalf("invoice must render after delete: %v", err)
	}
}

// RED 3: series bounds must be applied to the effective sequence.
func TestInvoiceVersionedMaterializationLimits(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	t.Run("effective_end_sequence", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-end", 5)
		execTestSQL(t, scenario.db, `UPDATE recurring_rules SET effective_end_sequence = 5 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.ruleID)
		execTestSQL(t, scenario.db, `DELETE FROM transactions WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.txID)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5)

		nextInvoiceID, _ := addNextOpenInvoice(t, scenario)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
			t.Fatalf("materialize next: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5)
	})

	t.Run("total_occurrences", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-total", 5)
		execTestSQL(t, scenario.db, `UPDATE recurring_rules SET total_occurrences = 6 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.ruleID)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		nextInvoiceID, _ := addNextOpenInvoice(t, scenario)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
			t.Fatalf("materialize next: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5, 6)
	})

	t.Run("series_total_occurrences", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-series-total", 5)
		execTestSQL(t, scenario.db, `UPDATE recurring_series SET total_occurrences = 6 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.seriesID)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		nextInvoiceID, _ := addNextOpenInvoice(t, scenario)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
			t.Fatalf("materialize next: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5, 6)
	})

	t.Run("end_date", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-date", 5)
		execTestSQL(t, scenario.db, `UPDATE recurring_rules SET end_date_civil = ? WHERE workspace_id=? AND id=?`, scenario.occurrence.Format("2006-01-02"), scenario.base.ws, scenario.ruleID)
		nextInvoiceID, _ := addNextOpenInvoice(t, scenario)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
			t.Fatalf("materialize next: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5)
	})

	t.Run("end_date_unix", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-date-unix", 5)
		execTestSQL(t, scenario.db, `UPDATE recurring_rules SET end_date = ? WHERE workspace_id=? AND id=?`, scenario.occurrence.Unix(), scenario.base.ws, scenario.ruleID)
		nextInvoiceID, _ := addNextOpenInvoice(t, scenario)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
			t.Fatalf("materialize next: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5)
	})

	t.Run("legacy_start_one", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-legacy", 1)
		nextInvoiceID, _ := addNextOpenInvoice(t, scenario)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, nextInvoiceID); err != nil {
			t.Fatalf("materialize next: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 1, 2)
	})

	t.Run("multiple_active_versions", func(t *testing.T) {
		db, base := newRecurringInvoiceTestBase(t, "ws-red-chain-active")
		now := time.Now().UTC()
		reference := "2026-11"
		cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
		occurrenceB := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
		if occurrenceB.Unix() >= cycleEnd {
			t.Fatalf("occurrence outside cycle")
		}
		occurrenceA := time.Unix(safeAddMonths(occurrenceB.Unix(), -2), 0).UTC()
		invoiceID := "invoice-" + base.ws
		addOpenInvoiceFutureClosing(t, db, invoiceID, base.card, reference, now)
		seriesID := "series-" + base.ws
		addRecurringInvoiceSeries(t, db, seriesID, base.ws, base.user)
		addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
			ID: "rule-a-" + base.ws, SeriesID: seriesID, WorkspaceID: base.ws, UserID: base.user,
			AccountID: base.card, CategoryID: base.category, Amount: 1000,
			Description: "Versão A", Start: occurrenceA, EffectiveStart: 1,
			EffectiveEnd: sql.NullInt64{Int64: 2, Valid: true}, Active: true,
		})
		addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
			ID: "rule-b-" + base.ws, SeriesID: seriesID, WorkspaceID: base.ws, UserID: base.user,
			AccountID: base.card, CategoryID: base.category, Amount: 2000,
			Description: "Versão B", Start: occurrenceB, EffectiveStart: 3, Active: true,
		})
		if err := materializeRecurringInvoiceTransactions(db, base.ws, invoiceID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		assertSequences(t, db, base.ws, "rule-a-"+base.ws)
		assertSequences(t, db, base.ws, "rule-b-"+base.ws, 3)
	})

	t.Run("consecutive_versions", func(t *testing.T) {
		scenario := newVersionedInvoiceScenario(t, "ws-red-chain", 3)
		// The previous version is inactive and must not be materialized.
		addRecurringInvoiceRule(t, scenario.db, recurringInvoiceRuleTestSeed{
			ID: scenario.ruleID + "-previous", SeriesID: scenario.seriesID, WorkspaceID: scenario.base.ws,
			UserID: scenario.base.user, AccountID: scenario.base.card, CategoryID: scenario.base.category,
			Amount: 12990, Description: "Assinatura sintética",
			Start:          time.Unix(safeAddMonths(scenario.occurrence.Unix(), -2), 0).UTC(),
			EffectiveStart: 1, EffectiveEnd: sql.NullInt64{Int64: 2, Valid: true}, Active: false,
		})
		execTestSQL(t, scenario.db, `DELETE FROM transactions WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.txID)
		if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 3)
	})
}

// P2 A: a frozen block is terminal and must never be projected.
func TestInvoiceProjectionIgnoresFrozenBlocks(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	db, base := newRecurringInvoiceTestBase(t, "ws-red-block-projection")
	now := time.Now().UTC()
	reference := "2026-11"
	cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
	occurrence := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
	if occurrence.Unix() >= cycleEnd {
		t.Fatalf("occurrence outside cycle")
	}
	invoiceID := "invoice-" + base.ws
	addF1Invoice(t, db, invoiceID, base.card, reference, "CLOSED", 0, now.Unix())
	seriesID := "series-" + base.ws
	ruleID := "rule-" + base.ws
	addRecurringInvoiceSeries(t, db, seriesID, base.ws, base.user)
	addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
		ID: ruleID, SeriesID: seriesID, WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, Amount: 12990,
		Description: "Assinatura sintética", Start: occurrence, EffectiveStart: 1, Active: true,
	})
	execTestSQL(t, db, `
		INSERT INTO recurring_occurrence_blocks (
			workspace_id, series_id, recurring_rule_id, recurrence_sequence,
			occurrence_date_civil, invoice_id, invoice_reference, invoice_status,
			reason, created_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, 'CLOSED', 'frozen_invoice', ?)
	`, base.ws, seriesID, ruleID, occurrence.Format("2006-01-02"), invoiceID, reference, now.Unix())

	rows, err := projectedRecurringInvoiceRows(db, base.ws, base.card, reference)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("blocked occurrence projected: %+v", rows)
	}
	if total, count, err := CalculateVirtualInvoiceTotal(db, base.ws, base.card, reference); err != nil || total != 0 || count != 0 {
		t.Fatalf("virtual total = %d/%d err=%v, want 0/0", total, count, err)
	}
}

// P2 B: a read failure on terminal evidence must surface as an error, never as
// "no skip" with potentially inflated totals.
func TestInvoiceProjectionPropagatesTerminalReadError(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	db, base := newRecurringInvoiceTestBase(t, "ws-red-read-error")
	now := time.Now().UTC()
	reference := "2026-11"
	cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
	occurrence := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
	if occurrence.Unix() >= cycleEnd {
		t.Fatalf("occurrence outside cycle")
	}
	invoiceID := "invoice-" + base.ws
	addOpenInvoiceFutureClosing(t, db, invoiceID, base.card, reference, now)
	seriesID := "series-" + base.ws
	ruleID := "rule-" + base.ws
	addRecurringInvoiceSeries(t, db, seriesID, base.ws, base.user)
	addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
		ID: ruleID, SeriesID: seriesID, WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, Amount: 12990,
		Description: "Assinatura sintética", Start: occurrence, EffectiveStart: 1, Active: true,
	})

	// The projection would legitimately include this occurrence before the fault.
	rows, err := projectedRecurringInvoiceRows(db, base.ws, base.card, reference)
	if err != nil || len(rows) != 1 {
		t.Fatalf("baseline projection = %d rows err=%v, want 1/nil", len(rows), err)
	}

	execTestSQL(t, db, `ALTER TABLE recurring_occurrence_skips RENAME TO recurring_occurrence_skips_unavailable`)

	if _, err := projectedRecurringInvoiceRows(db, base.ws, base.card, reference); err == nil {
		t.Fatal("projection must propagate terminal read errors")
	}
	if _, _, err := CalculateVirtualInvoiceTotal(db, base.ws, base.card, reference); err == nil {
		t.Fatal("virtual total must propagate terminal read errors")
	}
	if _, err := buildFaturaDataForInvoice(db, base.ws, invoiceID, "desc"); err == nil {
		t.Fatal("invoice view must propagate terminal read errors")
	}
}

func TestInvoiceVersionedMaterializationIdempotent(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-red-idempotent", 5)
	if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID, 5)

	if rr := deleteInvoiceTransactionViaSwipe(t, scenario); rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rr.Code)
	}
	if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
		t.Fatalf("materialize after delete: %v", err)
	}
	if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
		t.Fatalf("second materialize after delete: %v", err)
	}
	assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID)
}

func TestInvoiceMaterializationSkipsClosedAndPaidInvoices(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	for _, status := range []string{"CLOSED", "PAID"} {
		t.Run(status, func(t *testing.T) {
			scenario := newVersionedInvoiceScenario(t, "ws-red-frozen-"+status, 1)
			execTestSQL(t, scenario.db, `DELETE FROM transactions WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.txID)
			execTestSQL(t, scenario.db, `UPDATE invoices SET status=? WHERE id=?`, status, scenario.invoiceID)
			if err := materializeRecurringInvoiceTransactions(scenario.db, scenario.base.ws, scenario.invoiceID); err != nil {
				t.Fatalf("materialize: %v", err)
			}
			assertSequences(t, scenario.db, scenario.base.ws, scenario.ruleID)
		})
	}
}

func TestInvoiceMaterializationKeepsWorkspaceIsolation(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	db, base := newRecurringInvoiceTestBase(t, "ws-red-tenant-a")
	foreignBase := seedF1Base(t, db, "ws-red-tenant-b")
	now := time.Now().UTC()
	reference := "2026-11"
	cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
	occurrence := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
	if occurrence.Unix() >= cycleEnd {
		t.Fatalf("occurrence outside cycle")
	}
	invoiceID := "invoice-" + base.ws
	addOpenInvoiceFutureClosing(t, db, invoiceID, base.card, reference, now)
	foreignSeries := "series-" + foreignBase.ws
	foreignRule := "rule-" + foreignBase.ws
	addRecurringInvoiceSeries(t, db, foreignSeries, foreignBase.ws, foreignBase.user)
	addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
		ID: foreignRule, SeriesID: foreignSeries, WorkspaceID: foreignBase.ws,
		UserID: foreignBase.user, AccountID: base.card, CategoryID: foreignBase.category,
		Amount: 99999, Description: "Regra de outro workspace", Start: occurrence,
		EffectiveStart: 1, Active: true,
	})
	if err := materializeRecurringInvoiceTransactions(db, base.ws, invoiceID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	var foreignRows int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = ? AND recurring_rule_id = ?`, foreignBase.ws, foreignRule).Scan(&foreignRows); err != nil {
		t.Fatalf("count foreign rows: %v", err)
	}
	if foreignRows != 0 {
		t.Fatalf("foreign workspace rule materialized %d rows", foreignRows)
	}
}

func TestInvoiceMaterializationRollsBackOnFailure(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	db, base := newRecurringInvoiceTestBase(t, "ws-red-rollback")
	now := time.Now().UTC()
	reference := "2026-11"
	cycleStart, cycleEnd := invoiceCycleBoundsForReference(2026, time.November, 20, 10)
	occurrence := time.Unix(cycleStart, 0).UTC().Add(48 * time.Hour)
	if occurrence.Unix() >= cycleEnd {
		t.Fatalf("occurrence outside cycle")
	}
	invoiceID := "invoice-" + base.ws
	addOpenInvoiceFutureClosing(t, db, invoiceID, base.card, reference, now)
	seriesID := "series-" + base.ws
	ruleID := "rule-" + base.ws
	addRecurringInvoiceSeries(t, db, seriesID, base.ws, base.user)
	addRecurringInvoiceRule(t, db, recurringInvoiceRuleTestSeed{
		ID: ruleID, SeriesID: seriesID, WorkspaceID: base.ws, UserID: base.user,
		AccountID: base.card, CategoryID: base.category, Amount: 12990,
		Description: "Assinatura diária", Start: occurrence, Frequency: "DAILY",
		EffectiveStart: 1, Active: true,
	})
	execTestSQL(t, db, `
		CREATE TRIGGER fail_second_recurring_insert
		BEFORE INSERT ON transactions
		WHEN NEW.recurring_rule_id = '`+ruleID+`' AND NEW.recurrence_sequence = 2
		BEGIN
			SELECT RAISE(ABORT, 'forced second occurrence failure');
		END
	`)

	if err := materializeRecurringInvoiceTransactions(db, base.ws, invoiceID); err == nil {
		t.Fatal("materialization must fail on the forced second insert")
	}
	assertSequences(t, db, base.ws, ruleID)
}
