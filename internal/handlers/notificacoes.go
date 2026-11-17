package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

type NotificacoesHandler struct {
	reader      reportQueryer
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string

	// Presentation permissions are resolved from the authenticated member in
	// cmd/server. Destinations still enforce their own server-side guards.
	CanReadTransactions    bool
	CanViewDashboard       bool
	CanUpdatePaymentStatus bool
}

const (
	notificationTargetTransaction = "transaction"
	notificationTargetInvoice     = "invoice"
	notificationTargetLimit       = "limit"
)

func (h *NotificacoesHandler) notificationReader() reportQueryer {
	if h.reader != nil {
		return h.reader
	}
	return h.DB
}

type NotificacoesData struct {
	AppShellAccess
	Title               string
	UserName            string
	UserInitials        string
	ProfilePhotoURL     string
	NotificationCount   int
	ActiveWorkspaceName string
	IsBusiness          bool
	Items               []NotificationItem

	CanReadTransactions    bool
	CanViewDashboard       bool
	CanUpdatePaymentStatus bool
}

type NotificationItem struct {
	Key         string
	Title       string
	Description string
	Money       MoneyDisplay
	HasMoney    bool
	Icon        string
	Color       string
	CreatedAt   int64

	// TargetType/TargetID describe the record behind a computed notification.
	// They are presentation data only: the destination route re-validates the
	// workspace and the record before rendering anything.
	TargetType string
	TargetID   string
	CanOpen    bool

	// Payment action data mirrors the existing Lancamentos row contract. The
	// notification only opens the shared confirmation modal; the POST remains
	// on the existing status endpoint.
	CanPay             bool
	PaymentType        string
	PaymentAccountID   string
	PaymentAccountName string
	PaymentAmount      string
}

