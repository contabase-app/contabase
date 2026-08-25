package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

func TestShouldScheduleFutureFormPaymentEligibility(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	future := MustCivilWriteDate(formSchedulingDateAfterToday(t, 1))
	today := MustCivilWriteDate(civildate.CivilDateToday(appTimeLocation()).String())

	tests := []struct {
		name        string
		date        CivilWriteDate
		status      string
		trType      string
		accountType string
		want        bool
	}{
		{name: "future paid expense", date: future, status: models.PaymentStatusPaid, trType: models.TransactionTypeExpense, accountType: models.AccountTypeChecking, want: true},
		{name: "future paid income", date: future, status: models.PaymentStatusPaid, trType: models.TransactionTypeIncome, accountType: models.AccountTypeChecking, want: true},
		{name: "future pending", date: future, status: models.PaymentStatusPending, trType: models.TransactionTypeExpense, accountType: models.AccountTypeChecking},
		{name: "today paid", date: today, status: models.PaymentStatusPaid, trType: models.TransactionTypeExpense, accountType: models.AccountTypeChecking},
		{name: "future transfer", date: future, status: models.PaymentStatusPaid, trType: models.TransactionTypeTransfer, accountType: models.AccountTypeChecking},
		{name: "future card purchase", date: future, status: models.PaymentStatusPaid, trType: models.TransactionTypeExpense, accountType: models.AccountTypeCreditCard},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldScheduleFutureFormPayment(tc.date, tc.status, tc.trType, tc.accountType); got != tc.want {
				t.Fatalf("shouldScheduleFutureFormPayment() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHandleSalvarTransacaoSchedulesFuturePaidExpenseAndIncome(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedFormSchedulingScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	futureDate := formSchedulingDateAfterToday(t, 3)

	tests := []struct {
		name       string
		formType   string
		categoryID string
		modelType  string
	}{
		{name: "expense", formType: "despesa", categoryID: "cat-form-expense", modelType: models.TransactionTypeExpense},
		{name: "income", formType: "receita", categoryID: "cat-form-income", modelType: models.TransactionTypeIncome},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			description := "Form future paid " + tc.name
			rr := httptest.NewRecorder()
			handler.HandleSalvarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes", map[string]string{
				"tipo":             tc.formType,
				"valor":            "100,00",
				"descricao":        description,
				"data":             futureDate,
				"origem_conta_id":  "checking-test",
				"categoria_id":     tc.categoryID,
				"status_pagamento": "paid",
			}))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
			}

			var gotType, status string
			var scheduledAt, paidAt sql.NullInt64
			var scheduledCivil, createdBy sql.NullString
			if err := db.QueryRow(`
				SELECT type, status, scheduled_payment_at, scheduled_payment_date_civil,
				       scheduled_payment_created_by, paid_at
				FROM transactions
				WHERE workspace_id = 'ws-test' AND description = ?
			`, description).Scan(&gotType, &status, &scheduledAt, &scheduledCivil, &createdBy, &paidAt); err != nil {
				t.Fatalf("query scheduled transaction: %v", err)
			}
			if gotType != tc.modelType {
				t.Fatalf("type = %q, want %q", gotType, tc.modelType)
			}
			assertFormScheduledState(t, status, scheduledAt, scheduledCivil, createdBy, paidAt, futureDate)
		})
	}

	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test' AND workspace_id = 'ws-test'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 100000 {
		t.Fatalf("balance = %d, want unchanged 100000 before scheduled execution", balance)
	}
}

func TestHandleSalvarTransacaoKeepsFuturePendingWithoutSchedule(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedFormSchedulingScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	futureDate := formSchedulingDateAfterToday(t, 2)

	rr := httptest.NewRecorder()
	handler.HandleSalvarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes", map[string]string{
		"tipo":             "despesa",
		"valor":            "50,00",
		"descricao":        "Form future pending",
		"data":             futureDate,
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "pending",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var scheduledAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, scheduled_payment_at FROM transactions WHERE description = 'Form future pending'`).Scan(&status, &scheduledAt); err != nil {
		t.Fatal(err)
	}
	if status != models.PaymentStatusPending || scheduledAt.Valid {
		t.Fatalf("status/schedule = %q/%v, want pending without schedule", status, scheduledAt)
	}
}

func TestHandleSalvarTransacaoKeepsTodayPaidImmediate(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedFormSchedulingScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	today := civildate.CivilDateToday(appTimeLocation()).String()

	rr := httptest.NewRecorder()
	handler.HandleSalvarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes", map[string]string{
		"tipo":             "despesa",
		"valor":            "10,00",
		"descricao":        "Form today paid",
		"data":             today,
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "paid",
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var scheduledAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, scheduled_payment_at FROM transactions WHERE description = 'Form today paid'`).Scan(&status, &scheduledAt); err != nil {
		t.Fatal(err)
	}
	if status != models.PaymentStatusPaid || scheduledAt.Valid {
		t.Fatalf("status/schedule = %q/%v, want immediate paid without schedule", status, scheduledAt)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test' AND workspace_id = 'ws-test'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 99000 {
		t.Fatalf("balance = %d, want 99000", balance)
	}
}

