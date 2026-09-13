package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
)

const recentAdminAuthenticationWindow = 10 * time.Minute

var errRecentAdminAuthenticationRequired = errors.New("recent admin authentication required")

type adminReauthPageData struct {
	CSRFToken             string
	ReturnTo              string
	RequiresCurrentFactor bool
	Error                 string
}

func adminReauthRoute(
	authService *auth.Service,
	csrfSigner *csrfSigner,
	tpl handlers.TemplateEngine,
	limiter *memoryRateLimiter,
	policy rateLimitPolicy,
) http.HandlerFunc {
	return withAuth(authService, csrfSigner, func(w http.ResponseWriter, r *http.Request, ctx authContext) {
		if !requireGlobalAdmin(w, r, ctx) {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		returnTo := safeAdminReauthReturnPath(r.URL.Query().Get("return_to"))
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "formulário inválido", http.StatusBadRequest)
				return
			}
			returnTo = safeAdminReauthReturnPath(r.FormValue("return_to"))
		}

		requiresCurrentFactor, err := authService.IsTOTPEnabled(ctx.UserID)
		if err != nil {
			slog.Error("admin reauthentication factor lookup failed", "user_id", ctx.UserID, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodGet {
			renderAdminReauthPage(w, tpl, adminReauthPageData{
				CSRFToken:             ctx.CSRFToken,
				ReturnTo:              returnTo,
				RequiresCurrentFactor: requiresCurrentFactor,
			}, http.StatusOK)
			return
		}

		key := "admin:reauth:user:" + ctx.UserID + ":ip:" + clientIP(r)
		if !allowRateLimitedRequest(w, r, limiter, key, policy, time.Now()) {
			writeAuthAuditEvent(authService.DB, r, ctx.UserID, ctx.ActiveWorkspaceID, "ADMIN_REAUTH_RATE_LIMITED", nil)
			return
		}
		if err := verifySensitiveMFAAction(authService, ctx.UserID, r.FormValue("current_password"), r.FormValue("current_totp_code")); err != nil {
			writeAuthAuditEvent(authService.DB, r, ctx.UserID, ctx.ActiveWorkspaceID, "ADMIN_REAUTH_FAILED", nil)
			renderAdminReauthPage(w, tpl, adminReauthPageData{
				CSRFToken:             ctx.CSRFToken,
				ReturnTo:              returnTo,
				RequiresCurrentFactor: requiresCurrentFactor,
				Error:                 "Não foi possível confirmar sua identidade.",
			}, http.StatusUnauthorized)
			return
		}
		if err := authService.MarkSessionReauthenticated(ctx.SessionToken, ctx.UserID, time.Now()); err != nil {
			slog.Error("admin reauthentication session update failed", "user_id", ctx.UserID, "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		writeAuthAuditEvent(authService.DB, r, ctx.UserID, ctx.ActiveWorkspaceID, "ADMIN_REAUTH_SUCCESS", nil)
		redirectAfterAdminReauthentication(w, r, returnTo)
	})
}

func requireRecentAuthentication(w http.ResponseWriter, r *http.Request, authService *auth.Service, ctx authContext, returnTo string) bool {
	recent, err := authService.SessionRecentlyAuthenticated(ctx.SessionToken, time.Now(), recentAdminAuthenticationWindow)
	if err != nil {
		slog.Error("recent authentication lookup failed", "user_id", ctx.UserID, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return false
	}
	if recent {
		return true
	}
	redirectToAdminReauthentication(w, r, returnTo)
	return false
}

func safeAdminReauthReturnPath(raw string) string {
	switch strings.TrimSpace(raw) {
	case "/admin/usuarios", "/admin/workspaces", "/admin/backups", "/admin/auditoria":
		return strings.TrimSpace(raw)
	default:
		return "/configuracoes"
	}
}

func redirectToAdminReauthentication(w http.ResponseWriter, r *http.Request, returnTo string) {
	target := "/admin/reautenticar?" + url.Values{"return_to": {safeAdminReauthReturnPath(returnTo)}}.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func redirectAfterAdminReauthentication(w http.ResponseWriter, r *http.Request, returnTo string) {
	target := safeAdminReauthReturnPath(returnTo)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func renderAdminReauthPage(w http.ResponseWriter, tpl handlers.TemplateEngine, data adminReauthPageData, status int) {
	var body bytes.Buffer
	if err := tpl.ExecuteTemplate(&body, "admin-reauth-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}
