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

// Individual reversal of one non-initial purchase (schema 80). The receipts
// mirror the homologated schema 79 contract: preview HMAC with a 900 second
// TTL, replay-first confirmation under the Immediate writer, exactly one
// ApplyBalanceEffect for financed purchases and none for record-only ones, and
// an immutable receipt as the only idempotency authority. The signed preview
// is only an authorization carrier and never outlives its process.

const (
	investmentReversalOperation      = "REVERSE_INVESTMENT_PURCHASE"
	investmentReversalTTLSeconds     = 900
	investmentReversalPayloadVersion = 1
	investmentReversalReceiptVersion = 1
	investmentReversalReasonMaxRunes = 500
)

var ErrInvestmentReversalPreview = errors.New("prévia de estorno inválida ou expirada")

// IsMissingInvestmentPurchaseReversalsTable reports whether err is only the
// absence of the schema 80 receipt table. A pre-80 database holds no reversal
// by construction: read paths fall back to "nothing reversed" and migration
// 80 itself introduces the table and its triggers.
func IsMissingInvestmentPurchaseReversalsTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: investment_purchase_reversals")
}

// The preview signing key is random per process and never persisted, logged
// or exported. A restart silently invalidates every unconfirmed preview;
// replaying an already confirmed reversal reads the persisted receipt and
// never touches this key.
var investmentReversalPreviewKey = newInvestmentReversalPreviewKey()

func newInvestmentReversalPreviewKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("contabase: investment reversal preview key: " + err.Error())
	}
	return key
}

