package handlers

import (
	"context"
	"database/sql"
	"testing"
)

func TestInvoicePaymentTemporalHistoryCashExtratoAndAccountClosingT0T4(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	paidAt := reversalUnix("2026-08-05", 10)
	reversedAt := reversalUnix("2026-08-10", 15)
	const amount = int64(10000)
	const initialBalance = int64(100000)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: amount,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: amount, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: amount, InitialBalance: initialBalance,
	})

	command := reversalCommand("ip-a")
	command.ReversedAt = reversedAt
	if _, err := reverseInvoicePayment(db, command); err != nil {
		t.Fatalf("reverse invoice payment: %v", err)
	}

	snapshot, err := loadInvoiceFinancialSnapshotDB(db, reversalWorkspaceID, reversalInvoiceID)
	if err != nil {
		t.Fatalf("load CURRENT invoice snapshot after reversal: %v", err)
	}
	if snapshot.Total != amount || snapshot.TotalPaymentRecordCount != 1 || snapshot.ActivePaymentSum != 0 || snapshot.EffectivePaid != 0 {
		t.Fatalf("CURRENT snapshot after reversal = total:%d lineage:%d active:%d effective:%d, want %d/1/0/0", snapshot.Total, snapshot.TotalPaymentRecordCount, snapshot.ActivePaymentSum, snapshot.EffectivePaid, amount)
	}
	projectedPeriod := NewReportPeriodFromMonth(2026, 9)
	projected, err := QueryInvoiceProjectedCashEvents(db, reversalWorkspaceID, projectedPeriod)
	if err != nil {
		t.Fatalf("query CURRENT projected invoice after reversal: %v", err)
	}
	if len(projected) != 1 || projected[0].AmountCents != amount {
		t.Fatalf("CURRENT projected invoice after reversal = %#v, want one event of %d", projected, amount)
	}

	tests := []struct {
		name        string
		cutoff      int64
		wantBalance int64
		wantRows    int
		wantNet     int64
		wantIncome  int
		wantExpense int
	}{
		{name: "T0 before payment", cutoff: paidAt - 1, wantBalance: initialBalance, wantRows: 0, wantNet: 0, wantIncome: 0, wantExpense: 0},
		{name: "T1 at payment", cutoff: paidAt, wantBalance: initialBalance - amount, wantRows: 1, wantNet: -amount, wantIncome: 0, wantExpense: 1},
		{name: "T2 between payment and reversal", cutoff: reversedAt - 1, wantBalance: initialBalance - amount, wantRows: 1, wantNet: -amount, wantIncome: 0, wantExpense: 1},
		{name: "T3 at reversal", cutoff: reversedAt, wantBalance: initialBalance, wantRows: 2, wantNet: 0, wantIncome: 1, wantExpense: 1},
		{name: "T4 after reversal", cutoff: reversedAt + 1, wantBalance: initialBalance, wantRows: 2, wantNet: 0, wantIncome: 1, wantExpense: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			period := ReportPeriod{Start: paidAt - 1, End: tt.cutoff}
			cash, err := BuildRealizadoCashFlowData(db, reversalWorkspaceID, period)
			if err != nil {
				t.Fatalf("build realized cash: %v", err)
			}
			net, err := SumCashEvents(cash)
			if err != nil {
				t.Fatalf("sum realized cash: %v", err)
			}
			if len(cash) != tt.wantRows || net != tt.wantNet {
				t.Fatalf("cash rows/net = %d/%d, want %d/%d", len(cash), net, tt.wantRows, tt.wantNet)
			}
			if income, expense := cashDirectionCounts(cash); income != tt.wantIncome || expense != tt.wantExpense {
				t.Fatalf("cash income/expense = %d/%d, want %d/%d", income, expense, tt.wantIncome, tt.wantExpense)
			}

			extrato, err := BuildExtratoData(db, reversalWorkspaceID, period)
			if err != nil {
				t.Fatalf("build extrato: %v", err)
			}
			if len(extrato) != tt.wantRows {
				t.Fatalf("extrato rows = %d, want %d", len(extrato), tt.wantRows)
			}
			if got := extratoNet(extrato); got != tt.wantNet {
				t.Fatalf("extrato net = %d, want %d", got, tt.wantNet)
			}
			if income, expense := extratoDirectionCounts(extrato); income != tt.wantIncome || expense != tt.wantExpense {
				t.Fatalf("extrato income/expense = %d/%d, want %d/%d", income, expense, tt.wantIncome, tt.wantExpense)
			}

			balances, err := BuildAccountClosingBalances(db, reversalWorkspaceID, tt.cutoff)
			if err != nil {
				t.Fatalf("build account closing: %v", err)
			}
			if got := temporalClosingBalance(t, balances, reversalPaymentAccountID); got != tt.wantBalance {
				t.Fatalf("closing balance = %d, want %d", got, tt.wantBalance)
			}
		})
	}
}

