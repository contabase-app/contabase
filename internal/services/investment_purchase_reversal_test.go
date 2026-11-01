package services

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/google/uuid"
)

func newReversalDB(t *testing.T) *database.RuntimeDBs {
	t.Helper()
	pools, err := database.OpenRuntimeDBs(t.TempDir() + "/reversal.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	seedInvestmentServiceFixtures(t, pools.DB)
	return pools
}

// reversalFundedLot creates a funded investment (initial transfer) plus one
// financed additional purchase, both from acc-checking-a to acc-inv-a.
func reversalFundedLot(t *testing.T, pools *database.RuntimeDBs, principal int64) (investmentID, additional string) {
	t.Helper()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=? WHERE workspace_id='ws-a' AND id='acc-checking-a'`, principal*10)
	input := investmentCreateInput("ws-a", "acc-inv-a")
	input.RequestID = uuid.NewString()
	input.SourceAccountID = "acc-checking-a"
	input.PrincipalCents = principal
	inv := mustCreateInvestment(t, pools.Immediate, input)
	purchase := investmentPurchaseInput(inv.ID)
	purchase.PrincipalCents = principal / 2
	purchase.SourceAccountID = "acc-checking-a"
	created, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, purchase)
	if err != nil {
		t.Fatal(err)
	}
	return inv.ID, created.ID
}

func reversalPreviewInput(workspace, investment, purchase, reason string) InvestmentReversalPreviewInput {
	return InvestmentReversalPreviewInput{
		WorkspaceID: workspace, InvestmentID: investment, PurchaseID: purchase, ActingUserID: "user-a",
		RequestID: uuid.NewString(), Reason: reason, Location: time.UTC,
	}
}

func reversalConfirmInput(previewIn InvestmentReversalPreviewInput, preview InvestmentReversalPreview) ReverseInvestmentPurchaseInput {
	return ReverseInvestmentPurchaseInput{
		WorkspaceID: previewIn.WorkspaceID, InvestmentID: previewIn.InvestmentID, PurchaseID: previewIn.PurchaseID,
		CreatedBy: previewIn.ActingUserID, RequestID: previewIn.RequestID, Reason: previewIn.Reason,
		PreviewToken:          preview.PreviewToken,
		ExpectedSourceVersion: preview.OriginBalanceVersion, ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	}
}

func reversalAccountBalance(t *testing.T, db *sql.DB, account string) (int64, int64) {
	t.Helper()
	var balance, version int64
	if err := db.QueryRow(`SELECT current_balance,balance_version FROM accounts WHERE workspace_id='ws-a' AND id=?`, account).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	return balance, version
}

func reversalSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT
	 (SELECT COUNT(*) FROM transactions) || ':' ||
	 (SELECT COALESCE(group_concat(id || '=' || current_balance || '=' || balance_version, '|'), '') FROM (SELECT id,current_balance,balance_version FROM accounts ORDER BY id)) || ':' ||
	 (SELECT COUNT(*) FROM investment_purchase_reversals) || ':' ||
	 (SELECT COUNT(*) FROM investment_purchases)`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func reversalReceiptCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM investment_purchase_reversals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Scenario 1 and 3: a financed additional purchase is reversed with an exact
// compensating transfer while the initial purchase and an independent
// record-only purchase stay untouched in rows, journal and totals.
func TestInvestmentReversalFinancedExactCompensation(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)
	recordOnly := investmentPurchaseInput(investment)
	recordOnly.PrincipalCents = 20000
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, recordOnly); err != nil {
		t.Fatal(err)
	}

	invBefore, invVersion := reversalAccountBalance(t, pools.DB, "acc-inv-a")
	originBefore, originVersion := reversalAccountBalance(t, pools.DB, "acc-checking-a")
	previewIn := reversalPreviewInput("ws-a", investment, target, "valor digitado errado")
	before := reversalSnapshot(t, pools.DB)
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || preview.Movement != "TRANSFER" || preview.AmountCents != 50000 {
		t.Fatalf("preview: eligible=%v movement=%s amount=%d blockers=%v", preview.Eligible, preview.Movement, preview.AmountCents, preview.Blockers)
	}
	if preview.OriginAccountID != "acc-checking-a" || preview.InvestmentAccountID != "acc-inv-a" {
		t.Fatalf("preview accounts: %+v", preview)
	}
	if reversalSnapshot(t, pools.DB) != before {
		t.Fatal("preview wrote state")
	}

	result, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirmInput(previewIn, preview))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != InvestmentOperationCreated {
		t.Fatalf("outcome=%s", result.Outcome)
	}
	receipt := result.Reversal
	if receipt.Movement != "TRANSFER" || receipt.AmountCents != 50000 ||
		receipt.InvestmentBeforeCents != invBefore || receipt.InvestmentAfterCents != invBefore-50000 ||
		receipt.OriginBeforeCents != originBefore || receipt.OriginAfterCents != originBefore+50000 ||
		receipt.InvestmentBeforeVersion != invVersion || receipt.InvestmentAfterVersion != invVersion+1 ||
		receipt.OriginBeforeVersion != originVersion || receipt.OriginAfterVersion != originVersion+1 {
		t.Fatalf("receipt: %+v", receipt)
	}
	if receipt.OriginalTransactionID != InvestmentPurchaseTransferPrefix+target ||
		receipt.CompensationTransactionID != InvestmentReversalTransferPrefix+receipt.ID {
		t.Fatalf("receipt identities: %+v", receipt)
	}
	var kind, status, effect, source, destination, dateCivil string
	var amount int64
	if err := pools.DB.QueryRow(`SELECT type,status,effect_mode,account_id,destination_account_id,amount,date_civil FROM transactions WHERE workspace_id='ws-a' AND id=?`, receipt.CompensationTransactionID).
		Scan(&kind, &status, &effect, &source, &destination, &amount, &dateCivil); err != nil {
		t.Fatal(err)
	}
	if kind != "TRANSFER" || status != "paid" || effect != "STANDARD" || source != "acc-inv-a" || destination != "acc-checking-a" || amount != 50000 || dateCivil != receipt.EconomicDate {
		t.Fatalf("compensation shape: %s %s %s %s %s %d %s", kind, status, effect, source, destination, amount, dateCivil)
	}
	if invAfter, invAfterVersion := reversalAccountBalance(t, pools.DB, "acc-inv-a"); invAfter != invBefore-50000 || invAfterVersion != invVersion+1 {
		t.Fatalf("investment balance after=%d version=%d", invAfter, invAfterVersion)
	}
	if originAfter, originAfterVersion := reversalAccountBalance(t, pools.DB, "acc-checking-a"); originAfter != originBefore+50000 || originAfterVersion != originVersion+1 {
		t.Fatalf("origin balance after=%d version=%d", originAfter, originAfterVersion)
	}

	// History preserved: three rows, but only the active principal counts.
	detail, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", investment)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Purchases) != 3 || detail.PurchaseCount != 2 || detail.TotalPurchasesCents != 120000 {
		t.Fatalf("detail history=%d count=%d total=%d", len(detail.Purchases), detail.PurchaseCount, detail.TotalPurchasesCents)
	}
	reversed, initial := 0, 0
	for _, p := range detail.Purchases {
		if p.Reversed {
			reversed++
			if p.ID != target {
				t.Fatalf("wrong purchase reversed: %s", p.ID)
			}
		}
		if p.IsInitial && p.PrincipalCents != 100000 {
			t.Fatalf("initial purchase changed: %+v", p)
		}
		if p.IsInitial {
			initial++
		}
	}
	if reversed != 1 || initial != 1 {
		t.Fatalf("reversed=%d initial=%d", reversed, initial)
	}
	totals, err := GetWorkspaceInvestmentPurchaseTotals(ctx, pools.DB, "ws-a")
	if err != nil {
		t.Fatal(err)
	}
	if totals[investment].TotalCents != 120000 || totals[investment].Count != 2 {
		t.Fatalf("workspace totals: %+v", totals[investment])
	}
	// The original purchase transfer is preserved exactly as recorded.
	var originalCount int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-a' AND id=?`, InvestmentPurchaseTransferPrefix+target).Scan(&originalCount); err != nil || originalCount != 1 {
		t.Fatalf("original transfer count=%d err=%v", originalCount, err)
	}
}

