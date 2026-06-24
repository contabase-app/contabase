package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/admincli"
	"github.com/contabase-app/contabase/internal/auth"
	"github.com/contabase-app/contabase/internal/database"
	"github.com/contabase-app/contabase/internal/httpcookies"
	"github.com/contabase-app/contabase/internal/models"
	"github.com/contabase-app/contabase/internal/paths"
	"github.com/contabase-app/contabase/internal/repository"
	"github.com/contabase-app/contabase/internal/security"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	profileImageMaxBytes       int64 = 2 << 20
	profileMultipartMaxBytes   int64 = profileImageMaxBytes + (512 << 10)
	workspaceLogoMaxBytes      int64 = 2 << 20
	workspaceLogoMultipartMax  int64 = (2 * workspaceLogoMaxBytes) + (1 << 20)
	uploadImageValidationBytes       = 512
)

type ConfiguracoesHandler struct {
	DB                  *sql.DB
	Templates           TemplateEngine
	AuthService         *auth.Service
	WorkspaceID         string
	UserID              string
	ActorRole           string
	CanConfigRead       bool
	CanConfigWrite      bool
	BaseURL             string
	AuditEventFilter    string
	AuditSeverityFilter string
	SessionToken        string
}

type ConfiguracoesData struct {
	Title               string
	UserName            string
	UserFirstName       string
	UserInitials        string
	ProfilePhotoURL     string
	IsBusiness          bool
	ActiveWorkspaceName string
	Section             string

	Success                   string
	Error                     string
	FlashError                string
	FlashSuccess              string
	ActorRole                 string
	CanManageOps              bool
	CanManageMembers          bool
	CanManageGlobal           bool
	CanManageWorkspaceProfile bool
	Categorias                []ConfigCategoryRow
	CategoryTree              []*ConfigCategoryRow
	Contas                    []ConfigAccountRow
	Cartoes                   []ConfigCardRow
	ContasArquivadas          []ConfigAccountRow
	CartoesArquivados         []ConfigCardRow
	Workspace                 ConfigWorkspaceRow
	WorkspaceProfile          ConfigWorkspaceProfileRow
	Membros                   []ConfigMemberRow
	Perfil                    ConfigProfileRow
	AdminUsers                []AdminUserRow
	AdminWorkspaces           []AdminWorkspaceRow
	BackupTicker              string
	BackupRetention           int
	SystemWorkspaces          []ConfigWorkspaceRow
	CurrentWorkspace          string
	ProfileWorkspaces         []ConfigWorkspaceRow
	UserWorkspaces            []UserWorkspace
	AccountProviders          []AccountProviderOption
	SMTPHost                  string
	SMTPPort                  int
	SMTPUser                  string
	SMTPPass                  string
	NotificationEmail         string
	EmailPrefAuth             bool
	EmailPrefBackup           bool
	EmailPrefWorkspace        bool
	SecurityLogs              []SecurityLogRow
	AuditEventFilter          string
	AuditSeverityFilter       string
	PasswordAreaError         string
	PasswordAreaSuccess       string
}

type ConfigCategoryRow struct {
	ID                  string
	Name                string
	Type                string
	MacroGroup          string
	EffectiveMacroGroup string
	ParentID            string
	ParentName          string
	IsChild             bool
	IsBusiness          bool
	ParentOptions       []CategoryParentOption
	Children            []*ConfigCategoryRow
}

type CategoryParentOption struct {
	ID         string
	Name       string
	MacroGroup string
}

type ConfigAccountRow struct {
	ID           string
	Name         string
	Type         string
	TypeLabel    string
	Color        string
	Icon         string
	ProviderSlug string
	ProviderMark string
	Balance      string
	BalanceCents int64
	BalanceInput string
	SortOrder    int64
	Archived     bool
}

type ConfigCardRow struct {
	AccountID        string
	Name             string
	Color            string
	Icon             string
	ProviderSlug     string
	ProviderMark     string
	ClosingDay       int64
	DueDay           int64
	CreditLimit      string
	CreditLimitCents int64
	CreditLimitInput string
	SortOrder        int64
}

type ConfigWorkspaceRow struct {
	ID          string
	Name        string
	Description string
	ThemeToken  string
}

type ConfigWorkspaceProfileRow struct {
	ID           string
	Name         string
	Description  string
	Type         string
	CompanyName  string
	CNPJCPF      string
	Address      string
	Phone        string
	LogoLightURL string
	LogoDarkURL  string
}

type ConfigMemberRow struct {
	UserID string
	Name   string
	Email  string
	Role   string
	Joined string
}

type ConfigProfileRow struct {
	Name               string
	Email              string
	PhotoPath          string
	PhotoURL           string
	DefaultWorkspaceID string
	TwoFactorEnabled   bool
	UpdatedAt          int64
}

type AdminWorkspaceRoleRow struct {
	WorkspaceID       string
	WorkspaceName     string
	Role              string
	CustomPermissions []string
}

type AdminUserRow struct {
	ID                string
	Name              string
	Email             string
	Status            string
	PhotoPath         string
	PrimaryRole       string
	WorkspaceIDs      string
	WorkspaceRoles    []AdminWorkspaceRoleRow
	CustomPermissions []string
	CanBackupExport   bool
	CanContactsDelete bool
	CanWorkspaceEdit  bool
	CanReportsView    bool
	TOTPEnabled       bool
	IsAdmin           bool
}

type AdminWorkspaceRow struct {
	ID           string
	Name         string
	Description  string
	Type         string
	ThemeToken   string
	CompanyName  string
	CNPJCPF      string
	Address      string
	Phone        string
	LogoLightURL string
	LogoDarkURL  string
	Members      []ConfigMemberRow
}

type SecurityLogRow struct {
	OccurredAt  string
	EventType   string
	TargetEmail string
	Severity    string
	IPAddress   string
	Status      string
}

func (h *ConfiguracoesHandler) HandleConfiguracoesConceito(w http.ResponseWriter, r *http.Request) {
	section := normalizeConfigSection(r.URL.Query().Get("secao"), h.ActorRole)
	data, err := h.buildConfigRenderData(section, "", "", true)
	if err != nil {
		log.Printf("build configuracoes error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "configuracoes-page", data); err != nil {
		log.Printf("template configuracoes error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) HandleConfiguracoesSection(w http.ResponseWriter, r *http.Request, section string) {
	section = normalizeConfigSection(section, h.ActorRole)
	isHTMX := r.Header.Get("HX-Request") == "true"
	data, err := h.buildConfigRenderData(section, "", "", !isHTMX)
	if err != nil {
		log.Printf("build configuracoes section error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	sectionTplName := strings.ReplaceAll(section, "-", "_")
	templateName := "configuracoes-" + sectionTplName + "-content"
	if !isHTMX {
		templateName = "configuracoes-" + sectionTplName + "-page"
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template %s error: %v", templateName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) HandleAdminAuditoriaRows(w http.ResponseWriter, r *http.Request) {
	data := h.newConfigData("admin-auditoria", "", "")
	if err := h.loadAdminAuditLogs(&data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "admin-auditoria-table-rows", data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) HandlePerfilUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, profileMultipartMaxBytes)
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		h.renderConfigSectionWithFlash(w, "perfil", "Formulário inválido ou foto acima del limite permitido.", "")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	photoPath := ""
	hasPhotoUpload := false

	if name == "" || email == "" {
		h.renderConfigSectionWithFlash(w, "perfil", "Nome e e-mail são obrigatórios.", "")
		return
	}

	fileName, err := saveUploadedImageFile(r, "photo_file", paths.ProfileUploadsDir(), "profile", profileImageMaxBytes)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "perfil", err.Error(), "")
		return
	}
	if fileName != "" {
		hasPhotoUpload = true
		photoPath = "/uploads/profile/" + fileName
	}
	if strings.TrimSpace(photoPath) == "" {
		_ = h.DB.QueryRow(`SELECT COALESCE(profile_photo_path, '') FROM users WHERE id = ?`, h.UserID).Scan(&photoPath)
	}

	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if err := execOneTx(tx, `
			UPDATE users
			SET name = ?, email = ?, profile_photo_path = ?, updated_at = ?
			WHERE id = ?
`, name, email, photoPath, now, h.UserID); err != nil {
		h.renderConfigSectionWithFlash(w, "perfil", "Não foi possível atualizar o perfil.", "")
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if hasPhotoUpload {
		w.Header().Set("HX-Trigger", `{"mostrarSucesso":"Foto de perfil atualizada com sucesso."}`)
	}
	h.renderConfigSectionWithFlash(w, "perfil", "", "Perfil atualizado com sucesso.")
}

func (h *ConfiguracoesHandler) HandleWorkspaceCorporateProfileUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, workspaceLogoMultipartMax)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			h.renderConfigSectionWithFlashStatus(w, "workspace", "Formulário inválido.", "", http.StatusUnprocessableEntity)
			return
		}
	}

	if workspaceType(h.DB, h.WorkspaceID) != models.WorkspaceTypeBusiness {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "Perfil corporativo disponível apenas para workspaces Business.", "", http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	companyName := strings.TrimSpace(r.FormValue("company_name"))
	cnpjCPF := strings.TrimSpace(r.FormValue("cnpj_cpf"))
	address := strings.TrimSpace(r.FormValue("address"))
	phone := strings.TrimSpace(r.FormValue("phone"))

	if name == "" {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "Nome do workspace é obrigatório.", "", http.StatusUnprocessableEntity)
		return
	}
	if companyName == "" {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "Razão social / nome da empresa é obrigatório para workspace empresarial.", "", http.StatusUnprocessableEntity)
		return
	}
	if cnpjCPF == "" {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "CNPJ / CPF é obrigatório para workspace empresarial.", "", http.StatusUnprocessableEntity)
		return
	}

	logoLightURL := validateWorkspaceLogoFormValue(strings.TrimSpace(r.FormValue("logo_light_url")), h.WorkspaceID)
	logoDarkURL := validateWorkspaceLogoFormValue(strings.TrimSpace(r.FormValue("logo_dark_url")), h.WorkspaceID)
	if strings.TrimSpace(r.FormValue("remove_logo_light")) != "" {
		logoLightURL = ""
	}
	if strings.TrimSpace(r.FormValue("remove_logo_dark")) != "" {
		logoDarkURL = ""
	}

	if lightPath, err := saveWorkspaceLogoFile(r, "logo_light_file", h.WorkspaceID, "light"); err != nil {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "Logo claro: "+err.Error(), "", http.StatusUnprocessableEntity)
		return
	} else if lightPath != "" {
		logoLightURL = lightPath
	}
	if darkPath, err := saveWorkspaceLogoFile(r, "logo_dark_file", h.WorkspaceID, "dark"); err != nil {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "Logo escuro: "+err.Error(), "", http.StatusUnprocessableEntity)
		return
	} else if darkPath != "" {
		logoDarkURL = darkPath
	}

	result, err := h.DB.Exec(`
		UPDATE workspaces
		SET name = ?, description = ?, company_name = ?, cnpj_cpf = ?, address = ?, phone = ?, logo_light_url = ?, logo_dark_url = ?, updated_at = unixepoch()
		WHERE id = ? AND COALESCE(type, 'personal') = 'business'
	`, name, description, companyName, cnpjCPF, address, phone, logoLightURL, logoDarkURL, h.WorkspaceID)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "workspace", "Não foi possível atualizar o perfil corporativo.", "")
		return
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		h.renderConfigSectionWithFlashStatus(w, "workspace", "Perfil corporativo disponível apenas para workspaces Business.", "", http.StatusForbidden)
		return
	}

	h.renderConfigSectionWithFlash(w, "workspace", "", "Perfil corporativo atualizado com sucesso.")
}

func (h *ConfiguracoesHandler) HandlePerfilDefaultWorkspaceUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderConfigSectionWithFlash(w, "perfil", "Formulário inválido.", "")
		return
	}
	workspaceID := strings.TrimSpace(r.FormValue("default_workspace_id"))
	if workspaceID != "" {
		var count int
		if err := h.DB.QueryRow(`SELECT COUNT(1) FROM workspace_members WHERE user_id = ? AND workspace_id = ?`, h.UserID, workspaceID).Scan(&count); err != nil || count == 0 {
			h.renderConfigSectionWithFlash(w, "perfil", "Workspace padrão inválido para este usuário.", "")
			return
		}
	}
	if err := execOne(h.DB, `UPDATE users SET default_workspace_id = NULLIF(?, ''), updated_at = unixepoch() WHERE id = ?`, workspaceID, h.UserID); err != nil {
		h.renderConfigSectionWithFlash(w, "perfil", "Não foi possível salvar o workspace padrão.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "perfil", "", "Workspace padrão atualizado com sucesso.")
}

func (h *ConfiguracoesHandler) HandlePasswordChange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderPasswordChangeArea(w, "Formulário inválido.", "")
		return
	}
	currentPassword := r.FormValue("current_password")
	newPassword := r.FormValue("new_password")
	confirmPassword := r.FormValue("confirm_password")

	if currentPassword == "" {
		h.renderPasswordChangeArea(w, "Informe sua senha atual.", "")
		return
	}
	if len(newPassword) < 8 {
		h.renderPasswordChangeArea(w, "A nova senha deve ter no mínimo 8 caracteres.", "")
		return
	}
	if newPassword != confirmPassword {
		h.renderPasswordChangeArea(w, "A confirmação da senha não confere.", "")
		return
	}

	var storedHash string
	if err := h.DB.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, h.UserID).Scan(&storedHash); err != nil {
		h.renderPasswordChangeArea(w, "Erro ao verificar senha atual.", "")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(currentPassword)); err != nil {
		h.renderPasswordChangeArea(w, "Senha atual incorreta.", "")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		h.renderPasswordChangeArea(w, "Erro interno. Tente novamente.", "")
		return
	}
	now := time.Now().Unix()
	if _, err := h.DB.Exec(`
		UPDATE users
		SET password_hash = ?,
		    must_change_password = 0,
		    temporary_password_expires_at = NULL,
		    updated_at = ?
		WHERE id = ?
	`, string(hash), now, h.UserID); err != nil {
		h.renderPasswordChangeArea(w, "Não foi possível atualizar a senha.", "")
		return
	}

	var currentToken string
	if cookie, err := r.Cookie(httpcookies.Session); err == nil {
		currentToken = cookie.Value
	} else if h.SessionToken != "" {
		currentToken = h.SessionToken
	}
	if currentToken != "" {
		_ = h.AuthService.RevokeUserSessionsExcept(h.UserID, currentToken)
	} else {
		_ = h.AuthService.RevokeAllUserSessions(h.UserID)
	}

	slog.Info("password_changed", "user_id", h.UserID)
	h.renderPasswordChangeArea(w, "", "Senha alterada com sucesso. Outros dispositivos foram desconectados.")
}

