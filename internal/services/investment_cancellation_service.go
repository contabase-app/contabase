package services

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
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

// Cancellation of the single initial funded purchase of an ATIVA investment.
// The compensating transfer, both balance effects and the immutable receipt
// share one transaction on the runtime Immediate writer. The receipt is the
// durable idempotency record (schema 79); the signed preview is only an
// authorization carrier and never outlives the process that issued it.

const (
	investmentCancellationOperation      = "CANCEL_INITIAL_FUNDED_INVESTMENT"
	investmentCancellationTTLSeconds     = 900
	investmentCancellationPayloadVersion = 1
	investmentCancellationReceiptVersion = 1
	investmentCancellationReasonMaxRunes = 500
)

var ErrInvestmentCancellationPreview = errors.New("prévia de cancelamento inválida ou expirada")

// The preview signing key is random per process and never persisted, logged
// or exported. A restart silently invalidates every unconfirmed preview:
// confirmation verifies with the current key and fails closed. Replaying an
// already confirmed cancellation reads the persisted receipt and never
// touches this key.
var investmentCancellationPreviewKey = newInvestmentCancellationPreviewKey()

func newInvestmentCancellationPreviewKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("contabase: investment cancellation preview key: " + err.Error())
	}
	return key
}

// investmentCancellationClaims is the canonical, self-contained preview
// payload bound to the HMAC signature. Field order is fixed by the struct,
// so json.Marshal is the canonical encoding on both sides of the token.
type investmentCancellationClaims struct {
	Version                  int64  `json:"version"`
	Operation                string `json:"operation"`
	WorkspaceID              string `json:"workspace_id"`
	ActingUserID             string `json:"acting_user_id"`
	InvestmentID             string `json:"investment_id"`
	RequestID                string `json:"request_id"`
	Reason                   string `json:"reason"`
	PreviewID                string `json:"preview_id"`
	IssuedAt                 int64  `json:"issued_at"`
	ExpiresAt                int64  `json:"expires_at"`
	PurchaseID               string `json:"purchase_id"`
	OriginalTransactionID    string `json:"original_transaction_id"`
	InvestmentAccountID      string `json:"investment_account_id"`
	OriginAccountID          string `json:"origin_account_id"`
	AmountCents              int64  `json:"amount_cents"`
	EconomicDate             string `json:"economic_date"`
	Timezone                 string `json:"timezone"`
	InvestmentBalanceCents   int64  `json:"investment_balance_cents"`
	InvestmentBalanceVersion int64  `json:"investment_balance_version"`
	OriginBalanceCents       int64  `json:"origin_balance_cents"`
	OriginBalanceVersion     int64  `json:"origin_balance_version"`
	EvidenceHash             string `json:"evidence_hash"`
}

// investmentCancellationPayload is the request side of the receipt. It is
// the replay comparison basis: the same request_id with this exact payload
// returns the persisted receipt. It intentionally carries no derived data,
// so a retry on another civil date still replays.
type investmentCancellationPayload struct {
	Version      int64  `json:"version"`
	Operation    string `json:"operation"`
	Confirmed    bool   `json:"confirmed"`
	WorkspaceID  string `json:"workspace_id"`
	ActingUserID string `json:"acting_user_id"`
	InvestmentID string `json:"investment_id"`
	RequestID    string `json:"request_id"`
	Reason       string `json:"reason"`
	PreviewToken string `json:"preview_token"`
}

// The receipt evidence mirrors the ten identity columns checked by the
// schema 79 JSON trigger plus the state the confirmation revalidates. The
// signing key and the full token are never part of any persisted or hashed
// blob.
type investmentCancellationReceiptEvidence struct {
	WorkspaceID              string `json:"workspace_id"`
	InvestmentID             string `json:"investment_id"`
	PurchaseID               string `json:"purchase_id"`
	OriginalTransactionID    string `json:"original_transaction_id"`
	InvestmentAccountID      string `json:"investment_account_id"`
	OriginAccountID          string `json:"origin_account_id"`
	AmountCents              int64  `json:"amount_cents"`
	EconomicDate             string `json:"economic_date"`
	EvidenceHash             string `json:"evidence_hash"`
	Timezone                 string `json:"timezone"`
	InvestmentBalanceCents   int64  `json:"investment_balance_cents"`
	InvestmentBalanceVersion int64  `json:"investment_balance_version"`
	OriginBalanceCents       int64  `json:"origin_balance_cents"`
	OriginBalanceVersion     int64  `json:"origin_balance_version"`
	IssuedAt                 int64  `json:"issued_at"`
}

// investmentCancellationEvidence is the financial snapshot behind
// evidence_hash. It deliberately contains no display names: renaming an
// account or investment must not invalidate a preview. Every dependent
// count must be zero; the slice order is fixed so the hash is stable.
type investmentCancellationEvidence struct {
	Investment        investmentEvidenceInvestment
	Purchase          investmentEvidencePurchase
	OriginalTransfer  investmentEvidenceTransfer
	InvestmentAccount investmentEvidenceAccount
	OriginAccount     investmentEvidenceAccount
	Dependents        []investmentEvidenceCount
}

