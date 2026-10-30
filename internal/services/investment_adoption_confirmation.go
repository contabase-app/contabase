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
	"math/big"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

const (
	InvestmentAdoptionEvidenceVersion    = 1
	InvestmentAdoptionDeclarationVersion = 1
	InvestmentAdoptionPartial            = "PARTIAL"
	InvestmentAdoptionAllIdentified      = "ALL_IDENTIFIED"
	InvestmentAdoptionAdmissionPending   = "PENDING"
)

var ErrInvestmentAdoptionUnauthorized = errors.New("autorização de investimentos ausente no workspace")
var ErrInvestmentAdoptionAdmissionPending = errors.New("admissão financeira da aplicação adotada pendente")

// Delimitation is a positive, local declaration, never a claim that the external
// economic history is complete. OriginalCostUnknown cannot be repaired here.
type InvestmentAdoptionSelection struct {
	InvestmentID, PositionID, CoverageToken, ReferenceRevisionID string
	ExpectedReferenceVersion                                     int64
	ObservedCents                                                int64
	EconomicIdentity, IdentityBasis                              string
	IncludedInBalance                                            bool
	OriginalCostUnknown                                          bool
}
type InvestmentAdoptionExclusion struct{ InvestmentID, Reason string }
type InvestmentAdoptionDisposition struct{ Kind, ID, Disposition, Reason string }
type InvestmentAdoptionOverlapResolution struct{ FirstInvestmentID, SecondInvestmentID, Basis string }
type InvestmentAdoptionCashDeclaration struct {
	Cents       int64
	Date, Basis string
}

type ConfirmInvestmentAdoptionInput struct {
	WorkspaceID, AccountID, CreatedBy, RequestID, Reason, CutDate string
	ExpectedBalanceVersion, ExpectedAdoptionVersion               int64
	EvidenceToken                                                 string
	EvidenceVersion, DeclarationVersion                           int
	DayClosed                                                     bool
	Coverage                                                      string
	ReviewedInvestmentIDs                                         []string
	Items                                                         []InvestmentAdoptionSelection
	Excluded                                                      []InvestmentAdoptionExclusion
	Dispositions                                                  []InvestmentAdoptionDisposition
	OverlapResolutions                                            []InvestmentAdoptionOverlapResolution
	Cash                                                          *InvestmentAdoptionCashDeclaration
	// Optional independent declaration for D; never populated from current balance.
	DeclaredBalanceAtCutCents *int64
	DeclaredBalanceBasis      string
}

type InvestmentAdoptionConfirmedItem struct {
	InvestmentID, PositionID, ReferenceRevisionID       string
	RepresentedCents                                    int64
	EconomicIdentity, IdentityBasis                     string
	PrincipalEvidence, FundingEvidence, ReceiptEvidence InvestmentAdoptionEvidence
}

type InvestmentAdoptionConfirmation struct {
	ID, WorkspaceID, AccountID, CutDate, CreatedBy, RequestID, Reason string
	Version, CreatedAt, ObservedBalanceCents, ObservedBalanceVersion  int64
	Coverage, IntegrationAdmission                                    string
	FinancialEffectCents                                              int64
	Items                                                             []InvestmentAdoptionConfirmedItem // accumulated; prior references retained
	Cash                                                              InvestmentAdoptionAmount
	DeclaredBalanceAtCutCents                                         *int64
	Declaration                                                       ConfirmInvestmentAdoptionInput
	Diagnostic                                                        InvestmentAdoptionDiagnostic // exact pre-write evidence and limitations
}
type InvestmentAdoptionConfirmationResult struct {
	InvestmentAdoptionConfirmation
	Outcome InvestmentOperationOutcome
}

type InvestmentAdoptionConfirmationCandidate struct {
	Diagnostic      InvestmentAdoptionDiagnostic
	AdoptionVersion int64
	EvidenceVersion int
	EvidenceToken   string
	ConfirmedItems  []InvestmentAdoptionConfirmedItem
	// Complete local account evidence, including events after the cut. No files,
	// attachment content, original DB, remote source or runtime is accessed.
	EvidenceSnapshot json.RawMessage
}

type adoptionEvidenceRows struct {
	Kind    string
	Columns []string
	Rows    [][]any
}

func adoptionDigest(v any) (string, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}

