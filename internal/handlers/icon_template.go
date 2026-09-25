package handlers

import (
	"html/template"

	"github.com/contabase-app/contabase/internal/icons"
)

// IconTemplateFuncs devolve as funções de template do catálogo de ícones.
//
// É a única fonte dessas funções: o servidor e os harnesses de teste montam o
// FuncMap a partir daqui, evitando que os templates do seletor de ícones
// dependam de definições duplicadas.
func IconTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"iconCatalog":    func() []icons.Group { return icons.Grouped() },
		"iconPickerData": icons.NewPicker,
		"iconLabel":      icons.Label,
		"defaultIconKey": func() string { return icons.DefaultKey },
	}
}