type investmentEvidenceInvestment struct {
	Status         string `json:"status"`
	Archived       bool   `json:"archived"`
	AccountID      string `json:"account_id"`
	AppliedDate    string `json:"applied_date"`
	PrincipalCents int64  `json:"principal_cents"`
	CreatedBy      string `json:"created_by"`
}

type investmentEvidencePurchase struct {
	ID             string `json:"id"`
	PurchaseDate   string `json:"purchase_date"`
	PrincipalCents int64  `json:"principal_cents"`
	IsInitial      bool   `json:"is_initial"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      int64  `json:"created_at"`
	TotalPurchases int64  `json:"total_purchases"`
}

type investmentEvidenceTransfer struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Status               string `json:"status"`
	EffectMode           string `json:"effect_mode"`
	UserID               string `json:"user_id"`
	AccountID            string `json:"account_id"`
	DestinationAccountID string `json:"destination_account_id"`
	Amount               int64  `json:"amount"`
	Date                 int64  `json:"date"`
	DateCivil            string `json:"date_civil"`
	PaidAt               int64  `json:"paid_at"`
	PaidDateCivil        string `json:"paid_date_civil"`
}

type investmentEvidenceAccount struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Archived       bool   `json:"archived"`
	CurrentBalance int64  `json:"current_balance"`
	BalanceVersion int64  `json:"balance_version"`
}

type investmentEvidenceCount struct {
	Table  string `json:"table"`
	Column string `json:"column,omitempty"`
	Count  int64  `json:"count"`
}

// investmentCancellationState carries the resolved snapshot shared by the
// read-only preview and the locked confirmation.
type investmentCancellationState struct {
	Investment               models.Investment
	PurchaseID               string
	OriginalTransactionID    string
	InvestmentAccountID      string
	InvestmentAccountName    string
	OriginAccountID          string
	OriginAccountName        string
	InvestmentBalanceCents   int64
	InvestmentBalanceVersion int64
	OriginBalanceCents       int64
	OriginBalanceVersion     int64
	Evidence                 investmentCancellationEvidence
}

type InvestmentCancellationPreviewInput struct {
	WorkspaceID  string
	InvestmentID string
	ActingUserID string
	RequestID    string
	Reason       string
	// Location selects the civil date of the economic compensation. The
	// application timezone policy belongs to the HTTP layer; the service
	// only records and honors what it is given.
	Location *time.Location
}

type InvestmentCancellationPreview struct {
	Eligible                 bool
	Blockers                 []string
	AmountCents              int64
	EconomicDate             string
	Timezone                 string
	InvestmentAccountID      string
	InvestmentAccountName    string
	OriginAccountID          string
	OriginAccountName        string
	InvestmentBalanceCents   int64
	InvestmentBalanceVersion int64
	InvestmentAfterCents     int64
	OriginBalanceCents       int64
	OriginBalanceVersion     int64
	OriginAfterCents         int64
	PreviewID                string
	IssuedAt                 int64
	ExpiresAt                int64
	PreviewToken             string
}

type CancelInvestmentInput struct {
	WorkspaceID               string
	InvestmentID              string
	CreatedBy                 string
	RequestID                 string
	Reason                    string
	PreviewToken              string
	ExpectedSourceVersion     int64
	ExpectedInvestmentVersion int64
}

type InvestmentCancellationResult struct {
	Cancel  models.InvestmentCancellation
	Outcome InvestmentOperationOutcome
}

func validateInvestmentCancellationRequest(workspace, investment, user, requestID, reason string) (string, string, error) {
	if workspace == "" || investment == "" || user == "" {
		return "", "", fmt.Errorf("%w: contexto de cancelamento incompleto", ErrInvestmentValidation)
	}
	key, err := uuid.Parse(requestID)
	if err != nil {
		return "", "", fmt.Errorf("%w: formulário inválido; reabra o cancelamento", ErrInvestmentValidation)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > investmentCancellationReasonMaxRunes {
		return "", "", fmt.Errorf("%w: motivo deve ter de 1 a %d caracteres", ErrInvestmentValidation, investmentCancellationReasonMaxRunes)
	}
	return key.String(), reason, nil
}

// PreviewInvestmentCancellation resolves eligibility without writing and,
// when eligible, issues a self-contained HMAC-SHA256 signed token valid for
// 900 seconds. The preview binds the request payload, both account balances
// and versions and the financial evidence snapshot; any drift before
// confirmation fails closed.
func PreviewInvestmentCancellation(ctx context.Context, db *sql.DB, in InvestmentCancellationPreviewInput) (InvestmentCancellationPreview, error) {
	requestID, reason, err := validateInvestmentCancellationRequest(in.WorkspaceID, in.InvestmentID, in.ActingUserID, in.RequestID, in.Reason)
	if err != nil {
		return InvestmentCancellationPreview{}, err
	}
	if in.Location == nil {
		return InvestmentCancellationPreview{}, fmt.Errorf("%w: fuso horário obrigatório para a data econômica", ErrInvestmentValidation)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentCancellationPreview{}, err
	}
	defer tx.Rollback()
	economic := civildate.CivilDateToday(in.Location)
	state, blockers, err := evaluateInvestmentCancellationTx(ctx, tx, in.WorkspaceID, in.InvestmentID, economic.String())
	if err != nil {
		return InvestmentCancellationPreview{}, err
	}
	preview := InvestmentCancellationPreview{
		Blockers:                 blockers,
		AmountCents:              state.Investment.PrincipalCents,
		EconomicDate:             economic.String(),
		Timezone:                 in.Location.String(),
		InvestmentAccountID:      state.InvestmentAccountID,
		InvestmentAccountName:    state.InvestmentAccountName,
		OriginAccountID:          state.OriginAccountID,
		OriginAccountName:        state.OriginAccountName,
		InvestmentBalanceCents:   state.InvestmentBalanceCents,
		InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		OriginBalanceCents:       state.OriginBalanceCents,
		OriginBalanceVersion:     state.OriginBalanceVersion,
	}
	if len(blockers) > 0 {
		return preview, nil
	}
	if err := previewInvestmentCancellationOverflow(state); err != nil {
		preview.Blockers = append(preview.Blockers, "compensação deixaria saldo fora do intervalo permitido")
		return preview, nil
	}
	preview.Eligible = true
	preview.InvestmentAfterCents = state.InvestmentBalanceCents - state.Investment.PrincipalCents
	preview.OriginAfterCents = state.OriginBalanceCents + state.Investment.PrincipalCents
	evidenceHash, err := hashInvestmentCancellationEvidence(state.Evidence)
	if err != nil {
		return InvestmentCancellationPreview{}, err
	}
	issued := time.Now().Unix()
	claims := investmentCancellationClaims{
		Version: investmentCancellationPayloadVersion, Operation: investmentCancellationOperation,
		WorkspaceID: in.WorkspaceID, ActingUserID: in.ActingUserID, InvestmentID: in.InvestmentID,
		RequestID: requestID, Reason: reason,
		PreviewID: newInvestmentCancellationPreviewID(), IssuedAt: issued, ExpiresAt: issued + investmentCancellationTTLSeconds,
		PurchaseID: state.PurchaseID, OriginalTransactionID: state.OriginalTransactionID,
		InvestmentAccountID: state.InvestmentAccountID, OriginAccountID: state.OriginAccountID,
		AmountCents: state.Investment.PrincipalCents, EconomicDate: economic.String(), Timezone: in.Location.String(),
		InvestmentBalanceCents: state.InvestmentBalanceCents, InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		OriginBalanceCents: state.OriginBalanceCents, OriginBalanceVersion: state.OriginBalanceVersion,
		EvidenceHash: evidenceHash,
	}
	token, err := signInvestmentCancellationClaims(investmentCancellationPreviewKey, claims)
	if err != nil {
		return InvestmentCancellationPreview{}, err
	}
	preview.PreviewID = claims.PreviewID
	preview.IssuedAt = claims.IssuedAt
	preview.ExpiresAt = claims.ExpiresAt
	preview.PreviewToken = token
	return preview, nil
}

// CancelInvestment confirms a signed preview under the runtime Immediate
// writer. Replay by request_id precedes every key- or version-dependent
// check, so a confirmed cancellation replays after a restart without the
// previous signing key. The compensating transfer, exactly one
// ApplyBalanceEffect and the immutable receipt commit atomically; any
// failure rolls the whole effect back.
func CancelInvestment(ctx context.Context, db *sql.DB, in CancelInvestmentInput) (InvestmentCancellationResult, error) {
	requestID, reason, err := validateInvestmentCancellationRequest(in.WorkspaceID, in.InvestmentID, in.CreatedBy, in.RequestID, in.Reason)
	if err != nil {
		return InvestmentCancellationResult{}, err
	}
	if in.PreviewToken == "" || in.ExpectedSourceVersion < 0 || in.ExpectedInvestmentVersion < 0 {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: confirmação de cancelamento incompleta", ErrInvestmentValidation)
	}
	receiptID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cancellation\x00"+in.WorkspaceID+"\x00"+in.InvestmentID+"\x00"+in.CreatedBy+"\x00"+requestID)).String()
	payload, err := json.Marshal(investmentCancellationPayload{
		Version: investmentCancellationPayloadVersion, Operation: investmentCancellationOperation, Confirmed: true,
		WorkspaceID: in.WorkspaceID, ActingUserID: in.CreatedBy, InvestmentID: in.InvestmentID,
		RequestID: requestID, Reason: reason, PreviewToken: in.PreviewToken,
	})
	if err != nil {
		return InvestmentCancellationResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return InvestmentCancellationResult{}, err
	}
	defer tx.Rollback()
	// Replay-first: the persisted receipt is the only idempotency authority.
	// No signing key, expiry or version participates in this lookup.
	saved, savedPayload, err := readInvestmentCancellationRow(ctx, tx, investmentCancellationSelectWhere+` AND c.request_id=?`, in.WorkspaceID, requestID)
	if err == nil {
		if savedPayload != string(payload) {
			return InvestmentCancellationResult{}, fmt.Errorf("%w: este formulário já cancelou com outros dados; reabra o cancelamento", ErrInvestmentConflict)
		}
		if err := tx.Commit(); err != nil {
			return InvestmentCancellationResult{}, &InvestmentCommitError{err: err}
		}
		return InvestmentCancellationResult{Cancel: saved, Outcome: InvestmentOperationReplayed}, nil
	}
	if !errors.Is(err, ErrInvestmentNotFound) {
		return InvestmentCancellationResult{}, err
	}
	if _, _, err := readInvestmentCancellationRow(ctx, tx, investmentCancellationSelectWhere+` AND c.investment_id=?`, in.WorkspaceID, in.InvestmentID); err == nil {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: aplicação já cancelada", ErrInvestmentConflict)
	} else if !errors.Is(err, ErrInvestmentNotFound) {
		return InvestmentCancellationResult{}, err
	}
	claims, err := parseInvestmentCancellationToken(investmentCancellationPreviewKey, in.PreviewToken)
	if err != nil {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: %v", ErrInvestmentCancellationPreview, err)
	}
	// A signed preview of another workspace or investment is indistinguishable
	// from an unknown investment: no cross-tenant existence may leak.
	if claims.WorkspaceID != in.WorkspaceID || claims.InvestmentID != in.InvestmentID {
		return InvestmentCancellationResult{}, ErrInvestmentNotFound
	}
	if claims.ActingUserID != in.CreatedBy || claims.RequestID != requestID || claims.Reason != reason {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: prévia pertence a outra requisição", ErrInvestmentCancellationPreview)
	}
	if now := time.Now().Unix(); now < claims.IssuedAt || now >= claims.ExpiresAt || claims.ExpiresAt != claims.IssuedAt+investmentCancellationTTLSeconds {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: prévia expirada; solicite outra", ErrInvestmentCancellationPreview)
	}
	state, blockers, err := evaluateInvestmentCancellationTx(ctx, tx, in.WorkspaceID, in.InvestmentID, claims.EconomicDate)
	if err != nil {
		return InvestmentCancellationResult{}, err
	}
	if len(blockers) > 0 {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: %s", ErrInvestmentConflict, blockers[0])
	}
	if err := verifyInvestmentCancellationClaims(claims, state); err != nil {
		return InvestmentCancellationResult{}, err
	}
	if in.ExpectedSourceVersion != state.OriginBalanceVersion || in.ExpectedInvestmentVersion != state.InvestmentBalanceVersion {
		return InvestmentCancellationResult{}, ErrBalanceAdjustmentConflict
	}
	if err := previewInvestmentCancellationOverflow(state); err != nil {
		return InvestmentCancellationResult{}, err
	}
	economic, err := civildate.ParseCivilDateInput(claims.EconomicDate)
	if err != nil {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: data econômica da prévia inválida", ErrInvestmentCancellationPreview)
	}
	// Temporal revalidation immediately before the commit: confirmed_at must
	// still fall inside the signed window (the schema CHECK is the last guard).
	confirmedAt := time.Now().Unix()
	if confirmedAt < claims.IssuedAt || confirmedAt >= claims.ExpiresAt {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: prévia expirada; solicite outra", ErrInvestmentCancellationPreview)
	}
	compensationID := InvestmentCancellationTransferPrefix + receiptID
	at := civildate.CivilDateToLegacyUnixNoonUTC(economic)
	if _, err = tx.ExecContext(ctx, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
VALUES(?,?,?,?,?,'TRANSFER',?,?,?,?,?,'paid','Cancelamento de investimento',?,?)`,
		compensationID, in.WorkspaceID, in.CreatedBy, state.InvestmentAccountID, state.OriginAccountID,
		state.Investment.PrincipalCents, at, claims.EconomicDate, at, claims.EconomicDate, confirmedAt, confirmedAt); err != nil {
		return InvestmentCancellationResult{}, err
	}
	if err = ApplyBalanceEffect(tx, in.WorkspaceID, models.TransactionTypeTransfer, models.AccountTypeInvestment, models.PaymentStatusPaid, state.Investment.PrincipalCents, state.InvestmentAccountID, state.OriginAccountID, confirmedAt); err != nil {
		return InvestmentCancellationResult{}, err
	}
	var investmentAfter, investmentAfterVersion, originAfter, originAfterVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, state.InvestmentAccountID).Scan(&investmentAfter, &investmentAfterVersion); err != nil {
		return InvestmentCancellationResult{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, state.OriginAccountID).Scan(&originAfter, &originAfterVersion); err != nil {
		return InvestmentCancellationResult{}, err
	}
	if investmentAfter != state.InvestmentBalanceCents-state.Investment.PrincipalCents || investmentAfterVersion != state.InvestmentBalanceVersion+1 ||
		originAfter != state.OriginBalanceCents+state.Investment.PrincipalCents || originAfterVersion != state.OriginBalanceVersion+1 {
		return InvestmentCancellationResult{}, fmt.Errorf("%w: efeito de saldo divergiu durante o cancelamento", ErrInvestmentConflict)
	}
	receipt := models.InvestmentCancellation{
		WorkspaceID: in.WorkspaceID, ID: receiptID, InvestmentID: in.InvestmentID, PurchaseID: state.PurchaseID,
		OriginalTransactionID: state.OriginalTransactionID, CompensationTransactionID: compensationID,
		InvestmentAccountID: state.InvestmentAccountID, OriginAccountID: state.OriginAccountID,
		AmountCents:           state.Investment.PrincipalCents,
		InvestmentBeforeCents: state.InvestmentBalanceCents, InvestmentAfterCents: investmentAfter,
		OriginBeforeCents: state.OriginBalanceCents, OriginAfterCents: originAfter,
		InvestmentBeforeVersion: state.InvestmentBalanceVersion, InvestmentAfterVersion: investmentAfterVersion,
		OriginBeforeVersion: state.OriginBalanceVersion, OriginAfterVersion: originAfterVersion,
		CreatedBy: in.CreatedBy, ConfirmedAt: confirmedAt, EconomicDate: claims.EconomicDate, Reason: reason,
		RequestID: requestID, PreviewID: claims.PreviewID, PreviewIssuedAt: claims.IssuedAt, PreviewExpiresAt: claims.ExpiresAt,
		EvidenceHash: claims.EvidenceHash,
	}
	evidenceJSON, err := json.Marshal(investmentCancellationReceiptEvidence{
		WorkspaceID: receipt.WorkspaceID, InvestmentID: receipt.InvestmentID, PurchaseID: receipt.PurchaseID,
		OriginalTransactionID: receipt.OriginalTransactionID, InvestmentAccountID: receipt.InvestmentAccountID,
		OriginAccountID: receipt.OriginAccountID, AmountCents: receipt.AmountCents, EconomicDate: receipt.EconomicDate,
		EvidenceHash: receipt.EvidenceHash, Timezone: claims.Timezone,
		InvestmentBalanceCents: state.InvestmentBalanceCents, InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		OriginBalanceCents: state.OriginBalanceCents, OriginBalanceVersion: state.OriginBalanceVersion,
		IssuedAt: claims.IssuedAt,
	})
	if err != nil {
		return InvestmentCancellationResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investment_cancellations (`+investmentCancellationColumns+`,payload_version,receipt_version,payload_json,evidence_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.WorkspaceID, receipt.ID, receipt.InvestmentID, receipt.PurchaseID, receipt.OriginalTransactionID, receipt.CompensationTransactionID,
		receipt.InvestmentAccountID, receipt.OriginAccountID, receipt.AmountCents,
		receipt.InvestmentBeforeCents, receipt.InvestmentAfterCents, receipt.OriginBeforeCents, receipt.OriginAfterCents,
		receipt.InvestmentBeforeVersion, receipt.InvestmentAfterVersion, receipt.OriginBeforeVersion, receipt.OriginAfterVersion,
		receipt.CreatedBy, receipt.ConfirmedAt, receipt.EconomicDate, receipt.Reason, receipt.RequestID,
		receipt.PreviewID, receipt.PreviewIssuedAt, receipt.PreviewExpiresAt, receipt.EvidenceHash,
		investmentCancellationPayloadVersion, investmentCancellationReceiptVersion, string(payload), string(evidenceJSON)); err != nil {
		return InvestmentCancellationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		// The outcome is uncertain, not failed: consult the durable receipt
		// independently and recover it with the same idempotency key.
		if saved, savedPayload, lookupErr := investmentCancellationRecoveryRead(ctx, db, in.WorkspaceID, requestID); lookupErr == nil && savedPayload == string(payload) {
			return InvestmentCancellationResult{Cancel: saved, Outcome: InvestmentOperationReplayed}, nil
		}
		return InvestmentCancellationResult{}, &InvestmentCommitError{err: err}
	}
	return InvestmentCancellationResult{Cancel: receipt, Outcome: InvestmentOperationCreated}, nil
}

// GetInvestmentCancellation resolves the receipt of one investment for
// projections and replays. A foreign workspace is indistinguishable from an
// unknown investment.
func GetInvestmentCancellation(ctx context.Context, db *sql.DB, workspace, investment string) (models.InvestmentCancellation, error) {
	if workspace == "" || investment == "" {
		return models.InvestmentCancellation{}, ErrInvestmentNotFound
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentCancellation{}, err
	}
	defer tx.Rollback()
	receipt, _, err := readInvestmentCancellationRow(ctx, tx, investmentCancellationSelectWhere+` AND c.investment_id=?`, workspace, investment)
	if err != nil {
		return models.InvestmentCancellation{}, err
	}
	return receipt, nil
}

func investmentCancellationRecoveryRead(ctx context.Context, db *sql.DB, workspace, requestID string) (models.InvestmentCancellation, string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentCancellation{}, "", err
	}
	defer tx.Rollback()
	return readInvestmentCancellationRow(ctx, tx, investmentCancellationSelectWhere+` AND c.request_id=?`, workspace, requestID)
}