func TestInvoicePaymentTemporalHistorySameSecondUsesPostReversalEquality(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	instant := reversalUnix("2026-08-05", 10)
	const amount = int64(10000)
	const initialBalance = int64(100000)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: amount,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: amount, PaidAt: instant, CreatedAt: instant,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &instant,
		LegacyPaid: amount, InitialBalance: initialBalance,
	})

	command := reversalCommand("ip-a")
	command.ReversedAt = instant
	command.ReversedDateCivil = "2026-08-05"
	if _, err := reverseInvoicePayment(db, command); err != nil {
		t.Fatalf("reverse invoice payment in same second: %v", err)
	}

	period := ReportPeriod{Start: instant, End: instant}
	cash, err := BuildRealizadoCashFlowData(db, reversalWorkspaceID, period)
	if err != nil {
		t.Fatalf("build same-second cash: %v", err)
	}
	cashNet, err := SumCashEvents(cash)
	if err != nil {
		t.Fatalf("sum same-second cash: %v", err)
	}
	if len(cash) != 2 || cashNet != 0 {
		t.Fatalf("same-second cash rows/net = %d/%d, want 2/0", len(cash), cashNet)
	}
	if income, expense := cashDirectionCounts(cash); income != 1 || expense != 1 {
		t.Fatalf("same-second cash income/expense = %d/%d, want 1/1", income, expense)
	}
	extrato, err := BuildExtratoData(db, reversalWorkspaceID, period)
	if err != nil {
		t.Fatalf("build same-second extrato: %v", err)
	}
	if len(extrato) != 2 || extratoNet(extrato) != 0 {
		t.Fatalf("same-second extrato rows/net = %d/%d, want 2/0", len(extrato), extratoNet(extrato))
	}
	if income, expense := extratoDirectionCounts(extrato); income != 1 || expense != 1 {
		t.Fatalf("same-second extrato income/expense = %d/%d, want 1/1", income, expense)
	}
	balances, err := BuildAccountClosingBalances(db, reversalWorkspaceID, instant)
	if err != nil {
		t.Fatalf("build same-second account closing: %v", err)
	}
	if got := temporalClosingBalance(t, balances, reversalPaymentAccountID); got != initialBalance {
		t.Fatalf("same-second closing balance = %d, want post-reversal %d", got, initialBalance)
	}
}

