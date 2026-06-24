package handlers

import (
	"bytes"
	"database/sql"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRecurringProjectionNonCardRespectsDefaultPaymentStatusPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	totalOccurrences := int64(3)
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Pending",
		"",
		"",
		time.Now().UTC().AddDate(0, -1, 0).Unix(),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring pending: %v", err)
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia Pending", []string{"pending", "pending", "pending"})
}

func TestRecurringProjectionNonCardKeepsFuturePendingWhenCurrentIsPaid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	handler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}

	totalOccurrences := int64(3)
	_, err := handler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Paid",
		"",
		"",
		time.Now().UTC().AddDate(0, -1, 0).Unix(),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"paid",
		true,
		"MONTHLY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring paid: %v", err)
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia Paid", []string{"paid", "pending", "pending"})
	assertRecurringRuleDefaultStatus(t, db, "Recorrencia Paid", "PENDING")
}

func TestRecurringDailyTogglePaymentOnlySelectedOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		12000,
		"Recorrencia Diaria Toggle",
		"",
		"",
		testUnixDate("2026-07-01"),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring daily: %v", err)
	}

	ids := recurringIDsByDescription(t, db, "Recorrencia Diaria Toggle")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}

	toggleHandler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := httptest.NewRequest(http.MethodPost, "/transacoes/"+ids[0]+"/status-pagamento", nil)
	rr := httptest.NewRecorder()
	toggleHandler.HandleTogglePagamento(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle paid status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertRecurringStatusesByDescription(t, db, "Recorrencia Diaria Toggle", []string{"paid", "pending", "pending"})

	req = httptest.NewRequest(http.MethodPost, "/transacoes/"+ids[0]+"/status-pagamento", nil)
	rr = httptest.NewRecorder()
	toggleHandler.HandleTogglePagamento(rr, req, ids[0])
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle pending status = %d, body = %q", rr.Code, rr.Body.String())
	}
	assertRecurringStatusesByDescription(t, db, "Recorrencia Diaria Toggle", []string{"pending", "pending", "pending"})
}

func TestInstallmentSeriesUpdateUsesAmountPerInstallment(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		30000,
		"Serie Parcelada Base",
		"",
		"",
		testUnixDate("2026-07-05"),
		"checking-test",
		"",
		"cat-expense-rec",
		3,
		"pending",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction installment series: %v", err)
	}

	var rootID string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Serie Parcelada Base' AND installment_number = 1
	`).Scan(&rootID); err != nil {
		t.Fatalf("query root installment: %v", err)
	}
	var secondID string
	if err := db.QueryRow(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND parent_id = ? AND installment_number = 2
	`, rootID).Scan(&secondID); err != nil {
		t.Fatalf("query second installment: %v", err)
	}

	updateHandler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+secondID+"/salvar?escopo=future", map[string]string{
		"valor":            "150,00",
		"descricao":        "Serie Parcelada Atualizada",
		"data":             "2026-08-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, secondID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertInstallmentAmount(t, db, rootID, 1, 10000)
	assertInstallmentAmount(t, db, rootID, 2, 15000)
	assertInstallmentAmount(t, db, rootID, 3, 15000)
}