const investmentCancellationColumns = `workspace_id,id,investment_id,purchase_id,original_transaction_id,compensation_transaction_id,investment_account_id,origin_account_id,amount_cents,investment_before_cents,investment_after_cents,origin_before_cents,origin_after_cents,investment_before_version,investment_after_version,origin_before_version,origin_after_version,created_by,confirmed_at,economic_date,reason,request_id,preview_id,preview_issued_at,preview_expires_at,evidence_hash`

const investmentCancellationSelectWhere = `SELECT ` + investmentCancellationColumns + `,c.payload_json FROM investment_cancellations c WHERE c.workspace_id=?`

func readInvestmentCancellationRow(ctx context.Context, tx *sql.Tx, query string, args ...any) (models.InvestmentCancellation, string, error) {
	var r models.InvestmentCancellation
	var payload string
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&r.WorkspaceID, &r.ID, &r.InvestmentID, &r.PurchaseID, &r.OriginalTransactionID, &r.CompensationTransactionID,
		&r.InvestmentAccountID, &r.OriginAccountID, &r.AmountCents,
		&r.InvestmentBeforeCents, &r.InvestmentAfterCents, &r.OriginBeforeCents, &r.OriginAfterCents,
		&r.InvestmentBeforeVersion, &r.InvestmentAfterVersion, &r.OriginBeforeVersion, &r.OriginAfterVersion,
		&r.CreatedBy, &r.ConfirmedAt, &r.EconomicDate, &r.Reason, &r.RequestID,
		&r.PreviewID, &r.PreviewIssuedAt, &r.PreviewExpiresAt, &r.EvidenceHash, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return models.InvestmentCancellation{}, "", ErrInvestmentNotFound
	}
	if err != nil {
		return models.InvestmentCancellation{}, "", err
	}
	return r, payload, nil
}