// Each query scopes both its owner and its related tenant-owned rows explicitly.
// All rows are materialized and closed before any dependent query is issued.
func adoptionSourceEvidenceTx(ctx context.Context, tx *sql.Tx, ws, account string) ([]adoptionEvidenceRows, error) {
	projection, err := database.TransactionEvidenceProjection(ctx, tx, "t")
	if err != nil {
		return nil, err
	}
	queries := []struct {
		kind, query string
		args        []any
	}{
		{"accounts", `SELECT a.* FROM accounts a WHERE a.workspace_id=? AND (a.id=? OR EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=? AND ((t.destination_account_id=? AND t.account_id=a.id) OR (t.account_id=? AND t.destination_account_id=a.id)))) ORDER BY a.id`, []any{ws, account, ws, account, account}},
		{"transactions", `SELECT ` + projection + ` FROM transactions t WHERE t.workspace_id=? AND (t.account_id=? OR t.destination_account_id=?) ORDER BY t.id`, []any{ws, account, account}},
		{"categories", `SELECT c.* FROM categories c WHERE c.workspace_id=? AND EXISTS(SELECT 1 FROM transactions t WHERE t.workspace_id=? AND t.account_id=? AND t.category_id=c.id) ORDER BY c.id`, []any{ws, ws, account}},
		{"adjustments", `SELECT t.* FROM account_balance_adjustments t WHERE t.workspace_id=? AND t.account_id=? ORDER BY t.id`, []any{ws, account}},
		{"settlement_prior_effects", `SELECT t.* FROM investment_settlement_prior_effects t WHERE t.workspace_id=? AND t.account_id=? ORDER BY t.rowid`, []any{ws, account}},
		{"investments", `SELECT t.* FROM investments t WHERE t.workspace_id=? AND t.account_id=? ORDER BY t.id`, []any{ws, account}},
	}
	for _, table := range []string{"investment_purchases", "investment_valuations", "investment_withdrawals", "investment_withdrawal_allocations", "investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions", "investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions", "investment_settlement_receipts", "investment_settlement_applications", "investment_result_classifications"} {
		queries = append(queries, struct {
			kind, query string
			args        []any
		}{table, `SELECT t.* FROM ` + table + ` t JOIN investments i ON i.workspace_id=? AND i.workspace_id=t.workspace_id AND i.id=t.investment_id WHERE t.workspace_id=? AND i.account_id=? ORDER BY t.rowid`, []any{ws, ws, account}})
	}
	var out []adoptionEvidenceRows
	for _, q := range queries {
		rows, err := tx.QueryContext(ctx, q.query, q.args...)
		if err != nil {
			return nil, err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		group := adoptionEvidenceRows{Kind: q.kind, Columns: columns}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for n := range values {
				dest[n] = &values[n]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				return nil, err
			}
			for n, v := range values {
				if b, ok := v.([]byte); ok {
					values[n] = string(b)
				}
			}
			group.Rows = append(group.Rows, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, group)
	}
	return out, nil
}

func readAdoptionItemsTx(ctx context.Context, tx *sql.Tx, ws, account string) ([]InvestmentAdoptionConfirmedItem, error) {
	rows, err := tx.QueryContext(ctx, `SELECT investment_id,position_id,reference_revision_id,represented_cents,economic_identity,identity_basis FROM investment_adoption_items WHERE workspace_id=? AND account_id=? ORDER BY investment_id`, ws, account)
	if err != nil {
		return nil, err
	}
	var items []InvestmentAdoptionConfirmedItem
	for rows.Next() {
		var i InvestmentAdoptionConfirmedItem
		if err = rows.Scan(&i.InvestmentID, &i.PositionID, &i.ReferenceRevisionID, &i.RepresentedCents, &i.EconomicIdentity, &i.IdentityBasis); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	return items, err
}

func adoptionConfirmationCandidateTx(ctx context.Context, tx *sql.Tx, ws, account, cut string) (InvestmentAdoptionConfirmationCandidate, error) {
	var c InvestmentAdoptionConfirmationCandidate
	d, err := investmentAdoptionDiagnosticTx(ctx, tx, ws, account, cut)
	if err != nil {
		return c, err
	}
	c.Diagnostic = d
	c.EvidenceVersion = InvestmentAdoptionEvidenceVersion
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=?`, ws, account).Scan(&c.AdoptionVersion); err != nil {
		return c, err
	}
	c.ConfirmedItems, err = readAdoptionItemsTx(ctx, tx, ws, account)
	if err != nil {
		return c, err
	}
	sources, err := adoptionSourceEvidenceTx(ctx, tx, ws, account)
	if err != nil {
		return c, err
	}
	_, b, err := adoptionDigest(sources)
	if err != nil {
		return c, err
	}
	c.EvidenceSnapshot = b
	c.EvidenceToken, _, err = adoptionDigest(c)
	return c, err
}

func ReadInvestmentAdoptionConfirmationCandidate(ctx context.Context, db *sql.DB, ws, account, cut string) (InvestmentAdoptionConfirmationCandidate, error) {
	date, err := civildate.ParseCivilDateInput(cut)
	if db == nil || strings.TrimSpace(ws) == "" || strings.TrimSpace(account) == "" || err != nil || date.String() != cut {
		return InvestmentAdoptionConfirmationCandidate{}, ErrInvestmentValidation
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentAdoptionConfirmationCandidate{}, err
	}
	defer tx.Rollback()
	c, err := adoptionConfirmationCandidateTx(ctx, tx, ws, account, cut)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func adoptionAuthorizeTx(ctx context.Context, tx *sql.Tx, ws, user string) error {
	var m models.WorkspaceMember
	err := tx.QueryRowContext(ctx, `SELECT m.role,COALESCE(NULLIF(m.custom_permissions,''),'[]') FROM workspace_members m JOIN users u ON u.id=m.user_id AND COALESCE(u.deleted_at,0)=0 JOIN workspaces w ON w.id=m.workspace_id WHERE m.workspace_id=? AND m.user_id=?`, ws, user).Scan(&m.Role, &m.CustomPermissionsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentAdoptionUnauthorized
	}
	if err != nil {
		return err
	}
	m.CustomPermissions = models.ParsePermissionList(m.CustomPermissionsRaw)
	if !models.HasPermission(&m, models.PermissionInvestmentsWrite) {
		return ErrInvestmentAdoptionUnauthorized
	}
	return nil
}

// New A revisions supersede their predecessor, including equal-value revisions.
// The exact reference pinned by an adoption cannot be superseded in this phase.
func rejectPendingInvestmentAdoptionTx(ctx context.Context, tx *sql.Tx, ws, investment string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_adoption_items i JOIN investment_adoption_confirmations c ON c.workspace_id=? AND c.workspace_id=i.workspace_id AND c.account_id=i.account_id AND c.id=i.confirmation_id WHERE i.workspace_id=? AND i.investment_id=? AND c.integration_admission='PENDING'`, ws, ws, investment).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrInvestmentAdoptionAdmissionPending
	}
	return nil
}

