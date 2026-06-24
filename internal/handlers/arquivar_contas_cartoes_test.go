package handlers

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/repository"
)

func TestArchiveUnarchiveConta(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	repo := repository.NewConfigRepository(db)
	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN"}

	contas, err := handler.queryContas(repo)
	if err != nil {
		t.Fatalf("queryContas: %v", err)
	}
	if len(contas) != 1 {
		t.Fatalf("expected 1 active conta, got %d", len(contas))
	}
	if contas[0].ID != "conta-1" {
		t.Fatalf("expected conta-1, got %s", contas[0].ID)
	}

	archived, err := repo.ArchivedAccountsByWorkspace("ws-1")
	if err != nil {
		t.Fatalf("ArchivedAccountsByWorkspace: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("expected 0 archived contas, got %d", len(archived))
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := repo.ArchiveAccountTx(tx, "ws-1", "conta-1"); err != nil {
		tx.Rollback()
		t.Fatalf("ArchiveAccountTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	var archivedAt sql.NullInt64
	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'conta-1'`).Scan(&archivedAt); err != nil {
		t.Fatalf("query archived_at: %v", err)
	}
	if !archivedAt.Valid {
		t.Fatal("archived_at should be set after archiving")
	}

	contas, err = handler.queryContas(repo)
	if err != nil {
		t.Fatalf("queryContas after archive: %v", err)
	}
	if len(contas) != 0 {
		t.Fatalf("expected 0 active contas after archive, got %d", len(contas))
	}

	archived, err = repo.ArchivedAccountsByWorkspace("ws-1")
	if err != nil {
		t.Fatalf("ArchivedAccountsByWorkspace after archive: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived conta, got %d", len(archived))
	}
	if archived[0].ID != "conta-1" {
		t.Fatalf("expected conta-1 archived, got %s", archived[0].ID)
	}

	tx2, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	if err := repo.UnarchiveAccountTx(tx2, "ws-1", "conta-1"); err != nil {
		tx2.Rollback()
		t.Fatalf("UnarchiveAccountTx: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}

	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'conta-1'`).Scan(&archivedAt); err != nil {
		t.Fatalf("query archived_at after unarchive: %v", err)
	}
	if archivedAt.Valid {
		t.Fatal("archived_at should be NULL after unarchiving")
	}

	contas, err = handler.queryContas(repo)
	if err != nil {
		t.Fatalf("queryContas after unarchive: %v", err)
	}
	if len(contas) != 1 {
		t.Fatalf("expected 1 active conta after unarchive, got %d", len(contas))
	}
}

func TestArchiveUnarchiveCartao(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	repo := repository.NewConfigRepository(db)
	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN"}

	cartoes, err := handler.queryCartoes(repo)
	if err != nil {
		t.Fatalf("queryCartoes: %v", err)
	}
	if len(cartoes) != 1 {
		t.Fatalf("expected 1 active cartao, got %d", len(cartoes))
	}
	if cartoes[0].AccountID != "card-1" {
		t.Fatalf("expected card-1, got %s", cartoes[0].AccountID)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := repo.ArchiveAccountTx(tx, "ws-1", "card-1"); err != nil {
		tx.Rollback()
		t.Fatalf("ArchiveAccountTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	var archivedAt sql.NullInt64
	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'card-1'`).Scan(&archivedAt); err != nil {
		t.Fatalf("query archived_at: %v", err)
	}
	if !archivedAt.Valid {
		t.Fatal("archived_at should be set after archiving card")
	}

	cartoes, err = handler.queryCartoes(repo)
	if err != nil {
		t.Fatalf("queryCartoes after archive: %v", err)
	}
	if len(cartoes) != 0 {
		t.Fatalf("expected 0 active cartoes after archive, got %d", len(cartoes))
	}

	archivedCards, err := repo.ArchivedCardsByWorkspace("ws-1")
	if err != nil {
		t.Fatalf("ArchivedCardsByWorkspace: %v", err)
	}
	if len(archivedCards) != 1 {
		t.Fatalf("expected 1 archived card, got %d", len(archivedCards))
	}

	tx2, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	if err := repo.UnarchiveAccountTx(tx2, "ws-1", "card-1"); err != nil {
		tx2.Rollback()
		t.Fatalf("UnarchiveAccountTx: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit tx2: %v", err)
	}

	cartoes, err = handler.queryCartoes(repo)
	if err != nil {
		t.Fatalf("queryCartoes after unarchive: %v", err)
	}
	if len(cartoes) != 1 {
		t.Fatalf("expected 1 active cartao after unarchive, got %d", len(cartoes))
	}
}

func TestWorkpaceValidationPreventsArchivingWrongWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'U', 'u@e.com', 'h', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, created_at, updated_at) VALUES ('ws-1', 'W1', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, created_at, updated_at) VALUES ('ws-2', 'W2', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-1', 'user-1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES ('ws-2', 'user-1', 'ADMIN', ?)`, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, created_at, updated_at) VALUES ('acc-ws1', 'ws-1', 'A1', 'CHECKING', ?, ?)`, now, now)

	repo := repository.NewConfigRepository(db)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	err = repo.ArchiveAccountTx(tx, "ws-2", "acc-ws1")
	tx.Rollback()
	if err == nil {
		t.Fatal("expected error archiving account from wrong workspace")
	}
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}

	var archivedAt sql.NullInt64
	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'acc-ws1'`).Scan(&archivedAt); err != nil {
		t.Fatalf("query archived_at: %v", err)
	}
	if archivedAt.Valid {
		t.Fatal("account should not be archived from wrong workspace")
	}
}

func TestArchivedAccountDoesNotAppearInFormAccounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	repo := repository.NewConfigRepository(db)
	tx, _ := db.Begin()
	repo.ArchiveAccountTx(tx, "ws-1", "conta-1")
	tx.Commit()

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1"}
	accounts, err := handler.queryFormAccounts()
	if err != nil {
		t.Fatalf("queryFormAccounts: %v", err)
	}
	for _, acc := range accounts {
		if acc.ID == "conta-1" {
			t.Fatal("archived account should not appear in queryFormAccounts")
		}
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 form account (card only), got %d", len(accounts))
	}
}

func TestArchivedAccountsStillAppearInFilterAccounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	repo := repository.NewConfigRepository(db)
	tx, _ := db.Begin()
	repo.ArchiveAccountTx(tx, "ws-1", "conta-1")
	tx.Commit()

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1"}
	accounts, err := handler.queryFilterAccounts()
	if err != nil {
		t.Fatalf("queryFilterAccounts: %v", err)
	}

	found := false
	for _, acc := range accounts {
		if acc.ID == "conta-1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("archived account should still appear in FilterAccounts for historical queries")
	}
}

func TestNewTransactionRejectsArchivedAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	repo := repository.NewConfigRepository(db)
	tx, _ := db.Begin()
	repo.ArchiveAccountTx(tx, "ws-1", "conta-1")
	tx.Commit()

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1"}

	_, err := handler.insertTransaction(
		"EXPENSE", 1000, "Test", "", "", time.Now().Unix(),
		"conta-1", "", "cat-1",
		1, "paid", false, "", "", 0, false, nil, "", false,
	)
	if err == nil {
		t.Fatal("expected error when transacting with archived account")
	}
}

func TestArchivedCardDoesNotAppearInDashboardCards(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	repo := repository.NewConfigRepository(db)
	tx, _ := db.Begin()
	repo.ArchiveAccountTx(tx, "ws-1", "card-1")
	tx.Commit()

	dashboard := BuildDashboardData(db, "user-1", "ws-1")
	for _, card := range dashboard.Cards {
		if card.ID == "card-1" {
			t.Fatal("archived card should not appear in dashboard cards")
		}
	}
}

func TestSeedWorkspaceAccountsDoesNotCreateFinancialAccounts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	now := time.Now().Unix()
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-1', 'U', 'u@e.com', 'h', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, type, created_at, updated_at)
		VALUES ('ws-1', 'W', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-1', 'user-1', 'ADMIN', ?)
	`, now)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := database.SeedWorkspaceAccountsTx(tx, "ws-1", "personal"); err != nil {
		t.Fatalf("SeedWorkspaceAccountsTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit tx: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = 'ws-1' AND archived_at IS NULL`).Scan(&count); err != nil {
		t.Fatalf("count active accounts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no seeded active accounts or cards, got %d", count)
	}

	if err := db.QueryRow(`SELECT COUNT(1) FROM accounts WHERE workspace_id = 'ws-1' AND archived_at IS NOT NULL`).Scan(&count); err != nil {
		t.Fatalf("count archived accounts: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 archived seeded accounts, got %d", count)
	}
}