func TestInsertTransactionInstallmentsDistributeRemainderToFirstInstallment(t *testing.T) {
	for _, tc := range []struct {
		name       string
		txType     string
		categoryID string
	}{
		{name: "expense", txType: "EXPENSE", categoryID: "cat-expense-direct"},
		{name: "income", txType: "INCOME", categoryID: "cat-income"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			seedInvoicePaymentScenario(t, db)
			now := time.Now().Unix()
			execTestSQL(t, db, `
				INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
				VALUES
					('cat-expense-direct', 'ws-test', 'Despesa parcelada', 'receipt', '#6b7280', 'EXPENSE', ?),
					('cat-income', 'ws-test', 'Receita parcelada', 'wallet', '#22c55e', 'INCOME', ?)
			`, now, now)

			handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
			description := "Parcelamento 20000 em 3 " + tc.name
			_, err := handler.insertTransaction(
				tc.txType,
				2000000,
				description,
				"",
				"",
				testUnixDate("2026-07-05"),
				"checking-test",
				"",
				tc.categoryID,
				3,
				"pending",
				false,
				"",
				"",
				0,
				false,
				nil,
				"",
				false,
			)
			if err != nil {
				t.Fatalf("insertTransaction: %v", err)
			}

			rows, err := db.Query(`
				SELECT amount
				FROM transactions
				WHERE workspace_id = 'ws-test' AND description = ?
				ORDER BY installment_number
			`, description)
			if err != nil {
				t.Fatalf("query installments: %v", err)
			}
			defer rows.Close()

			var got []int64
			var sum int64
			for rows.Next() {
				var amount int64
				if err := rows.Scan(&amount); err != nil {
					t.Fatalf("scan installment: %v", err)
				}
				got = append(got, amount)
				sum += amount
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate installments: %v", err)
			}
			want := []int64{666668, 666666, 666666}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("installment amounts = %v, want %v", got, want)
			}
			if sum != 2000000 {
				t.Fatalf("installment sum = %d, want 2000000", sum)
			}
		})
	}
}

func TestRecurringEditBroadScopeRespondsWithListRefreshHeaders(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Mensal Refresh",
		"",
		"",
		testUnixDate("2026-07-05"),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	var firstID string
	if err := db.QueryRow(`
		SELECT id FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Recorrencia Mensal Refresh'
		ORDER BY date ASC LIMIT 1
	`).Scan(&firstID); err != nil {
		t.Fatalf("query first recurring tx: %v", err)
	}

	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Recorrencia Mensal Refresh Editada",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if rr.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("HX-Reswap = %q, want %q", rr.Header().Get("HX-Reswap"), "none")
	}
	trigger := rr.Header().Get("HX-Trigger")
	if trigger != "refreshLancamentosList" {
		t.Fatalf("HX-Trigger = %q, want %q", trigger, "refreshLancamentosList")
	}

	body := rr.Body.String()
	if !strings.Contains(body, `id="bottom-sheet-container"`) {
		t.Fatalf("body missing bottom-sheet-container OOB")
	}
	if !strings.Contains(body, `id="lancamento-form-error"`) {
		t.Fatalf("body missing lancamento-form-error OOB")
	}
}

func TestRecurringEditAllScopeUpdatesAllOccurrenceStatuses(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia All Status",
		"",
		"",
		testUnixDate("2026-07-05"),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	firstID := recurringIDsByDescription(t, db, "Recorrencia All Status")[0]
	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Recorrencia All Status",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia All Status", []string{"paid", "paid", "paid"})
	assertRecurringRuleDefaultStatus(t, db, "Recorrencia All Status", "PAID")
}

func TestRecurringEditFutureScopeDoesNotChangePastOccurrence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Future Scope",
		"",
		"",
		testUnixDate("2026-07-01"),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	ids := recurringIDsByDescription(t, db, "Recorrencia Future Scope")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}
	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+ids[1], map[string]string{
		"valor":            "110,00",
		"descricao":        "Recorrencia Future Scope Atualizada",
		"data":             "2026-07-02",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "future",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, ids[1])
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringDescriptionsAndStatuses(t, db, []struct {
		id          string
		description string
		status      string
	}{
		{ids[0], "Recorrencia Future Scope", "pending"},
		{ids[1], "Recorrencia Future Scope Atualizada", "paid"},
		{ids[2], "Recorrencia Future Scope Atualizada", "paid"},
	})
}

func TestRecurringEditAllScopeDoesNotChangeOtherWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seedOtherWorkspaceRecurringSeries(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Workspace Local",
		"",
		"",
		testUnixDate("2026-07-05"),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"DAILY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	firstID := recurringIDsByDescription(t, db, "Recorrencia Workspace Local")[0]
	updateHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "130,00",
		"descricao":        "Recorrencia Workspace Local",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "paid",
		"escopo":           "all",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	assertRecurringStatusesByDescription(t, db, "Recorrencia Workspace Local", []string{"paid", "paid", "paid"})
	assertOtherWorkspaceRecurringUnchanged(t, db)
}

