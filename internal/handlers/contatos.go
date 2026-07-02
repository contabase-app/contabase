package handlers

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ContatosHandler struct {
	DB          *sql.DB
	Templates   TemplateEngine
	WorkspaceID string
	UserID      string
}

func respondContatoFormError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<div id="contato-form-error" hx-swap-oob="true" class="rounded-xl border border-rose-500/35 bg-rose-500/10 px-3 py-2 text-sm text-rose-100">%s</div>`, template.HTMLEscapeString(message))
}

type ContatosData struct {
	Title                     string
	UserInitials              string
	ProfilePhotoURL           string
	Query                     string
	Tipo                      string
	TipoLabel                 string
	FabOOB                    bool
	Contatos                  []ContatoRow
	Type                      string
	CustomClientID            string
	CustomClientIDPlaceholder string
	Document                  string
	Name                      string
	Email                     string
	Phone                     string
	ActiveWorkspaceName       string
}

type ContatoRow struct {
	ID                        string
	CustomClientID            string
	CustomClientIDPlaceholder string
	Name                      string
	Document                  string
	Type                      string
	TypeLabel                 string
	Email                     string
	Phone                     string
	CreatedAt                 string
}

func (h *ContatosHandler) HandleContatosConceito(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	tipo := strings.TrimSpace(r.URL.Query().Get("tipo"))
	if r.URL.Query().Get("partial") == "lista" {
		data, err := h.buildContatosListData(query, tipo, r.Header.Get("HX-Request") != "")
		if err != nil {
			log.Printf("build contatos list error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		var buf bytes.Buffer
		if err := h.Templates.ExecuteTemplate(&buf, "contatos-list", data); err != nil {
			log.Printf("template contatos list error: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		buf.WriteTo(w)
		return
	}

	data, err := h.buildContatosPageData(query, tipo, r.Header.Get("HX-Request") != "")
	if err != nil {
		log.Printf("build contatos page error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "contatos-page", data); err != nil {
		log.Printf("template contatos error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// createContactRow valida e insere um contato no workspace atual dentro de tx.
// Reutilizada por HandleCriarContato (CRUD da página) e pela criação inline no
// formulário de lançamento, evitando duplicar a lógica de validação.
func createContactRow(tx *sql.Tx, workspaceID, name, contactType, document, email, phone, customClientIDInput string) (ContatoRow, error) {
	customClientID, err := resolveCustomClientID(tx, workspaceID, customClientIDInput, "")
	if err != nil {
		return ContatoRow{}, err
	}
	id := uuid.NewString()
	now := time.Now().Unix()
	if _, err := tx.Exec(`
		INSERT INTO contacts (id, workspace_id, custom_client_id, name, document, type, email, phone, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, workspaceID, customClientID, name, document, contactType, email, phone, now); err != nil {
		if isContactCustomIDUniqueViolation(err) {
			return ContatoRow{}, errDuplicateCustomClientID
		}
		return ContatoRow{}, err
	}
	return ContatoRow{
		ID:             id,
		CustomClientID: customClientID,
		Name:           name,
		Document:       document,
		Type:           contactType,
		TypeLabel:      contactTypeLabel(contactType),
		Email:          email,
		Phone:          phone,
	}, nil
}

func (h *ContatosHandler) HandleCriarContato(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		respondContatoFormError(w, "formulário inválido")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	contactType := normalizeContactType(r.FormValue("type"))
	if name == "" || contactType == "" {
		respondContatoFormError(w, "nome e tipo são obrigatórios")
		return
	}
	customClientIDInput := normalizeCustomClientID(r.FormValue("custom_client_id"))
	document := strings.TrimSpace(r.FormValue("document"))
	email := strings.TrimSpace(r.FormValue("email"))
	phone := strings.TrimSpace(r.FormValue("phone"))

	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "erro ao criar contato", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()
	_, err = createContactRow(tx, h.WorkspaceID, name, contactType, document, email, phone, customClientIDInput)
	if err != nil {
		if errors.Is(err, errDuplicateCustomClientID) {
			respondContatoFormError(w, "Este ID de cliente já está em uso neste workspace.")
			return
		}
		log.Printf("insert contact error: %v", err)
		respondContatoFormError(w, "erro ao criar contato")
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao criar contato", http.StatusInternalServerError)
		return
	}
	h.renderContatosList(w, "")
}

