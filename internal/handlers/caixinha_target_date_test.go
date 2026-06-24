package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHandleCriarCaixinhaPersistsTargetDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaTargetDateScenario(t, db)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	targetMonth := time.Now().UTC().AddDate(0, 2, 0).Format("2006-01")
	req := newCaixinhaRequest(t, url.Values{
		"name":             {"Viagem"},
		"category_id":      {"a-cat"},
		"target_amount":    {"1.000,00"},
		"monthly_recharge": {"200,00"},
		"target_month":     {targetMonth},
	})
	rr := httptest.NewRecorder()
	handler.HandleCriarCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var targetDate sql.NullInt64
	if err := db.QueryRow(`SELECT target_date FROM boxes WHERE workspace_id = ? AND name = ?`, "ws-a", "Viagem").Scan(&targetDate); err != nil {
		t.Fatalf("query created box target_date: %v", err)
	}
	if !targetDate.Valid {
		t.Fatalf("target_date not persisted")
	}
	wantDate, err := time.Parse("2006-01", targetMonth)
	if err != nil {
		t.Fatalf("parse target month: %v", err)
	}
	wantUnix := time.Date(wantDate.Year(), wantDate.Month(), 1, 12, 0, 0, 0, time.UTC).Unix()
	if targetDate.Int64 != wantUnix {
		t.Fatalf("target_date = %d, want %d", targetDate.Int64, wantUnix)
	}
}

func TestHandleCriarCaixinhaClearsTargetDate(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaTargetDateScenario(t, db)

	now := time.Now().UTC().Unix()
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, target_date, created_at, updated_at)
		VALUES ('a-box', 'ws-a', 'Meta A', 'a-cat', 100000, 5000, ?, ?, ?)
	`, time.Now().UTC().AddDate(0, 4, 0).Unix(), now, now)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := newCaixinhaRequest(t, url.Values{
		"box_id":            {"a-box"},
		"name":              {"Meta A"},
		"category_id":       {"a-cat"},
		"target_amount":     {"1.000,00"},
		"monthly_recharge":  {"200,00"},
		"target_month":      {""},
		"months_to_target":  {"0"},
		"target_month_hint": {""},
	})
	rr := httptest.NewRecorder()
	handler.HandleCriarCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var targetDate sql.NullInt64
	if err := db.QueryRow(`SELECT target_date FROM boxes WHERE id = 'a-box'`).Scan(&targetDate); err != nil {
		t.Fatalf("query updated box target_date: %v", err)
	}
	if targetDate.Valid {
		t.Fatalf("target_date should be NULL after clear, got %d", targetDate.Int64)
	}
}

func TestHandleCriarCaixinhaUpdateRespectsWorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaTargetDateScenario(t, db)

	now := time.Now().UTC().Unix()
	foreignTarget := time.Now().UTC().AddDate(0, 5, 0).Unix()
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, target_date, created_at, updated_at)
		VALUES ('b-box', 'ws-b', 'Meta B', 'b-cat', 200000, 7000, ?, ?, ?)
	`, foreignTarget, now, now)

	handler := MetasHandler{
		DB:          db,
		Templates:   testMetasMutationTemplates(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := newCaixinhaRequest(t, url.Values{
		"box_id":            {"b-box"},
		"name":              {"Meta B"},
		"category_id":       {"a-cat"},
		"target_amount":     {"2.000,00"},
		"monthly_recharge":  {"300,00"},
		"target_month":      {""},
		"months_to_target":  {"0"},
		"target_month_hint": {""},
	})
	rr := httptest.NewRecorder()
	handler.HandleCriarCaixinha(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "erro ao salvar reserva") {
		t.Fatalf("response should contain save error, got: %s", rr.Body.String())
	}

	var targetDate sql.NullInt64
	if err := db.QueryRow(`SELECT target_date FROM boxes WHERE id = 'b-box'`).Scan(&targetDate); err != nil {
		t.Fatalf("query foreign box target_date: %v", err)
	}
	if !targetDate.Valid || targetDate.Int64 != foreignTarget {
		t.Fatalf("foreign box target_date changed: valid=%v value=%d want=%d", targetDate.Valid, targetDate.Int64, foreignTarget)
	}
}

func newCaixinhaRequest(t *testing.T, form url.Values) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/metas/caixinha", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func testMetasMutationTemplates(t *testing.T) TemplateEngine {
	t.Helper()
	return template.Must(template.New("metas").Parse(`
{{define "metas-tabs"}}<div id="metas-tabs" data-active-tab="caixinhas"></div>{{end}}`))
}

func seedCaixinhaTargetDateScenario(t *testing.T, db *sql.DB) {
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
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('a-cat', 'ws-a', 'A Cat', 'EXPENSE', ?),
			('b-cat', 'ws-b', 'B Cat', 'EXPENSE', ?)
	`, now, now)
}
