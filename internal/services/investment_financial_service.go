package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

var ErrInvestmentFundingPending = errors.New("compra sem cobertura financeira comprovada")
var ErrInvestmentSettlementPending = errors.New("integração pendente de liquidação/conciliação da retirada")

// PurchaseFunding allocates a positive part of a proven incoming transfer.
// Zero AmountCents preserves the original API: the whole purchase, one source.
// Multiple sources require explicit amounts summing exactly to the purchase.
type PurchaseFunding struct {
	PurchaseID, TransferID string
	AmountCents            int64
}

type IntegrateInvestmentPositionInput struct {
	WorkspaceID, InvestmentID, AccountID, ReferenceRevisionID string
	PositionID, CoverageToken, CreatedBy, RequestID, Reason   string
	ExpectedVersion                                           int64
	DayClosed                                                 bool
	Funding                                                   []PurchaseFunding
	// Optional atomic correction of an already integrated non-root position.
	// CoverageToken identifies the source BEFORE this correction.
	CorrectionCents *int64
}

type InvestmentIntegrationResult struct {
	ID                                                        string
	Version, BalanceVersion, BalanceCents, NetAdjustmentCents int64
	AdjustmentIDs                                             []string
}

type financialPosition struct {
	Candidate    models.InvestmentReferenceCandidate
	DayClosed    bool
	Delta        int64
	AdjustmentID string
}

type financialAdjustmentPlan struct {
	ID, PositionID, Date, Reverses string
	Delta                          int64
}

type transferFundingEvidence struct {
	ID, Source, Destination, Type, Status, EconomicDate string
	Amount, EffectiveAt                                 int64
}

type pendingPurchaseFunding struct {
	PurchaseFunding
	Snapshot string
}

// IntegrateInvestmentPosition is an explicit internal opt-in. Pass the runtime
// Immediate writer; authorization of CreatedBy is the trusted caller's duty.
// Reference confirmation alone does not activate it. Legacy mutation flows do
// not call this API. Retry the same request after an uncertain commit.
func IntegrateInvestmentPosition(ctx context.Context, db *sql.DB, in IntegrateInvestmentPositionInput) (InvestmentIntegrationResult, error) {
	result, err := IntegrateInvestmentPositionWithOutcome(ctx, db, in)
	return result.InvestmentIntegrationResult, err
}

type InvestmentIntegrationOperationResult struct {
	InvestmentIntegrationResult
	Outcome InvestmentOperationOutcome `json:"outcome"`
}

// IntegrateInvestmentPositionWithOutcome keeps the durable financial receipt
// unchanged and reports creation/replay only after its writer transaction commits.
func IntegrateInvestmentPositionWithOutcome(ctx context.Context, db *sql.DB, in IntegrateInvestmentPositionInput) (InvestmentIntegrationOperationResult, error) {
	var empty InvestmentIntegrationOperationResult
	key, err := uuid.Parse(in.RequestID)
	in.Reason = strings.TrimSpace(in.Reason)
	if err != nil || in.WorkspaceID == "" || in.InvestmentID == "" || in.AccountID == "" || in.ReferenceRevisionID == "" || in.PositionID == "" || in.CreatedBy == "" || len(in.CoverageToken) != 64 || in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 500 || (in.CorrectionCents != nil && *in.CorrectionCents < 0) {
		return empty, ErrInvestmentValidation
	}
	in.RequestID = key.String()
	in.Funding = append([]PurchaseFunding(nil), in.Funding...)
	sort.Slice(in.Funding, func(i, j int) bool {
		if in.Funding[i].PurchaseID != in.Funding[j].PurchaseID {
			return in.Funding[i].PurchaseID < in.Funding[j].PurchaseID
		}
		return in.Funding[i].TransferID < in.Funding[j].TransferID
	})
	for i, f := range in.Funding {
		if f.PurchaseID == "" || f.TransferID == "" || f.AmountCents < 0 || (i > 0 && f.PurchaseID == in.Funding[i-1].PurchaseID && f.TransferID == in.Funding[i-1].TransferID) {
			return empty, ErrInvestmentValidation
		}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return empty, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	// Defensive reservation also prevents a read-snapshot upgrade if a caller
	// accidentally supplies a deferred pool. Never changes balance or its version.
	if _, err = tx.ExecContext(ctx, `UPDATE accounts SET balance_version=balance_version WHERE workspace_id=? AND id=?`, in.WorkspaceID, in.AccountID); err != nil {
		return empty, err
	}
	var savedPayload, savedResult string
	err = tx.QueryRowContext(ctx, `SELECT payload_json,result_json FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? AND request_id=?`, in.WorkspaceID, in.InvestmentID, in.RequestID).Scan(&savedPayload, &savedResult)
	if err == nil {
		if savedPayload != string(payload) {
			return empty, ErrInvestmentConflict
		}
		var result InvestmentIntegrationResult
		if err = json.Unmarshal([]byte(savedResult), &result); err != nil {
			return empty, err
		}
		if err = tx.Commit(); err != nil {
			return empty, &InvestmentCommitError{err: err}
		}
		return InvestmentIntegrationOperationResult{InvestmentIntegrationResult: result, Outcome: InvestmentOperationReplayed}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	// Existing durable replays above remain valid. Adoption alone never admits
	// a new financial operation, including activation with a zero adjustment.
	if err = rejectFundedInvestmentTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return empty, err
	}
	if err = rejectPendingInvestmentAdoptionTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return empty, err
	}
	result, err := integrateInvestmentPositionTx(ctx, tx, in, payload)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, &InvestmentCommitError{err: err}
	}
	return InvestmentIntegrationOperationResult{InvestmentIntegrationResult: result, Outcome: InvestmentOperationCreated}, nil
}

