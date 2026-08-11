package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func TestInvoicePaymentReversalRouteRequiresPOSTAndBodyLimit(t *testing.T) {
	fixture := newInvoicePaymentReversalRouteFixture(t, models.RoleAdmin, models.WorkspaceTypePersonal)

	get := httptest.NewRequest(http.MethodGet, invoicePaymentReversalPath, nil)
	addInvoicePaymentReversalSession(get, fixture.session)
	getResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(getResponse, get)
	if getResponse.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", getResponse.Code)
	}

	largeBody := strings.Repeat("x", invoicePaymentReversalMaxBodyBytes+1)
	post := httptest.NewRequest(http.MethodPost, invoicePaymentReversalPath, strings.NewReader(largeBody))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	largeResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(largeResponse, post)
	if largeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status = %d, want 413", largeResponse.Code)
	}

	chunked := httptest.NewRequest(http.MethodPost, invoicePaymentReversalPath, strings.NewReader(largeBody))
	chunked.ContentLength = -1
	chunked.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	chunkedResponse := httptest.NewRecorder()
	fixture.handler.ServeHTTP(chunkedResponse, chunked)
	if chunkedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked large body status = %d, want 413", chunkedResponse.Code)
	}
}

func TestInvoicePaymentReversalRouteRequiresAuthentication(t *testing.T) {
	fixture := newInvoicePaymentReversalRouteFixture(t, models.RoleAdmin, models.WorkspaceTypePersonal)
	req := serverInvoicePaymentReversalRequest(fixture.paymentID)
	addInvoicePaymentReversalCSRF(req, fixture.signer)
	rr := httptest.NewRecorder()
	fixture.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("response = %d location=%q body=%q", rr.Code, rr.Header().Get("Location"), rr.Body.String())
	}
}

func TestInvoicePaymentReversalRouteRequiresValidCSRF(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*http.Request, *csrfSigner)
	}{
		{name: "missing", setup: func(*http.Request, *csrfSigner) {}},
		{name: "mismatch", setup: func(req *http.Request, signer *csrfSigner) {
			token := signer.issue(time.Now())
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
			req.Header.Set(csrfHeaderName, token+"-different")
		}},
		{name: "expired", setup: func(req *http.Request, signer *csrfSigner) {
			token := signer.issue(time.Now().Add(-2 * time.Hour))
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
			req.Header.Set(csrfHeaderName, token)
		}},
		{name: "different signer", setup: func(req *http.Request, _ *csrfSigner) {
			other := &csrfSigner{secret: bytes.Repeat([]byte{99}, 32), ttl: time.Hour}
			token := other.issue(time.Now())
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
			req.Header.Set(csrfHeaderName, token)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newInvoicePaymentReversalRouteFixture(t, models.RoleAdmin, models.WorkspaceTypePersonal)
			req := serverInvoicePaymentReversalRequest(fixture.paymentID)
			addInvoicePaymentReversalSession(req, fixture.session)
			tt.setup(req, fixture.signer)
			rr := httptest.NewRecorder()
			fixture.handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%q", rr.Code, rr.Body.String())
			}
			assertServerInvoicePaymentActive(t, fixture.db, fixture.paymentID)
		})
	}
}

func TestInvoicePaymentReversalRouteAcceptsCSRFFormField(t *testing.T) {
	fixture := newInvoicePaymentReversalRouteFixture(t, models.RoleAdmin, models.WorkspaceTypePersonal)
	token := fixture.signer.issue(time.Now())
	values := url.Values{
		"invoice_payment_id": {fixture.paymentID},
		"reason":             {"Pagamento duplicado"},
		"csrf_token":         {token},
	}
	req := httptest.NewRequest(http.MethodPost, invoicePaymentReversalPath, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addInvoicePaymentReversalSession(req, fixture.session)
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	rr := httptest.NewRecorder()
	fixture.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%q", rr.Code, rr.Body.String())
	}
	assertServerInvoicePaymentReversed(t, fixture.db, fixture.paymentID)
}

