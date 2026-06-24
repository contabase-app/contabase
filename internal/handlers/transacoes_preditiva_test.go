package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func TestPredictiveSuggestionsFilterByExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("restaurante", "EXPENSE")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions EXPENSE: %v", err)
	}
	if len(suggestions) == 0 {
		t.Fatal("expected at least one EXPENSE suggestion for restaurante")
	}
	for _, s := range suggestions {
		if s.Type != "despesa" {
			t.Fatalf("expected only despesa, got %s for %q", s.Type, s.Description)
		}
	}
}

func TestPredictiveSuggestionsFilterByIncome(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("restaurante", "INCOME")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions INCOME: %v", err)
	}
	if len(suggestions) == 0 {
		t.Fatal("expected at least one INCOME suggestion for restaurante")
	}
	for _, s := range suggestions {
		if s.Type != "receita" {
			t.Fatalf("expected only receita, got %s for %q", s.Type, s.Description)
		}
	}
}

func TestPredictiveSuggestionsFilterByTransfer(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("restaurante", "TRANSFER")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions TRANSFER: %v", err)
	}
	if len(suggestions) == 0 {
		t.Fatal("expected at least one TRANSFER suggestion for restaurante")
	}
	for _, s := range suggestions {
		if s.Type != "transferencia" {
			t.Fatalf("expected only transferencia, got %s for %q", s.Type, s.Description)
		}
	}
}

func TestPredictiveSuggestionsWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("restaurante", "EXPENSE")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions: %v", err)
	}
	for _, s := range suggestions {
		if s.AccountID == "b-check" {
			t.Fatal("workspace B suggestion leaked into workspace A results")
		}
	}
}

func TestPredictiveSuggestionsNoMixingTypes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}

	for _, tc := range []struct {
		txType   string
		wantType string
	}{
		{"EXPENSE", "despesa"},
		{"INCOME", "receita"},
		{"TRANSFER", "transferencia"},
	} {
		suggestions, err := handler.queryPredictiveSuggestions("restaurante", tc.txType)
		if err != nil {
			t.Fatalf("queryPredictiveSuggestions %s: %v", tc.txType, err)
		}
		if len(suggestions) != 1 {
			t.Fatalf("expected exactly 1 suggestion for %s, got %d", tc.txType, len(suggestions))
		}
		if suggestions[0].Type != tc.wantType {
			t.Fatalf("type %s: expected %s, got %s", tc.txType, tc.wantType, suggestions[0].Type)
		}
	}
}

func TestPredictiveSuggestionsTransferHasNoCategory(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("restaurante", "TRANSFER")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions TRANSFER: %v", err)
	}
	if len(suggestions) == 0 {
		t.Fatal("expected at least one TRANSFER suggestion")
	}
	for _, s := range suggestions {
		if s.CategoryID != "" {
			t.Fatalf("TRANSFER suggestion should have empty CategoryID, got %q", s.CategoryID)
		}
		if s.CategoryName != "" {
			t.Fatalf("TRANSFER suggestion should have empty CategoryName, got %q", s.CategoryName)
		}
		if s.DestinationAccountID == "" {
			t.Fatal("TRANSFER suggestion should have DestinationAccountID")
		}
		if s.DestinationAccountName == "" {
			t.Fatal("TRANSFER suggestion should have DestinationAccountName")
		}
	}
}

func TestPredictiveSuggestionsExpenseDoesNotLeakDestination(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	now := time.Now().UTC()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('a-exp-dest', 'ws-a', 'user-a', 'a-check', 'a-check-2', 'a-expense-cat', 'EXPENSE', 5000, ?, 'Comida', 'paid', 1, 1, ?, ?)
	`, now.Unix(), now.Unix(), now.Unix())

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("comida", "EXPENSE")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions: %v", err)
	}
	if len(suggestions) == 0 {
		t.Fatal("expected at least one EXPENSE suggestion for comida")
	}
	for _, s := range suggestions {
		if s.DestinationAccountID != "" {
			t.Fatalf("EXPENSE suggestion should not have DestinationAccountID, got %q", s.DestinationAccountID)
		}
		if s.DestinationAccountName != "" {
			t.Fatalf("EXPENSE suggestion should not have DestinationAccountName, got %q", s.DestinationAccountName)
		}
	}
}

func TestPredictiveSuggestionsEmptyTypeDefaultsExpense(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	defaultType := mapTransactionType("")
	suggestions, err := handler.queryPredictiveSuggestions("restaurante", defaultType)
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions default: %v", err)
	}
	for _, s := range suggestions {
		if s.Type != "despesa" {
			t.Fatalf("empty tipo should default to EXPENSE, got %s", s.Type)
		}
	}
}

func seedPredictiveScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC()
	nowUnix := now.Unix()
	dateUnix := time.Date(now.Year(), now.Month(), 5, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'a@pred.test', 'hash', ?, ?),
			('user-b', 'User B', 'b@pred.test', 'hash', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-check', 'ws-a', 'A Checking', 'CHECKING', 50000, 50000, ?, ?),
			('a-check-2', 'ws-a', 'A Savings', 'CHECKING', 50000, 50000, ?, ?),
			('b-check', 'ws-b', 'B Checking', 'CHECKING', 50000, 50000, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('a-expense-cat', 'ws-a', 'Alimentação', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('a-income-cat', 'ws-a', 'Receita Geral', 'tag', '#6b7280', 'INCOME', 'INCOME', ?),
			('b-expense-cat', 'ws-b', 'B Expense', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)
	`, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('a-exp-1', 'ws-a', 'user-a', 'a-check', 'a-expense-cat', 'EXPENSE', 5000, ?, 'Restaurante', 'paid', 1, 1, ?, ?)
	`, dateUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('a-inc-1', 'ws-a', 'user-a', 'a-check', 'a-income-cat', 'INCOME', 15000, ?, 'Restaurante', 'paid', 1, 1, ?, ?)
	`, dateUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('a-transf-1', 'ws-a', 'user-a', 'a-check', 'a-check-2', 'TRANSFER', 3000, ?, 'Restaurante', 'paid', 1, 1, ?, ?)
	`, dateUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES ('b-exp-1', 'ws-b', 'user-b', 'b-check', 'b-expense-cat', 'EXPENSE', 9000, ?, 'Restaurante', 'paid', 1, 1, ?, ?)
	`, dateUnix, nowUnix, nowUnix)
}

