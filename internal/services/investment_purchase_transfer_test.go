package services

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func fundedInput() CreateInvestmentInput {
	in := investmentCreateInput("ws-a", "acc-inv-a")
	in.RequestID = uuid.NewString()
	in.SourceAccountID = "acc-checking-a"
	return in
}

func TestInvestmentBeta6FundedSchema77AtomicReplayAndHistory(t *testing.T) {
	p, _, _ := newInvestmentLifecycleDB(t)
	exec(t, p.Immediate, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	in := fundedInput()
	in.PrincipalCents = 500000
	inv, err := CreateInvestment(t.Context(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	before := adoptionSnapshot(t, p.DB)
	replay, err := CreateInvestment(t.Context(), p.Immediate, in)
	if err != nil || replay.ID != inv.ID {
		t.Fatalf("create replay %+v %v", replay, err)
	}
	if before != adoptionSnapshot(t, p.DB) {
		t.Fatal("create replay changed journal")
	}
	purchase := investmentPurchaseInput(inv.ID)
	purchase.SourceAccountID = in.SourceAccountID
	// Same-day applications are ordinary transfers, not fabricated end-of-day positions.
	purchase.PurchaseDate = in.AppliedDate
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := CreateInvestmentPurchase(t.Context(), p.Immediate, purchase)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	detail, err := GetInvestmentPurchaseDetail(t.Context(), p.DB, "ws-a", inv.ID)
	if err != nil || detail.TotalPurchasesCents != 700000 || detail.PurchaseCount != 2 || len(detail.Valuations) != 0 {
		t.Fatalf("detail %+v %v", detail, err)
	}
	for account, want := range map[string]int64{"acc-checking-a": 300000, "acc-inv-a": 700000, "acc-inv-b": 0} {
		workspace := "ws-a"
		if account == "acc-inv-b" {
			workspace = "ws-b"
		}
		var got int64
		if err := p.DB.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?`, workspace, account).Scan(&got); err != nil || got != want {
			t.Fatalf("balance %s=%d want=%d err=%v", account, got, want, err)
		}
	}
	var transfers, sum, version, experimental int64
	if err := p.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(amount),0) FROM transactions WHERE workspace_id='ws-a' AND type='TRANSFER'`).Scan(&transfers, &sum); err != nil || transfers != 2 || sum != 700000 {
		t.Fatalf("transfers=%d sum=%d %v", transfers, sum, err)
	}
	if err := p.DB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 77 {
		t.Fatalf("schema=%d %v", version, err)
	}
	if err := p.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='investment_adoption_admissions'`).Scan(&experimental); err != nil || experimental != 0 {
		t.Fatal("78 unexpectedly active", err)
	}
	before = adoptionSnapshot(t, p.DB)
	changed := purchase
	changed.SourceAccountID = "acc-inv-b"
	if _, err := CreateInvestmentPurchase(t.Context(), p.Immediate, changed); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("changed source replay", err)
	}
	changed = purchase
	changed.SourceAccountID = ""
	if _, err := CreateInvestmentPurchase(t.Context(), p.Immediate, changed); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("record-only replay", err)
	}
	if before != adoptionSnapshot(t, p.DB) {
		t.Fatal("conflicting replay changed data")
	}
}

func TestInvestmentBeta6FundedRollbackIsolationAndPolicy(t *testing.T) {
	for _, fault := range []string{"foreign source", "archived source", "same account", "credit source", "missing source", "destination overflow", "destination write", "purchase transfer write"} {
		t.Run(fault, func(t *testing.T) {
			p, _, _ := newInvestmentLifecycleDB(t)
			in := fundedInput()
			switch fault {
			case "foreign source":
				in.SourceAccountID = "acc-inv-b"
			case "archived source":
				exec(t, p.Immediate, `UPDATE accounts SET archived_at=1 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
			case "same account":
				in.SourceAccountID = in.AccountID
			case "credit source":
				exec(t, p.Immediate, `UPDATE accounts SET type='CREDIT_CARD' WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
			case "missing source":
				in.SourceAccountID = "absent"
			case "destination overflow":
				exec(t, p.Immediate, `UPDATE accounts SET current_balance=? WHERE workspace_id='ws-a' AND id='acc-inv-a'`, int64(math.MaxInt64))
			case "destination write":
				exec(t, p.Immediate, `CREATE TRIGGER beta6_fail_destination BEFORE UPDATE ON accounts WHEN NEW.workspace_id='ws-a' AND NEW.id='acc-inv-a' BEGIN SELECT RAISE(ABORT,'injected destination failure'); END`)
			}
			if fault == "purchase transfer write" {
				inv, err := CreateInvestment(t.Context(), p.Immediate, in)
				if err != nil {
					t.Fatal(err)
				}
				exec(t, p.Immediate, `CREATE TRIGGER beta6_fail_transfer BEFORE INSERT ON transactions WHEN NEW.workspace_id='ws-a' BEGIN SELECT RAISE(ABORT,'injected transfer failure'); END`)
				before := adoptionSnapshot(t, p.DB)
				purchase := investmentPurchaseInput(inv.ID)
				purchase.SourceAccountID = in.SourceAccountID
				if _, err := CreateInvestmentPurchase(t.Context(), p.Immediate, purchase); err == nil {
					t.Fatal("failure accepted")
				}
				if before != adoptionSnapshot(t, p.DB) {
					t.Fatal("partial purchase")
				}
				return
			}
			before := adoptionSnapshot(t, p.DB)
			if _, err := CreateInvestment(t.Context(), p.Immediate, in); err == nil {
				t.Fatal("invalid application accepted")
			}
			if before != adoptionSnapshot(t, p.DB) {
				t.Fatal("partial application/transfer/balance")
			}
		})
	}
	// Ordinary transfers permit negative cash balances. Preserve that policy;
	// int64 overflow and account ownership remain mandatory.
	p, _, legacy := newInvestmentLifecycleDB(t)
	in := fundedInput()
	inv, err := CreateInvestment(t.Context(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	var cash int64
	if err := p.DB.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-checking-a'`).Scan(&cash); err != nil || cash != -90000 {
		t.Fatalf("existing overdraft policy %d %v", cash, err)
	}
	before := adoptionSnapshot(t, p.DB)
	historical := investmentPurchaseInput(legacy.ID)
	historical.SourceAccountID = in.SourceAccountID
	if _, err := CreateInvestmentPurchase(t.Context(), p.Immediate, historical); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("historical adoption was accepted", err)
	}
	if err := RedeemInvestment(t.Context(), p.Immediate, investmentRedeemInput(inv.ID)); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("unhomologated closure", err)
	}
	w := CreateInvestmentWithdrawalInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-a", RequestID: uuid.NewString(), WithdrawalDate: "2026-02-01", Mode: InvestmentWithdrawalPartial, ReceivedCents: 100, Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + inv.ID, ReceivedCents: 100}}}
	if _, err := CreateInvestmentWithdrawal(t.Context(), p.Immediate, w); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("unhomologated withdrawal", err)
	}
	if before != adoptionSnapshot(t, p.DB) {
		t.Fatal("blocked operations changed journal")
	}
}
