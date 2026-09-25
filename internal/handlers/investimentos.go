package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

// InvestimentosHandler carries the per-request context of the investments
// surface. Reads use DB; mutations require the runtime's Immediate pool.
type InvestimentosHandler struct {
	DB          *sql.DB
	WriterDB    *sql.DB
	Templates   TemplateEngine
	CSRFToken   string
	WorkspaceID string
	UserID      string
}

// mapInvestmentError converts domain errors into HTTP semantics. Validation
// and invalid-account errors carry constructed, user-actionable messages;
// every unmapped error (including a trigger RAISE escaping a validation
// race) fails closed as a generic internal error, never raw SQL.
func mapInvestmentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrInvestmentNotFound):
		http.NotFound(w, r)
	case errors.Is(err, services.ErrInvestmentInvalidStatus), errors.Is(err, services.ErrInvestmentConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, services.ErrInvestmentValidation), errors.Is(err, services.ErrInvestmentInvalidAccount):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, "erro interno ao processar investimento", http.StatusInternalServerError)
	}
}

// parseInvestmentCreateForm builds the service input from the form. The
// workspace and the author come exclusively from the internal handler
// context: any workspace_id in the payload is ignored by design. Money
// arrives as integer cents — no currency parsing lives here.
func (h *InvestimentosHandler) parseInvestmentCreateForm(r *http.Request) (services.CreateInvestmentInput, error) {
	if err := r.ParseForm(); err != nil {
		return services.CreateInvestmentInput{}, fmt.Errorf("%w: formulário inválido", services.ErrInvestmentValidation)
	}
	principal, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("principal_cents")), 10, 64)
	if err != nil {
		return services.CreateInvestmentInput{}, fmt.Errorf("%w: principal deve ser um inteiro de centavos", services.ErrInvestmentValidation)
	}
	input := services.CreateInvestmentInput{
		WorkspaceID:    h.WorkspaceID,
		AccountID:      strings.TrimSpace(r.FormValue("account_id")),
		CreatedBy:      h.UserID,
		Name:           r.FormValue("name"),
		Institution:    r.FormValue("institution"),
		Kind:           r.FormValue("kind"),
		PrincipalCents: principal,
		TaxRegime:      strings.TrimSpace(r.FormValue("tax_regime")),
		AppliedDate:    strings.TrimSpace(r.FormValue("applied_date")),
		YieldNote:      r.FormValue("yield_note"),
	}
	if raw := strings.TrimSpace(r.FormValue("maturity_date")); raw != "" {
		input.MaturityDate = &raw
	}
	return input, nil
}

// parseInvestmentUpdateForm builds the update input from the form. Only the
// editable set is read; identity, account, principal, applied date, status
// and authorship cannot be expressed by the caller.
func (h *InvestimentosHandler) parseInvestmentUpdateForm(r *http.Request, investmentID string) (services.UpdateInvestmentInput, error) {
	if err := r.ParseForm(); err != nil {
		return services.UpdateInvestmentInput{}, fmt.Errorf("%w: formulário inválido", services.ErrInvestmentValidation)
	}
	input := services.UpdateInvestmentInput{
		WorkspaceID:  h.WorkspaceID,
		InvestmentID: investmentID,
		Name:         r.FormValue("name"),
		Institution:  r.FormValue("institution"),
		Kind:         r.FormValue("kind"),
		TaxRegime:    strings.TrimSpace(r.FormValue("tax_regime")),
		YieldNote:    r.FormValue("yield_note"),
	}
	if raw := strings.TrimSpace(r.FormValue("maturity_date")); raw != "" {
		input.MaturityDate = &raw
	}
	return input, nil
}

// createInvestment parses the creation form and persists the lot.
func (h *InvestimentosHandler) createInvestment(r *http.Request) (models.Investment, error) {
	input, err := h.parseInvestmentCreateForm(r)
	if err != nil {
		return models.Investment{}, err
	}
	return services.CreateInvestment(r.Context(), h.WriterDB, input)
}

// updateInvestment parses the edit form and applies it to an ATIVA lot.
func (h *InvestimentosHandler) updateInvestment(r *http.Request, investmentID string) error {
	input, err := h.parseInvestmentUpdateForm(r, investmentID)
	if err != nil {
		return err
	}
	input.UpdatedAt = time.Now().Unix()
	return services.UpdateInvestment(r.Context(), h.WriterDB, input)
}

