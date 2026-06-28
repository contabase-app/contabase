package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
)

var errPaidInvoiceMutationBlocked = errors.New("transacao vinculada a fatura paga")
var errBoxReserveInsufficient = errors.New("saldo reservado insuficiente para consumo da caixinha")
var errBoxCategoryAmbiguous = errors.New("categoria vinculada a múltiplas caixinhas")

type FormAccount struct {
	ID               string
	Name             string
	Icon             string
	Color            string
	ProviderSlug     string
	ProviderMark     string
	ProviderName     string
	Tipo             string
	ClosingDay       string
	DueDay           string
	CreditLimitLabel string
}

type FormCategory struct {
	ID                 string
	Name               string
	Icon               string
	Color              string
	Type               string
	BoxID              string
	BoxReservedBalance int64
	BoxName            string
	LimitMax           int64
	LimitSpent         int64
}

type InvoiceOption struct {
	ID            string
	Reference     string
	ReferenceLabel string
	Status        string
	StatusLabel   string
	CycleLabel    string
	FinLabel      string
	DueLabel      string
	ClosingLabel  string
	TotalDisplay  string
	PendingDisplay string
	IsSensitive   bool
}

type FormTransacaoData struct {
	Accounts              []FormAccount
	Contacts              []ContatoRow
	Categories            []FormCategory
	FaturaOptions         []InvoiceOption
	IsBusiness            bool
	IsEdit                bool
	EditID                string
	TipoInicial           string
	ValorPreenchido       string
	DescricaoPreenchida   string
	AnotacoesPreenchidas  string
	AttachmentViewURL     string
	AttachmentDownloadURL string
	DataPreenchida        string
	StatusInicial         string
	OrigemContaID         string
	OrigemTipo            string
	OrigemNome            string
	OrigemIcon            string
	OrigemColor           string
	OrigemProviderMark    string
	CategoriaID           string
	CategoriaNome         string
	CategoriaIcon         string
	CategoriaColor        string
	CategoriaType         string
	DestinoContaID        string
	DestinoNome           string
	DestinoIcon           string
	DestinoColor          string
	DestinoProviderMark   string
	IsSeriesEdit          bool
	FixoInicial           bool
	RecorrenciaInicial    string
	DueDatePreenchida     string
	ContatoIDPreenchido   string
	ReturnInvoiceID       string
	AuditCreatedBy        string
	AuditUpdatedAt        string
}

type PredictiveSuggestion struct {
	Description            string
	AccountID              string
	AccountName            string
	AccountKind            string
	CategoryID             string
	CategoryName           string
	DestinationAccountID   string
	DestinationAccountName string
	Type                   string
	Frequency              int64
}

type PredictiveSuggestionsData struct {
	Suggestions []PredictiveSuggestion
}

type TransacaoReciboData struct {
	TransactionID         string
	WorkspaceName         string
	WorkspaceDocument     string
	WorkspaceAddress      string
	WorkspacePhone        string
	WorkspaceLogoURL      string
	WorkspaceLogoLightURL string
	WorkspaceInitials     string
	ContactName           string
	ContactDocument       string
	Description           string
	AmountLabel           string
	DateLabel             string
	IssueDateLabel        string
	DeclarationVerb       string
	DeclarationPrep       string
	WarningMessage        string
	CanPrint              bool
}

type TransactionHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

