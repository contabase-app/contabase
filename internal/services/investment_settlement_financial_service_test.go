package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func c2aFixture(t *testing.T, net, patrimony int64, partial, unknown bool) (*database.RuntimeDBs, models.Investment, InvestmentSettlement, ApplyInvestmentSettlementInput) {
	t.Helper()
	return c2aFixtureWithReceipt(t, net, patrimony, partial, unknown, "")
}
func c2aFixtureWithReceipt(t *testing.T, net, patrimony int64, partial, unknown bool, kind string) (*database.RuntimeDBs, models.Investment, InvestmentSettlement, ApplyInvestmentSettlementInput) {
	t.Helper()
	p, _, _ := newInvestmentLifecycleDB(t)
	ci := investmentCreateInput("ws-a", "acc-inv-a")
	ci.PrincipalCents = 500000
	inv := mustCreateInvestment(t, p.Immediate, ci)
	exec(t, p.Immediate, `UPDATE accounts SET initial_balance=500000,current_balance=500000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	root, _ := financeRoot(t, p, inv)
	b := financeIntegrate(t, p, inv, root, "c2a-value", "2026-03-01", 530000, 1)
	mode := InvestmentWithdrawalTotal
	principal := withdrawalPrincipal(500000)
	if partial {
		mode = InvestmentWithdrawalPartial
		principal = withdrawalPrincipal(100000)
	}
	if unknown {
		principal = nil
	}
	w := settlementWithdrawalFixture(t, p, inv, net, principal, mode)
	input := settlementInput(w)
	input.PatrimonyCents = withdrawalPrincipal(patrimony)
	gross := patrimony
	if net > gross {
		gross = net
	}
	input.GrossCents = withdrawalPrincipal(gross)
	input.IRCents = withdrawalPrincipal(gross - net)
	input.ConfirmReceipt = true
	input.ReceiptEvidence = "Extrato confirma destino e valor líquido"
	if kind != "" {
		at := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC).Unix()
		tx, err := p.Immediate.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		source := "acc-inv-a"
		var dest any = "acc-checking-a"
		if kind == "INCOME" {
			source = "acc-checking-a"
			dest = nil
		}
		_, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES('c2a-receipt','ws-a','user-a',?,?,?,?,?,?,'paid')`, source, dest, kind, net, at, at)
		if err != nil {
			t.Fatal(err)
		}
		destination := "acc-checking-a"
		if kind == "INCOME" {
			destination = ""
		}
		if err = ApplyBalanceEffect(tx, "ws-a", kind, "INVESTMENT", "paid", net, source, destination, at); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		input.DestinationAccountID = "acc-checking-a"
		input.Receipts = []InvestmentSettlementReceipt{{TransactionID: "c2a-receipt", AmountCents: net, Evidence: "Comprovante identificado"}}
	}
	s := settlementMust(t, p, input)
	in := ApplyInvestmentSettlementInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, WithdrawalID: w.ID, SettlementRevisionID: s.ID, ReferenceRevisionID: root, FinancialRevisionID: b.ID, ExpectedSettlementVersion: s.Version, ExpectedFinancialVersion: b.Version, CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Liquidação comprovada", ReconciliationEvidence: "Conferidos extratos; sem outros efeitos equivalentes além dos vínculos informados"}
	if partial {
		insertValuationRow(t, p.Immediate, "c2a-remaining", "ws-a", inv.ID, w.WithdrawalDate, 530000-patrimony)
		c, err := ReadInvestmentReferenceCandidate(context.Background(), p.DB, "ws-a", inv.ID, "c2a-remaining")
		if err != nil {
			t.Fatal(err)
		}
		in.RemainingPositionID = "c2a-remaining"
		in.RemainingCoverageToken = c.CoverageToken
		in.DayClosed = true
	}
	c2aVersions(t, p.DB, s, &in)
	return p, inv, s, in
}
func c2aVersions(t *testing.T, db *sql.DB, s InvestmentSettlement, in *ApplyInvestmentSettlementInput) {
	t.Helper()
	if err := db.QueryRow(`SELECT balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, s.Input.AccountID).Scan(&in.ExpectedBalanceVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, s.Input.DestinationAccountID).Scan(&in.ExpectedDestinationVersion); err != nil {
		t.Fatal(err)
	}
}
func c2aApply(t *testing.T, p *database.RuntimeDBs, in ApplyInvestmentSettlementInput) InvestmentSettlementApplication {
	t.Helper()
	r, err := ApplyInvestmentSettlement(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func c2aStableSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"transactions", "investments", "investment_valuations", "investment_purchases", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions", "investment_settlement_receipts", "box_virtual_ledger"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%s:%#v\n", table, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}
func c2aReject(t *testing.T, p *database.RuntimeDBs, in ApplyInvestmentSettlementInput, want error) {
	t.Helper()
	before := settlementFinancialSnapshot(t, p.DB)
	events := countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_applications`)
	_, err := ApplyInvestmentSettlement(context.Background(), p.Immediate, in)
	if err == nil || want != nil && !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
	if before != settlementFinancialSnapshot(t, p.DB) || events != countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_applications`) {
		t.Fatal("rejection changed finances")
	}
}

func TestInvestmentSettlementFinancialResidualScenarios(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		net, patrimony, delta int64
		partial, unknown      bool
	}{
		{"total IR retained", 525500, 530000, -4500, false, false},
		{"partial IR", 105100, 106000, -900, true, false},
		{"loss", 480000, 530000, -50000, false, false},
		{"above withdrawn patrimony", 540000, 530000, 10000, false, false},
		{"zero", 530000, 530000, 0, false, false},
		{"unknown principal", 525500, 530000, -4500, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, inv, s, in := c2aFixture(t, tc.net, tc.patrimony, tc.partial, tc.unknown)
			before := c2aStableSnapshot(t, p.DB)
			oldAdjustment := queryScalar(t, p.DB, `SELECT id||':'||delta_cents||':'||created_at FROM account_balance_adjustments`)
			r := c2aApply(t, p, in)
			n := int64(2)
			if tc.delta == 0 {
				n = 1
			}
			financeWant(t, p.DB, 530000+tc.delta, n)
			if r.ResidualCents != tc.delta || r.State != InvestmentSettlementFinancialApplied || (r.AdjustmentID == "") != (tc.delta == 0) {
				t.Fatal(r)
			}
			if before != c2aStableSnapshot(t, p.DB) {
				t.Fatal("C2A mutated legacy or preparatory rows")
			}
			if oldAdjustment != queryScalar(t, p.DB, `SELECT id||':'||delta_cents||':'||created_at FROM account_balance_adjustments ORDER BY rowid LIMIT 1`) {
				t.Fatal("B adjustment changed")
			}
			if s.PrincipalCents == nil && s.RealizedResultCents != nil {
				t.Fatal("unknown became zero")
			}
			got := c2aApply(t, p, in)
			if !reflect.DeepEqual(got, r) {
				t.Fatal("replay changed result")
			}
			financeWant(t, p.DB, 530000+tc.delta, n)
			diverged := in
			diverged.Reason = "Divergente"
			c2aReject(t, p, diverged, ErrInvestmentConflict)
			diverged = in
			diverged.RequestID = uuid.NewString()
			c2aReject(t, p, diverged, ErrInvestmentConflict)
			d, err := GetInvestmentPurchaseDetail(context.Background(), p.DB, "ws-a", inv.ID)
			if err != nil || d.Investment.Status != "ATIVA" {
				t.Fatal("administrative close", err)
			}
			// B must still refuse to infer a later gain from withdrawn money.
			insertValuationRow(t, p.Immediate, "later", "ws-a", inv.ID, "2026-04-01", 0)
			_, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, financeInput(t, p.DB, inv.ID, "later", in.ReferenceRevisionID, 2))
			if !errors.Is(err, ErrInvestmentSettlementPending) {
				t.Fatal("B withdrawal barrier removed", err)
			}
		})
	}
}

func c2aReceipt(t *testing.T, p *database.RuntimeDBs, s InvestmentSettlement, in *ApplyInvestmentSettlementInput, kind string) InvestmentSettlement {
	t.Helper()
	// Create a distinct C1 fixture with its explicit receipt instead of modifying
	// an immutable reconciled revision. The previous fixture remains untouched.
	ci := investmentCreateInput("ws-a", "acc-inv-a")
	ci.PrincipalCents = 500000
	inv := mustCreateInvestment(t, p.Immediate, ci)
	financeTransfer(t, p.Immediate, "second-funds", "2026-01-15", 500000)
	root, b := financeRoot(t, p, inv)
	// Root is 500000, so this independent settlement has a 4500 residual.
	w := settlementWithdrawalFixture(t, p, inv, 495500, withdrawalPrincipal(500000), InvestmentWithdrawalTotal)
	at := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC).Unix()
	tx, err := p.Immediate.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	source := "acc-inv-a"
	var dest any = "acc-checking-a"
	if kind == "INCOME" {
		source = "acc-checking-a"
		dest = nil
	}
	_, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES('c2a-receipt','ws-a','user-a',?,?,?,495500,?,?,'paid')`, source, dest, kind, at, at)
	if err != nil {
		t.Fatal(err)
	}
	destination := "acc-checking-a"
	if kind == "INCOME" {
		destination = ""
	}
	if err = ApplyBalanceEffect(tx, "ws-a", kind, "CHECKING", "paid", 495500, source, destination, at); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	si := settlementInput(w)
	si.GrossCents = withdrawalPrincipal(500000)
	si.IRCents = withdrawalPrincipal(4500)
	si.PatrimonyCents = withdrawalPrincipal(500000)
	si.DestinationAccountID = "acc-checking-a"
	si.Receipts = []InvestmentSettlementReceipt{{TransactionID: "c2a-receipt", AmountCents: 495500, Evidence: "Transferência identificada"}}
	si.ConfirmReceipt = true
	si.ReceiptEvidence = "Comprovante bancário"
	s = settlementMust(t, p, si)
	*in = ApplyInvestmentSettlementInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, WithdrawalID: w.ID, SettlementRevisionID: s.ID, ReferenceRevisionID: root, FinancialRevisionID: b.ID, ExpectedSettlementVersion: 1, ExpectedFinancialVersion: 1, CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Conciliação", ReconciliationEvidence: "Comprovante e efeitos conferidos"}
	c2aVersions(t, p.DB, s, in)
	return s
}

