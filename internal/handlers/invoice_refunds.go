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
	OriginalAmount       MoneyDisplay
	RefundedAmount       MoneyDisplay
	RemainingAmount      MoneyDisplay
	RemainingAmountInput string
}

type transactionRefundTarget struct {
	TransactionID string
	Description   string
	InvoiceID     string
	InvoiceStatus string
	Amount        int64
	Refunded      int64
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
	data := TransactionRefundFormData{
		TransactionID:        target.TransactionID,
		TransactionLabel:     target.Description,
		InvoiceID:            target.InvoiceID,
		OriginalAmount:       MoneyMinor(target.Amount),
		RefundedAmount:       MoneyMinor(target.Refunded),
		RemainingAmount:      MoneyMinor(remaining),
		RemainingAmountInput: formatCurrencyCentsBase(remaining),
	}
	if err := h.Templates.ExecuteTemplate(w, "form-reembolso", data); err != nil {
		http.Error(w, "erro ao exibir reembolso", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleRegistrarReembolso(w http.ResponseWriter, r *http.Request, id string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	amount, err := parseCurrency(r.FormValue("valor"))
	if err != nil || amount <= 0 {
		http.Error(w, "informe um valor de reembolso válido", http.StatusBadRequest)
		return
	}
	description := strings.TrimSpace(r.FormValue("descricao"))
	if description == "" {
		http.Error(w, "informe a descrição do reembolso", http.StatusBadRequest)
		return
	}
	if len([]rune(description)) > 240 {
		http.Error(w, "descrição do reembolso muito longa", http.StatusBadRequest)
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
