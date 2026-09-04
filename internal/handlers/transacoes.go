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
	"math"
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

	"github.com/contabase-app/contabase/internal/categorycontract"
	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
)

var errPaidInvoiceMutationBlocked = errors.New("transacao vinculada a fatura paga")
var errInvoicePaymentTransactionProtected = errors.New("transacao de pagamento de fatura protegida")
var errTransactionMutationTargetInvalid = errors.New("target de mutacao de transacao invalido")
var errTransactionInvoiceLinkInvalid = errors.New("vinculo estrutural de fatura invalido")
var errRecurringFutureSplitBlocked = errors.New("recorrencia futura bloqueada")
var errRecurringFutureCivilConflict = errors.New("contrato civil da recorrencia futura inconsistente")
var errRecurringAllConflict = errors.New("edicao de toda a recorrencia bloqueada")
var errRecurringAllIndividualOnly = errors.New("campo permitido apenas em edicao individual")
var errRecurringDeleteConflict = errors.New("exclusao recorrente bloqueada")
var errBoxReserveInsufficient = errors.New("saldo reservado insuficiente para consumo da caixinha")
var errBoxCategoryAmbiguous = errors.New("categoria vinculada a múltiplas caixinhas")

func invoiceStatusMutationBlocked(err error) bool {
	return errors.Is(err, errPaidInvoiceMutationBlocked)
}

func respondInvoiceCompositionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvoiceStateTransitionConflict) {
		http.Error(w, "estado financeiro da fatura não permite esta alteração", http.StatusConflict)
		return
	}
	http.Error(w, "erro ao reconciliar fatura", http.StatusInternalServerError)
}

type FormAccount struct {
	ID               string
	Name             string
	Icon             string
	Color            string
	ProviderSlug     string
	ProviderMark     string
	ProviderLogoPath string
	ProviderName     string
	Tipo             string
	ClosingDay       string
	DueDay           string
	CreditLimitLabel string
	BalanceLabel     string
}

// PaymentAccountOption is an eligible cash account shown when confirming a
// direct income or expense. Credit cards are intentionally absent: paying a
// card invoice has its own settlement flow.
type PaymentAccountOption struct {
	ID   string
	Name string
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
	ParentID           string
	ParentName         string
}

type InvoiceOption struct {
	ID             string
	Reference      string
	ReferenceLabel string
	Status         string
	StatusLabel    string
	CycleLabel     string
	FinLabel       string
	DueLabel       string
	ClosingLabel   string
	TotalDisplay   string
	PendingDisplay string
	IsSensitive    bool
	ManualEligible bool
	ManualBlock    string
}

type FormTransacaoData struct {
	Accounts                []FormAccount
	Contacts                []ContatoRow
	Categories              []FormCategory
	FaturaOptions           []InvoiceOption
	IsBusiness              bool
	CanCreateContact        bool
	IsEdit                  bool
	EditID                  string
	TipoInicial             string
	ValorPreenchido         string
	DescricaoPreenchida     string
	AnotacoesPreenchidas    string
	AttachmentViewURL       string
	AttachmentDownloadURL   string
	DataPreenchida          string
	StatusInicial           string
	OrigemContaID           string
	OrigemTipo              string
	OrigemNome              string
	OrigemIcon              string
	OrigemColor             string
	OrigemProviderMark      string
	OrigemProviderLogoPath  string
	CategoriaID             string
	CategoriaNome           string
	CategoriaIcon           string
	CategoriaColor          string
	CategoriaType           string
	DestinoContaID          string
	DestinoNome             string
	DestinoIcon             string
	DestinoColor            string
	DestinoProviderMark     string
	DestinoProviderLogoPath string
	IsSeriesEdit            bool
	FixoInicial             bool
	RecorrenciaInicial      string
	DueDatePreenchida       string
	ContatoIDPreenchido     string
	ReturnInvoiceID         string
	ManualInvoiceID         string
	InvoiceAssignmentMode   string
	TransactionUpdatedAt    int64
	QuickMoveDestinationRef string
	QuickMoveScope          string
	AuditCreatedBy          string
	AuditUpdatedAt          string
	PaidAtDisplay           string
	ScheduledPaymentDisplay string
	CompetenciaDisplay      string
	VencimentoDisplay       string
	TodayCivil              string
	StatusHistory           []TransactionStatusEvent
}

type TransactionStatusEvent struct {
	ID                  string
	PreviousStatus      string
	NewStatus           string
	PreviousStatusLabel string
	NewStatusLabel      string
	PreviousPaidAt      string
	NewPaidAt           string
	UserName            string
	Source              string
	SourceLabel         string
	CreatedAtLabel      string
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
	DB               *sql.DB
	Templates        TemplateEngine
	WorkspaceID      string
	UserID           string
	CanCreateContact bool
	testHooks        *recurringEditTestHooks
}

