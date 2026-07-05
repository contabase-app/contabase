package handlers

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestFormAccountCheckingShowsBalanceLabel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}

	var checking FormAccount
	for _, acc := range accounts {
		if acc.ID == "checking-test" {
			checking = acc
			break
		}
	}
	if checking.ID == "" {
		t.Fatal("checking account not found in form accounts")
	}
	if checking.Tipo != "conta" {
		t.Fatalf("checking account tipo = %q, want conta", checking.Tipo)
	}
	if !strings.Contains(checking.BalanceLabel, "Saldo") {
		t.Fatalf("checking BalanceLabel should contain 'Saldo', got %q", checking.BalanceLabel)
	}
	if !strings.Contains(checking.BalanceLabel, "R$") {
		t.Fatalf("checking BalanceLabel should contain currency, got %q", checking.BalanceLabel)
	}
	if checking.BalanceLabel == "" {
		t.Fatal("checking BalanceLabel should not be empty")
	}
	if checking.CreditLimitLabel != "" {
		t.Fatalf("checking account should not have CreditLimitLabel, got %q", checking.CreditLimitLabel)
	}
}

func TestFormAccountCreditCardShowsAvailableLimitAndDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}

	var card FormAccount
	for _, acc := range accounts {
		if acc.ID == "card-test" {
			card = acc
			break
		}
	}
	if card.ID == "" {
		t.Fatal("credit card not found in form accounts")
	}
	if card.Tipo != "cartao" {
		t.Fatalf("credit card tipo = %q, want cartao", card.Tipo)
	}
	if !strings.Contains(card.CreditLimitLabel, "Limite") {
		t.Fatalf("card CreditLimitLabel should contain 'Limite', got %q", card.CreditLimitLabel)
	}
	if !strings.Contains(card.CreditLimitLabel, "R$") {
		t.Fatalf("card CreditLimitLabel should contain currency, got %q", card.CreditLimitLabel)
	}
	if !strings.Contains(card.CreditLimitLabel, "vence ") {
		t.Fatalf("card CreditLimitLabel should contain 'vence ', got %q", card.CreditLimitLabel)
	}
	// expect compact date pattern like "10/Ago" after "vence "
	venceIdx := strings.Index(card.CreditLimitLabel, "vence ")
	datePart := card.CreditLimitLabel[venceIdx+len("vence "):]
	if len(datePart) < 6 || !strings.Contains(datePart, "/") {
		t.Fatalf("card CreditLimitLabel should contain compact due date after 'vence ', got %q", card.CreditLimitLabel)
	}
	if strings.Contains(card.CreditLimitLabel, "limite") {
		t.Fatalf("card CreditLimitLabel should not show total limit, got %q", card.CreditLimitLabel)
	}
	if card.BalanceLabel != "" {
		t.Fatalf("credit card should not have BalanceLabel, got %q", card.BalanceLabel)
	}
}

func TestFormAccountCreditCardOpenInvoiceShowsOpenDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-open-due", "card-open-due", "cc-open-due", 500000)
	seedFormAccountInvoice(t, db, "card-open-due", "invoice-open-due", "2026-08", "OPEN", "2026-07-30", "2026-08-07")

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-open-due", UserID: "user-ws-open-due"}
	card := queryFormAccountByID(t, handler, "card-open-due")

	if !strings.Contains(card.CreditLimitLabel, "vence 07/Ago") {
		t.Fatalf("card CreditLimitLabel should use OPEN due_date 07/Ago, got %q", card.CreditLimitLabel)
	}
	if strings.Contains(card.CreditLimitLabel, "30/Jul") {
		t.Fatalf("card CreditLimitLabel must not use closing_date as due date, got %q", card.CreditLimitLabel)
	}
}

func TestFormAccountCreditCardClosedCurrentSkipsToNextOpenInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-closed-skip", "card-closed-skip", "cc-closed-skip", 500000)
	seedFormAccountInvoice(t, db, "card-closed-skip", "invoice-closed-current", "2026-07", "CLOSED", "2026-06-30", "2026-07-07")
	seedFormAccountInvoice(t, db, "card-closed-skip", "invoice-next-open", "2026-08", "OPEN", "2026-07-30", "2026-08-07")

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-closed-skip", UserID: "user-ws-closed-skip"}
	card := queryFormAccountByID(t, handler, "card-closed-skip")

	if !strings.Contains(card.CreditLimitLabel, "vence 07/Ago") {
		t.Fatalf("card CreditLimitLabel should skip CLOSED current invoice and use next OPEN due_date, got %q", card.CreditLimitLabel)
	}
	for _, forbidden := range []string{"07/Jul", "30/Jun", "30/Jul"} {
		if strings.Contains(card.CreditLimitLabel, forbidden) {
			t.Fatalf("card CreditLimitLabel should not contain %s from closed/closing dates, got %q", forbidden, card.CreditLimitLabel)
		}
	}
}

func TestFormAccountCreditCardClosedFutureDueStillSkipsToOpenInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-future-closed", "card-future-closed", "cc-future-closed", 500000)
	seedFormAccountInvoice(t, db, "card-future-closed", "invoice-closed-future", "2026-08", "CLOSED", "2026-07-30", "2026-08-10")
	seedFormAccountInvoice(t, db, "card-future-closed", "invoice-open-after", "2026-09", "OPEN", "2026-08-30", "2026-09-10")

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-future-closed", UserID: "user-ws-future-closed"}
	card := queryFormAccountByID(t, handler, "card-future-closed")

	if !strings.Contains(card.CreditLimitLabel, "vence 10/Set") {
		t.Fatalf("card CreditLimitLabel should ignore CLOSED invoice even with future due_date, got %q", card.CreditLimitLabel)
	}
	if strings.Contains(card.CreditLimitLabel, "10/Ago") {
		t.Fatalf("card CreditLimitLabel should not use future due_date from CLOSED invoice, got %q", card.CreditLimitLabel)
	}
}

