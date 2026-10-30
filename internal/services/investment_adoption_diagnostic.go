package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
)

const (
	InvestmentAdoptionNoIdentifiedBlockers = "NO_IDENTIFIED_BLOCKERS"
	InvestmentAdoptionNeedsReview          = "NEEDS_REVIEW"
	InvestmentAdoptionBlocked              = "BLOCKED"
)

type InvestmentEvidenceState string

const (
	InvestmentEvidenceProven      InvestmentEvidenceState = "PROVEN"
	InvestmentEvidenceDeclared    InvestmentEvidenceState = "DECLARED"
	InvestmentEvidenceInferred    InvestmentEvidenceState = "INFERRED"
	InvestmentEvidenceUnknown     InvestmentEvidenceState = "UNKNOWN"
	InvestmentEvidenceConflicting InvestmentEvidenceState = "CONFLICTING"
)

// Evidence describes one dimension, never the trustworthiness of an entire lot.
// PROVEN means verified against a ContaBase contract, not external bank audit.
type InvestmentAdoptionEvidence struct {
	State InvestmentEvidenceState
	IDs   []string
	Basis string
}

type InvestmentAdoptionAmount struct {
	Cents    *int64
	Evidence InvestmentAdoptionEvidence
}

type InvestmentAdoptionIssue struct {
	Code, InvestmentID, RecordID, Detail string
	Blocking                             bool
}

type InvestmentAdoptionDiagnostic struct {
	WorkspaceID, AccountID, CutDate string
	State                           string
	// This snapshot reports current records by economic date. It is not an
	// assertion about what the database contained on the historical cut date.
	CurrentBalanceCents, InitialBalanceCents, BalanceVersion int64
	BalanceAtCut, BrokerCash                                 InvestmentAdoptionAmount
	KnownPatrimonyCents                                      int64
	PositionCount, EligiblePositionCount                     int
	PatrimonyComplete                                        bool
	// A comparison with CURRENT balance only; never cash or an historical gap.
	CurrentBalanceMinusKnownPatrimonyCents *int64
	Investments                            []InvestmentAdoptionItem
	Transfers                              []InvestmentAdoptionTransfer
	IncomeCandidates                       []InvestmentAdoptionIncome
	Adjustments                            []InvestmentAdoptionAdjustment
	EventsAfterCut                         []InvestmentAdoptionEvent
	Issues                                 []InvestmentAdoptionIssue
	Limitations                            []string
}

type InvestmentAdoptionItem struct {
	ID, AccountID, AppliedDate, CurrentStatus    string
	RegisteredPrincipalCents                     *int64
	RedeemedDate                                 *string
	ArchivedAt                                   *int64
	ProvenPrincipalCents, DeclaredPrincipalCents *int64
	PositionEvidence, InclusionEvidence          InvestmentAdoptionEvidence
	PrincipalEvidence, FundingEvidence           InvestmentAdoptionEvidence
	ReceiptEvidence, IncomeRecognitionEvidence   InvestmentAdoptionEvidence
	Position                                     *models.InvestmentValuation
	PositionEligible                             bool
	Purchases                                    []models.InvestmentPurchase
	Withdrawals                                  []InvestmentAdoptionWithdrawal
	Reference                                    *models.InvestmentReferenceRevision
	FinancialRevisionID                          string
	FinancialVersion                             int64
	IntegratedPositions                          []models.InvestmentValuation
	Funding                                      []InvestmentAdoptionFunding
	UnfundedPurchaseIDs                          []string
	UncoveredPurchaseIDs, UncoveredWithdrawalIDs []string
	EventsAfterCut                               []InvestmentAdoptionEvent
	// Private current records used for validation; they never imply cut coverage.
	positions []models.InvestmentValuation
}

type InvestmentAdoptionEvent struct{ Kind, ID, EconomicDate string }

type InvestmentAdoptionFunding struct {
	PurchaseID, TransferID string
	AmountCents            int64
	Evidence               InvestmentAdoptionEvidence
}

type InvestmentAdoptionTransfer struct {
	ID, SourceAccountID, DestinationAccountID, EconomicDate, Status string
	AmountCents, AllocatedCents                                     int64
	// Unallocated amount is arithmetic, not proven funding capacity/free cash.
	UnallocatedCents *int64
	Evidence         InvestmentAdoptionEvidence
}

type InvestmentAdoptionIncome struct {
	ID, AccountID, CategoryID, Category, Description, Notes, AttachmentReference string
	EconomicDate, PaidDate, Status                                               string
	AmountCents                                                                  int64
	Evidence                                                                     InvestmentAdoptionEvidence
	Overlaps                                                                     []InvestmentAdoptionIncomeOverlap
}

type InvestmentAdoptionIncomeOverlap struct {
	InvestmentID, FromPositionID, ToPositionID, FromDate, ToDate, Reason string
	VariationCents                                                       *int64
	Integrated                                                           bool
}

