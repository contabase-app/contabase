package handlers

import (
	"strings"
	"testing"
)

// Locks the modal layout fix: header and action footer stay outside the
// scrollable body, and the shared overlay manager locks the document scroll.
func TestInvoiceModalsKeepActionsOutsideScrollableBody(t *testing.T) {
	tpl := readTestFile(t, "templates/pages/faturas.html")

	settleStart := strings.Index(tpl, `id="invoice-settle-payment-modal"`)
	partialStart := strings.Index(tpl, `id="invoice-partial-payment-modal"`)
	if settleStart < 0 || partialStart < 0 {
		t.Fatal("payment modals not found")
	}
	settle := tpl[settleStart:partialStart]
	partial := tpl[partialStart:]

	for name, block := range map[string]string{"settle": settle, "partial": partial} {
		bodyIdx := strings.Index(block, `class="cb-invoice-modal-body"`)
		summaryIdx := strings.Index(block, `class="cb-invoice-modal-summary"`)
		actionsIdx := strings.Index(block, `class="cb-invoice-modal-actions"`)
		if bodyIdx < 0 || summaryIdx < 0 || actionsIdx < 0 {
			t.Fatalf("%s modal must keep body, summary and actions", name)
		}
		if !(bodyIdx < summaryIdx && summaryIdx < actionsIdx) {
			t.Fatalf("%s modal must render summary inside the scroll body and actions after it", name)
		}
		if strings.Contains(block[bodyIdx:actionsIdx], `type="submit"`) {
			t.Fatalf("%s modal submit must stay outside the scrollable body", name)
		}
	}

	css := readTestFile(t, "assets/css/faturas.css")
	for _, contract := range []string{
		".cb-invoice-overlay:not(.hidden) {",
		"display: grid;",
		"place-items: center;",
		"height: 100dvh;",
		".cb-invoice-modal {",
		"display: flex;",
		"overflow: hidden;",
		"padding: 0;",
		".cb-invoice-modal-body {",
		"overflow-y: auto;",
		".cb-invoice-modal-actions,",
		"flex: 0 0 auto;",
		"justify-content: flex-end;",
		"min-height: var(--ui-control-height);",
		".cb-invoice-sheet-actions {",
		".cb-invoice-refund-dialog {",
	} {
		if !strings.Contains(css, contract) {
			t.Fatalf("modal layout fix missing %q", contract)
		}
	}

	// The general refund sheet keeps its actions outside the scroll area and
	// uses the same footer contract as the payment dialogs.
	refund := readTestFile(t, "templates/components/form_reembolso_fatura.html")
	scrollIdx := strings.Index(refund, `class="cb-invoice-sheet-body min-h-0 flex-1 overflow-y-auto"`)
	actionsIdx := strings.Index(refund, `class="cb-invoice-sheet-actions"`)
	cancelIdx := strings.Index(refund, `ui-btn ui-btn--ghost cb-invoice-modal-btn`)
	submitIdx := strings.Index(refund, `ui-btn ui-btn--primary cb-invoice-modal-btn`)
	if scrollIdx < 0 || actionsIdx < 0 || cancelIdx < 0 || submitIdx < 0 {
		t.Fatal("refund sheet must keep body, footer, cancel and submit")
	}
	if !(scrollIdx < actionsIdx && actionsIdx < cancelIdx && cancelIdx < submitIdx) {
		t.Fatal("refund sheet actions must live in a fixed footer outside the scrollable content")
	}
	if strings.Contains(refund, "backdrop-blur-sm") || !strings.Contains(refund, "bg-black/60") {
		t.Fatal("refund backdrop must match the shared modal darkening without extra blur")
	}

	// Shared overlay manager locks the page behind dialogs.
	overlay := readTestFile(t, "assets/js/overlay-stack.js")
	for _, contract := range []string{"_syncOverlayScrollLock", "cb-overlay-open"} {
		if !strings.Contains(overlay, contract) {
			t.Fatalf("overlay manager missing scroll lock contract %q", contract)
		}
	}
	shell := readTestFile(t, "assets/css/app-shell.css")
	if !strings.Contains(shell, "html.cb-overlay-open") || !strings.Contains(shell, "overflow: hidden;") {
		t.Fatal("app shell must prevent background scroll while an overlay is open")
	}
}

// Locks the Faturas selection refinement: a tonal border plus an explicit,
// screen-reader-hidden check; keyboard focus remains a separate outline so a
// clicked card never keeps a decorative accent ring.
func TestFaturasSelectedCardUsesTonalStateAndSemanticFocus(t *testing.T) {
	card := readTestFile(t, "templates/components/credit_card.html")
	for _, contract := range []string{
		`{{if .IsSelected}} is-selected{{end}}`,
		`aria-current="page"`,
		`cb-v2-credit-card-selected`,
		`aria-hidden="true"`,
		`{{.StatusLabel}}`,
	} {
		if !strings.Contains(card, contract) {
			t.Fatalf("selected card contract missing %q", contract)
		}
	}
	if !strings.Contains(card, `{{if and .IsSelected .IsFaturasIndex}}`) {
		t.Fatal("selection check must stay scoped to the Faturas hub")
	}

	css := readTestFile(t, "assets/css/faturas-index.css")
	if strings.Contains(css, "0 0 0 2px var(--ui-accent)") {
		t.Fatal("selected card must not use the permanent accent ring")
	}
	for _, contract := range []string{
		".cb-v2-faturas-index-main .cb-v2-credit-card.is-selected {",
		"border-color: color-mix(in srgb, var(--card-fg) 38%, var(--card-line));",
		"box-shadow: var(--card-shadow),",
		".cb-v2-credit-card-selected {",
		".cb-v2-faturas-index-main .cb-v2-credit-card:focus-visible {",
		"outline: 2px solid var(--ui-focus);",
		"outline-offset: 2px;",
	} {
		if !strings.Contains(css, contract) {
			t.Fatalf("selected card styling missing %q", contract)
		}
	}
}
