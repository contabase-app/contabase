package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// HTTP surface of the schema 79 investment cancellation. Preview and receipt
// are reads; only the explicit confirmation reaches WriterDB. The signed
// preview token travels in the POST body, never in a URL, and is never logged.

const (
	investmentCancellationReasonLimit = 500
	// The detail page asks the approved preview service whether the lot can be
	// cancelled. The synthetic request identity never confirms anything: it is
	// discarded and the user's real preview revalidates with its own key.
	investmentCancellationEligibilityReason = "verificação de elegibilidade"
)

type investmentCancellationFormView struct {
	CSRFToken string
	RequestID string
}

type investmentCancellationReceiptView struct {
	Amount                    string
	ConfirmedAt               string
	EconomicDate              string
	EconomicDateISO           string
	Reason                    string
	InvestmentAccountName     string
	OriginAccountName         string
	InvestmentBefore          string
	InvestmentAfter           string
	OriginBefore              string
	OriginAfter               string
	CompensationTransactionID string
}

type investmentCancellationPreviewView struct {
	Eligible                  bool
	Blockers                  []string
	InvestmentID              string
	InvestmentName            string
	Amount                    string
	EconomicDate              string
	InvestmentAccountName     string
	OriginAccountName         string
	InvestmentBefore          string
	InvestmentAfter           string
	OriginBefore              string
	OriginAfter               string
	PreviewToken              string
	RequestID                 string
	Reason                    string
	CSRFToken                 string
	ExpectedSourceVersion     int64
	ExpectedInvestmentVersion int64
}

// parseInvestmentCancellationRequest reads only the request identity, the
// mandatory reason and the signed preview carrier. Financial values, accounts
// and dates are resolved by the service from the authenticated workspace.
func parseInvestmentCancellationRequest(r *http.Request) (string, string, error) {
	if err := r.ParseForm(); err != nil {
		return "", "", fmt.Errorf("%w: formulário inválido", services.ErrInvestmentValidation)
	}
	raw := strings.TrimSpace(r.PostForm.Get("request_id"))
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: formulário expirado ou inválido; reabra o cancelamento", services.ErrInvestmentValidation)
	}
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || utf8.RuneCountInString(reason) > investmentCancellationReasonLimit {
		return "", "", fmt.Errorf("%w: motivo deve ter de 1 a %d caracteres", services.ErrInvestmentValidation, investmentCancellationReasonLimit)
	}
	return parsed.String(), reason, nil
}

