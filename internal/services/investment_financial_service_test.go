package services

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

// The helper must leave both successful and failed writes under the caller's
// transaction. Full snapshots also cover the foreign workspace and C1/C2 rows.
func TestInvestmentFinancialTxCallerOwnsTransaction(t *testing.T) {
	for _, mode := range []string{"root", "subsequent", "correction", "funding", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			p, _, inv := newInvestmentLifecycleDB(t)
			exec(t, p.Immediate, `UPDATE accounts SET initial_balance=500000,current_balance=500000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
			insertValuationRow(t, p.Immediate, "root:"+inv.ID, "ws-a", inv.ID, "2026-02-01", 500000)
			a := referenceInput(t, p.DB, inv.ID, "root:"+inv.ID)
			a.RepresentedCents = withdrawalPrincipal(500000)
			root, err := RecordInvestmentReference(ctx, p.Immediate, a)
			if err != nil {
				t.Fatal(err)
			}
			in := financeInput(t, p.DB, inv.ID, "root:"+inv.ID, root.ID, 0)
			wantBalance, wantDelta, wantVersion := int64(500000), int64(0), int64(1)
			if mode != "root" {
				if _, err = IntegrateInvestmentPosition(ctx, p.Immediate, in); err != nil {
					t.Fatal(err)
				}
				if mode == "funding" {
					financeTransfer(t, p.Immediate, "funds", "2026-03-01", 200000)
					purchase, e := CreateInvestmentPurchase(ctx, p.Immediate, investmentPurchaseInput(inv.ID))
					if e != nil {
						t.Fatal(e)
					}
					in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "funds"}}
				}
				value := int64(530000)
				if mode == "funding" {
					value = 730000
				}
				insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", value)
				funding := in.Funding
				in = financeInput(t, p.DB, inv.ID, "next", root.ID, 1)
				in.Funding = funding
				wantBalance, wantDelta, wantVersion = value, 30000, 2
				if mode == "correction" {
					if _, err = IntegrateInvestmentPosition(ctx, p.Immediate, in); err != nil {
						t.Fatal(err)
					}
					in = financeInput(t, p.DB, inv.ID, "next", root.ID, 2)
					in.CorrectionCents = withdrawalPrincipal(520000)
					wantBalance, wantDelta, wantVersion = 520000, -10000, 3
				}
			}
			if mode == "write failure" {
				exec(t, p.Immediate, `CREATE TRIGGER fail_finance_tx BEFORE INSERT ON investment_financial_revisions BEGIN SELECT RAISE(ABORT,'injected'); END`)
			}
			before := adoptionSnapshot(t, p.DB)
			payload, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := p.Immediate.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			got, callErr := integrateInvestmentPositionTx(ctx, tx, in, payload)
			if mode == "write failure" {
				if callErr == nil || !reflect.DeepEqual(got, InvestmentIntegrationResult{}) {
					t.Fatalf("write failure returned success: %+v %v", got, callErr)
				}
			} else {
				if callErr != nil || got.BalanceCents != wantBalance || got.NetAdjustmentCents != wantDelta || got.Version != wantVersion {
					t.Fatalf("helper result: %+v %v", got, callErr)
				}
				var saved string
				if err = tx.QueryRowContext(ctx, `SELECT result_json FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? AND request_id=?`, in.WorkspaceID, in.InvestmentID, in.RequestID).Scan(&saved); err != nil {
					t.Fatal(err)
				}
				var receipt InvestmentIntegrationResult
				if err = json.Unmarshal([]byte(saved), &receipt); err != nil || !reflect.DeepEqual(got, receipt) {
					t.Fatalf("helper receipt differs: %+v %v", receipt, err)
				}
			}
			var balance int64
			if err = tx.QueryRowContext(ctx, `SELECT current_balance FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, in.AccountID).Scan(&balance); err != nil || balance != wantBalance {
				t.Fatalf("caller lost transaction or pending writes: %d %v", balance, err)
			}
			if before != adoptionSnapshot(t, p.DB) {
				t.Fatal("helper committed writes visible to another connection")
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if before != adoptionSnapshot(t, p.DB) {
				t.Fatal("caller rollback did not restore complete snapshot")
			}
			out, err := IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, in)
			if mode == "write failure" {
				if err == nil || before != adoptionSnapshot(t, p.DB) {
					t.Fatalf("wrapper failed to roll back: %v", err)
				}
				return
			}
			if err != nil || out.Outcome != InvestmentOperationCreated || out.BalanceCents != got.BalanceCents || out.BalanceVersion != got.BalanceVersion || out.NetAdjustmentCents != got.NetAdjustmentCents || out.Version != got.Version || len(out.AdjustmentIDs) != len(got.AdjustmentIDs) {
				t.Fatalf("wrapper/helper financial result differs: %+v %+v %v", out, got, err)
			}
			after := adoptionSnapshot(t, p.DB)
			replay, err := IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, in)
			if err != nil || replay.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(out.InvestmentIntegrationResult, replay.InvestmentIntegrationResult) || after != adoptionSnapshot(t, p.DB) {
				t.Fatalf("replay changed receipt or snapshot: %+v %v", replay, err)
			}
		})
	}
}

// Reuse the real SQLite connector, changing only the final commit outcome.
type financialCommitFailureConnector struct {
	adoptionFailCommitConnector
	durable bool
}

func (c *financialCommitFailureConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &financialCommitFailureConn{Conn: conn, durable: c.durable}, nil
}

