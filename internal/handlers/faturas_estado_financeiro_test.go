package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func parseTestDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse date %q: %v", s, err)
	}
	return d
}

func TestLimitOpenInvoiceNoPaymentConsumesFullTotal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 100000 {
		t.Fatalf("used = %d, want 100000 (full total, no payment)", used)
	}
}

func TestLimitOpenInvoicePartialPaymentReducesUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 30000)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 70000 {
		t.Fatalf("used = %d, want 70000 (1000 - 300 partial = 700 pending)", used)
	}
}

func TestLimitClosedInvoicePartialPaymentReducesUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "CLOSED", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 30000)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 70000 {
		t.Fatalf("used = %d, want 70000 (CLOSED, partial 300 of 1000)", used)
	}
}

func TestLimitPaidInvoiceFullSettlementZeroUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 0 {
		t.Fatalf("used = %d, want 0 (PAID with full payment, pending=0)", used)
	}
}

func TestLimitInstallments10xNoPaymentFullUsed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	parent := "tx-parent"
	for i := int64(1); i <= 10; i++ {
		ref := time.Date(2026, time.Month(7+int(i-1)), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-" + ref
		seedInvoiceForCard(t, db, "ws", "card", invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, "ws", "card", invID, 100000, i, 10, parent)
	}

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 1000000 {
		t.Fatalf("used = %d, want 1000000 (10x100000, no payments)", used)
	}
}

func TestLimitInstallments10xFirstInvoicePaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	parent := "tx-parent"
	for i := int64(1); i <= 10; i++ {
		ref := time.Date(2026, time.Month(7+int(i-1)), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-" + ref
		seedInvoiceForCard(t, db, "ws", "card", invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, "ws", "card", invID, 100000, i, 10, parent)
		if i == 1 {
			seedInvoicePaymentFull(t, db, "ws", invID, "card", 100000)
		}
	}

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 900000 {
		t.Fatalf("used = %d, want 900000 (first invoice PAID, pending=0)", used)
	}
}

