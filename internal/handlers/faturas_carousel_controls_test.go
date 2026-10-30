package handlers

import (
	"strings"
	"testing"
)

// Locks the Faturas carousel controls: paired circular buttons in the section
// header, real disabled states, and navigation scoped to the rail (never to
// card selection).
func TestFaturasCarouselControlsStayInSectionHeader(t *testing.T) {
	hub := readTestFile(t, "templates/pages/faturas_index.html")
	css := readTestFile(t, "assets/css/faturas-index.css")

	for _, contract := range []string{
		`class="cb-v2-carousel-controls" role="group" aria-label="Navegação do carrossel de cartões"`,
		`type="button" class="cb-v2-carousel-control" data-faturas-card-prev aria-label="Cartão anterior" aria-controls="faturas-index-card-track" disabled`,
		`type="button" class="cb-v2-carousel-control" data-faturas-card-next aria-label="Próximo cartão" aria-controls="faturas-index-card-track" disabled`,
		`id="faturas-index-card-track" class="cb-v2-card-rail" data-faturas-card-rail role="region" aria-label="Cartões de crédito"`,
	} {
		if !strings.Contains(hub, contract) {
			t.Fatalf("Faturas carousel contract missing %q", contract)
		}
	}

	heading := strings.Index(hub, `class="cb-v2-section-heading"`)
	controls := strings.Index(hub, `class="cb-v2-carousel-controls"`)
	rail := strings.Index(hub, `id="faturas-index-card-track"`)
	if !(heading < controls && controls < rail) {
		t.Fatal("carousel controls must render inside the section header before the rail")
	}

	for _, contract := range []string{
		".cb-v2-carousel-controls {",
		"display: flex;",
		"flex: 0 0 auto;",
		".cb-v2-carousel-control {",
		"width: 38px;",
		"height: 38px;",
		"border: 1px solid var(--ui-border);",
		"border-radius: 50%;",
		"background: var(--ui-surface-raised);",
		".cb-v2-carousel-control:hover:not(:disabled) {",
		".cb-v2-carousel-control:focus-visible {",
		"outline: 2px solid var(--ui-focus);",
		".cb-v2-carousel-control:disabled {",
		"opacity: .35;",
	} {
		if !strings.Contains(css, contract) {
			t.Fatalf("Faturas carousel styling missing %q", contract)
		}
	}

	// Mobile keeps a 44px touch target without showing a third copy of the
	// controls or moving them below the rail.
	if !strings.Contains(css, ".cb-v2-carousel-control { width: 44px; height: 44px; }") {
		t.Fatal("mobile carousel controls must keep a 44px touch target")
	}
}
