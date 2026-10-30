package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func seedRecurringOccurrenceBlockFixture(t *testing.T, db *sql.DB, invoiceStatus string) recurringOccurrenceBlockExpectation {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at)
		VALUES ('series-block', 'ws-test', 'user-test', 1, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			description, start_date, start_date_civil, starts_at, starts_at_civil,
			frequency, default_payment_status, active, series_id,
			effective_start_sequence, created_at, updated_at
		) VALUES (
			'rule-block', 'ws-test', 'user-test', 'card-test', 'cat-expense-rec',
			'EXPENSE', 15000, 'Frozen block', ?, '2026-07-15', ?, '2026-07-15',
			'MONTHLY', 'PAID', 1, 'series-block', 1, ?, ?
		)
	`, testUnixDate("2026-07-15"), testUnixDate("2026-07-15"), now, now)
	execTestSQL(t, db, `
		INSERT INTO invoices (
			id, account_id, reference, closing_date, due_date, status, paid_at, created_at
		) VALUES (
			'invoice-block-r', 'card-test', '2026-07', ?, ?, ?,
			CASE WHEN ? = 'PAID' THEN ? ELSE NULL END, ?
		)
	`, testUnixDate("2026-07-20"), testUnixDate("2026-08-10"), invoiceStatus, invoiceStatus, now, now)
	return recurringOccurrenceBlockExpectation{
		WorkspaceID:        "ws-test",
		SeriesID:           "series-block",
		RecurringRuleID:    "rule-block",
		RecurrenceSequence: 1,
		OccurrenceDate:     MustCivilWriteDate("2026-07-15"),
		AccountID:          "card-test",
	}
}

func insertBlockFixture(t *testing.T, db *sql.DB, expected recurringOccurrenceBlockExpectation, ruleID, civilDate, invoiceID, reference string) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO recurring_occurrence_blocks (
			workspace_id, series_id, recurring_rule_id, recurrence_sequence,
			occurrence_date_civil, invoice_id, invoice_reference,
			invoice_status, reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'CLOSED', 'frozen_invoice', ?)
	`, expected.WorkspaceID, expected.SeriesID, ruleID, expected.RecurrenceSequence,
		civilDate, invoiceID, reference, time.Now().Unix())
}

func resolveBlockFixture(t *testing.T, db *sql.DB, expected recurringOccurrenceBlockExpectation) (bool, error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin block resolution: %v", err)
	}
	defer tx.Rollback()
	resolved, err := resolveRecurringOccurrenceBlockTx(tx, expected)
	if err == nil {
		if commitErr := tx.Commit(); commitErr != nil {
			t.Fatalf("commit block resolution: %v", commitErr)
		}
	}
	return resolved, err
}

func recurringBlockStateSnapshot(t *testing.T, db *sql.DB, workspaceID string) map[string][][]any {
	t.Helper()
	state := map[string][][]any{
		"workspaces": tenantScopeRows(t, db, `SELECT * FROM workspaces WHERE id = ?`, workspaceID),
		"invoices":   tenantScopeRows(t, db, `SELECT i.* FROM invoices i JOIN accounts a ON a.id = i.account_id WHERE a.workspace_id = ? ORDER BY i.id`, workspaceID),
	}
	for _, table := range []string{"workspace_members", "accounts", "transactions", "invoice_payments", "recurring_series", "recurring_rules", "recurring_occurrence_blocks", "recurring_occurrence_skips"} {
		state[table] = tenantScopeRows(t, db, `SELECT * FROM `+quoteSQLiteIdentifier(table)+` WHERE workspace_id = ? ORDER BY rowid`, workspaceID)
	}
	return state
}

