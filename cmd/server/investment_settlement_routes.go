package main

import (
	"net/http"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/features"
	"github.com/contabase-app/contabase/internal/handlers"
	"github.com/contabase-app/contabase/internal/models"
)

// Only the JSON settlement routes translate the existing login redirect to 401.
// Session resolution, CSRF and authorization still use the production guards.
type settlementAuthResponse struct {
	http.ResponseWriter
	unauthorized bool
}

func (w *settlementAuthResponse) WriteHeader(status int) {
	if status == http.StatusSeeOther && w.Header().Get("Location") == "/login" {
		w.unauthorized = true
		w.Header().Del("Location")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.ResponseWriter.WriteHeader(http.StatusUnauthorized)
		_, _ = w.ResponseWriter.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *settlementAuthResponse) Write(b []byte) (int, error) {
	if w.unauthorized {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func registerInvestmentSettlementRoutes(mux *http.ServeMux, authService *auth.Service, signer *csrfSigner, base *handlers.InvestimentosHandler) {
	const path = "/investimentos/{investment}/financeiro/retiradas/{withdrawal}/liquidacao"
	for _, operation := range []string{"", "conciliar", "aplicar", "classificar"} {
		pattern := path
		if operation != "" {
			pattern += "/" + operation
		}
		effects := operation == "aplicar" || operation == "classificar"
		guarded := withAuth(authService, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
			permission := models.PermissionInvestmentsWrite
			if r.Method == http.MethodGet {
				permission = models.PermissionInvestmentsRead
			}
			if !models.HasPermission(&ctx.Member, permission) {
				respondForbidden(w, r)
				return
			}
			h := *base // restore refreshes the runtime owner through this pointer
			h.WorkspaceID, h.UserID = ctx.ActiveWorkspaceID, ctx.UserID
			if effects {
				h.HandleInvestmentSettlementEffect(w, r, r.PathValue("investment"), r.PathValue("withdrawal"), operation)
			} else {
				h.HandleInvestmentSettlement(w, r, r.PathValue("investment"), r.PathValue("withdrawal"), operation == "conciliar")
			}
		})
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			flags := features.FromEnv()
			if !flags.InvestmentSettlements || effects && !flags.InvestmentSettlementEffects {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodPost && (r.Method != http.MethodGet || operation != "") {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			// CSRF may parse the form, so bound the body before the shared guard.
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			guarded(&settlementAuthResponse{ResponseWriter: w}, r)
		})
	}
}
