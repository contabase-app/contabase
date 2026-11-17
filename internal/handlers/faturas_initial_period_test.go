package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func seedPeriodDebt(t *testing.T, db *sql.DB, card string, period time.Time, status string, total, paid int64) string {
	t.Helper()
	id := card + "-" + period.Format("2006-01")
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, paid_amount, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, card, period.Format("2006-01"), period.AddDate(0, 0, 1).Unix(), period.AddDate(0, 0, 9).Unix(), status, paid, period.Unix())
	if total > 0 {
		var ws string
		if err := db.QueryRow(`SELECT workspace_id FROM accounts WHERE id = ?`, card).Scan(&ws); err != nil {
			t.Fatal(err)
		}
		seedExpenseOnInvoice(t, db, ws, card, id, total, 1, 1, "expense-"+id)
	}
	return id
}

func TestFaturasInitialPeriodEffectiveDebt(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	current := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	type invoice struct {
		card        string
		offset      int
		status      string
		total, paid int64
	}
	cases := []struct {
		name     string
		invoices []invoice
		want     int
	}{
		{"all pending", []invoice{{"a", 0, "OPEN", 1000, 0}, {"b", 0, "CLOSED", 2000, 0}}, 0},
		{"some paid", []invoice{{"a", 0, "PAID", 1000, 1000}, {"b", 0, "CLOSED", 2000, 0}}, 0},
		{"closed pending", []invoice{{"a", 0, "CLOSED", 1000, 0}, {"a", 1, "OPEN", 1000, 0}}, 0},
		{"partial", []invoice{{"a", 0, "CLOSED", 1000, 400}}, 0},
		{"fully paid", []invoice{{"a", 0, "PAID", 1000, 1000}}, 1},
		{"empty current is not paid but has no debt", []invoice{{"a", 0, "OPEN", 0, 0}}, 1},
		{"none", nil, 0},
		{"next exists", []invoice{{"a", 1, "OPEN", 1000, 0}}, 1},
		{"distant open never skips months", []invoice{{"a", 0, "PAID", 1000, 1000}, {"a", 4, "OPEN", 1000, 0}}, 1},
		{"distant only stays current", []invoice{{"a", 4, "OPEN", 1000, 0}}, 0},
		{"oldest overdue when current settled", []invoice{{"a", 0, "PAID", 1000, 1000}, {"a", -1, "CLOSED", 1000, 0}, {"b", -2, "CLOSED", 1000, 300}}, -2},
		{"current debt has priority over overdue", []invoice{{"a", 0, "CLOSED", 1000, 0}, {"b", -2, "CLOSED", 1000, 0}}, 0},
		{"old paid is ignored", []invoice{{"a", -1, "PAID", 1000, 1000}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedCreditCardWithLimit(t, db, "ws-a", "a", "cc-a", 100000)
			seedCreditCardWithLimit(t, db, "ws-b", "foreign", "cc-foreign", 100000)
			execTestSQL(t, db, `INSERT INTO accounts (id,workspace_id,name,type,created_at,updated_at) VALUES ('b','ws-a','B','CREDIT_CARD',1,1)`)
			execTestSQL(t, db, `INSERT INTO credit_cards (id,account_id,closing_day,due_day,credit_limit) VALUES ('cc-b','b',2,10,100000)`)
			seedPeriodDebt(t, db, "foreign", current.AddDate(0, -3, 0), "CLOSED", 990000, 0)
			for _, inv := range tc.invoices {
				seedPeriodDebt(t, db, inv.card, current.AddDate(0, inv.offset, 0), inv.status, inv.total, inv.paid)
			}
			month, year, err := initialFaturasIndexPeriod(db, "ws-a", now)
			want := current.AddDate(0, tc.want, 0)
			if err != nil || month != int(want.Month()) || year != want.Year() {
				t.Fatalf("period = %02d/%d, err %v; want %s", month, year, err, want.Format("2006-01"))
			}
		})
	}
}

func TestFaturasInitialPeriodYearBoundaryAndTimezone(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "a", "cc-a", 100000)
	december := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	seedPeriodDebt(t, db, "a", december, "CLOSED", 1000, 0)
	// UTC has crossed into January; the application's month is still December.
	now := time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC)
	month, year, err := initialFaturasIndexPeriod(db, "ws", now)
	if err != nil || month != 12 || year != 2026 {
		t.Fatalf("local December = %d/%d, %v", month, year, err)
	}
	execTestSQL(t, db, `UPDATE invoices SET status='PAID', paid_amount=1000`)
	month, year, err = initialFaturasIndexPeriod(db, "ws", now)
	if err != nil || month != 1 || year != 2027 {
		t.Fatalf("next January = %d/%d, %v", month, year, err)
	}
}

func TestFaturasInitialPeriodExplicitAndOverdueNotice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "a", "cc-a", 100000)
	current := appMonthStartUTC(time.Now())
	seedPeriodDebt(t, db, "a", current, "CLOSED", 1000, 0)
	seedPeriodDebt(t, db, "a", current.AddDate(0, -2, 0), "CLOSED", 1000, 0)
	h := FaturasHandler{DB: db, WorkspaceID: "ws"}
	for _, explicit := range []bool{false, true} {
		path := "/faturas"
		want := current
		if explicit {
			want = current.AddDate(0, 1, 0)
			path = faturasIndexURL("a", int(want.Month()), want.Year(), "asc")
		}
		data, err := h.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if data.PeriodLabel != faturasIndexPeriodLabel(want) {
			t.Fatal("lost requested period")
		}
		if data.OverduePeriodURL == "" {
			t.Fatal("older debt was hidden")
		}
		if data.Cards[0].Reference != want.Format("2006-01") {
			t.Fatal("rail and selected period disagree")
		}
	}
}

func TestFaturasInitialPeriodModernLineageOverridesLegacyPaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws", "a", "cc-a", 100000)
	current := appMonthStartUTC(time.Now())
	id := seedPeriodDebt(t, db, "a", current, "PAID", 1000, 1000)
	seedInvoicePaymentFull(t, db, "ws", id, "a", 400)
	month, year, err := initialFaturasIndexPeriod(db, "ws", time.Now())
	if err != nil || fmt.Sprintf("%04d-%02d", year, month) != current.Format("2006-01") {
		t.Fatalf("partial modern lineage ignored: %d/%d %v", month, year, err)
	}
}

func TestFaturasInitialPeriodWithoutCards(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	h := FaturasHandler{DB: db, WorkspaceID: "none"}
	data, err := h.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, "/faturas", nil))
	if err != nil || len(data.Cards) != 0 || data.PeriodLabel != faturasIndexPeriodLabel(appMonthStartUTC(time.Now())) {
		t.Fatalf("empty view: %+v %v", data, err)
	}
}
