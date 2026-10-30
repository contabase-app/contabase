package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

var accountBalancePattern = regexp.MustCompile(`^(?:[0-9]+(?:[,.][0-9]{1,2})?|[1-9][0-9]{0,2}(?:\.[0-9]{3})+(?:,[0-9]{1,2})?|[1-9][0-9]{0,2}(?:,[0-9]{3})+(?:\.[0-9]{1,2})?)$`)

// Account balances may be zero or negative. Keep their parsing separate from
// transaction amounts, which must be positive, and never round financial input.
func parseAccountBalance(raw string) (int64, error) {
	s := strings.TrimSpace(strings.ToUpper(raw))
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "R$"))
	if !negative && strings.HasPrefix(s, "-") {
		negative = true
		s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
	}
	if !accountBalancePattern.MatchString(s) {
		return 0, fmt.Errorf("saldo inválido")
	}
	normalized, err := normalizeDecimalString(s)
	if err != nil {
		return 0, err
	}
	parts := strings.SplitN(normalized, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("saldo fora do limite")
	}
	var cents uint64
	if len(parts) == 2 {
		fraction := parts[1]
		if len(fraction) == 1 {
			fraction += "0"
		}
		cents, err = strconv.ParseUint(fraction, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("centavos inválidos")
		}
	}
	limit := uint64(math.MaxInt64)
	if negative {
		limit++
	}
	if whole > (limit-cents)/100 {
		return 0, fmt.Errorf("saldo fora do limite")
	}
	magnitude := whole*100 + cents
	if negative {
		if magnitude == uint64(math.MaxInt64)+1 {
			return math.MinInt64, nil
		}
		return -int64(magnitude), nil
	}
	return int64(magnitude), nil
}

type accountAdjustmentHistoryRow struct {
	Before, After, Delta, Reason, Author, Date string
}

type accountAdjustmentForm struct {
	ID, Name, Balance, BalanceInput, RequestID, Error, Success string
	Version                                                    int64
	History                                                    []accountAdjustmentHistoryRow
}

func (h *ConfiguracoesHandler) HandleContaBalanceAdjustmentForm(w http.ResponseWriter, r *http.Request, accountID string) {
	h.renderAccountAdjustment(w, r, accountID, "", "")
}

func (h *ConfiguracoesHandler) HandleContaBalanceAdjustment(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	version, versionErr := strconv.ParseInt(r.FormValue("balance_version"), 10, 64)
	requestID := r.FormValue("request_id")
	if _, err := uuid.Parse(requestID); err != nil || versionErr != nil || version < 0 {
		http.Error(w, "formulário inválido; reabra a conciliação", http.StatusBadRequest)
		return
	}
	target, err := parseAccountBalance(r.FormValue("target_balance"))
	if err != nil {
		h.renderAccountAdjustment(w, r, accountID, "Saldo inválido. Informe um valor com até duas casas decimais.", "")
		return
	}
	err = services.AdjustAccountBalance(r.Context(), h.DB, services.AccountBalanceAdjustment{
		ID: requestID, WorkspaceID: h.WorkspaceID, AccountID: accountID, UserID: h.UserID,
		ExpectedVersion: version, TargetBalance: target, Reason: r.FormValue("reason"), CreatedAt: time.Now().Unix(),
	})
	switch {
	case err == nil:
		h.renderAccountAdjustment(w, r, accountID, "", "Conciliação registrada. Saldo atualizado sem gerar receita ou despesa.")
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, services.ErrBalanceAdjustmentConflict):
		h.renderAccountAdjustment(w, r, accountID, "O saldo mudou desde a abertura do formulário. Confira o saldo atualizado antes de confirmar novamente.", "")
	case errors.Is(err, services.ErrBalanceAdjustmentUnchanged):
		h.renderAccountAdjustment(w, r, accountID, "O valor informado já é o saldo atual. Nenhum ajuste foi registrado.", "")
	case errors.Is(err, services.ErrBalanceOutOfRange):
		h.renderAccountAdjustment(w, r, accountID, "A diferença entre os saldos excede o limite suportado. Nenhum ajuste foi registrado.", "")
	default:
		h.renderAccountAdjustment(w, r, accountID, "Não foi possível registrar a conciliação. Confira o motivo (até 500 caracteres) e tente novamente.", "")
	}
}

func (h *ConfiguracoesHandler) renderAccountAdjustment(w http.ResponseWriter, r *http.Request, accountID, errorMessage, successMessage string) {
	data := accountAdjustmentForm{RequestID: uuid.NewString(), Error: errorMessage, Success: successMessage}
	tx, err := h.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		http.Error(w, "erro ao carregar conciliação", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	var balance int64
	err = tx.QueryRowContext(r.Context(), `SELECT id, name, current_balance, balance_version FROM accounts
		WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, accountID, h.WorkspaceID).Scan(&data.ID, &data.Name, &balance, &data.Version)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar conciliação", http.StatusInternalServerError)
		return
	}
	data.Balance = formatCurrencyLabel(balance)
	// Pre-fill the exact string the money mask produces and this file's parser
	// accepts, so an untouched submit keeps the sign and every centavo.
	data.BalanceInput = data.Balance
	rows, err := tx.QueryContext(r.Context(), `SELECT a.before_balance, a.after_balance, a.delta_cents, a.reason, u.name, a.created_at
		FROM account_balance_adjustments a JOIN users u ON u.id = a.created_by
		WHERE a.workspace_id = ? AND a.account_id = ? ORDER BY a.created_at DESC, a.expected_version DESC LIMIT 50`, h.WorkspaceID, accountID)
	if err != nil {
		http.Error(w, "erro ao carregar histórico", http.StatusInternalServerError)
		return
	}
	for rows.Next() {
		var item accountAdjustmentHistoryRow
		var before, after, delta, created int64
		if err := rows.Scan(&before, &after, &delta, &item.Reason, &item.Author, &created); err != nil {
			rows.Close()
			http.Error(w, "erro ao carregar histórico", http.StatusInternalServerError)
			return
		}
		item.Before, item.After, item.Delta = formatCurrencyLabel(before), formatCurrencyLabel(after), formatCurrencyLabel(delta)
		item.Date = appTime(time.Unix(created, 0)).Format("02/01/2006 15:04:05")
		data.History = append(data.History, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		http.Error(w, "erro ao carregar histórico", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao carregar conciliação", http.StatusInternalServerError)
		return
	}
	var body bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&body, "config-contas-conciliar", data); err != nil {
		http.Error(w, "erro ao carregar conciliação", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = body.WriteTo(w)
}
