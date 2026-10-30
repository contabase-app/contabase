package services

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

func referenceInput(t *testing.T, db *sql.DB, investment, position string) RecordInvestmentReferenceInput {
	t.Helper()
	c, err := ReadInvestmentReferenceCandidate(context.Background(), db, "ws-a", investment, position)
	if err != nil {
		t.Fatal(err)
	}
	return RecordInvestmentReferenceInput{WorkspaceID: "ws-a", InvestmentID: investment, AccountID: "acc-inv-a", PositionID: position, CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Conciliação explícita", CoverageToken: c.CoverageToken, RepresentedCents: withdrawalPrincipal(100000), Confirm: true}
}

func TestInvestmentReferencesKnownUnknownRevisionAndNonMutation(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, "2026-02-01", 110000)
	exec(t, pools.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('reference-control','ws-a','user-a','acc-inv-a','INCOME',123,1760000000,'paid')`)
	exec(t, pools.Immediate, `UPDATE accounts SET current_balance=321000 WHERE id='acc-inv-a'`)
	financial := investmentPurchasesFinancialState(t, pools.DB)
	before, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := referenceInput(t, pools.DB, inv.ID, "position")
	in.RepresentedCents = nil
	in.Confirm = false
	first, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || first.State != models.InvestmentReferencePending || first.RepresentedCents != nil || first.ConfirmedAt != nil || first.Version != 1 {
		t.Fatalf("pending: %+v %v", first, err)
	}
	in.RequestID = uuid.NewString()
	in.ExpectedVersion = 1
	in.Confirm = true
	in.RepresentedCents = withdrawalPrincipal(0)
	second, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || second.State != models.InvestmentReferenceValid || second.RepresentedCents == nil || *second.RepresentedCents != 0 || second.ConfirmedAt == nil || second.ReferenceID != first.ID || second.PreviousRevisionID != first.ID || second.PositionVersion != 2 {
		t.Fatalf("known zero: %+v %v", second, err)
	}
	old, err := ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-a", inv.ID, first.ID)
	if err != nil || old.State != models.InvestmentReferenceSuperseded || old.RepresentedCents != nil {
		t.Fatalf("history: %+v %v", old, err)
	}
	in.RequestID = uuid.NewString()
	in.ExpectedVersion = 2
	in.RepresentedCents = withdrawalPrincipal(100000)
	third, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || third.Version != 3 || *third.RepresentedCents != 100000 || third.IntegrationState != "PREPARED" || third.Origin != "MANUAL_REFERENCE" {
		t.Fatalf("known: %+v %v", third, err)
	}
	replay, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || !reflect.DeepEqual(third, replay) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	in.Reason = "Divergente"
	if _, err = RecordInvestmentReference(ctx, pools.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("payload conflict: %v", err)
	}
	after, err := GetInvestmentPurchaseDetail(ctx, pools.DB, "ws-a", inv.ID)
	if err != nil || !reflect.DeepEqual(before, after) || financial != investmentPurchasesFinancialState(t, pools.DB) || countRows(t, pools.DB, `SELECT COUNT(*) FROM account_balance_adjustments`) != 0 {
		t.Fatalf("financial or legacy mutation: %v", err)
	}
	for _, query := range []string{`UPDATE investment_reference_revisions SET represented_cents=1`, `DELETE FROM investment_reference_revisions`, `INSERT OR REPLACE INTO investment_reference_revisions SELECT * FROM investment_reference_revisions WHERE version=3`, `UPDATE investment_reference_coverage SET operation_id='changed'`, `DELETE FROM investment_reference_coverage`, `INSERT OR REPLACE INTO investment_reference_coverage SELECT * FROM investment_reference_coverage WHERE revision_id='` + first.ID + `'`} {
		if _, err = pools.Immediate.Exec(query); err == nil {
			t.Fatalf("immutable evidence accepted %s", query)
		}
	}
}

func TestInvestmentReferencesCoverageAndInvalidation(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, "2026-01-15", 100000)
	in := referenceInput(t, pools.DB, inv.ID, "position")
	if _, err := RecordInvestmentReference(ctx, pools.Immediate, in); !errors.Is(err, ErrInvestmentValidation) {
		t.Fatalf("missing close confirmation: %v", err)
	}
	in.DayClosed = true
	first, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || !first.Candidate.HasSameDayOperations || first.State != models.InvestmentReferenceValid {
		t.Fatalf("close: %+v %v", first, err)
	}
	p := investmentPurchaseInput(inv.ID)
	p.PurchaseDate = "2026-01-15"
	if _, err = CreateInvestmentPurchase(ctx, pools.Immediate, p); err != nil {
		t.Fatal(err)
	}
	stale, err := ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-a", inv.ID, "")
	if err != nil || stale.State != models.InvestmentReferenceInvalidated || len(stale.Candidate.Snapshot.Purchases) != 1 {
		t.Fatalf("new same-day purchase: %+v %v", stale, err)
	}
	// A retry is still the old receipt, and must not resurrect its validity.
	replay, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || replay.State != models.InvestmentReferenceInvalidated || replay.ID != first.ID {
		t.Fatalf("stale replay: %+v %v", replay, err)
	}
	in.RequestID = uuid.NewString()
	in.ExpectedVersion = 1
	if _, err = RecordInvestmentReference(ctx, pools.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("stale confirmation token: %v", err)
	}
	in = referenceInput(t, pools.DB, inv.ID, "position")
	in.ExpectedVersion = 1
	in.DayClosed = true
	if _, err = RecordInvestmentReference(ctx, pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	w := withdrawalInput(inv.ID, "initial:"+inv.ID)
	w.WithdrawalDate = "2026-01-15"
	withdrawal, err := CreateInvestmentWithdrawal(ctx, pools.Immediate, w)
	if err != nil {
		t.Fatal(err)
	}
	stale, err = ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-a", inv.ID, "")
	if err != nil || stale.State != models.InvestmentReferenceInvalidated {
		t.Fatalf("new withdrawal: %+v %v", stale, err)
	}
	in = referenceInput(t, pools.DB, inv.ID, "position")
	in.ExpectedVersion = 2
	in.DayClosed = true
	r, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || r.Candidate.Snapshot.Withdrawals[0].Allocations[0].PrincipalCents != nil {
		t.Fatalf("unknown principal: %+v %v", r, err)
	}
	// Changes inside an existing withdrawal also invalidate its snapshot.
	exec(t, pools.Immediate, `UPDATE investment_withdrawal_allocations SET principal_cents=500 WHERE withdrawal_id=?`, withdrawal.ID)
	r, err = ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-a", inv.ID, "")
	if err != nil || r.State != models.InvestmentReferenceInvalidated {
		t.Fatalf("allocation change: %+v %v", r, err)
	}
	in = referenceInput(t, pools.DB, inv.ID, "position")
	in.ExpectedVersion = 3
	in.DayClosed = true
	if _, err = RecordInvestmentReference(ctx, pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	if _, err = UpsertInvestmentValuation(ctx, pools.Immediate, UpsertInvestmentValuationInput{WorkspaceID: "ws-a", InvestmentID: inv.ID, ActingUserID: "user-a", ValuationDate: "2026-01-15", AmountCents: 200000}); err != nil {
		t.Fatal(err)
	}
	r, err = ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-a", inv.ID, "")
	if err != nil || r.State != models.InvestmentReferenceInvalidated || r.Candidate.Snapshot.Position.AmountCents != 100000 {
		t.Fatalf("position correction: %+v %v", r, err)
	}
	in = referenceInput(t, pools.DB, inv.ID, "position")
	in.ExpectedVersion = 4
	in.DayClosed = true
	if _, err = RecordInvestmentReference(ctx, pools.Immediate, in); err != nil {
		t.Fatal(err)
	}
	// Later economic operations do not rewrite the earlier reference coverage.
	p = investmentPurchaseInput(inv.ID)
	if _, err = CreateInvestmentPurchase(ctx, pools.Immediate, p); err != nil {
		t.Fatal(err)
	}
	r, err = ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-a", inv.ID, "")
	if err != nil || r.State != models.InvestmentReferenceValid {
		t.Fatalf("later operation corrupted earlier reference: %+v %v", r, err)
	}
}

func TestInvestmentReferencesIsolationValidationAndRollback(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, "2026-02-01", 110000)
	other := mustCreateInvestment(t, pools.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	insertValuationRow(t, pools.Immediate, "other-position", "ws-a", other.ID, "2026-02-01", 100)
	purchase := investmentPurchaseInput(inv.ID)
	purchase.PurchaseDate = "2026-01-20"
	if _, err := CreateInvestmentPurchase(ctx, pools.Immediate, purchase); err != nil {
		t.Fatal(err)
	}
	in := referenceInput(t, pools.DB, inv.ID, "position")
	for _, tc := range []struct {
		name   string
		change func(*RecordInvestmentReferenceInput)
		want   error
	}{
		{"foreign workspace", func(i *RecordInvestmentReferenceInput) { i.WorkspaceID = "ws-b" }, ErrInvestmentNotFound},
		{"foreign account", func(i *RecordInvestmentReferenceInput) { i.AccountID = "acc-inv-b" }, ErrInvestmentNotFound},
		{"other account same workspace", func(i *RecordInvestmentReferenceInput) { i.AccountID = "acc-checking-a" }, ErrInvestmentNotFound},
		{"other position", func(i *RecordInvestmentReferenceInput) { i.PositionID = "other-position" }, ErrInvestmentNotFound},
		{"unknown confirmed", func(i *RecordInvestmentReferenceInput) { i.RepresentedCents = nil }, ErrInvestmentValidation},
		{"negative", func(i *RecordInvestmentReferenceInput) { i.RepresentedCents = withdrawalPrincipal(-1) }, ErrInvestmentValidation},
		{"version overflow", func(i *RecordInvestmentReferenceInput) { i.ExpectedVersion = math.MaxInt64 }, ErrInvestmentValidation},
		{"stale version", func(i *RecordInvestmentReferenceInput) { i.ExpectedVersion = 1 }, ErrInvestmentConflict},
		{"bad request", func(i *RecordInvestmentReferenceInput) { i.RequestID = "bad" }, ErrInvestmentValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := in
			tc.change(&input)
			if _, err := RecordInvestmentReference(ctx, pools.Immediate, input); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	financial := investmentPurchasesFinancialState(t, pools.DB)
	exec(t, pools.Immediate, `CREATE TRIGGER fail_reference_coverage BEFORE INSERT ON investment_reference_coverage WHEN EXISTS(SELECT 1 FROM investment_reference_coverage WHERE revision_id=NEW.revision_id) BEGIN SELECT RAISE(ABORT,'forced coverage failure'); END`)
	if result, err := RecordInvestmentReferenceWithOutcome(ctx, pools.Immediate, in); err == nil || result.Outcome != "" || result.ID != "" {
		t.Fatalf("forced failure reported success: %+v %v", result, err)
	}
	if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != 0 || countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_reference_coverage`) != 0 || financial != investmentPurchasesFinancialState(t, pools.DB) {
		t.Fatal("rollback left partial evidence or changed finances")
	}
	exec(t, pools.Immediate, `DROP TRIGGER fail_reference_coverage`)
	in.RepresentedCents = withdrawalPrincipal(math.MaxInt64)
	r, err := RecordInvestmentReference(ctx, pools.Immediate, in)
	if err != nil || *r.RepresentedCents != math.MaxInt64 {
		t.Fatalf("int64 boundary: %+v %v", r, err)
	}
	if _, err = ReadInvestmentReferenceRevision(ctx, pools.DB, "ws-b", inv.ID, r.ID); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
	maintenance := fixtureMaintenance(t, pools.Immediate)
	defer maintenance.Close()
	for _, date := range []string{"2026-02-31", "2026-02-29", "0000-01-01", "2026-02-01extra"} {
		exec(t, maintenance, `PRAGMA ignore_check_constraints=ON`)
		// Restore only the fixture's source date; the production schema is untouched.
		exec(t, maintenance, `DROP TRIGGER IF EXISTS trg_investment_valuations_immutable_keys`)
		exec(t, maintenance, `DROP TRIGGER IF EXISTS trg_investment_valuations_validate_update`)
		exec(t, maintenance, `UPDATE investment_valuations SET valuation_date=? WHERE id='position'`, date)
		if _, err = ReadInvestmentReferenceCandidate(ctx, pools.DB, "ws-a", inv.ID, "position"); !errors.Is(err, ErrInvestmentValidation) {
			t.Fatalf("bad date %q: %v", date, err)
		}
	}
}

