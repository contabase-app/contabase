package services

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func investmentPurchaseInput(id string) CreateInvestmentPurchaseInput {
	maturity := "2032-01-01"
	return CreateInvestmentPurchaseInput{WorkspaceID: "ws-a", InvestmentID: id, CreatedBy: "user-a", RequestID: uuid.NewString(),
		PurchaseDate: "2026-03-01", PrincipalCents: 200000, NoteReference: "NC-123", Kind: "Tesouro IPCA+", Institution: "Corretora B",
		MaturityDate: &maturity, TaxRegime: models.InvestmentTaxRegimeManual, ConditionsNote: "IPCA + 6%"}
}

func investmentPurchasesFinancialState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var state string
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) || ':' || COALESCE(SUM(amount),0) FROM transactions) || ':' ||
		(SELECT COUNT(*) FROM box_virtual_ledger) || ':' || group_concat(id || ':' || initial_balance || ':' || current_balance)
		FROM (SELECT id,initial_balance,current_balance FROM accounts ORDER BY id)`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestInvestmentAdjustedVariationSnapshotIncomeIsolationAndReadOnly(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, pools.Immediate, "start", "ws-a", inv.ID, "2026-02-01", 100000)
	insertValuationRow(t, pools.Immediate, "end", "ws-a", inv.ID, "2026-04-01", 135000)
	purchase := investmentPurchaseInput(inv.ID)
	purchase.PrincipalCents = 50000
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, purchase); err != nil {
		t.Fatal(err)
	}
	withdrawal := withdrawalInput(inv.ID, "initial:"+inv.ID)
	withdrawal.ReceivedCents, withdrawal.Allocations[0].ReceivedCents = 20000, 20000
	if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, withdrawal); err != nil {
		t.Fatal(err)
	}
	before, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || !before.AdjustedVariation.Available || before.AdjustedVariation.DeltaCents != 5000 || !before.PurchaseStates["initial:"+inv.ID].PrincipalUnknown {
		t.Fatalf("adjusted snapshot: %+v %v", before, err)
	}
	// A manual receipt already reflected in the position is neither added nor
	// subtracted. The reader has no financial-transaction query in its contract.
	exec(t, pools.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,description,status) VALUES('adjusted-income','ws-a','user-a','acc-inv-a','INCOME',5000,1772323200,'Manual income already in position','paid')`)
	foreign := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-b", "acc-inv-b"))
	insertValuationRow(t, pools.Immediate, "foreign-start", "ws-b", foreign.ID, "2026-02-01", 100000)
	insertValuationRow(t, pools.Immediate, "foreign-end", "ws-b", foreign.ID, "2026-04-01", 999000)
	financial := investmentPurchasesFinancialState(t, pools.DB)
	after, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("income/foreign history changed investment detail: %+v %v", after, err)
	}
	if _, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-b", inv.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign investment visible: %v", err)
	}
	if financial != investmentPurchasesFinancialState(t, pools.DB) {
		t.Fatal("read changed balances or ledger")
	}
	// A later operation makes today's position stale, while the dated historical
	// comparison remains valid and must not absorb that later contribution.
	purchase = investmentPurchaseInput(inv.ID)
	purchase.PurchaseDate = "2026-05-01"
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, purchase); err != nil {
		t.Fatal(err)
	}
	stale, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || !stale.PositionStale || stale.AdjustedVariation != before.AdjustedVariation || !reflect.DeepEqual(stale.Valuations, before.Valuations) {
		t.Fatalf("later operation altered historical comparison: %+v %v", stale, err)
	}
}