func TestInvoicePaymentReversalRouteRBACPersonalAndBusiness(t *testing.T) {
	tests := []struct {
		name          string
		role          string
		workspaceType string
		want          int
	}{
		{name: "personal admin", role: models.RoleAdmin, workspaceType: models.WorkspaceTypePersonal, want: http.StatusSeeOther},
		{name: "business admin", role: models.RoleAdmin, workspaceType: models.WorkspaceTypeBusiness, want: http.StatusSeeOther},
		{name: "business manager", role: models.RoleManager, workspaceType: models.WorkspaceTypeBusiness, want: http.StatusSeeOther},
		{name: "business user", role: models.RoleUser, workspaceType: models.WorkspaceTypeBusiness, want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newInvoicePaymentReversalRouteFixture(t, tt.role, tt.workspaceType)
			req := serverInvoicePaymentReversalRequest(fixture.paymentID)
			addInvoicePaymentReversalSession(req, fixture.session)
			addInvoicePaymentReversalCSRF(req, fixture.signer)
			rr := httptest.NewRecorder()
			fixture.handler.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tt.want, rr.Body.String())
			}
			if tt.want == http.StatusForbidden {
				assertServerInvoicePaymentActive(t, fixture.db, fixture.paymentID)
			} else {
				assertServerInvoicePaymentReversed(t, fixture.db, fixture.paymentID)
			}
		})
	}
}

func TestInvoicePaymentReversalRouteForeignWorkspaceIsNotFound(t *testing.T) {
	fixture := newInvoicePaymentReversalRouteFixture(t, models.RoleAdmin, models.WorkspaceTypeBusiness)
	foreignPaymentID := uuid.NewString()
	seedServerInvoicePaymentReversal(t, fixture.db, "foreign-workspace", "foreign-user", foreignPaymentID, models.WorkspaceTypeBusiness, models.RoleAdmin)

	req := serverInvoicePaymentReversalRequest(foreignPaymentID)
	addInvoicePaymentReversalSession(req, fixture.session)
	addInvoicePaymentReversalCSRF(req, fixture.signer)
	rr := httptest.NewRecorder()
	fixture.handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "não encontrado") {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertServerInvoicePaymentActive(t, fixture.db, foreignPaymentID)
}

func TestInvoicePaymentReversalRouteConcurrent(t *testing.T) {
	fixture := newInvoicePaymentReversalRouteFixture(t, models.RoleAdmin, models.WorkspaceTypePersonal)
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := serverInvoicePaymentReversalRequest(fixture.paymentID)
			addInvoicePaymentReversalSession(req, fixture.session)
			addInvoicePaymentReversalCSRF(req, fixture.signer)
			rr := httptest.NewRecorder()
			fixture.handler.ServeHTTP(rr, req)
			statuses <- rr.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusSeeOther] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("statuses = %#v", counts)
	}
	assertServerSingleInvoicePaymentReversalEffect(t, fixture.db, fixture.paymentID)
}

