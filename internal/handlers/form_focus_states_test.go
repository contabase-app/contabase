package handlers

import (
	"strings"
	"testing"
)

// Form focus is intentionally split by modality: pointer/touch keeps a quiet
// neutral border, while keyboard focus adds a visible outline. These tests pin
// the shared layer so a future restyle cannot bring back the 3px violet ring.
func TestPremiumFormFocusUsesNeutralBorderAndKeyboardOutline(t *testing.T) {
	tokens := readTestFile(t, "assets/css/ui-tokens.css")
	if !strings.Contains(tokens, "--ui-field-focus-border:") {
		t.Fatal("ui-tokens.css must define the semantic field focus border")
	}

	premium := readTestFile(t, "assets/css/premium.css")
	for _, expected := range []string{
		".ui-input:focus,",
		"border-color: var(--ui-field-focus-border);",
		"box-shadow: none;",
		`html[data-input-modality="keyboard"] .ui-input:focus,`,
		"outline: 2px solid var(--ui-focus);",
	} {
		if !strings.Contains(premium, expected) {
			t.Fatalf("premium form focus missing %q", expected)
		}
	}
	if strings.Contains(premium, "0 0 0 3px color-mix(in srgb, var(--ui-accent) 18%") {
		t.Fatal("premium fields must not keep the old violet focus ring")
	}

	shell := readTestFile(t, "assets/css/app-shell.css")
	for _, expected := range []string{
		".cb-v2 .cb-input:focus,",
		"border-color: var(--ui-field-focus-border);",
		`html[data-input-modality="keyboard"] .cb-v2 .cb-input:focus,`,
	} {
		if !strings.Contains(shell, expected) {
			t.Fatalf("shell field focus missing %q", expected)
		}
	}

	design := readTestFile(t, "assets/css/design-system.css")
	for _, expected := range []string{
		"--cb-field-focus-border:",
		".cb-input:focus {",
		"border-color: var(--cb-field-focus-border);",
	} {
		if !strings.Contains(design, expected) {
			t.Fatalf("design-system field focus missing %q", expected)
		}
	}
}

func TestSheetFormFocusUsesNeutralBorderAndKeyboardOutline(t *testing.T) {
	sheet := readTestFile(t, "assets/css/sheet-form.css")
	for _, expected := range []string{
		".sheet-amount-input-shell:focus-within",
		"border-color: var(--ui-field-focus-border);",
		`html[data-input-modality="keyboard"] .sheet-amount-input-shell:focus-within`,
		".sheet-lancamento-dialog .sheet-control:focus",
		".sheet-lancamento-dialog .sheet-select:focus",
		`html[data-input-modality="keyboard"] .sheet-lancamento-dialog .sheet-control:focus`,
		"box-shadow: none;",
	} {
		if !strings.Contains(sheet, expected) {
			t.Fatalf("sheet form focus missing %q", expected)
		}
	}
	if strings.Contains(sheet, "0 0 0 3px color-mix(in srgb, var(--ui-accent) 18%") {
		t.Fatal("sheet fields must not keep the old violet focus ring")
	}
}

func TestInputModalityIsLoadedByTheShell(t *testing.T) {
	shell := readTestFile(t, "templates/components/app_shell.html")
	if !strings.Contains(shell, `assetPath "/assets/js/input-modality.js"`) {
		t.Fatal("app shell must load the input modality helper")
	}

	script := readTestFile(t, "assets/js/input-modality.js")
	for _, expected := range []string{
		"__contabaseInputModalityReady",
		`setMode('pointer')`,
		"key === 'Tab'",
		"pointerdown",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("input-modality.js missing %q", expected)
		}
	}
}
