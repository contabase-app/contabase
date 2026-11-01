package services

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func newReversalDBAt(t *testing.T, path string) *database.RuntimeDBs {
	t.Helper()
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	seedInvestmentServiceFixtures(t, pools.DB)
	return pools
}

// postOrdinaryTransfer inserts an ordinary paid transfer and applies its
// balance effects through the same service used by every transaction flow.
func postOrdinaryTransfer(t *testing.T, pools *database.RuntimeDBs, id, from, to string, amount int64) {
	t.Helper()
	now := time.Now().Unix()
	noon := civildateNoon("2026-02-02")
	tx, err := pools.Immediate.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
VALUES(?,'ws-a','user-a',?,?,'TRANSFER',?,?,'2026-02-02',?,'2026-02-02','paid','Transferência operacional',?,?)`,
		id, from, to, amount, noon, noon, now, now); err != nil {
		t.Fatal(err)
	}
	if err := ApplyBalanceEffect(tx, "ws-a", models.TransactionTypeTransfer, models.AccountTypeChecking, models.PaymentStatusPaid, amount, from, to, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func mustCreateReversedFundedLot(t *testing.T, pools *database.RuntimeDBs, principal int64) (string, string) {
	t.Helper()
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, principal)
	previewIn := reversalPreviewInput("ws-a", investment, target, "erro de digitação")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil || !preview.Eligible {
		t.Fatalf("preview: eligible=%v err=%v blockers=%v", preview.Eligible, err, preview.Blockers)
	}
	if _, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirmInput(previewIn, preview)); err != nil {
		t.Fatal(err)
	}
	return investment, target
}

// P1: after an individual reversal the involved accounts must keep accepting
// ordinary movements, renames and new financed applications. The historical
// receipt is revalidated again on reopen even though every balance moved.
func TestInvestmentReversalKeepsFundedLotOperable(t *testing.T) {
	path := t.TempDir() + "/reversal-operable.db"
	pools := newReversalDBAt(t, path)
	ctx := context.Background()
	exec(t, pools.DB, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-savings-a','ws-a','Poupança A','SAVINGS',0,0)`)
	investment, target := mustCreateReversedFundedLot(t, pools, 100000)

	for _, account := range []string{"acc-inv-a", "acc-checking-a"} {
		exec(t, pools.DB, `UPDATE accounts SET name=name||' renomeada' WHERE workspace_id='ws-a' AND id=?`, account)
	}
	postOrdinaryTransfer(t, pools, "operability-checking", "acc-checking-a", "acc-savings-a", 7000)
	postOrdinaryTransfer(t, pools, "operability-investment", "acc-inv-a", "acc-savings-a", 1000)

	again := investmentPurchaseInput(investment)
	again.PrincipalCents = 25000
	again.SourceAccountID = "acc-checking-a"
	again.RequestID = uuid.NewString()
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, again); err != nil {
		t.Fatalf("later financed purchase refused: %v", err)
	}
	exec(t, pools.DB, `UPDATE investments SET name='Lote renomeado' WHERE workspace_id='ws-a' AND id=?`, investment)
	exec(t, pools.DB, `UPDATE accounts SET archived_at=unixepoch() WHERE workspace_id='ws-a' AND id='acc-inv-a'`)

	before, err := GetInvestmentPurchaseReversals(ctx, pools.DB, "ws-a", investment)
	if err != nil {
		t.Fatal(err)
	}
	receiptID := before[target].ID
	if receiptID == "" {
		t.Fatal("receipt missing before reopen")
	}
	pools.Close()

	reopened, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatalf("reopen with moved balances: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	after, err := GetInvestmentPurchaseReversals(ctx, reopened.DB, "ws-a", investment)
	if err != nil || after[target].ID != receiptID {
		t.Fatalf("reopened receipt=%+v err=%v", after[target], err)
	}
	var archived sql.NullInt64
	if err := reopened.DB.QueryRow(`SELECT archived_at FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&archived); err != nil || !archived.Valid {
		t.Fatalf("archived account lost: %v err=%v", archived, err)
	}
	exec(t, reopened.DB, `UPDATE accounts SET name=name||' pós-reabertura' WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	postOrdinaryTransfer(t, reopened, "operability-reopened", "acc-checking-a", "acc-savings-a", 1500)

	mustAbort := func(statement string, args ...any) {
		t.Helper()
		if _, err := reopened.Immediate.Exec(statement, args...); err == nil {
			t.Fatalf("mutation succeeded: %s", statement)
		}
	}
	mustAbort(`UPDATE accounts SET type='SAVINGS' WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	mustAbort(`UPDATE investments SET account_id='acc-inv-b' WHERE workspace_id='ws-a' AND id=?`, investment)
	mustAbort(`UPDATE investments SET principal_cents=1 WHERE workspace_id='ws-a' AND id=?`, investment)
	mustAbort(`UPDATE investment_purchase_reversals SET reason='reescrito' WHERE workspace_id='ws-a' AND id=?`, receiptID)
	mustAbort(`DELETE FROM investment_purchase_reversals WHERE workspace_id='ws-a' AND id=?`, receiptID)
	mustAbort(`UPDATE investment_purchases SET principal_cents=1 WHERE workspace_id='ws-a' AND id=?`, target)
	mustAbort(`UPDATE transactions SET description='x' WHERE workspace_id='ws-a' AND id=?`, InvestmentPurchaseTransferPrefix+target)
	mustAbort(`UPDATE transactions SET description='x' WHERE workspace_id='ws-a' AND id=?`, InvestmentReversalTransferPrefix+receiptID)
}

// P1 sibling: a record-only lot stays redeemable and archivable after an
// individual reversal, and the historical receipt survives the valuation
// written by the redemption plus the later lifecycle state.
func TestInvestmentReversalKeepsRecordOnlyLotArchivable(t *testing.T) {
	path := t.TempDir() + "/reversal-lifecycle.db"
	pools := newReversalDBAt(t, path)
	ctx := context.Background()
	input := investmentCreateInput("ws-a", "acc-inv-a")
	input.RequestID = uuid.NewString()
	investment := mustCreateInvestment(t, pools.Immediate, input)
	purchase := investmentPurchaseInput(investment.ID)
	purchase.PrincipalCents = 40000
	purchase.RequestID = uuid.NewString()
	created, err := CreateInvestmentPurchase(ctx, pools.Immediate, purchase)
	if err != nil {
		t.Fatal(err)
	}
	previewIn := reversalPreviewInput("ws-a", investment.ID, created.ID, "registro em duplicidade")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil || !preview.Eligible || preview.Movement != "RECORD" {
		t.Fatalf("preview: eligible=%v movement=%s err=%v blockers=%v", preview.Eligible, preview.Movement, err, preview.Blockers)
	}
	if _, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirmInput(previewIn, preview)); err != nil {
		t.Fatal(err)
	}
	if err := RedeemInvestment(ctx, pools.Immediate, RedeemInvestmentInput{
		WorkspaceID: "ws-a", InvestmentID: investment.ID, ActingUserID: "user-a",
		RedeemedDate: "2026-04-01", FinalAmountCents: 120000,
	}); err != nil {
		t.Fatalf("redeem after reversal: %v", err)
	}
	if err := ArchiveInvestment(ctx, pools.Immediate, ArchiveInvestmentInput{
		WorkspaceID: "ws-a", InvestmentID: investment.ID, ActingUserID: "user-a",
	}); err != nil {
		t.Fatalf("archive after reversal: %v", err)
	}
	pools.Close()

	reopened, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatalf("reopen redeemed/archived lot: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	var status string
	var archived sql.NullInt64
	if err := reopened.DB.QueryRow(`SELECT status, archived_at FROM investments WHERE workspace_id='ws-a' AND id=?`, investment.ID).Scan(&status, &archived); err != nil || status != "RESGATADA" || !archived.Valid {
		t.Fatalf("lifecycle state=%s archived=%v err=%v", status, archived, err)
	}
	receipts, err := GetInvestmentPurchaseReversals(ctx, reopened.DB, "ws-a", investment.ID)
	if err != nil || receipts[created.ID].ID == "" {
		t.Fatalf("receipt after lifecycle: %+v err=%v", receipts[created.ID], err)
	}
}

const reversal80AccountsUpdateTrigger = `CREATE TRIGGER trg_reversal80_accounts_update BEFORE UPDATE ON accounts WHEN EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=OLD.workspace_id AND OLD.id IN(r.investment_account_id,r.origin_account_id)) BEGIN SELECT RAISE(ABORT,'investment purchase reversal integrity'); END`
const reversal80InvestmentsUpdateTrigger = `CREATE TRIGGER trg_reversal80_investments_update BEFORE UPDATE ON investments WHEN EXISTS(SELECT 1 FROM investment_purchase_reversals r WHERE r.workspace_id=OLD.workspace_id AND r.investment_id=OLD.id) BEGIN SELECT RAISE(ABORT,'investment purchase reversal integrity'); END`

// Upgrade 81 -> 82 with a real pre-existing receipt whose account balances
// already moved: the historical preflight must accept the 81 database, the
// migration must replace only the two UPDATE triggers, and the receipt must
// remain readable and immutable.
func TestInvestmentReversalMigration82UpgradeWithExistingReceipt(t *testing.T) {
	path := t.TempDir() + "/reversal-upgrade.db"
	pools := newReversalDBAt(t, path)
	exec(t, pools.DB, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-savings-a','ws-a','Poupança A','SAVINGS',0,0)`)
	investment, target := mustCreateReversedFundedLot(t, pools, 100000)
	postOrdinaryTransfer(t, pools, "upgrade-movement", "acc-checking-a", "acc-savings-a", 3000)
	ctx := context.Background()
	receipts, err := GetInvestmentPurchaseReversals(ctx, pools.DB, "ws-a", investment)
	if err != nil || receipts[target].ID == "" {
		t.Fatalf("receipt before upgrade: %+v err=%v", receipts[target], err)
	}
	receiptID := receipts[target].ID
	pools.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM schema_migrations WHERE version=82`,
		`DROP TRIGGER trg_reversal82_accounts_update`,
		`DROP TRIGGER trg_reversal82_investments_update`,
		reversal80AccountsUpdateTrigger,
		reversal80InvestmentsUpdateTrigger,
	} {
		if _, err := raw.Exec(statement); err != nil {
			raw.Close()
			t.Fatalf("prepare schema 81: %v", err)
		}
	}
	raw.Close()

	upgraded, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatalf("upgrade 81->82: %v", err)
	}
	t.Cleanup(func() { upgraded.Close() })
	var version int
	if err := upgraded.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 82 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	var old int
	if err := upgraded.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name IN('trg_reversal80_accounts_update','trg_reversal80_investments_update')`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("legacy triggers=%d err=%v", old, err)
	}
	after, err := GetInvestmentPurchaseReversals(ctx, upgraded.DB, "ws-a", investment)
	if err != nil || after[target].ID != receiptID {
		t.Fatalf("receipt after upgrade: %+v err=%v", after[target], err)
	}
	exec(t, upgraded.DB, `UPDATE accounts SET name=name||' pós-upgrade' WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	postOrdinaryTransfer(t, upgraded, "upgrade-after", "acc-checking-a", "acc-savings-a", 1200)
	if _, err := upgraded.Immediate.Exec(`UPDATE accounts SET type='SAVINGS' WHERE workspace_id='ws-a' AND id='acc-checking-a'`); err == nil {
		t.Fatal("identity update succeeded after upgrade")
	}
}
