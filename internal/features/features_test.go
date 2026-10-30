package features

import (
	"os"
	"testing"
)

func TestInvestmentsDefaultsOnAndPreservesExplicitConfiguration(t *testing.T) {
	t.Setenv(investmentsEnv, "temporary")
	if err := os.Unsetenv(investmentsEnv); err != nil {
		t.Fatal(err)
	}
	if !FromEnv().Investments {
		t.Fatal("unset must enable Investments")
	}
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"false", false}, {"0", false},
		{"true", true}, {"1", true}, {" TRUE ", true},
		{"yes", false}, {"garbage", false},
	} {
		t.Setenv(investmentsEnv, tc.value)
		if got := FromEnv().Investments; got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestInvestmentOperationsExplicitOptIn(t *testing.T) {
	t.Setenv(investmentsEnv, "true")
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "")
	if err := os.Unsetenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS"); err != nil {
		t.Fatal(err)
	}
	if FromEnv().InvestmentOperations {
		t.Fatal("operations enabled by default")
	}
	for _, value := range []string{"", "false", "0", "yes", "garbage", "true", "1", " TRUE "} {
		t.Setenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS", value)
		want := value == "true" || value == "1" || value == " TRUE "
		if FromEnv().InvestmentOperations != want {
			t.Fatalf("unexpected operations flag for %q", value)
		}
	}
	t.Setenv(investmentsEnv, "false")
	if FromEnv().InvestmentOperations {
		t.Fatal("operations require investments")
	}
}

func TestInvestmentSettlementsExplicitOptIn(t *testing.T) {
	t.Setenv(investmentsEnv, "true")
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "true")
	t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "")
	if err := os.Unsetenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS"); err != nil {
		t.Fatal(err)
	}
	if FromEnv().InvestmentSettlements {
		t.Fatal("settlements enabled by default")
	}
	for _, value := range []string{"", "false", "0", "yes", "garbage", "true", "1", " TRUE "} {
		t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", value)
		want := value == "true" || value == "1" || value == " TRUE "
		if FromEnv().InvestmentSettlements != want {
			t.Fatalf("unexpected settlement flag for %q", value)
		}
	}
	for _, parent := range []string{investmentsEnv, "CONTABASE_FEATURE_INVESTMENT_OPERATIONS"} {
		t.Run(parent, func(t *testing.T) {
			t.Setenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS", "true")
			t.Setenv(parent, "false")
			if FromEnv().InvestmentSettlements {
				t.Fatal("settlements require both parent features")
			}
		})
	}
}

func TestInvestmentSettlementEffectsExplicitOptIn(t *testing.T) {
	parents := []string{investmentsEnv, "CONTABASE_FEATURE_INVESTMENT_OPERATIONS", "CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS"}
	for _, parent := range parents {
		t.Setenv(parent, "true")
	}
	const flag = "CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS"
	t.Setenv(flag, "temporary")
	if err := os.Unsetenv(flag); err != nil {
		t.Fatal(err)
	}
	if FromEnv().InvestmentSettlementEffects {
		t.Fatal("effects enabled by default")
	}
	for _, value := range []string{"", "false", "0", "yes", "garbage", "true", "1", " TRUE "} {
		t.Setenv(flag, value)
		want := value == "true" || value == "1" || value == " TRUE "
		if FromEnv().InvestmentSettlementEffects != want {
			t.Fatalf("unexpected effects flag for %q", value)
		}
	}
	for _, parent := range parents {
		t.Run(parent, func(t *testing.T) {
			t.Setenv(flag, "true")
			t.Setenv(parent, "false")
			if FromEnv().InvestmentSettlementEffects {
				t.Fatal("effects require every parent feature")
			}
		})
	}
}
