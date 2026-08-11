package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func TestManualInvoiceMoveClosedEmptyRegression(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedManualInvoiceMoveScenarioForTest(t, db)

	before := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
	rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
	after := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if after.invoiceID != "invoice-2026-08" || after.overrideID != "invoice-2026-08" {
		t.Fatalf("invoice/override = %q/%q, want August", after.invoiceID, after.overrideID)
	}
	if after.destinationTotal != before.destinationTotal+25000 || after.sourceTotal != before.sourceTotal-25000 {
		t.Fatalf("totals before=%+v after=%+v", before, after)
	}
	if !manualInvoiceProtectedFieldsEqualForTest(before, after) {
		t.Fatalf("non-invoice fields changed: before=%+v after=%+v", before, after)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status); err != nil {
		t.Fatalf("destination status: %v", err)
	}
	if status != "CLOSED" {
		t.Fatalf("destination status = %q, want CLOSED", status)
	}
}

func TestManualInvoiceMoveClosedSourceDestinations(t *testing.T) {
	for _, destinationStatus := range []string{"OPEN", "CLOSED"} {
		t.Run("closed to "+strings.ToLower(destinationStatus), func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedManualInvoiceMoveScenarioForTest(t, db)
			execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED', paid_at = NULL, paid_amount = 0 WHERE id = 'invoice-2026-09'`)
			execTestSQL(t, db, `UPDATE invoices SET status = ?, paid_at = NULL, paid_amount = 0 WHERE id = 'invoice-2026-08'`, destinationStatus)

			rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
			}
			var invoiceID, overrideID string
			if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID, &overrideID); err != nil {
				t.Fatal(err)
			}
			if invoiceID != "invoice-2026-08" || overrideID != "invoice-2026-08" {
				t.Fatalf("invoice/override = %q/%q, want destination", invoiceID, overrideID)
			}
		})
	}
}

func TestManualInvoiceMoveC0AProtectsPaymentAndReversalAcrossSourceStates(t *testing.T) {
	for _, sourceStatus := range []string{"OPEN", "CLOSED"} {
		for _, relation := range []string{"payment", "reversal"} {
			t.Run(strings.ToLower(sourceStatus)+"/"+relation, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				seedManualInvoiceMoveScenarioForTest(t, db)
				execTestSQL(t, db, `UPDATE invoices SET status = ?, paid_at = NULL, paid_amount = 0 WHERE id = 'invoice-2026-09'`, sourceStatus)
				now := time.Now().Unix()
				if relation == "payment" {
					execTestSQL(t, db, `
						INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
						VALUES ('ip-manual-payment', 'ws-test', 'invoice-2026-09', 'checking-test', 'purchase-test', 25000, ?, 'manual', 'user-test', ?)
					`, now, now)
				} else {
					execTestSQL(t, db, `
						INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, installment_number, total_installments, created_at, updated_at)
						VALUES ('manual-payment-anchor', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 25000, ?, 'Pagamento ancora', 'paid', ?, 1, 1, ?, ?)
					`, now, now, now, now)
					execTestSQL(t, db, `
						INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, reversed_at, reversal_transaction_id, created_by, created_at)
						VALUES ('ip-manual-reversal', 'ws-test', 'invoice-2026-09', 'checking-test', 'manual-payment-anchor', 25000, ?, 'manual', ?, 'purchase-test', 'user-test', ?)
					`, now, now, now)
				}

				before := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
				rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
				after := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
				if rr.Code != http.StatusConflict {
					t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
				}
				if after != before {
					t.Fatalf("C0-A protected move changed state: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func TestManualInvoiceMoveFinancialEligibility(t *testing.T) {
	t.Run("closed partially paid preserves payment and increases pending", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		insertInvoiceExpenseForManualMoveTest(t, db, "destination-existing", "invoice-2026-08", 20000)
		insertActiveInvoicePaymentForManualMoveTest(t, db, "destination-partial", "invoice-2026-08", 10000)

		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var total, paid int64
		if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM transactions WHERE workspace_id = 'ws-test' AND invoice_id = 'invoice-2026-08' AND type = 'EXPENSE'`).Scan(&total); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COALESCE(SUM(amount_cents), 0) FROM invoice_payments WHERE workspace_id = 'ws-test' AND invoice_id = 'invoice-2026-08' AND reversed_at IS NULL`).Scan(&paid); err != nil {
			t.Fatal(err)
		}
		if total != 45000 || paid != 10000 || total-paid != 35000 {
			t.Fatalf("total/paid/pending = %d/%d/%d, want 45000/10000/35000", total, paid, total-paid)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-08'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "CLOSED" {
			t.Fatalf("status = %q, want CLOSED", status)
		}
	})

	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{
			name: "paid status",
			setup: func(t *testing.T, db *sql.DB) {
				execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ? WHERE id = 'invoice-2026-08'`, time.Now().Unix())
			},
		},
		{
			name: "closed financially settled",
			setup: func(t *testing.T, db *sql.DB) {
				insertInvoiceExpenseForManualMoveTest(t, db, "destination-settled", "invoice-2026-08", 20000)
				insertActiveInvoicePaymentForManualMoveTest(t, db, "destination-settled", "invoice-2026-08", 20000)
			},
		},
		{
			name: "closed financially overpaid",
			setup: func(t *testing.T, db *sql.DB) {
				insertInvoiceExpenseForManualMoveTest(t, db, "destination-overpaid", "invoice-2026-08", 20000)
				insertActiveInvoicePaymentForManualMoveTest(t, db, "destination-overpaid", "invoice-2026-08", 21000)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedManualInvoiceMoveScenarioForTest(t, db)
			tc.setup(t, db)
			before := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
			rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
			after := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
			}
			if after.invoiceID != before.invoiceID || after.overrideID != before.overrideID || after.destinationTotal != before.destinationTotal || after.sourceTotal != before.sourceTotal {
				t.Fatalf("blocked move changed state: before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("unknown destination status is structural", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = ON`)
		execTestSQL(t, db, `UPDATE invoices SET status = 'BROKEN' WHERE id = 'invoice-2026-08'`)
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = OFF`)

		beforeTx := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
		beforeSource := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-09")
		beforeDestination := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-08")

		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body = %q", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "BROKEN") {
			t.Fatalf("response leaked structural status: %q", rr.Body.String())
		}

		afterTx := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
		afterSource := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-09")
		afterDestination := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-08")
		if afterTx != beforeTx {
			t.Fatalf("structural block changed transaction: before=%+v after=%+v", beforeTx, afterTx)
		}
		invoiceStatesEqual(t, "source", beforeSource, afterSource)
		invoiceStatesEqual(t, "destination", beforeDestination, afterDestination)
	})
}

func TestManualInvoiceMoveProtectsSourcePayments(t *testing.T) {
	for _, tc := range []struct {
		name       string
		payment    int64
		wantStatus int
		wantSource string
	}{
		{name: "would overpay source", payment: 15000, wantStatus: http.StatusConflict, wantSource: "CLOSED"},
		{name: "source exactly settled", payment: 10000, wantStatus: http.StatusOK, wantSource: "PAID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedManualInvoiceMoveScenarioForTest(t, db)
			insertInvoiceExpenseForManualMoveTest(t, db, "source-remainder", "invoice-2026-09", 10000)
			insertActiveInvoicePaymentForManualMoveTest(t, db, "source-payment", "invoice-2026-09", tc.payment)
			const paymentAt = int64(1700000000)
			execTestSQL(t, db, `UPDATE invoice_payments SET paid_at = ?, created_at = ? WHERE id = 'source-payment'`, paymentAt, paymentAt)
			execTestSQL(t, db, `UPDATE invoices SET status = 'CLOSED', closing_date = ? WHERE id = 'invoice-2026-09'`, testUnixDate("2026-07-20"))

			transitionStart := time.Now().Unix()
			rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
			transitionEnd := time.Now().Unix()
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, tc.wantStatus, rr.Body.String())
			}
			var invoiceID, status string
			if err := db.QueryRow(`SELECT invoice_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-2026-09'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != tc.wantSource {
				t.Fatalf("source status = %q, want %q", status, tc.wantSource)
			}
			if tc.wantSource == "PAID" {
				var paidAt sql.NullInt64
				if err := db.QueryRow(`SELECT paid_at FROM invoices WHERE id = 'invoice-2026-09'`).Scan(&paidAt); err != nil {
					t.Fatal(err)
				}
				if !paidAt.Valid || paidAt.Int64 < transitionStart || paidAt.Int64 > transitionEnd || paidAt.Int64 == paymentAt {
					t.Fatalf("source paid_at = %v, want composition transition in [%d,%d] and not payment %d", paidAt, transitionStart, transitionEnd, paymentAt)
				}
			}
			if tc.wantStatus == http.StatusConflict && invoiceID != "invoice-2026-09" {
				t.Fatalf("blocked source move changed invoice to %q", invoiceID)
			}
		})
	}
}