func TestInvoicePaymentReversalRouteRunsInsideRuntimeLease(t *testing.T) {
	root := t.TempDir()
	runtime := openFullBindingRuntime(t, filepath.Join(root, "lease.db"), "lease")
	defer runtime.Close()
	paymentID := uuid.NewString()
	userID, workspaceID := "lease-user", "lease-workspace"
	seedServerInvoicePaymentReversal(t, runtime.DBs.DB, workspaceID, userID, paymentID, models.WorkspaceTypePersonal, models.RoleAdmin)
	authService := auth.NewService(runtime.DBs.DB)
	session, _, err := authService.CreateSession(userID, workspaceID, time.Hour, false)
	if err != nil {
		t.Fatalf("create lease session: %v", err)
	}
	signer := &csrfSigner{secret: bytes.Repeat([]byte{18}, 32), ttl: time.Hour}
	base := handlers.FaturasHandler{DB: runtime.DBs.DB, WriterDB: runtime.DBs.Immediate}
	mux := http.NewServeMux()
	mux.HandleFunc(invoicePaymentReversalPath, invoicePaymentReversalRoute(authService, signer, &base))
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !runtimeLeaseHeld(r.Context()) {
			http.Error(w, "runtime lease missing", http.StatusInternalServerError)
			return
		}
		mux.ServeHTTP(w, r)
	})
	coordinator := newServerRuntimeCoordinator(runtime, nil)
	handler := coordinator.Middleware(probe, isRuntimeLeaseBypassRequest)

	req := serverInvoicePaymentReversalRequest(paymentID)
	addInvoicePaymentReversalSession(req, session)
	addInvoicePaymentReversalCSRF(req, signer)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertServerInvoicePaymentReversed(t, runtime.DBs.DB, paymentID)
}

func TestInvoicePaymentReversalRouteUsesCurrentRuntimeDBGeneration(t *testing.T) {
	root := t.TempDir()
	g1 := openFullBindingRuntime(t, filepath.Join(root, "g1.db"), "g1")
	g2 := openFullBindingRuntime(t, filepath.Join(root, "g2.db"), "g2")
	authService := auth.NewService(g1.DBs.DB)
	base := handlers.FaturasHandler{DB: g1.DBs.DB, WriterDB: g1.DBs.Immediate}
	bind := func(runtime *serverRuntime) {
		authService.DB = runtime.DBs.DB
		base.DB = runtime.DBs.DB
		base.WriterDB = runtime.DBs.Immediate
	}
	coordinator := newServerRuntimeCoordinator(g1, bind)
	defer coordinator.Close()
	signer := &csrfSigner{secret: bytes.Repeat([]byte{19}, 32), ttl: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc(invoicePaymentReversalPath, invoicePaymentReversalRoute(authService, signer, &base))
	handler := coordinator.Middleware(mux, isRuntimeLeaseBypassRequest)

	g1PaymentID := uuid.NewString()
	seedServerInvoicePaymentReversal(t, g1.DBs.DB, "g1-workspace", "g1-user", g1PaymentID, models.WorkspaceTypePersonal, models.RoleAdmin)
	g1Session, _, err := auth.NewService(g1.DBs.DB).CreateSession("g1-user", "g1-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create G1 session: %v", err)
	}
	g1Req := serverInvoicePaymentReversalRequest(g1PaymentID)
	addInvoicePaymentReversalSession(g1Req, g1Session)
	addInvoicePaymentReversalCSRF(g1Req, signer)
	g1Response := httptest.NewRecorder()
	handler.ServeHTTP(g1Response, g1Req)
	if g1Response.Code != http.StatusSeeOther {
		t.Fatalf("G1 status = %d body=%q", g1Response.Code, g1Response.Body.String())
	}

	oldDB := g1.DBs.DB
	if err := coordinator.Replace(nil, func(old *serverRuntime) (*serverRuntime, error) {
		if err := old.Close(); err != nil {
			return nil, err
		}
		return g2, nil
	}); err != nil {
		t.Fatalf("replace G1 with G2: %v", err)
	}
	if oldDB.Ping() == nil {
		t.Fatal("G1 normal DB remained open after replacement")
	}

	g2PaymentID := uuid.NewString()
	seedServerInvoicePaymentReversal(t, g2.DBs.DB, "g2-workspace", "g2-user", g2PaymentID, models.WorkspaceTypeBusiness, models.RoleAdmin)
	g2Session, _, err := auth.NewService(g2.DBs.DB).CreateSession("g2-user", "g2-workspace", time.Hour, false)
	if err != nil {
		t.Fatalf("create G2 session: %v", err)
	}
	g2Req := serverInvoicePaymentReversalRequest(g2PaymentID)
	addInvoicePaymentReversalSession(g2Req, g2Session)
	addInvoicePaymentReversalCSRF(g2Req, signer)
	g2Response := httptest.NewRecorder()
	handler.ServeHTTP(g2Response, g2Req)
	if g2Response.Code != http.StatusSeeOther {
		t.Fatalf("G2 status = %d body=%q", g2Response.Code, g2Response.Body.String())
	}
	assertServerInvoicePaymentReversed(t, g2.DBs.DB, g2PaymentID)
}