func adoptionText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max
}
func normalizeAdoptionInput(in ConfirmInvestmentAdoptionInput) (ConfirmInvestmentAdoptionInput, error) {
	key, err := uuid.Parse(in.RequestID)
	date, dateErr := civildate.ParseCivilDateInput(in.CutDate)
	in.Reason = strings.TrimSpace(in.Reason)
	if err != nil || dateErr != nil || date.String() != in.CutDate || in.WorkspaceID == "" || in.AccountID == "" || in.CreatedBy == "" || !adoptionText(in.Reason, 500) || in.ExpectedBalanceVersion < 0 || in.ExpectedAdoptionVersion < 0 || in.ExpectedAdoptionVersion == math.MaxInt64 || len(in.EvidenceToken) != 64 || in.EvidenceVersion != InvestmentAdoptionEvidenceVersion || in.DeclarationVersion != InvestmentAdoptionDeclarationVersion {
		return in, ErrInvestmentValidation
	}
	if _, err = hex.DecodeString(in.EvidenceToken); err != nil {
		return in, ErrInvestmentValidation
	}
	in.RequestID = key.String()
	// Roundtrip detaches all pointer/slice inputs before canonicalization.
	b, _ := json.Marshal(in)
	var detached ConfirmInvestmentAdoptionInput
	if err = json.Unmarshal(b, &detached); err != nil {
		return in, err
	}
	in = detached
	for n := range in.Items {
		p := &in.Items[n]
		p.EconomicIdentity = strings.ToLower(strings.Join(strings.Fields(p.EconomicIdentity), " "))
		p.IdentityBasis = strings.TrimSpace(p.IdentityBasis)
	}
	for n := range in.Excluded {
		in.Excluded[n].Reason = strings.TrimSpace(in.Excluded[n].Reason)
	}
	for n := range in.Dispositions {
		in.Dispositions[n].Reason = strings.TrimSpace(in.Dispositions[n].Reason)
	}
	for n := range in.OverlapResolutions {
		p := &in.OverlapResolutions[n]
		if p.FirstInvestmentID > p.SecondInvestmentID {
			p.FirstInvestmentID, p.SecondInvestmentID = p.SecondInvestmentID, p.FirstInvestmentID
		}
		p.Basis = strings.TrimSpace(p.Basis)
	}
	sort.Strings(in.ReviewedInvestmentIDs)
	sort.Slice(in.Items, func(i, j int) bool { return in.Items[i].InvestmentID < in.Items[j].InvestmentID })
	sort.Slice(in.Excluded, func(i, j int) bool { return in.Excluded[i].InvestmentID < in.Excluded[j].InvestmentID })
	sort.Slice(in.Dispositions, func(i, j int) bool {
		a, b := in.Dispositions[i], in.Dispositions[j]
		return a.Kind+":"+a.ID < b.Kind+":"+b.ID
	})
	sort.Slice(in.OverlapResolutions, func(i, j int) bool {
		a, b := in.OverlapResolutions[i], in.OverlapResolutions[j]
		return a.FirstInvestmentID+":"+a.SecondInvestmentID < b.FirstInvestmentID+":"+b.SecondInvestmentID
	})
	if in.Cash != nil {
		in.Cash.Basis = strings.TrimSpace(in.Cash.Basis)
	}
	if len(in.Excluded) == 0 {
		in.Excluded = nil
	}
	if len(in.Dispositions) == 0 {
		in.Dispositions = nil
	}
	if len(in.OverlapResolutions) == 0 {
		in.OverlapResolutions = nil
	}
	in.DeclaredBalanceBasis = strings.TrimSpace(in.DeclaredBalanceBasis)
	return in, nil
}

// ConfirmInvestmentAdoption reserves one SQLite writer before any decision.
// Domain rejections use a savepoint: coordinated writes roll back before their
// narrow audit event. Infrastructure and uncertain commits roll back in full.
func ConfirmInvestmentAdoption(ctx context.Context, db *sql.DB, input ConfirmInvestmentAdoptionInput) (InvestmentAdoptionConfirmationResult, error) {
	var empty InvestmentAdoptionConfirmationResult
	in, err := normalizeAdoptionInput(input)
	if err != nil {
		return empty, err
	}
	if db == nil {
		return empty, ErrInvestmentValidation
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
	// Reserves even a deferred pool; WHERE 0 changes no rows, including triggers.
	if _, err = tx.ExecContext(ctx, `UPDATE investment_adoption_confirmations SET version=version WHERE workspace_id=? AND account_id=? AND 0`, in.WorkspaceID, in.AccountID); err != nil {
		return empty, err
	}
	if err = adoptionAuthorizeTx(ctx, tx, in.WorkspaceID, in.CreatedBy); err != nil {
		return empty, err
	}
	var accountType string
	var archived sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT type,archived_at FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, in.AccountID).Scan(&accountType, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrInvestmentNotFound
	}
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT adoption_request`); err != nil {
		return empty, err
	}
	result, domainErr := confirmInvestmentAdoptionTx(ctx, tx, in, payload, accountType, archived.Valid)
	if domainErr != nil {
		code := ""
		switch {
		case errors.Is(domainErr, ErrInvestmentConflict):
			code = "CONFLICT"
		case errors.Is(domainErr, ErrInvestmentValidation), errors.Is(domainErr, ErrBalanceOutOfRange):
			code = "VALIDATION"
		case errors.Is(domainErr, ErrInvestmentNotFound):
			code = "NOT_FOUND"
		case errors.Is(domainErr, ErrInvestmentInvalidStatus), errors.Is(domainErr, ErrInvestmentInvalidAccount):
			code = "INVALID_STATUS"
		case errors.Is(domainErr, ErrInvestmentAdoptionAdmissionPending):
			code = "ADMISSION_PENDING"
		}
		if code == "" {
			return empty, domainErr
		}
		if _, err = tx.ExecContext(ctx, `ROLLBACK TO adoption_request`); err != nil {
			return empty, err
		}
		var conflict any
		var conflictID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=? AND request_id=?`, in.WorkspaceID, in.AccountID, in.RequestID).Scan(&conflictID)
		if err == nil {
			conflict = conflictID
		} else if !errors.Is(err, sql.ErrNoRows) {
			return empty, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_adoption_attempts(workspace_id,account_id,id,request_id,created_by,created_at,reason,code,payload_json,conflicting_confirmation_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.AccountID, uuid.NewString(), in.RequestID, in.CreatedBy, time.Now().Unix(), in.Reason, code, string(payload), conflict)
		if err != nil {
			return empty, err
		}
	}
	if err = tx.Commit(); err != nil {
		return empty, &InvestmentCommitError{err: err}
	}
	if domainErr != nil {
		return empty, domainErr
	}
	return result, nil
}

