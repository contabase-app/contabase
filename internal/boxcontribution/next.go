package boxcontribution

// NextAutomaticContributionInput describes the persisted state needed to
// decide the next automatic contribution without reading a clock or database.
type NextAutomaticContributionInput struct {
	BalanceCents        int64
	TargetCents         int64
	MonthlyCents        int64
	TargetDefined       bool
	ContinueAfterTarget bool
	Active              bool
}

// NextAutomaticContributionDecision is shared by the materializer and monthly
// planning snapshot so both surfaces use exactly the same target semantics.
type NextAutomaticContributionDecision struct {
	AmountCents   int64
	TargetReached bool
}

func NextAutomaticContribution(input NextAutomaticContributionInput) NextAutomaticContributionDecision {
	if !input.Active || input.MonthlyCents <= 0 {
		return NextAutomaticContributionDecision{}
	}
	if !input.TargetDefined || input.TargetCents <= 0 || input.ContinueAfterTarget {
		return NextAutomaticContributionDecision{AmountCents: input.MonthlyCents}
	}
	if input.BalanceCents >= input.TargetCents {
		return NextAutomaticContributionDecision{TargetReached: true}
	}

	// TargetCents and MonthlyCents are positive, so this threshold cannot
	// underflow int64. The final subtraction only runs after proving that the
	// positive remainder fits within MonthlyCents.
	if input.BalanceCents >= input.TargetCents-input.MonthlyCents {
		return NextAutomaticContributionDecision{
			AmountCents:   input.TargetCents - input.BalanceCents,
			TargetReached: true,
		}
	}
	return NextAutomaticContributionDecision{AmountCents: input.MonthlyCents}
}