func TestManualInvoiceMoveWorkspaceCardIdempotencyAndAuto(t *testing.T) {
	t.Run("other card is conflict", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		seedOtherCardInvoiceForManualMoveTest(t, db, "ws-test", "card-other", "invoice-other-card")
		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-other-card", "single")
		if rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
	})

	t.Run("other workspace is not found", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		seedOtherCardInvoiceForManualMoveTest(t, db, "ws-other", "card-other-workspace", "invoice-other-workspace")
		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-other-workspace", "single")
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "workspace") {
			t.Fatalf("status/body = %d/%q", rr.Code, rr.Body.String())
		}
	})

	t.Run("same destination persists override then is idempotent", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-09", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("first status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var invoiceID, overrideID string
		var updatedAt int64
		if err := db.QueryRow(`SELECT invoice_id, invoice_override_id, updated_at FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID, &overrideID, &updatedAt); err != nil {
			t.Fatal(err)
		}
		rr = postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-09", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("second status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var afterUpdatedAt int64
		if err := db.QueryRow(`SELECT updated_at FROM transactions WHERE id = 'purchase-test'`).Scan(&afterUpdatedAt); err != nil {
			t.Fatal(err)
		}
		if invoiceID != "invoice-2026-09" || overrideID != invoiceID || afterUpdatedAt != updatedAt {
			t.Fatalf("invoice/override/updated = %q/%q/%d -> %d", invoiceID, overrideID, updatedAt, afterUpdatedAt)
		}
	})

	t.Run("same destination replaces a different override", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		execTestSQL(t, db, `UPDATE transactions SET invoice_override_id = 'invoice-2026-08' WHERE id = 'purchase-test'`)
		before := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-09", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		after := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
		if after.invoiceID != "invoice-2026-09" || after.overrideID != "invoice-2026-09" || after.destinationTotal != before.destinationTotal || after.sourceTotal != before.sourceTotal {
			t.Fatalf("before=%+v after=%+v", before, after)
		}
	})

	t.Run("explicit auto clears override and resolves canonical cycle", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("manual status = %d, body = %q", rr.Code, rr.Body.String())
		}
		rr = postInvoiceAssignmentModeForTest(t, db, "purchase-test", "invoice-2026-08", "", "auto", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("auto status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var invoiceID string
		var overrideID sql.NullString
		if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID, &overrideID); err != nil {
			t.Fatal(err)
		}
		if invoiceID != "invoice-2026-09" || overrideID.Valid {
			t.Fatalf("invoice/override = %q/%v, want canonical September/NULL", invoiceID, overrideID)
		}
	})
}

func TestManualInvoiceMoveSingleScopeRollbackAndInstallmentIsolation(t *testing.T) {
	t.Run("arbitrary future scope blocked", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "future")
		if rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		var invoiceID string
		if err := db.QueryRow(`SELECT invoice_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID); err != nil {
			t.Fatal(err)
		}
		if invoiceID != "invoice-2026-09" {
			t.Fatalf("invoice = %q, want unchanged September", invoiceID)
		}
	})

	t.Run("single installment changes only selected row", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedInvoicePaymentScenario(t, db)
		seedExpenseCategoryForInvoiceResolutionTest(t, db)
		seedInstallmentMoveScenario(t, db)
		rr := postManualInvoiceMoveForTest(t, db, "installment-2", "invoice-2026-09", "invoice-2026-08", "single")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		rows, err := db.Query(`SELECT id, invoice_id, invoice_override_id FROM transactions WHERE id LIKE 'installment-%' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		want := map[string][2]string{
			"installment-1": {"invoice-2026-08", ""},
			"installment-2": {"invoice-2026-08", "invoice-2026-08"},
			"installment-3": {"invoice-2026-10", ""},
		}
		for rows.Next() {
			var id, invoiceID string
			var overrideID sql.NullString
			if err := rows.Scan(&id, &invoiceID, &overrideID); err != nil {
				t.Fatal(err)
			}
			if got := [2]string{invoiceID, overrideID.String}; got != want[id] {
				t.Fatalf("%s = %v, want %v", id, got, want[id])
			}
		}
	})

	t.Run("failure after update rolls back all state", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)

		beforeTx := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
		beforeSourceInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-09")
		beforeDestInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-08")
		beforePayments := capturePaymentsForTest(t, db, "ws-test", "invoice-2026-09", "invoice-2026-08")

		h := TransactionHandler{
			DB: db, Templates: testPaidInvoiceMutationTemplates(), WorkspaceID: "ws-test", UserID: "user-test",
			testHooks: &recurringEditTestHooks{afterInvoiceMove: func(*sql.Tx) error { return errors.New("forced rollback") }},
		}
		rr := postInvoiceAssignmentWithHandlerForTest(t, &h, "purchase-test", "invoice-2026-09", "invoice-2026-08", "manual", "single")
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}

		afterTx := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
		afterSourceInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-09")
		afterDestInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-08")
		afterPayments := capturePaymentsForTest(t, db, "ws-test", "invoice-2026-09", "invoice-2026-08")

		if afterTx != beforeTx {
			t.Fatalf("transaction rollback mismatch: before=%+v after=%+v", beforeTx, afterTx)
		}
		invoiceStatesEqual(t, "source (invoice-2026-09)", beforeSourceInvoice, afterSourceInvoice)
		invoiceStatesEqual(t, "destination (invoice-2026-08)", beforeDestInvoice, afterDestInvoice)
		paymentSnapshotsEqual(t, "combined payments", beforePayments, afterPayments)
	})
}

func TestManualInvoiceMoveOverrideDurability(t *testing.T) {
	t.Run("ordinary edit on closed invoice is blocked and preserves override", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		if rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single"); rr.Code != http.StatusOK {
			t.Fatalf("manual status = %d, body = %q", rr.Code, rr.Body.String())
		}
		rr := postInvoiceAssignmentModeForTest(t, db, "purchase-test", "invoice-2026-08", "", "", "single")
		if rr.Code != http.StatusConflict {
			t.Fatalf("edit status = %d, want 409, body = %q", rr.Code, rr.Body.String())
		}
		var invoiceID, overrideID string
		if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID, &overrideID); err != nil {
			t.Fatal(err)
		}
		if invoiceID != "invoice-2026-08" || overrideID != invoiceID {
			t.Fatalf("invoice/override = %q/%q, want August", invoiceID, overrideID)
		}
	})

	t.Run("recurring materialization does not overwrite manual invoice", func(t *testing.T) {
		db := openTestDB(t)
		defer db.Close()
		seedManualInvoiceMoveScenarioForTest(t, db)
		execTestSQL(t, db, `
			INSERT INTO recurring_rules (
				id, workspace_id, user_id, account_id, category_id, type, amount,
				description, start_date, frequency, active, created_at, updated_at
			) VALUES ('rule-manual-override', 'ws-test', 'user-test', 'card-test', 'cat-expense',
				'EXPENSE', 25000, 'Compra XP 01/08', ?, 'MONTHLY', 1, ?, ?)
		`, testUnixDate("2026-08-01"), time.Now().Unix(), time.Now().Unix())
		execTestSQL(t, db, `
			UPDATE transactions
			SET invoice_id = 'invoice-2026-08', invoice_override_id = 'invoice-2026-08',
			    recurring_rule_id = 'rule-manual-override', recurrence_sequence = 1
			WHERE id = 'purchase-test'
		`)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		rule := recurringInvoiceRuleSeed{
			id: "rule-manual-override", userID: "user-test", categoryID: sql.NullString{String: "cat-expense", Valid: true},
			amount: 25000, description: "Compra XP 01/08", startDate: testUnixDate("2026-08-01"), frequency: "MONTHLY",
		}
		if err := materializeRecurringInvoiceOccurrenceTx(tx, "ws-test", "invoice-2026-09", "card-test", rule, testUnixDate("2026-08-01"), 1, time.Now().Unix()+100); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var invoiceID, overrideID string
		if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID, &overrideID); err != nil {
			t.Fatal(err)
		}
		if invoiceID != "invoice-2026-08" || overrideID != invoiceID {
			t.Fatalf("invoice/override = %q/%q after materialization", invoiceID, overrideID)
		}
	})
}

func TestManualInvoiceQuickMoveReplayIsRejected(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "OPEN")
	var sourceInvoice string
	var sourceUpdatedAt int64
	if err := db.QueryRow(`SELECT invoice_id, updated_at FROM transactions WHERE id = 'purchase-test'`).Scan(&sourceInvoice, &sourceUpdatedAt); err != nil {
		t.Fatal(err)
	}
	h := TransactionHandler{DB: db, Templates: testPaidInvoiceMutationTemplates(), WorkspaceID: "ws-test", UserID: "user-test"}

	wrongDestination := httptest.NewRecorder()
	h.HandleMoverFatura(wrongDestination, quickInvoiceMoveRequestWithStateForTest("purchase-test", sourceInvoice, "2026-10", sourceUpdatedAt, "single"), "purchase-test")
	if wrongDestination.Code != http.StatusConflict {
		t.Fatalf("wrong destination status = %d, body = %q", wrongDestination.Code, wrongDestination.Body.String())
	}
	wrongScope := httptest.NewRecorder()
	h.HandleMoverFatura(wrongScope, quickInvoiceMoveRequestWithStateForTest("purchase-test", sourceInvoice, "2026-09", sourceUpdatedAt, "chain"), "purchase-test")
	if wrongScope.Code != http.StatusConflict {
		t.Fatalf("wrong scope status = %d, body = %q", wrongScope.Code, wrongScope.Body.String())
	}

	first := httptest.NewRecorder()
	h.HandleMoverFatura(first, quickInvoiceMoveRequestWithStateForTest("purchase-test", sourceInvoice, "2026-09", sourceUpdatedAt, "single"), "purchase-test")
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %q", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	h.HandleMoverFatura(second, quickInvoiceMoveRequestWithStateForTest("purchase-test", sourceInvoice, "2026-09", sourceUpdatedAt, "single"), "purchase-test")
	if second.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, body = %q", second.Code, second.Body.String())
	}
	var invoiceID, overrideID string
	if err := db.QueryRow(`SELECT invoice_id, invoice_override_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID, &overrideID); err != nil {
		t.Fatal(err)
	}
	if invoiceID != "invoice-2026-09" || overrideID != invoiceID {
		t.Fatalf("invoice/override after replay = %q/%q, want September", invoiceID, overrideID)
	}
}

