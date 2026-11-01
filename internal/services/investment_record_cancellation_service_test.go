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
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

// Schema 81 record-only registration cancellation: the receipt is the only
// write, every balance stays byte-identical and the history is preserved.

func recordCancellationInvestment(t *testing.T, db *sql.DB, workspace, investmentAccount string, principal int64) models.Investment {
	t.Helper()
	input := investmentCreateInput(workspace, investmentAccount)
	input.RequestID = uuid.NewString()
	input.PrincipalCents = principal
	// Empty SourceAccountID is the explicit record-only representation.
	return mustCreateInvestment(t, db, input)
}

func recordCancellationPreviewInput(workspace, investment, reason string) InvestmentRecordCancellationPreviewInput {
	return InvestmentRecordCancellationPreviewInput{
		WorkspaceID: workspace, InvestmentID: investment, ActingUserID: "user-a",
		RequestID: uuid.NewString(), Reason: reason, Location: time.UTC,
	}
}

func recordCancellationConfirmInput(previewIn InvestmentRecordCancellationPreviewInput, preview InvestmentRecordCancellationPreview) CancelInvestmentRecordInput {
	return CancelInvestmentRecordInput{
		WorkspaceID: previewIn.WorkspaceID, InvestmentID: previewIn.InvestmentID, CreatedBy: previewIn.ActingUserID,
		RequestID: previewIn.RequestID, Reason: previewIn.Reason, PreviewToken: preview.PreviewToken,
		ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	}
}

// recordCancellationSnapshot captures every financial surface plus both
// cancellation families: the zero-effect proof compares this byte for byte.
func recordCancellationSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return cancellationScalar(t, db, `SELECT
	 (SELECT COUNT(*) FROM transactions) || ':' ||
	 (SELECT COALESCE(group_concat(id || '=' || current_balance || '=' || balance_version, '|'), '') FROM (SELECT id,current_balance,balance_version FROM accounts ORDER BY id)) || ':' ||
	 (SELECT COUNT(*) FROM investment_cancellations) || ':' ||
	 (SELECT COUNT(*) FROM investment_record_cancellations) || ':' ||
	 (SELECT COUNT(*) FROM investment_purchases) || ':' ||
	 (SELECT COUNT(*) FROM investment_purchase_reversals) || ':' ||
	 (SELECT COUNT(*) FROM account_balance_adjustments)`)
}

func recordCancellationHistorySnapshot(t *testing.T, db *sql.DB, investmentID string) string {
	t.Helper()
	return cancellationScalar(t, db, `SELECT
	 (SELECT id||':'||status||':'||principal_cents||':'||applied_date||':'||COALESCE(archived_at,0) FROM investments WHERE id=?) || '#' ||
	 (SELECT id||':'||purchase_date||':'||principal_cents||':'||is_initial||':'||created_by||':'||created_at FROM investment_purchases WHERE id=?)`,
		investmentID, "initial:"+investmentID)
}

