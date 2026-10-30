package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

func (h *InvestimentosHandler) HandleCompraInvestimento(w http.ResponseWriter, r *http.Request, id string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		mapInvestmentError(w, r, services.ErrInvestmentValidation)
		return
	}
	cents, err := parseInvestmentAmount(r.PostForm.Get("principal_amount"), "valor aplicado", false)
	input := services.CreateInvestmentPurchaseInput{WorkspaceID: h.WorkspaceID, InvestmentID: id, CreatedBy: h.UserID,
		SourceAccountID: strings.TrimSpace(r.PostForm.Get("source_account_id")), RequestID: strings.TrimSpace(r.PostForm.Get("request_id")), PurchaseDate: strings.TrimSpace(r.PostForm.Get("purchase_date")), PrincipalCents: cents,
		NoteReference: r.PostForm.Get("note_reference"), Kind: r.PostForm.Get("kind"), Institution: r.PostForm.Get("institution"),
		TaxRegime: strings.TrimSpace(r.PostForm.Get("tax_regime")), ConditionsNote: r.PostForm.Get("conditions_note")}
	if maturity := strings.TrimSpace(r.PostForm.Get("maturity_date")); maturity != "" {
		input.MaturityDate = &maturity
	}
	if err == nil && r.PostForm.Get("funding_mode") == "transfer" && input.SourceAccountID == "" {
		err = services.ErrInvestmentValidation
	}
	if err == nil {
		_, err = services.CreateInvestmentPurchase(r.Context(), h.WriterDB, input)
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
		form := investmentFormData{Action: "/investimentos/" + id + "/compras", CSRFToken: h.CSRFToken, RequestID: input.RequestID, Error: err.Error(),
			SourceAccountID: input.SourceAccountID, PrincipalInput: r.PostForm.Get("principal_amount"), NoteReference: input.NoteReference,
			Investment: models.Investment{ID: id, AppliedDate: input.PurchaseDate, Kind: input.Kind, Institution: input.Institution, TaxRegime: input.TaxRegime, MaturityDate: input.MaturityDate, YieldNote: input.ConditionsNote}}
		status := http.StatusBadRequest
		if errors.Is(err, services.ErrInvestmentConflict) {
			status = http.StatusConflict
		}
		h.renderInvestmentDetail(w, r, id, &form, status)
		return
	}
	location := "/investimentos/" + id + "?purchased=1"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