func confirmInvestmentAdoptionTx(ctx context.Context, tx *sql.Tx, in ConfirmInvestmentAdoptionInput, payload []byte, accountType string, archived bool) (InvestmentAdoptionConfirmationResult, error) {
	var empty InvestmentAdoptionConfirmationResult
	var savedPayload, savedID string
	err := tx.QueryRowContext(ctx, `SELECT payload_json,id FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=? AND request_id=?`, in.WorkspaceID, in.AccountID, in.RequestID).Scan(&savedPayload, &savedID)
	if err == nil {
		if savedPayload != string(payload) {
			return empty, ErrInvestmentConflict
		}
		result, err := readValidatedAdoptionReceiptTx(ctx, tx, in.WorkspaceID, in.AccountID, savedID)
		if err != nil {
			return empty, err
		}
		return InvestmentAdoptionConfirmationResult{result, InvestmentOperationReplayed}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	if accountType != models.AccountTypeInvestment {
		return empty, ErrInvestmentInvalidAccount
	}
	if archived {
		return empty, ErrInvestmentInvalidStatus
	}
	c, err := adoptionConfirmationCandidateTx(ctx, tx, in.WorkspaceID, in.AccountID, in.CutDate)
	if err != nil {
		return empty, err
	}
	if c.AdoptionVersion != in.ExpectedAdoptionVersion || c.Diagnostic.BalanceVersion != in.ExpectedBalanceVersion || c.EvidenceToken != in.EvidenceToken {
		return empty, ErrInvestmentConflict
	}
	if err = validateAdoptionDeclaration(in, c); err != nil {
		return empty, err
	}
	var previous string
	if c.AdoptionVersion > 0 {
		var cut string
		if err = tx.QueryRowContext(ctx, `SELECT id,cut_date FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=? AND version=?`, in.WorkspaceID, in.AccountID, c.AdoptionVersion).Scan(&previous, &cut); err != nil {
			return empty, err
		}
		if cut != in.CutDate {
			return empty, ErrInvestmentConflict
		}
	}
	// Prior components remain pinned and must still validate before a complement.
	for _, item := range c.ConfirmedItems {
		r, e := readInvestmentReferenceRevisionTx(ctx, tx, in.WorkspaceID, item.InvestmentID, item.ReferenceRevisionID)
		if e != nil {
			return empty, e
		}
		if r.State != models.InvestmentReferenceValid || r.RepresentedCents == nil || *r.RepresentedCents != item.RepresentedCents || r.Candidate.Snapshot.Position.ValuationDate != in.CutDate {
			return empty, ErrInvestmentConflict
		}
	}
	result := InvestmentAdoptionConfirmation{ID: uuid.NewString(), WorkspaceID: in.WorkspaceID, AccountID: in.AccountID, CutDate: in.CutDate, CreatedBy: in.CreatedBy, RequestID: in.RequestID, Reason: in.Reason, Version: c.AdoptionVersion + 1, CreatedAt: time.Now().Unix(), ObservedBalanceCents: c.Diagnostic.CurrentBalanceCents, ObservedBalanceVersion: c.Diagnostic.BalanceVersion, Coverage: in.Coverage, IntegrationAdmission: InvestmentAdoptionAdmissionPending, Declaration: in, Diagnostic: c.Diagnostic, DeclaredBalanceAtCutCents: in.DeclaredBalanceAtCutCents}
	result.Cash.Evidence = adoptionEvidence(InvestmentEvidenceUnknown, "No broker cash declared or proven")
	if in.Cash != nil {
		v := in.Cash.Cents
		result.Cash.Cents = &v
		result.Cash.Evidence = adoptionEvidence(InvestmentEvidenceDeclared, in.Cash.Basis)
	}
	result.Items = append(result.Items, c.ConfirmedItems...)
	var newItems []InvestmentAdoptionConfirmedItem
	for _, selection := range in.Items {
		var item *InvestmentAdoptionItem
		for n := range c.Diagnostic.Investments {
			if c.Diagnostic.Investments[n].ID == selection.InvestmentID {
				item = &c.Diagnostic.Investments[n]
				break
			}
		}
		if item == nil {
			return empty, ErrInvestmentNotFound
		}
		ref, err := confirmAdoptionReferenceTx(ctx, tx, in, selection, *item)
		if err != nil {
			return empty, err
		}
		confirmed := InvestmentAdoptionConfirmedItem{InvestmentID: selection.InvestmentID, PositionID: selection.PositionID, ReferenceRevisionID: ref.ID, RepresentedCents: selection.ObservedCents, EconomicIdentity: selection.EconomicIdentity, IdentityBasis: selection.IdentityBasis, PrincipalEvidence: item.PrincipalEvidence, FundingEvidence: item.FundingEvidence, ReceiptEvidence: item.ReceiptEvidence}
		newItems = append(newItems, confirmed)
		result.Items = append(result.Items, confirmed)
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].InvestmentID < result.Items[j].InvestmentID })
	// Preserve dimensional evidence of earlier components from their own receipts.
	for n := range result.Items {
		if result.Items[n].PrincipalEvidence.State != "" {
			continue
		}
		var originalID string
		if err = tx.QueryRowContext(ctx, `SELECT c.id FROM investment_adoption_confirmations c JOIN investment_adoption_items i ON i.workspace_id=? AND i.workspace_id=c.workspace_id AND i.account_id=c.account_id AND i.confirmation_id=c.id WHERE c.workspace_id=? AND c.account_id=? AND i.investment_id=?`, in.WorkspaceID, in.WorkspaceID, in.AccountID, result.Items[n].InvestmentID).Scan(&originalID); err != nil {
			return empty, err
		}
		original, err := readValidatedAdoptionReceiptTx(ctx, tx, in.WorkspaceID, in.AccountID, originalID)
		if err != nil {
			return empty, err
		}
		for _, i := range original.Items {
			if i.InvestmentID == result.Items[n].InvestmentID {
				result.Items[n] = i
				break
			}
		}
	}
	sources, err := adoptionSourceEvidenceTx(ctx, tx, in.WorkspaceID, in.AccountID)
	if err != nil {
		return empty, err
	}
	sourceToken, _, err := adoptionDigest(sources)
	if err != nil {
		return empty, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return empty, err
	}
	// Return the persisted public representation, detached from diagnostic caches
	// and mutable evidence still held by the transaction's candidate.
	var receipt InvestmentAdoptionConfirmation
	if err = json.Unmarshal(resultJSON, &receipt); err != nil {
		return empty, err
	}
	evidenceJSON, err := json.Marshal(c)
	if err != nil {
		return empty, err
	}
	var previousID any
	if previous != "" {
		previousID = previous
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_adoption_confirmations(workspace_id,account_id,id,version,previous_id,previous_version,cut_date,created_by,created_at,request_id,reason,evidence_version,declaration_version,evidence_token,source_token,payload_json,evidence_json,result_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.AccountID, result.ID, result.Version, previousID, c.AdoptionVersion, in.CutDate, in.CreatedBy, result.CreatedAt, in.RequestID, in.Reason, in.EvidenceVersion, in.DeclarationVersion, in.EvidenceToken, sourceToken, string(payload), string(evidenceJSON), string(resultJSON))
	if err != nil {
		return empty, err
	}
	for _, i := range newItems {
		_, err = tx.ExecContext(ctx, `INSERT INTO investment_adoption_items(workspace_id,account_id,confirmation_id,investment_id,position_id,reference_revision_id,represented_cents,economic_identity,identity_basis) VALUES(?,?,?,?,?,?,?,?,?)`, in.WorkspaceID, in.AccountID, result.ID, i.InvestmentID, i.PositionID, i.ReferenceRevisionID, i.RepresentedCents, i.EconomicIdentity, i.IdentityBasis)
		if err != nil {
			return empty, err
		}
	}
	return InvestmentAdoptionConfirmationResult{receipt, InvestmentOperationCreated}, nil
}

