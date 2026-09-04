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

// InvoiceGeneralRefundFormData represents a credit received in the current
// invoice whose original purchase belongs to an earlier billing cycle.
type InvoiceGeneralRefundFormData struct {
	InvoiceID          string
	CardName           string
	Reference          string
	PendingAmount      MoneyDisplay
	AlreadyRefunded    MoneyDisplay
	HasAlreadyRefunded bool
}

type invoiceGeneralRefundTarget struct {
	InvoiceID string
	CardName  string
	Reference string
	Status    string
}

func loadInvoiceGeneralRefundTarget(query interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, workspaceID, invoiceID string) (invoiceGeneralRefundTarget, error) {
	var target invoiceGeneralRefundTarget
	err := query.QueryRow(`
		SELECT i.id, a.name, i.reference, i.status
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ?
		  AND a.workspace_id = ?
		  AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(
		&target.InvoiceID,
		&target.CardName,
		&target.Reference,
		&target.Status,
	)
	return target, err
}

func sumActiveInvoiceGeneralRefunds(query interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}, workspaceID, invoiceID string) (int64, error) {
	var total int64
	err := query.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM invoice_refunds
		WHERE workspace_id = ?
		  AND invoice_id = ?
		  AND reversed_at IS NULL
	`, workspaceID, invoiceID).Scan(&total)
	return total, err
}

func (h *FaturasHandler) HandleFormReembolsoGeral(w http.ResponseWriter, r *http.Request) {
	invoiceID := strings.TrimSpace(r.URL.Query().Get("invoice_id"))
	if invoiceID == "" {
		http.Error(w, "fatura obrigatória", http.StatusBadRequest)
		return
	}
	target, err := loadInvoiceGeneralRefundTarget(h.DB, h.WorkspaceID, invoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao carregar fatura", http.StatusInternalServerError)
		return
	}
	if target.Status == models.InvoiceStatusPaid {
		http.Error(w, "fatura paga não permite reembolso", http.StatusConflict)
		return
	}
	snapshot, err := loadInvoiceFinancialSnapshotDB(h.DB, h.WorkspaceID, invoiceID)
	if err != nil {
		http.Error(w, "erro ao calcular saldo da fatura", http.StatusInternalServerError)
		return
	}
	pending := CalculateInvoicePendingAmount(snapshot.Total, snapshot.EffectivePaid)
	if pending <= 0 {
		http.Error(w, "esta fatura não possui saldo disponível para reembolso", http.StatusConflict)
		return
	}
	refunded, err := sumActiveInvoiceGeneralRefunds(h.DB, h.WorkspaceID, invoiceID)
	if err != nil {
		http.Error(w, "erro ao carregar reembolsos", http.StatusInternalServerError)
		return
	}
	data := InvoiceGeneralRefundFormData{
		InvoiceID:          target.InvoiceID,
		CardName:           target.CardName,
		Reference:          target.Reference,
		PendingAmount:      MoneyMinor(pending),
		AlreadyRefunded:    MoneyMinor(refunded),
		HasAlreadyRefunded: refunded > 0,
	}
	if err := h.Templates.ExecuteTemplate(w, "form-reembolso-fatura", data); err != nil {
		http.Error(w, "erro ao exibir reembolso", http.StatusInternalServerError)
	}
}

func (h *FaturasHandler) HandleRegistrarReembolsoGeral(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	invoiceID := strings.TrimSpace(r.FormValue("invoice_id"))
	if invoiceID == "" {
		http.Error(w, "fatura obrigatória", http.StatusBadRequest)
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

	target, err := loadInvoiceGeneralRefundTarget(tx, h.WorkspaceID, invoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "erro ao validar fatura", http.StatusInternalServerError)
		return
	}
	if target.Status == models.InvoiceStatusPaid {
		http.Error(w, "fatura paga não permite reembolso", http.StatusConflict)
		return
	}
	snapshot, err := loadInvoiceFinancialSnapshotTx(tx, h.WorkspaceID, invoiceID)
	if err != nil {
		http.Error(w, "erro ao calcular saldo da fatura", http.StatusInternalServerError)
		return
	}
	pending := CalculateInvoicePendingAmount(snapshot.Total, snapshot.EffectivePaid)
	if amount > pending {
		http.Error(w, "reembolso maior que o saldo pendente da fatura", http.StatusConflict)
		return
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		INSERT INTO invoice_refunds (
			id, workspace_id, invoice_id, amount_cents, description, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), h.WorkspaceID, invoiceID, amount, description, h.UserID, now); err != nil {
		http.Error(w, "não foi possível registrar o reembolso", http.StatusConflict)
		return
	}
	if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, invoiceID, now); err != nil {
		respondInvoiceCompositionError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao concluir reembolso", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", `{"refreshFinancials":true,"app:toast-success":{"value":"Reembolso geral registrado"}}`)
	transactionRenderer := TransactionHandler{
		DB:          h.DB,
		Templates:   h.Templates,
		WorkspaceID: h.WorkspaceID,
		UserID:      h.UserID,
	}
	if err := transactionRenderer.renderInvoiceContextOOB(w, r, target.InvoiceID, true); err != nil {
		http.Error(w, fmt.Sprintf("reembolso registrado; erro ao atualizar fatura: %v", err), http.StatusInternalServerError)
	}
}