type InvestmentAdoptionAdjustment struct {
	ID, Reason, CreatedBy, RecordedDate, EconomicDate string
	DeltaCents                                        int64
}

type InvestmentAdoptionWithdrawal struct {
	Withdrawal                      models.InvestmentWithdrawal
	Settlement                      *InvestmentSettlement
	ApplicationID, ClassificationID string
	ClassificationState             string
	ReceiptEvidence                 InvestmentAdoptionEvidence
}

func adoptionEvidence(state InvestmentEvidenceState, basis string, ids ...string) InvestmentAdoptionEvidence {
	return InvestmentAdoptionEvidence{State: state, Basis: basis, IDs: ids}
}

func (d *InvestmentAdoptionDiagnostic) issue(code, investment, record, detail string, blocking bool) {
	d.Issues = append(d.Issues, InvestmentAdoptionIssue{code, investment, record, detail, blocking})
}

// ReadInvestmentAdoptionDiagnostic owns one deferred read snapshot. It does not
// call financial writers, materializers, migrations or public DB readers that
// open nested transactions. No value in the result authorizes an integration.
func ReadInvestmentAdoptionDiagnostic(ctx context.Context, db *sql.DB, workspace, account, cut string) (InvestmentAdoptionDiagnostic, error) {
	if db == nil || strings.TrimSpace(workspace) == "" || strings.TrimSpace(account) == "" {
		return InvestmentAdoptionDiagnostic{}, ErrInvestmentValidation
	}
	date, err := civildate.ParseCivilDateInput(cut)
	if err != nil || date.String() != cut {
		return InvestmentAdoptionDiagnostic{}, ErrInvestmentValidation
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentAdoptionDiagnostic{}, err
	}
	defer tx.Rollback()
	d, err := investmentAdoptionDiagnosticTx(ctx, tx, workspace, account, cut)
	if err != nil {
		return InvestmentAdoptionDiagnostic{}, err
	}
	return d, tx.Commit()
}