func TestInvoicePaymentPastCaixaAllocationSurvivesLaterReversal(t *testing.T) {
	tests := []struct {
		name         string
		invoiceTotal int64
		payments     []int64
	}{
		{name: "multiple payments 40 plus 60", invoiceTotal: 10000, payments: []int64{4000, 6000}},
		{name: "overpayment 80 plus 40", invoiceTotal: 10000, payments: []int64{8000, 4000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			paidAt := reversalUnix("2026-08-05", 10)
			seeds := make([]reversalPaymentSeed, 0, len(tt.payments))
			var want int64
			for i, amount := range tt.payments {
				id := string(rune('a' + i))
				seeds = append(seeds, reversalPaymentSeed{
					ID: "ip-" + id, TxID: "payment-" + id, Amount: amount,
					PaidAt: paidAt, CreatedAt: paidAt + int64(i),
				})
				want += amount
			}
			invoicePaidAt := paidAt
			seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
				InvoiceTotal: tt.invoiceTotal, Payments: seeds,
				ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &invoicePaidAt,
				LegacyPaid: want, InitialBalance: 200000,
			})
			for i, payment := range seeds {
				command := reversalCommand(payment.ID)
				command.ReversedAt += int64(i)
				if _, err := reverseInvoicePayment(db, command); err != nil {
					t.Fatalf("reverse %s: %v", payment.ID, err)
				}
			}

			h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: reversalWorkspaceID}
			start, end := paidAt-1, paidAt+1
			targets, err := h.queryInvoiceAllocationTargets(context.Background(), start, end, "paid")
			if err != nil {
				t.Fatalf("query paid allocation targets: %v", err)
			}
			if got := targets[reversalInvoiceID]; got != want {
				t.Fatalf("historical allocation target = %d, want %d", got, want)
			}
			rows, err := h.queryCaixaExpenseAnalysisRows(context.Background(), start, end, "realizado")
			if err != nil {
				t.Fatalf("query caixa analysis: %v", err)
			}
			var allocated int64
			for _, row := range rows {
				allocated += row.Amount
			}
			if allocated != want {
				t.Fatalf("historical allocated amount = %d, want %d", allocated, want)
			}

			caixaRows, err := BuildCaixaData(db, reversalWorkspaceID, 2026, 8)
			if err != nil {
				t.Fatalf("build caixa forecast: %v", err)
			}
			if got, err := SumCaixaByMacroGroup(caixaRows, "Fatura de cartão"); err != nil || got != -want {
				t.Fatalf("historical invoice-payment macro = %d, want %d", got, -want)
			}
		})
	}
}

func TestInvoicePaymentReversalExcludedFromCompetenceConsumers(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: 10000,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 10000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 10000, InitialBalance: 100000,
	})
	if _, err := reverseInvoicePayment(db, reversalCommand("ip-a")); err != nil {
		t.Fatalf("reverse invoice payment: %v", err)
	}

	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: reversalWorkspaceID}
	start := reversalUnix("2026-08-01", 0)
	end := reversalUnix("2026-08-31", 23)
	revenues, err := h.queryRevenueByCategory(context.Background(), start, end)
	if err != nil {
		t.Fatalf("query revenue by category: %v", err)
	}
	if len(revenues) != 0 {
		t.Fatalf("reversal compensation appeared in competence revenue categories: %#v", revenues)
	}
	macros, err := h.queryContabilReceitasByMacro(context.Background(), start, end)
	if err != nil {
		t.Fatalf("query accounting revenue macros: %v", err)
	}
	if len(macros) != 0 {
		t.Fatalf("reversal compensation appeared in accounting revenue macros: %#v", macros)
	}
}

func TestInvoicePaymentTemporalHistoryWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: 10000,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 10000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 10000, InitialBalance: 100000,
	})
	if _, err := reverseInvoicePayment(db, reversalCommand("ip-a")); err != nil {
		t.Fatalf("reverse invoice payment: %v", err)
	}
	seedForeignReversedInvoicePayment(t, db, paidAt, reversalCommand("ip-a").ReversedAt)

	period := ReportPeriod{Start: paidAt - 1, End: reversalCommand("ip-a").ReversedAt}
	events, err := QueryInvoiceCashEvents(db, reversalWorkspaceID, period)
	if err != nil {
		t.Fatalf("query invoice cash events: %v", err)
	}
	eventNet, err := SumCashEvents(events)
	if err != nil {
		t.Fatalf("sum workspace cash events: %v", err)
	}
	if len(events) != 2 || eventNet != 0 {
		t.Fatalf("workspace cash events rows/net = %d/%d, want 2/0", len(events), eventNet)
	}
	h := RelatoriosHandler{ActorRole: testMetasAdminRole, DB: db, WorkspaceID: reversalWorkspaceID}
	targets, err := h.queryInvoiceAllocationTargets(context.Background(), paidAt-1, paidAt+1, "paid")
	if err != nil {
		t.Fatalf("query allocation targets: %v", err)
	}
	if len(targets) != 1 || targets[reversalInvoiceID] != 10000 {
		t.Fatalf("workspace allocation targets = %#v, want only local invoice", targets)
	}
}