// investmentDetail resolves the lot, its derived current value and its full
// valuation history for the read-only detail view.
func (h *InvestimentosHandler) investmentDetail(ctx context.Context, investmentID string) (services.InvestmentDetail, []models.InvestmentValuation, error) {
	inv, err := services.GetInvestment(ctx, h.DB, h.WorkspaceID, investmentID)
	if err != nil {
		return services.InvestmentDetail{}, nil, err
	}
	valuations, err := services.GetInvestmentValuations(ctx, h.DB, h.WorkspaceID, investmentID)
	if err != nil {
		return services.InvestmentDetail{}, nil, err
	}
	current, fromPrincipal, err := services.GetCurrentInvestmentValue(ctx, h.DB, h.WorkspaceID, investmentID)
	if err != nil {
		return services.InvestmentDetail{}, nil, err
	}
	detail := services.InvestmentDetail{
		Investment:         inv,
		CurrentCents:       current,
		CurrentIsPrincipal: fromPrincipal,
	}
	return detail, valuations, nil
}

// listInvestments resolves a frozen listing view for the handler workspace.
func (h *InvestimentosHandler) listInvestments(ctx context.Context, filter services.InvestmentListFilter) ([]models.Investment, error) {
	return services.ListInvestments(ctx, h.DB, h.WorkspaceID, filter)
}

// Mutation errors are classified only on failure; a successful commit is never
// turned into a timeout merely because the request was cancelled afterwards.
func respondInvestmentWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case requestContextError(r.Context().Err()) || requestContextError(err):
		http.Error(w, "request timeout", http.StatusRequestTimeout)
	case sqliteContention(err):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "serviço temporariamente indisponível", http.StatusServiceUnavailable)
	default:
		mapInvestmentError(w, r, err)
	}
}

func (h *InvestimentosHandler) HandleCriarInvestimento(w http.ResponseWriter, r *http.Request) {
	if h.WriterDB == nil {
		http.Error(w, "erro interno ao processar investimento", http.StatusInternalServerError)
		return
	}
	if _, err := h.createInvestment(r); err != nil {
		respondInvestmentWriteError(w, r, err)
		return
	}
	respondInvestmentSaved(w, r)
}

func (h *InvestimentosHandler) HandleEditarInvestimento(w http.ResponseWriter, r *http.Request, id string) {
	if h.WriterDB == nil {
		http.Error(w, "erro interno ao processar investimento", http.StatusInternalServerError)
		return
	}
	if err := h.updateInvestment(r, id); err != nil {
		respondInvestmentWriteError(w, r, err)
		return
	}
	respondInvestmentSaved(w, r)
}

func (h *InvestimentosHandler) HandleValuation(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.WriterDB == nil {
		http.Error(w, "erro interno ao processar investimento", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		mapInvestmentError(w, r, services.ErrInvestmentValidation)
		return
	}
	amount, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("amount_cents")), 10, 64)
	if err != nil {
		mapInvestmentError(w, r, fmt.Errorf("%w: valor deve ser um inteiro de centavos", services.ErrInvestmentValidation))
		return
	}
	_, err = services.UpsertInvestmentValuation(r.Context(), h.WriterDB, services.UpsertInvestmentValuationInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: investmentID, ActingUserID: h.UserID,
		ValuationDate: r.PostForm.Get("valuation_date"), AmountCents: amount,
	})
	if err != nil {
		var commitError *services.InvestmentValuationCommitError
		if errors.As(err, &commitError) {
			mapInvestmentError(w, r, commitError)
		} else {
			respondInvestmentWriteError(w, r, err)
		}
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/investimentos/"+investmentID, http.StatusSeeOther)
}

func (h *InvestimentosHandler) HandleRedeemInvestment(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	if err := r.ParseForm(); err != nil {
		mapInvestmentError(w, r, services.ErrInvestmentValidation)
		return
	}
	amount, err := strconv.ParseInt(strings.TrimSpace(r.PostForm.Get("final_amount_cents")), 10, 64)
	if err != nil {
		mapInvestmentError(w, r, fmt.Errorf("%w: valor final deve ser um inteiro de centavos", services.ErrInvestmentValidation))
		return
	}
	err = services.RedeemInvestment(r.Context(), h.WriterDB, services.RedeemInvestmentInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: investmentID, ActingUserID: h.UserID,
		RedeemedDate: r.PostForm.Get("redeemed_date"), FinalAmountCents: amount,
	})
	if err != nil {
		respondInvestmentLifecycleError(w, r, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/investimentos/"+investmentID, http.StatusSeeOther)
}

func (h *InvestimentosHandler) HandleArchiveInvestment(w http.ResponseWriter, r *http.Request, investmentID string) {
	if h.WriterDB == nil {
		mapInvestmentError(w, r, errors.New("investment writer unavailable"))
		return
	}
	err := services.ArchiveInvestment(r.Context(), h.WriterDB, services.ArchiveInvestmentInput{
		WorkspaceID: h.WorkspaceID, InvestmentID: investmentID, ActingUserID: h.UserID,
	})
	if err != nil {
		respondInvestmentLifecycleError(w, r, err)
		return
	}
	respondInvestmentSaved(w, r)
}

func respondInvestmentLifecycleError(w http.ResponseWriter, r *http.Request, err error) {
	var commitError *services.InvestmentCommitError
	if errors.As(err, &commitError) {
		// Share the existing indeterminate-outcome protocol, never the
		// pre-commit retry response. I3b keeps its separate generic 500.
		respondTogglePagamentoIndeterminateOutcome(w, r)
		return
	}
	respondInvestmentWriteError(w, r, err)
}

func respondInvestmentSaved(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, "/investimentos", http.StatusSeeOther)
}