type financialCommitFailureConn struct {
	driver.Conn
	durable bool
}

func (c *financialCommitFailureConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &financialCommitFailureTx{Tx: tx, durable: c.durable}, nil
}

type financialCommitFailureTx struct {
	driver.Tx
	durable bool
}

func (tx *financialCommitFailureTx) Commit() error {
	if tx.durable {
		if err := tx.Tx.Commit(); err != nil {
			return err
		}
	} else if err := tx.Tx.Rollback(); err != nil {
		return err
	}
	return errors.New("injected uncertain financial commit")
}

func TestInvestmentFinancialCommitFailureAndRecovery(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, replay := range []bool{false, true} {
			t.Run(fmt.Sprintf("durable=%t/replay=%t", durable, replay), func(t *testing.T) {
				p, path, inv, root := financeFixture(t)
				insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", 530000)
				in := financeInput(t, p.DB, inv.ID, "next", root, 1)
				ctx := context.Background()
				var original InvestmentIntegrationOperationResult
				var err error
				if replay {
					original, err = IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, in)
					if err != nil {
						t.Fatal(err)
					}
				}
				db := sql.OpenDB(&financialCommitFailureConnector{adoptionFailCommitConnector{path + "?_txlock=immediate&_pragma=foreign_keys(1)"}, durable})
				db.SetMaxOpenConns(1)
				defer db.Close()
				before := adoptionSnapshot(t, p.DB)
				got, err := IntegrateInvestmentPositionWithOutcome(ctx, db, in)
				var commitErr *InvestmentCommitError
				if !errors.As(err, &commitErr) || !reflect.DeepEqual(got, InvestmentIntegrationOperationResult{}) {
					t.Fatalf("commit error returned success: %+v %v", got, err)
				}
				if (!durable || replay) && before != adoptionSnapshot(t, p.DB) {
					t.Fatal("failed commit or replay changed complete snapshot")
				}
				recovered, err := IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, in)
				wantOutcome := InvestmentOperationCreated
				if durable || replay {
					wantOutcome = InvestmentOperationReplayed
				}
				if err != nil || recovered.Outcome != wantOutcome || recovered.Version != 2 || recovered.NetAdjustmentCents != 30000 {
					t.Fatalf("retry after uncertain commit: %+v %v", recovered, err)
				}
				if replay && !reflect.DeepEqual(original.InvestmentIntegrationResult, recovered.InvestmentIntegrationResult) {
					t.Fatal("recovery lost original receipt")
				}
				financeWant(t, p.DB, 530000, 1)
				after := adoptionSnapshot(t, p.DB)
				again, err := IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, in)
				if err != nil || again.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(recovered.InvestmentIntegrationResult, again.InvestmentIntegrationResult) || after != adoptionSnapshot(t, p.DB) {
					t.Fatalf("recovery duplicated effects: %+v %v", again, err)
				}
			})
		}
	}
}

