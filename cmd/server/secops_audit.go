package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/contabase-app/contabase/internal/security"

	"github.com/google/uuid"
)

type requestContextKey string

const authContextKey requestContextKey = "auth_context"

const (
	secOpsEventHeader      = "X-SecOps-Event"
	secOpsSeverityHeader   = "X-SecOps-Severity"
	secOpsMetaReasonHeader = "X-SecOps-Meta-Reason"
	secOpsMetaEmailHeader  = "X-SecOps-Meta-Email-Tentado"
	secOpsMetaHeaderPrefix = "X-SecOps-Meta-"
)

type securityAuditService struct {
	dbProvider func() *sql.DB
	notifier   *smtpNotifier
}

type securityEvent struct {
	Type     string
	Severity string
	Status   string
}

type smtpAlertConfig struct {
	Host              string
	Port              int
	User              string
	Pass              string
	NotificationEmail string
	Preferences       map[string]struct{}
}

type smtpNotifier struct {
	jobs chan smtpJob
}

type smtpJob struct {
	Config  smtpAlertConfig
	Subject string
	Body    string
}

func newSecurityAuditService(dbProvider func() *sql.DB) *securityAuditService {
	notifier := &smtpNotifier{jobs: make(chan smtpJob, 128)}
	go notifier.run()
	return &securityAuditService{
		dbProvider: dbProvider,
		notifier:   notifier,
	}
}

func logSecurityEventAsync(db *sql.DB, r *http.Request, workspaceID, userID, eventType, severity string, metadata map[string]any) {
	if db == nil || r == nil {
		return
	}
	eventType = strings.ToLower(strings.TrimSpace(eventType))
	if eventType == "" {
		return
	}
	severity = strings.ToUpper(strings.TrimSpace(severity))
	if severity == "" {
		severity = defaultSeverityForEvent(eventType)
	}
	payload := map[string]any{
		"path":   r.URL.Path,
		"method": r.Method,
		"status": "blocked",
	}
	for key, value := range metadata {
		payload[key] = value
	}
	go func() {
		metaRaw, err := json.Marshal(payload)
		if err != nil {
			slog.Error("security async log marshal failed", "event_type", eventType, "error", err)
			return
		}
		if _, err := db.Exec(`
			INSERT INTO security_logs (
				id, workspace_id, user_id, event_type, severity, ip_address, user_agent, metadata, created_at
			) VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, unixepoch())
		`, uuid.NewString(), strings.TrimSpace(workspaceID), strings.TrimSpace(userID), eventType, severity, clientIP(r), strings.TrimSpace(r.UserAgent()), string(metaRaw)); err != nil {
			slog.Error("security async log insert failed", "event_type", eventType, "error", err)
		}
	}()
}

func (s *securityAuditService) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)

		status := recorder.status()
		event, ok := classifySecurityEvent(r, status, recorder.secOpsHeaders())
		if !ok {
			return
		}

		ctx, _ := authContextFromRequest(r)
		workspaceID := strings.TrimSpace(ctx.ActiveWorkspaceID)
		userID := strings.TrimSpace(ctx.UserID)
		if err := s.recordEvent(r, recorder.secOpsHeaders(), workspaceID, userID, status, event); err != nil {
			slog.Error("security audit insert failed", "event_type", event.Type, "error", err)
		}
	})
}

func (s *securityAuditService) recordEvent(r *http.Request, responseHeaders http.Header, workspaceID, userID string, httpStatus int, event securityEvent) error {
	db := s.dbProvider()
	if db == nil {
		return fmt.Errorf("nil database")
	}

	secOpsMeta := secOpsMetadataFromHeaders(responseHeaders)

	if workspaceID == "" {
		if wsID, ok := secOpsMeta["workspace_id"].(string); ok && strings.TrimSpace(wsID) != "" {
			workspaceID = strings.TrimSpace(wsID)
			delete(secOpsMeta, "workspace_id")
		}
	}

	metadata := map[string]any{
		"path":   r.URL.Path,
		"method": r.Method,
		"status": event.Status,
		"code":   httpStatus,
	}
	for key, value := range secOpsMeta {
		metadata[key] = value
	}
	if _, exists := metadata["email_tentado"]; !exists {
		if email := loginAttemptEmail(r); email != "" {
			metadata["email_tentado"] = email
		} else if email := s.lookupUserEmail(userID); email != "" {
			metadata["email_tentado"] = email
		}
	}
	if _, exists := metadata["reason"]; !exists {
		if reason := defaultReasonForEvent(event.Type); reason != "" {
			metadata["reason"] = reason
		}
	}
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}

	if _, err := db.Exec(`
		INSERT INTO security_logs (
			id, workspace_id, user_id, event_type, severity, ip_address, user_agent, metadata, created_at
		) VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, unixepoch())
	`, uuid.NewString(), workspaceID, userID, event.Type, event.Severity, clientIP(r), strings.TrimSpace(r.UserAgent()), string(metadataRaw)); err != nil {
		return err
	}

	if workspaceID == "" {
		return nil
	}
	cfg, err := s.loadAlertConfig(db, workspaceID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.NotificationEmail) == "" {
		return nil
	}
	if !eventEmailEnabled(cfg.Preferences, event.Type) {
		return nil
	}
	if strings.TrimSpace(cfg.Host) == "" || cfg.Port <= 0 {
		return nil
	}

	s.notifier.sendAsync(cfg, emailSubjectForEvent(event.Type), buildAlertBody(r, event))
	return nil
}

