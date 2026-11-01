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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

// Logical cancellation of the single initial record-only purchase of an ATIVA
// investment (schema 81). The lot has no automatic funding transfer, so the
// confirmation performs no financial compensation at all: no transaction, no
// ApplyBalanceEffect, no balance version bump. Only the immutable receipt is
// written, once, on the runtime Immediate writer. The receipt is the durable
// idempotency record; the signed preview is only an authorization carrier and
// never outlives the process that issued it.

const (
	investmentRecordCancellationOperation      = "CANCEL_INITIAL_RECORD_INVESTMENT"
	investmentRecordCancellationTTLSeconds     = 900
	investmentRecordCancellationPayloadVersion = 1
	investmentRecordCancellationReceiptVersion = 1
	investmentRecordCancellationReasonMaxRunes = 500
)

var ErrInvestmentRecordCancellationPreview = errors.New("prévia de cancelamento de registro inválida ou expirada")

// IsMissingInvestmentRecordCancellationsTable reports whether err is only the
// absence of the schema 81 receipt table. A pre-81 database holds no record
// cancellation by construction: read paths fall back to "nothing cancelled"
// and migration 81 itself introduces the table and its triggers.
func IsMissingInvestmentRecordCancellationsTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: investment_record_cancellations")
}

// The preview signing key is random per process and never persisted, logged
// or exported. A restart silently invalidates every unconfirmed preview:
// confirmation verifies with the current key and fails closed. Replaying an
// already confirmed cancellation reads the persisted receipt and never
// touches this key.
var investmentRecordCancellationPreviewKey = newInvestmentRecordCancellationPreviewKey()

func newInvestmentRecordCancellationPreviewKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("contabase: investment record cancellation preview key: " + err.Error())
	}
	return key
}

// investmentRecordCancellationClaims is the canonical, self-contained preview
// payload bound to the HMAC signature. Field order is fixed by the struct, so
// json.Marshal is the canonical encoding on both sides of the token.
type investmentRecordCancellationClaims struct {
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
	InvestmentAccountID      string `json:"investment_account_id"`
	AmountCents              int64  `json:"amount_cents"`
	EconomicDate             string `json:"economic_date"`
	Timezone                 string `json:"timezone"`
	InvestmentBalanceCents   int64  `json:"investment_balance_cents"`
	InvestmentBalanceVersion int64  `json:"investment_balance_version"`
	EvidenceHash             string `json:"evidence_hash"`
}

