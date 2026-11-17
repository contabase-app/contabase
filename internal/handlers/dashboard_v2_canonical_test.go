package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDashboardV2CanonicalRenderHasNoPreviewSurface(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	data := buildDashboardV2DataAtContext(context.Background(), db, base.user, base.ws, now, "12m", "2026-10")
	SortDashboardCardsByPaymentPriority(data.Cards, now)
	data.PreviewCards = buildPreviewCardsForPeriod(data.Cards, &now)
	data.LayoutPending = true

	var out strings.Builder
	if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-page", data); err != nil {
		t.Fatalf("render canonical V2: %v", err)
	}
	body := out.String()

	for _, forbidden := range []string{
		"Prévia DEV",
		"Painel · prévia",
		"Prévia de homologação",
		"Voltar ao painel atual",
		`name="robots"`,
		"/preview/dashboard-v2",
		`class="wallet-heading"`,
		`id="dashboard-balance"`,
		`id="dashboard-cards"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("canonical render retained preview/V1 surface %q", forbidden)
		}
	}
	for _, want := range []string{
		`class="cb-v2 cb-preview"`,
		`data-page-title="Dashboard"`,
		`data-cbp-refresh-root`,
		`hx-trigger="refreshFinancials from:body"`,
		`hx-get="/?fluxo=12m&amp;mes=2026-10"`,
		`href="/?fluxo=12m&amp;mes=2026-09"`,
		`<span>Painel</span>`,
		"Sua carteira, em perspectiva.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("canonical render missing %q", want)
		}
	}
	if got := strings.Count(body, `id="dashboard-content"`); got != 1 {
		t.Fatalf("canonical grid count = %d, want 1", got)
	}
	if got := strings.Count(body, `data-cbp-section=`); got != 8 {
		t.Fatalf("canonical card count = %d, want 8", got)
	}
}

func TestDashboardV2CanonicalRefreshURLUsesRoot(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	current := buildDashboardV2DataAtContext(context.Background(), db, base.user, base.ws, now, "6m", "")
	if got := current.DashboardRefreshURL; got != "/" {
		t.Fatalf("canonical current refresh URL = %q, want /", got)
	}

	historical := buildDashboardV2DataAtContext(context.Background(), db, base.user, base.ws, now, "12m", "2026-10")
	if got := historical.DashboardRefreshURL; got != "/?fluxo=12m&mes=2026-10" {
		t.Fatalf("canonical historical refresh URL = %q", got)
	}

}

func TestDashboardV2ServeHTTPHeadersAndLayoutGate(t *testing.T) {
	now := time.Now()
	db, base := seedDashboardPreviewScenario(t, now)
	defer db.Close()

	newHandler := func() DashboardV2Handler {
		return DashboardV2Handler{
			DB:          db,
			Templates:   previewTestTemplate(t),
			UserID:      base.user,
			WorkspaceID: base.ws,
		}
	}

	canonicalHandler := newHandler()
	canonical := httptest.NewRecorder()
	canonicalHandler.ServeHTTP(canonical, httptest.NewRequest(http.MethodGet, "/", nil))
	if canonical.Code != http.StatusOK {
		t.Fatalf("canonical status = %d", canonical.Code)
	}
	if got := canonical.Header().Get("X-Robots-Tag"); got != "" {
		t.Fatalf("canonical dashboard must not send X-Robots-Tag, got %q", got)
	}
	if got := canonical.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("canonical Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(canonical.Body.String(), `is-layout-pending`) {
		t.Fatal("canonical first paint must keep the anti-flash gate")
	}

	canonicalRefresh := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?fluxo=12m", nil)
	req.Header.Set("HX-Request", "true")
	canonicalHandler.ServeHTTP(canonicalRefresh, req)
	if strings.Contains(canonicalRefresh.Body.String(), `is-layout-pending`) {
		t.Fatal("canonical HTMX refresh must not hide the grid")
	}
	if !strings.Contains(canonicalRefresh.Body.String(), `hx-get="/?fluxo=12m"`) {
		t.Fatal("canonical refresh must keep the V2 refresh contract")
	}

}

func TestDashboardV2KeepsPWAManifestForRoot(t *testing.T) {
	raw := readTestFile(t, "assets/manifest.json")
	var manifest struct {
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if manifest.StartURL != "/" {
		t.Fatalf("manifest start_url = %q, want /", manifest.StartURL)
	}
	if manifest.Display != "standalone" {
		t.Fatalf("manifest display = %q, want standalone", manifest.Display)
	}
	var has512 bool
	for _, icon := range manifest.Icons {
		if icon.Sizes == "512x512" && strings.HasPrefix(icon.Src, "/assets/") {
			has512 = true
		}
	}
	if !has512 {
		t.Fatal("manifest must keep a local 512x512 icon")
	}
}