// Scenario 2: a record-only purchase reverses logically, with no transfer, no
// balance effect and no balance_version movement.
func TestInvestmentReversalRecordOnlyWithoutFinancialEffect(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, _ := reversalFundedLot(t, pools, 100000)
	recordOnly := investmentPurchaseInput(investment)
	recordOnly.PrincipalCents = 20000
	created, err := CreateInvestmentPurchase(ctx, pools.Immediate, recordOnly)
	if err != nil {
		t.Fatal(err)
	}
	before := reversalSnapshot(t, pools.DB)
	previewIn := reversalPreviewInput("ws-a", investment, created.ID, "reaplicação duplicada")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || preview.Movement != "RECORD" {
		t.Fatalf("preview: eligible=%v movement=%s blockers=%v", preview.Eligible, preview.Movement, preview.Blockers)
	}
	if preview.OriginAccountID != "" || preview.OriginBalanceCents != 0 || preview.OriginBalanceVersion != 0 {
		t.Fatalf("record preview carries origin state: %+v", preview)
	}
	if preview.InvestmentAfterCents != 0 || preview.OriginAfterCents != 0 {
		t.Fatalf("record preview promises balances: %+v", preview)
	}
	result, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirmInput(previewIn, preview))
	if err != nil {
		t.Fatal(err)
	}
	receipt := result.Reversal
	if receipt.Movement != "RECORD" || receipt.OriginalTransactionID != "" || receipt.CompensationTransactionID != "" || receipt.OriginAccountID != "" {
		t.Fatalf("record receipt: %+v", receipt)
	}
	if receipt.InvestmentBeforeCents != receipt.InvestmentAfterCents || receipt.InvestmentBeforeVersion != receipt.InvestmentAfterVersion ||
		receipt.OriginBeforeCents != receipt.OriginAfterCents || receipt.OriginBeforeVersion != receipt.OriginAfterVersion {
		t.Fatalf("record receipt moved balances: %+v", receipt)
	}
	after := reversalSnapshot(t, pools.DB)
	if after == before {
		t.Fatal("receipt row missing")
	}
	// Same journal, same balances: only the receipt count differs.
	if strings.Split(after, ":")[0] != strings.Split(before, ":")[0] ||
		strings.Split(after, ":")[1] != strings.Split(before, ":")[1] ||
		strings.Split(after, ":")[3] != strings.Split(before, ":")[3] {
		t.Fatalf("record reversal moved money: %s -> %s", before, after)
	}
	detail, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", investment)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TotalPurchasesCents != 150000 || detail.PurchaseCount != 2 || len(detail.Purchases) != 3 {
		t.Fatalf("detail after record reversal: total=%d count=%d history=%d", detail.TotalPurchasesCents, detail.PurchaseCount, len(detail.Purchases))
	}
}

