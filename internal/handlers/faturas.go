package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/civildate"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
)

type FaturasHandler struct {
	DB                     *sql.DB
	WriterDB               *sql.DB
	Templates              TemplateEngine
	WorkspaceID            string
	UserID                 string
	paymentWriterTestHooks *invoicePaymentWriterTestHooks
}

type invoicePaymentWriterTestHooks struct {
	beforeBegin func()
	afterBegin  func(*sql.Tx) error
}

type FaturaData struct {
	AppShellAccess
	Title             string
	UserName          string
	UserInitials      string
	ProfilePhotoURL   string
	NotificationCount int
	OOB               bool
	// OOBContent replaces the complete invoice composition in the invoice hub.
	// It prevents the summary, action panel and transaction list from settling
	// at different moments after a financial mutation.
	OOBContent              bool
	InvoiceID               string
	CardName                string
	CardIcon                string
	CardColor               string
	CardProviderSlug        string
	CardProviderMark        string
	CardProviderLogoPath    string
	AccountID               string
	HasActiveFilter         bool
	FilterLabel             string
	ClearFiltersURL         string
	Reference               string
	NextReference           string
	Status                  string
	StatusLabel             string
	StatusIcon              string
	StatusBadgeClass        string
	CycleLabel              string
	FinancialLabel          string
	HasCycleBadge           bool
	HasFinancialBadge       bool
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
	DefaultPaymentAccountID string
	CanAttemptPayment       bool
	CanSubmitPayment        bool
	CanMutateItems          bool
	PaymentDisabledReason   string
	InvoicePayments         []InvoicePaymentRow
	TotalPaid               MoneyDisplay
	PendingAmount           MoneyDisplay
	PendingAmountInput      string
	PaymentDateInput        string
	HasPayments             bool
	HasLegacyPaymentSummary bool
	LegacyPaymentNotice     string
	GeneralRefunds          []InvoiceGeneralRefundRow
	HasGeneralRefunds       bool
	IsBusiness              bool
	ActiveWorkspaceName     string
	ShowSensitiveWarning    bool
	SensitiveWarningText    string
	HideMonthNavigation     bool
	// IsFaturasIndex distinguishes the invoice hub from the direct, shareable
	// invoice detail route. The hub already presents the selected card in its
	// carousel, so repeating the card identity in the detail is unnecessary.
	IsFaturasIndex bool
}

// FaturasIndexData is the read-only workspace overview. It selects one card
// and one competence, then renders the existing invoice content below it.
// Financial mutations remain exclusively in the existing invoice handlers.
type FaturasIndexData struct {
	AppShellAccess
	Title               string
	UserName            string
	UserInitials        string
	ProfilePhotoURL     string
	NotificationCount   int
	IsBusiness          bool
	ActiveWorkspaceName string
	Cards               []CreditCardCard
	SelectedCardID      string
	PeriodLabel         string
	MonthOptions        []MonthOption
	PreviousURL         string
	NextURL             string
	RefreshCardsURL     string
	HasSelectedInvoice  bool
	SelectedFatura      FaturaData
	SelectedEmpty       FaturaEmptyData
}

type InvoicePaymentRow struct {
	DateLabel   string
	Amount      MoneyDisplay
	AccountName string
	Source      string
	Note        string
}

type InvoiceGeneralRefundRow struct {
	ID          string
	DateLabel   string
	Amount      MoneyDisplay
	Description string
}

// TransactionRefundRow is an active, auditable refund entry. Refund records
// are never overwritten or deleted: corrections reverse the original entry
// and create a replacement, preserving the financial audit trail.
type TransactionRefundRow struct {
	ID          string
	Amount      MoneyDisplay
	Description string
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

type FaturaEmptyData struct {
	AppShellAccess
	Title               string
	UserName            string
	UserInitials        string
	ProfilePhotoURL     string
	NotificationCount   int
	IsBusiness          bool
	ActiveWorkspaceName string
	CardName            string
	PeriodLabel         string
	PreviousURL         string
	NextURL             string
	OpenInvoiceURL      string
	IsFaturasIndex      bool
}

// populateFaturaShellData adds only the presentation context required by the
// authenticated V2 shell. Invoice state, values and payment eligibility stay
// exclusively in buildFaturaDataForInvoice.
func (h *FaturasHandler) populateFaturaShellData(data *FaturaData) {
	data.UserName, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.NotificationCount = queryDashboardNotificationCount(h.DB, h.UserID, h.WorkspaceID)
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, data.IsBusiness)
}

func (h *FaturasHandler) populateFaturasIndexShellData(data *FaturasIndexData) {
	data.UserName, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.NotificationCount = queryDashboardNotificationCount(h.DB, h.UserID, h.WorkspaceID)
	data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness
	data.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, data.IsBusiness)
}

