package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestHandleReverseInvoicePaymentRejectsInvalidHTTPInput(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	validID := uuid.NewString()
	tests := []struct {
		name        string
		values      url.Values
		contentType string
		query       string
		want        int
	}{
		{name: "missing content type", values: url.Values{"invoice_payment_id": {validID}, "reason": {"Duplicado"}}, want: http.StatusBadRequest},
		{name: "query rejected", values: url.Values{"invoice_payment_id": {validID}, "reason": {"Duplicado"}}, contentType: "application/x-www-form-urlencoded", query: "amount=1", want: http.StatusBadRequest},
		{name: "extra field", values: url.Values{"invoice_payment_id": {validID}, "reason": {"Duplicado"}, "amount": {"1"}}, contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "duplicate id", values: url.Values{"invoice_payment_id": {validID, validID}, "reason": {"Duplicado"}}, contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "missing id", values: url.Values{"reason": {"Duplicado"}}, contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "invalid id", values: url.Values{"invoice_payment_id": {"not-a-uuid"}, "reason": {"Duplicado"}}, contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "missing reason", values: url.Values{"invoice_payment_id": {validID}}, contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "empty reason", values: url.Values{"invoice_payment_id": {validID}, "reason": {"  "}}, contentType: "application/x-www-form-urlencoded", want: http.StatusBadRequest},
		{name: "long reason", values: url.Values{"invoice_payment_id": {validID}, "reason": {strings.Repeat("á", 501)}}, contentType: "application/x-www-form-urlencoded", want: http.StatusUnprocessableEntity},
		{name: "control reason", values: url.Values{"invoice_payment_id": {validID}, "reason": {"linha\nnova"}}, contentType: "application/x-www-form-urlencoded", want: http.StatusUnprocessableEntity},
		{name: "invalid utf8 reason", values: url.Values{"invoice_payment_id": {validID}, "reason": {string([]byte{0xff})}}, contentType: "application/x-www-form-urlencoded", want: http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var called atomic.Int32
			h := FaturasHandler{DB: db, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
			req := invoicePaymentReversalRequest(tt.values)
			if tt.contentType == "" {
				req.Header.Del("Content-Type")
			} else {
				req.Header.Set("Content-Type", tt.contentType)
			}
			req.URL.RawQuery = tt.query
			rr := httptest.NewRecorder()
			h.handleReverseInvoicePayment(rr, req, time.Now(), func(*sql.DB, invoicePaymentReversalCommand) (invoicePaymentReversalResult, error) {
				called.Add(1)
				return invoicePaymentReversalResult{InvoiceID: reversalInvoiceID}, nil
			})
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %q", rr.Code, tt.want, rr.Body.String())
			}
			if called.Load() != 0 {
				t.Fatal("financial primitive was called for invalid HTTP input")
			}
		})
	}
}

func TestHandleReverseInvoicePaymentRejectsClientFinancialAuthorityFields(t *testing.T) {
	dbA, _ := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})
	before := reversalSnapshot(t, dbA, paymentID, transactionID)
	h := FaturasHandler{DB: dbA, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
	for _, field := range []string{"amount", "account_id", "invoice_id", "card_id", "transaction_id", "reversal_transaction_id", "workspace_id", "reversed_by", "reversed_date_civil"} {
		t.Run(field, func(t *testing.T) {
			values := validInvoicePaymentReversalValues(paymentID)
			values.Set(field, "attacker-controlled")
			rr := httptest.NewRecorder()
			h.HandleReverseInvoicePayment(rr, invoicePaymentReversalRequest(values))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %q", rr.Code, rr.Body.String())
			}
			if got := reversalSnapshot(t, dbA, paymentID, transactionID); got != before {
				t.Fatalf("financial state changed for field %q: %+v", field, got)
			}
		})
	}
}