func validateAdoptionDeclaration(in ConfirmInvestmentAdoptionInput, c InvestmentAdoptionConfirmationCandidate) error {
	d := c.Diagnostic
	if !in.DayClosed || len(in.Items) == 0 || (in.Coverage != InvestmentAdoptionPartial && in.Coverage != InvestmentAdoptionAllIdentified) {
		return ErrInvestmentValidation
	}
	if d.CurrentBalanceCents < 0 {
		return ErrInvestmentConflict
	}
	inventory := map[string]InvestmentAdoptionItem{}
	for _, i := range d.Investments {
		inventory[i.ID] = i
	}
	if len(in.ReviewedInvestmentIDs) != len(inventory) {
		return ErrInvestmentConflict
	}
	for n, id := range in.ReviewedInvestmentIDs {
		if _, ok := inventory[id]; !ok {
			return ErrInvestmentNotFound
		}
		if n > 0 && id == in.ReviewedInvestmentIDs[n-1] {
			return ErrInvestmentValidation
		}
	}
	selected, identities, prior := map[string]bool{}, map[string]bool{}, map[string]bool{}
	sum := new(big.Int)
	for _, i := range c.ConfirmedItems {
		if _, ok := inventory[i.InvestmentID]; !ok {
			return ErrInvestmentConflict
		}
		prior[i.InvestmentID] = true
		identities[i.EconomicIdentity] = true
		sum.Add(sum, big.NewInt(i.RepresentedCents))
	}
	for _, s := range in.Items {
		item, ok := inventory[s.InvestmentID]
		if !ok {
			return ErrInvestmentNotFound
		}
		if selected[s.InvestmentID] || prior[s.InvestmentID] || identities[s.EconomicIdentity] {
			return ErrInvestmentConflict
		}
		if !s.IncludedInBalance || s.ObservedCents < 0 || !adoptionText(s.EconomicIdentity, 200) || !adoptionText(s.IdentityBasis, 500) || s.ExpectedReferenceVersion < 0 || s.ExpectedReferenceVersion == math.MaxInt64 || len(s.CoverageToken) != 64 {
			return ErrInvestmentValidation
		}
		if s.OriginalCostUnknown || adoptionUnsupportedCost(item.RegisteredPrincipalCents) {
			return ErrInvestmentValidation
		}
		if item.ArchivedAt != nil || item.CurrentStatus != models.InvestmentStatusAtiva {
			return ErrInvestmentInvalidStatus
		}
		if item.Position == nil || item.Position.ID != s.PositionID || item.Position.ValuationDate != in.CutDate || item.Position.AmountCents != s.ObservedCents || !adoptionPositionKnownAtCut(*item.Position, in.CutDate) {
			return ErrInvestmentConflict
		}
		if item.FinancialVersion != 0 {
			return ErrInvestmentConflict
		}
		for _, w := range item.Withdrawals {
			if w.ApplicationID != "" || w.ReceiptEvidence.State == InvestmentEvidenceConflicting {
				return ErrInvestmentConflict
			}
		}
		r := item.Reference
		if r == nil {
			if s.ExpectedReferenceVersion != 0 || s.ReferenceRevisionID != "" {
				return ErrInvestmentConflict
			}
		} else {
			if r.ID != s.ReferenceRevisionID || r.Version != s.ExpectedReferenceVersion || r.Candidate.CoverageToken != s.CoverageToken || r.Candidate.Snapshot.Position.ID != s.PositionID {
				return ErrInvestmentConflict
			}
			if r.State != models.InvestmentReferenceValid && r.State != models.InvestmentReferencePending {
				return ErrInvestmentConflict
			}
			if r.Confirmed && (r.RepresentedCents == nil || *r.RepresentedCents != s.ObservedCents) {
				return ErrInvestmentConflict
			}
		}
		selected[s.InvestmentID] = true
		identities[s.EconomicIdentity] = true
		sum.Add(sum, big.NewInt(s.ObservedCents))
	}
	excluded := map[string]bool{}
	for _, x := range in.Excluded {
		item, ok := inventory[x.InvestmentID]
		if !ok {
			return ErrInvestmentNotFound
		}
		if excluded[x.InvestmentID] || selected[x.InvestmentID] || prior[x.InvestmentID] || !adoptionText(x.Reason, 500) {
			return ErrInvestmentValidation
		}
		if in.Coverage == InvestmentAdoptionAllIdentified && item.AppliedDate <= in.CutDate {
			return ErrInvestmentConflict
		}
		excluded[x.InvestmentID] = true
	}
	if len(selected)+len(prior)+len(excluded) != len(inventory) {
		return ErrInvestmentConflict
	}
	if in.Cash != nil {
		if in.Cash.Cents < 0 || in.Cash.Date != in.CutDate || !adoptionText(in.Cash.Basis, 500) {
			return ErrInvestmentValidation
		}
		sum.Add(sum, big.NewInt(in.Cash.Cents))
	}
	if !sum.IsInt64() {
		return ErrBalanceOutOfRange
	}
	if in.DeclaredBalanceAtCutCents != nil {
		if *in.DeclaredBalanceAtCutCents < 0 || !adoptionText(in.DeclaredBalanceBasis, 500) {
			return ErrInvestmentValidation
		}
		cmp := sum.Cmp(big.NewInt(*in.DeclaredBalanceAtCutCents))
		if cmp > 0 || (in.Coverage == InvestmentAdoptionAllIdentified && cmp != 0) {
			return ErrInvestmentConflict
		}
	} else if in.DeclaredBalanceBasis != "" {
		return ErrInvestmentValidation
	}
	// Diagnostic status alone cannot decide the limited declaration. Global
	// ownership/funding contradictions remain blockers; unknown provenance does not.
	for _, issue := range d.Issues {
		if issue.Code == "ARCHIVED_ACCOUNT" || issue.Code == "INVALID_TRANSFER_OWNERSHIP" || (issue.Code == "UNVERIFIED_FUNDING" && issue.Blocking) {
			return ErrInvestmentConflict
		}
	}
	if err := validateAdoptionHistory(in, d); err != nil {
		return err
	}
	resolutions := map[string]bool{}
	pair := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "\x00" + b
	}
	for _, r := range in.OverlapResolutions {
		if _, ok := inventory[r.FirstInvestmentID]; !ok {
			return ErrInvestmentNotFound
		}
		if _, ok := inventory[r.SecondInvestmentID]; !ok {
			return ErrInvestmentNotFound
		}
		key := pair(r.FirstInvestmentID, r.SecondInvestmentID)
		if r.FirstInvestmentID == r.SecondInvestmentID || resolutions[key] || !adoptionText(r.Basis, 500) {
			return ErrInvestmentValidation
		}
		resolutions[key] = true
	}
	// A note is a candidate, never an economic identity in itself. Each affected
	// pair needs its own positive delimitation, including excluded/prior components.
	notes := map[string][]string{}
	for _, item := range d.Investments {
		for _, p := range item.Purchases {
			if p.NoteReference != "" {
				notes[p.NoteReference] = append(notes[p.NoteReference], item.ID)
			}
		}
	}
	for _, ids := range notes {
		for _, a := range ids {
			for _, b := range ids {
				if a != b && (selected[a] || selected[b]) && !resolutions[pair(a, b)] {
					return ErrInvestmentConflict
				}
			}
		}
	}
	return nil
}