func (s *securityAuditService) lookupUserEmail(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ""
	}
	db := s.dbProvider()
	if db == nil {
		return ""
	}
	var email string
	if err := db.QueryRow(`SELECT LOWER(TRIM(email)) FROM users WHERE id = ?`, userID).Scan(&email); err != nil {
		return ""
	}
	return strings.TrimSpace(email)
}

func (s *securityAuditService) loadAlertConfig(db *sql.DB, workspaceID string) (smtpAlertConfig, error) {
	var cfg smtpAlertConfig
	var (
		host              sql.NullString
		port              sql.NullInt64
		user              sql.NullString
		pass              sql.NullString
		notificationEmail sql.NullString
		preferencesRaw    sql.NullString
	)
	err := db.QueryRow(`
		SELECT
			COALESCE(smtp_host, ''),
			COALESCE(smtp_port, 587),
			COALESCE(smtp_user, ''),
			COALESCE(smtp_pass, ''),
			COALESCE(notification_email, ''),
			COALESCE(email_preferences, '[]')
		FROM workspaces
		WHERE id = ?
	`, workspaceID).Scan(&host, &port, &user, &pass, &notificationEmail, &preferencesRaw)
	if err != nil {
		return cfg, err
	}

	cfg.Host = strings.TrimSpace(host.String)
	cfg.Port = int(port.Int64)
	cfg.User = strings.TrimSpace(user.String)
	if strings.TrimSpace(pass.String) != "" {
		clearPass, err := security.Decrypt(pass.String)
		if err != nil {
			return cfg, err
		}
		cfg.Pass = clearPass
	}
	cfg.NotificationEmail = strings.TrimSpace(notificationEmail.String)
	cfg.Preferences = parseEmailPreferences(preferencesRaw.String)
	return cfg, nil
}

func parseEmailPreferences(raw string) map[string]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "[]"
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return map[string]struct{}{}
	}
	out := make(map[string]struct{}, len(values))
	for _, item := range values {
		token := strings.ToLower(strings.TrimSpace(item))
		if token == "" {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func emailSubjectForEvent(eventType string) string {
	return "[ContaBase] Alerta de Segurança: " + eventType
}

func eventEmailEnabled(preferences map[string]struct{}, eventType string) bool {
	if _, ok := preferences[eventType]; ok {
		return true
	}
	if eventType == "auth.2fa_failed" || eventType == "auth.unauthorized" {
		_, ok := preferences["auth.failed"]
		return ok
	}
	if eventType == "security.tampering" {
		return true
	}
	return false
}

func buildAlertBody(r *http.Request, event securityEvent) string {
	occurredAt := time.Now().Format("2006-01-02 15:04:05 -0700")
	return "Evento: " + event.Type + "\n" +
		"Severidade: " + event.Severity + "\n" +
		"Status: " + event.Status + "\n" +
		"IP: " + clientIP(r) + "\n" +
		"Dispositivo: " + strings.TrimSpace(r.UserAgent()) + "\n" +
		"Momento: " + occurredAt + "\n"
}

func classifySecurityEvent(r *http.Request, status int, headers http.Header) (securityEvent, bool) {
	path := strings.TrimSpace(r.URL.Path)
	if path == "/login" {
		// Login failures are logged actively by the auth handler.
		return securityEvent{}, false
	}

	if overrideEvent := strings.ToLower(strings.TrimSpace(headers.Get(secOpsEventHeader))); overrideEvent != "" {
		severity := strings.ToUpper(strings.TrimSpace(headers.Get(secOpsSeverityHeader)))
		if severity == "" {
			severity = defaultSeverityForEvent(overrideEvent)
		}
		return securityEvent{Type: overrideEvent, Severity: severity, Status: "blocked"}, true
	}
	if path == "/login/2fa" && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return securityEvent{Type: "auth.2fa_failed", Severity: "HIGH", Status: "blocked"}, true
	}
	if path == "/login" && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		return securityEvent{Type: "auth.failed", Severity: "HIGH", Status: "blocked"}, true
	}

	if status == http.StatusUnauthorized {
		return securityEvent{Type: "auth.failed", Severity: "HIGH", Status: "blocked"}, true
	}
	if status == http.StatusForbidden {
		return securityEvent{Type: "auth.unauthorized", Severity: "HIGH", Status: "blocked"}, true
	}

	if status < 200 || status >= 400 {
		return securityEvent{}, false
	}

	switch {
	case r.Method == http.MethodGet && path == "/admin/backups/exportar":
		return securityEvent{Type: "backup.export", Severity: "INFO", Status: "success"}, true
	case r.Method == http.MethodPost && (path == "/admin/workspaces/salvar" || path == "/admin/workspaces/editar" || path == "/admin/usuarios/salvar" || path == "/admin/usuarios/reset-senha" || path == "/admin/usuarios/desativar-2fa" || path == "/admin/usuarios/revogar-sessoes"):
		return securityEvent{Type: "workspace.edit", Severity: "INFO", Status: "success"}, true
	case r.Method == http.MethodDelete && (strings.HasPrefix(path, "/workspaces/") || strings.HasPrefix(path, "/users/")):
		return securityEvent{Type: "workspace.edit", Severity: "INFO", Status: "success"}, true
	default:
		return securityEvent{}, false
	}
}

