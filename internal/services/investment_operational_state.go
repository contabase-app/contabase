package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/contabase-app/contabase/internal/models"
)

// InvestmentOperationalState is a read-only snapshot, never permission to write.
// Financial writers must revalidate all evidence in their own transaction.
type InvestmentOperationalState struct {
	AccountID                            string
	ReferenceState                       string
	Reference                            *models.InvestmentReferenceRevision
	Candidate                            *models.InvestmentReferenceCandidate
	FinancialVersion, BalanceVersion     int64
	LastIntegratedPosition               *models.InvestmentValuation
	PendingPurchases, PendingWithdrawals []string
	BlockReason                          string
}

// ReadInvestmentOperationalState uses only read transactions and the A/B
// evidence validators. No ledger, aggregation with incidental writes, or free
// cash inference is involved. Position is optional; omission selects the latest.
func ReadInvestmentOperationalState(ctx context.Context, db *sql.DB, workspace, investment, position string) (InvestmentOperationalState, error) {
	s := InvestmentOperationalState{ReferenceState: "MISSING", PendingPurchases: []string{}, PendingWithdrawals: []string{}}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return s, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT i.account_id,a.balance_version FROM investments i JOIN accounts a ON a.workspace_id=i.workspace_id AND a.id=i.account_id WHERE i.workspace_id=? AND i.id=?`, workspace, investment).Scan(&s.AccountID, &s.BalanceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrInvestmentNotFound
	}
	if err != nil {
		return s, err
	}
	root, err := readInvestmentReferenceRevisionTx(ctx, tx, workspace, investment, "")
	if err == nil {
		s.Reference = &root
		s.ReferenceState = root.State
	} else if !errors.Is(err, ErrInvestmentNotFound) {
		return s, err
	}
	var chainJSON, financialReference string
	err = tx.QueryRowContext(ctx, `SELECT version,chain_json,reference_revision_id FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? ORDER BY version DESC LIMIT 1`, workspace, investment).Scan(&s.FinancialVersion, &chainJSON, &financialReference)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, err
	}
	var chain []financialPosition
	if chainJSON != "" {
		if financialReference != root.ID {
			s.BlockReason = "STALE_FINANCIAL_REFERENCE"
		}
		if err = json.Unmarshal([]byte(chainJSON), &chain); err != nil {
			return s, err
		}
		if len(chain) == 0 {
			return s, errors.New("empty financial evidence")
		}
		last := chain[len(chain)-1].Candidate.Snapshot.Position
		s.LastIntegratedPosition = &last
		for _, p := range chain {
			current, e := investmentReferenceCandidateTx(ctx, tx, workspace, investment, p.Candidate.Snapshot.Position.ID)
			if e != nil && !errors.Is(e, ErrInvestmentNotFound) && !errors.Is(e, ErrInvestmentInvalidStatus) {
				return s, e
			}
			if e != nil || current.CoverageToken != p.Candidate.CoverageToken {
				s.BlockReason = "STALE_FINANCIAL_POSITION"
			}
		}
	}
	if position == "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM investment_valuations WHERE workspace_id=? AND investment_id=? ORDER BY valuation_date DESC,id DESC LIMIT 1`, workspace, investment).Scan(&position)
		if errors.Is(err, sql.ErrNoRows) {
			s.BlockReason = "POSITION_MISSING"
		} else if err != nil {
			return s, err
		}
	}
	if position != "" {
		candidate, e := investmentReferenceCandidateTx(ctx, tx, workspace, investment, position)
		if e != nil {
			return s, e
		}
		s.Candidate = &candidate
	}
	// List operations outside the immutable integrated coverage, including ones
	// later than the selected position. They do not imply money available.
	coveredPurchases, coveredWithdrawals := map[string]bool{}, map[string]bool{}
	if len(chain) > 0 {
		for _, p := range chain[len(chain)-1].Candidate.Snapshot.Purchases {
			coveredPurchases[p.ID] = true
		}
		for _, w := range chain[len(chain)-1].Candidate.Snapshot.Withdrawals {
			coveredWithdrawals[w.ID] = true
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT 'PURCHASE',id FROM investment_purchases WHERE workspace_id=? AND investment_id=? UNION ALL SELECT 'WITHDRAWAL',id FROM investment_withdrawals WHERE workspace_id=? AND investment_id=? ORDER BY 1,2`, workspace, investment, workspace, investment)
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var kind, id string
		if err = rows.Scan(&kind, &id); err != nil {
			rows.Close()
			return s, err
		}
		if kind == "PURCHASE" && !coveredPurchases[id] {
			s.PendingPurchases = append(s.PendingPurchases, id)
		}
		if kind == "WITHDRAWAL" && !coveredWithdrawals[id] {
			s.PendingWithdrawals = append(s.PendingWithdrawals, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	if s.ReferenceState != models.InvestmentReferenceValid {
		s.BlockReason = "REFERENCE_" + s.ReferenceState
	} else if root.RepresentedCents == nil || *root.RepresentedCents != root.Candidate.Snapshot.Position.AmountCents {
		s.BlockReason = "HISTORICAL_RECONCILIATION_PENDING"
	} else if s.BlockReason == "" && len(chain) == 0 {
		s.BlockReason = "ROOT_ACTIVATION_REQUIRED"
	} else if s.BlockReason == "" && s.Candidate != nil {
		probe := append([]financialPosition(nil), chain...)
		last := chain[len(chain)-1].Candidate
		if s.Candidate.Snapshot.Position.ValuationDate > last.Snapshot.Position.ValuationDate {
			probe = append(probe, financialPosition{Candidate: *s.Candidate})
		}
		_, e := validateFinancialFunding(ctx, tx, IntegrateInvestmentPositionInput{WorkspaceID: workspace, InvestmentID: investment, AccountID: s.AccountID}, probe)
		switch {
		case errors.Is(e, ErrInvestmentSettlementPending):
			s.BlockReason = "WITHDRAWAL_RECONCILIATION_PENDING"
		case errors.Is(e, ErrInvestmentFundingPending):
			s.BlockReason = "PURCHASE_FUNDING_PENDING"
		case errors.Is(e, ErrInvestmentConflict):
			s.BlockReason = "STALE_FINANCIAL_EVIDENCE"
		case e != nil:
			return s, e
		}
	}
	return s, tx.Commit()
}