func TestHandleReverseInvoicePaymentSuccessUsesDeferredDBAndPersistedAuthority(t *testing.T) {
	t.Setenv("CONTABASE_TIMEZONE", "America/Sao_Paulo")
	dbA, _ := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})

	fixedNow := time.Date(2026, 8, 10, 23, 30, 0, 0, time.UTC)
	h := FaturasHandler{DB: dbA, WriterDB: nil, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
	rr := httptest.NewRecorder()
	h.handleReverseInvoicePayment(rr, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)), fixedNow, reverseInvoicePayment)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/cartoes/faturas/"+reversalInvoiceID {
		t.Fatalf("response = %d location=%q body=%q", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}

	state := reversalSnapshot(t, dbA, paymentID, transactionID)
	if state.Balance != 200000 || state.CompensationCount != 1 || state.OriginalAmount != 100000 || !state.PaymentReversalTxID.Valid {
		t.Fatalf("financial state = %+v", state)
	}
	if state.PaymentReversedBy.String != reversalUserID || state.PaymentReason.String != "Pagamento duplicado" || state.PaymentReversedDate.String != "2026-08-10" || state.PaymentReversedAt.Int64 != fixedNow.Unix() {
		t.Fatalf("server-derived provenance = %+v", state)
	}
}

func TestHandleReverseInvoicePaymentForeignWorkspaceIsNotFound(t *testing.T) {
	dbA, _ := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})
	before := reversalSnapshot(t, dbA, paymentID, transactionID)
	h := FaturasHandler{DB: dbA, WorkspaceID: "foreign-workspace", UserID: reversalUserID}
	rr := httptest.NewRecorder()
	h.HandleReverseInvoicePayment(rr, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)))
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "não encontrado") {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := reversalSnapshot(t, dbA, paymentID, transactionID); got != before {
		t.Fatalf("foreign request changed state: %+v", got)
	}
}

func TestHandleReverseInvoicePaymentAlreadyReversedReturnsConflictWithoutDuplicate(t *testing.T) {
	dbA, _ := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})
	h := FaturasHandler{DB: dbA, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
	first := httptest.NewRecorder()
	h.HandleReverseInvoicePayment(first, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)))
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first status = %d body=%q", first.Code, first.Body.String())
	}
	afterFirst := reversalSnapshot(t, dbA, paymentID, transactionID)
	second := httptest.NewRecorder()
	h.HandleReverseInvoicePayment(second, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)))
	if second.Code != http.StatusConflict {
		t.Fatalf("second status = %d body=%q", second.Code, second.Body.String())
	}
	if got := reversalSnapshot(t, dbA, paymentID, transactionID); got != afterFirst {
		t.Fatalf("retry duplicated financial effect: %+v", got)
	}
}