func (h *ContatosHandler) HandleOptionsContato(w http.ResponseWriter, r *http.Request) {
	contatos, err := h.queryContatos("")
	if err != nil {
		log.Printf("query contatos options error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "contatos-options", contatos); err != nil {
		log.Printf("template contatos-options error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ContatosHandler) HandleDetalheContato(w http.ResponseWriter, r *http.Request, id string) {
	row, err := h.queryContatoByID(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "contato não encontrado", http.StatusNotFound)
			return
		}
		log.Printf("query contact detail error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "contato-row-form", row); err != nil {
		log.Printf("template contato-row-form error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ContatosHandler) HandleAtualizarContato(w http.ResponseWriter, r *http.Request, id string) {
	if err := r.ParseForm(); err != nil {
		respondContatoFormError(w, "formulário inválido")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	contactType := normalizeContactType(r.FormValue("type"))
	if name == "" || contactType == "" {
		respondContatoFormError(w, "nome e tipo são obrigatórios")
		return
	}
	customClientIDInput := normalizeCustomClientID(r.FormValue("custom_client_id"))
	tx, err := h.DB.Begin()
	if err != nil {
		http.Error(w, "erro ao atualizar contato", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	customClientID, err := resolveCustomClientID(tx, h.WorkspaceID, customClientIDInput, id)
	if err != nil {
		if errors.Is(err, errDuplicateCustomClientID) {
			respondContatoFormError(w, "Este ID de cliente já está em uso neste workspace.")
			return
		}
		log.Printf("resolve custom client id error: %v", err)
		respondContatoFormError(w, "erro ao atualizar contato")
		return
	}

	res, err := tx.Exec(`
		UPDATE contacts
		SET custom_client_id = ?, name = ?, document = ?, type = ?, email = ?, phone = ?
		WHERE id = ? AND workspace_id = ?
	`, customClientID, name, strings.TrimSpace(r.FormValue("document")), contactType, strings.TrimSpace(r.FormValue("email")), strings.TrimSpace(r.FormValue("phone")), id, h.WorkspaceID)
	if err != nil {
		if isContactCustomIDUniqueViolation(err) {
			respondContatoFormError(w, "Este ID de cliente já está em uso neste workspace.")
			return
		}
		log.Printf("update contact error: %v", err)
		respondContatoFormError(w, "erro ao atualizar contato")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil || affected != 1 {
		http.Error(w, "erro ao atualizar contato", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "erro ao atualizar contato", http.StatusInternalServerError)
		return
	}
	row, err := h.queryContatoByID(id)
	if err != nil {
		log.Printf("query contact after update error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "contato-row", row); err != nil {
		log.Printf("template contato-row error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

func (h *ContatosHandler) HandleExcluirContato(w http.ResponseWriter, r *http.Request, id string) {
	if err := execOne(h.DB, `DELETE FROM contacts WHERE id = ? AND workspace_id = ?`, id, h.WorkspaceID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed") {
			w.Header().Set("HX-Trigger", `{"mostrarAlerta":"Não é possível excluir um contato que possui lançamentos financeiros ativos."}`)
			http.Error(w, "contato com lançamentos vinculados", http.StatusUnprocessableEntity)
			return
		}
		log.Printf("delete contact error: %v", err)
		http.Error(w, "erro ao excluir contato", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *ContatosHandler) renderContatosList(w http.ResponseWriter, query string) {
	data, err := h.buildContatosListData(query, "", false)
	if err != nil {
		log.Printf("build contatos list error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "contatos-list", data); err != nil {
		log.Printf("template contatos-list error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteString(`<div id="bottom-sheet-container" hx-swap-oob="innerHTML"></div>`)
	buf.WriteString(`<div id="contato-form-error" hx-swap-oob="true" class="hidden"></div>`)
	buf.WriteTo(w)
}

func (h *ContatosHandler) buildContatosListData(query, tipo string, fabOOB bool) (ContatosData, error) {
	contatos, err := h.queryContatos(query)
	if err != nil {
		return ContatosData{}, err
	}
	data := ContatosData{
		Title:    "Contatos",
		Query:    query,
		Tipo:     tipo,
		FabOOB:   fabOOB,
		Contatos: contatos,
	}
	if tipo == "client" {
		data.TipoLabel = "Clientes"
		filtered := make([]ContatoRow, 0, len(data.Contatos))
		for _, c := range data.Contatos {
			if c.Type == "client" {
				filtered = append(filtered, c)
			}
		}
		data.Contatos = filtered
	} else if tipo == "vendor" {
		data.TipoLabel = "Fornecedores"
		filtered := make([]ContatoRow, 0, len(data.Contatos))
		for _, c := range data.Contatos {
			if c.Type == "vendor" {
				filtered = append(filtered, c)
			}
		}
		data.Contatos = filtered
	}
	return data, nil
}

func (h *ContatosHandler) buildContatosPageData(query, tipo string, fabOOB bool) (ContatosData, error) {
	data, err := h.buildContatosListData(query, tipo, fabOOB)
	if err != nil {
		return ContatosData{}, err
	}
	data.CustomClientIDPlaceholder = "CLI-001"
	data.UserInitials = queryUserInitialsByID(h.DB, h.UserID)
	data.ProfilePhotoURL = queryUserProfilePhotoURL(h.DB, h.UserID)
	data.ActiveWorkspaceName = queryWorkspaceName(h.DB, h.WorkspaceID)
	return data, nil
}

func (h *ContatosHandler) queryContatos(query string) ([]ContatoRow, error) {
	nameExpr := "UNACCENT(COALESCE(name, ''))"
	sqlQuery := `
		SELECT id, COALESCE(custom_client_id, ''), name, COALESCE(document, ''), type, COALESCE(email, ''), COALESCE(phone, ''), created_at
		FROM contacts
		WHERE workspace_id = ?
	`
	args := []interface{}{h.WorkspaceID}
	if query != "" {
		like := "%" + query + "%"
		sqlQuery += fmt.Sprintf(`
			AND (
				%s LIKE UNACCENT(?)
				OR UNACCENT(COALESCE(custom_client_id, '')) LIKE UNACCENT(?)
				OR UNACCENT(COALESCE(document, '')) LIKE UNACCENT(?)
				OR UNACCENT(COALESCE(phone, '')) LIKE UNACCENT(?)
			)
		`, nameExpr)
		args = append(args, like, like, like, like)
	}
	sqlQuery += ` ORDER BY name COLLATE NOCASE ASC, created_at DESC`
	rows, err := h.DB.Query(sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContatoRow
	for rows.Next() {
		var row ContatoRow
		var createdAt int64
		if err := rows.Scan(&row.ID, &row.CustomClientID, &row.Name, &row.Document, &row.Type, &row.Email, &row.Phone, &createdAt); err != nil {
			return nil, err
		}
		row.TypeLabel = contactTypeLabel(row.Type)
		row.CreatedAt = formatDateTimeLabel(createdAt)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (h *ContatosHandler) queryContatoByID(id string) (ContatoRow, error) {
	var row ContatoRow
	var createdAt int64
	err := h.DB.QueryRow(`
		SELECT id, COALESCE(custom_client_id, ''), name, COALESCE(document, ''), type, COALESCE(email, ''), COALESCE(phone, ''), created_at
		FROM contacts
		WHERE id = ? AND workspace_id = ?
	`, id, h.WorkspaceID).Scan(&row.ID, &row.CustomClientID, &row.Name, &row.Document, &row.Type, &row.Email, &row.Phone, &createdAt)
	if err != nil {
		return row, err
	}
	row.TypeLabel = contactTypeLabel(row.Type)
	row.CreatedAt = formatDateTimeLabel(createdAt)
	return row, nil
}

func normalizeContactType(v string) string {
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "client", "cliente":
		return "client"
	case "vendor", "fornecedor":
		return "vendor"
	default:
		return ""
	}
}

// splitNameDocument separa "Nome · Documento" em name e document quando
// a segunda parte contém dígitos suficientes para parecer CPF/CNPJ.
// Separadores aceitos: " · ", " - ", ", ".
func splitNameDocument(raw string) (name, document string) {
	name = strings.TrimSpace(raw)
	if name == "" {
		return "", ""
	}
	separators := []string{" · ", " - ", ", "}
	for _, sep := range separators {
		if idx := strings.LastIndex(raw, sep); idx > 0 {
			candidate := strings.TrimSpace(raw[idx+len(sep):])
			digits := stripNonDigits(candidate)
			if len(digits) >= 3 {
				return strings.TrimSpace(raw[:idx]), candidate
			}
		}
	}
	return name, ""
}

func stripNonDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func contactTypeLabel(v string) string {
	if v == "client" {
		return "Cliente"
	}
	return "Fornecedor"
}

var errDuplicateCustomClientID = errors.New("duplicate custom client id")

func normalizeCustomClientID(v string) string {
	return strings.ToUpper(strings.TrimSpace(v))
}

func resolveCustomClientID(tx *sql.Tx, workspaceID, requestedID, exceptContactID string) (string, error) {
	candidate := requestedID
	if candidate == "" {
		nextID, err := nextSequentialClientID(tx, workspaceID)
		if err != nil {
			return "", err
		}
		candidate = nextID
	}

	countQuery := `SELECT COUNT(1) FROM contacts WHERE workspace_id = ? AND custom_client_id = ?`
	args := []interface{}{workspaceID, candidate}
	if exceptContactID != "" {
		countQuery += ` AND id <> ?`
		args = append(args, exceptContactID)
	}
	var count int
	if err := tx.QueryRow(countQuery, args...).Scan(&count); err != nil {
		return "", err
	}
	if count > 0 {
		return "", errDuplicateCustomClientID
	}
	return candidate, nil
}

func nextSequentialClientID(tx *sql.Tx, workspaceID string) (string, error) {
	var maxSeq int64
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(CAST(SUBSTR(custom_client_id, 5) AS INTEGER)), 0)
		FROM contacts
		WHERE workspace_id = ?
		  AND custom_client_id GLOB 'CLI-[0-9][0-9][0-9]*'
	`, workspaceID).Scan(&maxSeq); err != nil {
		return "", err
	}
	return fmt.Sprintf("CLI-%03d", maxSeq+1), nil
}

// ContatoInlineCreateData alimenta o template contato-picker-create (criação
// inline a partir do formulário de lançamento em Workspace Business).
type ContatoInlineCreateData struct {
	Name                      string
	Type                      string
	Error                     string
	Document                  string
	Email                     string
	Phone                     string
	CustomClientID            string
	CustomClientIDPlaceholder string
}

// HandleContatoInlineForm renderiza o formulário de criação rápida de contato
// dentro do seletor do formulário de lançamento. Reutiliza o chrome do picker
// (#modalPickerBackdrop) para integrar com a pilha de overlays.
func (h *ContatosHandler) HandleContatoInlineForm(w http.ResponseWriter, r *http.Request) {
	if workspaceType(h.DB, h.WorkspaceID) != "business" {
		http.NotFound(w, r)
		return
	}
	rawName := strings.TrimSpace(r.URL.Query().Get("name"))
	contactType := normalizeContactType(r.URL.Query().Get("type"))
	if contactType == "" {
		contactType = "vendor"
	}
	name, document := splitNameDocument(rawName)
	data := ContatoInlineCreateData{
		Name:                      name,
		Type:                      contactType,
		Document:                  document,
		CustomClientIDPlaceholder: "CLI-001",
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "contato-picker-create", data); err != nil {
		log.Printf("template contato-picker-create error: %v", err)
	}
}

// HandleContatoInlineCreate cria um contato no workspace atual a partir do
// formulário de lançamento e dispara HX-Trigger contactCreated para que o
// cliente preencha o lançamento com o novo contato selecionado.
func (h *ContatosHandler) HandleContatoInlineCreate(w http.ResponseWriter, r *http.Request) {
	if workspaceType(h.DB, h.WorkspaceID) != "business" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderContatoInlineError(w, r, "formulário inválido")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	contactType := normalizeContactType(r.FormValue("type"))
	if name == "" {
		h.renderContatoInlineError(w, r, "Nome do contato é obrigatório.")
		return
	}
	if contactType == "" {
		h.renderContatoInlineError(w, r, "Tipo do contato é obrigatório.")
		return
	}
	customClientIDInput := normalizeCustomClientID(r.FormValue("custom_client_id"))
	document := strings.TrimSpace(r.FormValue("document"))
	email := strings.TrimSpace(r.FormValue("email"))
	phone := strings.TrimSpace(r.FormValue("phone"))

	tx, err := h.DB.Begin()
	if err != nil {
		h.renderContatoInlineError(w, r, "erro ao criar contato")
		return
	}
	defer tx.Rollback()
	row, err := createContactRow(tx, h.WorkspaceID, name, contactType, document, email, phone, customClientIDInput)
	if err != nil {
		if errors.Is(err, errDuplicateCustomClientID) {
			h.renderContatoInlineError(w, r, "Este ID de cliente já está em uso neste workspace.")
			return
		}
		log.Printf("inline create contact error: %v", err)
		h.renderContatoInlineError(w, r, "erro ao criar contato")
		return
	}
	if err := tx.Commit(); err != nil {
		h.renderContatoInlineError(w, r, "erro ao criar contato")
		return
	}
	triggerData := fmt.Sprintf(
		`{"contactCreated": {"id":"%s","name":"%s","document":"%s","type":"%s","typeLabel":"%s","customClientId":"%s"}}`,
		row.ID, template.JSEscapeString(row.Name), template.JSEscapeString(row.Document), row.Type, row.TypeLabel, template.JSEscapeString(row.CustomClientID),
	)
	w.Header().Set("HX-Trigger", triggerData)
	w.WriteHeader(http.StatusOK)
}

func (h *ContatosHandler) renderContatoInlineError(w http.ResponseWriter, r *http.Request, errMsg string) {
	name := strings.TrimSpace(r.FormValue("name"))
	contactType := normalizeContactType(r.FormValue("type"))
	if contactType == "" {
		contactType = "vendor"
	}
	data := ContatoInlineCreateData{
		Name:           name,
		Type:           contactType,
		Error:          errMsg,
		Document:       strings.TrimSpace(r.FormValue("document")),
		Email:          strings.TrimSpace(r.FormValue("email")),
		Phone:          strings.TrimSpace(r.FormValue("phone")),
		CustomClientID: normalizeCustomClientID(r.FormValue("custom_client_id")),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "contato-picker-create", data); err != nil {
		log.Printf("template contato-picker-create error: %v", err)
	}
}

func isContactCustomIDUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") &&
		(strings.Contains(msg, "contacts.workspace_id, contacts.custom_client_id") || strings.Contains(msg, "idx_contacts_workspace_custom_client_id_unique"))
}