func validateAdoptionHistory(in ConfirmInvestmentAdoptionInput, d InvestmentAdoptionDiagnostic) error {
	required := map[string]map[string]bool{}
	add := func(kind, id string, allowed ...string) {
		set := map[string]bool{}
		for _, v := range allowed {
			set[v] = true
		}
		required[kind+":"+id] = set
	}
	for _, i := range d.IncomeCandidates {
		if i.Status == "paid" {
			add("INCOME", i.ID, "REVIEWED_UNATTRIBUTED", "DECLARED_ALREADY_REFLECTED")
		} else {
			add("INCOME", i.ID, "CANDIDATE_PENDING")
		}
	}
	for _, a := range d.Adjustments {
		if a.EconomicDate > in.CutDate {
			add("ADJUSTMENT", a.ID, "OUTSIDE_CUT_PENDING")
		} else {
			add("ADJUSTMENT", a.ID, "REVIEWED_UNATTRIBUTED", "DECLARED_ALREADY_REFLECTED")
		}
	}
	for _, e := range d.EventsAfterCut {
		if e.Kind == "INCOME" {
			add(e.Kind, e.ID, "OUTSIDE_CUT_PENDING")
		}
	}
	for _, i := range d.Investments {
		for _, w := range i.Withdrawals {
			add("WITHDRAWAL", w.Withdrawal.ID, "REMAINING_POSITION_ONLY")
		}
		for _, e := range i.EventsAfterCut {
			if e.Kind == "WITHDRAWAL" {
				add(e.Kind, e.ID, "OUTSIDE_CUT_PENDING")
			}
		}
	}
	for _, p := range in.Dispositions {
		key := p.Kind + ":" + p.ID
		allowed, ok := required[key]
		if !ok || !allowed[p.Disposition] || !adoptionText(p.Reason, 500) {
			return ErrInvestmentValidation
		}
		delete(required, key)
	}
	if len(required) != 0 {
		return ErrInvestmentConflict
	}
	return nil
}

