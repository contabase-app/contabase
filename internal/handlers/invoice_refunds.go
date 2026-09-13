package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/google/uuid"
)

type TransactionRefundFormData struct {
	TransactionID        string
	TransactionLabel     string
	InvoiceID            string
	RefundID             string
	OriginalAmount       MoneyDisplay
	RefundedAmount       MoneyDisplay
	RemainingAmount      MoneyDisplay
	RemainingAmountInput string
	CurrentAmountInput   string
	Description          string
	FormAction           string
	SubmitLabel          string
	IsEditing            bool
}

type transactionRefundTarget struct {
	TransactionID string
	Description   string
	InvoiceID     string
	InvoiceStatus string
	Amount        int64
	Refunded      int64
}

type activeTransactionRefund struct {
	ID          string
	Amount      int64
	Description string
}

type TransactionRefundManagementData struct {
	TransactionID    string
	TransactionLabel string
	Refunds          []TransactionRefundRow
}

func loadTransactionRefundTarget(query interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, workspaceID, transactionID string) (transactionRefundTarget, error) {
	var target transactionRefundTarget
	err := query.QueryRow(`
		SELECT t.id, t.description, t.invoice_id, i.status, t.amount,
		       COALESCE((
		           SELECT SUM(r.amount_cents)
		           FROM transaction_refunds r
		           WHERE r.workspace_id = t.workspace_id
		             AND r.transaction_id = t.id
		             AND r.reversed_at IS NULL
		       ), 0)
		FROM transactions t
		JOIN invoices i ON i.id = t.invoice_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ?
		  AND t.workspace_id = ?
		  AND t.type = ?
		  AND a.type = ?
	`, transactionID, workspaceID, models.TransactionTypeExpense, models.AccountTypeCreditCard).Scan(
		&target.TransactionID,
		&target.Description,
		&target.InvoiceID,
		&target.InvoiceStatus,
		&target.Amount,
		&target.Refunded,
	)
	return target, err
}

func loadActiveTransactionRefund(query interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, workspaceID, transactionID, refundID string) (activeTransactionRefund, error) {
	var refund activeTransactionRefund
	err := query.QueryRow(`
		SELECT id, amount_cents, description
		FROM transaction_refunds
		WHERE id = ?
		  AND workspace_id = ?
		  AND transaction_id = ?
		  AND reversed_at IS NULL
	`, refundID, workspaceID, transactionID).Scan(&refund.ID, &refund.Amount, &refund.Description)
	return refund, err
}