// Scenario 4: replay by request_id is a read; a second request for the same
// purchase conflicts; concurrent confirms produce exactly one effect.
func TestInvestmentReversalReplayConflictAndConcurrency(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)
	previewIn := reversalPreviewInput("ws-a", investment, target, "estorno")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	confirm := reversalConfirmInput(previewIn, preview)
	first, err := ReverseInvestmentPurchase(ctx, pools.Immediate, confirm)
	if err != nil {
		t.Fatal(err)
	}
	once := reversalSnapshot(t, pools.DB)
	replay, err := ReverseInvestmentPurchase(ctx, pools.Immediate, confirm)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || replay.Reversal.ID != first.Reversal.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if reversalSnapshot(t, pools.DB) != once {
		t.Fatal("replay wrote state")
	}
	// A fresh request identity for the same purchase conflicts before any
	// token verification: a reversed purchase never issues a new preview.
	second := reversalConfirmInput(reversalPreviewInput("ws-a", investment, target, "estorno"), preview)
	second.PreviewToken = "stale-token-of-a-refused-preview"
	if _, err := ReverseInvestmentPurchase(ctx, pools.Immediate, second); err == nil || !errors.Is(err, ErrInvestmentConflict) || !strings.Contains(err.Error(), "compra já estornada") {
		t.Fatalf("second request err=%v", err)
	}
	if reversalSnapshot(t, pools.DB) != once {
		t.Fatal("conflicting request wrote state")
	}
}

func mustReversalPreview(t *testing.T, db *sql.DB, workspace, investment, purchase, reason string) InvestmentReversalPreview {
	t.Helper()
	preview, err := PreviewInvestmentPurchaseReversal(context.Background(), db, reversalPreviewInput(workspace, investment, purchase, reason))
	if err != nil {
		t.Fatal(err)
	}
	return preview
}

