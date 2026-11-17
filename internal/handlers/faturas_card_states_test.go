package handlers

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

// Locks the status separation on the Faturas tile: paid, empty and missing
// invoice are distinct states, and the selection check follows the class the
// hub script updates instead of being rendered only for the server selection.
func TestFaturasCardStatesDistinguishPaidEmptyAndMissingInvoice(t *testing.T) {
	tpl := template.Must(template.New("card").Funcs(template.FuncMap{
		"assetPath": func(path string) string { return path },
	}).ParseFiles(resolveTemplatePath(t, "templates/components/credit_card.html")))
	now := time.Now()

	render := func(card CreditCardCard) string {
		t.Helper()
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "credit-card-tile", card); err != nil {
			t.Fatalf("render credit card tile: %v", err)
		}
		return out.String()
	}

	base := CreditCardCard{
		ID:             "card-1",
		Name:           "Cartão principal",
		DueDateUnix:    now.AddDate(0, 0, 5).Unix(),
		IsFaturasIndex: true,
		Href:           "/faturas?cartao=card-1",
	}

	paid := base
	paid.InvoiceID = "invoice-paid"
	paid.Status = "PAID"
	paid.Amount = 25000
	applyCardInvoiceState(&paid, invoiceFinancialSnapshot{Total: 25000, EffectivePaid: 25000}, now)

	empty := base
	empty.InvoiceID = "invoice-empty"
	empty.Status = "CLOSED"
	applyCardInvoiceState(&empty, invoiceFinancialSnapshot{Total: 0, EffectivePaid: 0, HasMovements: false}, now)
	empty.StatusLabel = "Sem movimentação"

	zeroWithMovements := base
	zeroWithMovements.InvoiceID = "invoice-zero"
	zeroWithMovements.Status = "CLOSED"
	applyCardInvoiceState(&zeroWithMovements, invoiceFinancialSnapshot{Total: 0, EffectivePaid: 0, HasMovements: true}, now)
	zeroWithMovements.StatusLabel = "Sem valor a pagar"

	missing := base
	missing.StatusLabel = "Sem fatura"

	paidHTML := render(paid)
	for _, contract := range []string{"is-settled", "is-paid", "cb-faturas-paid-check", ">PAGO</strong>"} {
		if !strings.Contains(paidHTML, contract) {
			t.Fatalf("paid card missing %q: %s", contract, paidHTML)
		}
	}
	if strings.Contains(paidHTML, "is-empty") {
		t.Fatal("paid card must not carry the empty state")
	}

	for name, card := range map[string]CreditCardCard{"empty": empty, "zero-with-movements": zeroWithMovements, "missing": missing} {
		html := render(card)
		for _, forbidden := range []string{"is-settled", "is-paid", "cb-faturas-paid-check", ">PAGO</strong>"} {
			if strings.Contains(html, forbidden) {
				t.Fatalf("%s card must not claim %q: %s", name, forbidden, html)
			}
		}
		if !strings.Contains(html, "is-empty") {
			t.Fatalf("%s card must use the neutral empty state: %s", name, html)
		}
		if !strings.Contains(html, "cb-v2-credit-card-dot") {
			t.Fatalf("%s card must keep the discrete status dot: %s", name, html)
		}
	}
	if !strings.Contains(render(empty), "Sem movimentação") {
		t.Fatal("an untouched invoice must state the absence of movements")
	}
	if !strings.Contains(render(zeroWithMovements), "Sem valor a pagar") {
		t.Fatal("a zero total with movements must not claim an untouched invoice")
	}
	if !strings.Contains(render(missing), "Sem fatura") {
		t.Fatal("missing invoice must keep its absence label")
	}

	selected := base
	selected.IsSelected = true
	selectedHTML := render(selected)
	unselectedHTML := render(base)
	for name, html := range map[string]string{"selected": selectedHTML, "unselected": unselectedHTML} {
		if !strings.Contains(html, "cb-v2-credit-card-selected") {
			t.Fatalf("%s card must carry the selection indicator for CSS to toggle: %s", name, html)
		}
	}
	if !strings.Contains(selectedHTML, "is-selected") || !strings.Contains(selectedHTML, `aria-current="page"`) {
		t.Fatal("selected card must expose its selected class and current page state")
	}
	if strings.Contains(unselectedHTML, "is-selected") || strings.Contains(unselectedHTML, `aria-current="page"`) {
		t.Fatal("unselected card must not expose selected state")
	}
}

func TestFaturasPaidBadgeUsesSuccessTokenWithoutDecoration(t *testing.T) {
	css := readTestFile(t, "assets/css/faturas-index.css")

	paidRule := cssRule(t, css, ".cb-v2-faturas-index-main .cb-v2-credit-card.is-settled .cb-v2-credit-card-status.is-paid {")
	for _, contract := range []string{"color: var(--ui-success);", "border-color: color-mix(in srgb, var(--ui-success) 55%, var(--card-badge-border));"} {
		if !strings.Contains(paidRule, contract) {
			t.Fatalf("paid badge rule missing %q: %s", contract, paidRule)
		}
	}
	for _, forbidden := range []string{"gradient", "drop-shadow", "text-shadow", "box-shadow"} {
		if strings.Contains(paidRule, forbidden) {
			t.Fatalf("paid badge must not use %q", forbidden)
		}
	}

	emptyRule := cssRule(t, css, ".cb-v2-faturas-index-main .cb-v2-credit-card .cb-v2-credit-card-status.is-empty {")
	for _, contract := range []string{
		"border-color: color-mix(in srgb, var(--card-badge-border) 45%, transparent);",
		"background: transparent;",
		"color: var(--card-fg-muted);",
	} {
		if !strings.Contains(emptyRule, contract) {
			t.Fatalf("neutral badge must stay discreet with %q: %s", contract, emptyRule)
		}
	}

	if !strings.Contains(css, `[data-theme="light"] .cb-v2-faturas-index-main .cb-v2-credit-card.is-settled {`) {
		t.Fatal("settled surface must keep an explicit light-theme override")
	}
}

func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	start := strings.Index(css, selector)
	if start < 0 {
		t.Fatalf("CSS rule not found: %s", selector)
	}
	rest := css[start+len(selector):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("unterminated CSS rule: %s", selector)
	}
	return rest[:end]
}
