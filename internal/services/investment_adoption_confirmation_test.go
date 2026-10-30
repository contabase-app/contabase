package services

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
	"modernc.org/sqlite"
)

const adoptionTestCut = "2026-09-30"

func adoptionConfirmationFixture(t *testing.T) (*database.RuntimeDBs, string, []models.Investment) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adoption.db")
	p, err := database.OpenRuntimeDBs(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	seedInvestmentServiceFixtures(t, p.DB)
	exec(t, p.Immediate, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES('ws-a','user-a','ADMIN'),('ws-b','user-b','ADMIN')`)
	var investments []models.Investment
	for n, cost := range []int64{900000, 700000} {
		input := investmentCreateInput("ws-a", "acc-inv-a")
		input.PrincipalCents = cost
		i := mustCreateInvestment(t, p.Immediate, input)
		investments = append(investments, i)
		insertValuationRow(t, p.Immediate, "adoption-position-"+i.ID, "ws-a", i.ID, adoptionTestCut, []int64{1000000, 800000}[n])
	}
	exec(t, p.Immediate, `UPDATE accounts SET current_balance=2000000 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	return p, path, investments
}

func adoptionConfirmationInput(t *testing.T, db *sql.DB, ids ...string) ConfirmInvestmentAdoptionInput {
	t.Helper()
	c, err := ReadInvestmentAdoptionConfirmationCandidate(context.Background(), db, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ReadInvestmentAdoptionConfirmationCandidate(context.Background(), db, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil || !reflect.DeepEqual(c, again) {
		t.Fatalf("candidate unstable: %v", err)
	}
	in := ConfirmInvestmentAdoptionInput{WorkspaceID: "ws-a", AccountID: "acc-inv-a", CreatedBy: "user-a", RequestID: uuid.NewString(), Reason: "Posições individualizadas já representadas no saldo considerado", CutDate: adoptionTestCut, ExpectedBalanceVersion: c.Diagnostic.BalanceVersion, ExpectedAdoptionVersion: c.AdoptionVersion, EvidenceVersion: 1, DeclarationVersion: 1, EvidenceToken: c.EvidenceToken, DayClosed: true, Coverage: InvestmentAdoptionPartial}
	selected := map[string]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	prior := map[string]bool{}
	for _, i := range c.ConfirmedItems {
		prior[i.InvestmentID] = true
	}
	for _, i := range c.Diagnostic.Investments {
		in.ReviewedInvestmentIDs = append(in.ReviewedInvestmentIDs, i.ID)
		if selected[i.ID] {
			if i.Position == nil {
				t.Fatal("fixture without position")
			}
			a, err := ReadInvestmentReferenceCandidate(context.Background(), db, "ws-a", i.ID, i.Position.ID)
			if err != nil {
				t.Fatal(err)
			}
			s := InvestmentAdoptionSelection{InvestmentID: i.ID, PositionID: i.Position.ID, ObservedCents: i.Position.AmountCents, CoverageToken: a.CoverageToken, IncludedInBalance: true, EconomicIdentity: "lot:" + i.ID, IdentityBasis: "Lote contratado individualmente e delimitado pelo identificador " + i.ID}
			if i.Reference != nil {
				s.ExpectedReferenceVersion = i.Reference.Version
				s.ReferenceRevisionID = i.Reference.ID
			}
			in.Items = append(in.Items, s)
		} else if !prior[i.ID] {
			in.Excluded = append(in.Excluded, InvestmentAdoptionExclusion{i.ID, "Componente revisado, confirmação ainda não solicitada"})
		}
		for _, w := range i.Withdrawals {
			in.Dispositions = append(in.Dispositions, InvestmentAdoptionDisposition{"WITHDRAWAL", w.Withdrawal.ID, "REMAINING_POSITION_ONLY", "Somente patrimônio remanescente; recebimento e custo baixado permanecem independentes"})
		}
		for _, e := range i.EventsAfterCut {
			if e.Kind == "WITHDRAWAL" {
				in.Dispositions = append(in.Dispositions, InvestmentAdoptionDisposition{e.Kind, e.ID, "OUTSIDE_CUT_PENDING", "Evento posterior sem admissão financeira"})
			}
		}
	}
	for _, i := range c.Diagnostic.IncomeCandidates {
		disposition := "REVIEWED_UNATTRIBUTED"
		if i.Status != "paid" {
			disposition = "CANDIDATE_PENDING"
		}
		in.Dispositions = append(in.Dispositions, InvestmentAdoptionDisposition{"INCOME", i.ID, disposition, "Receita preservada sem atribuição econômica conclusiva"})
	}
	for _, a := range c.Diagnostic.Adjustments {
		disposition := "REVIEWED_UNATTRIBUTED"
		if a.EconomicDate > adoptionTestCut {
			disposition = "OUTSIDE_CUT_PENDING"
		}
		in.Dispositions = append(in.Dispositions, InvestmentAdoptionDisposition{"ADJUSTMENT", a.ID, disposition, "Ajuste revisado sem nova compensação"})
	}
	for _, e := range c.Diagnostic.EventsAfterCut {
		if e.Kind == "INCOME" {
			in.Dispositions = append(in.Dispositions, InvestmentAdoptionDisposition{e.Kind, e.ID, "OUTSIDE_CUT_PENDING", "Fora da cobertura histórica"})
		}
	}
	return in
}

func adoptionFinancialSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(adoptionSnapshot(t, db), "\n") {
		if !strings.HasPrefix(line, "investment_reference_revisions:") && !strings.HasPrefix(line, "investment_reference_coverage:") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func adoptionAssertNoFinancialChange(t *testing.T, db *sql.DB, before string) {
	t.Helper()
	if got := adoptionFinancialSnapshot(t, db); got != before {
		t.Fatal("financial/source rows changed (complete row/column snapshot)")
	}
}

func TestInvestmentAdoptionConfirmationOneMultipleAndFinancialSnapshots(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(map[bool]string{false: "one-partial", true: "two-with-declared-cash"}[multiple], func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			ids := []string{inv[0].ID}
			if multiple {
				ids = append(ids, inv[1].ID)
			}
			beforeRead := adoptionSnapshot(t, p.DB)
			in := adoptionConfirmationInput(t, p.DB, ids...)
			if beforeRead != adoptionSnapshot(t, p.DB) {
				t.Fatal("candidate mutated sources")
			}
			if multiple {
				in.Coverage = InvestmentAdoptionAllIdentified
				in.Cash = &InvestmentAdoptionCashDeclaration{200000, adoptionTestCut, "Caixa informado por declaração independente"}
				in.DeclaredBalanceAtCutCents = withdrawalPrincipal(2000000)
				in.DeclaredBalanceBasis = "Valor total declarado para o mesmo corte, sem certificação histórica"
			}
			before := adoptionFinancialSnapshot(t, p.DB)
			got, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
			if err != nil || got.Outcome != InvestmentOperationCreated || got.Version != 1 || len(got.Items) != len(ids) || got.FinancialEffectCents != 0 || got.IntegrationAdmission != "PENDING" {
				t.Fatalf("confirmation: %+v %v", got, err)
			}
			sum := int64(0)
			for _, i := range got.Items {
				sum += i.RepresentedCents
				if i.PrincipalEvidence.State != InvestmentEvidenceUnknown || i.FundingEvidence.State == InvestmentEvidenceProven {
					t.Fatal("inclusion proved principal/funding")
				}
			}
			if multiple && (sum != 1800000 || got.Cash.Cents == nil || *got.Cash.Cents != 200000 || got.Cash.Evidence.State != InvestmentEvidenceDeclared) {
				t.Fatal("declared composition lost")
			}
			if !multiple && (sum != 1000000 || got.Cash.Cents != nil || got.Cash.Evidence.State != InvestmentEvidenceUnknown || len(got.Declaration.Excluded) != 1) {
				t.Fatal("partial coverage fabricated cash")
			}
			if got.Diagnostic.BalanceAtCut.Cents != nil || got.Diagnostic.BrokerCash.Cents != nil {
				t.Fatal("current balance became historical proof")
			}
			adoptionAssertNoFinancialChange(t, p.DB, before)
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != len(ids) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions`) != 0 {
				t.Fatal("unexpected A/B rows")
			}
			read, err := ReadInvestmentAdoptionConfirmation(context.Background(), p.DB, "ws-a", "acc-inv-a", got.ID, "user-a")
			if err != nil || !reflect.DeepEqual(read, got.InvestmentAdoptionConfirmation) {
				t.Fatalf("receipt differs: %v", err)
			}
			state, err := ReadInvestmentAdoptionConfirmationValidity(context.Background(), p.DB, "ws-a", "acc-inv-a", got.ID, "user-a")
			if err != nil || state.State != "CURRENT" || state.IntegrationAdmission != "PENDING" {
				t.Fatalf("validity: %+v %v", state, err)
			}
		})
	}
}

func TestInvestmentAdoptionConfirmationExistingReferences(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending-promoted", true: "valid-reused"}[confirmed], func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			a := referenceInput(t, p.DB, inv[0].ID, "adoption-position-"+inv[0].ID)
			a.Confirm = confirmed
			a.RepresentedCents = withdrawalPrincipal(1000000)
			ref, err := RecordInvestmentReference(context.Background(), p.Immediate, a)
			if err != nil {
				t.Fatal(err)
			}
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
			before := adoptionFinancialSnapshot(t, p.DB)
			got, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
			if err != nil {
				t.Fatal(err)
			}
			expected := 2
			if confirmed {
				expected = 1
				if got.Items[0].ReferenceRevisionID != ref.ID {
					t.Fatal("valid A duplicated")
				}
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != expected {
				t.Fatal("wrong reference revision count")
			}
			replay, err := RecordInvestmentReferenceWithOutcome(context.Background(), p.Immediate, a)
			if err != nil || replay.ID != ref.ID || replay.Outcome != InvestmentOperationReplayed {
				t.Fatalf("A replay lost: %+v %v", replay, err)
			}
			newer := referenceInput(t, p.DB, inv[0].ID, "adoption-position-"+inv[0].ID)
			newer.ExpectedVersion = int64(expected)
			newer.RepresentedCents = withdrawalPrincipal(1000000)
			if _, err = RecordInvestmentReference(context.Background(), p.Immediate, newer); !errors.Is(err, ErrInvestmentAdoptionAdmissionPending) {
				t.Fatalf("new A admitted: %v", err)
			}
			adoptionAssertNoFinancialChange(t, p.DB, before)
		})
	}
}

func TestInvestmentAdoptionConfirmationHistoryAndUnknowns(t *testing.T) {
	p, _, inv := adoptionConfirmationFixture(t)
	for _, tc := range []struct{ id, date, status string }{{"historical-income", "2026-08-01", "paid"}, {"same-day-income", adoptionTestCut, "paid"}, {"pending-income", "2026-09-01", "pending"}, {"later-income", "2026-10-01", "paid"}} {
		at, _ := time.Parse("2006-01-02", tc.date)
		exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,date_civil,status) VALUES(?,'ws-a','user-a','acc-inv-a','INCOME',30000,?,?,?)`, tc.id, at.Unix(), tc.date, tc.status)
	}
	financeTransfer(t, p.Immediate, "explicit-source", "2026-01-10",
		inv[0].PrincipalCents+inv[1].PrincipalCents)
	w := withdrawalInput(inv[0].ID, "initial:"+inv[0].ID)
	w.WithdrawalDate = "2026-08-10"
	w.ReceivedCents = 100000
	w.Allocations[0].ReceivedCents = 100000
	withdrawal, err := CreateInvestmentWithdrawal(context.Background(), p.Immediate, w)
	if err != nil {
		t.Fatal(err)
	}
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
	before := adoptionFinancialSnapshot(t, p.DB)
	got, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Items[0].PrincipalEvidence.State != InvestmentEvidenceUnknown || got.Items[0].ReceiptEvidence.State != InvestmentEvidenceUnknown {
		t.Fatal("principal or receipt invented")
	}
	if len(got.Diagnostic.IncomeCandidates) != 3 || len(got.Declaration.Dispositions) != 5 || !adoptionHas(got.Diagnostic, "UNRESOLVED_WITHDRAWAL") {
		t.Fatal("history/pending evidence erased")
	}
	var transferProven bool
	for _, f := range got.Diagnostic.Transfers {
		if f.ID == "explicit-source" && f.Evidence.State == InvestmentEvidenceProven {
			transferProven = true
		}
	}
	if !transferProven || got.Items[0].FundingEvidence.State == InvestmentEvidenceProven {
		t.Fatal("source confused with purchase allocation")
	}
	var principal sql.NullInt64
	if err = p.DB.QueryRow(`SELECT principal_cents FROM investment_withdrawal_allocations WHERE workspace_id='ws-a' AND investment_id=? AND withdrawal_id=?`, inv[0].ID, withdrawal.ID).Scan(&principal); err != nil || principal.Valid {
		t.Fatalf("unknown withdrawn principal replaced: %v", err)
	}
	adoptionAssertNoFinancialChange(t, p.DB, before)
}

func TestInvestmentAdoptionConfirmationDeclarationRejections(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ConfirmInvestmentAdoptionInput)
	}{
		{"duplicate-investment", func(in *ConfirmInvestmentAdoptionInput) { in.Items = append(in.Items, in.Items[0]) }},
		{"same-economic-identity", func(in *ConfirmInvestmentAdoptionInput) {
			in.Items[1].EconomicIdentity = "  " + strings.ToUpper(in.Items[0].EconomicIdentity) + " "
		}},
		{"missing-identity-basis", func(in *ConfirmInvestmentAdoptionInput) { in.Items[0].IdentityBasis = "" }},
		{"principal-effectively-unknown", func(in *ConfirmInvestmentAdoptionInput) { in.Items[0].OriginalCostUnknown = true }},
		{"missing-explicit-inclusion", func(in *ConfirmInvestmentAdoptionInput) { in.Items[0].IncludedInBalance = false }},
		{"missing-close", func(in *ConfirmInvestmentAdoptionInput) { in.DayClosed = false }},
		{"missing-reviewed-component", func(in *ConfirmInvestmentAdoptionInput) { in.ReviewedInvestmentIDs = in.ReviewedInvestmentIDs[:1] }},
		{"observed-cent-divergence", func(in *ConfirmInvestmentAdoptionInput) { in.Items[0].ObservedCents++ }},
		{"declared-total-contradiction", func(in *ConfirmInvestmentAdoptionInput) {
			in.DeclaredBalanceAtCutCents = withdrawalPrincipal(1)
			in.DeclaredBalanceBasis = "Total na mesma data e escopo"
		}},
		{"cash-wrong-date", func(in *ConfirmInvestmentAdoptionInput) {
			in.Cash = &InvestmentAdoptionCashDeclaration{1, "2026-09-29", "Caixa declarado"}
		}},
		{"arithmetic-overflow", func(in *ConfirmInvestmentAdoptionInput) {
			in.Cash = &InvestmentAdoptionCashDeclaration{math.MaxInt64, adoptionTestCut, "Caixa declarado"}
		}},
		{"foreign-position", func(in *ConfirmInvestmentAdoptionInput) { in.Items[0].PositionID = in.Items[1].PositionID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID, inv[1].ID)
			tc.change(&in)
			before := adoptionSnapshot(t, p.DB)
			got, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
			if err == nil || got.Outcome != "" {
				t.Fatalf("invalid declaration accepted: %+v %v", got, err)
			}
			if before != adoptionSnapshot(t, p.DB) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_items`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_confirmations`) != 0 {
				t.Fatal("rejection left domain writes")
			}
			if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_attempts`) != 1 {
				t.Fatal("authorized domain rejection not audited")
			}
		})
	}
	// Defense for unsupported absent/zero cost without invalid rows in the real schema.
	for _, cost := range []*int64{nil, withdrawalPrincipal(0), withdrawalPrincipal(-1)} {
		item := InvestmentAdoptionItem{ID: "i", RegisteredPrincipalCents: cost}
		c := InvestmentAdoptionConfirmationCandidate{Diagnostic: InvestmentAdoptionDiagnostic{Investments: []InvestmentAdoptionItem{item}}}
		in := ConfirmInvestmentAdoptionInput{DayClosed: true, Coverage: InvestmentAdoptionPartial, ReviewedInvestmentIDs: []string{"i"}, Items: []InvestmentAdoptionSelection{{InvestmentID: "i", CoverageToken: strings.Repeat("a", 64), IncludedInBalance: true, EconomicIdentity: "lot", IdentityBasis: "Identificador individual"}}}
		if err := validateAdoptionDeclaration(in, c); !errors.Is(err, ErrInvestmentValidation) {
			t.Fatalf("unsupported cost: %v", err)
		}
	}
}

func TestInvestmentAdoptionConfirmationOverlapRequiresPairResolution(t *testing.T) {
	p, _, inv := adoptionConfirmationFixture(t)
	for _, i := range inv {
		purchase := investmentPurchaseInput(i.ID)
		purchase.PurchaseDate = "2026-07-01"
		purchase.NoteReference = "shared-note"
		if _, err := CreateInvestmentPurchase(context.Background(), p.Immediate, purchase); err != nil {
			t.Fatal(err)
		}
	}
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
	if _, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("unresolved overlap: %v", err)
	}
	in.RequestID = uuid.NewString()
	in.OverlapResolutions = []InvestmentAdoptionOverlapResolution{{inv[0].ID, inv[1].ID, "A nota identifica dois lotes, páginas e números de contratação distintos delimitam cada componente"}}
	if _, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in); err != nil {
		t.Fatal(err)
	}
}

func TestInvestmentAdoptionConfirmationStaleGlobalEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *database.RuntimeDBs, []models.Investment)
	}{
		{"position-without-balance-change", func(t *testing.T, p *database.RuntimeDBs, inv []models.Investment) {
			exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=1010000 WHERE workspace_id='ws-a' AND investment_id=?`, inv[0].ID)
		}},
		{"later-position-full-value", func(t *testing.T, p *database.RuntimeDBs, inv []models.Investment) {
			insertValuationRow(t, p.Immediate, "later-position", "ws-a", inv[0].ID, "2026-10-10", 1010000)
		}},
		{"later-income-full-value", func(t *testing.T, p *database.RuntimeDBs, inv []models.Investment) {
			at, _ := time.Parse("2006-01-02", "2026-10-10")
			exec(t, p.Immediate, `INSERT INTO transactions(id,workspace_id,user_id,account_id,type,amount,date,status) VALUES('later-drift','ws-a','user-a','acc-inv-a','INCOME',17,?,'paid')`, at.Unix())
		}},
		{"composition", func(t *testing.T, p *database.RuntimeDBs, inv []models.Investment) {
			mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-a", "acc-inv-a"))
		}},
		{"balance-version", func(t *testing.T, p *database.RuntimeDBs, inv []models.Investment) {
			exec(t, p.Immediate, `UPDATE accounts SET current_balance=current_balance+1 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
			tc.mutate(t, p, inv)
			before := adoptionSnapshot(t, p.DB)
			if _, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in); !errors.Is(err, ErrInvestmentConflict) {
				t.Fatalf("stale accepted: %v", err)
			}
			if before != adoptionSnapshot(t, p.DB) {
				t.Fatal("stale request mutated source/evidence")
			}
		})
	}
}

func TestInvestmentAdoptionConfirmationIdempotencyImmutableAndCurrentValidity(t *testing.T) {
	p, _, inv := adoptionConfirmationFixture(t)
	ctx := context.Background()
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID, inv[1].ID)
	first, err := ConfirmInvestmentAdoption(ctx, p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	before := adoptionSnapshot(t, p.DB)
	// Set ordering is canonical, and caller-owned slices remain unchanged.
	original := append([]InvestmentAdoptionSelection(nil), in.Items...)
	in.Items[0], in.Items[1] = in.Items[1], in.Items[0]
	in.ReviewedInvestmentIDs[0], in.ReviewedInvestmentIDs[1] = in.ReviewedInvestmentIDs[1], in.ReviewedInvestmentIDs[0]
	replay, err := ConfirmInvestmentAdoption(ctx, p.Immediate, in)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(first.InvestmentAdoptionConfirmation, replay.InvestmentAdoptionConfirmation) {
		t.Fatalf("canonical replay: %+v %v", replay, err)
	}
	if in.Items[0].InvestmentID != original[1].InvestmentID {
		t.Fatal("normalization mutated caller input")
	}
	if before != adoptionSnapshot(t, p.DB) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_attempts`) != 0 {
		t.Fatal("replay wrote evidence")
	}
	divergent := in
	divergent.Reason = "Motivo divergente na mesma chave"
	if got, e := ConfirmInvestmentAdoption(ctx, p.Immediate, divergent); !errors.Is(e, ErrInvestmentConflict) || got.Outcome != "" {
		t.Fatalf("divergence: %+v %v", got, e)
	}
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_attempts WHERE conflicting_confirmation_id IS NOT NULL`) != 1 {
		t.Fatal("conflict lost accepted receipt link")
	}
	for _, table := range []string{"investment_adoption_confirmations", "investment_adoption_items", "investment_adoption_attempts"} {
		for _, q := range []string{`UPDATE ` + table + ` SET account_id=account_id`, `DELETE FROM ` + table, `INSERT OR REPLACE INTO ` + table + ` SELECT * FROM ` + table} {
			if _, e := p.Immediate.Exec(q); e == nil {
				t.Fatalf("immutable evidence accepted: %s", q)
			}
		}
	}
	exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=amount_cents+1 WHERE workspace_id='ws-a' AND investment_id=?`, inv[0].ID)
	state, err := ReadInvestmentAdoptionConfirmationValidity(ctx, p.DB, "ws-a", "acc-inv-a", first.ID, "user-a")
	if err != nil || state.State != "STALE_EVIDENCE" {
		t.Fatalf("validity did not detect drift: %+v %v", state, err)
	}
	replay, err = ConfirmInvestmentAdoption(ctx, p.Immediate, in)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(first.InvestmentAdoptionConfirmation, replay.InvestmentAdoptionConfirmation) {
		t.Fatalf("historical replay reapproved or changed evidence: %v", err)
	}
}

