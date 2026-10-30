package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
)

func adoptionHas(d InvestmentAdoptionDiagnostic, code string) bool {
	for _, i := range d.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

// Every row/column of all relevant tables, including evidence, coverage,
// settlement/classification and schema metadata. No count/sum-only oracle.
func adoptionSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out strings.Builder
	for _, table := range []string{"accounts", "transactions", "account_balance_adjustments", "box_virtual_ledger", "investments", "investment_purchases", "investment_valuations", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions", "investment_settlement_receipts", "investment_settlement_applications", "investment_settlement_prior_effects", "investment_result_classifications", "schema_migrations"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for n := range values {
				dest[n] = &values[n]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
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

func adoptionRead(t *testing.T, p *database.RuntimeDBs, cut string) InvestmentAdoptionDiagnostic {
	t.Helper()
	before := adoptionSnapshot(t, p.DB)
	d, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-a", "acc-inv-a", cut)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-a", "acc-inv-a", cut)
	if err != nil || !reflect.DeepEqual(d, again) {
		t.Fatalf("non deterministic: %v", err)
	}
	if before != adoptionSnapshot(t, p.DB) {
		t.Fatal("diagnostic changed persisted financial/evidence rows")
	}
	if d.BrokerCash.Cents != nil || d.BrokerCash.Evidence.State != InvestmentEvidenceUnknown {
		t.Fatal("fabricated broker cash")
	}
	if d.BalanceAtCut.Cents != nil || d.BalanceAtCut.Evidence.State != InvestmentEvidenceUnknown {
		t.Fatal("certified incomplete historical balance")
	}
	return d
}

func TestInvestmentAdoptionAEmptyAccount(t *testing.T) {
	p, _, _ := newInvestmentLifecycleDB(t)
	// Separate empty account; do not delete investment fixture history.
	exec(t, p.Immediate, `INSERT INTO accounts(id,workspace_id,name,type) VALUES('empty','ws-a','Empty','INVESTMENT')`)
	before := adoptionSnapshot(t, p.DB)
	d, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-a", "empty", "2026-04-01")
	if err != nil || len(d.Investments) != 0 || d.PositionCount != 0 || d.KnownPatrimonyCents != 0 || d.State != "NEEDS_REVIEW" {
		t.Fatalf("empty: %+v %v", d, err)
	}
	if before != adoptionSnapshot(t, p.DB) {
		t.Fatal("empty account mutated")
	}
	if !adoptionHas(d, "UNKNOWN_CASH_COMPONENT") {
		t.Fatal("no explicit external composition limit")
	}
}

func TestInvestmentAdoptionBExplicitTransferFunding(t *testing.T) {
	p, _, inv, root := financeFixture(t)
	financeTransfer(t, p.Immediate, "fund", "2026-02-15", 200000)
	in := investmentPurchaseInput(inv.ID)
	purchase, err := CreateInvestmentPurchase(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "funded-position", "ws-a", inv.ID, "2026-04-01", 730000)
	input := financeInput(t, p.DB, inv.ID, "funded-position", root, 1)
	input.Funding = []PurchaseFunding{{PurchaseID: purchase.ID, TransferID: "fund", AmountCents: 200000}}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, input); err != nil {
		t.Fatal(err)
	}
	d := adoptionRead(t, p, "2026-04-01")
	i := d.Investments[0]
	if len(d.Transfers) != 1 || d.Transfers[0].Evidence.State != InvestmentEvidenceProven || d.Transfers[0].AllocatedCents != 200000 || len(i.Funding) != 1 || i.Funding[0].Evidence.State != InvestmentEvidenceProven {
		t.Fatalf("explicit allocation missing: %+v %+v", d.Transfers, i.Funding)
	}
	if i.FundingEvidence.State == InvestmentEvidenceProven || len(i.UnfundedPurchaseIDs) != 1 {
		t.Fatal("root funding exemption was promoted to proof")
	}
	if i.PrincipalEvidence.State != InvestmentEvidenceUnknown || i.ProvenPrincipalCents != nil {
		t.Fatal("funding promoted to documentary acquisition cost proof")
	}
}