func (h *NotificacoesHandler) HandleExibirNotificacoes(w http.ResponseWriter, r *http.Request) {
	if err := autoCloseWorkspaceInvoices(h.DB, h.WorkspaceID); err != nil {
		log.Printf("auto close notification invoices error: %v", err)
	}

	data := NotificacoesData{
		Title:                  "Notificações",
		ProfilePhotoURL:        queryUserProfilePhotoURL(h.DB, h.UserID),
		ActiveWorkspaceName:    queryWorkspaceName(h.DB, h.WorkspaceID),
		CanReadTransactions:    h.CanReadTransactions,
		CanViewDashboard:       h.CanViewDashboard,
		CanUpdatePaymentStatus: h.CanUpdatePaymentStatus,
	}
	data.UserName, data.UserInitials = queryDashboardUser(h.DB, h.UserID)
	data.Items = append(data.Items, h.pendingOverdueExpenses()...)
	data.Items = append(data.Items, h.closedInvoices()...)
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness
	data.IsBusiness = isBusiness
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, isBusiness)
	data.Items = append(data.Items, h.exceededCostLimits(isBusiness)...)
	data.Items = append(data.Items, h.scheduledPaymentNotifications()...)
	data.Items = h.visibleItemsForUser(data.Items)
	data.NotificationCount = len(data.Items)

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "notificacoes-page", data); err != nil {
		log.Printf("template notificacoes error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	// OOB-only update: a full document load already renders the sink inside the
	// app shell. Appending a second #notification-badge here duplicated the id in
	// the DOM; HTMX responses still receive the OOB span.
	if r.Header.Get("HX-Request") == "true" {
		buf.WriteString(NotificationBadgeHTML(h.DB, h.UserID, h.WorkspaceID))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *NotificacoesHandler) HandleLimparTudo(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	if _, err := h.DB.Exec(`
		UPDATE users SET last_notifications_clear_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, h.UserID); err != nil {
		log.Printf("clear notifications error: %v", err)
		http.Error(w, "erro ao limpar notificações", http.StatusInternalServerError)
		return
	}
	h.HandleExibirNotificacoes(w, r)
}

func (h *NotificacoesHandler) HandleApagarNotificacao(w http.ResponseWriter, r *http.Request, key string) {
	if strings.TrimSpace(key) == "" {
		http.Error(w, "notificação inválida", http.StatusBadRequest)
		return
	}
	now := time.Now().Unix()
	_, err := h.DB.Exec(`
		INSERT INTO user_notification_dismissals (user_id, workspace_id, notification_key, dismissed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, workspace_id, notification_key)
		DO UPDATE SET dismissed_at = excluded.dismissed_at
	`, h.UserID, h.WorkspaceID, key, now)
	if err != nil {
		log.Printf("dismiss notification error: %v", err)
		http.Error(w, "erro ao apagar notificação", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, NotificationBadgeHTML(h.DB, h.UserID, h.WorkspaceID))
}

func (h *NotificacoesHandler) visibleItemsForUser(items []NotificationItem) []NotificationItem {
	clearAt := h.lastClearAt()
	dismissed := h.dismissedKeys()
	visible := make([]NotificationItem, 0, len(items))
	for _, item := range items {
		if item.CreatedAt > 0 && item.CreatedAt <= clearAt {
			continue
		}
		if dismissed[item.Key] {
			continue
		}
		visible = append(visible, item)
	}
	return visible
}

func (h *NotificacoesHandler) lastClearAt() int64 {
	var clearAt int64
	h.notificationReader().QueryRow(`SELECT COALESCE(last_notifications_clear_at, 0) FROM users WHERE id = ?`, h.UserID).Scan(&clearAt)
	return clearAt
}

func (h *NotificacoesHandler) dismissedKeys() map[string]bool {
	rows, err := h.notificationReader().Query(`
		SELECT notification_key
		FROM user_notification_dismissals
		WHERE user_id = ? AND workspace_id = ?
	`, h.UserID, h.WorkspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	keys := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err == nil {
			keys[key] = true
		}
	}
	return keys
}

func (h *NotificacoesHandler) pendingOverdueExpenses() []NotificationItem {
	loc := appTimeLocation()
	nowTZ := time.Now().In(loc)
	today := time.Date(nowTZ.Year(), nowTZ.Month(), nowTZ.Day(), 0, 0, 0, 0, time.UTC).Unix()
	rows, err := h.notificationReader().Query(`
		SELECT t.id, t.description, t.amount, t.date, t.updated_at,
			t.account_id, COALESCE(a.name, ''), COALESCE(a.type, '')
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.type = ?
		  AND t.status = 'pending'
		  AND t.date < ?
		  AND t.invoice_id IS NULL
		  AND t.scheduled_payment_at IS NULL
		ORDER BY t.date ASC, t.created_at ASC
		LIMIT 8
	`, h.WorkspaceID, models.TransactionTypeExpense, today)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var description, accountID, accountName, accountType string
		var id string
		var amount, dateUnix, updatedAt int64
		if err := rows.Scan(&id, &description, &amount, &dateUnix, &updatedAt,
			&accountID, &accountName, &accountType); err != nil {
			continue
		}
		amountDisplay := MoneyMinor(amount)
		items = append(items, NotificationItem{
			Key:         "transaction:" + id,
			Title:       "Despesa vencida",
			Description: fmt.Sprintf("%s · vencida em %s", description, formatDateLabel(dateUnix)),
			Money:       amountDisplay,
			HasMoney:    true,
			Icon:        "clock-alert",
			Color:       "rose",
			CreatedAt:   updatedAt,
			TargetType:  notificationTargetTransaction,
			TargetID:    id,
			CanOpen:     h.CanReadTransactions,
			// Scheduling rejects credit-card purchases. Keep the contextual
			// action only for transactions the existing flow can handle.
			CanPay:             h.CanUpdatePaymentStatus && accountType != models.AccountTypeCreditCard,
			PaymentType:        models.TransactionTypeExpense,
			PaymentAccountID:   accountID,
			PaymentAccountName: accountName,
			PaymentAmount:      formatCurrencyLabel(amount),
		})
	}
	return items
}

func (h *NotificacoesHandler) closedInvoices() []NotificationItem {
	rows, err := h.notificationReader().Query(`
		SELECT i.id, a.name, i.reference, i.closing_date, i.due_date,
			COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) - `+invoiceGeneralRefundTotalIASQL+` AS total
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		LEFT JOIN transactions t ON t.invoice_id = i.id
			AND t.workspace_id = a.workspace_id
			AND t.type = ?
			AND t.status = 'paid'
		WHERE a.workspace_id = ?
		  AND a.type = ?
		  AND i.status = ?
		GROUP BY i.id, a.name, i.reference, i.closing_date, i.due_date
		ORDER BY i.due_date ASC
		LIMIT 8
	`, models.TransactionTypeExpense, h.WorkspaceID, models.AccountTypeCreditCard, models.InvoiceStatusClosed)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var invoiceID, cardName, reference string
		var closingUnix, dueUnix, total int64
		if err := rows.Scan(&invoiceID, &cardName, &reference, &closingUnix, &dueUnix, &total); err != nil {
			continue
		}
		items = append(items, NotificationItem{
			Key:         "invoice:" + invoiceID,
			Title:       "Fatura aguardando pagamento",
			Description: fmt.Sprintf("%s · ref. %s · vence em %s", cardName, reference, formatDateLabel(dueUnix)),
			Money:       MoneyMinor(total),
			HasMoney:    true,
			Icon:        "triangle-alert",
			Color:       "amber",
			// The notification is created when the invoice closes, not when it
			// becomes due. Due dates are often in the future; using them here
			// made "Limpar tudo" leave those notifications visible.
			CreatedAt:  closingUnix,
			TargetType: notificationTargetInvoice,
			TargetID:   invoiceID,
			CanOpen:    h.CanReadTransactions,
		})
	}
	return items
}

func (h *NotificacoesHandler) exceededCostLimits(isBusiness bool) []NotificationItem {
	_ = isBusiness
	monthStartDate := appMonthStartUTC(time.Now())
	monthStart := monthStartDate.Unix()
	nextMonth := monthStartDate.AddDate(0, 1, 0).Unix()
	rows, err := h.notificationReader().Query(`
		SELECT cl.id, c.name, cl.max_amount_monthly, COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) AS spent, COALESCE(MAX(t.updated_at), 0) AS latest_tx
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id AND c.workspace_id = cl.workspace_id
		LEFT JOIN transactions t ON t.category_id = cl.category_id
			AND t.workspace_id = cl.workspace_id
				AND t.type = ?
				AND t.status = 'paid'
				AND t.date >= ? AND t.date < ?
				AND `+excludeInvoicePaymentCompetenceClause("t")+`
			WHERE cl.workspace_id = ?
		GROUP BY cl.id, c.name, cl.max_amount_monthly
		HAVING spent > cl.max_amount_monthly
		ORDER BY spent - cl.max_amount_monthly DESC
		LIMIT 8
	`, models.TransactionTypeExpense, monthStart, nextMonth, h.WorkspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var category string
		var limitID string
		var maxAmount, spent, latestTx int64
		if err := rows.Scan(&limitID, &category, &maxAmount, &spent, &latestTx); err != nil {
			continue
		}
		title := "Limite de categoria estourado"
		items = append(items, NotificationItem{
			Key:         "limit:" + limitID,
			Title:       title,
			Description: fmt.Sprintf("%s · limite %s", category, formatCurrencyLabel(maxAmount)),
			Money:       MoneyMinor(spent - maxAmount),
			HasMoney:    true,
			Icon:        "gauge",
			Color:       "red",
			CreatedAt:   latestTx,
			TargetType:  notificationTargetLimit,
			TargetID:    limitID,
			CanOpen:     h.CanViewDashboard,
		})
	}
	return items
}

func (h *NotificacoesHandler) scheduledPaymentNotifications() []NotificationItem {
	rows, err := h.notificationReader().Query(`
		SELECT id, title, message, type, created_at
		FROM user_notifications
		WHERE user_id = ?
		  AND workspace_id = ?
		  AND type = ?
		ORDER BY created_at DESC
		LIMIT 20
	`, h.UserID, h.WorkspaceID, scheduledPaymentNotificationType)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var id, title, message, notifType string
		var createdAt int64
		if err := rows.Scan(&id, &title, &message, &notifType, &createdAt); err != nil {
			continue
		}
		icon := "badge-check"
		color := "emerald"
		if strings.Contains(strings.ToLower(title), "recebimento") {
			icon = "circle-dollar-sign"
		}
		items = append(items, NotificationItem{
			Key:         "scheduled_payment_notif:" + id,
			Title:       title,
			Description: message,
			Icon:        icon,
			Color:       color,
			CreatedAt:   createdAt,
			// These rows are execution receipts. user_notifications has no
			// typed transaction reference, so the card stays informational and
			// never renders a zero-value amount.
			HasMoney: false,
		})
	}
	return items
}

func NotificationBadgeHTML(db *sql.DB, userID, workspaceID string) string {
	count := queryDashboardNotificationCount(db, userID, workspaceID)
	if count > 0 {
		label := "notificação não lida"
		if count > 1 {
			label = "notificações não lidas"
		}
		return fmt.Sprintf(`<span id="notification-badge" hx-swap-oob="true" aria-label="%d %s"></span>`, count, label)
	}
	return `<span id="notification-badge" hx-swap-oob="true" class="hidden"></span>`
}
