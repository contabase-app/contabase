package services

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

// F1 regressions: applications without an automatic transfer. Record-only
// registrations must never move balances, create receipts or relax a single
// hold that funded lots keep (withdrawal, redeem, adoption, cancellation
// eligibility). Every database here is synthetic and disposable.

func f1AccountState(t *testing.T, db *sql.DB, account string) (int64, int64) {
	t.Helper()
	var balance, version int64
	if err := db.QueryRow(`SELECT current_balance,balance_version FROM accounts WHERE workspace_id='ws-a' AND id=?`, account).Scan(&balance, &version); err != nil {
		t.Fatal(err)
	}
	return balance, version
}

// Cenário 1 — criação financiada: origem debitada, investimento creditado,
// exatamente uma transferência vinculada com recibo canônico.
func TestF1FundedCreationDebitsOriginAndCreditsDestinationOnce(t *testing.T) {
	pools := newCancellationDB(t)
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 1000000)
	checking, _ := f1AccountState(t, pools.DB, "acc-checking-a")
	investment, _ := f1AccountState(t, pools.DB, "acc-inv-a")
	if checking != 0 || investment != 1000000 {
		t.Fatalf("balances checking=%d investment=%d", checking, investment)
	}
	if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions WHERE workspace_id='ws-a' AND type='TRANSFER' AND destination_account_id='acc-inv-a'`); got != 1 {
		t.Fatalf("linked transfers=%d want=1", got)
	}
	if err := pools.DB.QueryRow(`SELECT amount FROM transactions WHERE workspace_id='ws-a' AND id=?`, InvestmentPurchaseTransferPrefix+"initial:"+inv.ID).Scan(new(int64)); err != nil {
		t.Fatalf("canonical receipt missing: %v", err)
	}
}

// Cenário 2 — criação record-only: o dinheiro já está na conta de
// investimento; saldo, versão e razão contábil permanecem intactos.
func TestF1RecordOnlyCreationKeepsBalancesAndLedger(t *testing.T) {
	pools := newCancellationDB(t)
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	beforeBalance, beforeVersion := f1AccountState(t, pools.DB, "acc-inv-a")
	in := investmentCreateInput("ws-a", "acc-inv-a")
	in.RequestID = uuid.NewString()
	in.PrincipalCents = 1000000
	inv := mustCreateInvestment(t, pools.Immediate, in)
	afterBalance, afterVersion := f1AccountState(t, pools.DB, "acc-inv-a")
	if afterBalance != beforeBalance || afterVersion != beforeVersion {
		t.Fatalf("record-only creation moved money: %d/%d → %d/%d", beforeBalance, beforeVersion, afterBalance, afterVersion)
	}
	if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`); got != 0 {
		t.Fatalf("record-only creation wrote %d ledger rows", got)
	}
	detail, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
	if err != nil || detail.PurchaseCount != 1 || detail.TotalPurchasesCents != 1000000 || len(detail.PurchaseTransfers) != 0 {
		t.Fatalf("principal not recorded: %+v %v", detail, err)
	}
}