func TestInvestmentAdoptionBRootFundingAndSourceDrift(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	financeTransfer(t, p.Immediate, "identified-original-funding", "2026-01-01", inv.PrincipalCents)
	insertValuationRow(t, p.Immediate, "funded-root", "ws-a", inv.ID, "2026-02-01", inv.PrincipalCents)
	r := referenceInput(t, p.DB, inv.ID, "funded-root")
	root, err := RecordInvestmentReference(context.Background(), p.Immediate, r)
	if err != nil {
		t.Fatal(err)
	}
	in := financeInput(t, p.DB, inv.ID, "funded-root", root.ID, 0)
	in.Funding = []PurchaseFunding{{PurchaseID: "initial:" + inv.ID, TransferID: "identified-original-funding", AmountCents: inv.PrincipalCents}}
	if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, in); err != nil {
		t.Fatal(err)
	}
	d := adoptionRead(t, p, "2026-04-01")
	if d.Investments[0].FundingEvidence.State != InvestmentEvidenceProven || len(d.Investments[0].UnfundedPurchaseIDs) != 0 {
		t.Fatal("complete original funding lost")
	}
	// Equal date and amount alone must not keep a modified source proven.
	exec(t, p.Immediate, `UPDATE transactions SET status='pending' WHERE workspace_id='ws-a' AND id='identified-original-funding'`)
	d = adoptionRead(t, p, "2026-04-01")
	if d.Investments[0].FundingEvidence.State == InvestmentEvidenceProven || !adoptionHas(d, "UNVERIFIED_FUNDING") || d.State != InvestmentAdoptionBlocked {
		t.Fatal("stale source remained proven")
	}
}

func TestInvestmentAdoptionCInitialBalanceAndJUnexplainedDifference(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	exec(t, p.Immediate, `UPDATE accounts SET initial_balance=123456,current_balance=123456 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	insertValuationRow(t, p.Immediate, "manual", "ws-a", inv.ID, "2026-02-01", 100000)
	d := adoptionRead(t, p, "2026-02-01")
	if !adoptionHas(d, "UNVERIFIED_INITIAL_BALANCE") || !adoptionHas(d, "UNEXPLAINED_BALANCE_DIFFERENCE") || d.InitialBalanceCents != 123456 || len(d.Investments[0].Funding) != 0 {
		t.Fatalf("opening balance became funding: %+v", d)
	}
	if d.CurrentBalanceMinusKnownPatrimonyCents == nil || *d.CurrentBalanceMinusKnownPatrimonyCents != 23456 {
		t.Fatal("missing labelled arithmetic")
	}
	if d.Investments[0].PrincipalEvidence.State != InvestmentEvidenceUnknown {
		t.Fatal("initial balance became proven principal")
	}
}

func adoptionIncomeFixture(t *testing.T, p *database.RuntimeDBs, id, date, status string, amount int64) {
	t.Helper()
	at, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,description,notes,attachment_path,status) VALUES(?,'ws-a','user-a','acc-inv-a','INCOME',?,?,?,'Rendimento manual','Extrato referido','local/reference.pdf',?)`, id, amount, at.Unix(), date, status)
}

