package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// insertInstitutionTestAccount cria uma conta fora do fluxo de criação para que
// cada cenário de edição parta de um estado conhecido do workspace.
func insertInstitutionTestAccount(t *testing.T, h ConfiguracoesHandler, id, workspaceID, name, accType, providerSlug string, balanceCents int64) {
	t.Helper()
	execTestSQL(t, h.DB, `
		INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, initial_balance, current_balance)
		VALUES (?, ?, ?, ?, '#00A6FF', '', ?, ?, ?)
	`, id, workspaceID, name, accType, providerSlug, balanceCents, balanceCents)
}

func accountProviderState(t *testing.T, h ConfiguracoesHandler, id string) (name, providerSlug, accType string, color string, initial, current int64) {
	t.Helper()
	if err := h.DB.QueryRow(`
		SELECT name, COALESCE(NULLIF(provider_slug, ''), 'custom'), type, COALESCE(NULLIF(color, ''), '#6B7280'), initial_balance, current_balance
		FROM accounts WHERE id = ?
	`, id).Scan(&name, &providerSlug, &accType, &color, &initial, &current); err != nil {
		t.Fatalf("read account %s: %v", id, err)
	}
	return name, providerSlug, accType, color, initial, current
}

// T1 — Stripe entra no catálogo canônico usado por todas as contas.
func TestStripeEntersAccountProviderCatalog(t *testing.T) {
	provider, ok := accountProviderBySlugOrCustom("stripe")
	if !ok {
		t.Fatal("stripe ausente do catálogo de instituições")
	}
	if provider.Slug != "stripe" || provider.Name != "Stripe" {
		t.Fatalf("stripe catalogado como %q/%q", provider.Slug, provider.Name)
	}
	if provider.LogoPath != "institutions/stripe.png" {
		t.Fatalf("stripe logo = %q", provider.LogoPath)
	}
	if !hexColorPattern.MatchString(provider.Color) {
		t.Fatalf("stripe color inválida: %q", provider.Color)
	}
	if got := normalizeAccountProviderSlug("STRIPE"); got != "stripe" {
		t.Fatalf("normalizeAccountProviderSlug(STRIPE) = %q", got)
	}
	if got := identifyAlias("Stripe Brasil"); got != "stripe" {
		t.Fatalf("identifyAlias(Stripe Brasil) = %q", got)
	}

	assetPath := filepath.Join("..", "..", "assets", "institutions", "stripe.png")
	if _, err := os.Stat(assetPath); err != nil {
		t.Fatalf("asset do logo Stripe ausente: %v", err)
	}

	// O seletor canônico precisa oferecer Stripe com opção e escolha visual.
	h := newConfiguracoesRenderTestHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/configuracoes/contas", nil)
	req.Header.Set("HX-Request", "true")
	h.HandleConfiguracoesSection(rr, req, "contas")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`<option value="stripe">Stripe</option>`,
		`data-provider-slug="stripe" data-provider-name="Stripe"`,
		`src="institutions/stripe.png?v=test"`,
	} {
		assertContains(t, body, want)
	}
}

// T2 — criação de conta com Stripe persiste instituição e logo.
func TestCreateAccountWithStripePersistsProviderAndLogo(t *testing.T) {
	h := newConfiguracoesRenderTestHandler(t)
	rr := httptest.NewRecorder()
	h.HandleContasCreate(rr, formPost("/configuracoes/contas", url.Values{
		"provider_slug":   {"stripe"},
		"type":            {"CHECKING"},
		"initial_balance": {"1399,31"},
	}))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var id, name, slug, color string
	var initial, current int64
	if err := h.DB.QueryRow(`
		SELECT id, name, COALESCE(NULLIF(provider_slug, ''), 'custom'), color, initial_balance, current_balance
		FROM accounts WHERE workspace_id = ?
	`, h.WorkspaceID).Scan(&id, &name, &slug, &color, &initial, &current); err != nil {
		t.Fatalf("conta criada não encontrada: %v", err)
	}
	if name != "Stripe" || slug != "stripe" {
		t.Fatalf("instituição persistida = %q/%q, want Stripe/stripe", name, slug)
	}
	if color != "#635BFF" {
		t.Fatalf("cor da conta = %q, want #635BFF", color)
	}
	if initial != 139931 || current != 139931 {
		t.Fatalf("saldos = %d/%d, want 139931/139931", initial, current)
	}
	assertContains(t, rr.Body.String(), `src="institutions/stripe.png?v=test"`)
}