func TestFormAccountCreditCardFallsBackWithoutReliableOpenDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-no-due", "card-no-due", "cc-no-due", 100000)
	seedFormAccountInvoiceWithUnixDates(t, db, "card-no-due", "invoice-open-zero-due", "2026-08", "OPEN", formAccountNoonUnix("2026-07-30"), 0)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-no-due", UserID: "user-ws-no-due"}
	card := queryFormAccountByID(t, handler, "card-no-due")

	if card.CreditLimitLabel != "Limite R$ 1.000,00" {
		t.Fatalf("card CreditLimitLabel should fall back to limit only, got %q", card.CreditLimitLabel)
	}
}

func TestFormAccountCreditCardCalculatesAvailableLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}

	var card FormAccount
	for _, acc := range accounts {
		if acc.ID == "card-test" {
			card = acc
			break
		}
	}
	if card.ID == "" {
		t.Fatal("credit card not found in form accounts")
	}

	// limit 500000 - expense 25000 = 475000 -> R$ 4.750,00
	if !strings.Contains(card.CreditLimitLabel, "4.750") {
		t.Fatalf("card CreditLimitLabel should show available limit 4.750, got %q", card.CreditLimitLabel)
	}
	if strings.Contains(card.CreditLimitLabel, "5.000") {
		t.Fatalf("card CreditLimitLabel should not show total limit 5.000, got %q", card.CreditLimitLabel)
	}
}

func TestFormAccountCreditCardWithoutLimit(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-zero", "card-zero", "cc-zero", 100000)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-zero", UserID: "user-ws-zero"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}

	var card FormAccount
	for _, acc := range accounts {
		if acc.ID == "card-zero" {
			card = acc
			break
		}
	}
	if card.ID == "" {
		t.Fatal("credit card not found in form accounts")
	}
	if !strings.Contains(card.CreditLimitLabel, "Limite R$ 1.000,00") {
		t.Fatalf("zero-usage card CreditLimitLabel should show 'Limite R$ 1.000,00', got %q", card.CreditLimitLabel)
	}
	if strings.Contains(card.CreditLimitLabel, "vence ") {
		t.Fatalf("card without reliable open due date should not show due suffix, got %q", card.CreditLimitLabel)
	}
}

func TestFormAccountWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-other", UserID: "user-other"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}

	for _, acc := range accounts {
		if acc.ID == "checking-test" || acc.ID == "card-test" {
			t.Fatalf("account from other workspace leaked: %s", acc.ID)
		}
	}
}

func TestFormAccountCreditCardDueDateWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedCreditCardWithLimit(t, db, "ws-due-a", "card-due-a", "cc-due-a", 100000)
	seedCreditCardWithLimit(t, db, "ws-due-b", "card-due-b", "cc-due-b", 100000)
	seedFormAccountInvoice(t, db, "card-due-a", "invoice-due-a", "2026-08", "OPEN", "2026-07-30", "2026-08-07")
	seedFormAccountInvoice(t, db, "card-due-b", "invoice-due-b", "2026-08", "OPEN", "2026-07-30", "2026-08-10")

	if dueUnix, err := nextOpenInvoiceDueDateForFormAccount(db, "ws-due-a", "card-due-b"); err != nil || dueUnix != 0 {
		t.Fatalf("mismatched workspace/card due = %d, err = %v; want 0, nil", dueUnix, err)
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-due-a", UserID: "user-ws-due-a"}
	card := queryFormAccountByID(t, handler, "card-due-a")
	if !strings.Contains(card.CreditLimitLabel, "vence 07/Ago") {
		t.Fatalf("card CreditLimitLabel should use own workspace due_date, got %q", card.CreditLimitLabel)
	}
	if strings.Contains(card.CreditLimitLabel, "10/Ago") {
		t.Fatalf("card CreditLimitLabel leaked other workspace due_date, got %q", card.CreditLimitLabel)
	}
}

func TestFormatDueDateCompact(t *testing.T) {
	ts := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).Unix()
	got := formatDueDateCompact(ts)
	if got != "10/Ago" {
		t.Fatalf("formatDueDateCompact = %q, want 10/Ago", got)
	}
	if formatDueDateCompact(0) != "" {
		t.Fatalf("formatDueDateCompact(0) should be empty, got %q", formatDueDateCompact(0))
	}
	if formatDueDateCompact(-1) != "" {
		t.Fatalf("formatDueDateCompact(-1) should be empty, got %q", formatDueDateCompact(-1))
	}
}

func queryFormAccountByID(t *testing.T, handler TransactionHandler, accountID string) FormAccount {
	t.Helper()
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}
	for _, acc := range accounts {
		if acc.ID == accountID {
			return acc
		}
	}
	t.Fatalf("form account %s not found", accountID)
	return FormAccount{}
}

func seedFormAccountInvoice(t *testing.T, db *sql.DB, accountID, invoiceID, reference, status, closingDate, dueDate string) {
	t.Helper()
	seedFormAccountInvoiceWithUnixDates(t, db, accountID, invoiceID, reference, status, formAccountNoonUnix(closingDate), formAccountNoonUnix(dueDate))
}

func seedFormAccountInvoiceWithUnixDates(t *testing.T, db *sql.DB, accountID, invoiceID, reference, status string, closingUnix, dueUnix int64) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, invoiceID, accountID, reference, closingUnix, dueUnix, status, now)
}

func formAccountNoonUnix(date string) int64 {
	t, _ := time.Parse("2006-01-02", date)
	return t.Add(12 * time.Hour).Unix()
}
