package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CFG5 — Consolidação: remove apenas apresentação comprovadamente sem uso.
func TestConfiguracoesConsolidationLeavesNoLegacyPresentation(t *testing.T) {
	for _, path := range []string{
		"templates/pages/configuracoes.html",
		"templates/pages/configuracoes_categorias.html",
		"templates/pages/configuracoes_contas.html",
		"templates/pages/configuracoes_cartoes.html",
		"templates/pages/configuracoes_perfil.html",
		"templates/pages/configuracoes_workspace.html",
	} {
		content := readTestFile(t, path)
		for _, forbidden := range []string{`data-config-legacy-hub-nav`, `configuracoes-sidebar`, `config-sidebar.js`} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s still references legacy presentation %q", path, forbidden)
			}
		}
	}

	if _, err := os.Stat(filepath.Join(resolveRepoRoot(t), "assets/js/config-sidebar.js")); !os.IsNotExist(err) {
		t.Fatal("config-sidebar.js must be removed after the hub rewrite")
	}

	masks := readTestFile(t, "assets/js/config-masks.js")
	if strings.Contains(masks, "submitProfilePhoto") {
		t.Fatal("config-masks.js must not keep the unused profile-photo auto-submit")
	}
	shell := readTestFile(t, "templates/components/app_shell.html")
	if strings.Contains(shell, "config-masks.js") {
		t.Fatal("config-masks.js must not be loaded globally by the app shell")
	}
	workspace := readTestFile(t, "templates/pages/configuracoes_workspace.html")
	if !strings.Contains(workspace, `assetPath "/assets/js/config-masks.js"`) {
		t.Fatal("workspace page must load the masks it consumes")
	}
	for _, path := range []string{
		"templates/pages/configuracoes_perfil.html",
		"templates/pages/configuracoes_categorias.html",
		"templates/pages/configuracoes_contas.html",
		"templates/pages/configuracoes_cartoes.html",
	} {
		if strings.Contains(readTestFile(t, path), "config-masks.js") {
			t.Fatalf("%s must not load workspace masks", path)
		}
	}
}

func resolveRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}