func financeInput(t *testing.T, db *sql.DB, inv, position, reference string, version int64) IntegrateInvestmentPositionInput {
	t.Helper()
	c, err := ReadInvestmentReferenceCandidate(context.Background(), db, "ws-a", inv, position)
	if err != nil {
		t.Fatal(err)
	}
	return IntegrateInvestmentPositionInput{WorkspaceID: "ws-a", InvestmentID: inv, AccountID: "acc-inv-a", ReferenceRevisionID: reference, PositionID: position, CoverageToken: c.CoverageToken, CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Posição conciliada", ExpectedVersion: version, DayClosed: true}
}

func financeRoot(t *testing.T, pools *database.RuntimeDBs, inv models.Investment) (string, InvestmentIntegrationResult) {
	t.Helper()
	insertValuationRow(t, pools.Immediate, "root:"+inv.ID, "ws-a", inv.ID, "2026-02-01", 500000)
	r := referenceInput(t, pools.DB, inv.ID, "root:"+inv.ID)
	r.RepresentedCents = withdrawalPrincipal(500000)
	root, err := RecordInvestmentReference(context.Background(), pools.Immediate, r)
	if err != nil {
		t.Fatal(err)
	}
	result, err := IntegrateInvestmentPosition(context.Background(), pools.Immediate, financeInput(t, pools.DB, inv.ID, "root:"+inv.ID, root.ID, 0))
	if err != nil {
		t.Fatal(err)
	}
	return root.ID, result
}

func financeFixture(t *testing.T) (*database.RuntimeDBs, string, models.Investment, string) {
	t.Helper()
	pools, path, inv := newInvestmentLifecycleDB(t)
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=500000,current_balance=500000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	before := investmentPurchasesFinancialState(t, pools.DB)
	root, result := financeRoot(t, pools, inv)
	if result.NetAdjustmentCents != 0 || result.BalanceCents != 500000 || len(result.AdjustmentIDs) != 0 || before != investmentPurchasesFinancialState(t, pools.DB) {
		t.Fatal("initial reference changed finances")
	}
	return pools, path, inv, root
}

func financeWant(t *testing.T, db *sql.DB, balance, adjustments int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(`SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != balance || countRows(t, db, `SELECT COUNT(*) FROM account_balance_adjustments`) != int(adjustments) {
		t.Fatalf("balance=%d adjustments=%d want %d/%d", got, countRows(t, db, `SELECT COUNT(*) FROM account_balance_adjustments`), balance, adjustments)
	}
}

func financeIntegrate(t *testing.T, p *database.RuntimeDBs, inv models.Investment, root, id, date string, value, version int64) InvestmentIntegrationResult {
	t.Helper()
	insertValuationRow(t, p.Immediate, id, "ws-a", inv.ID, date, value)
	r, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, financeInput(t, p.DB, inv.ID, id, root, version))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func financeTransfer(t *testing.T, db *sql.DB, id, date string, amount int64) {
	t.Helper()
	at, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,paid_at,status) VALUES(?,'ws-a','user-a','acc-checking-a','acc-inv-a','TRANSFER',?,?,?,'paid')`, id, amount, at.Unix(), at.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyBalanceEffect(tx, "ws-a", "TRANSFER", "CHECKING", "paid", amount, "acc-checking-a", "acc-inv-a", at.Unix()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestInvestmentFinancialSimpleChangesAndPreservation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		value, delta int64
	}{{"appreciation", 530000, 30000}, {"depreciation", 480000, -20000}, {"zero", 500000, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			p, _, inv, root := financeFixture(t)
			controls := queryScalar(t, p.DB, `SELECT group_concat(id||':'||current_balance) FROM (SELECT id,current_balance FROM accounts WHERE id!='acc-inv-a' ORDER BY id)`)
			r := financeIntegrate(t, p, inv, root, "next", "2026-03-01", tc.value, 1)
			n := int64(1)
			if tc.delta == 0 {
				n = 0
			}
			financeWant(t, p.DB, tc.value, n)
			if r.NetAdjustmentCents != tc.delta || countRows(t, p.DB, `SELECT COUNT(*) FROM transactions`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM box_virtual_ledger`) != 0 {
				t.Fatal("incorrect effect or income")
			}
			if controls != queryScalar(t, p.DB, `SELECT group_concat(id||':'||current_balance) FROM (SELECT id,current_balance FROM accounts WHERE id!='acc-inv-a' ORDER BY id)`) {
				t.Fatal("other balances changed")
			}
		})
	}
}

func TestInvestmentFinancialFundingAndSameDay(t *testing.T) {
	for _, mode := range []string{"funded", "missing proof", "pending transfer", "wrong amount", "foreign transfer", "no close", "stale token", "edited funding"} {
		t.Run(mode, func(t *testing.T) {
			p, _, inv, root := financeFixture(t)
			financeTransfer(t, p.Immediate, "funds", "2026-03-01", 200000)
			purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(inv.ID))
			if err != nil {
				t.Fatal(err)
			}
			financeWant(t, p.DB, 700000, 0)
			insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", 730000)
			in := financeInput(t, p.DB, inv.ID, "next", root, 1)
			in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "funds"}}
			switch mode {
			case "missing proof":
				in.Funding = nil
			case "pending transfer":
				exec(t, p.Immediate, `UPDATE transactions SET status='pending' WHERE workspace_id='ws-a' AND id='funds'`)
			case "wrong amount":
				exec(t, p.Immediate, `UPDATE transactions SET amount=199999 WHERE workspace_id='ws-a' AND id='funds'`)
			case "foreign transfer":
				in.Funding[0].TransferID = "foreign"
			case "no close":
				in.DayClosed = false
			case "stale token":
				in.CoverageToken = root + root
			}
			before := investmentPurchasesFinancialState(t, p.DB)
			r, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			if mode != "funded" && mode != "edited funding" {
				if err == nil || before != investmentPurchasesFinancialState(t, p.DB) {
					t.Fatalf("unsafe funding accepted %v", err)
				}
				return
			}
			if err != nil || r.NetAdjustmentCents != 30000 {
				t.Fatalf("funded: %+v %v", r, err)
			}
			financeWant(t, p.DB, 730000, 1)
			if countRows(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE type='INCOME'`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_purchase_funding`) != 1 {
				t.Fatal("income or funding duplicated")
			}
			if mode == "edited funding" {
				exec(t, p.Immediate, `UPDATE transactions SET amount=1 WHERE workspace_id='ws-a' AND id='funds'`)
				insertValuationRow(t, p.Immediate, "later", "ws-a", inv.ID, "2026-04-01", 740000)
				_, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, financeInput(t, p.DB, inv.ID, "later", root, 2))
				if err == nil {
					t.Fatal("stale transfer evidence accepted")
				}
				financeWant(t, p.DB, 730000, 1)
			}
		})
	}
}

func TestInvestmentFinancialCorrectionsAndAudit(t *testing.T) {
	for _, middle := range []bool{false, true} {
		t.Run(map[bool]string{false: "last", true: "intermediate"}[middle], func(t *testing.T) {
			p, _, inv, root := financeFixture(t)
			first := financeIntegrate(t, p, inv, root, "first", "2026-03-01", 530000, 1)
			version := int64(2)
			if middle {
				financeIntegrate(t, p, inv, root, "second", "2026-04-01", 540000, 2)
				version = 3
			}
			in := financeInput(t, p.DB, inv.ID, "first", root, version)
			in.CorrectionCents = withdrawalPrincipal(520000)
			r, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			if err != nil {
				t.Fatal(err)
			}
			want, count, delta := int64(520000), int64(3), int64(-10000)
			if middle {
				want, count, delta = 540000, 6, 0
			}
			financeWant(t, p.DB, want, count)
			if r.NetAdjustmentCents != delta {
				t.Fatal(r)
			}
			var original int64
			if err = p.DB.QueryRow(`SELECT delta_cents FROM account_balance_adjustments WHERE workspace_id='ws-a' AND id=?`, first.AdjustmentIDs[0]).Scan(&original); err != nil || original != 30000 {
				t.Fatal("original overwritten", err)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_adjustments WHERE reverses_id IS NOT NULL`) != map[bool]int{false: 1, true: 2}[middle] {
				t.Fatal("missing compensations")
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_valuations WHERE id='first' AND amount_cents=520000`) != 1 {
				t.Fatal("correction missing")
			}
			var chainJSON string
			if err = p.DB.QueryRow(`SELECT chain_json FROM investment_financial_revisions WHERE workspace_id='ws-a' AND investment_id=? ORDER BY version DESC LIMIT 1`, inv.ID).Scan(&chainJSON); err != nil {
				t.Fatal(err)
			}
			if middle && countRows(t, p.DB, `SELECT COUNT(*) FROM account_balance_adjustments WHERE delta_cents=20000`) != 2 {
				t.Fatal("dependent interval not recomputed", chainJSON)
			}
			// Retry uses persisted receipt even after correction changed its source token.
			again, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			if err != nil || !reflect.DeepEqual(r, again) {
				t.Fatal("correction replay", err)
			}
		})
	}
}

func TestInvestmentFinancialStaleAndWithdrawalBlock(t *testing.T) {
	for _, mode := range []string{"retroactive purchase", "same-day purchase", "withdrawal", "changed dependency", "new reference"} {
		t.Run(mode, func(t *testing.T) {
			p, _, inv, root := financeFixture(t)
			financeIntegrate(t, p, inv, root, "first", "2026-03-01", 530000, 1)
			switch mode {
			case "retroactive purchase", "same-day purchase":
				in := investmentPurchaseInput(inv.ID)
				in.PurchaseDate = "2026-02-15"
				if mode == "same-day purchase" {
					in.PurchaseDate = "2026-03-01"
				}
				if _, err := CreateInvestmentPurchase(context.Background(), p.Immediate, in); err != nil {
					t.Fatal(err)
				}
			case "withdrawal":
				w := withdrawalInput(inv.ID, "initial:"+inv.ID)
				w.WithdrawalDate = "2026-03-15"
				if _, err := CreateInvestmentWithdrawal(context.Background(), p.Immediate, w); err != nil {
					t.Fatal(err)
				}
			case "changed dependency":
				exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=520000 WHERE id='first' AND workspace_id='ws-a'`)
			case "new reference":
				r := referenceInput(t, p.DB, inv.ID, "root:"+inv.ID)
				r.ExpectedVersion = 1
				r.RepresentedCents = withdrawalPrincipal(500000)
				if _, err := RecordInvestmentReference(context.Background(), p.Immediate, r); err != nil {
					t.Fatal(err)
				}
			}
			insertValuationRow(t, p.Immediate, "later", "ws-a", inv.ID, "2026-04-01", 540000)
			before := investmentPurchasesFinancialState(t, p.DB)
			_, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, financeInput(t, p.DB, inv.ID, "later", root, 2))
			if err == nil || before != investmentPurchasesFinancialState(t, p.DB) {
				t.Fatalf("stale/withdrawal accepted %v", err)
			}
			if mode == "withdrawal" && !errors.Is(err, ErrInvestmentSettlementPending) {
				t.Fatalf("missing pending settlement: %v", err)
			}
			financeWant(t, p.DB, 530000, 1)
		})
	}
}