// investmentRecordCancellationPayload is the request side of the receipt. It
// is the replay comparison basis: the same request_id with this exact payload
// returns the persisted receipt. It intentionally carries no derived data, so
// a retry on another civil date still replays.
type investmentRecordCancellationPayload struct {
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

// The receipt evidence mirrors the identity columns checked by the schema 81
// JSON trigger plus the state the confirmation revalidates. The signing key
// and the full token are never part of any persisted or hashed blob.
type investmentRecordCancellationReceiptEvidence struct {
	WorkspaceID              string `json:"workspace_id"`
	InvestmentID             string `json:"investment_id"`
	PurchaseID               string `json:"purchase_id"`
	InvestmentAccountID      string `json:"investment_account_id"`
	AmountCents              int64  `json:"amount_cents"`
	EconomicDate             string `json:"economic_date"`
	EvidenceHash             string `json:"evidence_hash"`
	Timezone                 string `json:"timezone"`
	InvestmentBalanceCents   int64  `json:"investment_balance_cents"`
	InvestmentBalanceVersion int64  `json:"investment_balance_version"`
	IssuedAt                 int64  `json:"issued_at"`
}

// investmentRecordCancellationEvidence is the snapshot behind evidence_hash.
// No display names: renaming an account or investment must not invalidate a
// preview. Every dependent count must be zero; the slice order is fixed so
// the hash is stable. A record-only lot has no transfer and no origin account.
type investmentRecordCancellationEvidence struct {
	Investment        investmentEvidenceInvestment
	Purchase          investmentEvidencePurchase
	InvestmentAccount investmentEvidenceAccount
	Dependents        []investmentEvidenceCount
}

// investmentRecordCancellationState carries the resolved snapshot shared by
// the read-only preview and the locked confirmation.
type investmentRecordCancellationState struct {
	Investment               models.Investment
	PurchaseID               string
	InvestmentAccountID      string
	InvestmentAccountName    string
	InvestmentBalanceCents   int64
	InvestmentBalanceVersion int64
	Evidence                 investmentRecordCancellationEvidence
}

type InvestmentRecordCancellationPreviewInput struct {
	WorkspaceID  string
	InvestmentID string
	ActingUserID string
	RequestID    string
	Reason       string
	// Location selects the civil date recorded on the receipt. The
	// application timezone policy belongs to the HTTP layer; the service only
	// records and honors what it is given.
	Location *time.Location
}

type InvestmentRecordCancellationPreview struct {
	Eligible                 bool
	Blockers                 []string
	AmountCents              int64
	EconomicDate             string
	Timezone                 string
	InvestmentAccountID      string
	InvestmentAccountName    string
	InvestmentBalanceCents   int64
	InvestmentBalanceVersion int64
	PreviewID                string
	IssuedAt                 int64
	ExpiresAt                int64
	PreviewToken             string
}

type CancelInvestmentRecordInput struct {
	WorkspaceID               string
	InvestmentID              string
	CreatedBy                 string
	RequestID                 string
	Reason                    string
	PreviewToken              string
	ExpectedInvestmentVersion int64
}

type InvestmentRecordCancellationResult struct {
	Cancel  models.InvestmentRecordCancellation
	Outcome InvestmentOperationOutcome
}

func validateInvestmentRecordCancellationRequest(workspace, investment, user, requestID, reason string) (string, string, error) {
	if workspace == "" || investment == "" || user == "" {
		return "", "", fmt.Errorf("%w: contexto de cancelamento de registro incompleto", ErrInvestmentValidation)
	}
	key, err := uuid.Parse(requestID)
	if err != nil {
		return "", "", fmt.Errorf("%w: formulário inválido; reabra o cancelamento do registro", ErrInvestmentValidation)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > investmentRecordCancellationReasonMaxRunes {
		return "", "", fmt.Errorf("%w: motivo deve ter de 1 a %d caracteres", ErrInvestmentValidation, investmentRecordCancellationReasonMaxRunes)
	}
	return key.String(), reason, nil
}

// PreviewInvestmentRecordCancellation resolves eligibility without writing
// and, when eligible, issues a self-contained HMAC-SHA256 signed token valid
// for 900 seconds. The preview binds the request payload, the observed
// account balance and version and the evidence snapshot; any drift before
// confirmation fails closed. Eligibility itself never depends on balances:
// the registration is cancelled without moving anything.
func PreviewInvestmentRecordCancellation(ctx context.Context, db *sql.DB, in InvestmentRecordCancellationPreviewInput) (InvestmentRecordCancellationPreview, error) {
	requestID, reason, err := validateInvestmentRecordCancellationRequest(in.WorkspaceID, in.InvestmentID, in.ActingUserID, in.RequestID, in.Reason)
	if err != nil {
		return InvestmentRecordCancellationPreview{}, err
	}
	if in.Location == nil {
		return InvestmentRecordCancellationPreview{}, fmt.Errorf("%w: fuso horário obrigatório para a data do registro", ErrInvestmentValidation)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentRecordCancellationPreview{}, err
	}
	defer tx.Rollback()
	economic := civildate.CivilDateToday(in.Location)
	state, blockers, err := evaluateInvestmentRecordCancellationTx(ctx, tx, in.WorkspaceID, in.InvestmentID, economic.String())
	if err != nil {
		return InvestmentRecordCancellationPreview{}, err
	}
	preview := InvestmentRecordCancellationPreview{
		Blockers:                 blockers,
		AmountCents:              state.Investment.PrincipalCents,
		EconomicDate:             economic.String(),
		Timezone:                 in.Location.String(),
		InvestmentAccountID:      state.InvestmentAccountID,
		InvestmentAccountName:    state.InvestmentAccountName,
		InvestmentBalanceCents:   state.InvestmentBalanceCents,
		InvestmentBalanceVersion: state.InvestmentBalanceVersion,
	}
	if len(blockers) > 0 {
		return preview, nil
	}
	preview.Eligible = true
	evidenceHash, err := hashInvestmentRecordCancellationEvidence(state.Evidence)
	if err != nil {
		return InvestmentRecordCancellationPreview{}, err
	}
	issued := time.Now().Unix()
	claims := investmentRecordCancellationClaims{
		Version: investmentRecordCancellationPayloadVersion, Operation: investmentRecordCancellationOperation,
		WorkspaceID: in.WorkspaceID, ActingUserID: in.ActingUserID, InvestmentID: in.InvestmentID,
		RequestID: requestID, Reason: reason,
		PreviewID: newInvestmentRecordCancellationPreviewID(), IssuedAt: issued, ExpiresAt: issued + investmentRecordCancellationTTLSeconds,
		PurchaseID: state.PurchaseID, InvestmentAccountID: state.InvestmentAccountID,
		AmountCents: state.Investment.PrincipalCents, EconomicDate: economic.String(), Timezone: in.Location.String(),
		InvestmentBalanceCents:   state.InvestmentBalanceCents,
		InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		EvidenceHash:             evidenceHash,
	}
	token, err := signInvestmentRecordCancellationClaims(investmentRecordCancellationPreviewKey, claims)
	if err != nil {
		return InvestmentRecordCancellationPreview{}, err
	}
	preview.PreviewID = claims.PreviewID
	preview.IssuedAt = claims.IssuedAt
	preview.ExpiresAt = claims.ExpiresAt
	preview.PreviewToken = token
	return preview, nil
}

// CancelInvestmentRecord confirms a signed preview under the runtime Immediate
// writer. Replay by request_id precedes every key- or version-dependent check.
// The confirmation writes exactly one row — the immutable receipt — with no
// transaction, no balance effect and no balance version bump; any failure
// rolls the whole effect back.
func CancelInvestmentRecord(ctx context.Context, db *sql.DB, in CancelInvestmentRecordInput) (InvestmentRecordCancellationResult, error) {
	requestID, reason, err := validateInvestmentRecordCancellationRequest(in.WorkspaceID, in.InvestmentID, in.CreatedBy, in.RequestID, in.Reason)
	if err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	if in.PreviewToken == "" || in.ExpectedInvestmentVersion < 0 {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: confirmação de cancelamento de registro incompleta", ErrInvestmentValidation)
	}
	receiptID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("record-cancellation\x00"+in.WorkspaceID+"\x00"+in.InvestmentID+"\x00"+in.CreatedBy+"\x00"+requestID)).String()
	payload, err := json.Marshal(investmentRecordCancellationPayload{
		Version: investmentRecordCancellationPayloadVersion, Operation: investmentRecordCancellationOperation, Confirmed: true,
		WorkspaceID: in.WorkspaceID, ActingUserID: in.CreatedBy, InvestmentID: in.InvestmentID,
		RequestID: requestID, Reason: reason, PreviewToken: in.PreviewToken,
	})
	if err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	defer tx.Rollback()
	// Replay-first: the persisted receipt is the only idempotency authority.
	// No signing key, expiry or version participates in this lookup.
	saved, savedPayload, err := readInvestmentRecordCancellationRow(ctx, tx, investmentRecordCancellationSelectWhere+` AND c.request_id=?`, in.WorkspaceID, requestID)
	if err == nil {
		if savedPayload != string(payload) {
			return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: este formulário já cancelou com outros dados; reabra o cancelamento do registro", ErrInvestmentConflict)
		}
		if err := tx.Commit(); err != nil {
			return InvestmentRecordCancellationResult{}, &InvestmentCommitError{err: err}
		}
		return InvestmentRecordCancellationResult{Cancel: saved, Outcome: InvestmentOperationReplayed}, nil
	}
	if !errors.Is(err, ErrInvestmentNotFound) {
		return InvestmentRecordCancellationResult{}, err
	}
	if _, _, err := readInvestmentRecordCancellationRow(ctx, tx, investmentRecordCancellationSelectWhere+` AND c.investment_id=?`, in.WorkspaceID, in.InvestmentID); err == nil {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: registro de aplicação já cancelado; consulte o recibo", ErrInvestmentConflict)
	} else if !errors.Is(err, ErrInvestmentNotFound) && !IsMissingInvestmentRecordCancellationsTable(err) {
		return InvestmentRecordCancellationResult{}, err
	}
	claims, err := parseInvestmentRecordCancellationToken(investmentRecordCancellationPreviewKey, in.PreviewToken)
	if err != nil {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: %v", ErrInvestmentRecordCancellationPreview, err)
	}
	// A signed preview of another workspace or investment is indistinguishable
	// from an unknown investment: no cross-tenant existence may leak.
	if claims.WorkspaceID != in.WorkspaceID || claims.InvestmentID != in.InvestmentID {
		return InvestmentRecordCancellationResult{}, ErrInvestmentNotFound
	}
	if claims.ActingUserID != in.CreatedBy || claims.RequestID != requestID || claims.Reason != reason {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: prévia pertence a outra requisição", ErrInvestmentRecordCancellationPreview)
	}
	if now := time.Now().Unix(); now < claims.IssuedAt || now >= claims.ExpiresAt || claims.ExpiresAt != claims.IssuedAt+investmentRecordCancellationTTLSeconds {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: prévia expirada; solicite outra", ErrInvestmentRecordCancellationPreview)
	}
	state, blockers, err := evaluateInvestmentRecordCancellationTx(ctx, tx, in.WorkspaceID, in.InvestmentID, claims.EconomicDate)
	if err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	if len(blockers) > 0 {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: %s", ErrInvestmentConflict, blockers[0])
	}
	if err := verifyInvestmentRecordCancellationClaims(claims, state); err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	if in.ExpectedInvestmentVersion != state.InvestmentBalanceVersion {
		return InvestmentRecordCancellationResult{}, ErrBalanceAdjustmentConflict
	}
	if _, err := civildate.ParseCivilDateInput(claims.EconomicDate); err != nil {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: data do registro da prévia inválida", ErrInvestmentRecordCancellationPreview)
	}
	// Temporal revalidation immediately before the commit: confirmed_at must
	// still fall inside the signed window (the schema CHECK is the last guard).
	confirmedAt := time.Now().Unix()
	if confirmedAt < claims.IssuedAt || confirmedAt >= claims.ExpiresAt {
		return InvestmentRecordCancellationResult{}, fmt.Errorf("%w: prévia expirada; solicite outra", ErrInvestmentRecordCancellationPreview)
	}
	// RECORD: no transfer, no balance effect, no version bump. The receipt
	// pins the state it observed without moving anything; the equal
	// before/after columns are the durable zero-effect witness.
	receipt := models.InvestmentRecordCancellation{
		WorkspaceID: in.WorkspaceID, ID: receiptID, InvestmentID: in.InvestmentID, PurchaseID: state.PurchaseID,
		InvestmentAccountID: state.InvestmentAccountID, AmountCents: state.Investment.PrincipalCents,
		InvestmentBeforeCents: state.InvestmentBalanceCents, InvestmentAfterCents: state.InvestmentBalanceCents,
		InvestmentBeforeVersion: state.InvestmentBalanceVersion, InvestmentAfterVersion: state.InvestmentBalanceVersion,
		CreatedBy: in.CreatedBy, ConfirmedAt: confirmedAt, EconomicDate: claims.EconomicDate, Reason: reason,
		RequestID: requestID, PreviewID: claims.PreviewID, PreviewIssuedAt: claims.IssuedAt, PreviewExpiresAt: claims.ExpiresAt,
		EvidenceHash: claims.EvidenceHash,
	}
	evidenceJSON, err := json.Marshal(investmentRecordCancellationReceiptEvidence{
		WorkspaceID: receipt.WorkspaceID, InvestmentID: receipt.InvestmentID, PurchaseID: receipt.PurchaseID,
		InvestmentAccountID: receipt.InvestmentAccountID, AmountCents: receipt.AmountCents, EconomicDate: receipt.EconomicDate,
		EvidenceHash: receipt.EvidenceHash, Timezone: claims.Timezone,
		InvestmentBalanceCents: state.InvestmentBalanceCents, InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		IssuedAt: claims.IssuedAt,
	})
	if err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investment_record_cancellations (`+investmentRecordCancellationColumns+`,payload_version,receipt_version,payload_json,evidence_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.WorkspaceID, receipt.ID, receipt.InvestmentID, receipt.PurchaseID,
		receipt.InvestmentAccountID, receipt.AmountCents,
		receipt.InvestmentBeforeCents, receipt.InvestmentAfterCents,
		receipt.InvestmentBeforeVersion, receipt.InvestmentAfterVersion,
		receipt.CreatedBy, receipt.ConfirmedAt, receipt.EconomicDate, receipt.Reason, receipt.RequestID,
		receipt.PreviewID, receipt.PreviewIssuedAt, receipt.PreviewExpiresAt, receipt.EvidenceHash,
		investmentRecordCancellationPayloadVersion, investmentRecordCancellationReceiptVersion, string(payload), string(evidenceJSON)); err != nil {
		return InvestmentRecordCancellationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		// The outcome is uncertain, not failed: consult the durable receipt
		// independently and recover it with the same idempotency key.
		if saved, savedPayload, lookupErr := investmentRecordCancellationRecoveryRead(ctx, db, in.WorkspaceID, requestID); lookupErr == nil && savedPayload == string(payload) {
			return InvestmentRecordCancellationResult{Cancel: saved, Outcome: InvestmentOperationReplayed}, nil
		}
		return InvestmentRecordCancellationResult{}, &InvestmentCommitError{err: err}
	}
	return InvestmentRecordCancellationResult{Cancel: receipt, Outcome: InvestmentOperationCreated}, nil
}

// GetInvestmentRecordCancellation resolves the receipt of one investment for
// projections and replays. A foreign workspace is indistinguishable from an
// unknown investment.
func GetInvestmentRecordCancellation(ctx context.Context, db *sql.DB, workspace, investment string) (models.InvestmentRecordCancellation, error) {
	if workspace == "" || investment == "" {
		return models.InvestmentRecordCancellation{}, ErrInvestmentNotFound
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentRecordCancellation{}, err
	}
	defer tx.Rollback()
	receipt, _, err := readInvestmentRecordCancellationRow(ctx, tx, investmentRecordCancellationSelectWhere+` AND c.investment_id=?`, workspace, investment)
	if err != nil {
		return models.InvestmentRecordCancellation{}, err
	}
	return receipt, nil
}

func investmentRecordCancellationRecoveryRead(ctx context.Context, db *sql.DB, workspace, requestID string) (models.InvestmentRecordCancellation, string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentRecordCancellation{}, "", err
	}
	defer tx.Rollback()
	return readInvestmentRecordCancellationRow(ctx, tx, investmentRecordCancellationSelectWhere+` AND c.request_id=?`, workspace, requestID)
}

