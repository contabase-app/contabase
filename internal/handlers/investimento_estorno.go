package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

// HTTP surface of the schema 80 individual purchase reversal. Preview is a
// read; only the explicit confirmation reaches WriterDB. The signed preview
// token travels in the POST body, never in a URL, and is never logged.

const investmentReversalReasonLimit = 500

type investmentReversalPreviewView struct {
	Eligible                  bool
	Blockers                  []string
	InvestmentID              string
	PurchaseID                string
	PurchaseDate              string
	PurchaseValue             string
	Movement                  string
	MovementLabel             string
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

// parseInvestmentReversalRequest reads only the target purchase and the
// mandatory reason. The request identity is issued by the preview itself and
// echoed back by the confirm form; financial values, accounts and dates are
// resolved by the service from the authenticated workspace.
func parseInvestmentReversalRequest(r *http.Request) (reason, purchaseID string, err error) {
	if err := r.ParseForm(); err != nil {
		return "", "", fmt.Errorf("%w: formulário inválido", services.ErrInvestmentValidation)
	}
	reason = strings.TrimSpace(r.PostForm.Get("reason"))
	if reason == "" || utf8.RuneCountInString(reason) > investmentReversalReasonLimit {
		return "", "", fmt.Errorf("%w: motivo deve ter de 1 a %d caracteres", services.ErrInvestmentValidation, investmentReversalReasonLimit)
	}
	purchaseID = strings.TrimSpace(r.PostForm.Get("purchase_id"))
	if purchaseID == "" {
		return "", "", fmt.Errorf("%w: selecione a compra a estornar", services.ErrInvestmentValidation)
	}
	return reason, purchaseID, nil
}

func parseInvestmentReversalVersion(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: confirmação de estorno incompleta", services.ErrInvestmentValidation)
	}
	return value, nil
}

func (h *InvestimentosHandler) reversalPreviewView(preview services.InvestmentReversalPreview, investmentID, purchaseID, requestID, reason string) investmentReversalPreviewView {
	movement := "Sem movimentação automática"
	if preview.Movement == "TRANSFER" {
		movement = "Transferência automática"
	}
	return investmentReversalPreviewView{
		Eligible:                  preview.Eligible,
		Blockers:                  preview.Blockers,
		InvestmentID:              investmentID,
		PurchaseID:                purchaseID,
		PurchaseDate:              investmentDateLabel(preview.PurchaseDate),
		PurchaseValue:             formatCurrencyLabel(preview.AmountCents),
		Movement:                  preview.Movement,
		MovementLabel:             movement,
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

func (h *InvestimentosHandler) renderReversalPreview(w http.ResponseWriter, r *http.Request, view investmentReversalPreviewView) {
	if h.Templates == nil {
		mapInvestmentError(w, r, errors.New("investment templates unavailable"))
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "investment-reversal-preview", view); err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *InvestimentosHandler) HandlePreviewInvestmentPurchaseReversal(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.DB == nil || h.Templates == nil {
		mapInvestmentError(w, r, errors.New("investment reader or templates unavailable"))
		return
	}
	reason, purchaseID, err := parseInvestmentReversalRequest(r)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	// The preview issues the request identity: the confirm form echoes it back
	// together with the signed token, so one rendered page can target any of
	// its purchases without sharing a replay key between them.
	requestID := uuid.NewString()
	preview, err := services.PreviewInvestmentPurchaseReversal(r.Context(), h.DB, services.InvestmentReversalPreviewInput{
		WorkspaceID:  h.WorkspaceID,
		InvestmentID: investmentID,
		PurchaseID:   purchaseID,
		ActingUserID: h.UserID,
		RequestID:    requestID,
		Reason:       reason,
		Location:     appTimeLocation(),
	})
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	view := h.reversalPreviewView(preview, investmentID, purchaseID, requestID, reason)
	if r.Header.Get("HX-Request") != "true" {
		// Native POST fallback: re-render the existing detail page with the
		// preview inside its drawer instead of returning a bare fragment.
		h.reversalPreview = &view
		h.reversalPurchaseID = purchaseID
		h.renderInvestmentDetail(w, r, investmentID, nil, http.StatusOK)
		return
	}
	h.renderReversalPreview(w, r, view)
}

func (h *InvestimentosHandler) HandleConfirmInvestmentPurchaseReversal(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		mapInvestmentError(w, r, services.ErrInvestmentValidation)
		return
	}
	purchaseID := strings.TrimSpace(r.PostForm.Get("purchase_id"))
	reason := strings.TrimSpace(r.PostForm.Get("reason"))
	requestID, err := uuid.Parse(strings.TrimSpace(r.PostForm.Get("request_id")))
	if err != nil || reason == "" || purchaseID == "" {
		mapInvestmentError(w, r, fmt.Errorf("%w: formulário expirado ou inválido; reabra o estorno", services.ErrInvestmentValidation))
		return
	}
	sourceVersion, err := parseInvestmentReversalVersion(r.PostForm.Get("expected_source_version"))
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	investmentVersion, err := parseInvestmentReversalVersion(r.PostForm.Get("expected_investment_version"))
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	result, err := services.ReverseInvestmentPurchase(r.Context(), h.WriterDB, services.ReverseInvestmentPurchaseInput{
		WorkspaceID:               h.WorkspaceID,
		InvestmentID:              investmentID,
		PurchaseID:                purchaseID,
		CreatedBy:                 h.UserID,
		RequestID:                 requestID.String(),
		Reason:                    reason,
		PreviewToken:              strings.TrimSpace(r.PostForm.Get("preview_token")),
		ExpectedSourceVersion:     sourceVersion,
		ExpectedInvestmentVersion: investmentVersion,
	})
	if err != nil {
		var commitError *services.InvestmentCommitError
		if errors.As(err, &commitError) {
			w.Header().Set("HX-Reswap", "none")
			http.Error(w, "Resultado do estorno incerto. Consulte o histórico antes de tentar novamente.", http.StatusConflict)
			return
		}
		respondInvestmentWriteError(w, r, err)
		return
	}
	location := "/investimentos/" + investmentID + "?estornada=1"
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
