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

	"github.com/google/uuid"
)

type NotificacoesHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

type NotificacoesData struct {
	Title           string
	UserInitials    string
	ProfilePhotoURL string
	Items           []NotificationItem
}

type NotificationItem struct {
	Key         string
	Title       string
	Description string
	Money       MoneyDisplay
	Icon        string
	Color       string
	CreatedAt   int64
}

func (h *NotificacoesHandler) HandleExibirNotificacoes(w http.ResponseWriter, r *http.Request) {
	if err := autoCloseWorkspaceInvoices(h.DB, h.WorkspaceID); err != nil {
		log.Printf("auto close notification invoices error: %v", err)
	}

	data := NotificacoesData{
		Title:           "Notificações",
		UserInitials:    queryUserInitialsByID(h.DB, h.UserID),
		ProfilePhotoURL: queryUserProfilePhotoURL(h.DB, h.UserID),
	}
	data.Items = append(data.Items, h.pendingOverdueExpenses()...)
	data.Items = append(data.Items, h.closedInvoices()...)
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness
	data.Items = append(data.Items, h.exceededCostLimits(isBusiness)...)
	data.Items = append(data.Items, h.boxesInOverdraft(isBusiness)...)
	data.Items = append(data.Items, h.completedBoxGoals(isBusiness)...)
	data.Items = append(data.Items, h.caixinhaPersistedNotifications()...)
	data.Items = h.visibleItemsForUser(data.Items)

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "notificacoes-page", data); err != nil {
		log.Printf("template notificacoes error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	buf.WriteString(NotificationBadgeHTML(h.DB, h.UserID, h.WorkspaceID))

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
	h.HandleExibirNotificacoes(w, r)
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
	h.DB.QueryRow(`SELECT COALESCE(last_notifications_clear_at, 0) FROM users WHERE id = ?`, h.UserID).Scan(&clearAt)
	return clearAt
}

func (h *NotificacoesHandler) dismissedKeys() map[string]bool {
	rows, err := h.DB.Query(`
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
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	rows, err := h.DB.Query(`
		SELECT id, description, amount, date, updated_at
		FROM transactions
		WHERE workspace_id = ?
		  AND type = ?
		  AND status = 'pending'
		  AND date < ?
		  AND invoice_id IS NULL
		ORDER BY date ASC, created_at ASC
		LIMIT 8
	`, h.WorkspaceID, models.TransactionTypeExpense, today)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var description string
		var id string
		var amount, dateUnix, updatedAt int64
		if err := rows.Scan(&id, &description, &amount, &dateUnix, &updatedAt); err != nil {
			continue
		}
		items = append(items, NotificationItem{
			Key:         "transaction:" + id,
			Title:       "Despesa vencida",
			Description: fmt.Sprintf("%s · vencida em %s", description, formatDateLabel(dateUnix)),
			Money:       MoneyMinor(amount),
			Icon:        "clock-alert",
			Color:       "rose",
			CreatedAt:   updatedAt,
		})
	}
	return items
}

func (h *NotificacoesHandler) closedInvoices() []NotificationItem {
	rows, err := h.DB.Query(`
		SELECT i.id, a.name, i.reference, i.due_date, COALESCE(SUM(t.amount), 0) AS total
		FROM invoices i
		JOIN accounts a ON a.id = i.account_id
		LEFT JOIN transactions t ON t.invoice_id = i.id
			AND t.workspace_id = a.workspace_id
			AND t.type = ?
			AND t.status = 'paid'
		WHERE a.workspace_id = ?
		  AND a.type = ?
		  AND i.status = ?
		GROUP BY i.id, a.name, i.reference, i.due_date
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
		var dueUnix, total int64
		if err := rows.Scan(&invoiceID, &cardName, &reference, &dueUnix, &total); err != nil {
			continue
		}
		items = append(items, NotificationItem{
			Key:         "invoice:" + invoiceID,
			Title:       "Fatura aguardando pagamento",
			Description: fmt.Sprintf("%s · ref. %s · vence em %s", cardName, reference, formatDateLabel(dueUnix)),
			Money:       MoneyMinor(total),
			Icon:        "triangle-alert",
			Color:       "amber",
			CreatedAt:   dueUnix,
		})
	}
	return items
}

func (h *NotificacoesHandler) exceededCostLimits(isBusiness bool) []NotificationItem {
	_ = isBusiness
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()
	rows, err := h.DB.Query(`
		SELECT cl.id, c.name, cl.max_amount_monthly, COALESCE(SUM(t.amount), 0) AS spent, COALESCE(MAX(t.updated_at), 0) AS latest_tx
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id
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
			Icon:        "gauge",
			Color:       "red",
			CreatedAt:   latestTx,
		})
	}
	return items
}

func (h *NotificacoesHandler) boxesInOverdraft(isBusiness bool) []NotificationItem {
	_ = isBusiness
	rows, err := h.DB.Query(`
		SELECT b.id, b.name, b.category_id, b.target_amount,
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS balance,
			COALESCE(MAX(l.created_at), unixepoch()) AS latest_ledger
		FROM boxes b
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE b.workspace_id = ?
		GROUP BY b.id
		HAVING balance < 0
		ORDER BY balance ASC
		LIMIT 8
	`, h.WorkspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var boxID, name, categoryID string
		var target, balance, latestLedger int64
		if err := rows.Scan(&boxID, &name, &categoryID, &target, &balance, &latestLedger); err != nil {
			continue
		}
		title := "Reserva em excedente"
		desc := fmt.Sprintf("%s · voce usou mais do que havia reservado nesta reserva", name)
		items = append(items, NotificationItem{
			Key:         "box_overdraft:" + boxID,
			Title:       title,
			Description: desc,
			Money:       MoneyMinor(-balance),
			Icon:        "alert-triangle",
			Color:       "rose",
			CreatedAt:   latestLedger,
		})
	}
	return items
}

func (h *NotificacoesHandler) completedBoxGoals(isBusiness bool) []NotificationItem {
	_ = isBusiness
	rows, err := h.DB.Query(`
		SELECT b.id, b.name, b.category_id, b.target_amount,
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS balance,
			COALESCE(MAX(l.created_at), unixepoch()) AS latest_ledger
		FROM boxes b
		LEFT JOIN box_virtual_ledger l ON l.box_id = b.id
		WHERE b.workspace_id = ?
		  AND b.target_amount > 0
		GROUP BY b.id
		HAVING balance >= b.target_amount
		ORDER BY balance DESC
		LIMIT 8
	`, h.WorkspaceID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []NotificationItem
	for rows.Next() {
		var boxID, name, categoryID string
		var target, balance, latestLedger int64
		if err := rows.Scan(&boxID, &name, &categoryID, &target, &balance, &latestLedger); err != nil {
			continue
		}
		title := "Meta de reserva atingida"
		items = append(items, NotificationItem{
			Key:         "box_goal:" + boxID,
			Title:       title,
			Description: fmt.Sprintf("%s · alcancou a meta de %s", name, formatCurrencyLabel(target)),
			Money:       MoneyMinor(balance),
			Icon:        "trophy",
			Color:       "emerald",
			CreatedAt:   latestLedger,
		})
	}
	return items
}

func (h *NotificacoesHandler) caixinhaPersistedNotifications() []NotificationItem {
	rows, err := h.DB.Query(`
		SELECT id, title, message, type, created_at
		FROM user_notifications
		WHERE user_id = ?
		  AND workspace_id = ?
		  AND type LIKE 'caixinha.%'
		ORDER BY created_at DESC
		LIMIT 20
	`, h.UserID, h.WorkspaceID)
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
		icon := "piggy-bank"
		color := "violet"
		if notifType == "caixinha.resgate" {
			icon = "arrow-up-from-line"
			color = "amber"
		}
		items = append(items, NotificationItem{
			Key:         "caixinha_notif:" + id,
			Title:       title,
			Description: message,
			Icon:        icon,
			Color:       color,
			CreatedAt:   createdAt,
		})
	}
	return items
}

func insertCaixinhaNotification(db *sql.DB, userID, workspaceID, title, message, notificationType string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	if _, err := db.Exec(`
		INSERT INTO user_notifications (id, user_id, workspace_id, title, message, type, is_read, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)
	`, uuid.NewString(), userID, strings.TrimSpace(workspaceID), strings.TrimSpace(title), strings.TrimSpace(message), strings.TrimSpace(notificationType), time.Now().Unix()); err != nil {
		log.Printf("insert caixinha notification failed: user=%s ws=%s type=%s err=%v", userID, workspaceID, notificationType, err)
	}
}

func NotificationBadgeHTML(db *sql.DB, userID, workspaceID string) string {
	count := queryDashboardNotificationCount(db, userID, workspaceID)
	if count > 0 {
		return `<span id="notification-badge" hx-swap-oob="true" class="absolute top-2 right-2 flex h-2.5 w-2.5 rounded-full bg-rose-500 ring-2 ring-white dark:ring-zinc-950"></span>`
	}
	return `<span id="notification-badge" hx-swap-oob="true" class="hidden"></span>`
}
