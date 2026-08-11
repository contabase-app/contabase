package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
)

const (
	invoicePaymentReversalPath         = "/cartoes/faturas/pagamentos/reverter"
	invoicePaymentReversalMaxBodyBytes = 16 << 10
)

func invoicePaymentReversalRoute(
	authService *auth.Service,
	csrfSigner *csrfSigner,
	base *handlers.FaturasHandler,
) http.HandlerFunc {
	authenticated := withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !hasPermission(ctx.Role, permInvoicesStatusUpdate) {
			respondForbidden(w, r)
			return
		}
		if base == nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		h := *base
		h.WorkspaceID = ctx.ActiveWorkspaceID
		h.UserID = ctx.UserID
		h.HandleReverseInvoicePayment(w, r)
	})

	return func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > invoicePaymentReversalMaxBodyBytes {
			http.Error(w, "requisição muito grande", http.StatusRequestEntityTooLarge)
			return
		}
		limitedBody := http.MaxBytesReader(w, r.Body, invoicePaymentReversalMaxBodyBytes)
		body, err := io.ReadAll(limitedBody)
		_ = limitedBody.Close()
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "requisição muito grande", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "dados inválidos", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		authenticated(w, r)
	}
}
