package main

import (
	"os"
	"strings"
	"testing"
)

// M5 — Os históricos são sheets de leitura. Este contrato protege as rotas
// existentes contra escrita acidental e contra relaxamento de permissão
// durante a modernização da apresentação.
func TestMetasPremiumHistoryKeepsReadOnlyPermissionContract(t *testing.T) {
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
		{
			name:       "box history",
			route:      `mux.HandleFunc("/metas/caixinha/historico", withAuth(`,
			permission: "permGoalsWrite",
		},
		{
			name:       "limit history",
			route:      `mux.HandleFunc("/metas/limite/historico", withAuth(`,
			permission: "permDashboardRead",
		},
	}

	for _, contract := range contracts {
		t.Run(contract.name, func(t *testing.T) {
			start := strings.Index(text, contract.route)
			if start < 0 {
				t.Fatalf("route registration not found: %s", contract.route)
			}
			end := start + 900
			if end > len(text) {
				end = len(text)
			}
			window := text[start:end]

			if !strings.Contains(window, "r.Method != http.MethodGet") {
				t.Fatalf("route %s must reject every non-GET method", contract.route)
			}
			if !strings.Contains(window, contract.permission) {
				t.Fatalf("route %s must keep %s", contract.route, contract.permission)
			}
		})
	}
}
