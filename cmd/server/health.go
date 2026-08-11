package main

import (
	"encoding/json"
	"net/http"
)

type healthPinger interface {
	Ping() error
}

type healthResponse struct {
	Status string `json:"status"`
}

func registerHealthRoute(mux *http.ServeMux, runtime healthPinger) {
	mux.Handle("/health", newHealthHandler(runtime))
}

func newHealthHandler(db healthPinger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if err := db.Ping(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthResponse{Status: "unhealthy"})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "healthy"})
	})
}