func TestInvestmentReversalConcurrentSingleEffect(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)
	const workers = 4
	previews := make([]ReverseInvestmentPurchaseInput, 0, workers)
	for i := 0; i < workers; i++ {
		in := reversalPreviewInput("ws-a", investment, target, "estorno")
		preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, in)
		if err != nil {
			t.Fatal(err)
		}
		previews = append(previews, reversalConfirmInput(in, preview))
	}
	var mu sync.Mutex
	created, replayed, conflicts := 0, 0, 0
	var wg sync.WaitGroup
	for _, confirm := range previews {
		wg.Add(1)
		go func(in ReverseInvestmentPurchaseInput) {
			defer wg.Done()
			result, err := ReverseInvestmentPurchase(ctx, pools.Immediate, in)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && result.Outcome == InvestmentOperationCreated:
				created++
			case err == nil:
				replayed++
			case errors.Is(err, ErrInvestmentConflict):
				conflicts++
			}
		}(confirm)
	}
	wg.Wait()
	if created != 1 || replayed != 0 || conflicts != workers-1 {
		t.Fatalf("created=%d replayed=%d conflicts=%d", created, replayed, conflicts)
	}
	if got := reversalReceiptCount(t, pools.DB); got != 1 {
		t.Fatalf("receipts=%d", got)
	}
	invBalance, invVersion := reversalAccountBalance(t, pools.DB, "acc-inv-a")
	if invBalance != 100000 || invVersion != 3 {
		t.Fatalf("investment balance=%d version=%d", invBalance, invVersion)
	}
}

// Scenario 5: a failure after the compensating transfer and the balance
// effects rolls the whole confirmation back; the same form then succeeds.
func TestInvestmentReversalInjectedFailureRollback(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)
	previewIn := reversalPreviewInput("ws-a", investment, target, "falha injetada")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	before := reversalSnapshot(t, pools.DB)
	exec(t, pools.Immediate, `CREATE TRIGGER test_reversal_abort_receipt BEFORE INSERT ON investment_purchase_reversals
BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`)
	confirm := reversalConfirmInput(previewIn, preview)
	if _, err := ReverseInvestmentPurchase(ctx, pools.Immediate, confirm); err == nil {
		t.Fatal("injected failure succeeded")
	}
	if after := reversalSnapshot(t, pools.DB); after != before {
		t.Fatalf("failed reversal left writes: %s -> %s", before, after)
	}
	exec(t, pools.Immediate, `DROP TRIGGER test_reversal_abort_receipt`)
	retried, err := ReverseInvestmentPurchase(ctx, pools.Immediate, confirm)
	if err != nil || retried.Outcome != InvestmentOperationCreated {
		t.Fatalf("retry after rollback=%+v err=%v", retried, err)
	}
	if invBalance, _ := reversalAccountBalance(t, pools.DB, "acc-inv-a"); invBalance != 100000 {
		t.Fatalf("investment balance=%d", invBalance)
	}
	if originBalance, _ := reversalAccountBalance(t, pools.DB, "acc-checking-a"); originBalance != 900000 {
		t.Fatalf("origin balance=%d", originBalance)
	}
}