type investmentAccountOption struct{ ID, Name string }

type investmentRow struct {
	models.Investment
	CurrentValue string
}

type investmentValuationRow struct {
	Date, Value string
}

type investmentFormData struct {
	Investment models.Investment
	Accounts   []investmentAccountOption
	IsEdit     bool
	Action     string
	CSRFToken  string
}

type investimentosPageData struct {
	AppShellAccess
	Title, UserName, UserInitials, ProfilePhotoURL, ActiveWorkspaceName string
	NotificationCount                                                   int
	IsBusiness                                                          bool
	Filter                                                              string
	Investments                                                         []investmentRow
	Detail                                                              services.InvestmentDetail
	Principal, CurrentValue                                             string
	Valuations                                                          []investmentValuationRow
	Form                                                                investmentFormData
}

func (h *InvestimentosHandler) investmentPageData() investimentosPageData {
	data := investimentosPageData{Title: "Investimentos"}
	data.UserName, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.NotificationCount = queryDashboardNotificationCount(h.DB, h.UserID, h.WorkspaceID)
	data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == "business"
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, data.IsBusiness)
	data.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
	return data
}

func (h *InvestimentosHandler) renderInvestmentPage(w http.ResponseWriter, r *http.Request, name string, data investimentosPageData) {
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, name, data); err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *InvestimentosHandler) HandleInvestimentos(w http.ResponseWriter, r *http.Request) {
	filter, label := services.InvestmentListActive, "ATIVAS"
	switch r.URL.Query().Get("status") {
	case "RESGATADAS":
		filter, label = services.InvestmentListRedeemed, "RESGATADAS"
	case "ARQUIVADAS":
		filter, label = services.InvestmentListArchived, "ARQUIVADAS"
	case "TODAS":
		filter, label = services.InvestmentListAll, "TODAS"
	}
	investments, err := h.listInvestments(r.Context(), filter)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	data := h.investmentPageData()
	data.Filter = label
	for _, inv := range investments {
		current, _, err := services.GetCurrentInvestmentValue(r.Context(), h.DB, h.WorkspaceID, inv.ID)
		if err != nil {
			mapInvestmentError(w, r, err)
			return
		}
		data.Investments = append(data.Investments, investmentRow{Investment: inv, CurrentValue: formatCurrencyLabel(current)})
	}
	accounts, err := h.DB.QueryContext(r.Context(), `SELECT id, name FROM accounts WHERE workspace_id = ? AND type = 'INVESTMENT' AND archived_at IS NULL ORDER BY name, id`, h.WorkspaceID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	defer accounts.Close()
	data.Form = investmentFormData{Action: "/investimentos", CSRFToken: h.CSRFToken}
	for accounts.Next() {
		var account investmentAccountOption
		if err := accounts.Scan(&account.ID, &account.Name); err != nil {
			mapInvestmentError(w, r, err)
			return
		}
		data.Form.Accounts = append(data.Form.Accounts, account)
	}
	if err := accounts.Err(); err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	h.renderInvestmentPage(w, r, "investimentos-page", data)
}

func (h *InvestimentosHandler) HandleDetalheInvestimento(w http.ResponseWriter, r *http.Request, id string) {
	detail, valuations, err := h.investmentDetail(r.Context(), id)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	data := h.investmentPageData()
	data.Detail = detail
	data.Principal = formatCurrencyLabel(detail.Investment.PrincipalCents)
	data.CurrentValue = formatCurrencyLabel(detail.CurrentCents)
	for _, v := range valuations {
		data.Valuations = append(data.Valuations, investmentValuationRow{Date: v.ValuationDate, Value: formatCurrencyLabel(v.AmountCents)})
	}
	data.Form = investmentFormData{Investment: detail.Investment, IsEdit: true, Action: "/investimentos/" + id + "/editar", CSRFToken: h.CSRFToken}
	h.renderInvestmentPage(w, r, "investimento-detalhe-page", data)
}
