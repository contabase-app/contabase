package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/contabase-app/contabase"
)

func TestEnforcingCSPBlocksExecutableInlineSources(t *testing.T) {
	directives := parseCSPDirectives(cspEnforcingPolicy)

	assertCSPDirectiveTokens(t, directives, "default-src", "'self'")
	assertCSPDirectiveTokens(t, directives, "script-src", "'self'")
	assertCSPDirectiveTokens(t, directives, "script-src-attr", "'none'")
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'", "data:", "https:"} {
		if containsString(directives["script-src"], forbidden) {
			t.Fatalf("script-src permits %s: %q", forbidden, cspEnforcingPolicy)
		}
	}
	assertCSPDirectiveTokens(t, directives, "object-src", "'none'")
	assertCSPDirectiveTokens(t, directives, "frame-src", "'none'")
	assertCSPDirectiveTokens(t, directives, "frame-ancestors", "'none'")
	assertCSPDirectiveTokens(t, directives, "form-action", "'self'")
	assertCSPDirectiveTokens(t, directives, "base-uri", "'self'")

	assertCSPDirectiveTokens(t, directives, "style-src", "'self'")
	assertCSPDirectiveTokens(t, directives, "style-src-elem", "'self'")
	assertCSPDirectiveTokens(t, directives, "style-src-attr", "'none'")
}

