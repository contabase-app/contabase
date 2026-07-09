package handlers

import (
	"database/sql"
	"testing"
	"time"
)

func seedRecurringSeriesInfiniteChecking(t *testing.T, db *sql.DB, description, startDate string) (ruleID, seriesID string) {
	t.Helper()
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	ruleID = "rule-mat-inf"
	seriesID = "series-mat-inf"
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, NULL, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PENDING', 1, NULL, ?, 1, ?, ?)
	`, ruleID, description, testUnixDate(startDate), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-inf-1', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 1, ?, ?, ?)
	`, testUnixDate(startDate), description, ruleID, seriesID, now, now)
	return ruleID, seriesID
}

func seedRecurringSeriesFiniteChecking(t *testing.T, db *sql.DB, description, startDate string, total int64) (ruleID, seriesID string) {
	t.Helper()
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	ruleID = "rule-mat-fin"
	seriesID = "series-mat-fin"
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, ?, ?, ?)
	`, seriesID, total, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PENDING', 1, ?, ?, 1, ?, ?)
	`, ruleID, description, testUnixDate(startDate), total, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-fin-1', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 1, ?, ?, ?)
	`, testUnixDate(startDate), description, ruleID, seriesID, now, now)
	return ruleID, seriesID
}

func runMaterializer(t *testing.T, db *sql.DB, horizonDate time.Time) {
	t.Helper()
	if err := EnsureRecurringMaterialized(db, "ws-test", horizonDate); err != nil {
		t.Fatalf("EnsureRecurringMaterialized: %v", err)
	}
}

func TestEnsureRecurringMaterializedInfiniteMonthly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count = %d, want 16 (jun/26..set/27)", count)
	}
	assertDates(t, db, desc, []string{
		"2026-06-05", "2026-07-05", "2026-08-05", "2026-09-05", "2026-10-05", "2026-11-05",
		"2026-12-05", "2027-01-05", "2027-02-05", "2027-03-05", "2027-04-05", "2027-05-05",
		"2027-06-05", "2027-07-05", "2027-08-05", "2027-09-05",
	})

	txID, gotRuleID, _, _ := recurringTxRuleBySequence(t, db, seriesID, 5)
	if gotRuleID != ruleID {
		t.Fatalf("seq 5 recurring_rule_id = %q, want %q (tx=%s)", gotRuleID, ruleID, txID)
	}
}

func TestEnsureRecurringMaterializedIdempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	_, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count after second run = %d, want 16", count)
	}
}

func TestEnsureRecurringMaterializedFifteenMonthWindow(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	_, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count = %d, want 16 for 15 month window", count)
	}

	horizon2 := time.Date(2027, 10, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon2)

	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx after extended horizon: %v", err)
	}
	if count != 17 {
		t.Fatalf("tx count after extending horizon = %d, want 17", count)
	}
}

func TestEnsureRecurringMaterializedTotalOccurrences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	_, seriesID := seedRecurringSeriesFiniteChecking(t, db, desc, "2026-06-05", 5)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 5 {
		t.Fatalf("tx count = %d, want 5 (total_occurrences limit)", count)
	}
}

func TestEnsureRecurringMaterializedEffectiveEndSequence(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 4 WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 4 {
		t.Fatalf("tx count = %d, want 4 (effective_end_sequence limit)", count)
	}
}

func TestEnsureRecurringMaterializedEndDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	// end_date no meio do horizon: 2026-09-05. Espera-se seq 1..4
	// (jun, jul, ago, set/2026). Ocorrência de out/2026 já ultrapassa end_date.
	execTestSQL(t, db, `UPDATE recurring_rules SET end_date = ? WHERE id = ? AND workspace_id = 'ws-test'`, testUnixDate("2026-09-05"), ruleID)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 4 {
		t.Fatalf("tx count = %d, want 4 (end_date limit)", count)
	}
	assertDates(t, db, desc, []string{"2026-06-05", "2026-07-05", "2026-08-05", "2026-09-05"})
}

func TestEnsureRecurringMaterializedRuleTotalOccurrences(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	// Série infinita (series.total_occurrences NULL), mas a regra define
	// total_occurrences = 3. O motor deve respeitar como limite adicional.
	execTestSQL(t, db, `UPDATE recurring_rules SET total_occurrences = 3 WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 3 {
		t.Fatalf("tx count = %d, want 3 (recurring_rules.total_occurrences limit)", count)
	}
}