func TestLimitReversedPaymentDoesNotReduceLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, amount_cents, paid_at, source, reversed_at, created_at)
		VALUES ('pay-rev', 'ws', 'inv-1', 'card', 50000, ?, 'manual', ?, ?)
	`, now, now, now)

	used, err := sumCardOutstandingLimit(db, "ws", "card")
	if err != nil {
		t.Fatalf("sumCardOutstandingLimit: %v", err)
	}
	if used != 100000 {
		t.Fatalf("used = %d, want 100000 (reversed payment does not reduce pending)", used)
	}
}

func TestLimitUsesLegacyFallbackAndDoesNotResurrectItAfterReversal(t *testing.T) {
	tests := []struct {
		name        string
		withLineage bool
		wantUsed    int64
	}{
		{"legacy partial", false, 70000},
		{"all reversed ignores stale legacy", true, 100000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedCreditCardWithLimit(t, db, "ws-limit-canonical", "card-limit-canonical", "cc-limit-canonical", 1000000)
			seedInvoiceForCard(t, db, "ws-limit-canonical", "card-limit-canonical", "inv-limit-canonical", "2026-07", "OPEN", 2026, 7, 20)
			seedExpenseOnInvoice(t, db, "ws-limit-canonical", "card-limit-canonical", "inv-limit-canonical", 100000, 1, 1, "tx-limit-canonical")
			execTestSQL(t, db, `UPDATE invoices SET paid_amount = 30000 WHERE id = 'inv-limit-canonical'`)
			if tt.withLineage {
				seedInvoicePaymentWithReversalState(t, db, "limit-reversed", "ws-limit-canonical", "inv-limit-canonical", "card-limit-canonical", 30000, true)
			}

			used, err := sumCardOutstandingLimit(db, "ws-limit-canonical", "card-limit-canonical")
			if err != nil {
				t.Fatalf("sumCardOutstandingLimit: %v", err)
			}
			if used != tt.wantUsed {
				t.Fatalf("used = %d, want %d", used, tt.wantUsed)
			}
		})
	}
}

func TestInvoiceEffectivePaidUsesModernPaymentLineage(t *testing.T) {
	tests := []struct {
		name             string
		legacyPaidAmount int64
		activePayments   []int64
		reversedPayments []int64
		wantEffective    int64
	}{
		{
			name:             "legacy zero without modern records",
			legacyPaidAmount: 0,
			wantEffective:    0,
		},
		{
			name:             "legacy partial without modern records",
			legacyPaidAmount: 400,
			wantEffective:    400,
		},
		{
			name:             "legacy fallback without modern records",
			legacyPaidAmount: 1000,
			wantEffective:    1000,
		},
		{
			name:             "active modern payment replaces legacy cache",
			legacyPaidAmount: 1000,
			activePayments:   []int64{400},
			wantEffective:    400,
		},
		{
			name:             "active sum excludes reversed modern payment",
			legacyPaidAmount: 1000,
			activePayments:   []int64{400},
			reversedPayments: []int64{600},
			wantEffective:    400,
		},
		{
			name:             "all modern payments reversed means zero",
			legacyPaidAmount: 1000,
			reversedPayments: []int64{400, 600},
			wantEffective:    0,
		},
		{
			name:             "modern sum is not clamped to legacy value",
			legacyPaidAmount: 1000,
			activePayments:   []int64{1200},
			wantEffective:    1200,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedCreditCardWithLimit(t, db, "ws-lineage", "card-lineage", "cc-lineage", 1000000)
			seedInvoiceForCard(t, db, "ws-lineage", "card-lineage", "invoice-lineage", "2026-07", "PAID", 2026, 7, 20)
			seedExpenseOnInvoice(t, db, "ws-lineage", "card-lineage", "invoice-lineage", 1000, 1, 1, "tx-lineage-total")
			execTestSQL(t, db, `UPDATE invoices SET paid_amount = ? WHERE id = 'invoice-lineage'`, tt.legacyPaidAmount)

			for i, amount := range tt.activePayments {
				seedInvoicePaymentWithReversalState(t, db, "active-"+strconv.Itoa(i), "ws-lineage", "invoice-lineage", "card-lineage", amount, false)
			}
			for i, amount := range tt.reversedPayments {
				seedInvoicePaymentWithReversalState(t, db, "reversed-"+strconv.Itoa(i), "ws-lineage", "invoice-lineage", "card-lineage", amount, true)
			}

			wantTotalRecords := int64(len(tt.activePayments) + len(tt.reversedPayments))
			wantActiveRecords := int64(len(tt.activePayments))
			var wantActiveSum int64
			for _, amount := range tt.activePayments {
				wantActiveSum += amount
			}

			assertInvoiceEffectivePaidSnapshot(t, "DB", func() (invoiceFinancialSnapshot, error) {
				return loadInvoiceFinancialSnapshotDB(db, "ws-lineage", "invoice-lineage")
			}, wantTotalRecords, wantActiveRecords, wantActiveSum, tt.legacyPaidAmount, tt.wantEffective)

			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin snapshot transaction: %v", err)
			}
			assertInvoiceEffectivePaidSnapshot(t, "Tx", func() (invoiceFinancialSnapshot, error) {
				return loadInvoiceFinancialSnapshotTx(tx, "ws-lineage", "invoice-lineage")
			}, wantTotalRecords, wantActiveRecords, wantActiveSum, tt.legacyPaidAmount, tt.wantEffective)
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback read-only snapshot transaction: %v", err)
			}
		})
	}
}

func TestInvoiceEffectivePaidMultiplePaymentsAndReversalSequence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-sequence", "card-sequence", "cc-sequence", 1000000)
	seedInvoiceForCard(t, db, "ws-sequence", "card-sequence", "invoice-sequence", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-sequence", "card-sequence", "invoice-sequence", 1000, 1, 1, "tx-sequence")
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 1000 WHERE id = 'invoice-sequence'`)
	seedInvoicePaymentWithReversalState(t, db, "sequence-40", "ws-sequence", "invoice-sequence", "card-sequence", 400, false)
	seedInvoicePaymentWithReversalState(t, db, "sequence-60", "ws-sequence", "invoice-sequence", "card-sequence", 600, false)

	assertEffective := func(want int64) {
		t.Helper()
		snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-sequence", "invoice-sequence")
		if err != nil {
			t.Fatalf("load snapshot: %v", err)
		}
		if snapshot.EffectivePaid != want || CalculateInvoicePendingAmount(snapshot.Total, snapshot.EffectivePaid) != 1000-want {
			t.Fatalf("effective/remaining = %d/%d, want %d/%d", snapshot.EffectivePaid, CalculateInvoicePendingAmount(snapshot.Total, snapshot.EffectivePaid), want, 1000-want)
		}
	}
	assertEffective(1000)

	execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = ? WHERE id = 'sequence-60'`, time.Now().Unix())
	assertEffective(400)

	seedInvoicePaymentWithReversalState(t, db, "sequence-60-replacement", "ws-sequence", "invoice-sequence", "card-sequence", 600, false)
	assertEffective(1000)
}

func TestInvoiceEffectivePaidDoesNotResurrectLegacyAfterAllModernPaymentsReversed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-p1", "card-p1", "cc-p1", 1000000)
	seedInvoiceForCard(t, db, "ws-p1", "card-p1", "invoice-p1", "2026-07", "PAID", 2026, 7, 20)
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 750 WHERE id = 'invoice-p1'`)
	seedInvoicePaymentWithReversalState(t, db, "payment-p1", "ws-p1", "invoice-p1", "card-p1", 750, true)

	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-p1", "invoice-p1")
	if err != nil {
		t.Fatalf("load invoice financial snapshot: %v", err)
	}
	if snapshot.TotalPaymentRecordCount != 1 || snapshot.ActivePaymentCount != 0 || snapshot.ActivePaymentSum != 0 {
		t.Fatalf("payment provenance = total:%d active:%d sum:%d, want total:1 active:0 sum:0", snapshot.TotalPaymentRecordCount, snapshot.ActivePaymentCount, snapshot.ActivePaymentSum)
	}
	if snapshot.LegacyPaidAmount != 750 || snapshot.EffectivePaid != 0 {
		t.Fatalf("legacy/effective paid = %d/%d, want 750/0", snapshot.LegacyPaidAmount, snapshot.EffectivePaid)
	}
}

