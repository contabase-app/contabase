package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func settlementInput(w models.InvestmentWithdrawal) RecordInvestmentSettlementInput {
	return RecordInvestmentSettlementInput{WorkspaceID: w.WorkspaceID, InvestmentID: w.InvestmentID, WithdrawalID: w.ID, AccountID: "acc-inv-a", DestinationAccountID: "acc-inv-a", EconomicDate: w.WithdrawalDate, CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Extrato de liquidação", GrossCents: withdrawalPrincipal(w.ReceivedCents), IRCents: withdrawalPrincipal(0), IOFCents: withdrawalPrincipal(0), FeesCents: withdrawalPrincipal(0), NetCents: w.ReceivedCents, PatrimonyCents: withdrawalPrincipal(w.ReceivedCents), PatrimonyEvidence: "Extrato com parcela patrimonial baixada"}
}
func settlementWithdrawalFixture(t *testing.T, p *database.RuntimeDBs, inv models.Investment, net int64, principal *int64, mode string) models.InvestmentWithdrawal {
	t.Helper()
	in := withdrawalInput(inv.ID, "initial:"+inv.ID)
	in.ReceivedCents = net
	in.Allocations[0].ReceivedCents = net
	in.Allocations[0].PrincipalCents = principal
	in.Mode = mode
	w, err := CreateInvestmentWithdrawal(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func settlementMust(t *testing.T, p *database.RuntimeDBs, in RecordInvestmentSettlementInput) InvestmentSettlement {
	t.Helper()
	r, err := RecordInvestmentSettlement(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func settlementFinancialSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	// Exact row snapshots catch mutations that count/sum-only checks would miss.
	var out strings.Builder
	for _, table := range []string{"accounts", "transactions", "account_balance_adjustments", "box_virtual_ledger", "investments", "investment_valuations", "investment_purchases", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for _, v := range values {
				out.WriteString(fmt.Sprintf("%T:%v", v, v))
				out.WriteByte(0)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(table)
	}
	return out.String()
}

func TestInvestmentSettlementRequiredScenarioAndPhaseBPreserved(t *testing.T) {
	p, _, _ := newInvestmentLifecycleDB(t)
	ci := investmentCreateInput("ws-a", "acc-inv-a")
	ci.PrincipalCents = 500000
	inv := mustCreateInvestment(t, p.Immediate, ci)
	exec(t, p.Immediate, `UPDATE accounts SET initial_balance=500000,current_balance=500000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	root, _ := financeRoot(t, p, inv)
	integrated := financeIntegrate(t, p, inv, root, "settlement-position", "2026-03-01", 530000, 1)
	w := settlementWithdrawalFixture(t, p, inv, 525500, withdrawalPrincipal(500000), InvestmentWithdrawalTotal)
	in := settlementInput(w)
	in.GrossCents = withdrawalPrincipal(530000)
	in.IRCents = withdrawalPrincipal(4500)
	in.PatrimonyCents = withdrawalPrincipal(530000)
	before := settlementFinancialSnapshot(t, p.DB)
	r := settlementMust(t, p, in)
	if r.State != InvestmentSettlementRecorded || r.PrincipalCents == nil || *r.PrincipalCents != 500000 || r.RealizedResultCents == nil || *r.RealizedResultCents != 25500 || r.ReferenceRevisionID != root || r.FinancialRevisionID != integrated.ID || r.EffectsState != "NOT_ACTIVATED" {
		t.Fatalf("wrong result %+v", r)
	}
	in.ExpectedVersion = 1
	in.RequestID = uuid.NewString()
	in.ConfirmReceipt = true
	in.ReceiptEvidence = "Extrato confirma R$ 5.255,00 disponíveis na corretora"
	reconciled := settlementMust(t, p, in)
	if reconciled.State != InvestmentSettlementReconciled || reconciled.PreviousID != r.ID {
		t.Fatal(reconciled)
	}
	got, err := ReadInvestmentSettlement(context.Background(), p.DB, "ws-a", inv.ID, w.ID)
	if err != nil || !reflect.DeepEqual(got, reconciled) {
		t.Fatal("read", err)
	}
	if before != settlementFinancialSnapshot(t, p.DB) {
		t.Fatal("C1 financial or legacy mutation")
	}
	financeWant(t, p.DB, 530000, 1)
	detail, err := GetInvestmentPurchaseDetail(context.Background(), p.DB, "ws-a", inv.ID)
	if err != nil || detail.Investment.Status != "ATIVA" || detail.MaintainedPurchaseCount != 0 {
		t.Fatal("administrative closure", err)
	}
	// C1 proof cannot unlock B's withdrawal barrier.
	insertValuationRow(t, p.Immediate, "after-settlement", "ws-a", inv.ID, "2026-04-01", 0)
	before = settlementFinancialSnapshot(t, p.DB)
	_, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, financeInput(t, p.DB, inv.ID, "after-settlement", root, 2))
	if !errors.Is(err, ErrInvestmentSettlementPending) {
		t.Fatal("B unlocked", err)
	}
	if before != settlementFinancialSnapshot(t, p.DB) {
		t.Fatal("blocked B mutated finances")
	}
}

func TestInvestmentSettlementPartialResultsAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		principal *int64
		result    *int64
	}{
		{"profit", withdrawalPrincipal(9000), withdrawalPrincipal(1000)},
		{"loss", withdrawalPrincipal(11000), withdrawalPrincipal(-1000)},
		{"zero", withdrawalPrincipal(10000), withdrawalPrincipal(0)},
		{"unknown", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, inv := newInvestmentLifecycleDB(t)
			w := settlementWithdrawalFixture(t, p, inv, 10000, tc.principal, InvestmentWithdrawalPartial)
			before := settlementFinancialSnapshot(t, p.DB)
			in := settlementInput(w)
			in.PatrimonyCents = withdrawalPrincipal(10500)
			r := settlementMust(t, p, in)
			if !reflect.DeepEqual(r.RealizedResultCents, tc.result) || !reflect.DeepEqual(r.PrincipalCents, tc.principal) {
				t.Fatal(r)
			}
			d, err := GetInvestmentPurchaseDetail(context.Background(), p.DB, "ws-a", inv.ID)
			if err != nil || d.MaintainedPurchaseCount != 1 || d.Investment.Status != "ATIVA" {
				t.Fatal("remaining investment lost", err)
			}
			if before != settlementFinancialSnapshot(t, p.DB) {
				t.Fatal("partial changed source")
			}
		})
	}
}

func TestInvestmentSettlementMultiplePurchasesAndCosts(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			p, _, inv := newInvestmentLifecycleDB(t)
			purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(inv.ID))
			if err != nil {
				t.Fatal(err)
			}
			wi := withdrawalInput(inv.ID, "initial:"+inv.ID, purchase.ID)
			wi.Allocations[0].PrincipalCents = withdrawalPrincipal(9000)
			if !unknown {
				wi.Allocations[1].PrincipalCents = withdrawalPrincipal(9500)
			}
			w, err := CreateInvestmentWithdrawal(context.Background(), p.Immediate, wi)
			if err != nil {
				t.Fatal(err)
			}
			in := settlementInput(w)
			in.GrossCents = withdrawalPrincipal(21000)
			in.IRCents = withdrawalPrincipal(700)
			in.IOFCents = withdrawalPrincipal(200)
			in.FeesCents = withdrawalPrincipal(100)
			in.PatrimonyCents = withdrawalPrincipal(20700)
			before := settlementFinancialSnapshot(t, p.DB)
			r := settlementMust(t, p, in)
			if unknown {
				if r.PrincipalCents != nil || r.RealizedResultCents != nil {
					t.Fatal("fabricated principal")
				}
			} else if *r.PrincipalCents != 18500 || *r.RealizedResultCents != 1500 {
				t.Fatal(r)
			}
			if before != settlementFinancialSnapshot(t, p.DB) {
				t.Fatal("mutated allocations")
			}
		})
	}
}

func TestInvestmentSettlementPendingUnknownsAndCorrection(t *testing.T) {
	for _, field := range []string{"gross", "IR", "IOF", "fees", "patrimony"} {
		t.Run(field, func(t *testing.T) {
			p, _, inv := newInvestmentLifecycleDB(t)
			w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
			in := settlementInput(w)
			switch field {
			case "gross":
				in.GrossCents = nil
			case "IR":
				in.IRCents = nil
			case "IOF":
				in.IOFCents = nil
			case "fees":
				in.FeesCents = nil
			case "patrimony":
				in.PatrimonyCents = nil
				in.PatrimonyEvidence = ""
			}
			r := settlementMust(t, p, in)
			if r.State != InvestmentSettlementPending {
				t.Fatal("unknown treated as zero", r)
			}
			complete := settlementInput(w)
			complete.ExpectedVersion = 1
			next := settlementMust(t, p, complete)
			if next.Version != 2 || next.PreviousID != r.ID || next.State != InvestmentSettlementRecorded {
				t.Fatal(next)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 2 {
				t.Fatal("lost audit history")
			}
		})
	}
}

func settlementExistingReceipt(t *testing.T, p *database.RuntimeDBs, id, typ string, amount int64) {
	t.Helper()
	source := "acc-inv-a"
	var dest any = "acc-checking-a"
	if typ == "INCOME" {
		source = "acc-checking-a"
		dest = nil
	}
	at := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC).Unix()
	exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES(?,'ws-a','user-a',?,?,?,?,?,?,'paid')`, id, source, dest, typ, amount, at, at)
}
func settlementWithReceipt(in RecordInvestmentSettlementInput, id string, amount int64) RecordInvestmentSettlementInput {
	in.DestinationAccountID = "acc-checking-a"
	in.Receipts = []InvestmentSettlementReceipt{{TransactionID: id, AmountCents: amount, Evidence: "Identificador confirmado no extrato"}}
	in.ConfirmReceipt = true
	in.ReceiptEvidence = "Recebimento conferido explicitamente"
	return in
}

func TestInvestmentSettlementExistingReceiptsCapacityAndDrift(t *testing.T) {
	for _, typ := range []string{"TRANSFER", "INCOME"} {
		t.Run(typ, func(t *testing.T) {
			p, _, inv := newInvestmentLifecycleDB(t)
			w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
			settlementExistingReceipt(t, p, "receipt", typ, 15000)
			in := settlementWithReceipt(settlementInput(w), "receipt", 10000)
			before := settlementFinancialSnapshot(t, p.DB)
			r := settlementMust(t, p, in)
			if r.State != InvestmentSettlementReconciled {
				t.Fatal(r)
			}
			w2 := settlementWithdrawalFixture(t, p, inv, 5000, nil, InvestmentWithdrawalPartial)
			second := settlementWithReceipt(settlementInput(w2), "receipt", 5000)
			before = settlementFinancialSnapshot(t, p.DB)
			settlementMust(t, p, second)
			if before != settlementFinancialSnapshot(t, p.DB) {
				t.Fatal("existing receipt modified")
			}
			w3 := settlementWithdrawalFixture(t, p, inv, 1, nil, InvestmentWithdrawalPartial)
			if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, settlementWithReceipt(settlementInput(w3), "receipt", 1)); !errors.Is(err, ErrInvestmentConflict) {
				t.Fatal("reused receipt", err)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 2 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_receipts`) != 2 {
				t.Fatal("over-allocation persisted")
			}
			exec(t, p.Immediate, `UPDATE transactions SET amount=16000 WHERE workspace_id='ws-a' AND id='receipt'`)
			if _, err := ReadInvestmentSettlement(context.Background(), p.DB, "ws-a", inv.ID, w.ID); !errors.Is(err, ErrInvestmentConflict) {
				t.Fatal("stale proof accepted", err)
			}
		})
	}
}

func TestInvestmentSettlementValidationIsolationAndRollback(t *testing.T) {
	for _, mode := range []string{"gross mismatch", "IR negative", "IOF negative", "fees negative", "gross negative", "net mismatch", "deductions exceed gross", "deduction overflow", "patrimony evidence", "invalid date", "year zero", "before withdrawal", "foreign workspace", "foreign investment", "foreign withdrawal", "foreign account", "foreign destination", "credit destination", "missing user", "stale version", "version overflow", "missing receipt proof", "incomplete proof", "uncovered destination", "unknown source", "foreign transfer", "wrong source", "wrong destination", "unpaid", "invalid transfer date", "early receipt", "duplicate receipt", "receipt overflow", "receipt too large", "short receipt", "net unknown"} {
		t.Run(mode, func(t *testing.T) {
			p, _, inv := newInvestmentLifecycleDB(t)
			w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
			in := settlementInput(w)
			settlementExistingReceipt(t, p, "receipt", "TRANSFER", 10000)
			switch mode {
			case "gross mismatch":
				in.GrossCents = withdrawalPrincipal(10001)
			case "IR negative":
				in.IRCents = withdrawalPrincipal(-1)
			case "IOF negative":
				in.IOFCents = withdrawalPrincipal(-1)
			case "fees negative":
				in.FeesCents = withdrawalPrincipal(-1)
			case "gross negative":
				in.GrossCents = withdrawalPrincipal(-1)
			case "net mismatch":
				in.NetCents = 9999
				in.GrossCents = withdrawalPrincipal(9999)
			case "net unknown":
				in.NetCents = -1
			case "deductions exceed gross":
				in.IRCents = withdrawalPrincipal(10001)
			case "deduction overflow":
				in.GrossCents = withdrawalPrincipal(math.MaxInt64)
				in.IRCents = withdrawalPrincipal(math.MaxInt64)
				in.IOFCents = withdrawalPrincipal(1)
			case "patrimony evidence":
				in.PatrimonyEvidence = ""
			case "invalid date":
				in.EconomicDate = "2026-02-29"
			case "year zero":
				in.EconomicDate = "0000-01-01"
			case "before withdrawal":
				in.EconomicDate = "2026-03-14"
			case "foreign workspace":
				in.WorkspaceID = "ws-b"
			case "foreign investment":
				other := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-b", "acc-inv-b"))
				in.InvestmentID = other.ID
			case "foreign withdrawal":
				other := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
				otherW := settlementWithdrawalFixture(t, p, other, 10000, nil, InvestmentWithdrawalPartial)
				in.WithdrawalID = otherW.ID
			case "foreign account":
				in.AccountID = "acc-inv-b"
			case "foreign destination":
				in.DestinationAccountID = "acc-inv-b"
			case "credit destination":
				exec(t, p.Immediate, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('card','ws-a','Card','CREDIT_CARD')`)
				in.DestinationAccountID = "card"
			case "missing user":
				in.CreatedBy = "missing"
			case "stale version":
				in.ExpectedVersion = 1
			case "version overflow":
				in.ExpectedVersion = math.MaxInt64
			case "missing receipt proof":
				in.ConfirmReceipt = true
			case "incomplete proof":
				in.ConfirmReceipt = true
				in.ReceiptEvidence = "receipt"
				in.PatrimonyCents = nil
			case "uncovered destination":
				in.DestinationAccountID = "acc-checking-a"
				in.ConfirmReceipt = true
				in.ReceiptEvidence = "receipt"
			default:
				in = settlementWithReceipt(in, "receipt", 10000)
				switch mode {
				case "unknown source":
					in.Receipts[0].TransactionID = "unknown"
				case "foreign transfer":
					exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('foreign','ws-b','user-b','acc-inv-b','INCOME',10000,1773619200,'paid')`)
					in.Receipts[0].TransactionID = "foreign"
				case "wrong source":
					exec(t, p.Immediate, `UPDATE transactions SET account_id='acc-checking-a' WHERE workspace_id='ws-a' AND id='receipt'`)
				case "wrong destination":
					in.DestinationAccountID = "acc-inv-a"
				case "unpaid":
					exec(t, p.Immediate, `UPDATE transactions SET status='pending' WHERE workspace_id='ws-a' AND id='receipt'`)
				case "invalid transfer date":
					exec(t, p.Immediate, `UPDATE transactions SET paid_date_civil='2026-03-17' WHERE workspace_id='ws-a' AND id='receipt'`)
				case "early receipt":
					in.EconomicDate = "2026-03-17"
				case "duplicate receipt":
					in.Receipts = append(in.Receipts, in.Receipts[0])
				case "receipt overflow":
					in.Receipts[0].AmountCents = math.MaxInt64
					in.Receipts = append(in.Receipts, InvestmentSettlementReceipt{TransactionID: "z", AmountCents: 1, Evidence: "x"})
				case "receipt too large":
					in.Receipts[0].AmountCents = 10001
				case "short receipt":
					in.Receipts[0].AmountCents = 9999
				}
			}
			before := settlementFinancialSnapshot(t, p.DB)
			if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, in); err == nil {
				t.Fatal("invalid accepted")
			}
			if before != settlementFinancialSnapshot(t, p.DB) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_receipts`) != 0 {
				t.Fatal("failed validation changed state")
			}
		})
	}
}

func TestInvestmentSettlementIdempotencyHistoryAndClaims(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
	settlementExistingReceipt(t, p, "part1", "TRANSFER", 4000)
	settlementExistingReceipt(t, p, "part2", "TRANSFER", 6000)
	in := settlementWithReceipt(settlementInput(w), "part1", 4000)
	in.ConfirmReceipt = false
	first := settlementMust(t, p, in)
	replay := settlementMust(t, p, in)
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("non-idempotent")
	}
	changed := in
	changed.Reason = "different"
	if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, changed); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("divergent replay", err)
	}
	// A persistent key is also unique across withdrawals and acting users.
	changed = in
	changed.CreatedBy = "user-b"
	if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, changed); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("key reused by another author", err)
	}
	for _, mode := range []string{"drop", "change", "destination", "stale"} {
		changed = in
		changed.RequestID = uuid.NewString()
		changed.ExpectedVersion = 1
		changed.Receipts = append([]InvestmentSettlementReceipt(nil), in.Receipts...)
		switch mode {
		case "drop":
			changed.Receipts = nil
		case "change":
			changed.Receipts[0].AmountCents--
		case "destination":
			changed.DestinationAccountID = "acc-inv-a"
		case "stale":
			changed.ExpectedVersion = 0
		}
		if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, changed); err == nil {
			t.Fatal("claim correction accepted", mode)
		}
	}
	next := in
	next.ExpectedVersion = 1
	next.CreatedBy = "user-b"
	next.RequestID = uuid.NewString()
	next.ConfirmReceipt = true
	next.Receipts = append(next.Receipts, InvestmentSettlementReceipt{TransactionID: "part2", AmountCents: 6000, Evidence: "Segundo recebimento identificado"})
	second := settlementMust(t, p, next)
	if second.PreviousID != first.ID || second.CreatedBy != "user-b" || second.Version != 2 || second.State != InvestmentSettlementReconciled {
		t.Fatal(second)
	}
	next.ExpectedVersion = 2
	next.RequestID = uuid.NewString()
	if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, next); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("conciliated correction accepted", err)
	}
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 2 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_receipts`) != 2 {
		t.Fatal("duplicated history/claims")
	}
	for _, table := range []string{"investment_settlement_revisions", "investment_settlement_receipts"} {
		for _, q := range []string{`DELETE FROM ` + table, `UPDATE ` + table + ` SET workspace_id=workspace_id`, `INSERT OR REPLACE INTO ` + table + ` SELECT * FROM ` + table} {
			if _, err := p.Immediate.Exec(q); err == nil {
				t.Fatal("immutable evidence changed", q)
			}
		}
	}
	if _, err := ReadInvestmentSettlement(context.Background(), p.DB, "ws-b", inv.ID, w.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatal("foreign read", err)
	}
}

func TestInvestmentSettlementConcurrentIndependentWriters(t *testing.T) {
	for _, mode := range []string{"same key", "different keys", "shared receipt"} {
		t.Run(mode, func(t *testing.T) {
			p, path, inv := newInvestmentLifecycleDB(t)
			w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
			settlementExistingReceipt(t, p, "only-receipt", "TRANSFER", 10000)
			a := settlementWithReceipt(settlementInput(w), "only-receipt", 10000)
			b := a
			if mode == "different keys" {
				b.RequestID = uuid.NewString()
			}
			if mode == "shared receipt" {
				w2 := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
				b = settlementWithReceipt(settlementInput(w2), "only-receipt", 10000)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			atCommit, atBegin, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			dsn := path + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
			first := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeCommit: func() {
				close(atCommit)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}})
			first.SetMaxOpenConns(1)
			defer first.Close()
			second := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeBegin: func() { close(atBegin) }})
			second.SetMaxOpenConns(1)
			defer second.Close()
			before := settlementFinancialSnapshot(t, p.DB)
			type outcome struct {
				r   InvestmentSettlement
				err error
			}
			done := make(chan outcome, 2)
			go func() { r, err := RecordInvestmentSettlement(ctx, first, a); done <- outcome{r, err} }()
			select {
			case <-atCommit:
			case <-ctx.Done():
				t.Fatal("first writer blocked")
			}
			go func() { r, err := RecordInvestmentSettlement(ctx, second, b); done <- outcome{r, err} }()
			select {
			case <-atBegin:
			case <-ctx.Done():
				t.Fatal("second writer blocked")
			}
			close(release)
			results := []outcome{<-done, <-done}
			success, conflict := 0, 0
			for _, o := range results {
				if o.err == nil {
					success++
				} else if errors.Is(o.err, ErrInvestmentConflict) {
					conflict++
				} else {
					t.Fatal(o.err)
				}
			}
			if mode == "same key" {
				if success != 2 || results[0].r.ID != results[1].r.ID {
					t.Fatal("same key race", results)
				}
			} else if success != 1 || conflict != 1 {
				t.Fatal("double winner", results)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 1 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_receipts`) != 1 {
				t.Fatal("duplicate settlement")
			}
			if before != settlementFinancialSnapshot(t, p.DB) {
				t.Fatal("race changed finances")
			}
		})
	}
}

