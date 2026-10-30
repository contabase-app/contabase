package handlers

import (
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

type investmentSettlementResponse struct {
	Settlement              services.InvestmentSettlement
	ReceiptConfirmationKind string
}

// Context authority is supplied by the authenticated route. Every related row
// and capacity reservation is validated again inside C1's writer transaction.
func (h *InvestimentosHandler) HandleInvestmentSettlement(w http.ResponseWriter, r *http.Request, investment, withdrawal string, confirm bool) {
	if !features.FromEnv().InvestmentSettlements {
		http.NotFound(w, r)
		return
	}
	if h.WorkspaceID == "" || h.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet && !confirm {
		s, err := services.ReadInvestmentSettlementOperationalState(r.Context(), h.DB, h.WorkspaceID, investment, withdrawal)
		if err != nil {
			investmentOperationError(w, r, err)
			return
		}
		investmentOperationJSON(w, s)
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
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
	}
	get := func(key string) string { return strings.TrimSpace(r.PostForm.Get(key)) }
	key, err := uuid.Parse(get("request_id"))
	if err != nil {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	version, err := strconv.ParseInt(get("expected_version"), 10, 64)
	if err != nil {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	net, err := strconv.ParseInt(get("net_cents"), 10, 64)
	if err != nil {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	// The confirmation endpoint requires the whole explicit C1 payload. It never
	// reconstructs a mutation from a potentially stale pre-read or guesses receipts.
	if raw := get("confirm_receipt"); (confirm && raw != "true") || (!confirm && raw != "" && raw != "false") {
		investmentOperationError(w, r, services.ErrInvestmentValidation)
		return
	}
	in := services.RecordInvestmentSettlementInput{WorkspaceID: h.WorkspaceID, InvestmentID: investment, WithdrawalID: withdrawal,
		AccountID: get("account_id"), DestinationAccountID: get("destination_account_id"), EconomicDate: get("economic_date"),
		CreatedBy: h.UserID, RequestID: key.String(), Reason: get("reason"), ExpectedVersion: version, NetCents: net,
		PatrimonyEvidence: get("patrimony_evidence"), ReceiptEvidence: get("receipt_evidence"), ConfirmReceipt: confirm}
	for _, field := range []struct {
		name string
		dest **int64
	}{
		{"gross_cents", &in.GrossCents}, {"ir_cents", &in.IRCents}, {"iof_cents", &in.IOFCents}, {"fees_cents", &in.FeesCents}, {"patrimony_cents", &in.PatrimonyCents},
	} {
		if raw := get(field.name); raw != "" {
			cents, e := strconv.ParseInt(raw, 10, 64)
			if e != nil {
				investmentOperationError(w, r, services.ErrInvestmentValidation)
				return
			}
			*field.dest = &cents
		}
	}
	if raw := get("receipts"); raw != "" {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in.Receipts); err != nil {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			investmentOperationError(w, r, services.ErrInvestmentValidation)
			return
		}
	}
	result, err := services.RecordInvestmentSettlement(r.Context(), h.WriterDB, in)
	if err != nil {
		var commitError *services.InvestmentCommitError
		if errors.As(err, &commitError) {
			respondInvestmentLifecycleError(w, r, err)
		} else {
			investmentOperationError(w, r, err)
		}
		return
	}
	w.Header().Set("Idempotency-Key", key.String())
	w.Header().Set("Link", "</investimentos/"+url.PathEscape(investment)+"/financeiro/retiradas/"+url.PathEscape(withdrawal)+"/liquidacao>; rel=\"status\"")
	response := investmentSettlementResponse{Settlement: result}
	if result.State == services.InvestmentSettlementReconciled {
		response.ReceiptConfirmationKind = services.InvestmentReceiptUserDeclaration
	}
	// C1 supplies no invocation outcome metadata. The durable revision is the
	// result for both creation and replay; no handler-side replay guess is made.
	investmentOperationJSON(w, response)
}