func TestHandleReverseInvoicePaymentMapsPrimitiveErrorsSafely(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	busyErr := invoicePaymentReversalSQLiteBusyError(t)
	lockedErr := invoicePaymentReversalSQLiteLockedError(t)
	busySnapshotErr := invoicePaymentReversalSQLiteBusySnapshotError(t)
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  string
	}{
		{name: "not found", err: errInvoicePaymentReversalNotFound, wantStatus: http.StatusNotFound},
		{name: "already reversed", err: errInvoicePaymentAlreadyReversed, wantStatus: http.StatusConflict},
		{name: "link conflict", err: fmt.Errorf("%w: private detail", errInvoicePaymentReversalLinkConflict), wantStatus: http.StatusConflict},
		{name: "structural", err: fmt.Errorf("%w: private detail", errInvoicePaymentReversalStructural), wantStatus: http.StatusInternalServerError},
		{name: "primitive invalid", err: errInvoicePaymentReversalInvalidInput, wantStatus: http.StatusInternalServerError},
		{name: "typed sqlite busy", err: busyErr, wantStatus: http.StatusServiceUnavailable, wantRetry: "1"},
		{name: "typed sqlite locked", err: lockedErr, wantStatus: http.StatusServiceUnavailable, wantRetry: "1"},
		{name: "typed sqlite busy extended", err: busySnapshotErr, wantStatus: http.StatusServiceUnavailable, wantRetry: "1"},
		{name: "generic sqlite busy text", err: errors.New("SQLITE_BUSY: private detail"), wantStatus: http.StatusInternalServerError},
		{name: "generic sqlite locked text", err: errors.New("SQLITE_LOCKED: private detail"), wantStatus: http.StatusInternalServerError},
		{name: "generic database locked text", err: errors.New("business validation: database is locked"), wantStatus: http.StatusInternalServerError},
		{name: "unexpected", err: errors.New("private database detail"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := FaturasHandler{DB: db, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
			rr := httptest.NewRecorder()
			h.handleReverseInvoicePayment(rr, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(uuid.NewString())), time.Now(), func(*sql.DB, invoicePaymentReversalCommand) (invoicePaymentReversalResult, error) {
				return invoicePaymentReversalResult{}, tt.err
			})
			if rr.Code != tt.wantStatus || rr.Header().Get("Retry-After") != tt.wantRetry {
				t.Fatalf("status/retry = %d/%q, want %d/%q", rr.Code, rr.Header().Get("Retry-After"), tt.wantStatus, tt.wantRetry)
			}
			body := rr.Body.String()
			if strings.Contains(body, "private") || strings.Contains(body, "SQLITE") || strings.Contains(body, "database") {
				t.Fatalf("private error leaked: %q", body)
			}
		})
	}
}

func TestInvoicePaymentReversalSQLiteUnavailableUsesTypedCodesOnly(t *testing.T) {
	busyErr := invoicePaymentReversalSQLiteBusyError(t)
	lockedErr := invoicePaymentReversalSQLiteLockedError(t)
	busySnapshotErr := invoicePaymentReversalSQLiteBusySnapshotError(t)
	nonBusySQLiteErr := invoicePaymentReversalSQLiteConstraintError(t)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "sqlite busy base", err: busyErr, want: true},
		{name: "sqlite locked base", err: lockedErr, want: true},
		{name: "sqlite busy extended", err: busySnapshotErr, want: true},
		{name: "sqlite non busy", err: nonBusySQLiteErr, want: false},
		{name: "wrapped sqlite busy", err: fmt.Errorf("context: %w", busyErr), want: true},
		{name: "generic sqlite busy text", err: errors.New("SQLITE_BUSY"), want: false},
		{name: "generic sqlite locked text", err: errors.New("SQLITE_LOCKED"), want: false},
		{name: "generic database locked text", err: errors.New("database is locked"), want: false},
		{name: "wrapped generic locked text", err: fmt.Errorf("operation failed: %w", errors.New("database is locked")), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := invoicePaymentReversalSQLiteUnavailable(tt.err); got != tt.want {
				t.Fatalf("unavailable = %t, want %t; error=%T %v", got, tt.want, tt.err, tt.err)
			}
		})
	}

	for _, code := range []int{sqlite3.SQLITE_LOCKED_SHAREDCACHE, sqlite3.SQLITE_LOCKED_VTAB} {
		if !invoicePaymentReversalSQLiteCodeUnavailable(code) {
			t.Fatalf("extended SQLITE_LOCKED code %d was not classified by its base code", code)
		}
	}
}

