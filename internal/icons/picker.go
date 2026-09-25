package icons

// PickerData é o payload do componente reutilizável de seleção de ícones.
//
// O componente é compartilhado por Categorias, Limites e Metas: o mesmo
// template e o mesmo JS atendem os três, mudando apenas o nome do campo do
// formulário e o valor corrente.
type PickerData struct {
	// ID identifica a instância na página (ids de trigger, diálogo e input).
	ID string
	// Field é o nome do input enviado no POST (ex.: icon, icon_key).
	Field string
	// Value é a chave escolhida. Vazio significa "sem escolha explícita",
	// o que habilita a herança na camada de leitura.
	Value string
	// Preview é a chave efetivamente exibida quando Value está vazio.
	Preview string
	// PreviewLabel é o rótulo amigável do Preview.
	PreviewLabel string
	// Groups é o catálogo agrupado exibido no diálogo.
	Groups []Group
}

// NewPicker monta o payload do seletor para uma instância específica.
//
// fallback é a chave que seria usada caso nada fosse escolhido (ícone herdado
// da categoria ou o fallback oficial). Ele é apenas visual: a herança real
// continua acontecendo na leitura, então gravar uma chave explícita é sempre
// uma decisão do usuário.
func NewPicker(id, field, value, fallback string) PickerData {
	stored := CanonicalKey(value)
	preview := stored
	if preview == "" {
		preview = Resolve(fallback)
	}
	return PickerData{
		ID:           id,
		Field:        field,
		Value:        stored,
		Preview:      preview,
		PreviewLabel: Label(preview),
		Groups:       Grouped(),
	}
}

// HasExplicitChoice informa se o usuário já escolheu um ícone.
func (p PickerData) HasExplicitChoice() bool { return p.Value != "" }