func TestHandleAtualizarTransacaoSchedulesNewFuturePaidIntent(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedFormSchedulingScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	today := civildate.CivilDateToday(appTimeLocation()).String()
	futureDate := formSchedulingDateAfterToday(t, 4)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			date, date_civil, description, status, created_at, updated_at
		) VALUES (
			'tx-form-edit-schedule', 'ws-test', 'user-test', 'checking-test', 'cat-form-expense',
			'EXPENSE', 2500, ?, ?, 'Form edit schedule', 'pending', ?, ?
		)
	`, MustCivilWriteDate(today).LegacyUnix(), today, now, now)

	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes/tx-form-edit-schedule", map[string]string{
		"tipo":             "despesa",
		"valor":            "25,00",
		"descricao":        "Form edit schedule",
		"data":             futureDate,
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "paid",
		"escopo_edicao":    "single",
	}), "tx-form-edit-schedule")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var scheduledAt, paidAt sql.NullInt64
	var scheduledCivil, createdBy sql.NullString
	if err := db.QueryRow(`
		SELECT status, scheduled_payment_at, scheduled_payment_date_civil,
		       scheduled_payment_created_by, paid_at
		FROM transactions WHERE id = 'tx-form-edit-schedule'
	`).Scan(&status, &scheduledAt, &scheduledCivil, &createdBy, &paidAt); err != nil {
		t.Fatal(err)
	}
	assertFormScheduledState(t, status, scheduledAt, scheduledCivil, createdBy, paidAt, futureDate)
}

func TestHandleAtualizarTransacaoDoesNotRetroactivelyScheduleUnchangedFuturePaid(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedFormSchedulingScenario(t, db)
	handler := testPaidInvoiceMutationHandler(db)
	futureDate := formSchedulingDateAfterToday(t, 5)
	paidAt := MustCivilWriteDate(futureDate).LegacyUnix()
	now := time.Now().Unix()
	execTestSQL(t, db, `UPDATE accounts SET current_balance = 97500 WHERE id = 'checking-test' AND workspace_id = 'ws-test'`)
	execTestSQL(t, db, `
		INSERT INTO transactions (
			id, workspace_id, user_id, account_id, category_id, type, amount,
			date, date_civil, description, status, paid_at, paid_date_civil, created_at, updated_at
		) VALUES (
			'tx-form-existing-future-paid', 'ws-test', 'user-test', 'checking-test', 'cat-form-expense',
			'EXPENSE', 2500, ?, ?, 'Existing future paid', 'paid', ?, ?, ?, ?
		)
	`, paidAt, futureDate, paidAt, futureDate, now, now)

	rr := httptest.NewRecorder()
	handler.HandleAtualizarTransacao(rr, newMultipartUpdateRequest(t, "/transacoes/tx-form-existing-future-paid", map[string]string{
		"tipo":             "despesa",
		"valor":            "25,00",
		"descricao":        "Existing future paid edited",
		"data":             futureDate,
		"origem_conta_id":  "checking-test",
		"categoria_id":     "cat-form-expense",
		"status_pagamento": "paid",
		"escopo_edicao":    "single",
	}), "tx-form-existing-future-paid")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}

	var status string
	var scheduledAt sql.NullInt64
	var persistedPaidAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT status, scheduled_payment_at, paid_at
		FROM transactions WHERE id = 'tx-form-existing-future-paid'
	`).Scan(&status, &scheduledAt, &persistedPaidAt); err != nil {
		t.Fatal(err)
	}
	if status != models.PaymentStatusPaid || scheduledAt.Valid {
		t.Fatalf("status/schedule = %q/%v, want existing paid state preserved", status, scheduledAt)
	}
	if !persistedPaidAt.Valid || persistedPaidAt.Int64 != paidAt {
		t.Fatalf("paid_at = %v, want preserved %d", persistedPaidAt, paidAt)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = 'checking-test' AND workspace_id = 'ws-test'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 97500 {
		t.Fatalf("balance = %d, want unchanged 97500", balance)
	}
}

func seedFormSchedulingScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	seedInvoicePaymentScenario(t, db)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES
			('cat-form-expense', 'ws-test', 'Despesa Form', 'wallet', '#ef4444', 'EXPENSE', ?),
			('cat-form-income', 'ws-test', 'Receita Form', 'wallet', '#22c55e', 'INCOME', ?)
	`, now, now)
}

func formSchedulingDateAfterToday(t *testing.T, days int) string {
	t.Helper()
	today := civildate.CivilDateToday(appTimeLocation())
	date, err := civildate.CivilDateAddDays(today, days)
	if err != nil {
		t.Fatal(err)
	}
	return date.String()
}

func assertFormScheduledState(t *testing.T, status string, scheduledAt sql.NullInt64, scheduledCivil, createdBy sql.NullString, paidAt sql.NullInt64, wantDate string) {
	t.Helper()
	if status != models.PaymentStatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
	wantUnix := MustCivilWriteDate(wantDate).LegacyUnix()
	if !scheduledAt.Valid || scheduledAt.Int64 != wantUnix {
		t.Fatalf("scheduled_payment_at = %v, want %d", scheduledAt, wantUnix)
	}
	if !scheduledCivil.Valid || scheduledCivil.String != wantDate {
		t.Fatalf("scheduled_payment_date_civil = %v, want %q", scheduledCivil, wantDate)
	}
	if !createdBy.Valid || createdBy.String != "user-test" {
		t.Fatalf("scheduled_payment_created_by = %v, want user-test", createdBy)
	}
	if paidAt.Valid {
		t.Fatalf("paid_at = %v, want NULL", paidAt)
	}
}
