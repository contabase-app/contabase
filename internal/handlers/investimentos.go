package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
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

var investmentPrincipalPattern = regexp.MustCompile(`^(?:[0-9]+|[1-9][0-9]{0,2}(?:\.[0-9]{3})+)(?:,[0-9]{1,2})?$`)

// parseInvestmentAmount restricts the exact shared parser to the investment
// UI's Brazilian format. The optional currency prefix is visual only.
func parseInvestmentAmount(raw, label string, allowZero bool) (int64, error) {
	amount := strings.TrimSpace(raw)
	amount = strings.TrimSpace(strings.TrimPrefix(amount, "R$ "))
	if !investmentPrincipalPattern.MatchString(amount) {
		return 0, fmt.Errorf("%w: %s deve usar formato brasileiro, com até duas casas decimais (ex.: 300,00)", services.ErrInvestmentValidation, label)
	}
	cents, err := parseAccountBalance(amount)
	if err != nil || cents < 0 || (!allowZero && cents == 0) {
		return 0, fmt.Errorf("%w: %s fora do intervalo permitido", services.ErrInvestmentValidation, label)
	}
	return cents, nil
}

// Keep the established integer-cent HTTP contract for older callers. A visible
// reais field, even invalid or empty, always takes precedence over legacy cents.
func investmentOperationAmount(r *http.Request, reaisField, centsField, label string) (int64, error) {
	if _, present := r.PostForm[reaisField]; present {
		return parseInvestmentAmount(r.PostForm.Get(reaisField), label, true)
	}
	return strconv.ParseInt(strings.TrimSpace(r.PostForm.Get(centsField)), 10, 64)
}

