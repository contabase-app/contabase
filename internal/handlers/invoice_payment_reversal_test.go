package handlers

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
)

const (
	reversalWorkspaceID      = "ws-reversal"
	reversalUserID           = "user-reversal"
	reversalPaymentAccountID = "checking-reversal"
	reversalCardAccountID    = "card-reversal"
	reversalInvoiceID        = "invoice-reversal"
)

type reversalPaymentSeed struct {
	ID        string
	TxID      string
	Amount    int64
	PaidAt    int64
	CreatedAt int64
}

type reversalFixtureOptions struct {
	InvoiceTotal   int64
	Payments       []reversalPaymentSeed
	ClosingCivil   string
	InvoiceStatus  string
	InvoicePaidAt  *int64
	LegacyPaid     int64
	InitialBalance int64
}

type reversalState struct {
	Balance                 int64
	InvoiceStatus           string
	InvoicePaidAmount       int64
	InvoicePaidAt           sql.NullInt64
	PaymentReversedAt       sql.NullInt64
	PaymentReversedDate     sql.NullString
	PaymentReversedBy       sql.NullString
	PaymentReason           sql.NullString
	PaymentReversalTxID     sql.NullString
	TransactionCount        int
	CompensationCount       int
	OriginalCount           int
	OriginalType            string
	OriginalStatus          string
	OriginalAccountID       string
	OriginalAmount          int64
	OriginalDestination     sql.NullString
	OriginalInvoiceID       sql.NullString
	OriginalCategoryID      sql.NullString
	OriginalRecurringRuleID sql.NullString
}

func TestReverseInvoicePaymentFullSettlement(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil:   "2026-08-20",
		InvoiceStatus:  "PAID",
		InvoicePaidAt:  &paidAt,
		LegacyPaid:     100000,
		InitialBalance: 200000,
	})

	result, err := reverseInvoicePayment(db, reversalCommand("ip-a"))
	if err != nil {
		t.Fatalf("reverse invoice payment: %v", err)
	}
	if result.AmountCents != 100000 || result.ActivePaymentSum != 0 || result.InvoiceStatus != "OPEN" || result.InvoicePaidAt != nil {
		t.Fatalf("unexpected reversal result: %+v", result)
	}

	state := reversalSnapshot(t, db, "ip-a", "payment-a")
	if state.Balance != 200000 || state.InvoiceStatus != "OPEN" || state.InvoicePaidAmount != 0 || state.InvoicePaidAt.Valid {
		t.Fatalf("financial state after reversal: %+v", state)
	}
	if !state.PaymentReversedAt.Valid || state.PaymentReversedAt.Int64 != reversalCommand("ip-a").ReversedAt ||
		!state.PaymentReversedDate.Valid || state.PaymentReversedDate.String != "2026-08-10" ||
		!state.PaymentReversedBy.Valid || state.PaymentReversedBy.String != reversalUserID ||
		!state.PaymentReason.Valid || state.PaymentReason.String != "Pagamento duplicado" ||
		!state.PaymentReversalTxID.Valid || state.PaymentReversalTxID.String != result.ReversalTransactionID {
		t.Fatalf("reversal provenance: %+v", state)
	}
	if state.TransactionCount != 3 || state.CompensationCount != 1 || state.OriginalCount != 1 ||
		state.OriginalType != "EXPENSE" || state.OriginalStatus != "paid" ||
		state.OriginalAccountID != reversalPaymentAccountID || state.OriginalAmount != 100000 ||
		state.OriginalDestination.Valid || state.OriginalInvoiceID.Valid || state.OriginalCategoryID.Valid || state.OriginalRecurringRuleID.Valid {
		t.Fatalf("original transaction changed: %+v", state)
	}

	var txType, status, accountID, dateCivil, paidDateCivil, userID, description string
	var amount, dateUnix, paidAtUnix int64
	var invoiceID, categoryID, destinationID, recurringRuleID sql.NullString
	if err := db.QueryRow(`
		SELECT type, status, account_id, amount, date, date_civil, paid_at, paid_date_civil,
		       user_id, description, invoice_id, category_id, destination_account_id, recurring_rule_id
		FROM transactions WHERE id = ?
	`, result.ReversalTransactionID).Scan(
		&txType, &status, &accountID, &amount, &dateUnix, &dateCivil, &paidAtUnix, &paidDateCivil,
		&userID, &description, &invoiceID, &categoryID, &destinationID, &recurringRuleID,
	); err != nil {
		t.Fatalf("compensation query: %v", err)
	}
	if txType != "INCOME" || status != "paid" || accountID != reversalPaymentAccountID || amount != 100000 ||
		dateUnix != testUnixDate("2026-08-10")+12*60*60 || dateCivil != "2026-08-10" ||
		paidAtUnix != reversalCommand("ip-a").ReversedAt || paidDateCivil != "2026-08-10" ||
		userID != reversalUserID || description != invoicePaymentReversalDescription ||
		invoiceID.Valid || categoryID.Valid || destinationID.Valid || recurringRuleID.Valid {
		t.Fatalf("invalid compensation: type=%q status=%q account=%q amount=%d date=%d/%q paid=%d/%q user=%q description=%q invoice=%v category=%v destination=%v recurring=%v",
			txType, status, accountID, amount, dateUnix, dateCivil, paidAtUnix, paidDateCivil, userID, description,
			invoiceID, categoryID, destinationID, recurringRuleID)
	}
}