func TestInvestmentFinancialIdempotencyIsolationAndVersions(t *testing.T) {
	p, _, inv, root := financeFixture(t)
	insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", 530000)
	in := financeInput(t, p.DB, inv.ID, "next", root, 1)
	for _, mode := range []string{"workspace", "account", "reference", "position", "version", "max version"} {
		t.Run(mode, func(t *testing.T) {
			bad := in
			switch mode {
			case "workspace":
				bad.WorkspaceID = "ws-b"
			case "account":
				bad.AccountID = "acc-inv-b"
			case "reference":
				bad.ReferenceRevisionID = "missing"
			case "position":
				bad.PositionID = "foreign"
			case "version":
				bad.ExpectedVersion = 0
			case "max version":
				bad.ExpectedVersion = math.MaxInt64
			}
			if _, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, bad); err == nil {
				t.Fatal("foreign/stale accepted")
			}
			financeWant(t, p.DB, 500000, 0)
		})
	}
	r, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	// Reload through a separately opened writer: receipt survives process memory.
	replay, err := IntegrateInvestmentPosition(context.Background(), p.DB, in)
	if err != nil || !reflect.DeepEqual(r, replay) {
		t.Fatal("persistent replay", err)
	}
	withOutcome, err := IntegrateInvestmentPositionWithOutcome(context.Background(), p.Immediate, in)
	if err != nil || withOutcome.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(r, withOutcome.InvestmentIntegrationResult) {
		t.Fatalf("legacy persisted receipt replay: %+v %v", withOutcome, err)
	}
	in.Reason = "divergent"
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("divergent replay", err)
	}
	financeWant(t, p.DB, 530000, 1)
}

