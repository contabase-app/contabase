package models

// InvestmentReferenceSnapshot is the observed evidence, not a financial balance.
// Operations include the entire history through Position.ValuationDate. Their
// civil dates establish coverage only; array order never implies intraday order.
type InvestmentReferenceSnapshot struct {
	WorkspaceID, InvestmentID, AccountID string
	Position                             InvestmentValuation
	Purchases                            []InvestmentPurchase
	Withdrawals                          []InvestmentWithdrawal
}

type InvestmentReferenceCandidate struct {
	Snapshot             InvestmentReferenceSnapshot
	CoverageToken        string
	HasSameDayOperations bool
}

const (
	InvestmentReferencePending     = "PENDING"
	InvestmentReferenceValid       = "VALID"
	InvestmentReferenceInvalidated = "INVALIDATED"
	InvestmentReferenceSuperseded  = "SUPERSEDED"
)

// State is evaluated against the current source snapshot, never persisted as a
// permission to integrate. Future financial writers must revalidate in their tx.
type InvestmentReferenceRevision struct {
	ID, ReferenceID, PreviousRevisionID       string
	WorkspaceID, InvestmentID, AccountID      string
	Version, ExpectedVersion, PositionVersion int64
	RepresentedCents                          *int64
	Confirmed, DayClosed                      bool
	CreatedBy, RequestID, Reason              string
	CreatedAt                                 int64
	ConfirmedAt                               *int64
	Origin, IntegrationState                  string
	State                                     string
	Candidate                                 InvestmentReferenceCandidate
}