func (h *TransactionHandler) HandleNovaTransacao(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	accounts, err := h.queryFormAccounts()
	perfStep(reqID, "NovaTransacao", "queryFormAccounts", time.Since(t0))
	if err != nil {
		log.Printf("query accounts error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	t1 := time.Now()
	categories, err := h.queryFormCategories()
	perfStep(reqID, "NovaTransacao", "queryFormCategories", time.Since(t1))
	if err != nil {
		log.Printf("query categories error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	t2 := time.Now()
	data := newFormTransacaoData(accounts, categories)
	data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == "business"
	if data.IsBusiness {
		data.Contacts, _ = h.queryFormContacts()
	}
	perfStep(reqID, "NovaTransacao", "formData+contacts", time.Since(t2))

	tipoParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tipo")))
	if tipoParam == "receita" || tipoParam == "transferencia" || tipoParam == "despesa" {
		data.TipoInicial = tipoParam
		if tipoParam == "receita" {
			if cat, ok := preferredFormCategory(categories, "", "INCOME"); ok {
				applyFormCategory(&data, cat)
			}
		} else if tipoParam == "transferencia" {
			data.CategoriaID = ""
			data.CategoriaNome = ""
			data.CategoriaIcon = ""
			data.CategoriaColor = ""
			data.CategoriaType = ""
		} else if tipoParam == "despesa" {
			if cat, ok := preferredFormCategory(categories, "Alimentação", "EXPENSE"); ok {
				applyFormCategory(&data, cat)
			} else if cat, ok := preferredFormCategory(categories, "", "EXPENSE"); ok {
				applyFormCategory(&data, cat)
			}
		}
	}

	t3 := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
		log.Printf("template form-lancamento error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "NovaTransacao", "templateRender", time.Since(t3))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "NovaTransacao", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

var accentReplacer = strings.NewReplacer(
	"á", "a", "à", "a", "ã", "a", "â", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "õ", "o", "ô", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c",
	"ñ", "n",
	"Á", "A", "À", "A", "Ã", "A", "Â", "A", "Ä", "A",
	"É", "E", "È", "E", "Ê", "E", "Ë", "E",
	"Í", "I", "Ì", "I", "Î", "I", "Ï", "I",
	"Ó", "O", "Ò", "O", "Õ", "O", "Ô", "O", "Ö", "O",
	"Ú", "U", "Ù", "U", "Û", "U", "Ü", "U",
	"Ç", "C",
	"Ñ", "N",
)

func normalizeAccents(s string) string {
	return strings.ToLower(accentReplacer.Replace(s))
}

func (h *TransactionHandler) HandleTransacaoPreditiva(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("descricao"))
	}

	tipoParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tipo")))
	txType := mapTransactionType(tipoParam)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len([]rune(q)) < 2 {
		return
	}

	suggestions, err := h.queryPredictiveSuggestions(q, txType)
	if err != nil {
		log.Printf("query predictive suggestions error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if len(suggestions) == 0 {
		return
	}

	if err := h.Templates.ExecuteTemplate(w, "transacoes-preditiva", PredictiveSuggestionsData{Suggestions: suggestions}); err != nil {
		log.Printf("template transacoes-preditiva error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleSalvarTransacao(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	if err := parseMultipartOrForm(r); err != nil {
		respondTransactionValidationError(w, "Não foi possível ler o formulário do lançamento. Recarregue e tente novamente.", http.StatusBadRequest)
		return
	}

	tipoForm := r.FormValue("tipo")
	valorStr := r.FormValue("valor")
	descricao := r.FormValue("descricao")
	anotacoes := strings.TrimSpace(r.FormValue("anotacoes"))
	dataStr := r.FormValue("data")
	origemContaID := r.FormValue("origem_conta_id")
	destinoContaID := r.FormValue("destino_conta_id")
	categoriaID := r.FormValue("categoria_id")
	parceladaStr := r.FormValue("parcelada")
	allowBoxOverdraft := formAllowsBoxOverdraft(r)
	fixo := r.FormValue("lancamento_fixo") == "on"
	recorrencia := normalizeRecurrenceFrequency(r.FormValue("recorrencia"))
	totalOccurrencesStr := strings.TrimSpace(r.FormValue("total_occurrences"))
	var totalOccurrences *int64
	if fixo && totalOccurrencesStr != "" {
		if n, err := strconv.ParseInt(totalOccurrencesStr, 10, 64); err == nil && n >= 2 && n <= 360 {
			totalOccurrences = &n
		}
	}
	paymentStatus := r.FormValue("status_pagamento")
	if paymentStatus != "pending" {
		paymentStatus = "paid"
	}
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	dueDateUnix, hasDueDate := int64(0), false
	if isBusiness {
		if paymentStatus != "paid" {
			dueRaw := strings.TrimSpace(r.FormValue("due_date"))
			if dueRaw != "" {
				parsed, err := parseDate(dueRaw)
				if err != nil {
					respondTransactionValidationError(w, "A data de vencimento informada é inválida. Use um valor válido no formato de calendário.", http.StatusUnprocessableEntity)
					return
				}
				dueDateUnix = parsed
				hasDueDate = true
			}
		}
	}
	contactID := strings.TrimSpace(r.FormValue("contact_id"))
	if !isBusiness {
		contactID = ""
	}

	tipo := mapTransactionType(tipoForm)
	if tipo == "TRANSFER" {
		paymentStatus = "paid"
	}

	// Isolation: in Personal workspaces, we bypass commercial logic (due_date, contacts)
	// and force all credit card expenses to be 'paid' to avoid hiding them.
	if !isBusiness && paymentStatus == "pending" && tipo == "EXPENSE" && origemContaID != "" {
		var accType string
		if err := h.DB.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, origemContaID, h.WorkspaceID).Scan(&accType); err == nil && accType == "CREDIT_CARD" {
			paymentStatus = "paid"
		}
	}

	amountCents, err := parseCurrency(valorStr)
	if err != nil || amountCents <= 0 {
		log.Printf("invalid amount: %s (err=%v)", valorStr, err)
		respondTransactionFieldError(w, "valor", "O valor do lançamento é obrigatório e deve ser maior que zero.")
		return
	}

	if strings.TrimSpace(descricao) == "" {
		respondTransactionFieldError(w, "descricaoLancamento", "A descrição do lançamento é obrigatória.")
		return
	}

	dateUnix, err := parseDate(dataStr)
	if err != nil {
		log.Printf("invalid date: %s", dataStr)
		respondTransactionValidationError(w, "A Data de Competência é obrigatória e deve ser válida.", http.StatusUnprocessableEntity)
		return
	}

	totalInstallments := int64(1)
	if parceladaStr == "on" && !fixo {
		parcelasStr := r.FormValue("parcelas")
		n, err := strconv.ParseInt(parcelasStr, 10, 64)
		if err != nil || n < 2 || n > 12 {
			respondTransactionValidationError(w, "Para parcelamento, selecione entre 2 e 12 parcelas.", http.StatusUnprocessableEntity)
			return
		}
		totalInstallments = n
	}

	if tipo == "TRANSFER" {
		if origemContaID == "" || destinoContaID == "" {
			respondTransactionValidationError(w, "Por favor, selecione contas de origem e destino válidas para concluir a transferência.", http.StatusUnprocessableEntity)
			return
		}
	} else {
		if origemContaID == "" {
			respondTransactionValidationError(w, "Por favor, selecione uma conta de origem válida para salvar a despesa.", http.StatusUnprocessableEntity)
			return
		}
		if tipo != "TRANSFER" && categoriaID == "" {
			respondTransactionFieldError(w, "categoria", "Selecione uma categoria válida.")
			return
		}
	}

	attachmentPath, err := h.persistTransactionUpload(r)
	if err != nil {
		respondTransactionValidationError(w, "O anexo enviado é inválido ou não é permitido. Envie PDF, JPG, PNG ou WEBP de até 10MB.", http.StatusUnprocessableEntity)
		return
	}

	invoiceIDOverride := strings.TrimSpace(r.FormValue("invoice_id"))
	if r.FormValue("fatura_offset") == "next" {
		invoiceIDOverride = "NEXT_INVOICE"
	}

	perfStep(reqID, "SalvarTransacao", "parse+validate", time.Since(t0))
	tInsert := time.Now()
	dbB := dbSnap(h.DB)
	assignedInvoiceID, err := h.insertTransaction(tipo, amountCents, descricao, anotacoes, attachmentPath, dateUnix, origemContaID, destinoContaID, categoriaID, totalInstallments, paymentStatus, fixo, recorrencia, contactID, dueDateUnix, hasDueDate, totalOccurrences, invoiceIDOverride, allowBoxOverdraft)
	dbA := dbSnap(h.DB)
	perfStep(reqID, "SalvarTransacao", "insertTransaction", time.Since(tInsert))
	perfDBDelta(reqID, "SalvarTransacao", "insertTransaction", dbB, dbA)
	if err != nil {
		if errors.Is(err, errBoxReserveInsufficient) {
			respondTransactionValidationError(w, "Saldo reservado insuficiente na reserva vinculada para esta despesa. Marque a confirmação de excedente para continuar.", http.StatusUnprocessableEntity)
			return
		}
		if errors.Is(err, errBoxCategoryAmbiguous) {
			respondTransactionValidationError(w, "Há mais de uma reserva vinculada para esta categoria. Ajuste a configuração antes de salvar o lançamento.", http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(err.Error(), "conta não autorizada") || strings.Contains(err.Error(), "conta de origem arquivada") || strings.Contains(err.Error(), "conta de destino arquivada") {
			respondTransactionValidationError(w, "Por favor, selecione uma conta de origem válida para salvar o lançamento.", http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(err.Error(), "categoria não autorizada") {
			respondTransactionValidationError(w, "A categoria selecionada é inválida para este workspace.", http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(err.Error(), "contato não autorizado") {
			respondTransactionValidationError(w, "O contato selecionado é inválido para este workspace.", http.StatusUnprocessableEntity)
			return
		}
		if strings.Contains(err.Error(), "cartão de crédito sem configuração") {
			respondTransactionValidationError(w, "Este cartão de crédito não possui configuração de fechamento e vencimento. Configure o cartão em Ajustes > Cartões para usar faturas.", http.StatusUnprocessableEntity)
			return
		}
		log.Printf("insert transaction error: %v", err)
		http.Error(w, "erro ao salvar transacao", http.StatusInternalServerError)
		return
	}

	t1 := time.Now()
	var buf bytes.Buffer

	w.Header().Set("HX-Trigger", "refreshFinancials")

	if assignedInvoiceID != "" {
		invoiceData, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, assignedInvoiceID, "desc")
		if err == nil {
			invoiceData.OOB = true
			for _, templateName := range []string{"invoice-summary", "invoice-transactions"} {
				if h.Templates.Lookup(templateName) == nil {
					continue
				}
				if err := h.Templates.ExecuteTemplate(&buf, templateName, invoiceData); err != nil {
					log.Printf("template invoice oob after save error: %v", err)
				}
			}
		}
	}
	if invoicePageData, ok := h.faturaDataForCurrentRequest(r, assignedInvoiceID); ok {
		fmt.Fprint(&buf, `<div id="main-content" hx-swap-oob="innerHTML">`)
		if err := h.Templates.ExecuteTemplate(&buf, "faturas-content", invoicePageData); err != nil {
			log.Printf("template fatura page after save error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(&buf, `</div>`)
	}
	perfStep(reqID, "SalvarTransacao", "oobRender", time.Since(t1))

	fmt.Fprint(&buf, `<div id="bottom-sheet-container" hx-swap-oob="true"></div>`)
	fmt.Fprint(&buf, `<div id="lancamento-form-error" hx-swap-oob="true" class="hidden"></div>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

func (h *TransactionHandler) insertTransaction(tipo string, amountCents int64, descricao string, anotacoes string, attachmentPath string, dateUnix int64, origemContaID string, destinoContaID string, categoriaID string, totalInstallments int64, paymentStatus string, fixed bool, recurrenceFrequency string, contactID string, dueDateUnix int64, hasDueDate bool, totalOccurrences *int64, invoiceIDOverride string, allowBoxOverdraft bool) (string, error) {
	tx, err := h.DB.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var accType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, origemContaID, h.WorkspaceID).Scan(&accType); err != nil {
		return "", fmt.Errorf("conta de origem arquivada ou não encontrada: %w", err)
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return "", err
	}
	if err := ensureAccountActiveTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return "", err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return "", err
	}
	if err := ensureContactInWorkspaceTx(tx, contactID, h.WorkspaceID); err != nil {
		return "", err
	}

	now := time.Now().Unix()
	var recurringRuleID interface{}
	var projectionRule *recurrenceProjectionRule
	if fixed {
		ruleID := uuid.NewString()
		ruleStatus := recurringRuleDefaultStatus(paymentStatus, accType, tipo)
		var catID interface{}
		if categoriaID != "" {
			catID = categoriaID
		}
		var destID interface{}
		if destinoContaID != "" {
			destID = destinoContaID
		}
		if err := execOneTx(tx, `
			INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, destination_account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, total_occurrences, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		`, ruleID, h.WorkspaceID, h.UserID, origemContaID, destID, catID, tipo, amountCents, descricao, dateUnix, recurrenceFrequency, ruleStatus, totalOccurrences, now, now); err != nil {
			return "", fmt.Errorf("insert recurring rule: %w", err)
		}
		recurringRuleID = ruleID
		projectionRule = &recurrenceProjectionRule{
			ID:                   ruleID,
			AccountID:            origemContaID,
			DestinationAccountID: destinoContaID,
			CategoryID:           categoriaID,
			Type:                 tipo,
			Amount:               amountCents,
			Description:          descricao,
			StartDate:            dateUnix,
			Frequency:            recurrenceFrequency,
			DefaultPaymentStatus: ruleStatus,
			TotalOccurrences:     totalOccurrences,
		}
		totalInstallments = 1
	}

	installmentAmount := amountCents / totalInstallments
	remainder := amountCents % totalInstallments
	parentID := uuid.NewString()
	firstInstallmentAmount := installmentAmount + remainder
	var assignedInvoiceID string
	var overrideReference string

	touchedTxIDs := make([]string, 0, totalInstallments)
	for i := int64(1); i <= totalInstallments; i++ {
		id := uuid.NewString()
		if i == 1 && totalInstallments > 1 {
			id = parentID
		}
		touchedTxIDs = append(touchedTxIDs, id)
		installmentDate := safeAddMonths(dateUnix, i-1)

		var catID interface{}
		if categoriaID != "" {
			catID = categoriaID
		}

		var destID interface{}
		if destinoContaID != "" {
			destID = destinoContaID
		}

		var pid interface{}
		if i == 1 {
			pid = nil
		} else {
			pid = parentID
		}

		amount := installmentAmount
		if i == 1 {
			amount += remainder
		}
		installmentStatus := paymentStatus
		if totalInstallments > 1 && i > 1 {
			installmentStatus = "pending"
		}
		// Cartão de crédito: todos os lançamentos são sempre "paid" (já consomem limite).
		if accType == "CREDIT_CARD" {
			installmentStatus = "paid"
		}
		var dueDate interface{}
		if hasDueDate {
			dueDate = dueDateUnix
		}
		var contactRef interface{}
		if contactID != "" {
			contactRef = contactID
		}

		var invoiceID interface{}
		if tipo == "EXPENSE" && accType == "CREDIT_CARD" {
			if invoiceIDOverride == "NEXT_INVOICE" && i == 1 {
				nextInvID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, h.WorkspaceID, origemContaID, installmentDate, "next")
				if err != nil {
					return "", fmt.Errorf("next invoice: %w", err)
				}
				invoiceID = nextInvID
				if assignedInvoiceID == "" {
					assignedInvoiceID = nextInvID
				}
				invoiceIDOverride = "" // Clear so installments follow normally
			} else if invoiceIDOverride != "" && invoiceIDOverride != "NEXT_INVOICE" && i == 1 {
				var validInvoice int
				var overrideRef string
				err := tx.QueryRow(`
					SELECT COUNT(1), COALESCE(MAX(i.reference), '') FROM invoices i
					JOIN accounts a ON a.id = i.account_id
					WHERE i.id = ? AND i.account_id = ? AND a.workspace_id = ?
				`, invoiceIDOverride, origemContaID, h.WorkspaceID).Scan(&validInvoice, &overrideRef)
				if err != nil || validInvoice == 0 {
					return "", fmt.Errorf("fatura inválida ou não pertence ao cartão")
				}
				overrideReference = overrideRef
				invoiceID = invoiceIDOverride
				if assignedInvoiceID == "" {
					assignedInvoiceID = invoiceIDOverride
				}
			} else if invoiceIDOverride != "" && totalInstallments > 1 && overrideReference != "" {
				nextInvoiceID, err := ensureInvoiceAfterReferenceTx(tx, h.WorkspaceID, origemContaID, overrideReference)
				if err != nil {
					return "", fmt.Errorf("invoice override installments: %w", err)
				}
				invoiceID = nextInvoiceID
				year, month, err := parseInvoiceReference(overrideReference)
				if err != nil {
					return "", err
				}
				year, month = nextInvoiceMonth(year, month)
				overrideReference = fmt.Sprintf("%04d-%02d", year, month)
			} else {
				invID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, h.WorkspaceID, origemContaID, installmentDate, "auto")
				if err != nil {
					return "", fmt.Errorf("invoice: %w", err)
				}
				invoiceID = invID
				if assignedInvoiceID == "" {
					assignedInvoiceID = invID
				}
			}
		}

		err := execOneTx(tx, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, category_id, invoice_id, type, amount, date, description, notes, attachment_path, installment_number, total_installments, parent_id, recurring_rule_id, recurrence_sequence, status, due_date, contact_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, h.WorkspaceID, h.UserID, origemContaID, destID, catID, invoiceID, tipo, amount, installmentDate, descricao, anotacoes, attachmentPath, i, totalInstallments, pid, recurringRuleID, recurrenceSequence(i, fixed), installmentStatus, dueDate, contactRef, now, now)
		if err != nil {
			return "", fmt.Errorf("insert transaction: %w", err)
		}
		if err := h.consumeBoxReserveOnExpenseCreationTx(tx, tipo, categoriaID, amount, id, installmentDate, now, allowBoxOverdraft); err != nil {
			return "", err
		}
	}

	applyAmount := amountCents
	if totalInstallments > 1 {
		applyAmount = firstInstallmentAmount
	}
	if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, accType, paymentStatus, applyAmount, origemContaID, destinoContaID, now); err != nil {
		return "", fmt.Errorf("balance effect: %w", err)
	}
	if projectionRule != nil {
		if err := h.generateRecurrenceProjection(tx, *projectionRule, now); err != nil {
			return "", fmt.Errorf("generate recurrence projection: %w", err)
		}
	}

	if len(touchedTxIDs) > 0 {
		if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, touchedTxIDs); err != nil {
			return "", fmt.Errorf("reconcile touched invoice: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return assignedInvoiceID, nil
}

func (h *TransactionHandler) consumeBoxReserveOnExpenseCreationTx(tx *sql.Tx, transactionType, categoryID string, amount int64, transactionID string, referenceDate int64, now int64, allowBoxOverdraft bool) error {
	if transactionType != models.TransactionTypeExpense {
		return nil
	}
	if categoryID == "" || amount <= 0 || transactionID == "" {
		return nil
	}

	boxID, reserved, found, err := findBoxForCategoryConsumptionTx(tx, h.WorkspaceID, categoryID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if reserved < amount && !allowBoxOverdraft {
		return fmt.Errorf("%w: reservado=%d despesa=%d", errBoxReserveInsufficient, reserved, amount)
	}

	return insertConsumeLedgerEventTx(tx, boxID, transactionID, amount, referenceDate, now)
}

func findBoxForCategoryConsumptionTx(tx *sql.Tx, workspaceID, categoryID string) (string, int64, bool, error) {
	rows, err := tx.Query(`
		SELECT
			b.id,
			COALESCE(SUM(l.amount), 0) AS reserved
		FROM boxes b
		JOIN categories c ON c.workspace_id = b.workspace_id
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE c.id = ?
		  AND c.workspace_id = ?
		  AND (
			b.category_id = c.id OR
			(COALESCE(c.parent_id, '') != '' AND b.category_id = c.parent_id)
		  )
		GROUP BY b.id
		ORDER BY b.id
	`, categoryID, workspaceID)
	if err != nil {
		return "", 0, false, err
	}
	defer rows.Close()

	type candidate struct {
		id       string
		reserved int64
	}
	var matches []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.reserved); err != nil {
			return "", 0, false, err
		}
		matches = append(matches, c)
	}
	if err := rows.Err(); err != nil {
		return "", 0, false, err
	}

	if len(matches) == 0 {
		return "", 0, false, nil
	}
	if len(matches) > 1 {
		return "", 0, false, errBoxCategoryAmbiguous
	}
	return matches[0].id, matches[0].reserved, true, nil
}

func insertConsumeLedgerEventTx(tx *sql.Tx, boxID, sourceTransactionID string, amount int64, referenceDate int64, now int64) error {
	if boxID == "" || sourceTransactionID == "" || amount <= 0 {
		return nil
	}

	var exists int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger l
		WHERE l.box_id = ?
		  AND l.source_transaction_id = ?
		  AND l.type = 'CONSUME'
		  AND NOT EXISTS (
			SELECT 1
			FROM box_virtual_ledger r
			WHERE r.reversal_of_ledger_id = l.id
			  AND r.type = 'REVERSAL'
		  )
	`, boxID, sourceTransactionID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}

	return execOneTx(tx, `
		INSERT INTO box_virtual_ledger (
			id,
			box_id,
			amount,
			type,
			description,
			source_transaction_id,
			reversal_of_ledger_id,
			reference_date,
			created_at
		) VALUES (?, ?, ?, 'CONSUME', ?, ?, NULL, ?, ?)
	`, uuid.NewString(), boxID, -amount, "Consumo automático por lançamento", sourceTransactionID, referenceDate, now)
}

type activeConsumeEvent struct {
	LedgerID string
	BoxID    string
	Amount   int64
}

func activeConsumeEventsBySourceTransactionTx(tx *sql.Tx, workspaceID, sourceTransactionID string) ([]activeConsumeEvent, error) {
	rows, err := tx.Query(`
		SELECT
			l.id,
			l.box_id,
			ABS(l.amount)
		FROM box_virtual_ledger l
		JOIN boxes b ON b.id = l.box_id
		WHERE b.workspace_id = ?
		  AND l.source_transaction_id = ?
		  AND l.type = 'CONSUME'
		  AND NOT EXISTS (
			SELECT 1
			FROM box_virtual_ledger r
			WHERE r.reversal_of_ledger_id = l.id
			  AND r.type = 'REVERSAL'
		  )
		ORDER BY l.created_at ASC
	`, workspaceID, sourceTransactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []activeConsumeEvent
	for rows.Next() {
		var event activeConsumeEvent
		if err := rows.Scan(&event.LedgerID, &event.BoxID, &event.Amount); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func insertReversalLedgerEventTx(tx *sql.Tx, consumeEvent activeConsumeEvent, sourceTransactionID string, referenceDate int64, now int64) error {
	if consumeEvent.LedgerID == "" || consumeEvent.BoxID == "" || consumeEvent.Amount <= 0 || sourceTransactionID == "" {
		return nil
	}

	var exists int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM box_virtual_ledger
		WHERE reversal_of_ledger_id = ?
		  AND type = 'REVERSAL'
	`, consumeEvent.LedgerID).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}

	return execOneTx(tx, `
		INSERT INTO box_virtual_ledger (
			id,
			box_id,
			amount,
			type,
			description,
			source_transaction_id,
			reversal_of_ledger_id,
			reference_date,
			created_at
		) VALUES (?, ?, ?, 'REVERSAL', ?, ?, ?, ?, ?)
	`, uuid.NewString(), consumeEvent.BoxID, consumeEvent.Amount, "Reversão de consumo", sourceTransactionID, consumeEvent.LedgerID, referenceDate, now)
}

type recurrenceProjectionRule struct {
	ID                   string
	AccountID            string
	DestinationAccountID string
	CategoryID           string
	Type                 string
	Amount               int64
	Description          string
	StartDate            int64
	Frequency            string
	DefaultPaymentStatus string
	TotalOccurrences     *int64
}

func (h *TransactionHandler) generateRecurrenceProjection(tx *sql.Tx, rule recurrenceProjectionRule, now int64) error {
	horizon := safeAddMonths(now, 12)
	var accType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, rule.AccountID, h.WorkspaceID).Scan(&accType); err != nil {
		return err
	}
	isCreditCard := accType == "CREDIT_CARD" && rule.Type == "EXPENSE"
	if isCreditCard {
		var maxDue sql.NullInt64
		if err := tx.QueryRow(`
			SELECT MAX(i.due_date)
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE i.account_id = ? AND a.workspace_id = ? AND i.status IN ('OPEN', 'CLOSED')
		`, rule.AccountID, h.WorkspaceID).Scan(&maxDue); err != nil {
			return err
		}
		if maxDue.Valid && maxDue.Int64 > horizon {
			horizon = maxDue.Int64
		}
	}

	var maxSeq sql.NullInt64
	if err := tx.QueryRow(`
		SELECT MAX(recurrence_sequence)
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ?
	`, h.WorkspaceID, rule.ID).Scan(&maxSeq); err != nil {
		return err
	}

	lastDate := rule.StartDate
	nextSeq := int64(1)
	var lastInvID sql.NullString
	if maxSeq.Valid {
		nextSeq = maxSeq.Int64 + 1
		if err := tx.QueryRow(`
			SELECT date, invoice_id
			FROM transactions
			WHERE workspace_id = ? AND recurring_rule_id = ? AND recurrence_sequence = ?
		`, h.WorkspaceID, rule.ID, maxSeq.Int64).Scan(&lastDate, &lastInvID); err != nil {
			return err
		}
	}

	maxIterations := 5000
	if rule.TotalOccurrences != nil && *rule.TotalOccurrences > 0 {
		existing := nextSeq - 1
		if existing > 0 {
			remaining := *rule.TotalOccurrences - existing
			if remaining <= 0 {
				return nil
			}
			if int(remaining) < maxIterations {
				maxIterations = int(remaining)
			}
		}
	}

	occurrence := nextRecurrenceDate(time.Unix(lastDate, 0).UTC(), rule.Frequency)
	for i := 0; i < maxIterations && occurrence.Unix() <= horizon; i++ {
		prevDate := occurrence.Unix()
		id := uuid.NewString()
		var catID interface{}
		if rule.CategoryID != "" {
			catID = rule.CategoryID
		}
		var destID interface{}
		if rule.DestinationAccountID != "" {
			destID = rule.DestinationAccountID
		}

		var invoiceID interface{}
		status := projectionStatusForRule(rule.DefaultPaymentStatus)
		if isCreditCard {
			invID, ref, _, _, _, err := ensureOpenInvoiceTx(tx, h.WorkspaceID, rule.AccountID, occurrence.Unix())
			if err != nil {
				return fmt.Errorf("invoice for recurrence: %w", err)
			}

			if lastInvID.Valid && lastInvID.String == invID {
				nextInvID, err := ensureInvoiceAfterReferenceTx(tx, h.WorkspaceID, rule.AccountID, ref)
				if err != nil {
					return fmt.Errorf("next invoice for recurrence: %w", err)
				}
				invID = nextInvID
			}

			invoiceID = invID
			lastInvID = sql.NullString{String: invID, Valid: true}
			status = "paid"
		}

		if err := execOneTx(tx, `
			INSERT INTO transactions (
				id, workspace_id, user_id, account_id, destination_account_id,
				category_id, invoice_id, type, amount, date, description,
				installment_number, total_installments, parent_id,
				recurring_rule_id, recurrence_sequence, status, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, NULL, ?, ?, ?, ?, ?)
		`, id, h.WorkspaceID, h.UserID, rule.AccountID, destID, catID, invoiceID, rule.Type, rule.Amount, occurrence.Unix(), rule.Description, rule.ID, nextSeq, status, now, now); err != nil {
			return err
		}
		nextSeq++
		occurrence = nextRecurrenceDate(occurrence, rule.Frequency)
		if occurrence.Unix() <= prevDate {
			break
		}
	}

	return execOneTx(tx, `
		UPDATE recurring_rules
		SET generated_until = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
	`, horizon, now, rule.ID, h.WorkspaceID)
}

func (h *TransactionHandler) insertRecurringRuleTx(tx *sql.Tx, ruleID string, tipo string, amountCents int64, dateUnix int64, descricao string, origemContaID string, destinoContaID string, categoriaID string, paymentStatus string, recurrenceFrequency string, now int64) error {
	if err := ensureAccountInWorkspaceTx(tx, origemContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return err
	}
	var accType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, origemContaID, h.WorkspaceID).Scan(&accType); err != nil {
		return err
	}

	var catID interface{}
	if categoriaID != "" {
		catID = categoriaID
	}
	var destID interface{}
	if destinoContaID != "" {
		destID = destinoContaID
	}
	ruleStatus := recurringRuleDefaultStatus(paymentStatus, accType, tipo)
	err := execOneTx(tx, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, destination_account_id, category_id, type, amount, description, start_date, frequency, default_payment_status, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
	`, ruleID, h.WorkspaceID, h.UserID, origemContaID, destID, catID, tipo, amountCents, descricao, dateUnix, recurrenceFrequency, ruleStatus, now, now)
	return err
}

func (h *TransactionHandler) lancamentosDataForCurrentRequest(r *http.Request, dateUnix int64, accountID string) (LancamentosData, bool) {
	currentURL := r.Header.Get("HX-Current-URL")
	if currentURL == "" {
		return LancamentosData{}, false
	}

	u, err := url.Parse(currentURL)
	if err != nil {
		return LancamentosData{}, false
	}
	if u.Path != "/lancamentos" && u.Path != "/transacoes" && u.Path != "/lancamentos-legado" {
		return LancamentosData{}, false
	}

	txDate := time.Unix(dateUnix, 0)
	mes := int(txDate.Month())
	ano := txDate.Year()
	accountFilter := u.Query().Get("conta")
	if accountFilter != "" && accountFilter != accountID {
		accountFilter = ""
	}

	data, err := h.buildLancamentosData(accountFilter, mes, ano, lancamentosFiltersFromValues(u.Query()))
	if err != nil {
		log.Printf("build lancamentos after save error: %v", err)
		return LancamentosData{}, false
	}
	return data, true
}

func (h *TransactionHandler) faturaDataForCurrentRequest(r *http.Request, fallbackInvoiceID string) (FaturaData, bool) {
	currentURL := r.Header.Get("HX-Current-URL")
	if currentURL == "" {
		return FaturaData{}, false
	}
	u, err := url.Parse(currentURL)
	if err != nil {
		return FaturaData{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "cartoes" || parts[2] != "faturas" {
		return FaturaData{}, false
	}
	accountID := parts[1]
	mes, errMes := strconv.Atoi(u.Query().Get("mes"))
	ano, errAno := strconv.Atoi(u.Query().Get("ano"))
	if errMes == nil && errAno == nil && mes >= 1 && mes <= 12 && ano >= 2020 && ano <= time.Now().Year()+10 {
		reference := fmt.Sprintf("%04d-%02d", ano, mes)
		var invoiceID string
		err := h.DB.QueryRow(`
			SELECT i.id
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE i.account_id = ? AND i.reference = ? AND a.workspace_id = ? AND a.type = ?
		`, accountID, reference, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&invoiceID)
		if err == nil {
			data, buildErr := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, normalizeSortOrder(u.Query().Get("ordem"), "desc"))
			if buildErr == nil {
				data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
				return data, true
			}
		}
	}
	if fallbackInvoiceID == "" {
		return FaturaData{}, false
	}
	data, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, fallbackInvoiceID, normalizeSortOrder(u.Query().Get("ordem"), "desc"))
	if err != nil {
		return FaturaData{}, false
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	return data, true
}

func (h *TransactionHandler) renderLancamentosTableBodyFromRequest(w http.ResponseWriter, r *http.Request) error {
	currentURL := strings.TrimSpace(r.Header.Get("HX-Current-URL"))
	if currentURL == "" {
		currentURL = strings.TrimSpace(r.Referer())
	}
	now := time.Now()
	mes := int(now.Month())
	ano := now.Year()
	accountFilter := ""
	filters := LancamentosFilters{}
	if currentURL != "" {
		u, err := url.Parse(currentURL)
		if err == nil {
			if u.Path == "/lancamentos" || u.Path == "/transacoes" || u.Path == "/lancamentos-legado" {
				accountFilter = u.Query().Get("conta")
				filters = lancamentosFiltersFromValues(u.Query())
				if u.Query().Get("reset") == "true" {
					accountFilter = ""
					filters = LancamentosFilters{}
				}
				if mesStr := strings.TrimSpace(u.Query().Get("mes")); mesStr != "" {
					if parsed, err := strconv.Atoi(mesStr); err == nil && parsed >= 1 && parsed <= 12 {
						mes = parsed
					}
				}
				if anoStr := strings.TrimSpace(u.Query().Get("ano")); anoStr != "" {
					if parsed, err := strconv.Atoi(anoStr); err == nil && parsed >= 2020 && parsed <= now.Year()+10 {
						ano = parsed
					}
				}
			}
		}
	}

	data, err := h.buildLancamentosData(accountFilter, mes, ano, filters)
	if err != nil {
		return err
	}
	tableTemplate := "lancamentos-table-body"
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, tableTemplate, data); err != nil {
		return err
	}

	_ = h.renderLancamentosResumoOOB(&buf, r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = buf.WriteTo(w)
	return err
}

func (h *TransactionHandler) queryFormAccounts() ([]FormAccount, error) {
	rows, err := h.DB.Query(`
		SELECT a.id, a.name, a.type,
			COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
			COALESCE(NULLIF(a.color, ''), '#6B7280'),
			COALESCE(NULLIF(a.icon, ''), ''),
			COALESCE(cc.closing_day, 0), COALESCE(cc.due_day, 0),
			COALESCE(cc.credit_limit, 0)
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ? AND a.archived_at IS NULL
		  AND (a.type != 'CREDIT_CARD' OR cc.account_id IS NOT NULL)
		ORDER BY a.type ASC, a.name ASC
	`, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []FormAccount
	for rows.Next() {
		var id, name, accType, providerSlug, color, icon string
		var closingDay, dueDay, creditLimit int64
		if err := rows.Scan(&id, &name, &accType, &providerSlug, &color, &icon, &closingDay, &dueDay, &creditLimit); err != nil {
			return nil, err
		}

		accIcon := icon
		if accIcon == "" {
			accIcon = accountVisualByProvider(providerSlug, accType)
		}

		fa := FormAccount{
			ID:           id,
			Name:         name,
			Icon:         accIcon,
			Color:        normalizeHexColor(color, "#6B7280"),
			ProviderSlug: normalizeAccountProviderSlug(providerSlug),
			ProviderMark: accountProviderMark(providerSlug, name),
		}

		if accType == "CREDIT_CARD" {
			fa.Tipo = "cartao"
			fa.ClosingDay = fmt.Sprintf("%d", closingDay)
			fa.DueDay = fmt.Sprintf("%d", dueDay)
			fa.ProviderName = accountProviderName(providerSlug)
			fa.CreditLimitLabel = buildCreditCardSubtitle(fa.ProviderName, creditLimit)
		} else {
			fa.Tipo = "conta"
		}

		accounts = append(accounts, fa)
	}
	return accounts, rows.Err()
}

func (h *TransactionHandler) queryFilterAccounts() ([]FormAccount, error) {
	rows, err := h.DB.Query(`
		SELECT a.id, a.name, a.type,
			COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
			COALESCE(NULLIF(a.color, ''), '#6B7280'),
			COALESCE(NULLIF(a.icon, ''), ''),
			COALESCE(cc.closing_day, 0), COALESCE(cc.due_day, 0)
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ?
		ORDER BY a.type ASC, a.name ASC
	`, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []FormAccount
	for rows.Next() {
		var id, name, accType, providerSlug, color, icon string
		var closingDay, dueDay int64
		if err := rows.Scan(&id, &name, &accType, &providerSlug, &color, &icon, &closingDay, &dueDay); err != nil {
			return nil, err
		}

		accIcon := icon
		if accIcon == "" {
			accIcon = accountVisualByProvider(providerSlug, accType)
		}

		fa := FormAccount{
			ID:           id,
			Name:         name,
			Icon:         accIcon,
			Color:        normalizeHexColor(color, "#6B7280"),
			ProviderSlug: normalizeAccountProviderSlug(providerSlug),
			ProviderMark: accountProviderMark(providerSlug, name),
		}

		if accType == "CREDIT_CARD" {
			fa.Tipo = "cartao"
			fa.ClosingDay = fmt.Sprintf("%d", closingDay)
			fa.DueDay = fmt.Sprintf("%d", dueDay)
		} else {
			fa.Tipo = "conta"
		}

		accounts = append(accounts, fa)
	}
	return accounts, rows.Err()
}

func (h *TransactionHandler) queryFormCategories() ([]FormCategory, error) {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := nextMonth.Add(-1 * time.Second).Unix()

	rows, err := h.DB.Query(`
		WITH box_balances AS (
			SELECT
				b.id,
				b.workspace_id,
				b.category_id,
				COALESCE(SUM(l.amount), 0) AS reserved
			FROM boxes b
			LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
			WHERE b.workspace_id = ?
			GROUP BY b.id, b.workspace_id, b.category_id
		),
		category_box_candidates AS (
			SELECT
				c.id AS category_id,
				bb.id AS box_id,
				bb.reserved,
				COUNT(*) OVER (PARTITION BY c.id) AS box_count
			FROM categories c
			JOIN box_balances bb ON bb.workspace_id = c.workspace_id
				AND (
					bb.category_id = c.id OR
					(COALESCE(c.parent_id, '') != '' AND bb.category_id = c.parent_id)
				)
			WHERE c.workspace_id = ?
		),
		monthly_limit_spend AS (
			SELECT
				cl.category_id,
				COALESCE(SUM(t.amount), 0) AS spent
			FROM cost_limits cl
			LEFT JOIN transactions t ON t.workspace_id = cl.workspace_id
				AND t.category_id IN (
					SELECT cx.id FROM categories cx
					WHERE cx.workspace_id = cl.workspace_id
					  AND (cx.id = cl.category_id OR cx.parent_id = cl.category_id)
				)
				AND t.type = 'EXPENSE'
				AND t.date >= ? AND t.date <= ?
			WHERE cl.workspace_id = ?
			GROUP BY cl.category_id
		)
		SELECT
			c.id,
			c.name,
			c.icon,
			c.color,
			c.type,
			COALESCE(cbc.box_id, '') AS box_id,
			COALESCE(cbc.reserved, 0) AS box_reserved_balance,
			COALESCE(b.name, '') AS box_name,
			COALESCE(cl.max_amount_monthly, 0) AS limit_max,
			COALESCE(mls.spent, 0) AS limit_spent
		FROM categories c
		LEFT JOIN category_box_candidates cbc ON cbc.category_id = c.id AND cbc.box_count = 1
		LEFT JOIN boxes b ON b.id = cbc.box_id AND b.workspace_id = c.workspace_id
		LEFT JOIN cost_limits cl ON cl.category_id = c.id AND cl.workspace_id = c.workspace_id
		LEFT JOIN monthly_limit_spend mls ON mls.category_id = c.id
		WHERE c.workspace_id = ?
		ORDER BY c.type DESC, c.name ASC
	`, h.WorkspaceID, h.WorkspaceID, monthStart, monthEnd, h.WorkspaceID, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []FormCategory
	for rows.Next() {
		var c FormCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Icon, &c.Color, &c.Type, &c.BoxID, &c.BoxReservedBalance, &c.BoxName, &c.LimitMax, &c.LimitSpent); err != nil {
			return nil, err
		}
		c.Color = normalizeUIThemeColor(c.Color)
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

func (h *TransactionHandler) queryPredictiveSuggestions(q string, txType string) ([]PredictiveSuggestion, error) {
	normalizedQ := normalizeAccents(q)
	rows, err := h.DB.Query(`
		WITH matched AS (
			SELECT
				t.description,
				t.account_id,
				COALESCE(t.category_id, '') AS category_id,
				COALESCE(t.destination_account_id, '') AS destination_account_id,
				t.type,
				a.name AS account_name,
				a.type AS account_type,
				COALESCE(c.name, '') AS category_name,
				COALESCE(da.name, '') AS destination_account_name,
				t.date,
				t.created_at,
				COUNT(*) OVER (
					PARTITION BY lower(t.description)
				) AS frequency,
				ROW_NUMBER() OVER (
					PARTITION BY lower(t.description), t.account_id, COALESCE(t.category_id, ''), COALESCE(t.destination_account_id, '')
					ORDER BY t.date DESC, t.created_at DESC
				) AS rn
			FROM transactions t
			INNER JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN accounts da ON da.id = t.destination_account_id AND da.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
				AND t.type = ?
				AND (t.category_id IS NOT NULL OR t.type = 'TRANSFER')
				AND trim(t.description) <> ''
		)
		SELECT description, account_id, account_name, account_type, category_id, category_name, destination_account_id, destination_account_name, type, frequency
		FROM matched
		WHERE rn = 1
		ORDER BY frequency DESC, date DESC, created_at DESC
		LIMIT 100
	`, h.WorkspaceID, txType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var suggestions []PredictiveSuggestion
	for rows.Next() {
		var s PredictiveSuggestion
		var accountType string
		var rowType string
		if err := rows.Scan(&s.Description, &s.AccountID, &s.AccountName, &accountType, &s.CategoryID, &s.CategoryName, &s.DestinationAccountID, &s.DestinationAccountName, &rowType, &s.Frequency); err != nil {
			return nil, err
		}
		if !strings.Contains(normalizeAccents(s.Description), normalizedQ) {
			continue
		}
		if accountType == "CREDIT_CARD" {
			s.AccountKind = "cartao"
		} else {
			s.AccountKind = "conta"
		}
		switch rowType {
		case "INCOME":
			s.Type = "receita"
		case "TRANSFER":
			s.Type = "transferencia"
		default:
			s.Type = "despesa"
		}
		if rowType != "TRANSFER" {
			s.DestinationAccountID = ""
			s.DestinationAccountName = ""
		}
		suggestions = append(suggestions, s)
		if len(suggestions) >= 3 {
			break
		}
	}
	return suggestions, rows.Err()
}

func newFormTransacaoData(accounts []FormAccount, categories []FormCategory) FormTransacaoData {
	data := FormTransacaoData{
		Accounts:            accounts,
		Categories:          categories,
		TipoInicial:         "despesa",
		StatusInicial:       "paid",
		OrigemTipo:          "conta",
		OrigemNome:          "Conta de origem",
		OrigemIcon:          "wallet",
		OrigemColor:         "#6366F1",
		OrigemProviderMark:  "",
		CategoriaNome:       "Categoria",
		CategoriaIcon:       "tag",
		CategoriaColor:      "rose",
		DestinoNome:         "Conta de destino",
		DestinoIcon:         "wallet",
		DestinoColor:        "#6366F1",
		DestinoProviderMark: "",
	}

	if acc, ok := preferredFormAccount(accounts, "CDB Principal", "conta"); ok {
		applyFormOrigin(&data, acc)
	} else if acc, ok := preferredFormAccount(accounts, "", "conta"); ok {
		applyFormOrigin(&data, acc)
	} else if len(accounts) > 0 {
		applyFormOrigin(&data, accounts[0])
	}

	if acc, ok := preferredFormAccount(accounts, "Conta Nubank", "conta"); ok {
		applyFormDestination(&data, acc)
	} else if acc, ok := preferredFormAccount(accounts, "", "conta"); ok {
		applyFormDestination(&data, acc)
	}

	if cat, ok := preferredFormCategory(categories, "Alimentação", "EXPENSE"); ok {
		applyFormCategory(&data, cat)
	} else if cat, ok := preferredFormCategory(categories, "", "EXPENSE"); ok {
		applyFormCategory(&data, cat)
	}

	return data
}

func preferredFormAccount(accounts []FormAccount, preferredName, kind string) (FormAccount, bool) {
	if preferredName != "" {
		for _, acc := range accounts {
			if acc.Name == preferredName && (kind == "" || acc.Tipo == kind) {
				return acc, true
			}
		}
	}
	for _, acc := range accounts {
		if kind == "" || acc.Tipo == kind {
			return acc, true
		}
	}
	return FormAccount{}, false
}

func findFormAccount(accounts []FormAccount, id string) (FormAccount, bool) {
	for _, acc := range accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return FormAccount{}, false
}

func (h *TransactionHandler) queryFormContacts() ([]ContatoRow, error) {
	rows, err := h.DB.Query(`
		SELECT id, name, COALESCE(document, ''), type, COALESCE(email, ''), COALESCE(phone, ''), created_at
		FROM contacts
		WHERE workspace_id = ?
		ORDER BY name COLLATE NOCASE ASC
	`, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContatoRow
	for rows.Next() {
		var row ContatoRow
		var created int64
		if err := rows.Scan(&row.ID, &row.Name, &row.Document, &row.Type, &row.Email, &row.Phone, &created); err != nil {
			return nil, err
		}
		row.TypeLabel = contactTypeLabel(row.Type)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (h *TransactionHandler) queryInvoiceOptionsForAccount(accountID string) ([]InvoiceOption, error) {
	if accountID == "" {
		return nil, nil
	}
	var accType string
	var closingDay, dueDay int64
	if err := h.DB.QueryRow(`
		SELECT a.type, COALESCE(cc.closing_day, 0), COALESCE(cc.due_day, 0)
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.id = ? AND a.workspace_id = ?
	`, accountID, h.WorkspaceID).Scan(&accType, &closingDay, &dueDay); err != nil {
		return nil, nil
	}
	if accType != models.AccountTypeCreditCard {
		return nil, nil
	}
	rows, err := h.DB.Query(`
		SELECT i.id, i.reference, i.status, i.closing_date, i.due_date
		FROM invoices i
		WHERE i.account_id = ?
		ORDER BY i.reference DESC
		LIMIT 24
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var out []InvoiceOption
	for rows.Next() {
		var opt InvoiceOption
		var closingUnix, dueUnix int64
		if err := rows.Scan(&opt.ID, &opt.Reference, &opt.Status, &closingUnix, &dueUnix); err != nil {
			return nil, err
		}
		total, _ := sumInvoiceTotal(h.DB, h.WorkspaceID, opt.ID)
		paidActive, _ := sumActiveInvoicePaymentsDB(h.DB, h.WorkspaceID, opt.ID)
		pending := total - paidActive
		if pending < 0 {
			pending = 0
		}
		cycleLabel := computeCycleBadge(closingUnix, now)
		finLabel := computeFinancialBadge(pending, paidActive)
		opt.CycleLabel = cycleLabel
		opt.FinLabel = finLabel
		opt.StatusLabel = invoiceStatusLabel(opt.Status)
		opt.ReferenceLabel = formatReferenceLabel(opt.Reference)
		opt.DueLabel = formatDateLabel(dueUnix)
		opt.ClosingLabel = formatDateLabel(closingUnix)
		opt.TotalDisplay = "R$ " + formatCurrencyCentsBase(total)
		opt.PendingDisplay = "R$ " + formatCurrencyCentsBase(pending)
		opt.IsSensitive = opt.Status == models.InvoiceStatusClosed || opt.Status == models.InvoiceStatusPaid
		out = append(out, opt)
	}
	return out, rows.Err()
}

func formatReferenceLabel(reference string) string {
	if len(reference) < 7 {
		return reference
	}
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		return reference
	}
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	return months[month-1] + "/" + strconv.Itoa(year)
}

func preferredFormCategory(categories []FormCategory, preferredName, catType string) (FormCategory, bool) {
	if preferredName != "" {
		for _, cat := range categories {
			if cat.Name == preferredName && (catType == "" || cat.Type == catType) {
				return cat, true
			}
		}
	}
	for _, cat := range categories {
		if catType == "" || cat.Type == catType {
			return cat, true
		}
	}
	return FormCategory{}, false
}

func applyFormOrigin(data *FormTransacaoData, acc FormAccount) {
	data.OrigemContaID = acc.ID
	data.OrigemTipo = acc.Tipo
	data.OrigemNome = acc.Name
	data.OrigemIcon = acc.Icon
	data.OrigemColor = acc.Color
	data.OrigemProviderMark = acc.ProviderMark
}

func applyFormDestination(data *FormTransacaoData, acc FormAccount) {
	data.DestinoContaID = acc.ID
	data.DestinoNome = acc.Name
	data.DestinoIcon = acc.Icon
	data.DestinoColor = acc.Color
	data.DestinoProviderMark = acc.ProviderMark
}

func applyFormCategory(data *FormTransacaoData, cat FormCategory) {
	data.CategoriaID = cat.ID
	data.CategoriaNome = cat.Name
	data.CategoriaIcon = cat.Icon
	data.CategoriaColor = normalizeUIThemeColor(cat.Color)
	data.CategoriaType = cat.Type
}

func formTypeFromTransactionType(txType string) string {
	switch txType {
	case "INCOME":
		return "receita"
	case "TRANSFER":
		return "transferencia"
	default:
		return "despesa"
	}
}

func mapTransactionType(formType string) string {
	switch formType {
	case "receita":
		return "INCOME"
	case "transferencia":
		return "TRANSFER"
	default:
		return "EXPENSE"
	}
}

func normalizeRecurrenceFrequency(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DAILY", "DIARIO", "DIÁRIO":
		return "DAILY"
	case "WEEKLY", "SEMANAL":
		return "WEEKLY"
	case "BIWEEKLY", "QUINZENAL":
		return "BIWEEKLY"
	case "BIMONTHLY", "BIMESTRAL":
		return "BIMONTHLY"
	case "QUARTERLY", "TRIMESTRAL":
		return "QUARTERLY"
	case "SEMIANNUAL", "SEMESTRAL":
		return "SEMIANNUAL"
	case "ANNUAL", "ANUAL":
		return "ANNUAL"
	default:
		return "MONTHLY"
	}
}

func recurrenceSequence(sequence int64, recurring bool) interface{} {
	if !recurring {
		return nil
	}
	return sequence
}

func nullStringInterface(v sql.NullString) interface{} {
	if !v.Valid {
		return nil
	}
	return v.String
}

func ensureAccountInWorkspaceTx(tx *sql.Tx, accountID, workspaceID string) error {
	if accountID == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM accounts WHERE id = ? AND workspace_id = ?`, accountID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("conta não autorizada ou não encontrada: %w", err)
	}
	return nil
}

func ensureAccountActiveTx(tx *sql.Tx, accountID, workspaceID string) error {
	if accountID == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, accountID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("conta de destino arquivada ou não encontrada: %w", err)
	}
	return nil
}

func ensureCategoryInWorkspaceTx(tx *sql.Tx, categoryID, workspaceID string) error {
	if categoryID == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM categories WHERE id = ? AND workspace_id = ?`, categoryID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("categoria não autorizada ou não encontrada: %w", err)
	}
	return nil
}

func ensureContactInWorkspaceTx(tx *sql.Tx, contactID, workspaceID string) error {
	if contactID == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM contacts WHERE id = ? AND workspace_id = ?`, contactID, workspaceID).Scan(&exists); err != nil {
		return fmt.Errorf("contato não autorizado ou não encontrado: %w", err)
	}
	return nil
}

func parseMultipartOrForm(r *http.Request) error {
	if err := r.ParseMultipartForm(12 << 20); err != nil && err != http.ErrNotMultipart {
		return err
	}
	if r.MultipartForm == nil {
		return r.ParseForm()
	}
	return nil
}

func formAllowsBoxOverdraft(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.FormValue("permitir_excedente_caixinha")))
	switch value {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

func respondTransactionValidationError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	escaped := template.HTMLEscapeString(strings.TrimSpace(message))
	if escaped == "" {
		escaped = "Não foi possível validar os dados do lançamento."
	}
	fmt.Fprintf(w, `<div id="lancamento-form-error" hx-swap-oob="true" class="rounded-xl border border-rose-500/35 bg-rose-500/10 px-3 py-2 text-sm text-rose-100">%s</div>`, escaped)
}

func respondTransactionFieldError(w http.ResponseWriter, field, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"formError": {"field": "%s", "message": "%s"}}`, field, url.PathEscape(message)))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<div id="lancamento-form-error" hx-swap-oob="true" class="hidden"></div>`)
}

func (h *TransactionHandler) persistTransactionUpload(r *http.Request) (string, error) {
	file, header, err := r.FormFile("anexo")
	if err != nil {
		if err == http.ErrMissingFile {
			return "", nil
		}
		return "", err
	}
	defer file.Close()

	const maxSize = 10 << 20
	content, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return "", err
	}
	if int64(len(content)) > maxSize {
		return "", fmt.Errorf("file too large")
	}
	if len(content) == 0 {
		return "", nil
	}
	if !isAllowedUploadContent(content, header) {
		return "", fmt.Errorf("unsupported file type")
	}

	ext := normalizedUploadExt(header.Filename)
	if ext == "" {
		ext = ".bin"
	}
	fileName := uuid.NewString() + ext
	relPath := filepath.Join("uploads", h.WorkspaceID, fileName)
	fullPath := filepath.Join(paths.TransactionUploadsDir(h.WorkspaceID), fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
		log.Printf("mkdir attachment dir failed: path=%s err=%v", filepath.Dir(fullPath), err)
		return "", fmt.Errorf("não foi possível salvar o anexo")
	}
	if err := os.WriteFile(fullPath, content, 0600); err != nil {
		log.Printf("write attachment file failed: path=%s err=%v", fullPath, err)
		return "", fmt.Errorf("não foi possível salvar o anexo")
	}

	return filepath.ToSlash(relPath), nil
}

func (h *TransactionHandler) HandleDownloadAnexo(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var relPath string
	if err := h.DB.QueryRow(`
		SELECT attachment_path
		FROM transactions
		WHERE id = ? AND workspace_id = ? AND attachment_path != ''
	`, id, h.WorkspaceID).Scan(&relPath); err != nil {
		http.NotFound(w, r)
		return
	}

	fullPath, err := safeAttachmentFullPath(h.WorkspaceID, relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(fullPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	name := filepath.Base(fullPath)
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func safeAttachmentFullPath(workspaceID, relPath string) (string, error) {
	relPath = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relPath)))
	if relPath == "." || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("invalid path")
	}

	expectedPrefix := filepath.Join("uploads", workspaceID)
	if relPath != expectedPrefix && !strings.HasPrefix(relPath, expectedPrefix+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside workspace upload directory")
	}
	fileRelPath := strings.TrimPrefix(relPath, expectedPrefix)
	fileRelPath = strings.TrimPrefix(fileRelPath, string(os.PathSeparator))
	if fileRelPath == "" || filepath.IsAbs(fileRelPath) {
		return "", fmt.Errorf("invalid path")
	}

	workspaceUploadDir := paths.TransactionUploadsDir(workspaceID)
	fullPath := filepath.Join(workspaceUploadDir, fileRelPath)
	baseAbs, err := filepath.Abs(workspaceUploadDir)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside workspace upload directory")
	}
	return fullAbs, nil
}

func isAllowedUploadContent(content []byte, header *multipart.FileHeader) bool {
	contentType := http.DetectContentType(content)
	allowedTypes := map[string]struct{}{
		"application/pdf": {},
		"image/jpeg":      {},
		"image/png":       {},
		"image/webp":      {},
	}
	if _, ok := allowedTypes[contentType]; ok {
		return true
	}
	ext := normalizedUploadExt(header.Filename)
	allowedExt := map[string]struct{}{
		".pdf":  {},
		".jpg":  {},
		".jpeg": {},
		".png":  {},
		".webp": {},
	}
	_, ok := allowedExt[ext]
	return ok
}

func normalizedUploadExt(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == ".jpeg" {
		return ".jpg"
	}
	return ext
}

func parseDate(s string) (int64, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, err
	}
	return t.Add(12 * time.Hour).Unix(), nil
}

func formatCurrencyCents(cents int64) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}
	reais := cents / 100
	c := cents % 100
	reaisStr := formatInt(reais)
	centsStr := fmt.Sprintf(",%02d", c)
	if negative {
		return "-" + reaisStr + centsStr
	}
	return reaisStr + centsStr
}

func formatInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	if len(s) > 0 {
		parts = append([]string{s}, parts...)
	}
	return strings.Join(parts, ".")
}

func MoneyMajor(cents int64) MoneyDisplay {
	s := formatCurrencyCents(cents)
	idx := strings.LastIndex(s, ",")
	if idx < 0 {
		return MoneyDisplay{Reais: s, Cents: ",00", CentsClass: ""}
	}
	return MoneyDisplay{
		Reais:      s[:idx],
		Cents:      s[idx:],
		CentsClass: "",
	}
}

func MoneyMinor(cents int64) MoneyDisplay {
	m := MoneyMajor(cents)
	m.CentsClass = ""
	return m
}

type MoneyDisplay struct {
	Reais      string
	Cents      string
	CentsClass string
}

type UnifiedItem struct {
	IsInvoice   bool
	DateUnix    int64
	DateLabel   string
	Transaction TransactionRow
	Invoice     InvoiceRow
}

func sortUnifiedItems(items []UnifiedItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].DateUnix < items[j].DateUnix
	})
}

type DashboardData struct {
	Title                   string
	UserName                string
	UserFirstName           string
	UserInitials            string
	UserRole                string
	ProfilePhotoURL         string
	ActiveWorkspaceID       string
	HasWorkspaceQuickToggle bool
	QuickWorkspaceAID       string
	QuickWorkspaceBID       string
	CurrentWorkspaceName    string
	ActiveWorkspaceName     string
	IsBusiness              bool
	OOB                     bool
	Balance                 BalanceData
	Health                  DashboardHealthData
	Accounts                []AccountCard
	PaymentAccounts         []AccountCard
	Cards                   []CreditCardCard
	Limits                  []DashboardLimitCard
	Boxes                   []CaixinhaCard
	PendingPayables         []DashboardPayableItem
	PendingReceivables      []DashboardPayableItem
	Payable7dTotal          MoneyDisplay
	Receivable7dTotal       MoneyDisplay
	Payable7dCount          int
	Receivable7dCount       int
	Saldo7d                 MoneyDisplay
	Saldo7dNegativo         bool
	ResumoEntradas          MoneyDisplay
	ResumoSaidas            MoneyDisplay
	ResumoSaldo             MoneyDisplay
	ResumoNegativo          bool
	ResumoMesLabel          string
	PrevisaoMesLabel        string
	PrevisaoReceber         MoneyDisplay
	PrevisaoPagar           MoneyDisplay
	PrevisaoSaldo           MoneyDisplay
	PrevisaoNegativo        bool
	NotificationCount       int
	UserWorkspaces          []UserWorkspace
}

type UserWorkspace struct {
	ID   string
	Name string
}

type BalanceData struct {
	Money          MoneyDisplay
	Reserved       MoneyDisplay
	Free           MoneyDisplay
	TrendPercent   string
	TrendDirection string
	IsNegative     bool
	FreeIsNegative bool
}

type DashboardHealthData struct {
	SavingsRateLabel      string
	SavingsRateClass      string
	SavingsRatePercent    int
	RunwayLabel           string
	RunwayClass           string
	GrossProfit           MoneyDisplay
	GrossProfitClass      string
	CostEfficiencyLabel   string
	CostEfficiencyClass   string
	CostEfficiencyPercent int
}

type AccountCard struct {
	ID           string
	Name         string
	Money        MoneyDisplay
	Icon         string
	Color        string
	ProviderSlug string
	ProviderMark string
	TypeLabel    string
}

type CreditCardCard struct {
	ID                    string
	InvoiceID             string
	Name                  string
	DueDay                string
	Reference             string
	Status                string
	StatusLabel           string
	StatusIcon            string
	StatusBadgeClass      string
	Amount                int64
	Money                 MoneyDisplay
	LimitMoney            MoneyDisplay
	LimitPercent          int
	Icon                  string
	Color                 string
	ProviderSlug          string
	ProviderMark          string
	CanAttemptPayment     bool
	CanSubmitPayment      bool
	PaymentDisabledReason string
}

type DashboardLimitCard struct {
	CategoryName string
	Spent        MoneyDisplay
	Limit        MoneyDisplay
	Percent      int
}

type DashboardPayableItem struct {
	Description  string
	DueDateLabel string
	Amount       MoneyDisplay
	IsOverdue    bool
}

func BuildDashboardData(db *sql.DB, userID, workspaceID string) DashboardData {
	if err := autoCloseWorkspaceInvoices(db, workspaceID); err != nil {
		log.Printf("auto close workspace invoices error: %v", err)
	}

	data := DashboardData{
		Title:        "Dashboard",
		UserName:     "Vitor",
		UserInitials: "VF",
	}
	data.UserName, data.UserInitials = queryDashboardUser(db, userID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(db, userID)
	data.UserFirstName = extractFirstName(data.UserName)
	data.UserWorkspaces = queryUserWorkspaces(db, userID)
	data.ActiveWorkspaceID = workspaceID
	if len(data.UserWorkspaces) == 2 {
		data.HasWorkspaceQuickToggle = true
		data.QuickWorkspaceAID = data.UserWorkspaces[0].ID
		data.QuickWorkspaceBID = data.UserWorkspaces[1].ID
	}
	data.CurrentWorkspaceName = queryWorkspaceName(db, workspaceID)
	data.ActiveWorkspaceName = data.CurrentWorkspaceName
	data.IsBusiness = workspaceType(db, workspaceID) == "business"
	data.NotificationCount = queryDashboardNotificationCount(db, userID, workspaceID)

	var role string
	err := db.QueryRow(`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role)
	if err == nil {
		roleUpper := strings.ToUpper(strings.TrimSpace(role))
		if roleUpper == "ADMIN" || roleUpper == "ADMINISTRATOR" {
			data.UserRole = "Administrador"
		} else if roleUpper == "MANAGER" || roleUpper == "GESTOR" {
			data.UserRole = "Gestor"
		} else {
			data.UserRole = "Usuário"
		}
	} else {
		data.UserRole = "Usuário"
	}

	var totalBalance int64
	data.Balance.Reserved = MoneyMinor(0)
	data.Balance.Free = MoneyMinor(0)
	reserveBalance, reserveErr := services.CalculateWorkspaceReserveBalance(db, workspaceID)
	if reserveErr != nil {
		log.Printf("calculate workspace reserve balance error: %v", reserveErr)
		db.QueryRow(`SELECT COALESCE(SUM(current_balance), 0) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, workspaceID).Scan(&totalBalance)
	} else {
		totalBalance = reserveBalance.RealBalance
		data.Balance.Reserved = MoneyMinor(reserveBalance.ReservedBalance)
		data.Balance.Free = MoneyMinor(reserveBalance.FreeBalance)
		data.Balance.FreeIsNegative = reserveBalance.FreeBalance < 0
	}
	data.Balance.Money = MoneyMajor(totalBalance)
	data.Balance.TrendPercent, data.Balance.TrendDirection = queryBalanceTrend(db, workspaceID, totalBalance)
	data.Balance.IsNegative = totalBalance < 0
	now := time.Now()
	data.Health = queryDashboardHealth(db, workspaceID, totalBalance, data.IsBusiness)
	data.Limits = queryDashboardLimits(db, workspaceID, now)
	data.PendingPayables = QueryDashboardPendingPayables7d(db, workspaceID, now)
	data.PendingReceivables = QueryDashboardPendingReceivables7d(db, workspaceID, now)
	data.Payable7dTotal, data.Payable7dCount = queryDashboardPayable7dTotal(db, workspaceID, now)
	data.Receivable7dTotal, data.Receivable7dCount = queryDashboardReceivable7dTotal(db, workspaceID, now)

	var payable7dRaw, receivable7dRaw int64
	payable7dRaw = queryDashboardPendingWindowRawTotal(db, workspaceID, models.TransactionTypeExpense, now)
	receivable7dRaw = queryDashboardPendingWindowRawTotal(db, workspaceID, models.TransactionTypeIncome, now)
	saldo7d := receivable7dRaw - payable7dRaw
	data.Saldo7d = MoneyMinor(saldo7d)
	data.Saldo7dNegativo = saldo7d < 0

	monthlyIncome, monthlyExpense := queryDashboardMonthlySummary(db, workspaceID, now)
	data.ResumoEntradas = MoneyMinor(monthlyIncome)
	data.ResumoSaidas = MoneyMinor(monthlyExpense)
	data.ResumoMesLabel = dashboardCashSummaryMonthLabel(now)
	monthlyBalance := monthlyIncome - monthlyExpense
	data.ResumoSaldo = MoneyMinor(monthlyBalance)
	data.ResumoNegativo = monthlyBalance < 0

	forecastIncome, forecastExpense := queryDashboardMonthlyForecast(db, workspaceID, now)
	data.PrevisaoMesLabel = dashboardCashSummaryMonthLabel(now)
	data.PrevisaoReceber = MoneyMinor(forecastIncome)
	data.PrevisaoPagar = MoneyMinor(forecastExpense)
	forecastBalance := forecastIncome - forecastExpense
	data.PrevisaoSaldo = MoneyMinor(forecastBalance)
	data.PrevisaoNegativo = forecastBalance < 0

	data.Boxes = queryDashboardBoxes(db, workspaceID)

	rows, err := db.Query(`
		SELECT
			a.id,
			a.name,
			a.current_balance,
			a.type,
			COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
			COALESCE(NULLIF(a.color, ''), '#6B7280'),
			COALESCE(NULLIF(a.icon, ''), '')
		FROM accounts a
		WHERE a.workspace_id = ? AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
		ORDER BY a.sort_order ASC, a.name ASC
	`, workspaceID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, accType, providerSlug, color, icon string
			var balance int64
			if err := rows.Scan(&id, &name, &balance, &accType, &providerSlug, &color, &icon); err != nil {
				continue
			}
			var typeLabel string
			switch strings.ToUpper(accType) {
			case "CHECKING":
				typeLabel = "Conta Corrente"
			case "SAVINGS":
				typeLabel = "Poupança"
			case "INVESTMENT":
				typeLabel = "Investimento"
			case "WALLET":
				typeLabel = "Carteira / Dinheiro"
			default:
				typeLabel = "Conta"
			}
			accIcon := icon
			if accIcon == "" {
				accIcon = accountVisualByProvider(providerSlug, accType)
			}
			data.Accounts = append(data.Accounts, AccountCard{
				ID:           id,
				Name:         name,
				Money:        MoneyMinor(balance),
				Icon:         accIcon,
				Color:        normalizeHexColor(color, "#6B7280"),
				ProviderSlug: normalizeAccountProviderSlug(providerSlug),
				ProviderMark: accountProviderMark(providerSlug, name),
				TypeLabel:    typeLabel,
			})
			if accType == models.AccountTypeChecking || accType == models.AccountTypeSavings {
				data.PaymentAccounts = append(data.PaymentAccounts, AccountCard{
					ID:           id,
					Name:         name,
					Money:        MoneyMinor(balance),
					Icon:         accIcon,
					Color:        normalizeHexColor(color, "#6B7280"),
					ProviderSlug: normalizeAccountProviderSlug(providerSlug),
					ProviderMark: accountProviderMark(providerSlug, name),
					TypeLabel:    typeLabel,
				})
			}
		}
	}

	cardRows, err := db.Query(`
		SELECT
			a.id,
			a.name,
			COALESCE(cc.due_day, 0),
			COALESCE(cc.credit_limit, 0),
			COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
			COALESCE(NULLIF(a.color, ''), '#6B7280'),
			COALESCE(NULLIF(a.icon, ''), '')
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND a.archived_at IS NULL
		ORDER BY a.sort_order ASC, a.name ASC
	`, workspaceID)
	if err == nil {
		type cardSeed struct {
			id, name, providerSlug, color, icon string
			dueDay, creditLimit                 int64
		}
		var cards []cardSeed
		for cardRows.Next() {
			var c cardSeed
			if err := cardRows.Scan(&c.id, &c.name, &c.dueDay, &c.creditLimit, &c.providerSlug, &c.color, &c.icon); err != nil {
				continue
			}
			cards = append(cards, c)
		}
		cardRows.Close()

		for _, c := range cards {
			invoiceID, reference, status, _, dueUnix, err := resolveDashboardInvoice(db, workspaceID, c.id, time.Now().Unix())
			if err != nil {
				continue
			}
			invoiceTotal, err := sumInvoiceTotal(db, workspaceID, invoiceID)
			if err != nil {
				continue
			}
			outstandingLimit, err := sumCardOutstandingLimit(db, workspaceID, c.id)
			if err != nil {
				continue
			}
			statusLabel := invoiceDisplayStatusLabel(status, dueUnix)
			statusIcon, statusBadgeClass := invoiceStatusVisual(status, statusLabel)
			normalizedProvider := normalizeAccountProviderSlug(c.providerSlug)

			var limitPercent int
			var limitAvailable int64
			if c.creditLimit > 0 {
				limitAvailable = c.creditLimit - outstandingLimit
				if limitAvailable < 0 {
					limitAvailable = 0
				}
				limitPercent = int((limitAvailable * 100) / c.creditLimit)
			}
			if limitPercent < 0 {
				limitPercent = 0
			}
			if limitPercent > 100 {
				limitPercent = 100
			}

			cardIcon := c.icon
			if cardIcon == "" {
				cardIcon = accountVisualByProvider(normalizedProvider, models.AccountTypeCreditCard)
			}

			data.Cards = append(data.Cards, CreditCardCard{
				ID:               c.id,
				InvoiceID:        invoiceID,
				Name:             c.name,
				DueDay:           fmt.Sprintf("%d", c.dueDay),
				Reference:        reference,
				Status:           status,
				StatusLabel:      statusLabel,
				StatusIcon:       statusIcon,
				StatusBadgeClass: statusBadgeClass,
				Amount:           invoiceTotal,
				Money:            MoneyMinor(invoiceTotal),
				LimitMoney:       MoneyMinor(limitAvailable),
				LimitPercent:     limitPercent,
				Icon:             cardIcon,
				Color:            normalizeHexColor(c.color, "#6B7280"),
				ProviderSlug:     normalizedProvider,
				ProviderMark:     accountProviderMark(normalizedProvider, c.name),
			})
		}
	}

	return data
}

func queryDashboardLimits(db *sql.DB, workspaceID string, now time.Time) []DashboardLimitCard {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := nextMonth.Add(-1 * time.Second).Unix()

	rows, err := db.Query(`
		SELECT
			COALESCE(c.name, 'Sem categoria'),
			cl.max_amount_monthly,
			COALESCE(SUM(t.amount), 0) AS spent
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id AND c.workspace_id = cl.workspace_id
		LEFT JOIN transactions t ON t.workspace_id = cl.workspace_id
			AND t.category_id IN (
				SELECT cx.id
				FROM categories cx
				WHERE cx.workspace_id = cl.workspace_id
				  AND (cx.id = cl.category_id OR cx.parent_id = cl.category_id)
					)
					AND t.type = 'EXPENSE'
					AND t.date >= ? AND t.date <= ?
					AND `+excludeInvoicePaymentCompetenceClause("t")+`
				WHERE cl.workspace_id = ?
				GROUP BY cl.id, c.name
				ORDER BY c.name
			`, monthStart, monthEnd, workspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	limits := make([]DashboardLimitCard, 0)
	for rows.Next() {
		var card DashboardLimitCard
		var maxAmount, spent int64
		if err := rows.Scan(&card.CategoryName, &maxAmount, &spent); err != nil {
			continue
		}
		card.Spent = MoneyMinor(spent)
		card.Limit = MoneyMinor(maxAmount)
		if maxAmount > 0 {
			card.Percent = int((spent * 100) / maxAmount)
		}
		if card.Percent < 0 {
			card.Percent = 0
		}
		if card.Percent > 100 {
			card.Percent = 100
		}
		limits = append(limits, card)
	}

	if len(limits) > 0 {
		sort.Slice(limits, func(i, j int) bool {
			if limits[i].Percent == limits[j].Percent {
				return limits[i].CategoryName < limits[j].CategoryName
			}
			return limits[i].Percent > limits[j].Percent
		})
	}
	if len(limits) > 5 {
		limits = limits[:5]
	}

	return limits
}

func queryDashboardMonthlySummary(db *sql.DB, workspaceID string, now time.Time) (int64, int64) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	monthEnd := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()

	var monthlyIncome, monthlyExpense int64
	_ = db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM transactions
		WHERE workspace_id = ?
		  AND type IN ('INCOME', 'EXPENSE')
		  AND status = 'paid'
		  AND date >= ? AND date < ?
		  AND (type != 'EXPENSE' OR invoice_id IS NULL)
	`, workspaceID, monthStart, monthEnd).Scan(&monthlyIncome, &monthlyExpense)
	return monthlyIncome, monthlyExpense
}

func queryDashboardMonthlyForecast(db *sql.DB, workspaceID string, now time.Time) (int64, int64) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	monthEnd := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()

	var forecastIncome, forecastExpense int64
	_ = db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN kind = 'INCOME' THEN amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM (
			SELECT type AS kind, amount
			FROM transactions
			WHERE workspace_id = ?
				  AND type IN ('INCOME', 'EXPENSE')
				  AND status = 'pending'
				  AND invoice_id IS NULL
				  AND (type != 'EXPENSE' OR `+excludeInvoicePaymentCompetenceClause("")+`)
				  AND COALESCE(due_date, date) >= ? AND COALESCE(due_date, date) < ?
			UNION ALL
			SELECT
				'EXPENSE' AS kind,
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0) AS amount
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date >= ? AND i.due_date < ?
			  AND (
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0)
			  ) > 0
		)
		`, workspaceID, monthStart, monthEnd, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, monthStart, monthEnd).Scan(&forecastIncome, &forecastExpense)
	return forecastIncome, forecastExpense
}

func dashboardCashSummaryMonthLabel(now time.Time) string {
	months := []string{
		"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho",
		"Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro",
	}
	t := now.UTC()
	return fmt.Sprintf("%s/%d", months[int(t.Month())-1], t.Year())
}

func queryDashboardBoxes(db *sql.DB, workspaceID string) []CaixinhaCard {
	rows, err := db.Query(`
		SELECT b.id, b.category_id, b.name, b.target_amount, b.monthly_recharge,
			COALESCE(c.icon, 'piggy-bank'),
			COALESCE(c.color, '#6b7280'),
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS total_balance
		FROM boxes b
		JOIN categories c ON c.id = b.category_id
		WHERE b.workspace_id = ?
		ORDER BY b.name
	`, workspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var boxes []CaixinhaCard
	for rows.Next() {
		var card CaixinhaCard
		var target, monthly, balance int64
		if err := rows.Scan(&card.ID, &card.CategoryID, &card.Name, &target, &monthly, &card.Icon, &card.Color, &balance); err != nil {
			continue
		}
		if balance < 0 {
			balance = 0
		}
		if target > 0 {
			card.Percent = int(float64(balance) / float64(target) * 100)
		}
		card.PercentColor = "violet"
		if card.Percent >= 75 {
			card.PercentColor = "emerald"
		} else if card.Percent >= 50 {
			card.PercentColor = "sky"
		}
		card.Target = MoneyMinor(target)
		card.Balance = MoneyMinor(balance)
		card.MonthlyRecharge = MoneyMinor(monthly)
		boxes = append(boxes, card)
	}

	if len(boxes) > 0 {
		sort.Slice(boxes, func(i, j int) bool {
			if boxes[i].Percent == boxes[j].Percent {
				return boxes[i].Name < boxes[j].Name
			}
			return boxes[i].Percent > boxes[j].Percent
		})
	}
	if len(boxes) > 5 {
		boxes = boxes[:5]
	}

	return boxes
}

func queryDashboardPendingItems7d(db *sql.DB, workspaceID, transactionType string, now time.Time) []DashboardPayableItem {
	windowEnd := dashboardPendingWindowEnd(now)
	args := []interface{}{workspaceID, transactionType, windowEnd}
	query := `
		SELECT description, amount, due_ref
		FROM (
			SELECT
				COALESCE(NULLIF(description, ''), 'Sem descrição') AS description,
				amount,
				COALESCE(due_date, date) AS due_ref
			FROM transactions
			WHERE workspace_id = ?
				  AND type = ?
				  AND status = 'pending'
				  AND invoice_id IS NULL
				  AND COALESCE(due_date, date) < ?`
	if transactionType == models.TransactionTypeExpense {
		query += `
				  AND ` + excludeInvoicePaymentCompetenceClause("")
	}
	query += `
			UNION ALL
			SELECT
				'Fatura ' || a.name || ' - ' || i.reference AS description,
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0) AS amount,
				i.due_date AS due_ref
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE ? = 'EXPENSE'
			  AND a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date < ?
			  AND (
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0)
			  ) > 0
		)
		ORDER BY due_ref ASC
		LIMIT 10`
	args = append(args, transactionType, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, windowEnd)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	items := make([]DashboardPayableItem, 0)
	for rows.Next() {
		var item DashboardPayableItem
		var amount, dueUnix int64
		if err := rows.Scan(&item.Description, &amount, &dueUnix); err != nil {
			continue
		}
		item.Amount = MoneyMinor(amount)

		dueDate := time.Unix(dueUnix, 0).UTC()
		dueOnly := time.Date(dueDate.Year(), dueDate.Month(), dueDate.Day(), 0, 0, 0, 0, time.UTC)
		item.IsOverdue = dueOnly.Before(today)
		switch {
		case item.IsOverdue:
			item.DueDateLabel = "Atrasado · venceu em " + dueDate.Format("02/01")
		case dueOnly.Equal(today):
			item.DueDateLabel = "Vence hoje"
		default:
			item.DueDateLabel = "Vence em " + dueDate.Format("02/01")
		}

		items = append(items, item)
	}
	return items
}

func queryDashboardPayable7dTotal(db *sql.DB, workspaceID string, now time.Time) (MoneyDisplay, int) {
	return queryDashboardPendingWindowTotal(db, workspaceID, models.TransactionTypeExpense, now)
}

func queryDashboardReceivable7dTotal(db *sql.DB, workspaceID string, now time.Time) (MoneyDisplay, int) {
	return queryDashboardPendingWindowTotal(db, workspaceID, models.TransactionTypeIncome, now)
}

func queryDashboardPendingWindowTotal(db *sql.DB, workspaceID, transactionType string, now time.Time) (MoneyDisplay, int) {
	var totalAmount int64
	var itemCount int
	windowEnd := dashboardPendingWindowEnd(now)
	err := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0), COUNT(*)
		FROM (
			SELECT amount
			FROM transactions
			WHERE workspace_id = ?
				  AND type = ?
				  AND status = 'pending'
				  AND invoice_id IS NULL
				  AND (? != 'EXPENSE' OR `+excludeInvoicePaymentCompetenceClause("")+`)
				  AND COALESCE(due_date, date) < ?
			UNION ALL
			SELECT
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0) AS amount
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE ? = 'EXPENSE'
			  AND a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date < ?
			  AND (
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0)
			  ) > 0
		)
		`, workspaceID, transactionType, transactionType, windowEnd, transactionType, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, windowEnd).Scan(&totalAmount, &itemCount)
	if err != nil {
		return MoneyDisplay{}, 0
	}
	return MoneyMinor(totalAmount), itemCount
}

func queryDashboardPendingWindowRawTotal(db *sql.DB, workspaceID, transactionType string, now time.Time) int64 {
	var totalAmount int64
	windowEnd := dashboardPendingWindowEnd(now)
	_ = db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM (
			SELECT amount
			FROM transactions
			WHERE workspace_id = ?
				  AND type = ?
				  AND status = 'pending'
				  AND invoice_id IS NULL
				  AND (? != 'EXPENSE' OR `+excludeInvoicePaymentCompetenceClause("")+`)
				  AND COALESCE(due_date, date) < ?
			UNION ALL
			SELECT
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0) AS amount
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE ? = 'EXPENSE'
			  AND a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date < ?
			  AND (
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - COALESCE((
					SELECT SUM(ip.amount_cents)
					FROM invoice_payments ip
					WHERE ip.workspace_id = a.workspace_id AND ip.invoice_id = i.id AND ip.reversed_at IS NULL
				), 0)
			  ) > 0
		)
		`, workspaceID, transactionType, transactionType, windowEnd, transactionType, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, windowEnd).Scan(&totalAmount)
	return totalAmount
}

func QueryDashboardPendingPayables7d(db *sql.DB, workspaceID string, now time.Time) []DashboardPayableItem {
	return queryDashboardPendingItems7d(db, workspaceID, models.TransactionTypeExpense, now)
}

func QueryDashboardPendingReceivables7d(db *sql.DB, workspaceID string, now time.Time) []DashboardPayableItem {
	return queryDashboardPendingItems7d(db, workspaceID, models.TransactionTypeIncome, now)
}

func dashboardPendingWindowEnd(now time.Time) int64 {
	today := now.UTC()
	todayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	// Include overdue items, today, and the next seven full calendar days.
	return todayStart.AddDate(0, 0, 8).Unix()
}

func queryDashboardUser(db *sql.DB, userID string) (string, string) {
	var name string
	err := db.QueryRow(`SELECT name FROM users WHERE id = ?`, userID).Scan(&name)
	if err != nil || strings.TrimSpace(name) == "" {
		return "Usuário", "US"
	}
	return name, initials(name)
}

func queryUserProfilePhotoURL(db *sql.DB, userID string) string {
	if strings.TrimSpace(userID) == "" {
		return ""
	}
	var photoPath string
	var updatedAt int64
	err := db.QueryRow(`
		SELECT COALESCE(profile_photo_path, ''), COALESCE(updated_at, unixepoch())
		FROM users
		WHERE id = ?
	`, userID).Scan(&photoPath, &updatedAt)
	if err != nil || strings.TrimSpace(photoPath) == "" {
		return ""
	}
	fileName := strings.TrimPrefix(photoPath, "/uploads/profile/")
	if fileName == photoPath || fileName == "" {
		return ""
	}
	fullPath := filepath.Join(paths.ProfileUploadsDir(), fileName)
	if _, err := os.Stat(fullPath); err != nil {
		return ""
	}
	return fmt.Sprintf("%s?v=%d", photoPath, updatedAt)
}

func queryUserInitialsByID(db *sql.DB, userID string) string {
	_, initials := queryDashboardUser(db, userID)
	return initials
}

func queryDashboardNotificationCount(db *sql.DB, userID, workspaceID string) int {
	if strings.TrimSpace(userID) == "" {
		return 0
	}
	if err := autoCloseWorkspaceInvoices(db, workspaceID); err != nil {
		log.Printf("auto close notification count invoices error: %v", err)
	}
	h := NotificacoesHandler{DB: db, WorkspaceID: workspaceID, UserID: userID}
	isBusiness := workspaceType(db, workspaceID) == "business"
	items := append(h.pendingOverdueExpenses(), h.closedInvoices()...)
	items = append(items, h.exceededCostLimits(isBusiness)...)
	items = append(items, h.boxesInOverdraft(isBusiness)...)
	items = append(items, h.completedBoxGoals(isBusiness)...)
	items = append(items, h.caixinhaPersistedNotifications()...)
	return len(h.visibleItemsForUser(items))
}

func initials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "US"
	}
	initial := func(s string) string {
		r := []rune(s)
		if len(r) == 0 {
			return ""
		}
		return strings.ToUpper(string(r[0]))
	}
	if len(parts) == 1 {
		return initial(parts[0])
	}
	return initial(parts[0]) + initial(parts[len(parts)-1])
}

func extractFirstName(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "Usuário"
	}
	return parts[0]
}

