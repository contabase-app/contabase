package models

// Investment cancellation domain model for schema 79. The receipt is the
// durable idempotency record: it is written once, inside the same transaction
// as the compensating transfer and both balance effects, and can never be
// updated or deleted (enforced by triggers). Replaying a confirmed
// cancellation reads this row and never depends on the preview signing key.

// InvestmentCancellation is the immutable receipt of one confirmed
// cancellation of the single initial funded purchase of an ATIVA investment.
type InvestmentCancellation struct {
	WorkspaceID               string
	ID                        string
	InvestmentID              string
	PurchaseID                string
	OriginalTransactionID     string
	CompensationTransactionID string
	InvestmentAccountID       string
	OriginAccountID           string
	AmountCents               int64
	InvestmentBeforeCents     int64
	InvestmentAfterCents      int64
	OriginBeforeCents         int64
	OriginAfterCents          int64
	InvestmentBeforeVersion   int64
	InvestmentAfterVersion    int64
	OriginBeforeVersion       int64
	OriginAfterVersion        int64
	CreatedBy                 string
	ConfirmedAt               int64
	EconomicDate              string
	Reason                    string
	RequestID                 string
	PreviewID                 string
	PreviewIssuedAt           int64
	PreviewExpiresAt          int64
	EvidenceHash              string
}
