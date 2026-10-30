package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/security"
)

// ADM2 — contrato da credencial SMTP. Todos os segredos são sintéticos.

const (
	adm2StoredSecret   = "sintetico-smtp-armazenado-4451"
	adm2ReplacementKey = "sintetico-smtp-substituto-8819"
)

func seedAdm2SMTPCredential(t *testing.T, handler ConfiguracoesHandler, workspaceID, secret string) string {
	t.Helper()
	t.Setenv("SECURITY_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	cipher, err := security.Encrypt(secret)
	if err != nil {
		t.Fatalf("encrypt synthetic secret: %v", err)
	}
	execTestSQL(t, handler.DB, `UPDATE workspaces SET smtp_pass = ? WHERE id = ?`, cipher, workspaceID)
	return cipher
}

func storedAdm2SMTPPass(t *testing.T, handler ConfiguracoesHandler, workspaceID string) string {
	t.Helper()
	var pass sql.NullString
	if err := handler.DB.QueryRow(`SELECT smtp_pass FROM workspaces WHERE id = ?`, workspaceID).Scan(&pass); err != nil {
		t.Fatalf("read smtp_pass of %s: %v", workspaceID, err)
	}
	if !pass.Valid {
		return ""
	}
	return pass.String
}

func adm2AssertNotContains(t *testing.T, body, needle, context string) {
	t.Helper()
	if strings.Contains(body, needle) {
		t.Fatalf("%s: body não deve conter %q", context, needle)
	}
}

func adm2PostAuditoriaSave(handler ConfiguracoesHandler, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/auditoria/salvar", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	handler.HandleAdminAuditoriaSave(rr, req)
	return rr
}

// Casos 1, 2 (parcial) e 9: nenhuma renderização entrega a credencial armazenada.
func TestAdminAuditoriaRenderNeverDisclosesStoredSMTPPassword(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	cipher := seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	assertNoLeak := func(t *testing.T, body, context string) {
		t.Helper()
		adm2AssertNotContains(t, body, adm2StoredSecret, context)
		adm2AssertNotContains(t, body, cipher, context)
		adm2AssertNotContains(t, body, "sec:v1:", context)
	}

	t.Run("pagina completa", func(t *testing.T) {
		rr := httptest.NewRecorder()
		handler.HandleConfiguracoesSection(rr, httptest.NewRequest(http.MethodGet, "/admin/auditoria", nil), "admin-auditoria")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		assertNoLeak(t, body, "página completa")
		if !strings.Contains(body, "Credencial configurada") {
			t.Fatal("página completa deve exibir o estado 'Credencial configurada'")
		}
		if !strings.Contains(body, `name="smtp_pass" value=""`) {
			t.Fatal("campo smtp_pass deve ser renderizado vazio")
		}
		if !strings.Contains(body, `autocomplete="new-password"`) {
			t.Fatal("campo smtp_pass deve usar autocomplete=new-password")
		}
		if !strings.Contains(body, `name="smtp_pass_remove"`) {
			t.Fatal("com credencial configurada deve existir a ação explícita de remoção")
		}
	})

	t.Run("fragmento htmx", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/auditoria", nil)
		req.Header.Set("HX-Request", "true")
		rr := httptest.NewRecorder()
		handler.HandleConfiguracoesSection(rr, req, "admin-auditoria")
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		assertNoLeak(t, rr.Body.String(), "fragmento HTMX")
	})

	t.Run("caminho alternativo do hub", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/configuracoes?secao=admin-auditoria", nil)
		rr := httptest.NewRecorder()
		handler.HandleConfiguracoesConceito(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
		}
		assertNoLeak(t, rr.Body.String(), "hub ?secao=admin-auditoria")
	})
}

// Caso 2: sem credencial armazenada o estado exibido é "Sem credencial".
func TestAdminAuditoriaRenderWithoutCredentialShowsEmptyState(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, httptest.NewRequest(http.MethodGet, "/admin/auditoria", nil), "admin-auditoria")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Sem credencial") {
		t.Fatal("sem credencial armazenada o estado deve ser 'Sem credencial'")
	}
	adm2AssertNotContains(t, body, "Credencial configurada", "estado sem credencial")
	adm2AssertNotContains(t, body, `name="smtp_pass_remove"`, "sem credencial não deve oferecer remoção")
}