func TestHandleReverseInvoicePaymentSuccessResponseHTTPAndHTMX(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	for _, tc := range []struct {
		name       string
		hx         bool
		wantStatus int
	}{
		{name: "http", wantStatus: http.StatusSeeOther},
		{name: "htmx", hx: true, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := FaturasHandler{DB: db, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
			req := invoicePaymentReversalRequest(validInvoicePaymentReversalValues(uuid.NewString()))
			if tc.hx {
				req.Header.Set("HX-Request", "true")
			}
			rr := httptest.NewRecorder()
			h.handleReverseInvoicePayment(rr, req, time.Now(), func(*sql.DB, invoicePaymentReversalCommand) (invoicePaymentReversalResult, error) {
				return invoicePaymentReversalResult{InvoiceID: reversalInvoiceID}, nil
			})
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
			if tc.hx {
				if rr.Header().Get("HX-Redirect") != "/cartoes/faturas/"+reversalInvoiceID {
					t.Fatalf("HX-Redirect = %q", rr.Header().Get("HX-Redirect"))
				}
			} else if rr.Header().Get("Location") != "/cartoes/faturas/"+reversalInvoiceID {
				t.Fatalf("Location = %q", rr.Header().Get("Location"))
			}
		})
	}
}

func TestHandleReverseInvoicePaymentConcurrentSamePaymentCreditsOnce(t *testing.T) {
	dbA, _ := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 100000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "PAID", InvoicePaidAt: &paidAt,
		LegacyPaid: 100000, InitialBalance: 200000,
	})
	h := FaturasHandler{DB: dbA, WriterDB: nil, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
	start := make(chan struct{})
	responses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			h.HandleReverseInvoicePayment(rr, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)))
			responses <- rr.Code
		}()
	}
	close(start)
	wg.Wait()
	close(responses)
	counts := map[int]int{}
	for status := range responses {
		counts[status]++
	}
	if counts[http.StatusSeeOther] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses = %#v", counts)
	}
	state := reversalSnapshot(t, dbA, paymentID, transactionID)
	if state.Balance != 200000 || state.CompensationCount != 1 || !state.PaymentReversalTxID.Valid {
		t.Fatalf("concurrent financial state = %+v", state)
	}
}

