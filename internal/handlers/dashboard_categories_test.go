package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestDashboardCategoriesCompareSameElapsedIntervalAndRankByAmount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDashboardCategoriesScenario(t, db)

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	data := BuildDashboardCategories(db, "ws-cat-dash", now)

	if !data.HasData {
		t.Fatal("categories should have data")
	}
	if data.CurrentLabel != "01/10 a 24/10" || data.PreviousLabel != "01/09 a 24/09" {
		t.Fatalf("period labels = %q / %q", data.CurrentLabel, data.PreviousLabel)
	}
	if data.CurrentStart != "2026-10-01" || data.CurrentEnd != "2026-10-24" ||
		data.PreviousStart != "2026-09-01" || data.PreviousEnd != "2026-09-24" {
		t.Fatalf("period bounds = %+v", data)
	}
	assertMoneyDisplay(t, "period total", data.Total, "5.800", ",00")

	wantNames := []string{"Moradia", "Alimentação", "Transporte", "Lazer", "Saúde"}
	wantAmounts := []string{"2.000", "1.500", "800", "600", "500"}
	wantVariations := []string{"+100,0%", "0,0%", "Novo gasto", "−50,0%", "Novo gasto"}
	wantClasses := []string{"is-up is-alert", "is-flat", "is-new", "is-down", "is-new"}
	if len(data.Items) != len(wantNames) {
		t.Fatalf("items len = %d, want %d: %#v", len(data.Items), len(wantNames), data.Items)
	}
	for i, item := range data.Items {
		if item.Name != wantNames[i] {
			t.Fatalf("item[%d].Name = %q, want %q", i, item.Name, wantNames[i])
		}
		assertMoneyDisplay(t, item.Name+" amount", item.Amount, wantAmounts[i], ",00")
		if item.VariationLabel != wantVariations[i] {
			t.Fatalf("item[%d].VariationLabel = %q, want %q", i, item.VariationLabel, wantVariations[i])
		}
		if item.VariationClass != wantClasses[i] {
			t.Fatalf("item[%d].VariationClass = %q, want %q", i, item.VariationClass, wantClasses[i])
		}
	}
	for _, item := range data.Items {
		if item.Name == "Educação" {
			t.Fatal("sixth category must not appear in the top five")
		}
	}
	if data.Items[0].AmountCents != 200000 || data.Items[0].SharePercent != 34 || data.Items[0].BarPercent != 34 || data.Items[0].ShareLabel != "34%" {
		t.Fatalf("Moradia share = %#v", data.Items[0])
	}
}

func TestDashboardCategoriesTotalMatchesCanonicalCompetenceExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDashboardCategoriesScenario(t, db)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	data := BuildDashboardCategories(db, "ws-cat-dash", now)

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(2026, 10, 24, 23, 59, 59, 0, time.UTC).Unix()
	canonical, err := queryReportExactSum(context.Background(), db, "test category total", `
		SELECT `+transactionNetAmountTSQL+`
		FROM transactions t
		WHERE t.workspace_id = ? AND t.type = 'EXPENSE'
		  AND t.date >= ? AND t.date <= ?
		  AND `+excludeInvoicePaymentCompetenceClause("t")+`
	`, "ws-cat-dash", start, end)
	if err != nil {
		t.Fatalf("canonical competence total: %v", err)
	}
	got := data.Total.rawCents()
	if got != canonical {
		t.Fatalf("categories total = %d, canonical competence = %d", got, canonical)
	}
	if canonical != 580000 {
		t.Fatalf("canonical total = %d, want 580000 (invoice payment, transfer and other workspace excluded)", canonical)
	}

	// The sixth category (400,00) is not shown, but it is part of the
	// denominator: Moradia is 2.000/5.800 = 34%, not 2.000/5.400.
	if len(data.Items) != 5 || data.Items[0].SharePercent != 34 || data.Items[0].ShareLabel != "34%" {
		t.Fatalf("top-five share must use the full period total: %#v", data.Items)
	}
}