func TestProductionScriptsDoNotWriteInlineStyles(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	inlineStyleMutation := regexp.MustCompile(`(?i)\.style(?:\.|\[)|setAttribute\s*\(\s*["']style["']`)

	err := filepath.WalkDir(filepath.Join(repoRoot, "assets", "js"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".js") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if match := inlineStyleMutation.FindString(string(content)); match != "" {
			t.Errorf("%s writes an inline style blocked by style-src-attr 'none': %q", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk production scripts: %v", err)
	}
}

func TestRuntimeMotionLoadsBeforeInteractiveConsumers(t *testing.T) {
	content, err := fs.ReadFile(contabase.TemplatesFS, "templates/components/app_shell.html")
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	body := string(content)
	runtimeIndex := strings.Index(body, `assetPath "/assets/js/runtime-motion.js"`)
	if runtimeIndex < 0 {
		t.Fatal("app shell must load runtime-motion.js")
	}
	for _, consumer := range []string{`assetPath "/assets/js/swipe.js"`, `assetPath "/assets/js/tooltip.js"`} {
		consumerIndex := strings.Index(body, consumer)
		if consumerIndex < 0 {
			t.Fatalf("app shell is missing motion consumer %q", consumer)
		}
		if runtimeIndex >= consumerIndex {
			t.Fatalf("runtime-motion.js must load before %q", consumer)
		}
	}

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	stylesheet, err := os.ReadFile(filepath.Join(repoRoot, "assets", "css", "design-system.css"))
	if err != nil {
		t.Fatalf("read design system stylesheet: %v", err)
	}
	for _, selector := range []string{"[data-swipe-content]", "[data-swipe-bg-left]", "[data-swipe-notification]", "#global-tooltip"} {
		if !strings.Contains(string(stylesheet), selector) {
			t.Errorf("design system is missing motion base selector %q", selector)
		}
	}
}

func TestTemplatesContainOnlyExternalScriptsAndNoEventHandlerAttributes(t *testing.T) {
	scriptTag := regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	styleTag := regexp.MustCompile(`(?is)<style(?:\s|>)`)
	scriptSource := regexp.MustCompile(`(?i)\bsrc\s*=\s*["'][^"']+["']`)
	eventHandler := regexp.MustCompile(`(?i)\s+on[a-z]+\s*=`)
	javascriptURL := regexp.MustCompile(`(?i)(?:href|src)\s*=\s*["']\s*javascript:`)

	err := fs.WalkDir(contabase.TemplatesFS, "templates", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".html") {
			return nil
		}
		content, err := fs.ReadFile(contabase.TemplatesFS, path)
		if err != nil {
			return err
		}
		body := string(content)
		if match := styleTag.FindString(body); match != "" {
			t.Errorf("%s contains an inline style element: %q", path, match)
		}
		for _, match := range scriptTag.FindAllStringSubmatch(body, -1) {
			if len(match) != 2 || !scriptSource.MatchString(match[1]) {
				t.Errorf("%s contains an inline script tag: %q", path, match[0])
			}
		}
		if match := eventHandler.FindString(body); match != "" {
			t.Errorf("%s contains an executable event-handler attribute: %q", path, match)
		}
		if match := javascriptURL.FindString(body); match != "" {
			t.Errorf("%s contains a javascript URL: %q", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded templates: %v", err)
	}
}

func TestTemplatesContainNoStyleAttributes(t *testing.T) {
	styleAttribute := regexp.MustCompile(`(?i)\sstyle\s*=`)
	err := fs.WalkDir(contabase.TemplatesFS, "templates", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".html") {
			return nil
		}
		content, err := fs.ReadFile(contabase.TemplatesFS, path)
		if err != nil {
			return err
		}
		if match := styleAttribute.FindString(string(content)); match != "" {
			t.Errorf("%s contains a style attribute: %q", path, match)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded templates: %v", err)
	}
}

func TestWorkspaceColorsUseAuthenticatedExternalStylesheet(t *testing.T) {
	content, err := fs.ReadFile(contabase.TemplatesFS, "templates/components/app_shell.html")
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	body := string(content)
	for _, want := range []string{
		`href="/workspace/colors.css" data-workspace-colors`,
		`assetPath "/assets/js/runtime-colors.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app shell is missing %q", want)
		}
	}

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	for _, relativePath := range []string{
		"assets/js/config-selects.js",
		"assets/js/form-lancamento.js",
		"assets/js/form-meta.js",
	} {
		script, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		for _, forbidden := range []string{".style.color", ".style.backgroundColor", ".style.borderColor", ".style.setProperty('--ic'"} {
			if strings.Contains(string(script), forbidden) {
				t.Errorf("%s retains inline color mutation %q", relativePath, forbidden)
			}
		}
	}
}

func TestProductionGoHTMLContainsNoExecutableInlineSources(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	eventHandler := regexp.MustCompile(`(?i)(?:^|[^a-z0-9_-])on[a-z]+\s*=\s*\\?["']`)
	javascriptURL := regexp.MustCompile(`(?i)(?:href|src)\s*=\s*\\?["']\s*javascript:`)
	styleTag := regexp.MustCompile(`(?i)<style(?:\s|>)`)
	styleAttribute := regexp.MustCompile(`(?i)\sstyle\s*=\s*\\?["']`)

	for _, root := range []string{filepath.Join(repoRoot, "cmd"), filepath.Join(repoRoot, "internal")} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			body := string(content)
			if match := styleTag.FindString(body); match != "" {
				t.Errorf("%s emits or contains an inline style element: %q", path, match)
			}
			if match := styleAttribute.FindString(body); match != "" {
				t.Errorf("%s emits or contains an inline style attribute: %q", path, match)
			}
			if match := eventHandler.FindString(body); match != "" {
				t.Errorf("%s emits or contains an executable event-handler attribute: %q", path, match)
			}
			if match := javascriptURL.FindString(body); match != "" {
				t.Errorf("%s emits or contains a javascript URL: %q", path, match)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk production Go sources under %s: %v", root, err)
		}
	}
}

func TestWorkspaceDrawerThemeUsesExternalStylesheet(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))

	serverSource, err := os.ReadFile(filepath.Join(repoRoot, "cmd", "server", "main.go"))
	if err != nil {
		t.Fatalf("read server source: %v", err)
	}
	serverBody := string(serverSource)
	for _, want := range []string{
		`data-workspace-theme="%s"`,
		`data-workspace-active="%s"`,
		`cb-v2-workspace-choice-icon`,
	} {
		if !strings.Contains(serverBody, want) {
			t.Errorf("workspace drawer source is missing %q", want)
		}
	}
	for _, forbidden := range []string{`style="border-color:rgb(`, `style="color:rgb(`} {
		if strings.Contains(serverBody, forbidden) {
			t.Errorf("workspace drawer source still emits inline CSS: %q", forbidden)
		}
	}

	stylesheet, err := os.ReadFile(filepath.Join(repoRoot, "assets", "css", "app-shell.css"))
	if err != nil {
		t.Fatalf("read app shell stylesheet: %v", err)
	}
	css := string(stylesheet)
	for _, theme := range []string{"violeta", "laranja", "amarelo", "azul", "esmeralda", "rosa"} {
		if !strings.Contains(css, `data-workspace-theme="`+theme+`"`) {
			t.Errorf("stylesheet does not map workspace theme %q", theme)
		}
	}
	for _, want := range []string{`data-workspace-active="true"`, `.cb-v2-workspace-choice-icon`} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet is missing workspace drawer theme selector %q", want)
		}
	}
}