func TestRecurringEditSingleScopeKeepsRowSwap(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)

	insertHandler := TransactionHandler{
		DB:          db,
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	totalOccurrences := int64(3)
	_, err := insertHandler.insertTransaction(
		"EXPENSE",
		10000,
		"Recorrencia Single Scope",
		"",
		"",
		testUnixDate("2026-07-05"),
		"checking-test",
		"",
		"cat-expense-rec",
		1,
		"pending",
		true,
		"MONTHLY",
		"",
		0,
		false,
		&totalOccurrences,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction recurring: %v", err)
	}

	var firstID string
	if err := db.QueryRow(`
		SELECT id FROM transactions
		WHERE workspace_id = 'ws-test' AND description = 'Recorrencia Single Scope'
		ORDER BY date ASC LIMIT 1
	`).Scan(&firstID); err != nil {
		t.Fatalf("query first recurring tx: %v", err)
	}
	ids := recurringIDsByDescription(t, db, "Recorrencia Single Scope")
	if len(ids) != 3 {
		t.Fatalf("ids = %v, want 3 occurrences", ids)
	}

	updateHandler := TransactionHandler{
		DB:          db,
		Templates:   testPaidInvoiceMutationTemplates(),
		WorkspaceID: "ws-test",
		UserID:      "user-test",
	}
	req := newMultipartUpdateRequest(t, "/transacoes/"+firstID, map[string]string{
		"valor":            "100,00",
		"descricao":        "Recorrencia Single Scope Editada",
		"data":             "2026-07-05",
		"tipo":             "despesa",
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-expense-rec",
		"status_pagamento": "pending",
	})
	rr := httptest.NewRecorder()
	updateHandler.HandleAtualizarTransacao(rr, req, firstID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	if rr.Header().Get("HX-Reswap") == "none" {
		t.Fatalf("single scope should NOT have HX-Reswap: none; row swap must be preserved")
	}
	if rr.Header().Get("HX-Trigger") == "refreshLancamentosList" {
		t.Fatalf("single scope should NOT trigger refreshLancamentosList")
	}

	body := rr.Body.String()
	if !strings.Contains(body, firstID) {
		t.Fatalf("single scope response body missing row ID %q in: %q", firstID, body)
	}
	assertRecurringDescriptionsAndStatuses(t, db, []struct {
		id          string
		description string
		status      string
	}{
		{ids[0], "Recorrencia Single Scope Editada", "pending"},
		{ids[1], "Recorrencia Single Scope", "pending"},
		{ids[2], "Recorrencia Single Scope", "pending"},
	})
	assertRecurringRuleDefaultStatus(t, db, "Recorrencia Single Scope", "PENDING")
}

func seedRecurringExpenseCategory(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense-rec', 'ws-test', 'Assinaturas', 'repeat', '#6b7280', 'EXPENSE', ?)
	`, now)
}

func seedOtherWorkspaceRecurringSeries(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-other', 'User Other', 'user-other@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-other', 'Workspace Other', '', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-other', 'user-other', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-other', 'ws-other', 'Conta Other', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-expense-other', 'ws-other', 'Assinaturas Other', 'repeat', '#6b7280', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
		VALUES ('rule-other', 'ws-other', 'user-other', 'checking-other', 'cat-expense-other', 'EXPENSE', 9900, 'Recorrencia Workspace Other', ?, 'DAILY', 'PENDING', 1, 3, ?, ?)
	`, testUnixDate("2026-07-05"), now, now)
	for i, id := range []string{"other-rec-1", "other-rec-2", "other-rec-3"} {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
			VALUES (?, 'ws-other', 'user-other', 'checking-other', 'cat-expense-other', 'EXPENSE', 9900, ?, 'Recorrencia Workspace Other', 'pending', 1, 1, 'rule-other', ?, ?, ?)
		`, id, testUnixDate("2026-07-05")+int64(i*86400), int64(i+1), now, now)
	}
}

func assertRecurringStatusesByDescription(t *testing.T, db *sql.DB, description string, want []string) {
	t.Helper()

	rows, err := db.Query(`
		SELECT status
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
		ORDER BY COALESCE(recurrence_sequence, 0) ASC, date ASC
	`, description)
	if err != nil {
		t.Fatalf("query recurring statuses: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatalf("scan recurring status: %v", err)
		}
		got = append(got, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recurring statuses: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("status count = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

func recurringIDsByDescription(t *testing.T, db *sql.DB, description string) []string {
	t.Helper()

	rows, err := db.Query(`
		SELECT id
		FROM transactions
		WHERE workspace_id = 'ws-test' AND description = ?
		ORDER BY COALESCE(recurrence_sequence, 0) ASC, date ASC
	`, description)
	if err != nil {
		t.Fatalf("query recurring ids: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan recurring id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate recurring ids: %v", err)
	}
	return ids
}

func assertRecurringRuleDefaultStatus(t *testing.T, db *sql.DB, description, want string) {
	t.Helper()

	var got string
	if err := db.QueryRow(`
		SELECT default_payment_status
		FROM recurring_rules
		WHERE workspace_id = 'ws-test' AND description = ?
	`, description).Scan(&got); err != nil {
		t.Fatalf("query recurring rule status: %v", err)
	}
	if got != want {
		t.Fatalf("default_payment_status = %q, want %q", got, want)
	}
}

func assertOtherWorkspaceRecurringUnchanged(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`
		SELECT amount, status
		FROM transactions
		WHERE workspace_id = 'ws-other' AND recurring_rule_id = 'rule-other'
		ORDER BY recurrence_sequence ASC
	`)
	if err != nil {
		t.Fatalf("query other workspace recurring: %v", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var amount int64
		var status string
		if err := rows.Scan(&amount, &status); err != nil {
			t.Fatalf("scan other workspace recurring: %v", err)
		}
		count++
		if amount != 9900 || status != "pending" {
			t.Fatalf("other workspace occurrence changed: amount=%d status=%q", amount, status)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate other workspace recurring: %v", err)
	}
	if count != 3 {
		t.Fatalf("other workspace occurrence count = %d, want 3", count)
	}
}

func assertRecurringDescriptionsAndStatuses(t *testing.T, db *sql.DB, want []struct {
	id          string
	description string
	status      string
}) {
	t.Helper()

	for _, item := range want {
		var gotDescription, gotStatus string
		if err := db.QueryRow(`
			SELECT description, status
			FROM transactions
			WHERE workspace_id = 'ws-test' AND id = ?
		`, item.id).Scan(&gotDescription, &gotStatus); err != nil {
			t.Fatalf("query recurring item %s: %v", item.id, err)
		}
		if gotDescription != item.description || gotStatus != item.status {
			t.Fatalf("tx %s = (%q, %q), want (%q, %q)", item.id, gotDescription, gotStatus, item.description, item.status)
		}
	}
}

func newMultipartUpdateRequest(t *testing.T, path string, fields map[string]string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func assertInstallmentAmount(t *testing.T, db *sql.DB, rootID string, installmentNumber int64, want int64) {
	t.Helper()

	var amount int64
	if err := db.QueryRow(`
		SELECT amount
		FROM transactions
		WHERE workspace_id = 'ws-test'
		  AND (id = ? OR parent_id = ?)
		  AND installment_number = ?
	`, rootID, rootID, installmentNumber).Scan(&amount); err != nil {
		t.Fatalf("query installment %d: %v", installmentNumber, err)
	}
	if amount != want {
		t.Fatalf("installment %d amount = %d, want %d", installmentNumber, amount, want)
	}
}
