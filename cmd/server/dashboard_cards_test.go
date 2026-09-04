package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

func TestSortDashboardCardsByPaymentPriority(t *testing.T) {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	cards := []handlers.CreditCardCard{
		{ID: "paid", Status: models.InvoiceStatusPaid, Amount: 990000, DueDateUnix: time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "zero", Amount: 0, DueDateUnix: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "upcoming-later", Amount: 9500, DueDateUnix: time.Date(2026, time.September, 12, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "same-due-smaller", Amount: 1500, DueDateUnix: time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "overdue-recent", Amount: 1200, DueDateUnix: time.Date(2026, time.August, 29, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "undated", Amount: 6000},
		{ID: "overdue-oldest", Amount: 500, DueDateUnix: time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "same-due-larger", Amount: 7200, DueDateUnix: time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC).Unix()},
	}

	handlers.SortDashboardCardsByPaymentPriority(cards, now)

	got := make([]string, 0, len(cards))
	for _, card := range cards {
		got = append(got, card.ID)
	}
	want := []string{
		"overdue-oldest",
		"overdue-recent",
		"same-due-larger",
		"same-due-smaller",
		"upcoming-later",
		"undated",
		"paid",
		"zero",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payment-priority order = %v, want %v", got, want)
	}
}

func TestSortDashboardCardsUsesSaoPauloCivilDayAcrossUTCBoundary(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	t.Setenv("TZ", "UTC")

	now := time.Date(2026, time.September, 1, 1, 30, 0, 0, time.UTC)
	cards := []handlers.CreditCardCard{
		{ID: "due-today", Amount: 1000, DueDateUnix: time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "overdue", Amount: 1000, DueDateUnix: time.Date(2026, time.August, 30, 0, 0, 0, 0, time.UTC).Unix()},
	}

	handlers.SortDashboardCardsByPaymentPriority(cards, now)
	if got := []string{cards[0].ID, cards[1].ID}; !reflect.DeepEqual(got, []string{"overdue", "due-today"}) {
		t.Fatalf("payment-priority order = %v, want overdue before due-today", got)
	}
}