func TestInvestmentReferencesWithdrawalOverflow(t *testing.T) {
	pools, _, inv := newInvestmentLifecycleDB(t)
	insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, "2026-03-01", 100000)
	for _, id := range []string{"one", "two"} {
		exec(t, pools.Immediate, `INSERT INTO investment_withdrawals(id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id) VALUES(?,'ws-a',?,'2026-02-01',?,'PARTIAL','user-a',?)`, id, inv.ID, int64(math.MaxInt64), uuid.NewString())
		exec(t, pools.Immediate, `INSERT INTO investment_withdrawal_allocations(workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,closes_purchase) VALUES('ws-a',?,?,?, ?,0)`, inv.ID, id, "initial:"+inv.ID, int64(math.MaxInt64))
	}
	if _, err := ReadInvestmentReferenceCandidate(context.Background(), pools.DB, "ws-a", inv.ID, "position"); !errors.Is(err, ErrBalanceOutOfRange) {
		t.Fatalf("overflow: %v", err)
	}
	if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != 0 {
		t.Fatal("overflow persisted reference")
	}
}

func TestInvestmentReferencesImmediateConcurrency(t *testing.T) {
	for _, kind := range []string{"retry", "divergent payload", "competing version"} {
		t.Run(kind, func(t *testing.T) {
			pools, path, inv := newInvestmentLifecycleDB(t)
			insertValuationRow(t, pools.Immediate, "position", "ws-a", inv.ID, "2026-02-01", 110000)
			in := referenceInput(t, pools.DB, inv.ID, "position")
			other := in
			if kind == "divergent payload" {
				other.Reason = "Diferente"
			}
			if kind == "competing version" {
				other.RequestID = uuid.NewString()
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
			type result struct {
				row InvestmentReferenceOperationResult
				err error
			}
			a, b := make(chan result, 1), make(chan result, 1)
			go func() { r, e := RecordInvestmentReferenceWithOutcome(ctx, first, in); a <- result{r, e} }()
			select {
			case <-atCommit:
			case <-ctx.Done():
				t.Fatal("first did not reach commit")
			}
			go func() { r, e := RecordInvestmentReferenceWithOutcome(ctx, second, other); b <- result{r, e} }()
			select {
			case <-atBegin:
			case <-ctx.Done():
				t.Fatal("second did not reach begin")
			}
			close(release)
			ra, rb := <-a, <-b
			if ra.err != nil || ra.row.Outcome != InvestmentOperationCreated {
				t.Fatalf("creation: %+v", ra)
			}
			if kind == "retry" {
				if rb.err != nil || rb.row.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(ra.row.InvestmentReferenceRevision, rb.row.InvestmentReferenceRevision) {
					t.Fatalf("retry: %+v %+v", ra, rb)
				}
			} else if !errors.Is(rb.err, ErrInvestmentConflict) || rb.row.Outcome != "" {
				t.Fatalf("conflict: %v", rb.err)
			}
			if countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != 1 || countRows(t, pools.DB, `SELECT COUNT(*) FROM investment_reference_coverage`) != 1 {
				t.Fatal("duplicate or partial reference")
			}
		})
	}
}
