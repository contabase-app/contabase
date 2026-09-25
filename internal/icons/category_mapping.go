package icons

import "strings"

// Este arquivo implementa o mapeamento determinístico nome -> ícone usado pelo
// backfill de categorias. Nada aqui depende de IA, rede ou ordem de execução:
// o mesmo nome sempre produz a mesma chave.
//
// O processo é:
//  1. normaliza o nome (sem acentos, minúsculo, pontuação virando espaço);
//  2. tenta a tabela exata (baseline oficial da especificação);
//  3. tenta as regras por frase, da mais específica para a mais genérica;
//  4. devolve vazio quando nada casa, deixando o fallback para o chamador.
//
// A comparação de regras é feita por tokens: cada token da regra precisa casar
// com algum token do nome. Tokens de 4+ caracteres casam por prefixo, o que
// absorve plurais do português ("tarifa" casa com "tarifas") sem que regras
// curtas e perigosas ("ia", "gas") escapem para dentro de outras palavras.

// CategoryIconForName devolve a chave de ícone sugerida para o nome de uma
// categoria. Retorna string vazia quando o nome é desconhecido.
func CategoryIconForName(name string) string {
	text := matchTokens(name)
	if len(text) == 0 {
		return ""
	}
	joined := strings.Join(text, " ")
	if key, ok := exactCategoryIcons[joined]; ok {
		return key
	}
	for _, rule := range categoryIconRules {
		if matchesRule(text, rule.phrase) {
			return rule.key
		}
	}
	return ""
}

// CategoryIconOrDefault aplica o fallback oficial a nomes desconhecidos.
func CategoryIconOrDefault(name string) string {
	if key := CategoryIconForName(name); key != "" {
		return key
	}
	return DefaultKey
}