func TestEnsureRecurringMaterializedSmallestTotalOccurrencesWins(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	// Série define total_occurrences = 5; regra define total_occurrences = 2.
	// Vence o menor limite (2).
	ruleID, seriesID := seedRecurringSeriesFiniteChecking(t, db, desc, "2026-06-05", 5)
	execTestSQL(t, db, `UPDATE recurring_rules SET total_occurrences = 2 WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 2 {
		t.Fatalf("tx count = %d, want 2 (smallest total_occurrences wins)", count)
	}
}

func TestEnsureRecurringMaterializedSeriesTotalOccurrencesWinsWhenSmaller(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	// Série define total_occurrences = 3; regra define total_occurrences = 6.
	// Vence o menor limite (3, da série).
	ruleID, seriesID := seedRecurringSeriesFiniteChecking(t, db, desc, "2026-06-05", 3)
	execTestSQL(t, db, `UPDATE recurring_rules SET total_occurrences = 6 WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 3 {
		t.Fatalf("tx count = %d, want 3 (series total_occurrences wins)", count)
	}
}

func TestEnsureRecurringMaterializedChainABC(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const descA = "Assinatura A"
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seriesID := "series-mat-abc"
	ruleA := "rule-mat-a"
	ruleB := "rule-mat-b"
	ruleC := "rule-mat-c"

	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, NULL, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, effective_end_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 10000, ?, ?, 'MONTHLY', 'PENDING', 1, NULL, ?, NULL, 1, 3, ?, ?)
	`, ruleA, descA, testUnixDate("2026-06-05"), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 20000, ?, ?, 'MONTHLY', 'PENDING', 1, NULL, ?, ?, 4, ?, ?)
	`, ruleB, "Assinatura B", testUnixDate("2026-09-05"), seriesID, ruleA, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, parent_rule_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 30000, ?, ?, 'MONTHLY', 'PENDING', 1, NULL, ?, ?, 7, ?, ?)
	`, ruleC, "Assinatura C", testUnixDate("2026-12-05"), seriesID, ruleB, now, now)

	horizon := time.Date(2027, 5, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	want := map[int64]string{
		1: ruleA,
		2: ruleA,
		3: ruleA,
		4: ruleB,
		5: ruleB,
		6: ruleB,
		7: ruleC,
		8: ruleC,
		9: ruleC,
	}
	for seq, wantRule := range want {
		_, gotRuleID, _, amount := recurringTxRuleBySequence(t, db, seriesID, seq)
		if gotRuleID != wantRule {
			t.Fatalf("seq %d recurring_rule_id = %q, want %q", seq, gotRuleID, wantRule)
		}
		switch seq {
		case 1, 2, 3:
			if amount != 10000 {
				t.Fatalf("seq %d amount = %d, want 10000", seq, amount)
			}
		case 4, 5, 6:
			if amount != 20000 {
				t.Fatalf("seq %d amount = %d, want 20000", seq, amount)
			}
		case 7, 8, 9:
			if amount != 30000 {
				t.Fatalf("seq %d amount = %d, want 30000", seq, amount)
			}
		}
	}
}

func TestEnsureRecurringMaterializedDeleteFutureDoesNotRegenerate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	// Cenário realista de delete future a partir da sequência 2: a ocorrência 1
	// permanece, a regra é encerrada (effective_end_sequence=1, active=0), mas a
	// recurring_series continua ATIVA. O materializador não deve regenerar nada
	// porque não há regra ativa cobrindo sequências futuras.
	execTestSQL(t, db, `UPDATE recurring_rules SET effective_end_sequence = 1, active = 0 WHERE id = ? AND workspace_id = 'ws-test'`, ruleID)

	var seriesActive int
	if err := db.QueryRow(`SELECT active FROM recurring_series WHERE id = ? AND workspace_id = 'ws-test'`, seriesID).Scan(&seriesActive); err != nil {
		t.Fatalf("query series active: %v", err)
	}
	if seriesActive != 1 {
		t.Fatalf("series active = %d, want 1 (delete future keeps series active)", seriesActive)
	}

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 1 {
		t.Fatalf("tx count = %d, want 1 (delete future prevented regeneration)", count)
	}
}

func TestEnsureRecurringMaterializedPreservesManualException(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, is_recurring_exception, recurring_exception_reason, created_at, updated_at)
		VALUES ('tx-mat-exc-2', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 9999, ?, 'Assinatura Materializador Manual', 'pending', 1, 1, ?, 2, ?, 1, 'manual_edit', ?, ?)
	`, testUnixDate("2026-07-05"), ruleID, seriesID, time.Now().Unix(), time.Now().Unix())

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var desc2 string
	var amount int64
	if err := db.QueryRow(`SELECT description, amount FROM transactions WHERE id = 'tx-mat-exc-2' AND workspace_id = 'ws-test'`).Scan(&desc2, &amount); err != nil {
		t.Fatalf("query exception tx: %v", err)
	}
	if desc2 != "Assinatura Materializador Manual" || amount != 9999 {
		t.Fatalf("exception tx altered: desc=%q amount=%d", desc2, amount)
	}
}

