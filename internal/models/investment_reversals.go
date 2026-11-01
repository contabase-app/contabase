package models

// Investment purchase reversal domain model for schema 80. The receipt is the
// immutable, durable idempotency record of one confirmed individual reversal:
// it is written once, inside the same transaction as the compensating transfer
// and its balance effects (movement TRANSFER) or without any financial write
// (movement RECORD), and can never be updated or deleted (enforced by
// triggers). Replaying a confirmed reversal reads this row and never depends
// on the preview signing key.

// InvestmentPurchaseReversal is the immutable receipt of one confirmed
// individual reversal of a non-initial purchase of an ATIVA investment.
type InvestmentPurchaseReversal struct {
	WorkspaceID               string
	ID                        string
	InvestmentID              string
	PurchaseID                string
	Movement                  string
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
