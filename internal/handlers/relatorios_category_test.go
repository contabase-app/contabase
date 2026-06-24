package handlers

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRelatoriosGastosPorCategoriaIncluiSubcategoriaSemLancamentoNoPai(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedRelatoriosChildOnlyCategoryScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-cat", UserID: "user-cat"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "300", ",00")
	if len(data.Categorias) != 2 {
		t.Fatalf("categorias = %#v, want parent totalizer and child", data.Categorias)
	}

	parent := data.Categorias[0]
	if parent.Nome != "Moradia" || parent.IsChild || !parent.IsGroup {
		t.Fatalf("parent = %#v, want root group Moradia", parent)
	}
	assertMoneyDisplay(t, "parent valor", parent.Valor, "300", ",00")
	if parent.Percent != 100 {
		t.Fatalf("parent percent = %d, want 100", parent.Percent)
	}

	child := data.Categorias[1]
	if child.Nome != "Aluguel" || !child.IsChild || child.ParentName != "Moradia" || child.IsGroup {
		t.Fatalf("child = %#v, want child Aluguel under Moradia", child)
	}
	assertMoneyDisplay(t, "child valor", child.Valor, "300", ",00")
	if child.Percent != 100 {
		t.Fatalf("child percent = %d, want 100", child.Percent)
	}
	assertMoneyDisplay(t, "donut essencial", data.DonutEssentialTotal, "300", ",00")
	assertDRETotal(t, handler, 2026, -30000)
}