// categoryIconRules é avaliada em ordem: frases específicas antes das genéricas.
var categoryIconRules = []struct {
	phrase string
	key    string
}{
	// --- Pets: precisa preceder saúde, vestuário, hospedagem e genéricos ---
	{"plano de saude pet", "heart-pulse"},
	{"plano de saude", "heart-pulse"},
	{"banho e tosa", "scissors"},
	{"brinquedos pet", "bone"},
	{"petiscos pet", "bone"},
	{"acessorios pet", "bone"},
	{"hotel creche pet", "paw-print"},
	{"creche pet", "paw-print"},
	{"racao", "bone"},
	{"veterinario", "stethoscope"},
	{"pet", "paw-print"},

	// --- Veículos e mobilidade: precede manutenção, seguros e tarifas ---
	{"manutencao do veiculo", "car"},
	{"seguro do veiculo", "car"},
	{"seguro obrigatorio", "car"},
	{"ipva", "car"},
	{"licenciamento", "car"},
	{"veiculo", "car"},
	{"carro", "car"},
	{"combustivel", "fuel"},
	{"gasolina", "fuel"},
	{"pedagio", "car"},
	{"estacionamento", "car"},
	{"multa", "receipt"},
	{"transporte publico", "bus"},
	{"transporte", "bus"},
	{"taxi", "car"},
	{"aplicativo", "car"},
	{"viagem", "plane"},
	{"passeio", "footprints"},
	{"frete", "truck"},
	{"entrega", "truck"},
	{"mudanca", "truck"},

	// --- Moradia e utilidades ---
	{"internet", "wifi"},
	{"condominio", "house"},
	{"aluguel", "house"},
	{"moradia", "house"},
	{"decoracao", "house"},
	{"moveis", "house"},
	{"residencial", "house"},
	{"faxina", "sparkles"},
	{"limpeza", "sparkles"},
	{"jardinagem", "leaf"},
	{"energia", "zap"},
	{"eletricidade", "zap"},
	{"agua", "droplets"},
	{"gas", "flame"},
	{"telefonia", "smartphone"},
	{"celular", "smartphone"},
	{"telefone", "smartphone"},
	{"manutencao", "wrench"},
	{"conserto", "wrench"},
	{"reforma", "hammer"},
	{"obra", "hammer"},
	{"seguro de vida", "umbrella"},
	{"seguro residencial", "umbrella"},
	{"plano funerario", "umbrella"},
	{"seguro", "umbrella"},

	// --- Alimentação ---
	{"alimentacao", "utensils"},
	{"supermercado", "shopping-cart"},
	{"mercado", "shopping-cart"},
	{"hortifruti", "salad"},
	{"acougue", "salad"},
	{"refeicao", "utensils"},
	{"marmita", "utensils"},
	{"restaurante", "utensils"},
	{"padaria", "coffee"},
	{"lanche", "coffee"},
	{"cafe", "coffee"},
	{"doce", "cake"},
	{"sobremesa", "cake"},
	{"bar", "beer"},
	{"delivery", "bike"},

	// --- Compras, estoque e vestuário ---
	{"mercadoria", "package"},
	{"insumo", "package"},
	{"embalagem", "package"},
	{"encomenda", "package"},
	{"fornecedor", "package"},
	{"roupa", "shirt"},
	{"calcado", "footprints"},
	{"acessorio", "shopping-bag"},
	{"perfume", "scissors"},
	{"cosmetico", "scissors"},
	{"lente", "shopping-bag"},
	{"oculos", "shopping-bag"},
	{"compras", "shopping-cart"},
	{"presentes para familia", "gift"},
	{"presente", "gift"},
	{"brinde", "gift"},

	// --- Saúde e bem-estar ---
	{"consulta", "stethoscope"},
	{"dentista", "stethoscope"},
	{"odonto", "stethoscope"},
	{"farmacia", "pill"},
	{"medicamento", "pill"},
	{"exame", "syringe"},
	{"vacina", "syringe"},
	{"terapia", "hand-heart"},
	{"massagem", "hand-heart"},
	{"estetica", "scissors"},
	{"academia", "dumbbell"},
	{"personal", "dumbbell"},
	{"suplemento", "dumbbell"},
	{"higiene", "brush"},
	{"esporte", "trophy"},
	{"bem estar", "activity"},
	{"saude", "heart-pulse"},

	// --- Educação e trabalho ---
	{"escola", "graduation-cap"},
	{"faculdade", "graduation-cap"},
	{"curso", "graduation-cap"},
	{"certificacao", "graduation-cap"},
	{"treinamento", "graduation-cap"},
	{"livro", "book-open"},
	{"estudo", "book-open"},
	{"educacao", "graduation-cap"},
	{"trabalho", "briefcase"},

	// --- Lazer e cultura ---
	{"streaming", "film"},
	{"cinema", "film"},
	{"evento", "popcorn"},
	{"lazer", "popcorn"},
	{"clube", "popcorn"},
	{"jogo", "gamepad"},
	{"brinquedo", "gamepad"},
	{"hobby", "palette"},
	{"criativo", "palette"},
	{"musica", "music"},
	{"foto", "camera"},

	// --- Tecnologia, software e serviços ---
	{"inteligencia artificial", "sparkles"},
	{"hospedagem", "monitor"},
	{"servidor", "monitor"},
	{"nuvem", "monitor"},
	{"software", "monitor"},
	{"saas", "monitor"},
	{"plataforma", "monitor"},
	{"sistema", "monitor"},
	{"landing", "monitor"},
	{"crm", "monitor"},
	{"assinatura", "repeat"},
	{"mensalidade", "repeat"},
	{"renovacao", "repeat"},
	{"computador", "laptop"},
	{"eletronico", "laptop"},
	{"equipamento", "laptop"},
	{"ferramenta", "briefcase"},
	{"consultoria", "briefcase"},
	{"juridico", "briefcase"},
	{"contabilidade", "briefcase"},
	{"cartorio", "briefcase"},
	{"coworking", "briefcase"},
	{"escritorio", "briefcase"},
	{"administracao", "briefcase"},
	{"documento", "briefcase"},
	{"marketing", "trending-up"},
	{"trafego", "trending-up"},
	{"anuncio", "trending-up"},
	{"ads", "trending-up"},
	{"upsell", "trending-up"},
	{"prospeccao", "target"},
	{"comissao", "trending-up"},
	{"venda", "trending-up"},
	{"comercial", "trending-up"},
	{"expansao", "trending-up"},
	{"reuniao", "users"},
	{"equipe", "users"},
	{"cliente", "users"},
	{"freelance", "users"},
	{"terceirizado", "users"},
	{"beneficio", "hand-heart"},
	{"cuidado", "hand-heart"},
	{"organizacao", "shapes"},
	{"implantacao", "wrench"},
	{"projeto", "shapes"},
	{"certificado", "shield"},
	{"utensilio", "utensils"},
	{"despesa", "shapes"},

	// --- Impostos, tarifas e encargos ---
	{"imposto", "receipt"},
	{"taxa", "receipt"},
	{"tarifa", "receipt"},
	{"iptu", "receipt"},
	{"encargo", "receipt"},
	{"retencao", "receipt"},
	{"obrigacao", "receipt"},
	{"das", "receipt"},
	{"pis", "receipt"},
	{"cofins", "receipt"},
	{"irpj", "receipt"},
	{"csll", "receipt"},
	{"iss", "receipt"},
	{"iof", "receipt"},
	{"icms", "receipt"},
	{"juros", "percent"},
	{"deducao", "percent"},
	{"desconto", "percent"},
	{"anuidade", "credit-card"},
	{"chargeback", "credit-card"},
	{"parcela", "credit-card"},

	// --- Finanças ---
	{"investimento", "chart-line"},
	{"rendimento", "trending-up"},
	{"pro labore", "wallet"},
	{"salario", "circle-dollar-sign"},
	{"adiantamento", "hand-coins"},
	{"aporte", "hand-coins"},
	{"dividendo", "hand-coins"},
	{"bonus", "hand-coins"},
	{"cashback", "coins"},
	{"retirada", "banknote"},
	{"distribuicao", "banknote"},
	{"emprestimo", "banknote"},
	{"financiamento", "banknote"},
	{"reserva", "piggy-bank"},
	{"poupanca", "piggy-bank"},
	{"aposentadoria", "piggy-bank"},
	{"doacao", "hand-heart"},
	{"caridade", "hand-heart"},
	{"igreja", "hand-heart"},
	{"apoio", "hand-heart"},
	{"reembolso", "repeat"},
	{"estorno", "repeat"},
	{"ajuste", "shapes"},
	{"perda", "shapes"},
	{"saida", "shapes"},
	{"financeiro", "chart-column"},
	{"banco", "landmark"},
	{"emergencia", "shield"},
	{"seguros", "umbrella"},

	// --- Família ---
	{"familia", "users"},
	{"filho", "baby"},
	{"crianca", "baby"},

	// --- Genéricos por último ---
	{"ia", "sparkles"},
	{"casa", "house"},
	{"outros", "shapes"},
	{"geral", "tag"},
}