func TestInvoiceEffectivePaidPaymentProvenanceIsWorkspaceIsolated(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 1000000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 1000000)
	seedInvoiceForCard(t, db, "ws-a", "card-a", "invoice-a", "2026-07", "PAID", 2026, 7, 20)
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 1000 WHERE id = 'invoice-a'`)
	seedInvoicePaymentWithReversalState(t, db, "payment-a", "ws-a", "invoice-a", "card-a", 100, false)

	// The legacy schema permits each FK to be valid independently. This malformed
	// cross-workspace association proves the read primitive still scopes provenance.
	seedInvoicePaymentWithReversalState(t, db, "payment-cross", "ws-b", "invoice-a", "card-b", 900, false)

	snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-a", "invoice-a")
	if err != nil {
		t.Fatalf("load workspace-isolated snapshot: %v", err)
	}
	if snapshot.TotalPaymentRecordCount != 1 || snapshot.ActivePaymentCount != 1 || snapshot.ActivePaymentSum != 100 || snapshot.EffectivePaid != 100 {
		t.Fatalf("workspace-isolated snapshot = total:%d active:%d sum:%d effective:%d, want 1/1/100/100", snapshot.TotalPaymentRecordCount, snapshot.ActivePaymentCount, snapshot.ActivePaymentSum, snapshot.EffectivePaid)
	}
}

func TestQueryInvoiceOptionsForAccountCanonicalEffectivePaidGolden(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-options", "card-options", "cc-options", 100000)
	seedCreditCardWithLimit(t, db, "ws-options-other", "card-options-other", "cc-options-other", 100000)

	type invoiceSeed struct {
		id, reference, status string
		total                 int64
	}
	seeds := []invoiceSeed{
		{"opt-legacy-open", "2026-01", "OPEN", 1000},
		{"opt-legacy-closed", "2026-02", "CLOSED", 1000},
		{"opt-legacy-full", "2026-03", "PAID", 1000},
		{"opt-modern-partial", "2026-04", "OPEN", 1000},
		{"opt-all-reversed", "2026-05", "CLOSED", 1000},
		{"opt-overpaid", "2026-06", "OPEN", 1000},
		{"opt-zero", "2026-07", "OPEN", 0},
	}
	for i, seed := range seeds {
		seedInvoiceForCard(t, db, "ws-options", "card-options", seed.id, seed.reference, seed.status, 2026, i+1, 20)
		if seed.total > 0 {
			seedExpenseOnInvoice(t, db, "ws-options", "card-options", seed.id, seed.total, 1, 1, "tx-"+seed.id)
		}
	}
	seedInvoiceForCard(t, db, "ws-options-other", "card-options-other", "opt-foreign", "2026-01", "OPEN", 2026, 1, 20)
	seedExpenseOnInvoice(t, db, "ws-options-other", "card-options-other", "opt-foreign", 9000, 1, 1, "tx-opt-foreign")

	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 400 WHERE id IN ('opt-legacy-open', 'opt-legacy-closed')`)
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 1000 WHERE id IN ('opt-legacy-full', 'opt-modern-partial', 'opt-all-reversed', 'opt-overpaid')`)
	seedInvoicePaymentWithReversalState(t, db, "opt-pay-modern", "ws-options", "opt-modern-partial", "card-options", 400, false)
	seedInvoicePaymentWithReversalState(t, db, "opt-pay-reversed", "ws-options", "opt-all-reversed", "card-options", 1000, true)
	seedInvoicePaymentWithReversalState(t, db, "opt-pay-over", "ws-options", "opt-overpaid", "card-options", 1200, false)
	// A foreign payment must neither count nor activate modern lineage for the
	// legacy invoice in ws-options.
	seedInvoicePaymentWithReversalState(t, db, "opt-pay-foreign", "ws-options-other", "opt-legacy-open", "card-options-other", 900, false)

	options := queryInvoiceOptionsForAccount(db, "ws-options", "card-options")
	if len(options) != len(seeds) {
		t.Fatalf("options count = %d, want %d: %#v", len(options), len(seeds), options)
	}

	wantIDs := []string{"opt-legacy-open", "opt-legacy-closed", "opt-legacy-full", "opt-modern-partial", "opt-all-reversed", "opt-overpaid", "opt-zero"}
	wantEligible := []bool{true, true, false, true, true, false, true}
	wantPending := []string{"R$ 6,00", "R$ 6,00", "R$ 0,00", "R$ 6,00", "R$ 10,00", "R$ 0,00", "R$ 0,00"}
	wantFinancial := []string{"Parcial", "Parcial", "Paga", "Parcial", "Pendente", "Paga", "Vazia"}
	for i, option := range options {
		if option.ID != wantIDs[i] || option.Reference != seeds[i].reference || option.Status != seeds[i].status {
			t.Fatalf("option[%d] identity = %q/%q/%q, want %q/%q/%q", i, option.ID, option.Reference, option.Status, wantIDs[i], seeds[i].reference, seeds[i].status)
		}
		if option.ManualEligible != wantEligible[i] {
			t.Fatalf("option[%d] eligibility = %v, want %v", i, option.ManualEligible, wantEligible[i])
		}
		if option.PendingDisplay != wantPending[i] || option.FinLabel != wantFinancial[i] {
			t.Fatalf("option[%d] pending/financial = %q/%q, want %q/%q", i, option.PendingDisplay, option.FinLabel, wantPending[i], wantFinancial[i])
		}
	}

	if foreign := queryInvoiceOptionsForAccount(db, "ws-options", "card-options-other"); len(foreign) != 0 {
		t.Fatalf("foreign workspace options = %#v, want none", foreign)
	}
}

func TestQueryInvoiceOptionsForAccountPreservesAscendingLimit24(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-options-limit", "card-options-limit", "cc-options-limit", 100000)

	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 26; i++ {
		month := base.AddDate(0, i, 0)
		ref := month.Format("2006-01")
		seedInvoiceForCard(t, db, "ws-options-limit", "card-options-limit", "opt-limit-"+ref, ref, "OPEN", month.Year(), int(month.Month()), 20)
	}

	options := queryInvoiceOptionsForAccount(db, "ws-options-limit", "card-options-limit")
	if len(options) != 24 {
		t.Fatalf("options count = %d, want LIMIT 24", len(options))
	}
	if options[0].Reference != "2027-01" || options[23].Reference != "2028-12" {
		t.Fatalf("ascending limited references = %q..%q, want 2027-01..2028-12", options[0].Reference, options[23].Reference)
	}
	for i := 0; i < len(options)-1; i++ {
		if options[i].Reference >= options[i+1].Reference {
			t.Fatalf("options not strictly ascending at %d: %q >= %q", i, options[i].Reference, options[i+1].Reference)
		}
	}
}

func seedInvoicePaymentWithReversalState(t *testing.T, db *sql.DB, paymentID, workspaceID, invoiceID, accountID string, amount int64, reversed bool) {
	t.Helper()
	now := time.Now().Unix()
	var reversedAt any
	if reversed {
		reversedAt = now
	}
	if _, err := db.Exec(`
		INSERT INTO invoice_payments (
			id, workspace_id, invoice_id, account_id, amount_cents,
			paid_at, source, reversed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'manual', ?, ?)
	`, paymentID, workspaceID, invoiceID, accountID, amount, now, reversedAt, now); err != nil {
		t.Fatalf("seed invoice payment %s: %v", paymentID, err)
	}
}

func assertInvoiceEffectivePaidSnapshot(
	t *testing.T,
	source string,
	load func() (invoiceFinancialSnapshot, error),
	wantTotalRecords, wantActiveRecords, wantActiveSum, wantLegacyPaid, wantEffective int64,
) {
	t.Helper()
	snapshot, err := load()
	if err != nil {
		t.Fatalf("load %s invoice financial snapshot: %v", source, err)
	}
	if snapshot.TotalPaymentRecordCount != wantTotalRecords || snapshot.ActivePaymentCount != wantActiveRecords || snapshot.ActivePaymentSum != wantActiveSum {
		t.Fatalf("%s payment provenance = total:%d active:%d sum:%d, want %d/%d/%d", source, snapshot.TotalPaymentRecordCount, snapshot.ActivePaymentCount, snapshot.ActivePaymentSum, wantTotalRecords, wantActiveRecords, wantActiveSum)
	}
	if snapshot.Total != 1000 {
		t.Fatalf("%s invoice total = %d, want 1000", source, snapshot.Total)
	}
	if snapshot.LegacyPaidAmount != wantLegacyPaid || snapshot.EffectivePaid != wantEffective {
		t.Fatalf("%s legacy/effective paid = %d/%d, want %d/%d", source, snapshot.LegacyPaidAmount, snapshot.EffectivePaid, wantLegacyPaid, wantEffective)
	}
}

func TestLimitWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 1000000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 1000000)
	seedInvoiceForCard(t, db, "ws-a", "card-a", "inv-a", "2026-07", "OPEN", 2026, 7, 20)
	seedInvoiceForCard(t, db, "ws-b", "card-b", "inv-b", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-a", "card-a", "inv-a", 100000, 1, 1, "tx-a")
	seedExpenseOnInvoice(t, db, "ws-b", "card-b", "inv-b", 200000, 1, 1, "tx-b")

	usedA, _ := sumCardOutstandingLimit(db, "ws-a", "card-a")
	usedB, _ := sumCardOutstandingLimit(db, "ws-b", "card-b")
	if usedA != 100000 {
		t.Fatalf("ws-a used = %d, want 100000 (isolation)", usedA)
	}
	if usedB != 200000 {
		t.Fatalf("ws-b used = %d, want 200000 (isolation)", usedB)
	}
}

func TestLimitCardIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-ci", "card-a", "cc-a", 1000000)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-b', 'ws-ci', 'Card B', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cc-b', 'card-b', 20, 10, 1000000)
	`)
	seedInvoiceForCard(t, db, "ws-ci", "card-a", "inv-a", "2026-07", "OPEN", 2026, 7, 20)
	seedInvoiceForCard(t, db, "ws-ci", "card-b", "inv-b", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws-ci", "card-a", "inv-a", 100000, 1, 1, "tx-a")
	seedExpenseOnInvoice(t, db, "ws-ci", "card-b", "inv-b", 200000, 1, 1, "tx-b")

	usedA, _ := sumCardOutstandingLimit(db, "ws-ci", "card-a")
	usedB, _ := sumCardOutstandingLimit(db, "ws-ci", "card-b")
	if usedA != 100000 {
		t.Fatalf("card-a used = %d, want 100000 (card isolation)", usedA)
	}
	if usedB != 200000 {
		t.Fatalf("card-b used = %d, want 200000 (card isolation)", usedB)
	}
}

