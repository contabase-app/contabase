package handlers

import (
	"strings"
	"testing"
)

// Locks the shared credit-card foundation extraction (B2.1): one source for
// institutional surface/contrast/status/progress/logo, no duplicated luminance
// logic, and no page-level rule hiding cards.
func TestCreditCardFoundationIsSharedAndScoped(t *testing.T) {
	creditCards := readTestFile(t, "assets/css/credit-cards.css")
	dashboardCSS := readTestFile(t, "assets/css/dashboard.css")
	dashboardWallet := readTestFile(t, "assets/css/dashboard-wallet.css")
	faturasIndex := readTestFile(t, "assets/css/faturas-index.css")
	dashboardTpl := readTestFile(t, "templates/pages/dashboard.html")
	hubTpl := readTestFile(t, "templates/pages/faturas_index.html")
	creditCardsJS := readTestFile(t, "assets/js/credit-cards.js")
	dashboardJS := readTestFile(t, "assets/js/dashboard.js")

	// Foundation owns the shared pieces.
	for _, contract := range []string{
		".cb-card-face",
		`[data-card-scheme="light"]`,
		"--card-fg:", "--card-track:", "--card-fill:", "--card-shadow:",
		"oklch(from color-mix", // institutional tonal surface
		".cb-v2-credit-card-status", ".cb-v2-credit-card-dot",
		".cb-card-progress", ".cb-card-logo",
	} {
		if !strings.Contains(creditCards, contract) {
			t.Fatalf("credit-cards.css must own shared foundation %q", contract)
		}
	}

	// Dashboard consumes the foundation without redefining it.
	if !strings.Contains(dashboardTpl, "cb-card-face") || !strings.Contains(dashboardTpl, "cb-card-progress") {
		t.Fatal("dashboard cards must consume the shared face/progress hooks")
	}
	for _, duplicated := range []string{"--card-fg:", "--card-track:", "--card-fill:", "oklch(from color-mix"} {
		if strings.Contains(dashboardWallet, duplicated) {
			t.Fatalf("dashboard-wallet.css must not redefine shared foundation %q", duplicated)
		}
	}

	// The hub no longer gets card appearance from dashboard.css.
	if strings.Contains(dashboardCSS, ".cb-v2-credit-card {") || strings.Contains(dashboardCSS, ".cb-v2-card-rail {") {
		t.Fatal("dashboard.css must not define the Faturas card tile/rail anymore")
	}
	if !strings.Contains(faturasIndex, ".cb-v2-credit-card") || !strings.Contains(faturasIndex, ".cb-v2-card-rail") {
		t.Fatal("faturas-index.css must own the hub tile/rail composition")
	}

	// Load order: foundations after the shell tokens and before the consumer.
	if !strings.Contains(dashboardTpl, `/assets/css/credit-cards.css`) ||
		!strings.Contains(hubTpl, `/assets/css/credit-cards.css`) {
		t.Fatal("both card consumers must load credit-cards.css")
	}
	if strings.Index(dashboardTpl, "dashboard.css") > strings.Index(dashboardTpl, "credit-cards.css") ||
		strings.Index(dashboardTpl, "credit-cards.css") > strings.Index(dashboardTpl, "dashboard-wallet.css") {
		t.Fatal("dashboard must load credit-cards.css between dashboard.css and dashboard-wallet.css")
	}
	if strings.Index(hubTpl, "premium.css") > strings.Index(hubTpl, "credit-cards.css") ||
		strings.Index(hubTpl, "credit-cards.css") > strings.Index(hubTpl, "faturas.css") {
		t.Fatal("hub must load credit-cards.css after premium.css and before the page stylesheets")
	}

	// One luminance implementation only.
	if !strings.Contains(creditCardsJS, "schemeForColor") || !strings.Contains(creditCardsJS, "data-card-scheme") {
		t.Fatal("credit-cards.js must own the adaptive scheme calculation")
	}
	if strings.Contains(dashboardJS, "data-card-scheme") || strings.Contains(dashboardJS, "Math.pow((channel + 0.055)") {
		t.Fatal("dashboard.js must not duplicate the luminance calculation")
	}
	if !strings.Contains(dashboardTpl, `/assets/js/credit-cards.js`) ||
		strings.Index(dashboardTpl, "credit-cards.js") > strings.Index(dashboardTpl, "dashboard.js") {
		t.Fatal("credit-cards.js must load before dashboard.js")
	}

	// No consumer may hide cards again.
	for name, css := range map[string]string{"dashboard.css": dashboardCSS, "faturas-index.css": faturasIndex, "credit-cards.css": creditCards} {
		if strings.Contains(css, ":nth-child(n+7)") && strings.Contains(css, "[data-v2-card-item]") {
			t.Fatalf("%s must not cap the number of visible credit cards", name)
		}
	}
}