func loginAttemptEmail(r *http.Request) string {
	path := strings.TrimSpace(r.URL.Path)
	if path != "/login" && path != "/login/2fa" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(r.FormValue("email")))
}

func secOpsMetadataFromHeaders(headers http.Header) map[string]any {
	out := make(map[string]any)
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		canonical := http.CanonicalHeaderKey(key)
		if !strings.HasPrefix(canonical, secOpsMetaHeaderPrefix) {
			continue
		}
		metaKey := strings.ToLower(strings.TrimPrefix(canonical, secOpsMetaHeaderPrefix))
		metaKey = strings.ReplaceAll(metaKey, "-", "_")
		if metaKey == "" {
			continue
		}
		value := strings.TrimSpace(values[0])
		if value == "" {
			continue
		}
		out[metaKey] = value
	}
	return out
}

func defaultReasonForEvent(eventType string) string {
	switch eventType {
	case "auth.invalid_user":
		return "unknown_identity"
	case "auth.failed":
		return "invalid_password"
	case "auth.2fa_failed":
		return "invalid_totp"
	case "security.tampering":
		return "scope_violation"
	default:
		return ""
	}
}

func defaultSeverityForEvent(eventType string) string {
	switch eventType {
	case "auth.invalid_user":
		return "WARNING"
	case "auth.2fa_failed":
		return "HIGH"
	case "security.tampering":
		return "CRITICAL"
	case "auth.failed", "auth.unauthorized":
		return "HIGH"
	default:
		return "INFO"
	}
}

func authContextFromRequest(r *http.Request) (authContext, bool) {
	raw := r.Context().Value(authContextKey)
	if raw == nil {
		return authContext{}, false
	}
	ctx, ok := raw.(authContext)
	return ctx, ok
}

func (n *smtpNotifier) sendAsync(cfg smtpAlertConfig, subject, body string) {
	select {
	case n.jobs <- smtpJob{Config: cfg, Subject: subject, Body: body}:
	default:
		slog.Warn("smtp queue is full; dropping alert email")
	}
}

func (n *smtpNotifier) run() {
	for job := range n.jobs {
		if err := sendSMTP(job); err != nil {
			slog.Error("smtp alert delivery failed", "to", job.Config.NotificationEmail, "error", err)
		}
	}
}

func sendSMTP(job smtpJob) error {
	cfg := job.Config
	to := strings.TrimSpace(cfg.NotificationEmail)
	if to == "" {
		return nil
	}
	from := strings.TrimSpace(cfg.User)
	if from == "" {
		from = to
	}

	headers := []string{
		"To: " + to,
		"Subject: " + job.Subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
	}
	payload := strings.Join(headers, "\r\n") + "\r\n\r\n" + job.Body

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var auth smtp.Auth
	if strings.TrimSpace(cfg.User) != "" || strings.TrimSpace(cfg.Pass) != "" {
		auth = smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
	}
	return smtp.SendMail(addr, auth, from, []string{to}, []byte(payload))
}

type statusRecorder struct {
	http.ResponseWriter
	code                int
	capturedSecOpsHeads http.Header
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.captureAndStripSecOpsHeaders()
	r.code = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.captureAndStripSecOpsHeaders()
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) status() int {
	if r.code == 0 {
		return http.StatusOK
	}
	return r.code
}

func (r *statusRecorder) secOpsHeaders() http.Header {
	if r.capturedSecOpsHeads == nil {
		return http.Header{}
	}
	return r.capturedSecOpsHeads
}

func (r *statusRecorder) captureAndStripSecOpsHeaders() {
	headers := r.Header()
	for key, values := range headers {
		canonical := http.CanonicalHeaderKey(key)
		if canonical != secOpsEventHeader && canonical != secOpsSeverityHeader && !strings.HasPrefix(canonical, secOpsMetaHeaderPrefix) {
			continue
		}
		if r.capturedSecOpsHeads == nil {
			r.capturedSecOpsHeads = make(http.Header)
		}
		for _, value := range values {
			r.capturedSecOpsHeads.Add(canonical, value)
		}
		headers.Del(key)
	}
}
