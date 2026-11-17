package handlers

import (
	"strings"
	"testing"
)

// D4-C2 final adjustment: the Dashboard and /faturas render the same credit
// card tile. Geometry lives in credit-cards.css, not in a page-scoped copy, so
// the two pages cannot drift again.
func TestDashboardAndFaturasShareCreditCardTile(t *testing.T) {
	dashboard := readTestFile(t, "templates/components/dashboard_preview_cards.html")
	sharedTile := readTestFile(t, "templates/components/credit_card.html")
	sharedCSS := readTestFile(t, "assets/css/credit-cards.css")
	faturasPage := readTestFile(t, "templates/pages/faturas_index.html")

	if !strings.Contains(dashboard, `{{template "credit-card-tile" $card}}`) {
		t.Fatal("Dashboard cards must consume the canonical credit-card-tile")
	}
	if strings.Contains(dashboard, "cbp-cc-panel") {
		t.Fatal("Dashboard must not keep the old inner-panel composition")
	}
	if !strings.Contains(faturasPage, `{{template "credit-card-tile" .}}`) {
		t.Fatal("/faturas must keep consuming the canonical credit-card-tile")
	}
	for _, contract := range []string{
		".cb-v2-credit-card {",
		"border-radius: 16px",
		".cb-v2-credit-card-amount",
		".cb-v2-credit-card-limit",
		".cb-v2-credit-card-progress-row",
		".cb-v2-credit-card-pending",
	} {
		if !strings.Contains(sharedCSS, contract) {
			t.Fatalf("shared credit-card composition missing %q", contract)
		}
	}
	if !strings.Contains(sharedTile, `data-wallet-card="{{.WalletIndex}}"`) {
		t.Fatal("shared tile must expose the dashboard carousel index")
	}
	if !strings.Contains(dashboard, "cb-v2-credit-card cbp-credit-card cbp-credit-card--add") {
		t.Fatal("add-card placeholder must consume the shared card geometry")
	}
}