func TestRecurringOccurrenceBlockResolutionValidatesHistoricalIdentity(t *testing.T) {
	t.Run("compatible is terminal without writes", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
		insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-r", "2026-07")
		before := recurringBlockStateSnapshot(t, db, expected.WorkspaceID)

		resolved, err := resolveBlockFixture(t, db, expected)
		if err != nil || !resolved {
			t.Fatalf("compatible block resolved=%v err=%v", resolved, err)
		}
		var blocks, transactions int
		if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks`).Scan(&blocks); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id='series-block' AND recurrence_sequence=1`).Scan(&transactions); err != nil {
			t.Fatal(err)
		}
		if blocks != 1 || transactions != 0 {
			t.Fatalf("resolution wrote state: blocks=%d transactions=%d", blocks, transactions)
		}
		if !reflect.DeepEqual(before, recurringBlockStateSnapshot(t, db, expected.WorkspaceID)) {
			t.Fatal("valid resolution changed persisted state")
		}
	})

	tests := []struct {
		name      string
		ruleID    string
		civilDate string
		invoiceID string
		reference string
	}{
		{"rule mismatch", "rule-other", "2026-07-15", "invoice-block-r", "2026-07"},
		{"civil date mismatch", "rule-block", "2026-07-16", "invoice-block-r", "2026-07"},
		{"invoice reference mismatch", "rule-block", "2026-07-15", "invoice-block-r", "2026-08"},
		{"invoice id mismatch", "rule-block", "2026-07-15", "invoice-2026-08", "2026-07"},
		{"invoice never existed", "rule-block", "2026-07-15", "invoice-never-existed", "2026-07"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
			insertBlockFixture(t, db, expected, tc.ruleID, tc.civilDate, tc.invoiceID, tc.reference)
			before := recurringBlockStateSnapshot(t, db, expected.WorkspaceID)

			resolved, err := resolveBlockFixture(t, db, expected)
			if resolved || !errors.Is(err, errInvoiceStateStructural) {
				t.Fatalf("divergent block resolved=%v err=%v, want structural", resolved, err)
			}
			if err := EnsureRecurringMaterialized(db, expected.WorkspaceID, expected.OccurrenceDate); !errors.Is(err, errInvoiceStateStructural) {
				t.Fatalf("materializer error = %v, want structural", err)
			}
			var transactions int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id='series-block' AND recurrence_sequence=1`).Scan(&transactions); err != nil {
				t.Fatal(err)
			}
			if transactions != 0 {
				t.Fatalf("divergent block suppressed/changed legitimate write state: transactions=%d", transactions)
			}
			if !reflect.DeepEqual(before, recurringBlockStateSnapshot(t, db, expected.WorkspaceID)) {
				t.Fatal("structural failure changed persisted state")
			}
		})
	}

	t.Run("live invoice workspace and account mismatch", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
		now := time.Now().Unix()
		execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES ('ws-block-other', 'Other', 'personal', ?, ?)`, now, now)
		execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, created_at, updated_at) VALUES ('card-block-other', 'ws-block-other', 'Other card', 'CREDIT_CARD', ?, ?)`, now, now)
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES ('invoice-block-other', 'card-block-other', '2026-07', ?, ?, 'CLOSED', ?)`, testUnixDate("2026-07-20"), testUnixDate("2026-08-10"), now)
		insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-other", "2026-07")
		beforeOwn := recurringBlockStateSnapshot(t, db, expected.WorkspaceID)
		beforeForeign := recurringBlockStateSnapshot(t, db, "ws-block-other")

		query := tenantScopeStatement(t, "recurring_occurrence_blocks.go", "resolveRecurringOccurrenceBlockTx", "SELECT i.reference, i.status", "block.InvoiceID", "expected.WorkspaceID")
		if got := tenantScopeRows(t, db, query, "invoice-block-other", expected.WorkspaceID); len(got) != 0 {
			t.Fatal("production lookup read a foreign invoice")
		}
		if got := tenantScopeRows(t, db, query, "invoice-block-other", "ws-block-other"); len(got) != 1 {
			t.Fatal("production lookup rejected the invoice's own workspace")
		}
		// Removing only the tenant predicate must expose this fixture, proving
		// that the production statement enforces the boundary.
		unscoped := strings.Replace(query, " AND a.workspace_id = ?", "", 1)
		if got := tenantScopeRows(t, db, unscoped, "invoice-block-other"); len(got) != 1 {
			t.Fatal("tenant predicate control did not expose the foreign fixture")
		}

		resolved, err := resolveBlockFixture(t, db, expected)
		if resolved || !errors.Is(err, errInvoiceStateStructural) {
			t.Fatalf("cross-workspace invoice resolved=%v err=%v, want structural", resolved, err)
		}
		if err.Error() != errInvoiceStateStructural.Error()+": recurring occurrence block invoice unavailable" {
			t.Fatalf("foreign invoice error exposed unexpected detail: %v", err)
		}
		if err := EnsureRecurringMaterialized(db, expected.WorkspaceID, expected.OccurrenceDate); !errors.Is(err, errInvoiceStateStructural) {
			t.Fatalf("foreign materialization error = %v, want structural", err)
		}
		if !reflect.DeepEqual(beforeOwn, recurringBlockStateSnapshot(t, db, expected.WorkspaceID)) ||
			!reflect.DeepEqual(beforeForeign, recurringBlockStateSnapshot(t, db, "ws-block-other")) {
			t.Fatal("foreign rejection changed either workspace")
		}
	})

	t.Run("own invoice on unexpected card", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
		execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type) VALUES ('card-block-own-other', 'ws-test', 'Other own card', 'CREDIT_CARD')`)
		execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status) VALUES ('invoice-block-own-other', 'card-block-own-other', '2026-07', ?, ?, 'CLOSED')`, testUnixDate("2026-07-20"), testUnixDate("2026-08-10"))
		insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-own-other", "2026-07")
		query := tenantScopeStatement(t, "recurring_occurrence_blocks.go", "resolveRecurringOccurrenceBlockTx", "SELECT i.reference, i.status", "block.InvoiceID", "expected.WorkspaceID")
		want := [][]any{{"2026-07", "CLOSED", "card-block-own-other", expected.WorkspaceID, "CREDIT_CARD"}}
		if got := tenantScopeRows(t, db, query, "invoice-block-own-other", expected.WorkspaceID); !reflect.DeepEqual(got, want) {
			t.Fatalf("card mismatch fixture lookup = %v, want %v", got, want)
		}
		before := recurringBlockStateSnapshot(t, db, expected.WorkspaceID)
		resolved, err := resolveBlockFixture(t, db, expected)
		if resolved || !errors.Is(err, errInvoiceStateStructural) {
			t.Fatalf("unexpected own card resolved=%v err=%v, want structural", resolved, err)
		}
		if err.Error() != errInvoiceStateStructural.Error()+": recurring occurrence block live invoice mismatch" {
			t.Fatalf("card mismatch rejected before live invoice validation: %v", err)
		}
		if !reflect.DeepEqual(before, recurringBlockStateSnapshot(t, db, expected.WorkspaceID)) {
			t.Fatal("card mismatch changed persisted state")
		}
	})

	t.Run("rule civil drift is structural", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
		insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-r", "2026-07")
		execTestSQL(t, db, `UPDATE recurring_rules SET starts_at_civil='2026-07-16', start_date_civil='2026-07-16' WHERE id=?`, expected.RecurringRuleID)

		resolved, err := resolveBlockFixture(t, db, expected)
		if resolved || !errors.Is(err, errInvoiceStateStructural) {
			t.Fatalf("rule drift resolved=%v err=%v, want structural", resolved, err)
		}
	})
}

