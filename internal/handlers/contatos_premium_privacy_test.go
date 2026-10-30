package handlers

import (
	"bytes"
	"strings"
	"testing"
)

// C2 — Fundação Premium e privacidade de /contatos.
//
// Contratos cobertos aqui:
//   - o envelope Premium (premium.css + data-ui="premium") carrega antes do
//     CSS da página;
//   - o cabeçalho adota ui-page-header/ui-eyebrow/ui-page-title/
//     ui-page-description e mantém eyebrow, título, descrição e ação;
//   - o controle de privacidade canônico fica no header, fora do grupo que
//     esconde "Novo contato" no mobile;
//   - CPF/CNPJ, e-mail e telefone continuam marcados com data-sensitive e o
//     e-mail não expõe o endereço em title/atributo acessível;
//   - busca e estado vazio adotam os componentes compartilhados sem alterar
//     os contratos HTMX do backend;
//   - contatos.css cobre o modo privacidade no padrão de Metas/Relatórios,
//     sem mascarar inputs editáveis.

func contatosPremiumFixture() ContatosData {
	return ContatosData{
		Title:               "Contatos",
		UserName:            "Vitor Ferreira",
		UserInitials:        "VF",
		ActiveWorkspaceName: "Empresa",
		IsBusiness:          true,
		Query:               "ana",
		Tipo:                "client",
		TipoLabel:           "Clientes",
		Contatos: []ContatoRow{
			{
				ID:             "contact-1",
				Name:           "Ana Lima",
				Initials:       "AL",
				Type:           "client",
				TypeLabel:      "Cliente",
				CustomClientID: "CLI-001",
				Document:       "123.456.789-00",
				Email:          "ana@example.com",
				Phone:          "(11) 99999-9999",
				CreatedAt:      "10/06/2026",
				CanUpdate:      true,
				CanDelete:      true,
			},
			{
				ID:             "contact-2",
				Name:           "Fornecedora Beta",
				Initials:       "FB",
				Type:           "vendor",
				TypeLabel:      "Fornecedor",
				CustomClientID: "CLI-002",
				Document:       "12.345.678/0001-90",
				Email:          "beta@example.com",
				Phone:          "(21) 98888-7777",
				CreatedAt:      "11/06/2026",
				CanUpdate:      true,
				CanDelete:      true,
			},
		},
	}
}

func TestContatosPageAdoptsPremiumFoundation(t *testing.T) {
	tpl := testContatosRenderTemplates(t)

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contatos-page", contatosPremiumFixture()); err != nil {
		t.Fatalf("render contatos-page: %v", err)
	}
	body := out.String()

	for _, expected := range []string{
		`data-ui="premium"`,
		`/assets/css/premium.css?v=test`,
		`cb-v2-contatos-header ui-page-header`,
		`class="ui-eyebrow">Relacionamentos`,
		`class="ui-page-title">Contatos`,
		`class="ui-page-description">Organize clientes e fornecedores`,
		`data-privacy-toggle`,
		`aria-pressed="false"`,
		`aria-label="Ocultar valores financeiros"`,
		`data-tooltip-hover data-tooltip-text="Ocultar valores financeiros"`,
		`data-lucide="eye" data-privacy-icon`,
		`data-contact-create`,
		`hx-get="/contatos/novo`,
		`hx-target="#bottom-sheet-container"`,
		`cb-v2-contatos-create ui-btn ui-btn--primary`,
	} {
		assertContains(t, body, expected)
	}

	// premium.css precisa carregar antes do CSS da própria página.
	if strings.Index(body, "/assets/css/premium.css?v=test") > strings.Index(body, "/assets/css/contatos.css?v=test") {
		t.Fatal("premium.css must load before contatos.css")
	}
	// O headline legado não pode continuar definindo o cabeçalho.
	headerStart := strings.Index(body, `<header class="cb-v2-contatos-header ui-page-header">`)
	headerEnd := headerStart + strings.Index(body[headerStart:], `</header>`)
	header := body[headerStart:headerEnd]
	for _, legacy := range []string{`cb-v2-eyebrow">Relacionamentos`, `<h1>Contatos</h1>`, `cb-button cb-button-primary`} {
		if strings.Contains(header, legacy) {
			t.Fatalf("contatos header must not keep legacy markup %q", legacy)
		}
	}
}

