package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

func TestInvestmentSettlementFinancialEconomicHistoryAndTrend(t *testing.T) {
	db := r03Scenario(t, "business", 500000)
	ctx := context.Background()
	execTestSQL(t, db, `UPDATE accounts SET type='INVESTMENT' WHERE workspace_id='r03-ws' AND id='r03-account'`)
	inv, err := services.CreateInvestment(ctx, db, services.CreateInvestmentInput{WorkspaceID: "r03-ws", AccountID: "r03-account", CreatedBy: "r03-user", Name: "CDB", Institution: "Banco", Kind: "CDB", PrincipalCents: 500000, TaxRegime: "MANUAL", AppliedDate: "2026-01-15"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := services.UpsertInvestmentValuation(ctx, db, services.UpsertInvestmentValuationInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, ActingUserID: "r03-user", ValuationDate: "2026-02-01", AmountCents: 500000})
	if err != nil {
		t.Fatal(err)
	}
	c, err := services.ReadInvestmentReferenceCandidate(ctx, db, "r03-ws", inv.ID, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	represented := int64(500000)
	root, err := services.RecordInvestmentReference(ctx, db, services.RecordInvestmentReferenceInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, AccountID: "r03-account", PositionID: v.ID, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Referência", CoverageToken: c.CoverageToken, RepresentedCents: &represented, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = services.IntegrateInvestmentPosition(ctx, db, services.IntegrateInvestmentPositionInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, AccountID: "r03-account", ReferenceRevisionID: root.ID, PositionID: v.ID, CoverageToken: c.CoverageToken, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Ativação"})
	if err != nil {
		t.Fatal(err)
	}
	b := financialHistoryPosition(t, db, inv.ID, "2026-03-01", 530000, root.ID, 1)
	w, err := services.CreateInvestmentWithdrawal(ctx, db, services.CreateInvestmentWithdrawalInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, CreatedBy: "r03-user", RequestID: uuid.NewString(), WithdrawalDate: "2026-03-15", Mode: services.InvestmentWithdrawalTotal, ReceivedCents: 525500, Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + inv.ID, ReceivedCents: 525500, PrincipalCents: &represented}}})
	if err != nil {
		t.Fatal(err)
	}
	gross, ir, zero := int64(530000), int64(4500), int64(0)
	s, err := services.RecordInvestmentSettlement(ctx, db, services.RecordInvestmentSettlementInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, WithdrawalID: w.ID, AccountID: "r03-account", DestinationAccountID: "r03-account", EconomicDate: w.WithdrawalDate, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Comprovante", GrossCents: &gross, IRCents: &ir, IOFCents: &zero, FeesCents: &zero, NetCents: 525500, PatrimonyCents: &gross, PatrimonyEvidence: "Extrato", ReceiptEvidence: "Caixa confirmado", ConfirmReceipt: true})
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err = db.QueryRow(`SELECT balance_version FROM accounts WHERE workspace_id='r03-ws' AND id='r03-account'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	r, err := services.ApplyInvestmentSettlement(ctx, db, services.ApplyInvestmentSettlementInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, WithdrawalID: w.ID, SettlementRevisionID: s.ID, ReferenceRevisionID: root.ID, FinancialRevisionID: b.ID, ExpectedSettlementVersion: 1, ExpectedFinancialVersion: 2, ExpectedBalanceVersion: version, ExpectedDestinationVersion: version, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Reconciliação", ReconciliationEvidence: "Recebimento mantido; nenhum imposto previamente debitado"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		date    string
		balance int64
	}{
		{"2026-02-28T23:59:59Z", 500000}, {"2026-03-01T23:59:59Z", 530000},
		{"2026-03-15T23:59:58Z", 530000}, {"2026-03-15T23:59:59Z", 525500}, {"2026-04-01T00:00:00Z", 525500},
	} {
		at, err := time.Parse(time.RFC3339, tc.date)
		if err != nil {
			t.Fatal(err)
		}
		r03WantBalances(t, db, "r03-ws", at.Unix(), map[string]int64{"r03-account": tc.balance})
	}
	at := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	for _, account := range []string{"", "r03-account"} {
		percent, direction, found := investmentAdjustmentTrend(db, "r03-ws", account, 525500, 0, at.AddDate(0, 0, -30).Unix(), at.Unix())
		if !found || percent != "+5,1%" || direction != "up" {
			t.Fatalf("trend %s %s %v", percent, direction, found)
		}
	}
	var economic, recorded int64
	if err = db.QueryRow(`SELECT a.economic_at,h.created_at FROM investment_settlement_applications a JOIN account_balance_adjustments h ON h.workspace_id=a.workspace_id AND h.id=a.adjustment_id WHERE a.workspace_id='r03-ws' AND a.id=?`, r.ID).Scan(&economic, &recorded); err != nil || recorded <= economic {
		t.Fatal("lost registration time", err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='r03-ws'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("created operational cash flow", err)
	}
	if percent, direction := queryBalanceTrend(db, "r03-ws", 525500, time.Now()); percent != "0,0%" || direction != "flat" {
		t.Fatal("backdated adjustment in current period", percent, direction)
	}
	r03WantBalances(t, db, "foreign", at.Unix(), map[string]int64{})
}