func confirmAdoptionReferenceTx(ctx context.Context, tx *sql.Tx, in ConfirmInvestmentAdoptionInput, s InvestmentAdoptionSelection, item InvestmentAdoptionItem) (models.InvestmentReferenceRevision, error) {
	var empty models.InvestmentReferenceRevision
	candidate, err := investmentReferenceCandidateTx(ctx, tx, in.WorkspaceID, s.InvestmentID, s.PositionID)
	if err != nil {
		return empty, err
	}
	if !adoptionCandidateOwned(candidate, in.WorkspaceID, in.AccountID, s.InvestmentID) || candidate.CoverageToken != s.CoverageToken || candidate.Snapshot.Position.ValuationDate != in.CutDate || candidate.Snapshot.Position.AmountCents != s.ObservedCents {
		return empty, ErrInvestmentConflict
	}
	// The position is at D and the declaration closes D, covering every structurally
	// valid operation through D. This resolves same-day ordering only by declaration.
	if candidate.HasSameDayOperations && !in.DayClosed {
		return empty, ErrInvestmentValidation
	}
	if item.Reference != nil && item.Reference.State == models.InvestmentReferenceValid && item.Reference.Confirmed {
		return *item.Reference, nil
	}
	request, _ := uuid.Parse(in.RequestID)
	a := RecordInvestmentReferenceInput{WorkspaceID: in.WorkspaceID, InvestmentID: s.InvestmentID, AccountID: in.AccountID, PositionID: s.PositionID, CreatedBy: in.CreatedBy, RequestID: uuid.NewSHA1(request, []byte("O2B.2-A:"+s.InvestmentID)).String(), Reason: in.Reason, CoverageToken: s.CoverageToken, ExpectedVersion: s.ExpectedReferenceVersion, RepresentedCents: &s.ObservedCents, Confirm: true, DayClosed: in.DayClosed}
	payload, err := json.Marshal(a)
	if err != nil {
		return empty, err
	}
	r, err := recordInvestmentReferenceTx(ctx, tx, a, payload)
	if err != nil {
		return empty, err
	}
	if r.State != models.InvestmentReferenceValid {
		return empty, ErrInvestmentConflict
	}
	return r.InvestmentReferenceRevision, nil
}