func extratoNet(rows []StatementExtratoRow) int64 {
	var total int64
	for _, row := range rows {
		switch row.Tipo {
		case "INCOME":
			total += row.AmountCents
		case "EXPENSE":
			total -= row.AmountCents
		}
	}
	return total
}

func cashDirectionCounts(events []CashEvent) (income, expense int) {
	for _, event := range events {
		if event.IsIncome {
			income++
		} else {
			expense++
		}
	}
	return income, expense
}

func extratoDirectionCounts(rows []StatementExtratoRow) (income, expense int) {
	for _, row := range rows {
		switch row.Tipo {
		case "INCOME":
			income++
		case "EXPENSE":
			expense++
		}
	}
	return income, expense
}

func temporalClosingBalance(t *testing.T, balances []AccountClosingBalance, accountID string) int64 {
	t.Helper()
	for _, balance := range balances {
		if balance.ID == accountID {
			return balance.Balance
		}
	}
	t.Fatalf("account %q absent from closing balances", accountID)
	return 0
}

func seedForeignReversedInvoicePayment(t *testing.T, db *sql.DB, paidAt, reversedAt int64) {
	t.Helper()
	const workspaceID = "ws-temporal-foreign"
	const userID = "user-temporal-foreign"
	const checkingID = "checking-temporal-foreign"
	const cardID = "card-temporal-foreign"
	const invoiceID = "invoice-temporal-foreign"

	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, 'Foreign', 'foreign-temporal@example.com', 'hash', ?, ?)`, userID, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, created_at, updated_at) VALUES (?, 'Foreign', ?, ?)`, workspaceID, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, workspaceID, userID, paidAt)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Foreign Checking', 'CHECKING', 100000, 100000, ?, ?)`, checkingID, workspaceID, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Foreign Card', 'CREDIT_CARD', 0, 0, ?, ?)`, cardID, workspaceID, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-temporal-foreign', ?, 20, 10, 500000)`, cardID)
	execTestSQL(t, db, `INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at) VALUES (?, ?, '2026-08', ?, ?, 'OPEN', ?)`, invoiceID, cardID, paidAt, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, paid_at, created_at, updated_at) VALUES ('purchase-temporal-foreign', ?, ?, ?, ?, 'EXPENSE', 90000, ?, 'Foreign purchase', 'paid', ?, ?, ?)`, workspaceID, userID, cardID, invoiceID, paidAt, paidAt, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at) VALUES ('payment-temporal-foreign', ?, ?, ?, 'EXPENSE', 90000, ?, 'Foreign payment', 'paid', ?, ?, ?)`, workspaceID, userID, checkingID, paidAt, paidAt, paidAt, paidAt)
	execTestSQL(t, db, `INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, paid_at, created_at, updated_at) VALUES ('reversal-temporal-foreign', ?, ?, ?, 'INCOME', 90000, ?, ?, 'paid', ?, ?, ?)`, workspaceID, userID, checkingID, reversedAt, invoicePaymentReversalDescription, reversedAt, reversedAt, reversedAt)
	execTestSQL(t, db, `INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, reversed_at, reversal_transaction_id, created_by, created_at) VALUES ('ip-temporal-foreign', ?, ?, ?, 'payment-temporal-foreign', 90000, ?, 'manual', ?, 'reversal-temporal-foreign', ?, ?)`, workspaceID, invoiceID, checkingID, paidAt, reversedAt, userID, paidAt)
}
