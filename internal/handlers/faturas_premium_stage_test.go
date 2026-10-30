package handlers

import (
	"strings"
	"testing"
)

// Locks the Stage A + B1 contracts for Faturas: Premium foundation scoped to
// the page, canonical page header and competence rail, preserved HTMX/deep-link
// contracts and no regression to the Dashboard.
func TestFaturasPremiumFoundationStageIsScopedAndPreservesContracts(t *testing.T) {
	hub := readTestFile(t, "templates/pages/faturas_index.html")
	detail := readTestFile(t, "templates/pages/faturas.html")
	faturasCSS := readTestFile(t, "assets/css/faturas.css")
	indexCSS := readTestFile(t, "assets/css/faturas-index.css")
	hubJS := readTestFile(t, "assets/js/faturas-index.js")

	for name, tpl := range map[string]string{"hub": hub, "detail": detail} {
		if !strings.Contains(tpl, `data-ui="premium"`) {
			t.Fatalf("%s must activate the scoped Premium foundation", name)
		}
		if !strings.Contains(tpl, `/assets/css/premium.css`) {
			t.Fatalf("%s must load premium.css through assetPath", name)
		}
	}

	if strings.Index(hub, "premium.css") > strings.Index(hub, "dashboard.css") {
		t.Fatal("premium.css must load before the page stylesheets in the hub")
	}
	if strings.Index(detail, "premium.css") > strings.Index(detail, "faturas.css") {
		t.Fatal("premium.css must load before faturas.css in the detail")
	}

	// Page stylesheets consume the canonical bridge; they never redefine it.
	for _, token := range []string{
		"--cb-bg:", "--cb-bg-soft:", "--cb-surface-1:", "--cb-surface-2:",
		"--cb-surface-3:", "--cb-surface-overlay:", "--cb-border-subtle:",
		"--cb-border-strong:", "--cb-border-focus:", "--cb-text-main:",
		"--cb-text-muted:", "--cb-text-subtle:", "--cb-accent:", "--cb-accent-solid:",
		"--cb-accent-hover:", "--cb-accent-soft:", "--cb-success:", "--cb-danger:",
		"--cb-warning:", "--cb-info:",
	} {
		if strings.Contains(faturasCSS, token) {
			t.Fatalf("faturas.css must not redefine canonical token %q", token)
		}
	}

	// Header uses the real Premium components.
	for _, contract := range []string{
		"ui-page-header", "ui-eyebrow", "ui-page-title", "ui-page-description",
		"ui-btn ui-btn--secondary ui-btn--sm",
	} {
		if !strings.Contains(hub, contract) {
			t.Fatalf("hub header must adopt %q", contract)
		}
		if !strings.Contains(detail, contract) {
			t.Fatalf("detail header must adopt %q", contract)
		}
	}
	if strings.Contains(hub+detail, "cb-v2-faturas-back") {
		t.Fatal("legacy header action class must not remain after the Premium header migration")
	}
	if strings.Contains(faturasCSS, "cb-v2-faturas-back") {
		t.Fatal("legacy header action styles must be removed from faturas.css")
	}

	// Canonical competence rail, with the original URLs and active semantics.
	for _, contract := range []string{
		`data-month-selector`, `data-month-rail-viewport`, `data-month-rail`,
		`class="month-btn"`, `data-month-active="true"`, `data-month-current="true"`,
		`aria-current="date"`, `class="month-control"`, `{{.PreviousURL}}`, `{{.NextURL}}`,
	} {
		if !strings.Contains(hub, contract) {
			t.Fatalf("hub must use the canonical month selector contract %q", contract)
		}
	}
	if strings.Contains(indexCSS, ".cb-faturas-index-month") ||
		strings.Contains(indexCSS, ".cb-faturas-index-period-button") {
		t.Fatal("faturas-index.css must not keep the parallel month selector implementation")
	}

	// The hub JS must follow the updated DOM hooks (coordinated change).
	if !strings.Contains(hubJS, "[data-month-selector] a") {
		t.Fatal("faturas-index.js must keep the selected card in every month link")
	}
	if strings.Contains(hubJS, ".cb-faturas-index-month.is-active") {
		t.Fatal("faturas-index.js must not depend on the removed parallel month class")
	}
	monthJS := readTestFile(t, "assets/js/month-selector.js")
	for _, contract := range []string{"[data-month-rail-viewport]", `[data-month-active="true"]`, "getBoundingClientRect"} {
		if !strings.Contains(monthJS, contract) {
			t.Fatalf("month-selector.js must own the canonical rail centering contract %q", contract)
		}
	}

	// Functional contracts that the redesign must never break.
	for _, contract := range []string{
		`id="faturas-index-card-rail"`,
		`hx-get="{{.RefreshCardsURL}}"`,
		`hx-trigger="refreshFinancials from:body"`,
		`hx-select="#faturas-index-card-rail"`,
		`hx-swap="outerHTML"`,
		`id="faturas-index-detail"`,
		`data-faturas-card-prev`,
		`data-faturas-card-next`,
		`data-faturas-carousel-dots`,
	} {
		if !strings.Contains(hub, contract) {
			t.Fatalf("hub lost functional contract %q", contract)
		}
	}
	cardTile := readTestFile(t, "templates/components/credit_card.html")
	for _, contract := range []string{`data-faturas-card`, `data-faturas-card-id`, `data-faturas-content-url`} {
		if !strings.Contains(cardTile, contract) {
			t.Fatalf("credit card tile lost functional contract %q", contract)
		}
	}
	for _, contract := range []string{
		`id="faturas-content"`,
		`data-invoice-settle-modal`,
		`data-invoice-payment-modal`,
		`data-pending-cents`,
		`data-swipe-row="true"`,
		`hx-post="/cartoes/faturas/pagar"`,
	} {
		if !strings.Contains(detail, contract) {
			t.Fatalf("detail lost financial UI contract %q", contract)
		}
	}
}

