package handlers

import (
	"regexp"
	"strings"
	"testing"
)

// The invoice date headings are part of the scrolling list. They must not stick
// to the viewport while the user scrolls the invoice.
func TestInvoiceDayHeadingDoesNotStick(t *testing.T) {
	css := readTestFile(t, "assets/css/faturas.css")
	rule := regexp.MustCompile(`(?s)\.cb-invoice-day\s*\{[^}]*\}`).FindString(css)
	if rule == "" {
		t.Fatal("cb-invoice-day rule not found")
	}
	if regexp.MustCompile(`position\s*:\s*sticky`).MatchString(rule) {
		t.Fatalf("invoice day heading must scroll with the list, got %q", rule)
	}
	if !strings.Contains(rule, "display: flex") {
		t.Fatalf("invoice day heading lost its layout, got %q", rule)
	}
}

// Reservas and Limites share the metas overview. Their monetary values must
// start at the same left edge as the labels on every container width.
func TestMetasOverviewValuesAlignLeft(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	rules := regexp.MustCompile(`(?s)\.metas-overview__[^{}]*\.ui-kpi__value[^{}]*\{[^}]*\}`).FindAllString(css, -1)
	if len(rules) == 0 {
		t.Fatal("metas overview value rules not found")
	}
	for _, rule := range rules {
		if regexp.MustCompile(`text-align\s*:\s*right`).MatchString(rule) {
			t.Fatalf("metas overview value must not align right, got %q", rule)
		}
		if regexp.MustCompile(`justify-self\s*:\s*end`).MatchString(rule) {
			t.Fatalf("metas overview value must not be pushed to the end, got %q", rule)
		}
	}
	base := regexp.MustCompile(`(?s)\.metas-overview__metric \.ui-kpi__value\s*\{[^}]*\}`).FindString(css)
	if !strings.Contains(base, "text-align: left") || !strings.Contains(base, "justify-self: start") {
		t.Fatalf("metas overview base value must align left, got %q", base)
	}
}

// The reserve/limit creation and edit forms reuse .metas-form__money for their
// monetary inputs. Typed values and the R$ 0,00 placeholder must start left.
func TestMetasMoneyFormInputsAlignLeft(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	rule := regexp.MustCompile(`(?s)\.metas-form__money\s*\{[^}]*\}`).FindString(css)
	if rule == "" {
		t.Fatal("metas-form__money rule not found")
	}
	if regexp.MustCompile(`text-align\s*:\s*right`).MatchString(rule) {
		t.Fatalf("metas money form inputs must not align right, got %q", rule)
	}
	if !regexp.MustCompile(`text-align\s*:\s*left`).MatchString(rule) {
		t.Fatalf("metas money form inputs must align left, got %q", rule)
	}
	form := readTestFile(t, "templates/components/form_meta.html")
	if count := strings.Count(form, "metas-form__money"); count != 3 {
		t.Fatalf("expected the three reserve/limit money inputs, found %d", count)
	}
}
