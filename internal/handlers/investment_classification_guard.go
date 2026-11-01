package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/contabase-app/contabase/internal/services"
)

var errInvestmentClassificationProtected = errors.New("lançamento vinculado a investimento exige operação financeira específica")

func classificationMutationGuard(query interface {
	QueryRow(string, ...interface{}) *sql.Row
}, workspace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := []interface{}{workspace, services.InvestmentPurchaseTransferPrefix}
	args = appendStrings(args, ids)
	var count int
	err := query.QueryRow(`SELECT COUNT(*) FROM transactions WHERE workspace_id=? AND (effect_mode='CLASSIFICATION_ONLY' OR substr(id,1,20)=?) AND id IN (`+sqlPlaceholders(len(ids))+`)`, args...).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return errInvestmentClassificationProtected
	}
	return nil
}

// This early guard provides an explicit 409 even for otherwise invalid generic
// forms, for classification-only launches and for cancellation-protected
// transfers. The bulk and writer paths repeat this guard before balance effects.
func (h *TransactionHandler) rejectClassificationMutation(w http.ResponseWriter, ids []string) bool {
	err := protectedTransactionMutationGuard(h.DB, h.WorkspaceID, ids)
	if errors.Is(err, errInvestmentClassificationProtected) {
		http.Error(w, err.Error(), http.StatusConflict)
		return true
	}
	if err != nil {
		http.Error(w, "erro ao validar modalidade da transação", http.StatusInternalServerError)
		return true
	}
	return false
}
