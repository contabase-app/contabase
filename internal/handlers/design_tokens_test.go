package handlers

import (
	"strings"
	"testing"
)

// R1.3-D2-B: ui-tokens.css is the single source. The Dashboard consumes
// --ui-* directly; --v2-* remains a value-free alias only for pages not yet
// migrated, and --cbp-* must not exist.
func TestDesignTokensSingleSourceAndDashboardContext(t *testing.T) {
	ui := readTestFile(t, "assets/css/ui-tokens.css")
	preview := readTestFile(t, "assets/css/dashboard-preview.css")
	shell := readTestFile(t, "assets/css/app-shell.css")

	for _, want := range []string{
		"--ui-accent: #8b5cf6;",
		"--ui-accent-text: #a78bfa;",
		"--v2-canvas: var(--ui-canvas);",
		"--cb-accent: var(--ui-accent);",
		".cb-preview.cb-v2 {",
	} {
		if !strings.Contains(ui, want) {
			t.Fatalf("ui-tokens.css lost canonical contract %q", want)
		}
	}

	if strings.Contains(ui, "--cbp-") {
		t.Fatal("ui-tokens.css must not keep the retired --cbp-* compatibility layer")
	}
	if strings.Contains(preview, "--cbp-") {
		t.Fatal("dashboard-preview.css must consume --ui-* directly")
	}
	if strings.Contains(shell, "--v2-") {
		t.Fatal("app-shell.css must consume --ui-* directly")
	}
}