func TestInvestmentAdoptionDHistoricalIncomeCandidate(t *testing.T) {
	p, _, inv, root := financeFixture(t)
	financeIntegrate(t, p, inv, root, "gain", "2026-03-01", 530000, 1)
	adoptionIncomeFixture(t, p, "manual-income", "2026-03-01", "paid", 30000)
	adoptionIncomeFixture(t, p, "pending-income", "2026-02-15", "pending", 30000)
	adoptionIncomeFixture(t, p, "future-income", "2026-05-01", "paid", 123)
	d := adoptionRead(t, p, "2026-04-01")
	if len(d.IncomeCandidates) != 2 || !adoptionHas(d, "HISTORICAL_INCOME_OVERLAP") {
		t.Fatalf("candidates=%+v", d.IncomeCandidates)
	}
	for _, income := range d.IncomeCandidates {
		if income.Evidence.State != InvestmentEvidenceInferred || income.AttachmentReference != "local/reference.pdf" || len(income.Overlaps) != 1 || income.Overlaps[0].Reason != "POTENTIAL_DUPLICATE_VALUATION" || !income.Overlaps[0].Integrated || income.Overlaps[0].VariationCents == nil || *income.Overlaps[0].VariationCents != 30000 {
			t.Fatalf("candidate promoted or missing: %+v", income)
		}
	}
	if len(d.EventsAfterCut) != 1 || d.EventsAfterCut[0].ID != "future-income" {
		t.Fatal("future income silently included")
	}
}