func (h *ConfiguracoesHandler) renderPasswordChangeArea(w http.ResponseWriter, errMsg, okMsg string) {
	data, err := h.buildConfigData("perfil", errMsg, okMsg)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.PasswordAreaError = errMsg
	data.PasswordAreaSuccess = okMsg
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "configuracoes-perfil-password-area", data); err != nil {
		log.Printf("template configuracoes-perfil-password-area error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) HandleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var currentToken string
	if cookie, err := r.Cookie(httpcookies.Session); err == nil {
		currentToken = cookie.Value
	} else if h.SessionToken != "" {
		currentToken = h.SessionToken
	}
	if currentToken == "" {
		w.Header().Set("HX-Trigger", `{"mostrarAlerta":"Sessão inválida. Faça login novamente."}`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.AuthService.RevokeUserSessionsExcept(h.UserID, currentToken); err != nil {
		slog.Error("revoke_other_sessions_failed", "user_id", h.UserID, "error", err)
		w.Header().Set("HX-Trigger", `{"mostrarAlerta":"Não foi possível desconectar outros dispositivos. Tente novamente."}`)
		h.renderConfigSectionWithFlash(w, "perfil", "", "")
		return
	}
	slog.Info("revoke_other_sessions", "user_id", h.UserID)
	w.Header().Set("HX-Trigger", `{"mostrarSucesso":"Outros dispositivos foram desconectados com sucesso."}`)
	h.renderConfigSectionWithFlash(w, "perfil", "", "Outros dispositivos foram desconectados.")
}

func (h *ConfiguracoesHandler) HandleCategoriasCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	typ := normalizeCategoryType(r.FormValue("type"))
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	if name == "" || typ == "" {
		h.renderConfigSectionWithFlash(w, "categorias", "Nome e tipo são obrigatórios.", "")
		return
	}
	macroGroupValue := normalizeMacroGroup(r.FormValue("macro_group"))
	if custom := normalizeMacroGroup(r.FormValue("macro_group_custom")); custom != "" {
		macroGroupValue = custom
	}
	macroGroup, err := h.resolveCategoryMacroGroup(parentID, typ, macroGroupValue)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "categorias", err.Error(), "")
		return
	}
	now := time.Now().Unix()
	if _, err := h.DB.Exec(`
		INSERT INTO categories (id, workspace_id, name, icon, color, type, macro_group, parent_id, created_at)
		VALUES (?, ?, ?, 'tag', '#6b7280', ?, ?, NULLIF(?, ''), ?)
	`, uuid.NewString(), h.WorkspaceID, name, typ, macroGroup, parentID, now); err != nil {
		log.Printf("create category error: %v", err)
		h.renderConfigSectionWithFlash(w, "categorias", "Não foi possível criar a categoria.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "categorias", "", "Categoria criada com sucesso.")
}

func (h *ConfiguracoesHandler) HandleCategoriasEdit(w http.ResponseWriter, r *http.Request, id string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	typ := normalizeCategoryType(r.FormValue("type"))
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	if parentID == id {
		h.renderConfigSectionWithFlash(w, "categorias", "Categoria pai inválida.", "")
		return
	}
	if name == "" || typ == "" {
		h.renderConfigSectionWithFlash(w, "categorias", "Nome e tipo são obrigatórios.", "")
		return
	}
	macroGroupValue := normalizeMacroGroup(r.FormValue("macro_group"))
	if custom := normalizeMacroGroup(r.FormValue("macro_group_custom")); custom != "" {
		macroGroupValue = custom
	}
	macroGroup, err := h.resolveCategoryMacroGroup(parentID, typ, macroGroupValue)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "categorias", err.Error(), "")
		return
	}
	if err := execOne(h.DB, `
		UPDATE categories
		SET name = ?, type = ?, macro_group = ?, parent_id = NULLIF(?, '')
		WHERE id = ? AND workspace_id = ?
	`, name, typ, macroGroup, parentID, id, h.WorkspaceID); err != nil {
		log.Printf("edit category error: %v", err)
		h.renderConfigSectionWithFlash(w, "categorias", "Categoria não encontrada ou não autorizada.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "categorias", "", "Categoria atualizada.")
}

func (h *ConfiguracoesHandler) HandleCategoriasView(w http.ResponseWriter, r *http.Request, categoryID string) {
	h.renderCategoryRow(w, categoryID)
}

func (h *ConfiguracoesHandler) HandleCategoriasInlineForm(w http.ResponseWriter, r *http.Request, categoryID string) {
	row, err := h.queryCategoryRowByID(categoryID)
	if err != nil {
		http.Error(w, "categoria não encontrada", http.StatusNotFound)
		return
	}
	options, err := h.queryCategoryParentOptions(row.ID, row.Type, row.EffectiveMacroGroup)
	if err != nil {
		http.Error(w, "erro ao carregar categoria", http.StatusInternalServerError)
		return
	}
	row.ParentOptions = options
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = h.Templates.ExecuteTemplate(w, "config-categorias-row-edit", row)
}

func (h *ConfiguracoesHandler) HandleCategoriasInlineSave(w http.ResponseWriter, r *http.Request, categoryID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	requestedMacro := normalizeMacroGroup(r.FormValue("macro_group"))
	if custom := normalizeMacroGroup(r.FormValue("macro_group_custom")); custom != "" {
		requestedMacro = custom
	}
	if name == "" {
		http.Error(w, "nome da categoria obrigatório", http.StatusBadRequest)
		return
	}
	row, err := h.queryCategoryRowByID(categoryID)
	if err != nil {
		http.Error(w, "categoria não encontrada", http.StatusNotFound)
		return
	}
	if parentID == categoryID {
		http.Error(w, "categoria pai inválida", http.StatusBadRequest)
		return
	}
	macroGroup := ""
	if parentID != "" {
		defaultMacro := defaultMacroGroupForWorkspace(workspaceType(h.DB, h.WorkspaceID) == "business", row.Type)
		err := h.DB.QueryRow(`
			SELECT COALESCE(c.macro_group, p.macro_group, ?)
			FROM categories c
			LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
			WHERE c.id = ? AND c.workspace_id = ? AND c.parent_id IS NULL AND c.type = ?
		`, defaultMacro, parentID, h.WorkspaceID, row.Type).Scan(&macroGroup)
		if err == sql.ErrNoRows {
			http.Error(w, "categoria pai inválida", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "erro ao validar a categoria pai", http.StatusInternalServerError)
			return
		}
		if macroGroup != row.EffectiveMacroGroup {
			http.Error(w, "categoria pai deve ter o mesmo grupo macro", http.StatusBadRequest)
			return
		}
	} else {
		if requestedMacro == "" {
			macroGroup = defaultMacroGroupForWorkspace(workspaceType(h.DB, h.WorkspaceID) == "business", row.Type)
		} else {
			isBiz := workspaceType(h.DB, h.WorkspaceID) == "business"
			if !isMacroGroupValidForType(isBiz, requestedMacro, row.Type) {
				http.Error(w, "grupo macro inválido para este tipo de categoria", http.StatusBadRequest)
				return
			}
			macroGroup = requestedMacro
		}
	}
	if err := execOne(h.DB, `
		UPDATE categories
		SET name = ?, parent_id = NULLIF(?, ''), macro_group = NULLIF(?, '')
		WHERE id = ? AND workspace_id = ?
	`, name, parentID, macroGroup, categoryID, h.WorkspaceID); err != nil {
		http.Error(w, "categoria não encontrada", http.StatusNotFound)
		return
	}
	h.renderCategoryRow(w, categoryID)
}

func (h *ConfiguracoesHandler) HandleCategoriasDelete(w http.ResponseWriter, r *http.Request, id string) {
	if err := execOne(h.DB, `DELETE FROM categories WHERE id = ? AND workspace_id = ?`, id, h.WorkspaceID); err != nil {
		if strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY") {
			h.renderConfigSectionWithFlash(w, "categorias", "Categoria possui lançamentos vinculados e não pode ser removida.", "")
			return
		}
		log.Printf("delete category error: %v", err)
		h.renderConfigSectionWithFlash(w, "categorias", "Categoria não encontrada ou não autorizada.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "categorias", "", "Categoria removida.")
}

func (h *ConfiguracoesHandler) HandleContasCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name, providerSlug, color, providerErr := resolveAccountProviderInput(
		r.FormValue("provider_slug"),
		r.FormValue("provider_custom_name"),
		r.FormValue("color"),
	)
	if providerErr != nil {
		h.renderConfigSectionWithFlash(w, "contas", "Selecione um banco válido e informe os dados da conta personalizada.", "")
		return
	}
	accType := normalizeAccountType(r.FormValue("type"))
	initial, err := parseCurrency(r.FormValue("initial_balance"))
	if err != nil {
		initial = 0
	}
	icon := normalizeIconName(r.FormValue("icon"))
	if icon == "" {
		icon = accountVisualByProvider(providerSlug, accType)
	}
	if name == "" || accType == "" {
		h.renderConfigSectionWithFlash(w, "contas", "Nome e tipo da conta são obrigatórios.", "")
		return
	}
	now := time.Now().Unix()
	if _, err := h.DB.Exec(`
		INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, initial_balance, current_balance, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT MAX(sort_order) + 1 FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD'), 0), ?, ?)
	`, uuid.NewString(), h.WorkspaceID, name, accType, color, icon, providerSlug, initial, initial, h.WorkspaceID, now, now); err != nil {
		log.Printf("create account error: %v", err)
		h.renderConfigSectionWithFlash(w, "contas", "Não foi possível criar a conta.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "contas", "", "Conta criada com sucesso.")
}

func (h *ConfiguracoesHandler) HandleContasEdit(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		h.renderConfigSectionWithFlash(w, "contas", "Nome da conta é obrigatório.", "")
		return
	}
	if err := execOne(h.DB, `
		UPDATE accounts
		SET name = ?, updated_at = unixepoch()
		WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD'
	`, name, accountID, h.WorkspaceID); err != nil {
		h.renderConfigSectionWithFlash(w, "contas", "Conta não encontrada ou sem permissão.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "contas", "", "Conta atualizada com sucesso.")
}

func (h *ConfiguracoesHandler) HandleCartoesCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name, providerSlug, color, providerErr := resolveAccountProviderInput(
		r.FormValue("provider_slug"),
		r.FormValue("provider_custom_name"),
		r.FormValue("color"),
	)
	if providerErr != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Selecione um banco válido e informe os dados do cartão personalizado.", "")
		return
	}
	creditLimit, err := parseCurrency(r.FormValue("credit_limit"))
	if err != nil || creditLimit < 0 {
		h.renderConfigSectionWithFlash(w, "cartoes", "Limite inválido.", "")
		return
	}
	closingDay, err := parseIntRange(r.FormValue("closing_day"), 1, 31)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Dia de fechamento inválido.", "")
		return
	}
	dueDay, err := parseIntRange(r.FormValue("due_day"), 1, 31)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Dia de vencimento inválido.", "")
		return
	}
	icon := normalizeIconName(r.FormValue("icon"))
	if icon == "" {
		icon = accountVisualByProvider(providerSlug, "CREDIT_CARD")
	}
	if name == "" {
		h.renderConfigSectionWithFlash(w, "cartoes", "Nome do cartão é obrigatório.", "")
		return
	}
	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	accountID := uuid.NewString()
	if err := execOneTx(tx, `
		INSERT INTO accounts (id, workspace_id, name, type, color, icon, provider_slug, initial_balance, current_balance, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, 'CREDIT_CARD', ?, ?, ?, 0, 0, COALESCE((SELECT MAX(sort_order) + 1 FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD'), 0), ?, ?)
	`, accountID, h.WorkspaceID, name, color, icon, providerSlug, h.WorkspaceID, now, now); err != nil {
		log.Printf("create card account error: %v", err)
		h.renderConfigSectionWithFlash(w, "cartoes", "Não foi possível criar conta do cartão.", "")
		return
	}
	if err := execOneTx(tx, `
		INSERT INTO credit_cards (id, account_id, closing_day, due_day, credit_limit)
		VALUES (?, ?, ?, ?, ?)
	`, uuid.NewString(), accountID, closingDay, dueDay, creditLimit); err != nil {
		log.Printf("create credit card error: %v", err)
		h.renderConfigSectionWithFlash(w, "cartoes", "Não foi possível criar dados do cartão.", "")
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigSectionWithFlash(w, "cartoes", "", "Cartão criado com sucesso.")
}

func (h *ConfiguracoesHandler) HandleCartoesEdit(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	creditLimit, err := parseCurrency(r.FormValue("credit_limit"))
	if err != nil || creditLimit < 0 {
		h.renderConfigSectionWithFlash(w, "cartoes", "Limite inválido.", "")
		return
	}
	closingDay, err := parseIntRange(r.FormValue("closing_day"), 1, 31)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Dia de fechamento inválido.", "")
		return
	}
	dueDay, err := parseIntRange(r.FormValue("due_day"), 1, 31)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Dia de vencimento inválido.", "")
		return
	}
	if name == "" {
		h.renderConfigSectionWithFlash(w, "cartoes", "Nome do cartão é obrigatório.", "")
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := execOneTx(tx, `
		UPDATE accounts
		SET name = ?, updated_at = unixepoch()
		WHERE id = ? AND workspace_id = ? AND type = 'CREDIT_CARD'
	`, name, accountID, h.WorkspaceID); err != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Cartão não encontrado ou sem permissão.", "")
		return
	}
	result, err := tx.Exec(`
		UPDATE credit_cards
		SET closing_day = ?, due_day = ?, credit_limit = ?
		WHERE account_id = ?
	`, closingDay, dueDay, creditLimit, accountID)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "cartoes", "Não foi possível atualizar o cartão.", "")
		return
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		h.renderConfigSectionWithFlash(w, "cartoes", "Cartão sem configuração interna. Recrie ou repare este cartão.", "")
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigSectionWithFlash(w, "cartoes", "", "Cartão atualizado com sucesso.")
}

func (h *ConfiguracoesHandler) HandleContasView(w http.ResponseWriter, r *http.Request, accountID string) {
	h.renderAccountRow(w, accountID)
}