func TestEnsureRecurringMaterializedPreservesPaidWithoutInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, paid_at, created_at, updated_at)
		VALUES ('tx-mat-paid-2', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'paid', 1, 1, ?, 2, ?, ?, ?, ?)
	`, testUnixDate("2026-07-05"), desc, ruleID, seriesID, time.Now().Unix(), time.Now().Unix(), time.Now().Unix())

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var status string
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_at FROM transactions WHERE id = 'tx-mat-paid-2' AND workspace_id = 'ws-test'`).Scan(&status, &paidAt); err != nil {
		t.Fatalf("query paid tx: %v", err)
	}
	if status != "paid" || !paidAt.Valid {
		t.Fatalf("paid tx altered: status=%q paid_at=%v", status, paidAt)
	}
}

func TestEnsureRecurringMaterializedPreservesClosedAndPaidInvoices(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES ('invoice-mat-closed', 'card-test', '2026-07', ?, ?, 'CLOSED', ?)
	`, testUnixDate("2026-06-20"), testUnixDate("2026-07-10"), time.Now().Unix())
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-closed-2', 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'invoice-mat-closed', 'EXPENSE', 15000, ?, ?, 'paid', 1, 1, ?, 2, ?, ?, ?)
	`, testUnixDate("2026-07-05"), desc, ruleID, seriesID, time.Now().Unix(), time.Now().Unix())

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var invoiceStatus string
	if err := db.QueryRow(`SELECT status FROM invoices WHERE id = 'invoice-mat-closed' AND account_id = 'card-test'`).Scan(&invoiceStatus); err != nil {
		t.Fatalf("query invoice: %v", err)
	}
	if invoiceStatus != "CLOSED" {
		t.Fatalf("invoice status = %q, want CLOSED", invoiceStatus)
	}
	var txStatus string
	if err := db.QueryRow(`SELECT status FROM transactions WHERE id = 'tx-mat-closed-2' AND workspace_id = 'ws-test'`).Scan(&txStatus); err != nil {
		t.Fatalf("query tx: %v", err)
	}
	if txStatus != "paid" {
		t.Fatalf("tx status = %q, want paid", txStatus)
	}
}

