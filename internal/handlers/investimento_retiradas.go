package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

type investmentWithdrawalOption struct {
	ID, Label, Received, Principal string
	Selected                       bool
}
type investmentWithdrawalForm struct {
	InvestmentID, CSRFToken, RequestID, Date, Mode, Received, Note, Error string
	Options                                                               []investmentWithdrawalOption
}
type investmentWithdrawalAllocationRow struct {
	Purchase, Received, Principal string
	Closed                        bool
}
type investmentWithdrawalRow struct {
	Date, DateISO, Mode, Received, Note string
	Allocations                         []investmentWithdrawalAllocationRow
}

func (h *InvestimentosHandler) HandleRetiradaInvestimento(w http.ResponseWriter, r *http.Request, id string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		mapInvestmentError(w, r, services.ErrInvestmentValidation)
		return
	}
	form := investmentWithdrawalForm{InvestmentID: id, CSRFToken: h.CSRFToken, RequestID: strings.TrimSpace(r.PostForm.Get("request_id")), Date: strings.TrimSpace(r.PostForm.Get("withdrawal_date")), Mode: r.PostForm.Get("mode"), Received: r.PostForm.Get("received_amount"), Note: r.PostForm.Get("note")}
	cents, err := parseInvestmentAmount(form.Received, "valor recebido", true)
	input := services.CreateInvestmentWithdrawalInput{WorkspaceID: h.WorkspaceID, InvestmentID: id, CreatedBy: h.UserID, RequestID: form.RequestID, WithdrawalDate: form.Date, Mode: form.Mode, ReceivedCents: cents, Note: form.Note}
	selected := r.PostForm["purchase_id"]
	for _, purchaseID := range selected {
		option := investmentWithdrawalOption{ID: purchaseID, Selected: true, Received: r.PostForm.Get("received_" + purchaseID), Principal: r.PostForm.Get("principal_" + purchaseID)}
		form.Options = append(form.Options, option)
		if err != nil {
			continue
		}
		received := cents
		if len(selected) > 1 || strings.TrimSpace(option.Received) != "" {
			received, err = parseInvestmentAmount(option.Received, "valor atribuído à compra", true)
			if err != nil {
				continue
			}
		}
		allocation := models.InvestmentWithdrawalAllocation{PurchaseID: purchaseID, ReceivedCents: received}
		if strings.TrimSpace(option.Principal) != "" {
			principal, parseErr := parseInvestmentAmount(option.Principal, "principal baixado", true)
			if parseErr != nil {
				err = parseErr
				continue
			}
			allocation.PrincipalCents = &principal
		}
		input.Allocations = append(input.Allocations, allocation)
	}
	if err == nil {
		_, err = services.CreateInvestmentWithdrawal(r.Context(), h.WriterDB, input)
	}
	if err != nil {
		var commitError *services.InvestmentCommitError
		if errors.As(err, &commitError) {
			mapInvestmentError(w, r, commitError)
			return
		}
		if r.Header.Get("HX-Request") == "true" || h.Templates == nil || (!errors.Is(err, services.ErrInvestmentValidation) && !errors.Is(err, services.ErrInvestmentConflict)) {
			respondInvestmentWriteError(w, r, err)
			return
		}
		form.Error = err.Error()
		status := http.StatusBadRequest
		if errors.Is(err, services.ErrInvestmentConflict) {
			status = http.StatusConflict
		}
		h.renderInvestmentDetail(w, r, id, nil, status, &form)
		return
	}
	location := "/investimentos/" + id + "?withdrawn=" + form.Mode
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
