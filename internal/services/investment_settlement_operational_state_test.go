package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestInvestmentSettlementOperationalStateAppliedRemainsDistinctAndSealed(t *testing.T) {
	// Build a pre-existing applied settlement through the established C2 fixture;
	// the operational read and record/revise flow must never apply it again.
	p, inv, settlement, input := c2aFixture(t, 525500, 530000, false, false)
	application := c2aApply(t, p, input)
	before := settlementFinancialSnapshot(t, p.DB)
	s, err := ReadInvestmentSettlementOperationalState(context.Background(), p.DB, "ws-a", inv.ID, settlement.WithdrawalID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != InvestmentSettlementReconciled || s.ApplicationID != application.ID || s.ClassificationID != "" || s.Settlement.EffectsState != "NOT_ACTIVATED" {
		t.Fatalf("C1/C2 facts conflated: %+v", s)
	}
	revision := settlement.Input
	revision.RequestID, revision.ExpectedVersion = uuid.NewString(), settlement.Version
	if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, revision); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("applied settlement revision: %v", err)
	}
	if before != settlementFinancialSnapshot(t, p.DB) {
		t.Fatal("read/rejected revision moved finances")
	}
}
