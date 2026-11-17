package main

import (
	"bytes"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/handlers"
)

func TestAdminUpdateRoutesAuthenticationAuthorizationAndCSRF(t *testing.T) {
	db := openAdminUsersRBACTestDB(t)
	t.Cleanup(func() { db.Close() })
	service := auth.NewService(db)
	signer := &csrfSigner{secret: bytes.Repeat([]byte{7}, 32), ttl: time.Hour}
	tpl := template.Must(template.New("updates").Parse(`{{define "configuracoes-admin_updates-content"}}updates-admin-only{{end}}{{define "configuracoes-admin_updates-panel"}}updates-admin-only{{end}}`))
	mux := http.NewServeMux()
	registerAdminUpdateRoutes(mux, service, signer, func(ctx authContext) handlers.ConfiguracoesHandler {
		return handlers.ConfiguracoesHandler{ActorRole: ctx.Role, Templates: tpl}
	})
	for _, role := range []string{"ADMIN", "MANAGER", "USER", "anonymous"} {
		token := ""
		if role != "anonymous" {
			user := map[string]string{"ADMIN": "admin-user", "MANAGER": "manager-user", "USER": "regular-user"}[role]
			token = createAdminUsersRBACSession(t, service, user, role)
		}
		for _, path := range []string{"/admin/atualizacoes", "/admin/atualizacoes/status", "/admin/atualizacoes/verificar"} {
			for _, csrf := range []bool{false, true} {
				method := http.MethodGet
				if strings.HasSuffix(path, "verificar") {
					method = http.MethodPost
				}
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("HX-Request", "true")
				if token != "" {
					req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
				}
				if csrf {
					addAdminUsersRBACCSRF(t, req, signer)
				}
				rr := httptest.NewRecorder()
				mux.ServeHTTP(rr, req)
				allowed := role == "ADMIN" && (method == http.MethodGet || csrf)
				if allowed {
					if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "updates-admin-only") {
						t.Fatalf("admin denied: %s %d %s", path, rr.Code, rr.Body.String())
					}
				} else {
					if strings.Contains(rr.Body.String(), "updates-admin-only") {
						t.Fatalf("%s leaked admin response", role)
					}
					if role == "MANAGER" || role == "USER" || (method == http.MethodPost && !csrf) {
						if rr.Code != http.StatusForbidden {
							t.Fatalf("%s %s = %d, want 403", role, path, rr.Code)
						}
					} else if rr.Header().Get("HX-Redirect") == "" && rr.Code != http.StatusUnauthorized {
						t.Fatalf("anonymous response: %d %s", rr.Code, rr.Body.String())
					}
				}
			}
		}
	}
}