// verifyInvestmentCancellationClaims recomputes the preview evidence under
// the writer lock and requires the signed claims to describe exactly the
// current state: any independent movement, reconciliation or dependency
// added after the preview fails closed.
func verifyInvestmentCancellationClaims(claims investmentCancellationClaims, state investmentCancellationState) error {
	evidenceHash, err := hashInvestmentCancellationEvidence(state.Evidence)
	if err != nil {
		return err
	}
	if claims.PurchaseID != state.PurchaseID ||
		claims.OriginalTransactionID != state.OriginalTransactionID ||
		claims.InvestmentAccountID != state.InvestmentAccountID ||
		claims.OriginAccountID != state.OriginAccountID ||
		claims.AmountCents != state.Investment.PrincipalCents ||
		claims.InvestmentBalanceCents != state.InvestmentBalanceCents ||
		claims.InvestmentBalanceVersion != state.InvestmentBalanceVersion ||
		claims.OriginBalanceCents != state.OriginBalanceCents ||
		claims.OriginBalanceVersion != state.OriginBalanceVersion ||
		claims.EvidenceHash != evidenceHash {
		return fmt.Errorf("%w: estado financeiro mudou desde a prévia; solicite outra", ErrInvestmentConflict)
	}
	return nil
}

func previewInvestmentCancellationOverflow(state investmentCancellationState) error {
	amount := state.Investment.PrincipalCents
	if state.OriginBalanceCents > math.MaxInt64-amount || state.InvestmentBalanceCents < math.MinInt64+amount {
		return ErrBalanceOutOfRange
	}
	if state.OriginBalanceVersion >= math.MaxInt64 || state.InvestmentBalanceVersion >= math.MaxInt64 {
		return ErrBalanceOutOfRange
	}
	return nil
}

