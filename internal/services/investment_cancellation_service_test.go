package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func newCancellationDB(t *testing.T) *database.RuntimeDBs {
	t.Helper()
	pools, err := database.OpenRuntimeDBs(databaseFilePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pools.Close() })
	seedInvestmentServiceFixtures(t, pools.DB)
	return pools
}

func databaseFilePath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/cancellation.db"
}

func fundedCancellationInvestment(t *testing.T, db *sql.DB, workspace, investmentAccount, originAccount string, principal int64) models.Investment {
	t.Helper()
	input := investmentCreateInput(workspace, investmentAccount)
	input.RequestID = uuid.NewString()
	input.SourceAccountID = originAccount
	input.PrincipalCents = principal
	return mustCreateInvestment(t, db, input)
}

func cancellationPreviewInput(workspace, investment, reason string) InvestmentCancellationPreviewInput {
	return InvestmentCancellationPreviewInput{
		WorkspaceID: workspace, InvestmentID: investment, ActingUserID: "user-a",
		RequestID: uuid.NewString(), Reason: reason, Location: time.UTC,
	}
}

func cancellationConfirmInput(previewIn InvestmentCancellationPreviewInput, preview InvestmentCancellationPreview) CancelInvestmentInput {
	return CancelInvestmentInput{
		WorkspaceID: previewIn.WorkspaceID, InvestmentID: previewIn.InvestmentID, CreatedBy: previewIn.ActingUserID,
		RequestID: previewIn.RequestID, Reason: previewIn.Reason, PreviewToken: preview.PreviewToken,
		ExpectedSourceVersion: preview.OriginBalanceVersion, ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	}
}

func cancellationScalar(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func cancellationSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	return cancellationScalar(t, db, `SELECT
 (SELECT COUNT(*) FROM transactions) || ':' ||
 (SELECT COALESCE(group_concat(id || '=' || current_balance || '=' || balance_version, '|'), '') FROM (SELECT id,current_balance,balance_version FROM accounts ORDER BY id)) || ':' ||
 (SELECT COUNT(*) FROM investment_cancellations) || ':' ||
 (SELECT COUNT(*) FROM investment_purchases) || ':' ||
 (SELECT COUNT(*) FROM account_balance_adjustments)`)
}

func cancellationRowSnapshot(t *testing.T, db *sql.DB, investmentID string) string {
	t.Helper()
	return cancellationScalar(t, db, `SELECT
 (SELECT id||':'||status||':'||principal_cents||':'||applied_date FROM investments WHERE id=?) || '#' ||
 (SELECT id||':'||purchase_date||':'||principal_cents||':'||created_by||':'||created_at FROM investment_purchases WHERE id=?) || '#' ||
 (SELECT id||':'||type||':'||status||':'||effect_mode||':'||amount||':'||date_civil||':'||created_at FROM transactions WHERE id=?)`,
		investmentID, "initial:"+investmentID, InvestmentPurchaseTransferPrefix+"initial:"+investmentID)
}