func TestEnsureRecurringMaterializedCreditCardResolvesInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Cartao"
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seriesID := "series-mat-card"
	ruleID := "rule-mat-card"

	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, NULL, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PAID', 1, NULL, ?, 1, ?, ?)
	`, ruleID, desc, testUnixDate("2026-06-24"), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-card-1', 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'invoice-2026-08', 'EXPENSE', 15000, ?, ?, 'paid', 1, 1, ?, 1, ?, ?, ?)
	`, testUnixDate("2026-06-24"), desc, ruleID, seriesID, now, now)

	horizon := time.Date(2027, 9, 24, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count = %d, want 16", count)
	}

	var withoutInvoice int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ? AND invoice_id IS NULL`, seriesID).Scan(&withoutInvoice); err != nil {
		t.Fatalf("count without invoice: %v", err)
	}
	if withoutInvoice != 0 {
		t.Fatalf("found %d card occurrences without invoice", withoutInvoice)
	}
}

func TestEnsureRecurringMaterializedIdempotentOnOpenInvoice(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Cartao"
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	seriesID := "series-mat-card-open"
	ruleID := "rule-mat-card-open"

	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, NULL, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PAID', 1, NULL, ?, 1, ?, ?)
	`, ruleID, desc, testUnixDate("2026-06-24"), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-card-open-1', 'ws-test', 'user-test', 'card-test', 'cat-expense-rec', 'invoice-2026-08', 'EXPENSE', 15000, ?, ?, 'paid', 1, 1, ?, 1, ?, ?, ?)
	`, testUnixDate("2026-06-24"), desc, ruleID, seriesID, now, now)

	horizon := time.Date(2027, 9, 24, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count = %d, want 16 after idempotent run", count)
	}
}

func TestEnsureRecurringMaterializedBusinessPreservesContactAndDueDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Business"
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	execTestSQL(t, db, `UPDATE workspaces SET type = 'business' WHERE id = 'ws-test'`)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-mat', 'ws-test', 'Cliente Mat', 'client', ?)`, now)

	seriesID := "series-mat-business"
	ruleID := "rule-mat-business"
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, NULL, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, contact_id, due_date_offset_days, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PENDING', 1, NULL, ?, 'contact-mat', 10, 1, ?, ?)
	`, ruleID, desc, testUnixDate("2026-06-05"), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-bus-1', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 1, ?, ?, ?)
	`, testUnixDate("2026-06-05"), desc, ruleID, seriesID, now, now)

	horizon := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var dueDate sql.NullInt64
	var contactID sql.NullString
	if err := db.QueryRow(`SELECT due_date, contact_id FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ? AND recurrence_sequence = 2`, seriesID).Scan(&dueDate, &contactID); err != nil {
		t.Fatalf("query seq 2: %v", err)
	}
	if !dueDate.Valid || dueDate.Int64 != testUnixDate("2026-07-15") {
		t.Fatalf("due_date = %v, want 2026-07-15", dueDate)
	}
	if !contactID.Valid || contactID.String != "contact-mat" {
		t.Fatalf("contact_id = %v, want contact-mat", contactID)
	}
}

