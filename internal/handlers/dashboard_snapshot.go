package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/services"
)

func buildDashboardV2DataAtContext(ctx context.Context, db *sql.DB, userID, workspaceID string, now time.Time, cashFlowRange, monthRaw string) DashboardPreviewData {
	data, err := buildDashboardV2Snapshot(ctx, db, userID, workspaceID, now, cashFlowRange, monthRaw, true)
	if err != nil {
		log.Printf("dashboard snapshot unavailable: %v", err)
		return DashboardPreviewData{Financial: PreviewFinancialView{Error: true, ErrorMessage: "Não foi possível carregar o painel."}}
	}
	return data
}

func dashboardCardIDs(q reportQueryer, workspaceID string) ([]string, error) {
	rows, err := q.Query(`SELECT a.id FROM accounts a WHERE a.workspace_id=? AND a.type='CREDIT_CARD' AND a.archived_at IS NULL AND (EXISTS (SELECT 1 FROM credit_cards cc WHERE cc.account_id=a.id) OR EXISTS (SELECT 1 FROM invoices i WHERE i.account_id=a.id AND i.status IN ('OPEN','CLOSED'))) ORDER BY a.sort_order, a.name`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// These are the same lifecycle calls previously made by buildDashboardDataAt
// and its per-card resolver. They finish before any read snapshot is opened.
func prepareDashboardInvoices(db *sql.DB, workspaceID string, now time.Time) error {
	if err := autoCloseWorkspaceInvoices(db, workspaceID); err != nil {
		return err
	}
	ids, err := dashboardCardIDs(db, workspaceID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, _, _, _, _, err := resolveDashboardInvoice(db, workspaceID, id, now.Unix()); err != nil {
			return err
		}
	}
	return nil
}

func buildDashboardV2Snapshot(ctx context.Context, db *sql.DB, userID, workspaceID string, now time.Time, cashFlowRange, monthRaw string, prepare bool) (DashboardPreviewData, error) {
	if db == nil || strings.TrimSpace(workspaceID) == "" {
		return DashboardPreviewData{}, fmt.Errorf("dashboard: banco e workspace obrigatórios")
	}
	if err := ctx.Err(); err != nil {
		return DashboardPreviewData{}, err
	}
	if prepare {
		if err := prepareDashboardInvoices(db, workspaceID, now); err != nil {
			return DashboardPreviewData{}, fmt.Errorf("preparar faturas: %w", err)
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DashboardPreviewData{}, err
	}
	defer tx.Rollback()
	// Anchor the SQLite snapshot and reject a card introduced after preparation
	// without trying to repair it through another connection during this read.
	ids, err := dashboardCardIDs(tx, workspaceID)
	if err != nil {
		return DashboardPreviewData{}, err
	}
	for _, id := range ids {
		if _, _, _, _, _, err := resolveDashboardInvoiceRead(tx, workspaceID, id); err != nil {
			return DashboardPreviewData{}, fmt.Errorf("fatura indisponível no snapshot: %w", err)
		}
	}
	data, err := buildDashboardV2DataFromSnapshot(ctx, tx, userID, workspaceID, now, cashFlowRange, monthRaw)
	if err := ctx.Err(); err != nil {
		return DashboardPreviewData{}, err
	}
	if err != nil {
		return DashboardPreviewData{}, err
	}
	if data.Financial.Error {
		return DashboardPreviewData{}, fmt.Errorf("projeção indisponível no snapshot")
	}
	return data, nil
}

func dashboardFinancialPosition(q reportQueryer, workspaceID string) (services.WorkspaceFinancialPositionF1, error) {
	if tx := dashboardReadTx(q); tx != nil {
		probe := loadWorkspaceObligationsF1
		if _, ok := q.(*dashboardSnapshotReader); ok {
			probe = loadWorkspaceObligationsF1Strict
		}
		return services.CalculateWorkspaceFinancialPositionF1Tx(context.Background(), tx, workspaceID, probe)
	}
	return services.CalculateWorkspaceFinancialPositionF1(q.(*sql.DB), workspaceID, loadWorkspaceObligationsF1)
}

func resolveDashboardInvoiceReader(q reportQueryer, workspaceID, accountID string, refDate int64) (string, string, string, int64, int64, error) {
	if db, ok := q.(*sql.DB); ok {
		return resolveDashboardInvoice(db, workspaceID, accountID, refDate)
	}
	return resolveDashboardInvoiceRead(q, workspaceID, accountID)
}

func resolveDashboardInvoiceRead(q reportQueryer, workspaceID, accountID string) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	err = q.QueryRow(`SELECT i.id, i.reference, i.status, i.closing_date, i.due_date
		FROM invoices i JOIN accounts a ON a.id=i.account_id
		WHERE i.account_id=? AND a.workspace_id=? AND i.status IN ('CLOSED','OPEN')
		ORDER BY CASE status WHEN 'CLOSED' THEN 0 ELSE 1 END, closing_date ASC LIMIT 1`, accountID, workspaceID).Scan(&invoiceID, &reference, &status, &closingUnix, &dueUnix)
	return
}

// Legacy helpers can return empty data on failure. Retain their iterators so
// late read/close errors also invalidate the composition before rendering.
type dashboardSnapshotReader struct {
	*sql.Tx
	err  error
	rows []*sql.Rows
}

func recordDashboardReadError(q reportQueryer, err error) {
	if reader, ok := q.(*dashboardSnapshotReader); ok {
		reader.record(err)
	}
}

func (q *dashboardSnapshotReader) readError() error {
	for _, rows := range q.rows {
		q.record(rows.Close())
		q.record(rows.Err())
	}
	return q.err
}

func (q *dashboardSnapshotReader) record(err error) {
	if q.err == nil && err != nil {
		q.err = err
	}
}
func (q *dashboardSnapshotReader) Query(query string, args ...any) (*sql.Rows, error) {
	rows, err := q.Tx.Query(query, args...)
	q.record(err)
	if rows != nil {
		q.rows = append(q.rows, rows)
	}
	return rows, err
}
func (q *dashboardSnapshotReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := q.Tx.QueryContext(ctx, query, args...)
	q.record(err)
	if rows != nil {
		q.rows = append(q.rows, rows)
	}
	return rows, err
}
func (q *dashboardSnapshotReader) QueryRow(query string, args ...any) *sql.Row {
	row := q.Tx.QueryRow(query, args...)
	q.record(row.Err())
	return row
}
func (q *dashboardSnapshotReader) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	row := q.Tx.QueryRowContext(ctx, query, args...)
	q.record(row.Err())
	return row
}
func dashboardReadTx(q reportQueryer) *sql.Tx {
	switch q := q.(type) {
	case *sql.Tx:
		return q
	case *dashboardSnapshotReader:
		return q.Tx
	default:
		return nil
	}
}