// addIndependentOriginMovement posts an ordinary paid income on the origin
// account plus its balance effect, outside every investment flow.
func addIndependentOriginMovement(t *testing.T, db *sql.DB, amount int64) {
	t.Helper()
	at := civildateNoon("2026-02-01")
	exec(t, db, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
VALUES('independent-income','ws-a','user-a','acc-checking-a','INCOME',?,?,'2026-02-01',?,'2026-02-01','paid','Rendimento externo',400,400)`, amount, at, at)
	exec(t, db, `UPDATE accounts SET current_balance=current_balance+? WHERE workspace_id='ws-a' AND id='acc-checking-a'`, amount)
}

func civildateNoon(date string) int64 {
	civil, err := civildate.ParseCivilDateInput(date)
	if err != nil {
		panic(err)
	}
	return civildate.CivilDateToLegacyUnixNoonUTC(civil)
}

func TestInvestmentCancellationSyntheticExactCents(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	// Independent balances on both sides before the application: the seed on
	// the investment account proves cancellation must not require a zeroed
	// account nor a balance equal to the principal.
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=111111, current_balance=111111 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=400000000, current_balance=400000000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 357392200)
	// An ordinary movement on the origin account after the purchase must not
	// block the cancellation nor be reverted by it.
	addIndependentOriginMovement(t, pools.Immediate, 5000)
	before := cancellationSnapshot(t, pools.DB)
	originals := cancellationRowSnapshot(t, pools.DB, inv.ID)

	previewIn := cancellationPreviewInput("ws-a", inv.ID, "erro de instituição")
	preview, err := PreviewInvestmentCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible || len(preview.Blockers) != 0 {
		t.Fatalf("preview not eligible: %+v", preview.Blockers)
	}
	if after := cancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("preview wrote: %s -> %s", before, after)
	}
	if preview.AmountCents != 357392200 || preview.InvestmentAccountID != "acc-inv-a" || preview.OriginAccountID != "acc-checking-a" {
		t.Fatalf("preview identity: %+v", preview)
	}
	if preview.InvestmentBalanceCents != 357503311 || preview.OriginBalanceCents != 42612800 {
		t.Fatalf("preview balances: inv=%d origin=%d", preview.InvestmentBalanceCents, preview.OriginBalanceCents)
	}
	if len(preview.PreviewID) != 64 || preview.ExpiresAt-preview.IssuedAt != 900 || preview.PreviewToken == "" {
		t.Fatalf("preview shape: %+v", preview)
	}

	result, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(previewIn, preview))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != InvestmentOperationCreated {
		t.Fatalf("outcome=%s", result.Outcome)
	}
	receipt := result.Cancel
	if receipt.AmountCents != 357392200 || receipt.InvestmentBeforeCents != 357503311 || receipt.InvestmentAfterCents != 111111 ||
		receipt.OriginBeforeCents != 42612800 || receipt.OriginAfterCents != 400005000 {
		t.Fatalf("receipt amounts: %+v", receipt)
	}
	if receipt.InvestmentAfterVersion != receipt.InvestmentBeforeVersion+1 || receipt.OriginAfterVersion != receipt.OriginBeforeVersion+1 {
		t.Fatalf("receipt versions: %+v", receipt)
	}
	if receipt.EconomicDate != preview.EconomicDate || receipt.PreviewID != preview.PreviewID || receipt.RequestID != previewIn.RequestID ||
		receipt.OriginalTransactionID != InvestmentPurchaseTransferPrefix+"initial:"+inv.ID ||
		receipt.CompensationTransactionID != InvestmentCancellationTransferPrefix+receipt.ID {
		t.Fatalf("receipt identity: %+v", receipt)
	}
	transfer := cancellationScalar(t, pools.DB, `SELECT type||':'||status||':'||effect_mode||':'||account_id||':'||destination_account_id||':'||amount||':'||date_civil||':'||created_at||':'||updated_at||':'||paid_at||':'||date FROM transactions WHERE id=?`, receipt.CompensationTransactionID)
	if !strings.HasPrefix(transfer, "TRANSFER:paid:STANDARD:acc-inv-a:acc-checking-a:357392200:"+preview.EconomicDate+":"+fmt.Sprint(receipt.ConfirmedAt)+":"+fmt.Sprint(receipt.ConfirmedAt)+":") {
		t.Fatalf("compensation transfer: %s", transfer)
	}
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance||':'||balance_version FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`); balances != fmt.Sprintf("111111:%d", receipt.InvestmentAfterVersion) {
		t.Fatalf("investment balance after: %s", balances)
	}
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance||':'||balance_version FROM accounts WHERE workspace_id='ws-a' AND id='acc-checking-a'`); balances != fmt.Sprintf("400005000:%d", receipt.OriginAfterVersion) {
		t.Fatalf("origin balance after: %s", balances)
	}
	if again := cancellationRowSnapshot(t, pools.DB, inv.ID); again != originals {
		t.Fatalf("originals changed:\n%s\n%s", originals, again)
	}
	if saved, err := GetInvestmentCancellation(ctx, pools.DB, "ws-a", inv.ID); err != nil || saved.ID != receipt.ID {
		t.Fatalf("persisted receipt=%+v err=%v", saved, err)
	}
	if payload := cancellationScalar(t, pools.DB, `SELECT payload_json FROM investment_cancellations WHERE workspace_id='ws-a' AND id=?`, receipt.ID); !strings.Contains(payload, `"operation":"CANCEL_INITIAL_FUNDED_INVESTMENT"`) || !strings.Contains(payload, preview.PreviewToken) {
		t.Fatalf("payload json: %s", payload)
	}
	if evidence := cancellationScalar(t, pools.DB, `SELECT evidence_json FROM investment_cancellations WHERE workspace_id='ws-a' AND id=?`, receipt.ID); !strings.Contains(evidence, `"evidence_hash":"`+receipt.EvidenceHash+`"`) {
		t.Fatalf("evidence json: %s", evidence)
	}
}

func TestInvestmentCancellationPreviewReadOnlyAndBlockers(t *testing.T) {
	newEligible := func(t *testing.T) (*database.RuntimeDBs, models.Investment) {
		pools := newCancellationDB(t)
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=500000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
		return pools, fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 200000)
	}
	previewOf := func(t *testing.T, db *sql.DB, inv models.Investment) InvestmentCancellationPreview {
		preview, err := PreviewInvestmentCancellation(context.Background(), db, cancellationPreviewInput("ws-a", inv.ID, "motivo"))
		if err != nil {
			t.Fatal(err)
		}
		return preview
	}
	hasBlocker := func(preview InvestmentCancellationPreview, fragment string) bool {
		for _, blocker := range preview.Blockers {
			if strings.Contains(blocker, fragment) {
				return true
			}
		}
		return false
	}

	t.Run("read-only", func(t *testing.T) {
		pools, inv := newEligible(t)
		before := cancellationSnapshot(t, pools.DB)
		previewOf(t, pools.DB, inv)
		if after := cancellationSnapshot(t, pools.DB); after != before {
			t.Fatalf("preview wrote: %s -> %s", before, after)
		}
	})
	t.Run("independent movements stay eligible", func(t *testing.T) {
		pools, inv := newEligible(t)
		addIndependentOriginMovement(t, pools.Immediate, 7500)
		// An ordinary transfer into the investment account is equally independent.
		at := civildateNoon("2026-02-02")
		exec(t, pools.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,created_at,updated_at)
VALUES('independent-topup','ws-a','user-a','acc-checking-a','acc-inv-a','TRANSFER',2500,?,'2026-02-02',?,'2026-02-02','paid',410,410)`, at, at)
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=current_balance-2500 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=current_balance+2500 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
		if preview := previewOf(t, pools.DB, inv); !preview.Eligible {
			t.Fatalf("independent movement blocked: %+v", preview.Blockers)
		}
	})
	t.Run("later purchase blocks", func(t *testing.T) {
		pools, inv := newEligible(t)
		purchase := investmentPurchaseInput(inv.ID)
		purchase.SourceAccountID = "acc-checking-a"
		purchase.PurchaseDate = "2026-03-01"
		if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, purchase); err != nil {
			t.Fatal(err)
		}
		if preview := previewOf(t, pools.DB, inv); preview.Eligible || !hasBlocker(preview, "operações ou dependências posteriores") {
			t.Fatalf("later purchase not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("valuation blocks", func(t *testing.T) {
		pools, inv := newEligible(t)
		insertValuationRow(t, pools.Immediate, "v1", "ws-a", inv.ID, "2026-02-01", 205000)
		if preview := previewOf(t, pools.DB, inv); preview.Eligible || !hasBlocker(preview, "operações ou dependências posteriores") {
			t.Fatalf("valuation not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("reconciliation blocks either account", func(t *testing.T) {
		pools, inv := newEligible(t)
		for _, account := range []string{"acc-checking-a", "acc-inv-a"} {
			tx, err := pools.Immediate.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			version := int64(0)
			balance := int64(0)
			if err = tx.QueryRow(`SELECT balance_version,current_balance FROM accounts WHERE workspace_id='ws-a' AND id=?`, account).Scan(&version, &balance); err != nil {
				t.Fatal(err)
			}
			if err = adjustAccountBalanceTx(context.Background(), tx, AccountBalanceAdjustment{
				ID: uuid.NewString(), WorkspaceID: "ws-a", AccountID: account, UserID: "user-a", Reason: "conciliação de teste",
				ExpectedVersion: version, TargetBalance: balance + 1, CreatedAt: time.Now().Unix()}); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if preview := previewOf(t, pools.DB, inv); preview.Eligible || !hasBlocker(preview, "conciliação") {
				t.Fatalf("adjustment on %s not blocked: %+v", account, preview.Blockers)
			}
		}
	})
	t.Run("closed investment blocks", func(t *testing.T) {
		pools, inv := newEligible(t)
		exec(t, pools.Immediate, `UPDATE investments SET status='RESGATADA', redeemed_date='2026-06-01', redeemed_by='user-b' WHERE workspace_id='ws-a' AND id=?`, inv.ID)
		if preview := previewOf(t, pools.DB, inv); preview.Eligible || !hasBlocker(preview, "não está ativa") {
			t.Fatalf("closed not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("unfunded investment blocks", func(t *testing.T) {
		pools := newCancellationDB(t)
		inv := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
		if preview := previewOf(t, pools.DB, inv); preview.Eligible || !hasBlocker(preview, "transferência original ausente") {
			t.Fatalf("unfunded not blocked: %+v", preview.Blockers)
		}
	})
	t.Run("unknown and foreign investments are not found", func(t *testing.T) {
		pools, inv := newEligible(t)
		if _, err := PreviewInvestmentCancellation(context.Background(), pools.DB, cancellationPreviewInput("ws-a", uuid.NewString(), "motivo")); !errors.Is(err, ErrInvestmentNotFound) {
			t.Fatalf("unknown preview err=%v", err)
		}
		foreign := cancellationPreviewInput("ws-b", inv.ID, "motivo")
		if _, err := PreviewInvestmentCancellation(context.Background(), pools.DB, foreign); !errors.Is(err, ErrInvestmentNotFound) {
			t.Fatalf("foreign preview err=%v", err)
		}
	})
}

func TestInvestmentCancellationReplayAndConflict(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=900000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 300000)
	previewIn := cancellationPreviewInput("ws-a", inv.ID, "mesma chave")
	preview, err := PreviewInvestmentCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	confirm := cancellationConfirmInput(previewIn, preview)
	result, err := CancelInvestment(ctx, pools.Immediate, confirm)
	if err != nil || result.Outcome != InvestmentOperationCreated {
		t.Fatalf("cancel=%+v err=%v", result, err)
	}
	afterFirst := cancellationSnapshot(t, pools.DB)

	replay, err := CancelInvestment(ctx, pools.Immediate, confirm)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || replay.Cancel.ID != result.Cancel.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if afterReplay := cancellationSnapshot(t, pools.DB); afterReplay != afterFirst {
		t.Fatalf("replay wrote: %s -> %s", afterFirst, afterReplay)
	}

	// A restart replaces the signing key: the confirmed receipt must still
	// replay (it never depends on the key), while an unconfirmed preview
	// issued under the old key must fail closed.
	oldKey := investmentCancellationPreviewKey
	investmentCancellationPreviewKey = newInvestmentCancellationPreviewKey()
	defer func() { investmentCancellationPreviewKey = oldKey }()
	afterRotation, err := CancelInvestment(ctx, pools.Immediate, confirm)
	if err != nil || afterRotation.Outcome != InvestmentOperationReplayed || afterRotation.Cancel.ID != result.Cancel.ID {
		t.Fatalf("replay after key rotation=%+v err=%v", afterRotation, err)
	}

	divergent := confirm
	divergent.Reason = "outro motivo"
	if _, err := CancelInvestment(ctx, pools.Immediate, divergent); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("divergent payload err=%v", err)
	}
	foreignToken := confirm
	foreignToken.PreviewToken = "forged." + confirm.PreviewToken[strings.IndexByte(confirm.PreviewToken, '.')+1:]
	if _, err := CancelInvestment(ctx, pools.Immediate, foreignToken); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("foreign token err=%v", err)
	}

	// A second, freshly signed request for the same investment conflicts.
	secondIn := cancellationPreviewInput("ws-a", inv.ID, "segunda tentativa")
	second, err := PreviewInvestmentCancellation(ctx, pools.DB, secondIn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(secondIn, second)); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("second cancellation err=%v", err)
	}

	// An unconfirmed preview signed by the rotated-out key is invalid.
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=800000 WHERE workspace_id='ws-b' AND id='acc-inv-b'`)
	exec(t, pools.Immediate, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-checking-b', 'ws-b', 'Banco B', 'CHECKING', 0, 0)`)
	other := fundedCancellationInvestment(t, pools.Immediate, "ws-b", "acc-inv-b", "acc-checking-b", 50000)
	investmentCancellationPreviewKey = oldKey
	oldPreviewIn := cancellationPreviewInput("ws-b", other.ID, "prévia antiga")
	oldPreview, err := PreviewInvestmentCancellation(ctx, pools.DB, oldPreviewIn)
	if err != nil {
		t.Fatal(err)
	}
	investmentCancellationPreviewKey = newInvestmentCancellationPreviewKey()
	if _, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(oldPreviewIn, oldPreview)); !errors.Is(err, ErrInvestmentCancellationPreview) {
		t.Fatalf("stale-key confirm err=%v", err)
	}
	if count := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='ws-b'`); count != "0" {
		t.Fatalf("stale-key confirm wrote receipts: %s", count)
	}
}

func TestInvestmentCancellationConcurrentSingleEffect(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=900000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 300000)

	t.Run("same request replays", func(t *testing.T) {
		previewIn := cancellationPreviewInput("ws-a", inv.ID, "concorrente")
		preview, err := PreviewInvestmentCancellation(ctx, pools.DB, previewIn)
		if err != nil {
			t.Fatal(err)
		}
		confirm := cancellationConfirmInput(previewIn, preview)
		results := make(chan InvestmentCancellationResult, 4)
		errs := make(chan error, 4)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := CancelInvestment(ctx, pools.Immediate, confirm)
				results <- res
				errs <- err
			}()
		}
		wg.Wait()
		close(results)
		close(errs)
		created, replayed := 0, 0
		receiptID := ""
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		for res := range results {
			switch res.Outcome {
			case InvestmentOperationCreated:
				created++
				receiptID = res.Cancel.ID
			case InvestmentOperationReplayed:
				replayed++
				if receiptID != "" && res.Cancel.ID != receiptID {
					t.Fatalf("divergent receipt ids: %s vs %s", receiptID, res.Cancel.ID)
				}
			}
		}
		if created != 1 || replayed != 3 {
			t.Fatalf("created=%d replayed=%d", created, replayed)
		}
		if transfers := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM transactions WHERE id LIKE 'investment-cancellation:%'`); transfers != "1" {
			t.Fatalf("compensation transfers=%s", transfers)
		}
		if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`); balances != "0" {
			t.Fatalf("investment balance=%s", balances)
		}
		if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-checking-a'`); balances != "900000" {
			t.Fatalf("origin balance=%s", balances)
		}
	})

	t.Run("different requests conflict", func(t *testing.T) {
		second := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 100000)
		first := cancellationPreviewInput("ws-a", second.ID, "primeira")
		secondIn := cancellationPreviewInput("ws-a", second.ID, "segunda")
		firstPreview, err := PreviewInvestmentCancellation(ctx, pools.DB, first)
		if err != nil {
			t.Fatal(err)
		}
		secondPreview, err := PreviewInvestmentCancellation(ctx, pools.DB, secondIn)
		if err != nil {
			t.Fatal(err)
		}
		outcomes := make(chan error, 2)
		var wg sync.WaitGroup
		for _, confirm := range []CancelInvestmentInput{cancellationConfirmInput(first, firstPreview), cancellationConfirmInput(secondIn, secondPreview)} {
			wg.Add(1)
			go func(confirm CancelInvestmentInput) {
				defer wg.Done()
				_, err := CancelInvestment(ctx, pools.Immediate, confirm)
				outcomes <- err
			}(confirm)
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
		if receipts := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='ws-a' AND investment_id=?`, second.ID); receipts != "1" {
			t.Fatalf("receipts=%s", receipts)
		}
		if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`); balances != "0" {
			t.Fatalf("investment balance=%s", balances)
		}
	})
}

