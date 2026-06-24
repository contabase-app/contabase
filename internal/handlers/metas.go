package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
	"github.com/contabase-app/contabase/internal/services"

	"github.com/google/uuid"
)

var errDuplicateLimit = errors.New("limite duplicado")

type MetasHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

func respondMetaFormError(w http.ResponseWriter, idPrefix string, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<div id="%s-form-error" hx-swap-oob="true" class="rounded-xl border border-rose-500/35 bg-rose-500/10 px-3 py-2 text-sm text-rose-100">%s</div>`, idPrefix, template.HTMLEscapeString(message))
}

type MetasData struct {
	Title                  string
	UserName               string
	UserFirstName          string
	UserInitials           string
	ProfilePhotoURL        string
	MonthLabel             string
	Aba                    string
	ContentOOB             bool
	FabOOB                 bool
	TotalLimits            MoneyDisplay
	TotalAcumulado         MoneyDisplay
	TotalAcumuladoNegativo bool
	RealBalance            MoneyDisplay
	ReservedBalance        MoneyDisplay
	FreeBalance            MoneyDisplay
	FreeBalanceNegative    bool
	LimitesCount           int
	CaixinhasCount         int
	Limites                []LimiteCard
	Caixinhas              []CaixinhaCard
	IsBusiness             bool
	ActiveWorkspaceName    string
}

type MetaFormData struct {
	Mode                  string
	OpenTab               string
	BoxID                 string
	LimitID               string
	Name                  string
	TargetAmountInput     string
	MonthlyRechargeInput  string
	MonthsDesiredInput    string
	TargetMonthInput      string
	MonthlyYieldRateInput string
	CategoryID            string
	CategoryName          string
	CategoryIcon          string
	CategoryColor         string
	MaxAmountInput        string
	BalanceLabel          string
	Categories            []FormCategory
	IsBusiness            bool
}

type LimiteCard struct {
	ID            string
	CategoryID    string
	CategoryName  string
	CategoryIcon  string
	CategoryColor string
	MaxAmount     MoneyDisplay
	Spent         MoneyDisplay
	Remaining     MoneyDisplay
	Percent       int
	PercentColor  string
}

type CaixinhaCard struct {
	ID                 string
	CategoryID         string
	CategoryName       string
	Name               string
	Icon               string
	Color              string
	Target             MoneyDisplay
	Balance            MoneyDisplay
	Percent            int
	PercentColor       string
	MonthlyRecharge    MoneyDisplay
	MonthsLeft         int
	ForecastLabel      string
	TargetMonth        string
	RequiredMonthly    MoneyDisplay
	RequiredState      string
	Expenses           MoneyDisplay
	IsNegative         bool
	YieldForecastLabel string
	YieldMonthsLeft    int
}

const (
	requiredStateNone       = ""
	requiredStateValue      = "value"
	requiredStateCompleted  = "completed"
	requiredStateNoDeadline = "no_deadline"
)

func (h *MetasHandler) HandleListarMetas(w http.ResponseWriter, r *http.Request) {
	aba := normalizeMetasAba(r.URL.Query().Get("aba"))
	data, err := h.buildMetasData("", aba)
	if err != nil {
		log.Printf("build metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	templateName := "metas-page"
	if r.Header.Get("HX-Request") != "" {
		data.FabOOB = true
		if r.URL.Query().Get("partial") == "conteudo" {
			templateName = "metas-tabs"
		}
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *MetasHandler) HandleMetasConceito(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	aba := normalizeMetasAba(r.URL.Query().Get("aba"))
	tData := time.Now()
	data, err := h.buildMetasData(reqID, aba)
	perfStep(reqID, "Metas", "buildMetasData", time.Since(tData))
	if err != nil {
		log.Printf("build metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = "Metas"

	if r.Header.Get("HX-Request") != "" {
		data.FabOOB = true
	}

	templateName := "metas-page"
	if r.URL.Query().Get("partial") == "conteudo" {
		templateName = "metas-tabs"
	}

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "Metas", "templateRender", time.Since(tR))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "Metas", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

type metasRenderMetadata struct {
	UserName            string
	UserFirstName       string
	UserInitials        string
	ProfilePhotoURL     string
	ActiveWorkspaceName string
	IsBusiness          bool
}

func (h *MetasHandler) loadMetasRenderMetadata() metasRenderMetadata {
	meta := metasRenderMetadata{
		UserName:            "Usuário",
		UserFirstName:       "Usuário",
		UserInitials:        "US",
		ProfilePhotoURL:     "",
		ActiveWorkspaceName: "Workspace",
		IsBusiness:          false,
	}

	var userName sql.NullString
	var profilePhotoPath sql.NullString
	var userUpdatedAt sql.NullInt64
	var workspaceName sql.NullString
	var workspaceTypeName sql.NullString

	err := h.DB.QueryRow(`
		WITH workspace_data AS (
			SELECT name, COALESCE(type, 'personal') AS type
			FROM workspaces
			WHERE id = ?
		),
		user_data AS (
			SELECT name,
				COALESCE(profile_photo_path, '') AS profile_photo_path,
				COALESCE(updated_at, unixepoch()) AS updated_at
			FROM users
			WHERE id = ?
		)
		SELECT
			user_data.name,
			user_data.profile_photo_path,
			user_data.updated_at,
			workspace_data.name,
			workspace_data.type
		FROM workspace_data
		LEFT JOIN user_data ON 1 = 1
	`, h.WorkspaceID, h.UserID).Scan(&userName, &profilePhotoPath, &userUpdatedAt, &workspaceName, &workspaceTypeName)
	if err != nil {
		meta.UserName, meta.UserInitials = queryDashboardUser(h.DB, h.UserID)
		meta.UserFirstName = extractFirstName(meta.UserName)
		meta.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
		meta.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
		meta.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness
		return meta
	}

	if name := strings.TrimSpace(userName.String); name != "" {
		meta.UserName = name
		meta.UserFirstName = extractFirstName(name)
		meta.UserInitials = initials(name)
	}
	if path := strings.TrimSpace(profilePhotoPath.String); path != "" {
		meta.ProfilePhotoURL = resolveProfilePhotoURL(path, userUpdatedAt.Int64)
	}
	if name := strings.TrimSpace(workspaceName.String); name != "" {
		meta.ActiveWorkspaceName = name
	}
	if strings.TrimSpace(workspaceTypeName.String) == models.WorkspaceTypeBusiness {
		meta.IsBusiness = true
	}

	return meta
}

func (h *MetasHandler) HandleNovaMeta(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	tCat := time.Now()
	categories, err := h.queryMetaCategories()
	perfStep(reqID, "MetasForm", "queryCategories", time.Since(tCat))
	if err != nil {
		log.Printf("query categories metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	openTab := normalizeMetaFormTab(r.URL.Query().Get("aba"))

	form := MetaFormData{
		Mode:       "create",
		OpenTab:    openTab,
		Categories: categories,
		IsBusiness: workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness,
	}

	editID := strings.TrimSpace(r.URL.Query().Get("id"))
	if editID != "" {
		form.Mode = "edit"
		tLoad := time.Now()
		if openTab == "limite" {
			if err := h.loadLimitFormData(&form, editID); err != nil {
				http.Error(w, "registro não encontrado", http.StatusNotFound)
				return
			}
		} else {
			if err := h.loadBoxFormData(&form, editID); err != nil {
				http.Error(w, "registro não encontrado", http.StatusNotFound)
				return
			}
		}
		perfStep(reqID, "MetasForm", "loadFormData", time.Since(tLoad))
	}

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "metas-form", form); err != nil {
		log.Printf("template metas-form error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	perfStep(reqID, "MetasForm", "templateRender", time.Since(tR))

	dbA := dbSnap(h.DB)
	perfDBDelta(reqID, "MetasForm", "total", dbB, dbA)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	perfRequest(reqID, r, time.Since(t0), buf.Len())
	buf.WriteTo(w)
}

func (h *MetasHandler) HandleCriarCaixinha(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()

	if err := r.ParseForm(); err != nil {
		respondMetaFormError(w, "caixinha", "formulário inválido")
		return
	}

	boxID := strings.TrimSpace(r.FormValue("box_id"))
	name := strings.TrimSpace(r.FormValue("name"))
	categoryID := strings.TrimSpace(r.FormValue("category_id"))
	targetStr := strings.TrimSpace(r.FormValue("target_amount"))
	var targetAmount int64
	var err error
	if targetStr == "" {
		targetAmount = 0
	} else {
		targetAmount, err = parseCurrency(targetStr)
	}
	if err != nil || name == "" || categoryID == "" || targetAmount < 0 {
		respondMetaFormError(w, "caixinha", "dados inválidos")
		return
	}

	monthlyYieldRate, err := parseMonthlyYieldRate(strings.TrimSpace(r.FormValue("monthly_yield_rate")))
	if err != nil {
		respondMetaFormError(w, "caixinha", "taxa de rentabilidade inválida — use o formato 0,8 para 0,8% a.m.")
		return
	}

	monthlyRecharge, monthsDesired, err := h.resolveRechargeInputs(r, boxID, targetAmount)
	if err != nil {
		respondMetaFormError(w, "caixinha", err.Error())
		return
	}
	targetDateValue, err := parseTargetMonthInput(strings.TrimSpace(r.FormValue("target_month")))
	if err != nil {
		respondMetaFormError(w, "caixinha", "data-alvo inválida")
		return
	}
	if monthlyRecharge <= 0 {
		respondMetaFormError(w, "caixinha", "recarga mensal inválida")
		return
	}

	tMut := time.Now()
	now := time.Now().Unix()
	if err := h.execMetaMutation(func(tx *sql.Tx) error {
		if err := ensureCategoryInWorkspaceTx(tx, categoryID, h.WorkspaceID); err != nil {
			return err
		}
		if boxID == "" {
			return execOneTx(tx, `
				INSERT INTO boxes (id, workspace_id, category_id, name, target_amount, monthly_recharge, monthly_yield_rate, target_date, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, uuid.NewString(), h.WorkspaceID, categoryID, name, targetAmount, monthlyRecharge, monthlyYieldRate, targetDateValue, now, now)
		}
		return execOneTx(tx, `
			UPDATE boxes
			SET category_id = ?, name = ?, target_amount = ?, monthly_recharge = ?, monthly_yield_rate = ?, target_date = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?
		`, categoryID, name, targetAmount, monthlyRecharge, monthlyYieldRate, targetDateValue, now, boxID, h.WorkspaceID)
	}); err != nil {
		log.Printf("upsert caixinha error: %v", err)
		respondMetaFormError(w, "caixinha", "erro ao salvar reserva")
		return
	}
	perfStep(reqID, "MetasCaixinha", "mutation", time.Since(tMut))

	_ = monthsDesired
	h.renderMetasMutationOOB(w, reqID, "caixinhas", t0)
}