func TestArchiveUnarchiveRoundtripEndpoint(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedArchiveScenario(t, db, "ws-1")

	tmpl := template.Must(template.New("test").Parse(`
{{define "configuracoes-contas-content"}}ok{{end}}
{{define "configuracoes-contas-page"}}ok{{end}}
`))
	handler := ConfiguracoesHandler{DB: db, WorkspaceID: "ws-1", UserID: "user-1", ActorRole: "ADMIN", Templates: tmpl}

	r := httptest.NewRequest(http.MethodPost, "/configuracoes/contas/conta-1/arquivar", nil)
	w := httptest.NewRecorder()
	handler.HandleContaArchive(w, r, "conta-1")
	if w.Code != http.StatusOK {
		t.Fatalf("archive endpoint returned %d, want 200. body=%q", w.Code, w.Body.String())
	}

	var archivedAt sql.NullInt64
	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'conta-1'`).Scan(&archivedAt); err != nil {
		t.Fatalf("query archived_at: %v", err)
	}
	if !archivedAt.Valid {
		t.Fatal("archived_at should be set")
	}

	r2 := httptest.NewRequest(http.MethodPost, "/configuracoes/contas/conta-1/reativar", nil)
	w2 := httptest.NewRecorder()
	handler.HandleContaUnarchive(w2, r2, "conta-1")
	if w2.Code != http.StatusOK {
		t.Fatalf("unarchive endpoint returned %d, want 200. body=%q", w2.Code, w2.Body.String())
	}

	if err := db.QueryRow(`SELECT archived_at FROM accounts WHERE id = 'conta-1'`).Scan(&archivedAt); err != nil {
		t.Fatalf("query archived_at after unarchive: %v", err)
	}
	if archivedAt.Valid {
		t.Fatal("archived_at should be NULL after reativar")
	}
}

func seedArchiveScenario(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	now := time.Now().Unix()
	execTestSQL(t, db, `INSERT INTO users (id, name, email, password_hash, created_at, updated_at) VALUES ('user-1', 'User', 'u@e.com', 'h', ?, ?)`, now, now)
	execTestSQL(t, db, `INSERT INTO workspaces (id, name, type, created_at, updated_at) VALUES (?, 'Workspace', 'personal', ?, ?)`, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO workspace_members (workspace_id, user_id, role, joined_at) VALUES (?, 'user-1', 'ADMIN', ?)`, workspaceID, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, created_at, updated_at) VALUES ('conta-1', ?, 'Conta Teste', 'CHECKING', '#EC7000', '', 'custom', ?, ?)`, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, created_at, updated_at) VALUES ('card-1', ?, 'Cartao Teste', 'CREDIT_CARD', '#820AD1', '', 'custom', ?, ?)`, workspaceID, now, now)
	execTestSQL(t, db, `INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit) VALUES ('cc-1', 'card-1', 25, 5, 500000)`)
	execTestSQL(t, db, `INSERT INTO categories (id, workspace_id, name, type, macro_group, created_at) VALUES ('cat-1', ?, 'Categoria', 'EXPENSE', 'Estilo de Vida', ?)`, workspaceID, now)
}