// Casos 3 e 11: campo vazio mantém a credencial byte a byte e permite alterar os demais campos.
func TestAdminAuditoriaSaveEmptyPasswordKeepsCredential(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	cipher := seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host":          {"smtp.manter.exemplo"},
		"smtp_port":          {"2525"},
		"smtp_user":          {"manter@exemplo.com"},
		"smtp_pass":          {""},
		"notification_email": {"soc@exemplo.com"},
		"email_preferences":  {"auth.failed"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := storedAdm2SMTPPass(t, handler, "config-workspace"); got != cipher {
		t.Fatal("senha vazia deve preservar o cipher original byte a byte")
	}
	var host string
	if err := handler.DB.QueryRow(`SELECT smtp_host FROM workspaces WHERE id = 'config-workspace'`).Scan(&host); err != nil || host != "smtp.manter.exemplo" {
		t.Fatalf("host deve ser atualizado independentemente da senha: %q err=%v", host, err)
	}
}

// Caso 4: campo ausente também mantém a credencial.
func TestAdminAuditoriaSaveAbsentPasswordFieldKeepsCredential(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	cipher := seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host": {"smtp.ausente.exemplo"},
		"smtp_port": {"587"},
		"smtp_user": {"ausente@exemplo.com"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := storedAdm2SMTPPass(t, handler, "config-workspace"); got != cipher {
		t.Fatal("campo ausente deve preservar o cipher original byte a byte")
	}
}

// Casos 5 e 7: nova senha substitui e a resposta não devolve o valor.
func TestAdminAuditoriaSaveNewPasswordReplacesWithoutEcho(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host": {"smtp.trocar.exemplo"},
		"smtp_port": {"587"},
		"smtp_user": {"trocar@exemplo.com"},
		"smtp_pass": {adm2ReplacementKey},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	adm2AssertNotContains(t, body, adm2ReplacementKey, "resposta de sucesso")
	adm2AssertNotContains(t, body, adm2StoredSecret, "resposta de sucesso")
	adm2AssertNotContains(t, body, "sec:v1:", "resposta de sucesso")

	stored := storedAdm2SMTPPass(t, handler, "config-workspace")
	if stored == "" || strings.Contains(stored, adm2ReplacementKey) {
		t.Fatal("senha nova deve ser armazenada cifrada")
	}
	decrypted, err := security.Decrypt(stored)
	if err != nil || decrypted != adm2ReplacementKey {
		t.Fatalf("cipher armazenado deve decriptar para a senha sintética enviada: %v", err)
	}
	if !strings.Contains(body, "substituída") {
		t.Fatal("resposta deve confirmar a substituição da credencial")
	}
}

// Caso 6: remoção somente pela ação explícita e confirmada.
func TestAdminAuditoriaSaveRemovesCredentialOnlyByExplicitAction(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host":        {"smtp.remover.exemplo"},
		"smtp_port":        {"587"},
		"smtp_user":        {"remover@exemplo.com"},
		"smtp_pass_remove": {"1"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := storedAdm2SMTPPass(t, handler, "config-workspace"); got != "" {
		t.Fatal("ação explícita de remoção deve limpar a credencial")
	}
	var host string
	if err := handler.DB.QueryRow(`SELECT smtp_host FROM workspaces WHERE id = 'config-workspace'`).Scan(&host); err != nil || host != "smtp.remover.exemplo" {
		t.Fatalf("remoção deve preservar a atualização dos demais campos: %q err=%v", host, err)
	}
	if !strings.Contains(rr.Body.String(), "removida") {
		t.Fatal("resposta deve confirmar a remoção da credencial")
	}
}

// Substituir e remover ao mesmo tempo é ambíguo e deve ser rejeitado sem tocar no banco.
func TestAdminAuditoriaSaveRejectsReplaceAndRemoveTogether(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	cipher := seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host":        {"smtp.ambos.exemplo"},
		"smtp_port":        {"587"},
		"smtp_user":        {"ambos@exemplo.com"},
		"smtp_pass":        {adm2ReplacementKey},
		"smtp_pass_remove": {"1"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "não ambos") {
		t.Fatal("substituição + remoção juntas devem gerar erro de validação")
	}
	if got := storedAdm2SMTPPass(t, handler, "config-workspace"); got != cipher {
		t.Fatal("validação ambígua não deve alterar a credencial")
	}
	adm2AssertNotContains(t, body, adm2ReplacementKey, "erro de validação")
	adm2AssertNotContains(t, body, adm2StoredSecret, "erro de validação")
}

// Caso 8: resposta de erro não revela a credencial armazenada nem ecoa a senha enviada.
func TestAdminAuditoriaSaveErrorResponseLeaksNothing(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host": {"smtp.erro.exemplo"},
		"smtp_port": {"99999"},
		"smtp_user": {"erro@exemplo.com"},
		"smtp_pass": {"sintetico-tentativa-0000"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Porta SMTP inválida.") {
		t.Fatal("porta inválida deve gerar erro de validação")
	}
	adm2AssertNotContains(t, body, adm2StoredSecret, "resposta de erro")
	adm2AssertNotContains(t, body, "sintetico-tentativa-0000", "resposta de erro")
	adm2AssertNotContains(t, body, "sec:v1:", "resposta de erro")
	if got := storedAdm2SMTPPass(t, handler, "config-workspace"); got == "" {
		t.Fatal("erro de validação não deve apagar a credencial")
	}
}

// Caso 12: a gravação é escopada ao workspace ativo.
func TestAdminAuditoriaSaveIsScopedToActiveWorkspace(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)
	secondaryCipher := seedAdm2SMTPCredential(t, handler, "config-secondary", "sintetico-outro-workspace-777")

	rr := adm2PostAuditoriaSave(handler, url.Values{
		"smtp_host": {"smtp.isolado.exemplo"},
		"smtp_port": {"587"},
		"smtp_user": {"isolado@exemplo.com"},
		"smtp_pass": {adm2ReplacementKey},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if got := storedAdm2SMTPPass(t, handler, "config-secondary"); got != secondaryCipher {
		t.Fatal("workspace distinto não pode ser alterado pelo salvamento da auditoria")
	}
	adm2AssertNotContains(t, rr.Body.String(), "sintetico-outro-workspace-777", "resposta")
}

func TestAdminAuditoriaSMTPMasterKeyFailureAndExplicitRecovery(t *testing.T) {
	const legacyCipher = "sec:v1:AAECAwQFBgcICQoL:NLiC-eMkHgguaU-W8QZcl_wHP1xR5QIBnrG6gu0oMJW2CpwD1A"
	for _, appEnv := range []string{"development", "production"} {
		for _, key := range []string{"", "invalid-synthetic-key"} {
			t.Run(appEnv+"/key="+key, func(t *testing.T) {
				t.Setenv("APP_ENV", appEnv)
				t.Setenv("SECURITY_MASTER_KEY", key)
				handler := newConfiguracoesRenderTestHandler(t)
				execTestSQL(t, handler.DB, `UPDATE workspaces SET smtp_pass = ? WHERE id = 'config-workspace'`, legacyCipher)
				kept := adm2PostAuditoriaSave(handler, url.Values{"smtp_host": {"smtp.synthetic.invalid"}})
				if kept.Code != http.StatusOK || storedAdm2SMTPPass(t, handler, "config-workspace") != legacyCipher {
					t.Fatal("editing other settings must preserve inaccessible ciphertext")
				}
				failed := adm2PostAuditoriaSave(handler, url.Values{"smtp_pass": {adm2ReplacementKey}})
				if !strings.Contains(failed.Body.String(), "Não foi possível proteger a senha SMTP.") || storedAdm2SMTPPass(t, handler, "config-workspace") != legacyCipher {
					t.Fatal("key failure must preserve the previous credential and report the failure")
				}
				adm2AssertNotContains(t, failed.Body.String(), adm2ReplacementKey, "key failure")
				adm2AssertNotContains(t, failed.Body.String(), legacyCipher, "key failure")
				t.Setenv("SECURITY_MASTER_KEY", "0123456789abcdef0123456789abcdef")
				recovered := adm2PostAuditoriaSave(handler, url.Values{"smtp_pass": {adm2ReplacementKey}})
				stored := storedAdm2SMTPPass(t, handler, "config-workspace")
				plain, err := security.Decrypt(stored)
				if recovered.Code != http.StatusOK || err != nil || plain != adm2ReplacementKey || stored == legacyCipher {
					t.Fatalf("explicit administrative replacement must restore SMTP: %v", err)
				}
				adm2AssertNotContains(t, recovered.Body.String(), adm2ReplacementKey, "recovery")
			})
		}
	}
}

func TestAdminAuditoriaSMTPExplicitRemovalWithoutMasterKey(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)
	seedAdm2SMTPCredential(t, handler, "config-workspace", adm2StoredSecret)
	t.Setenv("SECURITY_MASTER_KEY", "")
	rr := adm2PostAuditoriaSave(handler, url.Values{"smtp_pass_remove": {"1"}})
	if rr.Code != http.StatusOK || storedAdm2SMTPPass(t, handler, "config-workspace") != "" || !strings.Contains(rr.Body.String(), "removida") {
		t.Fatal("explicit administrative removal must remain available without the key")
	}
}

// Caso 10 (não autorizado recebe acesso negado) é garantido no nível de rota por
// cmd/server/global_permissions_rbac_test.go (POST /admin/auditoria/salvar:
// admin 204, manager/user 403). A revalidação interna do handler entra na ADM2
// junto com sua matriz de testes (configuracoes_admin_auditoria_rbac_test.go).