func hashInvestmentCancellationEvidence(evidence investmentCancellationEvidence) (string, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newInvestmentCancellationPreviewID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("contabase: investment cancellation preview id: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func signInvestmentCancellationClaims(key []byte, claims investmentCancellationClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseInvestmentCancellationToken(key []byte, token string) (investmentCancellationClaims, error) {
	var claims investmentCancellationClaims
	dot := strings.IndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return claims, errors.New("formato inválido")
	}
	payload, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return claims, errors.New("conteúdo ilegível")
	}
	signature, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return claims, errors.New("assinatura ilegível")
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, errors.New("assinatura não confere")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, errors.New("conteúdo inválido")
	}
	if claims.Version != investmentCancellationPayloadVersion || claims.Operation != investmentCancellationOperation {
		return claims, errors.New("operação desconhecida")
	}
	return claims, nil
}

// The dependent tables mirrored from the schema 79 triggers. Every count must
// stay zero for the investment (children) and for the original transfer
// (links). The order is part of the evidence hash.
var investmentCancellationDependentTables = []string{
	"investment_valuations", "investment_withdrawals", "investment_withdrawal_allocations",
	"investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions",
	"investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions",
	"investment_settlement_receipts", "investment_settlement_applications", "investment_result_classifications",
	"investment_adoption_items",
}

