package icons_test

import (
	"testing"

	"github.com/contabase-app/contabase/internal/icons"
)

// Baseline da especificação: o mapeamento determinístico nome -> ícone precisa
// ser estável, porque é o que o backfill grava no banco.
func TestCategoryIconForNameBaseline(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Alimentação", "utensils"},
		{"comida", "utensils"},
		{"Restaurante", "utensils"},
		{"Delivery", "bike"},
		{"Mercado", "shopping-cart"},
		{"Supermercado", "shopping-cart"},
		{"Compras", "shopping-cart"},
		{"Moradia", "house"},
		{"Casa", "house"},
		{"Aluguel", "house"},
		{"Água", "droplets"},
		{"Energia", "zap"},
		{"Luz", "zap"},
		{"Internet", "wifi"},
		{"Telefonia", "smartphone"},
		{"Celular", "smartphone"},
		{"Transporte", "bus"},
		{"Carro", "car"},
		{"Combustível", "fuel"},
		{"Gasolina", "fuel"},
		{"Saúde", "heart-pulse"},
		{"Farmácia", "pill"},
		{"Educação", "graduation-cap"},
		{"Faculdade", "graduation-cap"},
		{"Lazer", "popcorn"},
		{"Viagens", "plane"},
		{"Roupas Vitor", "shirt"},
		{"Beleza", "scissors"},
		{"Pet", "paw-print"},
		{"Impostos", "receipt"},
		{"Investimentos", "chart-line"},
		{"Salário", "circle-dollar-sign"},
		{"Transferência", "arrow-left-right"},
		{"Cartão", "credit-card"},
		{"Emergência", "shield"},
		{"Aposentadoria", "piggy-bank"},
		{"Reforma", "hammer"},
		{"Família", "users"},
	}
	for _, tc := range cases {
		got := icons.CategoryIconForName(tc.name)
		if got != tc.want {
			t.Errorf("CategoryIconForName(%q) = %q, want %q", tc.name, got, tc.want)
		}
		if !icons.IsValid(got) {
			t.Errorf("mapeamento de %q gerou chave fora do catálogo: %q", tc.name, got)
		}
	}
}

// Toda chave sugerida precisa existir no catálogo, senão o backfill gravaria
// um ícone que o frontend não consegue renderizar.
func TestCategoryIconMappingsAlwaysLandInCatalog(t *testing.T) {
	names := []string{
		"Alimentação", "Açougue", "Hortifruti", "Padaria", "Doces/Sobremesas", "Bar",
		"Condomínio", "IPTU", "Gás", "Faxina Casa", "Jardinagem", "Automação residencial",
		"Manutenção do veículo", "Seguro do veículo", "IPVA/Licenciamento", "Pedágio",
		"Aplicativo/Taxi essencial", "Plano Celular", "Internet residencial",
		"Plano de saúde pet", "Banho e tosa pet", "Ração", "Veterinário", "Petiscos pet",
		"Consulta médica", "Dentista", "Exames", "Vacinas", "Terapia", "Suplementos",
		"Personal trainer", "Cursos profissionais", "Livros técnicos", "Certificações",
		"Streaming", "Cinema/Eventos", "Clubes/Assinaturas", "Jogos", "Passeios em família",
		"Tráfego pago", "Google Ads", "Meta Ads", "Landing pages", "CRM", "Coworking",
		"Inteligência Artificial", "Créditos de IA", "Hospedagem/servidor de cliente",
		"Ferramentas de trabalho", "Material de escritório", "Encargos", "Retenções",
		"IRPJ/CSLL", "PIS/COFINS", "DAS/Simples Nacional", "ISS", "ICMS", "IOF",
		"Tarifas bancárias", "Anuidade de cartão", "Juros pagos", "Empréstimos",
		"Financiamentos", "Pró-labore", "Distribuição de lucros", "Retiradas e Distribuição",
		"Aporte dos sócios", "Adiantamento a sócios", "Rendimentos financeiros",
		"Rendimentos de investimentos", "Dividendos", "Cashback", "Bônus",
		"Comissão recebida", "Upsell", "Venda de ativo", "Venda de item usado",
		"Reembolso ao cliente", "Estorno", "Ajuste de Caixa", "Perdas diversas",
		"Doações e Apoio", "Caridade", "Igreja", "Apoio familiar", "Presentes para família",
		"Obrigações e Documentos", "Documentos", "Cartório", "Consultoria", "Jurídico",
		"Contabilidade", "Freelancers", "Terceirizados", "Equipe", "Benefícios",
		"Reuniões comerciais", "Eventos/Networking", "Projeto pontual", "Implantação/Setup",
		"Expansão", "Administração", "Financeiro", "Organização", "Outras Saídas",
		"Despesa não recorrente", "Insumos", "Embalagens", "Fornecedores",
		"Compra de mercadoria", "Frete de compra", "Serviços terceirizados de entrega",
		"Areia/Higiene", "Utensílios não essenciais", "Perfumes/Cosméticos",
		"Óculos/Lentes", "Massagem/Estética", "Academia", "Esportes", "Brinquedos",
		"Multas", "Seguro de Vida", "Seguro residencial", "Plano Funerário",
		"Reserva de emergência", "Passeios", "Hobbies", "Brinquedos pet",
		"Hotel/Creche pet", "Acessórios pet", "Roupas Jéssica", "Crianças",
	}
	for _, name := range names {
		key := icons.CategoryIconForName(name)
		if key == "" {
			t.Errorf("nome real %q não recebeu mapeamento", name)
			continue
		}
		if !icons.IsValid(key) {
			t.Errorf("nome real %q mapeou para chave inválida %q", name, key)
		}
	}
}

// O mapeamento não depende de ordem de execução nem de estado: repetir a
// chamada devolve sempre o mesmo resultado (base da idempotência do backfill).
func TestCategoryIconForNameIsDeterministicAndIdempotent(t *testing.T) {
	names := []string{"Alimentação", "Energia", "Pet", "Marketing e Vendas", "Outros", "categoria sem mapeamento"}
	first := make([]string, len(names))
	for i, name := range names {
		first[i] = icons.CategoryIconForName(name)
	}
	for round := 0; round < 3; round++ {
		for i, name := range names {
			if got := icons.CategoryIconForName(name); got != first[i] {
				t.Fatalf("rodada %d divergiu para %q: %q != %q", round, name, got, first[i])
			}
		}
	}
}

// Nome desconhecido recebe o fallback oficial, nunca uma adivinhação.
func TestCategoryIconOrDefaultUsesOfficialFallback(t *testing.T) {
	if got := icons.CategoryIconForName("Zzzzqqq sem significado"); got != "" {
		t.Errorf("nome desconhecido deveria devolver vazio, got %q", got)
	}
	if got := icons.CategoryIconOrDefault("Zzzzqqq sem significado"); got != icons.DefaultKey {
		t.Errorf("fallback = %q, want %q", got, icons.DefaultKey)
	}
	// Acentuação, caixa e pontuação não podem mudar o resultado.
	for _, variant := range []string{"ALIMENTAÇÃO", "alimentacao", "  Alimentação  ", "Alimentacão"} {
		if got := icons.CategoryIconForName(variant); got != "utensils" {
			t.Errorf("variação %q = %q, want utensils", variant, got)
		}
	}
}