func TestBadgeCicloFechaEmXDias(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	closingUnix := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Unix()
	label := computeCycleBadge(closingUnix, now)
	if label != "Fecha em 3 dias" {
		t.Fatalf("label = %q, want %q", label, "Fecha em 3 dias")
	}
}

func TestBadgeCicloFechada(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	closingUnix := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC).Unix()
	label := computeCycleBadge(closingUnix, now)
	if label != "Fechada" {
		t.Fatalf("label = %q, want %q", label, "Fechada")
	}
}

func TestBadgeCicloAberta(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	closingUnix := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()
	label := computeCycleBadge(closingUnix, now)
	if label != "Aberta" {
		t.Fatalf("label = %q, want %q", label, "Aberta")
	}
}

func TestBadgeFinanceiraVazia(t *testing.T) {
	label := computeFinancialBadge(0, 0)
	if label != "Vazia" {
		t.Fatalf("label = %q, want %q", label, "Vazia")
	}
}

func TestBadgeFinanceiraPendente(t *testing.T) {
	label := computeFinancialBadge(5000, 0)
	if label != "Pendente" {
		t.Fatalf("label = %q, want %q", label, "Pendente")
	}
}

func TestBadgeFinanceiraParcial(t *testing.T) {
	label := computeFinancialBadge(7000, 3000)
	if label != "Parcial" {
		t.Fatalf("label = %q, want %q", label, "Parcial")
	}
}