func (h *MetasHandler) HandleCriarLimite(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()

	if err := r.ParseForm(); err != nil {
		respondMetaFormError(w, "limite", "formulário inválido")
		return
	}
	limitID := strings.TrimSpace(r.FormValue("limit_id"))
	categoryID := strings.TrimSpace(r.FormValue("category_id"))
	maxAmount, err := parseCurrency(r.FormValue("max_amount_monthly"))
	if categoryID == "" {
		respondMetaFormError(w, "limite", "Selecione uma categoria para criar o limite.")
		return
	}
	if err != nil || maxAmount <= 0 {
		respondMetaFormError(w, "limite", "dados inválidos")
		return
	}

	tMut := time.Now()
	now := time.Now().Unix()
	if err := h.execMetaMutation(func(tx *sql.Tx) error {
		if err := ensureCategoryInWorkspaceTx(tx, categoryID, h.WorkspaceID); err != nil {
			return err
		}
		var existingID string
		var dupErr error
		if limitID != "" {
			dupErr = tx.QueryRow(`SELECT id FROM cost_limits WHERE workspace_id = ? AND category_id = ? AND id != ?`, h.WorkspaceID, categoryID, limitID).Scan(&existingID)
		} else {
			dupErr = tx.QueryRow(`SELECT id FROM cost_limits WHERE workspace_id = ? AND category_id = ?`, h.WorkspaceID, categoryID).Scan(&existingID)
		}
		if dupErr != nil && !errors.Is(dupErr, sql.ErrNoRows) {
			return fmt.Errorf("verificar limite duplicado: %w", dupErr)
		}
		if existingID != "" {
			return errDuplicateLimit
		}
		if limitID != "" {
			return execOneTx(tx, `
				UPDATE cost_limits
				SET category_id = ?, max_amount_monthly = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ?
			`, categoryID, maxAmount, now, limitID, h.WorkspaceID)
		}
		return execOneTx(tx, `
			INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, uuid.NewString(), h.WorkspaceID, categoryID, maxAmount, 80, now, now)
	}); err != nil {
		if errors.Is(err, errDuplicateLimit) || strings.Contains(err.Error(), "UNIQUE constraint") {
			respondMetaFormError(w, "limite", "Já existe um limite para esta categoria. Edite o limite existente.")
			return
		}
		log.Printf("upsert limite error: %v", err)
		respondMetaFormError(w, "limite", "erro ao salvar limite")
		return
	}
	perfStep(reqID, "MetasLimite", "mutation", time.Since(tMut))

	h.renderMetasMutationOOB(w, reqID, "limites", t0)
}

func (h *MetasHandler) HandleDeleteCaixinha(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	boxID := strings.TrimPrefix(r.URL.Path, "/metas/caixinha/")
	if boxID == "" {
		http.Error(w, "ID inválido", http.StatusBadRequest)
		return
	}
	if err := h.execMetaMutation(func(tx *sql.Tx) error {
		return execOneTx(tx, `DELETE FROM boxes WHERE id = ? AND workspace_id = ?`, boxID, h.WorkspaceID)
	}); err != nil {
		log.Printf("delete caixinha error: %v", err)
		http.Error(w, "erro ao excluir reserva", http.StatusInternalServerError)
		return
	}
	h.renderMetasMutationOOB(w, reqID, "caixinhas", t0)
}

func (h *MetasHandler) HandleDeleteLimite(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()
	limitID := strings.TrimPrefix(r.URL.Path, "/metas/limite/")
	if limitID == "" {
		http.Error(w, "ID inválido", http.StatusBadRequest)
		return
	}
	if err := h.execMetaMutation(func(tx *sql.Tx) error {
		return execOneTx(tx, `DELETE FROM cost_limits WHERE id = ? AND workspace_id = ?`, limitID, h.WorkspaceID)
	}); err != nil {
		log.Printf("delete limite error: %v", err)
		http.Error(w, "erro ao excluir limite", http.StatusInternalServerError)
		return
	}
	h.renderMetasMutationOOB(w, reqID, "limites", t0)
}

func (h *MetasHandler) HandleAporteCaixinha(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()

	if err := r.ParseForm(); err != nil {
		respondMetaFormError(w, "aporte", "formulário inválido")
		return
	}
	boxID := strings.TrimSpace(r.FormValue("box_id"))
	amount, err := parseCurrency(r.FormValue("amount"))
	if err != nil || amount <= 0 || boxID == "" {
		respondMetaFormError(w, "aporte", "aporte inválido")
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if note == "" {
		note = "Aporte Manual"
	}
	now := time.Now().Unix()

	if err := h.execMetaMutation(func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM boxes WHERE id = ? AND workspace_id = ?`, boxID, h.WorkspaceID).Scan(&exists); err != nil {
			return fmt.Errorf("caixinha não autorizada ou não encontrada: %w", err)
		}
		return execOneTx(tx, `
			INSERT INTO box_virtual_ledger (id, box_id, reference_date, amount, type, description, created_at)
			VALUES (?, ?, ?, ?, 'RECHARGE', ?, ?)
		`, uuid.NewString(), boxID, now, amount, note, now)
	}); err != nil {
		log.Printf("aporte manual error: %v", err)
		respondMetaFormError(w, "aporte", "erro ao registrar aporte")
		return
	}

	aporteTitle := "Reserva recebeu aporte"
	aporteMsg := fmt.Sprintf("Voce aportou %s na reserva.", formatCurrencyLabel(amount))
	insertCaixinhaNotification(h.DB, h.UserID, h.WorkspaceID,
		aporteTitle,
		aporteMsg,
		"caixinha.aporte")

	h.renderMetasMutationOOB(w, reqID, "caixinhas", t0)
}

