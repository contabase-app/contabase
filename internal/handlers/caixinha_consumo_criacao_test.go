package handlers

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

func TestCreateExpenseConsumesReserveWhenCategoryLinked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		1200,
		"Despesa Direta",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-direct",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction direct expense: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Despesa Direta")
	assertConsumeLedgerEvent(t, db, txID, "box-direct", -1200)
}

func TestCreateExpenseConsumesReserveWhenSubcategoryLinked(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		1500,
		"Despesa Subcategoria",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-sub",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction subcategory expense: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Despesa Subcategoria")
	assertConsumeLedgerEvent(t, db, txID, "box-parent", -1500)
}

func TestCreateExpenseWithoutLinkedBoxDoesNotConsumeReserve(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		900,
		"Despesa Sem Caixinha",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-unlinked",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction unlinked expense: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Despesa Sem Caixinha")
	assertNoConsumeLedgerEvent(t, db, txID)
}

func TestQueryFormCategoriesIncludesBoxReserveMetadata(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	categories, err := handler.queryFormCategories()
	if err != nil {
		t.Fatalf("queryFormCategories: %v", err)
	}

	direct := mustFindFormCategoryForBoxTest(t, categories, "cat-expense-direct")
	if direct.BoxID != "box-direct" {
		t.Fatalf("direct category BoxID = %q, want box-direct", direct.BoxID)
	}
	if direct.BoxReservedBalance != 40000 {
		t.Fatalf("direct category reserve = %d, want 40000", direct.BoxReservedBalance)
	}

	subcategory := mustFindFormCategoryForBoxTest(t, categories, "cat-expense-sub")
	if subcategory.BoxID != "box-parent" {
		t.Fatalf("subcategory BoxID = %q, want box-parent", subcategory.BoxID)
	}
	if subcategory.BoxReservedBalance != 50000 {
		t.Fatalf("subcategory reserve = %d, want 50000", subcategory.BoxReservedBalance)
	}

	unlinked := mustFindFormCategoryForBoxTest(t, categories, "cat-expense-unlinked")
	if unlinked.BoxID != "" {
		t.Fatalf("unlinked category BoxID = %q, want empty", unlinked.BoxID)
	}
	if unlinked.BoxReservedBalance != 0 {
		t.Fatalf("unlinked category reserve = %d, want 0", unlinked.BoxReservedBalance)
	}

	for _, category := range categories {
		if strings.HasPrefix(category.ID, "b-") || category.BoxID == "b-box-1" {
			t.Fatalf("foreign workspace category metadata leaked: %#v", category)
		}
	}
}

func TestCreateIncomeAndTransferDoNotConsumeReserve(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"INCOME",
		2500,
		"Receita Sem Consumo",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-income",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction income: %v", err)
	}
	incomeID := findTransactionByDescription(t, db, "Receita Sem Consumo")
	assertNoConsumeLedgerEvent(t, db, incomeID)

	_, err = handler.insertTransaction(
		"TRANSFER",
		1800,
		"Transferencia Sem Consumo",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"checking-extra",
		"",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction transfer: %v", err)
	}
	transferID := findTransactionByDescription(t, db, "Transferencia Sem Consumo")
	assertNoConsumeLedgerEvent(t, db, transferID)
}