func TestRecurringOccurrenceBlockResolutionAcceptsFrozenStatusTransition(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
	insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-r", "2026-07")

	for _, liveStatus := range []string{"PAID", "CLOSED"} {
		execTestSQL(t, db, `UPDATE invoices SET status=? WHERE id='invoice-block-r'`, liveStatus)
		resolved, err := resolveBlockFixture(t, db, expected)
		if err != nil || !resolved {
			t.Fatalf("live status %s resolved=%v err=%v", liveStatus, resolved, err)
		}
	}
	execTestSQL(t, db, `UPDATE invoices SET status='OPEN' WHERE id='invoice-block-r'`)
	resolved, err := resolveBlockFixture(t, db, expected)
	if resolved || !errors.Is(err, errInvoiceStateStructural) {
		t.Fatalf("OPEN live invoice resolved=%v err=%v, want structural", resolved, err)
	}
}

func TestRecurringOccurrenceBlockDatabaseInvariants(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	expected := seedRecurringOccurrenceBlockFixture(t, db, "CLOSED")
	insertBlockFixture(t, db, expected, expected.RecurringRuleID, expected.OccurrenceDate.Civil(), "invoice-block-r", "2026-07")

	if _, err := db.Exec(`
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date, date_civil,
			description, recurring_rule_id, recurrence_sequence, series_id
		) VALUES (
			'tx-block-conflict', 'ws-test', 'user-test', 'card-test', 'EXPENSE', 15000,
			?, '2026-07-15', 'conflict', 'rule-block', 1, 'series-block'
		)
	`, testUnixDate("2026-07-15")); err == nil {
		t.Fatal("transaction inserted over frozen block")
	}
	if _, err := db.Exec(`
		INSERT INTO recurring_occurrence_skips (
			workspace_id, series_id, recurrence_sequence, reason, deleted_at
		) VALUES ('ws-test', 'series-block', 1, 'single_delete', ?)
	`, time.Now().Unix()); err == nil {
		t.Fatal("manual skip inserted over frozen block")
	}
	if _, err := db.Exec(`UPDATE recurring_occurrence_blocks SET invoice_reference='2026-08'`); err == nil {
		t.Fatal("frozen block update was accepted")
	}
	if _, err := db.Exec(`DELETE FROM recurring_occurrence_blocks`); err == nil {
		t.Fatal("frozen block delete was accepted")
	}
}

