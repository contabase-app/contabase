package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/investments"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

// Domain errors for the investment layer. Handlers classify with errors.Is;
// raw SQL and trigger messages never cross this boundary. A trigger RAISE
// that escapes validation races is a plain error and fails closed as a
// generic database failure.
var (
	ErrInvestmentNotFound       = errors.New("investimento não encontrado")
	ErrInvestmentValidation     = errors.New("dados do investimento inválidos")
	ErrInvestmentConflict       = errors.New("estado do investimento mudou")
	ErrInvestmentInvalidAccount = errors.New("conta não pode receber investimentos")
	ErrInvestmentInvalidStatus  = errors.New("status do investimento não permite esta operação")
)

const (
	investmentNameMaxRunes = 120
	investmentKindMaxRunes = 40
)

const investmentColumns = `id, workspace_id, account_id, name, institution, kind, principal_cents,
	tax_regime, applied_date, maturity_date, yield_note, status, redeemed_date, redeemed_by,
	archived_at, created_by, created_at, updated_at`

// CreateInvestmentInput carries only caller-supplied creation data. The
// workspace and the author always come from the internal request context
// (the authenticated user), never from a payload. status, redeemed_* and
// archived_at are never caller-supplied: a lot is born ATIVA and clean.
type CreateInvestmentInput struct {
	WorkspaceID     string
	AccountID       string
	SourceAccountID string // Empty retains the established record-only service contract.
	CreatedBy       string
	RequestID       string // Optional form key; repeats reuse the existing primary key.
	Name            string
	Institution     string
	Kind            string
	PrincipalCents  int64
	TaxRegime       string
	AppliedDate     string
	MaturityDate    *string
	YieldNote       string
}

// UpdateInvestmentInput carries exactly the editable set. Identity, account,
// principal, applied date, status and authorship are structurally absent, so
// the application blocks them before the database trigger ever could.
type UpdateInvestmentInput struct {
	WorkspaceID  string
	InvestmentID string
	UpdatedAt    int64
	Name         string
	Institution  string
	Kind         string
	TaxRegime    string
	MaturityDate *string
	YieldNote    string
}

// InvestmentListFilter mirrors the frozen listing views.
type InvestmentListFilter int

const (
	InvestmentListActive InvestmentListFilter = iota
	InvestmentListRedeemed
	InvestmentListArchived
	InvestmentListAll
)

// InvestmentDetail carries scoped purchases and positions. CurrentIsPrincipal
// means no position was recorded; the purchase-aware reader leaves its value
// unknown. The legacy pure builder below retains its single-purchase fallback.
type InvestmentDetail struct {
	AdjustedVariation                  investments.AdjustedVariation
	Withdrawals                        []models.InvestmentWithdrawal
	PurchaseStates                     map[string]models.InvestmentPurchaseState
	TotalWithdrawnCents                int64
	MaintainedPurchaseCount            int
	LatestWithdrawalDate               string
	Investment                         models.Investment
	CurrentCents                       int64
	CurrentIsPrincipal                 bool
	PositionDate                       string
	YieldCents                         int64
	ReturnBasisPoints                  int64
	ReturnAvailable                    bool
	Valuations                         []models.InvestmentValuation
	Purchases                          []models.InvestmentPurchase
	PurchaseTransfers                  map[string]InvestmentPurchaseTransfer
	TotalPurchasesCents                int64
	PurchaseCount                      int
	LatestPurchaseDate                 string
	PositionStale, DifferenceAvailable bool
}