func TestInvestmentAdoptionConfirmationComplement(t *testing.T) {
	p, _, inv := adoptionConfirmationFixture(t)
	ctx := context.Background()
	first, err := ConfirmInvestmentAdoption(ctx, p.Immediate, adoptionConfirmationInput(t, p.DB, inv[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	in := adoptionConfirmationInput(t, p.DB, inv[1].ID)
	in.Coverage = InvestmentAdoptionAllIdentified
	before := adoptionFinancialSnapshot(t, p.DB)
	second, err := ConfirmInvestmentAdoption(ctx, p.Immediate, in)
	if err != nil || second.Version != 2 || len(second.Items) != 2 {
		t.Fatalf("complement: %+v %v", second, err)
	}
	found := false
	for _, i := range second.Items {
		if reflect.DeepEqual(i, first.Items[0]) {
			found = true
		}
	}
	if !found || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != 2 {
		t.Fatal("complement changed prior component")
	}
	duplicate := adoptionConfirmationInput(t, p.DB, inv[0].ID)
	if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, duplicate); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("adopted component reattributed: %v", err)
	}
	adoptionAssertNoFinancialChange(t, p.DB, before)
}

func TestInvestmentAdoptionConfirmationRollbackSecondReference(t *testing.T) {
	for _, domain := range []bool{false, true} {
		t.Run(map[bool]string{false: "infrastructure-full-rollback", true: "domain-savepoint-and-audit"}[domain], func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID, inv[1].ID)
			sort.Slice(in.Items, func(i, j int) bool { return in.Items[i].InvestmentID < in.Items[j].InvestmentID })
			if domain {
				// A deliberately injected dependency change inside the first A INSERT makes
				// the second token stale after the initial consistent validation.
				exec(t, p.Immediate, `CREATE TRIGGER injected_adoption_domain AFTER INSERT ON investment_reference_revisions WHEN NEW.investment_id='`+in.Items[0].InvestmentID+`' BEGIN UPDATE investment_valuations SET amount_cents=amount_cents+1 WHERE workspace_id='ws-a' AND investment_id='`+in.Items[1].InvestmentID+`'; END`)
			} else {
				exec(t, p.Immediate, `CREATE TRIGGER injected_adoption_failure BEFORE INSERT ON investment_reference_revisions WHEN NEW.investment_id='`+in.Items[1].InvestmentID+`' BEGIN SELECT RAISE(ABORT,'injected second reference failure'); END`)
			}
			before := adoptionSnapshot(t, p.DB)
			got, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
			if err == nil || got.Outcome != "" {
				t.Fatal("failure returned success")
			}
			if domain && !errors.Is(err, ErrInvestmentConflict) {
				t.Fatalf("wrong domain failure: %v", err)
			}
			want := 0
			if domain {
				want = 1
			}
			if before != adoptionSnapshot(t, p.DB) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_items`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_confirmations`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_attempts`) != want {
				t.Fatal("partial A/confirmation/audit survived rollback")
			}
		})
	}
}

func TestInvestmentAdoptionConfirmationConcurrentWriters(t *testing.T) {
	for _, kind := range []string{"same-request", "different-investments", "divergent-payload"} {
		for _, deferred := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-immediate", true: "-deferred-defensive-reservation"}[deferred], func(t *testing.T) {
				p, path, inv := adoptionConfirmationFixture(t)
				in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
				other := in
				if kind == "different-investments" {
					other = adoptionConfirmationInput(t, p.DB, inv[1].ID)
				}
				if kind == "divergent-payload" {
					other.Reason = "Outro motivo na mesma chave"
				}
				before := adoptionFinancialSnapshot(t, p.DB)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				atCommit, atBegin, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				defer once.Do(func() { close(release) })
				dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
				if !deferred {
					dsn += "&_txlock=immediate"
				}
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
				type result struct {
					r InvestmentAdoptionConfirmationResult
					e error
				}
				a, b := make(chan result, 1), make(chan result, 1)
				go func() { r, e := ConfirmInvestmentAdoption(ctx, first, in); a <- result{r, e} }()
				select {
				case <-atCommit:
				case <-ctx.Done():
					t.Fatal("first did not reach commit")
				}
				go func() { r, e := ConfirmInvestmentAdoption(ctx, second, other); b <- result{r, e} }()
				select {
				case <-atBegin:
				case <-ctx.Done():
					t.Fatal("second did not reach begin")
				}
				once.Do(func() { close(release) })
				x, y := <-a, <-b
				if x.e != nil || x.r.Outcome != InvestmentOperationCreated {
					t.Fatalf("first: %+v", x)
				}
				if kind == "same-request" {
					if y.e != nil || y.r.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(x.r.InvestmentAdoptionConfirmation, y.r.InvestmentAdoptionConfirmation) {
						t.Fatalf("replay: %+v", y)
					}
				} else if !errors.Is(y.e, ErrInvestmentConflict) || y.r.Outcome != "" {
					t.Fatalf("competitor: %+v", y)
				}
				if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_reference_revisions`) != 1 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_confirmations`) != 1 {
					t.Fatal("duplicate confirmation")
				}
				adoptionAssertNoFinancialChange(t, p.DB, before)
			})
		}
	}
}

func TestInvestmentAdoptionConfirmationIsolationAndPermission(t *testing.T) {
	p, _, inv := adoptionConfirmationFixture(t)
	ctx := context.Background()
	foreign := mustCreateInvestment(t, p.Immediate, investmentCreateInput("ws-b", "acc-inv-b"))
	insertValuationRow(t, p.Immediate, "foreign-position", "ws-b", foreign.ID, adoptionTestCut, 123)
	candidate, err := ReadInvestmentAdoptionConfirmationCandidate(ctx, p.DB, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=456 WHERE workspace_id='ws-b' AND investment_id=?`, foreign.ID)
	after, err := ReadInvestmentAdoptionConfirmationCandidate(ctx, p.DB, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil || candidate.EvidenceToken != after.EvidenceToken {
		t.Fatal("foreign tenant changed evidence")
	}
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
	foreignInput := in
	foreignInput.AccountID = "acc-inv-b"
	if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, foreignInput); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign account: %v", err)
	}
	foreignInput = in
	foreignInput.Items = append([]InvestmentAdoptionSelection(nil), in.Items...)
	foreignInput.Items[0].InvestmentID = foreign.ID
	foreignInput.Items[0].PositionID = "foreign-position"
	if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, foreignInput); !errors.Is(err, ErrInvestmentNotFound) {
		t.Fatalf("foreign investment: %v", err)
	}
	exec(t, p.Immediate, `UPDATE workspace_members SET role='USER' WHERE workspace_id='ws-a' AND user_id='user-a'`)
	if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, in); !errors.Is(err, ErrInvestmentAdoptionUnauthorized) {
		t.Fatalf("revoked permission: %v", err)
	}
	exec(t, p.Immediate, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id='ws-a' AND user_id='user-a'`)
	got, err := ConfirmInvestmentAdoption(ctx, p.Immediate, in)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, p.Immediate, `DELETE FROM workspace_members WHERE workspace_id='ws-a' AND user_id='user-a'`)
	if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, in); !errors.Is(err, ErrInvestmentAdoptionUnauthorized) {
		t.Fatalf("unauthorized replay: %v", err)
	}
	if _, err = ReadInvestmentAdoptionConfirmation(ctx, p.DB, "ws-a", "acc-inv-a", got.ID, "user-a"); !errors.Is(err, ErrInvestmentAdoptionUnauthorized) {
		t.Fatalf("unauthorized receipt: %v", err)
	}
	if countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_items WHERE workspace_id='ws-b'`) != 0 {
		t.Fatal("foreign tenant mutated")
	}
}

