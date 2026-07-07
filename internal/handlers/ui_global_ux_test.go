package handlers

import (
	"os"
	"strings"
	"testing"
)

func TestRelatoriosExportDropdownStaysInViewport(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/relatorios.html"))
	if err != nil {
		t.Fatalf("read relatorios.html: %v", err)
	}
	body := string(raw)

	// Extrato PDF/CSV dropdowns keep both export options and the viewport-safe menu class.
	for _, want := range []string{
		`reports-export-menu`,
		`/relatorios/export.pdf?tipo=extrato-movimentos`,
		`/relatorios/export.pdf?tipo=extrato-diario`,
		`/relatorios/export.csv?tipo=extrato-movimentos`,
		`/relatorios/export.csv?tipo=extrato-diario`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("relatorios export dropdown missing %q", want)
		}
	}

	// Contábil CSV dropdown reuses the viewport-safe menu class instead of a bare panel.
	if strings.Contains(body, `cb-meta-menu-panel hidden right-0`) {
		t.Fatal("contabil CSV dropdown should use reports-export-menu for viewport clamping")
	}

	css, err := os.ReadFile(resolveTemplatePath(t, "assets/css/relatorios.css"))
	if err != nil {
		t.Fatalf("read relatorios.css: %v", err)
	}
	cssBody := string(css)
	if !strings.Contains(cssBody, "max-width: min(16rem, calc(100vw - 2rem))") {
		t.Fatal("reports-export-menu should clamp its width to the viewport")
	}
	if !strings.Contains(cssBody, "left: 0;") {
		t.Fatal("reports-export-menu should anchor left on small screens to avoid overflow")
	}
}

func TestMonthSelectorResponsiveContract(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/components/seletor_meses.html"))
	if err != nil {
		t.Fatalf("read seletor_meses.html: %v", err)
	}
	body := string(raw)

	if !strings.Contains(body, `id="monthRail"`) {
		t.Fatal("month selector must keep monthRail")
	}
	if !strings.Contains(body, `data-month-active="true"`) {
		t.Fatal("month selector must keep active-month marker")
	}
	// Light-theme selected month must have a visible fill (violet-100, not the old faint violet-50).
	if !strings.Contains(body, "bg-violet-100") {
		t.Fatal("selected month should use a visible light-theme background (bg-violet-100)")
	}
	// First visible month aligns to the content grid via a negative rail offset.
	if !strings.Contains(body, "-ml-1.5") {
		t.Fatal("month rail should offset the first month to align with content")
	}
	// Mobile keeps the compact scrollable rail (min-w-max); desktop distributes the
	// 12 months across the full width so the controls do not look disconnected.
	if !strings.Contains(body, "min-w-max") {
		t.Fatal("month rail should stay compact/scrollable on mobile (min-w-max)")
	}
	for _, want := range []string{"md:min-w-0", "md:w-full", "md:justify-between"} {
		if !strings.Contains(body, want) {
			t.Fatalf("month rail should distribute months on desktop, missing %q", want)
		}
	}
}

func TestProfileDropdownMobileTouchTargets(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/layout.html"))
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		"Painel de Controle",
		"Central de Ajuda",
		"Sair da Conta",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("profile dropdown missing link %q", want)
		}
	}
	// Mobile widens the panel and enlarges option hit areas.
	if !strings.Contains(body, "w-[min(17rem,calc(100vw-1.5rem))]") {
		t.Fatal("profile dropdown should widen on mobile without overflowing the viewport")
	}
	if !strings.Contains(body, "min-h-[44px] sm:min-h-0") {
		t.Fatal("profile dropdown options should use a comfortable mobile touch target")
	}
}

func TestDashboardMobileSectionSpacing(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/dashboard.css"))
	if err != nil {
		t.Fatalf("read dashboard.css: %v", err)
	}
	body := string(raw)

	idx := strings.Index(body, "@media (max-width: 620px)")
	if idx < 0 {
		t.Fatal("dashboard.css missing mobile media query")
	}
	mobile := body[idx:]
	if !strings.Contains(mobile, ".cb-dashboard-stack {\n        gap: 1.75rem;") {
		t.Fatal("mobile dashboard should widen the vertical gap between stacked sections")
	}
	if !strings.Contains(mobile, ".cb-dashboard-pair-grid {\n        gap: 1.125rem;") {
		t.Fatal("mobile Limites/Metas pair should keep the tighter block gap")
	}
}
