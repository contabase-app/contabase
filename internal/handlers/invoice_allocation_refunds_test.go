package handlers

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInvoiceAllocationUsesNetRefundWeights(t *testing.T) {
	for _, workspaceType := range []string{"personal", "business"} {
		for _, tc := range []struct {
			name                             string
			refund, general, payment         int64
			reversed                         bool
			paidA, paidB, pendingA, pendingB int64
		}{
			{name: "full", refund: 10000, payment: 5000, paidB: 5000, pendingB: 15000},
			{name: "partial", refund: 5000, payment: 5000, paidA: 1000, paidB: 4000, pendingA: 4000, pendingB: 16000},
			{name: "general over net", refund: 5000, general: 5000, payment: 5000, paidA: 1000, paidB: 4000, pendingA: 3000, pendingB: 12000},
			{name: "reversed refunds", refund: 5000, general: 5000, reversed: true, payment: 5000, paidA: 1667, paidB: 3333, pendingA: 8333, pendingB: 16667},
			{name: "residual cent", payment: 1, paidB: 1, pendingA: 10000, pendingB: 19999},
		} {
			t.Run(workspaceType+"/"+tc.name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				seedCaixaRealizadoScenario(t, db)
				execTestSQL(t, db, `UPDATE workspaces SET type=? WHERE id='ws-caixa'`, workspaceType)
				date := testUnixDate("2026-08-22")
				execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
					VALUES ('inv-net', 'card-caixa', '2026-08', ?, ?, 'CLOSED', ?)`, date-86400, date+86400, date)
				insertCaixaTransaction(t, db, "net-a", "EXPENSE", "card-caixa", "cat-food-caixa", 10000, date-86400, 0, "paid")
				insertCaixaTransaction(t, db, "net-b", "EXPENSE", "card-caixa", "cat-card-caixa", 20000, date-86400, 0, "paid")
				execTestSQL(t, db, `UPDATE transactions SET invoice_id='inv-net' WHERE id IN ('net-a','net-b')`)
				if tc.refund > 0 {
					execTestSQL(t, db, `INSERT INTO transaction_refunds (id, workspace_id, transaction_id, amount_cents, description, created_by)
						VALUES ('net-refund', 'ws-caixa', 'net-a', ?, 'Refund', 'u-caixa')`, tc.refund)
				}
				if tc.general > 0 {
					execTestSQL(t, db, `INSERT INTO invoice_refunds (id, workspace_id, invoice_id, amount_cents, description, created_by)
						VALUES ('net-general', 'ws-caixa', 'inv-net', ?, 'General refund', 'u-caixa')`, tc.general)
				}
				if tc.reversed {
					execTestSQL(t, db, `UPDATE transaction_refunds SET reversed_at=?, reversed_by='u-caixa' WHERE id='net-refund'`, date)
					execTestSQL(t, db, `UPDATE invoice_refunds SET reversed_at=?, reversed_by='u-caixa' WHERE id='net-general'`, date)
				}
				insertCaixaTransaction(t, db, "net-payment", "EXPENSE", "chk-caixa", "cat-food-caixa", tc.payment, date, date, "paid")
				execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
					VALUES ('net-payment', 'ws-caixa', 'inv-net', 'chk-caixa', 'net-payment', ?, ?, 'manual', 'u-caixa', ?)`, tc.payment, date, date)
				h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa", UserID: "u-caixa"}
				start, end := testUnixDate("2026-08-01"), testUnixDate("2026-09-01")-1
				for _, mode := range []struct {
					name string
					a, b int64
				}{{"paid", tc.paidA, tc.paidB}, {"pending", tc.pendingA, tc.pendingB}} {
					rows, err := h.queryInvoiceItemAllocations(context.Background(), start, end, mode.name)
					if err != nil {
						t.Fatal(err)
					}
					got := map[string]int64{}
					for _, row := range rows {
						got[row.CategoryID] += row.Amount
					}
					if got["cat-food-caixa"] != mode.a || got["cat-card-caixa"] != mode.b {
						t.Fatalf("%s allocation = %v, want A=%d B=%d", mode.name, got, mode.a, mode.b)
					}
				}
				// Report totals remain actual cash paid plus the outstanding invoice.
				data, err := h.buildRelatoriosDataWithModo("", "caixa", "movimentos", "previsao", 8, 2026)
				if err != nil {
					t.Fatal(err)
				}
				if data.RealizedExpenseTotal.rawCents() != tc.payment || data.CashMonthExpenseTotal.rawCents() != tc.payment+tc.pendingA+tc.pendingB {
					t.Fatalf("cash totals changed: paid=%d projected=%d", data.RealizedExpenseTotal.rawCents(), data.CashMonthExpenseTotal.rawCents())
				}
				if tc.name == "general over net" && workspaceType == "business" {
					_, categories, err := h.queryCaixaPDFCategorySummaries(8, 2026, "previsao")
					if err != nil {
						t.Fatal(err)
					}
					amounts := map[string]int64{}
					for _, category := range categories {
						amounts[category.Name] = category.Amount
					}
					if amounts["Alimentacao"] != 4000 || amounts["Compras Cartao"] != 16000 {
						t.Fatalf("PDF categories = %v, want 4000/16000", amounts)
					}
					rr := httptest.NewRecorder()
					h.HandleExportarCaixaPDF(rr, httptest.NewRequest(http.MethodGet, "/relatorios/caixa/pdf?mes=8&ano=2026&modo=previsao", nil))
					if rr.Code != http.StatusOK {
						t.Fatalf("PDF status=%d: %s", rr.Code, rr.Body.String())
					}
					assertPDFBodyContainsText(t, rr.Body.String(), "200,00")
				}
				foreign := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "other-workspace"}
				items, total, err := foreign.queryInvoiceAllocationItems(context.Background(), "inv-net")
				if err != nil || len(items) != 0 || total != 0 {
					t.Fatalf("foreign invoice lookup = %v/%d/%v", items, total, err)
				}
			})
		}
	}
}