func TestInvestmentFinancialRollbackAndOverflow(t *testing.T) {
	for _, mode := range []string{"adjustment failure", "account failure", "link failure", "revision failure", "overflow", "version exhausted", "correction failure"} {
		t.Run(mode, func(t *testing.T) {
			p, _, inv, root := financeFixture(t)
			insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", 530000)
			in := financeInput(t, p.DB, inv.ID, "next", root, 1)
			switch mode {
			case "adjustment failure":
				exec(t, p.Immediate, `CREATE TRIGGER fail_finance BEFORE INSERT ON account_balance_adjustments BEGIN SELECT RAISE(ABORT,'injected'); END`)
			case "account failure":
				exec(t, p.Immediate, `CREATE TRIGGER fail_finance BEFORE UPDATE OF current_balance ON accounts BEGIN SELECT RAISE(ABORT,'injected'); END`)
			case "link failure":
				exec(t, p.Immediate, `CREATE TRIGGER fail_finance BEFORE INSERT ON investment_financial_adjustments BEGIN SELECT RAISE(ABORT,'injected'); END`)
			case "revision failure":
				exec(t, p.Immediate, `CREATE TRIGGER fail_finance BEFORE INSERT ON investment_financial_revisions BEGIN SELECT RAISE(ABORT,'injected'); END`)
			case "overflow":
				exec(t, p.Immediate, `UPDATE accounts SET current_balance=? WHERE workspace_id='ws-a' AND id='acc-inv-a'`, int64(math.MaxInt64))
			case "version exhausted":
				exec(t, p.Immediate, `UPDATE accounts SET balance_version=? WHERE workspace_id='ws-a' AND id='acc-inv-a'`, int64(math.MaxInt64))
			case "correction failure":
				if _, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err != nil {
					t.Fatal(err)
				}
				in = financeInput(t, p.DB, inv.ID, "next", root, 2)
				in.CorrectionCents = withdrawalPrincipal(520000)
				exec(t, p.Immediate, `CREATE TRIGGER fail_finance BEFORE INSERT ON investment_financial_adjustments BEGIN SELECT RAISE(ABORT,'injected'); END`)
			}
			before := investmentPurchasesFinancialState(t, p.DB)
			revisions := countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions`)
			adjustments := countRows(t, p.DB, `SELECT COUNT(*) FROM account_balance_adjustments`)
			_, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			if err == nil || before != investmentPurchasesFinancialState(t, p.DB) || revisions != countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions`) || adjustments != countRows(t, p.DB, `SELECT COUNT(*) FROM account_balance_adjustments`) {
				t.Fatalf("partial commit %v", err)
			}
			if (mode == "overflow" || mode == "version exhausted") && !errors.Is(err, ErrBalanceOutOfRange) {
				t.Fatal(err)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_valuations WHERE id='next' AND amount_cents=530000`) != 1 {
				t.Fatal("failed correction changed source")
			}
		})
	}
}

func TestInvestmentFinancialConcurrentApplications(t *testing.T) {
	p, path, inv, root := financeFixture(t)
	other := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	otherRoot, _ := financeRoot(t, p, other)
	insertValuationRow(t, p.Immediate, "one", "ws-a", inv.ID, "2026-03-01", 530000)
	insertValuationRow(t, p.Immediate, "two", "ws-a", other.ID, "2026-03-01", 480000)
	a := financeInput(t, p.DB, inv.ID, "one", root, 1)
	b := financeInput(t, p.DB, other.ID, "two", otherRoot, 1)
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
	defer first.Close()
	second := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeBegin: func() { close(atBegin) }})
	defer second.Close()
	done := make(chan error, 2)
	go func() { _, err := IntegrateInvestmentPosition(ctx, first, a); done <- err }()
	select {
	case <-atCommit:
	case <-ctx.Done():
		t.Fatal("first blocked")
	}
	go func() { _, err := IntegrateInvestmentPosition(ctx, second, b); done <- err }()
	select {
	case <-atBegin:
	case <-ctx.Done():
		t.Fatal("second blocked")
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	financeWant(t, p.DB, 510000, 2)
	if countRows(t, p.DB, `SELECT COUNT(DISTINCT expected_version) FROM account_balance_adjustments`) != 2 {
		t.Fatal("lost account version")
	}
}

func TestInvestmentFinancialLegacyAndUnknownBaseline(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	before := investmentPurchasesFinancialState(t, p.DB)
	v, err := UpsertInvestmentValuation(context.Background(), p.Immediate, UpsertInvestmentValuationInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", ValuationDate: "2026-02-01", AmountCents: 530000})
	if err != nil {
		t.Fatal(err)
	}
	r := referenceInput(t, p.DB, inv.ID, v.ID) // represents 100000, not the 530000 position
	root, err := RecordInvestmentReference(context.Background(), p.Immediate, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, financeInput(t, p.DB, inv.ID, v.ID, root.ID, 0)); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("unknown historical difference integrated", err)
	}
	if before != investmentPurchasesFinancialState(t, p.DB) || countRows(t, p.DB, `SELECT COUNT(*) FROM account_balance_adjustments`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions`) != 0 {
		t.Fatal("legacy finances changed")
	}
}

func TestInvestmentFinancialFundingCustodyAndImmutableEvents(t *testing.T) {
	p, _, inv, root := financeFixture(t)
	financeTransfer(t, p.Immediate, "funds", "2026-03-01", 200000)
	exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('legacy-income','ws-a','user-a','acc-checking-a','INCOME',123,1760000000,'paid'),('legacy-expense','ws-a','user-a','acc-checking-a','EXPENSE',45,1760000000,'paid')`)
	purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(inv.ID))
	if err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", 730000)
	in := financeInput(t, p.DB, inv.ID, "next", root, 1)
	in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "funds"}}
	ledger := queryScalar(t, p.DB, `SELECT group_concat(json_object('id',id,'amount',amount,'type',type,'status',status,'account',account_id,'dest',destination_account_id)) FROM (SELECT * FROM transactions ORDER BY id)`)
	exec(t, p.Immediate, `CREATE TRIGGER fail_funding BEFORE INSERT ON investment_purchase_funding BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err == nil {
		t.Fatal("funding failure accepted")
	}
	financeWant(t, p.DB, 700000, 0)
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions`) != 1 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_adjustments`) != 0 {
		t.Fatal("orphan integration after funding failure")
	}
	exec(t, p.Immediate, `DROP TRIGGER fail_funding`)
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err != nil {
		t.Fatal(err)
	}
	if ledger != queryScalar(t, p.DB, `SELECT group_concat(json_object('id',id,'amount',amount,'type',type,'status',status,'account',account_id,'dest',destination_account_id)) FROM (SELECT * FROM transactions ORDER BY id)`) {
		t.Fatal("transactions changed")
	}
	for _, table := range []string{"investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding"} {
		for _, query := range []string{`DELETE FROM ` + table, `INSERT OR REPLACE INTO ` + table + ` SELECT * FROM ` + table, `UPDATE ` + table + ` SET workspace_id=workspace_id`} {
			if _, err = p.Immediate.Exec(query); err == nil {
				t.Fatal("audit mutation accepted", query)
			}
		}
	}
	financeWant(t, p.DB, 730000, 1)
	// The same transfer cannot fund an independent application.
	other := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	otherRoot, _ := financeRoot(t, p, other)
	otherPurchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(other.ID))
	if err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "other-end", "ws-a", other.ID, "2026-03-01", 730000)
	otherInput := financeInput(t, p.DB, other.ID, "other-end", otherRoot, 1)
	otherInput.Funding = []PurchaseFunding{{PurchaseID: otherPurchase.ID, TransferID: "funds"}}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, otherInput); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("transfer reused", err)
	}
	financeWant(t, p.DB, 730000, 1)
}