// BuildInvestmentDetail derives the summary from the same tenant-scoped
// history that the page displays. Valuations are returned by the service in
// ascending civil-date order; the domain validates dates and selects current.
func BuildInvestmentDetail(inv models.Investment, valuations []models.InvestmentValuation) (InvestmentDetail, error) {
	current, err := investments.CurrentValueCents(inv, valuations)
	if err != nil {
		return InvestmentDetail{}, err
	}
	yield, err := investments.YieldCents(current, inv.PrincipalCents)
	if err != nil {
		return InvestmentDetail{}, err
	}
	basisPoints, available, err := investments.AccumulatedReturnBasisPoints(yield, inv.PrincipalCents)
	if err != nil {
		return InvestmentDetail{}, err
	}
	detail := InvestmentDetail{Investment: inv, CurrentCents: current, CurrentIsPrincipal: len(valuations) == 0,
		YieldCents: yield, ReturnBasisPoints: basisPoints, ReturnAvailable: available, Valuations: valuations}
	for _, valuation := range valuations {
		if valuation.ValuationDate > detail.PositionDate {
			detail.PositionDate = valuation.ValuationDate
		}
	}
	return detail, nil
}

func validInvestmentTaxRegime(regime string) bool {
	switch regime {
	case models.InvestmentTaxRegimeIRRegressivo, models.InvestmentTaxRegimeIsentoPF, models.InvestmentTaxRegimeManual:
		return true
	default:
		return false
	}
}

// validateInvestmentFields normalizes and validates the text fields shared by
// create and update. Limits match the migration 65 CHECKs (SQLite length()
// counts runes for TEXT, so Go rune counts are the same discipline).
func validateInvestmentFields(name, institution, kind, taxRegime, yieldNote string, maturityDate *string) (string, string, string, *string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > investmentNameMaxRunes {
		return "", "", "", nil, "", fmt.Errorf("%w: nome deve ter de 1 a %d caracteres", ErrInvestmentValidation, investmentNameMaxRunes)
	}
	kind = strings.TrimSpace(kind)
	if kind == "" || utf8.RuneCountInString(kind) > investmentKindMaxRunes {
		return "", "", "", nil, "", fmt.Errorf("%w: tipo deve ter de 1 a %d caracteres", ErrInvestmentValidation, investmentKindMaxRunes)
	}
	if !validInvestmentTaxRegime(taxRegime) {
		return "", "", "", nil, "", fmt.Errorf("%w: regime de tributação inválido", ErrInvestmentValidation)
	}
	normalizedMaturity := maturityDate
	if maturityDate != nil {
		trimmed := strings.TrimSpace(*maturityDate)
		if trimmed == "" {
			normalizedMaturity = nil
		} else if _, err := civildate.ParseCivilDateInput(trimmed); err != nil {
			return "", "", "", nil, "", fmt.Errorf("%w: data de vencimento: %v", ErrInvestmentValidation, err)
		} else {
			normalizedMaturity = &trimmed
		}
	}
	return name, strings.TrimSpace(institution), kind, normalizedMaturity, strings.TrimSpace(yieldNote), nil
}

func validateMaturityNotBeforeApplied(applied civildate.CivilDate, maturity *string) error {
	if maturity == nil {
		return nil
	}
	maturityDate, err := civildate.ParseCivilDateInput(*maturity)
	if err != nil {
		return fmt.Errorf("%w: data de vencimento: %v", ErrInvestmentValidation, err)
	}
	if civildate.CivilDateDaysBetween(applied, maturityDate) < 0 {
		return fmt.Errorf("%w: vencimento anterior à aplicação", ErrInvestmentValidation)
	}
	return nil
}

func scanInvestment(scan func(dest ...any) error) (models.Investment, error) {
	var inv models.Investment
	err := scan(&inv.ID, &inv.WorkspaceID, &inv.AccountID, &inv.Name, &inv.Institution, &inv.Kind,
		&inv.PrincipalCents, &inv.TaxRegime, &inv.AppliedDate, &inv.MaturityDate, &inv.YieldNote,
		&inv.Status, &inv.RedeemedDate, &inv.RedeemedBy, &inv.ArchivedAt, &inv.CreatedBy,
		&inv.CreatedAt, &inv.UpdatedAt)
	if err != nil {
		return models.Investment{}, err
	}
	return inv, nil
}

