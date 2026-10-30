package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
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
		FROM financial_transactions t
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
		FROM financial_transactions t
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
	invoices, err := queryReportPendingInvoices(context.Background(), db, workspaceID, period.Start, period.End, true, false)
	if err != nil {
		return nil, err
	}
	var events []CashEvent
	for _, invoice := range invoices {
		if invoice.pending <= 0 {
			continue
		}
		ev := CashEvent{
			DateUnix:    invoice.dueDate,
			AmountCents: invoice.pending,
			Source:      CashSourceInvoiceProjected,
			AccountID:   "",
			Description: "Fatura prevista " + invoice.cardName + " - " + invoice.reference,
			IsRealized:  false,
			IsIncome:    false,
		}
		events = append(events, ev)
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
		FROM financial_transactions t
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
		FROM financial_transactions t
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
		FROM financial_transactions t
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
	invoices, err := queryReportPendingInvoices(context.Background(), db, workspaceID, period.Start, period.End, true, true)
	if err != nil {
		return nil, err
	}
	var events []CashEvent
	for _, invoice := range invoices {
		if invoice.pending <= 0 {
			continue
		}
		ev := CashEvent{
			DateUnix:    invoice.dueDate,
			AmountCents: invoice.pending,
			Source:      CashSourceInvoiceProjected,
			AccountID:   "",
			Description: "Fatura " + invoice.cardName + " - " + invoice.reference,
			IsRealized:  false,
			IsIncome:    false,
		}
		events = append(events, ev)
	}
	if events == nil {
		events = []CashEvent{}
	}
	return events, nil
}

var errReportAggregateOverflow = errors.New("report aggregate exceeds int64 range")

// reportExactAmount keeps intermediate cents outside int64 until the complete
// mathematical result for one public report field is known.
type reportExactAmount struct{ value big.Int }

func (a *reportExactAmount) add(cents int64) {
	a.value.Add(&a.value, big.NewInt(cents))
}

func (a *reportExactAmount) sub(cents int64) {
	a.value.Sub(&a.value, big.NewInt(cents))
}

func (a *reportExactAmount) addExact(other *reportExactAmount) {
	a.value.Add(&a.value, &other.value)
}

func (a *reportExactAmount) int64(component string) (int64, error) {
	if !a.value.IsInt64() {
		return 0, fmt.Errorf("%s: %w", component, errReportAggregateOverflow)
	}
	return a.value.Int64(), nil
}

