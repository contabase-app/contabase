package main

import (
	"os"
	"strings"
	"testing"
)

// M6 — Os formulários e sheets de escrita mantêm as rotas, métodos e
// permissões existentes. O teste é estático de propósito: a modernização é de
// apresentação e não deve relaxar o contrato de autorização.
func TestMetasPremiumFormsKeepWritePermissionContract(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	text := string(src)

	contracts := []struct {
		name       string
		route      string
		method     string
		permission string
	}{
		{name: "new form", route: `mux.HandleFunc("/metas/novo", withAuth(`, method: "http.MethodGet", permission: "permGoalsWrite"},
		{name: "save reserve", route: `mux.HandleFunc("/metas/caixinha", withAuth(`, method: "http.MethodPost", permission: "permGoalsWrite"},
		{name: "save limit", route: `mux.HandleFunc("/metas/limite", withAuth(`, method: "http.MethodPost", permission: "permGoalsWrite"},
		{name: "delete reserve", route: `mux.HandleFunc("/metas/caixinha/", withAuth(`, method: "http.MethodDelete", permission: "permGoalsWrite"},
		{name: "delete limit", route: `mux.HandleFunc("/metas/limite/", withAuth(`, method: "http.MethodDelete", permission: "permGoalsWrite"},
		{name: "contribute", route: `mux.HandleFunc("/metas/caixinha/aporte", withAuth(`, method: "http.MethodPost", permission: "permGoalsWrite"},
		{name: "release", route: `mux.HandleFunc("/metas/caixinha/resgate", withAuth(`, method: "http.MethodPost", permission: "permGoalsWrite"},
		{name: "contribute sheet", route: `mux.HandleFunc("/metas/caixinha/aporte/sheet", withAuth(`, method: "http.MethodGet", permission: "permGoalsWrite"},
		{name: "release sheet", route: `mux.HandleFunc("/metas/caixinha/resgate/sheet", withAuth(`, method: "http.MethodGet", permission: "permGoalsWrite"},
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

			if !strings.Contains(window, contract.method) {
				t.Fatalf("route %s must keep method %s", contract.route, contract.method)
			}
			if !strings.Contains(window, contract.permission) {
				t.Fatalf("route %s must keep %s", contract.route, contract.permission)
			}
		})
	}
}

// M8 — Os GETs de Metas mantêm a permissão na rota e passam a alimentar a
// revalidação interna do handler. A rota continua sendo a primeira barreira.
func TestMetasReadRoutesWireActorRoleForHandlerRevalidation(t *testing.T) {
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
		{name: "page", route: `mux.HandleFunc("/metas", withAuth(`, permission: "permDashboardRead"},
		{name: "new form", route: `mux.HandleFunc("/metas/novo", withAuth(`, permission: "permGoalsWrite"},
		{name: "limit history", route: `mux.HandleFunc("/metas/limite/historico", withAuth(`, permission: "permDashboardRead"},
		{name: "box history", route: `mux.HandleFunc("/metas/caixinha/historico", withAuth(`, permission: "permGoalsWrite"},
		{name: "contribute sheet", route: `mux.HandleFunc("/metas/caixinha/aporte/sheet", withAuth(`, permission: "permGoalsWrite"},
		{name: "release sheet", route: `mux.HandleFunc("/metas/caixinha/resgate/sheet", withAuth(`, permission: "permGoalsWrite"},
	}

	for _, contract := range contracts {
		t.Run(contract.name, func(t *testing.T) {
			start := strings.Index(text, contract.route)
			if start < 0 {
				t.Fatalf("route registration not found: %s", contract.route)
			}
			end := start + 1200
			if end > len(text) {
				end = len(text)
			}
			window := text[start:end]
			if !strings.Contains(window, contract.permission) {
				t.Fatalf("route %s must keep %s", contract.route, contract.permission)
			}
			if !strings.Contains(window, "h.ActorRole = ctx.Role") {
				t.Fatalf("route %s must wire h.ActorRole for handler revalidation", contract.route)
			}
		})
	}
}
