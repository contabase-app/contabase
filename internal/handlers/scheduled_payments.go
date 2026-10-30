package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const scheduledPaymentNotificationType = "transaction.scheduled_payment.executed"

type scheduledPaymentDueRow struct {
	ID                 string
	WorkspaceID        string
	UserID             string
	CreatedBy          sql.NullString
	Type               string
	Amount             int64
	AccountID          string
	AccountType        string
	Description        string
	ScheduledAt        int64
	CreatedAt          int64
	ScheduledDateCivil sql.NullString
	PaidAt             sql.NullInt64
}

func StartScheduledPaymentExecutor(ctx context.Context, db *sql.DB, interval time.Duration) <-chan struct{} {
	return startScheduledPaymentExecutor(ctx, db, interval, runScheduledPaymentExecutor)
}

func startScheduledPaymentExecutor(ctx context.Context, db *sql.DB, interval time.Duration, run func(context.Context, *sql.DB, time.Time)) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		defer close(done)
		run(ctx, db, time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				run(ctx, db, now)
			}
		}
	}()
	return done
}

func runScheduledPaymentExecutor(ctx context.Context, db *sql.DB, now time.Time) {
	count, err := RunDueScheduledPayments(ctx, db, now)
	if err != nil {
		log.Printf("scheduled payment executor failed: %v", err)
		return
	}
	if count > 0 {
		log.Printf("scheduled payment executor processed %d transaction(s)", count)
	}
}

func RunDueScheduledPayments(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	return runDueScheduledPayments(ctx, db, now, executeScheduledPayment)
}

func runDueScheduledPayments(ctx context.Context, db *sql.DB, now time.Time, execute func(context.Context, *sql.DB, scheduledPaymentDueRow, time.Time) (bool, error)) (int, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := queryDueScheduledPayments(ctx, db, now)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, row := range rows {
		ok, err := execute(ctx, db, row, now)
		if err != nil {
			return processed, err
		}
		if ok {
			processed++
		}
	}
	return processed, nil
}

