package handlers

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

type invoiceClosingDaysScenario struct {
	name        string
	timezone    string
	now         time.Time
	closing     time.Time
	status      string
	wantDays    int
	wantCard    string
	wantSummary string
	wantCycle   InvoiceCycleState
}

func seedInvoiceClosingDaysScenario(t *testing.T, db *sql.DB, now, closing time.Time, status string, total int64) (wsID, cardID, invoiceID string, closingUnix int64) {
	t.Helper()
	wsID = "ws-closing-days"
	cardID = "card-closing-days"
	invoiceID = "inv-closing-days"
	seedCreditCardWithLimit(t, db, wsID, cardID, "cc-closing-days", 1000000)
	reference := AppCivilDateUTC(now).Format("2006-01")
	seedInvoiceForCard(t, db, wsID, cardID, invoiceID, reference, status, closing.Year(), int(closing.Month()), closing.Day())
	if total > 0 {
		seedExpenseOnInvoice(t, db, wsID, cardID, invoiceID, total, 1, 1, "tx-closing-days")
	}
	closingUnix = time.Date(closing.Year(), closing.Month(), closing.Day(), 12, 0, 0, 0, time.UTC).Unix()
	return wsID, cardID, invoiceID, closingUnix
}

func TestInvoiceClosingDaysCivilConsistency(t *testing.T) {
	const saoPaulo = "America/Sao_Paulo"
	nowBRT := time.Date(2026, time.October, 30, 21, 0, 0, 0, time.UTC)

	cases := []invoiceClosingDaysScenario{
		{
			name:        "fechamento hoje",
			timezone:    saoPaulo,
			now:         nowBRT,
			closing:     time.Date(2026, time.October, 30, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    0,
			wantCard:    "fecha hoje",
			wantSummary: "Fechada",
			wantCycle:   InvoiceCycleClosed,
		},
		{
			name:        "amanha",
			timezone:    saoPaulo,
			now:         nowBRT,
			closing:     time.Date(2026, time.October, 31, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    1,
			wantCard:    "fecha em 1 dia",
			wantSummary: "Fecha em 1 dia",
			wantCycle:   InvoiceCycleSoon,
		},
		{
			name:        "dois dias regressao",
			timezone:    saoPaulo,
			now:         nowBRT,
			closing:     time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    2,
			wantCard:    "fecha em 2 dias",
			wantSummary: "Fecha em 2 dias",
			wantCycle:   InvoiceCycleSoon,
		},
		{
			name:        "mais de sete dias",
			timezone:    saoPaulo,
			now:         nowBRT,
			closing:     time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    11,
			wantCard:    "fecha em 11 dias",
			wantSummary: "Aberta",
			wantCycle:   InvoiceCycleOpen,
		},
		{
			name:        "dia anterior ao fechamento",
			timezone:    saoPaulo,
			now:         time.Date(2026, time.October, 31, 12, 0, 0, 0, time.UTC),
			closing:     time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    1,
			wantCard:    "fecha em 1 dia",
			wantSummary: "Fecha em 1 dia",
			wantCycle:   InvoiceCycleSoon,
		},
		{
			name:        "virada de mes",
			timezone:    saoPaulo,
			now:         time.Date(2026, time.November, 1, 2, 30, 0, 0, time.UTC),
			closing:     time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    1,
			wantCard:    "fecha em 1 dia",
			wantSummary: "Fecha em 1 dia",
			wantCycle:   InvoiceCycleSoon,
		},
		{
			name:        "virada de ano",
			timezone:    saoPaulo,
			now:         time.Date(2027, time.January, 1, 2, 30, 0, 0, time.UTC),
			closing:     time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    1,
			wantCard:    "fecha em 1 dia",
			wantSummary: "Fecha em 1 dia",
			wantCycle:   InvoiceCycleSoon,
		},
		{
			name:        "antes da meia-noite",
			timezone:    saoPaulo,
			now:         time.Date(2026, time.October, 31, 2, 30, 0, 0, time.UTC),
			closing:     time.Date(2026, time.October, 31, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    1,
			wantCard:    "fecha em 1 dia",
			wantSummary: "Fecha em 1 dia",
			wantCycle:   InvoiceCycleSoon,
		},
		{
			name:        "depois da meia-noite",
			timezone:    saoPaulo,
			now:         time.Date(2026, time.October, 31, 3, 30, 0, 0, time.UTC),
			closing:     time.Date(2026, time.October, 31, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    0,
			wantCard:    "fecha hoje",
			wantSummary: "Fechada",
			wantCycle:   InvoiceCycleClosed,
		},
		{
			name:        "fechada ontem",
			timezone:    saoPaulo,
			now:         nowBRT,
			closing:     time.Date(2026, time.October, 29, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    -1,
			wantCard:    "fechada",
			wantSummary: "Fechada",
			wantCycle:   InvoiceCycleClosed,
		},
		{
			name:        "horario de verao",
			timezone:    "America/New_York",
			now:         time.Date(2026, time.March, 7, 12, 30, 0, 0, time.UTC),
			closing:     time.Date(2026, time.March, 9, 0, 0, 0, 0, time.UTC),
			status:      models.InvoiceStatusOpen,
			wantDays:    2,
			wantCard:    "fecha em 2 dias",
			wantSummary: "Fecha em 2 dias",
			wantCycle:   InvoiceCycleSoon,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTABASE_TIMEZONE", tc.timezone)
			t.Setenv("TZ", "UTC")

			db := openTestDB(t)
			defer db.Close()

			const total int64 = 12345
			wsID, cardID, invoiceID, closingUnix := seedInvoiceClosingDaysScenario(t, db, tc.now, tc.closing, tc.status, total)
			civilNow := AppCivilDateUTC(tc.now)

			cards, err := queryFaturasIndexCardsAt(db, wsID, civilNow.Year(), civilNow.Month(), tc.now)
			if err != nil {
				t.Fatalf("query faturas index cards: %v", err)
			}
			if len(cards) != 1 {
				t.Fatalf("faturas cards = %d, want 1", len(cards))
			}
			invoiceCard := cards[0]
			if invoiceCard.ID != cardID || invoiceCard.InvoiceID != invoiceID {
				t.Fatalf("faturas card identity = %q/%q, want %q/%q", invoiceCard.ID, invoiceCard.InvoiceID, cardID, invoiceID)
			}
			if invoiceCard.ClosingDaysLabel != tc.wantCard {
				t.Fatalf("faturas card label = %q, want %q", invoiceCard.ClosingDaysLabel, tc.wantCard)
			}

			dashboard := buildDashboardDataAt(db, "user-"+wsID, wsID, tc.now)
			if len(dashboard.Cards) != 1 {
				t.Fatalf("dashboard cards = %d, want 1", len(dashboard.Cards))
			}
			dashboardCard := dashboard.Cards[0]
			if dashboardCard.ClosingDaysLabel != tc.wantCard {
				t.Fatalf("dashboard card label = %q, want %q", dashboardCard.ClosingDaysLabel, tc.wantCard)
			}
			if dashboardCard.ClosingDaysLabel != invoiceCard.ClosingDaysLabel {
				t.Fatalf("dashboard label %q != faturas label %q", dashboardCard.ClosingDaysLabel, invoiceCard.ClosingDaysLabel)
			}

			badge := computeCycleBadge(closingUnix, tc.now)
			if badge != tc.wantSummary {
				t.Fatalf("cycle badge = %q, want %q", badge, tc.wantSummary)
			}
			if tc.wantDays > 0 && tc.wantDays <= invoiceCycleSoonDays && !strings.EqualFold(invoiceCard.ClosingDaysLabel, badge) {
				t.Fatalf("card label %q and cycle badge %q disagree on civil days", invoiceCard.ClosingDaysLabel, badge)
			}

			state := computeInvoiceState(total, 0, closingUnix, tc.now)
			wantCycleDays := tc.wantDays
			if wantCycleDays < 0 {
				wantCycleDays = 0
			}
			if state.Cycle != tc.wantCycle || state.CycleDays != wantCycleDays || state.CycleLabel != tc.wantSummary {
				t.Fatalf("invoice state cycle = %q/%d/%q, want %q/%d/%q", state.Cycle, state.CycleDays, state.CycleLabel, tc.wantCycle, wantCycleDays, tc.wantSummary)
			}
			if state.Total != total || state.PaidActive != 0 || state.Pending != total || state.Financial != InvoiceFinancialPending {
				t.Fatalf("invoice state financial values changed: %+v", state)
			}
			if invoiceCard.Money != MoneyMinor(total) || dashboardCard.Money != MoneyMinor(total) {
				t.Fatalf("card amounts changed: faturas=%v dashboard=%v want=%v", invoiceCard.Money, dashboardCard.Money, MoneyMinor(total))
			}

			var gotClosing, gotDue, gotTotal int64
			var gotStatus string
			if err := db.QueryRow(`SELECT closing_date, due_date, status FROM invoices WHERE id = ?`, invoiceID).Scan(&gotClosing, &gotDue, &gotStatus); err != nil {
				t.Fatalf("read invoice after producers: %v", err)
			}
			if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM transactions WHERE invoice_id = ?`, invoiceID).Scan(&gotTotal); err != nil {
				t.Fatalf("read invoice transactions after producers: %v", err)
			}
			if gotClosing != closingUnix || gotTotal != total {
				t.Fatalf("invoice dates or amount changed: closing=%d total=%d want closing=%d total=%d", gotClosing, gotTotal, closingUnix, total)
			}
			if gotStatus == models.InvoiceStatusPaid {
				t.Fatalf("invoice status changed to PAID without a payment")
			}
			if gotDue <= 0 {
				t.Fatalf("invoice due date was cleared")
			}
		})
	}
}

func TestInvoiceClosingDaysPreserveInvoiceStatuses(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	t.Setenv("TZ", "UTC")

	now := time.Date(2026, time.October, 30, 21, 0, 0, 0, time.UTC)
	closing := time.Date(2099, time.January, 10, 0, 0, 0, 0, time.UTC)
	const total int64 = 54321

	for _, status := range []string{models.InvoiceStatusOpen, models.InvoiceStatusClosed, models.InvoiceStatusPaid} {
		t.Run(status, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()

			wsID, _, invoiceID, closingUnix := seedInvoiceClosingDaysScenario(t, db, now, closing, status, total)
			civilNow := AppCivilDateUTC(now)

			countInvoices := func() int {
				t.Helper()
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM invoices WHERE account_id = 'card-closing-days'`).Scan(&count); err != nil {
					t.Fatalf("count invoices: %v", err)
				}
				return count
			}
			readInvoice := func() (string, int64, int64, int64) {
				t.Helper()
				var gotStatus string
				var gotClosing, gotDue, gotPaid int64
				if err := db.QueryRow(`SELECT status, closing_date, due_date, COALESCE(paid_amount, 0) FROM invoices WHERE id = ?`, invoiceID).Scan(&gotStatus, &gotClosing, &gotDue, &gotPaid); err != nil {
					t.Fatalf("read invoice: %v", err)
				}
				return gotStatus, gotClosing, gotDue, gotPaid
			}

			beforeStatus, beforeClosing, beforeDue, beforePaid := readInvoice()
			beforeInvoiceCount := countInvoices()

			cards, err := queryFaturasIndexCardsAt(db, wsID, civilNow.Year(), civilNow.Month(), now)
			if err != nil {
				t.Fatalf("query faturas index cards: %v", err)
			}
			if len(cards) != 1 {
				t.Fatalf("faturas cards = %d, want 1", len(cards))
			}
			faturasLabel := cards[0].ClosingDaysLabel
			if status == models.InvoiceStatusPaid {
				if faturasLabel != "período selecionado" {
					t.Fatalf("paid faturas card label = %q, want default period label", faturasLabel)
				}
			} else {
				if !strings.HasPrefix(faturasLabel, "fecha em ") || !strings.HasSuffix(faturasLabel, " dias") {
					t.Fatalf("%s faturas card label = %q, want a closing countdown", status, faturasLabel)
				}
				if badge := computeCycleBadge(closingUnix, now); badge != "Aberta" {
					t.Fatalf("cycle badge = %q, want Aberta", badge)
				}
			}

			dashboard := buildDashboardDataAt(db, "user-"+wsID, wsID, now)
			foundInvoice := false
			for _, card := range dashboard.Cards {
				if card.InvoiceID != invoiceID {
					continue
				}
				foundInvoice = true
				if status == models.InvoiceStatusPaid {
					if card.ClosingDaysLabel != "" {
						t.Fatalf("paid dashboard card label = %q, want empty", card.ClosingDaysLabel)
					}
					continue
				}
				if card.ClosingDaysLabel != faturasLabel {
					t.Fatalf("dashboard label %q != faturas label %q for %s", card.ClosingDaysLabel, faturasLabel, status)
				}
			}
			if status != models.InvoiceStatusPaid && !foundInvoice {
				t.Fatalf("dashboard did not expose the %s invoice card", status)
			}

			afterStatus, afterClosing, afterDue, afterPaid := readInvoice()
			if afterStatus != beforeStatus || afterClosing != beforeClosing || afterDue != beforeDue || afterPaid != beforePaid {
				t.Fatalf("invoice state changed: before=%s/%d/%d/%d after=%s/%d/%d/%d", beforeStatus, beforeClosing, beforeDue, beforePaid, afterStatus, afterClosing, afterDue, afterPaid)
			}
			if status == models.InvoiceStatusClosed && countInvoices() != beforeInvoiceCount {
				t.Fatalf("invoice count changed for %s", status)
			}
		})
	}
}