type invoicePaymentReversalRouteFixture struct {
	db        *sql.DB
	handler   http.Handler
	signer    *csrfSigner
	session   string
	paymentID string
}

func newInvoicePaymentReversalRouteFixture(t *testing.T, role, workspaceType string) invoicePaymentReversalRouteFixture {
	t.Helper()
	db := openAdminUsersRBACTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`UPDATE workspaces SET type = ? WHERE id = 'workspace-rbac'`, workspaceType); err != nil {
		t.Fatalf("update workspace type: %v", err)
	}
	userID := map[string]string{
		models.RoleAdmin:   "admin-user",
		models.RoleManager: "manager-user",
		models.RoleUser:    "regular-user",
	}[role]
	paymentID := uuid.NewString()
	seedServerInvoicePaymentReversalFinancials(t, db, "workspace-rbac", userID, paymentID)
	authService := auth.NewService(db)
	session, _, err := authService.CreateSession(userID, "workspace-rbac", time.Hour, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	signer := &csrfSigner{secret: bytes.Repeat([]byte{17}, 32), ttl: time.Hour}
	base := handlers.FaturasHandler{DB: db}
	mux := http.NewServeMux()
	mux.HandleFunc(invoicePaymentReversalPath, invoicePaymentReversalRoute(authService, signer, &base))
	return invoicePaymentReversalRouteFixture{db: db, handler: mux, signer: signer, session: session, paymentID: paymentID}
}

