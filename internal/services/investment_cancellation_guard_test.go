package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

// Microetapa B7-I2-C: every existing mutation path that could modify a
// cancelled lot, its protected transfers or its dependencies must refuse with
// ErrInvestmentCancelled (a typed conflict) before any balance effect, while
// active lots, foreign workspaces and independent account movements stay
// untouched.

// cancelledLotForGuard builds the minimal cancelled state: one funded initial
// purchase and one confirmed cancellation receipt in ws-a.
func cancelledLotForGuard(t *testing.T, pools *database.RuntimeDBs, principal int64) models.Investment {
	t.Helper()
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=100000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", principal)
	previewIn := cancellationPreviewInput("ws-a", inv.ID, "proteção pós-cancelamento")
	preview, err := PreviewInvestmentCancellation(context.Background(), pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible {
		t.Fatalf("preview blockers: %+v", preview.Blockers)
	}
	if _, err := CancelInvestment(context.Background(), pools.Immediate, cancellationConfirmInput(previewIn, preview)); err != nil {
		t.Fatal(err)
	}
	return inv
}

// guardPurchaseInput passes pre-transaction validation so the refusal under
// test is the cancellation guard, not form validation.
func guardPurchaseInput(workspace, investment, source string) CreateInvestmentPurchaseInput {
	return CreateInvestmentPurchaseInput{
		WorkspaceID: workspace, InvestmentID: investment, CreatedBy: "user-a", RequestID: uuid.NewString(),
		SourceAccountID: source, PurchaseDate: "2026-02-10", PrincipalCents: 1000,
		Kind: "CDB", Institution: "Banco X", TaxRegime: models.InvestmentTaxRegimeIRRegressivo,
	}
}

func guardUpdateInput(workspace, investment string) UpdateInvestmentInput {
	return UpdateInvestmentInput{
		WorkspaceID: workspace, InvestmentID: investment, UpdatedAt: time.Now().Unix(),
		Name: "Renomeado", Institution: "Banco X", Kind: "CDB", TaxRegime: models.InvestmentTaxRegimeIRRegressivo,
	}
}

func TestInvestmentCancellationGuardBlocksLotMutations(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	inv := cancelledLotForGuard(t, pools, 50000)
	before := cancellationSnapshot(t, pools.DB)

	attempts := map[string]func() error{
		"compra com aporte": func() error {
			_, err := CreateInvestmentPurchase(ctx, pools.Immediate, guardPurchaseInput("ws-a", inv.ID, "acc-checking-a"))
			return err
		},
		"compra registrada": func() error {
			_, err := CreateInvestmentPurchase(ctx, pools.Immediate, guardPurchaseInput("ws-a", inv.ID, ""))
			return err
		},
		"edição": func() error {
			return UpdateInvestment(ctx, pools.Immediate, guardUpdateInput("ws-a", inv.ID))
		},
		"posição": func() error {
			_, err := UpsertInvestmentValuation(ctx, pools.Immediate, UpsertInvestmentValuationInput{
				WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", ValuationDate: "2026-03-01", AmountCents: 60000,
			})
			return err
		},
		"resgate": func() error {
			return RedeemInvestment(ctx, pools.Immediate, RedeemInvestmentInput{
				WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", RedeemedDate: "2026-03-01", FinalAmountCents: 50000,
			})
		},
		"arquivamento": func() error {
			return ArchiveInvestment(ctx, pools.Immediate, ArchiveInvestmentInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a"})
		},
		"retirada": func() error {
			_, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, CreateInvestmentWithdrawalInput{
				WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-a", RequestID: uuid.NewString(),
				WithdrawalDate: "2026-03-01", Mode: InvestmentWithdrawalTotal, ReceivedCents: 50000,
				Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + inv.ID, ReceivedCents: 50000}},
			})
			return err
		},
		"referência": func() error {
			_, err := RecordInvestmentReference(ctx, pools.Immediate, RecordInvestmentReferenceInput{
				WorkspaceID: "ws-a", InvestmentID: inv.ID, AccountID: "acc-inv-a", PositionID: "any-position",
				CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "referência pós-cancelamento",
				CoverageToken: strings.Repeat("ab", 32), ExpectedVersion: 0,
			})
			return err
		},
	}
	for name, attempt := range attempts {
		err := attempt()
		if !errors.Is(err, ErrInvestmentCancelled) {
			t.Fatalf("%s: err=%v want ErrInvestmentCancelled", name, err)
		}
		// The refusal is a conflict for the HTTP layer and stays distinguishable.
		if !errors.Is(err, ErrInvestmentConflict) || err == ErrInvestmentConflict {
			t.Fatalf("%s: error must wrap, not be, ErrInvestmentConflict: %v", name, err)
		}
	}
	if after := cancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("refused mutations changed state:\n%s\n%s", before, after)
	}
}

