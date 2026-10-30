package handlers

import (
	"os"
	"strings"
	"testing"
)

// These contracts are deliberately static: they guard the cascade and
// presentation decisions that caused real regressions during the redesign.
func TestDashboardStabilizationCSSCascadeAndDisclosure(t *testing.T) {
	walletCSS := readTestFile(t, "assets/css/dashboard-wallet.css")
	dashboardCSS := readTestFile(t, "assets/css/dashboard.css")
	dashboardTpl := readTestFile(t, "templates/pages/dashboard.html")
	shellTpl := readTestFile(t, "templates/components/app_shell.html")
	chartJS := readTestFile(t, "assets/js/balance-evolution.js")

	if strings.Contains(walletCSS, ".wallet .cb-v2-progress-item { min-width: 0; --meter-tone: var(--v2-tone-neutral); }") {
		t.Fatal("wallet must not set the neutral meter tone with higher specificity than dashboard.css")
	}
	for _, tone := range []string{"ok", "attention", "near", "exceeded", "danger", "info", "neutral"} {
		if !strings.Contains(walletCSS, ".wallet .cb-v2-progress-item.cb-v2-meter--"+tone) {
			t.Fatalf("missing explicit wallet tone guard for %s", tone)
		}
	}
	if !strings.Contains(dashboardCSS, ".cb-v2-progress-item {") ||
		!strings.Contains(dashboardCSS, "--meter-tone: var(--v2-tone-neutral);") {
		t.Fatal("dashboard.css must retain the canonical neutral meter default")
	}
	if !strings.Contains(walletCSS, ".wallet-category-variation.is-down { color: var(--v2-subtle); }") {
		t.Fatal("spending reductions must stay neutral instead of automatically green")
	}
	if !strings.Contains(dashboardTpl, "Por competência, incluindo lançamentos pendentes.") {
		t.Fatal("categories must disclose the competence criterion and pending transactions")
	}
	if !strings.Contains(dashboardTpl, `data-balance-composition`) ||
		!strings.Contains(chartJS, "account_archived_in_period") ||
		!strings.Contains(chartJS, "account_lifecycle") {
		t.Fatal("balance chart must expose the account-composition indication from existing limitations")
	}
	if !strings.Contains(shellTpl, `assetPath "/assets/css/app-shell.css"`) {
		t.Fatal("app-shell.css must use assetPath for cache busting")
	}
	if !strings.Contains(dashboardTpl, `assetPath "/assets/css/dashboard.css"`) {
		t.Fatal("dashboard.css must use assetPath for cache busting")
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(resolveTemplatePath(t, path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
