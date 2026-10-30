package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

func c2bInput(t *testing.T, p *database.RuntimeDBs, s InvestmentSettlement, a InvestmentSettlementApplication) ClassifyInvestmentSettlementInput {
	t.Helper()
	exec(t, p.Immediate, `INSERT INTO categories(id,workspace_id,name,type,macro_group) VALUES('c2b-gain','ws-a','Resultado realizado','INCOME','Receitas Financeiras'),('c2b-loss','ws-a','Perda realizada','EXPENSE','Financeiro')`)
	in := ClassifyInvestmentSettlementInput{WorkspaceID: "ws-a", InvestmentID: s.InvestmentID, WithdrawalID: s.WithdrawalID, SettlementRevisionID: s.ID, ApplicationID: a.ID, ExpectedSettlementVersion: s.Version, CreatedBy: "user-a", RequestID: uuid.NewString(), LegacyReconciliationEvidence: "Extratos e lançamentos conferidos; sem reconhecimento gerencial prévio do resultado, IR, IOF ou taxas"}
	if s.RealizedResultCents != nil && *s.RealizedResultCents != 0 {
		in.CategoryID = "c2b-gain"
		if *s.RealizedResultCents < 0 {
			in.CategoryID = "c2b-loss"
		}
	}
	return in
}

