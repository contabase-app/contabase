package models

// Investment domain models for the beta.5 patrimony feature. These models are
// domain-only: no RBAC, HTTP or handler coupling lives here. Persistence
// semantics are defined by migration 65 (investments / investment_valuations).

const (
	InvestmentStatusAtiva     = "ATIVA"
	InvestmentStatusResgatada = "RESGATADA"
)

// Tax regimes. There are no aliases: the database CHECK and the application
// share exactly these three tokens.
const (
	// InvestmentTaxRegimeIRRegressivo estimates IR by the regressive table.
	InvestmentTaxRegimeIRRegressivo = "IR_REGRESSIVO"
	// InvestmentTaxRegimeIsentoPF is legally exempt for individuals: the
	// estimated tax is a legitimate zero, not an unavailable value.
	InvestmentTaxRegimeIsentoPF = "ISENTO_PF"
	// InvestmentTaxRegimeManual means the provider computes taxes: IR and net
	// values are unavailable (never zero estimates).
	InvestmentTaxRegimeManual = "MANUAL"
)

// Investment groups purchases in one account. principal_cents, account_id,
// workspace_id, applied_date and authorship are immutable after creation
// (principal/date describe the initial purchase); everything else is editable while ATIVA through
// its own domain flow.
type Investment struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	AccountID      string  `json:"account_id"`
	Name           string  `json:"name"`
	Institution    string  `json:"institution"`
	Kind           string  `json:"kind"`
	PrincipalCents int64   `json:"principal_cents"`
	TaxRegime      string  `json:"tax_regime"`
	AppliedDate    string  `json:"applied_date"`
	MaturityDate   *string `json:"maturity_date,omitempty"`
	YieldNote      string  `json:"yield_note"`
	Status         string  `json:"status"`
	RedeemedDate   *string `json:"redeemed_date,omitempty"`
	RedeemedBy     *string `json:"redeemed_by,omitempty"`
	ArchivedAt     *int64  `json:"archived_at,omitempty"`
	CreatedBy      string  `json:"created_by"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// InvestmentValuation is the position of one investment on one civil date.
// The unique key (workspace_id, investment_id, valuation_date) admits exactly
// one row per investment per day: a new entry for the same day corrects the
// existing row. NULL updated_by/updated_at means "never corrected".
type InvestmentValuation struct {
	ID            string  `json:"id"`
	WorkspaceID   string  `json:"workspace_id"`
	InvestmentID  string  `json:"investment_id"`
	ValuationDate string  `json:"valuation_date"`
	AmountCents   int64   `json:"amount_cents"`
	CreatedBy     string  `json:"created_by"`
	CreatedAt     int64   `json:"created_at"`
	UpdatedBy     *string `json:"updated_by,omitempty"`
	UpdatedAt     *int64  `json:"updated_at,omitempty"`
}

// InvestmentPurchase is an immutable purchase, never a ledger transaction.
// Legacy terms are unknown: nullable fields must not be inferred from today's
// editable investment metadata. Recorded terms belong to this purchase alone.
type InvestmentPurchase struct {
	ID, WorkspaceID, InvestmentID                              string
	PurchaseDate                                               string
	PrincipalCents                                             int64
	NoteReference                                              string
	Kind, Institution, MaturityDate, TaxRegime, ConditionsNote *string
	ConditionsSource                                           string
	IsInitial                                                  bool
	CreatedBy                                                  string
	CreatedAt                                                  int64
	// Reversed reports a confirmed schema 80 individual reversal receipt.
	// It is derived at read time and never scanned from or persisted on the
	// row: the purchase itself stays immutable and in the history.
	Reversed bool
}

// TaxEstimate carries an IR estimate with explicit availability. When
// Available is false, TaxCents has no meaning and must never be read as a
// zero estimate — unavailable is not zero.
type TaxEstimate struct {
	Available bool
	TaxCents  int64
}

// LiquidEstimate mirrors TaxEstimate availability: an unavailable tax implies
// an unavailable net value.
type LiquidEstimate struct {
	Available bool
	NetCents  int64
}

// MonthlyVariation compares the current position against the last position of
// the previous month. When Available is false, Reason explains why and the
// numeric fields must not be read.
type MonthlyVariation struct {
	Available     bool
	Reason        string
	PreviousCents int64
	CurrentCents  int64
	DeltaCents    int64
	BasisPoints   int64
}

// Withdrawal records a receipt, independent of account balances and positions.
type InvestmentWithdrawal struct {
	ID, WorkspaceID, InvestmentID, WithdrawalDate, Mode, CreatedBy, RequestID, Note string
	ReceivedCents, CreatedAt                                                        int64
	Allocations                                                                     []InvestmentWithdrawalAllocation
}

type InvestmentWithdrawalAllocation struct {
	WorkspaceID, InvestmentID, WithdrawalID, PurchaseID string
	ReceivedCents                                       int64
	PrincipalCents                                      *int64
	ClosesPurchase                                      bool
}

// Unknown principal stays unknown even if a later allocation has a known part.
type InvestmentPurchaseState struct {
	Closed                  bool
	PrincipalUnknown        bool
	KnownPrincipalWithdrawn int64
	LatestWithdrawalDate    string
}
