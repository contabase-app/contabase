package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/models"
)

func TestAdminBoxLedgerReconcileRouteRequiresGlobalAdmin(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{5}, 32), ttl: time.Hour}
	handler := newAdminBoxLedgerReconcileTestHandler(db, authService, signer)

	sessions := map[string]string{
		models.RoleAdmin:   createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin),
		models.RoleManager: createAdminUsersRBACSession(t, authService, "manager-user", models.RoleManager),
		models.RoleUser:    createAdminUsersRBACSession(t, authService, "regular-user", models.RoleUser),
	}

	cases := []struct {
		name string
		role string
		want int
	}{
		{"admin can reconcile", models.RoleAdmin, http.StatusOK},
		{"manager cannot reconcile", models.RoleManager, http.StatusForbidden},
		{"user cannot reconcile", models.RoleUser, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/caixinhas/ledger/reconciliar?workspace_id=workspace-rbac", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessions[tc.role]})
			rr := httptest.NewRecorder()

			handler.ServeHTTP(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %q", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestAdminBoxLedgerReconcileRouteReturnsStructuredIssues(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, created_at)
		VALUES ('cat-rbac-ledger', 'workspace-rbac', 'Categoria Ledger', 'tag', '#6b7280', 'EXPENSE', ?)
	`, now); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('box-rbac-ledger', 'workspace-rbac', 'Caixinha Ledger', 'cat-rbac-ledger', 0, 0, ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed box: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, source_transaction_id, reference_date, created_at)
		VALUES ('ledger-orphan-consume', 'box-rbac-ledger', -900, 'CONSUME', 'Orfão', 'tx-missing-ledger', ?, ?)
	`, now, now); err != nil {
		t.Fatalf("seed orphan consume: %v", err)
	}

	authService := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{6}, 32), ttl: time.Hour}
	handler := newAdminBoxLedgerReconcileTestHandler(db, authService, signer)
	adminSession := createAdminUsersRBACSession(t, authService, "admin-user", models.RoleAdmin)

	req := httptest.NewRequest(http.MethodGet, "/admin/caixinhas/ledger/reconciliar?workspace_id=workspace-rbac", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: adminSession})
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%q", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}

	var payload adminBoxLedgerReconcilePayload
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v body=%q", err, rr.Body.String())
	}
	if payload.WorkspaceID != "workspace-rbac" {
		t.Fatalf("workspace_id = %q, want workspace-rbac", payload.WorkspaceID)
	}
	if payload.IssueCount == 0 {
		t.Fatalf("issue_count = 0, want > 0")
	}
	if len(payload.IssueCodes) == 0 {
		t.Fatalf("issue_codes is empty")
	}
	if len(payload.Issues) == 0 {
		t.Fatalf("issues is empty")
	}

	found := false
	for _, issue := range payload.Issues {
		if issue.Code == "active_consume_missing_transaction" {
			found = true
			if issue.BoxID != "box-rbac-ledger" {
				t.Fatalf("box_id = %q, want box-rbac-ledger", issue.BoxID)
			}
			if issue.SourceTransactionID != "tx-missing-ledger" {
				t.Fatalf("source_transaction_id = %q, want tx-missing-ledger", issue.SourceTransactionID)
			}
			if issue.LedgerID != "ledger-orphan-consume" {
				t.Fatalf("ledger_id = %q, want ledger-orphan-consume", issue.LedgerID)
			}
			if issue.Description == "" {
				t.Fatalf("description should not be empty")
			}
		}
	}
	if !found {
		t.Fatalf("expected active_consume_missing_transaction in issues: %+v", payload.Issues)
	}
}

func newAdminBoxLedgerReconcileTestHandler(db *sql.DB, authService *auth.Service, signer *csrfSigner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/caixinhas/ledger/reconciliar", withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		handleAdminBoxLedgerReconcile(w, r, db, ctx)
	}))
	return mux
}