func TestWorkspaceThemeScriptDoesNotWriteInlineCSS(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))

	script, err := os.ReadFile(filepath.Join(repoRoot, "assets", "js", "workspace.js"))
	if err != nil {
		t.Fatalf("read workspace script: %v", err)
	}
	body := string(script)
	if strings.Contains(body, ".style.") {
		t.Fatal("workspace theme script must not write inline CSS")
	}
	for _, want := range []string{
		`body.dataset.workspaceTheme = theme`,
		`['violeta', 'laranja', 'amarelo', 'azul', 'esmeralda', 'rosa']`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("workspace theme script is missing %q", want)
		}
	}
}

func TestProgressTemplatesUseNativeElementsWithoutInlineWidths(t *testing.T) {
	templateChecks := map[string][]string{
		"templates/components/credit_card.html": {
			`<progress class="cb-card-progress" value="{{.LimitPercent}}" max="100"`,
		},
		"templates/pages/dashboard.html": {
			// Medidores de Limites e de Metas usam o mesmo elemento nativo.
			`<progress class="cb-v2-meter-track" value="{{.Percent}}" max="100"`,
		},
		"templates/pages/metas.html": {
			// Reservas: barra de progresso nativa, sem largura inline.
			`<progress class="metas-progress" value="{{.Percent}}" max="100"`,
			// Limites: a mesma barra nativa comunica o disponível.
			`<progress class="metas-progress" {{if ge .Percent 100}}value="0"`,
		},
		"templates/pages/faturas.html": {
			`<progress class="cb-invoice-limit-progress {{if le .LimitPercent 30}}is-danger{{else if le .LimitPercent 70}}is-warning{{else}}is-success{{end}}" value="{{.LimitPercent}}" max="100"`,
		},
	}
	for path, wants := range templateChecks {
		content, err := fs.ReadFile(contabase.TemplatesFS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(content)
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s is missing native progress markup %q", path, want)
			}
		}
		for _, forbidden := range []string{`--v2-progress:`, `--pct:`, `style="width: {{.LimitPercent}}%`} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s retains inline progress styling %q", path, forbidden)
			}
		}
	}

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	for _, relativePath := range []string{
		"assets/css/dashboard.css",
		"assets/css/metas.css",
		"assets/css/faturas.css",
	} {
		content, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		body := string(content)
		for _, want := range []string{"appearance: none", "::-webkit-progress-value", "::-moz-progress-bar"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s is missing native progress styling %q", relativePath, want)
			}
		}
	}
}

