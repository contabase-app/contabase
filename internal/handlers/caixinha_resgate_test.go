package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

func TestHandleResgateCaixinhaAdjustsReservedAndFreeWithoutChangingRealBalance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaResgateScenario(t, db)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	before, err := services.CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate balance before: %v", err)
	}

	req := newFormRequest(t, http.MethodPost, "/metas/caixinha/resgate", url.Values{
		"box_id": {"a-box"},
		"amount": {"15,00"},
	})
	rr := httptest.NewRecorder()
	handler.HandleResgateCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	after, err := services.CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate balance after: %v", err)
	}

	if after.RealBalance != before.RealBalance {
		t.Fatalf("real balance changed from %d to %d, want unchanged", before.RealBalance, after.RealBalance)
	}
	if after.ReservedBalance != before.ReservedBalance-1500 {
		t.Fatalf("reserved balance = %d, want %d", after.ReservedBalance, before.ReservedBalance-1500)
	}
	if after.FreeBalance != before.FreeBalance+1500 {
		t.Fatalf("free balance = %d, want %d", after.FreeBalance, before.FreeBalance+1500)
	}

	var amount int64
	var eventType, description string
	if err := db.QueryRow(`
		SELECT amount, type, description
		FROM box_virtual_ledger
		WHERE box_id = 'a-box'
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1
	`).Scan(&amount, &eventType, &description); err != nil {
		t.Fatalf("query latest ledger event: %v", err)
	}
	if amount != -1500 || eventType != "RELEASE" || description != "Liberação de reserva" {
		t.Fatalf("latest ledger event = amount=%d type=%s desc=%q, want -1500/RELEASE/Liberação de reserva", amount, eventType, description)
	}
}

func TestHandleResgateCaixinhaRejectsAboveReserved(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaResgateScenario(t, db)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	before, err := services.CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate balance before: %v", err)
	}
	beforeCount := countBoxLedgerRows(t, db, "a-box")

	req := newFormRequest(t, http.MethodPost, "/metas/caixinha/resgate", url.Values{
		"box_id": {"a-box"},
		"amount": {"60,00"},
	})
	rr := httptest.NewRecorder()
	handler.HandleResgateCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "erro ao registrar liberação") {
		t.Fatalf("response should contain release error, got: %s", rr.Body.String())
	}

	after, err := services.CalculateWorkspaceReserveBalance(db, "ws-a")
	if err != nil {
		t.Fatalf("calculate balance after: %v", err)
	}
	if after != before {
		t.Fatalf("balances changed on blocked release: before=%+v after=%+v", before, after)
	}
	afterCount := countBoxLedgerRows(t, db, "a-box")
	if afterCount != beforeCount {
		t.Fatalf("ledger rows changed on blocked release: before=%d after=%d", beforeCount, afterCount)
	}
}

func TestHandleResgateCaixinhaRejectsInvalidAmount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaResgateScenario(t, db)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	for _, amount := range []string{"", "0,00", "abc"} {
		req := newFormRequest(t, http.MethodPost, "/metas/caixinha/resgate", url.Values{
			"box_id": {"a-box"},
			"amount": {amount},
		})
		rr := httptest.NewRecorder()
		handler.HandleResgateCaixinha(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status for amount %q = %d, want %d", amount, rr.Code, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), "resgate inválido") {
			t.Fatalf("response for amount %q should contain invalid release, got: %s", amount, rr.Body.String())
		}
	}
}

func TestHandleResgateCaixinhaRejectsForeignWorkspaceBox(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaResgateScenario(t, db)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	beforeCount := countBoxLedgerRows(t, db, "b-box")
	req := newFormRequest(t, http.MethodPost, "/metas/caixinha/resgate", url.Values{
		"box_id": {"b-box"},
		"amount": {"10,00"},
	})
	rr := httptest.NewRecorder()
	handler.HandleResgateCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "erro ao registrar liberação") {
		t.Fatalf("response should contain release error, got: %s", rr.Body.String())
	}

	afterCount := countBoxLedgerRows(t, db, "b-box")
	if afterCount != beforeCount {
		t.Fatalf("foreign ledger rows changed: before=%d after=%d", beforeCount, afterCount)
	}
}

func seedCaixinhaResgateScenario(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES
			('user-a', 'User A', 'user-a@example.com', 'hash', ?, ?),
			('user-b', 'User B', 'user-b@example.com', 'hash', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES
			('ws-a', 'Workspace A', '', 'personal', ?, ?),
			('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES
			('ws-a', 'user-a', 'ADMIN', ?),
			('ws-b', 'user-b', 'ADMIN', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES
			('a-checking', 'ws-a', 'Conta A', 'CHECKING', 10000, 10000, ?, ?),
			('a-card', 'ws-a', 'Cartão A', 'CREDIT_CARD', 0, 0, ?, ?),
			('b-checking', 'ws-b', 'Conta B', 'CHECKING', 5000, 5000, ?, ?)
	`, now, now, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('a-cat', 'ws-a', 'Categoria A', 'EXPENSE', ?),
			('b-cat', 'ws-b', 'Categoria B', 'EXPENSE', ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('a-box', 'ws-a', 'Caixinha A', 'a-cat', 100000, 10000, ?, ?),
			('b-box', 'ws-b', 'Caixinha B', 'b-cat', 100000, 10000, ?, ?)
	`, now, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('a-ledger-1', 'a-box', 5000, 'RECHARGE', 'Aporte', ?, ?),
			('b-ledger-1', 'b-box', 2000, 'RECHARGE', 'Aporte', ?, ?)
	`, now, now, now, now)
}

func countBoxLedgerRows(t *testing.T, db *sql.DB, boxID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE box_id = ?`, boxID).Scan(&count); err != nil {
		t.Fatalf("count box ledger rows: %v", err)
	}
	return count
}

func newFormRequest(t *testing.T, method, path string, form url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}