// T3/T4/T5/T6/T7 — edição troca a instituição preservando saldo, tipo e ledger.
func TestAccountInstitutionEditSwapsProviderOnly(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fromSlug     string
		storedType   string
		form         url.Values
		wantSlug     string
		wantLogoPath string
	}{
		{
			name:       "Mercado Pago -> Stripe",
			fromSlug:   "mercadopago",
			storedType: "INVESTMENT",
			form: url.Values{
				"name": {"Conta Principal"}, "type": {"INVESTMENT"}, "color": {"#00A6FF"}, "icon": {""},
				"provider_slug": {"stripe"},
			},
			wantSlug:     "stripe",
			wantLogoPath: "institutions/stripe.png",
		},
		{
			name:       "Stripe -> Nubank",
			fromSlug:   "stripe",
			storedType: "CHECKING",
			form: url.Values{
				"name": {"Conta Principal"}, "type": {"CHECKING"}, "color": {"#635BFF"}, "icon": {""},
				"provider_slug": {"nubank"},
			},
			wantSlug:     "nubank",
			wantLogoPath: "institutions/nubank.png",
		},
		{
			name:       "Stripe -> instituição personalizada",
			fromSlug:   "stripe",
			storedType: "CHECKING",
			form: url.Values{
				"name": {"Conta Principal"}, "type": {"CHECKING"}, "color": {"#123456"}, "icon": {""},
				"provider_slug": {"custom"}, "provider_custom_name": {"Cooperativa Local"},
			},
			wantSlug: "custom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfiguracoesRenderTestHandler(t)
			insertInstitutionTestAccount(t, h, "edit-target", h.WorkspaceID, "Conta Principal", tc.storedType, tc.fromSlug, 139931)
			// T6 — a troca de instituição não pode criar lançamentos.
			execTestSQL(t, h.DB, `
				INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, description, date, created_at, updated_at)
				VALUES ('tx-existing', ?, 'config-user', 'edit-target', NULL, 'EXPENSE', 1000, 'Existente', unixepoch(), unixepoch(), unixepoch())
			`, h.WorkspaceID)

			rr := httptest.NewRecorder()
			h.HandleContasInlineSave(rr, formPost("/configuracoes/contas/edit-target/salvar", tc.form), "edit-target")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
			}

			_, slug, gotType, _, initial, current := accountProviderState(t, h, "edit-target")
			if slug != tc.wantSlug {
				t.Fatalf("provider_slug = %q, want %q", slug, tc.wantSlug)
			}
			// T7 — instituição e tipo são independentes.
			if gotType != tc.storedType {
				t.Fatalf("type = %q, want %q", gotType, tc.storedType)
			}
			// T5 — saldo byte/valor-equivalente à origem.
			if initial != 139931 || current != 139931 {
				t.Fatalf("saldos = %d/%d, want 139931/139931", initial, current)
			}
			if tc.wantLogoPath != "" {
				assertContains(t, rr.Body.String(), `src="`+tc.wantLogoPath+`?v=test"`)
			}
			var transactions, adjustments int
			if err := h.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id = ?`, h.WorkspaceID).Scan(&transactions); err != nil {
				t.Fatal(err)
			}
			if err := h.DB.QueryRow(`SELECT COUNT(*) FROM account_balance_adjustments`).Scan(&adjustments); err != nil {
				t.Fatal(err)
			}
			if transactions != 1 || adjustments != 0 {
				t.Fatalf("transactions = %d, adjustments = %d; want 1/0", transactions, adjustments)
			}
		})
	}
}

// T8 — a edição segue restrita ao workspace autenticado.
func TestAccountInstitutionEditRejectsForeignWorkspace(t *testing.T) {
	h := newConfiguracoesRenderTestHandler(t)
	insertInstitutionTestAccount(t, h, "foreign-account", "config-secondary", "Conta Alheia", "CHECKING", "mercadopago", 500000)

	rr := httptest.NewRecorder()
	h.HandleContasInlineSave(rr, formPost("/configuracoes/contas/foreign-account/salvar", url.Values{
		"name": {"Tomada"}, "type": {"CHECKING"}, "color": {"#000000"}, "icon": {""},
		"provider_slug": {"stripe"}, "workspace_id": {"config-secondary"},
	}), "foreign-account")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body = %s)", rr.Code, rr.Body.String())
	}
	name, slug, _, color, initial, current := accountProviderState(t, h, "foreign-account")
	if name != "Conta Alheia" || slug != "mercadopago" || color != "#00A6FF" || initial != 500000 || current != 500000 {
		t.Fatalf("conta de outro workspace foi alterada: %s/%s/%s/%d/%d", name, slug, color, initial, current)
	}
}

// T9 — instituição ausente ou não definida continua opcional na edição.
func TestAccountInstitutionEditKeepsOptionalProvider(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   string
		wantSlug string
	}{
		{name: "personalizada", stored: "custom", wantSlug: "custom"},
		{name: "ausente", stored: "", wantSlug: "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfiguracoesRenderTestHandler(t)
			insertInstitutionTestAccount(t, h, "no-provider", h.WorkspaceID, "Carteira", "WALLET", tc.stored, 2500)

			rr := httptest.NewRecorder()
			h.HandleContasInlineSave(rr, formPost("/configuracoes/contas/no-provider/salvar", url.Values{
				"name": {"Carteira"}, "type": {"WALLET"}, "color": {"#6B7280"}, "icon": {"wallet"},
			}), "no-provider")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
			}
			_, slug, _, _, initial, current := accountProviderState(t, h, "no-provider")
			if slug != tc.wantSlug {
				t.Fatalf("provider_slug = %q, want %q", slug, tc.wantSlug)
			}
			if initial != 2500 || current != 2500 {
				t.Fatalf("saldos = %d/%d, want 2500/2500", initial, current)
			}
		})
	}

	// Instituição personalizada sem nome continua rejeitada, sem tocar a conta.
	h := newConfiguracoesRenderTestHandler(t)
	insertInstitutionTestAccount(t, h, "custom-needs-name", h.WorkspaceID, "Carteira", "WALLET", "custom", 2500)
	rr := httptest.NewRecorder()
	h.HandleContasInlineSave(rr, formPost("/configuracoes/contas/custom-needs-name/salvar", url.Values{
		"name": {"Carteira"}, "type": {"WALLET"}, "color": {"#6B7280"}, "provider_slug": {"custom"},
	}), "custom-needs-name")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if _, slug, _, _, _, _ := accountProviderState(t, h, "custom-needs-name"); slug != "custom" {
		t.Fatalf("provider_slug = %q, want custom", slug)
	}
}

// T10 — o formulário de edição abre com a instituição atual selecionada.
func TestAccountEditFormRendersCurrentInstitutionSelected(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stored      string
		wantOption  string
		wantPressed string
	}{
		{name: "Stripe", stored: "stripe", wantOption: `<option value="stripe" selected>Stripe</option>`, wantPressed: `data-provider-slug="stripe" data-provider-name="Stripe" data-provider-logo="institutions/stripe.png?v=test" data-provider-mark="Stripe" data-provider-color="#635BFF" data-provider-search-text="Stripe" aria-pressed="true"`},
		{name: "Personalizada", stored: "custom", wantOption: `<option value="custom" selected>Outra / Personalizada</option>`, wantPressed: `data-provider-slug="custom" data-provider-name="Outra / Personalizada" data-provider-custom-choice aria-pressed="true"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newConfiguracoesRenderTestHandler(t)
			insertInstitutionTestAccount(t, h, "form-target", h.WorkspaceID, "Conta Principal", "CHECKING", tc.stored, 139931)

			rr := httptest.NewRecorder()
			h.HandleContasInlineForm(rr, httptest.NewRequest(http.MethodGet, "/configuracoes/contas/form-target/formulario", nil), "form-target")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			body := rr.Body.String()
			for _, want := range []string{
				`data-provider-select`,
				`name="provider_slug" data-provider-value`,
				`data-provider-open`,
				`data-provider-dialog`,
				`name="provider_custom_name"`,
				tc.wantOption,
			} {
				assertContains(t, body, want)
			}
			assertContains(t, body, tc.wantPressed)
			if strings.Contains(body, `<option value="" selected>`) {
				t.Fatal("formulário de edição não pode abrir sem instituição selecionada")
			}
		})
	}
}