func seedFrozenMaterializationSeries(t *testing.T, db *sql.DB, invoiceStatus string) (ruleID, seriesID string) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	now := time.Now().Unix()
	ruleID = "rule-frozen-materialization"
	seriesID = "series-frozen-materialization"
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			description, start_date, start_date_civil, starts_at, starts_at_civil,
			frequency, default_payment_status, active, series_id,
			effective_start_sequence, created_at, updated_at
		) VALUES (
			?, 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'EXPENSE', 15000,
			'Frozen materialization', ?, '2026-06-15', ?, '2026-06-15',
			'MONTHLY', 'PAID', 1, ?, 1, ?, ?
		)
	`, ruleID, testUnixDate("2026-06-15"), testUnixDate("2026-06-15"), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, invoice_id,
			type, amount, date, date_civil, description, status,
			recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at
		) VALUES (
			'tx-frozen-seq-1', 'ws-test', 'user-test', 'card-test', 'cat-expense-rec',
			'invoice-2026-08', 'EXPENSE', 15000, ?, '2026-06-15',
			'Frozen materialization', 'paid', ?, 1, ?, ?, ?
		)
	`, testUnixDate("2026-06-15"), ruleID, seriesID, now, now)
	execTestSQL(t, db, `
		UPDATE invoices
		SET status=?, paid_at=CASE WHEN ?='PAID' THEN ? ELSE NULL END
		WHERE id='invoice-2026-08'
	`, invoiceStatus, invoiceStatus, now)
	return ruleID, seriesID
}

func TestEnsureRecurringMaterializedPersistsFrozenBlockAndContinues(t *testing.T) {
	for _, status := range []string{"CLOSED", "PAID"} {
		t.Run(status, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			ruleID, seriesID := seedFrozenMaterializationSeries(t, db, status)

			if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-08-15")); err != nil {
				t.Fatalf("materialize frozen series: %v", err)
			}

			var blockRule, blockDate, invoiceID, reference, snapshot, reason string
			if err := db.QueryRow(`
				SELECT recurring_rule_id, occurrence_date_civil, invoice_id,
				       invoice_reference, invoice_status, reason
				FROM recurring_occurrence_blocks
				WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=2
			`, seriesID).Scan(&blockRule, &blockDate, &invoiceID, &reference, &snapshot, &reason); err != nil {
				t.Fatalf("read frozen block: %v", err)
			}
			if blockRule != ruleID || blockDate != "2026-07-15" || invoiceID != "invoice-2026-08" || reference != "2026-08" || snapshot != status || reason != "frozen_invoice" {
				t.Fatalf("frozen block mismatch: rule=%q date=%q invoice=%q ref=%q status=%q reason=%q", blockRule, blockDate, invoiceID, reference, snapshot, reason)
			}
			var seq2Transactions, seq3Transactions int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&seq2Transactions); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=3`, seriesID).Scan(&seq3Transactions); err != nil {
				t.Fatal(err)
			}
			if seq2Transactions != 0 || seq3Transactions != 1 {
				t.Fatalf("continuation mismatch: seq2=%d seq3=%d", seq2Transactions, seq3Transactions)
			}
			var seq1InvoiceID, seq1Status string
			if err := db.QueryRow(`SELECT invoice_id, status FROM transactions WHERE id='tx-frozen-seq-1'`).Scan(&seq1InvoiceID, &seq1Status); err != nil {
				t.Fatal(err)
			}
			if seq1InvoiceID != "invoice-2026-08" || seq1Status != "paid" {
				t.Fatalf("existing frozen transaction changed: invoice=%q status=%q", seq1InvoiceID, seq1Status)
			}
		})
	}
}

func TestRecurringMaterializationJobTreatsFrozenBlockAsSuccess(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	_, seriesID := seedFrozenMaterializationSeries(t, db, "CLOSED")
	now := time.Now().Unix()

	if err := runRecurringMaterializationForWorkspace(context.Background(), db, "ws-test", now, defaultRecurringMaterializationRunner); err != nil {
		t.Fatalf("run recurring materialization job: %v", err)
	}
	var blocks int
	if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks); err != nil {
		t.Fatal(err)
	}
	state := readMaterializationState(t, db, "ws-test")
	if blocks != 1 || state == nil || state.Status != "idle" || state.LastError.Valid || state.LastSuccessAt != now {
		t.Fatalf("job success mismatch: blocks=%d state=%+v", blocks, state)
	}
}

func TestRecurringInvoiceReadDoesNotOwnFrozenDecision(t *testing.T) {
	for _, status := range []string{"CLOSED", "PAID"} {
		t.Run(status, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			_, seriesID := seedFrozenMaterializationSeries(t, db, status)

			if err := materializeRecurringInvoiceTransactions(db, "ws-test", "invoice-2026-08"); err != nil {
				t.Fatalf("read materializer on %s invoice: %v", status, err)
			}
			var transactions, blocks int
			if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactions); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=?`, seriesID).Scan(&blocks); err != nil {
				t.Fatal(err)
			}
			if transactions != 0 || blocks != 0 {
				t.Fatalf("read path wrote frozen outcome: transactions=%d blocks=%d", transactions, blocks)
			}
		})
	}

	t.Run("open remains materializable", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		_, seriesID := seedFrozenMaterializationSeries(t, db, "OPEN")
		if err := materializeRecurringInvoiceTransactions(db, "ws-test", "invoice-2026-08"); err != nil {
			t.Fatalf("read materializer on OPEN invoice: %v", err)
		}
		var transactions, blocks int
		_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2 AND invoice_id='invoice-2026-08'`, seriesID).Scan(&transactions)
		_ = db.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=?`, seriesID).Scan(&blocks)
		if transactions != 1 || blocks != 0 {
			t.Fatalf("OPEN read materialization mismatch: transactions=%d blocks=%d", transactions, blocks)
		}
	})
}

