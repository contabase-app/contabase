package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

const InvestmentSettlementFinancialApplied = "FINANCIAL_APPLIED_CLASSIFICATION_PENDING"

var ErrInvestmentLegacyIncome = errors.New("recebimento INCOME requer reconciliação específica antes da C2A")

// An explicit attribution of one entire, already posted effect to this exact
// settlement. No value/date matching, allocation of ambiguous effects or income
// neutralization is performed. Transfers and B adjustments are never eligible.
type InvestmentSettlementPriorEffect struct {
	AdjustmentID, TransactionID, Evidence string
	DeltaCents                            int64
}

type ApplyInvestmentSettlementInput struct {
	WorkspaceID, InvestmentID, WithdrawalID, SettlementRevisionID string
	ReferenceRevisionID, FinancialRevisionID                      string
	ExpectedSettlementVersion, ExpectedFinancialVersion           int64
	ExpectedBalanceVersion, ExpectedDestinationVersion            int64
	CreatedBy, RequestID, Reason                                  string
	// The trusted caller explicitly attests receipt reconciliation and completeness
	// of the listed equivalent effects. This is audit evidence, not inferred cash.
	ReconciliationEvidence string
	PriorEffects           []InvestmentSettlementPriorEffect
	// Partial withdrawals require an observed end-of-day remaining position.
	RemainingPositionID, RemainingCoverageToken string
	DayClosed                                   bool
}

type InvestmentSettlementApplication struct {
	ID, AdjustmentID, State                                        string
	SettlementRevisionID, ReferenceRevisionID, FinancialRevisionID string
	NetCents, PatrimonyCents, PriorEffectCents, ResidualCents      int64
	BalanceCents, BalanceVersion, CreatedAt                        int64
}

type settlementEffectProof struct {
	InvestmentSettlementPriorEffect
	Snapshot string
}

type settlementApplicationProof struct {
	Settlement InvestmentSettlement
	Chain      []financialPosition
	Remaining  *models.InvestmentReferenceCandidate
	Effects    []settlementEffectProof
	Funding    []string
}