func TestRelatoriosGastosPorCategoriaOrdemHierarquiaPaisEFilhos(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedRelatoriosMultiParentCategoryScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-hierarchy", UserID: "user-hierarchy"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "800", ",00")
	if len(data.Categorias) != 9 {
		t.Fatalf("len(categorias) = %d, want 9 (3 groups + 3 direct rows + 3 children)", len(data.Categorias))
	}

	// Expected order:
	//   0: Alimentação       (group total 350)
	//   1:   Alimentação     (direct parent row 300)
	//   2:   Café/Lanches    (child 50)
	//   3: Contas e Serviços (group total 280)
	//   4:   Contas e Serviços (direct parent row 200)
	//   5:   Celular/Telefone (child 80)
	//   6: Família e Pets    (group total 170)
	//   7:   Família e Pets  (direct parent row 100)
	//   8:   Pets            (child 70)

	expected := []struct {
		nome       string
		isChild    bool
		isGroup    bool
		isDirect   bool
		parentName string
		reais      string
	}{
		{"Alimentação", false, true, false, "", "350"},
		{"Alimentação", true, false, true, "Alimentação", "300"},
		{"Café/Lanches", true, false, false, "Alimentação", "50"},
		{"Contas e Serviços", false, true, false, "", "280"},
		{"Contas e Serviços", true, false, true, "Contas e Serviços", "200"},
		{"Celular/Telefone", true, false, false, "Contas e Serviços", "80"},
		{"Família e Pets", false, true, false, "", "170"},
		{"Família e Pets", true, false, true, "Família e Pets", "100"},
		{"Pets", true, false, false, "Família e Pets", "70"},
	}

	for i, exp := range expected {
		got := data.Categorias[i]
		if got.Nome != exp.nome {
			t.Errorf("index %d: nome = %q, want %q", i, got.Nome, exp.nome)
		}
		if got.IsChild != exp.isChild {
			t.Errorf("index %d (%q): IsChild = %v, want %v", i, got.Nome, got.IsChild, exp.isChild)
		}
		if got.IsGroup != exp.isGroup {
			t.Errorf("index %d (%q): IsGroup = %v, want %v", i, got.Nome, got.IsGroup, exp.isGroup)
		}
		if got.IsDirectParentEntry != exp.isDirect {
			t.Errorf("index %d (%q): IsDirectParentEntry = %v, want %v", i, got.Nome, got.IsDirectParentEntry, exp.isDirect)
		}
		if got.ParentName != exp.parentName {
			t.Errorf("index %d (%q): ParentName = %q, want %q", i, got.Nome, got.ParentName, exp.parentName)
		}
		assertMoneyDisplay(t, exp.nome+" valor", got.Valor, exp.reais, ",00")
	}

	// Verify group rows don't have indentation (IsChild=false).
	rootNames := []string{"Alimentação", "Contas e Serviços", "Família e Pets"}
	for _, name := range rootNames {
		var found bool
		for _, c := range data.Categorias {
			if c.Nome == name && !c.IsChild && c.IsGroup {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("group category %q should be IsChild=false and IsGroup=true", name)
		}
	}

	// Verify child "Pets" is under "Família e Pets" and NOT under "Contas e Serviços".
	for i, c := range data.Categorias {
		if c.Nome == "Pets" {
			if !c.IsChild {
				t.Errorf("Pets should be IsChild=true")
			}
			if c.ParentID == "" {
				t.Errorf("Pets should have a ParentID")
			}
			if c.ParentName != "Família e Pets" {
				t.Errorf("Pets ParentName = %q, want Família e Pets", c.ParentName)
			}
			if previousGroupName(data.Categorias, i) != "Família e Pets" {
				t.Errorf("Pets (index %d) should appear below Família e Pets group", i)
			}
		}
	}

	// Verify "Celular/Telefone" is NOT a child of "Família e Pets".
	for _, c := range data.Categorias {
		if c.Nome == "Celular/Telefone" && c.IsChild {
			if c.ParentName == "Família e Pets" {
				t.Errorf("Celular/Telefone should NOT have parent Família e Pets, got ParentName=%q", c.ParentName)
			}
		}
	}
}

func TestRelatoriosGastosPorCategoriaFilhoSemLancamentoNoPaiGanhaGrupo(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedRelatoriosOrphanChildScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-orphan", UserID: "user-orphan"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "350", ",00")
	if len(data.Categorias) != 5 {
		t.Fatalf("len(categorias) = %d, want 5 (2 groups + direct row + 2 children)", len(data.Categorias))
	}

	// Parent Alimentação appears first with its child.
	if data.Categorias[0].Nome != "Alimentação" || data.Categorias[0].IsChild || !data.Categorias[0].IsGroup {
		t.Errorf("index 0: want group Alimentação, got %#v", data.Categorias[0])
	}
	assertMoneyDisplay(t, "Alimentação total", data.Categorias[0].Valor, "250", ",00")
	if data.Categorias[1].Nome != "Alimentação" || !data.Categorias[1].IsChild || !data.Categorias[1].IsDirectParentEntry {
		t.Errorf("index 1: want direct parent row Alimentação, got %#v", data.Categorias[1])
	}
	assertMoneyDisplay(t, "Alimentação direta", data.Categorias[1].Valor, "200", ",00")
	if data.Categorias[2].Nome != "Café/Lanches" || !data.Categorias[2].IsChild {
		t.Errorf("index 2: want child Café/Lanches, got %#v", data.Categorias[2])
	}
	assertMoneyDisplay(t, "Café total", data.Categorias[2].Valor, "50", ",00")

	// Child Pets must be grouped under Família e Pets even when the parent has no direct spending.
	if data.Categorias[3].Nome != "Família e Pets" || data.Categorias[3].IsChild || !data.Categorias[3].IsGroup {
		t.Errorf("index 3: want group Família e Pets, got %#v", data.Categorias[3])
	}
	assertMoneyDisplay(t, "Família e Pets total", data.Categorias[3].Valor, "100", ",00")
	if data.Categorias[4].Nome != "Pets" || !data.Categorias[4].IsChild || data.Categorias[4].ParentName != "Família e Pets" {
		t.Errorf("index 4: want child Pets under Família e Pets, got %#v", data.Categorias[4])
	}
	assertMoneyDisplay(t, "Pets total", data.Categorias[4].Valor, "100", ",00")
}

func TestRelatoriosGastosPorCategoriaPaiDiretoDoisFilhosRaizSemFilhosEOutroWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedRelatoriosParentDirectChildrenStandaloneScenario(t, db)

	handler := RelatoriosHandler{DB: db, WorkspaceID: "ws-parent-direct", UserID: "user-parent-direct"}
	data, err := handler.buildRelatoriosData("", 8, 2026)
	if err != nil {
		t.Fatalf("buildRelatoriosData: %v", err)
	}

	assertMoneyDisplay(t, "total despesas", data.TotalDespesas, "325", ",00")
	if len(data.Categorias) != 5 {
		t.Fatalf("len(categorias) = %d, want 5 (group + direct + 2 children + standalone root)", len(data.Categorias))
	}

	expected := []struct {
		nome       string
		isChild    bool
		isGroup    bool
		isDirect   bool
		parentName string
		reais      string
		percent    int
	}{
		{"Alimentação", false, true, false, "", "300", 92},
		{"Alimentação", true, false, true, "Alimentação", "50", 15},
		{"Padaria", true, false, false, "Alimentação", "150", 46},
		{"Mercado", true, false, false, "Alimentação", "100", 30},
		{"Transporte", false, false, false, "", "25", 7},
	}

	for i, exp := range expected {
		got := data.Categorias[i]
		if got.Nome != exp.nome || got.IsChild != exp.isChild || got.IsGroup != exp.isGroup || got.IsDirectParentEntry != exp.isDirect || got.ParentName != exp.parentName {
			t.Errorf("index %d: categoria = %#v, want nome=%q child=%v group=%v direct=%v parent=%q", i, got, exp.nome, exp.isChild, exp.isGroup, exp.isDirect, exp.parentName)
		}
		assertMoneyDisplay(t, exp.nome+" valor", got.Valor, exp.reais, ",00")
		if got.Percent != exp.percent {
			t.Errorf("index %d (%q): Percent = %d, want %d", i, got.Nome, got.Percent, exp.percent)
		}
	}

	for _, cat := range data.Categorias {
		if strings.Contains(cat.ID, "other") || strings.Contains(cat.Nome, "Outro Workspace") {
			t.Fatalf("categoria de outro workspace entrou no relatório: %#v", cat)
		}
	}
}

