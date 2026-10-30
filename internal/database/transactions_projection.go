package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// These are the v1 financial fields, not a physical SELECT * order. Historical
// installations have different ALTER COLUMN orders. Preserve their cid order
// when projecting confirmation evidence; the technical key never enters v1.
var transactionFinancialColumnsV1 = strings.Fields(`id workspace_id user_id account_id destination_account_id
 category_id invoice_id invoice_override_id type amount date description notes attachment_path
 installment_number total_installments parent_id recurring_rule_id recurrence_sequence series_id
 is_recurring_exception recurring_exception_reason payment_status status due_date paid_at
 scheduled_payment_at scheduled_payment_created_by scheduled_payment_executed_at contact_id
 created_at updated_at date_civil due_date_civil paid_date_civil scheduled_payment_date_civil effect_mode`)

func quoteAdmissionIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// TransactionEvidenceProjection preserves the pre-78 column names and physical
// order. It is intentionally independent from CBTX's logical column order.
func TransactionEvidenceProjection(ctx context.Context, tx *sql.Tx, alias string) (string, error) {
	cols, err := transactionV1PhysicalColumns(ctx, tx)
	if err != nil {
		return "", err
	}
	for i, col := range cols {
		cols[i] = quoteAdmissionIdentifier(alias) + "." + quoteAdmissionIdentifier(col)
	}
	return strings.Join(cols, ","), nil
}

func transactionV1PhysicalColumns(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,hidden FROM pragma_table_xinfo('transactions') ORDER BY cid`)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, 37)
	for _, col := range transactionFinancialColumnsV1 {
		want[col] = true
	}
	var out []string
	for rows.Next() {
		var name string
		var hidden int
		if err = rows.Scan(&name, &hidden); err != nil {
			break
		}
		if name == "admission_occurrence_key" && hidden == 0 {
			continue
		}
		if !want[name] || hidden != 0 {
			err = fmt.Errorf("unsupported transactions v1 schema")
			break
		}
		delete(want, name)
		out = append(out, name)
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(want) != 0 || len(out) != 37 {
		return nil, fmt.Errorf("unsupported transactions v1 schema")
	}
	return out, nil
}
