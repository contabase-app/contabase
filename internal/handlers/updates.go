package handlers

import (
	"bytes"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/contabase-app/contabase/internal/models"
)

func updateHealthcheckURL(baseURL string) string {
	const fallback = "http://127.0.0.1:8080/health"
	if strings.ContainsAny(baseURL, "?#") || strings.IndexFunc(baseURL, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return fallback
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.ForceQuery || parsed.RawPath != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return fallback
	}
	host := parsed.Hostname()
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" || addr.Is6() != strings.HasPrefix(parsed.Host, "[") {
			return fallback
		}
	} else if strings.ContainsAny(parsed.Host, "[]") || !validUpdateHealthcheckHostname(host) {
		return fallback
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return fallback
	}
	if port := parsed.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fallback
		}
	}
	parsed.Path = "/health"
	return parsed.String()
}

func validUpdateHealthcheckHostname(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 || strings.Trim(host, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func (h *ConfiguracoesHandler) HandleAdminUpdates(w http.ResponseWriter, r *http.Request) {
	if h.ActorRole != models.RoleAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	manual := r.URL.Path == "/admin/atualizacoes/verificar"
	if (manual && r.Method != http.MethodPost) || (!manual && r.Method != http.MethodGet) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	result := h.Updates.Snapshot(manual)
	if manual && r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/admin/atualizacoes", http.StatusSeeOther)
		return
	}
	data := ConfiguracoesData{
		Title: "Atualizações", Section: "admin-updates", ActorRole: h.ActorRole,
		CanManageGlobal: true, CanManageMembers: true, CanManageOps: true,
		CurrentWorkspace: h.WorkspaceID, Updates: result,
		UpdatesCSRFToken:      h.UpdatesCSRFToken,
		UpdatesLastCheck:      FormatAppTime(result.CheckedAt),
		UpdatesHealthcheckURL: updateHealthcheckURL(h.BaseURL),
	}
	templateName := "configuracoes-admin_updates-panel"
	if r.URL.Path == "/admin/atualizacoes" {
		templateName = "configuracoes-admin_updates-content"
		if r.Header.Get("HX-Request") != "true" {
			if err := h.loadConfigShellData(&data); err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			templateName = "configuracoes-admin_updates-page"
		}
	}
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}
