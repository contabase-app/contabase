package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

func financialHistoryPosition(t *testing.T, db *sql.DB, investment, date string, amount int64, root string, version int64) services.InvestmentIntegrationResult {
	t.Helper()
	ctx := context.Background()
	v, err := services.UpsertInvestmentValuation(ctx, db, services.UpsertInvestmentValuationInput{WorkspaceID: "r03-ws", InvestmentID: investment, ActingUserID: "r03-user", ValuationDate: date, AmountCents: amount})
	if err != nil {
		t.Fatal(err)
	}
	c, err := services.ReadInvestmentReferenceCandidate(ctx, db, "r03-ws", investment, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := services.IntegrateInvestmentPosition(ctx, db, services.IntegrateInvestmentPositionInput{WorkspaceID: "r03-ws", InvestmentID: investment, AccountID: "r03-account", ReferenceRevisionID: root, PositionID: v.ID, CoverageToken: c.CoverageToken, ExpectedVersion: version, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Fechamento", DayClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInvestmentFinancialEconomicHistoryAndTrend(t *testing.T) {
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
	_, err = services.IntegrateInvestmentPosition(ctx, db, services.IntegrateInvestmentPositionInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, AccountID: "r03-account", ReferenceRevisionID: root.ID, PositionID: v.ID, CoverageToken: c.CoverageToken, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Ativar"})
	if err != nil {
		t.Fatal(err)
	}
	first := financialHistoryPosition(t, db, inv.ID, "2026-03-01", 530000, root.ID, 1)
	financialHistoryPosition(t, db, inv.ID, "2026-04-01", 540000, root.ID, 2)
	// Correct the intermediate position through the atomic backend API.
	vals, err := services.GetInvestmentValuations(ctx, db, "r03-ws", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	var position string
	for _, v := range vals {
		if v.ValuationDate == "2026-03-01" {
			position = v.ID
		}
	}
	c, err = services.ReadInvestmentReferenceCandidate(ctx, db, "r03-ws", inv.ID, position)
	if err != nil {
		t.Fatal(err)
	}
	corrected := int64(520000)
	_, err = services.IntegrateInvestmentPosition(ctx, db, services.IntegrateInvestmentPositionInput{WorkspaceID: "r03-ws", InvestmentID: inv.ID, AccountID: "r03-account", ReferenceRevisionID: root.ID, PositionID: position, CoverageToken: c.CoverageToken, ExpectedVersion: 3, CreatedBy: "r03-user", RequestID: uuid.NewString(), Reason: "Correção", CorrectionCents: &corrected, DayClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		date    string
		balance int64
	}{{"2026-02-28T23:59:59Z", 500000}, {"2026-03-01T23:59:58Z", 500000}, {"2026-03-01T23:59:59Z", 520000}, {"2026-03-31T23:59:59Z", 520000}, {"2026-04-01T23:59:59Z", 540000}} {
		at, err := time.Parse(time.RFC3339, tc.date)
		if err != nil {
			t.Fatal(err)
		}
		r03WantBalances(t, db, "r03-ws", at.Unix(), map[string]int64{"r03-account": tc.balance})
	}
	// Registration remains the real write time, not the historical position date.
	var recorded, economic int64
	if err = db.QueryRow(`SELECT h.created_at,f.economic_at FROM account_balance_adjustments h JOIN investment_financial_adjustments f ON f.workspace_id=h.workspace_id AND f.adjustment_id=h.id WHERE h.workspace_id='r03-ws' AND h.id=?`, first.AdjustmentIDs[0]).Scan(&recorded, &economic); err != nil || recorded <= economic {
		t.Fatal("registration/economic date conflated", err)
	}
	at := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	for _, account := range []string{"", "r03-account"} {
		var percent, direction string
		if account == "" {
			percent, direction = queryBalanceTrend(db, "r03-ws", 540000, at)
		} else {
			percent, direction = queryAccountTrend(db, "r03-ws", account, 540000, at)
		}
		if percent != "+3,8%" || direction != "up" {
			t.Fatalf("economic trend: %s %s", percent, direction)
		}
	}
	percent, direction := queryBalanceTrend(db, "r03-ws", 540000, time.Now())
	if percent != "0,0%" || direction != "flat" {
		t.Fatal("backdated correction presented as today's gain", percent, direction)
	}
	var transactions int
	if err = db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id='r03-ws'`).Scan(&transactions); err != nil || transactions != 0 {
		t.Fatal("adjustment became income")
	}
	r03WantBalances(t, db, "foreign", at.Unix(), map[string]int64{})
}
