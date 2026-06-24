package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
)

type FaturasHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

type FaturaData struct {
	Title                   string
	UserInitials            string
	ProfilePhotoURL         string
	OOB                     bool
	InvoiceID               string
	CardName                string
	CardIcon                string
	CardColor               string
	CardProviderSlug        string
	CardProviderMark        string
	AccountID               string
	HasActiveFilter         bool
	FilterLabel             string
	ClearFiltersURL         string
	Reference               string
	Status                  string
	StatusLabel             string
	StatusIcon              string
	StatusBadgeClass        string
	MonthOptions            []MonthOption
	MesAnteriorURL          string
	MesSeguinteURL          string
	ClosingLabel            string
	DueLabel                string
	Total                   MoneyDisplay
	CreditLimit             MoneyDisplay
	HasCreditLimit          bool
	LimitUsed               MoneyDisplay
	LimitAvailable          MoneyDisplay
	LimitPercent            int
	SortOrder               string
	SortRecentURL           string
	SortChronologicalURL    string
	InitialVisibleItems     int
	VisibleItemCountLabel   string
	HiddenItemCountLabel    string
	LoadMoreButtonLabel     string
	Transactions            []TransactionRow
	TransactionGroups       []TransactionDayGroup
	PaymentAccounts         []InvoicePaymentAccountOption
	CanAttemptPayment       bool
	CanSubmitPayment        bool
	PaymentDisabledReason   string
	InvoicePayments         []InvoicePaymentRow
	TotalPaid               MoneyDisplay
	PendingAmount           MoneyDisplay
	PendingAmountInput      string
	PaymentDateInput        string
	HasPayments             bool
	HasLegacyPaymentSummary bool
	LegacyPaymentNotice     string
	IsBusiness              bool
	ActiveWorkspaceName     string
}

type InvoicePaymentRow struct {
	DateLabel   string
	Amount      MoneyDisplay
	AccountName string
	Source      string
	Note        string
}

type InvoicePaymentAccountOption struct {
	ID                  string
	Name                string
	Money               MoneyDisplay
	BalanceCents        int64
	BalanceAfterPayment MoneyDisplay
	Icon                string
	Color               string
}

type TransactionDayGroup struct {
	DateLabel    string
	FirstIndex   int
	Transactions []TransactionRow
}