func TestCreateCardExpenseConsumesReserveOnPurchase(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	before, err := services.CalculateWorkspaceReserveBalance(db, "ws-test")
	if err != nil {
		t.Fatalf("calculate reserve before: %v", err)
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err = handler.insertTransaction(
		"EXPENSE",
		2000,
		"Compra Cartao Com Consumo",
		"",
		"",
		time.Now().UTC().Unix(),
		"card-test",
		"",
		"cat-expense-direct",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction card expense: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Compra Cartao Com Consumo")
	assertConsumeLedgerEvent(t, db, txID, "box-direct", -2000)

	after, err := services.CalculateWorkspaceReserveBalance(db, "ws-test")
	if err != nil {
		t.Fatalf("calculate reserve after: %v", err)
	}
	if after.RealBalance != before.RealBalance {
		t.Fatalf("real balance changed on card expense: got=%d want=%d", after.RealBalance, before.RealBalance)
	}
	if after.ReservedBalance != before.ReservedBalance-2000 {
		t.Fatalf("reserved balance after card consume = %d, want %d", after.ReservedBalance, before.ReservedBalance-2000)
	}
	if after.FreeBalance != before.FreeBalance+2000 {
		t.Fatalf("free balance after card consume = %d, want %d", after.FreeBalance, before.FreeBalance+2000)
	}
}

func TestCreateExpenseBlocksWhenReserveInsufficient(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		600,
		"Despesa Sem Reserva Suficiente",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-low",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if !errors.Is(err, errBoxReserveInsufficient) {
		t.Fatalf("expected errBoxReserveInsufficient, got %v", err)
	}

	var txCount int
	if err := db.QueryRow(`SELECT COUNT(1) FROM transactions WHERE workspace_id = 'ws-test' AND description = 'Despesa Sem Reserva Suficiente'`).Scan(&txCount); err != nil {
		t.Fatalf("count blocked transactions: %v", err)
	}
	if txCount != 0 {
		t.Fatalf("blocked transaction inserted: count=%d", txCount)
	}
}

func TestCreateExpenseAllowsOverdraftWhenExplicitlyConfirmed(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	before, err := services.CalculateWorkspaceReserveBalance(db, "ws-test")
	if err != nil {
		t.Fatalf("calculate reserve before: %v", err)
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err = handler.insertTransaction(
		"EXPENSE",
		600,
		"Despesa Excedente Confirmada",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-low",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		true,
	)
	if err != nil {
		t.Fatalf("insertTransaction overdraft confirmed: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Despesa Excedente Confirmada")
	assertConsumeLedgerEvent(t, db, txID, "box-low", -600)

	var lowReserved int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'box-low'`).Scan(&lowReserved); err != nil {
		t.Fatalf("query box-low reserved: %v", err)
	}
	if lowReserved >= 0 {
		t.Fatalf("box-low reserved should be negative after overdraft, got %d", lowReserved)
	}

	after, err := services.CalculateWorkspaceReserveBalance(db, "ws-test")
	if err != nil {
		t.Fatalf("calculate reserve after: %v", err)
	}
	if after.ReservedBalance != before.ReservedBalance-600 {
		t.Fatalf("reserved balance after overdraft = %d, want %d", after.ReservedBalance, before.ReservedBalance-600)
	}
	if after.FreeBalance != before.FreeBalance {
		t.Fatalf("free balance after overdraft = %d, want %d", after.FreeBalance, before.FreeBalance)
	}
	if after.FreeBalance != after.RealBalance-after.ReservedBalance {
		t.Fatalf("free balance formula mismatch: got=%d real=%d reserved=%d", after.FreeBalance, after.RealBalance, after.ReservedBalance)
	}
}

func TestCreateExpenseDoesNotConsumeForeignWorkspaceBox(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	var beforeForeignReserved int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'b-box-1'`).Scan(&beforeForeignReserved); err != nil {
		t.Fatalf("foreign reserve before: %v", err)
	}

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		1100,
		"Despesa Isolamento Workspace",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-direct",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction isolation expense: %v", err)
	}

	var afterForeignReserved int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM box_virtual_ledger WHERE box_id = 'b-box-1'`).Scan(&afterForeignReserved); err != nil {
		t.Fatalf("foreign reserve after: %v", err)
	}
	if afterForeignReserved != beforeForeignReserved {
		t.Fatalf("foreign workspace box changed: got=%d want=%d", afterForeignReserved, beforeForeignReserved)
	}
}

func TestInsertConsumeLedgerEventTxDoesNotDuplicateBySourceTransaction(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedBoxConsumeCreationScenario(t, db)

	handler := TransactionHandler{DB: db, WorkspaceID: "ws-test", UserID: "user-test"}
	_, err := handler.insertTransaction(
		"EXPENSE",
		1300,
		"Despesa Anti Duplicacao",
		"",
		"",
		time.Now().UTC().Unix(),
		"checking-test",
		"",
		"cat-expense-direct",
		1,
		"paid",
		false,
		"",
		"",
		0,
		false,
		nil,
		"",
		false,
	)
	if err != nil {
		t.Fatalf("insertTransaction anti-dup expense: %v", err)
	}

	txID := findTransactionByDescription(t, db, "Despesa Anti Duplicacao")
	now := time.Now().UTC().Unix()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx for anti-dup: %v", err)
	}
	if err := insertConsumeLedgerEventTx(tx, "box-direct", txID, 1300, now, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("reinsert consume event should be no-op: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit anti-dup tx: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE source_transaction_id = ? AND type = 'CONSUME'`, txID).Scan(&count); err != nil {
		t.Fatalf("count consume events by source_transaction_id: %v", err)
	}
	if count != 1 {
		t.Fatalf("consume duplication count = %d, want 1", count)
	}
}

