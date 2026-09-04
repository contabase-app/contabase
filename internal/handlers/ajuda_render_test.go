package handlers

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAjudaPageRenderContracts(t *testing.T) {
	tpl := testAjudaRenderTemplates(t)
	data := struct {
		AppShellAccess
		Title               string
		UserName            string
		UserInitials        string
		UserFirstName       string
		NotificationCount   int
		ActiveWorkspaceName string
		ProfilePhotoURL     string
		IsBusiness          bool
	}{
		Title:               "Ajuda",
		UserName:            "User Account",
		UserInitials:        "UA",
		UserFirstName:       "User",
		ActiveWorkspaceName: "Workspace A",
		IsBusiness:          true,
		AppShellAccess: AppShellAccess{
			CanManageOps:    true,
			CanManageGlobal: true,
		},
	}

	var out bytes.Buffer
	if err := tpl.ExecuteTemplate(&out, "ajuda-page", data); err != nil {
		t.Fatalf("render ajuda-page: %v", err)
	}

	html := out.String()
	for _, want := range []string{
		`data-app-shell`,
		`data-page-title="Ajuda"`,
		`data-page-section="help"`,
		`id="main-content"`,
		`id="help-search"`,
		`data-help-sections`,
		`/assets/css/ajuda.css`,
		`id="perfis-permissoes"`,
		`id="como-funcionam-relatorios"`,
		`id="relatorio-contabil-natureza-economica"`,
		`id="cartoes-relatorios"`,
		`id="exportacoes-relatorios"`,
		`id="categorias-hierarquia"`,
		`id="caixinhas-metas"`,
		`id="motor-temporal"`,
		`id="limite-gastos"`,
		`id="seguranca-backups"`,
		`Como funcionam os Relatórios`,
		`Fluxo de Caixa`,
		`Competência / DRE`,
		`Relatório Contábil`,
		`Natureza Econômica`,
		`Resultado operacional`,
		`Caixa realizado`,
		`Retiradas/Distribuição`,
		`Investimentos/Patrimonial`,
		`Sem Receitas Financeiras`,
		`Outras Entradas/Saídas`,
		`Investimentos/Outros legado`,
		`Saldo por conta`,
		`reconstruído a partir do saldo atual`,
		`Cartão de crédito e faturas`,
		`Compra no cartão entra na`,
		`Pagamento da fatura entra no`,
		`não entra de novo na competência`,
		`Exemplos e exportações`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("ajuda-page missing %q\nbody:\n%s", want, html)
		}
	}
}

func TestAjudaPageKeepsHelpSearchContracts(t *testing.T) {
	pagePath := resolveTemplatePath(t, "templates/pages/ajuda.html")
	content, err := os.ReadFile(filepath.Clean(pagePath))
	if err != nil {
		t.Fatalf("read ajuda template: %v", err)
	}

	if !strings.Contains(string(content), `/assets/js/ajuda-search.js`) {
		t.Fatalf("ajuda template missing help search script reference")
	}
	searchPath := resolveTemplatePath(t, "assets/js/ajuda-search.js")
	search, err := os.ReadFile(filepath.Clean(searchPath))
	if err != nil {
		t.Fatalf("read help search script: %v", err)
	}
	if !strings.Contains(string(search), `[data-help-sections] > section`) {
		t.Fatal("help search must target the stable V2 help sections selector")
	}
}

func testAjudaRenderTemplates(t *testing.T) *template.Template {
	t.Helper()

	pagePath := resolveTemplatePath(t, "templates/pages/ajuda.html")
	shellPath := resolveTemplatePath(t, "templates/components/app_shell.html")
	pageBytes, err := os.ReadFile(filepath.Clean(pagePath))
	if err != nil {
		t.Fatalf("read ajuda page template: %v", err)
	}

	shellBytes, err := os.ReadFile(filepath.Clean(shellPath))
	if err != nil {
		t.Fatalf("read app shell template: %v", err)
	}
	return template.Must(template.New("ajuda-test").Funcs(withAppShellTestFuncs(template.FuncMap{
		"assetPath": func(path string) string { return path + "?v=test" },
	})).Parse(string(pageBytes) + string(shellBytes)))
}