func TestInvestmentSettlementFinancialExistingTransferAndIncome(t *testing.T) {
	for _, kind := range []string{"TRANSFER", "INCOME", "changed transfer"} {
		t.Run(kind, func(t *testing.T) {
			typ := kind
			if typ == "changed transfer" {
				typ = "TRANSFER"
			}
			p, _, _, in := c2aFixtureWithReceipt(t, 525500, 530000, false, false, typ)
			if kind == "changed transfer" {
				exec(t, p.Immediate, `UPDATE transactions SET amount=495499 WHERE id='c2a-receipt'`)
				c2aReject(t, p, in, ErrInvestmentConflict)
				return
			}
			if kind == "INCOME" {
				c2aReject(t, p, in, ErrInvestmentLegacyIncome)
				return
			}
			before := c2aStableSnapshot(t, p.DB)
			checking := queryScalar(t, p.DB, `SELECT current_balance FROM accounts WHERE id='acc-checking-a'`)
			r := c2aApply(t, p, in)
			if r.ResidualCents != -4500 || r.BalanceCents != 0 {
				t.Fatal(r)
			}
			if before != c2aStableSnapshot(t, p.DB) || checking != queryScalar(t, p.DB, `SELECT current_balance FROM accounts WHERE id='acc-checking-a'`) {
				t.Fatal("transfer repeated")
			}
			for _, q := range []string{`UPDATE transactions SET amount=1 WHERE id='c2a-receipt'`, `UPDATE transactions SET status='pending' WHERE id='c2a-receipt'`, `DELETE FROM transactions WHERE id='c2a-receipt'`, `INSERT OR REPLACE INTO transactions SELECT * FROM transactions WHERE id='c2a-receipt'`} {
				if _, err := p.Immediate.Exec(q); err == nil {
					t.Fatal("source mutation allowed", q)
				}
			}
		})
	}
}

