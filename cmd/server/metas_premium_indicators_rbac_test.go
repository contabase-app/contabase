package main

import (
	"os"
	"strings"
	"testing"
)

// M3 — Indicadores premium não alteram o contrato de permissões de /metas.
// A leitura continua exigindo dashboard:read; todas as rotas de escrita
// continuam exigindo goals:write. Este teste protege o registro das rotas
// contra uma mudança acidental durante a modernização da apresentação.
func TestMetasPremiumIndicatorsKeepRoutePermissionContract(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(src)

	contracts := []struct {
		name       string
		route      string
		permission string
	}{
		{name: "metas read", route: `mux.HandleFunc("/metas", withAuth(`, permission: "permDashboardRead"},
		{name: "metas new sheet", route: `mux.HandleFunc("/metas/novo", withAuth(`, permission: "permGoalsWrite"},
		{name: "metas create box", route: `mux.HandleFunc("/metas/caixinha", withAuth(`, permission: "permGoalsWrite"},
		{name: "metas create limit", route: `mux.HandleFunc("/metas/limite", withAuth(`, permission: "permGoalsWrite"},
	}

	for _, contract := range contracts {
		t.Run(contract.name, func(t *testing.T) {
			start := strings.Index(text, contract.route)
			if start < 0 {
				t.Fatalf("route registration not found: %s", contract.route)
			}
			end := start + 1600
			if end > len(text) {
				end = len(text)
			}
			if !strings.Contains(text[start:end], contract.permission) {
				t.Fatalf("route %s must keep %s", contract.route, contract.permission)
			}
		})
	}
}
