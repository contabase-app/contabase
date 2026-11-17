package handlers

import (
	"strings"
	"testing"
)

// Locks the shared credit-card foundation extraction (B2.1): one source for
// institutional surface/contrast/status/progress/logo, no duplicated luminance
// logic, and no page-level rule hiding cards. The V2 dashboard and the Faturas
// hub are the remaining consumers.
func TestCreditCardFoundationIsSharedAndScoped(t *testing.T) {
	creditCards := readTestFile(t, "assets/css/credit-cards.css")
	faturasIndex := readTestFile(t, "assets/css/faturas-index.css")
	dashboardPage := readTestFile(t, "templates/pages/dashboard_preview.html")
	dashboardCards := readTestFile(t, "templates/components/dashboard_preview_cards.html")
	creditCardTile := readTestFile(t, "templates/components/credit_card.html")
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

	// The V2 dashboard consumes the shared tile without redefining the foundation.
	if !strings.Contains(creditCardTile, "cb-card-face") || !strings.Contains(creditCardTile, "cb-card-progress") {
		t.Fatal("shared credit-card tile must consume the face/progress hooks")
	}
	if !strings.Contains(dashboardCards, `{{template "credit-card-tile" $card}}`) {
		t.Fatal("dashboard cards must consume the shared credit-card tile")
	}
	if !strings.Contains(faturasIndex, ".cb-v2-credit-card") || !strings.Contains(faturasIndex, ".cb-v2-card-rail") {
		t.Fatal("faturas-index.css must own the hub tile/rail composition")
	}

	// Load order: foundations after the shell tokens and before the consumer.
	if !strings.Contains(dashboardPage, `/assets/css/credit-cards.css`) ||
		!strings.Contains(hubTpl, `/assets/css/credit-cards.css`) {
		t.Fatal("both card consumers must load credit-cards.css")
	}
	if strings.Index(dashboardPage, "credit-cards.css") > strings.Index(dashboardPage, "dashboard-preview.css") {
		t.Fatal("dashboard must load credit-cards.css before dashboard-preview.css")
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
	if !strings.Contains(dashboardPage, `/assets/js/credit-cards.js`) ||
		strings.Index(dashboardPage, "credit-cards.js") > strings.Index(dashboardPage, "dashboard.js") {
		t.Fatal("credit-cards.js must load before dashboard.js")
	}

	// No consumer may hide cards again.
	for name, css := range map[string]string{"faturas-index.css": faturasIndex, "credit-cards.css": creditCards} {
		if strings.Contains(css, ":nth-child(n+7)") && strings.Contains(css, "[data-v2-card-item]") {
			t.Fatalf("%s must not cap the number of visible credit cards", name)
		}
	}
}