func TestInvestmentSettlementFinancialPriorEffects(t *testing.T) {
	for _, kind := range []string{"manual", "expense", "B adjustment", "wrong delta", "foreign", "reused"} {
		t.Run(kind, func(t *testing.T) {
			p, _, s, in := c2aFixture(t, 525500, 530000, false, false)
			effect := InvestmentSettlementPriorEffect{AdjustmentID: "tax", DeltaCents: -4500, Evidence: "IR desta liquidação já baixado; comprovante identificado"}
			if kind == "B adjustment" {
				effect.AdjustmentID = queryScalar(t, p.DB, `SELECT id FROM account_balance_adjustments LIMIT 1`)
				effect.DeltaCents = 30000
			} else if kind == "expense" {
				effect.AdjustmentID = ""
				effect.TransactionID = "tax"
				at := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC).Unix()
				tx, err := p.Immediate.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,paid_at,status) VALUES('tax','ws-a','user-a','acc-inv-a','EXPENSE',4500,?,?,'paid')`, at, at); err != nil {
					t.Fatal(err)
				}
				if err = ApplyBalanceEffect(tx, "ws-a", "EXPENSE", "INVESTMENT", "paid", 4500, "acc-inv-a", "", at); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			} else {
				err := AdjustAccountBalance(context.Background(), p.Immediate, AccountBalanceAdjustment{ID: "tax", WorkspaceID: "ws-a", AccountID: "acc-inv-a", UserID: "user-a", Reason: "IR já pago", ExpectedVersion: in.ExpectedBalanceVersion, TargetBalance: 525500, CreatedAt: time.Now().Unix()})
				if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "wrong delta" {
				effect.DeltaCents = -4400
			}
			if kind == "foreign" {
				effect.AdjustmentID = "missing"
			}
			in.PriorEffects = []InvestmentSettlementPriorEffect{effect}
			c2aVersions(t, p.DB, s, &in)
			if kind == "B adjustment" || kind == "wrong delta" || kind == "foreign" {
				c2aReject(t, p, in, nil)
				return
			}
			before := settlementFinancialSnapshot(t, p.DB)
			r := c2aApply(t, p, in)
			if r.ResidualCents != 0 || r.AdjustmentID != "" || r.PriorEffectCents != -4500 {
				t.Fatal(r)
			}
			if before != settlementFinancialSnapshot(t, p.DB) {
				t.Fatal("tax deducted again")
			}
			if kind == "reused" {
				var second ApplyInvestmentSettlementInput
				c2aReceipt(t, p, s, &second, "TRANSFER")
				second.PriorEffects = in.PriorEffects
				c2aReject(t, p, second, ErrInvestmentConflict)
			}
			if kind == "expense" {
				if _, err := p.Immediate.Exec(`INSERT INTO transaction_refunds(id,workspace_id,transaction_id,amount_cents,description,created_by) VALUES('refund','ws-a','tax',4500,'refund','user-a')`); err == nil {
					t.Fatal("refunded sealed effect")
				}
			}
		})
	}
}

func TestInvestmentSettlementFinancialProofConflicts(t *testing.T) {
	for _, kind := range []string{"account version", "destination version", "C1 revision", "B revision", "reference", "retro purchase", "changed position", "missing remaining", "wrong remaining", "missing patrimony", "recorded only", "tenant", "overflow", "exhausted version"} {
		t.Run(kind, func(t *testing.T) {
			p, inv, s, in := c2aFixture(t, 105100, 106000, true, false)
			switch kind {
			case "account version":
				in.ExpectedBalanceVersion++
			case "destination version":
				in.ExpectedDestinationVersion++
			case "C1 revision":
				in.ExpectedSettlementVersion++
			case "B revision":
				in.FinancialRevisionID = uuid.NewString()
			case "reference":
				in.ReferenceRevisionID = uuid.NewString()
			case "retro purchase":
				pi := investmentPurchaseInput(inv.ID)
				pi.PurchaseDate = "2026-02-15"
				if _, err := CreateInvestmentPurchase(context.Background(), p.Immediate, pi); err != nil {
					t.Fatal(err)
				}
			case "changed position":
				exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=530001 WHERE id='c2a-value'`)
			case "missing remaining":
				in.RemainingPositionID = ""
				in.RemainingCoverageToken = ""
			case "wrong remaining":
				exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=424001 WHERE id='c2a-remaining'`)
			case "tenant":
				in.WorkspaceID = "ws-b"
			case "overflow":
				exec(t, p.Immediate, `UPDATE accounts SET current_balance=? WHERE id='acc-inv-a'`, int64(math.MinInt64))
				c2aVersions(t, p.DB, s, &in)
			case "exhausted version":
				exec(t, p.Immediate, `UPDATE accounts SET balance_version=? WHERE id='acc-inv-a'`, int64(math.MaxInt64))
				c2aVersions(t, p.DB, s, &in)
			case "missing patrimony", "recorded only":
				ci := investmentCreateInput("ws-a", "acc-inv-a")
				extra := mustCreateInvestment(t, p.Immediate, ci)
				root, b := financeRoot(t, p, extra)
				w := settlementWithdrawalFixture(t, p, extra, 10000, nil, InvestmentWithdrawalPartial)
				si := settlementInput(w)
				if kind == "missing patrimony" {
					si.PatrimonyCents = nil
					si.PatrimonyEvidence = ""
				}
				c1 := settlementMust(t, p, si)
				in.InvestmentID = extra.ID
				in.WithdrawalID = w.ID
				in.SettlementRevisionID = c1.ID
				in.ReferenceRevisionID = root
				in.FinancialRevisionID = b.ID
				in.ExpectedSettlementVersion = 1
				in.ExpectedFinancialVersion = 1
			}
			c2aReject(t, p, in, nil)
		})
	}
}

func TestInvestmentSettlementFinancialConcurrencyRollbackAndSeals(t *testing.T) {
	t.Run("same request", func(t *testing.T) {
		p, _, _, in := c2aFixture(t, 525500, 530000, false, false)
		start := make(chan struct{})
		results := make(chan InvestmentSettlementApplication, 8)
		errs := make(chan error, 8)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				r, e := ApplyInvestmentSettlement(context.Background(), p.Immediate, in)
				results <- r
				errs <- e
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		id := ""
		for r := range results {
			if id != "" && id != r.ID {
				t.Fatal("duplicate")
			}
			id = r.ID
		}
		financeWant(t, p.DB, 525500, 2)
	})
	for _, table := range []string{"account_balance_adjustments", "investment_settlement_applications", "investment_settlement_prior_effects"} {
		t.Run("rollback "+table, func(t *testing.T) {
			p, _, s, in := c2aFixture(t, 525500, 530000, false, false)
			if table == "investment_settlement_prior_effects" {
				err := AdjustAccountBalance(context.Background(), p.Immediate, AccountBalanceAdjustment{ID: "some-tax", WorkspaceID: "ws-a", AccountID: "acc-inv-a", UserID: "user-a", Reason: "Parte do IR", ExpectedVersion: in.ExpectedBalanceVersion, TargetBalance: 529000, CreatedAt: time.Now().Unix()})
				if err != nil {
					t.Fatal(err)
				}
				in.PriorEffects = []InvestmentSettlementPriorEffect{{AdjustmentID: "some-tax", DeltaCents: -1000, Evidence: "Parte comprovada"}}
				c2aVersions(t, p.DB, s, &in)
			}
			exec(t, p.Immediate, `CREATE TRIGGER c2a_failure BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
			c2aReject(t, p, in, nil)
		})
	}
	t.Run("sealed sources", func(t *testing.T) {
		p, inv, _, in := c2aFixture(t, 105100, 106000, true, false)
		c2aApply(t, p, in)
		for _, q := range []string{`UPDATE investment_valuations SET amount_cents=1 WHERE id='c2a-value'`, `UPDATE investment_valuations SET amount_cents=1 WHERE id='c2a-remaining'`, `DELETE FROM investment_settlement_applications`, `UPDATE investment_settlement_applications SET state='DONE'`, `INSERT OR REPLACE INTO investment_settlement_applications SELECT * FROM investment_settlement_applications`, `UPDATE investment_purchases SET principal_cents=1 WHERE investment_id='` + inv.ID + `'`} {
			if _, err := p.Immediate.Exec(q); err == nil {
				t.Fatal("sealed mutation", q)
			}
		}
		wi := withdrawalInput(inv.ID, "initial:"+inv.ID)
		wi.WithdrawalDate = "2026-03-14"
		if _, err := CreateInvestmentWithdrawal(context.Background(), p.Immediate, wi); err == nil {
			t.Fatal("retroactive withdrawal allowed")
		}
	})
}