func TestManualInvoiceMoveTwoConnectionsCAS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "manual-invoice-concurrent.db")
	bootstrap, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap disk db: %v", err)
	}
	seedManualInvoiceMoveScenarioForTest(t, bootstrap)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap: %v", err)
	}

	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	openPool := func() *sql.DB {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open disk db: %v", err)
		}
		db.SetMaxOpenConns(1)
		return db
	}
	dbA := openPool()
	defer dbA.Close()
	dbB := openPool()
	defer dbB.Close()

	var sourceInvoice string
	var sourceUpdatedAt int64
	if err := dbA.QueryRow(`SELECT invoice_id, updated_at FROM transactions WHERE id = 'purchase-test'`).Scan(&sourceInvoice, &sourceUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if sourceInvoice != "invoice-2026-09" {
		t.Fatalf("source invoice = %q, want invoice-2026-09", sourceInvoice)
	}

	// Snapshot financeiro integral antes da disputa.
	beforeTx := manualInvoiceMoveSnapshotForTest(t, dbA, "purchase-test", "invoice-2026-08", "invoice-2026-09")
	beforeDest := captureInvoiceStateForTest(t, dbA, "ws-test", "invoice-2026-08")
	beforePayments := capturePaymentsForTest(t, dbA, "ws-test", "invoice-2026-09", "invoice-2026-08")

	// Duas conexões independentes: cada goroutine abre sua própria transação,
	// lê invoice_id e updated_at da transação alvo, sinaliza que o snapshot
	// pré-CAS está pronto e só então disputa via assignTransactionInvoiceTx real.
	startRace := make(chan struct{})
	bothReady := make(chan struct{}, 2)
	type casSnapshot struct {
		invoiceID string
		updatedAt int64
	}
	type casResult struct {
		label   string
		changed bool
		err     error
	}
	results := make(chan casResult, 2)

	run := func(label string, db *sql.DB) {
		tx, err := db.Begin()
		if err != nil {
			results <- casResult{label: label, err: fmt.Errorf("begin: %w", err)}
			return
		}
		defer tx.Rollback()

		var snap casSnapshot
		if err := tx.QueryRow(`
			SELECT invoice_id, updated_at
			FROM transactions
			WHERE id = 'purchase-test' AND workspace_id = 'ws-test'
		`).Scan(&snap.invoiceID, &snap.updatedAt); err != nil {
			results <- casResult{label: label, err: fmt.Errorf("read: %w", err)}
			return
		}
		if snap.invoiceID != sourceInvoice {
			results <- casResult{label: label, err: fmt.Errorf("snapshot invoice_id diverged: %q", snap.invoiceID)}
			return
		}
		if snap.updatedAt != sourceUpdatedAt {
			results <- casResult{label: label, err: fmt.Errorf("snapshot updated_at diverged: %d", snap.updatedAt)}
			return
		}

		h := TransactionHandler{DB: db, Templates: testPaidInvoiceMutationTemplates(), WorkspaceID: "ws-test", UserID: "user-test"}

		bothReady <- struct{}{}
		<-startRace
		result, err := h.assignTransactionInvoiceTx(tx, "purchase-test", "invoice-2026-08", sourceInvoice, &snap.updatedAt, true, time.Now().Unix())

		changed := err == nil && result.Changed
		if changed {
			if cerr := tx.Commit(); cerr != nil {
				results <- casResult{label: label, err: fmt.Errorf("commit: %w", cerr)}
				return
			}
		}
		results <- casResult{label: label, changed: changed, err: err}
	}

	go run("A", dbA)
	go run("B", dbB)
	<-bothReady
	<-bothReady
	close(startRace)

	rA := <-results
	rB := <-results

	if rA.err != nil && !errors.Is(rA.err, errInvoiceMoveConflict) && !isSQLiteBusyLike(rA.err) {
		t.Fatalf("unexpected A error: %v", rA.err)
	}
	if rB.err != nil && !errors.Is(rB.err, errInvoiceMoveConflict) && !isSQLiteBusyLike(rB.err) {
		t.Fatalf("unexpected B error: %v", rB.err)
	}
	if !((rA.changed && !rB.changed) || (rB.changed && !rA.changed)) {
		t.Fatalf("concurrent CAS outcome unexpected: A=(changed=%v err=%v) B=(changed=%v err=%v)", rA.changed, rA.err, rB.changed, rB.err)
	}

	// Verificações de estado final via leitura nova em conexão independente.
	afterTx := manualInvoiceMoveSnapshotForTest(t, dbA, "purchase-test", "invoice-2026-08", "invoice-2026-09")
	afterSource := captureInvoiceStateForTest(t, dbA, "ws-test", "invoice-2026-09")
	afterDest := captureInvoiceStateForTest(t, dbA, "ws-test", "invoice-2026-08")
	afterPayments := capturePaymentsForTest(t, dbA, "ws-test", "invoice-2026-09", "invoice-2026-08")

	if afterTx.invoiceID != "invoice-2026-08" || afterTx.overrideID != afterTx.invoiceID || afterTx.sourceTotal != 0 || afterTx.destinationTotal != 25000 {
		t.Fatalf("transaction ended in wrong state: %+v", afterTx)
	}
	if afterTx == beforeTx {
		t.Fatalf("transaction snapshot did not change after winning move")
	}
	if afterSource.Total != 0 || afterSource.TxCount != 0 {
		t.Fatalf("source invoice still holds transaction: %+v", afterSource)
	}
	if afterDest.Total != beforeDest.Total+25000 || afterDest.TxCount != beforeDest.TxCount+1 {
		t.Fatalf("destination not reconciled exactly once: before=%+v after=%+v", beforeDest, afterDest)
	}
	paymentSnapshotsEqual(t, "combined payments", beforePayments, afterPayments)

	// A transação vencedora deve ser a única alteração: nenhum estado parcial
	// da perdedora pode ter persistido.
	var moveCount int
	if err := dbA.QueryRow(`SELECT COUNT(1) FROM transactions WHERE id = 'purchase-test' AND invoice_id = 'invoice-2026-08'`).Scan(&moveCount); err != nil {
		t.Fatal(err)
	}
	if moveCount != 1 {
		t.Fatalf("expected exactly one moved transaction, got %d", moveCount)
	}
}

