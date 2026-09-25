package features

import (
	"os"
	"testing"
)

func TestInvestmentsDefaultsOffAndAcceptsOnlyExplicitOptIn(t *testing.T) {
	t.Setenv(investmentsEnv, "temporary")
	if err := os.Unsetenv(investmentsEnv); err != nil {
		t.Fatal(err)
	}
	if FromEnv().Investments {
		t.Fatal("unset must disable Investments")
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
