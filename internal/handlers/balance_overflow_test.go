package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/services"
)

func TestBalanceOverflowInvoicePaymentRollsBack(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			moveSeedInvoiceScenarioToFuture(t, db)
			execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = 'ws-test'`, kind)
			execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'checking-test'`, int64(math.MinInt64))
			before := balanceMutationSnapshot(t, db)
			h := FaturasHandler{DB: db, WriterDB: db, Templates: testInvoicePaymentTemplates(t), WorkspaceID: "ws-test", UserID: "user-test"}
			rr := httptest.NewRecorder()
			h.HandlePagarFatura(rr, formPost("/cartoes/faturas/pagar", url.Values{
				"invoice_id": {"invoice-2026-08"}, "payment_account_id": {"checking-test"},
				"payment_mode": {"settle"}, "confirm_settle": {"1"},
			}))
			if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "erro ao debitar conta") {
				t.Fatalf("unexpected rejection: %d %s", rr.Code, rr.Body.String())
			}
			if after := balanceMutationSnapshot(t, db); before != after {
				t.Fatal("overflow persisted partial invoice/payment/transaction state")
			}
			assertAllBalancesInteger(t, db)
		})
	}
}

func TestBalanceOverflowInvoiceReversalRollsBack(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		t.Run(kind, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			paidAt := reversalUnix("2026-08-05", 10)
			seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
				InvoiceTotal: 100000, InvoiceStatus: "PAID", InvoicePaidAt: &paidAt, LegacyPaid: 100000,
				Payments: []reversalPaymentSeed{{ID: "ip-a", TxID: "payment-a", Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
			})
			execTestSQL(t, db, `UPDATE workspaces SET type = ? WHERE id = ?`, kind, reversalWorkspaceID)
			execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = ?`, int64(math.MaxInt64), reversalPaymentAccountID)
			before := balanceMutationSnapshot(t, db)
			if _, err := reverseInvoicePayment(db, reversalCommand("ip-a")); !errors.Is(err, services.ErrBalanceOutOfRange) {
				t.Fatalf("expected overflow: %v", err)
			}
			if after := balanceMutationSnapshot(t, db); before != after {
				t.Fatal("overflow persisted reversal CAS/compensation/invoice state")
			}
			assertAllBalancesInteger(t, db)
		})
	}
}

func TestBalanceOverflowScheduledPaymentRollsBackAndStopsBatch(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, typ := range []string{"INCOME", "EXPENSE"} {
			t.Run(kind+"/"+typ, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				row, now := seedStaleScheduledPayment(t, db, kind)
				seedFollowingScheduledPayment(t, db, row)
				balance := int64(math.MaxInt64)
				if typ == "EXPENSE" {
					balance = math.MinInt64
				}
				execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'checking-test'`, balance)
				execTestSQL(t, db, `UPDATE transactions SET type = ? WHERE id = ?`, typ, row.ID)
				before := balanceMutationSnapshot(t, db)
				count, err := RunDueScheduledPayments(context.Background(), db, now)
				if count != 0 || !errors.Is(err, services.ErrBalanceOutOfRange) {
					t.Fatalf("count=%d error=%v", count, err)
				}
				if after := balanceMutationSnapshot(t, db); before != after {
					t.Fatal("overflow persisted scheduled payment or following item")
				}
				assertAllBalancesInteger(t, db)
			})
		}
	}
}

func TestBalanceOverflowTransferHandlerRollsBack(t *testing.T) {
	for _, kind := range []string{"personal", "business"} {
		for _, reverse := range []bool{false, true} {
			name := kind + "/apply"
			if reverse {
				name = kind + "/reverse"
			}
			t.Run(name, func(t *testing.T) {
				db := openTestDB(t)
				defer db.Close()
				row, _ := seedStaleScheduledPayment(t, db, kind)
				status, action, message := "pending", "paid", "erro ao aplicar saldo"
				destBalance := int64(math.MaxInt64)
				if reverse {
					status, action, message, destBalance = "paid", "pending", "erro ao estornar", math.MinInt64
				}
				execTestSQL(t, db, `UPDATE transactions SET type = 'TRANSFER', amount = 1, destination_account_id = 'checking-extra', status = ?, scheduled_payment_at = NULL WHERE id = ?`, status, row.ID)
				execTestSQL(t, db, `UPDATE accounts SET current_balance = ? WHERE id = 'checking-extra'`, destBalance)
				before := balanceMutationSnapshot(t, db)
				h := testPaidInvoiceMutationHandler(db)
				rr := httptest.NewRecorder()
				h.HandleTogglePagamento(rr, formPost("/transacoes/"+row.ID+"/status-pagamento", url.Values{"payment_action": {action}}), row.ID)
				if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), message) {
					t.Fatalf("unexpected rejection: %d %s", rr.Code, rr.Body.String())
				}
				if after := balanceMutationSnapshot(t, db); before != after {
					t.Fatal("second-side overflow persisted source, status or history")
				}
				assertAllBalancesInteger(t, db)
			})
		}
	}
}

// Compare every persisted field in the financial tables touched by these
// operations, including timestamps, invoice reconciliation and reversal CAS.
func balanceMutationSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var snapshot [][][]interface{}
	for _, table := range []string{"accounts", "transactions", "invoices", "invoice_payments", "transaction_status_history", "user_notifications"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var contents [][]interface{}
		for rows.Next() {
			values, pointers := make([]interface{}, len(columns)), make([]interface{}, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			contents = append(contents, values)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, contents)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertAllBalancesInteger(t *testing.T, db *sql.DB) {
	t.Helper()
	var invalid int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE typeof(current_balance) != 'integer'`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("noninteger balances=%d error=%v", invalid, err)
	}
}
