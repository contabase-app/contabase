package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

func exactRefundInsert(t *testing.T, db *sql.DB, id string, amount int64) {
	t.Helper()
	execTestSQL(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES(?,'ws-test','purchase-test',?,'Refund','user-test')`, id, amount)
}

func TestTransactionRefundExactReadMatrix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gross    int64
		active   []int64
		reversed int64
		want     int64
		invalid  bool
	}{
		{"no-refund", 100, nil, 0, 100, false},
		{"partial", 100, []int64{20}, 0, 80, false},
		{"multiple", 100, []int64{20, 30}, 0, 50, false},
		{"full", 100, []int64{60, 40}, 0, 0, false},
		{"reversed", 100, []int64{20}, 70, 80, false},
		{"E1", math.MaxInt64, nil, 0, math.MaxInt64, false},
		{"E2", math.MaxInt64, []int64{math.MaxInt64}, 0, 0, false},
		{"E3", math.MaxInt64, []int64{math.MaxInt64 - 1, 1}, 0, 0, false},
		{"E4", math.MaxInt64, []int64{math.MaxInt64, 1}, 0, 0, true},
		{"E5", math.MaxInt64, []int64{1}, math.MaxInt64, math.MaxInt64 - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			execTestSQL(t, db, `UPDATE transactions SET amount=? WHERE workspace_id='ws-test' AND id='purchase-test'`, tc.gross)
			if tc.reversed > 0 {
				exactRefundInsert(t, db, "reversed", tc.reversed)
				execTestSQL(t, db, `UPDATE transaction_refunds SET reversed_at=1,reversed_by='user-test' WHERE workspace_id='ws-test' AND transaction_id='purchase-test' AND id='reversed'`)
			}
			if tc.invalid {
				execTestSQL(t, db, `DROP TRIGGER trg_transaction_refunds_validate_insert`)
			}
			for n, amount := range tc.active {
				exactRefundInsert(t, db, fmt.Sprintf("active-%d", n), amount)
			}
			for _, variant := range []struct{ alias, expr string }{{"t", transactionNetAmountTSQL}, {"tx", transactionNetAmountTxSQL}} {
				var got int64
				err := db.QueryRow(`SELECT `+variant.expr+` FROM transactions `+variant.alias+` WHERE `+variant.alias+`.workspace_id=? AND `+variant.alias+`.id=?`, "ws-test", "purchase-test").Scan(&got)
				if tc.invalid {
					exactRefundWantInvariant(t, err)
				} else if err != nil || got != tc.want {
					t.Fatalf("%s net=%d/%v, want %d", variant.alias, got, err, tc.want)
				}
			}
			snapshot, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
			if tc.invalid {
				exactRefundWantInvariant(t, err)
			} else if err != nil || snapshot.Total != tc.want {
				t.Fatalf("invoice net=%d/%v, want %d", snapshot.Total, err, tc.want)
			}
		})
	}
}

func exactRefundWantInvariant(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), database.ErrTransactionRefundInvariant.Error()) || strings.Contains(err.Error(), "integer overflow") {
		t.Fatalf("expected refund invariant error, got %v", err)
	}
}

func TestTransactionRefundExactWorkspaceAndQueryPlan(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	exactRefundInsert(t, db, "normal", 5000)
	seedCreditCardWithLimit(t, db, "foreign", "foreign-card", "foreign-cc", 0)
	seedInvoiceForCard(t, db, "foreign", "foreign-card", "foreign-invoice", "2026-08", "OPEN", 2026, 8, 20)
	seedExpenseOnInvoice(t, db, "foreign", "foreign-card", "foreign-invoice", math.MaxInt64, 1, 1, "foreign-purchase")
	execTestSQL(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('foreign','foreign','foreign-purchase',?,'Refund','user-foreign')`, int64(math.MaxInt64))
	// A malformed relationship with a globally known transaction ID must still
	// be excluded by the explicit workspace predicate, never by PK uniqueness.
	execTestSQL(t, db, `DROP TRIGGER trg_transaction_refunds_validate_insert`)
	execTestSQL(t, db, `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('foreign-link','foreign','purchase-test',?,'Refund','user-foreign')`, int64(math.MaxInt64))
	var got int64
	if err := db.QueryRow(`SELECT ` + transactionNetAmountTSQL + ` FROM transactions t WHERE t.workspace_id='ws-test' AND t.id='purchase-test'`).Scan(&got); err != nil || got != 20000 {
		t.Fatalf("cross-tenant net=%d/%v", got, err)
	}
	legacy := `t.amount - COALESCE((SELECT SUM(r.amount_cents) FROM transaction_refunds r WHERE r.workspace_id=t.workspace_id AND r.transaction_id=t.id AND r.reversed_at IS NULL),0)`
	plans := make([][]string, 0, 2)
	for _, expr := range []string{legacy, transactionNetAmountTSQL} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT `+expr+` FROM transactions t WHERE t.workspace_id=? ORDER BY t.date,t.id LIMIT 10`, "ws-test")
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	if strings.Join(plans[0], "\n") != strings.Join(plans[1], "\n") {
		t.Fatalf("refund access plan changed: %v -> %v", plans[0], plans[1])
	}
}

func exactRefundSeedCallerFixture(t *testing.T, db *sql.DB, now time.Time) time.Time {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	due := AppCivilDateUTC(now).AddDate(0, 0, 5)
	date := MustCivilWriteDate(AppCivilDateUTC(now).Format("2006-01-02"))
	execTestSQL(t, db, `UPDATE invoices SET status='CLOSED',closing_date=?,due_date=? WHERE id='invoice-2026-08'`, due.Unix(), due.Unix())
	execTestSQL(t, db, `INSERT INTO categories(id,workspace_id,name,type,macro_group) VALUES('exact-category','ws-test','Exact','EXPENSE','Essencial')`)
	// date is civil competence; paid_at remains the actual payment instant.
	execTestSQL(t, db, `UPDATE transactions SET category_id='exact-category',date=?,paid_at=? WHERE workspace_id='ws-test' AND id='purchase-test'`, date.LegacyUnix(), now.Unix())
	execTestSQL(t, db, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES('exact-limit','ws-test','exact-category',10000)`)
	exactRefundInsert(t, db, "one", 5000)
	exactRefundInsert(t, db, "two", 2500)
	exactRefundInsert(t, db, "reversed", 1000)
	execTestSQL(t, db, `UPDATE transaction_refunds SET reversed_at=1,reversed_by='user-test' WHERE workspace_id='ws-test' AND id='reversed'`)
	return due
}