func TestPredictiveSuggestionsSameNameDifferentAccounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveDedupScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("teste", "EXPENSE")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions: %v", err)
	}
	accountIDs := map[string]bool{}
	for _, s := range suggestions {
		accountIDs[s.AccountID] = true
	}
	if !accountIDs["a-check"] || !accountIDs["a-nubank"] {
		t.Fatalf("expected suggestions for both a-check and a-nubank, got %v", accountIDs)
	}
}

func TestPredictiveSuggestionsSameNameDifferentCategories(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveDedupScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("teste", "EXPENSE")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions: %v", err)
	}
	categoryIDs := map[string]bool{}
	for _, s := range suggestions {
		categoryIDs[s.CategoryID] = true
	}
	if !categoryIDs["a-mercado"] || !categoryIDs["a-restaurante"] {
		t.Fatalf("expected suggestions for both a-mercado and a-restaurante categories, got %v", categoryIDs)
	}
}

func TestPredictiveSuggestionsIdenticalCombinationDedupesLatest(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveDedupScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("teste", "EXPENSE")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions: %v", err)
	}
	count := 0
	for _, s := range suggestions {
		if s.AccountID == "a-check" && s.CategoryID == "a-mercado" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("identical combination (a-check + a-mercado) should appear once, got %d", count)
	}
}

func TestPredictiveSuggestionsTransferDifferentDestinations(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedPredictiveDedupScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-a", UserID: "user-a"}
	suggestions, err := handler.queryPredictiveSuggestions("teste", "TRANSFER")
	if err != nil {
		t.Fatalf("queryPredictiveSuggestions TRANSFER: %v", err)
	}
	destIDs := map[string]bool{}
	for _, s := range suggestions {
		destIDs[s.DestinationAccountID] = true
	}
	if !destIDs["a-nubank"] || !destIDs["a-savings"] {
		t.Fatalf("expected transfer suggestions for both destinations, got %v", destIDs)
	}
}

func seedPredictiveDedupScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC()
	nowUnix := now.Unix()
	d5 := time.Date(now.Year(), now.Month(), 5, 12, 0, 0, 0, time.UTC).Unix()
	d6 := time.Date(now.Year(), now.Month(), 6, 12, 0, 0, 0, time.UTC).Unix()
	d7 := time.Date(now.Year(), now.Month(), 7, 12, 0, 0, 0, time.UTC).Unix()
	d8 := time.Date(now.Year(), now.Month(), 8, 12, 0, 0, 0, time.UTC).Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-a', 'User A', 'a@dedup.test', 'hash', ?, ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-a', 'Workspace A', '', 'personal', ?, ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-a', 'user-a', 'ADMIN', ?)
	`, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-check', 'ws-a', 'Inter', 'CHECKING', 50000, 50000, ?, ?),
			('a-nubank', 'ws-a', 'Nubank', 'CHECKING', 50000, 50000, ?, ?),
			('a-savings', 'ws-a', 'Poupança', 'CHECKING', 50000, 50000, ?, ?)
	`, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES
			('a-mercado', 'ws-a', 'Mercado', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?),
			('a-restaurante', 'ws-a', 'Restaurante', 'tag', '#6b7280', 'EXPENSE', 'Estilo de Vida', ?)
	`, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('d-exp-1', 'ws-a', 'user-a', 'a-check', 'a-mercado', 'EXPENSE', 5000, ?, 'Teste', 'paid', 1, 1, ?, ?),
			('d-exp-2', 'ws-a', 'user-a', 'a-nubank', 'a-mercado', 'EXPENSE', 6000, ?, 'Teste', 'paid', 1, 1, ?, ?),
			('d-exp-3', 'ws-a', 'user-a', 'a-check', 'a-restaurante', 'EXPENSE', 7000, ?, 'Teste', 'paid', 1, 1, ?, ?),
			('d-exp-4', 'ws-a', 'user-a', 'a-check', 'a-mercado', 'EXPENSE', 8000, ?, 'Teste', 'paid', 1, 1, ?, ?)
	`, d5, nowUnix, nowUnix, d6, nowUnix, nowUnix, d7, nowUnix, nowUnix, d8, nowUnix, nowUnix)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES
			('d-transf-1', 'ws-a', 'user-a', 'a-check', 'a-nubank', 'TRANSFER', 3000, ?, 'Teste', 'paid', 1, 1, ?, ?),
			('d-transf-2', 'ws-a', 'user-a', 'a-check', 'a-savings', 'TRANSFER', 4000, ?, 'Teste', 'paid', 1, 1, ?, ?)
	`, d5, nowUnix, nowUnix, d6, nowUnix, nowUnix)
}