func queryUserWorkspaces(db *sql.DB, userID string) []UserWorkspace {
	workspaces := make([]UserWorkspace, 0)
	rows, err := db.Query(`
		SELECT w.id, w.name
		FROM workspaces w
		JOIN workspace_members wm ON wm.workspace_id = w.id
		WHERE wm.user_id = ?
		ORDER BY wm.joined_at ASC
	`, userID)
	if err != nil {
		return workspaces
	}
	defer rows.Close()
	for rows.Next() {
		var ws UserWorkspace
		if err := rows.Scan(&ws.ID, &ws.Name); err == nil {
			workspaces = append(workspaces, ws)
		}
	}
	return workspaces
}

func queryWorkspaceName(db *sql.DB, workspaceID string) string {
	var name string
	err := db.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, workspaceID).Scan(&name)
	if err != nil || strings.TrimSpace(name) == "" {
		return "Workspace"
	}
	return name
}

func queryBalanceTrend(db *sql.DB, workspaceID string, currentBalance int64) (string, string) {
	now := time.Now()
	currentStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	currentNet := queryRealAccountNetCashFlow(db, workspaceID, currentStart, now.Unix())
	previousClosingBalance := currentBalance - currentNet
	direction := "up"
	if currentBalance < previousClosingBalance {
		direction = "down"
	}
	diff := currentBalance - previousClosingBalance
	if diff < 0 {
		diff = -diff
	}
	if previousClosingBalance == 0 {
		if diff == 0 {
			return "0,0%", direction
		}
		return "100,0%", direction
	}
	percentTenths := (diff*1000 + absInt64(previousClosingBalance)/2) / absInt64(previousClosingBalance)
	return fmt.Sprintf("%d,%d%%", percentTenths/10, percentTenths%10), direction
}

