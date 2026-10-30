// Package features is the single authority for application feature availability.
package features

import (
	"os"
	"strings"
)

const investmentsEnv = "CONTABASE_FEATURE_INVESTMENTS"

// Flags contains read-only availability decisions for the current process.
type Flags struct {
	Investments                 bool
	InvestmentOperations        bool
	InvestmentSettlements       bool
	InvestmentSettlementEffects bool
}

// FromEnv enables Investments when its variable is absent. Explicit values,
// including empty and unknown values, retain the legacy true/1-only contract.
// Controlled financial operations still require explicit opt-in at every level.
func FromEnv() Flags {
	value, present := os.LookupEnv(investmentsEnv)
	value = strings.ToLower(strings.TrimSpace(value))
	investments := !present || value == "true" || value == "1"
	operations := strings.ToLower(strings.TrimSpace(os.Getenv("CONTABASE_FEATURE_INVESTMENT_OPERATIONS")))
	settlements := strings.ToLower(strings.TrimSpace(os.Getenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENTS")))
	effects := strings.ToLower(strings.TrimSpace(os.Getenv("CONTABASE_FEATURE_INVESTMENT_SETTLEMENT_EFFECTS")))
	ops := investments && (operations == "true" || operations == "1")
	c1 := ops && (settlements == "true" || settlements == "1")
	return Flags{Investments: investments, InvestmentOperations: ops, InvestmentSettlements: c1,
		InvestmentSettlementEffects: c1 && (effects == "true" || effects == "1")}
}