func TestInvestmentCancellationOverflowRollback(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 200000)

	t.Run("origin credit would overflow", func(t *testing.T) {
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=? WHERE workspace_id='ws-a' AND id='acc-checking-a'`, math.MaxInt64)
		before := cancellationSnapshot(t, pools.DB)
		preview, err := PreviewInvestmentCancellation(ctx, pools.DB, cancellationPreviewInput("ws-a", inv.ID, "overflow crédito"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || !strings.Contains(strings.Join(preview.Blockers, ";"), "intervalo permitido") {
			t.Fatalf("credit overflow not blocked: %+v", preview.Blockers)
		}
		if after := cancellationSnapshot(t, pools.DB); after != before {
			t.Fatalf("overflow preview wrote: %s -> %s", before, after)
		}
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=300000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	})
	t.Run("investment debit would overflow", func(t *testing.T) {
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=? WHERE workspace_id='ws-a' AND id='acc-inv-a'`, math.MinInt64)
		preview, err := PreviewInvestmentCancellation(ctx, pools.DB, cancellationPreviewInput("ws-a", inv.ID, "overflow débito"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || len(preview.Blockers) != 1 || !strings.Contains(preview.Blockers[0], "intervalo permitido") {
			t.Fatalf("debit overflow not blocked: %+v", preview.Blockers)
		}
		exec(t, pools.Immediate, `UPDATE accounts SET current_balance=200000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	})
	t.Run("balance version at the limit", func(t *testing.T) {
		exec(t, pools.Immediate, `UPDATE accounts SET balance_version=? WHERE workspace_id='ws-a' AND id='acc-checking-a'`, int64(math.MaxInt64))
		preview, err := PreviewInvestmentCancellation(ctx, pools.DB, cancellationPreviewInput("ws-a", inv.ID, "versão limite"))
		if err != nil {
			t.Fatal(err)
		}
		if preview.Eligible || len(preview.Blockers) != 1 || !strings.Contains(preview.Blockers[0], "intervalo permitido") {
			t.Fatalf("version limit not blocked: %+v", preview.Blockers)
		}
	})
	if count := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM transactions WHERE id LIKE 'investment-cancellation:%'`); count != "0" {
		t.Fatalf("overflow attempts wrote transfers: %s", count)
	}
	if count := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_cancellations`); count != "0" {
		t.Fatalf("overflow attempts wrote receipts: %s", count)
	}
}

func TestInvestmentCancellationInjectedFailureRollback(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=900000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 250000)
	previewIn := cancellationPreviewInput("ws-a", inv.ID, "falha injetada")
	preview, err := PreviewInvestmentCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	before := cancellationSnapshot(t, pools.DB)
	// Abort the receipt insert, which happens after the compensating transfer
	// and both balance effects: the whole transaction must roll back.
	exec(t, pools.Immediate, `CREATE TRIGGER test_cancellation_abort_receipt BEFORE INSERT ON investment_cancellations
BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`)
	confirm := cancellationConfirmInput(previewIn, preview)
	if _, err := CancelInvestment(ctx, pools.Immediate, confirm); err == nil {
		t.Fatal("injected failure succeeded")
	}
	if after := cancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("failed cancel left writes: %s -> %s", before, after)
	}
	exec(t, pools.Immediate, `DROP TRIGGER test_cancellation_abort_receipt`)
	retried, err := CancelInvestment(ctx, pools.Immediate, confirm)
	if err != nil || retried.Outcome != InvestmentOperationCreated {
		t.Fatalf("retry after rollback=%+v err=%v", retried, err)
	}
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`); balances != "0" {
		t.Fatalf("investment balance=%s", balances)
	}
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-checking-a'`); balances != "900000" {
		t.Fatalf("origin balance=%s", balances)
	}
}

