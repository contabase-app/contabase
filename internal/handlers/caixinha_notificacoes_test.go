package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBoxesInOverdraftReturnsBoxesWithNegativeBalance(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	items := h.boxesInOverdraft(false)
	if len(items) != 1 {
		t.Fatalf("overdraft items count = %d, want 1", len(items))
	}
	if items[0].Key != "box_overdraft:a-box-overdraft" {
		t.Fatalf("overdraft key = %q, want box_overdraft:a-box-overdraft", items[0].Key)
	}
	if items[0].Title != "Reserva em excedente" {
		t.Fatalf("overdraft title = %q", items[0].Title)
	}
	if items[0].Icon != "alert-triangle" || items[0].Color != "rose" {
		t.Fatalf("overdraft icon/color = %s/%s, want alert-triangle/rose", items[0].Icon, items[0].Color)
	}
}

func TestBoxesInOverdraftEmptyWhenNoNegativeBoxes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-x', 'User X', 'x@e.com', 'h', 1, 1)
	`)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, created_at, updated_at)
		VALUES ('ws-x', 'WS X', '', 1, 1)
	`)

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-x",
		UserID:      "user-x",
	}

	items := h.boxesInOverdraft(false)
	if len(items) != 0 {
		t.Fatalf("overdraft items count = %d, want 0", len(items))
	}
}

func TestCompletedBoxGoalsReturnsCompletedBoxes(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	items := h.completedBoxGoals(false)
	if len(items) != 1 {
		t.Fatalf("completed goals count = %d, want 1", len(items))
	}
	if items[0].Key != "box_goal:a-box-done" {
		t.Fatalf("goal key = %q, want box_goal:a-box-done", items[0].Key)
	}
	if items[0].Title != "Meta de reserva atingida" {
		t.Fatalf("goal title = %q", items[0].Title)
	}
	if items[0].Icon != "trophy" || items[0].Color != "emerald" {
		t.Fatalf("goal icon/color = %s/%s, want trophy/emerald", items[0].Icon, items[0].Color)
	}
}

func TestInsertAndReadCaixinhaPersistedNotifications(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	insertCaixinhaNotification(db, "user-a", "ws-a", "aporte teste", "mensagem de aporte", "caixinha.aporte")
	insertCaixinhaNotification(db, "user-a", "ws-a", "resgate teste", "mensagem de resgate", "caixinha.resgate")

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	items := h.caixinhaPersistedNotifications()
	if len(items) < 2 {
		t.Fatalf("persisted notifications count = %d, want >= 2", len(items))
	}

	foundAporte := false
	foundResgate := false
	for _, item := range items {
		if strings.HasPrefix(item.Key, "caixinha_notif:") {
			if item.Title == "aporte teste" {
				foundAporte = true
				if item.Icon != "piggy-bank" || item.Color != "violet" {
					t.Fatalf("aporte icon/color = %s/%s", item.Icon, item.Color)
				}
			}
			if item.Title == "resgate teste" {
				foundResgate = true
				if item.Icon != "arrow-up-from-line" || item.Color != "amber" {
					t.Fatalf("resgate icon/color = %s/%s", item.Icon, item.Color)
				}
			}
		}
	}
	if !foundAporte {
		t.Fatalf("did not find persisted aporte notification")
	}
	if !foundResgate {
		t.Fatalf("did not find persisted resgate notification")
	}
}

func TestCaixinhaNotificationsHandleApagar(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	overdraftItems := h.boxesInOverdraft(false)
	if len(overdraftItems) == 0 {
		t.Fatal("expected at least one overdraft item")
	}

	req := httptest.NewRequest(http.MethodDelete, "/notificacoes/box_overdraft:a-box-overdraft", nil)
	rr := httptest.NewRecorder()
	h.HandleApagarNotificacao(rr, req, "box_overdraft:a-box-overdraft")
	if rr.Code != http.StatusOK {
		t.Fatalf("dismiss status = %d", rr.Code)
	}

	visible := h.visibleItemsForUser(h.boxesInOverdraft(false))
	if len(visible) != 0 {
		t.Fatalf("overdraft items after dismiss = %d, want 0", len(visible))
	}
}

func TestCaixinhaNotificationsHandleLimparTudo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	req := httptest.NewRequest(http.MethodDelete, "/notificacoes/limpar", nil)
	rr := httptest.NewRecorder()
	h.HandleLimparTudo(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("limpar tudo status = %d", rr.Code)
	}

	allItems := h.boxesInOverdraft(false)
	allItems = append(allItems, h.completedBoxGoals(false)...)
	visible := h.visibleItemsForUser(allItems)
	if len(visible) != 0 {
		t.Fatalf("items after limpar tudo = %d, want 0", len(visible))
	}
}

