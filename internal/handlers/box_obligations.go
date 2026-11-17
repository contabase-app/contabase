package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"
)

type f1ObligationReasons struct {
	seen  map[string]bool
	items []services.AvailableBalanceReason
}

func newF1ObligationReasons() *f1ObligationReasons {
	return &f1ObligationReasons{seen: make(map[string]bool)}
}

func (r *f1ObligationReasons) add(code, message string) {
	if r == nil || r.seen[code] {
		return
	}
	r.seen[code] = true
	r.items = append(r.items, services.AvailableBalanceReason{Code: code, Message: message})
}

func (r *f1ObligationReasons) list() []services.AvailableBalanceReason {
	if r == nil {
		return nil
	}
	return append([]services.AvailableBalanceReason(nil), r.items...)
}

type f1ExpenseRow struct {
	ID                       string
	AccountID                string
	DestinationAccountID     string
	InvoiceID                string
	InvoiceOverrideID        string
	Amount                   int64
	NetAmount                int64
	Status                   string
	CategoryID               string
	RecurringRuleID          sql.NullString
	SeriesID                 sql.NullString
	RecurrenceSequence       sql.NullInt64
	IsRecurringException     int64
	RecurringExceptionReason sql.NullString
	InstallmentNumber        int64
	TotalInstallments        int64
	ParentID                 string
	CreatedAt                int64
	DueDate                  sql.NullInt64
	DueDateCivil             sql.NullString
	Date                     int64
	DateCivil                sql.NullString
	AccountWorkspace         string
	AccountType              string
	DestinationWorkspace     string
	DestinationType          string
	RecurringRuleWorkspace   string
	SeriesWorkspace          string
}

type f1InvoiceRow struct {
	ID           string
	AccountID    string
	Status       string
	DueDate      int64
	DueDateCivil sql.NullString
	Total        int64
	Paid         int64
}

// f1ObligationSchedule classifies each already counted U component exactly
// once. Overdue items are included in both 30- and 60-day windows. Items
// without a usable due date are kept separate and are never assigned a
// fictitious date. DueBeyond60 and DueWithoutDate are reconciliation-only.
type f1ObligationSchedule struct {
	todayStart       time.Time
	due30            *big.Int
	due60            *big.Int
	beyond60         *big.Int
	withoutDate      *big.Int
	withoutDateCount int64
}

func newF1ObligationSchedule(todayStart time.Time) *f1ObligationSchedule {
	return &f1ObligationSchedule{
		todayStart:  todayStart,
		due30:       new(big.Int),
		due60:       new(big.Int),
		beyond60:    new(big.Int),
		withoutDate: new(big.Int),
	}
}

// f1ObligationDueDate normalizes a due date to noon UTC of its civil date.
// The persisted due_date_civil is authoritative; legacy timestamps are
// interpreted in the configured application timezone. A missing or invalid
// date returns 0 and is never assigned a fictitious day.
func f1ObligationDueDate(dueDate int64, dueDateCivil sql.NullString) int64 {
	if dueDateCivil.Valid {
		if civil := strings.TrimSpace(dueDateCivil.String); civil != "" {
			if parsed, err := time.Parse("2006-01-02", civil); err == nil {
				return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 12, 0, 0, 0, time.UTC).Unix()
			}
		}
	}
	if dueDate <= 0 {
		return 0
	}
	local := time.Unix(dueDate, 0).In(appTimeLocation())
	return time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, time.UTC).Unix()
}

// f1ObligationExpenseDueDate uses the explicit due date when present. Only in
// Personal workspaces, where the single persisted financial date is the date
// field, it falls back to date/date_civil. Business keeps the requirement for
// an explicit due date.
func f1ObligationExpenseDueDate(workspaceType string, expense f1ExpenseRow) int64 {
	if due := f1ObligationDueDate(expense.DueDate.Int64, expense.DueDateCivil); due > 0 {
		return due
	}
	if workspaceType == models.WorkspaceTypePersonal {
		return f1ObligationDueDate(expense.Date, expense.DateCivil)
	}
	return 0
}

func (s *f1ObligationSchedule) add(dueDate int64, amount *big.Int) {
	if s == nil || amount == nil || amount.Sign() <= 0 {
		return
	}
	if dueDate <= 0 {
		s.withoutDate.Add(s.withoutDate, amount)
		s.withoutDateCount++
		return
	}
	due := time.Unix(dueDate, 0).UTC()
	switch {
	case due.Before(s.todayStart.AddDate(0, 0, 31)):
		s.due30.Add(s.due30, amount)
		s.due60.Add(s.due60, amount)
	case due.Before(s.todayStart.AddDate(0, 0, 61)):
		s.due60.Add(s.due60, amount)
	default:
		s.beyond60.Add(s.beyond60, amount)
	}
}