type BoxHistoryEvent struct {
	Amount      int64
	Type        string
	Description string
	DateLabel   string
	TypeLabel   string
	IsCredit    bool
	Money       MoneyDisplay
}

func boxLedgerTypeLabel(typ string) string {
	switch typ {
	case "RECHARGE":
		return "Aporte"
	case "BONUS":
		return "Ajuste/Bonus"
	case "RELEASE":
		return "Liberacao"
	case "CONSUME":
		return "Consumo"
	case "REVERSAL":
		return "Ajuste compensatorio"
	default:
		return typ
	}
}

func (h *MetasHandler) HandleHistoricoCaixinha(w http.ResponseWriter, r *http.Request) {
	boxID := strings.TrimSpace(r.URL.Query().Get("box_id"))
	if boxID == "" {
		http.Error(w, "ID inválido", http.StatusBadRequest)
		return
	}

	var boxName string
	if err := h.DB.QueryRow(`SELECT name FROM boxes WHERE id = ? AND workspace_id = ?`, boxID, h.WorkspaceID).Scan(&boxName); err != nil {
		http.Error(w, "reserva não encontrada", http.StatusNotFound)
		return
	}

	rows, err := h.DB.Query(`
		SELECT l.amount, l.type,
			CASE WHEN l.type = 'CONSUME' AND t.description IS NOT NULL AND t.description != ''
			     THEN t.description
			     ELSE l.description END as description,
			l.reference_date, l.created_at
		FROM box_virtual_ledger l
		LEFT JOIN transactions t ON l.source_transaction_id = t.id
		WHERE l.box_id = ?
		ORDER BY l.created_at DESC, l.id DESC
		LIMIT 100
	`, boxID)
	if err != nil {
		log.Printf("query box history error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var events []BoxHistoryEvent
	for rows.Next() {
		var e BoxHistoryEvent
		var refDate, createdAt int64
		if err := rows.Scan(&e.Amount, &e.Type, &e.Description, &refDate, &createdAt); err != nil {
			continue
		}
		e.DateLabel = formatDateLabel(refDate)
		e.TypeLabel = boxLedgerTypeLabel(e.Type)
		e.Money = MoneyMinor(e.Amount)
		e.IsCredit = e.Amount >= 0
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("box history rows error: %v", err)
	}

	data := struct {
		BoxID      string
		BoxName    string
		Events     []BoxHistoryEvent
		HasEvents  bool
		IsBusiness bool
	}{
		BoxID:      boxID,
		BoxName:    boxName,
		Events:     events,
		HasEvents:  len(events) > 0,
		IsBusiness: workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness,
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "box-history", data); err != nil {
		log.Printf("template box-history error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *MetasHandler) HandleHistoricoLimite(w http.ResponseWriter, r *http.Request) {
	limitID := strings.TrimSpace(r.URL.Query().Get("limit_id"))
	if limitID == "" {
		http.Error(w, "ID inválido", http.StatusBadRequest)
		return
	}

	var catID, catName, catIcon, catColor string
	var maxAmount int64
	if err := h.DB.QueryRow(`
		SELECT cl.max_amount_monthly, COALESCE(c.name, ''), c.id,
			COALESCE(c.icon, 'tag'), COALESCE(c.color, '#6b7280')
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id
		WHERE cl.id = ? AND cl.workspace_id = ?
	`, limitID, h.WorkspaceID).Scan(&maxAmount, &catName, &catID, &catIcon, &catColor); err != nil {
		http.Error(w, "limite não encontrado", http.StatusNotFound)
		return
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	monthEnd := time.Date(now.Year(), now.Month()+1, 0, 23, 59, 59, 0, time.UTC).Unix()

	rows, err := h.DB.Query(`
		SELECT t.id, t.description, t.amount, t.date
		FROM transactions t
		WHERE t.workspace_id = ?
			AND t.category_id IN (SELECT id FROM categories WHERE id = ? OR parent_id = ?)
			AND t.type = 'EXPENSE'
			AND t.date >= ? AND t.date <= ?
		ORDER BY t.date DESC
		LIMIT 50
	`, h.WorkspaceID, catID, catID, monthStart, monthEnd)
	if err != nil {
		log.Printf("query limit history error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type LimitTx struct {
		Description string
		DateLabel   string
		Amount      MoneyDisplay
		ID          string
	}
	var transactions []LimitTx
	var totalSpent int64
	for rows.Next() {
		var tx LimitTx
		var amount int64
		var dateUnix int64
		if err := rows.Scan(&tx.ID, &tx.Description, &amount, &dateUnix); err != nil {
			continue
		}
		tx.DateLabel = formatDateLabel(dateUnix)
		tx.Amount = MoneyMinor(amount)
		totalSpent += amount
		transactions = append(transactions, tx)
	}
	if err := rows.Err(); err != nil {
		log.Printf("limit history rows error: %v", err)
	}

	catColor = normalizeUIThemeColor(catColor)
	remaining := maxAmount - totalSpent
	if remaining < 0 {
		remaining = 0
	}
	percent := 0
	if maxAmount > 0 {
		percent = int(float64(totalSpent) / float64(maxAmount) * 100)
	}

	data := struct {
		LimitID         string
		LimitName       string
		CategoryID      string
		CategoryName    string
		CategoryIcon    string
		CategoryColor   string
		MaxAmount       MoneyDisplay
		Spent           MoneyDisplay
		Remaining       MoneyDisplay
		Percent         int
		Transactions    []LimitTx
		HasTransactions bool
		IsBusiness      bool
	}{
		LimitID:         limitID,
		LimitName:       catName,
		CategoryID:      catID,
		CategoryName:    catName,
		CategoryIcon:    catIcon,
		CategoryColor:   catColor,
		MaxAmount:       MoneyMinor(maxAmount),
		Spent:           MoneyMinor(totalSpent),
		Remaining:       MoneyMinor(remaining),
		Percent:         percent,
		Transactions:    transactions,
		HasTransactions: len(transactions) > 0,
		IsBusiness:      workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness,
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "limit-history", data); err != nil {
		log.Printf("template limit-history error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *MetasHandler) HandleResgateCaixinha(w http.ResponseWriter, r *http.Request) {
	t0 := time.Now()
	reqID := perfReqID()

	if err := r.ParseForm(); err != nil {
		respondMetaFormError(w, "resgate", "formulário inválido")
		return
	}
	boxID := strings.TrimSpace(r.FormValue("box_id"))
	amount, err := parseCurrency(r.FormValue("amount"))
	if err != nil || amount <= 0 || boxID == "" {
		respondMetaFormError(w, "resgate", "resgate inválido")
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if note == "" {
		note = "Liberação de reserva"
	}
	now := time.Now().Unix()

	if err := h.execMetaMutation(func(tx *sql.Tx) error {
		var reserved int64
		if err := tx.QueryRow(`
			SELECT COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0)
			FROM boxes b
			WHERE b.id = ? AND b.workspace_id = ?
		`, boxID, h.WorkspaceID).Scan(&reserved); err != nil {
			return fmt.Errorf("caixinha não autorizada ou não encontrada: %w", err)
		}
		if reserved <= 0 {
			return fmt.Errorf("caixinha sem saldo reservado disponível")
		}
		if amount > reserved {
			return fmt.Errorf("valor acima do saldo reservado atual")
		}
		return execOneTx(tx, `
			INSERT INTO box_virtual_ledger (id, box_id, reference_date, amount, type, description, created_at)
			VALUES (?, ?, ?, ?, 'RELEASE', ?, ?)
		`, uuid.NewString(), boxID, now, -amount, note, now)
	}); err != nil {
		log.Printf("resgate manual error: %v", err)
		respondMetaFormError(w, "resgate", "erro ao registrar liberação")
		return
	}

	resgateTitle := "Reserva teve saldo liberado"
	resgateMsg := fmt.Sprintf("Voce liberou %s da reserva para o saldo livre.", formatCurrencyLabel(amount))
	insertCaixinhaNotification(h.DB, h.UserID, h.WorkspaceID,
		resgateTitle,
		resgateMsg,
		"caixinha.resgate")

	h.renderMetasMutationOOB(w, reqID, "caixinhas", t0)
}

func (h *MetasHandler) execMetaMutation(run func(tx *sql.Tx) error) error {
	tx, err := h.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := run(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (h *MetasHandler) renderMetasMutationOOB(w http.ResponseWriter, reqID string, aba string, handlerStart time.Time) {
	tData := time.Now()
	data, err := h.buildMetasData(reqID, aba)
	perfStep(reqID, "MetasMutation", "buildMetasData", time.Since(tData))
	if err != nil {
		log.Printf("build metas after mutation error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.ContentOOB = true
	data.FabOOB = true

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "metas-tabs", data); err != nil {
		log.Printf("template metas-tabs after mutation error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	buf.WriteString(`<div id="bottom-sheet-container" hx-swap-oob="innerHTML"></div>`)
	buf.WriteString(`<div id="caixinha-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteString(`<div id="limite-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteString(`<div id="aporte-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteString(`<div id="resgate-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteString(NotificationBadgeHTML(h.DB, h.UserID, h.WorkspaceID))
	perfStep(reqID, "MetasMutation", "oobRender", time.Since(tR))
	perfStep(reqID, "MetasMutation", "total", time.Since(handlerStart))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *MetasHandler) queryMetaCategories() ([]FormCategory, error) {
	rows, err := h.DB.Query(`
		SELECT id, name, icon, color, type
		FROM categories
		WHERE workspace_id = ?
		ORDER BY name ASC
	`, h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []FormCategory
	for rows.Next() {
		var c FormCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.Icon, &c.Color, &c.Type); err != nil {
			return nil, err
		}
		c.Color = normalizeUIThemeColor(c.Color)
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

func (h *MetasHandler) buildMetasData(reqID string, aba string) (MetasData, error) {
	tS := time.Now()
	now := time.Now()
	months := []string{
		"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho",
		"Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro",
	}
	reserveBalance, err := services.CalculateWorkspaceReserveBalance(h.DB, h.WorkspaceID)
	if err != nil {
		return MetasData{}, fmt.Errorf("calculate workspace reserve balance: %w", err)
	}
	perfStep(reqID, "Metas", "reserveBalance", time.Since(tS))
	tS = time.Now()

	renderMeta := h.loadMetasRenderMetadata()
	data := MetasData{
		Title:               "Metas",
		UserName:            renderMeta.UserName,
		UserFirstName:       renderMeta.UserFirstName,
		UserInitials:        renderMeta.UserInitials,
		ProfilePhotoURL:     renderMeta.ProfilePhotoURL,
		Aba:                 normalizeMetasAba(aba),
		MonthLabel:          fmt.Sprintf("%s %d", months[now.Month()-1], now.Year()),
		RealBalance:         MoneyMinor(reserveBalance.RealBalance),
		ReservedBalance:     MoneyMinor(reserveBalance.ReservedBalance),
		FreeBalance:         MoneyMinor(reserveBalance.FreeBalance),
		FreeBalanceNegative: reserveBalance.FreeBalance < 0,
		IsBusiness:          renderMeta.IsBusiness,
		ActiveWorkspaceName: renderMeta.ActiveWorkspaceName,
	}

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	monthEnd := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")

	limitRows, err := h.DB.Query(`
		SELECT cl.id, cl.category_id, cl.max_amount_monthly,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(SUM(t.amount), 0) AS spent
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id
		LEFT JOIN transactions t ON t.workspace_id = cl.workspace_id
			AND t.category_id IN (SELECT id FROM categories WHERE id = cl.category_id OR parent_id = cl.category_id)
			AND t.type = 'EXPENSE'
			AND date(t.date, 'unixepoch') >= ? AND date(t.date, 'unixepoch') <= ?
		WHERE cl.workspace_id = ?
		GROUP BY cl.id
		ORDER BY c.name
	`, monthStart, monthEnd, h.WorkspaceID)
	if err != nil {
		return data, fmt.Errorf("query cost_limits: %w", err)
	}
	defer limitRows.Close()

	var totalLimits int64
	for limitRows.Next() {
		var card LimiteCard
		var maxAmount, spent int64
		if err := limitRows.Scan(&card.ID, &card.CategoryID, &maxAmount, &card.CategoryName, &card.CategoryIcon, &card.CategoryColor, &spent); err != nil {
			continue
		}
		remaining := maxAmount - spent
		if remaining < 0 {
			remaining = 0
		}
		if maxAmount > 0 {
			card.Percent = int(float64(spent) / float64(maxAmount) * 100)
		}
		card.PercentColor = "amber"
		if card.Percent >= 90 {
			card.PercentColor = "rose"
		}
		card.MaxAmount = MoneyMinor(maxAmount)
		card.Spent = MoneyMinor(spent)
		card.Remaining = MoneyMinor(remaining)
		totalLimits += spent
		data.Limites = append(data.Limites, card)
	}
	data.LimitesCount = len(data.Limites)
	data.TotalLimits = MoneyMinor(totalLimits)
	perfStep(reqID, "Metas", "loadLimits", time.Since(tS))
	tS = time.Now()

	boxRows, err := h.queryBoxesForMetas()
	if err != nil {
		return data, fmt.Errorf("query boxes: %w", err)
	}
	defer boxRows.Close()

	var totalAcumulado int64
	for boxRows.Next() {
		var card CaixinhaCard
		var targetDate sql.NullInt64
		var target, monthly, balance int64
		var yieldRate float64
		var categoryName string
		if err := boxRows.Scan(&card.ID, &card.CategoryID, &card.Name, &target, &monthly, &targetDate, &card.Icon, &card.Color, &categoryName, &yieldRate, &balance); err != nil {
			continue
		}
		card.CategoryName = categoryName
		card.IsNegative = balance < 0

		if balance < 0 {
			card.Percent = 0
		} else if target > 0 {
			card.Percent = int(float64(balance) / float64(target) * 100)
		}
		projection := services.EstimateBoxProjection(balance, target, monthly)
		switch projection.Status {
		case services.BoxProjectionStatusCompleted:
			card.ForecastLabel = "Previsão: meta concluída"
		case services.BoxProjectionStatusForecast:
			card.MonthsLeft = projection.MonthsLeft
			if card.MonthsLeft == 1 {
				card.ForecastLabel = "Previsão: 1 mês"
			} else {
				card.ForecastLabel = fmt.Sprintf("Previsão: %d meses", card.MonthsLeft)
			}
		default:
			card.ForecastLabel = "Previsão: sem previsão"
		}
		card.PercentColor = "violet"
		if card.Percent >= 75 {
			card.PercentColor = "emerald"
		} else if card.Percent >= 50 {
			card.PercentColor = "sky"
		}
		card.Target = MoneyMinor(target)
		card.Balance = MoneyMinor(balance)
		card.MonthlyRecharge = MoneyMinor(monthly)

		if yieldRate > 0 && target > 0 {
			yieldProjection := services.EstimateBoxProjectionWithYield(balance, target, monthly, yieldRate)
			switch yieldProjection.Status {
			case services.BoxProjectionStatusForecast:
				card.YieldMonthsLeft = yieldProjection.MonthsLeft
				if projection.Status == services.BoxProjectionStatusNoForecast && yieldProjection.MonthsLeft > 0 {
					if card.YieldMonthsLeft == 1 {
						card.YieldForecastLabel = "Com rendimento est.: 1 mês"
					} else {
						card.YieldForecastLabel = fmt.Sprintf("Com rendimento est.: %d meses", card.YieldMonthsLeft)
					}
				} else if projection.MonthsLeft > yieldProjection.MonthsLeft {
					if card.YieldMonthsLeft == 1 {
						card.YieldForecastLabel = "Com rendimento est.: 1 mês"
					} else {
						card.YieldForecastLabel = fmt.Sprintf("Com rendimento est.: %d meses", card.YieldMonthsLeft)
					}
				}
			}
		}

		if target > 0 {
			if projection.Status == services.BoxProjectionStatusCompleted {
				card.RequiredState = requiredStateCompleted
			} else {
				targetUnix := int64(0)
				if targetDate.Valid {
					targetUnix = targetDate.Int64
					card.TargetMonth = formatMonthLabelFromUnix(targetUnix)
				}
				required, _, ok := services.EstimateRequiredMonthlyContributionByTargetDate(balance, target, targetUnix, now.UTC())
				if ok {
					card.RequiredState = requiredStateValue
					card.RequiredMonthly = MoneyMinor(required)
				} else {
					card.RequiredState = requiredStateNoDeadline
				}
			}
		} else {
			card.RequiredState = requiredStateNone
		}
		totalAcumulado += balance
		data.Caixinhas = append(data.Caixinhas, card)
	}
	data.CaixinhasCount = len(data.Caixinhas)
	data.TotalAcumulado = MoneyMinor(totalAcumulado)
	data.TotalAcumuladoNegativo = totalAcumulado < 0
	perfStep(reqID, "Metas", "loadBoxes", time.Since(tS))
	return data, nil
}

func resolveProfilePhotoURL(photoPath string, updatedAt int64) string {
	photoPath = strings.TrimSpace(photoPath)
	if photoPath == "" {
		return ""
	}

	fileName := strings.TrimPrefix(photoPath, "/uploads/profile/")
	if fileName == photoPath || fileName == "" {
		return ""
	}

	fullPath := filepath.Join(paths.ProfileUploadsDir(), fileName)
	if _, err := os.Stat(fullPath); err != nil {
		return ""
	}

	return fmt.Sprintf("%s?v=%d", photoPath, updatedAt)
}

func (h *MetasHandler) resolveRechargeInputs(r *http.Request, boxID string, targetAmount int64) (int64, int, error) {
	monthlyRaw := strings.TrimSpace(r.FormValue("monthly_recharge"))
	targetMonthRaw := strings.TrimSpace(r.FormValue("target_month"))

	var monthlyRecharge int64
	var err error
	if monthlyRaw != "" {
		monthlyRecharge, err = parseCurrency(monthlyRaw)
		if err != nil {
			return 0, 0, fmt.Errorf("recarga mensal inválida")
		}
	}

	monthsDesired := 0
	if targetMonthRaw != "" {
		monthsDesired = monthsUntilTargetMonth(targetMonthRaw, time.Now())
	}

	currentBalance := int64(0)
	if boxID != "" {
		_ = h.DB.QueryRow(`
			SELECT COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0)
			FROM boxes b
			WHERE b.id = ? AND b.workspace_id = ?
		`, boxID, h.WorkspaceID).Scan(&currentBalance)
		if currentBalance < 0 {
			currentBalance = 0
		}
	}

	if targetAmount > 0 {
		if monthlyRecharge <= 0 && monthsDesired > 0 {
			if projectedMonthly, ok := services.EstimateRequiredMonthlyContribution(currentBalance, targetAmount, monthsDesired); ok {
				monthlyRecharge = projectedMonthly
			}
		}
		if monthsDesired <= 0 && monthlyRecharge > 0 {
			monthsDesired = services.EstimateBoxProjection(currentBalance, targetAmount, monthlyRecharge).MonthsLeft
		}
	}

	return monthlyRecharge, monthsDesired, nil
}

func (h *MetasHandler) loadLimitFormData(form *MetaFormData, id string) error {
	var maxAmount int64
	var catID, icon, color string
	err := h.DB.QueryRow(`
		SELECT cl.id, cl.category_id, cl.max_amount_monthly, COALESCE(c.name, ''), COALESCE(c.icon, 'tag'), COALESCE(c.color, '#6b7280')
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id
		WHERE cl.id = ? AND cl.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&form.LimitID, &catID, &maxAmount, &form.CategoryName, &icon, &color)
	if err != nil {
		return err
	}
	form.CategoryID = catID
	form.CategoryIcon = icon
	form.CategoryColor = normalizeUIThemeColor(color)
	form.MaxAmountInput = formatCurrencyCentsBase(maxAmount)
	return nil
}

func (h *MetasHandler) loadBoxFormData(form *MetaFormData, id string) error {
	var targetAmount, monthlyRecharge, balance int64
	var monthlyYieldRate float64
	var targetDate sql.NullInt64
	var catID, icon, color string

	err := h.DB.QueryRow(`
		SELECT b.id, b.category_id, b.name, b.target_amount, b.monthly_recharge, b.target_date,
			COALESCE(c.name, ''),
			COALESCE(c.icon, 'tag'),
			COALESCE(c.color, '#6b7280'),
			b.monthly_yield_rate,
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS balance
		FROM boxes b
		LEFT JOIN categories c ON c.id = b.category_id
		WHERE b.id = ? AND b.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&form.BoxID, &catID, &form.Name, &targetAmount, &monthlyRecharge, &targetDate, &form.CategoryName, &icon, &color, &monthlyYieldRate, &balance)
	if err != nil && isMissingColumnError(err, "target_date") {
		targetDate = sql.NullInt64{}
		monthlyYieldRate = 0
		err = h.DB.QueryRow(`
			SELECT b.id, b.category_id, b.name, b.target_amount, b.monthly_recharge,
				COALESCE(c.name, ''),
				COALESCE(c.icon, 'tag'),
				COALESCE(c.color, '#6b7280'),
				COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS balance
			FROM boxes b
			LEFT JOIN categories c ON c.id = b.category_id
			WHERE b.id = ? AND b.workspace_id = ?
		`, id, h.WorkspaceID).Scan(&form.BoxID, &catID, &form.Name, &targetAmount, &monthlyRecharge, &form.CategoryName, &icon, &color, &balance)
	}
	if err != nil {
		return err
	}

	form.CategoryID = catID
	form.CategoryIcon = icon
	form.CategoryColor = normalizeUIThemeColor(color)
	form.TargetAmountInput = formatCurrencyCentsBase(targetAmount)
	form.MonthlyRechargeInput = formatCurrencyCentsBase(monthlyRecharge)
	form.MonthlyYieldRateInput = formatMonthlyYieldRateForInput(monthlyYieldRate)
	form.TargetMonthInput = formatMonthInputFromUnix(targetDate)
	form.BalanceLabel = formatCurrencyLabel(balance)
	return nil
}

func normalizeMetasAba(aba string) string {
	if aba == "caixinhas" || aba == "reservas" {
		return "caixinhas"
	}
	return "limites"
}

func normalizeMetaFormTab(tab string) string {
	if tab == "caixinha" || tab == "caixinhas" || tab == "reserva" || tab == "reservas" {
		return "caixinha"
	}
	return "limite"
}

func monthsUntilTargetMonth(targetMonth string, now time.Time) int {
	t, err := time.Parse("2006-01", targetMonth)
	if err != nil {
		return 0
	}
	currentMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	target := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	if target.Before(currentMonth) {
		return 0
	}
	months := int(target.Year()-currentMonth.Year())*12 + int(target.Month()-currentMonth.Month())
	if months <= 0 {
		return 1
	}
	return months
}

func parseTargetMonthInput(targetMonth string) (interface{}, error) {
	if targetMonth == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01", targetMonth)
	if err != nil {
		return nil, err
	}
	return time.Date(t.Year(), t.Month(), 1, 12, 0, 0, 0, time.UTC).Unix(), nil
}

func formatMonthInputFromUnix(targetDate sql.NullInt64) string {
	if !targetDate.Valid || targetDate.Int64 <= 0 {
		return ""
	}
	return time.Unix(targetDate.Int64, 0).UTC().Format("2006-01")
}

func formatMonthLabelFromUnix(targetDateUnix int64) string {
	if targetDateUnix <= 0 {
		return ""
	}
	return time.Unix(targetDateUnix, 0).UTC().Format("01/2006")
}

func (h *MetasHandler) queryBoxesForMetas() (*sql.Rows, error) {
	queryWithTargetDate := `
		SELECT b.id, b.category_id, b.name, b.target_amount, b.monthly_recharge, b.target_date,
			COALESCE(c.icon, 'piggy-bank'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(c.name, ''),
			b.monthly_yield_rate,
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS total_balance
		FROM boxes b
		LEFT JOIN categories c ON c.id = b.category_id
		WHERE b.workspace_id = ?
		ORDER BY b.name
	`
	rows, err := h.DB.Query(queryWithTargetDate, h.WorkspaceID)
	if err == nil || !isMissingColumnError(err, "target_date") {
		return rows, err
	}

	queryLegacy := `
		SELECT b.id, b.category_id, b.name, b.target_amount, b.monthly_recharge, NULL AS target_date,
			COALESCE(c.icon, 'piggy-bank'),
			COALESCE(c.color, '#6b7280'),
			COALESCE(c.name, ''),
			0.0 AS monthly_yield_rate,
			COALESCE((SELECT SUM(amount) FROM box_virtual_ledger WHERE box_id = b.id), 0) AS total_balance
		FROM boxes b
		LEFT JOIN categories c ON c.id = b.category_id
		WHERE b.workspace_id = ?
		ORDER BY b.name
	`
	return h.DB.Query(queryLegacy, h.WorkspaceID)
}

func isMissingColumnError(err error, column string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such column") && strings.Contains(msg, strings.ToLower(column))
}

func parseMonthlyYieldRate(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	raw = strings.ReplaceAll(raw, ",", ".")
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if rate < 0 {
		return 0, nil
	}
	if rate > 100 {
		return 0, fmt.Errorf("taxa maxima de 100%% a.m.")
	}
	return rate / 100.0, nil
}

func formatMonthlyYieldRateForInput(rate float64) string {
	if rate <= 0 {
		return ""
	}
	ratePercent := rate * 100.0
	formatted := strconv.FormatFloat(ratePercent, 'f', -1, 64)
	formatted = strings.ReplaceAll(formatted, ".", ",")
	return formatted
}
