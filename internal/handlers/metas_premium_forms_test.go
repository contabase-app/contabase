package handlers

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// M6 — Formulários e bottom sheets de escrita de /metas.
//
// Contratos cobertos:
//   - o sheet de limite/reserva usa diálogo nomeado, cabeçalho, corpo rolável e
//     rodapé fixo, sem redefinir componentes globais;
//   - ids, names, hx-*, mascaras e campos opcionais existentes permanecem;
//   - aporte/resgate mantêm endpoint, payload e semântica financeira;
//   - valores exibidos continuam marcados com data-sensitive;
//   - exclusão continua usando hx-confirm + hx-delete, sem exclusão imediata.

func renderMetasFormForTest(t *testing.T, data MetaFormData) string {
	t.Helper()
	tpl := testMetasFormTemplates(t)
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "metas-form", data); err != nil {
		t.Fatalf("render metas-form: %v", err)
	}
	return out.String()
}

func renderMetasSheetForTest(t *testing.T, name string, data any) string {
	t.Helper()
	tpl := testMetasPageTemplates(t)
	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, name, data); err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	return out.String()
}

func tagContaining(body, needle, tag string) string {
	idx := strings.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(body[:idx], "<"+tag)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[idx:], ">")
	if end < 0 {
		return ""
	}
	return body[start : idx+end+1]
}

func inputTagContaining(body, needle string) string {
	return tagContaining(body, needle, "input")
}

func duplicateIDs(body string) []string {
	matches := regexp.MustCompile(`(?:^|\s)id="([^"]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	var duplicates []string
	for _, match := range matches {
		if seen[match[1]] {
			duplicates = append(duplicates, match[1])
		}
		seen[match[1]] = true
	}
	return duplicates
}

func TestMetasPremiumLimitFormPreservesContractAndSheetComposition(t *testing.T) {
	body := renderMetasFormForTest(t, MetaFormData{
		Mode:           "create",
		OpenTab:        "limite",
		CategoryID:     "cat-1",
		CategoryName:   "Alimentação",
		CategoryIcon:   "utensils",
		CategoryColor:  "#8b5cf6",
		MaxAmountInput: "1.800,00",
	})

	for _, expected := range []string{
		`data-metas-sheet`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="meta-sheet-title"`,
		`aria-describedby="meta-sheet-description"`,
		`metas-sheet__body`,
		`metas-sheet__footer`,
		`metas-sheet__close`,
		`Fechar formulário de limite`,
		`data-meta-form-root`,
		`data-metas-form-panel`,
		`hx-post="/metas/limite"`,
		`hx-swap="none"`,
		`hx-sync="this:drop"`,
		`id="meta-limite-form"`,
		`name="limit_id"`,
		`name="tipo" value="EXPENSE"`,
		`name="category_id"`,
		`name="max_amount_monthly"`,
		`name="icon_key"`,
		`id="categoriaPicker_limite"`,
		`id="categoriaId_limite"`,
		`id="categoriaNome_limite"`,
		`id="categoriaIcon_limite"`,
		`id="categoriaIconWrap_limite"`,
		`data-meta-category-picker`,
		`data-money-mask`,
		`value="R$ 1.800,00"`,
		`data-meta-create-submit`,
		`data-meta-submit-label`,
		`Criar limite`,
		`Teto de gasto do mês; não é saldo em conta.`,
		`data-metas-form-initial`,
		`>Limites<`,
		`>Reservas<`,
	} {
		assertContains(t, body, expected)
	}

	// A categoria é o foco inicial na criação do limite; o valor é o foco na edição.
	pickerTag := tagContaining(body, `data-meta-category-picker`, "button")
	if !strings.Contains(pickerTag, `data-metas-form-initial`) {
		t.Fatal("create limit must focus the category picker")
	}
	amountTag := inputTagContaining(body, `id="limiteMaxAmount"`)
	if strings.Contains(amountTag, `data-metas-form-initial`) {
		t.Fatal("create limit must not mark the amount as the initial focus target")
	}

	for _, forbidden := range []string{
		`bg-black/70`,
		`shadow-2xl`,
		`text-rose-100`,
		`!important`,
		`Nova Meta`,
		`cb-input h-12`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("limit form must not retain legacy composition %q", forbidden)
		}
	}
}