func (s *f1ObligationSchedule) finish(total *big.Int) (due30, due60, withoutDateCount int64, ok bool) {
	if s == nil || total == nil || total.Sign() < 0 ||
		!s.due30.IsInt64() || !s.due60.IsInt64() ||
		!s.beyond60.IsInt64() || !s.withoutDate.IsInt64() {
		return 0, 0, 0, false
	}
	if s.due60.Cmp(s.due30) < 0 {
		return 0, 0, 0, false
	}
	expected := new(big.Int).Add(new(big.Int).Set(s.due60), s.beyond60)
	expected.Add(expected, s.withoutDate)
	if expected.Cmp(total) != 0 {
		return 0, 0, 0, false
	}
	return s.due30.Int64(), s.due60.Int64(), s.withoutDateCount, true
}

type f1PaymentReference struct {
	Type             string
	Amount           int64
	AccountID        string
	AccountWorkspace string
	AccountType      string
	Status           string
	InvoiceID        string
	Effect           string
}

type f1PaymentRow struct {
	ID                    string
	InvoiceID             string
	AccountID             string
	TransactionID         sql.NullString
	ReversalTransactionID sql.NullString
	Amount                int64
	ReversedAt            sql.NullInt64
	InvoiceAccountID      string
	InvoiceWorkspace      string
	InvoiceAccountType    string
	PaymentWorkspace      string
	PaymentAccountType    string
}

// loadWorkspaceObligationsF1 classifies obligations and reads U inside the
// caller's read snapshot. It never writes and never repairs historical data.
func loadWorkspaceObligationsF1(ctx context.Context, tx *sql.Tx, workspaceID string) (services.WorkspaceObligationsF1, error) {
	return loadWorkspaceObligationsF1Read(ctx, tx, workspaceID, false)
}

func loadWorkspaceObligationsF1Strict(ctx context.Context, tx *sql.Tx, workspaceID string) (services.WorkspaceObligationsF1, error) {
	return loadWorkspaceObligationsF1Read(ctx, tx, workspaceID, true)
}