func TestBadgeFinanceiraPaga(t *testing.T) {
	label := computeFinancialBadge(0, 5000)
	if label != "Paga" {
		t.Fatalf("label = %q, want %q", label, "Paga")
	}
}

func TestBadgeFinanceiraPagaSobrepaid(t *testing.T) {
	label := computeFinancialBadge(-500, 1000)
	if label != "Paga" {
		t.Fatalf("label = %q, want %q", label, "Paga")
	}
}

func TestReconcileInvoiceStateBlocksImplicitPaidReopen(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-07", "OPEN", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)
	execTestSQL(t, db, `UPDATE invoices SET status = 'PAID', paid_at = ? WHERE id = 'inv-1'`, time.Now().Unix())
	execTestSQL(t, db, `UPDATE transactions SET amount = 150000 WHERE id = 'tx-1'`)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", time.Now().Unix()); !errors.Is(err, errInvoiceImplicitReopenBlocked) {
		t.Fatalf("reconcile error = %v, want implicit reopen conflict", err)
	}
	_ = tx.Rollback()

	var status string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, "inv-1").Scan(&status); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("invoice status = %q, want PAID preserved", status)
	}
}

func TestReconcileInvoiceStateCompositionSettlementUsesTransitionAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		paymentAt    = int64(1700000000)
		transitionAt = int64(1800000000)
	)
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2030-07", "OPEN", 2030, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 80000)
	execTestSQL(t, db, `UPDATE invoice_payments SET paid_at = ?, created_at = ? WHERE invoice_id = 'inv-1'`, paymentAt, paymentAt)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE transactions SET amount = 80000 WHERE id = 'tx-1'`); err != nil {
		t.Fatalf("composition mutation: %v", err)
	}
	if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", transitionAt); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, "inv-1").Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "PAID" {
		t.Fatalf("status = %q, want PAID", status)
	}
	if paidAmount != 80000 || !paidAt.Valid || paidAt.Int64 != transitionAt || paidAt.Int64 == paymentAt {
		t.Fatalf("paid fields = %d/%v, want 80000/%d distinct from payment %d", paidAmount, paidAt, transitionAt, paymentAt)
	}
}

func TestReconcileInvoiceStateCompositionFailClosedMatrix(t *testing.T) {
	setup := func(t *testing.T, status string, total, paid int64) *sql.DB {
		t.Helper()
		db := openTestDB(t)
		seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
		seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2030-07", status, 2030, 7, 20)
		if total > 0 {
			seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", total, 1, 1, "tx-1")
		}
		if paid > 0 {
			seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", paid)
		}
		return db
	}

	t.Run("partial reduction remains open", func(t *testing.T) {
		db := setup(t, "OPEN", 100000, 80000)
		defer db.Close()
		tx, _ := db.Begin()
		defer tx.Rollback()
		_, _ = tx.Exec(`UPDATE transactions SET amount = 90000 WHERE id = 'tx-1'`)
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var status string
		var paidAt sql.NullInt64
		if err := db.QueryRow(`SELECT status, paid_at FROM invoices WHERE id = 'inv-1'`).Scan(&status, &paidAt); err != nil {
			t.Fatal(err)
		}
		if status != "OPEN" || paidAt.Valid {
			t.Fatalf("state = %q/%v, want OPEN/NULL", status, paidAt)
		}
	})

	t.Run("reduction below paid rolls back", func(t *testing.T) {
		db := setup(t, "OPEN", 100000, 80000)
		defer db.Close()
		tx, _ := db.Begin()
		_, _ = tx.Exec(`UPDATE transactions SET amount = 70000 WHERE id = 'tx-1'`)
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); !errors.Is(err, errInvoiceWouldOverpay) {
			t.Fatalf("error = %v, want overpay", err)
		}
		_ = tx.Rollback()
		var amount int64
		if err := db.QueryRow(`SELECT amount FROM transactions WHERE id = 'tx-1'`).Scan(&amount); err != nil {
			t.Fatal(err)
		}
		if amount != 100000 {
			t.Fatalf("amount = %d, want rollback to 100000", amount)
		}
	})

	t.Run("closed never reopens", func(t *testing.T) {
		db := setup(t, "CLOSED", 100000, 80000)
		defer db.Close()
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'inv-1'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "CLOSED" {
			t.Fatalf("status = %q, want CLOSED", status)
		}
	})

	t.Run("zero total does not auto pay", func(t *testing.T) {
		db := setup(t, "OPEN", 0, 0)
		defer db.Close()
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'inv-1'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "PAID" {
			t.Fatal("zero-total invoice became PAID")
		}
	})

	t.Run("explicit paid zero total stays paid", func(t *testing.T) {
		db := setup(t, "PAID", 0, 0)
		defer db.Close()
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'inv-1'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "PAID" {
			t.Fatalf("status = %q, want PAID", status)
		}
	})

	t.Run("unknown status is structural", func(t *testing.T) {
		db := setup(t, "OPEN", 100000, 0)
		defer db.Close()
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = ON`)
		execTestSQL(t, db, `UPDATE invoices SET status = 'BROKEN' WHERE id = 'inv-1'`)
		execTestSQL(t, db, `PRAGMA ignore_check_constraints = OFF`)
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); !errors.Is(err, errInvoiceStateStructural) {
			t.Fatalf("error = %v, want structural", err)
		}
	})

	t.Run("coherent paid preserves paid at and syncs modern amount", func(t *testing.T) {
		db := setup(t, "PAID", 100000, 100000)
		defer db.Close()
		const paidAt = int64(1700000000)
		execTestSQL(t, db, `UPDATE invoices SET paid_amount = 1, paid_at = ? WHERE id = 'inv-1'`, paidAt)
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var amount int64
		var gotPaidAt sql.NullInt64
		if err := db.QueryRow(`SELECT paid_amount, paid_at FROM invoices WHERE id = 'inv-1'`).Scan(&amount, &gotPaidAt); err != nil {
			t.Fatal(err)
		}
		if amount != 100000 || !gotPaidAt.Valid || gotPaidAt.Int64 != paidAt {
			t.Fatalf("paid fields = %d/%v", amount, gotPaidAt)
		}
	})

	t.Run("all reversed modern lineage does not resurrect legacy", func(t *testing.T) {
		db := setup(t, "OPEN", 100000, 100000)
		defer db.Close()
		execTestSQL(t, db, `UPDATE invoice_payments SET reversed_at = 1750000000 WHERE invoice_id = 'inv-1'`)
		execTestSQL(t, db, `UPDATE invoices SET paid_amount = 100000 WHERE id = 'inv-1'`)
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "inv-1", 1800000000); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var status string
		var amount int64
		if err := db.QueryRow(`SELECT status, paid_amount FROM invoices WHERE id = 'inv-1'`).Scan(&status, &amount); err != nil {
			t.Fatal(err)
		}
		if status == "PAID" || amount != 0 {
			t.Fatalf("state = %q/%d, want non-PAID with modern active sum zero", status, amount)
		}
	})

	t.Run("missing invoice is structural", func(t *testing.T) {
		db := setup(t, "OPEN", 100000, 0)
		defer db.Close()
		tx, _ := db.Begin()
		defer tx.Rollback()
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, "ws", "missing", 1800000000); !errors.Is(err, errInvoiceStateStructural) {
			t.Fatalf("error = %v, want structural", err)
		}
	})
}

func TestDashboardAndInvoiceShowSameLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const (
		wsID   = "ws-consist"
		cardID = "card-consist"
		ccID   = "cc-consist"
	)
	seedCreditCardWithLimit(t, db, wsID, cardID, ccID, 1000000)
	parentID := "tx-consist-parent"
	for i := int64(1); i <= 10; i++ {
		m := time.Month(7 + int(i-1))
		ref := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		invID := "inv-consist-" + ref
		seedInvoiceForCard(t, db, wsID, cardID, invID, ref, "OPEN", 2026, 7+int(i-1), 20)
		seedExpenseOnInvoice(t, db, wsID, cardID, invID, 100000, i, 10, parentID)
	}

	invData, err := buildFaturaDataForInvoice(db, wsID, "inv-consist-2026-07", "desc")
	if err != nil {
		t.Fatalf("build fatura data: %v", err)
	}
	dashData := BuildDashboardData(db, "user-"+wsID, wsID)

	if len(dashData.Cards) != 1 {
		t.Fatalf("dashboard cards = %d, want 1", len(dashData.Cards))
	}
	dashCard := dashData.Cards[0]

	invAvailable := invData.LimitAvailable
	dashAvailable := dashCard.LimitMoney

	if invAvailable.Reais != dashAvailable.Reais || invAvailable.Cents != dashAvailable.Cents {
		t.Fatalf("invoice %s%s != dashboard %s%s", invAvailable.Reais, invAvailable.Cents, dashAvailable.Reais, dashAvailable.Cents)
	}
}

func TestNoSchemaChangeNoPartialColumn(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	var hasPartial bool
	err := db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('invoices') WHERE name = 'partial'`).Scan(&hasPartial)
	if err != nil {
		t.Fatalf("pragma query: %v", err)
	}
	if hasPartial {
		t.Fatalf("invoices.partial column should NOT exist (no schema change)")
	}
	var hasPartialStatus bool
	err = db.QueryRow(`SELECT COUNT(1) FROM pragma_table_info('invoices') WHERE name = 'partial_status'`).Scan(&hasPartialStatus)
	if err != nil {
		t.Fatalf("pragma query: %v", err)
	}
	if hasPartialStatus {
		t.Fatalf("invoices.partial_status column should NOT exist (no schema change)")
	}
}

func TestAutoRoutingDoesNotRouteNewCycleToPaidInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	now := time.Now()
	pastClose := now.AddDate(0, -2, 0).Unix()
	pastDue := now.AddDate(0, -1, 15).Unix()
	futureClose := now.AddDate(0, 0, 15).Unix()
	futureDue := now.AddDate(0, 1, 5).Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES
			('inv-old', 'card', '2026-04', ?, ?, 'PAID', ?),
			('inv-current', 'card', '2026-07', ?, ?, 'OPEN', ?)
	`, pastClose, pastDue, now.Unix(), futureClose, futureDue, now.Unix())

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	txDate := now.Unix()
	invID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID == "inv-old" {
		t.Fatalf("auto-routing should not route to PAID invoice of old cycle")
	}
	// Auto-routing may find the existing OPEN invoice or create a new one;
	// the key assertion is that it does NOT route to the PAID invoice.
}