// Scenario 6: ineligible purchases are refused with an explicit reason and no
// state change.
func TestInvestmentReversalBlockers(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)

	// The initial purchase belongs to the full cancellation flow.
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-a", investment, "initial:"+investment, "motivo"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Eligible || !containsReversalBlocker(preview.Blockers, "compra inicial não pode ser estornada individualmente") {
		t.Fatalf("initial blockers=%v", preview.Blockers)
	}

	// A position at or after the purchase makes the effect undecidable.
	insertValuationRow(t, pools.Immediate, "after-purchase", "ws-a", investment, "2026-03-01", 150000)
	preview, err = PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-a", investment, target, "motivo"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Eligible || !containsReversalBlocker(preview.Blockers, "posição igual ou posterior à compra impede determinar o efeito do estorno") {
		t.Fatalf("position blockers=%v", preview.Blockers)
	}
	exec(t, pools.Immediate, `DELETE FROM investment_valuations WHERE workspace_id='ws-a' AND investment_id=?`, investment)

	// A purchase dated in the future cannot be reversed on today's economics.
	future := investmentPurchaseInput(investment)
	future.PrincipalCents = 10000
	future.PurchaseDate = "2030-01-01"
	created, err := CreateInvestmentPurchase(ctx, pools.Immediate, future)
	if err != nil {
		t.Fatal(err)
	}
	preview, err = PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-a", investment, created.ID, "motivo"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Eligible || !containsReversalBlocker(preview.Blockers, "data econômica anterior à compra") {
		t.Fatalf("future blockers=%v", preview.Blockers)
	}

	// A withdrawn purchase has consumed principal: allocations block reversal.
	recordLot := investmentCreateInput("ws-a", "acc-inv-a")
	recordLot.RequestID = uuid.NewString()
	lot := mustCreateInvestment(t, pools.Immediate, recordLot)
	recordPurchase := investmentPurchaseInput(lot.ID)
	recordPurchase.PrincipalCents = 100000
	added, err := CreateInvestmentPurchase(ctx, pools.Immediate, recordPurchase)
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := withdrawalInput(lot.ID, added.ID)
	withdrawal.Allocations[0].PrincipalCents = withdrawalPrincipal(100000)
	if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, withdrawal); err != nil {
		t.Fatal(err)
	}
	preview, err = PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-a", lot.ID, added.ID, "motivo"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Eligible || !containsReversalBlocker(preview.Blockers, "compra possui retirada alocada") {
		t.Fatalf("allocation blockers=%v", preview.Blockers)
	}

	// A fully cancelled lot is frozen for individual reversals too.
	cancelledLotInput := investmentCreateInput("ws-a", "acc-inv-a")
	cancelledLotInput.RequestID = uuid.NewString()
	cancelledLotInput.SourceAccountID = "acc-checking-a"
	cancelledLotInput.PrincipalCents = 30000
	cancelledLot := mustCreateInvestment(t, pools.Immediate, cancelledLotInput)
	cancelPreviewIn := cancellationPreviewInput("ws-a", cancelledLot.ID, "encerrado por engano")
	cancelPreview, err := PreviewInvestmentCancellation(ctx, pools.DB, cancelPreviewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelPreview.Eligible {
		t.Fatalf("cancellation preview blockers=%v", cancelPreview.Blockers)
	}
	if _, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(cancelPreviewIn, cancelPreview)); err != nil {
		t.Fatal(err)
	}
	preview, err = PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-a", cancelledLot.ID, "initial:"+cancelledLot.ID, "motivo"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Eligible || !containsReversalBlocker(preview.Blockers, "aplicação possui cancelamento confirmado") {
		t.Fatalf("cancelled blockers=%v", preview.Blockers)
	}
}

func containsReversalBlocker(blockers []string, want string) bool {
	for _, blocker := range blockers {
		if blocker == want {
			return true
		}
	}
	return false
}

// Scenarios 7 and 8: the receipt survives a restart and replays without the
// signing key, and every generic mutation on the receipt family aborts.
func TestInvestmentReversalReopenRecoveryAndImmutability(t *testing.T) {
	path := t.TempDir() + "/reversal-reopen.db"
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	seedInvestmentServiceFixtures(t, pools.DB)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)
	previewIn := reversalPreviewInput("ws-a", investment, target, "recuperação")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	confirm := reversalConfirmInput(previewIn, preview)
	result, err := ReverseInvestmentPurchase(ctx, pools.Immediate, confirm)
	if err != nil {
		t.Fatal(err)
	}
	closed := reversalSnapshot(t, pools.DB)
	pools.Close()

	reopened, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	reversals, err := GetInvestmentPurchaseReversals(ctx, reopened.DB, "ws-a", investment)
	if err != nil || reversals[target].ID != result.Reversal.ID {
		t.Fatalf("reopened receipts=%+v err=%v", reversals[target], err)
	}
	replay, err := ReverseInvestmentPurchase(ctx, reopened.Immediate, confirm)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || replay.Reversal.ID != result.Reversal.ID {
		t.Fatalf("reopened replay=%+v err=%v", replay, err)
	}
	if after := reversalSnapshot(t, reopened.DB); after != closed {
		t.Fatalf("reopened replay wrote: %s -> %s", closed, after)
	}

	mustAbort := func(statement string, args ...any) {
		t.Helper()
		if _, err := reopened.Immediate.Exec(statement, args...); err == nil {
			t.Fatalf("mutation succeeded: %s", statement)
		}
	}
	mustAbort(`UPDATE investment_purchase_reversals SET reason='rewritten'`)
	mustAbort(`DELETE FROM investment_purchase_reversals`)
	mustAbort(`UPDATE investment_purchases SET principal_cents=1 WHERE workspace_id='ws-a' AND id=?`, target)
	mustAbort(`UPDATE transactions SET description='x' WHERE workspace_id='ws-a' AND id=?`, InvestmentPurchaseTransferPrefix+target)
	mustAbort(`UPDATE transactions SET description='x' WHERE workspace_id='ws-a' AND id=?`, result.Reversal.CompensationTransactionID)
	mustAbort(`DELETE FROM transactions WHERE workspace_id='ws-a' AND id=?`, result.Reversal.CompensationTransactionID)
	mustAbort(`INSERT INTO investment_withdrawal_allocations (workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,closes_purchase)
		VALUES ('ws-a',?,'w-x',?,1,0)`, investment, target)
	if after := reversalSnapshot(t, reopened.DB); after != closed {
		t.Fatalf("blocked mutations wrote: %s -> %s", closed, after)
	}
	// Independent later purchases remain welcome on the same lot.
	again := investmentPurchaseInput(investment)
	again.PrincipalCents = 10000
	if _, err := CreateInvestmentPurchase(ctx, reopened.Immediate, again); err != nil {
		t.Fatalf("later purchase refused: %v", err)
	}
}