func TestInvestmentAdoptionConfirmationBlocksBAndPreservesOldReplay(t *testing.T) {
	p, _, inv := adoptionConfirmationFixture(t)
	ctx := context.Background()
	got, err := ConfirmInvestmentAdoption(ctx, p.Immediate, adoptionConfirmationInput(t, p.DB, inv[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	in := financeInput(t, p.DB, inv[0].ID, got.Items[0].PositionID, got.Items[0].ReferenceRevisionID, 0)
	before := adoptionSnapshot(t, p.DB)
	if _, err = IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, in); !errors.Is(err, ErrInvestmentAdoptionAdmissionPending) {
		t.Fatalf("adopted B root admitted: %v", err)
	}
	if before != adoptionSnapshot(t, p.DB) {
		t.Fatal("blocked B changed state")
	}
	// A non-adopted investment on the same account retains the original A/B path.
	a := referenceInput(t, p.DB, inv[1].ID, "adoption-position-"+inv[1].ID)
	a.RepresentedCents = withdrawalPrincipal(800000)
	root, err := RecordInvestmentReference(ctx, p.Immediate, a)
	if err != nil {
		t.Fatal(err)
	}
	b := financeInput(t, p.DB, inv[1].ID, "adoption-position-"+inv[1].ID, root.ID, 0)
	original, err := IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, b)
	if err != nil || original.Outcome != InvestmentOperationCreated {
		t.Fatalf("unadopted B changed: %+v %v", original, err)
	}
	adoption := adoptionConfirmationInput(t, p.DB, inv[1].ID)
	if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, adoption); !errors.Is(err, ErrInvestmentConflict) {
		t.Fatalf("existing B re-adopted: %v", err)
	}
	replay, err := IntegrateInvestmentPositionWithOutcome(ctx, p.Immediate, b)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(original.InvestmentIntegrationResult, replay.InvestmentIntegrationResult) {
		t.Fatalf("old B replay lost: %v", err)
	}
}

