package handlers

import (
	"strings"
	"testing"
)

// D4-C2: the dashboard carousel ends with one visual add-card placeholder.
// It must exist with zero or one real card, must not be counted as a card and
// must not stretch the real card to the full rail width.
func TestDashboardPreviewAddCardPlaceholderContract(t *testing.T) {
	render := func(data DashboardPreviewData) string {
		t.Helper()
		var out strings.Builder
		if err := previewTestTemplate(t).ExecuteTemplate(&out, "dashboard-preview-cards", data); err != nil {
			t.Fatalf("render cards section: %v", err)
		}
		return out.String()
	}

	allowed := DashboardPreviewData{}
	allowed.CanManageOps = true
	empty := render(allowed)
	if !strings.Contains(empty, "data-wallet-add-card") || !strings.Contains(empty, "Adicionar cartão") {
		t.Fatal("zero-card state must offer the add-card placeholder")
	}
	if strings.Contains(empty, "data-wallet-card=") {
		t.Fatal("zero-card placeholder must not be marked as a real card")
	}
	if strings.Contains(empty, "data-wallet-position") {
		t.Fatal("zero-card state must not render carousel navigation")
	}

	denied := DashboardPreviewData{}
	deniedBody := render(denied)
	if strings.Contains(deniedBody, "data-wallet-add-card") {
		t.Fatal("members without card-management permission must not receive the add-card placeholder")
	}
	if !strings.Contains(deniedBody, "Cadastre seu primeiro cartão.") {
		t.Fatal("members without permission keep the neutral empty state")
	}

	one := DashboardPreviewData{PreviewCards: []PreviewCreditCard{{
		CreditCardCard: CreditCardCard{Name: "Cartão único", Href: "/faturas", WalletIndex: "0"},
	}}}
	one.CanManageOps = true
	body := render(one)
	if !strings.Contains(body, `data-card-count="1"`) {
		t.Fatal("one real card must keep the real-card count")
	}
	if strings.Count(body, "data-wallet-add-card") != 1 {
		t.Fatal("the carousel must render exactly one add-card placeholder")
	}
	if !strings.Contains(body, `data-wallet-card="0"`) {
		t.Fatal("the real card must remain distinct from the placeholder")
	}

	css := readTestFile(t, "assets/css/dashboard-preview.css")
	if strings.Contains(css, `[data-card-count="1"] .cbp-credit-card`) {
		t.Fatal("a single real card must not stretch to the full rail width")
	}
	if !strings.Contains(css, ".cbp-credit-card--add") {
		t.Fatal("add-card placeholder needs its own placeholder surface")
	}
}
