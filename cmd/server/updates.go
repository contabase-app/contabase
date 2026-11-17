package main

import (
	"net/http"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
)

func registerAdminUpdateRoutes(mux *http.ServeMux, service *auth.Service, signer *csrfSigner, handlerFor func(authContext) handlers.ConfiguracoesHandler) {
	for _, path := range []string{"/admin/atualizacoes", "/admin/atualizacoes/status", "/admin/atualizacoes/verificar"} {
		mux.HandleFunc(path, withAuth(service, signer, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
			if !requireGlobalAdmin(w, r, ctx) {
				return
			}
			h := handlerFor(ctx)
			h.UpdatesCSRFToken = ctx.CSRFToken
			h.HandleAdminUpdates(w, r)
		}))
	}
}