func queryRealAccountNetCashFlow(db *sql.DB, workspaceID string, startUnix, endUnix int64) int64 {
	var income, expense int64
	db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND a.workspace_id = ?
		  AND a.type != ?
		  AND t.status = 'paid'
		  AND t.type IN ('INCOME', 'EXPENSE')
		  AND t.date >= ? AND t.date < ?
	`, workspaceID, workspaceID, models.AccountTypeCreditCard, startUnix, endUnix).Scan(&income, &expense)
	return income - expense
}

func queryDashboardHealth(db *sql.DB, workspaceID string, totalBalance int64, isBusiness bool) DashboardHealthData {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()

	var income, expense int64
	db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM transactions
			WHERE workspace_id = ?
			  AND status = 'paid'
			  AND date >= ? AND date < ?
			  AND `+excludeInvoicePaymentCompetenceClause("")+`
		`, workspaceID, monthStart, nextMonth).Scan(&income, &expense)

	if isBusiness {
		var grossRevenue, directCosts int64
		db.QueryRow(`
			SELECT
				COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE 0 END), 0),
				COALESCE(SUM(CASE
					WHEN t.type = 'EXPENSE'
					 AND COALESCE(c.macro_group, p.macro_group, '') IN ('Custos Operacionais', 'OPERATING_COSTS')
					THEN t.amount
					ELSE 0
				END), 0)
			FROM transactions t
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
				WHERE t.workspace_id = ?
				  AND t.status = 'paid'
				  AND t.date >= ? AND t.date < ?
				  AND `+excludeInvoicePaymentCompetenceClause("t")+`
			`, workspaceID, monthStart, nextMonth).Scan(&grossRevenue, &directCosts)

		grossProfit := grossRevenue - directCosts
		grossProfitClass := "text-[#009866]"
		if grossProfit < 0 {
			grossProfitClass = "text-[#FE414F]"
		}

		efficiencyLabel := "0,0%"
		efficiencyClass := "text-amber-300"
		costEffPercent := 0
		if directCosts <= 0 {
			if grossProfit > 0 {
				efficiencyLabel = "∞"
				efficiencyClass = "text-[#009866]"
			}
			costEffPercent = 100
		} else {
			efficiencyTenths := int64(0)
			raw := grossProfit * 1000
			if raw >= 0 {
				efficiencyTenths = (raw + directCosts/2) / directCosts
			} else {
				efficiencyTenths = (raw - directCosts/2) / directCosts
			}
			efficiencyLabel = formatPercentTenths(efficiencyTenths)
			if efficiencyTenths < 0 {
				efficiencyClass = "text-[#FE414F]"
			} else if efficiencyTenths >= 100 {
				efficiencyClass = "text-[#009866]"
			}

			if efficiencyTenths > 0 {
				costEffPercent = int(efficiencyTenths / 10)
			}
			if costEffPercent > 100 {
				costEffPercent = 100
			}
		}

		return DashboardHealthData{
			GrossProfit:           MoneyMinor(grossProfit),
			GrossProfitClass:      grossProfitClass,
			CostEfficiencyLabel:   efficiencyLabel,
			CostEfficiencyClass:   efficiencyClass,
			CostEfficiencyPercent: costEffPercent,
		}
	}

	savingsRate := int64(0)
	if income > 0 {
		raw := (income - expense) * 1000
		if raw >= 0 {
			savingsRate = (raw + income/2) / income
		} else {
			savingsRate = (raw - income/2) / income
		}
	}
	savingsClass := "text-[#009866]"
	if savingsRate < 0 {
		savingsClass = "text-[#FE414F]"
	} else if savingsRate < 100 {
		savingsClass = "text-amber-300"
	}

	runwayDays := queryHybridRunwayDays(db, workspaceID, totalBalance)
	runwayLabel := "∞"
	runwayClass := "text-[#009866]"
	if runwayDays >= 0 {
		runwayLabel = fmt.Sprintf("%d dias", runwayDays)
		if runwayDays < 30 {
			runwayClass = "text-[#FE414F]"
		} else if runwayDays < 90 {
			runwayClass = "text-amber-300"
		}
	}

	savingsRatePercent := 0
	if savingsRate > 0 {
		savingsRatePercent = int(savingsRate / 10)
	}
	if savingsRatePercent > 100 {
		savingsRatePercent = 100
	}

	return DashboardHealthData{
		SavingsRateLabel:   formatPercentTenths(savingsRate),
		SavingsRateClass:   savingsClass,
		SavingsRatePercent: savingsRatePercent,
		RunwayLabel:        runwayLabel,
		RunwayClass:        runwayClass,
	}
}

func formatPercentTenths(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	return fmt.Sprintf("%s%d,%d%%", sign, value/10, value%10)
}

func queryHybridRunwayDays(db *sql.DB, workspaceID string, totalBalance int64) int64 {
	if totalBalance <= 0 {
		return 0
	}
	avgDailyExpense := queryVariableDailyExpense90d(db, workspaceID)
	events := queryRunwayScheduledExpenses(db, workspaceID)
	if avgDailyExpense <= 0 && len(events) == 0 {
		return -1
	}
	scheduled := make(map[int64]int64, len(events))
	for _, event := range events {
		scheduled[event.day] += event.amount
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	balance := totalBalance
	for day := int64(0); day <= 3650; day++ {
		current := today.AddDate(0, 0, int(day))
		balance -= avgDailyExpense
		balance -= scheduled[current.Unix()]
		if balance <= 0 {
			return day
		}
	}
	return -1
}

func queryVariableDailyExpense90d(db *sql.DB, workspaceID string) int64 {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -90)
	var totalExpense int64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(t.amount), 0)
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.type = 'EXPENSE'
		  AND t.status = 'paid'
		  AND t.date >= ? AND t.date < ?
			  AND t.invoice_id IS NULL
			  AND t.recurring_rule_id IS NULL
			  AND COALESCE(c.is_fixed, 0) = 0
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
		`, workspaceID, start.Unix(), today.AddDate(0, 0, 1).Unix()).Scan(&totalExpense)
	if err != nil || totalExpense <= 0 {
		return 0
	}
	return totalExpense / 90
}

type runwayExpenseEvent struct {
	day    int64
	amount int64
}

func queryRunwayScheduledExpenses(db *sql.DB, workspaceID string) []runwayExpenseEvent {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	horizon := today.AddDate(10, 0, 0)
	var events []runwayExpenseEvent
	events = append(events, queryRunwayRecurringExpenses(db, workspaceID, today, horizon)...)
	events = append(events, queryRunwayInvoiceExpenses(db, workspaceID, today, horizon)...)
	return events
}

func queryRunwayRecurringExpenses(db *sql.DB, workspaceID string, start, end time.Time) []runwayExpenseEvent {
	rows, err := db.Query(`
		SELECT amount, start_date, frequency
		FROM recurring_rules
		WHERE workspace_id = ?
		  AND active = 1
		  AND type = 'EXPENSE'
		  AND start_date < ?
	`, workspaceID, end.Unix())
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []runwayExpenseEvent
	for rows.Next() {
		var amount, startUnix int64
		var frequency string
		if err := rows.Scan(&amount, &startUnix, &frequency); err != nil {
			continue
		}
		occurrence := time.Unix(startUnix, 0).UTC()
		for i := 0; i < 40 && occurrence.Before(end); i++ {
			prevDate := occurrence.Unix()
			day := time.Date(occurrence.Year(), occurrence.Month(), occurrence.Day(), 0, 0, 0, 0, time.UTC)
			if !day.Before(start) {
				events = append(events, runwayExpenseEvent{day: day.Unix(), amount: amount})
			}
			occurrence = nextRecurrenceDate(occurrence, frequency)
			if occurrence.Unix() <= prevDate {
				break
			}
		}
	}
	return events
}

func queryRunwayInvoiceExpenses(db *sql.DB, workspaceID string, start, end time.Time) []runwayExpenseEvent {
	rows, err := db.Query(`
		SELECT i.due_date, COALESCE(SUM(t.amount), 0) AS total
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		JOIN transactions t ON t.invoice_id = i.id
		WHERE a.workspace_id = ?
		  AND t.workspace_id = ?
		  AND a.type = ?
		  AND i.status IN ('OPEN', 'CLOSED')
		  AND i.due_date >= ? AND i.due_date < ?
		  AND t.type = 'EXPENSE'
		  AND t.status = 'paid'
		GROUP BY i.id, i.due_date
	`, workspaceID, workspaceID, models.AccountTypeCreditCard, start.Unix(), end.Unix())
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []runwayExpenseEvent
	for rows.Next() {
		var dueUnix, total int64
		if err := rows.Scan(&dueUnix, &total); err != nil || total <= 0 {
			continue
		}
		due := time.Unix(dueUnix, 0).UTC()
		day := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
		events = append(events, runwayExpenseEvent{day: day.Unix(), amount: total})
	}
	return events
}

func queryAverageDailyExpense(db *sql.DB, workspaceID string) int64 {
	var totalExpense, firstDate int64
	err := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0), COALESCE(MIN(date), 0)
		FROM transactions
		WHERE workspace_id = ?
		  AND type = 'EXPENSE'
		  AND status = 'paid'
		  AND `+excludeInvoicePaymentCompetenceClause("")+`
	`, workspaceID).Scan(&totalExpense, &firstDate)
	if err != nil || totalExpense <= 0 || firstDate <= 0 {
		return 0
	}
	first := time.Unix(firstDate, 0).UTC()
	now := time.Now().UTC()
	days := int64(now.Sub(first).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	return totalExpense / days
}

