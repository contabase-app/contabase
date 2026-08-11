package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

type ReportPeriod struct {
	Start int64
	End   int64
}

func NewReportPeriodFromMonth(year, month int) ReportPeriod {
	start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second).Unix()
	return ReportPeriod{Start: start, End: end}
}

func NewReportPeriodFromYear(year int) ReportPeriod {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second).Unix()
	return ReportPeriod{Start: start, End: end}
}

type CashEvent struct {
	DateUnix    int64
	AmountCents int64
	Source      string
	CategoryID  *string
	AccountID   string
	MacroGroup  string
	IsRealized  bool
	IsIncome    bool
	Description string
}

const (
	CashSourceTransaction      = "transaction"
	CashSourceInvoicePayment   = "invoice_payment"
	CashSourceInvoiceProjected = "invoice_projected"
)

type DRERowView struct {
	Competencia string
	MacroGroup  string
	AmountCents int64
}

type StatementRow struct {
	UnixDate    int64
	AmountCents int64
	Description string
	AccountName string
	IsRealized  bool
	Type        string
}

type StatementExtratoRow struct {
	UnixDate     int64
	DateLabel    string
	Description  string
	AccountName  string
	ContactName  string
	CategoryName string
	MacroGroup   string
	Tipo         string
	Status       string
	IsRealized   bool
	IsTransfer   bool
	AmountCents  int64
	Entrada      MoneyDisplay
	Saida        MoneyDisplay
}

type AccountingCloseData struct {
	OpeningBalance       int64
	TotalIncomeRealized  int64
	TotalExpenseRealized int64
	NetResult            int64
	ClosingBalance       int64
	RealizedNetCashflow  int64
	ProjectedNetCashflow int64
}

func ResolveEffectiveCashDate(tx models.Transaction) int64 {
	if tx.PaidAt != nil && *tx.PaidAt > 0 {
		return *tx.PaidAt
	}
	return tx.Date
}

func ResolveCompetenceDate(tx models.Transaction) int64 {
	return tx.Date
}

func ResolveForecastDate(tx models.Transaction) int64 {
	if tx.DueDate != nil && *tx.DueDate > 0 {
		return *tx.DueDate
	}
	return tx.Date
}

