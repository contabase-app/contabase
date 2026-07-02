package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
)

const scheduledPaymentNotificationType = "transaction.scheduled_payment.executed"

type scheduledPaymentDueRow struct {
	ID          string
	WorkspaceID string
	UserID      string
	CreatedBy   sql.NullString
	Type        string
	Amount      int64
	AccountID   string
	AccountType string
	Description string
	ScheduledAt int64
	PaidAt      sql.NullInt64
}

func StartScheduledPaymentExecutor(ctx context.Context, db *sql.DB, interval time.Duration) {
	if db == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		runScheduledPaymentExecutor(ctx, db, time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				runScheduledPaymentExecutor(ctx, db, now)
			}
		}
	}()
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
	if db == nil {
		return 0, nil
	}
	rows, err := queryDueScheduledPayments(ctx, db, now)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, row := range rows {
		ok, err := executeScheduledPayment(ctx, db, row, now)
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
	tomorrowStart := appNextDayStartUnix(now)
	rows, err := db.QueryContext(ctx, `
		SELECT t.id, t.workspace_id, t.user_id, t.scheduled_payment_created_by, t.type, t.amount,
		       t.account_id, a.type, t.description, t.scheduled_payment_at, t.paid_at
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.status = 'pending'
		  AND t.scheduled_payment_at IS NOT NULL
		  AND t.scheduled_payment_executed_at IS NULL
		  AND t.scheduled_payment_at < ?
		  AND t.type IN (?, ?)
		  AND t.destination_account_id IS NULL
		  AND t.invoice_id IS NULL
		  AND a.type != ?
		ORDER BY t.scheduled_payment_at ASC, t.created_at ASC
	`, tomorrowStart, models.TransactionTypeIncome, models.TransactionTypeExpense, models.AccountTypeCreditCard)
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
			&row.PaidAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func executeScheduledPayment(ctx context.Context, db *sql.DB, row scheduledPaymentDueRow, now time.Time) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	nowUnix := now.Unix()
	tomorrowStart := appNextDayStartUnix(now)
	res, err := tx.ExecContext(ctx, `
		UPDATE transactions
		SET status = 'paid',
		    paid_at = scheduled_payment_at,
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
	`, nowUnix, nowUnix, row.ID, row.WorkspaceID, row.ScheduledAt, tomorrowStart, models.TransactionTypeIncome, models.TransactionTypeExpense)
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
	if err := insertAutomaticPaymentHistoryTx(tx, row, actorID, nowUnix); err != nil {
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

func insertAutomaticPaymentHistoryTx(tx *sql.Tx, row scheduledPaymentDueRow, actorID string, now int64) error {
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
	`, uuid.NewString(), row.ID, row.WorkspaceID, actorID, previousPaidAt, row.ScheduledAt, now)
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