func assertConsumeLedgerEvent(t *testing.T, db *sql.DB, sourceTxID, boxID string, amount int64) {
	t.Helper()
	var gotBoxID, gotType string
	var gotAmount int64
	if err := db.QueryRow(`
		SELECT box_id, amount, type
		FROM box_virtual_ledger
		WHERE source_transaction_id = ?
		  AND type = 'CONSUME'
	`, sourceTxID).Scan(&gotBoxID, &gotAmount, &gotType); err != nil {
		t.Fatalf("query consume ledger event: %v", err)
	}
	if gotBoxID != boxID {
		t.Fatalf("consume box_id = %q, want %q", gotBoxID, boxID)
	}
	if gotAmount != amount {
		t.Fatalf("consume amount = %d, want %d", gotAmount, amount)
	}
	if gotType != "CONSUME" {
		t.Fatalf("consume type = %q, want CONSUME", gotType)
	}
}

func assertNoConsumeLedgerEvent(t *testing.T, db *sql.DB, sourceTxID string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM box_virtual_ledger WHERE source_transaction_id = ? AND type = 'CONSUME'`, sourceTxID).Scan(&count); err != nil {
		t.Fatalf("count consume ledger events: %v", err)
	}
	if count != 0 {
		t.Fatalf("consume events count = %d, want 0", count)
	}
}

func mustFindFormCategoryForBoxTest(t *testing.T, categories []FormCategory, id string) FormCategory {
	t.Helper()
	for _, category := range categories {
		if category.ID == id {
			return category
		}
	}
	t.Fatalf("category %q not found in form categories", id)
	return FormCategory{}
}

func seedBoxConsumeCreationScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	seedInvoicePaymentScenario(t, db)
	now := time.Now().UTC().Unix()

	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-extra', 'ws-test', 'Conta Destino', 'CHECKING', 50000, 50000, ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, parent_id, created_at)
		VALUES
			('cat-expense-parent', 'ws-test', 'Moradia', 'EXPENSE', NULL, ?),
			('cat-expense-sub', 'ws-test', 'Aluguel', 'EXPENSE', 'cat-expense-parent', ?),
			('cat-expense-direct', 'ws-test', 'Mercado', 'EXPENSE', NULL, ?),
			('cat-expense-unlinked', 'ws-test', 'Lazer', 'EXPENSE', NULL, ?),
			('cat-expense-low', 'ws-test', 'Saude', 'EXPENSE', NULL, ?),
			('cat-income', 'ws-test', 'Salario', 'INCOME', NULL, ?)
	`, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES
			('box-parent', 'ws-test', 'Caixinha Moradia', 'cat-expense-parent', 0, 0, ?, ?),
			('box-direct', 'ws-test', 'Caixinha Mercado', 'cat-expense-direct', 0, 0, ?, ?),
			('box-low', 'ws-test', 'Caixinha Saude', 'cat-expense-low', 0, 0, ?, ?)
	`, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES
			('box-parent-recharge', 'box-parent', 50000, 'RECHARGE', 'Aporte inicial', ?, ?),
			('box-direct-recharge', 'box-direct', 40000, 'RECHARGE', 'Aporte inicial', ?, ?),
			('box-low-recharge', 'box-low', 500, 'RECHARGE', 'Aporte inicial', ?, ?)
	`, now, now, now, now, now, now)

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-b', 'User B', 'user-b@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-b', 'Workspace B', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-b', 'user-b', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, type, created_at)
		VALUES ('b-cat-expense', 'ws-b', 'Despesa B', 'EXPENSE', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO boxes (id, workspace_id, name, category_id, target_amount, monthly_recharge, created_at, updated_at)
		VALUES ('b-box-1', 'ws-b', 'Caixinha B', 'b-cat-expense', 0, 0, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO box_virtual_ledger (id, box_id, amount, type, description, reference_date, created_at)
		VALUES ('b-box-recharge', 'b-box-1', 9999, 'RECHARGE', 'Aporte B', ?, ?)
	`, now, now)
}
