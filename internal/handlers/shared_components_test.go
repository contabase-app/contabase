package handlers

import (
	"strings"
	"testing"
)

// R1.3-D4-B: the legacy cb-* bridge must render the same component language as
// the Premium ui-* layer. These assertions pin the shared contract without
// forcing every page to migrate markup in this phase.
func TestSharedLegacyComponentsConsumeCanonicalTokens(t *testing.T) {
	design := readTestFile(t, "assets/css/design-system.css")

	for _, want := range []string{
		".cb-button {",
		"min-height: var(--ui-control-height-sm, 2.125rem);",
		"padding: 0 12px;",
		"font-size: var(--ui-fs-action, 0.75rem);",
		"text-transform: none;",
		"min-height: var(--ui-control-height, 2.5rem);",
		"font-size: var(--ui-fs-md, 0.875rem);",
		"border-color: var(--ui-field-focus-border, var(--cb-field-focus-border));",
		`html[data-input-modality="keyboard"] .cb-input:focus {`,
		`.cb-label {`,
		"font-size: var(--ui-fs-label, 0.625rem);",
		"color-mix(in srgb, var(--ui-danger, var(--cb-danger)) 34%, var(--ui-border, var(--cb-border-subtle)))",
		".cb-status-chip-success",
		".cb-status-chip-info",
		".cb-card-dense",
		"border-radius: var(--ui-radius-panel, 1rem);",
		"box-shadow: var(--ui-shadow-panel, none);",
		".cb-empty-state",
		"border-radius: var(--ui-radius-panel, 12px);",
	} {
		if !strings.Contains(design, want) {
			t.Fatalf("design-system.css lost shared component contract %q", want)
		}
	}

	labelDefinitions := 0
	for _, line := range strings.Split(design, "\n") {
		if strings.TrimSpace(line) == ".cb-label {" {
			labelDefinitions++
		}
	}
	if labelDefinitions != 1 {
		t.Fatalf("design-system.css must define .cb-label once, got %d", labelDefinitions)
	}
	if strings.Contains(design, ".dark .cb-status-chip-") {
		t.Fatal("status chips must use theme-aware --ui-* tokens, not dark-only overrides")
	}
	for _, legacy := range []string{
		".cb-status-chip-success { background: color-mix(in srgb, var(--cb-success) 12%, transparent)",
		".cb-button {\n  display: inline-flex;\n  align-items: center;\n  justify-content: center;\n  gap: 0.375rem;\n  font-size: 0.75rem;\n  font-weight: 700;\n  text-transform: uppercase;",
	} {
		if strings.Contains(design, legacy) {
			t.Fatalf("design-system.css kept the divergent legacy contract %q", legacy)
		}
	}
}

// The global confirmation and delete-scope dialogs are shared overlays. Their
// actions must use the same button and semantic color contract as the pages.
func TestGlobalConfirmModalUsesSharedButtonTokens(t *testing.T) {
	js := readTestFile(t, "assets/js/modals-global.js")
	for _, want := range []string{
		`confirmBtn.className = "cb-button cb-button-primary`,
		`confirmBtn.className = "cb-button cb-button-danger`,
		`bg-[var(--ui-accent-soft)] text-[var(--ui-accent-text)]`,
		`bg-[var(--ui-danger-soft)] text-[var(--ui-danger)]`,
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("global confirm modal lost shared button contract %q", want)
		}
	}
	for _, forbidden := range []string{"bg-indigo-600", "bg-rose-600", "dark:bg-indigo-500"} {
		if strings.Contains(js, forbidden) {
			t.Fatalf("global confirm modal kept the raw utility color %q", forbidden)
		}
	}
	shell := readTestFile(t, "templates/components/app_shell.html")
	if !strings.Contains(shell, "cb-button cb-button-danger") {
		t.Fatal("global delete-scope confirm must use the shared danger button")
	}
	if strings.Contains(shell, `bg-[var(--cb-danger)] px-4 text-xs font-bold text-white`) {
		t.Fatal("global delete-scope confirm must not fill danger with white text")
	}
}

// The Relatórios macro helpers used to emit text-emerald-600, which fails AA
// on the light table surface. Positive values now consume the semantic token.
func TestRelatoriosPositiveValuesUseSemanticSuccessToken(t *testing.T) {
	server := readTestFile(t, "cmd/server/main.go")
	if strings.Contains(server, "text-emerald-600 dark:text-emerald-300") {
		t.Fatal("relatorios positive classes must not use raw emerald Tailwind colors")
	}
	if got := strings.Count(server, "text-[var(--ui-success)]"); got < 4 {
		t.Fatalf("relatorios positive classes must use --ui-success in both macro helpers, got %d", got)
	}
}

// Success text must use the semantic token on the remaining consumers that
// previously hardcoded emerald-600/700 (AA failure on light surfaces).
func TestTemplatesUseSemanticSuccessTextToken(t *testing.T) {
	for _, path := range []string{
		"templates/pages/lancamentos.html",
		"templates/pages/configuracoes_components.html",
		"templates/pages/configuracoes_perfil.html",
	} {
		body := readTestFile(t, path)
		for _, forbidden := range []string{"text-emerald-600", "text-emerald-700"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s must not use raw success text %q", path, forbidden)
			}
		}
	}
}