func (h *FaturasHandler) HandleExibirFatura(w http.ResponseWriter, r *http.Request) {
	cartaoID := r.URL.Query().Get("cartao")
	if cartaoID == "" {
		http.Error(w, "cartao obrigatorio", http.StatusBadRequest)
		return
	}

	data, err := h.buildFaturaData(cartaoID, faturaSortOrderFromRequest(r))
	if err != nil {
		log.Printf("build fatura error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-page", data); err != nil {
		log.Printf("template faturas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) HandleFaturaConceito(w http.ResponseWriter, r *http.Request) {
	cartaoID := r.URL.Query().Get("cartao")
	if cartaoID == "" {
		http.Error(w, "cartao obrigatorio", http.StatusBadRequest)
		return
	}

	data, err := h.buildFaturaData(cartaoID, faturaSortOrderFromRequest(r))
	if err != nil {
		log.Printf("build fatura error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.Title = "Fatura"

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-page", data); err != nil {
		log.Printf("template faturas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) HandleFaturaConceitoPorID(w http.ResponseWriter, r *http.Request, invoiceID string) {
	if invoiceID == "" {
		http.Error(w, "fatura obrigatoria", http.StatusBadRequest)
		return
	}

	data, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, faturaSortOrderFromRequest(r))
	if err != nil {
		log.Printf("build fatura by id error: %v", err)
		http.Error(w, "fatura nao encontrada", http.StatusNotFound)
		return
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.Title = "Fatura"

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-page", data); err != nil {
		log.Printf("template faturas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) HandleExibirFaturaPorID(w http.ResponseWriter, r *http.Request, invoiceID string) {
	if invoiceID == "" {
		http.Error(w, "fatura obrigatória", http.StatusBadRequest)
		return
	}
	if mes, ano, ok := parseMonthYearParams(r); ok {
		var accountID string
		if err := h.DB.QueryRow(`
			SELECT i.account_id
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
		`, invoiceID, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&accountID); err != nil {
			http.Error(w, "fatura não encontrada", http.StatusNotFound)
			return
		}
		h.HandleExibirFaturaPorCartaoMes(w, r, accountID, mes, ano)
		return
	}

	data, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, faturaSortOrderFromRequest(r))
	if err != nil {
		log.Printf("build fatura by id error: %v", err)
		http.Error(w, "fatura não encontrada", http.StatusNotFound)
		return
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-page", data); err != nil {
		log.Printf("template faturas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) HandleExibirFaturaPorCartaoMes(w http.ResponseWriter, r *http.Request, accountID string, mes, ano int) {
	if accountID == "" || mes < 1 || mes > 12 || ano < 2020 || ano > time.Now().Year()+10 {
		http.Error(w, "competência inválida", http.StatusBadRequest)
		return
	}

	var accountType string
	if err := h.DB.QueryRow(`
		SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND type = ?
	`, accountID, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&accountType); err != nil {
		http.Error(w, "cartão não encontrado", http.StatusNotFound)
		return
	}

	reference := fmt.Sprintf("%04d-%02d", ano, mes)
	var invoiceID string
	err := h.DB.QueryRow(`
		SELECT i.id
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.account_id = ? AND i.reference = ? AND a.workspace_id = ? AND a.type = ?
	`, accountID, reference, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&invoiceID)
	if err == sql.ErrNoRows {
		h.renderNoInvoiceForPeriod(w, r, accountID, mes, ano)
		return
	}
	if err != nil {
		log.Printf("query invoice by month error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, faturaSortOrderFromRequest(r))
	if err != nil {
		log.Printf("build fatura by card month error: %v", err)
		http.Error(w, "fatura não encontrada", http.StatusNotFound)
		return
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-page", data); err != nil {
		log.Printf("template faturas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) HandleAbrirFaturaPorCartaoMes(w http.ResponseWriter, r *http.Request, accountID string, mes, ano int) {
	if accountID == "" || mes < 1 || mes > 12 || ano < 2020 || ano > time.Now().Year()+10 {
		http.Error(w, "competência inválida", http.StatusBadRequest)
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var accountType string
	if err := tx.QueryRow(`
		SELECT type FROM accounts WHERE id = ? AND workspace_id = ? AND type = ?
	`, accountID, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&accountType); err != nil {
		http.Error(w, "cartão não encontrado", http.StatusNotFound)
		return
	}
	invoiceID, _, _, _, _, err := ensureInvoiceForReferenceTx(tx, h.WorkspaceID, accountID, ano, time.Month(mes))
	if err != nil {
		log.Printf("open invoice by month error: %v", err)
		http.Error(w, "erro ao abrir fatura", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, faturaSortOrderFromRequest(r))
	if err != nil {
		http.Error(w, "fatura não encontrada", http.StatusNotFound)
		return
	}
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-page", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) HandleListarFaturasDisponiveis(w http.ResponseWriter, r *http.Request, accountID string) {
	if accountID == "" {
		http.Error(w, "cartão obrigatório", http.StatusBadRequest)
		return
	}

	if err := autoCloseInvoices(h.DB, h.WorkspaceID, accountID); err != nil {
		log.Printf("auto close invoices error: %v", err)
	}

	var closingDay, dueDay int64
	if err := h.DB.QueryRow(`SELECT cc.closing_day, cc.due_day FROM credit_cards cc JOIN accounts a ON a.id = cc.account_id WHERE cc.account_id = ? AND a.workspace_id = ?`, accountID, h.WorkspaceID).Scan(&closingDay, &dueDay); err != nil {
		log.Printf("credit card query error: %v", err)
		closingDay = 20
		dueDay = 10
	}

	rows, err := h.DB.Query(`
		SELECT i.id, i.reference, i.status, i.closing_date, i.due_date,
			COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END), 0) as total
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id AND a.workspace_id = ?
		LEFT JOIN transactions t ON t.invoice_id = i.id AND t.workspace_id = ?
		WHERE i.account_id = ? AND i.status IN ('OPEN', 'CLOSED')
		GROUP BY i.id
		ORDER BY i.due_date DESC
		LIMIT 12
	`, h.WorkspaceID, h.WorkspaceID, accountID)
	if err != nil {
		log.Printf("list invoices error: %v", err)
		http.Error(w, "erro ao listar faturas", http.StatusInternalServerError)
		return
	}

	type invoiceOption struct {
		id    string
		label string
		ref   string
	}
	seenRefs := map[string]bool{}
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	var existingOptions []invoiceOption
	for rows.Next() {
		var id, reference, status string
		var closingDate, dueDate, total int64
		if err := rows.Scan(&id, &reference, &status, &closingDate, &dueDate, &total); err != nil {
			continue
		}
		refYear, refMonth, err := parseInvoiceReference(reference)
		if err != nil {
			continue
		}
		label := fmt.Sprintf("%s/%d", months[refMonth-1], refYear)
		if status == "CLOSED" {
			label += " (Fechada)"
		}
		if total > 0 {
			label += fmt.Sprintf(" - R$ %s", formatCurrencyCentsBase(total))
		}
		existingOptions = append(existingOptions, invoiceOption{id: id, label: label, ref: reference})
		seenRefs[reference] = true
	}
	rows.Close()

	now := time.Now().UTC()
	projected := projectedInvoiceMonths(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), 12, closingDay, dueDay)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<option value="">Automático (sugerir pela data)</option>`)
	for _, opt := range existingOptions {
		fmt.Fprintf(w, `<option value="%s">%s</option>`, opt.id, opt.label)
	}
	for _, prj := range projected {
		if seenRefs[prj.ref] {
			continue
		}
		fmt.Fprintf(w, `<option value="%s">%s</option>`, prj.ref, prj.label)
	}
}

type projectedInvoiceMonth struct {
	ref   string
	label string
}

func projectedInvoiceMonths(baseMonth time.Time, count int, closingDay, dueDay int64) []projectedInvoiceMonth {
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	var result []projectedInvoiceMonth
	t := time.Date(baseMonth.Year(), baseMonth.Month(), 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		_, _, ref := calculateInvoiceDates(t, closingDay, dueDay)
		refYear, refMonth, err := parseInvoiceReference(ref)
		if err != nil {
			continue
		}
		result = append(result, projectedInvoiceMonth{
			ref:   ref,
			label: fmt.Sprintf("%s/%d", months[refMonth-1], refYear),
		})
		t = time.Date(t.Year(), t.Month()+1, 1, 12, 0, 0, 0, time.UTC)
	}
	return result
}

func (h *FaturasHandler) HandlePagarFatura(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "dados inválidos", http.StatusBadRequest)
		return
	}

	invoiceID := r.FormValue("invoice_id")
	paymentAccountID := r.FormValue("payment_account_id")
	if invoiceID == "" || paymentAccountID == "" {
		http.Error(w, "fatura e conta de pagamento são obrigatórias", http.StatusBadRequest)
		return
	}

	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var cardAccountID, status, reference, cardName string
	err = tx.QueryRow(`
		SELECT i.account_id, i.status, i.reference, a.name
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&cardAccountID, &status, &reference, &cardName)
	if err != nil {
		http.Error(w, "fatura não autorizada ou não encontrada", http.StatusNotFound)
		return
	}
	if status == models.InvoiceStatusPaid {
		http.Error(w, "fatura já está paga", http.StatusConflict)
		return
	}

	var paymentAccountType string
	err = tx.QueryRow(`
		SELECT type FROM accounts
		WHERE id = ? AND workspace_id = ? AND type IN (?, ?)
	`, paymentAccountID, h.WorkspaceID, models.AccountTypeChecking, models.AccountTypeSavings).Scan(&paymentAccountType)
	if err != nil {
		http.Error(w, "conta corrente de pagamento não autorizada ou não encontrada", http.StatusBadRequest)
		return
	}

	total := sumInvoiceTotalTx(tx, h.WorkspaceID, invoiceID)
	if total <= 0 {
		http.Error(w, "fatura sem valor para pagar", http.StatusBadRequest)
		return
	}
	totalPaid, err := sumActiveInvoicePaymentsTx(tx, h.WorkspaceID, invoiceID)
	if err != nil {
		http.Error(w, "erro ao calcular pagamentos da fatura", http.StatusInternalServerError)
		return
	}
	pendingAmount := CalculateInvoicePendingAmount(total, totalPaid)
	if pendingAmount <= 0 {
		http.Error(w, "fatura sem saldo pendente para pagar", http.StatusBadRequest)
		return
	}

	paymentMode := strings.TrimSpace(r.FormValue("payment_mode"))
	if paymentMode == "" {
		paymentMode = "settle"
	}
	rawAmount := strings.TrimSpace(r.FormValue("payment_amount"))
	if paymentMode != "settle" && paymentMode != "partial" {
		http.Error(w, "modo de pagamento inválido", http.StatusUnprocessableEntity)
		return
	}
	if paymentMode == "partial" && rawAmount == "" {
		http.Error(w, "informe o valor do pagamento parcial", http.StatusUnprocessableEntity)
		return
	}
	if paymentMode == "settle" && r.FormValue("confirm_settle") != "1" {
		http.Error(w, "confirmação obrigatória para quitar fatura", http.StatusConflict)
		return
	}

	paymentAmount := pendingAmount
	if rawAmount != "" {
		parsedAmount, err := parseCurrency(rawAmount)
		if err != nil {
			http.Error(w, "valor de pagamento inválido", http.StatusUnprocessableEntity)
			return
		}
		if parsedAmount > pendingAmount {
			http.Error(w, "pagamento maior que o saldo pendente da fatura", http.StatusUnprocessableEntity)
			return
		}
		paymentAmount = parsedAmount
	}

	now := time.Now().Unix()
	paymentDate := now
	if rawDate := strings.TrimSpace(r.FormValue("payment_date")); rawDate != "" {
		parsedDate, err := parseDate(rawDate)
		if err != nil {
			http.Error(w, "data de pagamento inválida", http.StatusUnprocessableEntity)
			return
		}
		paymentDate = parsedDate
	}

	paidAmount := totalPaid + paymentAmount
	settleInvoice := paymentAmount == pendingAmount

	var res sql.Result
	if settleInvoice {
		res, err = tx.Exec(`
			UPDATE invoices
			SET status = 'PAID', paid_at = ?, paid_amount = ?
			WHERE id = ? AND account_id = ? AND status IN ('OPEN', 'CLOSED')
		`, paymentDate, paidAmount, invoiceID, cardAccountID)
	} else {
		res, err = tx.Exec(`
			UPDATE invoices
			SET paid_amount = ?
			WHERE id = ? AND account_id = ? AND status IN ('OPEN', 'CLOSED')
		`, paidAmount, invoiceID, cardAccountID)
	}
	if err != nil {
		http.Error(w, "erro ao registrar pagamento da fatura", http.StatusInternalServerError)
		return
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		http.Error(w, "erro ao registrar pagamento da fatura", http.StatusInternalServerError)
		return
	}
	if rowsAffected == 0 {
		http.Error(w, "fatura já está paga", http.StatusConflict)
		return
	}

	if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, "EXPENSE", paymentAccountType, "paid", paymentAmount, paymentAccountID, "", now); err != nil {
		http.Error(w, "erro ao debitar conta", http.StatusInternalServerError)
		return
	}

	paymentTxID := uuid.NewString()
	if err := execOneTx(tx, `
		INSERT INTO transactions (id, workspace_id, user_id, account_id, type, amount, date, description, status, installment_number, total_installments, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'EXPENSE', ?, ?, ?, 'paid', 1, 1, ?, ?)
	`, paymentTxID, h.WorkspaceID, h.UserID, paymentAccountID, paymentAmount, paymentDate, invoicePaymentDescription(cardName), now, now); err != nil {
		http.Error(w, "erro ao registrar pagamento no extrato", http.StatusInternalServerError)
		return
	}

	if err := execOneTx(tx, `
		INSERT INTO invoice_payments (id, workspace_id, invoice_id, account_id, transaction_id, amount_cents, paid_at, note, source, reversed_at, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 'manual', NULL, ?, ?)
	`, uuid.NewString(), h.WorkspaceID, invoiceID, paymentAccountID, paymentTxID, paymentAmount, paymentDate, h.UserID, now); err != nil {
		http.Error(w, "erro ao registrar pagamento da fatura", http.StatusInternalServerError)
		return
	}

	if _, _, _, _, _, err := ensureNextInvoiceTx(tx, h.WorkspaceID, cardAccountID, reference); err != nil {
		http.Error(w, "erro ao abrir próxima fatura", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if err := h.renderInvoiceAndDashboardOOB(w, r, invoiceID); err != nil {
		log.Printf("render dashboard after invoice payment error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
}

func (h *FaturasHandler) buildFaturaData(accountID string, sortOrder string) (FaturaData, error) {
	now := time.Now()
	invoiceID, _, _, _, _, err := resolveDashboardInvoice(h.DB, h.WorkspaceID, accountID, now.Unix())
	if err != nil {
		return FaturaData{}, err
	}
	data, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, sortOrder)
	if err != nil {
		return data, err
	}
	data.HasActiveFilter = true
	data.FilterLabel = data.CardName
	data.ClearFiltersURL = "/"
	return data, nil
}

func buildFaturaDataForInvoice(db *sql.DB, workspaceID, invoiceID, sortOrder string) (FaturaData, error) {
	data := FaturaData{
		Title:               "Fatura",
		UserInitials:        "VF",
		InvoiceID:           invoiceID,
		IsBusiness:          workspaceType(db, workspaceID) == models.WorkspaceTypeBusiness,
		ActiveWorkspaceName: queryWorkspaceName(db, workspaceID),
		SortOrder:           normalizeSortOrder(sortOrder, "desc"),
	}

	var closingUnix, dueUnix int64
	var providerSlug, cardColor string
	var creditLimitCents int64
	err := db.QueryRow(`
		SELECT i.account_id, i.reference, i.status, i.closing_date, i.due_date, a.name,
		       COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
		       COALESCE(NULLIF(a.color, ''), '#6B7280'),
		       COALESCE(cc.credit_limit, 0)
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		LEFT JOIN credit_cards cc ON cc.account_id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(&data.AccountID, &data.Reference, &data.Status, &closingUnix, &dueUnix, &data.CardName, &providerSlug, &cardColor, &creditLimitCents)
	if err != nil {
		return data, fmt.Errorf("fatura: %w", err)
	}

	if data.Status != models.InvoiceStatusPaid {
		if err := materializeRecurringInvoiceTransactions(db, workspaceID, invoiceID); err != nil {
			return data, err
		}
		if err := autoCloseInvoices(db, workspaceID, data.AccountID); err != nil {
			return data, err
		}
		if err := db.QueryRow(`SELECT status FROM invoices WHERE id = ?`, invoiceID).Scan(&data.Status); err != nil {
			return data, err
		}
	}

	data.CardProviderSlug = normalizeAccountProviderSlug(providerSlug)
	data.CardProviderMark = accountProviderMark(data.CardProviderSlug, data.CardName)
	data.CardIcon = accountVisualByProvider(data.CardProviderSlug, models.AccountTypeCreditCard)
	data.CardColor = normalizeHexColor(cardColor, "#6B7280")
	data.StatusLabel = invoiceDisplayStatusLabel(data.Status, dueUnix)
	data.StatusIcon, data.StatusBadgeClass = invoiceStatusVisual(data.Status, data.StatusLabel)
	if year, month, err := parseInvoiceReference(data.Reference); err == nil {
		selectedMonth := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		prevMonth := selectedMonth.AddDate(0, -1, 0)
		nextMonthDate := selectedMonth.AddDate(0, 1, 0)
		data.MesAnteriorURL = faturaMonthURL(data.AccountID, int(prevMonth.Month()), prevMonth.Year(), data.SortOrder)
		data.MesSeguinteURL = faturaMonthURL(data.AccountID, int(nextMonthDate.Month()), nextMonthDate.Year(), data.SortOrder)
		data.MonthOptions = buildFaturaMonthOptions(data.AccountID, selectedMonth, data.SortOrder)
		data.SortRecentURL = faturaMonthURL(data.AccountID, int(selectedMonth.Month()), selectedMonth.Year(), "desc")
		data.SortChronologicalURL = faturaMonthURL(data.AccountID, int(selectedMonth.Month()), selectedMonth.Year(), "asc")
	}
	data.ClosingLabel = formatDateLabel(closingUnix)
	data.DueLabel = formatDateLabel(dueUnix)

	total, err := sumInvoiceTotal(db, workspaceID, invoiceID)
	if err != nil {
		return data, err
	}
	data.Total = MoneyMinor(total)
	data.CreditLimit = MoneyMinor(creditLimitCents)
	data.LimitUsed = MoneyMinor(total)
	if creditLimitCents > 0 {
		data.HasCreditLimit = true
		limitAvailable := creditLimitCents - total
		if limitAvailable < 0 {
			limitAvailable = 0
		}
		data.LimitAvailable = MoneyMinor(limitAvailable)
		data.LimitPercent = int((limitAvailable * 100) / creditLimitCents)
		if data.LimitPercent < 0 {
			data.LimitPercent = 0
		}
		if data.LimitPercent > 100 {
			data.LimitPercent = 100
		}
	}
	data.PaymentAccounts = queryPaymentAccounts(db, workspaceID)
	data.PaymentDateInput = time.Now().Format("2006-01-02")

	pendingAmountRaw := total
	payments, err := QueryActiveInvoicePayments(db, workspaceID, invoiceID)
	if err == nil && len(payments) > 0 {
		totalPaid, payErr := SumActiveInvoicePayments(db, workspaceID, invoiceID)
		if payErr == nil {
			pendingAmountRaw = CalculateInvoicePendingAmount(total, totalPaid)
			data.HasPayments = true
			data.TotalPaid = MoneyMinor(totalPaid)
			data.PendingAmount = MoneyMinor(pendingAmountRaw)
			data.PendingAmountInput = formatCurrencyCentsBase(pendingAmountRaw)
			accountNameMap := paymentAccountNameMap(data.PaymentAccounts)
			data.InvoicePayments = buildInvoicePaymentRows(payments, accountNameMap)
		}
	} else {
		if data.Status == models.InvoiceStatusPaid {
			var legacyPaidAmount int64
			if err := db.QueryRow(`SELECT COALESCE(paid_amount, 0) FROM invoices WHERE id = ?`, invoiceID).Scan(&legacyPaidAmount); err == nil && legacyPaidAmount > 0 {
				pendingAmountRaw = 0
				data.HasLegacyPaymentSummary = true
				data.LegacyPaymentNotice = "Pagamento anterior ao hist\u00F3rico detalhado"
				data.TotalPaid = MoneyMinor(legacyPaidAmount)
				data.PendingAmount = MoneyMinor(0)
				data.PendingAmountInput = formatCurrencyCentsBase(0)
			} else {
				data.TotalPaid = MoneyMinor(0)
				data.PendingAmount = MoneyMinor(total)
				data.PendingAmountInput = formatCurrencyCentsBase(total)
			}
		} else {
			data.TotalPaid = MoneyMinor(0)
			data.PendingAmount = MoneyMinor(total)
			data.PendingAmountInput = formatCurrencyCentsBase(total)
		}
	}
	if data.PendingAmountInput == "" {
		data.PendingAmountInput = formatCurrencyCentsBase(pendingAmountRaw)
	}
	for i := range data.PaymentAccounts {
		data.PaymentAccounts[i].BalanceAfterPayment = MoneyMinor(data.PaymentAccounts[i].BalanceCents - pendingAmountRaw)
	}

	data.CanAttemptPayment = data.Status == models.InvoiceStatusOpen || data.Status == models.InvoiceStatusClosed
	data.CanSubmitPayment = data.CanAttemptPayment && pendingAmountRaw > 0 && len(data.PaymentAccounts) > 0
	if data.CanAttemptPayment && pendingAmountRaw <= 0 {
		data.PaymentDisabledReason = "Sem valor para pagar"
	} else if data.CanAttemptPayment && len(data.PaymentAccounts) == 0 {
		data.PaymentDisabledReason = "Nenhuma conta corrente disponível"
	}

	rows, err := db.Query(`
		SELECT t.id, t.type, t.amount, t.date, t.created_at, t.description, t.status,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			t.installment_number,
			t.total_installments,
			a.name,
			u.name,
			CASE WHEN t.total_installments > 1 OR t.recurring_rule_id IS NOT NULL THEN 1 ELSE 0 END
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		JOIN users u ON u.id = t.user_id
		WHERE t.invoice_id = ? AND t.workspace_id = ?
		ORDER BY t.date DESC, t.created_at DESC
	`, invoiceID, workspaceID)
	if err != nil {
		return data, fmt.Errorf("query fatura txs: %w", err)
	}
	defer rows.Close()

	data.Transactions = scanInvoiceTransactionRows(rows)
	if data.Status != models.InvoiceStatusPaid {
		data.Transactions = append(data.Transactions, projectedRecurringInvoiceRows(db, workspaceID, data.AccountID, data.Reference)...)
	}
	sortTransactionRows(data.Transactions, data.SortOrder)
	data.TransactionGroups = groupTransactionsByDay(data.Transactions)
	data.InitialVisibleItems = 30
	data.VisibleItemCountLabel, data.HiddenItemCountLabel, data.LoadMoreButtonLabel = invoiceVisibilityLabels(len(data.Transactions), data.InitialVisibleItems)
	return data, nil
}

type projectedRecurringRuleSeed struct {
	ruleID           string
	amount           int64
	description      string
	startDate        int64
	frequency        string
	paymentStatus    string
	categoryName     string
	categoryIcon     string
	categoryColor    string
	totalOccurrences sql.NullInt64
}

func projectedRecurringInvoiceRows(db *sql.DB, workspaceID, accountID, reference string) []TransactionRow {
	refYear, refMonth, err := parseInvoiceReference(reference)
	if err != nil {
		return nil
	}

	var closingDay, dueDay int64
	if err := db.QueryRow(`SELECT closing_day, due_day FROM credit_cards WHERE account_id = ?`, accountID).Scan(&closingDay, &dueDay); err != nil {
		return nil
	}

	cycleStartUnix, cycleEndUnix := invoiceCycleBoundsForReference(refYear, refMonth, closingDay, dueDay)
	cycleStart := time.Unix(cycleStartUnix, 0).UTC()
	cycleEnd := time.Unix(cycleEndUnix, 0).UTC()

	rows, err := db.Query(`
		SELECT rr.id, rr.amount, rr.description, rr.start_date, rr.frequency, rr.default_payment_status, rr.total_occurrences,
		       COALESCE(c.name, 'Sem categoria'),
		       COALESCE(c.icon, 'repeat'),
		       COALESCE(c.color, '#6366f1')
		FROM recurring_rules rr
		LEFT JOIN categories c ON c.id = rr.category_id
		WHERE rr.workspace_id = ?
		  AND rr.account_id = ?
		  AND rr.type = ?
		  AND rr.active = 1
		  AND rr.start_date < ?
	`, workspaceID, accountID, models.TransactionTypeExpense, cycleEnd.Unix())
	if err != nil {
		return nil
	}

	var rules []projectedRecurringRuleSeed
	for rows.Next() {
		var rule projectedRecurringRuleSeed
		var catName, catIcon, catColor sql.NullString
		if err := rows.Scan(&rule.ruleID, &rule.amount, &rule.description, &rule.startDate, &rule.frequency, &rule.paymentStatus, &rule.totalOccurrences, &catName, &catIcon, &catColor); err != nil {
			continue
		}
		rule.categoryName = catName.String
		rule.categoryIcon = catIcon.String
		rule.categoryColor = catColor.String
		rules = append(rules, rule)
	}
	if err := rows.Close(); err != nil {
		return nil
	}

	var projected []TransactionRow
	for _, rule := range rules {
		start := time.Unix(rule.startDate, 0).UTC()
		occurrence := start
		seq := int64(1)
		for i := 0; i < 20000 && occurrence.Before(cycleStart); i++ {
			if rule.totalOccurrences.Valid && rule.totalOccurrences.Int64 > 0 && seq >= rule.totalOccurrences.Int64 {
				break
			}
			next := nextRecurrenceDate(occurrence, rule.frequency)
			if !next.After(occurrence) {
				break
			}
			occurrence = next
			seq++
		}
		for i := 0; i < 400 && !occurrence.Before(cycleStart) && occurrence.Before(cycleEnd); i++ {
			if rule.totalOccurrences.Valid && rule.totalOccurrences.Int64 > 0 && seq > rule.totalOccurrences.Int64 {
				break
			}
			_, _, occRef := calculateInvoiceDates(occurrence, closingDay, dueDay)
			if occRef == reference {
				var exists int64
				db.QueryRow(`
					SELECT COUNT(1)
					FROM transactions
					WHERE workspace_id = ? AND recurring_rule_id = ? AND strftime('%Y-%m-%d', date, 'unixepoch') = strftime('%Y-%m-%d', ?, 'unixepoch')
				`, workspaceID, rule.ruleID, occurrence.Unix()).Scan(&exists)
				if exists == 0 {
					projected = append(projected, projectedInvoiceTransactionRow(rule.amount, occurrence, rule.description, rule.paymentStatus, rule.categoryName, rule.categoryIcon, rule.categoryColor))
				}
			}
			next := nextRecurrenceDate(occurrence, rule.frequency)
			if !next.After(occurrence) {
				break
			}
			occurrence = next
			seq++
		}
	}
	return projected
}

// CalculateVirtualInvoiceTotal calculates the projected total for a future billing cycle
func CalculateVirtualInvoiceTotal(db *sql.DB, workspaceID, accountID, reference string) (int64, int) {
	refYear, refMonth, err := parseInvoiceReference(reference)
	if err != nil {
		return 0, 0
	}

	var closingDay, dueDay int64
	if err := db.QueryRow(`SELECT closing_day, due_day FROM credit_cards WHERE account_id = ?`, accountID).Scan(&closingDay, &dueDay); err != nil {
		return 0, 0
	}

	cycleStart, cycleEnd := invoiceCycleBoundsForReference(refYear, refMonth, closingDay, dueDay)

	var physicalTotal int64
	var physicalCount int
	db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0), COUNT(id)
		FROM transactions
		WHERE workspace_id = ? AND account_id = ? AND type = 'EXPENSE'
		  AND date >= ? AND date < ? AND invoice_id IS NULL AND status != 'CANCELLED'
	`, workspaceID, accountID, cycleStart, cycleEnd).Scan(&physicalTotal, &physicalCount)

	projected := projectedRecurringInvoiceRows(db, workspaceID, accountID, reference)
	var projectedTotal int64
	for _, p := range projected {
		if p.Type == models.TransactionTypeExpense {
			projectedTotal += p.Amount
		}
	}

	return physicalTotal + projectedTotal, physicalCount + len(projected)
}

type recurringInvoiceRuleSeed struct {
	id                   string
	userID               string
	destinationAccountID sql.NullString
	categoryID           sql.NullString
	amount               int64
	description          string
	startDate            int64
	frequency            string
	totalOccurrences     sql.NullInt64
}

func materializeRecurringInvoiceTransactions(db *sql.DB, workspaceID, invoiceID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if err := materializeRecurringInvoiceTransactionsTx(tx, workspaceID, invoiceID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func materializeRecurringInvoiceTransactionsTx(tx *sql.Tx, workspaceID, invoiceID string, now int64) error {
	var accountID, reference string
	var closingUnix, dueUnix, closingDay, dueDay int64
	if err := tx.QueryRow(`
		SELECT i.account_id, i.reference, i.closing_date, i.due_date, cc.closing_day, cc.due_day
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		JOIN credit_cards cc ON cc.account_id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(&accountID, &reference, &closingUnix, &dueUnix, &closingDay, &dueDay); err != nil {
		return err
	}

	refYear, refMonth, err := parseInvoiceReference(reference)
	if err != nil {
		return err
	}
	cycleStartUnix, cycleEndUnix := invoiceCycleBoundsForReference(refYear, refMonth, closingDay, dueDay)
	cycleStart := time.Unix(cycleStartUnix, 0).UTC()
	cycleEnd := time.Unix(cycleEndUnix, 0).UTC()

	rows, err := tx.Query(`
		SELECT rr.id, rr.user_id, rr.destination_account_id, rr.category_id, rr.amount, rr.description, rr.start_date, rr.frequency, rr.total_occurrences
		FROM recurring_rules rr
		WHERE rr.workspace_id = ?
		  AND rr.account_id = ?
		  AND rr.type = ?
		  AND rr.active = 1
		  AND rr.start_date < ?
	`, workspaceID, accountID, models.TransactionTypeExpense, cycleEnd.Unix())
	if err != nil {
		return err
	}

	var rules []recurringInvoiceRuleSeed
	for rows.Next() {
		var rule recurringInvoiceRuleSeed
		if err := rows.Scan(&rule.id, &rule.userID, &rule.destinationAccountID, &rule.categoryID, &rule.amount, &rule.description, &rule.startDate, &rule.frequency, &rule.totalOccurrences); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, rule)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, rule := range rules {
		occurrence := time.Unix(rule.startDate, 0).UTC()
		seq := int64(1)
		for guard := 0; guard < 20000 && occurrence.Before(cycleStart); guard++ {
			if rule.totalOccurrences.Valid && rule.totalOccurrences.Int64 > 0 && seq >= rule.totalOccurrences.Int64 {
				break
			}
			next := nextRecurrenceDate(occurrence, rule.frequency)
			if !next.After(occurrence) {
				break
			}
			occurrence = next
			seq++
		}

		for guard := 0; guard < 400 && !occurrence.Before(cycleStart) && occurrence.Before(cycleEnd); guard++ {
			if rule.totalOccurrences.Valid && rule.totalOccurrences.Int64 > 0 && seq > rule.totalOccurrences.Int64 {
				break
			}
			_, _, occRef := calculateInvoiceDates(occurrence, closingDay, dueDay)
			if occRef == reference {
				if err := materializeRecurringInvoiceOccurrenceTx(tx, workspaceID, invoiceID, accountID, rule, occurrence.Unix(), seq, now); err != nil {
					return err
				}
			}
			next := nextRecurrenceDate(occurrence, rule.frequency)
			if !next.After(occurrence) {
				break
			}
			occurrence = next
			seq++
		}
	}
	return nil
}

func materializeRecurringInvoiceOccurrenceTx(tx *sql.Tx, workspaceID, invoiceID, accountID string, rule recurringInvoiceRuleSeed, occurrenceUnix, seq, now int64) error {
	var existingID string
	var existingInvoiceID sql.NullString
	err := tx.QueryRow(`
		SELECT id, invoice_id
		FROM transactions
		WHERE workspace_id = ? AND recurring_rule_id = ? AND strftime('%Y-%m-%d', date, 'unixepoch') = strftime('%Y-%m-%d', ?, 'unixepoch')
		LIMIT 1
	`, workspaceID, rule.id, occurrenceUnix).Scan(&existingID, &existingInvoiceID)
	if err == nil {
		if existingInvoiceID.Valid && existingInvoiceID.String != "" {
			return nil
		}
		_, execErr := tx.Exec(`
			UPDATE transactions
			SET invoice_id = ?, status = 'paid', updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, invoiceID, now, existingID, workspaceID)
		return execErr
	}
	if err != sql.ErrNoRows {
		return err
	}

	var destID interface{}
	if rule.destinationAccountID.Valid && strings.TrimSpace(rule.destinationAccountID.String) != "" {
		destID = rule.destinationAccountID.String
	}
	var catID interface{}
	if rule.categoryID.Valid && strings.TrimSpace(rule.categoryID.String) != "" {
		catID = rule.categoryID.String
	}
	id := uuid.NewString()
	_, execErr := tx.Exec(`
		INSERT OR IGNORE INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, category_id, invoice_id, type, amount, date, description, installment_number, total_installments, parent_id, recurring_rule_id, recurrence_sequence, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, NULL, ?, ?, 'paid', ?, ?)
	`, id, workspaceID, rule.userID, accountID, destID, catID, invoiceID, models.TransactionTypeExpense, rule.amount, occurrenceUnix, rule.description, rule.id, seq, now, now)
	return execErr
}

func invoiceReferenceForDate(t time.Time, closingDay int64) string {
	year, month := t.Year(), t.Month()
	if int64(t.Day()) > closingDay {
		year, month = nextInvoiceMonth(year, month)
	}
	return fmt.Sprintf("%04d-%02d", year, int(month))
}

func calculateInvoiceDates(t time.Time, closingDay, dueDay int64) (closingUnix, dueUnix int64, reference string) {
	year, month := t.Year(), t.Month()
	effectiveClosing := clampDayToMonth(year, month, closingDay)

	if int64(t.Day()) > int64(effectiveClosing) {
		year, month = nextInvoiceMonth(year, month)
	}

	dueMonth := month
	dueYear := year
	if dueDay < closingDay {
		dueYear, dueMonth = nextInvoiceMonth(dueYear, dueMonth)
	}
	closingUnix, dueUnix = invoiceDatesForReference(dueYear, dueMonth, closingDay, dueDay)
	reference = fmt.Sprintf("%04d-%02d", dueYear, int(dueMonth))
	return closingUnix, dueUnix, reference
}

func projectedInvoiceTransactionRow(amount int64, occurrence time.Time, description, paymentStatus, catName, catIcon, catColor string) TransactionRow {
	row := TransactionRow{
		Description:   description,
		Amount:        amount,
		AmountDisplay: "- R$ " + formatCurrencyCentsBase(amount),
		AmountInput:   formatCurrencyCentsBase(amount),
		AmountClass:   "text-indigo-300",
		CategoryName:  catName,
		CategoryIcon:  normalizeLucideIcon(catIcon),
		CategoryColor: normalizeUIThemeColor(catColor),
		AccountName:   "Cartão",
		Author:        "Previsto",
		PaymentStatus: strings.ToLower(paymentStatus),
		Type:          models.TransactionTypeExpense,
		IsProjected:   true,
		Date:          occurrence.Unix(),
		CreatedAt:     occurrence.Unix(),
		DateInput:     occurrence.Format("2006-01-02"),
		TimeDisplay:   "Previsto",
	}
	if row.CategoryIcon == "" {
		row.CategoryIcon = "repeat"
	}
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	row.DateLabel = fmt.Sprintf("%d %s", occurrence.Day(), months[occurrence.Month()-1])
	return row
}

func scanInvoiceTransactionRows(rows *sql.Rows) []TransactionRow {
	var list []TransactionRow
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}

	for rows.Next() {
		var row TransactionRow
		var trType string
		var amount int64
		var dateUnix int64
		var catName, catIcon, catColor sql.NullString
		var isSeries int64

		if err := rows.Scan(
			&row.ID,
			&trType,
			&amount,
			&dateUnix,
			&row.CreatedAt,
			&row.Description,
			&row.PaymentStatus,
			&catName,
			&catIcon,
			&catColor,
			&row.InstallmentNumber,
			&row.TotalInstallments,
			&row.AccountName,
			&row.Author,
			&isSeries,
		); err != nil {
			continue
		}

		row.Type = trType
		row.Amount = amount
		row.Date = dateUnix
		row.CategoryName = catName.String
		row.CategoryIcon = normalizeLucideIcon(catIcon.String)
		row.CategoryColor = normalizeUIThemeColor(catColor.String)
		if row.CategoryIcon == "" {
			row.CategoryIcon = "tag"
		}
		row.IsPending = row.PaymentStatus == "pending"
		row.IsSeries = isSeries == 1
		row.HasInstallmentInfo = row.IsSeries || row.TotalInstallments > 1

		t := time.Unix(dateUnix, 0).UTC()
		localTime := time.Unix(dateUnix, 0)
		row.TimeDisplay = localTime.Format("15:04")
		row.DateInput = localTime.Format("2006-01-02")
		row.DateLabel = fmt.Sprintf("%d %s", t.Day(), months[t.Month()-1])
		row.AmountDisplay = "- R$ " + formatCurrencyCentsBase(amount)
		row.AmountInput = formatCurrencyCentsBase(amount)
		row.AmountClass = "text-[#FE414F]"

		list = append(list, row)
	}

	return list
}

func queryPaymentAccounts(db *sql.DB, workspaceID string) []InvoicePaymentAccountOption {
	rows, err := db.Query(`
		SELECT id, name, current_balance
		FROM accounts
		WHERE workspace_id = ? AND type IN (?, ?)
		ORDER BY type, name
	`, workspaceID, models.AccountTypeChecking, models.AccountTypeSavings)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var accounts []InvoicePaymentAccountOption
	for rows.Next() {
		var account InvoicePaymentAccountOption
		var balance int64
		if err := rows.Scan(&account.ID, &account.Name, &balance); err != nil {
			continue
		}
		account.BalanceCents = balance
		account.Money = MoneyMinor(balance)
		account.Icon = AccountIcon(account.Name)
		account.Color = AccountColor(account.Name)
		accounts = append(accounts, account)
	}
	return accounts
}

func invoiceStatusLabel(status string) string {
	switch status {
	case "OPEN":
		return "Aberto"
	case "CLOSED":
		return "Fechado"
	case "PAID":
		return "Pago"
	default:
		return status
	}
}

func invoiceDisplayStatusLabel(status string, dueUnix int64) string {
	if status != "PAID" && dueUnix > 0 {
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		due := time.Unix(dueUnix, 0).UTC()
		dueDate := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
		if dueDate.Before(today) {
			return "Vencido"
		}
	}
	return invoiceStatusLabel(status)
}

func formatDateLabel(unix int64) string {
	t := time.Unix(unix, 0)
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	return fmt.Sprintf("%d %s %d", t.Day(), months[t.Month()-1], t.Year())
}

func formatDateTimeLabel(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	t := time.Unix(unix, 0)
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	return fmt.Sprintf("%d %s %d às %02d:%02d", t.Day(), months[t.Month()-1], t.Year(), t.Hour(), t.Minute())
}

func getAccountType(db *sql.DB, accountID, workspaceID string) (string, error) {
	var accType string
	err := db.QueryRow(`SELECT type FROM accounts WHERE id = ? AND workspace_id = ?`, accountID, workspaceID).Scan(&accType)
	return accType, err
}

func resolveOpenInvoice(db *sql.DB, workspaceID, accountID string, refDate int64) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	tx, err := db.Begin()
	if err != nil {
		return "", "", "", 0, 0, err
	}
	defer tx.Rollback()

	if err := autoCloseInvoicesTx(tx, workspaceID, accountID); err != nil {
		return "", "", "", 0, 0, err
	}

	id, ref, st, closeAt, dueAt, err := ensureOpenInvoiceTx(tx, workspaceID, accountID, refDate)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return "", "", "", 0, 0, err
	}
	return id, ref, st, closeAt, dueAt, nil
}

func resolveDashboardInvoice(db *sql.DB, workspaceID, accountID string, refDate int64) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	if err := autoCloseInvoices(db, workspaceID, accountID); err != nil {
		return "", "", "", 0, 0, err
	}

	err = db.QueryRow(`
		SELECT i.id, i.reference, i.status, i.closing_date, i.due_date
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.account_id = ? AND a.workspace_id = ? AND i.status IN ('CLOSED', 'OPEN')
		ORDER BY CASE status WHEN 'CLOSED' THEN 0 ELSE 1 END, closing_date ASC
		LIMIT 1
	`, accountID, workspaceID).Scan(&invoiceID, &reference, &status, &closingUnix, &dueUnix)
	if err == nil {
		return invoiceID, reference, status, closingUnix, dueUnix, nil
	}
	if err != sql.ErrNoRows {
		return "", "", "", 0, 0, err
	}
	return resolveOpenInvoice(db, workspaceID, accountID, refDate)
}

func autoCloseInvoices(db *sql.DB, workspaceID, accountID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := autoCloseInvoicesTx(tx, workspaceID, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

func autoCloseInvoicesTx(tx *sql.Tx, workspaceID, accountID string) error {
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		UPDATE invoices
		SET status = 'CLOSED'
		WHERE account_id = ?
		  AND status = 'OPEN'
		  AND closing_date <= ?
		  AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = invoices.account_id AND a.workspace_id = ?)
	`, accountID, now, workspaceID); err != nil {
		return err
	}
	return autoSettleZeroInvoicesTx(tx, workspaceID, accountID, now)
}

func autoCloseWorkspaceInvoices(db *sql.DB, workspaceID string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := autoCloseWorkspaceInvoicesTx(tx, workspaceID); err != nil {
		return err
	}
	return tx.Commit()
}

func autoCloseWorkspaceInvoicesTx(tx *sql.Tx, workspaceID string) error {
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		UPDATE invoices
		SET status = 'CLOSED'
		WHERE status = 'OPEN'
		  AND closing_date <= ?
		  AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = invoices.account_id AND a.workspace_id = ?)
	`, now, workspaceID); err != nil {
		return err
	}
	return autoSettleZeroInvoicesTx(tx, workspaceID, "", now)
}

func autoSettleZeroInvoicesTx(tx *sql.Tx, workspaceID, accountID string, now int64) error {
	_, err := tx.Exec(`
		UPDATE invoices
		SET status = 'PAID',
		    paid_at = COALESCE(paid_at, ?),
		    paid_amount = 0
		WHERE status IN ('OPEN', 'CLOSED')
		  AND (closing_date <= ? OR due_date <= ?)
		  AND (? = '' OR account_id = ?)
		  AND EXISTS (
		      SELECT 1
		      FROM accounts a
		      WHERE a.id = invoices.account_id
		        AND a.workspace_id = ?
		        AND a.type = ?
		  )
		  AND COALESCE((
		      SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN t.amount ELSE 0 END)
		      FROM transactions t
		      WHERE t.workspace_id = ?
		        AND t.invoice_id = invoices.id
		  ), 0) = 0
	`, now, now, now, accountID, accountID, workspaceID, models.AccountTypeCreditCard, workspaceID)
	return err
}

func ensureOpenInvoiceTx(tx *sql.Tx, workspaceID, accountID string, txDate int64) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	var closingDay, dueDay int64
	err = tx.QueryRow(`
		SELECT cc.closing_day, cc.due_day
		FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE cc.account_id = ? AND a.workspace_id = ?
	`, accountID, workspaceID).Scan(&closingDay, &dueDay)
	if err != nil {
		return "", "", "", 0, 0, fmt.Errorf("cartão de crédito sem configuração de fechamento/vencimento: %w", err)
	}

	t := time.Unix(txDate, 0)
	closingUnix, dueUnix, reference = calculateInvoiceDates(t, closingDay, dueDay)

	var refYear int
	var refMonth time.Month
	refYear, refMonth, err = parseInvoiceReference(reference)
	if err != nil {
		return "", "", "", 0, 0, err
	}

	for i := 0; i < 24; i++ {
		invoiceID, reference, status, closingUnix, dueUnix, err = ensureInvoiceForReferenceTx(tx, workspaceID, accountID, refYear, refMonth)
		if err != nil {
			return "", "", "", 0, 0, err
		}
		if status != models.InvoiceStatusPaid {
			return invoiceID, reference, status, closingUnix, dueUnix, nil
		}
		refYear, refMonth = nextInvoiceMonth(refYear, refMonth)
	}

	return "", "", "", 0, 0, fmt.Errorf("nenhuma fatura ativa encontrada")
}

func resolveCardTransactionInvoiceTx(tx *sql.Tx, workspaceID, accountID string, txDate int64, faturaOffset string) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	if err := autoCloseInvoicesTx(tx, workspaceID, accountID); err != nil {
		return "", "", "", 0, 0, err
	}

	_, candidateReference, _, _, _, err := candidateInvoiceForDateTx(tx, workspaceID, accountID, txDate)
	if err != nil {
		return "", "", "", 0, 0, err
	}

	startReference := candidateReference
	if normalizeFaturaOffset(faturaOffset) == "next" {
		startReference, err = nextInvoiceReference(candidateReference)
		if err != nil {
			return "", "", "", 0, 0, err
		}
	}

	return ensureFirstOpenInvoiceFromReferenceTx(tx, workspaceID, accountID, startReference)
}

func candidateInvoiceForDateTx(tx *sql.Tx, workspaceID, accountID string, txDate int64) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	var closingDay, dueDay int64
	err = tx.QueryRow(`
		SELECT cc.closing_day, cc.due_day
		FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE cc.account_id = ? AND a.workspace_id = ?
	`, accountID, workspaceID).Scan(&closingDay, &dueDay)
	if err != nil {
		return "", "", "", 0, 0, fmt.Errorf("cartão de crédito sem configuração de fechamento/vencimento: %w", err)
	}

	closingUnix, dueUnix, reference = calculateInvoiceDates(time.Unix(txDate, 0), closingDay, dueDay)
	err = tx.QueryRow(`
		SELECT i.id, i.status, i.closing_date, i.due_date
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.account_id = ? AND i.reference = ? AND a.workspace_id = ?
	`, accountID, reference, workspaceID).Scan(&invoiceID, &status, &closingUnix, &dueUnix)
	if err == nil {
		return invoiceID, reference, status, closingUnix, dueUnix, nil
	}
	if err != sql.ErrNoRows {
		return "", "", "", 0, 0, err
	}
	return "", reference, "", closingUnix, dueUnix, nil
}

func ensureFirstOpenInvoiceFromReferenceTx(tx *sql.Tx, workspaceID, accountID string, reference string) (invoiceID, resolvedReference, status string, closingUnix, dueUnix int64, err error) {
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	for i := 0; i < 24; i++ {
		invoiceID, resolvedReference, status, closingUnix, dueUnix, err = ensureInvoiceForReferenceTx(tx, workspaceID, accountID, year, month)
		if err != nil {
			return "", "", "", 0, 0, err
		}
		if status == models.InvoiceStatusOpen {
			return invoiceID, resolvedReference, status, closingUnix, dueUnix, nil
		}
		year, month = nextInvoiceMonth(year, month)
	}
	return "", "", "", 0, 0, fmt.Errorf("nenhuma fatura aberta encontrada")
}

func normalizeFaturaOffset(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "next") {
		return "next"
	}
	return "auto"
}

func nextInvoiceReference(reference string) (string, error) {
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		return "", err
	}
	year, month = nextInvoiceMonth(year, month)
	return fmt.Sprintf("%04d-%02d", year, month), nil
}

func ensureInvoiceForReferenceTx(tx *sql.Tx, workspaceID, accountID string, year int, month time.Month) (invoiceID, reference, status string, closingUnix, dueUnix int64, err error) {
	reference = fmt.Sprintf("%04d-%02d", year, month)
	err = tx.QueryRow(`
		SELECT i.id, i.status, i.closing_date, i.due_date
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.account_id = ? AND i.reference = ? AND a.workspace_id = ?
	`, accountID, reference, workspaceID).Scan(&invoiceID, &status, &closingUnix, &dueUnix)
	if err == nil {
		return invoiceID, reference, status, closingUnix, dueUnix, nil
	}
	if err != sql.ErrNoRows {
		return "", "", "", 0, 0, err
	}

	var closingDay, dueDay int64
	if err := tx.QueryRow(`
		SELECT cc.closing_day, cc.due_day
		FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE cc.account_id = ? AND a.workspace_id = ?
	`, accountID, workspaceID).Scan(&closingDay, &dueDay); err != nil {
		return "", "", "", 0, 0, fmt.Errorf("cartão de crédito sem configuração de fechamento/vencimento: %w", err)
	}

	closingUnix, dueUnix = invoiceDatesForReference(year, month, closingDay, dueDay)

	invoiceID = uuid.NewString()
	now := time.Now().Unix()
	if err := execOneTx(tx, `
		INSERT INTO invoices (id, account_id, reference, closing_date, due_date, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'OPEN', ?)
	`, invoiceID, accountID, reference, closingUnix, dueUnix, now); err != nil {
		return "", "", "", 0, 0, err
	}
	return invoiceID, reference, "OPEN", closingUnix, dueUnix, nil
}

func ensureNextInvoiceTx(tx *sql.Tx, workspaceID, accountID string, reference string) (invoiceID, nextReference, status string, closingUnix, dueUnix int64, err error) {
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	year, month = nextInvoiceMonth(year, month)
	return ensureInvoiceForReferenceTx(tx, workspaceID, accountID, year, month)
}

func (h *FaturasHandler) HandleResolverDestinoFatura(w http.ResponseWriter, r *http.Request, accountID string) {
	dataRaw := strings.TrimSpace(r.URL.Query().Get("data"))
	refDate := time.Now().Unix()
	if dataRaw != "" {
		parsed, err := parseDate(dataRaw)
		if err != nil {
			http.Error(w, "data inválida", http.StatusBadRequest)
			return
		}
		refDate = parsed
	}

	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	invoiceID, reference, status, closingUnix, dueUnix, err := resolveCardTransactionInvoiceTx(tx, h.WorkspaceID, accountID, refDate, r.URL.Query().Get("fatura_offset"))
	if err != nil {
		log.Printf("resolve invoice destination error: %v", err)
		http.Error(w, "erro ao resolver fatura", http.StatusUnprocessableEntity)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	dueDate := time.Unix(dueUnix, 0).UTC()
	closingDate := time.Unix(closingUnix, 0).UTC()
	months := []string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"}
	monthLabel := fmt.Sprintf("%s/%d", months[dueDate.Month()-1], dueDate.Year())
	payload := map[string]interface{}{
		"invoice_id":   invoiceID,
		"reference":    reference,
		"status":       status,
		"closing_date": closingUnix,
		"due_date":     dueUnix,
		"notice":       fmt.Sprintf("Fatura prevista: %s. Fecha dia %d e vence dia %d.", monthLabel, closingDate.Day(), dueDate.Day()),
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode invoice destination error: %v", err)
	}
}

func parseMonthYearParams(r *http.Request) (mes int, ano int, ok bool) {
	mesStr := r.URL.Query().Get("mes")
	anoStr := r.URL.Query().Get("ano")
	if mesStr == "" && anoStr == "" {
		return 0, 0, false
	}
	mes, err := strconv.Atoi(mesStr)
	if err != nil || mes < 1 || mes > 12 {
		return 0, 0, false
	}
	ano, err = strconv.Atoi(anoStr)
	if err != nil || ano < 2020 || ano > time.Now().Year()+10 {
		return 0, 0, false
	}
	return mes, ano, true
}

func buildFaturaMonthOptions(accountID string, selectedMonth time.Time, sortOrder string) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	now := time.Now().UTC()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	options := make([]MonthOption, 0, 12)
	for month := 1; month <= 12; month++ {
		m := time.Date(selectedMonth.Year(), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		options = append(options, MonthOption{
			Label:     shortMonths[int(m.Month())-1],
			Year:      fmt.Sprintf("%d", m.Year()),
			URL:       faturaMonthURL(accountID, int(m.Month()), m.Year(), sortOrder),
			IsActive:  m.Month() == selectedMonth.Month(),
			IsCurrent: m.Equal(currentMonth),
		})
	}
	return options
}

func faturaMonthURL(accountID string, mes, ano int, sortOrder string) string {
	values := url.Values{}
	values.Set("mes", strconv.Itoa(mes))
	values.Set("ano", strconv.Itoa(ano))
	if normalizeSortOrder(sortOrder, "desc") == "asc" {
		values.Set("ordem", "asc")
	}
	return "/cartoes/" + accountID + "/faturas?" + values.Encode()
}

func faturaSortOrderFromRequest(r *http.Request) string {
	if r == nil {
		return "desc"
	}
	return normalizeSortOrder(r.URL.Query().Get("ordem"), "desc")
}

func faturaSortOrderFromCurrentRequest(r *http.Request) string {
	if r == nil {
		return "desc"
	}
	currentURL := strings.TrimSpace(r.Header.Get("HX-Current-URL"))
	if currentURL == "" {
		currentURL = strings.TrimSpace(r.Referer())
	}
	if currentURL == "" {
		return "desc"
	}
	u, err := url.Parse(currentURL)
	if err != nil {
		return "desc"
	}
	return normalizeSortOrder(u.Query().Get("ordem"), "desc")
}

func groupTransactionsByDay(rows []TransactionRow) []TransactionDayGroup {
	if len(rows) == 0 {
		return nil
	}

	groups := make([]TransactionDayGroup, 0, len(rows))
	for _, row := range rows {
		if len(groups) == 0 || groups[len(groups)-1].DateLabel != row.DateLabel {
			groups = append(groups, TransactionDayGroup{
				DateLabel:  row.DateLabel,
				FirstIndex: row.ListIndex,
			})
		}
		groups[len(groups)-1].Transactions = append(groups[len(groups)-1].Transactions, row)
	}
	return groups
}

func invoiceVisibilityLabels(total, limit int) (string, string, string) {
	if limit <= 0 {
		limit = total
	}
	if total <= 0 {
		return "", "", ""
	}
	visible := total
	if visible > limit {
		visible = limit
	}
	hidden := total - visible
	visibleLabel := fmt.Sprintf("Você está vendo %d de %d lançamentos.", visible, total)
	if hidden <= 0 {
		return visibleLabel, "", ""
	}
	return visibleLabel,
		fmt.Sprintf("Ainda há %d lançamentos ocultos nesta fatura.", hidden),
		fmt.Sprintf("Carregar mais %d lançamentos", hidden)
}

func parseInvoiceReference(reference string) (int, time.Month, error) {
	if len(reference) != 7 || reference[4] != '-' {
		return 0, 0, fmt.Errorf("referência de fatura inválida")
	}
	year, err := strconv.Atoi(reference[:4])
	if err != nil {
		return 0, 0, err
	}
	monthInt, err := strconv.Atoi(reference[5:])
	if err != nil {
		return 0, 0, err
	}
	if monthInt < 1 || monthInt > 12 {
		return 0, 0, fmt.Errorf("mês de fatura inválido")
	}
	return year, time.Month(monthInt), nil
}

func nextInvoiceMonth(year int, month time.Month) (int, time.Month) {
	month++
	if month > 12 {
		return year + 1, 1
	}
	return year, month
}

func prevInvoiceMonth(year int, month time.Month) (int, time.Month) {
	month--
	if month < 1 {
		return year - 1, 12
	}
	return year, month
}

func clampDayToMonth(year int, month time.Month, requestedDay int64) int {
	if requestedDay < 1 {
		requestedDay = 1
	}
	lastDay := time.Date(year, month+1, 1, 12, 0, 0, 0, time.UTC).Add(-24 * time.Hour).Day()
	if requestedDay > int64(lastDay) {
		return lastDay
	}
	return int(requestedDay)
}

func clampedDateUTC(year int, month time.Month, requestedDay int64) time.Time {
	return time.Date(year, month, clampDayToMonth(year, month, requestedDay), 12, 0, 0, 0, time.UTC)
}

func invoiceDatesForReference(year int, month time.Month, closingDay, dueDay int64) (closingUnix, dueUnix int64) {
	dueDate := clampedDateUTC(year, month, dueDay)
	closingYear, closingMonth := year, month
	if dueDay < closingDay {
		closingYear, closingMonth = prevInvoiceMonth(closingYear, closingMonth)
	}
	closingDate := clampedDateUTC(closingYear, closingMonth, closingDay)
	return closingDate.Unix(), dueDate.Unix()
}

func invoiceCycleBoundsForReference(year int, month time.Month, closingDay, dueDay int64) (startUnix, endUnix int64) {
	currentClosingUnix, _ := invoiceDatesForReference(year, month, closingDay, dueDay)
	prevYear, prevMonth := prevInvoiceMonth(year, month)
	previousClosingUnix, _ := invoiceDatesForReference(prevYear, prevMonth, closingDay, dueDay)
	return previousClosingUnix + 1, currentClosingUnix + 1
}

func sumInvoiceTotal(db *sql.DB, workspaceID, invoiceID string) (int64, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return 0, fmt.Errorf("workspace_id obrigatório para somatório de fatura")
	}
	var total int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM transactions
		WHERE workspace_id = ? AND invoice_id = ? AND type = 'EXPENSE'
	`, workspaceID, invoiceID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func sumInvoiceTotalTx(tx *sql.Tx, workspaceID, invoiceID string) int64 {
	var total int64
	tx.QueryRow(`
		SELECT COALESCE(SUM(amount), 0) FROM transactions
		WHERE workspace_id = ? AND invoice_id = ? AND type = 'EXPENSE'
	`, workspaceID, invoiceID).Scan(&total)
	return total
}

func sumActiveInvoicePaymentsTx(tx *sql.Tx, workspaceID, invoiceID string) (int64, error) {
	var total int64
	err := tx.QueryRow(`
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM invoice_payments
		WHERE workspace_id = ? AND invoice_id = ? AND reversed_at IS NULL
	`, workspaceID, invoiceID).Scan(&total)
	return total, err
}

func (h *FaturasHandler) renderNoInvoiceForPeriod(w http.ResponseWriter, r *http.Request, accountID string, mes, ano int) {
	prev := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	next := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	cardName := ""
	if err := h.DB.QueryRow(`SELECT name FROM accounts WHERE id = ? AND workspace_id = ?`, accountID, h.WorkspaceID).Scan(&cardName); err != nil {
		cardName = "Cartao"
	}
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	periodLabel := fmt.Sprintf("%s/%d", months[mes-1], ano)

	isHX := r.Header.Get("HX-Request") != ""

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if !isHX {
		baseData := struct {
			Title           string
			IsBusiness      bool
			ProfilePhotoURL string
		}{
			Title:           "Fatura Não Encontrada",
			IsBusiness:      workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness,
			ProfilePhotoURL: queryUserProfilePhotoURL(h.DB, h.UserID),
		}
		_ = h.Templates.ExecuteTemplate(w, "layout-start", baseData)
	}

	fmt.Fprintf(w, `<section id="main-content" class="mx-auto max-w-md px-4 pt-6 pb-28">
<header class="mb-5 flex items-center gap-3">
<a href="/" hx-get="/" hx-target="#main-content" hx-select="#main-content" hx-push-url="true" class="w-9 h-9 rounded-xl bg-white border border-zinc-200 flex items-center justify-center hover:bg-zinc-100 active:scale-95 transition-all shrink-0 dark:bg-zinc-900/60 dark:border-zinc-800/50 dark:hover:bg-zinc-800/60">
<i data-lucide="arrow-left" class="w-4 h-4 text-zinc-400 dark:text-zinc-400"></i>
</a>
<div>
<p class="text-xs text-zinc-500 uppercase tracking-widest font-medium">FATURA</p>
<h1 class="text-lg font-bold text-zinc-800 dark:text-white/95 truncate">%s</h1>
</div>
</header>
<div class="rounded-2xl border border-zinc-200 bg-white p-5 dark:border-zinc-800/50 dark:bg-zinc-900/50">
<p class="text-xs font-semibold uppercase tracking-wide text-zinc-500">%s</p>
<h2 class="mt-2 text-lg font-bold text-zinc-800 dark:text-white/95">Nenhuma fatura para este período</h2>
<p class="mt-2 text-sm text-zinc-500">Use "Abrir fatura" para criar manualmente esta competência ou navegue entre os meses.</p>
<div class="mt-4 flex gap-2">
<a href="%s" hx-get="%s" hx-target="#main-content" hx-select="#main-content" hx-push-url="true" class="rounded-xl border border-zinc-200 px-3 py-2 text-xs font-semibold text-zinc-600 hover:bg-zinc-50 dark:border-zinc-800/40 dark:text-zinc-400 dark:hover:bg-zinc-800/40">Mês anterior</a>
<a href="%s" hx-get="%s" hx-target="#main-content" hx-select="#main-content" hx-push-url="true" class="rounded-xl border border-zinc-200 px-3 py-2 text-xs font-semibold text-zinc-600 hover:bg-zinc-50 dark:border-zinc-800/40 dark:text-zinc-400 dark:hover:bg-zinc-800/40">Mês seguinte</a>
</div>
<form class="mt-3" hx-post="/cartoes/%s/faturas/abrir?mes=%d&ano=%d" hx-target="#main-content" hx-select="#main-content" hx-push-url="true">
<button type="submit" class="w-full rounded-xl bg-indigo-500 px-3 py-2 text-sm font-semibold text-white hover:bg-indigo-600 transition-colors">Abrir fatura</button>
</form>
</div>
</section>`,
		cardName,
		periodLabel,
		faturaMonthURL(accountID, int(prev.Month()), prev.Year(), faturaSortOrderFromRequest(r)),
		faturaMonthURL(accountID, int(prev.Month()), prev.Year(), faturaSortOrderFromRequest(r)),
		faturaMonthURL(accountID, int(next.Month()), next.Year(), faturaSortOrderFromRequest(r)),
		faturaMonthURL(accountID, int(next.Month()), next.Year(), faturaSortOrderFromRequest(r)),
		accountID, mes, ano,
	)

	if !isHX {
		_ = h.Templates.ExecuteTemplate(w, "layout-end", nil)
	}
}

func (h *FaturasHandler) renderDashboardOOB(w http.ResponseWriter) error {
	data := BuildDashboardData(h.DB, h.UserID, h.WorkspaceID)
	data.OOB = true

	var buf bytes.Buffer
	for _, templateName := range dashboardOOBTemplateNames() {
		if h.Templates.Lookup(templateName) == nil {
			continue
		}
		if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
			return err
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

func (h *FaturasHandler) renderInvoiceAndDashboardOOB(w http.ResponseWriter, r *http.Request, invoiceID string) error {
	invoiceData, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, invoiceID, faturaSortOrderFromCurrentRequest(r))
	if err != nil {
		return err
	}
	invoiceData.OOB = false

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-content", invoiceData); err != nil {
		return err
	}

	dashboardData := BuildDashboardData(h.DB, h.UserID, h.WorkspaceID)
	dashboardData.OOB = true
	for _, templateName := range dashboardOOBTemplateNames() {
		if h.Templates.Lookup(templateName) == nil {
			continue
		}
		if err := h.Templates.ExecuteTemplate(&buf, templateName, dashboardData); err != nil {
			return err
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = buf.WriteTo(w)
	return err
}

func paymentAccountNameMap(accounts []InvoicePaymentAccountOption) map[string]string {
	m := make(map[string]string, len(accounts))
	for _, a := range accounts {
		m[a.ID] = a.Name
	}
	return m
}

func buildInvoicePaymentRows(payments []models.InvoicePayment, nameMap map[string]string) []InvoicePaymentRow {
	rows := make([]InvoicePaymentRow, 0, len(payments))
	for _, p := range payments {
		rows = append(rows, InvoicePaymentRow{
			DateLabel:   formatDateTimeLabel(p.PaidAt),
			Amount:      MoneyMinor(p.AmountCents),
			AccountName: nameMap[p.AccountID],
			Source:      p.Source,
			Note:        coalesceString(p.Note, ""),
		})
	}
	return rows
}

func coalesceString(s *string, fallback string) string {
	if s != nil && strings.TrimSpace(*s) != "" {
		return *s
	}
	return fallback
}

func execOneTx(tx *sql.Tx, query string, args ...interface{}) error {
	res, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("operação não autorizada ou registro não encontrado")
	}
	return nil
}