// The dashboard rejects interrupted reads; legacy consumers keep their
// existing unavailable-domain fallback. Classification is shared unchanged.
func loadWorkspaceObligationsF1Read(ctx context.Context, tx *sql.Tx, workspaceID string, strictRead bool) (services.WorkspaceObligationsF1, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return services.WorkspaceObligationsF1{}, fmt.Errorf("workspace obrigatório")
	}
	var workspaceType string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(type, '') FROM workspaces WHERE id = ?`, workspaceID).Scan(&workspaceType); err != nil {
		return services.WorkspaceObligationsF1{}, fmt.Errorf("tipo do workspace: %w", err)
	}
	switch workspaceType {
	case models.WorkspaceTypePersonal, models.WorkspaceTypeBusiness:
	default:
		return services.WorkspaceObligationsF1{}, fmt.Errorf("tipo de workspace ausente ou desconhecido")
	}
	reasons := newF1ObligationReasons()
	schedule := newF1ObligationSchedule(AppCivilDateUTC(time.Now()))

	verifiedPaymentReferences, err := loadF1InvoicePayments(ctx, tx, workspaceID, reasons)
	if err != nil {
		return services.WorkspaceObligationsF1{}, err
	}

	refundInvariantBroken, err := validateF1InvoiceRefunds(ctx, tx, workspaceID, reasons)
	if err != nil {
		return services.WorkspaceObligationsF1{}, err
	}
	if refundInvariantBroken {
		return services.WorkspaceObligationsF1{Reasons: reasons.list()}, nil
	}
	transactionRefundBroken, err := validateF1TransactionRefunds(ctx, tx, workspaceID, reasons)
	if err != nil {
		return services.WorkspaceObligationsF1{}, err
	}
	if transactionRefundBroken {
		return services.WorkspaceObligationsF1{Reasons: reasons.list()}, nil
	}

	invoices, invoiceReasons, err := loadF1Invoices(ctx, tx, workspaceID)
	if err != nil {
		if strictRead || ctx.Err() != nil {
			return services.WorkspaceObligationsF1{}, err
		}
		reasons.add("invoice_composition_invalid", "há faturas com composição inválida")
		return services.WorkspaceObligationsF1{Reasons: reasons.list()}, nil
	}
	for _, reason := range invoiceReasons {
		reasons.add(reason.Code, reason.Message)
	}

	expenses, err := loadF1ExpenseRows(ctx, tx, workspaceID)
	if err != nil {
		if strictRead || ctx.Err() != nil {
			return services.WorkspaceObligationsF1{}, err
		}
		reasons.add("expense_composition_invalid", "há despesas com composição inválida")
		return services.WorkspaceObligationsF1{Reasons: reasons.list()}, nil
	}

	invoiceItemTypeReasons, err := validateF1InvoiceItemTypes(ctx, tx, workspaceID)
	if err != nil {
		return services.WorkspaceObligationsF1{}, err
	}
	for _, reason := range invoiceItemTypeReasons {
		reasons.add(reason.Code, reason.Message)
	}

	expensesByID := make(map[string]f1ExpenseRow, len(expenses))
	for _, expense := range expenses {
		expensesByID[expense.ID] = expense
	}
	validateF1RecurringOccurrenceIdentity(expenses, workspaceID, reasons)

	cardObligations := new(big.Int)
	for _, invoice := range invoices {
		if invoice.Paid < 0 {
			reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
			continue
		}
		residual := new(big.Int).Sub(big.NewInt(invoice.Total), big.NewInt(invoice.Paid))
		if residual.Sign() > 0 {
			cardObligations.Add(cardObligations, residual)
			schedule.add(f1ObligationDueDate(invoice.DueDate, invoice.DueDateCivil), residual)
		}
	}

	nonCardObligations := new(big.Int)
	for _, expense := range expenses {
		if f1TechnicalExpense(expense, verifiedPaymentReferences) {
			if expense.AccountType == models.AccountTypeCreditCard {
				reasons.add("card_technical_payment", "há pagamento técnico de fatura lançado no cartão")
			}
			continue
		}
		if expense.Amount <= 0 {
			reasons.add("expense_composition_invalid", "há despesas com composição inválida")
			continue
		}
		// A conta de destino não tem efeito financeiro em EXPENSE: o writer
		// atual nem a persiste fora de TRANSFER e ApplyBalanceEffect ignora o
		// destino nesse tipo. Lançamentos legados com destino continuam sendo
		// despesas do tipo declarado. O vínculo é apenas sinalizado, mas uma
		// referência ausente ou de outro workspace continua bloqueando.
		if expense.DestinationAccountID != "" {
			if expense.DestinationWorkspace != workspaceID {
				reasons.add("expense_account_invalid", "há despesas com conta de destino ausente ou de outro workspace")
				continue
			}
			if expense.DestinationType == models.AccountTypeCreditCard {
				// Um EXPENSE fora do cartão com destino de cartão pode ser um
				// pagamento legado sem vínculo de fatura. Contar esse valor como
				// despesa e manter o residual da fatura duplicaria a obrigação.
				reasons.add("expense_composition_invalid", "há despesas com conta de destino de cartão sem vínculo de fatura")
				continue
			}
			reasons.add("expense_destination_historical", "há despesas históricas com conta de destino; o tipo EXPENSE prevalece e o destino não altera as obrigações")
		}
		switch expense.AccountWorkspace {
		case workspaceID:
		default:
			reasons.add("expense_account_invalid", "há despesas com conta ausente ou de outro workspace")
			continue
		}
		switch expense.AccountType {
		case models.AccountTypeCreditCard:
			if expense.InvoiceID == "" {
				reasons.add("card_expense_without_invoice", "há compras no cartão sem fatura vinculada")
				continue
			}
			if expense.InvoiceOverrideID != "" && expense.InvoiceOverrideID != expense.InvoiceID {
				reasons.add("invoice_link_invalid", "há vínculos de fatura ausentes ou incompatíveis")
				continue
			}
			invoice, ok := invoices[expense.InvoiceID]
			if !ok || invoice.AccountID != expense.AccountID {
				reasons.add("invoice_link_invalid", "há vínculos de fatura ausentes ou incompatíveis")
				continue
			}
			if expense.Status != models.PaymentStatusPaid && expense.Status != models.PaymentStatusPending {
				reasons.add("expense_status_invalid", "há despesas com estado inválido")
			}
		case models.AccountTypeChecking, models.AccountTypeSavings, models.AccountTypeInvestment, models.AccountTypeWallet:
			if expense.InvoiceID != "" || expense.InvoiceOverrideID != "" {
				reasons.add("non_card_invoice_link", "há despesas de conta comum vinculadas a fatura")
				continue
			}
			switch expense.Status {
			case models.PaymentStatusPending:
				if expense.NetAmount < 0 {
					reasons.add("expense_composition_invalid", "há despesas com composição inválida")
					continue
				}
				nonCardObligations.Add(nonCardObligations, big.NewInt(expense.NetAmount))
				schedule.add(f1ObligationExpenseDueDate(workspaceType, expense), big.NewInt(expense.NetAmount))
			case models.PaymentStatusPaid:
			default:
				reasons.add("expense_status_invalid", "há despesas com estado inválido")
			}
		default:
			reasons.add("expense_account_invalid", "há despesas com conta ausente ou de outro workspace")
		}
		if !f1InstallmentStructureValid(expense, expensesByID) {
			reasons.add("installment_invalid", "há parcelamentos com estrutura inválida")
		}
	}

	total := new(big.Int).Add(cardObligations, nonCardObligations)
	if !total.IsInt64() {
		reasons.add("obligations_overflow", "as obrigações excedem o limite representável")
		return services.WorkspaceObligationsF1{Reasons: reasons.list()}, nil
	}
	due30, due60, withoutDateCount, scheduleAvailable := schedule.finish(total)
	var beyond60, withoutDate int64
	if schedule.beyond60.IsInt64() {
		beyond60 = schedule.beyond60.Int64()
	}
	if schedule.withoutDate.IsInt64() {
		withoutDate = schedule.withoutDate.Int64()
	}
	return services.WorkspaceObligationsF1{
		Amount:              total.Int64(),
		Due30:               due30,
		Due60:               due60,
		DueBeyond60:         beyond60,
		DueWithoutDate:      withoutDate,
		DueWithoutDateCount: withoutDateCount,
		ScheduleAvailable:   scheduleAvailable,
		Reasons:             reasons.list(),
	}, nil
}

func f1HasRecurringLineage(expense f1ExpenseRow) bool {
	return (expense.RecurringRuleID.Valid && strings.TrimSpace(expense.RecurringRuleID.String) != "") ||
		(expense.SeriesID.Valid && strings.TrimSpace(expense.SeriesID.String) != "") ||
		expense.RecurrenceSequence.Valid ||
		expense.IsRecurringException != 0 ||
		(expense.RecurringExceptionReason.Valid && strings.TrimSpace(expense.RecurringExceptionReason.String) != "")
}

// A materialized recurring occurrence is an ordinary expense for U. Lineage by
// itself does not change its amount, status or account. Only a duplicated
// identity, a missing/foreign lineage reference or an occurrence without a
// usable identity can make the sum ambiguous, because those would count the
// same obligation twice, cross a workspace boundary or count without a
// provable identity.
func validateF1RecurringOccurrenceIdentity(expenses []f1ExpenseRow, workspaceID string, reasons *f1ObligationReasons) {
	seen := make(map[string]bool)
	for _, expense := range expenses {
		if !f1HasRecurringLineage(expense) {
			continue
		}
		if expense.RecurringRuleID.Valid && strings.TrimSpace(expense.RecurringRuleID.String) != "" && expense.RecurringRuleWorkspace != workspaceID {
			reasons.add("recurring_indeterminate", "há despesas recorrentes com regra ausente ou de outro workspace")
			continue
		}
		if expense.SeriesID.Valid && strings.TrimSpace(expense.SeriesID.String) != "" && expense.SeriesWorkspace != workspaceID {
			reasons.add("recurring_indeterminate", "há despesas recorrentes com série ausente ou de outro workspace")
			continue
		}
		keys := f1RecurringOccurrenceKeys(expense)
		if len(keys) == 0 {
			reasons.add("recurring_indeterminate", "há despesas recorrentes sem identidade de ocorrência comprovável")
			continue
		}
		duplicate := false
		for _, key := range keys {
			if seen[key] {
				duplicate = true
				break
			}
		}
		if duplicate {
			reasons.add("recurring_indeterminate", "há ocorrências recorrentes duplicadas para a mesma identidade e sequência")
			continue
		}
		for _, key := range keys {
			seen[key] = true
		}
	}
}

func f1RecurringOccurrenceKeys(expense f1ExpenseRow) []string {
	if !expense.RecurrenceSequence.Valid || expense.RecurrenceSequence.Int64 <= 0 {
		return nil
	}
	sequence := expense.RecurrenceSequence.Int64
	keys := make([]string, 0, 2)
	if expense.SeriesID.Valid && strings.TrimSpace(expense.SeriesID.String) != "" {
		keys = append(keys, fmt.Sprintf("series:%s:%d", strings.TrimSpace(expense.SeriesID.String), sequence))
	}
	if expense.RecurringRuleID.Valid && strings.TrimSpace(expense.RecurringRuleID.String) != "" {
		keys = append(keys, fmt.Sprintf("rule:%s:%d", strings.TrimSpace(expense.RecurringRuleID.String), sequence))
	}
	return keys
}

func f1TechnicalExpense(expense f1ExpenseRow, verifiedReferences map[string]bool) bool {
	return verifiedReferences[expense.ID]
}

func f1ObligationCashAccountType(accountType string) bool {
	switch accountType {
	case models.AccountTypeChecking, models.AccountTypeSavings:
		return true
	default:
		return false
	}
}

func f1ObligationNonCardAccountType(accountType string) bool {
	switch accountType {
	case models.AccountTypeChecking, models.AccountTypeSavings, models.AccountTypeInvestment, models.AccountTypeWallet:
		return true
	default:
		return false
	}
}

// f1PaymentReferenceMatches validates the financial identity of the transaction
// linked to an invoice payment. The invoice payment record is the source of the
// paid amount; the linked transaction proves that the outflow existed and was
// not an invoice item. Account attribution divergence is handled by the caller:
// an alert for active payments and a blocking mismatch for reversals.
func f1PaymentReferenceMatches(ref f1PaymentReference, ok bool, workspaceID, transactionType string, amount int64) bool {
	return ok &&
		ref.Type == transactionType &&
		ref.Status == models.PaymentStatusPaid &&
		ref.Amount == amount &&
		ref.InvoiceID == "" &&
		ref.Effect == "STANDARD" &&
		ref.AccountWorkspace == workspaceID &&
		f1ObligationNonCardAccountType(ref.AccountType)
}

func f1InstallmentStructureValid(expense f1ExpenseRow, byID map[string]f1ExpenseRow) bool {
	if expense.TotalInstallments < 1 || expense.InstallmentNumber < 1 || expense.InstallmentNumber > expense.TotalInstallments {
		return false
	}
	if expense.TotalInstallments == 1 {
		return expense.ParentID == ""
	}
	if expense.InstallmentNumber == 1 {
		return expense.ParentID == ""
	}
	if expense.ParentID == "" {
		return false
	}
	parent, ok := byID[expense.ParentID]
	if !ok {
		return false
	}
	return parent.InstallmentNumber == 1 &&
		parent.TotalInstallments == expense.TotalInstallments &&
		parent.AccountID == expense.AccountID
}

func loadF1ExpenseRows(ctx context.Context, tx *sql.Tx, workspaceID string) ([]f1ExpenseRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			t.id,
			t.account_id,
			COALESCE(t.destination_account_id, ''),
			COALESCE(t.invoice_id, ''),
			COALESCE(t.invoice_override_id, ''),
			t.amount,
			`+transactionNetAmountTSQL+`,
			t.status,
			COALESCE(t.category_id, ''),
			t.recurring_rule_id,
			t.series_id,
			t.recurrence_sequence,
			t.is_recurring_exception,
			t.recurring_exception_reason,
			t.installment_number,
			t.total_installments,
			COALESCE(t.parent_id, ''),
			t.created_at,
			t.date,
			t.date_civil,
			t.due_date,
			t.due_date_civil,
			COALESCE(a.workspace_id, ''),
			COALESCE(a.type, ''),
			COALESCE(da.workspace_id, ''),
			COALESCE(da.type, ''),
			COALESCE(rr.workspace_id, ''),
			COALESCE(rs.workspace_id, '')
		FROM financial_transactions t
		LEFT JOIN accounts a ON a.id = t.account_id
		LEFT JOIN accounts da ON da.id = t.destination_account_id
		LEFT JOIN recurring_rules rr ON rr.id = t.recurring_rule_id
		LEFT JOIN recurring_series rs ON rs.id = t.series_id
		WHERE t.workspace_id = ?
		  AND t.type = ?
		ORDER BY t.id
	`, workspaceID, models.TransactionTypeExpense)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var expenses []f1ExpenseRow
	for rows.Next() {
		var expense f1ExpenseRow
		if err := rows.Scan(
			&expense.ID,
			&expense.AccountID,
			&expense.DestinationAccountID,
			&expense.InvoiceID,
			&expense.InvoiceOverrideID,
			&expense.Amount,
			&expense.NetAmount,
			&expense.Status,
			&expense.CategoryID,
			&expense.RecurringRuleID,
			&expense.SeriesID,
			&expense.RecurrenceSequence,
			&expense.IsRecurringException,
			&expense.RecurringExceptionReason,
			&expense.InstallmentNumber,
			&expense.TotalInstallments,
			&expense.ParentID,
			&expense.CreatedAt,
			&expense.Date,
			&expense.DateCivil,
			&expense.DueDate,
			&expense.DueDateCivil,
			&expense.AccountWorkspace,
			&expense.AccountType,
			&expense.DestinationWorkspace,
			&expense.DestinationType,
			&expense.RecurringRuleWorkspace,
			&expense.SeriesWorkspace,
		); err != nil {
			return nil, err
		}
		expenses = append(expenses, expense)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return expenses, nil
}

