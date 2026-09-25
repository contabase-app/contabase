// Package features is the single authority for opt-in application features.
package features

import (
	"os"
	"strings"
)

const investmentsEnv = "CONTABASE_FEATURE_INVESTMENTS"

// Flags contains read-only availability decisions for the current process.
type Flags struct {
	Investments bool
}

// FromEnv defaults every feature to disabled. Only explicit true/1 enables
// Investments; unknown values fail closed.
func FromEnv() Flags {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(investmentsEnv)))
	return Flags{Investments: value == "true" || value == "1"}
}