// readValidatedAdoptionReceiptTx checks durable evidence only. In particular it
// must not call current reference/position validators: drift belongs to Validity.
func readValidatedAdoptionReceiptTx(ctx context.Context, tx *sql.Tx, ws, account, id string) (InvestmentAdoptionConfirmation, error) {
	var empty, header InvestmentAdoptionConfirmation
	var payload, evidence, saved, token string
	var evidenceVersion, declarationVersion int
	err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,account_id,version,cut_date,created_by,created_at,request_id,reason,integration_admission,financial_effect_cents,evidence_version,declaration_version,evidence_token,payload_json,evidence_json,result_json
 FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=? AND id=?`, ws, account, id).Scan(&header.ID, &header.WorkspaceID, &header.AccountID, &header.Version, &header.CutDate, &header.CreatedBy, &header.CreatedAt, &header.RequestID, &header.Reason, &header.IntegrationAdmission, &header.FinancialEffectCents, &evidenceVersion, &declarationVersion, &token, &payload, &evidence, &saved)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrInvestmentNotFound
	}
	if err != nil {
		return empty, err
	}
	invalid := func() (InvestmentAdoptionConfirmation, error) {
		// Corruption is not a domain refusal: replay must not write a journal entry.
		return empty, fmt.Errorf("invalid persisted investment adoption receipt")
	}
	var receipt InvestmentAdoptionConfirmation
	var declaration ConfirmInvestmentAdoptionInput
	var candidate InvestmentAdoptionConfirmationCandidate
	if json.Unmarshal([]byte(saved), &receipt) != nil || json.Unmarshal([]byte(payload), &declaration) != nil || json.Unmarshal([]byte(evidence), &candidate) != nil {
		return invalid()
	}
	// Null must not silently become a legitimate numeric zero during decoding.
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(saved), &fields) != nil {
		return invalid()
	}
	for _, name := range []string{"Version", "CreatedAt", "ObservedBalanceCents", "ObservedBalanceVersion", "FinancialEffectCents"} {
		var value int64
		b, ok := fields[name]
		if !ok || string(b) == "null" || json.Unmarshal(b, &value) != nil {
			return invalid()
		}
	}
	var itemFields []map[string]json.RawMessage
	if json.Unmarshal(fields["Items"], &itemFields) != nil {
		return invalid()
	}
	for _, item := range itemFields {
		var cents int64
		b, ok := item["RepresentedCents"]
		if !ok || string(b) == "null" || json.Unmarshal(b, &cents) != nil || cents < 0 {
			return invalid()
		}
	}
	date, dateErr := civildate.ParseCivilDateInput(receipt.CutDate)
	if receipt.ID != id || receipt.ID != header.ID || receipt.WorkspaceID != ws || receipt.WorkspaceID != header.WorkspaceID || receipt.AccountID != account || receipt.AccountID != header.AccountID || receipt.Version != header.Version || receipt.Version <= 0 || receipt.CutDate != header.CutDate || dateErr != nil || date.String() != receipt.CutDate || receipt.CreatedBy == "" || receipt.CreatedBy != header.CreatedBy || receipt.CreatedAt <= 0 || receipt.CreatedAt != header.CreatedAt || receipt.RequestID != header.RequestID || receipt.Reason != header.Reason || receipt.IntegrationAdmission != InvestmentAdoptionAdmissionPending || receipt.IntegrationAdmission != header.IntegrationAdmission || receipt.FinancialEffectCents != 0 || header.FinancialEffectCents != 0 || len(receipt.Items) == 0 {
		return invalid()
	}
	if evidenceVersion != InvestmentAdoptionEvidenceVersion || declarationVersion != InvestmentAdoptionDeclarationVersion || declaration.EvidenceVersion != evidenceVersion || declaration.DeclarationVersion != declarationVersion || declaration.EvidenceToken != token || candidate.EvidenceVersion != evidenceVersion || candidate.EvidenceToken != token || candidate.AdoptionVersion != receipt.Version-1 || declaration.ExpectedAdoptionVersion != candidate.AdoptionVersion || !reflect.DeepEqual(receipt.Declaration, declaration) {
		return invalid()
	}
	if declaration.WorkspaceID != ws || declaration.AccountID != account || declaration.CutDate != receipt.CutDate || declaration.CreatedBy != receipt.CreatedBy || declaration.RequestID != receipt.RequestID || declaration.Reason != receipt.Reason || receipt.Coverage != declaration.Coverage || (receipt.Coverage != InvestmentAdoptionPartial && receipt.Coverage != InvestmentAdoptionAllIdentified) || !reflect.DeepEqual(receipt.DeclaredBalanceAtCutCents, declaration.DeclaredBalanceAtCutCents) {
		return invalid()
	}
	if candidate.Diagnostic.WorkspaceID != ws || candidate.Diagnostic.AccountID != account || candidate.Diagnostic.CutDate != receipt.CutDate || !reflect.DeepEqual(receipt.Diagnostic, candidate.Diagnostic) || receipt.ObservedBalanceCents != candidate.Diagnostic.CurrentBalanceCents || receipt.ObservedBalanceVersion != candidate.Diagnostic.BalanceVersion || receipt.ObservedBalanceVersion != declaration.ExpectedBalanceVersion {
		return invalid()
	}
	candidate.EvidenceToken = ""
	expectedToken, _, err := adoptionDigest(candidate)
	if err != nil || expectedToken != token {
		return invalid()
	}
	expectedCash := InvestmentAdoptionAmount{Evidence: adoptionEvidence(InvestmentEvidenceUnknown, "No broker cash declared or proven")}
	if declaration.Cash != nil {
		expectedCash.Cents = &declaration.Cash.Cents
		expectedCash.Evidence = adoptionEvidence(InvestmentEvidenceDeclared, declaration.Cash.Basis)
	}
	if !reflect.DeepEqual(receipt.Cash, expectedCash) {
		return invalid()
	}
	// A complement accumulates earlier components. A historical version must never
	// accidentally include items added by a later confirmation of the same account.
	rows, err := tx.QueryContext(ctx, `SELECT i.investment_id,i.position_id,i.reference_revision_id,i.represented_cents,i.economic_identity,i.identity_basis,c.evidence_json
 FROM investment_adoption_items i JOIN investment_adoption_confirmations c ON c.workspace_id=? AND c.workspace_id=i.workspace_id AND c.account_id=i.account_id AND c.id=i.confirmation_id
 WHERE i.workspace_id=? AND i.account_id=? AND c.version<=? ORDER BY i.investment_id`, ws, ws, account, receipt.Version)
	if err != nil {
		return empty, err
	}
	expected := map[string]InvestmentAdoptionConfirmedItem{}
	for rows.Next() {
		var item InvestmentAdoptionConfirmedItem
		var originEvidence string
		if err = rows.Scan(&item.InvestmentID, &item.PositionID, &item.ReferenceRevisionID, &item.RepresentedCents, &item.EconomicIdentity, &item.IdentityBasis, &originEvidence); err != nil {
			rows.Close()
			return empty, err
		}
		var origin InvestmentAdoptionConfirmationCandidate
		if json.Unmarshal([]byte(originEvidence), &origin) != nil {
			rows.Close()
			return invalid()
		}
		found := false
		for _, source := range origin.Diagnostic.Investments {
			if source.ID == item.InvestmentID && source.AccountID == account {
				item.PrincipalEvidence, item.FundingEvidence, item.ReceiptEvidence = source.PrincipalEvidence, source.FundingEvidence, source.ReceiptEvidence
				found = true
				break
			}
		}
		if !found || item.PrincipalEvidence.State == "" || item.FundingEvidence.State == "" || item.ReceiptEvidence.State == "" {
			rows.Close()
			return invalid()
		}
		expected[item.InvestmentID] = item
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return empty, err
	}
	if len(expected) != len(receipt.Items) {
		return invalid()
	}
	for _, item := range receipt.Items {
		want, ok := expected[item.InvestmentID]
		if !ok || !reflect.DeepEqual(item, want) {
			return invalid()
		}
		delete(expected, item.InvestmentID)
	}
	if len(expected) != 0 {
		return invalid()
	}
	return receipt, nil
}

// ReadInvestmentAdoptionConfirmation returns the immutable historical receipt.
// Current authority is checked even on replay/receipt access. Source validity is
// deliberately a separate API and never changes this receipt or admits B.
func ReadInvestmentAdoptionConfirmation(ctx context.Context, db *sql.DB, ws, account, id, user string) (InvestmentAdoptionConfirmation, error) {
	var result InvestmentAdoptionConfirmation
	if db == nil || ws == "" || account == "" || id == "" || user == "" {
		return result, ErrInvestmentValidation
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = adoptionAuthorizeTx(ctx, tx, ws, user); err != nil {
		return result, err
	}
	result, err = readValidatedAdoptionReceiptTx(ctx, tx, ws, account, id)
	if err != nil {
		return InvestmentAdoptionConfirmation{}, err
	}
	return result, tx.Commit()
}

type InvestmentAdoptionConfirmationValidity struct{ State, IntegrationAdmission string }

func ReadInvestmentAdoptionConfirmationValidity(ctx context.Context, db *sql.DB, ws, account, id, user string) (InvestmentAdoptionConfirmationValidity, error) {
	var result InvestmentAdoptionConfirmationValidity
	if db == nil || ws == "" || account == "" || id == "" || user == "" {
		return result, ErrInvestmentValidation
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = adoptionAuthorizeTx(ctx, tx, ws, user); err != nil {
		return result, err
	}
	var token string
	err = tx.QueryRowContext(ctx, `SELECT source_token,integration_admission FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=? AND id=?`, ws, account, id).Scan(&token, &result.IntegrationAdmission)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrInvestmentNotFound
	}
	if err != nil {
		return result, err
	}
	sources, err := adoptionSourceEvidenceTx(ctx, tx, ws, account)
	if err != nil {
		return result, err
	}
	current, _, err := adoptionDigest(sources)
	if err != nil {
		return result, err
	}
	result.State = "CURRENT"
	if current != token {
		result.State = "STALE_EVIDENCE"
	}
	return result, tx.Commit()
}
