// Package icons define o catálogo controlado de ícones do ContaBase.
//
// O banco guarda apenas uma chave estável (icon_key); a resolução para o
// componente visual acontece no frontend, que já carrega o Lucide
// (assets/js/lucide.min.js). Nenhum SVG, HTML ou JSX é persistido.
//
// Este pacote é a única fonte da verdade das chaves aceitas. Ele é usado por:
//   - migration/backfill de categorias;
//   - seeder de workspaces novos;
//   - validação nos handlers de Categoria, Meta e Limite;
//   - funções de template do seletor de ícones.
package icons

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// DefaultKey é o ícone neutro aplicado quando não há chave utilizável.
// Coincide com o default histórico da coluna categories.icon.
const DefaultKey = "tag"

// FallbackGoal é o ícone de uma Meta/reserva que não tem ícone próprio nem
// categoria que o defina. Manter a reserva reconhecível como poupança é uma
// decisão de produto já refletida nas notificações de caixinha.
const FallbackGoal = "piggy-bank"

// Option descreve uma entrada do catálogo.
type Option struct {
	Key   string
	Label string
	Group string
}

// Group é um bloco do catálogo exibido pelo seletor de ícones.
type Group struct {
	Name    string
	Options []Option
}

// catalog é a lista curada de ícones oferecidos pelo ContaBase.
// Toda chave precisa existir no bundle Lucide versionado em
// assets/js/lucide.min.js — há teste automatizado que garante isso.
var catalog = []Option{
	// Alimentação
	{"utensils", "Alimentação", "Alimentação"},
	{"coffee", "Café e lanches", "Alimentação"},
	{"pizza", "Pizza", "Alimentação"},
	{"salad", "Refeição leve", "Alimentação"},
	{"beer", "Bebidas", "Alimentação"},
	{"cake", "Doces e festas", "Alimentação"},
	{"ice-cream-cone", "Sorvetes", "Alimentação"},

	// Moradia e contas
	{"house", "Moradia", "Moradia e contas"},
	{"lightbulb", "Luz", "Moradia e contas"},
	{"zap", "Energia elétrica", "Moradia e contas"},
	{"droplets", "Água", "Moradia e contas"},
	{"flame", "Gás", "Moradia e contas"},
	{"wifi", "Internet", "Moradia e contas"},
	{"smartphone", "Telefone e celular", "Moradia e contas"},
	{"wrench", "Manutenção", "Moradia e contas"},
	{"hammer", "Obras e reforma", "Moradia e contas"},
	{"leaf", "Jardinagem", "Moradia e contas"},
	{"truck", "Frete e mudança", "Moradia e contas"},
	{"shield", "Emergência", "Moradia e contas"},
	{"umbrella", "Seguros", "Moradia e contas"},

	// Transporte
	{"car", "Carro", "Transporte"},
	{"bus", "Transporte público", "Transporte"},
	{"bike", "Bicicleta e delivery", "Transporte"},
	{"fuel", "Combustível", "Transporte"},
	{"train", "Trem e metrô", "Transporte"},
	{"plane", "Viagem", "Transporte"},
	{"footprints", "Deslocamentos", "Transporte"},

	// Compras e vestuário
	{"shopping-cart", "Mercado", "Compras e vestuário"},
	{"shopping-bag", "Compras", "Compras e vestuário"},
	{"shirt", "Roupas", "Compras e vestuário"},
	{"gift", "Presentes", "Compras e vestuário"},
	{"store", "Loja", "Compras e vestuário"},
	{"package", "Encomendas", "Compras e vestuário"},

	// Saúde e bem-estar
	{"heart-pulse", "Saúde", "Saúde e bem-estar"},
	{"stethoscope", "Consultas", "Saúde e bem-estar"},
	{"pill", "Farmácia", "Saúde e bem-estar"},
	{"syringe", "Exames e vacinas", "Saúde e bem-estar"},
	{"dumbbell", "Academia", "Saúde e bem-estar"},
	{"activity", "Bem-estar", "Saúde e bem-estar"},
	{"hand-heart", "Cuidados", "Saúde e bem-estar"},
	{"scissors", "Beleza", "Saúde e bem-estar"},

	// Educação e trabalho
	{"graduation-cap", "Educação", "Educação e trabalho"},
	{"book-open", "Cursos e livros", "Educação e trabalho"},
	{"briefcase", "Trabalho", "Educação e trabalho"},
	{"laptop", "Equipamentos", "Educação e trabalho"},

	// Lazer e cultura
	{"gamepad", "Jogos", "Lazer e cultura"},
	{"popcorn", "Entretenimento", "Lazer e cultura"},
	{"music", "Música", "Lazer e cultura"},
	{"film", "Cinema e streaming", "Lazer e cultura"},
	{"camera", "Fotografia", "Lazer e cultura"},
	{"palette", "Arte e hobbies", "Lazer e cultura"},
	{"tv", "TV", "Lazer e cultura"},
	{"headphones", "Áudio", "Lazer e cultura"},
	{"trophy", "Esportes", "Lazer e cultura"},
	{"star", "Destaques", "Lazer e cultura"},

	// Família e pets
	{"users", "Família", "Família e pets"},
	{"baby", "Filhos", "Família e pets"},
	{"dog", "Cachorro", "Família e pets"},
	{"cat", "Gato", "Família e pets"},
	{"fish", "Aquário", "Família e pets"},
	{"bird", "Aves", "Família e pets"},
	{"bone", "Pet", "Família e pets"},
	{"paw-print", "Animais", "Família e pets"},

	// Finanças
	{"wallet", "Carteira e salário", "Finanças"},
	{"banknote", "Dinheiro", "Finanças"},
	{"banknote-arrow-up", "Receitas", "Finanças"},
	{"coins", "Moedas e troco", "Finanças"},
	{"hand-coins", "Proventos", "Finanças"},
	{"circle-dollar-sign", "Salário", "Finanças"},
	{"dollar-sign", "Valores", "Finanças"},
	{"percent", "Juros e taxas", "Finanças"},
	{"chart-line", "Investimentos", "Finanças"},
	{"chart-column", "Relatórios", "Finanças"},
	{"trending-up", "Rendimentos", "Finanças"},
	{"target", "Metas", "Finanças"},
	{"piggy-bank", "Poupança", "Finanças"},
	{"credit-card", "Cartão de crédito", "Finanças"},
	{"receipt", "Impostos e taxas", "Finanças"},
	{"landmark", "Banco", "Finanças"},
	{"building-2", "Empresa", "Finanças"},
	{"repeat", "Assinaturas", "Finanças"},
	{"arrow-left-right", "Transferências", "Finanças"},

	// Serviços e outros
	{"tag", "Geral", "Serviços e outros"},
	{"shapes", "Outros", "Serviços e outros"},
	{"circle", "Neutro", "Serviços e outros"},
	{"brush", "Serviços gerais", "Serviços e outros"},
	{"sparkles", "Limpeza", "Serviços e outros"},
	{"monitor", "Assinaturas digitais", "Serviços e outros"},
	{"accessibility", "Acessibilidade", "Serviços e outros"},
}

