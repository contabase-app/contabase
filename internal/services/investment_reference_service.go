package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

type RecordInvestmentReferenceInput struct {
	WorkspaceID, InvestmentID, AccountID, PositionID string
	CreatedBy, RequestID, Reason, CoverageToken      string
	ExpectedVersion                                  int64
	RepresentedCents                                 *int64
	Confirm, DayClosed                               bool
}

// ReadInvestmentReferenceCandidate supplies the exact evidence the caller must
// present for confirmation. The token is a concurrency witness, not authority.
// No balance is read or inferred. All operations through the civil date count.
func ReadInvestmentReferenceCandidate(ctx context.Context, db *sql.DB, workspace, investment, position string) (models.InvestmentReferenceCandidate, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentReferenceCandidate{}, err
	}
	defer tx.Rollback()
	candidate, err := investmentReferenceCandidateTx(ctx, tx, workspace, investment, position)
	if err != nil {
		return candidate, err
	}
	return candidate, tx.Commit()
}

func referenceToken(snapshot models.InvestmentReferenceSnapshot) (string, []byte, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), encoded, nil
}

func referenceSameDay(s models.InvestmentReferenceSnapshot) bool {
	for _, p := range s.Purchases {
		if p.PurchaseDate == s.Position.ValuationDate {
			return true
		}
	}
	for _, w := range s.Withdrawals {
		if w.WithdrawalDate == s.Position.ValuationDate {
			return true
		}
	}
	return false
}

