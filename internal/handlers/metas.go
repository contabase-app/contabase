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
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/icons"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"

	"github.com/google/uuid"
)

var errDuplicateLimit = errors.New("limite duplicado")

type MetasHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
	// ActorRole é a revalidação interna dos GETs (defesa em profundidade).
	// A rota continua sendo a primeira barreira de permissão.
	ActorRole string
}

const (
	metasPermDashboardRead = "dashboard:read"
	metasPermGoalsWrite    = "goals:write"
)

func (h *MetasHandler) allowMetas(permission string) bool {
	return models.HasPermission(&models.WorkspaceMember{Role: h.ActorRole}, permission)
}

func (h *MetasHandler) requireMetasPermission(w http.ResponseWriter, permission string) bool {
	if h.allowMetas(permission) {
		return true
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return false
}

func respondMetaFormError(w http.ResponseWriter, idPrefix string, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<div id="%s-form-error" hx-swap-oob="true" class="rounded-xl border border-rose-500/35 bg-rose-500/10 px-3 py-2 text-sm text-rose-100">%s</div>`, idPrefix, template.HTMLEscapeString(message))
}

type MetasData struct {
	AppShellAccess
	Title               string
	UserName            string
	UserFirstName       string
	UserInitials        string
	ProfilePhotoURL     string
	NotificationCount   int
	ContentOOB          bool
	HeaderActionOOB     bool
	TotalLimits         MoneyDisplay
	LimitesCount        int
	Limites             []LimiteCard
	IsBusiness          bool
	ActiveWorkspaceName string
}

type MetaFormData struct {
	Mode           string
	LimitID        string
	CategoryID     string
	CategoryName   string
	CategoryIcon   string
	CategoryColor  string
	LimitIconKey   string
	MaxAmountInput string
}

type LimiteCard struct {
	ID            string
	CategoryID    string
	CategoryName  string
	CategoryIcon  string
	IconKey       string
	CategoryColor string
	MaxAmount     MoneyDisplay
	Spent         MoneyDisplay
	Remaining     MoneyDisplay
	Percent       int
	PercentColor  string
}

// IconOrDefault aplica a herança do Limite: escolha própria -> ícone da
// categoria vinculada -> fallback neutro do catálogo.
func (c LimiteCard) IconOrDefault() string { return icons.Resolve(c.IconKey, c.CategoryIcon) }

// IconLabel devolve o nome amigável do ícone efetivo.
func (c LimiteCard) IconLabel() string { return icons.Label(c.IconOrDefault()) }

// HasOwnIcon informa se o Limite tem ícone próprio ou se está herdando.
func (c LimiteCard) HasOwnIcon() bool { return icons.CanonicalKey(c.IconKey) != "" }

// Tone devolve a faixa semântica de consumo do limite (mesma regra do
// dashboard e dos relatórios); a barra do card exibe o disponível.
func (c LimiteCard) Tone() string { return limitToneForPercent(c.Percent) }

func (h *MetasHandler) HandleListarMetas(w http.ResponseWriter, r *http.Request) {
	if !h.requireMetasPermission(w, metasPermDashboardRead) {
		return
	}
	data, err := h.buildMetasData("")
	if err != nil {
		log.Printf("build metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	templateName := "metas-page"
	if r.Header.Get("HX-Request") != "" {
		if r.URL.Query().Get("partial") == "conteudo" {
			data.HeaderActionOOB = true
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
	if !h.requireMetasPermission(w, metasPermDashboardRead) {
		return
	}
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	tData := time.Now()
	data, err := h.buildMetasData(reqID)
	perfStep(reqID, "Metas", "buildMetasData", time.Since(tData))
	if err != nil {
		log.Printf("build metas error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = "Metas"

	if r.Header.Get("HX-Request") != "" {
		if r.URL.Query().Get("partial") == "conteudo" {
			data.HeaderActionOOB = true
		}
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
	if !h.requireMetasPermission(w, metasPermGoalsWrite) {
		return
	}
	t0 := time.Now()
	reqID := perfReqID()
	dbB := dbSnap(h.DB)

	form := MetaFormData{Mode: "create"}

	editID := strings.TrimSpace(r.URL.Query().Get("id"))
	if editID != "" {
		form.Mode = "edit"
		tLoad := time.Now()
		if err := h.loadLimitFormData(&form, editID); err != nil {
			http.Error(w, "registro não encontrado", http.StatusNotFound)
			return
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
	iconKey := icons.CanonicalKey(r.FormValue("icon_key"))

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
				SET category_id = ?, max_amount_monthly = ?, icon_key = ?, updated_at = ?
				WHERE id = ? AND workspace_id = ?
			`, categoryID, maxAmount, iconKey, now, limitID, h.WorkspaceID)
		}
		return execOneTx(tx, `
			INSERT INTO cost_limits (id, workspace_id, category_id, max_amount_monthly, alert_threshold, icon_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, uuid.NewString(), h.WorkspaceID, categoryID, maxAmount, 80, iconKey, now, now)
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

	h.renderMetasMutationOOB(w, reqID, t0)
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
	h.renderMetasMutationOOB(w, reqID, t0)
}

func (h *MetasHandler) HandleHistoricoLimite(w http.ResponseWriter, r *http.Request) {
	if !h.requireMetasPermission(w, metasPermDashboardRead) {
		return
	}
	limitID := strings.TrimSpace(r.URL.Query().Get("limit_id"))
	if limitID == "" {
		http.Error(w, "ID inválido", http.StatusBadRequest)
		return
	}

	var catID, catName, catIcon, catColor, limitIconKey string
	var maxAmount int64
	if err := h.DB.QueryRow(`
		SELECT cl.max_amount_monthly, COALESCE(c.name, ''), c.id,
			COALESCE(c.icon, ''), COALESCE(c.color, '#6b7280'), COALESCE(cl.icon_key, '')
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id AND c.workspace_id = cl.workspace_id
		WHERE cl.id = ? AND cl.workspace_id = ?
	`, limitID, h.WorkspaceID).Scan(&maxAmount, &catName, &catID, &catIcon, &catColor, &limitIconKey); err != nil {
		http.Error(w, "limite não encontrado", http.StatusNotFound)
		return
	}

	now := appTime(time.Now())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	monthEnd := time.Date(now.Year(), now.Month()+1, 0, 23, 59, 59, 0, time.UTC).Unix()

	const historyFilter = `
		WHERE t.workspace_id = ?
			AND t.category_id IN (
				SELECT id
				FROM categories
				WHERE workspace_id = ?
				  AND (id = ? OR parent_id = ?)
			)
			AND t.type = 'EXPENSE'
			AND t.date >= ? AND t.date <= ?`
	args := []any{h.WorkspaceID, h.WorkspaceID, catID, catID, monthStart, monthEnd}
	// Read the full aggregate and the limited list from the same snapshot.
	readTx, err := h.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer readTx.Rollback()
	var totalSpent int64
	if err := readTx.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(t.amount), 0) FROM transactions t `+historyFilter, args...).Scan(&totalSpent); err != nil {
		log.Printf("query limit history total error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	rows, err := readTx.QueryContext(r.Context(), `
		SELECT t.id, t.description, t.amount, t.date,
			COALESCE(a.name, ''),
			COALESCE(a.type, '')
		FROM transactions t
		LEFT JOIN accounts a ON a.id = t.account_id AND a.workspace_id = t.workspace_id
		`+historyFilter+`
		ORDER BY t.date DESC
		LIMIT 50
	`, args...)
	if err != nil {
		log.Printf("query limit history error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type LimitTx struct {
		Description  string
		DateLabel    string
		Amount       MoneyDisplay
		ID           string
		AccountName  string
		AccountLabel string
	}
	var transactions []LimitTx
	for rows.Next() {
		var tx LimitTx
		var amount int64
		var dateUnix int64
		var accountType string
		if err := rows.Scan(&tx.ID, &tx.Description, &amount, &dateUnix, &tx.AccountName, &accountType); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		tx.DateLabel = formatDateLabel(dateUnix)
		tx.Amount = MoneyMinor(amount)
		tx.AccountLabel = transactionAccountLabel(accountType)
		transactions = append(transactions, tx)
	}
	if err := rows.Err(); err != nil {
		log.Printf("limit history rows error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := rows.Close(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if err := readTx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
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
		CategoryIcon:    icons.Resolve(limitIconKey, catIcon),
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

func (h *MetasHandler) renderMetasMutationOOB(w http.ResponseWriter, reqID string, handlerStart time.Time) {
	tData := time.Now()
	data, err := h.buildMetasData(reqID)
	perfStep(reqID, "MetasMutation", "buildMetasData", time.Since(tData))
	if err != nil {
		log.Printf("build metas after mutation error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.ContentOOB = true
	data.HeaderActionOOB = true

	tR := time.Now()
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "metas-tabs", data); err != nil {
		log.Printf("template metas-tabs after mutation error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	buf.WriteString(`<div id="bottom-sheet-container" hx-swap-oob="innerHTML"></div>`)
	buf.WriteString(`<div id="limite-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteString(NotificationBadgeHTML(h.DB, h.UserID, h.WorkspaceID))
	perfStep(reqID, "MetasMutation", "oobRender", time.Since(tR))
	perfStep(reqID, "MetasMutation", "total", time.Since(handlerStart))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *MetasHandler) buildMetasData(reqID string) (MetasData, error) {
	tS := time.Now()
	now := appTime(time.Now())

	renderMeta := h.loadMetasRenderMetadata()
	data := MetasData{
		Title:               "Metas",
		UserName:            renderMeta.UserName,
		UserFirstName:       renderMeta.UserFirstName,
		UserInitials:        renderMeta.UserInitials,
		ProfilePhotoURL:     renderMeta.ProfilePhotoURL,
		IsBusiness:          renderMeta.IsBusiness,
		ActiveWorkspaceName: renderMeta.ActiveWorkspaceName,
	}
	data.AppShellAccess = QueryAppShellAccess(h.DB, h.WorkspaceID, h.UserID, data.IsBusiness)

	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	monthEnd := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")

	limitRows, err := h.DB.Query(`
		SELECT cl.id, cl.category_id, cl.max_amount_monthly,
			COALESCE(c.name, 'Sem categoria'),
			COALESCE(c.icon, ''),
			COALESCE(c.color, '#6b7280'),
			COALESCE(cl.icon_key, ''),
			COALESCE(SUM(`+transactionNetAmountTSQL+`), 0) AS spent
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id AND c.workspace_id = cl.workspace_id
		LEFT JOIN transactions t ON t.workspace_id = cl.workspace_id
			AND t.category_id IN (SELECT id FROM categories WHERE workspace_id = cl.workspace_id AND (id = cl.category_id OR parent_id = cl.category_id))
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
		if err := limitRows.Scan(&card.ID, &card.CategoryID, &maxAmount, &card.CategoryName, &card.CategoryIcon, &card.CategoryColor, &card.IconKey, &spent); err != nil {
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

func (h *MetasHandler) loadLimitFormData(form *MetaFormData, id string) error {
	var maxAmount int64
	var catID sql.NullString
	var icon, color string
	err := h.DB.QueryRow(`
		SELECT cl.id, cl.category_id, cl.max_amount_monthly, COALESCE(c.name, ''), COALESCE(c.icon, ''), COALESCE(c.color, '#6b7280'), COALESCE(cl.icon_key, '')
		FROM cost_limits cl
		JOIN categories c ON c.id = cl.category_id AND c.workspace_id = cl.workspace_id
		WHERE cl.id = ? AND cl.workspace_id = ?
	`, id, h.WorkspaceID).Scan(&form.LimitID, &catID, &maxAmount, &form.CategoryName, &icon, &color, &form.LimitIconKey)
	if err != nil {
		return err
	}
	if catID.Valid {
		form.CategoryID = catID.String
	}
	form.CategoryIcon = icon
	form.CategoryColor = normalizeUIThemeColor(color)
	form.MaxAmountInput = formatCurrencyCentsBase(maxAmount)
	return nil
}

func transactionAccountLabel(accountType string) string {
	switch strings.ToUpper(strings.TrimSpace(accountType)) {
	case models.AccountTypeCreditCard:
		return "Cartão"
	default:
		return "Conta"
	}
}
