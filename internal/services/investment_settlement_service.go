package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

const (
	InvestmentSettlementPending    = "PENDENTE"
	InvestmentSettlementRecorded   = "REGISTRADA"
	InvestmentSettlementReconciled = "CONCILIADA"
)

// Receipt allocations are explicit and permanent. A correction must retain all
// existing claims unchanged; their release/reversal requires a separate phase.
type InvestmentSettlementReceipt struct {
	TransactionID string
	AmountCents   int64
	Evidence      string
}

type RecordInvestmentSettlementInput struct {
	WorkspaceID, InvestmentID, WithdrawalID, AccountID, DestinationAccountID string
	EconomicDate, CreatedBy, RequestID, Reason                               string
	ExpectedVersion                                                          int64
	GrossCents, IRCents, IOFCents, FeesCents                                 *int64
	NetCents                                                                 int64
	PatrimonyCents                                                           *int64
	PatrimonyEvidence, ReceiptEvidence                                       string
	ConfirmReceipt                                                           bool
	Receipts                                                                 []InvestmentSettlementReceipt
}

type InvestmentSettlement struct {
	ID, WorkspaceID, InvestmentID, WithdrawalID, PreviousID string
	Version, CreatedAt                                      int64
	CreatedBy, State, EffectsState                          string
	PrincipalCents, RealizedResultCents                     *int64
	ReferenceRevisionID, FinancialRevisionID                string
	Input                                                   RecordInvestmentSettlementInput
}

type settlementReceiptEvidence struct {
	ID, Source, Destination, Type, Status, EconomicDate string
	Amount, EffectiveAt                                 int64
}

type settlementClaim struct {
	InvestmentSettlementReceipt
	Source, Destination, Snapshot string
	Existing                      bool
}