func TestInvoiceHistoryGuardTargetSet(t *testing.T) {
	setup := func(t *testing.T) (*sql.DB, TransactionHandler) {
		t.Helper()
		db := openTestDB(t)
		t.Cleanup(func() { _ = db.Close() })
		seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
		seedInvoiceForCard(t, db, "ws", "card", "inv-open", "2026-07", "OPEN", 2026, 7, 20)
		seedInvoiceForCard(t, db, "ws", "card", "inv-closed", "2026-08", "CLOSED", 2026, 8, 20)
		seedInvoiceForCard(t, db, "ws", "card", "inv-paid", "2026-09", "PAID", 2026, 9, 20)
		seedExpenseOnInvoice(t, db, "ws", "card", "inv-open", 10000, 1, 1, "tx-open-1")
		seedExpenseOnInvoice(t, db, "ws", "card", "inv-open", 20000, 1, 1, "tx-open-2")
		seedExpenseOnInvoice(t, db, "ws", "card", "inv-closed", 30000, 1, 1, "tx-closed")
		seedExpenseOnInvoice(t, db, "ws", "card", "inv-paid", 40000, 1, 1, "tx-paid")
		seedCreditCardWithLimit(t, db, "ws-foreign", "card-foreign", "cc-foreign", 1000000)
		seedInvoiceForCard(t, db, "ws-foreign", "card-foreign", "inv-foreign", "2026-07", "OPEN", 2026, 7, 20)
		seedExpenseOnInvoice(t, db, "ws-foreign", "card-foreign", "inv-foreign", 50000, 1, 1, "tx-foreign")
		return db, TransactionHandler{DB: db, WorkspaceID: "ws", UserID: "user-ws"}
	}

	for _, tc := range []struct {
		name    string
		ids     []string
		wantErr error
	}{
		{name: "two local open allowed", ids: []string{"tx-open-1", "tx-open-2"}},
		{name: "open and closed are mutable", ids: []string{"tx-open-1", "tx-closed"}},
		{name: "open and paid block whole set", ids: []string{"tx-open-1", "tx-paid"}, wantErr: errPaidInvoiceMutationBlocked},
		{name: "open and missing fail closed", ids: []string{"tx-open-1", "tx-missing"}, wantErr: errTransactionMutationTargetInvalid},
		{name: "open and foreign fail closed", ids: []string{"tx-open-1", "tx-foreign"}, wantErr: errTransactionMutationTargetInvalid},
		{name: "duplicate is deduplicated", ids: []string{"tx-open-1", " tx-open-1 ", "tx-open-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, h := setup(t)
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer tx.Rollback()
			err = h.ensureTransactionsMutableByInvoiceStatusTx(tx, tc.ids)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("guard error = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("guard error = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("null invoice is allowed", func(t *testing.T) {
		db, h := setup(t)
		execTestSQL(t, db, `UPDATE transactions SET invoice_id = NULL WHERE id = 'tx-open-1'`)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-open-1"}); err != nil {
			t.Fatalf("guard error = %v, want nil", err)
		}
	})

	t.Run("dangling invoice link fails closed", func(t *testing.T) {
		db, h := setup(t)
		execTestSQL(t, db, `PRAGMA foreign_keys = OFF`)
		execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'inv-missing' WHERE id = 'tx-open-1'`)
		execTestSQL(t, db, `PRAGMA foreign_keys = ON`)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-open-1"}); !errors.Is(err, errTransactionInvoiceLinkInvalid) {
			t.Fatalf("guard error = %v, want structural link failure", err)
		}
	})

	t.Run("cross workspace invoice link fails closed", func(t *testing.T) {
		db, h := setup(t)
		execTestSQL(t, db, `UPDATE transactions SET invoice_id = 'inv-foreign' WHERE id = 'tx-open-1'`)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{"tx-open-1"}); !errors.Is(err, errTransactionInvoiceLinkInvalid) {
			t.Fatalf("guard error = %v, want structural link failure", err)
		}
	})
}

func TestPaidEarlyReceivesNewTransactionSameCycle(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, ref, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID != "inv-1" || ref != "2026-08" {
		t.Fatalf("expected inv-1/2026-08, got %s/%s", invID, ref)
	}
}

func TestPaidEarlyAfterClosingGoesToNextInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)

	txDate := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, ref, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID == "inv-1" || ref == "2026-08" {
		t.Fatalf("should not route to old PAID invoice, got %s/%s", invID, ref)
	}
}

func TestPaidEarlyDoesNotCreateDuplicateInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if invID != "inv-1" {
		t.Fatalf("should reuse existing PAID invoice inv-1, got %s", invID)
	}

	var count int
	if err := tx.QueryRow(`SELECT COUNT(1) FROM invoices WHERE account_id = 'card' AND reference = '2026-08'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 invoice for 2026-08, got %d", count)
	}
}

func TestPaidEarlySameCycleWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 1000000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 1000000)
	seedInvoiceForCard(t, db, "ws-a", "card-a", "inv-a", "2026-08", "PAID", 2026, 7, 20)
	seedInvoiceForCard(t, db, "ws-b", "card-b", "inv-b", "2026-08", "PAID", 2026, 7, 20)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invA, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws-a", "card-a", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve ws-a: %v", err)
	}
	if invA != "inv-a" {
		t.Fatalf("ws-a should route to inv-a, got %s", invA)
	}

	invB, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws-b", "card-b", txDate, "auto")
	if err != nil {
		t.Fatalf("resolve ws-b: %v", err)
	}
	if invB != "inv-b" {
		t.Fatalf("ws-b should route to inv-b, got %s", invB)
	}
}