// CreateInvestment validates the lot and inserts it atomically with its
// account check inside one transaction, so a concurrent account archive
// cannot slip between validation and insert. The migration trigger remains
// the second barrier; this layer must not rely on it.
func CreateInvestment(ctx context.Context, db *sql.DB, input CreateInvestmentInput) (models.Investment, error) {
	if input.WorkspaceID == "" || input.AccountID == "" || input.CreatedBy == "" || (input.SourceAccountID != "" && input.RequestID == "") {
		return models.Investment{}, fmt.Errorf("%w: contexto de criação incompleto", ErrInvestmentValidation)
	}
	name, institution, kind, maturity, yieldNote, err := validateInvestmentFields(input.Name, input.Institution, input.Kind, input.TaxRegime, input.YieldNote, input.MaturityDate)
	if err != nil {
		return models.Investment{}, err
	}
	if input.PrincipalCents <= 0 {
		return models.Investment{}, fmt.Errorf("%w: principal deve ser maior que zero", ErrInvestmentValidation)
	}
	applied, err := civildate.ParseCivilDateInput(input.AppliedDate)
	if err != nil {
		return models.Investment{}, fmt.Errorf("%w: data de aplicação: %v", ErrInvestmentValidation, err)
	}
	if err := validateMaturityNotBeforeApplied(applied, maturity); err != nil {
		return models.Investment{}, err
	}
	id := uuid.NewString()
	if input.RequestID != "" {
		requestID, err := uuid.Parse(input.RequestID)
		if err != nil {
			return models.Investment{}, fmt.Errorf("%w: formulário inválido; reabra o cadastro", ErrInvestmentValidation)
		}
		// The authenticated workspace and author scope a retry key. A caller
		// cannot use it as an investment ID or collide with another tenant.
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte(input.WorkspaceID+"\x00"+input.CreatedBy+"\x00"+requestID.String())).String()
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return models.Investment{}, err
	}
	defer tx.Rollback()

	var accountHits int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts
		WHERE id = ? AND workspace_id = ? AND type = 'INVESTMENT' AND archived_at IS NULL`,
		input.AccountID, input.WorkspaceID).Scan(&accountHits)
	if err != nil {
		return models.Investment{}, err
	}
	if accountHits != 1 {
		return models.Investment{}, ErrInvestmentInvalidAccount
	}
	if input.RequestID != "" {
		previous, err := scanInvestment(func(dest ...any) error {
			return tx.QueryRowContext(ctx, `SELECT `+investmentColumns+` FROM investments
				WHERE id = ? AND workspace_id = ? AND created_by = ?`, id, input.WorkspaceID, input.CreatedBy).Scan(dest...)
		})
		if err == nil {
			sameMaturity := previous.MaturityDate == nil && maturity == nil ||
				previous.MaturityDate != nil && maturity != nil && *previous.MaturityDate == *maturity
			if previous.AccountID != input.AccountID || previous.Name != name || previous.Institution != institution ||
				previous.Kind != kind || previous.PrincipalCents != input.PrincipalCents || previous.TaxRegime != input.TaxRegime ||
				previous.AppliedDate != input.AppliedDate || previous.YieldNote != yieldNote || !sameMaturity {
				return models.Investment{}, fmt.Errorf("%w: este formulário já cadastrou outro investimento; reabra o cadastro", ErrInvestmentConflict)
			}
			if err := verifyInvestmentPurchaseTransferTx(ctx, tx, input.WorkspaceID, "initial:"+id, input.SourceAccountID, input.AccountID, input.CreatedBy, input.AppliedDate, input.PrincipalCents); err != nil {
				return models.Investment{}, err
			}
			return previous, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return models.Investment{}, err
		}
	}

	now := time.Now().Unix()
	inv := models.Investment{
		ID:             id,
		WorkspaceID:    input.WorkspaceID,
		AccountID:      input.AccountID,
		Name:           name,
		Institution:    institution,
		Kind:           kind,
		PrincipalCents: input.PrincipalCents,
		TaxRegime:      input.TaxRegime,
		AppliedDate:    input.AppliedDate,
		MaturityDate:   maturity,
		YieldNote:      yieldNote,
		Status:         models.InvestmentStatusAtiva,
		CreatedBy:      input.CreatedBy,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO investments (
		id, workspace_id, account_id, name, institution, kind, principal_cents,
		tax_regime, applied_date, maturity_date, yield_note, status, created_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.ID, inv.WorkspaceID, inv.AccountID, inv.Name, inv.Institution, inv.Kind,
		inv.PrincipalCents, inv.TaxRegime, inv.AppliedDate, inv.MaturityDate, inv.YieldNote,
		inv.Status, inv.CreatedBy, inv.CreatedAt, inv.UpdatedAt)
	if err != nil {
		return models.Investment{}, err
	}
	if input.SourceAccountID != "" {
		if err := createInvestmentPurchaseTransferTx(ctx, tx, input.WorkspaceID, "initial:"+id, input.SourceAccountID, input.AccountID, input.CreatedBy, input.AppliedDate, input.PrincipalCents); err != nil {
			return models.Investment{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return models.Investment{}, &InvestmentCommitError{err: err}
	}
	return inv, nil
}

// GetInvestment resolves one lot by id + workspace. A foreign-workspace id is
// indistinguishable from an unknown id: both return ErrInvestmentNotFound so
// cross-tenant existence never leaks.
func GetInvestment(ctx context.Context, db *sql.DB, workspaceID, investmentID string) (models.Investment, error) {
	if workspaceID == "" || investmentID == "" {
		return models.Investment{}, ErrInvestmentNotFound
	}
	inv, err := scanInvestment(func(dest ...any) error {
		return db.QueryRowContext(ctx, `SELECT `+investmentColumns+` FROM investments
			WHERE id = ? AND workspace_id = ?`, investmentID, workspaceID).Scan(dest...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return models.Investment{}, ErrInvestmentNotFound
	}
	if err != nil {
		return models.Investment{}, err
	}
	return inv, nil
}

// investmentListClause maps each frozen filter to its constant SQL fragment.
// Filter values never reach SQL as interpolation — the mapping is total.
func investmentListClause(filter InvestmentListFilter) (predicate, orderBy string, err error) {
	switch filter {
	case InvestmentListActive:
		return `status = 'ATIVA' AND archived_at IS NULL`,
			`(maturity_date IS NULL) ASC, maturity_date ASC, applied_date DESC, id ASC`, nil
	case InvestmentListRedeemed:
		return `status = 'RESGATADA' AND archived_at IS NULL`,
			`redeemed_date ASC, id ASC`, nil
	case InvestmentListArchived:
		return `archived_at IS NOT NULL`,
			`archived_at DESC, id ASC`, nil
	case InvestmentListAll:
		return `1 = 1`, `applied_date DESC, id ASC`, nil
	default:
		return "", "", fmt.Errorf("filtro de listagem de investimentos inválido: %d", int(filter))
	}
}

// ListInvestments returns the workspace-scoped listing for the frozen filter,
// with a deterministic ordering (unique id as final tiebreaker).
func ListInvestments(ctx context.Context, db *sql.DB, workspaceID string, filter InvestmentListFilter) ([]models.Investment, error) {
	if workspaceID == "" {
		return nil, ErrInvestmentNotFound
	}
	predicate, orderBy, err := investmentListClause(filter)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+investmentColumns+` FROM investments
		WHERE workspace_id = ? AND `+predicate+` ORDER BY `+orderBy, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.Investment
	for rows.Next() {
		inv, err := scanInvestment(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// UpdateInvestment applies the editable set to an ATIVA lot. The mutation is
// guarded in the SQL itself (id + workspace + status) and requires exactly
// one affected row; a zero-row result is classified fail-closed afterwards.
func UpdateInvestment(ctx context.Context, db *sql.DB, input UpdateInvestmentInput) error {
	if input.WorkspaceID == "" || input.InvestmentID == "" {
		return ErrInvestmentNotFound
	}
	name, institution, kind, maturity, yieldNote, err := validateInvestmentFields(input.Name, input.Institution, input.Kind, input.TaxRegime, input.YieldNote, input.MaturityDate)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var appliedDate string
	err = tx.QueryRowContext(ctx, `SELECT applied_date FROM investments
		WHERE id = ? AND workspace_id = ?`, input.InvestmentID, input.WorkspaceID).Scan(&appliedDate)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentNotFound
	}
	if err != nil {
		return err
	}
	// A cancelled lot stays ATIVA, so only the receipt can refuse the edit.
	if err := rejectCancelledInvestmentTx(ctx, tx, input.WorkspaceID, input.InvestmentID); err != nil {
		return err
	}
	applied, err := civildate.ParseCivilDateInput(appliedDate)
	if err != nil {
		// Corrupted stored date: refuse to reason about it.
		return fmt.Errorf("data de aplicação corrompida do investimento %s: %w", input.InvestmentID, err)
	}
	if err := validateMaturityNotBeforeApplied(applied, maturity); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx, `UPDATE investments
		SET name = ?, institution = ?, kind = ?, tax_regime = ?, maturity_date = ?, yield_note = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'ATIVA'`,
		name, institution, kind, input.TaxRegime, maturity, yieldNote, input.UpdatedAt,
		input.InvestmentID, input.WorkspaceID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		var status string
		err := tx.QueryRowContext(ctx, `SELECT status FROM investments
			WHERE id = ? AND workspace_id = ?`, input.InvestmentID, input.WorkspaceID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvestmentNotFound
		}
		if err != nil {
			return err
		}
		if status != models.InvestmentStatusAtiva {
			return ErrInvestmentInvalidStatus
		}
		return ErrInvestmentConflict
	}
	return tx.Commit()
}

// Lifecycle identity is supplied by the authenticated context and resource path.
type RedeemInvestmentInput struct {
	WorkspaceID      string
	InvestmentID     string
	ActingUserID     string
	RedeemedDate     string
	FinalAmountCents int64
}

type ArchiveInvestmentInput struct {
	WorkspaceID  string
	InvestmentID string
	ActingUserID string
}

// InvestmentCommitError marks an uncertain lifecycle outcome. It must be
// classified before context cancellation or retryable pre-commit contention.
type InvestmentCommitError struct{ err error }

func (e *InvestmentCommitError) Error() string { return "investment lifecycle commit failed" }
func (e *InvestmentCommitError) Unwrap() error { return e.err }

// RedeemInvestment requires the runtime Immediate pool: reserve the writer
// before reading status, then persist the final position and status atomically.
// It never writes the ledger or account balances.
func RedeemInvestment(ctx context.Context, db *sql.DB, input RedeemInvestmentInput) error {
	if input.WorkspaceID == "" || input.InvestmentID == "" || input.ActingUserID == "" {
		return fmt.Errorf("%w: contexto do resgate incompleto", ErrInvestmentValidation)
	}
	redeemed, err := civildate.ParseCivilDateInput(input.RedeemedDate)
	if err != nil {
		return fmt.Errorf("%w: data do resgate: %v", ErrInvestmentValidation, err)
	}
	if input.FinalAmountCents < 0 {
		return fmt.Errorf("%w: valor final não pode ser negativo", ErrInvestmentValidation)
	}
	input.RedeemedDate = civildate.FormatCivilDate(redeemed)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rejectCancelledInvestmentTx(ctx, tx, input.WorkspaceID, input.InvestmentID); err != nil {
		return err
	}
	if err := rejectFundedInvestmentTx(ctx, tx, input.WorkspaceID, input.InvestmentID); err != nil {
		return err
	}
	var status, appliedDate string
	err = tx.QueryRowContext(ctx, `SELECT status, applied_date FROM investments
		WHERE id = ? AND workspace_id = ?`, input.InvestmentID, input.WorkspaceID).Scan(&status, &appliedDate)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentNotFound
	}
	if err != nil {
		return err
	}
	if status != models.InvestmentStatusAtiva {
		return ErrInvestmentInvalidStatus
	}
	applied, err := civildate.ParseCivilDateInput(appliedDate)
	if err != nil {
		return err
	}
	if civildate.CivilDateDaysBetween(applied, redeemed) < 0 {
		return fmt.Errorf("%w: resgate anterior à aplicação", ErrInvestmentValidation)
	}
	var latestPurchase sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT MAX(purchase_date) FROM investment_purchases WHERE workspace_id = ? AND investment_id = ?`, input.WorkspaceID, input.InvestmentID).Scan(&latestPurchase); err != nil {
		return err
	}
	if latestPurchase.Valid && input.RedeemedDate < latestPurchase.String {
		return fmt.Errorf("%w: encerramento anterior à última compra", ErrInvestmentValidation)
	}
	var latestValuationDate sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT MAX(valuation_date) FROM investment_valuations
		WHERE workspace_id = ? AND investment_id = ?`, input.WorkspaceID, input.InvestmentID).Scan(&latestValuationDate)
	if err != nil {
		return err
	}
	if latestValuationDate.Valid {
		latest, err := civildate.ParseCivilDateInput(latestValuationDate.String)
		if err != nil {
			return err
		}
		if civildate.CivilDateDaysBetween(latest, redeemed) < 0 {
			return fmt.Errorf("%w: resgate anterior à última posição registrada", ErrInvestmentValidation)
		}
	}
	// Migration 65 permits valuation writes only while the lot is ATIVA.
	if _, err := upsertInvestmentValuationTx(ctx, tx, UpsertInvestmentValuationInput{
		WorkspaceID: input.WorkspaceID, InvestmentID: input.InvestmentID, ActingUserID: input.ActingUserID,
		ValuationDate: input.RedeemedDate, AmountCents: input.FinalAmountCents,
	}); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE investments
		SET status = 'RESGATADA', redeemed_date = ?, redeemed_by = ?, updated_at = unixepoch()
		WHERE id = ? AND workspace_id = ? AND status = 'ATIVA'`,
		input.RedeemedDate, input.ActingUserID, input.InvestmentID, input.WorkspaceID)
	if err != nil {
		return err
	}
	if err := checkInvestmentLifecycleRows(ctx, tx, result, input.WorkspaceID, input.InvestmentID, models.InvestmentStatusAtiva); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return &InvestmentCommitError{err: err}
	}
	return nil
}