func TestCaixinhaNotificationsAreWorkspaceScoped(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-b",
		UserID:      "user-b",
	}

	items := h.boxesInOverdraft(false)
	if len(items) != 0 {
		t.Fatalf("workspace B overdraft items = %d, want 0 (box is in ws-a)", len(items))
	}

	goals := h.completedBoxGoals(false)
	if len(goals) != 0 {
		t.Fatalf("workspace B goals items = %d, want 0", len(goals))
	}

	insertCaixinhaNotification(db, "user-b", "ws-b", "msg b", "desc", "caixinha.aporte")
	persisted := h.caixinhaPersistedNotifications()
	if len(persisted) != 1 {
		t.Fatalf("persisted notifications for user-b/ws-b = %d, want 1", len(persisted))
	}
}

func TestPersistedNotificationsFilteredByWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	insertCaixinhaNotification(db, "user-a", "ws-a", "msg ws-a", "desc ws-a", "caixinha.aporte")
	insertCaixinhaNotification(db, "user-a", "ws-b", "msg ws-b", "desc ws-b", "caixinha.aporte")

	hWSA := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}
	itemsWSA := hWSA.caixinhaPersistedNotifications()
	foundWSA := false
	foundWSB := false
	for _, item := range itemsWSA {
		if item.Title == "msg ws-a" {
			foundWSA = true
		}
		if item.Title == "msg ws-b" {
			foundWSB = true
		}
	}
	if !foundWSA {
		t.Fatal("ws-a notification should appear in ws-a handler")
	}
	if foundWSB {
		t.Fatal("ws-b notification should NOT appear in ws-a handler")
	}

	hWSB := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-b",
		UserID:      "user-a",
	}
	itemsWSB := hWSB.caixinhaPersistedNotifications()
	foundWSAinB := false
	foundWSBinB := false
	for _, item := range itemsWSB {
		if item.Title == "msg ws-a" {
			foundWSAinB = true
		}
		if item.Title == "msg ws-b" {
			foundWSBinB = true
		}
	}
	if foundWSAinB {
		t.Fatal("ws-a notification should NOT appear in ws-b handler")
	}
	if !foundWSBinB {
		t.Fatal("ws-b notification should appear in ws-b handler")
	}
}

func TestNotificationsWithNullWorkspaceNotShown(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedCaixinhaNotificacoesScenario(t, db)

	_, err := db.Exec(`INSERT INTO user_notifications (id, user_id, title, message, type, is_read, created_at) VALUES (?, ?, ?, ?, ?, 0, ?)`,
		"notif-null-ws", "user-a", "null ws notif", "desc null ws", "caixinha.aporte", time.Now().Unix())
	if err != nil {
		t.Fatalf("insert null-ws notification: %v", err)
	}

	h := NotificacoesHandler{
		DB:          db,
		Templates:   testNotificacoesTemplate(t),
		WorkspaceID: "ws-a",
		UserID:      "user-a",
	}

	items := h.caixinhaPersistedNotifications()
	for _, item := range items {
		if item.Title == "null ws notif" {
			t.Fatal("notification with NULL workspace_id should not appear")
		}
	}
}

func seedCaixinhaNotificacoesScenario(t *testing.T, db *sql.DB) {
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
			('a-checking', 'ws-a', 'Conta A', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES
			('a-cat-overdraft', 'ws-a', 'Cat Overdraft', 'EXPENSE', ?),
			('a-cat-done', 'ws-a', 'Cat Done', 'EXPENSE', ?),
			('a-cat-normal', 'ws-a', 'Cat Normal', 'EXPENSE', ?)
	`, now, now, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('a-box-overdraft', 'ws-a', 'Overdraft Box', 'a-cat-overdraft', 50000, 0, ?, ?),
			('a-box-done', 'ws-a', 'Done Box', 'a-cat-done', 30000, 0, ?, ?),
			('a-box-normal', 'ws-a', 'Normal Box', 'a-cat-normal', 100000, 0, ?, ?)
	`, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('a-ledger-overdraft', 'a-box-overdraft', 2000, 'RECHARGE', 'Aporte', ?, ?),
			('a-ledger-overdraft-consume', 'a-box-overdraft', -8000, 'CONSUME', 'Consumo via teste', ?, ?),
			('a-ledger-done', 'a-box-done', 50000, 'RECHARGE', 'Aporte', ?, ?),
			('a-ledger-done-consume', 'a-box-done', -5000, 'CONSUME', 'Consumo via teste', ?, ?),
			('a-ledger-normal', 'a-box-normal', 10000, 'RECHARGE', 'Aporte', ?, ?),
			('a-ledger-normal-consume', 'a-box-normal', -5000, 'CONSUME', 'Consumo via teste', ?, ?)
	`, now, now, now, now, now, now, now, now, now, now, now, now)

}

func testNotificacoesTemplate(t *testing.T) TemplateEngine {
	t.Helper()
	return template.Must(template.New("notificacoes").Parse(`
{{define "notificacoes-page"}}<div id="notificacoes-content">{{range .Items}}{{.Title}}{{end}}</div>{{end}}
`))
}