var investmentCancellationTransferLinks = []investmentEvidenceCount{
	{"investment_purchase_funding", "transaction_id", 0},
	{"investment_settlement_receipts", "transaction_id", 0},
	{"investment_settlement_prior_effects", "transaction_id", 0},
	{"investment_result_classifications", "transaction_id", 0},
	{"transaction_refunds", "transaction_id", 0},
	{"transaction_status_history", "transaction_id", 0},
	{"transactions", "parent_id", 0},
	{"invoice_payments", "transaction_id", 0},
	{"invoice_payments", "reversal_transaction_id", 0},
	{"box_virtual_ledger", "source_transaction_id", 0},
}

// evaluateInvestmentCancellationTx mirrors the schema 79 insert trigger as a
// service-level check, so the preview can explain blockers and the
// confirmation can fail with typed errors. Independent movements on either
// account are deliberately not blockers: only reconciliations, later
// operations and dependencies of the application itself are.
func evaluateInvestmentCancellationTx(ctx context.Context, tx *sql.Tx, workspace, investment, economicDate string) (investmentCancellationState, []string, error) {
	state := investmentCancellationState{}
	var archived sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,status,account_id,applied_date,principal_cents,created_by,created_at,archived_at FROM investments WHERE workspace_id=? AND id=?`, workspace, investment).
		Scan(&state.Investment.ID, &state.Investment.Status, &state.Investment.AccountID, &state.Investment.AppliedDate, &state.Investment.PrincipalCents, &state.Investment.CreatedBy, &state.Investment.CreatedAt, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, ErrInvestmentNotFound
	}
	if err != nil {
		return state, nil, err
	}
	state.Investment.WorkspaceID = workspace
	state.InvestmentAccountID = state.Investment.AccountID
	state.Evidence.Investment = investmentEvidenceInvestment{
		Status: state.Investment.Status, Archived: archived.Valid, AccountID: state.Investment.AccountID,
		AppliedDate: state.Investment.AppliedDate, PrincipalCents: state.Investment.PrincipalCents, CreatedBy: state.Investment.CreatedBy,
	}
	blockers := make([]string, 0, 4)
	if state.Investment.Status != models.InvestmentStatusAtiva {
		blockers = append(blockers, "aplicação não está ativa")
	}
	if archived.Valid {
		blockers = append(blockers, "aplicação arquivada")
	}
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_purchases WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&total); err != nil {
		return state, nil, err
	}
	purchase := investmentEvidencePurchase{ID: "initial:" + investment, TotalPurchases: total}
	err = tx.QueryRowContext(ctx, `SELECT purchase_date,principal_cents,is_initial,created_by,created_at FROM investment_purchases WHERE workspace_id=? AND investment_id=? AND id=?`, workspace, investment, purchase.ID).
		Scan(&purchase.PurchaseDate, &purchase.PrincipalCents, &purchase.IsInitial, &purchase.CreatedBy, &purchase.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		blockers = append(blockers, "compra inicial financiada ausente")
		err = nil
	}
	if err != nil {
		return state, nil, err
	}
	state.PurchaseID = purchase.ID
	state.Evidence.Purchase = purchase
	if total != 1 {
		// A later purchase is a later operation of the application itself.
		blockers = append(blockers, "aplicação possui operações ou dependências posteriores")
	} else if !purchase.IsInitial ||
		purchase.PurchaseDate != state.Investment.AppliedDate ||
		purchase.PrincipalCents != state.Investment.PrincipalCents ||
		purchase.CreatedBy != state.Investment.CreatedBy ||
		purchase.CreatedAt != state.Investment.CreatedAt {
		blockers = append(blockers, "compra inicial divergente do registro da aplicação")
	}
	transfer := investmentEvidenceTransfer{ID: InvestmentPurchaseTransferPrefix + purchase.ID}
	err = tx.QueryRowContext(ctx, `SELECT type,status,effect_mode,user_id,account_id,destination_account_id,amount,date,date_civil,paid_at,paid_date_civil FROM transactions WHERE workspace_id=? AND id=?`, workspace, transfer.ID).
		Scan(&transfer.Type, &transfer.Status, &transfer.EffectMode, &transfer.UserID, &transfer.AccountID, &transfer.DestinationAccountID, &transfer.Amount, &transfer.Date, &transfer.DateCivil, &transfer.PaidAt, &transfer.PaidDateCivil)
	if errors.Is(err, sql.ErrNoRows) {
		blockers = append(blockers, "transferência original ausente")
		err = nil
	}
	if err != nil {
		return state, nil, err
	}
	state.OriginalTransactionID = transfer.ID
	state.OriginAccountID = transfer.AccountID
	noonOK := false
	if purchase.PurchaseDate != "" {
		if civil, civilErr := civildate.ParseCivilDateInput(purchase.PurchaseDate); civilErr == nil {
			noon := civildate.CivilDateToLegacyUnixNoonUTC(civil)
			noonOK = transfer.Type == models.TransactionTypeTransfer && transfer.Status == models.PaymentStatusPaid &&
				transfer.EffectMode == models.TransactionEffectStandard && transfer.UserID == purchase.CreatedBy &&
				transfer.AccountID != "" && transfer.DestinationAccountID == state.Investment.AccountID &&
				transfer.Amount == purchase.PrincipalCents && transfer.DateCivil == purchase.PurchaseDate &&
				transfer.PaidDateCivil == purchase.PurchaseDate && transfer.Date == noon && transfer.PaidAt == transfer.Date
		}
	}
	if !noonOK {
		blockers = append(blockers, "transferência original divergente")
	}
	state.Evidence.OriginalTransfer = transfer
	if err := readInvestmentCancellationAccountTx(ctx, tx, workspace, state.Investment.AccountID, models.AccountTypeInvestment, &state.Evidence.InvestmentAccount, &state.InvestmentAccountName); err != nil {
		if errors.Is(err, ErrInvestmentConflict) {
			blockers = append(blockers, "conta de investimento inválida ou arquivada")
			state.Evidence.InvestmentAccount = investmentEvidenceAccount{ID: state.Investment.AccountID}
		} else {
			return state, nil, err
		}
	}
	if state.OriginAccountID != "" {
		if err := readInvestmentCancellationAccountTx(ctx, tx, workspace, state.OriginAccountID, "", &state.Evidence.OriginAccount, &state.OriginAccountName); err != nil {
			if errors.Is(err, ErrInvestmentConflict) {
				blockers = append(blockers, "conta de origem inválida ou arquivada")
				state.Evidence.OriginAccount = investmentEvidenceAccount{ID: state.OriginAccountID}
			} else {
				return state, nil, err
			}
		}
	}
	if purchase.PurchaseDate != "" && purchase.PurchaseDate > economicDate {
		blockers = append(blockers, "data econômica anterior à aplicação")
	}
	for _, table := range investmentCancellationDependentTables {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&count); err != nil {
			return state, nil, err
		}
		state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: table, Count: count})
		if count != 0 {
			blockers = append(blockers, "aplicação possui operações ou dependências posteriores")
		}
	}
	var adjustments int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_balance_adjustments WHERE workspace_id=? AND account_id IN (?,?)`, workspace, state.Investment.AccountID, state.OriginAccountID).Scan(&adjustments); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "account_balance_adjustments", Count: adjustments})
	if adjustments != 0 {
		blockers = append(blockers, "conciliação presente em uma das contas")
	}
	var adoptions int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=?`, workspace, state.Investment.AccountID).Scan(&adoptions); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_adoption_confirmations", Column: "account_id", Count: adoptions})
	if adoptions != 0 {
		blockers = append(blockers, "conta de investimento possui declaração de adoção")
	}
	for _, link := range investmentCancellationTransferLinks {
		var count int64
		query := `SELECT COUNT(*) FROM ` + link.Table + ` WHERE workspace_id=? AND ` + link.Column + `=?`
		if link.Table == "box_virtual_ledger" {
			query = `SELECT COUNT(*) FROM box_virtual_ledger v JOIN boxes b ON b.id=v.box_id AND b.workspace_id=? WHERE v.source_transaction_id=?`
		}
		if err := tx.QueryRowContext(ctx, query, workspace, state.OriginalTransactionID).Scan(&count); err != nil {
			return state, nil, err
		}
		state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: link.Table, Column: link.Column, Count: count})
		if count != 0 {
			blockers = append(blockers, "transferência original já possui conciliação ou vínculo")
		}
	}
	state.InvestmentBalanceCents = state.Evidence.InvestmentAccount.CurrentBalance
	state.InvestmentBalanceVersion = state.Evidence.InvestmentAccount.BalanceVersion
	state.OriginBalanceCents = state.Evidence.OriginAccount.CurrentBalance
	state.OriginBalanceVersion = state.Evidence.OriginAccount.BalanceVersion
	return state, blockers, nil
}

func readInvestmentCancellationAccountTx(ctx context.Context, tx *sql.Tx, workspace, account, requiredType string, evidence *investmentEvidenceAccount, name *string) error {
	var archived sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,type,name,archived_at,current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, workspace, account).
		Scan(&evidence.ID, &evidence.Type, name, &archived, &evidence.CurrentBalance, &evidence.BalanceVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentConflict
	}
	if err != nil {
		return err
	}
	evidence.Archived = archived.Valid
	valid := evidence.Type == models.AccountTypeChecking || evidence.Type == models.AccountTypeSavings || evidence.Type == models.AccountTypeWallet
	if requiredType == models.AccountTypeInvestment {
		valid = evidence.Type == models.AccountTypeInvestment
	}
	if !valid || archived.Valid || evidence.ID == "" {
		return ErrInvestmentConflict
	}
	return nil
}