// Actual cash starts at zero in the investment account. The only funding is a
// R$ 5,000 paid transfer, preceding the R$ 1,000 initial application.
func financePreexistingCash(t *testing.T, claimInitial bool) (*database.RuntimeDBs, models.Investment, string) {
	t.Helper()
	p, _, inv := newInvestmentLifecycleDB(t)
	financeTransfer(t, p.Immediate, "preexisting", "2026-01-10", 500000)
	financeWant(t, p.DB, 500000, 0)
	insertValuationRow(t, p.Immediate, "cash-root", "ws-a", inv.ID, "2026-02-01", 100000)
	in := referenceInput(t, p.DB, inv.ID, "cash-root")
	root, err := RecordInvestmentReference(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	activate := financeInput(t, p.DB, inv.ID, "cash-root", root.ID, 0)
	if claimInitial {
		activate.Funding = []PurchaseFunding{{PurchaseID: "initial:" + inv.ID, TransferID: "preexisting", AmountCents: 100000}}
	}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, activate); err != nil {
		t.Fatal(err)
	}
	financeWant(t, p.DB, 500000, 0)
	return p, inv, root.ID
}

func TestInvestmentFinancialPreexistingCashAndSharedTransfers(t *testing.T) {
	for _, claimInitial := range []bool{false, true} {
		t.Run(map[bool]string{false: "unallocated initial cost reserved", true: "explicit shared allocation"}[claimInitial], func(t *testing.T) {
			p, inv, root := financePreexistingCash(t, claimInitial)
			purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(inv.ID))
			if err != nil {
				t.Fatal(err)
			}
			financeWant(t, p.DB, 500000, 0)
			insertValuationRow(t, p.Immediate, "reapplied", "ws-a", inv.ID, "2026-03-01", 330000)
			in := financeInput(t, p.DB, inv.ID, "reapplied", root, 1)
			in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "preexisting", AmountCents: 200000}}
			r, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			if err != nil || r.NetAdjustmentCents != 30000 {
				t.Fatalf("preexisting cash: %+v %v", r, err)
			}
			financeWant(t, p.DB, 530000, 1)
			var allocated int64
			if err = p.DB.QueryRow(`SELECT SUM(amount_cents) FROM investment_purchase_funding WHERE workspace_id='ws-a' AND transaction_id='preexisting'`).Scan(&allocated); err != nil {
				t.Fatal(err)
			}
			want := int64(200000)
			if claimInitial {
				want = 300000
			}
			if allocated != want {
				t.Fatal("wrong allocations", allocated)
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE type='TRANSFER'`) != 1 || countRows(t, p.DB, `SELECT COUNT(*) FROM transactions WHERE type='INCOME'`) != 0 {
				t.Fatal("created funding or income")
			}
			replay, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			if err != nil || !reflect.DeepEqual(r, replay) {
				t.Fatal("allocation replay", err)
			}
			in.Funding[0].AmountCents++
			if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
				t.Fatal("divergent allocation replay", err)
			}
		})
	}
}

func TestInvestmentFinancialMultipleTransfersAndCorrection(t *testing.T) {
	p, inv, root := financePreexistingCash(t, true)
	financeTransfer(t, p.Immediate, "second-source", "2026-02-15", 100000)
	purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(inv.ID))
	if err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "combined", "ws-a", inv.ID, "2026-03-01", 330000)
	in := financeInput(t, p.DB, inv.ID, "combined", root, 1)
	in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "second-source", AmountCents: 100000}, {PurchaseID: purchase.ID, TransferID: "preexisting", AmountCents: 100000}}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err != nil {
		t.Fatal(err)
	}
	financeWant(t, p.DB, 630000, 1)
	correction := financeInput(t, p.DB, inv.ID, "combined", root, 2)
	correction.CorrectionCents = withdrawalPrincipal(320000)
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, correction); err != nil {
		t.Fatal(err)
	}
	financeWant(t, p.DB, 620000, 3)
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_purchase_funding`) != 3 {
		t.Fatal("correction duplicated reservations")
	}
}