func c2bFinancialSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"accounts", "financial_transactions", "account_balance_adjustments", "box_virtual_ledger", "investments", "investment_valuations", "investment_purchases", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions", "investment_settlement_receipts", "investment_settlement_applications", "investment_settlement_prior_effects"} {
		order := "rowid"
		if table == "financial_transactions" {
			order = "id"
		}
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY ` + order)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%s:%#v\n", table, vals)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out.String()
}

func c2bClassify(t *testing.T, p *database.RuntimeDBs, in ClassifyInvestmentSettlementInput) InvestmentResultClassification {
	t.Helper()
	r, err := ClassifyInvestmentSettlementResult(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func c2bReject(t *testing.T, p *database.RuntimeDBs, in ClassifyInvestmentSettlementInput, want error) {
	t.Helper()
	before := c2bFinancialSnapshot(t, p.DB)
	n := countRows(t, p.DB, `SELECT COUNT(*) FROM investment_result_classifications`)
	_, err := ClassifyInvestmentSettlementResult(context.Background(), p.Immediate, in)
	if err == nil || want != nil && !errors.Is(err, want) {
		t.Fatalf("want %v got %v", want, err)
	}
	if before != c2bFinancialSnapshot(t, p.DB) || n != countRows(t, p.DB, `SELECT COUNT(*) FROM investment_result_classifications`) {
		t.Fatal("rejection changed persistent state")
	}
}

func TestInvestmentClassificationResultScenarios(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		net, patrimony, result int64
		partial, unknown       bool
		receipt, nature        string
	}{
		{"gain retained", 525500, 530000, 25500, false, false, "", "REALIZED_GAIN"},
		{"gain transferred", 525500, 530000, 25500, false, false, "TRANSFER", "REALIZED_GAIN"},
		{"partial 51", 105100, 106000, 5100, true, false, "", "REALIZED_GAIN"},
		{"loss", 490000, 530000, -10000, false, false, "", "REALIZED_LOSS"},
		{"zero", 500000, 530000, 0, false, false, "", "ZERO"},
		{"unknown", 525500, 530000, 0, false, true, "", "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, s, fin := c2aFixtureWithReceipt(t, tc.net, tc.patrimony, tc.partial, tc.unknown, tc.receipt)
			a := c2aApply(t, p, fin)
			in := c2bInput(t, p, s, a)
			before := c2bFinancialSnapshot(t, p.DB)
			reserveBefore, err := CalculateWorkspaceReserveBalance(p.DB, "ws-a")
			if err != nil {
				t.Fatal(err)
			}
			r := c2bClassify(t, p, in)
			if r.Nature != tc.nature || r.EconomicDate != s.Input.EconomicDate || r.Version != 1 {
				t.Fatal(r)
			}
			if tc.unknown {
				if r.ResultCents != nil || r.State != InvestmentClassificationUnavailable {
					t.Fatal(r)
				}
			} else if r.ResultCents == nil || *r.ResultCents != tc.result || r.State != InvestmentClassificationCompleted {
				t.Fatal(r)
			}
			if before != c2bFinancialSnapshot(t, p.DB) {
				t.Fatal("C2B changed financial snapshot")
			}
			reserveAfter, err := CalculateWorkspaceReserveBalance(p.DB, "ws-a")
			if err != nil || reserveBefore != reserveAfter {
				t.Fatal("reserve changed", err)
			}
			if tc.result == 0 {
				if r.TransactionID != "" {
					t.Fatal("zero/unknown monetary transaction")
				}
			} else {
				var amount, at, created int64
				var typ, mode, day string
				var paid sql.NullInt64
				if err = p.DB.QueryRow(`SELECT amount,type,effect_mode,date,date_civil,created_at,paid_at FROM transactions WHERE workspace_id=? AND id=?`, in.WorkspaceID, r.TransactionID).Scan(&amount, &typ, &mode, &at, &day, &created, &paid); err != nil {
					t.Fatal(err)
				}
				want := tc.result
				expectedType := "INCOME"
				if want < 0 {
					want = -want
					expectedType = "EXPENSE"
				}
				if amount != want || typ != expectedType || mode != models.TransactionEffectClassificationOnly || day != s.Input.EconomicDate || time.Unix(at, 0).UTC().Format("2006-01-02") != day || created <= at || paid.Valid {
					t.Fatal("bad classification", amount, typ, mode, day)
				}
			}
			if again := c2bClassify(t, p, in); !reflect.DeepEqual(again, r) {
				t.Fatal("non-idempotent replay")
			}
			diverged := in
			diverged.LegacyReconciliationEvidence = "Outro payload"
			c2bReject(t, p, diverged, ErrInvestmentConflict)
			diverged = in
			diverged.RequestID = uuid.NewString()
			c2bReject(t, p, diverged, ErrInvestmentConflict)
		})
	}
}

func TestInvestmentClassificationValidationAndIsolation(t *testing.T) {
	p, _, s, fin := c2aFixture(t, 525500, 530000, false, false)
	in := c2bInput(t, p, s, InvestmentSettlementApplication{ID: uuid.NewString()})
	c2bReject(t, p, in, ErrInvestmentSettlementPending)
	a := c2aApply(t, p, fin)
	in.ApplicationID = a.ID
	for _, tc := range []struct {
		name string
		edit func(*ClassifyInvestmentSettlementInput)
		want error
	}{
		{"stale C1", func(i *ClassifyInvestmentSettlementInput) { i.ExpectedSettlementVersion++ }, ErrInvestmentConflict},
		{"wrong C1", func(i *ClassifyInvestmentSettlementInput) { i.SettlementRevisionID = uuid.NewString() }, ErrInvestmentConflict},
		{"foreign workspace", func(i *ClassifyInvestmentSettlementInput) { i.WorkspaceID = "ws-b" }, ErrInvestmentNotFound},
		{"foreign category", func(i *ClassifyInvestmentSettlementInput) { i.CategoryID = "foreign-cat" }, ErrInvestmentNotFound},
		{"expense category for gain", func(i *ClassifyInvestmentSettlementInput) { i.CategoryID = "c2b-loss" }, ErrInvestmentValidation},
		{"invalid key", func(i *ClassifyInvestmentSettlementInput) { i.RequestID = "invalid" }, ErrInvestmentValidation},
		{"wrong version", func(i *ClassifyInvestmentSettlementInput) { i.ExpectedVersion = 1 }, ErrInvestmentValidation},
		{"no legacy evidence", func(i *ClassifyInvestmentSettlementInput) { i.LegacyReconciliationEvidence = "" }, ErrInvestmentValidation},
	} {
		t.Run(tc.name, func(t *testing.T) { v := in; tc.edit(&v); c2bReject(t, p, v, tc.want) })
	}
	c2bClassify(t, p, in)
}

func TestInvestmentClassificationLegacyAmbiguity(t *testing.T) {
	for _, typ := range []string{"INCOME", "EXPENSE"} {
		t.Run(typ, func(t *testing.T) {
			p, _, s, fin := c2aFixture(t, 525500, 530000, false, false)
			a := c2aApply(t, p, fin)
			in := c2bInput(t, p, s, a)
			exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('legacy','ws-a','user-a','acc-inv-a',?,4500,1773576000,'paid')`, typ)
			in.LegacyTransactionIDs = []string{"legacy"}
			c2bReject(t, p, in, ErrInvestmentLegacyClassification)
		})
	}
	t.Run("C2A known paid tax", func(t *testing.T) {
		p, _, s, fin := c2aFixture(t, 525500, 530000, false, false)
		at := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC).Unix()
		exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,paid_at,status) VALUES('tax','ws-a','user-a','acc-inv-a','EXPENSE',4500,?,?,'paid')`, at, at)
		tx, err := p.Immediate.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = ApplyBalanceEffect(tx, "ws-a", "EXPENSE", "INVESTMENT", "paid", 4500, "acc-inv-a", "", at); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		fin.PriorEffects = []InvestmentSettlementPriorEffect{{TransactionID: "tax", DeltaCents: -4500, Evidence: "IR da mesma liquidação"}}
		c2aVersions(t, p.DB, s, &fin)
		a := c2aApply(t, p, fin)
		in := c2bInput(t, p, s, a)
		c2bReject(t, p, in, ErrInvestmentLegacyClassification)
	})
	t.Run("C1 legacy receipt", func(t *testing.T) {
		p, _, s, fin := c2aFixtureWithReceipt(t, 525500, 530000, false, false, "INCOME")
		c2aReject(t, p, fin, ErrInvestmentLegacyIncome)
		in := c2bInput(t, p, s, InvestmentSettlementApplication{ID: uuid.NewString()})
		c2bReject(t, p, in, ErrInvestmentSettlementPending)
	})
}

func TestInvestmentClassificationConcurrentAndRollback(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		t.Run(fmt.Sprint("concurrent same key=", sameKey), func(t *testing.T) {
			p, _, s, fin := c2aFixture(t, 525500, 530000, false, false)
			a := c2aApply(t, p, fin)
			in := c2bInput(t, p, s, a)
			before := c2bFinancialSnapshot(t, p.DB)
			start := make(chan struct{})
			var wg sync.WaitGroup
			errs := make(chan error, 2)
			results := make(chan InvestmentResultClassification, 2)
			for i := 0; i < 2; i++ {
				v := in
				if !sameKey {
					v.RequestID = uuid.NewString()
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					r, err := ClassifyInvestmentSettlementResult(context.Background(), p.Immediate, v)
					errs <- err
					results <- r
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			close(results)
			success, conflict := 0, 0
			for err := range errs {
				if err == nil {
					success++
				} else if errors.Is(err, ErrInvestmentConflict) {
					conflict++
				} else {
					t.Fatal(err)
				}
			}
			if sameKey && success != 2 || !sameKey && (success != 1 || conflict != 1) {
				t.Fatal(success, conflict)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_result_classifications`) != 1 || countRows(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE effect_mode='CLASSIFICATION_ONLY'`) != 1 || before != c2bFinancialSnapshot(t, p.DB) {
				t.Fatal("duplicate or financial mutation")
			}
		})
	}
	t.Run("transaction insert fails", func(t *testing.T) {
		p, _, s, fin := c2aFixture(t, 525500, 530000, false, false)
		a := c2aApply(t, p, fin)
		in := c2bInput(t, p, s, a)
		exec(t, p.Immediate, `CREATE TRIGGER fail_c2b BEFORE INSERT ON transactions WHEN NEW.effect_mode='CLASSIFICATION_ONLY' BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
		c2bReject(t, p, in, nil)
		if countRows(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE effect_mode='CLASSIFICATION_ONLY'`) != 0 {
			t.Fatal("partial transaction")
		}
	})
}

func TestInvestmentClassificationBalanceModesAndProtection(t *testing.T) {
	p, _, s, fin := c2aFixture(t, 525500, 530000, false, false)
	a := c2aApply(t, p, fin)
	in := c2bInput(t, p, s, a)
	r := c2bClassify(t, p, in)
	before := c2bFinancialSnapshot(t, p.DB)
	tx, err := p.Immediate.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = ApplyBalanceEffect(tx, "ws-a", "INCOME", "INVESTMENT", "paid", 25500, "acc-inv-a", "", time.Now().Unix(), models.TransactionEffectClassificationOnly); err != nil {
		t.Fatal(err)
	}
	if err = ReverseBalanceEffect(tx, "ws-a", "INCOME", "INVESTMENT", 25500, "acc-inv-a", "", time.Now().Unix(), models.TransactionEffectClassificationOnly); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if before != c2bFinancialSnapshot(t, p.DB) {
		t.Fatal("classification balance effect")
	}
	for _, q := range []string{
		`UPDATE transactions SET amount=1 WHERE workspace_id='ws-a' AND id=?`,
		`UPDATE transactions SET status='pending' WHERE workspace_id='ws-a' AND id=?`,
		`UPDATE transactions SET recurring_rule_id='rule' WHERE workspace_id='ws-a' AND id=?`,
		`UPDATE transactions SET effect_mode='STANDARD' WHERE workspace_id='ws-a' AND id=?`,
		`DELETE FROM transactions WHERE workspace_id='ws-a' AND id=?`,
		`UPDATE investment_result_classifications SET state='UNAVAILABLE' WHERE workspace_id='ws-a' AND transaction_id=?`,
		`DELETE FROM investment_result_classifications WHERE workspace_id='ws-a' AND transaction_id=?`,
	} {
		if _, err = p.Immediate.Exec(q, r.TransactionID); err == nil {
			t.Fatal("mutation allowed", q)
		}
	}
	if _, err = p.Immediate.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,effect_mode) VALUES('free','ws-a','user-a','acc-inv-a','INCOME',25500,1,'CLASSIFICATION_ONLY')`); err == nil {
		t.Fatal("free classification allowed")
	}
	if _, err = p.Immediate.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,effect_mode) SELECT 'clone',workspace_id,user_id,account_id,type,amount,date,effect_mode FROM transactions WHERE workspace_id='ws-a' AND id=?`, r.TransactionID); err == nil {
		t.Fatal("classification cloned")
	}
	if before != c2bFinancialSnapshot(t, p.DB) {
		t.Fatal("protected mutation affected finances")
	}
	exec(t, p.Immediate, `INSERT INTO boxes(id,workspace_id,name,category_id,target_amount) VALUES('c2b-box','ws-a','Reserva','c2b-loss',100000)`)
	if _, err = p.Immediate.Exec(`INSERT INTO box_virtual_ledger(id,box_id,amount,type,source_transaction_id,reference_date) VALUES('c2b-consume','c2b-box',-25500,'CONSUME',?,1)`, r.TransactionID); err == nil {
		t.Fatal("classification moved reserves")
	}
	exec(t, p.Immediate, `INSERT INTO box_virtual_ledger(id,box_id,amount,type,reference_date) VALUES('ordinary-bonus','c2b-box',100,'BONUS',1)`)
	if _, err = p.Immediate.Exec(`UPDATE box_virtual_ledger SET source_transaction_id=? WHERE id='ordinary-bonus'`, r.TransactionID); err == nil {
		t.Fatal("classification reserve update allowed")
	}
	// STANDARD still applies and reverses exactly once, including omitted mode.
	tx, err = p.Immediate.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = ApplyBalanceEffect(tx, "ws-a", "INCOME", "INVESTMENT", "paid", 123, "acc-inv-a", "", 1, models.TransactionEffectStandard); err != nil {
		t.Fatal(err)
	}
	var bal int64
	if err = tx.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&bal); err != nil || bal != 525623 {
		t.Fatal(bal, err)
	}
	if err = ReverseBalanceEffect(tx, "ws-a", "INCOME", "INVESTMENT", 123, "acc-inv-a", "", 1); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&bal); err != nil || bal != 525500 {
		t.Fatal(bal, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestInvestmentClassificationNetOfIRIOFFees(t *testing.T) {
	p, _, _ := newInvestmentLifecycleDB(t)
	ci := investmentCreateInput("ws-a", "acc-inv-a")
	ci.PrincipalCents = 500000
	inv := mustCreateInvestment(t, p.Immediate, ci)
	exec(t, p.Immediate, `UPDATE accounts SET initial_balance=500000,current_balance=500000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	root, _ := financeRoot(t, p, inv)
	b := financeIntegrate(t, p, inv, root, "costs-value", "2026-03-01", 530000, 1)
	w := settlementWithdrawalFixture(t, p, inv, 525500, withdrawalPrincipal(500000), InvestmentWithdrawalTotal)
	si := settlementInput(w)
	si.GrossCents = withdrawalPrincipal(530000)
	si.IRCents = withdrawalPrincipal(3000)
	si.IOFCents = withdrawalPrincipal(1000)
	si.FeesCents = withdrawalPrincipal(500)
	si.PatrimonyCents = withdrawalPrincipal(530000)
	si.ConfirmReceipt = true
	si.ReceiptEvidence = "Líquido comprovado após três descontos"
	s := settlementMust(t, p, si)
	fin := ApplyInvestmentSettlementInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, WithdrawalID: w.ID, SettlementRevisionID: s.ID, ReferenceRevisionID: root, FinancialRevisionID: b.ID, ExpectedSettlementVersion: s.Version, ExpectedFinancialVersion: b.Version, CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Custos comprovados", ReconciliationEvidence: "Sem débitos prévios"}
	c2aVersions(t, p.DB, s, &fin)
	a := c2aApply(t, p, fin)
	in := c2bInput(t, p, s, a)
	before := c2bFinancialSnapshot(t, p.DB)
	r := c2bClassify(t, p, in)
	if r.ResultCents == nil || *r.ResultCents != 25500 || countRows(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-a' AND type='EXPENSE'`) != 0 || before != c2bFinancialSnapshot(t, p.DB) {
		t.Fatal("double deduction or financial mutation", r)
	}
}

func TestInvestmentClassificationNotActivatedByHTTPOrJobs(t *testing.T) {
	for _, root := range []string{"../../cmd/server", "../handlers", "../jobs"} {
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
			calls := strings.Count(string(b), "ClassifyInvestmentSettlementResult(")
			if filepath.Clean(path) == filepath.Clean("../handlers/investment_settlement_effects.go") {
				if calls != 1 {
					t.Errorf("expected one explicit O1B2 C2B invocation: %s", path)
				}
				return nil // The O1B2 HTTP suite verifies opt-in and explicit POST.
			}
			if calls != 0 {
				t.Errorf("automatic C2B activation: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