func TestAllocateInvoiceItemSharesPreservesLargeAmounts(t *testing.T) {
	items := []invoiceAllocationItem{
		{TransactionID: "a", Amount: math.MaxInt64 / 2},
		{TransactionID: "b", Amount: math.MaxInt64/2 + 1},
	}
	shares := allocateInvoiceItemShares(items, math.MaxInt64, math.MaxInt64)
	if len(shares) != 2 || shares[0].Amount != items[0].Amount || shares[1].Amount != items[1].Amount {
		t.Fatalf("large allocation = %v", shares)
	}
	shares = allocateInvoiceItemShares(items, math.MaxInt64, 1)
	if len(shares) != 2 || shares[0].Amount != 0 || shares[1].Amount != 1 {
		t.Fatalf("large residual allocation = %v", shares)
	}
}

func TestInvoiceAllocationRejectsOverflowingTotal(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixaRealizadoScenario(t, db)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('inv-overflow', 'card-caixa', '2026-08', 1, 2, 'OPEN', 1)`)
	insertCaixaTransaction(t, db, "huge-a", "EXPENSE", "card-caixa", "cat-food-caixa", math.MaxInt64, 1, 0, "paid")
	insertCaixaTransaction(t, db, "huge-b", "EXPENSE", "card-caixa", "cat-card-caixa", 1, 1, 0, "paid")
	execTestSQL(t, db, `UPDATE transactions SET invoice_id='inv-overflow' WHERE id IN ('huge-a','huge-b')`)
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-caixa"}
	items, total, err := h.queryInvoiceAllocationItems(context.Background(), "inv-overflow")
	if err == nil || items != nil || total != 0 {
		t.Fatalf("overflow must fail without partial allocation: %v/%d/%v", items, total, err)
	}
}