func loadF1Invoices(ctx context.Context, tx *sql.Tx, workspaceID string) (map[string]f1InvoiceRow, []services.AvailableBalanceReason, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			i.id,
			i.account_id,
			i.status,
			i.due_date,
			i.due_date_civil,
			COALESCE((
				SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
				FROM financial_transactions t
				WHERE t.workspace_id = a.workspace_id
				  AND t.invoice_id = i.id
			), 0) - `+invoiceGeneralRefundTotalIASQL+`,
			`+invoiceEffectivePaidSQLExpr+`
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE a.workspace_id = ?
		  AND a.type = ?
		ORDER BY i.id
	`, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	invoices := make(map[string]f1InvoiceRow)
	var reasons []services.AvailableBalanceReason
	for rows.Next() {
		var invoice f1InvoiceRow
		if err := rows.Scan(&invoice.ID, &invoice.AccountID, &invoice.Status, &invoice.DueDate, &invoice.DueDateCivil, &invoice.Total, &invoice.Paid); err != nil {
			return nil, nil, err
		}
		switch invoice.Status {
		case models.InvoiceStatusOpen, models.InvoiceStatusClosed, models.InvoiceStatusPaid:
		default:
			reasons = append(reasons, services.AvailableBalanceReason{
				Code:    "invoice_status_invalid",
				Message: "há faturas com estado inválido",
			})
		}
		if invoice.Paid < 0 {
			reasons = append(reasons, services.AvailableBalanceReason{
				Code:    "invoice_payment_invalid",
				Message: "há pagamentos de fatura com vínculos incompatíveis",
			})
		}
		invoices[invoice.ID] = invoice
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return invoices, reasons, nil
}

func validateF1InvoiceItemTypes(ctx context.Context, tx *sql.Tx, workspaceID string) ([]services.AvailableBalanceReason, error) {
	var count int64
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(1)
		FROM financial_transactions
		WHERE workspace_id = ?
		  AND invoice_id IS NOT NULL
		  AND invoice_id <> ''
		  AND type <> ?
	`, workspaceID, models.TransactionTypeExpense).Scan(&count)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	return []services.AvailableBalanceReason{{
		Code:    "invoice_item_type_invalid",
		Message: "há itens de fatura incompatíveis",
	}}, nil
}