// exactCategoryIcons é a tabela baseline da especificação: nomes canônicos
// cujo mapeamento deve ser estável e explícito.
var exactCategoryIcons = map[string]string{
	"alimentacao":     "utensils",
	"comida":          "utensils",
	"restaurante":     "utensils",
	"delivery":        "bike",
	"mercado":         "shopping-cart",
	"supermercado":    "shopping-cart",
	"compras":         "shopping-cart",
	"moradia":         "house",
	"casa":            "house",
	"aluguel":         "house",
	"agua":            "droplets",
	"energia":         "zap",
	"luz":             "zap",
	"eletricidade":    "zap",
	"internet":        "wifi",
	"telefone":        "smartphone",
	"celular":         "smartphone",
	"transporte":      "bus",
	"carro":           "car",
	"veiculo":         "car",
	"combustivel":     "fuel",
	"gasolina":        "fuel",
	"saude":           "heart-pulse",
	"medico":          "stethoscope",
	"farmacia":        "pill",
	"educacao":        "graduation-cap",
	"curso":           "graduation-cap",
	"faculdade":       "graduation-cap",
	"lazer":           "popcorn",
	"entretenimento":  "popcorn",
	"viagem":          "plane",
	"roupas":          "shirt",
	"vestuario":       "shirt",
	"beleza":          "scissors",
	"cabeleireiro":    "scissors",
	"pet":             "paw-print",
	"pets":            "paw-print",
	"animais":         "paw-print",
	"impostos":        "receipt",
	"taxas":           "receipt",
	"investimento":    "chart-line",
	"investimentos":   "chart-line",
	"salario":         "circle-dollar-sign",
	"receita":         "wallet",
	"transferencia":   "arrow-left-right",
	"cartao":          "credit-card",
	"emergencia":      "shield",
	"aposentadoria":   "piggy-bank",
	"obras":           "hammer",
	"reforma":         "hammer",
	"familia":         "users",
	"doces":           "cake",
	"sobremesas":      "cake",
	"acougue":         "salad",
	"hortifruti":      "salad",
	"padaria":         "coffee",
	"lanches":         "coffee",
	"cafe":            "coffee",
	"bar":             "beer",
	"refeicoes":       "utensils",
	"marmitas":        "utensils",
	"presentes":       "gift",
	"brinquedos":      "gamepad",
	"jogos":           "gamepad",
	"hobbies":         "palette",
	"passeios":        "footprints",
	"calcados":        "footprints",
	"vendas":          "banknote-arrow-up",
	"outras receitas": "banknote-arrow-up",
	"outras entradas": "banknote-arrow-up",
	"outros":          "shapes",
	"decoracao":       "house",
	"moveis":          "house",
}

// matchTokens normaliza o nome e devolve apenas seus tokens alfanuméricos.
func matchTokens(name string) []string {
	normalized := normalizeToken(name)
	if normalized == "" {
		return nil
	}
	var builder strings.Builder
	builder.Grow(len(normalized))
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			continue
		}
		builder.WriteRune(' ')
	}
	return strings.Fields(builder.String())
}

// matchesRule informa se todos os tokens da frase casam com algum token do
// texto. A comparação usa tokens dobrados (plurais) e, para tokens de 4+
// caracteres, também aceita casamento por prefixo.
func matchesRule(text []string, phrase string) bool {
	folded := make([]string, 0, len(text))
	for _, have := range text {
		folded = append(folded, foldToken(have))
	}
	for _, want := range strings.Fields(phrase) {
		want = foldToken(want)
		found := false
		for _, have := range folded {
			if have == want || (len(want) >= 4 && strings.HasPrefix(have, want)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// foldToken reduz um token à sua forma singular/estável para comparação.
// Cobre as regularidades do português que aparecem nos nomes reais:
// "-ções" -> "-ção" e plurais terminados em "-ns"/"-s".
func foldToken(token string) string {
	switch {
	case strings.HasSuffix(token, "coes") && len(token) > 4:
		return strings.TrimSuffix(token, "coes") + "cao"
	case strings.HasSuffix(token, "oes") && len(token) > 3:
		return strings.TrimSuffix(token, "oes") + "ao"
	case strings.HasSuffix(token, "ns") && len(token) > 3:
		return strings.TrimSuffix(token, "ns") + "m"
	case strings.HasSuffix(token, "s") && len(token) > 3:
		return strings.TrimSuffix(token, "s")
	}
	return token
}