func TestInvestmentCancellationGuardBlocksAdoptionOfCancelledAccount(t *testing.T) {
	pools := newCancellationDB(t)
	exec(t, pools.Immediate, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-a','user-a','ADMIN'),('ws-b','user-b','ADMIN')`)
	cancelledLotForGuard(t, pools, 50000)
	before := cancellationSnapshot(t, pools.DB)

	_, err := ConfirmInvestmentAdoption(context.Background(), pools.Immediate, ConfirmInvestmentAdoptionInput{
		WorkspaceID: "ws-a", AccountID: "acc-inv-a", CreatedBy: "user-a", RequestID: uuid.NewString(),
		Reason: "declaração sobre conta com aplicação cancelada", CutDate: "2026-03-01",
		ExpectedBalanceVersion: 0, ExpectedAdoptionVersion: 0,
		EvidenceVersion: InvestmentAdoptionEvidenceVersion, DeclarationVersion: InvestmentAdoptionDeclarationVersion,
		EvidenceToken: strings.Repeat("cd", 32), DayClosed: true, Coverage: InvestmentAdoptionPartial,
	})
	if !errors.Is(err, ErrInvestmentCancelled) {
		t.Fatalf("adoption err=%v want ErrInvestmentCancelled", err)
	}
	if after := cancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("refused adoption changed state:\n%s\n%s", before, after)
	}
}

// The schema 79 barriers stay authoritative for raw UPDATE, DELETE, REPLACE
// and UPSERT attempts that bypass every service guard.
func TestInvestmentCancellationGuardSchemaBarriers(t *testing.T) {
	pools := newCancellationDB(t)
	inv := cancelledLotForGuard(t, pools, 50000)
	var original, compensation string
	if err := pools.DB.QueryRow(`SELECT original_transaction_id,compensation_transaction_id FROM investment_cancellations WHERE workspace_id='ws-a' AND investment_id=?`, inv.ID).Scan(&original, &compensation); err != nil {
		t.Fatal(err)
	}
	before := cancellationSnapshot(t, pools.DB)
	originalRow := cancellationScalar(t, pools.DB, `SELECT type||':'||status||':'||effect_mode||':'||amount||':'||description FROM transactions WHERE workspace_id='ws-a' AND id=?`, original)

	statements := map[string]string{
		"update transferência original":      `UPDATE transactions SET description='editado' WHERE workspace_id='ws-a' AND id='` + original + `'`,
		"update transferência compensatória": `UPDATE transactions SET updated_at=updated_at+1 WHERE workspace_id='ws-a' AND id='` + compensation + `'`,
		"delete transferência compensatória": `DELETE FROM transactions WHERE workspace_id='ws-a' AND id='` + compensation + `'`,
		"replace transferência original":     `INSERT OR REPLACE INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status,description,created_at,updated_at) VALUES('` + original + `','ws-a','user-a','acc-checking-a','EXPENSE',1,1,'paid','substituída',1,1)`,
		"update investimento":                `UPDATE investments SET name='editado' WHERE workspace_id='ws-a' AND id='` + inv.ID + `'`,
		"delete investimento":                `DELETE FROM investments WHERE workspace_id='ws-a' AND id='` + inv.ID + `'`,
		"upsert posição":                     `INSERT INTO investment_valuations(id,workspace_id,investment_id,valuation_date,amount_cents,created_by,created_at) VALUES('v-guard','ws-a','` + inv.ID + `','2026-03-01',1,'user-a',1) ON CONFLICT(workspace_id,investment_id,valuation_date) DO UPDATE SET amount_cents=excluded.amount_cents`,
		"update recibo":                      `UPDATE investment_cancellations SET reason='editado' WHERE workspace_id='ws-a' AND investment_id='` + inv.ID + `'`,
		"delete recibo":                      `DELETE FROM investment_cancellations WHERE workspace_id='ws-a' AND investment_id='` + inv.ID + `'`,
		"vínculo em transferência":           `INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by,created_at) VALUES('refund-guard','ws-a','` + compensation + `',1,'reembolso vedado','user-a',1)`,
	}
	for name, statement := range statements {
		_, err := pools.Immediate.Exec(statement)
		// REPLACE resolves its conflict through a delete that the driver may
		// refuse on the receipt's ON DELETE RESTRICT before the trigger text;
		// any refusal still leaves the row untouched (statement atomicity).
		exactMessage := name != "replace transferência original"
		if err == nil || exactMessage && !strings.Contains(err.Error(), "investment cancellation integrity") {
			t.Fatalf("%s: barrier must abort with the schema 79 message, got %v", name, err)
		}
	}
	if after := cancellationSnapshot(t, pools.DB); after != before {
		t.Fatalf("barrier-aborted statements changed state:\n%s\n%s", before, after)
	}
	// A refused REPLACE must leave the protected row byte-identical.
	if after := cancellationScalar(t, pools.DB, `SELECT type||':'||status||':'||effect_mode||':'||amount||':'||description FROM transactions WHERE workspace_id='ws-a' AND id=?`, original); after != originalRow {
		t.Fatalf("original transfer replaced:\n%s\n%s", originalRow, after)
	}
}

