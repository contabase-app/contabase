package handlers

import (
	"strings"
	"testing"
)

// Locks the B2.2 Faturas tonal tile: institutional face consumed from the
// shared foundation while selection, navigation and deep-link contracts stay
// independent from the Dashboard card.
func TestFaturasTonalCardConsumesSharedFoundation(t *testing.T) {
	tile := readTestFile(t, "templates/components/credit_card.html")
	faturasIndex := readTestFile(t, "assets/css/faturas-index.css")
	hubTpl := readTestFile(t, "templates/pages/faturas_index.html")

	for _, contract := range []string{
		`cb-card-face`,
		`cb-card-logo`,
		`cb-card-progress`,
		`cb-v2-credit-card-name`,
		`cb-v2-credit-card-amount`,
		`cb-v2-credit-card-meta`,
		`cb-v2-credit-card-limit`,
		`data-card-visual="physical"`,
		`data-cb-color="{{.Color}}"`,
	} {
		if !strings.Contains(tile, contract) {
			t.Fatalf("Faturas tile must consume shared foundation %q", contract)
		}
	}
	if strings.Contains(tile, "cb-v2-provider-mark") {
		t.Fatal("Faturas tile must use the shared card logo, not the legacy provider mark")
	}

	// Selection/navigation contracts are functional and must never be dropped.
	for _, contract := range []string{
		`data-faturas-card`,
		`data-faturas-card-id="{{.ID}}"`,
		`data-faturas-content-url="{{.ContentURL}}"`,
		`aria-current="page"`,
		`is-selected`,
		`href="{{if .Href}}{{.Href}}{{else}}/cartoes/faturas/{{.InvoiceID}}{{end}}"`,
	} {
		if !strings.Contains(tile, contract) {
			t.Fatalf("Faturas tile lost selection/navigation contract %q", contract)
		}
	}

	// Status semantics remain integrated to the face.
	for _, contract := range []string{`is-open`, `is-closed`, `is-paid`, `is-overdue`} {
		if !strings.Contains(tile, contract) {
			t.Fatalf("Faturas tile lost status semantic %q", contract)
		}
	}

	// Tonal composition and independent selection/focus states.
	for _, contract := range []string{
		`--card-fg`, `--card-fg-muted`, `--card-line`,
		`border-radius: 16px`,
	} {
		if !strings.Contains(faturasIndex, contract) {
			t.Fatalf("Faturas tonal tile must use shared surface token %q", contract)
		}
	}
	if strings.Contains(faturasIndex, "grid-template-columns: 28px") {
		t.Fatal("Faturas tile must not keep the legacy grid composition")
	}
	if !strings.Contains(faturasIndex, "is-selected") ||
		!strings.Contains(faturasIndex, "cb-v2-credit-card-selected") ||
		!strings.Contains(faturasIndex, "border-color: color-mix(in srgb, var(--card-fg)") {
		t.Fatal("selected card must use a tonal border and an explicit check")
	}
	if strings.Contains(faturasIndex, "0 0 0 2px var(--ui-accent)") {
		t.Fatal("selected card must not keep the permanent violet ring")
	}
	if !strings.Contains(faturasIndex, ":focus-visible") || !strings.Contains(faturasIndex, "var(--ui-focus)") {
		t.Fatal("keyboard focus must stay visually separate from the selected state")
	}
	if strings.Contains(faturasIndex, ".cb-v2-progress") {
		t.Fatal("Faturas tile must use the shared .cb-card-progress instead of the removed local progress")
	}

	if !strings.Contains(hubTpl, `/assets/js/credit-cards.js`) ||
		strings.Index(hubTpl, "credit-cards.js") > strings.Index(hubTpl, "faturas-index.js") {
		t.Fatal("hub must load the shared scheme script before the page script")
	}
}