func TestInvestmentSettlementAtomicWriteFailure(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
	settlementExistingReceipt(t, p, "a", "TRANSFER", 5000)
	settlementExistingReceipt(t, p, "b", "TRANSFER", 5000)
	in := settlementWithReceipt(settlementInput(w), "a", 5000)
	in.Receipts = append(in.Receipts, InvestmentSettlementReceipt{TransactionID: "b", AmountCents: 5000, Evidence: "Second part"})
	exec(t, p.Immediate, `CREATE TRIGGER settlement_injected BEFORE INSERT ON investment_settlement_receipts WHEN NEW.transaction_id='b' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	before := settlementFinancialSnapshot(t, p.DB)
	if _, err := RecordInvestmentSettlement(context.Background(), p.Immediate, in); err == nil {
		t.Fatal("injected failure accepted")
	}
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_receipts`) != 0 || before != settlementFinancialSnapshot(t, p.DB) {
		t.Fatal("partial write survived")
	}
	exec(t, p.Immediate, `DROP TRIGGER settlement_injected`)
	settlementMust(t, p, in)
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_receipts`) != 2 {
		t.Fatal("retry lost claims")
	}
}

func TestInvestmentSettlementBoundariesAndLegacyClosure(t *testing.T) {
	t.Run("max int cents", func(t *testing.T) {
		p, _, inv := newInvestmentLifecycleDB(t)
		w := settlementWithdrawalFixture(t, p, inv, math.MaxInt64, nil, InvestmentWithdrawalTotal)
		in := settlementInput(w)
		r := settlementMust(t, p, in)
		if r.Input.NetCents != math.MaxInt64 || r.RealizedResultCents != nil {
			t.Fatal(r)
		}
	})
	t.Run("zero receipt loss", func(t *testing.T) {
		p, _, inv := newInvestmentLifecycleDB(t)
		w := settlementWithdrawalFixture(t, p, inv, 0, withdrawalPrincipal(100000), InvestmentWithdrawalTotal)
		in := settlementInput(w)
		r := settlementMust(t, p, in)
		if r.RealizedResultCents == nil || *r.RealizedResultCents != -100000 {
			t.Fatal(r)
		}
	})
	t.Run("administrative closure is not settlement", func(t *testing.T) {
		p, _, inv := newInvestmentLifecycleDB(t)
		w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalTotal)
		in := investmentRedeemInput(inv.ID)
		in.RedeemedDate = "2026-03-16"
		if err := RedeemInvestment(context.Background(), p.Immediate, in); err != nil {
			t.Fatal(err)
		}
		if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_revisions`) != 0 {
			t.Fatal("automatic settlement")
		}
		si := settlementInput(w)
		si.IRCents = nil
		r := settlementMust(t, p, si)
		if r.State != InvestmentSettlementPending {
			t.Fatal("status used as financial proof")
		}
	})
	t.Run("withdrawal snapshot drift", func(t *testing.T) {
		p, _, inv := newInvestmentLifecycleDB(t)
		w := settlementWithdrawalFixture(t, p, inv, 10000, nil, InvestmentWithdrawalPartial)
		settlementMust(t, p, settlementInput(w))
		exec(t, p.Immediate, `UPDATE investment_withdrawal_allocations SET principal_cents=1 WHERE workspace_id='ws-a' AND withdrawal_id=?`, w.ID)
		if _, err := ReadInvestmentSettlement(context.Background(), p.DB, "ws-a", inv.ID, w.ID); !errors.Is(err, ErrInvestmentConflict) {
			t.Fatal("source drift ignored", err)
		}
	})
}

func TestInvestmentSettlementOnlyExplicitOperationalActivation(t *testing.T) {
	for _, root := range []string{"../handlers", "../../cmd/server", "../jobs"} {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), "RecordInvestmentSettlement(") {
				if filepath.Clean(path) != filepath.Clean("../handlers/investment_settlement_operations.go") ||
					!strings.Contains(string(b), "features.FromEnv().InvestmentSettlements") {
					t.Error("C1 activation outside the explicit guarded operation", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