func TestInvestmentAdoptionConfirmationCivilCutAndSameDay(t *testing.T) {
	for _, kind := range []string{"different-dates", "corrected-after-cut", "same-day-closed"} {
		t.Run(kind, func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			switch kind {
			case "different-dates":
				// The shared fixture registers a synthetic position at the cut date.
				// Rebuild exactly that row at 2026-09-29 instead of mutating the
				// immutable valuation identity. No reference, integration or adoption
				// item depends on the fresh fixture position at this point.
				valuationID := "adoption-position-" + inv[0].ID
				for _, table := range []string{"investment_reference_revisions", "investment_financial_adjustments", "investment_adoption_items"} {
					var dependent int
					if err := p.DB.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE workspace_id='ws-a' AND investment_id=? AND position_id=?`, inv[0].ID, valuationID).Scan(&dependent); err != nil {
						t.Fatal(err)
					}
					if dependent != 0 {
						t.Fatalf("synthetic position has %d dependent rows in %s", dependent, table)
					}
				}
				res, err := p.Immediate.Exec(`DELETE FROM investment_valuations WHERE workspace_id=? AND investment_id=? AND id=?`, "ws-a", inv[0].ID, valuationID)
				if err != nil {
					t.Fatal(err)
				}
				if affected, err := res.RowsAffected(); err != nil || affected != 1 {
					t.Fatalf("synthetic position delete affected %d rows: %v", affected, err)
				}
				insertValuationRow(t, p.Immediate, valuationID, "ws-a", inv[0].ID, "2026-09-29", 1000000)
				var cutPositions, syntheticPositions int
				if err := p.DB.QueryRow(`SELECT
					(SELECT COUNT(*) FROM investment_valuations WHERE workspace_id='ws-a' AND investment_id=? AND valuation_date='2026-09-30'),
					(SELECT COUNT(*) FROM investment_valuations WHERE workspace_id='ws-a' AND investment_id=? AND valuation_date='2026-09-29' AND amount_cents=1000000)`,
					inv[0].ID, inv[0].ID).Scan(&cutPositions, &syntheticPositions); err != nil || cutPositions != 0 || syntheticPositions != 1 {
					t.Fatalf("synthetic position state: cut=%d dated=%d err=%v", cutPositions, syntheticPositions, err)
				}
			case "corrected-after-cut":
				exec(t, p.Immediate, `UPDATE investment_valuations SET updated_by='user-a',updated_at=? WHERE workspace_id='ws-a' AND investment_id=?`, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix(), inv[0].ID)
			case "same-day-closed":
				purchase := investmentPurchaseInput(inv[0].ID)
				purchase.PurchaseDate = adoptionTestCut
				if _, err := CreateInvestmentPurchase(context.Background(), p.Immediate, purchase); err != nil {
					t.Fatal(err)
				}
			}
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
			before := adoptionFinancialSnapshot(t, p.DB)
			got, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
			if kind == "same-day-closed" {
				if err != nil || got.Items[0].RepresentedCents != 1000000 {
					t.Fatalf("same-day declaration: %v", err)
				}
			} else if !errors.Is(err, ErrInvestmentConflict) {
				t.Fatalf("invalid civil evidence admitted: %v", err)
			} else if got.Outcome != "" ||
				countRows(t, p.DB, `SELECT COUNT(*) FROM investment_reference_revisions WHERE workspace_id='ws-a'`) != 0 ||
				countRows(t, p.DB, `SELECT COUNT(*) FROM investment_financial_revisions WHERE workspace_id='ws-a'`) != 0 ||
				countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_confirmations WHERE workspace_id='ws-a'`) != 0 ||
				countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_attempts WHERE workspace_id='ws-a' AND code='CONFLICT'`) != 1 {
				t.Fatal("rejected civil evidence left success, A/B or adoption writes")
			}
			adoptionAssertNoFinancialChange(t, p.DB, before)
		})
	}
}

type adoptionFailCommitConnector struct{ dsn string }

func (c *adoptionFailCommitConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *adoptionFailCommitConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &adoptionFailCommitConn{conn}, nil
}

type adoptionFailCommitConn struct{ driver.Conn }

func (c *adoptionFailCommitConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &adoptionFailCommitTx{tx}, nil
}

type adoptionFailCommitTx struct{ driver.Tx }

func (t *adoptionFailCommitTx) Commit() error {
	_ = t.Tx.Rollback()
	return errors.New("injected commit failure after real transactional writes")
}

func TestInvestmentAdoptionConfirmationCommitFailureNoSuccess(t *testing.T) {
	p, path, inv := adoptionConfirmationFixture(t)
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID, inv[1].ID)
	db := sql.OpenDB(&adoptionFailCommitConnector{path + "?_txlock=immediate&_pragma=foreign_keys(1)"})
	db.SetMaxOpenConns(1)
	defer db.Close()
	before := adoptionSnapshot(t, p.DB)
	got, err := ConfirmInvestmentAdoption(context.Background(), db, in)
	var commitErr *InvestmentCommitError
	if !errors.As(err, &commitErr) || got.Outcome != "" {
		t.Fatalf("uncertain commit returned success: %+v %v", got, err)
	}
	if before != adoptionSnapshot(t, p.DB) || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_confirmations`) != 0 || countRows(t, p.DB, `SELECT COUNT(*) FROM investment_adoption_attempts`) != 0 {
		t.Fatal("failed commit leaked writes or durable audit promise")
	}
}

// Snapshot hook observes a real WAL commit after the read snapshot has already
// been established. The candidate must retain the old row and token consistently.
func TestInvestmentAdoptionConfirmationCandidateConsistentSnapshot(t *testing.T) {
	p, path, _ := adoptionConfirmationFixture(t)
	before, err := ReadInvestmentAdoptionConfirmationCandidate(context.Background(), p.DB, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(&adoptionSnapshotConnector{dsn: path + "?_pragma=busy_timeout(5000)", write: func() {
		exec(t, p.Immediate, `UPDATE accounts SET current_balance=current_balance+7 WHERE workspace_id='ws-a' AND id='acc-inv-a'`)
	}})
	db.SetMaxOpenConns(1)
	defer db.Close()
	got, err := ReadInvestmentAdoptionConfirmationCandidate(context.Background(), db, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil {
		t.Fatal(err)
	}
	if got.EvidenceToken != before.EvidenceToken {
		t.Fatal("candidate mixed WAL snapshots")
	}
	after, err := ReadInvestmentAdoptionConfirmationCandidate(context.Background(), p.DB, "ws-a", "acc-inv-a", adoptionTestCut)
	if err != nil || after.EvidenceToken == before.EvidenceToken {
		t.Fatal("fresh candidate did not see committed drift")
	}
	var evidence any
	if err = json.Unmarshal(got.EvidenceSnapshot, &evidence); err != nil {
		t.Fatal(err)
	}
}

// Independent of the preexisting I3.2A/WIP fixture loaders.
type adoptionSnapshotConnector struct {
	dsn   string
	once  sync.Once
	write func()
}

func (c *adoptionSnapshotConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *adoptionSnapshotConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &adoptionSnapshotConn{Conn: conn, owner: c}, nil
}

type adoptionSnapshotConn struct {
	driver.Conn
	owner *adoptionSnapshotConnector
}

func (c *adoptionSnapshotConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *adoptionSnapshotConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "SELECT id,account_id,applied_date,status,principal_cents") {
		c.owner.once.Do(c.owner.write)
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// Inject only the bound receipt at INSERT, through a real SQLite connection.
// Every constraint, trigger and FK stays active. Row-local metadata is valid;
// these deliberately inconsistent component/evidence sets require the reader.
type adoptionReceiptInsertConnector struct {
	dsn    string
	mutate func(string) string
}

func (c *adoptionReceiptInsertConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *adoptionReceiptInsertConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &adoptionReceiptInsertConn{Conn: conn, mutate: c.mutate}, nil
}

type adoptionReceiptInsertConn struct {
	driver.Conn
	mutate func(string) string
}

func (c *adoptionReceiptInsertConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *adoptionReceiptInsertConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "INSERT INTO investment_adoption_confirmations(") {
		args = append([]driver.NamedValue(nil), args...)
		args[17].Value = c.mutate(args[17].Value.(string))
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func adoptionReceiptStateSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	out := adoptionSnapshot(t, db)
	for _, table := range []string{"investment_adoption_confirmations", "investment_adoption_items", "investment_adoption_attempts"} {
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
			v := make([]any, len(cols))
			dest := make([]any, len(cols))
			for n := range v {
				dest[n] = &v[n]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			b, err := json.Marshal(v)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out += table + ":" + string(b) + "\n"
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestInvestmentAdoptionReceiptIntegrityRejectsPersistedInconsistency(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"empty-component", func(r map[string]any) { r["Items"] = []any{map[string]any{}} }},
		{"omitted-component", func(r map[string]any) { r["Items"] = r["Items"].([]any)[:1] }},
		{"additional-component", func(r map[string]any) {
			items := r["Items"].([]any)
			additional := map[string]any{}
			for k, v := range items[0].(map[string]any) {
				additional[k] = v
			}
			additional["InvestmentID"] = "additional-investment"
			r["Items"] = append(items, additional)
		}},
		{"duplicate-component", func(r map[string]any) { items := r["Items"].([]any); items[1] = items[0] }},
		{"value", func(r map[string]any) { r["Items"].([]any)[0].(map[string]any)["RepresentedCents"] = 1000001 }},
		{"value-null", func(r map[string]any) { r["Items"].([]any)[0].(map[string]any)["RepresentedCents"] = nil }},
		{"reference", func(r map[string]any) {
			r["Items"].([]any)[0].(map[string]any)["ReferenceRevisionID"] = uuid.NewString()
		}},
		{"position", func(r map[string]any) { r["Items"].([]any)[0].(map[string]any)["PositionID"] = "foreign-position" }},
		{"identity", func(r map[string]any) { r["Items"].([]any)[0].(map[string]any)["EconomicIdentity"] = "another-lot" }},
		{"principal-proof", func(r map[string]any) {
			r["Items"].([]any)[0].(map[string]any)["PrincipalEvidence"].(map[string]any)["State"] = "PROVEN"
		}},
		{"diagnostic", func(r map[string]any) { r["Diagnostic"].(map[string]any)["KnownPatrimonyCents"] = 1 }},
		{"declaration-items", func(r map[string]any) { r["Declaration"].(map[string]any)["Items"] = nil }},
		{"cash-evidence", func(r map[string]any) {
			r["Cash"].(map[string]any)["Evidence"].(map[string]any)["Basis"] = "Altered evidence"
		}},
		{"declared-balance", func(r map[string]any) { r["DeclaredBalanceAtCutCents"] = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, path, inv := adoptionConfirmationFixture(t)
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID, inv[1].ID)
			db := sql.OpenDB(&adoptionReceiptInsertConnector{dsn: path + "?_txlock=immediate&_pragma=foreign_keys(1)", mutate: func(raw string) string {
				var r map[string]any
				dec := json.NewDecoder(strings.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&r); err != nil {
					t.Fatal(err)
				}
				tc.change(r)
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}})
			db.SetMaxOpenConns(1)
			defer db.Close()
			before := adoptionFinancialSnapshot(t, p.DB)
			got, err := ConfirmInvestmentAdoption(context.Background(), db, in)
			if err != nil || got.Outcome != InvestmentOperationCreated {
				t.Fatalf("schema-valid adversarial fixture rejected before reader: %v", err)
			}
			state := adoptionReceiptStateSnapshot(t, p.DB)
			read, err := ReadInvestmentAdoptionConfirmation(context.Background(), p.DB, "ws-a", "acc-inv-a", got.ID, "user-a")
			if err == nil || errors.Is(err, ErrInvestmentNotFound) || !reflect.DeepEqual(read, InvestmentAdoptionConfirmation{}) {
				t.Fatalf("invalid receipt read as success/not-found: %+v %v", read, err)
			}
			replay, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
			if err == nil || replay.Outcome != "" || !reflect.DeepEqual(replay.InvestmentAdoptionConfirmation, InvestmentAdoptionConfirmation{}) {
				t.Fatalf("invalid receipt replayed: %+v %v", replay, err)
			}
			if state != adoptionReceiptStateSnapshot(t, p.DB) {
				t.Fatal("read/replay changed durable state or journal")
			}
			adoptionAssertNoFinancialChange(t, p.DB, before)
		})
	}
}

func TestInvestmentAdoptionReceiptIntegrityHistoricalAndReplay(t *testing.T) {
	for _, cash := range []string{"UNKNOWN", "DECLARED"} {
		t.Run(cash, func(t *testing.T) {
			p, _, inv := adoptionConfirmationFixture(t)
			ctx := context.Background()
			in := adoptionConfirmationInput(t, p.DB, inv[0].ID)
			if cash == "DECLARED" {
				in.Cash = &InvestmentAdoptionCashDeclaration{0, adoptionTestCut, "Explicit zero cash declaration"}
			}
			before := adoptionFinancialSnapshot(t, p.DB)
			first, err := ConfirmInvestmentAdoption(ctx, p.Immediate, in)
			if err != nil {
				t.Fatal(err)
			}
			if first.Coverage != InvestmentAdoptionPartial || first.Cash.Evidence.State != InvestmentEvidenceState(cash) || first.Items[0].PrincipalEvidence.State != InvestmentEvidenceUnknown {
				t.Fatal("legitimate partial/unknown dimensions changed")
			}
			if _, err = ConfirmInvestmentAdoption(ctx, p.Immediate, adoptionConfirmationInput(t, p.DB, inv[1].ID)); err != nil {
				t.Fatal(err)
			}
			// The old receipt excludes the later complement and survives source drift.
			exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=amount_cents+1 WHERE workspace_id='ws-a' AND investment_id=?`, inv[0].ID)
			state := adoptionReceiptStateSnapshot(t, p.DB)
			read, err := ReadInvestmentAdoptionConfirmation(ctx, p.DB, "ws-a", "acc-inv-a", first.ID, "user-a")
			if err != nil || !reflect.DeepEqual(read, first.InvestmentAdoptionConfirmation) {
				t.Fatalf("historical receipt changed: %v", err)
			}
			replay, err := ConfirmInvestmentAdoption(ctx, p.Immediate, in)
			if err != nil || replay.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(replay.InvestmentAdoptionConfirmation, read) {
				t.Fatalf("historical replay: %v", err)
			}
			if _, err = ReadInvestmentAdoptionConfirmation(ctx, p.DB, "ws-b", "acc-inv-b", first.ID, "user-b"); !errors.Is(err, ErrInvestmentNotFound) {
				t.Fatalf("foreign receipt exposed: %v", err)
			}
			if state != adoptionReceiptStateSnapshot(t, p.DB) {
				t.Fatal("read/replay wrote state")
			}
			// Only the explicit fixture drift differs from the financial snapshot.
			exec(t, p.Immediate, `UPDATE investment_valuations SET amount_cents=amount_cents-1 WHERE workspace_id='ws-a' AND investment_id=?`, inv[0].ID)
			adoptionAssertNoFinancialChange(t, p.DB, before)
		})
	}
}

func TestInvestmentAdoptionReceiptIntegrityUnorderedComponents(t *testing.T) {
	p, path, inv := adoptionConfirmationFixture(t)
	in := adoptionConfirmationInput(t, p.DB, inv[0].ID, inv[1].ID)
	db := sql.OpenDB(&adoptionReceiptInsertConnector{dsn: path + "?_txlock=immediate&_pragma=foreign_keys(1)", mutate: func(raw string) string {
		var receipt InvestmentAdoptionConfirmation
		if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
			t.Fatal(err)
		}
		receipt.Items[0], receipt.Items[1] = receipt.Items[1], receipt.Items[0]
		b, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}})
	db.SetMaxOpenConns(1)
	defer db.Close()
	created, err := ConfirmInvestmentAdoption(context.Background(), db, in)
	if err != nil {
		t.Fatal(err)
	}
	before := adoptionReceiptStateSnapshot(t, p.DB)
	read, err := ReadInvestmentAdoptionConfirmation(context.Background(), p.DB, "ws-a", "acc-inv-a", created.ID, "user-a")
	if err != nil || len(read.Items) != 2 || read.Items[0].InvestmentID != created.Items[1].InvestmentID {
		t.Fatalf("item order treated as identity: %v", err)
	}
	replay, err := ConfirmInvestmentAdoption(context.Background(), p.Immediate, in)
	if err != nil || replay.Outcome != InvestmentOperationReplayed || !reflect.DeepEqual(read, replay.InvestmentAdoptionConfirmation) {
		t.Fatalf("unordered replay: %v", err)
	}
	if before != adoptionReceiptStateSnapshot(t, p.DB) {
		t.Fatal("unordered read/replay wrote state")
	}
}
