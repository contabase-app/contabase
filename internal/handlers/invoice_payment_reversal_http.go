package handlers

import (
	"database/sql"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const invoicePaymentReversalReasonMaxRunes = 500

type invoicePaymentReversalFunc func(*sql.DB, invoicePaymentReversalCommand) (invoicePaymentReversalResult, error)

func (h *FaturasHandler) HandleReverseInvoicePayment(w http.ResponseWriter, r *http.Request) {
	h.handleReverseInvoicePayment(w, r, time.Now(), reverseInvoicePayment)
}

func (h *FaturasHandler) handleReverseInvoicePayment(
	w http.ResponseWriter,
	r *http.Request,
	now time.Time,
	reverse invoicePaymentReversalFunc,
) {
	paymentID, reason, ok := parseInvoicePaymentReversalForm(w, r)
	if !ok {
		return
	}
	if h.DB == nil || reverse == nil {
		logInvoicePaymentReversalFailure(h, paymentID, "unavailable_dependency")
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	result, err := reverse(h.DB, invoicePaymentReversalCommand{
		WorkspaceID:       h.WorkspaceID,
		InvoicePaymentID:  paymentID,
		ActorID:           h.UserID,
		Reason:            reason,
		ReversedAt:        now.Unix(),
		ReversedDateCivil: FormatCivilDate(civilDateAt(now, appTimeLocation())),
	})
	if err != nil {
		writeInvoicePaymentReversalError(w, h, paymentID, err)
		return
	}

	location := "/cartoes/faturas/" + url.PathEscape(result.InvoiceID)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func parseInvoicePaymentReversalForm(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if strings.TrimSpace(r.URL.RawQuery) != "" {
		http.Error(w, "campos inválidos", http.StatusBadRequest)
		return "", "", false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		http.Error(w, "dados inválidos", http.StatusBadRequest)
		return "", "", false
	}
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "requisição muito grande", http.StatusRequestEntityTooLarge)
			return "", "", false
		}
		http.Error(w, "dados inválidos", http.StatusBadRequest)
		return "", "", false
	}

	allowed := map[string]struct{}{
		"invoice_payment_id": {},
		"reason":             {},
		"csrf_token":         {},
	}
	for key, values := range r.PostForm {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			http.Error(w, "campos inválidos", http.StatusBadRequest)
			return "", "", false
		}
	}

	paymentValues, paymentOK := r.PostForm["invoice_payment_id"]
	reasonValues, reasonOK := r.PostForm["reason"]
	if !paymentOK || len(paymentValues) != 1 {
		http.Error(w, "pagamento de fatura inválido", http.StatusBadRequest)
		return "", "", false
	}
	if !reasonOK || len(reasonValues) != 1 {
		http.Error(w, "motivo da reversão é obrigatório", http.StatusBadRequest)
		return "", "", false
	}

	rawPaymentID := strings.TrimSpace(paymentValues[0])
	parsedPaymentID, err := uuid.Parse(rawPaymentID)
	if err != nil || rawPaymentID == "" {
		http.Error(w, "pagamento de fatura inválido", http.StatusBadRequest)
		return "", "", false
	}
	reason := strings.TrimSpace(reasonValues[0])
	if reason == "" {
		http.Error(w, "motivo da reversão é obrigatório", http.StatusBadRequest)
		return "", "", false
	}
	if !validInvoicePaymentReversalReason(reason) {
		http.Error(w, "motivo da reversão inválido", http.StatusUnprocessableEntity)
		return "", "", false
	}
	return parsedPaymentID.String(), reason, true
}

func validInvoicePaymentReversalReason(reason string) bool {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > invoicePaymentReversalReasonMaxRunes {
		return false
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func writeInvoicePaymentReversalError(w http.ResponseWriter, h *FaturasHandler, paymentID string, err error) {
	switch {
	case errors.Is(err, errInvoicePaymentReversalNotFound):
		http.Error(w, "pagamento de fatura não encontrado", http.StatusNotFound)
	case errors.Is(err, errInvoicePaymentAlreadyReversed):
		http.Error(w, "pagamento de fatura já revertido", http.StatusConflict)
	case errors.Is(err, errInvoicePaymentReversalLinkConflict):
		http.Error(w, "estado do pagamento foi alterado; recarregue e tente novamente", http.StatusConflict)
	case invoicePaymentReversalSQLiteUnavailable(err):
		logInvoicePaymentReversalFailure(h, paymentID, "sqlite_unavailable")
		w.Header().Set("Retry-After", "1")
		http.Error(w, "serviço temporariamente indisponível", http.StatusServiceUnavailable)
	case errors.Is(err, errInvoicePaymentReversalStructural):
		logInvoicePaymentReversalFailure(h, paymentID, "structural")
		http.Error(w, "internal server error", http.StatusInternalServerError)
	case errors.Is(err, errInvoicePaymentReversalInvalidInput):
		logInvoicePaymentReversalFailure(h, paymentID, "primitive_invalid_input")
		http.Error(w, "internal server error", http.StatusInternalServerError)
	default:
		logInvoicePaymentReversalFailure(h, paymentID, "unexpected")
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func invoicePaymentReversalSQLiteUnavailable(err error) bool {
	var sqliteErr *sqliteDriver.Error
	if errors.As(err, &sqliteErr) {
		return invoicePaymentReversalSQLiteCodeUnavailable(sqliteErr.Code())
	}
	return false
}

func invoicePaymentReversalSQLiteCodeUnavailable(code int) bool {
	baseCode := code & 0xff
	return baseCode == sqlite3.SQLITE_BUSY || baseCode == sqlite3.SQLITE_LOCKED
}

func logInvoicePaymentReversalFailure(h *FaturasHandler, paymentID, class string) {
	slog.Error("invoice payment reversal failed",
		"class", class,
		"workspace_id", strings.TrimSpace(h.WorkspaceID),
		"actor_user_id", strings.TrimSpace(h.UserID),
		"invoice_payment_id", strings.TrimSpace(paymentID),
	)
}
