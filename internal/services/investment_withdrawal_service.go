package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

const InvestmentWithdrawalPartial = "PARTIAL"
const InvestmentWithdrawalTotal = "TOTAL"

type CreateInvestmentWithdrawalInput struct {
	WorkspaceID, InvestmentID, CreatedBy, RequestID, WithdrawalDate, Mode, Note string
	ReceivedCents                                                               int64
	Allocations                                                                 []models.InvestmentWithdrawalAllocation
}

func withdrawalInvalid(message string) error {
	return fmt.Errorf("%w: %s", ErrInvestmentValidation, message)
}

// The caller supplies the runtime Immediate pool, as for purchases. Admission,
// idempotency, principal bounds and every allocation share a single writer tx.
func CreateInvestmentWithdrawal(ctx context.Context, db *sql.DB, in CreateInvestmentWithdrawalInput) (models.InvestmentWithdrawal, error) {
	var empty models.InvestmentWithdrawal
	key, err := uuid.Parse(in.RequestID)
	if err != nil || in.WorkspaceID == "" || in.InvestmentID == "" || in.CreatedBy == "" {
		return empty, withdrawalInvalid("formulário inválido; reabra a retirada")
	}
	date, err := civildate.ParseCivilDateInput(in.WithdrawalDate)
	if err != nil {
		return empty, withdrawalInvalid("data da retirada inválida")
	}
	if (in.Mode != InvestmentWithdrawalPartial && in.Mode != InvestmentWithdrawalTotal) || in.ReceivedCents < 0 || (in.Mode == InvestmentWithdrawalPartial && in.ReceivedCents == 0) {
		return empty, withdrawalInvalid("modalidade ou valor recebido inválido")
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > 2000 {
		return empty, withdrawalInvalid("observações devem ter até 2000 caracteres")
	}
	w := models.InvestmentWithdrawal{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("withdrawal\x00"+in.WorkspaceID+"\x00"+in.InvestmentID+"\x00"+in.CreatedBy+"\x00"+key.String())).String(), WorkspaceID: in.WorkspaceID, InvestmentID: in.InvestmentID, CreatedBy: in.CreatedBy, RequestID: key.String(), WithdrawalDate: date.String(), Mode: in.Mode, ReceivedCents: in.ReceivedCents, Note: note}
	if len(in.Allocations) == 0 {
		return empty, withdrawalInvalid("selecione explicitamente as compras afetadas")
	}
	seen := make(map[string]bool)
	var sum int64
	for _, a := range in.Allocations {
		if a.PurchaseID == "" || seen[a.PurchaseID] || a.ReceivedCents < 0 || (w.Mode == InvestmentWithdrawalPartial && a.ReceivedCents == 0) || (a.PrincipalCents != nil && *a.PrincipalCents < 0) {
			return empty, withdrawalInvalid("atribuição inválida ou compra repetida")
		}
		if sum > math.MaxInt64-a.ReceivedCents {
			return empty, withdrawalInvalid("soma das atribuições fora do intervalo permitido")
		}
		sum += a.ReceivedCents
		seen[a.PurchaseID] = true
		w.Allocations = append(w.Allocations, models.InvestmentWithdrawalAllocation{WorkspaceID: w.WorkspaceID, InvestmentID: w.InvestmentID, WithdrawalID: w.ID, PurchaseID: a.PurchaseID, ReceivedCents: a.ReceivedCents, PrincipalCents: a.PrincipalCents, ClosesPurchase: w.Mode == InvestmentWithdrawalTotal})
	}
	if sum != w.ReceivedCents {
		return empty, withdrawalInvalid("a soma atribuída às compras deve ser igual ao valor recebido")
	}
	sort.Slice(w.Allocations, func(i, j int) bool { return w.Allocations[i].PurchaseID < w.Allocations[j].PurchaseID })
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err := rejectFundedInvestmentTx(ctx, tx, in.WorkspaceID, in.InvestmentID); err != nil {
		return models.InvestmentWithdrawal{}, err
	}
	var status string
	var archived sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT status, archived_at FROM investments WHERE workspace_id=? AND id=?`, w.WorkspaceID, w.InvestmentID).Scan(&status, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, ErrInvestmentNotFound
	}
	if err != nil {
		return empty, err
	}
	history, err := readInvestmentWithdrawals(ctx, tx, w.WorkspaceID, w.InvestmentID)
	if err != nil {
		return empty, err
	}
	for _, prior := range history {
		if prior.ID == w.ID {
			w.CreatedAt = prior.CreatedAt
			if !reflect.DeepEqual(prior, w) {
				return empty, fmt.Errorf("%w: formulário já utilizado com outros dados; reabra a retirada", ErrInvestmentConflict)
			}
			return prior, nil
		}
	}
	if status != models.InvestmentStatusAtiva || archived.Valid {
		return empty, ErrInvestmentInvalidStatus
	}
	states, total, _, err := withdrawalStates(history)
	if err != nil {
		return empty, err
	}
	if total > math.MaxInt64-w.ReceivedCents {
		return empty, withdrawalInvalid("total recebido fora do intervalo permitido")
	}
	for _, a := range w.Allocations {
		var purchased string
		var principal int64
		err := tx.QueryRowContext(ctx, `SELECT purchase_date, principal_cents FROM investment_purchases WHERE workspace_id=? AND investment_id=? AND id=?`, w.WorkspaceID, w.InvestmentID, a.PurchaseID).Scan(&purchased, &principal)
		if errors.Is(err, sql.ErrNoRows) {
			return empty, ErrInvestmentNotFound
		}
		if err != nil {
			return empty, err
		}
		state := states[a.PurchaseID]
		if state.Closed {
			return empty, fmt.Errorf("%w: compra já integralmente retirada", ErrInvestmentConflict)
		}
		if w.WithdrawalDate < purchased {
			return empty, withdrawalInvalid("retirada anterior à compra selecionada")
		}
		if a.ClosesPurchase && w.WithdrawalDate < state.LatestWithdrawalDate {
			return empty, withdrawalInvalid("retirada total anterior a uma retirada desta compra")
		}
		if a.PrincipalCents != nil && (*a.PrincipalCents > principal || state.KnownPrincipalWithdrawn > principal-*a.PrincipalCents) {
			return empty, withdrawalInvalid("principal baixado acumulado ultrapassa o principal da compra")
		}
	}
	w.CreatedAt = time.Now().Unix()
	_, err = tx.ExecContext(ctx, `INSERT INTO investment_withdrawals (id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id,note,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, w.ID, w.WorkspaceID, w.InvestmentID, w.WithdrawalDate, w.ReceivedCents, w.Mode, w.CreatedBy, w.RequestID, w.Note, w.CreatedAt)
	if err != nil {
		return empty, err
	}
	for _, a := range w.Allocations {
		_, err := tx.ExecContext(ctx, `INSERT INTO investment_withdrawal_allocations (workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase) VALUES (?,?,?,?,?,?,?)`, a.WorkspaceID, a.InvestmentID, a.WithdrawalID, a.PurchaseID, a.ReceivedCents, a.PrincipalCents, a.ClosesPurchase)
		if err != nil {
			return empty, err
		}
	}
	if err := tx.Commit(); err != nil {
		return empty, &InvestmentCommitError{err: err}
	}
	return w, nil
}