func TestTransactionRefundExactFixtureMonthBoundary(t *testing.T) {
	// Use the application's existing timezone setting, without changing TZ or
	// the process clock. Both UTC instants belong to September 30 in Sao Paulo.
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	before := time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC)
	after := time.Date(2026, 10, 1, 1, 30, 0, 0, time.UTC)
	for _, now := range []time.Time{before, after, after.In(appTimeLocation())} {
		t.Run(now.Format(time.RFC3339), func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			exactRefundSeedCallerFixture(t, db, now)
			var date, paidAt int64
			if err := db.QueryRow(`SELECT date,paid_at FROM transactions WHERE workspace_id='ws-test' AND id='purchase-test'`).Scan(&date, &paidAt); err != nil {
				t.Fatal(err)
			}
			if date != MustCivilWriteDate("2026-09-30").LegacyUnix() || paidAt != now.Unix() {
				t.Fatalf("date=%d paid_at=%d, want civil September 30 and instant %d", date, paidAt, now.Unix())
			}
			for _, month := range []int{9, 10} {
				start := time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
				var spent int64
				err := db.QueryRow(`SELECT COALESCE(SUM(`+transactionNetAmountTSQL+`),0) FROM transactions t WHERE t.workspace_id=? AND t.category_id=? AND t.date>=? AND t.date<?`, "ws-test", "exact-category", start.Unix(), start.AddDate(0, 1, 0).Unix()).Scan(&spent)
				var want int64
				if month == 9 {
					want = 17500
				}
				if err != nil || spent != want {
					t.Fatalf("month=%d spent=%d/%v, want %d", month, spent, err, want)
				}
			}
		})
	}
}