func TestEnsureRecurringMaterializedPersonalKeepsContactNull(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Personal"
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	execTestSQL(t, db, `INSERT INTO contacts (id, workspace_id, name, type, created_at) VALUES ('contact-mat-personal', 'ws-test', 'Cliente Mat', 'client', ?)`, now)

	seriesID := "series-mat-personal"
	ruleID := "rule-mat-personal"
	execTestSQL(t, db, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 1, NULL, ?, ?)
	`, seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, series_id, contact_id, due_date_offset_days, effective_start_sequence, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'MONTHLY', 'PENDING', 1, NULL, ?, 'contact-mat-personal', 10, 1, ?, ?)
	`, ruleID, desc, testUnixDate("2026-06-05"), seriesID, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-per-1', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 1, ?, ?, ?)
	`, testUnixDate("2026-06-05"), desc, ruleID, seriesID, now, now)

	horizon := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var dueDate sql.NullInt64
	var contactID sql.NullString
	if err := db.QueryRow(`SELECT due_date, contact_id FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ? AND recurrence_sequence = 2`, seriesID).Scan(&dueDate, &contactID); err != nil {
		t.Fatalf("query seq 2: %v", err)
	}
	if dueDate.Valid {
		t.Fatalf("due_date = %v, want NULL for personal", dueDate)
	}
	if contactID.Valid {
		t.Fatalf("contact_id = %v, want NULL for personal", contactID)
	}
}

func TestEnsureRecurringMaterializedDoesNotCrossWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	_, _ = seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	seedOtherWorkspaceRecurringSeries(t, db)

	var otherBefore int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-other'`).Scan(&otherBefore); err != nil {
		t.Fatalf("count other workspace before: %v", err)
	}

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var otherAfter int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-other'`).Scan(&otherAfter); err != nil {
		t.Fatalf("count other workspace after: %v", err)
	}
	if otherAfter != otherBefore {
		t.Fatalf("other workspace tx count changed from %d to %d", otherBefore, otherAfter)
	}
}

func TestEnsureRecurringMaterializedBackfillsLegacyRuleWithoutSeries(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().Unix()
	seedInvoicePaymentScenario(t, db)
	seedRecurringExpenseCategory(t, db)
	ruleID := "rule-mat-legacy"
	execTestSQL(t, db, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
		VALUES (?, 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, 'Assinatura Legacy', ?, 'MONTHLY', 'PENDING', 1, NULL, ?, ?)
	`, ruleID, testUnixDate("2026-06-05"), now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, created_at, updated_at)
		VALUES ('tx-mat-legacy-1', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, 'Assinatura Legacy', 'pending', 1, 1, ?, 1, ?, ?)
	`, testUnixDate("2026-06-05"), ruleID, now, now)

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var seriesID sql.NullString
	if err := db.QueryRow(`SELECT series_id FROM recurring_rules WHERE id = ? AND workspace_id = 'ws-test'`, ruleID).Scan(&seriesID); err != nil {
		t.Fatalf("query rule series: %v", err)
	}
	if !seriesID.Valid || seriesID.String == "" {
		t.Fatalf("legacy rule series_id not backfilled")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID.String).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count = %d, want 16 after legacy backfill", count)
	}
}

func TestEnsureRecurringMaterializedFillsGapOnly(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesInfiniteChecking(t, db, desc, "2026-06-05")
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-gap-5', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 5, ?, ?, ?)
	`, testUnixDate("2026-10-05"), desc, ruleID, seriesID, now, now)

	horizon := time.Date(2027, 9, 5, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 16 {
		t.Fatalf("tx count = %d, want 16", count)
	}
}

func TestEnsureRecurringMaterializedPartiallyMaterializedDoesNotDuplicate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	const desc = "Assinatura Materializador"
	ruleID, seriesID := seedRecurringSeriesFiniteChecking(t, db, desc, "2026-06-05", 5)
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, installment_number, total_installments, recurring_rule_id, recurrence_sequence, series_id, created_at, updated_at)
		VALUES ('tx-mat-part-2', 'ws-test', 'user-test', 'checking-test', 'cat-expense-rec', 'EXPENSE', 15000, ?, ?, 'pending', 1, 1, ?, 2, ?, ?, ?)
	`, testUnixDate("2026-07-05"), desc, ruleID, seriesID, now, now)

	horizon := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)
	runMaterializer(t, db, horizon)

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND series_id = ?`, seriesID).Scan(&count); err != nil {
		t.Fatalf("count series tx: %v", err)
	}
	if count != 5 {
		t.Fatalf("tx count = %d, want 5", count)
	}
}