func TestInvestmentAdoptionEArithmeticIsNotCash(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	exec(t, p.Immediate, `UPDATE accounts SET initial_balance=2000000,current_balance=2000000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	insertValuationRow(t, p.Immediate, "eighteen-thousand", "ws-a", inv.ID, "2026-02-01", 1800000)
	d := adoptionRead(t, p, "2026-02-01")
	if d.KnownPatrimonyCents != 1800000 || d.CurrentBalanceCents != 2000000 || *d.CurrentBalanceMinusKnownPatrimonyCents != 200000 {
		t.Fatalf("composition: %+v", d)
	}
	if d.BrokerCash.Cents != nil || d.BrokerCash.Evidence.State != InvestmentEvidenceUnknown {
		t.Fatal("external declaration absent from DB was fabricated as cash proof")
	}
}

func TestInvestmentAdoptionFMultipleInvestmentsSharedFundingPartialCoverage(t *testing.T) {
	p, _, inv, root := financeFixture(t)
	second := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	financeTransfer(t, p.Immediate, "shared", "2026-02-15", 500000)
	firstInput := investmentPurchaseInput(inv.ID)
	firstInput.PrincipalCents = 100000
	a, err := CreateInvestmentPurchase(context.Background(), p.Immediate, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	secondInput := investmentPurchaseInput(second.ID)
	secondInput.PrincipalCents = 200000
	b, err := CreateInvestmentPurchase(context.Background(), p.Immediate, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot, _ := financeRoot(t, p, second)
	for _, x := range []struct {
		inv      models.Investment
		root     string
		purchase models.InvestmentPurchase
		value    int64
	}{{inv, root, a, 610000}, {second, secondRoot, b, 710000}} {
		position := "shared:" + x.inv.ID
		insertValuationRow(t, p.Immediate, position, "ws-a", x.inv.ID, "2026-04-01", x.value)
		input := financeInput(t, p.DB, x.inv.ID, position, x.root, 1)
		input.Funding = []PurchaseFunding{{PurchaseID: x.purchase.ID, TransferID: "shared", AmountCents: x.purchase.PrincipalCents}}
		if _, err = IntegrateInvestmentPosition(context.Background(), p.Immediate, input); err != nil {
			t.Fatal(err)
		}
	}
	third := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	d := adoptionRead(t, p, "2026-04-01")
	if len(d.Investments) != 3 || len(d.Transfers) != 1 || d.Transfers[0].AllocatedCents != 300000 || *d.Transfers[0].UnallocatedCents != 200000 || d.PatrimonyComplete || d.EligiblePositionCount != 2 {
		t.Fatalf("multi composition: %+v", d)
	}
	seen := map[string]bool{}
	for _, i := range d.Investments {
		for _, f := range i.Funding {
			if seen[f.PurchaseID] {
				t.Fatal("duplicated funding")
			}
			seen[f.PurchaseID] = true
		}
		if i.ID == third.ID && i.Position != nil {
			t.Fatal("fabricated partial position")
		}
	}
	if len(seen) != 2 || !adoptionHas(d, "MISSING_POSITION") || !adoptionHas(d, "POSITION_OVERLAP") {
		t.Fatal("missing partial/overlap review")
	}
}

func TestInvestmentAdoptionGUnknownPrincipal(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	d := adoptionRead(t, p, "2026-02-01")
	i := d.Investments[0]
	if i.RegisteredPrincipalCents == nil || *i.RegisteredPrincipalCents != inv.PrincipalCents || i.ProvenPrincipalCents != nil || i.DeclaredPrincipalCents != nil || i.PrincipalEvidence.State != InvestmentEvidenceUnknown || !adoptionHas(d, "UNKNOWN_PRINCIPAL") {
		t.Fatal("numeric principal conferred provenance")
	}
	// No schema relaxation to fabricate an unsupported persisted state.
	if !adoptionUnsupportedCost(nil) || !adoptionUnsupportedCost(withdrawalPrincipal(0)) || adoptionUnsupportedCost(withdrawalPrincipal(100000)) {
		t.Fatal("unknown/zero/known representation conflated")
	}
	// The diagnostic's defensive path must name the precise schema limitation.
	tx, err := p.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	probe := InvestmentAdoptionDiagnostic{WorkspaceID: "ws-a", AccountID: "acc-inv-a", CutDate: "2026-02-01"}
	item := InvestmentAdoptionItem{ID: inv.ID, AccountID: inv.AccountID, AppliedDate: inv.AppliedDate, CurrentStatus: inv.Status}
	if err = adoptionReadItem(context.Background(), tx, &probe, &item); err != nil {
		t.Fatal(err)
	}
	if !adoptionHas(probe, "UNKNOWN_OR_UNSUPPORTED_ORIGINAL_COST") {
		t.Fatal("unsupported original cost not objectively blocked")
	}
}

func TestInvestmentAdoptionHHistoricalWithdrawal(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	w := settlementWithdrawalFixture(t, p, inv, 50000, nil, InvestmentWithdrawalPartial)
	insertValuationRow(t, p.Immediate, "after-withdrawal", "ws-a", inv.ID, "2026-04-01", 60000)
	d := adoptionRead(t, p, "2026-04-01")
	i := d.Investments[0]
	if len(i.Withdrawals) != 1 || i.Withdrawals[0].Withdrawal.ID != w.ID || i.Withdrawals[0].Withdrawal.Allocations[0].PrincipalCents != nil || i.Withdrawals[0].Settlement != nil || i.ReceiptEvidence.State != InvestmentEvidenceUnknown || !adoptionHas(d, "UNRESOLVED_WITHDRAWAL") || d.State != "BLOCKED" {
		t.Fatalf("unproven withdrawal: %+v", i)
	}
}

func TestInvestmentAdoptionIPartialIntegrationAndLaterEvents(t *testing.T) {
	p, _, inv, root := financeFixture(t)
	financeIntegrate(t, p, inv, root, "integrated", "2026-03-01", 530000, 1)
	input := investmentPurchaseInput(inv.ID)
	input.PurchaseDate = "2026-05-01"
	if _, err := CreateInvestmentPurchase(context.Background(), p.Immediate, input); err != nil {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "future-position", "ws-a", inv.ID, "2026-06-01", 740000)
	d := adoptionRead(t, p, "2026-04-01")
	i := d.Investments[0]
	if i.Reference == nil || i.Reference.ID != root || i.FinancialVersion != 2 || len(i.IntegratedPositions) != 2 || len(d.Adjustments) != 1 || !adoptionHas(d, "EXISTING_FINANCIAL_INTEGRATION") || len(i.EventsAfterCut) != 2 || i.Position.ID != "integrated" || len(i.Purchases) != 1 {
		t.Fatalf("integration boundaries: %+v", i)
	}
	if i.InclusionEvidence.State != InvestmentEvidenceDeclared {
		t.Fatal("manual represented amount promoted to proof")
	}
}

func TestInvestmentAdoptionKWorkspaceIsolationIncludingIndirectAssociations(t *testing.T) {
	p, _, _ := newInvestmentLifecycleDB(t)
	before := adoptionRead(t, p, "2026-04-01")
	foreign := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-b", "acc-inv-b"))
	insertValuationRow(t, p.Immediate, "foreign-pos", "ws-b", foreign.ID, "2026-02-01", 7777777)
	exec(t, p.Immediate, `INSERT INTO categories(id,workspace_id,name,type) VALUES('foreign-cat','ws-b','SECRET CATEGORY','INCOME')`)
	exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('foreign-income','ws-b','user-b','acc-inv-b','INCOME',88888,1772323200,'paid')`)
	after := adoptionRead(t, p, "2026-04-01")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("foreign history affected diagnosis")
	}
	if _, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-b", "acc-inv-a", "2026-04-01"); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign account accepted: %v", err)
	}
	// Old single-column FKs permit hostile indirect ownership associations.
	// A scoped category join must never disclose the foreign category/name.
	exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,category_id,type,amount,date,status) VALUES('bad-category','ws-a','user-a','acc-inv-a','foreign-cat','INCOME',300,1772323200,'paid')`)
	exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,status) VALUES('bad-transfer','ws-a','user-a','acc-inv-b','acc-inv-a','TRANSFER',400,1772323200,'paid')`)
	d := adoptionRead(t, p, "2026-04-01")
	encoded, _ := json.Marshal(d)
	if strings.Contains(string(encoded), "SECRET CATEGORY") || strings.Contains(string(encoded), "foreign-cat") || strings.Contains(string(encoded), "acc-inv-b") || len(d.Transfers) != 0 || !adoptionHas(d, "INVALID_TRANSFER_OWNERSHIP") {
		t.Fatalf("foreign association disclosed: %s", encoded)
	}
}

