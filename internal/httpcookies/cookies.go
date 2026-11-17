package httpcookies

import (
	"net/http"
	"time"
)

// ClearSession expires the authenticated session cookie. The caller supplies the
// same Secure policy used when the cookie was issued, so the browser can clear
// it on both HTTP-local and HTTPS/proxied deployments.
func ClearSession(w http.ResponseWriter, secure bool) {
	clear(w, Session, secure)
}

// ClearPreAuth expires the pending second-factor challenge cookie.
func ClearPreAuth(w http.ResponseWriter, secure bool) {
	clear(w, PreAuth, secure)
}

func clear(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		MaxAge:   -1,
		Expires:  time.Now().Add(-24 * time.Hour),
	})
}