func TestInvestmentCancellationGuardPreservesActiveLotsAndIndependentMovements(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	cancelledLotForGuard(t, pools, 50000)

	// A second funded lot on the same investment account and origin account.
	active := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 30000)
	if _, err := UpsertInvestmentValuation(ctx, pools.Immediate, UpsertInvestmentValuationInput{
		WorkspaceID: "ws-a", InvestmentID: active.ID, ActingUserID: "user-a", ValuationDate: "2026-03-02", AmountCents: 31000,
	}); err != nil {
		t.Fatalf("active valuation: %v", err)
	}
	if err := UpdateInvestment(ctx, pools.Immediate, guardUpdateInput("ws-a", active.ID)); err != nil {
		t.Fatalf("active edit: %v", err)
	}
	// The funded hold for active lots is the pre-existing homologation rule,
	// never the cancellation refusal.
	if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, CreateInvestmentWithdrawalInput{
		WorkspaceID: "ws-a", InvestmentID: active.ID, CreatedBy: "user-a", RequestID: uuid.NewString(),
		WithdrawalDate: "2026-03-02", Mode: InvestmentWithdrawalTotal, ReceivedCents: 30000,
		Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + active.ID, ReceivedCents: 30000}},
	}); !errors.Is(err, ErrInvestmentConflict) || errors.Is(err, ErrInvestmentCancelled) {
		t.Fatalf("active withdrawal err=%v want the funded hold, not the cancellation refusal", err)
	}

	// A record-only lot on the same investment account accepts later purchases.
	recordOnly := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, guardPurchaseInput("ws-a", recordOnly.ID, "")); err != nil {
		t.Fatalf("record-only purchase: %v", err)
	}

	// Independent movements on both involved accounts keep working.
	addIndependentOriginMovement(t, pools.Immediate, 7000)
	if balances := cancellationScalar(t, pools.DB, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-checking-a'`); balances == "0" {
		t.Fatal("origin account movements must remain independent")
	}
}

func TestInvestmentCancellationGuardWorkspaceIsolation(t *testing.T) {
	pools := newCancellationDB(t)
	ctx := context.Background()
	inv := cancelledLotForGuard(t, pools, 50000)

	// A foreign workspace never sees the receipt: unknown lot semantics.
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, guardPurchaseInput("ws-b", inv.ID, "")); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign purchase err=%v want ErrInvestmentNotFound", err)
	}
	if err := UpdateInvestment(ctx, pools.Immediate, guardUpdateInput("ws-b", inv.ID)); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign edit err=%v want ErrInvestmentNotFound", err)
	}

	// ws-b owns an active lot that the ws-a receipt must not reach.
	exec(t, pools.Immediate, `INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance) VALUES ('acc-checking-b', 'ws-b', 'Banco B', 'CHECKING', 0, 0)`)
	other := fundedCancellationInvestment(t, pools.Immediate, "ws-b", "acc-inv-b", "acc-checking-b", 100000)
	if _, err := UpsertInvestmentValuation(ctx, pools.Immediate, UpsertInvestmentValuationInput{
		WorkspaceID: "ws-b", InvestmentID: other.ID, ActingUserID: "user-a", ValuationDate: "2026-03-03", AmountCents: 1000,
	}); err != nil {
		t.Fatalf("foreign active valuation: %v", err)
	}
	if leaked := cancellationScalar(t, pools.DB, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='ws-b'`); leaked != "0" {
		t.Fatalf("workspace leak: %s", leaked)
	}
}

// A pre-79 schema only lacks the table: the guards pass and the migration
// itself introduces the protection. The classifier must be exact.
func TestIsMissingInvestmentCancellationsTable(t *testing.T) {
	if !IsMissingInvestmentCancellationsTable(errors.New("SQL logic error: no such table: investment_cancellations (1)")) {
		t.Fatal("schema 78 missing table must classify")
	}
	for _, err := range []error{
		nil,
		errors.New("no such table: transactions"),
		errors.New("constraint failed: FOREIGN KEY constraint failed (1811)"),
		errors.New("investment cancellation integrity"),
	} {
		if IsMissingInvestmentCancellationsTable(err) {
			t.Fatalf("must not classify %v", err)
		}
	}
}

// The B service itself keeps its replay semantics under the guard: replaying
// the confirmed cancellation still returns the receipt, never the refusal.
func TestInvestmentCancellationGuardReplayUnaffected(t *testing.T) {
	pools := newCancellationDB(t)
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=100000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 50000)
	previewIn := cancellationPreviewInput("ws-a", inv.ID, "replay sob guarda")
	preview, err := PreviewInvestmentCancellation(context.Background(), pools.DB, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CancelInvestment(context.Background(), pools.Immediate, cancellationConfirmInput(previewIn, preview))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != InvestmentOperationCreated {
		t.Fatalf("outcome=%s", result.Outcome)
	}
	replayed, err := CancelInvestment(context.Background(), pools.Immediate, cancellationConfirmInput(previewIn, preview))
	if err != nil || replayed.Outcome != InvestmentOperationReplayed || replayed.Cancel.ID != result.Cancel.ID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	var count int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id='ws-a'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("receipts=%d", count)
	}
}
