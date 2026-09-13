package main

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/contabase-app/contabase/internal/auth"
)

func workspaceSwitchRoute(authService *auth.Service, csrfSigner *csrfSigner) http.HandlerFunc {
	return withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		workspaceID := strings.TrimSpace(r.FormValue("workspace_id"))
		if workspaceID == "" {
			slog.Error("workspace switch failed: empty workspace_id", "user_id", ctx.UserID)
			http.Error(w, "workspace_id required", http.StatusBadRequest)
			return
		}
		slog.Debug("workspace switch requested", "user_id", ctx.UserID, "workspace_id", workspaceID, "active_workspace_id", ctx.ActiveWorkspaceID)
		member, err := authService.ResolveWorkspaceMember(ctx.UserID, workspaceID)
		if err != nil {
			slog.Error("workspace switch failed: membership not found", "user_id", ctx.UserID, "workspace_id", workspaceID, "error", err)
			setSecOpsFailureHeaders(w, "security.tampering", "CRITICAL", "workspace_scope_violation", "")
			w.Header().Set("X-SecOps-Meta-Workspace-Id", workspaceID)
			http.Error(w, "workspace not found", http.StatusForbidden)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			slog.Error("workspace switch failed: missing session cookie", "user_id", ctx.UserID, "workspace_id", workspaceID)
			http.Error(w, "session not found", http.StatusUnauthorized)
			return
		}
		if err := authService.UpdateSessionWorkspace(cookie.Value, workspaceID); err != nil {
			slog.Error("workspace switch failed: unable to update session", "user_id", ctx.UserID, "workspace_id", workspaceID, "error", err)
			http.Error(w, "unable to switch workspace", http.StatusInternalServerError)
			return
		}
		slog.Debug("workspace switch session updated", "user_id", ctx.UserID, "workspace_id", workspaceID, "role", member.Role)
		http.SetCookie(w, &http.Cookie{
			Name:     "workspace_role",
			Value:    member.Role,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   shouldUseSecureCookie(r),
			MaxAge:   86400 * 30,
		})
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}
