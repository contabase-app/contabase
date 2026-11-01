package models

// Investment record cancellation domain model for schema 81. The receipt is
// the immutable, durable idempotency record of one confirmed logical
// cancellation of the single initial record-only purchase of an ATIVA
// investment: it is written once, performs no transaction, no balance effect
// and no balance version bump, and can never be updated or deleted (enforced
// by triggers). Replaying a confirmed cancellation reads this row and never
// depends on the preview signing key.

// InvestmentRecordCancellation is the immutable receipt of one confirmed
// record-only registration cancellation. The before/after columns are equal by
// contract: they witness that no balance moved.
type InvestmentRecordCancellation struct {
	WorkspaceID             string
	ID                      string
	InvestmentID            string
	PurchaseID              string
	InvestmentAccountID     string
	AmountCents             int64
	InvestmentBeforeCents   int64
	InvestmentAfterCents    int64
	InvestmentBeforeVersion int64
	InvestmentAfterVersion  int64
	CreatedBy               string
	ConfirmedAt             int64
	EconomicDate            string
	Reason                  string
	RequestID               string
	PreviewID               string
	PreviewIssuedAt         int64
	PreviewExpiresAt        int64
	EvidenceHash            string
}