// aliases redireciona chaves legadas (já gravadas por versões anteriores ou
// sugeridas por nomes antigos do Lucide) para a chave canônica atual.
// Toda chave de destino precisa existir no catálogo.
var aliases = map[string]string{
	"home":                   "house",
	"house-key":              "house",
	"gamepad-2":              "gamepad",
	"arrows-left-right":      "arrow-left-right",
	"shopping-basket":        "shopping-cart",
	"badge-dollar-sign":      "circle-dollar-sign",
	"circle-dollar":          "circle-dollar-sign",
	"heart-handshake":        "hand-heart",
	"chef-hat":               "utensils",
	"bus-front":              "bus",
	"car-front":              "car",
	"banknote-arrow-down":    "receipt",
	"calendar-dollar":        "receipt",
	"chart-no-axes-combined": "chart-line",
}

var byKey = func() map[string]Option {
	index := make(map[string]Option, len(catalog))
	for _, option := range catalog {
		index[option.Key] = option
	}
	return index
}()

// groupOrder fixa a ordem de exibição dos grupos no seletor.
var groupOrder = []string{
	"Alimentação",
	"Moradia e contas",
	"Transporte",
	"Compras e vestuário",
	"Saúde e bem-estar",
	"Educação e trabalho",
	"Lazer e cultura",
	"Família e pets",
	"Finanças",
	"Serviços e outros",
}