func TestInvestmentSettlementFinancialNoActivation(t *testing.T) {
	for _, dir := range []string{"../handlers", "../../cmd/server"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
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
			calls := strings.Count(string(b), "ApplyInvestmentSettlement(")
			if filepath.Clean(path) == filepath.Clean("../handlers/investment_settlement_effects.go") {
				if calls != 1 {
					t.Errorf("expected one explicit O1B2 C2A invocation: %s", path)
				}
				return nil // The O1B2 HTTP suite verifies opt-in and explicit POST.
			}
			if calls != 0 {
				t.Errorf("automatic C2A activation: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestInvestmentSettlementFinancialConcurrentDifferentKeysAndInvestments(t *testing.T) {
	for _, differentInvestment := range []bool{false, true} {
		t.Run(fmt.Sprint(differentInvestment), func(t *testing.T) {
			p, _, s, first := c2aFixture(t, 525500, 530000, false, false)
			second := first
			second.RequestID = uuid.NewString()
			if differentInvestment {
				c2aReceipt(t, p, s, &second, "TRANSFER")
				c2aVersions(t, p.DB, s, &first)
			}
			inputs := []ApplyInvestmentSettlementInput{first, second}
			errs := make([]error, 2)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range inputs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, errs[i] = ApplyInvestmentSettlement(context.Background(), p.Immediate, inputs[i])
				}(i)
			}
			close(start)
			wg.Wait()
			success := 0
			failed := -1
			for i, e := range errs {
				if e == nil {
					success++
				} else {
					failed = i
					if !errors.Is(e, ErrInvestmentConflict) && !errors.Is(e, ErrBalanceAdjustmentConflict) {
						t.Fatal(e)
					}
				}
			}
			if success != 1 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_applications`) != 1 {
				t.Fatal("concurrent application", errs)
			}
			if differentInvestment {
				retry := inputs[failed]
				retry.RequestID = uuid.NewString()
				var sj string
				if err := p.DB.QueryRow(`SELECT result_json FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, retry.WorkspaceID, retry.InvestmentID, retry.WithdrawalID).Scan(&sj); err != nil {
					t.Fatal(err)
				}
				var c1 InvestmentSettlement
				if err := json.Unmarshal([]byte(sj), &c1); err != nil {
					t.Fatal(err)
				}
				c2aVersions(t, p.DB, c1, &retry)
				c2aApply(t, p, retry)
				if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_settlement_applications`) != 2 {
					t.Fatal("second investment blocked")
				}
				financeWant(t, p.DB, 525500, 3)
			}
		})
	}
}

func TestInvestmentSettlementFinancialSuccessivePartialAndSourceDrift(t *testing.T) {
	p, inv, _, in := c2aFixture(t, 105100, 106000, true, false)
	first := c2aApply(t, p, in)
	// An allocation is part of the immutable applied proof, not just its parent.
	if _, err := p.Immediate.Exec(`UPDATE investment_withdrawal_allocations SET principal_cents=1 WHERE workspace_id='ws-a' AND withdrawal_id=?`, in.WithdrawalID); err == nil {
		t.Fatal("allocation changed after financial application")
	}
	wi := withdrawalInput(inv.ID, "initial:"+inv.ID)
	wi.WithdrawalDate = "2026-03-20"
	wi.ReceivedCents = 105100
	wi.Allocations[0].ReceivedCents = 105100
	wi.Allocations[0].PrincipalCents = withdrawalPrincipal(100000)
	w, err := CreateInvestmentWithdrawal(context.Background(), p.Immediate, wi)
	if err != nil {
		t.Fatal(err)
	}
	si := settlementInput(w)
	si.GrossCents = withdrawalPrincipal(106000)
	si.IRCents = withdrawalPrincipal(900)
	si.PatrimonyCents = withdrawalPrincipal(106000)
	si.ConfirmReceipt = true
	si.ReceiptEvidence = "Segundo resgate confirmado"
	s := settlementMust(t, p, si)
	insertValuationRow(t, p.Immediate, "second-remaining", "ws-a", inv.ID, "2026-03-20", 318000)
	c, err := ReadInvestmentReferenceCandidate(context.Background(), p.DB, "ws-a", inv.ID, "second-remaining")
	if err != nil {
		t.Fatal(err)
	}
	in.WithdrawalID = w.ID
	in.SettlementRevisionID = s.ID
	in.RequestID = uuid.NewString()
	in.RemainingPositionID = "second-remaining"
	in.RemainingCoverageToken = c.CoverageToken
	c2aVersions(t, p.DB, s, &in)
	r := c2aApply(t, p, in)
	if r.ResidualCents != -900 || r.BalanceCents != first.BalanceCents-900 {
		t.Fatal(r)
	}
	financeWant(t, p.DB, 528200, 3)
}

func TestInvestmentSettlementFinancialConstraintsAndTenantFK(t *testing.T) {
	p, _, _, in := c2aFixture(t, 530000, 530000, false, false)
	r := c2aApply(t, p, in)
	// Copy a valid zero-effect event with entirely fresh identity/request keys;
	// the foreign workspace cannot borrow its C1/B origins through SQL either.
	rows, err := p.DB.Query(`PRAGMA table_info(investment_settlement_applications)`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ column, expression string }{{"workspace_id", "'ws-b'"}, {"investment_id", "'foreign-investment'"}, {"account_id", "'acc-inv-b'"}, {"settlement_revision_id", "'foreign-revision'"}, {"financial_revision_id", "'foreign-revision'"}} {
		expressions := append([]string(nil), cols...)
		for i, col := range cols {
			switch col {
			case "id":
				expressions[i] = "'" + uuid.NewString() + "'"
			case "request_id":
				expressions[i] = "'" + uuid.NewString() + "'"
			case "withdrawal_id":
				expressions[i] = "'foreign-withdrawal'"
			}
			if col == change.column {
				expressions[i] = change.expression
			}
		}
		q := `INSERT INTO investment_settlement_applications (` + strings.Join(cols, ",") + `) SELECT ` + strings.Join(expressions, ",") + ` FROM investment_settlement_applications WHERE workspace_id='ws-a' AND id=?`
		if _, err = p.Immediate.Exec(q, r.ID); err == nil {
			t.Fatal("cross-tenant origin accepted", change.column)
		}
	}
	rows, err = p.DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
