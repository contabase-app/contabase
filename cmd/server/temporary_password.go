package main

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"

	"golang.org/x/crypto/bcrypt"
)

type requiredPasswordChangeData struct {
	Error     string
	CSRFToken string
}

func handleRequiredPasswordChangePage(w http.ResponseWriter, tpl handlers.TemplateEngine, csrfToken, errMsg string) {
	data := requiredPasswordChangeData{Error: errMsg, CSRFToken: csrfToken}
	if err := tpl.ExecuteTemplate(w, "required-password-change-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func handleRequiredPasswordChangeSubmit(w http.ResponseWriter, r *http.Request, tpl handlers.TemplateEngine, authService *auth.Service, db *sql.DB, ctx authContext, csrfToken string) {
	if err := r.ParseForm(); err != nil {
		handleRequiredPasswordChangePage(w, tpl, csrfToken, "Formulário inválido.")
		return
	}
	state, err := authService.TemporaryPasswordState(ctx.UserID, time.Now())
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !state.Required {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if state.Expired {
		writeAuthAuditEvent(db, r, ctx.UserID, ctx.ActiveWorkspaceID, "TEMPORARY_PASSWORD_CHANGE_DENIED", map[string]string{"reason": "temporary_password_expired"})
		handleRequiredPasswordChangePage(w, tpl, csrfToken, "Senha temporária expirada. Solicite um novo reset de acesso.")
		return
	}

	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")
	if len(newPassword) < 8 {
		handleRequiredPasswordChangePage(w, tpl, csrfToken, "A nova senha deve ter no mínimo 8 caracteres.")
		return
	}
	if newPassword != confirmPassword {
		handleRequiredPasswordChangePage(w, tpl, csrfToken, "A confirmação da senha não confere.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if _, err := db.Exec(`
		UPDATE users
		SET password_hash = ?,
		    must_change_password = 0,
		    temporary_password_expires_at = NULL,
		    updated_at = unixepoch()
		WHERE id = ?
	`, string(hash), ctx.UserID); err != nil {
		handleRequiredPasswordChangePage(w, tpl, csrfToken, "Não foi possível atualizar a senha.")
		return
	}

	currentToken := ctx.SessionToken
	if strings.TrimSpace(currentToken) == "" {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			currentToken = cookie.Value
		}
	}
	if strings.TrimSpace(currentToken) != "" {
		_ = authService.RevokeUserSessionsExcept(ctx.UserID, currentToken)
	} else {
		_ = authService.RevokeAllUserSessions(ctx.UserID)
	}
	_ = authService.RevokeAllUserPreAuthSessions(ctx.UserID)
	writeAuthAuditEvent(db, r, ctx.UserID, ctx.ActiveWorkspaceID, "TEMPORARY_PASSWORD_CHANGED", map[string]string{"method": "required_change"})
	log.Printf("temporary password changed: user=%s", ctx.UserID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