func isSQLiteBusyLike(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "busy") || strings.Contains(msg, "database is locked")
}

func TestManualInvoiceMoveDerivedViewsAndFinancialDates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedManualInvoiceMoveScenarioForTest(t, db)

	execTestSQL(t, db, `INSERT OR IGNORE INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-second', 'ws-test', 'Categoria Secundaria', 'car', '#ef4444', 'EXPENSE', 'Essencial', ?)`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `UPDATE invoices SET due_date = ? WHERE id = 'invoice-2026-09'`, testUnixDate("2026-09-15"))

	insertInvoiceExpenseForManualMoveTest(t, db, "dst-extra", "invoice-2026-08", 30000)
	execTestSQL(t, db, `UPDATE transactions SET category_id = 'cat-second' WHERE id = 'dst-extra'`)
	insertActiveInvoicePaymentForManualMoveTest(t, db, "dst-partial", "invoice-2026-08", 20000)
	execTestSQL(t, db, `UPDATE invoice_payments SET paid_at = ? WHERE id = 'ip-dst-partial'`, testUnixDate("2026-08-01"))

	report := RelatoriosHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}

	descSrc := "desc"
	beforeSourceFatura, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-09", descSrc)
	if err != nil {
		t.Fatal(err)
	}
	beforeDestFatura, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", descSrc)
	if err != nil {
		t.Fatal(err)
	}

	beforeRealizado, err := report.buildRelatoriosDataWithModo("", "caixa", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	beforePrevisao, err := report.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	beforeCompetencia, err := report.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}

	beforePayment := capturePaymentRowForTest(t, db, "ip-dst-partial")
	var beforePaymentCount int
	var beforePaymentSum int64
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(amount_cents), 0) FROM invoice_payments WHERE workspace_id = 'ws-test' AND reversed_at IS NULL`).Scan(&beforePaymentCount, &beforePaymentSum); err != nil {
		t.Fatal(err)
	}

	// Limites e dívida global antes da movimentação.
	if !beforeDestFatura.HasCreditLimit {
		t.Fatalf("destination fatura must have credit limit")
	}
	limitBefore := beforeDestFatura.LimitAvailable.rawCents()
	debtBefore, err := sumCardOutstandingLimit(db, "ws-test", "card-test")
	if err != nil {
		t.Fatalf("sum card outstanding before: %v", err)
	}

	rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	afterRealizado, err := report.buildRelatoriosDataWithModo("", "caixa", "movimentos", "realizado", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	afterPrevisao, err := report.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	afterCompetencia, err := report.buildRelatoriosData("", "competencia", "movimentos", 8, 2026)
	if err != nil {
		t.Fatal(err)
	}
	afterSourceFatura, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-09", descSrc)
	if err != nil {
		t.Fatal(err)
	}
	afterDestFatura, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", descSrc)
	if err != nil {
		t.Fatal(err)
	}

	// A. Caixa realizado: vem do pagamento da fatura (20000 cents = R$ 200,00).
	// A troca de fatura não cria nem remove caixa realizado.
	if beforeRealizado.TotalDespesas.rawCents() != 20000 {
		t.Fatalf("caixa realizado despesa antes = %d, want 20000", beforeRealizado.TotalDespesas.rawCents())
	}
	if afterRealizado.TotalDespesas.rawCents() != 20000 {
		t.Fatalf("caixa realizado despesa depois = %d, want 20000", afterRealizado.TotalDespesas.rawCents())
	}
	if beforeRealizado.TotalDespesas != afterRealizado.TotalDespesas {
		t.Fatalf("caixa realizado despesa mudou: antes=%s%s depois=%s%s",
			beforeRealizado.TotalDespesas.Reais, beforeRealizado.TotalDespesas.Cents,
			afterRealizado.TotalDespesas.Reais, afterRealizado.TotalDespesas.Cents)
	}

	// B. Competência canônica: a data civil da compra (2026-08-01) não muda.
	// A competência de agosto contém a compra (25000) + dst-extra (30000) = 55000.
	// A linha canônica é a categoria no relatório de competência.
	beforeCompetenciaTotal := beforeCompetencia.TotalDespesas.rawCents()
	afterCompetenciaTotal := afterCompetencia.TotalDespesas.rawCents()
	if beforeCompetenciaTotal != 55000 {
		t.Fatalf("competencia despesa total antes = %d, want 55000", beforeCompetenciaTotal)
	}
	if afterCompetenciaTotal != 55000 {
		t.Fatalf("competencia despesa total depois = %d, want 55000", afterCompetenciaTotal)
	}
	if beforeCompetenciaTotal != afterCompetenciaTotal {
		t.Fatalf("competencia despesa total mudou: antes=%d depois=%d", beforeCompetenciaTotal, afterCompetenciaTotal)
	}
	beforeCompCatExpense := mustFindCategoryBarValueForTest(t, beforeCompetencia.Categorias, "cat-expense")
	beforeCompCatSecond := mustFindCategoryBarValueForTest(t, beforeCompetencia.Categorias, "cat-second")
	afterCompCatExpense := mustFindCategoryBarValueForTest(t, afterCompetencia.Categorias, "cat-expense")
	afterCompCatSecond := mustFindCategoryBarValueForTest(t, afterCompetencia.Categorias, "cat-second")
	if beforeCompCatExpense != 25000 {
		t.Fatalf("competencia cat-expense antes = %d, want 25000", beforeCompCatExpense)
	}
	if afterCompCatExpense != 25000 {
		t.Fatalf("competencia cat-expense depois = %d, want 25000", afterCompCatExpense)
	}
	if beforeCompCatSecond != 30000 {
		t.Fatalf("competencia cat-second antes = %d, want 30000", beforeCompCatSecond)
	}
	if afterCompCatSecond != 30000 {
		t.Fatalf("competencia cat-second depois = %d, want 30000", afterCompCatSecond)
	}

	// C/D. Previsão por vencimento: a compra deixa setembro e entra em agosto.
	beforeCaixaAugust := mustFindCaixaMonth(t, beforePrevisao.Caixa, "2026-08")
	afterCaixaAugust := mustFindCaixaMonth(t, afterPrevisao.Caixa, "2026-08")
	beforeCaixaSeptember := mustFindCaixaMonth(t, beforePrevisao.Caixa, "2026-09")
	afterCaixaSeptember := mustFindCaixaMonth(t, afterPrevisao.Caixa, "2026-09")

	// Antes: realizado 20000 + pending 10000 = 30000 (RawResultado = -30000, despesa).
	// Depois: realizado 20000 + pending 35000 = 55000 (RawResultado = -55000).
	if beforeCaixaAugust.RawResultado != -30000 {
		t.Fatalf("previsao agosto antes = %d, want -30000", beforeCaixaAugust.RawResultado)
	}
	if afterCaixaAugust.RawResultado != -55000 {
		t.Fatalf("previsao agosto depois = %d, want -55000", afterCaixaAugust.RawResultado)
	}
	if beforeCaixaSeptember.RawResultado != -25000 {
		t.Fatalf("previsao setembro antes = %d, want -25000", beforeCaixaSeptember.RawResultado)
	}
	if afterCaixaSeptember.RawResultado != 0 {
		t.Fatalf("previsao setembro depois = %d, want 0", afterCaixaSeptember.RawResultado)
	}

	// E. Rateio pago e pendente.
	// A fatura destino (representação canônica da fatura) possui:
	// total 55000, pagamento ativo 20000 e pendente 35000.
	if afterDestFatura.Total.rawCents() != 55000 {
		t.Fatalf("dest fatura total = %d, want 55000", afterDestFatura.Total.rawCents())
	}
	if afterDestFatura.TotalPaid.rawCents() != 20000 {
		t.Fatalf("dest fatura effective paid = %d, want 20000", afterDestFatura.TotalPaid.rawCents())
	}
	if afterDestFatura.PendingAmount.rawCents() != 35000 {
		t.Fatalf("dest fatura pending = %d, want 35000", afterDestFatura.PendingAmount.rawCents())
	}
	// No relatório caixa/previsão, RealizedExpenseTotal é o realizado/pago do mês
	// e CashMonthExpenseTotal é o total combinado (realizado + projetado/pendente).
	// A parcela pendente é a diferença entre esses campos reais.
	if afterPrevisao.RealizedExpenseTotal.rawCents() != 20000 {
		t.Fatalf("previsao rateio pago = %d, want 20000", afterPrevisao.RealizedExpenseTotal.rawCents())
	}
	if afterPrevisao.CashMonthExpenseTotal.rawCents() != 55000 {
		t.Fatalf("previsao despesa mês = %d, want 55000", afterPrevisao.CashMonthExpenseTotal.rawCents())
	}
	reportPending := afterPrevisao.CashMonthExpenseTotal.rawCents() - afterPrevisao.RealizedExpenseTotal.rawCents()
	if reportPending != 35000 {
		t.Fatalf("previsao rateio pendente = %d, want 35000 (CashMonth - Realized)", reportPending)
	}

	// F. Unicidade das categorias no rateio de previsão.
	beforeCatExpense := mustFindCategoryBarValueForTest(t, beforePrevisao.CategoriasCaixa, "cat-expense")
	beforeCatSecond := mustFindCategoryBarValueForTest(t, beforePrevisao.CategoriasCaixa, "cat-second")
	afterCatExpense := mustFindCategoryBarValueForTest(t, afterPrevisao.CategoriasCaixa, "cat-expense")
	afterCatSecond := mustFindCategoryBarValueForTest(t, afterPrevisao.CategoriasCaixa, "cat-second")

	if beforeCatExpense != 0 {
		t.Fatalf("cat-expense no rateio agosto antes = %d, want 0", beforeCatExpense)
	}
	if beforeCatSecond != 30000 {
		t.Fatalf("cat-second no rateio agosto antes = %d, want 30000", beforeCatSecond)
	}
	if afterCatExpense != 25000 {
		t.Fatalf("cat-expense no rateio agosto depois = %d, want 25000", afterCatExpense)
	}
	if afterCatSecond != 30000 {
		t.Fatalf("cat-second no rateio agosto depois = %d, want 30000", afterCatSecond)
	}

	var afterCatSum int64
	for _, c := range afterPrevisao.CategoriasCaixa {
		if c.IsGroup {
			continue
		}
		afterCatSum += c.Valor.rawCents()
	}
	if afterCatSum != afterPrevisao.ProjectedExpenseTotal.rawCents() {
		t.Fatalf("soma rateio %d != despesa prevista %d", afterCatSum, afterPrevisao.ProjectedExpenseTotal.rawCents())
	}

	// G. Limite disponível: movimentação entre faturas do mesmo cartão não altera
	// a dívida global consumidora de limite.
	if !afterDestFatura.HasCreditLimit {
		t.Fatalf("destination fatura must keep credit limit after move")
	}
	limitAfter := afterDestFatura.LimitAvailable.rawCents()
	if limitBefore != limitAfter {
		t.Fatalf("limit changed: before=%d after=%d", limitBefore, limitAfter)
	}
	if limitBefore != 465000 {
		t.Fatalf("limit before = %d, want 465000", limitBefore)
	}

	// H. Dívida global: total devido do cartão permanece inalterado.
	debtAfter, err := sumCardOutstandingLimit(db, "ws-test", "card-test")
	if err != nil {
		t.Fatalf("sum card outstanding after: %v", err)
	}
	if debtBefore != debtAfter {
		t.Fatalf("global debt changed: before=%d after=%d", debtBefore, debtAfter)
	}
	if debtBefore != 35000 {
		t.Fatalf("global debt before = %d, want 35000", debtBefore)
	}

	// I. Pagamento: não é movido junto com a compra.
	afterPayment := capturePaymentRowForTest(t, db, "ip-dst-partial")
	if afterPayment != beforePayment {
		t.Fatalf("invoice_payment mudou:\n  antes=%+v\n  depois=%+v", beforePayment, afterPayment)
	}

	var afterPaymentCount int
	var afterPaymentSum int64
	if err := db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(amount_cents), 0) FROM invoice_payments WHERE workspace_id = 'ws-test' AND reversed_at IS NULL`).Scan(&afterPaymentCount, &afterPaymentSum); err != nil {
		t.Fatal(err)
	}
	if afterPaymentCount != beforePaymentCount || afterPaymentSum != beforePaymentSum {
		t.Fatalf("payments changed: before count=%d sum=%d, after count=%d sum=%d", beforePaymentCount, beforePaymentSum, afterPaymentCount, afterPaymentSum)
	}

	// Sanity totals.
	if beforeSourceFatura.Total != MoneyMinor(25000) {
		t.Fatalf("source total before = %+v, want R$ 250,00", beforeSourceFatura.Total)
	}
	if afterSourceFatura.Total != MoneyMinor(0) {
		t.Fatalf("source total after = %+v, want R$ 0,00", afterSourceFatura.Total)
	}
	if afterDestFatura.Total != MoneyMinor(55000) {
		t.Fatalf("dest total after = %+v, want R$ 550,00", afterDestFatura.Total)
	}
}