// The swipe hint is only text: it must describe the gesture that swipe.js
// actually executes. Leftward drag runs the right-side action (move invoice)
// and rightward drag runs the left-side action (delete).
func TestFaturasSwipeHintMatchesGestureMapping(t *testing.T) {
	detail := readTestFile(t, "templates/pages/faturas.html")
	swipeJS := readTestFile(t, "assets/js/swipe.js")

	if !strings.Contains(swipeJS, "if (diff < -100)") || !strings.Contains(swipeJS, "runSwipeAction(row, 'right')") {
		t.Fatal("swipe.js mapping changed: leftward drag must run the right-side action")
	}
	if !strings.Contains(swipeJS, "} else if (diff > 100)") || !strings.Contains(swipeJS, "runSwipeAction(row, 'left')") {
		t.Fatal("swipe.js mapping changed: rightward drag must run the left-side action")
	}
	if !strings.Contains(detail, `data-swipe-left-method="DELETE"`) {
		t.Fatal("left-side swipe action must remain the destructive delete")
	}
	if !strings.Contains(detail, `data-swipe-right-url="/transacoes/{{.ID}}/mover-fatura"`) {
		t.Fatal("right-side swipe action must remain the invoice move")
	}
	if !strings.Contains(detail, "para a Esquerda (&larr;) para mover") {
		t.Fatal("swipe hint must state that the leftward drag moves the charge")
	}
	if !strings.Contains(detail, "para a Direita (&rarr;) para apagar") {
		t.Fatal("swipe hint must state that the rightward drag deletes the record")
	}
}

// The hub rail must render every existing card: the legacy 6-card cap belonged
// to the previous Dashboard rail and is not applied anymore.
func TestFaturasCardRailDoesNotHideCardsFromDashboardCSS(t *testing.T) {
	dashboardCSS := readTestFile(t, "assets/css/dashboard.css")
	hub := readTestFile(t, "templates/pages/faturas_index.html")

	if strings.Contains(dashboardCSS, `.cb-v2-card-rail > [data-v2-card-item]:nth-child(n+7)`) {
		t.Fatal("dashboard.css must not hide cards from every consumer of .cb-v2-card-rail")
	}
	if !strings.Contains(hub, `data-faturas-card-rail`) {
		t.Fatal("hub must keep its own rail hook for the carousel JS")
	}
}
