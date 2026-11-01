package handlers

import (
	"database/sql"
	"fmt"

	"github.com/contabase-app/contabase/internal/services"
)

// Transaction-side guard for schema 79. The original purchase transfer and
// the compensating transfer of a confirmed cancellation are immutable
// financial evidence: schema 79 triggers abort any UPDATE or DELETE on them
// and any link INSERT (funding, receipts, refunds, status history, invoice
// payments, box ledger). This guard restores the typed 409 the generic
// transaction handlers already use for investment-derived launches, before
// balance reversals or partial form work run.

// errInvestmentCancellationProtected wraps the classification guard sentinel
// so every existing mapping site answers 409 unchanged while tests and future
// projections can still tell the two refusals apart.
var errInvestmentCancellationProtected = fmt.Errorf("%w: cancelamento confirmado protege este lançamento", errInvestmentClassificationProtected)

// cancellationMutationGuard refuses ids that are the original or the
// compensatory transfer of a confirmed cancellation receipt. Workspace scoping
// is exact: a foreign-workspace id never matches and stays a plain not-found.
func cancellationMutationGuard(query interface {
	QueryRow(string, ...interface{}) *sql.Row
}, workspace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := sqlPlaceholders(len(ids))
	args := make([]interface{}, 0, 1+len(ids)*2)
	args = append(args, workspace)
	args = appendStrings(args, ids)
	args = appendStrings(args, ids)
	var count int
	err := query.QueryRow(`SELECT COUNT(*) FROM investment_cancellations c
 WHERE c.workspace_id=? AND (c.original_transaction_id IN (`+placeholders+`) OR c.compensation_transaction_id IN (`+placeholders+`))`, args...).Scan(&count)
	if err != nil {
		// A pre-79 schema can hold no receipt: only the table is missing.
		if services.IsMissingInvestmentCancellationsTable(err) {
			return nil
		}
		return err
	}
	if count != 0 {
		return errInvestmentCancellationProtected
	}
	return nil
}

// protectedTransactionMutationGuard is the single generic-mutation gate for
// investment-derived transactions: classification-only launches and both
// cancellation transfers refuse edits, deletes, payment toggles, invoice moves
// and refunds with typed conflicts. Writer paths call it before any balance
// effect; the early HTTP guards call it before any form parsing.
func protectedTransactionMutationGuard(query interface {
	QueryRow(string, ...interface{}) *sql.Row
}, workspace string, ids []string) error {
	if err := classificationMutationGuard(query, workspace, ids); err != nil {
		return err
	}
	return cancellationMutationGuard(query, workspace, ids)
}
