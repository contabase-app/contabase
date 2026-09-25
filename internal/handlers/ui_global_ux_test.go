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
	for _, want := range []string{`data-month-selector`, `data-month-rail-viewport`, `data-month-rail`, `data-month-current="true"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("month selector must expose the shared component marker %q", want)
		}
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

	css, err := os.ReadFile(resolveTemplatePath(t, "assets/css/design-system.css"))
	if err != nil {
		t.Fatalf("read design-system.css: %v", err)
	}
	for _, want := range []string{"[data-month-selector]", ".month-btn[data-month-active=\"true\"]", ".month-control", "min-height: 2.75rem"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("shared month selector styles missing %q", want)
		}
	}
	for _, scope := range []string{"cb-v2-transactions-main", "cb-v2-contas-main", "cb-v2-relatorios-main", "cb-v2-faturas-main"} {
		if !strings.Contains(string(css), scope) {
			t.Fatalf("continuous month selector must include %q", scope)
		}
	}
	for _, want := range []string{"gap: 10px !important", "padding: 12px", "border-radius: 16px", "width: 36px", "gap: 7px !important", "padding: 9px", "width: 34px", "width: 50px"} {
		if !strings.Contains(string(css), want) {
			t.Fatalf("continuous selector must match the Faturas visual token %q", want)
		}
	}

	js, err := os.ReadFile(resolveTemplatePath(t, "assets/js/month-selector.js"))
	if err != nil {
		t.Fatalf("read month-selector.js: %v", err)
	}
	for _, want := range []string{"revealActiveMonth", "htmx:afterSwap", "data-month-active"} {
		if !strings.Contains(string(js), want) {
			t.Fatalf("shared month selector behavior missing %q", want)
		}
	}

	markup, readErr := os.ReadFile(resolveTemplatePath(t, "templates/components/app_shell.html"))
	if readErr != nil {
		t.Fatalf("read app shell: %v", readErr)
	}
	if !strings.Contains(string(markup), `/assets/js/month-selector.js`) {
		t.Fatal("app shell must load the shared month selector behavior")
	}
}

func TestAppShellKeepsConfirmationAndSwipeRuntimeAcrossHTMXNavigation(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/components/app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		`{{define "app-v2-shared-overlays"}}`,
		`id="global-confirm-modal"`,
		`id="global-delete-scope-modal"`,
		`{{template "app-v2-shared-overlays" .}}`,
		`<script src="/assets/js/modals-global.js"></script>`,
		`assetPath "/assets/js/faturas.js"`,
		`<script src="/assets/js/swipe.js"></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("app shell missing shared interaction runtime %q", want)
		}
	}

	for _, page := range []string{"contas.html", "faturas.html", "faturas_index.html", "lancamentos.html"} {
		pageRaw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/"+page))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		pageBody := string(pageRaw)
		for _, redundant := range []string{
			`{{template "lancamentos-global-overlays" .}}`,
			`<script src="/assets/js/modals-global.js"></script>`,
			`<script src="{{assetPath "/assets/js/faturas.js"}}"></script>`,
			`<script src="/assets/js/swipe.js"></script>`,
		} {
			if strings.Contains(pageBody, redundant) {
				t.Fatalf("%s must rely on the app-shell runtime, not duplicate %q", page, redundant)
			}
		}
	}
}