// All devolve o catálogo completo na ordem de exibição.
func All() []Option {
	out := make([]Option, len(catalog))
	copy(out, catalog)
	return out
}

// Grouped devolve o catálogo organizado pelos grupos oficiais.
func Grouped() []Group {
	byGroup := make(map[string][]Option, len(groupOrder))
	for _, option := range catalog {
		byGroup[option.Group] = append(byGroup[option.Group], option)
	}
	groups := make([]Group, 0, len(groupOrder))
	for _, name := range groupOrder {
		if options := byGroup[name]; len(options) > 0 {
			groups = append(groups, Group{Name: name, Options: options})
		}
	}
	return groups
}

// CanonicalKey normaliza uma chave para a forma canônica do catálogo.
// Retorna string vazia quando a chave não é reconhecida, permitindo ao
// chamador decidir entre herança e fallback.
func CanonicalKey(key string) string {
	normalized := normalizeToken(key)
	if normalized == "" || strings.Contains(normalized, " ") {
		// Uma chave de ícone nunca contém espaços: é texto livre, não chave.
		return ""
	}
	if canonical, ok := aliases[normalized]; ok {
		normalized = canonical
	}
	if _, ok := byKey[normalized]; ok {
		return normalized
	}
	return ""
}

// IsValid informa se a chave existe no catálogo.
func IsValid(key string) bool { return CanonicalKey(key) != "" }

// Lookup devolve a entrada do catálogo para uma chave já canônica.
func Lookup(key string) (Option, bool) {
	option, ok := byKey[key]
	return option, ok
}

// Resolve devolve a primeira chave válida da lista e, se nenhuma servir, o
// ícone neutro oficial. A ordem dos argumentos define a precedência, o que
// implementa a herança Limite -> Categoria -> fallback.
func Resolve(keys ...string) string {
	for _, key := range keys {
		if canonical := CanonicalKey(key); canonical != "" {
			return canonical
		}
	}
	return DefaultKey
}

// ResolveGoal resolve o ícone de uma Meta/reserva e fecha a herança com a marca
// de poupança em vez do ícone neutro de categoria. É a única implementação
// dessa precedência: escolha própria -> categoria vinculada -> reserva.
func ResolveGoal(keys ...string) string {
	return Resolve(append(keys, FallbackGoal)...)
}

// Label devolve o nome amigável de uma chave, com fallback seguro.
func Label(key string) string {
	canonical := CanonicalKey(key)
	if canonical == "" {
		return byKey[DefaultKey].Label
	}
	return byKey[canonical].Label
}

// SortedKeys devolve todas as chaves canônicas em ordem alfabética.
// Usado por testes de estabilidade e auditoria.
func SortedKeys() []string {
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// normalizeToken reduz um valor arbitrário à forma comparável: sem acentos,
// minúsculo e com espaços internos colapsados.
func normalizeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToLower(RemoveAccents(value))
	return strings.Join(strings.Fields(value), " ")
}

// RemoveAccents devolve o texto sem diacríticos, preservando o restante.
func RemoveAccents(value string) string {
	decomposed := norm.NFD.String(value)
	var builder strings.Builder
	builder.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
