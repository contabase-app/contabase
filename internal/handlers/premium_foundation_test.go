package handlers

import (
	"strings"
	"testing"
)

func TestPremiumFoundationIsScopedAndContasPreservesContracts(t *testing.T) {
	premium := readTestFile(t, "assets/css/premium.css")
	uiTokens := readTestFile(t, "assets/css/ui-tokens.css")
	shell := readTestFile(t, "templates/components/app_shell.html")
	contas := readTestFile(t, "templates/pages/contas.html")
	dashboard := readTestFile(t, "templates/pages/dashboard.html")

	if !strings.Contains(premium, `[data-ui="premium"]`) {
		t.Fatal("premium foundation must be scoped by data-ui=premium")
	}
	if strings.Contains(premium, "\n:root {") {
		t.Fatal("premium foundation must not replace global tokens")
	}
	if !strings.Contains(uiTokens, "--ui-surface: var(--v2-surface);") ||
		!strings.Contains(uiTokens, "--cb-surface-1: var(--ui-surface);") {
		t.Fatal("canonical tokens must alias shell tokens and bridge legacy --cb-* in ui-tokens.css")
	}
	if strings.Contains(premium, "--ui-surface: var(--v2-surface);") {
		t.Fatal("premium must consume canonical tokens instead of duplicating them")
	}
	if !strings.Contains(uiTokens, "--ui-action-bg:") {
		t.Fatal("canonical foundation needs an AA-contrast action token separate from the decorative accent")
	}
	if !strings.Contains(shell, `/assets/css/ui-tokens.css`) {
		t.Fatal("authenticated shell must load the canonical ui-tokens.css")
	}
	if strings.Index(shell, `ui-tokens.css`) < strings.Index(shell, `app-shell.css`) {
		t.Fatal("ui-tokens.css must load after app-shell primitives and before page stylesheets")
	}

	if !strings.Contains(contas, `data-ui="premium"`) ||
		!strings.Contains(contas, `/assets/css/premium.css`) ||
		!strings.Contains(contas, `/assets/css/contas.css`) {
		t.Fatal("Contas must load and activate the premium foundation")
	}
	if strings.Contains(contas, "dashboard-wallet.css") {
		t.Fatal("Contas must not import dashboard-only CSS")
	}
	if strings.Contains(dashboard, "premium.css") {
		t.Fatal("Dashboard must remain on its existing stylesheet")
	}
	if strings.Contains(contas, "cb-tab-active") {
		t.Fatal("Contas should use the premium tab state, not the legacy tab class")
	}
	for _, contract := range []string{
		`id="contas-body"`,
		`id="contas-list"`,
		`data-payment-confirm`,
		`data-swipe-row="true"`,
		`data-swipe-right-payment-confirm="true"`,
		`hx-get="/contas?aba=pagar&partial=lista"`,
		`hx-vals='{"partial":"lista"}'`,
	} {
		if !strings.Contains(contas, contract) {
			t.Fatalf("Contas lost functional contract %q", contract)
		}
	}
}