// TestInvestmentRecordCancellationZeroEffect covers scenarios 1-6: create a
// record-only initial application, prove the transfer is absent, preview and
// confirm, prove every balance and version is byte-identical, the receipt is
// durable, the history is preserved and the lot is cancelled.
func TestInvestmentRecordCancellationZeroEffect(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	// The investment account carries an independent balance: the record-only
	// registration never touched it and the cancellation must not either.
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=777777, current_balance=777777 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 357392200)
	if transfers := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM transactions WHERE id=?`, InvestmentPurchaseTransferPrefix+"initial:"+inv.ID); transfers != "0" {
		t.Fatalf("record-only lot carries a transfer: %s", transfers)
	}
	// An independent movement on the checking account must survive untouched.
	addIndependentOriginMovement(t, pools.Immediate, 5000)
	before := recordCancellationSnapshot(t, pools.DB)
	history := recordCancellationHistorySnapshot(t, pools.DB, inv.ID)

	previewIn := recordCancellationPreviewInput("ws-a", inv.ID, "valor correto é outro")
	preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || len(preview.Blockers) != 0 {
		t.Fatalf("preview not eligible: %+v", preview.Blockers)
	}
	if after := recordCancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("preview wrote: %s -> %s", before, after)
	}
	if preview.AmountCents != 357392200 || preview.InvestmentAccountID != "acc-inv-a" || preview.InvestmentBalanceCents != 777777 {
		t.Fatalf("preview identity: %+v", preview)
	}
	if len(preview.PreviewID) != 64 || preview.ExpiresAt-preview.IssuedAt != 900 || preview.PreviewToken == "" {
		t.Fatalf("preview shape: %+v", preview)
	}

	result, err := CancelInvestmentRecord(ctx, pools.Immediate, recordCancellationConfirmInput(previewIn, preview))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != InvestmentOperationCreated {
		t.Fatalf("outcome=%s", result.Outcome)
	}
	receipt := result.Cancel
	if receipt.AmountCents != 357392200 || receipt.PurchaseID != "initial:"+inv.ID ||
		receipt.InvestmentBeforeCents != 777777 || receipt.InvestmentAfterCents != 777777 ||
		receipt.InvestmentBeforeVersion != receipt.InvestmentAfterVersion {
		t.Fatalf("receipt zero-effect witness: %+v", receipt)
	}
	if receipt.EconomicDate != preview.EconomicDate || receipt.PreviewID != preview.PreviewID || receipt.RequestID != previewIn.RequestID {
		t.Fatalf("receipt identity: %+v", receipt)
	}
	// The single allowed difference is the receipt row itself.
	after := recordCancellationSnapshot(t, pools.DB)
	if after == before {
		t.Fatalf("receipt row missing from snapshot: %s", after)
	}
	partsBefore, partsAfter := strings.Split(before, ":"), strings.Split(after, ":")
	if len(partsBefore) != 7 || len(partsAfter) != 7 {
		t.Fatalf("snapshot shape: %q -> %q", before, after)
	}
	for i := range partsBefore {
		switch i {
		case 3: // investment_record_cancellations: 0 -> 1
			if partsAfter[i] != "1" || partsBefore[i] != "0" {
				t.Fatalf("receipt count %s -> %s", partsBefore[i], partsAfter[i])
			}
		default:
			if partsBefore[i] != partsAfter[i] {
				t.Fatalf("snapshot segment %d changed: %s -> %s", i, partsBefore[i], partsAfter[i])
			}
		}
	}
	if again := recordCancellationHistorySnapshot(t, pools.DB, inv.ID); again != history {
		t.Fatalf("history changed:\n%s\n%s", history, again)
	}
	saved, err := GetInvestmentRecordCancellation(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || saved.ID != receipt.ID {
		t.Fatalf("persisted receipt=%+v err=%v", saved, err)
	}
	if payload := cancellationScalar(t, pools.DB, `SELECT payload_json FROM investment_record_cancellations WHERE id=?`, receipt.ID); !strings.Contains(payload, `"operation":"CANCEL_INITIAL_RECORD_INVESTMENT"`) || !strings.Contains(payload, preview.PreviewToken) {
		t.Fatalf("payload json: %s", payload)
	}
	if evidence := cancellationScalar(t, pools.DB, `SELECT evidence_json FROM investment_record_cancellations WHERE id=?`, receipt.ID); !strings.Contains(evidence, `"evidence_hash":"`+receipt.EvidenceHash+`"`) {
		t.Fatalf("evidence json: %s", evidence)
	}
	// The cancelled lot refuses every later mutation of its own family.
	tx, err := pools.Immediate.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectCancelledInvestmentTx(ctx, tx, "ws-a", inv.ID); !errors.Is(err, ErrInvestmentCancelled) {
		t.Fatalf("guard after record cancellation err=%v", err)
	}
	tx.Rollback()
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, CreateInvestmentPurchaseInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-a", RequestID: uuid.NewString(),
		PurchaseDate: "2026-06-01", PrincipalCents: 1000, Kind: "CDB", TaxRegime: models.InvestmentTaxRegimeIRRegressivo,
	}); !errors.Is(err, ErrInvestmentCancelled) {
		t.Fatalf("new purchase after cancellation err=%v", err)
	}
}

// TestInvestmentRecordCancellationReplayAndConcurrency covers scenarios 7-9:
// replay by request_id, concurrent confirms and injected-failure rollback.
func TestInvestmentRecordCancellationReplayAndConcurrency(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 200000)

	previewIn := recordCancellationPreviewInput("ws-a", inv.ID, "replay")
	preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	confirm := recordCancellationConfirmInput(previewIn, preview)
	results := make(chan InvestmentRecordCancellationResult, 4)
	errs := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := CancelInvestmentRecord(ctx, pools.Immediate, confirm)
			results <- res
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	created, replayed := 0, 0
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for res := range results {
		switch res.Outcome {
		case InvestmentOperationCreated:
			created++
		case InvestmentOperationReplayed:
			replayed++
		}
	}
	if created != 1 || replayed != 3 {
		t.Fatalf("created=%d replayed=%d", created, replayed)
	}
	if receipts := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_record_cancellations`); receipts != "1" {
		t.Fatalf("receipts=%s", receipts)
	}
	// A changed payload on the same request id is a conflict, never a rewrite.
	changed := confirm
	changed.Reason = "outro motivo"
	if _, err := CancelInvestmentRecord(ctx, pools.Immediate, changed); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	// A different request on the same lot is a conflict.
	secondIn := recordCancellationPreviewInput("ws-a", inv.ID, "segunda")
	secondPreview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, secondIn)
	if err != nil {
		t.Fatal(err)
	}
	if secondPreview.Eligible {
		t.Fatal("cancelled lot preview eligible")
	}
	if _, err := CancelInvestmentRecord(ctx, pools.Immediate, recordCancellationConfirmInput(secondIn, secondPreview)); err == nil {
		t.Fatal("confirm without a signed preview succeeded")
	}

	// Two concurrent different requests on a second lot: exactly one receipt.
	other := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 100000)
	firstIn := recordCancellationPreviewInput("ws-a", other.ID, "primeira")
	second2In := recordCancellationPreviewInput("ws-a", other.ID, "segunda")
	firstPreview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, firstIn)
	if err != nil {
		t.Fatal(err)
	}
	second2Preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, second2In)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan error, 2)
	wg = sync.WaitGroup{}
	for _, c := range []CancelInvestmentRecordInput{recordCancellationConfirmInput(firstIn, firstPreview), recordCancellationConfirmInput(second2In, second2Preview)} {
		wg.Add(1)
		go func(c CancelInvestmentRecordInput) {
			defer wg.Done()
			_, err := CancelInvestmentRecord(ctx, pools.Immediate, c)
			outcomes <- err
		}(c)
	}
	wg.Wait()
	close(outcomes)
	conflicts := 0
	for err := range outcomes {
		switch {
		case err == nil:
		case errors.Is(err, ErrInvestmentConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("expected one conflict, got %d", conflicts)
	}
	if receipts := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_record_cancellations WHERE investment_id=?`, other.ID); receipts != "1" {
		t.Fatalf("second lot receipts=%s", receipts)
	}
}

func TestInvestmentRecordCancellationInjectedFailureRollback(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 250000)
	previewIn := recordCancellationPreviewInput("ws-a", inv.ID, "falha injetada")
	preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	before := recordCancellationSnapshot(t, pools.DB)
	exec(t, pools.Immediate, `CREATE TRIGGER test_record_cancel_abort BEFORE INSERT ON investment_record_cancellations
BEGIN SELECT RAISE(ABORT, 'injected record receipt failure'); END`)
	confirm := recordCancellationConfirmInput(previewIn, preview)
	if _, err := CancelInvestmentRecord(ctx, pools.Immediate, confirm); err == nil {
		t.Fatal("injected failure succeeded")
	}
	if after := recordCancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("failed cancel left writes: %s -> %s", before, after)
	}
	exec(t, pools.Immediate, `DROP TRIGGER test_record_cancel_abort`)
	retried, err := CancelInvestmentRecord(ctx, pools.Immediate, confirm)
	if err != nil || retried.Outcome != InvestmentOperationCreated {
		t.Fatalf("retry after rollback=%+v err=%v", retried, err)
	}
	if receipts := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_record_cancellations`); receipts != "1" {
		t.Fatalf("receipts=%s", receipts)
	}
}