// ArchiveInvestment is a one-way transition for a previously redeemed lot.
// db must be the Immediate pool, including the guarded update and re-read.
func ArchiveInvestment(ctx context.Context, db *sql.DB, input ArchiveInvestmentInput) error {
	if input.WorkspaceID == "" || input.InvestmentID == "" || input.ActingUserID == "" {
		return fmt.Errorf("%w: contexto do arquivamento incompleto", ErrInvestmentValidation)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := rejectCancelledInvestmentTx(ctx, tx, input.WorkspaceID, input.InvestmentID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE investments
		SET archived_at = unixepoch(), updated_at = unixepoch()
		WHERE id = ? AND workspace_id = ? AND status = 'RESGATADA' AND archived_at IS NULL`,
		input.InvestmentID, input.WorkspaceID)
	if err != nil {
		return err
	}
	if err := checkInvestmentLifecycleRows(ctx, tx, result, input.WorkspaceID, input.InvestmentID, models.InvestmentStatusResgatada); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return &InvestmentCommitError{err: err}
	}
	return nil
}

func checkInvestmentLifecycleRows(ctx context.Context, tx *sql.Tx, result sql.Result, workspaceID, investmentID, expectedStatus string) error {
	affected, err := result.RowsAffected()
	if err != nil || affected == 1 {
		return err
	}
	var status string
	var archivedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT status, archived_at FROM investments
		WHERE id = ? AND workspace_id = ?`, investmentID, workspaceID).Scan(&status, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvestmentNotFound
	}
	if err != nil {
		return err
	}
	if status != expectedStatus || archivedAt.Valid {
		return ErrInvestmentInvalidStatus
	}
	return ErrInvestmentConflict
}

func scanValuation(scan func(dest ...any) error) (models.InvestmentValuation, error) {
	var val models.InvestmentValuation
	err := scan(&val.ID, &val.WorkspaceID, &val.InvestmentID, &val.ValuationDate, &val.AmountCents,
		&val.CreatedBy, &val.CreatedAt, &val.UpdatedBy, &val.UpdatedAt)
	if err != nil {
		return models.InvestmentValuation{}, err
	}
	return val, nil
}

// UpsertInvestmentValuationInput receives identity exclusively from the
// authenticated request context and the investment resource path.
type UpsertInvestmentValuationInput struct {
	WorkspaceID   string
	InvestmentID  string
	ActingUserID  string
	ValuationDate string
	AmountCents   int64
}

// InvestmentValuationCommitError distinguishes commit failures from retryable
// pre-commit contention. HTTP callers must return a generic 500 for this phase.
type InvestmentValuationCommitError struct{ err error }

func (e *InvestmentValuationCommitError) Error() string { return "investment valuation commit failed" }
func (e *InvestmentValuationCommitError) Unwrap() error { return e.err }

// UpsertInvestmentValuation records or corrects a day's position without
// changing principal, ledger or account balance. db must be the Immediate pool
// so the writer reservation precedes every read used to decide this mutation.
func UpsertInvestmentValuation(ctx context.Context, db *sql.DB, input UpsertInvestmentValuationInput) (models.InvestmentValuation, error) {
	if input.WorkspaceID == "" || input.InvestmentID == "" || input.ActingUserID == "" {
		return models.InvestmentValuation{}, fmt.Errorf("%w: contexto da posição incompleto", ErrInvestmentValidation)
	}
	valuation, err := civildate.ParseCivilDateInput(input.ValuationDate)
	if err != nil {
		return models.InvestmentValuation{}, fmt.Errorf("%w: data da posição: %v", ErrInvestmentValidation, err)
	}
	if input.AmountCents < 0 {
		return models.InvestmentValuation{}, fmt.Errorf("%w: valor da posição não pode ser negativo", ErrInvestmentValidation)
	}
	input.ValuationDate = civildate.FormatCivilDate(valuation)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return models.InvestmentValuation{}, err
	}
	defer tx.Rollback()

	var status, appliedDate string
	err = tx.QueryRowContext(ctx, `SELECT status, applied_date FROM investments
		WHERE id = ? AND workspace_id = ?`, input.InvestmentID, input.WorkspaceID).Scan(&status, &appliedDate)
	if errors.Is(err, sql.ErrNoRows) {
		return models.InvestmentValuation{}, ErrInvestmentNotFound
	}
	if err != nil {
		return models.InvestmentValuation{}, err
	}
	// Position upserts are child writes of the lot: a cancelled receipt must
	// refuse them before the trigger turns the UPSERT into a raw abort.
	if err := rejectCancelledInvestmentTx(ctx, tx, input.WorkspaceID, input.InvestmentID); err != nil {
		return models.InvestmentValuation{}, err
	}
	if status != models.InvestmentStatusAtiva {
		return models.InvestmentValuation{}, ErrInvestmentInvalidStatus
	}
	applied, err := civildate.ParseCivilDateInput(appliedDate)
	if err != nil {
		return models.InvestmentValuation{}, err
	}
	if civildate.CivilDateDaysBetween(applied, valuation) < 0 {
		return models.InvestmentValuation{}, fmt.Errorf("%w: posição anterior à aplicação", ErrInvestmentValidation)
	}
	final, err := upsertInvestmentValuationTx(ctx, tx, input)
	if err != nil {
		return models.InvestmentValuation{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.InvestmentValuation{}, &InvestmentValuationCommitError{err: err}
	}
	return final, nil
}

// upsertInvestmentValuationTx only persists and reads back the canonical row.
// The caller owns validation, transaction lifetime and commit.
func upsertInvestmentValuationTx(ctx context.Context, tx *sql.Tx, input UpsertInvestmentValuationInput) (models.InvestmentValuation, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO investment_valuations (
		id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at
	) VALUES (?, ?, ?, ?, ?, ?, unixepoch())
	ON CONFLICT(workspace_id, investment_id, valuation_date)
	DO UPDATE SET
		amount_cents = excluded.amount_cents,
		updated_by = excluded.created_by,
		updated_at = unixepoch()`, uuid.NewString(), input.WorkspaceID, input.InvestmentID,
		input.ValuationDate, input.AmountCents, input.ActingUserID)
	if err != nil {
		return models.InvestmentValuation{}, err
	}
	return scanValuation(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT id, workspace_id, investment_id, valuation_date, amount_cents,
			created_by, created_at, updated_by, updated_at FROM investment_valuations
			WHERE workspace_id = ? AND investment_id = ? AND valuation_date = ?`,
			input.WorkspaceID, input.InvestmentID, input.ValuationDate).Scan(dest...)
	})
}

