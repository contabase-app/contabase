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
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

const (
	InvestmentClassificationCompleted   = "COMPLETED"
	InvestmentClassificationUnavailable = "UNAVAILABLE"
)

var ErrInvestmentLegacyClassification = errors.New("efeito gerencial legado exige conciliação específica")

type ClassifyInvestmentSettlementInput struct {
	WorkspaceID, InvestmentID, WithdrawalID, SettlementRevisionID, ApplicationID string
	ExpectedSettlementVersion, ExpectedVersion                                   int64
	CreatedBy, RequestID, CategoryID                                             string
	// Trusted caller attests a complete review of legacy income, taxes and costs.
	// Known legacy identities are never silently edited or counted a second time.
	LegacyReconciliationEvidence string
	LegacyTransactionIDs         []string
}

type InvestmentResultClassification struct {
	ID, TransactionID, State, Nature, EconomicDate string
	ResultCents                                    *int64
	Version, CreatedAt                             int64
}

// ClassifyInvestmentSettlementResult is internal and opt-in. The trusted caller
// authorizes CreatedBy and supplies RuntimeDBs.Immediate. No route, job, startup
// or C2A call activates this service. An uncertain commit must be replayed using
// the ORIGINAL key and payload. Existing C1/C2A evidence remains immutable;
// this audit row is the authoritative classification completion state.
func ClassifyInvestmentSettlementResult(ctx context.Context, db *sql.DB, in ClassifyInvestmentSettlementInput) (InvestmentResultClassification, error) {
	var zero InvestmentResultClassification
	key, err := uuid.Parse(in.RequestID)
	in.LegacyReconciliationEvidence = strings.TrimSpace(in.LegacyReconciliationEvidence)
	if err != nil || in.WorkspaceID == "" || in.InvestmentID == "" || in.WithdrawalID == "" || in.SettlementRevisionID == "" || in.ApplicationID == "" || in.CreatedBy == "" || in.ExpectedSettlementVersion <= 0 || in.ExpectedVersion != 0 || in.LegacyReconciliationEvidence == "" || utf8.RuneCountInString(in.LegacyReconciliationEvidence) > 2000 {
		return zero, ErrInvestmentValidation
	}
	in.RequestID = key.String()
	in.LegacyTransactionIDs = append([]string(nil), in.LegacyTransactionIDs...)
	sort.Strings(in.LegacyTransactionIDs)
	for i, id := range in.LegacyTransactionIDs {
		if id == "" || i > 0 && id == in.LegacyTransactionIDs[i-1] {
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
	// Reserve the writer before ANY reads, including for a deferred pool.
	if _, err = tx.ExecContext(ctx, `UPDATE investment_result_classifications SET id=id WHERE 0`); err != nil {
		return zero, err
	}
	var savedPayload, savedResult string
	err = tx.QueryRowContext(ctx, `SELECT payload_json,result_json FROM investment_result_classifications WHERE workspace_id=? AND request_id=?`, in.WorkspaceID, in.RequestID).Scan(&savedPayload, &savedResult)
	if err == nil {
		if savedPayload != string(payload) {
			return zero, ErrInvestmentConflict
		}
		if err = json.Unmarshal([]byte(savedResult), &zero); err != nil {
			return InvestmentResultClassification{}, err
		}
		if err = tx.Commit(); err != nil {
			return InvestmentResultClassification{}, &InvestmentCommitError{err: err}
		}
		return zero, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_result_classifications WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID).Scan(&existing); err != nil {
		return zero, err
	}
	if existing != 0 {
		return zero, ErrInvestmentConflict
	}
	s, app, err := classificationSourceTx(ctx, tx, in)
	if err != nil {
		return zero, err
	}
	// A prior EXPENSE already enters the DRE, including costs/withholding already
	// netted in C1. A later dedicated reconciliation must decide its treatment.
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_settlement_prior_effects WHERE workspace_id=? AND application_id=? AND transaction_id IS NOT NULL`, in.WorkspaceID, app.ID).Scan(&existing); err != nil {
		return zero, err
	}
	if existing != 0 {
		return zero, ErrInvestmentLegacyClassification
	}
	for _, id := range in.LegacyTransactionIDs {
		var typ string
		if err = tx.QueryRowContext(ctx, `SELECT type FROM transactions WHERE workspace_id=? AND id=?`, in.WorkspaceID, id).Scan(&typ); errors.Is(err, sql.ErrNoRows) {
			return zero, ErrInvestmentNotFound
		} else if err != nil {
			return zero, err
		}
		return zero, ErrInvestmentLegacyClassification
	}
	r := InvestmentResultClassification{ID: uuid.NewString(), State: InvestmentClassificationCompleted, Nature: "ZERO", EconomicDate: s.Input.EconomicDate, ResultCents: s.RealizedResultCents, Version: 1, CreatedAt: time.Now().Unix()}
	var amount int64
	typ := models.TransactionTypeIncome
	if s.PrincipalCents == nil {
		r.State, r.Nature = InvestmentClassificationUnavailable, "UNKNOWN"
	} else if s.RealizedResultCents == nil {
		return zero, ErrInvestmentConflict
	} else if *s.RealizedResultCents != 0 {
		v := big.NewInt(*s.RealizedResultCents)
		r.Nature = "REALIZED_GAIN"
		if v.Sign() < 0 {
			typ, r.Nature = models.TransactionTypeExpense, "REALIZED_LOSS"
			v.Neg(v)
		}
		if !v.IsInt64() {
			return zero, ErrBalanceOutOfRange
		}
		amount = v.Int64()
		var categoryType string
		err = tx.QueryRowContext(ctx, `SELECT type FROM categories WHERE workspace_id=? AND id=?`, in.WorkspaceID, in.CategoryID).Scan(&categoryType)
		if errors.Is(err, sql.ErrNoRows) {
			return zero, ErrInvestmentNotFound
		}
		if err != nil {
			return zero, err
		}
		if categoryType != typ {
			return zero, ErrInvestmentValidation
		}
		r.TransactionID = uuid.NewString()
	}
	if r.TransactionID == "" && in.CategoryID != "" {
		return zero, ErrInvestmentValidation
	}
	economic, err := time.Parse("2006-01-02", r.EconomicDate)
	if err != nil {
		return zero, err
	}
	at := economic.Add(12 * time.Hour).Unix()
	result, err := json.Marshal(r)
	if err != nil {
		return zero, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_result_classifications
 (workspace_id,id,investment_id,withdrawal_id,settlement_revision_id,application_id,settlement_version,version,transaction_id,category_id,principal_cents,result_cents,nature,state,economic_date,economic_at,created_at,created_by,request_id,payload_json,result_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, r.ID, in.InvestmentID, in.WithdrawalID, s.ID, app.ID, s.Version, r.Version, settlementNullable(r.TransactionID), settlementNullable(in.CategoryID), s.PrincipalCents, r.ResultCents, r.Nature, r.State, r.EconomicDate, at, r.CreatedAt, in.CreatedBy, in.RequestID, string(payload), string(result))
	if err != nil {
		return zero, err
	}
	if r.TransactionID != "" {
		// Deliberately no ApplyBalanceEffect, reserve effect or financial ledger.
		_, err = tx.ExecContext(ctx, `INSERT INTO transactions
 (id,workspace_id,user_id,account_id,category_id,type,amount,date,date_civil,description,status,effect_mode,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,'paid','CLASSIFICATION_ONLY',?,?)`, r.TransactionID, in.WorkspaceID, in.CreatedBy, s.Input.AccountID, in.CategoryID, typ, amount, at, r.EconomicDate, "Resultado realizado de investimento", r.CreatedAt, r.CreatedAt)
		if err != nil {
			return zero, err
		}
	}
	if err = tx.Commit(); err != nil {
		return zero, &InvestmentCommitError{err: err}
	}
	return r, nil
}

// Revalidate sealed C1/C2A identities and the evidence still used by this
// classification. Current account balances are intentionally irrelevant: cash
// may have been transferred after C2A and must never be adjusted here.
func classificationSourceTx(ctx context.Context, tx *sql.Tx, in ClassifyInvestmentSettlementInput) (InvestmentSettlement, InvestmentSettlementApplication, error) {
	var s InvestmentSettlement
	var app InvestmentSettlementApplication
	var result, withdrawalJSON string
	err := tx.QueryRowContext(ctx, `SELECT result_json,withdrawal_json FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID).Scan(&result, &withdrawalJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return s, app, ErrInvestmentNotFound
	}
	if err != nil {
		return s, app, err
	}
	if err = json.Unmarshal([]byte(result), &s); err != nil {
		return s, app, err
	}
	if s.ID != in.SettlementRevisionID || s.Version != in.ExpectedSettlementVersion {
		return s, app, ErrInvestmentConflict
	}
	if s.State != InvestmentSettlementReconciled || s.Input.PatrimonyCents == nil {
		return s, app, ErrInvestmentSettlementPending
	}
	w, err := settlementWithdrawal(ctx, tx, in.WorkspaceID, in.InvestmentID, in.WithdrawalID)
	if err != nil {
		return s, app, err
	}
	b, err := json.Marshal(w)
	if err != nil {
		return s, app, err
	}
	if string(b) != withdrawalJSON || w.ReceivedCents != s.Input.NetCents {
		return s, app, ErrInvestmentConflict
	}
	principal, err := settlementPrincipal(w)
	if err != nil {
		return s, app, err
	}
	if (principal == nil) != (s.PrincipalCents == nil) || principal != nil && *principal != *s.PrincipalCents {
		return s, app, ErrInvestmentConflict
	}
	if principal != nil {
		v := new(big.Int).Sub(big.NewInt(s.Input.NetCents), big.NewInt(*principal))
		if !v.IsInt64() {
			return s, app, ErrBalanceOutOfRange
		}
		if s.RealizedResultCents == nil || *s.RealizedResultCents != v.Int64() {
			return s, app, ErrInvestmentConflict
		}
	} else if s.RealizedResultCents != nil {
		return s, app, ErrInvestmentConflict
	}
	var state, revision, proofJSON, applicationResult string
	var version, net, patrimony, residual int64
	err = tx.QueryRowContext(ctx, `SELECT state,settlement_revision_id,settlement_version,net_cents,patrimony_cents,residual_cents,result_json,proof_json FROM investment_settlement_applications WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? AND id=?`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID, in.ApplicationID).Scan(&state, &revision, &version, &net, &patrimony, &residual, &applicationResult, &proofJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return s, app, ErrInvestmentSettlementPending
	}
	if err != nil {
		return s, app, err
	}
	if state != InvestmentSettlementFinancialApplied || revision != s.ID || version != s.Version || net != s.Input.NetCents || patrimony != *s.Input.PatrimonyCents {
		return s, app, ErrInvestmentConflict
	}
	if err = json.Unmarshal([]byte(applicationResult), &app); err != nil {
		return s, app, err
	}
	if app.ID != in.ApplicationID || app.SettlementRevisionID != s.ID || app.State != state || app.NetCents != net || app.PatrimonyCents != patrimony || app.ResidualCents != residual {
		return s, app, ErrInvestmentConflict
	}
	var proof settlementApplicationProof
	if err = json.Unmarshal([]byte(proofJSON), &proof); err != nil {
		return s, app, err
	}
	if proof.Settlement.ID != s.ID || app.ReferenceRevisionID != s.ReferenceRevisionID || app.FinancialRevisionID != s.FinancialRevisionID {
		return s, app, ErrInvestmentConflict
	}
	encodedSettlement, err := json.Marshal(s)
	if err != nil {
		return s, app, err
	}
	encodedProof, err := json.Marshal(proof.Settlement)
	if err != nil {
		return s, app, err
	}
	if string(encodedSettlement) != string(encodedProof) || len(proof.Chain) == 0 {
		return s, app, ErrInvestmentConflict
	}
	var financialVersion int64
	var referenceID string
	if err = tx.QueryRowContext(ctx, `SELECT version,reference_revision_id FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? AND account_id=? AND id=?`, in.WorkspaceID, in.InvestmentID, s.Input.AccountID, s.FinancialRevisionID).Scan(&financialVersion, &referenceID); err != nil {
		return s, app, err
	}
	if referenceID != s.ReferenceRevisionID {
		return s, app, ErrInvestmentConflict
	}
	claims, err := settlementClaims(ctx, tx, s.Input)
	if err != nil {
		return s, app, err
	}
	total := new(big.Int)
	for _, claim := range claims {
		if !claim.Existing {
			return s, app, ErrInvestmentConflict
		}
		var e settlementReceiptEvidence
		if err = json.Unmarshal([]byte(claim.Snapshot), &e); err != nil {
			return s, app, err
		}
		if e.Type == models.TransactionTypeIncome {
			return s, app, ErrInvestmentLegacyIncome
		}
		total.Add(total, big.NewInt(claim.AmountCents))
	}
	if s.Input.AccountID != s.Input.DestinationAccountID && total.Cmp(big.NewInt(net)) != 0 {
		return s, app, ErrInvestmentSettlementPending
	}
	if app.AdjustmentID != "" {
		var delta int64
		if err = tx.QueryRowContext(ctx, `SELECT delta_cents FROM account_balance_adjustments WHERE workspace_id=? AND account_id=? AND id=?`, in.WorkspaceID, s.Input.AccountID, app.AdjustmentID).Scan(&delta); err != nil {
			return s, app, err
		}
		if delta != residual {
			return s, app, ErrInvestmentConflict
		}
	} else if residual != 0 {
		return s, app, ErrInvestmentConflict
	}
	root, err := readInvestmentReferenceRevisionTx(ctx, tx, in.WorkspaceID, in.InvestmentID, s.ReferenceRevisionID)
	if err != nil {
		return s, app, err
	}
	if root.State != models.InvestmentReferenceValid {
		return s, app, ErrInvestmentConflict
	}
	for _, position := range proof.Chain {
		current, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, position.Candidate.Snapshot.Position.ID)
		if err != nil {
			return s, app, err
		}
		if current.CoverageToken != position.Candidate.CoverageToken {
			return s, app, ErrInvestmentConflict
		}
	}
	if proof.Remaining != nil {
		current, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, proof.Remaining.Snapshot.Position.ID)
		if err != nil {
			return s, app, err
		}
		if current.CoverageToken != proof.Remaining.CoverageToken {
			return s, app, ErrInvestmentConflict
		}
	}
	return s, app, nil
}