func (h *ConfiguracoesHandler) HandleContasInlineForm(w http.ResponseWriter, r *http.Request, accountID string) {
	var row ConfigAccountRow
	err := h.DB.QueryRow(`
		SELECT id, name, type, COALESCE(NULLIF(color, ''), '#6B7280'), COALESCE(NULLIF(icon, ''), ''), COALESCE(NULLIF(provider_slug, ''), 'custom'), current_balance
		FROM accounts
		WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD'
	`, accountID, h.WorkspaceID).Scan(&row.ID, &row.Name, &row.Type, &row.Color, &row.Icon, &row.ProviderSlug, &row.BalanceCents)
	if err != nil {
		http.Error(w, "conta não encontrada", http.StatusNotFound)
		return
	}
	row.Balance = formatCurrencyLabel(row.BalanceCents)
	row.BalanceInput = centsToInput(row.BalanceCents)
	row.TypeLabel = models.AccountTypeLabel(row.Type)
	row.Color = normalizeHexColor(row.Color, "#6B7280")
	row.ProviderSlug = normalizeAccountProviderSlug(row.ProviderSlug)
	row.ProviderMark = accountProviderMark(row.ProviderSlug, row.Name)
	if row.Icon == "" {
		row.Icon = accountVisualByProvider(row.ProviderSlug, row.Type)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.Templates.ExecuteTemplate(w, "config-contas-row-edit", row)
}

func (h *ConfiguracoesHandler) HandleContasInlineSave(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	accType := normalizeAccountType(r.FormValue("type"))
	balance, err := parseCurrency(r.FormValue("balance"))
	if err != nil {
		balance = 0
	}
	color := normalizeHexColor(r.FormValue("color"), "#6B7280")
	icon := normalizeIconName(r.FormValue("icon"))
	if name == "" || accType == "" {
		http.Error(w, "nome e tipo obrigatórios", http.StatusBadRequest)
		return
	}
	if err := execOne(h.DB, `
		UPDATE accounts
		SET name = ?, type = ?, current_balance = ?, color = ?, icon = ?, updated_at = unixepoch()
		WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD'
	`, name, accType, balance, color, icon, accountID, h.WorkspaceID); err != nil {
		http.Error(w, "conta não encontrada", http.StatusNotFound)
		return
	}
	h.renderAccountRow(w, accountID)
}

func (h *ConfiguracoesHandler) HandleCartoesView(w http.ResponseWriter, r *http.Request, accountID string) {
	h.renderCardRow(w, accountID)
}

func (h *ConfiguracoesHandler) HandleCartoesInlineForm(w http.ResponseWriter, r *http.Request, accountID string) {
	var row ConfigCardRow
	err := h.DB.QueryRow(`
		SELECT a.id, a.name, COALESCE(NULLIF(a.color, ''), '#6B7280'), COALESCE(NULLIF(a.icon, ''), ''), COALESCE(NULLIF(a.provider_slug, ''), 'custom'), cc.closing_day, cc.due_day, cc.credit_limit
		FROM accounts a
		JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.id = ? AND a.workspace_id = ? AND a.type = 'CREDIT_CARD'
	`, accountID, h.WorkspaceID).Scan(&row.AccountID, &row.Name, &row.Color, &row.Icon, &row.ProviderSlug, &row.ClosingDay, &row.DueDay, &row.CreditLimitCents)
	if err != nil {
		http.Error(w, "cartão não encontrado", http.StatusNotFound)
		return
	}
	row.CreditLimit = formatCurrencyLabel(row.CreditLimitCents)
	row.CreditLimitInput = centsToInput(row.CreditLimitCents)
	row.Color = normalizeHexColor(row.Color, "#6B7280")
	row.ProviderSlug = normalizeAccountProviderSlug(row.ProviderSlug)
	row.ProviderMark = accountProviderMark(row.ProviderSlug, row.Name)
	if row.Icon == "" {
		row.Icon = accountVisualByProvider(row.ProviderSlug, "CREDIT_CARD")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.Templates.ExecuteTemplate(w, "config-cartoes-row-edit", row)
}

func (h *ConfiguracoesHandler) HandleCartoesInlineSave(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	creditLimit, err := parseCurrency(r.FormValue("credit_limit"))
	if err != nil || creditLimit < 0 {
		http.Error(w, "limite inválido", http.StatusBadRequest)
		return
	}
	closingDay, err := parseIntRange(r.FormValue("closing_day"), 1, 31)
	if err != nil {
		http.Error(w, "dia de fechamento inválido", http.StatusBadRequest)
		return
	}
	dueDay, err := parseIntRange(r.FormValue("due_day"), 1, 31)
	if err != nil {
		http.Error(w, "dia de vencimento inválido", http.StatusBadRequest)
		return
	}
	color := normalizeHexColor(r.FormValue("color"), "#6B7280")
	icon := normalizeIconName(r.FormValue("icon"))
	if name == "" {
		http.Error(w, "nome do cartão obrigatório", http.StatusBadRequest)
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := execOneTx(tx, `
		UPDATE accounts
		SET name = ?, color = ?, icon = ?, updated_at = unixepoch()
		WHERE id = ? AND workspace_id = ? AND type = 'CREDIT_CARD'
	`, name, color, icon, accountID, h.WorkspaceID); err != nil {
		http.Error(w, "cartão não encontrado", http.StatusNotFound)
		return
	}
	result, err := tx.Exec(`
		UPDATE credit_cards
		SET closing_day = ?, due_day = ?, credit_limit = ?
		WHERE account_id = ?
	`, closingDay, dueDay, creditLimit, accountID)
	if err != nil {
		http.Error(w, "não foi possível atualizar o cartão", http.StatusInternalServerError)
		return
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		http.Error(w, "cartão sem configuração interna — recrie ou repare este cartão", http.StatusUnprocessableEntity)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderCardRow(w, accountID)
}

func (h *ConfiguracoesHandler) HandleContaArchive(w http.ResponseWriter, r *http.Request, accountID string) {
	repo := repository.NewConfigRepository(h.DB)
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := repo.ArchiveAccountTx(tx, h.WorkspaceID, accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "conta não encontrada ou já arquivada", http.StatusNotFound)
			return
		}
		http.Error(w, "não foi possível arquivar conta", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigContasSection(w, r, "Conta arquivada com sucesso.")
}

func (h *ConfiguracoesHandler) HandleContaUnarchive(w http.ResponseWriter, r *http.Request, accountID string) {
	repo := repository.NewConfigRepository(h.DB)
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := repo.UnarchiveAccountTx(tx, h.WorkspaceID, accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "conta não encontrada ou já ativa", http.StatusNotFound)
			return
		}
		http.Error(w, "não foi possível reativar conta", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigContasSection(w, r, "Conta reativada com sucesso.")
}

func (h *ConfiguracoesHandler) HandleCartaoArchive(w http.ResponseWriter, r *http.Request, accountID string) {
	repo := repository.NewConfigRepository(h.DB)
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := repo.ArchiveAccountTx(tx, h.WorkspaceID, accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "cartão não encontrado ou já arquivado", http.StatusNotFound)
			return
		}
		http.Error(w, "não foi possível arquivar cartão", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigCartoesSection(w, r, "Cartão arquivado com sucesso. Faturas e histórico permanecem mantidos.")
}

func (h *ConfiguracoesHandler) HandleCartaoUnarchive(w http.ResponseWriter, r *http.Request, accountID string) {
	repo := repository.NewConfigRepository(h.DB)
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	if err := repo.UnarchiveAccountTx(tx, h.WorkspaceID, accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "cartão não encontrado ou já ativo", http.StatusNotFound)
			return
		}
		http.Error(w, "não foi possível reativar cartão", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigCartoesSection(w, r, "Cartão reativado com sucesso.")
}

func (h *ConfiguracoesHandler) HandleContasReorder(w http.ResponseWriter, r *http.Request, accountID, direction string) {
	if direction != "up" && direction != "down" {
		http.Error(w, "direção inválida", http.StatusBadRequest)
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var sortOrder int64
	err = tx.QueryRow(`SELECT sort_order FROM accounts WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL`, accountID, h.WorkspaceID).Scan(&sortOrder)
	if err != nil {
		h.renderConfigContasSection(w, r, "")
		return
	}
	var adjacentID string
	var adjacentSort int64
	if direction == "up" {
		err = tx.QueryRow(`SELECT id, sort_order FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL AND sort_order < ? ORDER BY sort_order DESC, name DESC LIMIT 1`, h.WorkspaceID, sortOrder).Scan(&adjacentID, &adjacentSort)
	} else {
		err = tx.QueryRow(`SELECT id, sort_order FROM accounts WHERE workspace_id = ? AND type != 'CREDIT_CARD' AND archived_at IS NULL AND sort_order > ? ORDER BY sort_order ASC, name ASC LIMIT 1`, h.WorkspaceID, sortOrder).Scan(&adjacentID, &adjacentSort)
	}
	if err != nil {
		h.renderConfigContasSection(w, r, "")
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET sort_order = ? WHERE id = ?`, adjacentSort, accountID); err != nil {
		h.renderConfigContasSection(w, r, "")
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET sort_order = ? WHERE id = ?`, sortOrder, adjacentID); err != nil {
		h.renderConfigContasSection(w, r, "")
		return
	}
	if err := tx.Commit(); err != nil {
		h.renderConfigContasSection(w, r, "")
		return
	}
	h.renderConfigContasSection(w, r, "")
}

func (h *ConfiguracoesHandler) HandleCartoesReorder(w http.ResponseWriter, r *http.Request, accountID, direction string) {
	if direction != "up" && direction != "down" {
		http.Error(w, "direção inválida", http.StatusBadRequest)
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var sortOrder int64
	err = tx.QueryRow(`SELECT sort_order FROM accounts WHERE id = ? AND workspace_id = ? AND type = 'CREDIT_CARD' AND archived_at IS NULL`, accountID, h.WorkspaceID).Scan(&sortOrder)
	if err != nil {
		h.renderConfigCartoesSection(w, r, "")
		return
	}
	var adjacentID string
	var adjacentSort int64
	if direction == "up" {
		err = tx.QueryRow(`SELECT id, sort_order FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD' AND archived_at IS NULL AND sort_order < ? ORDER BY sort_order DESC, name DESC LIMIT 1`, h.WorkspaceID, sortOrder).Scan(&adjacentID, &adjacentSort)
	} else {
		err = tx.QueryRow(`SELECT id, sort_order FROM accounts WHERE workspace_id = ? AND type = 'CREDIT_CARD' AND archived_at IS NULL AND sort_order > ? ORDER BY sort_order ASC, name ASC LIMIT 1`, h.WorkspaceID, sortOrder).Scan(&adjacentID, &adjacentSort)
	}
	if err != nil {
		h.renderConfigCartoesSection(w, r, "")
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET sort_order = ? WHERE id = ?`, adjacentSort, accountID); err != nil {
		h.renderConfigCartoesSection(w, r, "")
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET sort_order = ? WHERE id = ?`, sortOrder, adjacentID); err != nil {
		h.renderConfigCartoesSection(w, r, "")
		return
	}
	if err := tx.Commit(); err != nil {
		h.renderConfigCartoesSection(w, r, "")
		return
	}
	h.renderConfigCartoesSection(w, r, "")
}

func (h *ConfiguracoesHandler) renderConfigContasSection(w http.ResponseWriter, r *http.Request, okMsg string) {
	isHTMX := r.Header.Get("HX-Request") == "true"
	data, err := h.buildConfigRenderData("contas", "", okMsg, !isHTMX)
	if err != nil {
		log.Printf("build config contas section error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	templateName := "configuracoes-contas-content"
	if !isHTMX {
		templateName = "configuracoes-contas-page"
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template %s error: %v", templateName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) renderConfigCartoesSection(w http.ResponseWriter, r *http.Request, okMsg string) {
	isHTMX := r.Header.Get("HX-Request") == "true"
	data, err := h.buildConfigRenderData("cartoes", "", okMsg, !isHTMX)
	if err != nil {
		log.Printf("build config cartoes section error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	templateName := "configuracoes-cartoes-content"
	if !isHTMX {
		templateName = "configuracoes-cartoes-page"
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template %s error: %v", templateName, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) renderAccountRow(w http.ResponseWriter, accountID string) {
	var row ConfigAccountRow
	err := h.DB.QueryRow(`
		SELECT id, name, type, COALESCE(NULLIF(color, ''), '#6B7280'), COALESCE(NULLIF(icon, ''), ''), COALESCE(NULLIF(provider_slug, ''), 'custom'), current_balance
		FROM accounts
		WHERE id = ? AND workspace_id = ? AND type != 'CREDIT_CARD'
	`, accountID, h.WorkspaceID).Scan(&row.ID, &row.Name, &row.Type, &row.Color, &row.Icon, &row.ProviderSlug, &row.BalanceCents)
	if err != nil {
		http.Error(w, "conta não encontrada", http.StatusNotFound)
		return
	}
	row.Balance = formatCurrencyLabel(row.BalanceCents)
	row.BalanceInput = centsToInput(row.BalanceCents)
	row.TypeLabel = models.AccountTypeLabel(row.Type)
	row.Color = normalizeHexColor(row.Color, "#6B7280")
	row.ProviderSlug = normalizeAccountProviderSlug(row.ProviderSlug)
	row.ProviderMark = accountProviderMark(row.ProviderSlug, row.Name)
	if row.Icon == "" {
		row.Icon = accountVisualByProvider(row.ProviderSlug, row.Type)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.Templates.ExecuteTemplate(w, "config-contas-row", row)
}

func (h *ConfiguracoesHandler) renderCardRow(w http.ResponseWriter, accountID string) {
	var row ConfigCardRow
	err := h.DB.QueryRow(`
		SELECT a.id, a.name, COALESCE(NULLIF(a.color, ''), '#6B7280'), COALESCE(NULLIF(a.icon, ''), ''), COALESCE(NULLIF(a.provider_slug, ''), 'custom'), cc.closing_day, cc.due_day, cc.credit_limit
		FROM accounts a
		JOIN credit_cards cc ON cc.account_id = a.id
		WHERE a.id = ? AND a.workspace_id = ? AND a.type = 'CREDIT_CARD'
	`, accountID, h.WorkspaceID).Scan(&row.AccountID, &row.Name, &row.Color, &row.Icon, &row.ProviderSlug, &row.ClosingDay, &row.DueDay, &row.CreditLimitCents)
	if err != nil {
		http.Error(w, "cartão não encontrado", http.StatusNotFound)
		return
	}
	row.CreditLimit = formatCurrencyLabel(row.CreditLimitCents)
	row.CreditLimitInput = centsToInput(row.CreditLimitCents)
	row.Color = normalizeHexColor(row.Color, "#6B7280")
	row.ProviderSlug = normalizeAccountProviderSlug(row.ProviderSlug)
	row.ProviderMark = accountProviderMark(row.ProviderSlug, row.Name)
	if row.Icon == "" {
		row.Icon = accountVisualByProvider(row.ProviderSlug, "CREDIT_CARD")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.Templates.ExecuteTemplate(w, "config-cartoes-row", row)
}

func (h *ConfiguracoesHandler) renderCategoryRow(w http.ResponseWriter, categoryID string) {
	row, err := h.queryCategoryRowByID(categoryID)
	if err != nil {
		http.Error(w, "categoria não encontrada", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if row.IsChild {
		_ = h.Templates.ExecuteTemplate(w, "config-categorias-row", row)
	} else {
		children := h.queryCategoryChildren(categoryID, row.IsBusiness)
		row.Children = children
		_ = h.Templates.ExecuteTemplate(w, "config-categorias-tree-node", row)
	}
}

func (h *ConfiguracoesHandler) queryCategoryChildren(parentID string, isBusiness bool) []*ConfigCategoryRow {
	rows, err := h.DB.Query(`
		SELECT c.id, c.name, c.type, COALESCE(c.macro_group, ''), COALESCE(c.parent_id, ''), COALESCE(p.name, '')
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.workspace_id = ? AND c.parent_id = ?
		ORDER BY c.name
	`, h.WorkspaceID, parentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var children []*ConfigCategoryRow
	for rows.Next() {
		var child ConfigCategoryRow
		var parentName string
		if err := rows.Scan(&child.ID, &child.Name, &child.Type, &child.MacroGroup, &child.ParentID, &parentName); err != nil {
			return nil
		}
		child.ParentName = parentName
		child.IsChild = true
		child.IsBusiness = isBusiness
		children = append(children, &child)
	}
	return children
}

func (h *ConfiguracoesHandler) queryCategoryRowByID(categoryID string) (ConfigCategoryRow, error) {
	var row ConfigCategoryRow
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	defaultIncomeMacro := defaultMacroGroupForWorkspace(isBusiness, "INCOME")
	defaultExpenseMacro := defaultMacroGroupForWorkspace(isBusiness, "EXPENSE")
	err := h.DB.QueryRow(`
		SELECT
			c.id,
			c.name,
			c.type,
			COALESCE(c.macro_group, ''),
			COALESCE(
				CASE WHEN c.parent_id IS NULL OR c.parent_id = '' THEN c.macro_group ELSE p.macro_group END,
				CASE WHEN c.type = 'INCOME' THEN ? ELSE ? END
			) AS effective_macro_group,
			COALESCE(c.parent_id, ''),
			COALESCE(p.name, '')
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.id = ? AND c.workspace_id = ?
	`, defaultIncomeMacro, defaultExpenseMacro, categoryID, h.WorkspaceID).Scan(
		&row.ID,
		&row.Name,
		&row.Type,
		&row.MacroGroup,
		&row.EffectiveMacroGroup,
		&row.ParentID,
		&row.ParentName,
	)
	if err != nil {
		return row, err
	}
	row.IsChild = row.ParentID != ""
	row.IsBusiness = isBusiness
	return row, nil
}

func (h *ConfiguracoesHandler) queryCategoryParentOptions(currentID, typ, macroGroup string) ([]CategoryParentOption, error) {
	rows, err := h.DB.Query(`
		SELECT id, name
		FROM categories
		WHERE workspace_id = ? AND type = ? AND parent_id IS NULL AND id != ?
		ORDER BY name
	`, h.WorkspaceID, typ, currentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CategoryParentOption
	for rows.Next() {
		var item CategoryParentOption
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (h *ConfiguracoesHandler) HandleWorkspaceInviteMember(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := normalizeWorkspaceRole(r.FormValue("role"), h.ActorRole)
	if email == "" || role == "" {
		h.renderConfigSectionWithFlash(w, "workspace", "E-mail e permissão são obrigatórios.", "")
		return
	}

	workspaceIDs := collectWorkspaceIDs(r, h.WorkspaceID)
	if len(workspaceIDs) == 0 {
		h.renderConfigSectionWithFlash(w, "workspace", "Selecione ao menos um workspace.", "")
		return
	}

	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	var existingUserID string
	err = tx.QueryRow(`SELECT id FROM users WHERE lower(email) = ?`, email).Scan(&existingUserID)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("query user by email error: %v", err)
		h.renderConfigSectionWithFlash(w, "workspace", "Não foi possível adicionar membro.", "")
		return
	}
	isNewUser := false
	if existingUserID == "" {
		existingUserID = uuid.NewString()
		isNewUser = true
		name := defaultNameFromEmail(email)
		if err := execOneTx(tx, `
			INSERT INTO users (id, name, email, password_hash, status, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 'pending', ?, ?)
		`, existingUserID, name, email, now, now); err != nil {
			log.Printf("create invited user error: %v", err)
			h.renderConfigSectionWithFlash(w, "workspace", "Não foi possível criar usuário convidado.", "")
			return
		}
	}

	for _, workspaceID := range workspaceIDs {
		var actorRole string
		if err := tx.QueryRow(`
			SELECT role FROM workspace_members
			WHERE workspace_id = ? AND user_id = ?
		`, workspaceID, h.UserID).Scan(&actorRole); err != nil {
			if err == sql.ErrNoRows {
				h.renderConfigSectionWithFlash(w, "workspace", "Você não tem permissão para convidar membros neste workspace.", "")
				return
			}
			log.Printf("check inviter workspace role error: %v", err)
			h.renderConfigSectionWithFlash(w, "workspace", "Não foi possível validar as permissões de convite.", "")
			return
		}
		if actorRole != models.RoleManager && actorRole != models.RoleAdmin {
			h.renderConfigSectionWithFlash(w, "workspace", "Você não tem permissão para convidar membros neste workspace.", "")
			return
		}
		if _, err := tx.Exec(`
			INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role
		`, workspaceID, existingUserID, role, now); err != nil {
			log.Printf("upsert workspace member error: %v", err)
			h.renderConfigSectionWithFlash(w, "workspace", "Não foi possível adicionar membro.", "")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if h.AuthService != nil && (isNewUser || role != "") {
		_, err := h.AuthService.SetActivationTokenByEmail(email, 48*time.Hour)
		if err != nil {
			log.Printf("set activation token error: %v", err)
		}
	}
	h.renderConfigSectionWithFlash(w, "workspace", "", "Membro adicionado/atualizado com sucesso.")
}

func (h *ConfiguracoesHandler) HandleAdminUsersSave(w http.ResponseWriter, r *http.Request) {
	if h.ActorRole != models.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Formulário inválido.", "")
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := normalizeWorkspaceRole(r.FormValue("role"), models.RoleAdmin)
	status := strings.ToLower(strings.TrimSpace(r.FormValue("status")))
	if status != "pending" {
		status = "active"
	}
	workspaceIDs := collectWorkspaceIDs(r)
	customPermissions := normalizeCustomPermissions(r.Form["custom_permissions"])

	if name == "" || email == "" || role == "" || len(workspaceIDs) == 0 {
		h.renderConfigSectionWithFlash(w, "admin-users", "Preencha nome, e-mail, permissão e selecione os workspaces.", "")
		return
	}

	now := time.Now().Unix()
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if userID == "" {
		userID = uuid.NewString()
		userStatus := "pending"
		if err := execOneTx(tx, `
			INSERT INTO users (id, name, email, password_hash, profile_photo_path, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, '', ?, ?, ?)
		`, userID, name, email, "pending", userStatus, now, now); err != nil {
			h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível criar o usuário.", "")
			return
		}
	} else {
		if _, err := tx.Exec(`
			UPDATE users
			SET name = ?, email = ?, status = ?, updated_at = ?
			WHERE id = ?
		`, name, email, status, now, userID); err != nil {
			h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível atualizar o usuário.", "")
			return
		}
	}

	if _, err := tx.Exec(`DELETE FROM workspace_members WHERE user_id = ?`, userID); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível atualizar os vínculos.", "")
		return
	}
	for _, workspaceID := range workspaceIDs {
		if _, err := tx.Exec(`
			INSERT INTO workspace_members (workspace_id, user_id, role, custom_permissions, joined_at)
			VALUES (?, ?, ?, ?, ?)
		`, workspaceID, userID, role, models.PermissionListToJSON(customPermissions), now); err != nil {
			h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível atualizar os vínculos de workspace.", "")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigSectionWithFlash(w, "admin-users", "", "Usuário salvo com sucesso.")
}

func (h *ConfiguracoesHandler) HandleAdminUsersResetPassword(w http.ResponseWriter, r *http.Request) {
	if h.ActorRole != models.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Formulário inválido.", "")
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	target, err := h.lookupAdminRecoveryTarget(userID)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Usuário não encontrado.", "")
		return
	}
	if target.IsAdmin {
		confirmation := strings.ToLower(strings.TrimSpace(r.FormValue("confirm_email")))
		if confirmation != target.Email {
			h.renderConfigSectionWithFlash(w, "admin-users", "Para gerar senha temporária de ADMIN, digite o e-mail do usuário exatamente.", "")
			return
		}
	}
	result, err := admincli.ResetUserTemporaryPassword(h.DB, target.Email, admincli.TemporaryPasswordTTL, admincli.RecoveryAudit{
		EventType: "ADMIN_USER_TEMPORARY_PASSWORD_RESET",
		IP:        "admin-panel",
		UserAgent: "admin-panel",
		Metadata: map[string]string{
			"actor_user_id": h.UserID,
			"method":        "admin_panel",
			"target_role":   target.PrimaryRole,
		},
	})
	if err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível gerar a senha temporária.", "")
		return
	}
	success := fmt.Sprintf("Senha temporária gerada para %s: %s. Copie agora. Esta senha não será exibida novamente. Expira em 1 hora e exigirá troca no próximo login.", result.Email, result.TemporaryPassword)
	h.renderConfigSectionWithFlashStatus(w, "admin-users", "", success, http.StatusOK)
}

func (h *ConfiguracoesHandler) HandleAdminUsersDisable2FA(w http.ResponseWriter, r *http.Request) {
	if h.ActorRole != models.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Formulário inválido.", "")
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	target, err := h.lookupAdminRecoveryTarget(userID)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Usuário não encontrado.", "")
		return
	}
	if target.IsAdmin {
		confirmation := strings.ToLower(strings.TrimSpace(r.FormValue("confirm_email")))
		if confirmation != target.Email {
			h.renderConfigSectionWithFlash(w, "admin-users", "Para desativar 2FA de ADMIN, digite o e-mail do usuário exatamente.", "")
			return
		}
	}
	result, err := admincli.DisableUser2FAWithAudit(h.DB, target.Email, admincli.RecoveryAudit{
		EventType: "ADMIN_USER_2FA_DISABLED",
		IP:        "admin-panel",
		UserAgent: "admin-panel",
		Metadata: map[string]string{
			"actor_user_id": h.UserID,
			"method":        "admin_panel",
			"target_role":   target.PrimaryRole,
		},
	})
	if err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível desativar o 2FA do usuário.", "")
		return
	}
	if result.WasEnabled {
		h.renderConfigSectionWithFlash(w, "admin-users", "", "2FA desativado e sessões revogadas com sucesso.")
		return
	}
	h.renderConfigSectionWithFlash(w, "admin-users", "", "2FA já estava desativado; sessões foram revogadas.")
}

func (h *ConfiguracoesHandler) HandleAdminUsersRevokeSessions(w http.ResponseWriter, r *http.Request) {
	if h.ActorRole != models.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Formulário inválido.", "")
		return
	}
	userID := strings.TrimSpace(r.FormValue("user_id"))
	target, err := h.lookupAdminRecoveryTarget(userID)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Usuário não encontrado.", "")
		return
	}
	if target.IsAdmin {
		confirmation := strings.ToLower(strings.TrimSpace(r.FormValue("confirm_email")))
		if confirmation != target.Email {
			h.renderConfigSectionWithFlash(w, "admin-users", "Para revogar sessões de ADMIN, digite o e-mail do usuário exatamente.", "")
			return
		}
	}
	if _, err := admincli.RevokeUserAuthStateWithAudit(h.DB, target.Email, admincli.RecoveryAudit{
		EventType: "ADMIN_USER_SESSIONS_REVOKED",
		IP:        "admin-panel",
		UserAgent: "admin-panel",
		Metadata: map[string]string{
			"actor_user_id": h.UserID,
			"method":        "admin_panel",
			"target_role":   target.PrimaryRole,
		},
	}); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível revogar sessões do usuário.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "admin-users", "", "Sessões e desafios 2FA pendentes foram revogados.")
}

func (h *ConfiguracoesHandler) HandleAdminAuditoriaSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-auditoria", "Formulário inválido.", "")
		return
	}

	smtpHost := strings.TrimSpace(r.FormValue("smtp_host"))
	smtpUser := strings.TrimSpace(r.FormValue("smtp_user"))
	smtpPass := strings.TrimSpace(r.FormValue("smtp_pass"))
	notificationEmail := strings.TrimSpace(r.FormValue("notification_email"))
	encryptedSMTPPass := ""

	smtpPort := int64(587)
	if rawPort := strings.TrimSpace(r.FormValue("smtp_port")); rawPort != "" {
		parsedPort, err := parseIntRange(rawPort, 1, 65535)
		if err != nil {
			h.renderConfigSectionWithFlash(w, "admin-auditoria", "Porta SMTP inválida.", "")
			return
		}
		smtpPort = parsedPort
	}

	preferences := normalizeAuditPreferences(r.Form["email_preferences"])
	prefsRaw, err := json.Marshal(preferences)
	if err != nil {
		h.renderConfigSectionWithFlash(w, "admin-auditoria", "Não foi possível salvar as preferências.", "")
		return
	}
	if smtpPass != "" {
		encryptedSMTPPass, err = security.Encrypt(smtpPass)
		if err != nil {
			h.renderConfigSectionWithFlash(w, "admin-auditoria", "Não foi possível proteger a senha SMTP.", "")
			return
		}
	}

	if err := execOne(h.DB, `
		UPDATE workspaces
		SET smtp_host = NULLIF(?, ''),
			smtp_port = ?,
			smtp_user = NULLIF(?, ''),
			smtp_pass = NULLIF(?, ''),
			notification_email = NULLIF(?, ''),
			email_preferences = ?,
			updated_at = unixepoch()
		WHERE id = ?
	`, smtpHost, smtpPort, smtpUser, encryptedSMTPPass, notificationEmail, string(prefsRaw), h.WorkspaceID); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-auditoria", "Não foi possível salvar as configurações de auditoria.", "")
		return
	}

	h.renderConfigSectionWithFlash(w, "admin-auditoria", "", "Configurações de auditoria atualizadas com sucesso.")
}

// saveWorkspaceLogoFile validates and persists an uploaded logo file.
// Returns the public serve path (/uploads/workspaces/{id}/filename) or "" if no file was uploaded.
func saveWorkspaceLogoFile(r *http.Request, fieldName, workspaceID, logoType string) (string, error) {
	uploadDir := paths.WorkspaceUploadsDir(workspaceID)
	fileName, err := saveUploadedImageFile(r, fieldName, uploadDir, "logo-"+logoType, workspaceLogoMaxBytes)
	if err != nil {
		return "", err
	}
	if fileName == "" {
		return "", nil
	}
	return "/uploads/workspaces/" + workspaceID + "/" + fileName, nil
}

func saveUploadedImageFile(r *http.Request, fieldName, uploadDir, filePrefix string, maxBytes int64) (string, error) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		if err == http.ErrMissingFile {
			return "", nil
		}
		return "", fmt.Errorf("não foi possível ler o arquivo enviado")
	}
	defer file.Close()

	ext, expectedContentType, err := validateUploadedImageHeader(header)
	if err != nil {
		return "", err
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("não foi possível ler o arquivo enviado")
	}
	if int64(len(content)) > maxBytes {
		return "", fmt.Errorf("arquivo muito grande: limite de %d MB", maxBytes/(1<<20))
	}
	if len(content) == 0 {
		return "", fmt.Errorf("arquivo vazio não permitido")
	}
	if !uploadedImageContentMatches(content, expectedContentType) {
		return "", fmt.Errorf("tipo de arquivo inválido ou inconsistente")
	}
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		log.Printf("mkdir upload dir failed: path=%s err=%v", uploadDir, err)
		return "", fmt.Errorf("não foi possível criar o diretório de upload")
	}

	fileName := safeUploadPrefix(filePrefix) + "-" + uuid.NewString() + ext
	dstPath, err := safeUploadPath(uploadDir, fileName)
	if err != nil {
		return "", fmt.Errorf("caminho de upload inválido")
	}
	if err := os.WriteFile(dstPath, content, 0o600); err != nil {
		log.Printf("write upload file failed: path=%s err=%v", dstPath, err)
		return "", fmt.Errorf("não foi possível salvar o arquivo")
	}
	return fileName, nil
}

func validateUploadedImageHeader(header *multipart.FileHeader) (string, string, error) {
	if header == nil {
		return "", "", fmt.Errorf("arquivo inválido")
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(header.Filename)))
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	switch ext {
	case ".jpg":
		return ext, "image/jpeg", nil
	case ".png":
		return ext, "image/png", nil
	case ".webp":
		return ext, "image/webp", nil
	case ".svg":
		return "", "", fmt.Errorf("SVG não é permitido para upload")
	default:
		return "", "", fmt.Errorf("formato não permitido: use JPG, PNG ou WebP")
	}
}

func uploadedImageContentMatches(content []byte, expectedContentType string) bool {
	sniffLen := len(content)
	if sniffLen > uploadImageValidationBytes {
		sniffLen = uploadImageValidationBytes
	}
	return http.DetectContentType(content[:sniffLen]) == expectedContentType
}

func safeUploadPrefix(prefix string) string {
	prefix = strings.TrimSpace(strings.ToLower(prefix))
	prefix = strings.NewReplacer("/", "-", "\\", "-", ".", "-", " ", "-").Replace(prefix)
	prefix = strings.Trim(prefix, "-")
	if prefix == "" {
		return "upload"
	}
	return prefix
}

func safeUploadPath(uploadDir, fileName string) (string, error) {
	if fileName == "" || filepath.Base(fileName) != fileName || strings.Contains(fileName, "..") {
		return "", fmt.Errorf("invalid filename")
	}
	baseAbs, err := filepath.Abs(uploadDir)
	if err != nil {
		return "", err
	}
	fullAbs, err := filepath.Abs(filepath.Join(uploadDir, fileName))
	if err != nil {
		return "", err
	}
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside upload directory")
	}
	return fullAbs, nil
}

func (h *ConfiguracoesHandler) HandleAdminWorkspacesCreate(w http.ResponseWriter, r *http.Request) {
	slog.Debug("admin workspace create: parsing form", "actor_user_id", h.UserID)
	r.Body = http.MaxBytesReader(w, r.Body, workspaceLogoMultipartMax)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			h.respondWorkspaceCreateError(w, "Formulário inválido.")
			return
		}
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	workspaceType := normalizeWorkspaceType(r.FormValue("type"))
	themeToken := normalizeWorkspaceThemeToken(r.FormValue("theme_token"), workspaceType)
	companyName := strings.TrimSpace(r.FormValue("company_name"))
	cnpjCPF := strings.TrimSpace(r.FormValue("cnpj_cpf"))
	address := strings.TrimSpace(r.FormValue("address"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	logoLightURL := strings.TrimSpace(r.FormValue("logo_light_url"))
	logoDarkURL := strings.TrimSpace(r.FormValue("logo_dark_url"))
	if workspaceType != models.WorkspaceTypeBusiness {
		companyName = ""
		cnpjCPF = ""
		address = ""
		phone = ""
	}
	slog.Debug("admin workspace create: form decoded", "actor_user_id", h.UserID, "workspace_type", workspaceType, "name_empty", name == "", "cnpj_empty", cnpjCPF == "")

	if strings.TrimSpace(h.UserID) == "" {
		h.respondWorkspaceCreateError(w, "Sessão inválida: usuário criador não identificado.")
		return
	}
	if name == "" {
		h.respondWorkspaceCreateError(w, "Nome do workspace é obrigatório.")
		return
	}
	if workspaceType == models.WorkspaceTypeBusiness {
		if companyName == "" {
			h.respondWorkspaceCreateError(w, "Razão social / nome da empresa é obrigatório para workspace empresarial.")
			return
		}
		if cnpjCPF == "" {
			h.respondWorkspaceCreateError(w, "CNPJ / CPF é obrigatório para workspace empresarial.")
			return
		}
	}
	slog.Debug("admin workspace create: opening transaction", "actor_user_id", h.UserID)
	tx, err := h.DB.Begin()
	if err != nil {
		h.respondWorkspaceCreateError(w, "Não foi possível iniciar transação para criar workspace.")
		return
	}
	defer tx.Rollback()
	newID := uuid.NewString()
	slog.Debug("admin workspace create: inserting workspace", "workspace_id", newID, "actor_user_id", h.UserID, "workspace_type", workspaceType)
	if _, err := tx.Exec(`
		INSERT INTO workspaces (id, name, description, type, theme_token, company_name, cnpj_cpf, address, phone, logo_light_url, logo_dark_url, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch(), unixepoch())
	`, newID, name, description, workspaceType, themeToken, companyName, cnpjCPF, address, phone, logoLightURL, logoDarkURL); err != nil {
		slog.Error("admin workspace create: insert workspaces failed", "workspace_id", newID, "actor_user_id", h.UserID, "error", err)
		h.respondWorkspaceCreateError(w, "Erro ao criar workspace: falha ao salvar dados do workspace.")
		return
	}
	slog.Debug("admin workspace create: inserting workspace member", "workspace_id", newID, "actor_user_id", h.UserID)
	if _, err := tx.Exec(`
		INSERT INTO workspace_members (workspace_id, user_id, role, joined_at)
		VALUES (?, ?, 'ADMIN', unixepoch())
	`, newID, h.UserID); err != nil {
		slog.Error("admin workspace create: insert workspace_members failed", "workspace_id", newID, "actor_user_id", h.UserID, "error", err)
		h.respondWorkspaceCreateError(w, "Erro ao criar workspace: não foi possível vincular o criador ao workspace.")
		return
	}
	if err := seedWorkspaceDefaultCategoriesTx(tx, newID, workspaceType); err != nil {
		slog.Error("admin workspace create: seed categories failed", "workspace_id", newID, "actor_user_id", h.UserID, "error", err)
		h.respondWorkspaceCreateError(w, "Erro ao criar workspace: não foi possível estruturar o plano de contas inicial.")
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Error("admin workspace create: commit failed", "workspace_id", newID, "actor_user_id", h.UserID, "error", err)
		h.respondWorkspaceCreateError(w, "Erro ao criar workspace: falha ao finalizar transação.")
		return
	}
	slog.Debug("admin workspace create: success", "workspace_id", newID, "actor_user_id", h.UserID, "workspace_type", workspaceType)

	// Handle logo file uploads after workspace is committed so we have the stable ID.
	if workspaceType == models.WorkspaceTypeBusiness {
		lightPath, err := saveWorkspaceLogoFile(r, "logo_light_file", newID, "light")
		if err != nil {
			slog.Warn("admin workspace create: logo_light upload failed", "workspace_id", newID, "error", err)
		} else if lightPath != "" {
			logoLightURL = lightPath
		}
		darkPath, err := saveWorkspaceLogoFile(r, "logo_dark_file", newID, "dark")
		if err != nil {
			slog.Warn("admin workspace create: logo_dark upload failed", "workspace_id", newID, "error", err)
		} else if darkPath != "" {
			logoDarkURL = darkPath
		}
		if logoLightURL != "" || logoDarkURL != "" {
			_, _ = h.DB.Exec(`UPDATE workspaces SET logo_light_url = ?, logo_dark_url = ?, updated_at = unixepoch() WHERE id = ?`,
				logoLightURL, logoDarkURL, newID)
		}
	}

	h.renderConfigSectionWithFlash(w, "admin-workspaces", "", "Workspace criado com sucesso.")
}

// seedWorkspaceDefaultCategoriesTx delega ao motor centralizado de seed
// localizado em internal/database/seeder.go.
// Categorias estruturais continuam centralizadas no seeder. Contas e cartões
// reais não são criados automaticamente.
func seedWorkspaceDefaultCategoriesTx(tx *sql.Tx, workspaceID, workspaceType string) error {
	if err := database.SeedWorkspaceCategoriesTx(tx, workspaceID, workspaceType); err != nil {
		return err
	}
	return database.SeedWorkspaceAccountsTx(tx, workspaceID, workspaceType)
}

func (h *ConfiguracoesHandler) respondWorkspaceCreateError(w http.ResponseWriter, message string) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		trimmed = "Erro ao criar workspace."
	}
	slog.Error("admin workspace create failed", "actor_user_id", h.UserID, "message", trimmed)
	w.Header().Set("HX-Trigger", fmt.Sprintf(`{"mostrarAlerta":%q}`, "❌ Erro ao criar workspace: "+trimmed))
	// For HTMX form submit, retarget only the inline error container.
	w.Header().Set("HX-Retarget", "#workspace-error-container")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	fmt.Fprintf(w, `<div class="rounded-xl border border-rose-500/35 bg-rose-500/10 p-3 text-sm text-rose-100">❌ Erro ao criar workspace: %s</div>`, template.HTMLEscapeString(trimmed))
}

func (h *ConfiguracoesHandler) HandleAdminWorkspaceEdit(w http.ResponseWriter, r *http.Request, workspaceID string) {
	r.Body = http.MaxBytesReader(w, r.Body, workspaceLogoMultipartMax)
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			h.renderConfigSectionWithFlash(w, "admin-workspaces", "Formulário inválido.", "")
			return
		}
	}
	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	workspaceType := normalizeWorkspaceType(r.FormValue("type"))
	themeToken := normalizeWorkspaceThemeToken(r.FormValue("theme_token"), workspaceType)
	companyName := strings.TrimSpace(r.FormValue("company_name"))
	cnpjCPF := strings.TrimSpace(r.FormValue("cnpj_cpf"))
	address := strings.TrimSpace(r.FormValue("address"))
	phone := strings.TrimSpace(r.FormValue("phone"))
	logoLightURL := strings.TrimSpace(r.FormValue("logo_light_url"))
	logoDarkURL := strings.TrimSpace(r.FormValue("logo_dark_url"))
	if workspaceType != models.WorkspaceTypeBusiness {
		companyName = ""
		cnpjCPF = ""
		address = ""
		phone = ""
		logoLightURL = ""
		logoDarkURL = ""
	}
	if name == "" || workspaceID == "" {
		h.renderConfigSectionWithFlash(w, "admin-workspaces", "Nome do workspace é obrigatório.", "")
		return
	}
	// Process uploaded logo files; a new upload overrides the URL text field.
	if workspaceType == models.WorkspaceTypeBusiness {
		if lightPath, err := saveWorkspaceLogoFile(r, "logo_light_file", workspaceID, "light"); err != nil {
			h.renderConfigSectionWithFlash(w, "admin-workspaces", "Logo claro: "+err.Error(), "")
			return
		} else if lightPath != "" {
			logoLightURL = lightPath
		}
		if darkPath, err := saveWorkspaceLogoFile(r, "logo_dark_file", workspaceID, "dark"); err != nil {
			h.renderConfigSectionWithFlash(w, "admin-workspaces", "Logo escuro: "+err.Error(), "")
			return
		} else if darkPath != "" {
			logoDarkURL = darkPath
		}
	}
	if err := execOne(h.DB, `
		UPDATE workspaces
		SET name = ?, description = ?, type = ?, theme_token = ?, company_name = ?, cnpj_cpf = ?, address = ?, phone = ?, logo_light_url = ?, logo_dark_url = ?, updated_at = unixepoch()
		WHERE id = ?
	`, name, description, workspaceType, themeToken, companyName, cnpjCPF, address, phone, logoLightURL, logoDarkURL, workspaceID); err != nil {
		h.renderConfigSectionWithFlash(w, "admin-workspaces", "Não foi possível atualizar o workspace.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "admin-workspaces", "", "Workspace atualizado com sucesso.")
}

func (h *ConfiguracoesHandler) HandleAdminWorkspaceDelete(w http.ResponseWriter, r *http.Request, workspaceID string) {
	if h.ActorRole != models.RoleAdmin && h.ActorRole != models.RoleManager {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(workspaceID) == "" {
		h.renderConfigSectionWithFlash(w, "admin-workspaces", "Workspace inválido.", "")
		return
	}
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM workspaces WHERE id = ?`, workspaceID)
	if err != nil {
		if isSQLiteForeignKeyConstraint(err) {
			msg := "⚠️ Não é possível excluir este Workspace porque ele possui vínculos ativos. Remova ou transfira os registros vinculados antes de prosseguir."
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"mostrarAlerta":%q}`, msg))
			h.renderConfigSectionWithFlashStatus(w, "admin-workspaces", msg, "", http.StatusUnprocessableEntity)
			return
		}
		h.renderConfigSectionWithFlash(w, "admin-workspaces", "Não foi possível excluir o workspace.", "")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if affected != 1 {
		h.renderConfigSectionWithFlash(w, "admin-workspaces", "Workspace não encontrado.", "")
		return
	}
	if err := ensureWorkspaceCascadeDeleted(tx, workspaceID); err != nil {
		log.Printf("workspace cascade delete validation failed for %s: %v", workspaceID, err)
		h.renderConfigSectionWithFlash(w, "admin-workspaces", "A exclusão não foi concluída com segurança. Nenhuma alteração foi aplicada.", "")
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.renderConfigSectionWithFlash(w, "admin-workspaces", "", "Workspace excluído com sucesso.")
}

func (h *ConfiguracoesHandler) HandleAdminUserDelete(w http.ResponseWriter, r *http.Request, userID string) {
	if h.ActorRole != models.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(userID) == "" {
		h.renderConfigSectionWithFlash(w, "admin-users", "Usuário inválido.", "")
		return
	}
	if userID == h.UserID {
		h.renderConfigSectionWithFlash(w, "admin-users", "Não é possível excluir seu próprio usuário.", "")
		return
	}
	if h.ActorRole == models.RoleManager {
		var targetRole string
		if err := h.DB.QueryRow(`
			SELECT role
			FROM workspace_members
			WHERE workspace_id = ? AND user_id = ?
			LIMIT 1
		`, h.WorkspaceID, userID).Scan(&targetRole); err == nil && strings.TrimSpace(targetRole) == models.RoleAdmin {
			h.renderConfigSectionWithFlash(w, "admin-users", "Manager não pode excluir usuários Admin.", "")
			return
		}
	}
	if err := execOne(h.DB, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		if isSQLiteForeignKeyConstraint(err) {
			msg := "⚠️ Não é possível excluir este usuário porque ele possui Workspaces ativos ou registros financeiros atrelados. Remova ou transfira os Workspaces dele antes de prosseguir."
			w.Header().Set("HX-Trigger", fmt.Sprintf(`{"mostrarAlerta":%q}`, msg))
			h.renderConfigSectionWithFlashStatus(w, "admin-users", msg, "", http.StatusUnprocessableEntity)
			return
		}
		h.renderConfigSectionWithFlash(w, "admin-users", "Não foi possível excluir o usuário.", "")
		return
	}
	h.renderConfigSectionWithFlash(w, "admin-users", "", "Usuário excluído com sucesso.")
}

func (h *ConfiguracoesHandler) renderConfigSectionWithFlash(w http.ResponseWriter, section, errMsg, okMsg string) {
	h.renderConfigSectionWithFlashStatus(w, section, errMsg, okMsg, http.StatusOK)
}

func (h *ConfiguracoesHandler) RenderConfigSectionWithFlashStatus(w http.ResponseWriter, section, errMsg, okMsg string, status int) {
	h.renderConfigSectionWithFlashStatus(w, section, errMsg, okMsg, status)
}

func (h *ConfiguracoesHandler) renderConfigSectionWithFlashStatus(w http.ResponseWriter, section, errMsg, okMsg string, status int) {
	section = normalizeConfigSection(section, h.ActorRole)
	data, err := h.buildConfigRenderData(section, errMsg, okMsg, false)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	sectionTplName := strings.ReplaceAll(section, "-", "_")
	templateName := "configuracoes-" + sectionTplName + "-content"

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		log.Printf("template error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (h *ConfiguracoesHandler) newConfigData(section, errMsg, okMsg string) ConfiguracoesData {
	return ConfiguracoesData{
		Title:               "Configurações",
		Section:             normalizeConfigSection(section, h.ActorRole),
		FlashError:          errMsg,
		FlashSuccess:        okMsg,
		ActorRole:           h.ActorRole,
		CanManageOps:        h.ActorRole == models.RoleManager || h.ActorRole == models.RoleAdmin,
		CanManageMembers:    h.ActorRole == models.RoleAdmin,
		CanManageGlobal:     h.ActorRole == models.RoleAdmin,
		CurrentWorkspace:    h.WorkspaceID,
		BackupTicker:        "24h",
		BackupRetention:     5,
		AccountProviders:    accountProviderOptions(),
		AuditEventFilter:    normalizeAuditEventFilter(h.AuditEventFilter),
		AuditSeverityFilter: normalizeAuditSeverityFilter(h.AuditSeverityFilter),
	}
}

func (h *ConfiguracoesHandler) buildConfigRenderData(section, errMsg, okMsg string, includeShell bool) (ConfiguracoesData, error) {
	data := h.newConfigData(section, errMsg, okMsg)
	if includeShell {
		if err := h.loadConfigShellData(&data); err != nil {
			return data, err
		}
	}
	if err := h.loadConfigSectionData(&data, includeShell); err != nil {
		return data, err
	}
	return data, nil
}

func (h *ConfiguracoesHandler) loadConfigShellData(data *ConfiguracoesData) error {
	var workspaceTypeValue string
	if err := h.DB.QueryRow(`
		SELECT COALESCE(name, 'Workspace'), COALESCE(type, 'personal')
		FROM workspaces
		WHERE id = ?
	`, h.WorkspaceID).Scan(&data.ActiveWorkspaceName, &workspaceTypeValue); err != nil {
		return err
	}
	data.IsBusiness = normalizeWorkspaceType(workspaceTypeValue) == models.WorkspaceTypeBusiness
	data.CanManageWorkspaceProfile = data.IsBusiness && h.CanConfigWrite

	var photoPath string
	var updatedAt int64
	if err := h.DB.QueryRow(`
		SELECT COALESCE(name, 'Usuário'), COALESCE(profile_photo_path, ''), COALESCE(updated_at, unixepoch())
		FROM users
		WHERE id = ?
	`, h.UserID).Scan(&data.UserName, &photoPath, &updatedAt); err != nil {
		return err
	}
	data.UserInitials = initials(data.UserName)
	data.UserFirstName = extractFirstName(data.UserName)
	if fileName := strings.TrimPrefix(photoPath, "/uploads/profile/"); photoPath != "" && fileName != photoPath && fileName != "" {
		if _, err := os.Stat(filepath.Join(paths.ProfileUploadsDir(), fileName)); err == nil {
			data.ProfilePhotoURL = fmt.Sprintf("%s?v=%d", photoPath, updatedAt)
		}
	}
	return nil
}

func (h *ConfiguracoesHandler) loadConfigSectionData(data *ConfiguracoesData, shellLoaded bool) error {
	repo := repository.NewConfigRepository(h.DB)

	switch data.Section {
	case "categorias":
		if !shellLoaded {
			data.IsBusiness = workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness
		}
		rows, tree, err := h.queryCategoriasWithWorkspaceType(repo, data.IsBusiness)
		if err != nil {
			return err
		}
		data.Categorias = rows
		data.CategoryTree = tree
	case "contas":
		rows, err := h.queryContas(repo)
		if err != nil {
			return err
		}
		data.Contas = rows
		archivedRows, err := h.queryArchivedContas(repo)
		if err != nil {
			return err
		}
		data.ContasArquivadas = archivedRows
	case "cartoes":
		rows, err := h.queryCartoes(repo)
		if err != nil {
			return err
		}
		data.Cartoes = rows
		archivedRows, err := h.queryArchivedCartoes(repo)
		if err != nil {
			return err
		}
		data.CartoesArquivados = archivedRows
	case "workspace":
		profile, err := h.queryWorkspaceCorporateProfile()
		if err != nil {
			return err
		}
		data.WorkspaceProfile = profile
		data.IsBusiness = profile.Type == models.WorkspaceTypeBusiness
	case "perfil":
		profile, err := h.queryProfile()
		if err != nil {
			return err
		}
		data.Perfil = profile
		if data.UserInitials == "" {
			data.UserInitials = initials(profile.Name)
		}
		if err := h.loadProfileWorkspaces(data); err != nil {
			return err
		}
	case "admin-users":
		if err := h.loadWorkspaces(data); err != nil {
			return err
		}
		users, err := h.queryAdminUsers()
		if err != nil {
			return err
		}
		data.AdminUsers = users
	case "admin-workspaces":
		workspaces, err := h.queryAdminWorkspaces()
		if err != nil {
			return err
		}
		data.AdminWorkspaces = workspaces
	case "admin-auditoria":
		if err := h.loadAdminAuditoriaData(data); err != nil {
			return err
		}
	}
	return nil
}

func (h *ConfiguracoesHandler) buildConfigData(section, errMsg, okMsg string) (ConfiguracoesData, error) {
	repo := repository.NewConfigRepository(h.DB)
	data := ConfiguracoesData{
		Title:                     "Configurações",
		IsBusiness:                workspaceType(h.DB, h.WorkspaceID) == "business",
		ActiveWorkspaceName:       queryWorkspaceName(h.DB, h.WorkspaceID),
		Section:                   normalizeConfigSection(section, h.ActorRole),
		FlashError:                errMsg,
		FlashSuccess:              okMsg,
		ActorRole:                 h.ActorRole,
		CanManageOps:              h.ActorRole == models.RoleManager || h.ActorRole == models.RoleAdmin,
		CanManageMembers:          h.ActorRole == models.RoleAdmin,
		CanManageGlobal:           h.ActorRole == models.RoleAdmin,
		CanManageWorkspaceProfile: workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness && h.CanConfigWrite,
		CurrentWorkspace:          h.WorkspaceID,
		BackupTicker:              "24h",
		BackupRetention:           5,
		AccountProviders:          accountProviderOptions(),
		AuditEventFilter:          normalizeAuditEventFilter(h.AuditEventFilter),
		AuditSeverityFilter:       normalizeAuditSeverityFilter(h.AuditSeverityFilter),
	}
	data.UserInitials = queryUserInitials(repo, h.UserID)
	if data.UserInitials == "" {
		data.UserInitials = "US"
	}
	data.UserName, _ = queryDashboardUser(h.DB, h.UserID)
	data.UserFirstName = extractFirstName(data.UserName)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.UserWorkspaces = queryUserWorkspaces(h.DB, h.UserID)
	if err := h.loadWorkspaces(&data); err != nil {
		return data, err
	}

	switch data.Section {
	case "categorias":
		rows, tree, err := h.queryCategorias(repo)
		if err != nil {
			return data, err
		}
		data.Categorias = rows
		data.CategoryTree = tree
	case "contas":
		rows, err := h.queryContas(repo)
		if err != nil {
			return data, err
		}
		data.Contas = rows
		archivedRows, err := h.queryArchivedContas(repo)
		if err != nil {
			return data, err
		}
		data.ContasArquivadas = archivedRows
	case "cartoes":
		rows, err := h.queryCartoes(repo)
		if err != nil {
			return data, err
		}
		data.Cartoes = rows
		archivedRows, err := h.queryArchivedCartoes(repo)
		if err != nil {
			return data, err
		}
		data.CartoesArquivados = archivedRows
	case "workspace":
		ws, members, err := h.queryWorkspaceAndMembers(repo)
		if err != nil {
			return data, err
		}
		data.Workspace = ws
		data.Membros = members
		profile, err := h.queryWorkspaceCorporateProfile()
		if err != nil {
			return data, err
		}
		data.WorkspaceProfile = profile
	case "perfil":
		profile, err := h.queryProfile()
		if err != nil {
			return data, err
		}
		data.Perfil = profile
		if err := h.loadProfileWorkspaces(&data); err != nil {
			return data, err
		}
	case "admin-users":
		if !data.CanManageMembers {
			data.Section = "perfil"
			profile, err := h.queryProfile()
			if err != nil {
				return data, err
			}
			data.Perfil = profile
			break
		}
		users, err := h.queryAdminUsers()
		if err != nil {
			return data, err
		}
		data.AdminUsers = users
	case "admin-workspaces":
		if !data.CanManageGlobal {
			data.Section = "perfil"
			profile, err := h.queryProfile()
			if err != nil {
				return data, err
			}
			data.Perfil = profile
			break
		}
		workspaces, err := h.queryAdminWorkspaces()
		if err != nil {
			return data, err
		}
		data.AdminWorkspaces = workspaces
	case "admin-backups":
		if !data.CanManageGlobal {
			data.Section = "perfil"
			profile, err := h.queryProfile()
			if err != nil {
				return data, err
			}
			data.Perfil = profile
		}
	case "admin-auditoria":
		if !data.CanManageGlobal {
			data.Section = "perfil"
			profile, err := h.queryProfile()
			if err != nil {
				return data, err
			}
			data.Perfil = profile
			break
		}
		if err := h.loadAdminAuditoriaData(&data); err != nil {
			return data, err
		}
	}
	return data, nil
}

func (h *ConfiguracoesHandler) loadAdminAuditoriaData(data *ConfiguracoesData) error {
	if err := h.loadAdminAuditSettings(data); err != nil {
		return err
	}
	return h.loadAdminAuditLogs(data)
}

func (h *ConfiguracoesHandler) loadAdminAuditSettings(data *ConfiguracoesData) error {
	var (
		smtpHost          string
		smtpPort          int
		smtpUser          string
		smtpPass          string
		notificationEmail string
		emailPreferences  string
	)
	if err := h.DB.QueryRow(`
		SELECT
			COALESCE(smtp_host, ''),
			COALESCE(smtp_port, 587),
			COALESCE(smtp_user, ''),
			COALESCE(smtp_pass, ''),
			COALESCE(notification_email, ''),
			COALESCE(email_preferences, '[]')
		FROM workspaces
		WHERE id = ?
	`, h.WorkspaceID).Scan(&smtpHost, &smtpPort, &smtpUser, &smtpPass, &notificationEmail, &emailPreferences); err != nil {
		return err
	}
	data.SMTPHost = smtpHost
	data.SMTPPort = smtpPort
	data.SMTPUser = smtpUser
	if strings.TrimSpace(smtpPass) != "" {
		decryptedSMTPPass, err := security.Decrypt(smtpPass)
		if err != nil {
			return err
		}
		data.SMTPPass = decryptedSMTPPass
	}
	data.NotificationEmail = notificationEmail

	preferences := parseAuditPreferences(emailPreferences)
	data.EmailPrefAuth = preferences["auth.failed"]
	data.EmailPrefBackup = preferences["backup.export"]
	data.EmailPrefWorkspace = preferences["workspace.edit"]
	return nil
}

func (h *ConfiguracoesHandler) loadAdminAuditLogs(data *ConfiguracoesData) error {
	data.AuditEventFilter = normalizeAuditEventFilter(data.AuditEventFilter)
	data.AuditSeverityFilter = normalizeAuditSeverityFilter(data.AuditSeverityFilter)

	var baseQuery string
	var args []interface{}
	if h.ActorRole == models.RoleAdmin {
		baseQuery = `
			SELECT COALESCE(event_type, ''), COALESCE(severity, ''), COALESCE(ip_address, ''), COALESCE(metadata, '{}'), COALESCE(created_at, unixepoch())
			FROM security_logs
			WHERE created_at >= unixepoch('now', '-14 days')
				AND (workspace_id = ? OR workspace_id IS NULL)
		`
		args = []interface{}{h.WorkspaceID}
	} else {
		baseQuery = `
			SELECT COALESCE(event_type, ''), COALESCE(severity, ''), COALESCE(ip_address, ''), COALESCE(metadata, '{}'), COALESCE(created_at, unixepoch())
			FROM security_logs
			WHERE created_at >= unixepoch('now', '-14 days')
				AND (
					workspace_id = ?
					OR (
						workspace_id IS NULL
						AND EXISTS (
							SELECT 1
							FROM workspace_members wm
							JOIN users u ON u.id = wm.user_id
							WHERE wm.workspace_id = ?
							  AND LOWER(TRIM(COALESCE(u.email, ''))) = LOWER(TRIM(COALESCE(
									json_extract(security_logs.metadata, '$.email_tentado'),
									json_extract(security_logs.metadata, '$.email'),
									''
							  )))
						)
					)
				)
		`
		args = []interface{}{h.WorkspaceID, h.WorkspaceID}
	}
	if data.AuditEventFilter != "" {
		baseQuery += ` AND event_type = ?`
		args = append(args, data.AuditEventFilter)
	}
	if data.AuditSeverityFilter != "" {
		baseQuery += ` AND UPPER(severity) = ?`
		args = append(args, data.AuditSeverityFilter)
	}
	baseQuery += ` ORDER BY created_at DESC LIMIT 400`

	rows, err := h.DB.Query(baseQuery, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			eventType string
			severity  string
			ipAddress string
			metadata  string
			createdAt int64
		)
		if err := rows.Scan(&eventType, &severity, &ipAddress, &metadata, &createdAt); err != nil {
			return err
		}

		status := "registrado"
		var payload map[string]interface{}
		targetEmail := ""
		if err := json.Unmarshal([]byte(metadata), &payload); err == nil {
			if candidate, ok := payload["status"].(string); ok && strings.TrimSpace(candidate) != "" {
				status = strings.TrimSpace(candidate)
			}
			if candidate, ok := payload["email_tentado"].(string); ok {
				targetEmail = strings.ToLower(strings.TrimSpace(candidate))
			} else if candidate, ok := payload["email"].(string); ok {
				targetEmail = strings.ToLower(strings.TrimSpace(candidate))
			}
		}
		data.SecurityLogs = append(data.SecurityLogs, SecurityLogRow{
			OccurredAt:  formatDateTimeLabel(createdAt),
			EventType:   eventType,
			TargetEmail: targetEmail,
			Severity:    strings.ToUpper(strings.TrimSpace(severity)),
			IPAddress:   ipAddress,
			Status:      status,
		})
	}
	return rows.Err()
}

func validateWorkspaceLogoFile(logoURL string) string {
	if logoURL == "" {
		return ""
	}
	relativePath := strings.TrimPrefix(logoURL, "/uploads/workspaces/")
	if relativePath == logoURL || relativePath == "" {
		return ""
	}
	fullPath := filepath.Join(paths.UploadsDir(), "workspaces", relativePath)
	if _, err := os.Stat(fullPath); err == nil {
		return logoURL
	}
	return ""
}

func validateWorkspaceLogoFormValue(logoURL, workspaceID string) string {
	logoURL = strings.TrimSpace(logoURL)
	workspaceID = strings.TrimSpace(workspaceID)
	if logoURL == "" || workspaceID == "" {
		return ""
	}
	prefix := "/uploads/workspaces/" + workspaceID + "/"
	if !strings.HasPrefix(logoURL, prefix) {
		return ""
	}
	return validateWorkspaceLogoFile(logoURL)
}

func (h *ConfiguracoesHandler) queryProfile() (ConfigProfileRow, error) {
	var row ConfigProfileRow
	err := h.DB.QueryRow(`SELECT name, email, COALESCE(profile_photo_path, ''), COALESCE(default_workspace_id, ''), COALESCE(totp_enabled, 0), COALESCE(updated_at, unixepoch()) FROM users WHERE id = ?`, h.UserID).Scan(&row.Name, &row.Email, &row.PhotoPath, &row.DefaultWorkspaceID, &row.TwoFactorEnabled, &row.UpdatedAt)
	if err == nil && strings.TrimSpace(row.PhotoPath) != "" {
		fileName := strings.TrimPrefix(row.PhotoPath, "/uploads/profile/")
		if fileName != row.PhotoPath && fileName != "" {
			fullPath := filepath.Join(paths.ProfileUploadsDir(), fileName)
			if _, statErr := os.Stat(fullPath); statErr == nil {
				row.PhotoURL = fmt.Sprintf("%s?v=%d", row.PhotoPath, row.UpdatedAt)
			}
		}
	}
	return row, err
}

func (h *ConfiguracoesHandler) loadProfileWorkspaces(data *ConfiguracoesData) error {
	rows, err := h.DB.Query(`
		SELECT w.id, w.name, COALESCE(w.description, ''), COALESCE(w.theme_token, '')
		FROM workspace_members wm
		JOIN workspaces w ON w.id = wm.workspace_id
		WHERE wm.user_id = ?
		ORDER BY w.name
	`, h.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item ConfigWorkspaceRow
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.ThemeToken); err != nil {
			return err
		}
		item.ThemeToken = normalizeWorkspaceThemeToken(item.ThemeToken, "")
		data.ProfileWorkspaces = append(data.ProfileWorkspaces, item)
	}
	return rows.Err()
}

func (h *ConfiguracoesHandler) loadWorkspaces(data *ConfiguracoesData) error {
	rows, err := h.DB.Query(`SELECT id, name, description, COALESCE(theme_token, '') FROM workspaces ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item ConfigWorkspaceRow
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.ThemeToken); err != nil {
			return err
		}
		item.ThemeToken = normalizeWorkspaceThemeToken(item.ThemeToken, "")
		data.SystemWorkspaces = append(data.SystemWorkspaces, item)
	}
	return rows.Err()
}

func (h *ConfiguracoesHandler) queryCategorias(repo *repository.ConfigRepository) ([]ConfigCategoryRow, []*ConfigCategoryRow, error) {
	return h.queryCategoriasWithWorkspaceType(repo, workspaceType(h.DB, h.WorkspaceID) == models.WorkspaceTypeBusiness)
}

func (h *ConfiguracoesHandler) queryCategoriasWithWorkspaceType(repo *repository.ConfigRepository, isBusiness bool) ([]ConfigCategoryRow, []*ConfigCategoryRow, error) {
	rows, err := repo.CategoriesByWorkspace(h.WorkspaceID)
	if err != nil {
		return nil, nil, err
	}
	var flat []ConfigCategoryRow
	for _, item := range rows {
		flat = append(flat, ConfigCategoryRow{
			ID:                  item.ID,
			Name:                item.Name,
			Type:                item.Type,
			MacroGroup:          item.MacroGroup,
			EffectiveMacroGroup: item.EffectiveMac,
			ParentID:            item.ParentID,
			ParentName:          item.ParentName,
			IsChild:             item.ParentID != "",
			IsBusiness:          isBusiness,
		})
	}
	tree := buildCategoryTree(flat)
	return flat, tree, nil
}

func buildCategoryTree(flat []ConfigCategoryRow) []*ConfigCategoryRow {
	byID := make(map[string]*ConfigCategoryRow)
	var roots []*ConfigCategoryRow
	for i := range flat {
		row := &flat[i]
		byID[row.ID] = row
	}
	for _, row := range byID {
		if row.ParentID != "" {
			if parent, ok := byID[row.ParentID]; ok {
				parent.Children = append(parent.Children, row)
			} else {
				roots = append(roots, row)
			}
		} else {
			roots = append(roots, row)
		}
	}
	return roots
}

func (h *ConfiguracoesHandler) resolveCategoryMacroGroup(parentID, typ, requestedMacro string) (string, error) {
	isBusiness := workspaceType(h.DB, h.WorkspaceID) == "business"
	defaultMacro := defaultMacroGroupForWorkspace(isBusiness, typ)
	if parentID == "" {
		if requestedMacro == "" {
			return defaultMacro, nil
		}
		if !isMacroGroupValidForType(isBusiness, requestedMacro, typ) {
			return "", fmt.Errorf("Grupo macro %q não é válido para o tipo %q.", requestedMacro, typ)
		}
		return requestedMacro, nil
	}
	var parentMacro, parentType string
	err := h.DB.QueryRow(`
		SELECT COALESCE(c.macro_group, p.macro_group, ?), c.type
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id AND p.workspace_id = c.workspace_id
		WHERE c.id = ? AND c.workspace_id = ? AND c.parent_id IS NULL
	`, defaultMacro, parentID, h.WorkspaceID).Scan(&parentMacro, &parentType)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("Categoria pai não encontrada.")
	}
	if err != nil {
		return "", fmt.Errorf("Não foi possível validar a categoria pai.")
	}
	if parentType != typ {
		return "", fmt.Errorf("Categoria pai deve ter o mesmo tipo da categoria filha.")
	}
	return parentMacro, nil
}

func (h *ConfiguracoesHandler) queryContas(repo *repository.ConfigRepository) ([]ConfigAccountRow, error) {
	rows, err := repo.AccountsByWorkspace(h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []ConfigAccountRow
	for _, item := range rows {
		icon := item.Icon
		if icon == "" {
			icon = accountVisualByProvider(item.ProviderSlug, item.Type)
		}
		out = append(out, ConfigAccountRow{
			ID:           item.ID,
			Name:         item.Name,
			Type:         item.Type,
			TypeLabel:    models.AccountTypeLabel(item.Type),
			Color:        normalizeHexColor(item.Color, "#6B7280"),
			Icon:         icon,
			ProviderSlug: normalizeAccountProviderSlug(item.ProviderSlug),
			ProviderMark: accountProviderMark(item.ProviderSlug, item.Name),
			Balance:      formatCurrencyLabel(item.CurrentBalance),
			BalanceCents: item.CurrentBalance,
			BalanceInput: centsToInput(item.CurrentBalance),
			SortOrder:    item.SortOrder,
		})
	}
	return out, nil
}

func (h *ConfiguracoesHandler) queryCartoes(repo *repository.ConfigRepository) ([]ConfigCardRow, error) {
	rows, err := repo.CardsByWorkspace(h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []ConfigCardRow
	for _, item := range rows {
		providerSlug := normalizeAccountProviderSlug(item.ProviderSlug)
		icon := item.Icon
		if icon == "" {
			icon = accountVisualByProvider(providerSlug, "CREDIT_CARD")
		}
		out = append(out, ConfigCardRow{
			AccountID:        item.AccountID,
			Name:             item.Name,
			Color:            normalizeHexColor(item.Color, "#6B7280"),
			Icon:             icon,
			ProviderSlug:     providerSlug,
			ProviderMark:     accountProviderMark(providerSlug, item.Name),
			ClosingDay:       item.ClosingDay,
			DueDay:           item.DueDay,
			CreditLimit:      formatCurrencyLabel(item.CreditLimit),
			CreditLimitCents: item.CreditLimit,
			CreditLimitInput: centsToInput(item.CreditLimit),
			SortOrder:        item.SortOrder,
		})
	}
	return out, nil
}

func (h *ConfiguracoesHandler) queryArchivedContas(repo *repository.ConfigRepository) ([]ConfigAccountRow, error) {
	rows, err := repo.ArchivedAccountsByWorkspace(h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []ConfigAccountRow
	for _, item := range rows {
		icon := item.Icon
		if icon == "" {
			icon = accountVisualByProvider(item.ProviderSlug, item.Type)
		}
		out = append(out, ConfigAccountRow{
			ID:           item.ID,
			Name:         item.Name,
			Type:         item.Type,
			TypeLabel:    models.AccountTypeLabel(item.Type),
			Color:        normalizeHexColor(item.Color, "#6B7280"),
			Icon:         icon,
			ProviderSlug: normalizeAccountProviderSlug(item.ProviderSlug),
			ProviderMark: accountProviderMark(item.ProviderSlug, item.Name),
			Balance:      formatCurrencyLabel(item.CurrentBalance),
			BalanceCents: item.CurrentBalance,
			BalanceInput: centsToInput(item.CurrentBalance),
			SortOrder:    item.SortOrder,
			Archived:     true,
		})
	}
	return out, nil
}

func (h *ConfiguracoesHandler) queryArchivedCartoes(repo *repository.ConfigRepository) ([]ConfigCardRow, error) {
	rows, err := repo.ArchivedCardsByWorkspace(h.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var out []ConfigCardRow
	for _, item := range rows {
		providerSlug := normalizeAccountProviderSlug(item.ProviderSlug)
		icon := item.Icon
		if icon == "" {
			icon = accountVisualByProvider(providerSlug, "CREDIT_CARD")
		}
		out = append(out, ConfigCardRow{
			AccountID:        item.AccountID,
			Name:             item.Name,
			Color:            normalizeHexColor(item.Color, "#6B7280"),
			Icon:             icon,
			ProviderSlug:     providerSlug,
			ProviderMark:     accountProviderMark(providerSlug, item.Name),
			ClosingDay:       item.ClosingDay,
			DueDay:           item.DueDay,
			CreditLimit:      formatCurrencyLabel(item.CreditLimit),
			CreditLimitCents: item.CreditLimit,
			CreditLimitInput: centsToInput(item.CreditLimit),
			SortOrder:        item.SortOrder,
		})
	}
	return out, nil
}

func (h *ConfiguracoesHandler) queryWorkspaceAndMembers(repo *repository.ConfigRepository) (ConfigWorkspaceRow, []ConfigMemberRow, error) {
	var ws ConfigWorkspaceRow
	workspace, err := repo.WorkspaceByID(h.WorkspaceID)
	if err != nil {
		return ws, nil, err
	}
	ws = ConfigWorkspaceRow{
		ID:          workspace.ID,
		Name:        workspace.Name,
		Description: workspace.Description,
		ThemeToken:  normalizeWorkspaceThemeToken(workspace.ThemeToken, ""),
	}

	rows, err := repo.WorkspaceMembers(h.WorkspaceID)
	if err != nil {
		return ws, nil, err
	}
	var members []ConfigMemberRow
	for _, item := range rows {
		members = append(members, ConfigMemberRow{UserID: item.UserID, Name: item.Name, Email: item.Email, Role: item.Role, Joined: formatDateTimeLabel(item.JoinedAt)})
	}
	return ws, members, nil
}

func (h *ConfiguracoesHandler) queryWorkspaceCorporateProfile() (ConfigWorkspaceProfileRow, error) {
	var row ConfigWorkspaceProfileRow
	err := h.DB.QueryRow(`
		SELECT
			id,
			name,
			COALESCE(description, ''),
			COALESCE(type, 'personal'),
			COALESCE(company_name, ''),
			COALESCE(cnpj_cpf, ''),
			COALESCE(address, ''),
			COALESCE(phone, ''),
			COALESCE(logo_light_url, ''),
			COALESCE(logo_dark_url, '')
		FROM workspaces
		WHERE id = ?
	`, h.WorkspaceID).Scan(
		&row.ID,
		&row.Name,
		&row.Description,
		&row.Type,
		&row.CompanyName,
		&row.CNPJCPF,
		&row.Address,
		&row.Phone,
		&row.LogoLightURL,
		&row.LogoDarkURL,
	)
	if err != nil {
		return row, err
	}
	row.Type = normalizeWorkspaceType(row.Type)
	row.LogoLightURL = validateWorkspaceLogoFile(row.LogoLightURL)
	row.LogoDarkURL = validateWorkspaceLogoFile(row.LogoDarkURL)
	return row, nil
}

func (h *ConfiguracoesHandler) queryAdminUsers() ([]AdminUserRow, error) {
	rows, err := h.DB.Query(`
		SELECT id, name, email, COALESCE(status, 'active'), COALESCE(profile_photo_path, ''), COALESCE(totp_enabled, 0)
		FROM users
		ORDER BY name, email
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AdminUserRow
	for rows.Next() {
		var item AdminUserRow
		if err := rows.Scan(&item.ID, &item.Name, &item.Email, &item.Status, &item.PhotoPath, &item.TOTPEnabled); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	wmRows, err := h.DB.Query(`
		SELECT wm.user_id, wm.workspace_id, w.name, wm.role, COALESCE(wm.custom_permissions, '[]')
		FROM workspace_members wm
		JOIN workspaces w ON w.id = wm.workspace_id
		ORDER BY w.name
	`)
	if err != nil {
		return nil, err
	}
	defer wmRows.Close()

	rolesByUser := map[string][]AdminWorkspaceRoleRow{}
	for wmRows.Next() {
		var userID, workspaceID, workspaceName, role, customPermissionsRaw string
		if err := wmRows.Scan(&userID, &workspaceID, &workspaceName, &role, &customPermissionsRaw); err != nil {
			return nil, err
		}
		rolesByUser[userID] = append(rolesByUser[userID], AdminWorkspaceRoleRow{
			WorkspaceID:       workspaceID,
			WorkspaceName:     workspaceName,
			Role:              role,
			CustomPermissions: models.ParsePermissionList(customPermissionsRaw),
		})
	}
	if err := wmRows.Err(); err != nil {
		return nil, err
	}

	for i := range out {
		out[i].WorkspaceRoles = rolesByUser[out[i].ID]
		sort.Slice(out[i].WorkspaceRoles, func(a, b int) bool {
			return out[i].WorkspaceRoles[a].WorkspaceName < out[i].WorkspaceRoles[b].WorkspaceName
		})
		ids := make([]string, 0, len(out[i].WorkspaceRoles))
		primary := models.RoleUser
		for idx, wr := range out[i].WorkspaceRoles {
			ids = append(ids, wr.WorkspaceID)
			if wr.Role == models.RoleAdmin {
				out[i].IsAdmin = true
			}
			if idx == 0 {
				primary = wr.Role
			}
			out[i].CustomPermissions = append(out[i].CustomPermissions, wr.CustomPermissions...)
		}
		out[i].CustomPermissions = uniqueCustomPermissions(out[i].CustomPermissions)
		out[i].WorkspaceIDs = strings.Join(ids, ",")
		out[i].PrimaryRole = primary
		out[i].CanBackupExport = hasCustomPermission(out[i].CustomPermissions, models.PermissionBackupExport)
		out[i].CanContactsDelete = hasCustomPermission(out[i].CustomPermissions, models.PermissionContactsDelete)
		out[i].CanWorkspaceEdit = hasCustomPermission(out[i].CustomPermissions, models.PermissionWorkspaceEdit)
		out[i].CanReportsView = hasCustomPermission(out[i].CustomPermissions, models.PermissionReportsView)
	}
	return out, nil
}

type adminRecoveryTarget struct {
	UserID      string
	Email       string
	PrimaryRole string
	IsAdmin     bool
}

func (h *ConfiguracoesHandler) lookupAdminRecoveryTarget(userID string) (adminRecoveryTarget, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return adminRecoveryTarget{}, sql.ErrNoRows
	}
	var target adminRecoveryTarget
	target.UserID = userID
	if err := h.DB.QueryRow(`SELECT lower(email) FROM users WHERE id = ?`, userID).Scan(&target.Email); err != nil {
		return adminRecoveryTarget{}, err
	}
	rows, err := h.DB.Query(`
		SELECT role
		FROM workspace_members
		WHERE user_id = ?
		ORDER BY joined_at ASC
	`, userID)
	if err != nil {
		return adminRecoveryTarget{}, err
	}
	defer rows.Close()
	target.PrimaryRole = models.RoleUser
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return adminRecoveryTarget{}, err
		}
		role = strings.TrimSpace(role)
		if target.PrimaryRole == models.RoleUser {
			target.PrimaryRole = role
		}
		if role == models.RoleAdmin {
			target.IsAdmin = true
		}
	}
	if err := rows.Err(); err != nil {
		return adminRecoveryTarget{}, err
	}
	return target, nil
}

func (h *ConfiguracoesHandler) revokeUserAuthState(userID, eventType string, metadata map[string]string) error {
	tx, err := h.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE sessions SET revoked_at = unixepoch() WHERE user_id = ? AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE pre_auth_sessions SET consumed_at = unixepoch() WHERE user_id = ? AND consumed_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_lockouts WHERE user_id = ?`, userID); err != nil {
		return err
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}
	_, err = tx.Exec(`
		INSERT INTO auth_audit_events (id, user_id, workspace_id, event_type, ip, user_agent, metadata_json, created_at)
		VALUES (?, ?, NULL, ?, 'admin-panel', 'admin-panel', ?, unixepoch())
	`, uuid.NewString(), userID, eventType, string(metadataJSON))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (h *ConfiguracoesHandler) queryAdminWorkspaces() ([]AdminWorkspaceRow, error) {
	rows, err := h.DB.Query(`
		SELECT
			w.id,
			w.name,
			COALESCE(w.description, ''),
			COALESCE(w.type, 'personal'),
			COALESCE(w.theme_token, ''),
			COALESCE(w.company_name, ''),
			COALESCE(w.cnpj_cpf, ''),
			COALESCE(w.address, ''),
			COALESCE(w.phone, ''),
			COALESCE(w.logo_light_url, ''),
			COALESCE(w.logo_dark_url, ''),
			u.id,
			u.name,
			u.email,
			wm.role,
			wm.joined_at
		FROM workspaces w
		LEFT JOIN workspace_members wm ON wm.workspace_id = w.id
		LEFT JOIN users u ON u.id = wm.user_id
		ORDER BY w.name, wm.joined_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byWorkspace := map[string]int{}
	var out []AdminWorkspaceRow
	for rows.Next() {
		var workspaceID, workspaceName, workspaceDescription, workspaceType, themeToken, companyName, cnpjCPF, address, phone, logoLightURL, logoDarkURL string
		var userID, userName, userEmail, role sql.NullString
		var joinedAt sql.NullInt64
		if err := rows.Scan(
			&workspaceID,
			&workspaceName,
			&workspaceDescription,
			&workspaceType,
			&themeToken,
			&companyName,
			&cnpjCPF,
			&address,
			&phone,
			&logoLightURL,
			&logoDarkURL,
			&userID,
			&userName,
			&userEmail,
			&role,
			&joinedAt,
		); err != nil {
			return nil, err
		}
		idx, ok := byWorkspace[workspaceID]
		if !ok {
			out = append(out, AdminWorkspaceRow{
				ID:           workspaceID,
				Name:         workspaceName,
				Description:  workspaceDescription,
				Type:         normalizeWorkspaceType(workspaceType),
				ThemeToken:   normalizeWorkspaceThemeToken(themeToken, workspaceType),
				CompanyName:  companyName,
				CNPJCPF:      cnpjCPF,
				Address:      address,
				Phone:        phone,
				LogoLightURL: validateWorkspaceLogoFile(logoLightURL),
				LogoDarkURL:  validateWorkspaceLogoFile(logoDarkURL),
			})
			idx = len(out) - 1
			byWorkspace[workspaceID] = idx
		}
		if userID.Valid {
			member := ConfigMemberRow{
				UserID: userID.String,
				Name:   userName.String,
				Email:  userEmail.String,
				Role:   role.String,
			}
			if joinedAt.Valid {
				member.Joined = formatDateTimeLabel(joinedAt.Int64)
			}
			out[idx].Members = append(out[idx].Members, member)
		}
	}
	return out, rows.Err()
}

func normalizeConfigSection(section, actorRole string) string {
	normalized := strings.ToLower(strings.TrimSpace(section))
	if normalized == "" {
		return "perfil"
	}
	switch normalized {
	case "categorias", "contas", "cartoes", "workspace", "perfil", "admin-users", "admin-workspaces", "admin-backups", "admin-auditoria":
		if actorRole == models.RoleUser {
			return "perfil"
		}
		if actorRole == models.RoleManager && (normalized == "admin-users" || normalized == "admin-workspaces" || normalized == "admin-backups" || normalized == "admin-auditoria") {
			return "perfil"
		}
		return normalized
	default:
		return "perfil"
	}
}

func normalizeCategoryType(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "INCOME", "RECEITA":
		return "INCOME"
	case "EXPENSE", "DESPESA":
		return "EXPENSE"
	default:
		return ""
	}
}

func normalizeMacroGroup(v string) string {
	normalized := strings.TrimSpace(v)
	switch strings.ToUpper(normalized) {
	case "", "NONE", "NULL":
		return ""
	}
	if len([]rune(normalized)) > 80 {
		normalized = string([]rune(normalized)[:80])
	}
	return normalized
}

func defaultMacroGroupForWorkspace(isBusiness bool, typ string) string {
	if !isBusiness {
		if typ == "INCOME" {
			return "Receitas"
		}
		return "Estilo de Vida"
	}
	if typ == "INCOME" {
		return "Receitas Operacionais"
	}
	return "Custos Operacionais"
}

func validMacroGroupsForType(isBusiness bool, typ string) []string {
	if !isBusiness {
		if typ == "INCOME" {
			return []string{"Receitas"}
		}
		return []string{"Essencial", "Estilo de Vida"}
	}
	if typ == "INCOME" {
		return []string{"Receitas Operacionais"}
	}
	return []string{
		"Deduções/Impostos",
		"Custos Operacionais",
		"Despesas Administrativas",
		"Despesas Comerciais",
		"Equipe e Prestadores",
		"Financeiro",
		"Investimentos/Outros",
	}
}

func isMacroGroupValidForType(isBusiness bool, macroGroup, typ string) bool {
	macroGroup = strings.TrimSpace(macroGroup)
	if macroGroup == "" {
		return true
	}
	for _, valid := range validMacroGroupsForType(isBusiness, typ) {
		if macroGroup == valid {
			return true
		}
	}
	return false
}

func normalizeAccountType(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "CHECKING", "SAVINGS", "INVESTMENT", "WALLET":
		return strings.ToUpper(strings.TrimSpace(v))
	default:
		return ""
	}
}

var validIcons = map[string]bool{
	"landmark": true, "building-2": true, "wallet": true, "credit-card": true,
	"piggy-bank": true, "briefcase": true, "circle-dollar-sign": true, "receipt": true,
	"badge-japanese-yen": true, "banknote": true, "coins": true, "gem": true,
	"hand-coins": true, "scale": true, "wallet-cards": true, "contactless": true,
}

func normalizeIconName(v string) string {
	icon := strings.TrimSpace(strings.ToLower(v))
	if validIcons[icon] {
		return icon
	}
	return ""
}

func normalizeWorkspaceRole(v, actorRole string) string {
	actorRole = strings.ToUpper(strings.TrimSpace(actorRole))
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case models.RoleUser:
		return models.RoleUser
	case models.RoleManager:
		if actorRole == models.RoleManager || actorRole == models.RoleAdmin {
			return models.RoleManager
		}
		return ""
	case models.RoleAdmin:
		if actorRole == models.RoleAdmin {
			return models.RoleAdmin
		}
		return ""
	default:
		return ""
	}
}