const investmentRecordCancellationColumns = `workspace_id,id,investment_id,purchase_id,investment_account_id,amount_cents,investment_before_cents,investment_after_cents,investment_before_version,investment_after_version,created_by,confirmed_at,economic_date,reason,request_id,preview_id,preview_issued_at,preview_expires_at,evidence_hash`

const investmentRecordCancellationSelectWhere = `SELECT ` + investmentRecordCancellationColumns + `,c.payload_json FROM investment_record_cancellations c WHERE c.workspace_id=?`

func readInvestmentRecordCancellationRow(ctx context.Context, tx *sql.Tx, query string, args ...any) (models.InvestmentRecordCancellation, string, error) {
	var r models.InvestmentRecordCancellation
	var payload string
	err := tx.QueryRowContext(ctx, query, args...).Scan(
		&r.WorkspaceID, &r.ID, &r.InvestmentID, &r.PurchaseID, &r.InvestmentAccountID, &r.AmountCents,
		&r.InvestmentBeforeCents, &r.InvestmentAfterCents, &r.InvestmentBeforeVersion, &r.InvestmentAfterVersion,
		&r.CreatedBy, &r.ConfirmedAt, &r.EconomicDate, &r.Reason, &r.RequestID,
		&r.PreviewID, &r.PreviewIssuedAt, &r.PreviewExpiresAt, &r.EvidenceHash, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return models.InvestmentRecordCancellation{}, "", ErrInvestmentNotFound
	}
	if err != nil {
		return models.InvestmentRecordCancellation{}, "", err
	}
	return r, payload, nil
}