func TestHandleReverseInvoicePaymentConcurrentWithNewPaymentIsSerializable(t *testing.T) {
	dbA, dbB := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 40000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "OPEN", LegacyPaid: 40000, InitialBalance: 200000,
	})

	casHeld := make(chan struct{})
	releaseCAS := make(chan struct{})
	paymentBeginAttempted := make(chan struct{})
	reversalDone := make(chan *httptest.ResponseRecorder, 1)
	reversalHandler := FaturasHandler{DB: dbA, WriterDB: nil, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
	go func() {
		rr := httptest.NewRecorder()
		reversalHandler.handleReverseInvoicePayment(rr, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)), time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC), func(db *sql.DB, command invoicePaymentReversalCommand) (invoicePaymentReversalResult, error) {
			if db != dbA {
				return invoicePaymentReversalResult{}, errors.New("reversal did not use deferred DB")
			}
			return reverseInvoicePaymentWithHooks(db, command, &invoicePaymentReversalTestHooks{afterCAS: func(*sql.Tx) error {
				close(casHeld)
				<-releaseCAS
				return nil
			}})
		})
		reversalDone <- rr
	}()
	<-casHeld

	paymentHandler := FaturasHandler{
		DB: dbA, WriterDB: dbB, Templates: testInvoicePaymentTemplates(t), WorkspaceID: reversalWorkspaceID, UserID: reversalUserID,
		paymentWriterTestHooks: &invoicePaymentWriterTestHooks{beforeBegin: func() { close(paymentBeginAttempted) }},
	}
	paymentDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		paymentHandler.HandlePagarFatura(rr, formPost("/cartoes/faturas/pagar", url.Values{
			"invoice_id": {reversalInvoiceID}, "payment_account_id": {reversalPaymentAccountID},
			"payment_mode": {"settle"}, "confirm_settle": {"1"}, "payment_date": {"2026-08-11"},
		}))
		paymentDone <- rr
	}()
	<-paymentBeginAttempted
	close(releaseCAS)
	if rr := <-reversalDone; rr.Code != http.StatusSeeOther {
		t.Fatalf("reversal status = %d body=%q", rr.Code, rr.Body.String())
	}
	if rr := <-paymentDone; rr.Code != http.StatusOK {
		t.Fatalf("payment status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertSerializableReversalAndPaymentState(t, dbA)
}

func TestHandleReverseInvoicePaymentConcurrentAfterPaymentIsSerializable(t *testing.T) {
	dbA, dbB := openInvoicePaymentReversalFileDBs(t)
	paymentID := uuid.NewString()
	transactionID := uuid.NewString()
	paidAt := reversalUnix("2026-08-05", 10)
	seedInvoicePaymentReversalFixture(t, dbA, reversalFixtureOptions{
		InvoiceTotal: 100000,
		Payments:     []reversalPaymentSeed{{ID: paymentID, TxID: transactionID, Amount: 40000, PaidAt: paidAt, CreatedAt: paidAt}},
		ClosingCivil: "2026-08-20", InvoiceStatus: "OPEN", LegacyPaid: 40000, InitialBalance: 200000,
	})
	paymentWriterHeld := make(chan struct{})
	releasePayment := make(chan struct{})
	paymentHandler := FaturasHandler{
		DB: dbA, WriterDB: dbB, Templates: testInvoicePaymentTemplates(t), WorkspaceID: reversalWorkspaceID, UserID: reversalUserID,
		paymentWriterTestHooks: &invoicePaymentWriterTestHooks{afterBegin: func(*sql.Tx) error {
			close(paymentWriterHeld)
			<-releasePayment
			return nil
		}},
	}
	paymentDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		paymentHandler.HandlePagarFatura(rr, formPost("/cartoes/faturas/pagar", url.Values{
			"invoice_id": {reversalInvoiceID}, "payment_account_id": {reversalPaymentAccountID},
			"payment_mode": {"settle"}, "confirm_settle": {"1"}, "payment_date": {"2026-08-11"},
		}))
		paymentDone <- rr
	}()
	<-paymentWriterHeld

	reversalHandler := FaturasHandler{DB: dbA, WriterDB: nil, WorkspaceID: reversalWorkspaceID, UserID: reversalUserID}
	reversalDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		reversalHandler.handleReverseInvoicePayment(rr, invoicePaymentReversalRequest(validInvoicePaymentReversalValues(paymentID)), time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC), reverseInvoicePayment)
		reversalDone <- rr
	}()
	waitForDBConnectionInUse(t, dbA)
	close(releasePayment)
	if rr := <-paymentDone; rr.Code != http.StatusOK {
		t.Fatalf("payment status = %d body=%q", rr.Code, rr.Body.String())
	}
	if rr := <-reversalDone; rr.Code != http.StatusSeeOther {
		t.Fatalf("reversal status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertSerializablePaymentThenReversalState(t, dbA)
}

func invoicePaymentReversalRequest(values url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/cartoes/faturas/pagamentos/reverter", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func validInvoicePaymentReversalValues(paymentID string) url.Values {
	return url.Values{
		"invoice_payment_id": {paymentID},
		"reason":             {"  Pagamento duplicado  "},
	}
}

func invoicePaymentReversalSQLiteBusyError(t *testing.T) error {
	t.Helper()
	dbA, dbB := openInvoicePaymentReversalSQLiteErrorDBs(t, "busy-base")
	if _, err := dbA.Exec(`CREATE TABLE busy_items (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create busy table: %v", err)
	}
	tx, err := dbA.Begin()
	if err != nil {
		t.Fatalf("begin busy holder: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO busy_items (id) VALUES (1)`); err != nil {
		t.Fatalf("acquire busy write lock: %v", err)
	}
	_, err = dbB.Exec(`INSERT INTO busy_items (id) VALUES (2)`)
	requireInvoicePaymentReversalSQLiteCode(t, err, sqlite3.SQLITE_BUSY)
	return err
}

func invoicePaymentReversalSQLiteLockedError(t *testing.T) error {
	t.Helper()
	db, _ := openInvoicePaymentReversalSQLiteErrorDBs(t, "locked-base")
	if _, err := db.Exec(`CREATE TABLE locked_items (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create locked table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO locked_items (id) VALUES (1)`); err != nil {
		t.Fatalf("seed locked table: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire locked connection: %v", err)
	}
	defer conn.Close()
	rows, err := conn.QueryContext(context.Background(), `SELECT id FROM locked_items`)
	if err != nil {
		t.Fatalf("open locked cursor: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("locked cursor returned no row")
	}
	_, err = conn.ExecContext(context.Background(), `DROP TABLE locked_items`)
	requireInvoicePaymentReversalSQLiteCode(t, err, sqlite3.SQLITE_LOCKED)
	return err
}

func invoicePaymentReversalSQLiteBusySnapshotError(t *testing.T) error {
	t.Helper()
	dbA, dbB := openInvoicePaymentReversalSQLiteErrorDBs(t, "busy-snapshot")
	if _, err := dbA.Exec(`CREATE TABLE snapshot_items (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create snapshot table: %v", err)
	}
	if _, err := dbA.Exec(`INSERT INTO snapshot_items (id, value) VALUES (1, 1)`); err != nil {
		t.Fatalf("seed snapshot table: %v", err)
	}
	tx, err := dbA.Begin()
	if err != nil {
		t.Fatalf("begin snapshot reader: %v", err)
	}
	defer tx.Rollback()
	var value int
	if err := tx.QueryRow(`SELECT value FROM snapshot_items WHERE id = 1`).Scan(&value); err != nil {
		t.Fatalf("establish snapshot: %v", err)
	}
	if _, err := dbB.Exec(`UPDATE snapshot_items SET value = 2 WHERE id = 1`); err != nil {
		t.Fatalf("advance snapshot from second connection: %v", err)
	}
	_, err = tx.Exec(`UPDATE snapshot_items SET value = 3 WHERE id = 1`)
	requireInvoicePaymentReversalSQLiteCode(t, err, sqlite3.SQLITE_BUSY_SNAPSHOT)
	return err
}

func invoicePaymentReversalSQLiteConstraintError(t *testing.T) error {
	t.Helper()
	db, _ := openInvoicePaymentReversalSQLiteErrorDBs(t, "constraint")
	if _, err := db.Exec(`CREATE TABLE unique_items (value TEXT UNIQUE)`); err != nil {
		t.Fatalf("create constraint table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO unique_items (value) VALUES ('duplicate')`); err != nil {
		t.Fatalf("seed constraint table: %v", err)
	}
	_, err := db.Exec(`INSERT INTO unique_items (value) VALUES ('duplicate')`)
	var sqliteErr *sqliteDriver.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("constraint error type = %T, want *sqlite.Error: %v", err, err)
	}
	if sqliteErr.Code()&0xff != sqlite3.SQLITE_CONSTRAINT {
		t.Fatalf("constraint base code = %d, want %d", sqliteErr.Code()&0xff, sqlite3.SQLITE_CONSTRAINT)
	}
	return err
}

func openInvoicePaymentReversalSQLiteErrorDBs(t *testing.T, name string) (*sql.DB, *sql.DB) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), name+".db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
	open := func(label string) *sql.DB {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open %s sqlite error database: %v", label, err)
		}
		db.SetMaxOpenConns(1)
		if err := db.Ping(); err != nil {
			_ = db.Close()
			t.Fatalf("ping %s sqlite error database: %v", label, err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	return open("first"), open("second")
}

func requireInvoicePaymentReversalSQLiteCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected SQLite code %d, got nil", want)
	}
	var sqliteErr *sqliteDriver.Error
	if !errors.As(err, &sqliteErr) {
		t.Fatalf("error type = %T, want *sqlite.Error: %v", err, err)
	}
	if sqliteErr.Code() != want {
		t.Fatalf("SQLite code = %d, want %d; error=%v", sqliteErr.Code(), want, err)
	}
}