// Scenario 9: a foreign workspace is indistinguishable from an unknown
// purchase at every layer, and no cross-tenant row appears.
func TestInvestmentReversalWorkspaceIsolation(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)

	foreignPreview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-b", investment, target, "motivo"))
	if !errors.Is(err, ErrInvestmentNotFound) || foreignPreview.Eligible {
		t.Fatalf("foreign preview err=%v eligible=%v", err, foreignPreview.Eligible)
	}
	// A ws-a token confirmed under ws-b identities stays unknown.
	previewIn := reversalPreviewInput("ws-a", investment, target, "motivo")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	hijack := reversalConfirmInput(previewIn, preview)
	hijack.WorkspaceID, hijack.CreatedBy = "ws-b", "user-b"
	if _, err := ReverseInvestmentPurchase(ctx, pools.Immediate, hijack); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("hijacked confirm err=%v", err)
	}
	unknown, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirmInput(reversalPreviewInput("ws-a", investment, "missing-purchase", "motivo"),
		mustReversalPreview(t, pools.DB, "ws-a", investment, target, "motivo")))
	if !errors.Is(err, ErrInvestmentNotFound) || unknown.Outcome != "" {
		t.Fatalf("unknown purchase err=%v", err)
	}
	if got := reversalReceiptCount(t, pools.DB); got != 0 {
		t.Fatalf("foreign receipts=%d", got)
	}
	var foreignRows int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM investment_purchase_reversals WHERE workspace_id='ws-b'`).Scan(&foreignRows); err != nil || foreignRows != 0 {
		t.Fatalf("ws-b receipts=%d err=%v", foreignRows, err)
	}
	// Same lot, other purchase, still eligible after isolation probes.
	preview, err = PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalPreviewInput("ws-a", investment, target, "motivo"))
	if err != nil || !preview.Eligible {
		t.Fatalf("legitimate preview after probes: eligible=%v err=%v", preview.Eligible, err)
	}
}

// The reversal keeps working after an ordinary independent movement on the
// origin account: only reconciliations and links of the purchase itself block.
func TestInvestmentReversalIndependentMovementsDoNotBlock(t *testing.T) {
	pools := newReversalDB(t)
	ctx := context.Background()
	investment, target := reversalFundedLot(t, pools, 100000)
	addIndependentOriginMovement(t, pools.Immediate, 777)
	previewIn := reversalPreviewInput("ws-a", investment, target, "após movimento independente")
	preview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, previewIn)
	if err != nil || !preview.Eligible {
		t.Fatalf("preview after independent movement: eligible=%v err=%v blockers=%v", preview.Eligible, err, preview.Blockers)
	}
	result, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirmInput(previewIn, preview))
	if err != nil {
		t.Fatal(err)
	}
	if result.Reversal.OriginAfterCents != result.Reversal.OriginBeforeCents+50000 {
		t.Fatalf("origin compensation: %+v", result.Reversal)
	}
	var independent int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-a' AND id='independent-income'`).Scan(&independent); err != nil || independent != 1 {
		t.Fatalf("independent movement=%d err=%v", independent, err)
	}
}