func investmentAdoptionDiagnosticTx(ctx context.Context, tx *sql.Tx, workspace, account, cut string) (InvestmentAdoptionDiagnostic, error) {
	d := InvestmentAdoptionDiagnostic{WorkspaceID: workspace, AccountID: account, CutDate: cut, State: InvestmentAdoptionNoIdentifiedBlockers, PatrimonyComplete: true}
	var kind string
	var archived sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT type,archived_at,current_balance,initial_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, workspace, account).Scan(&kind, &archived, &d.CurrentBalanceCents, &d.InitialBalanceCents, &d.BalanceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrInvestmentNotFound
	}
	if err != nil {
		return d, err
	}
	if kind != models.AccountTypeInvestment {
		return d, ErrInvestmentInvalidAccount
	}
	if archived.Valid {
		d.issue("ARCHIVED_ACCOUNT", "", account, "Current account lifecycle is archived", true)
	}
	d.BalanceAtCut.Evidence = adoptionEvidence(InvestmentEvidenceUnknown, "Mutable transactions and initial balance have no complete immutable historical journal")
	d.BrokerCash.Evidence = adoptionEvidence(InvestmentEvidenceUnknown, "No persisted broker cash reconciliation or external cash attestation")
	d.Limitations = []string{
		"Current records selected by explicit economic cut; current references/integrations are labelled separately, including later events",
		"Historical balance cannot be certified from mutable/deletable transactions and initial balance",
		"Numeric principal and LEGACY_UNKNOWN terms do not establish original cost provenance",
		"No proof of absence of omitted income, external economic facts or duplicate external investment identities",
		"NO_IDENTIFIED_BLOCKERS is diagnostic only; cut approval belongs to O2B.2",
	}
	d.issue("UNKNOWN_HISTORICAL_BALANCE", "", account, d.BalanceAtCut.Evidence.Basis, false)
	d.issue("UNKNOWN_CASH_COMPONENT", "", account, d.BrokerCash.Evidence.Basis, false)
	if d.InitialBalanceCents != 0 {
		d.issue("UNVERIFIED_INITIAL_BALANCE", "", account, "Recorded opening amount has no source or component attribution", false)
	}
	if d.CurrentBalanceCents < 0 {
		d.issue("UNEXPLAINED_BALANCE_DIFFERENCE", "", account, "Negative current financial balance requires explicit review", true)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,account_id,applied_date,status,principal_cents,redeemed_date,archived_at FROM investments WHERE workspace_id=? AND account_id=? ORDER BY applied_date,id`, workspace, account)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var item InvestmentAdoptionItem
		if err = rows.Scan(&item.ID, &item.AccountID, &item.AppliedDate, &item.CurrentStatus, &item.RegisteredPrincipalCents, &item.RedeemedDate, &item.ArchivedAt); err != nil {
			rows.Close()
			return d, err
		}
		d.Investments = append(d.Investments, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return d, err
	}
	for n := range d.Investments {
		if err = adoptionReadItem(ctx, tx, &d, &d.Investments[n]); err != nil {
			return d, err
		}
	}
	if err = adoptionFunding(ctx, tx, &d); err != nil {
		return d, err
	}
	if err = adoptionIncomeAndAdjustments(ctx, tx, &d); err != nil {
		return d, err
	}
	// Same explicit note identity is a candidate overlap, never an automatic
	// assertion that two lots or equal positions are the same asset.
	notes := map[string]string{}
	for _, i := range d.Investments {
		for _, p := range i.Purchases {
			if p.NoteReference == "" {
				continue
			}
			if previous, ok := notes[p.NoteReference]; ok && previous != i.ID {
				d.issue("POSITION_OVERLAP", i.ID, p.ID, "Shared note reference with investment "+previous+"; candidate identity requires review", false)
			} else {
				notes[p.NoteReference] = i.ID
			}
		}
	}
	difference := new(big.Int).Sub(big.NewInt(d.CurrentBalanceCents), big.NewInt(d.KnownPatrimonyCents))
	if difference.IsInt64() {
		v := difference.Int64()
		d.CurrentBalanceMinusKnownPatrimonyCents = &v
		if v != 0 {
			d.issue("UNEXPLAINED_BALANCE_DIFFERENCE", "", account, "Current balance differs from known cut positions; dates/components are not reconciled and difference is not cash", false)
		}
	} else {
		return d, ErrBalanceOutOfRange
	}
	for _, i := range d.Issues {
		if i.Blocking {
			d.State = InvestmentAdoptionBlocked
			break
		}
		d.State = InvestmentAdoptionNeedsReview
	}
	return d, nil
}

func adoptionReadItem(ctx context.Context, tx *sql.Tx, d *InvestmentAdoptionDiagnostic, i *InvestmentAdoptionItem) error {
	unknown := adoptionEvidence(InvestmentEvidenceUnknown, "No typed evidence of this dimension in the current persistence model")
	i.PositionEvidence, i.InclusionEvidence, i.PrincipalEvidence = unknown, unknown, unknown
	i.FundingEvidence, i.ReceiptEvidence, i.IncomeRecognitionEvidence = unknown, unknown, unknown
	if _, err := civildate.ParseCivilDateInput(i.AppliedDate); err != nil {
		return ErrInvestmentValidation
	}
	if i.AppliedDate > d.CutDate {
		i.EventsAfterCut = append(i.EventsAfterCut, InvestmentAdoptionEvent{"INVESTMENT", i.ID, i.AppliedDate})
	} else {
		d.issue("UNKNOWN_PRINCIPAL", i.ID, i.ID, "Registered numeric cost has no separately verifiable provenance; funding is a different dimension", false)
		if adoptionUnsupportedCost(i.RegisteredPrincipalCents) {
			d.issue("UNKNOWN_OR_UNSUPPORTED_ORIGINAL_COST", i.ID, i.ID, "Missing original cost cannot be represented by the positive-cost schema", true)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+purchaseColumns+` FROM investment_purchases WHERE workspace_id=? AND investment_id=? ORDER BY purchase_date,id`, d.WorkspaceID, i.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		p, e := scanInvestmentPurchase(rows.Scan)
		if e != nil {
			rows.Close()
			return e
		}
		if _, e = civildate.ParseCivilDateInput(p.PurchaseDate); e != nil || p.PrincipalCents <= 0 {
			rows.Close()
			return ErrInvestmentValidation
		}
		if p.PurchaseDate > d.CutDate {
			i.EventsAfterCut = append(i.EventsAfterCut, InvestmentAdoptionEvent{"PURCHASE", p.ID, p.PurchaseDate})
		} else {
			i.Purchases = append(i.Purchases, p)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,workspace_id,investment_id,valuation_date,amount_cents,created_by,created_at,updated_by,updated_at FROM investment_valuations WHERE workspace_id=? AND investment_id=? ORDER BY valuation_date,id`, d.WorkspaceID, i.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		p, e := scanValuation(rows.Scan)
		if e != nil {
			rows.Close()
			return e
		}
		if _, e = civildate.ParseCivilDateInput(p.ValuationDate); e != nil || p.AmountCents < 0 {
			rows.Close()
			return ErrInvestmentValidation
		}
		if p.ValuationDate > d.CutDate {
			i.EventsAfterCut = append(i.EventsAfterCut, InvestmentAdoptionEvent{"POSITION", p.ID, p.ValuationDate})
		} else {
			i.positions = append(i.positions, p)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	history, err := readInvestmentWithdrawals(ctx, tx, d.WorkspaceID, i.ID)
	if err != nil {
		return err
	}
	for _, w := range history {
		if _, e := civildate.ParseCivilDateInput(w.WithdrawalDate); e != nil {
			return ErrInvestmentValidation
		}
		if w.WithdrawalDate > d.CutDate {
			i.EventsAfterCut = append(i.EventsAfterCut, InvestmentAdoptionEvent{"WITHDRAWAL", w.ID, w.WithdrawalDate})
			continue
		}
		entry := InvestmentAdoptionWithdrawal{Withdrawal: w, ReceiptEvidence: unknown}
		// Validate persisted JSON ownership before allowing the C1 reader to use
		// any identities from that payload in its relational validators.
		var encodedSettlement string
		e := tx.QueryRowContext(ctx, `SELECT result_json FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? ORDER BY version DESC LIMIT 1`, d.WorkspaceID, i.ID, w.ID).Scan(&encodedSettlement)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil {
			var probe InvestmentSettlement
			if e = json.Unmarshal([]byte(encodedSettlement), &probe); e != nil {
				return e
			}
			if probe.WorkspaceID != d.WorkspaceID || probe.InvestmentID != i.ID || probe.WithdrawalID != w.ID || probe.Input.WorkspaceID != d.WorkspaceID || probe.Input.InvestmentID != i.ID || probe.Input.WithdrawalID != w.ID || probe.Input.AccountID != d.AccountID {
				return ErrInvestmentConflict
			}
		}
		s, e := readInvestmentSettlementTx(ctx, tx, d.WorkspaceID, i.ID, w.ID)
		if e == nil {
			// Do not expose a JSON payload that smuggles a foreign identity.
			if s.WorkspaceID != d.WorkspaceID || s.InvestmentID != i.ID || s.WithdrawalID != w.ID || s.Input.WorkspaceID != d.WorkspaceID || s.Input.AccountID != d.AccountID {
				return ErrInvestmentConflict
			}
			entry.Settlement = &s
			if s.State == InvestmentSettlementReconciled {
				entry.ReceiptEvidence = adoptionEvidence(InvestmentEvidenceDeclared, "Explicit C1 receipt confirmation; not an externally verified bank statement", s.ID)
			}
		} else if !errors.Is(e, ErrInvestmentNotFound) && !errors.Is(e, ErrInvestmentConflict) && !errors.Is(e, ErrInvestmentFundingPending) {
			return e
		}
		if e != nil && !errors.Is(e, ErrInvestmentNotFound) {
			entry.ReceiptEvidence = adoptionEvidence(InvestmentEvidenceConflicting, "C1 evidence no longer validates", w.ID)
		}
		for _, q := range []struct {
			table string
			dest  *string
		}{{"investment_settlement_applications", &entry.ApplicationID}, {"investment_result_classifications", &entry.ClassificationID}} {
			e = tx.QueryRowContext(ctx, `SELECT id FROM `+q.table+` WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, d.WorkspaceID, i.ID, w.ID).Scan(q.dest)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if entry.ApplicationID == "" || entry.ReceiptEvidence.State != InvestmentEvidenceDeclared {
			d.issue("UNRESOLVED_WITHDRAWAL", i.ID, w.ID, "Receipt, removed patrimony and existing effects require reconciliation", true)
		}
		if entry.ClassificationID != "" {
			if e = tx.QueryRowContext(ctx, `SELECT state FROM investment_result_classifications WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? AND id=?`, d.WorkspaceID, i.ID, w.ID, entry.ClassificationID).Scan(&entry.ClassificationState); e != nil {
				return e
			}
			if entry.ClassificationState == InvestmentClassificationCompleted {
				i.IncomeRecognitionEvidence = adoptionEvidence(InvestmentEvidenceProven, "C2B completed record exists; completeness of historical income remains unproven", entry.ClassificationID)
			} else {
				i.IncomeRecognitionEvidence = unknown
				d.issue("INCOMPLETE_COVERAGE", i.ID, entry.ClassificationID, "C2B result unavailable; no recognized result may be inferred", false)
			}
		}
		i.Withdrawals = append(i.Withdrawals, entry)
	}
	if len(i.Withdrawals) > 0 {
		i.ReceiptEvidence = adoptionEvidence(InvestmentEvidenceDeclared, "Explicit confirmations only for individually reconciled withdrawals")
		for _, w := range i.Withdrawals {
			if w.ReceiptEvidence.State != InvestmentEvidenceDeclared {
				i.ReceiptEvidence = unknown
				break
			}
			i.ReceiptEvidence.IDs = append(i.ReceiptEvidence.IDs, w.Settlement.ID)
		}
	}
	if len(i.positions) > 0 {
		p := i.positions[len(i.positions)-1]
		i.Position = &p
		i.PositionEligible = true
		i.PositionEvidence = adoptionEvidence(InvestmentEvidenceDeclared, "Manual observed position with author and economic date", p.ID)
		if !adoptionPositionKnownAtCut(p, d.CutDate) {
			i.PositionEligible = false
			i.PositionEvidence = adoptionEvidence(InvestmentEvidenceUnknown, "Position corrected after cut; previous value is not recoverable", p.ID)
		}
		if i.RedeemedDate != nil && *i.RedeemedDate <= d.CutDate {
			i.PositionEligible = false
			d.issue("UNRESOLVED_WITHDRAWAL", i.ID, i.ID, "Administrative redemption is not proof of reconciled receipt or remaining position", true)
		}
		if i.AppliedDate > p.ValuationDate {
			return ErrInvestmentValidation
		}
		for _, purchase := range i.Purchases {
			if purchase.PurchaseDate >= p.ValuationDate {
				i.PositionEligible = false
			}
		}
		for _, w := range i.Withdrawals {
			if w.Withdrawal.WithdrawalDate >= p.ValuationDate {
				i.PositionEligible = false
			}
		}
		if !i.PositionEligible {
			d.issue("STALE_EVIDENCE", i.ID, p.ID, "Position precedes operations or has ambiguous same-day ordering", true)
		}
		d.PositionCount++
	} else if i.AppliedDate <= d.CutDate {
		d.issue("MISSING_POSITION", i.ID, i.ID, "No recorded position through the explicit cut; principal is not a substitute", true)
	}
	r, err := readInvestmentReferenceRevisionTx(ctx, tx, d.WorkspaceID, i.ID, "")
	if err == nil {
		i.Reference = &r
		if r.AccountID != d.AccountID || !adoptionCandidateOwned(r.Candidate, d.WorkspaceID, d.AccountID, i.ID) {
			return ErrInvestmentConflict
		}
		if r.Candidate.Snapshot.Position.ValuationDate > d.CutDate {
			i.EventsAfterCut = append(i.EventsAfterCut, InvestmentAdoptionEvent{"REFERENCE", r.ID, r.Candidate.Snapshot.Position.ValuationDate})
		}
		if r.State == models.InvestmentReferenceValid && r.Confirmed && r.Candidate.Snapshot.Position.ValuationDate <= d.CutDate {
			i.InclusionEvidence = adoptionEvidence(InvestmentEvidenceDeclared, "A confirms represented amount explicitly; no documentary cost or broker cash proof", r.ID)
		} else {
			d.issue("STALE_EVIDENCE", i.ID, r.ID, "Current reference is pending, invalid or after the cut", true)
		}
	} else if !errors.Is(err, ErrInvestmentNotFound) {
		return err
	}
	var chainJSON, rootID string
	err = tx.QueryRowContext(ctx, `SELECT id,version,reference_revision_id,chain_json FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? AND account_id=? ORDER BY version DESC LIMIT 1`, d.WorkspaceID, i.ID, d.AccountID).Scan(&i.FinancialRevisionID, &i.FinancialVersion, &rootID, &chainJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	coveredPurchases, coveredWithdrawals := map[string]bool{}, map[string]bool{}
	if i.Reference != nil && i.Reference.State == models.InvestmentReferenceValid && i.Reference.Candidate.Snapshot.Position.ValuationDate <= d.CutDate {
		for _, p := range i.Reference.Candidate.Snapshot.Purchases {
			coveredPurchases[p.ID] = true
		}
		for _, w := range i.Reference.Candidate.Snapshot.Withdrawals {
			coveredWithdrawals[w.ID] = true
		}
	}
	if chainJSON != "" {
		d.issue("EXISTING_FINANCIAL_INTEGRATION", i.ID, i.FinancialRevisionID, "Existing B chain; never propose a second activation of its root", true)
		var chain []financialPosition
		if err = json.Unmarshal([]byte(chainJSON), &chain); err != nil {
			return err
		}
		if len(chain) == 0 {
			return ErrInvestmentConflict
		}
		for _, p := range chain {
			s := p.Candidate.Snapshot
			if !adoptionCandidateOwned(p.Candidate, d.WorkspaceID, d.AccountID, i.ID) {
				return ErrInvestmentConflict
			}
			current, e := investmentReferenceCandidateTx(ctx, tx, d.WorkspaceID, i.ID, s.Position.ID)
			if e != nil && !errors.Is(e, ErrInvestmentNotFound) && !errors.Is(e, ErrInvestmentInvalidStatus) {
				return e
			}
			if e != nil || current.CoverageToken != p.Candidate.CoverageToken || i.Reference == nil || rootID != i.Reference.ID {
				d.issue("STALE_EVIDENCE", i.ID, s.Position.ID, "B coverage no longer validates against current records", true)
			}
			i.IntegratedPositions = append(i.IntegratedPositions, s.Position)
			if s.Position.ValuationDate > d.CutDate {
				i.EventsAfterCut = append(i.EventsAfterCut, InvestmentAdoptionEvent{"INTEGRATION", s.Position.ID, s.Position.ValuationDate})
				continue
			}
			for _, p := range s.Purchases {
				coveredPurchases[p.ID] = true
			}
			for _, w := range s.Withdrawals {
				coveredWithdrawals[w.ID] = true
			}
		}
	}
	for _, p := range i.Purchases {
		if !coveredPurchases[p.ID] {
			i.UncoveredPurchaseIDs = append(i.UncoveredPurchaseIDs, p.ID)
		}
	}
	for _, w := range i.Withdrawals {
		if !coveredWithdrawals[w.Withdrawal.ID] {
			i.UncoveredWithdrawalIDs = append(i.UncoveredWithdrawalIDs, w.Withdrawal.ID)
		}
	}
	if i.AppliedDate <= d.CutDate {
		if i.Position == nil || !i.PositionEligible {
			d.PatrimonyComplete = false
		} else {
			n := new(big.Int).Add(big.NewInt(d.KnownPatrimonyCents), big.NewInt(i.Position.AmountCents))
			if !n.IsInt64() {
				return ErrBalanceOutOfRange
			}
			d.KnownPatrimonyCents = n.Int64()
			d.EligiblePositionCount++
		}
		if len(i.UncoveredPurchaseIDs)+len(i.UncoveredWithdrawalIDs) > 0 {
			d.issue("INCOMPLETE_COVERAGE", i.ID, i.ID, "Operations outside recorded reference/integrated coverage", false)
		}
	}
	return nil
}

func adoptionFunding(ctx context.Context, tx *sql.Tx, d *InvestmentAdoptionDiagnostic) error {
	// Read all source identities first, closing rows before Phase B validators.
	rows, err := tx.QueryContext(ctx, `SELECT t.id FROM financial_transactions t WHERE t.workspace_id=? AND t.destination_account_id=? AND t.type='TRANSFER' ORDER BY t.id`, d.WorkspaceID, d.AccountID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	sources := map[string]transferFundingEvidence{}
	snapshots := map[string]string{}
	for _, id := range ids {
		e, s, eErr := readTransferFunding(ctx, tx, d.WorkspaceID, d.AccountID, id)
		if eErr != nil && !errors.Is(eErr, ErrInvestmentFundingPending) {
			return eErr
		}
		// readTransferFunding's tenant joins fail closed on foreign account FKs.
		if e.ID == "" {
			d.issue("INVALID_TRANSFER_OWNERSHIP", "", id, "Source/destination ownership cannot be verified", true)
			continue
		}
		t := InvestmentAdoptionTransfer{ID: e.ID, SourceAccountID: e.Source, DestinationAccountID: e.Destination, EconomicDate: e.EconomicDate, Status: e.Status, AmountCents: e.Amount, Evidence: adoptionEvidence(InvestmentEvidenceUnknown, "Transfer does not meet B funding criteria", id)}
		if eErr == nil {
			sources[id] = e
			snapshots[id] = s
			t.Evidence = adoptionEvidence(InvestmentEvidenceProven, "Paid transfer satisfies Phase B identity, ownership, amount and civil/instant date criteria; allocation remains separate", id)
		}
		if e.EconomicDate > d.CutDate {
			d.EventsAfterCut = append(d.EventsAfterCut, InvestmentAdoptionEvent{"TRANSFER", e.ID, e.EconomicDate})
		}
		d.Transfers = append(d.Transfers, t)
	}
	rows, err = tx.QueryContext(ctx, `SELECT f.investment_id,f.purchase_id,f.transaction_id,f.amount_cents,f.snapshot_json FROM investment_purchase_funding f JOIN investments i ON i.workspace_id=? AND i.workspace_id=f.workspace_id AND i.id=f.investment_id WHERE f.workspace_id=? AND i.account_id=? ORDER BY f.investment_id,f.purchase_id,f.transaction_id`, d.WorkspaceID, d.WorkspaceID, d.AccountID)
	if err != nil {
		return err
	}
	var claims []accountFundingClaim
	for rows.Next() {
		var c accountFundingClaim
		if err = rows.Scan(&c.InvestmentID, &c.PurchaseID, &c.TransferID, &c.AmountCents, &c.Snapshot); err != nil {
			rows.Close()
			return err
		}
		claims = append(claims, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	valid := true
	for _, c := range claims {
		if snapshots[c.TransferID] == "" || snapshots[c.TransferID] != c.Snapshot {
			valid = false
		}
	}
	if valid {
		err = validateAccountFundingCapacity(ctx, tx, d.WorkspaceID, d.AccountID, claims, sources)
		if errors.Is(err, ErrInvestmentConflict) || errors.Is(err, ErrInvestmentFundingPending) {
			valid = false
		} else if err != nil {
			return err
		}
	}
	if !valid {
		d.issue("UNVERIFIED_FUNDING", "", "", "Current shared capacity or source snapshot conflicts; no automatic allocation", true)
	}
	for n := range d.Transfers {
		t := &d.Transfers[n]
		sum := new(big.Int)
		for _, c := range claims {
			if c.TransferID == t.ID {
				sum.Add(sum, big.NewInt(c.AmountCents))
			}
		}
		if !sum.IsInt64() {
			return ErrBalanceOutOfRange
		}
		t.AllocatedCents = sum.Int64()
		left := new(big.Int).Sub(big.NewInt(t.AmountCents), sum)
		if !left.IsInt64() {
			return ErrBalanceOutOfRange
		}
		v := left.Int64()
		t.UnallocatedCents = &v
		if v < 0 {
			t.Evidence = adoptionEvidence(InvestmentEvidenceConflicting, "Source overallocated", t.ID)
		}
	}
	for n := range d.Investments {
		i := &d.Investments[n]
		all := len(i.Purchases) > 0
		for _, p := range i.Purchases {
			sum := new(big.Int)
			for _, c := range claims {
				if c.InvestmentID != i.ID || c.PurchaseID != p.ID {
					continue
				}
				state := InvestmentEvidenceConflicting
				if valid && sources[c.TransferID].EconomicDate <= p.PurchaseDate {
					state = InvestmentEvidenceProven
				}
				i.Funding = append(i.Funding, InvestmentAdoptionFunding{p.ID, c.TransferID, c.AmountCents, adoptionEvidence(state, "Explicit B allocation, revalidated with shared CURRENT account capacity", c.TransferID)})
				sum.Add(sum, big.NewInt(c.AmountCents))
			}
			if !valid || sum.Cmp(big.NewInt(p.PrincipalCents)) != 0 {
				all = false
				i.UnfundedPurchaseIDs = append(i.UnfundedPurchaseIDs, p.ID)
			}
		}
		if all {
			i.FundingEvidence = adoptionEvidence(InvestmentEvidenceProven, "All purchases through cut have explicit valid allocations; external original cost provenance remains separate")
		} else if len(i.Purchases) > 0 {
			d.issue("UNVERIFIED_FUNDING", i.ID, i.ID, "No complete explicit transfer allocation; positive initial balance/income are not funding evidence", false)
		}
	}
	return nil
}

func adoptionIncomeAndAdjustments(ctx context.Context, tx *sql.Tx, d *InvestmentAdoptionDiagnostic) error {
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.account_id,COALESCE(c.id,''),COALESCE(c.name,''),COALESCE(t.description,''),COALESCE(t.notes,''),COALESCE(t.attachment_path,''),COALESCE(NULLIF(t.date_civil,''),strftime('%Y-%m-%d',t.date,'unixepoch')),CASE WHEN t.paid_at IS NOT NULL THEN COALESCE(NULLIF(t.paid_date_civil,''),strftime('%Y-%m-%d',t.paid_at,'unixepoch')) ELSE '' END,t.status,t.amount FROM transactions t LEFT JOIN categories c ON c.workspace_id=? AND c.id=t.category_id WHERE t.workspace_id=? AND t.account_id=? AND t.type='INCOME' AND t.effect_mode='STANDARD' ORDER BY t.date,t.id`, d.WorkspaceID, d.WorkspaceID, d.AccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v InvestmentAdoptionIncome
		if err = rows.Scan(&v.ID, &v.AccountID, &v.CategoryID, &v.Category, &v.Description, &v.Notes, &v.AttachmentReference, &v.EconomicDate, &v.PaidDate, &v.Status, &v.AmountCents); err != nil {
			rows.Close()
			return err
		}
		if _, e := civildate.ParseCivilDateInput(v.EconomicDate); e != nil {
			rows.Close()
			return ErrInvestmentValidation
		}
		if v.EconomicDate <= d.CutDate {
			d.IncomeCandidates = append(d.IncomeCandidates, v)
		} else {
			d.EventsAfterCut = append(d.EventsAfterCut, InvestmentAdoptionEvent{"INCOME", v.ID, v.EconomicDate})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for n := range d.IncomeCandidates {
		income := &d.IncomeCandidates[n]
		income.Evidence = adoptionEvidence(InvestmentEvidenceInferred, "STANDARD income on investment account is only a candidate; category, text, documents and date do not establish attribution", income.ID)
		for _, i := range d.Investments {
			for p := 1; p < len(i.positions); p++ {
				a, b := i.positions[p-1], i.positions[p]
				if income.EconomicDate < a.ValuationDate || income.EconomicDate > b.ValuationDate {
					continue
				}
				interval := InvestmentAdoptionIncomeOverlap{InvestmentID: i.ID, FromPositionID: a.ID, ToPositionID: b.ID, FromDate: a.ValuationDate, ToDate: b.ValuationDate, Reason: "POTENTIAL_INTERVAL_OVERLAP"}
				before, e1 := investmentReferenceCandidateTx(ctx, tx, d.WorkspaceID, i.ID, a.ID)
				after, e2 := investmentReferenceCandidateTx(ctx, tx, d.WorkspaceID, i.ID, b.ID)
				for _, e := range []error{e1, e2} {
					if e != nil && !errors.Is(e, ErrInvestmentNotFound) && !errors.Is(e, ErrInvestmentInvalidStatus) && !errors.Is(e, ErrInvestmentValidation) && !errors.Is(e, ErrBalanceOutOfRange) {
						return e
					}
				}
				if e1 == nil && e2 == nil && !before.HasSameDayOperations && !after.HasSameDayOperations && adoptionPositionKnownAtCut(a, d.CutDate) && adoptionPositionKnownAtCut(b, d.CutDate) {
					delta, e := financialIntervalDelta(before.Snapshot, after.Snapshot)
					if e == nil {
						interval.VariationCents = &delta
						if delta == income.AmountCents {
							interval.Reason = "POTENTIAL_DUPLICATE_VALUATION"
						}
					}
				}
				for k := 1; k < len(i.IntegratedPositions); k++ {
					if i.IntegratedPositions[k-1].ID == a.ID && i.IntegratedPositions[k].ID == b.ID {
						interval.Integrated = true
					}
				}
				income.Overlaps = append(income.Overlaps, interval)
			}
		}
		d.issue("HISTORICAL_INCOME_OVERLAP", "", income.ID, "Historical account income requires review; no definitive attribution or absence-of-omission claim", false)
	}
	rows, err = tx.QueryContext(ctx, `SELECT h.id,h.reason,h.created_by,strftime('%Y-%m-%d',h.created_at,'unixepoch'),COALESCE(f.economic_date,s.economic_date,strftime('%Y-%m-%d',h.created_at,'unixepoch')),h.delta_cents FROM account_balance_adjustments h LEFT JOIN investment_financial_adjustments f ON f.workspace_id=? AND f.account_id=h.account_id AND f.adjustment_id=h.id LEFT JOIN investment_settlement_applications s ON s.workspace_id=? AND s.account_id=h.account_id AND s.adjustment_id=h.id WHERE h.workspace_id=? AND h.account_id=? ORDER BY h.created_at,h.id`, d.WorkspaceID, d.WorkspaceID, d.WorkspaceID, d.AccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a InvestmentAdoptionAdjustment
		if err = rows.Scan(&a.ID, &a.Reason, &a.CreatedBy, &a.RecordedDate, &a.EconomicDate, &a.DeltaCents); err != nil {
			rows.Close()
			return err
		}
		d.Adjustments = append(d.Adjustments, a)
		if a.EconomicDate > d.CutDate {
			d.EventsAfterCut = append(d.EventsAfterCut, InvestmentAdoptionEvent{"ADJUSTMENT", a.ID, a.EconomicDate})
		} else {
			d.issue("FINANCIAL_ADJUSTMENT_REQUIRES_REVIEW", "", a.ID, "Recorded adjustment requires component attribution; no automatic compensation", false)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Stable order also covers IDs generated by independent writers.
	for n := range d.Investments {
		sort.Slice(d.Investments[n].EventsAfterCut, func(a, b int) bool {
			x, y := d.Investments[n].EventsAfterCut[a], d.Investments[n].EventsAfterCut[b]
			return x.EconomicDate+x.Kind+x.ID < y.EconomicDate+y.Kind+y.ID
		})
	}
	return nil
}

// The positive numeric cost schema cannot encode genuinely missing original cost.
func adoptionUnsupportedCost(registered *int64) bool { return registered == nil || *registered <= 0 }

func adoptionPositionKnownAtCut(p models.InvestmentValuation, cut string) bool {
	return p.UpdatedAt == nil || time.Unix(*p.UpdatedAt, 0).UTC().Format("2006-01-02") <= cut
}

func adoptionCandidateOwned(c models.InvestmentReferenceCandidate, workspace, account, investment string) bool {
	s := c.Snapshot
	if s.WorkspaceID != workspace || s.AccountID != account || s.InvestmentID != investment || s.Position.WorkspaceID != workspace || s.Position.InvestmentID != investment {
		return false
	}
	for _, p := range s.Purchases {
		if p.WorkspaceID != workspace || p.InvestmentID != investment {
			return false
		}
	}
	for _, w := range s.Withdrawals {
		if w.WorkspaceID != workspace || w.InvestmentID != investment {
			return false
		}
		for _, a := range w.Allocations {
			if a.WorkspaceID != workspace || a.InvestmentID != investment || a.WithdrawalID != w.ID {
				return false
			}
		}
	}
	return true
}