func TestMetasPremiumReserveFormPreservesOptionalFieldsAndDeleteGuard(t *testing.T) {
	body := renderMetasFormForTest(t, MetaFormData{
		Mode:                  "edit",
		OpenTab:               "caixinha",
		BoxID:                 "box-1",
		Name:                  "Aposentadoria",
		CategoryID:            "cat-1",
		CategoryName:          "Aposentadoria",
		CategoryIcon:          "piggy-bank",
		TargetAmountInput:     "5.000.000,00",
		MonthlyRechargeInput:  "18.000,00",
		TargetMonthInput:      "2040-12",
		MonthlyYieldRateInput: "0,8",
		BalanceLabel:          "R$ 184.912,09",
	})

	for _, expected := range []string{
		`id="meta-caixinha-form"`,
		`hx-post="/metas/caixinha"`,
		`hx-swap="none"`,
		`hx-sync="this:drop"`,
		`name="box_id" value="box-1"`,
		`name="name"`,
		`name="target_amount"`,
		`name="monthly_recharge"`,
		`name="target_month"`,
		`name="monthly_yield_rate"`,
		`name="category_id"`,
		`value="R$ 5.000.000,00"`,
		`value="R$ 18.000,00"`,
		`Valor alvo (opcional)`,
		`Recarga mensal (opcional)`,
		`Data alvo (opcional)`,
		`Sem alvo, a reserva não exibe barra de progresso.`,
		`Aporte mensal programado; não movimenta contas agora.`,
		`Opcional; sem data, a reserva fica sem prazo.`,
		`Saldo atual`,
		`class="metas-form__summary-value" data-sensitive>R$ 184.912,09`,
		`data-metas-form-initial`,
		`hx-delete="/metas/caixinha/box-1"`,
		`hx-target="closest .sheet-in"`,
		`hx-swap="delete"`,
		`hx-confirm="Tem certeza que deseja excluir esta reserva?"`,
		`metas-form__danger-zone`,
		`A exclusão pede confirmação.`,
		`form="meta-caixinha-form"`,
		`>Salvar<`,
	} {
		assertContains(t, body, expected)
	}

	for _, optional := range []string{`name="target_amount"`, `name="monthly_recharge"`, `name="target_month"`, `name="monthly_yield_rate"`} {
		tag := inputTagContaining(body, optional)
		if tag == "" {
			t.Fatalf("missing optional input %s", optional)
		}
		if strings.Contains(tag, "required") {
			t.Fatalf("optional input %s must not become required: %s", optional, tag)
		}
	}

	if strings.Contains(body, `name="account_id"`) {
		t.Fatal("metas must not gain an account field in M6")
	}
	if strings.Contains(body, `aria-label="R$`) || strings.Contains(body, `title="R$`) {
		t.Fatal("form must not expose monetary values in aria-label/title")
	}
}

func TestMetasPremiumCreateReserveStartsOnReserveTab(t *testing.T) {
	body := renderMetasFormForTest(t, MetaFormData{
		Mode:    "create",
		OpenTab: "caixinha",
	})

	assertContains(t, body, `data-open-tab="caixinha"`)
	assertContains(t, body, `id="meta-caixinha-form" class="metas-form"`)
	assertContains(t, body, `id="meta-limite-form" class="metas-form hidden"`)
	assertContains(t, body, `id="tab-caixinha" class="ui-tab metas-form__tab is-active"`)
	assertContains(t, body, `id="tab-limite" class="ui-tab metas-form__tab"`)
	assertContains(t, body, `aria-pressed="true" aria-controls="meta-caixinha-form"`)
	assertContains(t, body, `aria-pressed="false" aria-controls="meta-limite-form"`)
	assertContains(t, body, `>Nova reserva<`)
	assertContains(t, body, `data-label-caixinha="Criar reserva"`)
	assertContains(t, body, `form="meta-caixinha-form"`)
	assertContains(t, body, `>Criar reserva<`)

	nameTag := inputTagContaining(body, `id="caixinhaName"`)
	if !strings.Contains(nameTag, `data-metas-form-initial`) {
		t.Fatal("create reserve must focus the reserve name")
	}
	amountTag := inputTagContaining(body, `id="limiteMaxAmount"`)
	if strings.Contains(amountTag, `data-metas-form-initial`) {
		t.Fatal("hidden limit amount must not be the initial focus target")
	}
}

func TestMetasPremiumAporteSheetPreservesMoneyContract(t *testing.T) {
	body := renderMetasSheetForTest(t, "caixinha-aporte-sheet", BoxSheetData{
		BoxID:        "box-1",
		BoxName:      "Aposentadoria",
		CategoryName: "Aposentadoria",
		Balance:      MoneyDisplay{Reais: "184.912", Cents: ",09"},
		Target:       MoneyDisplay{Reais: "5.000.000", Cents: ",00"},
	})

	for _, expected := range []string{
		`data-metas-sheet`,
		`data-metas-money-sheet="aporte"`,
		`role="dialog"`,
		`aria-modal="true"`,
		`aria-labelledby="aporte-sheet-title"`,
		`aria-describedby="aporte-sheet-description"`,
		`id="aporte-form"`,
		`hx-post="/metas/caixinha/aporte"`,
		`hx-swap="none"`,
		`hx-sync="this:drop"`,
		`data-metas-money-form`,
		`data-metas-form-kind="aporte"`,
		`name="box_id" value="box-1"`,
		`name="_from_sheet" value="1"`,
		`id="aporteAmount"`,
		`name="amount"`,
		`data-money-mask`,
		`inputmode="decimal"`,
		`metas-sheet__footer`,
		`form="aporte-form"`,
		`Confirmar aporte`,
		`O valor fica reservado; não sai da conta até o lançamento da despesa.`,
		`metas-form__summary-value--money" data-sensitive>R$ 184.912,09`,
		`data-metas-form-initial`,
	} {
		assertContains(t, body, expected)
	}

	for _, forbidden := range []string{`!important`, `!bg-`, `style=`, `shadow-2xl`, `text-rose-100`, `bg-amber-500`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("aporte sheet must not retain legacy composition %q", forbidden)
		}
	}
	if strings.Contains(body, `aria-label="R$`) || strings.Contains(body, `title="R$`) {
		t.Fatal("aporte sheet must not expose monetary values in aria-label/title")
	}
}

