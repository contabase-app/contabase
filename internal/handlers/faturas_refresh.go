package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func faturasIndexCurrentRequest(r *http.Request) *http.Request {
	if r == nil {
		return nil
	}
	raw := r.Header.Get("HX-Current-URL")
	if raw == "" {
		raw = r.Referer()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path != "/faturas" {
		return nil
	}
	current := r.Clone(r.Context())
	current.Method = http.MethodGet
	current.URL = &url.URL{Path: "/faturas", RawQuery: u.RawQuery}
	return current
}

// Called only after the existing financial transaction has committed. One
// buffered OOB region owns both the rail and the complete selected invoice.
func (h *FaturasHandler) renderFaturasIndexMutation(w http.ResponseWriter, r *http.Request, invoiceID string, closeSheet bool) (handled bool, renderErr error) {
	current := faturasIndexCurrentRequest(r)
	if current == nil {
		return false, nil
	}
	defer func() {
		if renderErr == nil {
			return
		}
		// A read that cannot load financial data must surface as unavailable,
		// not as a successful redirect that would repeat the same failure.
		if r.Method == http.MethodGet && errors.Is(renderErr, errInvoiceFinancialUnavailable) {
			return
		}
		log.Printf("render committed invoice view error: %v", renderErr)
		// The mutation is committed. Recover with a GET, never invite a retry.
		w.Header().Del("HX-Trigger")
		w.Header().Set("HX-Redirect", current.URL.RequestURI())
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusOK)
		renderErr = nil
	}()
	// Old clients may still submit from the unqualified entry URL. Pin the
	// affected invoice before reading instead of rerunning automatic selection.
	if current.URL.Query().Get("mes") == "" && current.URL.Query().Get("ano") == "" {
		var reference, accountID string
		if err := h.DB.QueryRow(`SELECT i.reference, i.account_id FROM invoices i
			JOIN accounts a ON a.id = i.account_id WHERE i.id = ? AND a.workspace_id = ?`, invoiceID, h.WorkspaceID).Scan(&reference, &accountID); err != nil {
			return true, err
		}
		year, month, err := parseInvoiceReference(reference)
		if err != nil {
			return true, err
		}
		values := current.URL.Query()
		values.Set("mes", strconv.Itoa(int(month)))
		values.Set("ano", strconv.Itoa(year))
		values.Set("cartao", accountID)
		current.URL.RawQuery = values.Encode()
	}

	data, err := h.buildFaturasIndexData(current)
	if err != nil {
		return true, err
	}
	data.OOB = true
	var buf bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&buf, "faturas-index-state", data); err != nil {
		return true, err
	}
	if closeSheet {
		buf.WriteString(`<div id="bottom-sheet-container" hx-swap-oob="innerHTML"></div>`)
	}
	// Other financial listeners still receive the event. The hub already has
	// its snapshot and must not issue a competing refresh from that same event.
	triggers := map[string]interface{}{}
	raw := strings.TrimSpace(w.Header().Get("HX-Trigger"))
	if strings.HasPrefix(raw, "{") {
		_ = json.Unmarshal([]byte(raw), &triggers)
	} else if raw != "" {
		for _, name := range strings.Split(raw, ",") {
			triggers[strings.TrimSpace(name)] = true
		}
	}
	triggers["refreshFinancials"] = map[string]bool{"invoiceSnapshot": true}
	encoded, err := json.Marshal(triggers)
	if err != nil {
		return true, err
	}
	w.Header().Set("HX-Trigger", string(encoded))
	w.Header().Set("HX-Reswap", "none")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = buf.WriteTo(w)
	return true, err
}