func TestFaturaOffsetNextIgnoresPaidCurrentInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)

	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	invID, ref, _, _, _, err := resolveCardTransactionInvoiceTx(tx, "ws", "card", txDate, "next")
	if err != nil {
		t.Fatalf("resolve next: %v", err)
	}
	if invID == "inv-1" || ref == "2026-08" {
		t.Fatalf("fatura_offset=next should not route to current PAID invoice, got %s/%s", invID, ref)
	}
	if ref != "2026-09" {
		t.Fatalf("expected next reference 2026-09, got %s", ref)
	}
}

func TestPaidEarlySelectedByRoutingIsBlockedByStateMachine(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const originalPaidAt = int64(1700000000)
	seedCreditCardWithLimit(t, db, "ws", "card", "cc", 1000000)
	seedInvoiceForCard(t, db, "ws", "card", "inv-1", "2026-08", "PAID", 2026, 7, 20)
	seedExpenseOnInvoice(t, db, "ws", "card", "inv-1", 100000, 1, 1, "tx-1")
	seedInvoicePaymentFull(t, db, "ws", "inv-1", "card", 100000)
	execTestSQL(t, db, `UPDATE invoices SET paid_amount = 100000, paid_at = ? WHERE id = 'inv-1'`, originalPaidAt)

	var balanceBefore int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'card'`).Scan(&balanceBefore); err != nil {
		t.Fatal(err)
	}
	var reserveEventsBefore int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger`).Scan(&reserveEventsBefore); err != nil {
		t.Fatal(err)
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws", UserID: "user-ws"}
	txDate := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC).Unix()
	_, err := handler.insertTransaction(
		"EXPENSE", 50000, "Nova Compra Apos Quitacao", "", "", civilWriteDateFromLegacyUnix(txDate), "card", "", "", 1, "paid", false, "", "", OptionalCivilWriteDate{}, nil, "", false,
	)
	if !errors.Is(err, errAutomaticInvoiceFrozen) || !errors.Is(err, errInvoiceStateTransitionConflict) {
		t.Fatalf("insertTransaction error = %v, want automatic frozen invoice conflict", err)
	}

	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = 'inv-1'`).Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "PAID" || paidAmount != 100000 || !paidAt.Valid || paidAt.Int64 != originalPaidAt {
		t.Fatalf("invoice changed after rollback: %q/%d/%v", status, paidAmount, paidAt)
	}
	var inserted int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Nova Compra Apos Quitacao'`).Scan(&inserted); err != nil {
		t.Fatal(err)
	}
	if inserted != 0 {
		t.Fatalf("inserted transactions = %d, want 0", inserted)
	}
	var balanceAfter int64
	var reserveEventsAfter int
	_ = db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'card'`).Scan(&balanceAfter)
	_ = db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger`).Scan(&reserveEventsAfter)
	if balanceAfter != balanceBefore || reserveEventsAfter != reserveEventsBefore {
		t.Fatalf("side effects changed: balance %d->%d reserve events %d->%d", balanceBefore, balanceAfter, reserveEventsBefore, reserveEventsAfter)
	}
}

func TestHandleResolverDestinoFaturaIncludeOptionsNoHang(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-test', 'User Test', 'user-test@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-test', 'Workspace Test', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-test', 'user-test', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('card-test', 'ws-test', 'Cartao Teste', 'CREDIT_CARD', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('credit-card-test', 'card-test', 20, 10, 500000)
	`)

	refs := []struct {
		ref       string
		closeDate string
		dueDate   string
		status    string
	}{
		{"2026-07", "2026-07-20", "2026-08-10", "CLOSED"},
		{"2026-08", "2026-08-20", "2026-09-10", "OPEN"},
		{"2026-09", "2026-09-20", "2026-10-10", "PAID"},
	}
	for _, r := range refs {
		closeUnix := parseTestDate(t, r.closeDate).Unix()
		dueUnix := parseTestDate(t, r.dueDate).Unix()
		if r.status == "OPEN" {
			closeUnix, dueUnix = futureOpenInvoiceDatesForTest()
		}
		execTestSQL(t, db, `
			INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, 'card-test', ?, ?, ?, ?, ?)
		`, "invoice-"+r.ref, r.ref, closeUnix, dueUnix, r.status, now)
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-test', 'ws-test', 'user-test', 'card-test', 'invoice-2026-08', 'EXPENSE', 25000, ?, 'Compra Teste', 'paid', 1, 1, ?, ?)
	`, parseTestDate(t, "2026-07-10").Unix(), now, now)

	handler := FaturasHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	req := httptest.NewRequest("GET", "/cartoes/fatura-destino/card-test?data=2026-07-10&fatura_offset=auto&include_options=true", nil)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.HandleResolverDestinoFatura(rr, req, "card-test")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("HandleResolverDestinoFatura with include_options timed out (hang)")
	}

	if rr.Code != 200 {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var payload struct {
		Reference string          `json:"reference"`
		Options   []InvoiceOption `json:"options"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Options) != 3 {
		t.Fatalf("options count = %d, want 3", len(payload.Options))
	}
	for i := 0; i < len(payload.Options)-1; i++ {
		if payload.Options[i].Reference >= payload.Options[i+1].Reference {
			t.Fatalf("options not sorted ascending: %v", payload.Options)
		}
	}

	var sensitiveCount int
	for _, opt := range payload.Options {
		if opt.IsSensitive {
			sensitiveCount++
		}
	}
	if sensitiveCount != 2 {
		t.Fatalf("sensitive options = %d, want 2 (CLOSED + PAID)", sensitiveCount)
	}

	_ = time.After(1 * time.Millisecond)
}