func QueryRealizedCashEvents(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			t.id,
			COALESCE(t.description, '') AS description,
			t.amount,
			t.date,
			t.paid_at,
			t.type,
			t.account_id,
			COALESCE(a.name, '') AS account_name,
			COALESCE(a.type, '') AS account_type,
			t.category_id,
			COALESCE(c.macro_group, p.macro_group, '') AS macro_group
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ?
				AND t.status = 'paid'
				AND t.type != 'TRANSFER'
				AND a.type != 'CREDIT_CARD'
				AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ? AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
				AND `+excludeInvoicePaymentCompetenceClause("t")+`
			ORDER BY COALESCE(NULLIF(t.paid_at, 0), t.date) ASC
		`, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, description, txType, accountID, accountName, accountType, macroGroup sql.NullString
		var amount, date int64
		var paidAt sql.NullInt64
		var categoryID sql.NullString
		if err := rows.Scan(&id, &description, &amount, &date, &paidAt, &txType,
			&accountID, &accountName, &accountType, &categoryID, &macroGroup); err != nil {
			return nil, err
		}
		effectiveDate := date
		if paidAt.Valid && paidAt.Int64 > 0 {
			effectiveDate = paidAt.Int64
		}
		ev := CashEvent{
			DateUnix:    effectiveDate,
			AmountCents: amount,
			Source:      CashSourceTransaction,
			AccountID:   accountID.String,
			Description: description.String,
			IsRealized:  true,
			IsIncome:    txType.String == models.TransactionTypeIncome,
		}
		if categoryID.Valid {
			ev.CategoryID = &categoryID.String
		}
		if macroGroup.Valid {
			ev.MacroGroup = macroGroup.String
		}
		if ev.IsIncome {
			ev.Description = description.String
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func QueryProjectedCashEvents(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			t.id,
			COALESCE(t.description, '') AS description,
			t.amount,
			t.date,
			COALESCE(t.due_date, t.date) AS forecast_date,
			t.type,
			t.account_id,
			COALESCE(a.name, '') AS account_name,
			COALESCE(a.type, '') AS account_type,
			t.category_id,
			COALESCE(c.macro_group, p.macro_group, '') AS macro_group
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			AND t.status = 'pending'
			AND t.type != 'TRANSFER'
			AND a.type != 'CREDIT_CARD'
			AND COALESCE(t.due_date, t.date) >= ? AND COALESCE(t.due_date, t.date) <= ?
		ORDER BY COALESCE(t.due_date, t.date) ASC
	`, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, description, txType, accountID, accountName, accountType, macroGroup sql.NullString
		var amount, date, forecastDate int64
		var categoryID sql.NullString
		if err := rows.Scan(&id, &description, &amount, &date, &forecastDate, &txType,
			&accountID, &accountName, &accountType, &categoryID, &macroGroup); err != nil {
			return nil, err
		}
		ev := CashEvent{
			DateUnix:    forecastDate,
			AmountCents: amount,
			Source:      CashSourceTransaction,
			AccountID:   accountID.String,
			Description: description.String,
			IsRealized:  false,
			IsIncome:    txType.String == models.TransactionTypeIncome,
		}
		if categoryID.Valid {
			ev.CategoryID = &categoryID.String
		}
		if macroGroup.Valid {
			ev.MacroGroup = macroGroup.String
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func QueryInvoiceCashEvents(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			ip.id,
			ip.invoice_id,
			ip.account_id,
			ip.amount_cents,
			ip.paid_at,
			ip.reversed_at,
			COALESCE(a.name, '') AS account_name,
			COALESCE(i.account_id, '') AS card_account_id,
			COALESCE(ca.name, '') AS card_name,
			i.reference AS invoice_ref
		FROM invoice_payments ip
		JOIN accounts a ON a.id = ip.account_id AND a.workspace_id = ip.workspace_id
		JOIN invoices i ON i.id = ip.invoice_id
		JOIN accounts ca ON ca.id = i.account_id AND ca.workspace_id = ip.workspace_id
		WHERE ip.workspace_id = ?
			AND (
				(ip.paid_at >= ? AND ip.paid_at <= ?)
				OR (ip.reversed_at >= ? AND ip.reversed_at <= ?)
			)
		ORDER BY ip.paid_at ASC
	`, workspaceID, period.Start, period.End, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, invoiceID, accountID, accountName, cardAccountID, cardName, invoiceRef sql.NullString
		var amountCents, paidAt int64
		var reversedAt sql.NullInt64
		if err := rows.Scan(&id, &invoiceID, &accountID, &amountCents, &paidAt, &reversedAt,
			&accountName, &cardAccountID, &cardName, &invoiceRef); err != nil {
			return nil, err
		}
		if paidAt >= period.Start && paidAt <= period.End {
			events = append(events, CashEvent{
				DateUnix:    paidAt,
				AmountCents: amountCents,
				Source:      CashSourceInvoicePayment,
				AccountID:   accountID.String,
				Description: "Pagamento Fatura " + cardName.String + " - " + invoiceRef.String,
				IsRealized:  true,
				IsIncome:    false,
			})
		}
		if reversedAt.Valid && reversedAt.Int64 >= period.Start && reversedAt.Int64 <= period.End {
			events = append(events, CashEvent{
				DateUnix:    reversedAt.Int64,
				AmountCents: amountCents,
				Source:      CashSourceInvoicePayment,
				AccountID:   accountID.String,
				Description: "Reversão pagamento Fatura " + cardName.String + " - " + invoiceRef.String,
				IsRealized:  true,
				IsIncome:    true,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func QueryInvoiceProjectedCashEvents(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			i.id,
			i.due_date,
			COALESCE(a.name, '') AS card_name,
			i.reference AS invoice_ref,
			i.status,
			COALESCE((
				SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
				FROM transactions t
				WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
			), 0) AS total,
			`+invoiceEffectivePaidSQLExpr+` AS paid
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE a.workspace_id = ?
			AND i.status IN ('OPEN', 'CLOSED')
			AND i.due_date >= ? AND i.due_date <= ?
		ORDER BY i.due_date ASC
	`, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, cardName, invoiceRef, status sql.NullString
		var dueDate, total, paid int64
		if err := rows.Scan(&id, &dueDate, &cardName, &invoiceRef, &status, &total, &paid); err != nil {
			return nil, err
		}
		pending := total - paid
		if pending <= 0 {
			continue
		}
		ev := CashEvent{
			DateUnix:    dueDate,
			AmountCents: pending,
			Source:      CashSourceInvoiceProjected,
			AccountID:   "",
			Description: "Fatura prevista " + cardName.String + " - " + invoiceRef.String,
			IsRealized:  false,
			IsIncome:    false,
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func BuildCashFlowData(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, []CashEvent, error) {
	realized, err := QueryRealizedCashEvents(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	invoiceRealized, err := QueryInvoiceCashEvents(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	allRealized := make([]CashEvent, 0, len(realized)+len(invoiceRealized))
	allRealized = append(allRealized, realized...)
	allRealized = append(allRealized, invoiceRealized...)

	projected, err := QueryProjectedCashEvents(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	invoiceProjected, err := QueryInvoiceProjectedCashEvents(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	allProjected := make([]CashEvent, 0, len(projected)+len(invoiceProjected))
	allProjected = append(allProjected, projected...)
	allProjected = append(allProjected, invoiceProjected...)

	return allRealized, allProjected, nil
}

// BuildCashFlowDataByDueDate is the competence-style cash-flow variant that
// classifies realized and pending events by due date.
func BuildCashFlowDataByDueDate(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, []CashEvent, error) {
	realized, err := QueryRealizedCashEventsByDueDate(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	projected, err := QueryProjectedCashEventsByDueDate(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	invoiceEvents, err := QueryInvoiceCashEventsByDueDate(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	for _, ev := range invoiceEvents {
		if ev.IsRealized {
			realized = append(realized, ev)
		} else {
			projected = append(projected, ev)
		}
	}
	return realized, projected, nil
}

// BuildPrevisaoCashFlowData returns the Fluxo de Caixa forecast contract:
// realized cash by paid_at plus pending cash by due_date. Paid invoice payments
// are realized cash; open/closed invoice balances are pending cash.
func BuildPrevisaoCashFlowData(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, []CashEvent, error) {
	realized, err := BuildRealizadoCashFlowData(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	projected, err := QueryProjectedCashEventsByDueDate(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	invoiceProjected, err := QueryInvoiceCashEventsByDueDate(db, workspaceID, period)
	if err != nil {
		return nil, nil, err
	}
	projected = append(projected, invoiceProjected...)
	return realized, projected, nil
}

// BuildRealizadoCashFlowData retorna apenas eventos de caixa efetivamente
// realizados (status = paid), usando a data de pagamento (paid_at) como base.
// Exclui transferências e compras individuais em cartão de crédito.
// Pagamentos de fatura entram como saída realizada.
func BuildRealizadoCashFlowData(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			t.id,
			COALESCE(t.description, '') AS description,
			t.amount,
			COALESCE(NULLIF(t.paid_at, 0), t.date) AS effective_date,
			t.type,
			t.account_id,
			COALESCE(a.name, '') AS account_name,
			COALESCE(a.type, '') AS account_type,
			t.category_id,
			COALESCE(c.macro_group, p.macro_group, CASE WHEN ip.id IS NOT NULL THEN 'Fatura de cartão' ELSE 'Sem grupo macro' END) AS macro_group,
			ip.id IS NOT NULL AS is_invoice_payment
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		LEFT JOIN invoice_payments ip ON ip.transaction_id = t.id AND ip.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
			AND t.status = 'paid'
			AND t.type IN ('INCOME', 'EXPENSE')
			AND a.type != 'CREDIT_CARD'
			AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
			AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
		ORDER BY COALESCE(NULLIF(t.paid_at, 0), t.date) ASC
	`, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, description, txType, accountID, accountName, accountType, macroGroup sql.NullString
		var amount, effectiveDate int64
		var categoryID sql.NullString
		var isInvoicePayment bool
		if err := rows.Scan(&id, &description, &amount, &effectiveDate, &txType,
			&accountID, &accountName, &accountType, &categoryID, &macroGroup, &isInvoicePayment); err != nil {
			return nil, err
		}
		ev := CashEvent{
			DateUnix:    effectiveDate,
			AmountCents: amount,
			Source:      CashSourceTransaction,
			AccountID:   accountID.String,
			Description: description.String,
			IsRealized:  true,
			IsIncome:    txType.String == models.TransactionTypeIncome,
		}
		if isInvoicePayment {
			ev.Source = CashSourceInvoicePayment
		}
		if categoryID.Valid {
			ev.CategoryID = &categoryID.String
		}
		if macroGroup.Valid {
			ev.MacroGroup = macroGroup.String
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func QueryRealizedCashEventsByDueDate(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			t.id,
			COALESCE(t.description, '') AS description,
			t.amount,
			COALESCE(NULLIF(t.due_date, 0), t.date) AS effective_date,
			t.type,
			t.account_id,
			COALESCE(a.name, '') AS account_name,
			COALESCE(a.type, '') AS account_type,
			t.category_id,
			COALESCE(c.macro_group, p.macro_group, '') AS macro_group
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			AND t.status = 'paid'
			AND t.type != 'TRANSFER'
			AND a.type != 'CREDIT_CARD'
			AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
			AND `+excludeInvoicePaymentCompetenceClause("t")+`
		ORDER BY COALESCE(NULLIF(t.due_date, 0), t.date) ASC
	`, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, description, txType, accountID, accountName, accountType, macroGroup sql.NullString
		var amount, effectiveDate int64
		var categoryID sql.NullString
		if err := rows.Scan(&id, &description, &amount, &effectiveDate, &txType,
			&accountID, &accountName, &accountType, &categoryID, &macroGroup); err != nil {
			return nil, err
		}
		ev := CashEvent{
			DateUnix:    effectiveDate,
			AmountCents: amount,
			Source:      CashSourceTransaction,
			AccountID:   accountID.String,
			Description: description.String,
			IsRealized:  true,
			IsIncome:    txType.String == models.TransactionTypeIncome,
		}
		if categoryID.Valid {
			ev.CategoryID = &categoryID.String
		}
		if macroGroup.Valid {
			ev.MacroGroup = macroGroup.String
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func QueryProjectedCashEventsByDueDate(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			t.id,
			COALESCE(t.description, '') AS description,
			t.amount,
			COALESCE(NULLIF(t.due_date, 0), t.date) AS effective_date,
			t.type,
			t.account_id,
			COALESCE(a.name, '') AS account_name,
			COALESCE(a.type, '') AS account_type,
			t.category_id,
			COALESCE(c.macro_group, p.macro_group, '') AS macro_group
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			AND t.status = 'pending'
				AND t.type != 'TRANSFER'
				AND a.type != 'CREDIT_CARD'
				AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
				AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
				AND COALESCE(t.description, '') NOT LIKE ?
				AND `+excludeInvoicePaymentCompetenceClause("t")+`
			ORDER BY COALESCE(NULLIF(t.due_date, 0), t.date) ASC
	`, workspaceID, period.Start, period.End, invoicePaymentDescriptionPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, description, txType, accountID, accountName, accountType, macroGroup sql.NullString
		var amount, effectiveDate int64
		var categoryID sql.NullString
		if err := rows.Scan(&id, &description, &amount, &effectiveDate, &txType,
			&accountID, &accountName, &accountType, &categoryID, &macroGroup); err != nil {
			return nil, err
		}
		ev := CashEvent{
			DateUnix:    effectiveDate,
			AmountCents: amount,
			Source:      CashSourceTransaction,
			AccountID:   accountID.String,
			Description: description.String,
			IsRealized:  false,
			IsIncome:    txType.String == models.TransactionTypeIncome,
		}
		if categoryID.Valid {
			ev.CategoryID = &categoryID.String
		}
		if macroGroup.Valid {
			ev.MacroGroup = macroGroup.String
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func QueryInvoiceCashEventsByDueDate(db *sql.DB, workspaceID string, period ReportPeriod) ([]CashEvent, error) {
	rows, err := db.Query(`
		SELECT
			i.id,
			i.due_date,
			COALESCE(a.name, '') AS card_name,
			i.reference AS invoice_ref,
			i.status,
				COALESCE((
					SELECT SUM(t.amount)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) AS total,
				`+invoiceEffectivePaidSQLExpr+` AS paid
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
				AND i.status IN ('OPEN', 'CLOSED')
				AND i.due_date >= ? AND i.due_date <= ?
		ORDER BY i.due_date ASC
	`, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []CashEvent
	for rows.Next() {
		var id, cardName, invoiceRef, status sql.NullString
		var dueDate, total, paid int64
		if err := rows.Scan(&id, &dueDate, &cardName, &invoiceRef, &status, &total, &paid); err != nil {
			return nil, err
		}
		pending := total - paid
		if pending <= 0 {
			continue
		}
		ev := CashEvent{
			DateUnix:    dueDate,
			AmountCents: pending,
			Source:      CashSourceInvoiceProjected,
			AccountID:   "",
			Description: "Fatura " + cardName.String + " - " + invoiceRef.String,
			IsRealized:  false,
			IsIncome:    false,
		}
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

func SumCashEvents(events []CashEvent) int64 {
	var total int64
	for _, ev := range events {
		if ev.IsIncome {
			total += ev.AmountCents
		} else {
			total -= ev.AmountCents
		}
	}
	return total
}

func SumCashIncome(events []CashEvent) int64 {
	var total int64
	for _, ev := range events {
		if ev.IsIncome {
			total += ev.AmountCents
		}
	}
	return total
}

func SumCashExpense(events []CashEvent) int64 {
	var total int64
	for _, ev := range events {
		if !ev.IsIncome {
			total += ev.AmountCents
		}
	}
	return total
}

func BuildProjectedBalanceUntilPeriodEnd(db *sql.DB, workspaceID string, periodEnd int64, now time.Time) (int64, bool, error) {
	now = now.UTC()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	if periodEnd < todayStart {
		return 0, false, nil
	}

	var currentBalance int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(current_balance), 0)
		FROM accounts
		WHERE workspace_id = ?
			AND type != 'CREDIT_CARD'
			AND archived_at IS NULL
	`, workspaceID).Scan(&currentBalance); err != nil {
		return 0, false, err
	}

	var pendingIncome, pendingExpense int64
	if err := db.QueryRow(`
		SELECT
			COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END), 0)
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
			AND t.status = 'pending'
			AND t.type IN ('INCOME', 'EXPENSE')
			AND a.type != 'CREDIT_CARD'
			AND COALESCE(t.description, '') NOT LIKE ?
			AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
	`, workspaceID, invoicePaymentDescriptionPrefix+"%", todayStart, periodEnd).Scan(&pendingIncome, &pendingExpense); err != nil {
		return 0, false, err
	}

	var pendingInvoices int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(CASE WHEN pending_amount > 0 THEN pending_amount ELSE 0 END), 0)
		FROM (
			SELECT
				COALESCE((
					SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
					FROM transactions t
					WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
				), 0) -
				`+invoiceEffectivePaidSQLExpr+` AS pending_amount
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ?
				AND i.status IN ('OPEN', 'CLOSED')
				AND i.due_date >= ?
				AND i.due_date <= ?
		)
	`, workspaceID, todayStart, periodEnd).Scan(&pendingInvoices); err != nil {
		return 0, false, err
	}

	return currentBalance + pendingIncome - pendingExpense - pendingInvoices, true, nil
}

func BuildDREData(db *sql.DB, workspaceID string, year int) ([]DRERowView, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	rows, err := db.Query(`
		SELECT
			strftime('%Y-%m', datetime(t.date, 'unixepoch')) AS competencia,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			COALESCE(SUM(CASE WHEN t.type = 'INCOME' THEN t.amount ELSE -t.amount END), 0) AS total
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			AND t.type IN ('INCOME', 'EXPENSE')
			AND t.date >= ? AND t.date < ?
			AND `+excludeInvoicePaymentCompetenceClause("t")+`
		GROUP BY
			strftime('%Y-%m', datetime(t.date, 'unixepoch')),
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro')
		ORDER BY
			strftime('%Y-%m', datetime(t.date, 'unixepoch')) ASC,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') ASC
	`, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DRERowView
	for rows.Next() {
		var competencia, macroGroup string
		var amount int64
		if err := rows.Scan(&competencia, &macroGroup, &amount); err != nil {
			return nil, err
		}
		out = append(out, DRERowView{
			Competencia: competencia,
			MacroGroup:  canonicalMacroGroup(macroGroup),
			AmountCents: amount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []DRERowView{}
	}
	return out, nil
}

type CaixaRowView struct {
	Vencimento  string
	MacroGroup  string
	AmountCents int64
}

// BuildCaixaData returns the forecast statement by macro group for the
// selected month plus the next 12 months: realized cash by paid_at plus pending
// cash and open invoice balances by due date. Transfers and individual
// credit-card purchases are excluded from cash.
func BuildCaixaData(db *sql.DB, workspaceID string, year, month int) ([]CaixaRowView, error) {
	windowStart := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	start := windowStart.Unix()
	end := windowStart.AddDate(1, 1, 0).Unix()

	rows, err := db.Query(`
		SELECT vencimento, macro_group, COALESCE(SUM(amount), 0) AS total
		FROM (
			SELECT
				strftime('%Y-%m', datetime(COALESCE(NULLIF(t.paid_at, 0), t.date), 'unixepoch')) AS vencimento,
				COALESCE(c.macro_group, p.macro_group, CASE WHEN ip.id IS NOT NULL THEN 'Fatura de cartão' ELSE 'Sem grupo macro' END) AS macro_group,
				CASE WHEN t.type = 'INCOME' THEN t.amount ELSE -t.amount END AS amount
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			LEFT JOIN invoice_payments ip ON ip.transaction_id = t.id AND ip.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
					AND t.status = 'paid'
					AND t.type IN ('INCOME', 'EXPENSE')
					AND a.type != 'CREDIT_CARD'
					AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
					AND COALESCE(NULLIF(t.paid_at, 0), t.date) < ?

				UNION ALL

				SELECT
					strftime('%Y-%m', datetime(COALESCE(NULLIF(t.due_date, 0), t.date), 'unixepoch')) AS vencimento,
					COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
					CASE WHEN t.type = 'INCOME' THEN t.amount ELSE -t.amount END AS amount
				FROM transactions t
				JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
				LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
				LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
				WHERE t.workspace_id = ?
					AND t.status = 'pending'
					AND t.type IN ('INCOME', 'EXPENSE')
					AND a.type != 'CREDIT_CARD'
					AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
					AND COALESCE(NULLIF(t.due_date, 0), t.date) < ?
					AND COALESCE(t.description, '') NOT LIKE ?
					AND `+excludeInvoicePaymentCompetenceClause("t")+`
			)
		GROUP BY vencimento, macro_group
		ORDER BY vencimento ASC, macro_group ASC
		`, workspaceID, start, end, workspaceID, start, end, invoicePaymentDescriptionPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CaixaRowView
	for rows.Next() {
		var vencimento, macroGroup string
		var amount int64
		if err := rows.Scan(&vencimento, &macroGroup, &amount); err != nil {
			return nil, err
		}
		out = append(out, CaixaRowView{
			Vencimento:  vencimento,
			MacroGroup:  canonicalMacroGroup(macroGroup),
			AmountCents: amount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pendingInvoiceRows, err := buildPendingInvoiceCaixaRows(db, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	out = append(out, pendingInvoiceRows...)

	if out == nil {
		out = []CaixaRowView{}
	}
	sortCaixaRowViews(out)
	return out, nil
}

func buildPendingInvoiceCaixaRows(db *sql.DB, workspaceID string, start, end int64) ([]CaixaRowView, error) {
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT
			i.id,
			i.due_date,
			COALESCE((
				SELECT SUM(CASE WHEN tx.type = 'EXPENSE' THEN tx.amount ELSE 0 END)
				FROM transactions tx
				WHERE tx.workspace_id = a.workspace_id AND tx.invoice_id = i.id
			), 0) -
			`+invoiceEffectivePaidSQLExpr+` AS pending_amount
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE a.workspace_id = ?
			AND i.status IN ('OPEN', 'CLOSED')
			AND i.due_date >= ? AND i.due_date < ?
		ORDER BY i.due_date ASC, i.id ASC
	`, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type pendingInvoiceTarget struct {
		invoiceID     string
		dueDate       int64
		pendingAmount int64
	}
	targets := make([]pendingInvoiceTarget, 0)
	for rows.Next() {
		var target pendingInvoiceTarget
		if err := rows.Scan(&target.invoiceID, &target.dueDate, &target.pendingAmount); err != nil {
			return nil, err
		}
		if target.pendingAmount > 0 {
			targets = append(targets, target)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	handler := RelatoriosHandler{DB: db, WorkspaceID: workspaceID}
	out := make([]CaixaRowView, 0)
	for _, target := range targets {
		items, total, err := handler.queryInvoiceAllocationItems(ctx, target.invoiceID)
		if err != nil {
			return nil, err
		}
		for _, share := range allocateInvoiceItemShares(items, total, target.pendingAmount) {
			if share.Amount <= 0 {
				continue
			}
			out = append(out, CaixaRowView{
				Vencimento:  time.Unix(target.dueDate, 0).UTC().Format("2006-01"),
				MacroGroup:  canonicalMacroGroup(share.MacroGroup),
				AmountCents: -share.Amount,
			})
		}
	}
	return out, nil
}

func sortCaixaRowViews(rows []CaixaRowView) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Vencimento != rows[j].Vencimento {
			return rows[i].Vencimento < rows[j].Vencimento
		}
		return rows[i].MacroGroup < rows[j].MacroGroup
	})
}

// BuildCaixaRealizadoData retorna o Demonstrativo de Caixa apenas com
// movimentos realizados (paid), usando a data efetiva de pagamento.
// Exclui transferências e compras individuais em cartão de crédito.
func BuildCaixaRealizadoData(db *sql.DB, workspaceID string, year int) ([]CaixaRowView, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	rows, err := db.Query(`
		SELECT
			strftime('%Y-%m', datetime(effective_date, 'unixepoch')) AS vencimento,
			macro_group,
			COALESCE(SUM(CASE WHEN type = 'INCOME' THEN amount ELSE -amount END), 0) AS total
		FROM (
			SELECT
				COALESCE(NULLIF(t.paid_at, 0), t.date) AS effective_date,
				COALESCE(c.macro_group, p.macro_group, CASE WHEN ip.id IS NOT NULL THEN 'Fatura de cartão' ELSE 'Sem grupo macro' END) AS macro_group,
				t.type,
				t.amount
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			LEFT JOIN invoice_payments ip ON ip.transaction_id = t.id AND ip.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
				AND t.status = 'paid'
				AND t.type IN ('INCOME', 'EXPENSE')
				AND a.type != 'CREDIT_CARD'
				AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ?
				AND COALESCE(NULLIF(t.paid_at, 0), t.date) < ?
		)
		GROUP BY vencimento, macro_group
		ORDER BY vencimento ASC, macro_group ASC
	`, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CaixaRowView
	for rows.Next() {
		var vencimento, macroGroup string
		var amount int64
		if err := rows.Scan(&vencimento, &macroGroup, &amount); err != nil {
			return nil, err
		}
		out = append(out, CaixaRowView{
			Vencimento:  vencimento,
			MacroGroup:  canonicalMacroGroup(macroGroup),
			AmountCents: amount,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []CaixaRowView{}
	}
	return out, nil
}

func SumCaixaByMacroGroup(rows []CaixaRowView, macroGroup string) int64 {
	var total int64
	for _, row := range rows {
		if row.MacroGroup == macroGroup {
			total += row.AmountCents
		}
	}
	return total
}

func SumCaixaVencimento(rows []CaixaRowView, vencimento string) int64 {
	var total int64
	for _, row := range rows {
		if row.Vencimento == vencimento {
			total += row.AmountCents
		}
	}
	return total
}

func SumDREByMacroGroup(rows []DRERowView, macroGroup string) int64 {
	var total int64
	for _, row := range rows {
		if row.MacroGroup == macroGroup {
			total += row.AmountCents
		}
	}
	return total
}

func SumDRECompetencia(rows []DRERowView, competencia string) int64 {
	var total int64
	for _, row := range rows {
		if row.Competencia == competencia {
			total += row.AmountCents
		}
	}
	return total
}

func BuildStatementData(db *sql.DB, workspaceID string, accountID string, period ReportPeriod) ([]StatementRow, error) {
	rows, err := db.Query(`
		SELECT date, amount, type, description, account_name, status, paid_at FROM (
			SELECT
				t.date,
				t.amount,
				t.type,
				COALESCE(t.description, '') AS description,
				COALESCE(a.name, '') AS account_name,
				t.status,
				t.paid_at
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
				AND t.account_id = ?
				AND t.date >= ? AND t.date <= ?

			UNION ALL

			SELECT
				t.date,
				t.amount,
				t.type,
				COALESCE(t.description, '') AS description,
				COALESCE(dest.name, '') AS account_name,
				t.status,
				t.paid_at
			FROM transactions t
			JOIN accounts dest ON dest.id = t.destination_account_id AND dest.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
				AND t.type = 'TRANSFER'
				AND t.destination_account_id = ?
				AND t.date >= ? AND t.date <= ?
		)
		ORDER BY date ASC
	`, workspaceID, accountID, period.Start, period.End, workspaceID, accountID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StatementRow
	for rows.Next() {
		var date, amount int64
		var txType, description, accountName, status string
		var paidAt sql.NullInt64
		if err := rows.Scan(&date, &amount, &txType, &description, &accountName, &status, &paidAt); err != nil {
			return nil, err
		}
		isRealized := status == models.PaymentStatusPaid && paidAt.Valid && paidAt.Int64 > 0
		out = append(out, StatementRow{
			UnixDate:    date,
			AmountCents: amount,
			Description: description,
			AccountName: accountName,
			IsRealized:  isRealized,
			Type:        txType,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []StatementRow{}
	}
	return out, nil
}

func BuildExtratoData(db *sql.DB, workspaceID string, period ReportPeriod) ([]StatementExtratoRow, error) {
	// Extrato consolidado: apenas movimentos confirmados (status = paid) que geraram
	// entrada/saida real em contas nao-cartao. Compras no cartao de credito nao entram
	// aqui; o pagamento da fatura (saida da conta pagadora) e que e registrado.
	rows, err := db.Query(`
		SELECT effective_date, amount, type, description, account_name, contact_name, category_name, macro_group, status, transfer_side FROM (
			SELECT
				COALESCE(NULLIF(t.paid_at, 0), t.date) AS effective_date,
				t.amount,
				t.type,
				COALESCE(t.description, '') AS description,
				COALESCE(a.name, '') AS account_name,
				COALESCE(NULLIF(TRIM(ct.name), ''), '') AS contact_name,
				COALESCE(c.name, '') AS category_name,
				COALESCE(c.macro_group, p.macro_group, '') AS macro_group,
				t.status,
				'' AS transfer_side
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
			LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE t.workspace_id = ?
				AND t.status = 'paid'
				AND a.type != 'CREDIT_CARD'
				AND t.type IN ('INCOME', 'EXPENSE')
				AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ? AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?

			UNION ALL

			SELECT
				COALESCE(NULLIF(t.paid_at, 0), t.date) AS effective_date,
				t.amount,
				t.type,
				COALESCE(t.description, '') AS description,
				COALESCE(a.name, '') AS account_name,
				COALESCE(NULLIF(TRIM(ct.name), ''), '') AS contact_name,
				'' AS category_name,
				'' AS macro_group,
				t.status,
				'source' AS transfer_side
			FROM transactions t
			JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
			LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
				AND t.status = 'paid'
				AND a.type != 'CREDIT_CARD'
				AND t.type = 'TRANSFER'
				AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ? AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?

			UNION ALL

			SELECT
				COALESCE(NULLIF(t.paid_at, 0), t.date) AS effective_date,
				t.amount,
				t.type,
				COALESCE(t.description, '') AS description,
				COALESCE(dest.name, '') AS account_name,
				COALESCE(NULLIF(TRIM(ct.name), ''), '') AS contact_name,
				'' AS category_name,
				'' AS macro_group,
				t.status,
				'dest' AS transfer_side
			FROM transactions t
			JOIN accounts dest ON dest.id = t.destination_account_id AND dest.workspace_id = t.workspace_id
			LEFT JOIN contacts ct ON ct.id = t.contact_id AND ct.workspace_id = t.workspace_id
			WHERE t.workspace_id = ?
				AND t.status = 'paid'
				AND dest.type != 'CREDIT_CARD'
				AND t.type = 'TRANSFER'
				AND t.destination_account_id IS NOT NULL
				AND COALESCE(NULLIF(t.paid_at, 0), t.date) >= ? AND COALESCE(NULLIF(t.paid_at, 0), t.date) <= ?
		)
		ORDER BY effective_date ASC, account_name ASC
	`, workspaceID, period.Start, period.End, workspaceID, period.Start, period.End, workspaceID, period.Start, period.End)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StatementExtratoRow
	for rows.Next() {
		var effectiveDate, amount int64
		var txType, description, accountName, contactName, categoryName, macroGroup, status, transferSide string
		if err := rows.Scan(&effectiveDate, &amount, &txType, &description, &accountName, &contactName, &categoryName, &macroGroup, &status, &transferSide); err != nil {
			return nil, err
		}
		t := time.Unix(effectiveDate, 0).UTC()
		row := StatementExtratoRow{
			UnixDate:     effectiveDate,
			DateLabel:    fmt.Sprintf("%02d/%02d", t.Day(), int(t.Month())),
			Description:  description,
			AccountName:  accountName,
			ContactName:  contactName,
			CategoryName: categoryName,
			MacroGroup:   canonicalMacroGroup(macroGroup),
			Tipo:         txType,
			Status:       status,
			IsRealized:   true,
			AmountCents:  amount,
		}
		if txType == models.TransactionTypeIncome {
			row.Entrada = MoneyMinor(amount)
		} else if txType == models.TransactionTypeExpense {
			row.Saida = MoneyMinor(amount)
		} else if txType == models.TransactionTypeTransfer {
			row.IsTransfer = true
			if transferSide == "source" {
				row.Saida = MoneyMinor(amount)
			} else {
				row.Entrada = MoneyMinor(amount)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []StatementExtratoRow{}
	}
	return out, nil
}

func BuildAccountingCloseData(db *sql.DB, workspaceID string, period ReportPeriod) (AccountingCloseData, error) {
	realized, err := QueryRealizedCashEvents(db, workspaceID, period)
	if err != nil {
		return AccountingCloseData{}, err
	}
	invoiceRealized, err := QueryInvoiceCashEvents(db, workspaceID, period)
	if err != nil {
		return AccountingCloseData{}, err
	}

	var totalIncomeRealized, totalExpenseRealized int64
	for _, ev := range realized {
		if ev.IsIncome {
			totalIncomeRealized += ev.AmountCents
		} else {
			totalExpenseRealized += ev.AmountCents
		}
	}
	for _, ev := range invoiceRealized {
		if ev.IsIncome {
			totalIncomeRealized += ev.AmountCents
		} else {
			totalExpenseRealized += ev.AmountCents
		}
	}

	var openingBalanceReal int64
	err = db.QueryRow(`
		SELECT COALESCE(SUM(current_balance), 0)
		FROM accounts
		WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL
	`, workspaceID).Scan(&openingBalanceReal)
	if err != nil {
		return AccountingCloseData{}, err
	}

	projected, err := QueryProjectedCashEvents(db, workspaceID, period)
	if err != nil {
		return AccountingCloseData{}, err
	}
	invoiceProjected, err := QueryInvoiceProjectedCashEvents(db, workspaceID, period)
	if err != nil {
		return AccountingCloseData{}, err
	}

	var projectedNet int64
	for _, ev := range projected {
		if ev.IsIncome {
			projectedNet += ev.AmountCents
		} else {
			projectedNet -= ev.AmountCents
		}
	}
	for _, ev := range invoiceProjected {
		projectedNet -= ev.AmountCents
	}

	return AccountingCloseData{
		OpeningBalance:       openingBalanceReal,
		TotalIncomeRealized:  totalIncomeRealized,
		TotalExpenseRealized: totalExpenseRealized,
		NetResult:            totalIncomeRealized - totalExpenseRealized,
		ClosingBalance:       openingBalanceReal + (totalIncomeRealized - totalExpenseRealized),
		RealizedNetCashflow:  totalIncomeRealized - totalExpenseRealized,
		ProjectedNetCashflow: projectedNet,
	}, nil
}
