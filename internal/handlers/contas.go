package handlers

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
)

type ContasHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

type ContasData struct {
	OOB                       bool
	Title                     string
	UserInitials              string
	ProfilePhotoURL           string
	MesAtual                  int
	AnoAtual                  int
	Aba                       string
	PeriodoRef                string
	MonthSelectorHXGet        string
	MonthSelectorHXSelect     string
	MonthSelectorHXTarget     string
	MonthSelectorHXSwap       string
	MonthSelectorPartial      string
	MonthSelectorPrevQuery    string
	MonthSelectorNextQuery    string
	MonthSelectorCurrentQuery string
	MesAnteriorURL            string
	MesSeguinteURL            string
	CurrentMonthURL           string
	MonthOptions              []MonthOption
	Busca                     string
	HasFilters                bool
	Payables                  []PendingItemRow
	Receivables               []PendingItemRow
	TotalPayables             MoneyDisplay
	TotalReceivables          MoneyDisplay
	TotalOverdueReceivabs     MoneyDisplay
	IsBusiness                bool
	ActiveWorkspaceName       string
}

type PendingItemRow struct {
	ID          string
	Description string
	Amount      int64
	AmountMoney MoneyDisplay
	DueDateUnix int64
	DueDate     string
	AccountName string
	ContactName string
	IsOverdue   bool
}