func seedServerInvoicePaymentReversal(t *testing.T, db *sql.DB, workspaceID, userID, paymentID, workspaceType, role string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
		VALUES (?, ?, ?, 'hash', 'active', ?, ?)
	`, userID, userID, userID+"@example.test", now, now); err != nil {
		t.Fatalf("seed server reversal user: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES (?, ?, '', ?, ?, ?)
	`, workspaceID, workspaceID, workspaceType, now, now); err != nil {
		t.Fatalf("seed server reversal workspace: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, ?, ?)
	`, workspaceID, userID, role, now); err != nil {
		t.Fatalf("seed server reversal membership: %v", err)
	}
	seedServerInvoicePaymentReversalFinancials(t, db, workspaceID, userID, paymentID)
}

func seedServerInvoicePaymentReversalFinancials(t *testing.T, db *sql.DB, workspaceID, userID, paymentID string) {
	t.Helper()
	now := time.Now().Unix()
	suffix := strings.ReplaceAll(paymentID, "-", "")
	checkingID := "checking-" + suffix
	cardID := "card-" + suffix
	creditCardID := "cc-" + suffix
	invoiceID := "invoice-" + suffix
	purchaseID := "purchase-" + suffix
	paymentTransactionID := uuid.NewString()
	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "accounts", query: `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES (?, ?, 'Checking', 'CHECKING', 200000, 100000, ?, ?),
		       (?, ?, 'Card', 'CREDIT_CARD', 0, 0, ?, ?)`, args: []any{checkingID, workspaceID, now, now, cardID, workspaceID, now, now}},
		{name: "credit card", query: `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES (?, ?, 20, 10, 500000)`, args: []any{creditCardID, cardID}},
		{name: "invoice", query: `
		INSERT INTO invoices (id, account_id, reference, closing_date, closing_date_civil, due_date, due_date_civil, status, paid_amount, paid_at, created_at)
		VALUES (?, ?, '2026-08', ?, '2026-08-20', ?, '2026-09-10', 'PAID', 100000, ?, ?)`, args: []any{invoiceID, cardID, now, now + 86400, now, now}},
		{name: "purchase", query: `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, date_civil, description, status, paid_at, paid_date_civil, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'EXPENSE', 100000, ?, '2026-08-02', 'Purchase', 'paid', ?, '2026-08-02', 1, 1, ?, ?)`, args: []any{purchaseID, workspaceID, userID, cardID, invoiceID, now, now, now, now}},
		{name: "payment transaction", query: `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, date_civil, description, status, paid_at, paid_date_civil, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'EXPENSE', 100000, ?, '2026-08-05', 'Invoice payment', 'paid', ?, '2026-08-05', 1, 1, ?, ?)`, args: []any{paymentTransactionID, workspaceID, userID, checkingID, now, now, now, now}},
		{name: "invoice payment", query: `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, source, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, 100000, ?, 'manual', ?, ?)
	`, args: []any{paymentID, workspaceID, invoiceID, checkingID, paymentTransactionID, now, userID, now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed server reversal %s: %v", statement.name, err)
		}
	}
}

func serverInvoicePaymentReversalRequest(paymentID string) *http.Request {
	values := url.Values{"invoice_payment_id": {paymentID}, "reason": {"Pagamento duplicado"}}
	req := httptest.NewRequest(http.MethodPost, invoicePaymentReversalPath, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func addInvoicePaymentReversalSession(req *http.Request, session string) {
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
}

func addInvoicePaymentReversalCSRF(req *http.Request, signer *csrfSigner) {
	token := signer.issue(time.Now())
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
	req.Header.Set(csrfHeaderName, token)
}

func assertServerInvoicePaymentActive(t *testing.T, db *sql.DB, paymentID string) {
	t.Helper()
	var reversedAt sql.NullInt64
	var reversalTransactionID sql.NullString
	if err := db.QueryRow(`SELECT reversed_at, reversal_transaction_id FROM invoice_payments WHERE id = ?`, paymentID).Scan(&reversedAt, &reversalTransactionID); err != nil {
		t.Fatalf("query active payment: %v", err)
	}
	if reversedAt.Valid || reversalTransactionID.Valid {
		t.Fatalf("payment unexpectedly reversed: reversed_at=%v reversal_transaction_id=%v", reversedAt, reversalTransactionID)
	}
}

func assertServerInvoicePaymentReversed(t *testing.T, db *sql.DB, paymentID string) {
	t.Helper()
	var reversedAt int64
	var reversalTransactionID string
	if err := db.QueryRow(`SELECT reversed_at, reversal_transaction_id FROM invoice_payments WHERE id = ?`, paymentID).Scan(&reversedAt, &reversalTransactionID); err != nil {
		t.Fatalf("query reversed payment: %v", err)
	}
	if reversedAt <= 0 || strings.TrimSpace(reversalTransactionID) == "" {
		t.Fatalf("payment reversal provenance missing: reversed_at=%d reversal_transaction_id=%q", reversedAt, reversalTransactionID)
	}
}

func assertServerSingleInvoicePaymentReversalEffect(t *testing.T, db *sql.DB, paymentID string) {
	t.Helper()
	assertServerInvoicePaymentReversed(t, db, paymentID)
	var accountID string
	if err := db.QueryRow(`SELECT account_id FROM invoice_payments WHERE id = ?`, paymentID).Scan(&accountID); err != nil {
		t.Fatalf("query payment account: %v", err)
	}
	var balance int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE id = ?`, accountID).Scan(&balance); err != nil {
		t.Fatalf("query balance: %v", err)
	}
	if balance != 200000 {
		t.Fatalf("balance = %d, want 200000", balance)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE description = 'Reversão de pagamento de fatura'`).Scan(&count); err != nil {
		t.Fatalf("count reversal transactions: %v", err)
	}
	if count != 1 {
		t.Fatalf("reversal transaction count = %d, want 1", count)
	}
}