func TestInvestmentPurchasesIndependentHistoryTotalsAndPositions(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	exec(t, pools.Immediate, `INSERT INTO transactions (id,workspace_id,user_id,account_id,type,amount,date,description,status) VALUES ('purchase-ledger-control','ws-a','user-a','acc-checking-a','EXPENSE',123,1768478400,'Control','paid')`)
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=72345 WHERE id='acc-inv-a' AND workspace_id='ws-a'`)
	insertValuationRow(t, pools.Immediate, "prior-position", "ws-a", inv.ID, "2026-02-01", 110000)
	beforeFinancial := investmentPurchasesFinancialState(t, pools.DB)
	before, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.PurchaseCount != 1 || before.TotalPurchasesCents != inv.PrincipalCents || !before.DifferenceAvailable || before.YieldCents != 10000 {
		t.Fatalf("initial summary: %+v", before)
	}
	input := investmentPurchaseInput(inv.ID)
	row, err := CreateInvestmentPurchase(ctx, pools.Immediate, input)
	if err != nil {
		t.Fatal(err)
	}
	if row.IsInitial || row.PrincipalCents != 200000 || row.NoteReference != "NC-123" || *row.Kind != input.Kind {
		t.Fatalf("purchase=%+v", row)
	}
	after, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Investment, after.Investment) || !reflect.DeepEqual(before.Purchases[0], after.Purchases[0]) || !reflect.DeepEqual(before.Valuations, after.Valuations) {
		t.Fatal("reapplication changed earlier data")
	}
	if after.TotalPurchasesCents != 300000 || after.PurchaseCount != 2 || after.CurrentCents != 110000 || after.PositionDate != "2026-02-01" || !after.PositionStale || after.DifferenceAvailable || after.ReturnAvailable || after.YieldCents != 0 {
		t.Fatalf("stale position compared to future purchase: %+v", after)
	}
	// Backdated second purchase is ordered chronologically, separately from positions.
	input = investmentPurchaseInput(inv.ID)
	input.PurchaseDate = "2026-02-15"
	input.PrincipalCents = 50000
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, input); err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, pools.Immediate, "fresh-position", "ws-a", inv.ID, "2026-03-02", 355000)
	after, err = GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || after.TotalPurchasesCents != 350000 || after.PurchaseCount != 3 || after.PositionStale || !after.DifferenceAvailable || after.YieldCents != 5000 || after.ReturnAvailable {
		t.Fatalf("fresh summary: %+v, %v", after, err)
	}
	if after.Purchases[1].PurchaseDate != "2026-02-15" || after.Purchases[2].PurchaseDate != "2026-03-01" || len(after.Valuations) != 2 {
		t.Fatal("purchase/position histories conflated")
	}
	if beforeFinancial != investmentPurchasesFinancialState(t, pools.DB) {
		t.Fatal("purchases changed ledger or balances")
	}
	for _, ws := range []string{"ws-b", ""} {
		if _, err := GetInvestmentPurchaseDetail(ctx, pools.DB, ws, inv.ID); !errors.Is(err, ErrInvestmentNotFound) {
			t.Fatalf("foreign read: %v", err)
		}
	}
	foreignTotals, err := GetWorkspaceInvestmentPurchaseTotals(ctx, pools.DB, "ws-b")
	if err != nil || len(foreignTotals) != 0 {
		t.Fatalf("foreign totals=%+v err=%v", foreignTotals, err)
	}
}

func TestInvestmentPurchasesValidationReplayAndClosure(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	base := investmentPurchaseInput(inv.ID)
	for _, tc := range []struct {
		name   string
		mutate func(*CreateInvestmentPurchaseInput)
		want   error
	}{
		{"zero", func(p *CreateInvestmentPurchaseInput) { p.PrincipalCents = 0 }, ErrInvestmentValidation},
		{"negative", func(p *CreateInvestmentPurchaseInput) { p.PrincipalCents = -1 }, ErrInvestmentValidation},
		{"overflow", func(p *CreateInvestmentPurchaseInput) { p.PrincipalCents = math.MaxInt64 }, ErrInvestmentValidation},
		{"bad-date", func(p *CreateInvestmentPurchaseInput) { p.PurchaseDate = "2026-02-31" }, ErrInvestmentValidation},
		{"before-initial", func(p *CreateInvestmentPurchaseInput) { p.PurchaseDate = "2025-01-01" }, ErrInvestmentValidation},
		{"maturity", func(p *CreateInvestmentPurchaseInput) { s := "2026-02-01"; p.MaturityDate = &s }, ErrInvestmentValidation},
		{"key", func(p *CreateInvestmentPurchaseInput) { p.RequestID = "" }, ErrInvestmentValidation},
		{"kind", func(p *CreateInvestmentPurchaseInput) { p.Kind = " " }, ErrInvestmentValidation},
		{"tax", func(p *CreateInvestmentPurchaseInput) { p.TaxRegime = "other" }, ErrInvestmentValidation},
		{"reference", func(p *CreateInvestmentPurchaseInput) { p.NoteReference = strings.Repeat("á", 121) }, ErrInvestmentValidation},
		{"foreign", func(p *CreateInvestmentPurchaseInput) { p.WorkspaceID = "ws-b" }, ErrInvestmentNotFound},
		{"missing", func(p *CreateInvestmentPurchaseInput) { p.InvestmentID = "missing" }, ErrInvestmentNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, p); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
	if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_purchases`); got != 1 {
		t.Fatal("rejected writes persisted")
	}
	row, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, base)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, base)
	if err != nil || !reflect.DeepEqual(row, replayed) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := base
	changed.PrincipalCents++
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, changed); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("changed retry=%v", err)
	}
	if err := RedeemInvestment(context.Background(), pools.Immediate, investmentRedeemInput(inv.ID)); !errors.Is(err, ErrInvestmentValidation) {
		t.Fatalf("closed before purchase: %v", err)
	}
	closeInput := investmentRedeemInput(inv.ID)
	closeInput.RedeemedDate = "2026-04-01"
	if err := RedeemInvestment(context.Background(), pools.Immediate, closeInput); err != nil {
		t.Fatal(err)
	}
	base.RequestID = uuid.NewString()
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, base); !errors.Is(err, ErrInvestmentInvalidStatus) {
		t.Fatalf("closed=%v", err)
	}
	if err := ArchiveInvestment(context.Background(), pools.Immediate, ArchiveInvestmentInput{"ws-a", inv.ID, "user-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, base); !errors.Is(err, ErrInvestmentInvalidStatus) {
		t.Fatalf("archived=%v", err)
	}
	detail, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
	if err != nil || detail.PurchaseCount != 2 || len(detail.Valuations) != 1 || detail.Investment.ArchivedAt == nil {
		t.Fatalf("closed compatibility: %+v %v", detail, err)
	}
}