// Cenário 3 — reaplicação record-only em investimento financiado: nova compra
// no histórico, saldos e versões inalterados, nenhum recibo financeiro novo e
// a elegibilidade do cancelamento integral permanece bloqueada.
func TestF1FundedLotAcceptsRecordOnlyReapplicationWithoutExtendingCancellation(t *testing.T) {
	pools := newCancellationDB(t)
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 500000)
	beforeCheckingBalance, beforeCheckingVersion := f1AccountState(t, pools.DB, "acc-checking-a")
	beforeInvBalance, beforeInvVersion := f1AccountState(t, pools.DB, "acc-inv-a")
	purchase := investmentPurchaseInput(inv.ID)
	purchase.PurchaseDate = "2026-02-01"
	row, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, purchase)
	if err != nil {
		t.Fatal(err)
	}
	if afterCheckingBalance, afterCheckingVersion := f1AccountState(t, pools.DB, "acc-checking-a"); afterCheckingBalance != beforeCheckingBalance || afterCheckingVersion != beforeCheckingVersion {
		t.Fatalf("reapplication moved origin: %d/%d → %d/%d", beforeCheckingBalance, beforeCheckingVersion, afterCheckingBalance, afterCheckingVersion)
	}
	if afterInvBalance, afterInvVersion := f1AccountState(t, pools.DB, "acc-inv-a"); afterInvBalance != beforeInvBalance || afterInvVersion != beforeInvVersion {
		t.Fatalf("reapplication moved destination: %d/%d → %d/%d", beforeInvBalance, beforeInvVersion, afterInvBalance, afterInvVersion)
	}
	if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`); got != 1 {
		t.Fatalf("record-only reapplication created %d extra ledger rows", got-1)
	}
	if err := pools.DB.QueryRow(`SELECT 1 FROM transactions WHERE workspace_id='ws-a' AND id=?`, InvestmentPurchaseTransferPrefix+row.ID).Scan(new(int)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("record-only purchase has a receipt: %v", err)
	}
	detail, err := GetInvestmentPurchaseDetail(context.Background(), pools.DB, "ws-a", inv.ID)
	if err != nil || detail.PurchaseCount != 2 || detail.TotalPurchasesCents != 700000 {
		t.Fatalf("history lost the reapplication: %+v %v", detail, err)
	}
	if len(detail.PurchaseTransfers) != 1 {
		t.Fatalf("receipts after reapplication=%d want=1 (the initial one)", len(detail.PurchaseTransfers))
	}
	// F1 não amplia a elegibilidade do cancelamento integral (B7-I2): a compra
	// adicional, financiada ou não, continua sendo dependência que bloqueia.
	preview, err := PreviewInvestmentCancellation(context.Background(), pools.DB, cancellationPreviewInput("ws-a", inv.ID, "tentativa após reaplicação"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Eligible || len(preview.Blockers) != 1 || !strings.Contains(preview.Blockers[0], "operações ou dependências posteriores") {
		t.Fatalf("cancellation eligibility silently extended: eligible=%v blockers=%v", preview.Eligible, preview.Blockers)
	}
	if _, err := CancelInvestment(context.Background(), pools.Immediate, CancelInvestmentInput{
		WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-a",
		RequestID: uuid.NewString(), Reason: "não deveria passar", PreviewToken: "forged.payload",
	}); !errors.Is(err, ErrInvestmentCancellationPreview) {
		t.Fatalf("unconfirmed cancellation attempt err=%v", err)
	}
}

// Cenário 4 — replay por request_id e concorrência no mesmo formulário.
func TestF1RecordOnlyReapplicationReplayConflictAndConcurrency(t *testing.T) {
	pools := newCancellationDB(t)
	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 500000)
	base := investmentPurchaseInput(inv.ID)
	base.PurchaseDate = "2026-02-01"
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
		t.Fatalf("divergent retry err=%v", err)
	}
	concurrent := investmentPurchaseInput(inv.ID)
	concurrent.PurchaseDate = "2026-02-02"
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := CreateInvestmentPurchase(context.Background(), pools.Immediate, concurrent)
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
	var concurrentPurchases int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM investment_purchases WHERE workspace_id='ws-a' AND investment_id=?`, inv.ID).Scan(&concurrentPurchases); err != nil {
		t.Fatal(err)
	}
	if concurrentPurchases != 3 {
		t.Fatalf("purchases after concurrency=%d want=3 (initial + 2)", concurrentPurchases)
	}
	if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`); got != 1 {
		t.Fatalf("ledger rows after record-only concurrency=%d want=1", got)
	}
}

// Cenário 5 — proteções preservadas: cancelado, origem inválida, cross-workspace,
// financiamento ainda funcional e bloqueios de resgate/retirada em lote financiado.
func TestF1ProtectionsPreservedOnFundedLots(t *testing.T) {
	pools := newCancellationDB(t)
	cancelled := cancelledLotForGuard(t, pools, 50000)
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, guardPurchaseInput("ws-a", cancelled.ID, "")); !errors.Is(err, ErrInvestmentCancelled) {
		t.Fatalf("cancelled lot accepted record-only purchase: %v", err)
	}
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, guardPurchaseInput("ws-b", cancelled.ID, "")); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("cross-workspace purchase err=%v", err)
	}

	exec(t, pools.Immediate, `UPDATE accounts SET initial_balance=1000000,current_balance=1000000 WHERE workspace_id='ws-a' AND id='acc-checking-a'`)
	inv := fundedCancellationInvestment(t, pools.Immediate, "ws-a", "acc-inv-a", "acc-checking-a", 500000)
	sameAccount := investmentPurchaseInput(inv.ID)
	sameAccount.PurchaseDate = "2026-02-01"
	sameAccount.SourceAccountID = "acc-inv-a"
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, sameAccount); !errors.Is(err, ErrInvestmentInvalidAccount) {
		t.Fatalf("source == destination err=%v", err)
	}
	var refusedPurchases int
	if err := pools.DB.QueryRow(`SELECT COUNT(*) FROM investment_purchases WHERE workspace_id='ws-a' AND investment_id=?`, inv.ID).Scan(&refusedPurchases); err != nil {
		t.Fatal(err)
	}
	if refusedPurchases != 1 {
		t.Fatalf("refused same-account purchase persisted rows=%d", refusedPurchases)
	}

	// Compra financiada continua funcionando sem regressão.
	beforeFunded := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`)
	funded := investmentPurchaseInput(inv.ID)
	funded.PurchaseDate = "2026-02-01"
	funded.SourceAccountID = "acc-checking-a"
	if _, err := CreateInvestmentPurchase(context.Background(), pools.Immediate, funded); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, pools.DB, `SELECT COUNT(*) FROM transactions`); got != beforeFunded+1 {
		t.Fatalf("funded purchase transfers=%d want=%d", got, beforeFunded+1)
	}

	// Record-only em lote financiado não libera resgate nem retirada.
	if err := RedeemInvestment(context.Background(), pools.Immediate, investmentRedeemInput(inv.ID)); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("funded redeem err=%v", err)
	}
	withdrawal := CreateInvestmentWithdrawalInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, CreatedBy: "user-a", RequestID: uuid.NewString(), WithdrawalDate: "2026-03-01", Mode: InvestmentWithdrawalTotal, ReceivedCents: 500000,
		Allocations: []models.InvestmentWithdrawalAllocation{{PurchaseID: "initial:" + inv.ID, ReceivedCents: 500000}}}
	if _, err := CreateInvestmentWithdrawal(context.Background(), pools.Immediate, withdrawal); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("funded withdrawal err=%v", err)
	}
}
