package boxcontribution

import (
	"math"
	"testing"
)

func TestNextAutomaticContribution(t *testing.T) {
	tests := []struct {
		name        string
		input       NextAutomaticContributionInput
		wantAmount  int64
		wantReached bool
	}{
		{name: "inactive", input: NextAutomaticContributionInput{Active: false, MonthlyCents: 1000}, wantAmount: 0},
		{name: "zero monthly", input: NextAutomaticContributionInput{Active: true}, wantAmount: 0},
		{name: "no target", input: NextAutomaticContributionInput{Active: true, MonthlyCents: 1000}, wantAmount: 1000},
		{name: "continue after target", input: NextAutomaticContributionInput{Active: true, TargetDefined: true, ContinueAfterTarget: true, BalanceCents: 9500, TargetCents: 10000, MonthlyCents: 1000}, wantAmount: 1000},
		{name: "full below target", input: NextAutomaticContributionInput{Active: true, TargetDefined: true, BalanceCents: 5000, TargetCents: 10000, MonthlyCents: 1000}, wantAmount: 1000},
		{name: "exact remainder", input: NextAutomaticContributionInput{Active: true, TargetDefined: true, BalanceCents: 9000, TargetCents: 10000, MonthlyCents: 1000}, wantAmount: 1000, wantReached: true},
		{name: "partial remainder", input: NextAutomaticContributionInput{Active: true, TargetDefined: true, BalanceCents: 9500, TargetCents: 10000, MonthlyCents: 1000}, wantAmount: 500, wantReached: true},
		{name: "already reached", input: NextAutomaticContributionInput{Active: true, TargetDefined: true, BalanceCents: 10000, TargetCents: 10000, MonthlyCents: 1000}, wantReached: true},
		{name: "extreme range stays safe", input: NextAutomaticContributionInput{Active: true, TargetDefined: true, BalanceCents: math.MinInt64, TargetCents: math.MaxInt64, MonthlyCents: math.MaxInt64}, wantAmount: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NextAutomaticContribution(tt.input)
			if got.AmountCents != tt.wantAmount || got.TargetReached != tt.wantReached {
				t.Fatalf("decision = %+v, want amount=%d reached=%v", got, tt.wantAmount, tt.wantReached)
			}
		})
	}
}