func TestMetasPremiumResgateSheetPreservesWarningAndContracts(t *testing.T) {
	body := renderMetasSheetForTest(t, "caixinha-resgate-sheet", BoxSheetData{
		BoxID:               "box-1",
		BoxName:             "Aposentadoria",
		Balance:             MoneyDisplay{Reais: "184.912", Cents: ",09"},
		AvailableForRelease: MoneyDisplay{Reais: "184.912", Cents: ",09"},
		ErrorMessage:        "valor acima do saldo reservado atual",
	})

	for _, expected := range []string{
		`data-metas-money-sheet="resgate"`,
		`id="resgate-form"`,
		`hx-post="/metas/caixinha/resgate"`,
		`hx-sync="this:drop"`,
		`data-metas-form-kind="resgate"`,
		`id="resgateAmount"`,
		`name="amount"`,
		`data-money-mask`,
		`metas-sheet__footer`,
		`form="resgate-form"`,
		`Confirmar liberação`,
		`A liberação reduz o saldo reservado e devolve o valor ao saldo livre das contas.`,
		`metas-form__summary-value--money" data-sensitive>R$ 184.912,09`,
		`metas-form__notice--warning`,
		`O valor volta ao saldo livre e não altera lançamentos.`,
		`role="alert"`,
		`tabindex="-1"`,
		`valor acima do saldo reservado atual`,
	} {
		assertContains(t, body, expected)
	}

	if strings.Contains(body, `!important`) || strings.Contains(body, `!bg-`) {
		t.Fatal("resgate primary action must not rely on !important")
	}
	if strings.Contains(body, `background:`) || strings.Contains(body, `style=`) {
		t.Fatal("resgate sheet must not carry inline styles")
	}
}

func TestMetasPremiumFormsCSSStaysScopedAndTokenized(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	start := strings.Index(css, "/* === M6")
	if start < 0 {
		t.Fatal("metas.css missing M6 section")
	}
	section := css[start:]

	for _, expected := range []string{
		`.metas-sheet__dialog`,
		`.metas-sheet__body`,
		`.metas-sheet__footer`,
		`.metas-form__control`,
		`.metas-form__picker`,
		`.metas-form__danger-action`,
		`--ui-field-focus-border`,
		`html[data-input-modality="keyboard"]`,
		`outline: 2px solid var(--ui-focus)`,
		`env(safe-area-inset-bottom)`,
		`max-height: 92dvh`,
		`[aria-invalid="true"]`,
		`@media (prefers-reduced-motion: reduce)`,
	} {
		if !strings.Contains(section, expected) {
			t.Fatalf("metas.css M6 section missing %q", expected)
		}
	}

	for _, forbidden := range []string{
		`!important`,
		`#`,
		`rgb(`,
		`rgba(`,
		".ui-input {",
		".ui-btn {",
		".ui-row {",
		".ui-list-panel {",
	} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("metas.css M6 section must not contain %q", forbidden)
		}
	}
}

func TestMetasCSSNoLongerRedefinesCanonicalCBBridge(t *testing.T) {
	css := readTestFile(t, "assets/css/metas.css")
	for _, forbidden := range []string{
		`--cb-accent-solid:`,
		`--cb-accent-hover:`,
		`--cb-border-focus:`,
		`--cb-surface-overlay:`,
	} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("metas.css must consume the canonical --ui-* bridge, not redefine %q", forbidden)
		}
	}
	if strings.Contains(css, ".cb-v2-metas-main {\n  --cb-") {
		t.Fatal("metas.css must not keep the legacy page-scoped --cb-* bridge")
	}
}

func TestMetasPremiumFormIDsStayUnique(t *testing.T) {
	cases := []struct {
		name string
		data MetaFormData
	}{
		{
			name: "create limit",
			data: MetaFormData{Mode: "create", OpenTab: "limite"},
		},
		{
			name: "edit reserve",
			data: MetaFormData{Mode: "edit", OpenTab: "caixinha", BoxID: "box-1", Name: "Aposentadoria"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := renderMetasFormForTest(t, tc.data)
			if duplicates := duplicateIDs(body); len(duplicates) > 0 {
				t.Fatalf("duplicate ids in metas form: %v", duplicates)
			}
		})
	}
}
