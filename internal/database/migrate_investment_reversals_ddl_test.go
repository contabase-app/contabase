package database

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

// Frozen from schema 80 as published at HEAD 2fe04422. The historical trigger
// is part of the migration contract: later refactors may change whitespace,
// but the canonical (normalized) definition may never change.
const reversal80InsertDDLNormalizedSHA256 = "64a317dc6921b04efd50faf6abdc37f2a69782f19ef187fb187b89ace9efb53b"

func TestReversal80InsertDDLNormalizedStable(t *testing.T) {
	var insert cancellationSchemaObject
	for _, object := range reversal80Objects() {
		if object.name == "trg_reversal80_insert" {
			insert = object
		}
	}
	if insert.ddl == "" {
		t.Fatal("schema 80 insert trigger missing")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(sqlite79NormalizeDDL(insert.ddl))))
	if digest != reversal80InsertDDLNormalizedSHA256 {
		t.Fatalf("schema 80 insert trigger DDL changed: %s", digest)
	}
}

// The confirmation arm proves current reality; the historical arm may only
// depend on immutable evidence. Mutable account/lot state belongs exclusively
// to the insert trigger.
func TestReversal80RelationSeparatesConfirmationFromHistory(t *testing.T) {
	confirm := reversal80Relation("r", true)
	history := reversal80Relation("r", false)
	for _, pin := range []string{
		"i.status='ATIVA'",
		"i.archived_at IS NULL",
		"a.type='INVESTMENT'",
		"s.type IN('CHECKING','SAVINGS','WALLET')",
		"a.current_balance",
		"a.balance_version",
		"s.current_balance",
		"s.balance_version",
		"FROM investment_valuations",
	} {
		if !strings.Contains(confirm, pin) {
			t.Fatalf("confirmation arm lost %q", pin)
		}
		if strings.Contains(history, pin) {
			t.Fatalf("historical arm still pins mutable state %q", pin)
		}
	}
	for _, evidence := range []string{
		"p.principal_cents=",
		"p.is_initial=0",
		"p.purchase_date<=",
		"investment_purchase_reversals x",
		"investment_withdrawal_allocations w",
	} {
		if !strings.Contains(history, evidence) {
			t.Fatalf("historical arm lost immutable evidence %q", evidence)
		}
	}
}
