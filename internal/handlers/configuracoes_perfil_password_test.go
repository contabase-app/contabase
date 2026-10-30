package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CFG3 — A área de senha usa hx-swap=outerHTML e um único container
// #passwordFormArea. A resposta do handler (erro ou sucesso) devolve exatamente
// um container, substituindo o anterior em vez de aninhar outro.
func TestConfiguracoesPerfilPasswordAreaKeepsSingleID(t *testing.T) {
	handler := newConfiguracoesRenderTestHandler(t)

	for _, tc := range []struct {
		name   string
		errMsg string
		okMsg  string
		role   string
		marker string
	}{
		{name: "error", errMsg: "Senha atual incorreta.", role: "alert", marker: `data-password-feedback="error"`},
		{name: "success", okMsg: "Senha alterada com sucesso.", role: "status", marker: `data-password-feedback="success"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.renderPasswordChangeArea(rr, tc.errMsg, tc.okMsg)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			body := rr.Body.String()
			if count := strings.Count(body, `id="passwordFormArea"`); count != 1 {
				t.Fatalf("passwordFormArea rendered %d times, want 1", count)
			}
			for _, want := range []string{
				`hx-post="/configuracoes/perfil/senha"`,
				`hx-target="#passwordFormArea"`,
				`hx-swap="outerHTML"`,
				`role="` + tc.role + `"`,
				`tabindex="-1"`,
				tc.marker,
			} {
				assertContains(t, body, want)
			}
		})
	}

	fullReq := httptest.NewRequest(http.MethodGet, "/configuracoes/perfil", nil)
	fullRR := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(fullRR, fullReq, "perfil")
	if count := strings.Count(fullRR.Body.String(), `id="passwordFormArea"`); count != 1 {
		t.Fatalf("full page passwordFormArea rendered %d times, want 1", count)
	}
	assertContains(t, fullRR.Body.String(), `hx-swap="outerHTML"`)
}
