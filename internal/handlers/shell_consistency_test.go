package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellOwnsNavigationPresentation guards the architectural rule established
// in the design-system consolidation: the authenticated shell is the only place
// that defines sidebar, appbar, bottom navigation and FAB geometry/state.
// Page stylesheets may compose their own content but must not restyle the shell.
func TestShellOwnsNavigationPresentation(t *testing.T) {
	shell := readTestFile(t, "assets/css/app-shell.css")
	tokens := readTestFile(t, "assets/css/ui-tokens.css")

	for _, want := range []string{
		"--ui-nav-width: 248px;",
		"--ui-nav-item-height: 40px;",
		"--ui-nav-item-radius: 10px;",
		"--ui-nav-active-bg: var(--v2-accent-soft);",
		"--ui-nav-active-fg: var(--v2-accent-on-soft);",
		"--ui-nav-active-indicator-width: 2px;",
		"--ui-icon-button-size-sm: 34px;",
	} {
		if !strings.Contains(tokens, want) {
			t.Fatalf("ui-tokens.css lost canonical navigation token %q", want)
		}
	}

	if !strings.Contains(shell, ".cb-v2-nav-link.is-active {") ||
		!strings.Contains(shell, "background: var(--ui-nav-active-bg);") ||
		!strings.Contains(shell, "box-shadow: inset var(--ui-nav-active-indicator-width) 0 0 var(--ui-nav-active-indicator);") {
		t.Fatal("app-shell.css must define the single canonical selected navigation state")
	}

	// The FAB is a shell component: it disappears on desktop for every page,
	// never because a specific page is mounted.
	if strings.Contains(shell, ":has([data-dashboard])") {
		t.Fatal("shell must not branch on a page marker; the FAB rule is global")
	}
	if !strings.Contains(shell, "@media (min-width: 1024px) {\n    .cb-v2-fab { display: none; }\n}") {
		t.Fatal("the FAB must be hidden on desktop by a single global shell rule")
	}

	// One bottom navigation for every authenticated route.
	if !strings.Contains(shell, "border-radius: 20px;") ||
		!strings.Contains(shell, "box-shadow: var(--ui-shadow-floating);") ||
		!strings.Contains(shell, ".cb-v2-bottom-nav a.is-active { background: var(--ui-nav-active-bg);") {
		t.Fatal("bottom navigation must use the canonical floating geometry and selected state")
	}
}

func TestPageStylesheetsDoNotRestyleTheShell(t *testing.T) {
	shellSelectors := []string{
		".cb-v2-sidebar",
		".cb-v2-nav-link",
		".cb-v2-nav-sub-link",
		".cb-v2-nav-group-toggle",
		".cb-v2-nav-separator",
		".cb-v2-quick-action",
		".cb-v2-workspace",
		".cb-v2-sidebar-toggle",
		".cb-v2-icon-button",
		".cb-v2-appbar",
		".cb-v2-bottom-nav",
		".cb-v2-fab",
		".cb-v2-main",
		".cb-v2-footer",
	}

	entries, err := os.ReadDir(resolveTemplatePath(t, "assets/css"))
	if err != nil {
		t.Fatalf("read assets/css: %v", err)
	}
	shared := map[string]bool{
		"app-shell.css":     true,
		"ui-tokens.css":     true,
		"design-system.css": true, // legacy shell bridge, pre-existing
		"style.css":         true, // compiled Tailwind utilities
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".css") || shared[name] {
			continue
		}
		body := readTestFile(t, filepath.Join("assets/css", name))
		for _, selector := range shellSelectors {
			for _, line := range strings.Split(body, "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.HasPrefix(trimmed, selector) {
					continue
				}
				// A page may reference the shell as an ancestor (to scope its own
				// content) as long as it does not declare shell properties.
				if !strings.Contains(trimmed, "{") {
					continue
				}
				t.Errorf("%s redefines shell selector %q: %s", name, selector, trimmed)
			}
		}
	}
}

func TestShellDoesNotResetButtonTypography(t *testing.T) {
	shell := readTestFile(t, "assets/css/app-shell.css")
	for _, forbidden := range []string{
		".cb-v2 button { border: 0; font: inherit; }",
		".cb-v2 button { font: inherit; }",
		".cb-v2 a { font: inherit; }",
	} {
		if strings.Contains(shell, forbidden) {
			t.Fatalf("shell reset %q has higher specificity than component classes and silently overrides their typography", forbidden)
		}
	}
}

func TestSharedComponentsUseCanonicalTokens(t *testing.T) {
	premium := readTestFile(t, "assets/css/premium.css")
	design := readTestFile(t, "assets/css/design-system.css")
	metas := readTestFile(t, "assets/css/metas.css")

	if strings.Contains(premium, "--ui-surface: var(--v2-surface);") {
		t.Fatal("premium.css must consume canonical tokens, not redefine them")
	}
	for _, legacy := range []string{"var(--ui-radius-lg)", "var(--ui-radius-md)", "var(--ui-radius-sm)"} {
		if strings.Contains(premium, legacy) {
			t.Fatalf("premium.css still depends on compatibility alias %s", legacy)
		}
	}

	// .cb-tabs is the canonical tab implementation for pages without premium.css.
	if !strings.Contains(design, "border-radius: var(--ui-radius-tab, 12px);") ||
		!strings.Contains(design, "border-radius: var(--ui-radius-tab-item, 8px);") ||
		!strings.Contains(design, "background-color: var(--ui-tab-active-bg, var(--cb-accent-soft));") {
		t.Fatal("design-system.css .cb-tabs must implement the canonical tab contract")
	}
	if strings.Contains(design, "background-color: var(--cb-accent-solid);\n  color: var(--cb-accent-text);\n  box-shadow: 0 1px 2px 0 rgba(0, 0, 0, 0.05);\n  border-color: transparent;") {
		t.Fatal("legacy solid-violet tab state must not come back")
	}

	// Metas consumes the canonical tabs instead of reimplementing them.
	for _, duplicate := range []string{
		".cb-v2-metas-panels .cb-tab {",
		".cb-v2-metas-panels .cb-tab-active {",
		".cb-v2-metas-panels .cb-tabs {\n  width: min(100%, 360px);\n  margin-bottom: 18px;\n  padding: 4px;",
	} {
		if strings.Contains(metas, duplicate) {
			t.Fatalf("metas.css reimplements the shared tab component: %q", duplicate)
		}
	}
}