func TestDashboardCategoriesGroupParentRefundsAndInvoicePayments(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDashboardCategoriesScenario(t, db)
	data := BuildDashboardCategories(db, "ws-cat-dash", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))

	// Aluguel is a child of Moradia: the parent total must aggregate it once and
	// the child must not appear as a second category.
	moradia := data.Items[0]
	if moradia.Name != "Moradia" || moradia.AmountCents != 200000 {
		t.Fatalf("Moradia = %#v, want parent total 200000", moradia)
	}
	for _, item := range data.Items {
		if item.Name == "Aluguel" {
			t.Fatal("child category must be aggregated into its parent")
		}
	}

	// Alimentação is 1.600,00 with an active 100,00 refund, so the canonical net
	// is 1.500,00. The previous period is 1.500,00 and the variation is flat.
	alimentacao := data.Items[1]
	if alimentacao.AmountCents != 150000 || alimentacao.VariationLabel != "0,0%" {
		t.Fatalf("Alimentação = %#v, want net 150000 and flat", alimentacao)
	}

	// The invoice-payment transaction, the transfer and the other workspace all
	// carry values large enough to change the ranking if they leaked in.
	if data.Total.Cents != ",00" || data.Total.Reais != "5.800" {
		t.Fatalf("total = %#v, want R$ 5.800,00", data.Total)
	}
	for _, item := range data.Items {
		if item.AmountCents >= 888888 {
			t.Fatalf("excluded movement leaked into %#v", item)
		}
	}
}

func TestDashboardCategoriesNoPreviousBaseIsNewGasto(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedDashboardCategoriesScenario(t, db)
	data := BuildDashboardCategories(db, "ws-cat-dash", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))

	for _, item := range data.Items {
		if item.Name != "Transporte" && item.Name != "Saúde" {
			continue
		}
		if item.HasBase {
			t.Fatalf("%s should not have a previous base: %#v", item.Name, item)
		}
		if item.VariationLabel != "Novo gasto" || item.VariationClass != "is-new" {
			t.Fatalf("%s variation = %#v", item.Name, item)
		}
		if item.VariationDescription == "" {
			t.Fatalf("%s must carry an accessible description", item.Name)
		}
	}
}

func TestDashboardCategoriesEmptyWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-empty-cat', 'User', 'empty-cat@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, created_at, updated_at) VALUES ('ws-empty-cat', 'Empty', '', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-empty-cat', 'user-empty-cat', 'ADMIN', ?)`, now)

	data := BuildDashboardCategories(db, "ws-empty-cat", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	if data.HasData || len(data.Items) != 0 {
		t.Fatalf("empty workspace must render the empty state, got %#v", data)
	}
}

func TestDashboardCategoriesPreviousMonthClampsShortMonths(t *testing.T) {
	cases := []struct {
		name          string
		previousStart time.Time
		day           int
		want          string
	}{
		{"march-31-to-february", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 31, "2026-02-28"},
		{"march-30-to-february", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 30, "2026-02-28"},
		{"april-30-to-march", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 30, "2026-03-30"},
		{"may-31-to-april", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), 31, "2026-04-30"},
		{"january-31-to-december", time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), 31, "2025-12-31"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameDayPreviousMonth(tc.previousStart, tc.day)
			if got.Format("2006-01-02") != tc.want {
				t.Fatalf("previous end = %s, want %s", got.Format("2006-01-02"), tc.want)
			}
		})
	}

	leap := time.Date(2028, 3, 1, 0, 0, 0, 0, time.UTC)
	if got := sameDayPreviousMonth(leap.AddDate(0, -1, 0), 31); got.Format("2006-01-02") != "2028-02-29" {
		t.Fatalf("leap year clamp = %s, want 2028-02-29", got.Format("2006-01-02"))
	}
}

func seedDashboardCategoriesScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-cat-dash', 'User Cat', 'cat-dash@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-cat-other', 'User Other', 'cat-other@example.com', 'hash', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, created_at, updated_at) VALUES ('ws-cat-dash', 'Cat Dash', '', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, type, created_at, updated_at) VALUES ('ws-cat-other', 'Cat Other', '', 'personal', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-cat-dash', 'user-cat-dash', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-cat-other', 'user-cat-other', 'ADMIN', ?)`, now)

	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-cat-dash', 'ws-cat-dash', 'Conta', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('card-cat-dash', 'ws-cat-dash', 'Cartão', 'CREDIT_CARD', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES ('acc-cat-other', 'ws-cat-other', 'Conta Other', 'CHECKING', 0, 0, ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-cat-dash', 'card-cat-dash', 20, 10, 500000)`)

	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-moradia', 'ws-cat-dash', 'Moradia', 'house', '#6b7280', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at) VALUES ('cat-aluguel', 'ws-cat-dash', 'Aluguel', 'house', '#6b7280', 'EXPENSE', 'Essencial', 'cat-moradia', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-food', 'ws-cat-dash', 'Alimentação', 'utensils', '#6b7280', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-transport', 'ws-cat-dash', 'Transporte', 'bus', '#6b7280', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-leisure', 'ws-cat-dash', 'Lazer', 'popcorn', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-health', 'ws-cat-dash', 'Saúde', 'heart-pulse', '#6b7280', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-education', 'ws-cat-dash', 'Educação', 'graduation-cap', '#6b7280', 'EXPENSE', 'Essencial', ?)`, now)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at) VALUES ('cat-other', 'ws-cat-other', 'Outro', 'tag', '#6b7280', 'EXPENSE', 'Essencial', ?)`, now)

	insertDashboardInvoice(t, db, "inv-cat-dash", "card-cat-dash", "2026-10", "2026-09-20", "2026-10-10", "PAID", 999999)

	// Current period: 2026-10-01 .. 2026-10-24.
	insertDashboardCategoryTx(t, db, "tx-moradia-cur", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-aluguel", "EXPENSE", 200000, "2026-10-05", "paid")
	insertDashboardCardPurchase(t, db, "tx-food-cur", "ws-cat-dash", "user-cat-dash", "card-cat-dash", "inv-cat-dash", 160000, "2026-10-06", "Mercado no cartão")
	execTestSQL(t, db, `UPDATE transactions SET category_id = 'cat-food' WHERE id = 'tx-food-cur' AND workspace_id = 'ws-cat-dash'`)
	insertDashboardCategoryTx(t, db, "tx-transport-cur", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-transport", "EXPENSE", 80000, "2026-10-08", "pending")
	insertDashboardCategoryTx(t, db, "tx-leisure-cur", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-leisure", "EXPENSE", 60000, "2026-10-09", "paid")
	insertDashboardCategoryTx(t, db, "tx-health-cur", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-health", "EXPENSE", 50000, "2026-10-10", "paid")
	insertDashboardCategoryTx(t, db, "tx-education-cur", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-education", "EXPENSE", 40000, "2026-10-11", "paid")

	// Previous comparable period: 2026-09-01 .. 2026-09-24.
	insertDashboardCategoryTx(t, db, "tx-moradia-prev", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-aluguel", "EXPENSE", 100000, "2026-09-05", "paid")
	insertDashboardCategoryTx(t, db, "tx-food-prev", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-food", "EXPENSE", 150000, "2026-09-06", "paid")
	insertDashboardCategoryTx(t, db, "tx-leisure-prev", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-leisure", "EXPENSE", 120000, "2026-09-09", "paid")

	// Refund: 1.600,00 gross becomes 1.500,00 net in the current period.
	execTestSQL(t, db, `INSERT INTO transaction_refunds (id, workspace_id, transaction_id, amount_cents, description, created_by) VALUES ('refund-food', 'ws-cat-dash', 'tx-food-cur', 10000, 'Estorno parcial', 'user-cat-dash')`)

	// A paid invoice and its payment transaction. The payment must be excluded
	// by invoice_payments lineage even though its amount is huge.
	insertDashboardCategoryTx(t, db, "tx-invoice-payment", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "", "EXPENSE", 999999, "2026-10-12", "paid")
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_at) VALUES ('ip-cat-dash', 'ws-cat-dash', 'inv-cat-dash', 'acc-cat-dash', 'tx-invoice-payment', 999999, ?, 'manual', ?)`, testUnixDate("2026-10-12"), now)

	insertDashboardCategoryTx(t, db, "tx-transfer", "ws-cat-dash", "user-cat-dash", "acc-cat-dash", "cat-food", "TRANSFER", 888888, "2026-10-13", "paid")
	insertDashboardCategoryTx(t, db, "tx-other-ws", "ws-cat-other", "user-cat-other", "acc-cat-other", "cat-other", "EXPENSE", 777777, "2026-10-14", "paid")
}

func insertDashboardCategoryTx(t *testing.T, db *sql.DB, id, workspaceID, userID, accountID, categoryID, txType string, amount int64, date, status string) {
	t.Helper()
	now := time.Now().Unix()
	var category any
	if categoryID != "" {
		category = categoryID
	}
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?)
	`, id, workspaceID, userID, accountID, category, txType, amount, testUnixDate(date), id, status, now, now)
}
