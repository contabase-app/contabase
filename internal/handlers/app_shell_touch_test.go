package handlers

import (
	"strings"
	"testing"
)

// M8 — o App Shell mantém a geometria desktop e ganha alvos de 44px no
// appbar mobile/tablet. A correção é escopada ao shell touch; sidebar,
// navegação inferior e appbar desktop permanecem com seus tamanhos.
func TestAppShellAppbarTouchTargetsAreScopedToTouchShell(t *testing.T) {
	css := readTestFile(t, "assets/css/app-shell.css")
	start := strings.Index(css, "@media (max-width: 1023px)")
	if start < 0 {
		t.Fatal("app-shell.css missing the touch shell media query")
	}
	next := strings.Index(css[start+1:], "@media")
	var section string
	if next < 0 {
		section = css[start:]
	} else {
		section = css[start : start+1+next]
	}

	for _, expected := range []string{
		`.cb-v2-appbar .cb-v2-icon-button,`,
		`.cb-v2-appbar .cb-v2-menu-button {`,
		`width: 44px;`,
		`height: 44px;`,
		`min-width: 44px;`,
		`min-height: 44px;`,
	} {
		if !strings.Contains(section, expected) {
			t.Fatalf("touch appbar must contain %q", expected)
		}
	}
	if strings.Contains(section, "!important") {
		t.Fatal("appbar touch targets must not use !important")
	}

	// O token desktop canônico continua 40px; a sidebar usa os tokens próprios.
	tokens := readTestFile(t, "assets/css/ui-tokens.css")
	if !strings.Contains(tokens, "--ui-icon-button-size: 40px;") {
		t.Fatal("desktop appbar token must remain 40px")
	}
}