func readInvestmentWithdrawals(ctx context.Context, tx *sql.Tx, workspace, investment string) ([]models.InvestmentWithdrawal, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,workspace_id,investment_id,withdrawal_date,received_cents,mode,created_by,request_id,note,created_at FROM investment_withdrawals WHERE workspace_id=? AND investment_id=? ORDER BY withdrawal_date,id`, workspace, investment)
	if err != nil {
		return nil, err
	}
	var history []models.InvestmentWithdrawal
	indexes := make(map[string]int)
	for rows.Next() {
		var w models.InvestmentWithdrawal
		if err := rows.Scan(&w.ID, &w.WorkspaceID, &w.InvestmentID, &w.WithdrawalDate, &w.ReceivedCents, &w.Mode, &w.CreatedBy, &w.RequestID, &w.Note, &w.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		indexes[w.ID] = len(history)
		history = append(history, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT workspace_id,investment_id,withdrawal_id,purchase_id,received_cents,principal_cents,closes_purchase FROM investment_withdrawal_allocations WHERE workspace_id=? AND investment_id=? ORDER BY withdrawal_id,purchase_id`, workspace, investment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a models.InvestmentWithdrawalAllocation
		if err := rows.Scan(&a.WorkspaceID, &a.InvestmentID, &a.WithdrawalID, &a.PurchaseID, &a.ReceivedCents, &a.PrincipalCents, &a.ClosesPurchase); err != nil {
			return nil, err
		}
		index, ok := indexes[a.WithdrawalID]
		if !ok {
			return nil, errors.New("withdrawal allocation without event")
		}
		history[index].Allocations = append(history[index].Allocations, a)
	}
	return history, rows.Err()
}

func withdrawalStates(history []models.InvestmentWithdrawal) (map[string]models.InvestmentPurchaseState, int64, string, error) {
	states := make(map[string]models.InvestmentPurchaseState)
	var total int64
	latest := ""
	for _, w := range history {
		if w.ReceivedCents < 0 || total > math.MaxInt64-w.ReceivedCents {
			return nil, 0, "", withdrawalInvalid("total recebido fora do intervalo permitido")
		}
		total += w.ReceivedCents
		if w.WithdrawalDate > latest {
			latest = w.WithdrawalDate
		}
		for _, a := range w.Allocations {
			state := states[a.PurchaseID]
			state.Closed = state.Closed || a.ClosesPurchase
			if a.PrincipalCents == nil {
				state.PrincipalUnknown = true
			} else {
				if *a.PrincipalCents < 0 || state.KnownPrincipalWithdrawn > math.MaxInt64-*a.PrincipalCents {
					return nil, 0, "", withdrawalInvalid("principal fora do intervalo permitido")
				}
				state.KnownPrincipalWithdrawn += *a.PrincipalCents
			}
			if w.WithdrawalDate > state.LatestWithdrawalDate {
				state.LatestWithdrawalDate = w.WithdrawalDate
			}
			states[a.PurchaseID] = state
		}
	}
	return states, total, latest, nil
}

func enrichInvestmentWithdrawals(detail *InvestmentDetail, history []models.InvestmentWithdrawal) error {
	states, total, latest, err := withdrawalStates(history)
	if err != nil {
		return err
	}
	detail.Withdrawals, detail.PurchaseStates = history, states
	detail.TotalWithdrawnCents, detail.LatestWithdrawalDate = total, latest
	for _, p := range detail.Purchases {
		if !states[p.ID].Closed {
			detail.MaintainedPurchaseCount++
		}
	}
	if len(history) > 0 {
		// Neither historical purchases nor receipts are a return basis for the
		// remaining position. Never fabricate a difference after withdrawals.
		detail.DifferenceAvailable = false
		detail.YieldCents = 0
		detail.PositionStale = detail.PositionDate != "" && (detail.PositionStale || latest > detail.PositionDate)
	}
	return nil
}
