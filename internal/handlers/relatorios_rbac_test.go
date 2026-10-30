package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/contabase-app/contabase/internal/models"
)

// R8 — defesa em profundidade nos GETs de Relatórios.
//
// A rota continua exigindo withAuth + PermissionReportsView; estes testes
// chamam os handlers diretamente para provar que a permissão é revalidada
// antes de qualquer acesso ao banco.

func TestRelatoriosHandlersRejectRolesWithoutReportsView(t *testing.T) {
	cases := []struct {
		name string
		call func(h *RelatoriosHandler, w http.ResponseWriter, r *http.Request)
	}{
		{"page legacy", (*RelatoriosHandler).HandleExibirRelatorios},
		{"page", (*RelatoriosHandler).HandleRelatoriosConceito},
		{"csv", (*RelatoriosHandler).HandleExportarCSV},
		{"extrato pdf", (*RelatoriosHandler).HandleExportarExtratoPDF},
		{"dre pdf", (*RelatoriosHandler).HandleExportarDREPDF},
		{"caixa pdf", (*RelatoriosHandler).HandleExportarCaixaPDF},
		{"contabil pdf", (*RelatoriosHandler).HandleExportarContabilPDF},
		{"contabil resumo csv", (*RelatoriosHandler).HandleExportarContabilResumoCSV},
		{"contabil macros csv", (*RelatoriosHandler).HandleExportarContabilMacrosCSV},
		{"contabil saldos csv", (*RelatoriosHandler).HandleExportarContabilSaldosCSV},
		{"contabil zip", (*RelatoriosHandler).HandleExportarContabilZIP},
	}

	for _, role := range []string{models.RoleUser, "", "UNKNOWN"} {
		for _, tc := range cases {
			t.Run(role+"/"+tc.name, func(t *testing.T) {
				// Sem DB e sem templates: a negação precisa acontecer antes de
				// qualquer leitura ou renderização.
				h := &RelatoriosHandler{ActorRole: role}
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/relatorios", nil)

				tc.call(h, rr, req)

				if rr.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403", rr.Code)
				}
				if !strings.Contains(rr.Body.String(), "forbidden") {
					t.Fatalf("body = %q, want forbidden", rr.Body.String())
				}
			})
		}
	}
}

func TestRelatoriosHandlersAllowAdminAndManager(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	seedInvoicePaymentScenario(t, db)

	for _, role := range []string{models.RoleAdmin, models.RoleManager} {
		t.Run(role, func(t *testing.T) {
			h := &RelatoriosHandler{
				ActorRole:   role,
				DB:          db,
				Templates:   testRelatoriosBranchTemplates(t),
				WorkspaceID: "ws-test",
				UserID:      "user-test",
			}

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/relatorios?mes=7&ano=2026", nil)
			h.HandleRelatoriosConceito(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("page status = %d, want 200", rr.Code)
			}

			csvRR := httptest.NewRecorder()
			csvReq := httptest.NewRequest(http.MethodGet, "/relatorios/export.csv?tipo=extrato-movimentos&mes=7&ano=2026", nil)
			h.HandleExportarCSV(csvRR, csvReq)
			if csvRR.Code != http.StatusOK {
				t.Fatalf("csv status = %d, want 200", csvRR.Code)
			}
			if got := csvRR.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/csv") {
				t.Fatalf("csv content-type = %q", got)
			}
			if got := csvRR.Header().Get("Content-Disposition"); !strings.Contains(got, `filename="extrato-movimentos.csv"`) {
				t.Fatalf("csv content-disposition = %q", got)
			}
		})
	}
}

func TestRelatoriosActorRoleIsPerRequestCopy(t *testing.T) {
	base := RelatoriosHandler{}
	type result struct {
		role   string
		allow  bool
		status int
	}
	results := make(chan result, 400)
	var wg sync.WaitGroup
	roles := []string{models.RoleAdmin, models.RoleManager, models.RoleUser, "UNKNOWN", ""}
	for i := 0; i < 400; i++ {
		role := roles[i%len(roles)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := base
			h.ActorRole = role
			rr := httptest.NewRecorder()
			allow := h.requireReportsPermission(rr)
			results <- result{role: role, allow: allow, status: rr.Code}
		}()
	}
	wg.Wait()
	close(results)

	for res := range results {
		wantAllow := res.role == models.RoleAdmin || res.role == models.RoleManager
		if res.allow != wantAllow {
			t.Fatalf("role %q allow=%v, want %v", res.role, res.allow, wantAllow)
		}
		if wantAllow && res.status != http.StatusOK {
			t.Fatalf("role %q status=%d, want 200 (no rejection)", res.role, res.status)
		}
		if !wantAllow && res.status != http.StatusForbidden {
			t.Fatalf("role %q status=%d, want 403", res.role, res.status)
		}
	}
	if base.ActorRole != "" {
		t.Fatalf("shared base handler ActorRole mutated to %q", base.ActorRole)
	}
}
