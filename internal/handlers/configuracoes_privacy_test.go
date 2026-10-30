package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// CFG2 — Privacidade de Configurações.
//
// A regra reusa exclusivamente o contrato global (privacy.js +
// sessionStorage.contabasePrivacy + body.privacy-enabled + data-sensitive) e
// mascara apenas a apresentação de valores exibidos, nunca campos editáveis.

func TestConfiguracoesPrivacyMasksFinancialValuesOnlyInPresentation(t *testing.T) {
	css := readTestFile(t, "assets/css/configuracoes.css")
	for _, want := range []string{
		`.privacy-enabled .cb-v2-configuracoes-main [data-sensitive]:not(input):not(textarea):not(select)`,
		`visibility: hidden`,
		`content: "••••"`,
		`visibility: visible`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("configuracoes.css privacy rule lost %q", want)
		}
	}

	handler := newConfiguracoesRenderTestHandler(t)
	now := time.Now().Unix()
	execTestSQL(t, handler.DB, `
		INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, created_at, updated_at)
		VALUES
			('cfg2-account', 'config-workspace', 'Conta CFG2', 'CHECKING', '#EC7000', '', 'custom', ?, ?),
			('cfg2-card', 'config-workspace', 'Cartão CFG2', 'CREDIT_CARD', '#820AD1', '', 'custom', ?, ?)
	`, now, now, now, now)
	execTestSQL(t, handler.DB, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES ('cfg2-cc', 'cfg2-card', 25, 5, 500000)
	`)

	contas := renderSectionPartial(t, handler, "contas")
	if count := strings.Count(contas, `data-privacy-toggle`); count != 1 {
		t.Fatalf("contas privacy toggles = %d, want 1", count)
	}
	for _, want := range []string{
		`data-privacy-icon`,
		`aria-label="Ocultar valores financeiros"`,
		`data-sensitive`,
	} {
		assertContains(t, contas, want)
	}
	assertNoSensitiveEditableInputs(t, contas)

	cartoes := renderSectionPartial(t, handler, "cartoes")
	if count := strings.Count(cartoes, `data-privacy-toggle`); count != 1 {
		t.Fatalf("cartoes privacy toggles = %d, want 1", count)
	}
	assertContains(t, cartoes, `data-sensitive>R$ 5.000,00<`)
	assertNoSensitiveEditableInputs(t, cartoes)

	// O swap HTMX devolve o mesmo payload, então a máscara e o toggle
	// continuam válidos após criar/editar/arquivar.
	assertContains(t, contas, `id="settings-dynamic-payload"`)
	assertContains(t, cartoes, `id="settings-dynamic-payload"`)
}

func renderSectionPartial(t *testing.T, handler ConfiguracoesHandler, section string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/configuracoes/"+section, nil)
	req.Header.Set("HX-Request", "true")
	rr := httptest.NewRecorder()
	handler.HandleConfiguracoesSection(rr, req, section)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s partial status = %d, want %d", section, rr.Code, http.StatusOK)
	}
	return rr.Body.String()
}

func assertNoSensitiveEditableInputs(t *testing.T, body string) {
	t.Helper()
	for _, fragment := range strings.Split(body, "<input")[1:] {
		tag := fragment
		if end := strings.Index(tag, ">"); end >= 0 {
			tag = tag[:end]
		}
		if strings.Contains(tag, "data-sensitive") {
			t.Fatalf("editable input must never be masked: %q", tag)
		}
	}
}
