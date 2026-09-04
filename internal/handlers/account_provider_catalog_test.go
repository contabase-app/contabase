package handlers

import (
	"strings"
	"testing"
)

func TestAccountProviderMark(t *testing.T) {
	tests := []struct {
		name         string
		providerSlug string
		accountName  string
		expected     string
	}{
		// Catálogo conhecido
		{"Nubank (conhecido)", "nubank", "Minha Conta Nubank", "Nu"},
		{"Caixa (conhecido)", "caixa", "Caixa Econômica Federal", "CAIXA"},
		{"Inter (conhecido)", "inter", "Inter | Vitor", "Inter"},
		{"XP (novo catálogo)", "xp", "XP Investimentos", "XP"},
		{"PagBank (novo catálogo)", "pagbank", "PagSeguro", "Pag"},

		// Fallbacks custom
		{"Dinheiro genérico (deve retornar vazio para forçar icone wallet)", "custom", "Dinheiro", ""},
		{"Dinheiro maiúsculo", "custom", "DINHEIRO", ""},
		{"Nome curto até 5 caracteres", "custom", "Banco", "Banco"},
		{"Nome curto com acentos (5 ou menos visual)", "custom", "Itaú ", "Itaú"},
		{"Palavra única longa (> 5 caracteres)", "custom", "Banrisul", "B"},
		{"Duas palavras médias", "custom", "Cresol Unidade", "CU"},
		{"Múltiplas palavras", "custom", "Minha Conta de Teste Super", "MCDTS"},
		{"Múltiplas palavras acima de 5", "custom", "Banco Regional de Desenvolvimento do Extremo Sul", "BRDDD"}, // Limitado a 5 caracteres
		{"Espaços extras", "custom", "  Super   Bank  ", "SB"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := accountProviderMark(tc.providerSlug, tc.accountName)
			if got != tc.expected {
				t.Errorf("accountProviderMark(%q, %q) = %q, expected %q", tc.providerSlug, tc.accountName, got, tc.expected)
			}
		})
	}
}

func TestAccountProviderLogoPath(t *testing.T) {
	for slug, want := range map[string]string{
		"bb":          "institutions/bb.png",
		"bradesco":    "institutions/bradesco.png",
		"c6":          "institutions/c6.png",
		"caixa":       "institutions/caixa.png",
		"inter":       "institutions/inter.png",
		"itau":        "institutions/itau.png",
		"mercadopago": "institutions/mercadopago.png",
		"nubank":      "institutions/nubank.png",
		"pagbank":     "institutions/pagbank.png",
		"picpay":      "institutions/picpay.png",
		"santander":   "institutions/santander.png",
		"xp":          "institutions/xp.png",
	} {
		if got := accountProviderLogoPath(slug); got != want {
			t.Fatalf("%s logo path = %q, want %q", slug, got, want)
		}
	}
	if got := accountProviderLogoPath("custom"); got != "" {
		t.Fatalf("custom provider logo path = %q, want empty", got)
	}
	if got := accountProviderLogoPath("unknown"); got != "" {
		t.Fatalf("unknown provider logo path = %q, want empty", got)
	}
	if got := accountProviderLogoPathForAccount("custom", "Itaú | Vitor"); got != "institutions/itau.png" {
		t.Fatalf("legacy Itaú logo path = %q", got)
	}
	if got := accountProviderLogoPathForAccount("custom", "Dinheiro"); got != "" {
		t.Fatalf("custom wallet logo path = %q, want empty", got)
	}
}

func TestResolveAccountProviderInputRequiresExplicitSelection(t *testing.T) {
	_, _, _, err := resolveAccountProviderInput("", "", "")
	if err == nil || !strings.Contains(err.Error(), "selecione uma instituição") {
		t.Fatalf("empty provider error = %v", err)
	}
}

func TestIdentifyAlias(t *testing.T) {
	tests := []struct {
		accountName string
		expected    string
	}{
		{"Inter | Vitor", "inter"},
		{"Inter Pessoa Jurídica", "inter"},
		{"Caixa Econômica", "caixa"},
		{"caixa Federal", "caixa"},
		{"Mercado Pago", "mercadopago"},
		{"MercadoPago", "mercadopago"},
		{"Nubank Principal", "nubank"},
		{"XP Investimentos", "xp"},
		{"PagBank do José", "pagbank"},
		{"Cooperativa de Crédito", ""},
		{"Cresol", ""},
	}

	for _, tc := range tests {
		t.Run(tc.accountName, func(t *testing.T) {
			got := identifyAlias(tc.accountName)
			if got != tc.expected {
				t.Errorf("identifyAlias(%q) = %q, expected %q", tc.accountName, got, tc.expected)
			}
		})
	}
}

func TestDeterministicColor(t *testing.T) {
	// Garante que é estável e retorna do array de cores
	color1 := deterministicColor("Minha Conta")
	color2 := deterministicColor("Minha Conta")
	color3 := deterministicColor("Outra Conta")

	if color1 != color2 {
		t.Errorf("deterministicColor should be stable, got %s and %s for the same name", color1, color2)
	}

	if color1 == "" || color1[0] != '#' || len(color1) != 7 {
		t.Errorf("deterministicColor returned invalid hex: %s", color1)
	}

	if color3 == "" || color3[0] != '#' || len(color3) != 7 {
		t.Errorf("deterministicColor returned invalid hex: %s", color3)
	}
}