func TestReverseInvoicePaymentPreservesHistoricalActiveThreshold(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	t1 := reversalUnix("2026-08-01", 10)
	t2 := reversalUnix("2026-08-02", 10)
	t3 := reversalUnix("2026-08-03", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments: []reversalPaymentSeed{
			{ID: "ip-a", TxID: "payment-a", Amount: 70000, PaidAt: t1, CreatedAt: t1},
			{ID: "ip-b", TxID: "payment-b", Amount: 50000, PaidAt: t2, CreatedAt: t2},
			{ID: "ip-c", TxID: "payment-c", Amount: 20000, PaidAt: t3, CreatedAt: t3},
		},
		ClosingCivil:   "2026-08-20",
		InvoiceStatus:  "PAID",
		InvoicePaidAt:  &t2,
		LegacyPaid:     140000,
		InitialBalance: 300000,
	})

	result, err := reverseInvoicePayment(db, reversalCommand("ip-c"))
	if err != nil {
		t.Fatal(err)
	}
	if result.InvoiceStatus != "PAID" || result.ActivePaymentSum != 120000 || result.InvoicePaidAt == nil || *result.InvoicePaidAt != t2 {
		t.Fatalf("result = %+v, want PAID/120000/threshold %d", result, t2)
	}
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT paid_at FROM invoices WHERE id = ?`, reversalInvoiceID).Scan(&paidAt); err != nil {
		t.Fatal(err)
	}
	if !paidAt.Valid || paidAt.Int64 != t2 || paidAt.Int64 == reversalCommand("ip-c").ReversedAt {
		t.Fatalf("paid_at = %v, want historical threshold %d", paidAt, t2)
	}
}

func TestReverseInvoicePaymentRollbackFaultMatrix(t *testing.T) {
	fault := errors.New("injected reversal failure")
	tests := []struct {
		name  string
		hooks invoicePaymentReversalTestHooks
	}{
		{name: "after CAS", hooks: invoicePaymentReversalTestHooks{afterCAS: func(*sql.Tx) error { return fault }}},
		{name: "after compensation insert", hooks: invoicePaymentReversalTestHooks{afterCompensationInsert: func(*sql.Tx) error { return fault }}},
		{name: "after balance effect", hooks: invoicePaymentReversalTestHooks{afterBalanceEffect: func(*sql.Tx) error { return fault }}},
		{name: "after reversal link", hooks: invoicePaymentReversalTestHooks{afterReversalLink: func(*sql.Tx) error { return fault }}},
		{name: "after invoice update", hooks: invoicePaymentReversalTestHooks{afterInvoiceUpdate: func(*sql.Tx) error { return fault }}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			paidAt := reversalUnix("2026-08-05", 10)
			seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
				InvoiceTotal: 100000,
				Payments: []reversalPaymentSeed{{
					ID: "ip-a", TxID: "payment-a", Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt,
				}},
				ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
				LegacyPaid: 100000, InitialBalance: 200000,
			})
			before := reversalSnapshot(t, db, "ip-a", "payment-a")

			_, err := reverseInvoicePaymentWithHooks(db, reversalCommand("ip-a"), &tt.hooks)
			if !errors.Is(err, fault) {
				t.Fatalf("error = %v, want injected fault", err)
			}
			if got := reversalSnapshot(t, db, "ip-a", "payment-a"); got != before {
				t.Fatalf("rollback left state changes\n got: %+v\nwant: %+v", got, before)
			}
		})
	}
}

func TestReverseInvoicePaymentConcurrentWithNewPaymentIsSerializable(t *testing.T) {
	dbA, dbB := openInvoicePaymentReversalFileDBs(t)
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 40000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "OPEN", LegacyPaid: 40000, InitialBalance: 200000,
	})

	casHeld := make(chan struct{})
	releaseCAS := make(chan struct{})
	paymentBeginAttempted := make(chan struct{})
	reversalDone := make(chan error, 1)
	go func() {
		_, err := reverseInvoicePaymentWithHooks(dbA, reversalCommand("ip-a"), &invoicePaymentReversalTestHooks{
			afterCAS: func(*sql.Tx) error {
				close(casHeld)
				<-releaseCAS
				return nil
			},
		})
		reversalDone <- err
	}()
	<-casHeld

	handler := FaturasHandler{
		DB: dbA, WriterDB: dbB, Templates: testInvoicePaymentTemplates(t), WorkspaceID: reversalWorkspaceID, UserID: reversalUserID,
		paymentWriterTestHooks: &invoicePaymentWriterTestHooks{
			beforeBegin: func() { close(paymentBeginAttempted) },
		},
	}
	paymentDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		handler.HandlePagarFatura(rr, formPost("/cartoes/faturas/pagar", url.Values{
			"invoice_id": {reversalInvoiceID}, "payment_account_id": {reversalPaymentAccountID},
			"payment_mode": {"settle"}, "confirm_settle": {"1"}, "payment_date": {"2026-08-11"},
		}))
		paymentDone <- rr
	}()

	<-paymentBeginAttempted
	close(releaseCAS)
	if err := <-reversalDone; err != nil {
		t.Fatalf("reversal error: %v", err)
	}
	rr := <-paymentDone
	if rr.Code != http.StatusOK {
		t.Fatalf("concurrent payment status = %d, want 200; body = %q", rr.Code, rr.Body.String())
	}

	assertSerializableReversalAndPaymentState(t, dbA)
}

func TestNewPaymentConcurrentWithReversalIsSerializable(t *testing.T) {
	dbA, dbB := openInvoicePaymentReversalFileDBs(t)
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 40000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: time.Now().UTC().AddDate(5, 0, 0).Format("2006-01-02"), InvoiceStatus: "OPEN", LegacyPaid: 40000, InitialBalance: 200000,
	})

	paymentWriterHeld := make(chan struct{})
	releasePayment := make(chan struct{})
	handler := FaturasHandler{
		DB: dbA, WriterDB: dbB, Templates: testInvoicePaymentTemplates(t), WorkspaceID: reversalWorkspaceID, UserID: reversalUserID,
		paymentWriterTestHooks: &invoicePaymentWriterTestHooks{
			afterBegin: func(*sql.Tx) error {
				close(paymentWriterHeld)
				<-releasePayment
				return nil
			},
		},
	}
	paymentDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		handler.HandlePagarFatura(rr, formPost("/cartoes/faturas/pagar", url.Values{
			"invoice_id": {reversalInvoiceID}, "payment_account_id": {reversalPaymentAccountID},
			"payment_mode": {"settle"}, "confirm_settle": {"1"}, "payment_date": {"2026-08-11"},
		}))
		paymentDone <- rr
	}()
	<-paymentWriterHeld

	reversalStarted := make(chan struct{})
	reversalDone := make(chan error, 1)
	go func() {
		close(reversalStarted)
		_, err := reverseInvoicePayment(dbA, reversalCommand("ip-a"))
		reversalDone <- err
	}()
	<-reversalStarted
	waitForDBConnectionInUse(t, dbA)
	close(releasePayment)

	rr := <-paymentDone
	if rr.Code != http.StatusOK {
		t.Fatalf("concurrent payment status = %d, want 200; body = %q", rr.Code, rr.Body.String())
	}
	if err := <-reversalDone; err != nil {
		t.Fatalf("reversal error: %v", err)
	}
	assertSerializablePaymentThenReversalState(t, dbA)
}

func reversalCommand(paymentID string) invoicePaymentReversalCommand {
	return invoicePaymentReversalCommand{
		WorkspaceID: reversalWorkspaceID, InvoicePaymentID: paymentID, ActorID: reversalUserID,
		Reason: "  Pagamento duplicado  ", ReversedAt: reversalUnix("2026-08-10", 15), ReversedDateCivil: "2026-08-10",
	}
}

func reversalUnix(date string, hour int) int64 {
	parsed, _ := time.Parse("2006-01-02", date)
	return parsed.Add(time.Duration(hour) * time.Hour).Unix()
}

func seedInvoicePaymentReversalFixture(t *testing.T, db *sql.DB, opts reversalFixtureOptions) {
	t.Helper()
	createdAt := reversalUnix("2026-08-01", 9)
	if opts.InitialBalance == 0 {
		opts.InitialBalance = 200000
	}
	if opts.ClosingCivil == "" {
		opts.ClosingCivil = "2026-08-20"
	}
	if opts.InvoiceStatus == "" {
		opts.InvoiceStatus = "OPEN"
	}
	activePaid := int64(0)
	for _, payment := range opts.Payments {
		activePaid += payment.Amount
	}
	currentBalance := opts.InitialBalance - activePaid
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES (?, 'Reversal User', 'reversal@example.com', 'hash', ?, ?)`, reversalUserID, createdAt, createdAt)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, description, created_at, updated_at) VALUES (?, 'Reversal Workspace', '', ?, ?)`, reversalWorkspaceID, createdAt, createdAt)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, ?, 'ADMIN', ?)`, reversalWorkspaceID, reversalUserID, createdAt)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Checking', 'CHECKING', ?, ?, ?, ?)`, reversalPaymentAccountID, reversalWorkspaceID, opts.InitialBalance, currentBalance, createdAt, createdAt)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at) VALUES (?, ?, 'Card', 'CREDIT_CARD', 0, 0, ?, ?)`, reversalCardAccountID, reversalWorkspaceID, createdAt, createdAt)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-reversal', ?, 20, 10, 500000)`, reversalCardAccountID)
	closingUnix := testUnixDate(opts.ClosingCivil) + 12*60*60
	execTestSQL(t, db, `
		INSERT INTO invoices (id, account_id, reference, closing_date, closing_date_civil, due_date, due_date_civil, status, paid_amount, paid_at, created_at)
		VALUES (?, ?, '2026-08', ?, ?, ?, '2026-09-10', ?, ?, ?, ?)
	`, reversalInvoiceID, reversalCardAccountID, closingUnix, opts.ClosingCivil, testUnixDate("2026-09-10")+12*60*60,
		opts.InvoiceStatus, opts.LegacyPaid, nullableInt64(opts.InvoicePaidAt), createdAt)
	if opts.InvoiceTotal > 0 {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, paid_at, paid_date_civil, installment_number, total_installments, created_at, updated_at)
			VALUES ('purchase-reversal', ?, ?, ?, ?, 'EXPENSE', ?, ?, '2026-08-02', 'Purchase', 'paid', ?, '2026-08-02', 1, 1, ?, ?)
		`, reversalWorkspaceID, reversalUserID, reversalCardAccountID, reversalInvoiceID, opts.InvoiceTotal,
			reversalUnix("2026-08-02", 12), reversalUnix("2026-08-02", 12), createdAt, createdAt)
	}
	for _, payment := range opts.Payments {
		execTestSQL(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, description, status, paid_at, paid_date_civil, installment_number, total_installments, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'EXPENSE', ?, ?, '2026-08-05', 'Invoice payment', 'paid', ?, '2026-08-05', 1, 1, ?, ?)
		`, payment.TxID, reversalWorkspaceID, reversalUserID, reversalPaymentAccountID, payment.Amount,
			payment.PaidAt, payment.PaidAt, payment.CreatedAt, payment.CreatedAt)
		execTestSQL(t, db, `
			INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'manual', ?, ?)
		`, payment.ID, reversalWorkspaceID, reversalInvoiceID, reversalPaymentAccountID, payment.TxID,
			payment.Amount, payment.PaidAt, reversalUserID, payment.CreatedAt)
	}
}

func nullableInt64(value *int64) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

func reversalSnapshot(t *testing.T, db *sql.DB, paymentID, originalTxID string) reversalState {
	t.Helper()
	var state reversalState
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, reversalPaymentAccountID).Scan(&state.Balance); err != nil {
		t.Fatalf("balance snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, reversalInvoiceID).Scan(&state.InvoiceStatus, &state.InvoicePaidAmount, &state.InvoicePaidAt); err != nil {
		t.Fatalf("invoice snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT reversed_at, reversed_date_civil, reversed_by, reversal_reason, reversal_transaction_id
		FROM invoice_payments WHERE id = ?
	`, paymentID).Scan(&state.PaymentReversedAt, &state.PaymentReversedDate, &state.PaymentReversedBy, &state.PaymentReason, &state.PaymentReversalTxID); err != nil {
		t.Fatalf("payment snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions`).Scan(&state.TransactionCount); err != nil {
		t.Fatalf("transaction count snapshot: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = ?`, invoicePaymentReversalDescription).Scan(&state.CompensationCount); err != nil {
		t.Fatalf("compensation count snapshot: %v", err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(1), COALESCE(MAX(type), ''), COALESCE(MAX(status), ''), COALESCE(MAX(account_id), ''), COALESCE(MAX(amount), 0),
		       MAX(destination_account_id), MAX(invoice_id), MAX(category_id), MAX(recurring_rule_id)
		FROM transactions WHERE id = ?
	`, originalTxID).Scan(&state.OriginalCount, &state.OriginalType, &state.OriginalStatus, &state.OriginalAccountID,
		&state.OriginalAmount, &state.OriginalDestination, &state.OriginalInvoiceID, &state.OriginalCategoryID, &state.OriginalRecurringRuleID); err != nil {
		t.Fatalf("original transaction snapshot: %v", err)
	}
	return state
}

func openInvoicePaymentReversalFileDBs(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	path := filepath.Join(dir, "reversal-concurrency.db")
	runtimeDBs, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatalf("open runtime file databases: %v", err)
	}
	t.Cleanup(func() { _ = runtimeDBs.Close() })
	return runtimeDBs.DB, runtimeDBs.Immediate
}

func waitForDBConnectionInUse(t *testing.T, db *sql.DB) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if db.Stats().InUse == 1 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("database connection was not acquired by concurrent reversal")
		case <-ticker.C:
		}
	}
}

func assertSerializableReversalAndPaymentState(t *testing.T, db *sql.DB) {
	t.Helper()
	var activeSum, activeCount int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0), COUNT(1)
		FROM invoice_payments WHERE workspace_id = ? AND invoice_id = ? AND reversed_at IS NULL
	`, reversalWorkspaceID, reversalInvoiceID).Scan(&activeSum, &activeCount); err != nil {
		t.Fatalf("active payments: %v", err)
	}
	if activeSum != 100000 || activeCount != 1 {
		t.Fatalf("active payments = %d/%d, want 100000/1", activeSum, activeCount)
	}
	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, reversalInvoiceID).Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice state: %v", err)
	}
	if status != "PAID" || paidAmount != 100000 || !paidAt.Valid {
		t.Fatalf("invoice = status %q paid_amount %d paid_at %v", status, paidAmount, paidAt)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, reversalPaymentAccountID).Scan(&balance); err != nil {
		t.Fatalf("account balance: %v", err)
	}
	if balance != 100000 {
		t.Fatalf("balance = %d, want 100000", balance)
	}
	var reversalCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = ?`, invoicePaymentReversalDescription).Scan(&reversalCount); err != nil {
		t.Fatalf("reversal count: %v", err)
	}
	if reversalCount != 1 {
		t.Fatalf("reversal transaction count = %d, want 1", reversalCount)
	}
}

func assertSerializablePaymentThenReversalState(t *testing.T, db *sql.DB) {
	t.Helper()
	var activeSum, activeCount int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0), COUNT(1)
		FROM invoice_payments WHERE workspace_id = ? AND invoice_id = ? AND reversed_at IS NULL
	`, reversalWorkspaceID, reversalInvoiceID).Scan(&activeSum, &activeCount); err != nil {
		t.Fatalf("active payments: %v", err)
	}
	if activeSum != 60000 || activeCount != 1 {
		t.Fatalf("active payments = %d/%d, want 60000/1", activeSum, activeCount)
	}
	var status string
	var paidAmount int64
	var paidAt sql.NullInt64
	if err := db.QueryRow(`SELECT status, paid_amount, paid_at FROM invoices WHERE id = ?`, reversalInvoiceID).Scan(&status, &paidAmount, &paidAt); err != nil {
		t.Fatalf("invoice state: %v", err)
	}
	if status != "OPEN" || paidAmount != 60000 || paidAt.Valid {
		t.Fatalf("invoice = status %q paid_amount %d paid_at %v", status, paidAmount, paidAt)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, reversalPaymentAccountID).Scan(&balance); err != nil {
		t.Fatalf("account balance: %v", err)
	}
	if balance != 140000 {
		t.Fatalf("balance = %d, want 140000", balance)
	}
	var reversalCount, paymentCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = ?`, invoicePaymentReversalDescription).Scan(&reversalCount); err != nil {
		t.Fatalf("reversal count: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(1) FROM invoice_payments WHERE invoice_id = ?`, reversalInvoiceID).Scan(&paymentCount); err != nil {
		t.Fatalf("invoice payment count: %v", err)
	}
	if reversalCount != 1 || paymentCount != 2 {
		t.Fatalf("reversal/payment counts = %d/%d, want 1/2", reversalCount, paymentCount)
	}
}