// RecordInvestmentSettlement is C1 preparation only. The trusted caller must
// authorize CreatedBy and supply the runtime Immediate writer. No balance,
// income, transfer, position or withdrawal is written here.
func RecordInvestmentSettlement(ctx context.Context, db *sql.DB, in RecordInvestmentSettlementInput) (InvestmentSettlement, error) {
	var zero InvestmentSettlement
	key, err := uuid.Parse(in.RequestID)
	in.Reason = strings.TrimSpace(in.Reason)
	in.PatrimonyEvidence = strings.TrimSpace(in.PatrimonyEvidence)
	in.ReceiptEvidence = strings.TrimSpace(in.ReceiptEvidence)
	if err != nil || in.WorkspaceID == "" || in.InvestmentID == "" || in.WithdrawalID == "" || in.AccountID == "" || in.DestinationAccountID == "" || in.CreatedBy == "" || in.ExpectedVersion < 0 || in.ExpectedVersion == math.MaxInt64 || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 500 || utf8.RuneCountInString(in.PatrimonyEvidence) > 2000 || utf8.RuneCountInString(in.ReceiptEvidence) > 2000 || in.NetCents < 0 {
		return zero, ErrInvestmentValidation
	}
	date, err := civildate.ParseCivilDateInput(in.EconomicDate)
	if err != nil {
		return zero, ErrInvestmentValidation
	}
	in.EconomicDate = date.String()
	in.RequestID = key.String()
	for _, v := range []*int64{in.GrossCents, in.IRCents, in.IOFCents, in.FeesCents, in.PatrimonyCents} {
		if v != nil && *v < 0 {
			return zero, ErrInvestmentValidation
		}
	}
	if in.PatrimonyCents != nil && in.PatrimonyEvidence == "" {
		return zero, ErrInvestmentValidation
	}
	// Subtract each nonnegative deduction from a bounded remainder. Neither
	// summing taxes nor computing net-principal can overflow int64.
	complete := in.GrossCents != nil && in.IRCents != nil && in.IOFCents != nil && in.FeesCents != nil
	if in.GrossCents != nil {
		remaining := *in.GrossCents
		for _, v := range []*int64{in.IRCents, in.IOFCents, in.FeesCents} {
			if v != nil {
				if *v > remaining {
					return zero, ErrInvestmentValidation
				}
				remaining -= *v
			}
		}
		if in.NetCents > remaining || (complete && in.NetCents != remaining) {
			return zero, ErrInvestmentValidation
		}
	}
	in.Receipts = append([]InvestmentSettlementReceipt(nil), in.Receipts...)
	sort.Slice(in.Receipts, func(i, j int) bool { return in.Receipts[i].TransactionID < in.Receipts[j].TransactionID })
	var receiptTotal int64
	for i := range in.Receipts {
		r := &in.Receipts[i]
		r.Evidence = strings.TrimSpace(r.Evidence)
		if r.TransactionID == "" || r.AmountCents <= 0 || r.Evidence == "" || utf8.RuneCountInString(r.Evidence) > 2000 || (i > 0 && r.TransactionID == in.Receipts[i-1].TransactionID) || receiptTotal > math.MaxInt64-r.AmountCents {
			return zero, ErrInvestmentValidation
		}
		receiptTotal += r.AmountCents
	}
	if receiptTotal > in.NetCents {
		return zero, ErrInvestmentValidation
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
	// Reserve the SQLite writer even if a deferred pool was supplied by mistake.
	// No rows are touched, including audit triggers and all financial tables.
	if _, err = tx.ExecContext(ctx, `UPDATE investment_settlement_revisions SET id=id WHERE 0`); err != nil {
		return zero, err
	}
	var savedPayload, savedResult string
	err = tx.QueryRowContext(ctx, `SELECT payload_json,result_json FROM investment_settlement_revisions WHERE workspace_id=? AND request_id=?`, in.WorkspaceID, in.RequestID).Scan(&savedPayload, &savedResult)
	if err == nil {
		if savedPayload != string(payload) {
			return zero, ErrInvestmentConflict
		}
		if err = json.Unmarshal([]byte(savedResult), &zero); err != nil {
			return zero, err
		}
		return zero, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	var accountType, destinationType string
	var accountArchived, destinationArchived sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT a.type,a.archived_at,d.type,d.archived_at FROM investments i
 JOIN accounts a ON a.workspace_id=i.workspace_id AND a.id=i.account_id
 JOIN accounts d ON d.workspace_id=i.workspace_id AND d.id=?
 WHERE i.workspace_id=? AND i.id=? AND i.account_id=?`, in.DestinationAccountID, in.WorkspaceID, in.InvestmentID, in.AccountID).Scan(&accountType, &accountArchived, &destinationType, &destinationArchived)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, ErrInvestmentNotFound
	}
	if err != nil {
		return zero, err
	}
	if accountType != models.AccountTypeInvestment || destinationType == models.AccountTypeCreditCard || accountArchived.Valid || destinationArchived.Valid {
		return zero, ErrInvestmentInvalidStatus
	}
	w, err := settlementWithdrawal(ctx, tx, in.WorkspaceID, in.InvestmentID, in.WithdrawalID)
	if err != nil {
		return zero, err
	}
	if in.NetCents != w.ReceivedCents || in.EconomicDate < w.WithdrawalDate {
		return zero, ErrInvestmentValidation
	}
	principal, err := settlementPrincipal(w)
	if err != nil {
		return zero, err
	}
	withdrawalJSON, err := json.Marshal(w)
	if err != nil {
		return zero, err
	}
	r := InvestmentSettlement{ID: uuid.NewString(), WorkspaceID: in.WorkspaceID, InvestmentID: in.InvestmentID, WithdrawalID: in.WithdrawalID, Version: in.ExpectedVersion + 1, CreatedAt: time.Now().Unix(), CreatedBy: in.CreatedBy, State: InvestmentSettlementPending, EffectsState: "NOT_ACTIVATED", PrincipalCents: principal, Input: in}
	if principal != nil {
		v := in.NetCents - *principal
		r.RealizedResultCents = &v
	}
	var previous InvestmentSettlement
	var previousWithdrawal string
	err = tx.QueryRowContext(ctx, `SELECT result_json,withdrawal_json FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID).Scan(&savedResult, &previousWithdrawal)
	if err == nil {
		if err = json.Unmarshal([]byte(savedResult), &previous); err != nil {
			return zero, err
		}
		if previous.State == InvestmentSettlementReconciled || previousWithdrawal != string(withdrawalJSON) {
			return zero, ErrInvestmentConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if previous.Version != in.ExpectedVersion {
		return zero, ErrInvestmentConflict
	}
	r.PreviousID = previous.ID
	// Preserve the A/B revision identities as context, without claiming they prove
	// the removed patrimony or recalculating B. Its immutable chain retains exact
	// position identities, versions and coverage snapshots for C2.
	var reference, financial sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,reference_revision_id FROM investment_financial_revisions WHERE workspace_id=? AND investment_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID).Scan(&financial, &reference)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if !reference.Valid {
		err = tx.QueryRowContext(ctx, `SELECT id FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=? ORDER BY version DESC LIMIT 1`, in.WorkspaceID, in.InvestmentID).Scan(&reference)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return zero, err
		}
	}
	r.ReferenceRevisionID, r.FinancialRevisionID = reference.String, financial.String
	claims, err := settlementClaims(ctx, tx, in)
	if err != nil {
		return zero, err
	}
	if complete && in.PatrimonyCents != nil {
		r.State = InvestmentSettlementRecorded
	}
	if in.ConfirmReceipt {
		if r.State != InvestmentSettlementRecorded || in.ReceiptEvidence == "" || (in.DestinationAccountID != in.AccountID && receiptTotal != in.NetCents) {
			return zero, ErrInvestmentSettlementPending
		}
		// Same-account retention requires explicit human receipt evidence. A maturity
		// date or administrative RESGATADA status is never used as evidence.
		r.State = InvestmentSettlementReconciled
	}
	resultJSON, err := json.Marshal(r)
	if err != nil {
		return zero, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_settlement_revisions
 (workspace_id,investment_id,withdrawal_id,id,previous_id,version,account_id,destination_account_id,economic_date,gross_cents,ir_cents,iof_cents,fees_cents,net_cents,principal_cents,result_cents,patrimony_cents,patrimony_evidence,receipt_evidence,state,reference_revision_id,financial_revision_id,request_id,created_by,created_at,reason,payload_json,result_json,withdrawal_json)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID, r.ID, settlementNullable(r.PreviousID), r.Version, in.AccountID, in.DestinationAccountID, in.EconomicDate, in.GrossCents, in.IRCents, in.IOFCents, in.FeesCents, in.NetCents, principal, r.RealizedResultCents, in.PatrimonyCents, in.PatrimonyEvidence, in.ReceiptEvidence, r.State, reference, financial, in.RequestID, in.CreatedBy, r.CreatedAt, in.Reason, string(payload), string(resultJSON), string(withdrawalJSON))
	if err != nil {
		return zero, err
	}
	for _, c := range claims {
		if c.Existing {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_settlement_receipts(workspace_id,investment_id,withdrawal_id,revision_id,transaction_id,source_account_id,destination_account_id,amount_cents,evidence,snapshot_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID, r.ID, c.TransactionID, settlementNullable(c.Source), c.Destination, c.AmountCents, c.Evidence, c.Snapshot)
		if err != nil {
			return zero, err
		}
	}
	if err = tx.Commit(); err != nil {
		return zero, &InvestmentCommitError{err: err}
	}
	return r, nil
}

func settlementNullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func settlementWithdrawal(ctx context.Context, tx *sql.Tx, workspace, investment, id string) (models.InvestmentWithdrawal, error) {
	history, err := readInvestmentWithdrawals(ctx, tx, workspace, investment)
	if err != nil {
		return models.InvestmentWithdrawal{}, err
	}
	for _, w := range history {
		if w.ID == id {
			return w, nil
		}
	}
	return models.InvestmentWithdrawal{}, ErrInvestmentNotFound
}

func settlementPrincipal(w models.InvestmentWithdrawal) (*int64, error) {
	if len(w.Allocations) == 0 {
		return nil, ErrInvestmentValidation
	}
	var net, principal int64
	known := true
	for _, a := range w.Allocations {
		if a.ReceivedCents < 0 || net > math.MaxInt64-a.ReceivedCents {
			return nil, ErrInvestmentValidation
		}
		net += a.ReceivedCents
		if a.PrincipalCents == nil {
			known = false
			continue
		}
		if *a.PrincipalCents < 0 || principal > math.MaxInt64-*a.PrincipalCents {
			return nil, ErrInvestmentValidation
		}
		principal += *a.PrincipalCents
	}
	if net != w.ReceivedCents {
		return nil, ErrInvestmentValidation
	}
	if !known {
		return nil, nil
	}
	return &principal, nil
}

func readSettlementReceipt(ctx context.Context, tx *sql.Tx, workspace, id string) (settlementReceiptEvidence, string, error) {
	var e settlementReceiptEvidence
	var destination sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT t.id,t.account_id,t.destination_account_id,t.type,t.status,t.amount,COALESCE(t.paid_at,t.date),
 CASE WHEN t.paid_at IS NOT NULL THEN COALESCE(NULLIF(t.paid_date_civil,''),strftime('%Y-%m-%d',t.paid_at,'unixepoch')) ELSE COALESCE(NULLIF(t.date_civil,''),strftime('%Y-%m-%d',t.date,'unixepoch')) END
 FROM financial_transactions t JOIN accounts a ON a.workspace_id=t.workspace_id AND a.id=t.account_id
 WHERE t.workspace_id=? AND t.id=? AND a.type!='CREDIT_CARD' AND a.archived_at IS NULL
 AND (t.type!='TRANSFER' OR EXISTS(SELECT 1 FROM accounts d WHERE d.workspace_id=t.workspace_id AND d.id=t.destination_account_id AND d.type!='CREDIT_CARD' AND d.archived_at IS NULL))`, workspace, id).Scan(&e.ID, &e.Source, &destination, &e.Type, &e.Status, &e.Amount, &e.EffectiveAt, &e.EconomicDate)
	if errors.Is(err, sql.ErrNoRows) {
		return e, "", ErrInvestmentNotFound
	}
	if err != nil {
		return e, "", err
	}
	switch e.Type {
	case "TRANSFER":
		if !destination.Valid || destination.String == e.Source {
			return e, "", ErrInvestmentValidation
		}
		e.Destination = destination.String
	case "INCOME":
		if destination.Valid && destination.String != "" {
			return e, "", ErrInvestmentValidation
		}
		e.Destination = e.Source
		e.Source = ""
	default:
		return e, "", ErrInvestmentValidation
	}
	if e.Status != "paid" || e.Amount <= 0 {
		return e, "", ErrInvestmentValidation
	}
	if _, err = civildate.ParseCivilDateInput(e.EconomicDate); err != nil || e.EconomicDate != time.Unix(e.EffectiveAt, 0).UTC().Format("2006-01-02") {
		return e, "", ErrInvestmentValidation
	}
	b, err := json.Marshal(e)
	return e, string(b), err
}

func settlementClaims(ctx context.Context, tx *sql.Tx, in RecordInvestmentSettlementInput) ([]settlementClaim, error) {
	// All previous claims must be carried forward verbatim. An audit correction
	// cannot silently release a receipt for a different economic withdrawal.
	rows, err := tx.QueryContext(ctx, `SELECT transaction_id,amount_cents,evidence,snapshot_json FROM investment_settlement_receipts WHERE workspace_id=? AND investment_id=? AND withdrawal_id=?`, in.WorkspaceID, in.InvestmentID, in.WithdrawalID)
	if err != nil {
		return nil, err
	}
	old := map[string]settlementClaim{}
	for rows.Next() {
		var c settlementClaim
		if err = rows.Scan(&c.TransactionID, &c.AmountCents, &c.Evidence, &c.Snapshot); err != nil {
			rows.Close()
			return nil, err
		}
		old[c.TransactionID] = c
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var claims []settlementClaim
	for _, r := range in.Receipts {
		e, snapshot, err := readSettlementReceipt(ctx, tx, in.WorkspaceID, r.TransactionID)
		if err != nil {
			return nil, err
		}
		if e.Destination != in.DestinationAccountID || (e.Type == "TRANSFER" && e.Source != in.AccountID) || e.EconomicDate < in.EconomicDate {
			return nil, ErrInvestmentValidation
		}
		c := settlementClaim{InvestmentSettlementReceipt: r, Source: e.Source, Destination: e.Destination, Snapshot: snapshot}
		if prior, ok := old[r.TransactionID]; ok {
			if prior.InvestmentSettlementReceipt != r || prior.Snapshot != snapshot {
				return nil, ErrInvestmentConflict
			}
			c.Existing = true
			delete(old, r.TransactionID)
		}
		// Revalidate all reservations sharing this receipt, before trusting capacity.
		rows, err := tx.QueryContext(ctx, `SELECT amount_cents,snapshot_json FROM investment_settlement_receipts WHERE workspace_id=? AND transaction_id=?`, in.WorkspaceID, r.TransactionID)
		if err != nil {
			return nil, err
		}
		remaining := e.Amount
		for rows.Next() {
			var amount int64
			var saved string
			if err = rows.Scan(&amount, &saved); err != nil {
				rows.Close()
				return nil, err
			}
			if saved != snapshot || amount < 0 || amount > remaining {
				rows.Close()
				return nil, ErrInvestmentConflict
			}
			remaining -= amount
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if !c.Existing && r.AmountCents > remaining {
			return nil, ErrInvestmentConflict
		}
		claims = append(claims, c)
	}
	if len(old) > 0 {
		return nil, ErrInvestmentConflict
	}
	return claims, nil
}

// ReadInvestmentSettlement returns current proof, not just a historical state
// label. Generic transaction editing remains unchanged; drift in a claimed
// source or the original withdrawal fails closed here. C2 must revalidate again
// atomically before any future financial effect.
func ReadInvestmentSettlement(ctx context.Context, db *sql.DB, workspace, investment, withdrawal string) (InvestmentSettlement, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentSettlement{}, err
	}
	defer tx.Rollback()
	r, err := readInvestmentSettlementTx(ctx, tx, workspace, investment, withdrawal)
	if err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func readInvestmentSettlementTx(ctx context.Context, tx *sql.Tx, workspace, investment, withdrawal string) (InvestmentSettlement, error) {
	var r InvestmentSettlement
	var result, withdrawalJSON string
	err := tx.QueryRowContext(ctx, `SELECT result_json,withdrawal_json FROM investment_settlement_revisions WHERE workspace_id=? AND investment_id=? AND withdrawal_id=? ORDER BY version DESC LIMIT 1`, workspace, investment, withdrawal).Scan(&result, &withdrawalJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrInvestmentNotFound
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal([]byte(result), &r); err != nil {
		return InvestmentSettlement{}, err
	}
	w, err := settlementWithdrawal(ctx, tx, workspace, investment, withdrawal)
	if err != nil {
		return InvestmentSettlement{}, err
	}
	encoded, err := json.Marshal(w)
	if err != nil {
		return InvestmentSettlement{}, err
	}
	if string(encoded) != withdrawalJSON {
		return InvestmentSettlement{}, ErrInvestmentConflict
	}
	if _, err = settlementClaims(ctx, tx, r.Input); err != nil {
		return InvestmentSettlement{}, err
	}
	return r, nil
}