func (h *ContasHandler) HandleContasConceito(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	now := time.Now()
	mes := int(now.Month())
	ano := now.Year()
	periodo := strings.TrimSpace(r.URL.Query().Get("periodo"))
	if periodo != "" {
		parts := strings.Split(periodo, "-")
		if len(parts) == 2 {
			if parsedAno, err := strconv.Atoi(parts[0]); err == nil && parsedAno >= 2020 && parsedAno <= now.Year()+10 {
				ano = parsedAno
			}
			if parsedMes, err := strconv.Atoi(parts[1]); err == nil && parsedMes >= 1 && parsedMes <= 12 {
				mes = parsedMes
			}
		}
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	aba := strings.TrimSpace(r.URL.Query().Get("aba"))
	if aba == "" {
		aba = "pagar"
	}
	partial := r.URL.Query().Get("partial")

	var data ContasData
	var templateName string
	switch partial {
	case "lista":
		templateName = "contas-list"
		var payables, receivables []PendingItemRow
		var err error
		switch aba {
		case "receber":
			tQ := time.Now()
			receivables, err = queryPendingItemsFiltered(h.DB, h.WorkspaceID, "INCOME", false, mes, ano, q)
			perfStep(reqID, "Contas", "queryReceivables", time.Since(tQ))
		default:
			tQ := time.Now()
			payables, err = queryPendingItemsFiltered(h.DB, h.WorkspaceID, "EXPENSE", false, mes, ano, q)
			perfStep(reqID, "Contas", "queryPayables", time.Since(tQ))
		}
		if err != nil {
			log.Printf("query contas partial error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		data = ContasData{
			Aba:         aba,
			Payables:    payables,
			Receivables: receivables,
		}
	default:
		selectedMonth := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, time.UTC)

		tQ1 := time.Now()
		payables, err := queryPendingItemsFiltered(h.DB, h.WorkspaceID, "EXPENSE", false, mes, ano, q)
		perfStep(reqID, "Contas", "queryPayables", time.Since(tQ1))
		if err != nil {
			log.Printf("query payables error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		tQ2 := time.Now()
		receivables, err := queryPendingItemsFiltered(h.DB, h.WorkspaceID, "INCOME", false, mes, ano, q)
		perfStep(reqID, "Contas", "queryReceivables", time.Since(tQ2))
		if err != nil {
			log.Printf("query receivables error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		tQ3 := time.Now()
		overdueReceivables, err := queryPendingItemsFiltered(h.DB, h.WorkspaceID, "INCOME", true, mes, ano, q)
		perfStep(reqID, "Contas", "queryOverdue", time.Since(tQ3))
		if err != nil {
			log.Printf("query overdue receivables error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		data = ContasData{
			Aba:                       aba,
			MesAtual:                  mes,
			AnoAtual:                  ano,
			PeriodoRef:                fmt.Sprintf("%04d-%02d", ano, mes),
			MonthSelectorHXGet:        "/contas",
			MonthSelectorHXTarget:     "#contas-body",
			MonthSelectorHXSwap:       "outerHTML",
			MonthSelectorPartial:      "content",
			MonthSelectorPrevQuery:    contasMonthQuery(int(selectedMonth.AddDate(0, -1, 0).Month()), selectedMonth.AddDate(0, -1, 0).Year(), q),
			MonthSelectorNextQuery:    contasMonthQuery(int(selectedMonth.AddDate(0, 1, 0).Month()), selectedMonth.AddDate(0, 1, 0).Year(), q),
			MonthSelectorCurrentQuery: contasMonthQuery(int(now.Month()), now.Year(), q),
			MesAnteriorURL:            "/contas?" + contasMonthQuery(int(selectedMonth.AddDate(0, -1, 0).Month()), selectedMonth.AddDate(0, -1, 0).Year(), q),
			MesSeguinteURL:            "/contas?" + contasMonthQuery(int(selectedMonth.AddDate(0, 1, 0).Month()), selectedMonth.AddDate(0, 1, 0).Year(), q),
			CurrentMonthURL:           "/contas?" + contasMonthQuery(int(now.Month()), now.Year(), q),
			MonthOptions:              buildContasMonthOptions(selectedMonth, q),
			Busca:                     q,
			HasFilters:                q != "",
			Payables:                  payables,
			Receivables:               receivables,
			TotalPayables:             MoneyMinor(sumPendingAmount(payables)),
			TotalReceivables:          MoneyMinor(sumPendingAmount(receivables)),
			TotalOverdueReceivabs:     MoneyMinor(sumPendingAmount(overdueReceivables)),
		}
		if partial == "content" {
			templateName = "contas-body"
		} else {
			templateName = "contas-page"
			data.Title = "Contas"
			data.UserInitials = queryUserInitialsByID(h.DB, h.UserID)
			data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
			data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness
			data.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
		}
	}
	perfStep(reqID, "Contas", "buildData", time.Since(t0))

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template contas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "Contas", "templateRender", time.Since(tR))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "Contas", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if partial == "content" {
		cleanQ := url.Values{}
		cleanQ.Set("periodo", fmt.Sprintf("%04d-%02d", ano, mes))
		if q != "" {
			cleanQ.Set("q", q)
		}
		w.Header().Set("HX-Replace-Url", "/contas?"+cleanQ.Encode())
	}
	if partial == "lista" {
		cleanQ := url.Values{}
		cleanQ.Set("periodo", fmt.Sprintf("%04d-%02d", ano, mes))
		cleanQ.Set("aba", aba)
		w.Header().Set("HX-Replace-Url", "/contas?"+cleanQ.Encode())
	}
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

func queryPendingItems(db *sql.DB, workspaceID, itemType string, onlyOverdue bool) ([]PendingItemRow, error) {
	return queryPendingItemsFiltered(db, workspaceID, itemType, onlyOverdue, 0, 0, "")
}

func queryPendingItemsFiltered(db *sql.DB, workspaceID, itemType string, onlyOverdue bool, mes, ano int, search string) ([]PendingItemRow, error) {
	sqlQuery := `
		SELECT t.id,
		       COALESCE(t.description, ''),
		       t.amount,
		       COALESCE(t.due_date, t.date),
		       COALESCE(a.name, ''),
		       COALESCE(c.name, '')
		FROM transactions t
		JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
		WHERE t.workspace_id = ?
		  AND t.status = 'pending'
		  AND t.type = ?
	`
	args := []interface{}{workspaceID, itemType}
	if mes > 0 && ano > 0 {
		sqlQuery += `
		  AND CAST(strftime('%m', COALESCE(t.due_date, t.date), 'unixepoch') AS INTEGER) = ?
		  AND CAST(strftime('%Y', COALESCE(t.due_date, t.date), 'unixepoch') AS INTEGER) = ?
		`
		args = append(args, mes, ano)
	}
	if search != "" {
		like := "%" + search + "%"
		sqlQuery += `
		  AND (
			  UNACCENT(COALESCE(t.description, '')) LIKE UNACCENT(?)
			  OR UNACCENT(COALESCE(c.name, '')) LIKE UNACCENT(?)
			  OR UNACCENT(COALESCE(a.name, '')) LIKE UNACCENT(?)
		  )
		`
		args = append(args, like, like, like)
	}
	if onlyOverdue {
		sqlQuery += ` AND t.due_date IS NOT NULL AND t.due_date < ?`
		args = append(args, businessDayStartUnix(time.Now()))
	}
	sqlQuery += ` ORDER BY COALESCE(t.due_date, t.date) ASC, t.created_at ASC`
	rows, err := db.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var out []PendingItemRow
	for rows.Next() {
		var row PendingItemRow
		if err := rows.Scan(&row.ID, &row.Description, &row.Amount, &row.DueDateUnix, &row.AccountName, &row.ContactName); err != nil {
			return nil, err
		}
		row.AmountMoney = MoneyMinor(row.Amount)
		row.DueDate = formatDateLabelFromUnix(row.DueDateUnix)
		due := time.Unix(row.DueDateUnix, 0).UTC()
		dueDay := time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, time.UTC)
		row.IsOverdue = dueDay.Before(today)
		out = append(out, row)
	}
	return out, rows.Err()
}

func sumPendingAmount(items []PendingItemRow) int64 {
	var total int64
	for _, item := range items {
		total += item.Amount
	}
	return total
}

func formatDateLabelFromUnix(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	t := time.Unix(unix, 0).UTC()
	return fmt.Sprintf("%02d/%02d/%04d", t.Day(), t.Month(), t.Year())
}

func buildContasMonthOptions(selectedMonth time.Time, search string) []MonthOption {
	shortMonths := []string{"Jan", "Fev", "Mar", "Abr", "Mai", "Jun", "Jul", "Ago", "Set", "Out", "Nov", "Dez"}
	now := time.Now().UTC()
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	options := make([]MonthOption, 0, 12)
	for month := 1; month <= 12; month++ {
		m := time.Date(selectedMonth.Year(), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		query := contasMonthQuery(int(m.Month()), m.Year(), search)
		options = append(options, MonthOption{
			Label:     shortMonths[int(m.Month())-1],
			Year:      fmt.Sprintf("%d", m.Year()),
			URL:       "/contas?" + query,
			Query:     query,
			IsActive:  m.Month() == selectedMonth.Month(),
			IsCurrent: m.Equal(currentMonth),
		})
	}
	return options
}

func contasURL(mes, ano int, search string) string {
	return "/contas?" + contasMonthQuery(mes, ano, search)
}

func contasMonthQuery(mes, ano int, search string) string {
	values := url.Values{}
	values.Set("periodo", fmt.Sprintf("%04d-%02d", ano, mes))
	if strings.TrimSpace(search) != "" {
		values.Set("q", strings.TrimSpace(search))
	}
	return values.Encode()
}
