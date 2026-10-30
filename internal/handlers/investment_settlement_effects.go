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

func investmentSettlementEffectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, services.ErrBalanceAdjustmentConflict), errors.Is(err, services.ErrInvestmentLegacyIncome), errors.Is(err, services.ErrInvestmentLegacyClassification):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		var commitError *services.InvestmentCommitError
		if errors.As(err, &commitError) {
			respondInvestmentLifecycleError(w, r, err)
		} else {
			investmentOperationError(w, r, err)
		}
	}
}

// Each authenticated request invokes exactly one existing transactional writer.
// No financial amount, session authority or replay outcome is inferred here.
func (h *InvestimentosHandler) HandleInvestmentSettlementEffect(w http.ResponseWriter, r *http.Request, investment, withdrawal, operation string) {
	if !features.FromEnv().InvestmentSettlementEffects || operation != "aplicar" && operation != "classificar" {
		http.NotFound(w, r)
		return
	}
	if h.WorkspaceID == "" || h.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.WriterDB == nil {
		http.Error(w, "writer indisponível", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
		return
	}
	allowed := " request_id csrf_token settlement_revision_id expected_settlement_version "
	if operation == "aplicar" {
		allowed += "reference_revision_id financial_revision_id expected_financial_version expected_balance_version expected_destination_version reason reconciliation_evidence prior_effects remaining_position_id remaining_coverage_token day_closed "
	} else {
		allowed += "application_id expected_version category_id legacy_reconciliation_evidence legacy_transaction_ids "
	}
	for key, values := range r.PostForm {
		if len(values) != 1 || !strings.Contains(allowed, " "+key+" ") {
			investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
			return
		}
	}
	get := func(key string) string { return strings.TrimSpace(r.PostForm.Get(key)) }
	key, err := uuid.Parse(get("request_id"))
	if err != nil {
		investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
		return
	}
	version, err := strconv.ParseInt(get("expected_settlement_version"), 10, 64)
	if err != nil {
		investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
		return
	}
	decode := func(name string, dest any) error {
		if get(name) == "" {
			return nil
		}
		dec := json.NewDecoder(strings.NewReader(get(name)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dest); err != nil {
			return services.ErrInvestmentValidation
		}
		if dec.Decode(new(any)) != io.EOF {
			return services.ErrInvestmentValidation
		}
		return nil
	}
	var result any
	if operation == "aplicar" {
		in := services.ApplyInvestmentSettlementInput{WorkspaceID: h.WorkspaceID, InvestmentID: investment, WithdrawalID: withdrawal,
			SettlementRevisionID: get("settlement_revision_id"), ReferenceRevisionID: get("reference_revision_id"), FinancialRevisionID: get("financial_revision_id"),
			ExpectedSettlementVersion: version, CreatedBy: h.UserID, RequestID: key.String(), Reason: get("reason"),
			ReconciliationEvidence: get("reconciliation_evidence"), RemainingPositionID: get("remaining_position_id"), RemainingCoverageToken: get("remaining_coverage_token"), DayClosed: get("day_closed") == "true"}
		for _, field := range []struct {
			name string
			dest *int64
		}{
			{"expected_financial_version", &in.ExpectedFinancialVersion}, {"expected_balance_version", &in.ExpectedBalanceVersion}, {"expected_destination_version", &in.ExpectedDestinationVersion},
		} {
			*field.dest, err = strconv.ParseInt(get(field.name), 10, 64)
			if err != nil {
				investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
				return
			}
		}
		if raw := get("day_closed"); raw != "" && raw != "true" && raw != "false" {
			investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
			return
		}
		if err = decode("prior_effects", &in.PriorEffects); err == nil {
			result, err = services.ApplyInvestmentSettlement(r.Context(), h.WriterDB, in)
		}
	} else {
		in := services.ClassifyInvestmentSettlementInput{WorkspaceID: h.WorkspaceID, InvestmentID: investment, WithdrawalID: withdrawal,
			SettlementRevisionID: get("settlement_revision_id"), ApplicationID: get("application_id"), ExpectedSettlementVersion: version,
			CreatedBy: h.UserID, RequestID: key.String(), CategoryID: get("category_id"), LegacyReconciliationEvidence: get("legacy_reconciliation_evidence")}
		in.ExpectedVersion, err = strconv.ParseInt(get("expected_version"), 10, 64)
		if err != nil {
			investmentSettlementEffectError(w, r, services.ErrInvestmentValidation)
			return
		}
		if err = decode("legacy_transaction_ids", &in.LegacyTransactionIDs); err == nil {
			result, err = services.ClassifyInvestmentSettlementResult(r.Context(), h.WriterDB, in)
		}
	}
	if err != nil {
		investmentSettlementEffectError(w, r, err)
		return
	}
	w.Header().Set("Idempotency-Key", key.String())
	w.Header().Set("Link", "</investimentos/"+url.PathEscape(investment)+"/financeiro/retiradas/"+url.PathEscape(withdrawal)+"/liquidacao>; rel=\"status\"")
	// These services return the same durable result on creation and replay.
	// A later read failure must not mask a committed operation.
	investmentOperationJSON(w, result)
}