func mustFindCaixaMonth(t *testing.T, rows []CaixaRow, vencimento string) CaixaRow {
	t.Helper()
	var found *CaixaRow
	for i := range rows {
		if rows[i].Vencimento == vencimento {
			if found != nil {
				t.Fatalf("duplicated caixa row for %s", vencimento)
			}
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("caixa row for %s not found in %#v", vencimento, rows)
	}
	return *found
}

func mustFindCategoryBarValueForTest(t *testing.T, bars []CategoriaBar, categoryID string) int64 {
	t.Helper()
	var found *CategoriaBar
	for i := range bars {
		c := &bars[i]
		if c.IsGroup {
			continue
		}
		if c.ID == categoryID {
			if found != nil {
				t.Fatalf("category %s appears more than once in report", categoryID)
			}
			found = c
		}
	}
	if found == nil {
		return 0
	}
	return found.Valor.rawCents()
}

func capturePaymentRowForTest(t *testing.T, db *sql.DB, id string) manualInvoiceMovePaymentRow {
	t.Helper()
	var row manualInvoiceMovePaymentRow
	if err := db.QueryRow(`
		SELECT id, invoice_id, account_id, transaction_id, amount_cents, paid_at,
		       reversed_at, source, created_by, created_at, note
		FROM invoice_payments
		WHERE id = ?
	`, id).Scan(&row.ID, &row.InvoiceID, &row.AccountID, &row.TransactionID,
		&row.AmountCents, &row.PaidAt, &row.ReversedAt, &row.Source, &row.CreatedBy,
		&row.CreatedAt, &row.Note); err != nil {
		t.Fatalf("capture payment %s: %v", id, err)
	}
	return row
}

func TestManualInvoiceTemplateDisablesPaidAndExplainsClosed(t *testing.T) {
	tpl := manualInvoiceTemplateForTest(t)
	data := FormTransacaoData{
		IsEdit:                  true,
		EditID:                  "purchase-test",
		OrigemTipo:              "cartao",
		InvoiceAssignmentMode:   "manual",
		ManualInvoiceID:         "invoice-closed",
		TransactionUpdatedAt:    123,
		QuickMoveDestinationRef: "2026-09",
		QuickMoveScope:          "single",
		ReturnInvoiceID:         "invoice-closed",
		TipoInicial:             "despesa",
		ValorPreenchido:         "250,00",
		DataPreenchida:          "2026-08-01",
		DescricaoPreenchida:     "Compra",
		StatusInicial:           "paid",
		FaturaOptions: []InvoiceOption{
			{ID: "invoice-open", Status: "OPEN", ReferenceLabel: "Setembro", ManualEligible: true},
			{ID: "invoice-closed", Status: "CLOSED", ReferenceLabel: "Agosto", IsSensitive: true, ManualEligible: true},
			{ID: "invoice-paid", Status: "PAID", ReferenceLabel: "Julho", IsSensitive: true, ManualBlock: "Movimentação bloqueada: esta fatura está integralmente paga."},
		},
	}
	var body bytes.Buffer
	if err := tpl.ExecuteTemplate(&body, "form-lancamento", data); err != nil {
		t.Fatal(err)
	}
	html := body.String()
	for _, want := range []string{
		`value="invoice-closed"`,
		`value="invoice-paid"`,
		`disabled title="Movimentação bloqueada: esta fatura está integralmente paga."`,
		"Esta fatura está fechada. O lançamento será tratado como correção manual e o saldo pendente será recalculado.",
		"Movimentação bloqueada: esta fatura está integralmente paga.",
		"expected_invoice_id",
		"expected_destination_reference",
		"expected_updated_at",
		"expected_move_scope",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered template missing %q", want)
		}
	}

	db := openTestDB(t)
	defer db.Close()
	seedManualInvoiceMoveScenarioForTest(t, db)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ? WHERE id = 'invoice-2026-08'`, time.Now().Unix())
	if rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single"); rr.Code != http.StatusConflict {
		t.Fatalf("direct backend status = %d, body = %q", rr.Code, rr.Body.String())
	}
}

type manualInvoiceMoveSnapshot struct {
	workspaceID                string
	userID                     string
	accountID                  string
	destinationAccountID       sql.NullString
	categoryID                 string
	invoiceID                  string
	overrideID                 string
	txType                     string
	amount                     int64
	date                       int64
	dateCivil                  string
	dueDate                    sql.NullInt64
	dueDateCivil               sql.NullString
	description                string
	notes                      string
	attachmentPath             string
	installment                int64
	totalInstallment           int64
	parentID                   sql.NullString
	recurringRuleID            sql.NullString
	seriesID                   sql.NullString
	recurrenceSeq              sql.NullInt64
	isRecurringException       int64
	recurringExceptionReason   sql.NullString
	paymentStatus              string
	status                     string
	paidAt                     sql.NullInt64
	scheduledPaymentAt         sql.NullInt64
	scheduledPaymentCreatedBy  sql.NullString
	scheduledPaymentExecutedAt sql.NullInt64
	contactID                  sql.NullString
	createdAt                  int64
	updatedAt                  int64
	destinationTotal           int64
	sourceTotal                int64
}

func manualInvoiceMoveSnapshotForTest(t *testing.T, db *sql.DB, transactionID, destinationID, sourceID string) manualInvoiceMoveSnapshot {
	t.Helper()
	var got manualInvoiceMoveSnapshot
	var overrideID sql.NullString
	var dateCivil sql.NullString
	var categoryID sql.NullString
	var destinationAccountID sql.NullString
	var dueDateCivil sql.NullString
	var recurringExceptionReason sql.NullString
	if err := db.QueryRow(`
		SELECT
			workspace_id, user_id, account_id, destination_account_id, category_id,
			invoice_id, invoice_override_id, type, amount, date, date_civil, due_date, due_date_civil,
			description, notes, attachment_path, installment_number, total_installments,
			parent_id, recurring_rule_id, series_id, recurrence_sequence,
			is_recurring_exception, recurring_exception_reason, payment_status, status,
			paid_at, scheduled_payment_at, scheduled_payment_created_by, scheduled_payment_executed_at,
			contact_id, created_at, updated_at
		FROM transactions WHERE id = ?
	`, transactionID).Scan(
		&got.workspaceID, &got.userID, &got.accountID, &destinationAccountID, &categoryID,
		&got.invoiceID, &overrideID, &got.txType, &got.amount, &got.date, &dateCivil, &got.dueDate, &dueDateCivil,
		&got.description, &got.notes, &got.attachmentPath, &got.installment, &got.totalInstallment,
		&got.parentID, &got.recurringRuleID, &got.seriesID, &got.recurrenceSeq,
		&got.isRecurringException, &recurringExceptionReason, &got.paymentStatus, &got.status,
		&got.paidAt, &got.scheduledPaymentAt, &got.scheduledPaymentCreatedBy, &got.scheduledPaymentExecutedAt,
		&got.contactID, &got.createdAt, &got.updatedAt,
	); err != nil {
		t.Fatalf("transaction snapshot: %v", err)
	}
	got.overrideID = overrideID.String
	got.dateCivil = dateCivil.String
	got.categoryID = categoryID.String
	got.destinationAccountID = destinationAccountID
	got.dueDateCivil = dueDateCivil
	got.recurringExceptionReason = recurringExceptionReason
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM transactions WHERE invoice_id = ?`, destinationID).Scan(&got.destinationTotal); err != nil {
		t.Fatalf("destination total: %v", err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM transactions WHERE invoice_id = ?`, sourceID).Scan(&got.sourceTotal); err != nil {
		t.Fatalf("source total: %v", err)
	}
	return got
}

func manualInvoiceProtectedFieldsEqualForTest(a, b manualInvoiceMoveSnapshot) bool {
	return a.date == b.date &&
		a.dateCivil == b.dateCivil &&
		a.dueDate == b.dueDate &&
		a.dueDateCivil == b.dueDateCivil &&
		a.amount == b.amount &&
		a.categoryID == b.categoryID &&
		a.description == b.description &&
		a.status == b.status &&
		a.paidAt == b.paidAt &&
		a.parentID == b.parentID &&
		a.recurringRuleID == b.recurringRuleID &&
		a.seriesID == b.seriesID &&
		a.recurrenceSeq == b.recurrenceSeq &&
		a.installment == b.installment &&
		a.totalInstallment == b.totalInstallment
}

type manualInvoiceMoveInvoiceState struct {
	ID          string
	AccountID   string
	Status      string
	PaidAt      sql.NullInt64
	PaidAmount  sql.NullInt64
	Reference   string
	ClosingDate sql.NullInt64
	DueDate     sql.NullInt64
	Total       int64
	TxCount     int
	PaidActive  int64
	Pending     int64
}

type manualInvoiceMovePaymentRow struct {
	ID            string
	InvoiceID     string
	AccountID     string
	TransactionID sql.NullString
	AmountCents   int64
	PaidAt        int64
	ReversedAt    sql.NullInt64
	Source        string
	CreatedBy     string
	CreatedAt     int64
	Note          sql.NullString
}

type manualInvoiceMovePaymentSnapshot struct {
	Rows        []manualInvoiceMovePaymentRow
	TotalCount  int
	ActiveSum   int64
	ActiveCount int
}

func captureInvoiceStateForTest(t *testing.T, db *sql.DB, workspaceID, invoiceID string) manualInvoiceMoveInvoiceState {
	t.Helper()
	var state manualInvoiceMoveInvoiceState
	state.ID = invoiceID
	var err error
	if err = db.QueryRow(`
		SELECT i.account_id, i.status, i.paid_at, i.paid_amount, i.reference, i.closing_date, i.due_date,
		       COALESCE((SELECT SUM(t.amount) FROM transactions t WHERE t.invoice_id = i.id AND t.type = 'EXPENSE'), 0),
		       (SELECT COUNT(1) FROM transactions t WHERE t.invoice_id = i.id AND t.type = 'EXPENSE')
		FROM invoices i
		WHERE i.id = ?
	`, invoiceID).Scan(
		&state.AccountID, &state.Status, &state.PaidAt, &state.PaidAmount, &state.Reference,
		&state.ClosingDate, &state.DueDate, &state.Total, &state.TxCount,
	); err != nil {
		t.Fatalf("capture invoice state for %s: %v", invoiceID, err)
	}
	state.PaidActive, _ = sumActiveInvoicePaymentsDB(db, workspaceID, invoiceID)
	state.Pending = state.Total - state.PaidActive
	if state.Pending < 0 {
		state.Pending = 0
	}
	return state
}

func capturePaymentsForTest(t *testing.T, db *sql.DB, workspaceID string, invoiceIDs ...string) manualInvoiceMovePaymentSnapshot {
	t.Helper()
	if len(invoiceIDs) == 0 {
		return manualInvoiceMovePaymentSnapshot{}
	}
	args := make([]interface{}, 0, len(invoiceIDs)+1)
	args = append(args, workspaceID)
	for _, id := range invoiceIDs {
		args = append(args, id)
	}
	placeholders := make([]string, len(invoiceIDs))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	rows, err := db.Query(`
		SELECT id, invoice_id, account_id, transaction_id, amount_cents, paid_at,
		       reversed_at, source, created_by, created_at, note
		FROM invoice_payments
		WHERE workspace_id = ? AND invoice_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY id ASC
	`, args...)
	if err != nil {
		t.Fatalf("capture payments: %v", err)
	}
	defer rows.Close()
	var snapshot manualInvoiceMovePaymentSnapshot
	for rows.Next() {
		var row manualInvoiceMovePaymentRow
		if err := rows.Scan(&row.ID, &row.InvoiceID, &row.AccountID, &row.TransactionID,
			&row.AmountCents, &row.PaidAt, &row.ReversedAt, &row.Source, &row.CreatedBy,
			&row.CreatedAt, &row.Note); err != nil {
			t.Fatalf("scan payment row: %v", err)
		}
		snapshot.Rows = append(snapshot.Rows, row)
		if !row.ReversedAt.Valid {
			snapshot.ActiveSum += row.AmountCents
			snapshot.ActiveCount++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate payments: %v", err)
	}
	snapshot.TotalCount = len(snapshot.Rows)
	return snapshot
}

func invoiceStatesEqual(t *testing.T, label string, before, after manualInvoiceMoveInvoiceState) {
	t.Helper()
	if before != after {
		t.Fatalf("%s invoice state mismatch:\n  before=%+v\n  after=%+v", label, before, after)
	}
}

func paymentSnapshotsEqual(t *testing.T, label string, before, after manualInvoiceMovePaymentSnapshot) {
	t.Helper()
	if before.TotalCount != after.TotalCount {
		t.Fatalf("%s payment count mismatch: before=%d after=%d", label, before.TotalCount, after.TotalCount)
	}
	if before.ActiveSum != after.ActiveSum {
		t.Fatalf("%s payment active sum mismatch: before=%d after=%d", label, before.ActiveSum, after.ActiveSum)
	}
	if before.ActiveCount != after.ActiveCount {
		t.Fatalf("%s payment active count mismatch: before=%d after=%d", label, before.ActiveCount, after.ActiveCount)
	}
	if len(before.Rows) != len(after.Rows) {
		t.Fatalf("%s payment row count mismatch: before=%d after=%d", label, len(before.Rows), len(after.Rows))
	}
	for i := range before.Rows {
		if before.Rows[i] != after.Rows[i] {
			t.Fatalf("%s payment row %d mismatch:\n  before=%+v\n  after=%+v", label, i, before.Rows[i], after.Rows[i])
		}
	}
}

func seedManualInvoiceMoveScenarioForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	seedExpenseCategoryForInvoiceResolutionTest(t, db)
	seedInvoiceForStatusTest(t, db, "card-test", "2026-09", "OPEN")
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'CLOSED', closing_date = ?, due_date = ?, paid_at = NULL, paid_amount = 0
		WHERE id = 'invoice-2026-08'
	`, testUnixDate("2026-07-20"), testUnixDate("2026-08-10"))
	execTestSQL(t, db, `
		UPDATE transactions
		SET invoice_id = 'invoice-2026-09', invoice_override_id = NULL,
		    category_id = 'cat-expense', date = ?, date_civil = '2026-08-01',
		    description = 'Compra XP 01/08', updated_at = 100
		WHERE id = 'purchase-test'
	`, testUnixDate("2026-08-01"))
}