func TestInvestmentPurchasesRollbackAndAtomicInitial(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	before := investmentPurchasesFinancialState(t, pools.DB)
	exec(t, pools.Immediate, `CREATE TRIGGER purchase_failure BEFORE INSERT ON investment_purchases BEGIN SELECT RAISE(ABORT,'forced failure'); END`)
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, investmentPurchaseInput(inv.ID)); err == nil {
		t.Fatal("forced failure admitted")
	}
	if _, err := CreateInvestment(context.Background(), pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a")); err == nil {
		t.Fatal("initial purchase failure did not rollback investment")
	}
	if countRows(t, pools.DB, `SELECT COUNT(*) FROM investments`) != 1 || countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_purchases`) != 1 || before != investmentPurchasesFinancialState(t, pools.DB) {
		t.Fatal("rollback changed existing state")
	}
}

func TestInvestmentPurchasesConcurrentRetryImmediate(t *testing.T) {
	pools, path, inv := newInvestmentLifecycleDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstAtCommit, secondAtBegin, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	dsn := path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	first := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeCommit: func() {
		close(firstAtCommit)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}})
	defer first.Close()
	second := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeBegin: func() { close(secondAtBegin) }})
	defer second.Close()
	in := investmentPurchaseInput(inv.ID)
	type result struct {
		p   models.InvestmentPurchase
		err error
	}
	a, b := make(chan result, 1), make(chan result, 1)
	go func() { p, e := CreateInvestmentPurchase(ctx, first, in); a <- result{p, e} }()
	select {
	case <-firstAtCommit:
	case <-ctx.Done():
		t.Fatal("first did not reach commit")
	}
	go func() { p, e := CreateInvestmentPurchase(ctx, second, in); b <- result{p, e} }()
	select {
	case <-secondAtBegin:
	case <-ctx.Done():
		t.Fatal("second did not reach begin")
	}
	close(release)
	ra, rb := <-a, <-b
	if ra.err != nil || rb.err != nil || !reflect.DeepEqual(ra.p, rb.p) {
		t.Fatalf("concurrent replay: %+v %+v", ra, rb)
	}
	if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_purchases`) != 2 {
		t.Fatal("concurrent request duplicated purchase")
	}
}

func TestInvestmentPurchasesConcurrentDistinctAndOverflow(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct", true: "overflow"}[overflow], func(t *testing.T) {
			pools, _, inv := newInvestmentLifecycleDB(t)
			a, b := investmentPurchaseInput(inv.ID), investmentPurchaseInput(inv.ID)
			if overflow {
				a.PrincipalCents = math.MaxInt64 - inv.PrincipalCents - 5
				b.PrincipalCents = 10
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, input := range []CreateInvestmentPurchaseInput{a, b} {
				go func(in CreateInvestmentPurchaseInput) {
					<-start
					_, e := CreateInvestmentPurchase(context.Background(), pools.Immediate, in)
					results <- e
				}(input)
			}
			close(start)
			success := 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					success++
				} else if !overflow || !errors.Is(err, ErrInvestmentValidation) {
					t.Fatal(err)
				}
			}
			want := 2
			if overflow {
				want = 1
			}
			if success != want {
				t.Fatalf("success=%d want=%d", success, want)
			}
			d, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
			if err != nil || d.PurchaseCount != 1+want || d.TotalPurchasesCents <= 0 {
				t.Fatalf("total=%+v %v", d, err)
			}
			if !overflow && d.TotalPurchasesCents != 500000 {
				t.Fatal("distinct purchases lost")
			}
		})
	}
}