func TestInvestmentAdoptionLReadOnlyDatabaseAndAllEvidenceTables(t *testing.T) {
	p, inv, s, in := c2aFixture(t, 525500, 530000, false, false)
	a := c2aApply(t, p, in)
	c2bClassify(t, p, c2bInput(t, p, s, a))
	path := queryScalar(t, p.DB, `SELECT file FROM pragma_database_list WHERE name='main'`)
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	reader.SetMaxOpenConns(1)
	defer reader.Close()
	before := adoptionSnapshot(t, p.DB)
	d, err := ReadInvestmentAdoptionDiagnostic(context.Background(), reader, "ws-a", "acc-inv-a", "2026-04-01")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range d.Investments {
		if i.ID == inv.ID {
			found = true
			if len(i.Withdrawals) != 1 || i.Withdrawals[0].ApplicationID == "" || i.Withdrawals[0].ClassificationID == "" || i.ReceiptEvidence.State != InvestmentEvidenceDeclared || i.IncomeRecognitionEvidence.State != InvestmentEvidenceProven {
				t.Fatalf("C1/C2 evidence lost: %+v", i)
			}
		}
	}
	if !found || before != adoptionSnapshot(t, p.DB) {
		t.Fatal("mutation or missing investment")
	}
}

func TestInvestmentAdoptionUnavailableClassificationIsNotRecognizedIncome(t *testing.T) {
	p, inv, s, in := c2aFixtureWithReceipt(t, 525500, 530000, false, true, "TRANSFER")
	a := c2aApply(t, p, in)
	c2bClassify(t, p, c2bInput(t, p, s, a))
	d := adoptionRead(t, p, "2026-04-01")
	found := false
	for _, i := range d.Investments {
		if i.ID == inv.ID {
			found = true
			if i.IncomeRecognitionEvidence.State != InvestmentEvidenceUnknown || len(i.Withdrawals) != 1 || i.Withdrawals[0].ClassificationState != InvestmentClassificationUnavailable || i.Withdrawals[0].Withdrawal.Allocations[0].PrincipalCents != nil {
				t.Fatalf("unknown cost became income: %+v", i)
			}
		}
	}
	if !found {
		t.Fatal("missing classified investment")
	}
}