func TestRecurringFrozenOccurrencesInSameCycleKeepSameReference(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	_, seriesID := seedFrozenMaterializationSeries(t, db, "CLOSED")
	execTestSQL(t, db, `DELETE FROM transactions WHERE id='tx-frozen-seq-1'`)
	execTestSQL(t, db, `
		UPDATE recurring_rules
		SET frequency='WEEKLY', start_date=?, start_date_civil='2026-07-01',
		    starts_at=?, starts_at_civil='2026-07-01'
		WHERE id='rule-frozen-materialization'
	`, testUnixDate("2026-07-01"), testUnixDate("2026-07-01"))

	if err := EnsureRecurringMaterialized(db, "ws-test", MustCivilWriteDate("2026-07-08")); err != nil {
		t.Fatalf("materialize same-cycle frozen occurrences: %v", err)
	}
	var blocks, transactions, distinctReferences int
	var minReference, maxReference string
	if err := db.QueryRow(`
		SELECT COUNT(1), COUNT(DISTINCT invoice_reference), MIN(invoice_reference), MAX(invoice_reference)
		FROM recurring_occurrence_blocks
		WHERE series_id=? AND recurrence_sequence IN (1, 2)
	`, seriesID).Scan(&blocks, &distinctReferences, &minReference, &maxReference); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence IN (1, 2)`, seriesID).Scan(&transactions)
	if blocks != 2 || transactions != 0 || distinctReferences != 1 || minReference != "2026-08" || maxReference != "2026-08" {
		t.Fatalf("same-cycle result: blocks=%d transactions=%d refs=%d %q..%q", blocks, transactions, distinctReferences, minReference, maxReference)
	}
}

func TestRecurringOccurrenceFrozenBlockSurvivesCardConfigurationDrift(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	_, seriesID := seedFrozenMaterializationSeries(t, db, "CLOSED")
	horizon := MustCivilWriteDate("2026-07-15")

	if err := EnsureRecurringMaterialized(db, "ws-test", horizon); err != nil {
		t.Fatalf("first materialization: %v", err)
	}
	var beforeID, beforeReference, beforeDate string
	if err := db.QueryRow(`
		SELECT invoice_id, invoice_reference, occurrence_date_civil
		FROM recurring_occurrence_blocks
		WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=2
	`, seriesID).Scan(&beforeID, &beforeReference, &beforeDate); err != nil {
		t.Fatalf("read initial block: %v", err)
	}

	// July 15 belongs to reference 2026-08 with closing_day=20/due_day=10. After
	// this change, current routing would choose 2026-09; the committed tombstone must
	// not be reinterpreted.
	execTestSQL(t, db, `UPDATE credit_cards SET closing_day=10, due_day=5 WHERE account_id='card-test'`)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin current routing proof: %v", err)
	}
	_, currentReference, _, _, _, routeErr := candidateInvoiceForDateTx(tx, "ws-test", "card-test", testUnixDate("2026-07-15"))
	_ = tx.Rollback()
	if routeErr != nil || currentReference == beforeReference {
		t.Fatalf("fixture did not produce routing drift: current=%q historical=%q err=%v", currentReference, beforeReference, routeErr)
	}
	if err := EnsureRecurringMaterialized(db, "ws-test", horizon); err != nil {
		t.Fatalf("rerun after card configuration drift: %v", err)
	}

	var blockCount, transactionCount int
	var afterID, afterReference, afterDate string
	if err := db.QueryRow(`
		SELECT COUNT(1), MIN(invoice_id), MIN(invoice_reference), MIN(occurrence_date_civil)
		FROM recurring_occurrence_blocks
		WHERE workspace_id='ws-test' AND series_id=? AND recurrence_sequence=2
	`, seriesID).Scan(&blockCount, &afterID, &afterReference, &afterDate); err != nil {
		t.Fatalf("read block after drift: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactionCount); err != nil {
		t.Fatal(err)
	}
	if blockCount != 1 || transactionCount != 0 || afterID != beforeID || afterReference != beforeReference || afterDate != beforeDate {
		t.Fatalf("config drift changed terminal evidence: blocks=%d tx=%d before=(%q,%q,%q) after=(%q,%q,%q)", blockCount, transactionCount, beforeID, beforeReference, beforeDate, afterID, afterReference, afterDate)
	}
}

type recurringPaymentMaterializerState struct {
	Reference      string
	Status         string
	Total          int64
	PaidAmount     int64
	EffectivePaid  int64
	PaidAt         sql.NullInt64
	PaymentCount   int
	PaymentSum     int64
	AccountBalance int64
}

func prepareRealPaymentMaterializerFixture(t *testing.T, db *sql.DB) string {
	t.Helper()
	_, seriesID := seedFrozenMaterializationSeries(t, db, "OPEN")
	execTestSQL(t, db, `UPDATE recurring_rules SET total_occurrences=2 WHERE id='rule-frozen-materialization'`)
	execTestSQL(t, db, `UPDATE recurring_series SET total_occurrences=2 WHERE id=?`, seriesID)
	return seriesID
}

func invokeRealInvoiceSettlement(handler FaturasHandler) int {
	request := formPost("/cartoes/faturas/pagar", url.Values{
		"invoice_id":         {"invoice-2026-08"},
		"payment_account_id": {"checking-test"},
		"payment_mode":       {"settle"},
		"confirm_settle":     {"1"},
		"payment_date":       {"2026-08-11"},
	})
	recorder := httptest.NewRecorder()
	handler.HandlePagarFatura(recorder, request)
	return recorder.Code
}

func readRecurringPaymentMaterializerState(t *testing.T, db *sql.DB) recurringPaymentMaterializerState {
	t.Helper()
	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
	if err != nil {
		t.Fatalf("load invoice financial snapshot: %v", err)
	}
	state := recurringPaymentMaterializerState{
		Status:        snapshot.Status,
		Total:         snapshot.Total,
		PaidAmount:    snapshot.LegacyPaidAmount,
		EffectivePaid: snapshot.EffectivePaid,
		PaidAt:        snapshot.PaidAt,
	}
	if err := db.QueryRow(`SELECT reference FROM invoices WHERE id='invoice-2026-08'`).Scan(&state.Reference); err != nil {
		t.Fatalf("read invoice reference: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(SUM(amount_cents), 0)
		FROM invoice_payments
		WHERE workspace_id='ws-test' AND invoice_id='invoice-2026-08' AND reversed_at IS NULL
	`).Scan(&state.PaymentCount, &state.PaymentSum); err != nil {
		t.Fatalf("read invoice payments: %v", err)
	}
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id='checking-test' AND workspace_id='ws-test'`).Scan(&state.AccountBalance); err != nil {
		t.Fatalf("read payment account balance: %v", err)
	}
	return state
}

func TestRecurringPaymentMaterializerRealWriterOrdering(t *testing.T) {
	t.Run("materializer first payment includes committed occurrence", func(t *testing.T) {
		dbs, err := database.OpenRuntimeDBs("file:" + filepath.Join(t.TempDir(), "materializer-first.db"))
		if err != nil {
			t.Fatalf("open runtime pools: %v", err)
		}
		defer dbs.Close()
		seriesID := prepareRealPaymentMaterializerFixture(t, dbs.DB)
		before := readRecurringPaymentMaterializerState(t, dbs.DB)

		if err := EnsureRecurringMaterialized(dbs.Immediate, "ws-test", MustCivilWriteDate("2026-07-15")); err != nil {
			t.Fatalf("materialize before payment: %v", err)
		}
		afterMaterializer := readRecurringPaymentMaterializerState(t, dbs.DB)
		if afterMaterializer.Total != before.Total+15000 {
			t.Fatalf("materialized total = %d, want %d", afterMaterializer.Total, before.Total+15000)
		}

		handler := FaturasHandler{
			DB: dbs.DB, WriterDB: dbs.Immediate, Templates: testInvoicePaymentTemplates(t),
			WorkspaceID: "ws-test", UserID: "user-test",
		}
		if status := invokeRealInvoiceSettlement(handler); status != http.StatusOK {
			t.Fatalf("real payment status = %d, want 200", status)
		}

		final := readRecurringPaymentMaterializerState(t, dbs.DB)
		var transactions, blocks int
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2 AND invoice_id='invoice-2026-08'`, seriesID).Scan(&transactions)
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks)
		if transactions != 1 || blocks != 0 {
			t.Fatalf("terminal state transactions=%d blocks=%d", transactions, blocks)
		}
		if final.Status != "PAID" || final.Total != afterMaterializer.Total || final.PaymentCount != 1 || final.PaymentSum != final.Total || final.PaidAmount != final.Total || final.EffectivePaid != final.Total || !final.PaidAt.Valid {
			t.Fatalf("materializer-first financial state = %+v", final)
		}
	})

	t.Run("payment first preserves settlement and creates block", func(t *testing.T) {
		dbs, err := database.OpenRuntimeDBs("file:" + filepath.Join(t.TempDir(), "payment-first.db"))
		if err != nil {
			t.Fatalf("open runtime pools: %v", err)
		}
		defer dbs.Close()
		seriesID := prepareRealPaymentMaterializerFixture(t, dbs.DB)
		handler := FaturasHandler{
			DB: dbs.DB, WriterDB: dbs.Immediate, Templates: testInvoicePaymentTemplates(t),
			WorkspaceID: "ws-test", UserID: "user-test",
		}
		if status := invokeRealInvoiceSettlement(handler); status != http.StatusOK {
			t.Fatalf("real payment status = %d, want 200", status)
		}
		afterPayment := readRecurringPaymentMaterializerState(t, dbs.DB)
		if afterPayment.Status != "PAID" || afterPayment.PaymentCount != 1 || afterPayment.PaymentSum != afterPayment.Total || afterPayment.PaidAmount != afterPayment.Total || afterPayment.EffectivePaid != afterPayment.Total || !afterPayment.PaidAt.Valid {
			t.Fatalf("payment-first settlement state = %+v", afterPayment)
		}

		if err := EnsureRecurringMaterialized(dbs.Immediate, "ws-test", MustCivilWriteDate("2026-07-15")); err != nil {
			t.Fatalf("materialize after payment: %v", err)
		}
		final := readRecurringPaymentMaterializerState(t, dbs.DB)
		var transactions, blocks int
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactions)
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks)
		if transactions != 0 || blocks != 1 {
			t.Fatalf("terminal state transactions=%d blocks=%d", transactions, blocks)
		}
		if final != afterPayment {
			t.Fatalf("materializer changed settled invoice: before=%+v after=%+v", afterPayment, final)
		}
	})
}

