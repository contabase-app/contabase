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

func withdrawalInput(id string, purchaseIDs ...string) CreateInvestmentWithdrawalInput {
	in := CreateInvestmentWithdrawalInput{WorkspaceID: "ws-a", InvestmentID: id, CreatedBy: "user-a", RequestID: uuid.NewString(), WithdrawalDate: "2026-03-15", Mode: InvestmentWithdrawalPartial, ReceivedCents: int64(len(purchaseIDs)) * 10000}
	for _, id := range purchaseIDs {
		in.Allocations = append(in.Allocations, models.InvestmentWithdrawalAllocation{PurchaseID: id, ReceivedCents: 10000})
	}
	return in
}
func withdrawalPrincipal(cents int64) *int64 { return &cents }

func TestInvestmentWithdrawalsHistoryPartialTotalAndReopening(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, "2026-02-01", 110000)
	exec(t, pools.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,description,status) VALUES('withdraw-control','ws-a','user-a','acc-checking-a','EXPENSE',123,1768478400,'Control','paid')`)
	purchase, err := CreateInvestmentPurchase(ctx, pools.Immediate, investmentPurchaseInput(inv.ID))
	if err != nil {
		t.Fatal(err)
	}
	before, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	financial := investmentPurchasesFinancialState(t, pools.DB)
	initial := "initial:" + inv.ID
	in := withdrawalInput(inv.ID, initial)
	// Receipt can exceed original principal, with no invented split.
	in.ReceivedCents, in.Allocations[0].ReceivedCents = 120000, 120000
	in.Allocations[0].PrincipalCents = withdrawalPrincipal(20000)
	if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	d, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || d.PurchaseStates[initial].PrincipalUnknown || d.PurchaseStates[initial].KnownPrincipalWithdrawn != 20000 || d.MaintainedPurchaseCount != 2 {
		t.Fatalf("known principal: %+v %v", d, err)
	}
	in = withdrawalInput(inv.ID, initial, purchase.ID)
	if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	d, err = GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || d.TotalWithdrawnCents != 140000 || !d.PurchaseStates[initial].PrincipalUnknown || d.MaintainedPurchaseCount != 2 || !d.PositionStale || d.CurrentCents != 110000 || d.DifferenceAvailable {
		t.Fatalf("partial %+v %v", d, err)
	}
	if !reflect.DeepEqual(before.Investment, d.Investment) || !reflect.DeepEqual(before.Purchases, d.Purchases) || !reflect.DeepEqual(before.Valuations, d.Valuations) {
		t.Fatal("withdrawal rewrote history")
	}
	totals, err := GetWorkspaceInvestmentPurchaseTotals(ctx, pools.DB, "ws-a")
	if err != nil || totals[inv.ID].LatestDate != "2026-03-15" {
		t.Fatalf("list stale marker: %+v %v", totals, err)
	}
	in = withdrawalInput(inv.ID, initial, purchase.ID)
	in.Mode = InvestmentWithdrawalTotal
	in.WithdrawalDate = "2026-03-16"
	// A known principal may exceed the receipt (a realized loss).
	in.Allocations[1].PrincipalCents = withdrawalPrincipal(150000)
	if _, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	d, err = GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || d.MaintainedPurchaseCount != 0 || d.Investment.Status != "ATIVA" || len(d.Valuations) != 1 || !d.PurchaseStates[initial].Closed || !d.PurchaseStates[purchase.ID].Closed {
		t.Fatalf("total %+v %v", d, err)
	}
	if !reflect.DeepEqual(before.Investment, d.Investment) || !reflect.DeepEqual(before.Valuations, d.Valuations) || financial != investmentPurchasesFinancialState(t, pools.DB) {
		t.Fatal("total mutated original history or finances")
	}
	fresh := investmentPurchaseInput(inv.ID)
	fresh.PurchaseDate = "2026-03-17"
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, fresh); err != nil {
		t.Fatal(err)
	}
	d, err = GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || d.MaintainedPurchaseCount != 1 || len(d.Withdrawals) != 3 || d.PurchaseCount != 3 {
		t.Fatalf("reopen %+v %v", d, err)
	}
	insertValuationRow(t, pools.Immediate, "fresh", "ws-a", inv.ID, "2026-03-18", 201000)
	d, err = GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || d.PositionStale || d.DifferenceAvailable || d.CurrentCents != 201000 {
		t.Fatalf("fresh manual position %+v %v", d, err)
	}
}

func TestInvestmentWithdrawalsValidationIsolationAndPrincipal(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	initial := "initial:" + inv.ID
	other := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	foreign := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-b", "acc-inv-b"))
	for _, tc := range []struct {
		name   string
		mutate func(*CreateInvestmentWithdrawalInput)
		want   error
	}{
		{"sum", func(in *CreateInvestmentWithdrawalInput) { in.ReceivedCents++ }, ErrInvestmentValidation},
		{"foreign-workspace", func(in *CreateInvestmentWithdrawalInput) { in.WorkspaceID = "ws-b" }, ErrInvestmentNotFound},
		{"foreign-purchase", func(in *CreateInvestmentWithdrawalInput) { in.Allocations[0].PurchaseID = "initial:" + foreign.ID }, ErrInvestmentNotFound},
		{"other-investment", func(in *CreateInvestmentWithdrawalInput) { in.Allocations[0].PurchaseID = "initial:" + other.ID }, ErrInvestmentNotFound},
		{"bad-date", func(in *CreateInvestmentWithdrawalInput) { in.WithdrawalDate = "2026-02-31" }, ErrInvestmentValidation},
		{"before-purchase", func(in *CreateInvestmentWithdrawalInput) { in.WithdrawalDate = "2026-01-14" }, ErrInvestmentValidation},
		{"request", func(in *CreateInvestmentWithdrawalInput) { in.RequestID = "" }, ErrInvestmentValidation},
		{"mode", func(in *CreateInvestmentWithdrawalInput) { in.Mode = "OTHER" }, ErrInvestmentValidation},
		{"empty", func(in *CreateInvestmentWithdrawalInput) { in.Allocations = nil }, ErrInvestmentValidation},
		{"duplicate-purchase", func(in *CreateInvestmentWithdrawalInput) { in.Allocations = append(in.Allocations, in.Allocations[0]) }, ErrInvestmentValidation},
		{"negative", func(in *CreateInvestmentWithdrawalInput) { in.Allocations[0].ReceivedCents = -1 }, ErrInvestmentValidation},
		{"principal-negative", func(in *CreateInvestmentWithdrawalInput) { in.Allocations[0].PrincipalCents = withdrawalPrincipal(-1) }, ErrInvestmentValidation},
		{"principal-over", func(in *CreateInvestmentWithdrawalInput) {
			in.Allocations[0].PrincipalCents = withdrawalPrincipal(100001)
		}, ErrInvestmentValidation},
		{"overflow", func(in *CreateInvestmentWithdrawalInput) {
			in.Allocations = append(in.Allocations, models.InvestmentWithdrawalAllocation{PurchaseID: "other", ReceivedCents: math.MaxInt64})
		}, ErrInvestmentValidation},
		{"note", func(in *CreateInvestmentWithdrawalInput) { in.Note = strings.Repeat("á", 2001) }, ErrInvestmentValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := withdrawalInput(inv.ID, initial)
			tc.mutate(&in)
			if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); !errors.Is(err, tc.want) {
				t.Fatalf("%v want %v", err, tc.want)
			}
		})
	}
	if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_withdrawals`) != 0 {
		t.Fatal("rejection persisted")
	}
	in := withdrawalInput(inv.ID, initial)
	in.Allocations[0].PrincipalCents = withdrawalPrincipal(60000)
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	in.RequestID = uuid.NewString()
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); !errors.Is(err, ErrInvestmentValidation) {
		t.Fatalf("cumulative principal %v", err)
	}
	in.Allocations[0].PrincipalCents = nil
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	in.RequestID = uuid.NewString()
	in.Allocations[0].PrincipalCents = withdrawalPrincipal(40000)
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	d, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
	if err != nil || !d.PurchaseStates[initial].PrincipalUnknown {
		t.Fatalf("unknown was estimated: %+v %v", d, err)
	}
	in.RequestID = uuid.NewString()
	in.Mode = InvestmentWithdrawalTotal
	in.WithdrawalDate = "2026-03-14"
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); !errors.Is(err, ErrInvestmentValidation) {
		t.Fatalf("total predating partial %v", err)
	}
	in.WithdrawalDate = "2026-03-16"
	in.ReceivedCents = 0
	in.Allocations[0].ReceivedCents = 0
	in.Allocations[0].PrincipalCents = nil
	row, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in)
	if err != nil || !reflect.DeepEqual(row, replay) {
		t.Fatalf("replay %+v %v", replay, err)
	}
	in.Note = "changed"
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("changed replay %v", err)
	}
	in.RequestID = uuid.NewString()
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("closed purchase %v", err)
	}
	closeIn := investmentRedeemInput(other.ID)
	closeIn.RedeemedDate = "2026-04-01"
	if err := RedeemInvestment(context.Background(), pools.Immediate, closeIn); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, withdrawalInput(other.ID, "initial:"+other.ID)); !errors.Is(err, ErrInvestmentInvalidStatus) {
		t.Fatalf("admin closed %v", err)
	}
}