// verifyInvestmentRecordCancellationClaims recomputes the preview evidence
// under the writer lock and requires the signed claims to describe exactly
// the current state: any independent reconciliation, adoption or dependency
// added after the preview fails closed.
func verifyInvestmentRecordCancellationClaims(claims investmentRecordCancellationClaims, state investmentRecordCancellationState) error {
	evidenceHash, err := hashInvestmentRecordCancellationEvidence(state.Evidence)
	if err != nil {
		return err
	}
	if claims.PurchaseID != state.PurchaseID ||
		claims.InvestmentAccountID != state.InvestmentAccountID ||
		claims.AmountCents != state.Investment.PrincipalCents ||
		claims.InvestmentBalanceCents != state.InvestmentBalanceCents ||
		claims.InvestmentBalanceVersion != state.InvestmentBalanceVersion ||
		claims.EvidenceHash != evidenceHash {
		return fmt.Errorf("%w: estado do registro mudou desde a prévia; solicite outra", ErrInvestmentConflict)
	}
	return nil
}

func hashInvestmentRecordCancellationEvidence(evidence investmentRecordCancellationEvidence) (string, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newInvestmentRecordCancellationPreviewID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("contabase: investment record cancellation preview id: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func signInvestmentRecordCancellationClaims(key []byte, claims investmentRecordCancellationClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseInvestmentRecordCancellationToken(key []byte, token string) (investmentRecordCancellationClaims, error) {
	var claims investmentRecordCancellationClaims
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
	if claims.Version != investmentRecordCancellationPayloadVersion || claims.Operation != investmentRecordCancellationOperation {
		return claims, errors.New("operação desconhecida")
	}
	return claims, nil
}

// evaluateInvestmentRecordCancellationTx mirrors the schema 81 insert trigger
// as a service-level check, so the preview can explain blockers and the
// confirmation can fail with typed errors. Independent movements on the
// investment account are deliberately not blockers: nothing moves, and only
// the application's own later operations and dependencies are.
func evaluateInvestmentRecordCancellationTx(ctx context.Context, tx *sql.Tx, workspace, investment, economicDate string) (investmentRecordCancellationState, []string, error) {
	state := investmentRecordCancellationState{}
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
		blockers = append(blockers, "compra inicial ausente")
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
	// The presence of the schema-77 purchase transfer defines the movement
	// family: a funded lot belongs to the schema 79 integral cancellation and
	// is out of scope here by construction.
	var funded int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE workspace_id=? AND id=?`, workspace, InvestmentPurchaseTransferPrefix+purchase.ID).Scan(&funded); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "transactions", Column: "purchase_transfer", Count: funded})
	if funded != 0 {
		blockers = append(blockers, "aplicação possui transferência automática vinculada")
	}
	var cancelled int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&cancelled); err != nil {
		if !IsMissingInvestmentCancellationsTable(err) {
			return state, nil, err
		}
		cancelled = 0
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_cancellations", Count: cancelled})
	if cancelled != 0 {
		blockers = append(blockers, "aplicação possui cancelamento confirmado")
	}
	var recordCancelled int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_record_cancellations WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&recordCancelled); err != nil {
		if !IsMissingInvestmentRecordCancellationsTable(err) {
			return state, nil, err
		}
		recordCancelled = 0
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_record_cancellations", Count: recordCancelled})
	if recordCancelled != 0 {
		blockers = append(blockers, "aplicação possui cancelamento confirmado")
	}
	var reversed int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_purchase_reversals WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&reversed); err != nil {
		if !IsMissingInvestmentPurchaseReversalsTable(err) {
			return state, nil, err
		}
		reversed = 0
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_purchase_reversals", Count: reversed})
	if reversed != 0 {
		blockers = append(blockers, "compra já estornada")
	}
	if err := readInvestmentCancellationAccountTx(ctx, tx, workspace, state.Investment.AccountID, models.AccountTypeInvestment, &state.Evidence.InvestmentAccount, &state.InvestmentAccountName); err != nil {
		if errors.Is(err, ErrInvestmentConflict) {
			blockers = append(blockers, "conta de investimento inválida ou arquivada")
			state.Evidence.InvestmentAccount = investmentEvidenceAccount{ID: state.Investment.AccountID}
		} else {
			return state, nil, err
		}
	}
	if purchase.PurchaseDate != "" && purchase.PurchaseDate > economicDate {
		blockers = append(blockers, "data do registro anterior à aplicação")
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_balance_adjustments WHERE workspace_id=? AND account_id=?`, workspace, state.Investment.AccountID).Scan(&adjustments); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "account_balance_adjustments", Count: adjustments})
	if adjustments != 0 {
		blockers = append(blockers, "conciliação presente na conta de investimento")
	}
	var adoptions int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_adoption_confirmations WHERE workspace_id=? AND account_id=?`, workspace, state.Investment.AccountID).Scan(&adoptions); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_adoption_confirmations", Column: "account_id", Count: adoptions})
	if adoptions != 0 {
		blockers = append(blockers, "conta de investimento possui declaração de adoção")
	}
	state.InvestmentBalanceCents = state.Evidence.InvestmentAccount.CurrentBalance
	state.InvestmentBalanceVersion = state.Evidence.InvestmentAccount.BalanceVersion
	return state, blockers, nil
}
