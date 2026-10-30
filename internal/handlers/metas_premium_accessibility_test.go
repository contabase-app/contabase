package handlers

import (
	"bytes"
	"strings"
	"testing"
)

// M7 — Semântica das abas, empty states canônicos e alvos de toque.
//
// A M7 corrige apenas o que a M2/M4/M5 ainda deixavam em aberto na página:
// as abas não tinham contrato tablist/tab/tabpanel, os empty states de
// Limites/Reservas ainda usavam o componente legado e tabs/privacidade
// ficavam abaixo de 44px no shell touch.

func renderMetasTabsForTest(t *testing.T, aba string) string {
	t.Helper()
	tpl := testMetasPageTemplates(t)
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-tabs", MetasData{Aba: aba}); err != nil {
		t.Fatalf("render metas-tabs (%s): %v", aba, err)
	}
	return out.String()
}

func TestMetasTabsExposeTablistContract(t *testing.T) {
	cases := []struct {
		aba           string
		activeID      string
		inactiveID    string
		activePanel   string
		inactivePanel string
	}{
		{
			aba:           "limites",
			activeID:      "metas-tab-limites",
			inactiveID:    "metas-tab-reservas",
			activePanel:   "metas-panel-limites",
			inactivePanel: "metas-panel-reservas",
		},
		{
			aba:           "caixinhas",
			activeID:      "metas-tab-reservas",
			inactiveID:    "metas-tab-limites",
			activePanel:   "metas-panel-reservas",
			inactivePanel: "metas-panel-limites",
		},
	}

	for _, tc := range cases {
		t.Run(tc.aba, func(t *testing.T) {
			body := renderMetasTabsForTest(t, tc.aba)
			for _, expected := range []string{
				`role="tablist"`,
				`aria-label="Limites e reservas"`,
				`aria-orientation="horizontal"`,
				`role="tab"`,
				`id="` + tc.activeID + `"`,
				`aria-selected="true"`,
				`tabindex="0"`,
				`aria-controls="` + tc.activePanel + `"`,
				`id="` + tc.inactiveID + `"`,
				`aria-selected="false"`,
				`tabindex="-1"`,
				`aria-controls="` + tc.inactivePanel + `"`,
				`id="` + tc.activePanel + `" role="tabpanel"`,
				`aria-labelledby="` + tc.activeID + `"`,
			} {
				assertContains(t, body, expected)
			}
			if got := strings.Count(body, `role="tab"`); got != 2 {
				t.Fatalf("expected exactly two tabs, got %d", got)
			}
			if strings.Contains(body, `class="cb-tabs mb-5 w-full"`) && !strings.Contains(body, `class="cb-tabs mb-5 w-full" role="tablist"`) {
				t.Fatal("tablist must keep the cb-tabs class and role=tablist")
			}
		})
	}
}

func TestMetasEmptyStatesUseCanonicalUIEmpty(t *testing.T) {
	cases := []struct {
		aba     string
		title   string
		message string
		icon    string
	}{
		{aba: "limites", title: "Nenhum limite definido", message: "Use o botão + para criar seu primeiro limite.", icon: "gauge"},
		{aba: "caixinhas", title: "Nenhuma reserva criada", message: "Use o botão + para criar sua primeira reserva.", icon: "piggy-bank"},
	}

	for _, tc := range cases {
		t.Run(tc.aba, func(t *testing.T) {
			body := renderMetasTabsForTest(t, tc.aba)
			for _, expected := range []string{
				`class="ui-empty" role="status"`,
				`class="ui-empty__icon" aria-hidden="true"`,
				`data-lucide="` + tc.icon + `"`,
				`class="ui-empty__title"`,
				tc.title,
				`class="ui-empty__text"`,
				tc.message,
			} {
				assertContains(t, body, expected)
			}
			if strings.Contains(body, `cb-empty-state`) || strings.Contains(body, `metas-empty-icon`) {
				t.Fatal("metas empty state must use ui-empty, not the legacy cb-empty-state")
			}
		})
	}
}

func TestMetasTouchTargetsStayScopedToTouchShell(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	start := strings.Index(css, "@media (max-width: 1023px)")
	if start < 0 {
		t.Fatal("metas.css missing the touch shell media query")
	}
	next := strings.Index(css[start+1:], "@media")
	var section string
	if next < 0 {
		section = css[start:]
	} else {
		section = css[start : start+1+next]
	}
	for _, expected := range []string{
		`.cb-v2-metas-panels .cb-tabs .cb-tab { min-height: 2.75rem; }`,
		`.cb-v2-metas-header [data-privacy-toggle],`,
		`.cb-v2-metas-header #metas-primary-action {`,
		`min-height: 2.75rem;`,
		`width: 2.75rem;`,
		`height: 2.75rem;`,
	} {
		if !strings.Contains(section, expected) {
			t.Fatalf("touch shell must contain %q", expected)
		}
	}
	if strings.Contains(section, "!important") {
		t.Fatal("touch target rules must not use !important")
	}
	// A geometria desktop aprovada não é alterada fora do shell touch.
	if strings.Contains(css, ".cb-v2-metas-panels .cb-tabs .cb-tab {\n  min-height: 2.75rem;") {
		t.Fatal("touch target rule must stay inside the max-width: 1023px shell")
	}
}