func TestInvestmentWithdrawalsRollbackSecondAllocation(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	purchase, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, investmentPurchaseInput(inv.ID))
	if err != nil {
		t.Fatal(err)
	}
	before, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	financial := investmentPurchasesFinancialState(t, pools.DB)
	exec(t, pools.Immediate, `CREATE TRIGGER fail_withdrawal_second BEFORE INSERT ON investment_withdrawal_allocations WHEN EXISTS(SELECT 1 FROM investment_withdrawal_allocations WHERE withdrawal_id=NEW.withdrawal_id) BEGIN SELECT RAISE(ABORT,'forced second allocation'); END`)
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, withdrawalInput(inv.ID, "initial:"+inv.ID, purchase.ID)); err == nil {
		t.Fatal("forced failure admitted")
	}
	after, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
	if err != nil || !reflect.DeepEqual(before, after) || financial != investmentPurchasesFinancialState(t, pools.DB) || countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_withdrawal_allocations`) != 0 {
		t.Fatalf("rollback changed state %+v %v", after, err)
	}
}

func TestInvestmentWithdrawalsImmediateConcurrency(t *testing.T) {
	for _, kind := range []string{"retry", "principal", "total"} {
		t.Run(kind, func(t *testing.T) {
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
			in := withdrawalInput(inv.ID, "initial:"+inv.ID)
			if kind == "principal" {
				in.Allocations[0].PrincipalCents = withdrawalPrincipal(60000)
			}
			if kind == "total" {
				in.Mode = InvestmentWithdrawalTotal
			}
			other := in
			if kind != "retry" {
				other.RequestID = uuid.NewString()
			}
			type result struct {
				w   models.InvestmentWithdrawal
				err error
			}
			a, b := make(chan result, 1), make(chan result, 1)
			go func() { w, e := CreateInvestmentWithdrawal(ctx, first, in); a <- result{w, e} }()
			select {
			case <-firstAtCommit:
			case <-ctx.Done():
				t.Fatal("first did not reach commit")
			}
			go func() { w, e := CreateInvestmentWithdrawal(ctx, second, other); b <- result{w, e} }()
			select {
			case <-secondAtBegin:
			case <-ctx.Done():
				t.Fatal("second did not reach begin")
			}
			close(release)
			ra, rb := <-a, <-b
			if ra.err != nil {
				t.Fatal(ra.err)
			}
			if kind == "retry" {
				if rb.err != nil || !reflect.DeepEqual(ra.w, rb.w) {
					t.Fatalf("retry %+v %+v", ra, rb)
				}
			} else {
				want := ErrInvestmentValidation
				if kind == "total" {
					want = ErrInvestmentConflict
				}
				if !errors.Is(rb.err, want) {
					t.Fatalf("second %v want %v", rb.err, want)
				}
			}
			if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_withdrawals`) != 1 || countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_withdrawal_allocations`) != 1 {
				t.Fatal("concurrent duplicate or partial event")
			}
		})
	}
}