func loadF1InvoicePayments(ctx context.Context, tx *sql.Tx, workspaceID string, reasons *f1ObligationReasons) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			ip.id,
			ip.invoice_id,
			ip.account_id,
			ip.transaction_id,
			ip.reversal_transaction_id,
			ip.amount_cents,
			ip.reversed_at,
			COALESCE(i.account_id, ''),
			COALESCE(ia.workspace_id, ''),
			COALESCE(ia.type, ''),
			COALESCE(pa.workspace_id, ''),
			COALESCE(pa.type, '')
		FROM invoice_payments ip
		LEFT JOIN invoices i ON i.id = ip.invoice_id
		LEFT JOIN accounts ia ON ia.id = i.account_id
		LEFT JOIN accounts pa ON pa.id = ip.account_id
		WHERE ip.workspace_id = ?
		ORDER BY ip.id
	`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type loadedPayment struct {
		row               f1PaymentRow
		structurallyValid bool
	}

	var payments []loadedPayment
	referenceCounts := make(map[string]int)
	references := make(map[string]bool)
	for rows.Next() {
		var payment f1PaymentRow
		if err := rows.Scan(
			&payment.ID,
			&payment.InvoiceID,
			&payment.AccountID,
			&payment.TransactionID,
			&payment.ReversalTransactionID,
			&payment.Amount,
			&payment.ReversedAt,
			&payment.InvoiceAccountID,
			&payment.InvoiceWorkspace,
			&payment.InvoiceAccountType,
			&payment.PaymentWorkspace,
			&payment.PaymentAccountType,
		); err != nil {
			return nil, err
		}
		structurallyValid := true
		if payment.InvoiceAccountID == "" || payment.InvoiceWorkspace != workspaceID || payment.InvoiceAccountType != models.AccountTypeCreditCard {
			reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
			structurallyValid = false
		}
		if !f1ObligationNonCardAccountType(payment.PaymentAccountType) {
			reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
			structurallyValid = false
		} else if !f1ObligationCashAccountType(payment.PaymentAccountType) {
			// A política atual oferece apenas contas de caixa no formulário, mas
			// pagamentos históricos saíram de contas reais não-cartão. O vínculo
			// com o lançamento pago continua sendo a prova financeira.
			reasons.add("invoice_payment_account_historical", "há pagamentos de fatura históricos originados de conta não classificada como caixa")
		}
		if payment.PaymentWorkspace != workspaceID {
			reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
			structurallyValid = false
		}
		if payment.TransactionID.Valid && strings.TrimSpace(payment.TransactionID.String) != "" {
			id := strings.TrimSpace(payment.TransactionID.String)
			referenceCounts[id]++
			references[id] = true
		}
		if payment.ReversalTransactionID.Valid && strings.TrimSpace(payment.ReversalTransactionID.String) != "" {
			id := strings.TrimSpace(payment.ReversalTransactionID.String)
			referenceCounts[id]++
			references[id] = true
			if !payment.ReversedAt.Valid {
				reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
				structurallyValid = false
			}
		}
		payments = append(payments, loadedPayment{row: payment, structurallyValid: structurallyValid})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, count := range referenceCounts {
		if count > 1 {
			reasons.add("payment_reference_duplicate", "há referências duplicadas em pagamentos de fatura")
			break
		}
	}

	verified := make(map[string]bool)
	if len(references) > 0 {
		ids := make([]string, 0, len(references))
		for id := range references {
			ids = append(ids, id)
		}
		args := make([]any, 0, len(ids)+1)
		args = append(args, workspaceID)
		for _, id := range ids {
			args = append(args, id)
		}
		txRows, err := tx.QueryContext(ctx, `
			SELECT t.id, t.type, t.amount, t.account_id, t.status, COALESCE(t.invoice_id, ''), t.effect_mode,
			       COALESCE(a.workspace_id, ''), COALESCE(a.type, '')
			FROM transactions t
			LEFT JOIN accounts a ON a.id = t.account_id
			WHERE t.workspace_id = ?
			  AND t.id IN (`+sqlPlaceholders(len(ids))+`)
		`, args...)
		if err != nil {
			return nil, err
		}
		loaded := make(map[string]f1PaymentReference, len(ids))
		for txRows.Next() {
			var ref f1PaymentReference
			var id string
			if err := txRows.Scan(&id, &ref.Type, &ref.Amount, &ref.AccountID, &ref.Status, &ref.InvoiceID, &ref.Effect, &ref.AccountWorkspace, &ref.AccountType); err != nil {
				txRows.Close()
				return nil, err
			}
			loaded[id] = ref
		}
		if err := txRows.Err(); err != nil {
			txRows.Close()
			return nil, err
		}
		if err := txRows.Close(); err != nil {
			return nil, err
		}
		for _, entry := range payments {
			payment := entry.row
			if payment.TransactionID.Valid && strings.TrimSpace(payment.TransactionID.String) != "" {
				id := strings.TrimSpace(payment.TransactionID.String)
				ref, ok := loaded[id]
				validReference := f1PaymentReferenceMatches(ref, ok, workspaceID, models.TransactionTypeExpense, payment.Amount)
				if !validReference {
					reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
				}
				if validReference && ref.AccountID != payment.AccountID {
					reasons.add("invoice_payment_attribution_historical", "há pagamentos de fatura históricos com conta de origem divergente do lançamento vinculado")
				}
				if validReference && entry.structurallyValid && referenceCounts[id] == 1 {
					verified[id] = true
				}
			}
			if payment.ReversalTransactionID.Valid && strings.TrimSpace(payment.ReversalTransactionID.String) != "" {
				id := strings.TrimSpace(payment.ReversalTransactionID.String)
				ref, ok := loaded[id]
				validReference := f1PaymentReferenceMatches(ref, ok, workspaceID, models.TransactionTypeIncome, payment.Amount) &&
					ref.AccountID == payment.AccountID
				if !validReference {
					reasons.add("invoice_payment_invalid", "há pagamentos de fatura com vínculos incompatíveis")
				}
				if validReference && entry.structurallyValid && referenceCounts[id] == 1 {
					verified[id] = true
				}
			}
		}
	}
	if err := validateF1AmbiguousInvoiceDescriptions(ctx, tx, workspaceID, verified, reasons); err != nil {
		return nil, err
	}
	return verified, nil
}

func f1LooksLikeInvoiceTechnicalDescription(description string) bool {
	normalized := strings.ToLower(strings.TrimSpace(description))
	if normalized == "" {
		return false
	}
	matchesPrefix := func(prefix string) bool {
		if !strings.HasPrefix(normalized, prefix) {
			return false
		}
		if len(normalized) == len(prefix) {
			return true
		}
		switch normalized[len(prefix)] {
		case ' ', '-', ':':
			return true
		default:
			return false
		}
	}
	return matchesPrefix("pagamento de fatura") ||
		matchesPrefix(strings.ToLower(invoicePaymentReversalDescription))
}

// validateF1AmbiguousInvoiceDescriptions flags entries whose description claims
// to be a technical invoice payment or reversal but which carry no verified
// financial identity. Only rows structurally identical to the shapes written by
// the invoice payment flow can be technical: a paid STANDARD EXPENSE without an
// invoice link (payment) or a paid STANDARD INCOME (reversal), both single
// account. Anything else — pending expenses, transfers, classification-only
// entries — is a real entry whose description alone must never exclude it.
func validateF1AmbiguousInvoiceDescriptions(ctx context.Context, tx *sql.Tx, workspaceID string, verified map[string]bool, reasons *f1ObligationReasons) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, COALESCE(description, '')
		FROM transactions
		WHERE workspace_id = ?
		  AND description IS NOT NULL
		  AND LOWER(description) LIKE '%fatura%'
		  AND status = ?
		  AND effect_mode = 'STANDARD'
		  AND COALESCE(destination_account_id, '') = ''
		  AND (
			(type = ? AND COALESCE(invoice_id, '') = '')
			OR type = ?
		  )
		ORDER BY id
	`, workspaceID, models.PaymentStatusPaid, models.TransactionTypeExpense, models.TransactionTypeIncome)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id, description string
		if err := rows.Scan(&id, &description); err != nil {
			return err
		}
		if verified[id] || !f1LooksLikeInvoiceTechnicalDescription(description) {
			continue
		}
		reasons.add("invoice_payment_description_ambiguous", "há lançamentos com descrição de pagamento ou reversão de fatura sem vínculo técnico comprovável")
	}
	return rows.Err()
}

