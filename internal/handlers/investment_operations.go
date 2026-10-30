package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/contabase-app/contabase/internal/features"
	"github.com/contabase-app/contabase/internal/services"
	"github.com/google/uuid"
)

func investmentOperationJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func investmentOperationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, services.ErrInvestmentFundingPending), errors.Is(err, services.ErrInvestmentSettlementPending):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		respondInvestmentWriteError(w, r, err)
	}
}

// HandleInvestmentOperation is bound exclusively by the authenticated routes.
// Authority and workspace come from withAuth, never the submitted form.
func (h *InvestimentosHandler) HandleInvestmentOperation(w http.ResponseWriter, r *http.Request, investment, operation string) {
	if !features.FromEnv().InvestmentOperations {
		http.NotFound(w, r)
		return
	}
	if h.WorkspaceID == "" || h.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if operation == "estado" {
		state, err := services.ReadInvestmentOperationalState(r.Context(), h.DB, h.WorkspaceID, investment, r.URL.Query().Get("position_id"))
		if err != nil {
			investmentOperationError(w, r, err)
			return
		}
		investmentOperationJSON(w, state)
		return
	}
	if h.WriterDB == nil {
		http.Error(w, "writer indisponível", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	get := func(key string) string { return strings.TrimSpace(r.PostForm.Get(key)) }
	for _, values := range r.PostForm {
		if len(values) != 1 {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
	}
	key, err := uuid.Parse(get("request_id"))
	if err != nil {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	r.PostForm.Set("request_id", key.String())
	if closed := get("day_closed"); closed != "" && closed != "true" && closed != "false" {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	version, err := strconv.ParseInt(get("expected_version"), 10, 64)
	if err != nil {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	dayClosed := get("day_closed") == "true"
	switch operation {
	case "referencia":
		if get("confirm") != "true" {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
		represented, err := strconv.ParseInt(get("represented_cents"), 10, 64)
		if err != nil {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
		// Initial confirmation requires exact reconciliation, never a compensating
		// balance adjustment. On replay use immutable evidence, then let A compare
		// its canonical payload (including session author). This lookup validates
		// reconciliation only; the writer transaction alone determines outcome.
		var savedID string
		err = h.DB.QueryRowContext(r.Context(), `SELECT id FROM investment_reference_revisions WHERE workspace_id=? AND investment_id=? AND request_id=?`, h.WorkspaceID, investment, get("request_id")).Scan(&savedID)
		var date string
		var amount int64
		if err == nil {
			saved, e := services.ReadInvestmentReferenceRevision(r.Context(), h.DB, h.WorkspaceID, investment, savedID)
			if e != nil {
				investmentOperationError(w, r, e)
				return
			}
			date, amount = saved.Candidate.Snapshot.Position.ValuationDate, saved.Candidate.Snapshot.Position.AmountCents
		} else if errors.Is(err, sql.ErrNoRows) {
			candidate, e := services.ReadInvestmentReferenceCandidate(r.Context(), h.DB, h.WorkspaceID, investment, get("position_id"))
			if e != nil {
				investmentOperationError(w, r, e)
				return
			}
			if candidate.Snapshot.AccountID != get("account_id") {
				investmentOperationError(w, r, services.ErrInvestmentNotFound)
				return
			}
			if candidate.CoverageToken != get("coverage_token") {
				investmentOperationError(w, r, services.ErrInvestmentConflict)
				return
			}
			date, amount = candidate.Snapshot.Position.ValuationDate, candidate.Snapshot.Position.AmountCents
		} else {
			investmentOperationError(w, r, err)
			return
		}
		if date != get("economic_date") {
			investmentOperationError(w, r, services.ErrInvestmentConflict)
			return
		}
		if represented != amount {
			http.Error(w, "conciliação histórica pendente: valor representado difere da posição", http.StatusConflict)
			return
		}
		result, e := services.RecordInvestmentReferenceWithOutcome(r.Context(), h.WriterDB, services.RecordInvestmentReferenceInput{WorkspaceID: h.WorkspaceID, InvestmentID: investment, AccountID: get("account_id"), PositionID: get("position_id"), CreatedBy: h.UserID, RequestID: get("request_id"), Reason: get("reason"), CoverageToken: get("coverage_token"), ExpectedVersion: version, RepresentedCents: &represented, Confirm: true, DayClosed: dayClosed})
		if e != nil {
			investmentOperationError(w, r, e)
			return
		}
		w.Header().Set("Idempotency-Key", key.String())
		w.Header().Set("Link", "</investimentos/"+url.PathEscape(investment)+"/financeiro/estado>; rel=\"status\"")
		investmentOperationJSON(w, result)
	case "posicao":
		if _, err := uuid.Parse(get("reference_revision_id")); err != nil {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
		input := services.IntegrateInvestmentPositionInput{WorkspaceID: h.WorkspaceID, InvestmentID: investment, AccountID: get("account_id"), ReferenceRevisionID: get("reference_revision_id"), PositionID: get("position_id"), CoverageToken: get("coverage_token"), CreatedBy: h.UserID, RequestID: get("request_id"), Reason: get("reason"), ExpectedVersion: version, DayClosed: dayClosed}
		// Optional correction is passed directly to B's atomic writer; never save
		// a valuation before integration has succeeded.
		if raw, ok := r.PostForm["correction_cents"]; ok {
			if len(raw) != 1 {
				investmentOperationError(w, r, services.ErrInvestmentValidation)
				return
			}
			cents, e := strconv.ParseInt(raw[0], 10, 64)
			if e != nil {
				investmentOperationError(w, r, services.ErrInvestmentValidation)
				return
			}
			input.CorrectionCents = &cents
		}
		if raw := get("funding"); raw != "" {
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.DisallowUnknownFields()
			if e := dec.Decode(&input.Funding); e != nil {
				investmentOperationError(w, r, services.ErrInvestmentValidation)
				return
			}
			if e := dec.Decode(new(any)); e != io.EOF {
				investmentOperationError(w, r, services.ErrInvestmentValidation)
				return
			}
		}
		result, e := services.IntegrateInvestmentPositionWithOutcome(r.Context(), h.WriterDB, input)
		if e != nil {
			investmentOperationError(w, r, e)
			return
		}
		// A successful commit always returns its durable idempotent result. Fetching
		// mutable state afterwards could turn a committed operation into an error;
		// callers obtain fresh operational state through the separate read route.
		w.Header().Set("Idempotency-Key", key.String())
		w.Header().Set("Link", "</investimentos/"+url.PathEscape(investment)+"/financeiro/estado>; rel=\"status\"")
		investmentOperationJSON(w, result)
	default:
		http.NotFound(w, r)
	}
}
