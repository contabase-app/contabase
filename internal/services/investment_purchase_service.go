package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/investments"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

const purchaseColumns = `id, workspace_id, investment_id, purchase_date, principal_cents, note_reference,
	kind, institution, maturity_date, tax_regime, conditions_note, conditions_source, is_initial, created_by, created_at`

type CreateInvestmentPurchaseInput struct {
	WorkspaceID, InvestmentID, CreatedBy, RequestID             string
	SourceAccountID                                             string
	PurchaseDate                                                string
	PrincipalCents                                              int64
	NoteReference, Kind, Institution, TaxRegime, ConditionsNote string
	MaturityDate                                                *string
}

type InvestmentPurchaseTotal struct {
	TotalCents int64
	Count      int
	LatestDate string
}

func scanInvestmentPurchase(scan func(...any) error) (models.InvestmentPurchase, error) {
	var p models.InvestmentPurchase
	err := scan(&p.ID, &p.WorkspaceID, &p.InvestmentID, &p.PurchaseDate, &p.PrincipalCents, &p.NoteReference,
		&p.Kind, &p.Institution, &p.MaturityDate, &p.TaxRegime, &p.ConditionsNote, &p.ConditionsSource, &p.IsInitial, &p.CreatedBy, &p.CreatedAt)
	return p, err
}

func addInvestmentPurchase(total InvestmentPurchaseTotal, p models.InvestmentPurchase) (InvestmentPurchaseTotal, error) {
	if p.PrincipalCents <= 0 || total.TotalCents > math.MaxInt64-p.PrincipalCents {
		return InvestmentPurchaseTotal{}, fmt.Errorf("%w: total de compras fora do intervalo permitido", ErrInvestmentValidation)
	}
	total.TotalCents += p.PrincipalCents
	total.Count++
	if p.PurchaseDate > total.LatestDate {
		total.LatestDate = p.PurchaseDate
	}
	return total, nil
}

