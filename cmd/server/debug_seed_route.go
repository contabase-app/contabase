package main

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/models"
)

func seedDemoB2BRoute(
	db *sql.DB,
	authService *auth.Service,
	csrfSigner *csrfSigner,
	rateLimiter *memoryRateLimiter,
	policy rateLimitPolicy,
) http.HandlerFunc {
	return withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.EqualFold(strings.TrimSpace(os.Getenv("APP_DEBUG")), "true") {
			http.Error(w, "seed demo desabilitada fora do modo debug", http.StatusForbidden)
			return
		}
		if !models.HasPermission(&ctx.Member, string(permConfigWrite)) {
			respondForbidden(w, r)
			return
		}
		key := "admin:debug:seed-demo-b2b:workspace:" + ctx.ActiveWorkspaceID + ":user:" + ctx.UserID + ":route:" + r.URL.Path
		if !allowRateLimitedRequest(w, r, rateLimiter, key, policy, time.Now()) {
			return
		}
		if err := seedDemoB2B(db, ctx.ActiveWorkspaceID, ctx.UserID); err != nil {
			http.Error(w, "erro ao gerar seed demo B2B: "+err.Error(), http.StatusUnprocessableEntity)
			return
		}
		slog.Warn("admin_action", "action", "seed_demo_b2b", "actor_user_id", ctx.UserID, "workspace_id", ctx.ActiveWorkspaceID, "client_ip", clientIP(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Seed demo B2B aplicada com sucesso para Abril, Maio e Junho de 2026."))
	})
}