func TestTransactionRefundExactCallerFamilies(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now()
	due := exactRefundSeedCallerFixture(t, db, now)
	const want int64 = 17500
	t.Run("invoice-state-db-and-tx", func(t *testing.T) {
		s, err := loadInvoiceFinancialSnapshotDB(db, "ws-test", "invoice-2026-08")
		if err != nil || s.Total != want {
			t.Fatalf("snapshot %d/%v", s.Total, err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		s, err = loadInvoiceFinancialSnapshotTx(tx, "ws-test", "invoice-2026-08")
		if err != nil || s.Total != want {
			t.Fatalf("tx snapshot %d/%v", s.Total, err)
		}
		if got, err := sumInvoiceTotalTx(tx, "ws-test", "invoice-2026-08"); err != nil || got != want {
			t.Fatalf("tx total %d/%v", got, err)
		}
	})
	t.Run("invoice-and-refund-form", func(t *testing.T) {
		data, err := buildFaturaDataForInvoice(db, "ws-test", "invoice-2026-08", "asc")
		if err != nil || data.Total.rawCents() != want || len(data.Transactions) != 1 || data.Transactions[0].RefundedAmount != 7500 {
			t.Fatalf("invoice read: total=%d rows=%d err=%v", data.Total.rawCents(), len(data.Transactions), err)
		}
		target, err := loadTransactionRefundTarget(db, "ws-test", "purchase-test")
		if err != nil || target.Amount-target.Refunded != want {
			t.Fatalf("refund target %v/%v", target, err)
		}
		if got, err := sumInvoiceTotal(db, "ws-test", "invoice-2026-08"); err != nil || got != want {
			t.Fatalf("invoice total %d/%v", got, err)
		}
		if got, err := sumCardOutstandingLimit(db, "ws-test", "card-test"); err != nil || got != want {
			t.Fatalf("card limit %d/%v", got, err)
		}
	})
	t.Run("contas-selection", func(t *testing.T) {
		rows, err := queryPendingItemsFiltered(db, "ws-test", "EXPENSE", false, int(due.Month()), due.Year(), "")
		if err != nil || len(rows) != 1 || rows[0].Amount != want {
			t.Fatalf("pending rows %v/%v", rows, err)
		}
	})
	t.Run("metas", func(t *testing.T) {
		data, err := (&MetasHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-test", UserID: "user-test"}).buildMetasData("", "limites")
		if err != nil || len(data.Limites) != 1 || data.Limites[0].Spent.rawCents() != want {
			t.Fatalf("limits=%v err=%v", data.Limites, err)
		}
		t.Logf("Metas Spent=%d", data.Limites[0].Spent.rawCents())
	})
	t.Run("notifications-order-having-limit", func(t *testing.T) {
		h := NotificacoesHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
		items := h.closedInvoices()
		if len(items) != 1 || items[0].Money.rawCents() != want {
			t.Fatalf("closed notifications %v", items)
		}
		t.Logf("Closed invoice Money=%d", items[0].Money.rawCents())
		items = h.exceededCostLimits(false)
		if len(items) != 1 || items[0].Money.rawCents() != 7500 {
			t.Fatalf("limit notifications %v", items)
		}
		t.Logf("Exceeded limit Money=%d", items[0].Money.rawCents())
		t.Run("multiple-categories", func(t *testing.T) {
			// Keep extra candidates in a separate DB so other callers retain
			// their single-category fixture. Ten exceed, one equals the limit.
			db := openTestDB(t)
			defer db.Close()
			exactRefundSeedCallerFixture(t, db, now)
			date := MustCivilWriteDate(AppCivilDateUTC(now).Format("2006-01-02"))
			for n := 0; n < 10; n++ {
				id := fmt.Sprintf("order-%d", n)
				execTestSQL(t, db, `INSERT INTO categories(id,workspace_id,name,type) VALUES(?,'ws-test',?,'EXPENSE')`, id, id)
				execTestSQL(t, db, `INSERT INTO cost_limits(id,workspace_id,category_id,max_amount_monthly) VALUES(?,'ws-test',?,10000)`, id, id)
				execTestSQL(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,category_id,type,amount,date,paid_at,description,status) VALUES(?,'ws-test','user-test','checking-test',?,'EXPENSE',?,?,?,'Order fixture','paid')`, id, id, 10000+n*1000, date.LegacyUnix(), now.Unix())
			}
			items := (&NotificacoesHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}).exceededCostLimits(false)
			wantIDs := []string{"order-9", "order-8", "exact-limit", "order-7", "order-6", "order-5", "order-4", "order-3"}
			wantMoney := []int64{9000, 8000, 7500, 7000, 6000, 5000, 4000, 3000}
			if len(items) != 8 {
				t.Fatalf("LIMIT returned %d items, want 8", len(items))
			}
			for n, item := range items {
				if item.Key != "limit:"+wantIDs[n] || item.Money.rawCents() != wantMoney[n] {
					t.Fatalf("ORDER/HAVING item %d: key=%s money=%d", n, item.Key, item.Money.rawCents())
				}
			}
			// With truncation removed by keeping only the threshold category,
			// HAVING must still exclude equality rather than emit a zero item.
			execTestSQL(t, db, `DELETE FROM cost_limits WHERE workspace_id='ws-test' AND id<>'order-0'`)
			if items := (&NotificacoesHandler{DB: db, WorkspaceID: "ws-test"}).exceededCostLimits(false); len(items) != 0 {
				t.Fatalf("HAVING admitted threshold equality: %v", items)
			}
		})
	})
	t.Run("reports-and-pdf", func(t *testing.T) {
		h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
		a, b, err := h.queryMacroGroupTotals(context.Background(), 0, math.MaxInt64)
		if err != nil || a+b != want {
			t.Fatalf("report %d/%d/%v", a, b, err)
		}
		rows, err := queryDREPDFCategorySummary(db, "ws-test", "EXPENSE", 0, math.MaxInt64)
		if err != nil || len(rows) != 1 || rows[0].Amount != want {
			t.Fatalf("PDF categories %v/%v", rows, err)
		}
	})
	t.Run("AGG-09-and-AGG-10", func(t *testing.T) {
		got, show, err := BuildProjectedBalanceUntilPeriodEnd(db, "ws-test", due.AddDate(0, 0, 1).Unix(), now)
		if err != nil || !show || got != 100000-want {
			t.Fatalf("AGG-09 %d/%v/%v", got, show, err)
		}
		t.Logf("AGG-09=%d", got)
		month := time.Date(due.Year(), due.Month(), 1, 0, 0, 0, 0, time.UTC)
		got, err = calcProjectedAccumulatedBalance(db, "ws-test", 100000, month)
		// Paid transactions dated today are already outside the future flow.
		if err != nil || got != 100000 {
			t.Fatalf("AGG-10 paid today %d/%v, want 100000", got, err)
		}
		t.Logf("AGG-10 paid today=%d", got)
		t.Run("pending", func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			due := exactRefundSeedCallerFixture(t, db, now)
			execTestSQL(t, db, `UPDATE transactions SET status='pending',paid_at=NULL WHERE workspace_id='ws-test' AND id='purchase-test'`)
			var net int64
			if err := db.QueryRow(`SELECT ` + transactionNetAmountTSQL + ` FROM transactions t WHERE t.workspace_id='ws-test' AND t.id='purchase-test'`).Scan(&net); err != nil || net != want {
				t.Fatalf("pending refund net %d/%v, want %d", net, err, want)
			}
			month := time.Date(due.Year(), due.Month(), 1, 0, 0, 0, 0, time.UTC)
			got, err := calcProjectedAccumulatedBalance(db, "ws-test", 100000, month)
			if err != nil || got != 100000-want {
				t.Fatalf("AGG-10 pending %d/%v, want %d", got, err, 100000-want)
			}
			t.Logf("AGG-10 pending=%d refund net=%d; reversed refund excluded", got, net)
		})
	})
	t.Run("transactions-consolidated-invoice", func(t *testing.T) {
		data, err := (&TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}).buildLancamentosData("", int(due.Month()), due.Year(), LancamentosFilters{})
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, row := range data.Invoices {
			if row.ID == "invoice-2026-08" {
				found = true
				if row.Total != want {
					t.Fatalf("transaction invoice total %d", row.Total)
				}
			}
		}
		if !found {
			t.Fatal("invoice missing from transactions")
		}
	})
}

func TestTransactionRefundExactInvalidHTTPAndStreaming(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)
	execTestSQL(t, db, `DROP TRIGGER trg_transaction_refunds_validate_insert`)
	exactRefundInsert(t, db, "invalid", 25001)
	_, err := loadTransactionRefundTarget(db, "ws-test", "purchase-test")
	exactRefundWantInvariant(t, err)
	response := httptest.NewRecorder()
	(&TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}).HandleFormReembolso(response, httptest.NewRequest(http.MethodGet, "/transacoes/purchase-test/reembolso", nil), "purchase-test")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "R$") {
		t.Fatalf("invalid refund HTTP %d: %s", response.Code, response.Body.String())
	}
	rows, err := db.Query(`SELECT 1 UNION ALL SELECT ` + transactionNetAmountTSQL + ` FROM transactions t WHERE t.workspace_id='ws-test' AND t.id='purchase-test'`)
	if err != nil {
		exactRefundWantInvariant(t, err)
		return
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("missing first streaming row")
	}
	var value int64
	if err := rows.Scan(&value); err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("invalid net exposed a value")
	}
	exactRefundWantInvariant(t, rows.Err())
}