func validateF1InvoiceRefunds(ctx context.Context, tx *sql.Tx, workspaceID string, reasons *f1ObligationReasons) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			ir.reversed_at,
			ir.reversed_by,
			COALESCE(i.id, ''),
			COALESCE(a.workspace_id, ''),
			COALESCE(a.type, '')
		FROM invoice_refunds ir
		LEFT JOIN invoices i ON i.id = ir.invoice_id
		LEFT JOIN accounts a ON a.id = i.account_id
		WHERE ir.workspace_id = ?
		ORDER BY ir.id
	`, workspaceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	broken := false
	for rows.Next() {
		var reversedAt sql.NullInt64
		var reversedBy sql.NullString
		var foundInvoiceID, workspace, accountType string
		if err := rows.Scan(&reversedAt, &reversedBy, &foundInvoiceID, &workspace, &accountType); err != nil {
			return false, err
		}
		if foundInvoiceID == "" || workspace != workspaceID || accountType != models.AccountTypeCreditCard {
			reasons.add("invoice_refund_invalid", "há reembolsos de fatura com vínculos incompatíveis")
			broken = true
		}
		if reversedAt.Valid != reversedBy.Valid {
			reasons.add("invoice_refund_invalid", "há reembolsos de fatura com vínculos incompatíveis")
			broken = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return broken, nil
}

func validateF1TransactionRefunds(ctx context.Context, tx *sql.Tx, workspaceID string, reasons *f1ObligationReasons) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			tr.transaction_id,
			tr.amount_cents,
			tr.reversed_at,
			tr.reversed_by,
			COALESCE(t.id, ''),
			COALESCE(t.type, ''),
			COALESCE(t.amount, 0),
			COALESCE(t.invoice_id, ''),
			COALESCE(t.workspace_id, ''),
			COALESCE(t.effect_mode, '')
		FROM transaction_refunds tr
		LEFT JOIN transactions t ON t.id = tr.transaction_id AND t.workspace_id = tr.workspace_id
		WHERE tr.workspace_id = ?
		ORDER BY tr.id
	`, workspaceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	broken := false
	activeByTransaction := make(map[string]*big.Int)
	amountByTransaction := make(map[string]int64)
	for rows.Next() {
		var transactionID string
		var amount int64
		var reversedAt sql.NullInt64
		var reversedBy sql.NullString
		var foundID, typ, invoiceID, workspace, effect string
		var transactionAmount int64
		if err := rows.Scan(&transactionID, &amount, &reversedAt, &reversedBy, &foundID, &typ, &transactionAmount, &invoiceID, &workspace, &effect); err != nil {
			return false, err
		}
		if foundID == "" || typ != models.TransactionTypeExpense || invoiceID == "" ||
			workspace != workspaceID || effect != "STANDARD" {
			reasons.add("transaction_refund_invalid", "há reembolsos de lançamento com vínculos incompatíveis")
			broken = true
			continue
		}
		if reversedAt.Valid != reversedBy.Valid {
			reasons.add("transaction_refund_invalid", "há reembolsos de lançamento com vínculos incompatíveis")
			broken = true
		}
		if !reversedAt.Valid {
			if activeByTransaction[transactionID] == nil {
				activeByTransaction[transactionID] = new(big.Int)
				amountByTransaction[transactionID] = transactionAmount
			}
			activeByTransaction[transactionID].Add(activeByTransaction[transactionID], big.NewInt(amount))
			if activeByTransaction[transactionID].Cmp(big.NewInt(amountByTransaction[transactionID])) > 0 {
				reasons.add("transaction_refund_invalid", "há reembolsos de lançamento com vínculos incompatíveis")
				broken = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return broken, nil
}