// GetInvestmentValuations returns the lot's full valuation history ascending.
// The lot must exist in the workspace (unknown and foreign ids are both
// ErrInvestmentNotFound); every row is workspace + investment scoped.
func GetInvestmentValuations(ctx context.Context, db *sql.DB, workspaceID, investmentID string) ([]models.InvestmentValuation, error) {
	if workspaceID == "" || investmentID == "" {
		return nil, ErrInvestmentNotFound
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var lotHits int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investments
		WHERE id = ? AND workspace_id = ?`, investmentID, workspaceID).Scan(&lotHits); err != nil {
		return nil, err
	}
	if lotHits != 1 {
		return nil, ErrInvestmentNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, workspace_id, investment_id, valuation_date, amount_cents,
		created_by, created_at, updated_by, updated_at
		FROM investment_valuations
		WHERE workspace_id = ? AND investment_id = ?
		ORDER BY valuation_date ASC, id ASC`, workspaceID, investmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.InvestmentValuation
	for rows.Next() {
		val, err := scanValuation(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, val)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// GetCurrentInvestmentValue resolves the frozen current-value rule in SQL:
// the latest valuation by valuation_date wins (the UNIQUE key admits at most
// one per day), otherwise the lot falls back to principal_cents. Both steps
// are workspace-scoped; nothing is persisted.
func GetCurrentInvestmentValue(ctx context.Context, db *sql.DB, workspaceID, investmentID string) (int64, bool, error) {
	if workspaceID == "" || investmentID == "" {
		return 0, false, ErrInvestmentNotFound
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var principal int64
	err = tx.QueryRowContext(ctx, `SELECT principal_cents FROM investments
		WHERE id = ? AND workspace_id = ?`, investmentID, workspaceID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrInvestmentNotFound
	}
	if err != nil {
		return 0, false, err
	}

	var current int64
	err = tx.QueryRowContext(ctx, `SELECT amount_cents FROM investment_valuations
		WHERE workspace_id = ? AND investment_id = ?
		ORDER BY valuation_date DESC LIMIT 1`, workspaceID, investmentID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return principal, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return current, false, nil
}