// recurringEditTestHooks is deliberately private and nil in production. Tests
// use per-handler hooks to coordinate real requests without global state.
type recurringEditTestHooks struct {
	afterPreflight     func(scope string)
	beforeAllRuleWrite func(tx *sql.Tx) error
	afterInvoiceMove   func(tx *sql.Tx) error
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
		data.CanCreateContact = h.CanCreateContact
	}
	perfStep(reqID, "NovaTransacao", "formData+contacts", time.Since(t2))

	tipoParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tipo")))
	if tipoParam == "receita" || tipoParam == "transferencia" || tipoParam == "despesa" {
		data.TipoInicial = tipoParam
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
	var dueDate OptionalCivilWriteDate
	if isBusiness {
		if paymentStatus != "paid" {
			dueRaw := strings.TrimSpace(r.FormValue("due_date"))
			if dueRaw != "" {
				parsedDueDate, err := NewOptionalCivilWriteDate(dueRaw)
				if err != nil {
					respondTransactionValidationError(w, "A data de vencimento informada é inválida. Use um valor válido no formato de calendário.", http.StatusUnprocessableEntity)
					return
				}
				dueDate = parsedDueDate
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

	civilDate, err := NewCivilWriteDate(dataStr)
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
	keepNewUpload := false
	if attachmentPath != "" {
		defer func() {
			if !keepNewUpload {
				h.removeNewTransactionUpload(attachmentPath)
			}
		}()
	}

	invoiceIDOverride := strings.TrimSpace(r.FormValue("invoice_id"))
	if r.FormValue("fatura_offset") == "next" {
		invoiceIDOverride = "NEXT_INVOICE"
	}

	perfStep(reqID, "SalvarTransacao", "parse+validate", time.Since(t0))
	tInsert := time.Now()
	dbB := dbSnap(h.DB)
	assignedInvoiceID, err := h.insertTransactionFromForm(tipo, amountCents, descricao, anotacoes, attachmentPath, civilDate, origemContaID, destinoContaID, categoriaID, totalInstallments, paymentStatus, fixo, recorrencia, contactID, dueDate, totalOccurrences, invoiceIDOverride, allowBoxOverdraft)
	dbA := dbSnap(h.DB)
	perfStep(reqID, "SalvarTransacao", "insertTransaction", time.Since(tInsert))
	perfDBDelta(reqID, "SalvarTransacao", "insertTransaction", dbB, dbA)
	if err != nil {
		if errors.Is(err, errInvoiceStateTransitionConflict) {
			http.Error(w, "estado financeiro da fatura não permite este lançamento", http.StatusConflict)
			return
		}
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
	keepNewUpload = true

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

func (h *TransactionHandler) insertTransaction(tipo string, amountCents int64, descricao string, anotacoes string, attachmentPath string, date CivilWriteDate, origemContaID string, destinoContaID string, categoriaID string, totalInstallments int64, paymentStatus string, fixed bool, recurrenceFrequency string, contactID string, dueDate OptionalCivilWriteDate, totalOccurrences *int64, invoiceIDOverride string, allowBoxOverdraft bool) (string, error) {
	return h.insertTransactionWithOptions(tipo, amountCents, descricao, anotacoes, attachmentPath, date, origemContaID, destinoContaID, categoriaID, totalInstallments, paymentStatus, fixed, recurrenceFrequency, contactID, dueDate, totalOccurrences, invoiceIDOverride, allowBoxOverdraft, insertTransactionOptions{})
}

type insertTransactionOptions struct {
	scheduleFuturePaid bool
}

func (h *TransactionHandler) insertTransactionFromForm(tipo string, amountCents int64, descricao string, anotacoes string, attachmentPath string, date CivilWriteDate, origemContaID string, destinoContaID string, categoriaID string, totalInstallments int64, paymentStatus string, fixed bool, recurrenceFrequency string, contactID string, dueDate OptionalCivilWriteDate, totalOccurrences *int64, invoiceIDOverride string, allowBoxOverdraft bool) (string, error) {
	return h.insertTransactionWithOptions(tipo, amountCents, descricao, anotacoes, attachmentPath, date, origemContaID, destinoContaID, categoriaID, totalInstallments, paymentStatus, fixed, recurrenceFrequency, contactID, dueDate, totalOccurrences, invoiceIDOverride, allowBoxOverdraft, insertTransactionOptions{scheduleFuturePaid: true})
}

func (h *TransactionHandler) insertTransactionWithOptions(tipo string, amountCents int64, descricao string, anotacoes string, attachmentPath string, date CivilWriteDate, origemContaID string, destinoContaID string, categoriaID string, totalInstallments int64, paymentStatus string, fixed bool, recurrenceFrequency string, contactID string, dueDate OptionalCivilWriteDate, totalOccurrences *int64, invoiceIDOverride string, allowBoxOverdraft bool, options insertTransactionOptions) (string, error) {
	if err := date.validate(); err != nil {
		return "", fmt.Errorf("invalid civil competence date: %w", err)
	}
	if err := dueDate.validate(); err != nil {
		return "", fmt.Errorf("invalid civil due date: %w", err)
	}
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	tx, err := h.DB.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var accType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, origemContaID, h.WorkspaceID).Scan(&accType); err != nil {
		return "", fmt.Errorf("conta de origem arquivada ou não encontrada: %w", err)
	}
	scheduleFirstPayment := options.scheduleFuturePaid && shouldScheduleFutureFormPayment(date, paymentStatus, tipo, accType)
	if scheduleFirstPayment {
		paymentStatus = models.PaymentStatusPending
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
	var recurringSeriesID interface{}
	var projectionRule *recurrenceProjectionRule
	if fixed {
		ruleID := uuid.NewString()
		seriesID := uuid.NewString()
		ruleStatus := recurringRuleDefaultStatus(paymentStatus, accType, tipo)
		var catID interface{}
		if categoriaID != "" {
			catID = categoriaID
		}
		var destID interface{}
		if destinoContaID != "" {
			destID = destinoContaID
		}
		if err := h.insertRecurringSeriesTx(tx, seriesID, totalOccurrences, now); err != nil {
			return "", fmt.Errorf("insert recurring series: %w", err)
		}

		dueDateOffset := sql.NullInt64{}
		if isBusiness && dueDate.Valid() {
			dueDateOffset = dueDateOffsetDays(dueDate, date)
		}
		var ruleContactRef interface{}
		if isBusiness && contactID != "" {
			ruleContactRef = contactID
		}
		if err := execOneTx(tx, `
			INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, destination_account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, due_date_offset_days, contact_id, frequency, default_payment_status, active, total_occurrences, series_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)
		`, ruleID, h.WorkspaceID, h.UserID, origemContaID, destID, catID, tipo, amountCents, descricao, date.LegacyUnix(), date.Civil(), date.LegacyUnix(), date.Civil(), dueDateOffset, ruleContactRef, recurrenceFrequency, ruleStatus, totalOccurrences, seriesID, now, now); err != nil {
			return "", fmt.Errorf("insert recurring rule: %w", err)
		}
		recurringRuleID = ruleID
		recurringSeriesID = seriesID
		projectionRule = &recurrenceProjectionRule{
			ID:                   ruleID,
			SeriesID:             seriesID,
			AccountID:            origemContaID,
			DestinationAccountID: destinoContaID,
			CategoryID:           categoriaID,
			Type:                 tipo,
			Amount:               amountCents,
			Description:          descricao,
			StartDate:            date.LegacyUnix(),
			StartDateCivil:       date.Civil(),
			Frequency:            recurrenceFrequency,
			DefaultPaymentStatus: ruleStatus,
			TotalOccurrences:     totalOccurrences,
			HasDueDate:           isBusiness && dueDate.Valid(),
			ContactID:            contactID,
			IsBusiness:           isBusiness,
		}
		if isBusiness && dueDate.Valid() {
			projectionRule.DueDateUnix = dueDate.LegacyValue().(int64)
			projectionRule.DueDateCivil = dueDate.CivilValue().(string)
			projectionRule.DueDateOffsetDays = dueDateOffsetDays(dueDate, date)
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
		installmentDate, err := date.AddMonths(int(i - 1))
		if err != nil {
			return "", fmt.Errorf("insertTransaction: add %d months to competence date: %w", i-1, err)
		}
		installmentDueDate, err := dueDate.AddMonths(int(i - 1))
		if err != nil {
			return "", fmt.Errorf("insertTransaction: add %d months to due date: %w", i-1, err)
		}
		if !isBusiness {
			installmentDueDate = OptionalCivilWriteDate{}
		}

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
		var contactRef interface{}
		if contactID != "" {
			contactRef = contactID
		}
		var scheduledPaymentAt interface{}
		var scheduledPaymentDateCivil interface{}
		var scheduledPaymentCreatedBy interface{}
		if scheduleFirstPayment && i == 1 {
			scheduledPaymentAt = installmentDate.LegacyUnix()
			scheduledPaymentDateCivil = installmentDate.Civil()
			scheduledPaymentCreatedBy = h.UserID
		}

		var invoiceID interface{}
		var invoiceOverrideRef interface{}
		if tipo == "EXPENSE" && accType == "CREDIT_CARD" {
			if invoiceIDOverride == "NEXT_INVOICE" && i == 1 {
				nextInvID, _, _, _, _, err := resolveCardTransactionInvoiceTx(tx, h.WorkspaceID, origemContaID, installmentDate.LegacyUnix(), "next")
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
				destinationSnapshot, snapshotErr := loadInvoiceFinancialSnapshotTx(tx, h.WorkspaceID, invoiceIDOverride)
				if snapshotErr != nil || destinationSnapshot.AccountID != origemContaID {
					return "", fmt.Errorf("fatura inválida ou não pertence ao cartão")
				}
				if destinationSnapshot.Status != models.InvoiceStatusOpen && destinationSnapshot.Status != models.InvoiceStatusClosed && destinationSnapshot.Status != models.InvoiceStatusPaid {
					return "", fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
				}
				if destinationSnapshot.blocksIncomingTransaction() {
					return "", fmt.Errorf("%w: fatura integralmente paga não aceita lançamento manual", errInvoiceStateTransitionConflict)
				}
				invoiceID = invoiceIDOverride
				invoiceOverrideRef = invoiceIDOverride
				if assignedInvoiceID == "" {
					assignedInvoiceID = invoiceIDOverride
				}
			} else if invoiceIDOverride != "" && totalInstallments > 1 && overrideReference != "" {
				nextInvoiceID, err := ensureInvoiceAfterReferenceTx(tx, h.WorkspaceID, origemContaID, overrideReference)
				if err != nil {
					return "", fmt.Errorf("invoice override installments: %w", err)
				}
				if _, err := ensureInvoiceAcceptsMutableCompositionTx(tx, h.WorkspaceID, origemContaID, nextInvoiceID); err != nil {
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
				invID, _, _, _, _, err := resolveAutomaticCardTransactionInvoiceTx(tx, h.WorkspaceID, origemContaID, installmentDate.LegacyUnix(), "auto")
				if err != nil {
					return "", fmt.Errorf("invoice: %w", err)
				}
				invoiceID = invID
				if assignedInvoiceID == "" {
					assignedInvoiceID = invID
				}
			}
		}

		err = execOneTx(tx, `
			INSERT INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, category_id, invoice_id, invoice_override_id, type, amount, date, date_civil, description, notes, attachment_path, installment_number, total_installments, parent_id, recurring_rule_id, recurrence_sequence, series_id, status, due_date, due_date_civil, contact_id, scheduled_payment_at, scheduled_payment_date_civil, scheduled_payment_created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, h.WorkspaceID, h.UserID, origemContaID, destID, catID, invoiceID, invoiceOverrideRef, tipo, amount, installmentDate.LegacyUnix(), installmentDate.Civil(), descricao, anotacoes, attachmentPath, i, totalInstallments, pid, recurringRuleID, recurrenceSequence(i, fixed), recurringSeriesID, installmentStatus, installmentDueDate.LegacyValue(), installmentDueDate.CivilValue(), contactRef, scheduledPaymentAt, scheduledPaymentDateCivil, scheduledPaymentCreatedBy, now, now)
		if err != nil {
			return "", fmt.Errorf("insert transaction: %w", err)
		}
		if err := h.consumeBoxReserveOnExpenseCreationTx(tx, tipo, categoriaID, amount, id, installmentDate.LegacyUnix(), now, allowBoxOverdraft); err != nil {
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
		if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, touchedTxIDs, now); err != nil {
			return "", fmt.Errorf("reconcile touched invoice: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return assignedInvoiceID, nil
}

func shouldScheduleFutureFormPayment(date CivilWriteDate, paymentStatus, transactionType, accountType string) bool {
	if paymentStatus != models.PaymentStatusPaid {
		return false
	}
	if transactionType != models.TransactionTypeExpense && transactionType != models.TransactionTypeIncome {
		return false
	}
	if accountType == models.AccountTypeCreditCard {
		return false
	}
	today := civildate.CivilDateToday(appTimeLocation())
	return civildate.CivilDateDaysBetween(today, date.date) > 0
}

func (h *TransactionHandler) scheduleFormPaymentTx(tx *sql.Tx, id string, date CivilWriteDate, now int64) error {
	return execOneTx(tx, `
		UPDATE transactions
		SET status = 'pending',
		    paid_at = NULL,
		    paid_date_civil = NULL,
		    scheduled_payment_at = ?,
		    scheduled_payment_date_civil = ?,
		    scheduled_payment_created_by = ?,
		    scheduled_payment_executed_at = NULL,
		    updated_at = CASE WHEN updated_at >= ? THEN updated_at + 1 ELSE ? END
		WHERE id = ? AND workspace_id = ? AND status = 'pending'
	`, date.LegacyUnix(), date.Civil(), h.UserID, now, now, id, h.WorkspaceID)
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
	SeriesID             string
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
	EffectiveStartSeq    int64
	EffectiveEndSeq      sql.NullInt64
	EndDateCivil         sql.NullString
	StartDateCivil       string
	DueDateOffsetDays    sql.NullInt64
	HasDueDate           bool
	DueDateUnix          int64
	DueDateCivil         string
	ContactID            string
	IsBusiness           bool
}

// dueDateOffsetDays computes the signed civil day difference between competence and due date.
func dueDateOffsetDays(dueDate OptionalCivilWriteDate, competence CivilWriteDate) sql.NullInt64 {
	if !dueDate.Valid() {
		return sql.NullInt64{}
	}
	compCD := civildate.MustCivilDate(competence.Civil())
	dueCD := civildate.MustCivilDate(dueDate.CivilValue().(string))
	offset := civildate.CivilDateDaysBetween(compCD, dueCD)
	return sql.NullInt64{Int64: offset, Valid: true}
}

func (h *TransactionHandler) generateRecurrenceProjection(tx *sql.Tx, rule recurrenceProjectionRule, now int64) error {
	seriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, rule.ID, now)
	if err != nil {
		return err
	}
	rule.SeriesID = seriesID

	horizonCWD, err := recurrenceCivilHorizon(time.Unix(now, 0).UTC())
	if err != nil {
		return fmt.Errorf("recurrence projection horizon: %w", err)
	}
	horizon := horizonCWD.LegacyUnix()
	horizonCivil := horizonCWD.Civil()
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
		if maxDue.Valid && maxDue.Int64 > 0 {
			maxDueCivil := civildate.LegacyUnixToCivilDateUTC(maxDue.Int64).String()
			if maxDueCivil > horizonCivil {
				horizon = civildate.CivilDateToLegacyUnixNoonUTC(civildate.MustCivilDate(maxDueCivil))
				horizonCivil = maxDueCivil
			}
		}
	}

	if rule.StartDateCivil == "" {
		rule.StartDateCivil = civildate.LegacyUnixToCivilDateUTC(rule.StartDate).String()
	}
	if _, err := ParseCivilDateInput(rule.StartDateCivil); err != nil {
		return fmt.Errorf("start_date_civil invalida: %w", err)
	}
	var endDateCivilValidated string
	if rule.EndDateCivil.Valid {
		if _, err := ParseCivilDateInput(rule.EndDateCivil.String); err != nil {
			return fmt.Errorf("end_date_civil invalida: %w", err)
		}
		endDateCivilValidated = rule.EndDateCivil.String
	}
	anchor := MustCivilWriteDate(rule.StartDateCivil)
	effectiveStartSeq := rule.EffectiveStartSeq
	if effectiveStartSeq < 1 {
		effectiveStartSeq = 1
	}

	startSeq := effectiveStartSeq
	endSeq := int64(math.MaxInt64)
	if rule.TotalOccurrences != nil && *rule.TotalOccurrences > 0 {
		if *rule.TotalOccurrences < endSeq {
			endSeq = *rule.TotalOccurrences
		}
	}
	if rule.EffectiveEndSeq.Valid && rule.EffectiveEndSeq.Int64 < endSeq {
		endSeq = rule.EffectiveEndSeq.Int64
	}

	for seq := startSeq; seq <= endSeq; seq++ {
		offset := seq - effectiveStartSeq
		occDate, err := recurrenceCivilDateFromOffset(anchor, rule.Frequency, offset)
		if err != nil {
			return fmt.Errorf("projection seq %d offset %d: %w", seq, offset, err)
		}

		if occDate.LegacyUnix() > horizon {
			break
		}

		if endDateCivilValidated != "" {
			if occDate.Civil() > endDateCivilValidated {
				break
			}
		}

		dateCivil := occDate.Civil()
		var dueDateRef interface{}
		var dueDateCivilRef interface{}
		if rule.HasDueDate && rule.DueDateOffsetDays.Valid {
			dueOcc, dErr := occDate.AddDays(int(rule.DueDateOffsetDays.Int64))
			if dErr != nil {
				return fmt.Errorf("projection seq %d: due date calculation: %w", seq, dErr)
			}
			dueDateRef = dueOcc.LegacyUnix()
			dueDateCivilRef = dueOcc.Civil()
		}

		exists, skipped, blocked, err := recurringOccurrenceTerminalConflictsTx(tx, h.WorkspaceID, rule.SeriesID, seq)
		if err != nil {
			return err
		}
		if exists {
			if skipped || blocked {
				return fmt.Errorf("%w: recurring transaction contradicts terminal tombstone", errInvoiceStateStructural)
			}
			continue
		}
		if skipped {
			if blocked {
				return fmt.Errorf("%w: manual skip contradicts frozen block", errInvoiceStateStructural)
			}
			continue
		}
		blocked, err = resolveRecurringOccurrenceBlockTx(tx, recurringOccurrenceBlockExpectation{
			WorkspaceID:        h.WorkspaceID,
			SeriesID:           rule.SeriesID,
			RecurringRuleID:    rule.ID,
			RecurrenceSequence: seq,
			OccurrenceDate:     occDate,
			AccountID:          rule.AccountID,
		})
		if err != nil {
			return err
		}
		if blocked {
			continue
		}
		id := uuid.NewString()
		var catID interface{}
		if rule.CategoryID != "" {
			catID = rule.CategoryID
		}
		var destID interface{}
		if rule.DestinationAccountID != "" {
			destID = rule.DestinationAccountID
		}
		var seriesIDRef interface{}
		if strings.TrimSpace(rule.SeriesID) != "" {
			seriesIDRef = rule.SeriesID
		}
		var contactIDRef interface{}
		if rule.IsBusiness && strings.TrimSpace(rule.ContactID) != "" {
			contactIDRef = rule.ContactID
		}

		var invoiceID interface{}
		status := projectionStatusForRule(rule.DefaultPaymentStatus)
		if isCreditCard {
			evidence, err := resolveAutomaticInvoiceForOccurrenceTx(tx, h.WorkspaceID, rule.AccountID, occDate)
			if err != nil {
				return fmt.Errorf("invoice for recurrence: %w", err)
			}
			invoiceID = evidence.InvoiceID
			status = "paid"
		}

		if err := execOneTx(tx, `
			INSERT INTO transactions (
				id, workspace_id, user_id, account_id, destination_account_id,
				category_id, invoice_id, type, amount, date, date_civil, description,
				installment_number, total_installments, parent_id,
				recurring_rule_id, recurrence_sequence, series_id, status,
				due_date, due_date_civil, contact_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, id, h.WorkspaceID, h.UserID, rule.AccountID, destID, catID, invoiceID, rule.Type, rule.Amount, occDate.LegacyUnix(), dateCivil, rule.Description, rule.ID, seq, seriesIDRef, status, dueDateRef, dueDateCivilRef, contactIDRef, now, now); err != nil {
			return err
		}
	}

	return execOneTx(tx, `
		UPDATE recurring_rules
		SET generated_until = ?, generated_until_civil = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
	`, horizon, horizonCivil, now, rule.ID, h.WorkspaceID)
}

// EnsureRecurringMaterialized materializa ocorrências recorrentes faltantes para
// todas as séries ativas do workspace até horizonDate. Não liga job, boot ou
// fallback; deve ser chamada explicitamente pelo caller.
func EnsureRecurringMaterialized(db *sql.DB, workspaceID string, horizon CivilWriteDate) error {
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("workspace_id obrigatorio")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := EnsureRecurringMaterializedTx(tx, workspaceID, horizon, time.Now().Unix()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// EnsureRecurringMaterializedTx é o motor central de materialização recorrente.
// Para cada série ativa, identifica as regras ativas e os intervalos de
// recurrence_sequence esperados. Gera apenas ocorrências faltantes, preservando
// pagas, exceções manuais, faturas CLOSED/PAID e nunca sobrescrevendo transações
// existentes.
func EnsureRecurringMaterializedTx(tx *sql.Tx, workspaceID string, horizon CivilWriteDate, now int64) error {
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("workspace_id obrigatorio")
	}
	if tx == nil {
		return fmt.Errorf("tx obrigatorio")
	}

	h := &TransactionHandler{DB: nil, WorkspaceID: workspaceID, UserID: ""}

	// Backfill de regras legadas sem series_id: cria recurring_series e vincula
	// regras/transações antes de iterar. Isso garante que regras antigas sejam
	// materializadas sem exigir migration adicional.
	rowsLegacy, err := tx.Query(`
		SELECT id FROM recurring_rules
		WHERE workspace_id = ? AND active = 1 AND (series_id IS NULL OR TRIM(series_id) = '')
	`, workspaceID)
	if err != nil {
		return fmt.Errorf("listar regras legadas: %w", err)
	}
	var legacyRuleIDs []string
	for rowsLegacy.Next() {
		var ruleID string
		if err := rowsLegacy.Scan(&ruleID); err != nil {
			_ = rowsLegacy.Close()
			return err
		}
		legacyRuleIDs = append(legacyRuleIDs, ruleID)
	}
	_ = rowsLegacy.Close()
	for _, ruleID := range legacyRuleIDs {
		if _, err := EnsureRecurringSeriesForRuleTx(tx, workspaceID, ruleID, now); err != nil {
			return fmt.Errorf("backfill regra legada %s: %w", ruleID, err)
		}
	}

	rows, err := tx.Query(`
		SELECT id, total_occurrences
		FROM recurring_series
		WHERE workspace_id = ? AND active = 1
		ORDER BY created_at ASC, id ASC
	`, workspaceID)
	if err != nil {
		return fmt.Errorf("listar series: %w", err)
	}
	defer rows.Close()

	type seriesInfo struct {
		id               string
		totalOccurrences sql.NullInt64
	}
	var seriesList []seriesInfo
	for rows.Next() {
		var s seriesInfo
		if err := rows.Scan(&s.id, &s.totalOccurrences); err != nil {
			return fmt.Errorf("scan series: %w", err)
		}
		seriesList = append(seriesList, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterar series: %w", err)
	}

	for _, s := range seriesList {
		if err := h.ensureSeriesMaterializedTx(tx, workspaceID, s.id, s.totalOccurrences, horizon, now); err != nil {
			return fmt.Errorf("materializar serie %s: %w", s.id, err)
		}
	}

	return nil
}

type recurringMaterializedRule struct {
	id                   string
	accountID            string
	destinationAccountID sql.NullString
	categoryID           sql.NullString
	trType               string
	amount               int64
	description          string
	startDate            int64
	startDateCivil       sql.NullString
	startsAt             sql.NullInt64
	startsAtCivil        sql.NullString
	frequency            string
	defaultPaymentStatus string
	effectiveStartSeq    int64
	effectiveEndSeq      sql.NullInt64
	totalOccurrences     sql.NullInt64
	endDate              sql.NullInt64
	endDateCivil         sql.NullString
	generatedUntil       sql.NullInt64
	generatedUntilCivil  sql.NullString
	dueDatePolicy        sql.NullString
	dueDateOffsetDays    sql.NullInt64
	contactID            sql.NullString
	isBusiness           bool
}

func (h *TransactionHandler) loadActiveSeriesRulesTx(tx *sql.Tx, workspaceID, seriesID string) ([]recurringMaterializedRule, error) {
	rows, err := tx.Query(`
		SELECT
		    r.id,
		    r.account_id,
		    r.destination_account_id,
		    r.category_id,
		    r.type,
		    r.amount,
		    r.description,
		    r.start_date,
		    r.start_date_civil,
		    r.starts_at,
		    r.starts_at_civil,
		    r.frequency,
		    r.default_payment_status,
		    r.effective_start_sequence,
		    r.effective_end_sequence,
		    r.total_occurrences,
		    r.end_date,
		    r.end_date_civil,
		    r.generated_until,
		    r.generated_until_civil,
		    r.due_date_policy,
		    r.due_date_offset_days,
		    r.contact_id,
		    CASE WHEN w.type = 'business' THEN 1 ELSE 0 END AS is_business
		FROM recurring_rules r
		JOIN workspaces w ON w.id = r.workspace_id
		WHERE r.workspace_id = ? AND r.series_id = ? AND r.active = 1
		ORDER BY r.effective_start_sequence ASC, r.created_at ASC
	`, workspaceID, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []recurringMaterializedRule
	for rows.Next() {
		var r recurringMaterializedRule
		if err := rows.Scan(
			&r.id,
			&r.accountID,
			&r.destinationAccountID,
			&r.categoryID,
			&r.trType,
			&r.amount,
			&r.description,
			&r.startDate,
			&r.startDateCivil,
			&r.startsAt,
			&r.startsAtCivil,
			&r.frequency,
			&r.defaultPaymentStatus,
			&r.effectiveStartSeq,
			&r.effectiveEndSeq,
			&r.totalOccurrences,
			&r.endDate,
			&r.endDateCivil,
			&r.generatedUntil,
			&r.generatedUntilCivil,
			&r.dueDatePolicy,
			&r.dueDateOffsetDays,
			&r.contactID,
			&r.isBusiness,
		); err != nil {
			return nil, err
		}

		if r.startDateCivil.Valid {
			if _, err := ParseCivilDateInput(r.startDateCivil.String); err != nil {
				return nil, fmt.Errorf("start_date_civil invalida na regra %s: %w", r.id, err)
			}
		} else {
			r.startDateCivil = sql.NullString{String: civildate.LegacyUnixToCivilDateUTC(r.startDate).String(), Valid: true}
		}

		if r.startsAtCivil.Valid {
			if _, err := ParseCivilDateInput(r.startsAtCivil.String); err != nil {
				return nil, fmt.Errorf("starts_at_civil invalida na regra %s: %w", r.id, err)
			}
		} else if r.startsAt.Valid && r.startsAt.Int64 > 0 {
			r.startsAtCivil = sql.NullString{String: civildate.LegacyUnixToCivilDateUTC(r.startsAt.Int64).String(), Valid: true}
		} else {
			r.startsAtCivil = r.startDateCivil
		}

		if r.endDateCivil.Valid {
			if _, err := ParseCivilDateInput(r.endDateCivil.String); err != nil {
				return nil, fmt.Errorf("end_date_civil invalida na regra %s: %w", r.id, err)
			}
		} else if r.endDate.Valid && r.endDate.Int64 > 0 {
			r.endDateCivil = sql.NullString{String: civildate.LegacyUnixToCivilDateUTC(r.endDate.Int64).String(), Valid: true}
		}

		if r.generatedUntilCivil.Valid {
			if _, err := ParseCivilDateInput(r.generatedUntilCivil.String); err != nil {
				return nil, fmt.Errorf("generated_until_civil invalida na regra %s: %w", r.id, err)
			}
		} else if r.generatedUntil.Valid && r.generatedUntil.Int64 > 0 {
			r.generatedUntilCivil = sql.NullString{String: civildate.LegacyUnixToCivilDateUTC(r.generatedUntil.Int64).String(), Valid: true}
		}

		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (h *TransactionHandler) ensureSeriesMaterializedTx(tx *sql.Tx, workspaceID, seriesID string, seriesTotalOccurrences sql.NullInt64, horizon CivilWriteDate, now int64) error {
	// Garante que regras legadas sem series_id sejam compatibilizadas.
	// Primeiro tenta pela série; se não houver, busca uma regra ativa com o próprio
	// id da série (caso de legacy onde series_id ainda não foi preenchido).
	rowsRules, err := tx.Query(`
		SELECT id, user_id FROM recurring_rules
		WHERE workspace_id = ? AND series_id = ? AND active = 1
		ORDER BY effective_start_sequence ASC
		LIMIT 1
	`, workspaceID, seriesID)
	if err != nil {
		return err
	}
	var firstRuleID, firstUserID string
	var hasRule bool
	if rowsRules.Next() {
		hasRule = true
		if err := rowsRules.Scan(&firstRuleID, &firstUserID); err != nil {
			_ = rowsRules.Close()
			return err
		}
	}
	_ = rowsRules.Close()
	if !hasRule {
		rowsLegacy, err := tx.Query(`
			SELECT id, user_id FROM recurring_rules
			WHERE workspace_id = ? AND id = ? AND active = 1
			ORDER BY effective_start_sequence ASC
			LIMIT 1
		`, workspaceID, seriesID)
		if err != nil {
			return err
		}
		if rowsLegacy.Next() {
			hasRule = true
			if err := rowsLegacy.Scan(&firstRuleID, &firstUserID); err != nil {
				_ = rowsLegacy.Close()
				return err
			}
		}
		_ = rowsLegacy.Close()
	}
	if !hasRule {
		return nil
	}

	// Regras legadas sem series_id: backfill via EnsureRecurringSeriesForRuleTx.
	if _, err := EnsureRecurringSeriesForRuleTx(tx, workspaceID, firstRuleID, now); err != nil {
		return err
	}

	rules, err := h.loadActiveSeriesRulesTx(tx, workspaceID, seriesID)
	if err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}

	// Limite absoluto de sequence: total_occurrences da série, se houver.
	var absoluteMaxSeq int64 = math.MaxInt64
	if seriesTotalOccurrences.Valid && seriesTotalOccurrences.Int64 > 0 {
		absoluteMaxSeq = seriesTotalOccurrences.Int64
	}

	horizonUnix := horizon.LegacyUnix()

	// Cache de tipo de conta por account_id, evitando N+1 de SELECT type dentro do
	// loop de sequences para regras que compartilham a mesma conta.
	accountTypeCache := make(map[string]string)

	// Para cada regra ativa, determinar o intervalo de sequences esperado.
	for i, rule := range rules {
		startSeq := rule.effectiveStartSeq
		if startSeq < 1 {
			startSeq = 1
		}

		endSeq := absoluteMaxSeq
		if rule.effectiveEndSeq.Valid && rule.effectiveEndSeq.Int64 < endSeq {
			endSeq = rule.effectiveEndSeq.Int64
		}
		if rule.totalOccurrences.Valid && rule.totalOccurrences.Int64 > 0 && rule.totalOccurrences.Int64 < endSeq {
			endSeq = rule.totalOccurrences.Int64
		}
		if i+1 < len(rules) {
			nextStart := rules[i+1].effectiveStartSeq - 1
			if nextStart < endSeq {
				endSeq = nextStart
			}
		}

		if startSeq > endSeq {
			continue
		}

		anchor := MustCivilWriteDate(rule.startsAtCivil.String)

		for seq := startSeq; seq <= endSeq; seq++ {
			offset := seq - rule.effectiveStartSeq
			occDate, err := recurrenceCivilDateFromOffset(anchor, rule.frequency, offset)
			if err != nil {
				return fmt.Errorf("rule %s seq %d: civil date: %w", rule.id, seq, err)
			}

			if occDate.LegacyUnix() > horizonUnix {
				break
			}
			if rule.endDateCivil.Valid {
				if occDate.Civil() > rule.endDateCivil.String {
					break
				}
			}

			var dueDateRef OptionalCivilWriteDate
			if rule.isBusiness && rule.dueDateOffsetDays.Valid {
				dueOcc, dErr := occDate.AddDays(int(rule.dueDateOffsetDays.Int64))
				if dErr != nil {
					return fmt.Errorf("rule %s seq %d: due date calculation: %w", rule.id, seq, dErr)
				}
				dueDateRef = MustOptionalCivilWriteDate(dueOcc.Civil())
			}

			expected := recurringOccurrenceExpectation{
				RuleID:    rule.id,
				AccountID: rule.accountID,
				Type:      rule.trType,
				Date:      occDate,
				DueDate:   dueDateRef,
			}
			exists, err := h.seriesSequenceValidateTx(tx, workspaceID, seriesID, seq, expected)
			if err != nil {
				return err
			}
			if exists {
				_, skipped, blocked, err := recurringOccurrenceTerminalConflictsTx(tx, workspaceID, seriesID, seq)
				if err != nil {
					return err
				}
				if skipped || blocked {
					return fmt.Errorf("%w: recurring transaction contradicts terminal tombstone", errInvoiceStateStructural)
				}
				continue
			}
			skipped, err := recurringOccurrenceSkippedTx(tx, workspaceID, seriesID, seq)
			if err != nil {
				return err
			}
			if skipped {
				_, _, blocked, err := recurringOccurrenceTerminalConflictsTx(tx, workspaceID, seriesID, seq)
				if err != nil {
					return err
				}
				if blocked {
					return fmt.Errorf("%w: manual skip contradicts frozen block", errInvoiceStateStructural)
				}
				continue
			}
			blockExpected := recurringOccurrenceBlockExpectation{
				WorkspaceID:        workspaceID,
				SeriesID:           seriesID,
				RecurringRuleID:    rule.id,
				RecurrenceSequence: seq,
				OccurrenceDate:     occDate,
				AccountID:          rule.accountID,
			}
			blocked, err := resolveRecurringOccurrenceBlockTx(tx, blockExpected)
			if err != nil {
				return err
			}
			if blocked {
				continue
			}

			if _, err := h.insertMaterializedOccurrenceTx(tx, workspaceID, firstUserID, seriesID, rule, seq, occDate, now, accountTypeCache, blockExpected); err != nil {
				return err
			}
		}

		if err := execOneTx(tx, `
			UPDATE recurring_rules
			SET generated_until = ?, generated_until_civil = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, horizon.LegacyUnix(), horizon.Civil(), now, rule.id, workspaceID); err != nil {
			return err
		}
	}

	return nil
}

func recurringOccurrenceSkippedTx(tx *sql.Tx, workspaceID, seriesID string, sequence int64) (bool, error) {
	if tx == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(seriesID) == "" || sequence <= 0 {
		return false, fmt.Errorf("consulta de exclusao recorrente invalida")
	}
	var exists int
	if err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM recurring_occurrence_skips
			WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
		)
	`, workspaceID, seriesID, sequence).Scan(&exists); err != nil {
		return false, fmt.Errorf("consultar exclusao recorrente: %w", err)
	}
	return exists == 1, nil
}

func insertRecurringOccurrenceSkipTx(tx *sql.Tx, workspaceID, seriesID string, sequence int64, originalTransactionID, deletedBy string, deletedAt int64) error {
	if tx == nil || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(seriesID) == "" || sequence <= 0 {
		return fmt.Errorf("registro de exclusao recorrente invalido")
	}
	var seriesExists int
	if err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM recurring_series WHERE workspace_id = ? AND id = ?
		)
	`, workspaceID, seriesID).Scan(&seriesExists); err != nil {
		return fmt.Errorf("validar serie recorrente: %w", err)
	}
	if seriesExists != 1 {
		return fmt.Errorf("serie recorrente invalida")
	}

	var deletedByValue interface{}
	if strings.TrimSpace(deletedBy) != "" {
		deletedByValue = deletedBy
	}
	if _, err := tx.Exec(`
		INSERT INTO recurring_occurrence_skips (
			workspace_id, series_id, recurrence_sequence,
			original_transaction_id, reason, deleted_by, deleted_at
		) VALUES (?, ?, ?, ?, 'single_delete', ?, ?)
		ON CONFLICT(workspace_id, series_id, recurrence_sequence) DO NOTHING
	`, workspaceID, seriesID, sequence, originalTransactionID, deletedByValue, deletedAt); err != nil {
		return fmt.Errorf("registrar exclusao recorrente: %w", err)
	}

	var reason string
	if err := tx.QueryRow(`
		SELECT reason
		FROM recurring_occurrence_skips
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
	`, workspaceID, seriesID, sequence).Scan(&reason); err != nil {
		return fmt.Errorf("confirmar exclusao recorrente: %w", err)
	}
	if reason != "single_delete" {
		return fmt.Errorf("exclusao recorrente existente incompativel")
	}
	return nil
}

// recurringOccurrenceExpectation carries the expected content for a materialized
// occurrence at a given (workspace_id, series_id, recurrence_sequence) triple.
type recurringOccurrenceExpectation struct {
	RuleID    string
	AccountID string
	Type      string
	Date      CivilWriteDate
	DueDate   OptionalCivilWriteDate
}

func (h *TransactionHandler) seriesSequenceValidateTx(tx *sql.Tx, workspaceID, seriesID string, seq int64, expected recurringOccurrenceExpectation) (bool, error) {
	type existingRow struct {
		recurringRuleID sql.NullString
		trType          sql.NullString
		status          sql.NullString
		date            sql.NullInt64
		dateCivil       sql.NullString
		dueDate         sql.NullInt64
		dueDateCivil    sql.NullString
		accountID       sql.NullString
		invoiceID       sql.NullString
		isException     sql.NullInt64
	}

	var rows []existingRow
	r, err := tx.Query(`
		SELECT recurring_rule_id, type, status, date, date_civil,
		       due_date, due_date_civil, account_id, invoice_id,
		       is_recurring_exception
		FROM transactions
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
	`, workspaceID, seriesID, seq)
	if err != nil {
		return false, fmt.Errorf("validate sequence %d: %w", seq, err)
	}
	defer r.Close()

	for r.Next() {
		var e existingRow
		if scanErr := r.Scan(&e.recurringRuleID, &e.trType, &e.status, &e.date, &e.dateCivil,
			&e.dueDate, &e.dueDateCivil, &e.accountID, &e.invoiceID, &e.isException); scanErr != nil {
			return false, fmt.Errorf("validate sequence %d: %w", seq, scanErr)
		}
		rows = append(rows, e)
	}
	if err := r.Err(); err != nil {
		return false, fmt.Errorf("validate sequence %d: %w", seq, err)
	}

	if len(rows) == 0 {
		return false, nil
	}
	if len(rows) > 1 {
		return false, fmt.Errorf("duplicate series sequence %d in workspace %s series %s", seq, workspaceID, seriesID)
	}
	existing := rows[0]

	isException := existing.isException.Valid && existing.isException.Int64 != 0

	if err := h.validateSequenceExistingRow(tx, existing, seq, workspaceID, expected, isException); err != nil {
		return false, err
	}

	return true, nil
}

func (h *TransactionHandler) validateSequenceExistingRow(tx *sql.Tx, existing struct {
	recurringRuleID sql.NullString
	trType          sql.NullString
	status          sql.NullString
	date            sql.NullInt64
	dateCivil       sql.NullString
	dueDate         sql.NullInt64
	dueDateCivil    sql.NullString
	accountID       sql.NullString
	invoiceID       sql.NullString
	isException     sql.NullInt64
}, seq int64, workspaceID string, expected recurringOccurrenceExpectation, isException bool) error {

	// --- recurring_rule_id (common + exception) ---
	if !existing.recurringRuleID.Valid || existing.recurringRuleID.String == "" {
		return fmt.Errorf("null recurring_rule_id at seq %d", seq)
	}
	if existing.recurringRuleID.String != expected.RuleID {
		return fmt.Errorf("divergent recurring_rule_id at seq %d: got %q want %q", seq, existing.recurringRuleID.String, expected.RuleID)
	}

	// --- invoice validation (common + exception) ---
	if existing.invoiceID.Valid {
		if !existing.trType.Valid || existing.trType.String != models.TransactionTypeExpense {
			return fmt.Errorf("invoice on non-expense at seq %d: type=%q", seq, existing.trType.String)
		}
		if err := validateInvoiceBelongsToAccount(tx, existing.invoiceID.String, existing.accountID, workspaceID); err != nil {
			return fmt.Errorf("seq %d: %w", seq, err)
		}
		var invStatus string
		if err := tx.QueryRow(`SELECT status FROM invoices WHERE id = ?`, existing.invoiceID.String).Scan(&invStatus); err != nil {
			return fmt.Errorf("seq %d: invoice %s not found: %w", seq, existing.invoiceID.String, err)
		}
		if invStatus != "OPEN" && invStatus != "CLOSED" && invStatus != "PAID" {
			return fmt.Errorf("seq %d: invalid invoice status %q", seq, invStatus)
		}
		if !existing.status.Valid || existing.status.String != models.TransactionStatusPaid {
			return fmt.Errorf("seq %d: invoice-linked transaction must be paid, got %q", seq, existing.status.String)
		}
	}

	// --- due_date integer integrity (common + exception) ---
	if existing.dueDate.Valid && existing.dueDate.Int64 <= 0 {
		return fmt.Errorf("invalid due_date at seq %d: %d (must be positive or NULL)", seq, existing.dueDate.Int64)
	}

	// A sequência 1 é o lançamento-base da série. Em dados legados ela pode
	// preservar escolhas locais (competência, conta, tipo e vencimento) diferentes
	// da regra que passou a governar as ocorrências seguintes. Trate-a como uma
	// exceção controlada sem alterar o registro; identidade, integridade civil e
	// invariantes de fatura continuam obrigatórias. Sequências 2+ seguem estritas.
	allowControlledDivergence := isException || seq == 1
	if allowControlledDivergence {
		if seq == 1 && !isException {
			if !existing.accountID.Valid || strings.TrimSpace(existing.accountID.String) == "" {
				return fmt.Errorf("null account_id at seq %d", seq)
			}
			var accountExists int
			if err := tx.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM accounts WHERE id = ? AND workspace_id = ?
				)
			`, existing.accountID.String, workspaceID).Scan(&accountExists); err != nil {
				return fmt.Errorf("validate account_id at seq %d: %w", seq, err)
			}
			if accountExists != 1 {
				return fmt.Errorf("account_id outside workspace at seq %d", seq)
			}
		}
		if !existing.date.Valid || existing.date.Int64 == 0 {
			return fmt.Errorf("null date in controlled occurrence at seq %d", seq)
		}
		if existing.dateCivil.Valid {
			if _, parseErr := ParseCivilDateInput(existing.dateCivil.String); parseErr != nil {
				return fmt.Errorf("invalid date_civil in controlled occurrence at seq %d: %w", seq, parseErr)
			}
		}
		derivedFromLegacy := civildate.LegacyUnixToCivilDateUTC(existing.date.Int64).String()
		if existing.dateCivil.Valid && existing.dateCivil.String != derivedFromLegacy {
			return fmt.Errorf("date/date_civil mismatch at seq %d: date→%q date_civil=%q", seq, derivedFromLegacy, existing.dateCivil.String)
		}
		if existing.dueDateCivil.Valid {
			if _, parseErr := ParseCivilDateInput(existing.dueDateCivil.String); parseErr != nil {
				return fmt.Errorf("invalid due_date_civil in controlled occurrence at seq %d: %w", seq, parseErr)
			}
			if !existing.dueDate.Valid || existing.dueDate.Int64 <= 0 {
				return fmt.Errorf("due_date NULL or invalid with due_date_civil SET in controlled occurrence at seq %d: due=%v", seq, existing.dueDate)
			}
			derivedDue := civildate.LegacyUnixToCivilDateUTC(existing.dueDate.Int64).String()
			if existing.dueDateCivil.String != derivedDue {
				return fmt.Errorf("due_date/due_date_civil mismatch at seq %d: due→%q due_civil=%q", seq, derivedDue, existing.dueDateCivil.String)
			}
		}
		return nil
	}

	// --- date / date_civil ---
	if !existing.date.Valid || existing.date.Int64 == 0 {
		return fmt.Errorf("null date at seq %d", seq)
	}
	if existing.dateCivil.Valid {
		if _, parseErr := ParseCivilDateInput(existing.dateCivil.String); parseErr != nil {
			return fmt.Errorf("invalid date_civil at seq %d: %w", seq, parseErr)
		}
		derivedFromLegacy := civildate.LegacyUnixToCivilDateUTC(existing.date.Int64).String()
		if existing.dateCivil.String != derivedFromLegacy {
			return fmt.Errorf("date/date_civil mismatch at seq %d: date→%q date_civil=%q", seq, derivedFromLegacy, existing.dateCivil.String)
		}
		if existing.dateCivil.String != expected.Date.Civil() {
			return fmt.Errorf("divergent date at seq %d: got %q want %q", seq, existing.dateCivil.String, expected.Date.Civil())
		}
	} else {
		derived := civildate.LegacyUnixToCivilDateUTC(existing.date.Int64).String()
		if derived != expected.Date.Civil() {
			return fmt.Errorf("divergent date at seq %d: got %q want %q", seq, derived, expected.Date.Civil())
		}
	}

	// --- due_date / due_date_civil ---
	if existing.dueDateCivil.Valid && !existing.dueDate.Valid {
		return fmt.Errorf("due_date NULL with due_date_civil SET at seq %d", seq)
	}
	expectDue := expected.DueDate.Valid()
	expectDueCivil := ""
	if expectDue {
		expectDueCivil = expected.DueDate.CivilValue().(string)
	}
	existingHasDue := existing.dueDate.Valid && existing.dueDate.Int64 != 0
	existingHasDueCivil := existing.dueDateCivil.Valid
	existingDueCivil := ""
	if existingHasDueCivil {
		if _, parseErr := ParseCivilDateInput(existing.dueDateCivil.String); parseErr != nil {
			return fmt.Errorf("invalid due_date_civil at seq %d: %w", seq, parseErr)
		}
		existingDueCivil = existing.dueDateCivil.String
		if !existingHasDue {
			return fmt.Errorf("due_date NULL with due_date_civil SET at seq %d", seq)
		}
		derivedFromDue := civildate.LegacyUnixToCivilDateUTC(existing.dueDate.Int64).String()
		if existingDueCivil != derivedFromDue {
			return fmt.Errorf("due_date/due_date_civil mismatch at seq %d: due→%q due_civil=%q", seq, derivedFromDue, existingDueCivil)
		}
	} else if existingHasDue {
		existingDueCivil = civildate.LegacyUnixToCivilDateUTC(existing.dueDate.Int64).String()
	}
	if expectDue != (existingHasDue || existingHasDueCivil) {
		if expectDue {
			return fmt.Errorf("divergent due_date at seq %d: got NULL want %q", seq, expectDueCivil)
		}
		return fmt.Errorf("divergent due_date at seq %d: got %q want NULL", seq, existingDueCivil)
	}
	if expectDue && existingDueCivil != expectDueCivil {
		return fmt.Errorf("divergent due_date at seq %d: got %q want %q", seq, existingDueCivil, expectDueCivil)
	}

	// --- common-row account_id check ---
	if existing.accountID.Valid && existing.accountID.String != expected.AccountID {
		return fmt.Errorf("divergent account_id at seq %d: got %q want %q", seq, existing.accountID.String, expected.AccountID)
	}

	// --- common-row type check ---
	if !existing.trType.Valid || existing.trType.String != expected.Type {
		return fmt.Errorf("divergent type at seq %d: got %q want %q", seq, existing.trType.String, expected.Type)
	}

	return nil
}

func validateInvoiceBelongsToAccount(tx *sql.Tx, invoiceID string, accountID sql.NullString, workspaceID string) error {
	if !accountID.Valid {
		return fmt.Errorf("invoice %s on null account", invoiceID)
	}
	var accType, accWorkspaceID string
	if err := tx.QueryRow(`SELECT type, workspace_id FROM accounts WHERE id = ?`, accountID.String).Scan(&accType, &accWorkspaceID); err != nil {
		return fmt.Errorf("account %s not found for invoice %s: %w", accountID.String, invoiceID, err)
	}
	if accType != models.AccountTypeCreditCard {
		return fmt.Errorf("invoice %s on non-credit-card account %s (type=%s)", invoiceID, accountID.String, accType)
	}
	if accWorkspaceID != workspaceID {
		return fmt.Errorf("invoice %s: account %s belongs to workspace %s, not %s", invoiceID, accountID.String, accWorkspaceID, workspaceID)
	}
	var invAccountID string
	if err := tx.QueryRow(`SELECT account_id FROM invoices WHERE id = ?`, invoiceID).Scan(&invAccountID); err != nil {
		return fmt.Errorf("invoice %s not found: %w", invoiceID, err)
	}
	if invAccountID != accountID.String {
		return fmt.Errorf("invoice %s belongs to account %s, not transaction account %s", invoiceID, invAccountID, accountID.String)
	}
	var invAccWorkspaceID string
	if err := tx.QueryRow(`SELECT workspace_id FROM accounts WHERE id = ?`, invAccountID).Scan(&invAccWorkspaceID); err != nil {
		return fmt.Errorf("invoice %s: account %s not found: %w", invoiceID, invAccountID, err)
	}
	if invAccWorkspaceID != workspaceID {
		return fmt.Errorf("invoice %s: invoice account %s belongs to workspace %s, not %s", invoiceID, invAccountID, invAccWorkspaceID, workspaceID)
	}
	return nil
}

func (h *TransactionHandler) insertMaterializedOccurrenceTx(tx *sql.Tx, workspaceID, userID, seriesID string, rule recurringMaterializedRule, seq int64, occDate CivilWriteDate, now int64, accountTypeCache map[string]string, blockExpected recurringOccurrenceBlockExpectation) (bool, error) {
	accountType, ok := accountTypeCache[rule.accountID]
	if !ok {
		if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, rule.accountID, workspaceID).Scan(&accountType); err != nil {
			return false, fmt.Errorf("tipo da conta %s: %w", rule.accountID, err)
		}
		accountTypeCache[rule.accountID] = accountType
	}

	status := projectionStatusForRule(rule.defaultPaymentStatus)
	isCreditCard := accountType == "CREDIT_CARD" && rule.trType == "EXPENSE"
	if isCreditCard {
		status = "paid"
	}

	var invoiceID interface{}
	if isCreditCard {
		evidence, err := resolveAutomaticInvoiceForOccurrenceTx(tx, workspaceID, rule.accountID, occDate)
		if errors.Is(err, errAutomaticInvoiceFrozen) {
			persisted, blockErr := insertRecurringOccurrenceFrozenBlockTx(tx, blockExpected, evidence, now)
			if blockErr != nil {
				return false, blockErr
			}
			return persisted, nil
		}
		if err != nil {
			return false, fmt.Errorf("fatura para recorrencia: %w", err)
		}
		invoiceID = evidence.InvoiceID
	}

	var catID interface{}
	if rule.categoryID.Valid && rule.categoryID.String != "" {
		catID = rule.categoryID.String
	}
	var destID interface{}
	if rule.destinationAccountID.Valid && rule.destinationAccountID.String != "" {
		destID = rule.destinationAccountID.String
	}
	var contactID interface{}
	var dueDateRef interface{}
	var dueDateCivilRef interface{}
	if rule.isBusiness {
		if rule.contactID.Valid && rule.contactID.String != "" {
			contactID = rule.contactID.String
		}
		if rule.dueDateOffsetDays.Valid {
			dueOcc, dErr := occDate.AddDays(int(rule.dueDateOffsetDays.Int64))
			if dErr != nil {
				return false, fmt.Errorf("due date calculation seq %d: %w", seq, dErr)
			}
			dueDateRef = dueOcc.LegacyUnix()
			dueDateCivilRef = dueOcc.Civil()
		}
	}

	id := uuid.NewString()
	_, err := tx.Exec(`
		INSERT INTO transactions (
		    id, workspace_id, user_id, account_id, destination_account_id,
		    category_id, invoice_id, type, amount, date, date_civil, description,
		    installment_number, total_installments, parent_id,
		    recurring_rule_id, recurrence_sequence, series_id, status,
		    due_date, due_date_civil, contact_id,
		    created_at, updated_at
		) VALUES (
		?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		    1, 1, NULL,
		    ?, ?, ?, ?, ?, ?, ?,
		    ?, ?
		)
	`, id, workspaceID, userID, rule.accountID, destID, catID, invoiceID, rule.trType, rule.amount, occDate.LegacyUnix(), occDate.Civil(), rule.description,
		rule.id, seq, seriesID, status, dueDateRef, dueDateCivilRef, contactID,
		now, now)
	if err != nil {
		return false, fmt.Errorf("inserir ocorrencia seq %d: %w", seq, err)
	}
	return false, nil
}

func (h *TransactionHandler) insertRecurringSeriesTx(tx *sql.Tx, seriesID string, totalOccurrences *int64, now int64) error {
	return execOneTx(tx, `
		INSERT INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)
	`, seriesID, h.WorkspaceID, h.UserID, totalOccurrences, now, now)
}

func EnsureRecurringSeriesForRuleTx(tx *sql.Tx, workspaceID, recurringRuleID string, now int64) (string, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(recurringRuleID) == "" {
		return "", fmt.Errorf("workspace_id e recurring_rule_id são obrigatórios")
	}

	var existingSeriesID sql.NullString
	var userID sql.NullString
	var active int
	var totalOccurrences sql.NullInt64
	var createdAt sql.NullInt64
	var updatedAt sql.NullInt64
	if err := tx.QueryRow(`
		SELECT series_id, user_id, active, total_occurrences, created_at, updated_at
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, recurringRuleID, workspaceID).Scan(&existingSeriesID, &userID, &active, &totalOccurrences, &createdAt, &updatedAt); err != nil {
		return "", fmt.Errorf("query recurring rule series: %w", err)
	}

	seriesID := strings.TrimSpace(existingSeriesID.String)
	if !existingSeriesID.Valid || seriesID == "" {
		seriesID = recurringRuleID
		created := now
		if createdAt.Valid && createdAt.Int64 > 0 {
			created = createdAt.Int64
		}
		updated := now
		if updatedAt.Valid && updatedAt.Int64 > 0 {
			updated = updatedAt.Int64
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO recurring_series (id, workspace_id, user_id, active, total_occurrences, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, seriesID, workspaceID, nullStringInterface(userID), active, nullInt64Interface(totalOccurrences), created, updated); err != nil {
			return "", fmt.Errorf("insert recurring series for rule: %w", err)
		}
		// Backfill de compatibilidade: preencher series_id NÃO é uma modificação de
		// domínio do usuário, então preserva-se recurring_rules.updated_at (não é bumpado).
		if _, err := tx.Exec(`
			UPDATE recurring_rules
			SET series_id = ?
			WHERE id = ? AND workspace_id = ?
			  AND (series_id IS NULL OR TRIM(series_id) = '')
		`, seriesID, recurringRuleID, workspaceID); err != nil {
			return "", fmt.Errorf("update recurring rule series_id: %w", err)
		}
	}

	// Isolamento multi-tenant: valida a existência da série SOMENTE dentro do workspace
	// informado. Nunca lê workspace_id da série (evita ler metadado de outro tenant) e o
	// erro é genérico, sem expor workspace estrangeiro ou qualquer dado de outro tenant.
	var seriesExistsInWorkspace int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM recurring_series
		WHERE id = ? AND workspace_id = ?
	`, seriesID, workspaceID).Scan(&seriesExistsInWorkspace); err != nil {
		return "", fmt.Errorf("query recurring series: %w", err)
	}
	if seriesExistsInWorkspace == 0 {
		return "", fmt.Errorf("recurring series não encontrada no workspace informado")
	}

	if _, err := tx.Exec(`
		UPDATE transactions
		SET series_id = ?
		WHERE workspace_id = ?
		  AND recurring_rule_id = ?
		  AND (series_id IS NULL OR TRIM(series_id) = '')
	`, seriesID, workspaceID, recurringRuleID); err != nil {
		return "", fmt.Errorf("update recurring transactions series_id: %w", err)
	}

	return seriesID, nil
}

type validatedRecurringRuleContact struct {
	ID         string
	IsBusiness bool
}

func (h *TransactionHandler) insertRecurringRuleTx(tx *sql.Tx, ruleID string, tipo string, amountCents int64, date CivilWriteDate, dueDate OptionalCivilWriteDate, descricao string, origemContaID string, destinoContaID string, categoriaID string, paymentStatus string, recurrenceFrequency string, contact validatedRecurringRuleContact, now int64) (string, error) {
	if err := ensureAccountInWorkspaceTx(tx, origemContaID, h.WorkspaceID); err != nil {
		return "", err
	}
	if err := ensureAccountInWorkspaceTx(tx, destinoContaID, h.WorkspaceID); err != nil {
		return "", err
	}
	if err := ensureCategoryInWorkspaceTx(tx, categoriaID, h.WorkspaceID); err != nil {
		return "", err
	}
	var accType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, origemContaID, h.WorkspaceID).Scan(&accType); err != nil {
		return "", err
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
	seriesID := uuid.NewString()
	if err := h.insertRecurringSeriesTx(tx, seriesID, nil, now); err != nil {
		return "", err
	}
	dueDateOffset := sql.NullInt64{}
	if contact.IsBusiness && dueDate.Valid() {
		dueDateOffset = dueDateOffsetDays(dueDate, date)
	}
	var ruleContactRef interface{}
	if contact.IsBusiness && contact.ID != "" {
		ruleContactRef = contact.ID
	}
	err := execOneTx(tx, `
		INSERT INTO recurring_rules (id, workspace_id, user_id, account_id, destination_account_id, category_id, type, amount, description, start_date, start_date_civil, starts_at, starts_at_civil, due_date_offset_days, contact_id, frequency, default_payment_status, active, series_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
	`, ruleID, h.WorkspaceID, h.UserID, origemContaID, destID, catID, tipo, amountCents, descricao, date.LegacyUnix(), date.Civil(), date.LegacyUnix(), date.Civil(), dueDateOffset, ruleContactRef, recurrenceFrequency, ruleStatus, seriesID, now, now)
	if err != nil {
		return "", err
	}
	return seriesID, nil
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
	if errMes == nil && errAno == nil && mes >= 1 && mes <= 12 && ano >= 2020 && ano <= appTime(time.Now()).Year()+10 {
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
	now := appTime(time.Now())
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
			a.current_balance,
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
	cardLimits := make(map[string]int64)
	for rows.Next() {
		var id, name, accType, providerSlug, color, icon string
		var currentBalance, closingDay, dueDay, creditLimit int64
		if err := rows.Scan(&id, &name, &accType, &providerSlug, &color, &icon, &currentBalance, &closingDay, &dueDay, &creditLimit); err != nil {
			return nil, err
		}

		accIcon := icon
		if accIcon == "" {
			accIcon = accountVisualByProvider(providerSlug, accType)
		}

		fa := FormAccount{
			ID:               id,
			Name:             name,
			Icon:             accIcon,
			Color:            normalizeHexColor(color, "#6B7280"),
			ProviderSlug:     normalizeAccountProviderSlug(providerSlug),
			ProviderMark:     accountProviderMark(providerSlug, name),
			ProviderLogoPath: accountProviderLogoPathForAccount(providerSlug, name),
		}

		if accType == "CREDIT_CARD" {
			fa.Tipo = "cartao"
			fa.ClosingDay = fmt.Sprintf("%d", closingDay)
			fa.DueDay = fmt.Sprintf("%d", dueDay)
			fa.ProviderName = accountProviderName(providerSlug)
			cardLimits[id] = creditLimit
		} else {
			fa.Tipo = "conta"
			fa.BalanceLabel = "Saldo " + formatCurrencyLabel(currentBalance)
		}

		accounts = append(accounts, fa)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for i := range accounts {
		if accounts[i].Tipo == "cartao" {
			accounts[i].CreditLimitLabel = buildCreditCardAvailableLimitSubtitle(h.DB, h.WorkspaceID, accounts[i].ID, cardLimits[accounts[i].ID])
		}
	}

	return accounts, nil
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
			ID:               id,
			Name:             name,
			Icon:             accIcon,
			Color:            normalizeHexColor(color, "#6B7280"),
			ProviderSlug:     normalizeAccountProviderSlug(providerSlug),
			ProviderMark:     accountProviderMark(providerSlug, name),
			ProviderLogoPath: accountProviderLogoPathForAccount(providerSlug, name),
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
	monthStartDate := appMonthStartUTC(time.Now())
	monthStart := monthStartDate.Unix()
	nextMonth := monthStartDate.AddDate(0, 1, 0)
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
				COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) AS spent
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
			COALESCE(mls.spent, 0) AS limit_spent,
			COALESCE(c.parent_id, '') AS parent_id,
			COALESCE(p.name, '') AS parent_name
		FROM categories c
		LEFT JOIN category_box_candidates cbc ON cbc.category_id = c.id AND cbc.box_count = 1
		LEFT JOIN boxes b ON b.id = cbc.box_id AND b.workspace_id = c.workspace_id
		LEFT JOIN cost_limits cl ON cl.category_id = c.id AND cl.workspace_id = c.workspace_id
		LEFT JOIN monthly_limit_spend mls ON mls.category_id = c.id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.workspace_id = ?
		ORDER BY c.type DESC, COALESCE(p.name, c.name) COLLATE NOCASE, CASE WHEN COALESCE(c.parent_id, '') = '' THEN 0 ELSE 1 END, c.name COLLATE NOCASE
	`, h.WorkspaceID, h.WorkspaceID, monthStart, monthEnd, h.WorkspaceID, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []FormCategory
	for rows.Next() {
		var c FormCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Icon, &c.Color, &c.Type, &c.BoxID, &c.BoxReservedBalance, &c.BoxName, &c.LimitMax, &c.LimitSpent, &c.ParentID, &c.ParentName); err != nil {
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
		DestinoNome:         "Conta de destino",
		DestinoIcon:         "wallet",
		DestinoColor:        "#6366F1",
		DestinoProviderMark: "",
		TodayCivil:          civildate.CivilDateToday(appTimeLocation()).String(),
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
		opt.ManualEligible = (opt.Status == models.InvoiceStatusOpen || opt.Status == models.InvoiceStatusClosed) && paidActive <= total && !(total > 0 && paidActive >= total)
		if !opt.ManualEligible {
			opt.ManualBlock = "Movimentação bloqueada: esta fatura está integralmente paga."
		}
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
	data.OrigemProviderLogoPath = acc.ProviderLogoPath
}

func applyFormDestination(data *FormTransacaoData, acc FormAccount) {
	data.DestinoContaID = acc.ID
	data.DestinoNome = acc.Name
	data.DestinoIcon = acc.Icon
	data.DestinoColor = acc.Color
	data.DestinoProviderMark = acc.ProviderMark
	data.DestinoProviderLogoPath = acc.ProviderLogoPath
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

func nullInt64Interface(v sql.NullInt64) interface{} {
	if !v.Valid {
		return nil
	}
	return v.Int64
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

func (h *TransactionHandler) removeNewTransactionUpload(relPath string) {
	cleanRel := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relPath)))
	expectedDir := filepath.Join("uploads", h.WorkspaceID)
	if cleanRel == "." || filepath.Dir(cleanRel) != expectedDir || filepath.Base(cleanRel) == "." {
		log.Printf("refusing attachment cleanup outside generated workspace path")
		return
	}
	root, err := filepath.Abs(paths.TransactionUploadsDir(h.WorkspaceID))
	if err != nil {
		log.Printf("resolve attachment cleanup root failed: err=%v", err)
		return
	}
	fullPath, err := filepath.Abs(filepath.Join(root, filepath.Base(cleanRel)))
	if err != nil || filepath.Dir(fullPath) != root {
		log.Printf("refusing attachment cleanup outside upload root")
		return
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove newly-created attachment failed: err=%v", err)
	}
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

type LancamentosStatusSummary struct {
	Key         string
	Label       string
	Icon        string
	Amount      string
	Count       int
	CountLabel  string
	ToneClasses string
	DotClasses  string
}

func sortUnifiedItems(items []UnifiedItem) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].DateUnix < items[j].DateUnix
	})
}

type lancamentosStatusBucket struct {
	amount int64
	count  int
}

func buildLancamentosStatusSummary(transactions []TransactionRow, invoices []InvoiceRow) []LancamentosStatusSummary {
	buckets := map[string]*lancamentosStatusBucket{
		"pago":     {},
		"pendente": {},
		"vencido":  {},
		"agendado": {},
	}
	add := func(key string, amount int64) {
		if key == "" {
			return
		}
		b := buckets[key]
		if b == nil {
			return
		}
		b.amount += amount
		b.count++
	}
	for _, row := range transactions {
		if row.Type != models.TransactionTypeExpense {
			continue
		}
		add(lancamentosTransactionStatusKey(row), row.Amount)
	}
	for _, inv := range invoices {
		add(lancamentosInvoiceStatusKey(inv), inv.Pending)
	}

	return []LancamentosStatusSummary{
		lancamentosStatusSummaryItem("pago", "Pago", "check-circle-2", "text-emerald-700 bg-emerald-500/10 border-emerald-500/20 dark:text-emerald-200 dark:bg-emerald-500/15 dark:border-emerald-400/20", "bg-emerald-500", buckets["pago"]),
		lancamentosStatusSummaryItem("pendente", "Pendente", "clock", "text-amber-700 bg-amber-500/10 border-amber-500/20 dark:text-amber-200 dark:bg-amber-500/15 dark:border-amber-400/20", "bg-amber-500", buckets["pendente"]),
		lancamentosStatusSummaryItem("vencido", "Vencido", "triangle-alert", "text-red-700 bg-red-500/10 border-red-500/20 dark:text-red-200 dark:bg-red-500/15 dark:border-red-400/20", "bg-red-500", buckets["vencido"]),
		lancamentosStatusSummaryItem("agendado", "Agendado", "timer", "text-violet-700 bg-violet-500/10 border-violet-500/20 dark:text-violet-200 dark:bg-violet-500/15 dark:border-violet-400/20", "bg-violet-500", buckets["agendado"]),
	}
}

func lancamentosStatusSummaryItem(key, label, icon, toneClasses, dotClasses string, b *lancamentosStatusBucket) LancamentosStatusSummary {
	count := 0
	var amount int64
	if b != nil {
		count = b.count
		amount = b.amount
	}
	countLabel := fmt.Sprintf("%d itens", count)
	if count == 1 {
		countLabel = "1 item"
	}
	return LancamentosStatusSummary{
		Key:         key,
		Label:       label,
		Icon:        icon,
		Amount:      formatCurrencyLabel(amount),
		Count:       count,
		CountLabel:  countLabel,
		ToneClasses: toneClasses,
		DotClasses:  dotClasses,
	}
}

func lancamentosTransactionStatusKey(row TransactionRow) string {
	switch {
	case row.PaymentStatus == models.PaymentStatusPaid:
		return "pago"
	case row.IsScheduled:
		return "agendado"
	case row.IsOverdue:
		return "vencido"
	case row.IsPending:
		return "pendente"
	default:
		return ""
	}
}

func lancamentosInvoiceStatusKey(inv InvoiceRow) string {
	switch {
	case inv.Pending <= 0:
		return "pago"
	case inv.IsOverdue:
		return "vencido"
	default:
		return "pendente"
	}
}

type DashboardData struct {
	AppShellAccess
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
	ID               string
	Name             string
	Money            MoneyDisplay
	TrendPercent     string
	TrendDirection   string
	Icon             string
	Color            string
	ProviderSlug     string
	ProviderMark     string
	ProviderLogoPath string
	TypeLabel        string
}

type CreditCardCard struct {
	ID                    string
	InvoiceID             string
	Name                  string
	DueDay                string
	DueDateUnix           int64
	DueDateCompact        string
	ClosingDaysLabel      string
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
	ProviderLogoPath      string
	CanAttemptPayment     bool
	CanSubmitPayment      bool
	PaymentDisabledReason string
	// Both the Dashboard and invoice hub supply a card/competence URL.
	// Only the hub supplies selection state and a partial-content URL.
	Href           string
	ContentURL     string
	IsFaturasIndex bool
	IsSelected     bool
}

// SortDashboardCardsByPaymentPriority keeps the card rail focused on what
// needs attention first. It changes only presentation order: overdue payable
// invoices, upcoming payable invoices, payable invoices without a due date,
// then paid or zero-value invoices.
func SortDashboardCardsByPaymentPriority(cards []CreditCardCard, now time.Time) {
	today := AppCivilDateUTC(now)
	sort.SliceStable(cards, func(i, j int) bool {
		left, right := cards[i], cards[j]
		leftPriority := dashboardCardPaymentPriority(left, today)
		rightPriority := dashboardCardPaymentPriority(right, today)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if leftPriority <= 1 && left.DueDateUnix != right.DueDateUnix {
			return left.DueDateUnix < right.DueDateUnix
		}
		if left.Amount != right.Amount {
			return left.Amount > right.Amount
		}
		return false
	})
}

func dashboardCardPaymentPriority(card CreditCardCard, today time.Time) int {
	if card.Status == models.InvoiceStatusPaid || card.Amount <= 0 {
		return 3
	}
	if card.DueDateUnix <= 0 {
		return 2
	}
	due := time.Unix(card.DueDateUnix, 0).UTC()
	dueDate := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
	if dueDate.Before(today) {
		return 0
	}
	return 1
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
	now := appTime(time.Now())
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
	data.AppShellAccess = QueryAppShellAccess(db, workspaceID, userID, data.IsBusiness)
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
	data.Balance.TrendPercent, data.Balance.TrendDirection = queryBalanceTrend(db, workspaceID, totalBalance, now)
	data.Balance.IsNegative = totalBalance < 0
	data.Health = queryDashboardHealth(db, workspaceID, totalBalance, data.IsBusiness, now)
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
		type accRaw struct {
			id, name, accType, providerSlug, color, icon string
			balance                                      int64
		}
		var rawAccounts []accRaw
		for rows.Next() {
			var r accRaw
			if err := rows.Scan(&r.id, &r.name, &r.balance, &r.accType, &r.providerSlug, &r.color, &r.icon); err != nil {
				continue
			}
			rawAccounts = append(rawAccounts, r)
		}
		rows.Close()

		for _, r := range rawAccounts {
			var typeLabel string
			switch strings.ToUpper(r.accType) {
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
			accIcon := r.icon
			if accIcon == "" {
				accIcon = accountVisualByProvider(r.providerSlug, r.accType)
			}
			trendPercent, trendDirection := queryAccountTrend(db, workspaceID, r.id, r.balance, now)
			data.Accounts = append(data.Accounts, AccountCard{
				ID:               r.id,
				Name:             r.name,
				Money:            MoneyMinor(r.balance),
				TrendPercent:     trendPercent,
				TrendDirection:   trendDirection,
				Icon:             accIcon,
				Color:            normalizeHexColor(r.color, "#6B7280"),
				ProviderSlug:     normalizeAccountProviderSlug(r.providerSlug),
				ProviderMark:     accountProviderMark(r.providerSlug, r.name),
				ProviderLogoPath: accountProviderLogoPathForAccount(r.providerSlug, r.name),
				TypeLabel:        typeLabel,
			})
			if r.accType == models.AccountTypeChecking || r.accType == models.AccountTypeSavings {
				data.PaymentAccounts = append(data.PaymentAccounts, AccountCard{
					ID:               r.id,
					Name:             r.name,
					Money:            MoneyMinor(r.balance),
					TrendPercent:     trendPercent,
					TrendDirection:   trendDirection,
					Icon:             accIcon,
					Color:            normalizeHexColor(r.color, "#6B7280"),
					ProviderSlug:     normalizeAccountProviderSlug(r.providerSlug),
					ProviderMark:     accountProviderMark(r.providerSlug, r.name),
					ProviderLogoPath: accountProviderLogoPathForAccount(r.providerSlug, r.name),
					TypeLabel:        typeLabel,
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
			invoiceID, reference, status, closingUnix, dueUnix, err := resolveDashboardInvoice(db, workspaceID, c.id, time.Now().Unix())
			if err != nil {
				continue
			}
			invoiceHubURL, err := faturasIndexURLForReference(c.id, reference, "desc")
			if err != nil {
				log.Printf("build dashboard invoice hub URL error: %v", err)
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

			var dueDateCompact, closingDaysLabel string
			if dueUnix > 0 {
				monthsShort := []string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}
				dueTime := time.Unix(dueUnix, 0)
				dueDateCompact = fmt.Sprintf("%d/%s", dueTime.Day(), monthsShort[dueTime.Month()-1])
			}
			if closingUnix > 0 && status != models.InvoiceStatusPaid {
				now := time.Now().Unix()
				days := (closingUnix - now) / 86400
				if days < 0 {
					closingDaysLabel = "fechada"
				} else if days == 0 {
					closingDaysLabel = "fecha hoje"
				} else if days == 1 {
					closingDaysLabel = "fecha em 1 dia"
				} else {
					closingDaysLabel = fmt.Sprintf("fecha em %d dias", days)
				}
			}

			data.Cards = append(data.Cards, CreditCardCard{
				ID:               c.id,
				InvoiceID:        invoiceID,
				Name:             c.name,
				DueDay:           fmt.Sprintf("%d", c.dueDay),
				DueDateUnix:      dueUnix,
				DueDateCompact:   dueDateCompact,
				ClosingDaysLabel: closingDaysLabel,
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
				ProviderLogoPath: accountProviderLogoPathForAccount(c.providerSlug, c.name),
				Href:             invoiceHubURL,
			})
		}
	}

	return data
}

func queryDashboardLimits(db *sql.DB, workspaceID string, now time.Time) []DashboardLimitCard {
	monthStartDate := appMonthStartUTC(now)
	monthStart := monthStartDate.Unix()
	nextMonth := monthStartDate.AddDate(0, 1, 0)
	monthEnd := nextMonth.Add(-1 * time.Second).Unix()

	rows, err := db.Query(`
		SELECT
			COALESCE(c.name, 'Sem categoria'),
			cl.max_amount_monthly,
			COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) AS spent
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
	monthStartDate := appMonthStartUTC(now)
	monthStart := monthStartDate.Unix()
	monthEnd := monthStartDate.AddDate(0, 1, 0).Unix()

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
	monthStartDate := appMonthStartUTC(now)
	monthStart := monthStartDate.Unix()
	monthEnd := monthStartDate.AddDate(0, 1, 0).Unix()

	var forecastIncome, forecastExpense int64
	_ = db.QueryRow(`
		WITH invoice_current AS (
			SELECT
				i.id,
				i.due_date,
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - `+invoiceGeneralRefundTotalIASQL+` AS total_amount,
				`+invoiceEffectivePaidSQLExpr+` AS effective_paid
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date >= ? AND i.due_date < ?
		)
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
				total_amount - effective_paid AS amount
			FROM invoice_current
			WHERE total_amount - effective_paid > 0
		)
		`, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, monthStart, monthEnd, workspaceID, monthStart, monthEnd).Scan(&forecastIncome, &forecastExpense)
	return forecastIncome, forecastExpense
}

func dashboardCashSummaryMonthLabel(now time.Time) string {
	months := []string{
		"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho",
		"Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro",
	}
	t := appTime(now)
	return fmt.Sprintf("%s/%d", months[int(t.Month())-1], t.Year())
}

func queryDashboardBoxes(db *sql.DB, workspaceID string) []CaixinhaCard {
	rows, err := db.Query(`
		SELECT b.id, b.category_id, b.name, b.target_amount, b.monthly_recharge,
			COALESCE(c.icon, 'piggy-bank'),
			COALESCE(c.color, '#6b7280'),
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS total_balance
		FROM boxes b
		LEFT JOIN categories c ON c.id = b.category_id AND c.workspace_id = b.workspace_id
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
		var categoryID sql.NullString
		var target, monthly, balance int64
		if err := rows.Scan(&card.ID, &categoryID, &card.Name, &target, &monthly, &card.Icon, &card.Color, &balance); err != nil {
			continue
		}
		if categoryID.Valid {
			card.CategoryID = categoryID.String
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
	args := []interface{}{workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, windowEnd, workspaceID, transactionType, windowEnd}
	query := `
		WITH invoice_current AS (
			SELECT
				i.reference,
				a.name AS account_name,
				i.due_date,
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN ` + transactionNetAmountTSQL + ` ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - ` + invoiceGeneralRefundTotalIASQL + ` AS total_amount,
				` + invoiceEffectivePaidSQLExpr + ` AS effective_paid
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date < ?
		)
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
				'Fatura ' || account_name || ' - ' || reference AS description,
				total_amount - effective_paid AS amount,
				due_date AS due_ref
			FROM invoice_current
			WHERE ? = 'EXPENSE'
			  AND total_amount - effective_paid > 0
		)
		ORDER BY due_ref ASC
		LIMIT 10`
	args = append(args, transactionType)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	today := AppCivilDateUTC(now)
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
		WITH invoice_current AS (
			SELECT
				i.id,
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - `+invoiceGeneralRefundTotalIASQL+` AS total_amount,
				`+invoiceEffectivePaidSQLExpr+` AS effective_paid
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date < ?
		)
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
				total_amount - effective_paid AS amount
			FROM invoice_current
			WHERE ? = 'EXPENSE'
			  AND total_amount - effective_paid > 0
		)
		`, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, windowEnd, workspaceID, transactionType, transactionType, windowEnd, transactionType).Scan(&totalAmount, &itemCount)
	if err != nil {
		return MoneyDisplay{}, 0
	}
	return MoneyMinor(totalAmount), itemCount
}

func queryDashboardPendingWindowRawTotal(db *sql.DB, workspaceID, transactionType string, now time.Time) int64 {
	var totalAmount int64
	windowEnd := dashboardPendingWindowEnd(now)
	_ = db.QueryRow(`
		WITH invoice_current AS (
			SELECT
				i.id,
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) - `+invoiceGeneralRefundTotalIASQL+` AS total_amount,
				`+invoiceEffectivePaidSQLExpr+` AS effective_paid
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
			  AND a.type = ?
			  AND i.status IN (?, ?)
			  AND i.due_date < ?
		)
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
				total_amount - effective_paid AS amount
			FROM invoice_current
			WHERE ? = 'EXPENSE'
			  AND total_amount - effective_paid > 0
		)
		`, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen, models.InvoiceStatusClosed, windowEnd, workspaceID, transactionType, transactionType, windowEnd, transactionType).Scan(&totalAmount)
	return totalAmount
}

func QueryDashboardPendingPayables7d(db *sql.DB, workspaceID string, now time.Time) []DashboardPayableItem {
	return queryDashboardPendingItems7d(db, workspaceID, models.TransactionTypeExpense, now)
}

func QueryDashboardPendingReceivables7d(db *sql.DB, workspaceID string, now time.Time) []DashboardPayableItem {
	return queryDashboardPendingItems7d(db, workspaceID, models.TransactionTypeIncome, now)
}

func dashboardPendingWindowEnd(now time.Time) int64 {
	todayStart := AppCivilDateUTC(now)
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
	items = append(items, h.scheduledPaymentNotifications()...)
	return len(h.visibleItemsForUser(items))
}

// QueryDashboardNotificationCount returns the visible notification count for
// authenticated app shells rendered outside the handlers package.
func QueryDashboardNotificationCount(db *sql.DB, userID, workspaceID string) int {
	return queryDashboardNotificationCount(db, userID, workspaceID)
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

func parsePaymentDate(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return appDayStartUnix(time.Now()), nil
	}
	parsed, err := parseCivilDateStartUnix(value)
	if err != nil {
		return 0, fmt.Errorf("data de pagamento inválida: %q", value)
	}
	return parsed, nil
}

func parseRequiredPaymentDate(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("data obrigatória")
	}
	return parsePaymentDate(value)
}

func queryBalanceTrend(db *sql.DB, workspaceID string, currentBalance int64, now time.Time) (string, string) {
	currentStart := now.AddDate(0, 0, -30).Unix()
	currentNet := queryRealAccountNetCashFlow(db, workspaceID, currentStart, now.Unix())
	previousClosingBalance := currentBalance - currentNet
	return formatTrendPercent(currentBalance, previousClosingBalance), trendDirectionFromDelta(currentBalance - previousClosingBalance)
}

func queryAccountTrend(db *sql.DB, workspaceID, accountID string, currentBalance int64, now time.Time) (string, string) {
	currentStart := now.AddDate(0, 0, -30).Unix()
	var netFlow int64
	_ = db.QueryRow(`
		SELECT COALESCE(SUM(CASE
			WHEN t.type = 'INCOME' THEN t.amount
			WHEN t.type = 'EXPENSE' THEN -t.amount
			WHEN t.type = 'TRANSFER' AND t.account_id = ? THEN -t.amount
			WHEN t.type = 'TRANSFER' AND t.destination_account_id = ? THEN t.amount
			ELSE 0
		END), 0)
		FROM transactions t
		WHERE t.workspace_id = ?
		  AND t.status = 'paid'
		  AND t.date >= ? AND t.date < ?
		  AND (t.account_id = ? OR t.destination_account_id = ?)
	`, accountID, accountID, workspaceID, currentStart, now.Unix(), accountID, accountID).Scan(&netFlow)

	previousBalance := currentBalance - netFlow
	return formatTrendPercent(currentBalance, previousBalance), trendDirectionFromDelta(currentBalance - previousBalance)
}

func trendDirectionFromDelta(delta int64) string {
	switch {
	case delta > 0:
		return "up"
	case delta < 0:
		return "down"
	default:
		return "flat"
	}
}

func formatTrendPercent(current, previous int64) string {
	delta := current - previous
	if delta == 0 {
		return "0,0%"
	}
	base := absInt64(previous)
	if base < 10 {
		return "novo"
	}
	absDelta := delta
	if absDelta < 0 {
		absDelta = -absDelta
	}
	percentTenths := (absDelta*1000 + base/2) / base
	if percentTenths > 9990 {
		if delta > 0 {
			return ">+999%"
		}
		return "<-999%"
	}
	sign := "+"
	if delta < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d,%d%%", sign, percentTenths/10, percentTenths%10)
}

func queryRealAccountNetCashFlow(db *sql.DB, workspaceID string, startUnix, endUnix int64) int64 {
	query := `
	SELECT COALESCE(SUM(net), 0) FROM (
		SELECT t.amount AS net
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
		  AND t.status = 'paid'
		  AND t.type = 'INCOME'
		  AND t.date >= ? AND t.date < ?

		UNION ALL

		SELECT -t.amount AS net
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
		  AND t.status = 'paid'
		  AND t.type = 'EXPENSE'
		  AND t.date >= ? AND t.date < ?

		UNION ALL

		SELECT -t.amount AS net
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
		  AND t.status = 'paid'
		  AND t.type = 'TRANSFER'
		  AND t.date >= ? AND t.date < ?
		  AND NOT EXISTS (
			SELECT 1 FROM accounts da
			WHERE da.id = t.destination_account_id
			  AND da.workspace_id = t.workspace_id
			  AND da.type != 'CREDIT_CARD'
			  AND da.archived_at IS NULL
		  )

		UNION ALL

		SELECT t.amount AS net
		FROM transactions t
		JOIN accounts a ON a.id = t.destination_account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND a.type != 'CREDIT_CARD' AND a.archived_at IS NULL
		  AND t.status = 'paid'
		  AND t.type = 'TRANSFER'
		  AND t.date >= ? AND t.date < ?
		  AND NOT EXISTS (
			SELECT 1 FROM accounts sa
			WHERE sa.id = t.account_id
			  AND sa.workspace_id = t.workspace_id
			  AND sa.type != 'CREDIT_CARD'
			  AND sa.archived_at IS NULL
		  )
	)`
	var net int64
	_ = db.QueryRow(query,
		workspaceID, startUnix, endUnix,
		workspaceID, startUnix, endUnix,
		workspaceID, startUnix, endUnix,
		workspaceID, startUnix, endUnix,
	).Scan(&net)
	return net
}

func queryDashboardHealth(db *sql.DB, workspaceID string, totalBalance int64, isBusiness bool, now time.Time) DashboardHealthData {
	now = appTime(now)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()

	var income, expense int64
	db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END), 0)
		FROM transactions t
			WHERE t.workspace_id = ?
			  AND t.status = 'paid'
			  AND t.date >= ? AND t.date < ?
			  AND `+excludeInvoicePaymentCompetenceClause("t")+`
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

	today := AppCivilDateUTC(time.Now())
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
	today := AppCivilDateUTC(time.Now())
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
	today := AppCivilDateUTC(time.Now())
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
	firstInstant := time.Unix(firstDate, 0).UTC()
	first := time.Date(firstInstant.Year(), firstInstant.Month(), firstInstant.Day(), 0, 0, 0, 0, time.UTC)
	today := AppCivilDateUTC(time.Now())
	days := int64(today.Sub(first).Hours()/24) + 1
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
	ID                   string
	AccountID            string
	AccountName          string
	CardIcon             string
	CardColor            string
	CardProviderMark     string
	CardProviderLogoPath string
	Reference            string
	ReferenceLabel       string
	Status               string
	DueDate              int64
	DateLabel            string
	Total                int64
	Paid                 int64
	Pending              int64
	TotalMoney           MoneyDisplay
	PaidMoney            MoneyDisplay
	PendingMoney         MoneyDisplay
	BadgeLabel           string
	HasPartialPayment    bool
	IsOverdue            bool
	FaturaURL            string
	ItemCount            int
}

type FilterLabelItem struct {
	Key   string
	Label string
}

type LancamentosData struct {
	AppShellAccess
	OOB                       bool
	Title                     string
	IsBusiness                bool
	ActiveWorkspaceName       string
	UserName                  string
	UserInitials              string
	ProfilePhotoURL           string
	NotificationCount         int
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
	ResumoStatus              []LancamentosStatusSummary
	UnifiedItems              []UnifiedItem
	SortOrder                 string
	StatusPendenteOn          bool
	StatusVencidoOn           bool
	StatusAgendadoOn          bool
	StatusPagoOn              bool
	Transactions              []TransactionRow
	Invoices                  []InvoiceRow
	Filters                   LancamentosFilters
	FilterAccounts            []FormAccount
	FilterCategories          []FormCategory
	PaymentAccounts           []PaymentAccountOption
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
	ID                          string
	Date                        int64
	CreatedAt                   int64
	UpdatedAt                   int64
	ListIndex                   int
	DateLabel                   string
	DisplayDateLabel            string
	CompetenciaDateLabel        string
	UsesDueDate                 bool
	HasDueDate                  bool
	DueDateDiffers              bool
	TimeDisplay                 string
	DateInput                   string
	Description                 string
	Amount                      int64
	AmountDisplay               string
	AmountInput                 string
	AmountClass                 string
	CategoryIcon                string
	CategoryColor               string
	CategoryName                string
	AccountName                 string
	DestinationAccountName      string
	PaymentAccountID            string
	PaymentAccountChangeAllowed bool
	ContactName                 string
	IsBusiness                  bool
	Author                      string
	PaymentStatus               string
	IsPending                   bool
	IsOverdue                   bool
	IsScheduled                 bool
	PaymentIcon                 string
	PaymentTone                 string
	PaymentTitle                string
	PaymentConfirm              string
	PaymentConfirmTitle         string
	Type                        string
	InstallmentNumber           int64
	TotalInstallments           int64
	HasInstallmentInfo          bool
	IsSeries                    bool
	IsProjected                 bool
	CanGenerateReceipt          bool
	HasRefund                   bool
	RefundedAmount              int64
	RefundedAmountDisplay       string
	EffectiveAmountDisplay      string
	RefundDescriptions          string
	PaidAt                      *int64
	PaidAtDisplay               string
	ScheduledPaymentAt          *int64
	ScheduledPaymentAtDisplay   string
	CompetenceDate              int64
	DueDate                     *int64
}

func queryPaymentAccountOptions(db *sql.DB, workspaceID string) ([]PaymentAccountOption, error) {
	rows, err := db.Query(`
		SELECT id, name
		FROM accounts
		WHERE workspace_id = ?
		  AND archived_at IS NULL
		  AND type != ?
		ORDER BY name ASC
	`, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []PaymentAccountOption
	for rows.Next() {
		var account PaymentAccountOption
		if err := rows.Scan(&account.ID, &account.Name); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

func paymentAccountChangeAllowed(trType, accountType, accountID string, invoiceID, destinationAccountID sql.NullString, hasRecurringRule bool) bool {
	// The new-transaction form keeps the origin in its hidden destination field
	// until a transfer is selected. Treat that same-account placeholder as empty;
	// a real cross-account destination still blocks payment-account changes.
	hasDifferentDestination := destinationAccountID.Valid && strings.TrimSpace(destinationAccountID.String) != "" && destinationAccountID.String != accountID
	if hasRecurringRule || invoiceID.Valid || hasDifferentDestination || accountType == models.AccountTypeCreditCard {
		return false
	}
	return trType == models.TransactionTypeExpense || trType == models.TransactionTypeIncome
}

type TransactionVisualStatus struct {
	Icon  string
	Tone  string
	Title string
}

func transactionVisualStatus(trType, paymentStatus string, displayDateUnix int64, scheduledAt *int64) TransactionVisualStatus {
	if paymentStatus == models.PaymentStatusPaid {
		if trType == models.TransactionTypeTransfer {
			return TransactionVisualStatus{Icon: "arrow-left-right", Tone: "success", Title: "Transferência confirmada"}
		}
		if trType == models.TransactionTypeIncome {
			return TransactionVisualStatus{Icon: "check-circle-2", Tone: "success", Title: "Recebido — toque para remover recebimento"}
		}
		return TransactionVisualStatus{Icon: "check-circle-2", Tone: "success", Title: "Pago — toque para remover pagamento"}
	}
	if scheduledAt != nil {
		if trType == models.TransactionTypeTransfer {
			return TransactionVisualStatus{Icon: "arrow-left-right", Tone: "scheduled", Title: "Transferência agendada"}
		}
		if trType == models.TransactionTypeIncome {
			return TransactionVisualStatus{Icon: "timer", Tone: "scheduled", Title: "Agendado — toque para confirmar recebimento ou alterar agendamento"}
		}
		return TransactionVisualStatus{Icon: "timer", Tone: "scheduled", Title: "Agendado — toque para confirmar pagamento ou alterar agendamento"}
	}

	loc := appTimeLocation()
	nowTZ := time.Now().In(loc)
	todayUTC := time.Date(nowTZ.Year(), nowTZ.Month(), nowTZ.Day(), 0, 0, 0, 0, time.UTC)
	t := time.Unix(displayDateUnix, 0).UTC()
	displayDayUTC := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if displayDayUTC.Before(todayUTC) {
		if trType == models.TransactionTypeTransfer {
			return TransactionVisualStatus{Icon: "arrow-left-right", Tone: "danger", Title: "Transferência atrasada"}
		}
		if trType == models.TransactionTypeIncome {
			return TransactionVisualStatus{Icon: "triangle-alert", Tone: "danger", Title: "Atrasado — toque para marcar como recebido"}
		}
		return TransactionVisualStatus{Icon: "triangle-alert", Tone: "danger", Title: "Atrasado — toque para marcar como pago"}
	}
	if trType == models.TransactionTypeTransfer {
		return TransactionVisualStatus{Icon: "arrow-left-right", Tone: "warning", Title: "Transferência pendente"}
	}
	if trType == models.TransactionTypeIncome {
		return TransactionVisualStatus{Icon: "clock", Tone: "warning", Title: "Pendente — toque para marcar como recebido"}
	}
	return TransactionVisualStatus{Icon: "clock", Tone: "warning", Title: "Pendente — toque para marcar como pago"}
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

	now := appTime(time.Now())
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
	var recurrenceSequence sql.NullInt64
	var seriesID sql.NullString
	var invoiceStatus sql.NullString

	tLoad := time.Now()
	err = tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, a.type,
		       t.invoice_id, i.status, t.parent_id, t.recurring_rule_id,
		       t.installment_number, t.total_installments, t.date,
		       t.recurrence_sequence, t.series_id
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&ignoredType, &ignoredAmount, &ignoredAccountID, &ignoredDestAccountID, &ignoredStatus, &ignoredAccType, &invoiceID, &invoiceStatus, &parentID, &recurringRuleID, &installmentNumber, &totalInstallments, &txDate, &recurrenceSequence, &seriesID)
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
	if err := h.ensureTransactionsNotInvoicePaymentsTx(tx, []string{id}); err != nil {
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		log.Printf("check invoice payment transaction before delete error: %v", err)
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao validar transação"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	rootID := id
	if parentID.Valid {
		rootID = parentID.String
	}

	now := time.Now().Unix()

	// Delete scope=future/all em recorrência: caminhos versionados específicos.
	// Future preserva sua semântica auditada; all encerra a série inteira.
	if (scope == "future" || scope == "all") && recurringRuleID.Valid {
		if scope == "future" && (!recurrenceSequence.Valid || recurrenceSequence.Int64 <= 0) {
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Recorrência sem sequência válida"}}`)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var affectedInvoiceIDs []string
		if scope == "future" {
			_, affectedInvoiceIDs, err = h.deleteFutureRecurringSeriesVersionedTx(tx, recurringRuleID.String, recurrenceSequence.Int64, now)
		} else {
			_, affectedInvoiceIDs, err = h.deleteAllRecurringSeriesVersionedTx(tx, id, recurringRuleID.String, now)
		}
		if err != nil {
			log.Printf("delete recurring series error: %v", err)
			if errors.Is(err, errInvoicePaymentTransactionProtected) {
				respondInvoicePaymentTransactionProtected(w)
			} else if errors.Is(err, errInvoiceStateTransitionConflict) {
				respondInvoiceCompositionError(w, err)
			} else if scope == "all" && errors.Is(err, errRecurringDeleteConflict) {
				w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Não é possível excluir toda a recorrência com os dados atuais"}}`)
				w.WriteHeader(http.StatusConflict)
			} else {
				w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao remover recorrências futuras"}}`)
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}
		for _, invID := range dedupeIDs(affectedInvoiceIDs) {
			if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, invID, now); err != nil {
				log.Printf("reconcile invoice after future delete error: %v", err)
				respondInvoiceCompositionError(w, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("commit error: %v", err)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro interno ao consolidar remoção"}}`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if returnInvoiceID != "" {
			if err := h.renderInvoiceContextOOB(w, r, returnInvoiceID, fromSheet); err != nil {
				log.Printf("render invoice ctx oob error: %v", err)
				w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao atualizar fatura visualmente"}}`)
				w.WriteHeader(http.StatusInternalServerError)
			}
			perfStep(reqID, "DeletarTransacao", "renderInvoiceContextOOB", time.Since(t0))
			_ = h.renderLancamentosResumoOOB(w, r)
			perfDBDelta(reqID, "DeletarTransacao", "total", dbB, dbSnap(h.DB))
			perfStep(reqID, "DeletarTransacao", "responseTotal", time.Since(t0))
			return
		}
		w.Header().Set("HX-Trigger", "refreshFinancials")
		if !isLancamentosContext(r) {
			if err := h.renderDashboardOOB(w); err != nil {
				log.Printf("delete dashboard render error: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			_ = h.renderLancamentosResumoOOB(w, r)
		}
		perfDBDelta(reqID, "DeletarTransacao", "total", dbB, dbSnap(h.DB))
		perfStep(reqID, "DeletarTransacao", "responseTotal", time.Since(t0))
		return
	}

	if scope == "single" && recurringRuleID.Valid {
		if !recurrenceSequence.Valid || recurrenceSequence.Int64 <= 0 {
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Recorrência sem sequência válida"}}`)
			w.WriteHeader(http.StatusConflict)
			return
		}
		if invoiceID.Valid {
			status := strings.ToUpper(strings.TrimSpace(invoiceStatus.String))
			if !invoiceStatus.Valid || status == "PAID" {
				w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Este lançamento pertence a uma fatura paga e não pode ser excluído"}}`)
				http.Error(w, "lançamento de fatura protegida", http.StatusConflict)
				return
			}
		}
		resolvedSeriesID, resolveErr := h.resolveRecurringSingleDeleteSeriesTx(tx, id, recurringRuleID.String, seriesID, recurrenceSequence.Int64, now)
		if resolveErr != nil {
			log.Printf("validate single recurring delete error: %v", resolveErr)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Não é possível excluir esta ocorrência recorrente com os dados atuais"}}`)
			w.WriteHeader(http.StatusConflict)
			return
		}
		if err := insertRecurringOccurrenceSkipTx(tx, h.WorkspaceID, resolvedSeriesID, recurrenceSequence.Int64, id, h.UserID, now); err != nil {
			log.Printf("insert recurring occurrence skip error: %v", err)
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao registrar a exclusão permanente"}}`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	selectionSQL := `SELECT t.id
		FROM transactions t
		WHERE t.workspace_id = ? AND t.id = ?`
	args := []interface{}{h.WorkspaceID, id}
	if scope == "future" {
		switch {
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
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, idsToDelete); err != nil {
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(err) {
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Não é possível alterar lançamentos de fatura paga"}}`)
			w.WriteHeader(http.StatusConflict)
			return
		}
		if errors.Is(err, errTransactionMutationTargetInvalid) {
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Transação não encontrada"}}`)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		log.Printf("check invoice history before scoped delete error: %v", err)
		w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Erro ao validar transações"}}`)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

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
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(err) {
			w.Header().Set("HX-Trigger", `{"app:toast-error": {"value": "Não é possível alterar lançamentos de fatura paga"}}`)
			w.WriteHeader(http.StatusConflict)
			return
		}
		if errors.Is(err, errInvoiceStateTransitionConflict) {
			respondInvoiceCompositionError(w, err)
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

func (h *TransactionHandler) resolveRecurringSingleDeleteSeriesTx(tx *sql.Tx, transactionID, ruleID string, currentSeriesID sql.NullString, sequence, now int64) (string, error) {
	if tx == nil || strings.TrimSpace(transactionID) == "" || strings.TrimSpace(ruleID) == "" || sequence <= 0 {
		return "", errRecurringDeleteConflict
	}
	seriesID := strings.TrimSpace(currentSeriesID.String)
	if !currentSeriesID.Valid || seriesID == "" {
		var err error
		seriesID, err = EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, ruleID, now)
		if err != nil {
			return "", fmt.Errorf("%w: legacy series", errRecurringDeleteConflict)
		}
	}

	var seriesCount int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM recurring_series
		WHERE workspace_id = ? AND id = ?
	`, h.WorkspaceID, seriesID).Scan(&seriesCount); err != nil || seriesCount != 1 {
		return "", fmt.Errorf("%w: invalid series", errRecurringDeleteConflict)
	}

	var ruleStart int64
	var ruleEnd sql.NullInt64
	if err := tx.QueryRow(`
		SELECT effective_start_sequence, effective_end_sequence
		FROM recurring_rules
		WHERE workspace_id = ? AND id = ? AND series_id = ?
	`, h.WorkspaceID, ruleID, seriesID).Scan(&ruleStart, &ruleEnd); err != nil {
		return "", fmt.Errorf("%w: invalid rule", errRecurringDeleteConflict)
	}
	if sequence < ruleStart || (ruleEnd.Valid && ruleEnd.Int64 > 0 && sequence > ruleEnd.Int64) {
		return "", fmt.Errorf("%w: sequence outside rule", errRecurringDeleteConflict)
	}

	var occurrenceCount int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
	`, h.WorkspaceID, seriesID, sequence).Scan(&occurrenceCount); err != nil || occurrenceCount != 1 {
		return "", fmt.Errorf("%w: invalid occurrence identity", errRecurringDeleteConflict)
	}
	var selectedCount int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE workspace_id = ? AND id = ? AND series_id = ?
		  AND recurring_rule_id = ? AND recurrence_sequence = ?
	`, h.WorkspaceID, transactionID, seriesID, ruleID, sequence).Scan(&selectedCount); err != nil || selectedCount != 1 {
		return "", fmt.Errorf("%w: selected occurrence mismatch", errRecurringDeleteConflict)
	}
	return seriesID, nil
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
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, invID, now); err != nil {
			return err
		}
	}
	return nil
}

var (
	errInvoiceMoveNotFound   = errors.New("invoice move target not found")
	errInvoiceMoveConflict   = errors.New("invoice move concurrent conflict")
	errInvoiceMoveFinancial  = errors.New("invoice move financial conflict")
	errInvoiceMoveWrongCard  = errors.New("invoice move target belongs to another card")
	errInvoiceMoveWrongScope = errors.New("invoice move requires single scope")
)

type invoiceAssignmentResult struct {
	SourceInvoiceID      string
	DestinationInvoiceID string
	Changed              bool
}

func (h *TransactionHandler) assignTransactionInvoiceTx(tx *sql.Tx, transactionID, destinationInvoiceID, expectedSourceInvoiceID string, expectedUpdatedAt *int64, persistOverride bool, transitionAt int64) (invoiceAssignmentResult, error) {
	var result invoiceAssignmentResult
	if err := h.ensureTransactionsNotInvoicePaymentsTx(tx, []string{transactionID}); err != nil {
		return result, err
	}
	var accountID, transactionType, accountType string
	var currentInvoiceID, currentOverrideID sql.NullString
	var amount, updatedAt int64
	err := tx.QueryRow(`
		SELECT t.account_id, t.type, a.type, t.invoice_id, t.invoice_override_id, t.amount, t.updated_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, transactionID, h.WorkspaceID).Scan(
		&accountID,
		&transactionType,
		&accountType,
		&currentInvoiceID,
		&currentOverrideID,
		&amount,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return result, errInvoiceMoveNotFound
	}
	if err != nil {
		return result, err
	}
	if transactionType != models.TransactionTypeExpense || accountType != models.AccountTypeCreditCard || !currentInvoiceID.Valid || currentInvoiceID.String == "" {
		return result, errInvoiceMoveNotFound
	}
	result.SourceInvoiceID = currentInvoiceID.String
	result.DestinationInvoiceID = destinationInvoiceID

	if expectedSourceInvoiceID != "" && currentInvoiceID.String != expectedSourceInvoiceID {
		return result, errInvoiceMoveConflict
	}
	if expectedUpdatedAt != nil && updatedAt != *expectedUpdatedAt {
		return result, errInvoiceMoveConflict
	}

	source, err := loadInvoiceFinancialSnapshotTx(tx, h.WorkspaceID, currentInvoiceID.String)
	if err == sql.ErrNoRows {
		return result, errInvoiceMoveNotFound
	}
	if err != nil {
		return result, err
	}
	if source.AccountID != accountID {
		return result, errInvoiceMoveFinancial
	}
	if source.Status == models.InvoiceStatusPaid {
		return result, errInvoiceMoveFinancial
	}
	if source.Status != models.InvoiceStatusOpen && source.Status != models.InvoiceStatusClosed {
		return result, errInvoiceMoveFinancial
	}
	if currentInvoiceID.String != destinationInvoiceID {
		newSourceTotal := source.Total - amount
		if newSourceTotal < 0 || source.EffectivePaid > newSourceTotal {
			return result, errInvoiceMoveFinancial
		}
	}

	destination := source
	if currentInvoiceID.String != destinationInvoiceID {
		destination, err = loadInvoiceFinancialSnapshotTx(tx, h.WorkspaceID, destinationInvoiceID)
		if err == sql.ErrNoRows {
			return result, errInvoiceMoveNotFound
		}
		if err != nil {
			return result, err
		}
	}
	if destination.AccountID != accountID {
		return result, errInvoiceMoveWrongCard
	}
	if destination.Status != models.InvoiceStatusOpen && destination.Status != models.InvoiceStatusClosed && destination.Status != models.InvoiceStatusPaid {
		return result, fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
	}
	if destination.blocksIncomingTransaction() {
		return result, errInvoiceMoveFinancial
	}

	desiredOverride := sql.NullString{}
	if persistOverride {
		desiredOverride = sql.NullString{String: destinationInvoiceID, Valid: true}
	}
	if currentInvoiceID.String == destinationInvoiceID && currentOverrideID.Valid == desiredOverride.Valid && (!desiredOverride.Valid || currentOverrideID.String == desiredOverride.String) {
		return result, nil
	}

	nextUpdatedAt := time.Now().Unix()
	if nextUpdatedAt <= updatedAt {
		nextUpdatedAt = updatedAt + 1
	}
	var overrideRef interface{}
	if desiredOverride.Valid {
		overrideRef = desiredOverride.String
	}
	updateResult, err := tx.Exec(`
		UPDATE transactions
		SET invoice_id = ?, invoice_override_id = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND invoice_id IS ? AND updated_at = ?
	`, destinationInvoiceID, overrideRef, nextUpdatedAt, transactionID, h.WorkspaceID, currentInvoiceID.String, updatedAt)
	if err != nil {
		return result, err
	}
	affected, err := updateResult.RowsAffected()
	if err != nil {
		return result, err
	}
	if affected != 1 {
		return result, errInvoiceMoveConflict
	}
	result.Changed = true

	if h.testHooks != nil && h.testHooks.afterInvoiceMove != nil {
		if err := h.testHooks.afterInvoiceMove(tx); err != nil {
			return result, err
		}
	}

	if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, currentInvoiceID.String, transitionAt); err != nil {
		return result, err
	}
	if currentInvoiceID.String != destinationInvoiceID {
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, destinationInvoiceID, transitionAt); err != nil {
			return result, err
		}
	}
	return result, nil
}

func respondInvoiceAssignmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvoicePaymentTransactionProtected):
		respondInvoicePaymentTransactionProtected(w)
	case errors.Is(err, errInvoiceMoveNotFound):
		http.Error(w, "lançamento ou fatura não encontrados", http.StatusNotFound)
	case errors.Is(err, errInvoiceMoveWrongCard):
		http.Error(w, "a fatura selecionada não pertence ao mesmo cartão", http.StatusConflict)
	case errors.Is(err, errInvoiceMoveFinancial):
		http.Error(w, "movimentação bloqueada por estado financeiro da fatura", http.StatusConflict)
	case errors.Is(err, errInvoiceStateTransitionConflict):
		http.Error(w, "movimentação bloqueada por estado financeiro da fatura", http.StatusConflict)
	case errors.Is(err, errInvoiceMoveConflict):
		http.Error(w, "a movimentação ficou obsoleta; recarregue e tente novamente", http.StatusConflict)
	case errors.Is(err, errInvoiceMoveWrongScope):
		http.Error(w, "a movimentação manual de fatura exige apenas este lançamento", http.StatusConflict)
	case recurringAllSQLiteConflict(err):
		http.Error(w, "a movimentação ficou obsoleta; recarregue e tente novamente", http.StatusConflict)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func (h *TransactionHandler) handleExplicitInvoiceAssignment(w http.ResponseWriter, r *http.Request, id, destinationInvoiceID, expectedSourceInvoiceID string, persistOverride bool) {
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	transitionAt := time.Now().Unix()
	result, err := h.assignTransactionInvoiceTx(tx, id, destinationInvoiceID, expectedSourceInvoiceID, nil, persistOverride, transitionAt)
	if err != nil {
		respondInvoiceAssignmentError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		respondInvoiceAssignmentError(w, err)
		return
	}

	renderInvoiceID := result.SourceInvoiceID
	if expectedSourceInvoiceID != "" {
		renderInvoiceID = expectedSourceInvoiceID
	}
	if renderInvoiceID != "" {
		if err := h.renderInvoiceContextOOB(w, r, renderInvoiceID, true); err != nil {
			log.Printf("render invoice after explicit assignment error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	h.respondBroadEditAndCloseSheet(w, r)
}

func (h *TransactionHandler) handleAutomaticInvoiceAssignment(w http.ResponseWriter, r *http.Request, id, expectedSourceInvoiceID string) {
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var accountID string
	var invoiceID sql.NullString
	var transactionType, accountType string
	var transactionDate, updatedAt int64
	if err := tx.QueryRow(`
		SELECT t.account_id, t.invoice_id, t.type, a.type, t.date, t.updated_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&accountID, &invoiceID, &transactionType, &accountType, &transactionDate, &updatedAt); err != nil {
		respondInvoiceAssignmentError(w, errInvoiceMoveNotFound)
		return
	}
	if transactionType != models.TransactionTypeExpense || accountType != models.AccountTypeCreditCard || !invoiceID.Valid {
		respondInvoiceAssignmentError(w, errInvoiceMoveNotFound)
		return
	}
	if expectedSourceInvoiceID != "" && invoiceID.String != expectedSourceInvoiceID {
		respondInvoiceAssignmentError(w, errInvoiceMoveConflict)
		return
	}
	destinationInvoiceID, _, _, _, _, err := resolveAutomaticCardTransactionInvoiceTx(tx, h.WorkspaceID, accountID, transactionDate, "auto")
	if err != nil {
		respondInvoiceAssignmentError(w, err)
		return
	}
	transitionAt := time.Now().Unix()
	result, err := h.assignTransactionInvoiceTx(tx, id, destinationInvoiceID, invoiceID.String, &updatedAt, false, transitionAt)
	if err != nil {
		respondInvoiceAssignmentError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		respondInvoiceAssignmentError(w, err)
		return
	}
	renderInvoiceID := result.SourceInvoiceID
	if expectedSourceInvoiceID != "" {
		renderInvoiceID = expectedSourceInvoiceID
	}
	if err := h.renderInvoiceContextOOB(w, r, renderInvoiceID, true); err != nil {
		log.Printf("render invoice after automatic assignment error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
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
	expectedInvoiceID := strings.TrimSpace(r.FormValue("expected_invoice_id"))
	expectedDestinationReference := strings.TrimSpace(r.FormValue("expected_destination_reference"))
	expectedMoveScope := strings.TrimSpace(r.FormValue("expected_move_scope"))
	expectedUpdatedAtRaw := strings.TrimSpace(r.FormValue("expected_updated_at"))
	expectedUpdatedAt, parseErr := strconv.ParseInt(expectedUpdatedAtRaw, 10, 64)
	if expectedInvoiceID == "" || expectedDestinationReference == "" || expectedMoveScope == "" || parseErr != nil || expectedUpdatedAt <= 0 {
		http.Error(w, "estado esperado da movimentação ausente", http.StatusBadRequest)
		return
	}
	if current.invoiceID != expectedInvoiceID || current.updatedAt != expectedUpdatedAt {
		respondInvoiceAssignmentError(w, errInvoiceMoveConflict)
		return
	}
	nextReference, nextReferenceErr := nextInvoiceReference(current.reference)
	if nextReferenceErr != nil || nextReference != expectedDestinationReference {
		respondInvoiceAssignmentError(w, errInvoiceMoveConflict)
		return
	}
	currentMoveScope := "single"
	if current.parentID.Valid || current.recurringRuleID.Valid || current.totalInstallments > 1 {
		currentMoveScope = "chain"
	}
	if currentMoveScope != expectedMoveScope {
		respondInvoiceAssignmentError(w, errInvoiceMoveConflict)
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

	transitionAt := time.Now().Unix()
	for _, item := range items {
		nextInvoiceID, err := ensureInvoiceAfterReferenceTx(tx, h.WorkspaceID, item.accountID, item.reference)
		if err != nil {
			log.Printf("ensure next invoice error: %v", err)
			http.Error(w, "erro ao localizar próxima fatura", http.StatusInternalServerError)
			return
		}
		if _, err := h.assignTransactionInvoiceTx(tx, item.id, nextInvoiceID, item.invoiceID, &item.updatedAt, true, transitionAt); err != nil {
			log.Printf("move invoice update error: %v", err)
			respondInvoiceAssignmentError(w, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("move invoice commit error: %v", err)
		respondInvoiceAssignmentError(w, err)
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
	updatedAt         int64
}

type invoiceMoveItem struct {
	id        string
	accountID string
	invoiceID string
	reference string
	updatedAt int64
}

func (h *TransactionHandler) fetchInvoiceMoveCandidateTx(tx *sql.Tx, id string) (invoiceMoveCandidate, error) {
	var item invoiceMoveCandidate
	err := tx.QueryRow(`
		SELECT t.id, t.account_id, t.invoice_id, i.reference, i.status,
		       t.parent_id, t.recurring_rule_id, t.total_installments, t.date, t.updated_at
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
		&item.updatedAt,
	)
	return item, err
}

func (h *TransactionHandler) invoiceMoveQueueTx(tx *sql.Tx, current invoiceMoveCandidate) ([]invoiceMoveItem, error) {
	if !current.parentID.Valid && !current.recurringRuleID.Valid && current.totalInstallments <= 1 {
		return []invoiceMoveItem{{id: current.id, accountID: current.accountID, invoiceID: current.invoiceID, reference: current.reference, updatedAt: current.updatedAt}}, nil
	}

	var rows *sql.Rows
	var err error
	if current.recurringRuleID.Valid {
		rows, err = tx.Query(`
			SELECT t.id, t.account_id, t.invoice_id, i.reference, t.updated_at
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
			SELECT t.id, t.account_id, t.invoice_id, i.reference, t.updated_at
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
		if err := rows.Scan(&item.id, &item.accountID, &item.invoiceID, &item.reference, &item.updatedAt); err != nil {
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
	SortDashboardCardsByPaymentPriority(dashboardData.Cards, time.Now())
	for _, templateName := range dashboardOOBTemplateNames(h.Templates) {
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
	SortDashboardCardsByPaymentPriority(data.Cards, time.Now())
	var buf bytes.Buffer
	for _, templateName := range dashboardOOBTemplateNames(h.Templates) {
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

func dashboardOOBTemplateNames(templates TemplateEngine) []string {
	if templates.Lookup("dashboard-canonical-content") != nil {
		return []string{"dashboard-canonical-content"}
	}
	return []string{"dashboard-summary", "dashboard-financial", "dashboard-balance", "dashboard-accounts", "dashboard-cards", "dashboard-health-cards"}
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
		data.CanCreateContact = h.CanCreateContact
	}
	data.IsEdit = true
	data.EditID = id
	data.TipoInicial = formTypeFromTransactionType(row.Type)
	data.ValorPreenchido = "R$ " + row.AmountInput
	data.DescricaoPreenchida = row.Description
	data.StatusInicial = status

	var accountID string
	var destinationAccountID sql.NullString
	var categoryID sql.NullString
	var totalInstallments int64
	var recurringRuleID sql.NullString
	var recurrenceFrequency sql.NullString
	var invoiceID sql.NullString
	var invoiceReference sql.NullString
	var invoiceOverrideID sql.NullString
	var notes string
	var attachmentPath string
	var dueDate sql.NullInt64
	var contactID sql.NullString
	var updatedAt int64
	var authorName string
	var txDate int64
	var paidAt sql.NullInt64
	var scheduledPaymentAt sql.NullInt64
	if err := h.DB.QueryRow(`
		SELECT t.account_id, t.destination_account_id, t.category_id, t.total_installments, t.recurring_rule_id, rr.frequency, t.invoice_id, i.reference, t.invoice_override_id, t.notes, t.attachment_path, t.due_date, t.contact_id, t.updated_at, COALESCE(u.name, 'Usuário removido'), t.date, t.paid_at, t.scheduled_payment_at
		FROM transactions t
		LEFT JOIN users u ON u.id = t.user_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		LEFT JOIN invoices i ON i.id = t.invoice_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&accountID, &destinationAccountID, &categoryID, &totalInstallments, &recurringRuleID, &recurrenceFrequency, &invoiceID, &invoiceReference, &invoiceOverrideID, &notes, &attachmentPath, &dueDate, &contactID, &updatedAt, &authorName, &txDate, &paidAt, &scheduledPaymentAt); err == nil {
		data.AnotacoesPreenchidas = notes
		if attachmentPath != "" {
			data.AttachmentViewURL = "/transacoes/" + id + "/anexo"
			data.AttachmentDownloadURL = "/transacoes/" + id + "/anexo?download=1"
		}
		data.AuditCreatedBy = authorName
		data.AuditUpdatedAt = formatDateTimeLabel(updatedAt)
		data.TransactionUpdatedAt = updatedAt
		data.QuickMoveScope = "single"
		if recurringRuleID.Valid || totalInstallments > 1 {
			data.QuickMoveScope = "chain"
		}
		if invoiceReference.Valid {
			data.QuickMoveDestinationRef, _ = nextInvoiceReference(invoiceReference.String)
		}
		data.InvoiceAssignmentMode = "preserve"
		if invoiceOverrideID.Valid && invoiceOverrideID.String != "" {
			data.InvoiceAssignmentMode = "manual"
			data.ManualInvoiceID = invoiceOverrideID.String
		}
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

		data.DataPreenchida = time.Unix(txDate, 0).Format("2006-01-02")
		data.CompetenciaDisplay = formatDateLabelFromUnix(txDate)
		if dueDate.Valid {
			data.VencimentoDisplay = formatDateLabelFromUnix(dueDate.Int64)
		}
		if paidAt.Valid {
			data.PaidAtDisplay = formatAppDateLabelFromUnix(paidAt.Int64)
		}
		if scheduledPaymentAt.Valid {
			data.ScheduledPaymentDisplay = formatAppDateLabelFromUnix(scheduledPaymentAt.Int64)
		}

		data.StatusHistory = h.loadTransactionStatusHistory(id)
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
		IssueDateLabel:    appTime(time.Now()).Format("02/01/2006"),
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
	assignmentMode := strings.ToLower(strings.TrimSpace(r.FormValue("invoice_assignment_mode")))
	requestedInvoiceID := strings.TrimSpace(r.FormValue("invoice_id"))
	returnInvoiceID := strings.TrimSpace(r.FormValue("return_invoice_id"))
	if assignmentMode != "" && assignmentMode != "preserve" && assignmentMode != "manual" && assignmentMode != "auto" {
		http.Error(w, "modo de fatura inválido", http.StatusBadRequest)
		return
	}
	manualAssignment := assignmentMode == "manual" || (assignmentMode == "" && requestedInvoiceID != "")
	if manualAssignment {
		if scope != "single" {
			respondInvoiceAssignmentError(w, errInvoiceMoveWrongScope)
			return
		}
		if requestedInvoiceID == "" {
			http.Error(w, "fatura de destino obrigatória", http.StatusBadRequest)
			return
		}
		h.handleExplicitInvoiceAssignment(w, r, id, requestedInvoiceID, returnInvoiceID, true)
		return
	}
	if assignmentMode == "auto" {
		if scope != "single" {
			respondInvoiceAssignmentError(w, errInvoiceMoveWrongScope)
			return
		}
		h.handleAutomaticInvoiceAssignment(w, r, id, returnInvoiceID)
		return
	}

	valorStr := r.FormValue("valor")
	descricao := r.FormValue("descricao")
	noteValues, notesProvided := r.Form["anotacoes"]
	rawNotes := ""
	if notesProvided && len(noteValues) > 0 {
		rawNotes = noteValues[len(noteValues)-1]
	}
	anotacoes := strings.TrimSpace(rawNotes)
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
	_, dueDateProvided := r.Form["due_date"]
	_, contactProvided := r.Form["contact_id"]
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	dueRaw := ""
	if dueDateProvided {
		dueRaw = strings.TrimSpace(r.FormValue("due_date"))
	}
	var dueDateWrite OptionalCivilWriteDate
	dueDateCleared := false
	if isBusiness && newStatus != "paid" && dueDateProvided {
		if dueRaw == "" {
			dueDateCleared = true
		} else {
			parsedDueDate, err := NewOptionalCivilWriteDate(dueRaw)
			if err != nil {
				http.Error(w, "data de vencimento invalida", http.StatusBadRequest)
				return
			}
			dueDateWrite = parsedDueDate
		}
	}
	dueDateLegacy := dueDateWrite.LegacyNull()
	dueDateCivil := dueDateWrite.CivilNull()
	dueDateUnix := dueDateLegacy.Int64
	hasDueDate := dueDateWrite.Valid()
	contactID := strings.TrimSpace(r.FormValue("contact_id"))
	if !isBusiness {
		contactID = ""
	}

	newAmount, err := parseCurrency(valorStr)
	if err != nil || newAmount <= 0 {
		http.Error(w, "valor inválido", http.StatusBadRequest)
		return
	}

	civilDate, err := NewCivilWriteDate(dataStr)
	if err != nil {
		http.Error(w, "data inválida", http.StatusBadRequest)
		return
	}
	newDate := civilDate.LegacyUnix()
	newDateCivil := civilDate.Civil()

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

	// 4B-B1a: scope=future preflight runs inside the SAME tx as writes.
	if scope == "future" {
		preflight, pfErr := h.loadRecurringFuturePreflightTx(tx, id)
		if pfErr != nil {
			if errors.Is(pfErr, errRecurringFutureSplitBlocked) {
				log.Printf("recurring future preflight blocked: %v", pfErr)
			} else {
				log.Printf("recurring future preflight error: %v", pfErr)
			}
			http.Error(w, "não é possível alterar recorrencias futuras protegidas", http.StatusConflict)
			return
		}
		if preflight != nil {
			if err := validateRecurringFuturePreflight(preflight); err != nil {
				if errors.Is(err, errRecurringFutureSplitBlocked) {
					http.Error(w, "não é possível alterar recorrencias futuras protegidas", http.StatusConflict)
					return
				}
				log.Printf("recurring future preflight validation: %v", err)
				http.Error(w, "erro ao validar recorrencias futuras", http.StatusInternalServerError)
				return
			}
			if h.testHooks != nil && h.testHooks.afterPreflight != nil {
				h.testHooks.afterPreflight("future")
			}
		}
	}
	defer tx.Rollback()

	var oldTrType, oldPaymentStatus string
	var oldAmount int64
	var oldAccountID string
	var oldDestAccountID sql.NullString
	var oldCategoryID sql.NullString
	var invoiceID sql.NullString
	var invoiceOverrideID sql.NullString
	var parentID sql.NullString
	var recurringRuleID sql.NullString
	var oldSeriesID sql.NullString
	var oldRecurrenceSequence sql.NullInt64
	var installmentNumber, totalInstallments int64
	var oldAccType string
	var oldAttachmentPath string
	var oldDateUnix int64
	var oldDateCivil sql.NullString
	var oldDueDate sql.NullInt64
	var oldDueDateCivil sql.NullString
	var oldContactID sql.NullString
	var oldNotes string

	err = tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, t.category_id, t.invoice_id, t.invoice_override_id, t.parent_id, t.recurring_rule_id, t.series_id, t.recurrence_sequence, t.installment_number, t.total_installments, a.type, t.attachment_path, t.date, t.date_civil, t.due_date, t.due_date_civil, t.contact_id, t.notes
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&oldTrType, &oldAmount, &oldAccountID, &oldDestAccountID, &oldPaymentStatus, &oldCategoryID, &invoiceID, &invoiceOverrideID, &parentID, &recurringRuleID, &oldSeriesID, &oldRecurrenceSequence, &installmentNumber, &totalInstallments, &oldAccType, &oldAttachmentPath, &oldDateUnix, &oldDateCivil, &oldDueDate, &oldDueDateCivil, &oldContactID, &oldNotes)
	if err != nil {
		http.Error(w, "transação não encontrada", http.StatusNotFound)
		return
	}
	oldDateCivilValue, err := validatedPersistedCivilDate(oldDateCivil, oldDateUnix)
	if err != nil {
		http.Error(w, "data civil persistida inválida", http.StatusConflict)
		return
	}
	oldDueDateCivilValue, err := validatedPersistedOptionalCivilDate(oldDueDateCivil, oldDueDate)
	if err != nil {
		http.Error(w, "data civil persistida inválida", http.StatusConflict)
		return
	}

	scheduleFormPayment := false
	paymentIntentChanged := oldPaymentStatus != models.PaymentStatusPaid ||
		oldDateCivilValue != newDateCivil || oldTrType != tipo || oldAccountID != origemContaID
	if paymentIntentChanged && !invoiceID.Valid {
		var requestedAccountType string
		if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND archived_at IS NULL`, origemContaID, h.WorkspaceID).Scan(&requestedAccountType); err == nil {
			scheduleFormPayment = shouldScheduleFutureFormPayment(civilDate, newStatus, tipo, requestedAccountType)
			if scheduleFormPayment {
				newStatus = models.PaymentStatusPending
			}
		}
	}
	if scheduleFormPayment && totalInstallments > 1 && scope != "single" && !recurringRuleID.Valid {
		http.Error(w, "para agendar, aplique a alteração apenas a este lançamento", http.StatusUnprocessableEntity)
		return
	}

	if isBusiness && fixo && !recurringRuleID.Valid && !dueDateProvided && newStatus != "paid" {
		// PRESERVE: derive dueDateWrite from persisted transaction pair.
		// Must validate full pair coherence before creating rule/projection.

		if oldDueDate.Valid && oldDueDate.Int64 <= 0 {
			http.Error(w, "vencimento persistido inválido para conversão em recorrência", http.StatusConflict)
			return
		}

		if oldDueDateCivil.Valid {
			// Civil column explicitly populated — takes priority.
			if !oldDueDate.Valid {
				http.Error(w, "par inconsistente de vencimento para conversão em recorrência", http.StatusConflict)
				return
			}
			derivedFromLegacy := civildate.LegacyUnixToCivilDateUTC(oldDueDate.Int64).String()
			if oldDueDateCivil.String != derivedFromLegacy {
				http.Error(w, "par divergente de vencimento para conversão em recorrência", http.StatusConflict)
				return
			}
			resolvedDue, parseErr := NewOptionalCivilWriteDate(oldDueDateCivilValue.String)
			if parseErr != nil {
				http.Error(w, "data de vencimento persistida inválida", http.StatusConflict)
				return
			}
			dueDateWrite = resolvedDue
		} else if oldDueDate.Valid && oldDueDate.Int64 > 0 {
			// Legacy: positive integer with NULL civil. Convert without backfill.
			resolvedDue := MustOptionalCivilWriteDate(civildate.LegacyUnixToCivilDateUTC(oldDueDate.Int64).String())
			dueDateWrite = resolvedDue
		}
		// Else: both NULL — valid absence, dueDateWrite stays empty.

		dueDateLegacy = dueDateWrite.LegacyNull()
		dueDateCivil = dueDateWrite.CivilNull()
		dueDateUnix = dueDateLegacy.Int64
		hasDueDate = dueDateWrite.Valid()
	}

	dateChanged := oldDateCivilValue != newDateCivil
	dueDateChanged := false
	if dueDateProvided && isBusiness && newStatus != "paid" {
		switch {
		case dueDateCleared:
			dueDateChanged = oldDueDate.Valid || oldDueDateCivil.Valid
		case hasDueDate:
			dueDateChanged = !oldDueDateCivilValue.Valid || oldDueDateCivilValue.String != dueDateCivil.String
		}
	}
	wasRecurringOccurrence := recurringRuleID.Valid
	if returnInvoiceID != "" && (!invoiceID.Valid || invoiceID.String != returnInvoiceID) {
		http.Error(w, "fatura não autorizada ou não encontrada", http.StatusNotFound)
		return
	}
	var mutationGuardErr error
	if scope == "all" && recurringRuleID.Valid {
		// Recurrence scope=all preserves protected occurrences and validates only
		// its selected mutableIDs in updateAllRecurringSeries4BB2Tx. C0-A still
		// protects the selected row before that specialized preflight.
		mutationGuardErr = h.ensureTransactionsNotInvoicePaymentsTx(tx, []string{id})
	} else {
		mutationGuardErr = h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{id})
	}
	if mutationGuardErr != nil {
		if errors.Is(mutationGuardErr, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(mutationGuardErr) {
			http.Error(w, "não é possível alterar lançamento de fatura paga", http.StatusConflict)
			return
		}
		if errors.Is(mutationGuardErr, errTransactionMutationTargetInvalid) {
			http.Error(w, "transação não encontrada", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var activeRefunds int64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM transaction_refunds
		WHERE workspace_id = ? AND transaction_id = ? AND reversed_at IS NULL
	`, h.WorkspaceID, id).Scan(&activeRefunds); err != nil {
		http.Error(w, "erro ao validar reembolsos do lançamento", http.StatusInternalServerError)
		return
	}
	if activeRefunds > 0 {
		var requestedAccountType string
		if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, origemContaID, h.WorkspaceID).Scan(&requestedAccountType); err != nil {
			http.Error(w, "conta de origem inválida", http.StatusUnprocessableEntity)
			return
		}
		if tipo != models.TransactionTypeExpense || requestedAccountType != models.AccountTypeCreditCard {
			http.Error(w, "lançamento com reembolso deve permanecer como despesa de cartão", http.StatusConflict)
			return
		}
		if newAmount < activeRefunds {
			http.Error(w, "valor do lançamento não pode ser menor que os reembolsos registrados", http.StatusConflict)
			return
		}
	}
	now := time.Now().Unix()
	var futureDueIntent futureOptionalCivilIntent
	if scope == "future" && recurringRuleID.Valid {
		futureDueIntent, err = parseFutureOptionalCivilIntent(isBusiness, dueDateProvided, dueRaw)
		if err != nil {
			http.Error(w, "data de vencimento invalida", http.StatusBadRequest)
			return
		}
		if !oldRecurrenceSequence.Valid || oldRecurrenceSequence.Int64 <= 0 {
			http.Error(w, "recorrencia sem sequencia valida", http.StatusConflict)
			return
		}
		if err := h.validateRecurringFutureCivilIntentTx(tx, recurringRuleID.String, oldSeriesID.String, oldRecurrenceSequence.Int64, oldDateCivilValue, civilDate, futureDueIntent, isBusiness); err != nil {
			if errors.Is(err, errRecurringFutureCivilConflict) {
				http.Error(w, "não é possível preservar o vencimento após alterar a competência", http.StatusConflict)
				return
			}
			log.Printf("validate future recurring civil intent error: %v", err)
			http.Error(w, "erro ao validar vencimento da recorrencia", http.StatusInternalServerError)
			return
		}
	}
	// 4B-B2: recurring scope=all has one dedicated, series-aware path. It runs
	// before compatibility backfill and upload persistence so every decision is
	// read-only until the global preflight succeeds.
	if scope == "all" && recurringRuleID.Valid {
		if !oldRecurrenceSequence.Valid || oldRecurrenceSequence.Int64 <= 0 {
			http.Error(w, "recorrência sem sequência válida", http.StatusConflict)
			return
		}
		touchedIDs, oldInvoiceIDs, allErr := h.updateAllRecurringSeries4BB2Tx(tx, recurringAll4BB2Params{
			CurrentID:         id,
			Type:              tipo,
			Amount:            newAmount,
			Date:              civilDate,
			Description:       descricao,
			RawNotes:          rawNotes,
			NotesProvided:     notesProvided,
			SelectedNotes:     oldNotes,
			HasUpload:         recurringAllRequestHasUpload(r),
			AccountID:         origemContaID,
			DestinationID:     destinoContaID,
			CategoryID:        categoriaID,
			Status:            newStatus,
			Frequency:         recorrencia,
			DueRaw:            dueRaw,
			DueProvided:       dueDateProvided,
			ContactID:         contactID,
			ContactProvided:   contactProvided,
			IsBusiness:        isBusiness,
			Now:               now,
			AllowBoxOverdraft: allowBoxOverdraft,
		})
		if allErr != nil {
			switch {
			case errors.Is(allErr, errInvoicePaymentTransactionProtected):
				respondInvoicePaymentTransactionProtected(w)
			case invoiceStatusMutationBlocked(allErr):
				http.Error(w, "não é possível alterar recorrências protegidas", http.StatusConflict)
			case errors.Is(allErr, errRecurringAllIndividualOnly):
				http.Error(w, "notas e anexos devem ser alterados usando apenas este lançamento", http.StatusConflict)
			case errors.Is(allErr, errRecurringAllConflict), recurringAllSQLiteConflict(allErr):
				http.Error(w, "não é possível alterar toda a recorrência com os dados atuais", http.StatusConflict)
			case errors.Is(allErr, errBoxReserveInsufficient):
				http.Error(w, "saldo reservado insuficiente na reserva vinculada; confirme excedente para continuar", http.StatusUnprocessableEntity)
			case errors.Is(allErr, errBoxCategoryAmbiguous):
				http.Error(w, "há mais de uma reserva vinculada para a categoria informada", http.StatusUnprocessableEntity)
			default:
				log.Printf("update recurring all 4B-B2 error: %v", allErr)
				http.Error(w, "erro ao atualizar série", http.StatusInternalServerError)
			}
			return
		}
		if scheduleFormPayment {
			if err := h.scheduleFormPaymentTx(tx, id, civilDate, now); err != nil {
				http.Error(w, "erro ao agendar lançamento", http.StatusInternalServerError)
				return
			}
		}
		if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, touchedIDs, now); err != nil {
			respondInvoiceCompositionError(w, err)
			return
		}
		for _, oldInvID := range dedupeIDs(oldInvoiceIDs) {
			if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, oldInvID, now); err != nil {
				respondInvoiceCompositionError(w, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			if recurringAllSQLiteConflict(err) {
				http.Error(w, "não é possível alterar toda a recorrência com os dados atuais", http.StatusConflict)
				return
			}
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if returnInvoiceID != "" {
			if err := h.renderInvoiceContextOOB(w, r, returnInvoiceID, true); err != nil {
				log.Printf("render invoice after recurring all 4B-B2 error: %v", err)
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
	if recurringRuleID.Valid && scope != "future" {
		seriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, recurringRuleID.String, now)
		if err != nil {
			log.Printf("ensure recurring series error: %v", err)
			http.Error(w, "erro ao preparar recorrencia", http.StatusInternalServerError)
			return
		}
		oldSeriesID = sql.NullString{String: seriesID, Valid: true}
	}

	attachmentPath, err := h.persistTransactionUpload(r)
	if err != nil {
		http.Error(w, "arquivo inválido ou não permitido", http.StatusBadRequest)
		return
	}
	newAttachmentPath := attachmentPath
	keepNewUpload := false
	if newAttachmentPath != "" {
		defer func() {
			if !keepNewUpload {
				h.removeNewTransactionUpload(newAttachmentPath)
			}
		}()
	}
	if attachmentPath == "" {
		attachmentPath = oldAttachmentPath
	}
	// scope=future em recorrência (Fase 4): caminho VERSIONADO. Faz split da regra em
	// current_sequence, cria nova regra versionada (parent_rule_id + mesma series_id) e
	// move apenas as ocorrências mutáveis. Intercepta ANTES do fluxo legado abaixo.
	// scope=all versionado (Fase 5) é interceptado a seguir; este bloco só trata future.
	if scope == "future" && recurringRuleID.Valid {
		if !oldRecurrenceSequence.Valid || oldRecurrenceSequence.Int64 <= 0 {
			http.Error(w, "recorrencia sem sequencia valida", http.StatusConflict)
			return
		}
		_, touchedIDs, oldInvoiceIDs, err := h.splitFutureRecurringRuleTx(tx, recurringFutureSplitParams{
			CurrentID:         id,
			RuleID:            recurringRuleID.String,
			SeriesID:          oldSeriesID.String,
			CurrentSequence:   oldRecurrenceSequence.Int64,
			Type:              tipo,
			Amount:            newAmount,
			Anchor:            civilDate,
			Description:       descricao,
			Notes:             anotacoes,
			AttachmentPath:    attachmentPath,
			AccountID:         origemContaID,
			DestinationID:     destinoContaID,
			CategoryID:        categoriaID,
			Status:            newStatus,
			Frequency:         recorrencia,
			DueDateIntent:     futureDueIntent,
			ContactID:         contactID,
			ContactProvided:   contactProvided,
			IsBusiness:        isBusiness,
			Now:               now,
			AllowBoxOverdraft: allowBoxOverdraft,
		})
		if err != nil {
			if errors.Is(err, errInvoicePaymentTransactionProtected) {
				respondInvoicePaymentTransactionProtected(w)
				return
			}
			if errors.Is(err, errRecurringFutureSplitBlocked) || invoiceStatusMutationBlocked(err) {
				http.Error(w, "não é possível alterar recorrencias futuras protegidas", http.StatusConflict)
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
			log.Printf("split future recurring rule error: %v", err)
			http.Error(w, "erro ao atualizar recorrencias futuras", http.StatusInternalServerError)
			return
		}
		if scheduleFormPayment {
			if err := h.scheduleFormPaymentTx(tx, id, civilDate, now); err != nil {
				http.Error(w, "erro ao agendar lançamento", http.StatusInternalServerError)
				return
			}
		}
		if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, touchedIDs, now); err != nil {
			respondInvoiceCompositionError(w, err)
			return
		}
		for _, oldInvID := range dedupeIDs(oldInvoiceIDs) {
			if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, oldInvID, now); err != nil {
				respondInvoiceCompositionError(w, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		keepNewUpload = true
		if returnInvoiceID != "" {
			if err := h.renderInvoiceContextOOB(w, r, returnInvoiceID, true); err != nil {
				log.Printf("render invoice after future recurring split error: %v", err)
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
				if err := h.updateFutureTransactionSeries(tx, id, parentID, installmentNumber, tipo, newAmount, civilDate, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateWrite, dueDateCleared, dueDateProvided, contactID, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errInvoicePaymentTransactionProtected) {
						respondInvoicePaymentTransactionProtected(w)
						return
					}
					if invoiceStatusMutationBlocked(err) {
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
					if errors.Is(err, errInvoicePaymentTransactionProtected) {
						respondInvoicePaymentTransactionProtected(w)
						return
					}
					if invoiceStatusMutationBlocked(err) {
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
					if errors.Is(err, errInvoicePaymentTransactionProtected) {
						respondInvoicePaymentTransactionProtected(w)
						return
					}
					if invoiceStatusMutationBlocked(err) {
						http.Error(w, "não é possível alterar lançamento de fatura paga", http.StatusConflict)
						return
					}
					log.Printf("update current recurring transaction error: %v", err)
					http.Error(w, "erro ao atualizar", http.StatusInternalServerError)
					return
				}
			}
		case "all":
			// Parcelamentos não recorrentes continuam usando o fluxo próprio. Toda
			// recorrência com scope=all já retornou pelo preflight dedicado acima.
			if totalInstallments > 1 {
				if err := h.updateAllTransactionSeries(tx, id, parentID, tipo, newAmount, civilDate, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDateWrite, dueDateCleared, dueDateProvided, contactID, now, allowBoxOverdraft); err != nil {
					if errors.Is(err, errInvoicePaymentTransactionProtected) {
						respondInvoicePaymentTransactionProtected(w)
						return
					}
					if invoiceStatusMutationBlocked(err) {
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
				SeriesID:             oldSeriesID.String,
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
		if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, []string{id}, now); err != nil {
			respondInvoiceCompositionError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		keepNewUpload = true
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

	if err := ensureContactInWorkspaceTx(tx, contactID, h.WorkspaceID); err != nil {
		http.Error(w, "contato não encontrado", http.StatusBadRequest)
		return
	}

	var newRecurringProjectionRule *recurrenceProjectionRule
	if fixo && !recurringRuleID.Valid {
		ruleID := uuid.NewString()
		seriesID, err := h.insertRecurringRuleTx(tx, ruleID, tipo, newAmount, civilDate, dueDateWrite, descricao, origemContaID, destinoContaID, categoriaID, newStatus, recorrencia, validatedRecurringRuleContact{ID: contactID, IsBusiness: isBusiness}, now)
		if err != nil {
			log.Printf("update transaction series error: %v", err)
			http.Error(w, "erro ao criar recorrencia", http.StatusInternalServerError)
			return
		}
		newRecurringProjectionRule = &recurrenceProjectionRule{
			ID:                   ruleID,
			SeriesID:             seriesID,
			AccountID:            origemContaID,
			DestinationAccountID: destinoContaID,
			CategoryID:           categoriaID,
			Type:                 tipo,
			Amount:               newAmount,
			Description:          descricao,
			StartDate:            newDate,
			StartDateCivil:       newDateCivil,
			Frequency:            recorrencia,
			DefaultPaymentStatus: strings.ToUpper(newStatus),
			HasDueDate:           hasDueDate,
			ContactID:            contactID,
			IsBusiness:           isBusiness,
		}
		if hasDueDate {
			newRecurringProjectionRule.DueDateUnix = dueDateUnix
			newRecurringProjectionRule.DueDateCivil = dueDateCivil.String
			newRecurringProjectionRule.DueDateOffsetDays = dueDateOffsetDays(dueDateWrite, civilDate)
		}
		recurringRuleID = sql.NullString{String: ruleID, Valid: true}
		oldSeriesID = sql.NullString{String: seriesID, Valid: true}
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
	dueDateCivilRef := interface{}(nil)
	dueDateUpdateSet := false
	dueDateUpdateClear := false
	if hasDueDate && dueDateProvided {
		// SET: operator sent a valid date.
		dueDateRef = dueDateUnix
		if dueDateCivil.Valid {
			dueDateCivilRef = dueDateCivil.String
		}
		dueDateUpdateSet = true
	} else if dueDateCleared {
		// CLEAR: operator sent the field explicitly empty.
		dueDateUpdateClear = true
	}
	// else: PRESERVE — field not sent. SQL omits both columns entirely.
	contactRef := interface{}(nil)
	if contactID != "" {
		contactRef = contactID
	} else if wasRecurringOccurrence && isBusiness && !contactProvided && oldContactID.Valid {
		contactRef = oldContactID.String
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
	if invoiceOverrideID.Valid && invoiceOverrideID.String != "" && (tipo != models.TransactionTypeExpense || newAccType != models.AccountTypeCreditCard) {
		http.Error(w, "retorne a fatura ao modo automático antes de alterar o tipo ou a conta", http.StatusConflict)
		return
	}
	if tipo == "EXPENSE" && newAccType == "CREDIT_CARD" {
		if invoiceOverrideID.Valid && invoiceOverrideID.String != "" {
			overrideSnapshot, overrideErr := loadInvoiceFinancialSnapshotTx(tx, h.WorkspaceID, invoiceOverrideID.String)
			if overrideErr != nil || overrideSnapshot.AccountID != origemContaID {
				http.Error(w, "retorne a fatura ao modo automático antes de alterar o cartão", http.StatusConflict)
				return
			}
			invoiceID = invoiceOverrideID
		} else {
			invID, _, _, _, _, err := resolveAutomaticCardTransactionInvoiceTx(tx, h.WorkspaceID, origemContaID, newDate, r.FormValue("fatura_offset"))
			if err != nil {
				if errors.Is(err, errInvoiceStateTransitionConflict) {
					http.Error(w, "estado financeiro da fatura não permite esta alteração", http.StatusConflict)
				} else {
					http.Error(w, "erro na fatura", http.StatusInternalServerError)
				}
				return
			}
			invoiceID = sql.NullString{String: invID, Valid: true}
		}
	}

	recurrenceSeqRef := recurrenceSequence(1, recurringRuleID.Valid)
	if wasRecurringOccurrence && oldRecurrenceSequence.Valid {
		recurrenceSeqRef = oldRecurrenceSequence.Int64
	}
	seriesIDRef := nullStringInterface(oldSeriesID)
	isRecurringException := 0
	var recurringExceptionReason interface{}
	if wasRecurringOccurrence {
		isRecurringException = 1
		recurringExceptionReason = "manual_edit"
	}

	if dueDateUpdateSet || dueDateUpdateClear {
		err = execOneTx(tx, `
			UPDATE transactions
			SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, date_civil = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, due_date = ?, due_date_civil = ?, contact_id = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, recurring_rule_id = ?, recurrence_sequence = ?, series_id = ?, is_recurring_exception = ?, recurring_exception_reason = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, tipo, origemContaID, destID, newAmount, newDate, newDateCivil, descricao, anotacoes, attachmentPath, catID, newStatus, dueDateRef, dueDateCivilRef, contactRef, invoiceID, nullStringInterface(recurringRuleID), recurrenceSeqRef, seriesIDRef, isRecurringException, recurringExceptionReason, now, id, h.WorkspaceID)
	} else {
		err = execOneTx(tx, `
			UPDATE transactions
			SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, date_civil = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, contact_id = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, recurring_rule_id = ?, recurrence_sequence = ?, series_id = ?, is_recurring_exception = ?, recurring_exception_reason = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, tipo, origemContaID, destID, newAmount, newDate, newDateCivil, descricao, anotacoes, attachmentPath, catID, newStatus, contactRef, invoiceID, nullStringInterface(recurringRuleID), recurrenceSeqRef, seriesIDRef, isRecurringException, recurringExceptionReason, now, id, h.WorkspaceID)
	}
	if err != nil {
		http.Error(w, "erro ao atualizar", http.StatusInternalServerError)
		return
	}
	if scheduleFormPayment {
		if err := h.scheduleFormPaymentTx(tx, id, civilDate, now); err != nil {
			http.Error(w, "erro ao agendar lançamento", http.StatusInternalServerError)
			return
		}
	}
	if newRecurringProjectionRule != nil {
		if err := h.generateRecurrenceProjection(tx, *newRecurringProjectionRule, now); err != nil {
			http.Error(w, "erro ao criar recorrencia", http.StatusInternalServerError)
			return
		}
	}

	if newStatus == "paid" {
		if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, newAccType, newStatus, newAmount, origemContaID, destinoContaID, now); err != nil {
			http.Error(w, "erro ao ajustar saldo", http.StatusInternalServerError)
			return
		}
	}

	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, []string{id}, now); err != nil {
		respondInvoiceCompositionError(w, err)
		return
	}
	if oldInvoiceID.Valid && oldInvoiceID.String != "" {
		if err := reconcileInvoiceAfterCompositionMutationTx(tx, h.WorkspaceID, oldInvoiceID.String, now); err != nil {
			respondInvoiceCompositionError(w, err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	keepNewUpload = true

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

	if dateChanged || dueDateChanged {
		h.respondBroadEditAndCloseSheet(w, r)
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

func (h *TransactionHandler) updateFutureTransactionSeries(tx *sql.Tx, currentID string, parentID sql.NullString, fromInstallment int64, tipo string, newAmount int64, date CivilWriteDate, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDate OptionalCivilWriteDate, dueDateCleared bool, dueDateProvided bool, contactID string, now int64, allowBoxOverdraft bool) error {
	if err := date.validate(); err != nil {
		return fmt.Errorf("invalid civil competence date: %w", err)
	}
	if err := dueDate.validate(); err != nil {
		return fmt.Errorf("invalid civil due date: %w", err)
	}
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
	dueDateUpdateSet := false
	dueDateUpdateClear := false
	if dueDate.Valid() {
		dueDateUpdateSet = true
	} else if dueDateCleared {
		dueDateUpdateClear = true
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

		offset := int(old.installmentNumber - fromInstallment)

		installmentDate, err := date.AddMonths(offset)
		if err != nil {
			return fmt.Errorf("updateFutureTransactionSeries: add %d months to competence date: %w", offset, err)
		}
		installmentDueDate, err := dueDate.AddMonths(offset)
		if err != nil {
			return fmt.Errorf("updateFutureTransactionSeries: add %d months to due date: %w", offset, err)
		}
		if err := h.adjustReserveForSingleTransactionUpdateTx(tx, old.id, old.trType, old.categoryID, old.amount, tipo, categoriaID, newAmount, installmentDate.LegacyUnix(), now, allowBoxOverdraft); err != nil {
			return err
		}
		var invoiceID interface{}
		if tipo == "EXPENSE" && newAccType == "CREDIT_CARD" {
			evidence, err := resolveAutomaticInvoiceForOccurrenceTx(tx, h.WorkspaceID, origemContaID, installmentDate)
			if err != nil {
				return fmt.Errorf("invoice: %w", err)
			}
			invoiceID = evidence.InvoiceID
		}

		if dueDateUpdateSet || dueDateUpdateClear {
			if err := execOneTx(tx, `
				UPDATE transactions
					SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, date_civil = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, due_date = ?, due_date_civil = ?, contact_id = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, updated_at = ?
					WHERE id = ? AND workspace_id = ?
				`, tipo, origemContaID, destID, newAmount, installmentDate.LegacyUnix(), installmentDate.Civil(), descricao, anotacoes, attachmentPath, catID, newStatus, installmentDueDate.LegacyValue(), installmentDueDate.CivilValue(), contactRef, invoiceID, now, old.id, h.WorkspaceID); err != nil {
				return fmt.Errorf("update installment: %w", err)
			}
		} else {
			if err := execOneTx(tx, `
				UPDATE transactions
					SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, date_civil = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, contact_id = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, updated_at = ?
					WHERE id = ? AND workspace_id = ?
				`, tipo, origemContaID, destID, newAmount, installmentDate.LegacyUnix(), installmentDate.Civil(), descricao, anotacoes, attachmentPath, catID, newStatus, contactRef, invoiceID, now, old.id, h.WorkspaceID); err != nil {
				return fmt.Errorf("update installment: %w", err)
			}
		}

		if newStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, tipo, newAccType, newStatus, newAmount, origemContaID, destinoContaID, now); err != nil {
				return fmt.Errorf("apply installment balance: %w", err)
			}
		}
	}

	return nil
}

func (h *TransactionHandler) updateAllTransactionSeries(tx *sql.Tx, currentID string, parentID sql.NullString, tipo string, newAmount int64, date CivilWriteDate, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDate OptionalCivilWriteDate, dueDateCleared bool, dueDateProvided bool, contactID string, now int64, allowBoxOverdraft bool) error {
	rootID := currentID
	if parentID.Valid {
		rootID = parentID.String
	}
	return h.updateFutureTransactionSeries(tx, rootID, sql.NullString{String: rootID, Valid: true}, 1, tipo, newAmount, date, descricao, anotacoes, attachmentPath, origemContaID, destinoContaID, categoriaID, newStatus, dueDate, dueDateCleared, dueDateProvided, contactID, now, allowBoxOverdraft)
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

// ---------- 4B-B1a: recurring future preflight (read-only) ----------

type recurringFutureRuleSnapshot struct {
	RuleID                 string
	WorkspaceID            string
	SeriesID               sql.NullString
	Active                 bool
	EffectiveStartSequence int64
	EffectiveEndSequence   sql.NullInt64
}

type recurringFutureRuleIntegrity struct {
	ID                     string
	WorkspaceID            string
	SeriesID               string
	ParentRuleID           sql.NullString
	Active                 int
	EffectiveStartSequence int64
	EffectiveEndSequence   sql.NullInt64
}

type recurringFuturePreflightItem struct {
	TransactionID    string
	WorkspaceID      string
	RuleID           sql.NullString
	SeriesID         sql.NullString
	Sequence         int64
	PaymentStatus    string
	PaidAt           sql.NullInt64
	PaidDateCivil    sql.NullString
	IsException      bool
	InvoiceID        sql.NullString
	HasStatusHistory bool
}

type recurringFuturePreflight struct {
	CurrentTransactionID string
	Rule                 recurringFutureRuleSnapshot
	LogicalSeriesID      string
	NeedsSeriesEnsure    bool
	Items                []recurringFuturePreflightItem
}

func (h *TransactionHandler) loadRecurringFuturePreflightTx(tx *sql.Tx, transactionID string) (*recurringFuturePreflight, error) {
	var (
		ruleID   sql.NullString
		seriesID sql.NullString
		seq      sql.NullInt64
	)
	if err := tx.QueryRow(`
		SELECT recurring_rule_id, series_id, recurrence_sequence
		FROM transactions
		WHERE id = ? AND workspace_id = ?
	`, transactionID, h.WorkspaceID).Scan(&ruleID, &seriesID, &seq); err != nil {
		return nil, fmt.Errorf("preflight: load current transaction: %w", err)
	}
	if !ruleID.Valid {
		return nil, nil
	}
	if !seq.Valid || seq.Int64 <= 0 {
		return nil, fmt.Errorf("preflight: invalid recurrence sequence")
	}
	currentSeq := seq.Int64

	var rule recurringFutureRuleSnapshot
	rule.RuleID = ruleID.String
	rule.WorkspaceID = h.WorkspaceID
	var activeInt int
	if err := tx.QueryRow(`
		SELECT COALESCE(series_id, ''), active, COALESCE(effective_start_sequence, 1), effective_end_sequence
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, ruleID.String, h.WorkspaceID).Scan(&rule.SeriesID, &activeInt, &rule.EffectiveStartSequence, &rule.EffectiveEndSequence); err != nil {
		return nil, fmt.Errorf("preflight: load rule: %w", err)
	}
	rule.Active = activeInt == 1
	if !rule.Active {
		return nil, errRecurringFutureSplitBlocked
	}

	logicalSeriesID := rule.SeriesID.String
	needsSeriesEnsure := false
	if logicalSeriesID == "" {
		logicalSeriesID = rule.RuleID
		needsSeriesEnsure = true
	}

	ids := map[string]bool{}
	seqs := map[int64]bool{}
	var items []recurringFuturePreflightItem
	currentFound := false

	if needsSeriesEnsure {
		rows, err := tx.Query(`
			SELECT t.id, t.workspace_id, t.recurring_rule_id, t.series_id,
			       t.recurrence_sequence, t.status,
			       t.paid_at, t.paid_date_civil,
			       COALESCE(t.is_recurring_exception, 0),
			       t.invoice_id,
			       EXISTS (
			           SELECT 1
			           FROM transaction_status_history h
			           WHERE h.workspace_id = t.workspace_id
			             AND h.transaction_id = t.id
			       ) AS has_status_history
			FROM transactions t
			WHERE t.workspace_id = ?
			  AND t.recurring_rule_id = ?
			  AND t.recurrence_sequence >= ?
			ORDER BY t.recurrence_sequence ASC
		`, h.WorkspaceID, rule.RuleID, currentSeq)
		if err != nil {
			return nil, fmt.Errorf("preflight: load items: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var it recurringFuturePreflightItem
			if err := rows.Scan(&it.TransactionID, &it.WorkspaceID, &it.RuleID, &it.SeriesID,
				&it.Sequence, &it.PaymentStatus,
				&it.PaidAt, &it.PaidDateCivil,
				&it.IsException, &it.InvoiceID,
				&it.HasStatusHistory); err != nil {
				return nil, fmt.Errorf("preflight: scan item: %w", err)
			}
			if it.WorkspaceID != h.WorkspaceID {
				return nil, fmt.Errorf("preflight: workspace mismatch on item %s", it.TransactionID)
			}
			if ids[it.TransactionID] {
				return nil, fmt.Errorf("preflight: duplicate transaction id %s", it.TransactionID)
			}
			ids[it.TransactionID] = true
			if seqs[it.Sequence] {
				return nil, fmt.Errorf("preflight: duplicate sequence %d", it.Sequence)
			}
			seqs[it.Sequence] = true
			if it.TransactionID == transactionID {
				currentFound = true
			}
			items = append(items, it)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("preflight: close rows: %w", err)
		}

		for _, it := range items {
			if it.RuleID.Valid && it.RuleID.String != rule.RuleID {
				return nil, fmt.Errorf("preflight: legacy item %s has mismatched rule_id", it.TransactionID)
			}
			if it.SeriesID.Valid && it.SeriesID.String != "" && it.SeriesID.String != logicalSeriesID {
				return nil, fmt.Errorf("preflight: legacy item %s has incompatible series_id %q (expected %q or NULL)", it.TransactionID, it.SeriesID.String, logicalSeriesID)
			}
		}
	} else {
		rows1, err := tx.Query(`
			SELECT t.id, t.workspace_id, t.recurring_rule_id, t.series_id,
			       t.recurrence_sequence, t.status,
			       t.paid_at, t.paid_date_civil,
			       COALESCE(t.is_recurring_exception, 0),
			       t.invoice_id,
			       EXISTS (
			           SELECT 1
			           FROM transaction_status_history h
			           WHERE h.workspace_id = t.workspace_id
			             AND h.transaction_id = t.id
			       ) AS has_status_history
			FROM transactions t
			WHERE t.workspace_id = ?
			  AND t.series_id = ?
			  AND t.recurrence_sequence >= ?
			ORDER BY t.recurrence_sequence ASC
		`, h.WorkspaceID, logicalSeriesID, currentSeq)
		if err != nil {
			return nil, fmt.Errorf("preflight: load by series_id: %w", err)
		}
		defer rows1.Close()

		for rows1.Next() {
			var it recurringFuturePreflightItem
			if err := rows1.Scan(&it.TransactionID, &it.WorkspaceID, &it.RuleID, &it.SeriesID,
				&it.Sequence, &it.PaymentStatus,
				&it.PaidAt, &it.PaidDateCivil,
				&it.IsException, &it.InvoiceID,
				&it.HasStatusHistory); err != nil {
				return nil, fmt.Errorf("preflight: scan item: %w", err)
			}
			if it.WorkspaceID != h.WorkspaceID {
				return nil, fmt.Errorf("preflight: workspace mismatch on item %s", it.TransactionID)
			}
			if ids[it.TransactionID] {
				return nil, fmt.Errorf("preflight: duplicate transaction id %s", it.TransactionID)
			}
			ids[it.TransactionID] = true
			if seqs[it.Sequence] {
				return nil, fmt.Errorf("preflight: duplicate sequence %d", it.Sequence)
			}
			seqs[it.Sequence] = true
			if it.TransactionID == transactionID {
				currentFound = true
			}
			items = append(items, it)
		}
		if err := rows1.Close(); err != nil {
			return nil, fmt.Errorf("preflight: close rows: %w", err)
		}

		rows2, err := tx.Query(`
			SELECT t.id, t.workspace_id, t.recurring_rule_id, t.series_id,
			       t.recurrence_sequence, t.status,
			       t.paid_at, t.paid_date_civil,
			       COALESCE(t.is_recurring_exception, 0),
			       t.invoice_id,
			       EXISTS (
			           SELECT 1
			           FROM transaction_status_history h
			           WHERE h.workspace_id = t.workspace_id
			             AND h.transaction_id = t.id
			       ) AS has_status_history
			FROM transactions t
			WHERE t.workspace_id = ?
			  AND t.recurring_rule_id = ?
			  AND t.recurrence_sequence >= ?
			ORDER BY t.recurrence_sequence ASC
		`, h.WorkspaceID, rule.RuleID, currentSeq)
		if err != nil {
			return nil, fmt.Errorf("preflight: load by rule_id: %w", err)
		}
		defer rows2.Close()

		for rows2.Next() {
			var it recurringFuturePreflightItem
			if err := rows2.Scan(&it.TransactionID, &it.WorkspaceID, &it.RuleID, &it.SeriesID,
				&it.Sequence, &it.PaymentStatus,
				&it.PaidAt, &it.PaidDateCivil,
				&it.IsException, &it.InvoiceID,
				&it.HasStatusHistory); err != nil {
				return nil, fmt.Errorf("preflight: scan item: %w", err)
			}
			if it.WorkspaceID != h.WorkspaceID {
				return nil, fmt.Errorf("preflight: workspace mismatch on item %s", it.TransactionID)
			}
			if ids[it.TransactionID] {
				continue
			}
			ids[it.TransactionID] = true
			if seqs[it.Sequence] {
				return nil, fmt.Errorf("preflight: duplicate sequence %d", it.Sequence)
			}
			seqs[it.Sequence] = true
			if it.TransactionID == transactionID {
				currentFound = true
			}
			items = append(items, it)
		}
		if err := rows2.Close(); err != nil {
			return nil, fmt.Errorf("preflight: close rows: %w", err)
		}

		for _, it := range items {
			if it.SeriesID.Valid && it.SeriesID.String != "" && it.SeriesID.String != logicalSeriesID {
				return nil, fmt.Errorf("preflight: item %s has incompatible series_id %q (expected %q or NULL)", it.TransactionID, it.SeriesID.String, logicalSeriesID)
			}
			if it.SeriesID.Valid && it.SeriesID.String == logicalSeriesID && !it.RuleID.Valid {
				return nil, fmt.Errorf("preflight: item %s has series_id %q but no recurring_rule_id", it.TransactionID, logicalSeriesID)
			}
		}

		ruleMap, validChain, err := loadRecurringFutureRuleChainTx(tx, h.WorkspaceID, logicalSeriesID, rule.RuleID)
		if err != nil {
			return nil, fmt.Errorf("preflight: rule chain: %w", err)
		}
		for _, it := range items {
			if err := validateRecurringFutureItemRule(it, ruleMap, validChain, rule.RuleID); err != nil {
				return nil, fmt.Errorf("preflight: item %s: %w", it.TransactionID, err)
			}
		}
	}

	if !currentFound {
		return nil, fmt.Errorf("preflight: current transaction not found in affected set")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("preflight: no items loaded")
	}

	return &recurringFuturePreflight{
		CurrentTransactionID: transactionID,
		Rule:                 rule,
		LogicalSeriesID:      logicalSeriesID,
		NeedsSeriesEnsure:    needsSeriesEnsure,
		Items:                items,
	}, nil
}

func recurringFuturePreflightItemIsProtected(it recurringFuturePreflightItem) bool {
	if it.PaymentStatus == "paid" {
		return true
	}
	if it.PaidAt.Valid && it.PaidAt.Int64 > 0 {
		return true
	}
	if it.PaidDateCivil.Valid {
		return true
	}
	if it.IsException {
		return true
	}
	if it.InvoiceID.Valid {
		return true
	}
	return false
}

func validateRecurringFuturePreflight(preflight *recurringFuturePreflight) error {
	for _, it := range preflight.Items {
		if !recurringFuturePreflightItemIsProtected(it) {
			continue
		}
		// The selected occurrence may already be an occurrence-local payment or
		// manual exception. It is still safe to edit together with the mutable
		// future tail because the split preserves its payment evidence and the
		// balance move happens atomically. Invoice-backed items remain protected.
		if it.TransactionID == preflight.CurrentTransactionID &&
			!it.InvoiceID.Valid &&
			(strings.EqualFold(strings.TrimSpace(it.PaymentStatus), models.PaymentStatusPaid) || it.IsException) {
			continue
		}
		return errRecurringFutureSplitBlocked
	}
	return nil
}

func loadRecurringFutureRuleChainTx(tx *sql.Tx, workspaceID, seriesID, sourceRuleID string) (map[string]recurringFutureRuleIntegrity, map[string]bool, error) {
	rows, err := tx.Query(`
		SELECT id, workspace_id, COALESCE(series_id, ''), parent_rule_id,
		       active, COALESCE(effective_start_sequence, 1), effective_end_sequence
		FROM recurring_rules
		WHERE workspace_id = ? AND series_id = ?
	`, workspaceID, seriesID)
	if err != nil {
		return nil, nil, fmt.Errorf("load series rules: %w", err)
	}
	defer rows.Close()

	rules := map[string]recurringFutureRuleIntegrity{}
	for rows.Next() {
		var r recurringFutureRuleIntegrity
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.SeriesID, &r.ParentRuleID,
			&r.Active, &r.EffectiveStartSequence, &r.EffectiveEndSequence); err != nil {
			return nil, nil, fmt.Errorf("scan rule: %w", err)
		}
		if r.WorkspaceID != workspaceID {
			return nil, nil, fmt.Errorf("rule %s has mismatched workspace %q", r.ID, r.WorkspaceID)
		}
		if r.SeriesID != seriesID {
			return nil, nil, fmt.Errorf("rule %s has mismatched series_id %q (expected %q)", r.ID, r.SeriesID, seriesID)
		}
		if r.EffectiveStartSequence < 1 {
			return nil, nil, fmt.Errorf("rule %s has invalid effective_start_sequence %d", r.ID, r.EffectiveStartSequence)
		}
		if r.EffectiveEndSequence.Valid && r.EffectiveEndSequence.Int64 > 0 && r.EffectiveEndSequence.Int64 < r.EffectiveStartSequence {
			return nil, nil, fmt.Errorf("rule %s has effective_end_sequence %d < effective_start_sequence %d", r.ID, r.EffectiveEndSequence.Int64, r.EffectiveStartSequence)
		}
		rules[r.ID] = r
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close rules rows: %w", err)
	}

	if _, ok := rules[sourceRuleID]; !ok {
		return nil, nil, fmt.Errorf("source rule %s not found in series %s", sourceRuleID, seriesID)
	}

	validChain := map[string]bool{}
	visited := map[string]bool{}
	currentID := sourceRuleID
	for currentID != "" {
		if visited[currentID] {
			return nil, nil, fmt.Errorf("cycle detected in rule chain at %s", currentID)
		}
		visited[currentID] = true
		r, ok := rules[currentID]
		if !ok {
			return nil, nil, fmt.Errorf("parent rule %s not found in series %s", currentID, seriesID)
		}
		validChain[currentID] = true
		if r.ParentRuleID.Valid && r.ParentRuleID.String != "" {
			currentID = r.ParentRuleID.String
		} else {
			currentID = ""
		}
	}

	return rules, validChain, nil
}

func validateRecurringFutureItemRule(
	it recurringFuturePreflightItem,
	rules map[string]recurringFutureRuleIntegrity,
	validChain map[string]bool,
	sourceRuleID string,
) error {
	if !it.SeriesID.Valid || it.SeriesID.String == "" {
		if it.RuleID.Valid && it.RuleID.String != sourceRuleID {
			return fmt.Errorf("partially normalized item references rule %q (expected source %q)", it.RuleID.String, sourceRuleID)
		}
		return nil
	}

	if !it.RuleID.Valid || it.RuleID.String == "" {
		return fmt.Errorf("modern item has series_id but no recurring_rule_id")
	}

	ruleID := it.RuleID.String
	r, ok := rules[ruleID]
	if !ok {
		return fmt.Errorf("rule %q not found in series rules", ruleID)
	}
	if r.WorkspaceID != it.WorkspaceID {
		return fmt.Errorf("rule %q belongs to workspace %q, item is in %q", ruleID, r.WorkspaceID, it.WorkspaceID)
	}
	if !validChain[ruleID] {
		return fmt.Errorf("rule %q is not in the ancestor chain of source rule %q", ruleID, sourceRuleID)
	}
	if it.Sequence < r.EffectiveStartSequence {
		return fmt.Errorf("sequence %d is before effective_start %d of rule %q", it.Sequence, r.EffectiveStartSequence, ruleID)
	}
	if r.EffectiveEndSequence.Valid && it.Sequence > r.EffectiveEndSequence.Int64 {
		return fmt.Errorf("sequence %d is after effective_end %d of rule %q", it.Sequence, r.EffectiveEndSequence.Int64, ruleID)
	}
	return nil
}

type futureCivilIntentState uint8

const (
	futureCivilPreserve futureCivilIntentState = iota
	futureCivilClear
	futureCivilSet
)

type futureOptionalCivilIntent struct {
	state futureCivilIntentState
	value CivilWriteDate
}

func preserveFutureOptionalCivilIntent() futureOptionalCivilIntent {
	return futureOptionalCivilIntent{state: futureCivilPreserve}
}

func clearFutureOptionalCivilIntent() futureOptionalCivilIntent {
	return futureOptionalCivilIntent{state: futureCivilClear}
}

func setFutureOptionalCivilIntent(value CivilWriteDate) futureOptionalCivilIntent {
	return futureOptionalCivilIntent{state: futureCivilSet, value: value}
}

func (intent futureOptionalCivilIntent) validate() error {
	zero := CivilWriteDate{}
	switch intent.state {
	case futureCivilPreserve, futureCivilClear:
		if intent.value != zero {
			return fmt.Errorf("future civil intent %d cannot carry a value", intent.state)
		}
		return nil
	case futureCivilSet:
		if intent.value == zero {
			return fmt.Errorf("future civil SET requires a value")
		}
		return intent.value.validate()
	default:
		return fmt.Errorf("unknown future civil intent state %d", intent.state)
	}
}

func parseFutureOptionalCivilIntent(isBusiness, provided bool, raw string) (futureOptionalCivilIntent, error) {
	if !isBusiness || !provided {
		return preserveFutureOptionalCivilIntent(), nil
	}
	if strings.TrimSpace(raw) == "" {
		return clearFutureOptionalCivilIntent(), nil
	}
	value, err := NewCivilWriteDate(strings.TrimSpace(raw))
	if err != nil {
		return futureOptionalCivilIntent{}, err
	}
	return setFutureOptionalCivilIntent(value), nil
}

func (h *TransactionHandler) validateRecurringFutureCivilIntentTx(
	tx *sql.Tx,
	ruleID string,
	seriesID string,
	currentSequence int64,
	currentCivil string,
	anchor CivilWriteDate,
	intent futureOptionalCivilIntent,
	isBusiness bool,
) error {
	if err := anchor.validate(); err != nil {
		return err
	}
	if err := intent.validate(); err != nil {
		return err
	}
	if !isBusiness || intent.state != futureCivilPreserve || currentCivil == anchor.Civil() {
		return nil
	}

	var ruleSeriesID sql.NullString
	var offset sql.NullInt64
	if err := tx.QueryRow(`
		SELECT series_id, due_date_offset_days
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, ruleID, h.WorkspaceID).Scan(&ruleSeriesID, &offset); err != nil {
		return err
	}
	if offset.Valid {
		return nil
	}
	logicalSeriesID := strings.TrimSpace(seriesID)
	if logicalSeriesID == "" {
		logicalSeriesID = strings.TrimSpace(ruleSeriesID.String)
	}

	var dueCount int
	if logicalSeriesID == "" {
		if err := tx.QueryRow(`
			SELECT COUNT(1)
			FROM transactions
			WHERE workspace_id = ?
			  AND recurring_rule_id = ?
			  AND recurrence_sequence >= ?
			  AND (due_date IS NOT NULL OR due_date_civil IS NOT NULL)
		`, h.WorkspaceID, ruleID, currentSequence).Scan(&dueCount); err != nil {
			return err
		}
	} else {
		if err := tx.QueryRow(`
			SELECT COUNT(1)
			FROM transactions
			WHERE workspace_id = ?
			  AND recurrence_sequence >= ?
			  AND (series_id = ? OR recurring_rule_id = ?)
			  AND (due_date IS NOT NULL OR due_date_civil IS NOT NULL)
		`, h.WorkspaceID, currentSequence, logicalSeriesID, ruleID).Scan(&dueCount); err != nil {
			return err
		}
	}
	if dueCount > 0 {
		return errRecurringFutureCivilConflict
	}
	return nil
}

type recurringFutureSplitParams struct {
	CurrentID         string
	RuleID            string
	SeriesID          string
	CurrentSequence   int64
	Type              string
	Amount            int64
	Anchor            CivilWriteDate
	Description       string
	Notes             string
	AttachmentPath    string
	AccountID         string
	DestinationID     string
	CategoryID        string
	Status            string
	Frequency         string
	DueDateIntent     futureOptionalCivilIntent
	ContactID         string
	ContactProvided   bool
	IsBusiness        bool
	Now               int64
	AllowBoxOverdraft bool
}

type recurringFutureSplitItem struct {
	id                 string
	trType             string
	amount             int64
	accountID          string
	destinationID      sql.NullString
	categoryID         sql.NullString
	paymentStatus      string
	accountType        string
	dateUnix           int64
	dateCivil          sql.NullString
	invoiceID          sql.NullString
	invoiceStatus      sql.NullString
	dueDate            sql.NullInt64
	dueDateCivil       sql.NullString
	contactID          sql.NullString
	recurrenceSequence int64
	isException        int
}

func recurringFutureSplitItemMutable(item recurringFutureSplitItem) bool {
	if item.isException != 0 {
		return false
	}
	if item.invoiceID.Valid {
		if !item.invoiceStatus.Valid {
			return false
		}
		status := strings.ToUpper(strings.TrimSpace(item.invoiceStatus.String))
		return status != "CLOSED" && status != "PAID"
	}
	return item.paymentStatus != "paid"
}

func recurringFutureSelectedProtectedItemMutable(item recurringFutureSplitItem) bool {
	if item.invoiceID.Valid {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.paymentStatus), models.PaymentStatusPaid) || item.isException != 0
}

func recurringFutureSplitDueDateRefs(item recurringFutureSplitItem, occurrenceDate CivilWriteDate, competenceChanged bool, intent futureOptionalCivilIntent, offset sql.NullInt64, isBusiness bool) (interface{}, interface{}, error) {
	if !isBusiness {
		return nil, nil, nil
	}
	switch intent.state {
	case futureCivilPreserve:
		if !competenceChanged {
			return nullInt64Interface(item.dueDate), nullStringInterface(item.dueDateCivil), nil
		}
		if !offset.Valid {
			if item.dueDate.Valid || item.dueDateCivil.Valid {
				return nil, nil, errRecurringFutureCivilConflict
			}
			return nil, nil, nil
		}
	case futureCivilClear:
		return nil, nil, nil
	case futureCivilSet:
		if !offset.Valid {
			return nil, nil, fmt.Errorf("future civil SET without due date offset")
		}
	default:
		return nil, nil, fmt.Errorf("invalid future civil intent state %d", intent.state)
	}

	dueDate, err := occurrenceDate.AddDays(int(offset.Int64))
	if err != nil {
		return nil, nil, err
	}
	return dueDate.LegacyUnix(), dueDate.Civil(), nil
}

func recurringFutureSplitContactRef(item recurringFutureSplitItem, contactID string, contactProvided bool, isBusiness bool) interface{} {
	if contactID != "" {
		return contactID
	}
	if isBusiness && !contactProvided && item.contactID.Valid {
		return item.contactID.String
	}
	return nil
}

// ---------- 4B-B2: recurring scope=all global preflight and update ----------

type recurringAllDueIntentState uint8

const (
	recurringAllDuePreserve recurringAllDueIntentState = iota
	recurringAllDueClear
	recurringAllDueSet
)

type recurringAllDueIntent struct {
	state recurringAllDueIntentState
	value OptionalCivilWriteDate
}

type recurringAll4BB2Params struct {
	CurrentID         string
	Type              string
	Amount            int64
	Date              CivilWriteDate
	Description       string
	RawNotes          string
	NotesProvided     bool
	SelectedNotes     string
	HasUpload         bool
	AccountID         string
	DestinationID     string
	CategoryID        string
	Status            string
	Frequency         string
	DueRaw            string
	DueProvided       bool
	ContactID         string
	ContactProvided   bool
	IsBusiness        bool
	Now               int64
	AllowBoxOverdraft bool
}

type recurringAllRuleSnapshot struct {
	id                     string
	seriesID               sql.NullString
	parentRuleID           sql.NullString
	active                 bool
	effectiveStartSequence int64
	effectiveEndSequence   sql.NullInt64
	accountID              string
	destinationID          sql.NullString
	categoryID             sql.NullString
	trType                 string
	amount                 int64
	description            string
	startDate              int64
	startDateCivil         sql.NullString
	startsAt               sql.NullInt64
	startsAtCivil          sql.NullString
	endDate                sql.NullInt64
	endDateCivil           sql.NullString
	generatedUntil         sql.NullInt64
	generatedUntilCivil    sql.NullString
	frequency              string
	defaultPaymentStatus   string
	dueDatePolicy          sql.NullString
	dueDateOffsetDays      sql.NullInt64
	contactID              sql.NullString
	updatedAt              int64
}

type recurringAllItemSnapshot struct {
	id                 string
	ruleID             sql.NullString
	seriesID           sql.NullString
	recurrenceSequence int64
	trType             string
	amount             int64
	accountID          string
	destinationID      sql.NullString
	categoryID         sql.NullString
	accountType        string
	paymentStatus      string
	dateUnix           int64
	dateCivil          sql.NullString
	dueDate            sql.NullInt64
	dueDateCivil       sql.NullString
	contactID          sql.NullString
	invoiceID          sql.NullString
	invoiceStatus      sql.NullString
	paidAt             sql.NullInt64
	paidDateCivil      sql.NullString
	isException        bool
	hasStatusHistory   bool
	hasInvoicePayment  bool
	notes              string
	attachmentPath     string
	updatedAt          int64
	protected          bool
	validatedDate      CivilWriteDate
	validatedDueDate   OptionalCivilWriteDate
}

type recurringAllPreflight struct {
	logicalSeriesID   string
	needsSeriesEnsure bool
	isBusiness        bool
	rules             []recurringAllRuleSnapshot
	activeRule        recurringAllRuleSnapshot
	selectedRule      recurringAllRuleSnapshot
	items             []recurringAllItemSnapshot
	selected          recurringAllItemSnapshot
}

type recurringAllPostRule struct {
	accountID            string
	destinationID        string
	categoryID           string
	trType               string
	amount               int64
	description          string
	startDate            CivilWriteDate
	startsAt             CivilWriteDate
	frequency            string
	defaultPaymentStatus string
	dueDatePolicy        sql.NullString
	dueDateOffsetDays    sql.NullInt64
	contactID            string
}

type recurringAllMutablePlan struct {
	item         recurringAllItemSnapshot
	date         CivilWriteDate
	due          OptionalCivilWriteDate
	writeDate    bool
	writeDue     bool
	contactWrite bool
	contact      interface{}
}

func recurringAllRequestHasUpload(r *http.Request) bool {
	if r == nil || r.MultipartForm == nil {
		return false
	}
	for _, header := range r.MultipartForm.File["anexo"] {
		if header != nil {
			return true
		}
	}
	return false
}

func recurringAllSQLiteConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "DATABASE IS LOCKED") || strings.Contains(msg, "ACTIVE RULE CANNOT HAVE CHILDREN")
}

func recurringAllConflictf(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", errRecurringAllConflict, fmt.Sprintf(format, args...))
}

func recurringAllCivilDatePair(legacy int64, civil sql.NullString) (CivilWriteDate, error) {
	if legacy <= 0 {
		return CivilWriteDate{}, recurringAllConflictf("invalid required legacy date")
	}
	legacyCivil := civildate.LegacyUnixToCivilDateUTC(legacy).String()
	if civil.Valid {
		parsed, err := NewCivilWriteDate(civil.String)
		if err != nil {
			return CivilWriteDate{}, recurringAllConflictf("invalid civil date: %v", err)
		}
		if parsed.Civil() != legacyCivil {
			return CivilWriteDate{}, recurringAllConflictf("divergent required civil pair")
		}
		return parsed, nil
	}
	parsed, err := NewCivilWriteDate(legacyCivil)
	if err != nil {
		return CivilWriteDate{}, recurringAllConflictf("invalid legacy date: %v", err)
	}
	return parsed, nil
}

func recurringAllOptionalCivilDatePair(legacy sql.NullInt64, civil sql.NullString) (OptionalCivilWriteDate, error) {
	if !legacy.Valid && !civil.Valid {
		return OptionalCivilWriteDate{}, nil
	}
	if !legacy.Valid || legacy.Int64 <= 0 || !civil.Valid {
		if legacy.Valid && legacy.Int64 > 0 && !civil.Valid {
			legacyCivil := civildate.LegacyUnixToCivilDateUTC(legacy.Int64).String()
			parsed, err := NewOptionalCivilWriteDate(legacyCivil)
			if err != nil {
				return OptionalCivilWriteDate{}, recurringAllConflictf("invalid optional legacy date: %v", err)
			}
			return parsed, nil
		}
		return OptionalCivilWriteDate{}, recurringAllConflictf("incomplete optional civil pair")
	}
	legacyCivil := civildate.LegacyUnixToCivilDateUTC(legacy.Int64).String()
	parsed, err := NewOptionalCivilWriteDate(civil.String)
	if err != nil {
		return OptionalCivilWriteDate{}, recurringAllConflictf("invalid optional civil date: %v", err)
	}
	if parsed.CivilValue().(string) != legacyCivil {
		return OptionalCivilWriteDate{}, recurringAllConflictf("divergent optional civil pair")
	}
	return parsed, nil
}

func recurringAllRuleAnchor(rule recurringAllRuleSnapshot) (CivilWriteDate, error) {
	if rule.startsAt.Valid {
		return recurringAllCivilDatePair(rule.startsAt.Int64, rule.startsAtCivil)
	}
	if rule.startsAtCivil.Valid {
		return CivilWriteDate{}, recurringAllConflictf("starts_at civil without legacy value")
	}
	return recurringAllCivilDatePair(rule.startDate, rule.startDateCivil)
}

func recurringAllSequenceInRule(rule recurringAllRuleSnapshot, sequence int64) bool {
	if sequence < rule.effectiveStartSequence || rule.effectiveStartSequence <= 0 {
		return false
	}
	if rule.effectiveEndSequence.Valid {
		if rule.effectiveEndSequence.Int64 == 0 {
			return false
		}
		return sequence <= rule.effectiveEndSequence.Int64
	}
	return true
}

func recurringAllItemIsProtected(item recurringAllItemSnapshot) bool {
	if strings.EqualFold(strings.TrimSpace(item.paymentStatus), models.PaymentStatusPaid) {
		return true
	}
	if item.paidAt.Valid && item.paidAt.Int64 > 0 {
		return true
	}
	if item.paidDateCivil.Valid || item.hasStatusHistory || item.isException || item.hasInvoicePayment {
		return true
	}
	if item.invoiceID.Valid {
		status := strings.ToUpper(strings.TrimSpace(item.invoiceStatus.String))
		return status == "CLOSED" || status == "PAID" || !item.invoiceStatus.Valid
	}
	return false
}

func (h *TransactionHandler) loadRecurringAllPreflightTx(tx *sql.Tx, currentID string) (*recurringAllPreflight, error) {
	var workspaceKind string
	if err := tx.QueryRow(`SELECT type FROM workspaces WHERE id = ?`, h.WorkspaceID).Scan(&workspaceKind); err != nil {
		return nil, recurringAllConflictf("workspace not found")
	}

	var currentRuleID sql.NullString
	var currentSeriesID sql.NullString
	var currentSequence sql.NullInt64
	if err := tx.QueryRow(`
		SELECT recurring_rule_id, series_id, recurrence_sequence
		FROM transactions
		WHERE id = ? AND workspace_id = ?
	`, currentID, h.WorkspaceID).Scan(&currentRuleID, &currentSeriesID, &currentSequence); err != nil {
		return nil, recurringAllConflictf("current transaction not found")
	}
	if !currentRuleID.Valid || strings.TrimSpace(currentRuleID.String) == "" || !currentSequence.Valid || currentSequence.Int64 <= 0 {
		return nil, recurringAllConflictf("current recurrence identity is incomplete")
	}

	var sourceSeriesID sql.NullString
	var sourceParentID sql.NullString
	if err := tx.QueryRow(`
		SELECT series_id, parent_rule_id
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, currentRuleID.String, h.WorkspaceID).Scan(&sourceSeriesID, &sourceParentID); err != nil {
		return nil, recurringAllConflictf("source rule not found")
	}

	preflight := &recurringAllPreflight{isBusiness: strings.EqualFold(strings.TrimSpace(workspaceKind), "business")}
	preflight.logicalSeriesID = strings.TrimSpace(sourceSeriesID.String)
	if preflight.logicalSeriesID == "" {
		preflight.logicalSeriesID = currentRuleID.String
		preflight.needsSeriesEnsure = true
		if sourceParentID.Valid && strings.TrimSpace(sourceParentID.String) != "" {
			return nil, recurringAllConflictf("legacy rule has a parent")
		}
		if currentSeriesID.Valid && strings.TrimSpace(currentSeriesID.String) != "" {
			return nil, recurringAllConflictf("legacy association is partial")
		}
		var partialSeries, partialRules int
		if err := tx.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE id = ? AND workspace_id = ?`, preflight.logicalSeriesID, h.WorkspaceID).Scan(&partialSeries); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(`SELECT COUNT(1) FROM recurring_rules WHERE workspace_id = ? AND series_id = ?`, h.WorkspaceID, preflight.logicalSeriesID).Scan(&partialRules); err != nil {
			return nil, err
		}
		if partialSeries != 0 || partialRules != 0 {
			return nil, recurringAllConflictf("legacy association is partial")
		}
	} else {
		if !currentSeriesID.Valid || strings.TrimSpace(currentSeriesID.String) != preflight.logicalSeriesID {
			return nil, recurringAllConflictf("current transaction series is incompatible")
		}
		var seriesCount int
		if err := tx.QueryRow(`SELECT COUNT(1) FROM recurring_series WHERE id = ? AND workspace_id = ?`, preflight.logicalSeriesID, h.WorkspaceID).Scan(&seriesCount); err != nil {
			return nil, err
		}
		if seriesCount != 1 {
			return nil, recurringAllConflictf("recurring series not found")
		}
	}

	rulesQuery := `
		SELECT id, series_id, parent_rule_id, active,
		       COALESCE(effective_start_sequence, 1), effective_end_sequence,
		       account_id, destination_account_id, category_id, type, amount, description,
		       start_date, start_date_civil, starts_at, starts_at_civil,
		       end_date, end_date_civil, generated_until, generated_until_civil,
		       frequency, default_payment_status, due_date_policy, due_date_offset_days,
		       contact_id, updated_at
		FROM recurring_rules
		WHERE workspace_id = ? AND `
	var rulesArgs []interface{}
	if preflight.needsSeriesEnsure {
		rulesQuery += `id = ?`
		rulesArgs = []interface{}{h.WorkspaceID, currentRuleID.String}
	} else {
		rulesQuery += `series_id = ?`
		rulesArgs = []interface{}{h.WorkspaceID, preflight.logicalSeriesID}
	}
	rulesQuery += ` ORDER BY created_at ASC, id ASC`
	ruleRows, err := tx.Query(rulesQuery, rulesArgs...)
	if err != nil {
		return nil, err
	}
	for ruleRows.Next() {
		var rule recurringAllRuleSnapshot
		var active int
		if err := ruleRows.Scan(
			&rule.id, &rule.seriesID, &rule.parentRuleID, &active,
			&rule.effectiveStartSequence, &rule.effectiveEndSequence,
			&rule.accountID, &rule.destinationID, &rule.categoryID, &rule.trType, &rule.amount, &rule.description,
			&rule.startDate, &rule.startDateCivil, &rule.startsAt, &rule.startsAtCivil,
			&rule.endDate, &rule.endDateCivil, &rule.generatedUntil, &rule.generatedUntilCivil,
			&rule.frequency, &rule.defaultPaymentStatus, &rule.dueDatePolicy, &rule.dueDateOffsetDays,
			&rule.contactID, &rule.updatedAt,
		); err != nil {
			_ = ruleRows.Close()
			return nil, err
		}
		rule.active = active == 1
		preflight.rules = append(preflight.rules, rule)
	}
	if err := ruleRows.Close(); err != nil {
		return nil, err
	}
	if len(preflight.rules) == 0 {
		return nil, recurringAllConflictf("series has no rules")
	}

	itemsQuery := `
		SELECT t.id, t.recurring_rule_id, t.series_id, t.recurrence_sequence,
		       t.type, t.amount, t.account_id, t.destination_account_id, t.category_id,
		       COALESCE(a.type, ''), t.status, t.date, t.date_civil, t.due_date, t.due_date_civil,
		       t.contact_id, t.invoice_id, i.status, t.paid_at, t.paid_date_civil,
		       COALESCE(t.is_recurring_exception, 0),
		       EXISTS (
		           SELECT 1 FROM transaction_status_history tsh
		           WHERE tsh.workspace_id = t.workspace_id AND tsh.transaction_id = t.id
		       ),
		       EXISTS (
		           SELECT 1 FROM invoice_payments ip
		           WHERE ip.workspace_id = t.workspace_id AND ip.reversed_at IS NULL
		             AND (ip.transaction_id = t.id OR (t.invoice_id IS NOT NULL AND ip.invoice_id = t.invoice_id))
		       ),
		       t.notes, t.attachment_path, t.updated_at
		FROM transactions t
		LEFT JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		WHERE t.workspace_id = ? AND `
	var itemArgs []interface{}
	if preflight.needsSeriesEnsure {
		itemsQuery += `t.recurring_rule_id = ?`
		itemArgs = []interface{}{h.WorkspaceID, currentRuleID.String}
	} else {
		itemsQuery += `(
			t.series_id = ? OR t.recurring_rule_id IN (
				SELECT id FROM recurring_rules WHERE workspace_id = ? AND series_id = ?
			)
		)`
		itemArgs = []interface{}{h.WorkspaceID, preflight.logicalSeriesID, h.WorkspaceID, preflight.logicalSeriesID}
	}
	itemsQuery += ` ORDER BY t.recurrence_sequence ASC, t.id ASC`
	itemRows, err := tx.Query(itemsQuery, itemArgs...)
	if err != nil {
		return nil, err
	}
	for itemRows.Next() {
		var item recurringAllItemSnapshot
		var isException int
		if err := itemRows.Scan(
			&item.id, &item.ruleID, &item.seriesID, &item.recurrenceSequence,
			&item.trType, &item.amount, &item.accountID, &item.destinationID, &item.categoryID,
			&item.accountType, &item.paymentStatus, &item.dateUnix, &item.dateCivil, &item.dueDate, &item.dueDateCivil,
			&item.contactID, &item.invoiceID, &item.invoiceStatus, &item.paidAt, &item.paidDateCivil,
			&isException, &item.hasStatusHistory, &item.hasInvoicePayment,
			&item.notes, &item.attachmentPath, &item.updatedAt,
		); err != nil {
			_ = itemRows.Close()
			return nil, err
		}
		item.isException = isException != 0
		preflight.items = append(preflight.items, item)
	}
	if err := itemRows.Close(); err != nil {
		return nil, err
	}
	if len(preflight.items) == 0 {
		return nil, recurringAllConflictf("series has no materialized occurrences")
	}
	for _, rule := range preflight.rules {
		if err := ensureAccountInWorkspaceTx(tx, rule.accountID, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical rule account is outside workspace")
		}
		if err := ensureAccountInWorkspaceTx(tx, rule.destinationID.String, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical rule destination is outside workspace")
		}
		if err := ensureCategoryInWorkspaceTx(tx, rule.categoryID.String, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical rule category is outside workspace")
		}
		if err := ensureContactInWorkspaceTx(tx, rule.contactID.String, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical rule contact is outside workspace")
		}
	}
	for _, item := range preflight.items {
		if strings.TrimSpace(item.accountType) == "" {
			return nil, recurringAllConflictf("historical occurrence account is outside workspace")
		}
		if err := ensureAccountInWorkspaceTx(tx, item.destinationID.String, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical occurrence destination is outside workspace")
		}
		if err := ensureCategoryInWorkspaceTx(tx, item.categoryID.String, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical occurrence category is outside workspace")
		}
		if err := ensureContactInWorkspaceTx(tx, item.contactID.String, h.WorkspaceID); err != nil {
			return nil, recurringAllConflictf("historical occurrence contact is outside workspace")
		}
	}

	if err := validateRecurringAllPreflight(preflight, currentID, currentRuleID.String); err != nil {
		return nil, err
	}
	var hasAnyChild int
	if err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM recurring_rules
			WHERE parent_rule_id = ?
		)
	`, preflight.activeRule.id).Scan(&hasAnyChild); err != nil {
		return nil, err
	}
	if hasAnyChild != 0 {
		return nil, recurringAllConflictf("active rule is not a global leaf")
	}
	return preflight, nil
}

func validateRecurringAllPreflight(preflight *recurringAllPreflight, currentID, currentRuleID string) error {
	ruleMap := make(map[string]recurringAllRuleSnapshot, len(preflight.rules))
	children := make(map[string]int)
	activeCount := 0
	rootCount := 0
	for _, rule := range preflight.rules {
		if _, exists := ruleMap[rule.id]; exists {
			return recurringAllConflictf("duplicate recurring rule")
		}
		if rule.effectiveStartSequence <= 0 {
			return recurringAllConflictf("invalid rule start sequence")
		}
		if rule.effectiveEndSequence.Valid && rule.effectiveEndSequence.Int64 < 0 {
			return recurringAllConflictf("invalid rule end sequence")
		}
		if rule.effectiveEndSequence.Valid && rule.effectiveEndSequence.Int64 > 0 && rule.effectiveEndSequence.Int64 < rule.effectiveStartSequence {
			return recurringAllConflictf("inverted rule range")
		}
		if _, err := recurringAllCivilDatePair(rule.startDate, rule.startDateCivil); err != nil {
			return err
		}
		if _, err := recurringAllRuleAnchor(rule); err != nil {
			return err
		}
		if _, err := recurringAllOptionalCivilDatePair(rule.endDate, rule.endDateCivil); err != nil {
			return err
		}
		if _, err := recurringAllOptionalCivilDatePair(rule.generatedUntil, rule.generatedUntilCivil); err != nil {
			return err
		}
		if rule.active {
			activeCount++
			preflight.activeRule = rule
		}
		if !rule.parentRuleID.Valid || strings.TrimSpace(rule.parentRuleID.String) == "" {
			rootCount++
		}
		ruleMap[rule.id] = rule
	}
	if activeCount != 1 || rootCount != 1 {
		return recurringAllConflictf("series must have exactly one active rule and one root")
	}
	if preflight.needsSeriesEnsure && len(preflight.rules) != 1 {
		return recurringAllConflictf("legacy recurrence is not a single rule")
	}
	for _, rule := range preflight.rules {
		if rule.parentRuleID.Valid && strings.TrimSpace(rule.parentRuleID.String) != "" {
			parent, ok := ruleMap[rule.parentRuleID.String]
			if !ok || parent.id == rule.id {
				return recurringAllConflictf("invalid parent rule")
			}
			children[parent.id]++
			if children[parent.id] > 1 {
				return recurringAllConflictf("version graph is not a chain")
			}
			if parent.effectiveEndSequence.Valid {
				switch parent.effectiveEndSequence.Int64 {
				case 0:
					if rule.effectiveStartSequence != parent.effectiveStartSequence {
						return recurringAllConflictf("empty sentinel does not preserve start sequence")
					}
				default:
					if rule.effectiveStartSequence != parent.effectiveEndSequence.Int64+1 {
						return recurringAllConflictf("rule chain has a gap or overlap")
					}
				}
			} else if !parent.active {
				return recurringAllConflictf("inactive parent has no closed range")
			}
		}
		seen := map[string]bool{}
		cursor := rule
		for cursor.parentRuleID.Valid && strings.TrimSpace(cursor.parentRuleID.String) != "" {
			if seen[cursor.id] {
				return recurringAllConflictf("rule chain cycle")
			}
			seen[cursor.id] = true
			cursor = ruleMap[cursor.parentRuleID.String]
		}
	}
	if children[preflight.activeRule.id] != 0 {
		return recurringAllConflictf("active rule is not the final rule in the chain")
	}

	selectedFound := false
	seqs := make(map[int64]bool, len(preflight.items))
	for index := range preflight.items {
		item := &preflight.items[index]
		if item.recurrenceSequence <= 0 || seqs[item.recurrenceSequence] {
			return recurringAllConflictf("invalid or duplicate recurrence sequence")
		}
		seqs[item.recurrenceSequence] = true
		if !item.ruleID.Valid {
			return recurringAllConflictf("occurrence without historical rule")
		}
		rule, ok := ruleMap[item.ruleID.String]
		if !ok || !recurringAllSequenceInRule(rule, item.recurrenceSequence) {
			return recurringAllConflictf("occurrence is outside its historical rule")
		}
		if preflight.needsSeriesEnsure {
			if item.seriesID.Valid && strings.TrimSpace(item.seriesID.String) != "" {
				return recurringAllConflictf("legacy occurrence has partial series association")
			}
		} else if !item.seriesID.Valid || strings.TrimSpace(item.seriesID.String) != preflight.logicalSeriesID {
			return recurringAllConflictf("occurrence has incompatible series")
		}
		validatedDate, err := recurringAllCivilDatePair(item.dateUnix, item.dateCivil)
		if err != nil {
			return err
		}
		validatedDue, err := recurringAllOptionalCivilDatePair(item.dueDate, item.dueDateCivil)
		if err != nil {
			return err
		}
		if item.paidDateCivil.Valid {
			if _, err := recurringAllOptionalCivilDatePair(item.paidAt, item.paidDateCivil); err != nil {
				return err
			}
		}
		if item.invoiceID.Valid && !item.invoiceStatus.Valid {
			return recurringAllConflictf("invoice link is incompatible")
		}
		item.validatedDate = validatedDate
		item.validatedDueDate = validatedDue
		item.protected = recurringAllItemIsProtected(*item)
		if item.id == currentID {
			selectedFound = true
			preflight.selected = *item
		}
	}
	if !selectedFound {
		return recurringAllConflictf("selected occurrence not found in series")
	}
	selectedRule, ok := ruleMap[currentRuleID]
	if !ok {
		return recurringAllConflictf("selected historical rule not found")
	}
	preflight.selectedRule = selectedRule
	return nil
}

func recurringAllDueIntentForParams(params recurringAll4BB2Params, selected recurringAllItemSnapshot) (recurringAllDueIntent, error) {
	if !params.IsBusiness {
		if params.DueProvided && strings.TrimSpace(params.DueRaw) != "" {
			return recurringAllDueIntent{}, recurringAllConflictf("personal recurrence cannot receive due date")
		}
		return recurringAllDueIntent{state: recurringAllDuePreserve}, nil
	}
	if !params.DueProvided {
		return recurringAllDueIntent{state: recurringAllDuePreserve}, nil
	}
	raw := strings.TrimSpace(params.DueRaw)
	if raw == "" {
		if selected.validatedDueDate.Valid() {
			return recurringAllDueIntent{state: recurringAllDueClear}, nil
		}
		return recurringAllDueIntent{state: recurringAllDuePreserve}, nil
	}
	parsed, err := NewOptionalCivilWriteDate(raw)
	if err != nil {
		return recurringAllDueIntent{}, recurringAllConflictf("invalid due date")
	}
	if selected.validatedDueDate.Valid() && parsed.CivilValue().(string) == selected.validatedDueDate.CivilValue().(string) {
		return recurringAllDueIntent{state: recurringAllDuePreserve}, nil
	}
	return recurringAllDueIntent{state: recurringAllDueSet, value: parsed}, nil
}

func recurringAllPostRuleState(preflight *recurringAllPreflight, params recurringAll4BB2Params, dueIntent recurringAllDueIntent, dateChanged, frequencyChanged bool) (recurringAllPostRule, error) {
	active := preflight.activeRule
	start, err := recurringAllCivilDatePair(active.startDate, active.startDateCivil)
	if err != nil {
		return recurringAllPostRule{}, err
	}
	startsAt, err := recurringAllRuleAnchor(active)
	if err != nil {
		return recurringAllPostRule{}, err
	}
	if dateChanged {
		start = params.Date
		startsAt = params.Date
	}
	frequency := active.frequency
	if frequencyChanged {
		frequency = params.Frequency
	}
	post := recurringAllPostRule{
		accountID:            params.AccountID,
		destinationID:        params.DestinationID,
		categoryID:           params.CategoryID,
		trType:               params.Type,
		amount:               params.Amount,
		description:          params.Description,
		startDate:            start,
		startsAt:             startsAt,
		frequency:            frequency,
		defaultPaymentStatus: active.defaultPaymentStatus,
		dueDatePolicy:        active.dueDatePolicy,
		dueDateOffsetDays:    active.dueDateOffsetDays,
	}
	if params.IsBusiness {
		if params.ContactProvided {
			post.contactID = params.ContactID
		} else if active.contactID.Valid {
			post.contactID = active.contactID.String
		}
	} else {
		post.dueDatePolicy = sql.NullString{}
		post.dueDateOffsetDays = sql.NullInt64{}
	}
	switch dueIntent.state {
	case recurringAllDueClear:
		post.dueDatePolicy = sql.NullString{}
		post.dueDateOffsetDays = sql.NullInt64{}
	case recurringAllDueSet:
		post.dueDatePolicy = sql.NullString{}
		post.dueDateOffsetDays = dueDateOffsetDays(dueIntent.value, params.Date)
	}
	return post, nil
}

func recurringAllExpectedDate(rule recurringAllRuleSnapshot, post recurringAllPostRule, sequence int64) (CivilWriteDate, error) {
	offset := sequence - rule.effectiveStartSequence
	return recurrenceCivilDateFromOffset(post.startsAt, post.frequency, offset)
}

func recurringAllExpectedDue(post recurringAllPostRule, occurrenceDate CivilWriteDate, isBusiness bool) (OptionalCivilWriteDate, error) {
	if !isBusiness || !post.dueDateOffsetDays.Valid {
		return OptionalCivilWriteDate{}, nil
	}
	due, err := occurrenceDate.AddDays(int(post.dueDateOffsetDays.Int64))
	if err != nil {
		return OptionalCivilWriteDate{}, err
	}
	return MustOptionalCivilWriteDate(due.Civil()), nil
}

func validateRecurringAllProtectedCompatibility(preflight *recurringAllPreflight, post recurringAllPostRule, params recurringAll4BB2Params) error {
	active := preflight.activeRule
	for _, item := range preflight.items {
		if !item.protected || item.isException || !recurringAllSequenceInRule(active, item.recurrenceSequence) {
			continue
		}
		expectedDate, err := recurringAllExpectedDate(active, post, item.recurrenceSequence)
		if err != nil {
			return recurringAllConflictf("protected occurrence date cannot be calculated")
		}
		expectedDue, err := recurringAllExpectedDue(post, expectedDate, params.IsBusiness)
		if err != nil {
			return recurringAllConflictf("protected occurrence due date cannot be calculated")
		}
		if !item.ruleID.Valid || item.ruleID.String != active.id ||
			item.validatedDate.Civil() != expectedDate.Civil() ||
			item.accountID != post.accountID || item.trType != post.trType {
			return recurringAllConflictf("protected occurrence conflicts with materializer expectation")
		}
		itemDue := ""
		if item.validatedDueDate.Valid() {
			itemDue = item.validatedDueDate.CivilValue().(string)
		}
		expectedDueCivil := ""
		if expectedDue.Valid() {
			expectedDueCivil = expectedDue.CivilValue().(string)
		}
		if itemDue != expectedDueCivil {
			return recurringAllConflictf("protected occurrence conflicts with materializer due date")
		}
	}
	return nil
}

func (h *TransactionHandler) updateAllRecurringSeries4BB2Tx(tx *sql.Tx, params recurringAll4BB2Params) ([]string, []string, error) {
	if params.HasUpload || (params.NotesProvided && params.RawNotes != params.SelectedNotes) {
		return nil, nil, errRecurringAllIndividualOnly
	}
	preflight, err := h.loadRecurringAllPreflightTx(tx, params.CurrentID)
	if err != nil {
		return nil, nil, err
	}
	if h.testHooks != nil && h.testHooks.afterPreflight != nil {
		h.testHooks.afterPreflight("all")
	}
	if params.NotesProvided && params.RawNotes != preflight.selected.notes {
		return nil, nil, errRecurringAllIndividualOnly
	}
	if preflight.isBusiness != params.IsBusiness {
		return nil, nil, recurringAllConflictf("workspace type changed during preflight")
	}
	if err := ensureAccountInWorkspaceTx(tx, params.AccountID, h.WorkspaceID); err != nil {
		return nil, nil, recurringAllConflictf("account is outside workspace")
	}
	if err := ensureAccountInWorkspaceTx(tx, params.DestinationID, h.WorkspaceID); err != nil {
		return nil, nil, recurringAllConflictf("destination account is outside workspace")
	}
	if err := ensureCategoryInWorkspaceTx(tx, params.CategoryID, h.WorkspaceID); err != nil {
		return nil, nil, recurringAllConflictf("category is outside workspace")
	}
	if err := ensureContactInWorkspaceTx(tx, params.ContactID, h.WorkspaceID); err != nil {
		return nil, nil, recurringAllConflictf("contact is outside workspace")
	}
	var newAccountType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, params.AccountID, h.WorkspaceID).Scan(&newAccountType); err != nil {
		return nil, nil, recurringAllConflictf("account type cannot be loaded")
	}

	if !params.IsBusiness {
		if params.ContactID != "" {
			return nil, nil, recurringAllConflictf("personal recurrence cannot receive contact")
		}
		for _, rule := range preflight.rules {
			if rule.contactID.Valid || rule.dueDatePolicy.Valid || rule.dueDateOffsetDays.Valid {
				return nil, nil, recurringAllConflictf("personal recurring rule has business metadata")
			}
		}
		for _, item := range preflight.items {
			if item.contactID.Valid || item.validatedDueDate.Valid() {
				return nil, nil, recurringAllConflictf("personal occurrence has business metadata")
			}
		}
	}

	dueIntent, err := recurringAllDueIntentForParams(params, preflight.selected)
	if err != nil {
		return nil, nil, err
	}
	dateChanged := preflight.selected.validatedDate.Civil() != params.Date.Civil()
	frequencyChanged := preflight.selectedRule.frequency != params.Frequency
	dueChange := dueIntent.state == recurringAllDueClear || dueIntent.state == recurringAllDueSet
	calendarChanged := dateChanged || frequencyChanged || dueChange
	if calendarChanged && (preflight.selected.recurrenceSequence != 1 || len(preflight.rules) != 1 || !preflight.selectedRule.active) {
		return nil, nil, recurringAllConflictf("calendar changes require sequence one of an unversioned series")
	}
	if dueIntent.state == recurringAllDuePreserve && dateChanged && params.IsBusiness && !preflight.activeRule.dueDateOffsetDays.Valid {
		for _, item := range preflight.items {
			if !item.protected && item.validatedDueDate.Valid() {
				return nil, nil, recurringAllConflictf("due date offset cannot be inferred")
			}
		}
	}

	post, err := recurringAllPostRuleState(preflight, params, dueIntent, dateChanged, frequencyChanged)
	if err != nil {
		return nil, nil, err
	}
	if err := validateRecurringAllProtectedCompatibility(preflight, post, params); err != nil {
		return nil, nil, err
	}

	mutablePlans := make([]recurringAllMutablePlan, 0, len(preflight.items))
	for _, item := range preflight.items {
		if item.protected {
			continue
		}
		plan := recurringAllMutablePlan{
			item:         item,
			date:         item.validatedDate,
			due:          item.validatedDueDate,
			writeDate:    dateChanged || frequencyChanged,
			contactWrite: params.IsBusiness && params.ContactProvided,
		}
		if plan.writeDate {
			plan.date, err = recurrenceCivilDateFromOffset(params.Date, post.frequency, item.recurrenceSequence-1)
			if err != nil {
				return nil, nil, recurringAllConflictf("occurrence date cannot be calculated")
			}
		}
		if !params.IsBusiness {
			plan.due = OptionalCivilWriteDate{}
			plan.writeDue = item.validatedDueDate.Valid()
		} else {
			switch dueIntent.state {
			case recurringAllDueClear:
				plan.due = OptionalCivilWriteDate{}
				plan.writeDue = true
			case recurringAllDueSet:
				plan.due, err = recurringAllExpectedDue(post, plan.date, true)
				if err != nil {
					return nil, nil, recurringAllConflictf("occurrence due date cannot be calculated")
				}
				plan.writeDue = true
			case recurringAllDuePreserve:
				if plan.writeDate && post.dueDateOffsetDays.Valid {
					plan.due, err = recurringAllExpectedDue(post, plan.date, true)
					if err != nil {
						return nil, nil, recurringAllConflictf("occurrence due date cannot be calculated")
					}
					plan.writeDue = true
				}
			}
		}
		if plan.contactWrite && params.ContactID != "" {
			plan.contact = params.ContactID
		}
		mutablePlans = append(mutablePlans, plan)
	}
	mutableIDs := make([]string, 0, len(mutablePlans))
	for _, plan := range mutablePlans {
		mutableIDs = append(mutableIDs, plan.item.id)
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, mutableIDs); err != nil {
		return nil, nil, err
	}

	if preflight.needsSeriesEnsure {
		seriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, preflight.selectedRule.id, params.Now)
		if err != nil {
			return nil, nil, err
		}
		if seriesID != preflight.logicalSeriesID {
			return nil, nil, recurringAllConflictf("legacy series identity changed")
		}
	}

	nextUpdatedAt := params.Now
	if nextUpdatedAt <= preflight.activeRule.updatedAt {
		nextUpdatedAt = preflight.activeRule.updatedAt + 1
	}
	var destRef interface{}
	if post.destinationID != "" {
		destRef = post.destinationID
	}
	var categoryRef interface{}
	if post.categoryID != "" {
		categoryRef = post.categoryID
	}
	var contactRef interface{}
	if params.IsBusiness && post.contactID != "" {
		contactRef = post.contactID
	}
	if h.testHooks != nil && h.testHooks.beforeAllRuleWrite != nil {
		if err := h.testHooks.beforeAllRuleWrite(tx); err != nil {
			return nil, nil, err
		}
	}
	result, err := tx.Exec(`
		UPDATE recurring_rules
		SET account_id = ?, destination_account_id = ?, category_id = ?, type = ?, amount = ?,
		    description = ?,
		    start_date = CASE WHEN ? THEN ? ELSE start_date END,
		    start_date_civil = CASE WHEN ? THEN ? ELSE start_date_civil END,
		    starts_at = CASE WHEN ? THEN ? ELSE starts_at END,
		    starts_at_civil = CASE WHEN ? THEN ? ELSE starts_at_civil END,
		    frequency = CASE WHEN ? THEN ? ELSE frequency END,
		    default_payment_status = ?, due_date_policy = ?, due_date_offset_days = ?,
		    contact_id = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND series_id = ? AND active = 1 AND updated_at = ?
	`, post.accountID, destRef, categoryRef, post.trType, post.amount,
		post.description,
		dateChanged, post.startDate.LegacyUnix(), dateChanged, post.startDate.Civil(),
		dateChanged, post.startsAt.LegacyUnix(), dateChanged, post.startsAt.Civil(),
		frequencyChanged, post.frequency,
		post.defaultPaymentStatus, nullStringInterface(post.dueDatePolicy), nullInt64Interface(post.dueDateOffsetDays),
		contactRef, nextUpdatedAt,
		preflight.activeRule.id, h.WorkspaceID, preflight.logicalSeriesID, preflight.activeRule.updatedAt)
	if err != nil {
		return nil, nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, nil, err
	}
	if affected != 1 {
		return nil, nil, recurringAllConflictf("active rule concurrency guard lost")
	}

	touchedIDs := make([]string, 0, len(mutablePlans))
	oldInvoiceIDs := make([]string, 0)
	for _, plan := range mutablePlans {
		item := plan.item
		itemStatus := item.paymentStatus
		if item.id == params.CurrentID {
			itemStatus = params.Status
		}
		if post.trType == models.TransactionTypeExpense && newAccountType == models.AccountTypeCreditCard {
			itemStatus = models.PaymentStatusPaid
		}

		if err := h.adjustReserveForSingleTransactionUpdateTx(tx, item.id, item.trType, item.categoryID, item.amount, post.trType, post.categoryID, post.amount, plan.date.LegacyUnix(), params.Now, params.AllowBoxOverdraft); err != nil {
			return nil, nil, err
		}

		var invoiceRef interface{}
		if post.trType == models.TransactionTypeExpense && newAccountType == models.AccountTypeCreditCard {
			evidence, err := resolveAutomaticInvoiceForOccurrenceTx(tx, h.WorkspaceID, post.accountID, plan.date)
			if err != nil {
				return nil, nil, err
			}
			invoiceRef = evidence.InvoiceID
		}
		if item.invoiceID.Valid && item.invoiceID.String != "" {
			oldInvoiceIDs = append(oldInvoiceIDs, item.invoiceID.String)
		}

		itemNextUpdatedAt := params.Now
		if itemNextUpdatedAt <= item.updatedAt {
			itemNextUpdatedAt = item.updatedAt + 1
		}
		result, err := tx.Exec(`
			UPDATE transactions
			SET type = ?, account_id = ?, destination_account_id = ?, amount = ?,
			    description = ?, category_id = ?, status = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END,
			    date = CASE WHEN ? THEN ? ELSE date END,
			    date_civil = CASE WHEN ? THEN ? ELSE date_civil END,
			    due_date = CASE WHEN ? THEN ? ELSE due_date END,
			    due_date_civil = CASE WHEN ? THEN ? ELSE due_date_civil END,
			    contact_id = CASE WHEN ? THEN ? ELSE contact_id END,
			    updated_at = ?
			WHERE id = ? AND workspace_id = ? AND series_id = ?
			  AND recurring_rule_id = ? AND recurrence_sequence = ? AND updated_at = ?
		`, post.trType, post.accountID, destRef, post.amount,
			post.description, categoryRef, itemStatus, invoiceRef,
			plan.writeDate, plan.date.LegacyUnix(), plan.writeDate, plan.date.Civil(),
			plan.writeDue, plan.due.LegacyValue(), plan.writeDue, plan.due.CivilValue(),
			plan.contactWrite, plan.contact,
			itemNextUpdatedAt,
			item.id, h.WorkspaceID, preflight.logicalSeriesID,
			item.ruleID.String, item.recurrenceSequence, item.updatedAt)
		if err != nil {
			return nil, nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, nil, err
		}
		if affected != 1 {
			return nil, nil, recurringAllConflictf("occurrence concurrency guard lost")
		}
		if itemStatus == models.PaymentStatusPaid {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, post.trType, newAccountType, itemStatus, post.amount, post.accountID, post.destinationID, params.Now); err != nil {
				return nil, nil, err
			}
		}
		touchedIDs = append(touchedIDs, item.id)
	}
	return touchedIDs, oldInvoiceIDs, nil
}

type recurringAllVersionedParams struct {
	RuleID            string
	SeriesID          string
	Type              string
	Amount            int64
	Description       string
	Notes             string
	AttachmentPath    string
	AccountID         string
	DestinationID     string
	CategoryID        string
	Status            string
	DueDateUnix       int64
	HasDueDate        bool
	DueDateProvided   bool
	ContactID         string
	ContactProvided   bool
	IsBusiness        bool
	Now               int64
	AllowBoxOverdraft bool
}

type recurringAllVersionedItem struct {
	id                 string
	trType             string
	amount             int64
	accountID          string
	destinationID      sql.NullString
	categoryID         sql.NullString
	paymentStatus      string
	accountType        string
	dateUnix           int64
	invoiceID          sql.NullString
	invoiceStatus      sql.NullString
	dueDate            sql.NullInt64
	contactID          sql.NullString
	recurrenceSequence int64
	isException        int
}

func recurringAllVersionedItemMutable(item recurringAllVersionedItem) bool {
	if item.isException != 0 {
		return false
	}
	if item.invoiceID.Valid {
		if !item.invoiceStatus.Valid {
			return false
		}
		status := strings.ToUpper(strings.TrimSpace(item.invoiceStatus.String))
		return status != "CLOSED" && status != "PAID"
	}
	return strings.ToLower(strings.TrimSpace(item.paymentStatus)) != "paid"
}

func recurringDeleteConflictf(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", errRecurringDeleteConflict, fmt.Sprintf(format, args...))
}

// deleteAllRecurringSeriesVersionedTx encerra a série completa sem apagar seu
// histórico estrutural. Apenas ocorrências mutáveis são removidas.
func (h *TransactionHandler) deleteAllRecurringSeriesVersionedTx(tx *sql.Tx, selectedTransactionID, ownerRuleID string, now int64) ([]string, []string, error) {
	if tx == nil || strings.TrimSpace(selectedTransactionID) == "" || strings.TrimSpace(ownerRuleID) == "" {
		return nil, nil, recurringDeleteConflictf("invalid recurring identity")
	}
	var selectedSeriesID sql.NullString
	var selectedSequence sql.NullInt64
	if err := tx.QueryRow(`
		SELECT series_id, recurrence_sequence
		FROM transactions
		WHERE workspace_id = ? AND id = ? AND recurring_rule_id = ?
	`, h.WorkspaceID, selectedTransactionID, ownerRuleID).Scan(&selectedSeriesID, &selectedSequence); err != nil {
		return nil, nil, recurringDeleteConflictf("selected occurrence not found")
	}
	if !selectedSequence.Valid || selectedSequence.Int64 <= 0 {
		return nil, nil, recurringDeleteConflictf("selected occurrence has invalid sequence")
	}
	seriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, ownerRuleID, now)
	if err != nil {
		return nil, nil, recurringDeleteConflictf("cannot resolve recurring series")
	}
	if selectedSeriesID.Valid && strings.TrimSpace(selectedSeriesID.String) != "" && selectedSeriesID.String != seriesID {
		return nil, nil, recurringDeleteConflictf("selected occurrence has incompatible series")
	}
	var selectedCount int
	if err := tx.QueryRow(`
		SELECT COUNT(1)
		FROM transactions
		WHERE workspace_id = ? AND id = ? AND recurring_rule_id = ?
		  AND series_id = ? AND recurrence_sequence = ?
	`, h.WorkspaceID, selectedTransactionID, ownerRuleID, seriesID, selectedSequence.Int64).Scan(&selectedCount); err != nil || selectedCount != 1 {
		return nil, nil, recurringDeleteConflictf("selected occurrence identity changed")
	}

	var seriesActive int
	if err := tx.QueryRow(`
		SELECT active
		FROM recurring_series
		WHERE workspace_id = ? AND id = ?
	`, h.WorkspaceID, seriesID).Scan(&seriesActive); err != nil {
		return nil, nil, recurringDeleteConflictf("recurring series not found")
	}
	if seriesActive == 0 {
		var activeRules int
		if err := tx.QueryRow(`
			SELECT COUNT(1) FROM recurring_rules
			WHERE workspace_id = ? AND series_id = ? AND active = 1
		`, h.WorkspaceID, seriesID).Scan(&activeRules); err != nil {
			return nil, nil, err
		}
		if activeRules != 0 {
			return nil, nil, recurringDeleteConflictf("inactive series has active rules")
		}
		return nil, nil, nil
	}

	if _, _, err := validateRecurringSeriesChainAndFindActiveLeafTx(tx, h.WorkspaceID, seriesID, ownerRuleID); err != nil {
		return nil, nil, recurringDeleteConflictf("invalid recurring rule chain")
	}
	var externalChild int
	if err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM recurring_rules child
			WHERE child.parent_rule_id IN (
				SELECT id FROM recurring_rules
				WHERE workspace_id = ? AND series_id = ?
			)
			AND (child.workspace_id != ? OR COALESCE(child.series_id, '') != ?)
		)
	`, h.WorkspaceID, seriesID, h.WorkspaceID, seriesID).Scan(&externalChild); err != nil {
		return nil, nil, err
	}
	if externalChild != 0 {
		return nil, nil, recurringDeleteConflictf("recurring chain has an external child")
	}

	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id,
		       t.category_id, t.status, a.type, t.date, t.invoice_id, i.status,
		       t.due_date, t.contact_id, t.recurrence_sequence,
		       COALESCE(t.is_recurring_exception, 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		WHERE t.workspace_id = ? AND t.series_id = ?
		ORDER BY t.recurrence_sequence ASC, t.id ASC
	`, h.WorkspaceID, seriesID)
	if err != nil {
		return nil, nil, err
	}
	var items []recurringAllVersionedItem
	for rows.Next() {
		var item recurringAllVersionedItem
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.categoryID, &item.paymentStatus, &item.accountType, &item.dateUnix, &item.invoiceID, &item.invoiceStatus, &item.dueDate, &item.contactID, &item.recurrenceSequence, &item.isException); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	var idsToDelete []string
	var invoiceIDs []string
	for _, item := range items {
		if !recurringAllVersionedItemMutable(item) {
			continue
		}
		idsToDelete = append(idsToDelete, item.id)
		if item.invoiceID.Valid && item.invoiceID.String != "" {
			invoiceIDs = append(invoiceIDs, item.invoiceID.String)
		}
	}
	if err := h.ensureTransactionsNotInvoicePaymentsTx(tx, idsToDelete); err != nil {
		return nil, nil, err
	}

	result, err := tx.Exec(`
		UPDATE recurring_series
		SET active = 0, updated_at = ?
		WHERE workspace_id = ? AND id = ? AND active = 1
	`, now, h.WorkspaceID, seriesID)
	if err != nil {
		return nil, nil, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return nil, nil, recurringDeleteConflictf("recurring series changed during delete")
	}
	rulesResult, err := tx.Exec(`
		UPDATE recurring_rules
		SET active = 0, updated_at = ?
		WHERE workspace_id = ? AND series_id = ? AND active = 1
	`, now, h.WorkspaceID, seriesID)
	if err != nil {
		return nil, nil, err
	}
	if affected, err := rulesResult.RowsAffected(); err != nil || affected != 1 {
		return nil, nil, recurringDeleteConflictf("active recurring leaf changed during delete")
	}

	for _, transactionID := range idsToDelete {
		if err := h.reverseActiveConsumesForTransactionTx(tx, transactionID, now); err != nil {
			return nil, nil, err
		}
	}
	if err := h.deleteTransactionsByIDsTx(tx, idsToDelete, now); err != nil {
		return nil, nil, err
	}
	return idsToDelete, dedupeIDs(invoiceIDs), nil
}

// updateAllRecurringSeriesVersionedTx implementa scope=all versionado SEGURO (Fase 5).
//
// Semântica:
//   - Não cria nova regra.
//   - Não consolida cadeia de regras.
//   - Não encerra regras ativas intermediárias.
//   - Não chama generateRecurrenceProjection.
//   - Não limpa projeções futuras.
//   - Atualiza apenas regras ativas (active=1) da mesma series_id, preservando
//     series_id, parent_rule_id, effective_start_sequence, effective_end_sequence,
//     active, start_date, starts_at, frequency e total_occurrences.
//   - Atualiza apenas transações materializadas mutáveis da série, preservando:
//     is_recurring_exception=1, transações pagas sem fatura, faturas CLOSED/PAID.
//   - Workspace isolation: todas as queries usam workspace_id.
//   - Personal: contact_id sempre NULL.
//   - Business: contact_id/due_date seguem payload; omitidos, preservam valores existentes.
//
// Deve ser chamado apenas quando data e frequência NÃO mudaram em relação à regra
// atual; caso contrário o handler cai no fluxo legado.
func (h *TransactionHandler) updateAllRecurringSeriesVersionedTx(tx *sql.Tx, params recurringAllVersionedParams) ([]string, []string, error) {
	if err := ensureAccountInWorkspaceTx(tx, params.AccountID, h.WorkspaceID); err != nil {
		return nil, nil, err
	}
	if err := ensureAccountInWorkspaceTx(tx, params.DestinationID, h.WorkspaceID); err != nil {
		return nil, nil, err
	}
	if err := ensureCategoryInWorkspaceTx(tx, params.CategoryID, h.WorkspaceID); err != nil {
		return nil, nil, err
	}
	if err := ensureContactInWorkspaceTx(tx, params.ContactID, h.WorkspaceID); err != nil {
		return nil, nil, err
	}

	var newAccType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, params.AccountID, h.WorkspaceID).Scan(&newAccType); err != nil {
		return nil, nil, err
	}
	newStatus := params.Status
	if params.Type == "EXPENSE" && newAccType == "CREDIT_CARD" {
		newStatus = "paid"
	}

	// Garante series_id sem vazar tenant (Fase 3.1.1).
	seriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, params.RuleID, params.Now)
	if err != nil {
		return nil, nil, err
	}

	var catID interface{}
	if params.CategoryID != "" {
		catID = params.CategoryID
	}
	var destID interface{}
	if params.DestinationID != "" {
		destID = params.DestinationID
	}
	ruleStatus := strings.ToUpper(newStatus)
	ruleContactRef := interface{}(nil)
	if params.IsBusiness && params.ContactID != "" {
		ruleContactRef = params.ContactID
	}

	// Atualiza apenas regras ATIVAS da série, preservando a estrutura versionada.
	if _, err := tx.Exec(`
		UPDATE recurring_rules
		SET account_id = ?,
		    destination_account_id = ?,
		    category_id = ?,
		    type = ?,
		    amount = ?,
		    description = ?,
		    default_payment_status = ?,
		    contact_id = ?,
		    updated_at = ?
		WHERE workspace_id = ?
		  AND series_id = ?
		  AND active = 1
	`, params.AccountID, destID, catID, params.Type, params.Amount, params.Description, ruleStatus, ruleContactRef, params.Now, h.WorkspaceID, seriesID); err != nil {
		return nil, nil, fmt.Errorf("update active recurring rules: %w", err)
	}

	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id,
		       t.category_id, t.status, a.type, t.date, t.invoice_id, i.status,
		       t.due_date, t.contact_id, t.recurrence_sequence,
		       COALESCE(t.is_recurring_exception, 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		WHERE t.workspace_id = ?
		  AND t.series_id = ?
		ORDER BY t.recurrence_sequence ASC
	`, h.WorkspaceID, seriesID)
	if err != nil {
		return nil, nil, err
	}

	var items []recurringAllVersionedItem
	var oldInvoiceIDs []string
	for rows.Next() {
		var item recurringAllVersionedItem
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.categoryID, &item.paymentStatus, &item.accountType, &item.dateUnix, &item.invoiceID, &item.invoiceStatus, &item.dueDate, &item.contactID, &item.recurrenceSequence, &item.isException); err != nil {
			rows.Close()
			return nil, nil, err
		}
		items = append(items, item)
		if item.invoiceID.Valid && item.invoiceID.String != "" {
			oldInvoiceIDs = append(oldInvoiceIDs, item.invoiceID.String)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	touchedIDs := make([]string, 0, len(items))
	for _, item := range items {
		if !recurringAllVersionedItemMutable(item) {
			continue
		}

		oldDest := ""
		if item.destinationID.Valid {
			oldDest = item.destinationID.String
		}
		if strings.ToLower(strings.TrimSpace(item.paymentStatus)) == "paid" {
			if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, item.trType, item.accountType, item.amount, item.accountID, oldDest, params.Now); err != nil {
				return nil, nil, err
			}
		}
		if err := h.adjustReserveForSingleTransactionUpdateTx(tx, item.id, item.trType, item.categoryID, item.amount, params.Type, params.CategoryID, params.Amount, item.dateUnix, params.Now, params.AllowBoxOverdraft); err != nil {
			return nil, nil, err
		}

		dueDateRef := interface{}(nil)
		if params.HasDueDate {
			dueDateRef = params.DueDateUnix
		} else if params.IsBusiness && !params.DueDateProvided && newStatus != "paid" && item.dueDate.Valid {
			dueDateRef = item.dueDate.Int64
		}
		contactRef := interface{}(nil)
		if params.ContactID != "" {
			contactRef = params.ContactID
		} else if params.IsBusiness && !params.ContactProvided && item.contactID.Valid {
			contactRef = item.contactID.String
		}

		if err := execOneTx(tx, `
			UPDATE transactions
			SET type = ?,
			    account_id = ?,
			    destination_account_id = ?,
			    amount = ?,
			    description = ?,
			    notes = ?,
			    attachment_path = ?,
			    category_id = ?,
			    status = ?,
			    due_date = ?,
			    contact_id = ?,
			    recurring_rule_id = (
			        SELECT id FROM recurring_rules
			        WHERE workspace_id = ? AND series_id = ? AND active = 1
			          AND effective_start_sequence <= ?
			          AND (effective_end_sequence IS NULL OR effective_end_sequence >= ?)
			        ORDER BY effective_start_sequence DESC
			        LIMIT 1
			    ),
			    updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, params.Type, params.AccountID, destID, params.Amount, params.Description, params.Notes, params.AttachmentPath, catID, newStatus, dueDateRef, contactRef, h.WorkspaceID, seriesID, item.recurrenceSequence, item.recurrenceSequence, params.Now, item.id, h.WorkspaceID); err != nil {
			return nil, nil, err
		}

		if newStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, params.Type, newAccType, newStatus, params.Amount, params.AccountID, params.DestinationID, params.Now); err != nil {
				return nil, nil, err
			}
		}
		touchedIDs = append(touchedIDs, item.id)
	}

	return touchedIDs, oldInvoiceIDs, nil
}

// deleteFutureRecurringSeriesVersionedTx implementa delete scope=future versionado (Fase 6).
type recurringSeriesChainRule struct {
	id                     string
	workspaceID            string
	seriesID               string
	parentRuleID           sql.NullString
	active                 int
	effectiveStartSequence int64
	effectiveEndSequence   sql.NullInt64
}

func validateRecurringSeriesChainAndFindActiveLeafTx(tx *sql.Tx, workspaceID, seriesID, ownerRuleID string) (activeID string, effectiveStart int64, err error) {
	rows, err := tx.Query(`
		SELECT id, workspace_id, series_id, parent_rule_id, active,
		       effective_start_sequence, effective_end_sequence
		FROM recurring_rules
		WHERE workspace_id = ? AND series_id = ?
		ORDER BY id
	`, workspaceID, seriesID)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()

	var allRules []recurringSeriesChainRule
	for rows.Next() {
		var rule recurringSeriesChainRule
		if err := rows.Scan(&rule.id, &rule.workspaceID, &rule.seriesID, &rule.parentRuleID,
			&rule.active, &rule.effectiveStartSequence, &rule.effectiveEndSequence); err != nil {
			return "", 0, err
		}
		allRules = append(allRules, rule)
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}

	if len(allRules) == 0 {
		return "", 0, fmt.Errorf("nenhuma regra ativa cobre a sequencia atual")
	}

	ruleMap := make(map[string]recurringSeriesChainRule, len(allRules))
	children := make(map[string]int)
	var activeRule *recurringSeriesChainRule
	rootCount := 0
	activeCount := 0
	ownerFound := false

	for i := range allRules {
		rule := allRules[i]
		if rule.workspaceID != workspaceID {
			return "", 0, recurringAllConflictf("rule from different workspace in series")
		}
		if rule.seriesID != seriesID {
			return "", 0, recurringAllConflictf("rule from different series")
		}
		if _, exists := ruleMap[rule.id]; exists {
			return "", 0, recurringAllConflictf("duplicate recurring rule")
		}
		if rule.effectiveStartSequence <= 0 {
			return "", 0, recurringAllConflictf("invalid rule start sequence")
		}
		if rule.effectiveEndSequence.Valid && rule.effectiveEndSequence.Int64 < 0 {
			return "", 0, recurringAllConflictf("invalid rule end sequence")
		}
		if rule.effectiveEndSequence.Valid && rule.effectiveEndSequence.Int64 > 0 && rule.effectiveEndSequence.Int64 < rule.effectiveStartSequence {
			return "", 0, recurringAllConflictf("inverted rule range")
		}
		ruleMap[rule.id] = rule
		if rule.active == 1 {
			activeCount++
			cp := rule
			activeRule = &cp
		}
		if !rule.parentRuleID.Valid || strings.TrimSpace(rule.parentRuleID.String) == "" {
			rootCount++
		}
		if rule.id == ownerRuleID {
			ownerFound = true
		}
	}

	if !ownerFound {
		return "", 0, recurringAllConflictf("selected occurrence rule not in chain")
	}
	if activeCount != 1 || rootCount != 1 {
		return "", 0, recurringAllConflictf("series must have exactly one active rule and one root")
	}
	if activeRule == nil {
		return "", 0, recurringAllConflictf("no active rule found")
	}

	for _, rule := range allRules {
		if rule.parentRuleID.Valid && strings.TrimSpace(rule.parentRuleID.String) != "" {
			parent, ok := ruleMap[rule.parentRuleID.String]
			if !ok || parent.id == rule.id {
				return "", 0, recurringAllConflictf("invalid parent rule")
			}
			children[parent.id]++
			if children[parent.id] > 1 {
				return "", 0, recurringAllConflictf("version graph is not a chain")
			}
			if parent.effectiveEndSequence.Valid && parent.effectiveEndSequence.Int64 == 0 {
				if rule.effectiveStartSequence != parent.effectiveStartSequence {
					return "", 0, recurringAllConflictf("empty sentinel range mismatch")
				}
			} else if parent.effectiveEndSequence.Valid {
				if rule.effectiveStartSequence != parent.effectiveEndSequence.Int64+1 {
					return "", 0, recurringAllConflictf("rule chain has a gap or overlap")
				}
			} else if parent.active == 0 {
				return "", 0, recurringAllConflictf("inactive parent has no closed range")
			}
		}
		seen := map[string]bool{}
		cursor := rule
		for cursor.parentRuleID.Valid && strings.TrimSpace(cursor.parentRuleID.String) != "" {
			if seen[cursor.id] {
				return "", 0, recurringAllConflictf("rule chain cycle")
			}
			seen[cursor.id] = true
			cursor = ruleMap[cursor.parentRuleID.String]
		}
	}

	if children[activeRule.id] != 0 {
		return "", 0, recurringAllConflictf("active rule is not the final rule in the chain")
	}

	cursor := *activeRule
	descendantOfOwner := false
	for {
		if cursor.id == ownerRuleID {
			descendantOfOwner = true
			break
		}
		if !cursor.parentRuleID.Valid || strings.TrimSpace(cursor.parentRuleID.String) == "" {
			break
		}
		cursor = ruleMap[cursor.parentRuleID.String]
	}
	if !descendantOfOwner {
		return "", 0, recurringAllConflictf("active rule is not a descendant of the selected rule")
	}

	return activeRule.id, activeRule.effectiveStartSequence, nil
}

// Semântica:
//   - Garante series_id via EnsureRecurringSeriesForRuleTx.
//   - Encerra todas as regras da mesma série cuja faixa alcance currentSequence.
//     Isso torna a exclusão futura recuperável para séries legadas cuja cadeia
//     versionada não seja mais linear, sem tentar reescrever seu histórico.
//   - Apaga fisicamente ocorrências da série a partir de currentSequence,
//     inclusive as marcadas como pagas sem vínculo de fatura.
//   - Preserva faturas CLOSED/PAID e exceções manuais, e reconcilia faturas OPEN.
//   - Não consolida cadeia de regras; não apaga regras antigas encerradas.
func (h *TransactionHandler) deleteFutureRecurringSeriesVersionedTx(tx *sql.Tx, ruleID string, currentSequence int64, now int64) ([]string, []string, error) {
	if currentSequence <= 0 {
		return nil, nil, fmt.Errorf("sequencia invalida")
	}

	seriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, ruleID, now)
	if err != nil {
		return nil, nil, err
	}

	if _, err := tx.Exec(`
		UPDATE recurring_rules
		SET effective_end_sequence = CASE
				WHEN effective_start_sequence < ? THEN ?
				ELSE 0
			END,
		    active = 0,
		    updated_at = ?
		WHERE workspace_id = ?
		  AND series_id = ?
		  AND (active = 1 OR effective_end_sequence IS NULL OR effective_end_sequence >= ?)
	`, currentSequence, currentSequence-1, now, h.WorkspaceID, seriesID, currentSequence); err != nil {
		return nil, nil, fmt.Errorf("encerrar regras da recorrência: %w", err)
	}

	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id,
		       t.category_id, t.status, a.type, t.date, t.invoice_id, i.status,
		       t.due_date, t.contact_id, t.recurrence_sequence,
		       COALESCE(t.is_recurring_exception, 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		WHERE t.workspace_id = ?
		  AND t.series_id = ?
		  AND t.recurrence_sequence >= ?
		ORDER BY t.recurrence_sequence ASC
	`, h.WorkspaceID, seriesID, currentSequence)
	if err != nil {
		return nil, nil, err
	}

	var items []recurringAllVersionedItem
	for rows.Next() {
		var item recurringAllVersionedItem
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.categoryID, &item.paymentStatus, &item.accountType, &item.dateUnix, &item.invoiceID, &item.invoiceStatus, &item.dueDate, &item.contactID, &item.recurrenceSequence, &item.isException); err != nil {
			rows.Close()
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	var idsToDelete []string
	var oldInvoiceIDs []string
	for _, item := range items {
		if !recurringFutureDeleteItemMutable(item) {
			continue
		}
		idsToDelete = append(idsToDelete, item.id)
		if item.invoiceID.Valid && item.invoiceID.String != "" {
			oldInvoiceIDs = append(oldInvoiceIDs, item.invoiceID.String)
		}
	}
	if err := h.ensureTransactionsNotInvoicePaymentsTx(tx, idsToDelete); err != nil {
		return nil, nil, err
	}

	if len(idsToDelete) > 0 {
		if err := h.deleteTransactionsByIDsTx(tx, idsToDelete, now); err != nil {
			return nil, nil, err
		}
	}

	return idsToDelete, dedupeIDs(oldInvoiceIDs), nil
}

func recurringFutureDeleteItemMutable(item recurringAllVersionedItem) bool {
	if item.isException != 0 {
		return false
	}
	if !item.invoiceID.Valid {
		return true
	}
	if !item.invoiceStatus.Valid {
		return false
	}
	status := strings.ToUpper(strings.TrimSpace(item.invoiceStatus.String))
	return status != "CLOSED" && status != "PAID"
}

func (h *TransactionHandler) splitFutureRecurringRuleTx(tx *sql.Tx, params recurringFutureSplitParams) (string, []string, []string, error) {
	if params.CurrentSequence <= 0 {
		return "", nil, nil, errRecurringFutureSplitBlocked
	}
	if err := params.Anchor.validate(); err != nil {
		return "", nil, nil, err
	}
	if err := params.DueDateIntent.validate(); err != nil {
		return "", nil, nil, err
	}
	if err := ensureAccountInWorkspaceTx(tx, params.AccountID, h.WorkspaceID); err != nil {
		return "", nil, nil, err
	}
	if err := ensureAccountInWorkspaceTx(tx, params.DestinationID, h.WorkspaceID); err != nil {
		return "", nil, nil, err
	}
	if err := ensureCategoryInWorkspaceTx(tx, params.CategoryID, h.WorkspaceID); err != nil {
		return "", nil, nil, err
	}
	if err := ensureContactInWorkspaceTx(tx, params.ContactID, h.WorkspaceID); err != nil {
		return "", nil, nil, err
	}

	var newAccType string
	if err := tx.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, params.AccountID, h.WorkspaceID).Scan(&newAccType); err != nil {
		return "", nil, nil, err
	}
	newStatus := params.Status
	if params.Type == "EXPENSE" && newAccType == "CREDIT_CARD" {
		newStatus = "paid"
	}
	appliedDueDateIntent := params.DueDateIntent

	var ruleUserID sql.NullString
	var ruleSeriesID sql.NullString
	var effectiveStartSequence int64
	var sourceEffectiveEnd sql.NullInt64
	var totalOccurrences sql.NullInt64
	var ruleDefaultPaymentStatus string
	var ruleDueDatePolicy sql.NullString
	var ruleDueDateOffsetDays sql.NullInt64
	if err := tx.QueryRow(`
		SELECT user_id, series_id, COALESCE(effective_start_sequence, 1), effective_end_sequence,
		       total_occurrences, default_payment_status, due_date_policy, due_date_offset_days
		FROM recurring_rules
		WHERE id = ? AND workspace_id = ?
	`, params.RuleID, h.WorkspaceID).Scan(&ruleUserID, &ruleSeriesID, &effectiveStartSequence, &sourceEffectiveEnd, &totalOccurrences, &ruleDefaultPaymentStatus, &ruleDueDatePolicy, &ruleDueDateOffsetDays); err != nil {
		return "", nil, nil, err
	}

	seriesID := strings.TrimSpace(params.SeriesID)
	if seriesID == "" {
		seriesID = strings.TrimSpace(ruleSeriesID.String)
	}
	if seriesID == "" {
		var err error
		seriesID, err = EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, params.RuleID, params.Now)
		if err != nil {
			return "", nil, nil, err
		}
	} else if ensuredSeriesID, err := EnsureRecurringSeriesForRuleTx(tx, h.WorkspaceID, params.RuleID, params.Now); err != nil {
		return "", nil, nil, err
	} else if ensuredSeriesID != seriesID {
		return "", nil, nil, fmt.Errorf("recurring series mismatch")
	}

	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id,
		       t.category_id, t.status, a.type, t.date, t.date_civil, t.invoice_id, i.status,
		       t.due_date, t.due_date_civil, t.contact_id, t.recurrence_sequence,
		       COALESCE(t.is_recurring_exception, 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN invoices i ON i.id = t.invoice_id AND i.account_id = t.account_id
		WHERE t.workspace_id = ?
		  AND t.series_id = ?
		  AND t.recurrence_sequence >= ?
		ORDER BY t.recurrence_sequence ASC
	`, h.WorkspaceID, seriesID, params.CurrentSequence)
	if err != nil {
		return "", nil, nil, err
	}
	var items []recurringFutureSplitItem
	var currentItem recurringFutureSplitItem
	currentFound := false
	for rows.Next() {
		var item recurringFutureSplitItem
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.categoryID, &item.paymentStatus, &item.accountType, &item.dateUnix, &item.dateCivil, &item.invoiceID, &item.invoiceStatus, &item.dueDate, &item.dueDateCivil, &item.contactID, &item.recurrenceSequence, &item.isException); err != nil {
			rows.Close()
			return "", nil, nil, err
		}
		if item.id == params.CurrentID {
			currentFound = true
			currentItem = item
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return "", nil, nil, err
	}
	currentIsNormallyMutable := recurringFutureSplitItemMutable(currentItem)
	currentIsSelectedProtected := !currentIsNormallyMutable && recurringFutureSelectedProtectedItemMutable(currentItem)
	if !currentFound || (!currentIsNormallyMutable && !currentIsSelectedProtected) {
		return "", nil, nil, errRecurringFutureSplitBlocked
	}
	if err := h.ensureTransactionsNotInvoicePaymentsTx(tx, []string{params.CurrentID}); err != nil {
		return "", nil, nil, err
	}
	currentCivil, err := validatedPersistedCivilDate(currentItem.dateCivil, currentItem.dateUnix)
	if err != nil {
		return "", nil, nil, fmt.Errorf("current occurrence civil date: %w", err)
	}
	competenceChanged := currentCivil != params.Anchor.Civil()
	if params.IsBusiness && appliedDueDateIntent.state == futureCivilPreserve && competenceChanged && !ruleDueDateOffsetDays.Valid {
		for _, item := range items {
			if recurringFutureSplitItemMutable(item) && (item.dueDate.Valid || item.dueDateCivil.Valid) {
				return "", nil, nil, errRecurringFutureCivilConflict
			}
		}
	}

	childDueDatePolicy := ruleDueDatePolicy
	childDueDateOffsetDays := ruleDueDateOffsetDays
	if params.IsBusiness {
		switch appliedDueDateIntent.state {
		case futureCivilPreserve:
			// Keep the source rule contract unchanged in the child.
		case futureCivilClear:
			childDueDatePolicy = sql.NullString{}
			childDueDateOffsetDays = sql.NullInt64{}
		case futureCivilSet:
			childDueDatePolicy = sql.NullString{}
			childDueDateOffsetDays = dueDateOffsetDays(
				OptionalCivilWriteDate{date: appliedDueDateIntent.value, valid: true},
				params.Anchor,
			)
		default:
			return "", nil, nil, fmt.Errorf("invalid future civil intent state %d", appliedDueDateIntent.state)
		}
	}

	var catID interface{}
	if params.CategoryID != "" {
		catID = params.CategoryID
	}
	var destID interface{}
	if params.DestinationID != "" {
		destID = params.DestinationID
	}
	ruleContactRef := recurringFutureSplitContactRef(currentItem, params.ContactID, params.ContactProvided, params.IsBusiness)
	projectionContactID, _ := ruleContactRef.(string)
	ruleStatus := strings.ToUpper(strings.TrimSpace(ruleDefaultPaymentStatus))
	if ruleStatus == "" {
		ruleStatus = "PENDING"
	}
	targetRuleID := uuid.NewString()
	var previousEndDate interface{}
	var previousEndDateCivil interface{}
	if params.CurrentSequence > 1 {
		var previousDate sql.NullInt64
		if err := tx.QueryRow(`
			SELECT date
			FROM transactions
			WHERE workspace_id = ? AND series_id = ? AND recurrence_sequence = ?
			ORDER BY date DESC
			LIMIT 1
		`, h.WorkspaceID, seriesID, params.CurrentSequence-1).Scan(&previousDate); err != nil && err != sql.ErrNoRows {
			return "", nil, nil, err
		}
		if previousDate.Valid {
			previousEndDate = previousDate.Int64
			previousEndDateCivil = LegacyUnixToCivilDateUTC(previousDate.Int64).String()
		}
	}
	result, err := tx.Exec(`
		UPDATE recurring_rules
		SET active = 0,
		    effective_end_sequence = CASE WHEN ? = ? THEN 0 ELSE ? - 1 END,
		    end_date = ?,
		    end_date_civil = ?,
		    updated_at = ?
		WHERE id = ? AND workspace_id = ? AND series_id = ? AND active = 1
	`, params.CurrentSequence, effectiveStartSequence, params.CurrentSequence, previousEndDate, previousEndDateCivil, params.Now, params.RuleID, h.WorkspaceID, seriesID)
	if err != nil {
		return "", nil, nil, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return "", nil, nil, err
	}
	if rowsAffected != 1 {
		return "", nil, nil, fmt.Errorf("source rule deactivation affected %d rows (expected 1)", rowsAffected)
	}
	childStartCivil := params.Anchor.Civil()
	childStartUnix := params.Anchor.LegacyUnix()
	var childEffectiveEnd interface{}
	if sourceEffectiveEnd.Valid {
		childEffectiveEnd = sourceEffectiveEnd.Int64
	}
	if err := execOneTx(tx, `
		INSERT INTO recurring_rules (
			id, workspace_id, user_id, account_id, destination_account_id,
			category_id, type, amount, description, start_date, start_date_civil, frequency,
			default_payment_status, active, total_occurrences, series_id,
			parent_rule_id, effective_start_sequence, effective_end_sequence,
			starts_at, starts_at_civil, contact_id, due_date_policy, due_date_offset_days,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, targetRuleID, h.WorkspaceID, nullStringInterface(ruleUserID), params.AccountID, destID, catID, params.Type, params.Amount, params.Description, childStartUnix, childStartCivil, params.Frequency, ruleStatus, nullInt64Interface(totalOccurrences), seriesID, params.RuleID, params.CurrentSequence, childEffectiveEnd, childStartUnix, childStartCivil, ruleContactRef, nullStringInterface(childDueDatePolicy), nullInt64Interface(childDueDateOffsetDays), params.Now, params.Now); err != nil {
		return "", nil, nil, err
	}
	if err := execOneTx(tx, `
		UPDATE recurring_rules
		SET account_id = ?,
		    destination_account_id = ?,
		    category_id = ?,
		    type = ?,
		    amount = ?,
		    description = ?,
		    start_date = ?,
		    start_date_civil = ?,
		    frequency = ?,
		    default_payment_status = ?,
		    active = 1,
		    series_id = ?,
		    effective_start_sequence = ?,
		    starts_at = ?,
		    starts_at_civil = ?,
		    end_date = NULL,
		    contact_id = ?,
		    generated_until = NULL,
		    updated_at = ?
		WHERE id = ? AND workspace_id = ?
		`, params.AccountID, destID, catID, params.Type, params.Amount, params.Description, childStartUnix, childStartCivil, params.Frequency, ruleStatus, seriesID, params.CurrentSequence, childStartUnix, childStartCivil, ruleContactRef, params.Now, targetRuleID, h.WorkspaceID); err != nil {
		return "", nil, nil, err
	}

	childAnchor := MustCivilWriteDate(childStartCivil)
	touchedIDs := make([]string, 0, len(items))
	oldInvoiceIDs := make([]string, 0)
	for _, item := range items {
		itemIsSelectedProtected := item.id == params.CurrentID && currentIsSelectedProtected
		if !recurringFutureSplitItemMutable(item) && !itemIsSelectedProtected {
			continue
		}
		itemStatus := item.paymentStatus
		if item.id == params.CurrentID && !itemIsSelectedProtected {
			itemStatus = newStatus
		}
		if params.Type == models.TransactionTypeExpense && newAccType == models.AccountTypeCreditCard {
			itemStatus = models.PaymentStatusPaid
		}
		if item.invoiceID.Valid && item.invoiceID.String != "" {
			oldInvoiceIDs = append(oldInvoiceIDs, item.invoiceID.String)
		}
		oldDest := ""
		if item.destinationID.Valid {
			oldDest = item.destinationID.String
		}
		if item.paymentStatus == "paid" {
			if err := services.ReverseBalanceEffect(tx, h.WorkspaceID, item.trType, item.accountType, item.amount, item.accountID, oldDest, params.Now); err != nil {
				return "", nil, nil, err
			}
		}

		newItemCivilDate, err := recurrenceCivilDateFromOffset(childAnchor, params.Frequency, item.recurrenceSequence-params.CurrentSequence)
		if err != nil {
			return "", nil, nil, fmt.Errorf("split occurrence seq %d: civil date: %w", item.recurrenceSequence, err)
		}
		newItemDate := newItemCivilDate.LegacyUnix()
		if err := h.adjustReserveForSingleTransactionUpdateTx(tx, item.id, item.trType, item.categoryID, item.amount, params.Type, params.CategoryID, params.Amount, newItemDate, params.Now, params.AllowBoxOverdraft); err != nil {
			return "", nil, nil, err
		}

		var invoiceID interface{}
		if params.Type == "EXPENSE" && newAccType == "CREDIT_CARD" {
			evidence, err := resolveAutomaticInvoiceForUnixTx(tx, h.WorkspaceID, params.AccountID, newItemDate)
			if err != nil {
				return "", nil, nil, err
			}
			invoiceID = evidence.InvoiceID
		}
		dueDateRef, dueDateCivilRef, err := recurringFutureSplitDueDateRefs(item, newItemCivilDate, competenceChanged, appliedDueDateIntent, childDueDateOffsetDays, params.IsBusiness)
		if err != nil {
			return "", nil, nil, fmt.Errorf("split occurrence seq %d: due date: %w", item.recurrenceSequence, err)
		}
		contactRef := recurringFutureSplitContactRef(item, params.ContactID, params.ContactProvided, params.IsBusiness)

		if err := execOneTx(tx, `
			UPDATE transactions
			SET type = ?,
			    account_id = ?,
			    destination_account_id = ?,
			    amount = ?,
			    date = ?,
			    date_civil = ?,
			    description = ?,
			    notes = ?,
			    attachment_path = ?,
			    category_id = ?,
			    status = ?,
			    due_date = ?,
			    due_date_civil = ?,
			    contact_id = ?,
			    invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END,
			    recurring_rule_id = ?,
			    recurrence_sequence = ?,
			    series_id = ?,
			    updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, params.Type, params.AccountID, destID, params.Amount, newItemDate, newItemCivilDate.Civil(), params.Description, params.Notes, params.AttachmentPath, catID, itemStatus, dueDateRef, dueDateCivilRef, contactRef, invoiceID, targetRuleID, item.recurrenceSequence, seriesID, params.Now, item.id, h.WorkspaceID); err != nil {
			return "", nil, nil, err
		}

		if itemStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, params.Type, newAccType, itemStatus, params.Amount, params.AccountID, params.DestinationID, params.Now); err != nil {
				return "", nil, nil, err
			}
		}
		touchedIDs = append(touchedIDs, item.id)
	}

	ruleTotalOccurrences := (*int64)(nil)
	if totalOccurrences.Valid && totalOccurrences.Int64 > 0 {
		value := totalOccurrences.Int64
		ruleTotalOccurrences = &value
	}
	projectionRule := recurrenceProjectionRule{
		ID:                   targetRuleID,
		SeriesID:             seriesID,
		AccountID:            params.AccountID,
		DestinationAccountID: params.DestinationID,
		CategoryID:           params.CategoryID,
		Type:                 params.Type,
		Amount:               params.Amount,
		Description:          params.Description,
		StartDate:            childStartUnix,
		StartDateCivil:       childStartCivil,
		Frequency:            params.Frequency,
		DefaultPaymentStatus: ruleStatus,
		TotalOccurrences:     ruleTotalOccurrences,
		EffectiveStartSeq:    params.CurrentSequence,
		EffectiveEndSeq:      sourceEffectiveEnd,
		ContactID:            projectionContactID,
		IsBusiness:           params.IsBusiness,
	}
	if params.IsBusiness && childDueDateOffsetDays.Valid {
		projectionRule.HasDueDate = true
		projectionRule.DueDateOffsetDays = childDueDateOffsetDays
	}
	if err := h.generateRecurrenceProjection(tx, projectionRule, params.Now); err != nil {
		return "", nil, nil, err
	}

	return targetRuleID, touchedIDs, oldInvoiceIDs, nil
}

func (h *TransactionHandler) updateFutureRecurringTransactions(tx *sql.Tx, ruleID string, currentID string, fromDate int64, newDate int64, oldCurrentDate int64, tipo string, newAmount int64, descricao string, anotacoes string, attachmentPath string, origemContaID string, destinoContaID string, categoriaID string, newStatus string, dueDateUnix int64, hasDueDate bool, contactID string, now int64, allowBoxOverdraft bool) error {
	delta := newDate - oldCurrentDate
	rows, err := tx.Query(`
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id, t.category_id, t.status, a.type, t.date
		FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ? AND t.recurring_rule_id = ? AND t.id != ? AND t.date >= ?
		  AND COALESCE(t.is_recurring_exception, 0) = 0
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
			evidence, err := resolveAutomaticInvoiceForUnixTx(tx, h.WorkspaceID, origemContaID, newItemDate)
			if err != nil {
				return err
			}
			invoiceID = evidence.InvoiceID
		}

		if err := execOneTx(tx, `
			UPDATE transactions
				SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, due_date = ?, contact_id = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, updated_at = ?
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
		evidence, err := resolveAutomaticInvoiceForUnixTx(tx, h.WorkspaceID, origemContaID, newDate)
		if err != nil {
			return err
		}
		invoiceID = evidence.InvoiceID
	}

	if err := execOneTx(tx, `
		UPDATE transactions
		SET type = ?, account_id = ?, destination_account_id = ?, amount = ?, date = ?, description = ?, notes = ?, attachment_path = ?, category_id = ?, status = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, updated_at = ?
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
	if err := r.ParseForm(); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	paymentAction := strings.TrimSpace(r.FormValue("payment_action"))
	if paymentAction == "" {
		paymentAction = "paid"
	}
	if paymentAction != "paid" && paymentAction != "pending" && paymentAction != "scheduled" && paymentAction != "clear_schedule" {
		http.Error(w, "ação de pagamento inválida", http.StatusBadRequest)
		return
	}
	requestedPaymentAccountID := strings.TrimSpace(r.FormValue("payment_account_id"))
	var paymentDateUnix int64
	var paymentDateCivil string
	var err error
	rawDate := strings.TrimSpace(r.FormValue("payment_date"))
	if paymentAction == "scheduled" {
		rawDate = strings.TrimSpace(r.FormValue("scheduled_payment_date"))
		if rawDate == "" {
			http.Error(w, "data de agendamento obrigatoria", http.StatusBadRequest)
			return
		}
	}
	if rawDate == "" {
		cd := civildate.CivilDateToday(appTimeLocation())
		paymentDateCivil = civildate.FormatCivilDate(cd)
		paymentDateUnix = civildate.CivilDateToLegacyUnixNoonUTC(cd)
	} else {
		cwd, cerr := NewCivilWriteDate(rawDate)
		if cerr != nil {
			http.Error(w, "data inválida", http.StatusBadRequest)
			return
		}
		paymentDateCivil = cwd.Civil()
		paymentDateUnix = cwd.LegacyUnix()
	}

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
	var currentPaidAt sql.NullInt64
	var invoiceID sql.NullString
	var recurringRuleID sql.NullString

	err = tx.QueryRow(`
		SELECT t.type, t.amount, t.account_id, t.destination_account_id, t.status, t.date, a.type, t.paid_at, t.invoice_id, t.recurring_rule_id
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&trType, &amount, &accountID, &destAccountID, &paymentStatus, &txDate, &accType, &currentPaidAt, &invoiceID, &recurringRuleID)
	if err != nil {
		http.Error(w, "transação não encontrada", http.StatusNotFound)
		return
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, []string{id}); err != nil {
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "não é possível alterar lançamento de fatura paga", http.StatusConflict)
			return
		}
		if errors.Is(err, errTransactionMutationTargetInvalid) {
			http.Error(w, "transação não encontrada", http.StatusNotFound)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if paymentAction == "scheduled" || paymentAction == "clear_schedule" {
		if err := validateSchedulableTransaction(trType, paymentStatus, accType, accountID, invoiceID, destAccountID); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		now := time.Now().Unix()
		if paymentAction == "scheduled" {
			err = execOneTx(tx, `
				UPDATE transactions
				SET scheduled_payment_at = ?, scheduled_payment_date_civil = ?, scheduled_payment_created_by = ?, scheduled_payment_executed_at = NULL, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND status = 'pending'
			`, paymentDateUnix, paymentDateCivil, h.UserID, now, id, h.WorkspaceID)
		} else {
			err = execOneTx(tx, `
				UPDATE transactions
				SET scheduled_payment_at = NULL, scheduled_payment_date_civil = NULL, scheduled_payment_created_by = NULL, scheduled_payment_executed_at = NULL, updated_at = ?
				WHERE id = ? AND workspace_id = ? AND status = 'pending'
			`, now, id, h.WorkspaceID)
		}
		if err != nil {
			http.Error(w, "erro ao atualizar agendamento", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		h.respondPaymentStatusChange(w, r, id)
		return
	}

	newStatus := models.PaymentStatusPaid
	if paymentAction == "pending" {
		newStatus = models.PaymentStatusPending
	}
	if accType == models.AccountTypeCreditCard && newStatus == models.PaymentStatusPending {
		newStatus = models.PaymentStatusPaid
	}
	if requestedPaymentAccountID != "" && requestedPaymentAccountID != accountID &&
		!paymentAccountChangeAllowed(trType, accType, accountID, invoiceID, destAccountID, recurringRuleID.Valid) {
		http.Error(w, "esta conta não pode ser trocada durante a confirmação de pagamento", http.StatusUnprocessableEntity)
		return
	}

	// State-setting actions are idempotent. In particular, a repeated
	// payment_action=paid must not reverse balance, rewrite the account or add a
	// duplicate history entry. Missing payment_action defaults to paid; only an
	// explicit payment_action=pending can undo a payment.
	if paymentStatus == newStatus {
		if err := tx.Commit(); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		h.respondPaymentStatusChange(w, r, id)
		return
	}

	nextAccountID := accountID
	nextAccountType := accType
	if requestedPaymentAccountID != "" && requestedPaymentAccountID != accountID {
		canChangeAccount := paymentStatus != models.PaymentStatusPaid && newStatus == models.PaymentStatusPaid &&
			paymentAccountChangeAllowed(trType, accType, accountID, invoiceID, destAccountID, recurringRuleID.Valid)
		if !canChangeAccount {
			http.Error(w, "esta conta não pode ser trocada durante a confirmação de pagamento", http.StatusUnprocessableEntity)
			return
		}
		if err := tx.QueryRow(`
			SELECT type
			FROM accounts
			WHERE id = ? AND workspace_id = ? AND archived_at IS NULL AND type != ?
		`, requestedPaymentAccountID, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&nextAccountType); err != nil {
			http.Error(w, "conta de pagamento não encontrada", http.StatusBadRequest)
			return
		}
		nextAccountID = requestedPaymentAccountID
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

	var nextInvoiceID interface{}
	if trType == "EXPENSE" && accType == "CREDIT_CARD" {
		if invoiceID.Valid {
			// A status toggle does not reroute a transaction already attached to
			// an invoice. Historical CLOSED/PAID links are preserved verbatim.
			nextInvoiceID = invoiceID.String
		} else {
			evidence, err := resolveAutomaticInvoiceForUnixTx(tx, h.WorkspaceID, accountID, txDate)
			if err != nil {
				if errors.Is(err, errInvoiceStateTransitionConflict) {
					http.Error(w, "estado financeiro da fatura não permite esta alteração", http.StatusConflict)
				} else {
					http.Error(w, "erro na fatura", http.StatusInternalServerError)
				}
				return
			}
			nextInvoiceID = evidence.InvoiceID
		}
	}

	var paidAt interface{}
	var paidDateCivil interface{}
	if newStatus == "paid" {
		paidAt = paymentDateUnix
		paidDateCivil = paymentDateCivil
	}

	err = execOneTx(tx, `
		UPDATE transactions
		SET status = ?, account_id = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, updated_at = ?, paid_at = ?, paid_date_civil = ?,
			scheduled_payment_at = NULL, scheduled_payment_date_civil = NULL, scheduled_payment_created_by = NULL, scheduled_payment_executed_at = NULL
		WHERE id = ? AND workspace_id = ? AND status = ?
	`, newStatus, nextAccountID, nextInvoiceID, now, paidAt, paidDateCivil, id, h.WorkspaceID, paymentStatus)
	if err != nil {
		http.Error(w, "erro ao atualizar status", http.StatusInternalServerError)
		return
	}

	if err := h.insertTransactionStatusHistoryTx(tx, id, paymentStatus, newStatus, currentPaidAt, paidAt, now); err != nil {
		http.Error(w, "erro ao registrar histórico", http.StatusInternalServerError)
		return
	}

	if newStatus == "paid" {
		if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, trType, nextAccountType, newStatus, amount, nextAccountID, dest, now); err != nil {
			http.Error(w, "erro ao aplicar saldo", http.StatusInternalServerError)
			return
		}
	}

	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, []string{id}, now); err != nil {
		respondInvoiceCompositionError(w, err)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	h.respondPaymentStatusChange(w, r, id)
}

func (h *TransactionHandler) respondPaymentStatusChange(w http.ResponseWriter, r *http.Request, id string) {
	context := r.FormValue("context")
	if context == "contas" {
		w.Header().Set("HX-Reswap", "none")
		w.Header().Set("HX-Trigger", `{"refreshFinancials":null,"closePaymentConfirmModal":null}`)
		w.WriteHeader(http.StatusOK)
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

func validateSchedulableTransaction(trType, paymentStatus, accountType, accountID string, invoiceID sql.NullString, destAccountID sql.NullString) error {
	if paymentStatus == models.PaymentStatusPaid {
		return fmt.Errorf("lançamento já pago não pode ser agendado")
	}
	if trType != models.TransactionTypeIncome && trType != models.TransactionTypeExpense {
		return fmt.Errorf("transferências não podem ser agendadas")
	}
	if destAccountID.Valid && strings.TrimSpace(destAccountID.String) != "" && destAccountID.String != accountID {
		return fmt.Errorf("transferências não podem ser agendadas")
	}
	if accountType == models.AccountTypeCreditCard || invoiceID.Valid {
		return fmt.Errorf("faturas e compras de cartão não podem ser agendadas")
	}
	return nil
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
	if validCount != len(ids) {
		_ = h.logSecurityTamperingTx(tx, r, "bulk_delete_workspace_scope_violation", ids)
		_ = tx.Rollback()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
			return
		}
		if errors.Is(err, errTransactionMutationTargetInvalid) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
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
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
			return
		}
		if errors.Is(err, errInvoiceStateTransitionConflict) {
			respondInvoiceCompositionError(w, err)
			return
		}
		http.Error(w, "erro ao remover em lote", http.StatusInternalServerError)
		return
	}
	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, ids, now); err != nil {
		respondInvoiceCompositionError(w, err)
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
			IDs              []string `json:"ids"`
			StatusPagamento  string   `json:"status_pagamento"`
			Status           string   `json:"status"`
			PaymentAccountID string   `json:"payment_account_id"`
		}
		if err = json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "payload inválido", http.StatusBadRequest)
			return
		}
		ids = payload.IDs
		if strings.TrimSpace(payload.PaymentAccountID) != "" {
			http.Error(w, "troca de conta não é permitida em lote", http.StatusBadRequest)
			return
		}
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
		if strings.TrimSpace(r.FormValue("payment_account_id")) != "" {
			http.Error(w, "troca de conta não é permitida em lote", http.StatusBadRequest)
			return
		}
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

	var paymentDateUnix int64
	var paymentDateCivil string
	rawDate := strings.TrimSpace(r.FormValue("payment_date"))
	if rawDate == "" {
		cd := civildate.CivilDateToday(appTimeLocation())
		paymentDateCivil = civildate.FormatCivilDate(cd)
		paymentDateUnix = civildate.CivilDateToLegacyUnixNoonUTC(cd)
	} else {
		cwd, cerr := NewCivilWriteDate(rawDate)
		if cerr != nil {
			http.Error(w, "data inválida", http.StatusBadRequest)
			return
		}
		paymentDateCivil = cwd.Civil()
		paymentDateUnix = cwd.LegacyUnix()
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
	if validCount != len(ids) {
		_ = h.logSecurityTamperingTx(tx, r, "bulk_update_workspace_scope_violation", ids)
		_ = tx.Rollback()
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := h.ensureTransactionsMutableByInvoiceStatusTx(tx, ids); err != nil {
		if errors.Is(err, errInvoicePaymentTransactionProtected) {
			respondInvoicePaymentTransactionProtected(w)
			return
		}
		if invoiceStatusMutationBlocked(err) {
			http.Error(w, "não é possível alterar lançamentos de fatura paga", http.StatusConflict)
			return
		}
		if errors.Is(err, errTransactionMutationTargetInvalid) {
			http.Error(w, "forbidden", http.StatusForbidden)
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
		paidAt        sql.NullInt64
	}
	query := `
		SELECT t.id, t.type, t.amount, t.account_id, t.destination_account_id, t.status, a.type, t.date, t.paid_at
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
		if err := rows.Scan(&item.id, &item.trType, &item.amount, &item.accountID, &item.destinationID, &item.status, &item.accountType, &item.dateUnix, &item.paidAt); err != nil {
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
			evidence, err := resolveAutomaticInvoiceForUnixTx(tx, h.WorkspaceID, item.accountID, item.dateUnix)
			if err != nil {
				if errors.Is(err, errInvoiceStateTransitionConflict) {
					http.Error(w, "estado financeiro da fatura não permite esta alteração", http.StatusConflict)
				} else {
					http.Error(w, "erro na fatura", http.StatusInternalServerError)
				}
				return
			}
			invoiceID = evidence.InvoiceID
		}
		var paidAt interface{}
		var paidDateCivil interface{}
		if newStatus == "paid" {
			paidAt = paymentDateUnix
			paidDateCivil = paymentDateCivil
		}
		if err := execOneTx(tx, `
			UPDATE transactions
			SET status = ?, invoice_id = CASE WHEN invoice_override_id IS NOT NULL THEN invoice_override_id ELSE ? END, updated_at = ?, paid_at = ?, paid_date_civil = ?,
				scheduled_payment_at = NULL, scheduled_payment_date_civil = NULL, scheduled_payment_created_by = NULL, scheduled_payment_executed_at = NULL
			WHERE id = ? AND workspace_id = ?
		`, newStatus, invoiceID, now, paidAt, paidDateCivil, item.id, h.WorkspaceID); err != nil {
			http.Error(w, "erro ao atualizar em lote", http.StatusInternalServerError)
			return
		}
		if err := h.insertTransactionStatusHistoryTx(tx, item.id, item.status, newStatus, item.paidAt, paidAt, now); err != nil {
			http.Error(w, "erro ao registrar histórico", http.StatusInternalServerError)
			return
		}
		if newStatus == "paid" {
			if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, item.trType, item.accountType, newStatus, item.amount, item.accountID, dest, now); err != nil {
				http.Error(w, "erro ao ajustar saldo", http.StatusInternalServerError)
				return
			}
		}
	}

	if err := reconcileInvoicesForTransactionsTx(tx, h.WorkspaceID, ids, now); err != nil {
		respondInvoiceCompositionError(w, err)
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
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	if err := h.ensureTransactionsNotInvoicePaymentsTx(tx, ids); err != nil {
		return err
	}

	query := `
		SELECT
			t.id,
			t.account_id,
			t.invoice_id,
			i.id,
			i.account_id,
			i.status,
			ia.workspace_id
		FROM transactions t
		JOIN accounts ta
		  ON ta.id = t.account_id
		 AND ta.workspace_id = t.workspace_id
		LEFT JOIN invoices i
		  ON i.id = t.invoice_id
		LEFT JOIN accounts ia
		  ON ia.id = i.account_id
		WHERE t.workspace_id = ?
		  AND t.id IN (` + sqlPlaceholders(len(ids)) + `)
		ORDER BY t.id ASC
	`
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, h.WorkspaceID)
	args = appendStrings(args, ids)

	type resolvedTransaction struct {
		id                 string
		accountID          string
		invoiceID          sql.NullString
		resolvedInvoiceID  sql.NullString
		invoiceAccountID   sql.NullString
		invoiceStatus      sql.NullString
		invoiceWorkspaceID sql.NullString
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		return err
	}
	resolved := make(map[string]resolvedTransaction, len(ids))
	for rows.Next() {
		var item resolvedTransaction
		if err := rows.Scan(
			&item.id,
			&item.accountID,
			&item.invoiceID,
			&item.resolvedInvoiceID,
			&item.invoiceAccountID,
			&item.invoiceStatus,
			&item.invoiceWorkspaceID,
		); err != nil {
			_ = rows.Close()
			return err
		}
		resolved[item.id] = item
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(resolved) != len(ids) {
		return errTransactionMutationTargetInvalid
	}

	for _, id := range ids {
		item := resolved[id]
		if !item.invoiceID.Valid {
			continue
		}
		if item.invoiceID.String == "" ||
			!item.resolvedInvoiceID.Valid || item.resolvedInvoiceID.String != item.invoiceID.String ||
			!item.invoiceAccountID.Valid || item.invoiceAccountID.String != item.accountID ||
			!item.invoiceWorkspaceID.Valid || item.invoiceWorkspaceID.String != h.WorkspaceID ||
			!item.invoiceStatus.Valid {
			return errTransactionInvoiceLinkInvalid
		}
		switch item.invoiceStatus.String {
		case models.InvoiceStatusOpen, models.InvoiceStatusClosed:
			continue
		case models.InvoiceStatusPaid:
			return errPaidInvoiceMutationBlocked
		default:
			return errTransactionInvoiceLinkInvalid
		}
	}
	return nil
}

func (h *TransactionHandler) ensureTransactionsNotInvoicePaymentsTx(tx *sql.Tx, ids []string) error {
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return nil
	}

	// The reference is a historical custody boundary: reversed payments remain
	// protected, so this query deliberately has no reversed_at predicate.
	query := `
		SELECT t.id
		FROM invoice_payments ip
		JOIN transactions t
		  ON (t.id = ip.transaction_id OR t.id = ip.reversal_transaction_id)
		 AND t.workspace_id = ip.workspace_id
		WHERE ip.workspace_id = ?
		  AND t.workspace_id = ?
		  AND t.id IN (` + sqlPlaceholders(len(ids)) + `)
		ORDER BY t.id ASC
		LIMIT 1
	`
	args := make([]interface{}, 0, len(ids)+2)
	args = append(args, h.WorkspaceID, h.WorkspaceID)
	args = appendStrings(args, ids)

	var protectedID string
	err := tx.QueryRow(query, args...).Scan(&protectedID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return errInvoicePaymentTransactionProtected
}

func respondInvoicePaymentTransactionProtected(w http.ResponseWriter) {
	http.Error(w, "pagamentos de fatura devem ser corrigidos pelo fluxo próprio da fatura", http.StatusConflict)
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
		SELECT t.id, t.type, t.amount, t.date, t.due_date, t.created_at, t.description, t.status,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(NULLIF(t.recurrence_sequence, 0), t.installment_number, 0),
			COALESCE(rr.total_occurrences, t.total_installments, 0),
			a.name AS account_name,
			COALESCE(ct.name, '') AS contact_name,
			COALESCE(u.name, 'Usuário removido') AS user_name,
			CASE WHEN t.due_date IS NOT NULL THEN 1 ELSE 0 END AS has_due_date,
			CASE WHEN (t.recurring_rule_id IS NOT NULL OR t.total_installments > 1) THEN 1 ELSE 0 END AS is_series,
			t.paid_at,
			t.scheduled_payment_at,
			t.account_id,
			a.type,
			t.invoice_id,
			t.destination_account_id,
			COALESCE(destination_account.name, '') AS destination_account_name,
			CASE WHEN t.recurring_rule_id IS NOT NULL THEN 1 ELSE 0 END AS has_recurring_rule
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN accounts destination_account ON destination_account.id = t.destination_account_id AND destination_account.workspace_id = t.workspace_id
		LEFT JOIN users u ON u.id = t.user_id
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

	now := appMonthStartUTC(time.Now())
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
	data.UserName, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
	data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == "business"
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, data.IsBusiness)
	if data.IsBusiness {
		data.Title = "Fluxo de Caixa"
	}
	statuses := filters.normalizedStatuses()
	data.StatusPendenteOn = containsString(statuses, "pendente")
	data.StatusVencidoOn = containsString(statuses, "vencido")
	data.StatusAgendadoOn = containsString(statuses, "agendado")
	data.StatusPagoOn = containsString(statuses, "pago")
	data.HasActiveFilters = filters.hasActiveSelections(accountFilter)
	data.FilterAccounts, _ = h.queryFilterAccounts()
	data.FilterCategories, _ = h.queryFormCategories()
	data.PaymentAccounts, _ = queryPaymentAccountOptions(h.DB, h.WorkspaceID)
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
	defaultStatus := len(statuses) == 4 && containsString(statuses, "pendente") && containsString(statuses, "vencido") && containsString(statuses, "agendado") && containsString(statuses, "pago")
	if !defaultStatus {
		for _, s := range statuses {
			label := s
			switch s {
			case "pendente":
				label = "Pendentes"
			case "vencido":
				label = "Vencidos"
			case "agendado":
				label = "Agendados"
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

	filterAccountType := ""
	if accountFilter != "" {
		_ = h.DB.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, accountFilter, h.WorkspaceID).Scan(&filterAccountType)
	}

	query := `
		SELECT t.id, t.type, t.amount, t.date, t.due_date, t.created_at, t.description, t.status,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(NULLIF(t.recurrence_sequence, 0), t.installment_number, 0),
			COALESCE(rr.total_occurrences, t.total_installments, 0),
			a.name AS account_name,
			COALESCE(ct.name, '') AS contact_name,
			COALESCE(u.name, 'Usuário removido') AS user_name,
			CASE WHEN t.due_date IS NOT NULL THEN 1 ELSE 0 END AS has_due_date,
			CASE WHEN (t.recurring_rule_id IS NOT NULL OR t.total_installments > 1) THEN 1 ELSE 0 END AS is_series,
			t.paid_at,
			t.scheduled_payment_at,
			t.account_id,
			a.type,
			t.invoice_id,
			t.destination_account_id,
			COALESCE(destination_account.name, '') AS destination_account_name,
			CASE WHEN t.recurring_rule_id IS NOT NULL THEN 1 ELSE 0 END AS has_recurring_rule
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN accounts destination_account ON destination_account.id = t.destination_account_id AND destination_account.workspace_id = t.workspace_id
		LEFT JOIN users u ON u.id = t.user_id
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
				COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount - COALESCE((
					SELECT SUM(r.amount_cents) FROM transaction_refunds r
					WHERE r.workspace_id = t.workspace_id AND r.transaction_id = t.id AND r.reversed_at IS NULL
				), 0) ELSE 0 END), 0) - `+invoiceGeneralRefundTotalIASQL+` as total,
				`+invoiceEffectivePaidSQLExpr+` as effective_paid,
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
			todayUnix := businessDayStartUnix(time.Now())
			realInvoiceAccounts := map[string]struct{}{}
			var invoiceCandidates []InvoiceRow
			for invoiceRows.Next() {
				var inv InvoiceRow
				var providerSlug, color string
				var closingUnix int64
				var effectivePaid int64
				if err := invoiceRows.Scan(&inv.ID, &inv.AccountID, &inv.AccountName, &providerSlug, &color, &inv.Reference, &closingUnix, &inv.Status, &inv.DueDate, &inv.Total, &effectivePaid, &inv.ItemCount); err != nil {
					continue
				}
				realInvoiceAccounts[inv.AccountID] = struct{}{}
				normalizedSlug := normalizeAccountProviderSlug(providerSlug)
				inv.CardIcon = accountVisualByProvider(normalizedSlug, models.AccountTypeCreditCard)
				inv.CardColor = normalizeHexColor(color, "#6B7280")
				inv.CardProviderMark = accountProviderMark(providerSlug, inv.AccountName)
				inv.CardProviderLogoPath = accountProviderLogoPathForAccount(providerSlug, inv.AccountName)
				prepareLancamentosInvoiceRow(&inv, effectivePaid, todayUnix)
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
				invoiceCandidates = append(invoiceCandidates, inv)
			}
			invoiceRows.Close()

			for _, inv := range invoiceCandidates {
				if !h.lancamentosInvoiceMatchesNonStatusFilters(inv, filters) {
					continue
				}
				if !lancamentosInvoiceMatchesStatus(inv, filters, todayUnix) {
					continue
				}
				data.Invoices = append(data.Invoices, inv)
			}

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
					if _, ok := realInvoiceAccounts[card.accountID]; ok {
						found = true
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
						inv.CardProviderLogoPath = accountProviderLogoPathForAccount(card.providerSlug, inv.AccountName)
						inv.Reference = reference
						inv.DueDate = targetDue.Unix()
						inv.Total = total
						inv.ItemCount = count
						inv.Status = models.InvoiceStatusOpen
						prepareLancamentosInvoiceRow(&inv, 0, todayUnix)
						inv.DateLabel = formatDateLabel(inv.DueDate)

						months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
						inv.ReferenceLabel = fmt.Sprintf("%s/%d", months[refMonth-1], refYear)
						inv.FaturaURL = fmt.Sprintf("/cartoes/%s/faturas?mes=%d&ano=%d", inv.AccountID, refMonth, refYear)
						if !h.lancamentosInvoiceMatchesNonStatusFilters(inv, filters) {
							continue
						}
						if !lancamentosInvoiceMatchesStatus(inv, filters, todayUnix) {
							continue
						}
						data.Invoices = append(data.Invoices, inv)
					}
				}
				cardRows.Close()
			}

		}
	}
	data.ResumoStatus = buildLancamentosStatusSummary(data.Transactions, data.Invoices)

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

func prepareLancamentosInvoiceRow(inv *InvoiceRow, effectivePaid, todayUnix int64) {
	if inv == nil {
		return
	}
	inv.Paid = effectivePaid
	inv.Pending = CalculateInvoicePendingAmount(inv.Total, inv.Paid)
	inv.TotalMoney = MoneyMinor(inv.Total)
	inv.PaidMoney = MoneyMinor(inv.Paid)
	inv.PendingMoney = MoneyMinor(inv.Pending)
	inv.HasPartialPayment = inv.Paid > 0 && inv.Pending > 0
	inv.IsOverdue = inv.Pending > 0 && inv.DueDate > 0 && inv.DueDate < todayUnix
	switch {
	case inv.Status == models.InvoiceStatusPaid:
		inv.BadgeLabel = "Paga"
	case inv.HasPartialPayment:
		inv.BadgeLabel = "Parcial"
	case inv.Status == models.InvoiceStatusClosed:
		inv.BadgeLabel = "Fechada"
	default:
		inv.BadgeLabel = "Aberta"
	}
}

func lancamentosInvoiceMatchesStatus(inv InvoiceRow, filters LancamentosFilters, todayUnix int64) bool {
	statuses := filters.normalizedStatuses()
	if hasAllLancamentosStatuses(statuses) {
		return true
	}
	pendingEligible := inv.Status == models.InvoiceStatusOpen || inv.Status == models.InvoiceStatusClosed
	for _, status := range statuses {
		switch status {
		case "pendente":
			if pendingEligible && inv.Pending > 0 {
				return true
			}
		case "vencido":
			if pendingEligible && inv.Pending > 0 && inv.DueDate > 0 && inv.DueDate < todayUnix {
				return true
			}
		case "pago":
			if inv.Status == models.InvoiceStatusPaid {
				return true
			}
		case "agendado":
			// Faturas nao possuem agendamento na beta.4.
		}
	}
	return false
}

func (h *TransactionHandler) lancamentosInvoiceMatchesNonStatusFilters(inv InvoiceRow, filters LancamentosFilters) bool {
	if len(filters.OrigemIDs) > 0 && !containsString(filters.OrigemIDs, inv.AccountID) {
		return false
	}
	if len(filters.DestinoIDs) > 0 {
		return false
	}
	if !lancamentosInvoiceMatchesTypeSelection(filters.normalizedTypes()) {
		return false
	}
	if !lancamentosInvoiceNeedsItemFilter(filters) {
		return true
	}
	if strings.HasPrefix(inv.ID, "virtual-") {
		return h.lancamentosVirtualInvoiceHasMatchingItems(inv, filters)
	}
	return h.lancamentosInvoiceHasMatchingItems(inv.ID, filters)
}

func lancamentosInvoiceMatchesTypeSelection(types []string) bool {
	if len(types) == 0 {
		return true
	}
	for _, txType := range types {
		if txType == "despesa" || txType == "fixo" || txType == "parcelado" {
			return true
		}
	}
	return false
}

func lancamentosInvoiceNeedsItemFilter(filters LancamentosFilters) bool {
	if len(filters.Categorias) > 0 || filters.Busca != "" {
		return true
	}
	for _, txType := range filters.normalizedTypes() {
		if txType == "fixo" || txType == "parcelado" {
			return true
		}
	}
	return false
}

func (h *TransactionHandler) lancamentosInvoiceHasMatchingItems(invoiceID string, filters LancamentosFilters) bool {
	clauses := []string{`t.workspace_id = ?`, `t.invoice_id = ?`, `t.type = 'EXPENSE'`}
	args := []interface{}{h.WorkspaceID, invoiceID}
	return h.lancamentosInvoiceItemMatchExists(clauses, args, filters)
}

func (h *TransactionHandler) lancamentosVirtualInvoiceHasMatchingItems(inv InvoiceRow, filters LancamentosFilters) bool {
	refYear, refMonth, err := parseInvoiceReference(inv.Reference)
	if err != nil {
		return false
	}
	var closingDay, dueDay int64
	if err := h.DB.QueryRow(`SELECT closing_day, due_day FROM credit_cards WHERE account_id = ?`, inv.AccountID).Scan(&closingDay, &dueDay); err != nil {
		return false
	}
	cycleStart, cycleEnd := invoiceCycleBoundsForReference(refYear, refMonth, closingDay, dueDay)
	clauses := []string{
		`t.workspace_id = ?`,
		`t.account_id = ?`,
		`t.invoice_id IS NULL`,
		`t.type = 'EXPENSE'`,
		`t.date >= ?`,
		`t.date < ?`,
	}
	args := []interface{}{h.WorkspaceID, inv.AccountID, cycleStart, cycleEnd}
	return h.lancamentosInvoiceItemMatchExists(clauses, args, filters)
}

func (h *TransactionHandler) lancamentosInvoiceItemMatchExists(clauses []string, args []interface{}, filters LancamentosFilters) bool {
	typeClauses := lancamentosInvoiceItemTypeClauses(filters.normalizedTypes())
	if len(typeClauses) > 0 {
		clauses = append(clauses, "("+strings.Join(typeClauses, " OR ")+")")
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
			`UNACCENT(COALESCE(c.name, '')) LIKE UNACCENT(?)`,
			`UNACCENT(COALESCE(a.name, '')) LIKE UNACCENT(?)`,
		}
		args = append(args, like, like, like, like)
		clauses = append(clauses, "("+strings.Join(searchClauses, " OR ")+")")
	}

	query := `
		SELECT COUNT(1)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		WHERE ` + strings.Join(clauses, " AND ")
	var count int
	if err := h.DB.QueryRow(query, args...).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

func lancamentosInvoiceItemTypeClauses(types []string) []string {
	var clauses []string
	hasExpense := false
	for _, txType := range types {
		if txType == "despesa" {
			hasExpense = true
			break
		}
	}
	if len(types) == 0 || hasExpense {
		return clauses
	}
	for _, txType := range types {
		switch txType {
		case "fixo":
			clauses = append(clauses, `t.recurring_rule_id IS NOT NULL`)
		case "parcelado":
			clauses = append(clauses, `t.total_installments > 1`)
		}
	}
	return clauses
}

func hasAllLancamentosStatuses(statuses []string) bool {
	return containsString(statuses, "pendente") &&
		containsString(statuses, "vencido") &&
		containsString(statuses, "agendado") &&
		containsString(statuses, "pago")
}

func (h *TransactionHandler) lancamentosWhereClause(monthStart, monthEnd int64, accountFilter string, filters LancamentosFilters) (string, []interface{}) {
	loc := appTimeLocation()
	nowTZ := time.Now().In(loc)
	today := time.Date(nowTZ.Year(), nowTZ.Month(), nowTZ.Day(), 0, 0, 0, 0, time.UTC).Unix()
	clauses := []string{`WHERE t.workspace_id = ?`, `COALESCE(t.due_date, t.date) >= ?`, `COALESCE(t.due_date, t.date) <= ?`}
	args := []interface{}{h.WorkspaceID, monthStart, monthEnd}
	if accountFilter != "" {
		clauses = append(clauses, `(t.account_id = ? OR (t.type = 'TRANSFER' AND t.destination_account_id = ?))`)
		args = append(args, accountFilter, accountFilter)
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
				statusClauses = append(statusClauses, `(t.status = 'pending' AND t.scheduled_payment_at IS NULL AND COALESCE(t.due_date, t.date) >= ?)`)
				args = append(args, today)
			case "vencido":
				statusClauses = append(statusClauses, `(t.status = 'pending' AND t.scheduled_payment_at IS NULL AND COALESCE(t.due_date, t.date) < ?)`)
				args = append(args, today)
			case "agendado":
				statusClauses = append(statusClauses, `(t.status = 'pending' AND t.scheduled_payment_at IS NOT NULL AND t.scheduled_payment_executed_at IS NULL)`)
			case "pago":
				statusClauses = append(statusClauses, `(t.status = 'paid')`)
			}
		}
		if len(statusClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(statusClauses, " OR ")+")")
		}
	}
	if len(filters.OrigemIDs) > 0 {
		placeholders := sqlPlaceholders(len(filters.OrigemIDs))
		clauses = append(clauses, `(t.account_id IN (`+placeholders+`) OR (t.type = 'TRANSFER' AND t.destination_account_id IN (`+placeholders+`)))`)
		args = appendStrings(args, filters.OrigemIDs)
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
		return []string{"pendente", "vencido", "agendado", "pago"}
	}
	seen := make(map[string]struct{}, len(f.Situacoes))
	var statuses []string
	for _, raw := range f.Situacoes {
		switch raw {
		case "aberto":
			raw = "pendente"
		}
		if raw != "pendente" && raw != "vencido" && raw != "agendado" && raw != "pago" {
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
	defaultStatus := len(statuses) == 4 && containsString(statuses, "pendente") && containsString(statuses, "vencido") && containsString(statuses, "agendado") && containsString(statuses, "pago")
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

func recurrenceDateFromOffset(startUnix int64, frequency string, offset int64) int64 {
	if offset <= 0 {
		return startUnix
	}
	occurrence := time.Unix(startUnix, 0).UTC()
	for i := int64(0); i < offset; i++ {
		prev := occurrence.Unix()
		occurrence = nextRecurrenceDate(occurrence, frequency)
		if occurrence.Unix() <= prev {
			break
		}
	}
	return occurrence.Unix()
}

func (h *TransactionHandler) paidNetCashFlowBetween(start, end time.Time) int64 {
	return calcPaidNetCashFlowBetween(h.DB, h.WorkspaceID, start, end)
}

// Package-level accumulated balance helpers (shared by /lancamentos and /relatorios).

func calcProjectedAccumulatedBalance(db *sql.DB, workspaceID string, currentBalance int64, selectedMonth time.Time) int64 {
	today := AppCivilDateUTC(time.Now())
	tomorrow := today.AddDate(0, 0, 1)
	currentMonth := appMonthStartUTC(time.Now())
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
		SELECT COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END), 0)
		FROM transactions t
		WHERE t.workspace_id = ?
		  AND t.date >= ?
		  AND t.date < ?
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
		SELECT COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END), 0)
		FROM transactions t
		WHERE t.workspace_id = ? AND t.status = 'paid' AND t.date >= ? AND t.date < ?
	`, workspaceID, start.Unix(), end.Unix()).Scan(&incomeTotal, &expenseTotal)
	return incomeTotal - expenseTotal
}

func buildMonthOptions(accountFilter string, selectedMonth time.Time, filters LancamentosFilters) []MonthOption {
	return buildMonthOptionsAt(accountFilter, selectedMonth, filters, time.Now())
}

func buildMonthOptionsAt(accountFilter string, selectedMonth time.Time, filters LancamentosFilters, now time.Time) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	currentMonth := appMonthStartUTC(now)

	// Keep the selected competence in the sixth position. This makes it
	// immediately visible on mobile and lets the rail cross year boundaries
	// without changing the URL or filter contract.
	firstMonth := selectedMonth.AddDate(0, -5, 0)
	options := make([]MonthOption, 0, 12)
	for offset := 0; offset < 12; offset++ {
		m := firstMonth.AddDate(0, offset, 0)
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
	today := AppCivilDateUTC(time.Now())
	yesterday := today.AddDate(0, 0, -1)
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}

	for rows.Next() {
		var row TransactionRow
		var trType string
		var amount int64
		var dateUnix int64
		var dueDate sql.NullInt64
		var catName, catIcon, catColor sql.NullString
		var hasDueDate int64
		var isSeries int64
		var paidAt sql.NullInt64
		var scheduledPaymentAt sql.NullInt64
		var accountType string
		var invoiceID, destinationAccountID sql.NullString
		var hasRecurringRule int64

		if err := rows.Scan(&row.ID, &trType, &amount, &dateUnix, &dueDate, &row.CreatedAt, &row.Description, &row.PaymentStatus,
			&catName, &catIcon, &catColor,
			&row.InstallmentNumber, &row.TotalInstallments,
			&row.AccountName, &row.ContactName, &row.Author, &hasDueDate, &isSeries, &paidAt, &scheduledPaymentAt,
			&row.PaymentAccountID, &accountType, &invoiceID, &destinationAccountID, &row.DestinationAccountName, &hasRecurringRule); err != nil {
			continue
		}

		row.IsBusiness = isBusiness
		row.Type = trType
		row.Amount = amount
		row.PaymentAccountChangeAllowed = paymentAccountChangeAllowed(trType, accountType, row.PaymentAccountID, invoiceID, destinationAccountID, hasRecurringRule == 1)
		row.CompetenceDate = dateUnix
		if dueDate.Valid {
			row.DueDate = &dueDate.Int64
		}
		row.HasDueDate = dueDate.Valid
		row.DueDateDiffers = dueDate.Valid && dueDate.Int64 != dateUnix

		displayDate := dateUnix
		if dueDate.Valid {
			displayDate = dueDate.Int64
		}
		row.Date = displayDate

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
		row.UsesDueDate = isBusiness && row.HasDueDate && row.DueDateDiffers
		if row.UsesDueDate {
			row.DisplayDateLabel = formatDateLabelFromUnix(displayDate)
			row.CompetenciaDateLabel = formatDateLabelFromUnix(dateUnix)
		} else if row.HasDueDate {
			row.DisplayDateLabel = formatDateLabelFromUnix(displayDate)
		}

		if paidAt.Valid {
			row.PaidAt = &paidAt.Int64
			row.PaidAtDisplay = formatAppDateLabelFromUnix(paidAt.Int64)
		}
		if scheduledPaymentAt.Valid {
			row.ScheduledPaymentAt = &scheduledPaymentAt.Int64
			row.ScheduledPaymentAtDisplay = formatAppDateLabelFromUnix(scheduledPaymentAt.Int64)
		}

		t := time.Unix(displayDate, 0).UTC()
		localTime := time.Unix(displayDate, 0)
		competenceLocalTime := time.Unix(dateUnix, 0)
		row.TimeDisplay = localTime.Format("15:04")
		row.DateInput = competenceLocalTime.Format("2006-01-02")
		dateOnly := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)

		row.IsScheduled = row.IsPending && row.ScheduledPaymentAt != nil
		row.IsOverdue = row.PaymentStatus != "paid" && !row.IsScheduled && dateOnly.Before(today)
		visual := transactionVisualStatus(row.Type, row.PaymentStatus, displayDate, row.ScheduledPaymentAt)
		row.PaymentIcon = visual.Icon
		row.PaymentTone = visual.Tone
		row.PaymentTitle = visual.Title
		if row.Type == "INCOME" {
			if row.PaymentStatus == "paid" {
				row.PaymentConfirm = "Remover o recebimento deste lançamento e voltar para pendente?"
				row.PaymentConfirmTitle = "Confirmar alteração de status"
			} else {
				row.PaymentConfirm = "Marcar este lançamento como recebido?"
				row.PaymentConfirmTitle = "Confirmar recebimento"
			}
		} else if row.PaymentStatus == "paid" {
			row.PaymentConfirm = "Remover o pagamento deste lançamento e voltar para pendente?"
			row.PaymentConfirmTitle = "Confirmar alteração de status"
		} else {
			row.PaymentConfirm = "Marcar este lançamento como pago?"
			row.PaymentConfirmTitle = "Confirmar pagamento"
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
	today := AppCivilDateUTC(time.Now())
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

func buildCreditCardAvailableLimitSubtitle(db *sql.DB, workspaceID, accountID string, creditLimit int64) string {
	available := creditLimit
	if creditLimit > 0 {
		if outstanding, err := sumCardOutstandingLimit(db, workspaceID, accountID); err == nil {
			available = creditLimit - outstanding
			if available < 0 {
				available = 0
			}
		}
	}

	var dueLabel string
	if dueUnix, err := nextOpenInvoiceDueDateForFormAccount(db, workspaceID, accountID); err == nil && dueUnix > 0 {
		dueLabel = formatDueDateCompact(dueUnix)
	}

	if dueLabel != "" {
		return "Limite " + formatCurrencyLabel(available) + " \u2022 vence " + dueLabel
	}
	return "Limite " + formatCurrencyLabel(available)
}

func nextOpenInvoiceDueDateForFormAccount(db *sql.DB, workspaceID, accountID string) (int64, error) {
	var dueUnix int64
	err := db.QueryRow(`
		SELECT i.due_date
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.account_id = ?
		  AND a.workspace_id = ?
		  AND a.type = ?
		  AND i.status = ?
		  AND i.due_date > 0
		ORDER BY i.reference ASC, i.due_date ASC
		LIMIT 1
	`, accountID, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen).Scan(&dueUnix)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return dueUnix, nil
}

func formatDueDateCompact(dueUnix int64) string {
	if dueUnix <= 0 {
		return ""
	}
	monthsShort := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	t := time.Unix(dueUnix, 0)
	return fmt.Sprintf("%02d/%s", t.Day(), monthsShort[t.Month()-1])
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
	ParentID           string
	ParentName         string
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
			ParentID:           c.ParentID,
			ParentName:         c.ParentName,
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
	MacroGroups      []categorycontract.MacroGroup
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
	macroGroups := categoryMacroGroupsForTemplate(isBusiness, typ)
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
	macroGroups := categoryMacroGroupsForTemplate(isBusiness, typ)
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

	now := appTime(time.Now())
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

func (h *TransactionHandler) insertTransactionStatusHistoryTx(tx *sql.Tx, transactionID, previousStatus, newStatus string, previousPaidAt sql.NullInt64, newPaidAtVal interface{}, now int64) error {
	return h.insertTransactionStatusHistorySourceTx(tx, transactionID, previousStatus, newStatus, previousPaidAt, newPaidAtVal, now, "manual")
}

func (h *TransactionHandler) insertTransactionStatusHistorySourceTx(tx *sql.Tx, transactionID, previousStatus, newStatus string, previousPaidAt sql.NullInt64, newPaidAtVal interface{}, now int64, source string) error {
	statusChanged := previousStatus != newStatus
	paidAtChanged := false
	var newPaidAtPtr *int64
	if newPaidAtVal != nil {
		v := newPaidAtVal.(int64)
		newPaidAtPtr = &v
	}
	if previousPaidAt.Valid != (newPaidAtPtr != nil) {
		paidAtChanged = true
	} else if previousPaidAt.Valid && newPaidAtPtr != nil && previousPaidAt.Int64 != *newPaidAtPtr {
		paidAtChanged = true
	}
	if !statusChanged && !paidAtChanged {
		return nil
	}

	var prevPaidAt, newPaidAt interface{}
	if previousPaidAt.Valid {
		prevPaidAt = previousPaidAt.Int64
	}
	if newPaidAtPtr != nil {
		newPaidAt = *newPaidAtPtr
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "manual"
	}

	_, err := tx.Exec(`
		INSERT INTO transaction_status_history (id, transaction_id, workspace_id, user_id, previous_status, new_status, previous_paid_at, new_paid_at, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, uuid.NewString(), transactionID, h.WorkspaceID, h.UserID, previousStatus, newStatus, prevPaidAt, newPaidAt, source, now)
	return err
}

func (h *TransactionHandler) loadTransactionStatusHistory(transactionID string) []TransactionStatusEvent {
	rows, err := h.DB.Query(`
		SELECT h.id, h.previous_status, h.new_status, h.previous_paid_at, h.new_paid_at, COALESCE(u.name, 'Usuario removido'), COALESCE(h.source, 'manual'), h.created_at
		FROM transaction_status_history h
		LEFT JOIN users u ON u.id = h.user_id
		WHERE h.transaction_id = ? AND h.workspace_id = ?
		ORDER BY h.created_at ASC
	`, transactionID, h.WorkspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []TransactionStatusEvent
	for rows.Next() {
		var e TransactionStatusEvent
		var prevPaidAt, newPaidAt sql.NullInt64
		var createdAt int64
		if err := rows.Scan(&e.ID, &e.PreviousStatus, &e.NewStatus, &prevPaidAt, &newPaidAt, &e.UserName, &e.Source, &createdAt); err != nil {
			continue
		}
		e.PreviousPaidAt = formatNullableDateDisplay(prevPaidAt)
		e.NewPaidAt = formatNullableDateDisplay(newPaidAt)
		e.PreviousStatusLabel = statusLabel(e.PreviousStatus)
		e.NewStatusLabel = statusLabel(e.NewStatus)
		e.SourceLabel = statusHistorySourceLabel(e.Source)
		e.CreatedAtLabel = formatDateTimeLabel(createdAt)
		events = append(events, e)
	}
	return events
}

func statusHistorySourceLabel(source string) string {
	switch strings.TrimSpace(source) {
	case "automatic":
		return "automático"
	default:
		return "manual"
	}
}

func formatNullableDateDisplay(v sql.NullInt64) string {
	if !v.Valid {
		return "-"
	}
	return formatAppDateLabelFromUnix(v.Int64)
}

func statusLabel(s string) string {
	switch s {
	case "paid":
		return "Pago"
	case "pending":
		return "Pendente"
	default:
		return s
	}
}