type investmentReversalClaims struct {
	Version                  int64  `json:"version"`
	Operation                string `json:"operation"`
	WorkspaceID              string `json:"workspace_id"`
	ActingUserID             string `json:"acting_user_id"`
	InvestmentID             string `json:"investment_id"`
	PurchaseID               string `json:"purchase_id"`
	RequestID                string `json:"request_id"`
	Reason                   string `json:"reason"`
	PreviewID                string `json:"preview_id"`
	IssuedAt                 int64  `json:"issued_at"`
	ExpiresAt                int64  `json:"expires_at"`
	Movement                 string `json:"movement"`
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

type investmentReversalPayload struct {
	Version      int64  `json:"version"`
	Operation    string `json:"operation"`
	Confirmed    bool   `json:"confirmed"`
	WorkspaceID  string `json:"workspace_id"`
	ActingUserID string `json:"acting_user_id"`
	InvestmentID string `json:"investment_id"`
	PurchaseID   string `json:"purchase_id"`
	RequestID    string `json:"request_id"`
	Reason       string `json:"reason"`
	PreviewToken string `json:"preview_token"`
}

type investmentReversalReceiptEvidence struct {
	WorkspaceID              string `json:"workspace_id"`
	InvestmentID             string `json:"investment_id"`
	PurchaseID               string `json:"purchase_id"`
	Movement                 string `json:"movement"`
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

// The financial snapshot behind evidence_hash. No display names: renaming an
// account or investment must not invalidate a preview. Slice order is fixed
// so the hash is stable. For a RECORD reversal the transfer and origin
// account blocks stay zeroed.
type investmentReversalEvidence struct {
	Investment        investmentEvidenceInvestment
	Purchase          investmentEvidencePurchase
	OriginalTransfer  investmentEvidenceTransfer
	InvestmentAccount investmentEvidenceAccount
	OriginAccount     investmentEvidenceAccount
	Dependents        []investmentEvidenceCount
}

type investmentReversalState struct {
	Investment               models.Investment
	Purchase                 models.InvestmentPurchase
	Movement                 string
	OriginalTransactionID    string
	InvestmentAccountID      string
	InvestmentAccountName    string
	OriginAccountID          string
	OriginAccountName        string
	InvestmentBalanceCents   int64
	InvestmentBalanceVersion int64
	OriginBalanceCents       int64
	OriginBalanceVersion     int64
	Evidence                 investmentReversalEvidence
}

type InvestmentReversalPreviewInput struct {
	WorkspaceID  string
	InvestmentID string
	PurchaseID   string
	ActingUserID string
	RequestID    string
	Reason       string
	Location     *time.Location
}

type InvestmentReversalPreview struct {
	Eligible                 bool
	Blockers                 []string
	Movement                 string
	AmountCents              int64
	PurchaseDate             string
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

type ReverseInvestmentPurchaseInput struct {
	WorkspaceID               string
	InvestmentID              string
	PurchaseID                string
	CreatedBy                 string
	RequestID                 string
	Reason                    string
	PreviewToken              string
	ExpectedSourceVersion     int64
	ExpectedInvestmentVersion int64
}

type InvestmentReversalResult struct {
	Reversal models.InvestmentPurchaseReversal
	Outcome  InvestmentOperationOutcome
}

func validateInvestmentReversalRequest(workspace, investment, purchase, user, requestID, reason string) (string, string, error) {
	if workspace == "" || investment == "" || purchase == "" || user == "" {
		return "", "", fmt.Errorf("%w: contexto de estorno incompleto", ErrInvestmentValidation)
	}
	key, err := uuid.Parse(requestID)
	if err != nil {
		return "", "", fmt.Errorf("%w: formulário inválido; reabra o estorno", ErrInvestmentValidation)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > investmentReversalReasonMaxRunes {
		return "", "", fmt.Errorf("%w: motivo deve ter de 1 a %d caracteres", ErrInvestmentValidation, investmentReversalReasonMaxRunes)
	}
	return key.String(), reason, nil
}

// PreviewInvestmentPurchaseReversal resolves eligibility without writing and,
// when eligible, issues a self-contained HMAC-SHA256 signed token valid for
// 900 seconds. The preview binds the request payload, the purchase identity,
// the resolved movement and the financial evidence snapshot; any drift before
// confirmation fails closed.
func PreviewInvestmentPurchaseReversal(ctx context.Context, db *sql.DB, in InvestmentReversalPreviewInput) (InvestmentReversalPreview, error) {
	requestID, reason, err := validateInvestmentReversalRequest(in.WorkspaceID, in.InvestmentID, in.PurchaseID, in.ActingUserID, in.RequestID, in.Reason)
	if err != nil {
		return InvestmentReversalPreview{}, err
	}
	if in.Location == nil {
		return InvestmentReversalPreview{}, fmt.Errorf("%w: fuso horário obrigatório para a data econômica", ErrInvestmentValidation)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentReversalPreview{}, err
	}
	defer tx.Rollback()
	economic := civildate.CivilDateToday(in.Location)
	state, blockers, err := evaluateInvestmentPurchaseReversalTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.PurchaseID, economic.String())
	if err != nil {
		return InvestmentReversalPreview{}, err
	}
	preview := InvestmentReversalPreview{
		Blockers:                 blockers,
		Movement:                 state.Movement,
		AmountCents:              state.Purchase.PrincipalCents,
		PurchaseDate:             state.Purchase.PurchaseDate,
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
	if state.Movement == "TRANSFER" {
		if err := previewInvestmentReversalOverflow(state); err != nil {
			preview.Blockers = append(preview.Blockers, "compensação deixaria saldo fora do intervalo permitido")
			return preview, nil
		}
		preview.InvestmentAfterCents = state.InvestmentBalanceCents - state.Purchase.PrincipalCents
		preview.OriginAfterCents = state.OriginBalanceCents + state.Purchase.PrincipalCents
	}
	preview.Eligible = true
	evidenceHash, err := hashInvestmentReversalEvidence(state.Evidence)
	if err != nil {
		return InvestmentReversalPreview{}, err
	}
	issued := time.Now().Unix()
	claims := investmentReversalClaims{
		Version: investmentReversalPayloadVersion, Operation: investmentReversalOperation,
		WorkspaceID: in.WorkspaceID, ActingUserID: in.ActingUserID, InvestmentID: in.InvestmentID,
		PurchaseID: in.PurchaseID, RequestID: requestID, Reason: reason,
		PreviewID: newInvestmentReversalPreviewID(), IssuedAt: issued, ExpiresAt: issued + investmentReversalTTLSeconds,
		Movement: state.Movement, OriginalTransactionID: state.OriginalTransactionID,
		InvestmentAccountID: state.InvestmentAccountID, OriginAccountID: state.OriginAccountID,
		AmountCents: state.Purchase.PrincipalCents, EconomicDate: economic.String(), Timezone: in.Location.String(),
		InvestmentBalanceCents: state.InvestmentBalanceCents, InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		OriginBalanceCents: state.OriginBalanceCents, OriginBalanceVersion: state.OriginBalanceVersion,
		EvidenceHash: evidenceHash,
	}
	token, err := signInvestmentReversalClaims(investmentReversalPreviewKey, claims)
	if err != nil {
		return InvestmentReversalPreview{}, err
	}
	preview.PreviewID = claims.PreviewID
	preview.IssuedAt = claims.IssuedAt
	preview.ExpiresAt = claims.ExpiresAt
	preview.PreviewToken = token
	return preview, nil
}

// ReverseInvestmentPurchase confirms a signed preview under the runtime
// Immediate writer. Replay by request_id precedes every key- or
// version-dependent check. A TRANSFER reversal writes the compensating
// transfer, exactly one ApplyBalanceEffect and the immutable receipt
// atomically; a RECORD reversal writes only the receipt, with no transfer and
// no balance effect. Any failure rolls the whole effect back.
func ReverseInvestmentPurchase(ctx context.Context, db *sql.DB, in ReverseInvestmentPurchaseInput) (InvestmentReversalResult, error) {
	requestID, reason, err := validateInvestmentReversalRequest(in.WorkspaceID, in.InvestmentID, in.PurchaseID, in.CreatedBy, in.RequestID, in.Reason)
	if err != nil {
		return InvestmentReversalResult{}, err
	}
	if in.PreviewToken == "" || in.ExpectedSourceVersion < 0 || in.ExpectedInvestmentVersion < 0 {
		return InvestmentReversalResult{}, fmt.Errorf("%w: confirmação de estorno incompleta", ErrInvestmentValidation)
	}
	receiptID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("reversal\x00"+in.WorkspaceID+"\x00"+in.InvestmentID+"\x00"+in.PurchaseID+"\x00"+in.CreatedBy+"\x00"+requestID)).String()
	payload, err := json.Marshal(investmentReversalPayload{
		Version: investmentReversalPayloadVersion, Operation: investmentReversalOperation, Confirmed: true,
		WorkspaceID: in.WorkspaceID, ActingUserID: in.CreatedBy, InvestmentID: in.InvestmentID,
		PurchaseID: in.PurchaseID, RequestID: requestID, Reason: reason, PreviewToken: in.PreviewToken,
	})
	if err != nil {
		return InvestmentReversalResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return InvestmentReversalResult{}, err
	}
	defer tx.Rollback()
	// Replay-first: the persisted receipt is the only idempotency authority.
	// No signing key, expiry or version participates in this lookup.
	saved, savedPayload, err := readInvestmentReversalRow(ctx, tx, investmentReversalSelectWhere+` AND r.request_id=?`, in.WorkspaceID, requestID)
	if err == nil {
		if savedPayload != string(payload) {
			return InvestmentReversalResult{}, fmt.Errorf("%w: este formulário já estornou outra compra; reabra o estorno", ErrInvestmentConflict)
		}
		if err := tx.Commit(); err != nil {
			return InvestmentReversalResult{}, &InvestmentCommitError{err: err}
		}
		return InvestmentReversalResult{Reversal: saved, Outcome: InvestmentOperationReplayed}, nil
	}
	if !errors.Is(err, ErrInvestmentNotFound) {
		return InvestmentReversalResult{}, err
	}
	if _, _, err := readInvestmentReversalRow(ctx, tx, investmentReversalSelectWhere+` AND r.purchase_id=?`, in.WorkspaceID, in.PurchaseID); err == nil {
		return InvestmentReversalResult{}, fmt.Errorf("%w: compra já estornada; consulte o recibo", ErrInvestmentConflict)
	} else if !errors.Is(err, ErrInvestmentNotFound) {
		return InvestmentReversalResult{}, err
	}
	claims, err := parseInvestmentReversalToken(investmentReversalPreviewKey, in.PreviewToken)
	if err != nil {
		return InvestmentReversalResult{}, fmt.Errorf("%w: %v", ErrInvestmentReversalPreview, err)
	}
	// A signed preview of another workspace, investment or purchase is
	// indistinguishable from an unknown purchase: no cross-tenant existence
	// may leak.
	if claims.WorkspaceID != in.WorkspaceID || claims.InvestmentID != in.InvestmentID || claims.PurchaseID != in.PurchaseID {
		return InvestmentReversalResult{}, ErrInvestmentNotFound
	}
	if claims.ActingUserID != in.CreatedBy || claims.RequestID != requestID || claims.Reason != reason {
		return InvestmentReversalResult{}, fmt.Errorf("%w: prévia pertence a outra requisição", ErrInvestmentReversalPreview)
	}
	if now := time.Now().Unix(); now < claims.IssuedAt || now >= claims.ExpiresAt || claims.ExpiresAt != claims.IssuedAt+investmentReversalTTLSeconds {
		return InvestmentReversalResult{}, fmt.Errorf("%w: prévia expirada; solicite outra", ErrInvestmentReversalPreview)
	}
	state, blockers, err := evaluateInvestmentPurchaseReversalTx(ctx, tx, in.WorkspaceID, in.InvestmentID, in.PurchaseID, claims.EconomicDate)
	if err != nil {
		return InvestmentReversalResult{}, err
	}
	if len(blockers) > 0 {
		return InvestmentReversalResult{}, fmt.Errorf("%w: %s", ErrInvestmentConflict, blockers[0])
	}
	if err := verifyInvestmentReversalClaims(claims, state); err != nil {
		return InvestmentReversalResult{}, err
	}
	if in.ExpectedSourceVersion != state.OriginBalanceVersion || in.ExpectedInvestmentVersion != state.InvestmentBalanceVersion {
		return InvestmentReversalResult{}, ErrBalanceAdjustmentConflict
	}
	if state.Movement == "TRANSFER" {
		if err := previewInvestmentReversalOverflow(state); err != nil {
			return InvestmentReversalResult{}, err
		}
	}
	economic, err := civildate.ParseCivilDateInput(claims.EconomicDate)
	if err != nil {
		return InvestmentReversalResult{}, fmt.Errorf("%w: data econômica da prévia inválida", ErrInvestmentReversalPreview)
	}
	// Temporal revalidation immediately before the commit: confirmed_at must
	// still fall inside the signed window (the schema CHECK is the last guard).
	confirmedAt := time.Now().Unix()
	if confirmedAt < claims.IssuedAt || confirmedAt >= claims.ExpiresAt {
		return InvestmentReversalResult{}, fmt.Errorf("%w: prévia expirada; solicite outra", ErrInvestmentReversalPreview)
	}
	compensationID := ""
	if state.Movement == "TRANSFER" {
		compensationID = InvestmentReversalTransferPrefix + receiptID
		at := civildate.CivilDateToLegacyUnixNoonUTC(economic)
		if _, err = tx.ExecContext(ctx, `INSERT INTO transactions(id,workspace_id,user_id,account_id,destination_account_id,type,amount,date,date_civil,paid_at,paid_date_civil,status,description,created_at,updated_at)
	VALUES(?,?,?,?,?,'TRANSFER',?,?,?,?,?,'paid','Estorno de compra de investimento',?,?)`,
			compensationID, in.WorkspaceID, in.CreatedBy, state.InvestmentAccountID, state.OriginAccountID,
			state.Purchase.PrincipalCents, at, claims.EconomicDate, at, claims.EconomicDate, confirmedAt, confirmedAt); err != nil {
			return InvestmentReversalResult{}, err
		}
		if err = ApplyBalanceEffect(tx, in.WorkspaceID, models.TransactionTypeTransfer, models.AccountTypeInvestment, models.PaymentStatusPaid, state.Purchase.PrincipalCents, state.InvestmentAccountID, state.OriginAccountID, confirmedAt); err != nil {
			return InvestmentReversalResult{}, err
		}
	}
	var investmentAfter, investmentAfterVersion, originAfter, originAfterVersion int64
	if state.Movement == "TRANSFER" {
		if err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, state.InvestmentAccountID).Scan(&investmentAfter, &investmentAfterVersion); err != nil {
			return InvestmentReversalResult{}, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT current_balance,balance_version FROM accounts WHERE workspace_id=? AND id=?`, in.WorkspaceID, state.OriginAccountID).Scan(&originAfter, &originAfterVersion); err != nil {
			return InvestmentReversalResult{}, err
		}
		if investmentAfter != state.InvestmentBalanceCents-state.Purchase.PrincipalCents || investmentAfterVersion != state.InvestmentBalanceVersion+1 ||
			originAfter != state.OriginBalanceCents+state.Purchase.PrincipalCents || originAfterVersion != state.OriginBalanceVersion+1 {
			return InvestmentReversalResult{}, fmt.Errorf("%w: efeito de saldo divergiu durante o estorno", ErrInvestmentConflict)
		}
	} else {
		// RECORD: no transfer, no balance effect. The receipt pins the state
		// it observed without moving anything.
		investmentAfter, investmentAfterVersion = state.InvestmentBalanceCents, state.InvestmentBalanceVersion
		originAfter, originAfterVersion = state.OriginBalanceCents, state.OriginBalanceVersion
	}
	receipt := models.InvestmentPurchaseReversal{
		WorkspaceID: in.WorkspaceID, ID: receiptID, InvestmentID: in.InvestmentID, PurchaseID: in.PurchaseID,
		Movement: state.Movement, OriginalTransactionID: state.OriginalTransactionID, CompensationTransactionID: compensationID,
		InvestmentAccountID: state.InvestmentAccountID, OriginAccountID: state.OriginAccountID,
		AmountCents:           state.Purchase.PrincipalCents,
		InvestmentBeforeCents: state.InvestmentBalanceCents, InvestmentAfterCents: investmentAfter,
		OriginBeforeCents: state.OriginBalanceCents, OriginAfterCents: originAfter,
		InvestmentBeforeVersion: state.InvestmentBalanceVersion, InvestmentAfterVersion: investmentAfterVersion,
		OriginBeforeVersion: state.OriginBalanceVersion, OriginAfterVersion: originAfterVersion,
		CreatedBy: in.CreatedBy, ConfirmedAt: confirmedAt, EconomicDate: claims.EconomicDate, Reason: reason,
		RequestID: requestID, PreviewID: claims.PreviewID, PreviewIssuedAt: claims.IssuedAt, PreviewExpiresAt: claims.ExpiresAt,
		EvidenceHash: claims.EvidenceHash,
	}
	evidenceJSON, err := json.Marshal(investmentReversalReceiptEvidence{
		WorkspaceID: receipt.WorkspaceID, InvestmentID: receipt.InvestmentID, PurchaseID: receipt.PurchaseID,
		Movement: receipt.Movement, OriginalTransactionID: receipt.OriginalTransactionID,
		InvestmentAccountID: receipt.InvestmentAccountID, OriginAccountID: receipt.OriginAccountID,
		AmountCents: receipt.AmountCents, EconomicDate: receipt.EconomicDate,
		EvidenceHash: receipt.EvidenceHash, Timezone: claims.Timezone,
		InvestmentBalanceCents: state.InvestmentBalanceCents, InvestmentBalanceVersion: state.InvestmentBalanceVersion,
		OriginBalanceCents: state.OriginBalanceCents, OriginBalanceVersion: state.OriginBalanceVersion,
		IssuedAt: claims.IssuedAt,
	})
	if err != nil {
		return InvestmentReversalResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO investment_purchase_reversals (`+investmentReversalColumns+`,payload_version,receipt_version,payload_json,evidence_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		receipt.WorkspaceID, receipt.ID, receipt.InvestmentID, receipt.PurchaseID, receipt.Movement,
		receipt.OriginalTransactionID, receipt.CompensationTransactionID, receipt.InvestmentAccountID, receipt.OriginAccountID, receipt.AmountCents,
		receipt.InvestmentBeforeCents, receipt.InvestmentAfterCents, receipt.OriginBeforeCents, receipt.OriginAfterCents,
		receipt.InvestmentBeforeVersion, receipt.InvestmentAfterVersion, receipt.OriginBeforeVersion, receipt.OriginAfterVersion,
		receipt.CreatedBy, receipt.ConfirmedAt, receipt.EconomicDate, receipt.Reason, receipt.RequestID,
		receipt.PreviewID, receipt.PreviewIssuedAt, receipt.PreviewExpiresAt, receipt.EvidenceHash,
		investmentReversalPayloadVersion, investmentReversalReceiptVersion, string(payload), string(evidenceJSON)); err != nil {
		return InvestmentReversalResult{}, err
	}
	if err = tx.Commit(); err != nil {
		// The outcome is uncertain, not failed: consult the durable receipt
		// independently and recover it with the same idempotency key.
		if saved, savedPayload, lookupErr := investmentReversalRecoveryRead(ctx, db, in.WorkspaceID, requestID); lookupErr == nil && savedPayload == string(payload) {
			return InvestmentReversalResult{Reversal: saved, Outcome: InvestmentOperationReplayed}, nil
		}
		return InvestmentReversalResult{}, &InvestmentCommitError{err: err}
	}
	return InvestmentReversalResult{Reversal: receipt, Outcome: InvestmentOperationCreated}, nil
}

// GetInvestmentPurchaseReversals resolves the receipts of one investment for
// the detail page and eligibility projections. A foreign workspace is
// indistinguishable from an unknown investment.
func GetInvestmentPurchaseReversals(ctx context.Context, db *sql.DB, workspace, investment string) (map[string]models.InvestmentPurchaseReversal, error) {
	reversals := make(map[string]models.InvestmentPurchaseReversal)
	if workspace == "" || investment == "" {
		return reversals, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT `+investmentReversalColumns+` FROM investment_purchase_reversals WHERE workspace_id=? AND investment_id=?`, workspace, investment)
	if err != nil {
		if IsMissingInvestmentPurchaseReversalsTable(err) {
			return reversals, nil
		}
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanInvestmentReversal(rows.Scan)
		if err != nil {
			return nil, err
		}
		reversals[r.PurchaseID] = r
	}
	return reversals, rows.Err()
}

func investmentReversalRecoveryRead(ctx context.Context, db *sql.DB, workspace, requestID string) (models.InvestmentPurchaseReversal, string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return models.InvestmentPurchaseReversal{}, "", err
	}
	defer tx.Rollback()
	return readInvestmentReversalRow(ctx, tx, investmentReversalSelectWhere+` AND r.request_id=?`, workspace, requestID)
}

// readInvestmentReversalPurchaseIDsTx marks which purchases of one investment
// have a confirmed reversal, inside the caller's read snapshot. A pre-80
// database holds none by construction.
func readInvestmentReversalPurchaseIDsTx(ctx context.Context, tx *sql.Tx, workspace, investment string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT purchase_id FROM investment_purchase_reversals WHERE workspace_id=? AND investment_id=?`, workspace, investment)
	if err != nil {
		if IsMissingInvestmentPurchaseReversalsTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var reversed map[string]bool
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if reversed == nil {
			reversed = make(map[string]bool)
		}
		reversed[id] = true
	}
	return reversed, rows.Err()
}

const investmentReversalColumns = `workspace_id,id,investment_id,purchase_id,movement,original_transaction_id,compensation_transaction_id,investment_account_id,origin_account_id,amount_cents,investment_before_cents,investment_after_cents,origin_before_cents,origin_after_cents,investment_before_version,investment_after_version,origin_before_version,origin_after_version,created_by,confirmed_at,economic_date,reason,request_id,preview_id,preview_issued_at,preview_expires_at,evidence_hash`

const investmentReversalSelectWhere = `SELECT ` + investmentReversalColumns + `,r.payload_json FROM investment_purchase_reversals r WHERE r.workspace_id=?`

func scanInvestmentReversal(scan func(...any) error) (models.InvestmentPurchaseReversal, error) {
	var r models.InvestmentPurchaseReversal
	err := scan(&r.WorkspaceID, &r.ID, &r.InvestmentID, &r.PurchaseID, &r.Movement, &r.OriginalTransactionID, &r.CompensationTransactionID,
		&r.InvestmentAccountID, &r.OriginAccountID, &r.AmountCents,
		&r.InvestmentBeforeCents, &r.InvestmentAfterCents, &r.OriginBeforeCents, &r.OriginAfterCents,
		&r.InvestmentBeforeVersion, &r.InvestmentAfterVersion, &r.OriginBeforeVersion, &r.OriginAfterVersion,
		&r.CreatedBy, &r.ConfirmedAt, &r.EconomicDate, &r.Reason, &r.RequestID,
		&r.PreviewID, &r.PreviewIssuedAt, &r.PreviewExpiresAt, &r.EvidenceHash)
	return r, err
}

func readInvestmentReversalRow(ctx context.Context, tx *sql.Tx, query string, args ...any) (models.InvestmentPurchaseReversal, string, error) {
	var payload string
	r, err := scanInvestmentReversal(func(dest ...any) error {
		err := tx.QueryRowContext(ctx, query, args...).Scan(append(dest, &payload)...)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return models.InvestmentPurchaseReversal{}, "", ErrInvestmentNotFound
	}
	if err != nil {
		return models.InvestmentPurchaseReversal{}, "", err
	}
	return r, payload, nil
}

// verifyInvestmentReversalClaims recomputes the preview evidence under the
// writer lock and requires the signed claims to describe exactly the current
// state: any independent movement, reconciliation or dependency added after
// the preview fails closed.
func verifyInvestmentReversalClaims(claims investmentReversalClaims, state investmentReversalState) error {
	evidenceHash, err := hashInvestmentReversalEvidence(state.Evidence)
	if err != nil {
		return err
	}
	if claims.PurchaseID != state.Purchase.ID ||
		claims.Movement != state.Movement ||
		claims.OriginalTransactionID != state.OriginalTransactionID ||
		claims.InvestmentAccountID != state.InvestmentAccountID ||
		claims.OriginAccountID != state.OriginAccountID ||
		claims.AmountCents != state.Purchase.PrincipalCents ||
		claims.InvestmentBalanceCents != state.InvestmentBalanceCents ||
		claims.InvestmentBalanceVersion != state.InvestmentBalanceVersion ||
		claims.OriginBalanceCents != state.OriginBalanceCents ||
		claims.OriginBalanceVersion != state.OriginBalanceVersion ||
		claims.EvidenceHash != evidenceHash {
		return fmt.Errorf("%w: estado financeiro mudou desde a prévia; solicite outra", ErrInvestmentConflict)
	}
	return nil
}

func previewInvestmentReversalOverflow(state investmentReversalState) error {
	amount := state.Purchase.PrincipalCents
	if state.OriginBalanceCents > math.MaxInt64-amount || state.InvestmentBalanceCents < math.MinInt64+amount {
		return ErrBalanceOutOfRange
	}
	if state.OriginBalanceVersion >= math.MaxInt64 || state.InvestmentBalanceVersion >= math.MaxInt64 {
		return ErrBalanceOutOfRange
	}
	return nil
}

func hashInvestmentReversalEvidence(evidence investmentReversalEvidence) (string, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newInvestmentReversalPreviewID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("contabase: investment reversal preview id: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func signInvestmentReversalClaims(key []byte, claims investmentReversalClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func parseInvestmentReversalToken(key []byte, token string) (investmentReversalClaims, error) {
	var claims investmentReversalClaims
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
	if claims.Version != investmentReversalPayloadVersion || claims.Operation != investmentReversalOperation {
		return claims, errors.New("operação desconhecida")
	}
	return claims, nil
}

// The dependent tables mirrored from the schema 80 trigger. Every count must
// stay zero for the investment; withdrawals enter only through allocations to
// this purchase and positions only from the purchase date on.
var investmentReversalDependentTables = []string{
	"investment_reference_revisions", "investment_reference_coverage", "investment_financial_revisions",
	"investment_financial_adjustments", "investment_purchase_funding", "investment_settlement_revisions",
	"investment_settlement_receipts", "investment_settlement_applications", "investment_result_classifications",
	"investment_adoption_items",
}

// evaluateInvestmentPurchaseReversalTx mirrors the schema 80 insert trigger as
// a service-level check, so the preview can explain blockers and the
// confirmation can fail with typed errors. Multiple purchases are NOT a
// blocker: independent later purchases stay untouched. The initial purchase
// is reserved for the full cancellation of schema 79.
func evaluateInvestmentPurchaseReversalTx(ctx context.Context, tx *sql.Tx, workspace, investment, purchaseID, economicDate string) (investmentReversalState, []string, error) {
	state := investmentReversalState{}
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
	purchase, err := scanInvestmentPurchase(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+purchaseColumns+` FROM investment_purchases WHERE workspace_id=? AND investment_id=? AND id=?`, workspace, investment, purchaseID).Scan(dest...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, ErrInvestmentNotFound
	}
	if err != nil {
		return state, nil, err
	}
	state.Purchase = purchase
	state.Evidence.Purchase = investmentEvidencePurchase{
		ID: purchase.ID, PurchaseDate: purchase.PurchaseDate, PrincipalCents: purchase.PrincipalCents,
		IsInitial: purchase.IsInitial, CreatedBy: purchase.CreatedBy, CreatedAt: purchase.CreatedAt,
	}
	if purchase.IsInitial {
		blockers = append(blockers, "compra inicial não pode ser estornada individualmente")
	}
	if err := rejectCancelledInvestmentTx(ctx, tx, workspace, investment); err != nil {
		if errors.Is(err, ErrInvestmentCancelled) {
			blockers = append(blockers, "aplicação possui cancelamento confirmado")
		} else {
			return state, nil, err
		}
	}
	if _, _, err := readInvestmentReversalRow(ctx, tx, investmentReversalSelectWhere+` AND r.purchase_id=?`, workspace, purchaseID); err == nil {
		blockers = append(blockers, "compra já estornada")
	} else if !errors.Is(err, ErrInvestmentNotFound) && !IsMissingInvestmentPurchaseReversalsTable(err) {
		return state, nil, err
	}
	// The presence of the schema-77 purchase receipt defines the movement: a
	// financed purchase must invert its transfer; a record-only purchase has
	// no transfer to invert and no balance to touch.
	transfer := investmentEvidenceTransfer{ID: InvestmentPurchaseTransferPrefix + purchaseID}
	err = tx.QueryRowContext(ctx, `SELECT type,status,effect_mode,user_id,account_id,destination_account_id,amount,date,date_civil,paid_at,paid_date_civil FROM transactions WHERE workspace_id=? AND id=?`, workspace, transfer.ID).
		Scan(&transfer.Type, &transfer.Status, &transfer.EffectMode, &transfer.UserID, &transfer.AccountID, &transfer.DestinationAccountID, &transfer.Amount, &transfer.Date, &transfer.DateCivil, &transfer.PaidAt, &transfer.PaidDateCivil)
	if errors.Is(err, sql.ErrNoRows) {
		state.Movement = "RECORD"
		err = nil
	} else if err == nil {
		state.Movement = "TRANSFER"
	}
	if err != nil {
		return state, nil, err
	}
	if state.Movement == "TRANSFER" {
		state.OriginalTransactionID = transfer.ID
		state.OriginAccountID = transfer.AccountID
		noonOK := false
		if civil, civilErr := civildate.ParseCivilDateInput(purchase.PurchaseDate); civilErr == nil {
			noon := civildate.CivilDateToLegacyUnixNoonUTC(civil)
			noonOK = transfer.Type == models.TransactionTypeTransfer && transfer.Status == models.PaymentStatusPaid &&
				transfer.EffectMode == models.TransactionEffectStandard && transfer.UserID == purchase.CreatedBy &&
				transfer.AccountID != "" && transfer.DestinationAccountID == state.Investment.AccountID &&
				transfer.Amount == purchase.PrincipalCents && transfer.DateCivil == purchase.PurchaseDate &&
				transfer.PaidDateCivil == purchase.PurchaseDate && transfer.Date == noon && transfer.PaidAt == transfer.Date
		}
		if !noonOK {
			blockers = append(blockers, "transferência vinculada divergente")
		}
	}
	if err := readInvestmentCancellationAccountTx(ctx, tx, workspace, state.Investment.AccountID, models.AccountTypeInvestment, &state.Evidence.InvestmentAccount, &state.InvestmentAccountName); err != nil {
		if errors.Is(err, ErrInvestmentConflict) {
			blockers = append(blockers, "conta de investimento inválida ou arquivada")
			state.Evidence.InvestmentAccount = investmentEvidenceAccount{ID: state.Investment.AccountID}
		} else {
			return state, nil, err
		}
	}
	if state.Movement == "TRANSFER" {
		if err := readInvestmentCancellationAccountTx(ctx, tx, workspace, state.OriginAccountID, "", &state.Evidence.OriginAccount, &state.OriginAccountName); err != nil {
			if errors.Is(err, ErrInvestmentConflict) {
				blockers = append(blockers, "conta de origem inválida ou arquivada")
				state.Evidence.OriginAccount = investmentEvidenceAccount{ID: state.OriginAccountID}
			} else {
				return state, nil, err
			}
		}
	} else {
		state.Evidence.OriginAccount = investmentEvidenceAccount{}
	}
	state.Evidence.OriginalTransfer = transfer
	if purchase.PurchaseDate != "" && purchase.PurchaseDate > economicDate {
		blockers = append(blockers, "data econômica anterior à compra")
	}
	var allocations int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_withdrawal_allocations WHERE workspace_id=? AND investment_id=? AND purchase_id=?`, workspace, investment, purchaseID).Scan(&allocations); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_withdrawal_allocations", Column: "purchase_id", Count: allocations})
	if allocations != 0 {
		blockers = append(blockers, "compra possui retirada alocada")
	}
	var positions int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_valuations WHERE workspace_id=? AND investment_id=? AND valuation_date>=?`, workspace, investment, purchase.PurchaseDate).Scan(&positions); err != nil {
		return state, nil, err
	}
	state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: "investment_valuations", Column: "valuation_date>=purchase_date", Count: positions})
	if positions != 0 {
		blockers = append(blockers, "posição igual ou posterior à compra impede determinar o efeito do estorno")
	}
	for _, table := range investmentReversalDependentTables {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&count); err != nil {
			return state, nil, err
		}
		state.Evidence.Dependents = append(state.Evidence.Dependents, investmentEvidenceCount{Table: table, Count: count})
		if count != 0 {
			blockers = append(blockers, "aplicação possui liquidação ou integração posterior registrada")
		}
	}
	if state.Movement == "TRANSFER" {
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
				blockers = append(blockers, "transferência vinculada já possui conciliação ou vínculo")
			}
		}
	}
	state.InvestmentBalanceCents = state.Evidence.InvestmentAccount.CurrentBalance
	state.InvestmentBalanceVersion = state.Evidence.InvestmentAccount.BalanceVersion
	state.OriginBalanceCents = state.Evidence.OriginAccount.CurrentBalance
	state.OriginBalanceVersion = state.Evidence.OriginAccount.BalanceVersion
	return state, blockers, nil
}