func TestRecurringPaymentMaterializerRealWriterRace(t *testing.T) {
	dbs, err := database.OpenRuntimeDBs("file:" + filepath.Join(t.TempDir(), "payment-materializer-race.db"))
	if err != nil {
		t.Fatalf("open runtime pools: %v", err)
	}
	defer dbs.Close()
	seriesID := prepareRealPaymentMaterializerFixture(t, dbs.DB)
	handler := FaturasHandler{
		DB: dbs.DB, WriterDB: dbs.Immediate, Templates: testInvoicePaymentTemplates(t),
		WorkspaceID: "ws-test", UserID: "user-test",
	}

	start := make(chan struct{})
	paymentDone := make(chan int, 1)
	materializerDone := make(chan error, 1)
	go func() {
		<-start
		paymentDone <- invokeRealInvoiceSettlement(handler)
	}()
	go func() {
		<-start
		materializerDone <- runRecurringMaterializationForWorkspace(
			context.Background(), dbs.Immediate, "ws-test", time.Now().Unix(), defaultRecurringMaterializationRunner,
		)
	}()
	close(start)
	if status := <-paymentDone; status != http.StatusOK {
		t.Fatalf("concurrent real payment status = %d, want 200", status)
	}
	if err := <-materializerDone; err != nil {
		t.Fatalf("concurrent materializer: %v", err)
	}

	final := readRecurringPaymentMaterializerState(t, dbs.DB)
	var transactions, blocks int
	_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactions)
	_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks)
	state := readMaterializationState(t, dbs.DB, "ws-test")
	if transactions+blocks != 1 || (transactions != 0 && blocks != 0) {
		t.Fatalf("impossible terminal combination transactions=%d blocks=%d", transactions, blocks)
	}
	if final.Status != "PAID" || final.PaymentCount != 1 || final.PaymentSum != final.Total || final.PaidAmount != final.Total || final.EffectivePaid != final.Total || !final.PaidAt.Valid {
		t.Fatalf("concurrent financial state = %+v", final)
	}
	if transactions == 1 && final.Total != 55000 {
		t.Fatalf("materializer-first payment used stale total: %+v", final)
	}
	if blocks == 1 && final.Total != 40000 {
		t.Fatalf("payment-first block changed settled total: %+v", final)
	}
	if state == nil || state.Status != "idle" || state.LastError.Valid || state.LastSuccessAt <= 0 {
		t.Fatalf("materialization marker state = %+v", state)
	}
}