func TestGlobalProviderSelectorIsLoadedByAppShell(t *testing.T) {
	markup, err := os.ReadFile(resolveTemplatePath(t, "templates/components/app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	if !strings.Contains(string(markup), `assetPath "/assets/js/config-selects.js"`) {
		t.Fatal("app shell must load the cache-versioned global provider selector")
	}
}

func TestGlobalShellOmitsRedundantProfileCard(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/components/app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	body := string(raw)

	for _, removed := range []string{
		`cb-v2-user-card`,
		`cb-v2-user-avatar`,
		`data-lucide="user-pen"`,
	} {
		if strings.Contains(body, removed) {
			t.Fatalf("global shell must not keep redundant profile control %q", removed)
		}
	}
}

func TestAuthenticatedPagesUseSingleAppShell(t *testing.T) {
	pages := []string{
		"ajuda.html", "configuracoes.html", "contas.html", "contatos.html",
		"dashboard.html", "faturas.html", "lancamentos.html", "metas.html",
		"notificacoes.html", "relatorios.html",
	}
	for _, page := range pages {
		raw, err := os.ReadFile(resolveTemplatePath(t, "templates/pages/"+page))
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		body := string(raw)
		if !strings.Contains(body, `{{template "app-v2-document-head-start" .}}`) {
			t.Fatalf("%s must use the authenticated app shell", page)
		}
		if strings.Contains(body, `{{template "layout-start"`) {
			t.Fatalf("%s must not reference the removed legacy layout", page)
		}
	}
}

func TestInstitutionLogoUsesGlobalRoundedContract(t *testing.T) {
	css, err := os.ReadFile(resolveTemplatePath(t, "assets/css/design-system.css"))
	if err != nil {
		t.Fatalf("read design system: %v", err)
	}
	body := string(css)
	for _, want := range []string{".institution-logo {", "border-radius: 8px;", "object-fit: cover;", ".institution-logo.hidden { display: none; }"} {
		if !strings.Contains(body, want) {
			t.Fatalf("global institution logo contract missing %q", want)
		}
	}
}

func TestNotificationsContentUsesCenteredColumn(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/notificacoes.css"))
	if err != nil {
		t.Fatalf("read notificacoes.css: %v", err)
	}
	if !strings.Contains(string(raw), ".cb-v2-notificacoes-content {\n    width: min(100%, 1180px);\n    margin-inline: auto;\n") {
		t.Fatal("notifications content must be centered within the main column")
	}
}

func TestSidebarNavigationUsesThreeCompactBlocks(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/app-shell.css"))
	if err != nil {
		t.Fatalf("read app-shell.css: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		".cb-v2-sidebar-body {\n    display: flex;",
		".cb-v2-nav-list { display: grid; gap: 3px; margin-top: 30px; }",
		".cb-v2-quick-actions { display: grid; gap: 3px; margin-top: 22px; padding-top: 16px; border-top: 1px solid var(--v2-border); }",
		".cb-v2-settings-nav { display: grid; gap: 6px; margin-top: 16px; }",
		".cb-v2-sidebar-footer { display: grid; flex: 0 0 auto; gap: 8px; margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--v2-border); }",
		".cb-v2-drawer-footer { display: grid; gap: 8px; margin-top: auto; padding-top: 14px; border-top: 1px solid var(--v2-border); }",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sidebar compact block styling missing %q", want)
		}
	}
}

func TestPrimaryNavigationOrderMatchesDesktopAndMobile(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "templates/components/app_shell.html"))
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	body := string(raw)
	desktopExpected := []string{
		`href="/"`,
		`href="/lancamentos"`,
		`href="/metas"`,
		`href="/contas"`,
		`href="/relatorios"`,
		`href="/contatos"`,
	}

	desktopStart := strings.Index(body, `{{define "app-v2-nav-links"}}`)
	if desktopStart < 0 {
		t.Fatal("desktop primary navigation not found")
	}
	desktopEnd := strings.Index(body[desktopStart:], `{{define "app-v2-settings-nav"}}`)
	if desktopEnd < 0 {
		t.Fatal("desktop primary navigation closing tag not found")
	}
	assertOrderedNavigationMarkers(t, "desktop", body[desktopStart:desktopStart+desktopEnd], desktopExpected)

	mobileStart := strings.Index(body, `<nav class="cb-v2-bottom-nav"`)
	if mobileStart < 0 {
		t.Fatal("mobile bottom navigation not found")
	}
	mobileEnd := strings.Index(body[mobileStart:], `</nav>`)
	if mobileEnd < 0 {
		t.Fatal("mobile bottom navigation closing tag not found")
	}
	mobileExpected := []string{
		`href="/"`,
		`href="/lancamentos"`,
		`href="/metas"`,
		`href="/contas"`,
	}
	assertOrderedNavigationMarkers(t, "mobile", body[mobileStart:mobileStart+mobileEnd], mobileExpected)
}

func assertOrderedNavigationMarkers(t *testing.T, navigation string, body string, expected []string) {
	t.Helper()
	cursor := 0
	for _, marker := range expected {
		index := strings.Index(body[cursor:], marker)
		if index < 0 {
			t.Fatalf("%s navigation missing %s", navigation, marker)
		}
		cursor += index + len(marker)
	}
}

func TestDashboardMobileSectionSpacing(t *testing.T) {
	raw, err := os.ReadFile(resolveTemplatePath(t, "assets/css/dashboard.css"))
	if err != nil {
		t.Fatalf("read dashboard.css: %v", err)
	}
	body := string(raw)

	idx := strings.Index(body, "@media (max-width: 767px)")
	if idx < 0 {
		t.Fatal("dashboard.css missing mobile media query")
	}
	mobile := body[idx:]
	if !strings.Contains(mobile, ".cb-v2-left-column { display: contents; }") {
		t.Fatal("mobile dashboard should flatten the layout columns")
	}
	if !strings.Contains(mobile, ".cb-v2-card-rail { display: flex;") || !strings.Contains(mobile, "scroll-snap-type: x mandatory") {
		t.Fatal("mobile dashboard should show credit cards in a horizontal snap rail")
	}
	if !strings.Contains(mobile, ".cb-v2-card-rail > [data-v2-card-item]:nth-child(n+7) { display: none; }") {
		t.Fatal("mobile dashboard should hide real cards after the sixth")
	}
}
