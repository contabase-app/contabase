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

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// HTTP surface of the schema 81 record-only registration cancellation. The
// drawer family of the funded cancellation is reused with its own endpoints:
// preview and receipt are reads; only the explicit confirmation reaches
// WriterDB. The signed preview token travels in the POST body, never in a
// URL, and is never logged. No financial write exists in this flow.

const investmentRecordCancellationReasonLimit = 500

type investmentRecordCancellationPreviewView struct {
	Eligible                  bool
	Blockers                  []string
	InvestmentID              string
	InvestmentName            string
	Amount                    string
	EconomicDate              string
	InvestmentAccountName     string
	InvestmentBalance         string
	PreviewToken              string
	RequestID                 string
	Reason                    string
	CSRFToken                 string
	ExpectedInvestmentVersion int64
}

func (h *InvestimentosHandler) recordCancellationReceipt(ctx context.Context, investmentID string) (*models.InvestmentRecordCancellation, error) {
	receipt, err := services.GetInvestmentRecordCancellation(ctx, h.DB, h.WorkspaceID, investmentID)
	if errors.Is(err, services.ErrInvestmentNotFound) || services.IsMissingInvestmentRecordCancellationsTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

// recordCancellationReceiptView reuses the funded receipt view shape: for a
// record-only cancellation the origin and compensation fields stay empty and
// the template renders the no-movement copy instead.
func (h *InvestimentosHandler) recordCancellationReceiptView(ctx context.Context, receipt models.InvestmentRecordCancellation) (investmentCancellationReceiptView, error) {
	names, err := h.cancellationAccountNames(ctx, receipt.InvestmentAccountID)
	if err != nil {
		return investmentCancellationReceiptView{}, err
	}
	investmentName := names[receipt.InvestmentAccountID]
	if investmentName == "" {
		investmentName = receipt.InvestmentAccountID
	}
	confirmed := FormatAppTime(time.Unix(receipt.ConfirmedAt, 0))
	return investmentCancellationReceiptView{
		Amount:                formatCurrencyLabel(receipt.AmountCents),
		ConfirmedAt:           confirmed.DateTime + " · " + confirmed.Timezone,
		EconomicDate:          investmentDateLabel(receipt.EconomicDate),
		EconomicDateISO:       receipt.EconomicDate,
		Reason:                receipt.Reason,
		InvestmentAccountName: investmentName,
		InvestmentBefore:      formatCurrencyLabel(receipt.InvestmentBeforeCents),
		InvestmentAfter:       formatCurrencyLabel(receipt.InvestmentAfterCents),
	}, nil
}

// initialPurchaseFunded reports whether the lot carries the schema 77
// automatic funding transfer. It selects the cancellation family of the
// detail page: funded lots keep the schema 79 integral compensation flow;
// record-only lots use the schema 81 registration cancellation.
func (h *InvestimentosHandler) initialPurchaseFunded(ctx context.Context, investmentID string) (bool, error) {
	var funded int
	err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE workspace_id=? AND id=?`,
		h.WorkspaceID, services.InvestmentPurchaseTransferPrefix+"initial:"+investmentID).Scan(&funded)
	if err != nil {
		return false, err
	}
	return funded != 0, nil
}

// recordCancellationState runs the approved read-only preview once with a
// synthetic request identity and discards the token, mirroring the funded
// eligibility probe. The real preview rechecks every evidence.
func (h *InvestimentosHandler) recordCancellationState(ctx context.Context, investmentID string) (services.InvestmentRecordCancellationPreview, error) {
	preview, err := services.PreviewInvestmentRecordCancellation(ctx, h.DB, services.InvestmentRecordCancellationPreviewInput{
		WorkspaceID:  h.WorkspaceID,
		InvestmentID: investmentID,
		ActingUserID: h.UserID,
		RequestID:    uuid.NewString(),
		Reason:       investmentCancellationEligibilityReason,
		Location:     appTimeLocation(),
	})
	if errors.Is(err, services.ErrInvestmentNotFound) {
		return services.InvestmentRecordCancellationPreview{}, nil
	}
	return preview, err
}

// investmentRecordCancellationBlockerMessages translates the service's
// technical blockers into objective explanations. It never adds a new
// eligibility rule: the service remains the only authority.
func investmentRecordCancellationBlockerMessages(blockers []string, detail services.InvestmentDetail) []string {
	messages := make([]string, 0, len(blockers))
	seen := make(map[string]bool, len(blockers))
	add := func(message string) {
		if message == "" || seen[message] {
			return
		}
		seen[message] = true
		messages = append(messages, message)
	}
	for _, blocker := range blockers {
		switch blocker {
		case "aplicação não está ativa":
			add("Esta aplicação não está ativa.")
		case "aplicação arquivada":
			add("Esta aplicação está arquivada.")
		case "compra inicial divergente do registro da aplicação":
			add("A compra inicial não corresponde ao registro desta aplicação.")
		case "aplicação possui operações ou dependências posteriores":
			switch {
			case detail.PurchaseCount > 1:
				add("Esta aplicação possui compras adicionais e não pode ter o registro cancelado.")
			case len(detail.Valuations) > 0:
				add("Esta aplicação possui posições registradas que impedem o cancelamento do registro.")
			default:
				add("Esta aplicação possui operações ou dependências posteriores que impedem o cancelamento do registro.")
			}
		case "aplicação possui transferência automática vinculada":
			add("Esta aplicação possui transferência automática vinculada e usa o cancelamento com compensação.")
		case "aplicação possui cancelamento confirmado":
			add("Esta aplicação possui cancelamento confirmado.")
		case "compra já estornada":
			add("A compra desta aplicação já possui estorno registrado.")
		case "data do registro anterior à aplicação":
			add("A data do cancelamento não pode ser anterior à aplicação.")
		case "conta de investimento inválida ou arquivada":
			add("A conta de investimento está arquivada ou indisponível.")
		case "conciliação presente na conta de investimento":
			add("Há conciliação de saldo na conta de investimento.")
		case "conta de investimento possui declaração de adoção":
			add("A conta de investimento possui declaração de adoção registrada.")
		default:
			add(blocker)
		}
	}
	if len(messages) == 0 {
		messages = append(messages, "Esta aplicação não atende aos requisitos para o cancelamento do registro.")
	}
	return messages
}

func (h *InvestimentosHandler) recordCancellationPreviewView(preview services.InvestmentRecordCancellationPreview, investmentID, requestID, reason string) investmentRecordCancellationPreviewView {
	return investmentRecordCancellationPreviewView{
		Eligible:                  preview.Eligible,
		Blockers:                  preview.Blockers,
		InvestmentID:              investmentID,
		Amount:                    formatCurrencyLabel(preview.AmountCents),
		EconomicDate:              investmentDateLabel(preview.EconomicDate),
		InvestmentAccountName:     preview.InvestmentAccountName,
		InvestmentBalance:         formatCurrencyLabel(preview.InvestmentBalanceCents),
		PreviewToken:              preview.PreviewToken,
		RequestID:                 requestID,
		Reason:                    reason,
		CSRFToken:                 h.CSRFToken,
		ExpectedInvestmentVersion: preview.InvestmentBalanceVersion,
	}
}

func (h *InvestimentosHandler) renderRecordCancellationPreview(w http.ResponseWriter, r *http.Request, view investmentRecordCancellationPreviewView) {
	if h.Templates == nil {
		mapInvestmentError(w, r, errors.New("investment templates unavailable"))
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "investment-record-cancellation-preview", view); err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *InvestimentosHandler) HandlePreviewInvestmentRecordCancellation(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.DB == nil || h.Templates == nil {
		mapInvestmentError(w, r, errors.New("investment reader or templates unavailable"))
		return
	}
	requestID, reason, err := parseInvestmentCancellationRequest(r)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	receipt, err := h.recordCancellationReceipt(r.Context(), investmentID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	if receipt != nil {
		mapInvestmentError(w, r, fmt.Errorf("%w: registro de aplicação já cancelado; consulte o recibo", services.ErrInvestmentConflict))
		return
	}
	investment, err := services.GetInvestment(r.Context(), h.DB, h.WorkspaceID, investmentID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	preview, err := services.PreviewInvestmentRecordCancellation(r.Context(), h.DB, services.InvestmentRecordCancellationPreviewInput{
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
	view := h.recordCancellationPreviewView(preview, investmentID, requestID, reason)
	view.InvestmentName = investment.Name
	if r.Header.Get("HX-Request") != "true" {
		// Native POST fallback: re-render the existing detail page with the
		// preview inside its drawer instead of returning a bare fragment.
		h.recordCancellationPreview = &view
		h.renderInvestmentDetail(w, r, investmentID, nil, http.StatusOK)
		return
	}
	h.renderRecordCancellationPreview(w, r, view)
}

func (h *InvestimentosHandler) HandleConfirmInvestmentRecordCancellation(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	requestID, reason, err := parseInvestmentCancellationRequest(r)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	investmentVersion, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("expected_investment_version")), 10, 64)
	if err != nil || investmentVersion < 0 {
		mapInvestmentError(w, r, fmt.Errorf("%w: confirmação de cancelamento de registro incompleta", services.ErrInvestmentValidation))
		return
	}
	result, err := services.CancelInvestmentRecord(r.Context(), h.WriterDB, services.CancelInvestmentRecordInput{
		WorkspaceID:               h.WorkspaceID,
		InvestmentID:              investmentID,
		CreatedBy:                 h.UserID,
		RequestID:                 requestID,
		Reason:                    reason,
		PreviewToken:              strings.TrimSpace(r.PostForm.Get("preview_token")),
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
	if result.Outcome == services.InvestmentOperationReplayed {
		location = "/investimentos/" + investmentID
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