func TestReverseInvoicePaymentConcurrentSamePaymentCreditsOnce(t *testing.T) {
	dbA, dbB := openInvoicePaymentReversalFileDBs(t)
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: "ip-a", TxID: "payment-a", Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{dbA, dbB} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			_, err := reverseInvoicePayment(db, reversalCommand("ip-a"))
			errs <- err
		}(db)
	}
	close(start)
	wg.Wait()
	close(errs)

	var successes, alreadyReversed int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errInvoicePaymentAlreadyReversed):
			alreadyReversed++
		default:
			t.Fatalf("unexpected concurrent reversal error: %v", err)
		}
	}
	if successes != 1 || alreadyReversed != 1 {
		t.Fatalf("concurrent outcomes: success=%d already=%d", successes, alreadyReversed)
	}
	state := reversalSnapshot(t, dbA, "ip-a", "payment-a")
	if state.Balance != 200000 || state.CompensationCount != 1 || !state.PaymentReversalTxID.Valid {
		t.Fatalf("concurrent final state: %+v", state)
	}
}

// insertReversalPaymentTransaction inserts the original EXPENSE transaction of
// an invoice payment directly, mirroring the production payment write.
func insertReversalPaymentTransaction(t *testing.T, db *sql.DB, txID string, amount, paidAt, createdAt int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, description, status, paid_at, paid_date_civil, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'EXPENSE', ?, ?, '2026-08-05', 'Invoice payment', 'paid', ?, '2026-08-05', 1, 1, ?, ?)
	`, txID, reversalWorkspaceID, reversalUserID, reversalPaymentAccountID, amount, paidAt, paidAt, createdAt, createdAt)
}

// insertReversalPaymentRecord inserts an invoice_payments row directly so test
// fixtures can stage extreme amounts without passing through int64
// accumulators (fixture computes current_balance via naive sums).
func insertReversalPaymentRecord(t *testing.T, db *sql.DB, id string, txID sql.NullString, amount, paidAt, createdAt int64) {
	t.Helper()
	execTestSQL(t, db, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'manual', ?, ?)
	`, id, reversalWorkspaceID, reversalInvoiceID, reversalPaymentAccountID, txID, amount, paidAt, reversalUserID, createdAt)
}