func queryActiveTransactionRefundRows(db *sql.DB, workspaceID, transactionID string) ([]TransactionRefundRow, error) {
	rows, err := db.Query(`
		SELECT id, amount_cents, description
		FROM transaction_refunds
		WHERE workspace_id = ?
		  AND transaction_id = ?
		  AND reversed_at IS NULL
		ORDER BY created_at DESC, id DESC
	`, workspaceID, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refunds []TransactionRefundRow
	for rows.Next() {
		var refund TransactionRefundRow
		var amount int64
		if err := rows.Scan(&refund.ID, &amount, &refund.Description); err != nil {
			return nil, err
		}
		refund.Amount = MoneyMinor(amount)
		refunds = append(refunds, refund)
	}
	return refunds, rows.Err()
}

func parseRefundForm(r *http.Request) (int64, string, error) {
	if err := r.ParseForm(); err != nil {
		return 0, "", fmt.Errorf("formulário inválido")
	}
	amount, err := parseCurrency(r.FormValue("valor"))
	if err != nil || amount <= 0 {
		return 0, "", fmt.Errorf("informe um valor de reembolso válido")
	}
	description := strings.TrimSpace(r.FormValue("descricao"))
	if description == "" {
		return 0, "", fmt.Errorf("informe a descrição do reembolso")
	}
	if len([]rune(description)) > 240 {
		return 0, "", fmt.Errorf("descrição do reembolso muito longa")
	}
	return amount, description, nil
}

func transactionRefundFormData(target transactionRefundTarget, refund activeTransactionRefund) TransactionRefundFormData {
	remaining := target.Amount - target.Refunded
	data := TransactionRefundFormData{
		TransactionID:        target.TransactionID,
		TransactionLabel:     target.Description,
		InvoiceID:            target.InvoiceID,
		OriginalAmount:       MoneyMinor(target.Amount),
		RefundedAmount:       MoneyMinor(target.Refunded),
		RemainingAmount:      MoneyMinor(remaining),
		RemainingAmountInput: formatCurrencyCentsBase(remaining),
		FormAction:           fmt.Sprintf("/transacoes/%s/reembolso", target.TransactionID),
		SubmitLabel:          "Registrar reembolso",
	}
	if refund.ID != "" {
		data.RefundID = refund.ID
		data.CurrentAmountInput = formatCurrencyCentsBase(refund.Amount)
		data.Description = refund.Description
		data.FormAction = fmt.Sprintf("/transacoes/%s/reembolsos/%s/editar", target.TransactionID, refund.ID)
		data.SubmitLabel = "Salvar reembolso"
		data.IsEditing = true
	}
	return data
}

func (h *TransactionHandler) HandleFormReembolso(w http.ResponseWriter, r *http.Request, id string) {
	target, err := loadTransactionRefundTarget(h.DB, h.WorkspaceID, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar reembolso", http.StatusInternalServerError)
		return
	}
	if target.InvoiceStatus == models.InvoiceStatusPaid {
		http.Error(w, "fatura paga não permite reembolso", http.StatusConflict)
		return
	}
	remaining := target.Amount - target.Refunded
	if remaining <= 0 {
		http.Error(w, "este lançamento já foi totalmente reembolsado", http.StatusConflict)
		return
	}
	data := transactionRefundFormData(target, activeTransactionRefund{})
	if err := h.Templates.ExecuteTemplate(w, "form-reembolso", data); err != nil {
		http.Error(w, "erro ao exibir reembolso", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleRegistrarReembolso(w http.ResponseWriter, r *http.Request, id string) {
	amount, description, err := parseRefundForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "erro ao iniciar reembolso", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{id}); err != nil {
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "fatura paga não permite reembolso", http.StatusConflict)
			return
		}
		http.Error(w, "lançamento inválido para reembolso", http.StatusConflict)
		return
	}
	target, err := loadTransactionRefundTarget(tx, h.WorkspaceID, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "apenas despesas de cartão podem receber reembolso", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "erro ao validar reembolso", http.StatusInternalServerError)
		return
	}
	if amount > target.Amount-target.Refunded {
		http.Error(w, "reembolso maior que o valor ainda disponível no lançamento", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		INSERT INTO transaction_refunds (
			id, workspace_id, transaction_id, amount_cents, description, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), h.WorkspaceID, id, amount, description, h.UserID, now); err != nil {
		http.Error(w, "não foi possível registrar o reembolso", http.StatusConflict)
		return
	}
	if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, target.InvoiceID, now); err != nil {
		respondInvoiceCompositionError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao concluir reembolso", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", `{"refreshFinancials":true,"app:toast-success":{"value":"Reembolso registrado"}}`)
	if err := h.renderInvoiceContextOOB(w, r, target.InvoiceID, true); err != nil {
		http.Error(w, fmt.Sprintf("reembolso registrado; erro ao atualizar fatura: %v", err), http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleFormEditarReembolso(w http.ResponseWriter, r *http.Request, transactionID, refundID string) {
	target, err := loadTransactionRefundTarget(h.DB, h.WorkspaceID, transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar reembolso", http.StatusInternalServerError)
		return
	}
	if target.InvoiceStatus == models.InvoiceStatusPaid {
		http.Error(w, "fatura paga não permite alterar reembolso", http.StatusConflict)
		return
	}
	refund, err := loadActiveTransactionRefund(h.DB, h.WorkspaceID, transactionID, refundID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar reembolso", http.StatusInternalServerError)
		return
	}
	if err := h.Templates.ExecuteTemplate(w, "form-reembolso", transactionRefundFormData(target, refund)); err != nil {
		http.Error(w, "erro ao exibir reembolso", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleGerenciarReembolsos(w http.ResponseWriter, r *http.Request, transactionID string) {
	target, err := loadTransactionRefundTarget(h.DB, h.WorkspaceID, transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar reembolsos", http.StatusInternalServerError)
		return
	}
	if target.InvoiceStatus == models.InvoiceStatusPaid {
		http.Error(w, "fatura paga não permite alterar reembolso", http.StatusConflict)
		return
	}
	refunds, err := queryActiveTransactionRefundRows(h.DB, h.WorkspaceID, transactionID)
	if err != nil {
		http.Error(w, "erro ao carregar reembolsos", http.StatusInternalServerError)
		return
	}
	if len(refunds) == 0 {
		http.NotFound(w, r)
		return
	}
	if err := h.Templates.ExecuteTemplate(w, "reembolsos-lancamento", TransactionRefundManagementData{
		TransactionID: transactionID, TransactionLabel: target.Description, Refunds: refunds,
	}); err != nil {
		http.Error(w, "erro ao exibir reembolsos", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleAtualizarReembolso(w http.ResponseWriter, r *http.Request, transactionID, refundID string) {
	amount, description, err := parseRefundForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "erro ao iniciar atualização do reembolso", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{transactionID}); err != nil {
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "fatura paga não permite alterar reembolso", http.StatusConflict)
			return
		}
		http.Error(w, "lançamento inválido para reembolso", http.StatusConflict)
		return
	}
	target, err := loadTransactionRefundTarget(tx, h.WorkspaceID, transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao validar reembolso", http.StatusInternalServerError)
		return
	}
	refund, err := loadActiveTransactionRefund(tx, h.WorkspaceID, transactionID, refundID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar reembolso", http.StatusInternalServerError)
		return
	}
	if amount > target.Amount-target.Refunded+refund.Amount {
		http.Error(w, "reembolso maior que o valor ainda disponível no lançamento", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE transaction_refunds SET reversed_at = ?, reversed_by = ? WHERE id = ? AND workspace_id = ? AND transaction_id = ? AND reversed_at IS NULL`, now, h.UserID, refundID, h.WorkspaceID, transactionID); err != nil {
		http.Error(w, "não foi possível atualizar o reembolso", http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`INSERT INTO transaction_refunds (id, workspace_id, transaction_id, amount_cents, description, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), h.WorkspaceID, transactionID, amount, description, h.UserID, now); err != nil {
		http.Error(w, "não foi possível atualizar o reembolso", http.StatusConflict)
		return
	}
	if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, target.InvoiceID, now); err != nil {
		respondInvoiceCompositionError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao concluir atualização do reembolso", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", `{"refreshFinancials":true,"app:toast-success":{"value":"Reembolso atualizado"}}`)
	if err := h.renderInvoiceContextOOB(w, r, target.InvoiceID, true); err != nil {
		http.Error(w, fmt.Sprintf("reembolso atualizado; erro ao atualizar fatura: %v", err), http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleRemoverReembolso(w http.ResponseWriter, r *http.Request, transactionID, refundID string) {
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "erro ao iniciar remoção do reembolso", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{transactionID}); err != nil {
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "fatura paga não permite remover reembolso", http.StatusConflict)
			return
		}
		http.Error(w, "lançamento inválido para reembolso", http.StatusConflict)
		return
	}
	target, err := loadTransactionRefundTarget(tx, h.WorkspaceID, transactionID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao validar reembolso", http.StatusInternalServerError)
		return
	}
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE transaction_refunds SET reversed_at = ?, reversed_by = ? WHERE id = ? AND workspace_id = ? AND transaction_id = ? AND reversed_at IS NULL`, now, h.UserID, refundID, h.WorkspaceID, transactionID)
	if err != nil {
		http.Error(w, "não foi possível remover o reembolso", http.StatusInternalServerError)
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		http.Error(w, "não foi possível remover o reembolso", http.StatusInternalServerError)
		return
	}
	if affected != 1 {
		http.NotFound(w, r)
		return
	}
	if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, target.InvoiceID, now); err != nil {
		respondInvoiceCompositionError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao concluir remoção do reembolso", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", `{"refreshFinancials":true,"app:toast-success":{"value":"Reembolso removido"}}`)
	if err := h.renderInvoiceContextOOB(w, r, target.InvoiceID, true); err != nil {
		http.Error(w, fmt.Sprintf("reembolso removido; erro ao atualizar fatura: %v", err), http.StatusInternalServerError)
	}
}
