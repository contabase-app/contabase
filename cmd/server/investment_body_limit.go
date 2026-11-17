package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
)

const investmentMutationBodyMaxBytes = 64 << 10

// limitInvestmentMutationBodies bounds the body before withAuth reads a CSRF
// form field. Buffering the request here makes a chunked body fail with the
// same 413 response as an explicit oversized Content-Length; wrapping r.Body
// alone would let a form CSRF check report the overflow as an invalid token.
func limitInvestmentMutationBodies(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isInvestmentMutationRequest(r) || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > investmentMutationBodyMaxBytes {
			http.Error(w, "requisição muito grande", http.StatusRequestEntityTooLarge)
			return
		}
		limitedBody := http.MaxBytesReader(w, r.Body, investmentMutationBodyMaxBytes)
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
		next.ServeHTTP(w, r)
	}
}

func isInvestmentMutationRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	// withAuth checks CSRF before the route rejects an unsupported method, so
	// PUT and PATCH need the same bound as POST on this path.
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return false
	}
	path := strings.TrimSpace(r.URL.Path)
	return path == "/investimentos" || strings.HasPrefix(path, "/investimentos/")
}
