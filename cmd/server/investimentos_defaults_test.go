package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contabase-app/contabase/internal/features"
)

func TestInvestimentosDefaultStartupConfigurationAndDataPreservation(t *testing.T) {
	for _, workspaceType := range []string{"personal", "business"} {
		for _, tc := range []struct {
			name, config string
			enabled      bool
		}{
			{"absent", "# Existing installation without feature configuration\n", true},
			{"enabled", "CONTABASE_FEATURE_INVESTMENTS=true\n", true},
			{"disabled", "CONTABASE_FEATURE_INVESTMENTS=false\n", false},
			{"zero", "CONTABASE_FEATURE_INVESTMENTS=0\n", false},
			{"empty", "CONTABASE_FEATURE_INVESTMENTS=\n", false},
		} {
			t.Run(workspaceType+"/"+tc.name, func(t *testing.T) {
				h := newInvestimentosI22Harness(t)
				h.exec(t, `UPDATE workspaces SET type=? WHERE id='workspace-rbac'`, workspaceType)
				id := h.create(t, "Preserved investment")
				before := o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases", "investment_valuations")
				configPath := filepath.Join(t.TempDir(), "synthetic.env")
				if err := os.WriteFile(configPath, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
				for _, flag := range []string{"CONTABASE_FEATURE_INVESTMENTS", "CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS"} {
					t.Setenv(flag, "")
					if err := os.Unsetenv(flag); err != nil {
						t.Fatal(err)
					}
				}
				for startup := 0; startup < 3; startup++ {
					loadDotEnv(configPath)
					flags := features.FromEnv()
					if flags.Investments != tc.enabled || flags.InvestmentOperations || flags.InvestmentSettlements || flags.InvestmentSettlementEffects {
						t.Fatalf("startup %d: unexpected flags %+v", startup, flags)
					}
					h.mux = http.NewServeMux()
					registerInvestimentosRoutes(h.mux, h.service, h.signer, h.base)
					want := http.StatusNotFound
					if tc.enabled {
						want = http.StatusOK
					}
					for _, path := range []string{"/investimentos", "/investimentos/" + id} {
						rr := h.request(t, "GET", path, h.admin, nil, false, false)
						requireInvestimentosStatus(t, rr, want)
						if tc.enabled && !strings.Contains(rr.Body.String(), `href="/investimentos"`) {
							t.Fatal("default module missing from shared navigation")
						}
					}
					if before != o1aSnapshot(t, h, "accounts", "transactions", "investments", "investment_purchases", "investment_valuations") {
						t.Fatal("availability change mutated persisted data")
					}
				}
			})
		}
	}
}