// queryReportExactSum streams one monetary column; an empty result is zero.
func queryReportExactSum(ctx context.Context, db *sql.DB, component, query string, args ...any) (int64, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var sum reportExactAmount
	for rows.Next() {
		var cents int64
		if err := rows.Scan(&cents); err != nil {
			return 0, err
		}
		sum.add(cents)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return sum.int64(component)
}

func queryReportIncomeExpense(ctx context.Context, db *sql.DB, query string, args ...any) (int64, int64, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var income, expense reportExactAmount
	for rows.Next() {
		var kind string
		var cents int64
		if err := rows.Scan(&kind, &cents); err != nil {
			return 0, 0, err
		}
		switch kind {
		case "INCOME":
			income.add(cents)
		case "EXPENSE":
			expense.add(cents)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	incomeCents, err := income.int64("report income")
	if err != nil {
		return 0, 0, err
	}
	expenseCents, err := expense.int64("report expense")
	if err != nil {
		return 0, 0, err
	}
	return incomeCents, expenseCents, nil
}

type reportMonthlyAmount struct {
	month string
	group string
	cents int64
}

type reportMonthlyKey struct{ month, group string }

type reportGroupedAmount struct {
	name  string
	cents int64
}

type reportPendingInvoice struct {
	id, cardName, reference string
	dueDate                 int64
	pending                 int64
}

// queryReportPendingInvoices reads all invoice components in one tenant-scoped
// statement. The pending amount is checked only after items, general refunds,
// and effective payments have all been applied.
func queryReportPendingInvoices(ctx context.Context, db *sql.DB, workspaceID string, start, end int64, inclusiveEnd, includeAllItems bool) ([]reportPendingInvoice, error) {
	endOp := "<"
	if inclusiveEnd {
		endOp = "<="
	}
	rows, err := db.QueryContext(ctx, `
		WITH selected AS (
			SELECT i.id, i.due_date, COALESCE(a.name, '') AS card_name,
			       COALESCE(i.reference, '') AS reference, COALESCE(i.paid_amount, 0) AS legacy_paid,
			       a.workspace_id
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ? AND i.status IN ('OPEN', 'CLOSED')
			  AND i.due_date >= ? AND i.due_date `+endOp+` ?
		)
		SELECT id, due_date, card_name, reference, legacy_paid, 'base', 0 FROM selected
		UNION ALL
		SELECT s.id, s.due_date, s.card_name, s.reference, s.legacy_paid, 'item', `+transactionNetAmountTSQL+`
		FROM selected s JOIN financial_transactions t ON t.workspace_id = s.workspace_id AND t.invoice_id = s.id
		WHERE (? = 1 OR t.type = 'EXPENSE')
		UNION ALL
		SELECT s.id, s.due_date, s.card_name, s.reference, s.legacy_paid, 'refund', ir.amount_cents
		FROM selected s JOIN invoice_refunds ir ON ir.workspace_id = s.workspace_id AND ir.invoice_id = s.id AND ir.reversed_at IS NULL
		UNION ALL
		SELECT s.id, s.due_date, s.card_name, s.reference, s.legacy_paid, 'payment', ip.amount_cents
		FROM selected s JOIN invoice_payments ip ON ip.workspace_id = s.workspace_id AND ip.invoice_id = s.id AND ip.reversed_at IS NULL
		UNION ALL
		SELECT s.id, s.due_date, s.card_name, s.reference, s.legacy_paid, 'lineage', 0
		FROM selected s JOIN invoice_payments ip ON ip.workspace_id = s.workspace_id AND ip.invoice_id = s.id
	`, workspaceID, start, end, includeAllItems)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type aggregate struct {
		invoice                  reportPendingInvoice
		legacy                   int64
		items, refunds, payments reportExactAmount
		lineage                  bool
	}
	byID := make(map[string]*aggregate)
	for rows.Next() {
		var id, cardName, reference, kind string
		var dueDate, legacy, cents int64
		if err := rows.Scan(&id, &dueDate, &cardName, &reference, &legacy, &kind, &cents); err != nil {
			return nil, err
		}
		acc := byID[id]
		if acc == nil {
			acc = &aggregate{invoice: reportPendingInvoice{id: id, cardName: cardName, reference: reference, dueDate: dueDate}, legacy: legacy}
			byID[id] = acc
		}
		switch kind {
		case "item":
			acc.items.add(cents)
		case "refund":
			acc.refunds.add(cents)
		case "payment":
			acc.payments.add(cents)
		case "lineage":
			acc.lineage = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]reportPendingInvoice, 0, len(byID))
	for _, acc := range byID {
		var pending reportExactAmount
		pending.addExact(&acc.items)
		pending.value.Sub(&pending.value, &acc.refunds.value)
		if acc.lineage {
			pending.value.Sub(&pending.value, &acc.payments.value)
		} else {
			pending.sub(acc.legacy)
		}
		var err error
		acc.invoice.pending, err = pending.int64("invoice pending amount")
		if err != nil {
			return nil, err
		}
		out = append(out, acc.invoice)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].dueDate != out[j].dueDate {
			return out[i].dueDate < out[j].dueDate
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

func queryReportGroupedAmounts(ctx context.Context, db *sql.DB, component, query string, args ...any) ([]reportGroupedAmount, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	amounts := make(map[string]*reportExactAmount)
	for rows.Next() {
		var name string
		var cents int64
		if err := rows.Scan(&name, &cents); err != nil {
			return nil, err
		}
		if amounts[name] == nil {
			amounts[name] = &reportExactAmount{}
		}
		amounts[name].add(cents)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]reportGroupedAmount, 0, len(amounts))
	for name, amount := range amounts {
		cents, err := amount.int64(component)
		if err != nil {
			return nil, err
		}
		out = append(out, reportGroupedAmount{name, cents})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].cents != out[j].cents {
			return out[i].cents > out[j].cents
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// queryReportMonthlyAmounts preserves one SQL read and accumulates by the
// displayed month and canonical macro group before checking int64 bounds.
func queryReportMonthlyExact(ctx context.Context, db *sql.DB, query string, args ...any) (map[reportMonthlyKey]*reportExactAmount, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	amounts := make(map[reportMonthlyKey]*reportExactAmount)
	for rows.Next() {
		var month, group string
		var cents int64
		if err := rows.Scan(&month, &group, &cents); err != nil {
			return nil, err
		}
		k := reportMonthlyKey{month, canonicalMacroGroup(group)}
		if amounts[k] == nil {
			amounts[k] = &reportExactAmount{}
		}
		amounts[k].add(cents)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return amounts, nil
}

func reportMonthlyAmountsFromExact(amounts map[reportMonthlyKey]*reportExactAmount) ([]reportMonthlyAmount, error) {
	out := make([]reportMonthlyAmount, 0, len(amounts))
	for k, amount := range amounts {
		cents, err := amount.int64("monthly macro group")
		if err != nil {
			return nil, err
		}
		out = append(out, reportMonthlyAmount{k.month, k.group, cents})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].month != out[j].month {
			return out[i].month < out[j].month
		}
		return out[i].group < out[j].group
	})
	return out, nil
}

func queryReportMonthlyAmounts(ctx context.Context, db *sql.DB, query string, args ...any) ([]reportMonthlyAmount, error) {
	amounts, err := queryReportMonthlyExact(ctx, db, query, args...)
	if err != nil {
		return nil, err
	}
	return reportMonthlyAmountsFromExact(amounts)
}

// addReportInt64Checked reaproveita addInt64Checked e traduz o overflow para o
// sentinel de relatórios, mantendo um único erro identificável nesta cadeia.
func addReportInt64Checked(a, b int64) (int64, error) {
	sum, err := addInt64Checked(a, b)
	if err != nil {
		return 0, errReportAggregateOverflow
	}
	return sum, nil
}

// subInt64Checked subtrai dois int64 detectando overflow/underflow antes da
// operação, sem saturação ou wrap. Trata b == MinInt64 separadamente porque
// nesse caso -b não é representável em int64.
func subInt64Checked(a, b int64) (int64, error) {
	if b == math.MinInt64 {
		if a >= 0 {
			return 0, errReportAggregateOverflow
		}
		return a - b, nil
	}
	return addReportInt64Checked(a, -b)
}

func SumCashEvents(events []CashEvent) (int64, error) {
	var total reportExactAmount
	for _, ev := range events {
		if ev.IsIncome {
			total.add(ev.AmountCents)
		} else {
			total.sub(ev.AmountCents)
		}
	}
	return total.int64("cash events")
}

func SumCashIncome(events []CashEvent) (int64, error) {
	var total reportExactAmount
	for _, ev := range events {
		if ev.IsIncome {
			total.add(ev.AmountCents)
		}
	}
	return total.int64("cash income")
}

func SumCashExpense(events []CashEvent) (int64, error) {
	var total reportExactAmount
	for _, ev := range events {
		if !ev.IsIncome {
			total.add(ev.AmountCents)
		}
	}
	return total.int64("cash expense")
}

// queryProjectionExactSum leaves the monetary result unbounded for the final
// projected-balance calculation.
func queryProjectionExactSum(db *sql.DB, query string, args ...any) (*reportExactAmount, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	total := &reportExactAmount{}
	for rows.Next() {
		var amount int64
		if err := rows.Scan(&amount); err != nil {
			return nil, err
		}
		total.add(amount)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return total, nil
}

// queryProjectionSignedFlow applies the existing income/expense signs while
// keeping cancellation possible across every row in one projection point.
func queryProjectionSignedFlow(db *sql.DB, query string, args ...any) (*reportExactAmount, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	total := &reportExactAmount{}
	for rows.Next() {
		var kind string
		var amount int64
		if err := rows.Scan(&kind, &amount); err != nil {
			return nil, err
		}
		switch kind {
		case "INCOME":
			total.add(amount)
		case "EXPENSE":
			total.sub(amount)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return total, nil
}

// queryProjectionPendingInvoices keeps each invoice's positive-part test exact.
// The UNION branches avoid multiplying items by refunds or payment rows.
func queryProjectionPendingInvoices(db *sql.DB, workspaceID string, start, end int64) (*reportExactAmount, error) {
	rows, err := db.Query(`
		WITH selected AS (
			SELECT i.id, COALESCE(i.paid_amount, 0) AS legacy_paid, a.workspace_id
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE a.workspace_id = ? AND i.status IN ('OPEN', 'CLOSED')
			  AND i.due_date >= ? AND i.due_date <= ?
		)
		SELECT id, legacy_paid, 'base', 0 FROM selected
		UNION ALL
		SELECT s.id, s.legacy_paid, 'item', `+transactionNetAmountTSQL+`
		FROM selected s JOIN financial_transactions t ON t.workspace_id = s.workspace_id AND t.invoice_id = s.id
		WHERE t.type = 'EXPENSE'
		UNION ALL
		SELECT s.id, s.legacy_paid, 'refund', ir.amount_cents
		FROM selected s JOIN invoice_refunds ir ON ir.workspace_id = s.workspace_id AND ir.invoice_id = s.id AND ir.reversed_at IS NULL
		UNION ALL
		SELECT s.id, s.legacy_paid, 'payment', ip.amount_cents
		FROM selected s JOIN invoice_payments ip ON ip.workspace_id = s.workspace_id AND ip.invoice_id = s.id AND ip.reversed_at IS NULL
		UNION ALL
		SELECT s.id, s.legacy_paid, 'lineage', 0
		FROM selected s JOIN invoice_payments ip ON ip.workspace_id = s.workspace_id AND ip.invoice_id = s.id
	`, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type invoiceParts struct {
		legacy                   int64
		items, refunds, payments reportExactAmount
		lineage                  bool
	}
	byID := make(map[string]*invoiceParts)
	for rows.Next() {
		var id, kind string
		var legacy, amount int64
		if err := rows.Scan(&id, &legacy, &kind, &amount); err != nil {
			return nil, err
		}
		parts := byID[id]
		if parts == nil {
			parts = &invoiceParts{legacy: legacy}
			byID[id] = parts
		}
		switch kind {
		case "item":
			parts.items.add(amount)
		case "refund":
			parts.refunds.add(amount)
		case "payment":
			parts.payments.add(amount)
		case "lineage":
			parts.lineage = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	total := &reportExactAmount{}
	for _, parts := range byID {
		var pending reportExactAmount
		pending.addExact(&parts.items)
		pending.value.Sub(&pending.value, &parts.refunds.value)
		if parts.lineage {
			pending.value.Sub(&pending.value, &parts.payments.value)
		} else {
			pending.sub(parts.legacy)
		}
		if pending.value.Sign() > 0 {
			total.addExact(&pending)
		}
	}
	return total, nil
}

func BuildProjectedBalanceUntilPeriodEnd(db *sql.DB, workspaceID string, periodEnd int64, now time.Time) (int64, bool, error) {
	todayStart := AppCivilDateUTC(now).Unix()
	if periodEnd < todayStart {
		return 0, false, nil
	}

	currentBalance, err := queryProjectionExactSum(db, `
		SELECT current_balance
		FROM accounts
		WHERE workspace_id = ?
			AND type != 'CREDIT_CARD'
			AND archived_at IS NULL
	`, workspaceID)
	if err != nil {
		return 0, false, err
	}

	pendingFlow, err := queryProjectionSignedFlow(db, `
		SELECT t.type, CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE t.amount END
		FROM financial_transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
			AND t.status = 'pending'
			AND t.type IN ('INCOME', 'EXPENSE')
			AND a.type != 'CREDIT_CARD'
			AND COALESCE(t.description, '') NOT LIKE ?
			AND COALESCE(NULLIF(t.due_date, 0), t.date) >= ?
			AND COALESCE(NULLIF(t.due_date, 0), t.date) <= ?
	`, workspaceID, invoicePaymentDescriptionPrefix+"%", todayStart, periodEnd)
	if err != nil {
		return 0, false, err
	}

	pendingInvoices, err := queryProjectionPendingInvoices(db, workspaceID, todayStart, periodEnd)
	if err != nil {
		return 0, false, err
	}
	var projected reportExactAmount
	projected.addExact(currentBalance)
	projected.addExact(pendingFlow)
	projected.value.Sub(&projected.value, &pendingInvoices.value)
	result, err := projected.int64("projected period ending balance")
	if err != nil {
		return 0, false, err
	}
	return result, true, nil
}

func BuildDREData(db *sql.DB, workspaceID string, year int) ([]DRERowView, error) {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	monthly, err := queryReportMonthlyAmounts(context.Background(), db, `
		SELECT
			strftime('%Y-%m', datetime(t.date, 'unixepoch')) AS competencia,
			COALESCE(c.macro_group, p.macro_group, 'Sem grupo macro') AS macro_group,
			CASE WHEN t.type = 'INCOME' THEN t.amount ELSE -t.amount END AS total
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE t.workspace_id = ?
			AND t.type IN ('INCOME', 'EXPENSE')
			AND t.date >= ? AND t.date < ?
			AND `+excludeInvoicePaymentCompetenceClause("t")+`
	`, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]DRERowView, 0, len(monthly))
	for _, item := range monthly {
		out = append(out, DRERowView{
			Competencia: item.month,
			MacroGroup:  item.group,
			AmountCents: item.cents,
		})
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

	monthlyExact, err := queryReportMonthlyExact(context.Background(), db, `
		SELECT vencimento, macro_group, amount
		FROM (
			SELECT
				strftime('%Y-%m', datetime(COALESCE(NULLIF(t.paid_at, 0), t.date), 'unixepoch')) AS vencimento,
				COALESCE(c.macro_group, p.macro_group, CASE WHEN ip.id IS NOT NULL THEN 'Fatura de cartão' ELSE 'Sem grupo macro' END) AS macro_group,
				CASE WHEN t.type = 'INCOME' THEN t.amount ELSE -t.amount END AS amount
			FROM financial_transactions t
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
				FROM financial_transactions t
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
		`, workspaceID, start, end, workspaceID, start, end, invoicePaymentDescriptionPrefix+"%")
	if err != nil {
		return nil, err
	}
	pendingInvoiceRows, err := buildPendingInvoiceCaixaRows(db, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	for _, row := range pendingInvoiceRows {
		k := reportMonthlyKey{row.Vencimento, canonicalMacroGroup(row.MacroGroup)}
		if monthlyExact[k] == nil {
			monthlyExact[k] = &reportExactAmount{}
		}
		monthlyExact[k].add(row.AmountCents)
	}
	monthly, err := reportMonthlyAmountsFromExact(monthlyExact)
	if err != nil {
		return nil, err
	}
	out := make([]CaixaRowView, 0, len(monthly))
	for _, item := range monthly {
		out = append(out, CaixaRowView{Vencimento: item.month, MacroGroup: item.group, AmountCents: item.cents})
	}
	sortCaixaRowViews(out)
	return out, nil
}

func buildPendingInvoiceCaixaRows(db *sql.DB, workspaceID string, start, end int64) ([]CaixaRowView, error) {
	ctx := context.Background()
	invoices, err := queryReportPendingInvoices(ctx, db, workspaceID, start, end, false, false)
	if err != nil {
		return nil, err
	}

	handler := RelatoriosHandler{DB: db, WorkspaceID: workspaceID}
	out := make([]CaixaRowView, 0)
	for _, invoice := range invoices {
		if invoice.pending <= 0 {
			continue
		}
		items, total, err := handler.queryInvoiceAllocationItems(ctx, invoice.id)
		if err != nil {
			return nil, err
		}
		for _, share := range allocateInvoiceItemShares(items, total, invoice.pending) {
			if share.Amount <= 0 {
				continue
			}
			out = append(out, CaixaRowView{
				Vencimento:  time.Unix(invoice.dueDate, 0).UTC().Format("2006-01"),
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

	monthly, err := queryReportMonthlyAmounts(context.Background(), db, `
		SELECT
			strftime('%Y-%m', datetime(effective_date, 'unixepoch')) AS vencimento,
			macro_group,
			CASE WHEN type = 'INCOME' THEN amount ELSE -amount END AS total
		FROM (
			SELECT
				COALESCE(NULLIF(t.paid_at, 0), t.date) AS effective_date,
				COALESCE(c.macro_group, p.macro_group, CASE WHEN ip.id IS NOT NULL THEN 'Fatura de cartão' ELSE 'Sem grupo macro' END) AS macro_group,
				t.type,
				t.amount
			FROM financial_transactions t
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
	`, workspaceID, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]CaixaRowView, 0, len(monthly))
	for _, item := range monthly {
		out = append(out, CaixaRowView{
			Vencimento:  item.month,
			MacroGroup:  item.group,
			AmountCents: item.cents,
		})
	}
	return out, nil
}

func SumCaixaByMacroGroup(rows []CaixaRowView, macroGroup string) (int64, error) {
	var total reportExactAmount
	for _, row := range rows {
		if row.MacroGroup == macroGroup {
			total.add(row.AmountCents)
		}
	}
	return total.int64("cash macro group")
}

func SumCaixaVencimento(rows []CaixaRowView, vencimento string) (int64, error) {
	var total reportExactAmount
	for _, row := range rows {
		if row.Vencimento == vencimento {
			total.add(row.AmountCents)
		}
	}
	return total.int64("cash month")
}

func SumDREByMacroGroup(rows []DRERowView, macroGroup string) (int64, error) {
	var total reportExactAmount
	for _, row := range rows {
		if row.MacroGroup == macroGroup {
			total.add(row.AmountCents)
		}
	}
	return total.int64("DRE macro group")
}

func SumDRECompetencia(rows []DRERowView, competencia string) (int64, error) {
	var total reportExactAmount
	for _, row := range rows {
		if row.Competencia == competencia {
			total.add(row.AmountCents)
		}
	}
	return total.int64("DRE month")
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
			FROM financial_transactions t
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
			FROM financial_transactions t
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
			FROM financial_transactions t
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
			FROM financial_transactions t
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
			FROM financial_transactions t
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

	var incomeExact, expenseExact reportExactAmount
	for _, ev := range realized {
		if ev.IsIncome {
			incomeExact.add(ev.AmountCents)
		} else {
			expenseExact.add(ev.AmountCents)
		}
	}
	for _, ev := range invoiceRealized {
		if ev.IsIncome {
			incomeExact.add(ev.AmountCents)
		} else {
			expenseExact.add(ev.AmountCents)
		}
	}
	totalIncomeRealized, err := incomeExact.int64("accounting realized income")
	if err != nil {
		return AccountingCloseData{}, err
	}
	totalExpenseRealized, err := expenseExact.int64("accounting realized expense")
	if err != nil {
		return AccountingCloseData{}, err
	}
	openingBalanceReal, err := queryReportExactSum(context.Background(), db, "accounting opening balance", `
		SELECT current_balance
		FROM accounts
		WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL
	`, workspaceID)
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

	var projectedNetExact reportExactAmount
	for _, ev := range projected {
		if ev.IsIncome {
			projectedNetExact.add(ev.AmountCents)
		} else {
			projectedNetExact.sub(ev.AmountCents)
		}
	}
	for _, ev := range invoiceProjected {
		projectedNetExact.sub(ev.AmountCents)
	}
	projectedNet, err := projectedNetExact.int64("accounting projected net")
	if err != nil {
		return AccountingCloseData{}, err
	}
	var netExact reportExactAmount
	netExact.addExact(&incomeExact)
	netExact.value.Sub(&netExact.value, &expenseExact.value)
	realizedNet, err := netExact.int64("accounting realized net")
	if err != nil {
		return AccountingCloseData{}, err
	}
	var closeExact reportExactAmount
	closeExact.add(openingBalanceReal)
	closeExact.addExact(&netExact)
	closingBalance, err := closeExact.int64("accounting closing balance")
	if err != nil {
		return AccountingCloseData{}, err
	}

	return AccountingCloseData{
		OpeningBalance:       openingBalanceReal,
		TotalIncomeRealized:  totalIncomeRealized,
		TotalExpenseRealized: totalExpenseRealized,
		NetResult:            realizedNet,
		ClosingBalance:       closingBalance,
		RealizedNetCashflow:  realizedNet,
		ProjectedNetCashflow: projectedNet,
	}, nil
}