func (h *InvestimentosHandler) cancellationReceipt(ctx context.Context, investmentID string) (*models.InvestmentCancellation, error) {
	receipt, err := services.GetInvestmentCancellation(ctx, h.DB, h.WorkspaceID, investmentID)
	if errors.Is(err, services.ErrInvestmentNotFound) || services.IsMissingInvestmentCancellationsTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (h *InvestimentosHandler) cancellationAccountNames(ctx context.Context, ids ...string) (map[string]string, error) {
	names := make(map[string]string, len(ids))
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return names, nil
	}
	args := make([]interface{}, 0, 1+len(unique))
	args = append(args, h.WorkspaceID)
	args = appendStrings(args, unique)
	rows, err := h.DB.QueryContext(ctx, `SELECT id,name FROM accounts WHERE workspace_id=? AND id IN (`+sqlPlaceholders(len(unique))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func (h *InvestimentosHandler) cancellationReceiptView(ctx context.Context, receipt models.InvestmentCancellation) (investmentCancellationReceiptView, error) {
	names, err := h.cancellationAccountNames(ctx, receipt.InvestmentAccountID, receipt.OriginAccountID)
	if err != nil {
		return investmentCancellationReceiptView{}, err
	}
	investmentName := names[receipt.InvestmentAccountID]
	if investmentName == "" {
		investmentName = receipt.InvestmentAccountID
	}
	originName := names[receipt.OriginAccountID]
	if originName == "" {
		originName = receipt.OriginAccountID
	}
	confirmed := FormatAppTime(time.Unix(receipt.ConfirmedAt, 0))
	return investmentCancellationReceiptView{
		Amount:                    formatCurrencyLabel(receipt.AmountCents),
		ConfirmedAt:               confirmed.DateTime + " · " + confirmed.Timezone,
		EconomicDate:              investmentDateLabel(receipt.EconomicDate),
		EconomicDateISO:           receipt.EconomicDate,
		Reason:                    receipt.Reason,
		InvestmentAccountName:     investmentName,
		OriginAccountName:         originName,
		InvestmentBefore:          formatCurrencyLabel(receipt.InvestmentBeforeCents),
		InvestmentAfter:           formatCurrencyLabel(receipt.InvestmentAfterCents),
		OriginBefore:              formatCurrencyLabel(receipt.OriginBeforeCents),
		OriginAfter:               formatCurrencyLabel(receipt.OriginAfterCents),
		CompensationTransactionID: receipt.CompensationTransactionID,
	}, nil
}

// cancellationState runs the approved read-only preview once with a synthetic
// request identity and discards the token. The detail page reuses its
// eligibility and blockers; the real preview rechecks every evidence.
func (h *InvestimentosHandler) cancellationState(ctx context.Context, investmentID string) (services.InvestmentCancellationPreview, error) {
	preview, err := services.PreviewInvestmentCancellation(ctx, h.DB, services.InvestmentCancellationPreviewInput{
		WorkspaceID:  h.WorkspaceID,
		InvestmentID: investmentID,
		ActingUserID: h.UserID,
		RequestID:    uuid.NewString(),
		Reason:       investmentCancellationEligibilityReason,
		Location:     appTimeLocation(),
	})
	if errors.Is(err, services.ErrInvestmentNotFound) {
		return services.InvestmentCancellationPreview{}, nil
	}
	return preview, err
}

func (h *InvestimentosHandler) applyCancellationState(ctx context.Context, investmentID string, data *investimentosPageData) error {
	receipt, err := h.cancellationReceipt(ctx, investmentID)
	if err != nil {
		return err
	}
	if receipt != nil {
		data.Cancelled = true
		data.Cancellation, err = h.cancellationReceiptView(ctx, *receipt)
		return err
	}
	recordReceipt, err := h.recordCancellationReceipt(ctx, investmentID)
	if err != nil {
		return err
	}
	if recordReceipt != nil {
		data.Cancelled = true
		data.CancellationRecord = true
		data.Cancellation, err = h.recordCancellationReceiptView(ctx, *recordReceipt)
		return err
	}
	funded, err := h.initialPurchaseFunded(ctx, investmentID)
	if err != nil {
		return err
	}
	if funded {
		preview, err := h.cancellationState(ctx, investmentID)
		if err != nil {
			return err
		}
		data.CancellationEligible = preview.Eligible
		if preview.Eligible {
			data.CancellationForm = investmentCancellationFormView{CSRFToken: h.CSRFToken, RequestID: uuid.NewString()}
			return nil
		}
		data.CancellationBlockers, data.CancellationNote = investmentCancellationBlockerMessages(preview.Blockers, data.Detail)
		return nil
	}
	// Record-only lot: the schema 81 registration cancellation is the only
	// applicable family. The same drawer renders it with no-movement copy.
	preview, err := h.recordCancellationState(ctx, investmentID)
	if err != nil {
		return err
	}
	data.RecordCancellationEligible = preview.Eligible
	if preview.Eligible {
		data.RecordCancellationForm = investmentCancellationFormView{CSRFToken: h.CSRFToken, RequestID: uuid.NewString()}
		return nil
	}
	data.RecordCancellationBlockers = investmentRecordCancellationBlockerMessages(preview.Blockers, data.Detail)
	return nil
}

// investmentCancellationBlockerMessages translates the service's technical
// blockers into objective, non-financial explanations. It never adds a new
// eligibility rule: the service remains the only authority.
func investmentCancellationBlockerMessages(blockers []string, detail services.InvestmentDetail) ([]string, string) {
	messages := make([]string, 0, len(blockers))
	seen := make(map[string]bool, len(blockers))
	add := func(message string) {
		if message == "" || seen[message] {
			return
		}
		seen[message] = true
		messages = append(messages, message)
	}
	note := ""
	for _, blocker := range blockers {
		switch blocker {
		case "aplicação não está ativa":
			add("Esta aplicação não está ativa.")
		case "aplicação arquivada":
			add("Esta aplicação está arquivada.")
		case "compra inicial financiada ausente", "transferência original ausente":
			add("Esta aplicação não possui transferência inicial vinculada.")
		case "compra inicial divergente do registro da aplicação":
			add("A compra inicial não corresponde ao registro desta aplicação.")
		case "transferência original divergente":
			add("A transferência inicial não corresponde ao registro original.")
		case "aplicação possui operações ou dependências posteriores":
			switch {
			case detail.PurchaseCount > 1:
				add("Esta aplicação possui compras adicionais e não pode ser cancelada integralmente.")
			case len(detail.Valuations) > 0:
				add("Esta aplicação possui posições registradas que impedem o cancelamento.")
			default:
				add("Esta aplicação possui operações ou dependências posteriores que impedem o cancelamento.")
			}
		case "conta de investimento inválida ou arquivada":
			add("A conta de investimento está arquivada ou não pode receber a compensação.")
		case "conta de origem inválida ou arquivada":
			add("A conta de origem está arquivada ou não pode receber a compensação.")
		case "data econômica anterior à aplicação":
			add("A data do cancelamento não pode ser anterior à aplicação.")
		case "conciliação presente em uma das contas":
			add("Há conciliação de saldo em uma das contas envolvidas.")
		case "conta de investimento possui declaração de adoção":
			add("A conta de investimento possui declaração de adoção registrada.")
		case "transferência original já possui conciliação ou vínculo":
			add("A transferência inicial já possui conciliação ou vínculo.")
		case "compensação deixaria saldo fora do intervalo permitido":
			add("A compensação deixaria o saldo fora do intervalo permitido.")
		default:
			add(blocker)
		}
	}
	if detail.PurchaseCount > 1 {
		note = "O estorno de compra individual ainda não está disponível nesta versão."
	}
	if len(messages) == 0 {
		messages = append(messages, "Esta aplicação não atende aos requisitos para cancelamento.")
	}
	return messages, note
}

func (h *InvestimentosHandler) cancellationPreviewView(preview services.InvestmentCancellationPreview, investmentID, requestID, reason string) investmentCancellationPreviewView {
	return investmentCancellationPreviewView{
		Eligible:                  preview.Eligible,
		Blockers:                  preview.Blockers,
		InvestmentID:              investmentID,
		Amount:                    formatCurrencyLabel(preview.AmountCents),
		EconomicDate:              investmentDateLabel(preview.EconomicDate),
		InvestmentAccountName:     preview.InvestmentAccountName,
		OriginAccountName:         preview.OriginAccountName,
		InvestmentBefore:          formatCurrencyLabel(preview.InvestmentBalanceCents),
		InvestmentAfter:           formatCurrencyLabel(preview.InvestmentAfterCents),
		OriginBefore:              formatCurrencyLabel(preview.OriginBalanceCents),
		OriginAfter:               formatCurrencyLabel(preview.OriginAfterCents),
		PreviewToken:              preview.PreviewToken,
		RequestID:                 requestID,
		Reason:                    reason,
		CSRFToken:                 h.CSRFToken,
		ExpectedSourceVersion:     preview.OriginBalanceVersion,
		ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	}
}

func (h *InvestimentosHandler) renderCancellationPreview(w http.ResponseWriter, r *http.Request, view investmentCancellationPreviewView) {
	if h.Templates == nil {
		mapInvestmentError(w, r, errors.New("investment templates unavailable"))
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "investment-cancellation-preview", view); err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *InvestimentosHandler) HandlePreviewInvestmentCancellation(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.DB == nil || h.Templates == nil {
		mapInvestmentError(w, r, errors.New("investment reader or templates unavailable"))
		return
	}
	requestID, reason, err := parseInvestmentCancellationRequest(r)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	receipt, err := h.cancellationReceipt(r.Context(), investmentID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	if receipt != nil {
		mapInvestmentError(w, r, fmt.Errorf("%w: aplicação já cancelada; consulte o recibo", services.ErrInvestmentConflict))
		return
	}
	investment, err := services.GetInvestment(r.Context(), h.DB, h.WorkspaceID, investmentID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	preview, err := services.PreviewInvestmentCancellation(r.Context(), h.DB, services.InvestmentCancellationPreviewInput{
		WorkspaceID:  h.WorkspaceID,
		InvestmentID: investmentID,
		ActingUserID: h.UserID,
		RequestID:    requestID,
		Reason:       reason,
		Location:     appTimeLocation(),
	})
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	view := h.cancellationPreviewView(preview, investmentID, requestID, reason)
	view.InvestmentName = investment.Name
	if r.Header.Get("HX-Request") != "true" {
		// Native POST fallback: re-render the existing detail page with the
		// preview inside its drawer instead of returning a bare fragment.
		h.cancellationPreview = &view
		h.renderInvestmentDetail(w, r, investmentID, nil, http.StatusOK)
		return
	}
	h.renderCancellationPreview(w, r, view)
}

func parseInvestmentCancellationVersion(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: confirmação de cancelamento incompleta", services.ErrInvestmentValidation)
	}
	return value, nil
}

func respondInvestmentCancellationUncertain(w http.ResponseWriter) {
	const message = "Resultado do cancelamento incerto. Consulte o recibo antes de tentar novamente."
	w.Header().Set("HX-Reswap", "none")
	http.Error(w, message, http.StatusConflict)
}

func (h *InvestimentosHandler) HandleConfirmInvestmentCancellation(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	requestID, reason, err := parseInvestmentCancellationRequest(r)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	sourceVersion, err := parseInvestmentCancellationVersion(r.PostForm.Get("expected_source_version"))
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	investmentVersion, err := parseInvestmentCancellationVersion(r.PostForm.Get("expected_investment_version"))
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	_, err = services.CancelInvestment(r.Context(), h.WriterDB, services.CancelInvestmentInput{
		WorkspaceID:               h.WorkspaceID,
		InvestmentID:              investmentID,
		CreatedBy:                 h.UserID,
		RequestID:                 requestID,
		Reason:                    reason,
		PreviewToken:              strings.TrimSpace(r.PostForm.Get("preview_token")),
		ExpectedSourceVersion:     sourceVersion,
		ExpectedInvestmentVersion: investmentVersion,
	})
	if err != nil {
		var commitError *services.InvestmentCommitError
		if errors.As(err, &commitError) {
			respondInvestmentCancellationUncertain(w)
			return
		}
		respondInvestmentWriteError(w, r, err)
		return
	}
	location := "/investimentos/" + investmentID + "?cancelada=1"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

// HandleInvestmentCancellationReceipt reuses the detail page as the durable
// receipt view of either cancellation family (funded 79 and record-only 81).
// No preview token is needed: authorization and workspace scope are the only
// inputs.
func (h *InvestimentosHandler) HandleInvestmentCancellationReceipt(w http.ResponseWriter, r *http.Request, investmentID string) {
	receipt, err := h.cancellationReceipt(r.Context(), investmentID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	if receipt == nil {
		recordReceipt, err := h.recordCancellationReceipt(r.Context(), investmentID)
		if err != nil {
			mapInvestmentError(w, r, err)
			return
		}
		if recordReceipt == nil {
			http.NotFound(w, r)
			return
		}
	}
	h.renderInvestmentDetail(w, r, investmentID, nil, http.StatusOK)
}