// ApplyInvestmentSettlement is internal and opt-in. The trusted caller must
// authorize CreatedBy and provide the runtime Immediate writer. Nothing calls
// it from HTTP, jobs, C1 recording or startup. After an uncertain commit, replay
// the ORIGINAL key and payload to recover its persisted result.
func ApplyInvestmentSettlement(ctx context.Context, db *sql.DB, in ApplyInvestmentSettlementInput) (InvestmentSettlementApplication, error) {
	var zero InvestmentSettlementApplication
	key, err := uuid.Parse(in.RequestID)
	in.Reason = strings.TrimSpace(in.Reason)
	in.ReconciliationEvidence = strings.TrimSpace(in.ReconciliationEvidence)
	if err != nil || in.WorkspaceID == "" || in.InvestmentID == "" || in.WithdrawalID == "" || in.SettlementRevisionID == "" || in.ReferenceRevisionID == "" || in.FinancialRevisionID == "" || in.CreatedBy == "" || in.ExpectedSettlementVersion <= 0 || in.ExpectedFinancialVersion <= 0 || in.ExpectedBalanceVersion < 0 || in.ExpectedDestinationVersion < 0 || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 500 || in.ReconciliationEvidence == "" || utf8.RuneCountInString(in.ReconciliationEvidence) > 2000 {
		return zero, ErrInvestmentValidation
	}
	in.RequestID = key.String()
	in.PriorEffects = append([]InvestmentSettlementPriorEffect(nil), in.PriorEffects...)
	sort.Slice(in.PriorEffects, func(i, j int) bool {
		return in.PriorEffects[i].AdjustmentID+":"+in.PriorEffects[i].TransactionID < in.PriorEffects[j].AdjustmentID+":"+in.PriorEffects[j].TransactionID
	})
	for i := range in.PriorEffects {
		e := &in.PriorEffects[i]
		e.Evidence = strings.TrimSpace(e.Evidence)
		if (e.AdjustmentID == "") == (e.TransactionID == "") || e.DeltaCents == 0 || e.Evidence == "" || utf8.RuneCountInString(e.Evidence) > 2000 || (i > 0 && e.AdjustmentID == in.PriorEffects[i-1].AdjustmentID && e.TransactionID == in.PriorEffects[i-1].TransactionID) {
			return zero, ErrInvestmentValidation
		}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return zero, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	// Defensive writer reservation precedes every read even with a deferred pool.
	if _, err = tx.ExecContext(ctx, `UPDATE investment_settlement_applications SET id=id WHERE 0`); err != nil {
		return zero, err
	}
	var savedPayload, savedResult string
	err = tx.QueryRowContext(ctx, `SELECT payload_json,result_json FROM investment_settlement_applications WHERE workspace_id=? AND request_id=?`, in.WorkspaceID, in.RequestID).Scan(&savedPayload, &savedResult)
	if err == nil {
		if savedPayload != string(payload) {
			return zero, ErrInvestmentConflict
		}
		if err = json.Unmarshal([]byte(savedResult), &zero); err != nil {
			return zero, err
		}
		if err = tx.Commit(); err != nil {
			return InvestmentSettlementApplication{}, &InvestmentCommitError{err: err}
		}
		return zero, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var proof settlementApplicationProof
	s := &proof.Settlement
	var withdrawalJSON string
	err = tx.QueryRowContext(ctx, `SELECT result_json,withdrawal_json FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID).Scan(&savedResult, &withdrawalJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, ErrInvestmentNotFound
	}
	if err != nil {
		return zero, err
	}
	if err = json.Unmarshal([]byte(savedResult), s); err != nil {
		return zero, err
	}
	if s.ID != in.SettlementRevisionID || s.Version != in.ExpectedSettlementVersion || s.ReferenceRevisionID != in.ReferenceRevisionID || s.FinancialRevisionID != in.FinancialRevisionID {
		return zero, ErrInvestmentConflict
	}
	if s.State != InvestmentSettlementReconciled || s.Input.PatrimonyCents == nil {
		return zero, ErrInvestmentSettlementPending
	}
	var applied int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_settlement_applications WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID).Scan(&applied); err != nil {
		return zero, err
	}
	if applied != 0 {
		return zero, ErrInvestmentConflict
	}
	w, err := settlementWithdrawal(ctx, tx, in.WorkspaceID, in.InvestmentID, in.WithdrawalID)
	if err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(w)
	if err != nil {
		return zero, err
	}
	if string(encoded) != withdrawalJSON || w.ReceivedCents != s.Input.NetCents {
		return zero, ErrInvestmentConflict
	}
	claims, err := settlementClaims(ctx, tx, s.Input)
	if err != nil {
		return zero, err
	}
	receiptTotal := new(big.Int)
	for _, c := range claims {
		if !c.Existing {
			return zero, ErrInvestmentConflict
		}
		var e settlementReceiptEvidence
		if err = json.Unmarshal([]byte(c.Snapshot), &e); err != nil {
			return zero, err
		}
		if e.Type == "INCOME" {
			return zero, ErrInvestmentLegacyIncome
		}
		receiptTotal.Add(receiptTotal, big.NewInt(c.AmountCents))
	}
	if s.Input.DestinationAccountID != s.Input.AccountID && receiptTotal.Cmp(big.NewInt(s.Input.NetCents)) != 0 {
		return zero, ErrInvestmentSettlementPending
	}
	var balance, version, destinationVersion int64
	err = tx.QueryRowContext(ctx, `SELECT a.current_balance,a.balance_version,d.balance_version FROM accounts a JOIN accounts d ON d.workspace_id=a.workspace_id AND d.id=? WHERE a.workspace_id=? AND a.id=? AND a.type='INVESTMENT' AND a.archived_at IS NULL AND d.type!='CREDIT_CARD' AND d.archived_at IS NULL`, s.Input.DestinationAccountID, in.WorkspaceID, s.Input.AccountID).Scan(&balance, &version, &destinationVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, ErrInvestmentNotFound
	}
	if err != nil {
		return zero, err
	}
	if version != in.ExpectedBalanceVersion || destinationVersion != in.ExpectedDestinationVersion {
		return zero, ErrBalanceAdjustmentConflict
	}
	if err = validateSettlementPositionChain(ctx, tx, in, &proof, w); err != nil {
		return zero, err
	}
	effects, prior, err := settlementPriorEffects(ctx, tx, in, s.Input.AccountID, s.Input.EconomicDate)
	if err != nil {
		return zero, err
	}
	proof.Effects = effects
	residual := new(big.Int).Sub(big.NewInt(s.Input.NetCents), big.NewInt(*s.Input.PatrimonyCents))
	residual.Sub(residual, big.NewInt(prior))
	next := new(big.Int).Add(big.NewInt(balance), residual)
	if !residual.IsInt64() || !next.IsInt64() || (residual.Sign() != 0 && version == math.MaxInt64) {
		return zero, ErrBalanceOutOfRange
	}
	r := InvestmentSettlementApplication{ID: uuid.NewString(), State: InvestmentSettlementFinancialApplied, SettlementRevisionID: s.ID, ReferenceRevisionID: in.ReferenceRevisionID, FinancialRevisionID: in.FinancialRevisionID, NetCents: s.Input.NetCents, PatrimonyCents: *s.Input.PatrimonyCents, PriorEffectCents: prior, ResidualCents: residual.Int64(), BalanceCents: next.Int64(), BalanceVersion: version, CreatedAt: time.Now().Unix()}
	if r.ResidualCents != 0 {
		r.AdjustmentID = uuid.NewString()
		if err = adjustAccountBalanceTx(ctx, tx, AccountBalanceAdjustment{ID: r.AdjustmentID, WorkspaceID: in.WorkspaceID, AccountID: s.Input.AccountID, UserID: in.CreatedBy, Reason: in.Reason, ExpectedVersion: version, TargetBalance: r.BalanceCents, CreatedAt: r.CreatedAt}); err != nil {
			return zero, err
		}
		r.BalanceVersion++
	}
	result, err := json.Marshal(r)
	if err != nil {
		return zero, err
	}
	proofJSON, err := json.Marshal(proof)
	if err != nil {
		return zero, err
	}
	date, err := time.Parse("2006-01-02", s.Input.EconomicDate)
	if err != nil {
		return zero, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_settlement_applications
 (workspace_id,investment_id,withdrawal_id,id,settlement_revision_id,account_id,destination_account_id,reference_revision_id,financial_revision_id,settlement_version,financial_version,net_cents,patrimony_cents,prior_effect_cents,residual_cents,adjustment_id,economic_date,economic_at,coverage_date,created_at,created_by,reason,request_id,expected_balance_version,expected_destination_version,state,payload_json,result_json,proof_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID, r.ID, s.ID, s.Input.AccountID, s.Input.DestinationAccountID, in.ReferenceRevisionID, in.FinancialRevisionID, s.Version, in.ExpectedFinancialVersion, r.NetCents, r.PatrimonyCents, r.PriorEffectCents, r.ResidualCents, settlementNullable(r.AdjustmentID), s.Input.EconomicDate, date.Add(24*time.Hour-time.Second).Unix(), w.WithdrawalDate, r.CreatedAt, in.CreatedBy, in.Reason, in.RequestID, version, destinationVersion, r.State, string(payload), string(result), string(proofJSON))
	if err != nil {
		return zero, err
	}
	for _, e := range effects {
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_settlement_prior_effects(workspace_id,application_id,account_id,adjustment_id,transaction_id,delta_cents,evidence,snapshot_json) VALUES(?,?,?,?,?,?,?,?)`, in.WorkspaceID, r.ID, s.Input.AccountID, settlementNullable(e.AdjustmentID), settlementNullable(e.TransactionID), e.DeltaCents, e.Evidence, e.Snapshot)
		if err != nil {
			return zero, err
		}
	}
	if err = tx.Commit(); err != nil {
		return zero, &InvestmentCommitError{err: err}
	}
	return r, nil
}

func validateSettlementPositionChain(ctx context.Context, tx *sql.Tx, in ApplyInvestmentSettlementInput, proof *settlementApplicationProof, w models.InvestmentWithdrawal) error {
	s := proof.Settlement
	root, err := readInvestmentReferenceRevisionTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.ReferenceRevisionID)
	if err != nil {
		return err
	}
	if root.State != models.InvestmentReferenceValid || root.AccountID != s.Input.AccountID || root.RepresentedCents == nil || *root.RepresentedCents != root.Candidate.Snapshot.Position.AmountCents {
		return ErrInvestmentConflict
	}
	var id, ref, chainJSON string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT id,reference_revision_id,version,chain_json FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID).Scan(&id, &ref, &version, &chainJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentSettlementPending
	}
	if err != nil {
		return err
	}
	if id != in.FinancialRevisionID || ref != in.ReferenceRevisionID || version != in.ExpectedFinancialVersion {
		return ErrInvestmentConflict
	}
	if err = json.Unmarshal([]byte(chainJSON), &proof.Chain); err != nil {
		return err
	}
	chain := proof.Chain
	if len(chain) == 0 || chain[0].Candidate.CoverageToken != root.Candidate.CoverageToken {
		return ErrInvestmentConflict
	}
	for i, p := range chain {
		c, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, p.Candidate.Snapshot.Position.ID)
		if err != nil {
			return err
		}
		if c.CoverageToken != p.Candidate.CoverageToken || (c.HasSameDayOperations && !p.DayClosed) {
			return ErrInvestmentConflict
		}
		if i > 0 {
			delta, err := financialIntervalDelta(chain[i-1].Candidate.Snapshot, c.Snapshot)
			if err != nil {
				return err
			}
			if delta != p.Delta {
				return ErrInvestmentConflict
			}
		}
		if p.AdjustmentID != "" {
			var delta int64
			err = tx.QueryRowContext(ctx, `SELECT h.delta_cents FROM investment_financial_adjustments f JOIN account_balance_adjustments h ON h.workspace_id=f.workspace_id AND h.account_id=f.account_id AND h.id=f.adjustment_id WHERE f.workspace_id=? AND f.investment_id=? AND f.account_id=? AND f.adjustment_id=? AND NOT EXISTS(SELECT 1 FROM investment_financial_adjustments x WHERE x.workspace_id=f.workspace_id AND x.reverses_id=f.adjustment_id)`, in.WorkspaceID, in.InvestmentID, s.Input.AccountID, p.AdjustmentID).Scan(&delta)
			if err != nil {
				return err
			}
			if delta != p.Delta {
				return ErrInvestmentConflict
			}
		} else if p.Delta != 0 {
			return ErrInvestmentConflict
		}
	}
	last := chain[len(chain)-1].Candidate.Snapshot
	if w.WithdrawalDate <= last.Position.ValuationDate {
		return ErrInvestmentConflict
	}
	// An unintegrated observation or purchase cannot be silently swallowed into
	// the old basis. Subsequent B integration still requires its own later phase.
	var latest string
	err = tx.QueryRowContext(ctx, `SELECT id FROM investment_valuations WHERE workspace_id=? AND investment_id=? AND valuation_date<? ORDER BY valuation_date DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID, w.WithdrawalDate).Scan(&latest)
	if err != nil {
		return err
	}
	// Prior C2A partials can supply the latest sealed remaining observation.
	remaining := big.NewInt(last.Position.AmountCents)
	rows, err := tx.QueryContext(ctx, `SELECT withdrawal_id,patrimony_cents,coverage_date,financial_revision_id,proof_json FROM investment_settlement_applications WHERE workspace_id=? AND investment_id=? ORDER BY coverage_date,id`, in.WorkspaceID, in.InvestmentID)
	if err != nil {
		return err
	}
	priorWithdrawals := map[string]bool{}
	for rows.Next() {
		var wid, day, financial, pj string
		var amount int64
		if err = rows.Scan(&wid, &amount, &day, &financial, &pj); err != nil {
			rows.Close()
			return err
		}
		if day >= w.WithdrawalDate || financial != id {
			rows.Close()
			return ErrInvestmentConflict
		}
		var old settlementApplicationProof
		if err = json.Unmarshal([]byte(pj), &old); err != nil {
			rows.Close()
			return err
		}
		if old.Remaining == nil {
			rows.Close()
			return ErrInvestmentConflict
		}
		if latest == old.Remaining.Snapshot.Position.ID {
			latest = last.Position.ID
		}
		remaining.Sub(remaining, big.NewInt(amount))
		priorWithdrawals[wid] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if latest != last.Position.ID || remaining.Sign() < 0 || !remaining.IsInt64() || *s.Input.PatrimonyCents > remaining.Int64() {
		return ErrInvestmentConflict
	}
	history, err := readInvestmentWithdrawals(ctx, tx, in.WorkspaceID, in.InvestmentID)
	if err != nil {
		return err
	}
	base := map[string]bool{}
	for _, old := range last.Withdrawals {
		base[old.ID] = true
	}
	for _, other := range history {
		if other.WithdrawalDate > w.WithdrawalDate {
			continue
		}
		if !base[other.ID] && !priorWithdrawals[other.ID] && other.ID != w.ID {
			return ErrInvestmentSettlementPending
		}
	}
	var purchases int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_purchases WHERE workspace_id=? AND investment_id=? AND purchase_date>? AND purchase_date<=?`, in.WorkspaceID, in.InvestmentID, last.Position.ValuationDate, w.WithdrawalDate).Scan(&purchases); err != nil {
		return err
	}
	if purchases != 0 {
		return ErrInvestmentConflict
	}
	remaining.Sub(remaining, big.NewInt(*s.Input.PatrimonyCents))
	if w.Mode == InvestmentWithdrawalTotal && remaining.Sign() != 0 {
		return ErrInvestmentConflict
	}
	if w.Mode == InvestmentWithdrawalPartial || in.RemainingPositionID != "" {
		if in.RemainingPositionID == "" || len(in.RemainingCoverageToken) != 64 || !in.DayClosed {
			return ErrInvestmentSettlementPending
		}
		c, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.RemainingPositionID)
		if err != nil {
			return err
		}
		if c.CoverageToken != in.RemainingCoverageToken || c.Snapshot.Position.ValuationDate != w.WithdrawalDate || c.Snapshot.Position.AmountCents != remaining.Int64() {
			return ErrInvestmentConflict
		}
		proof.Remaining = &c
	} else {
		if in.RemainingCoverageToken != "" {
			return ErrInvestmentValidation
		}
		// A same-day contrary observation must not be ignored for total withdrawal.
		var contradictions int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_valuations WHERE workspace_id=? AND investment_id=? AND valuation_date=? AND amount_cents!=0`, in.WorkspaceID, in.InvestmentID, w.WithdrawalDate).Scan(&contradictions); err != nil {
			return err
		}
		if contradictions != 0 {
			return ErrInvestmentConflict
		}
	}
	// Revalidate persisted funding identities without treating outgoing receipts
	// as missing funding capacity or subtracting them from the residual.
	rows, err = tx.QueryContext(ctx, `SELECT transaction_id,snapshot_json FROM investment_purchase_funding WHERE workspace_id=? AND investment_id=? ORDER BY purchase_id,transaction_id`, in.WorkspaceID, in.InvestmentID)
	if err != nil {
		return err
	}
	type funding struct{ id, snapshot string }
	var funds []funding
	for rows.Next() {
		var f funding
		if err = rows.Scan(&f.id, &f.snapshot); err != nil {
			rows.Close()
			return err
		}
		funds = append(funds, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, f := range funds {
		_, snapshot, err := readTransferFunding(ctx, tx, in.WorkspaceID, s.Input.AccountID, f.id)
		if err != nil {
			return err
		}
		if snapshot != f.snapshot {
			return ErrInvestmentConflict
		}
		proof.Funding = append(proof.Funding, snapshot)
	}
	return nil
}

func settlementPriorEffects(ctx context.Context, tx *sql.Tx, in ApplyInvestmentSettlementInput, account, date string) ([]settlementEffectProof, int64, error) {
	var proofs []settlementEffectProof
	total := new(big.Int)
	for _, e := range in.PriorEffects {
		var snapshot string
		var delta int64
		var used int
		if e.AdjustmentID != "" {
			var before, after, version, created int64
			var author, reason string
			err := tx.QueryRowContext(ctx, `SELECT before_balance,after_balance,expected_version,created_at,created_by,reason,delta_cents FROM account_balance_adjustments WHERE workspace_id=? AND account_id=? AND id=?`, in.WorkspaceID, account, e.AdjustmentID).Scan(&before, &after, &version, &created, &author, &reason, &delta)
			if err != nil {
				return nil, 0, err
			}
			// B gains/losses are already inside patrimony; no double subtraction.
			err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM investment_financial_adjustments WHERE workspace_id=? AND adjustment_id=?)+(SELECT COUNT(*) FROM investment_settlement_applications WHERE workspace_id=? AND adjustment_id=?)+(SELECT COUNT(*) FROM investment_settlement_prior_effects WHERE workspace_id=? AND adjustment_id=?)`, in.WorkspaceID, e.AdjustmentID, in.WorkspaceID, e.AdjustmentID, in.WorkspaceID, e.AdjustmentID).Scan(&used)
			if err != nil {
				return nil, 0, err
			}
			b, err := json.Marshal([]any{e.AdjustmentID, account, before, after, version, created, author, reason, delta})
			if err != nil {
				return nil, 0, err
			}
			snapshot = string(b)
		} else {
			var typ, status, day string
			var amount, at int64
			err := tx.QueryRowContext(ctx, `SELECT type,status,amount,COALESCE(paid_at,date),CASE WHEN paid_at IS NOT NULL THEN COALESCE(NULLIF(paid_date_civil,''),strftime('%Y-%m-%d',paid_at,'unixepoch')) ELSE COALESCE(NULLIF(date_civil,''),strftime('%Y-%m-%d',date,'unixepoch')) END FROM financial_transactions WHERE workspace_id=? AND account_id=? AND id=? AND (destination_account_id IS NULL OR destination_account_id='')`, in.WorkspaceID, account, e.TransactionID).Scan(&typ, &status, &amount, &at, &day)
			if err != nil {
				return nil, 0, err
			}
			if typ == "INCOME" {
				return nil, 0, ErrInvestmentLegacyIncome
			}
			if typ != "EXPENSE" || status != "paid" || amount <= 0 || day != date || day != time.Unix(at, 0).UTC().Format("2006-01-02") {
				return nil, 0, ErrInvestmentConflict
			}
			delta = -amount
			err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM investment_settlement_prior_effects WHERE workspace_id=? AND transaction_id=?)+(SELECT COUNT(*) FROM transaction_refunds WHERE workspace_id=? AND transaction_id=?)`, in.WorkspaceID, e.TransactionID, in.WorkspaceID, e.TransactionID).Scan(&used)
			if err != nil {
				return nil, 0, err
			}
			b, err := json.Marshal([]any{e.TransactionID, account, typ, status, amount, at, day})
			if err != nil {
				return nil, 0, err
			}
			snapshot = string(b)
		}
		if used != 0 || delta != e.DeltaCents {
			return nil, 0, ErrInvestmentConflict
		}
		total.Add(total, big.NewInt(delta))
		proofs = append(proofs, settlementEffectProof{InvestmentSettlementPriorEffect: e, Snapshot: snapshot})
	}
	if !total.IsInt64() {
		return nil, 0, ErrBalanceOutOfRange
	}
	return proofs, total.Int64(), nil
}
