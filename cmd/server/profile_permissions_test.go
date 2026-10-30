package main

import (
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

// CFG3 — Perfil é autoatendimento: todos os papéis autenticados podem ler e
// alterar o próprio perfil/senha; papéis desconhecidos continuam negados.
// Este teste impede que a modernização dos formulários introduza uma regra de
// autorização nova por engano.
func TestProfilePermissionsRemainSelfServiceForAuthenticatedRoles(t *testing.T) {
	for _, role := range []string{models.RoleAdmin, models.RoleManager, models.RoleUser} {
		if !hasPermission(role, permProfileRead) {
			t.Fatalf("role %s must keep profile:read", role)
		}
		if !hasPermission(role, permProfileWrite) {
			t.Fatalf("role %s must keep profile:write", role)
		}
	}
	for _, role := range []string{"VIEWER", "GUEST", ""} {
		if hasPermission(role, permProfileWrite) {
			t.Fatalf("unknown role %q must not receive profile:write", role)
		}
	}
}