func insertInvoiceExpenseForManualMoveTest(t *testing.T, db *sql.DB, id, invoiceID string, amount int64) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, invoice_id,
			type, amount, date, date_civil, description, status,
			installment_number, total_installments, created_at, updated_at
		) VALUES (?, 'ws-test', 'user-test', 'card-test', 'cat-expense', ?,
			'EXPENSE', ?, ?, '2026-08-02', 'Auxiliar', 'paid', 1, 1, ?, ?)
	`, id, invoiceID, amount, testUnixDate("2026-08-02"), now, now)
}

func insertActiveInvoicePaymentForManualMoveTest(t *testing.T, db *sql.DB, suffix, invoiceID string, amount int64) {
	t.Helper()
	now := time.Now().Unix()
	txID := "payment-" + suffix
	ipID := "ip-" + suffix
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date,
			description, status, installment_number, total_installments, created_at, updated_at
		) VALUES (?, 'ws-test', 'user-test', 'checking-test', 'EXPENSE', ?, ?,
			?, 'paid', 1, 1, ?, ?)
	`, txID, amount, now, invoicePaymentDescription("Cartao Teste"), now, now)
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, transaction_id,
			amount_cents, paid_at, source, created_by, created_at
		) VALUES (?, 'ws-test', ?, 'checking-test', ?, ?, ?, 'manual', 'user-test', ?)
	`, ipID, invoiceID, txID, amount, now, now)
}

func seedOtherCardInvoiceForManualMoveTest(t *testing.T, db *sql.DB, workspaceID, accountID, invoiceID string) {
	t.Helper()
	now := time.Now().Unix()
	if workspaceID != "ws-test" {
		execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, 'Other', '', ?, ?)`, workspaceID, now, now)
	}
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Other card', 'CREDIT_CARD', 0, 0, ?, ?)
	`, accountID, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES (?, ?, 20, 10, 100000)`, "cc-"+accountID, accountID)
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, '2026-08', ?, ?, 'OPEN', ?)
	`, invoiceID, accountID, testUnixDate("2026-07-20"), testUnixDate("2026-08-10"), now)
}

func postManualInvoiceMoveForTest(t *testing.T, db *sql.DB, transactionID, sourceInvoiceID, destinationInvoiceID, scope string) *httptest.ResponseRecorder {
	t.Helper()
	h := TransactionHandler{DB: db, Templates: testPaidInvoiceMutationTemplates(), WorkspaceID: "ws-test", UserID: "user-test"}
	return postInvoiceAssignmentWithHandlerForTest(t, &h, transactionID, sourceInvoiceID, destinationInvoiceID, "manual", scope)
}

func postInvoiceAssignmentModeForTest(t *testing.T, db *sql.DB, transactionID, sourceInvoiceID, destinationInvoiceID, mode, scope string) *httptest.ResponseRecorder {
	t.Helper()
	h := TransactionHandler{DB: db, Templates: testPaidInvoiceMutationTemplates(), WorkspaceID: "ws-test", UserID: "user-test"}
	return postInvoiceAssignmentWithHandlerForTest(t, &h, transactionID, sourceInvoiceID, destinationInvoiceID, mode, scope)
}

func postInvoiceAssignmentWithHandlerForTest(t *testing.T, h *TransactionHandler, transactionID, sourceInvoiceID, destinationInvoiceID, mode, scope string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"valor":                   "250,00",
		"descricao":               "Compra XP 01/08",
		"data":                    "2026-08-01",
		"tipo":                    "despesa",
		"origem_conta_id":         "card-test",
		"categoria_id":            "cat-expense",
		"status_pagamento":        "paid",
		"invoice_id":              destinationInvoiceID,
		"invoice_assignment_mode": mode,
		"fatura_offset":           "manual",
		"return_invoice_id":       sourceInvoiceID,
		"escopo":                  scope,
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+transactionID+"/salvar", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	h.HandleAtualizarTransacao(rr, req, transactionID)
	return rr
}

func invoiceFinancialSnapshotForTest(t *testing.T, db *sql.DB, workspaceID, invoiceID string) invoiceFinancialSnapshot {
	t.Helper()
	snapshot, err := loadInvoiceFinancialSnapshotDB(db, workspaceID, invoiceID)
	if err != nil {
		t.Fatalf("load snapshot for %s: %v", invoiceID, err)
	}
	return snapshot
}

func TestManualInvoiceMoveLegacyPaidAmountBlocksSourceReopen(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedManualInvoiceMoveScenarioForTest(t, db)

	paidAt := time.Now().Unix()
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'PAID', paid_at = ?, paid_amount = 25000
		WHERE id = 'invoice-2026-09'
	`, paidAt)

	sourceSnapshot := invoiceFinancialSnapshotForTest(t, db, "ws-test", "invoice-2026-09")
	if sourceSnapshot.ActivePaymentCount != 0 || sourceSnapshot.ActivePaymentSum != 0 || sourceSnapshot.LegacyPaidAmount != 25000 || sourceSnapshot.EffectivePaid != 25000 {
		t.Fatalf("source snapshot = %+v, want active_count=0 active_sum=0 legacy=25000 effective=25000", sourceSnapshot)
	}
	if sourceSnapshot.Total != 25000 {
		t.Fatalf("source total = %d, want 25000", sourceSnapshot.Total)
	}

	beforeTx := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
	beforeSourceInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-09")
	beforeDestInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-08")
	beforePayments := capturePaymentsForTest(t, db, "ws-test", "invoice-2026-09", "invoice-2026-08")

	rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %q", rr.Code, rr.Body.String())
	}

	afterTx := manualInvoiceMoveSnapshotForTest(t, db, "purchase-test", "invoice-2026-08", "invoice-2026-09")
	afterSourceInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-09")
	afterDestInvoice := captureInvoiceStateForTest(t, db, "ws-test", "invoice-2026-08")
	afterPayments := capturePaymentsForTest(t, db, "ws-test", "invoice-2026-09", "invoice-2026-08")

	if afterTx != beforeTx {
		t.Fatalf("transaction changed after blocked legacy-paid move: before=%+v after=%+v", beforeTx, afterTx)
	}
	invoiceStatesEqual(t, "source (invoice-2026-09)", beforeSourceInvoice, afterSourceInvoice)
	invoiceStatesEqual(t, "destination (invoice-2026-08)", beforeDestInvoice, afterDestInvoice)
	paymentSnapshotsEqual(t, "combined payments", beforePayments, afterPayments)

}