// integrateInvestmentPositionTx creates a financial revision in the caller's
// writer transaction. The caller owns input normalization, canonical payload,
// writer reservation, replay, adoption guard and transaction completion.
// All creation-time evidence and versions are revalidated here in that tx.
func integrateInvestmentPositionTx(ctx context.Context, tx *sql.Tx, in IntegrateInvestmentPositionInput, payload []byte) (InvestmentIntegrationResult, error) {
	var empty InvestmentIntegrationResult
	root, err := readInvestmentReferenceRevisionTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.ReferenceRevisionID)
	if err != nil {
		return empty, err
	}
	if root.AccountID != in.AccountID {
		return empty, ErrInvestmentNotFound
	}
	if root.State != models.InvestmentReferenceValid || root.RepresentedCents == nil || *root.RepresentedCents != root.Candidate.Snapshot.Position.AmountCents {
		return empty, ErrInvestmentConflict
	}
	var version int64
	var previous, reference, chainJSON string
	err = tx.QueryRowContext(ctx, `SELECT id,version,reference_revision_id,chain_json FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID).Scan(&previous, &version, &reference, &chainJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if version != in.ExpectedVersion || (version > 0 && reference != root.ID) {
		return empty, ErrInvestmentConflict
	}
	var chain []financialPosition
	if version == 0 {
		chain = []financialPosition{{Candidate: root.Candidate, DayClosed: root.DayClosed}}
	} else if err = json.Unmarshal([]byte(chainJSON), &chain); err != nil {
		return empty, err
	}
	if len(chain) == 0 || chain[0].Candidate.CoverageToken != root.Candidate.CoverageToken {
		return empty, ErrInvestmentConflict
	}
	target, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.PositionID)
	if err != nil {
		return empty, err
	}
	if target.CoverageToken != in.CoverageToken {
		return empty, ErrInvestmentConflict
	}
	if target.HasSameDayOperations && !in.DayClosed {
		return empty, ErrInvestmentValidation
	}
	index := -1
	for i, p := range chain {
		if p.Candidate.Snapshot.Position.ID == in.PositionID {
			index = i
		}
	}
	if version == 0 {
		// Activation records only the root. It cannot silently absorb old positions.
		if index != 0 || in.CorrectionCents != nil {
			return empty, ErrInvestmentConflict
		}
	} else if index == 0 {
		return empty, ErrInvestmentConflict
	}
	if index < 0 && (in.CorrectionCents != nil || target.Snapshot.Position.ValuationDate <= chain[len(chain)-1].Candidate.Snapshot.Position.ValuationDate) {
		return empty, ErrInvestmentConflict
	}
	// Every dependency is revalidated inside this writer transaction. A changed
	// purchase/withdrawal invalidates even a correction with a freshly read token.
	for i, p := range chain {
		current, e := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, p.Candidate.Snapshot.Position.ID)
		if e != nil {
			return empty, e
		}
		if i == index && i > 0 {
			a, b := current.Snapshot, p.Candidate.Snapshot
			a.Position = b.Position
			if !reflect.DeepEqual(a, b) || current.Snapshot.Position.ValuationDate != b.Position.ValuationDate {
				return empty, ErrInvestmentConflict
			}
		} else if current.CoverageToken != p.Candidate.CoverageToken {
			return empty, ErrInvestmentConflict
		}
	}
	if in.CorrectionCents != nil {
		_, err = upsertInvestmentValuationTx(ctx, tx, UpsertInvestmentValuationInput{WorkspaceID: in.WorkspaceID, InvestmentID: in.InvestmentID, ActingUserID: in.CreatedBy, ValuationDate: target.Snapshot.Position.ValuationDate, AmountCents: *in.CorrectionCents})
		if err != nil {
			return empty, err
		}
		target, err = investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.PositionID)
		if err != nil {
			return empty, err
		}
	}
	old := append([]financialPosition(nil), chain...)
	if index < 0 {
		index = len(chain)
		chain = append(chain, financialPosition{Candidate: target, DayClosed: in.DayClosed})
	} else if index > 0 {
		chain[index].Candidate = target
		chain[index].DayClosed = in.DayClosed
	}
	id := uuid.NewString()
	pending, err := validateFinancialFunding(ctx, tx, in, chain)
	if err != nil {
		return empty, err
	}
	var plans []financialAdjustmentPlan
	if version > 0 {
		for i := index; i < len(chain); i++ {
			delta, e := financialIntervalDelta(chain[i-1].Candidate.Snapshot, chain[i].Candidate.Snapshot)
			if e != nil {
				return empty, e
			}
			if i < len(old) && old[i].AdjustmentID != "" {
				if old[i].Delta == math.MinInt64 {
					return empty, ErrBalanceOutOfRange
				}
				plans = append(plans, financialAdjustmentPlan{ID: uuid.NewString(), PositionID: old[i].Candidate.Snapshot.Position.ID, Date: old[i].Candidate.Snapshot.Position.ValuationDate, Reverses: old[i].AdjustmentID, Delta: -old[i].Delta})
			}
			chain[i].Delta = delta
			chain[i].AdjustmentID = ""
			if delta != 0 {
				chain[i].AdjustmentID = uuid.NewString()
				plans = append(plans, financialAdjustmentPlan{ID: chain[i].AdjustmentID, PositionID: chain[i].Candidate.Snapshot.Position.ID, Date: chain[i].Candidate.Snapshot.Position.ValuationDate, Delta: delta})
			}
		}
	}
	result := InvestmentIntegrationResult{ID: id, Version: version + 1}
	net := new(big.Int)
	now := time.Now().Unix()
	for _, p := range plans {
		var balance, balanceVersion int64
		if err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=? AND type='INVESTMENT' AND archived_at IS NULL`, in.WorkspaceID, in.AccountID).Scan(&balance, &balanceVersion); err != nil {
			return empty, err
		}
		next := new(big.Int).Add(big.NewInt(balance), big.NewInt(p.Delta))
		if !next.IsInt64() || balanceVersion == math.MaxInt64 {
			return empty, ErrBalanceOutOfRange
		}
		if err = adjustAccountBalanceTx(ctx, tx, AccountBalanceAdjustment{ID: p.ID, WorkspaceID: in.WorkspaceID, AccountID: in.AccountID, UserID: in.CreatedBy, Reason: in.Reason, ExpectedVersion: balanceVersion, TargetBalance: next.Int64(), CreatedAt: now}); err != nil {
			return empty, err
		}
		net.Add(net, big.NewInt(p.Delta))
		result.AdjustmentIDs = append(result.AdjustmentIDs, p.ID)
	}
	if !net.IsInt64() {
		return empty, ErrBalanceOutOfRange
	}
	result.NetAdjustmentCents = net.Int64()
	if err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, in.AccountID).Scan(&result.BalanceCents, &result.BalanceVersion); err != nil {
		return empty, err
	}
	chainBytes, err := json.Marshal(chain)
	if err != nil {
		return empty, err
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return empty, err
	}
	var previousValue any
	if previous != "" {
		previousValue = previous
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_financial_revisions(workspace_id,investment_id,account_id,id,reference_revision_id,previous_id,version,request_id,created_by,created_at,payload_json,result_json,chain_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, in.AccountID, id, root.ID, previousValue, result.Version, in.RequestID, in.CreatedBy, now, string(payload), string(resultBytes), string(chainBytes))
	if err != nil {
		return empty, err
	}
	for _, p := range plans {
		date, e := time.Parse("2006-01-02", p.Date)
		if e != nil {
			return empty, e
		}
		var reversal any
		if p.Reverses != "" {
			reversal = p.Reverses
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_financial_adjustments(workspace_id,investment_id,account_id,revision_id,adjustment_id,position_id,reverses_id,economic_date,economic_at) VALUES(?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, in.AccountID, id, p.ID, p.PositionID, reversal, p.Date, date.Add(24*time.Hour-time.Second).Unix())
		if err != nil {
			return empty, err
		}
	}
	for _, f := range pending {
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_purchase_funding(workspace_id,investment_id,purchase_id,transaction_id,revision_id,amount_cents,snapshot_json) VALUES(?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, f.PurchaseID, f.TransferID, id, f.AmountCents, f.Snapshot)
		if err != nil {
			return empty, err
		}
	}
	return result, nil
}

func financialIntervalDelta(before, after models.InvestmentReferenceSnapshot) (int64, error) {
	if after.Position.ValuationDate <= before.Position.ValuationDate {
		return 0, ErrInvestmentConflict
	}
	// Existing withdrawals are part of the initial reconciled basis only. There
	// is no financial settlement evidence in Phase B for any new withdrawal.
	if !reflect.DeepEqual(before.Withdrawals, after.Withdrawals) {
		return 0, ErrInvestmentSettlementPending
	}
	known := map[string]models.InvestmentPurchase{}
	for _, p := range before.Purchases {
		known[p.ID] = p
	}
	delta := new(big.Int).Sub(big.NewInt(after.Position.AmountCents), big.NewInt(before.Position.AmountCents))
	for _, p := range after.Purchases {
		if prior, ok := known[p.ID]; ok {
			if !reflect.DeepEqual(prior, p) {
				return 0, ErrInvestmentConflict
			}
			delete(known, p.ID)
		} else {
			if p.PurchaseDate <= before.Position.ValuationDate {
				return 0, ErrInvestmentConflict
			}
			delta.Sub(delta, big.NewInt(p.PrincipalCents))
		}
	}
	if len(known) > 0 {
		return 0, ErrInvestmentConflict
	}
	if !delta.IsInt64() {
		return 0, ErrBalanceOutOfRange
	}
	return delta.Int64(), nil
}

func readTransferFunding(ctx context.Context, tx *sql.Tx, workspace, account, id string) (transferFundingEvidence, string, error) {
	var e transferFundingEvidence
	err := tx.QueryRowContext(ctx, `SELECT t.id,t.account_id,t.destination_account_id,t.type,t.status,t.amount,COALESCE(t.paid_at,t.date),CASE WHEN t.paid_at IS NOT NULL THEN COALESCE(t.paid_date_civil,strftime('%Y-%m-%d',t.paid_at,'unixepoch')) ELSE COALESCE(t.date_civil,strftime('%Y-%m-%d',t.date,'unixepoch')) END
 FROM financial_transactions t JOIN accounts a ON a.workspace_id=t.workspace_id AND a.id=t.account_id
 JOIN accounts d ON d.workspace_id=t.workspace_id AND d.id=t.destination_account_id
 WHERE t.workspace_id=? AND t.id=? AND t.destination_account_id=? AND a.type!='CREDIT_CARD' AND d.type='INVESTMENT'`, workspace, id, account).Scan(&e.ID, &e.Source, &e.Destination, &e.Type, &e.Status, &e.Amount, &e.EffectiveAt, &e.EconomicDate)
	if errors.Is(err, sql.ErrNoRows) {
		return e, "", ErrInvestmentFundingPending
	}
	if err != nil {
		return e, "", err
	}
	if e.Type != "TRANSFER" || e.Status != "paid" || e.Amount <= 0 || e.Source == e.Destination {
		return e, "", ErrInvestmentFundingPending
	}
	if _, err := civildate.ParseCivilDateInput(e.EconomicDate); err != nil || e.EconomicDate != time.Unix(e.EffectiveAt, 0).UTC().Format("2006-01-02") {
		return e, "", ErrInvestmentFundingPending
	}
	b, err := json.Marshal(e)
	return e, string(b), err
}

// Funding claims are allocation evidence, not an account balance. Unallocated
// purchases and cash debits conservatively consume every potentially matching
// source. Ambiguous use can reject a valid allocation, never invent free cash.
type accountFundingClaim struct {
	InvestmentID string
	pendingPurchaseFunding
}

func fundingPurchaseKey(investment, purchase string) string { return investment + "\x00" + purchase }

func validateFinancialFunding(ctx context.Context, tx *sql.Tx, in IntegrateInvestmentPositionInput, chain []financialPosition) ([]pendingPurchaseFunding, error) {
	offered := map[string][]PurchaseFunding{}
	for _, f := range in.Funding {
		offered[f.PurchaseID] = append(offered[f.PurchaseID], f)
	}
	rows, err := tx.QueryContext(ctx, `SELECT f.investment_id,f.purchase_id,f.transaction_id,f.amount_cents,f.snapshot_json
 FROM investment_purchase_funding f JOIN investments i ON i.workspace_id=f.workspace_id AND i.id=f.investment_id
 WHERE f.workspace_id=? AND i.account_id=? ORDER BY f.investment_id,f.purchase_id,f.transaction_id`, in.WorkspaceID, in.AccountID)
	if err != nil {
		return nil, err
	}
	var claims []accountFundingClaim
	existing := map[string][]PurchaseFunding{}
	for rows.Next() {
		var c accountFundingClaim
		if err = rows.Scan(&c.InvestmentID, &c.PurchaseID, &c.TransferID, &c.AmountCents, &c.Snapshot); err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, c)
		if c.InvestmentID == in.InvestmentID {
			existing[c.PurchaseID] = append(existing[c.PurchaseID], c.PurchaseFunding)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	sources := map[string]transferFundingEvidence{}
	encodedSources := map[string]string{}
	// Revalidate every source already claimed on this account before excluding
	// its purchases from the unallocated set. A changed source blocks new proof.
	for _, c := range claims {
		if _, ok := sources[c.TransferID]; !ok {
			e, s, err := readTransferFunding(ctx, tx, in.WorkspaceID, in.AccountID, c.TransferID)
			if err != nil {
				return nil, err
			}
			sources[c.TransferID] = e
			encodedSources[c.TransferID] = s
		}
		if c.Snapshot != encodedSources[c.TransferID] {
			return nil, ErrInvestmentConflict
		}
	}
	seen := map[string]bool{}
	var pending []pendingPurchaseFunding
	for i, pos := range chain {
		if i > 0 {
			if _, err := financialIntervalDelta(chain[i-1].Candidate.Snapshot, pos.Candidate.Snapshot); err != nil {
				return nil, err
			}
		}
		for _, p := range pos.Candidate.Snapshot.Purchases {
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			supplied, provided := offered[p.ID]
			delete(offered, p.ID)
			for n := range supplied {
				if supplied[n].AmountCents == 0 {
					if len(supplied) != 1 {
						return nil, ErrInvestmentValidation
					}
					supplied[n].AmountCents = p.PrincipalCents
				}
			}
			if saved := existing[p.ID]; len(saved) > 0 {
				if provided && !reflect.DeepEqual(saved, supplied) {
					return nil, ErrInvestmentConflict
				}
				continue
			}
			if !provided {
				if i == 0 {
					continue
				} // Root coverage is reconciled; its unclaimed cost is reserved below.
				return nil, ErrInvestmentFundingPending
			}
			total := new(big.Int)
			for _, f := range supplied {
				if f.AmountCents <= 0 {
					return nil, ErrInvestmentValidation
				}
				total.Add(total, big.NewInt(f.AmountCents))
				if _, ok := sources[f.TransferID]; !ok {
					e, s, err := readTransferFunding(ctx, tx, in.WorkspaceID, in.AccountID, f.TransferID)
					if err != nil {
						return nil, err
					}
					sources[f.TransferID] = e
					encodedSources[f.TransferID] = s
				}
				if sources[f.TransferID].EconomicDate > p.PurchaseDate {
					return nil, ErrInvestmentFundingPending
				}
				c := pendingPurchaseFunding{PurchaseFunding: f, Snapshot: encodedSources[f.TransferID]}
				pending = append(pending, c)
				claims = append(claims, accountFundingClaim{InvestmentID: in.InvestmentID, pendingPurchaseFunding: c})
			}
			if total.Cmp(big.NewInt(p.PrincipalCents)) != 0 {
				return nil, ErrInvestmentFundingPending
			}
		}
	}
	if len(offered) > 0 {
		return nil, ErrInvestmentConflict
	}
	if err = validateAccountFundingCapacity(ctx, tx, in.WorkspaceID, in.AccountID, claims, sources); err != nil {
		return nil, err
	}
	return pending, nil
}

func validateAccountFundingCapacity(ctx context.Context, tx *sql.Tx, workspace, account string, claims []accountFundingClaim, sources map[string]transferFundingEvidence) error {
	if len(sources) == 0 {
		return nil
	}
	allocated := map[string]*big.Int{}
	sourceUse := map[string]*big.Int{}
	for id := range sources {
		sourceUse[id] = new(big.Int)
	}
	for _, c := range claims {
		key := fundingPurchaseKey(c.InvestmentID, c.PurchaseID)
		if allocated[key] == nil {
			allocated[key] = new(big.Int)
		}
		allocated[key].Add(allocated[key], big.NewInt(c.AmountCents))
		sourceUse[c.TransferID].Add(sourceUse[c.TransferID], big.NewInt(c.AmountCents))
	}
	type purchaseCost struct {
		date   string
		amount int64
	}
	var unallocated []purchaseCost
	dates := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT p.investment_id,p.id,p.purchase_date,p.principal_cents
 FROM investment_purchases p JOIN investments i ON i.workspace_id=p.workspace_id AND i.id=p.investment_id
 WHERE p.workspace_id=? AND i.account_id=?`, workspace, account)
	if err != nil {
		return err
	}
	for rows.Next() {
		var investment, id, date string
		var amount int64
		if err = rows.Scan(&investment, &id, &date, &amount); err != nil {
			rows.Close()
			return err
		}
		if _, err = civildate.ParseCivilDateInput(date); err != nil || amount <= 0 {
			rows.Close()
			return ErrInvestmentFundingPending
		}
		key := fundingPurchaseKey(investment, id)
		dates[key] = date
		if n := allocated[key]; n != nil {
			if n.Cmp(big.NewInt(amount)) != 0 {
				rows.Close()
				return ErrInvestmentConflict
			}
		} else {
			unallocated = append(unallocated, purchaseCost{date, amount})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range claims {
		date, ok := dates[fundingPurchaseKey(c.InvestmentID, c.PurchaseID)]
		if !ok || sources[c.TransferID].EconomicDate > date {
			return ErrInvestmentConflict
		}
	}
	// Paid expenses and outgoing transfers can consume a source's cash,
	// including old overdrafts repaid by the incoming transfer. Incoming
	// credits, unrealized valuation gains and unliquidated withdrawals never add
	// capacity. Same-day debits are included without inferring intraday ordering.
	var debits []purchaseCost
	rows, err = tx.QueryContext(ctx, `SELECT amount,CASE WHEN paid_at IS NOT NULL THEN COALESCE(paid_date_civil,strftime('%Y-%m-%d',paid_at,'unixepoch')) ELSE COALESCE(date_civil,strftime('%Y-%m-%d',date,'unixepoch')) END
 FROM financial_transactions WHERE workspace_id=? AND account_id=? AND status='paid' AND type IN ('EXPENSE','TRANSFER')`, workspace, account)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d purchaseCost
		if err = rows.Scan(&d.amount, &d.date); err != nil {
			rows.Close()
			return err
		}
		if _, err = civildate.ParseCivilDateInput(d.date); err != nil || d.amount <= 0 {
			rows.Close()
			return ErrInvestmentFundingPending
		}
		debits = append(debits, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT h.delta_cents,strftime('%Y-%m-%d',h.created_at,'unixepoch')
 FROM account_balance_adjustments h WHERE h.workspace_id=? AND h.account_id=? AND h.delta_cents<0
 AND NOT EXISTS(SELECT 1 FROM investment_financial_adjustments f WHERE f.workspace_id=h.workspace_id AND f.account_id=h.account_id AND f.adjustment_id=h.id)`, workspace, account)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d purchaseCost
		var delta int64
		if err = rows.Scan(&delta, &d.date); err != nil {
			rows.Close()
			return err
		}
		if delta == math.MinInt64 {
			rows.Close()
			return ErrBalanceOutOfRange
		}
		d.amount = -delta
		if _, err = civildate.ParseCivilDateInput(d.date); err != nil {
			rows.Close()
			return ErrInvestmentFundingPending
		}
		debits = append(debits, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var initialBalance int64
	if err = tx.QueryRowContext(ctx, `SELECT initial_balance FROM accounts WHERE workspace_id=? AND id=?`, workspace, account).Scan(&initialBalance); err != nil {
		return err
	}
	for id, source := range sources {
		used := sourceUse[id]
		if initialBalance < 0 {
			used.Sub(used, big.NewInt(initialBalance))
		}
		for _, p := range unallocated {
			if p.date >= source.EconomicDate {
				used.Add(used, big.NewInt(p.amount))
			}
		}
		for _, d := range debits {
			used.Add(used, big.NewInt(d.amount))
		}
		if used.Cmp(big.NewInt(source.Amount)) > 0 {
			return ErrInvestmentConflict
		}
	}
	return nil
}