func TestReverseInvoicePaymentInvoiceTotalOverflowRollsBack(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: math.MaxInt64,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 300000,
	})
	// Second invoice item of 1 cent pushes SUM(amount) past math.MaxInt64.
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, paid_at, paid_date_civil, installment_number, total_installments, created_at, updated_at)
		VALUES ('purchase-overflow-extra', ?, ?, ?, ?, 'EXPENSE', 1, ?, '2026-08-03', 'Purchase overflow', 'paid', ?, '2026-08-03', 1, 1, ?, ?)
	`, reversalWorkspaceID, reversalUserID, reversalCardAccountID, reversalInvoiceID,
		reversalUnix("2026-08-03", 12), reversalUnix("2026-08-03", 12), paidAt, paidAt)

	before := reversalSnapshot(t, db, "ip-a", "payment-a")
	_, err := reverseInvoicePayment(db, reversalCommand("ip-a"))
	if err == nil {
		t.Fatal("invoice total overflow must fail the reversal")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "overflow") {
		t.Fatalf("expected integer overflow from invoice total sum, got: %v", err)
	}
	after := reversalSnapshot(t, db, "ip-a", "payment-a")
	if after != before {
		t.Fatalf("overflow persisted partial state\n got: %+v\nwant: %+v", after, before)
	}
	if after.PaymentReversedAt.Valid || after.PaymentReversalTxID.Valid || after.CompensationCount != 0 {
		t.Fatalf("reversal fields/compensation persisted after overflow: %+v", after)
	}
	if after.InvoiceStatus != before.InvoiceStatus || after.InvoicePaidAmount != before.InvoicePaidAmount ||
		after.InvoicePaidAt != before.InvoicePaidAt || after.Balance != before.Balance ||
		after.TransactionCount != before.TransactionCount || after.OriginalAmount != before.OriginalAmount {
		t.Fatalf("invoice/balance/transaction state diverged after overflow: %+v", after)
	}
}

func TestReverseInvoicePaymentActiveSumOverflowRollsBack(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	tSmall := reversalUnix("2026-08-01", 10)
	tBig1 := reversalUnix("2026-08-02", 10)
	tBig2 := reversalUnix("2026-08-03", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal:  100000,
		Payments:      nil,
		ClosingCivil:  "2026-08-20",
		InvoiceStatus: "OPEN",
		InitialBalance: 200000,
	})
	// Extreme active payments are staged directly: the fixture accumulator
	// (activePaid += Amount) would itself overflow and corrupt the balance.
	insertReversalPaymentTransaction(t, db, "payment-a", 100, tSmall, tSmall)
	insertReversalPaymentRecord(t, db, "ip-a", sql.NullString{String: "payment-a", Valid: true}, 100, tSmall, tSmall)
	insertReversalPaymentRecord(t, db, "ip-big1", sql.NullString{}, math.MaxInt64, tBig1, tBig1)
	insertReversalPaymentRecord(t, db, "ip-big2", sql.NullString{}, 1, tBig2, tBig2)

	before := reversalSnapshot(t, db, "ip-a", "payment-a")
	_, err := reverseInvoicePayment(db, reversalCommand("ip-a"))
	if !errors.Is(err, errInvoicePaymentSumOverflow) {
		t.Fatalf("error = %v, want errInvoicePaymentSumOverflow", err)
	}
	after := reversalSnapshot(t, db, "ip-a", "payment-a")
	if after != before {
		t.Fatalf("active sum overflow persisted partial state\n got: %+v\nwant: %+v", after, before)
	}
	if after.PaymentReversedAt.Valid || after.PaymentReversalTxID.Valid || after.CompensationCount != 0 {
		t.Fatalf("reversal fields/compensation persisted after overflow: %+v", after)
	}
	if after.Balance != before.Balance || after.InvoiceStatus != before.InvoiceStatus ||
		after.InvoicePaidAmount != before.InvoicePaidAmount || after.InvoicePaidAt != before.InvoicePaidAt ||
		after.TransactionCount != before.TransactionCount {
		t.Fatalf("balance/invoice/transaction state diverged after overflow: %+v", after)
	}
}

func TestReverseInvoicePaymentActiveSumUnderflowRollsBack(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	tNeg1 := reversalUnix("2026-08-01", 10)
	tNeg2 := reversalUnix("2026-08-02", 10)
	tSmall := reversalUnix("2026-08-03", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal:  100000,
		Payments:      nil,
		ClosingCivil:  "2026-08-20",
		InvoiceStatus: "OPEN",
		InitialBalance: 200000,
	})
	insertReversalPaymentTransaction(t, db, "payment-a", 100, tSmall, tSmall)
	insertReversalPaymentRecord(t, db, "ip-a", sql.NullString{String: "payment-a", Valid: true}, 100, tSmall, tSmall)

	// Negative amounts are impossible under the normal schema
	// (invoice_payments.amount_cents > 0). Disable CHECK constraints only to
	// stage the extreme fixture, then restore before exercising production code.
	execTestSQL(t, db, `PRAGMA ignore_check_constraints = ON`)
	insertReversalPaymentRecord(t, db, "ip-neg1", sql.NullString{}, math.MinInt64, tNeg1, tNeg1)
	insertReversalPaymentRecord(t, db, "ip-neg2", sql.NullString{}, -1, tNeg2, tNeg2)
	execTestSQL(t, db, `PRAGMA ignore_check_constraints = OFF`)

	if _, err := db.Exec(`
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES ('ip-reject', ?, ?, ?, NULL, -1, ?, 'manual', ?, ?)
	`, reversalWorkspaceID, reversalInvoiceID, reversalPaymentAccountID, tNeg1, reversalUserID, tNeg1); err == nil {
		t.Fatal("normal schema must reject negative amount_cents")
	}

	before := reversalSnapshot(t, db, "ip-a", "payment-a")
	_, err := reverseInvoicePayment(db, reversalCommand("ip-a"))
	if !errors.Is(err, errInvoicePaymentSumOverflow) {
		t.Fatalf("error = %v, want errInvoicePaymentSumOverflow", err)
	}
	after := reversalSnapshot(t, db, "ip-a", "payment-a")
	if after != before {
		t.Fatalf("underflow persisted partial state\n got: %+v\nwant: %+v", after, before)
	}
	if after.PaymentReversedAt.Valid || after.PaymentReversalTxID.Valid || after.CompensationCount != 0 {
		t.Fatalf("reversal fields/compensation persisted after underflow: %+v", after)
	}
	if after.Balance != before.Balance || after.InvoiceStatus != before.InvoiceStatus ||
		after.InvoicePaidAmount != before.InvoicePaidAmount || after.InvoicePaidAt != before.InvoicePaidAt ||
		after.TransactionCount != before.TransactionCount {
		t.Fatalf("balance/invoice/transaction state diverged after underflow: %+v", after)
	}
}

func TestAddInt64CheckedExactLimits(t *testing.T) {
	tests := []struct {
		name    string
		a, b    int64
		want    int64
		wantErr bool
	}{
		{name: "MaxInt64 plus zero", a: math.MaxInt64, b: 0, want: math.MaxInt64},
		{name: "zero plus MaxInt64", a: 0, b: math.MaxInt64, want: math.MaxInt64},
		{name: "reach MaxInt64 from below", a: math.MaxInt64 - 1, b: 1, want: math.MaxInt64},
		{name: "MinInt64 plus zero", a: math.MinInt64, b: 0, want: math.MinInt64},
		{name: "zero plus MinInt64", a: 0, b: math.MinInt64, want: math.MinInt64},
		{name: "reach MinInt64 from above", a: math.MinInt64 + 1, b: -1, want: math.MinInt64},
		{name: "MaxInt64 plus one overflows", a: math.MaxInt64, b: 1, wantErr: true},
		{name: "MaxInt64 plus MaxInt64 overflows", a: math.MaxInt64, b: math.MaxInt64, wantErr: true},
		{name: "MinInt64 minus one underflows", a: math.MinInt64, b: -1, wantErr: true},
		{name: "MinInt64 plus MinInt64 underflows", a: math.MinInt64, b: math.MinInt64, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := addInt64Checked(tt.a, tt.b)
			if tt.wantErr {
				if !errors.Is(err, errInvoicePaymentSumOverflow) {
					t.Fatalf("error = %v, want errInvoicePaymentSumOverflow", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid boundary rejected: %v", err)
			}
			if got != tt.want {
				t.Fatalf("sum = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSumInvoiceTotalTxPropagatesQueryError(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, db, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments: []reversalPaymentSeed{{
			ID: "ip-a", TxID: "payment-a", Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt,
		}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DROP TABLE transaction_refunds`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	total, err := sumInvoiceTotalTx(tx, reversalWorkspaceID, reversalInvoiceID)
	if err == nil {
		t.Fatalf("query error must propagate, got total=%d err=nil", total)
	}
	if !errors.Is(err, sql.ErrNoRows) && total != 0 {
		t.Fatalf("failed scan must not report a usable total: %d/%v", total, err)
	}
}
