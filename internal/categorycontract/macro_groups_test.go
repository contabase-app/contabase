package categorycontract

import "testing"

func TestBusinessIncomeMacroGroups(t *testing.T) {
	got := MacroGroupNamesForType(WorkspaceTypeBusiness, CategoryTypeIncome, true)
	want := []string{MacroReceitasOperacionais, MacroReceitasFinanceiras, MacroOutrasEntradas}
	assertStringsEqual(t, got, want)
}

func TestBusinessExpenseMacroGroupsIncludeLegacyCompatibility(t *testing.T) {
	got := MacroGroupNamesForType(WorkspaceTypeBusiness, CategoryTypeExpense, true)
	want := []string{
		MacroDeducoesImpostos,
		MacroCustosOperacionais,
		MacroDespesasAdministrativas,
		MacroDespesasComerciais,
		MacroEquipePrestadores,
		MacroFinanceiro,
		MacroRetiradasDistribuicao,
		MacroInvestimentosPatrimonial,
		MacroOutrasSaidas,
		MacroInvestimentosOutros,
	}
	assertStringsEqual(t, got, want)
	if !IsLegacyMacroGroup(WorkspaceTypeBusiness, CategoryTypeExpense, MacroInvestimentosOutros) {
		t.Fatalf("%q should be marked as legacy", MacroInvestimentosOutros)
	}
}

func TestBusinessExpensePreferredMacroGroupsOmitLegacy(t *testing.T) {
	got := MacroGroupNamesForType(WorkspaceTypeBusiness, CategoryTypeExpense, false)
	for _, item := range got {
		if item == MacroInvestimentosOutros {
			t.Fatalf("preferred business expense macros should omit legacy %q: %v", MacroInvestimentosOutros, got)
		}
	}
}

func TestPersonalMacroGroupsStayUnchanged(t *testing.T) {
	assertStringsEqual(t, MacroGroupNamesForType(WorkspaceTypePersonal, CategoryTypeIncome, true), []string{MacroPersonalReceitas})
	assertStringsEqual(t, MacroGroupNamesForType(WorkspaceTypePersonal, CategoryTypeExpense, true), []string{MacroPersonalEssencial, MacroPersonalEstiloVida})
}

func TestMacroGroupValidationByWorkspaceAndType(t *testing.T) {
	for _, macro := range []string{MacroReceitasOperacionais, MacroReceitasFinanceiras, MacroOutrasEntradas} {
		if !IsMacroGroupValidForType(WorkspaceTypeBusiness, CategoryTypeIncome, macro) {
			t.Fatalf("business income macro %q should be valid", macro)
		}
	}
	for _, macro := range []string{MacroRetiradasDistribuicao, MacroInvestimentosPatrimonial, MacroOutrasSaidas, MacroInvestimentosOutros} {
		if !IsMacroGroupValidForType(WorkspaceTypeBusiness, CategoryTypeExpense, macro) {
			t.Fatalf("business expense macro %q should be valid", macro)
		}
	}
	if IsMacroGroupValidForType(WorkspaceTypeBusiness, CategoryTypeIncome, MacroPersonalReceitas) {
		t.Fatalf("personal income macro should not be valid for business income")
	}
	if IsMacroGroupValidForType(WorkspaceTypePersonal, CategoryTypeExpense, MacroFinanceiro) {
		t.Fatalf("business expense macro should not be valid for personal expense")
	}
}

func assertStringsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d = %q, want %q: got=%v want=%v", i, got[i], want[i], got, want)
		}
	}
}