func investmentReferenceCandidateTx(ctx context.Context, tx *sql.Tx, workspace, investment, position string) (models.InvestmentReferenceCandidate, error) {
	var result models.InvestmentReferenceCandidate
	s := &result.Snapshot
	s.WorkspaceID, s.InvestmentID = workspace, investment
	var status, accountType string
	var archived, accountArchived sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT i.account_id,i.status,i.archived_at,a.type,a.archived_at
	 FROM investments i JOIN accounts a ON a.id=i.account_id AND a.workspace_id=i.workspace_id
	 WHERE i.workspace_id=? AND i.id=?`, workspace, investment).Scan(&s.AccountID, &status, &archived, &accountType, &accountArchived)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrInvestmentNotFound
	}
	if err != nil {
		return result, err
	}
	if status != models.InvestmentStatusAtiva || archived.Valid || accountArchived.Valid || accountType != models.AccountTypeInvestment {
		return result, ErrInvestmentInvalidStatus
	}
	s.Position, err = scanValuation(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT id,workspace_id,investment_id,valuation_date,amount_cents,created_by,created_at,updated_by,updated_at
		 FROM investment_valuations WHERE workspace_id=? AND investment_id=? AND id=?`, workspace, investment, position).Scan(dest...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrInvestmentNotFound
	}
	if err != nil {
		return result, err
	}
	if _, err = civildate.ParseCivilDateInput(s.Position.ValuationDate); err != nil || s.Position.AmountCents < 0 {
		return result, ErrInvestmentValidation
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+purchaseColumns+` FROM investment_purchases WHERE workspace_id=? AND investment_id=? AND purchase_date<=? ORDER BY purchase_date,id`, workspace, investment, s.Position.ValuationDate)
	if err != nil {
		return result, err
	}
	var total int64
	for rows.Next() {
		p, err := scanInvestmentPurchase(rows.Scan)
		if err != nil {
			rows.Close()
			return result, err
		}
		if _, err = civildate.ParseCivilDateInput(p.PurchaseDate); err != nil || p.PrincipalCents <= 0 {
			rows.Close()
			return result, ErrInvestmentValidation
		}
		if total > math.MaxInt64-p.PrincipalCents {
			rows.Close()
			return result, ErrBalanceOutOfRange
		}
		total += p.PrincipalCents
		s.Purchases = append(s.Purchases, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	history, err := readInvestmentWithdrawals(ctx, tx, workspace, investment)
	if err != nil {
		return result, err
	}
	total = 0
	for _, w := range history {
		if _, err = civildate.ParseCivilDateInput(w.WithdrawalDate); err != nil {
			return result, ErrInvestmentValidation
		}
		if w.WithdrawalDate > s.Position.ValuationDate {
			continue
		}
		if w.ReceivedCents < 0 || total > math.MaxInt64-w.ReceivedCents {
			return result, ErrBalanceOutOfRange
		}
		total += w.ReceivedCents
		var allocated, principal int64
		for _, a := range w.Allocations {
			if a.ReceivedCents < 0 || allocated > math.MaxInt64-a.ReceivedCents {
				return result, ErrBalanceOutOfRange
			}
			allocated += a.ReceivedCents
			if a.PrincipalCents != nil {
				if *a.PrincipalCents < 0 || principal > math.MaxInt64-*a.PrincipalCents {
					return result, ErrBalanceOutOfRange
				}
				principal += *a.PrincipalCents
			}
		}
		if allocated != w.ReceivedCents || len(w.Allocations) == 0 {
			return result, ErrInvestmentValidation
		}
		s.Withdrawals = append(s.Withdrawals, w)
	}
	result.HasSameDayOperations = referenceSameDay(*s)
	result.CoverageToken, _, err = referenceToken(*s)
	return result, err
}

// RecordInvestmentReference appends preparatory evidence only. Use the runtime
// Immediate pool: the writer reservation must precede all decisions. There is
// no automatic call from legacy investment mutation flows.
func RecordInvestmentReference(ctx context.Context, db *sql.DB, in RecordInvestmentReferenceInput) (models.InvestmentReferenceRevision, error) {
	result, err := RecordInvestmentReferenceWithOutcome(ctx, db, in)
	return result.InvestmentReferenceRevision, err
}

// InvestmentOperationOutcome describes this invocation, never persisted evidence.
type InvestmentOperationOutcome string

const (
	InvestmentOperationCreated  InvestmentOperationOutcome = "CREATED"
	InvestmentOperationReplayed InvestmentOperationOutcome = "REPLAYED"
)

type InvestmentReferenceOperationResult struct {
	models.InvestmentReferenceRevision
	Outcome InvestmentOperationOutcome `json:"outcome"`
}

// RecordInvestmentReferenceWithOutcome preserves A's result and reports which
// writer-transaction branch committed. Errors never return a success outcome.
func RecordInvestmentReferenceWithOutcome(ctx context.Context, db *sql.DB, in RecordInvestmentReferenceInput) (InvestmentReferenceOperationResult, error) {
	var empty InvestmentReferenceOperationResult
	key, err := uuid.Parse(in.RequestID)
	in.Reason = strings.TrimSpace(in.Reason)
	if err != nil || in.WorkspaceID == "" || in.InvestmentID == "" || in.AccountID == "" || in.PositionID == "" || in.CreatedBy == "" || in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || len(in.CoverageToken) != 64 || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 500 || (in.RepresentedCents != nil && *in.RepresentedCents < 0) || (in.Confirm && in.RepresentedCents == nil) {
		return empty, ErrInvestmentValidation
	}
	in.RequestID = key.String()
	payload, err := json.Marshal(in)
	if err != nil {
		return empty, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	result, err := recordInvestmentReferenceTx(ctx, tx, in, payload)
	if err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, &InvestmentCommitError{err: err}
	}
	return result, nil
}

// recordInvestmentReferenceTx receives the normalized input and its canonical
// payload from the owner of the writer transaction. It never opens or commits one.
func recordInvestmentReferenceTx(ctx context.Context, tx *sql.Tx, in RecordInvestmentReferenceInput, payload []byte) (InvestmentReferenceOperationResult, error) {
	var empty InvestmentReferenceOperationResult
	var existingID, existingPayload string
	err := tx.QueryRowContext(ctx, `SELECT id,payload_json FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=? AND request_id=?`, in.WorkspaceID, in.InvestmentID, in.RequestID).Scan(&existingID, &existingPayload)
	if err == nil {
		if existingPayload != string(payload) {
			return empty, ErrInvestmentConflict
		}
		result, err := readInvestmentReferenceRevisionTx(ctx, tx, in.WorkspaceID, in.InvestmentID, existingID)
		if err != nil {
			return empty, err
		}
		return InvestmentReferenceOperationResult{InvestmentReferenceRevision: result, Outcome: InvestmentOperationReplayed}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if err = rejectCancelledInvestmentTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return empty, err
	}
	if err = rejectFundedInvestmentTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return empty, err
	}
	if err = rejectPendingInvestmentAdoptionTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return empty, err
	}
	candidate, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.PositionID)
	if err != nil {
		return empty, err
	}
	if candidate.Snapshot.AccountID != in.AccountID {
		return empty, ErrInvestmentNotFound
	}
	if candidate.CoverageToken != in.CoverageToken {
		return empty, ErrInvestmentConflict
	}
	if in.Confirm && candidate.HasSameDayOperations && !in.DayClosed {
		return empty, ErrInvestmentValidation
	}
	var previous, root string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT id,reference_id,version FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID).Scan(&previous, &root, &version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if version != in.ExpectedVersion {
		return empty, ErrInvestmentConflict
	}
	var positionVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position_version),0) FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=? AND position_id=?`, in.WorkspaceID, in.InvestmentID, in.PositionID).Scan(&positionVersion); err != nil {
		return empty, err
	}
	if positionVersion == math.MaxInt64 {
		return empty, ErrBalanceOutOfRange
	}
	_, snapshot, err := referenceToken(candidate.Snapshot)
	if err != nil {
		return empty, err
	}
	id := uuid.NewString()
	if version == 0 {
		root = id
	}
	now := time.Now().Unix()
	var previousValue, confirmedAt any
	if previous != "" {
		previousValue = previous
	}
	if in.Confirm {
		confirmedAt = now
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_reference_revisions
	 (workspace_id,investment_id,account_id,id,reference_id,previous_revision_id,version,expected_version,position_id,position_version,economic_date,represented_cents,confirmed,day_closed,confirmed_at,created_by,created_at,request_id,reason,snapshot_json,coverage_token,payload_json)
	 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, in.AccountID, id, root, previousValue, version+1, version, in.PositionID, positionVersion+1, candidate.Snapshot.Position.ValuationDate, in.RepresentedCents, in.Confirm, in.DayClosed, confirmedAt, in.CreatedBy, now, in.RequestID, in.Reason, string(snapshot), candidate.CoverageToken, string(payload))
	if err != nil {
		return empty, err
	}
	for _, p := range candidate.Snapshot.Purchases {
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_reference_coverage(workspace_id,investment_id,revision_id,kind,operation_id,purchase_id) VALUES(?,?,?,'PURCHASE',?,?)`, in.WorkspaceID, in.InvestmentID, id, p.ID, p.ID)
		if err != nil {
			return empty, err
		}
	}
	for _, w := range candidate.Snapshot.Withdrawals {
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_reference_coverage(workspace_id,investment_id,revision_id,kind,operation_id,withdrawal_id) VALUES(?,?,?,'WITHDRAWAL',?,?)`, in.WorkspaceID, in.InvestmentID, id, w.ID, w.ID)
		if err != nil {
			return empty, err
		}
	}
	result, err := readInvestmentReferenceRevisionTx(ctx, tx, in.WorkspaceID, in.InvestmentID, id)
	if err != nil {
		return empty, err
	}
	return InvestmentReferenceOperationResult{InvestmentReferenceRevision: result, Outcome: InvestmentOperationCreated}, nil
}

// ReadInvestmentReferenceRevision returns historical evidence plus its current
// validity, all from one snapshot. An empty revisionID selects the latest one.
func ReadInvestmentReferenceRevision(ctx context.Context, db *sql.DB, workspace, investment, revisionID string) (models.InvestmentReferenceRevision, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentReferenceRevision{}, err
	}
	defer tx.Rollback()
	result, err := readInvestmentReferenceRevisionTx(ctx, tx, workspace, investment, revisionID)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func readInvestmentReferenceRevisionTx(ctx context.Context, tx *sql.Tx, workspace, investment, id string) (models.InvestmentReferenceRevision, error) {
	var r models.InvestmentReferenceRevision
	var snapshot, positionID, economicDate string
	err := tx.QueryRowContext(ctx, `SELECT id,reference_id,COALESCE(previous_revision_id,''),workspace_id,investment_id,account_id,version,expected_version,position_version,represented_cents,confirmed,day_closed,created_by,created_at,confirmed_at,request_id,reason,origin,integration_state,snapshot_json,coverage_token,position_id,economic_date
	 FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=? AND (?='' OR id=?) ORDER BY version DESC LIMIT 1`, workspace, investment, id, id).Scan(&r.ID, &r.ReferenceID, &r.PreviousRevisionID, &r.WorkspaceID, &r.InvestmentID, &r.AccountID, &r.Version, &r.ExpectedVersion, &r.PositionVersion, &r.RepresentedCents, &r.Confirmed, &r.DayClosed, &r.CreatedBy, &r.CreatedAt, &r.ConfirmedAt, &r.RequestID, &r.Reason, &r.Origin, &r.IntegrationState, &snapshot, &r.Candidate.CoverageToken, &positionID, &economicDate)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrInvestmentNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(snapshot), &r.Candidate.Snapshot); err != nil {
		return r, err
	}
	s := r.Candidate.Snapshot
	token, _, err := referenceToken(s)
	if err != nil {
		return r, err
	}
	if token != r.Candidate.CoverageToken || s.WorkspaceID != workspace || s.InvestmentID != investment || s.AccountID != r.AccountID || s.Position.ID != positionID || s.Position.ValuationDate != economicDate {
		return r, fmt.Errorf("invalid reference evidence")
	}
	r.Candidate.HasSameDayOperations = referenceSameDay(s)
	// Check the relational coverage against the immutable evidence too. A
	// partially inserted record must never be accepted as a valid reference.
	expected := make(map[string]bool)
	for _, p := range s.Purchases {
		expected["PURCHASE:"+p.ID] = true
	}
	for _, w := range s.Withdrawals {
		expected["WITHDRAWAL:"+w.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,operation_id FROM investment_reference_coverage WHERE workspace_id=? AND investment_id=? AND revision_id=?`, workspace, investment, r.ID)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var kind, operation string
		if err = rows.Scan(&kind, &operation); err != nil {
			rows.Close()
			return r, err
		}
		key := kind + ":" + operation
		if !expected[key] {
			rows.Close()
			return r, fmt.Errorf("invalid reference coverage")
		}
		delete(expected, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	if len(expected) != 0 {
		return r, fmt.Errorf("incomplete reference coverage")
	}
	var latest int64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(version) FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&latest); err != nil {
		return r, err
	}
	if latest > r.Version {
		r.State = models.InvestmentReferenceSuperseded
		return r, nil
	}
	current, err := investmentReferenceCandidateTx(ctx, tx, workspace, investment, positionID)
	if errors.Is(err, ErrInvestmentNotFound) || errors.Is(err, ErrInvestmentInvalidStatus) || errors.Is(err, ErrInvestmentValidation) || errors.Is(err, ErrBalanceOutOfRange) {
		r.State = models.InvestmentReferenceInvalidated
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if current.CoverageToken != r.Candidate.CoverageToken || (r.Confirmed && r.Candidate.HasSameDayOperations && !r.DayClosed) {
		r.State = models.InvestmentReferenceInvalidated
		return r, nil
	}
	r.State = models.InvestmentReferencePending
	if r.Confirmed {
		r.State = models.InvestmentReferenceValid
	}
	return r, nil
}