func TestContatosPrivacyToggleIsNotHiddenWithMobilePrimaryAction(t *testing.T) {
	tpl := testContatosRenderTemplates(t)

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contatos-page", contatosPremiumFixture()); err != nil {
		t.Fatalf("render contatos-page: %v", err)
	}
	body := out.String()

	privacy := strings.Index(body, `data-privacy-toggle`)
	create := strings.Index(body, `data-contact-create`)
	if privacy < 0 || create < 0 {
		t.Fatal("header must render both the privacy toggle and the primary action")
	}
	// Os dois botões vivem no mesmo grupo de ações; apenas o primário é
	// escondido no breakpoint mobile, então o toggle continua acessível.
	group := body[strings.Index(body, `class="flex items-center gap-2 shrink-0"`):]
	group = group[:strings.Index(group, `</header>`)]
	assertContains(t, group, `data-privacy-toggle`)
	assertContains(t, group, `cb-v2-contatos-create`)

	css := readTestFile(t, "assets/css/contatos.css")
	assertContains(t, css, `.cb-v2-contatos-privacy {
    width: 44px;
    height: 44px;
    min-height: 44px;
    padding: 0;
  }`)
	assertContains(t, css, `.cb-v2-contatos-create { display: none; }`)
	if strings.Contains(css, `.cb-v2-contatos-privacy { display: none; }`) {
		t.Fatal("privacy toggle must stay visible on mobile")
	}
}