func queryDueScheduledPayments(ctx context.Context, db *sql.DB, now time.Time) ([]scheduledPaymentDueRow, error) {
	// Enumerate only control-plane IDs and materialize every tenant's candidates
	// in one short snapshot. No financial execution may use this transaction.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var workspaces []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		workspaces = append(workspaces, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []scheduledPaymentDueRow
	tomorrowStart := appNextDayStartUnix(now)
	for _, workspaceID := range workspaces {
		candidates, err := queryWorkspaceDueScheduledPayments(ctx, tx, workspaceID, tomorrowStart)
		if err != nil {
			return nil, err
		}
		out = append(out, candidates...)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Keep the global runner's original order, with no new workspace tie-break.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ScheduledAt != out[j].ScheduledAt {
			return out[i].ScheduledAt < out[j].ScheduledAt
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out, nil
}

func queryWorkspaceDueScheduledPayments(ctx context.Context, tx *sql.Tx, workspaceID string, tomorrowStart int64) ([]scheduledPaymentDueRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT t.id, t.workspace_id, t.user_id, t.scheduled_payment_created_by, t.type, t.amount,
		       t.account_id, a.type, t.description, t.scheduled_payment_at, t.scheduled_payment_date_civil, t.paid_at, t.created_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.status = 'pending'
		  AND t.scheduled_payment_at IS NOT NULL
		  AND t.scheduled_payment_executed_at IS NULL
		  AND t.scheduled_payment_at < ?
		  AND t.type IN (?, ?)
		  AND t.destination_account_id IS NULL
		  AND t.invoice_id IS NULL
		  AND a.type != ?
		  AND NOT EXISTS (
		      SELECT 1
		      FROM invoice_payments ip
		      WHERE (ip.transaction_id = t.id OR ip.reversal_transaction_id = t.id)
		        AND ip.workspace_id = t.workspace_id
		  )
		ORDER BY t.scheduled_payment_at ASC, t.created_at ASC
	`, workspaceID, tomorrowStart, models.TransactionTypeIncome, models.TransactionTypeExpense, models.AccountTypeCreditCard)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []scheduledPaymentDueRow
	for rows.Next() {
		var row scheduledPaymentDueRow
		if err := rows.Scan(
			&row.ID,
			&row.WorkspaceID,
			&row.UserID,
			&row.CreatedBy,
			&row.Type,
			&row.Amount,
			&row.AccountID,
			&row.AccountType,
			&row.Description,
			&row.ScheduledAt,
			&row.ScheduledDateCivil,
			&row.PaidAt,
			&row.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func executeScheduledPayment(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time) (bool, error) {
	return executeScheduledPaymentWithRetry(ctx, db, row, now, nil)
}

// afterReload is a per-call synchronization hook for real SQLite conflict tests.
// Production callers leave it nil; it is never shared between executor runs.
func executeScheduledPaymentWithRetry(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time, afterReload func() error) (bool, error) {
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// Pass the original candidate by value. Each attempt opens a new
		// transaction and reloads every financial field; no snapshot is reused.
		ok, err := executeScheduledPaymentAttempt(ctx, db, row, now, afterReload)
		if ctxErr := ctx.Err(); ctxErr != nil && err != nil {
			return false, ctxErr
		}
		var sqliteErr *sqliteDriver.Error
		if err == nil || attempt == maxAttempts || !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_BUSY_SNAPSHOT {
			return ok, err
		}
		// The failed attempt has already rolled back before this bounded wait.
		// Other persistence/integrity errors retain the batch's fail-fast policy.
		timer := time.NewTimer(time.Duration(attempt) * 10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

func executeScheduledPaymentAttempt(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time, afterReload func() error) (executed bool, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				// Do not expose the original snapshot error to retry classification
				// when rollback itself failed and transaction state is uncertain.
				err = fmt.Errorf("scheduled payment rollback failed: %w (original error: %v)", rollbackErr, err)
			}
		}
	}()

	nowUnix := now.Unix()
	tomorrowStart := appNextDayStartUnix(now)

	// Candidate selection precedes this transaction. Reload the eligible row so
	// the payment, balance, history and notification all use the current version.
	err = tx.QueryRowContext(ctx, `
		SELECT t.id, t.workspace_id, t.user_id, t.scheduled_payment_created_by, t.type, t.amount,
		       t.account_id, a.type, t.description, t.scheduled_payment_at, t.scheduled_payment_date_civil, t.paid_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.id = ? AND t.workspace_id = ?
		  AND t.status = 'pending'
		  AND t.scheduled_payment_at = ?
		  AND t.scheduled_payment_at < ?
		  AND t.scheduled_payment_executed_at IS NULL
		  AND t.type IN (?, ?)
		  AND t.destination_account_id IS NULL
		  AND t.invoice_id IS NULL
		  AND a.type != ?
		  AND NOT EXISTS (
		      SELECT 1 FROM invoice_payments ip
		      WHERE (ip.transaction_id = t.id OR ip.reversal_transaction_id = t.id)
		        AND ip.workspace_id = t.workspace_id
		  )
	`, row.ID, row.WorkspaceID, row.ScheduledAt, tomorrowStart, models.TransactionTypeIncome, models.TransactionTypeExpense, models.AccountTypeCreditCard).Scan(
		&row.ID, &row.WorkspaceID, &row.UserID, &row.CreatedBy, &row.Type, &row.Amount,
		&row.AccountID, &row.AccountType, &row.Description, &row.ScheduledAt, &row.ScheduledDateCivil, &row.PaidAt,
	)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if afterReload != nil {
		if err := afterReload(); err != nil {
			return false, err
		}
	}

	// Resolve paid_at and paid_date_civil for the payment.
	var paidAt int64
	var paidDateCivil string
	if row.ScheduledDateCivil.Valid && row.ScheduledDateCivil.String != "" {
		// New schedule: civil date is the source.
		cd, cerr := ParseCivilDateInput(row.ScheduledDateCivil.String)
		if cerr != nil {
			return false, fmt.Errorf("scheduled payment %s has invalid civil date %q: %w", row.ID, row.ScheduledDateCivil.String, cerr)
		}
		paidDateCivil = FormatCivilDate(cd)
		paidAt = CivilDateToLegacyUnixNoonUTC(cd)
	} else if row.ScheduledAt > 0 {
		// Legacy schedule: convert INTEGER using app timezone to get
		// the civil date it represents, then derive the canonical
		// paid_at via CivilDateToLegacyUnixNoonUTC.
		loc := appTimeLocation()
		instant := time.Unix(row.ScheduledAt, 0).In(loc)
		dateStr := fmt.Sprintf("%04d-%02d-%02d", instant.Year(), instant.Month(), instant.Day())
		cd, cerr := ParseCivilDateInput(dateStr)
		if cerr != nil {
			return false, fmt.Errorf("scheduled payment %s legacy timestamp %d cannot be resolved: %w", row.ID, row.ScheduledAt, cerr)
		}
		paidDateCivil = FormatCivilDate(cd)
		paidAt = CivilDateToLegacyUnixNoonUTC(cd)
	} else {
		return false, fmt.Errorf("scheduled payment %s has no schedule date", row.ID)
	}

	// The UPDATE marks the transaction as paid and records execution.
	// Schedule columns (scheduled_payment_at, scheduled_payment_date_civil)
	// are NOT cleared here — only scheduled_payment_executed_at marks
	// execution. Explicit clear_schedule remains the action that clears them.
	res, err := tx.ExecContext(ctx, `
		UPDATE transactions
		SET status = 'paid',
		    paid_at = ?,
		    paid_date_civil = ?,
		    scheduled_payment_executed_at = ?,
		    updated_at = ?
		WHERE id = ?
		  AND workspace_id = ?
		  AND status = 'pending'
		  AND scheduled_payment_at = ?
		  AND scheduled_payment_at < ?
		  AND scheduled_payment_executed_at IS NULL
		  AND invoice_id IS NULL
		  AND destination_account_id IS NULL
		  AND type IN (?, ?)
		  AND NOT EXISTS (
		      SELECT 1
		      FROM invoice_payments ip
		      WHERE (ip.transaction_id = transactions.id OR ip.reversal_transaction_id = transactions.id)
		        AND ip.workspace_id = transactions.workspace_id
		  )
	`, paidAt, paidDateCivil, nowUnix, nowUnix, row.ID, row.WorkspaceID, row.ScheduledAt, tomorrowStart, models.TransactionTypeIncome, models.TransactionTypeExpense)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		committed = true
		return false, nil
	}
	if affected != 1 {
		return false, fmt.Errorf("scheduled payment update affected %d rows for transaction %s", affected, row.ID)
	}

	if err := services.ApplyBalanceEffect(tx, row.WorkspaceID, row.Type, row.AccountType, models.PaymentStatusPaid, row.Amount, row.AccountID, "", nowUnix); err != nil {
		return false, err
	}

	actorID := strings.TrimSpace(row.UserID)
	if row.CreatedBy.Valid && strings.TrimSpace(row.CreatedBy.String) != "" {
		actorID = strings.TrimSpace(row.CreatedBy.String)
	}
	if err := insertAutomaticPaymentHistoryTx(tx, row, actorID, paidAt, nowUnix); err != nil {
		return false, err
	}
	if err := insertScheduledPaymentNotificationTx(tx, row, actorID, nowUnix); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

func insertAutomaticPaymentHistoryTx(tx *sql.Tx, row scheduledPaymentDueRow, actorID string, newPaidAt int64, now int64) error {
	var previousPaidAt interface{}
	if row.PaidAt.Valid {
		previousPaidAt = row.PaidAt.Int64
	}
	_, err := tx.Exec(`
		INSERT INTO transaction_status_history (
			id, transaction_id, workspace_id, user_id, previous_status, new_status,
			previous_paid_at, new_paid_at, source, created_at
		)
		VALUES (?, ?, ?, ?, 'pending', 'paid', ?, ?, 'automatic', ?)
	`, uuid.NewString(), row.ID, row.WorkspaceID, actorID, previousPaidAt, newPaidAt, now)
	return err
}

func insertScheduledPaymentNotificationTx(tx *sql.Tx, row scheduledPaymentDueRow, userID string, now int64) error {
	title := "Pagamento automático executado"
	if row.Type == models.TransactionTypeIncome {
		title = "Recebimento automático executado"
	}
	description := strings.TrimSpace(row.Description)
	if description == "" {
		description = "Lançamento"
	}
	message := fmt.Sprintf("%s em %s.", description, formatAppDateLabelFromUnix(row.ScheduledAt))
	dedupeKey := fmt.Sprintf("scheduled_payment:%s:%d", row.ID, row.ScheduledAt)
	_, err := tx.Exec(`
		INSERT OR IGNORE INTO user_notifications (
			id, user_id, workspace_id, title, message, type, dedupe_key, is_read, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
	`, uuid.NewString(), userID, row.WorkspaceID, title, message, scheduledPaymentNotificationType, dedupeKey, now)
	return err
}