func TestInvestmentFinancialFundingCapacityGuards(t *testing.T) {
	for _, mode := range []string{"insufficient source", "duplicate allocation", "short allocation", "excess allocation", "foreign source", "source changed", "outgoing cash", "unallocated other purchase", "positive balance is not proof", "negative reconciliation"} {
		t.Run(mode, func(t *testing.T) {
			p, inv, root := financePreexistingCash(t, true)
			purchaseInput := investmentPurchaseInput(inv.ID)
			if mode == "insufficient source" || mode == "positive balance is not proof" {
				purchaseInput.PrincipalCents = 500000
			}
			purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, purchaseInput)
			if err != nil {
				t.Fatal(err)
			}
			insertValuationRow(t, p.Immediate, "guarded", "ws-a", inv.ID, "2026-03-01", purchase.PrincipalCents+130000)
			in := financeInput(t, p.DB, inv.ID, "guarded", root, 1)
			in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "preexisting", AmountCents: purchase.PrincipalCents}}
			switch mode {
			case "duplicate allocation":
				in.Funding = append(in.Funding, in.Funding[0])
			case "short allocation":
				in.Funding[0].AmountCents--
			case "excess allocation":
				in.Funding[0].AmountCents++
			case "foreign source":
				exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,status) VALUES('foreign-funds','ws-b','user-b','acc-inv-b','acc-inv-b','TRANSFER',900000,1768003200,'paid')`)
				in.Funding[0].TransferID = "foreign-funds"
			case "source changed":
				exec(t, p.Immediate, `UPDATE transactions SET amount=600000 WHERE workspace_id='ws-a' AND id='preexisting'`)
			case "outgoing cash":
				tx, err := p.Immediate.Begin()
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				at := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC).Unix()
				if _, err = tx.Exec(`INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,status) VALUES('outflow','ws-a','user-a','acc-inv-a','acc-checking-a','TRANSFER',300000,?,'paid')`, at); err != nil {
					t.Fatal(err)
				}
				if err = ApplyBalanceEffect(tx, "ws-a", "TRANSFER", "INVESTMENT", "paid", 300000, "acc-inv-a", "acc-checking-a", at); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "unallocated other purchase":
				other := investmentCreateInput("ws-a", "acc-inv-a")
				other.PrincipalCents = 300000
				other.AppliedDate = "2026-02-15"
				mustCreateInvestment(t, p.Immediate, other)
			case "positive balance is not proof", "negative reconciliation":
				target := int64(999999)
				if mode == "negative reconciliation" {
					target = 200000
				}
				var version int64
				if err = p.DB.QueryRow(`SELECT balance_version FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&version); err != nil {
					t.Fatal(err)
				}
				err = AdjustAccountBalance(context.Background(), p.Immediate, AccountBalanceAdjustment{ID: uuid.NewString(), WorkspaceID: "ws-a", AccountID: "acc-inv-a", UserID: "user-a", Reason: "Conciliação manual", ExpectedVersion: version, TargetBalance: target, CreatedAt: time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC).Unix()})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := investmentPurchasesFinancialState(t, p.DB)
			n := countRows(t, p.DB, `SELECT COUNT(*) FROM account_balance_adjustments`)
			if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err == nil {
				t.Fatal("unproven capacity accepted")
			}
			if before != investmentPurchasesFinancialState(t, p.DB) || n != countRows(t, p.DB, `SELECT COUNT(*) FROM account_balance_adjustments`) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_purchase_funding`) != 1 {
				t.Fatal("rejected capacity mutated financial state")
			}
		})
	}
}

func TestInvestmentFinancialFundingPriorDebtAndSourceDates(t *testing.T) {
	for _, mode := range []string{"prior cash debit", "negative initial balance", "divergent source date"} {
		t.Run(mode, func(t *testing.T) {
			p, inv, root := financePreexistingCash(t, true)
			switch mode {
			case "prior cash debit":
				// An old debit must not disappear from capacity just because the funding
				// transfer came later (it may have repaid an overdraft).
				exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('old-debt','ws-a','user-a','acc-inv-a','EXPENSE',400000,1767225600,'paid')`)
			case "negative initial balance":
				exec(t, p.Immediate, `UPDATE accounts SET initial_balance=-400000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
			case "divergent source date":
				exec(t, p.Immediate, `UPDATE transactions SET paid_date_civil='2026-01-09' WHERE workspace_id='ws-a' AND id='preexisting'`)
			}
			purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, investmentPurchaseInput(inv.ID))
			if err != nil {
				t.Fatal(err)
			}
			insertValuationRow(t, p.Immediate, "debt-guard", "ws-a", inv.ID, "2026-03-01", 330000)
			in := financeInput(t, p.DB, inv.ID, "debt-guard", root, 1)
			in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "preexisting", AmountCents: 200000}}
			before := investmentPurchasesFinancialState(t, p.DB)
			if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err == nil {
				t.Fatal("debt/ambiguous date accepted")
			}
			if before != investmentPurchasesFinancialState(t, p.DB) {
				t.Fatal("blocked funding changed finances")
			}
		})
	}
}

func TestInvestmentFinancialSharedFundingConcurrentAndRollback(t *testing.T) {
	p, inv, root := financePreexistingCash(t, true)
	// A second application's initial cost is explicitly allocated from the same
	// transfer; no financial effect is produced by either initial purchase.
	other := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	insertValuationRow(t, p.Immediate, "other-root", "ws-a", other.ID, "2026-02-01", 100000)
	reference, err := RecordInvestmentReference(context.Background(), p.Immediate, referenceInput(t, p.DB, other.ID, "other-root"))
	if err != nil {
		t.Fatal(err)
	}
	activation := financeInput(t, p.DB, other.ID, "other-root", reference.ID, 0)
	activation.Funding = []PurchaseFunding{{PurchaseID: "initial:" + other.ID, TransferID: "preexisting", AmountCents: 100000}}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, activation); err != nil {
		t.Fatal(err)
	}
	inputs := make([]IntegrateInvestmentPositionInput, 0, 2)
	for i, v := range []models.Investment{inv, other} {
		pi := investmentPurchaseInput(v.ID)
		pi.PrincipalCents = 100000
		purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, pi)
		if err != nil {
			t.Fatal(err)
		}
		position := "shared-one"
		ref := root
		amount := int64(210000)
		if i == 1 {
			position = "shared-two"
			ref = reference.ID
			amount = 220000
		}
		insertValuationRow(t, p.Immediate, position, "ws-a", v.ID, "2026-03-01", amount)
		in := financeInput(t, p.DB, v.ID, position, ref, 1)
		in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "preexisting", AmountCents: 100000}}
		inputs = append(inputs, in)
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, in := range inputs {
		go func(in IntegrateInvestmentPositionInput) {
			<-start
			_, err := IntegrateInvestmentPosition(context.Background(), p.Immediate, in)
			done <- err
		}(in)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	financeWant(t, p.DB, 530000, 2)
	var allocated int64
	if err = p.DB.QueryRow(`SELECT SUM(amount_cents) FROM investment_purchase_funding WHERE workspace_id='ws-a' AND transaction_id='preexisting'`).Scan(&allocated); err != nil || allocated != 400000 {
		t.Fatal("lost/duplicate source capacity", allocated, err)
	}
	// Claiming more than the remainder must roll back the entire new operation.
	pi := investmentPurchaseInput(inv.ID)
	pi.PurchaseDate = "2026-04-01"
	pi.PrincipalCents = 200000
	purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, pi)
	if err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "excess-after-shared", "ws-a", inv.ID, "2026-04-01", 440000)
	in := financeInput(t, p.DB, inv.ID, "excess-after-shared", root, 2)
	in.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "preexisting", AmountCents: 200000}}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatal("oversubscribed shared source", err)
	}
	financeWant(t, p.DB, 530000, 2)
}

func TestInvestmentFinancialOutcomeConcurrentReplay(t *testing.T) {
	for _, divergent := range []bool{false, true} {
		t.Run(fmt.Sprint(divergent), func(t *testing.T) {
			p, path, inv, root := financeFixture(t)
			insertValuationRow(t, p.Immediate, "next", "ws-a", inv.ID, "2026-03-01", 530000)
			in := financeInput(t, p.DB, inv.ID, "next", root, 1)
			other := in
			if divergent {
				other.Reason = "Different payload"
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
			defer first.Close()
			second := sql.OpenDB(&valuationTestConnector{dsn: dsn, beforeBegin: func() { close(atBegin) }})
			defer second.Close()
			type response struct {
				result InvestmentIntegrationOperationResult
				err    error
			}
			a, b := make(chan response, 1), make(chan response, 1)
			go func() { r, e := IntegrateInvestmentPositionWithOutcome(ctx, first, in); a <- response{r, e} }()
			select {
			case <-atCommit:
			case <-ctx.Done():
				t.Fatal("creator did not reach commit")
			}
			go func() { r, e := IntegrateInvestmentPositionWithOutcome(ctx, second, other); b <- response{r, e} }()
			select {
			case <-atBegin:
			case <-ctx.Done():
				t.Fatal("contender did not reach begin")
			}
			close(release)
			ra, rb := <-a, <-b
			if ra.err != nil || ra.result.Outcome != InvestmentOperationCreated {
				t.Fatalf("creator: %+v", ra)
			}
			if divergent {
				if !errors.Is(rb.err, ErrInvestmentConflict) || rb.result.Outcome != "" {
					t.Fatalf("conflict: %+v", rb)
				}
			} else if rb.err != nil || rb.result.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(ra.result.InvestmentIntegrationResult, rb.result.InvestmentIntegrationResult) {
				t.Fatalf("persistent replay: %+v %+v", ra, rb)
			}
			financeWant(t, p.DB, 530000, 1)
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions WHERE workspace_id='ws-a'`) != 2 {
				t.Fatal("duplicate financial receipt")
			}
		})
	}
}
