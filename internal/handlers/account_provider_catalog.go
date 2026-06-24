package handlers

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/contabase-app/contabase/internal/models"
)

const customAccountProviderSlug = "custom"

type AccountProviderOption struct {
	Slug  string
	Name  string
	Color string
	Icon  string
	Mark  string
}

var accountProviderCatalog = []AccountProviderOption{
	{Slug: "nubank", Name: "Nubank", Color: "#820AD1", Icon: "landmark", Mark: "Nu"},
	{Slug: "itau", Name: "Itaú", Color: "#EC7000", Icon: "building-2", Mark: "Itaú"},
	{Slug: "inter", Name: "Inter", Color: "#FF6B00", Icon: "building-2", Mark: "Inter"},
	{Slug: "bb", Name: "Banco do Brasil", Color: "#0056A4", Icon: "building-2", Mark: "BB"},
	{Slug: "caixa", Name: "Caixa Econômica", Color: "#005CA9", Icon: "building-2", Mark: "CAIXA"},
	{Slug: "picpay", Name: "PicPay", Color: "#11C76F", Icon: "wallet-cards", Mark: "P"},
	{Slug: "mercadopago", Name: "Mercado Pago", Color: "#00A6FF", Icon: "wallet-cards", Mark: "MP"},
	{Slug: "bradesco", Name: "Bradesco", Color: "#CC092F", Icon: "building-2", Mark: "B"},
	{Slug: "santander", Name: "Santander", Color: "#EC0000", Icon: "building-2", Mark: "S"},
	{Slug: "c6", Name: "C6 Bank", Color: "#111111", Icon: "credit-card", Mark: "C6"},
	{Slug: "xp", Name: "XP Investimentos", Color: "#000000", Icon: "building-2", Mark: "XP"},
	{Slug: "pagbank", Name: "PagBank", Color: "#FFE72D", Icon: "building-2", Mark: "Pag"},
}

var (
	hexColorPattern       = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	accountProviderBySlug = func() map[string]AccountProviderOption {
		out := make(map[string]AccountProviderOption, len(accountProviderCatalog))
		for _, item := range accountProviderCatalog {
			out[item.Slug] = item
		}
		return out
	}()
)

var deterministicColors = []string{
	"#3B82F6", "#10B981", "#F59E0B", "#8B5CF6", "#EC4899", "#6366F1", "#14B8A6", "#F43F5E",
}

func accountProviderOptions() []AccountProviderOption {
	out := make([]AccountProviderOption, len(accountProviderCatalog))
	copy(out, accountProviderCatalog)
	return out
}

func accountProviderBySlugOrCustom(raw string) (AccountProviderOption, bool) {
	slug := normalizeAccountProviderSlug(raw)
	if slug == customAccountProviderSlug {
		return AccountProviderOption{}, false
	}
	item, ok := accountProviderBySlug[slug]
	return item, ok
}

// identifyAlias check if a custom name matches one of the known banks.
func identifyAlias(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(lower, "inter") {
		return "inter"
	}
	if strings.Contains(lower, "caixa") {
		return "caixa"
	}
	if strings.Contains(lower, "mercado pago") || strings.Contains(lower, "mercadopago") {
		return "mercadopago"
	}
	if strings.Contains(lower, "nubank") {
		return "nubank"
	}
	if strings.Contains(lower, "xp") {
		return "xp"
	}
	if strings.Contains(lower, "pagbank") {
		return "pagbank"
	}
	return ""
}

func normalizeAccountProviderSlug(raw string) string {
	slug := strings.TrimSpace(strings.ToLower(raw))
	if slug == customAccountProviderSlug {
		return customAccountProviderSlug
	}
	if _, ok := accountProviderBySlug[slug]; ok {
		return slug
	}
	return customAccountProviderSlug
}

func normalizeHexColor(raw, fallback string) string {
	color := strings.TrimSpace(raw)
	if !hexColorPattern.MatchString(color) {
		color = fallback
	}
	if !hexColorPattern.MatchString(color) {
		color = "#6B7280"
	}
	return strings.ToUpper(color)
}

func deterministicColor(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "#6B7280"
	}
	hash := sha256.Sum256([]byte(name))
	idx := int(hash[0]) % len(deterministicColors)
	return deterministicColors[idx]
}

func resolveAccountProviderInput(rawSlug, rawCustomName, rawColor string) (name, providerSlug, color string, err error) {
	providerSlug = normalizeAccountProviderSlug(rawSlug)
	if provider, ok := accountProviderBySlugOrCustom(providerSlug); ok {
		return provider.Name, provider.Slug, provider.Color, nil
	}

	name = strings.TrimSpace(rawCustomName)
	if name == "" {
		return "", "", "", fmt.Errorf("nome da instituição é obrigatório para conta personalizada")
	}

	alias := identifyAlias(name)
	if alias != "" {
		if provider, ok := accountProviderBySlugOrCustom(alias); ok {
			return provider.Name, provider.Slug, provider.Color, nil
		}
	}

	color = strings.TrimSpace(rawColor)
	if !hexColorPattern.MatchString(color) {
		color = deterministicColor(name)
	}

	return name, customAccountProviderSlug, normalizeHexColor(color, "#6B7280"), nil
}

func accountVisualByProvider(providerSlug, accType string) string {
	if normalizeAccountProviderSlug(providerSlug) == customAccountProviderSlug {
		if accType == models.AccountTypeCreditCard {
			return "credit-card"
		}
		return "landmark"
	}
	if accType == models.AccountTypeCreditCard {
		return "credit-card"
	}
	if provider, ok := accountProviderBySlugOrCustom(providerSlug); ok && provider.Icon != "" {
		return provider.Icon
	}
	return "wallet"
}

func accountProviderMark(providerSlug, accountName string) string {
	if provider, ok := accountProviderBySlugOrCustom(providerSlug); ok && strings.TrimSpace(provider.Mark) != "" {
		return provider.Mark
	}

	name := strings.TrimSpace(accountName)
	if name == "" {
		return ""
	}
	if strings.ToLower(name) == "dinheiro" {
		return ""
	}

	words := strings.Fields(name)

	// se o nome tiver até 5 caracteres, usar o próprio nome
	if len([]rune(name)) <= 5 {
		return name
	}

	// se tiver múltiplas palavras, usar as iniciais das palavras
	if len(words) > 1 {
		var initials string
		for _, w := range words {
			runes := []rune(w)
			if len(runes) > 0 {
				initials += string(runes[0])
			}
			if len([]rune(initials)) >= 5 {
				break
			}
		}
		return strings.ToUpper(initials)
	}

	// se for uma única palavra com mais de 5 caracteres, usar apenas a primeira letra
	runes := []rune(name)
	if len(runes) > 0 {
		return strings.ToUpper(string(runes[0]))
	}
	return ""
}