func TestRecurringFrozenWriterRacesRemainTerminalAndUnique(t *testing.T) {
	for _, status := range []string{"CLOSED", "PAID"} {
		t.Run(status+" wins before materializer", func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "frozen-writer-race.db")
			dbs, err := database.OpenRuntimeDBs("file:" + dbPath)
			if err != nil {
				t.Fatalf("open runtime pools: %v", err)
			}
			defer dbs.Close()
			_, seriesID := seedFrozenMaterializationSeries(t, dbs.DB, "OPEN")

			writer, err := dbs.Immediate.Begin()
			if err != nil {
				t.Fatalf("begin frozen writer: %v", err)
			}
			if _, err := writer.Exec(`UPDATE invoices SET status=? WHERE id='invoice-2026-08'`, status); err != nil {
				_ = writer.Rollback()
				t.Fatalf("set frozen status: %v", err)
			}
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				done <- EnsureRecurringMaterialized(dbs.Immediate, "ws-test", MustCivilWriteDate("2026-07-15"))
			}()
			<-started
			if err := writer.Commit(); err != nil {
				t.Fatalf("commit frozen writer: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("materializer after %s: %v", status, err)
			}

			var blocks, transactions int
			_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks)
			_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactions)
			if blocks != 1 || transactions != 0 {
				t.Fatalf("race result blocks=%d transactions=%d", blocks, transactions)
			}
		})
	}

	t.Run("auto-close versus materializer", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "auto-close-materializer-race.db")
		dbs, err := database.OpenRuntimeDBs("file:" + dbPath)
		if err != nil {
			t.Fatalf("open runtime pools: %v", err)
		}
		defer dbs.Close()
		_, seriesID := seedFrozenMaterializationSeries(t, dbs.DB, "OPEN")
		execTestSQL(t, dbs.DB, `UPDATE invoices SET closing_date=? WHERE id='invoice-2026-08'`, time.Now().Add(-time.Hour).Unix())

		start := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-start
			done <- autoCloseInvoices(dbs.DB, "ws-test", "card-test")
		}()
		go func() {
			<-start
			done <- EnsureRecurringMaterialized(dbs.Immediate, "ws-test", MustCivilWriteDate("2026-07-15"))
		}()
		close(start)
		// SQLite may reject the deferred writer that loses the snapshot upgrade.
		// Retrying both operations proves that either serialization order converges
		// to exactly one terminal outcome.
		for i := 0; i < 2; i++ {
			if raceErr := <-done; raceErr != nil {
				message := strings.ToLower(raceErr.Error())
				if !strings.Contains(message, "locked") && !strings.Contains(message, "busy") {
					t.Fatalf("unexpected concurrent writer error: %v", raceErr)
				}
			}
		}
		if err := autoCloseInvoices(dbs.DB, "ws-test", "card-test"); err != nil {
			t.Fatalf("auto-close retry: %v", err)
		}
		if err := EnsureRecurringMaterialized(dbs.Immediate, "ws-test", MustCivilWriteDate("2026-07-15")); err != nil {
			t.Fatalf("materializer retry: %v", err)
		}

		var invoiceStatus string
		var blocks, transactions int
		_ = dbs.DB.QueryRow(`SELECT status FROM invoices WHERE id='invoice-2026-08'`).Scan(&invoiceStatus)
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks)
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactions)
		if !invoiceStatusIsFrozen(invoiceStatus) || blocks+transactions != 1 || (blocks != 0 && transactions != 0) {
			t.Fatalf("auto-close race status=%q blocks=%d transactions=%d", invoiceStatus, blocks, transactions)
		}
	})

	t.Run("materializer versus materializer", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "duplicate-materializer-race.db")
		dbs, err := database.OpenRuntimeDBs("file:" + dbPath)
		if err != nil {
			t.Fatalf("open runtime pools: %v", err)
		}
		defer dbs.Close()
		_, seriesID := seedFrozenMaterializationSeries(t, dbs.DB, "CLOSED")

		start := make(chan struct{})
		errs := make(chan error, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for i := 0; i < 2; i++ {
			go func() {
				ready.Done()
				<-start
				errs <- EnsureRecurringMaterialized(dbs.Immediate, "ws-test", MustCivilWriteDate("2026-07-15"))
			}()
		}
		ready.Wait()
		close(start)
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent materializer: %v", err)
			}
		}
		var blocks, transactions int
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM recurring_occurrence_blocks WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&blocks)
		_ = dbs.DB.QueryRow(`SELECT COUNT(1) FROM transactions WHERE series_id=? AND recurrence_sequence=2`, seriesID).Scan(&transactions)
		if blocks != 1 || transactions != 0 {
			t.Fatalf("duplicate materializer result blocks=%d transactions=%d", blocks, transactions)
		}
	})
}