// parseInvestmentCreateForm builds the service input from the form. The
// workspace and the author come exclusively from the internal handler
// context: any workspace_id in the payload is ignored by design. The visible
// amount is in reais; parsing uses exact integers, without rounding.
func (h *InvestimentosHandler) parseInvestmentCreateForm(r *http.Request) (services.CreateInvestmentInput, error) {
	if err := r.ParseForm(); err != nil {
		return services.CreateInvestmentInput{}, fmt.Errorf("%w: formulário inválido", services.ErrInvestmentValidation)
	}
	principal, err := parseInvestmentAmount(r.PostForm.Get("principal_amount"), "valor aplicado", false)
	if err != nil {
		return services.CreateInvestmentInput{}, err
	}
	requestID := strings.TrimSpace(r.PostForm.Get("request_id"))
	if _, err := uuid.Parse(requestID); err != nil {
		return services.CreateInvestmentInput{}, fmt.Errorf("%w: formulário expirado ou inválido; reabra o cadastro", services.ErrInvestmentValidation)
	}
	input := services.CreateInvestmentInput{
		WorkspaceID:     h.WorkspaceID,
		AccountID:       strings.TrimSpace(r.FormValue("account_id")),
		SourceAccountID: strings.TrimSpace(r.PostForm.Get("source_account_id")),
		CreatedBy:       h.UserID,
		RequestID:       requestID,
		Name:            r.FormValue("name"),
		Institution:     r.FormValue("institution"),
		Kind:            r.FormValue("kind"),
		PrincipalCents:  principal,
		TaxRegime:       strings.TrimSpace(r.FormValue("tax_regime")),
		AppliedDate:     strings.TrimSpace(r.FormValue("applied_date")),
		YieldNote:       r.FormValue("yield_note"),
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
	if r.PostForm.Get("funding_mode") == "transfer" && input.SourceAccountID == "" {
		return models.Investment{}, fmt.Errorf("%w: selecione a conta de origem", services.ErrInvestmentValidation)
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
	detail, err := services.GetInvestmentPurchaseDetail(ctx, h.DB, h.WorkspaceID, investmentID)
	return detail, detail.Valuations, err
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
	inv, err := h.createInvestment(r)
	if err != nil {
		if r.Header.Get("HX-Request") == "true" || h.Templates == nil ||
			(!errors.Is(err, services.ErrInvestmentValidation) && !errors.Is(err, services.ErrInvestmentInvalidAccount) && !errors.Is(err, services.ErrInvestmentConflict)) {
			respondInvestmentWriteError(w, r, err)
			return
		}
		form := investmentFormData{Action: "/investimentos", CSRFToken: h.CSRFToken,
			SourceAccountID: r.PostForm.Get("source_account_id"), PrincipalInput: r.PostForm.Get("principal_amount"), RequestID: r.PostForm.Get("request_id"), Error: err.Error(),
			Investment: models.Investment{AccountID: r.PostForm.Get("account_id"), Name: r.PostForm.Get("name"),
				Institution: r.PostForm.Get("institution"), Kind: r.PostForm.Get("kind"), TaxRegime: r.PostForm.Get("tax_regime"),
				AppliedDate: r.PostForm.Get("applied_date"), YieldNote: r.PostForm.Get("yield_note")}}
		if maturity := r.PostForm.Get("maturity_date"); maturity != "" {
			form.Investment.MaturityDate = &maturity
		}
		status := http.StatusBadRequest
		if errors.Is(err, services.ErrInvestmentConflict) {
			status = http.StatusConflict
		}
		h.renderInvestimentos(w, r, &form, status)
		return
	}
	location := "/investimentos/" + inv.ID + "?created=1"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
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
	amount, err := investmentOperationAmount(r, "amount", "amount_cents", "valor da posição")
	if err != nil {
		mapInvestmentError(w, r, fmt.Errorf("%w: valor da posição deve usar formato brasileiro ou um inteiro de centavos", services.ErrInvestmentValidation))
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
	amount, err := investmentOperationAmount(r, "final_amount", "final_amount_cents", "valor final")
	if err != nil {
		mapInvestmentError(w, r, fmt.Errorf("%w: valor final deve usar formato brasileiro ou um inteiro de centavos", services.ErrInvestmentValidation))
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
	Principal, CurrentValue, PositionDate, PositionDateISO, MaturityLabel, MaturityDateLabel, MaturityDateISO string
	PurchaseCount                                                                                             int
	PositionStale                                                                                             bool
}

type investmentPurchaseRow struct {
	models.InvestmentPurchase
	Date, Value, Maturity, TaxLabel string
	Closed                          bool
	RemainingPrincipal              string
	TransferID, SourceAccountName   string
}

type investmentValuationRow struct {
	Date, DateISO, Value string
}

type investmentSummary struct {
	ActiveCount, WithoutPosition                    int
	StalePositions                                  int
	KnownTotal, LatestDate, LatestDateISO           string
	NextMaturity, NextMaturityISO, NextMaturityName string
}

type investmentFormData struct {
	Investment                       models.Investment
	Accounts                         []investmentAccountOption
	SourceAccounts                   []investmentAccountOption
	SourceAccountID                  string
	IsEdit                           bool
	Action                           string
	CSRFToken                        string
	PrincipalInput, RequestID, Error string
	NoteReference, MinimumDate       string
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
	PositionReference, PositionDateLabel, PositionDateISO               string
	AppliedDateLabel, MaturityDateISO                                   string
	GrossYield, AccumulatedReturn, ReturnTone                           string
	ReturnAvailable                                                     bool
	Valuations                                                          []investmentValuationRow
	Purchases                                                           []investmentPurchaseRow
	Summary                                                             investmentSummary
	MaturityLabel, MaturityDateLabel                                    string
	Form                                                                investmentFormData
	PurchaseForm                                                        investmentFormData
	WithdrawalForm                                                      investmentWithdrawalForm
	Withdrawals                                                         []investmentWithdrawalRow
	TotalWithdrawn                                                      string
	AdjustedVariationValue                                              string
	AdjustedVariationStart                                              string
	AdjustedVariationEnd                                                string
	Success                                                             string
	StatusCode                                                          int
	Funded                                                              bool
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
	if data.StatusCode != 0 {
		w.WriteHeader(data.StatusCode)
	}
	_, _ = buf.WriteTo(w)
}

func (h *InvestimentosHandler) HandleInvestimentos(w http.ResponseWriter, r *http.Request) {
	h.renderInvestimentos(w, r, nil, http.StatusOK)
}

func (h *InvestimentosHandler) renderInvestimentos(w http.ResponseWriter, r *http.Request, submitted *investmentFormData, status int) {
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
	// Read only the latest recorded positions. Principal is not a known
	// position, and neither accounts nor ledger amounts enter this total.
	positions, err := h.latestInvestmentPositions(r.Context())
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	purchases, err := services.GetWorkspaceInvestmentPurchaseTotals(r.Context(), h.DB, h.WorkspaceID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	active := investments
	if filter != services.InvestmentListActive {
		active, err = h.listInvestments(r.Context(), services.InvestmentListActive)
		if err != nil {
			mapInvestmentError(w, r, err)
			return
		}
	}
	data.Summary, err = summarizeInvestmentPositions(active, positions, purchases)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	today := civildate.CivilDateToday(appTimeLocation())
	for _, inv := range active {
		if inv.Status == models.InvestmentStatusAtiva && inv.ArchivedAt == nil && inv.MaturityDate != nil && *inv.MaturityDate >= today.String() &&
			(data.Summary.NextMaturityISO == "" || *inv.MaturityDate < data.Summary.NextMaturityISO) {
			data.Summary.NextMaturityISO = *inv.MaturityDate
			data.Summary.NextMaturity = investmentDateLabel(*inv.MaturityDate)
			data.Summary.NextMaturityName = inv.Name
		}
	}
	for _, inv := range investments {
		maturity, err := investmentMaturityLabel(inv, today)
		if err != nil {
			mapInvestmentError(w, r, err)
			return
		}
		total, ok := purchases[inv.ID]
		if !ok {
			mapInvestmentError(w, r, errors.New("missing investment purchases"))
			return
		}
		row := investmentRow{Investment: inv, Principal: formatCurrencyLabel(total.TotalCents), PurchaseCount: total.Count, CurrentValue: "Sem posição registrada", MaturityLabel: maturity}
		if position, ok := positions[inv.ID]; ok {
			row.CurrentValue = formatCurrencyLabel(position.AmountCents)
			row.PositionDate = investmentDateLabel(position.ValuationDate)
			row.PositionDateISO = position.ValuationDate
			row.PositionStale = total.LatestDate > position.ValuationDate
		}
		if inv.MaturityDate != nil {
			row.MaturityDateLabel = investmentDateLabel(*inv.MaturityDate)
			row.MaturityDateISO = *inv.MaturityDate
		}
		data.Investments = append(data.Investments, row)
	}
	accounts, err := h.DB.QueryContext(r.Context(), `SELECT id, name FROM accounts WHERE workspace_id = ? AND type = 'INVESTMENT' AND archived_at IS NULL ORDER BY name, id`, h.WorkspaceID)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	defer accounts.Close()
	data.Form = investmentFormData{Action: "/investimentos", CSRFToken: h.CSRFToken, RequestID: uuid.NewString()}
	if submitted != nil {
		data.Form = *submitted
	}
	data.StatusCode = status
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
	accounts.Close()
	data.Form.SourceAccounts, err = h.investmentSourceAccounts(r.Context())
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	h.renderInvestmentPage(w, r, "investimentos-page", data)
}

func (h *InvestimentosHandler) HandleDetalheInvestimento(w http.ResponseWriter, r *http.Request, id string) {
	h.renderInvestmentDetail(w, r, id, nil, http.StatusOK)
}

func (h *InvestimentosHandler) renderInvestmentDetail(w http.ResponseWriter, r *http.Request, id string, submitted *investmentFormData, status int, withdrawalSubmitted ...*investmentWithdrawalForm) {
	detail, valuations, err := h.investmentDetail(r.Context(), id)
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	data := h.investmentPageData()
	data.Detail = detail
	if r.URL.Query().Get("created") == "1" {
		data.Success = "Investimento cadastrado com sucesso."
	}
	if r.URL.Query().Get("purchased") == "1" {
		data.Success = "Reaplicação registrada com sucesso."
	}
	if mode := r.URL.Query().Get("withdrawn"); mode == services.InvestmentWithdrawalPartial {
		data.Success = "Retirada parcial registrada"
	} else if mode == services.InvestmentWithdrawalTotal {
		data.Success = "Retirada total registrada"
	}
	data.TotalWithdrawn = formatCurrencyLabel(detail.TotalWithdrawnCents)
	if variation := detail.AdjustedVariation; variation.Available {
		data.AdjustedVariationValue = formatCurrencyLabel(variation.DeltaCents)
		if variation.DeltaCents > 0 {
			data.AdjustedVariationValue = "+ " + data.AdjustedVariationValue
		} else if variation.DeltaCents < 0 {
			data.AdjustedVariationValue = "- " + strings.TrimPrefix(data.AdjustedVariationValue, "-")
		}
		data.AdjustedVariationStart = investmentDateLabel(variation.PreviousDate)
		data.AdjustedVariationEnd = investmentDateLabel(variation.CurrentDate)
	}
	data.WithdrawalForm = investmentWithdrawalForm{InvestmentID: id, CSRFToken: h.CSRFToken, RequestID: uuid.NewString(), Date: civildate.CivilDateToday(appTimeLocation()).String(), Mode: services.InvestmentWithdrawalPartial}
	if len(withdrawalSubmitted) > 0 && withdrawalSubmitted[0] != nil {
		data.WithdrawalForm = *withdrawalSubmitted[0]
	}
	submittedOptions := data.WithdrawalForm.Options
	data.WithdrawalForm.Options = nil
	labels := make(map[string]string)
	data.StatusCode = status
	data.Principal = formatCurrencyLabel(detail.TotalPurchasesCents)
	data.CurrentValue = formatCurrencyLabel(detail.CurrentCents)
	data.PositionReference = "Valor inicial · Sem posição registrada"
	data.AppliedDateLabel = investmentDateLabel(detail.Investment.AppliedDate)
	data.MaturityLabel, err = investmentMaturityLabel(detail.Investment, civildate.CivilDateToday(appTimeLocation()))
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	if detail.Investment.MaturityDate != nil {
		data.MaturityDateLabel = investmentDateLabel(*detail.Investment.MaturityDate)
		data.MaturityDateISO = *detail.Investment.MaturityDate
	}
	if detail.PositionDate != "" {
		date, err := time.Parse("2006-01-02", detail.PositionDate)
		if err != nil {
			mapInvestmentError(w, r, err)
			return
		}
		data.PositionDateLabel = date.Format("02/01/2006")
		data.PositionDateISO = detail.PositionDate
	}
	data.GrossYield = formatCurrencyLabel(detail.YieldCents)
	data.ReturnTone = ""
	if detail.YieldCents > 0 {
		data.GrossYield = "+ " + data.GrossYield
		data.ReturnTone = "text-emerald-700 dark:text-emerald-400"
	} else if detail.YieldCents < 0 {
		data.GrossYield = "- " + strings.TrimPrefix(data.GrossYield, "-")
		data.ReturnTone = "text-red-700 dark:text-red-400"
	}
	if detail.CurrentIsPrincipal {
		data.CurrentValue = "Sem posição registrada"
		data.GrossYield = "Indisponível"
		data.ReturnAvailable = false
	}
	if !detail.DifferenceAvailable {
		data.GrossYield = "Indisponível"
		data.ReturnTone = ""
	}
	for _, v := range valuations {
		data.Valuations = append(data.Valuations, investmentValuationRow{Date: investmentDateLabel(v.ValuationDate), DateISO: v.ValuationDate, Value: formatCurrencyLabel(v.AmountCents)})
	}
	data.Form = investmentFormData{Investment: detail.Investment, IsEdit: true, Action: "/investimentos/" + id + "/editar", CSRFToken: h.CSRFToken}
	data.PurchaseForm = investmentFormData{Investment: detail.Investment, Action: "/investimentos/" + id + "/compras", CSRFToken: h.CSRFToken, RequestID: uuid.NewString(), MinimumDate: detail.Investment.AppliedDate}
	data.PurchaseForm.Investment.AppliedDate = civildate.CivilDateToday(appTimeLocation()).String()
	if data.PurchaseForm.Investment.AppliedDate < detail.Investment.AppliedDate {
		data.PurchaseForm.Investment.AppliedDate = detail.Investment.AppliedDate
	}
	if submitted != nil {
		data.PurchaseForm = *submitted
	}
	data.PurchaseForm.MinimumDate = detail.Investment.AppliedDate
	data.PurchaseForm.SourceAccounts, err = h.investmentSourceAccounts(r.Context())
	if err != nil {
		mapInvestmentError(w, r, err)
		return
	}
	data.Funded = len(detail.PurchaseTransfers) > 0

	for purchaseIndex, purchase := range detail.Purchases {
		row := investmentPurchaseRow{InvestmentPurchase: purchase, Date: investmentDateLabel(purchase.PurchaseDate), Value: formatCurrencyLabel(purchase.PrincipalCents)}
		if receipt, ok := detail.PurchaseTransfers[purchase.ID]; ok {
			row.TransferID = receipt.TransactionID
			row.SourceAccountName = receipt.SourceAccountName
		}
		if purchase.MaturityDate != nil {
			row.Maturity = investmentDateLabel(*purchase.MaturityDate)
		}
		if purchase.TaxRegime != nil {
			switch *purchase.TaxRegime {
			case models.InvestmentTaxRegimeIRRegressivo:
				row.TaxLabel = "IR regressivo"
			case models.InvestmentTaxRegimeIsentoPF:
				row.TaxLabel = "Isento para pessoa física"
			case models.InvestmentTaxRegimeManual:
				row.TaxLabel = "Manual"
			}
		}
		state := detail.PurchaseStates[purchase.ID]
		row.Closed = state.Closed
		row.RemainingPrincipal = "Desconhecido"
		if !state.PrincipalUnknown {
			row.RemainingPrincipal = formatCurrencyLabel(purchase.PrincipalCents - state.KnownPrincipalWithdrawn)
		}
		label := "Compra " + strconv.Itoa(purchaseIndex+1) + " · " + row.Date + " · " + row.Value
		if purchase.NoteReference != "" {
			label += " · " + purchase.NoteReference
		}
		labels[purchase.ID] = label
		if !state.Closed {
			option := investmentWithdrawalOption{ID: purchase.ID, Label: label, Selected: detail.MaintainedPurchaseCount == 1 && len(withdrawalSubmitted) == 0}
			for _, previous := range submittedOptions {
				if previous.ID == purchase.ID {
					option.Selected, option.Received, option.Principal = previous.Selected, previous.Received, previous.Principal
				}
			}
			data.WithdrawalForm.Options = append(data.WithdrawalForm.Options, option)
		}
		data.Purchases = append(data.Purchases, row)
	}
	for _, withdrawal := range detail.Withdrawals {
		row := investmentWithdrawalRow{Date: investmentDateLabel(withdrawal.WithdrawalDate), DateISO: withdrawal.WithdrawalDate, Mode: "Parcial", Received: formatCurrencyLabel(withdrawal.ReceivedCents), Note: withdrawal.Note}
		if withdrawal.Mode == services.InvestmentWithdrawalTotal {
			row.Mode = "Total"
		}
		for _, a := range withdrawal.Allocations {
			allocation := investmentWithdrawalAllocationRow{Purchase: labels[a.PurchaseID], Received: formatCurrencyLabel(a.ReceivedCents), Principal: "Desconhecido", Closed: a.ClosesPurchase}
			if a.PrincipalCents != nil {
				allocation.Principal = formatCurrencyLabel(*a.PrincipalCents)
			}
			row.Allocations = append(row.Allocations, allocation)
		}
		data.Withdrawals = append(data.Withdrawals, row)
	}
	h.renderInvestmentPage(w, r, "investimento-detalhe-page", data)
}

func (h *InvestimentosHandler) latestInvestmentPositions(ctx context.Context) (map[string]models.InvestmentValuation, error) {
	rows, err := h.DB.QueryContext(ctx, `SELECT v.investment_id, v.valuation_date, v.amount_cents
		FROM investment_valuations v
		WHERE v.workspace_id = ? AND v.valuation_date = (
			SELECT MAX(latest.valuation_date) FROM investment_valuations latest
			WHERE latest.workspace_id = ? AND latest.investment_id = v.investment_id
		)`, h.WorkspaceID, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	positions := make(map[string]models.InvestmentValuation)
	for rows.Next() {
		var position models.InvestmentValuation
		if err := rows.Scan(&position.InvestmentID, &position.ValuationDate, &position.AmountCents); err != nil {
			return nil, err
		}
		positions[position.InvestmentID] = position
	}
	return positions, rows.Err()
}

func summarizeInvestmentPositions(active []models.Investment, positions map[string]models.InvestmentValuation, purchases ...map[string]services.InvestmentPurchaseTotal) (investmentSummary, error) {
	summary := investmentSummary{}
	var total int64
	latest := ""
	for _, inv := range active {
		if inv.Status != models.InvestmentStatusAtiva || inv.ArchivedAt != nil {
			continue
		}
		summary.ActiveCount++
		position, ok := positions[inv.ID]
		if !ok {
			summary.WithoutPosition++
			continue
		}
		if len(purchases) != 0 && purchases[0][inv.ID].LatestDate > position.ValuationDate {
			summary.WithoutPosition++
			summary.StalePositions++
			continue
		}
		if position.AmountCents < 0 || total > math.MaxInt64-position.AmountCents {
			return investmentSummary{}, errors.New("investment position total outside supported range")
		}
		total += position.AmountCents
		if position.ValuationDate > latest {
			latest = position.ValuationDate
		}
	}
	if summary.ActiveCount > summary.WithoutPosition {
		summary.KnownTotal = formatCurrencyLabel(total)
		summary.LatestDate = investmentDateLabel(latest)
		summary.LatestDateISO = latest
	} else {
		summary.KnownTotal = "Sem posição registrada"
		summary.LatestDate = "Indisponível"
	}
	return summary, nil
}

func investmentMaturityLabel(inv models.Investment, today civildate.CivilDate) (string, error) {
	if inv.Status == models.InvestmentStatusResgatada || inv.ArchivedAt != nil {
		return "Encerrada", nil
	}
	if inv.MaturityDate == nil {
		return "Sem data de vencimento", nil
	}
	maturity, err := civildate.ParseCivilDateInput(*inv.MaturityDate)
	if err != nil {
		return "", err
	}
	days := civildate.CivilDateDaysBetween(today, maturity)
	switch {
	case days < 0:
		return "Vencida", nil
	case days == 0:
		return "Vencendo nos próximos 30 dias · Vence hoje", nil
	case days <= 30:
		return "Vencendo nos próximos 30 dias", nil
	default:
		return "Sem vencimento próximo", nil
	}
}

func investmentDateLabel(date string) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return parsed.Format("02/01/2006")
}

func formatInvestmentBasisPoints(basisPoints int64) string {
	sign := ""
	magnitude := uint64(basisPoints)
	if basisPoints < 0 {
		sign = "-"
		magnitude = uint64(-(basisPoints + 1)) + 1
	} else if basisPoints > 0 {
		sign = "+"
	}
	return fmt.Sprintf("%s%d,%02d%%", sign, magnitude/100, magnitude%100)
}

func (h *InvestimentosHandler) investmentSourceAccounts(ctx context.Context) ([]investmentAccountOption, error) {
	rows, err := h.DB.QueryContext(ctx, `SELECT id,name FROM accounts WHERE workspace_id=? AND type IN ('CHECKING','SAVINGS','WALLET') AND archived_at IS NULL ORDER BY name,id`, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []investmentAccountOption
	for rows.Next() {
		var a investmentAccountOption
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