func invoiceStatusVisual(status, label string) (icon, badgeClass string) {
	switch {
	case status == models.InvoiceStatusPaid:
		return "check-circle-2", "border-emerald-400/30 bg-emerald-500 text-emerald-950"
	case status == models.InvoiceStatusClosed || label == "Vencido":
		return "triangle-alert", "border-amber-400/30 bg-amber-500/15 text-amber-200"
	default:
		return "loader-circle", "border-sky-400/20 bg-sky-500/10 text-sky-200"
	}
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func AccountIcon(name string) string {
	switch name {
	case "CDB Principal":
		return "landmark"
	case "Conta Corrente Itaú":
		return "building-2"
	case "Conta Nubank":
		return "circle-dollar-sign"
	case "Banco Inter":
		return "banknote"
	case "Dinheiro em Espécie":
		return "wallet"
	default:
		return "wallet"
	}
}

func AccountColor(name string) string {
	switch name {
	case "CDB Principal", "Porto Bank":
		return "emerald"
	case "Conta Corrente Itaú", "C6 Carbon":
		return "sky"
	case "Conta Nubank", "Nubank UV":
		return "violet"
	case "Banco Inter", "Inter Gold":
		return "amber"
	case "Dinheiro em Espécie":
		return "lime"
	case "Itaú Black":
		return "rose"
	default:
		return "indigo"
	}
}

func normalizeUIThemeColor(color string) string {
	switch color {
	case "amber", "emerald", "indigo", "lime", "red", "rose", "sky", "slate", "violet":
		return color
	default:
		return "slate"
	}
}

type InvoiceRow struct {
	ID               string
	AccountID        string
	AccountName      string
	CardIcon         string
	CardColor        string
	CardProviderMark string
	Reference        string
	ReferenceLabel   string
	Status           string
	DueDate          int64
	DateLabel        string
	Total            int64
	TotalMoney       MoneyDisplay
	IsOverdue        bool
	FaturaURL        string
	ItemCount        int
}

type FilterLabelItem struct {
	Key   string
	Label string
}

type LancamentosData struct {
	OOB                       bool
	Title                     string
	IsBusiness                bool
	ActiveWorkspaceName       string
	UserInitials              string
	ProfilePhotoURL           string
	FilterAccountID           string
	FilterLabel               string
	HasActiveFilters          bool
	FilterLabels              []FilterLabelItem
	ClearFiltersURL           string
	MesAtual                  int
	AnoAtual                  int
	MonthLabel                string
	MonthSelectorHXGet        string
	MonthSelectorHXSelect     string
	MonthSelectorHXTarget     string
	MonthSelectorHXSwap       string
	MonthSelectorPartial      string
	MonthSelectorPrevQuery    string
	MonthSelectorNextQuery    string
	MonthSelectorCurrentQuery string
	MesAnteriorURL            string
	MesSeguinteURL            string
	CurrentMonthURL           string
	MonthOptions              []MonthOption
	ResumoEntradas            string
	ResumoSaidas              string
	ResumoSaldo               string
	ResumoNegativo            bool
	ResumoAcumulado           string
	AcumuladoNegativo         bool
	UnifiedItems              []UnifiedItem
	SortOrder                 string
	StatusPendenteOn          bool
	StatusVencidoOn           bool
	StatusPagoOn              bool
	Transactions              []TransactionRow
	Invoices                  []InvoiceRow
	Filters                   LancamentosFilters
	FilterAccounts            []FormAccount
	FilterCategories          []FormCategory
}

type LancamentosFilters struct {
	Tipos      []string
	Situacoes  []string
	OrigemIDs  []string
	DestinoIDs []string
	Categorias []string
	Busca      string
	Order      string
}

type MonthOption struct {
	Label     string
	Year      string
	URL       string
	Query     string
	IsActive  bool
	IsCurrent bool
}

type TransactionRow struct {
	ID                  string
	Date                int64
	CreatedAt           int64
	ListIndex           int
	DateLabel           string
	DisplayDateLabel    string
	UsesDueDate         bool
	TimeDisplay         string
	DateInput           string
	Description         string
	Amount              int64
	AmountDisplay       string
	AmountInput         string
	AmountClass         string
	CategoryIcon        string
	CategoryColor       string
	CategoryName        string
	AccountName         string
	ContactName         string
	IsBusiness          bool
	Author              string
	PaymentStatus       string
	IsPending           bool
	IsOverdue           bool
	PaymentIcon         string
	PaymentTitle        string
	PaymentConfirm      string
	PaymentConfirmTitle string
	Type                string
	InstallmentNumber   int64
	TotalInstallments   int64
	HasInstallmentInfo  bool
	IsSeries            bool
	IsProjected         bool
	CanGenerateReceipt  bool
}

func (h *TransactionHandler) HandleListarTransacoes(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	accountFilter := r.URL.Query().Get("conta")
	mesStr := r.URL.Query().Get("mes")
	anoStr := r.URL.Query().Get("ano")
	filters := lancamentosFiltersFromRequest(r)
	if r.URL.Query().Get("reset") == "true" {
		accountFilter = ""
		filters = LancamentosFilters{}
	}

	now := time.Now()
	mes := int(now.Month())
	ano := now.Year()
	if mesStr != "" {
		if v, err := strconv.Atoi(mesStr); err == nil && v >= 1 && v <= 12 {
			mes = v
		}
	}
	if anoStr != "" {
		if v, err := strconv.Atoi(anoStr); err == nil && v >= 2020 && v <= now.Year()+10 {
			ano = v
		}
	}

	tData := time.Now()
	data, err := h.buildLancamentosData(accountFilter, mes, ano, filters)
	perfStep(reqID, "Lancamentos", "buildLancamentosData", time.Since(tData))
	if err != nil {
		log.Printf("build lancamentos error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	templateName := "lancamentos-page"
	isListPartial := false
	if r.Header.Get("HX-Request") != "" {
		if r.URL.Query().Get("partial") == "lista" {
			templateName = "lancamentos-list"
			isListPartial = true
		}
	}

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template lancamentos error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if isListPartial {
		if h.Templates.Lookup("lancamentos-resumo") != nil {
			data.OOB = true
			if err := h.Templates.ExecuteTemplate(&buf, "lancamentos-resumo", data); err != nil {
				log.Printf("template lancamentos-resumo oob error: %v", err)
			}
		}
		if h.Templates.Lookup("seletor_meses") != nil {
			data.OOB = true
			if err := h.Templates.ExecuteTemplate(&buf, "seletor_meses", data); err != nil {
				log.Printf("template seletor_meses oob error: %v", err)
			}
		}
		if h.Templates.Lookup("lancamentos-filter-period") != nil {
			data.OOB = true
			if err := h.Templates.ExecuteTemplate(&buf, "lancamentos-filter-period", data); err != nil {
				log.Printf("template lancamentos-filter-period oob error: %v", err)
			}
		}
	}
	perfStep(reqID, "Lancamentos", "templateRender", time.Since(tR))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "Lancamentos", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isListPartial {
		cleanQ := r.URL.Query()
		cleanQ.Del("partial")
		if cleanQ.Get("q") == "" {
			cleanQ.Del("q")
		}
		w.Header().Set("HX-Replace-Url", "/lancamentos?"+cleanQ.Encode())
	}
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

func (h *TransactionHandler) HandleDeletarTransacao(w http.ResponseWriter, r *http.Request, id string) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)
	tParse := time.Now()
	returnInvoiceID := r.URL.Query().Get("invoice_id")
	fromSheet := r.URL.Query().Get("from_sheet") == "1"
	scope := normalizeDeleteScope(r.URL.Query().Get("escopo"))
	if scope == "" {
		scope = normalizeDeleteScope(r.FormValue("escopo"))
	}
	if scope == "" {
		scope = "single"
	}
	perfStep(reqID, "DeletarTransacao", "parse+validate", time.Since(tParse))

	tBegin := time.Now()
	tx, err := h.DB.Begin()
	perfStep(reqID, "DeletarTransacao", "beginTx", time.Since(tBegin))
	if err != nil {
		log.Printf("begin tx error: %v", err)
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro interno ao iniciar transação"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var invoiceID sql.NullString
	var parentID sql.NullString
	var recurringRuleID sql.NullString
	var installmentNumber int64
	var totalInstallments int64
	var txDate int64
	var ignoredType string
	var ignoredAmount int64
	var ignoredAccountID string
	var ignoredDestAccountID sql.NullString
	var ignoredStatus string
	var ignoredAccType string

	tLoad := time.Now()
	err = tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, a.type, t.invoice_id, t.parent_id, t.recurring_rule_id, t.installment_number, t.total_installments, t.date
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&ignoredType, &ignoredAmount, &ignoredAccountID, &ignoredDestAccountID, &ignoredStatus, &ignoredAccType, &invoiceID, &parentID, &recurringRuleID, &installmentNumber, &totalInstallments, &txDate)
	perfStep(reqID, "DeletarTransacao", "loadTransaction+workspace", time.Since(tLoad))
	if err != nil {
		log.Printf("find transaction error: %v", err)
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Transação não encontrada"}}`)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	tPermission := time.Now()
	if returnInvoiceID != "" && (!invoiceID.Valid || invoiceID.String != returnInvoiceID) {
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Fatura não autorizada ou não encontrada"}}`)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	perfStep(reqID, "DeletarTransacao", "permission+context", time.Since(tPermission))

	rootID := id
	if parentID.Valid {
		rootID = parentID.String
	}

	selectionSQL := `SELECT t.id
		FROM transactions t
		WHERE t.workspace_id = ? AND t.id = ?`
	args := []interface{}{h.WorkspaceID, id}
	if scope == "future" {
		switch {
		case recurringRuleID.Valid:
			selectionSQL = `SELECT t.id
				FROM transactions t
				WHERE t.workspace_id = ?
				  AND t.recurring_rule_id = ?
				  AND t.date >= ?`
			args = []interface{}{h.WorkspaceID, recurringRuleID.String, txDate}
		case totalInstallments > 1:
			selectionSQL = `SELECT t.id
				FROM transactions t
				WHERE t.workspace_id = ?
				  AND (t.id = ? OR t.parent_id = ?)
				  AND t.installment_number >= ?`
			args = []interface{}{h.WorkspaceID, rootID, rootID, installmentNumber}
		}
	} else if scope == "all" {
		switch {
		case recurringRuleID.Valid:
			selectionSQL = `SELECT t.id
				FROM transactions t
				WHERE t.workspace_id = ?
				  AND t.recurring_rule_id = ?`
			args = []interface{}{h.WorkspaceID, recurringRuleID.String}
		case totalInstallments > 1:
			selectionSQL = `SELECT t.id
				FROM transactions t
				WHERE t.workspace_id = ?
				  AND (t.id = ? OR t.parent_id = ?)`
			args = []interface{}{h.WorkspaceID, rootID, rootID}
		}
	}

	tSelect := time.Now()
	rows, err := tx.Query(selectionSQL, args...)
	if err != nil {
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao carregar série"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var idsToDelete []string
	for rows.Next() {
		var txID string
		if err := rows.Scan(&txID); err != nil {
			rows.Close()
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao carregar série"}}`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		idsToDelete = append(idsToDelete, txID)
	}
	if err := rows.Close(); err != nil {
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao carregar série"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if len(idsToDelete) == 0 {
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Transação não encontrada"}}`)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	perfStep(reqID, "DeletarTransacao", "selectScopeIDs", time.Since(tSelect))

	now := time.Now().Unix()
	tReverse := time.Now()
	for _, txID := range idsToDelete {
		if err := h.reverseActiveConsumesForTransactionTx(tx, txID, now); err != nil {
			log.Printf("reverse consume on delete error: %v", err)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao ajustar reserva"}}`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	perfStep(reqID, "DeletarTransacao", "reverseConsumes", time.Since(tReverse))
	tDelete := time.Now()
	if err := h.deleteTransactionsByIDsTx(tx, idsToDelete, now); err != nil {
		if errors.Is(err, errPaidInvoiceMutationBlocked) {
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Não é possível alterar lançamentos de fatura paga"}}`)
			w.WriteHeader(http.StatusConflict)
			return
		}
		log.Printf("delete transaction error: %v", err)
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao remover transação"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "DeletarTransacao", "deleteRows", time.Since(tDelete))
	tRecurrence := time.Now()
	if scope == "all" && recurringRuleID.Valid {
		if err := execOneTx(tx, `DELETE FROM recurring_rules WHERE id = ? AND workspace_id = ?`, recurringRuleID.String, h.WorkspaceID); err != nil {
			log.Printf("delete recurring rule error: %v", err)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao remover regra recorrente"}}`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	} else if scope == "future" && recurringRuleID.Valid {
		if err := execOneTx(tx, `UPDATE recurring_rules SET active = 0, updated_at = ? WHERE id = ? AND workspace_id = ?`, now, recurringRuleID.String, h.WorkspaceID); err != nil {
			log.Printf("update recurring rule error: %v", err)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao desativar regra recorrente"}}`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	perfStep(reqID, "DeletarTransacao", "recurrenceHandling", time.Since(tRecurrence))

	tCommit := time.Now()
	if err := tx.Commit(); err != nil {
		log.Printf("commit error: %v", err)
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro interno ao consolidar remoção"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "DeletarTransacao", "commit", time.Since(tCommit))

	if returnInvoiceID != "" {
		tInvoiceRender := time.Now()
		if err := h.renderInvoiceContextOOB(w, r, returnInvoiceID, fromSheet); err != nil {
			log.Printf("render invoice ctx oob error: %v", err)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao atualizar fatura visualmente"}}`)
			w.WriteHeader(http.StatusInternalServerError)
		}
		perfStep(reqID, "DeletarTransacao", "renderInvoiceContextOOB", time.Since(tInvoiceRender))
		tResumo := time.Now()
		_ = h.renderLancamentosResumoOOB(w, r)
		perfStep(reqID, "DeletarTransacao", "renderLancamentosResumoOOB", time.Since(tResumo))
		perfDBDelta(reqID, "DeletarTransacao", "total", dbB, dbSnap(h.DB))
		perfStep(reqID, "DeletarTransacao", "responseTotal", time.Since(t0))
		return
	}

	w.Header().Set("HX-Trigger", "refreshFinancials")

	if !isLancamentosContext(r) {
		tDashboard := time.Now()
		if err := h.renderDashboardOOB(w); err != nil {
			log.Printf("delete dashboard render error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		perfStep(reqID, "DeletarTransacao", "renderDashboardOOB", time.Since(tDashboard))

		tResumo := time.Now()
		_ = h.renderLancamentosResumoOOB(w, r)
		perfStep(reqID, "DeletarTransacao", "renderLancamentosResumoOOB", time.Since(tResumo))
	}
	perfDBDelta(reqID, "DeletarTransacao", "total", dbB, dbSnap(h.DB))
	perfStep(reqID, "DeletarTransacao", "responseTotal", time.Since(t0))
}

func normalizeDeleteScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "single":
		return strings.ToLower(strings.TrimSpace(raw))
	case "future":
		return "future"
	case "all":
		return "all"
	default:
		return ""
	}
}

func (h *TransactionHandler) deleteTransactionsByIDsTx(tx *sql.Tx, ids []string, now int64) error {
	if len(ids) == 0 {
		return nil
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
		return err
	}
	query := `
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id, t.status, t.invoice_id, a.type
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.id IN (` + sqlPlaceholders(len(ids)) + `)
	`
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, h.WorkspaceID)
	args = appendStrings(args, ids)

	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	type txRow struct {
		id            string
		trType        string
		amount        int64
		accountID     string
		destinationID sql.NullString
		status        string
		invoiceID     sql.NullString
		accountType   string
	}
	byID := make(map[string]txRow, len(ids))
	touchedInvoices := make(map[string]struct{})
	for rows.Next() {
		var row txRow
		if err := rows.Scan(&row.id, &row.trType, &row.amount, &row.accountID, &row.destinationID, &row.status, &row.invoiceID, &row.accountType); err != nil {
			rows.Close()
			return err
		}
		byID[row.id] = row
		if row.invoiceID.Valid && row.invoiceID.String != "" {
			touchedInvoices[row.invoiceID.String] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			return fmt.Errorf("transacao %s não encontrada no workspace", id)
		}
		dest := ""
		if row.destinationID.Valid {
			dest = row.destinationID.String
		}
		if row.status == "paid" {
			if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, row.trType, row.accountType, row.amount, row.accountID, dest, now); err != nil {
				return err
			}
		}
		if err := execOneTx(tx, `DELETE FROM transactions WHERE id = ? AND workspace_id = ?`, row.id, h.WorkspaceID); err != nil {
			return err
		}
	}
	for invID := range touchedInvoices {
		if invID == "" {
			continue
		}
		if err := reconcileInvoiceStatusTx(tx, h.WorkspaceID, invID); err != nil {
			return err
		}
	}
	return nil
}

func (h *TransactionHandler) HandleMoverFatura(w http.ResponseWriter, r *http.Request, id string) {
	tx, err := h.DB.Begin()
	if err != nil {
		log.Printf("begin move invoice tx error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	current, err := h.fetchInvoiceMoveCandidateTx(tx, id)
	if err != nil {
		log.Printf("move invoice candidate error: %v", err)
		http.Error(w, "lançamento não encontrado ou não autorizado", http.StatusNotFound)
		return
	}

	items, err := h.invoiceMoveQueueTx(tx, current)
	if err != nil {
		log.Printf("move invoice queue error: %v", err)
		http.Error(w, "erro ao montar cadeia de faturas", http.StatusInternalServerError)
		return
	}
	if len(items) == 0 {
		http.Error(w, "nenhum lançamento elegível para mover", http.StatusNotFound)
		return
	}

	now := time.Now().Unix()
	touchedInvoices := make(map[string]struct{})
	if current.invoiceID != "" {
		touchedInvoices[current.invoiceID] = struct{}{}
	}
	for _, item := range items {
		nextInvoiceID, err := ensureInvoiceAfterReferenceTx(tx, h.WorkspaceID, item.accountID, item.reference)
		if err != nil {
			log.Printf("ensure next invoice error: %v", err)
			http.Error(w, "erro ao localizar próxima fatura", http.StatusInternalServerError)
			return
		}
		if err := execOneTx(tx, `
			UPDATE transactions
			SET invoice_id = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, nextInvoiceID, now, item.id, h.WorkspaceID); err != nil {
			log.Printf("move invoice update error: %v", err)
			http.Error(w, "erro ao mover lançamento", http.StatusInternalServerError)
			return
		}
		touchedInvoices[nextInvoiceID] = struct{}{}
	}

	for invID := range touchedInvoices {
		if invID == "" {
			continue
		}
		if err := reconcileInvoiceStatusTx(tx, h.WorkspaceID, invID); err != nil {
			log.Printf("move invoice reconcile error: %v", err)
			http.Error(w, "erro ao reconcilizar fatura", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("move invoice commit error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := h.renderInvoiceMoveOOB(w, r, current.invoiceID); err != nil {
		log.Printf("move invoice render error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

type invoiceMoveCandidate struct {
	id                string
	accountID         string
	invoiceID         string
	reference         string
	invoiceStatus     string
	parentID          sql.NullString
	recurringRuleID   sql.NullString
	totalInstallments int64
	date              int64
}

type invoiceMoveItem struct {
	id        string
	accountID string
	reference string
}

func (h *TransactionHandler) fetchInvoiceMoveCandidateTx(tx *sql.Tx, id string) (invoiceMoveCandidate, error) {
	var item invoiceMoveCandidate
	err := tx.QueryRow(`
		SELECT t.id, t.account_id, t.invoice_id, i.reference, i.status,
		       t.parent_id, t.recurring_rule_id, t.total_installments, t.date
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ?
		  AND t.workspace_id = ?
		  AND a.workspace_id = ?
		  AND a.type = ?
		  AND t.type = ?
	`, id, h.WorkspaceID, h.WorkspaceID, models.AccountTypeCreditCard, models.TransactionTypeExpense).Scan(
		&item.id,
		&item.accountID,
		&item.invoiceID,
		&item.reference,
		&item.invoiceStatus,
		&item.parentID,
		&item.recurringRuleID,
		&item.totalInstallments,
		&item.date,
	)
	return item, err
}

func (h *TransactionHandler) invoiceMoveQueueTx(tx *sql.Tx, current invoiceMoveCandidate) ([]invoiceMoveItem, error) {
	if !current.parentID.Valid && !current.recurringRuleID.Valid && current.totalInstallments <= 1 {
		return []invoiceMoveItem{{id: current.id, accountID: current.accountID, reference: current.reference}}, nil
	}

	var rows *sql.Rows
	var err error
	if current.recurringRuleID.Valid {
		rows, err = tx.Query(`
			SELECT t.id, t.account_id, i.reference
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			JOIN invoices i ON i.id = t.invoice_id
			WHERE t.workspace_id = ?
			  AND a.workspace_id = ?
			  AND a.type = ?
			  AND t.type = ?
			  AND t.recurring_rule_id = ?
			  AND t.date >= ?
			  AND i.status IN ('OPEN', 'CLOSED')
			ORDER BY t.date ASC, t.created_at ASC
		`, h.WorkspaceID, h.WorkspaceID, models.AccountTypeCreditCard, models.TransactionTypeExpense, current.recurringRuleID.String, current.date)
	} else {
		rootID := current.id
		if current.parentID.Valid {
			rootID = current.parentID.String
		}
		rows, err = tx.Query(`
			SELECT t.id, t.account_id, i.reference
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			JOIN invoices i ON i.id = t.invoice_id
			WHERE t.workspace_id = ?
			  AND a.workspace_id = ?
			  AND a.type = ?
			  AND t.type = ?
			  AND (t.id = ? OR t.parent_id = ?)
			  AND t.date >= ?
			  AND i.status IN ('OPEN', 'CLOSED')
			ORDER BY t.installment_number ASC, t.date ASC
		`, h.WorkspaceID, h.WorkspaceID, models.AccountTypeCreditCard, models.TransactionTypeExpense, rootID, rootID, current.date)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []invoiceMoveItem
	for rows.Next() {
		var item invoiceMoveItem
		if err := rows.Scan(&item.id, &item.accountID, &item.reference); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func ensureInvoiceAfterReferenceTx(tx *sql.Tx, workspaceID, accountID string, reference string) (string, error) {
	nextReference, err := nextInvoiceReference(reference)
	if err != nil {
		return "", err
	}
	invoiceID, _, _, _, _, err := ensureFirstOpenInvoiceFromReferenceTx(tx, workspaceID, accountID, nextReference)
	if err != nil {
		return "", err
	}
	return invoiceID, nil
}

func (h *TransactionHandler) renderInvoiceMoveOOB(w http.ResponseWriter, r *http.Request, invoiceID string) error {
	return h.renderInvoiceContextOOB(w, r, invoiceID, false)
}

func (h *TransactionHandler) renderInvoiceContextOOB(w http.ResponseWriter, r *http.Request, invoiceID string, closeSheet bool) error {
	invoiceData, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, faturaSortOrderFromCurrentRequest(r))
	if err != nil {
		return err
	}
	invoiceData.OOB = true
	dashboardData := BuildDashboardData(h.DB, h.UserID, h.WorkspaceID)
	dashboardData.OOB = true

	var buf bytes.Buffer
	for _, templateName := range []string{"invoice-summary", "invoice-transactions"} {
		if err := h.Templates.ExecuteTemplate(&buf, templateName, invoiceData); err != nil {
			return err
		}
	}
	for _, templateName := range dashboardOOBTemplateNames() {
		if h.Templates.Lookup(templateName) == nil {
			continue
		}
		if err := h.Templates.ExecuteTemplate(&buf, templateName, dashboardData); err != nil {
			return err
		}
	}
	if closeSheet {
		buf.WriteString(`<div id="bottom-sheet-container" hx-swap-oob="innerHTML"></div>`)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = buf.WriteTo(w)
	return err
}

func (h *TransactionHandler) renderDashboardOOB(w http.ResponseWriter) error {
	data := BuildDashboardData(h.DB, h.UserID, h.WorkspaceID)
	data.OOB = true
	var buf bytes.Buffer
	for _, templateName := range dashboardOOBTemplateNames() {
		if h.Templates.Lookup(templateName) == nil {
			continue
		}
		if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
			return err
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

func dashboardOOBTemplateNames() []string {
	return []string{"dashboard-summary", "dashboard-balance", "dashboard-accounts", "dashboard-cards", "dashboard-health-cards"}
}

func (h *TransactionHandler) HandleDetalheTransacao(w http.ResponseWriter, r *http.Request, id string) {
	returnInvoiceID := r.URL.Query().Get("invoice_id")
	row, err := h.fetchTransactionRow(id)
	if err != nil {
		log.Printf("fetch transaction error: %v", err)
		http.Error(w, "transação não encontrada", http.StatusNotFound)
		return
	}

	accounts, _ := h.queryFormAccounts()
	categories, _ := h.queryFormCategories()

	status := "paid"
	if row.IsPending {
		status = "pending"
	}

	data := newFormTransacaoData(accounts, categories)
	data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == "business"
	if data.IsBusiness {
		data.Contacts, _ = h.queryFormContacts()
	}
	data.IsEdit = true
	data.EditID = id
	data.TipoInicial = formTypeFromTransactionType(row.Type)
	data.ValorPreenchido = "R$ " + row.AmountInput
	data.DescricaoPreenchida = row.Description
	data.DataPreenchida = row.DateInput
	data.StatusInicial = status

	var accountID string
	var destinationAccountID sql.NullString
	var categoryID sql.NullString
	var totalInstallments int64
	var recurringRuleID sql.NullString
	var recurrenceFrequency sql.NullString
	var invoiceID sql.NullString
	var notes string
	var attachmentPath string
	var dueDate sql.NullInt64
	var contactID sql.NullString
	var updatedAt int64
	var authorName string
	if err := h.DB.QueryRow(`
		SELECT t.account_id, t.destination_account_id, t.category_id, t.total_installments, t.recurring_rule_id, rr.frequency, t.invoice_id, t.notes, t.attachment_path, t.due_date, t.contact_id, t.updated_at, u.name
		FROM transactions t
		JOIN users u ON u.id = t.user_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&accountID, &destinationAccountID, &categoryID, &totalInstallments, &recurringRuleID, &recurrenceFrequency, &invoiceID, &notes, &attachmentPath, &dueDate, &contactID, &updatedAt, &authorName); err == nil {
		data.AnotacoesPreenchidas = notes
		if attachmentPath != "" {
			data.AttachmentViewURL = "/transacoes/" + id + "/anexo"
			data.AttachmentDownloadURL = "/transacoes/" + id + "/anexo?download=1"
		}
		data.AuditCreatedBy = authorName
		data.AuditUpdatedAt = formatDateTimeLabel(updatedAt)
		if returnInvoiceID != "" {
			if !invoiceID.Valid || invoiceID.String != returnInvoiceID {
				http.Error(w, "fatura não autorizada ou não encontrada", http.StatusNotFound)
				return
			}
			data.ReturnInvoiceID = returnInvoiceID
		}
		if acc, ok := findFormAccount(accounts, accountID); ok {
			applyFormOrigin(&data, acc)
		}
		if destinationAccountID.Valid {
			if acc, ok := findFormAccount(accounts, destinationAccountID.String); ok {
				applyFormDestination(&data, acc)
			}
		}
		if categoryID.Valid {
			data.CategoriaID = categoryID.String
			data.CategoriaNome = row.CategoryName
			data.CategoriaIcon = row.CategoryIcon
			data.CategoriaColor = row.CategoryColor
			data.CategoriaType = row.Type
			if row.Type == "INCOME" {
				data.CategoriaType = "INCOME"
			} else if row.Type == "EXPENSE" {
				data.CategoriaType = "EXPENSE"
			}
		}
		if recurringRuleID.Valid {
			data.FixoInicial = true
			data.RecorrenciaInicial = recurrenceFrequency.String
		}
		if dueDate.Valid {
			data.DueDatePreenchida = time.Unix(dueDate.Int64, 0).Format("2006-01-02")
		}
		if contactID.Valid {
			data.ContatoIDPreenchido = contactID.String
		}
		data.IsSeriesEdit = totalInstallments > 1 || recurringRuleID.Valid
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "form-lancamento", data); err != nil {
		log.Printf("template form-lancamento error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *TransactionHandler) HandleGerarRecibo(w http.ResponseWriter, r *http.Request, id string) {
	if workspaceType(h.DB, h.WorkspaceID) != "business" {
		http.Error(w, "Recibo disponível apenas para workspaces empresariais.", http.StatusUnprocessableEntity)
		return
	}

	data := TransacaoReciboData{
		TransactionID:     id,
		IssueDateLabel:    time.Now().Format("02/01/2006"),
		DeclarationPrep:   "DE",
		WorkspaceInitials: "WS",
	}
	var status, txType string
	var amount, txDate int64
	var logoFallback string
	err := h.DB.QueryRow(`
		SELECT
			COALESCE(w.name, ''),
			COALESCE(w.cnpj_cpf, ''),
			COALESCE(w.address, ''),
			COALESCE(w.phone, ''),
			COALESCE(w.logo_light_url, ''),
			COALESCE(w.logo_dark_url, ''),
			COALESCE((
				SELECT u.profile_photo_path
				FROM workspace_members wm
				JOIN users u ON u.id = wm.user_id
				WHERE wm.workspace_id = w.id
				ORDER BY CASE wm.role WHEN 'ADMIN' THEN 0 WHEN 'MANAGER' THEN 1 ELSE 2 END, wm.joined_at ASC
				LIMIT 1
			), ''),
			COALESCE(t.status, ''),
			COALESCE(t.type, ''),
			COALESCE(t.amount, 0),
			COALESCE(t.date, 0),
			COALESCE(t.description, ''),
			COALESCE(c.name, ''),
			COALESCE(c.document, '')
		FROM transactions t
		JOIN workspaces w ON w.id = t.workspace_id
		LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(
		&data.WorkspaceName,
		&data.WorkspaceDocument,
		&data.WorkspaceAddress,
		&data.WorkspacePhone,
		&data.WorkspaceLogoLightURL,
		&data.WorkspaceLogoURL,
		&logoFallback,
		&status,
		&txType,
		&amount,
		&txDate,
		&data.Description,
		&data.ContactName,
		&data.ContactDocument,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			http.Error(w, "Transação não encontrada.", http.StatusNotFound)
			return
		}
		http.Error(w, "Erro ao carregar dados do recibo.", http.StatusInternalServerError)
		return
	}

	if strings.TrimSpace(data.ContactName) == "" {
		data.ContactName = "Contato não informado"
	}
	if strings.TrimSpace(data.ContactDocument) == "" {
		data.ContactDocument = "não informado"
	}
	if strings.TrimSpace(data.Description) == "" {
		data.Description = "Lançamento financeiro"
	}
	if strings.TrimSpace(data.WorkspaceName) != "" {
		data.WorkspaceInitials = initials(data.WorkspaceName)
	}
	// Apply logo fallback: prefer light logo for print; fall back to dark logo, then profile photo
	if data.WorkspaceLogoLightURL == "" {
		if data.WorkspaceLogoURL != "" {
			data.WorkspaceLogoLightURL = data.WorkspaceLogoURL
		} else {
			data.WorkspaceLogoLightURL = logoFallback
		}
	}
	if data.WorkspaceLogoURL == "" {
		data.WorkspaceLogoURL = logoFallback
	}

	data.AmountLabel = "R$ " + formatCurrencyCentsBase(amount)
	data.DateLabel = formatDateLabelFromUnix(txDate)
	data.DeclarationVerb = "recebemos"
	data.DeclarationPrep = "DE"
	if txType == "EXPENSE" || txType == "TRANSFER" {
		data.DeclarationVerb = "pagamos"
		data.DeclarationPrep = "PARA"
	}

	if status != "paid" {
		data.WarningMessage = "Este recibo só pode ser emitido após a confirmação de pagamento deste lançamento."
		data.CanPrint = false
	} else {
		data.CanPrint = true
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "transacao-recibo-page", data); err != nil {
		http.Error(w, "Erro ao renderizar recibo.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !data.CanPrint {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	_, _ = buf.WriteTo(w)
}

func (h *TransactionHandler) HandleAtualizarTransacao(w http.ResponseWriter, r *http.Request, id string) {
	if err := parseMultipartOrForm(r); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	valorStr := r.FormValue("valor")
	returnInvoiceID := r.FormValue("return_invoice_id")
	descricao := r.FormValue("descricao")
	anotacoes := strings.TrimSpace(r.FormValue("anotacoes"))
	dataStr := r.FormValue("data")
	tipo := mapTransactionType(r.FormValue("tipo"))
	origemContaID := r.FormValue("origem_conta_id")
	destinoContaID := r.FormValue("destino_conta_id")
	categoriaID := r.FormValue("categoria_id")
	fixo := r.FormValue("lancamento_fixo") == "on"
	recorrencia := normalizeRecurrenceFrequency(r.FormValue("recorrencia"))
	allowBoxOverdraft := formAllowsBoxOverdraft(r)
	newStatus := r.FormValue("status_pagamento")
	if newStatus != "pending" {
		newStatus = "paid"
	}
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	dueDateUnix, hasDueDate := int64(0), false
	if isBusiness && newStatus != "paid" {
		if dueRaw := strings.TrimSpace(r.FormValue("due_date")); dueRaw != "" {
			if parsed, err := parseDate(dueRaw); err == nil {
				dueDateUnix = parsed
				hasDueDate = true
			}
		}
	}
	contactID := strings.TrimSpace(r.FormValue("contact_id"))
	if !isBusiness {
		contactID = ""
	}

	newAmount, err := parseCurrency(valorStr)
	if err != nil || newAmount <= 0 {
		http.Error(w, "valor inválido", http.StatusBadRequest)
		return
	}

	newDate, err := parseDate(dataStr)
	if err != nil {
		http.Error(w, "data inválida", http.StatusBadRequest)
		return
	}

	if tipo == "TRANSFER" {
		if origemContaID == "" || destinoContaID == "" {
			http.Error(w, "selecione as contas de origem e destino", http.StatusBadRequest)
			return
		}
		categoriaID = ""
	} else {
		if origemContaID == "" {
			http.Error(w, "Por favor, selecione uma conta de origem válida.", http.StatusUnprocessableEntity)
			return
		}
		if categoriaID == "" {
			http.Error(w, "A categoria é obrigatória para a classificação do fluxo de caixa.", http.StatusUnprocessableEntity)
			return
		}
		destinoContaID = ""
	}

	// Cartão de crédito: força status "paid" antes de qualquer caminho de update.
	if newStatus == "pending" && tipo == "EXPENSE" && origemContaID != "" {
		var preCheckType string
		if err := h.DB.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, origemContaID, h.WorkspaceID).Scan(&preCheckType); err == nil && preCheckType == "CREDIT_CARD" {
			newStatus = "paid"
		}
	}

	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var oldTrType, oldPaymentStatus string
	var oldAmount int64
	var oldAccountID string
	var oldDestAccountID sql.NullString
	var oldCategoryID sql.NullString
	var invoiceID sql.NullString
	var parentID sql.NullString
	var recurringRuleID sql.NullString
	var installmentNumber, totalInstallments int64
	var oldAccType string
	var oldAttachmentPath string
	var oldDateUnix int64

	err = tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, t.category_id, t.invoice_id, t.parent_id, t.recurring_rule_id, t.installment_number, t.total_installments, a.type, t.attachment_path, t.date
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&oldTrType, &oldAmount, &oldAccountID, &oldDestAccountID, &oldPaymentStatus, &oldCategoryID, &invoiceID, &parentID, &recurringRuleID, &installmentNumber, &totalInstallments, &oldAccType, &oldAttachmentPath, &oldDateUnix)
	if err != nil {
		http.Error(w, "transação não encontrada", http.StatusNotFound)
		return
	}
	if returnInvoiceID != "" && (!invoiceID.Valid || invoiceID.String != returnInvoiceID) {
		http.Error(w, "fatura não autorizada ou não encontrada", http.StatusNotFound)
		return
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{id}); err != nil {
		if errors.Is(err, errPaidInvoiceMutationBlocked) {
			http.Error(w, "não é possível alterar lançamento de fatura paga", http.StatusConflict)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	scope := normalizeEditScope(r.URL.Query().Get("escopo"))
	if scope == "" {
		scope = normalizeEditScope(r.FormValue("escopo"))
	}
	if scope == "" {
		scope = normalizeEditScope(r.FormValue("escopo_edicao"))
	}
	if scope == "" {
		scope = "single"
	}

	now := time.Now().Unix()
	attachmentPath, err := h.persistTransactionUpload(r)
	if err != nil {
		http.Error(w, "arquivo inválido ou não permitido", http.StatusBadRequest)
		return
	}
	if attachmentPath == "" {
		attachmentPath = oldAttachmentPath
	}
	if scope != "single" && (totalInstallments > 1 || recurringRuleID.Valid) {
		if recurringRuleID.Valid {
			if err := h.updateRecurringRule(tx, recurringRuleID.String, tipo, newAmount, newDate, descricao, origemContaID, destinoContaID, categoriaID, newStatus, recorrencia, now); err != nil {
				log.Printf("update recurring rule error: %v", err)
				http.Error(w, "erro ao atualizar recorrencia", http.StatusInternalServerError)
				return
			}
		}
		switch scope {
		case "future":
			if totalInstallments > 1 {
				if err := h.updateFutureTransactionSeries(tx, id, parentID, installmentNumber, tipo, newAmount, newDate, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateUnix, hasDueDate, contactID, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errPaidInvoiceMutationBlocked) {
						http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
						return
					}
					if errors.Is(err, errBoxReserveInsufficient) {
						http.Error(w, "saldo reservado insuficiente na reserva vinculada; confirme excedente para continuar", http.StatusUnprocessableEntity)
						return
					}
					if errors.Is(err, errBoxCategoryAmbiguous) {
						http.Error(w, "há mais de uma reserva vinculada para a categoria informada", http.StatusUnprocessableEntity)
						return
					}
					log.Printf("update transaction series error: %v", err)
					http.Error(w, "erro ao atualizar serie", http.StatusInternalServerError)
					return
				}
			}
			if recurringRuleID.Valid {
				if err := h.updateFutureRecurringTransactions(tx, recurringRuleID.String, id, oldDateUnix, newDate, oldDateUnix, tipo, newAmount, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateUnix, hasDueDate, contactID, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errPaidInvoiceMutationBlocked) {
						http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
						return
					}
					if errors.Is(err, errBoxReserveInsufficient) {
						http.Error(w, "saldo reservado insuficiente na reserva vinculada; confirme excedente para continuar", http.StatusUnprocessableEntity)
						return
					}
					if errors.Is(err, errBoxCategoryAmbiguous) {
						http.Error(w, "há mais de uma reserva vinculada para a categoria informada", http.StatusUnprocessableEntity)
						return
					}
					log.Printf("update recurring transactions error: %v", err)
					http.Error(w, "erro ao atualizar recorrencias futuras", http.StatusInternalServerError)
					return
				}
			}
			if totalInstallments <= 1 && recurringRuleID.Valid {
				if err := h.updateSingleTransactionInTx(tx, id, tipo, newAmount, newDate, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errPaidInvoiceMutationBlocked) {
						http.Error(w, "não é possível alterar lançamento de fatura paga", http.StatusConflict)
						return
					}
					log.Printf("update current recurring transaction error: %v", err)
					http.Error(w, "erro ao atualizar", http.StatusInternalServerError)
					return
				}
			}
		case "all":
			if totalInstallments > 1 {
				if err := h.updateAllTransactionSeries(tx, id, parentID, tipo, newAmount, newDate, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateUnix, hasDueDate, contactID, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errPaidInvoiceMutationBlocked) {
						http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
						return
					}
					if errors.Is(err, errBoxReserveInsufficient) {
						http.Error(w, "saldo reservado insuficiente na reserva vinculada; confirme excedente para continuar", http.StatusUnprocessableEntity)
						return
					}
					if errors.Is(err, errBoxCategoryAmbiguous) {
						http.Error(w, "há mais de uma reserva vinculada para a categoria informada", http.StatusUnprocessableEntity)
						return
					}
					log.Printf("update all transaction series error: %v", err)
					http.Error(w, "erro ao atualizar serie", http.StatusInternalServerError)
					return
				}
			}
			if recurringRuleID.Valid {
				if err := h.updateAllRecurringTransactions(tx, recurringRuleID.String, newDate, oldDateUnix, tipo, newAmount, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateUnix, hasDueDate, contactID, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errPaidInvoiceMutationBlocked) {
						http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
						return
					}
					if errors.Is(err, errBoxReserveInsufficient) {
						http.Error(w, "saldo reservado insuficiente na reserva vinculada; confirme excedente para continuar", http.StatusUnprocessableEntity)
						return
					}
					if errors.Is(err, errBoxCategoryAmbiguous) {
						http.Error(w, "há mais de uma reserva vinculada para a categoria informada", http.StatusUnprocessableEntity)
						return
					}
					log.Printf("update all recurring transactions error: %v", err)
					http.Error(w, "erro ao atualizar recorrencias", http.StatusInternalServerError)
					return
				}
			}
		default:
			http.Error(w, "escopo inválido", http.StatusBadRequest)
			return
		}
		if recurringRuleID.Valid {
			ruleTotalOccurrences, err := h.recurringRuleTotalOccurrencesTx(tx, recurringRuleID.String)
			if err != nil {
				log.Printf("query recurring rule total occurrences error: %v", err)
				http.Error(w, "erro ao carregar recorrencia", http.StatusInternalServerError)
				return
			}
			if err := h.generateRecurrenceProjection(tx, recurrenceProjectionRule{
				ID:                   recurringRuleID.String,
				AccountID:            origemContaID,
				DestinationAccountID: destinoContaID,
				CategoryID:           categoriaID,
				Type:                 tipo,
				Amount:               newAmount,
				Description:          descricao,
				StartDate:            newDate,
				Frequency:            recorrencia,
				DefaultPaymentStatus: strings.ToUpper(newStatus),
				TotalOccurrences:     ruleTotalOccurrences,
			}, now); err != nil {
				log.Printf("generate recurrence projection error: %v", err)
				http.Error(w, "erro ao atualizar recorrencia", http.StatusInternalServerError)
				return
			}
		}
		if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, []string{id}); err != nil {
			http.Error(w, "erro ao reconcilizar fatura", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if returnInvoiceID != "" {
			if err := h.renderInvoiceContextOOB(w, r, returnInvoiceID, true); err != nil {
				log.Printf("render invoice after series update error: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
			return
		}
		if isHXFromContas(r) {
			respondContasRefreshAndCloseSheet(w)
			return
		}
		h.respondBroadEditAndCloseSheet(w, r)
		return
	}

	if fixo && !recurringRuleID.Valid {
		ruleID := uuid.NewString()
		if err := h.insertRecurringRuleTx(tx, ruleID, tipo, newAmount, newDate, descricao, origemContaID, destinoContaID, categoriaID, newStatus, recorrencia, now); err != nil {
			log.Printf("update transaction series error: %v", err)
			http.Error(w, "erro ao criar recorrencia", http.StatusInternalServerError)
			return
		}
		if err := h.generateRecurrenceProjection(tx, recurrenceProjectionRule{
			ID:                   ruleID,
			AccountID:            origemContaID,
			DestinationAccountID: destinoContaID,
			CategoryID:           categoriaID,
			Type:                 tipo,
			Amount:               newAmount,
			Description:          descricao,
			StartDate:            newDate,
			Frequency:            recorrencia,
			DefaultPaymentStatus: strings.ToUpper(newStatus),
		}, now); err != nil {
			http.Error(w, "erro ao criar recorrencia", http.StatusInternalServerError)
			return
		}
		recurringRuleID = sql.NullString{String: ruleID, Valid: true}
	}

	oldDest := ""
	if oldDestAccountID.Valid {
		oldDest = oldDestAccountID.String
	}

	if oldPaymentStatus == "paid" {
		if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, oldTrType, oldAccType, oldAmount, oldAccountID, oldDest, now); err != nil {
			http.Error(w, "erro ao ajustar saldo", http.StatusInternalServerError)
			return
		}
	}

	catID := interface{}(nil)
	if categoriaID != "" {
		catID = categoriaID
	}
	destID := interface{}(nil)
	if destinoContaID != "" {
		destID = destinoContaID
	}
	dueDateRef := interface{}(nil)
	if hasDueDate {
		dueDateRef = dueDateUnix
	}
	contactRef := interface{}(nil)
	if contactID != "" {
		contactRef = contactID
	}

	var newAccType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, origemContaID, h.WorkspaceID).Scan(&newAccType); err != nil {
		http.Error(w, "conta não encontrada", http.StatusBadRequest)
		return
	}
	// Cartão de crédito: força status "paid" (lançamentos já consomem limite).
	if newAccType == "CREDIT_CARD" {
		newStatus = "paid"
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		http.Error(w, "conta não encontrada", http.StatusBadRequest)
		return
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		http.Error(w, "categoria não encontrada", http.StatusBadRequest)
		return
	}
	if err := ensureContactInWorkspaceTx(tx, contactID, h.WorkspaceID); err != nil {
		http.Error(w, "contato não encontrado", http.StatusBadRequest)
		return
	}

	if err := h.adjustReserveForSingleTransactionUpdateTx(tx, id, oldTrType, oldCategoryID, oldAmount, tipo, categoriaID, newAmount, newDate, now, allowBoxOverdraft); err != nil {
		switch {
		case errors.Is(err, errBoxReserveInsufficient):
			http.Error(w, "saldo reservado insuficiente na reserva vinculada; confirme excedente para continuar", http.StatusUnprocessableEntity)
		case errors.Is(err, errBoxCategoryAmbiguous):
			http.Error(w, "há mais de uma reserva vinculada para a categoria informada", http.StatusUnprocessableEntity)
		default:
			http.Error(w, "erro ao ajustar reserva", http.StatusInternalServerError)
		}
		return
	}

	oldInvoiceID := invoiceID
	invoiceID = sql.NullString{}
	if tipo == "EXPENSE" && newAccType == "CREDIT_CARD" {
		invID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, h.WorkspaceID, origemContaID, newDate, r.FormValue("fatura_offset"))
		if err != nil {
			http.Error(w, "erro na fatura", http.StatusInternalServerError)
			return
		}
		invoiceID = sql.NullString{String: invID, Valid: true}
	}

	err = execOneTx(tx, `
		UPDATE transactions
		SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, due_date = ?, contact_id = ?, invoice_id = ?, recurring_rule_id = ?, recurrence_sequence = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
	`, tipo, origemContaID, destID, newAmount, newDate, descricao, anotacoes, attachmentPath, catID, newStatus, dueDateRef, contactRef, invoiceID, nullStringInterface(recurringRuleID), recurrenceSequence(1, recurringRuleID.Valid), now, id, h.WorkspaceID)
	if err != nil {
		http.Error(w, "erro ao atualizar", http.StatusInternalServerError)
		return
	}

	if newStatus == "paid" {
		if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, newAccType, newStatus, newAmount, origemContaID, destinoContaID, now); err != nil {
			http.Error(w, "erro ao ajustar saldo", http.StatusInternalServerError)
			return
		}
	}

	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, []string{id}); err != nil {
		http.Error(w, "erro ao reconcilizar fatura", http.StatusInternalServerError)
		return
	}
	if oldInvoiceID.Valid && oldInvoiceID.String != "" {
		if err := reconcileInvoiceStatusTx(tx, h.WorkspaceID, oldInvoiceID.String); err != nil {
			http.Error(w, "erro ao reconcilizar fatura anterior", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if returnInvoiceID != "" {
		if err := h.renderInvoiceContextOOB(w, r, returnInvoiceID, true); err != nil {
			log.Printf("render invoice after update error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}

	if isHXFromContas(r) {
		respondContasRefreshAndCloseSheet(w)
		return
	}

	h.respondRowAndCloseSheet(w, r, id)
}

func normalizeEditScope(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "single", "":
		return strings.ToLower(strings.TrimSpace(raw))
	case "future":
		return "future"
	case "all":
		return "all"
	default:
		return ""
	}
}

func projectionStatusForRule(defaultStatus string) string {
	if strings.EqualFold(strings.TrimSpace(defaultStatus), "paid") {
		return "paid"
	}
	return "pending"
}

func recurringRuleDefaultStatus(paymentStatus, accountType, transactionType string) string {
	if accountType == models.AccountTypeCreditCard && transactionType == models.TransactionTypeExpense {
		return strings.ToUpper(paymentStatus)
	}
	return "PENDING"
}

func (h *TransactionHandler) recurringRuleTotalOccurrencesTx(tx *sql.Tx, ruleID string) (*int64, error) {
	var total sql.NullInt64
	if err := tx.QueryRow(`
		SELECT total_occurrences
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, ruleID, h.WorkspaceID).Scan(&total); err != nil {
		return nil, err
	}
	if !total.Valid || total.Int64 <= 0 {
		return nil, nil
	}
	value := total.Int64
	return &value, nil
}

func (h *TransactionHandler) updateFutureTransactionSeries(tx *sql.Tx, currentID string, parentID sql.NullString, fromInstallment int64, tipo string, newAmount int64, newDate int64, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDateUnix int64, hasDueDate bool, contactID string, now int64, allowBoxOverdraft bool) error {
	var newAccType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, origemContaID, h.WorkspaceID).Scan(&newAccType); err != nil {
		return fmt.Errorf("new account type: %w", err)
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureContactInWorkspaceTx(tx, contactID, h.WorkspaceID); err != nil {
		return err
	}

	rootID := currentID
	if parentID.Valid {
		rootID = parentID.String
	}

	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id, t.category_id, t.status, t.installment_number, a.type
		FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND (t.id = ? OR t.parent_id = ?)
		  AND t.installment_number >= ?
		ORDER BY t.installment_number ASC
	`, h.WorkspaceID, rootID, rootID, fromInstallment)
	if err != nil {
		return fmt.Errorf("query future installments: %w", err)
	}

	type installmentRow struct {
		id                string
		trType            string
		amount            int64
		accountID         string
		destinationID     sql.NullString
		categoryID        sql.NullString
		paymentStatus     string
		installmentNumber int64
		accountType       string
	}
	var installments []installmentRow
	for rows.Next() {
		var row installmentRow
		if err := rows.Scan(&row.id, &row.trType, &row.amount, &row.accountID, &row.destinationID, &row.categoryID, &row.paymentStatus, &row.installmentNumber, &row.accountType); err != nil {
			rows.Close()
			return fmt.Errorf("scan future installment: %w", err)
		}
		installments = append(installments, row)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close future installments: %w", err)
	}
	if len(installments) == 0 {
		return fmt.Errorf("no future installments")
	}
	ids := make([]string, 0, len(installments))
	for _, item := range installments {
		ids = append(ids, item.id)
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
		return err
	}

	var catID interface{}
	if categoriaID != "" {
		catID = categoriaID
	}
	var destID interface{}
	if destinoContaID != "" {
		destID = destinoContaID
	}
	var dueDateRef interface{}
	if hasDueDate {
		dueDateRef = dueDateUnix
	}
	var contactRef interface{}
	if contactID != "" {
		contactRef = contactID
	}

	for _, old := range installments {
		oldDest := ""
		if old.destinationID.Valid {
			oldDest = old.destinationID.String
		}
		if old.paymentStatus == "paid" {
			if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, old.trType, old.accountType, old.amount, old.accountID, oldDest, now); err != nil {
				return fmt.Errorf("reverse installment balance: %w", err)
			}
		}

		installmentDate := safeAddMonths(newDate, old.installmentNumber-fromInstallment)
		if err := h.adjustReserveForSingleTransactionUpdateTx(tx, old.id, old.trType, old.categoryID, old.amount, tipo, categoriaID, newAmount, installmentDate, now, allowBoxOverdraft); err != nil {
			return err
		}
		var invoiceID interface{}
		if tipo == "EXPENSE" && newAccType == "CREDIT_CARD" {
			invID, _, _, _, _, err := ensureOpenInvoiceTx(tx, h.WorkspaceID, origemContaID, installmentDate)
			if err != nil {
				return fmt.Errorf("invoice: %w", err)
			}
			invoiceID = invID
		}

		if err := execOneTx(tx, `
			UPDATE transactions
				SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, due_date = ?, contact_id = ?, invoice_id = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ?
			`, tipo, origemContaID, destID, newAmount, installmentDate, descricao, anotacoes, attachmentPath, catID, newStatus, dueDateRef, contactRef, invoiceID, now, old.id, h.WorkspaceID); err != nil {
			return fmt.Errorf("update installment: %w", err)
		}

		if newStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, newAccType, newStatus, newAmount, origemContaID, destinoContaID, now); err != nil {
				return fmt.Errorf("apply installment balance: %w", err)
			}
		}
	}

	return nil
}

func (h *TransactionHandler) updateAllTransactionSeries(tx *sql.Tx, currentID string, parentID sql.NullString, tipo string, newAmount int64, newDate int64, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDateUnix int64, hasDueDate bool, contactID string, now int64, allowBoxOverdraft bool) error {
	rootID := currentID
	if parentID.Valid {
		rootID = parentID.String
	}
	return h.updateFutureTransactionSeries(tx, rootID, sql.NullString{String: rootID, Valid: true}, 1, tipo, newAmount, newDate, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateUnix, hasDueDate, contactID, now, allowBoxOverdraft)
}

func (h *TransactionHandler) updateRecurringRule(tx *sql.Tx, ruleID string, tipo string, newAmount int64, newDate int64, descricao string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, recurrenceFrequency string, now int64) error {
	if err := ensureAccountInWorkspaceTx(tx, origemContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return err
	}

	var catID interface{}
	if categoriaID != "" {
		catID = categoriaID
	}
	var destID interface{}
	if destinoContaID != "" {
		destID = destinoContaID
	}

	ruleStatus := strings.ToUpper(newStatus)
	err := execOneTx(tx, `
		UPDATE recurring_rules
		SET type = ?, account_id = ?, destination_account_id = ?, category_id = ?, amount = ?, description = ?, start_date = ?, frequency = ?, default_payment_status = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
	`, tipo, origemContaID, destID, catID, newAmount, descricao, newDate, recurrenceFrequency, ruleStatus, now, ruleID, h.WorkspaceID)
	return err
}

func (h *TransactionHandler) updateFutureRecurringTransactions(tx *sql.Tx, ruleID string, currentID string, fromDate int64, newDate int64, oldCurrentDate int64, tipo string, newAmount int64, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDateUnix int64, hasDueDate bool, contactID string, now int64, allowBoxOverdraft bool) error {
	delta := newDate - oldCurrentDate
	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id, t.category_id, t.status, a.type, t.date
		FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ? AND t.recurring_rule_id = ? AND t.id != ? AND t.date >= ?
	`, h.WorkspaceID, ruleID, currentID, fromDate)
	if err != nil {
		return err
	}

	type recurringTx struct {
		id            string
		trType        string
		amount        int64
		accountID     string
		destinationID sql.NullString
		categoryID    sql.NullString
		paymentStatus string
		accountType   string
		dateUnix      int64
	}
	var items []recurringTx
	for rows.Next() {
		var item recurringTx
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.categoryID, &item.paymentStatus, &item.accountType, &item.dateUnix); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.id)
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
		return err
	}

	var catID interface{}
	if categoriaID != "" {
		catID = categoriaID
	}
	var destID interface{}
	if destinoContaID != "" {
		destID = destinoContaID
	}

	var newAccType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, origemContaID, h.WorkspaceID).Scan(&newAccType); err != nil {
		return err
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureContactInWorkspaceTx(tx, contactID, h.WorkspaceID); err != nil {
		return err
	}
	var dueDateRef interface{}
	if hasDueDate {
		dueDateRef = dueDateUnix
	}
	var contactRef interface{}
	if contactID != "" {
		contactRef = contactID
	}

	for _, item := range items {
		newItemDate := item.dateUnix + delta
		oldDest := ""
		if item.destinationID.Valid {
			oldDest = item.destinationID.String
		}
		if item.paymentStatus == "paid" {
			if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, item.trType, item.accountType, item.amount, item.accountID, oldDest, now); err != nil {
				return err
			}
		}
		if err := h.adjustReserveForSingleTransactionUpdateTx(tx, item.id, item.trType, item.categoryID, item.amount, tipo, categoriaID, newAmount, newItemDate, now, allowBoxOverdraft); err != nil {
			return err
		}

		var invoiceID interface{}
		if tipo == "EXPENSE" && newAccType == "CREDIT_CARD" {
			invID, _, _, _, _, err := ensureOpenInvoiceTx(tx, h.WorkspaceID, origemContaID, newItemDate)
			if err != nil {
				return err
			}
			invoiceID = invID
		}

		if err := execOneTx(tx, `
			UPDATE transactions
				SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, due_date = ?, contact_id = ?, invoice_id = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ?
			`, tipo, origemContaID, destID, newAmount, newItemDate, descricao, anotacoes, attachmentPath, catID, newStatus, dueDateRef, contactRef, invoiceID, now, item.id, h.WorkspaceID); err != nil {
			return err
		}

		if newStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, newAccType, newStatus, newAmount, origemContaID, destinoContaID, now); err != nil {
				return err
			}
		}
	}

	return nil
}

func (h *TransactionHandler) updateAllRecurringTransactions(tx *sql.Tx, ruleID string, newDate int64, oldCurrentDate int64, tipo string, newAmount int64, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDateUnix int64, hasDueDate bool, contactID string, now int64, allowBoxOverdraft bool) error {
	return h.updateFutureRecurringTransactions(tx, ruleID, "", 0, newDate, oldCurrentDate, tipo, newAmount, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateUnix, hasDueDate, contactID, now, allowBoxOverdraft)
}

func (h *TransactionHandler) updateSingleTransactionInTx(tx *sql.Tx, id string, tipo string, newAmount int64, newDate int64, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, now int64, allowBoxOverdraft bool) error {
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{id}); err != nil {
		return err
	}

	var oldTrType, oldPaymentStatus string
	var oldAmount int64
	var oldAccountID string
	var oldDestAccountID sql.NullString
	var oldAccType string
	var oldCategoryID sql.NullString
	if err := tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, a.type, t.category_id
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&oldTrType, &oldAmount, &oldAccountID, &oldDestAccountID, &oldPaymentStatus, &oldAccType, &oldCategoryID); err != nil {
		return err
	}

	oldDest := ""
	if oldDestAccountID.Valid {
		oldDest = oldDestAccountID.String
	}
	if oldPaymentStatus == "paid" {
		if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, oldTrType, oldAccType, oldAmount, oldAccountID, oldDest, now); err != nil {
			return err
		}
	}

	var newAccType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, origemContaID, h.WorkspaceID).Scan(&newAccType); err != nil {
		return err
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return err
	}

	if err := h.adjustReserveForSingleTransactionUpdateTx(tx, id, oldTrType, oldCategoryID, oldAmount, tipo, categoriaID, newAmount, newDate, now, allowBoxOverdraft); err != nil {
		return err
	}

	var catID interface{}
	if categoriaID != "" {
		catID = categoriaID
	}
	var destID interface{}
	if destinoContaID != "" {
		destID = destinoContaID
	}
	var invoiceID interface{}
	if tipo == "EXPENSE" && newAccType == "CREDIT_CARD" {
		invID, _, _, _, _, err := ensureOpenInvoiceTx(tx, h.WorkspaceID, origemContaID, newDate)
		if err != nil {
			return err
		}
		invoiceID = invID
	}

	if err := execOneTx(tx, `
		UPDATE transactions
		SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, invoice_id = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
	`, tipo, origemContaID, destID, newAmount, newDate, descricao, anotacoes, attachmentPath, catID, newStatus, invoiceID, now, id, h.WorkspaceID); err != nil {
		return err
	}

	if newStatus == "paid" {
		if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, newAccType, newStatus, newAmount, origemContaID, destinoContaID, now); err != nil {
			return err
		}
	}
	return nil
}

func reserveAffectsExpense(categoryID string) bool {
	return strings.TrimSpace(categoryID) != ""
}

func reserveRelevantFieldsChanged(oldType string, oldCategoryID sql.NullString, oldAmount int64, newType, newCategoryID string, newAmount int64) bool {
	oldExpense := oldType == models.TransactionTypeExpense && oldCategoryID.Valid && reserveAffectsExpense(oldCategoryID.String)
	newExpense := newType == models.TransactionTypeExpense && reserveAffectsExpense(newCategoryID)

	if !oldExpense && !newExpense {
		return false
	}
	if oldExpense != newExpense {
		return true
	}
	return strings.TrimSpace(oldCategoryID.String) != strings.TrimSpace(newCategoryID) || oldAmount != newAmount
}

func (h *TransactionHandler) reverseActiveConsumesForTransactionTx(tx *sql.Tx, transactionID string, now int64) error {
	if strings.TrimSpace(transactionID) == "" {
		return nil
	}
	events, err := activeConsumeEventsBySourceTransactionTx(tx, h.WorkspaceID, transactionID)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := insertReversalLedgerEventTx(tx, event, transactionID, now, now); err != nil {
			return err
		}
	}
	return nil
}

func (h *TransactionHandler) adjustReserveForSingleTransactionUpdateTx(tx *sql.Tx, transactionID string, oldType string, oldCategoryID sql.NullString, oldAmount int64, newType, newCategoryID string, newAmount, newDate, now int64, allowBoxOverdraft bool) error {
	if !reserveRelevantFieldsChanged(oldType, oldCategoryID, oldAmount, newType, newCategoryID, newAmount) {
		return nil
	}

	if err := h.reverseActiveConsumesForTransactionTx(tx, transactionID, now); err != nil {
		return err
	}

	return h.consumeBoxReserveOnExpenseCreationTx(tx, newType, newCategoryID, newAmount, transactionID, newDate, now, allowBoxOverdraft)
}

func safeAddMonths(dateUnix int64, months int64) int64 {
	t := time.Unix(dateUnix, 0).UTC()
	originalDay := t.Day()

	targetYear := t.Year()
	targetMonth := int(t.Month()) + int(months)
	for targetMonth > 12 {
		targetMonth -= 12
		targetYear++
	}
	for targetMonth < 1 {
		targetMonth += 12
		targetYear--
	}

	firstOfNext := time.Date(targetYear, time.Month(targetMonth)+1, 1, 12, 0, 0, 0, time.UTC)
	lastDayOfTarget := firstOfNext.Add(-24 * time.Hour).Day()
	if originalDay > lastDayOfTarget {
		originalDay = lastDayOfTarget
	}

	return time.Date(targetYear, time.Month(targetMonth), originalDay, 12, 0, 0, 0, time.UTC).Unix()
}

func isHXFromContas(r *http.Request) bool {
	u := r.Header.Get("HX-Current-URL")
	if u == "" {
		return false
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	return parsed.Path == "/contas"
}

func respondContasRefreshAndCloseSheet(w http.ResponseWriter) {
	w.Header().Set("HX-Reswap", "none")
	w.Header().Set("HX-Trigger", "refreshFinancials")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var buf bytes.Buffer
	fmt.Fprint(&buf, `<div id="bottom-sheet-container" hx-swap-oob="true"></div>`)
	fmt.Fprint(&buf, `<div id="lancamento-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteTo(w)
}

func (h *TransactionHandler) respondRowAndCloseSheet(w http.ResponseWriter, r *http.Request, id string) {
	row, err := h.fetchTransactionRow(id)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "lancamento-row", row); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	fmt.Fprint(&buf, `<div id="bottom-sheet-container" hx-swap-oob="true"></div>`)
	fmt.Fprint(&buf, `<div id="lancamento-form-error" hx-swap-oob="true" class="hidden"></div>`)

	_ = h.renderLancamentosResumoOOB(&buf, r)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *TransactionHandler) respondBroadEditAndCloseSheet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("HX-Reswap", "none")
	w.Header().Set("HX-Trigger", "refreshLancamentosList")
	var buf bytes.Buffer
	fmt.Fprint(&buf, `<div id="bottom-sheet-container" hx-swap-oob="true"></div>`)
	fmt.Fprint(&buf, `<div id="lancamento-form-error" hx-swap-oob="true" class="hidden"></div>`)
	_ = h.renderLancamentosResumoOOB(&buf, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *TransactionHandler) HandleTogglePagamento(w http.ResponseWriter, r *http.Request, id string) {
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var trType, paymentStatus string
	var amount int64
	var accountID string
	var destAccountID sql.NullString
	var txDate int64
	var accType string

	err = tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, t.date, a.type
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&trType, &amount, &accountID, &destAccountID, &paymentStatus, &txDate, &accType)
	if err != nil {
		http.Error(w, "transação não encontrada", http.StatusNotFound)
		return
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{id}); err != nil {
		if errors.Is(err, errPaidInvoiceMutationBlocked) {
			http.Error(w, "não é possível alterar lançamento de fatura paga", http.StatusConflict)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	newStatus := "paid"
	if paymentStatus == "paid" {
		newStatus = "pending"
	}
	// Cartão de crédito: nunca permite voltar para "pending".
	if accType == "CREDIT_CARD" {
		newStatus = "paid"
	}

	now := time.Now().Unix()
	dest := ""
	if destAccountID.Valid {
		dest = destAccountID.String
	}

	if paymentStatus == "paid" {
		if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, trType, accType, amount, accountID, dest, now); err != nil {
			http.Error(w, "erro ao estornar", http.StatusInternalServerError)
			return
		}
	}

	var invoiceID interface{}
	if trType == "EXPENSE" && accType == "CREDIT_CARD" {
		invID, _, _, _, _, err := ensureOpenInvoiceTx(tx, h.WorkspaceID, accountID, txDate)
		if err != nil {
			http.Error(w, "erro na fatura", http.StatusInternalServerError)
			return
		}
		invoiceID = invID
	}

	err = execOneTx(tx, `
		UPDATE transactions SET status = ?, invoice_id = ?, updated_at = ? WHERE id = ? AND workspace_id = ?
	`, newStatus, invoiceID, now, id, h.WorkspaceID)
	if err != nil {
		http.Error(w, "erro ao atualizar status", http.StatusInternalServerError)
		return
	}

	if newStatus == "paid" {
		if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, trType, accType, newStatus, amount, accountID, dest, now); err != nil {
			http.Error(w, "erro ao aplicar saldo", http.StatusInternalServerError)
			return
		}
	}

	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, []string{id}); err != nil {
		http.Error(w, "erro ao reconcilizar fatura", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	row, err := h.fetchTransactionRow(id)
	if err != nil {
		log.Printf("fetch row after toggle error: %v", err)
		http.Error(w, "erro ao carregar linha", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "lancamento-row", row); err != nil {
		log.Printf("template row after toggle error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", "refreshFinancials")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	_ = h.renderLancamentosResumoOOB(&buf, r)

	buf.WriteTo(w)
}

func (h *TransactionHandler) HandleBulkDelete(w http.ResponseWriter, r *http.Request) {
	ids, err := parseBulkIDsFromRequest(r)
	if err != nil || len(ids) == 0 || len(ids) > 100 {
		http.Error(w, "1 a 100 IDs obrigatórios", http.StatusBadRequest)
		return
	}

	tx, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	validCount, err := h.countWorkspaceTransactionsByIDsTx(tx, ids)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if validCount < len(ids) {
		_ = h.logSecurityTamperingTx(tx, r, "bulk_delete_workspace_scope_violation", ids)
		_ = tx.Rollback()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	now := time.Now().Unix()
	for _, id := range ids {
		if err := h.reverseActiveConsumesForTransactionTx(tx, id, now); err != nil {
			http.Error(w, "erro ao ajustar reserva em lote", http.StatusInternalServerError)
			return
		}
	}

	if err := h.deleteTransactionsByIDsTx(tx, ids, now); err != nil {
		if errors.Is(err, errPaidInvoiceMutationBlocked) {
			http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
			return
		}
		http.Error(w, "erro ao remover em lote", http.StatusInternalServerError)
		return
	}
	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, ids); err != nil {
		http.Error(w, "erro ao reconcilizar fatura", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	committed = true
	if err := h.renderLancamentosTableBodyFromRequest(w, r); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) HandleBulkUpdate(w http.ResponseWriter, r *http.Request) {
	var (
		ids       []string
		newStatus string
		err       error
	)
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType == "application/json" {
		var payload struct {
			IDs             []string `json:"ids"`
			StatusPagamento string   `json:"status_pagamento"`
			Status          string   `json:"status"`
		}
		if err = json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "payload inválido", http.StatusBadRequest)
			return
		}
		ids = payload.IDs
		if payload.StatusPagamento != "" {
			newStatus = payload.StatusPagamento
		} else {
			newStatus = payload.Status
		}
	} else {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "payload inválido", http.StatusBadRequest)
			return
		}
		ids = append(ids, r.Form["ids"]...)
		ids = append(ids, r.Form["ids[]"]...)
		newStatus = strings.TrimSpace(r.FormValue("status_pagamento"))
	}
	ids = dedupeIDs(ids)
	if err != nil || len(ids) == 0 || len(ids) > 100 {
		http.Error(w, "1 a 100 IDs obrigatórios", http.StatusBadRequest)
		return
	}
	if newStatus != "pending" {
		newStatus = "paid"
	}

	tx, err := h.DB.BeginTx(context.Background(), nil)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	validCount, err := h.countWorkspaceTransactionsByIDsTx(tx, ids)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if validCount < len(ids) {
		_ = h.logSecurityTamperingTx(tx, r, "bulk_update_workspace_scope_violation", ids)
		_ = tx.Rollback()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
		if errors.Is(err, errPaidInvoiceMutationBlocked) {
			http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	type bulkRow struct {
		id            string
		trType        string
		amount        int64
		accountID     string
		destinationID sql.NullString
		status        string
		accountType   string
		dateUnix      int64
	}
	query := `
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id, t.status, a.type, t.date
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.id IN (` + sqlPlaceholders(len(ids)) + `)
	`
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, h.WorkspaceID)
	args = appendStrings(args, ids)
	rows, err := tx.Query(query, args...)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	byID := make(map[string]bulkRow, len(ids))
	for rows.Next() {
		var item bulkRow
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.status, &item.accountType, &item.dateUnix); err != nil {
			rows.Close()
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		byID[item.id] = item
	}
	if err := rows.Close(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	now := time.Now().Unix()
	for _, id := range ids {
		item, ok := byID[id]
		if !ok {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		dest := ""
		if item.destinationID.Valid {
			dest = item.destinationID.String
		}
		if item.status == newStatus {
			continue
		}
		// Cartão de crédito: nunca permite voltar para "pending" em bulk.
		if newStatus == "pending" && item.accountType == "CREDIT_CARD" {
			continue
		}
		if item.status == "paid" {
			if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, item.trType, item.accountType, item.amount, item.accountID, dest, now); err != nil {
				http.Error(w, "erro ao ajustar saldo", http.StatusInternalServerError)
				return
			}
		}

		var invoiceID interface{}
		if item.trType == "EXPENSE" && item.accountType == "CREDIT_CARD" {
			invID, _, _, _, _, err := ensureOpenInvoiceTx(tx, h.WorkspaceID, item.accountID, item.dateUnix)
			if err != nil {
				http.Error(w, "erro na fatura", http.StatusInternalServerError)
				return
			}
			invoiceID = invID
		}
		if err := execOneTx(tx, `
			UPDATE transactions
			SET status = ?, invoice_id = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, newStatus, invoiceID, now, item.id, h.WorkspaceID); err != nil {
			http.Error(w, "erro ao atualizar em lote", http.StatusInternalServerError)
			return
		}
		if newStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, item.trType, item.accountType, newStatus, item.amount, item.accountID, dest, now); err != nil {
				http.Error(w, "erro ao ajustar saldo", http.StatusInternalServerError)
				return
			}
		}
	}

	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, ids); err != nil {
		http.Error(w, "erro ao reconcilizar fatura", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	committed = true
	if err := h.renderLancamentosTableBodyFromRequest(w, r); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func parseBulkIDsFromRequest(r *http.Request) ([]string, error) {
	var ids []string
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if contentType == "application/json" {
		var payload struct {
			IDs []string `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		ids = payload.IDs
	} else {
		if err := r.ParseForm(); err != nil {
			return nil, err
		}
		ids = append(ids, r.Form["ids"]...)
		ids = append(ids, r.Form["ids[]"]...)
	}
	return dedupeIDs(ids), nil
}

func dedupeIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	return filtered
}

func (h *TransactionHandler) ensureTransactionsMutableByInvoiceStatusTx(tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	query := `
		SELECT COUNT(1)
		FROM transactions t
		WHERE t.workspace_id = ?
		  AND t.id IN (` + sqlPlaceholders(len(ids)) + `)
	`
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, h.WorkspaceID)
	args = appendStrings(args, ids)

	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return err
	}
	// PAID invoices no longer block mutation: corrections must be allowed so
	// users can move/edit/delete launches in paid or closed invoices. Only
	// workspace isolation remains enforced (verified above by workspace_id).
	return nil
}

func (h *TransactionHandler) countWorkspaceTransactionsByIDsTx(tx *sql.Tx, ids []string) (int, error) {
	query := `
		SELECT COUNT(1)
		FROM transactions
		WHERE workspace_id = ?
		  AND id IN (` + sqlPlaceholders(len(ids)) + `)
	`
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, h.WorkspaceID)
	args = appendStrings(args, ids)
	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (h *TransactionHandler) logSecurityTamperingTx(tx *sql.Tx, r *http.Request, reason string, ids []string) error {
	metadata := map[string]any{
		"path":   r.URL.Path,
		"method": r.Method,
		"status": "blocked",
		"reason": reason,
		"ids":    ids,
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	ip := strings.TrimSpace(r.RemoteAddr)
	if host, _, splitErr := net.SplitHostPort(ip); splitErr == nil {
		ip = host
	}
	_, err = tx.Exec(`
		INSERT INTO security_logs (
			id, workspace_id, user_id, event_type, severity, ip_address, user_agent, metadata, created_at
		) VALUES (?, NULLIF(?, ''), NULLIF(?, ''), 'security.tampering', 'CRITICAL', ?, ?, ?, unixepoch())
	`, uuid.NewString(), h.WorkspaceID, h.UserID, ip, strings.TrimSpace(r.UserAgent()), string(raw))
	return err
}

func (h *TransactionHandler) fetchTransactionRow(id string) (TransactionRow, error) {
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	rows, err := h.DB.Query(`
		SELECT t.id, t.type, t.amount, COALESCE(t.due_date, t.date), t.created_at, t.description, t.status,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(NULLIF(t.recurrence_sequence, 0), t.installment_number, 0),
			COALESCE(rr.total_occurrences, t.total_installments, 0),
			a.name AS account_name,
			COALESCE(ct.name, '') AS contact_name,
			u.name AS user_name,
			CASE WHEN t.due_date IS NOT NULL THEN 1 ELSE 0 END AS has_due_date,
			CASE WHEN (t.recurring_rule_id IS NOT NULL OR t.total_installments > 1) THEN 1 ELSE 0 END AS is_series
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		JOIN users u ON u.id = t.user_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID)
	if err != nil {
		return TransactionRow{}, err
	}
	defer rows.Close()

	list := scanLancamentosRowsWithWorkspace(rows, isBusiness)
	if len(list) == 0 {
		return TransactionRow{}, fmt.Errorf("not found")
	}
	return list[0], nil
}

func (h *TransactionHandler) buildLancamentosData(accountFilter string, mes, ano int, filters LancamentosFilters) (LancamentosData, error) {
	if err := autoCloseWorkspaceInvoices(h.DB, h.WorkspaceID); err != nil {
		return LancamentosData{}, fmt.Errorf("auto close invoices: %w", err)
	}

	selectedMonth := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)
	prevMonth := selectedMonth.AddDate(0, -1, 0)
	nextMonthDate := selectedMonth.AddDate(0, 1, 0)

	now := time.Now().UTC()
	sortOrder := normalizeSortOrder(filters.Order, "asc")
	data := LancamentosData{
		Title:                     "Lançamentos",
		FilterAccountID:           accountFilter,
		MesAtual:                  mes,
		AnoAtual:                  ano,
		MonthLabel:                monthLabel(mes, ano),
		MonthSelectorHXGet:        "/lancamentos",
		MonthSelectorHXTarget:     "#lancamentos-list-wrapper",
		MonthSelectorHXSelect:     "#lancamentos-list-wrapper",
		MonthSelectorHXSwap:       "outerHTML",
		MonthSelectorPartial:      "lista",
		MonthSelectorPrevQuery:    lancamentosMonthQueryWithFilters(accountFilter, int(prevMonth.Month()), prevMonth.Year(), filters),
		MonthSelectorNextQuery:    lancamentosMonthQueryWithFilters(accountFilter, int(nextMonthDate.Month()), nextMonthDate.Year(), filters),
		MonthSelectorCurrentQuery: lancamentosMonthQueryWithFilters(accountFilter, int(now.Month()), now.Year(), filters),
		MesAnteriorURL:            "/lancamentos?" + lancamentosMonthQueryWithFilters(accountFilter, int(prevMonth.Month()), prevMonth.Year(), filters),
		MesSeguinteURL:            "/lancamentos?" + lancamentosMonthQueryWithFilters(accountFilter, int(nextMonthDate.Month()), nextMonthDate.Year(), filters),
		CurrentMonthURL:           "/lancamentos?" + lancamentosMonthQueryWithFilters(accountFilter, int(now.Month()), now.Year(), filters),
		MonthOptions:              buildMonthOptions(accountFilter, selectedMonth, filters),
		Filters:                   filters,
		SortOrder:                 sortOrder,
	}
	_, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
	data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == "business"
	if data.IsBusiness {
		data.Title = "Fluxo de Caixa"
	}
	statuses := filters.normalizedStatuses()
	data.StatusPendenteOn = containsString(statuses, "pendente")
	data.StatusVencidoOn = containsString(statuses, "vencido")
	data.StatusPagoOn = containsString(statuses, "pago")
	data.HasActiveFilters = filters.hasActiveSelections(accountFilter)
	data.FilterAccounts, _ = h.queryFilterAccounts()
	data.FilterCategories, _ = h.queryFormCategories()
	data.ClearFiltersURL = fmt.Sprintf("/lancamentos?mes=%d&ano=%d", mes, ano)
	if sortOrder == "desc" {
		data.ClearFiltersURL += "&ordem=desc"
	}

	var filterLabels []FilterLabelItem
	if accountFilter != "" {
		var name string
		h.DB.QueryRow(`SELECT name FROM accounts WHERE id = ? AND workspace_id = ?`, accountFilter, h.WorkspaceID).Scan(&name)
		if name != "" {
			filterLabels = append(filterLabels, FilterLabelItem{Key: "conta", Label: name})
			data.FilterLabel = name
		}
	}
	for _, catID := range filters.Categorias {
		for _, cat := range data.FilterCategories {
			if cat.ID == catID {
				filterLabels = append(filterLabels, FilterLabelItem{Key: "categoria", Label: cat.Name})
				break
			}
		}
	}
	for _, accID := range filters.OrigemIDs {
		for _, acc := range data.FilterAccounts {
			if acc.ID == accID {
				filterLabels = append(filterLabels, FilterLabelItem{Key: "origem", Label: acc.Name})
				break
			}
		}
	}
	for _, tipo := range filters.normalizedTypes() {
		label := tipo
		switch tipo {
		case "receita":
			label = "Receitas"
		case "despesa":
			label = "Despesas"
		case "transferencia":
			label = "Transferências"
		case "fixo":
			label = "Fixos"
		case "parcelado":
			label = "Parcelados"
		}
		filterLabels = append(filterLabels, FilterLabelItem{Key: "tipo", Label: label})
	}
	statuses = filters.normalizedStatuses()
	defaultStatus := len(statuses) == 2 && containsString(statuses, "pendente") && containsString(statuses, "vencido")
	if !defaultStatus {
		for _, s := range statuses {
			label := s
			switch s {
			case "pendente":
				label = "Pendentes"
			case "vencido":
				label = "Vencidos"
			case "pago":
				label = "Pagos"
			}
			filterLabels = append(filterLabels, FilterLabelItem{Key: "situacao", Label: label})
		}
	}
	if filters.Busca != "" {
		filterLabels = append(filterLabels, FilterLabelItem{Key: "busca", Label: filters.Busca})
	}
	data.FilterLabels = filterLabels

	loc := time.UTC
	monthStart := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, loc).Unix()
	nextMonth := time.Date(ano, time.Month(mes)+1, 1, 0, 0, 0, 0, loc)
	monthEnd := nextMonth.Add(-1 * time.Second).Unix()

	var incomeTotal, expenseTotal int64
	summaryRows, err := h.DB.Query(`
		SELECT t.type, COALESCE(SUM(t.amount), 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.date >= ?
		  AND t.date <= ?
		  AND t.type IN ('INCOME', 'EXPENSE')
		  AND a.type != 'CREDIT_CARD'
		GROUP BY t.type
	`, h.WorkspaceID, monthStart, monthEnd)
	if err != nil {
		return data, fmt.Errorf("query monthly summary: %w", err)
	}
	defer summaryRows.Close()
	for summaryRows.Next() {
		var txType string
		var total int64
		if err := summaryRows.Scan(&txType, &total); err != nil {
			return data, fmt.Errorf("scan monthly summary: %w", err)
		}
		switch txType {
		case "INCOME":
			incomeTotal += total
		case "EXPENSE":
			expenseTotal += total
		}
	}
	if err := summaryRows.Err(); err != nil {
		return data, fmt.Errorf("monthly summary rows: %w", err)
	}

	data.ResumoEntradas = formatCurrencyLabel(incomeTotal)
	data.ResumoSaidas = formatCurrencyLabel(expenseTotal)
	saldo := incomeTotal - expenseTotal
	data.ResumoSaldo = formatCurrencyLabel(saldo)
	if saldo < 0 {
		data.ResumoNegativo = true
	}

	var totalBalance int64
	h.DB.QueryRow(`SELECT COALESCE(SUM(current_balance), 0) FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, h.WorkspaceID).Scan(&totalBalance)
	acumulado := h.projectedAccumulatedBalance(totalBalance, selectedMonth)
	data.ResumoAcumulado = formatCurrencyLabel(acumulado)
	if acumulado < 0 {
		data.AcumuladoNegativo = true
	}

	filterAccountType := ""
	if accountFilter != "" {
		_ = h.DB.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, accountFilter, h.WorkspaceID).Scan(&filterAccountType)
	}

	query := `
		SELECT t.id, t.type, t.amount, COALESCE(t.due_date, t.date), t.created_at, t.description, t.status,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(NULLIF(t.recurrence_sequence, 0), t.installment_number, 0),
			COALESCE(rr.total_occurrences, t.total_installments, 0),
			a.name AS account_name,
			COALESCE(ct.name, '') AS contact_name,
			u.name AS user_name,
			CASE WHEN t.due_date IS NOT NULL THEN 1 ELSE 0 END AS has_due_date,
			CASE WHEN (t.recurring_rule_id IS NOT NULL OR t.total_installments > 1) THEN 1 ELSE 0 END AS is_series
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		JOIN users u ON u.id = t.user_id
	`
	where, args := h.lancamentosWhereClause(monthStart, monthEnd, accountFilter, filters)
	query += where
	query += ` AND a.type != 'CREDIT_CARD' ORDER BY COALESCE(t.due_date, t.date) ASC, t.created_at ASC`

	rows, err := h.DB.Query(query, args...)
	if err != nil {
		return data, fmt.Errorf("query transactions: %w", err)
	}
	defer rows.Close()

	data.Transactions = scanLancamentosRowsWithWorkspace(rows, data.IsBusiness)
	if err := rows.Err(); err != nil {
		return data, fmt.Errorf("transaction rows: %w", err)
	}
	sortTransactionRows(data.Transactions, sortOrder)

	// Credit card expenses are listed as consolidated invoices instead of raw transactions.
	// Filtramos por mês de VENCIMENTO (due_date), não por mês do ciclo (reference),
	// para que a fatura apareça no mês em que o usuário precisa pagá-la.
	if accountFilter == "" || filterAccountType == models.AccountTypeCreditCard {
		invoiceRows, invoiceErr := h.DB.Query(`
			SELECT
				i.id,
				a.id as account_id,
				a.name as account_name,
				COALESCE(NULLIF(a.provider_slug, ''), 'custom') as provider_slug,
				COALESCE(NULLIF(a.color, ''), '#6B7280') as color,
				i.reference,
				i.closing_date,
				i.status,
				i.due_date,
				COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END), 0) as total,
				COUNT(t.id) as item_count
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id AND a.workspace_id = ?
			LEFT JOIN transactions t ON t.invoice_id = i.id AND t.workspace_id = ?
			WHERE a.workspace_id = ?
				AND a.type = 'CREDIT_CARD'
				AND i.due_date >= ?
				AND i.due_date <= ?
				AND (? = '' OR a.id = ?)
		GROUP BY i.id
					ORDER BY i.due_date ASC, a.name ASC
		`, h.WorkspaceID, h.WorkspaceID, h.WorkspaceID, monthStart, monthEnd, accountFilter, accountFilter)
		if invoiceErr == nil {
			defer invoiceRows.Close()
			nowUnix := now.Unix()
			for invoiceRows.Next() {
				var inv InvoiceRow
				var providerSlug, color string
				var closingUnix int64
				if err := invoiceRows.Scan(&inv.ID, &inv.AccountID, &inv.AccountName, &providerSlug, &color, &inv.Reference, &closingUnix, &inv.Status, &inv.DueDate, &inv.Total, &inv.ItemCount); err != nil {
					continue
				}
				normalizedSlug := normalizeAccountProviderSlug(providerSlug)
				inv.CardIcon = accountVisualByProvider(normalizedSlug, models.AccountTypeCreditCard)
				inv.CardColor = normalizeHexColor(color, "#6B7280")
				inv.CardProviderMark = accountProviderMark(providerSlug, inv.AccountName)
				inv.TotalMoney = MoneyMinor(inv.Total)
				// IsOverdue: fatura não paga cujo due_date já passou (vencimento no passado)
				inv.IsOverdue = inv.Status != models.InvoiceStatusPaid && inv.DueDate > 0 && inv.DueDate < nowUnix
				inv.DateLabel = formatDateLabel(inv.DueDate)
				// ReferenceLabel: indica o mês do ciclo de fechamento (reference), não o vencimento
				if refYear, refMonth, refErr := parseInvoiceReference(inv.Reference); refErr == nil {
					months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
					inv.ReferenceLabel = fmt.Sprintf("%s/%d", months[refMonth-1], refYear)
				} else {
					inv.ReferenceLabel = inv.Reference
				}
				if refYear, refMonth, refErr := parseInvoiceReference(inv.Reference); refErr == nil {
					inv.FaturaURL = fmt.Sprintf("/cartoes/%s/faturas?mes=%d&ano=%d", inv.AccountID, refMonth, refYear)
				} else {
					inv.FaturaURL = fmt.Sprintf("/cartoes/%s/faturas?mes=%d&ano=%d", inv.AccountID, mes, ano)
				}
				data.Invoices = append(data.Invoices, inv)
			}

			// As faturas são imunes aos filtros de transações comuns

			// Include virtual future invoices if not present
			cardRows, cardErr := h.DB.Query(`
				SELECT a.id, a.name,
					COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
					COALESCE(NULLIF(a.color, ''), '#6B7280'),
					COALESCE(cc.closing_day, 0), COALESCE(cc.due_day, 0)
				FROM accounts a
				LEFT JOIN credit_cards cc ON cc.account_id = a.id
				WHERE a.workspace_id = ? AND a.type = 'CREDIT_CARD' AND a.archived_at IS NULL
			`, h.WorkspaceID)
			if cardErr == nil {
				type virtualInvoiceCardSeed struct {
					accountID    string
					accountName  string
					providerSlug string
					color        string
					closingDay   int64
					dueDay       int64
				}
				var cardSeeds []virtualInvoiceCardSeed
				for cardRows.Next() {
					var card virtualInvoiceCardSeed
					if err := cardRows.Scan(&card.accountID, &card.accountName, &card.providerSlug, &card.color, &card.closingDay, &card.dueDay); err != nil {
						continue
					}
					cardSeeds = append(cardSeeds, card)
				}
				cardRows.Close()

				for _, card := range cardSeeds {
					// Skip if we already have an invoice for this card
					found := false
					for _, inv := range data.Invoices {
						if inv.AccountID == card.accountID {
							found = true
							break
						}
					}
					if found {
						continue
					}

					// Find reference for the due_date
					targetDue := time.Date(ano, time.Month(mes), int(card.dueDay), 12, 0, 0, 0, time.UTC)
					refYear, refMonth := ano, mes
					if card.closingDay > card.dueDay {
						prev := targetDue.AddDate(0, -1, 0)
						refYear, refMonth = prev.Year(), int(prev.Month())
					}
					reference := fmt.Sprintf("%04d-%02d", refYear, refMonth)

					total, count := CalculateVirtualInvoiceTotal(h.DB, h.WorkspaceID, card.accountID, reference)
					if total > 0 {
						var inv InvoiceRow
						inv.ID = "virtual-" + card.accountID + "-" + reference
						inv.AccountID = card.accountID
						inv.AccountName = card.accountName
						normalizedSlug := normalizeAccountProviderSlug(card.providerSlug)
						inv.CardIcon = accountVisualByProvider(normalizedSlug, models.AccountTypeCreditCard)
						inv.CardColor = normalizeHexColor(card.color, "#6B7280")
						inv.CardProviderMark = accountProviderMark(card.providerSlug, inv.AccountName)
						inv.Reference = reference
						inv.DueDate = targetDue.Unix()
						inv.Total = total
						inv.ItemCount = count
						inv.TotalMoney = MoneyMinor(inv.Total)
						inv.IsOverdue = false
						inv.Status = models.InvoiceStatusOpen
						inv.DateLabel = formatDateLabel(inv.DueDate)

						months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
						inv.ReferenceLabel = fmt.Sprintf("%s/%d", months[refMonth-1], refYear)
						inv.FaturaURL = fmt.Sprintf("/cartoes/%s/faturas?mes=%d&ano=%d", inv.AccountID, refMonth, refYear)
						data.Invoices = append(data.Invoices, inv)
					}
				}
				cardRows.Close()
			}

			// Include non-paid invoices in expense summary
			for _, inv := range data.Invoices {
				if inv.Status != models.InvoiceStatusPaid {
					expenseTotal += inv.Total
				}
			}
			data.ResumoSaidas = formatCurrencyLabel(expenseTotal)
			saldo := incomeTotal - expenseTotal
			data.ResumoSaldo = formatCurrencyLabel(saldo)
			data.ResumoNegativo = saldo < 0
		}
	}

	// CONTRACT: docs/contracts/INVOICE_RENDER_CONTRACT.md
	// Build UnifiedItems: merge transactions and invoices sorted by date desc
	for _, tx := range data.Transactions {
		data.UnifiedItems = append(data.UnifiedItems, UnifiedItem{
			IsInvoice:   false,
			DateUnix:    tx.Date,
			DateLabel:   tx.DateLabel,
			Transaction: tx,
		})
	}
	for _, inv := range data.Invoices {
		if inv.Total > 0 {
			data.UnifiedItems = append(data.UnifiedItems, UnifiedItem{
				IsInvoice: true,
				DateUnix:  inv.DueDate,
				DateLabel: inv.DateLabel,
				Invoice:   inv,
			})
		}
	}

	sortUnifiedItems(data.UnifiedItems)

	return data, nil
}

func (h *TransactionHandler) lancamentosWhereClause(monthStart, monthEnd int64, accountFilter string, filters LancamentosFilters) (string, []interface{}) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	clauses := []string{`WHERE t.workspace_id = ?`, `COALESCE(t.due_date, t.date) >= ?`, `COALESCE(t.due_date, t.date) <= ?`}
	args := []interface{}{h.WorkspaceID, monthStart, monthEnd}
	if accountFilter != "" {
		clauses = append(clauses, `t.account_id = ?`)
		args = append(args, accountFilter)
	}
	if len(filters.normalizedTypes()) > 0 {
		var typeClauses []string
		for _, txType := range filters.normalizedTypes() {
			switch txType {
			case "receita":
				typeClauses = append(typeClauses, `t.type IN ('INCOME')`)
			case "despesa":
				typeClauses = append(typeClauses, `t.type = 'EXPENSE'`)
			case "transferencia":
				typeClauses = append(typeClauses, `t.type = 'TRANSFER'`)
			case "fixo":
				typeClauses = append(typeClauses, `t.recurring_rule_id IS NOT NULL`)
			case "parcelado":
				typeClauses = append(typeClauses, `t.total_installments > 1`)
			}
		}
		if len(typeClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(typeClauses, " OR ")+")")
		}
	}
	statuses := filters.normalizedStatuses()
	if len(statuses) > 0 {
		var statusClauses []string
		for _, status := range statuses {
			switch status {
			case "pendente":
				statusClauses = append(statusClauses, `(t.status = 'pending' AND COALESCE(t.due_date, t.date) >= ?)`)
				args = append(args, today)
			case "vencido":
				statusClauses = append(statusClauses, `(t.status = 'pending' AND COALESCE(t.due_date, t.date) < ?)`)
				args = append(args, today)
			case "pago":
				statusClauses = append(statusClauses, `(t.status = 'paid')`)
			}
		}
		if len(statusClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(statusClauses, " OR ")+")")
		}
	}
	if len(filters.OrigemIDs) > 0 {
		clauses = append(clauses, `t.account_id IN (`+sqlPlaceholders(len(filters.OrigemIDs))+`)`)
		args = appendStrings(args, filters.OrigemIDs)
	}
	if len(filters.DestinoIDs) > 0 {
		clauses = append(clauses, `t.destination_account_id IN (`+sqlPlaceholders(len(filters.DestinoIDs))+`)`)
		args = appendStrings(args, filters.DestinoIDs)
	}
	if len(filters.Categorias) > 0 {
		clauses = append(clauses, `t.category_id IN (`+sqlPlaceholders(len(filters.Categorias))+`)`)
		args = appendStrings(args, filters.Categorias)
	}
	if filters.Busca != "" {
		like := "%" + filters.Busca + "%"
		searchClauses := []string{
			`UNACCENT(COALESCE(t.description, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(t.notes, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(ct.name, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(ct.custom_client_id, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(ct.document, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(ct.phone, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(ct.email, '')) LIKE UNACCENT(?)`,
		}
		args = append(args, like, like, like, like, like, like, like)
		if digits := onlyDigits(filters.Busca); digits != "" {
			digitsLike := "%" + digits + "%"
			searchClauses = append(searchClauses,
				sqlStripPunctuation("ct.document")+` LIKE ?`,
				sqlStripPunctuation("ct.phone")+` LIKE ?`,
			)
			args = append(args, digitsLike, digitsLike)
		}
		clauses = append(clauses, "("+strings.Join(searchClauses, " OR ")+")")
	}
	return strings.Join(clauses, " AND "), args
}

func lancamentosFiltersFromRequest(r *http.Request) LancamentosFilters {
	return lancamentosFiltersFromValues(r.URL.Query())
}

func lancamentosFiltersFromValues(values url.Values) LancamentosFilters {
	return LancamentosFilters{
		Tipos:      compactFilterValues(values["tipo"]),
		Situacoes:  compactFilterValues(values["situacao"]),
		OrigemIDs:  compactFilterValues(values["origem"]),
		DestinoIDs: compactFilterValues(values["destino"]),
		Categorias: compactFilterValues(values["categoria"]),
		Busca:      strings.TrimSpace(values.Get("q")),
		Order:      normalizeSortOrder(values.Get("ordem"), "asc"),
	}
}

func compactFilterValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (f LancamentosFilters) normalizedTypes() []string {
	allowed := map[string]struct{}{
		"receita":       {},
		"despesa":       {},
		"transferencia": {},
		"fixo":          {},
		"parcelado":     {},
	}
	var out []string
	for _, value := range f.Tipos {
		if _, ok := allowed[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

func (f LancamentosFilters) normalizedStatuses() []string {
	if len(f.Situacoes) == 0 {
		return []string{"pendente", "vencido"}
	}
	seen := make(map[string]struct{}, len(f.Situacoes))
	var statuses []string
	for _, raw := range f.Situacoes {
		switch raw {
		case "aberto":
			raw = "pendente"
		}
		if raw != "pendente" && raw != "vencido" && raw != "pago" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		statuses = append(statuses, raw)
	}
	return statuses
}

func (f LancamentosFilters) hasActiveSelections(accountFilter string) bool {
	statuses := f.normalizedStatuses()
	defaultStatus := len(statuses) == 2 && containsString(statuses, "pendente") && containsString(statuses, "vencido")
	return accountFilter != "" || len(f.normalizedTypes()) > 0 || len(f.OrigemIDs) > 0 || len(f.DestinoIDs) > 0 || len(f.Categorias) > 0 || f.Busca != "" || !defaultStatus
}

func normalizeSortOrder(raw string, defaultOrder string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "asc":
		return "asc"
	case "desc":
		return "desc"
	default:
		return defaultOrder
	}
}

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func appendStrings(args []interface{}, values []string) []interface{} {
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

func sqlStripPunctuation(column string) string {
	expr := "COALESCE(" + column + ", '')"
	for _, ch := range []string{".", "/", "-", "(", ")", " ", "+"} {
		expr = "REPLACE(" + expr + ", '" + ch + "', '')"
	}
	return expr
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func (h *TransactionHandler) projectedAccumulatedBalance(currentBalance int64, selectedMonth time.Time) int64 {
	return calcProjectedAccumulatedBalance(h.DB, h.WorkspaceID, currentBalance, selectedMonth)
}

func (h *TransactionHandler) projectedTransactionNetCashFlowBetween(today, tomorrow, end time.Time) int64 {
	return calcProjectedTransactionNetCashFlowBetween(h.DB, h.WorkspaceID, today, tomorrow, end)
}

func (h *TransactionHandler) projectedRecurringNetCashFlowBetween(start, end time.Time) int64 {
	return calcProjectedRecurringNetCashFlowBetween(h.DB, h.WorkspaceID, start, end)
}

func projectedRecurringRuleNet(trType string, amount int64, startDate int64, frequency string, rangeStart, rangeEnd time.Time) int64 {
	if trType == "TRANSFER" {
		return 0
	}

	occurrence := time.Unix(startDate, 0).UTC()
	rangeStart = time.Date(rangeStart.Year(), rangeStart.Month(), rangeStart.Day(), 0, 0, 0, 0, time.UTC)
	rangeEnd = time.Date(rangeEnd.Year(), rangeEnd.Month(), rangeEnd.Day(), 0, 0, 0, 0, time.UTC)

	var total int64
	for i := 0; i < 40 && occurrence.Before(rangeEnd); i++ {
		prevDate := occurrence.Unix()
		if occurrence.After(time.Unix(startDate, 0).UTC()) && !occurrence.Before(rangeStart) {
			if trType == "INCOME" {
				total += amount
			} else if trType == "EXPENSE" {
				total -= amount
			}
		}
		occurrence = nextRecurrenceDate(occurrence, frequency)
		if occurrence.Unix() <= prevDate {
			break
		}
	}
	return total
}

func nextRecurrenceDate(t time.Time, frequency string) time.Time {
	switch frequency {
	case "DAILY":
		return t.AddDate(0, 0, 1)
	case "WEEKLY":
		return t.AddDate(0, 0, 7)
	case "BIWEEKLY":
		return t.AddDate(0, 0, 14)
	case "MONTHLY":
		return time.Unix(safeAddMonths(t.Unix(), 1), 0).UTC()
	case "BIMONTHLY":
		return time.Unix(safeAddMonths(t.Unix(), 2), 0).UTC()
	case "QUARTERLY":
		return time.Unix(safeAddMonths(t.Unix(), 3), 0).UTC()
	case "SEMIANNUAL":
		return time.Unix(safeAddMonths(t.Unix(), 6), 0).UTC()
	case "ANNUAL":
		return time.Unix(safeAddMonths(t.Unix(), 12), 0).UTC()
	default:
		return time.Unix(safeAddMonths(t.Unix(), 1), 0).UTC()
	}
}

func (h *TransactionHandler) paidNetCashFlowBetween(start, end time.Time) int64 {
	return calcPaidNetCashFlowBetween(h.DB, h.WorkspaceID, start, end)
}

// Package-level accumulated balance helpers (shared by /lancamentos and /relatorios).

func calcProjectedAccumulatedBalance(db *sql.DB, workspaceID string, currentBalance int64, selectedMonth time.Time) int64 {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	selectedStart := time.Date(selectedMonth.Year(), selectedMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	selectedEnd := selectedStart.AddDate(0, 1, 0)

	if !selectedStart.Before(currentMonth) {
		return currentBalance +
			calcProjectedTransactionNetCashFlowBetween(db, workspaceID, today, tomorrow, selectedEnd) +
			calcProjectedRecurringNetCashFlowBetween(db, workspaceID, today, selectedEnd)
	}
	return currentBalance - calcPaidNetCashFlowBetween(db, workspaceID, selectedEnd, today)
}

func calcProjectedTransactionNetCashFlowBetween(db *sql.DB, workspaceID string, today, tomorrow, end time.Time) int64 {
	var incomeTotal, expenseTotal int64
	db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM transactions
		WHERE workspace_id = ?
		  AND date >= ?
		  AND date < ?
		  AND (
		  	status = 'pending'
		  	OR date >= ?
		  )
	`, workspaceID, today.Unix(), end.Unix(), tomorrow.Unix()).Scan(&incomeTotal, &expenseTotal)
	return incomeTotal - expenseTotal
}

func calcProjectedRecurringNetCashFlowBetween(db *sql.DB, workspaceID string, start, end time.Time) int64 {
	rows, err := db.Query(`
		SELECT id, type, amount, start_date, frequency
		FROM recurring_rules
		WHERE workspace_id = ? AND active = 1 AND start_date < ?
	`, workspaceID, end.Unix())
	if err != nil {
		return 0
	}
	defer rows.Close()

	type recurringRuleProjection struct {
		id        string
		trType    string
		amount    int64
		startDate int64
		frequency string
	}
	var rules []recurringRuleProjection
	for rows.Next() {
		var item recurringRuleProjection
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.startDate, &item.frequency); err != nil {
			continue
		}
		rules = append(rules, item)
	}
	if err := rows.Err(); err != nil {
		return 0
	}
	if err := rows.Close(); err != nil {
		return 0
	}

	var total int64
	for _, rule := range rules {
		var projectedCount int64
		if err := db.QueryRow(`
			SELECT COUNT(1)
			FROM transactions
			WHERE workspace_id = ?
			  AND recurring_rule_id = ?
			  AND date >= ?
			  AND date < ?
		`, workspaceID, rule.id, start.Unix(), end.Unix()).Scan(&projectedCount); err == nil && projectedCount > 0 {
			continue
		}
		total += projectedRecurringRuleNet(rule.trType, rule.amount, rule.startDate, rule.frequency, start, end)
	}
	return total
}

func calcPaidNetCashFlowBetween(db *sql.DB, workspaceID string, start, end time.Time) int64 {
	var incomeTotal, expenseTotal int64
	db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN type = 'EXPENSE' THEN amount ELSE 0 END), 0)
		FROM transactions
		WHERE workspace_id = ? AND status = 'paid' AND date >= ? AND date < ?
	`, workspaceID, start.Unix(), end.Unix()).Scan(&incomeTotal, &expenseTotal)
	return incomeTotal - expenseTotal
}

func buildMonthOptions(accountFilter string, selectedMonth time.Time, filters LancamentosFilters) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	now := time.Now().UTC()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	options := make([]MonthOption, 0, 12)
	for month := 1; month <= 12; month++ {
		m := time.Date(selectedMonth.Year(), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		query := lancamentosMonthQueryWithFilters(accountFilter, int(m.Month()), m.Year(), filters)
		options = append(options, MonthOption{
			Label:     shortMonths[int(m.Month())-1],
			Year:      fmt.Sprintf("%d", m.Year()),
			URL:       "/lancamentos?" + query,
			Query:     query,
			IsActive:  m.Month() == selectedMonth.Month(),
			IsCurrent: m.Equal(currentMonth),
		})
	}
	return options
}

func monthLabel(mes, ano int) string {
	months := []string{
		"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho",
		"Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro",
	}
	if mes < 1 || mes > 12 {
		return fmt.Sprintf("%02d/%d", mes, ano)
	}
	return fmt.Sprintf("%s %d", months[mes-1], ano)
}

func lancamentosURL(accountFilter string, mes, ano int, sortOrder string) string {
	return "/lancamentos?" + lancamentosMonthQuery(accountFilter, mes, ano, sortOrder)
}

func lancamentosMonthQuery(accountFilter string, mes, ano int, sortOrder string) string {
	values := url.Values{}
	values.Set("mes", strconv.Itoa(mes))
	values.Set("ano", strconv.Itoa(ano))
	if accountFilter != "" {
		values.Set("conta", accountFilter)
	}
	if normalizeSortOrder(sortOrder, "asc") == "desc" {
		values.Set("ordem", "desc")
	}
	return values.Encode()
}

func lancamentosMonthQueryWithFilters(accountFilter string, mes, ano int, filters LancamentosFilters) string {
	values := url.Values{}
	values.Set("mes", strconv.Itoa(mes))
	values.Set("ano", strconv.Itoa(ano))
	if accountFilter != "" {
		values.Set("conta", accountFilter)
	}
	for _, s := range filters.Situacoes {
		if s != "" {
			values.Add("situacao", s)
		}
	}
	for _, t := range filters.Tipos {
		if t != "" {
			values.Add("tipo", t)
		}
	}
	for _, o := range filters.OrigemIDs {
		if o != "" {
			values.Add("origem", o)
		}
	}
	for _, d := range filters.DestinoIDs {
		if d != "" {
			values.Add("destino", d)
		}
	}
	for _, c := range filters.Categorias {
		if c != "" {
			values.Add("categoria", c)
		}
	}
	if filters.Busca != "" {
		values.Set("q", filters.Busca)
	}
	if normalizeSortOrder(filters.Order, "asc") == "desc" {
		values.Set("ordem", "desc")
	}
	return values.Encode()
}

func scanTransactionRows(rows *sql.Rows) []TransactionRow {
	return scanTransactionRowsWithWorkspace(rows, false)
}

func sortTransactionRows(rows []TransactionRow, sortOrder string) {
	sortOrder = normalizeSortOrder(sortOrder, "asc")
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Date == rows[j].Date {
			if rows[i].CreatedAt == rows[j].CreatedAt {
				return rows[i].ID < rows[j].ID
			}
			if sortOrder == "desc" {
				return rows[i].CreatedAt > rows[j].CreatedAt
			}
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		if sortOrder == "desc" {
			return rows[i].Date > rows[j].Date
		}
		return rows[i].Date < rows[j].Date
	})
	for i := range rows {
		rows[i].ListIndex = i
	}
}

func scanLancamentosRowsWithWorkspace(rows *sql.Rows, isBusiness bool) []TransactionRow {
	var list []TransactionRow
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}

	for rows.Next() {
		var row TransactionRow
		var trType string
		var amount int64
		var dateUnix int64
		var catName, catIcon, catColor sql.NullString
		var hasDueDate int64
		var isSeries int64

		if err := rows.Scan(&row.ID, &trType, &amount, &dateUnix, &row.CreatedAt, &row.Description, &row.PaymentStatus,
			&catName, &catIcon, &catColor,
			&row.InstallmentNumber, &row.TotalInstallments,
			&row.AccountName, &row.ContactName, &row.Author, &hasDueDate, &isSeries); err != nil {
			continue
		}

		row.IsBusiness = isBusiness
		row.Type = trType
		row.Amount = amount
		row.Date = dateUnix
		row.CategoryName = catName.String
		if isBusiness && strings.EqualFold(row.CategoryName, "Salário") {
			row.CategoryName = "Faturamento/Receita"
		}
		row.CategoryIcon = normalizeLucideIcon(catIcon.String)
		row.CategoryColor = normalizeUIThemeColor(catColor.String)
		if row.CategoryIcon == "" {
			row.CategoryIcon = "tag"
		}

		row.IsPending = row.PaymentStatus == "pending"

		row.IsSeries = isSeries == 1
		row.HasInstallmentInfo = row.IsSeries || row.TotalInstallments > 1
		row.UsesDueDate = isBusiness && row.IsPending && hasDueDate == 1
		if row.UsesDueDate {
			row.DisplayDateLabel = formatDateLabelFromUnix(dateUnix)
		}

		t := time.Unix(dateUnix, 0).UTC()
		localTime := time.Unix(dateUnix, 0)
		row.TimeDisplay = localTime.Format("15:04")
		row.DateInput = localTime.Format("2006-01-02")
		dateOnly := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)

		row.IsOverdue = row.PaymentStatus != "paid" && dateOnly.Before(today)
		if row.IsOverdue {
			row.PaymentIcon = "alert-triangle"
			if row.Type == "INCOME" {
				row.PaymentTitle = "Atrasado — toque para marcar como recebido"
				row.PaymentConfirm = "Marcar este lançamento como recebido?"
				row.PaymentConfirmTitle = "Confirmar recebimento"
			} else {
				row.PaymentTitle = "Atrasado — toque para marcar como pago"
				row.PaymentConfirm = "Marcar este lançamento como pago?"
				row.PaymentConfirmTitle = "Confirmar pagamento"
			}
		} else if row.IsPending {
			row.PaymentIcon = "clock"
			if row.Type == "INCOME" {
				row.PaymentTitle = "Pendente — toque para marcar como recebido"
				row.PaymentConfirm = "Marcar este lançamento como recebido?"
				row.PaymentConfirmTitle = "Confirmar recebimento"
			} else {
				row.PaymentTitle = "Pendente — toque para marcar como pago"
				row.PaymentConfirm = "Marcar este lançamento como pago?"
				row.PaymentConfirmTitle = "Confirmar pagamento"
			}
		} else {
			row.PaymentIcon = "check-circle-2"
			if row.Type == "INCOME" {
				row.PaymentTitle = "Recebido — toque para remover recebimento"
				row.PaymentConfirm = "Remover o recebimento deste lançamento e voltar para pendente?"
				row.PaymentConfirmTitle = "Confirmar alteração de status"
			} else {
				row.PaymentTitle = "Pago — toque para remover pagamento"
				row.PaymentConfirm = "Remover o pagamento deste lançamento e voltar para pendente?"
				row.PaymentConfirmTitle = "Confirmar alteração de status"
			}
		}

		switch {
		case dateOnly.Equal(today):
			row.DateLabel = "Hoje"
		case dateOnly.Equal(yesterday):
			row.DateLabel = "Ontem"
		case dateOnly.Year() == today.Year():
			row.DateLabel = fmt.Sprintf("%d %s", t.Day(), months[t.Month()-1])
		default:
			row.DateLabel = fmt.Sprintf("%d %s %d", t.Day(), months[t.Month()-1], t.Year())
		}

		sign := "-"
		cls := "text-[#FE414F]"
		if trType == "INCOME" {
			sign = "+"
			cls = "text-[#009866]"
		}
		row.AmountDisplay = sign + " R$ " + formatCurrencyCentsBase(amount)
		row.AmountInput = formatCurrencyCentsBase(amount)
		row.AmountClass = cls

		row.CanGenerateReceipt = isBusiness

		list = append(list, row)
	}
	return list
}

func normalizeLucideIcon(icon string) string {
	if icon == "" {
		return ""
	}
	validIcons := map[string]bool{
		"tag": true, "wallet": true, "credit-card": true, "landmark": true,
		"building-2": true, "banknote": true, "wallet-cards": true,
		"circle-dollar-sign": true, "piggy-bank": true, "target": true,
		"repeat": true, "home": true, "car": true, "shopping-cart": true,
		"shopping-bag": true, "utensils": true, "coffee": true, "gift": true,
		"heart": true, "star": true, "trophy": true, "graduation-cap": true,
		"book-open": true, "briefcase": true, "plane": true, "train": true,
		"bus": true, "bike": true, "fuel": true, "wrench": true,
		"hammer": true, "lightbulb": true, "zap": true, "droplets": true,
		"flame": true, "umbrella": true, "sun": true, "moon": true,
		"cloud": true, "snowflake": true, "leaf": true, "flower-2": true,
		"baby": true, "dog": true, "cat": true, "fish": true,
		"smartphone": true, "laptop": true, "monitor": true, "printer": true,
		"tv": true, "gamepad-2": true, "headphones": true, "camera": true,
		"music": true, "film": true, "clapperboard": true, "palette": true,
		"dumbbell": true, "footprints": true, "biceps-flexed": true,
		"pill": true, "stethoscope": true, "syringe": true,
		"file-text": true, "folder": true, "archive": true, "database": true,
		"shield": true, "lock": true, "key": true, "bell": true,
		"calendar": true, "clock": true, "alarm-clock": true,
		"map-pin": true, "globe": true, "compass": true,
		"users": true, "user": true, "user-check": true, "user-plus": true,
		"smile": true, "frown": true, "meh": true, "thumbs-up": true,
		"percent": true, "dollar-sign": true, "euro": true, "pound-sterling": true,
		"bitcoin": true, "receipt": true, "scroll-text": true,
		"truck": true, "package": true, "shopping-basket": true,
		"warehouse": true, "factory": true, "building": true,
		"banknote-euro": true, "badge-dollar-sign": true, "hand-coins": true,
		"scale": true, "gavel": true, "calculator": true,
	}
	if !validIcons[icon] {
		return "tag"
	}
	return icon
}

func scanTransactionRowsWithWorkspace(rows *sql.Rows, isBusiness bool) []TransactionRow {
	var list []TransactionRow
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}

	for rows.Next() {
		var row TransactionRow
		var trType string
		var amount int64
		var dateUnix int64
		var catName, catIcon, catColor sql.NullString

		var isSeries int64

		if err := rows.Scan(&row.ID, &trType, &amount, &dateUnix, &row.Description, &row.PaymentStatus,
			&catName, &catIcon, &catColor,
			&row.InstallmentNumber, &row.TotalInstallments,
			&row.AccountName, &row.Author, &isSeries); err != nil {
			continue
		}

		row.Type = trType
		row.Amount = amount
		row.CategoryName = catName.String
		if isBusiness && strings.EqualFold(row.CategoryName, "Salário") {
			row.CategoryName = "Faturamento/Receita"
		}
		row.CategoryIcon = normalizeLucideIcon(catIcon.String)
		row.CategoryColor = normalizeUIThemeColor(catColor.String)
		if row.CategoryIcon == "" {
			row.CategoryIcon = "tag"
		}

		row.IsPending = row.PaymentStatus == "pending"
		row.IsSeries = isSeries == 1
		row.HasInstallmentInfo = row.IsSeries || row.TotalInstallments > 1

		t := time.Unix(dateUnix, 0).UTC()
		localTime := time.Unix(dateUnix, 0)
		row.TimeDisplay = localTime.Format("15:04")
		row.DateInput = localTime.Format("2006-01-02")
		dateOnly := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)

		row.IsOverdue = row.PaymentStatus != "paid" && dateOnly.Before(today)
		if row.IsOverdue {
			row.PaymentIcon = "alert-triangle"
			if row.Type == "INCOME" {
				row.PaymentTitle = "Atrasado — toque para marcar como recebido"
				row.PaymentConfirm = "Marcar este lançamento como recebido?"
				row.PaymentConfirmTitle = "Confirmar recebimento"
			} else {
				row.PaymentTitle = "Atrasado — toque para marcar como pago"
				row.PaymentConfirm = "Marcar este lançamento como pago?"
				row.PaymentConfirmTitle = "Confirmar pagamento"
			}
		} else if row.IsPending {
			row.PaymentIcon = "clock"
			if row.Type == "INCOME" {
				row.PaymentTitle = "Pendente — toque para marcar como recebido"
				row.PaymentConfirm = "Marcar este lançamento como recebido?"
				row.PaymentConfirmTitle = "Confirmar recebimento"
			} else {
				row.PaymentTitle = "Pendente — toque para marcar como pago"
				row.PaymentConfirm = "Marcar este lançamento como pago?"
				row.PaymentConfirmTitle = "Confirmar pagamento"
			}
		} else {
			row.PaymentIcon = "check-circle-2"
			if row.Type == "INCOME" {
				row.PaymentTitle = "Recebido — toque para remover recebimento"
				row.PaymentConfirm = "Remover o recebimento deste lançamento e voltar para pendente?"
				row.PaymentConfirmTitle = "Confirmar alteração de status"
			} else {
				row.PaymentTitle = "Pago — toque para remover pagamento"
				row.PaymentConfirm = "Remover o pagamento deste lançamento e voltar para pendente?"
				row.PaymentConfirmTitle = "Confirmar alteração de status"
			}
		}

		switch {
		case dateOnly.Equal(today):
			row.DateLabel = "Hoje"
		case dateOnly.Equal(yesterday):
			row.DateLabel = "Ontem"
		case dateOnly.Year() == today.Year():
			row.DateLabel = fmt.Sprintf("%d %s", t.Day(), months[t.Month()-1])
		default:
			row.DateLabel = fmt.Sprintf("%d %s %d", t.Day(), months[t.Month()-1], t.Year())
		}

		sign := "-"
		cls := "text-[#FE414F]"
		if trType == "INCOME" {
			sign = "+"
			cls = "text-[#009866]"
		}
		row.AmountDisplay = sign + " R$ " + formatCurrencyCentsBase(amount)
		row.AmountInput = formatCurrencyCentsBase(amount)
		row.AmountClass = cls

		row.CanGenerateReceipt = isBusiness

		list = append(list, row)
	}
	return list
}

func formatCurrencyCentsBase(cents int64) string {
	s := formatCurrencyCents(cents)
	if len(s) > 0 && s[0] == '-' {
		return s[1:]
	}
	return s
}

func formatCurrencyLabel(cents int64) string {
	if cents < 0 {
		return "-R$ " + formatCurrencyCentsBase(cents)
	}
	return "R$ " + formatCurrencyCentsBase(cents)
}

func buildCreditCardSubtitle(providerName string, creditLimit int64) string {
	var parts []string
	if providerName != "" {
		parts = append(parts, providerName)
	}
	if creditLimit > 0 {
		parts = append(parts, "limite "+formatCurrencyLabel(creditLimit))
	}
	if len(parts) == 0 {
		return "Cartão"
	}
	return "Cartão \u2022 " + strings.Join(parts, " \u2022 ")
}

type ModalPickerItem struct {
	ID                 string
	Name               string
	Icon               string
	Color              string
	Type               string
	BoxID              string
	BoxReservedBalance int64
	BoxName            string
	LimitMax           int64
	LimitSpent         int64
}

type ModalPickerData struct {
	Title          string
	TargetName     string
	SearchEndpoint string
	Items          []ModalPickerItem
	SearchQuery    string
	Tipo           string
	ItemType       string
	CreateURL      string
}

func (h *TransactionHandler) HandleCategorySelector(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	rawQ := strings.TrimSpace(r.URL.Query().Get("q"))
	q := normalizeAccents(rawQ)
	tipo := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tipo")))

	tQ := time.Now()
	categories, err := h.queryFormCategories()
	perfStep(reqID, "CategorySelector", "queryFormCategories", time.Since(tQ))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var items []ModalPickerItem
	for _, c := range categories {
		if q != "" && !strings.Contains(normalizeAccents(c.Name), q) {
			continue
		}

		if tipo == "receita" && c.Type != "INCOME" {
			continue
		}
		if tipo == "despesa" && c.Type != "EXPENSE" {
			continue
		}

		items = append(items, ModalPickerItem{
			ID:                 c.ID,
			Name:               c.Name,
			Icon:               c.Icon,
			Color:              c.Color,
			Type:               c.Type,
			BoxID:              c.BoxID,
			BoxReservedBalance: c.BoxReservedBalance,
			BoxName:            c.BoxName,
			LimitMax:           c.LimitMax,
			LimitSpent:         c.LimitSpent,
		})
	}

	createURL := ""
	if rawQ != "" && len(items) == 0 {
		createURL = fmt.Sprintf("/api/categorias/novo?tipo=%s&name=%s", url.QueryEscape(tipo), url.QueryEscape(rawQ))
	}

	itemType := "EXPENSE"
	if tipo == "receita" {
		itemType = "INCOME"
	}

	data := ModalPickerData{
		Title:          "Selecionar Categoria",
		TargetName:     "categoria",
		SearchEndpoint: "/api/categorias/seletor",
		Items:          items,
		SearchQuery:    rawQ,
		Tipo:           tipo,
		ItemType:       itemType,
		CreateURL:      createURL,
	}

	isHtmxRequest := r.Header.Get("HX-Request") == "true"
	_, hasQ := r.URL.Query()["q"]
	isSearch := isHtmxRequest && hasQ
	templateName := "modal-picker"
	if isSearch {
		templateName = "modal-picker-list"
	}

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template %s error: %v", templateName, err)
	}
	perfStep(reqID, "CategorySelector", "templateRender", time.Since(tR))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "CategorySelector", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

type CategoryCreateData struct {
	Tipo             string
	Name             string
	Error            string
	ParentOptions    []CategoryParentOption
	MacroGroups      []string
	DefaultMacro     string
	SelectedParentID string
	SelectedMacro    string
}

func (h *TransactionHandler) queryParentCategoryOptions(typ, excludeID string) ([]CategoryParentOption, error) {
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	defaultMacro := defaultMacroGroupForWorkspace(isBusiness, typ)
	rows, err := h.DB.Query(`
		SELECT id, name, COALESCE(macro_group, ?) AS effective_macro
		FROM categories
		WHERE workspace_id = ? AND type = ? AND parent_id IS NULL AND id != ?
		ORDER BY name
	`, defaultMacro, h.WorkspaceID, typ, excludeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryParentOption
	for rows.Next() {
		var item CategoryParentOption
		var effectiveMacro string
		if err := rows.Scan(&item.ID, &item.Name, &effectiveMacro); err != nil {
			return nil, err
		}
		item.MacroGroup = effectiveMacro
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *TransactionHandler) resolveCreateCategoryMacro(parentID, typ, requestedMacro string) (string, error) {
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	defaultMacro := defaultMacroGroupForWorkspace(isBusiness, typ)
	if parentID == "" {
		if requestedMacro == "" {
			return defaultMacro, nil
		}
		if !isMacroGroupValidForType(isBusiness, requestedMacro, typ) {
			return "", fmt.Errorf("Grupo macro %q não é válido para o tipo %q.", requestedMacro, typ)
		}
		return requestedMacro, nil
	}
	var parentMacro, parentType string
	err := h.DB.QueryRow(`
		SELECT COALESCE(c.macro_group, p.macro_group, ?), c.type
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.id = ? AND c.workspace_id = ? AND c.parent_id IS NULL
	`, defaultMacro, parentID, h.WorkspaceID).Scan(&parentMacro, &parentType)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("Categoria pai não encontrada.")
	}
	if err != nil {
		return "", fmt.Errorf("Não foi possível validar a categoria pai.")
	}
	if parentType != typ {
		return "", fmt.Errorf("Categoria pai deve ter o mesmo tipo da categoria filha.")
	}
	return parentMacro, nil
}

func (h *TransactionHandler) HandleCategoryCreateForm(w http.ResponseWriter, r *http.Request) {
	tipo := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tipo")))
	if tipo != "receita" {
		tipo = "despesa"
	}
	typ := normalizeCategoryType(tipo)
	name := strings.TrimSpace(r.URL.Query().Get("name"))

	parentOptions, err := h.queryParentCategoryOptions(typ, "")
	if err != nil {
		log.Printf("query parent categories error: %v", err)
		parentOptions = nil
	}
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	macroGroups := validMacroGroupsForType(isBusiness, typ)
	defaultMacro := defaultMacroGroupForWorkspace(isBusiness, typ)

	data := CategoryCreateData{
		Tipo:          tipo,
		Name:          name,
		ParentOptions: parentOptions,
		MacroGroups:   macroGroups,
		DefaultMacro:  defaultMacro,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.Templates.ExecuteTemplate(w, "modal-picker-create", data)
}

func (h *TransactionHandler) HandleCategoryCreateAPI(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderCategoryCreateError(w, r, "formulário inválido")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	tipo := strings.TrimSpace(r.FormValue("tipo"))
	typ := normalizeCategoryType(tipo)
	if name == "" {
		h.renderCategoryCreateError(w, r, "Nome da categoria é obrigatório.")
		return
	}
	if typ == "" {
		h.renderCategoryCreateError(w, r, "Tipo da categoria é inválido.")
		return
	}
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	requestedMacro := normalizeMacroGroup(r.FormValue("macro_group"))
	macroGroup, err := h.resolveCreateCategoryMacro(parentID, typ, requestedMacro)
	if err != nil {
		h.renderCategoryCreateError(w, r, err.Error())
		return
	}
	now := time.Now().Unix()
	newID := uuid.NewString()
	if _, err := h.DB.Exec(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, NULLIF(?, ''), ?)
	`, newID, h.WorkspaceID, name, typ, macroGroup, parentID, now); err != nil {
		log.Printf("api create category error: %v", err)
		h.renderCategoryCreateError(w, r, "Não foi possível criar a categoria.")
		return
	}
	triggerData := fmt.Sprintf(
		`{"categoryCreated": {"id":"%s","name":"%s","icon":"tag","color":"#6b7280","type":"%s"}}`,
		newID, template.JSEscapeString(name), typ,
	)
	w.Header().Set("HX-Trigger", triggerData)
	w.WriteHeader(http.StatusOK)
}

func (h *TransactionHandler) renderCategoryCreateError(w http.ResponseWriter, r *http.Request, errMsg string) {
	tipo := strings.ToLower(strings.TrimSpace(r.FormValue("tipo")))
	if tipo != "receita" {
		tipo = "despesa"
	}
	typ := normalizeCategoryType(tipo)
	name := strings.TrimSpace(r.FormValue("name"))
	selectedParentID := strings.TrimSpace(r.FormValue("parent_id"))
	selectedMacro := strings.TrimSpace(r.FormValue("macro_group"))

	parentOptions, err := h.queryParentCategoryOptions(typ, "")
	if err != nil {
		log.Printf("query parent categories error: %v", err)
		parentOptions = nil
	}
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	macroGroups := validMacroGroupsForType(isBusiness, typ)
	defaultMacro := defaultMacroGroupForWorkspace(isBusiness, typ)

	if selectedParentID != "" && selectedMacro == "" {
		for _, opt := range parentOptions {
			if opt.ID == selectedParentID {
				selectedMacro = opt.MacroGroup
				break
			}
		}
	}

	data := CategoryCreateData{
		Tipo:             tipo,
		Name:             name,
		Error:            errMsg,
		ParentOptions:    parentOptions,
		MacroGroups:      macroGroups,
		DefaultMacro:     defaultMacro,
		SelectedParentID: selectedParentID,
		SelectedMacro:    selectedMacro,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.Templates.ExecuteTemplate(w, "modal-picker-create", data)
}

func isLancamentosContext(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get("HX-Current-URL"))
	if raw == "" {
		raw = strings.TrimSpace(r.Referer())
	}
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Path == "/lancamentos" || u.Path == "/transacoes" || u.Path == "/lancamentos-legado"
}

func (h *TransactionHandler) renderLancamentosResumoOOB(w io.Writer, r *http.Request) error {
	currentURL := strings.TrimSpace(r.Header.Get("HX-Current-URL"))
	if currentURL == "" {
		currentURL = strings.TrimSpace(r.Referer())
	}
	if currentURL == "" {
		return nil
	}
	u, err := url.Parse(currentURL)
	if err != nil || (u.Path != "/lancamentos" && u.Path != "/transacoes" && u.Path != "/lancamentos-legado") {
		return nil
	}

	now := time.Now()
	mes := int(now.Month())
	ano := now.Year()
	accountFilter := u.Query().Get("conta")
	filters := lancamentosFiltersFromValues(u.Query())

	if mesStr := strings.TrimSpace(u.Query().Get("mes")); mesStr != "" {
		if parsed, err := strconv.Atoi(mesStr); err == nil && parsed >= 1 && parsed <= 12 {
			mes = parsed
		}
	}
	if anoStr := strings.TrimSpace(u.Query().Get("ano")); anoStr != "" {
		if parsed, err := strconv.Atoi(anoStr); err == nil && parsed >= 2020 && parsed <= now.Year()+10 {
			ano = parsed
		}
	}

	data, err := h.buildLancamentosData(accountFilter, mes, ano, filters)
	if err != nil {
		return err
	}
	data.OOB = true

	resumoName := "lancamentos-resumo"
	if u.Path == "/lancamentos-legado" {
		resumoName = "lancamentos-resumo"
	}
	return h.Templates.ExecuteTemplate(w, resumoName, data)
}
