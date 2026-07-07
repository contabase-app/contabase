package categorycontract

import "strings"

const (
	WorkspaceTypePersonal = "personal"
	WorkspaceTypeBusiness = "business"

	CategoryTypeIncome  = "INCOME"
	CategoryTypeExpense = "EXPENSE"
)

const (
	MacroPersonalReceitas         = "Receitas"
	MacroPersonalEssencial        = "Essencial"
	MacroPersonalEstiloVida       = "Estilo de Vida"
	MacroReceitasOperacionais     = "Receitas Operacionais"
	MacroReceitasFinanceiras      = "Receitas Financeiras"
	MacroOutrasEntradas           = "Outras Entradas"
	MacroDeducoesImpostos         = "Deduções/Impostos"
	MacroCustosOperacionais       = "Custos Operacionais"
	MacroDespesasAdministrativas  = "Despesas Administrativas"
	MacroDespesasComerciais       = "Despesas Comerciais"
	MacroEquipePrestadores        = "Equipe e Prestadores"
	MacroFinanceiro               = "Financeiro"
	MacroRetiradasDistribuicao    = "Retiradas/Distribuição"
	MacroInvestimentosPatrimonial = "Investimentos/Patrimonial"
	MacroOutrasSaidas             = "Outras Saídas"
	MacroInvestimentosOutros      = "Investimentos/Outros"
)

type MacroGroup struct {
	Name   string
	Type   string
	Legacy bool
}

var personalIncomeMacros = []MacroGroup{
	{Name: MacroPersonalReceitas, Type: CategoryTypeIncome},
}

var personalExpenseMacros = []MacroGroup{
	{Name: MacroPersonalEssencial, Type: CategoryTypeExpense},
	{Name: MacroPersonalEstiloVida, Type: CategoryTypeExpense},
}

var businessIncomeMacros = []MacroGroup{
	{Name: MacroReceitasOperacionais, Type: CategoryTypeIncome},
	{Name: MacroReceitasFinanceiras, Type: CategoryTypeIncome},
	{Name: MacroOutrasEntradas, Type: CategoryTypeIncome},
}

var businessExpenseMacros = []MacroGroup{
	{Name: MacroDeducoesImpostos, Type: CategoryTypeExpense},
	{Name: MacroCustosOperacionais, Type: CategoryTypeExpense},
	{Name: MacroDespesasAdministrativas, Type: CategoryTypeExpense},
	{Name: MacroDespesasComerciais, Type: CategoryTypeExpense},
	{Name: MacroEquipePrestadores, Type: CategoryTypeExpense},
	{Name: MacroFinanceiro, Type: CategoryTypeExpense},
	{Name: MacroRetiradasDistribuicao, Type: CategoryTypeExpense},
	{Name: MacroInvestimentosPatrimonial, Type: CategoryTypeExpense},
	{Name: MacroOutrasSaidas, Type: CategoryTypeExpense},
	{Name: MacroInvestimentosOutros, Type: CategoryTypeExpense, Legacy: true},
}

func DefaultMacroGroup(workspaceType, categoryType string) string {
	if normalizeWorkspaceType(workspaceType) == WorkspaceTypeBusiness {
		if normalizeCategoryType(categoryType) == CategoryTypeIncome {
			return MacroReceitasOperacionais
		}
		return MacroCustosOperacionais
	}
	if normalizeCategoryType(categoryType) == CategoryTypeIncome {
		return MacroPersonalReceitas
	}
	return MacroPersonalEstiloVida
}

func MacroGroupsForType(workspaceType, categoryType string, includeLegacy bool) []MacroGroup {
	var source []MacroGroup
	if normalizeWorkspaceType(workspaceType) == WorkspaceTypeBusiness {
		if normalizeCategoryType(categoryType) == CategoryTypeIncome {
			source = businessIncomeMacros
		} else {
			source = businessExpenseMacros
		}
	} else if normalizeCategoryType(categoryType) == CategoryTypeIncome {
		source = personalIncomeMacros
	} else {
		source = personalExpenseMacros
	}

	out := make([]MacroGroup, 0, len(source))
	for _, group := range source {
		if group.Legacy && !includeLegacy {
			continue
		}
		out = append(out, group)
	}
	return out
}

func MacroGroupNamesForType(workspaceType, categoryType string, includeLegacy bool) []string {
	groups := MacroGroupsForType(workspaceType, categoryType, includeLegacy)
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.Name)
	}
	return out
}

func IsMacroGroupValidForType(workspaceType, categoryType, macroGroup string) bool {
	macroGroup = strings.TrimSpace(macroGroup)
	if macroGroup == "" {
		return true
	}
	for _, group := range MacroGroupsForType(workspaceType, categoryType, true) {
		if macroGroup == group.Name {
			return true
		}
	}
	return false
}

func IsLegacyMacroGroup(workspaceType, categoryType, macroGroup string) bool {
	macroGroup = strings.TrimSpace(macroGroup)
	for _, group := range MacroGroupsForType(workspaceType, categoryType, true) {
		if macroGroup == group.Name {
			return group.Legacy
		}
	}
	return false
}

func normalizeWorkspaceType(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), WorkspaceTypeBusiness) {
		return WorkspaceTypeBusiness
	}
	return WorkspaceTypePersonal
}

func normalizeCategoryType(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case CategoryTypeIncome:
		return CategoryTypeIncome
	default:
		return CategoryTypeExpense
	}
}
