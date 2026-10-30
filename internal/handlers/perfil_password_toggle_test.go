package handlers

import (
	"strings"
	"testing"
)

// The profile toggle is a page-specific module, loaded with assetPath inside
// the settings payload so it also binds after HTMX swaps. These assertions
// guard the wiring that was missing before this phase.
func TestPerfilPasswordToggleIsWired(t *testing.T) {
	template := readTestFile(t, "templates/pages/configuracoes_perfil.html")
	for _, expected := range []string{
		`id="togglePasswordForm"`,
		`aria-expanded="false"`,
		`aria-controls="passwordFormArea"`,
		`id="passwordFormArea"`,
		`id="passwordChangeForm"`,
		`hx-post="/configuracoes/perfil/senha"`,
		`hx-swap="outerHTML"`,
		`assetPath "/assets/js/perfil-ui.js"`,
	} {
		if !strings.Contains(template, expected) {
			t.Fatalf("perfil template missing password contract %q", expected)
		}
	}

	script := readTestFile(t, "assets/js/perfil-ui.js")
	for _, expected := range []string{
		"__contabasePerfilUIInitialized",
		"syncPasswordToggle",
		"aria-expanded",
		"htmx:afterSwap",
		"toggleBtn.focus()",
		"form.reset()",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("perfil-ui.js missing toggle behavior %q", expected)
		}
	}
}