// HandleFaturasIndex is the canonical workspace view for credit-card invoices.
// The legacy detail routes remain available for deep links and mutations.
func (h *FaturasHandler) HandleFaturasIndex(w http.ResponseWriter, r *http.Request) {
	data, err := h.buildFaturasIndexData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.populateFaturasIndexShellData(&data)

	templateName := "faturas-index-page"
	if r.URL.Query().Get("partial") == "cards" && r.Header.Get("HX-Request") != "" {
		templateName = "faturas-index-card-rail"
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template faturas index error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// HandleFaturasIndexContent swaps only the selected invoice, preserving the
// card rail and its current scroll position.
func (h *FaturasHandler) HandleFaturasIndexContent(w http.ResponseWriter, r *http.Request) {
	data, err := h.buildFaturasIndexData(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if data.SelectedCardID == "" {
		http.Error(w, "nenhum cartão encontrado", http.StatusNotFound)
		return
	}

	var buf bytes.Buffer
	if data.HasSelectedInvoice {
		if err := h.Templates.ExecuteTemplate(&buf, "faturas-content", data.SelectedFatura); err != nil {
			log.Printf("template faturas index content error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	} else if err := h.Templates.ExecuteTemplate(&buf, "faturas-empty-content", data.SelectedEmpty); err != nil {
		log.Printf("template faturas index empty content error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	month, year, _ := faturasIndexPeriodFromRequest(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("HX-Push-Url", faturasIndexURL(data.SelectedCardID, month, year, faturaSortOrderFromRequest(r)))
	_, _ = buf.WriteTo(w)
}

func (h *FaturasHandler) buildFaturasIndexData(r *http.Request) (FaturasIndexData, error) {
	month, year, err := faturasIndexPeriodFromRequest(r)
	if err != nil {
		return FaturasIndexData{}, err
	}
	if err := autoCloseWorkspaceInvoices(h.DB, h.WorkspaceID); err != nil {
		log.Printf("auto close workspace invoices on invoice hub error: %v", err)
	}
	if r != nil && r.URL.Path == "/faturas" &&
		strings.TrimSpace(r.URL.Query().Get("mes")) == "" &&
		strings.TrimSpace(r.URL.Query().Get("ano")) == "" {
		month, year, err = initialFaturasIndexPeriod(h.DB, h.WorkspaceID, appTime(time.Now()))
		if err != nil {
			return FaturasIndexData{}, fmt.Errorf("selecionar competência inicial: %w", err)
		}
	}

	cards, err := queryFaturasIndexCards(h.DB, h.WorkspaceID, year, time.Month(month))
	if err != nil {
		return FaturasIndexData{}, fmt.Errorf("carregar cartões: %w", err)
	}
	// Keep the carousel order identical to the Dashboard for the selected
	// competence: overdue, upcoming, no due date, then paid/zero-value cards.
	// The sort is stable, so the configured account order remains the tie-break.
	SortDashboardCardsByPaymentPriority(cards, time.Now())

	selectedCardID := strings.TrimSpace(r.URL.Query().Get("cartao"))
	if selectedCardID == "" && len(cards) > 0 {
		selectedCardID = cards[0].ID
	}
	selectedIndex := -1
	for i := range cards {
		cards[i].Href = faturasIndexURL(cards[i].ID, month, year, faturaSortOrderFromRequest(r))
		cards[i].ContentURL = faturasIndexContentURL(cards[i].ID, month, year, faturaSortOrderFromRequest(r))
		cards[i].IsFaturasIndex = true
		cards[i].IsSelected = cards[i].ID == selectedCardID
		if cards[i].IsSelected {
			selectedIndex = i
		}
	}
	if selectedIndex < 0 && selectedCardID != "" {
		return FaturasIndexData{}, fmt.Errorf("cartão não encontrado")
	}

	selectedMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	prev := selectedMonth.AddDate(0, -1, 0)
	next := selectedMonth.AddDate(0, 1, 0)
	data := FaturasIndexData{
		Title:           "Faturas",
		Cards:           cards,
		SelectedCardID:  selectedCardID,
		PeriodLabel:     faturasIndexPeriodLabel(selectedMonth),
		MonthOptions:    buildFaturasIndexMonthOptions(selectedCardID, selectedMonth, faturaSortOrderFromRequest(r)),
		PreviousURL:     faturasIndexURL(selectedCardID, int(prev.Month()), prev.Year(), faturaSortOrderFromRequest(r)),
		NextURL:         faturasIndexURL(selectedCardID, int(next.Month()), next.Year(), faturaSortOrderFromRequest(r)),
		RefreshCardsURL: faturasIndexCardsURL(selectedCardID, month, year, faturaSortOrderFromRequest(r)),
	}
	if selectedIndex < 0 {
		return data, nil
	}

	selectedCard := cards[selectedIndex]
	if selectedCard.InvoiceID != "" {
		fatura, err := buildFaturaDataForInvoice(h.DB, h.WorkspaceID, selectedCard.InvoiceID, faturaSortOrderFromRequest(r))
		if err != nil {
			return FaturasIndexData{}, fmt.Errorf("carregar fatura selecionada: %w", err)
		}
		configureFaturaForIndex(&fatura, selectedCardID, month, year)
		data.HasSelectedInvoice = true
		data.SelectedFatura = fatura
		return data, nil
	}

	data.SelectedEmpty = faturasIndexEmptyData(selectedCard, month, year, faturaSortOrderFromRequest(r))
	return data, nil
}

func queryFaturasIndexCards(db *sql.DB, workspaceID string, year int, month time.Month) ([]CreditCardCard, error) {
	return queryFaturasIndexCardsAt(db, workspaceID, year, month, time.Now())
}

func queryFaturasIndexCardsAt(db *sql.DB, workspaceID string, year int, month time.Month, now time.Time) ([]CreditCardCard, error) {
	rows, err := db.Query(`
		SELECT a.id, a.name, COALESCE(cc.closing_day, 0), COALESCE(cc.due_day, 0),
			COALESCE(cc.credit_limit, 0), COALESCE(NULLIF(a.provider_slug, ''), 'custom'),
			COALESCE(NULLIF(a.color, ''), '#6B7280'), COALESCE(NULLIF(a.icon, ''), '')
		FROM accounts a
		LEFT JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.workspace_id = ? AND a.type = ? AND a.archived_at IS NULL
		ORDER BY a.sort_order ASC, a.name ASC
	`, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type cardSeed struct {
		id, name, providerSlug, color, icon string
		closingDay, dueDay, creditLimit     int64
	}
	var seeds []cardSeed
	for rows.Next() {
		var seed cardSeed
		if err := rows.Scan(&seed.id, &seed.name, &seed.closingDay, &seed.dueDay, &seed.creditLimit, &seed.providerSlug, &seed.color, &seed.icon); err != nil {
			return nil, err
		}
		seeds = append(seeds, seed)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	reference := fmt.Sprintf("%04d-%02d", year, int(month))
	cards := make([]CreditCardCard, 0, len(seeds))
	for _, seed := range seeds {
		var invoiceID, status string
		var closingUnix, dueUnix int64
		err := db.QueryRow(`
			SELECT i.id, i.status, i.closing_date, i.due_date
			FROM invoices i
			JOIN accounts a ON a.id = i.account_id
			WHERE i.account_id = ? AND i.reference = ? AND a.workspace_id = ? AND a.type = ?
		`, seed.id, reference, workspaceID, models.AccountTypeCreditCard).Scan(&invoiceID, &status, &closingUnix, &dueUnix)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == sql.ErrNoRows {
			closingUnix, dueUnix = invoiceDatesForReference(year, month, seed.closingDay, seed.dueDay)
		}

		outstanding, err := sumCardOutstandingLimit(db, workspaceID, seed.id)
		if err != nil {
			return nil, err
		}
		available := seed.creditLimit - outstanding
		if available < 0 {
			available = 0
		}
		limitPercent := 0
		if seed.creditLimit > 0 {
			limitPercent = int((available * 100) / seed.creditLimit)
		}
		if limitPercent > 100 {
			limitPercent = 100
		}

		amount := int64(0)
		if invoiceID != "" {
			amount, err = sumInvoiceTotal(db, workspaceID, invoiceID)
			if err != nil {
				return nil, err
			}
		}
		normalizedProvider := normalizeAccountProviderSlug(seed.providerSlug)
		icon := seed.icon
		if icon == "" {
			icon = accountVisualByProvider(normalizedProvider, models.AccountTypeCreditCard)
		}
		dueCompact := "—"
		if dueUnix > 0 {
			months := []string{"jan", "fev", "mar", "abr", "mai", "jun", "jul", "ago", "set", "out", "nov", "dez"}
			due := time.Unix(dueUnix, 0)
			dueCompact = fmt.Sprintf("%d/%s", due.Day(), months[due.Month()-1])
		}
		statusLabel, statusIcon, statusBadgeClass := "Sem fatura", "calendar-search", "cb-status-chip-neutral"
		if invoiceID != "" {
			statusLabel = invoiceDisplayStatusLabel(status, dueUnix)
			statusIcon, statusBadgeClass = invoiceStatusVisual(status, statusLabel)
		}
		closingLabel := "período selecionado"
		if invoiceID != "" && closingUnix > 0 && status != models.InvoiceStatusPaid {
			days := invoiceClosingCivilDays(closingUnix, now)
			switch {
			case days < 0:
				closingLabel = "fechada"
			case days == 0:
				closingLabel = "fecha hoje"
			case days == 1:
				closingLabel = "fecha em 1 dia"
			default:
				closingLabel = fmt.Sprintf("fecha em %d dias", days)
			}
		}

		cards = append(cards, CreditCardCard{
			ID:               seed.id,
			InvoiceID:        invoiceID,
			Name:             seed.name,
			DueDay:           fmt.Sprintf("%d", seed.dueDay),
			DueDateUnix:      dueUnix,
			DueDateCompact:   dueCompact,
			ClosingDaysLabel: closingLabel,
			Reference:        reference,
			Status:           status,
			StatusLabel:      statusLabel,
			StatusIcon:       statusIcon,
			StatusBadgeClass: statusBadgeClass,
			Amount:           amount,
			Money:            MoneyMinor(amount),
			LimitMoney:       MoneyMinor(available),
			LimitPercent:     limitPercent,
			Icon:             icon,
			Color:            normalizeHexColor(seed.color, "#6B7280"),
			ProviderSlug:     normalizedProvider,
			ProviderMark:     accountProviderMark(normalizedProvider, seed.name),
			ProviderLogoPath: accountProviderLogoPathForAccount(seed.providerSlug, seed.name),
		})
	}
	return cards, nil
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
	h.populateFaturaShellData(&data)

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
	h.populateFaturaShellData(&data)
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

	// Keep invoice-id links valid, but make the invoice hub the only full-page
	// destination. Resolving from the stored reference is essential here: a
	// closed unpaid invoice may belong to the prior calendar month.
	var accountID, reference string
	err := h.DB.QueryRow(`
		SELECT i.account_id, i.reference
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, h.WorkspaceID, models.AccountTypeCreditCard).Scan(&accountID, &reference)
	if err != nil {
		log.Printf("resolve invoice hub redirect error: %v", err)
		http.Error(w, "fatura nao encontrada", http.StatusNotFound)
		return
	}

	destination, err := faturasIndexURLForReference(accountID, reference, faturaSortOrderFromRequest(r))
	if err != nil {
		log.Printf("build invoice hub redirect error: %v", err)
		http.Error(w, "fatura nao encontrada", http.StatusNotFound)
		return
	}
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
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
	h.populateFaturaShellData(&data)

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
	if accountID == "" || mes < 1 || mes > 12 || ano < 2020 || ano > appTime(time.Now()).Year()+10 {
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
	h.populateFaturaShellData(&data)

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
	if accountID == "" || mes < 1 || mes > 12 || ano < 2020 || ano > appTime(time.Now()).Year()+10 {
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
	h.populateFaturaShellData(&data)
	var buf bytes.Buffer
	templateName := "faturas-page"
	if r.Header.Get("HX-Request") != "" {
		configureFaturaForIndex(&data, accountID, mes, ano)
		templateName = "faturas-content"
		w.Header().Set("HX-Push-Url", faturasIndexURL(accountID, mes, ano, faturaSortOrderFromRequest(r)))
	}
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
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
			COALESCE(SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END), 0) - `+invoiceGeneralRefundTotalIASQL+` as total
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

	projected := projectedInvoiceMonths(appMonthStartUTC(time.Now()), 12, closingDay, dueDay)

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

	if h.WriterDB == nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if h.paymentWriterTestHooks != nil && h.paymentWriterTestHooks.beforeBegin != nil {
		h.paymentWriterTestHooks.beforeBegin()
	}
	tx, err := h.WriterDB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if h.paymentWriterTestHooks != nil && h.paymentWriterTestHooks.afterBegin != nil {
		if err := h.paymentWriterTestHooks.afterBegin(tx); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}

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

	snapshot, err := loadInvoiceFinancialSnapshotTx(tx, h.WorkspaceID, invoiceID)
	if err != nil {
		http.Error(w, "erro ao carregar estado financeiro da fatura", http.StatusInternalServerError)
		return
	}
	if err := validateInvoiceForNewPayment(snapshot); err != nil {
		if errors.Is(err, errInvoiceStateTransitionConflict) {
			http.Error(w, "estado financeiro da fatura não permite novo pagamento", http.StatusConflict)
		} else {
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	total := snapshot.Total
	if total <= 0 {
		http.Error(w, "fatura sem valor para pagar", http.StatusBadRequest)
		return
	}
	totalPaid := snapshot.EffectivePaid
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

	if err := reconcileInvoiceAfterPaymentMutationTx(tx, h.WorkspaceID, invoiceID, now); err != nil {
		if errors.Is(err, errInvoiceStateTransitionConflict) {
			http.Error(w, "estado financeiro da fatura não permite novo pagamento", http.StatusConflict)
		} else {
			http.Error(w, "erro ao reconciliar pagamento da fatura", http.StatusInternalServerError)
		}
		return
	}

	if err := services.ApplyBalanceEffect(tx, h.WorkspaceID, "EXPENSE", paymentAccountType, "paid", paymentAmount, paymentAccountID, "", now); err != nil {
		http.Error(w, "erro ao debitar conta", http.StatusInternalServerError)
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
		       COALESCE(cc.credit_limit, 0),
		       COALESCE(cc.default_payment_account_id, '')
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		LEFT JOIN credit_cards cc ON cc.account_id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(&data.AccountID, &data.Reference, &data.Status, &closingUnix, &dueUnix, &data.CardName, &providerSlug, &cardColor, &creditLimitCents, &data.DefaultPaymentAccountID)
	if err != nil {
		return data, fmt.Errorf("fatura: %w", err)
	}
	data.NextReference, _ = nextInvoiceReference(data.Reference)

	if data.DefaultPaymentAccountID != "" {
		var validPaymentAccount string
		err := db.QueryRow(`
			SELECT id FROM accounts
			WHERE id = ? AND workspace_id = ? AND type IN (?, ?)
		`, data.DefaultPaymentAccountID, workspaceID, models.AccountTypeChecking, models.AccountTypeSavings).Scan(&validPaymentAccount)
		if err != nil {
			data.DefaultPaymentAccountID = ""
		}
	}

	if err := autoCloseInvoices(db, workspaceID, data.AccountID); err != nil {
		return data, err
	}
	if err := db.QueryRow(`
		SELECT i.status FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ?
	`, invoiceID, workspaceID).Scan(&data.Status); err != nil {
		return data, err
	}
	switch data.Status {
	case models.InvoiceStatusOpen:
		if err := materializeRecurringInvoiceTransactions(db, workspaceID, invoiceID); err != nil {
			return data, err
		}
	case models.InvoiceStatusClosed, models.InvoiceStatusPaid:
		// Historical page reads never own the terminal recurring decision. The
		// authoritative catch-up persists frozen blocks; this path is a no-op.
	default:
		return data, fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
	}

	data.CardProviderSlug = normalizeAccountProviderSlug(providerSlug)
	data.CardProviderMark = accountProviderMark(data.CardProviderSlug, data.CardName)
	data.CardProviderLogoPath = accountProviderLogoPathForAccount(providerSlug, data.CardName)
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

	snapshot, err := loadInvoiceFinancialSnapshotDB(db, workspaceID, invoiceID)
	if err != nil {
		return data, err
	}
	total := snapshot.Total
	effectivePaid := snapshot.EffectivePaid
	data.Total = MoneyMinor(total)
	data.CreditLimit = MoneyMinor(creditLimitCents)
	if creditLimitCents > 0 {
		data.HasCreditLimit = true
		outstandingLimit, err := sumCardOutstandingLimit(db, workspaceID, data.AccountID)
		if err != nil {
			return data, err
		}
		data.LimitUsed = MoneyMinor(outstandingLimit)
		limitAvailable := creditLimitCents - outstandingLimit
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
	data.PaymentDateInput = appTime(time.Now()).Format("2006-01-02")

	pendingAmountRaw := CalculateInvoicePendingAmount(total, effectivePaid)
	data.TotalPaid = MoneyMinor(effectivePaid)
	data.PendingAmount = MoneyMinor(pendingAmountRaw)
	data.PendingAmountInput = formatCurrencyCentsBase(pendingAmountRaw)
	if snapshot.TotalPaymentRecordCount == 0 && snapshot.LegacyPaidAmount > 0 {
		data.HasLegacyPaymentSummary = true
		data.LegacyPaymentNotice = "Pagamento anterior ao hist\u00F3rico detalhado"
	}

	payments, err := QueryActiveInvoicePayments(db, workspaceID, invoiceID)
	if err == nil && len(payments) > 0 {
		data.HasPayments = true
		accountNameMap := paymentAccountNameMap(data.PaymentAccounts)
		data.InvoicePayments = buildInvoicePaymentRows(payments, accountNameMap)
	}
	generalRefunds, err := queryActiveInvoiceGeneralRefunds(db, workspaceID, invoiceID)
	if err != nil {
		return data, err
	}
	data.GeneralRefunds = generalRefunds
	data.HasGeneralRefunds = len(generalRefunds) > 0
	for i := range data.PaymentAccounts {
		data.PaymentAccounts[i].BalanceAfterPayment = MoneyMinor(data.PaymentAccounts[i].BalanceCents - pendingAmountRaw)
	}

	data.CanAttemptPayment = data.Status == models.InvoiceStatusOpen || data.Status == models.InvoiceStatusClosed
	data.CanSubmitPayment = data.CanAttemptPayment && pendingAmountRaw > 0 && len(data.PaymentAccounts) > 0
	data.CanMutateItems = data.Status != models.InvoiceStatusPaid

	cycleBadge := computeCycleBadge(closingUnix, time.Now())
	finBadge := computeFinancialBadge(pendingAmountRaw, effectivePaid)
	data.CycleLabel = cycleBadge
	data.HasCycleBadge = cycleBadge != ""
	data.FinancialLabel = finBadge
	data.HasFinancialBadge = finBadge != "" && finBadge != data.StatusLabel
	data.ShowSensitiveWarning = data.Status == models.InvoiceStatusClosed || data.Status == models.InvoiceStatusPaid
	if data.ShowSensitiveWarning {
		now := time.Now().Unix()
		closingPassed := closingUnix > 0 && closingUnix <= now
		if data.Status == models.InvoiceStatusClosed {
			data.SensitiveWarningText = "Esta fatura está fechada. Alterações aqui são tratadas como correção manual e podem reabrir pendência."
		} else if closingPassed {
			data.SensitiveWarningText = "Esta fatura está fechada e paga. Alterações aqui são tratadas como correção manual e podem reabrir pendência."
		} else {
			data.SensitiveWarningText = "Esta fatura está paga. Novos lançamentos neste ciclo podem reabrir pendência e alterar o limite disponível."
		}
	}
	if data.CanAttemptPayment && pendingAmountRaw <= 0 {
		data.PaymentDisabledReason = "Sem valor para pagar"
	} else if data.CanAttemptPayment && len(data.PaymentAccounts) == 0 {
		data.PaymentDisabledReason = "Nenhuma conta corrente disponível"
	}

	rows, err := db.Query(`
		SELECT t.id, t.type, t.amount, t.date, t.created_at, t.updated_at, t.description, t.status,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			t.installment_number,
			t.total_installments,
			a.name,
			COALESCE(u.name, 'Usuário removido'),
			CASE WHEN t.total_installments > 1 OR t.recurring_rule_id IS NOT NULL THEN 1 ELSE 0 END,
			CASE WHEN t.total_installments > 1 AND t.recurring_rule_id IS NULL THEN 1 ELSE 0 END,
			(t.amount - (`+transactionNetAmountTSQL+`)),
			COALESCE((
				SELECT GROUP_CONCAT(r.description, ' • ') FROM transaction_refunds r
				WHERE r.workspace_id = t.workspace_id AND r.transaction_id = t.id AND r.reversed_at IS NULL
			), '')
		FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id AND c.workspace_id = t.workspace_id
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN users u ON u.id = t.user_id
		WHERE t.invoice_id = ? AND t.workspace_id = ?
		ORDER BY t.date DESC, t.created_at DESC
	`, invoiceID, workspaceID)
	if err != nil {
		return data, fmt.Errorf("query fatura txs: %w", err)
	}
	defer rows.Close()

	data.Transactions, err = scanInvoiceTransactionRows(rows)
	if err != nil {
		return data, fmt.Errorf("read fatura txs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return data, fmt.Errorf("close fatura txs: %w", err)
	}
	if err := attachActiveTransactionRefundRows(db, workspaceID, data.Transactions); err != nil {
		return data, fmt.Errorf("query invoice refund entries: %w", err)
	}
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
	if err := db.QueryRow(`
		SELECT cc.closing_day, cc.due_day
		FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE cc.account_id = ? AND a.workspace_id = ?
	`, accountID, workspaceID).Scan(&closingDay, &dueDay); err != nil {
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
		LEFT JOIN categories c ON c.id = rr.category_id AND c.workspace_id = rr.workspace_id
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
	if err := db.QueryRow(`
		SELECT cc.closing_day, cc.due_day
		FROM credit_cards cc
		JOIN accounts a ON a.id = cc.account_id
		WHERE cc.account_id = ? AND a.workspace_id = ?
	`, accountID, workspaceID).Scan(&closingDay, &dueDay); err != nil {
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
	seriesID             sql.NullString
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
	var accountID, reference, invoiceStatus string
	var closingUnix, dueUnix, closingDay, dueDay int64
	if err := tx.QueryRow(`
		SELECT i.account_id, i.reference, i.status, i.closing_date, i.due_date, cc.closing_day, cc.due_day
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		JOIN credit_cards cc ON cc.account_id = i.account_id
		WHERE i.id = ? AND a.workspace_id = ? AND a.type = ?
	`, invoiceID, workspaceID, models.AccountTypeCreditCard).Scan(&accountID, &reference, &invoiceStatus, &closingUnix, &dueUnix, &closingDay, &dueDay); err != nil {
		return err
	}
	switch invoiceStatus {
	case models.InvoiceStatusOpen:
	case models.InvoiceStatusClosed, models.InvoiceStatusPaid:
		return nil
	default:
		return fmt.Errorf("%w: unsupported invoice status", errInvoiceStateStructural)
	}

	refYear, refMonth, err := parseInvoiceReference(reference)
	if err != nil {
		return err
	}
	cycleStartUnix, cycleEndUnix := invoiceCycleBoundsForReference(refYear, refMonth, closingDay, dueDay)
	cycleStart := time.Unix(cycleStartUnix, 0).UTC()
	cycleEnd := time.Unix(cycleEndUnix, 0).UTC()

	rows, err := tx.Query(`
		SELECT rr.id, rr.series_id, rr.user_id, rr.destination_account_id, rr.category_id, rr.amount, rr.description, rr.start_date, rr.frequency, rr.total_occurrences
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
		if err := rows.Scan(&rule.id, &rule.seriesID, &rule.userID, &rule.destinationAccountID, &rule.categoryID, &rule.amount, &rule.description, &rule.startDate, &rule.frequency, &rule.totalOccurrences); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, rule)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, rule := range rules {
		seriesID, err := EnsureRecurringSeriesForRuleTx(tx, workspaceID, rule.id, now)
		if err != nil {
			return err
		}
		rule.seriesID = sql.NullString{String: seriesID, Valid: true}

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
	if _, err := ensureAutomaticInvoiceAcceptsTransactionTx(tx, workspaceID, accountID, invoiceID); err != nil {
		if errors.Is(err, errAutomaticInvoiceFrozen) {
			return nil
		}
		return err
	}

	if rule.seriesID.Valid && strings.TrimSpace(rule.seriesID.String) != "" {
		_, skipped, blocked, err := recurringOccurrenceTerminalConflictsTx(tx, workspaceID, rule.seriesID.String, seq)
		if err != nil {
			return err
		}
		if skipped && blocked {
			return fmt.Errorf("%w: manual skip contradicts frozen block", errInvoiceStateStructural)
		}
		if skipped {
			return nil
		}
		resolved, err := resolveRecurringOccurrenceBlockTx(tx, recurringOccurrenceBlockExpectation{
			WorkspaceID:        workspaceID,
			SeriesID:           rule.seriesID.String,
			RecurringRuleID:    rule.id,
			RecurrenceSequence: seq,
			OccurrenceDate:     MustCivilWriteDate(civildate.LegacyUnixToCivilDateUTC(occurrenceUnix).String()),
			AccountID:          accountID,
		})
		if err != nil {
			return err
		}
		if resolved {
			return nil
		}
	}

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
			SET invoice_id = ?, status = 'paid', series_id = COALESCE(series_id, ?), updated_at = ?
			WHERE id = ? AND workspace_id = ? AND invoice_override_id IS NULL
		`, invoiceID, nullStringInterface(rule.seriesID), now, existingID, workspaceID)
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
		INSERT OR IGNORE INTO transactions (id, workspace_id, user_id, account_id, destination_account_id, category_id, invoice_id, type, amount, date, description, installment_number, total_installments, parent_id, recurring_rule_id, recurrence_sequence, series_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 1, NULL, ?, ?, ?, 'paid', ?, ?)
	`, id, workspaceID, rule.userID, accountID, destID, catID, invoiceID, models.TransactionTypeExpense, rule.amount, occurrenceUnix, rule.description, rule.id, seq, nullStringInterface(rule.seriesID), now, now)
	return execErr
}

func invoiceReferenceForDate(t time.Time, closingDay int64) string {
	year, month := t.Year(), t.Month()
	if int64(t.Day()) >= closingDay {
		year, month = nextInvoiceMonth(year, month)
	}
	return fmt.Sprintf("%04d-%02d", year, int(month))
}

func calculateInvoiceDates(t time.Time, closingDay, dueDay int64) (closingUnix, dueUnix int64, reference string) {
	year, month := t.Year(), t.Month()
	effectiveClosing := clampDayToMonth(year, month, closingDay)

	if int64(t.Day()) >= int64(effectiveClosing) {
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

func scanInvoiceTransactionRows(rows *sql.Rows) ([]TransactionRow, error) {
	var list []TransactionRow
	months := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}

	for rows.Next() {
		var row TransactionRow
		var trType string
		var amount int64
		var dateUnix int64
		var catName, catIcon, catColor sql.NullString
		var isSeries, isInstallmentSeries int64

		if err := rows.Scan(
			&row.ID,
			&trType,
			&amount,
			&dateUnix,
			&row.CreatedAt,
			&row.UpdatedAt,
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
			&isInstallmentSeries,
			&row.RefundedAmount,
			&row.RefundDescriptions,
		); err != nil {
			return nil, err
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
		row.IsInstallmentSeries = isInstallmentSeries == 1
		row.HasInstallmentInfo = row.IsSeries || row.TotalInstallments > 1

		t := time.Unix(dateUnix, 0).UTC()
		localTime := time.Unix(dateUnix, 0)
		row.TimeDisplay = localTime.Format("15:04")
		row.DateInput = localTime.Format("2006-01-02")
		row.DateLabel = fmt.Sprintf("%d %s", t.Day(), months[t.Month()-1])
		row.AmountDisplay = "- R$ " + formatCurrencyCentsBase(amount)
		row.AmountInput = formatCurrencyCentsBase(amount)
		row.AmountClass = "text-[#FE414F]"
		row.HasRefund = row.RefundedAmount > 0
		if row.HasRefund {
			row.RefundedAmountDisplay = "+ R$ " + formatCurrencyCentsBase(row.RefundedAmount)
			row.EffectiveAmountDisplay = "R$ " + formatCurrencyCentsBase(amount-row.RefundedAmount)
		}

		list = append(list, row)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return list, nil
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
		today := AppCivilDateUTC(time.Now())
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
			  SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
		      FROM transactions t
		      WHERE t.workspace_id = ?
		        AND t.invoice_id = invoices.id
		  ), 0) - COALESCE((
		      SELECT SUM(ir.amount_cents)
		      FROM invoice_refunds ir
		      WHERE ir.workspace_id = ?
		        AND ir.invoice_id = invoices.id
		        AND ir.reversed_at IS NULL
		  ), 0) = 0
	`, now, now, now, accountID, accountID, workspaceID, models.AccountTypeCreditCard, workspaceID, workspaceID)
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

	invoiceID, reference, status, closingUnix, dueUnix, err = ensureInvoiceForReferenceTx(tx, workspaceID, accountID, refYear, refMonth)
	return invoiceID, reference, status, closingUnix, dueUnix, err
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

	invoiceID, reference, status, closingUnix, dueUnix, err = ensureFirstOpenInvoiceFromReferenceTx(tx, workspaceID, accountID, startReference)
	if err != nil {
		return "", "", "", 0, 0, err
	}
	return invoiceID, reference, status, closingUnix, dueUnix, nil
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
	invoiceID, resolvedReference, status, closingUnix, dueUnix, err = ensureInvoiceForReferenceTx(tx, workspaceID, accountID, year, month)
	return invoiceID, resolvedReference, status, closingUnix, dueUnix, err
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

func queryInvoiceOptionsForAccount(db *sql.DB, workspaceID, accountID string) []InvoiceOption {
	if accountID == "" {
		return nil
	}
	rows, err := db.Query(`
		SELECT
			i.id,
			i.reference,
			i.status,
			i.closing_date,
			i.due_date,
			COALESCE((
				SELECT SUM(CASE WHEN t.type = 'EXPENSE' THEN `+transactionNetAmountTSQL+` ELSE 0 END)
				FROM transactions t
				WHERE t.workspace_id = a.workspace_id AND t.invoice_id = i.id
			), 0) - `+invoiceGeneralRefundTotalIASQL+` AS total,
			`+invoiceEffectivePaidSQLExpr+` AS effective_paid
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE i.account_id = ?
		  AND a.workspace_id = ?
		  AND a.type = ?
		ORDER BY i.reference ASC
		LIMIT 24
	`, accountID, workspaceID, models.AccountTypeCreditCard)
	if err != nil {
		return nil
	}
	defer rows.Close()

	now := time.Now()
	var out []InvoiceOption
	for rows.Next() {
		var id, reference, status string
		var closingUnix, dueUnix, total, effectivePaid int64
		if err := rows.Scan(&id, &reference, &status, &closingUnix, &dueUnix, &total, &effectivePaid); err != nil {
			return nil
		}
		pending := total - effectivePaid
		if pending < 0 {
			pending = 0
		}
		opt := InvoiceOption{
			ID:             id,
			Reference:      reference,
			Status:         status,
			CycleLabel:     computeCycleBadge(closingUnix, now),
			FinLabel:       computeFinancialBadge(pending, effectivePaid),
			StatusLabel:    invoiceStatusLabel(status),
			ReferenceLabel: formatReferenceLabel(reference),
			DueLabel:       formatDateLabel(dueUnix),
			ClosingLabel:   formatDateLabel(closingUnix),
			TotalDisplay:   "R$ " + formatCurrencyCentsBase(total),
			PendingDisplay: "R$ " + formatCurrencyCentsBase(pending),
			IsSensitive:    status == models.InvoiceStatusClosed || status == models.InvoiceStatusPaid,
			ManualEligible: (status == models.InvoiceStatusOpen || status == models.InvoiceStatusClosed) && effectivePaid <= total && !(total > 0 && effectivePaid >= total),
		}
		if !opt.ManualEligible {
			opt.ManualBlock = "Movimentação bloqueada: esta fatura está integralmente paga."
		}
		out = append(out, opt)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
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

	if strings.EqualFold(r.URL.Query().Get("include_options"), "true") {
		payload["options"] = queryInvoiceOptionsForAccount(h.DB, h.WorkspaceID, accountID)
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
	if err != nil || ano < 2020 || ano > appTime(time.Now()).Year()+10 {
		return 0, 0, false
	}
	return mes, ano, true
}

func buildFaturaMonthOptions(accountID string, selectedMonth time.Time, sortOrder string) []MonthOption {
	return buildFaturaMonthOptionsAt(accountID, selectedMonth, sortOrder, time.Now())
}

func buildFaturaMonthOptionsAt(accountID string, selectedMonth time.Time, sortOrder string, now time.Time) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	currentMonth := appMonthStartUTC(now)
	firstMonth := selectedMonth.AddDate(0, -5, 0)

	options := make([]MonthOption, 0, 12)
	for offset := 0; offset < 12; offset++ {
		m := firstMonth.AddDate(0, offset, 0)
		options = append(options, MonthOption{
			Label:     shortMonths[int(m.Month())-1],
			Year:      fmt.Sprintf("%d", m.Year()),
			URL:       faturaMonthURL(accountID, int(m.Month()), m.Year(), sortOrder),
			IsActive:  m.Equal(selectedMonth),
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

func faturasIndexPeriodFromRequest(r *http.Request) (int, int, error) {
	now := appTime(time.Now())
	if r == nil {
		return int(now.Month()), now.Year(), nil
	}
	monthRaw := strings.TrimSpace(r.URL.Query().Get("mes"))
	yearRaw := strings.TrimSpace(r.URL.Query().Get("ano"))
	if monthRaw == "" && yearRaw == "" {
		return int(now.Month()), now.Year(), nil
	}
	month, monthErr := strconv.Atoi(monthRaw)
	year, yearErr := strconv.Atoi(yearRaw)
	if monthErr != nil || yearErr != nil || month < 1 || month > 12 || year < 2020 || year > now.Year()+10 {
		return 0, 0, fmt.Errorf("competência inválida")
	}
	return month, year, nil
}

// Initial selection reads persisted OPEN invoices only. A CLOSED invoice has
// completed its billing cycle, while PAID is settled; neither is an open month.
// Invoice ownership is established through the credit card's workspace.
// The upper bound matches faturasIndexPeriodFromRequest's navigable range.
func initialFaturasIndexPeriod(db *sql.DB, workspaceID string, now time.Time) (int, int, error) {
	month, year := int(now.Month()), now.Year()
	var reference string
	err := db.QueryRow(`
		SELECT i.reference
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		WHERE a.workspace_id = ?
		  AND a.type = ?
		  AND a.archived_at IS NULL
		  AND i.status = ?
		  AND i.reference >= ?
		  AND i.reference <= ?
		ORDER BY i.reference ASC
		LIMIT 1
	`, workspaceID, models.AccountTypeCreditCard, models.InvoiceStatusOpen,
		fmt.Sprintf("%04d-%02d", year, month), fmt.Sprintf("%04d-12", year+10)).Scan(&reference)
	if err == sql.ErrNoRows {
		return month, year, nil
	}
	if err != nil {
		return 0, 0, err
	}
	selectedYear, selectedMonth, err := parseInvoiceReference(reference)
	if err != nil {
		return 0, 0, err
	}
	return int(selectedMonth), selectedYear, nil
}

func faturasIndexURL(accountID string, mes, ano int, sortOrder string) string {
	values := url.Values{}
	if strings.TrimSpace(accountID) != "" {
		values.Set("cartao", accountID)
	}
	values.Set("mes", strconv.Itoa(mes))
	values.Set("ano", strconv.Itoa(ano))
	if normalizeSortOrder(sortOrder, "desc") == "asc" {
		values.Set("ordem", "asc")
	}
	return "/faturas?" + values.Encode()
}

// faturasIndexURLForReference keeps every navigation entrypoint aligned with
// the invoice actually selected by the financial rules, rather than assuming
// the browser's current calendar month.
func faturasIndexURLForReference(accountID, reference, sortOrder string) (string, error) {
	year, month, err := parseInvoiceReference(reference)
	if err != nil {
		return "", err
	}
	return faturasIndexURL(accountID, int(month), year, sortOrder), nil
}

func faturasIndexContentURL(accountID string, mes, ano int, sortOrder string) string {
	values := url.Values{}
	values.Set("cartao", accountID)
	values.Set("mes", strconv.Itoa(mes))
	values.Set("ano", strconv.Itoa(ano))
	if normalizeSortOrder(sortOrder, "desc") == "asc" {
		values.Set("ordem", "asc")
	}
	return "/faturas/conteudo?" + values.Encode()
}

func faturasIndexCardsURL(accountID string, mes, ano int, sortOrder string) string {
	values := url.Values{}
	if strings.TrimSpace(accountID) != "" {
		values.Set("cartao", accountID)
	}
	values.Set("mes", strconv.Itoa(mes))
	values.Set("ano", strconv.Itoa(ano))
	values.Set("partial", "cards")
	if normalizeSortOrder(sortOrder, "desc") == "asc" {
		values.Set("ordem", "asc")
	}
	return "/faturas?" + values.Encode()
}

func buildFaturasIndexMonthOptions(accountID string, selectedMonth time.Time, sortOrder string) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	currentMonth := appMonthStartUTC(time.Now())
	options := make([]MonthOption, 0, 12)
	// A rolling window keeps the selected competence near the center. Besides
	// using the desktop width more naturally, it makes the year transition a
	// continuous month sequence instead of an abrupt Jan–Dec reset.
	firstMonth := selectedMonth.AddDate(0, -5, 0)
	for offset := 0; offset < 12; offset++ {
		m := firstMonth.AddDate(0, offset, 0)
		options = append(options, MonthOption{
			Label:     shortMonths[int(m.Month())-1],
			Year:      strconv.Itoa(m.Year()),
			URL:       faturasIndexURL(accountID, int(m.Month()), m.Year(), sortOrder),
			IsActive:  m.Month() == selectedMonth.Month(),
			IsCurrent: m.Equal(currentMonth),
		})
	}
	return options
}

func faturasIndexPeriodLabel(selectedMonth time.Time) string {
	months := []string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}
	return fmt.Sprintf("%s de %d", months[selectedMonth.Month()-1], selectedMonth.Year())
}

func configureFaturaForIndex(data *FaturaData, accountID string, mes, ano int) {
	if data == nil {
		return
	}
	selected := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)
	previous := selected.AddDate(0, -1, 0)
	next := selected.AddDate(0, 1, 0)
	data.HideMonthNavigation = true
	data.IsFaturasIndex = true
	data.MesAnteriorURL = faturasIndexURL(accountID, int(previous.Month()), previous.Year(), data.SortOrder)
	data.MesSeguinteURL = faturasIndexURL(accountID, int(next.Month()), next.Year(), data.SortOrder)
	data.MonthOptions = buildFaturasIndexMonthOptions(accountID, selected, data.SortOrder)
	data.SortRecentURL = faturasIndexURL(accountID, mes, ano, "desc")
	data.SortChronologicalURL = faturasIndexURL(accountID, mes, ano, "asc")
}

// configureFaturaForIndexRequest keeps an HTMX mutation inside the invoice hub
// instead of reintroducing the detail page's second month navigator.
func configureFaturaForIndexRequest(data *FaturaData, r *http.Request) {
	if data == nil || r == nil {
		return
	}
	currentURL := strings.TrimSpace(r.Header.Get("HX-Current-URL"))
	if currentURL == "" {
		currentURL = strings.TrimSpace(r.Referer())
	}
	u, err := url.Parse(currentURL)
	if err != nil || u.Path != "/faturas" {
		return
	}
	accountID := strings.TrimSpace(u.Query().Get("cartao"))
	if accountID == "" || accountID != data.AccountID {
		return
	}
	mes, mesErr := strconv.Atoi(u.Query().Get("mes"))
	ano, anoErr := strconv.Atoi(u.Query().Get("ano"))
	if mesErr != nil || anoErr != nil || mes < 1 || mes > 12 || ano < 2020 {
		return
	}
	configureFaturaForIndex(data, accountID, mes, ano)
}

func faturasIndexEmptyData(card CreditCardCard, mes, ano int, sortOrder string) FaturaEmptyData {
	selected := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)
	prev := selected.AddDate(0, -1, 0)
	next := selected.AddDate(0, 1, 0)
	return FaturaEmptyData{
		Title:          "Faturas",
		CardName:       card.Name,
		PeriodLabel:    fmt.Sprintf("%s/%d", []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}[selected.Month()-1], selected.Year()),
		PreviousURL:    faturasIndexURL(card.ID, int(prev.Month()), prev.Year(), sortOrder),
		NextURL:        faturasIndexURL(card.ID, int(next.Month()), next.Year(), sortOrder),
		OpenInvoiceURL: fmt.Sprintf("/cartoes/%s/faturas/abrir?mes=%d&ano=%d", url.PathEscape(card.ID), mes, ano),
		IsFaturasIndex: true,
	}
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
	return previousClosingUnix, currentClosingUnix
}

func sumInvoiceTotal(db *sql.DB, workspaceID, invoiceID string) (int64, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return 0, fmt.Errorf("workspace_id obrigatório para somatório de fatura")
	}
	var total int64
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) - COALESCE((
			SELECT SUM(ir.amount_cents) FROM invoice_refunds ir
			WHERE ir.workspace_id = ? AND ir.invoice_id = ? AND ir.reversed_at IS NULL
		), 0) FROM transactions t
		WHERE t.workspace_id = ? AND t.invoice_id = ? AND t.type = 'EXPENSE'
	`, workspaceID, invoiceID, workspaceID, invoiceID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func sumInvoiceTotalTx(tx *sql.Tx, workspaceID, invoiceID string) (int64, error) {
	var total int64
	if err := tx.QueryRow(`
		SELECT COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) - COALESCE((
			SELECT SUM(ir.amount_cents) FROM invoice_refunds ir
			WHERE ir.workspace_id = ? AND ir.invoice_id = ? AND ir.reversed_at IS NULL
		), 0) FROM transactions t
		WHERE t.workspace_id = ? AND t.invoice_id = ? AND t.type = 'EXPENSE'
	`, workspaceID, invoiceID, workspaceID, invoiceID).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func sumCardOutstandingLimit(db *sql.DB, workspaceID, cardAccountID string) (int64, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return 0, fmt.Errorf("workspace_id obrigatorio para soma de limite do cartao")
	}
	if strings.TrimSpace(cardAccountID) == "" {
		return 0, fmt.Errorf("card_account_id obrigatorio para soma de limite do cartao")
	}

	rows, err := db.Query(`
		SELECT i.id, i.closing_date,
		       COALESCE((
		           SELECT SUM(`+transactionNetAmountTSQL+`)
		           FROM transactions t
		           WHERE t.workspace_id = a.workspace_id
		             AND t.invoice_id = i.id
		             AND t.type = 'EXPENSE'
		       ), 0) - `+invoiceGeneralRefundTotalIASQL+` AS invoice_total,
		       `+invoiceEffectivePaidSQLExpr+` AS invoice_paid
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id AND a.workspace_id = ?
		WHERE i.account_id = ?
		  AND a.type = ?
	`, workspaceID, cardAccountID, models.AccountTypeCreditCard)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var outstanding int64
	for rows.Next() {
		var invoiceID string
		var closingUnix int64
		var invoiceTotal int64
		var invoicePaid int64
		if err := rows.Scan(&invoiceID, &closingUnix, &invoiceTotal, &invoicePaid); err != nil {
			return 0, err
		}
		pending := invoiceTotal - invoicePaid
		if pending <= 0 {
			continue
		}
		outstanding += pending
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return outstanding, nil
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

	data := FaturaEmptyData{
		Title:               "Fatura",
		IsBusiness:          workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness,
		ActiveWorkspaceName: queryWorkspaceName(h.DB, h.WorkspaceID),
		CardName:            cardName,
		PeriodLabel:         periodLabel,
		PreviousURL:         faturaMonthURL(accountID, int(prev.Month()), prev.Year(), faturaSortOrderFromRequest(r)),
		NextURL:             faturaMonthURL(accountID, int(next.Month()), next.Year(), faturaSortOrderFromRequest(r)),
		OpenInvoiceURL:      fmt.Sprintf("/cartoes/%s/faturas/abrir?mes=%d&ano=%d", url.PathEscape(accountID), mes, ano),
	}
	data.UserName, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.NotificationCount = queryDashboardNotificationCount(h.DB, h.UserID, h.WorkspaceID)
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, data.IsBusiness)

	templateName := "faturas-empty-page"
	if r.Header.Get("HX-Request") != "" {
		templateName = "faturas-empty-content"
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template empty faturas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *FaturasHandler) renderDashboardOOB(w http.ResponseWriter) error {
	data := BuildDashboardData(h.DB, h.UserID, h.WorkspaceID)
	data.OOB = true
	SortDashboardCardsByPaymentPriority(data.Cards, time.Now())

	var buf bytes.Buffer
	for _, templateName := range dashboardOOBTemplateNames(h.Templates) {
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
	configureFaturaForIndexRequest(&invoiceData, r)

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-content", invoiceData); err != nil {
		return err
	}

	dashboardData := BuildDashboardData(h.DB, h.UserID, h.WorkspaceID)
	dashboardData.OOB = true
	SortDashboardCardsByPaymentPriority(dashboardData.Cards, time.Now())
	for _, templateName := range dashboardOOBTemplateNames(h.Templates) {
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

func queryActiveInvoiceGeneralRefunds(db *sql.DB, workspaceID, invoiceID string) ([]InvoiceGeneralRefundRow, error) {
	rows, err := db.Query(`
		SELECT id, amount_cents, description, created_at
		FROM invoice_refunds
		WHERE workspace_id = ?
		  AND invoice_id = ?
		  AND reversed_at IS NULL
		ORDER BY created_at DESC, id DESC
	`, workspaceID, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []InvoiceGeneralRefundRow
	for rows.Next() {
		var id, description string
		var amount, createdAt int64
		if err := rows.Scan(&id, &amount, &description, &createdAt); err != nil {
			return nil, err
		}
		result = append(result, InvoiceGeneralRefundRow{
			ID:          id,
			DateLabel:   formatDateTimeLabel(createdAt),
			Amount:      MoneyMinor(amount),
			Description: description,
		})
	}
	return result, rows.Err()
}

func attachActiveTransactionRefundRows(db *sql.DB, workspaceID string, transactions []TransactionRow) error {
	ids := make([]string, 0, len(transactions))
	byID := make(map[string]*TransactionRow, len(transactions))
	for i := range transactions {
		if transactions[i].ID == "" || transactions[i].IsProjected {
			continue
		}
		ids = append(ids, transactions[i].ID)
		byID[transactions[i].ID] = &transactions[i]
	}
	if len(ids) == 0 {
		return nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, workspaceID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := db.Query(`
		SELECT id, transaction_id, amount_cents, description
		FROM transaction_refunds
		WHERE workspace_id = ?
		  AND reversed_at IS NULL
		  AND transaction_id IN (`+placeholders+`)
		ORDER BY created_at DESC, id DESC
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var refundID, transactionID, description string
		var amount int64
		if err := rows.Scan(&refundID, &transactionID, &amount, &description); err != nil {
			return err
		}
		if transaction := byID[transactionID]; transaction != nil {
			transaction.RefundEntries = append(transaction.RefundEntries, TransactionRefundRow{
				ID:          refundID,
				Amount:      MoneyMinor(amount),
				Description: description,
			})
		}
	}
	return rows.Err()
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