func TestContatosMarksSensitiveFieldsWithoutRevealingEmail(t *testing.T) {
	tpl := testContatosRenderTemplates(t)
	fixture := contatosPremiumFixture()

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contatos-list", fixture); err != nil {
		t.Fatalf("render contatos-list: %v", err)
	}
	body := out.String()

	// 2 contatos × (documento, e-mail, telefone) = 6 valores sensíveis.
	if got, want := strings.Count(body, `data-sensitive`), 6; got != want {
		t.Fatalf("contatos-list data-sensitive count = %d, want %d\nbody:\n%s", got, want, body)
	}
	for _, mark := range []string{
		`<dd data-sensitive>123.456.789-00</dd>`,
		`<dd data-sensitive>ana@example.com</dd>`,
		`<dd data-sensitive>(11) 99999-9999</dd>`,
		`<dd data-sensitive>12.345.678/0001-90</dd>`,
	} {
		assertContains(t, body, mark)
	}
	// Nenhum tooltip ou atributo acessível pode repetir o endereço sensível.
	if got := strings.Count(body, `ana@example.com`); got != 1 {
		t.Fatalf("e-mail must appear only as masked text, got %d occurrences\nbody:\n%s", got, body)
	}
	if strings.Contains(body, `title="ana@example.com"`) {
		t.Fatal("sensitive e-mail must not be exposed through the title attribute")
	}
	for _, forbidden := range []string{`aria-label="ana@example.com"`, `data-tooltip-text="ana@example.com"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sensitive e-mail must not be exposed in accessible attribute %q", forbidden)
		}
	}

	// Identificação e metadados operacionais permanecem legíveis.
	for _, preserved := range []string{
		`<h3>Ana Lima</h3>`,
		`<h3>Fornecedora Beta</h3>`,
		`ui-badge--success`,
		`ui-badge--warning`,
		"Cliente\n",
		"Fornecedor\n",
		`id="contato-contact-1"`,
		`>CLI-001<`,
		`aria-label="Editar Ana Lima"`,
	} {
		assertContains(t, body, preserved)
	}
	if strings.Contains(body, `data-sensitive>CLI-001<`) {
		t.Fatal("custom client id is an operational identifier and must not be masked")
	}
}

func TestContatosSearchKeepsHTMXContractOnPremiumComponent(t *testing.T) {
	tpl := testContatosRenderTemplates(t)

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contatos-list", contatosPremiumFixture()); err != nil {
		t.Fatalf("render contatos-list: %v", err)
	}
	body := out.String()

	for _, expected := range []string{
		`class="ui-search"`,
		`class="ui-input ui-input--search"`,
		`class="ui-search__icon"`,
		`name="q"`,
		`value="ana"`,
		`placeholder="Buscar por nome, documento ou telefone..."`,
		`aria-label="Buscar contatos"`,
		`hx-get="/contatos?tipo=client&partial=lista"`,
		`hx-target="#contatos-grid"`,
		`hx-select="#contatos-grid"`,
		`hx-swap="outerHTML"`,
		`hx-trigger="keyup delay:300ms changed, search"`,
	} {
		assertContains(t, body, expected)
	}
	if strings.Contains(body, `cb-v2-contatos-search`) {
		t.Fatal("search must use the canonical ui-search component")
	}
}

func TestContatosEmptyStateUsesPremiumComponent(t *testing.T) {
	tpl := testContatosRenderTemplates(t)

	render := func(q string) string {
		t.Helper()
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "contatos-list", ContatosData{
			Title:      "Contatos",
			Query:      q,
			Tipo:       "todos",
			Contatos:   nil,
			IsBusiness: true,
		}); err != nil {
			t.Fatalf("render empty contatos-list: %v", err)
		}
		return out.String()
	}

	empty := render("")
	for _, expected := range []string{
		`class="ui-empty" role="status"`,
		`class="ui-empty__icon" aria-hidden="true"`,
		`class="ui-empty__title">Nenhum contato encontrado`,
		`Use “Novo contato” para começar.`,
	} {
		assertContains(t, empty, expected)
	}
	if strings.Contains(empty, `cb-contact-empty`) {
		t.Fatal("empty state must use the canonical ui-empty component")
	}
	// Nenhum CTA novo: o estado vazio continua sendo apenas informativo.
	emptyState := empty[strings.Index(empty, `class="ui-empty"`):]
	if strings.Contains(emptyState, `<button`) || strings.Contains(emptyState, `hx-`) {
		t.Fatal("empty state must not introduce a new CTA or HTMX action")
	}

	searchEmpty := render("zzz")
	assertContains(t, searchEmpty, `Tente ajustar sua busca.`)
}

func TestContatosPreservesPermissionsAndHTMXContracts(t *testing.T) {
	tpl := testContatosRenderTemplates(t)

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "contatos-page", contatosPremiumFixture()); err != nil {
		t.Fatalf("render contatos-page: %v", err)
	}
	body := out.String()

	for _, expected := range []string{
		`hx-get="/contatos/novo?tipo=client"`,
		`hx-target="#bottom-sheet-container"`,
		`hx-get="/contatos?tipo=client&partial=lista"`,
		`hx-push-url="true"`,
		`hx-get="/contatos/contact-1"`,
		`/assets/js/contatos-modal.js?v=test`,
	} {
		assertContains(t, body, expected)
	}
	for _, legacy := range []string{`hx-delete="/contatos/contact-1"`, `id="contatos-modal"`, `openContatosModal`} {
		if strings.Contains(body, legacy) {
			t.Fatalf("legacy contact surface must be gone: %q", legacy)
		}
	}
	if strings.Contains(body, `hx-swap-oob`) {
		t.Fatal("page render must not inject OOB swaps")
	}
}

func TestContatosCSSPrivacyCoverageFollowsCanonicalPattern(t *testing.T) {
	css := readTestFile(t, "assets/css/contatos.css")

	for _, expected := range []string{
		`.privacy-enabled .cb-v2-contatos-main [data-sensitive]`,
		`:is(span, strong, p, small, div, dd)[data-sensitive]`,
		`content: "••••"`,
		`visibility: hidden`,
		`visibility: visible`,
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("contatos.css missing privacy coverage %q", expected)
		}
	}
	// O mascaramento não pode alcançar inputs editáveis.
	if strings.Contains(css, `input[data-sensitive]`) {
		t.Fatal("privacy rules must not mask editable inputs")
	}
	// O envelope premium precisa manter a busca e o vazio canônicos.
	if strings.Contains(css, `.cb-v2-contatos-search`) || strings.Contains(css, `.cb-contact-empty`) {
		t.Fatal("contatos.css must not keep bespoke search/empty styles")
	}
}