func TestInvoiceEffectivePaidPrefersActivePayments(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedManualInvoiceMoveScenarioForTest(t, db)

	insertInvoiceExpenseForManualMoveTest(t, db, "source-extra", "invoice-2026-09", 10000)
	insertActiveInvoicePaymentForManualMoveTest(t, db, "source-active", "invoice-2026-09", 10000)
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, type, amount, date,
			description, status, installment_number, total_installments, created_at, updated_at
		) VALUES ('payment-reverted', 'ws-test', 'user-test', 'checking-test', 'EXPENSE', 5000, ?,
			?, 'paid', 1, 1, ?, ?)
	`, time.Now().Unix(), invoicePaymentDescription("Cartao Teste"), time.Now().Unix(), time.Now().Unix())
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, transaction_id,
			amount_cents, paid_at, reversed_at, source, created_by, created_at
		) VALUES ('ip-reverted', 'ws-test', 'invoice-2026-09', 'checking-test', 'payment-reverted', 5000, ?, ?, 'manual', 'user-test', ?)
	`, time.Now().Unix(), time.Now().Unix(), time.Now().Unix())
	execTestSQL(t, db, `
		UPDATE invoices
		SET status = 'CLOSED', closing_date = ?, paid_amount = 30000, paid_at = ?
		WHERE id = 'invoice-2026-09'
	`, testUnixDate("2026-07-20"), time.Now().Unix())

	reverted := capturePaymentRowForTest(t, db, "ip-reverted")
	if !reverted.ReversedAt.Valid || reverted.ReversedAt.Int64 == 0 {
		t.Fatalf("ip-reversed deve ter reversed_at preenchido, got=%v", reverted.ReversedAt)
	}
	if reverted.AmountCents != 5000 {
		t.Fatalf("ip-reversed amount = %d, want 5000", reverted.AmountCents)
	}

	active := capturePaymentRowForTest(t, db, "ip-source-active")
	if active.ReversedAt.Valid {
		t.Fatalf("ip-source-active deve ter reversed_at nulo, got=%v", active.ReversedAt)
	}
	if active.AmountCents != 10000 {
		t.Fatalf("ip-source-active amount = %d, want 10000", active.AmountCents)
	}

	sourceSnapshot := invoiceFinancialSnapshotForTest(t, db, "ws-test", "invoice-2026-09")
	if sourceSnapshot.ActivePaymentCount != 1 || sourceSnapshot.ActivePaymentSum != 10000 || sourceSnapshot.LegacyPaidAmount != 30000 {
		t.Fatalf("snapshot = %+v, want count=1 sum=10000 legacy=30000", sourceSnapshot)
	}
	if sourceSnapshot.EffectivePaid != 10000 {
		t.Fatalf("effective_paid = %d, want 10000 (must prefer active sum, not legacy)", sourceSnapshot.EffectivePaid)
	}

	rr := postManualInvoiceMoveForTest(t, db, "purchase-test", "invoice-2026-09", "invoice-2026-08", "single")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %q", rr.Code, rr.Body.String())
	}

	var invoiceID string
	if err := db.QueryRow(`SELECT invoice_id FROM transactions WHERE id = 'purchase-test'`).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if invoiceID != "invoice-2026-08" {
		t.Fatalf("invoice_id = %q, want invoice-2026-08", invoiceID)
	}

	newSourceTotal := sourceSnapshot.Total - 25000
	if newSourceTotal != sourceSnapshot.EffectivePaid {
		t.Fatalf("new_source_total %d != effective_paid %d; legacy %d would have blocked", newSourceTotal, sourceSnapshot.EffectivePaid, sourceSnapshot.LegacyPaidAmount)
	}
}

func manualInvoiceTemplateForTest(t *testing.T) *template.Template {
	t.Helper()
	return template.Must(template.New("form-test").Funcs(template.FuncMap{
		"appVersion": func() string { return "test" },
		"assetPath":  func(path string) string { return path + "?v=test" },
		"intRange": func(start, end int) []int {
			var values []int
			for value := start; value <= end; value++ {
				values = append(values, value)
			}
			return values
		},
	}).ParseFiles(
		resolveTemplatePath(t, "templates/components/form_lancamento.html"),
		resolveTemplatePath(t, "templates/components/financial-emblem.html"),
	))
}

func quickInvoiceMoveRequestWithStateForTest(transactionID, invoiceID, destinationReference string, updatedAt int64, moveScope string) *http.Request {
	form := url.Values{}
	form.Set("expected_invoice_id", invoiceID)
	form.Set("expected_destination_reference", destinationReference)
	form.Set("expected_updated_at", fmt.Sprintf("%d", updatedAt))
	form.Set("expected_move_scope", moveScope)
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+transactionID+"/mover-fatura", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