func TestInvestmentAdoptionSnapshotConsistentDuringConcurrentWriter(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	ctx := context.Background()
	insertValuationRow(t, p.Immediate, "snapshot-position", "ws-a", inv.ID, "2026-02-01", 100000)
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Pin the WAL read snapshot before the independent writer commits.
	var old int64
	if err = tx.QueryRowContext(ctx, `SELECT current_balance FROM accounts WHERE workspace_id='ws-a' AND id='acc-inv-a'`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	exec(t, p.Immediate, `UPDATE accounts SET current_balance=99999 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	adoptionIncomeFixture(t, p, "concurrent-income", "2026-02-01", "paid", 99999)
	d, err := investmentAdoptionDiagnosticTx(ctx, tx, "ws-a", "acc-inv-a", "2026-04-01")
	if err != nil || d.CurrentBalanceCents != old || len(d.IncomeCandidates) != 0 {
		t.Fatalf("mixed snapshots: %+v %v", d, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	newer := adoptionRead(t, p, "2026-04-01")
	if newer.CurrentBalanceCents != 99999 || len(newer.IncomeCandidates) != 1 {
		t.Fatal("new snapshot did not see writer")
	}
}

func TestInvestmentAdoptionValidationTemporalAndOverflow(t *testing.T) {
	p, _, inv := newInvestmentLifecycleDB(t)
	for _, cut := range []string{"", "2026-02-30", "2026-2-01", "2026-02-01T00:00:00Z"} {
		if _, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-a", "acc-inv-a", cut); !errors.Is(err, ErrInvestmentValidation) {
			t.Fatalf("invalid cut %q: %v", cut, err)
		}
	}
	if _, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-a", "acc-checking-a", "2026-02-01"); !errors.Is(err, ErrInvestmentInvalidAccount) {
		t.Fatal(err)
	}
	insertValuationRow(t, p.Immediate, "old", "ws-a", inv.ID, "2026-02-01", 100000)
	input := investmentPurchaseInput(inv.ID)
	input.PurchaseDate = "2026-02-01"
	if _, err := CreateInvestmentPurchase(context.Background(), p.Immediate, input); err != nil {
		t.Fatal(err)
	}
	d := adoptionRead(t, p, "2026-02-01")
	if d.EligiblePositionCount != 0 || !adoptionHas(d, "STALE_EVIDENCE") {
		t.Fatal("same-day ordering inferred")
	}
	// Corrected historical value cannot be certified as its pre-correction value.
	insertValuationRow(t, p.Immediate, "corrected", "ws-a", inv.ID, "2026-03-01", 350000)
	exec(t, p.Immediate, `UPDATE investment_valuations SET updated_at=unixepoch('2026-05-01'),updated_by='user-a' WHERE workspace_id='ws-a' AND id='corrected'`)
	d = adoptionRead(t, p, "2026-04-01")
	if d.Investments[0].PositionEligible || d.Investments[0].PositionEvidence.State != InvestmentEvidenceUnknown {
		t.Fatal("post-cut correction treated as historical proof")
	}
	// Arithmetic uses checked integer sums, never wraps.
	other := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
	insertValuationRow(t, p.Immediate, "max-a", "ws-a", inv.ID, "2026-06-01", math.MaxInt64)
	insertValuationRow(t, p.Immediate, "max-b", "ws-a", other.ID, "2026-06-01", math.MaxInt64)
	if _, err := ReadInvestmentAdoptionDiagnostic(context.Background(), p.DB, "ws-a", "acc-inv-a", "2026-06-01"); !errors.Is(err, ErrBalanceOutOfRange) {
		t.Fatalf("overflow: %v", err)
	}
}
