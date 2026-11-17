package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func projectionScenarioNextReference(t *testing.T, scenario versionedInvoiceScenario) (string, time.Time) {
	t.Helper()
	next := nextRecurrenceDate(scenario.occurrence, "MONTHLY")
	_, _, reference := calculateInvoiceDates(next, 20, 10)
	if reference == scenario.reference {
		t.Fatalf("fixture must span two references")
	}
	return reference, next
}

func projectionRowsForReference(t *testing.T, scenario versionedInvoiceScenario, reference string) []TransactionRow {
	t.Helper()
	rows, err := projectedRecurringInvoiceRows(scenario.db, scenario.base.ws, scenario.base.card, reference)
	if err != nil {
		t.Fatalf("projection %s: %v", reference, err)
	}
	return rows
}

func deleteScenarioTransaction(t *testing.T, scenario versionedInvoiceScenario) {
	t.Helper()
	execTestSQL(t, scenario.db, `DELETE FROM transactions WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.txID)
}

func TestInvoiceProjectionRespectsEffectiveEnd(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-proj-effective-end", 5)
	deleteScenarioTransaction(t, scenario)
	nextReference, nextOccurrence := projectionScenarioNextReference(t, scenario)
	addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-next", scenario.base.card, nextReference, time.Now().UTC())

	execTestSQL(t, scenario.db, `UPDATE recurring_rules SET effective_end_sequence=5 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.ruleID)
	if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 0 {
		t.Fatalf("projection after effective end = %+v, want none", rows)
	}
	if total, count, err := CalculateVirtualInvoiceTotal(scenario.db, scenario.base.ws, scenario.base.card, nextReference); err != nil || total != 0 || count != 0 {
		t.Fatalf("virtual total after effective end = %d/%d err=%v, want 0/0", total, count, err)
	}

	execTestSQL(t, scenario.db, `UPDATE recurring_rules SET effective_end_sequence=6 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.ruleID)
	rows := projectionRowsForReference(t, scenario, nextReference)
	if len(rows) != 1 || rows[0].Date != nextOccurrence.Unix() {
		t.Fatalf("legitimate occurrence within end not projected: %+v", rows)
	}
}

func TestInvoiceProjectionRespectsEndDate(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	for _, field := range []string{"civil", "unix"} {
		t.Run(field, func(t *testing.T) {
			scenario := newVersionedInvoiceScenario(t, "ws-proj-end-date-"+field, 5)
			deleteScenarioTransaction(t, scenario)
			nextReference, nextOccurrence := projectionScenarioNextReference(t, scenario)
			addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-next", scenario.base.card, nextReference, time.Now().UTC())

			if field == "civil" {
				execTestSQL(t, scenario.db, `UPDATE recurring_rules SET end_date_civil=? WHERE workspace_id=? AND id=?`, scenario.occurrence.Format("2006-01-02"), scenario.base.ws, scenario.ruleID)
			} else {
				execTestSQL(t, scenario.db, `UPDATE recurring_rules SET end_date=? WHERE workspace_id=? AND id=?`, scenario.occurrence.Unix(), scenario.base.ws, scenario.ruleID)
			}
			if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 0 {
				t.Fatalf("projection after end date = %+v, want none", rows)
			}
			if total, count, err := CalculateVirtualInvoiceTotal(scenario.db, scenario.base.ws, scenario.base.card, nextReference); err != nil || total != 0 || count != 0 {
				t.Fatalf("virtual total after end date = %d/%d err=%v, want 0/0", total, count, err)
			}

			if field == "civil" {
				execTestSQL(t, scenario.db, `UPDATE recurring_rules SET end_date_civil=? WHERE workspace_id=? AND id=?`, nextOccurrence.Format("2006-01-02"), scenario.base.ws, scenario.ruleID)
			} else {
				execTestSQL(t, scenario.db, `UPDATE recurring_rules SET end_date=? WHERE workspace_id=? AND id=?`, nextOccurrence.Unix(), scenario.base.ws, scenario.ruleID)
			}
			if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 1 || rows[0].Date != nextOccurrence.Unix() {
				t.Fatalf("legitimate occurrence within end date not projected: %+v", rows)
			}
		})
	}
}

func TestInvoiceProjectionRespectsTotalOccurrencesForVersioned(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-proj-total", 5)
	deleteScenarioTransaction(t, scenario)
	nextReference, nextOccurrence := projectionScenarioNextReference(t, scenario)
	afterNext := nextRecurrenceDate(nextOccurrence, "MONTHLY")
	_, _, afterNextReference := calculateInvoiceDates(afterNext, 20, 10)
	addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-next", scenario.base.card, nextReference, time.Now().UTC())
	addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-after", scenario.base.card, afterNextReference, time.Now().UTC())

	execTestSQL(t, scenario.db, `UPDATE recurring_rules SET total_occurrences=5 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.ruleID)
	if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 0 {
		t.Fatalf("projection after rule total = %+v, want none", rows)
	}

	execTestSQL(t, scenario.db, `UPDATE recurring_rules SET total_occurrences=6 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.ruleID)
	if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 1 || rows[0].Date != nextOccurrence.Unix() {
		t.Fatalf("occurrence at rule total not projected: %+v", rows)
	}
	if rows := projectionRowsForReference(t, scenario, afterNextReference); len(rows) != 0 {
		t.Fatalf("projection after rule total = %+v, want none", rows)
	}
}

func TestInvoiceProjectionRespectsSeriesTotalOccurrences(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-proj-series-total", 5)
	deleteScenarioTransaction(t, scenario)
	nextReference, nextOccurrence := projectionScenarioNextReference(t, scenario)
	afterNext := nextRecurrenceDate(nextOccurrence, "MONTHLY")
	_, _, afterNextReference := calculateInvoiceDates(afterNext, 20, 10)
	addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-next", scenario.base.card, nextReference, time.Now().UTC())
	addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-after", scenario.base.card, afterNextReference, time.Now().UTC())

	execTestSQL(t, scenario.db, `UPDATE recurring_series SET total_occurrences=6 WHERE workspace_id=? AND id=?`, scenario.base.ws, scenario.seriesID)
	if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 1 || rows[0].Date != nextOccurrence.Unix() {
		t.Fatalf("occurrence at series total not projected: %+v", rows)
	}
	if rows := projectionRowsForReference(t, scenario, afterNextReference); len(rows) != 0 {
		t.Fatalf("projection after series total = %+v, want none", rows)
	}
}

func TestInvoiceProjectionRespectsNextVersionStart(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-proj-next-version", 5)
	deleteScenarioTransaction(t, scenario)
	nextReference, nextOccurrence := projectionScenarioNextReference(t, scenario)
	addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-next", scenario.base.card, nextReference, time.Now().UTC())
	addRecurringInvoiceRule(t, scenario.db, recurringInvoiceRuleTestSeed{
		ID: scenario.ruleID + "-next", SeriesID: scenario.seriesID, WorkspaceID: scenario.base.ws,
		UserID: scenario.base.user, AccountID: scenario.base.card, CategoryID: scenario.base.category,
		Amount: 15000, Description: "Versão seguinte", Start: nextOccurrence,
		EffectiveStart: 6, Active: true,
	})

	rows := projectionRowsForReference(t, scenario, nextReference)
	if len(rows) != 1 {
		t.Fatalf("projection across next version = %d rows, want exactly 1: %+v", len(rows), rows)
	}
	if rows[0].Date != nextOccurrence.Unix() || rows[0].Amount != 15000 {
		t.Fatalf("next version occurrence = %+v", rows[0])
	}
}

func TestInvoiceProjectionDoesNotDuplicatePreservedException(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	for _, shifted := range []bool{false, true} {
		name := "same_date"
		if shifted {
			name = "shifted_date"
		}
		t.Run(name, func(t *testing.T) {
			scenario := newVersionedInvoiceScenario(t, "ws-proj-exception-"+name, 5)
			deleteScenarioTransaction(t, scenario)
			nextReference, nextOccurrence := projectionScenarioNextReference(t, scenario)
			nextInvoiceID := scenario.invoiceID + "-next"
			addOpenInvoiceFutureClosing(t, scenario.db, nextInvoiceID, scenario.base.card, nextReference, time.Now().UTC())
			oldRuleID := scenario.ruleID + "-old"
			addRecurringInvoiceRule(t, scenario.db, recurringInvoiceRuleTestSeed{
				ID: oldRuleID, SeriesID: scenario.seriesID, WorkspaceID: scenario.base.ws,
				UserID: scenario.base.user, AccountID: scenario.base.card, CategoryID: scenario.base.category,
				Amount: 9990, Description: "Exceção preservada", Start: nextOccurrence,
				EffectiveStart: 1, EffectiveEnd: sql.NullInt64{Int64: 4, Valid: true}, Active: false,
			})
			exceptionDate := nextOccurrence
			if shifted {
				exceptionDate = nextOccurrence.AddDate(0, 0, 1)
			}
			addF1Tx(t, scenario.db, f1TxSeed{
				ID: "tx-exception-" + name, WorkspaceID: scenario.base.ws, UserID: scenario.base.user,
				AccountID: scenario.base.card, CategoryID: scenario.base.category, InvoiceID: nextInvoiceID,
				Type: "EXPENSE", Amount: 9990, Status: "paid", Description: "Exceção preservada",
				RecurringRuleID: oldRuleID, SeriesID: scenario.seriesID, RecurrenceSequence: 6,
				IsRecurringException: true, RecurringExceptionReason: "manual",
				CreatedAt: exceptionDate.Unix(),
			})

			data, err := buildFaturaDataForInvoice(scenario.db, scenario.base.ws, nextInvoiceID, "desc")
			if err != nil {
				t.Fatalf("invoice build: %v", err)
			}
			real, projected := 0, 0
			for _, row := range data.Transactions {
				if row.IsProjected {
					projected++
				} else {
					real++
				}
			}
			if real != 1 || projected != 0 {
				t.Fatalf("invoice rows real/projected = %d/%d, want 1/0: %+v", real, projected, data.Transactions)
			}
			if rows := projectionRowsForReference(t, scenario, nextReference); len(rows) != 0 {
				t.Fatalf("projection duplicated preserved exception: %+v", rows)
			}

			var amount int64
			var ruleID string
			if err := scenario.db.QueryRow(`SELECT amount, recurring_rule_id FROM transactions WHERE workspace_id=? AND id=?`, scenario.base.ws, "tx-exception-"+name).Scan(&amount, &ruleID); err != nil {
				t.Fatalf("exception preserved: %v", err)
			}
			if amount != 9990 || ruleID != oldRuleID {
				t.Fatalf("exception mutated: amount=%d rule=%s", amount, ruleID)
			}

			afterNext := nextRecurrenceDate(nextOccurrence, "MONTHLY")
			_, _, afterNextReference := calculateInvoiceDates(afterNext, 20, 10)
			addOpenInvoiceFutureClosing(t, scenario.db, scenario.invoiceID+"-after", scenario.base.card, afterNextReference, time.Now().UTC())
			if rows := projectionRowsForReference(t, scenario, afterNextReference); len(rows) != 1 || rows[0].Date != afterNext.Unix() {
				t.Fatalf("legitimate occurrence after exception lost: %+v", rows)
			}
		})
	}
}

func countVirtualInvoices(invoices []InvoiceRow) int {
	count := 0
	for _, invoice := range invoices {
		if strings.HasPrefix(invoice.ID, "virtual-") {
			count++
		}
	}
	return count
}

func TestBuildLancamentosFailsLoudlyOnVirtualInvoiceProjectionError(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	scenario := newVersionedInvoiceScenario(t, "ws-proj-lancamentos-error", 5)
	handler := TransactionHandler{DB: scenario.db, WorkspaceID: scenario.base.ws, UserID: scenario.base.user}

	baseline, err := handler.buildLancamentosData("", 1, 2027, LancamentosFilters{})
	if err != nil {
		t.Fatalf("baseline build: %v", err)
	}
	if countVirtualInvoices(baseline.Invoices) == 0 {
		t.Fatalf("fixture must expose a virtual invoice: %+v", baseline.Invoices)
	}

	execTestSQL(t, scenario.db, `ALTER TABLE recurring_occurrence_skips RENAME TO recurring_occurrence_skips_unavailable`)

	if _, err := handler.buildLancamentosData("", 1, 2027, LancamentosFilters{}); err == nil {
		t.Fatal("buildLancamentosData must propagate the financial projection error")
	}
}

func TestLancamentosHTTPReportsProjectionUnavailable(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "UTC")
	for _, htmx := range []bool{false, true} {
		name := "html"
		if htmx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			scenario := newVersionedInvoiceScenario(t, "ws-proj-http-"+name, 5)
			handler := TransactionHandler{DB: scenario.db, WorkspaceID: scenario.base.ws, UserID: scenario.base.user}
			execTestSQL(t, scenario.db, `ALTER TABLE recurring_occurrence_skips RENAME TO recurring_occurrence_skips_unavailable`)

			req := httptest.NewRequest(http.MethodGet, "/lancamentos?mes=1&ano=2027", nil)
			if htmx {
				req.Header.Set("HX-Request", "true")
			}
			rr := httptest.NewRecorder()
			handler.HandleListarTransacoes(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body=%q", rr.Code, rr.Body.String())
			}
			body := strings.ToLower(rr.Body.String())
			for _, forbidden := range []string{"no such table", "recurring_occurrence_skips", "sql logic error"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("response leaked SQL detail %q: %q", forbidden, rr.Body.String())
				}
			}
			if htmx {
				if got := rr.Header().Get("HX-Reswap"); got != "none" {
					t.Fatalf("HX-Reswap = %q, want none", got)
				}
				if trigger := rr.Header().Get("HX-Trigger"); !strings.Contains(trigger, "mostrarAlerta") {
					t.Fatalf("HX-Trigger = %q, want mostrarAlerta", trigger)
				}
			}
		})
	}
}
