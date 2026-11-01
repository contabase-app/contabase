package services

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Post-cancellation mutation guard for schema 79. A confirmed receipt keeps
// the investment row exactly as it was cancelled (status ATIVA, not archived),
// so the ordinary status predicates cannot refuse later operations: only the
// receipt can. The schema 79 triggers remain the barrier of last resort for
// UPDATE, DELETE, REPLACE and UPSERT; these guards turn the refusal into a
// typed domain error before any balance effect or partial write runs.

// ErrInvestmentCancelled refuses a mutation because the investment has a
// confirmed cancellation receipt. It is a conflict: the existing error
// mappings (HTTP 409, adoption audit code CONFLICT) apply unchanged.
var ErrInvestmentCancelled = fmt.Errorf("%w: aplicação possui cancelamento confirmado", ErrInvestmentConflict)

// IsMissingInvestmentCancellationsTable reports whether err is only the
// absence of the schema 79 receipt table. A pre-79 database can hold no
// receipt by construction: the guard passes and migration 79 itself
// introduces the table and its triggers.
func IsMissingInvestmentCancellationsTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: investment_cancellations")
}

// rejectCancelledInvestmentTx must run inside the caller's writer transaction,
// after idempotent replay lookups and before any INSERT, UPDATE or DELETE on
// the investment or its dependencies. Since schema 81 it refuses both
// cancellation families: the funded cancellation (79) and the record-only
// registration cancellation (81). A foreign workspace never matches either
// receipt, so cross-tenant ids stay indistinguishable from unknown ones.
func rejectCancelledInvestmentTx(ctx context.Context, tx *sql.Tx, workspace, investment string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&n); err != nil {
		if !IsMissingInvestmentCancellationsTable(err) {
			return err
		}
	} else if n != 0 {
		return ErrInvestmentCancelled
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_record_cancellations WHERE workspace_id=? AND investment_id=?`, workspace, investment).Scan(&n); err != nil {
		if !IsMissingInvestmentRecordCancellationsTable(err) {
			return err
		}
		return nil
	}
	if n != 0 {
		return ErrInvestmentCancelled
	}
	return nil
}

// rejectCancelledAdoptionAccountTx mirrors the schema 79 and 81 adoption
// triggers: the account itself remains usable, but a declaration about the
// investment account of an already cancelled lot is incompatible with its
// receipt.
func rejectCancelledAdoptionAccountTx(ctx context.Context, tx *sql.Tx, workspace, account string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_cancellations WHERE workspace_id=? AND investment_account_id=?`, workspace, account).Scan(&n); err != nil {
		if !IsMissingInvestmentCancellationsTable(err) {
			return err
		}
	} else if n != 0 {
		return fmt.Errorf("%w: conta possui aplicação com cancelamento confirmado", ErrInvestmentCancelled)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM investment_record_cancellations WHERE workspace_id=? AND investment_account_id=?`, workspace, account).Scan(&n); err != nil {
		if !IsMissingInvestmentRecordCancellationsTable(err) {
			return err
		}
		return nil
	}
	if n != 0 {
		return fmt.Errorf("%w: conta possui aplicação com cancelamento confirmado", ErrInvestmentCancelled)
	}
	return nil
}