func TestInvestmentCancellationReopenRecovery(t *testing.T) {
	path := databaseFilePath(t)
	pools, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	seedInvestmentServiceFixtures(t, pools.DB)
	ctx := context.Background()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=600000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 150000)
	previewIn := cancellationPreviewInput("ws-a", inv.ID, "recuperação")
	preview, err := PreviewInvestmentCancellation(ctx, pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	confirm := cancellationConfirmInput(previewIn, preview)
	result, err := CancelInvestment(ctx, pools.Immediate, confirm)
	if err != nil {
		t.Fatal(err)
	}
	closed := cancellationSnapshot(t, pools.DB)
	pools.Close()

	reopened, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	saved, err := GetInvestmentCancellation(ctx, reopened.DB, "ws-a", inv.ID)
	if err != nil || saved.ID != result.Cancel.ID {
		t.Fatalf("reopened receipt=%+v err=%v", saved, err)
	}
	replay, err := CancelInvestment(ctx, reopened.Immediate, confirm)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || replay.Cancel.ID != result.Cancel.ID {
		t.Fatalf("reopened replay=%+v err=%v", replay, err)
	}
	if after := cancellationSnapshot(t, reopened.DB); after != closed {
		t.Fatalf("reopened replay wrote: %s -> %s", closed, after)
	}
	if version := cancellationScalar(t, reopened.DB, `SELECT MAX(version) FROM schema_migrations`); version != "82" {
		t.Fatalf("schema version=%s", version)
	}
}

