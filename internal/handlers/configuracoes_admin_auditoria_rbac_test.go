package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ADM2 — revalidação interna do HandleAdminAuditoriaSave: papéis não ADMIN são
// rejeitados com 403 antes de qualquer acesso ao banco, no mesmo padrão dos
// demais handlers administrativos (configuracoes.go:1655, 1871, 1911, 1954...).
func TestAdminAuditoriaSaveRejectsNonAdminRolesBeforeDatabase(t *testing.T) {
	for _, role := range []string{"MANAGER", "USER", "", "SUPERUSER"} {
		t.Run("role="+role, func(t *testing.T) {
			handler := newConfiguracoesRenderTestHandler(t)
			cipher := seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)
			handler.ActorRole = role

			req := httptest.NewRequest(http.MethodPost, "/admin/auditoria/salvar", strings.NewReader(url.Values{
				"smtp_host": {"smtp.negado.exemplo"},
				"smtp_port": {"587"},
				"smtp_user": {"negado@exemplo.com"},
				"smtp_pass": {adm2ReplacementKey},
			}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			handler.HandleAdminAuditoriaSave(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("role %q deve receber 403 antes de qualquer acesso ao banco, recebeu %d", role, rr.Code)
			}
			if got := storedAdm2SMTPPass(t, handler, "config-workspace"); got != cipher {
				t.Fatal("papel não autorizado não pode alterar a credencial")
			}
			var host string
			if err := handler.DB.QueryRow(`SELECT COALESCE(smtp_host, '') FROM workspaces WHERE id = 'config-workspace'`).Scan(&host); err != nil || host != "" {
				t.Fatalf("papel não autorizado não pode gravar host: %q err=%v", host, err)
			}
		})
	}
}

func TestAdminAuditoriaSaveAllowsAdminRole(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	if handler.ActorRole != "ADMIN" {
		t.Fatalf("helper deve semear papel ADMIN, tem %q", handler.ActorRole)
	}

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host": {"smtp.admin.exemplo"},
		"smtp_port": {"587"},
		"smtp_user": {"admin@exemplo.com"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	var host string
	if err := handler.DB.QueryRow(`SELECT smtp_host FROM workspaces WHERE id = 'config-workspace'`).Scan(&host); err != nil || host != "smtp.admin.exemplo" {
		t.Fatalf("ADMIN deve conseguir salvar: host=%q err=%v", host, err)
	}
}