func TestRelatoriosGastosPorCategoriaTemplatePreservaDataSensitiveERole(t *testing.T) {
	raw, err := os.ReadFile("../../templates/pages/relatorios.html")
	if err != nil {
		t.Fatalf("ReadFile relatorios.html: %v", err)
	}
	html := string(raw)
	if !strings.Contains(html, `data-category-role="{{if .IsGroup}}group{{else if .IsDirectParentEntry}}direct-parent{{else if .IsChild}}child{{else}}root{{end}}"`) {
		t.Fatalf("template should expose category row role for group/direct/child/root inspection")
	}
	if !strings.Contains(html, `<span class="text-xs font-bold text-[var(--cb-text-main)]" data-sensitive>R$ {{.Valor.Reais}}{{.Valor.Cents}}</span>`) {
		t.Fatalf("template should keep category amount data-sensitive")
	}
	if !strings.Contains(html, `<span class="text-xs font-semibold text-[var(--cb-text-subtle)] w-9 text-right" data-sensitive>{{.Percent}}%</span>`) {
		t.Fatalf("template should keep category percent data-sensitive")
	}
}

func previousGroupName(categorias []CategoriaBar, index int) string {
	for i := index - 1; i >= 0; i-- {
		if categorias[i].IsGroup && !categorias[i].IsChild {
			return categorias[i].Nome
		}
	}
	return ""
}

func seedRelatoriosChildOnlyCategoryScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	expenseUnix := testUnixDate("2026-08-05")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-cat', 'User Cat', 'user-cat@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-cat', 'Workspace Cat', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-cat', 'user-cat', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('checking-cat', 'ws-cat', 'Conta Cat', 'CHECKING', 100000, 70000, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('cat-parent', 'ws-cat', 'Moradia', 'home', 'blue', 'EXPENSE', 'Essencial', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('cat-child', 'ws-cat', 'Aluguel', 'receipt', 'sky', 'EXPENSE', NULL, 'cat-parent', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-child-only', 'ws-cat', 'user-cat', 'checking-cat', 'cat-child', 'EXPENSE', 30000, ?, 'Aluguel agosto', 'paid', ?, ?)
	`, expenseUnix, now, now)
}

func seedRelatoriosMultiParentCategoryScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	expenseUnix := testUnixDate("2026-08-05")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-hierarchy', 'User H', 'user-hierarchy@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-hierarchy', 'Workspace H', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-hierarchy', 'user-hierarchy', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-h', 'ws-hierarchy', 'Conta H', 'CHECKING', 100000, 40000, ?, ?)
	`, now, now)

	// 3 root parents
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('h-alimentacao', 'ws-hierarchy', 'Alimentação', 'utensils', 'orange', 'EXPENSE', 'Essencial', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('h-contas', 'ws-hierarchy', 'Contas e Serviços', 'zap', 'gray', 'EXPENSE', 'Essencial', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('h-familia', 'ws-hierarchy', 'Família e Pets', 'heart', 'pink', 'EXPENSE', 'Estilo de Vida', ?)
	`, now)

	// Children
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('h-cafe', 'ws-hierarchy', 'Café/Lanches', 'coffee', 'amber', 'EXPENSE', NULL, 'h-alimentacao', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('h-celular', 'ws-hierarchy', 'Celular/Telefone', 'smartphone', 'slate', 'EXPENSE', NULL, 'h-contas', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('h-pets', 'ws-hierarchy', 'Pets', 'paw-print', 'rose', 'EXPENSE', NULL, 'h-familia', ?)
	`, now)

	// Transactions — Alimentação gets largest (30000), Contas medium (20000), Família smallest (10000).
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-h-alim', 'ws-hierarchy', 'user-hierarchy', 'acc-h', 'h-alimentacao', 'EXPENSE', 30000, ?, 'Mercado', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-h-cafe', 'ws-hierarchy', 'user-hierarchy', 'acc-h', 'h-cafe', 'EXPENSE', 5000, ?, 'Café', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-h-contas', 'ws-hierarchy', 'user-hierarchy', 'acc-h', 'h-contas', 'EXPENSE', 20000, ?, 'Internet', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-h-cel', 'ws-hierarchy', 'user-hierarchy', 'acc-h', 'h-celular', 'EXPENSE', 8000, ?, 'Celular', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-h-fam', 'ws-hierarchy', 'user-hierarchy', 'acc-h', 'h-familia', 'EXPENSE', 10000, ?, 'Presente', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-h-pets', 'ws-hierarchy', 'user-hierarchy', 'acc-h', 'h-pets', 'EXPENSE', 7000, ?, 'Ração', 'paid', ?, ?)
	`, expenseUnix, now, now)
}

func seedRelatoriosParentDirectChildrenStandaloneScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	expenseUnix := testUnixDate("2026-08-05")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-parent-direct', 'User Parent Direct', 'user-parent-direct@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-parent-direct-other', 'User Parent Direct Other', 'user-parent-direct-other@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-parent-direct', 'Workspace Parent Direct', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-parent-direct-other', 'Outro Workspace', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-parent-direct', 'user-parent-direct', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-parent-direct-other', 'user-parent-direct-other', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-parent-direct', 'ws-parent-direct', 'Conta Parent Direct', 'CHECKING', 100000, 67500, ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-parent-direct-other', 'ws-parent-direct-other', 'Conta Outro Workspace', 'CHECKING', 100000, 100000, ?, ?)
	`, now, now)

	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('pd-alimentacao', 'ws-parent-direct', 'Alimentação', 'utensils', 'orange', 'EXPENSE', 'Essencial', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('pd-transporte', 'ws-parent-direct', 'Transporte', 'car', 'sky', 'EXPENSE', 'Essencial', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('pd-mercado', 'ws-parent-direct', 'Mercado', 'shopping-cart', 'green', 'EXPENSE', NULL, 'pd-alimentacao', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('pd-padaria', 'ws-parent-direct', 'Padaria', 'wheat', 'amber', 'EXPENSE', NULL, 'pd-alimentacao', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('pd-other-alimentacao', 'ws-parent-direct-other', 'Outro Workspace Alimentação', 'utensils', 'red', 'EXPENSE', 'Essencial', ?)
	`, now)

	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pd-alim-direto', 'ws-parent-direct', 'user-parent-direct', 'acc-parent-direct', 'pd-alimentacao', 'EXPENSE', 5000, ?, 'Feira', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pd-mercado', 'ws-parent-direct', 'user-parent-direct', 'acc-parent-direct', 'pd-mercado', 'EXPENSE', 10000, ?, 'Mercado', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pd-padaria', 'ws-parent-direct', 'user-parent-direct', 'acc-parent-direct', 'pd-padaria', 'EXPENSE', 15000, ?, 'Padaria', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pd-transporte', 'ws-parent-direct', 'user-parent-direct', 'acc-parent-direct', 'pd-transporte', 'EXPENSE', 2500, ?, 'Ônibus', 'paid', ?, ?)
	`, expenseUnix, now, now)
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-pd-other', 'ws-parent-direct-other', 'user-parent-direct-other', 'acc-parent-direct-other', 'pd-other-alimentacao', 'EXPENSE', 99900, ?, 'Outro Workspace', 'paid', ?, ?)
	`, expenseUnix, now, now)
}

func seedRelatoriosOrphanChildScenario(t *testing.T, db *sql.DB) {
	t.Helper()

	now := time.Now().Unix()
	expenseUnix := testUnixDate("2026-08-05")

	execTestSQL(t, db, `
		INSERT INTO users (id, name, email, password_hash, created_at, updated_at)
		VALUES ('user-orphan', 'User O', 'user-orphan@example.com', 'hash', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspaces (id, name, description, type, created_at, updated_at)
		VALUES ('ws-orphan', 'Workspace O', '', 'personal', ?, ?)
	`, now, now)
	execTestSQL(t, db, `
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES ('ws-orphan', 'user-orphan', 'ADMIN', ?)
	`, now)
	execTestSQL(t, db, `
		INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance, created_at, updated_at)
		VALUES ('acc-o', 'ws-orphan', 'Conta O', 'CHECKING', 100000, 50000, ?, ?)
	`, now, now)

	// Parent Alimentação (has spending)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('o-alimentacao', 'ws-orphan', 'Alimentação', 'utensils', 'orange', 'EXPENSE', 'Essencial', ?)
	`, now)
	// Parent Família e Pets (NO spending)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, created_at)
		VALUES ('o-familia', 'ws-orphan', 'Família e Pets', 'heart', 'pink', 'EXPENSE', 'Estilo de Vida', ?)
	`, now)

	// Child of Alimentação (has spending)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('o-cafe', 'ws-orphan', 'Café/Lanches', 'coffee', 'amber', 'EXPENSE', NULL, 'o-alimentacao', ?)
	`, now)
	// Child of Família e Pets (has spending BUT parent doesn't)
	execTestSQL(t, db, `
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES ('o-pets', 'ws-orphan', 'Pets', 'paw-print', 'rose', 'EXPENSE', NULL, 'o-familia', ?)
	`, now)

	// Alimentação (parent) has spending
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-o-alim', 'ws-orphan', 'user-orphan', 'acc-o', 'o-alimentacao', 'EXPENSE', 20000, ?, 'Mercado', 'paid', ?, ?)
	`, expenseUnix, now, now)
	// Café (child) has spending
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-o-cafe', 'ws-orphan', 'user-orphan', 'acc-o', 'o-cafe', 'EXPENSE', 5000, ?, 'Café', 'paid', ?, ?)
	`, expenseUnix, now, now)
	// Pets (child) has spending but parent (Família e Pets) does NOT
	execTestSQL(t, db, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, category_id, type, amount, date, description, status, created_at, updated_at)
		VALUES ('tx-o-pets', 'ws-orphan', 'user-orphan', 'acc-o', 'o-pets', 'EXPENSE', 10000, ?, 'Ração', 'paid', ?, ?)
	`, expenseUnix, now, now)
}
