package handlers

import (
	"strings"
	"testing"
)

func TestConfiguracoesPerfilPremiumFormContract(t *testing.T) {
	template := readTestFile(t, "templates/pages/configuracoes_perfil.html")

	for _, want := range []string{
		`ui-panel`,
		`ui-label`,
		`ui-input`,
		`ui-btn ui-btn--primary`,
		`ui-btn ui-btn--secondary`,
		`ui-page-header`,
		`id="profileName"`,
		`for="profileName"`,
		`autocomplete="name"`,
		`id="profileEmail"`,
		`for="profileEmail"`,
		`autocomplete="email"`,
		`required`,
		`for="currentPassword"`,
		`autocomplete="current-password"`,
		`id="newPassword"`,
		`autocomplete="new-password"`,
		`minlength="8"`,
		`aria-controls="passwordFormArea"`,
		`aria-expanded="false"`,
		`hx-target="#passwordFormArea"`,
		`hx-swap="outerHTML"`,
		`hx-disabled-elt="find button[type='submit']"`,
		`hx-sync="this:drop"`,
		`hx-indicator="#htmx-indicator"`,
		`data-password-feedback`,
	} {
		if !strings.Contains(template, want) {
			t.Fatalf("perfil template missing premium/accessibility contract %q", want)
		}
	}

	if strings.Contains(template, "cb-input") || strings.Contains(template, "cb-button") {
		t.Fatal("perfil must use canonical ui-input/ui-btn instead of legacy cb-* form controls")
	}

	script := readTestFile(t, "assets/js/perfil-ui.js")
	for _, want := range []string{
		`#passwordFormArea [data-password-feedback]`,
		`focusPasswordFeedback`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("perfil-ui.js missing feedback focus contract %q", want)
		}
	}
}

func TestConfiguracoesPerfilPhotoContract(t *testing.T) {
	template := readTestFile(t, "templates/pages/configuracoes_perfil.html")

	for _, want := range []string{
		`name="photo_file"`,
		`accept="image/jpeg,image/png,image/webp"`,
		`class="sr-only"`,
		`aria-describedby="profilePhotoHelp"`,
		`id="profilePhotoHelp"`,
		`A foto é aplicada ao salvar o perfil.`,
	} {
		if !strings.Contains(template, want) {
			t.Fatalf("perfil photo flow missing %q", want)
		}
	}
	if strings.Contains(template, `data-onchange="submitProfilePhoto"`) {
		t.Fatal("perfil must not reference the orphan submitProfilePhoto handler")
	}
}
