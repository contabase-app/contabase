package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

func TestMoverFaturaParceladaRouteCSRFWorkspaceAndReplay(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	seedTransacoesEditarRBACData(t, db)
	seedMoverFaturaRouteCard(t, db, "workspace-rbac", "route", "card-route", []string{"2030-01", "2030-02", "2030-03"})
	seedMoverFaturaRouteCard(t, db, "workspace-editar-other", "other", "card-other", []string{"2030-01", "2030-02", "2030-03"})

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{29}, 32), ttl: time.Hour}
	mux := http.NewServeMux()
	txBase := handlers.TransactionHandler{DB: db, Templates: transacoesEditarStubTemplates{}}
	registerTransacoesRoutes(mux, db, authService, signer, &txBase)
	session := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)

	form := url.Values{}
	form.Set("expected_invoice_id", "invoice-route-2")
	form.Set("expected_destination_reference", "2030-03")
	form.Set("expected_updated_at", fmt.Sprintf("%d", moverFaturaRouteUpdatedAt(t, db, "tx-route-2")))
	form.Set("expected_move_scope", "chain")
	form.Set("move_scope", "single")

	withoutCSRF := httptest.NewRequest(http.MethodPost, "/transacoes/tx-route-2/mover-fatura", strings.NewReader(form.Encode()))
	withoutCSRF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withoutCSRF.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	withoutCSRFRecorder := httptest.NewRecorder()
	mux.ServeHTTP(withoutCSRFRecorder, withoutCSRF)
	if withoutCSRFRecorder.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status = %d, want 403", withoutCSRFRecorder.Code)
	}

	first := httptest.NewRequest(http.MethodPost, "/transacoes/tx-route-2/mover-fatura", strings.NewReader(form.Encode()))
	first.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	first.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	addAdminUsersRBACCSRF(t, first, signer)
	firstRecorder := httptest.NewRecorder()
	mux.ServeHTTP(firstRecorder, first)
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %q", firstRecorder.Code, firstRecorder.Body.String())
	}

	replay := httptest.NewRequest(http.MethodPost, "/transacoes/tx-route-2/mover-fatura", strings.NewReader(form.Encode()))
	replay.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replay.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	addAdminUsersRBACCSRF(t, replay, signer)
	replayRecorder := httptest.NewRecorder()
	mux.ServeHTTP(replayRecorder, replay)
	if replayRecorder.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want 409, body = %q", replayRecorder.Code, replayRecorder.Body.String())
	}

	foreign := url.Values{}
	foreign.Set("expected_invoice_id", "invoice-other-2")
	foreign.Set("expected_destination_reference", "2030-03")
	foreign.Set("expected_updated_at", fmt.Sprintf("%d", moverFaturaRouteUpdatedAt(t, db, "tx-other-2")))
	foreign.Set("expected_move_scope", "chain")
	foreign.Set("move_scope", "single")
	foreignReq := httptest.NewRequest(http.MethodPost, "/transacoes/tx-other-2/mover-fatura", strings.NewReader(foreign.Encode()))
	foreignReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	foreignReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	addAdminUsersRBACCSRF(t, foreignReq, signer)
	foreignRecorder := httptest.NewRecorder()
	mux.ServeHTTP(foreignRecorder, foreignReq)
	if foreignRecorder.Code != http.StatusNotFound {
		t.Fatalf("foreign workspace status = %d, want 404, body = %q", foreignRecorder.Code, foreignRecorder.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/transacoes/tx-route-2/mover-fatura", nil)
	getReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	getRecorder := httptest.NewRecorder()
	mux.ServeHTTP(getRecorder, getReq)
	if getRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", getRecorder.Code)
	}
}

func seedMoverFaturaRouteCard(t *testing.T, db *sql.DB, workspaceID, prefix, cardID string, references []string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQLForMoverRoute(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, created_at, updated_at)
		VALUES (?, ?, ?, 'CREDIT_CARD', ?, ?)
	`, cardID, workspaceID, "Cartão "+prefix, now, now)
	execTestSQLForMoverRoute(t, db, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES (?, ?, 20, 10, 500000)
	`, "credit-"+cardID, cardID)
	parentID := "tx-" + prefix + "-1"
	for i, reference := range references {
		invoiceID := fmt.Sprintf("invoice-%s-%d", prefix, i+1)
		txID := fmt.Sprintf("tx-%s-%d", prefix, i+1)
		closing, due := moverRouteInvoiceDates(t, reference)
		execTestSQLForMoverRoute(t, db, `
			INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
			VALUES (?, ?, ?, ?, ?, 'OPEN', ?)
		`, invoiceID, cardID, reference, closing, due, now)
		var parent interface{}
		if i > 0 {
			parent = parentID
		}
		execTestSQLForMoverRoute(t, db, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, invoice_id, type, amount, date, description, status, installment_number, total_installments, parent_id, created_at, updated_at)
			VALUES (?, ?, 'admin-user', ?, ?, 'EXPENSE', 10000, ?, ?, 'paid', ?, 3, ?, ?, ?)
		`, txID, workspaceID, cardID, invoiceID, now, "Parcela "+prefix, i+1, parent, now, now)
	}
}

func moverRouteInvoiceDates(t *testing.T, reference string) (closingUnix, dueUnix int64) {
	t.Helper()
	referenceDate, err := time.Parse("2006-01", reference)
	if err != nil {
		t.Fatalf("parse invoice reference %s: %v", reference, err)
	}
	closing := time.Date(referenceDate.Year(), referenceDate.Month(), 20, 12, 0, 0, 0, time.UTC)
	due := time.Date(referenceDate.Year(), referenceDate.Month(), 1, 12, 0, 0, 0, time.UTC).AddDate(0, 1, 9)
	return closing.Unix(), due.Unix()
}

func moverFaturaRouteUpdatedAt(t *testing.T, db *sql.DB, txID string) int64 {
	t.Helper()
	var updatedAt int64
	if err := db.QueryRow(`SELECT updated_at FROM transactions WHERE id = ?`, txID).Scan(&updatedAt); err != nil {
		t.Fatalf("updated_at %s: %v", txID, err)
	}
	return updatedAt
}

func execTestSQLForMoverRoute(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec route fixture: %v\nquery:\n%s", err, query)
	}
}