func normalizeCustomPermissions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		token := strings.ToLower(strings.TrimSpace(value))
		if !models.IsAllowedCustomPermission(token) {
			continue
		}
		normalized = append(normalized, token)
	}
	return uniqueCustomPermissions(normalized)
}

func uniqueCustomPermissions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		token := strings.ToLower(strings.TrimSpace(value))
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func normalizeAuditPreferences(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	allowed := map[string]struct{}{
		"auth.failed":    {},
		"backup.export":  {},
		"workspace.edit": {},
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		token := strings.ToLower(strings.TrimSpace(value))
		if token == "" {
			continue
		}
		if _, ok := allowed[token]; !ok {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

func parseAuditPreferences(raw string) map[string]bool {
	values := normalizeAuditPreferences(models.ParsePermissionList(raw))
	out := map[string]bool{
		"auth.failed":    false,
		"backup.export":  false,
		"workspace.edit": false,
	}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func normalizeAuditEventFilter(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "auth.failed", "auth.invalid_user", "auth.2fa_failed", "security.tampering":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func normalizeAuditSeverityFilter(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "INFO", "WARNING", "HIGH", "CRITICAL":
		return strings.ToUpper(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func hasCustomPermission(values []string, permission string) bool {
	token := strings.ToLower(strings.TrimSpace(permission))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == token {
			return true
		}
	}
	return false
}

func collectWorkspaceIDs(r *http.Request, fallback ...string) []string {
	values := r.Form["workspace_ids"]
	if len(values) == 0 {
		values = r.Form["workspace_id"]
	}
	if len(values) == 0 {
		values = fallback
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func parseIntRange(v string, min, max int64) (int64, error) {
	n, err := parseInt64(strings.TrimSpace(v))
	if err != nil {
		return 0, err
	}
	if n < min || n > max {
		return 0, fmt.Errorf("out of range")
	}
	return n, nil
}

func parseInt64(v string) (int64, error) {
	var out int64
	_, err := fmt.Sscan(v, &out)
	return out, err
}

func normalizeWorkspaceType(raw string) string {
	return normalizeWorkspaceTypeValue(raw)
}

func normalizeWorkspaceThemeToken(rawToken, workspaceType string) string {
	return NormalizeWorkspaceThemeToken(rawToken, workspaceType)
}

func centsToInput(v int64) string {
	reais := v / 100
	cents := v % 100
	if cents < 0 {
		cents = -cents
	}
	return fmt.Sprintf("%d.%02d", reais, cents)
}

func execOne(db *sql.DB, query string, args ...interface{}) error {
	res, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("no rows affected")
	}
	return nil
}

func isSQLiteForeignKeyConstraint(err error) bool {
	var sqliteErr *sqliteDriver.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

func ensureWorkspaceCascadeDeleted(tx *sql.Tx, workspaceID string) error {
	tables, err := tx.Query(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	`)
	if err != nil {
		return err
	}
	defer tables.Close()

	for tables.Next() {
		var tableName string
		if err := tables.Scan(&tableName); err != nil {
			return err
		}
		hasWorkspaceID, err := tableHasColumn(tx, tableName, "workspace_id")
		if err != nil {
			return err
		}
		if !hasWorkspaceID {
			continue
		}
		query := fmt.Sprintf("SELECT COUNT(1) FROM %s WHERE workspace_id = ?", quoteSQLiteIdentifier(tableName))
		var count int
		if err := tx.QueryRow(query, workspaceID).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("workspace cascade left %d rows in %s", count, tableName)
		}
	}
	if err := tables.Err(); err != nil {
		return err
	}

	fkRows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer fkRows.Close()
	if fkRows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var fkID int
		if err := fkRows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			return err
		}
		return fmt.Errorf("foreign key check failed on %s -> %s", table, parent)
	}
	return fkRows.Err()
}

func tableHasColumn(tx *sql.Tx, tableName, columnName string) (bool, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", quoteSQLiteIdentifier(tableName))
	rows, err := tx.Query(query)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func quoteSQLiteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func queryUserInitials(repo *repository.ConfigRepository, userID string) string {
	name, err := repo.UserNameByID(userID)
	if err != nil {
		return ""
	}
	return initials(name)
}

func defaultNameFromEmail(email string) string {
	local := email
	if idx := strings.Index(local, "@"); idx > 0 {
		local = local[:idx]
	}
	local = strings.ReplaceAll(local, ".", " ")
	local = strings.ReplaceAll(local, "_", " ")
	local = strings.TrimSpace(local)
	if local == "" {
		return "Novo Membro"
	}
	parts := strings.Fields(local)
	for i := range parts {
		runes := []rune(parts[i])
		if len(runes) == 0 {
			continue
		}
		if runes[0] >= 'a' && runes[0] <= 'z' {
			runes[0] = runes[0] - ('a' - 'A')
		}
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}
