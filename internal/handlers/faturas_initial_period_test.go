package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

func seedInitialPeriodInvoice(t *testing.T, db *sql.DB, cardID string, period time.Time, status string) {
	t.Helper()
	reference := period.Format("2006-01")
	// Keep the fixture ahead of the real clock so the existing page-entry
	// auto-close process does not change the status being tested.
	closing := time.Now().AddDate(1, 0, 0).Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, cardID+"-"+reference, cardID, reference, closing, closing+86400, status, time.Now().Unix())
}

func TestFaturasInitialPeriodPageSelection(t *testing.T) {
	current := appMonthStartUTC(time.Now())
	next := current.AddDate(0, 1, 0)
	later := current.AddDate(0, 2, 0)
	cases := []struct {
		name     string
		invoices []struct {
			card   string
			period time.Time
			status string
		}
		request string
		want    time.Time
	}{
		{
			name: "current open stays current",
			invoices: []struct {
				card   string
				period time.Time
				status string
			}{
				{"card-a", current, models.InvoiceStatusOpen},
				{"card-a", next, models.InvoiceStatusOpen},
			},
			request: "/faturas", want: current,
		},
		{
			name: "paid current selects next open",
			invoices: []struct {
				card   string
				period time.Time
				status string
			}{
				{"card-a", current, models.InvoiceStatusPaid},
				{"card-a", next, models.InvoiceStatusOpen},
			},
			request: "/faturas", want: next,
		},
		{
			name: "skips paid and closed intermediate months",
			invoices: []struct {
				card   string
				period time.Time
				status string
			}{
				{"card-a", current, models.InvoiceStatusPaid},
				{"card-a", next, models.InvoiceStatusClosed},
				{"card-a", later, models.InvoiceStatusOpen},
			},
			request: "/faturas", want: later,
		},
		{
			name: "no future open falls back to current",
			invoices: []struct {
				card   string
				period time.Time
				status string
			}{
				{"card-a", current, models.InvoiceStatusPaid},
				{"card-a", next, models.InvoiceStatusClosed},
			},
			request: "/faturas", want: current,
		},
		{
			name: "explicit period wins and survives refresh",
			invoices: []struct {
				card   string
				period time.Time
				status string
			}{
				{"card-a", next, models.InvoiceStatusOpen},
			},
			request: fmt.Sprintf("/faturas?mes=%d&ano=%d", current.Month(), current.Year()), want: current,
		},
		{
			name: "one open invoice among multiple cards selects month",
			invoices: []struct {
				card   string
				period time.Time
				status string
			}{
				{"card-a", current, models.InvoiceStatusPaid},
				{"card-b", current, models.InvoiceStatusPaid},
				{"card-a", next, models.InvoiceStatusPaid},
				{"card-b", next, models.InvoiceStatusOpen},
			},
			request: "/faturas", want: next,
		},
		{name: "no invoices", request: "/faturas", want: current},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedCreditCardWithLimit(t, db, "ws-initial", "card-a", "cc-a", 100000)
			// A second card shares the same workspace without duplicating the user.
			now := time.Now().Unix()
			execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
				VALUES ('card-b', 'ws-initial', 'Card B', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
			execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
				VALUES ('cc-b', 'card-b', 20, 10, 100000)`)
			for _, invoice := range tc.invoices {
				seedInitialPeriodInvoice(t, db, invoice.card, invoice.period, invoice.status)
			}
			h := FaturasHandler{DB: db, WorkspaceID: "ws-initial"}
			data, err := h.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, tc.request, nil))
			if err != nil {
				t.Fatal(err)
			}
			if got, want := data.PeriodLabel, faturasIndexPeriodLabel(tc.want); got != want {
				t.Fatalf("page period = %q, want %q", got, want)
			}
			if got := data.Cards[0].Reference; got != tc.want.Format("2006-01") {
				t.Fatalf("card period = %q, want %q", got, tc.want.Format("2006-01"))
			}
		})
	}
}

func TestFaturasInitialPeriodYearBoundaryAndWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 100000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 100000)
	december := time.Date(2026, time.December, 1, 12, 0, 0, 0, time.UTC)
	january := december.AddDate(0, 1, 0)
	seedInitialPeriodInvoice(t, db, "card-a", december, models.InvoiceStatusPaid)
	seedInitialPeriodInvoice(t, db, "card-a", january, models.InvoiceStatusOpen)
	seedInitialPeriodInvoice(t, db, "card-b", december, models.InvoiceStatusPaid)

	month, year, err := initialFaturasIndexPeriod(db, "ws-a", december)
	if err != nil || month != 1 || year != 2027 {
		t.Fatalf("workspace A period = %02d/%d, err %v; want 01/2027", month, year, err)
	}
	month, year, err = initialFaturasIndexPeriod(db, "ws-b", december)
	if err != nil || month != 12 || year != 2026 {
		t.Fatalf("workspace B period = %02d/%d, err %v; want 12/2026", month, year, err)
	}
}

func TestFaturasInitialPeriodPageIgnoresOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-a", "card-a", "cc-a", 100000)
	seedCreditCardWithLimit(t, db, "ws-b", "card-b", "cc-b", 100000)
	current := appMonthStartUTC(time.Now())
	next := current.AddDate(0, 1, 0)
	seedInitialPeriodInvoice(t, db, "card-a", next, models.InvoiceStatusOpen)
	seedInitialPeriodInvoice(t, db, "card-b", current, models.InvoiceStatusPaid)
	h := FaturasHandler{DB: db, WorkspaceID: "ws-b"}
	data, err := h.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, "/faturas", nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := data.PeriodLabel, faturasIndexPeriodLabel(current); got != want {
		t.Fatalf("workspace B page period = %q, want %q", got, want)
	}
}

func TestFaturasInitialPeriodStaysWithinNavigableRange(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCreditCardWithLimit(t, db, "ws-range", "card-range", "cc-range", 100000)
	current := appMonthStartUTC(time.Now())
	seedInitialPeriodInvoice(t, db, "card-range", current.AddDate(11, 0, 0), models.InvoiceStatusOpen)
	month, year, err := initialFaturasIndexPeriod(db, "ws-range", current)
	if err != nil {
		t.Fatal(err)
	}
	if month != int(current.Month()) || year != current.Year() {
		t.Fatalf("period = %02d/%d; want current %02d/%d", month, year, current.Month(), current.Year())
	}
}

func TestFaturasInitialPeriodWithoutCards(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	current := appMonthStartUTC(time.Now())
	h := FaturasHandler{DB: db, WorkspaceID: "ws-without-cards"}
	data, err := h.buildFaturasIndexData(httptest.NewRequest(http.MethodGet, "/faturas", nil))
	if err != nil {
		t.Fatal(err)
	}
	if data.PeriodLabel != faturasIndexPeriodLabel(current) || len(data.Cards) != 0 {
		t.Fatalf("empty page period/cards = %q/%d", data.PeriodLabel, len(data.Cards))
	}
}