func TestLegacyDashboardCardsTemplateHasNoInlineProgressStyle(t *testing.T) {
	content, err := fs.ReadFile(contabase.TemplatesFS, "templates/components/dashboard-cards.html")
	if err != nil {
		t.Fatalf("read dashboard cards template: %v", err)
	}
	body := string(content)
	for _, forbidden := range []string{"card-cc-bar", `style="width: {{.LimitPercent}}%`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("legacy dashboard cards template retains obsolete inline progress markup %q", forbidden)
		}
	}
}

func TestHTMXDoesNotInjectIndicatorStyleElement(t *testing.T) {
	content, err := fs.ReadFile(contabase.TemplatesFS, "templates/components/app_shell.html")
	if err != nil {
		t.Fatalf("read app shell: %v", err)
	}
	if !strings.Contains(string(content), `"includeIndicatorStyles":false`) {
		t.Fatal("HTMX must not inject its default inline indicator style under style-src-elem 'self'")
	}
}

func TestTemplatesDoNotReintroduceMigratedStaticStyleAttributes(t *testing.T) {
	styleAttribute := regexp.MustCompile(`(?is)\sstyle\s*=\s*["']([^"']*)["']`)
	forbidden := []string{
		`style="background: color-mix(in srgb, var(--cb-`,
		`style="border-color: color-mix(in srgb, var(--cb-`,
		`style="color: color-mix(in srgb, var(--cb-`,
		`style="background-color:rgb(0, 212, 146)"`,
		`style="background-color:rgb(103, 68, 241)"`,
		`style="color:rgb(0, 212, 146)"`,
		`style="color:rgb(103, 68, 241)"`,
	}

	err := fs.WalkDir(contabase.TemplatesFS, "templates", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".html") {
			return nil
		}
		content, err := fs.ReadFile(contabase.TemplatesFS, path)
		if err != nil {
			return err
		}
		body := string(content)
		for _, match := range styleAttribute.FindAllStringSubmatch(body, -1) {
			if !strings.Contains(match[1], "{{") {
				t.Errorf("%s contains a static style attribute that must use a stylesheet class: %q", path, match[0])
			}
		}
		for _, style := range forbidden {
			if strings.Contains(body, style) {
				t.Errorf("%s reintroduces a static style attribute covered by the CSP migration: %q", path, style)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded templates: %v", err)
	}
}

func TestVisibilityPathsDoNotWriteInlineDisplayStyles(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	paths := []string{
		"assets/js/categorias-ui.js",
		"assets/js/config-selects.js",
		"assets/js/event-handlers.js",
		"assets/js/form-lancamento.js",
		"assets/js/lancamentos-filters.js",
		"assets/js/lancamentos-selection.js",
		"assets/js/modals-global.js",
		"assets/js/overlay-stack.js",
		"assets/js/recibo.js",
	}
	for _, relativePath := range paths {
		content, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		if strings.Contains(string(content), ".style.display") {
			t.Errorf("%s writes or relies on an inline display style", relativePath)
		}
	}
}

func TestReceiptAvatarFallbackUsesItsExternalVisibilityClass(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	checks := map[string]string{
		"templates/pages/recibo.html": `class="brand-fallback receipt-hidden"`,
		"assets/css/recibo.css":       ".receipt-hidden { display: none; }",
		"assets/js/recibo.js":         "classList.remove('receipt-hidden')",
	}
	for relativePath, want := range checks {
		content, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		if !strings.Contains(string(content), want) {
			t.Errorf("%s must contain %q", relativePath, want)
		}
	}
}

func parseCSPDirectives(policy string) map[string][]string {
	directives := make(map[string][]string)
	for _, raw := range strings.Split(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) == 0 {
			continue
		}
		directives[fields[0]] = append([]string(nil), fields[1:]...)
	}
	return directives
}

func assertCSPDirectiveTokens(t *testing.T, directives map[string][]string, name string, want ...string) {
	t.Helper()
	got, ok := directives[name]
	if !ok {
		t.Fatalf("CSP directive %q is missing", name)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("CSP directive %q = %q, want %q", name, got, want)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
