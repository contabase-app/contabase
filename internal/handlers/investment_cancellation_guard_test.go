package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// Microetapa B7-I2-C, transaction side: generic mutations (edit, delete,
// payment toggle, invoice move, bulk, refunds) that reach the original or the
// compensatory transfer of a confirmed cancellation must be refused by the
// shared guard with a typed 409 before any balance reversal.

func TestCancellationMutationGuardScoping(t *testing.T) {
	db := r03Scenario(t, "personal", 1000000)
	ctx := context.Background()
	execTestSQL(t, db, `INSERT INTO accounts(id,workspace_id,name,type,initial_balance,current_balance) VALUES('r03-inv','r03-ws','Corretora R03','INVESTMENT',0,0)`)
	inv, err := services.CreateInvestment(ctx, db, services.CreateInvestmentInput{
		WorkspaceID: "r03-ws", AccountID: "r03-inv", SourceAccountID: "r03-account", CreatedBy: "r03-user",
		RequestID: uuid.NewString(), Name: "CDB", Institution: "Banco", Kind: "CDB",
		PrincipalCents: 200000, TaxRegime: "MANUAL", AppliedDate: "2026-01-15",
	})
	if err != nil {
		t.Fatal(err)
	}
	previewIn := services.InvestmentCancellationPreviewInput{
		WorkspaceID: "r03-ws", InvestmentID: inv.ID, ActingUserID: "r03-user",
		RequestID: uuid.NewString(), Reason: "proteção de transferências", Location: time.UTC,
	}
	preview, err := services.PreviewInvestmentCancellation(ctx, db, previewIn)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Eligible {
		t.Fatalf("preview blockers: %+v", preview.Blockers)
	}
	result, err := services.CancelInvestment(ctx, db, services.CancelInvestmentInput{
		WorkspaceID: previewIn.WorkspaceID, InvestmentID: previewIn.InvestmentID, CreatedBy: previewIn.ActingUserID,
		RequestID: previewIn.RequestID, Reason: previewIn.Reason, PreviewToken: preview.PreviewToken,
		ExpectedSourceVersion: preview.OriginBalanceVersion, ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	original := result.Cancel.OriginalTransactionID
	compensation := result.Cancel.CompensationTransactionID

	for _, id := range []string{original, compensation} {
		if err := cancellationMutationGuard(db, "r03-ws", []string{id}); !errors.Is(err, errInvestmentCancellationProtected) {
			t.Fatalf("protected id %s: err=%v", id, err)
		}
	}
	// The refusal rides the classification sentinel: every existing 409
	// mapping site answers it without new wiring.
	if err := cancellationMutationGuard(db, "r03-ws", []string{compensation}); !errors.Is(err, errInvestmentClassificationProtected) {
		t.Fatalf("guard must wrap the classification sentinel: %v", err)
	}
	// Foreign workspace and unrelated ids pass: the guard adds no false positives.
	if err := cancellationMutationGuard(db, "r03-other", []string{original, compensation}); err != nil {
		t.Fatalf("foreign workspace must not match: %v", err)
	}
	if err := cancellationMutationGuard(db, "r03-ws", []string{"unrelated-transaction"}); err != nil {
		t.Fatalf("unrelated id must not match: %v", err)
	}
	if err := cancellationMutationGuard(db, "r03-ws", nil); err != nil {
		t.Fatalf("empty ids must be a no-op: %v", err)
	}

	// The combined gate keeps both families of protected launches.
	if err := protectedTransactionMutationGuard(db, "r03-ws", []string{compensation}); !errors.Is(err, errInvestmentCancellationProtected) {
		t.Fatalf("combined gate: %v", err)
	}

	// The early HTTP guard answers 409 with the specific message.
	h := TransactionHandler{DB: db, WorkspaceID: "r03-ws"}
	rec := httptest.NewRecorder()
	if !h.rejectClassificationMutation(rec, []string{compensation}) {
		t.Fatal("early guard must refuse")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "cancelamento confirmado protege este lançamento") {
		t.Fatalf("body=%q", body)
	}

	// The writer-transaction shape (ensureTransactionsNotInvoicePaymentsTx path).
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := cancellationMutationGuard(tx, "r03-ws", []string{original, "unrelated-transaction"}); !errors.Is(err, errInvestmentCancellationProtected) {
		t.Fatalf("tx guard: %v", err)
	}
}