// CreateInvestmentPurchase reserves the Immediate writer before every state
// check. The request identity is scoped to workspace, investment and author;
// an identical retry returns its row, a changed payload fails with a conflict.
func CreateInvestmentPurchase(ctx context.Context, db *sql.DB, in CreateInvestmentPurchaseInput) (models.InvestmentPurchase, error) {
	if in.WorkspaceID == "" || in.InvestmentID == "" || in.CreatedBy == "" {
		return models.InvestmentPurchase{}, ErrInvestmentValidation
	}
	key, err := uuid.Parse(in.RequestID)
	if err != nil {
		return models.InvestmentPurchase{}, fmt.Errorf("%w: formulário inválido; reabra a reaplicação", ErrInvestmentValidation)
	}
	date, err := civildate.ParseCivilDateInput(in.PurchaseDate)
	if err != nil || in.PrincipalCents <= 0 {
		return models.InvestmentPurchase{}, fmt.Errorf("%w: data ou valor da compra inválido", ErrInvestmentValidation)
	}
	_, institution, kind, maturity, note, err := validateInvestmentFields("Compra", in.Institution, in.Kind, in.TaxRegime, in.ConditionsNote, in.MaturityDate)
	if err != nil {
		return models.InvestmentPurchase{}, err
	}
	if err := validateMaturityNotBeforeApplied(date, maturity); err != nil {
		return models.InvestmentPurchase{}, err
	}
	reference := strings.TrimSpace(in.NoteReference)
	if utf8.RuneCountInString(reference) > 120 {
		return models.InvestmentPurchase{}, fmt.Errorf("%w: identificação da nota deve ter até 120 caracteres", ErrInvestmentValidation)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return models.InvestmentPurchase{}, err
	}
	defer tx.Rollback()
	var status, applied, account string
	var archived sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT status, applied_date, account_id, archived_at FROM investments WHERE workspace_id = ? AND id = ?`, in.WorkspaceID, in.InvestmentID).Scan(&status, &applied, &account, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return models.InvestmentPurchase{}, ErrInvestmentNotFound
	}
	if err != nil {
		return models.InvestmentPurchase{}, err
	}
	p := models.InvestmentPurchase{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("purchase\x00"+in.WorkspaceID+"\x00"+in.InvestmentID+"\x00"+in.CreatedBy+"\x00"+key.String())).String(),
		WorkspaceID: in.WorkspaceID, InvestmentID: in.InvestmentID, CreatedBy: in.CreatedBy, PurchaseDate: date.String(), PrincipalCents: in.PrincipalCents,
		NoteReference: reference, Kind: &kind, Institution: &institution, TaxRegime: &in.TaxRegime, MaturityDate: maturity, ConditionsNote: &note, ConditionsSource: "RECORDED"}
	previous, err := scanInvestmentPurchase(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+purchaseColumns+` FROM investment_purchases WHERE workspace_id = ? AND investment_id = ? AND id = ? AND created_by = ?`, in.WorkspaceID, in.InvestmentID, p.ID, in.CreatedBy).Scan(dest...)
	})
	if err == nil {
		// A committed retry is safe even if the investment was later closed.
		p.CreatedAt = previous.CreatedAt
		if !reflect.DeepEqual(p, previous) {
			return models.InvestmentPurchase{}, fmt.Errorf("%w: este formulário já registrou outra compra; reabra a reaplicação", ErrInvestmentConflict)
		}
		if err := verifyInvestmentPurchaseTransferTx(ctx, tx, in.WorkspaceID, p.ID, in.SourceAccountID, account, in.CreatedBy, p.PurchaseDate, p.PrincipalCents); err != nil {
			return models.InvestmentPurchase{}, err
		}
		return previous, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return models.InvestmentPurchase{}, err
	}
	if status != models.InvestmentStatusAtiva || archived.Valid {
		return models.InvestmentPurchase{}, ErrInvestmentInvalidStatus
	}
	if p.PurchaseDate < applied {
		return models.InvestmentPurchase{}, fmt.Errorf("%w: compra anterior à aplicação inicial", ErrInvestmentValidation)
	}
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(principal_cents), 0) FROM investment_purchases WHERE workspace_id = ? AND investment_id = ?`, in.WorkspaceID, in.InvestmentID).Scan(&total); err != nil {
		return models.InvestmentPurchase{}, err
	}
	if total <= 0 || total > math.MaxInt64-p.PrincipalCents {
		return models.InvestmentPurchase{}, fmt.Errorf("%w: total de compras fora do intervalo permitido", ErrInvestmentValidation)
	}
	// Never turn a historical record into an implicit reconciliation.
	if in.SourceAccountID != "" {
		if err := requireFundedInvestmentTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
			return models.InvestmentPurchase{}, err
		}
	}
	p.CreatedAt = time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_purchases (`+purchaseColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.WorkspaceID, p.InvestmentID, p.PurchaseDate, p.PrincipalCents, p.NoteReference, p.Kind, p.Institution, p.MaturityDate, p.TaxRegime, p.ConditionsNote, p.ConditionsSource, p.IsInitial, p.CreatedBy, p.CreatedAt)
	if err != nil {
		return models.InvestmentPurchase{}, err
	}
	if in.SourceAccountID != "" {
		if err := createInvestmentPurchaseTransferTx(ctx, tx, in.WorkspaceID, p.ID, in.SourceAccountID, account, in.CreatedBy, p.PurchaseDate, p.PrincipalCents); err != nil {
			return models.InvestmentPurchase{}, err
		}
	} else if err := rejectFundedInvestmentTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return models.InvestmentPurchase{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.InvestmentPurchase{}, &InvestmentCommitError{err: err}
	}
	return p, nil
}

// GetInvestmentPurchaseDetail reads investment, positions and purchases from
// one snapshot, so a concurrent purchase cannot make an old position appear
// comparable to a total from a different database state.
func GetInvestmentPurchaseDetail(ctx context.Context, db *sql.DB, workspace, id string) (InvestmentDetail, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return InvestmentDetail{}, err
	}
	defer tx.Rollback()
	inv, err := scanInvestment(func(dest ...any) error {
		return tx.QueryRowContext(ctx, `SELECT `+investmentColumns+` FROM investments WHERE workspace_id = ? AND id = ?`, workspace, id).Scan(dest...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return InvestmentDetail{}, ErrInvestmentNotFound
	}
	if err != nil {
		return InvestmentDetail{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, workspace_id, investment_id, valuation_date, amount_cents, created_by, created_at, updated_by, updated_at FROM investment_valuations WHERE workspace_id = ? AND investment_id = ? ORDER BY valuation_date, id`, workspace, id)
	if err != nil {
		return InvestmentDetail{}, err
	}
	var valuations []models.InvestmentValuation
	for rows.Next() {
		v, err := scanValuation(rows.Scan)
		if err != nil {
			rows.Close()
			return InvestmentDetail{}, err
		}
		valuations = append(valuations, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return InvestmentDetail{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT `+purchaseColumns+` FROM investment_purchases WHERE workspace_id = ? AND investment_id = ? ORDER BY purchase_date, is_initial DESC, id`, workspace, id)
	if err != nil {
		return InvestmentDetail{}, err
	}
	var purchases []models.InvestmentPurchase
	for rows.Next() {
		p, err := scanInvestmentPurchase(rows.Scan)
		if err != nil {
			rows.Close()
			return InvestmentDetail{}, err
		}
		purchases = append(purchases, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return InvestmentDetail{}, err
	}
	detail, err := BuildInvestmentPurchaseDetail(inv, valuations, purchases)
	if err != nil {
		return InvestmentDetail{}, err
	}
	history, err := readInvestmentWithdrawals(ctx, tx, workspace, id)
	if err != nil {
		return InvestmentDetail{}, err
	}
	if err := enrichInvestmentWithdrawals(&detail, history); err != nil {
		return InvestmentDetail{}, err
	}
	detail.AdjustedVariation = investments.AdjustedPositionVariation(inv, valuations, purchases, history)
	detail.PurchaseTransfers, err = readInvestmentPurchaseTransfersTx(ctx, tx, workspace, id, inv.AccountID)
	if err != nil {
		return InvestmentDetail{}, err
	}

	if err := tx.Commit(); err != nil {
		return InvestmentDetail{}, err
	}
	return detail, nil
}

func BuildInvestmentPurchaseDetail(inv models.Investment, valuations []models.InvestmentValuation, purchases []models.InvestmentPurchase) (InvestmentDetail, error) {
	detail := InvestmentDetail{Investment: inv, Valuations: valuations, Purchases: purchases, CurrentIsPrincipal: len(valuations) == 0}
	var total InvestmentPurchaseTotal
	initials := 0
	for _, p := range purchases {
		if p.WorkspaceID != inv.WorkspaceID || p.InvestmentID != inv.ID {
			return InvestmentDetail{}, ErrInvestmentNotFound
		}
		if _, err := civildate.ParseCivilDateInput(p.PurchaseDate); err != nil {
			return InvestmentDetail{}, err
		}
		if p.IsInitial {
			initials++
		}
		var err error
		total, err = addInvestmentPurchase(total, p)
		if err != nil {
			return InvestmentDetail{}, err
		}
	}
	if initials != 1 {
		return InvestmentDetail{}, errors.New("investment initial purchase missing or duplicated")
	}
	detail.TotalPurchasesCents, detail.PurchaseCount, detail.LatestPurchaseDate = total.TotalCents, total.Count, total.LatestDate
	// The pure domain validates dates/duplicates and selects the latest position.
	current, err := investments.CurrentValueCents(inv, valuations)
	if err != nil {
		return InvestmentDetail{}, err
	}
	for _, v := range valuations {
		if v.ValuationDate > detail.PositionDate {
			detail.PositionDate = v.ValuationDate
		}
	}
	if len(valuations) == 0 {
		return detail, nil
	}
	detail.CurrentCents = current
	detail.PositionStale = total.LatestDate > detail.PositionDate
	detail.DifferenceAvailable = !detail.PositionStale
	if detail.DifferenceAvailable {
		detail.YieldCents, err = investments.YieldCents(current, total.TotalCents)
	}
	// No return percentage or tax estimate can be inferred from pooled positions.
	return detail, err
}

func GetWorkspaceInvestmentPurchaseTotals(ctx context.Context, db *sql.DB, workspace string) (map[string]InvestmentPurchaseTotal, error) {
	rows, err := db.QueryContext(ctx, `SELECT investment_id, purchase_date, principal_cents FROM investment_purchases WHERE workspace_id = ? ORDER BY investment_id, purchase_date, id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	totals := make(map[string]InvestmentPurchaseTotal)
	for rows.Next() {
		var p models.InvestmentPurchase
		if err := rows.Scan(&p.InvestmentID, &p.PurchaseDate, &p.PrincipalCents); err != nil {
			return nil, err
		}
		total, err := addInvestmentPurchase(totals[p.InvestmentID], p)
		if err != nil {
			return nil, err
		}
		totals[p.InvestmentID] = total
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Use the latest operation date to keep stale manual positions out of the list consolidation.
	rows, err = db.QueryContext(ctx, `SELECT investment_id, MAX(withdrawal_date) FROM investment_withdrawals WHERE workspace_id=? GROUP BY investment_id`, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, date string
		if err := rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		total := totals[id]
		if date > total.LatestDate {
			total.LatestDate = date
			totals[id] = total
		}
	}
	return totals, rows.Err()
}