// TestInvestmentRecordCancellationBlockers covers scenario 10: additional
// purchases and every dependency of the 79 family refuse the cancellation.
func TestInvestmentRecordCancellationBlockers(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()

	t.Run("additional purchase", func(t *testing.T) {
		inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 100000)
		if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, CreateInvestmentPurchaseInput{
			WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-a", RequestID: uuid.NewString(),
			PurchaseDate: "2026-02-01", PrincipalCents: 50000, Kind: "CDB", TaxRegime: models.InvestmentTaxRegimeIRRegressivo,
		}); err != nil {
			t.Fatal(err)
		}
		preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, recordCancellationPreviewInput("ws-a", inv.ID, "lote"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || !containsBlocker(preview.Blockers, "aplicação possui operações ou dependências posteriores") {
			t.Fatalf("additional purchase not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("position", func(t *testing.T) {
		inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 100000)
		if _, err := UpsertInvestmentValuation(ctx, pools.Immediate, UpsertInvestmentValuationInput{
			WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a",
			ValuationDate: "2026-03-01", AmountCents: 101000,
		}); err != nil {
			t.Fatal(err)
		}
		preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, recordCancellationPreviewInput("ws-a", inv.ID, "posição"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || !containsBlocker(preview.Blockers, "aplicação possui operações ou dependências posteriores") {
			t.Fatalf("position not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("funded lot uses schema 79", func(t *testing.T) {
		inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 100000)
		preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, recordCancellationPreviewInput("ws-a", inv.ID, "financiada"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || !containsBlocker(preview.Blockers, "aplicação possui transferência automática vinculada") {
			t.Fatalf("funded lot not diverted to 79: %+v", preview.Blockers)
		}
	})
	t.Run("balance adjustment", func(t *testing.T) {
		inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 100000)
		var balance, version int64
		if err := pools.DB.QueryRow(`SELECT current_balance,balance_version FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&balance, &version); err != nil {
			t.Fatal(err)
		}
		exec(t, pools.Immediate, `INSERT INTO account_balance_adjustments(id,workspace_id,account_id,before_balance,after_balance,delta_cents,expected_version,reason,created_by,created_at) VALUES('adj','ws-a','acc-inv-a',?,?,1,?,'sintético','user-a',1)`, balance, balance+1, version)
		preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, recordCancellationPreviewInput("ws-a", inv.ID, "conciliação"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || !containsBlocker(preview.Blockers, "conciliação presente na conta de investimento") {
			t.Fatalf("adjustment not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("stale version", func(t *testing.T) {
		// Own database: the previous subtests left a reconciliation on the
		// shared investment account, which is a legitimate blocker there.
		fresh := newCancellationDB(t)
		inv := recordCancellationInvestment(t, fresh.Immediate, "ws-a", "acc-inv-a", 100000)
		exec(t, fresh.Immediate, `UPDATE accounts SET current_balance=current_balance WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
		previewIn := recordCancellationPreviewInput("ws-a", inv.ID, "versão")
		preview, err := PreviewInvestmentRecordCancellation(ctx, fresh.DB, previewIn)
		if err != nil {
			t.Fatal(err)
		}
		if !preview.Eligible {
			t.Fatalf("preview must stay eligible: %+v", preview.Blockers)
		}
		confirm := recordCancellationConfirmInput(previewIn, preview)
		confirm.ExpectedInvestmentVersion = preview.InvestmentBalanceVersion - 1
		if _, err := CancelInvestmentRecord(ctx, fresh.Immediate, confirm); !errors.Is(err, ErrBalanceAdjustmentConflict) {
			t.Fatalf("stale version err=%v", err)
		}
		if receipts := cancellationScalar(t, fresh.DB, `SELECT COUNT(*) FROM investment_record_cancellations`); receipts != "0" {
			t.Fatalf("stale version wrote receipts: %s", receipts)
		}
	})
}

func containsBlocker(blockers []string, want string) bool {
	for _, blocker := range blockers {
		if blocker == want {
			return true
		}
	}
	return false
}

// TestInvestmentRecordCancellationPreserves79And80 covers scenarios 11-12:
// the funded integral cancellation and the individual reversal keep working
// beside the new family, and a record-cancelled lot refuses both.
func TestInvestmentRecordCancellationPreserves79And80(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=900000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)

	funded := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 120000)
	fundedIn := cancellationPreviewInput("ws-a", funded.ID, "79 preservada")
	fundedPreview, err := PreviewInvestmentCancellation(ctx, pools.DB, fundedIn)
	if err != nil {
		t.Fatal(err)
	}
	if !fundedPreview.Eligible {
		t.Fatalf("funded cancellation no longer eligible: %+v", fundedPreview.Blockers)
	}
	fundedResult, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(fundedIn, fundedPreview))
	if err != nil {
		t.Fatal(err)
	}
	if fundedResult.Outcome != InvestmentOperationCreated || fundedResult.Cancel.CompensationTransactionID == "" {
		t.Fatalf("schema 79 result: %+v", fundedResult.Cancel)
	}

	// Individual reversal (80) on a record-only lot with a later purchase.
	lot := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 100000)
	purchase, err := CreateInvestmentPurchase(ctx, pools.Immediate, CreateInvestmentPurchaseInput{
		WorkspaceID: "ws-a", InvestmentID: lot.ID, CreatedBy: "user-a", RequestID: uuid.NewString(),
		PurchaseDate: "2026-02-01", PrincipalCents: 30000, Kind: "CDB", TaxRegime: models.InvestmentTaxRegimeIRRegressivo,
	})
	if err != nil {
		t.Fatal(err)
	}
	reversalIn := InvestmentReversalPreviewInput{
		WorkspaceID: "ws-a", InvestmentID: lot.ID, PurchaseID: purchase.ID, ActingUserID: "user-a",
		RequestID: uuid.NewString(), Reason: "estorno preservado", Location: time.UTC,
	}
	reversalPreview, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, reversalIn)
	if err != nil {
		t.Fatal(err)
	}
	if !reversalPreview.Eligible || reversalPreview.Movement != "RECORD" {
		t.Fatalf("reversal preview: eligible=%v movement=%s blockers=%v", reversalPreview.Eligible, reversalPreview.Movement, reversalPreview.Blockers)
	}
	reversalConfirm := ReverseInvestmentPurchaseInput{
		WorkspaceID: reversalIn.WorkspaceID, InvestmentID: reversalIn.InvestmentID, PurchaseID: reversalIn.PurchaseID,
		CreatedBy: reversalIn.ActingUserID, RequestID: reversalIn.RequestID, Reason: reversalIn.Reason,
		PreviewToken: reversalPreview.PreviewToken, ExpectedSourceVersion: reversalPreview.OriginBalanceVersion,
		ExpectedInvestmentVersion: reversalPreview.InvestmentBalanceVersion,
	}
	reversal, err := ReverseInvestmentPurchase(ctx, pools.Immediate, reversalConfirm)
	if err != nil || reversal.Outcome != InvestmentOperationCreated {
		t.Fatalf("schema 80 reversal=%+v err=%v", reversal, err)
	}

	// A record-cancelled lot refuses the funded cancellation and both its own
	// purchases and their reversals.
	cancelled := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 100000)
	cancelledIn := recordCancellationPreviewInput("ws-a", cancelled.ID, "registro")
	cancelledPreview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, cancelledIn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CancelInvestmentRecord(ctx, pools.Immediate, recordCancellationConfirmInput(cancelledIn, cancelledPreview)); err != nil {
		t.Fatal(err)
	}
	if _, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(cancellationPreviewInput("ws-a", cancelled.ID, "79 sobre 81"), InvestmentCancellationPreview{
		PreviewToken: "forged", OriginBalanceVersion: 0, InvestmentBalanceVersion: cancelledPreview.InvestmentBalanceVersion,
	})); err == nil {
		t.Fatal("schema 79 accepted a record-cancelled lot")
	}
	if _, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, recordCancellationPreviewInput("ws-a", cancelled.ID, "de novo")); err != nil {
		t.Fatal(err)
	}
	reversalAfter, err := PreviewInvestmentPurchaseReversal(ctx, pools.DB, InvestmentReversalPreviewInput{
		WorkspaceID: "ws-a", InvestmentID: cancelled.ID, PurchaseID: "initial:" + cancelled.ID, ActingUserID: "user-a",
		RequestID: uuid.NewString(), Reason: "estorno sobre cancelada", Location: time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reversalAfter.Eligible {
		t.Fatal("initial purchase reversal eligible after record cancellation")
	}
}

// TestInvestmentRecordCancellationWorkspaceIsolation covers scenario 13.
func TestInvestmentRecordCancellationWorkspaceIsolation(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 200000)

	foreignPreview := recordCancellationPreviewInput("ws-b", inv.ID, "invasão")
	if _, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, foreignPreview); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign preview err=%v", err)
	}
	preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, recordCancellationPreviewInput("ws-a", inv.ID, "válida"))
	if err != nil {
		t.Fatal(err)
	}
	foreignConfirm := recordCancellationConfirmInput(foreignPreview, preview)
	foreignConfirm.PreviewToken = preview.PreviewToken
	foreignConfirm.ExpectedInvestmentVersion = preview.InvestmentBalanceVersion
	if _, err := CancelInvestmentRecord(ctx, pools.Immediate, foreignConfirm); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign cancel err=%v", err)
	}
	if leaked := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_record_cancellations WHERE workspace_id='ws-b'`); leaked != "0" {
		t.Fatalf("workspace leak: %s", leaked)
	}
	if _, err := GetInvestmentRecordCancellation(ctx, pools.DB, "ws-b", inv.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign receipt read err=%v", err)
	}
}

// TestInvestmentRecordCancellationReopenWithUpdatedSchema covers scenario 14:
// reopen the database with schema 81 and replay through the durable receipt.
func TestInvestmentRecordCancellationReopenWithUpdatedSchema(t *testing.T) {
	path := databaseFilePath(t)
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	seedInvestmentServiceFixtures(t, pools.DB)
	ctx := context.Background()
	inv := recordCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", 150000)
	previewIn := recordCancellationPreviewInput("ws-a", inv.ID, "reabertura")
	preview, err := PreviewInvestmentRecordCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	confirm := recordCancellationConfirmInput(previewIn, preview)
	result, err := CancelInvestmentRecord(ctx, pools.Immediate, confirm)
	if err != nil {
		t.Fatal(err)
	}
	closed := recordCancellationSnapshot(t, pools.DB)
	// An independent movement after the cancellation must neither break the
	// receipt nor the reopen validation: the pins are insert-time only.
	addIndependentOriginMovement(t, pools.Immediate, 7000)
	pools.Close()

	reopened, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if version := cancellationScalar(t, reopened.DB, `SELECT MAX(version) FROM schema_migrations`); version != "82" {
		t.Fatalf("schema version=%s", version)
	}
	saved, err := GetInvestmentRecordCancellation(ctx, reopened.DB, "ws-a", inv.ID)
	if err != nil || saved.ID != result.Cancel.ID {
		t.Fatalf("reopened receipt=%+v err=%v", saved, err)
	}
	replay, err := CancelInvestmentRecord(ctx, reopened.Immediate, confirm)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || replay.Cancel.ID != result.Cancel.ID {
		t.Fatalf("reopened replay=%+v err=%v", replay, err)
	}
	// The replay wrote nothing; the snapshot difference is only the
	// independent movement added before closing.
	if again := recordCancellationSnapshot(t, reopened.DB); again == closed {
		t.Fatal("expected the independent movement in the reopened snapshot")
	}
	if receipts := cancellationScalar(t, reopened.DB, `SELECT COUNT(*) FROM investment_record_cancellations`); receipts != "1" {
		t.Fatalf("reopened receipts=%s", receipts)
	}
}