func TestInvestmentCancellationWorkspaceIsolation(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=700000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 200000)

	foreignPreview := cancellationPreviewInput("ws-b", inv.ID, "invasão")
	if _, err := PreviewInvestmentCancellation(ctx, pools.DB, foreignPreview); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign preview err=%v", err)
	}
	preview, err := PreviewInvestmentCancellation(ctx, pools.DB, cancellationPreviewInput("ws-a", inv.ID, "válida"))
	if err != nil {
		t.Fatal(err)
	}
	foreignConfirm := cancellationConfirmInput(foreignPreview, preview)
	foreignConfirm.PreviewToken = preview.PreviewToken
	foreignConfirm.ExpectedSourceVersion = preview.OriginBalanceVersion
	foreignConfirm.ExpectedInvestmentVersion = preview.InvestmentBalanceVersion
	if _, err := CancelInvestment(ctx, pools.Immediate, foreignConfirm); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign cancel err=%v", err)
	}
	if leaked := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='ws-b'`); leaked != "0" {
		t.Fatalf("workspace leak: %s", leaked)
	}

	// The same idempotency key is scoped per workspace: ws-b reusing the ws-a
	// request id for its own investment must not collide.
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=800000 WHERE workspace_id='ws-b' AND id='acc-inv-b'`)
	exec(t, pools.Immediate, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-checking-b', 'ws-b', 'Banco B', 'CHECKING', 0, 0)`)
	other := fundedCancellationInvestment(t, pools.Immediate, "ws-b", "acc-inv-b", "acc-checking-b", 100000)
	sharedKey := cancellationPreviewInput("ws-a", inv.ID, "válida").RequestID
	ownPreviewIn := InvestmentCancellationPreviewInput{
		WorkspaceID: "ws-b", InvestmentID: other.ID, ActingUserID: "user-b",
		RequestID: sharedKey, Reason: "chave repetida", Location: time.UTC,
	}
	ownPreview, err := PreviewInvestmentCancellation(ctx, pools.DB, ownPreviewIn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CancelInvestment(ctx, pools.Immediate, cancellationConfirmInput(ownPreviewIn, ownPreview)); err != nil {
		t.Fatal(err)
	}
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-b' AND id='acc-inv-b'`); balances != "800000" {
		t.Fatalf("ws-b investment balance=%s", balances)
	}
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-b' AND id='acc-checking-b'`); balances != "0" {
		t.Fatalf("ws-b checking balance=%s", balances)
	}
	if untouched := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='ws-a'`); untouched != "0" {
		t.Fatalf("ws-a cancellation before its own confirm: %s", untouched)
	}
}
